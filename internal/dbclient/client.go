// Package dbclient is the client of the DB daemon (concept section 3.6); internal/store keeps its tables through it.
//
// A Client has the shape of the engine's DB: View and Update run a function with a Tx that reads one snapshot of the
// daemon, and Update sends the writes of the function to the daemon as one Commit, after the function has returned.
// Reads go to the daemon, except those that the transaction can answer itself: it reads its own writes, and keeps what
// the daemon has answered for a key. GetMany, PrefetchObjects and PrefetchScans ask for many keys, objects and first pages
// of scans with a few round trips: the requests go out in windows, before their replies are read, and what comes back is
// kept for the reads that follow, which return what they would have returned without them. No error text of this
// package holds a key, a value or the bytes of an object.
package dbclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

var (
	ErrReadOnly = errors.New("read-only transaction")
	// ErrUnavailable is wrapped by the errors of a daemon that cannot be reached, of a connection that ends or that the
	// client cuts because the daemon moves no byte for Options.Stall, of a request that a closing daemon refuses, and of a
	// reader that a daemon at its limit of readers refuses.
	ErrUnavailable = errors.New("cannot reach the daemon")
	ErrClosed      = errors.New("the client is closed")

	errEnded = errors.New("the transaction has ended")
)

const (
	defaultReaders  = 16
	defaultDialWait = 2 * time.Second
	defaultStall    = 30 * time.Second
	dialEvery       = 50 * time.Millisecond
	// maxStallTick keeps a connection that stalls from being cut more than two ticks after its limit.
	maxStallTick = 500 * time.Millisecond
	// cleanupWait bounds a request that ends a transaction when its context has ended already.
	cleanupWait = 5 * time.Second
	// helloFrame is the limit of a frame until the daemon has said its own.
	helloFrame = 1 << 20
	// maxValue and maxObject bound what the daemon may send as a value, an event or an object.
	maxValue  = 1 << 28
	maxObject = 1 << 30
)

// A Dialer connects to the daemon as a role. It may also have String, its name in errors, Close, which Client.Close
// calls, and LockWriter, as SocketDir has. The connections that it returns must honor deadlines.
type Dialer interface {
	Dial(ctx context.Context, role dbwire.Role) (net.Conn, error)
}

// writerLocker is a Dialer that has a lock of the writer, which a Client of the writer holds from before its first
// connection until Close. LockWriter fails with an error that wraps vfs.ErrLocked while another holds it.
type writerLocker interface {
	LockWriter() (unlock func() error, err error)
}

// SocketDir is the Dialer of the sockets writer.sock and reader.sock in a directory.
type SocketDir string

func (s SocketDir) Dial(ctx context.Context, role dbwire.Role) (net.Conn, error) {
	name := dbwire.ReaderSocket
	if role == dbwire.RoleWriter {
		name = dbwire.WriterSocket
	}
	var d net.Dialer
	return d.DialContext(ctx, "unix", filepath.Join(string(s), name))
}

func (s SocketDir) String() string { return string(s) }

// LockWriter takes the lock of the file dbwire.WriterLock in the directory, which the daemon makes, so that the writer of
// the daemon is one process also while it has no connection: after a restart of the daemon, and after a connection has
// ended in a request. It opens the file by its path, which needs only the search permission of the directory, as the
// sockets do.
func (s SocketDir) LockWriter() (unlock func() error, err error) {
	return lockFile(filepath.Join(string(s), dbwire.WriterLock))
}

// Options are the options of Open; a zero field is its default.
type Options struct {
	// Readers is how many connections to the reader socket Views use at once; 0: 16.
	Readers int
	// DialWait is how long to try to connect, every 50 milliseconds, when the daemon cannot be reached or says that it is
	// busy, which a writer meets for a moment after another writer's connection has ended, and how long Open tries to take
	// the lock of the writer; 0: 2 seconds, negative: once.
	DialWait time.Duration
	// Stall is how long a Read or a Write of a connection may move no byte, while the request or the reply that it carries
	// is under the limit, before the connection is cut; 0: 30 seconds, negative: no limit. The wait for the reply to a
	// Verify, a Commit, an Events and the first frame of a Backup is not under it, nor is the time between Reads and Writes.
	Stall time.Duration
}

