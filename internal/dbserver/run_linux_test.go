package dbserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// A daemon is a Run in a goroutine, with a data directory and a directory of sockets that are short enough for a socket.
type daemon struct {
	t       *testing.T
	f       *fixture // for the clients
	dir     string
	data    string
	socks   string
	cfg     Config
	heads   *lineWriter
	ticks   chan time.Time
	ready   atomic.Int64 // the calls of Ready
	cancel  context.CancelFunc
	done    chan error
	stopped bool
}

// newDaemonDirs makes the directories of a daemon, and the data directory in them, with the commits that seed has.
func newDaemonDirs(t *testing.T) (dir, data, socks string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "wr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	data, socks = filepath.Join(dir, "data"), filepath.Join(dir, "s")
	mkdir(t, socks)
	return dir, data, socks
}

// startDaemon runs the daemon, with the options of the tests, in a data directory that has the commits of seed (through a
// server that is closed again), once it has said that it is ready; mod changes the configuration first.
func startDaemon(t *testing.T, seed func(w *client), mod func(*Config)) *daemon {
	t.Helper()
	noLeaks(t)
	d := &daemon{t: t, heads: &lineWriter{}, ticks: make(chan time.Time), done: make(chan error, 1)}
	logs := &logLines{}
	d.f = &fixture{t: t, logs: logs, clock: &clock{t: testBase, step: time.Second}}
	d.dir, d.data, d.socks = newDaemonDirs(t)
	o := testOptions()
	o.Now, o.Logf = d.f.clock.now, logs.logf
	if err := CreateDir(d.data, o); err != nil {
		t.Fatal(err)
	}
	if seed != nil {
		srv, err := OpenDir(d.data, o)
		if err != nil {
			t.Fatal(err)
		}
		seed((&fixture{t: t, srv: srv, logs: logs}).connect(dbwire.RoleWriter))
		if err := srv.Close(); err != nil {
			t.Fatal(err)
		}
	}
	ready := make(chan struct{})
	d.cfg = Config{Data: d.data, Sockets: d.socks, WriterUID: uint32(os.Getuid()), WriterGID: -1, ReaderGID: -1, Options: o,
		Heads: d.heads, Ticks: func() (<-chan time.Time, func()) { return d.ticks, func() {} },
		Ready: func() {
			if d.ready.Add(1) == 1 {
				close(ready)
			}
		}}
	if mod != nil {
		mod(&d.cfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	go func() { d.done <- Run(ctx, d.cfg) }()
	select {
	case <-ready:
	case err := <-d.done:
		cancel()
		t.Fatalf("Run = %v before it was ready", err)
	}
	t.Cleanup(func() {
		if !d.stopped {
			_ = d.stop()
		}
	})
	return d
}

// stop ends the context of Run, and returns its result.
func (d *daemon) stop() error {
	d.stopped = true
	d.cancel()
	return <-d.done
}

// dial connects to a socket of the daemon, and says Hello unless raw.
func (d *daemon) dial(name string, raw bool) *client {
	d.t.Helper()
	conn, err := net.Dial("unix", filepath.Join(d.socks, name))
	if err != nil {
		d.t.Fatal(err)
	}
	d.t.Cleanup(func() { _ = conn.Close() })
	c := d.f.clientOf(conn)
	if !raw {
		if err := c.sayHello(); err != nil {
			d.t.Fatalf("Hello on %s: %v", name, err)
		}
	}
	return c
}

func TestRun(t *testing.T) {
	d := startDaemon(t, func(w *client) {
		w.mustCommit(put(2, "a", "1"), put(dbwire.TableHeads, "activity", headLine(1)))
	}, func(c *Config) {
		c.WriterGID, c.ReaderGID = os.Getegid(), os.Getegid()
	})
	if n := d.ready.Load(); n != 1 {
		t.Errorf("Ready was called %d times", n)
	}
	for _, name := range []string{dbwire.WriterSocket, dbwire.ReaderSocket} {
		info, err := os.Lstat(filepath.Join(d.socks, name))
		if err != nil || info.Mode().Type() != os.ModeSocket || info.Mode().Perm() != 0o660 {
			t.Errorf("%s is %v (%v), want a socket with the mode 0660", name, info, err)
			continue
		}
		if gid := info.Sys().(*syscall.Stat_t).Gid; int(gid) != os.Getegid() {
			t.Errorf("the group of %s is %d, want %d", name, gid, os.Getegid())
		}
	}

	// The heads at the start.
	eventually(t, "the heads at the start", func() bool { return len(d.heads.all()) == 1 })
	if got := d.heads.all(); got[0] != headLine(1)+"\n" {
		t.Errorf("the lines at the start are %q", got)
	}

	// A client of the writer socket is the writer, and one of the reader socket a reader, who cannot commit.
	w := d.dial(dbwire.WriterSocket, false)
	if w.hello.Role != dbwire.RoleWriter {
		t.Errorf("the role on %s is %v", dbwire.WriterSocket, w.hello.Role)
	}
	r := d.dial(dbwire.ReaderSocket, false)
	if r.hello.Role != dbwire.RoleReader {
		t.Errorf("the role on %s is %v", dbwire.ReaderSocket, r.hello.Role)
	}
	seq := r.mustSnapshot()
	if seq != 1 {
		t.Errorf("the reader sees the sequence number %d, want 1", seq)
	}
	_, _, err := r.commit(seq, put(2, "x", "y"), event("e"))
	wantCode(t, err, dbwire.CodeDenied)
	// A reader does not use the writer's place: the writer commits, and the reader sees it after a new snapshot.
	w.mustCommit(put(2, "b", "2"), put(dbwire.TableHeads, "activity", headLine(2)))
	r.mustSnapshot()
	if found, v := r.mustGet(2, "b"); !found || v != "2" {
		t.Errorf("the reader reads %v %q", found, v)
	}

	// The heads at a tick.
	d.ticks <- time.Time{}
	eventually(t, "the heads at a tick", func() bool { return len(d.heads.all()) == 2 })
	if got := d.heads.all(); got[1] != headLine(2)+"\n" {
		t.Errorf("the line at the tick is %q", got[1])
	}

	// A change that no tick has seen is written at the stop.
	w.mustCommit(put(dbwire.TableHeads, "activity", headLine(3)))
	if err := d.stop(); err != nil {
		t.Fatalf("Run = %v after the end of its context", err)
	}
	if got := d.heads.all(); len(got) != 3 || got[2] != headLine(3)+"\n" {
		t.Errorf("the lines after the stop are %q, want the last head at the stop", got)
	}
	for _, name := range []string{dbwire.WriterSocket, dbwire.ReaderSocket} {
		if _, err := os.Lstat(filepath.Join(d.socks, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s after the stop: %v, want it gone", name, err)
		}
	}
	if _, err := net.Dial("unix", filepath.Join(d.socks, dbwire.WriterSocket)); err == nil {
		t.Error("the writer socket takes a connection after the stop")
	}
	// The data directory is free: and has what the writer put.
	srv, err := OpenDir(d.data, d.cfg.Options)
	if err != nil {
		t.Fatalf("OpenDir() after the stop = %v", err)
	}
	defer func() { _ = srv.Close() }()
	g := &fixture{t: t, srv: srv}
	if got := g.dump().Seq; got != 3 {
		t.Errorf("the store has the sequence number %d, want 3", got)
	}
}

// TestRunPeer checks that the writer socket is served only to connections whose peer has the user that the configuration
// names: another user's Hello is answered CodeDenied, nothing changes, the writer's place is not taken, and the reader socket
// goes on serving.
func TestRunPeer(t *testing.T) {
	var fake atomic.Int64 // the user of every peer, or -1 for the real one
	fake.Store(-1)
	var failure atomic.Pointer[error]
	realUID := peerUID
	peerUID = func(conn net.Conn) (uint32, error) {
		if e := failure.Load(); e != nil {
			return 0, *e
		}
		if v := fake.Load(); v >= 0 {
			return uint32(v), nil
		}
		return realUID(conn)
	}
	t.Cleanup(func() { peerUID = realUID })
	d := startDaemon(t, nil, nil)

	other := int64(os.Getuid()) + 1
	fake.Store(other)
	r := d.dial(dbwire.ReaderSocket, false)
	before := r.mustSnapshot()
	for range 2 {
		c := d.dial(dbwire.WriterSocket, true)
		re := wantCode(t, c.sayHello(), dbwire.CodeDenied)
		if want := fmt.Sprintf("the user %d is not the writer of this daemon", other); re.Msg != want {
			t.Errorf("the text is %q, want %q", re.Msg, want)
		}
		c.wantClosed()
	}
	if got := r.mustSnapshot(); got != before {
		t.Errorf("the store changed: %d, was %d", got, before)
	}
	// The reader socket serves every user.
	if r2 := d.dial(dbwire.ReaderSocket, false); r2.hello.Role != dbwire.RoleReader {
		t.Errorf("the role is %v", r2.hello.Role)
	}

	// A peer whose user is not known is denied too.
	boom := errors.New("no credentials")
	failure.Store(&boom)
	c := d.dial(dbwire.WriterSocket, true)
	re := wantCode(t, c.sayHello(), dbwire.CodeDenied)
	if want := "cannot tell the user of the peer: no credentials"; re.Msg != want {
		t.Errorf("the text is %q, want %q", re.Msg, want)
	}
	failure.Store(nil)

	// The writer: the denied connections did not take its place.
	fake.Store(int64(os.Getuid()))
	w := d.dial(dbwire.WriterSocket, false)
	w.mustCommit(put(2, "a", "1"))
}

// TestPeerUID checks that the real peerUID gives the user of a process that connects to a socket: this one.
func TestPeerUID(t *testing.T) {
	dir, err := os.MkdirTemp("", "wr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	l, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- c
	}()
	client, err := net.Dial("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server := <-accepted
	t.Cleanup(func() { _ = server.Close() })
	for name, conn := range map[string]net.Conn{"server": server, "client": client} {
		if uid, err := peerUID(conn); err != nil || int(uid) != os.Getuid() {
			t.Errorf("peerUID of the %s end = %d, %v; want %d", name, uid, err, os.Getuid())
		}
	}
	a, b := net.Pipe()
	defer func() { _ = a.Close(); _ = b.Close() }()
	if _, err := peerUID(a); err == nil {
		t.Error("peerUID of a connection that is not of a socket works")
	}
}

// TestRunTwice checks that a second daemon on the data directory of the first fails with the sentence of the engine, and one
// on its sockets with that of the socket, and that neither changes what the first has.
func TestRunTwice(t *testing.T) {
	d := startDaemon(t, nil, nil)
	_, _, socks2 := newDaemonDirs(t)

	cfg := d.cfg
	cfg.Sockets = socks2
	cfg.Ready = func() { t.Error("the second daemon is ready") }
	err := Run(t.Context(), cfg)
	if err == nil || !strings.Contains(err.Error(), "another process holds the writer lock") {
		t.Errorf("Run on the same data directory = %v, want the sentence of the engine", err)
	}
	if entries, _ := os.ReadDir(socks2); len(entries) != 0 {
		t.Errorf("the second daemon has made %d files in its directory of sockets", len(entries))
	}

	_, data2, _ := newDaemonDirs(t)
	if err := CreateDir(data2, d.cfg.Options); err != nil {
		t.Fatal(err)
	}
	cfg = d.cfg
	cfg.Data = data2
	cfg.Ready = func() { t.Error("the second daemon is ready") }
	err = Run(t.Context(), cfg)
	want := "listen unix " + filepath.Join(d.socks, dbwire.WriterSocket) +
		": another process listens on the socket, which is left as it is"
	if err == nil || err.Error() != want {
		t.Errorf("Run on the same sockets = %v, want %q", err, want)
	}
	// The data directory of the second is free again, and the first still serves.
	srv, err := OpenDir(data2, d.cfg.Options)
	if err != nil {
		t.Errorf("OpenDir() of the data directory of the failed daemon = %v", err)
	} else {
		_ = srv.Close()
	}
	d.dial(dbwire.WriterSocket, false).mustCommit(put(2, "a", "1"))
}

// TestRunStaleSocket checks that a socket file that nobody listens on is replaced.
func TestRunStaleSocket(t *testing.T) {
	_, data, socks := newDaemonDirs(t)
	if err := CreateDir(data, testOptions()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{dbwire.WriterSocket, dbwire.ReaderSocket} {
		l, err := net.Listen("unix", filepath.Join(socks, name))
		if err != nil {
			t.Fatal(err)
		}
		l.(*net.UnixListener).SetUnlinkOnClose(false)
		_ = l.Close()
	}
	ready := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{Data: data, Sockets: socks, WriterUID: uint32(os.Getuid()), WriterGID: -1, ReaderGID: -1,
			Options: testOptions(), Ready: func() { close(ready) }})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("Run = %v", err)
	}
	conn, err := net.Dial("unix", filepath.Join(socks, dbwire.WriterSocket))
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run = %v", err)
	}
}

// TestRunShutdown checks that a request in progress when the context ends is answered before its connection closes, that no
// new connection is taken, and that Run waits for the request.
func TestRunShutdown(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	d := startDaemon(t, nil, func(c *Config) {
		c.Options.Hook = func(point string) error {
			if point == "commit:gate" && once.CompareAndSwap(false, true) {
				close(entered)
				<-release
			}
			return nil
		}
	})
	w := d.dial(dbwire.WriterSocket, false)
	res := make(chan error, 1)
	go func() {
		_, _, err := w.tryCommit(put(2, "a", "1"), put(dbwire.TableHeads, "activity", headLine(1)))
		res <- err
	}()
	<-entered
	d.cancel()
	eventually(t, "the sockets to be closed", func() bool {
		_, err := net.Dial("unix", filepath.Join(d.socks, dbwire.ReaderSocket))
		return err != nil
	})
	select {
	case err := <-d.done:
		t.Fatalf("Run returned %v while a request is in progress", err)
	default:
	}
	close(release)
	if err := <-res; err != nil {
		t.Errorf("the request in progress: %v", err)
	}
	if err := <-d.done; err != nil {
		t.Errorf("Run = %v", err)
	}
	d.stopped = true
	w.wantClosed()
	// The heads are written after the requests have ended: the last write has that of the request in progress.
	if got := d.heads.all(); len(got) != 1 || got[0] != headLine(1)+"\n" {
		t.Errorf("the lines of the heads are %q, want the head that the request in progress has put", got)
	}
	srv, err := OpenDir(d.data, d.cfg.Options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	if got := (&fixture{t: t, srv: srv}).dump().Seq; got != 1 {
		t.Errorf("the store has the sequence number %d, want 1: the request in progress has been committed", got)
	}
}

// TestRunErrors checks what stops Run before it serves: a path of a socket that is too long, a data directory that is not
// one, a group that the process may not give a socket; and that none leaves a socket, or the data directory locked.
func TestRunErrors(t *testing.T) {
	t.Run("a path that is too long", func(t *testing.T) {
		long := filepath.Join(t.TempDir(), strings.Repeat("d", 100))
		missing := filepath.Join(t.TempDir(), "missing")
		err := Run(t.Context(), Config{Data: missing, Sockets: long, WriterGID: -1, ReaderGID: -1})
		want := "the path of a socket has at most 107 bytes"
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Run = %v, want %q in it", err, want)
		}
		// Before anything is opened: the data directory is not even looked at.
		if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the data directory: %v", err)
		}
		if _, err := os.Lstat(long); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the directory of the sockets: %v", err)
		}
	})
	t.Run("a data directory that is missing", func(t *testing.T) {
		_, data, socks := newDaemonDirs(t)
		err := Run(t.Context(), Config{Data: data, Sockets: socks, WriterGID: -1, ReaderGID: -1})
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Run = %v, want an error that wraps ErrNotExist", err)
		}
		if entries, _ := os.ReadDir(socks); len(entries) != 0 {
			t.Errorf("the directory of the sockets has %d files", len(entries))
		}
	})
	t.Run("a directory of sockets that is missing", func(t *testing.T) {
		_, data, socks := newDaemonDirs(t)
		if err := CreateDir(data, testOptions()); err != nil {
			t.Fatal(err)
		}
		err := Run(t.Context(), Config{Data: data, Sockets: filepath.Join(socks, "none"), WriterGID: -1, ReaderGID: -1,
			Options: testOptions()})
		if err == nil || !strings.Contains(err.Error(), "no such file or directory") {
			t.Errorf("Run = %v", err)
		}
		srv, err := OpenDir(data, testOptions())
		if err != nil {
			t.Fatalf("the data directory is locked after the failure: %v", err)
		}
		_ = srv.Close()
	})
	t.Run("a group that the process is not in", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root may give any group")
		}
		groups, _ := os.Getgroups()
		gid := 1
		for slices.Contains(groups, gid) || gid == os.Getegid() {
			gid++
		}
		_, data, socks := newDaemonDirs(t)
		if err := CreateDir(data, testOptions()); err != nil {
			t.Fatal(err)
		}
		err := Run(t.Context(), Config{Data: data, Sockets: socks, WriterGID: -1, ReaderGID: gid, Options: testOptions()})
		if !errors.Is(err, syscall.EPERM) {
			t.Errorf("Run = %v, want an error that wraps EPERM", err)
		}
		if entries, _ := os.ReadDir(socks); len(entries) != 0 {
			t.Errorf("the directory of the sockets has %d files after the failure", len(entries))
		}
		srv, err := OpenDir(data, testOptions())
		if err != nil {
			t.Fatalf("the data directory is locked after the failure: %v", err)
		}
		_ = srv.Close()
	})
}

