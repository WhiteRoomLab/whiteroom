// Package vfs is the file-system interface of internal/logdb and of the originals of internal/store. Its OS
// implementation is an *os.Root, and returns the errors of the os package unchanged; internal/logdb/simfs simulates
// one for the tests.
package vfs

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
)

// File is a file that an FS has opened, with the methods of *os.File that are used.
type File interface {
	io.Reader
	io.ReaderAt
	io.Writer
	io.WriterAt
	io.Seeker
	io.Closer
	Sync() error
	Truncate(size int64) error
	Stat() (fs.FileInfo, error)
	ReadDir(n int) ([]fs.DirEntry, error)
}

// LockMode is the mode of a lock of FS.Lock.
type LockMode int

const (
	Shared LockMode = iota
	Exclusive
)

// ErrLocked is the error of FS.Lock, in an *fs.PathError, when a lock that conflicts is held.
var ErrLocked = errors.New("locked by another process")

// FS is a directory tree in which names are relative to its directory and cannot leave it, as in an *os.Root.
type FS interface {
	Name() string
	Open(name string) (File, error)
	OpenFile(name string, flag int, perm fs.FileMode) (File, error)
	Mkdir(name string, perm fs.FileMode) error
	Remove(name string) error
	Rename(oldname, newname string) error
	Lstat(name string) (fs.FileInfo, error)
	ReadDir(name string) ([]fs.DirEntry, error) // sorted by name
	SyncDir(name string) error                  // opens name, syncs it and closes it
	Sub(name string) (FS, error)                // as Root.OpenRoot; Name returns the joined path
	// Lock locks the file name, which must exist, until unlock is called or the process ends. It never waits: if a
	// lock that conflicts is held, even one of this process, it fails with ErrLocked.
	Lock(name string, mode LockMode) (unlock func() error, err error)
	Close() error
}

// OpenRoot opens the directory dir with os.OpenRoot.
func OpenRoot(dir string) (FS, error) {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return NewRoot(r), nil
}

// NewRoot returns the FS of r. Its Close closes r.
func NewRoot(r *os.Root) FS {
	return &rootFS{r: r}
}

type rootFS struct {
	r *os.Root
}

func (f *rootFS) Name() string {
	return f.r.Name()
}

func (f *rootFS) Open(name string) (File, error) {
	return f.OpenFile(name, os.O_RDONLY, 0)
}

func (f *rootFS) OpenFile(name string, flag int, perm fs.FileMode) (File, error) {
	file, err := f.r.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err // not file: a nil *os.File in a File is not nil
	}
	return file, nil
}

func (f *rootFS) Mkdir(name string, perm fs.FileMode) error {
	return f.r.Mkdir(name, perm)
}

func (f *rootFS) Remove(name string) error {
	return f.r.Remove(name)
}

func (f *rootFS) Rename(oldname, newname string) error {
	return f.r.Rename(oldname, newname)
}

func (f *rootFS) Lstat(name string) (fs.FileInfo, error) {
	return f.r.Lstat(name)
}

// ReadDir is os.ReadDir in the root, which has no ReadDir.
func (f *rootFS) ReadDir(name string) ([]fs.DirEntry, error) {
	d, err := f.r.Open(name)
	if err != nil {
		return nil, err
	}
	entries, err := d.ReadDir(-1)
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return entries, err
}

func (f *rootFS) SyncDir(name string) error {
	d, err := f.r.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

func (f *rootFS) Sub(name string) (FS, error) {
	r, err := f.r.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	return &rootFS{r: r}, nil
}

func (f *rootFS) Close() error {
	return f.r.Close()
}