// A Client is connected to a daemon as a writer or as a reader. It is safe for concurrent use.
type Client struct {
	d    Dialer
	name string
	role dbwire.Role
	o    Options

	slots chan struct{} // one per View in progress
	turn  chan struct{} // held by the Update in progress
	done  chan struct{} // closed by Close

	mu        sync.Mutex
	closed    bool
	conns     map[*conn]struct{}
	idle      []*conn // reader connections that are not in use, with no snapshot
	closeOnce sync.Once
	closeErr  error

	watchStop chan struct{} // closed to end watchStalls; nil if the limit is off
	watchEnd  chan struct{} // closed when watchStalls has returned
	watchOnce sync.Once

	wconn   *conn        // the writer connection, for the holder of turn; nil if the last dial failed, dead if it has ended
	unlock  func() error // gives up the lock of the writer, if the client holds it
	dataDir dbwire.DirID
}

// Open connects to the daemon as role and says Hello. A writer first takes the lock of the writer, if d has one: while
// another process holds it, Open fails with the error that says that another process is connected as the writer. The
// Client owns d from the call on: Close closes it, and so does an Open that fails.
func Open(ctx context.Context, d Dialer, role dbwire.Role, o Options) (*Client, error) {
	closeDialer := func() {
		if cl, ok := d.(io.Closer); ok {
			_ = cl.Close()
		}
	}
	switch {
	case role != dbwire.RoleReader && role != dbwire.RoleWriter:
		closeDialer()
		return nil, fmt.Errorf("dbclient: cannot connect as the role %d", uint8(role))
	case o.Readers < 0:
		closeDialer()
		return nil, fmt.Errorf("dbclient: Readers is negative (%d)", o.Readers)
	}
	if o.Readers == 0 {
		o.Readers = defaultReaders
	}
	if o.DialWait == 0 {
		o.DialWait = defaultDialWait
	}
	if o.Stall == 0 {
		o.Stall = defaultStall
	}
	c := &Client{d: d, name: nameOf(d), role: role, o: o, conns: map[*conn]struct{}{},
		slots: make(chan struct{}, o.Readers), turn: make(chan struct{}, 1), done: make(chan struct{})}
	if o.Stall > 0 {
		c.watchStop, c.watchEnd = make(chan struct{}), make(chan struct{})
		go c.watchStalls()
	}
	if l, ok := d.(writerLocker); ok && role == dbwire.RoleWriter {
		unlock, err := c.lockWriter(ctx, l)
		if err != nil {
			c.endWatch()
			closeDialer()
			return nil, err
		}
		c.unlock = unlock
	}
	cn, err := c.connect(ctx, role)
	if err != nil {
		c.endWatch()
		if c.unlock != nil {
			_ = c.unlock()
		}
		closeDialer()
		return nil, err
	}
	c.dataDir = cn.hello.DataDir
	if role == dbwire.RoleWriter {
		c.wconn = cn
	} else {
		c.idle = append(c.idle, cn)
	}
	return c, nil
}

// lockWriter takes the lock of the writer of l, trying again every 50 milliseconds for Options.DialWait while another
// process holds it. A lock that cannot be taken otherwise, as one that is missing until the daemon has started once, is
// tried again too, but the error is that of a connection to the daemon, if that fails: why it cannot be reached, or its
// refusal, which is not tried again. If the connection is made, the daemon has no lock to take, which a daemon that has
// started does not make later: its error ends the wait.
func (c *Client) lockWriter(ctx context.Context, l writerLocker) (unlock func() error, err error) {
	deadline := time.Now().Add(max(c.o.DialWait, 0))
	for {
		unlock, err := l.LockWriter()
		if err == nil {
			return unlock, nil
		}
		retry := true
		if errors.Is(err, vfs.ErrLocked) {
			err = &busyError{name: c.name, err: err}
		} else if cn, again, cerr := c.attempt(ctx, dbwire.RoleWriter); cerr == nil {
			cn.close()
			err, retry = &ioError{c.name, dialFailure{err}}, false
		} else {
			err, retry = cerr, again
		}
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case !retry || !time.Now().Before(deadline):
			return nil, err
		}
		t := time.NewTimer(min(dialEvery, time.Until(deadline)))
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		}
	}
}

