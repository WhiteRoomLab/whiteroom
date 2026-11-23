package dbserver

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

// contents returns the mode and the bytes of each file in dir, by its path in dir.
func contents(t testing.TB, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		text := ""
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			text = string(data)
		}
		m[filepath.ToSlash(rel)] = fmt.Sprintf("%v %q", info.Mode(), text)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMaintain(t *testing.T) {
	f := newDirFixture(t, nil)
	w := f.connect(dbwire.RoleWriter)
	object := strings.Repeat("o", 1000)
	seq := w.mustSnapshot()
	if _, err := w.putObject(object); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.commit(seq, put(2, "a", "1"), event("first")); err != nil {
		t.Fatal(err)
	}
	original := []byte("GIF89a")
	oh := mustPut(t, w, original)
	w.mustCommit(put(2, "b", "2"), del(2, "a"), event("second"))
	before := f.dump()
	if err := f.srv.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, filepath.FromSlash(objectPath(object)))); err != nil {
		t.Fatalf("the object has no file: %v", err)
	}
	segments := names(t, filepath.Join(f.dir, "logdb", "log"))

	// A file of an object that no commit names, and a temporary file of one: the garbage that CollectGarbage removes.
	orphan := strings.Repeat("ab", 32)
	mkdir(t, filepath.Join(f.dir, "logdb", "objects", "ab"))
	for _, name := range []string{orphan, ".tmp-1234"} {
		if err := os.WriteFile(filepath.Join(f.dir, "logdb", "objects", "ab", name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var res logdb.GCResult
	err := Maintain(t.Context(), f.dir, f.opts, func(m *logdb.Maintenance) error {
		if err := m.Compact(t.Context(), logdb.Rewrite{}); err != nil {
			return err
		}
		var err error
		res, err = m.CollectGarbage(t.Context())
		return err
	})
	if err != nil {
		t.Fatalf("Maintain() = %v", err)
	}
	if want := (logdb.GCResult{Objects: 1, Temps: 1}); res != want {
		t.Errorf("CollectGarbage() = %+v, want %+v", res, want)
	}
	if got := names(t, filepath.Join(f.dir, "logdb", "log")); len(got) != 1 || reflect.DeepEqual(got, segments) {
		t.Errorf("the log has the segments %q after the compaction, was %q: want one of a new generation", got, segments)
	}
	if got := names(t, filepath.Join(f.dir, "logdb", "objects", "ab")); len(got) != 0 {
		t.Errorf("objects/ab has %q after the collection, want nothing", got)
	}

	// The store is what it was, with its object file and its original.
	f.open()
	if after := f.dump(); !reflect.DeepEqual(before, after) {
		t.Errorf("the store after Maintain:\n %+v\nwas\n %+v", after, before)
	}
	r := f.connect(dbwire.RoleReader)
	r.mustSnapshot()
	if found, data, err := r.object(hashOf(object)); err != nil || !found || string(data) != object {
		t.Errorf("the object after Maintain: %v, %v", found, err)
	}
	if got, err := r.original(oh); err != nil || string(got) != string(original) {
		t.Errorf("the original after Maintain: %q, %v", got, err)
	}
}

func TestMaintainInUse(t *testing.T) {
	f := newDirFixture(t, nil)
	w := f.connect(dbwire.RoleWriter)
	w.mustCommit(put(2, "a", "1"))
	before := contents(t, f.dir)
	state := f.dump()

	called := false
	start := time.Now()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	err := Maintain(ctx, f.dir, f.opts, func(*logdb.Maintenance) error {
		called = true
		return nil
	})
	if elapsed := time.Since(start); elapsed < lockWait-100*time.Millisecond || elapsed > lockWait+3*time.Second {
		t.Errorf("Maintain() of a directory in use took %v, want about %v", elapsed, lockWait)
	}
	if want := f.dir + ": the data directory is in use"; !errors.Is(err, ErrInUse) || err.Error() != want {
		t.Errorf("Maintain() of a directory in use = %v, want %q, wrapping ErrInUse", err, want)
	}
	if called {
		t.Error("Maintain() called f for a directory in use")
	}
	if got := contents(t, f.dir); !maps.Equal(got, before) {
		t.Errorf("Maintain() of a directory in use changed the files:\n%v\nwas\n%v", got, before)
	}
	// The daemon goes on.
	f.unchanged(state)
	w.mustCommit(put(2, "b", "2"))

	// A caller that ends sooner, by its cancellation or by its deadline, is not told that the directory is in use.
	canceled, cancelAfter := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancelAfter)
	timed, stop := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer stop()
	for _, tt := range []struct {
		name string
		ctx  context.Context
		want error
	}{{"canceled", canceled, context.Canceled}, {"timed out", timed, context.DeadlineExceeded}} {
		start = time.Now()
		err = Maintain(tt.ctx, f.dir, f.opts, func(*logdb.Maintenance) error { return nil })
		if !errors.Is(err, tt.want) || errors.Is(err, ErrInUse) {
			t.Errorf("Maintain() with a context that is %s = %v, want %v and not ErrInUse", tt.name, err, tt.want)
		}
		if elapsed := time.Since(start); elapsed > lockWait/2 {
			t.Errorf("Maintain() with a context that is %s after 100 ms took %v", tt.name, elapsed)
		}
	}

	if err := f.srv.Close(); err != nil {
		t.Fatal(err)
	}
	called = false
	if err := Maintain(t.Context(), f.dir, f.opts, func(*logdb.Maintenance) error {
		called = true
		return nil
	}); err != nil || !called {
		t.Errorf("Maintain() after the daemon closed = %v, f called: %v", err, called)
	}
	// Maintain has let go of the directory.
	f.open()
	w = f.connect(dbwire.RoleWriter)
	w.mustCommit(put(2, "c", "3"))
}

// TestMaintainErrors checks that the errors of f are returned as they are, also one of the context, which is not the wait
// for the other processes; and that a data directory that OpenDir refuses is refused with the same words, with nothing
// changed.
func TestMaintainErrors(t *testing.T) {
	f := newDirFixture(t, nil)
	f.connect(dbwire.RoleWriter).mustCommit(put(2, "a", "1"))
	if err := f.srv.Close(); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	if err := Maintain(t.Context(), f.dir, f.opts, func(*logdb.Maintenance) error { return boom }); err == nil || err.Error() != "boom" || !errors.Is(err, boom) {
		t.Errorf("Maintain() with an error of f = %v, want it as it is", err)
	}
	if err := Maintain(t.Context(), f.dir, f.opts, func(*logdb.Maintenance) error {
		return fmt.Errorf("the compaction: %w", context.Canceled)
	}); errors.Is(err, ErrInUse) || !errors.Is(err, context.Canceled) {
		t.Errorf("Maintain() with an error of f that wraps context.Canceled = %v, want it and not ErrInUse", err)
	}

	for _, name := range []string{"files", "logdb"} {
		dir := filepath.Join(t.TempDir(), "data")
		if err := CreateDir(dir, f.opts); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
		before := contents(t, dir)
		_, openErr := OpenDir(dir, f.opts)
		called := false
		err := Maintain(t.Context(), dir, f.opts, func(*logdb.Maintenance) error {
			called = true
			return nil
		})
		if want := filepath.Join(dir, name) + ": missing directory"; openErr == nil || err == nil || err.Error() != openErr.Error() || err.Error() != want {
			t.Errorf("Maintain() without %s = %v, want %q, as OpenDir says (%v)", name, err, want, openErr)
		}
		if called || !maps.Equal(contents(t, dir), before) {
			t.Errorf("Maintain() without %s called f (%v) or changed the directory", name, called)
		}
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if err := Maintain(t.Context(), missing, f.opts, func(*logdb.Maintenance) error { return nil }); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Maintain() of a missing directory = %v, want an error that wraps fs.ErrNotExist", err)
	}
	if _, err := os.Lstat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Lstat() of the missing directory after Maintain() = %v", err)
	}
}

