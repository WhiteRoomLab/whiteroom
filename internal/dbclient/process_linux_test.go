package dbclient_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver/dbtest"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// The environment of a helper process: its mode, the data directory and the directory of the sockets of the daemon, and
// the point at which it stops.
const (
	envHelper  = "WHITEROOM_DBCLIENT_HELPER"
	envData    = "WHITEROOM_DBCLIENT_DATA"
	envSockets = "WHITEROOM_DBCLIENT_SOCKETS"
	envPoint   = "WHITEROOM_DBCLIENT_POINT"
)

// helperMark starts the lines that a helper prints to stdout to tell where it is.
const helperMark = "dbclient-helper: "

// What the Update and the PutOriginal of the kill tests write: a row, an object that is a file of the engine, and an event;
// and an original that has more than one frame.
const killEvent = `{"kill":true}`

var (
	killObject   = []byte(strings.Repeat("an object in a file ", 100))
	killHash     = sha256.Sum256(killObject)
	killOriginal = data(3*testFrame+17, 5)
	killOrigHash = sha256.Sum256(killOriginal)
)

func killUpdate(tx *dbclient.Tx) error {
	if err := tx.Put(2, []byte("k"), []byte("v")); err != nil {
		return err
	}
	if err := tx.PutObject(killHash, killObject); err != nil {
		return err
	}
	return tx.AppendEvent([]byte(killEvent))
}

// helperOptions are the options of the daemons of the kill tests, which have the data directory on the OS.
func helperOptions() dbtest.Options {
	return dbtest.Options{
		Log: &logdb.Options{Placement: logdb.Hybrid, Threshold: 64, WriterWait: 5 * time.Second},
		Limits: &dbwire.Limits{Frame: testFrame, SnapshotIdle: -1, Stall: waitLimit, CommitsPerSecond: -1, MinFree: -1,
			Readers: 8},
	}
}

// block is a hook of the daemon that prints its point and stops there, if it is the point of the process.
func block(point string) func(string) error {
	return func(at string) error {
		if at != point {
			return nil
		}
		fmt.Println(helperMark + at)
		time.Sleep(time.Minute)
		return errors.New("not killed")
	}
}

// blockingReader gives n bytes and then stops, with a line that says so.
type blockingReader struct{ n int }

func (r *blockingReader) Read(p []byte) (int, error) {
	if r.n == 0 {
		fmt.Println(helperMark + "original")
		time.Sleep(time.Minute)
		return 0, errors.New("not killed")
	}
	n := min(len(p), r.n)
	copy(p[:n], data(n, 1))
	r.n -= n
	return n, nil
}

