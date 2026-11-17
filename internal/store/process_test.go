//go:build unix

package store

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver/dbtest"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// The processes of these tests are the helper (TestHelperProcess) in a mode, which is the daemon and the store in one
// process, the daemon alone (the mode daemon), or a client of the daemon of the test (a mode with envSockets); and the
// test, which runs the other half, if there is one, and checks the store after it has killed the helper.

// The environment of the helper process: its mode, the data directory, and the name of its backend.
const (
	envMode    = "WHITEROOM_STORE_HELPER"
	envDir     = "WHITEROOM_STORE_DIR"
	envBackend = "WHITEROOM_STORE_BACKEND"
	// envSockets is the directory of the sockets of the daemon of another process: the mode daemon listens on them, and
	// the other modes connect to them as clients, as a reader in the mode reader, and else as the writer.
	envSockets = "WHITEROOM_STORE_SOCKETS"
	// envPoint is the point of the hook of the daemon at which the mode daemon blocks.
	envPoint = "WHITEROOM_STORE_POINT"
	// envWait is how long a client of the sockets tries to connect, as a duration; 2 seconds if it is not set.
	envWait = "WHITEROOM_STORE_WAIT"
)

// marker starts the lines that the helper process prints to stdout to tell where it is.
const marker = "store-helper: "

// crashOriginal is the original that the helper puts.
var crashOriginal = bytes.Repeat([]byte("original "), 100_000)

// The points of the engine of the log backend at which the helper process blocks in an Update, in the modes of the same
// names.
var enginePoints = []string{"logdb:update:objects", "logdb:update:written", "logdb:update:synced",
	"logdb:update:published"}

// helperLimit is how long a helper that nobody kills runs.
const helperLimit = 2 * time.Minute

// TestHelperProcess is the process that the tests below run, with the environment of envMode, envDir and envBackend. In
// the modes commit, original:synced and original:renamed, it blocks at that point of the hook, where the test kills
// it, and so it does in the modes of enginePoints in an Update that puts the pair after the one in the store; in loop,
// it puts the pair until it is killed; in open, it prints the result of opening the store and ends; in hold, it keeps
// the store open until it is killed; in reader, it checks the pair in Views of the store of the sockets until it is
// killed (readerLoop); in original:partial, it blocks in a PutOriginal after the first bytes of the original. With
// envSockets, the store that these modes use is that of a client of the daemon that listens on the sockets, and not
// of a daemon of the process, so that only the points of the client are there: commit, and original:partial. In the
// mode daemon, it is the daemon of the data directory, which listens on the sockets, and blocks at the point of
// envPoint of its hooks: the points of enginePoints, commit:gate, original:synced and original:renamed. A helper ends
// by itself after helperLimit. On its own, it does nothing.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv(envMode)
	if mode == "" {
		t.Skip("run by the tests of crashes and of other processes")
	}
	time.AfterFunc(helperLimit, func() { os.Exit(3) })
	tb, ok := backendByName(os.Getenv(envBackend))
	if !ok {
		t.Fatalf("no backend %q", os.Getenv(envBackend))
	}
	dir, sockets := os.Getenv(envDir), os.Getenv(envSockets)
	if mode == "daemon" {
		runDaemon(t, tb, dir, sockets, os.Getenv(envPoint))
		return
	}
	var s *Store
	var err error
	switch wait := helperWait(t); {
	case mode == "reader":
		if sockets == "" {
			t.Fatal("the mode reader needs the sockets of a daemon")
		}
		s, err = openOn(context.Background(), dbclient.SocketDir(sockets), dbwire.RoleReader, time.Now, dbclient.Options{DialWait: wait})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		readerLoop(t, s)
		return
	case sockets != "":
		s, err = openOn(context.Background(), dbclient.SocketDir(sockets), dbwire.RoleWriter, time.Now, dbclient.Options{DialWait: wait})
	default:
		s, err = tb.open(dir)
	}
	if mode == "open" {
		result := "ok"
		if err != nil {
			result = err.Error()
		} else {
			_ = s.Close()
		}
		fmt.Println(marker + "open: " + result)
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	s.hook = func(point string) error {
		if point != mode {
			return nil
		}
		fmt.Println(marker + point)
		time.Sleep(time.Minute)
		return errors.New("not killed")
	}
	switch mode {
	case "commit":
		// Large data, which the engine writes only after f has returned.
		err = bigCommit(t.Context(), s)
	case "loop":
		for n := 1; err == nil; n++ {
			err = s.Update(t.Context(), withRecord(func(tx *Tx) error { return putPair(tx, n) }))
			if n == 20 && err == nil {
				fmt.Println(marker + "progress")
			}
		}
	case enginePoints[0], enginePoints[1], enginePoints[2], enginePoints[3]:
		var n [2]int
		view(t, s, func(tx *Tx) error {
			n, err = readPair(tx)
			return err
		})
		err = s.Update(t.Context(), withRecord(func(tx *Tx) error { return putPair(tx, n[0]+1) }))
	case "original:synced", "original:renamed":
		_, err = s.PutOriginal(t.Context(), bytes.NewReader(crashOriginal))
	case "original:partial":
		_, err = s.PutOriginal(t.Context(), &partialReader{n: 3 << 20})
	case "hold":
		fmt.Println(marker + "hold")
		time.Sleep(time.Minute)
	}
	t.Fatalf("not killed in the mode %s: %v", mode, err)
}

