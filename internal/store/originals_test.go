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
	"sync/atomic"
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

// The methods of a TempFile are those of the interfaces that its callers take it as.
var (
	_ io.ReadWriteCloser = (*TempFile)(nil)
	_ io.Seeker          = (*TempFile)(nil)
)

// TestCreateTemp checks CreateTemp: a new empty file in files/ with the name of the temporary file of PutOriginal and the
// mode 0600, to write, to read after a Seek, and to close, which removes it.
func TestCreateTemp(t *testing.T) {
	forBackends(t, oneBackend, func(t *testing.T, tb testBackend) {
		s, dir := newStore(t, tb)
		files := filepath.Join(dir, "files")
		f, err := s.CreateTemp()
		if err != nil {
			t.Fatalf("CreateTemp() = %v", err)
		}
		got := names(t, files)
		if len(got) != 1 || !tempName.MatchString(got[0]) {
			t.Fatalf("files/ has %q, want a temporary file alone", got)
		}
		p := filepath.Join(files, got[0])
		if mode := modeOf(t, p); mode != 0o600 {
			t.Errorf("mode of %s = %v, want %v", p, mode, fs.FileMode(0o600))
		}
		if info, err := os.Stat(p); err != nil || info.Size() != 0 {
			t.Errorf("the new temporary file has the size %v, %v; want it empty", info, err)
		}

		want := bytes.Repeat([]byte("temporary "), 100_000)
		if n, err := f.Write(want); err != nil || n != len(want) {
			t.Fatalf("Write() = %d, %v; want %d bytes", n, err, len(want))
		}
		if off, err := f.Seek(0, io.SeekStart); err != nil || off != 0 {
			t.Fatalf("Seek() to the start = %d, %v", off, err)
		}
		if data, err := io.ReadAll(f); err != nil || !bytes.Equal(data, want) {
			t.Errorf("the temporary file reads %d bytes, %v; want the %d bytes that were written", len(data), err, len(want))
		}
		if off, err := f.Seek(-4, io.SeekEnd); err != nil || off != int64(len(want)-4) {
			t.Fatalf("Seek() to 4 bytes before the end = %d, %v", off, err)
		}
		if data, err := io.ReadAll(f); err != nil || string(data) != "ary " {
			t.Errorf("the temporary file reads %q, %v from 4 bytes before the end; want %q", data, err, "ary ")
		}
		// Written again from the start, it is overwritten, and the file has the size of the longer.
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte("TEMPORARY")); err != nil {
			t.Fatal(err)
		}
		if disk, err := os.ReadFile(p); err != nil || !bytes.HasPrefix(disk, []byte("TEMPORARY temporary ")) ||
			len(disk) != len(want) {
			t.Errorf("the file on the disk has %d bytes, %v, and starts with %.20q; want %d bytes", len(disk), err, disk,
				len(want))
		}

		// Another has a name of its own, and closing one removes it alone.
		g, err := s.CreateTemp()
		if err != nil {
			t.Fatalf("CreateTemp() = %v", err)
		}
		if two := names(t, files); len(two) != 2 || two[0] == two[1] {
			t.Fatalf("files/ has %q, want two temporary files", two)
		}
		if err := f.Close(); err != nil {
			t.Errorf("Close() = %v", err)
		}
		if left := names(t, files); len(left) != 1 || left[0] == got[0] {
			t.Errorf("files/ has %q after Close(), want the other temporary file alone", left)
		}
		if err := f.Close(); err != nil {
			t.Errorf("the second Close() = %v, want nil", err)
		}
		if _, err := f.Write([]byte("x")); err == nil {
			t.Errorf("Write() after Close() = nil, want an error")
		}
		if left := names(t, files); len(left) != 1 {
			t.Errorf("files/ has %q after the second Close(), want the other temporary file alone", left)
		}
		if err := g.Close(); err != nil {
			t.Errorf("Close() = %v", err)
		}
		if left := names(t, files); left != nil {
			t.Errorf("files/ has %q after the Close() of both, want nothing", left)
		}
	})
}