// TestHelperProcess is the process that the tests below run, with the environment above. In the mode daemon, it is a
// daemon on the data directory, with sockets, that stops at the hook point of the environment; in the mode client it
// is a client of the daemon of the sockets, that stops at its point: object (after the PutObject of an Update), commit
// (in a Commit that the daemon has read a part of), original (in a PutOriginal) and view (in a View that has read). On its
// own it does nothing.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv(envHelper)
	if mode == "" {
		t.Skip("run by the tests of other processes")
	}
	// A helper that nobody has killed ends by itself.
	time.AfterFunc(2*time.Minute, func() { os.Exit(3) })
	point, sockets := os.Getenv(envPoint), os.Getenv(envSockets)
	switch mode {
	case "daemon":
		o := helperOptions()
		hook := block(point)
		o.Log.Hook, o.Hook = hook, hook
		h, err := dbtest.Acquire(os.Getenv(envData), o)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.Listen(sockets); err != nil {
			t.Fatal(err)
		}
		fmt.Println(helperMark + "ready")
		time.Sleep(time.Minute)
	case "client":
		rec := &recorder{Dialer: dbclient.SocketDir(sockets)}
		c, err := dbclient.Open(bg, rec, dbwire.RoleWriter, dbclient.Options{DialWait: 10 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		fmt.Println(helperMark + "ready")
		switch point {
		case "object":
			err = c.Update(bg, func(tx *dbclient.Tx) error {
				if err := killUpdate(tx); err != nil {
					return err
				}
				fmt.Println(helperMark + "object")
				time.Sleep(time.Minute)
				return errors.New("not killed")
			})
		case "commit":
			rec.before = func(_ *recConn, kind dbwire.Kind, frame int, _ bool) error {
				if kind == dbwire.KindCommit && frame == 2 {
					fmt.Println(helperMark + "commit")
					time.Sleep(time.Minute)
				}
				return nil
			}
			// The ops that come first are in the frames that the daemon reads: a daemon that went on with what it has read would
			// commit a row and an event.
			err = c.Update(bg, func(tx *dbclient.Tx) error {
				if err := tx.Put(2, []byte("first"), []byte("1")); err != nil {
					return err
				}
				if err := tx.AppendEvent([]byte(killEvent)); err != nil {
					return err
				}
				return tx.Put(2, []byte("big"), bytes.Repeat([]byte("v"), 10*testFrame))
			})
		case "original":
			_, _, err = c.PutOriginal(bg, &blockingReader{n: 3 * testFrame})
		case "view":
			err = c.View(bg, func(tx *dbclient.Tx) error {
				if _, _, err := tx.Get(2, []byte("a")); err != nil {
					return err
				}
				fmt.Println(helperMark + "view")
				time.Sleep(time.Minute)
				return errors.New("not killed")
			})
		}
		t.Fatalf("not killed at %s: %v", point, err)
	}
	t.Fatalf("not killed in the mode %s", mode)
}

// helper is a process that runs TestHelperProcess.
type helper struct {
	cmd     *exec.Cmd
	mode    string
	mu      sync.Mutex
	marks   []string      // what it has printed after helperMark, guarded by mu
	changed chan struct{} // gets a value when marks grows
	ended   chan struct{} // closed at the end of its stdout
	stderr  bytes.Buffer  // complete when wait has returned
	wait    func() error  // waits for the end of its stdout and of the process, once
}

// startHelper runs TestHelperProcess in mode on the store in dir, with the sockets in sockets, and returns when it has
// printed "ready". The end of the test kills it, and logs its output if the test has failed.
func startHelper(t *testing.T, mode, dir, sockets, point string) *helper {
	t.Helper()
	h := &helper{cmd: exec.Command(os.Args[0], "-test.run=^TestHelperProcess$"), mode: mode,
		changed: make(chan struct{}, 1), ended: make(chan struct{})}
	// With -race, a process sleeps for a second before it exits unless atexit_sleep_ms is 0.
	h.cmd.Env = append(os.Environ(), envHelper+"="+mode, envData+"="+dir, envSockets+"="+sockets, envPoint+"="+point,
		"GORACE="+strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	h.cmd.Stderr = &h.stderr
	out, err := h.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := h.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(h.ended)
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if m, ok := strings.CutPrefix(sc.Text(), helperMark); ok {
				h.mu.Lock()
				h.marks = append(h.marks, m)
				h.mu.Unlock()
				select {
				case h.changed <- struct{}{}:
				default:
				}
			}
		}
	}()
	h.wait = sync.OnceValue(func() error {
		<-h.ended
		return h.cmd.Wait()
	})
	t.Cleanup(func() {
		_ = h.cmd.Process.Kill()
		err := h.wait()
		if t.Failed() {
			h.mu.Lock()
			defer h.mu.Unlock()
			t.Logf("the helper in the mode %s: %v\nmarks: %q\nstderr:\n%s", mode, err, h.marks, h.stderr.Bytes())
		}
	})
	h.waitMark(t, "ready")
	return h
}

// waitMark waits until the helper has printed want, and fails the test if it ends first or does not within waitLimit.
func (h *helper) waitMark(t *testing.T, want string) {
	t.Helper()
	deadline := time.NewTimer(waitLimit)
	defer deadline.Stop()
	for {
		h.mu.Lock()
		marks := slices.Clone(h.marks)
		h.mu.Unlock()
		if slices.Contains(marks, want) {
			return
		}
		select {
		case <-h.changed:
		case <-h.ended:
			h.mu.Lock()
			marks := slices.Clone(h.marks)
			h.mu.Unlock()
			if !slices.Contains(marks, want) {
				t.Fatalf("the helper in the mode %s has ended after printing %q, and not %q", h.mode, marks, want)
			}
			return
		case <-deadline.C:
			t.Fatalf("the helper in the mode %s has not printed %q in %v, but %q", h.mode, want, waitLimit, marks)
		}
	}
}

// lastMark returns the last line that the helper has printed.
func (h *helper) lastMark() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.marks) == 0 {
		return ""
	}
	return h.marks[len(h.marks)-1]
}

// kill kills the helper with SIGKILL, and waits for it: it must have died of the signal.
func (h *helper) kill(t *testing.T) {
	t.Helper()
	if err := h.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err := h.wait()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("the helper has ended with %v, want SIGKILL", err)
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("the helper has ended with %v, want SIGKILL", err)
	}
}

