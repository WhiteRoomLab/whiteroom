package dbserver

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// noSys is a FileInfo that has no device and inode.
type noSys struct{ fs.FileInfo }

func (noSys) Sys() any { return nil }

// TestRepairKeepInsideData checks that the directory that is opened is not in the data directory whatever the checks of its
// names saw: here checkKeep has seen the directory that the name leads to now, which is in the data directory, as it would if
// the name had been turned between its two looks at it. Nothing is made, and nothing is written: not even a directory that is
// removed again, which the time of the change of the directory that would have held it tells.
func TestRepairKeepInsideData(t *testing.T) {
	old := time.Unix(1_000_000_000, 0)
	const inDataWhat = ": -keep is in the data directory: the copy of logdb/ must be kept apart from it"
	for _, tc := range []struct {
		name string
		to   string // where the link leads, in the data directory
		leaf bool   // the directory of the copy is a directory that is there, in the link's
	}{
		{"a parent in logdb", "logdb/log", false},
		{"a parent that is the data directory", ".", false},
		{"a parent that is a subdirectory", "sub", false},
		{"an empty directory in a subdirectory", "sub", true},
		{"an empty directory in logdb", "logdb/log", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newKeepWorld(t)
			if tc.leaf {
				mkdir(t, filepath.Join(w.data, filepath.FromSlash(tc.to), "keep"))
			}
			w.before = contents(t, w.data)
			link := w.link(t, "a", filepath.Join(w.data, filepath.FromSlash(tc.to)))
			keep := filepath.Join(link, "keep")
			var c keepCheck
			var err error
			held := filepath.Join(w.data, filepath.FromSlash(tc.to))
			if tc.leaf {
				c.info, err = os.Lstat(keep)
				held = filepath.Join(held, "keep")
			} else {
				c.parent, err = os.Stat(link)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(held, old, old); err != nil {
				t.Fatal(err)
			}
			k, err := openKeep(w.info, keep, c)
			if err == nil || err.Error() != keep+inDataWhat {
				if err == nil {
					_ = k.fs.Close()
				}
				t.Fatalf("openKeep() = %v, want %q", err, keep+inDataWhat)
			}
			if info, err := os.Stat(held); err != nil || !info.ModTime().Equal(old) {
				t.Errorf("the directory that would have held the copy was changed (%v)", err)
			}
			w.unchanged(t)
		})
	}

	t.Run("the identity of the data directory is not known", func(t *testing.T) {
		for _, there := range []bool{true, false} {
			w := newKeepWorld(t)
			keep := w.path("keep")
			if there {
				mkdir(t, keep)
			}
			c, err := checkKeep(w.data, keep)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(w.top, old, old); err != nil {
				t.Fatal(err)
			}
			k, err := openKeep(noSys{w.info}, keep, c)
			want := keep + ": cannot tell whether -keep is in the data directory: the device and the inode of the data directory are not known"
			if err == nil || err.Error() != want {
				if err == nil {
					_ = k.fs.Close()
				}
				t.Errorf("openKeep() with a directory that is there %v = %v, want %q", there, err, want)
			}
			if info, err := os.Stat(w.top); err != nil || !info.ModTime().Equal(old) {
				t.Errorf("the directory that would have held the copy was changed (%v)", err)
			}
		}
	})

	t.Run("outside it", func(t *testing.T) {
		w := newKeepWorld(t)
		elsewhere := w.path("elsewhere")
		mkdir(t, elsewhere)
		link := w.link(t, "a", elsewhere)
		keep := filepath.Join(link, "keep")
		parent, err := os.Stat(link)
		if err != nil {
			t.Fatal(err)
		}
		k, err := openKeep(w.info, keep, keepCheck{parent: parent})
		if err != nil {
			t.Fatalf("openKeep() = %v", err)
		}
		if err := k.fs.Close(); err != nil {
			t.Error(err)
		}
		if info, err := os.Lstat(filepath.Join(elsewhere, "keep")); err != nil || !info.IsDir() {
			t.Errorf("the directory of the copy is %v, %v", info, err)
		}
	})
}

// TestRepairKeepParentFlip checks, on the OS, that a copy never lands in the data directory while another process, which owns a
// directory that holds the directory of the copy, turns the link to it between a directory outside the data directory and one in
// it, and that is also while checkKeep looks at the names: the directory of the copy is missing (and is made), or is an empty
// directory that is there.
func TestRepairKeepParentFlip(t *testing.T) {
	for _, tc := range []struct {
		name string
		to   string // in the data directory
		leaf bool   // the directory of the copy is there in both, empty
	}{
		{"a missing directory in logdb", "logdb/log", false},
		{"a missing directory in the data directory", ".", false},
		{"an empty directory in a subdirectory", "sub", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newKeepWorld(t)
			safe := w.path("safe")
			mkdir(t, safe)
			inData := filepath.Join(w.data, filepath.FromSlash(tc.to))
			if tc.leaf {
				mkdir(t, filepath.Join(safe, "keep"))
				mkdir(t, filepath.Join(inData, "keep"))
			}
			w.before = contents(t, w.data)
			link := w.path("a")
			if err := os.Symlink(safe, link); err != nil {
				t.Fatal(err)
			}
			keep := filepath.Join(link, "keep")
			stop := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(done)
				tmp := w.path("a.tmp")
				for i := 0; ; i++ {
					select {
					case <-stop:
						return
					default:
					}
					target := safe
					if i%2 == 1 {
						target = inData
					}
					_ = os.Remove(tmp)
					if err := os.Symlink(target, tmp); err == nil {
						_ = os.Rename(tmp, link)
					}
				}
			}()
			wins, opened, tries := 0, 0, 3000
			for range tries {
				k, err := osKeep(w.root, keep)()
				if err != nil {
					continue
				}
				opened++
				info, err := k.fs.Lstat(".")
				if err != nil {
					t.Error(err)
				}
				if tc.leaf {
					if in, err := os.Lstat(filepath.Join(inData, "keep")); err == nil && os.SameFile(info, in) {
						wins++ // the directory of the copy is a directory of the data directory
					}
				} else if _, err := os.Lstat(filepath.Join(inData, "keep")); err == nil {
					wins++ // the directory of the copy is made in the data directory
					_ = os.Remove(filepath.Join(inData, "keep"))
				}
				k.discard()
				_ = k.fs.Close()
				if !tc.leaf {
					_ = os.Remove(filepath.Join(safe, "keep"))
				}
			}
			close(stop)
			<-done
			t.Logf("%d calls: %d opened a directory, %d of them in the data directory", tries, opened, wins)
			if wins > 0 {
				t.Errorf("osKeep gave a directory of the data directory %d times", wins)
			}
			if opened == 0 {
				t.Error("no call opened a directory")
			}
			w.unchanged(t)
		})
	}
}
