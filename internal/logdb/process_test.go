//go:build unix

package logdb

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// The environment of the helper process: its mode, the directory of the store, and its options as JSON.
const (
	envLogMode = "WHITEROOM_LOGDB_HELPER"
	envLogDir  = "WHITEROOM_LOGDB_DIR"
	envLogOpts = "WHITEROOM_LOGDB_OPTS"
)

// logMarker starts the lines that the helper process prints to stdout to tell where it is.
const logMarker = "logdb-helper: "

// helperOpts are the options of the helper process.
type helperOpts struct {
	Placement       Placement
	Threshold       int
	SegmentBytes    int64
	CheckpointBytes int64
	SyncCheckpoints bool
	BaseBytes       int    // of the compaction of the mode point
	Point           string // the hook point at which the mode point blocks
}

func (h helperOpts) options() Options {
	return Options{Placement: h.Placement, Threshold: h.Threshold, SegmentBytes: h.SegmentBytes,
		CheckpointBytes: h.CheckpointBytes, SyncCheckpoints: h.SyncCheckpoints}
}

// readPair returns n of the pair in tx, which must have the same value in both tables (0 if neither is there), and the
// object of n with its data.
func readPair(tx *Tx) (int, error) {
	a, okA := tx.Get(1, []byte("a"))
	b, okB := tx.Get(2, []byte("b"))
	switch {
	case okA != okB || !bytes.Equal(a, b):
		return 0, fmt.Errorf("the pair differs: a=%q (%v), b=%q (%v)", a, okA, b, okB)
	case !okA:
		return 0, nil
	}
	n, err := strconv.Atoi(string(a))
	if err != nil {
		return 0, fmt.Errorf("the pair has %q", a)
	}
	if err := checkPairObject(tx, n); err != nil {
		return 0, err
	}
	return n, nil
}

// checkPairObject checks that tx has the object of the commit n of pairF, with its data.
func checkPairObject(tx *Tx, n int) error {
	want := pairObject(n)
	data, err := tx.Object(sha256.Sum256(want))
	switch {
	case err != nil:
		return fmt.Errorf("the object of %d: %w", n, err)
	case !bytes.Equal(data, want):
		return fmt.Errorf("the object of %d has other data: %d bytes", n, len(data))
	}
	return nil
}

// pairRewrite is the rewrite of the compactions of the tests of processes: it drops table 3, which has a secret.
var pairRewrite = Rewrite{Value: func(table uint32, _, value []byte) ([]byte, bool) { return value, table != 3 }}