// helperWait returns how long a client of the sockets tries to connect: the duration of envWait, or 0 (the 2 seconds of
// the client) if it is not set.
func helperWait(t *testing.T) time.Duration {
	t.Helper()
	v := os.Getenv(envWait)
	if v == "" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		t.Fatalf("%s=%s: %v", envWait, v, err)
	}
	return d
}

// runDaemon is the mode daemon of TestHelperProcess: the daemon of the data directory dir of tb, with its sockets in
// the directory sockets, whose hooks block at point, if it is not "", after they have printed it.
func runDaemon(t *testing.T, tb testBackend, dir, sockets, point string) {
	t.Helper()
	block := func(at string) error {
		if at != point {
			return nil
		}
		fmt.Println(marker + at)
		time.Sleep(time.Minute)
		return errors.New("not killed")
	}
	log := matrixOptions(tb)
	log.Hook = func(at string) error { return block("logdb:" + at) }
	h, err := dbtest.Acquire(dir, dbtest.Options{Log: &log, Now: tb.options().now, Hook: block})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Listen(sockets); err != nil {
		t.Fatal(err)
	}
	fmt.Println(marker + "ready")
	time.Sleep(time.Minute)
	t.Fatal("not killed")
}

// bigCommit makes an Update of s that puts more than 4 MiB of JSON in a document and a revision.
func bigCommit(ctx context.Context, s *Store) error {
	big := strings.Repeat("x", 4<<20)
	return s.Update(ctx, withRecord(func(tx *Tx) error {
		data := []byte(`{"schemaVersion": 1, "id": "` + entryID + `", "type": "` + big + `"}`)
		if _, err := tx.Put(content.RefEntry, entryID, data, userID); err != nil {
			return err
		}
		if _, err := tx.Put(content.RefEntry, pageID, docJSON(content.RefEntry, pageID), userID); err != nil {
			return err
		}
		_, err := tx.AddRevision(entryID, revisionID, revJSON(big), userID)
		return err
	}))
}

// partialReader gives n bytes of an original, and then blocks, after it has printed that it has.
type partialReader struct{ n int }

func (r *partialReader) Read(p []byte) (int, error) {
	if r.n == 0 {
		fmt.Println(marker + "original:partial")
		time.Sleep(time.Minute)
		return 0, errors.New("not killed")
	}
	n := min(len(p), r.n)
	copy(p[:n], crashOriginal)
	r.n -= n
	return n, nil
}

// readerLoop checks the pair in Views of s, a store that is a reader client of a daemon, until the process is killed
// (checkPairView), and that its number never goes back. It prints "ready" first, "seen <n>" when it sees a new number
// n, "views <n>" after every 50 Views, and "violation: <what>" when a check fails, after which it waits to be killed.
func readerLoop(t *testing.T, s *Store) {
	fmt.Println(marker + "ready")
	seen := 0
	for views := 1; ; views++ {
		var n int
		err := s.View(t.Context(), func(tx *Tx) error {
			var err error
			n, err = checkPairView(tx)
			return err
		})
		if err == nil && n < seen {
			err = fmt.Errorf("the pair of %d after that of %d", n, seen)
		}
		if err != nil {
			fmt.Println(marker + "violation: " + err.Error())
			time.Sleep(time.Minute)
			t.Fatal("not killed")
		}
		if n > seen {
			fmt.Println(marker + "seen " + strconv.Itoa(n))
			seen = n
		}
		if views%50 == 0 {
			fmt.Println(marker + "views " + strconv.Itoa(views))
		}
		time.Sleep(time.Millisecond)
	}
}

// helper is a process that runs TestHelperProcess.
type helper struct {
	cmd     *exec.Cmd
	markers chan string  // what it prints after marker; closed at the end of its stdout
	stdout  []string     // the other lines of its stdout, complete when markers is closed
	stderr  bytes.Buffer // complete when wait has returned
	wait    func() error // waits for the end of its stdout and of the process, once
}

