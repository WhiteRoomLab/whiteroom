// Package dbserver is the DB daemon without its process (concept section 3.6): it opens the data directory with the
// engine of internal/logdb and serves connections that speak internal/dbwire. It keeps the rules and the limits that
// the daemon keeps, adds the mark to every commit, and stops writing after a sync has failed. cmd/whiteroom-db runs it.
//
// Its peer may be a compromised admin server: a refusal leaves the store as it was, a limit holds against a peer that
// sends anything, and nothing of a connection (its snapshot, the objects that it staged, its goroutines) outlives it.
// No reply text and no log line holds bytes of a key, a value, an object or an event.
package dbserver

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/fspath"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// Options are the options of Create and Open.
type Options struct {
	// Log are the options of the engine; Open sets its Accept.
	Log logdb.Options
	// Limits are the limits of the daemon; a zero field is its default (dbwire.Limits.WithDefaults).
	Limits dbwire.Limits
	// Now is the clock of the marks; nil: time.Now.
	Now func() time.Time
	// FreeSpace returns the free bytes of the file system of the data directory; nil: no check.
	FreeSpace func() (uint64, error)
	// Hook is called at points of the server, for the tests: "commit:gate" (inside the commit gate, before the Update),
	// "backup:gate" (while a Backup holds the gate, before it knows its position), "backup:sending" (after it has let the
	// gate go), "original:synced" (after the sync of the temporary file of a PutOriginal), "original:renamed" (after
	// the sync of the directory of the original), "clock:tick" (by the goroutine of the clock of the deadlines, before each
	// reading of the time) and "reply:held" (when a request that has a reply in the buffer is still counted, before the
	// next one begins). Its error fails the operation; that of the last two is not used.
	Hook func(point string) error
	// Logf receives the lines that the daemon logs; nil drops them.
	Logf func(format string, args ...any)
}

type Server struct {
	name   string
	db     *logdb.DB
	logFS  vfs.FS // logdb/
	files  vfs.FS // files/
	limits dbwire.Limits
	o      Options
	now    func() time.Time
	bootID [16]byte
	bucket *bucket

	// gate is held by a Commit while its Update runs, so that a Backup can keep commits out while its reader opens.
	gate    sync.Mutex
	stopped atomic.Bool
	maxOps  int // the most ops of a Commit

	spaceStep int64 // how much of an original is written between two looks at the free space

	// The originals: the directories of files/ whose names are durable, as keys; syncs is read-locked by each sync of a
	// PutOriginal, and locked before a PutOriginal answers, so that one that ran beside a failed sync fails too.
	dirs  sync.Map
	syncs sync.RWMutex

	ctx    context.Context // ended by Close
	cancel context.CancelFunc

	// The deadlines of the connections come from a clock that one goroutine reads once a tick: coarse is its last reading,
	// in nanoseconds since started, and lateAt is the last reading that came more than two ticks after the one before.
	started time.Time
	tick    time.Duration
	coarse  atomic.Int64
	lateAt  atomic.Int64

	mu          sync.Mutex
	sessions    map[*session]struct{}
	writers     int // the sessions that have said Hello as the writer
	readers     int
	closing     bool
	draining    bool // Shutdown has begun: no new request
	requests    int  // the requests that sessions are in
	drained     chan struct{}
	drainedOnce sync.Once
	wg          sync.WaitGroup

	closeOnce sync.Once
	closeErr  error

	scanned atomic.Int64 // the rows that the scans of the engine have given the server
	onIdle  func()       // for the tests: a snapshot was released for being idle
}

// EngineOf returns the engine of s, for the tests.
func EngineOf(s *Server) *logdb.DB { return s.db }

// Create makes the data directory in root, which must be empty: files/ and logdb/ with an engine that has no commit.
// If it fails after making something, root must be emptied before Create can try again. name is the directory in
// errors.
func Create(root vfs.FS, name string, o Options) error {
	entries, err := root.ReadDir(".")
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%s: not empty; a new data directory needs a missing or empty directory", name)
	}
	for _, dir := range []string{"files", "logdb"} {
		if err := root.Mkdir(dir, 0o700); err != nil {
			return err
		}
	}
	if err := root.SyncDir("."); err != nil {
		return err
	}
	logFS, err := root.Sub("logdb")
	if err != nil {
		return err
	}
	o.Log.Accept = nil
	db, err := logdb.Create(logFS, o.Log)
	if err != nil {
		_ = logFS.Close()
		return err
	}
	return errors.Join(db.Close(), logFS.Close())
}