// TestLogHelperProcess is the process that the tests below run, with the environment of envLogMode, envLogDir and
// envLogOpts. In the mode loop, it makes the Updates of pairF after the last one in the store, until it is
// killed; in point, it opens the store and blocks at the hook point of its options in a checkpoint (an Update and
// Checkpoint, or Updates until an automatic one begins) or a compaction (Maintain and Compact with pairRewrite); in
// reader, it opens a reader and checks the pair in Views until it is killed, printing each new n that it sees; in
// holdreader, it keeps a reader open until it is killed. On its own, it does nothing.
func TestLogHelperProcess(t *testing.T) {
	mode := os.Getenv(envLogMode)
	if mode == "" {
		t.Skip("run by the tests of other processes")
	}
	var h helperOpts
	if err := json.Unmarshal([]byte(os.Getenv(envLogOpts)), &h); err != nil {
		t.Fatal(err)
	}
	if h.BaseBytes > 0 {
		baseBytes = h.BaseBytes
	}
	fsys := osFS(t, os.Getenv(envLogDir))
	ctx := context.Background()
	o := h.options()
	block := func(point string) error {
		if point != h.Point {
			return nil
		}
		fmt.Println(logMarker + point)
		time.Sleep(time.Minute)
		return errors.New("not killed")
	}
	// last returns n of the pair in db.
	last := func(db *DB) int {
		var n int
		if err := db.View(ctx, func(tx *Tx) error {
			var err error
			n, err = readPair(tx)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	var err error
	switch mode {
	case "loop":
		db := reopen(t, fsys, o)
		n := last(db)
		for i := 1; err == nil; i++ {
			err = db.Update(ctx, pairF(n+i))
			if i == 20 && err == nil {
				fmt.Println(logMarker + "progress")
			}
		}
	case "point":
		o.Hook = block
		if strings.HasPrefix(h.Point, "compact:") {
			err = Maintain(ctx, fsys, o, func(m *Maintenance) error { return m.Compact(ctx, pairRewrite) })
			break
		}
		db := reopen(t, fsys, o)
		n := last(db)
		if h.Point == "checkpoint:begin" {
			for i := 1; err == nil; i++ {
				err = db.Update(ctx, pairF(n+i)) // until an automatic checkpoint begins
			}
			break
		}
		if err = db.Update(ctx, pairF(n+1)); err == nil {
			err = db.Checkpoint()
		}
	case "reader":
		rd, err := OpenReader(fsys, o)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rd.Close() }()
		fmt.Println(logMarker + "ready")
		seen := 0
		for {
			err := rd.View(ctx, func(tx *Tx) error {
				n, err := readPair(tx)
				switch {
				case err != nil:
					return err
				case n < seen:
					return fmt.Errorf("the pair of %d after that of %d", n, seen)
				case n > seen:
					fmt.Println(logMarker + "seen " + strconv.Itoa(n))
				}
				seen = n
				return nil
			})
			if err != nil {
				fmt.Println(logMarker + "violation: " + err.Error())
				time.Sleep(time.Minute)
				t.Fatal("not killed")
			}
			time.Sleep(time.Millisecond)
		}
	case "holdreader":
		rd, err := OpenReader(fsys, o)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rd.Close() }()
		fmt.Println(logMarker + "hold")
		time.Sleep(time.Minute)
	}
	t.Fatalf("not killed in the mode %s: %v", mode, err)
}

// logHelper is a process that runs TestLogHelperProcess.
type logHelper struct {
	cmd     *exec.Cmd
	mode    string
	mu      sync.Mutex
	marks   []string      // what it has printed after logMarker, guarded by mu
	stdout  []string      // its other lines of stdout, guarded by mu
	changed chan struct{} // gets a value when marks grows
	ended   chan struct{} // closed at the end of its stdout
	stderr  bytes.Buffer  // complete when wait has returned
	wait    func() error  // waits for the end of its stdout and of the process, once
}

// startLogHelper runs TestLogHelperProcess in mode on the store in dir with o, and returns once it has printed want
// after logMarker. A helper that runs for 30 seconds is killed; the end of the test kills it, and logs its output if
// the test has failed.
func startLogHelper(t *testing.T, dir, mode string, o helperOpts, want string) *logHelper {
	t.Helper()
	opts, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	h := &logHelper{cmd: exec.Command(os.Args[0], "-test.run=^TestLogHelperProcess$"), mode: mode,
		changed: make(chan struct{}, 1), ended: make(chan struct{})}
	// With -race, a process sleeps for a second before it exits unless atexit_sleep_ms is 0.
	h.cmd.Env = append(os.Environ(), envLogMode+"="+mode, envLogDir+"="+dir, envLogOpts+"="+string(opts),
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
			h.mu.Lock()
			if m, ok := strings.CutPrefix(sc.Text(), logMarker); ok {
				h.marks = append(h.marks, m)
				select {
				case h.changed <- struct{}{}:
				default:
				}
			} else {
				h.stdout = append(h.stdout, sc.Text())
			}
			h.mu.Unlock()
		}
	}()
	timer := time.AfterFunc(30*time.Second, func() { _ = h.cmd.Process.Kill() })
	h.wait = sync.OnceValue(func() error {
		<-h.ended
		err := h.cmd.Wait()
		timer.Stop()
		return err
	})
	t.Cleanup(func() {
		_ = h.cmd.Process.Kill()
		err := h.wait()
		if t.Failed() {
			h.mu.Lock()
			defer h.mu.Unlock()
			t.Logf("the helper in the mode %s: %v\nmarks: %q\nstdout: %q\nstderr:\n%s", mode, err, h.marks, h.stdout,
				h.stderr.Bytes())
		}
	})
	h.waitFor(t, 10*time.Second, func(marks []string) bool { return slices.Contains(marks, want) })
	return h
}

