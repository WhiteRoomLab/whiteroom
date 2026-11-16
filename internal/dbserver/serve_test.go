package dbserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// pipeListener is a net.Listener of in-memory connections: dial makes one, which Accept gives.
type pipeListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
	taken atomic.Int64 // the connections that Accept has given
	// accept, if it is not nil, is called first, and its error is the one of Accept.
	accept func() error
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn, 64), done: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	if l.accept != nil {
		if err := l.accept(); err != nil {
			return nil, err
		}
	}
	select {
	case c := <-l.conns:
		l.taken.Add(1)
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr{} }

// dial makes a connection, the other end of which is for Accept.
func (l *pipeListener) dial(t testing.TB) net.Conn {
	t.Helper()
	cc, sc := net.Pipe()
	l.conns <- sc
	t.Cleanup(func() { _ = cc.Close() })
	return cc
}

// serve runs Serve of the fixture with the listener, and ends it at the end of the test; the result is on the channel.
func (f *fixture) serve(l net.Listener, role dbwire.Role, check func(net.Conn) error) <-chan error {
	f.t.Helper()
	done, finished := make(chan error, 1), make(chan struct{})
	go func() {
		done <- f.srv.Serve(l, role, check)
		close(finished)
	}()
	f.t.Cleanup(func() {
		_ = l.Close()
		<-finished
	})
	return done
}

// clientOf makes the client of a test of the connection conn, which has said nothing yet.
func (f *fixture) clientOf(conn net.Conn) *client {
	return &client{t: f.t, f: f, conn: conn, rd: dbwire.NewReader(conn, 1<<20), wr: dbwire.NewWriter(conn, 1<<20)}
}

// blockedIn reports whether a goroutine has all of the names in its stack.
func blockedIn(names ...string) bool {
	buf := make([]byte, 1<<20)
	for _, g := range strings.Split(string(buf[:runtime.Stack(buf, true)]), "\n\n") {
		if !slices.ContainsFunc(names, func(name string) bool { return !strings.Contains(g, name) }) {
			return true
		}
	}
	return false
}

// serving reports whether a goroutine is in the function name of the server.
func serving(name string) bool {
	buf := make([]byte, 1<<20)
	return strings.Contains(string(buf[:runtime.Stack(buf, true)]), name)
}

func TestServeBound(t *testing.T) {
	// The bound of a listener of readers is Limits.Readers + 16, and that of the one of the writer is 1 + 16.
	for _, tc := range []struct {
		role  dbwire.Role
		bound int
	}{{dbwire.RoleReader, 19}, {dbwire.RoleReader, 17}, {dbwire.RoleWriter, 17}} {
		bound := tc.bound
		t.Run(fmt.Sprintf("%v %d", tc.role, bound), func(t *testing.T) {
			f := newFixture(t, func(o *Options) { o.Limits.Readers = bound - 16 })
			role := tc.role
			l := newPipeListener()
			f.serve(l, role, nil)
			conns := make([]net.Conn, 0, bound+1)
			for range bound {
				conns = append(conns, l.dial(t))
			}
			eventually(t, "the connections to be accepted", func() bool { return l.taken.Load() == int64(bound) })
			// One more connection is not accepted: Serve waits for a session to end. It does not call Accept, so that the
			// connections wait in the backlog of the kernel.
			conns = append(conns, l.dial(t))
			eventually(t, "Serve to wait for a slot", func() bool { return serving("dbserver.(*Server).takeSlot") })
			if n := l.taken.Load(); n != int64(bound) {
				t.Errorf("%d connections are accepted at the bound of %d", n, bound)
			}
			// Connections that have said Hello are no different: the bound is of the connections.
			_ = conns[0].Close()
			eventually(t, "the connection that waited to be accepted", func() bool { return l.taken.Load() == int64(bound)+1 })
			// A connection that is served says Hello as usual.
			c := f.clientOf(conns[bound])
			if err := c.sayHello(); err != nil {
				t.Errorf("Hello of the connection that waited: %v", err)
			}
		})
	}
}

