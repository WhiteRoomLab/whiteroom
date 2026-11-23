package dbserver

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

// names returns the names of the entries of dir, sorted.
func names(t testing.TB, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func modeOf(t testing.TB, name string) fs.FileMode {
	t.Helper()
	info, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode()
}

func symlink(t testing.TB, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		t.Skipf("cannot make symbolic links: %v", err)
	}
}

func mkdir(t testing.TB, name string) {
	t.Helper()
	if err := os.Mkdir(name, 0o700); err != nil {
		t.Fatal(err)
	}
}

// originalName is the name of the file of the original with the hash h in errors: files/<2 hex>/<64 hex>.
func originalName(h [32]byte) string { return originalPath(h) }

// diskPath is the path of the file of the original h in the data directory dir.
func diskPath(dir string, h [32]byte) string {
	return filepath.Join(dir, filepath.FromSlash(originalPath(h)))
}

// withObject sets the limit of an original, for the tests that need a small one.
func withObject(n int64) func(*Options) {
	return func(o *Options) { o.Limits.Object = n }
}

// mustPut puts an original, and checks that the file is there with the data.
func mustPut(t testing.TB, w *client, data []byte) [32]byte {
	t.Helper()
	h, err := w.putOriginal(data)
	if err != nil {
		t.Fatalf("PutOriginal: %v", err)
	}
	if h != sha256.Sum256(data) {
		t.Fatal("the hash is not that of the data")
	}
	return h
}

// TestOriginals puts every original of the sample site of internal/build twice, and reads them back, as a reader too.
func TestOriginals(t *testing.T) {
	src := filepath.Join("..", "build", "testdata", "site", "files")
	entries, err := os.ReadDir(src)
	if err != nil || len(entries) == 0 {
		t.Fatalf("the originals of the sample site: %d, %v", len(entries), err)
	}
	f := newDirFixture(t, nil)
	w, r := f.connect(dbwire.RoleWriter), f.connect(dbwire.RoleReader)
	originals := map[[32]byte][]byte{}
	dirs := map[string][]string{} // the names of the files in each directory of files/
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		// The same data twice is the same original, in one file.
		var h [32]byte
		for range 2 {
			h = mustPut(t, w, data)
		}
		if got := fmt.Sprintf("%x", h); got != e.Name() {
			t.Errorf("the hash of %s is %s", e.Name(), got)
		}
		originals[h] = data
		dirs[e.Name()[:2]] = append(dirs[e.Name()[:2]], e.Name())
	}

	files := filepath.Join(f.dir, "files")
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
	for h, want := range originals {
		if p := diskPath(f.dir, h); modeOf(t, p) != 0o600 {
			t.Errorf("mode of %s = %v, want %v", p, modeOf(t, p), fs.FileMode(0o600))
		}
		for _, c := range []*client{w, r} {
			if got, err := c.original(h); err != nil || !bytes.Equal(got, want) {
				t.Errorf("Original(%x) reads %d bytes, %v; want the %d bytes of the original", h, len(got), err, len(want))
			}
		}
	}
}

// TestLargestOriginal puts and reads an original of exactly Limits.Object, and one byte more is refused.
func TestLargestOriginal(t *testing.T) {
	const limit = 100_000
	f := newDirFixture(t, withObject(limit))
	w := f.connect(dbwire.RoleWriter)
	data := bytes.Repeat([]byte{0x5a}, limit)
	h := mustPut(t, w, data)
	if got, err := w.original(h); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("Original reads %d bytes, %v; want %d", len(got), err, limit)
	}
	_, err := w.putOriginal(append(data, 0))
	re := wantCode(t, err, dbwire.CodeLimit)
	if want := fmt.Sprintf("the original is larger than %d bytes", limit); re.Msg != want {
		t.Errorf("the text is %q, want %q", re.Msg, want)
	}
	w.wantClosed()
	if got := names(t, filepath.Join(f.dir, "files")); len(got) != 1 {
		t.Errorf("files/ has %q, want the directory of the original alone", got)
	}

	// A file that has grown over the limit since it was put is not sent.
	if err := os.Truncate(diskPath(f.dir, h), limit+1<<10); err != nil {
		t.Fatal(err)
	}
	w = f.connect(dbwire.RoleWriter)
	_, err = w.original(h)
	re = wantCode(t, err, dbwire.CodeFailed)
	if want := originalName(h) + ": too large: 101024 bytes (max 100000)"; re.Msg != want {
		t.Errorf("the text is %q, want %q", re.Msg, want)
	}
}