// waitFor waits until ok reports true of what the helper has printed, and fails the test if it does not within
// timeout, or if the helper ends first.
func (h *logHelper) waitFor(t *testing.T, timeout time.Duration, ok func(marks []string) bool) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		h.mu.Lock()
		marks := slices.Clone(h.marks)
		h.mu.Unlock()
		if ok(marks) {
			return
		}
		select {
		case <-h.changed:
		case <-h.ended:
			h.mu.Lock()
			marks := slices.Clone(h.marks)
			h.mu.Unlock()
			if !ok(marks) {
				t.Fatalf("the helper in the mode %s has ended, after printing %q", h.mode, marks)
			}
			return
		case <-deadline.C:
			t.Fatalf("the helper in the mode %s has not printed what the test waits for in %v, after %q", h.mode, timeout,
				marks)
		}
	}
}

// kill kills the helper with SIGKILL, and waits for it: it must have died of the signal.
func (h *logHelper) kill(t *testing.T) {
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

// newPairStore makes a store in a new directory with o, makes the Updates 1 to n of pairF in it, and closes it.
func newPairStore(t *testing.T, o Options, n int) string {
	t.Helper()
	dir := t.TempDir()
	fsys := osFS(t, dir)
	db, err := Create(fsys, o)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		if err := db.Update(context.Background(), pairF(i)); err != nil {
			t.Fatal(err)
		}
	}
	closeDB(t, db)
	return dir
}

