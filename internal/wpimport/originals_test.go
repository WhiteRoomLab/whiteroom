package wpimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/fstest"
	"testing/iotest"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/mediafile"
	"github.com/WhiteRoomLab/whiteroom/internal/sitedir"
)

func sha256Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func lowerMediaLimit(t *testing.T, v int64) {
	t.Helper()
	old := maxMediaBytes
	maxMediaBytes = v
	t.Cleanup(func() { maxMediaBytes = old })
}

func newTestOriginals(t *testing.T) (*originals, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "files")
	o, err := newOriginals(dir)
	if err != nil {
		t.Fatal(err)
	}
	return o, dir
}

var tempName = regexp.MustCompile(`^\.tmp-[0-9a-f]{16}$`)

func dirNames(t *testing.T, dir string) []string {
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

// wantNames checks that the directory has the files with these names, and nothing else, such as a temporary file.
func wantNames(t *testing.T, dir string, want ...string) {
	t.Helper()
	if got := dirNames(t, dir); !slices.Equal(got, want) {
		t.Errorf("files in the directory = %q, want %q", got, want)
	}
}

func wantSkip(t *testing.T, err error, reason string) {
	t.Helper()
	var se *skipError
	if !errors.As(err, &se) {
		t.Fatalf("error = %v (%T), want a *skipError", err, err)
	}
	if se.reason != reason {
		t.Errorf("reason = %q, want %q", se.reason, reason)
	}
}

// wantStopping checks that err is an error that stops the import: none of those that save and openUpload name.
func wantStopping(t *testing.T, err error) {
	t.Helper()
	var se *skipError
	var re *readError
	switch {
	case err == nil:
		t.Fatal("no error")
	case errors.As(err, &se), errors.As(err, &re), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		t.Fatalf("error = %v (%T), want one that is not a skip, a read failure or a done context", err, err)
	}
}

func TestOriginalsSave(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
		want mediafile.Info
	}{
		{"PNG", testPNG(30, 20, 1), mediafile.Info{MIMEType: mediafile.PNG, Width: 30, Height: 20, Orientation: 1}},
		{"JPEG", testJPEG(24, 16), mediafile.Info{MIMEType: mediafile.JPEG, Width: 24, Height: 16, Orientation: 1}},
		{"GIF", testGIF(12, 9, 3), mediafile.Info{MIMEType: mediafile.GIF, Width: 12, Height: 9, Orientation: 1}},
		{"PDF", testPDF(), mediafile.Info{MIMEType: mediafile.PDF}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o, dir := newTestOriginals(t)
			f, err := o.save(context.Background(), bytes.NewReader(tt.data))
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256Hash(tt.data)
			if f.hash != hash || f.size != int64(len(tt.data)) || f.info != tt.want {
				t.Errorf("original = {%s %d %+v}, want {%s %d %+v}", f.hash, f.size, f.info, hash, len(tt.data), tt.want)
			}
			if probed, err := mediafile.Probe(bytes.NewReader(tt.data)); err != nil || probed != f.info {
				t.Errorf("info = %+v, but Probe gives %+v, %v", f.info, probed, err)
			}
			// Until keep, the data is in a temporary file, which internal/sitedir does not see.
			if names := dirNames(t, dir); len(names) != 1 || !tempName.MatchString(names[0]) {
				t.Fatalf("files in the directory before keep = %q, want one temporary file", names)
			}
			if got, err := f.read(); err != nil || !bytes.Equal(got, tt.data) {
				t.Errorf("read before keep = %d bytes, %v; want the data", len(got), err)
			}
			if err := f.keep(); err != nil {
				t.Fatal(err)
			}
			digits := strings.TrimPrefix(hash, "sha256:")
			wantNames(t, dir, digits)
			if got, err := os.ReadFile(filepath.Join(dir, digits)); err != nil || !bytes.Equal(got, tt.data) {
				t.Errorf("file of the original = %d bytes, %v; want the data", len(got), err)
			}
			if got, err := f.read(); err != nil || !bytes.Equal(got, tt.data) {
				t.Errorf("read after keep = %d bytes, %v; want the data", len(got), err)
			}
			f.discard()
			wantNames(t, dir, digits)
		})
	}
}