func TestPutOriginalErrors(t *testing.T) {
	const limit = 1000
	f := newFixture(t, withObject(limit))
	w := f.connect(dbwire.RoleWriter)
	before := f.dump()
	good := []byte("GIF89a")
	goodHash := sha256.Sum256(good)
	other := sha256.Sum256([]byte("GIF87a"))
	for _, tc := range []struct {
		name   string
		do     func(w *client) error
		code   dbwire.Code
		text   string
		closed bool
	}{
		{"empty", func(w *client) error { _, err := w.putOriginal(nil); return err }, dbwire.CodeRule,
			"the original is empty", false},
		{"larger than the limit", func(w *client) error { _, err := w.putOriginal(make([]byte, limit+1)); return err },
			dbwire.CodeLimit, "the original is larger than 1000 bytes", true},
		{"a chunk that claims more than the limit", func(w *client) error {
			return w.do(dbwire.KindPutOriginal, func(e *dbwire.Enc) {
				e.U32(1 << 31)
				e.Raw(make([]byte, limit+1))
			}, nil)
		}, dbwire.CodeLimit, "the original is larger than 1000 bytes", true},
		{"another hash", func(w *client) error { return w.putOriginalAs(good, other, int64(len(good))) }, dbwire.CodeHash,
			"the data do not have the hash and the size that were given", false},
		{"another size", func(w *client) error { return w.putOriginalAs(good, goodHash, int64(len(good))+1) }, dbwire.CodeHash,
			"the data do not have the hash and the size that were given", false},
		{"a hash of the empty data", func(w *client) error {
			return w.putOriginalAs(good, sha256.Sum256(nil), 0)
		}, dbwire.CodeHash, "the data do not have the hash and the size that were given", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w = f.connectAfter(w)
			re := wantCode(t, tc.do(w), tc.code)
			if re.Msg != tc.text {
				t.Errorf("the text is %q, want %q", re.Msg, tc.text)
			}
			if tc.closed {
				w.wantClosed()
			}
			// Nothing is left: no temporary file, and no original.
			if got := filesOf(f.fs, "files"); len(got) != 0 {
				t.Errorf("files/ has %q after the refusal, want nothing", slices.Sorted(maps.Keys(got)))
			}
			f.unchanged(before)
			if !tc.closed {
				// The connection goes on.
				mustPut(t, w, good)
				if _, err := f.fs.Lstat(originalPath(goodHash)); err != nil {
					t.Errorf("the original after the refusal: %v", err)
				}
				if err := f.fs.Remove(originalPath(goodHash)); err != nil {
					t.Fatal(err)
				}
			}
		})
	}

	t.Run("a connection that ends in the middle", func(t *testing.T) {
		w := f.connectAfter(w)
		chunk := binaryChunk([]byte("GIF89a"))
		done := w.writeRaw(rawFrame(byte(dbwire.KindPutOriginal), 0, chunk))
		<-done
		eventually(t, "the temporary file", func() bool { return len(tempFiles(f.fs)) == 1 })
		_ = w.conn.Close()
		f.waitSessions(0)
		if got := filesOf(f.fs, "files"); len(got) != 0 {
			t.Errorf("files/ has %q after the connection has ended, want nothing", slices.Sorted(maps.Keys(got)))
		}
	})

	t.Run("a body that is not chunks", func(t *testing.T) {
		w := f.connect(dbwire.RoleWriter)
		done := w.writeRaw(rawFrame(byte(dbwire.KindPutOriginal), 1, []byte{0, 0, 0, 3, 'a'}))
		<-done
		w.wantClosed()
		f.waitSessions(0)
		if got := tempFiles(f.fs); got != nil {
			t.Errorf("a temporary file is left: %q", got)
		}
	})
}

// binaryChunk is data as one chunk of a PutOriginal.
func binaryChunk(data []byte) []byte {
	return append([]byte{byte(len(data) >> 24), byte(len(data) >> 16), byte(len(data) >> 8), byte(len(data))}, data...)
}

// TestPutOriginalRenameError checks that a PutOriginal removes its temporary file when it cannot rename it.
func TestPutOriginalRenameError(t *testing.T) {
	f := newFixture(t, nil)
	w := f.connect(dbwire.RoleWriter)
	data := []byte("GIF89a")
	h := sha256.Sum256(data)
	d := filepath.Dir(originalPath(h))
	file, err := f.fs.OpenFile(d, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	_, err = w.putOriginal(data)
	wantCode(t, err, dbwire.CodeFailed)
	if got := filesOf(f.fs, "files"); len(got) != 1 || got[d] != "" {
		t.Errorf("files/ has %q, want %q alone", slices.Sorted(maps.Keys(got)), d)
	}
	if got := tempFiles(f.fs); got != nil {
		t.Errorf("a temporary file is left: %q", got)
	}
}

// TestFilesGone checks that a PutOriginal fails with the error of the file system when files/ is gone, or its temporary file
// is: the text of the error is kept, and the reply is not that of an original that is not there.
func TestFilesGone(t *testing.T) {
	data := []byte("GIF89a")
	h := sha256.Sum256(data)
	fault := func(t *testing.T, err error) {
		t.Helper()
		re := wantCode(t, err, dbwire.CodeFailed)
		if !strings.Contains(re.Msg, syscall.ENOENT.Error()) {
			t.Errorf("the text is %q, want the error of the file system in it", re.Msg)
		}
	}
	t.Run("files/ is gone", func(t *testing.T) {
		f := newDirFixture(t, nil)
		w := f.connect(dbwire.RoleWriter)
		if err := os.RemoveAll(filepath.Join(f.dir, "files")); err != nil {
			t.Fatal(err)
		}
		_, err := w.putOriginal(data)
		fault(t, err)
		// An original that is not there is not there, as before.
		_, err = w.original(h)
		wantCode(t, err, dbwire.CodeNotFound)
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
				if isTempName(name) {
					if err := os.Remove(filepath.Join(dir, "files", name)); err != nil {
						t.Fatal(err)
					}
				}
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var dir atomic.Value
			f := newDirFixture(t, func(o *Options) {
				o.Hook = func(point string) error {
					if point == "original:synced" {
						tt.gone(t, dir.Load().(string))
					}
					return nil
				}
			})
			dir.Store(f.dir)
			w := f.connect(dbwire.RoleWriter)
			_, err := w.putOriginal(data)
			fault(t, err)
		})
	}
}

