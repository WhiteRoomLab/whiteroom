package logdb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

// checkCrashes checks the state that each crash of fsys leaves after reopening.
func checkCrashes(t *testing.T, fsys *simfs.FS, o Options, want map[string]string) {
	t.Helper()
	for _, m := range []simfs.Model{keepAll(fsys), loseAll} {
		db := reopen(t, fsys.Crash(m), o)
		if got := dump(t, db); got != want[m.Name] {
			t.Errorf("after the crash %s:\n%s\nwant:\n%s", m.Name, got, want[m.Name])
		}
		closeDB(t, db)
	}
}

func checkFailed(t *testing.T, db *DB, err, cause error) {
	t.Helper()
	if !errors.Is(err, ErrFailed) || !errors.Is(err, cause) || !strings.HasPrefix(err.Error(), ErrFailed.Error()+": ") {
		t.Fatalf("Update: %v, want ErrFailed and %v", err, cause)
	}
	called := false
	again := db.Update(context.Background(), func(tx *Tx) error {
		called = true
		return tx.Put(1, []byte("later"), nil)
	})
	if !errors.Is(again, ErrFailed) || again.Error() != err.Error() || called {
		t.Errorf("a later Update: %v, f called %v", again, called)
	}
}

func TestSyncFailure(t *testing.T) {
	for _, drop := range []bool{false, true} {
		t.Run(fmt.Sprintf("drop %v", drop), func(t *testing.T) {
			fsys, db := newSim(t, Options{})
			putKey(t, db, 1, "a", "1")
			fsys.Inject(simfs.Fault{Match: on(simfs.OpSync, seg1), Err: syscall.EIO, Drop: drop, Times: 1})
			err := db.Update(context.Background(), putF(1, "b", "2"))
			checkFailed(t, db, err, syscall.EIO)
			if got := dump(t, db); got != "1/a=1" {
				t.Errorf("a View after the failure: %s", got)
			}
			counted := fsys.Counted()
			closeDB(t, db)
			if n := fsys.Counted() - counted; n != 0 {
				t.Errorf("Close after the failure made %d operations", n)
			}
			// The outcome of the failed commit is unknown: after a crash it is there or not, never a part of it.
			withB := "1/a=1\n1/b=2"
			if drop {
				withB = "1/a=1"
			}
			checkCrashes(t, fsys, Options{}, map[string]string{"keep-all": withB, "lose-all": "1/a=1"})
		})
	}
}

func TestWriteErrorTruncates(t *testing.T) {
	for _, c := range []struct {
		name  string
		fault simfs.Fault
	}{
		{"EIO", simfs.Fault{Err: syscall.EIO}},
		{"ENOSPC", simfs.Fault{Err: syscall.ENOSPC}},
		{"ENOSPC after 13 bytes", simfs.Fault{Err: syscall.ENOSPC, Short: 13}},
		{"EIO after all but 1 byte", simfs.Fault{Err: syscall.EIO, Short: frameLen(17) - 1}},
	} {
		t.Run(c.name, func(t *testing.T) {
			fsys, db := newSim(t, Options{})
			putKey(t, db, 1, "a", "1")
			size := fileSize(t, fsys, seg1)
			f := c.fault
			f.Match, f.Times = on(simfs.OpWrite, seg1), 1
			fsys.Inject(f)
			err := db.Update(context.Background(), putF(1, "b", "2"))
			if !errors.Is(err, c.fault.Err) || errors.Is(err, ErrFailed) {
				t.Fatalf("Update: %v", err)
			}
			if got := fileSize(t, fsys, seg1); got != size {
				t.Errorf("the segment has %d bytes, want %d", got, size)
			}
			if p := pendingOn(fsys, seg1); len(p) != 0 {
				t.Errorf("the cut is not durable: %v", p)
			}
			putKey(t, db, 1, "c", "3")
			want := "1/a=1\n1/c=3"
			if got := dump(t, db); got != want {
				t.Errorf("dump:\n%s", got)
			}
			checkCrashes(t, fsys, Options{}, map[string]string{"keep-all": want, "lose-all": want})
			closeDB(t, db)
			db = reopen(t, fsys, Options{})
			if got := dump(t, db); got != want {
				t.Errorf("after reopening:\n%s", got)
			}
		})
	}
}

