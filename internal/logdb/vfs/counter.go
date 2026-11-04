package vfs

import (
	"io/fs"
	"sync/atomic"
)

// Counts are the calls that a Counter has counted, whether they failed or not, and the bytes that the writes wrote.
type Counts struct {
	Writes, BytesWritten, Syncs, DirSyncs, Renames, Removes, Mkdirs int64
}

// Counter is an FS that counts the writes, syncs and changes of names made through it, its files and the FSes that
// its Sub returns.
type Counter struct {
	fsys FS
	c    *counters
}

type counters struct {
	writes, bytesWritten, syncs, dirSyncs, renames, removes, mkdirs atomic.Int64
}

// Count returns a Counter of fsys.
func Count(fsys FS) *Counter {
	return &Counter{fsys: fsys, c: new(counters)}
}

func (c *Counter) Counts() Counts {
	return Counts{
		Writes:       c.c.writes.Load(),
		BytesWritten: c.c.bytesWritten.Load(),
		Syncs:        c.c.syncs.Load(),
		DirSyncs:     c.c.dirSyncs.Load(),
		Renames:      c.c.renames.Load(),
		Removes:      c.c.removes.Load(),
		Mkdirs:       c.c.mkdirs.Load(),
	}
}

func (c *Counter) Name() string {
	return c.fsys.Name()
}

func (c *Counter) Open(name string) (File, error) {
	return c.wrap(c.fsys.Open(name))
}

func (c *Counter) OpenFile(name string, flag int, perm fs.FileMode) (File, error) {
	return c.wrap(c.fsys.OpenFile(name, flag, perm))
}

func (c *Counter) wrap(f File, err error) (File, error) {
	if err != nil {
		return nil, err
	}
	return &countingFile{File: f, c: c.c}, nil
}

func (c *Counter) Mkdir(name string, perm fs.FileMode) error {
	c.c.mkdirs.Add(1)
	return c.fsys.Mkdir(name, perm)
}

func (c *Counter) Remove(name string) error {
	c.c.removes.Add(1)
	return c.fsys.Remove(name)
}

func (c *Counter) Rename(oldname, newname string) error {
	c.c.renames.Add(1)
	return c.fsys.Rename(oldname, newname)
}

func (c *Counter) Lstat(name string) (fs.FileInfo, error) {
	return c.fsys.Lstat(name)
}

func (c *Counter) ReadDir(name string) ([]fs.DirEntry, error) {
	return c.fsys.ReadDir(name)
}

func (c *Counter) SyncDir(name string) error {
	c.c.dirSyncs.Add(1)
	return c.fsys.SyncDir(name)
}

func (c *Counter) Sub(name string) (FS, error) {
	sub, err := c.fsys.Sub(name)
	if err != nil {
		return nil, err
	}
	return &Counter{fsys: sub, c: c.c}, nil
}

func (c *Counter) Lock(name string, mode LockMode) (unlock func() error, err error) {
	return c.fsys.Lock(name, mode)
}

func (c *Counter) Close() error {
	return c.fsys.Close()
}

type countingFile struct {
	File
	c *counters
}

func (f *countingFile) Write(p []byte) (int, error) {
	n, err := f.File.Write(p)
	f.c.writes.Add(1)
	f.c.bytesWritten.Add(int64(n))
	return n, err
}

func (f *countingFile) WriteAt(p []byte, off int64) (int, error) {
	n, err := f.File.WriteAt(p, off)
	f.c.writes.Add(1)
	f.c.bytesWritten.Add(int64(n))
	return n, err
}

func (f *countingFile) Sync() error {
	f.c.syncs.Add(1)
	return f.File.Sync()
}
