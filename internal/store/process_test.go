//go:build unix

package store

import (
	"bufio"
	"bytes"
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
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// The environment of the helper process: its mode, the data directory, and the name of its backend.
const (
	envMode    = "WHITEROOM_STORE_HELPER"
	envDir     = "WHITEROOM_STORE_DIR"
	envBackend = "WHITEROOM_STORE_BACKEND"
)

// marker starts the lines that the helper process prints to stdout to tell where it is.
const marker = "store-helper: "

// The formats of the IDs of the entries that TestOtherProcess puts from the test and from the helper, with a number.
const (
	testEntry   = "ent_01JE%022d"
	helperEntry = "ent_01JH%022d"
)

// crashOriginal is the original that the helper puts.
var crashOriginal = bytes.Repeat([]byte("original "), 100_000)

// The points of the engine of the log backend at which the helper process blocks in an Update, in the modes of the same
// names.
var enginePoints = []string{"logdb:update:objects", "logdb:update:written", "logdb:update:synced",
	"logdb:update:published"}

// TestHelperProcess is the process that the tests below run, with the environment of envMode, envDir and envBackend. In
// the modes commit, original:synced and original:renamed, it blocks at that point of the hook, where the test kills
// it, and so it does in the modes of enginePoints in an Update that puts the pair after the one in the store; in loop,
// it puts the pair until it is killed; in write, it puts 50 entries and ends; in open, it prints the result of opening
// the store and ends; in hold, it keeps the store open until it is killed; in reader, it opens the store with
// openLogReader and checks the pair in Views until it is killed (readerLoop). On its own, it does nothing.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv(envMode)
	if mode == "" {
		t.Skip("run by the tests of crashes and of other processes")
	}
	tb, ok := backendByName(os.Getenv(envBackend))
	if !ok {
		t.Fatalf("no backend %q", os.Getenv(envBackend))
	}
	if mode == "reader" {
		s, err := openLogReader(os.Getenv(envDir), tb.options())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		readerLoop(t, s)
		return
	}
	s, err := tb.open(os.Getenv(envDir))
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
		// More than the page cache of SQLite, which writes the rest to the WAL before the commit.
		big := strings.Repeat("x", 4<<20)
		err = s.Update(t.Context(), func(tx *Tx) error {
			data := []byte(`{"schemaVersion": 1, "id": "` + entryID + `", "type": "` + big + `"}`)
			if _, err := tx.Put(content.RefEntry, entryID, data, userID); err != nil {
				return err
			}
			if _, err := tx.Put(content.RefEntry, pageID, docJSON(content.RefEntry, pageID), userID); err != nil {
				return err
			}
			_, err := tx.AddRevision(entryID, revisionID, revJSON(big), userID)
			return err
		})
	case "loop":
		for n := 1; err == nil; n++ {
			err = s.Update(t.Context(), func(tx *Tx) error { return putPair(tx, n) })
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
		err = s.Update(t.Context(), func(tx *Tx) error { return putPair(tx, n[0]+1) })
	case "original:synced", "original:renamed":
		_, err = s.PutOriginal(t.Context(), bytes.NewReader(crashOriginal))
	case "write":
		fmt.Println(marker + "write")
		for i := range 50 {
			id := fmt.Sprintf(helperEntry, i)
			if err := s.Update(t.Context(), func(tx *Tx) error {
				_, err := tx.Put(content.RefEntry, id, docJSON(content.RefEntry, id), userID)
				return err
			}); err != nil {
				t.Fatalf("Update() of %s = %v", id, err)
			}
		}
		return
	case "hold":
		fmt.Println(marker + "hold")
		time.Sleep(time.Minute)
	}
	t.Fatalf("not killed in the mode %s: %v", mode, err)
}

