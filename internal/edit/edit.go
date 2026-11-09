// Package edit saves the documents of a site in a store as the admin screen does (6.7): it normalizes and validates
// them, validates again the documents that a change makes invalid without changing them, checks them across the site as
// the build does (internal/siteplan), and updates the store and its index in one Update. The files of media are saved as
// originals before their documents, and their formats and sizes are read from their contents (internal/mediafile). Publish,
// Unpublish and Archive change the state of an entry with the same checks, and Publish redirects the old URL of an entry
// whose URL it changes. Schedule sets the time at which an entry that is not published is published, with the checks of a
// publish, and PublishScheduled publishes it then. A Result says whether an operation has changed what a build of the site
// publishes. Import imports a whole site that a Source reads, such as a site directory, into a store that has no documents.
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
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// ErrConflict is the error of an operation whose base is not the version that the store has: the document has changed
// since the caller read it.
var ErrConflict = errors.New("changed since it was read")

// Editor saves the documents of a site into a store. It is safe for concurrent use.
type Editor struct {
	store  *store.Store
	schema content.SiteFile
	theme  *theme.Theme
	now    func() time.Time
	ids    *content.IDSource
	notify func(Result)
	wrap   func(reader) reader // nil outside the tests, which make the reads of the index fail
}

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

// Op says who does an operation, and which one it is.
type Op struct {
	By string // the ID of the user (usr_), of which only the format is checked until there are accounts (#45)
	// ID is "op_" and a ULID that the caller makes, or "". With it, the operation is recorded with its request in
	// the Update that does it, so that a retry returns the result without doing it again; with another request, a
	// retry fails.
	ID string
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

// request is what a caller asked an operation to do, of which the record of an operation ID keeps the hash.
type request struct {
	name   string   // of the operation
	params []string // its arguments, other than the data
	data   []byte
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
		return checkOpID(op.ID)
	}
	return nil
}

// run does an operation, whose op has passed checkOp, in one Update, with the record of its operation ID: f makes its
// changes in the Update and returns its result, and if it returns an error, nothing stays.
func (e *Editor) run(ctx context.Context, op Op, q request, f func(*run) (Result, error)) (Result, error) {
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
					return fmt.Errorf("operation ID %q was used for another request", op.ID)
				}
				res, replayed = rec.Result.result(), true
				return nil
			}
		}
		var err error
		if res, err = f(newRun(e, ctx, tx, op.By)); err != nil {
			return err
		}
		if op.ID == "" {
			return nil
		}
		data, err := json.Marshal(record{Request: hash, Result: newRecordResult(res)})
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

// record is the value of an operation in the table of operations of the store: the hash of the request, and the result.
type record struct {
	Request string       `json:"request"`
	Result  recordResult `json:"result"`
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