// DataDir returns the identity of the data directory of the daemon, as its first Hello told it: zero if it is unknown.
func (c *Client) DataDir() dbwire.DirID { return c.dataDir }

func nameOf(d Dialer) string {
	if s, ok := d.(fmt.Stringer); ok {
		return s.String()
	}
	return fmt.Sprintf("%T", d)
}

// Close closes every connection of the Client, which ends the transactions that are running, and the Dialer. A second call
// returns what the first returned.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		close(c.done)
		conns := make([]*conn, 0, len(c.conns))
		for cn := range c.conns {
			conns = append(conns, cn)
		}
		c.idle = nil
		c.mu.Unlock()
		for _, cn := range conns {
			cn.close()
		}
		c.endWatch()
		var errs []error
		if c.unlock != nil {
			errs = append(errs, c.unlock())
		}
		if cl, ok := c.d.(io.Closer); ok {
			errs = append(errs, cl.Close())
		}
		c.closeErr = errors.Join(errs...)
	})
	return c.closeErr
}

func (c *Client) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// Verify has the daemon check its store, on a reader connection. The daemon's error text is the error.
func (c *Client) Verify(ctx context.Context) error {
	return c.withReader(ctx, func(cn *conn) error {
		return cn.call(ctx, dbwire.KindVerify, nil, dbwire.ReadEmpty)
	})
}

// ioError is a failure of the connection or of the dial, as opposed to an answer of the daemon.
type ioError struct {
	name string
	err  error
}

func (e *ioError) Error() string {
	return e.name + ": " + ErrUnavailable.Error() + ": " + e.err.Error()
}
func (e *ioError) Unwrap() error        { return e.err }
func (e *ioError) Is(target error) bool { return target == ErrUnavailable }

// dialFailure has the text of a failed dial and none of its chain: the socket file of a daemon that has stopped is missing,
// which wraps fs.ErrNotExist, and a caller that tests for that would take a key or a document for missing.
type dialFailure struct{ err error }

func (e dialFailure) Error() string { return e.err.Error() }

// stallError is the failure of a connection that the client cut because the daemon moved no byte of a Read of a reply
// (write is false) or of a Write of a request (write is true) for the stall limit. It is not an *ioError: a request that
// is sent again to a daemon that does not answer waits as long again.
type stallError struct {
	name  string
	stall time.Duration
	write bool
}

func (e *stallError) Error() string {
	what := "sent"
	if e.write {
		what = "read"
	}
	return e.name + ": " + ErrUnavailable.Error() + ": the daemon has " + what + " nothing for " + e.stall.String()
}
func (e *stallError) Is(target error) bool { return target == ErrUnavailable }

// refusedError is the refusal busy of a request, which the daemon sends while it shuts down and ends the connection after,
// or of the Hello of a reader that it has no place for. It has the text of the daemon, and is not an *ioError: a request
// that is sent again to the same daemon is refused again.
type refusedError struct {
	name string
	err  *dbwire.RemoteError
}

func (e *refusedError) Error() string {
	return e.name + ": " + ErrUnavailable.Error() + ": " + e.err.Error()
}
func (e *refusedError) Unwrap() error        { return e.err }
func (e *refusedError) Is(target error) bool { return target == ErrUnavailable }

// busyError is the refusal of a writer, which another writer's connection, or the lock of another process, keeps out.
type busyError struct {
	name string
	err  error
}

func (e *busyError) Error() string { return e.name + ": another process is connected as the writer" }
func (e *busyError) Unwrap() error { return e.err }

// lastWriterError is the refusal busy of a writer that holds the lock of the writer: the connection that keeps it out is
// not that of another process that takes the lock, but the last one of this client, or of a writer that has just ended,
// whose session the daemon has not ended yet, as while its Commit waits for a Verify.
type lastWriterError struct {
	name string
	err  *dbwire.RemoteError
}