// checkPairStore opens the store in dir with o, which a killed process has left, and checks it: it opens, Verify
// passes, and it has the pair of n, with the events 1 to n and their objects. It returns n and what Open found.
func checkPairStore(t *testing.T, dir string, o Options) (int, RecoveryInfo) {
	t.Helper()
	ctx := context.Background()
	db := reopen(t, osFS(t, dir), o)
	defer closeDB(t, db)
	info := db.Recovery()
	if strings.HasPrefix(info.Checkpoint, "damaged") {
		t.Errorf("the checkpoint is %s", info.Checkpoint)
	}
	if err := db.Verify(ctx); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	var n int
	if err := db.View(ctx, func(tx *Tx) error {
		var err error
		if n, err = readPair(tx); err != nil {
			return err
		}
		for i := 1; i < n; i++ {
			if err := checkPairObject(tx, i); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	number := uint64(0)
	if err := db.Events(ctx, func(e Event) bool {
		number++
		if e.Number != number || string(e.Data) != strconv.FormatUint(number, 10) {
			t.Errorf("event %d/%d=%q, want number %d", e.Number, e.Seq, e.Data, number)
		}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if number != uint64(n) {
		t.Errorf("%d events, want %d", number, n)
	}
	return n, info
}

// TestKillWriter kills a writer that makes Updates, at a random time after its first 20, three times (long: 20):
// each time the store opens, passes Verify and has the pair of one Update, with its events and objects; the next
// writer goes on.
func TestKillWriter(t *testing.T) {
	ho := helperOpts{Placement: Hybrid, Threshold: 100, SegmentBytes: 4096, CheckpointBytes: 8192}
	dir := newPairStore(t, ho.options(), 0)
	kills := 3
	if longTests() {
		kills = 20
	}
	last := 0
	for round := range kills {
		h := startLogHelper(t, dir, "loop", ho, "progress")
		time.Sleep(rand.N(30 * time.Millisecond))
		h.kill(t)
		n, info := checkPairStore(t, dir, ho.options())
		if n < last+20 {
			t.Errorf("kill %d: the store has the pair of %d, want %d or more", round+1, n, last+20)
		}
		t.Logf("kill %d: after the Update %d; recovery %+v", round+1, n, info)
		last = n
	}
}

// TestKillDuringCheckpoint kills a writer at each hook point of a checkpoint: the store has every Update, uses the
// old checkpoint or the new one, never a damaged one, and a GC removes the temporary file that the kill has left.
func TestKillDuringCheckpoint(t *testing.T) {
	for _, c := range []struct {
		point    string
		replayed int // after the checkpoint that Open uses; -1: the records of the Updates after the first
		temps    int // that a GC removes
	}{
		{"checkpoint:begin", -1, 0},
		{"checkpoint:synced", 1, 1},
		{"checkpoint:renamed", 0, 0},
		{"checkpoint:done", 0, 0},
	} {
		t.Run(c.point, func(t *testing.T) {
			ho := helperOpts{Placement: Hybrid, Threshold: 100, SegmentBytes: 2048, CheckpointBytes: -1, Point: c.point}
			if c.point == "checkpoint:begin" {
				ho.CheckpointBytes, ho.SyncCheckpoints = 1500, true
			}
			dir := newPairStore(t, ho.options(), 30)
			startLogHelper(t, dir, "point", ho, c.point).kill(t)
			n, info := checkPairStore(t, dir, Options{})
			if want := n - 30; c.replayed < 0 && info.Replayed != want || c.replayed >= 0 && (n != 31 || info.Replayed != c.replayed) {
				t.Errorf("the pair of %d, recovery %+v", n, info)
			}
			if info.Checkpoint != "used" {
				t.Errorf("the checkpoint is %q, want used", info.Checkpoint)
			}
			if res := gcWithin(t, osFS(t, dir)); res != (GCResult{Temps: c.temps}) {
				t.Errorf("GC: %+v, want %d temporary files", res, c.temps)
			}
		})
	}
}

// TestKillDuringCompaction kills a compaction at each of its hook points: the store has the old generation with the
// old state, before the base-end, or the new one with the rewritten state, from it on, never a mix; a GC removes the
// other generation.
func TestKillDuringCompaction(t *testing.T) {
	for _, c := range []struct {
		point      string
		gen        uint64
		checkpoint string
	}{
		{"compact:base", 1, "used"},
		{"compact:base-end", 2, "stale"},
		{"compact:checkpoint", 2, "used"},
		{"compact:delete", 2, "used"},
	} {
		t.Run(c.point, func(t *testing.T) {
			ho := helperOpts{Placement: Hybrid, Threshold: 100, SegmentBytes: 1024, CheckpointBytes: -1, BaseBytes: 512,
				Point: c.point}
			dir := newPairStore(t, ho.options(), 30)
			fsys := osFS(t, dir)
			db := reopen(t, fsys, ho.options())
			putKey(t, db, 3, "secret", "s")
			closeDB(t, db)
			startLogHelper(t, dir, "point", ho, c.point).kill(t)
			n, info := checkPairStore(t, dir, Options{})
			db = reopen(t, fsys, Options{})
			_, secret := getKey(t, db, 3, "secret")
			closeDB(t, db)
			switch {
			case n != 30 || info.Generation != c.gen || info.Checkpoint != c.checkpoint:
				t.Errorf("the pair of %d, recovery %+v", n, info)
			case secret != (c.gen == 1):
				t.Errorf("generation %d has the secret: %v", info.Generation, secret)
			case c.gen == 1 && !slices.Equal(info.IgnoredGenerations, []uint64{2}):
				t.Errorf("ignored generations %v, want [2]", info.IgnoredGenerations)
			}
			if res := gcWithin(t, fsys); res.Segments == 0 || res.Temps != 0 {
				t.Errorf("GC: %+v, want the segments of the other generation", res)
			}
			if gens := gensOf(t, fsys); !slices.Equal(gens, []uint64{c.gen}) {
				t.Errorf("after the GC, generations %v, want %d", gens, c.gen)
			}
		})
	}
}

// gcWithin runs a GC in maintenance on the store in fsys, which must begin within 10 seconds: the killed process has
// released its locks.
func gcWithin(t *testing.T, fsys vfs.FS) GCResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var res GCResult
	if err := Maintain(ctx, fsys, Options{}, func(m *Maintenance) error {
		var err error
		res, err = m.CollectGarbage(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return res
}

// getKey returns the value of key in table, read in a View.
func getKey(t *testing.T, db *DB, table uint32, key string) ([]byte, bool) {
	t.Helper()
	var v []byte
	var ok bool
	if err := db.View(context.Background(), func(tx *Tx) error {
		v, ok = tx.Get(table, []byte(key))
		v = slices.Clone(v)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return v, ok
}

// TestReadersWhileWriting runs three readers in other processes, which check the pair in Views, while the test makes
// 300 Updates in segments of 4096 bytes with automatic checkpoints: no reader sees a pair that differs, or goes back,
// and each sees a commit before the last one, and then the last one.
func TestReadersWhileWriting(t *testing.T) {
	const updates = 300
	ho := helperOpts{Placement: Hybrid, Threshold: 100, SegmentBytes: 4096, CheckpointBytes: 8192}
	dir := newPairStore(t, ho.options(), 0)
	var readers []*logHelper
	for range 3 {
		readers = append(readers, startLogHelper(t, dir, "reader", ho, "ready"))
	}
	db := reopen(t, osFS(t, dir), ho.options())
	for n := 1; n <= updates; n++ {
		if err := db.Update(context.Background(), pairF(n)); err != nil {
			t.Fatal(err)
		}
	}
	last := "seen " + strconv.Itoa(updates)
	for i, h := range readers {
		h.waitFor(t, 10*time.Second, func(marks []string) bool {
			return slices.Contains(marks, last) || slices.ContainsFunc(marks, isViolation)
		})
		h.kill(t)
		h.mu.Lock()
		marks := slices.Clone(h.marks)
		h.mu.Unlock()
		if v := slices.IndexFunc(marks, isViolation); v >= 0 {
			t.Errorf("reader %d: %s", i, marks[v])
		}
		if len(marks) < 3 { // ready, a commit before the last one, and the last one
			t.Errorf("reader %d has seen %q", i, marks)
		}
		t.Logf("reader %d has seen %d commits", i, len(marks)-1)
	}
	closeDB(t, db)
	if stats := db.Stats(); stats.Segments < 3 {
		t.Errorf("%d segments, want rollovers", stats.Segments)
	}
}

func isViolation(mark string) bool {
	return strings.HasPrefix(mark, "violation: ")
}

// TestKilledReaderReleasesMaintenance kills a reader in another process while Maintain waits for it: Maintain then
// goes on within 2 seconds.
func TestKilledReaderReleasesMaintenance(t *testing.T) {
	dir := newPairStore(t, Options{}, 1)
	h := startLogHelper(t, dir, "holdreader", helperOpts{}, "hold")
	fsys := osFS(t, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Maintain(ctx, fsys, Options{}, func(m *Maintenance) error {
			return m.View(ctx, func(tx *Tx) error {
				_, err := readPair(tx)
				return err
			})
		})
	}()
	waitLocked(t, fsys, "lock/entry")
	select {
	case err := <-done:
		t.Fatalf("Maintain has returned %v while the reader has the store open", err)
	case <-time.After(200 * time.Millisecond):
	}
	h.kill(t)
	start := time.Now()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Maintain: %v", err)
		}
		t.Logf("Maintain has gone on %v after the kill", time.Since(start))
	case <-time.After(2 * time.Second):
		t.Fatal("Maintain has not gone on 2 seconds after the kill")
	}
}
