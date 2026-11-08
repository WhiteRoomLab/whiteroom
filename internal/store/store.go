// Package store is the Content Store (3.4), which keeps the documents of a site with every version that has been
// saved, the revisions of the entries and the originals of the media, in a data directory:
//
//	logdb/                   the directory of the engine of internal/logdb (3.5), with the tables of logbackend.go
//	files/<2 hex>/<64 hex>   the originals, by the 64 lower case hex digits of their SHA-256, in directories named by
//	                         the first 2 of them
//	files/.tmp-<16 hex>      an original being written, which stays only after a crash
//
// The directories have the mode 0700, and the files 0600. The files of the developers, such as the schema file and the
// theme, are not in it.
package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// SettingsKind is the kind of the settings document (6.6.8) in the store; its ID is "".
const SettingsKind content.RefKind = "settings"

// Store is the Content Store of a data directory. Its methods may be called concurrently.
type Store struct {
	b                backend
	files            vfs.FS       // files/
	dirs             sync.Map     // the directories of files/ whose names are durable, as keys
	syncs            sync.RWMutex // read-locked by each sync of PutOriginal, and locked before PutOriginal returns nil
	originalsStopped atomic.Bool  // a sync of PutOriginal has failed: errOriginalsStopped
	now              func() time.Time
	hook             func(point string) error // nil outside the tests, which make it fail at points such as "commit"
	readOnly         bool                     // opened by OpenReader or openLogReader
}

// options are those of the stores that the tests make with create, open and openLogReader: the engine of
// internal/logdb with *log, or with defaultLog if log is nil, whose Hook and Accept are those of the store
// (logbackend.go); the data directory is fs if it is not nil, in place of dir on the OS, which then only names it in
// errors; now is the clock of the versions (nil: time.Now).
type options struct {
	log *logdb.Options
	fs  vfs.FS
	now func() time.Time
}

// defaultLog are the options of the engine of Create, Open and OpenReader (3.4): objects of 64 KiB or more in files,
// and the defaults of the engine for the rest (segments and checkpoints every 64 MiB, 2 seconds for the writer lock).
var defaultLog = logdb.Options{Placement: logdb.Hybrid}

func (o options) engine() logdb.Options {
	if o.log != nil {
		return *o.log
	}
	return defaultLog
}

func (o options) clock() func() time.Time {
	if o.now != nil {
		return o.now
	}
	return time.Now
}

// Create makes a data directory at dir, which must be missing or an empty directory, and opens its store. If it fails
// after making something in dir, Create needs dir emptied before it can try again.
func Create(dir string) (*Store, error) {
	return create(dir, options{})
}

func create(dir string, o options) (*Store, error) {
	if err := newDataDir(dir, o.fs); err != nil {
		return nil, err
	}
	s := &Store{now: o.clock()}
	root, done, err := dataRoot(dir, o.fs)
	if err != nil {
		return nil, err
	}
	defer done()
	for _, name := range []string{"files", "logdb"} {
		if err := root.Mkdir(name, 0o700); err != nil {
			return nil, err
		}
	}
	if err := root.SyncDir("."); err != nil {
		return nil, err
	}
	b, err := createLog(root, s.logOptions(o.engine(), nil))
	if err != nil {
		return nil, err
	}
	return s.withFiles(root, b)
}

// newDataDir checks that dir, a new data directory, is empty: fsys if it is not nil, else the directory on the OS,
// which it makes if it is missing.
func newDataDir(dir string, fsys vfs.FS) error {
	var entries []fs.DirEntry
	var err error
	if fsys != nil {
		entries, err = fsys.ReadDir(".")
	} else if err = os.MkdirAll(dir, 0o700); err == nil {
		entries, err = os.ReadDir(dir)
	}
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%s: not empty; a new data directory needs a missing or empty directory", dir)
	}
	return nil
}

// dataRoot returns fsys, or if it is nil, the directory dir on the OS; done closes what dataRoot has opened.
func dataRoot(dir string, fsys vfs.FS) (root vfs.FS, done func(), err error) {
	if fsys != nil {
		return fsys, func() {}, nil
	}
	if root, err = vfs.OpenRoot(dir); err != nil {
		return nil, nil, err
	}
	return root, func() { _ = root.Close() }, nil
}

