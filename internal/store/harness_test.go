package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver/dbtest"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// The tests of this package run on DB daemons inside the test process (internal/dbserver/dbtest): create, open and
// openLogReader start or join the daemon of a data directory, and connect a store to it over net.Pipe. The daemon is
// released when the store is closed.

// options are those of the stores that the tests make with create, open and openLogReader: the engine of the daemon
// with *log, or with logdb.Options{Placement: logdb.Hybrid} if log is nil, whose Hook and Accept are those of the daemon
// and the store; the data directory is fs if it is not nil, in place of dir on the OS, which then only names it in
// errors; now is the clock of the versions (nil: time.Now), also that of the marks of the daemon; limits are those of
// the daemon (nil: no limit of commits per second, and no check of the free space). The first store of a data
// directory in the process starts its daemon with them, and the others join it.
type options struct {
	log    *logdb.Options
	fs     vfs.FS
	now    func() time.Time
	limits *dbwire.Limits
	wait   time.Duration // how long the store tries to connect as the writer while another is connected; 0: 2 seconds
	// boot is the boot ID of the machine that the daemon sees; nil: the real one.
	boot func() (string, error)
}

func (o options) clock() func() time.Time {
	if o.now != nil {
		return o.now
	}
	return time.Now
}

// stopBootID is the boot ID of the machine in the worlds where a test makes the daemon stop, which then cannot open them
// again in the same boot; a restart of the machine, which a crash models, opens in the real boot, which differs.
const stopBootID = "00000000-0000-4000-8000-000000000068"

func stopBoot() (string, error) { return stopBootID, nil }

// daemonState is what the harness keeps of the daemon of a data directory.
type daemonState struct {
	writer atomic.Pointer[Store] // the last store opened as its writer, whose hook the daemon calls
	refs   int                   // the stores that use the daemon, under harness.mu
}

func (d *daemonState) call(point string) error {
	if s := d.writer.Load(); s != nil {
		return s.call(point)
	}
	return nil
}

// storeInfo is what the harness keeps of a store.
type storeInfo struct {
	h    *dbtest.Daemon
	dir  string
	fsys vfs.FS // of the data directory: o.fs, or nil for dir on the OS
}

var harness struct {
	mu      sync.Mutex
	daemons map[*dbserver.Server]*daemonState
	stores  map[*Store]*storeInfo
	live    atomic.Int64 // the dialers that have been made and not closed
}

func TestMain(m *testing.M) {
	code := m.Run()
	// A store that a test forgets to close keeps its daemon, with its engine and goroutines.
	var leaked []string
	for range 200 {
		leaked = leakedGoroutines()
		if harness.live.Load() == 0 && leaked == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := harness.live.Load(); n != 0 && code == 0 {
		fmt.Fprintf(os.Stderr, "FAIL: %d stores were not closed\n", n)
		code = 1
	}
	if leaked != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "FAIL: %d goroutines of the daemon or of its client are left:\n%s\n", len(leaked),
			strings.Join(leaked, "\n\n"))
		code = 1
	}
	os.Exit(code)
}

// leakedGoroutines returns the stacks of the goroutines that are in the code of the daemon or of its client, which no
// test has any more after m.Run.
func leakedGoroutines() []string {
	var buf strings.Builder
	if err := pprof.Lookup("goroutine").WriteTo(&buf, 2); err != nil {
		return nil
	}
	var leaked []string
	for _, g := range strings.Split(buf.String(), "\n\n") {
		if strings.Contains(g, "internal/dbserver.") || strings.Contains(g, "internal/dbclient.") {
			leaked = append(leaked, g)
		}
	}
	return leaked
}

// tempName matches the name of a temporary file of the daemon in files/.
var tempName = regexp.MustCompile(`^\.tmp-[0-9a-f]{16}$`)

// dialer is the Dialer of a store of the harness, which gives back what the harness keeps of the store and of its daemon
// when it is closed.
type dialer struct {
	dbclient.Dialer
	name  string
	state *daemonState
	srv   *dbserver.Server
	store atomic.Pointer[Store] // the store that was made with it, once it is made
	once  sync.Once
	err   error
}

