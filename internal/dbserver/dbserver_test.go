package dbserver

import (
	"context"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// tree returns the content of every file of fsys, by path, and the names of its directories with a slash.
func tree(t testing.TB, fsys vfs.FS, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := fsys.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if dir != "." {
			name = dir + "/" + name
		}
		if e.IsDir() {
			out[name+"/"] = ""
			maps := tree(t, fsys, name)
			for k, v := range maps {
				out[k] = v
			}
			continue
		}
		f, err := fsys.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[name] = string(b)
	}
	return out
}

func TestCreateOpen(t *testing.T) {
	t.Run("layout", func(t *testing.T) {
		noLeaks(t)
		fsys := simfs.New("data")
		if err := Create(fsys, "data", testOptions()); err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{"files", "logdb"} {
			info, err := fsys.Lstat(dir)
			if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
				t.Errorf("%s: %v, %v", dir, info, err)
			}
		}
		if entries, err := fsys.ReadDir("."); err != nil || len(entries) != 2 {
			t.Errorf("the root has %d entries (%v), want files and logdb", len(entries), err)
		}
		if entries, err := fsys.ReadDir("files"); err != nil || len(entries) != 0 {
			t.Errorf("files/ has %d entries (%v)", len(entries), err)
		}
		srv, err := Open(fsys, "data", testOptions())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = srv.Close() }()
		if seq, err := srv.lastSeq(); err != nil || seq != 0 {
			t.Errorf("the engine has the last commit %d (%v), want none", seq, err)
		}
	})

	t.Run("modes on the OS", func(t *testing.T) {
		root, err := vfs.OpenRoot(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = root.Close() }()
		if err := Create(root, "d", testOptions()); err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{"files", "logdb"} {
			info, err := root.Lstat(dir)
			if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
				t.Errorf("%s: %v, %v", dir, info, err)
			}
		}
		srv, err := Open(root, "d", testOptions())
		if err != nil {
			t.Fatal(err)
		}
		if err := srv.Close(); err != nil {
			t.Error(err)
		}
	})

	t.Run("not empty", func(t *testing.T) {
		fsys := simfs.New("data")
		f, err := fsys.OpenFile("stray", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
		before, counted := tree(t, fsys, "."), fsys.Counted()
		err = Create(fsys, "data", testOptions())
		want := "data: not empty; a new data directory needs a missing or empty directory"
		if err == nil || err.Error() != want {
			t.Fatalf("got %v, want %q", err, want)
		}
		if after := tree(t, fsys, "."); !reflect.DeepEqual(before, after) || fsys.Counted() != counted {
			t.Errorf("Create changed a root that is not empty: %v", after)
		}
	})

	t.Run("Open of what is not a data directory", func(t *testing.T) {
		makeDirs := func(names ...string) *simfs.FS {
			fsys := simfs.New("data")
			for _, n := range names {
				if err := fsys.Mkdir(n, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			return fsys
		}
		file := func(fsys *simfs.FS, name string) {
			f, err := fsys.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			_ = f.Close()
		}
		fileFor := func(name string) *simfs.FS {
			fsys := makeDirs("files")
			file(fsys, name)
			return fsys
		}
		unreadable := makeDirs("logdb", "files")
		unreadable.Inject(simfs.Fault{
			Match: func(op simfs.Op) bool { return op.Kind == simfs.OpLstat && op.Path == "logdb" },
			Err:   syscall.EACCES,
		})
		for _, c := range []struct {
			name string
			fsys *simfs.FS
			want string
		}{
			{"nothing", simfs.New("data"), "data/logdb: missing directory"},
			{"no logdb", makeDirs("files"), "data/logdb: missing directory"},
			{"no files", makeDirs("logdb"), "data/files: missing directory"},
			{"logdb is a file", fileFor("logdb"), "data/logdb: must be a directory"},
			{"cannot read", unreadable, "data/logdb: cannot read: permission denied"},
		} {
			t.Run(c.name, func(t *testing.T) {
				counted := c.fsys.Counted()
				srv, err := Open(c.fsys, "data", testOptions())
				if err == nil {
					_ = srv.Close()
					t.Fatal("Open succeeded")
				}
				if err.Error() != c.want {
					t.Errorf("got %q, want %q", err, c.want)
				}
				if c.fsys.Counted() != counted {
					t.Errorf("Open changed %d things", c.fsys.Counted()-counted)
				}
			})
		}

		t.Run("symbolic link", func(t *testing.T) {
			for _, name := range []string{"logdb", "files"} {
				dir := t.TempDir()
				if err := Create(mustRoot(t, dir), "d", testOptions()); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(dir, name), filepath.Join(dir, "moved")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("moved", filepath.Join(dir, name)); err != nil {
					t.Skipf("no symbolic links: %v", err)
				}
				srv, err := Open(mustRoot(t, dir), "d", testOptions())
				if err == nil {
					_ = srv.Close()
					t.Fatalf("Open followed the link %s", name)
				}
				if want := "d/" + name + ": symbolic links are not allowed"; err.Error() != want {
					t.Errorf("got %q, want %q", err, want)
				}
			}
		})
	})

	t.Run("a second Open", func(t *testing.T) {
		noLeaks(t)
		for name, root := range map[string]vfs.FS{"simfs": simfs.New("data"), "OS": mustRoot(t, t.TempDir())} {
			t.Run(name, func(t *testing.T) {
				o := testOptions()
				if err := Create(root, "data", o); err != nil {
					t.Fatal(err)
				}
				first, err := Open(root, "data", o)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = first.Close() }()
				second, err := Open(root, "data", o)
				if err == nil {
					_ = second.Close()
					t.Fatal("a second Open succeeded")
				}
				if !strings.HasSuffix(err.Error(), "logdb: another process holds the writer lock") {
					t.Errorf("got %q", err)
				}
				if seq, err := first.lastSeq(); err != nil || seq != 0 {
					t.Errorf("the first server: %d, %v", seq, err)
				}
			})
		}
	})

	t.Run("negative limits", func(t *testing.T) {
		for _, set := range []func(*dbwire.Limits){
			func(l *dbwire.Limits) { l.Frame = -1 }, func(l *dbwire.Limits) { l.Commit = -1 },
			func(l *dbwire.Limits) { l.Object = -1 }, func(l *dbwire.Limits) { l.Staged = -1 },
			func(l *dbwire.Limits) { l.Readers = -1 }, func(l *dbwire.Limits) { l.Frame = dbwire.MaxFrame + 1 },
		} {
			fsys := simfs.New("data")
			if err := Create(fsys, "data", testOptions()); err != nil {
				t.Fatal(err)
			}
			o := testOptions()
			set(&o.Limits)
			if srv, err := Open(fsys, "data", o); err == nil {
				_ = srv.Close()
				t.Errorf("Open accepted the limits %+v", o.Limits)
			}
		}
	})
}

func mustRoot(t testing.TB, dir string) vfs.FS {
	t.Helper()
	root, err := vfs.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func TestAccept(t *testing.T) {
	// seed makes a store whose meta table has row, if it is not nil, and whose engine has the commits that it needs.
	seed := func(t *testing.T, row []byte, commit bool) *simfs.FS {
		fsys := simfs.New("data")
		if err := Create(fsys, "data", testOptions()); err != nil {
			t.Fatal(err)
		}
		sub, err := fsys.Sub("logdb")
		if err != nil {
			t.Fatal(err)
		}
		db, err := logdb.Open(sub, logdb.Options{WriterWait: -1})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Update(context.Background(), func(tx *logdb.Tx) error {
			if row != nil {
				return tx.Put(dbwire.TableMeta, []byte(dbwire.MetaSchema), row)
			}
			if commit {
				return tx.Put(2, []byte("k"), []byte("v"))
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		_ = sub.Close()
		return fsys
	}
	uv := func(n uint64) []byte { return binary.AppendUvarint(nil, n) }
	for _, c := range []struct {
		name   string
		row    []byte
		commit bool
		want   string // the error, or "" for an Open that works
	}{
		{"a fresh store", nil, false, ""},
		{"schema 9", uv(9), true, ""},
		{"schema 8", uv(8), true, ""},
		{"schema 7", uv(7), true, ""},
		{"schema 6", uv(6), true, ""},
		{"schema 1", uv(1), true, ""},
		{"schema 10", uv(10), true, "data/logdb: the store schema is of version 10, newer than this program knows (9)"},
		{"a schema of 2^40", uv(1 << 40), true,
			"data/logdb: the store schema is of version 1099511627776, newer than this program knows (9)"},
		{"commits and no schema", nil, true, "data/logdb: not a store of whiteroom (no schema version)"},
		{"schema 0", uv(0), true, `data/logdb: meta "schema": invalid value`},
		{"an empty row", []byte{}, true, `data/logdb: meta "schema": invalid value`},
		{"a byte after the number", append(uv(7), 0), true, `data/logdb: meta "schema": invalid value`},
		{"a number that does not end", []byte{0x80, 0x80}, true, `data/logdb: meta "schema": invalid value`},
		{"a number over 64 bits", slices.Repeat([]byte{0xff}, 11), true, `data/logdb: meta "schema": invalid value`},
	} {
		t.Run(c.name, func(t *testing.T) {
			noLeaks(t)
			fsys := seed(t, c.row, c.commit)
			before, counted := tree(t, fsys, "."), fsys.Counted()
			srv, err := Open(fsys, "data", testOptions())
			if c.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if err := srv.Close(); err != nil {
					t.Error(err)
				}
				return
			}
			if err == nil {
				_ = srv.Close()
				t.Fatal("Open succeeded")
			}
			if err.Error() != c.want {
				t.Errorf("got %q, want %q", err, c.want)
			}
			if after := tree(t, fsys, "."); !reflect.DeepEqual(before, after) || fsys.Counted() != counted {
				t.Errorf("a refused Open changed the files (%d counted operations)", fsys.Counted()-counted)
			}
		})
	}
}

func TestMaxKey(t *testing.T) {
	if dbwire.MaxKey != logdb.MaxKey {
		t.Errorf("dbwire.MaxKey is %d, logdb.MaxKey %d", dbwire.MaxKey, logdb.MaxKey)
	}
}
