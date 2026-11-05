package store

import (
	"context"
	"fmt"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// backend keeps the documents, their versions, the objects and the revisions of a store. The shared code makes every
// check, and the errors of the checks; a backend only reads and writes.
type backend interface {
	// view and update run f in a transaction as View and Update do; update commits it if f returns nil.
	view(ctx context.Context, f func(backendTx) error) error
	update(ctx context.Context, f func(backendTx) error) error
	close() error
}

// backendTx is a transaction of a backend. Its methods return false for what is missing, and do not check data against
// its hash.
type backendTx interface {
	current(kind content.RefKind, id string) (Version, bool, error)
	addObject(hash string, data []byte) error // unless the store has it
	// addVersion and addRevision return the seq of what they add: one more than that of the last one, from 1.
	addVersion(kind content.RefKind, id, hash, savedAt, by string) (int64, error) // hash "" = deletion
	setCurrent(kind content.RefKind, id string, seq int64) error
	deleteCurrent(kind content.RefKind, id string) error
	document(kind content.RefKind, id string) (Document, bool, error)
	// documents returns the current versions of kind in the order of their IDs. With an error, it also returns those
	// that it has read before it, whose data Documents checks before it returns the error.
	documents(kind content.RefKind) ([]Document, error)
	history(kind content.RefKind, id string) ([]Version, error) // in the order of saving
	object(hash string) ([]byte, bool, error)
	revisionExists(id string) (bool, error)
	addRevision(id, entry, hash, savedAt, by string) (int64, error)
	revision(id string) (Revision, bool, error) // with Data
	revisions(entry string) ([]Revision, error) // in the order of saving, without Data
	addEvent(data []byte) error                 // an audit event (events.go)
}

// versionErr is the error of a backend for the saved_at of the version seq, which does not parse.
func versionErr(seq int64, err error) error {
	return fmt.Errorf("version %d: %w", seq, err)
}

// revisionErr is the error of a backend for the saved_at of the revision id, which does not parse.
func revisionErr(id string, err error) error {
	return fmt.Errorf("revision %s: %w", id, err)
}
