package vfs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func openRoot(t *testing.T, dir string) FS {
	t.Helper()
	fsys, err := OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fsys.Close() })
	return fsys
}

func writeFile(t *testing.T, fsys FS, name, data string) {
	t.Helper()
	f, err := fsys.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func describe(err error) string {
	if err == nil {
		return "nil"
	}
	return fmt.Sprintf("%T %s", err, err)
}

// TestOSErrors checks that the errors are those of os.Root, as internal/store's messages need.
func TestOSErrors(t *testing.T) {
	dir := t.TempDir()
	fsys := openRoot(t, dir)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	writeFile(t, fsys, "f", "data")
	if err := fsys.Mkdir("d", 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	symlinks := os.Symlink(outside, filepath.Join(dir, "esc")) == nil

	ignore := func(_ any, err error) error { return err }
	tests := []struct {
		name     string
		got      func() error
		want     func() error
		is       error  // what errors.Is must find, if not nil
		contains string // what the message must contain, if not empty
	}{
		{"open missing", func() error { return ignore(fsys.Open("missing")) }, func() error { return ignore(root.Open("missing")) }, fs.ErrNotExist, "openat missing: "},
		{"open missing/x", func() error { return ignore(fsys.Open("missing/x")) }, func() error { return ignore(root.Open("missing/x")) }, fs.ErrNotExist, ""},
		{"open f/x", func() error { return ignore(fsys.Open("f/x")) }, func() error { return ignore(root.Open("f/x")) }, syscall.ENOTDIR, ""},
		{"create existing", func() error { return ignore(fsys.OpenFile("f", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)) },
			func() error { return ignore(root.OpenFile("f", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)) }, fs.ErrExist, ""},
		{"open dir to write", func() error { return ignore(fsys.OpenFile("d", os.O_RDWR, 0)) }, func() error { return ignore(root.OpenFile("d", os.O_RDWR, 0)) }, nil, ""},
		{"escape", func() error { return ignore(fsys.Open("../x")) }, func() error { return ignore(root.Open("../x")) }, nil, "path escapes from parent"},
		{"mkdir existing", func() error { return fsys.Mkdir("d", 0o700) }, func() error { return root.Mkdir("d", 0o700) }, fs.ErrExist, ""},
		{"remove missing", func() error { return fsys.Remove("missing") }, func() error { return root.Remove("missing") }, fs.ErrNotExist, ""},
		{"lstat missing", func() error { return ignore(fsys.Lstat("missing")) }, func() error { return ignore(root.Lstat("missing")) }, fs.ErrNotExist, ""},
		{"rename missing", func() error { return fsys.Rename("missing", "x") }, func() error { return root.Rename("missing", "x") }, fs.ErrNotExist, "renameat missing x: "},
		{"rename onto dir", func() error { return fsys.Rename("f", "d") }, func() error { return root.Rename("f", "d") }, fs.ErrExist, ""},
		{"readdir missing", func() error { return ignore(fsys.ReadDir("missing")) }, func() error { return ignore(root.Open("missing")) }, fs.ErrNotExist, ""},
		{"syncdir missing", func() error { return fsys.SyncDir("missing") }, func() error { return ignore(root.Open("missing")) }, fs.ErrNotExist, ""},
		{"sub missing", func() error { return ignore(fsys.Sub("missing")) }, func() error { return ignore(root.OpenRoot("missing")) }, fs.ErrNotExist, ""},
		{"sub of a file", func() error { return ignore(fsys.Sub("f")) }, func() error { return ignore(root.OpenRoot("f")) }, nil, "not a directory"},
		{"openroot missing", func() error { return ignore(OpenRoot(filepath.Join(dir, "missing"))) },
			func() error { return ignore(os.OpenRoot(filepath.Join(dir, "missing"))) }, fs.ErrNotExist, ""},
		{"write read-only", func() error {
			f, err := fsys.Open("f")
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			return ignore(f.Write([]byte("x")))
		}, func() error {
			f, err := root.Open("f")
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			return ignore(f.Write([]byte("x")))
		}, nil, filepath.Join(dir, "f")},
	}
	if symlinks {
		tests = append(tests, struct {
			name     string
			got      func() error
			want     func() error
			is       error
			contains string
		}{"symlink out of the root", func() error { return ignore(fsys.Lstat("esc/x")) }, func() error { return ignore(root.Lstat("esc/x")) }, nil, "statat esc/x: path escapes from parent"})
	}
	for _, tt := range tests {
		got, want := tt.got(), tt.want()
		switch {
		case got == nil:
			t.Errorf("%s: no error", tt.name)
		case describe(got) != describe(want):
			t.Errorf("%s: %s, want %s", tt.name, describe(got), describe(want))
		case tt.is != nil && !errors.Is(got, tt.is):
			t.Errorf("%s: %v is not %v", tt.name, got, tt.is)
		case !strings.Contains(got.Error(), tt.contains):
			t.Errorf("%s: %q does not contain %q", tt.name, got, tt.contains)
		}
	}

	if f, err := fsys.Open("missing"); f != nil || err == nil {
		t.Errorf("Open of a missing file: %v, %v; want a nil File", f, err)
	}
	var pe *fs.PathError
	if _, err := fsys.Open("missing"); !errors.As(err, &pe) || pe.Op != "openat" || pe.Path != "missing" {
		t.Errorf("Open of a missing file: %#v", err)
	}
	var le *os.LinkError
	if err := fsys.Rename("missing", "x"); !errors.As(err, &le) {
		t.Errorf("Rename of a missing file: %T, want *os.LinkError", err)
	}
}

func TestReadDirSorted(t *testing.T) {
	fsys := openRoot(t, t.TempDir())
	for _, name := range []string{"b", "c", "a"} {
		writeFile(t, fsys, name, name)
	}
	if err := fsys.Mkdir("0", 0o700); err != nil {
		t.Fatal(err)
	}
	entries, err := fsys.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, fmt.Sprintf("%s %v", e.Name(), e.IsDir()))
	}
	if want := []string{"0 true", "a false", "b false", "c false"}; !slices.Equal(names, want) {
		t.Errorf("ReadDir: %q, want %q", names, want)
	}
	if entries, err := fsys.ReadDir("0"); err != nil || len(entries) != 0 {
		t.Errorf("ReadDir of an empty directory: %v, %v", entries, err)
	}
	if _, err := fsys.ReadDir("a"); !errors.Is(err, syscall.ENOTDIR) {
		t.Errorf("ReadDir of a file: %v", err)
	}
}