// Open opens the data directory in root as the writer of its engine. It refuses a store that has commits and no schema
// row, an invalid row, and a schema newer than dbwire.TableVersion, before the engine changes a file. It removes the
// temporary files that a daemon that was killed left in files/; if it cannot, it fails, and the store is as it was.
// name is the directory in errors. root stays the caller's: Close does not close it.
func Open(root vfs.FS, name string, o Options) (*Server, error) {
	if err := checkLimits(o.Limits); err != nil {
		return nil, err
	}
	for _, dir := range []string{"logdb", "files"} {
		if err := checkDir(root, name, dir); err != nil {
			return nil, err
		}
	}
	logFS, err := root.Sub("logdb")
	if err != nil {
		return nil, err
	}
	files, err := root.Sub("files")
	if err != nil {
		_ = logFS.Close()
		return nil, err
	}
	o.Limits = o.Limits.WithDefaults()
	o.Log.Accept = acceptSchema(fspath.Join(name, "logdb"))
	db, err := logdb.Open(logFS, o.Log)
	if err != nil {
		_ = logFS.Close()
		_ = files.Close()
		return nil, err
	}
	s := &Server{name: name, db: db, logFS: logFS, files: files, limits: o.Limits, o: o, now: o.Now,
		bucket: newBucket(o.Limits.CommitsPerSecond), sessions: map[*session]struct{}{}, maxOps: maxCommitOps, spaceStep: originalSpaceStep,
		drained: make(chan struct{}), started: time.Now()}
	if s.now == nil {
		s.now = time.Now
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.startClock()
	if _, err := rand.Read(s.bootID[:]); err != nil {
		_ = s.Close()
		return nil, err
	}
	// The writer lock is held: no temporary file of files/ is in use.
	if err := s.removeTemps(); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// maxTick keeps a limit of time at most two seconds late (four ticks, see timedConn).
const maxTick = 500 * time.Millisecond

// startClock starts the goroutine of the clock of the deadlines. A tick is a sixteenth of the shorter of the two limits of
// time that are on, at least a millisecond and at most maxTick.
func (s *Server) startClock() {
	var shorter time.Duration
	for _, d := range []time.Duration{s.limits.Stall, s.limits.SnapshotIdle} {
		if d > 0 && (shorter == 0 || d < shorter) {
			shorter = d
		}
	}
	if shorter == 0 {
		return
	}
	s.tick = min(max(shorter/16, time.Millisecond), maxTick)
	s.coarse.Store(int64(time.Since(s.started)))
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(s.tick)
		defer t.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-t.C:
			}
			_ = s.hook("clock:tick")
			now := int64(time.Since(s.started))
			if now-s.coarse.Load() > 2*int64(s.tick) {
				s.lateAt.Store(now) // before the time: whoever then loads the time, and lateAt after it, sees that it was late
			}
			s.coarse.Store(now)
		}
	}()
}

func checkLimits(l dbwire.Limits) error {
	for _, f := range []struct {
		name string
		v    int64
	}{{"Frame", int64(l.Frame)}, {"Commit", l.Commit}, {"Object", l.Object}, {"Staged", l.Staged}, {"Readers", int64(l.Readers)}} {
		if f.v < 0 {
			return fmt.Errorf("the limit %s is negative (%d)", f.name, f.v)
		}
	}
	if l.Frame > dbwire.MaxFrame {
		return fmt.Errorf("the limit Frame is %d, over %d", l.Frame, dbwire.MaxFrame)
	}
	return nil
}

func checkDir(root vfs.FS, name, dir string) error {
	p := fspath.Join(name, dir)
	info, err := root.Lstat(dir)
	var pe *fs.PathError
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%s: missing directory", p)
	case errors.As(err, &pe):
		return fmt.Errorf("%s: cannot read: %w", p, pe.Err)
	case err != nil:
		return err
	case info.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("%s: symbolic links are not allowed", p)
	case !info.IsDir():
		return fmt.Errorf("%s: must be a directory", p)
	}
	return nil
}

