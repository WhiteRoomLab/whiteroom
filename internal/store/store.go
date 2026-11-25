// Package store is the Content Store (3.4), which keeps the documents of a site with every version that has been
// saved, the revisions of the entries and the originals of the media. The tables, the objects and the originals are in a
// data directory that only the DB daemon (3.6) opens, and the store talks to the daemon through internal/dbclient: the
// tables and the objects are those of the engine of internal/logdb (3.5) in logdb/ of the data directory, with the tables
// of logbackend.go, and the originals are files in files/:
//
//	files/<2 hex>/<64 hex>   the originals, by the 64 lower case hex digits of their SHA-256, in directories named by
//	                         the first 2 of them
//	files/.tmp-<16 hex>      a temporary file of the daemon: an original being written; it stays only after a crash, and
//	                         the daemon removes it when it opens the data directory
//
// The files of the developers, such as the schema file and the theme, are not in the data directory.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// SettingsKind is the kind of the settings document (6.6.8) in the store; its ID is "".
const SettingsKind content.RefKind = "settings"

// ErrNoStore is the error of Open, OpenReader and Backup for a data directory that has no store in it: the daemon of a directory
// that whiteroom-db init has just made, whose first writer has not made the store yet.
var ErrNoStore = errors.New("not a store of whiteroom (no schema version)")

// ErrUnavailable is wrapped by the errors that the operations of a store return while the daemon cannot be reached, when the
// connection to it ends, or when the daemon sends no byte of a reply, or does not take a request, for the stall limit of the
// client (dbclient.Options.Stall; 30 seconds). A store whose daemon is back works again with no new Open.
var ErrUnavailable = dbclient.ErrUnavailable

// Store is the Content Store of the data directory of a DB daemon. Its methods may be called concurrently.
type Store struct {
	b        backend
	c        *dbclient.Client
	now      func() time.Time
	hook     func(point string) error // nil outside the tests, which make it fail at points such as "commit"
	readOnly bool                     // opened by OpenReader
}

// Create makes the store in the data directory that the daemon of d serves, which must be new: one that whiteroom-db init
// has just made, without a commit. It connects as the writer, and commits the schema and the record store.create in one
// commit. For a store that has commits, Create fails and writes nothing. While the store is open, no other process
// connects as the writer. ctx bounds Create alone: the connection, the Hello and the commit; the store that it returns
// does not depend on it.
func Create(ctx context.Context, d dbclient.Dialer) (*Store, error) {
	return createOn(ctx, d, time.Now, dbclient.Options{})
}

