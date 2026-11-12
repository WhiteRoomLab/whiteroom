// Package edit saves the documents of a site in a store as the admin screen does (6.7): it normalizes and validates
// them, validates again the documents that a change makes invalid without changing them, checks them across the site as
// the build does (internal/siteplan), and updates the store and its index in one Update. The files of media are saved as
// originals before their documents, and their formats and sizes are read from their contents (internal/mediafile). Publish,
// Unpublish and Archive change the state of an entry with the same checks, and Publish redirects the old URL of an entry
// whose URL it changes. Schedule sets the time at which an entry that is not published is published, with the checks of a
// publish, and PublishScheduled publishes it then. A Result says whether an operation has changed what a build of the site
// publishes. Import imports a whole site that a Source reads, such as a site directory, into a store that has no documents.
// Each operation that succeeds adds one record to the activity log of the store (7.4) in the Update that does it. The
// record of an operation ID answers a retry for 24 hours: an operation looks, at most once an hour, for the older records
// and removes them in an Update of its own, with the record operations.expire.
package edit

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// ErrConflict is the error of an operation whose base is not the version that the store has: the document has changed
// since the caller read it.
var ErrConflict = errors.New("changed since it was read")

// ErrOperationReused is wrapped by the error of an operation whose ID (Op.ID) was used for another request.
var ErrOperationReused = errors.New("was used for another request")

// Editor saves the documents of a site into a store. It is safe for concurrent use.
type Editor struct {
	store  *store.Store
	schema content.SiteFile
	theme  *theme.Theme
	now    func() time.Time
	ids    *content.IDSource
	notify func(Result)
	wrap   func(reader) reader // nil outside the tests, which make the reads of the index fail

	expireMu     sync.Mutex
	expiredAt    time.Time    // when an operation last looked for the records of operation IDs to remove; zero before the first
	beforeExpire func() error // nil outside the tests, which make the Update that removes the records fail
}

const (
	// operationsKept is how long the record of an operation ID is kept, so that a retry within it returns the result.
	operationsKept = 24 * time.Hour
	// expireEvery is the least time between two looks for the records to remove.
	expireEvery = time.Hour
)

// New returns an Editor of s for the site that has the schema file schema, named as in a site directory
// (site/schema.yaml), and the theme th, which must not be nil.
func New(s *store.Store, schema content.SiteFile, th *theme.Theme) *Editor {
	return &Editor{store: s, schema: schema, theme: th, now: time.Now, ids: content.NewIDSource(rand.Reader)}
}

// Notify makes the Editor call f with the Result of each operation whose Update has committed, after the Update and
// outside it, in the goroutine of the caller of the operation, which waits for f: it should not block. An operation that
// saved nothing is one of them, with Publishes false; a retry that returns a recorded result (Op.ID) and an operation
// that failed are not. Notify must be called before the Editor is used.
func (e *Editor) Notify(f func(Result)) { e.notify = f }

// SetClock replaces the clock of the operations, which gives the times that they write (such as the creation time of an
// entry and the date of a post that is published without one) and the time part of the IDs that they make; the IDs keep
// growing whatever the clock does. SetClock must be called before the Editor is used.
func (e *Editor) SetClock(now func() time.Time) { e.now = now }

// Schema returns the schema file that New was given. The caller must not change its Data.
func (e *Editor) Schema() content.SiteFile { return e.schema }

// ThemeSettings returns the settings of the theme that New was given (theme.yaml), which revisions are validated against.
// The caller must not change them.
func (e *Editor) ThemeSettings() *content.ThemeSettings { return e.theme.Settings }

// Op says who does an operation, and which one it is.
type Op struct {
	By string // the ID of the user (usr_), whose format the Editor checks; the caller checks the account and its permission (6.7)
	// ID is "op_" and a ULID that the caller makes, or "". With it, the operation is recorded with its request in
	// the Update that does it, so that a retry returns the result without doing it again; with another request, a
	// retry fails with an error that wraps ErrOperationReused. The record is kept for 24 hours (operationsKept): a
	// retry after that does the operation as a new one.
	ID string
	// Check is the caller's check of the permission of By, or nil. The operation calls it once, in the Update that does the
	// operation, after it has looked up the record of ID (a retry that returns the recorded result does not call it) and
	// before it reads or writes anything else, so that the check and the change are one state. Its error is the error of
	// the operation, as it is, and nothing stays but the original of a medium that AddMedia and ReplaceMedia have saved
	// before the Update. The hash of the request of the record of ID does not have Check or EditHTML.
	Check func(tx *store.Tx) error
	// EditHTML says that By may add or change html blocks (html.edit, 7.3). If it is false, SaveRevision and
	// CreateTranslation refuse a revision with an html block that the previous revision does not have, with its ID and
	// html, when the site allows html blocks (allowHtml).
	EditHTML bool
}