// startHelper runs TestHelperProcess in the mode on the data directory dir of tb, and returns once it has printed want
// after marker. It kills a helper that runs for 30 seconds. The end of the test kills it, and logs its output if the
// test has failed.
func startHelper(t *testing.T, tb testBackend, dir, mode, want string, env ...string) *helper {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	// With -race, a process sleeps for a second before it exits unless atexit_sleep_ms is 0.
	cmd.Env = append(append(os.Environ(), envMode+"="+mode, envDir+"="+dir, envBackend+"="+tb.name,
		"GORACE="+strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0")), env...)
	return startProcess(t, cmd, mode, want, 30*time.Second)
}

// startProcess starts cmd, which runs a test of the test binary in the mode, and returns it once it has printed want
// after marker, as startHelper does. It kills the process after limit.
func startProcess(t *testing.T, cmd *exec.Cmd, mode, want string, limit time.Duration) *helper {
	t.Helper()
	h := &helper{cmd: cmd, markers: make(chan string, 1)}
	h.cmd.Stderr = &h.stderr
	out, err := h.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := h.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(h.markers)
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if m, ok := strings.CutPrefix(sc.Text(), marker); ok {
				h.markers <- m
			} else {
				h.stdout = append(h.stdout, sc.Text())
			}
		}
	}()
	timer := time.AfterFunc(limit, func() { _ = h.cmd.Process.Kill() })
	h.wait = sync.OnceValue(func() error {
		for range h.markers {
		}
		err := h.cmd.Wait()
		timer.Stop()
		return err
	})
	t.Cleanup(func() {
		_ = h.cmd.Process.Kill()
		err := h.wait()
		if t.Failed() {
			t.Logf("the helper in the mode %s: %v\nstdout: %q\nstderr:\n%s", mode, err, h.stdout, h.stderr.Bytes())
		}
	})
	var other []string
	for m := range h.markers {
		if m == want {
			return h
		}
		other = append(other, m)
	}
	t.Fatalf("the helper in the mode %s has ended without printing %q, after %q", mode, marker+want, other)
	return nil
}

// kill kills the helper with SIGKILL, and waits for it.
func kill(t *testing.T, h *helper) {
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

// startDaemonHelper runs the daemon of the data directory dir of tb in a process of its own, which blocks at point (see
// TestHelperProcess), and returns it once it listens on its sockets, with the directory of them.
func startDaemonHelper(t *testing.T, tb testBackend, dir, point string) (*helper, string) {
	t.Helper()
	sockets := socketDir(t)
	return startHelper(t, tb, dir, "daemon", "ready", envSockets+"="+sockets, envPoint+"="+point), sockets
}

// clientOf makes a store that is a client of the daemon that listens on the sockets in the directory sockets, as role,
// which tries to connect for wait (see dbclient.Options). The end of the test closes it.
func clientOf(t *testing.T, sockets string, role dbwire.Role, wait time.Duration) *Store {
	t.Helper()
	s, err := openOn(context.Background(), dbclient.SocketDir(sockets), role, time.Now, dbclient.Options{DialWait: wait})
	if err != nil {
		t.Fatalf("a client of %s: %v", sockets, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// await waits for what ch gives, for 30 seconds.
func await(t *testing.T, ch <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(30 * time.Second):
		t.Fatalf("%s has not returned in 30 seconds", what)
		return nil
	}
}

// watchPair checks the pair in Views of s, until the function that it returns is called: no View may show the pair with
// two numbers, or a number before the one that an earlier View has shown. A View that cannot reach its daemon counts for
// nothing. The function returns the last number that a View has shown.
func watchPair(t *testing.T, s *Store) func() int {
	t.Helper()
	var seen atomic.Int64
	stop := make(chan struct{})
	var done sync.WaitGroup
	done.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			var n int
			err := s.View(context.Background(), func(tx *Tx) error {
				var err error
				n, err = checkPairView(tx)
				return err
			})
			switch {
			case errors.Is(err, dbclient.ErrUnavailable):
			case err != nil:
				t.Errorf("a View beside the kill: %v", err)
				return
			case int64(n) < seen.Load():
				t.Errorf("a View beside the kill shows the pair of %d after that of %d", n, seen.Load())
				return
			default:
				seen.Store(int64(n))
			}
			time.Sleep(time.Millisecond)
		}
	})
	return func() int {
		close(stop)
		done.Wait()
		return int(seen.Load())
	}
}

// checkPair checks that s has the pair with one number, which is from lo to hi, and that every term of it has as many
// versions, and returns it.
func checkPair(t *testing.T, s *Store, lo, hi int) int {
	t.Helper()
	n := 0
	view(t, s, func(tx *Tx) error {
		numbers, err := readPair(tx)
		if err != nil || numbers[0] != numbers[1] || numbers[0] < lo || numbers[0] > hi {
			t.Errorf("the names of the pair = %v, %v; want the same number, from %d to %d", numbers, err, lo, hi)
		}
		for _, id := range pair {
			if versions, err := tx.History(content.RefTerm, id); err != nil || len(versions) != numbers[0] {
				t.Errorf("History(%q) = %d versions, %v; want %d", id, len(versions), err, numbers[0])
			}
		}
		n = numbers[0]
		return nil
	})
	return n
}