func TestOriginalsSitedir(t *testing.T) {
	site := t.TempDir()
	o, err := newOriginals(filepath.Join(site, "files"))
	if err != nil {
		t.Fatal(err)
	}
	data, other := testPNG(8, 8, 3), testPNG(8, 8, 4)
	f, err := o.save(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	g, err := o.save(context.Background(), bytes.NewReader(other))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.keep(); err != nil {
		t.Fatal(err)
	}
	g.discard()
	d, err := sitedir.Open(site)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	rd, err := d.OpenMedia(f.hash)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rd)
	_ = rd.Close()
	if err != nil || !bytes.Equal(got, data) {
		t.Errorf("original read by sitedir = %d bytes, %v; want the data", len(got), err)
	}
	if _, err := d.OpenMedia(g.hash); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("OpenMedia of a discarded original: error = %v, want one that wraps fs.ErrNotExist", err)
	}
}

func TestOriginalsKeepAndDiscard(t *testing.T) {
	data := testPNG(10, 10, 0)
	digits := strings.TrimPrefix(sha256Hash(data), "sha256:")
	save := func(t *testing.T, o *originals) *original {
		t.Helper()
		f, err := o.save(context.Background(), bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		return f
	}

	t.Run("discard", func(t *testing.T) {
		o, dir := newTestOriginals(t)
		f := save(t, o)
		f.discard()
		wantNames(t, dir)
		f.discard()
		wantNames(t, dir)
	})
	t.Run("keep twice", func(t *testing.T) {
		o, dir := newTestOriginals(t)
		f := save(t, o)
		for range 2 {
			if err := f.keep(); err != nil {
				t.Fatal(err)
			}
		}
		wantNames(t, dir, digits)
	})
	t.Run("replaces", func(t *testing.T) {
		o, dir := newTestOriginals(t)
		if err := os.WriteFile(filepath.Join(dir, digits), []byte("damaged"), 0o644); err != nil {
			t.Fatal(err)
		}
		f := save(t, o)
		if err := f.keep(); err != nil {
			t.Fatal(err)
		}
		wantNames(t, dir, digits)
		if got, err := os.ReadFile(filepath.Join(dir, digits)); err != nil || !bytes.Equal(got, data) {
			t.Errorf("file = %d bytes, %v; want the data", len(got), err)
		}
	})
	t.Run("keep fails", func(t *testing.T) {
		o, dir := newTestOriginals(t)
		f := save(t, o)
		// A directory that is not empty cannot be replaced by a file.
		if err := os.MkdirAll(filepath.Join(dir, digits, "x"), 0o755); err != nil {
			t.Fatal(err)
		}
		err := f.keep()
		if err == nil {
			t.Fatal("keep: no error")
		}
		wantStopping(t, err)
		if got, err := f.read(); err != nil || !bytes.Equal(got, data) {
			t.Errorf("read after a failed keep = %d bytes, %v; want the data", len(got), err)
		}
		f.discard()
		wantNames(t, dir, digits)
	})
	t.Run("directory gone", func(t *testing.T) {
		o, dir := newTestOriginals(t)
		f := save(t, o)
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if err := f.keep(); err == nil {
			t.Error("keep: no error")
		}
		f.discard()
	})
}

func TestOriginalsLimit(t *testing.T) {
	o, dir := newTestOriginals(t)
	data := testPNG(10, 10, 0)

	lowerMediaLimit(t, int64(len(data)))
	f, err := o.save(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("a file of exactly the limit: %v", err)
	}
	f.discard()

	lowerMediaLimit(t, int64(len(data))-1)
	_, err = o.save(context.Background(), bytes.NewReader(data))
	wantSkip(t, err, fmt.Sprintf("too large: more than %d bytes", len(data)-1))
	wantNames(t, dir)

	// A reader without an end is read until one byte more than the limit.
	lowerMediaLimit(t, 1000)
	var read int
	_, err = o.save(context.Background(), readerFunc(func(p []byte) (int, error) {
		clear(p)
		read += len(p)
		return len(p), nil
	}))
	wantSkip(t, err, "too large: more than 1000 bytes")
	if read != 1001 {
		t.Errorf("bytes read = %d, want 1001", read)
	}
	wantNames(t, dir)
}

type readerFunc func(p []byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func TestOriginalsRejects(t *testing.T) {
	const unsupported = "cannot read the file: unsupported file format"
	for _, tt := range []struct {
		name   string
		data   []byte
		reason string
	}{
		{"empty", nil, "empty file"},
		{"SVG", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), unsupported},
		{"HTML", []byte("<!doctype html><title>x</title>"), unsupported},
		{"text", []byte("hello"), unsupported},
		{"PDF header cut", []byte("%PDF-"), unsupported},
		{"PNG signature only", []byte("\x89PNG\r\n\x1a\n"), "cannot read the file: invalid PNG: unexpected EOF"},
		{"PNG cut in the header", testPNG(10, 10, 0)[:20], "cannot read the file: invalid PNG: unexpected EOF"},
		{"JPEG signature only", []byte("\xff\xd8\xff"), "cannot read the file: invalid JPEG: "},
		{"GIF header only", []byte("GIF89a"), "cannot read the file: invalid GIF: "},
		{"PNG with no width", pngHeader(0, 5), "cannot read the file: invalid PNG: "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o, dir := newTestOriginals(t)
			_, err := o.save(context.Background(), bytes.NewReader(tt.data))
			var se *skipError
			if !errors.As(err, &se) {
				t.Fatalf("error = %v (%T), want a *skipError", err, err)
			}
			// The text of the decoder after "invalid GIF: " and the like is Go's, which may change.
			ok := se.reason == tt.reason
			if strings.HasSuffix(tt.reason, ": ") {
				ok = strings.HasPrefix(se.reason, tt.reason)
			}
			if !ok {
				t.Errorf("reason = %q, want %q", se.reason, tt.reason)
			}
			wantNames(t, dir)
		})
	}
}