// TestServeHugeReaders checks that a limit of readers near the largest int does not make Serve panic.
func TestServeHugeReaders(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Limits.Readers = math.MaxInt })
	l := newPipeListener()
	f.serve(l, dbwire.RoleReader, nil)
	c := f.clientOf(l.dial(t))
	if err := c.sayHello(); err != nil {
		t.Errorf("Hello with Limits.Readers = math.MaxInt: %v", err)
	}
}

// TestServeStall checks that a connection that never says Hello is closed after Limits.Stall, and frees its slot.
func TestServeStall(t *testing.T) {
	const bound = 17
	f := newFixture(t, func(o *Options) { o.Limits.Readers, o.Limits.Stall = 1, 40*time.Millisecond })
	l := newPipeListener()
	f.serve(l, dbwire.RoleReader, nil)
	var conns []net.Conn
	for range bound + 3 {
		conns = append(conns, l.dial(t))
	}
	// The first ones fill the slots and are closed by the daemon one after another, and the others are accepted then.
	eventually(t, "all the connections to be accepted", func() bool { return l.taken.Load() == bound+3 })
	for i, c := range conns {
		_ = c.SetReadDeadline(time.Now().Add(waitLimit))
		if _, err := c.Read(make([]byte, 1)); !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
			t.Errorf("connection %d: Read = %v, want it closed by the daemon", i, err)
		}
	}
	f.waitSessions(0)
}

// TestServeCheck checks that a connection that check refuses gets its Hello answered CodeDenied with the text of the error,
// and is closed; it does not take the place of the writer; and the other connections are served.
func TestServeCheck(t *testing.T) {
	f := newFixture(t, nil)
	l := newPipeListener()
	var deny atomic.Bool
	var checked atomic.Int64
	f.serve(l, dbwire.RoleWriter, func(conn net.Conn) error {
		checked.Add(1)
		if deny.Load() {
			return errors.New("not for you: " + secret)
		}
		return nil
	})
	before := f.dump()
	deny.Store(true)
	for range 2 {
		c := f.clientOf(l.dial(t))
		re := wantCode(t, c.sayHello(), dbwire.CodeDenied)
		if re.Msg != "not for you: "+secret {
			t.Errorf("the text is %q", re.Msg)
		}
		c.wantClosed()
	}
	if n := checked.Load(); n != 2 {
		t.Errorf("check was called %d times, want 2", n)
	}
	f.waitSessions(0)
	f.unchanged(before)
	deny.Store(false)
	c := f.clientOf(l.dial(t))
	if err := c.sayHello(); err != nil {
		t.Fatalf("Hello of a connection that check accepts: %v", err)
	}
	if c.hello.Role != dbwire.RoleWriter {
		t.Errorf("the role is %v", c.hello.Role)
	}
	c.mustCommit(put(2, "a", "1"))
}