// checkNoTemps checks that the directory files/ of the data directory dir has no temporary file of the daemon.
func checkNoTemps(t *testing.T, dir string) {
	t.Helper()
	for _, name := range names(t, filepath.Join(dir, "files")) {
		if tempName.MatchString(name) {
			t.Errorf("files/ has the temporary file %s", name)
		}
	}
}

// TestCrashBeforeCommit kills a process in an Update of more than 4 MiB of JSON before it commits, and checks that the
// store has nothing of it: the engine writes an Update only after f has returned (TestLogHooks has its points after
// that). The process is the daemon and the client together (the Update blocks at the point commit of the client), the
// daemon alone (it blocks before the Update of the engine, when it has read the whole request; the client of the test
// gets an error that does not say whether it was made), or the client alone (the daemon of the test has read some of it,
// and then the connection ends).
func TestCrashBeforeCommit(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		nothing := func(t *testing.T, s *Store) {
			t.Helper()
			checkEmpty(t, s)
			checkMissing(t, s, content.RefEntry, entryID, "entries/"+entryID+".json", nil)
			checkMissing(t, s, content.RefEntry, pageID, "entries/"+pageID+".json", nil)
			view(t, s, func(tx *Tx) error {
				if r, err := tx.Revision(revisionID); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("Revision() = %+v, %v; want an error that wraps fs.ErrNotExist", r, err)
				}
				return nil
			})
		}
		t.Run("the daemon and the client", func(t *testing.T) {
			dir := newDir(t, tb)
			before := logFiles(t, dir)
			h := startHelper(t, tb, dir, "commit", "commit")
			files := logFiles(t, dir)
			kill(t, h)
			if !maps.Equal(files, before) {
				t.Errorf("logdb/ had %v when the helper was killed, want %v as before the Update", files, before)
			}
			nothing(t, reopen(t, tb, dir))
		})
		t.Run("the daemon alone", func(t *testing.T) {
			dir := newDir(t, tb)
			before := logFiles(t, dir)
			h, sockets := startDaemonHelper(t, tb, dir, "commit:gate")
			s := clientOf(t, sockets, dbwire.RoleWriter, -1)
			done := make(chan error, 1)
			go func() { done <- bigCommit(t.Context(), s) }()
			waitMarkers(t, h, 30*time.Second, func(m string) bool { return m == "commit:gate" })
			files := logFiles(t, dir)
			kill(t, h)
			if !maps.Equal(files, before) {
				t.Errorf("logdb/ had %v when the daemon was killed, want %v as before the Update", files, before)
			}
			if err := await(t, done, "the Update of the client"); !errors.Is(err, dbclient.ErrUnavailable) {
				t.Errorf("the Update of the client of the daemon that was killed returned %v, want an error that says that "+
					"the daemon cannot be reached", err)
			}
			nothing(t, reopen(t, tb, dir))
		})
		t.Run("the client alone", func(t *testing.T) {
			dir := newDir(t, tb)
			before := logFiles(t, dir)
			_, sockets := serve(t, dir, tb.options())
			reader, err := openLogReader(dir, tb.options())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reader.Close() })
			h := startHelper(t, tb, dir, "commit", "commit", envSockets+"="+sockets)
			files := logFiles(t, dir)
			kill(t, h)
			if !maps.Equal(files, before) {
				t.Errorf("logdb/ had %v when the client was killed, want %v as before the Update", files, before)
			}
			// The daemon has let go of the writer, and has staged nothing: a new writer works at once, and commits only
			// what it puts.
			w := clientOf(t, sockets, dbwire.RoleWriter, 0)
			nothing(t, reader)
			update(t, w, func(tx *Tx) error { return putPair(tx, 1) })
			if n := checkPair(t, reader, 1, 1); n != 1 {
				t.Errorf("the pair of the new writer is %d", n)
			}
			checkIntegrity(t, reader)
			checkConsistent(t, reader)
			checkNoTemps(t, dir)
			if objects := logDB(t, reader).Stats(); objects.InlineObjects+objects.FileObjects != 2 {
				t.Errorf("the store has %d objects, want the 2 of the pair: the Update that was cut staged some",
					objects.InlineObjects+objects.FileObjects)
			}
		})
	})
}