func TestTruncateFails(t *testing.T) {
	hookErr := errors.New("hook failed")
	for _, c := range []struct {
		name        string
		first, then simfs.Fault
		hook        bool // the first failure is the hook update:written instead
		keepAll     string
		cause       error
	}{
		{name: "truncate", first: simfs.Fault{Err: syscall.ENOSPC, Short: 5}, then: simfs.Fault{Match: on(simfs.OpTruncate, seg1), Err: syscall.EIO},
			keepAll: "1/a=1", cause: syscall.EIO},
		{name: "sync after the truncate", first: simfs.Fault{Err: syscall.ENOSPC, Short: 5},
			then: simfs.Fault{Match: on(simfs.OpSync, seg1), Err: syscall.EIO}, keepAll: "1/a=1", cause: syscall.EIO},
		{name: "after the hook", hook: true, then: simfs.Fault{Match: on(simfs.OpTruncate, seg1), Err: syscall.EIO},
			keepAll: "1/a=1\n1/b=2", cause: syscall.EIO},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := Options{}
			if c.hook {
				o.Hook = func(point string) error {
					if point == "update:written" {
						return hookErr
					}
					return nil
				}
			}
			fsys, db := newSim(t, Options{})
			putKey(t, db, 1, "a", "1")
			closeDB(t, db)
			db = reopen(t, fsys, o)
			if !c.hook {
				f := c.first
				f.Match, f.Times = on(simfs.OpWrite, seg1), 1
				fsys.Inject(f)
			}
			then := c.then
			then.Times = 1
			fsys.Inject(then)
			err := db.Update(context.Background(), putF(1, "b", "2"))
			checkFailed(t, db, err, c.cause)
			closeDB(t, db)
			// After a cut that failed, a crash can keep the record: the Update failed, so either is right.
			checkCrashes(t, fsys, Options{}, map[string]string{"keep-all": c.keepAll, "lose-all": "1/a=1"})
		})
	}
}

func TestHooks(t *testing.T) {
	hookErr := errors.New("hook failed")
	var points []string
	fail := ""
	o := Options{Hook: func(point string) error {
		points = append(points, point)
		if point == fail {
			return hookErr
		}
		return nil
	}}
	fsys, db := newSim(t, o)
	putKey(t, db, 1, "a", "1")
	if got := strings.Join(points, " "); got != "update:written update:synced update:published" {
		t.Errorf("hook points: %s", got)
	}
	size := fileSize(t, fsys, seg1)
	fail = "update:written"
	if err := db.Update(context.Background(), putF(1, "b", "2")); !errors.Is(err, hookErr) {
		t.Errorf("Update with the hook %s failing: %v", fail, err)
	}
	if fileSize(t, fsys, seg1) != size || len(pendingOn(fsys, seg1)) != 0 {
		t.Error("the record of the failed Update is not cut durably")
	}
	for _, p := range []string{"update:synced", "update:published"} {
		fail = p
		if err := db.Update(context.Background(), putF(1, p, "after the commit point")); err != nil {
			t.Errorf("Update with the hook %s failing: %v", p, err)
		}
	}
	want := "1/a=1\n1/update:published=after the commit point\n1/update:synced=after the commit point"
	if got := dump(t, db); got != want {
		t.Errorf("dump:\n%s", got)
	}
	checkCrashes(t, fsys, Options{}, map[string]string{"keep-all": want, "lose-all": want})
}

