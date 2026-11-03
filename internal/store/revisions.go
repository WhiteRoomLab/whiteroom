package store

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// Revision is a revision of an entry, which never changes.
type Revision struct {
	Seq     int64     // the order of saving among the revisions in the store
	ID      string    // rev_
	Entry   string    // the ID of the entry (ent_)
	Hash    string    // of the JSON, as Object takes it
	SavedAt time.Time // in UTC
	SavedBy string    // the ID of the user (usr_)
	Data    []byte    // the JSON; nil from Revisions
}

// revisionName returns the name of the revision with id in errors, that of its file in a site directory (4.3).
func revisionName(id string) string {
	return "revisions/" + id + ".json"
}

// AddRevision adds data, the JSON of the revision with id of the entry entry, by the user by, and returns the revision.
// The entry must exist, and id must be new, even for the same data: revisions never change. data must decode as a
// revision, but is not validated against the site settings or the entry.
func (t *Tx) AddRevision(entry, id string, data []byte, by string) (Revision, error) {
	if err := t.check(true); err != nil {
		return Revision{}, err
	}
	if err := content.CheckID(content.RefEntry, entry); err != nil {
		return Revision{}, err
	}
	if err := content.CheckID(content.RefRevision, id); err != nil {
		return Revision{}, err
	}
	if err := content.CheckID(content.RefUser, by); err != nil {
		return Revision{}, err
	}
	file := revisionName(id)
	if err := decodeFile(content.SiteFile{Name: file, Data: data}, new(content.Revision).UnmarshalJSON); err != nil {
		return Revision{}, err
	}
	if _, err := t.current(content.RefEntry, entry); err != nil {
		return Revision{}, err
	}
	var exists bool
	const q = "SELECT EXISTS (SELECT 1 FROM revisions WHERE id = ?)"
	if err := t.tx.QueryRowContext(t.ctx, q, id).Scan(&exists); err != nil {
		return Revision{}, err
	}
	if exists {
		return Revision{}, existsError(file)
	}
	hash := hashOf(data)
	if err := t.addObject(hash, data); err != nil {
		return Revision{}, err
	}
	const insert = "INSERT INTO revisions (id, entry, object, saved_at, saved_by) VALUES (?, ?, ?, ?, ?)"
	res, err := t.tx.ExecContext(t.ctx, insert, id, entry, hash, t.savedAt, by)
	if err != nil {
		return Revision{}, err
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return Revision{}, err
	}
	savedAt, err := parseSavedAt(t.savedAt)
	if err != nil {
		return Revision{}, err
	}
	return Revision{Seq: seq, ID: id, Entry: entry, Hash: hash, SavedAt: savedAt, SavedBy: by, Data: data}, nil
}

// revisionColumns are the columns of a revision, r, that scanRevision reads.
const revisionColumns = "r.seq, r.id, r.entry, r.object, r.saved_at, r.saved_by"

// scanRevision reads a revision from the columns of revisionColumns of row, and the columns after them into more.
func scanRevision(row scanner, more ...any) (Revision, error) {
	var r Revision
	var savedAt string
	if err := row.Scan(append([]any{&r.Seq, &r.ID, &r.Entry, &r.Hash, &savedAt, &r.SavedBy}, more...)...); err != nil {
		return Revision{}, err
	}
	var err error
	if r.SavedAt, err = parseSavedAt(savedAt); err != nil {
		return Revision{}, fmt.Errorf("revision %s: %w", r.ID, err)
	}
	return r, nil
}

// Revision returns the revision with id, also after its entry has been deleted. If there is none, the error wraps
// fs.ErrNotExist.
func (t *Tx) Revision(id string) (Revision, error) {
	if err := t.check(false); err != nil {
		return Revision{}, err
	}
	if err := content.CheckID(content.RefRevision, id); err != nil {
		return Revision{}, err
	}
	const q = "SELECT " + revisionColumns + ", o.data FROM revisions AS r JOIN objects AS o ON o.hash = r.object " +
		"WHERE r.id = ?"
	var data []byte
	r, err := scanRevision(t.tx.QueryRowContext(t.ctx, q, id), &data)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Revision{}, notFoundError(revisionName(id))
	case err != nil:
		return Revision{}, err
	}
	if err := verify(r.Hash, data); err != nil {
		return Revision{}, err
	}
	r.Data = data
	return r, nil
}

// Revisions returns the revisions of the entry entry, also after it has been deleted, without their JSON, in the
// order of saving; none if it has none.
func (t *Tx) Revisions(entry string) ([]Revision, error) {
	if err := t.check(false); err != nil {
		return nil, err
	}
	if err := content.CheckID(content.RefEntry, entry); err != nil {
		return nil, err
	}
	const q = "SELECT " + revisionColumns + " FROM revisions AS r WHERE r.entry = ? ORDER BY r.seq"
	rows, err := t.tx.QueryContext(t.ctx, q, entry)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var revs []Revision
	for rows.Next() {
		r, err := scanRevision(rows)
		if err != nil {
			return nil, err
		}
		revs = append(revs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return revs, nil
}

// existsError is the error for a revision that exists, by its name in errors, such as "revisions/<ID>.json".
type existsError string

func (e existsError) Error() string { return string(e) + ": already exists" }

func (existsError) Unwrap() error { return fs.ErrExist }