// TestCrashWhileWriting kills a process at a random time while the pair is put in one Update after another, and checks
// that the store has the pair of one of them: every one that returned nil to its caller, and the one that was running,
// or not. The process is the daemon and the client together, the daemon alone (the client of the test puts the pair,
// and a reader client watches that no View shows a half done pair, or one that goes back, and that a new daemon has
// what the reader has seen), or the client alone (the daemon of the test is free for a new writer at once).
func TestCrashWhileWriting(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		t.Run("the daemon and the client", func(t *testing.T) {
			for range 3 {
				dir := newDir(t, tb)
				h := startHelper(t, tb, dir, "loop", "progress")
				time.Sleep(rand.N(30 * time.Millisecond))
				kill(t, h)
				t.Logf("the helper was killed after %d Updates", checkPair(t, reopen(t, tb, dir), 20, 1<<30))
			}
		})
		t.Run("the daemon alone", func(t *testing.T) {
			for range 3 {
				dir := newDir(t, tb)
				h, sockets := startDaemonHelper(t, tb, dir, "")
				w, r := clientOf(t, sockets, dbwire.RoleWriter, -1), clientOf(t, sockets, dbwire.RoleReader, -1)
				stopWatch := watchPair(t, r)
				var acked atomic.Int64 // the last Update that returned nil
				done := make(chan error, 1)
				go func() {
					var err error
					for n := 1; err == nil; n++ {
						err = w.Update(t.Context(), withRecord(func(tx *Tx) error { return putPair(tx, n) }))
						if err == nil {
							acked.Store(int64(n))
						}
					}
					done <- err
				}()
				eventually(t, func() bool { return acked.Load() >= 20 }, "20 Updates of the client")
				time.Sleep(rand.N(30 * time.Millisecond))
				kill(t, h)
				if err := await(t, done, "the Update of the client"); !errors.Is(err, dbclient.ErrUnavailable) {
					t.Errorf("the Update of the client of the daemon that was killed returned %v, want an error that says "+
						"that the daemon cannot be reached", err)
				}
				seen, n := stopWatch(), int(acked.Load())
				got := checkPair(t, reopen(t, tb, dir), max(n, seen), n+1)
				t.Logf("the daemon was killed after %d Updates that returned nil: the store has %d, a reader had seen %d", n,
					got, seen)
			}
		})
		t.Run("the client alone", func(t *testing.T) {
			for range 3 {
				dir := newDir(t, tb)
				_, sockets := serve(t, dir, tb.options())
				r, err := openLogReader(dir, tb.options())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = r.Close() })
				stopWatch := watchPair(t, r)
				h := startHelper(t, tb, dir, "loop", "progress", envSockets+"="+sockets)
				time.Sleep(rand.N(30 * time.Millisecond))
				kill(t, h)
				// A new writer is let in, and the pair is whole.
				w := clientOf(t, sockets, dbwire.RoleWriter, 0)
				n := checkPair(t, r, 20, 1<<30)
				if seen := stopWatch(); seen > n {
					t.Errorf("a reader had seen the pair of %d, and the store has %d", seen, n)
				}
				update(t, w, func(tx *Tx) error { return putPair(tx, n+1) })
				checkPair(t, r, n+1, n+1)
				checkIntegrity(t, r)
				checkConsistent(t, r)
				checkNoTemps(t, dir)
				t.Logf("the client was killed after %d Updates", n)
			}
		})
	})
}