func TestRolloverFaults(t *testing.T) {
	o := Options{SegmentBytes: 100} // a segment of one record of 32 bytes
	for _, c := range []struct {
		name   string
		fault  simfs.Fault
		failed bool
	}{
		{"create", simfs.Fault{Match: on(simfs.OpCreate, seg2), Err: syscall.ENOSPC}, false},
		{"header write", simfs.Fault{Match: on(simfs.OpWrite, seg2), Err: syscall.ENOSPC}, false},
		{"short header write", simfs.Fault{Match: on(simfs.OpWrite, seg2), Err: syscall.ENOSPC, Short: 20}, false},
		{"header sync", simfs.Fault{Match: on(simfs.OpSync, seg2), Err: syscall.EIO}, true},
		{"dropped header sync", simfs.Fault{Match: on(simfs.OpSync, seg2), Err: syscall.EIO, Drop: true}, true},
		{"log sync", simfs.Fault{Match: on(simfs.OpSyncDir, "log"), Err: syscall.EIO}, true},
		{"record write", simfs.Fault{Match: func(op simfs.Op) bool {
			return on(simfs.OpWrite, seg2)(op) && op.Off == segHeaderSize
		}, Err: syscall.EIO, Short: 7}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			fsys, db := newSim(t, o)
			putKey(t, db, 1, "a", "1")
			f := c.fault
			f.Times = 1
			fsys.Inject(f)
			err := db.Update(context.Background(), putF(1, "b", "2"))
			want := "1/a=1"
			if c.failed {
				checkFailed(t, db, err, c.fault.Err)
			} else {
				if !errors.Is(err, c.fault.Err) || errors.Is(err, ErrFailed) {
					t.Fatalf("Update: %v", err)
				}
				switch {
				case c.name == "record write":
					// the new segment is complete: it stays, cut back to its header, and the next record goes in it
					if size := fileSize(t, fsys, seg2); size != segHeaderSize || len(pendingOn(fsys, seg2)) != 0 {
						t.Errorf("%s has %d bytes, %v", seg2, size, pendingOn(fsys, seg2))
					}
				case exists(t, fsys, seg2):
					t.Errorf("%s is left", seg2)
				}
				putKey(t, db, 1, "b", "2")
				want = "1/a=1\n1/b=2"
				if got := dump(t, db); got != want || !exists(t, fsys, seg2) {
					t.Errorf("after the next Update: %s", got)
				}
			}
			closeDB(t, db)
			checkCrashes(t, fsys, o, map[string]string{"keep-all": want, "lose-all": want})
		})
	}
}

// TestCrashDuringUpdate crashes after each operation of an Update, or of a Close, and checks every crash that simfs
// can make of it: Open never refuses, an acknowledged Update is there, and one that failed is there or not at all.
func TestCrashDuringUpdate(t *testing.T) {
	update := func(db *DB) error {
		return db.Update(context.Background(), func(tx *Tx) error {
			return errors.Join(tx.Put(1, []byte("b"), []byte("2")), tx.Delete(1, []byte("a")))
		})
	}
	for _, c := range []struct {
		name string
		o    Options
		step func(db *DB) error
	}{
		{"update", Options{}, update},
		{"update with a rollover", Options{SegmentBytes: 100}, update},
		{"close", Options{}, func(db *DB) error { return db.Close() }},
	} {
		t.Run(c.name, func(t *testing.T) {
			setup := func() (*simfs.FS, *DB) {
				fsys, db := newSim(t, c.o)
				putKey(t, db, 1, "a", "1")
				return fsys, db
			}
			state := func(fsys *simfs.FS) string {
				db, err := Open(fsys, c.o)
				if err != nil {
					t.Fatalf("Open: %v", err)
				}
				defer closeDB(t, db)
				return dump(t, db)
			}
			fsys, db := setup()
			before, start := dump(t, db), fsys.Counted()
			if err := c.step(db); err != nil {
				t.Fatal(err)
			}
			ops := fsys.Counted() - start
			after := state(copyFS(fsys))
			crashes := 0
			for k := range ops + 1 {
				fsys, db := setup()
				fsys.CrashAt(fsys.Counted() + k)
				err := c.step(db)
				for _, m := range simfs.Models(fsys.Pending(), 16, nil) {
					got := state(fsys.Crash(m))
					if got != after && (err == nil || got != before) {
						t.Errorf("a crash after %d of %d operations (the step returned %v), %s:\n%s", k, ops, err, m.Name, got)
					}
					crashes++
				}
			}
			t.Logf("%d crash points, %d crashes", ops+1, crashes)
		})
	}
}
