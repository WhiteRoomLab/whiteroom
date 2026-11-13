//go:build linux || darwin

package wpimport

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// diskCopy is a copy of the uploads in a directory, with a file of each kind that the last element of a path can be, and
// a file of another directory (secret.png of outside) that links lead to.
type diskCopy struct {
	dir, outside string
	data, secret []byte // of 2026/10/a.png and of outside/secret.png
	pipes        []string
}

const (
	errSymlink    = "a symbolic link in the copy of the uploads"
	errNotRegular = "not a regular file in the copy of the uploads"
)

func newDiskCopy(t *testing.T) *diskCopy {
	t.Helper()
	c := &diskCopy{dir: t.TempDir(), outside: t.TempDir(), data: testPNG(10, 10, 1), secret: testPNG(12, 12, 2)}
	write := func(name string, data []byte, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, data, mode); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, name string) {
		t.Helper()
		if err := os.Symlink(target, filepath.Join(c.dir, name)); err != nil {
			t.Skipf("cannot make symbolic links: %v", err)
		}
	}
	sub := filepath.Join(c.dir, "2026", "10")
	write(filepath.Join(sub, "a.png"), c.data, 0o644)
	write(filepath.Join(c.outside, "secret.png"), c.secret, 0o644)
	write(filepath.Join(sub, "noperm.png"), c.data, 0o644)
	if err := os.Chmod(filepath.Join(sub, "noperm.png"), 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(sub, "dir.png"), 0o755); err != nil {
		t.Fatal(err)
	}
	pipe := filepath.Join(sub, "pipe.png")
	if err := syscall.Mkfifo(pipe, 0o644); err != nil {
		t.Skipf("cannot make named pipes: %v", err)
	}
	c.pipes = append(c.pipes, pipe)
	link(filepath.Join(c.outside, "secret.png"), "2026/10/out.png")
	link("a.png", "2026/10/in.png")
	link("missing.png", "2026/10/gone.png")
	link(c.outside, "outside-dir")
	link("2026/10", "inside-dir")
	return c
}

// fsys returns the copy as the file system that kind tells: "DirFS" or "OpenRoot".
func (c *diskCopy) fsys(t *testing.T, kind string) fs.FS {
	t.Helper()
	if kind == "DirFS" {
		return os.DirFS(c.dir)
	}
	root, err := os.OpenRoot(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root.FS()
}

// within calls f, and fails the test if it does not return in time, as it waits on a named pipe, which it then lets go.
func (c *diskCopy) within(t *testing.T, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
		return
	case <-time.After(10 * time.Second):
	}
	// Opening a named pipe for writing lets a reader that waits on it go on.
	for _, p := range c.pipes {
		if w, err := os.OpenFile(p, os.O_RDWR, 0); err == nil {
			_ = w.Close()
		}
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	t.Fatal("did not return in 10 seconds: it waits on a named pipe")
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root reads a file that has no permissions")
	}
}

func TestOpenUploadOnDisk(t *testing.T) {
	for _, kind := range []string{"DirFS", "OpenRoot"} {
		t.Run(kind, func(t *testing.T) {
			c := newDiskCopy(t)
			fsys := c.fsys(t, kind)
			for _, tt := range []struct {
				name, rel string
				// want is a reason of a skip, "file" or "missing" (errNotInCopy), and "stops" is any other error.
				want string
			}{
				{"a file", "2026/10/a.png", "file"},
				{"a link out of the copy", "2026/10/out.png", errSymlink},
				{"a link in the copy", "2026/10/in.png", errSymlink},
				{"a link to nothing", "2026/10/gone.png", errSymlink},
				{"a named pipe", "2026/10/pipe.png", errNotRegular},
				{"a directory", "2026/10/dir.png", errNotRegular},
				{"no file", "2026/10/none.png", "missing"},
				{"a path through a file", "2026/10/a.png/x", "missing"},
				// Links in the directories of the path are followed unless they lead out of an OpenRoot.
				{"a directory that is a link in the copy", "inside-dir/a.png", "file"},
			} {
				t.Run(tt.name, func(t *testing.T) {
					var f fs.File
					var err error
					c.within(t, func() { f, err = openUpload(fsys, tt.rel) })
					switch tt.want {
					case "file":
						if err != nil {
							t.Fatal(err)
						}
						got, rerr := io.ReadAll(f)
						_ = f.Close()
						if rerr != nil || string(got) != string(c.data) {
							t.Errorf("file = %d bytes, %v; want the data", len(got), rerr)
						}
					case "missing":
						if f != nil || !errors.Is(err, errNotInCopy) {
							t.Errorf("file = %v, error = %v; want errNotInCopy", f, err)
						}
					default:
						if f != nil {
							_ = f.Close()
							t.Errorf("file = %v, want none", f)
						}
						wantSkip(t, err, tt.want)
						if errors.Is(err, errNotInCopy) {
							t.Error("error is errNotInCopy, which the download follows")
						}
					}
				})
			}

			t.Run("a file without permissions", func(t *testing.T) {
				skipIfRoot(t)
				f, err := openUpload(fsys, "2026/10/noperm.png")
				if f != nil {
					t.Errorf("file = %v, want none", f)
				}
				wantStopping(t, err)
				if !errors.Is(err, fs.ErrPermission) || !strings.HasPrefix(err.Error(), "reading the copy of the uploads: ") {
					t.Errorf("error = %v, want one that wraps fs.ErrPermission, after the reading", err)
				}
			})
			t.Run("a directory that is a link out of the copy", func(t *testing.T) {
				f, err := openUpload(fsys, "outside-dir/secret.png")
				if kind == "DirFS" {
					// What the comment of MediaOptions.Uploads says: os.DirFS cannot keep the link in a directory from
					// leading out of the copy.
					if err != nil {
						t.Fatalf("os.DirFS: %v, want the file of the other directory", err)
					}
					got, _ := io.ReadAll(f)
					_ = f.Close()
					if string(got) != string(c.secret) {
						t.Errorf("file = %d bytes, want the file of the other directory", len(got))
					}
					return
				}
				if f != nil {
					_ = f.Close()
					t.Errorf("file = %v, want none", f)
				}
				wantStopping(t, err)
				if !strings.HasPrefix(err.Error(), "reading the copy of the uploads: ") {
					t.Errorf("error = %v, want it to start with the reading", err)
				}
			})
		})
	}
}