// socketDir makes a directory for the sockets of a daemon, which has a path short enough for them.
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

// stackCount returns how many goroutines have marker in their stacks.
func stackCount(marker string) int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	count := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, marker) {
			count++
		}
	}
	return count
}

// The goroutines of the daemon of this process that a connection has.
const (
	sessionMarker  = "internal/dbserver.(*session).serve"
	snapshotMarker = "internal/dbserver.(*Server).openSnapshot"
	commitMarker   = "internal/dbserver.(*session).commitRequest"
)

// dirFiles returns the regular files under dir: the paths relative to dir, and what they hold.
func dirFiles(t testing.TB, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || !e.Type().IsRegular() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		m[rel] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// dirBytes returns the size of the files in dir, which may be missing.
func dirBytes(t testing.TB, dir string) int64 {
	t.Helper()
	var n int64
	for _, b := range dirFiles(t, dir) {
		n += int64(len(b))
	}
	return n
}

func tempIn(files map[string]string) []string {
	var temps []string
	for p := range files {
		if strings.HasPrefix(filepath.Base(p), ".tmp-") {
			temps = append(temps, p)
		}
	}
	return temps
}

// killStore is a data directory with a few commits and an original, and what it had then.
type killStore struct {
	dir, sockets string
	before       state
	files        map[string]string // files/ and what it holds
	logBytes     int64             // the segments of the engine
	old          [32]byte          // the original that it has
}

// newKillStore makes a data directory on the OS, and gives it commits through a daemon of this process, which it closes.
func newKillStore(t *testing.T) *killStore {
	t.Helper()
	ks := &killStore{dir: t.TempDir(), sockets: socketDir(t)}
	o := helperOptions()
	must(t, dbtest.Init(ks.dir, o))
	root, err := dbtest.Acquire(ks.dir, o)
	must(t, err)
	client, err := dbtest.Acquire(ks.dir, o)
	must(t, err)
	c, err := dbclient.Open(bg, client.Dialer(), dbwire.RoleWriter, dbclient.Options{})
	must(t, err)
	seed(t, c)
	commit(t, c, func(tx *dbclient.Tx) { put(t, tx, 2, "a", "1"); put(t, tx, 2, "b", "2") })
	ks.old = putOriginal(t, c, data(5000, 9))
	commit(t, c, func(tx *dbclient.Tx) { put(t, tx, 2, "c", "3") })
	ks.before, err = readState(dbserver.EngineOf(root.Server()))
	must(t, err)
	must(t, c.Close())
	must(t, root.Release())
	ks.files = dirFiles(t, filepath.Join(ks.dir, "files"))
	ks.logBytes = dirBytes(t, filepath.Join(ks.dir, "logdb", "log"))
	if len(ks.files) != 1 || ks.logBytes == 0 {
		t.Fatalf("the store has the files %v and %d bytes of log", ks.files, ks.logBytes)
	}
	return ks
}

// openDaemon starts a daemon of this process on the data directory, on its sockets.
func (ks *killStore) openDaemon(t *testing.T) *dbtest.Daemon {
	t.Helper()
	d, err := dbtest.Acquire(ks.dir, helperOptions())
	if err != nil {
		t.Fatalf("the daemon does not open the store that was killed: %v", err)
	}
	t.Cleanup(func() {
		if err := d.Release(); err != nil {
			t.Errorf("Release: %v", err)
		}
	})
	must(t, d.Listen(ks.sockets))
	return d
}

// wholeCommit tells whether after has the commit of killUpdate that before does not have: "none" if the store is as it was,
// "all" if it has the row, the object, the event and the mark of the commit, and nothing else; it fails the test if it
// has some of them.
func wholeCommit(t *testing.T, before, after state) string {
	t.Helper()
	if reflect.DeepEqual(before, after) {
		return "none"
	}
	fail := func(format string, args ...any) string {
		t.Helper()
		t.Errorf("the store has a part of a commit: "+format, args...)
		return "part"
	}
	if after.Seq != before.Seq+1 {
		return fail("the commit %d after the commit %d", after.Seq, before.Seq)
	}
	for table := uint32(1); table <= 12; table++ {
		want := slices.Clone(before.Tables[table])
		if table == 2 {
			want = append(want, fmt.Sprintf("%q=%s", "k", digest([]byte("v"))))
		}
		if !slices.Equal(want, after.Tables[table]) {
			return fail("table %d has %q, want %q", table, after.Tables[table], want)
		}
	}
	wantObjects := append(slices.Clone(before.Objects), object{killHash, int64(len(killObject))})
	less := func(a, b object) int { return bytes.Compare(a.Hash[:], b.Hash[:]) }
	slices.SortFunc(wantObjects, less)
	gotObjects := slices.Clone(after.Objects)
	slices.SortFunc(gotObjects, less)
	if !slices.Equal(wantObjects, gotObjects) {
		return fail("the objects are %v, want %v", gotObjects, wantObjects)
	}
	n := len(before.Events)
	if len(after.Events) != n+2 || !reflect.DeepEqual(after.Events[:n], before.Events) {
		return fail("%d events after %d", len(after.Events), n)
	}
	ev, mark := after.Events[n], after.Events[n+1]
	if _, ok := dbwire.ParseMark(mark.Data); ev.Number != uint64(n+1) || ev.Seq != after.Seq || string(ev.Data) != killEvent ||
		mark.Number != uint64(n+2) || mark.Seq != after.Seq || !ok {
		return fail("the events of the commit are %+v and %+v", ev, mark)
	}
	return "all"
}

// TestKillDaemon runs a daemon in another process and kills it at each point of a commit and of an original: a new
// daemon on the directory has the whole commit or none of it, and the original whole or not at all, and the client
// never says that it made a commit that is not there, nor that it did not make one that is.
func TestKillDaemon(t *testing.T) {
	noLeaks(t)
	for _, c := range []struct {
		point  string
		commit bool
		want   string // of the store, with a process that is killed: "none", "all", or "either"
	}{
		{"update:objects", true, "none"},
		{"update:written", true, "either"},
		{"update:synced", true, "all"},
		{"update:published", true, "all"},
		{"original:synced", false, "none"},
		{"original:renamed", false, "all"},
	} {
		t.Run(c.point, func(t *testing.T) {
			for _, back := range []bool{false, true} {
				if back && !c.commit {
					continue
				}
				name := "the daemon stays down"
				if back {
					name = "a new daemon comes"
				}
				t.Run(name, func(t *testing.T) { killDaemon(t, c.point, c.commit, c.want, back) })
			}
		})
	}
}

// killDaemon is a case of TestKillDaemon: back tells that a new daemon comes while the client of a commit looks for out
// whether its commit was made, and else that it has given up by then.
func killDaemon(t *testing.T, point string, isCommit bool, want string, back bool) {
	ks := newKillStore(t)
	d1 := startHelper(t, "daemon", ks.dir, ks.sockets, point)
	dialWait := time.Duration(-1)
	if back {
		dialWait = waitLimit
	}
	c, err := dbclient.Open(bg, dbclient.SocketDir(ks.sockets), dbwire.RoleWriter, dbclient.Options{DialWait: dialWait})
	must(t, err)
	t.Cleanup(func() { _ = c.Close() })

	ctx, cancel := context.WithTimeout(bg, 2*waitLimit)
	defer cancel()
	type result struct {
		err  error
		hash [32]byte
		size int64
	}
	done := make(chan result, 1)
	go func() {
		var r result
		if isCommit {
			r.err = c.Update(ctx, killUpdate)
		} else {
			r.hash, r.size, r.err = c.PutOriginal(ctx, bytes.NewReader(killOriginal))
		}
		done <- r
	}()
	d1.waitMark(t, point)
	if got := d1.lastMark(); got != point {
		t.Fatalf("the daemon has stopped at %q, want %q", got, point)
	}
	d1.kill(t)

	// What the daemon had done when it was killed.
	files := dirFiles(t, filepath.Join(ks.dir, "files"))
	logBytes := dirBytes(t, filepath.Join(ks.dir, "logdb", "log"))
	finalFile := fmt.Sprintf("%x/%x", killOrigHash[:1], killOrigHash) // in files/
	switch point {
	case "update:objects":
		if logBytes != ks.logBytes {
			t.Errorf("the log has %d bytes, and had %d: the record was written before the point", logBytes, ks.logBytes)
		}
	case "update:written", "update:synced", "update:published":
		if logBytes <= ks.logBytes {
			t.Errorf("the log has %d bytes, and had %d: the record was not written at the point", logBytes, ks.logBytes)
		}
	case "original:synced":
		temps := tempIn(files)
		if _, has := files[finalFile]; len(temps) != 1 || files[temps[0]] != string(killOriginal) || has {
			t.Errorf("at the point the files are %v, want the original in a temporary file and no more", slices.Sorted(maps.Keys(files)))
		}
	case "original:renamed":
		if files[finalFile] != string(killOriginal) || len(tempIn(files)) != 0 {
			t.Errorf("at the point the files are %v, want the original whole", slices.Sorted(maps.Keys(files)))
		}
	}
	if isCommit {
		objectFile := fmt.Sprintf("logdb/objects/%x/%x", killHash[:1], killHash)
		if _, ok := dirFiles(t, ks.dir)[objectFile]; !ok {
			t.Errorf("the object of the commit has no file %s at the point", objectFile)
		}
	}

	var r result
	if !back {
		r = <-done
	}
	d2 := ks.openDaemon(t)
	if back {
		select {
		case r = <-done:
		case <-time.After(2 * waitLimit):
			t.Fatal("the client has not ended the Update that was cut")
		}
	}
	engine := dbserver.EngineOf(d2.Server())
	after, err := readState(engine)
	must(t, err)
	must(t, checkObjects(engine, after.Objects))
	must(t, engine.Verify(bg))

	// The client's answer, and what the store has.
	if !isCommit {
		if r.err == nil || !errors.Is(r.err, dbclient.ErrUnavailable) {
			t.Errorf("PutOriginal: %v, want an error that says that the daemon cannot be reached", r.err)
		}
		if r.hash != ([32]byte{}) || r.size != 0 {
			t.Errorf("PutOriginal gave %x and %d with its error", r.hash, r.size)
		}
		if !reflect.DeepEqual(ks.before, after) {
			t.Error("the store has changed, but the kill was in an original")
		}
		got := dirFiles(t, filepath.Join(ks.dir, "files"))
		wantFiles := maps.Clone(ks.files)
		if want == "all" {
			wantFiles[finalFile] = string(killOriginal)
		}
		if !reflect.DeepEqual(got, wantFiles) {
			t.Errorf("the files after the new daemon has opened are %v, want %v", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(wantFiles)))
		}
		o, err := c.Original(ctx, killOrigHash)
		switch want {
		case "none":
			if !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("the original of a kill before the rename: %v, want that it is missing", err)
			}
		default:
			must(t, err)
			if got := readAll(t, o); !bytes.Equal(got, killOriginal) {
				t.Errorf("the original has %d bytes, want %d", len(got), len(killOriginal))
			}
		}
		// The caller sends it again.
		h, size, err := c.PutOriginal(ctx, bytes.NewReader(killOriginal))
		if err != nil || h != killOrigHash || size != int64(len(killOriginal)) {
			t.Errorf("PutOriginal again = %x, %d, %v", h, size, err)
		}
		wantFiles[finalFile] = string(killOriginal)
		if got := dirFiles(t, filepath.Join(ks.dir, "files")); !reflect.DeepEqual(got, wantFiles) {
			t.Errorf("the files after the second PutOriginal are %v", slices.Sorted(maps.Keys(got)))
		}
		if got := readOriginal(t, c, ks.old); !bytes.Equal(got, data(5000, 9)) {
			t.Error("the old original is not there")
		}
		return
	}

	made := wholeCommit(t, ks.before, after)
	if want != "either" && made != want {
		t.Errorf("the store has %s of the commit, want %s", made, want)
	}
	t.Logf("killed at %s: the store has %s of the commit, and the client says %v", point, made, r.err)
	switch {
	case !back:
		// The daemon is gone and nothing can tell: the client says that it does not know, in either case.
		if r.err == nil || !errors.Is(r.err, dbclient.ErrUnavailable) || !strings.Contains(r.err.Error(), "it is not known whether the commit was made") ||
			strings.Contains(r.err.Error(), "was not committed") {
			t.Errorf("Update: %v, want an error that says that it is not known whether the commit was made", r.err)
		}
	case made == "all":
		if r.err != nil {
			t.Errorf("the store has the commit, and Update says: %v", r.err)
		}
	default:
		if r.err == nil || !errors.Is(r.err, dbclient.ErrUnavailable) || !strings.Contains(r.err.Error(), "the transaction was not committed") {
			t.Errorf("the store has no commit, and Update says: %v", r.err)
		}
	}

	// The daemon goes on: the client's next Update is the next commit.
	must(t, c.Update(ctx, func(tx *dbclient.Tx) error {
		if err := tx.Put(2, []byte("next"), []byte("1")); err != nil {
			return err
		}
		return tx.AppendEvent([]byte(`{"next":true}`))
	}))
	next, err := readState(engine)
	must(t, err)
	if next.Seq != after.Seq+1 {
		t.Errorf("the next Update made the commit %d, want %d", next.Seq, after.Seq+1)
	}
	must(t, c.Verify(ctx))
	if got := readOriginal(t, c, ks.old); !bytes.Equal(got, data(5000, 9)) {
		t.Error("the old original is not there")
	}
	if temps := tempIn(dirFiles(t, filepath.Join(ks.dir, "files"))); len(temps) != 0 {
		t.Errorf("temporary files are left: %v", temps)
	}
}