func TestOriginalsReadError(t *testing.T) {
	boom := errors.New("connection reset")
	data := testPNG(10, 10, 0)
	for _, tt := range []struct {
		name string
		r    io.Reader
		want error
	}{
		{"at the start", iotest.ErrReader(boom), boom},
		{"in the middle", io.MultiReader(bytes.NewReader(data[:20]), iotest.ErrReader(boom)), boom},
		{"after all the data", io.MultiReader(bytes.NewReader(data), iotest.ErrReader(boom)), boom},
		{"an end that is not io.EOF", io.MultiReader(bytes.NewReader(data), iotest.ErrReader(fmt.Errorf("body: %w", io.EOF))), io.EOF},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o, dir := newTestOriginals(t)
			_, err := o.save(context.Background(), tt.r)
			var re *readError
			if !errors.As(err, &re) {
				t.Fatalf("error = %v (%T), want a *readError", err, err)
			}
			if !errors.Is(err, tt.want) || !errors.Is(re.Unwrap(), tt.want) {
				t.Errorf("error = %v, want one that wraps %v", err, tt.want)
			}
			var se *skipError
			if errors.As(err, &se) {
				t.Errorf("error is also a *skipError")
			}
			wantNames(t, dir)
		})
	}
}

func TestOriginalsCanceled(t *testing.T) {
	data := testPNG(10, 10, 0)
	boom := errors.New("connection reset")
	t.Run("before", func(t *testing.T) {
		o, dir := newTestOriginals(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := o.save(ctx, bytes.NewReader(data))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		wantNames(t, dir)
	})
	t.Run("deadline", func(t *testing.T) {
		o, dir := newTestOriginals(t)
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		_, err := o.save(ctx, bytes.NewReader(data))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want context.DeadlineExceeded", err)
		}
		wantNames(t, dir)
	})
	t.Run("during the reads", func(t *testing.T) {
		o, dir := newTestOriginals(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		first := true
		_, err := o.save(ctx, readerFunc(func(p []byte) (int, error) {
			if !first {
				return 0, io.EOF
			}
			first = false
			cancel()
			return copy(p, data[:20]), nil
		}))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		wantNames(t, dir)
	})
	t.Run("with a failing reader", func(t *testing.T) {
		o, dir := newTestOriginals(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, err := o.save(ctx, readerFunc(func(p []byte) (int, error) {
			cancel()
			return 0, boom
		}))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		var re *readError
		if errors.As(err, &re) {
			t.Error("error is a *readError, want the error of the context")
		}
		wantNames(t, dir)
	})
}

func TestNewOriginals(t *testing.T) {
	tmp := t.TempDir()
	nested := filepath.Join(tmp, "a", "b", "files")
	if _, err := newOriginals(nested); err != nil {
		t.Fatalf("a directory with parents that are missing: %v", err)
	}
	if info, err := os.Stat(nested); err != nil || !info.IsDir() {
		t.Errorf("Stat(%s) = %v, %v; want a directory", nested, info, err)
	}
	if _, err := newOriginals(nested); err != nil {
		t.Errorf("a directory that is there: %v", err)
	}
	file := filepath.Join(tmp, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := newOriginals(file); err == nil {
		t.Error("a regular file as the directory: no error")
	}
	if _, err := newOriginals(filepath.Join(file, "files")); err == nil {
		t.Error("a directory below a regular file: no error")
	}
}

func TestOriginalsDirectoryGone(t *testing.T) {
	o, dir := newTestOriginals(t)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	_, err := o.save(context.Background(), bytes.NewReader(testPNG(4, 4, 0)))
	wantStopping(t, err)
	if want := "saving an original in " + dir + ": "; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error = %q, want it to start with %q", err, want)
	}
}

func TestOriginalsSameContent(t *testing.T) {
	saveKeep := func(o *originals, data []byte) error {
		f, err := o.save(context.Background(), bytes.NewReader(data))
		if err != nil {
			return err
		}
		defer f.discard()
		return f.keep()
	}

	t.Run("twice", func(t *testing.T) {
		o, dir := newTestOriginals(t)
		data := testPNG(20, 20, 5)
		for range 2 {
			if err := saveKeep(o, data); err != nil {
				t.Fatal(err)
			}
		}
		wantNames(t, dir, strings.TrimPrefix(sha256Hash(data), "sha256:"))
	})
	t.Run("at the same time", func(t *testing.T) {
		o, dir := newTestOriginals(t)
		data := testPNG(20, 20, 5)
		errs := make([]error, 8)
		var wg sync.WaitGroup
		for i := range errs {
			wg.Go(func() { errs[i] = saveKeep(o, data) })
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("save %d: %v", i, err)
			}
		}
		wantNames(t, dir, strings.TrimPrefix(sha256Hash(data), "sha256:"))
		if got, err := os.ReadFile(filepath.Join(dir, strings.TrimPrefix(sha256Hash(data), "sha256:"))); err != nil || !bytes.Equal(got, data) {
			t.Errorf("file = %d bytes, %v; want the data", len(got), err)
		}
	})
	t.Run("different contents at the same time", func(t *testing.T) {
		o, dir := newTestOriginals(t)
		errs := make([]error, 8)
		var want []string
		var wg sync.WaitGroup
		for i := range errs {
			data := testPNG(8, 8, byte(i))
			want = append(want, strings.TrimPrefix(sha256Hash(data), "sha256:"))
			wg.Go(func() { errs[i] = saveKeep(o, data) })
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("save %d: %v", i, err)
			}
		}
		slices.Sort(want)
		wantNames(t, dir, want...)
	})
}