// logOptions returns o with accept, and with the hook of s, which the engine calls with "logdb:" before its points.
func (s *Store) logOptions(o logdb.Options, accept func(*logdb.Tx) error) logdb.Options {
	o.Hook = func(point string) error { return s.call("logdb:" + point) }
	o.Accept = accept
	return o
}

// withFiles gives s the backend b and files/ of root, its data directory. If it fails, it closes b.
func (s *Store) withFiles(root vfs.FS, b backend) (*Store, error) {
	files, err := root.Sub("files")
	if err != nil {
		_ = b.close()
		return nil, err
	}
	s.b, s.files = b, files
	return s, nil
}

// Open opens the store of the data directory dir, which Create has made, as its writer. While it is open, Open in
// another process fails after 2 seconds, and other processes read the store with OpenReader. A store of the table
// schema 1 or 2 gets the schema 3 in one Update, which makes its index again (the schema 1 has none); if that fails, so
// does Open, and the store is unchanged.
func Open(dir string) (*Store, error) {
	return open(dir, options{})
}

func open(dir string, o options) (*Store, error) {
	return openDir(dir, o, false)
}

// OpenReader opens the store of the data directory dir to read it, in a process other than its writer, which may run
// or not. Its Views see the commits that are durable when they begin. It changes no file of the store: Update fails
// with "read-only transaction", and PutOriginal fails too. A store of the table schema 1 has no index until its writer
// opens it: a reader reads it, but Summary and the other reads of the index fail with ErrNoIndex.
func OpenReader(dir string) (*Store, error) {
	return openLogReader(dir, options{})
}

// openLogReader is OpenReader with o.
func openLogReader(dir string, o options) (*Store, error) {
	return openDir(dir, o, true)
}

// openDir is open, or openLogReader if reader is true.
func openDir(dir string, o options, reader bool) (*Store, error) {
	root, done, err := dataRoot(dir, o.fs)
	if err != nil {
		return nil, err
	}
	defer done()
	for _, name := range []string{"logdb", "files"} {
		if err := checkDir(root, dir, name); err != nil {
			return nil, err
		}
	}
	s := &Store{now: o.clock(), readOnly: reader}
	logName := filepath.Join(dir, "logdb")
	b, err := openLog(root, s.logOptions(o.engine(), checkSchema(logName)), reader)
	if err != nil {
		return nil, err
	}
	if !reader {
		if err := upgradeLog(b, logName); err != nil {
			_ = b.close()
			return nil, err
		}
	}
	return s.withFiles(root, b)
}

// checkDir checks that name in root, the data directory dir, is a directory, without following a symbolic link.
func checkDir(root vfs.FS, dir, name string) error {
	path := filepath.Join(dir, name)
	info, err := root.Lstat(name)
	var pe *fs.PathError
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%s: missing directory", path)
	case errors.As(err, &pe):
		return fmt.Errorf("%s: cannot read: %w", path, pe.Err)
	case err != nil:
		return err
	case info.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("%s: symbolic links are not allowed", path)
	case !info.IsDir():
		return fmt.Errorf("%s: must be a directory", path)
	}
	return nil
}

func (s *Store) Close() error {
	return errors.Join(s.b.close(), s.files.Close())
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
	savedAt        string // the time of the versions that the Update saves, as saved_at has it
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

// Update runs f in a transaction that writes, and commits it if f returns nil; otherwise, nothing that f has written
// stays. The versions that f saves get the time when the transaction began. Updates run one at a time: Update waits
// for the others of s until ctx ends. f must not call Update of s, which would wait for itself.
func (s *Store) Update(ctx context.Context, f func(*Tx) error) error {
	return s.b.update(ctx, func(b backendTx) error {
		t := &Tx{b: b, savedAt: s.now().UTC().Format(savedAtLayout)}
		if err := t.run(f); err != nil {
			return err
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
