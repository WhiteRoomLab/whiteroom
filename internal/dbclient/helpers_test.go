package dbclient_test

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver/dbtest"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

const waitLimit = 20 * time.Second

// noLeaks fails the test at its end if a goroutine of the client, the server or the harness is left. Register it before
// the daemon and the clients are made, so that it runs after they are closed.
func noLeaks(t testing.TB) {
	t.Helper()
	t.Cleanup(func() {
		var stacks string
		leaked := func() bool {
			buf := make([]byte, 1<<20)
			stacks = string(buf[:runtime.Stack(buf, true)])
			for _, g := range strings.Split(stacks, "\n\n") {
				for _, marker := range []string{"internal/dbclient.", "internal/dbserver.", "internal/dbserver/dbtest."} {
					if strings.Contains(g, marker) {
						return true
					}
				}
			}
			return false
		}
		for deadline := time.Now().Add(waitLimit); leaked(); {
			if time.Now().After(deadline) {
				t.Errorf("goroutines of the client or the daemon are left:\n%s", stacks)
				return
			}
			time.Sleep(time.Millisecond)
		}
	})
}

// clock is the clock of the marks: each call returns the time and moves it on by a second.
type clock struct {
	mu sync.Mutex
	n  int
}

var clockBase = time.Date(2026, 11, 15, 9, 30, 0, 123456789, time.UTC)

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := clockBase.Add(time.Duration(c.n) * time.Second)
	c.n++
	return t
}

// testFrame is the limit of a frame of the daemons of the tests, so that a few hundred bytes of values make a page.
const testFrame = 4096

// fixture is a daemon on a simulated file system, with the clients of a test.
type fixture struct {
	t     testing.TB
	fs    *simfs.FS
	opts  dbtest.Options
	root  *dbtest.Daemon
	clock *clock
}

func testOptions(fsys *simfs.FS) dbtest.Options {
	return dbtest.Options{
		FS:  fsys,
		Log: &logdb.Options{Placement: logdb.Hybrid, Threshold: 64, WriterWait: 20 * time.Millisecond},
		Limits: &dbwire.Limits{Frame: testFrame, SnapshotIdle: -1, Stall: waitLimit, CommitsPerSecond: -1, MinFree: -1,
			Readers: 8},
	}
}

// newFixture makes and opens a data directory on a simulated file system. mod changes the options first.
func newFixture(t testing.TB, mod func(*dbtest.Options)) *fixture {
	t.Helper()
	noLeaks(t)
	f := &fixture{t: t, fs: simfs.New("data"), clock: &clock{}}
	f.opts = testOptions(f.fs)
	f.opts.Now = f.clock.now
	if mod != nil {
		mod(&f.opts)
	}
	if err := dbtest.Init("data", f.opts); err != nil {
		t.Fatal(err)
	}
	root, err := dbtest.Acquire("data", f.opts)
	if err != nil {
		t.Fatal(err)
	}
	f.root = root
	t.Cleanup(func() {
		if err := root.Release(); err != nil {
			t.Errorf("Release: %v", err)
		}
	})
	return f
}

// open connects a client as role, through a handle of its own, and closes it at the end of the test.
func (f *fixture) open(role dbwire.Role, o dbclient.Options) (*dbclient.Client, *recorder) {
	f.t.Helper()
	c, rec, err := f.tryOpen(context.Background(), role, o)
	if err != nil {
		f.t.Fatal(err)
	}
	return c, rec
}

func (f *fixture) tryOpen(ctx context.Context, role dbwire.Role, o dbclient.Options) (*dbclient.Client, *recorder, error) {
	rec := &recorder{Dialer: f.acquire().Dialer()}
	return f.tryOpenWith(ctx, rec, role, o)
}