func TestOriginalsPixels(t *testing.T) {
	for _, tt := range []struct{ w, h int }{{20000, 10000}, {65536, 1}, {1, 70000}, {1, 1}} {
		t.Run(fmt.Sprintf("%dx%d", tt.w, tt.h), func(t *testing.T) {
			o, dir := newTestOriginals(t)
			data := pngHeader(tt.w, tt.h)
			f, err := o.save(context.Background(), bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			want := mediafile.Info{MIMEType: mediafile.PNG, Width: tt.w, Height: tt.h, Orientation: 1}
			if f.info != want {
				t.Errorf("info = %+v, want %+v", f.info, want)
			}
			f.discard()
			wantNames(t, dir)
		})
	}
}

// countFS counts the files that its FS opens and closes, and gives statErr from their Stat if it is not nil.
type countFS struct {
	fs.FS
	statErr        error
	opened, closed int
}

func (c *countFS) Open(name string) (fs.File, error) {
	f, err := c.FS.Open(name)
	if err != nil {
		return nil, err
	}
	c.opened++
	return &countFile{File: f, fsys: c}, nil
}

type countFile struct {
	fs.File
	fsys *countFS
}

func (f *countFile) Close() error {
	f.fsys.closed++
	return f.File.Close()
}

func (f *countFile) Stat() (fs.FileInfo, error) {
	if f.fsys.statErr != nil {
		return nil, f.fsys.statErr
	}
	return f.File.Stat()
}

// errorFS fails every Open with err.
type errorFS struct{ err error }

func (e errorFS) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: e.err}
}