func (e *lastWriterError) Error() string {
	return e.name + ": " + ErrUnavailable.Error() + ": the last connection of the writer has not ended yet"
}
func (e *lastWriterError) Unwrap() error        { return e.err }
func (e *lastWriterError) Is(target error) bool { return target == ErrUnavailable }

// conn is a connection after its Hello. One goroutine at a time uses it.
type conn struct {
	c     *Client
	nc    net.Conn // a *watchedConn, unless the limit of Options.Stall is off
	rd    *dbwire.Reader
	wr    *dbwire.Writer
	hello dbwire.HelloReply
	dead  atomic.Bool
	once  sync.Once

	watch   atomic.Bool   // the Reads and Writes that begin now are under the limit
	state   atomic.Uint64 // see watchedConn
	stalled atomic.Int32  // the direction of the Read or Write that watchStalls found with no byte moved, as in state
}

// The directions of a Read or a Write under the limit, in the low bits of the state of a conn.
const (
	ioNone  = 0
	ioRead  = 1
	ioWrite = 2
)

// watchedConn counts the Reads and Writes of a connection that are under the limit of Options.Stall, so that watchStalls
// can tell a connection that moves no byte from one that does, with no clock and no deadline of the request. The state of
// the conn is the number of the Reads and Writes that have begun and ended, in the bits above the two low ones, and the
// direction of the one that is in progress, in the two low ones: it is never the same twice.
type watchedConn struct {
	net.Conn
	cn *conn
}

func (w *watchedConn) Read(p []byte) (int, error) {
	if !w.cn.watch.Load() {
		return w.Conn.Read(p)
	}
	w.cn.state.Add(4 + ioRead)
	n, err := w.Conn.Read(p)
	w.cn.state.Add(4 - ioRead)
	return n, err
}

func (w *watchedConn) Write(p []byte) (int, error) {
	if !w.cn.watch.Load() {
		return w.Conn.Write(p)
	}
	w.cn.state.Add(4 + ioWrite)
	n, err := w.Conn.Write(p)
	w.cn.state.Add(4 - ioWrite)
	return n, err
}

// endWatch ends watchStalls, and waits for it.
func (c *Client) endWatch() {
	if c.watchStop == nil {
		return
	}
	c.watchOnce.Do(func() { close(c.watchStop) })
	<-c.watchEnd
}

// watchStalls cuts a connection when the same Read or Write under the limit is still there at the tick that is more than the
// limit in ticks (rounded up) after the one that first saw it. A tick is a sixteenth of the limit, at least a millisecond
// and at most maxStallTick, and the ticks are its own: a process that was stopped, or a tick that comes late, makes it cut
// later, never earlier. So a connection is cut no earlier than the limit after the Read or the Write began, and at most two
// ticks later when the limit is a multiple of a tick, as the default is.
func (c *Client) watchStalls() {
	defer close(c.watchEnd)
	tick := min(max(c.o.Stall/16, time.Millisecond), maxStallTick)
	limit := int(c.o.Stall / tick)
	if c.o.Stall%tick != 0 {
		limit++
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	type sighting struct {
		state uint64
		ticks int    // the ticks after the first that saw state
		round uint64 // of the last tick that saw it
	}
	seen := map[*conn]*sighting{}
	var conns []*conn
	for round := uint64(1); ; round++ {
		select {
		case <-c.watchStop:
			return
		case <-t.C:
		}
		c.mu.Lock()
		conns = conns[:0]
		for cn := range c.conns {
			conns = append(conns, cn)
		}
		c.mu.Unlock()
		for _, cn := range conns {
			state := cn.state.Load()
			if state&3 == ioNone {
				continue
			}
			g := seen[cn]
			switch {
			case g == nil:
				g = &sighting{state: state}
				seen[cn] = g
			case g.state != state:
				g.state, g.ticks = state, 0
			default:
				g.ticks++
			}
			g.round = round
			if g.ticks > limit {
				delete(seen, cn)
				// The error is stored before the connection ends, which fails the Read or the Write.
				cn.stalled.Store(int32(state & 3))
				cn.close()
			}
		}
		for cn, g := range seen {
			if g.round != round {
				delete(seen, cn)
			}
		}
	}
}

func (cn *conn) close() {
	cn.dead.Store(true)
	cn.once.Do(func() {
		_ = cn.nc.Close()
		cn.c.mu.Lock()
		delete(cn.c.conns, cn)
		cn.c.mu.Unlock()
	})
}

// connect dials as role, trying again for Options.DialWait.
func (c *Client) connect(ctx context.Context, role dbwire.Role) (*conn, error) {
	return c.connectWait(ctx, role, c.o.DialWait)
}

func (c *Client) connectWait(ctx context.Context, role dbwire.Role, wait time.Duration) (*conn, error) {
	var deadline time.Time
	if wait > 0 {
		deadline = time.Now().Add(wait)
	}
	for {
		cn, retry, err := c.attempt(ctx, role)
		if err == nil {
			return cn, nil
		}
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case c.isClosed():
			return nil, ErrClosed
		case !retry || wait <= 0 || !time.Now().Before(deadline):
			return nil, err
		}
		t := time.NewTimer(min(dialEvery, time.Until(deadline)))
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-c.done:
			t.Stop()
			return nil, ErrClosed
		}
	}
}