func (d *dialer) String() string { return d.name }

func (d *dialer) Close() error {
	d.once.Do(func() {
		if cl, ok := d.Dialer.(interface{ Close() error }); ok {
			d.err = cl.Close()
		}
		harness.live.Add(-1)
		harness.mu.Lock()
		if d.state.refs--; d.state.refs == 0 {
			delete(harness.daemons, d.srv)
		}
		if s := d.store.Load(); s != nil {
			delete(harness.stores, s)
		}
		harness.mu.Unlock()
	})
	return d.err
}

// daemonOptions returns the options of the daemon for dbtest, whose hooks call the hook of the writer store of st.
func (o options) daemonOptions(st *daemonState) dbtest.Options {
	l := logdb.Options{Placement: logdb.Hybrid}
	if o.log != nil {
		l = *o.log
	}
	if st != nil {
		l.Hook = func(point string) error { return st.call("logdb:" + point) }
	}
	do := dbtest.Options{FS: o.fs, Log: &l, Limits: o.limits, Now: o.now, Boot: o.boot}
	if st != nil {
		// The points of the originals; "commit:gate" and those of Backup are the daemon's and not the store's.
		do.Hook = func(point string) error {
			if strings.HasPrefix(point, "original:") {
				return st.call(point)
			}
			return nil
		}
	}
	return do
}

// acquire starts or joins the daemon of the data directory dir, and returns a dialer for a store of it.
func acquire(dir string, o options) (*dbtest.Daemon, *dialer, error) {
	st := &daemonState{}
	h, err := dbtest.Acquire(dir, o.daemonOptions(st))
	if err != nil {
		return nil, nil, err
	}
	harness.mu.Lock()
	if harness.daemons == nil {
		harness.daemons = map[*dbserver.Server]*daemonState{}
	}
	if cur, ok := harness.daemons[h.Server()]; ok {
		st = cur
	} else {
		harness.daemons[h.Server()] = st
	}
	st.refs++
	harness.mu.Unlock()
	harness.live.Add(1)
	return h, &dialer{Dialer: h.Dialer(), name: h.Dialer().(fmt.Stringer).String(), state: st, srv: h.Server()}, nil
}

// start makes a store of the data directory dir with mk, which takes the dialer of a daemon that it joins.
func start(dir string, o options, writer bool, mk func(dbclient.Dialer) (*Store, error)) (*Store, error) {
	h, d, err := acquire(dir, o)
	if err != nil {
		return nil, err
	}
	s, err := mk(d)
	if err != nil {
		return nil, err
	}
	harness.mu.Lock()
	if harness.stores == nil {
		harness.stores = map[*Store]*storeInfo{}
	}
	harness.stores[s] = &storeInfo{h: h, dir: dir, fsys: o.fs}
	harness.mu.Unlock()
	d.store.Store(s)
	if writer {
		d.state.writer.Store(s)
	}
	return s, nil
}

func (o options) client() dbclient.Options {
	return dbclient.Options{DialWait: o.wait}
}

// create makes the data directory dir, with a daemon, and the store in it, as Create does for a daemon that
// whiteroom-db init has made it for.
func create(dir string, o options) (*Store, error) {
	if err := dbtest.Init(dir, o.daemonOptions(nil)); err != nil {
		return nil, err
	}
	return start(dir, o, true, func(d dbclient.Dialer) (*Store, error) {
		return createOn(context.Background(), d, o.clock(), o.client())
	})
}

// open opens the store of the data directory dir as its writer, as Open does.
func open(dir string, o options) (*Store, error) {
	return start(dir, o, true, func(d dbclient.Dialer) (*Store, error) {
		return openOn(context.Background(), d, dbwire.RoleWriter, o.clock(), o.client())
	})
}

// openLogReader opens the store of the data directory dir to read it, as OpenReader does.
func openLogReader(dir string, o options) (*Store, error) {
	return start(dir, o, false, func(d dbclient.Dialer) (*Store, error) {
		return openOn(context.Background(), d, dbwire.RoleReader, o.clock(), o.client())
	})
}

