package dbserver

import (
	"context"
	"io/fs"
	"sync/atomic"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// defaultPulse is Options.Pulse when it is 0.
const defaultPulse = 5 * time.Second

// progressFS is the FS that the engine runs on. It adds 1 to moved after each call of the FS and of its files has
// returned, whether it failed or not, and passes each call down as it came: the engine has moved if moved has.
type progressFS struct {
	vfs.FS
	moved *atomic.Uint64
}

func (p *progressFS) wrap(f vfs.File, err error) (vfs.File, error) {
	p.moved.Add(1)
	if err != nil {
		return nil, err
	}
	return &progressFile{File: f, moved: p.moved}, nil
}

func (p *progressFS) Open(name string) (vfs.File, error) {
	return p.wrap(p.FS.Open(name))
}

func (p *progressFS) OpenFile(name string, flag int, perm fs.FileMode) (vfs.File, error) {
	return p.wrap(p.FS.OpenFile(name, flag, perm))
}

func (p *progressFS) Mkdir(name string, perm fs.FileMode) error {
	defer p.moved.Add(1)
	return p.FS.Mkdir(name, perm)
}

func (p *progressFS) Remove(name string) error {
	defer p.moved.Add(1)
	return p.FS.Remove(name)
}

func (p *progressFS) Rename(oldname, newname string) error {
	defer p.moved.Add(1)
	return p.FS.Rename(oldname, newname)
}

func (p *progressFS) Lstat(name string) (fs.FileInfo, error) {
	defer p.moved.Add(1)
	return p.FS.Lstat(name)
}

func (p *progressFS) ReadDir(name string) ([]fs.DirEntry, error) {
	defer p.moved.Add(1)
	return p.FS.ReadDir(name)
}

func (p *progressFS) SyncDir(name string) error {
	defer p.moved.Add(1)
	return p.FS.SyncDir(name)
}

func (p *progressFS) Sub(name string) (vfs.FS, error) {
	defer p.moved.Add(1)
	sub, err := p.FS.Sub(name)
	if err != nil {
		return nil, err
	}
	return &progressFS{FS: sub, moved: p.moved}, nil
}

func (p *progressFS) Lock(name string, mode vfs.LockMode) (func() error, error) {
	defer p.moved.Add(1)
	return p.FS.Lock(name, mode)
}

type progressFile struct {
	vfs.File
	moved *atomic.Uint64
}

func (f *progressFile) Read(b []byte) (int, error) {
	defer f.moved.Add(1)
	return f.File.Read(b)
}

func (f *progressFile) ReadAt(b []byte, off int64) (int, error) {
	defer f.moved.Add(1)
	return f.File.ReadAt(b, off)
}

func (f *progressFile) Write(b []byte) (int, error) {
	defer f.moved.Add(1)
	return f.File.Write(b)
}

func (f *progressFile) WriteAt(b []byte, off int64) (int, error) {
	defer f.moved.Add(1)
	return f.File.WriteAt(b, off)
}

func (f *progressFile) Seek(offset int64, whence int) (int64, error) {
	defer f.moved.Add(1)
	return f.File.Seek(offset, whence)
}

func (f *progressFile) Close() error {
	defer f.moved.Add(1)
	return f.File.Close()
}

func (f *progressFile) Sync() error {
	defer f.moved.Add(1)
	return f.File.Sync()
}

func (f *progressFile) Truncate(size int64) error {
	defer f.moved.Add(1)
	return f.File.Truncate(size)
}

func (f *progressFile) Stat() (fs.FileInfo, error) {
	defer f.moved.Add(1)
	return f.File.Stat()
}

func (f *progressFile) ReadDir(n int) ([]fs.DirEntry, error) {
	defer f.moved.Add(1)
	return f.File.ReadDir(n)
}

// pulser writes a pulse into the reply of a long request, which is open, each time the interval of the server has passed
// and the count that it watches has moved since the last time: that of the server while the request waits for its turn or
// the gate, and for a Commit, and the request's own once its own call of the engine has begun (useOwn). A timer writes it,
// so the session must not touch the reply from the Begin before pulse until end has returned, except in the callbacks that
// hold lock.
type pulser struct {
	c       *session
	t       *time.Timer
	src     *atomic.Uint64 // the count that fire compares; under c.pulseMu
	own     atomic.Uint64  // the steps of the engine calls of this request alone
	last    uint64
	abort   func() // called, once, by a write that failed; nil: the request goes on
	stopped bool   // set by end, or by a write that failed; under c.pulseMu
}

// progressCtx is the context that a Verify or an Events gives the engine. The engine calls Err before each record of the
// log that it reads and each object that it checks, so each call is a step of the request. Done is not counted: the
// engine selects on it while it waits, and a wait is no step.
type progressCtx struct {
	context.Context
	n *atomic.Uint64
}

func (x progressCtx) Err() error {
	x.n.Add(1)
	return x.Context.Err()
}

// ctx returns the context for the engine calls of the request, which count into its own count.
func (p *pulser) ctx() context.Context { return progressCtx{p.c.ctx, &p.own} }

// useOwn makes the pulser compare the count of the engine calls of its own request from now on, and not that of the server.
func (p *pulser) useOwn() {
	p.c.pulseMu.Lock()
	defer p.c.pulseMu.Unlock()
	p.src = &p.own
	p.last = p.own.Load()
}

// pulse starts the pulser of the reply that is open, which compares the count of the server until useOwn. end must be
// called, on every path of the request, before the reply is written to: it is safe to call it again. The timer, and not a
// goroutine that waits, keeps a request that is answered before the interval from paying for a goroutine and the wakeups of
// it. A pulse that cannot be written means that the connection is broken: abort, if it is not nil, ends the request that
// nobody will read the answer of. A Commit does not abort: its Update runs to its end.
func (c *session) pulse(abort func()) *pulser {
	p := &pulser{c: c, src: c.s.moved, abort: abort}
	p.last = p.src.Load()
	c.pulseMu.Lock() // fire reads p.t
	p.t = time.AfterFunc(c.s.pulse, p.fire)
	c.pulseMu.Unlock()
	return p
}

func (p *pulser) fire() {
	c := p.c
	c.pulseMu.Lock()
	defer c.pulseMu.Unlock()
	if p.stopped {
		return
	}
	if now := p.src.Load(); now != p.last {
		p.last = now
		if c.wr.Pulse() != nil {
			p.stopped = true
			if p.abort != nil {
				p.abort() // under c.pulseMu: it must not take it
			}
			return
		}
		_ = c.s.hook("reply:pulse")
	}
	p.t.Reset(c.s.pulse)
}

// end stops the pulser, and waits for a pulse that it is writing: none is written after end has returned.
func (p *pulser) end() {
	p.c.pulseMu.Lock()
	p.stopped = true
	p.c.pulseMu.Unlock()
	p.t.Stop()
}

// lock and unlock keep the pulser from writing a pulse in the middle of what the session writes while the pulser runs.
func (p *pulser) lock()   { p.c.pulseMu.Lock() }
func (p *pulser) unlock() { p.c.pulseMu.Unlock() }