// TestServeEnds checks what ends Serve: the end of its listener, an error of Accept that cannot be waited out, the end of
// the server; and that an error that can be waited out does not.
func TestServeEnds(t *testing.T) {
	t.Run("a role that is not one", func(t *testing.T) {
		f := newFixture(t, nil)
		if err := f.srv.Serve(newPipeListener(), 0, nil); err == nil {
			t.Error("Serve of a role that is not one returns nil")
		}
	})
	t.Run("the end of the listener", func(t *testing.T) {
		f := newFixture(t, nil)
		l := newPipeListener()
		done := f.serve(l, dbwire.RoleReader, nil)
		conn := l.dial(t)
		eventually(t, "the connection to be accepted", func() bool { return l.taken.Load() == 1 })
		_ = l.Close()
		if err := <-done; err != nil {
			t.Errorf("Serve = %v, want nil after the listener is closed", err)
		}
		// The session that it started goes on: Serve does not wait for it.
		c := f.clientOf(conn)
		if err := c.sayHello(); err != nil {
			t.Errorf("Hello after the listener is closed: %v", err)
		}
	})
	t.Run("an error", func(t *testing.T) {
		f := newFixture(t, nil)
		l := newPipeListener()
		boom := errors.New("accept failed")
		l.accept = func() error { return boom }
		if err := f.srv.Serve(l, dbwire.RoleReader, nil); !errors.Is(err, boom) {
			t.Errorf("Serve = %v, want the error of Accept", err)
		}
	})
	t.Run("an error that goes by", func(t *testing.T) {
		f := newFixture(t, nil)
		l := newPipeListener()
		var calls atomic.Int64
		l.accept = func() error {
			if calls.Add(1) <= 3 {
				return &net.OpError{Op: "accept", Err: syscall.EMFILE}
			}
			return nil
		}
		done := f.serve(l, dbwire.RoleReader, nil)
		c := f.clientOf(l.dial(t))
		if err := c.sayHello(); err != nil {
			t.Errorf("Hello after errors of Accept that go by: %v", err)
		}
		if n := calls.Load(); n < 4 {
			t.Errorf("Accept was called %d times, want 4 or more", n)
		}
		select {
		case err := <-done:
			t.Errorf("Serve ended with %v", err)
		default:
		}
	})
	t.Run("the end of the server", func(t *testing.T) {
		f := newFixture(t, func(o *Options) { o.Limits.Readers = 1 })
		l := newPipeListener()
		done := f.serve(l, dbwire.RoleReader, nil)
		for range 17 {
			l.dial(t)
		}
		eventually(t, "the connections to be accepted", func() bool { return l.taken.Load() == 17 })
		l.dial(t)
		eventually(t, "Serve to wait for a slot", func() bool { return serving("dbserver.(*Server).takeSlot") })
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Errorf("Serve = %v, want nil after the server is closed", err)
		}
		done2 := make(chan error, 1)
		l2 := newPipeListener()
		l2.dial(t)
		go func() { done2 <- f.srv.Serve(l2, dbwire.RoleReader, nil) }()
		if err := <-done2; err != nil {
			t.Errorf("Serve of a server that is closed = %v, want nil", err)
		}
	})
}

// TestNotLinuxText checks the sentence of Run on a system that is not Linux, which only such a system returns.
func TestNotLinuxText(t *testing.T) {
	if want := "runs on Linux only: it needs SO_PEERCRED to know the user of a peer"; errNotLinux.Error() != want {
		t.Errorf("the sentence is %q, want %q", errNotLinux, want)
	}
}