// lookup returns what the harness keeps of s, or an error if the harness did not open it or it is closed.
func lookup(s *Store) (*storeInfo, error) {
	harness.mu.Lock()
	defer harness.mu.Unlock()
	info, ok := harness.stores[s]
	if !ok {
		return nil, errors.New("the store was not opened by the harness, or its daemon has been released")
	}
	return info, nil
}

func infoOf(t *testing.T, s *Store) *storeInfo {
	t.Helper()
	info, err := lookup(s)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// socketDir makes a directory for the sockets of a daemon, which has a path short enough for them, and removes it at the
// end of the test.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "wr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if n := len(filepath.Join(dir, dbwire.WriterSocket)); n > 100 {
		t.Fatalf("the path of a socket would have %d bytes: %s", n, dir)
	}
	return dir
}

// serve starts or joins the daemon of the data directory dir with o, with no store of its own, and makes it listen on
// the sockets in a new directory, which it returns, for the clients of other processes. The end of the test releases it.
func serve(t *testing.T, dir string, o options) (*dbtest.Daemon, string) {
	t.Helper()
	h, d, err := acquire(dir, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	sockets := socketDir(t)
	if err := h.Listen(sockets); err != nil {
		t.Fatalf("Listen() = %v", err)
	}
	return h, sockets
}

// daemonOf returns the handle of the daemon of s, which s was opened on.
func daemonOf(t *testing.T, s *Store) *dbtest.Daemon {
	t.Helper()
	return infoOf(t, s).h
}

// logDB returns the engine of the daemon of s.
func logDB(t *testing.T, s *Store) *logdb.DB {
	t.Helper()
	return dbserver.EngineOf(daemonOf(t, s).Server())
}

// dataFS returns the data directory of s as a vfs.FS: the one that s was opened with, or the directory on the OS, which
// the end of the test closes.
func dataFS(t *testing.T, s *Store) vfs.FS {
	t.Helper()
	info := infoOf(t, s)
	if info.fsys != nil {
		return info.fsys
	}
	root, err := vfs.OpenRoot(info.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

// logFS returns the directory logdb/ of s, which the end of the test closes.
func logFS(t *testing.T, s *Store) vfs.FS {
	t.Helper()
	sub, err := dataFS(t, s).Sub("logdb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	return sub
}

// backup is Backup of the data directory srcDir, or src if it is not nil, to dstDir, or dst if it is not nil, through a
// reader of the daemon of the source; the daemon of the source is the one that is running, if there is one, else a new
// one for the call.
func backup(ctx context.Context, srcDir string, src vfs.FS, dstDir string, dst vfs.FS) ([]audit.Head, error) {
	_, d, err := acquire(srcDir, options{fs: src})
	if err != nil {
		return nil, err
	}
	c, err := dbclient.Open(ctx, d, dbwire.RoleReader, dbclient.Options{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	return backupTo(ctx, c, srcDir, dstDir, dst, nil)
}

// failedAtHook reports whether err is what a call returns when the hook of the store fails with errHook at point: at
// "commit", which the store calls itself, the error of the hook; at the points of the daemon, the daemon's refusal, which
// does not say what failed.
func failedAtHook(err, errHook error, point string) bool {
	if point == "commit" {
		return errors.Is(err, errHook) && err.Error() == errHook.Error()
	}
	re, ok := errors.AsType[*dbwire.RemoteError](err)
	return ok && re.Code == dbwire.CodeFailed && re.Msg == "the engine failed"
}

// isStopped reports whether err is the refusal of the daemon after a sync has failed, with its sentence.
func isStopped(err error) bool {
	re, ok := errors.AsType[*dbwire.RemoteError](err)
	return ok && re.Code == dbwire.CodeStopped && strings.HasPrefix(re.Msg, "the store has stopped after a sync failed")
}

// eventually waits up to 5 seconds for ok to return true, and fails the test with what, "what must hold", if it does not.
func eventually(t *testing.T, ok func() bool, what string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Errorf("%s: not in 5 seconds", what)
			return
		}
	}
}

// testBackend is a backend that the tests run on: the engine of the daemon with *log.
type testBackend struct {
	name string
	log  *logdb.Options
}

// allBackends are the backends that the tests run on: the log backend in each placement of objects. With a threshold of
// 50 bytes, hybrid puts the JSON of docJSON in files, and that of revJSON and of the settings document inline. The log
// backend writes the checkpoints that the growth of its log starts in the Update, not in the background, so that no
// goroutine of the engine calls the hook of the store, which the tests change.
var allBackends = []testBackend{
	{name: "log-inline", log: &logdb.Options{Placement: logdb.Inline, WriterWait: writerWait, SyncCheckpoints: true}},
	{name: "log-files", log: &logdb.Options{Placement: logdb.Files, WriterWait: writerWait, SyncCheckpoints: true}},
	{name: "log-hybrid", log: &logdb.Options{Placement: logdb.Hybrid, Threshold: 50, WriterWait: writerWait,
		SyncCheckpoints: true}},
}

// writerWait is how long the engine of a daemon waits for the writer lock that another process holds (2 seconds by
// default), and how long a store of the harness tries to connect as the writer while another is connected, so that the
// tests of a second writer do not wait long.
const writerWait = 50 * time.Millisecond

// oneBackend is the log backend with inline objects, for what does not depend on the placement, such as the originals.
var oneBackend = allBackends[:1:1]

// forBackends runs f in a subtest of t, named by the backend, for each backend of set.
func forBackends(t *testing.T, set []testBackend, f func(t *testing.T, tb testBackend)) {
	t.Helper()
	for _, tb := range set {
		t.Run(tb.name, func(t *testing.T) { f(t, tb) })
	}
}

func backendByName(name string) (testBackend, bool) {
	for _, tb := range allBackends {
		if tb.name == name {
			return tb, true
		}
	}
	return testBackend{}, false
}

func (tb testBackend) options() options {
	return options{log: tb.log, now: func() time.Time { return testTime }}
}

// create makes a data directory with a store of tb at dir.
func (tb testBackend) create(dir string) (*Store, error) {
	return create(dir, tb.options())
}

// open opens the store of tb in the data directory dir.
func (tb testBackend) open(dir string) (*Store, error) {
	return open(dir, tb.options())
}

// newStore creates a store of tb in a new directory, and returns it with the directory. The end of the test closes it.
func newStore(t *testing.T, tb testBackend) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	s, err := tb.create(dir)
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	s.now = func() time.Time { return testTime }
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

// newDir creates a data directory of tb, and returns it with its store closed.
func newDir(t *testing.T, tb testBackend) string {
	t.Helper()
	s, dir := newStore(t, tb)
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	return dir
}

// reopen opens the store of tb in the data directory dir after a crash, and checks it. The end of the test closes it.
func reopen(t *testing.T, tb testBackend, dir string) *Store {
	t.Helper()
	s, err := tb.open(dir)
	if err != nil {
		t.Fatalf("Open() after the crash = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	checkIntegrity(t, s)
	checkConsistent(t, s)
	return s
}

// benchStore opens the store of tb in the data directory dir, or makes it there if fresh is true. It uses dir through a
// vfs.Counter, which it returns too.
func benchStore(t *testing.T, tb testBackend, dir string, fresh bool) (*Store, *vfs.Counter) {
	t.Helper()
	if fresh {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	root, err := vfs.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := vfs.Count(root)
	var s *Store
	if fresh {
		s, err = create(dir, options{log: tb.log, fs: c})
	} else {
		s, err = open(dir, options{log: tb.log, fs: c})
	}
	if err != nil {
		_ = c.Close()
		t.Fatal(err)
	}
	return s, c
}

// benchClose closes s and c, which benchStore has returned, and returns how long Close of s has taken.
func benchClose(t *testing.T, s *Store, c *vfs.Counter) int64 {
	t.Helper()
	start := time.Now()
	err := s.Close()
	d := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	return int64(d)
}