// TestMaintainSchema checks that a store of a table schema newer than dbwire.TableVersion is refused as OpenDir refuses it,
// without calling f, and without changing a file.
func TestMaintainSchema(t *testing.T) {
	f := newDirFixture(t, nil)
	f.connect(dbwire.RoleWriter).mustCommit(put(2, "a", "1"))
	if err := f.db().Update(t.Context(), func(tx *logdb.Tx) error {
		return tx.Put(dbwire.TableMeta, []byte(dbwire.MetaSchema), binary.AppendUvarint(nil, dbwire.TableVersion+1))
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.srv.Close(); err != nil {
		t.Fatal(err)
	}
	before := contents(t, f.dir)
	_, openErr := OpenDir(f.dir, f.opts)
	called := false
	err := Maintain(t.Context(), f.dir, f.opts, func(*logdb.Maintenance) error {
		called = true
		return nil
	})
	want := fmt.Sprintf("%s: the store schema is of version %d, newer than this program knows (%d)",
		filepath.Join(f.dir, "logdb"), dbwire.TableVersion+1, dbwire.TableVersion)
	if err == nil || err.Error() != want || openErr == nil || openErr.Error() != want {
		t.Errorf("Maintain() of a newer schema = %v, OpenDir() = %v, want both %q", err, openErr, want)
	}
	if called {
		t.Error("Maintain() called f for a newer schema")
	}
	if got := contents(t, f.dir); !maps.Equal(got, before) {
		t.Errorf("Maintain() of a newer schema changed the files:\n%v\nwas\n%v", got, before)
	}
}

// committedFixture is a fixture whose store has a commit (the row "a"), and whose server is closed.
func committedFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t, nil)
	f.connect(dbwire.RoleWriter).mustCommit(schemaOp(), put(2, "a", "1"))
	if err := f.srv.Close(); err != nil {
		t.Fatal(err)
	}
	return f
}

