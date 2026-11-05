package simfs

import (
	"errors"
	"io/fs"
	"sync"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// ErrKilled is the error of every operation of a Process after it has died.
var ErrKilled = errors.New("simfs: the process has been killed")

// Process is a process on a world that dies after a counted operation of the world, as a process that is killed with
// SIGKILL: its later operations fail with ErrKilled, and the locks that it holds are released, as the kernel releases
// the flocks of a process that dies. What it has written stays as it is, durable or in the page cache (Pending), for
// the other processes, which use the FSes of New, Sub and Crash. A process of HangAt instead hangs there, with its
// locks and everything else that it holds, until Release. Its operations are those of FS, of the files that FS opens
// and of its Subs.
type Process struct {
	fs      *FS
	at      int
	hang    bool
	hung    chan struct{}
	release chan struct{}
	once    sync.Once // closes release

	mu    sync.Mutex
	dead  bool
	locks []func() error // of the locks that it holds; nil once released
}

// KillAt returns a process on the world of s, with the view s, that is killed after the counted operation n of the
// world: when an operation of the process ends with n or more operations counted in the world, by any process, the
// process dies. If n operations have been counted, it is dead from the start.
func (s *FS) KillAt(n int) *Process {
	return &Process{fs: s, at: n, hung: make(chan struct{}), release: make(chan struct{}), dead: s.Counted() >= n}
}

// HangAt returns a process on the world of s, with the view s, that hangs after the counted operation n of the world:
// the operation of the process that ends with n or more operations counted blocks, after it has done what it does,
// until Release. While it blocks, Hung is closed, the process keeps its locks, and its other operations fail with
// ErrKilled, without changing the world. After Release, the operation returns what it has done, and the process is
// dead, with its locks still held, as a process that never goes on. HangAt panics if n operations have been counted.
func (s *FS) HangAt(n int) *Process {
	if s.Counted() >= n {
		panic("simfs: HangAt of an operation that has been counted")
	}
	return &Process{fs: s, at: n, hang: true, hung: make(chan struct{}), release: make(chan struct{})}
}

// FS returns the file system of the process.
func (p *Process) FS() vfs.FS {
	return &procFS{p: p, fs: p.fs}
}

// Hung returns a channel that is closed when the process hangs.
func (p *Process) Hung() <-chan struct{} {
	return p.hung
}

// Release lets a process of HangAt go on, dead: from its hang, or, if it has not reached it, without hanging there.
// Release may be called more than once.
func (p *Process) Release() {
	p.once.Do(func() { close(p.release) })
}

// begin starts an operation of p.
func (p *Process) begin() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead {
		return ErrKilled
	}
	return nil
}

// end ends an operation of p, after which p dies, or hangs, if the world has counted p.at operations.
func (p *Process) end() {
	if p.fs.Counted() < p.at {
		return
	}
	p.mu.Lock()
	if p.dead {
		p.mu.Unlock()
		return
	}
	p.dead = true
	locks := p.locks
	p.locks = nil
	p.mu.Unlock()
	if p.hang {
		close(p.hung)
		<-p.release
		return
	}
	for _, unlock := range locks {
		if unlock != nil {
			_ = unlock()
		}
	}
}

func (p *Process) do(f func() error) error {
	if err := p.begin(); err != nil {
		return err
	}
	defer p.end()
	return f()
}

// procFS is the view fs of the world as the process p uses it.
type procFS struct {
	p  *Process
	fs *FS
}

func (f *procFS) Name() string { return f.fs.Name() }

func (f *procFS) Open(name string) (vfs.File, error) {
	if err := f.p.begin(); err != nil {
		return nil, err
	}
	defer f.p.end()
	return f.file(f.fs.Open(name))
}

func (f *procFS) OpenFile(name string, flag int, perm fs.FileMode) (vfs.File, error) {
	if err := f.p.begin(); err != nil {
		return nil, err
	}
	defer f.p.end()
	return f.file(f.fs.OpenFile(name, flag, perm))
}

// file returns file, which the process has opened, or err.
func (f *procFS) file(file vfs.File, err error) (vfs.File, error) {
	if err != nil {
		return nil, err
	}
	return &procFile{p: f.p, f: file}, nil
}

