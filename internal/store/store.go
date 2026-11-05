// Package store is the Content Store (3.4), which keeps the documents of a site with every version that has been
// saved, the revisions of the entries and the originals of the media, in a data directory:
//
//	whiteroom.db             the SQLite database (internal/db), with whiteroom.db-wal and whiteroom.db-shm while open
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
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/db"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// SettingsKind is the kind of the settings document (6.6.8) in the store; its ID is "".
const SettingsKind content.RefKind = "settings"

// Store is the Content Store of a data directory. Its methods may be called concurrently.
type Store struct {
	b     backend
	files vfs.FS // files/
	now   func() time.Time
	hook  func(point string) error // nil outside the tests, which make it fail at points such as "commit"
}

// Create makes a data directory at dir, which must be missing or an empty directory, and opens its store. If it fails
// after making something in dir, Create needs dir emptied before it can try again.
func Create(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	if len(entries) > 0 {
		return nil, fmt.Errorf("%s: not empty; a new data directory needs a missing or empty directory", dir)
	}
	files := filepath.Join(dir, "files")
	if err := os.Mkdir(files, 0o700); err != nil {
		return nil, err
	}
	d, err := db.Create(filepath.Join(dir, "whiteroom.db"))
	if err != nil {
		return nil, err
	}
	root, err := vfs.OpenRoot(files)
	if err != nil {
		_ = d.Close()
		return nil, err
	}
	return &Store{b: &sqliteBackend{db: d}, files: root, now: time.Now}, nil
}

// Open opens the store of the data directory dir, which Create has made.
func Open(dir string) (*Store, error) {
	root, err := vfs.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	if err := checkEntry(root, dir, "whiteroom.db", false); err != nil {
		return nil, err
	}
	if err := checkEntry(root, dir, "files", true); err != nil {
		return nil, err
	}
	d, err := db.Open(filepath.Join(dir, "whiteroom.db"))
	if err != nil {
		return nil, err
	}
	files, err := root.Sub("files")
	if err != nil {
		_ = d.Close()
		return nil, err
	}
	return &Store{b: &sqliteBackend{db: d}, files: files, now: time.Now}, nil
}

// checkEntry checks that name in root, the data directory dir, is a directory if isDir is true, and a regular file if
// not, without following a symbolic link.
func checkEntry(root vfs.FS, dir, name string, isDir bool) error {
	path := filepath.Join(dir, name)
	info, err := root.Lstat(name)
	var pe *fs.PathError
	switch {
	case errors.Is(err, fs.ErrNotExist) && isDir:
		return fmt.Errorf("%s: missing directory", path)
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%s: missing file", path)
	case errors.As(err, &pe):
		return fmt.Errorf("%s: cannot read: %w", path, pe.Err)
	case err != nil:
		return err
	case info.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("%s: symbolic links are not allowed", path)
	case isDir && !info.IsDir():
		return fmt.Errorf("%s: must be a directory", path)
	case !isDir && !info.Mode().IsRegular():
		return fmt.Errorf("%s: must be a regular file", path)
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
// for the others of s until ctx ends, and for those of other processes up to 5 seconds. f must not call Update of s,
// which would wait for itself.
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
