package logdb

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

func maintain(t *testing.T, fsys vfs.FS, o Options, f func(m *Maintenance) error) {
	t.Helper()
	if err := Maintain(context.Background(), fsys, o, f); err != nil {
		t.Fatal(err)
	}
}

func gc(t *testing.T, m *Maintenance) GCResult {
	t.Helper()
	res, err := m.CollectGarbage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// waitLocked waits until name is locked exclusively by another, as Maintain locks lock/entry first.
func waitLocked(t *testing.T, fsys vfs.FS, name string) {
	t.Helper()
	for start := time.Now(); time.Since(start) < 10*time.Second; time.Sleep(time.Millisecond) {
		unlock, err := fsys.Lock(name, vfs.Shared)
		if errors.Is(err, vfs.ErrLocked) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := unlock(); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatalf("%s is not locked", name)
}

// TestMaintainWaits: Maintain waits for the writer and the readers to close, until its context ends.
func TestMaintainWaits(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	fsys := osFS(t, dir)
	db, err := Create(fsys, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	putKey(t, db, 1, "a", "1")
	rd := openReader(t, osFS(t, dir), Options{})
	var called atomic.Bool
	f := func(m *Maintenance) error {
		called.Store(true)
		return m.View(ctx, func(tx *Tx) error {
			if v, _ := tx.Get(1, []byte("a")); string(v) != "1" {
				return fmt.Errorf("a=%q", v)
			}
			return nil
		})
	}
	short := func() error {
		start := time.Now() // before the deadline is set
		c, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		err := Maintain(c, fsys, Options{}, f)
		if d := time.Since(start); d < 100*time.Millisecond {
			t.Errorf("Maintain waited %v", d)
		}
		return err
	}
	for _, closeOne := range []func(){func() { closeDB(t, db) }, func() {}} {
		if err := short(); !errors.Is(err, context.DeadlineExceeded) || called.Load() {
			t.Errorf("Maintain while the store is open: %v, f called: %v", err, called.Load())
		}
		closeOne()
	}
	// It waits for lock/writer too, which a reader that probes holds.
	closeDB(t, rd)
	release := lockFile(t, fsys, "lock/writer", vfs.Shared)
	if err := short(); !errors.Is(err, context.DeadlineExceeded) || called.Load() {
		t.Errorf("Maintain while lock/writer is held: %v, f called: %v", err, called.Load())
	}
	release()
	rd = openReader(t, osFS(t, dir), Options{})
	done := make(chan error, 1)
	go func() { done <- Maintain(ctx, fsys, Options{}, f) }()
	select {
	case err := <-done:
		t.Fatalf("Maintain did not wait for the reader: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	closeDB(t, rd)
	if err := wait(t, done, "Maintain"); err != nil || !called.Load() {
		t.Errorf("Maintain: %v, f called: %v", err, called.Load())
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	called.Store(false)
	if err := Maintain(canceled, fsys, Options{}, f); !errors.Is(err, context.Canceled) || called.Load() {
		t.Errorf("Maintain with a canceled context: %v", err)
	}
	closeDB(t, reopen(t, fsys, Options{WriterWait: time.Millisecond}))
}

// TestOpenDuringMaintenance: while Maintain runs, or waits for the processes that have the store open, the others fail
// at once.
func TestOpenDuringMaintenance(t *testing.T) {
	ctx := context.Background()
	fsys, db := newSim(t, Options{})
	putKey(t, db, 1, "a", "1")
	closeDB(t, db)
	refused := func(what string, err error) {
		t.Helper()
		if err == nil || err.Error() != simDir+": maintenance is running" {
			t.Errorf("%s during maintenance: %v", what, err)
		}
	}
	openers := func() {
		t.Helper()
		_, err := Open(fsys, Options{WriterWait: time.Second})
		refused("Open", err)
		_, err = OpenReader(fsys, Options{})
		refused("OpenReader", err)
		_, err = Backup(ctx, fsys, simfs.New("/backup"), nil)
		refused("Backup", err)
	}
	maintain(t, fsys, Options{}, func(*Maintenance) error {
		openers()
		return nil
	})
	rd := openReader(t, fsys, Options{})
	done := make(chan error, 1)
	go func() { done <- Maintain(ctx, fsys, Options{}, func(*Maintenance) error { return nil }) }()
	waitLocked(t, fsys, "lock/entry")
	openers()
	checkDump(t, rd, "1/a=1") // the reader that is open goes on
	closeDB(t, rd)
	if err := wait(t, done, "Maintain"); err != nil {
		t.Error(err)
	}
}

func TestMaintainErrors(t *testing.T) {
	ctx := context.Background()
	fsys, db := newSim(t, Options{})
	putKey(t, db, 1, "a", "1")
	closeDB(t, db)
	fErr := errors.New("f failed")
	var kept *Maintenance
	if err := Maintain(ctx, fsys, Options{}, func(m *Maintenance) error {
		kept = m
		return fErr
	}); !errors.Is(err, fErr) || err.Error() != "f failed" {
		t.Errorf("Maintain: %v", err)
	}
	if err := kept.View(ctx, func(*Tx) error { return nil }); !errors.Is(err, errClosed) {
		t.Errorf("View after Maintain: %v", err)
	}
	if err := kept.Compact(ctx, Rewrite{}); !errors.Is(err, errClosed) {
		t.Errorf("Compact after Maintain: %v", err)
	}
	if _, err := kept.CollectGarbage(ctx); !errors.Is(err, errClosed) {
		t.Errorf("CollectGarbage after Maintain: %v", err)
	}
	func() {
		defer func() {
			if r := recover(); r != "f panics" {
				t.Errorf("recover: %v", r)
			}
		}()
		_ = Maintain(ctx, fsys, Options{}, func(*Maintenance) error { panic("f panics") })
	}()
	if err := Maintain(ctx, fsys, Options{Placement: 9}, nil); err == nil || err.Error() != "unknown placement 9" {
		t.Errorf("Maintain with bad options: %v", err)
	}
	// A failed Open releases the locks of maintenance.
	refused := errors.New("refused")
	if err := Maintain(ctx, fsys, Options{Accept: func(*Tx) error { return refused }}, nil); !errors.Is(err, refused) {
		t.Errorf("Maintain with Accept: %v", err)
	}
	for _, name := range []string{"lock/entry", "lock/readers", "lock/writer"} {
		lockFile(t, fsys, name, vfs.Exclusive)()
	}
	// Compact and CollectGarbage wait for each other until their contexts end.
	maintain(t, fsys, Options{}, func(m *Maintenance) error {
		m.db.sem <- struct{}{}
		short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		if err := m.Compact(short, Rewrite{}); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Compact that waits: %v", err)
		}
		if _, err := m.CollectGarbage(short); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("CollectGarbage that waits: %v", err)
		}
		<-m.db.sem
		return nil
	})
}

// TestMaintainCompactAndGC removes what a failed commit, crashes and a compaction leave, and keeps what the state has.
func TestMaintainCompactAndGC(t *testing.T) {
	ctx := context.Background()
	withBaseBytes(t, 200)
	hookErr := errors.New("hook failed")
	var failObjects atomic.Bool
	o := Options{Placement: Hybrid, Threshold: 100, SegmentBytes: 400, Hook: func(p string) error {
		if p == "update:objects" && failObjects.Load() {
			return hookErr
		}
		return nil
	}}
	fsys, db := newSim(t, o)
	for i := range 6 {
		change(t, db, i)
	}
	dropped, orphan := objData(100, 300), objData(101, 300)
	putObjects(t, db, dropped)
	failObjects.Store(true)
	if err := db.Update(ctx, func(tx *Tx) error {
		_, err := tx.PutObject(orphan)
		return err
	}); !errors.Is(err, hookErr) {
		t.Fatalf("Update: %v", err)
	}
	failObjects.Store(false)
	orphanFile := objName(sha256.Sum256(orphan))
	if !exists(t, fsys, orphanFile) {
		t.Fatal("the failed commit left no file")
	}
	closeDB(t, db)
	db = reopenNoHook(t, fsys, o)
	before := stateOf(t, db)
	closeDB(t, db)
	// A crash during a compaction leaves an incomplete newer generation, and crashes leave temporary files.
	writeSeg(t, fsys, 2, 1, 6, appendBase(nil, 6, 0, []op{{code: opPut, table: 9, key: "x"}}))
	objTemp := path.Dir(objName(sha256.Sum256(dropped))) + "/.tmp-0123456789abcdef"
	setFile(t, fsys, objTemp, []byte("half an object"))
	setFile(t, fsys, ".tmp-checkpoint-0123456789abcdef", []byte("half a checkpoint"))
	// Names that the engine does not make stay: in a directory that is not one of objects, or an object in another
	// directory than its own.
	dir := path.Dir(objName(sha256.Sum256(dropped)))
	other := "ff"
	if dir == "objects/ff" {
		other = "fe"
	}
	unknown := []string{"objects/notes", "objects/zz/" + strings.Repeat("0", 64), "objects/zz/.tmp-0123456789abcdef",
		"notes", dir + "/notes", dir + "/" + other + strings.Repeat("0", 62), dir + "/" + path.Base(dir)}
	if err := errors.Join(fsys.Mkdir("objects/zz", 0o700), fsys.Mkdir(dir+"/.tmp-dir", 0o700), fsys.SyncDir("objects"),
		fsys.SyncDir(dir)); err != nil {
		t.Fatal(err)
	}
	for _, name := range unknown {
		setFile(t, fsys, name, []byte("x"))
	}
	unknown = append(unknown, dir+"/.tmp-dir")
	maintain(t, fsys, o, func(m *Maintenance) error {
		if res := gc(t, m); res != (GCResult{Segments: 1, Objects: 1, Temps: 2}) {
			t.Errorf("GC: %+v", res)
		}
		if res := gc(t, m); res != (GCResult{}) {
			t.Errorf("a second GC: %+v", res)
		}
		return nil
	})
	if exists(t, fsys, orphanFile) || exists(t, fsys, objTemp) || exists(t, fsys, ".tmp-checkpoint-0123456789abcdef") ||
		!slices.Equal(gensOf(t, fsys), []uint64{1}) {
		t.Errorf("after GC: %v", fileNames(fsys))
	}
	// A compaction whose checkpoint fails leaves the old generation; the file of the object that it drops stays.
	fsys.Inject(simfs.Fault{Match: ckpTemp(simfs.OpSync), Err: syscall.EIO, Times: 1})
	maintain(t, fsys, o, func(m *Maintenance) error {
		h := sha256.Sum256(dropped)
		err := m.Compact(ctx, Rewrite{Object: func(hash [32]byte) bool { return hash != h }})
		if !errors.Is(err, syscall.EIO) || !slices.Equal(gensOf(t, fsys), []uint64{1, 2}) {
			t.Errorf("Compact: %v, generations %v", err, gensOf(t, fsys))
		}
		if res := gc(t, m); res.Segments < 2 || res.Objects != 1 || res.Temps != 0 {
			t.Errorf("GC after the compaction: %+v", res)
		}
		return nil
	})
	if !slices.Equal(gensOf(t, fsys), []uint64{2}) || exists(t, fsys, objName(sha256.Sum256(dropped))) {
		t.Errorf("after the second GC: %v", fileNames(fsys))
	}
	for _, name := range unknown {
		if !exists(t, fsys, name) {
			t.Errorf("GC removed %s", name)
		}
	}
	// The removals are durable, and the store has everything else.
	crashed := fsys.Crash(loseAll)
	if got := fileNames(crashed); !slices.Equal(got, fileNames(fsys)) {
		t.Errorf("after a crash:\n%v\nwant:\n%v", got, fileNames(fsys))
	}
	for _, f := range []*simfs.FS{fsys, crashed} {
		db := reopenNoHook(t, f, o)
		if info := db.Recovery(); info.Generation != 2 {
			t.Errorf("Recovery: %+v", info)
		}
		if got := stateOf(t, db); got != strings.Replace(before, objectLine(dropped)+"\n", "", 1) {
			t.Errorf("state:\n%s\nwant:\n%s", got, before)
		}
	}
}

func reopenNoHook(t *testing.T, fsys vfs.FS, o Options) *DB {
	t.Helper()
	o.Hook = nil
	return reopen(t, fsys, o)
}

func objectLine(data []byte) string {
	return fmt.Sprintf("%x:%d", sha256.Sum256(data), len(data))
}

// fileNames returns the names of the files of fsys, as a reader sees them.
func fileNames(fsys *simfs.FS) []string {
	var names []string
	fsys.Walk(func(p string, _ []byte) { names = append(names, p) })
	return names
}

func TestCollectGarbage(t *testing.T) {
	ctx := context.Background()
	o := Options{Placement: Hybrid, Threshold: 100}
	setup := func(t *testing.T, hook func(string) error) (*simfs.FS, Options) {
		fsys, db := newSim(t, o)
		change(t, db, 1) // an object in a file
		change(t, db, 0) // an inline object
		closeDB(t, db)
		// A file of an object that the state has inline, which a commit that failed may leave.
		inline := objName(sha256.Sum256(objData(0, 50)))
		if err := fsys.Mkdir(path.Dir(inline), 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			t.Fatal(err)
		}
		if err := fsys.SyncDir("objects"); err != nil {
			t.Fatal(err)
		}
		setFile(t, fsys, inline, []byte(objectFile(objData(0, 50))))
		setFile(t, fsys, ".tmp-checkpoint-1", []byte("x"))
		setFile(t, fsys, ".tmp-checkpoint-2", []byte("x"))
		writeSeg(t, fsys, 2, 1, 2, appendBase(nil, 2, 0, nil))
		writeSeg(t, fsys, 2, 2, 2, appendBase(nil, 2, 1, nil))
		oh := o
		oh.Hook = hook
		return fsys, oh
	}
	t.Run("order", func(t *testing.T) {
		var points []string
		var fsys *simfs.FS
		fsys, oh := setup(t, func(p string) error {
			if p == "gc:remove" {
				points = append(points, fmt.Sprint(len(fileNames(fsys))))
			}
			return nil
		})
		ops := recordOps(fsys)
		maintain(t, fsys, oh, func(m *Maintenance) error {
			n := len(ops())
			if res := gc(t, m); res != (GCResult{Segments: 2, Objects: 1, Temps: 2}) {
				t.Errorf("GC: %+v", res)
			}
			var got []string
			for _, op := range ops()[n:] {
				if op.N > 0 {
					got = append(got, op.Kind.String()+" "+op.Path)
				}
			}
			inline := objName(sha256.Sum256(objData(0, 50)))
			want := []string{"remove " + segName(2, 2), "syncdir log", "remove " + segName(2, 1), "syncdir log",
				"remove " + inline, "syncdir " + path.Dir(inline), "remove .tmp-checkpoint-1", "remove .tmp-checkpoint-2",
				"syncdir ."}
			if !slices.Equal(got, want) {
				t.Errorf("operations:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			return nil
		})
		if len(points) != 5 {
			t.Errorf("hook points %v", points)
		}
	})
	t.Run("hook error", func(t *testing.T) {
		hookErr := errors.New("hook failed")
		calls := 0
		fsys, oh := setup(t, func(p string) error {
			if p == "gc:remove" {
				if calls++; calls == 3 {
					return hookErr
				}
			}
			return nil
		})
		maintain(t, fsys, oh, func(m *Maintenance) error {
			if res, err := m.CollectGarbage(ctx); !errors.Is(err, hookErr) || res != (GCResult{Segments: 2}) {
				t.Errorf("GC: %+v, %v", res, err)
			}
			if res := gc(t, m); res != (GCResult{Objects: 1, Temps: 2}) {
				t.Errorf("GC after the hook error: %+v", res)
			}
			return nil
		})
	})
	t.Run("remove error", func(t *testing.T) {
		fsys, oh := setup(t, nil)
		fsys.Inject(simfs.Fault{Match: on(simfs.OpRemove, ".tmp-checkpoint-1"), Err: syscall.EIO, Times: 1})
		maintain(t, fsys, oh, func(m *Maintenance) error {
			if _, err := m.CollectGarbage(ctx); !errors.Is(err, syscall.EIO) {
				t.Errorf("GC: %v", err)
			}
			if res := gc(t, m); res != (GCResult{Temps: 2}) {
				t.Errorf("GC after the error: %+v", res)
			}
			return nil
		})
	})
	t.Run("canceled", func(t *testing.T) {
		fsys, oh := setup(t, nil)
		maintain(t, fsys, oh, func(m *Maintenance) error {
			c, cancel := context.WithCancel(ctx)
			cancel()
			if res, err := m.CollectGarbage(c); !errors.Is(err, context.Canceled) || res != (GCResult{}) {
				t.Errorf("GC: %+v, %v", res, err)
			}
			cc := &countdownContext{Context: ctx, left: 2}
			if res, err := m.CollectGarbage(cc); !errors.Is(err, context.Canceled) || res != (GCResult{Segments: 1}) {
				t.Errorf("GC that is canceled: %+v, %v", res, err)
			}
			return nil
		})
	})
	for _, dir := range []string{"log", "objects/", "."} {
		t.Run("sync of "+dir+" fails", func(t *testing.T) {
			fsys, oh := setup(t, nil)
			match := on(simfs.OpSyncDir, dir)
			if dir == "objects/" {
				match = objDirOp(simfs.OpSyncDir)
			}
			var ops func() []simfs.Op
			err := Maintain(ctx, fsys, oh, func(m *Maintenance) error {
				fsys.Inject(simfs.Fault{Match: match, Err: syscall.EIO, Times: 1})
				ops = recordOps(fsys)
				if _, err := m.CollectGarbage(ctx); !errors.Is(err, ErrFailed) || !errors.Is(err, syscall.EIO) {
					t.Errorf("GC: %v", err)
				}
				if _, err := m.CollectGarbage(ctx); !errors.Is(err, ErrFailed) {
					t.Errorf("a second GC: %v", err)
				}
				if err := m.Compact(ctx, Rewrite{}); !errors.Is(err, ErrFailed) {
					t.Errorf("Compact: %v", err)
				}
				return nil
			})
			if err != nil {
				t.Errorf("Maintain: %v", err)
			}
			for _, op := range ops() {
				if op.N > 0 && op.Kind != simfs.OpRemove && op.Kind != simfs.OpSyncDir {
					t.Errorf("after the failed sync: %v", op) // Close writes nothing
				}
			}
		})
	}
}

// TestCrashDuringGC crashes after each operation of a GC that removes two generations: Open never refuses, and the
// state is that of the store before.
func TestCrashDuringGC(t *testing.T) {
	o := Options{SegmentBytes: 200}
	base, db := newSim(t, o)
	for i := range 4 {
		putKey(t, db, 1, fmt.Sprintf("k%d", i), "a value of some bytes")
	}
	if err := compactDB(db, Rewrite{}); err != nil {
		t.Fatal(err)
	}
	putKey(t, db, 1, "after", "x")
	closeDB(t, db)
	// The old generation stays, as after a failed checkpoint, and a newer one is incomplete.
	want := dump(t, reopenNoHook(t, copyFS(base), o))
	writeSeg(t, base, 3, 1, 5, appendBase(nil, 5, 0, nil))
	writeSeg(t, base, 3, 2, 5, appendBase(nil, 5, 1, nil))
	writeSeg(t, base, 1, 1, 1, oldCommit(1))
	writeSeg(t, base, 1, 2, 2, oldCommit(2))
	run := func(fsys *simfs.FS) error {
		return Maintain(context.Background(), fsys, o, func(m *Maintenance) error {
			_, err := m.CollectGarbage(context.Background())
			return err
		})
	}
	clean := copyFS(base)
	start := clean.Counted()
	if err := run(clean); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gensOf(t, clean), []uint64{2}) {
		t.Fatalf("generations %v", gensOf(t, clean))
	}
	n, crashes := clean.Counted()-start, 0
	for k := 1; k <= n; k++ {
		fsys := copyFS(base)
		fsys.CrashAt(fsys.Counted() + k)
		_ = run(fsys)
		for _, m := range simfs.Models(fsys.Pending(), 16, nil) {
			crashed := fsys.Crash(m)
			db, err := Open(crashed, o)
			if err != nil {
				t.Fatalf("crash after %d, %s: %v", k, m.Name, err)
			}
			if got := dump(t, db); got != want || db.Recovery().Generation != 2 {
				t.Fatalf("crash after %d, %s: generation %d:\n%s", k, m.Name, db.Recovery().Generation, got)
			}
			closeDB(t, db)
			crashes++
		}
	}
	t.Logf("%d crash points, %d crashes", n, crashes)
}

func oldCommit(seq seqNum) []byte {
	return appendCommit(nil, seq, []op{{code: opPut, table: 1, key: "old", value: []byte("x")}})
}

// TestNoDeadlock opens writers, readers and maintenance on one store at once, again and again, with short waits: every
// call returns, with nil or with one of the errors of a store that another process has open. With two writers, the one
// that waits for the other holds lock/entry, which can keep maintenance out; with one, every kind gets its turn.
func TestNoDeadlock(t *testing.T) {
	dir := t.TempDir()
	db, err := Create(osFS(t, dir), Options{})
	if err != nil {
		t.Fatal(err)
	}
	putKey(t, db, 1, "n", "0")
	closeDB(t, db)
	done := make(chan [3]int64)
	go func() {
		churn(t, dir, 2, time.Second)
		done <- churn(t, dir, 1, time.Second)
	}()
	var ok [3]int64
	select {
	case ok = <-done:
	case <-time.After(20 * time.Second):
		buf := make([]byte, 1<<20)
		t.Fatalf("deadlock:\n%s", buf[:runtime.Stack(buf, true)])
	}
	for i, what := range []string{"writer", "reader", "maintenance"} {
		if ok[i] == 0 {
			t.Errorf("no %s opened with one writer", what)
		}
	}
}

// churn runs writers loops of writers, two of readers and one of maintenance on the store in dir for d, and returns
// how many of each have opened.
func churn(t *testing.T, dir string, writers int, d time.Duration) [3]int64 {
	ctx := context.Background()
	expected := func(err error) bool {
		return err == nil || errors.Is(err, context.DeadlineExceeded) || err.Error() == dir+": maintenance is running" ||
			err.Error() == dir+": another process holds the writer lock"
	}
	var wg sync.WaitGroup
	var ok [3]atomic.Int64
	stop := time.Now().Add(d)
	loop := func(kind int, f func(fsys vfs.FS) error) {
		wg.Go(func() {
			fsys, err := vfs.OpenRoot(dir)
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = fsys.Close() }()
			for time.Now().Before(stop) {
				err := f(fsys)
				if !expected(err) {
					t.Error(err)
					return
				}
				if err == nil {
					ok[kind].Add(1)
				}
				time.Sleep(time.Millisecond) // so that maintenance finds the locks free at times
			}
		})
	}
	for range writers {
		loop(0, func(fsys vfs.FS) error {
			db, err := Open(fsys, Options{WriterWait: 20 * time.Millisecond})
			if err != nil {
				return err
			}
			return errors.Join(db.Update(ctx, putF(1, "n", "1")), db.Close())
		})
	}
	for range 2 {
		loop(1, func(fsys vfs.FS) error {
			db, err := OpenReader(fsys, Options{})
			if err != nil {
				return err
			}
			return errors.Join(db.View(ctx, func(*Tx) error { return nil }), db.Close())
		})
	}
	loop(2, func(fsys vfs.FS) error {
		c, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		return Maintain(c, fsys, Options{}, func(m *Maintenance) error {
			_, err := m.CollectGarbage(ctx)
			return err
		})
	})
	wg.Wait()
	t.Logf("%d writers: writers %d, readers %d, maintenance %d", writers, ok[0].Load(), ok[1].Load(), ok[2].Load())
	return [3]int64{ok[0].Load(), ok[1].Load(), ok[2].Load()}
}