// TestCrashOriginal kills a process in PutOriginal, and checks the files of the original. The process is the daemon and
// the client together, at the points of the hook after the sync of the temporary file and after the sync of the
// directory of the name; the daemon alone, at the same points (the client of the test gets an error that says that the
// daemon cannot be reached, and sends the original again after a new daemon has come); or the client alone, after the
// first bytes (the daemon of the test drops the temporary file when the connection ends).
func TestCrashOriginal(t *testing.T) {
	hash := sum(crashOriginal)
	digits := strings.TrimPrefix(hash, "sha256:")
	forBackends(t, oneBackend, func(t *testing.T, tb testBackend) {
		// checkTemp checks that files/ of dir has the temporary file of the original, whole, and no more.
		checkTemp := func(t *testing.T, dir string) {
			t.Helper()
			files := filepath.Join(dir, "files")
			got := names(t, files)
			if len(got) != 1 || !tempName.MatchString(got[0]) {
				t.Fatalf("files/ has %q, want a temporary file alone", got)
			}
			if data, err := os.ReadFile(filepath.Join(files, got[0])); err != nil || !bytes.Equal(data, crashOriginal) {
				t.Errorf("the temporary file has %d bytes, %v; want the %d bytes of the original", len(data), err,
					len(crashOriginal))
			}
		}
		// synced checks the store of a daemon that has opened the directory after the kill at original:synced, and renamed
		// after that at original:renamed.
		synced := func(t *testing.T, dir string, s *Store) {
			t.Helper()
			f, err := s.OpenOriginal(context.Background(), hash)
			if f != nil {
				_ = f.Close()
			}
			if !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("OpenOriginal() = %v, %v; want an error that wraps fs.ErrNotExist", f, err)
			}
			if got := names(t, filepath.Join(dir, "files")); got != nil {
				t.Errorf("files/ has %q after the daemon has opened the directory, want the temporary file removed", got)
			}
		}
		renamed := func(t *testing.T, dir string, s *Store) {
			t.Helper()
			if data, err := readOriginal(t, s, hash); err != nil || !bytes.Equal(data, crashOriginal) {
				t.Errorf("OpenOriginal() reads %d bytes, %v; want the %d bytes of the original", len(data), err,
					len(crashOriginal))
			}
			if got, want := names(t, filepath.Join(dir, "files")), []string{digits[:2]}; !slices.Equal(got, want) {
				t.Errorf("files/ has %q, want %q", got, want)
			}
		}
		t.Run("the daemon and the client", func(t *testing.T) {
			t.Run("original:synced", func(t *testing.T) {
				dir := newDir(t, tb)
				kill(t, startHelper(t, tb, dir, "original:synced", "original:synced"))
				// The whole temporary file stays after the crash, until the daemon opens the store.
				checkTemp(t, dir)
				synced(t, dir, reopen(t, tb, dir))
			})
			t.Run("original:renamed", func(t *testing.T) {
				dir := newDir(t, tb)
				kill(t, startHelper(t, tb, dir, "original:renamed", "original:renamed"))
				renamed(t, dir, reopen(t, tb, dir))
			})
		})
		t.Run("the daemon alone", func(t *testing.T) {
			for _, point := range []string{"original:synced", "original:renamed"} {
				t.Run(point, func(t *testing.T) {
					dir := newDir(t, tb)
					h, sockets := startDaemonHelper(t, tb, dir, point)
					s := clientOf(t, sockets, dbwire.RoleWriter, -1)
					done := make(chan error, 1)
					go func() {
						o, err := s.PutOriginal(t.Context(), bytes.NewReader(crashOriginal))
						if err == nil {
							err = fmt.Errorf("PutOriginal returned %+v and nil", o)
						}
						done <- err
					}()
					waitMarkers(t, h, 30*time.Second, func(m string) bool { return m == point })
					if point == "original:synced" {
						checkTemp(t, dir)
					}
					kill(t, h)
					if err := await(t, done, "PutOriginal of the client"); !errors.Is(err, dbclient.ErrUnavailable) {
						t.Errorf("PutOriginal of the client of the daemon that was killed returned %v, want an error that "+
							"says that the daemon cannot be reached", err)
					}
					r := reopen(t, tb, dir)
					if point == "original:synced" {
						synced(t, dir, r)
					} else {
						renamed(t, dir, r)
					}
					// The caller sends it again: the new daemon has it, or saves it.
					putOriginal(t, r, crashOriginal)
					if data, err := readOriginal(t, r, hash); err != nil || !bytes.Equal(data, crashOriginal) {
						t.Errorf("OpenOriginal() after it was put again reads %d bytes, %v", len(data), err)
					}
					checkNoTemps(t, dir)
				})
			}
		})
		t.Run("the client alone", func(t *testing.T) {
			dir := newDir(t, tb)
			_, sockets := serve(t, dir, tb.options())
			h := startHelper(t, tb, dir, "original:partial", "original:partial", envSockets+"="+sockets)
			files := filepath.Join(dir, "files")
			eventually(t, func() bool {
				for _, name := range names(t, files) {
					if info, err := os.Stat(filepath.Join(files, name)); err == nil && tempName.MatchString(name) &&
						info.Size() > 0 {
						return true
					}
				}
				return false
			}, "the daemon to have the first bytes of the original in a temporary file")
			kill(t, h)
			eventually(t, func() bool { return len(names(t, files)) == 0 }, "the daemon to drop the temporary file")
			r, err := openLogReader(dir, tb.options())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = r.Close() })
			if f, err := r.OpenOriginal(context.Background(), hash); !errors.Is(err, fs.ErrNotExist) {
				if f != nil {
					_ = f.Close()
				}
				t.Errorf("OpenOriginal() = %v, %v; want an error that wraps fs.ErrNotExist", f, err)
			}
			w := clientOf(t, sockets, dbwire.RoleWriter, 0)
			putOriginal(t, w, crashOriginal)
			if data, err := readOriginal(t, r, hash); err != nil || !bytes.Equal(data, crashOriginal) {
				t.Errorf("OpenOriginal() after it was put again reads %d bytes, %v", len(data), err)
			}
			checkNoTemps(t, dir)
		})
	})
}