// readerLoop checks the pair in Views of s, a store that openLogReader has opened, until the process is killed
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
func startHelper(t *testing.T, tb testBackend, dir, mode, want string) *helper {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	// With -race, a process sleeps for a second before it exits unless atexit_sleep_ms is 0.
	cmd.Env = append(os.Environ(), envMode+"="+mode, envDir+"="+dir, envBackend+"="+tb.name,
		"GORACE="+strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
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

// sizeOf returns the size of the file name, or -1 if it cannot.
func sizeOf(name string) int64 {
	info, err := os.Stat(name)
	if err != nil {
		return -1
	}
	return info.Size()
}

// TestCrashBeforeCommit kills the helper in an Update that has written more than the page cache of SQLite, before it
// commits, and checks that the store has nothing of it. The log backend has written nothing of the Update by then:
// its engine writes an Update when f has returned (TestLogHooks has its points after that).
func TestCrashBeforeCommit(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		dir := newDir(t, tb)
		var before map[string]int64
		if tb.log != nil {
			before = logFiles(t, dir)
		}
		h := startHelper(t, tb, dir, "commit", "commit")
		if tb.log == nil {
			wal := sizeOf(filepath.Join(dir, "whiteroom.db-wal"))
			kill(t, h)
			t.Logf("the WAL had %d bytes when the helper was killed", wal)
			if wal < 4<<20 {
				t.Errorf("the WAL had %d bytes when the helper was killed, want at least %d", wal, 4<<20)
			}
		} else {
			files := logFiles(t, dir)
			kill(t, h)
			if !maps.Equal(files, before) {
				t.Errorf("logdb/ had %v when the helper was killed, want %v as before the Update", files, before)
			}
		}
		s := reopen(t, tb, dir)
		checkEmpty(t, s)
		checkMissing(t, s, content.RefEntry, entryID, "entries/"+entryID+".json", nil)
		checkMissing(t, s, content.RefEntry, pageID, "entries/"+pageID+".json", nil)
		view(t, s, func(tx *Tx) error {
			if r, err := tx.Revision(revisionID); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("Revision() = %+v, %v; want an error that wraps fs.ErrNotExist", r, err)
			}
			return nil
		})
	})
}

// TestCrashWhileWriting kills the helper at a random time while it puts the pair in one Update after another, and
// checks that the store has the pair of one of them.
func TestCrashWhileWriting(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		for range 3 {
			dir := newDir(t, tb)
			h := startHelper(t, tb, dir, "loop", "progress")
			time.Sleep(rand.N(30 * time.Millisecond))
			kill(t, h)
			s := reopen(t, tb, dir)
			view(t, s, func(tx *Tx) error {
				numbers, err := readPair(tx)
				if err != nil || numbers[0] != numbers[1] || numbers[0] < 20 {
					t.Errorf("the names of the pair after the crash = %v, %v; want the same number, at least 20",
						numbers, err)
				}
				for _, id := range pair {
					if versions, err := tx.History(content.RefTerm, id); err != nil || len(versions) != numbers[0] {
						t.Errorf("History(%q) = %d versions, %v; want %d", id, len(versions), err, numbers[0])
					}
				}
				t.Logf("the helper was killed after %d Updates", numbers[0])
				return nil
			})
		}
	})
}

// TestCrashOriginal kills the helper in PutOriginal at the points of the hook, and checks the files of the original.
func TestCrashOriginal(t *testing.T) {
	hash := sum(crashOriginal)
	digits := strings.TrimPrefix(hash, "sha256:")
	forBackends(t, twoBackends, func(t *testing.T, tb testBackend) {
		t.Run("original:synced", func(t *testing.T) {
			dir := newDir(t, tb)
			kill(t, startHelper(t, tb, dir, "original:synced", "original:synced"))
			s := reopen(t, tb, dir)
			f, err := s.OpenOriginal(hash)
			if f != nil {
				_ = f.Close()
			}
			if !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("OpenOriginal() = %v, %v; want an error that wraps fs.ErrNotExist", f, err)
			}
			// The whole temporary file stays.
			files := filepath.Join(dir, "files")
			got := names(t, files)
			if len(got) != 1 || len(got[0]) != len(".tmp-")+16 || !strings.HasPrefix(got[0], ".tmp-") {
				t.Fatalf("files/ has %q, want a temporary file alone", got)
			}
			if data, err := os.ReadFile(filepath.Join(files, got[0])); err != nil || !bytes.Equal(data, crashOriginal) {
				t.Errorf("the temporary file has %d bytes, %v; want the %d bytes of the original", len(data), err,
					len(crashOriginal))
			}
		})
		t.Run("original:renamed", func(t *testing.T) {
			dir := newDir(t, tb)
			kill(t, startHelper(t, tb, dir, "original:renamed", "original:renamed"))
			s := reopen(t, tb, dir)
			if data, err := readOriginal(t, s, hash); err != nil || !bytes.Equal(data, crashOriginal) {
				t.Errorf("OpenOriginal() reads %d bytes, %v; want the %d bytes of the original", len(data), err,
					len(crashOriginal))
			}
			if got, want := names(t, filepath.Join(dir, "files")), []string{digits[:2]}; !slices.Equal(got, want) {
				t.Errorf("files/ has %q, want %q", got, want)
			}
		})
	})
}

