package fspath

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeTree makes, in a temporary directory, which it returns with its symbolic links resolved, the directories a,
// b/entries and b/x, the file a/file, and the symbolic links a/link and c to b/entries.
func makeTree(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	in := func(name string) string { return filepath.Join(root, filepath.FromSlash(name)) }
	for _, dir := range []string{"a", "b/entries", "b/x"} {
		if err := os.MkdirAll(in(dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(in("a/file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, link := range []string{"a/link", "c"} {
		if err := os.Symlink(in("b/entries"), in(link)); err != nil {
			t.Skipf("no symbolic links: %v", err)
		}
	}
	return root
}

// rooted returns name with "/", and ROOT at its start replaced with root: not filepath.Join, which drops "link/..".
func rooted(root, name string) string {
	return filepath.FromSlash(strings.Replace(name, "ROOT", filepath.ToSlash(root), 1))
}

func TestJoin(t *testing.T) {
	root := makeTree(t)
	t.Chdir(filepath.Join(root, "a"))
	for _, c := range []struct{ dir, name, want string }{
		{"link/..", "out", "link/../out"},
		{"./link//../", "out", "link/../out"},
		{"link/..", "", "link/.."},
		{"link/..", ".", "link/.."},
		{"", "link/../out", "link/../out"},
		{"link/../..", "out", "link/../../out"},
		{"link/../x/..", "out", "link/../out"},
		{"ROOT/a/link/..", "out", "ROOT/a/link/../out"},
		{"ROOT/a/link/../..", "out", "ROOT/a/link/../../out"},
		{"file/..", "out", "file/../out"},
		// As filepath.Join: a ".." after a directory, a missing element, or none.
		{"ROOT/b/x", "../out", "ROOT/b/out"},
		{"../b/x", "..", "../b"},
		{"missing/..", "out", "out"},
		{".", "out", "out"},
		{"./site/", "out", "site/out"},
		{".", "../site", "../site"},
		{"/..", "out", "/out"},
	} {
		dir, name, want := rooted(root, c.dir), rooted(root, c.name), rooted(root, c.want)
		if got := Join(dir, name); got != want {
			t.Errorf("Join(%q, %q) = %q, want %q", dir, name, got, want)
		}
	}
	for _, c := range []struct{ name, want string }{
		{"./link//../out/", "link/../out"},
		{"ROOT/b/x/../out", "ROOT/b/out"},
		{"./site//", "site"},
	} {
		name, want := rooted(root, c.name), rooted(root, c.want)
		if got := Clean(name); got != want {
			t.Errorf("Clean(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestAbs works in a and in c of makeTree, with $PWD the path through c.
func TestAbs(t *testing.T) {
	root := makeTree(t)
	in := func(name string) string { return filepath.Join(root, filepath.FromSlash(name)) }
	for _, c := range []struct {
		wd, name string
		abs      string // of Abs, and of Resolve for a name without ".."
		resolved string // of Resolve, if not abs
	}{
		{wd: "a", name: "link/..", abs: "b"},
		{wd: "a", name: "link/../out", abs: "b/out"},
		{wd: "a", name: "link/../y/../z", abs: "b/z"},
		{wd: "a", name: "ROOT/a/link/../out", abs: "b/out"},
		{wd: "a", name: "file/x", abs: "a/file/x"},
		{wd: "a", name: "link/x", abs: "a/link/x", resolved: "b/entries/x"},
		{wd: "c", name: "../out", abs: "b/out"},
		{wd: "c", name: "out", abs: "c/out", resolved: "b/entries/out"},
	} {
		t.Chdir(in(c.wd))
		name := rooted(root, c.name)
		if got, err := Abs(name); err != nil || got != in(c.abs) {
			t.Errorf("in %s, Abs(%q) = %q, %v; want %q", c.wd, name, got, err, in(c.abs))
		}
		want := in(c.abs)
		if c.resolved != "" {
			want = in(c.resolved)
		}
		if got, err := Resolve(name); err != nil || got != want {
			t.Errorf("in %s, Resolve(%q) = %q, %v; want %q", c.wd, name, got, err, want)
		}
	}
}