func readAll(t testing.TB, o *dbclient.Original) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(o); err != nil {
		t.Fatal(err)
	}
	must(t, o.Close())
	return buf.Bytes()
}

// TestKillClient runs a client in another process and kills it where it is: after a PutObject, in a Commit that the daemon
// has read a part of, in a PutOriginal, and in a View with a snapshot. The daemon has made no commit of it, has no
// temporary file, has let go of what the connection had, and takes a new writer.
func TestKillClient(t *testing.T) {
	for _, c := range []struct {
		name, point string
		// ready waits until the daemon is where the point of the client puts it, and fails the test if it is not.
		ready func(t *testing.T, f *fixture)
	}{
		{"after a PutObject", "object", func(t *testing.T, f *fixture) {
			eventually(t, "the daemon to have the writer", func() bool { return stackCount(sessionMarker) >= 1 })
		}},
		{"in a Commit that is read in part", "commit", func(t *testing.T, f *fixture) {
			eventually(t, "the daemon to be in the Commit", func() bool { return stackCount(commitMarker) == 1 })
		}},
		{"in a PutOriginal", "original", func(t *testing.T, f *fixture) {
			eventually(t, "the daemon to have a temporary file with bytes in it", func() bool {
				for p, data := range f.originalFiles() {
					if strings.Contains(p, ".tmp-") && len(data) >= testFrame-100 {
						return true
					}
				}
				return false
			})
		}},
		{"in a View", "view", func(t *testing.T, f *fixture) {
			eventually(t, "the daemon to hold the snapshot", func() bool { return stackCount(snapshotMarker) == 1 })
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, nil)
			sockets := socketDir(t)
			must(t, f.root.Listen(sockets))
			w, _ := f.writer(dbclient.Options{})
			commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "a", "1"); put(t, tx, 2, "b", "2") })
			old := putOriginal(t, w, data(5000, 9))
			must(t, w.Close())
			eventually(t, "the sessions of the seeding client to end", func() bool { return stackCount(sessionMarker) == 0 })
			before, beforeFiles := f.state(), f.originalFiles()

			h := startHelper(t, "client", "", sockets, c.point)
			h.waitMark(t, c.point)
			c.ready(t, f)
			if got := h.lastMark(); got != c.point {
				t.Fatalf("the client has stopped at %q, want %q", got, c.point)
			}
			h.kill(t)

			// The daemon lets go of what the connection had.
			eventually(t, "the daemon to end the sessions of the client", func() bool { return stackCount(sessionMarker) == 0 })
			if n := stackCount(snapshotMarker); n != 0 {
				t.Errorf("the daemon holds %d snapshots", n)
			}
			f.noTemps()
			if after := f.state(); !reflect.DeepEqual(before, after) {
				t.Errorf("the store has changed:\n before %+v\n after  %+v", before, after)
			}
			if got := f.originalFiles(); !reflect.DeepEqual(got, beforeFiles) {
				t.Errorf("files/ has %v, want %v", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(beforeFiles)))
			}
			if n := f.seq(); n != before.Seq {
				t.Errorf("the daemon is at the commit %d, want %d", n, before.Seq)
			}

			// A new writer is let in at once, and the daemon goes on.
			start := time.Now()
			w2, err := dbclient.Open(bg, dbclient.SocketDir(sockets), dbwire.RoleWriter, dbclient.Options{DialWait: -1})
			if err != nil {
				t.Fatalf("a new writer is not let in at once: %v", err)
			}
			defer func() { _ = w2.Close() }()
			if d := time.Since(start); d > time.Second {
				t.Errorf("a new writer was let in after %v", d)
			}
			must(t, w2.Update(bg, killUpdate))
			wantState := f.state()
			if whole := wholeCommit(t, before, wantState); whole != "all" {
				t.Errorf("the commit of the new writer is %s", whole)
			}
			if got := readOriginal(t, w2, old); !bytes.Equal(got, data(5000, 9)) {
				t.Error("the old original is not there")
			}
			must(t, w2.Verify(bg))
		})
	}
}