// TestOtherWriterRefused checks that a second process cannot be the writer of a store while one is, and can when it has
// ended: a client that connects to the daemon while another client is connected as the writer fails (after the wait of
// the client) with the error that says so, whichever of the test and the helper has the first; and a second daemon on
// the data directory fails at the writer lock of the engine.
func TestOtherWriterRefused(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		t.Run("a client in the helper", func(t *testing.T) {
			s, dir := newStore(t, tb)
			putWithRevision(t, s)
			_, sockets := serve(t, dir, tb.options())
			// s, over net.Pipe, takes no lock of the writer: the test takes it for s, as a client over the sockets does.
			unlock, err := dbclient.SocketDir(sockets).LockWriter()
			if err != nil {
				t.Fatal(err)
			}
			want := sockets + ": another process is connected as the writer"
			h := startHelper(t, tb, dir, "open", "open: "+want, envSockets+"="+sockets, envWait+"=100ms")
			if err := h.wait(); err != nil {
				t.Errorf("the helper has ended with %v, want exit status 0", err)
			}
			data := docJSON(content.RefEntry, pageID)
			put(t, s, content.RefEntry, pageID, data, userID, Version{Seq: 2, Kind: content.RefEntry, ID: pageID,
				Hash: sum(data), SavedAt: testTime.UTC(), SavedBy: userID})
			// After the first writer has ended, another process is the writer.
			if err := errors.Join(s.Close(), unlock()); err != nil {
				t.Fatal(err)
			}
			h = startHelper(t, tb, dir, "open", "open: ok", envSockets+"="+sockets, envWait+"=2s")
			if err := h.wait(); err != nil {
				t.Errorf("the helper has ended with %v, want exit status 0", err)
			}
		})
		t.Run("a client in the test", func(t *testing.T) {
			dir := newDir(t, tb)
			_, sockets := serve(t, dir, tb.options())
			h := startHelper(t, tb, dir, "hold", "hold", envSockets+"="+sockets)
			// The helper is the writer of the daemon of the test.
			o := tb.options()
			o.wait = writerWait
			s, err := open(dir, o)
			if err == nil {
				_ = s.Close()
			}
			if want := dir + ": another process is connected as the writer"; err == nil || err.Error() != want {
				t.Errorf("Open() while the helper is the writer = %v, want %q", err, want)
			}
			kill(t, h)
			s = reopen(t, tb, dir)
			putWithRevision(t, s)
		})
		t.Run("a second daemon in the helper", func(t *testing.T) {
			s, dir := newStore(t, tb)
			putWithRevision(t, s)
			want := filepath.Join(dir, "logdb") + ": another process holds the writer lock"
			h := startHelper(t, tb, dir, "open", "open: "+want)
			if err := h.wait(); err != nil {
				t.Errorf("the helper has ended with %v, want exit status 0", err)
			}
			data := docJSON(content.RefEntry, pageID)
			put(t, s, content.RefEntry, pageID, data, userID, Version{Seq: 2, Kind: content.RefEntry, ID: pageID,
				Hash: sum(data), SavedAt: testTime.UTC(), SavedBy: userID})
		})
		t.Run("a second daemon in the test", func(t *testing.T) {
			dir := newDir(t, tb)
			h := startHelper(t, tb, dir, "hold", "hold")
			s, err := tb.open(dir)
			if err == nil {
				_ = s.Close()
			}
			if want := filepath.Join(dir, "logdb") + ": another process holds the writer lock"; err == nil ||
				err.Error() != want {
				t.Errorf("Open() while the helper has the store open = %v, want %q", err, want)
			}
			kill(t, h)
			s = reopen(t, tb, dir)
			putWithRevision(t, s)
		})
	})
}

// waitMarkers reads what h prints after marker until ok reports true of a mark, and returns the marks that it has read.
// It fails the test if h ends first, or if timeout passes.
func waitMarkers(t *testing.T, h *helper, timeout time.Duration, ok func(mark string) bool) []string {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	var marks []string
	for {
		select {
		case m, open := <-h.markers:
			if !open {
				t.Fatalf("the helper has ended after printing %q", marks)
			}
			if marks = append(marks, m); ok(m) {
				return marks
			}
		case <-deadline.C:
			t.Fatalf("the helper has not printed what the test waits for in %v, after %q", timeout, marks)
		}
	}
}

func isViolation(mark string) bool {
	return strings.HasPrefix(mark, "violation: ")
}

