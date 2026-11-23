package dbserver

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// TestRootNotWritable checks that OpenDir and Maintain refuse a data directory whose root this process cannot write in,
// although logdb/ and files/ are writable (a copy that the superuser has made and that only its subdirectories have been
// given away from): the daemon could not leave the mark of a failed sync there. They refuse it before the engine opens, and
// change nothing.
func TestRootNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the superuser passes access(2)")
	}
	f := newDirFixture(t, nil)
	f.connect(dbwire.RoleWriter).mustCommit(schemaOp(), put(2, "a", "1"))
	if err := f.srv.Close(); err != nil {
		t.Fatal(err)
	}
	mark := filepath.Join(f.dir, markName)
	restore := func() {
		if err := os.Chmod(f.dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = os.Chmod(f.dir, 0o700) })
	want := f.dir + ": whiteroom-db cannot write in the data directory (permission denied); it must be able to leave the " +
		"mark of a failed sync there: give the directory to the user of the daemon"

	t.Run("refuses", func(t *testing.T) {
		if err := os.Chmod(f.dir, 0o500); err != nil {
			t.Fatal(err)
		}
		defer restore()
		before := contents(t, f.dir)
		var checked []string
		o := f.opts
		o.Hook = func(point string) error {
			checked = append(checked, point)
			return nil
		}
		if srv, err := OpenDir(f.dir, o); err == nil {
			_ = srv.Close()
			t.Error("OpenDir of a directory that it cannot write in succeeded")
		} else if err.Error() != want || errors.Is(err, ErrNotRestarted) {
			t.Errorf("OpenDir = %v, want %q", err, want)
		}
		called := false
		err := Maintain(t.Context(), f.dir, o, func(*logdb.Maintenance) error {
			called = true
			return nil
		})
		if err == nil || err.Error() != want || errors.Is(err, ErrNotRestarted) || called {
			t.Errorf("Maintain = %v, f called: %v, want %q and no call", err, called, want)
		}
		if len(checked) != 0 {
			t.Errorf("the refusals went on to %q, want them before the engine opens", checked)
		}
		if after := contents(t, f.dir); !maps.Equal(before, after) {
			t.Errorf("the refusals changed the data directory:\n%v\nwas\n%v", after, before)
		}
		if lines := f.logs.all(); len(lines) != 0 {
			t.Errorf("log lines %q, want none", lines)
		}
	})

	t.Run("after the mark", func(t *testing.T) {
		text := markWith(testBoot, "time: 2026-11-15T09:29:00Z\n", "cause: x\n")
		if err := os.WriteFile(mark, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Remove(mark) }()
		if err := os.Chmod(f.dir, 0o500); err != nil {
			t.Fatal(err)
		}
		defer restore()
		if _, err := openClosing(t, f, f.opts); !errors.Is(err, ErrNotRestarted) {
			t.Errorf("OpenDir = %v, want the refusal of the mark", err)
		}
		if _, err := maintainOS(t, f); !errors.Is(err, ErrNotRestarted) {
			t.Errorf("Maintain = %v, want the refusal of the mark", err)
		}
	})

	// The refusals left no lock and no file: the directory opens once it is given back.
	t.Run("given back", func(t *testing.T) {
		if called, err := maintainOS(t, f); err != nil || !called {
			t.Fatalf("Maintain = %v, f called: %v", err, called)
		}
		srv, err := openClosing(t, f, f.opts)
		if err != nil {
			t.Fatalf("OpenDir = %v", err)
		}
		_ = srv.Close()
	})
}

// maintainOS runs Maintain on the data directory of f, and reports whether it called its function, which does nothing.
func maintainOS(t *testing.T, f *fixture) (called bool, err error) {
	t.Helper()
	err = Maintain(t.Context(), f.dir, f.opts, func(*logdb.Maintenance) error {
		called = true
		return nil
	})
	return called, err
}