// TestShutdown checks that Shutdown refuses new requests and new Hellos, waits until no session is inside a request or its
// context ends, and then closes the connections, with their snapshots.
func TestShutdown(t *testing.T) {
	t.Run("no request in progress", func(t *testing.T) {
		f := newFixture(t, nil)
		r := f.connect(dbwire.RoleReader)
		r.mustSnapshot()
		f.srv.Shutdown(t.Context())
		r.wantClosed()
		f.waitSessions(0)
		wantCode(t, f.dial(dbwire.RoleReader).sayHello(), dbwire.CodeBusy)
	})

	for _, name := range []string{"the request ends first", "the context ends first"} {
		t.Run(name, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			f := newFixture(t, func(o *Options) {
				o.Hook = func(point string) error {
					if point == "commit:gate" {
						once.Do(func() { close(entered) })
						<-release
					}
					return nil
				}
			})
			w := f.connect(dbwire.RoleWriter)
			r := f.connect(dbwire.RoleReader)
			committed := make(chan error, 1)
			go func() {
				_, _, err := w.tryCommit(put(2, "a", "1"))
				committed <- err
			}()
			<-entered
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			shutdown := make(chan struct{})
			go func() {
				f.srv.Shutdown(ctx)
				close(shutdown)
			}()
			eventually(t, "the server to refuse requests", func() bool {
				f.srv.mu.Lock()
				defer f.srv.mu.Unlock()
				return f.srv.draining
			})
			// A request that comes now is refused, and its connection closed; so is a new Hello.
			re := wantCode(t, func() error { _, err := r.snapshot(); return err }(), dbwire.CodeBusy)
			if re.Msg != closingText {
				t.Errorf("the text is %q, want %q", re.Msg, closingText)
			}
			r.wantClosed()
			wantCode(t, f.dial(dbwire.RoleReader).sayHello(), dbwire.CodeBusy)
			select {
			case <-shutdown:
				t.Fatal("Shutdown has returned while a request is in progress")
			default:
			}
			if name == "the request ends first" {
				close(release)
				if err := <-committed; err != nil {
					t.Errorf("the request in progress: %v", err)
				}
				<-shutdown
				w.wantClosed()
				return
			}
			cancel()
			<-shutdown
			// The connection is closed under the request, which gets no answer.
			if err := <-committed; err == nil {
				t.Error("a request that the end of Shutdown has cut is answered")
			}
			close(release)
		})
	}

	// A request that Shutdown finds in progress is answered before its connection is closed: the reply that waits in the
	// buffer of the session is written first.
	shuttingDown := func(t *testing.T, f *fixture) <-chan struct{} {
		t.Helper()
		done := make(chan struct{})
		go func() {
			f.srv.Shutdown(t.Context())
			close(done)
		}()
		eventually(t, "the server to refuse requests", func() bool {
			f.srv.mu.Lock()
			defer f.srv.mu.Unlock()
			return f.srv.draining
		})
		return done
	}
	notYet := func(t *testing.T, done <-chan struct{}, why string) {
		t.Helper()
		select {
		case <-done:
			t.Fatalf("Shutdown has returned while %s", why)
		case <-time.After(50 * time.Millisecond):
		}
	}

	t.Run("a session that holds a snapshot and waits", func(t *testing.T) {
		f := newFixture(t, nil)
		r := f.connect(dbwire.RoleReader)
		r.mustSnapshot()
		ctx, cancel := context.WithTimeout(t.Context(), waitLimit)
		defer cancel()
		start := time.Now()
		f.srv.Shutdown(ctx)
		if d := time.Since(start); d > waitLimit/4 || ctx.Err() != nil {
			t.Errorf("Shutdown took %v, with a session that only waits for a request", d)
		}
		r.wantClosed()
		f.waitSessions(0)
	})

	t.Run("the reply to a Get", func(t *testing.T) {
		f := newFixture(t, nil)
		seedWindow(f)
		r := f.connect(dbwire.RoleReader)
		r.mustSnapshot()
		if err := <-r.send(dbwire.KindGet, func(e *dbwire.Enc) { e.GetRequest(2, []byte("k001")) }); err != nil {
			t.Fatal(err)
		}
		// The request is read and handled, and its reply waits for the client, inside the request.
		eventually(t, "the daemon to write the reply", func() bool {
			return blockedIn("dbserver.(*session).request", "dbserver.(*timedConn).Write")
		})
		done := shuttingDown(t, f)
		notYet(t, done, "the reply to a request is not read")
		if got := r.readOne(dbwire.KindGet); got != `get true "v001"` {
			t.Errorf("the reply: %s", got)
		}
		<-done
		r.wantClosed()
	})

	t.Run("a Snapshot", func(t *testing.T) {
		f := newFixture(t, nil)
		r := f.connect(dbwire.RoleReader)
		if err := <-r.send(dbwire.KindSnapshot, nil); err != nil {
			t.Fatal(err)
		}
		eventually(t, "the daemon to write the reply", func() bool {
			return blockedIn("dbserver.(*session).hold", "dbserver.(*timedConn).Write")
		})
		done := shuttingDown(t, f)
		notYet(t, done, "the reply to a Snapshot is not read")
		if got := r.readOne(dbwire.KindSnapshot); got != "snapshot 0" {
			t.Errorf("the reply: %s", got)
		}
		<-done
		r.wantClosed()
	})

	t.Run("a window that has begun", func(t *testing.T) {
		held, release := make(chan struct{}), make(chan struct{})
		var once, unblock sync.Once
		f := newFixture(t, func(o *Options) {
			o.Hook = func(point string) error {
				if point == "reply:held" {
					once.Do(func() { close(held) })
					<-release
				}
				return nil
			}
		})
		t.Cleanup(func() { unblock.Do(func() { close(release) }) })
		seedWindow(f)
		r := f.connect(dbwire.RoleReader)
		r.mustSnapshot()
		var w window
		w.get(2, "k001", `get true "v001"`)
		w.get(2, "k002", `get true "v002"`)
		r.writeWindow(w.bytes)
		// The reply to the first request is held, because the second is in the read buffer, and the first is still in progress.
		<-held
		done := shuttingDown(t, f)
		notYet(t, done, "a reply is in the buffer of a session")
		unblock.Do(func() { close(release) })
		r.readWindow(&w)
		<-done
		r.wantClosed()
	})
}