func TestOpenUpload(t *testing.T) {
	data := testPNG(10, 10, 0)
	newFS := func() *countFS {
		return &countFS{FS: fstest.MapFS{
			"2026/10/a.png":      {Data: data},
			"2026/10/ten.bin":    {Data: bytes.Repeat([]byte("x"), 10)},
			"2026/10/eleven.bin": {Data: bytes.Repeat([]byte("x"), 11)},
		}}
	}

	t.Run("a file", func(t *testing.T) {
		fsys := newFS()
		f, err := openUpload(fsys, "2026/10/a.png")
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil || !bytes.Equal(got, data) {
			t.Errorf("file = %d bytes, %v; want the data", len(got), err)
		}
		if fsys.opened != 1 || fsys.closed != 1 {
			t.Errorf("opened %d, closed %d", fsys.opened, fsys.closed)
		}
	})
	t.Run("saved", func(t *testing.T) {
		f, err := openUpload(newFS(), "2026/10/a.png")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		o, dir := newTestOriginals(t)
		orig, err := o.save(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		if err := orig.keep(); err != nil {
			t.Fatal(err)
		}
		wantNames(t, dir, strings.TrimPrefix(sha256Hash(data), "sha256:"))
	})
	t.Run("missing", func(t *testing.T) {
		for _, rel := range []string{"2026/10/b.png", "2026/10/a.png/x", "2027"} {
			f, err := openUpload(newFS(), rel)
			if f != nil || !errors.Is(err, errNotInCopy) {
				t.Errorf("%s: file = %v, error = %v; want errNotInCopy", rel, f, err)
			}
		}
	})
	t.Run("not a path", func(t *testing.T) {
		for _, rel := range []string{"../x", "/2026/10/a.png", "", "2026//10/a.png", "2026/10/./a.png", "2026/10/"} {
			f, err := openUpload(newFS(), rel)
			if f != nil || !errors.Is(err, errNotInCopy) {
				t.Errorf("%q: file = %v, error = %v; want errNotInCopy", rel, f, err)
			}
		}
	})
	t.Run("not a regular file", func(t *testing.T) {
		for _, rel := range []string{"2026/10", "2026", "."} {
			fsys := newFS()
			f, err := openUpload(fsys, rel)
			if f != nil {
				t.Errorf("%s: file = %v, want none", rel, f)
			}
			wantSkip(t, err, "not a regular file in the copy of the uploads")
			if errors.Is(err, errNotInCopy) {
				t.Errorf("%s: error is errNotInCopy", rel)
			}
			if fsys.opened != 1 || fsys.closed != 1 {
				t.Errorf("%s: opened %d, closed %d", rel, fsys.opened, fsys.closed)
			}
		}
	})
	t.Run("size", func(t *testing.T) {
		lowerMediaLimit(t, 10)
		fsys := newFS()
		f, err := openUpload(fsys, "2026/10/ten.bin")
		if err != nil {
			t.Fatalf("a file of exactly the limit: %v", err)
		}
		_ = f.Close()
		f, err = openUpload(fsys, "2026/10/eleven.bin")
		if f != nil {
			t.Errorf("file = %v, want none", f)
		}
		wantSkip(t, err, "too large: 11 bytes (max 10)")
		if fsys.opened != 2 || fsys.closed != 2 {
			t.Errorf("opened %d, closed %d", fsys.opened, fsys.closed)
		}
	})
	t.Run("cannot be read", func(t *testing.T) {
		f, err := openUpload(errorFS{fs.ErrPermission}, "2026/10/a.png")
		if f != nil {
			t.Errorf("file = %v, want none", f)
		}
		wantStopping(t, err)
		if !errors.Is(err, fs.ErrPermission) || !strings.HasPrefix(err.Error(), "reading the copy of the uploads: ") {
			t.Errorf("error = %v, want one that wraps fs.ErrPermission, after the reading", err)
		}
	})
	t.Run("stat fails", func(t *testing.T) {
		boom := errors.New("input/output error")
		fsys := newFS()
		fsys.statErr = boom
		f, err := openUpload(fsys, "2026/10/a.png")
		if f != nil {
			t.Errorf("file = %v, want none", f)
		}
		wantStopping(t, err)
		if !errors.Is(err, boom) {
			t.Errorf("error = %v, want one that wraps %v", err, boom)
		}
		if fsys.opened != 1 || fsys.closed != 1 {
			t.Errorf("opened %d, closed %d", fsys.opened, fsys.closed)
		}
	})
	t.Run("names that the file system cannot open", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "2026"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		fsys := os.DirFS(dir)
		// The path through a file, and a name that the file system cannot have, name no file of the copy.
		for _, rel := range []string{"2026/a.jpg", strings.Repeat("a", 300) + ".jpg"} {
			if _, err := fsys.Open(rel); err == nil || errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("%.20s: the file system gives %v, the test needs an error that is not fs.ErrNotExist", rel, err)
			} else if !errors.Is(err, syscall.ENOTDIR) && !errors.Is(err, syscall.ENAMETOOLONG) {
				t.Fatalf("%.20s: the file system gives %v", rel, err)
			}
			if f, err := openUpload(fsys, rel); f != nil || !errors.Is(err, errNotInCopy) {
				t.Errorf("%.20s: file = %v, error = %v; want errNotInCopy", rel, f, err)
			}
		}
	})
}
