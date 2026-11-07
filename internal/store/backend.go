package store

import (
	"context"
	"fmt"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// backend keeps the documents, their versions, the objects and the revisions of a store. The shared code makes every
// check, and the errors of the checks; a backend only reads and writes. The store has one backend, the log backend
// (logbackend.go), and the tests compare it with a model (model_test.go).
type backend interface {
	// view and update run f in a transaction as View and Update do; update commits it if f returns nil.
	view(ctx context.Context, f func(backendTx) error) error
	update(ctx context.Context, f func(backendTx) error) error
	events(ctx context.Context, fn func(number uint64, data []byte) bool) error // as Store.events
	close() error
}

// indexTable is a table that the store keeps beside those of the documents (index.go): the summaries of the current
// documents and the references between them, which are derived from the documents, and the operations, which are not.
type indexTable int

const (
	indexSummaries indexTable = iota
	indexReferences
	indexOperations
	numIndexTables
)

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

	// The rows of the index tables, by key. indexScan calls fn with the keys that start with prefix, and their values,
	// in ascending order of the keys, until fn returns false; it scans the table as it is when it begins, also if fn
	// writes. A value that indexGet or indexScan returns must not be changed.
	indexGet(table indexTable, key []byte) ([]byte, bool, error)
	indexPut(table indexTable, key, value []byte) error
	indexDelete(table indexTable, key []byte) error
	indexScan(table indexTable, prefix []byte, fn func(key, value []byte) bool) error
	indexReady() (bool, error)  // false only for a store of the table schema 1, which has no index
	lastNumber() (int64, error) // the largest number that an entry has had; 0 if none
	setLastNumber(n int64) error
}

// versionErr is the error of a backend for the saved_at of the version seq, which does not parse.
func versionErr(seq int64, err error) error {
	return fmt.Errorf("version %d: %w", seq, err)
}

// revisionErr is the error of a backend for the saved_at of the revision id, which does not parse.
func revisionErr(id string, err error) error {
	return fmt.Errorf("revision %s: %w", id, err)
}