// acceptSchema returns the Accept of the engine of the store whose logdb/ is at name: a store that has commits needs
// a valid schema row of a version that this program knows.
func acceptSchema(name string) func(*logdb.Tx) error {
	return func(tx *logdb.Tx) error {
		v, ok := tx.Get(dbwire.TableMeta, []byte(dbwire.MetaSchema))
		if !ok {
			if tx.Seq() == 0 {
				return nil
			}
			return fmt.Errorf("%s: not a store of whiteroom (no schema version)", name)
		}
		switch version, valid := schemaVersion(v); {
		case !valid:
			return fmt.Errorf("%s: meta %q: invalid value", name, dbwire.MetaSchema)
		case version > dbwire.TableVersion:
			return fmt.Errorf("%s: the store schema is of version %d, newer than this program knows (%d)", name,
				version, dbwire.TableVersion)
		}
		return nil
	}
}

// schemaVersion reads the value of the row meta/schema: one whole uvarint that is not 0.
func schemaVersion(v []byte) (version uint64, valid bool) {
	version, n := binary.Uvarint(v)
	return version, n > 0 && n == len(v) && version != 0
}

// ServeConn serves the connection conn, whose peer has the role, until it ends, and closes it.
func (s *Server) ServeConn(conn net.Conn, role dbwire.Role) {
	s.serveConn(conn, role, nil)
}

// serveConn is ServeConn for a peer that is denied if deny is not nil: its Hello is answered CodeDenied with the text of
// deny, and nothing changes.
func (s *Server) serveConn(conn net.Conn, role dbwire.Role, deny error) {
	c := s.newSession(conn, role)
	if c == nil {
		_ = conn.Close()
		return
	}
	c.deny = deny
	c.serve()
}

// Close closes the connections, waits for their sessions to end, and closes the engine. Every snapshot is released by
// then. A second call returns what the first returned.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()
		s.cancel()
		s.closeConns()
		s.wg.Wait()
		s.closeErr = errors.Join(s.db.Close(), s.logFS.Close(), s.files.Close())
	})
	return s.closeErr
}

func (s *Server) closeConns() {
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for c := range s.sessions {
		sessions = append(sessions, c)
	}
	s.mu.Unlock()
	for _, c := range sessions {
		_ = c.conn.Close()
	}
}

// beginRequest counts a request that a session is in, unless Shutdown has begun.
func (s *Server) beginRequest() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.draining {
		return false
	}
	s.requests++
	return true
}

// keepRequest reports whether a request whose reply is held may stay counted until the next request has begun, which is
// not so once Shutdown has begun: the reply is then written before the request ends.
func (s *Server) keepRequest() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.draining
}

func (s *Server) endRequest() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requests--; s.draining && s.requests == 0 {
		s.drainedOnce.Do(func() { close(s.drained) })
	}
}

// Shutdown refuses new requests, waits until no session is inside a request or ctx ends, and then closes the
// connections. Close still closes the engine.
func (s *Server) Shutdown(ctx context.Context) {
	s.mu.Lock()
	s.draining = true
	if s.requests == 0 {
		s.drainedOnce.Do(func() { close(s.drained) })
	}
	s.mu.Unlock()
	select {
	case <-s.drained:
	case <-ctx.Done():
	}
	s.closeConns()
}

func (s *Server) logf(format string, args ...any) {
	if s.o.Logf != nil {
		s.o.Logf(format, args...)
	}
}

func (s *Server) hook(point string) error {
	if s.o.Hook == nil {
		return nil
	}
	return s.o.Hook(point)
}

const stoppedText = "the store has stopped after a sync failed; restart the machine before starting the daemon again"

func (s *Server) stop(err error) {
	if s.stopped.CompareAndSwap(false, true) {
		s.logf("the store has stopped after a sync failed (%s); writes are refused until the daemon is started again "+
			"after a restart of the machine", safeText(err))
	}
}

func (s *Server) lastSeq() (seq uint64, err error) {
	err = s.db.View(s.ctx, func(tx *logdb.Tx) error {
		seq = tx.Seq()
		return nil
	})
	return seq, err
}
