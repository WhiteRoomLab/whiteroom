package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"testing/iotest"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

// zeros reads zero bytes without end.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// readerFunc is an io.Reader that calls itself to read.
type readerFunc func(p []byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// originalName returns the name of the file of the original whose hash is hash in errors: files/<2 hex>/<64 hex>.
func originalName(hash string) string {
	digits := strings.TrimPrefix(hash, "sha256:")
	return "files/" + digits[:2] + "/" + digits
}

// originalPath returns the path of the file of the original whose hash is hash in the data directory dir.
func originalPath(dir, hash string) string {
	return filepath.Join(dir, filepath.FromSlash(originalName(hash)))
}

// putOriginal saves data as an original of s, and checks that PutOriginal returns its hash and size.
func putOriginal(t *testing.T, s *Store, data []byte) {
	t.Helper()
	want := Original{Hash: sum(data), Size: int64(len(data))}
	if o, err := s.PutOriginal(t.Context(), bytes.NewReader(data)); err != nil || o != want {
		t.Fatalf("PutOriginal() = %+v, %v; want %+v", o, err, want)
	}
}

// readOriginal reads the original whose hash is hash from OpenOriginal to its end. It checks that Stat has the size of
// what it reads, and that the file cannot be read without Read.
func readOriginal(t *testing.T, s *Store, hash string) ([]byte, error) {
	t.Helper()
	f, err := s.OpenOriginal(hash)
	if err != nil {
		t.Fatalf("OpenOriginal() = %v", err)
	}
	defer func() { _ = f.Close() }()
	switch f.(type) {
	case io.WriterTo, io.ReaderAt, io.Seeker:
		t.Errorf("OpenOriginal() = %T, which can read without Read", f)
	}
	info, err := f.Stat()
	if err != nil {
		t.Fatalf("Stat() of OpenOriginal() = %v", err)
	}
	data, err := io.ReadAll(f)
	if int64(len(data)) != info.Size() {
		t.Errorf("OpenOriginal() reads %d bytes, but Stat() has the size %d", len(data), info.Size())
	}
	return data, err
}

// TestOriginals puts every original of the sample site of internal/build twice, and reads them back.
func TestOriginals(t *testing.T) {
	src := filepath.Join("..", "build", "testdata", "site", "files")
	entries, err := os.ReadDir(src)
	if err != nil || len(entries) == 0 {
		t.Fatalf("the originals of the sample site: %d, %v", len(entries), err)
	}
	forBackends(t, oneBackend, func(t *testing.T, tb testBackend) {
		s, dir := newStore(t, tb)
		originals := make(map[string][]byte) // by hash
		dirs := make(map[string][]string)    // the names of the files in each directory of files/
		for _, e := range entries {
			data, err := os.ReadFile(filepath.Join(src, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			want := Original{Hash: "sha256:" + e.Name(), Size: int64(len(data))}
			// The same data twice is the same original, in one file.
			for range 2 {
				if o, err := s.PutOriginal(t.Context(), bytes.NewReader(data)); err != nil || o != want {
					t.Errorf("PutOriginal() of %s = %+v, %v; want %+v", e.Name(), o, err, want)
				}
			}
			originals[want.Hash] = data
			dirs[e.Name()[:2]] = append(dirs[e.Name()[:2]], e.Name())
		}

		files := filepath.Join(dir, "files")
		if got, want := names(t, files), slices.Sorted(maps.Keys(dirs)); !slices.Equal(got, want) {
			t.Errorf("files/ has %q, want %q", got, want)
		}
		for d, want := range dirs {
			p := filepath.Join(files, d)
			if got := modeOf(t, p); got != fs.ModeDir|0o700 {
				t.Errorf("mode of %s = %v, want %v", p, got, fs.ModeDir|0o700)
			}
			if got := names(t, p); !slices.Equal(got, slices.Sorted(slices.Values(want))) {
				t.Errorf("%s has %q, want %q", p, got, want)
			}
		}
		for hash, want := range originals {
			if p := originalPath(dir, hash); modeOf(t, p) != 0o600 {
				t.Errorf("mode of %s = %v, want %v", p, modeOf(t, p), fs.FileMode(0o600))
			}
			if data, err := readOriginal(t, s, hash); err != nil || !bytes.Equal(data, want) {
				t.Errorf("OpenOriginal(%q) reads %d bytes, %v; want the %d bytes of the original", hash, len(data), err,
					len(want))
			}
		}
	})
}

// TestLargestOriginal puts and reads an original of content.MaxMediaBytes, and reads it again while its file grows.
func TestLargestOriginal(t *testing.T) {
	forBackends(t, oneBackend, func(t *testing.T, tb testBackend) {
		s, dir := newStore(t, tb)
		h := sha256.New()
		if _, err := io.Copy(h, io.LimitReader(zeros{}, content.MaxMediaBytes)); err != nil {
			t.Fatal(err)
		}
		want := Original{Hash: fmt.Sprintf("sha256:%x", h.Sum(nil)), Size: content.MaxMediaBytes}
		if o, err := s.PutOriginal(t.Context(), io.LimitReader(zeros{}, content.MaxMediaBytes)); err != nil ||
			o != want {
			t.Fatalf("PutOriginal() = %+v, %v; want %+v", o, err, want)
		}
		open := func() fs.File {
			t.Helper()
			f, err := s.OpenOriginal(want.Hash)
			if err != nil {
				t.Fatalf("OpenOriginal() = %v", err)
			}
			t.Cleanup(func() { _ = f.Close() })
			return f
		}
		if n, err := io.Copy(io.Discard, open()); err != nil || n != content.MaxMediaBytes {
			t.Errorf("OpenOriginal() reads %d bytes, %v; want %d", n, err, content.MaxMediaBytes)
		}

		f := open()
		// More than a byte, so that the error tells where Read stops.
		if err := os.Truncate(originalPath(dir, want.Hash), content.MaxMediaBytes+1<<20); err != nil {
			t.Fatal(err)
		}
		n, err := io.Copy(io.Discard, f)
		if msg := originalName(want.Hash) + ": too large: at least 67108865 bytes (max 67108864)"; err == nil ||
			err.Error() != msg {
			t.Errorf("OpenOriginal() of a file that grows reads %d bytes, %v; want an error %q", n, err, msg)
		}
	})
}

func TestPutOriginalErrors(t *testing.T) {
	forBackends(t, oneBackend, func(t *testing.T, tb testBackend) {
		s, dir := newStore(t, tb)
		errTest := errors.New("test")
		canceled, cancel := context.WithCancel(t.Context())
		cancel()
		reading, stop := context.WithCancel(t.Context())
		t.Cleanup(stop)
		stopping := readerFunc(func(p []byte) (int, error) {
			stop()
			return copy(p, "GIF89a"), nil
		})
		for _, tt := range []struct {
			name string
			ctx  context.Context
			r    io.Reader
			want string
			is   error // that the error wraps, if not nil
		}{
			{name: "empty", ctx: t.Context(), r: strings.NewReader(""), want: "the original is empty"},
			{name: "too large", ctx: t.Context(), r: io.LimitReader(zeros{}, content.MaxMediaBytes+1),
				want: "the original is larger than 67108864 bytes"},
			{name: "canceled", ctx: canceled, r: strings.NewReader("GIF89a"), want: "context canceled",
				is: context.Canceled},
			{name: "canceled while reading", ctx: reading, r: stopping, want: "context canceled", is: context.Canceled},
			{name: "failing reader", ctx: t.Context(),
				r: io.MultiReader(strings.NewReader("GIF89a"), iotest.ErrReader(errTest)), want: "test", is: errTest},
		} {
			t.Run(tt.name, func(t *testing.T) {
				o, err := s.PutOriginal(tt.ctx, tt.r)
				if o != (Original{}) || err == nil || err.Error() != tt.want || tt.is != nil && !errors.Is(err, tt.is) {
					t.Errorf("PutOriginal() = %+v, %v; want an error %q", o, err, tt.want)
				}
				if got := names(t, filepath.Join(dir, "files")); got != nil {
					t.Errorf("files/ has %q after PutOriginal(), want nothing", got)
				}
			})
		}
	})
}

// TestPutOriginalRenameError checks that PutOriginal removes its temporary file when it cannot rename it.
func TestPutOriginalRenameError(t *testing.T) {
	forBackends(t, oneBackend, func(t *testing.T, tb testBackend) {
		s, dir := newStore(t, tb)
		data := []byte("GIF89a")
		d := filepath.Dir(originalPath(dir, sum(data)))
		if err := os.WriteFile(d, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if o, err := s.PutOriginal(t.Context(), bytes.NewReader(data)); err == nil {
			t.Errorf("PutOriginal() with a file as the directory of the original = %+v, want an error", o)
		}
		if got, want := names(t, filepath.Join(dir, "files")), []string{filepath.Base(d)}; !slices.Equal(got, want) {
			t.Errorf("files/ has %q, want %q", got, want)
		}
	})
}

// TestFilesGone checks that PutOriginal fails with the error of the file system when files/ is gone, or its temporary file
// is: the text of the error is kept, but it does not wrap fs.ErrNotExist, which is for a document or an original that is not
// there.
func TestFilesGone(t *testing.T) {
	data := []byte("GIF89a")
	fault := func(t *testing.T, what string, err error) {
		t.Helper()
		switch {
		case err == nil:
			t.Errorf("%s = nil, want an error", what)
		case errors.Is(err, fs.ErrNotExist):
			t.Errorf("%s = %q, which wraps fs.ErrNotExist", what, err)
		case !strings.Contains(err.Error(), syscall.ENOENT.Error()):
			t.Errorf("%s = %q, want the error of the file system in it", what, err)
		}
	}
	forBackends(t, oneBackend, func(t *testing.T, tb testBackend) {
		t.Run("files/ is gone", func(t *testing.T) {
			s, dir := newStore(t, tb)
			if err := os.RemoveAll(filepath.Join(dir, "files")); err != nil {
				t.Fatal(err)
			}
			o, err := s.PutOriginal(t.Context(), bytes.NewReader(data))
			fault(t, "PutOriginal()", err)
			if o != (Original{}) {
				t.Errorf("PutOriginal() = %+v, want none", o)
			}
			// An original that is not there is not there, as before.
			if _, err := s.OpenOriginal(sum(data)); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("OpenOriginal() of an original that is not there = %v, want an error that wraps fs.ErrNotExist", err)
			}
		})
		for _, tt := range []struct {
			name string
			gone func(t *testing.T, dir string)
		}{
			{"files/ is gone after the temporary file is written", func(t *testing.T, dir string) {
				if err := os.RemoveAll(filepath.Join(dir, "files")); err != nil {
					t.Fatal(err)
				}
			}},
			{"the temporary file is gone after it is written", func(t *testing.T, dir string) {
				for _, name := range names(t, filepath.Join(dir, "files")) {
					if tempName.MatchString(name) {
						if err := os.Remove(filepath.Join(dir, "files", name)); err != nil {
							t.Fatal(err)
						}
					}
				}
			}},
		} {
			t.Run(tt.name, func(t *testing.T) {
				s, dir := newStore(t, tb)
				s.hook = func(point string) error {
					if point == "original:synced" {
						tt.gone(t, dir)
					}
					return nil
				}
				o, err := s.PutOriginal(t.Context(), bytes.NewReader(data))
				fault(t, "PutOriginal()", err)
				if o != (Original{}) {
					t.Errorf("PutOriginal() = %+v, want none", o)
				}
			})
		}
	})
}

// TestFilesGoneAtTheSync checks that a sync of the directory of an original that fails because the directory is gone stops
// the saving of originals, as any failed sync does, and that its error keeps the text of the file system and does not wrap
// fs.ErrNotExist.
func TestFilesGoneAtTheSync(t *testing.T) {
	fsys := simfs.New("data")
	s, err := create("data", options{log: &logdb.Options{}, fs: fsys})
	if err != nil {
		t.Fatalf("create() = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	fsys.Inject(simfs.Fault{Match: func(op simfs.Op) bool {
		return op.Kind == simfs.OpSyncDir && strings.HasPrefix(op.Path, "files/")
	}, Err: syscall.ENOENT, Times: 1})
	o, err := s.PutOriginal(t.Context(), strings.NewReader("GIF89a"))
	switch {
	case err == nil:
		t.Errorf("PutOriginal() = %+v, nil, want an error", o)
	case !errors.Is(err, errOriginalsStopped):
		t.Errorf("PutOriginal() = %q, want an error that wraps %q", err, errOriginalsStopped)
	case errors.Is(err, fs.ErrNotExist):
		t.Errorf("PutOriginal() = %q, which wraps fs.ErrNotExist", err)
	case !strings.Contains(err.Error(), syscall.ENOENT.Error()):
		t.Errorf("PutOriginal() = %q, want the error of the file system in it", err)
	}
}

// TestOriginalDirDurable checks that PutOriginal syncs files/ also when the directory of the original is there, but
// its name may not be durable yet, as when a process that was killed has made it. On simfs, a crash keeps only what
// has been synced. After a failed sync, PutOriginal fails (TestOriginalAfterFailedSync).
func TestOriginalDirDurable(t *testing.T) {
	data := []byte("GIF89a")
	fsys := simfs.New("data")
	if err := fsys.Mkdir("files", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := fsys.SyncDir("."); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Mkdir(path.Dir(originalName(sum(data))), 0o700); err != nil {
		t.Fatal(err)
	}
	files, err := fsys.Sub("files")
	if err != nil {
		t.Fatal(err)
	}
	putOriginal(t, &Store{files: files}, data)
	files, err = fsys.Crash(simfs.Model{Name: "lose-all"}).Sub("files")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := readOriginal(t, &Store{files: files}, sum(data)); err != nil || !bytes.Equal(got, data) {
		t.Errorf("the original after a crash = %q, %v; want %q", got, err, data)
	}
}

func TestOpenOriginalErrors(t *testing.T) {
	forBackends(t, oneBackend, func(t *testing.T, tb testBackend) {
		data := []byte("GIF89a")
		hash := sum(data)
		digits := strings.TrimPrefix(hash, "sha256:")
		s, _ := newStore(t, tb)
		putOriginal(t, s, data)
		use := `: use "sha256:" and 64 lower case hex digits`
		for _, h := range []string{
			"sha256:" + strings.ToUpper(digits),
			digits,
			"sha256:" + digits[:63],
			"sha256:" + digits + "0",
			"sha512:" + digits,
			"SHA256:" + digits,
			"sha256:../" + digits[3:],
			"",
		} {
			f, err := s.OpenOriginal(h)
			if want := `invalid hash "` + h + `"` + use; f != nil || err == nil || err.Error() != want {
				t.Errorf("OpenOriginal(%q) = %v, %v; want an error %q", h, f, err, want)
			}
		}

		for _, tt := range []struct {
			name     string
			setup    func(t *testing.T, s *Store, path string) // path is that of the file of the original of data
			want     string                                    // after the name of the file
			notExist bool
		}{
			{name: "missing", setup: func(*testing.T, *Store, string) {}, want: "not found", notExist: true},
			{
				name:     "missing from its directory",
				setup:    func(t *testing.T, _ *Store, path string) { mkdir(t, filepath.Dir(path)) },
				want:     "not found",
				notExist: true,
			},
			{
				name: "symbolic link",
				setup: func(t *testing.T, s *Store, path string) {
					putOriginal(t, s, data)
					rename(t, path, path+".old")
					symlink(t, filepath.Base(path)+".old", path)
				},
				want: "symbolic links are not allowed",
			},
			{
				name: "directory",
				setup: func(t *testing.T, _ *Store, path string) {
					mkdir(t, filepath.Dir(path))
					mkdir(t, path)
				},
				want: "must be a regular file",
			},
			{
				name: "too large",
				setup: func(t *testing.T, s *Store, path string) {
					putOriginal(t, s, data)
					if err := os.Truncate(path, content.MaxMediaBytes+1); err != nil {
						t.Fatal(err)
					}
				},
				want: "too large: 67108865 bytes (max 67108864)",
			},
			{
				name: "unreadable",
				setup: func(t *testing.T, s *Store, path string) {
					if os.Geteuid() == 0 {
						t.Skip("every file is readable") // to root
					}
					putOriginal(t, s, data)
					if err := os.Chmod(path, 0); err != nil {
						t.Fatal(err)
					}
				},
				want: "cannot read: permission denied",
			},
			{
				name: "file as its directory",
				setup: func(t *testing.T, _ *Store, path string) {
					if err := os.WriteFile(filepath.Dir(path), nil, 0o600); err != nil {
						t.Fatal(err)
					}
				},
				want: "cannot read: not a directory",
			},
			{
				name: "its directory out of files",
				setup: func(t *testing.T, _ *Store, path string) {
					out := t.TempDir()
					if err := os.WriteFile(filepath.Join(out, filepath.Base(path)), data, 0o600); err != nil {
						t.Fatal(err)
					}
					symlink(t, out, filepath.Dir(path))
				},
				want: "cannot read: path escapes from parent",
			},
		} {
			t.Run(tt.name, func(t *testing.T) {
				s, dir := newStore(t, tb)
				tt.setup(t, s, originalPath(dir, hash))
				f, err := s.OpenOriginal(hash)
				if f != nil {
					_ = f.Close()
				}
				if want := originalName(hash) + ": " + tt.want; f != nil || err == nil || err.Error() != want ||
					errors.Is(err, fs.ErrNotExist) != tt.notExist {
					t.Errorf("OpenOriginal() = %v, %v; want an error %q", f, err, want)
				}
			})
		}
	})
}

// TestDamagedOriginal checks that reading an original whose file has changed fails at its end, and that PutOriginal
// repairs it.
func TestDamagedOriginal(t *testing.T) {
	forBackends(t, oneBackend, func(t *testing.T, tb testBackend) {
		s, dir := newStore(t, tb)
		data, damaged := []byte("GIF89a"), []byte("GIF87a")
		hash := sum(data)
		putOriginal(t, s, data)
		if err := os.WriteFile(originalPath(dir, hash), damaged, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := readOriginal(t, s, hash)
		if want := "original " + hash + ": the data has the hash " + sum(damaged); !bytes.Equal(got, damaged) ||
			err == nil || err.Error() != want {
			t.Errorf("OpenOriginal() of a damaged original reads %q, %v; want %q and an error %q", got, err, damaged,
				want)
		}
		putOriginal(t, s, data)
		if got, err := readOriginal(t, s, hash); err != nil || !bytes.Equal(got, data) {
			t.Errorf("OpenOriginal() after PutOriginal() reads %q, %v; want %q", got, err, data)
		}
	})
}

// tempName matches the name of a temporary file of the store in files/.
var tempName = regexp.MustCompile(`^\.tmp-[0-9a-f]{16}$`)

// TestOpenRemovesTemps checks that the writer removes, when it opens the store, the regular files of files/ that are
// named like a temporary file, and nothing else: no file with another name, no directory, no symbolic link, nothing in
// a directory of files/; and that a reader removes nothing.
func TestOpenRemovesTemps(t *testing.T) {
	forBackends(t, oneBackend, func(t *testing.T, tb testBackend) {
		s, dir := newStore(t, tb)
		original := []byte("GIF89a")
		putOriginal(t, s, original)
		if err := s.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}
		files := filepath.Join(dir, "files")
		originalDir := filepath.Join(files, strings.TrimPrefix(sum(original), "sha256:")[:2])
		write := func(path, data string) {
			t.Helper()
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		temps := []string{".tmp-0123456789abcdef", ".tmp-fedcba9876543210", ".tmp-0000000000000000"}
		for _, name := range temps {
			write(filepath.Join(files, name), "half of "+name)
		}
		// Not named like a temporary file: the prefix alone, other lengths, upper case, a digit that is not hexadecimal,
		// no dot, a suffix.
		kept := []string{
			".tmp-", ".tmp-x", ".tmp-0123456789abcde", ".tmp-0123456789abcdef0", ".tmp-0123456789ABCDEF",
			".tmp-0123456789abcdeg", "tmp-0123456789abcdef", ".tmp-0123456789abcdef.old", "notes",
		}
		for _, name := range kept {
			write(filepath.Join(files, name), "keep "+name)
		}
		// A directory with the name of a temporary file, and files in it and in the directory of the original with that
		// name: only the files of files/ itself are temporary files.
		mkdir(t, filepath.Join(files, ".tmp-aaaaaaaaaaaaaaaa"))
		write(filepath.Join(files, ".tmp-aaaaaaaaaaaaaaaa", ".tmp-0123456789abcdef"), "in a directory")
		write(filepath.Join(originalDir, ".tmp-0123456789abcdef"), "beside an original")
		target := filepath.Join(t.TempDir(), "target")
		write(target, "the target of a link")
		symlink(t, target, filepath.Join(files, ".tmp-bbbbbbbbbbbbbbbb"))
		symlink(t, filepath.Join(t.TempDir(), "missing"), filepath.Join(files, ".tmp-cccccccccccccccc"))

		before := names(t, files)
		// A reader removes nothing.
		r, err := openLogReader(dir, tb.options())
		if err != nil {
			t.Fatalf("openLogReader() = %v", err)
		}
		if got := names(t, files); !slices.Equal(got, before) {
			t.Errorf("files/ has %q after openLogReader(), want %q", got, before)
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}

		s = reopen(t, tb, dir)
		want := slices.DeleteFunc(slices.Clone(before), func(name string) bool { return slices.Contains(temps, name) })
		if got := names(t, files); !slices.Equal(got, want) {
			t.Errorf("files/ has %q after open(), want %q: the temporary files removed, the rest kept", got, want)
		}
		for _, name := range kept {
			if data, err := os.ReadFile(filepath.Join(files, name)); err != nil || string(data) != "keep "+name {
				t.Errorf("%s after open() reads %q, %v; want it unchanged", name, data, err)
			}
		}
		for path, want := range map[string]string{
			filepath.Join(files, ".tmp-aaaaaaaaaaaaaaaa", ".tmp-0123456789abcdef"): "in a directory",
			filepath.Join(originalDir, ".tmp-0123456789abcdef"):                    "beside an original",
			target: "the target of a link",
		} {
			if data, err := os.ReadFile(path); err != nil || string(data) != want {
				t.Errorf("%s after open() reads %q, %v; want %q", path, data, err, want)
			}
		}
		for _, name := range []string{".tmp-bbbbbbbbbbbbbbbb", ".tmp-cccccccccccccccc"} {
			if info, err := os.Lstat(filepath.Join(files, name)); err != nil || info.Mode()&fs.ModeSymlink == 0 {
				t.Errorf("%s after open() = %v, %v; want the symbolic link", name, info, err)
			}
		}
		if data, err := readOriginal(t, s, sum(original)); err != nil || !bytes.Equal(data, original) {
			t.Errorf("the original after open() reads %q, %v; want %q", data, err, original)
		}

		// A store opened again, with nothing to remove, is opened as it is.
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		reopen(t, tb, dir)
		if got := names(t, files); !slices.Equal(got, want) {
			t.Errorf("files/ has %q after the second open(), want %q", got, want)
		}
	})
}

// TestOpenAfterKilledTemp checks that a temporary file that a killed PutOriginal has left stays when the store is closed, and
// that the writer removes it when it opens the store.
func TestOpenAfterKilledTemp(t *testing.T) {
	forBackends(t, oneBackend, func(t *testing.T, tb testBackend) {
		s, dir := newStore(t, tb)
		files := filepath.Join(dir, "files")
		if err := os.WriteFile(filepath.Join(files, ".tmp-0123456789abcdef"), []byte("half"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}
		if got := names(t, files); len(got) != 1 || !tempName.MatchString(got[0]) {
			t.Fatalf("files/ has %q after the store is closed, want the temporary file alone", got)
		}
		reopen(t, tb, dir)
		if got := names(t, files); got != nil {
			t.Errorf("files/ has %q after open(), want nothing", got)
		}
	})
}

// TestOpenTempsErrors checks that open fails with an error that names the file when it cannot remove a temporary file,
// and with one that names files/ when it cannot read it, and that it changes nothing then: the files are as they were,
// also for a store of an older table schema, which the failed open does not upgrade; and that the next open works.
func TestOpenTempsErrors(t *testing.T) {
	const temp = ".tmp-0123456789abcdef"
	// snapshot returns the files of fsys with their data, but for the head file, which the engine writes at each open.
	snapshot := func(fsys *simfs.FS) map[string]string {
		m := map[string]string{}
		fsys.Walk(func(path string, data []byte) {
			if path != "logdb/head" {
				m[path] = string(data)
			}
		})
		return m
	}
	removeTemp := func(op simfs.Op) bool { return op.Kind == simfs.OpRemove && op.Path == "files/"+temp }
	for _, tt := range []struct {
		name   string
		schema byte // the table schema that the store has, if not the current one
		match  func(op simfs.Op) bool
		want   string
	}{
		{"remove", 0, removeTemp, "data/files/" + temp + ": cannot remove: input/output error"},
		{"remove, schema 5", 5, removeTemp, "data/files/" + temp + ": cannot remove: input/output error"},
		{"remove, schema 6", 6, removeTemp, "data/files/" + temp + ": cannot remove: input/output error"},
		{"read", 0, func(op simfs.Op) bool { return op.Kind == simfs.OpReadDir && op.Path == "files" },
			"data/files: cannot read: input/output error"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fsys := simfs.New("data")
			o := options{log: &logdb.Options{}, fs: fsys}
			s, err := create("data", o)
			if err != nil {
				t.Fatalf("create() = %v", err)
			}
			if tt.schema != 0 {
				rawUpdate(t, s, func(tx *logdb.Tx) error { return tx.Put(tableMeta, []byte(metaSchema), []byte{tt.schema}) })
			}
			if err := s.Close(); err != nil {
				t.Fatalf("Close() = %v", err)
			}
			filesFS, err := fsys.Sub("files")
			if err != nil {
				t.Fatal(err)
			}
			f, err := filesFS.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write([]byte("half an original")); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			before := snapshot(fsys)
			fsys.Inject(simfs.Fault{Match: tt.match, Err: syscall.EIO, Times: 1})

			s, err = open("data", o)
			if err == nil || err.Error() != tt.want {
				if s != nil {
					_ = s.Close()
				}
				t.Fatalf("open() = %v, want an error %q", err, tt.want)
			}
			if s != nil {
				t.Errorf("open() that fails returns a store")
			}
			if after := snapshot(fsys); !maps.Equal(after, before) {
				t.Errorf("the files after the open() that fails are not those from before it:\nbefore %q\nafter  %q", before,
					after)
			}

			// The failed open has let the lock go: the next one works, and removes the file.
			s, err = open("data", o)
			if err != nil {
				t.Fatalf("open() after the failure = %v", err)
			}
			t.Cleanup(func() { _ = s.Close() })
			if got := tempNames(fsys); got != nil {
				t.Errorf("the file system has the temporary files %q after the open(), want none", got)
			}
			view(t, s, func(tx *Tx) error { return nil })
		})
	}
}

// TestOpenTempPermission checks the error of open for a temporary file that the OS refuses to remove, and that the open
// works once the directory can be written again.
func TestOpenTempPermission(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes the files of a directory that it cannot write")
	}
	forBackends(t, oneBackend, func(t *testing.T, tb testBackend) {
		dir := newDir(t, tb)
		files := filepath.Join(dir, "files")
		temp := filepath.Join(files, ".tmp-0123456789abcdef")
		if err := os.WriteFile(temp, []byte("half"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(files, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(files, 0o700) })
		s, err := tb.open(dir)
		if want := temp + ": cannot remove: permission denied"; err == nil || err.Error() != want {
			if s != nil {
				_ = s.Close()
			}
			t.Fatalf("open() = %v, want an error %q", err, want)
		}
		if err := os.Chmod(files, 0o700); err != nil {
			t.Fatal(err)
		}
		reopen(t, tb, dir)
		if got := names(t, files); got != nil {
			t.Errorf("files/ has %q after open(), want nothing", got)
		}
	})
}