// TestReadersWhileWriting runs two readers in other processes (the helper in the mode reader), which check the pair in
// Views (checkPairView), while the pair is put 200 times in a store of the log backend, in segments of 4096 bytes with
// checkpoints every 8192 bytes: no reader sees a violation or a step back, and each sees a pair before the last one,
// and then the last one. The readers are clients of the sockets of the daemon, and the writer is the test, which has
// the daemon; or the test, which has a client, while the daemon is a process of its own.
func TestReadersWhileWriting(t *testing.T) {
	const updates = 200
	forBackends(t, allBackends[:2:2], func(t *testing.T, tb testBackend) {
		// run puts the pair updates times with w, and checks the readers that connect to sockets.
		run := func(t *testing.T, dir, sockets string, w *Store) {
			t.Helper()
			var readers []*helper
			for range 2 {
				readers = append(readers, startHelper(t, tb, dir, "reader", "ready", envSockets+"="+sockets))
			}
			for n := 1; n <= updates; n++ {
				update(t, w, func(tx *Tx) error { return putPair(tx, n) })
			}
			last := "seen " + strconv.Itoa(updates)
			for i, h := range readers {
				marks := waitMarkers(t, h, 10*time.Second, func(m string) bool { return m == last || isViolation(m) })
				kill(t, h)
				seen, views := 0, "no"
				for _, m := range marks {
					switch {
					case isViolation(m):
						t.Errorf("reader %d: %s", i, m)
					case strings.HasPrefix(m, "seen "):
						seen++
					case strings.HasPrefix(m, "views "):
						views = strings.TrimPrefix(m, "views ")
					}
				}
				if seen < 2 { // a pair before the last one, and the last one
					t.Errorf("reader %d has printed %q", i, marks)
				}
				t.Logf("reader %d has seen %d pairs, in %s views or more", i, seen, views)
			}
		}
		t.Run("the daemon in the test", func(t *testing.T) {
			o := *tb.log
			o.SegmentBytes, o.CheckpointBytes = 4096, 8192
			dir := filepath.Join(t.TempDir(), "data")
			s, err := create(dir, options{log: &o})
			if err != nil {
				t.Fatalf("create() = %v", err)
			}
			t.Cleanup(func() { _ = s.Close() })
			// The readers are clients of the daemon of the test, which listens on sockets for them.
			sockets := socketDir(t)
			if err := daemonOf(t, s).Listen(sockets); err != nil {
				t.Fatalf("Listen() = %v", err)
			}
			run(t, dir, sockets, s)
			if st := logDB(t, s).Stats(); st.Segments < 3 {
				t.Errorf("%d segments, want rollovers", st.Segments)
			}
		})
		t.Run("the daemon in a process of its own", func(t *testing.T) {
			dir := newDir(t, tb)
			_, sockets := startDaemonHelper(t, tb, dir, "")
			run(t, dir, sockets, clientOf(t, sockets, dbwire.RoleWriter, 0))
			if segments := len(logSegments(t, dir)); segments < 3 {
				t.Errorf("%d segments, want rollovers", segments)
			}
		})
	})
}

// logSegments returns the names of the segments of the log in the data directory dir.
func logSegments(t *testing.T, dir string) []string {
	t.Helper()
	var segments []string
	for name := range logFiles(t, dir) {
		if strings.HasPrefix(name, "logdb/log/") {
			segments = append(segments, name)
		}
	}
	return segments
}

// TestCrashAtEnginePoints kills a process at each point of enginePoints in an Update that puts the pair after the third
// one, on the log backend in each placement. The store then has the third pair if the process was killed before the
// engine wrote the record of the Update (logdb:update:objects: the objects of a placement with files are written before
// it), and else the fourth one, whose record the page cache has kept, and which Open makes durable; it passes
// checkIntegrity and checkConsistent. The process is the daemon and the client together; or the daemon alone, whose
// client has an Update that returns an error, and cannot tell whether it was made.
func TestCrashAtEnginePoints(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		for _, point := range enginePoints {
			want := 4
			if point == "logdb:update:objects" {
				want = 3
			}
			t.Run(point, func(t *testing.T) {
				t.Run("the daemon and the client", func(t *testing.T) {
					s, dir := newStore(t, tb)
					for n := 1; n <= 3; n++ {
						update(t, s, func(tx *Tx) error { return putPair(tx, n) })
					}
					if err := s.Close(); err != nil {
						t.Fatalf("Close() = %v", err)
					}
					kill(t, startHelper(t, tb, dir, point, point))
					view(t, reopen(t, tb, dir), func(tx *Tx) error {
						if n, err := checkPairView(tx); err != nil || n != want {
							t.Errorf("the pair after the kill = %d, %v; want %d", n, err, want)
						}
						return nil
					})
				})
				t.Run("the daemon alone", func(t *testing.T) {
					s, dir := newStore(t, tb)
					for n := 1; n <= 3; n++ {
						update(t, s, func(tx *Tx) error { return putPair(tx, n) })
					}
					if err := s.Close(); err != nil {
						t.Fatalf("Close() = %v", err)
					}
					h, sockets := startDaemonHelper(t, tb, dir, point)
					c := clientOf(t, sockets, dbwire.RoleWriter, -1)
					done := make(chan error, 1)
					go func() {
						done <- c.Update(t.Context(), withRecord(func(tx *Tx) error { return putPair(tx, 4) }))
					}()
					waitMarkers(t, h, 30*time.Second, func(m string) bool { return m == point })
					kill(t, h)
					err := await(t, done, "the Update of the client")
					r := reopen(t, tb, dir)
					view(t, r, func(tx *Tx) error {
						n, perr := checkPairView(tx)
						if perr != nil || n != want {
							t.Errorf("the pair after the kill = %d, %v; want %d", n, perr, want)
						}
						// The client was never told that the Update was made.
						if err == nil || !errors.Is(err, dbclient.ErrUnavailable) {
							t.Errorf("the Update of the client of the daemon that was killed returned %v, want an error that "+
								"says that the daemon cannot be reached (the store has the pair of %d)", err, n)
						}
						return nil
					})
				})
			})
		}
	})
}