// worldFiles returns every file of fsys, with what it holds.
func worldFiles(fsys *simfs.FS) map[string]string {
	m := map[string]string{}
	fsys.Walk(func(p string, data []byte) { m[p] = string(data) })
	return m
}

// maintainCalled runs maintain on the world of f, and reports whether it called its function, which does nothing.
func maintainCalled(t *testing.T, f *fixture, o Options) (called bool, err error) {
	t.Helper()
	err = maintain(t.Context(), f.fs, "data", o, func(*logdb.Maintenance) error {
		called = true
		return nil
	})
	return called, err
}

// TestMaintainMark checks the rule of the mark of a failed sync (concept section 3.5) for Maintain: it refuses a directory
// whose mark is of this boot before the engine changes a file, removes the mark of an earlier boot, and leaves the mark
// for a failure of the maintenance that has the sentence of the engine, before the engine lets go of its locks.
func TestMaintainMark(t *testing.T) {
	const at, cause = "2026-11-15T09:29:00Z", "sync data/logdb/log/x.log: input/output error"
	valid := markWith(testBoot, "time: "+at+"\n", "cause: "+cause+"\n")

	t.Run("refuses", func(t *testing.T) {
		for _, tc := range []struct {
			name, mark, want string
			notRestarted     bool
		}{
			{"a mark of this boot", valid, "data: a sync failed at " + at + " (" + cause + ")" + refusedTail, true},
			{"a mark that cannot be read", "", unreadable, false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := committedFixture(t)
				putMark(t, f.fs, tc.mark)
				counted, before := f.fs.Counted(), worldFiles(f.fs)
				called, err := maintainCalled(t, f, f.opts)
				if err == nil || err.Error() != tc.want || errors.Is(err, ErrNotRestarted) != tc.notRestarted {
					t.Errorf("maintain = %v, want %q, wrapping ErrNotRestarted: %v", err, tc.want, tc.notRestarted)
				}
				if called {
					t.Error("maintain called f")
				}
				if n := f.fs.Counted(); n != counted {
					t.Errorf("the refusal made %d operations, want none", n-counted)
				}
				if after := worldFiles(f.fs); !maps.Equal(before, after) {
					t.Errorf("the refusal changed the files: before %v, after %v", slicesOf(before), slicesOf(after))
				}
				if lines := f.logs.all(); len(lines) != 0 {
					t.Errorf("log lines %q, want none", lines)
				}

				// It took no lock: a maintenance of another boot enters at once, and removes the mark of the first.
				o := f.opts
				o.Boot = machineWith(anotherBoot)
				if !tc.notRestarted {
					if _, err := maintainCalled(t, f, o); err == nil || err.Error() != tc.want {
						t.Errorf("maintain in another boot = %v, want the refusal as it was", err)
					}
					return
				}
				start := time.Now()
				if called, err := maintainCalled(t, f, o); err != nil || !called {
					t.Fatalf("maintain in another boot = %v, f called: %v", err, called)
				}
				if took := time.Since(start); took > lockWait/2 {
					t.Errorf("maintain in another boot took %v, want it at once: the refusal has left a lock", took)
				}
				if _, ok := worldFile(f.fs, "stopped"); ok {
					t.Error("the mark is still there after the maintenance of another boot")
				}
			})
		}
	})

	t.Run("an earlier boot", func(t *testing.T) {
		f := committedFixture(t)
		putMark(t, f.fs, valid)
		o := f.opts
		o.Boot = machineWith(anotherBoot)
		sawMark := true
		called := false
		if err := maintain(t.Context(), f.fs, "data", o, func(*logdb.Maintenance) error {
			called = true
			_, sawMark = worldFile(f.fs, "stopped")
			return nil
		}); err != nil || !called {
			t.Fatalf("maintain = %v, f called: %v", err, called)
		}
		if lines := f.logs.all(); len(lines) != 1 || lines[0] != removalLine(at, cause) {
			t.Errorf("log lines %q, want %q", lines, removalLine(at, cause))
		}
		if _, ok := worldFile(f.fs, "stopped"); ok {
			t.Error("the mark of an earlier boot is still there")
		}
		if sawMark {
			t.Error("f ran before the mark of an earlier boot was removed")
		}
	})

	// A mark that appears after the first look, while the engine opens: nothing is written, not the checkpoint of the state
	// that the engine has read from a log that the page cache may have had. The mark may be one that the stopping daemon has
	// made and not yet written: it is empty, and refuses as well.
	for _, tc := range []struct {
		name, mark, want string
		notRestarted     bool
	}{
		{"a mark after the first look", valid, "data: a sync failed at " + at + " (" + cause + ")" + refusedTail, true},
		{"an empty mark after the first look", "", unreadable, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, nil)
			f.connect(dbwire.RoleWriter).mustCommit(schemaOp(), put(2, "a", "1"))
			// A copy before the server closes: it has the commit in the log, and no checkpoint of it.
			world := f.fs.Crash(keepAll(f.fs))
			snapshot := func() map[string]string {
				files := filesOf(world, "logdb")
				delete(files, "logdb/head")
				return files
			}
			before := snapshot()
			var written string
			o := f.opts
			o.Hook = func(point string) error {
				if point == "maintain:checked" {
					written = tc.mark
					putMark(t, world, written)
				}
				return nil
			}
			called := false
			err := maintain(t.Context(), world, "data", o, func(*logdb.Maintenance) error {
				called = true
				return nil
			})
			if err == nil || err.Error() != tc.want || errors.Is(err, ErrNotRestarted) != tc.notRestarted {
				t.Fatalf("maintain = %v, want %q, wrapping ErrNotRestarted: %v", err, tc.want, tc.notRestarted)
			}
			if called {
				t.Error("maintain called f")
			}
			if after := snapshot(); !maps.Equal(before, after) {
				t.Errorf("the refused maintain changed logdb/: before %v, after %v", slicesOf(before), slicesOf(after))
			}
			for p := range filesOf(world, "logdb") {
				if strings.HasPrefix(path.Base(p), ".tmp-") {
					t.Errorf("the refused maintain left %s", p)
				}
			}
			if got, _ := worldFile(world, "stopped"); got != written {
				t.Errorf("the mark is %q, want it as it was written", got)
			}
			// The engine has let go of its locks.
			if srv, err := Open(world, "data", f.opts); err == nil {
				_ = srv.Close()
				t.Error("Open in the same boot succeeded after the refusal")
			} else if err.Error() != tc.want || errors.Is(err, ErrNotRestarted) != tc.notRestarted {
				t.Errorf("Open in the same boot = %v, want %q, wrapping ErrNotRestarted: %v", err, tc.want, tc.notRestarted)
			}
		})
	}

	failures := []struct {
		name   string
		inside bool // the failure is f's: the mark is left while the engine still has its locks
		// arrange runs before maintain; run is f.
		arrange func(t *testing.T, f *fixture)
		run     func(t *testing.T, f *fixture, m *logdb.Maintenance) error
	}{
		{name: "a compaction whose sync fails", inside: true,
			run: func(t *testing.T, f *fixture, m *logdb.Maintenance) error {
				if _, ok := filesOf(f.fs, "logdb/log")["logdb/log/0000000000000001-0000000000000001.log"]; !ok {
					t.Fatalf("the world has the segments %v, want one of generation 1", slicesOf(filesOf(f.fs, "logdb/log")))
				}
				f.fs.Inject(simfs.Fault{Match: func(op simfs.Op) bool {
					return op.Kind == simfs.OpSync && strings.HasPrefix(op.Path, "logdb/log/0000000000000002-")
				}, Err: syscall.EIO, Drop: true, Times: 1})
				return m.Compact(t.Context(), logdb.Rewrite{})
			}},
		{name: "a collection whose sync of logdb/ fails", inside: true,
			arrange: func(t *testing.T, f *fixture) {
				const temp = "logdb/.tmp-checkpoint-0123456789abcdef"
				file, err := f.fs.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				if err := errors.Join(file.Sync(), file.Close(), f.fs.SyncDir("logdb")); err != nil {
					t.Fatal(err)
				}
				// The first sync of logdb/ after the removal of the file.
				removed := false
				f.fs.Inject(simfs.Fault{Match: func(op simfs.Op) bool {
					if op.Kind == simfs.OpRemove && op.Path == temp {
						removed = true
					}
					return removed && op.Kind == simfs.OpSyncDir && op.Path == "logdb"
				}, Err: syscall.EIO, Drop: true, Times: 1})
			},
			run: func(t *testing.T, f *fixture, m *logdb.Maintenance) error {
				res, err := m.CollectGarbage(t.Context())
				if res.Temps != 1 {
					t.Errorf("the collection removed %d temporary files, want the one that was made", res.Temps)
				}
				return err
			}},
		{name: "an f that fails with the sentence of the engine", inside: true,
			run: func(t *testing.T, f *fixture, m *logdb.Maintenance) error {
				return fmt.Errorf("%w: %w", logdb.ErrFailed, &fs.PathError{Op: "sync", Path: "data/logdb/log/x.log", Err: syscall.EIO})
			}},
		{name: "the sync of the open of the engine fails",
			arrange: func(t *testing.T, f *fixture) {
				f.fs.Inject(simfs.Fault{Match: func(op simfs.Op) bool {
					return op.Kind == simfs.OpSync && strings.HasPrefix(op.Path, "logdb/log/")
				}, Err: syscall.EIO, Drop: true, Times: 1})
			},
			run: func(t *testing.T, f *fixture, m *logdb.Maintenance) error {
				t.Error("f was called although the engine did not open")
				return nil
			}},
	}
	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			f := committedFixture(t)
			if tc.arrange != nil {
				tc.arrange(t, f)
			}
			var creates atomic.Int32
			f.fs.Inject(simfs.Fault{Match: func(op simfs.Op) bool {
				if op.Kind == simfs.OpCreate && op.Path == "stopped" {
					creates.Add(1)
				}
				return false
			}, Err: syscall.EIO})
			var markedCalls int
			var openInside error
			o := f.opts
			o.Hook = func(point string) error {
				if point != "maintain:marked" {
					return nil
				}
				markedCalls++
				// The engine has the locks, as it does until maintain returns: the mark has to refuse this.
				if srv, err := Open(f.fs, "data", f.opts); err != nil {
					openInside = err
				} else {
					_ = srv.Close()
				}
				return nil
			}
			err := maintain(t.Context(), f.fs, "data", o, func(m *logdb.Maintenance) error { return tc.run(t, f, m) })
			if !errors.Is(err, logdb.ErrFailed) {
				t.Fatalf("maintain = %v, want an error that wraps logdb.ErrFailed", err)
			}
			if n := creates.Load(); n != 1 {
				t.Errorf("the mark was made %d times, want once", n)
			}
			if tc.inside {
				if markedCalls != 1 {
					t.Fatalf("the hook maintain:marked was called %d times, want once", markedCalls)
				}
				if !errors.Is(openInside, ErrNotRestarted) || strings.Contains(openInside.Error(), "maintenance is running") {
					t.Errorf("Open while the engine has its locks = %v, want the refusal of the mark", openInside)
				}
			} else if markedCalls != 0 {
				t.Errorf("the hook maintain:marked was called %d times, want none: the engine had not opened", markedCalls)
			}

			mark, ok := worldFile(f.fs, "stopped")
			if !ok {
				t.Fatal("no mark after the failure")
			}
			at, cause := markLine(mark, "time"), markLine(mark, "cause")
			if markLine(mark, "boot") != testBoot || !strings.HasPrefix(mark, markSentence+"\n") ||
				!strings.Contains(cause, "input/output error") || len(strings.Split(mark, "\n")) != 5 {
				t.Errorf("the mark is %q, want the sentence, the boot %s, the time and a cause with the error", mark, testBoot)
			}
			if _, err := time.Parse(time.RFC3339, at); err != nil {
				t.Errorf("the time of the mark is %q (%v)", at, err)
			}
			if lines := f.logs.all(); len(lines) != 0 {
				t.Errorf("log lines %q, want none", lines)
			}

			// No process of this boot opens the directory again.
			if _, err := openClosing(t, f, f.opts); !errors.Is(err, ErrNotRestarted) {
				t.Errorf("Open in the same boot = %v, want ErrNotRestarted", err)
			}
			if called, err := maintainCalled(t, f, f.opts); !errors.Is(err, ErrNotRestarted) || called {
				t.Errorf("maintain in the same boot = %v, f called: %v, want ErrNotRestarted", err, called)
			}
			// After a restart of the machine, whatever the crash has kept, it opens, and removes the mark.
			for _, m := range []simfs.Model{keepAll(f.fs), loseAll} {
				t.Run(m.Name, func(t *testing.T) {
					g := f.restart(m, anotherBoot)
					g.open()
					if lines := g.logs.all(); len(lines) != 1 || lines[0] != removalLine(at, cause) {
						t.Errorf("log lines %q, want the removal of the mark", lines)
					}
					if _, ok := worldFile(g.fs, "stopped"); ok {
						t.Error("the mark is still there after the restart")
					}
					r := g.connect(dbwire.RoleReader)
					r.mustSnapshot()
					if found, v := r.mustGet(2, "a"); !found || v != "1" {
						t.Errorf("the row after the restart: %v %q", found, v)
					}
				})
			}
		})
	}

	// An error that is not the sentence of the engine leaves no mark: not that of f, and not the wait for another process.
	t.Run("no mark", func(t *testing.T) {
		boom := errors.New("boom")
		for _, tc := range []struct {
			name string
			ctx  func(t *testing.T) context.Context
			run  func(m *logdb.Maintenance) error
			want error
		}{
			{"an error of f", nil, func(*logdb.Maintenance) error { return boom }, boom},
			{"an error of f that wraps a cancellation", nil, func(*logdb.Maintenance) error {
				return fmt.Errorf("the compaction: %w", context.Canceled)
			}, context.Canceled},
			{"a context that is canceled", func(t *testing.T) context.Context {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				return ctx
			}, func(*logdb.Maintenance) error { return nil }, context.Canceled},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := committedFixture(t)
				ctx := t.Context()
				if tc.ctx != nil {
					ctx = tc.ctx(t)
				}
				if err := maintain(ctx, f.fs, "data", f.opts, tc.run); !errors.Is(err, tc.want) || errors.Is(err, ErrInUse) {
					t.Errorf("maintain = %v, want %v", err, tc.want)
				}
				if _, ok := worldFile(f.fs, "stopped"); ok {
					t.Error("maintain left a mark")
				}
			})
		}

		t.Run("a directory in use", func(t *testing.T) {
			f := newFixture(t, nil)
			f.connect(dbwire.RoleWriter).mustCommit(schemaOp(), put(2, "a", "1"))
			called, err := maintainCalled(t, f, f.opts)
			if !errors.Is(err, ErrInUse) || called {
				t.Errorf("maintain of a directory in use = %v, f called: %v, want ErrInUse", err, called)
			}
			if _, ok := worldFile(f.fs, "stopped"); ok {
				t.Error("maintain left a mark")
			}
		})
	})

	t.Run("on the OS", func(t *testing.T) {
		f := newDirFixture(t, nil)
		f.connect(dbwire.RoleWriter).mustCommit(schemaOp(), put(2, "a", "1"))
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		mark := filepath.Join(f.dir, "stopped")
		if err := os.WriteFile(mark, []byte(valid), 0o600); err != nil {
			t.Fatal(err)
		}
		before := contents(t, f.dir)
		called := false
		fn := func(*logdb.Maintenance) error {
			called = true
			return nil
		}
		want := f.dir + ": a sync failed at " + at + " (" + cause + ")" + refusedTail
		if err := Maintain(t.Context(), f.dir, f.opts, fn); !errors.Is(err, ErrNotRestarted) || err.Error() != want || called {
			t.Errorf("Maintain in the same boot = %v, f called: %v, want %q", err, called, want)
		}
		if got := contents(t, f.dir); !maps.Equal(got, before) {
			t.Errorf("the refusal changed the files:\n%v\nwas\n%v", got, before)
		}
		o := f.opts
		o.Boot = machineWith(anotherBoot)
		if err := Maintain(t.Context(), f.dir, o, fn); err != nil || !called {
			t.Fatalf("Maintain in another boot = %v, f called: %v", err, called)
		}
		if _, err := os.Lstat(mark); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the mark is still there: %v", err)
		}
		if lines := f.logs.all(); len(lines) != 1 || !strings.Contains(lines[0], "before the machine restarted") {
			t.Errorf("log lines %q, want the one of the removal", lines)
		}
	})
}
