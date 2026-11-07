package store

import (
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
// revision, but is not validated against the site settings or the entry. It updates the index (Summary).
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
	rev := new(content.Revision)
	if err := decodeFile(content.SiteFile{Name: file, Data: data}, rev.UnmarshalJSON); err != nil {
		return Revision{}, err
	}
	if _, err := t.current(content.RefEntry, entry); err != nil {
		return Revision{}, err
	}
	exists, err := t.b.revisionExists(id)
	if err != nil {
		return Revision{}, err
	}
	if exists {
		return Revision{}, existsError(file)
	}
	hash := hashOf(data)
	if err := t.b.addObject(hash, data); err != nil {
		return Revision{}, err
	}
	seq, err := t.b.addRevision(id, entry, hash, t.savedAt, by)
	if err != nil {
		return Revision{}, err
	}
	if err := t.indexRevision(entry, id, rev, data); err != nil {
		return Revision{}, err
	}
	savedAt, err := parseSavedAt(t.savedAt)
	if err != nil {
		return Revision{}, err
	}
	return Revision{Seq: seq, ID: id, Entry: entry, Hash: hash, SavedAt: savedAt, SavedBy: by, Data: data}, nil
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
	r, ok, err := t.b.revision(id)
	switch {
	case err != nil:
		return Revision{}, err
	case !ok:
		return Revision{}, notFoundError(revisionName(id))
	}
	if err := verify(r.Hash, r.Data); err != nil {
		return Revision{}, err
	}
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
	revs, err := t.b.revisions(entry)
	if err != nil {
		return nil, err
	}
	return revs, nil
}

// existsError is the error for a revision that exists, by its name in errors, such as "revisions/<ID>.json".
type existsError string

func (e existsError) Error() string { return string(e) + ": already exists" }

func (existsError) Unwrap() error { return fs.ErrExist }
