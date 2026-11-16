package dbserver

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
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
