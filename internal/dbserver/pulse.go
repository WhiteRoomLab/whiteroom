package dbserver

import (
	"context"
	"io/fs"
	"strings"
	"sync/atomic"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// defaultPulse is Options.Pulse when it is 0.
const defaultPulse = 5 * time.Second

// progressFS is the FS that the reader of a Backup runs on. It adds 1 to moved after each call of the FS and of its files
// has returned, whether it failed or not, and passes each call down as it came: the reader has moved if moved has.
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

// writeFS is the FS that the engine runs on. After each write call of the engine has returned (OpenFile, Mkdir, Remove,
// Rename and SyncDir, and Write, WriteAt, Sync and Truncate of a file that it has opened with OpenFile), whether it failed
// or not, it adds 1 to the count that to points at, if any: that of the Commit whose Update holds the turn. The calls of
// the checkpoint, which the engine writes in the background, are not counted, by their names.
type writeFS struct {
	vfs.FS
	to *atomic.Pointer[atomic.Uint64]
}

// checkpointName reports whether name is one that only the checkpoint of the engine writes: its temporary file, the
// file, and the directory of the store, which it syncs.
func checkpointName(name string) bool {
	return name == "." || name == "checkpoint" || strings.HasPrefix(name, ".tmp-checkpoint-")
}

func step(to *atomic.Pointer[atomic.Uint64]) {
	if n := to.Load(); n != nil {
		n.Add(1)
	}
}

func (w *writeFS) stepFor(name string) {
	if !checkpointName(name) {
		step(w.to)
	}
}

func (w *writeFS) OpenFile(name string, flag int, perm fs.FileMode) (vfs.File, error) {
	f, err := w.FS.OpenFile(name, flag, perm)
	w.stepFor(name)
	if err != nil || checkpointName(name) {
		return f, err
	}
	return &writeFile{File: f, to: w.to}, nil
}

func (w *writeFS) Mkdir(name string, perm fs.FileMode) error {
	defer w.stepFor(name)
	return w.FS.Mkdir(name, perm)
}

func (w *writeFS) Remove(name string) error {
	defer w.stepFor(name)
	return w.FS.Remove(name)
}

func (w *writeFS) Rename(oldname, newname string) error {
	defer w.stepFor(oldname)
	return w.FS.Rename(oldname, newname)
}

func (w *writeFS) SyncDir(name string) error {
	defer w.stepFor(name)
	return w.FS.SyncDir(name)
}

func (w *writeFS) Sub(name string) (vfs.FS, error) {
	sub, err := w.FS.Sub(name)
	if err != nil {
		return nil, err
	}
	return &writeFS{FS: sub, to: w.to}, nil
}

type writeFile struct {
	vfs.File
	to *atomic.Pointer[atomic.Uint64]
}

func (f *writeFile) Write(b []byte) (int, error) {
	defer step(f.to)
	return f.File.Write(b)
}

func (f *writeFile) WriteAt(b []byte, off int64) (int, error) {
	defer step(f.to)
	return f.File.WriteAt(b, off)
}

func (f *writeFile) Sync() error {
	defer step(f.to)
	return f.File.Sync()
}

func (f *writeFile) Truncate(size int64) error {
	defer step(f.to)
	return f.File.Truncate(size)
}

// pulser writes a pulse into the reply of a long request, which is open, each time the interval of the server has passed
// and the request has moved since the last time: its own count, or, while it waits for the gate or the turn, that of the
// request that holds it, and so on along the chain. A timer writes it, so the session must not touch the reply from the
// Begin before pulse until end has returned, except in the callbacks that hold lock.
type pulser struct {
	c    *session
	t    *time.Timer
	own  atomic.Uint64                          // the steps of this request alone
	wait atomic.Pointer[atomic.Pointer[pulser]] // the holder that this request waits for: &Server.gateBy, &Server.turnBy, nil
	// Under c.pulseMu: the end of the chain and its count at the last look.
	who     *pulser
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

// maxChain bounds the walk along the holders: a Backup waits for a Commit, which waits for a Verify.
const maxChain = 4

// head returns the request at the end of the chain of waits from p, or false while a holder is between two requests.
func (p *pulser) head() (*pulser, bool) {
	q := p
	for range maxChain {
		slot := q.wait.Load()
		if slot == nil {
			break
		}
		h := slot.Load()
		if h == nil {
			return nil, false
		}
		q = h
	}
	return q, true
}

// hold makes p the holder of slot, which it has taken, and stops it waiting. A request changes its wait before it fills a
// slot, so that no one walks the chain back to a slot that it holds.
func (p *pulser) hold(slot *atomic.Pointer[pulser]) {
	p.wait.Store(nil)
	slot.Store(p)
}

// pulse starts the pulser of the reply that is open, which waits for the holder of wait (nil: none). end must be called,
// on every path of the request, before the reply is written to: it is safe to call it again. The timer, and not a goroutine
// that waits, keeps a request that is answered before the interval from paying for a goroutine and the wakeups of it. A
// pulse that cannot be written means that the connection is broken: abort, if it is not nil, ends the request that nobody
// will read the answer of. A Commit does not abort: its Update runs to its end.
func (c *session) pulse(abort func(), wait *atomic.Pointer[pulser]) *pulser {
	p := &pulser{c: c, abort: abort}
	p.wait.Store(wait)
	c.pulseMu.Lock() // fire reads p.t
	if who, ok := p.head(); ok {
		p.who, p.last = who, who.own.Load()
	} else {
		p.who = p // the holder is about to change: the first look sees whoever has it then
	}
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
	if who, ok := p.head(); ok {
		if now := who.own.Load(); who != p.who || now != p.last {
			p.who, p.last = who, now
			if c.wr.Pulse() != nil {
				p.stopped = true
				if p.abort != nil {
					p.abort() // under c.pulseMu: it must not take it
				}
				return
			}
			_ = c.s.hook("reply:pulse")
		}
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