// Result is what an operation has done.
type Result struct {
	Kind      content.RefKind
	ID        string         // of the document that the operation made, changed or deleted; "" for the settings
	Version   store.Version  // that it saved; the current one if it saved nothing, as when it added a revision to an entry
	Revision  string         // the revision that it added to an entry, published or scheduled
	Redirect  string         // the redirect that a publish has registered from the old URL of the entry
	Number    int            // the number of the entry that it made
	Warnings  content.Errors // problems that do not stop it, located as the build locates its own
	Publishes bool           // it has changed what a build of the site publishes, so that a new build is due
	Status    content.Status // of the entry after an operation that sets it (Publish, Unpublish, Archive, Schedule and PublishScheduled); "" for the others
}

// request is what a caller asked an operation to do, of which the record of an operation ID keeps the hash. The fields
// after data tell the record of the operation in the activity log, and are not part of the hash.
type request struct {
	name   string   // of the operation
	params []string // its arguments, other than the data
	data   []byte

	action    audit.Action
	base      int64 // the Seq of the version that the caller has read, which the record has as before; 0 if there is none
	noVersion bool  // the operation saves no version of its document, only a revision of it
}

// hash returns the SHA-256 of the request of the user by, with the length of each part before it so that no two
// requests are encoded alike.
func (q request) hash(by string) string {
	h := sha256.New()
	put := func(b []byte) {
		h.Write(binary.BigEndian.AppendUint64(nil, uint64(len(b))))
		h.Write(b)
	}
	put([]byte(by))
	put([]byte(q.name))
	put(binary.BigEndian.AppendUint64(nil, uint64(len(q.params))))
	for _, p := range q.params {
		put([]byte(p))
	}
	put(q.data)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// checkOp checks the formats of the user and of the operation ID of op, which every operation does before anything.
func checkOp(op Op) error {
	if err := content.CheckID(content.RefUser, op.By); err != nil {
		return err
	}
	if op.ID != "" {
		return CheckOperationID(op.ID)
	}
	return nil
}

// run does an operation, whose op has passed checkOp, in one Update, with its record in the activity log and the record of
// its operation ID: f makes its changes in the Update and returns its result, and if it returns an error, nothing stays.
// op.Check, if there is one, is called in the Update before f, unless the operation ID has a record.
func (e *Editor) run(ctx context.Context, op Op, q request, f func(*run) (Result, error)) (Result, error) {
	now := e.now()
	e.expireOperations(ctx, now)
	hash := ""
	if op.ID != "" {
		hash = q.hash(op.By)
	}
	var res Result
	replayed := false
	err := e.store.Update(ctx, func(tx *store.Tx) error {
		replayed = false
		if op.ID != "" {
			data, ok, err := tx.Operation(op.ID)
			if err != nil {
				return err
			}
			if ok {
				rec, err := decodeRecord(op.ID, data)
				if err != nil {
					return err
				}
				if rec.Request != hash {
					return fmt.Errorf("operation ID %q %w", op.ID, ErrOperationReused)
				}
				res, replayed = rec.Result.result(), true
				return nil
			}
		}
		if op.Check != nil {
			if err := op.Check(tx); err != nil {
				return err
			}
		}
		var err error
		r := newRun(e, ctx, tx, op.By)
		if res, err = f(r); err != nil {
			return err
		}
		// Changed is read before the record of the operation ID, which is a write too.
		if err := r.recordActivity(op, q, res, tx.Changed()); err != nil {
			return err
		}
		if op.ID == "" {
			return nil
		}
		data, err := json.Marshal(record{Request: hash, Result: newRecordResult(res), At: now.UTC()})
		if err != nil {
			return err
		}
		return tx.PutOperation(op.ID, data)
	})
	if err != nil {
		return Result{}, err
	}
	if e.notify != nil && !replayed {
		e.notify(res)
	}
	return res, nil
}

// expireOperations removes the records of operation IDs that were made operationsKept before now or earlier, and those
// that have no time, which an earlier program made, unless it has looked for them in the last expireEvery (it does at the
// first operation). It does so in an Update of its own, before that of the operation, which it does not make longer, and
// adds the record operations.expire if it has removed any. Its failure does not stop the operation: the records stay,
// and the next look is expireEvery later. A record that cannot be read stays.
func (e *Editor) expireOperations(ctx context.Context, now time.Time) {
	e.expireMu.Lock()
	due := e.expiredAt.IsZero() || now.Sub(e.expiredAt) >= expireEvery
	if due {
		e.expiredAt = now
	}
	e.expireMu.Unlock()
	if !due {
		return
	}
	cutoff := now.Add(-operationsKept)
	_ = e.store.Update(ctx, func(tx *store.Tx) error {
		if e.beforeExpire != nil {
			if err := e.beforeExpire(); err != nil {
				return err
			}
		}
		var expired []string
		if err := tx.Operations(func(id string, data []byte) bool {
			if rec, err := decodeRecord(id, data); err == nil && !rec.At.After(cutoff) {
				expired = append(expired, id)
			}
			return true
		}); err != nil {
			return err
		}
		if len(expired) == 0 {
			return nil
		}
		for _, id := range expired {
			if err := tx.DeleteOperation(id); err != nil {
				return err
			}
		}
		return tx.Record(audit.Record{
			Log: audit.ActivityLog, Actor: audit.System, Action: audit.OperationsExpire,
			Detail: map[string]string{"expired": strconv.Itoa(len(expired))},
		})
	})
}

// recordActivity adds the record of the operation q, whose result is res, to the activity log (7.4): it points at the
// documents and copies none. changed is whether the Update has saved anything. An operation of no user is the system's.
func (r *run) recordActivity(op Op, q request, res Result, changed bool) error {
	rec := audit.Record{
		Log: audit.ActivityLog, Actor: op.By, Action: q.action, Operation: op.ID, Changed: changed,
		Before: audit.Version{Seq: q.base}, Revision: res.Revision, Status: string(res.Status),
	}
	if res.Kind != "" {
		rec.Target = audit.Target{Kind: string(res.Kind), ID: res.ID}
	}
	if changed && !q.noVersion && res.Version.Seq != 0 {
		rec.After = audit.Version{Seq: res.Version.Seq, Hash: res.Version.Hash}
	}
	if rec.Actor == "" {
		rec.Actor = audit.System
		if r.by != "" {
			r.note("scheduledBy", r.by)
		}
	}
	if n := len(res.Warnings); n > 0 {
		r.note("warnings", strconv.Itoa(n))
	}
	if res.Publishes {
		r.note("publishes", "true")
	}
	if res.Redirect != "" {
		r.note("redirect", res.Redirect)
	}
	if res.Number > 0 {
		r.note("number", strconv.Itoa(res.Number))
	}
	rec.Detail = r.detail
	return r.tx.Record(rec)
}

// record is the value of an operation in the table of operations of the store: the hash of the request, the result and
// the time of the operation (UTC), which tells when the record can be removed. A record of an earlier program has no
// time, and is removed at the first look.
type record struct {
	Request string       `json:"request"`
	Result  recordResult `json:"result"`
	At      time.Time    `json:"at,omitzero"`
}

type recordResult struct {
	Kind      content.RefKind `json:"kind"`
	ID        string          `json:"id,omitempty"`
	Version   recordVersion   `json:"version"`
	Revision  string          `json:"revision,omitempty"`
	Redirect  string          `json:"redirect,omitempty"`
	Number    int             `json:"number,omitzero"`
	Publishes bool            `json:"publishes,omitzero"`
	Status    content.Status  `json:"status,omitempty"`
	Warnings  []recordProblem `json:"warnings,omitempty"`
}

type recordVersion struct {
	Seq     int64           `json:"seq"`
	Kind    content.RefKind `json:"kind"`
	ID      string          `json:"id,omitempty"`
	Hash    string          `json:"hash,omitempty"`
	SavedAt time.Time       `json:"savedAt"`
	SavedBy string          `json:"savedBy"`
}

type recordProblem struct {
	File   string `json:"file,omitempty"`
	Path   string `json:"path,omitempty"`
	Line   int    `json:"line,omitzero"`
	Column int    `json:"column,omitzero"`
	Msg    string `json:"msg"`
}

func newRecordResult(r Result) recordResult {
	v := r.Version
	rec := recordResult{
		Kind: r.Kind, ID: r.ID, Revision: r.Revision, Redirect: r.Redirect, Number: r.Number, Publishes: r.Publishes, Status: r.Status,
		Version: recordVersion{Seq: v.Seq, Kind: v.Kind, ID: v.ID, Hash: v.Hash, SavedAt: v.SavedAt, SavedBy: v.SavedBy},
	}
	for _, w := range r.Warnings {
		rec.Warnings = append(rec.Warnings, recordProblem{File: w.File, Path: w.Path, Line: w.Line, Column: w.Column, Msg: w.Msg})
	}
	return rec
}

func (r recordResult) result() Result {
	v := r.Version
	res := Result{
		Kind: r.Kind, ID: r.ID, Revision: r.Revision, Redirect: r.Redirect, Number: r.Number, Publishes: r.Publishes, Status: r.Status,
		Version: store.Version{Seq: v.Seq, Kind: v.Kind, ID: v.ID, Hash: v.Hash, SavedAt: v.SavedAt, SavedBy: v.SavedBy},
	}
	for _, w := range r.Warnings {
		res.Warnings = append(res.Warnings, &content.Error{File: w.File, Path: w.Path, Line: w.Line, Column: w.Column, Msg: w.Msg})
	}
	return res
}

func decodeRecord(id string, data []byte) (record, error) {
	var rec record
	if err := json.Unmarshal(data, &rec); err != nil {
		return record{}, fmt.Errorf("operation ID %q: its record cannot be read: %w", id, err)
	}
	return rec, nil
}