func (f *fixture) tryOpenWith(ctx context.Context, rec *recorder, role dbwire.Role, o dbclient.Options) (*dbclient.Client, *recorder, error) {
	if o.DialWait == 0 {
		o.DialWait = 300 * time.Millisecond
	}
	c, err := dbclient.Open(ctx, rec, role, o)
	if err != nil {
		return nil, rec, err
	}
	f.t.Cleanup(func() { _ = c.Close() })
	return c, rec, nil
}

// writer opens a client of the writer, and seeds the store.
func (f *fixture) writer(o dbclient.Options) (*dbclient.Client, *recorder) {
	f.t.Helper()
	c, rec := f.open(dbwire.RoleWriter, o)
	seed(f.t, c)
	rec.reset()
	return c, rec
}

// seed makes the first commit of a store, which has to put the row of its schema.
func seed(t testing.TB, c *dbclient.Client) {
	t.Helper()
	must(t, c.Update(bg, func(tx *dbclient.Tx) error {
		if err := tx.Put(dbwire.TableMeta, []byte(dbwire.MetaSchema), binary.AppendUvarint(nil, dbwire.TableVersion)); err != nil {
			return err
		}
		return tx.AppendEvent([]byte(`{"seed":true}`))
	}))
}

// acquire returns a handle of the daemon that the caller has to release, or to give to a client.
func (f *fixture) acquire() *dbtest.Daemon {
	f.t.Helper()
	h, err := dbtest.Acquire("data", f.opts)
	must(f.t, err)
	return h
}

func (f *fixture) engine() *logdb.DB { return dbserver.EngineOf(f.root.Server()) }

// recorder is a Dialer that wraps another, counts what the client does, and lets a test cut a connection.
type recorder struct {
	dbclient.Dialer

	mu       sync.Mutex
	attempts int
	dials    []dbwire.Role
	reqs     []dbwire.Kind
	live     int
	conns    []*recConn
	closes   int // of the dialer
	// failDial, if it is not nil, is called with the number of this attempt, and its error is the error of the dial.
	failDial func(attempt int) error
	// before is called before a frame of a request is written, with the kind of the request, the number of the frame in
	// it and whether it is the last; its error ends the connection and fails the write. after is called after the frame.
	before func(c *recConn, kind dbwire.Kind, frame int, last bool) error
	after  func(c *recConn, kind dbwire.Kind, frame int, last bool)
}

func (r *recorder) Dial(ctx context.Context, role dbwire.Role) (net.Conn, error) {
	r.mu.Lock()
	r.attempts++
	attempt, failDial := r.attempts, r.failDial
	r.mu.Unlock()
	if failDial != nil {
		if err := failDial(attempt); err != nil {
			return nil, err
		}
	}
	nc, err := r.Dialer.Dial(ctx, role)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dials = append(r.dials, role)
	r.live++
	rc := &recConn{Conn: nc, r: r, start: true}
	r.conns = append(r.conns, rc)
	return rc, nil
}

// cutAll closes every connection that the dialer made, under the client, which does not know: the daemon has ended them,
// as a restart does.
func (r *recorder) cutAll() {
	r.mu.Lock()
	conns := slices.Clone(r.conns)
	r.mu.Unlock()
	for _, c := range conns {
		_ = c.Conn.Close()
	}
}

func (r *recorder) String() string {
	if s, ok := r.Dialer.(fmt.Stringer); ok {
		return s.String()
	}
	return "recorder"
}

func (r *recorder) Close() error {
	r.mu.Lock()
	r.closes++
	r.mu.Unlock()
	if cl, ok := r.Dialer.(io.Closer); ok {
		return cl.Close()
	}
	return nil
}

func (r *recorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = nil
}

// requests returns the kinds of the requests sent since reset, in order.
func (r *recorder) requests() []dbwire.Kind {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.reqs)
}

func (r *recorder) count(kind dbwire.Kind) int {
	n := 0
	for _, k := range r.requests() {
		if k == kind {
			n++
		}
	}
	return n
}

func (r *recorder) dialed(role dbwire.Role) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return countOf(r.dials, role)
}