// TestOtherProcess puts entries from the test and from the helper, in another process, to the same data directory at
// the same time.
func TestOtherProcess(t *testing.T) {
	forBackends(t, sqliteOnly, func(t *testing.T, tb testBackend) {
		s, dir := newStore(t, tb)
		h := startHelper(t, tb, dir, "write", "write")
		var want []string
		for i := range 50 {
			id := fmt.Sprintf(testEntry, i)
			update(t, s, func(tx *Tx) error {
				_, err := tx.Put(content.RefEntry, id, docJSON(content.RefEntry, id), userID)
				return err
			})
			want = append(want, id, fmt.Sprintf(helperEntry, i))
		}
		if err := h.wait(); err != nil {
			t.Fatalf("the helper has ended with %v, want exit status 0", err)
		}
		slices.Sort(want)
		view(t, s, func(tx *Tx) error {
			docs, err := tx.Documents(content.RefEntry)
			var ids []string
			for _, d := range docs {
				ids = append(ids, d.ID)
				if want := docJSON(content.RefEntry, d.ID); !bytes.Equal(d.Data, want) {
					t.Errorf("Documents() has %s with %q, want %q", d.ID, d.Data, want)
				}
			}
			if err != nil || !slices.Equal(ids, want) {
				t.Errorf("IDs of Documents() = %q, %v; want %q", ids, err, want)
			}
			return nil
		})
		checkConsistent(t, s)
		checkIntegrity(t, s)
	})
}

// TestOtherWriterRefused checks that a store of the log backend that a process has opened cannot be opened by another
// one, which waits only briefly for the lock of the writer, whichever of the test and the helper has opened it first.
// With SQLite, both open it and take their turns (TestOtherProcess).
func TestOtherWriterRefused(t *testing.T) {
	forBackends(t, logBackends, func(t *testing.T, tb testBackend) {
		t.Run("in the helper", func(t *testing.T) {
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
		t.Run("in the test", func(t *testing.T) {
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
// Views (checkPairView), while the test puts the pair 200 times in a store of the log backend, in segments of 4096
// bytes with checkpoints every 8192 bytes: no reader sees a violation or a step back, and each sees a pair before the
// last one, and then the last one.
func TestReadersWhileWriting(t *testing.T) {
	const updates = 200
	forBackends(t, logBackends[:2:2], func(t *testing.T, tb testBackend) {
		o := *tb.log
		o.SegmentBytes, o.CheckpointBytes = 4096, 8192
		dir := filepath.Join(t.TempDir(), "data")
		s, err := create(dir, options{log: &o})
		if err != nil {
			t.Fatalf("create() = %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		var readers []*helper
		for range 2 {
			readers = append(readers, startHelper(t, tb, dir, "reader", "ready"))
		}
		for n := 1; n <= updates; n++ {
			update(t, s, func(tx *Tx) error { return putPair(tx, n) })
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
		if st := logDB(t, s).Stats(); st.Segments < 3 {
			t.Errorf("%d segments, want rollovers", st.Segments)
		}
	})
}

// TestCrashAtEnginePoints kills the helper at each point of enginePoints in an Update that puts the pair after the
// third one, on the log backend in each placement. The store then has the third pair if the helper was killed before
// the engine wrote the record of the Update (logdb:update:objects: the objects of a placement with files are written
// before it), and else the fourth one, whose record the page cache has kept, and which Open makes durable; it passes
// checkIntegrity and checkConsistent.
func TestCrashAtEnginePoints(t *testing.T) {
	forBackends(t, logBackends, func(t *testing.T, tb testBackend) {
		for _, point := range enginePoints {
			t.Run(point, func(t *testing.T) {
				s, dir := newStore(t, tb)
				for n := 1; n <= 3; n++ {
					update(t, s, func(tx *Tx) error { return putPair(tx, n) })
				}
				if err := s.Close(); err != nil {
					t.Fatalf("Close() = %v", err)
				}
				kill(t, startHelper(t, tb, dir, point, point))
				want := 4
				if point == "logdb:update:objects" {
					want = 3
				}
				view(t, reopen(t, tb, dir), func(tx *Tx) error {
					if n, err := checkPairView(tx); err != nil || n != want {
						t.Errorf("the pair after the kill = %d, %v; want %d", n, err, want)
					}
					return nil
				})
			})
		}
	})
}
