package logdb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

// eventLines returns the events of db, as lines "<number>/<seq>=<data>".
func eventLines(t *testing.T, db *DB) string {
	t.Helper()
	lines, err := events(db)
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

func events(db *DB) (string, error) {
	var lines []string
	err := db.Events(context.Background(), func(e Event) bool {
		lines = append(lines, fmt.Sprintf("%d/%d=%s", e.Number, e.Seq, e.Data))
		return true
	})
	return strings.Join(lines, "\n"), err
}

// countdownContext ends after left calls of Err, without closing Done.
type countdownContext struct {
	context.Context
	left int
}

func (c *countdownContext) Err() error {
	if c.left == 0 {
		return context.Canceled
	}
	c.left--
	return nil
}

// appendF returns a function for Update that appends events with data.
func appendF(data ...string) func(*Tx) error {
	return func(tx *Tx) error {
		for _, d := range data {
			if err := tx.AppendEvent([]byte(d)); err != nil {
				return err
			}
		}
		return nil
	}
}

func TestEventNumbers(t *testing.T) {
	ctx := context.Background()
	fsys, db := newSim(t, Options{})
	update := func(f func(*Tx) error) {
		t.Helper()
		if err := db.Update(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	update(appendF("e1", "e2"))
	boom := errors.New("boom")
	if err := db.Update(ctx, func(tx *Tx) error {
		if err := tx.AppendEvent([]byte("aborted")); err != nil {
			return err
		}
		return boom
	}); !errors.Is(err, boom) {
		t.Errorf("an aborted Update: %v", err)
	}
	fsys.Inject(simfs.Fault{Match: on(simfs.OpWrite, seg1), Err: syscall.EIO, Times: 1})
	if err := db.Update(ctx, appendF("failed")); !errors.Is(err, syscall.EIO) {
		t.Errorf("a failing Update: %v", err)
	}
	update(func(tx *Tx) error {
		return errors.Join(tx.Put(1, []byte("k"), []byte("v")), tx.AppendEvent([]byte("e3")))
	})
	putKey(t, db, 1, "l", "w")
	update(func(tx *Tx) error {
		data := []byte("e4")
		if err := tx.AppendEvent(data); err != nil {
			return err
		}
		data[1] = 'x' // AppendEvent has copied it
		return appendF("e5", "")(tx)
	})
	want := "1/1=e1\n2/1=e2\n3/2=e3\n4/4=e4\n5/4=e5\n6/4="
	if got := eventLines(t, db); got != want {
		t.Errorf("events:\n%s\nwant:\n%s", got, want)
	}
	if s := db.Stats(); s.Events != 6 {
		t.Errorf("Stats: %d events", s.Events)
	}
	for limit := range 6 {
		n := 0
		if err := db.Events(ctx, func(Event) bool {
			n++
			return n <= limit
		}); err != nil || n != limit+1 {
			t.Errorf("Events stopping after %d: %d calls, %v", limit+1, n, err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := db.Events(canceled, func(Event) bool {
		t.Error("fn called")
		return true
	}); !errors.Is(err, context.Canceled) {
		t.Errorf("Events with a canceled context: %v", err)
	}
	// ctx is checked before each record: Events stops before the third.
	n := 0
	if err := db.Events(&countdownContext{Context: ctx, left: 3}, func(Event) bool {
		n++
		return true
	}); !errors.Is(err, context.Canceled) || n != 3 {
		t.Errorf("Events with a context that ends: %d events, %v", n, err)
	}
	closeDB(t, db)
	if err := db.Events(ctx, func(Event) bool { return true }); !errors.Is(err, errClosed) {
		t.Errorf("Events after Close: %v", err)
	}
	db = reopen(t, fsys, Options{})
	if got := eventLines(t, db); got != want {
		t.Errorf("events after reopening:\n%s", got)
	}
	update = func(f func(*Tx) error) {
		t.Helper()
		if err := db.Update(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	update(appendF("e7"))
	if got := eventLines(t, db); got != want+"\n7/5=e7" {
		t.Errorf("events after reopening and appending:\n%s", got)
	}
}

// TestEventsAtomic checks that an event and the other writes of its Update are committed together: after every crash
// during the Update, both are there or neither is.
func TestEventsAtomic(t *testing.T) {
	ctx := context.Background()
	step := func(db *DB) error {
		return db.Update(ctx, func(tx *Tx) error {
			return errors.Join(tx.Put(1, []byte("b"), []byte("2")), tx.AppendEvent([]byte("b put")))
		})
	}
	check := func(t *testing.T, fsys *simfs.FS, name string) {
		t.Helper()
		db, err := Open(fsys, Options{})
		if err != nil {
			t.Fatalf("%s: Open: %v", name, err)
		}
		defer closeDB(t, db)
		got := dump(t, db) + "\n" + eventLines(t, db)
		if got != "1/a=1\n1/1=" && got != "1/a=1\n1/b=2\n1/1=\n2/2=b put" {
			t.Errorf("%s:\n%s", name, got)
		}
	}
	setup := func() (*simfs.FS, *DB) {
		fsys, db := newSim(t, Options{})
		if err := db.Update(ctx, func(tx *Tx) error {
			return errors.Join(tx.Put(1, []byte("a"), []byte("1")), tx.AppendEvent(nil))
		}); err != nil {
			t.Fatal(err)
		}
		return fsys, db
	}
	fsys, db := setup()
	start := fsys.Counted()
	if err := step(db); err != nil {
		t.Fatal(err)
	}
	ops := fsys.Counted() - start
	for k := range ops + 1 {
		fsys, db := setup()
		fsys.CrashAt(fsys.Counted() + k)
		err := step(db)
		for _, m := range simfs.Models(fsys.Pending(), 16, nil) {
			check(t, fsys.Crash(m), fmt.Sprintf("a crash after %d of %d operations (%v), %s", k, ops, err, m.Name))
		}
	}
	// After a failed Sync, the outcome is unknown, but whole.
	fsys, db = setup()
	fsys.Inject(simfs.Fault{Match: on(simfs.OpSync, seg1), Err: syscall.EIO, Times: 1})
	checkFailed(t, db, step(db), syscall.EIO)
	for _, m := range []simfs.Model{keepAll(fsys), loseAll} {
		check(t, fsys.Crash(m), "after a failed sync, "+m.Name)
	}
}

func TestEventsFromDisk(t *testing.T) {
	ctx := context.Background()
	o := Options{SegmentBytes: 200}
	fsys, db := newSim(t, o)
	var want []string
	for i := range 8 {
		data := strings.Repeat(string(rune('a'+i)), 30+i)
		if err := db.Update(ctx, appendF(data)); err != nil {
			t.Fatal(err)
		}
		want = append(want, fmt.Sprintf("%d/%d=%s", i+1, i+1, data))
	}
	if n := len(logNames(t, fsys)); n < 3 {
		t.Fatalf("%d segments", n)
	}
	if got := eventLines(t, db); got != strings.Join(want, "\n") {
		t.Errorf("events across segments:\n%s", got)
	}
	// An Update that has written its record, but not committed it: its event is not there yet, and Events does not
	// read it.
	o.SegmentBytes = 1 << 20
	written, release := make(chan struct{}), make(chan struct{})
	o.Hook = func(point string) error {
		if point == "update:written" {
			close(written)
			<-release
		}
		return nil
	}
	closeDB(t, db)
	db = reopen(t, fsys, o)
	done := make(chan error)
	go func() { done <- db.Update(ctx, appendF("in flight")) }()
	<-written
	// Nothing here may stop the test before release is closed: Close would wait for the Update.
	last, end := segName(1, db.cur.Load().seg), db.cur.Load().end
	if info, err := fsys.Lstat(last); err != nil || info.Size() <= end {
		t.Errorf("the record is not after the end: %v", err)
	}
	var guard atomic.Bool
	guard.Store(true)
	fsys.Inject(simfs.Fault{Err: syscall.EIO, Match: func(op simfs.Op) bool {
		return guard.Load() && op.Kind == simfs.OpRead && op.Path == last && op.Off+int64(op.Len) > end
	}})
	if got, err := events(db); err != nil || got != strings.Join(want, "\n") {
		t.Errorf("events while an Update writes: %v\n%s", err, got)
	}
	guard.Store(false)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := eventLines(t, db); got != strings.Join(append(want, "9/9=in flight"), "\n") {
		t.Errorf("events after the Update:\n%s", got)
	}
	// Events reads the log: it finds a damaged record.
	_, offs := frames(t, fsys, seg2)
	b := fileData(t, fsys, seg2)
	b[offs[0]+30] ^= 1
	setFile(t, fsys, seg2, b)
	err := db.Events(ctx, func(Event) bool { return true })
	checkCorrupt(t, err, simPath(seg2), offs[0], "bad record CRC")
}

func TestStats(t *testing.T) {
	ctx := context.Background()
	o := Options{Placement: Hybrid, Threshold: 100, SegmentBytes: 300}
	fsys, db := newSim(t, o)
	if s := db.Stats(); s.Segments != 1 || s.LogBytes != segHeaderSize || len(s.Keys) != 0 || s.Events != 0 {
		t.Errorf("Stats of a new store: %+v", s)
	}
	if err := db.Update(ctx, func(tx *Tx) error {
		_, err1 := tx.PutObject(objData(1, 50))
		_, err2 := tx.PutObject(objData(2, 200))
		return errors.Join(err1, err2, tx.Put(1, []byte("a"), []byte("1")), tx.Put(1, []byte("bb"), []byte("22")),
			tx.Put(2, []byte("c"), []byte("333")), tx.AppendEvent([]byte("e1")))
	}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := db.Update(ctx, appendF(strings.Repeat("e", 100))); err != nil {
			t.Fatal(err)
		}
	}
	delKey(t, db, 1, "a")
	fsys.Inject(simfs.Fault{Match: on(simfs.OpWrite, "head"), Err: syscall.EIO, Times: 2})
	putObjects(t, db, objData(3, 10))
	putKey(t, db, 3, "", "")
	check := func(db *DB, headErrors int64) {
		t.Helper()
		var size int64
		names := logNames(t, fsys)
		for _, name := range names {
			size += fileSize(t, fsys, name)
		}
		s := db.Stats()
		want := Stats{Keys: map[uint32]int{1: 1, 2: 1, 3: 1}, KeyBytes: 3, ValueBytes: 5, InlineObjects: 2, FileObjects: 1,
			Events: 4, LogBytes: size, Segments: len(names), HeadErrors: headErrors}
		if fmt.Sprint(s) != fmt.Sprint(want) {
			t.Errorf("Stats:\n%+v\nwant:\n%+v", s, want)
		}
	}
	if len(logNames(t, fsys)) < 3 {
		t.Fatalf("segments %q", logNames(t, fsys))
	}
	check(db, 2)
	closeDB(t, db)
	check(reopen(t, fsys, o), 0)
}

// TestEventsDuringClose checks that Events, like a View, goes on reading when Close is called, and that the files are
// closed when it ends.
func TestEventsDuringClose(t *testing.T) {
	ctx := context.Background()
	_, db := newSim(t, Options{SegmentBytes: 200})
	for i := range 6 {
		if err := db.Update(ctx, appendF(strings.Repeat(string(rune('a'+i)), 80))); err != nil {
			t.Fatal(err)
		}
	}
	read, closed, done := make(chan struct{}), make(chan struct{}), make(chan error)
	n := 0
	go func() {
		done <- db.Events(ctx, func(Event) bool {
			if n == 0 {
				close(read)
				<-closed
			}
			n++
			return true
		})
	}()
	<-read
	closeDB(t, db)
	close(closed)
	if err := <-done; err != nil || n != 6 {
		t.Errorf("Events during Close: %d events, %v", n, err)
	}
	db.handles.mu.Lock()
	files := db.handles.files
	db.handles.mu.Unlock()
	if files != nil {
		t.Errorf("%d files are open after Events", len(files))
	}
	if err := db.Events(ctx, func(Event) bool { return true }); !errors.Is(err, errClosed) {
		t.Errorf("Events after Close: %v", err)
	}
}

func TestEventsNotInMemory(t *testing.T) {
	ctx := context.Background()
	db, err := Create(osFS(t, t.TempDir()), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB(t, db)
	heap := func() uint64 {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.HeapAlloc
	}
	data := bytes.Repeat([]byte("event data "), 100<<10) // 1.1 MB
	before := heap()
	for range 16 {
		if err := db.Update(ctx, func(tx *Tx) error { return tx.AppendEvent(data) }); err != nil {
			t.Fatal(err)
		}
	}
	after := heap()
	if grown := int64(after) - int64(before); grown > 4<<20 {
		t.Errorf("the heap has grown by %d bytes with %d bytes of events", grown, 16*len(data))
	}
	if s := db.Stats(); s.Events != 16 || s.KeyBytes != 0 || s.ValueBytes != 0 || s.InlineObjects != 0 {
		t.Errorf("Stats: %+v", s)
	}
	n := 0
	if err := db.Events(ctx, func(e Event) bool {
		n++
		if !bytes.Equal(e.Data, data) {
			t.Errorf("event %d has %d bytes", e.Number, len(e.Data))
		}
		return true
	}); err != nil || n != 16 {
		t.Errorf("Events: %d, %v", n, err)
	}
}