// TestCreateTempReader checks that a store that is opened to read only makes no temporary file.
func TestCreateTempReader(t *testing.T) {
	forBackends(t, oneBackend, func(t *testing.T, tb testBackend) {
		_, dir := newStore(t, tb)
		r, err := openLogReader(dir, tb.options())
		if err != nil {
			t.Fatalf("openLogReader() = %v", err)
		}
		t.Cleanup(func() { _ = r.Close() })
		f, err := r.CreateTemp()
		if f != nil || err == nil || err.Error() != "read-only transaction" {
			t.Errorf("CreateTemp() of a reader = %v, %v; want an error %q", f, err, "read-only transaction")
		}
		if got := names(t, filepath.Join(dir, "files")); got != nil {
			t.Errorf("files/ has %q after the CreateTemp() of a reader, want nothing", got)
		}
	})
}

// TestCreateTempNoSync checks that a temporary file is not synced, in the file or in files/, as it is not kept; and
// that a Close that cannot remove the file says so, and the next Open removes it.
func TestCreateTempNoSync(t *testing.T) {
	fsys := simfs.New("data")
	o := options{log: &logdb.Options{}, fs: fsys}
	s, err := create("data", o)
	if err != nil {
		t.Fatalf("create() = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	var syncs atomic.Int64
	fsys.Inject(simfs.Fault{Match: func(op simfs.Op) bool {
		if op.Kind == simfs.OpSync || op.Kind == simfs.OpSyncDir {
			syncs.Add(1)
		}
		return false
	}, Err: syscall.EIO})
	f, err := s.CreateTemp()
	if err != nil {
		t.Fatalf("CreateTemp() = %v", err)
	}
	if _, err := f.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if temps := tempNames(fsys); len(temps) != 1 {
		t.Errorf("the file system has the temporary files %q, want one", temps)
	}
	if err := f.Close(); err != nil {
		t.Errorf("Close() = %v", err)
	}
	if n := syncs.Load(); n != 0 {
		t.Errorf("CreateTemp(), Write() and Close() have synced %d times, want never", n)
	}
	if temps := tempNames(fsys); temps != nil {
		t.Errorf("the file system has the temporary files %q after Close(), want none", temps)
	}

	// A Close that cannot remove the file closes it, returns the error, and leaves the file to the next Open.
	g, err := s.CreateTemp()
	if err != nil {
		t.Fatalf("CreateTemp() = %v", err)
	}
	temps := tempNames(fsys)
	if len(temps) != 1 {
		t.Fatalf("the file system has the temporary files %q, want one", temps)
	}
	fsys.Inject(simfs.Fault{Match: func(op simfs.Op) bool { return op.Kind == simfs.OpRemove && op.Path == temps[0] },
		Err: syscall.EIO, Times: 1})
	if err := g.Close(); !errors.Is(err, syscall.EIO) {
		t.Errorf("Close() that cannot remove the file = %v, want an error that wraps EIO", err)
	}
	if err := g.Close(); err != nil {
		t.Errorf("the second Close() = %v, want nil", err)
	}
	if got := tempNames(fsys); !slices.Equal(got, temps) {
		t.Errorf("the file system has the temporary files %q, want %q: the file stays", got, temps)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() of the store = %v", err)
	}
	r, err := open("data", o)
	if err != nil {
		t.Fatalf("open() = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if got := tempNames(fsys); got != nil {
		t.Errorf("the file system has the temporary files %q after open(), want none", got)
	}
}

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

// TestOpenAfterKilledTemp checks that a temporary file that CreateTemp made stays when the store is closed without it,
// and that the writer removes it when it opens the store.
func TestOpenAfterKilledTemp(t *testing.T) {
	forBackends(t, oneBackend, func(t *testing.T, tb testBackend) {
		s, dir := newStore(t, tb)
		f, err := s.CreateTemp()
		if err != nil {
			t.Fatalf("CreateTemp() = %v", err)
		}
		if _, err := f.Write([]byte("half")); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}
		files := filepath.Join(dir, "files")
		if got := names(t, files); len(got) != 1 || !tempName.MatchString(got[0]) {
			t.Fatalf("files/ has %q after the store is closed, want the temporary file alone", got)
		}
		reopen(t, tb, dir)
		if got := names(t, files); got != nil {
			t.Errorf("files/ has %q after open(), want nothing", got)
		}
		// The file that was open is closed late: its Close finds the file removed or the store closed, and leaves nothing.
		_ = f.Close()
		if got := names(t, files); got != nil {
			t.Errorf("files/ has %q after the late Close(), want nothing", got)
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
