package logdb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

func openReader(t *testing.T, fsys vfs.FS, o Options) *DB {
	t.Helper()
	rd, err := OpenReader(fsys, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rd.Close() })
	return rd
}

// lockFile locks name in mode, as another process does, until the test ends or the returned function is called.
func lockFile(t *testing.T, fsys vfs.FS, name string, mode vfs.LockMode) func() {
	t.Helper()
	unlock, err := fsys.Lock(name, mode)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			if err := unlock(); err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(release)
	return release
}

// appendRaw writes b at the end of the file name and does not sync it, as a writer that was killed leaves a write.
func appendRaw(t *testing.T, fsys vfs.FS, name string, b []byte) {
	t.Helper()
	f, err := fsys.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(b, fileSize(t, fsys, name)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// headFile returns the content of head with s in both slots, the later with the larger counter.
func headFile(s headSlot) []byte {
	s0, s1 := s, s
	s0.counter, s1.counter = 100, 101
	return appendHeadSlot(appendHeadSlot(nil, s0), s1)
}

func checkDump(t *testing.T, db *DB, want string) {
	t.Helper()
	if got := dump(t, db); got != want {
		t.Errorf("dump:\n%s\nwant:\n%s", got, want)
	}
}

// TestReaderSeesOnlyDurable holds an Update of the writer at each point before head names its commit: neither a
// reader that is open nor one that opens then sees it; after the Update has returned, both do.
func TestReaderSeesOnlyDurable(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		point      string
		checkpoint bool  // of the first commit: a reader that opens starts from it
		segment    int64 // SegmentBytes: 100 makes the commit that is held go in a new segment
	}{
		{"update:written", false, 0}, {"update:synced", false, 0}, {"update:published", false, 0},
		{"update:written", true, 0}, {"update:published", true, 0}, {"update:written", false, 100},
	} {
		point := c.point
		t.Run(fmt.Sprintf("%s, checkpoint %v, segment %d", point, c.checkpoint, c.segment), func(t *testing.T) {
			dir := t.TempDir()
			fsys := osFS(t, dir)
			var armed atomic.Bool
			blocked, hold := make(chan struct{}), make(chan struct{})
			var once sync.Once
			release := func() { once.Do(func() { close(hold) }) }
			db, err := Create(fsys, Options{SegmentBytes: c.segment, Hook: func(p string) error {
				if p == point && armed.CompareAndSwap(true, false) {
					close(blocked)
					<-hold
				}
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			t.Cleanup(release) // before Close, which waits for the Update
			putKey(t, db, 1, "a", "1")
			if c.checkpoint {
				if err := db.Checkpoint(); err != nil {
					t.Fatal(err)
				}
			}
			putKey(t, db, 1, "b", "2")
			rd := openReader(t, osFS(t, dir), Options{})
			armed.Store(true)
			done := make(chan error, 1)
			go func() { done <- db.Update(ctx, putF(1, "c", "3")) }()
			wait(t, blocked, "block")
			if c.segment > 0 && len(logNames(t, fsys)) != 3 {
				t.Fatalf("segments %v", logNames(t, fsys))
			}
			checkDump(t, rd, "1/a=1\n1/b=2")
			late := openReader(t, fsys, Options{})
			checkDump(t, late, "1/a=1\n1/b=2")
			if info := late.Recovery(); info.Seq != 2 || c.checkpoint != (info.Checkpoint == "used") {
				t.Errorf("a reader that opened during the Update: %+v", info)
			}
			release()
			if err := wait(t, done, "Update"); err != nil {
				t.Fatal(err)
			}
			checkDump(t, rd, "1/a=1\n1/b=2\n1/c=3")
			checkDump(t, late, "1/a=1\n1/b=2\n1/c=3")
		})
	}
}

func TestReaderFollowsRollover(t *testing.T) {
	fsys, db := newSim(t, Options{Placement: Hybrid, Threshold: 100, SegmentBytes: 300})
	rd := openReader(t, fsys, Options{})
	for i := range 30 {
		change(t, db, i)
		if i%7 == 6 {
			if got, want := stateOf(t, rd), stateOf(t, db); got != want {
				t.Fatalf("after %d commits the reader has:\n%s\nwant:\n%s", i+1, got, want)
			}
		}
	}
	if got, want := stateOf(t, rd), stateOf(t, db); got != want {
		t.Fatalf("the reader has:\n%s\nwant:\n%s", got, want)
	}
	if rs, ws := rd.Stats(), db.Stats(); rs.Segments != ws.Segments || rs.LogBytes != ws.LogBytes || ws.Segments < 5 {
		t.Errorf("reader stats %+v, writer stats %+v", rs, ws)
	}
	// Without the writer, the reader reads to the end of the log.
	closeDB(t, db)
	if got, want := stateOf(t, rd), stateOf(t, openReader(t, fsys, Options{})); got != want {
		t.Errorf("after Close the reader has:\n%s\nwant:\n%s", got, want)
	}
}

func TestReaderAfterCheckpointRename(t *testing.T) {
	fsys, db := newSim(t, Options{CheckpointBytes: -1})
	for i := range 3 {
		change(t, db, i)
	}
	if err := db.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	rd := openReader(t, fsys, Options{})
	if info := rd.Recovery(); info.Checkpoint != "used" || info.Seq != 3 || info.Replayed != 0 {
		t.Errorf("a reader while the writer runs: %+v", info)
	}
	if rs, ws := rd.Stats(), db.Stats(); rs.LogBytes != ws.LogBytes || rs.Segments != ws.Segments || rs.Events != ws.Events {
		t.Errorf("reader stats %+v, writer stats %+v", rs, ws)
	}
	for i := 3; i < 10; i++ {
		change(t, db, i)
		if i%2 == 0 {
			if err := db.Checkpoint(); err != nil {
				t.Fatal(err)
			}
		}
		if got, want := stateOf(t, rd), stateOf(t, db); got != want {
			t.Fatalf("after %d commits the reader has:\n%s\nwant:\n%s", i+1, got, want)
		}
	}
	late := openReader(t, fsys, Options{})
	if info := late.Recovery(); info.Checkpoint != "used" || info.Seq != 10 || info.Replayed != 1 {
		t.Errorf("a reader that opened later: %+v", info)
	}
	want := stateOf(t, db)
	closeDB(t, db)
	for _, r := range []*DB{rd, late, openReader(t, fsys, Options{})} {
		if got := stateOf(t, r); got != want {
			t.Errorf("after Close a reader has:\n%s", got)
		}
	}
}

// TestReaderWithoutWriter shows a reader of a store whose writer was killed: it reads the record that the writer left
// in the page cache, and syncs it before a View sees it; it stops at a torn tail, which it does not cut; it changes
// no file.
func TestReaderWithoutWriter(t *testing.T) {
	fsys, db := newSim(t, Options{})
	putKey(t, db, 1, "a", "1")
	putKey(t, db, 1, "b", "2")
	closeDB(t, db)
	h, _ := frames(t, fsys, seg1)
	appendRaw(t, fsys, seg1, commitFrame(h.salt, 3, 1, "c", "3"))
	if len(pendingOn(fsys, seg1)) == 0 {
		t.Fatal("the record is durable")
	}
	before := fileMap(fsys)
	rd := openReader(t, fsys, Options{})
	if info := rd.Recovery(); info.Seq != 3 || info.Checkpoint != "used" || info.TruncatedAt != -1 {
		t.Errorf("Recovery: %+v", info)
	}
	if p := pendingOn(fsys, seg1); len(p) != 0 {
		t.Errorf("the reader did not sync what it read: %v", p)
	}
	checkFilesUnchanged(t, fsys, before)
	checkDump(t, rd, "1/a=1\n1/b=2\n1/c=3")

	// A torn tail: the reader stops before it, and leaves it for the writer to cut.
	torn := commitFrame(h.salt, 4, 1, "d", "4")
	appendRaw(t, fsys, seg1, torn[:13])
	before = fileMap(fsys)
	checkDump(t, rd, "1/a=1\n1/b=2\n1/c=3")
	tornReader := openReader(t, fsys, Options{})
	if info := tornReader.Recovery(); info.Seq != 3 || info.TruncatedAt != -1 {
		t.Errorf("Recovery of a reader with a torn tail: %+v", info)
	}
	checkDump(t, tornReader, "1/a=1\n1/b=2\n1/c=3")
	closeDB(t, tornReader)
	checkFilesUnchanged(t, fsys, before)
	// The record that the reader synced survives a crash.
	checkDump(t, reopen(t, fsys.Crash(loseAll), Options{}), "1/a=1\n1/b=2\n1/c=3")

	// A writer cuts the tail and goes on; the reader follows, while the writer runs and after it.
	db = reopen(t, fsys, Options{})
	putKey(t, db, 1, "e", "5")
	checkDump(t, rd, "1/a=1\n1/b=2\n1/c=3\n1/e=5")
	closeDB(t, db)
	checkDump(t, rd, "1/a=1\n1/b=2\n1/c=3\n1/e=5")

	// A record after a torn one is damage: the reader refuses it, and changes nothing.
	at := fileSize(t, fsys, seg1)
	appendRaw(t, fsys, seg1, append(commitFrame(h.salt, 5, 1, "f", "6")[:20], make([]byte, 4)...))
	appendRaw(t, fsys, seg1, commitFrame(h.salt, 6, 1, "g", "7"))
	before = fileMap(fsys)
	err := rd.View(context.Background(), func(*Tx) error { return nil })
	want := fmt.Sprintf("%s: corrupt at offset %d: a valid record follows at offset %d", simPath(seg1), at, at+24)
	if err == nil || err.Error() != want {
		t.Errorf("View: %v, want %s", err, want)
	}
	checkFilesUnchanged(t, fsys, before)
}

// TestReaderInvariant runs Views in two readers while the writer commits: each View sees one commit, whole, and never
// an older one than the View before it.
func TestReaderInvariant(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	fsys := osFS(t, dir)
	db, err := Create(fsys, Options{SegmentBytes: 2048, CheckpointBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const commits = 200
	var stop atomic.Bool
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	views := make([]int, 4)
	readers := []*DB{openReader(t, osFS(t, dir), Options{}), openReader(t, fsys, Options{})}
	for i := range 4 {
		rd := readers[i%2] // two goroutines share each reader
		wg.Go(func() {
			last := -1
			for done := false; !done; {
				done = stop.Load() // one more View after the last commit
				err := rd.View(ctx, func(tx *Tx) error {
					a, okA := tx.Get(1, []byte("a"))
					b, okB := tx.Get(2, []byte("b"))
					if okA != okB || string(a) != string(b) {
						return fmt.Errorf("a=%q (%v), b=%q (%v)", a, okA, b, okB)
					}
					n := 0
					if okA {
						n, _ = strconv.Atoi(string(a))
					}
					if n < last {
						return fmt.Errorf("commit %d after %d", n, last)
					}
					if done && n != commits {
						return fmt.Errorf("the last View sees commit %d", n)
					}
					last = n
					return nil
				})
				if err != nil {
					errs <- err
					return
				}
				views[i]++
			}
		})
	}
	for n := 1; n <= commits; n++ {
		if err := db.Update(ctx, func(tx *Tx) error {
			v := []byte(strconv.Itoa(n))
			return errors.Join(tx.Put(1, []byte("a"), v), tx.Put(2, []byte("b"), v))
		}); err != nil {
			t.Fatal(err)
		}
	}
	stop.Store(true)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	t.Logf("views: %v", views)
}

func TestReaderReadOnly(t *testing.T) {
	ctx := context.Background()
	fsys, db := newSim(t, Options{Placement: Files})
	change(t, db, 0)
	ops := recordOps(fsys)
	rd := openReader(t, fsys, Options{})
	readerOnly := func(from int, sync bool) int {
		t.Helper()
		all := ops()
		for _, op := range all[from:] {
			if op.N > 0 && (!sync || op.Kind != simfs.OpSync || op.Path != seg1) {
				t.Errorf("the reader made %v", op)
			}
		}
		return len(all)
	}
	called := false
	if err := rd.Update(ctx, func(*Tx) error { called = true; return nil }); !errors.Is(err, ErrReadOnly) || called {
		t.Errorf("Update: %v, f called: %v", err, called)
	}
	if err := rd.Checkpoint(); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Checkpoint: %v", err)
	}
	for name, f := range map[string]func(tx *Tx) error{
		"Put":         func(tx *Tx) error { return tx.Put(1, []byte("x"), nil) },
		"Delete":      func(tx *Tx) error { return tx.Delete(1, []byte("k00")) },
		"PutObject":   func(tx *Tx) error { _, err := tx.PutObject([]byte("x")); return err },
		"AppendEvent": func(tx *Tx) error { return tx.AppendEvent([]byte("x")) },
	} {
		if err := rd.View(ctx, f); !errors.Is(err, ErrReadOnly) || err.Error() != "read-only transaction" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got, want := stateOf(t, rd), stateOf(t, db); got != want {
		t.Errorf("the reader has:\n%s\nwant:\n%s", got, want)
	}
	readerOnly(0, false)
	closeDB(t, db)
	n := len(ops())
	_ = stateOf(t, rd)
	readerOnly(n, false)
	// A record that a killed writer left in the page cache: the reader syncs it, and makes no other change.
	h, _ := frames(t, fsys, seg1)
	appendRaw(t, fsys, seg1, commitFrame(h.salt, 2, 1, "late", "x"))
	n = len(ops())
	_ = stateOf(t, rd)
	readerOnly(n, true)
	if got := countOps(ops()[n:], func(op simfs.Op) bool { return op.Kind == simfs.OpSync }); got != 1 {
		t.Errorf("the reader synced %d times", got)
	}
	closeDB(t, rd)
	if err := rd.View(ctx, func(*Tx) error { return nil }); !errors.Is(err, errClosed) {
		t.Errorf("View after Close: %v", err)
	}
	if err := rd.Update(ctx, func(*Tx) error { return nil }); !errors.Is(err, errClosed) {
		t.Errorf("Update after Close: %v", err)
	}
	if err := rd.Checkpoint(); !errors.Is(err, errClosed) {
		t.Errorf("Checkpoint after Close: %v", err)
	}
	if err := rd.Events(ctx, func(Event) bool { return true }); !errors.Is(err, errClosed) {
		t.Errorf("Events after Close: %v", err)
	}
	if err := rd.Close(); err != nil {
		t.Errorf("a second Close: %v", err)
	}
}

func TestWriterWaitsForProbe(t *testing.T) {
	dir := t.TempDir()
	fsys := osFS(t, dir)
	db, err := Create(fsys, Options{})
	if err != nil {
		t.Fatal(err)
	}
	closeDB(t, db)
	// A reader holds lock/writer while it reads the end of the log: Open waits for it.
	release := lockFile(t, fsys, "lock/writer", vfs.Shared)
	start := time.Now()
	time.AfterFunc(100*time.Millisecond, release)
	db, err = Open(fsys, Options{WriterWait: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 100*time.Millisecond {
		t.Errorf("Open waited %v", d)
	}
	closeDB(t, db)
	release = lockFile(t, fsys, "lock/writer", vfs.Shared)
	start = time.Now()
	if _, err := Open(fsys, Options{WriterWait: 50 * time.Millisecond}); err == nil ||
		err.Error() != dir+": another process holds the writer lock" {
		t.Errorf("Open while a probe holds the lock: %v", err)
	}
	if d := time.Since(start); d < 50*time.Millisecond {
		t.Errorf("Open waited %v", d)
	}
	release()
	closeDB(t, reopen(t, fsys, Options{}))
}

// TestReaderLocks checks the locks that a reader holds, and those that keep it out.
func TestReaderLocks(t *testing.T) {
	fsys, db := newSim(t, Options{})
	closeDB(t, db)
	rd := openReader(t, fsys, Options{})
	for _, c := range []struct {
		name string
		mode vfs.LockMode
		held bool
	}{
		{"lock/entry", vfs.Exclusive, false},
		{"lock/readers", vfs.Shared, false},
		{"lock/readers", vfs.Exclusive, true},
		{"lock/writer", vfs.Exclusive, false},
	} {
		unlock, err := fsys.Lock(c.name, c.mode)
		if c.held != errors.Is(err, vfs.ErrLocked) || !c.held && err != nil {
			t.Errorf("%s while a reader is open: %v", c.name, err)
		}
		if err == nil {
			if err := unlock(); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A reader probes lock/writer shared: another probe does not make it think that a writer runs.
	release := lockFile(t, fsys, "lock/writer", vfs.Shared)
	h, _ := frames(t, fsys, seg1)
	appendRaw(t, fsys, seg1, commitFrame(h.salt, 1, 1, "late", "x"))
	checkDump(t, rd, "1/late=x")
	others := []*DB{openReader(t, fsys, Options{})}
	if info := others[0].Recovery(); info.Seq != 1 {
		t.Errorf("a reader that opens while another probes: %+v", info)
	}
	checkDump(t, others[0], "1/late=x")
	release()
	// An opener holds lock/entry shared while it takes its locks.
	release = lockFile(t, fsys, "lock/entry", vfs.Shared)
	others = append(others, openReader(t, fsys, Options{}))
	release()
	closeDB(t, rd)
	for _, r := range others {
		closeDB(t, r)
	}
	for _, name := range []string{"lock/entry", "lock/readers"} {
		release := lockFile(t, fsys, name, vfs.Exclusive)
		if _, err := OpenReader(fsys, Options{}); err == nil || err.Error() != simDir+": maintenance is running" {
			t.Errorf("OpenReader while %s is held: %v", name, err)
		}
		release()
	}
	refused := errors.New("refused")
	if _, err := OpenReader(fsys, Options{Accept: func(tx *Tx) error {
		if tx.Seq() != 1 {
			return fmt.Errorf("Accept sees seq %d", tx.Seq())
		}
		return refused
	}}); !errors.Is(err, refused) {
		t.Errorf("OpenReader with Accept: %v", err)
	}
	for _, name := range []string{"lock/entry", "lock/readers", "lock/writer"} {
		lockFile(t, fsys, name, vfs.Exclusive)() // the locks of a reader that closed or failed are released
	}
}

// TestReaderHead reads with a writer that holds lock/writer, with heads that the test writes: the reader reads up to
// the record that head names, never goes back, and refuses a head that the log does not have.
func TestReaderHead(t *testing.T) {
	ctx := context.Background()
	fsys, db := newSim(t, Options{})
	putKey(t, db, 1, "a", "1")
	st1 := db.cur.Load()
	putKey(t, db, 1, "b", "2")
	st2 := db.cur.Load()
	closeDB(t, db)
	dropCheckpoint(t, fsys)
	lockFile(t, fsys, "lock/writer", vfs.Exclusive) // a writer runs
	setHead := func(s headSlot) {
		t.Helper()
		setFile(t, fsys, "head", headFile(s))
	}
	setHead(headOf(st1, 0))
	rd := openReader(t, fsys, Options{})
	checkDump(t, rd, "1/a=1")
	view := func() error { return rd.View(ctx, func(*Tx) error { return nil }) }
	setHead(headOf(st2, 0))
	checkDump(t, rd, "1/a=1\n1/b=2")
	setHead(headOf(st1, 0)) // older than what the reader has: a writer that has not written it yet
	checkDump(t, rd, "1/a=1\n1/b=2")

	h, _ := frames(t, fsys, seg1)
	frame := commitFrame(h.salt, 3, 1, "c", "3")
	appendRaw(t, fsys, seg1, frame)
	st3 := headSlot{gen: 1, seg: 1, end: st2.end + segOff(len(frame)), seq: 3, crc: crcAt(t, fsys, seg1, int64(st2.end))}
	checkDump(t, rd, "1/a=1\n1/b=2") // head does not name it yet
	cases := []struct {
		name string
		s    func(s *headSlot)
	}{
		{"another CRC", func(s *headSlot) { s.crc++ }},
		{"another sequence number", func(s *headSlot) { s.seq++ }},
		{"an end inside the record", func(s *headSlot) { s.end -= 8 }},
		{"an end before that of the reader", func(s *headSlot) { s.end = st2.end - 8 }},
		{"an end after the file", func(s *headSlot) { s.end += 8 }},
		{"a later segment", func(s *headSlot) { s.seg++ }},
		{"a later generation", func(s *headSlot) { s.gen++ }},
	}
	for _, c := range append(cases, []struct {
		name string
		s    func(s *headSlot)
	}{
		{"the state of the reader at another end", func(s *headSlot) { *s = headOf(st2, 0); s.end -= 8 }},
		{"the state of the reader with another CRC", func(s *headSlot) { *s = headOf(st2, 0); s.crc++ }},
		{"the state of the reader in a later generation", func(s *headSlot) { *s = headOf(st2, 0); s.gen++ }},
	}...) {
		s := st3
		c.s(&s)
		setHead(s)
		if err := view(); err == nil || err.Error() != simDir+"/head: corrupt: the log does not match head" {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	// A reader that opens refuses them too.
	for _, c := range cases {
		s := st3
		c.s(&s)
		setHead(s)
		if _, err := OpenReader(fsys, Options{}); err == nil || err.Error() != simDir+"/head: corrupt: the log does not match head" {
			t.Errorf("OpenReader with %s: %v", c.name, err)
		}
	}
	setHead(st3)
	checkDump(t, rd, "1/a=1\n1/b=2\n1/c=3")
	setFile(t, fsys, "head", make([]byte, headSize))
	if err := view(); err == nil || err.Error() != simDir+"/head: corrupt: no valid slot" {
		t.Errorf("a head without a valid slot: %v", err)
	}
	// A reader that opens while head is older than the checkpoint starts from the checkpoint, which is durable.
	f, err := fsys.OpenFile("checkpoint", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(writeCheckpoint(f, rd.cur.Load()), f.Close(), fsys.SyncDir(".")); err != nil {
		t.Fatal(err)
	}
	setHead(headOf(st1, 0))
	late := openReader(t, fsys, Options{})
	if info := late.Recovery(); info.Checkpoint != "used" || info.Seq != 3 || info.Replayed != 0 {
		t.Errorf("a reader with head behind the checkpoint: %+v", info)
	}
	checkDump(t, late, "1/a=1\n1/b=2\n1/c=3")
	frame4 := commitFrame(h.salt, 4, 1, "d", "4")
	appendRaw(t, fsys, seg1, frame4)
	st4 := headSlot{gen: 1, seg: 1, end: st3.end + segOff(len(frame4)), seq: 4, crc: crcAt(t, fsys, seg1, int64(st3.end))}
	for _, c := range []struct {
		name string
		s    headSlot
		fn   func(s *headSlot)
	}{
		{"the record of the checkpoint at another end", st3, func(s *headSlot) { s.end -= 8 }},
		{"the record of the checkpoint with another CRC", st3, func(s *headSlot) { s.crc++ }},
		{"a record after the checkpoint with another CRC", st4, func(s *headSlot) { s.crc++ }},
	} {
		s := c.s
		c.fn(&s)
		setHead(s)
		if _, err := OpenReader(fsys, Options{}); err == nil || err.Error() != simDir+"/head: corrupt: the log does not match head" {
			t.Errorf("OpenReader with %s: %v", c.name, err)
		}
	}
	setHead(st4)
	if info := openReader(t, fsys, Options{}).Recovery(); info.Checkpoint != "used" || info.Seq != 4 || info.Replayed != 1 {
		t.Errorf("a reader with head after the checkpoint: %+v", info)
	}
	// A record before the end that head names is damaged: the reader refuses it, as damage, not as a torn tail.
	dropCheckpoint(t, fsys)
	b := fileData(t, fsys, seg1)
	b[st2.end+10] ^= 1 // in the body of the record
	setFile(t, fsys, seg1, b)
	setHead(st3)
	want := fmt.Sprintf("%s: corrupt at offset %d: bad record CRC", simPath(seg1), st2.end)
	if _, err := OpenReader(fsys, Options{}); err == nil || err.Error() != want {
		t.Errorf("OpenReader with a damaged record: %v, want %s", err, want)
	}
}

// TestReaderHeadSegments: head names a record in a later segment than the reader has, which the reader reads through
// the segments between; a missing one, or a record before the checkpoint, does not match.
func TestReaderHeadSegments(t *testing.T) {
	ctx := context.Background()
	o := Options{SegmentBytes: 100}
	fsys, db := newSim(t, o)
	var states []*state
	for i := range 6 {
		putKey(t, db, 1, fmt.Sprintf("k%d", i), "v")
		states = append(states, db.cur.Load())
	}
	closeDB(t, db)
	if states[5].seg < 4 {
		t.Fatalf("segments of the last record: %d", states[5].seg)
	}
	release := lockFile(t, fsys, "lock/writer", vfs.Exclusive)
	setFile(t, fsys, "head", headFile(headOf(states[0], 0)))
	dropCheckpoint(t, fsys)
	rd := openReader(t, fsys, Options{})
	if seqOf(t, rd) != 1 {
		t.Fatalf("seq %d", seqOf(t, rd))
	}
	// Segment 3 is lost: head names the last record, after it.
	c := copyFS(fsys)
	lockFile(t, c, "lock/writer", vfs.Exclusive)
	rc := openReader(t, c, Options{})
	removeFile(t, c, segName(1, 3))
	setFile(t, c, "head", headFile(headOf(states[5], 0)))
	if err := rc.View(ctx, func(*Tx) error { return nil }); err == nil ||
		err.Error() != "/data/head: corrupt: the log does not match head" {
		t.Errorf("View with a missing segment: %v", err)
	}
	setFile(t, fsys, "head", headFile(headOf(states[5], 0)))
	if got := seqOf(t, rd); got != 6 || rd.Stats().Segments != int(states[5].seg) {
		t.Errorf("seq %d, %d segments", got, rd.Stats().Segments)
	}
	// A checkpoint in a later segment than the record that head names, with a later sequence number.
	release()
	db = reopen(t, fsys, o)
	if err := db.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	closeDB(t, db)
	lockFile(t, fsys, "lock/writer", vfs.Exclusive)
	s := headOf(states[1], 0)
	s.seq = 7
	setFile(t, fsys, "head", headFile(s))
	if _, err := OpenReader(fsys, Options{}); err == nil || err.Error() != simDir+"/head: corrupt: the log does not match head" {
		t.Errorf("OpenReader: %v", err)
	}
}

// TestReaderHeadInBase: head names a record inside the base of the only generation, which is no record that a writer
// publishes.
func TestReaderHeadInBase(t *testing.T) {
	withBaseBytes(t, 50)
	fsys, db := newSim(t, Options{})
	for i := range 4 {
		putKey(t, db, 1, fmt.Sprintf("k%d", i), "a value")
	}
	if err := compactDB(db, Rewrite{}); err != nil {
		t.Fatal(err)
	}
	closeDB(t, db)
	dropCheckpoint(t, fsys)
	if !slices.Equal(gensOf(t, fsys), []uint64{2}) || baseRecords(t, fsys) < 2 {
		t.Fatalf("generations %v, %d base records", gensOf(t, fsys), baseRecords(t, fsys))
	}
	name := segName(2, 1)
	_, offs := frames(t, fsys, name)
	lockFile(t, fsys, "lock/writer", vfs.Exclusive)
	setFile(t, fsys, "head", headFile(headSlot{gen: 2, seg: 1, end: segOff(offs[1]), seq: 4, crc: crcAt(t, fsys, name, offs[0])}))
	if _, err := OpenReader(fsys, Options{}); err == nil || err.Error() != simDir+"/head: corrupt: the log does not match head" {
		t.Errorf("OpenReader: %v", err)
	}
}

// TestReaderDuringWriterOpen opens a reader while a writer opens a store whose compaction was killed after the
// base-end: head still names the old generation, but the reader takes the new one, as the writer does.
func TestReaderDuringWriterOpen(t *testing.T) {
	fsys, db := newSim(t, Options{})
	putKey(t, db, 1, "a", "1")
	putKey(t, db, 2, "b", "2")
	var snap *simfs.FS
	db.o.Hook = func(p string) error {
		if p == "compact:base-end" {
			snap = copyFS(fsys)
		}
		return nil
	}
	if err := compactDB(db, Rewrite{Value: func(table uint32, _, v []byte) ([]byte, bool) { return v, table != 2 }}); err != nil {
		t.Fatal(err)
	}
	closeDB(t, db)
	if s := readHeadFile(t, snap); s.gen != 1 {
		t.Fatalf("head names generation %d", s.gen)
	}
	// Records after the base-end, which head does not name: the reader does not know them to be durable.
	name := segName(2, 1)
	h, _ := frames(t, snap, name)
	appendRaw(t, snap, name, commitFrame(h.salt, 3, 1, "c", "3"))
	writeSeg(t, snap, 2, 2, 4, appendCommit(nil, 4, []op{{code: opPut, table: 1, key: "d", value: []byte("4")}}))
	release := lockFile(t, snap, "lock/writer", vfs.Exclusive)
	rd := openReader(t, snap, Options{})
	if info := rd.Recovery(); info.Generation != 2 || info.Seq != 2 || info.Checkpoint != "none" ||
		info.IgnoredGenerations != nil {
		t.Errorf("Recovery: %+v", info)
	}
	checkDump(t, rd, "1/a=1")
	release()
	db = reopen(t, snap, Options{})
	if info := db.Recovery(); info.Generation != 2 || info.Seq != 4 {
		t.Errorf("Recovery of the writer: %+v", info)
	}
	checkDump(t, rd, "1/a=1\n1/c=3\n1/d=4")
	putKey(t, db, 1, "e", "5")
	checkDump(t, rd, "1/a=1\n1/c=3\n1/d=4\n1/e=5")
	if got, want := stateOf(t, rd), stateOf(t, db); got != want {
		t.Errorf("the reader has:\n%s\nwant:\n%s", got, want)
	}
}

// TestReaderSyncsBase opens a reader, while a writer opens, on a store whose compaction was killed after it wrote the
// base-end and before it synced it: the reader takes the new generation, and syncs it.
func TestReaderSyncsBase(t *testing.T) {
	fsys, db := newSim(t, Options{})
	putKey(t, db, 1, "a", "1")
	putKey(t, db, 1, "b", "2")
	closeDB(t, db)
	b, salt := genSeg(2, 1, 2)
	b = appendFrame(b, salt, appendBase(nil, 2, 0, []op{
		{code: opPut, table: 1, key: "a", value: []byte("1")}, {code: opPut, table: 1, key: "b", value: []byte("2")}}))
	b = appendFrame(b, salt, appendBaseEnd(nil, 2, baseEnd{parts: 1, keys: 2, nextEvent: 1}))
	f, err := fsys.OpenFile(segName(2, 1), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(b, 0); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(f.Close(), fsys.SyncDir("log")); err != nil {
		t.Fatal(err)
	}
	release := lockFile(t, fsys, "lock/writer", vfs.Exclusive)
	rd := openReader(t, fsys, Options{})
	if info := rd.Recovery(); info.Generation != 2 || info.Seq != 2 || info.Checkpoint != "stale" {
		t.Errorf("Recovery: %+v", info)
	}
	checkDump(t, rd, "1/a=1\n1/b=2")
	if p := pendingOn(fsys, segName(2, 1)); len(p) != 0 {
		t.Errorf("the reader did not sync the base: %v", p)
	}
	release()
	checkDump(t, rd, "1/a=1\n1/b=2") // no writer: the reader reads the end of generation 2 only
}

// TestReaderTornLastSegment reads a store whose writer was killed while it began a segment: the reader does not keep
// that segment open, and follows the writer that removes it and makes it again.
func TestReaderTornLastSegment(t *testing.T) {
	o := Options{SegmentBytes: 100}
	fsys, db := newSim(t, o)
	putKey(t, db, 1, "a", "1")
	putKey(t, db, 1, "b", "2")
	closeDB(t, db)
	if logNames(t, fsys)[len(logNames(t, fsys))-1] != seg2 {
		t.Fatalf("segments %v", logNames(t, fsys))
	}
	setFile(t, fsys, seg3, make([]byte, 100))
	before := fileMap(fsys)
	rd := openReader(t, fsys, Options{})
	if info := rd.Recovery(); info.RemovedSegment != "" || info.Seq != 2 {
		t.Errorf("Recovery: %+v", info)
	}
	checkDump(t, rd, "1/a=1\n1/b=2")
	checkFilesUnchanged(t, fsys, before)
	checkDump(t, rd, "1/a=1\n1/b=2")
	rd.handles.mu.Lock()
	kept := rd.handles.files[segID{1, 3}] != nil
	rd.handles.mu.Unlock()
	if kept {
		t.Error("the reader keeps a segment whose header is not valid")
	}
	db = reopen(t, fsys, o) // removes it
	putKey(t, db, 1, "c", "3")
	putKey(t, db, 1, "d", "4")
	if !exists(t, fsys, seg3) || fileSize(t, fsys, seg3) == 100 {
		t.Fatalf("segments %v", logNames(t, fsys))
	}
	checkDump(t, rd, "1/a=1\n1/b=2\n1/c=3\n1/d=4")
	closeDB(t, db)
	checkDump(t, rd, "1/a=1\n1/b=2\n1/c=3\n1/d=4")
}

// TestReaderErrors shows that a refresh that fails publishes nothing, and that a reader stops after a Sync failed.
func TestReaderErrors(t *testing.T) {
	ctx := context.Background()
	fsys, db := newSim(t, Options{})
	putKey(t, db, 1, "a", "1")
	rd := openReader(t, fsys, Options{})
	putKey(t, db, 1, "b", "2")
	fsys.Inject(simfs.Fault{Match: on(simfs.OpRead, seg1), Err: syscall.EIO, Times: 1})
	if err := rd.View(ctx, func(*Tx) error { return nil }); !errors.Is(err, syscall.EIO) {
		t.Errorf("View with a read error: %v", err)
	}
	if seq := rd.cur.Load().seq; seq != 1 {
		t.Errorf("a failed refresh published seq %d", seq)
	}
	checkDump(t, rd, "1/a=1\n1/b=2")
	// A View that waits for the refresh that runs stops when its context ends.
	rd.rd.turn <- struct{}{}
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	called := false
	if err := rd.View(short, func(*Tx) error { called = true; return nil }); !errors.Is(err, context.DeadlineExceeded) || called {
		t.Errorf("View that waits: %v, f called: %v", err, called)
	}
	<-rd.rd.turn
	canceled, cancel2 := context.WithCancel(ctx)
	cancel2()
	if err := rd.View(canceled, func(*Tx) error { called = true; return nil }); !errors.Is(err, context.Canceled) || called {
		t.Errorf("View with a canceled context: %v, f called: %v", err, called)
	}
	if err := rd.Events(canceled, func(Event) bool { return true }); !errors.Is(err, context.Canceled) {
		t.Errorf("Events with a canceled context: %v", err)
	}
	// A context that ends once the View has begun, while it takes its turn.
	err := rd.View(&countdownContext{Context: ctx, left: 1}, func(*Tx) error { called = true; return nil })
	if !errors.Is(err, context.Canceled) || called {
		t.Errorf("View with a context that ends: %v, f called: %v", err, called)
	}
	fsys.Inject(simfs.Fault{Match: on(simfs.OpLock, "lock/writer"), Err: syscall.EIO, Times: 1})
	if err := rd.View(ctx, func(*Tx) error { return nil }); !errors.Is(err, syscall.EIO) {
		t.Errorf("View with a lock error: %v", err)
	}
	closeDB(t, db)
	h, _ := frames(t, fsys, seg1)
	appendRaw(t, fsys, seg1, commitFrame(h.salt, 3, 1, "c", "3"))
	fsys.Inject(simfs.Fault{Match: on(simfs.OpSync, seg1), Err: syscall.EIO, Times: 1})
	for range 2 {
		if err := rd.View(ctx, func(*Tx) error { return nil }); !errors.Is(err, ErrFailed) || !errors.Is(err, syscall.EIO) {
			t.Errorf("View after a failed Sync: %v", err)
		}
	}
	if seq := rd.cur.Load().seq; seq != 2 {
		t.Errorf("a reader published seq %d after a failed Sync", seq)
	}
	// So does a reader that opens.
	fsys.Inject(simfs.Fault{Match: on(simfs.OpSync, seg1), Err: syscall.EIO, Times: 1})
	if _, err := OpenReader(fsys, Options{}); !errors.Is(err, ErrFailed) {
		t.Errorf("OpenReader with a failed Sync: %v", err)
	}
	fsys.Inject(simfs.Fault{Match: on(simfs.OpLock, "lock/writer"), Err: syscall.EIO, Times: 1})
	if _, err := OpenReader(fsys, Options{}); !errors.Is(err, syscall.EIO) {
		t.Errorf("OpenReader with a lock error: %v", err)
	}
	late := openReader(t, fsys, Options{})
	checkDump(t, late, "1/a=1\n1/b=2\n1/c=3")
	// A refresh after Close, as that of a View that began before it, with and without a writer.
	closeDB(t, late)
	if err := late.rd.refresh(ctx, late); !errors.Is(err, errClosed) {
		t.Errorf("refresh after Close: %v", err)
	}
	lockFile(t, fsys, "lock/writer", vfs.Exclusive)
	if err := late.rd.refresh(ctx, late); !errors.Is(err, errClosed) {
		t.Errorf("refresh after Close while a writer runs: %v", err)
	}
}

// TestReaderMissingSegment: a segment that a reader has not read yet is missing.
func TestReaderMissingSegment(t *testing.T) {
	ctx := context.Background()
	o := Options{SegmentBytes: 100}
	for _, c := range []struct {
		name, remove string
		index        int
	}{
		{"before the last", seg3, 3},
		{"the last that the reader has", seg2, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			fsys, db := newSim(t, o)
			for i := range 3 {
				putKey(t, db, 1, fmt.Sprintf("k%d", i), "v")
			}
			closeDB(t, db)
			rd := openReader(t, fsys, Options{})
			db = reopen(t, fsys, o)
			for i := 3; i < 6; i++ {
				putKey(t, db, 1, fmt.Sprintf("k%d", i), "v")
			}
			closeDB(t, db)
			if len(logNames(t, fsys)) < 4 {
				t.Fatalf("segments %v", logNames(t, fsys))
			}
			if c.remove == seg2 {
				for _, name := range logNames(t, fsys)[1:] {
					removeFile(t, fsys, name)
				}
			} else {
				removeFile(t, fsys, c.remove)
			}
			want := fmt.Sprintf("%s: corrupt: missing segment %d of generation 1", simPath("log"), c.index)
			if err := rd.View(ctx, func(*Tx) error { return nil }); err == nil || err.Error() != want {
				t.Errorf("View: %v, want %s", err, want)
			}
		})
	}
}

// TestReaderSnapshot: a View sees the state that it first read, while refreshes of other Views publish newer ones.
func TestReaderSnapshot(t *testing.T) {
	ctx := context.Background()
	fsys, db := newSim(t, Options{})
	putKey(t, db, 1, "a", "1")
	rd := openReader(t, fsys, Options{})
	read, next, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- rd.View(ctx, func(tx *Tx) error {
			v, _ := tx.Get(1, []byte("a"))
			close(read)
			<-next
			w, _ := tx.Get(1, []byte("a"))
			_, ok := tx.Get(1, []byte("b"))
			if string(v) != "1" || string(w) != "1" || ok || tx.Seq() != 1 {
				return fmt.Errorf("a=%q, then a=%q, b %v, seq %d", v, w, ok, tx.Seq())
			}
			return nil
		})
	}()
	wait(t, read, "read")
	putKey(t, db, 1, "a", "2")
	putKey(t, db, 1, "b", "3")
	checkDump(t, rd, "1/a=2\n1/b=3")
	close(next)
	if err := wait(t, done, "View"); err != nil {
		t.Error(err)
	}
	// Events refreshes as a View does.
	if err := db.Update(ctx, appendF("e")); err != nil {
		t.Fatal(err)
	}
	if got := eventLines(t, rd); got != "1/4=e" {
		t.Errorf("events: %s", got)
	}
}

func TestReaderReleasesFiles(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("no /proc/self/fd")
	}
	open := func() int {
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	dir := t.TempDir()
	fsys := osFS(t, dir)
	o := Options{SegmentBytes: 300, Placement: Hybrid, Threshold: 150}
	db, err := Create(fsys, o)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		change(t, db, i)
	}
	closeDB(t, db)
	o.SegmentBytes = 1 << 20 // the writer goes on in its last segment, so that it opens no more files
	db = reopen(t, fsys, o)
	n := 0
	use := func() {
		rd, err := OpenReader(fsys, Options{})
		if err != nil {
			t.Fatal(err)
		}
		_ = stateOf(t, rd)
		n++
		putKey(t, db, 3, "x", strconv.Itoa(n))
		_ = stateOf(t, rd)
		closeDB(t, rd)
	}
	use()
	before := open()
	use()
	if after := open(); after != before {
		t.Errorf("%d files open after a reader, %d before", after, before)
	}
}

// TestReaderCloseDuringView: a View of a reader goes on reading when Close is called, and Close waits for its refresh.
func TestReaderCloseDuringView(t *testing.T) {
	ctx := context.Background()
	fsys, db := newSim(t, Options{})
	change(t, db, 0)
	change(t, db, 1)
	rd := openReader(t, fsys, Options{})
	read, closed, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- rd.View(ctx, func(tx *Tx) error {
			_ = tx.Seq()
			close(read)
			<-closed
			var err error
			tx.Objects(func(h [32]byte, _ int64) bool {
				_, err = tx.Object(h)
				return err == nil
			})
			return err
		})
	}()
	wait(t, read, "read")
	closeDB(t, rd)
	close(closed)
	if err := wait(t, done, "View"); err != nil {
		t.Errorf("View after Close: %v", err)
	}
	// Close waits for the refresh that runs.
	rd = openReader(t, fsys, Options{})
	rd.rd.turn <- struct{}{}
	closing := make(chan error, 1)
	go func() { closing <- rd.Close() }()
	select {
	case err := <-closing:
		t.Fatalf("Close did not wait: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	<-rd.rd.turn
	if err := wait(t, closing, "Close"); err != nil {
		t.Error(err)
	}
}

// flakyHead is a file head whose first reads see no valid slot, as while a writer writes it again whole.
type flakyHead struct {
	vfs.File
	bad int
}

func (f *flakyHead) ReadAt(b []byte, off int64) (int, error) {
	n, err := f.File.ReadAt(b, off)
	if f.bad > 0 {
		f.bad--
		clear(b[:n])
	}
	return n, err
}

func TestReadHeadAgain(t *testing.T) {
	fsys, db := newSim(t, Options{})
	putKey(t, db, 1, "a", "1")
	closeDB(t, db)
	f, err := fsys.Open("head")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	for _, bad := range []int{0, 1, 3, 4} {
		s, err := readHeadAgain(&flakyHead{File: f, bad: bad}, "head")
		if bad <= 3 && (err != nil || s.seq != 1) || bad > 3 && (err == nil || err.Error() != "head: corrupt: no valid slot") {
			t.Errorf("%d bad reads: %+v, %v", bad, s, err)
		}
	}
}

// shortFS is a file system whose segments report 64 bytes more than they have, as a writer that cuts a segment
// between the Stat and the read of a reader makes them seem.
type shortFS struct {
	vfs.FS
}

func (s shortFS) Open(name string) (vfs.File, error) {
	f, err := s.FS.Open(name)
	if err != nil || !strings.HasPrefix(name, "log/") {
		return f, err
	}
	return longFile{f}, nil
}

type longFile struct {
	vfs.File
}

func (f longFile) Stat() (fs.FileInfo, error) {
	info, err := f.File.Stat()
	if err != nil {
		return nil, err
	}
	return longInfo{info}, nil
}

type longInfo struct {
	fs.FileInfo
}

func (i longInfo) Size() int64 {
	return i.FileInfo.Size() + 64
}

// TestReaderShortRead: a reader takes the bytes that a segment has when it reads it, which another process may cut
// after it took the size; the writer, which no other process changes the log under, refuses such a read.
func TestReaderShortRead(t *testing.T) {
	fsys, db := newSim(t, Options{})
	putKey(t, db, 1, "a", "1")
	putKey(t, db, 1, "b", "2")
	closeDB(t, db)
	for _, writer := range []bool{false, true} {
		release := func() {}
		if writer {
			release = lockFile(t, fsys, "lock/writer", vfs.Exclusive)
		}
		rd := openReader(t, shortFS{fsys}, Options{})
		checkDump(t, rd, "1/a=1\n1/b=2")
		closeDB(t, rd)
		release()
	}
	if _, err := Open(shortFS{fsys}, Options{}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("Open: %v", err)
	}
}