func TestSyncDir(t *testing.T) {
	fsys := openRoot(t, t.TempDir())
	if err := fsys.Mkdir("d", 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, fsys, "d/f", "data")
	for _, name := range []string{".", "d", "d/f"} {
		if err := fsys.SyncDir(name); err != nil {
			t.Errorf("SyncDir(%q): %v", name, err)
		}
	}
	if err := fsys.SyncDir("missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("SyncDir of a missing directory: %v", err)
	}
}

func TestSubName(t *testing.T) {
	dir := t.TempDir()
	fsys := openRoot(t, dir)
	if err := fsys.Mkdir("a", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Mkdir("a/b", 0o700); err != nil {
		t.Fatal(err)
	}
	a, err := fsys.Sub("a")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	b, err := a.Sub("b")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	if got, want := a.Name(), dir+string(filepath.Separator)+"a"; got != want {
		t.Errorf("Name: %q, want %q", got, want)
	}
	if got, want := b.Name(), filepath.Join(dir, "a", "b"); got != want {
		t.Errorf("Name: %q, want %q", got, want)
	}
	writeFile(t, b, "f", "data")
	if _, err := fsys.Lstat("a/b/f"); err != nil {
		t.Error(err)
	}
	if _, err := b.Open("../f"); err == nil || !strings.Contains(err.Error(), "path escapes from parent") {
		t.Errorf("Open of ../f in a Sub: %v", err)
	}
}

func TestLock(t *testing.T) {
	dir := t.TempDir()
	a, b := openRoot(t, dir), openRoot(t, dir)
	writeFile(t, a, "lock", "")
	switch runtime.GOOS {
	case "darwin", "dragonfly", "freebsd", "illumos", "linux", "netbsd", "openbsd":
	default:
		_, err := a.Lock("lock", Exclusive)
		if err == nil || !strings.Contains(err.Error(), "logdb needs flock(2), which "+runtime.GOOS+" does not have") {
			t.Errorf("Lock: %v", err)
		}
		return
	}
	lock := func(fsys FS, mode LockMode) func() error {
		t.Helper()
		unlock, err := fsys.Lock("lock", mode)
		if err != nil {
			t.Fatal(err)
		}
		return unlock
	}
	locked := func(fsys FS, mode LockMode) {
		t.Helper()
		_, err := fsys.Lock("lock", mode)
		var pe *fs.PathError
		if !errors.Is(err, ErrLocked) || !errors.As(err, &pe) || err.Error() != "flock lock: locked by another process" {
			t.Errorf("Lock(%d): %v, want ErrLocked", mode, err)
		}
	}
	unlock := lock(a, Exclusive)
	locked(b, Shared)
	locked(b, Exclusive)
	locked(a, Exclusive)
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	if err := unlock(); err == nil {
		t.Error("a second unlock did not fail")
	}
	s1, s2 := lock(a, Shared), lock(b, Shared)
	locked(b, Exclusive)
	if err := s1(); err != nil {
		t.Fatal(err)
	}
	locked(a, Exclusive)
	if err := s2(); err != nil {
		t.Fatal(err)
	}
	if err := lock(b, Exclusive)(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Lock("missing", Shared); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Lock of a missing file: %v", err)
	}
	if _, err := a.Lock("lock", LockMode(2)); !errors.Is(err, syscall.EINVAL) {
		t.Errorf("Lock with a bad mode: %v", err)
	}
}

func TestCounter(t *testing.T) {
	c := Count(openRoot(t, t.TempDir()))
	if err := c.Mkdir("d", 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := c.OpenFile("d/f", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("xy"), 10); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.SyncDir("d"); err != nil {
		t.Fatal(err)
	}
	if err := c.Rename("d/f", "d/g"); err != nil {
		t.Fatal(err)
	}
	sub, err := c.Sub("d")
	if err != nil {
		t.Fatal(err)
	}
	if sub.Name() != c.Name()+string(filepath.Separator)+"d" {
		t.Errorf("Name of a Sub: %q", sub.Name())
	}
	g, err := sub.OpenFile("h", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(g.Sync(), g.Close(), sub.SyncDir("."), sub.Remove("g"), sub.Mkdir("e", 0o700), sub.Close()); err != nil {
		t.Fatal(err)
	}
	if err := c.Remove("missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Remove of a missing file: %v", err)
	}
	if f, err := c.Open("missing"); f != nil || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open of a missing file: %v, %v", f, err)
	}
	if got, err := c.ReadDir("d"); err != nil || len(got) != 2 {
		t.Errorf("ReadDir: %v, %v", got, err)
	}
	want := Counts{Writes: 3, BytesWritten: 10, Syncs: 2, DirSyncs: 2, Renames: 1, Removes: 2, Mkdirs: 2}
	if got := c.Counts(); got != want {
		t.Errorf("Counts: %+v, want %+v", got, want)
	}
}
