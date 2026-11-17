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
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
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
	f, err := s.OpenOriginal(context.Background(), hash)
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

// TestLargestOriginal puts and reads an original of content.MaxMediaBytes, and reads it again while its file grows: the
// daemon sends the size that it found when it opened the file, and ends the reply with an error if the file has more.
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
			f, err := s.OpenOriginal(context.Background(), want.Hash)
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
		if err := os.Truncate(originalPath(dir, want.Hash), content.MaxMediaBytes+1<<20); err != nil {
			t.Fatal(err)
		}
		n, err := io.Copy(io.Discard, f)
		if msg := originalName(want.Hash) + ": the file is longer than its size"; err == nil || err.Error() != msg {
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
				// The daemon removes the temporary file of a request that ends with the connection a moment later.
				eventually(t, func() bool { return names(t, filepath.Join(dir, "files")) == nil }, "files/ has nothing")
			})
		}
	})
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
			f, err := s.OpenOriginal(context.Background(), h)
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
				f, err := s.OpenOriginal(context.Background(), hash)
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

// TestOriginalLimit checks that the largest original is content.MaxMediaBytes, the limit of the store, also when the limit
// of the daemon is higher: PutOriginal refuses one with a byte more, and OpenOriginal refuses a file with a byte more.
func TestOriginalLimit(t *testing.T) {
	forBackends(t, oneBackend, func(t *testing.T, tb testBackend) {
		o := tb.options()
		o.limits = &dbwire.Limits{Object: 2 * content.MaxMediaBytes, CommitsPerSecond: -1, MinFree: -1, MinFreeInodes: -1}
		dir := filepath.Join(t.TempDir(), "data")
		s, err := create(dir, o)
		if err != nil {
			t.Fatalf("create() = %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		want := fmt.Sprintf("the original is larger than %d bytes", content.MaxMediaBytes)
		if got, err := s.PutOriginal(t.Context(), io.LimitReader(zeros{}, content.MaxMediaBytes+1)); got != (Original{}) ||
			err == nil || err.Error() != want {
			t.Errorf("PutOriginal() of a byte more than the limit = %+v, %v; want an error %q", got, err, want)
		}
		eventually(t, func() bool { return names(t, filepath.Join(dir, "files")) == nil }, "files/ has nothing")

		// A file of the daemon that has a byte more, which the daemon of this limit sends.
		h := sha256.New()
		if _, err := io.Copy(h, io.LimitReader(zeros{}, content.MaxMediaBytes+1)); err != nil {
			t.Fatal(err)
		}
		hash := fmt.Sprintf("sha256:%x", h.Sum(nil))
		path := originalPath(dir, hash)
		mkdir(t, filepath.Dir(path))
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, err = io.Copy(f, io.LimitReader(zeros{}, content.MaxMediaBytes+1))
			err = errors.Join(err, f.Close())
		}
		if err != nil {
			t.Fatal(err)
		}
		file, err := s.OpenOriginal(context.Background(), hash)
		if file != nil {
			_ = file.Close()
		}
		want = fmt.Sprintf("%s: too large: %d bytes (max %d)", originalName(hash), content.MaxMediaBytes+1, content.MaxMediaBytes)
		if err == nil || err.Error() != want {
			t.Errorf("OpenOriginal() of a file of a byte more than the limit = %v, %v; want an error %q", file, err, want)
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