// attempt dials once and says Hello. retry reports that it may work a moment later.
func (c *Client) attempt(ctx context.Context, role dbwire.Role) (cn *conn, retry bool, err error) {
	nc, err := c.d.Dial(ctx, role)
	if err != nil {
		return nil, true, &ioError{c.name, dialFailure{err}}
	}
	cn = &conn{c: c, nc: nc}
	if c.o.Stall > 0 {
		cn.nc = &watchedConn{Conn: nc, cn: cn}
	}
	cn.rd, cn.wr = dbwire.NewReader(cn.nc, helloFrame), dbwire.NewWriter(cn.nc, helloFrame)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = nc.Close()
		return nil, false, ErrClosed
	}
	c.conns[cn] = struct{}{}
	c.mu.Unlock()
	err = cn.call(ctx, dbwire.KindHello, func(e *dbwire.Enc) { e.HelloRequest() }, func(d *dbwire.Dec) (err error) {
		cn.hello, err = dbwire.ReadHelloReply(d)
		return err
	})
	if err == nil && cn.hello.Role != role {
		err = fmt.Errorf("%s: the daemon made the connection a %v, not a %v", c.name, cn.hello.Role, role)
	}
	if err == nil {
		cn.rd.SetMaxFrame(cn.hello.Frame)
		cn.wr.SetMaxFrame(cn.hello.Frame)
		return cn, false, nil
	}
	cn.close()
	var (
		re  *dbwire.RemoteError
		ioe *ioError
	)
	switch {
	case errors.As(err, &re) && re.Code == dbwire.CodeBusy:
		switch {
		case role == dbwire.RoleWriter && c.unlock != nil:
			return nil, true, &lastWriterError{c.name, re}
		case role == dbwire.RoleWriter:
			return nil, true, &busyError{c.name, re}
		}
		return nil, true, &refusedError{c.name, re}
	case errors.As(err, &ioe):
		return nil, true, err
	}
	return nil, false, err
}

// call sends a request of kind with the body that body writes, reads its reply, and gives the body of it to read, which
// reads it to the end. An error that is not the daemon's answer ends the connection. The error is the *dbwire.RemoteError
// of the daemon's refusal, the error of ctx, ErrClosed, an error of the protocol, or an *ioError.
func (cn *conn) call(ctx context.Context, kind dbwire.Kind, body func(*dbwire.Enc), read func(*dbwire.Dec) error) error {
	if cn.dead.Load() {
		return cn.failure(ctx, errors.New("the connection has ended"))
	}
	stop := context.AfterFunc(ctx, func() { _ = cn.nc.SetDeadline(time.Unix(1, 0)) })
	err := cn.exchange(kind, body, read)
	if !stop() {
		cn.close() // the deadline may be set, or about to be
	}
	if err == nil {
		return nil
	}
	if re, ok := errors.AsType[*dbwire.RemoteError](err); ok {
		if re.Code == dbwire.CodeBusy && kind != dbwire.KindHello {
			// The daemon is closing, and ends the connection after it has refused the request.
			cn.close()
			return &refusedError{cn.c.name, re}
		}
		return err
	}
	cn.close()
	return cn.failure(ctx, err)
}