// TestFilesGoneAtTheSync checks that a sync of the directory of an original that fails because the directory is gone
// stops the server, as any failed sync does, and that the log has the text of the file system, which the reply has not.
func TestFilesGoneAtTheSync(t *testing.T) {
	f := newFixture(t, nil)
	w := f.connect(dbwire.RoleWriter)
	f.fs.Inject(simfs.Fault{Match: func(op simfs.Op) bool {
		return op.Kind == simfs.OpSyncDir && strings.HasPrefix(op.Path, "files/")
	}, Err: syscall.ENOENT, Times: 1})
	_, err := w.putOriginal([]byte("GIF89a"))
	if re := wantCode(t, err, dbwire.CodeStopped); re.Msg != stoppedText {
		t.Errorf("the text is %q, want %q", re.Msg, stoppedText)
	}
	if !f.srv.stopped.Load() {
		t.Error("the server has not stopped")
	}
	lines := f.logs.all()
	if len(lines) != 1 || !strings.Contains(lines[0], syscall.ENOENT.Error()) {
		t.Errorf("the log has %q, want one line with the error of the file system", lines)
	}
}

// TestOriginalDirDurable checks that a PutOriginal syncs files/ also when the directory of the original is there, but its
// name may not be durable yet, as when a daemon that was killed has made it. On simfs, a crash keeps only what has been
// synced.
func TestOriginalDirDurable(t *testing.T) {
	f := newFixture(t, nil)
	w := f.connect(dbwire.RoleWriter)
	data := []byte("GIF89a")
	h := sha256.Sum256(data)
	if err := f.fs.Mkdir(filepath.Dir(originalPath(h)), 0o700); err != nil {
		t.Fatal(err)
	}
	mustPut(t, w, data)
	crashed := f.fs.Crash(loseAll)
	if got := filesOf(crashed, "files"); got[originalPath(h)] != string(data) {
		t.Errorf("the files after a crash are %q, want the original", got)
	}
}

func TestOpenOriginalErrors(t *testing.T) {
	data := []byte("GIF89a")
	h := sha256.Sum256(data)
	name := originalName(h)
	for _, tt := range []struct {
		name     string
		setup    func(t *testing.T, f *fixture, w *client, path string) // path is that of the file of the original of data
		code     dbwire.Code
		want     string // after the name of the file
		notFound bool
	}{
		{name: "missing", setup: func(*testing.T, *fixture, *client, string) {}, code: dbwire.CodeNotFound, want: "not found"},
		{
			name:  "missing from its directory",
			setup: func(t *testing.T, _ *fixture, _ *client, path string) { mkdir(t, filepath.Dir(path)) },
			code:  dbwire.CodeNotFound,
			want:  "not found",
		},
		{
			name: "symbolic link",
			setup: func(t *testing.T, _ *fixture, w *client, path string) {
				mustPut(t, w, data)
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				symlink(t, filepath.Base(path)+".old", path)
			},
			code: dbwire.CodeFailed,
			want: "symbolic links are not allowed",
		},
		{
			name: "directory",
			setup: func(t *testing.T, _ *fixture, _ *client, path string) {
				mkdir(t, filepath.Dir(path))
				mkdir(t, path)
			},
			code: dbwire.CodeFailed,
			want: "must be a regular file",
		},
		{
			name: "too large",
			setup: func(t *testing.T, _ *fixture, w *client, path string) {
				mustPut(t, w, data)
				if err := os.Truncate(path, 1001); err != nil {
					t.Fatal(err)
				}
			},
			code: dbwire.CodeFailed,
			want: "too large: 1001 bytes (max 1000)",
		},
		{
			name: "unreadable",
			setup: func(t *testing.T, _ *fixture, w *client, path string) {
				if os.Geteuid() == 0 {
					t.Skip("every file is readable") // to root
				}
				mustPut(t, w, data)
				if err := os.Chmod(path, 0); err != nil {
					t.Fatal(err)
				}
			},
			code: dbwire.CodeFailed,
			want: "cannot read: permission denied",
		},
		{
			name: "file as its directory",
			setup: func(t *testing.T, _ *fixture, _ *client, path string) {
				if err := os.WriteFile(filepath.Dir(path), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			code: dbwire.CodeFailed,
			want: "cannot read: not a directory",
		},
		{
			name: "its directory out of files",
			setup: func(t *testing.T, _ *fixture, _ *client, path string) {
				out := t.TempDir()
				if err := os.WriteFile(filepath.Join(out, filepath.Base(path)), data, 0o600); err != nil {
					t.Fatal(err)
				}
				symlink(t, out, filepath.Dir(path))
			},
			code: dbwire.CodeFailed,
			want: "cannot read: path escapes from parent",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newDirFixture(t, withObject(1000))
			w := f.connect(dbwire.RoleWriter)
			tt.setup(t, f, w, diskPath(f.dir, h))
			for _, c := range []*client{w, f.connect(dbwire.RoleReader)} {
				_, err := c.original(h)
				re := wantCode(t, err, tt.code)
				if want := name + ": " + tt.want; re.Msg != want {
					t.Errorf("the text is %q, want %q", re.Msg, want)
				}
			}
		})
	}
}

// TestDamagedOriginal checks that the server sends the bytes of an original as they are, damaged or not: the client checks
// them against the hash at the end of a read; and that a PutOriginal repairs it.
func TestDamagedOriginal(t *testing.T) {
	f := newDirFixture(t, nil)
	w := f.connect(dbwire.RoleWriter)
	data, damaged := []byte("GIF89a"), []byte("GIF87a")
	h := mustPut(t, w, data)
	if err := os.WriteFile(diskPath(f.dir, h), damaged, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := w.original(h); err != nil || !bytes.Equal(got, damaged) {
		t.Errorf("Original of a damaged original reads %q, %v; want %q", got, err, damaged)
	}
	mustPut(t, w, data)
	if got, err := w.original(h); err != nil || !bytes.Equal(got, data) {
		t.Errorf("Original after PutOriginal reads %q, %v; want %q", got, err, data)
	}
}

// TestOriginalReadError checks that a file that cannot be read to its end ends the reply with an error, and that the connection goes on.
func TestOriginalReadError(t *testing.T) {
	data := []byte("GIF89a")
	for name, tc := range map[string]struct {
		hook func(fsys *simfs.FS, p string)
		want string
	}{
		"fewer bytes": {func(fsys *simfs.FS, p string) {
			fsys.Inject(simfs.Fault{Match: func(op simfs.Op) bool {
				return op.Kind == simfs.OpRead && op.Path == p
			}, Err: syscall.EIO, Times: 1})
		}, "cannot read: input/output error"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, nil)
			w := f.connect(dbwire.RoleWriter)
			h := mustPut(t, w, data)
			tc.hook(f.fs, originalPath(h))
			_, err := w.original(h)
			re := wantCode(t, err, dbwire.CodeFailed)
			if !strings.Contains(re.Msg, tc.want) || !strings.HasPrefix(re.Msg, originalName(h)) {
				t.Errorf("the text is %q, want %q after the name of the file", re.Msg, tc.want)
			}
			// The connection goes on.
			if got, err := w.original(h); err != nil || !bytes.Equal(got, data) {
				t.Errorf("Original after the error reads %q, %v", got, err)
			}
		})
	}
}