// TestMediaCopyOnDisk checks that a file of the copy that is a link or a named pipe costs only its own attachment, and
// that nothing of what a link leads to is imported.
func TestMediaCopyOnDisk(t *testing.T) {
	for _, kind := range []string{"DirFS", "OpenRoot"} {
		t.Run(kind, func(t *testing.T) {
			c := newDiskCopy(t)
			imp := newPlannedImport(t, uploadExport("https://example.com",
				uploadItem(1, "out.png", ""),
				uploadItem(2, "a.png", ""),
				uploadItem(3, "pipe.png", ""),
				uploadItem(4, "in.png", ""),
				uploadItem(5, "gone.png", ""),
				uploadItem(6, "dir.png", ""),
			))
			dir := filesDir(t)
			opts := MediaOptions{Files: dir, Uploads: c.fsys(t, kind), By: mediaTestBy, Workers: 3}
			var docs []content.Media
			var err error
			c.within(t, func() { docs, err = imp.Media(t.Context(), mediaSite(), opts) })
			if err != nil {
				t.Fatalf("Media: %v", err)
			}
			if len(docs) != 1 || docs[0].Hash != sha256Hash(c.data) || docs[0].FileName != "a.png" {
				t.Errorf("documents = %v, want only a.png", docs)
			}
			wantFiles(t, dir, c.data)
			want := []string{
				`skipped item 1: the attachment is not imported: "out.png": ` + errSymlink,
				`skipped item 3: the attachment is not imported: "pipe.png": ` + errNotRegular,
				`skipped item 4: the attachment is not imported: "in.png": ` + errSymlink,
				`skipped item 5: the attachment is not imported: "gone.png": ` + errSymlink,
				`skipped item 6: the attachment is not imported: "dir.png": ` + errNotRegular,
			}
			if got := entryLines(imp.Report.Entries()); !slices.Equal(got, want) {
				t.Errorf("report:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
			}
		})
	}
}

// TestMediaCopyOnDiskStops checks that a file that cannot be read stops the import.
func TestMediaCopyOnDiskStops(t *testing.T) {
	skipIfRoot(t)
	for _, kind := range []string{"DirFS", "OpenRoot"} {
		t.Run(kind, func(t *testing.T) {
			c := newDiskCopy(t)
			imp := newPlannedImport(t, uploadExport("https://example.com", uploadItem(1, "a.png", ""), uploadItem(2, "noperm.png", "")))
			before := mediaState(imp)
			docs, err := imp.Media(t.Context(), mediaSite(), MediaOptions{Files: filesDir(t), Uploads: c.fsys(t, kind), By: mediaTestBy, Workers: 1})
			if docs != nil || !errors.Is(err, fs.ErrPermission) || !strings.HasPrefix(err.Error(), "reading the copy of the uploads: ") {
				t.Errorf("Media = %v, %v; want an error that wraps fs.ErrPermission, after the reading", docs, err)
			}
			if got := mediaState(imp); got != before {
				t.Errorf("the table and the references are\n%s\nwant\n%s", got, before)
			}
			if got := entryLines(imp.Report.Entries()); len(got) != 0 {
				t.Errorf("report = %q, want none", got)
			}
		})
	}
}