// TestServeCloseAtBound checks that closing the listener ends Serve while it waits for a slot, with its sessions still open.
func TestServeCloseAtBound(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Limits.Readers = 1 })
	dir, err := os.MkdirTemp("", "wr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	done := f.serve(l, dbwire.RoleReader, nil)
	var conns []net.Conn
	for range 18 {
		c, err := net.Dial("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		conns = append(conns, c)
	}
	eventually(t, "Serve to wait for a slot", func() bool { return serving("dbserver.(*Server).takeSlot") })
	eventually(t, "the 17 sessions", func() bool {
		f.srv.mu.Lock()
		defer f.srv.mu.Unlock()
		return len(f.srv.sessions) == 17
	})
	_ = l.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve = %v", err)
		}
	case <-time.After(waitLimit):
		t.Fatal("Serve does not end when its listener is closed while it waits for a slot")
	}
	// The sessions go on.
	if err := f.clientOf(conns[0]).sayHello(); err != nil {
		t.Errorf("Hello after the listener is closed: %v", err)
	}
}

// TestRunGroups checks that the daemon gives each socket the group that the configuration asks for, and leaves the group of
// the file with -1. It needs a group of the user other than its own, which it takes from the groups that the user is in.
func TestRunGroups(t *testing.T) {
	groups, _ := os.Getgroups()
	var others []int
	for _, g := range groups {
		if g != os.Getegid() && !slices.Contains(others, g) {
			others = append(others, g)
		}
	}
	if len(others) == 0 {
		t.Skip("the user is in no group but its own")
	}
	writerGID, readerGID := others[0], others[len(others)-1]
	group := func(d *daemon, name string) int {
		t.Helper()
		info, err := os.Lstat(filepath.Join(d.socks, name))
		if err != nil {
			t.Fatal(err)
		}
		return int(info.Sys().(*syscall.Stat_t).Gid)
	}
	d := startDaemon(t, nil, func(c *Config) { c.WriterGID, c.ReaderGID = writerGID, readerGID })
	if got := group(d, dbwire.WriterSocket); got != writerGID {
		t.Errorf("the group of the writer socket is %d, want %d", got, writerGID)
	}
	if got := group(d, dbwire.ReaderSocket); got != readerGID {
		t.Errorf("the group of the reader socket is %d, want %d", got, readerGID)
	}
	if err := d.stop(); err != nil {
		t.Fatal(err)
	}
	// -1 leaves the group of the file, which is that of the process.
	d = startDaemon(t, nil, nil)
	for _, name := range []string{dbwire.WriterSocket, dbwire.ReaderSocket} {
		if got := group(d, name); got != os.Getegid() {
			t.Errorf("the group of %s is %d, want %d, that of the process", name, got, os.Getegid())
		}
	}
}
