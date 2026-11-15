package fspath

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// symlink makes a symbolic link at name to target.
func symlink(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		t.Skipf("cannot make symbolic links: %v", err)
	}
}

// modeOf returns the mode of name, without following a symbolic link.
func modeOf(t *testing.T, name string) fs.FileMode {
	t.Helper()
	info, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode()
}

// TestMakeDirs checks which names makeDirs makes durable: those of the directories that it has made, the outermost
// first, by paths as the one that it is given, and none of a directory that was there. The directory that SyncParent
// opens for each is the one that holds it. An error of the sync is the error of makeDirs.
func TestMakeDirs(t *testing.T) {
	top, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var synced []string
	var parents []fs.FileInfo
	record := func(dir string) error {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("%s is synced before it is a directory (%v)", dir, err)
		}
		// The directory that the kernel takes for the parent. Not filepath.Join, which drops the "..".
		parent, err := os.Stat(dir + string(filepath.Separator) + "..")
		if err != nil {
			t.Error(err)
		}
		synced, parents = append(synced, dir), append(parents, parent)
		return nil
	}
	symlink(t, filepath.Join(top, "a"), filepath.Join(top, "link"))
	symlink(t, filepath.Join(top, "a", "b"), filepath.Join(top, "deep"))
	t.Chdir(top)
	in := func(names ...string) []string {
		for i, name := range names {
			names[i] = filepath.Join(top, filepath.FromSlash(name))
		}
		return names
	}
	for _, c := range []struct {
		what, dir string
		want      []string // the directories whose names are synced
		parents   []string // the directories that hold them, from top
	}{
		{"three missing directories", filepath.Join(top, "a", "b", "data"), in("a", "a/b", "a/b/data"),
			[]string{".", "a", "a/b"}},
		{"a directory that is there", filepath.Join(top, "a", "b", "data"), nil, nil},
		{"one missing directory", filepath.Join(top, "a", "b", "other"), in("a/b/other"), []string{"a/b"}},
		{"through a symbolic link", filepath.Join(top, "link", "c", "data"), in("link/c", "link/c/data"),
			[]string{"a", "a/c"}},
		{"after a link and ..", "link/../d", []string{"link/../d"}, []string{"."}},
		{"a relative path", "rel/data", []string{"rel", "rel/data"}, []string{".", "rel"}},
		{"a .. after a missing directory", "x/missing/../y/", []string{"x", "x/y"}, []string{".", "x"}},
		// The parent of what deep points to is a, not the directory of deep.
		{"missing directories after a link and ..", "deep/../m/d", []string{"deep/../m", "deep/../m/d"},
			[]string{"a", "a/m"}},
	} {
		synced, parents = nil, nil
		if err := makeDirs(filepath.FromSlash(c.dir), record); err != nil {
			t.Fatalf("%s: makeDirs() = %v", c.what, err)
		}
		var want []string
		for _, name := range c.want {
			want = append(want, filepath.FromSlash(name))
		}
		if !slices.Equal(synced, want) {
			t.Fatalf("%s: makeDirs() has synced the names of %q, want %q", c.what, synced, want)
		}
		for i, name := range c.parents {
			holder, err := os.Stat(filepath.Join(top, filepath.FromSlash(name)))
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(parents[i], holder) {
				t.Errorf("%s: the name of %s is not synced in %s", c.what, synced[i], name)
			}
		}
		if got := modeOf(t, filepath.FromSlash(c.dir)); got != fs.ModeDir|0o700 {
			t.Errorf("%s: the mode of the directory is %v", c.what, got)
		}
	}

	failed := errors.New("sync failed")
	synced = nil
	err = makeDirs(filepath.Join(top, "e", "f"), func(dir string) error {
		synced = append(synced, dir)
		return failed
	})
	if !errors.Is(err, failed) || !slices.Equal(synced, []string{filepath.Join(top, "e")}) {
		t.Errorf("makeDirs() with a sync that fails = %v after the syncs %q, want the error after the first", err, synced)
	}
	file := filepath.Join(top, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	synced = nil
	if err := makeDirs(filepath.Join(file, "data"), record); err == nil || synced != nil {
		t.Errorf("makeDirs() in a file = %v after the syncs %q, want an error and no sync", err, synced)
	}
	// The error is that of os.MkdirAll.
	symlink(t, filepath.Join(top, "nowhere"), filepath.Join(top, "dangling"))
	dangling := "dangling" + string(filepath.Separator) + ".." + string(filepath.Separator) + "data"
	if err, want := makeDirs(dangling, record), os.MkdirAll(dangling, 0o700); err == nil || want == nil ||
		err.Error() != want.Error() || synced != nil {
		t.Errorf("makeDirs() after a link to nothing and .. = %v after the syncs %q, want %v and no sync", err, synced, want)
	}
}

// TestSyncParent checks that SyncParent opens the directory that holds the directory that it is given, and that the
// error names it: after a symbolic link and "..", it is the one that holds what the link points to, which the kernel
// takes for the parent.
func TestSyncParent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open every directory")
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(parent, "empty")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	// In a directory that can be read: beside is next to link, which points to empty.
	readable, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(readable, "beside"), 0o700); err != nil {
		t.Fatal(err)
	}
	symlink(t, empty, filepath.Join(readable, "link"))
	// Write and search, but not read.
	if err := os.Chmod(parent, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	denied := func(what, name string, err error) {
		t.Helper()
		if pe, ok := errors.AsType[*fs.PathError](err); !ok || pe.Op != "open" || pe.Path != name ||
			!errors.Is(err, fs.ErrPermission) {
			t.Errorf("%s = %v, want that %s cannot be opened", what, err, name)
		}
	}

	denied("SyncParent()", parent, SyncParent(empty))
	if err := SyncParent(filepath.Join(readable, "beside")); err != nil {
		t.Errorf("SyncParent() in a directory that can be read = %v", err)
	}
	// Not the directory of the link, which can be read. Not filepath.Join, which drops the "..".
	through := filepath.Join(readable, "link") + string(filepath.Separator) + ".."
	denied("SyncParent() after a symbolic link and ..", through,
		SyncParent(through+string(filepath.Separator)+"copied"))
}

// TestMakeDirsSyncs checks that MakeDirs syncs the parent of each directory that it makes: it makes the directories
// with the mode 0700, and an error of the file system is that of os.MkdirAll.
func TestMakeDirsSyncs(t *testing.T) {
	top, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(top, "a", "b")
	if err := MakeDirs(dir); err != nil {
		t.Fatalf("MakeDirs() = %v", err)
	}
	for _, d := range []string{filepath.Join(top, "a"), dir} {
		if got := modeOf(t, d); got != fs.ModeDir|0o700 {
			t.Errorf("mode of %s = %v, want %v", d, got, fs.ModeDir|0o700)
		}
	}
	if err := MakeDirs(dir); err != nil {
		t.Errorf("MakeDirs() of a directory that is there = %v", err)
	}
	file := filepath.Join(top, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	want := os.MkdirAll(filepath.Join(file, "data"), 0o700)
	if err := MakeDirs(filepath.Join(file, "data")); err == nil || want == nil || err.Error() != want.Error() {
		t.Errorf("MakeDirs() in a file = %v, want %v", err, want)
	}
}
