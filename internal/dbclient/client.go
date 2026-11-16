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
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

var (
	ErrReadOnly = errors.New("read-only transaction")
	// ErrUnavailable is wrapped by the errors of a daemon that cannot be reached, and of a connection that ends.
	ErrUnavailable = errors.New("cannot reach the daemon")
	ErrClosed      = errors.New("the client is closed")

	errEnded = errors.New("the transaction has ended")
)

const (
	defaultReaders  = 16
	defaultDialWait = 2 * time.Second
	dialEvery       = 50 * time.Millisecond
	// cleanupWait bounds a request that ends a transaction when its context has ended already.
	cleanupWait = 5 * time.Second
	// helloFrame is the limit of a frame until the daemon has said its own.
	helloFrame = 1 << 20
	// maxValue and maxObject bound what the daemon may send as a value, an event or an object.
	maxValue  = 1 << 28
	maxObject = 1 << 30
)

// A Dialer connects to the daemon as a role. It may also have String, its name in errors, and Close, which Client.Close
// calls. The connections that it returns must honor deadlines.
type Dialer interface {
	Dial(ctx context.Context, role dbwire.Role) (net.Conn, error)
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

// Options are the options of Open; a zero field is its default.
type Options struct {
	// Readers is how many connections to the reader socket Views use at once; 0: 16.
	Readers int
	// DialWait is how long to try to connect, every 50 milliseconds, when the daemon cannot be reached or says that it is
	// busy, which a writer meets for a moment after another writer's connection has ended; 0: 2 seconds, negative: once.
	DialWait time.Duration
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

	wconn *conn // the writer connection, for the holder of turn
}

// Open connects to the daemon as role and says Hello. The Client owns d from the call on: Close closes it, and so does an
// Open that fails.
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
	c := &Client{d: d, name: nameOf(d), role: role, o: o, conns: map[*conn]struct{}{},
		slots: make(chan struct{}, o.Readers), turn: make(chan struct{}, 1), done: make(chan struct{})}
	cn, err := c.connect(ctx, role)
	if err != nil {
		closeDialer()
		return nil, err
	}
	if role == dbwire.RoleWriter {
		c.wconn = cn
	} else {
		c.idle = append(c.idle, cn)
	}
	return c, nil
}

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
		if cl, ok := c.d.(io.Closer); ok {
			c.closeErr = cl.Close()
		}
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

// busyError is the refusal of a writer, which another writer's connection keeps out.
type busyError struct {
	name string
	err  *dbwire.RemoteError
}

func (e *busyError) Error() string { return e.name + ": another process is connected as the writer" }
func (e *busyError) Unwrap() error { return e.err }

// conn is a connection after its Hello. One goroutine at a time uses it.
type conn struct {
	c     *Client
	nc    net.Conn
	rd    *dbwire.Reader
	wr    *dbwire.Writer
	hello dbwire.HelloReply
	dead  atomic.Bool
	once  sync.Once
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
	cn = &conn{c: c, nc: nc, rd: dbwire.NewReader(nc, helloFrame), wr: dbwire.NewWriter(nc, helloFrame)}
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
		if role == dbwire.RoleWriter {
			return nil, true, &busyError{c.name, re}
		}
		return nil, true, err
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
	if _, ok := errors.AsType[*dbwire.RemoteError](err); ok {
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
	switch {
	case errors.Is(err, errStop):
		return err
	case ctx.Err() != nil:
		return ctx.Err()
	case cn.c.isClosed():
		return ErrClosed
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

func (cn *conn) exchange(kind dbwire.Kind, body func(*dbwire.Enc), read func(*dbwire.Dec) error) error {
	cn.wr.Begin(kind)
	e := &dbwire.Enc{W: cn.wr}
	if body != nil {
		body(e)
	}
	if e.Err != nil {
		return e.Err // the message is left open, and the connection ends
	}
	if err := cn.wr.End(); err != nil {
		return err
	}
	if err := cn.rd.Reply(); err != nil {
		return err
	}
	if read == nil {
		return nil
	}
	return read(&dbwire.Dec{R: cn.rd})
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