// TestOpenRemovesTemps checks that the server removes, when it opens the data directory, the regular files of files/ that
// are named like a temporary file, and nothing else: no file with another name, no directory, no symbolic link, nothing in
// a directory of files/.
func TestOpenRemovesTemps(t *testing.T) {
	f := newDirFixture(t, nil)
	w := f.connect(dbwire.RoleWriter)
	original := []byte("GIF89a")
	h := mustPut(t, w, original)
	if err := f.srv.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	files := filepath.Join(f.dir, "files")
	originalDir := filepath.Dir(diskPath(f.dir, h))
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
	// Not named like a temporary file: the prefix alone, other lengths, upper case, a digit that is not hexadecimal, no
	// dot, a suffix.
	kept := []string{
		".tmp-", ".tmp-x", ".tmp-0123456789abcde", ".tmp-0123456789abcdef0", ".tmp-0123456789ABCDEF",
		".tmp-0123456789abcdeg", "tmp-0123456789abcdef", ".tmp-0123456789abcdef.old", "notes",
	}
	for _, name := range kept {
		write(filepath.Join(files, name), "keep "+name)
	}
	// A directory with the name of a temporary file, and files in it and in the directory of the original with that name:
	// only the files of files/ itself are temporary files.
	mkdir(t, filepath.Join(files, ".tmp-aaaaaaaaaaaaaaaa"))
	write(filepath.Join(files, ".tmp-aaaaaaaaaaaaaaaa", ".tmp-0123456789abcdef"), "in a directory")
	write(filepath.Join(originalDir, ".tmp-0123456789abcdef"), "beside an original")
	target := filepath.Join(t.TempDir(), "target")
	write(target, "the target of a link")
	symlink(t, target, filepath.Join(files, ".tmp-bbbbbbbbbbbbbbbb"))
	symlink(t, filepath.Join(t.TempDir(), "missing"), filepath.Join(files, ".tmp-cccccccccccccccc"))

	before := names(t, files)
	srv, err := OpenDir(f.dir, f.opts)
	if err != nil {
		t.Fatalf("OpenDir() = %v", err)
	}
	want := slices.DeleteFunc(slices.Clone(before), func(name string) bool { return slices.Contains(temps, name) })
	if got := names(t, files); !slices.Equal(got, want) {
		t.Errorf("files/ has %q after the open, want %q: the temporary files removed, the rest kept", got, want)
	}
	for _, name := range kept {
		if data, err := os.ReadFile(filepath.Join(files, name)); err != nil || string(data) != "keep "+name {
			t.Errorf("%s after the open reads %q, %v; want it unchanged", name, data, err)
		}
	}
	for path, want := range map[string]string{
		filepath.Join(files, ".tmp-aaaaaaaaaaaaaaaa", ".tmp-0123456789abcdef"): "in a directory",
		filepath.Join(originalDir, ".tmp-0123456789abcdef"):                    "beside an original",
		target: "the target of a link",
	} {
		if data, err := os.ReadFile(path); err != nil || string(data) != want {
			t.Errorf("%s after the open reads %q, %v; want %q", path, data, err, want)
		}
	}
	for _, name := range []string{".tmp-bbbbbbbbbbbbbbbb", ".tmp-cccccccccccccccc"} {
		if info, err := os.Lstat(filepath.Join(files, name)); err != nil || info.Mode()&fs.ModeSymlink == 0 {
			t.Errorf("%s after the open = %v, %v; want the symbolic link", name, info, err)
		}
	}
	g := &fixture{t: t, srv: srv, logs: f.logs}
	r := g.connect(dbwire.RoleReader)
	if data, err := r.original(h); err != nil || !bytes.Equal(data, original) {
		t.Errorf("the original after the open reads %q, %v; want %q", data, err, original)
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}

	// A directory opened again, with nothing to remove, is opened as it is.
	srv, err = OpenDir(f.dir, f.opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	if got := names(t, files); !slices.Equal(got, want) {
		t.Errorf("files/ has %q after the second open, want %q", got, want)
	}
}

// TestOpenAfterKilledTemp checks that a temporary file that a killed daemon has left stays when the server is closed, and
// that the next open removes it.
func TestOpenAfterKilledTemp(t *testing.T) {
	f := newDirFixture(t, nil)
	files := filepath.Join(f.dir, "files")
	if err := os.WriteFile(filepath.Join(files, ".tmp-0123456789abcdef"), []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.srv.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if got := names(t, files); len(got) != 1 || !isTempName(got[0]) {
		t.Fatalf("files/ has %q after the server is closed, want the temporary file alone", got)
	}
	srv, err := OpenDir(f.dir, f.opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	if got := names(t, files); got != nil {
		t.Errorf("files/ has %q after the open, want nothing", got)
	}
}

// TestOpenTempsErrors checks that Open fails with an error that names the file when it cannot remove a temporary file,
// and with one that names files/ when it cannot read it, and that it changes nothing then: the files are as they were;
// and that the next open works.
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
	for _, tt := range []struct {
		name  string
		match func(op simfs.Op) bool
		want  string
	}{
		{"remove", func(op simfs.Op) bool { return op.Kind == simfs.OpRemove && op.Path == "files/"+temp },
			"data/files/" + temp + ": cannot remove: input/output error"},
		{"read", func(op simfs.Op) bool { return op.Kind == simfs.OpReadDir && op.Path == "files" },
			"data/files: cannot read: input/output error"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fsys := simfs.New("data")
			o := testOptions()
			if err := Create(fsys, "data", o); err != nil {
				t.Fatal(err)
			}
			filesFS, err := fsys.Sub("files")
			if err != nil {
				t.Fatal(err)
			}
			file, err := filesFS.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.Write([]byte("half an original")); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			before := snapshot(fsys)
			fsys.Inject(simfs.Fault{Match: tt.match, Err: syscall.EIO, Times: 1})

			s, err := Open(fsys, "data", o)
			if err == nil || err.Error() != tt.want {
				if s != nil {
					_ = s.Close()
				}
				t.Fatalf("Open() = %v, want an error %q", err, tt.want)
			}
			if s != nil {
				t.Errorf("Open() that fails returns a server")
			}
			if after := snapshot(fsys); !maps.Equal(after, before) {
				t.Errorf("the files after the Open() that fails are not those from before it:\nbefore %q\nafter  %q", before, after)
			}

			// The failed open has let the lock go: the next one works, and removes the file.
			s, err = Open(fsys, "data", o)
			if err != nil {
				t.Fatalf("Open() after the failure = %v", err)
			}
			t.Cleanup(func() { _ = s.Close() })
			if got := tempFiles(fsys); got != nil {
				t.Errorf("the file system has the temporary files %q after the open, want none", got)
			}
		})
	}
}

// TestOpenTempPermission checks the error of Open for a temporary file that the OS refuses to remove, and that the open
// works once the directory can be written again.
func TestOpenTempPermission(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes the files of a directory that it cannot write")
	}
	f := newDirFixture(t, nil)
	if err := f.srv.Close(); err != nil {
		t.Fatal(err)
	}
	files := filepath.Join(f.dir, "files")
	temp := filepath.Join(files, ".tmp-0123456789abcdef")
	if err := os.WriteFile(temp, []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(files, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(files, 0o700) })
	s, err := OpenDir(f.dir, f.opts)
	if want := temp + ": cannot remove: permission denied"; err == nil || err.Error() != want {
		if s != nil {
			_ = s.Close()
		}
		t.Fatalf("OpenDir() = %v, want an error %q", err, want)
	}
	if err := os.Chmod(files, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDir(f.dir, f.opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if got := names(t, files); got != nil {
		t.Errorf("files/ has %q after the open, want nothing", got)
	}
}

// TestOriginalAfterFailedSync checks that a failed sync of a PutOriginal stops the whole server (13 of the plan, F2 in
// concept section 3.5): the sync may drop the names that it would have made durable, which the page cache still has, and
// which later syncs do not write. The request that fails, and every later PutObject, PutOriginal and Commit, get
// CodeStopped, without a change in the files; reads go on. After a restart of the machine, which a crash models, the
// server saves the original again.
func TestOriginalAfterFailedSync(t *testing.T) {
	data, other := []byte("GIF89a"), []byte("GIF87a")
	isTemp := func(op simfs.Op) bool { return op.Kind == simfs.OpSync && strings.HasPrefix(op.Path, "files/.tmp-") }
	for _, tt := range []struct {
		name  string
		match func(op simfs.Op) bool
	}{
		{"of the temporary file", isTemp},
		{"of files/", func(op simfs.Op) bool { return op.Kind == simfs.OpSyncDir && op.Path == "files" }},
		{"of the directory", func(op simfs.Op) bool {
			return op.Kind == simfs.OpSyncDir && strings.HasPrefix(op.Path, "files/")
		}},
	} {
		t.Run("the sync "+tt.name, func(t *testing.T) {
			f := newFixture(t, nil)
			w := f.connect(dbwire.RoleWriter)
			f.fs.Inject(simfs.Fault{Match: tt.match, Err: syscall.EIO, Drop: true, Times: 1})
			_, err := w.putOriginal(data)
			if re := wantCode(t, err, dbwire.CodeStopped); re.Msg != stoppedText {
				t.Errorf("the text is %q, want %q", re.Msg, stoppedText)
			}
			if got := tempFiles(f.fs); got != nil {
				t.Errorf("a temporary file is left: %q", got)
			}
			before := f.fs.Counted()
			for _, d := range [][]byte{data, other} {
				_, err := w.putOriginal(d)
				wantCode(t, err, dbwire.CodeStopped)
			}
			w.mustSnapshot()
			_, err = w.putObject("object")
			wantCode(t, err, dbwire.CodeStopped)
			_, _, err = w.commit(w.mustSnapshot(), event("e"))
			wantCode(t, err, dbwire.CodeStopped)
			if n := f.fs.Counted() - before; n != 0 {
				t.Errorf("the requests after the failed sync have made %d operations, want none", n)
			}
			// Reads go on, and Hello says that the server has stopped.
			r := f.connect(dbwire.RoleReader)
			if !r.hello.Stopped {
				t.Error("Hello does not say that the server has stopped")
			}
			if _, err := r.original(sha256.Sum256(data)); err != nil {
				wantCode(t, err, dbwire.CodeNotFound)
			}

			restarted := f.fs.Crash(keepAll(f.fs))
			after := f.opts
			after.Boot = machineWith(anotherBoot)
			srv, err := Open(restarted, "data", after)
			if err != nil {
				t.Fatalf("Open() after a restart = %v", err)
			}
			t.Cleanup(func() { _ = srv.Close() })
			g := &fixture{t: t, srv: srv, logs: f.logs}
			h := mustPut(t, g.connect(dbwire.RoleWriter), data)
			if got := filesOf(restarted.Crash(loseAll), "files"); got[originalPath(h)] != string(data) {
				t.Errorf("after a restart, the original that PutOriginal has saved is not there after a crash: %q", got)
			}
		})
	}

	// A PutOriginal that is inside the server when it stops is answered CodeStopped too, and syncs nothing more.
	for _, point := range []string{"original:synced", "original:renamed"} {
		t.Run("at the same time, at "+point, func(t *testing.T) {
			waiting, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			f := newFixture(t, func(o *Options) {
				o.Hook = func(p string) error {
					if p == point {
						once.Do(func() { close(waiting) })
						<-release
					}
					return nil
				}
			})
			w := f.connect(dbwire.RoleWriter)
			errc := make(chan error, 1)
			go func() {
				_, err := w.putOriginal(other)
				errc <- err
			}()
			<-waiting
			f.srv.stop(syscall.EIO) // the failure of a sync of another request
			var syncs atomic.Int64
			f.fs.Inject(simfs.Fault{Match: func(op simfs.Op) bool {
				if op.Kind == simfs.OpSync || op.Kind == simfs.OpSyncDir {
					syncs.Add(1)
				}
				return false
			}, Err: syscall.EIO})
			close(release)
			if re := wantCode(t, <-errc, dbwire.CodeStopped); re.Msg != stoppedText {
				t.Errorf("the text is %q, want %q", re.Msg, stoppedText)
			}
			if n := syncs.Load(); n != 0 {
				t.Errorf("the PutOriginal that ran at the same time has synced %d times after the stop, want never", n)
			}
			if got := tempFiles(f.fs); got != nil {
				t.Errorf("a temporary file is left: %q", got)
			}
		})
	}
}

// TestPutOriginalSpace checks the free space: an original is refused with CodeNoSpace if the free space is below
// Limits.MinFree when it begins, or when it has been written for as many bytes as one step, and its temporary file is gone.
func TestPutOriginalSpace(t *testing.T) {
	var free atomic.Uint64
	var calls atomic.Int64
	f := newFixture(t, func(o *Options) {
		o.Limits.MinFree = 1000
		o.FreeSpace = func() (Space, error) {
			calls.Add(1)
			return Space{Bytes: free.Load()}, nil
		}
	})
	f.srv.spaceStep = 2048
	w := f.connect(dbwire.RoleWriter)
	data := bytes.Repeat([]byte("o"), 5000)

	free.Store(1000) // one byte needs 1001
	_, err := w.putOriginal(data)
	re := wantCode(t, err, dbwire.CodeNoSpace)
	if want := "the free space is 1000 bytes; a write of 1 bytes must leave 1000"; re.Msg != want {
		t.Errorf("the text is %q, want %q", re.Msg, want)
	}
	if got := filesOf(f.fs, "files"); len(got) != 0 {
		t.Errorf("files/ has %q after the refusal, want nothing", slices.Sorted(maps.Keys(got)))
	}

	// Enough for the first step, and not for the second: the connection ends, and nothing is left.
	free.Store(1001)
	calls.Store(0)
	w = f.connectAfter(w)
	_, err = w.putOriginal(data)
	re = wantCode(t, err, dbwire.CodeNoSpace)
	if want := "the free space is 1001 bytes; a write of 2048 bytes must leave 1000"; re.Msg != want {
		t.Errorf("the text is %q, want %q", re.Msg, want)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("the free space was read %d times, want 2: at the start and after the first step", n)
	}
	w.wantClosed()
	if got := filesOf(f.fs, "files"); len(got) != 0 {
		t.Errorf("files/ has %q after the refusal, want nothing", slices.Sorted(maps.Keys(got)))
	}

	free.Store(1 << 40)
	w = f.connectAfter(w)
	h := mustPut(t, w, data)
	if got, err := w.original(h); err != nil || !bytes.Equal(got, data) {
		t.Errorf("Original reads %d bytes, %v", len(got), err)
	}
}

// TestPutOriginalSpaceSteps checks that the steps of a PutOriginal look at the free bytes only: its file is made, and the
// inode that it took is not asked for again.
func TestPutOriginalSpaceSteps(t *testing.T) {
	var calls atomic.Int64
	f := newFixture(t, func(o *Options) {
		o.Limits.MinFree, o.Limits.MinFreeInodes = 1, 100
		o.FreeSpace = func() (Space, error) {
			if calls.Add(1) == 1 {
				return Space{Bytes: 1 << 62, Inodes: 101, HasInodes: true}, nil
			}
			return Space{Bytes: 1 << 62, Inodes: 100, HasInodes: true}, nil
		}
	})
	f.srv.spaceStep = 2048
	w := f.connect(dbwire.RoleWriter)
	if _, err := w.putOriginal(bytes.Repeat([]byte("o"), 5000)); err != nil {
		t.Fatalf("PutOriginal = %v, want it stored", err)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("the free space was read %d times, want 2: at the start and after the first step", n)
	}
}

// TestPutOriginalHooks checks the two points of the hook, which the order of the syncs of the store gives: after the sync of
// the temporary file, which still is a temporary file; and after the sync of the directory, when the original has its name.
func TestPutOriginalHooks(t *testing.T) {
	data := []byte("GIF89a")
	h := sha256.Sum256(data)
	var fsys atomic.Pointer[simfs.FS]
	var points, states []string
	f := newFixture(t, func(o *Options) {
		o.Hook = func(point string) error {
			if strings.HasPrefix(point, "original:") {
				points = append(points, point)
				var temp, final bool
				for p := range filesOf(fsys.Load(), "files") {
					temp = temp || strings.HasPrefix(filepath.Base(p), tempPrefix)
					final = final || p == originalPath(h)
				}
				states = append(states, fmt.Sprintf("temp=%v original=%v", temp, final))
			}
			return nil
		}
	})
	fsys.Store(f.fs)
	w := f.connect(dbwire.RoleWriter)
	mustPut(t, w, data)
	if want := []string{"original:synced", "original:renamed"}; !slices.Equal(points, want) {
		t.Errorf("the hook was called at %q, want %q", points, want)
	}
	if want := []string{"temp=true original=false", "temp=false original=true"}; !slices.Equal(states, want) {
		t.Errorf("the files at those points were %q, want %q", states, want)
	}
}

// TestPutOriginalHookFails checks that a hook that fails fails the request, and leaves no temporary file.
func TestPutOriginalHookFails(t *testing.T) {
	var failAt atomic.Value
	failAt.Store("")
	f := newFixture(t, func(o *Options) {
		o.Hook = func(point string) error {
			if point == failAt.Load().(string) {
				return errors.New("boom " + secret)
			}
			return nil
		}
	})
	w := f.connect(dbwire.RoleWriter)
	for _, point := range []string{"original:synced", "original:renamed"} {
		failAt.Store(point)
		_, err := w.putOriginal([]byte("GIF89a"))
		if re := wantCode(t, err, dbwire.CodeFailed); strings.Contains(re.Msg, secret) {
			t.Errorf("the text %q has what the hook says", re.Msg)
		}
		if got := tempFiles(f.fs); got != nil {
			t.Errorf("a temporary file is left after a failure at %s: %q", point, got)
		}
	}
}

// neverEnds sends frames of the kind to c, without the end flag, until the server has closed the connection or about
// total bytes are written, and returns the number of bytes that it has written, and a channel that is closed when it stops.
func neverEnds(c *client, kind dbwire.Kind, total int64) (written *atomic.Int64, done chan struct{}) {
	frame := rawFrame(byte(kind), 0, make([]byte, 1000))
	written, done = new(atomic.Int64), make(chan struct{})
	go func() {
		defer close(done)
		for written.Load() < total {
			n, err := c.conn.Write(frame)
			written.Add(int64(n))
			if err != nil {
				return
			}
		}
	}()
	return written, done
}

// TestOriginalRoles checks that a reader may not put an original, whatever it sends, and that its connection goes on, but
// for a body that has no end, which is cut at the bound of its kind.
func TestOriginalRoles(t *testing.T) {
	f := newFixture(t, withObject(1000))
	r := f.connect(dbwire.RoleReader)
	before := f.dump()
	_, err := r.putOriginal([]byte("GIF89a"))
	if re := wantCode(t, err, dbwire.CodeDenied); !strings.Contains(re.Msg, "only the writer") {
		t.Errorf("the text is %q", re.Msg)
	}
	written, done := neverEnds(r, dbwire.KindPutOriginal, 4<<20)
	re := wantCode(t, r.readReply(), dbwire.CodeLimit)
	if !strings.Contains(re.Msg, "more than 5064 bytes") {
		t.Errorf("the text is %q", re.Msg)
	}
	<-done
	r.wantClosed()
	if n := written.Load(); n > 5064+64<<10 {
		t.Errorf("the server read %d bytes of a request that may have 5064", n)
	}
	if got := filesOf(f.fs, "files"); len(got) != 0 {
		t.Errorf("files/ has %q after the refusals, want nothing", slices.Sorted(maps.Keys(got)))
	}
	f.unchanged(before)

	// A reader reads originals, as the writer does.
	w := f.connect(dbwire.RoleWriter)
	h := mustPut(t, w, []byte("GIF89a"))
	r = f.connect(dbwire.RoleReader)
	if got, err := r.original(h); err != nil || string(got) != "GIF89a" {
		t.Errorf("Original by a reader reads %q, %v", got, err)
	}
}

// TestPutOriginalStopped checks that a server that has stopped refuses the body of a PutOriginal without writing it to a
// file, up to the bound of a request, and that a longer body ends the connection.
func TestPutOriginalStopped(t *testing.T) {
	f := newFixture(t, withObject(1000))
	w := f.connect(dbwire.RoleWriter)
	f.srv.stop(syscall.EIO)
	counted := f.fs.Counted()
	_, err := w.putOriginal([]byte("GIF89a"))
	wantCode(t, err, dbwire.CodeStopped)
	if n := f.fs.Counted(); n != counted {
		t.Errorf("the refusal made %d changes in the files", n-counted)
	}
	_, done := neverEnds(w, dbwire.KindPutOriginal, 4<<20)
	wantCode(t, w.readReply(), dbwire.CodeLimit)
	<-done
	w.wantClosed()
	if n := f.fs.Counted(); n != counted {
		t.Errorf("the refusals made %d changes in the files", n-counted)
	}
}

// TestPutOriginalOrder checks the order of the writes and the syncs of a PutOriginal, which is that of the store, and was
// checked against power loss with dm-log-writes (issue #69): the temporary file is written and synced, files/ is synced
// after its directory is made, and the directory of the original after the rename; and a directory whose name is durable
// is not synced again.
func TestPutOriginalOrder(t *testing.T) {
	f := newFixture(t, nil)
	w := f.connect(dbwire.RoleWriter)
	var mu sync.Mutex
	var ops []string
	f.fs.Inject(simfs.Fault{Match: func(op simfs.Op) bool {
		switch op.Kind {
		case simfs.OpCreate, simfs.OpWrite, simfs.OpTruncate, simfs.OpSync, simfs.OpSyncDir, simfs.OpMkdir, simfs.OpRemove,
			simfs.OpRename:
			p := op.Path
			if i := strings.Index(p, tempPrefix); i >= 0 {
				p = p[:i] + ".tmp-*"
			}
			mu.Lock()
			ops = append(ops, op.Kind.String()+" "+p)
			mu.Unlock()
		case simfs.OpOpen, simfs.OpRead, simfs.OpLstat, simfs.OpReadDir, simfs.OpLock:
		}
		return false
	}, Err: syscall.EIO})
	take := func() []string {
		mu.Lock()
		defer mu.Unlock()
		defer func() { ops = nil }()
		return slices.Clone(ops)
	}
	first := mustPut(t, w, []byte("GIF89a"))
	dir := fmt.Sprintf("files/%x", first[:1])
	want := []string{"create files/.tmp-*", "write files/.tmp-*", "sync files/.tmp-*", "mkdir " + dir, "syncdir files",
		"rename files/.tmp-*", "syncdir " + dir}
	if got := take(); !slices.Equal(got, want) {
		t.Errorf("the first original made %q, want %q", got, want)
	}
	// Another original in the same directory.
	var data []byte
	for i := 0; ; i++ {
		data = []byte(fmt.Sprintf("original %d", i))
		if h := sha256.Sum256(data); h[0] == first[0] {
			break
		}
	}
	mustPut(t, w, data)
	want = []string{"create files/.tmp-*", "write files/.tmp-*", "sync files/.tmp-*", "mkdir " + dir, "rename files/.tmp-*",
		"syncdir " + dir}
	if got := take(); !slices.Equal(got, want) {
		t.Errorf("the second original in the directory made %q, want %q", got, want)
	}
	// The same original again: the file is replaced.
	mustPut(t, w, data)
	if got := take(); !slices.Equal(got, want) {
		t.Errorf("the same original again made %q, want %q", got, want)
	}
}
