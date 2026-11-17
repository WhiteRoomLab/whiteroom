package dbclient_test

import (
	"cmp"
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
			MinFreeInodes: -1, Readers: 8},
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
	// sent and received count the messages that the client has written and the replies that it has read from the
	// connection, in all.
	sent, received int
	writes         []writeRecord // the Write calls since reset
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
	r.writes = nil
}

// writeRecord is a Write call of the client: its bytes, the kinds of the requests that begin in it, and how many requests
// that the client had sent were still without a reply that it had read from the connection when it was made.
type writeRecord struct {
	bytes       int
	kinds       []dbwire.Kind
	outstanding int
}

// writeCalls returns the Write calls since reset.
func (r *recorder) writeCalls() []writeRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.writes)
}

// windows returns the Write calls since reset whose first request is of kind.
func (r *recorder) windows(kind dbwire.Kind) []writeRecord {
	var out []writeRecord
	for _, w := range r.writeCalls() {
		if len(w.kinds) > 0 && w.kinds[0] == kind {
			out = append(out, w)
		}
	}
	return out
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

// frameInfo is a frame of a stream: its kind, and whether it ends its message.
type frameInfo struct {
	kind dbwire.Kind
	last bool
}

// framesOf returns the headers of the frames in p, which is whole frames: a Write call of a Writer is.
func framesOf(p []byte) []frameInfo {
	var out []frameInfo
	for len(p) >= dbwire.HeaderSize {
		out = append(out, frameInfo{dbwire.Kind(p[0]), p[1]&1 != 0})
		p = p[dbwire.HeaderSize+int(binary.BigEndian.Uint32(p[4:])):]
	}
	return out
}

// frameCounter counts the messages that end in a stream of frames that it is given in pieces.
type frameCounter struct {
	hdr  [dbwire.HeaderSize]byte
	have int // the bytes of the header that it has
	left int // the bytes of the payload that it has not seen
	last bool
}

// feed takes the next bytes of the stream, and returns how many messages end in them.
func (f *frameCounter) feed(p []byte) (ended int) {
	for len(p) > 0 {
		if f.have < dbwire.HeaderSize {
			n := copy(f.hdr[f.have:], p)
			f.have += n
			p = p[n:]
			if f.have == dbwire.HeaderSize {
				f.left, f.last = int(binary.BigEndian.Uint32(f.hdr[4:])), f.hdr[1]&1 != 0
			}
		} else {
			n := min(f.left, len(p))
			f.left -= n
			p = p[n:]
		}
		if f.have == dbwire.HeaderSize && f.left == 0 {
			f.have = 0
			if f.last {
				ended++
			}
		}
	}
	return ended
}

// recConn watches the frames that the client writes and the messages that it reads. A Write of the client's Writer is
// one frame, or, in a window, the frames of several requests.
type recConn struct {
	net.Conn
	r     *recorder
	start bool // the next frame starts a message
	kind  dbwire.Kind
	frame int
	once  sync.Once
	in    frameCounter // of what the client reads
}

func (c *recConn) Write(p []byte) (int, error) {
	type frameAt struct {
		kind  dbwire.Kind
		frame int
		last  bool
	}
	frames := framesOf(p)
	at := make([]frameAt, len(frames))
	c.r.mu.Lock()
	rec := writeRecord{bytes: len(p), outstanding: c.r.sent - c.r.received}
	for i, f := range frames {
		if c.start {
			c.kind, c.frame = f.kind, 0
			c.r.reqs = append(c.r.reqs, f.kind)
			rec.kinds = append(rec.kinds, f.kind)
		}
		at[i] = frameAt{c.kind, c.frame, f.last}
		c.start, c.frame = f.last, c.frame+1
		if f.last {
			c.r.sent++
		}
	}
	c.r.writes = append(c.r.writes, rec)
	before, after := c.r.before, c.r.after
	c.r.mu.Unlock()
	if before != nil {
		for _, f := range at {
			if err := before(c, f.kind, f.frame, f.last); err != nil {
				_ = c.Close()
				return 0, err
			}
		}
	}
	n, err := c.Conn.Write(p)
	if err == nil && after != nil {
		for _, f := range at {
			after(c, f.kind, f.frame, f.last)
		}
	}
	return n, err
}

func (c *recConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.r.mu.Lock()
		c.r.received += c.in.feed(p[:n])
		c.r.mu.Unlock()
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

// peerDialer is a Dialer whose connections are one end of a net.Pipe, with a peer on the other end that speaks as the
// daemon of a test: script runs once for each connection, and returns when that connection is done.
type peerDialer struct {
	t      testing.TB
	frame  int // the limit of a frame that the Hello says; 0: testFrame
	script func(p *peer)

	mu    sync.Mutex
	dials int
	wg    sync.WaitGroup
}

func (d *peerDialer) Dial(_ context.Context, role dbwire.Role) (net.Conn, error) {
	d.mu.Lock()
	n := d.dials
	d.dials++
	d.mu.Unlock()
	cc, sc := net.Pipe()
	p := &peer{t: d.t, n: n, role: role, frame: cmp.Or(d.frame, testFrame), nc: sc, wr: dbwire.NewWriter(sc, 1<<20),
		reqs: make(chan peerRequest, 4096), done: make(chan struct{})}
	d.wg.Add(2)
	go func() {
		defer d.wg.Done()
		p.read()
	}()
	go func() {
		defer d.wg.Done()
		defer func() {
			close(p.done)
			_ = sc.Close()
		}()
		d.script(p)
	}()
	return cc, nil
}

func (d *peerDialer) String() string { return "peer" }

func (d *peerDialer) Close() error {
	d.wg.Wait()
	return nil
}

func (d *peerDialer) dialed() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dials
}

// peerRequest is a request that a peer has read: for a read, what it asks for.
type peerRequest struct {
	kind  dbwire.Kind
	table uint32
	key   []byte // of a Get, and the prefix of a Scan
	after []byte
	more  bool // a Scan that goes on after the key after
	hash  [32]byte
}

func (r peerRequest) String() string {
	if r.kind == dbwire.KindGet {
		return fmt.Sprintf("Get %d %q", r.table, r.key)
	}
	if r.kind == dbwire.KindScan {
		return fmt.Sprintf("Scan %d %q after %v %q", r.table, r.key, r.more, r.after)
	}
	if r.kind == dbwire.KindObject {
		return fmt.Sprintf("Object %x", r.hash[:4])
	}
	return r.kind.String()
}

// peer is the daemon's end of a connection of a peerDialer. A method that cannot go on, because the test has failed or the
// client has ended the connection, ends the goroutine of the script.
type peer struct {
	t     testing.TB
	n     int // the number of the connection, from 0
	role  dbwire.Role
	frame int
	nc    net.Conn
	wr    *dbwire.Writer
	reqs  chan peerRequest // the requests that have been read, in order; closed when the stream ends
	done  chan struct{}    // closed when the script has returned
}

// readPeerRequest reads the body of a request of kind that rd has begun; the bodies of the others are left out.
func readPeerRequest(rd *dbwire.Reader, kind dbwire.Kind) (peerRequest, error) {
	r := peerRequest{kind: kind}
	d := &dbwire.Dec{R: rd}
	var err error
	if kind == dbwire.KindGet {
		r.table, r.key, err = dbwire.ReadGetRequest(d, dbwire.MaxKey)
		return r, err
	}
	if kind == dbwire.KindScan {
		var s dbwire.ScanRequest
		s, err = dbwire.ReadScanRequest(d, dbwire.MaxKey)
		r.table, r.key, r.after, r.more = s.Table, s.Prefix, s.After, s.HasAfter
		return r, err
	}
	if kind == dbwire.KindObject {
		r.hash, err = dbwire.ReadObjectRequest(d)
		return r, err
	}
	return r, rd.Discard()
}

// read reads the requests of the client for as long as it sends them, so that a client that writes more than it should
// does not block, and a test can tell.
func (p *peer) read() {
	defer close(p.reqs)
	rd := dbwire.NewReader(p.nc, 1<<20)
	for {
		kind, err := rd.Next()
		if err != nil {
			return
		}
		r, err := readPeerRequest(rd, kind)
		if err != nil {
			return
		}
		select {
		case p.reqs <- r:
		case <-p.done:
			return
		}
	}
}

// stop ends the goroutine of the script.
func (p *peer) stop() { runtime.Goexit() }

// request returns the next request.
func (p *peer) request() peerRequest {
	p.t.Helper()
	select {
	case r, ok := <-p.reqs:
		if !ok {
			p.stop()
		}
		return r
	case <-time.After(waitLimit):
		p.t.Errorf("the peer has had no request for %v", waitLimit)
		p.stop()
	}
	return peerRequest{}
}

// expect returns the next request, which must be of kind.
func (p *peer) expect(kind dbwire.Kind) peerRequest {
	p.t.Helper()
	r := p.request()
	if r.kind != kind {
		p.t.Errorf("the request is %v, want a %v", r, kind)
		p.stop()
	}
	return r
}

// silent fails the test if the client sends a request within d.
func (p *peer) silent(d time.Duration) {
	p.t.Helper()
	select {
	case r, ok := <-p.reqs:
		if ok {
			p.t.Errorf("the client sent %v before it had read every reply", r)
		}
		p.stop()
	case <-time.After(d):
	}
}

// reply writes an OK reply with the body that body writes.
func (p *peer) reply(body func(e *dbwire.Enc)) {
	p.wr.Begin(dbwire.KindOK)
	e := &dbwire.Enc{W: p.wr}
	if body != nil {
		body(e)
	}
	if err := p.wr.End(); err != nil || e.Err != nil {
		p.stop()
	}
}

// refuse writes an Error frame as the reply.
func (p *peer) refuse(code dbwire.Code, msg string) {
	if p.wr.Fail(code, msg) != nil {
		p.stop()
	}
}

// raw writes bytes to the connection as they are.
func (p *peer) raw(b []byte) {
	if _, err := p.nc.Write(b); err != nil {
		p.stop()
	}
}

func (p *peer) hello() {
	p.t.Helper()
	p.expect(dbwire.KindHello)
	p.reply(func(e *dbwire.Enc) {
		e.HelloReply(dbwire.HelloReply{Version: dbwire.Version, Role: p.role, Frame: p.frame, Commit: 1 << 20,
			Object: 1 << 20, Staged: 1 << 20, MaxKey: dbwire.MaxKey})
	})
}

func (p *peer) snapshot(seq uint64) {
	p.t.Helper()
	p.expect(dbwire.KindSnapshot)
	p.reply(func(e *dbwire.Enc) { e.SnapshotReply(seq) })
}

func (p *peer) release() {
	p.t.Helper()
	p.expect(dbwire.KindRelease)
	p.reply(nil)
}

func (p *peer) getReply(found bool, value string) {
	p.reply(func(e *dbwire.Enc) { e.GetReply(found, []byte(value)) })
}

func (p *peer) objectReply(found bool, data string) {
	p.reply(func(e *dbwire.Enc) { e.ObjectReply(found, []byte(data)) })
}

// pageReply writes a page of rows, which come as key, value, key, value...
func (p *peer) pageReply(more bool, kv ...string) {
	p.reply(func(e *dbwire.Enc) {
		for i := 0; i+1 < len(kv); i += 2 {
			e.ScanRow([]byte(kv[i]), []byte(kv[i+1]))
		}
		e.ScanEnd(more)
	})
}

// openPeer opens a client as role of a peerDialer that runs script, and closes it at the end of the test.
func openPeer(t *testing.T, role dbwire.Role, script func(p *peer)) (*dbclient.Client, *recorder, *peerDialer) {
	t.Helper()
	noLeaks(t)
	pd := &peerDialer{t: t, script: script}
	rec := &recorder{Dialer: pd}
	c, err := dbclient.Open(bg, rec, role, dbclient.Options{DialWait: -1})
	must(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c, rec, pd
}
