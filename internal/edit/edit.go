// Package edit saves the documents of a site in a store as the admin screen does (6.7): it normalizes and validates
// them, validates again the documents that a change makes invalid without changing them, checks them across the site as
// the build does (internal/siteplan), and updates the store and its index in one Update. The files of media are saved as
// originals before their documents, and their formats and sizes are read from their contents (internal/mediafile).
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
	ids    idSource
	wrap   func(reader) reader // nil outside the tests, which make the reads of the index fail
}

// New returns an Editor of s for the site that has the schema file schema, named as in a site directory
// (site/schema.yaml), and the theme th, which must not be nil.
func New(s *store.Store, schema content.SiteFile, th *theme.Theme) *Editor {
	return &Editor{store: s, schema: schema, theme: th, now: time.Now, ids: idSource{rand: rand.Reader}}
}

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
	Kind     content.RefKind
	ID       string         // of the document that the operation made, changed or deleted; "" for the settings
	Version  store.Version  // that it saved; the current one if it saved nothing, as when it added a revision to an entry
	Revision string         // the revision that it added to an entry
	Number   int            // the number of the entry that it made
	Warnings content.Errors // problems that do not stop it, located as the build locates its own
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
	err := e.store.Update(ctx, func(tx *store.Tx) error {
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
				res = rec.Result.result()
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
	return res, nil
}

// record is the value of an operation in the table of operations of the store: the hash of the request, and the result.
type record struct {
	Request string       `json:"request"`
	Result  recordResult `json:"result"`
}

type recordResult struct {
	Kind     content.RefKind `json:"kind"`
	ID       string          `json:"id,omitempty"`
	Version  recordVersion   `json:"version"`
	Revision string          `json:"revision,omitempty"`
	Number   int             `json:"number,omitzero"`
	Warnings []recordProblem `json:"warnings,omitempty"`
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
		Kind: r.Kind, ID: r.ID, Revision: r.Revision, Number: r.Number,
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
		Kind: r.Kind, ID: r.ID, Revision: r.Revision, Number: r.Number,
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