func (f *procFS) Mkdir(name string, perm fs.FileMode) error {
	return f.p.do(func() error { return f.fs.Mkdir(name, perm) })
}

func (f *procFS) Remove(name string) error {
	return f.p.do(func() error { return f.fs.Remove(name) })
}

func (f *procFS) Rename(oldname, newname string) error {
	return f.p.do(func() error { return f.fs.Rename(oldname, newname) })
}

func (f *procFS) SyncDir(name string) error {
	return f.p.do(func() error { return f.fs.SyncDir(name) })
}

func (f *procFS) Close() error {
	return f.p.do(f.fs.Close)
}

func (f *procFS) Lstat(name string) (fs.FileInfo, error) {
	if err := f.p.begin(); err != nil {
		return nil, err
	}
	defer f.p.end()
	return f.fs.Lstat(name)
}

func (f *procFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if err := f.p.begin(); err != nil {
		return nil, err
	}
	defer f.p.end()
	return f.fs.ReadDir(name)
}

func (f *procFS) Sub(name string) (vfs.FS, error) {
	if err := f.p.begin(); err != nil {
		return nil, err
	}
	defer f.p.end()
	sub, err := f.fs.Sub(name)
	if err != nil {
		return nil, err
	}
	return &procFS{p: f.p, fs: sub.(*FS)}, nil
}

func (f *procFS) Lock(name string, mode vfs.LockMode) (func() error, error) {
	if err := f.p.begin(); err != nil {
		return nil, err
	}
	defer f.p.end()
	unlock, err := f.fs.Lock(name, mode)
	if err != nil {
		return nil, err
	}
	p := f.p
	p.mu.Lock()
	i := len(p.locks)
	p.locks = append(p.locks, unlock)
	p.mu.Unlock()
	return func() error {
		p.mu.Lock()
		if p.dead {
			p.mu.Unlock()
			return ErrKilled // the kill has released the lock, or the hang keeps it
		}
		u := p.locks[i]
		p.locks[i] = nil
		p.mu.Unlock()
		if u == nil {
			return &fs.PathError{Op: "close", Path: joinPath(f.fs.name, name), Err: fs.ErrClosed}
		}
		return u()
	}, nil
}

// procFile is a file that a process has opened.
type procFile struct {
	p *Process
	f vfs.File
}

func (f *procFile) Read(b []byte) (int, error) {
	if err := f.p.begin(); err != nil {
		return 0, err
	}
	defer f.p.end()
	return f.f.Read(b)
}

func (f *procFile) ReadAt(b []byte, off int64) (int, error) {
	if err := f.p.begin(); err != nil {
		return 0, err
	}
	defer f.p.end()
	return f.f.ReadAt(b, off)
}

func (f *procFile) Write(b []byte) (int, error) {
	if err := f.p.begin(); err != nil {
		return 0, err
	}
	defer f.p.end()
	return f.f.Write(b)
}

func (f *procFile) WriteAt(b []byte, off int64) (int, error) {
	if err := f.p.begin(); err != nil {
		return 0, err
	}
	defer f.p.end()
	return f.f.WriteAt(b, off)
}

func (f *procFile) Seek(offset int64, whence int) (int64, error) {
	if err := f.p.begin(); err != nil {
		return 0, err
	}
	defer f.p.end()
	return f.f.Seek(offset, whence)
}

func (f *procFile) Close() error { return f.p.do(f.f.Close) }
func (f *procFile) Sync() error  { return f.p.do(f.f.Sync) }

func (f *procFile) Truncate(size int64) error {
	return f.p.do(func() error { return f.f.Truncate(size) })
}

func (f *procFile) Stat() (fs.FileInfo, error) {
	if err := f.p.begin(); err != nil {
		return nil, err
	}
	defer f.p.end()
	return f.f.Stat()
}

func (f *procFile) ReadDir(n int) ([]fs.DirEntry, error) {
	if err := f.p.begin(); err != nil {
		return nil, err
	}
	defer f.p.end()
	return f.f.ReadDir(n)
}