func countOf[T comparable](s []T, v T) int {
	n := 0
	for _, x := range s {
		if x == v {
			n++
		}
	}
	return n
}

func (r *recorder) liveConns() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.live
}

// recConn watches the frames that the client writes: each Write of the client's Writer is one frame.
type recConn struct {
	net.Conn
	r     *recorder
	start bool // the next frame starts a message
	kind  dbwire.Kind
	frame int
	once  sync.Once
}

func (c *recConn) Write(p []byte) (int, error) {
	last := p[1]&1 != 0
	if c.start {
		c.kind, c.frame = dbwire.Kind(p[0]), 0
		c.r.mu.Lock()
		c.r.reqs = append(c.r.reqs, c.kind)
		c.r.mu.Unlock()
	}
	c.r.mu.Lock()
	before, after := c.r.before, c.r.after
	c.r.mu.Unlock()
	frame := c.frame
	if before != nil {
		if err := before(c, c.kind, frame, last); err != nil {
			_ = c.Close()
			return 0, err
		}
	}
	n, err := c.Conn.Write(p)
	c.start, c.frame = last, frame+1
	if err == nil && after != nil {
		after(c, c.kind, frame, last)
	}
	return n, err
}

func (c *recConn) Close() error {
	c.once.Do(func() {
		c.r.mu.Lock()
		c.r.live--
		c.r.mu.Unlock()
	})
	return c.Conn.Close()
}

// kindsOf formats request kinds for a message.
func kindsOf(kinds []dbwire.Kind) string {
	var parts []string
	for _, k := range kinds {
		parts = append(parts, k.String())
	}
	return "[" + strings.Join(parts, " ") + "]"
}

func wantRequests(t testing.TB, rec *recorder, want ...dbwire.Kind) {
	t.Helper()
	if got := rec.requests(); !slices.Equal(got, want) {
		t.Errorf("the client sent %s, want %s", kindsOf(got), kindsOf(want))
	}
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

var bg = context.Background()

// put, get and del are the calls of a Tx that a test does not expect to fail.
func put(t testing.TB, tx *dbclient.Tx, table uint32, key, value string) {
	t.Helper()
	must(t, tx.Put(table, []byte(key), []byte(value)))
}

func del(t testing.TB, tx *dbclient.Tx, table uint32, key string) {
	t.Helper()
	must(t, tx.Delete(table, []byte(key)))
}

func get(t testing.TB, tx *dbclient.Tx, table uint32, key string) (string, bool) {
	t.Helper()
	v, ok, err := tx.Get(table, []byte(key))
	must(t, err)
	return string(v), ok
}

func event(t testing.TB, tx *dbclient.Tx, data string) {
	t.Helper()
	must(t, tx.AppendEvent([]byte(data)))
}

// commit runs an Update of c that does f and adds an event, which a commit that changes a table needs.
func commit(t testing.TB, c *dbclient.Client, f func(tx *dbclient.Tx)) {
	t.Helper()
	must(t, c.Update(bg, func(tx *dbclient.Tx) error {
		f(tx)
		return tx.AppendEvent([]byte(`{"test":true}`))
	}))
}

// rows reads the rows of a table with the prefix in a View.
func rows(t testing.TB, c *dbclient.Client, table uint32, prefix string) []string {
	t.Helper()
	var out []string
	must(t, c.View(bg, func(tx *dbclient.Tx) error {
		return tx.Scan(table, []byte(prefix), func(k, v []byte) bool {
			out = append(out, string(k)+"="+string(v))
			return true
		})
	}))
	return out
}

func wantRows(t testing.TB, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// remote returns the error reply that err is, or fails the test.
func remote(t testing.TB, err error) *dbwire.RemoteError {
	t.Helper()
	re, ok := errors.AsType[*dbwire.RemoteError](err)
	if !ok {
		t.Fatalf("got %v, want an error reply of the daemon", err)
	}
	return re
}