// failure returns the error to give for err, which has ended the connection.
func (cn *conn) failure(ctx context.Context, err error) error {
	if le, ok := errors.AsType[*localError](err); ok {
		return le.err
	}
	if re, ok := errors.AsType[*refusedError](err); ok {
		return re
	}
	switch {
	case errors.Is(err, errStop):
		return err
	case ctx.Err() != nil:
		return ctx.Err()
	case cn.c.isClosed():
		return ErrClosed
	case cn.stalled.Load() != ioNone:
		return &stallError{cn.c.name, cn.c.o.Stall, cn.stalled.Load() == ioWrite}
	case errors.Is(err, dbwire.ErrProtocol):
		return fmt.Errorf("%s: %w", cn.c.name, err)
	}
	return &ioError{cn.c.name, err}
}

// errStop ends a reply that its reader does not want to read to the end.
var errStop = errors.New("the reply is not read to its end")

// localError is a failure on the caller's side of a request, such as a reader or a directory that fails: it ends the
// connection, as the request is not done, and is returned as it is.
type localError struct{ err error }

func (e *localError) Error() string { return e.err.Error() }
func (e *localError) Unwrap() error { return e.err }

// exchange writes the request and reads the reply. The Reads and Writes are under the limit of Options.Stall, except the
// wait for the reply to a request that takes as long as the store is large (Verify and Commit, which waits for a Verify),
// the first frame of a Backup, and the whole reply of Events, whose frames come as the log is read.
func (cn *conn) exchange(kind dbwire.Kind, body func(*dbwire.Enc), read func(*dbwire.Dec) error) error {
	cn.watch.Store(true)
	defer cn.watch.Store(false)
	cn.wr.Begin(kind)
	e := &dbwire.Enc{W: cn.wr}
	if body != nil {
		body(e)
	}
	if e.Err != nil {
		return cn.refusedBefore(e.Err) // the message is left open, and the connection ends
	}
	if err := cn.wr.End(); err != nil {
		return cn.refusedBefore(err)
	}
	if kind == dbwire.KindVerify || kind == dbwire.KindCommit || kind == dbwire.KindEvents || kind == dbwire.KindBackup {
		cn.watch.Store(false)
	}
	if err := cn.rd.Reply(); err != nil {
		return err
	}
	if kind != dbwire.KindEvents {
		cn.watch.Store(true)
	}
	if read == nil {
		return nil
	}
	return read(&dbwire.Dec{R: cn.rd})
}

// refusedBefore returns the refusal of the daemon in place of err, the failed write of a request, if the daemon has closed
// the connection and has answered before that: it refuses an original at its first bytes, with the reason, and does not
// read the rest. Only then is there a reply to read, or the end of the stream: any other failure is returned as it is.
func (cn *conn) refusedBefore(err error) error {
	if !errors.Is(err, syscall.EPIPE) && !errors.Is(err, syscall.ECONNRESET) {
		return err
	}
	if re, ok := errors.AsType[*dbwire.RemoteError](cn.rd.Reply()); ok {
		cn.close()
		return re
	}
	return err
}

func (cn *conn) snapshot(ctx context.Context) (seq uint64, err error) {
	err = cn.call(ctx, dbwire.KindSnapshot, nil, func(d *dbwire.Dec) (err error) {
		seq, err = dbwire.ReadSnapshotReply(d)
		return err
	})
	return seq, err
}

func (cn *conn) release(ctx context.Context) error {
	return cn.call(ctx, dbwire.KindRelease, nil, dbwire.ReadEmpty)
}