// createOn is Create with the clock now and the options o of the client.
func createOn(ctx context.Context, d dbclient.Dialer, now func() time.Time, o dbclient.Options) (*Store, error) {
	s, err := connect(ctx, d, dbwire.RoleWriter, now, o)
	if err != nil {
		return nil, err
	}
	if err := createLog(ctx, s.c, dialerName(d), s.now); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// Open opens the store that the daemon of d serves, as its writer. While it is open, Open in another process fails
// with an error that says that another process is connected as the writer, after the connection has been tried again
// for 2 seconds, and other processes read the store with OpenReader. A store of the table schema 1 to 9 gets the schema 10
// in one Update, with the record store.upgrade (7.4) and the rows of heads of its audit logs: one of the schema 1 gets
// its index too (it has none), and one of the schema 2 to 9 gets nothing else and has no document read; if that fails,
// so does Open, and the store is unchanged unless the error says that it is not known whether the commit was made: the
// next Open finds out. A daemon whose store has no schema is an error that wraps ErrNoStore. ctx bounds Open alone: the
// connection, the Hello, the check of the schema and the upgrade; the store that it returns does not depend on it.
func Open(ctx context.Context, d dbclient.Dialer) (*Store, error) {
	return openOn(ctx, d, dbwire.RoleWriter, time.Now, dbclient.Options{})
}

// OpenReader opens the store that the daemon of d serves to read it, in a process other than its writer, which may run
// or not. Its Views see the commits that are durable when they begin. It changes nothing: Update fails with "read-only
// transaction", and PutOriginal fails too. A store of the table schema 1 has no index until its writer opens it: a
// reader reads it, but Summary and the other reads of the index fail with ErrNoIndex. A store of the table schema 1 to 5
// has no audit log until its writer opens it: ReadAudit, AuditHeads and VerifyAudit fail with ErrNoAuditLog. ctx bounds
// OpenReader alone, as that of Open.
func OpenReader(ctx context.Context, d dbclient.Dialer) (*Store, error) {
	return openOn(ctx, d, dbwire.RoleReader, time.Now, dbclient.Options{})
}

// openOn connects as role and checks the schema, and upgrades an older one if the role is the writer.
func openOn(ctx context.Context, d dbclient.Dialer, role dbwire.Role, now func() time.Time, o dbclient.Options) (*Store, error) {
	s, err := connect(ctx, d, role, now, o)
	if err != nil {
		return nil, err
	}
	name := dialerName(d)
	if err := checkSchema(ctx, s.c, name); err != nil {
		_ = s.Close()
		return nil, err
	}
	if role == dbwire.RoleWriter {
		if err := upgradeLog(ctx, s.c, name, s.now); err != nil {
			_ = s.Close()
			return nil, err
		}
	}
	return s, nil
}

// connect opens a client of d as role, with the options o, and makes the store of it, with the clock now for the
// versions. It takes d, and closes it if it fails.
func connect(ctx context.Context, d dbclient.Dialer, role dbwire.Role, now func() time.Time, o dbclient.Options) (*Store, error) {
	c, err := dbclient.Open(ctx, d, role, o)
	if err != nil {
		return nil, err
	}
	return &Store{b: &logBackend{c: c}, c: c, now: now, readOnly: role == dbwire.RoleReader}, nil
}

// dialerName is the name of d in errors.
func dialerName(d dbclient.Dialer) string {
	if s, ok := d.(fmt.Stringer); ok {
		return s.String()
	}
	return fmt.Sprintf("%T", d)
}

func (s *Store) Close() error {
	return s.b.close()
}

// call calls the hook of the tests at point.
func (s *Store) call(point string) error {
	if s.hook == nil {
		return nil
	}
	return s.hook(point)
}

// Tx is a transaction of View or Update. It is valid only inside the function that they call with it, and not for
// concurrent use.
type Tx struct {
	b              backendTx
	readOnly, done bool
	savedAt        string // the time of the versions and the records that the Update saves, as saved_at has it
	records        int    // the audit records that the Update has added
}

// savedAtLayout is RFC 3339 with 9 digits of fractional seconds, in UTC, so that the order of the texts of saved_at is
// that of their times.
const savedAtLayout = "2006-01-02T15:04:05.000000000Z07:00"

// View runs f in a transaction that only reads, and that sees the store as it was when f first read it.
func (s *Store) View(ctx context.Context, f func(*Tx) error) error {
	return s.b.view(ctx, func(b backendTx) error {
		return (&Tx{b: b, readOnly: true}).run(f)
	})
}

// ErrUnrecorded is the error of an Update whose function has written something besides audit records and has added no
// record (7.4): the Update commits nothing. A change is made together with the record that tells of it.
var ErrUnrecorded = errors.New("the transaction writes but adds no audit record")

// Update runs f in a transaction that writes, and commits it if f returns nil; otherwise, nothing that f has written
// stays. The versions that f saves get the time when the transaction began. Updates run one at a time: Update waits
// for the others of s until ctx ends. f must not call Update of s, which would wait for itself. If f returns nil after
// it has written anything but records (Changed) and has added no record, Update fails with ErrUnrecorded and commits
// nothing; an Update that only adds records, and one that writes nothing, are as they were.
func (s *Store) Update(ctx context.Context, f func(*Tx) error) error {
	return s.b.update(ctx, func(b backendTx) error {
		t := &Tx{b: b, savedAt: s.now().UTC().Format(savedAtLayout)}
		if err := t.run(f); err != nil {
			return err
		}
		if t.records == 0 && t.Changed() {
			return ErrUnrecorded
		}
		return s.call("commit")
	})
}

// run calls f with t, which ends when f returns.
func (t *Tx) run(f func(*Tx) error) error {
	defer func() { t.done = true }()
	return f(t)
}

var (
	errReadOnly = errors.New("read-only transaction")
	errEnded    = errors.New("the transaction has ended")
)

// check returns why t cannot be used now, to write if write is true, or nil.
func (t *Tx) check(write bool) error {
	switch {
	case t.done:
		return errEnded
	case write && t.readOnly:
		return errReadOnly
	}
	return nil
}
