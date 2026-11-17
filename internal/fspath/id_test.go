package fspath

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
)

func TestIn(t *testing.T) {
	root := makeTree(t)
	in := func(name string) string { return filepath.Join(root, filepath.FromSlash(name)) }
	id := func(name string) (dev, ino uint64) {
		t.Helper()
		dev, ino, ok, err := FileID(in(name))
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Skip("no identity of files on this system")
		}
		return dev, ino
	}
	if _, _, _, err := FileID(in("missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("FileID of a missing file: %v", err)
	}
	linkDev, linkIno := id("c")
	if dev, ino := id("b/entries"); linkDev != dev || linkIno != ino {
		t.Error("FileID does not follow a symbolic link")
	}

	t.Chdir(root)
	for _, c := range []struct {
		dir, name string
		want      bool
	}{
		{"b/entries", "b/entries", true},
		{"b/entries", "b/entries/new", true},
		{"b/entries", "b/entries/new/deeper", true},
		{"b/entries", "c/new", true},
		{"b/entries", "a/link/new", true},
		{"b/entries", in("b/entries/new"), true},
		{"b/entries", "b", false},
		{"b/entries", "b/x", false},
		{"b/entries", "b/x/new", false},
		{"b/entries", "a/new", false},
		// The kernel takes a/link/.. for b, which a lexical path would take for a.
		{"b", "a/link/../new", true},
		{"a", "a/link/../new", false},
	} {
		dev, ino := id(c.dir)
		got, err := In(c.name, dev, ino)
		if err != nil || got != c.want {
			t.Errorf("In(%q, %s): %v, %v, want %v", c.name, c.dir, got, err, c.want)
		}
	}
}
