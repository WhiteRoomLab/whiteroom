package imaging

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/mediafile"
)

// source is an original of an image medium in the tests of Images, which counts how many times it is opened.
type source struct {
	data   []byte
	hash   string
	opened atomic.Int64
}

func newSource(data []byte) *source {
	sum := sha256.Sum256(data)
	return &source{data: data, hash: "sha256:" + hex.EncodeToString(sum[:])}
}

func (s *source) open() (io.ReadCloser, error) {
	s.opened.Add(1)
	return io.NopCloser(bytes.NewReader(s.data)), nil
}

// testSources returns originals of every kind: a still image larger than its published file, one with a profile,
// one with a warning, a GIF animation and a WebP image.
func testSources(t *testing.T) []*source {
	t.Helper()
	frame := gifImageWith(0, 0, 40, 30, 0, nil, 2, lzwOf(t, 2, indexesOf(40, 30, 4)))
	return []*source{
		newSource(largeJPEG(t, 6)),
		newSource(insertPNG(pngOf(t, colored(40, 30)), iccpChunk(t, p3Profile))),
		newSource(cmykJPEG(0)),
		newSource(gifWith("GIF89a", 40, 30, rgbTable, frame, frame)),
		newSource(readVector(t, "meta-exif-o3-alpha")),
	}
}

func newImages(t *testing.T, dir string, memory int64) *Images {
	t.Helper()
	im, err := New(Options{Dir: dir, Memory: memory})
	if err != nil {
		t.Fatal(err)
	}
	return im
}

// infoName and fileName return the names of the cached Info of the original with hash, and of its file of v, in the
// cache directory dir.
func infoName(dir, hash string) string {
	d := strings.TrimPrefix(hash, "sha256:")
	return filepath.Join(dir, versionDir, "info", d[:2], d+".json")
}

func fileName(dir, hash string, v Variant) string {
	key := cacheKey(hash, v)
	return filepath.Join(dir, versionDir, "files", key[:2], key+ext(v.Format))
}

// infoOf returns the Info of s from im.
func infoOf(t *testing.T, im *Images, s *source) Info {
	t.Helper()
	info, err := im.Info(s.hash, s.open)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// filesOf returns the files of vs of s from im.
func filesOf(t *testing.T, im *Images, s *source, info Info, vs []Variant) [][]byte {
	t.Helper()
	files, err := im.Files(t.Context(), s.hash, s.open, info, vs)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func checkOpened(t *testing.T, s *source, want int64) {
	t.Helper()
	if n := s.opened.Load(); n != want {
		t.Errorf("the original was opened %d times, want %d", n, want)
	}
}

func TestNew(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache", "images")
	im := newImages(t, dir, 0)
	if tag, err := os.ReadFile(filepath.Join(dir, "CACHEDIR.TAG")); err != nil || string(tag) != cacheTag {
		t.Errorf("CACHEDIR.TAG = %q, %v", tag, err)
	}
	if im.budget.size != 1<<30 {
		t.Errorf("a budget of %d bytes, want 1 GiB", im.budget.size)
	}
	if im = newImages(t, dir, 12345); im.budget.size != 12345 {
		t.Errorf("a budget of %d bytes, want 12345", im.budget.size)
	}
	t.Chdir(filepath.Dir(dir))
	if im = newImages(t, "images", 0); im.dir != dir {
		t.Errorf("the directory of %q is %q, want %q", "images", im.dir, dir)
	}

	used := t.TempDir()
	photo := filepath.Join(used, "photo.jpg")
	if err := os.WriteFile(photo, []byte("photo"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "CACHEDIR.TAG"), []byte("Signature: 8a477f597d28d172789f06886806bc55\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		opts Options
		err  string
	}{
		{"no directory", Options{}, "no cache directory"},
		{"negative memory", Options{Dir: t.TempDir(), Memory: -1}, "memory -1 is out of range (0 or more)"},
		{"directory in use", Options{Dir: used},
			used + ": not empty, and not a cache directory of whiteroom (no CACHEDIR.TAG)"},
		{"cache directory of another program", Options{Dir: other}, other + ": a cache directory of another program"},
		{"file", Options{Dir: photo}, "mkdir " + photo + ": not a directory"},
	} {
		if im, err := New(tt.opts); err == nil || err.Error() != tt.err {
			t.Errorf("%s: New() = %v, %v; want %q", tt.name, im, err, tt.err)
		}
	}
	if b, err := os.ReadFile(photo); err != nil || string(b) != "photo" {
		t.Errorf("the file in the directory in use: %q, %v", b, err)
	}
}

func TestImagesInfo(t *testing.T) {
	dir := t.TempDir()
	im := newImages(t, dir, 0)
	sources := testSources(t)
	for _, s := range sources {
		want, err := Inspect(s.data)
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if got := infoOf(t, im, s); !reflect.DeepEqual(got, want) {
				t.Errorf("Info() = %+v, want %+v", got, want)
			}
		}
		checkOpened(t, s, 1)
		var r infoRecord
		b, err := os.ReadFile(infoName(dir, s.hash))
		if err == nil {
			err = json.Unmarshal(b, &r)
		}
		if err != nil || r.Go != goVersion || !equalInfo(r.Info, want) {
			t.Errorf("cached Info %+v, %v", r, err)
		}
		// The warnings returned are the caller's.
		if got := infoOf(t, im, s); got.Warnings != nil {
			got.Warnings[0] = "changed"
			if got := infoOf(t, im, s); !reflect.DeepEqual(got, want) {
				t.Errorf("Info() after a change of its warnings = %+v", got)
			}
		}
	}
	// Another Images reads them from the cache.
	again := newImages(t, dir, 0)
	for _, s := range sources {
		want, _ := Inspect(s.data)
		if got := infoOf(t, again, s); !reflect.DeepEqual(got, want) {
			t.Errorf("Info() from the cache = %+v, want %+v", got, want)
		}
		checkOpened(t, s, 1)
	}
}

func TestImagesFiles(t *testing.T) {
	dir := t.TempDir()
	im := newImages(t, dir, 0)
	sources := testSources(t)
	want := make([][][]byte, len(sources))
	for k, s := range sources {
		info := infoOf(t, im, s)
		vs := variantsOf(info)
		var err error
		if want[k], err = makeFiles(t.Context(), s.data, info, vs); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			for i, f := range filesOf(t, im, s, info, vs) {
				if !bytes.Equal(f, want[k][i]) {
					t.Errorf("%s: %+v: other bytes than makeFiles", info.MIMEType, vs[i])
				}
			}
		}
		checkOpened(t, s, 2)
		for i, v := range vs {
			if b, err := os.ReadFile(fileName(dir, s.hash, v)); err != nil || !bytes.Equal(b, want[k][i]) {
				t.Errorf("%s: %+v: cached file of %d bytes, %v", info.MIMEType, v, len(b), err)
			}
		}
	}
	// Another Images reads them from the cache.
	again := newImages(t, dir, 0)
	for k, s := range sources {
		info := infoOf(t, again, s)
		for i, f := range filesOf(t, again, s, info, variantsOf(info)) {
			if !bytes.Equal(f, want[k][i]) {
				t.Errorf("%s: file %d from the cache differs", info.MIMEType, i)
			}
		}
		checkOpened(t, s, 2)
	}
	if files, err := again.Files(t.Context(), sources[0].hash, sources[0].open, Info{}, nil); err != nil ||
		files == nil || len(files) != 0 {
		t.Errorf("Files() of no variants = %v, %v", files, err)
	}
}

func TestImagesBrokenCache(t *testing.T) {
	dir := t.TempDir()
	s := newSource(insertPNG(pngOf(t, colored(40, 30)), iccpChunk(t, p3Profile)))
	info := infoOf(t, newImages(t, dir, 0), s)
	vs := []Variant{Published(info), Icon(info, 32), pngVariant(20, 15)}
	want := filesOf(t, newImages(t, dir, 0), s, info, vs)
	checkOpened(t, s, 2)
	unreadable := func(name string) error {
		if os.Geteuid() == 0 || runtime.GOOS == "windows" {
			return os.WriteFile(name, nil, 0o644) // root reads any file
		}
		return os.Chmod(name, 0)
	}
	for _, tt := range []struct {
		what  string
		i     int // in vs
		spoil func(name string) error
	}{
		{"empty file", 1, func(name string) error { return os.WriteFile(name, nil, 0o644) }},
		{"missing file", 2, os.Remove},
		{"unreadable file", 0, unreadable},
	} {
		name := fileName(dir, s.hash, vs[tt.i])
		if err := tt.spoil(name); err != nil {
			t.Fatal(err)
		}
		opened := s.opened.Load()
		for i, f := range filesOf(t, newImages(t, dir, 0), s, info, vs) {
			if !bytes.Equal(f, want[i]) {
				t.Errorf("%s: file %d differs", tt.what, i)
			}
		}
		checkOpened(t, s, opened+1)
		if b, err := os.ReadFile(name); err != nil || !bytes.Equal(b, want[tt.i]) {
			t.Errorf("%s: not made again: %d bytes, %v", tt.what, len(b), err)
		}
	}

	name := infoName(dir, s.hash)
	record := func(goVer string) []byte {
		b, err := json.Marshal(infoRecord{Go: goVer, Info: info})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, tt := range []struct {
		what string
		data []byte
	}{
		{"empty", nil},
		{"cut", record(goVersion)[:20]},
		{"not an object", []byte("[]")},
		{"of another version of Go", record("go1.0")},
		{"without an Info", []byte(`{"go":"` + goVersion + `"}`)},
	} {
		if err := os.WriteFile(name, tt.data, 0o644); err != nil {
			t.Fatal(err)
		}
		opened := s.opened.Load()
		if got := infoOf(t, newImages(t, dir, 0), s); !reflect.DeepEqual(got, info) {
			t.Errorf("Info() of a cached Info %s = %+v", tt.what, got)
		}
		checkOpened(t, s, opened+1)
		if b, err := os.ReadFile(name); err != nil || !bytes.Equal(b, record(goVersion)) {
			t.Errorf("cached Info %s: not made again: %s, %v", tt.what, b, err)
		}
	}
}

func TestImagesError(t *testing.T) {
	dir := t.TempDir()
	im := newImages(t, dir, 0)
	s := newSource(pngOf(t, colored(40, 30)))
	other := newSource(pngOf(t, colored(80, 60)))
	unknown := newSource(pngOf(t, colored(20, 10)))
	pdf := newSource([]byte("%PDF-1.7\n"))
	if len(s.data) == len(other.data) {
		t.Fatal("originals of the same size")
	}
	info := infoOf(t, im, s)
	oinfo := infoOf(t, im, other)
	vs := []Variant{Published(info)}
	errOpen := errors.New("cannot open")
	failing := func() (io.ReadCloser, error) { return nil, errOpen }
	for _, tt := range []struct {
		name     string
		files    bool // the call of Files, else of Info
		hash     string
		info     Info
		vs       []Variant
		open     func() (io.ReadCloser, error)
		unopened bool // the original is not opened
		err      string
	}{
		{name: "invalid hash", hash: "sha256:" + strings.Repeat("0", 63), unopened: true,
			err: `invalid hash "sha256:` + strings.Repeat("0", 63) + `": use "sha256:" and 64 lower case hex digits`},
		{name: "hash in upper case", hash: strings.ToUpper(other.hash), unopened: true, err: "invalid hash"},
		{name: "path in the hash", hash: "sha256:../../" + strings.Repeat("0", 58), unopened: true, err: "invalid hash"},
		{name: "invalid hash", files: true, hash: "md5:" + strings.Repeat("0", 32), unopened: true, err: "invalid hash"},
		{name: "another original", hash: unknown.hash, open: s.open,
			err: unknown.hash + ": the original has the hash " + s.hash},
		{name: "another original", files: true, hash: other.hash, info: oinfo, vs: vs, open: s.open,
			err: other.hash + ": the original has the hash " + s.hash},
		{name: "open fails", hash: unknown.hash, open: failing, err: unknown.hash + ": cannot open"},
		{name: "open fails", files: true, hash: s.hash, info: info, vs: vs, open: failing,
			err: s.hash + ": cannot open"},
		{name: "not an image", hash: pdf.hash, open: pdf.open, err: pdf.hash + ": not an image: application/pdf"},
		{name: "Info of another original", files: true, hash: s.hash, info: oinfo, vs: vs, open: s.open,
			err: fmt.Sprintf("%s: an original of %d bytes, not %d as its Info says", s.hash, len(s.data),
				len(other.data))},
		{name: "variant larger than the image", files: true, hash: s.hash, info: info, vs: []Variant{pngVariant(41, 30)},
			open: s.open, unopened: true, err: s.hash + ": 41x30 image/png: not within the image of 40x30 pixels"},
		{name: "animation of a still image", files: true, hash: s.hash, info: info,
			vs: []Variant{{Width: 40, Height: 30, Format: mediafile.PNG, Animated: true}}, open: s.open, unopened: true,
			err: s.hash + ": 40x30 image/png animation: not the published animation"},
	} {
		opened := s.opened.Load() + other.opened.Load() + unknown.opened.Load()
		var err error
		if tt.files {
			var files [][]byte
			files, err = im.Files(t.Context(), tt.hash, tt.open, tt.info, tt.vs)
			if files != nil {
				t.Errorf("%s: Files() = %d files", tt.name, len(files))
			}
		} else {
			var info Info
			info, err = im.Info(tt.hash, tt.open)
			if !reflect.DeepEqual(info, Info{}) {
				t.Errorf("%s: Info() = %+v", tt.name, info)
			}
		}
		if err == nil || !strings.HasPrefix(err.Error(), tt.err) {
			t.Errorf("%s: error %v, want %q", tt.name, err, tt.err)
		}
		if n := s.opened.Load() + other.opened.Load() + unknown.opened.Load(); tt.unopened && n != opened {
			t.Errorf("%s: an original was opened", tt.name)
		}
	}
	if _, err := im.Info(unknown.hash, failing); !errors.Is(err, errOpen) {
		t.Errorf("Info() = %v, not the error of open wrapped", err)
	}
	// Nothing was cached for them.
	for _, name := range []string{
		fileName(dir, other.hash, vs[0]), fileName(dir, s.hash, vs[0]), fileName(dir, s.hash, pngVariant(41, 30)),
		infoName(dir, unknown.hash), infoName(dir, pdf.hash),
	} {
		if _, err := os.Stat(name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: cached after an error: %v", name, err)
		}
	}
}

func TestImagesWriteError(t *testing.T) {
	dir := t.TempDir()
	im := newImages(t, dir, 0)
	s := newSource(pngOf(t, colored(40, 30)))
	info := infoOf(t, im, s)
	for _, tt := range []struct {
		what string
		v    Variant
		// block puts something in the way of the file name.
		block func(name string) error
		err   string
	}{
		{"a file where its directory goes", Icon(info, 32), func(name string) error {
			if err := os.MkdirAll(filepath.Dir(filepath.Dir(name)), 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Dir(name), nil, 0o644)
		}, ": 30x30 image/png: mkdir "},
		{"a directory where it goes", Published(info), func(name string) error {
			return os.MkdirAll(filepath.Join(name, "x"), 0o755)
		}, ": 40x30 image/png: rename "},
	} {
		name := fileName(dir, s.hash, tt.v)
		if err := tt.block(name); err != nil {
			t.Fatal(err)
		}
		_, err := im.Files(t.Context(), s.hash, s.open, info, []Variant{tt.v})
		if want := s.hash + tt.err; err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s: Files() error %v, want %q", tt.what, err, want)
		}
		if entries, err := os.ReadDir(filepath.Dir(name)); err == nil {
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".tmp") {
					t.Errorf("%s: temporary file %s left", tt.what, e.Name())
				}
			}
		}
	}
}

// treeOf returns the files and directories in dir, relative to it in slashes, with "/" after directories, in order.
func treeOf(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if d.IsDir() {
			rel += "/"
		}
		names = append(names, filepath.ToSlash(rel))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	return names
}

// treeWith returns the files of names in dir as treeOf does, with their directories.
func treeWith(t *testing.T, dir string, names ...string) []string {
	t.Helper()
	set := make(map[string]bool)
	for _, name := range names {
		rel, err := filepath.Rel(dir, name)
		if err != nil {
			t.Fatal(err)
		}
		rel = filepath.ToSlash(rel)
		set[rel] = true
		for d := filepath.ToSlash(filepath.Dir(rel)); d != "."; d = filepath.ToSlash(filepath.Dir(d)) {
			set[d+"/"] = true
		}
	}
	var tree []string
	for name := range set {
		tree = append(tree, name)
	}
	slices.Sort(tree)
	return tree
}

func TestImagesPrune(t *testing.T) {
	dir := t.TempDir()
	a := newSource(pngOf(t, colored(40, 30)))
	b := newSource(cmykJPEG(0))
	im := newImages(t, dir, 0)
	ainfo := infoOf(t, im, a)
	filesOf(t, im, a, ainfo, []Variant{Published(ainfo), Icon(ainfo, 32)})
	binfo := infoOf(t, im, b)
	filesOf(t, im, b, binfo, []Variant{Published(binfo)})
	if err := im.Prune(); err != nil {
		t.Fatal(err)
	}
	all := treeWith(t, dir, filepath.Join(dir, "CACHEDIR.TAG"), infoName(dir, a.hash), infoName(dir, b.hash),
		fileName(dir, a.hash, Published(ainfo)), fileName(dir, a.hash, Icon(ainfo, 32)),
		fileName(dir, b.hash, Published(binfo)))
	if got := treeOf(t, dir); !slices.Equal(got, all) {
		t.Errorf("after Prune of what was used:\n%q\nwant\n%q", got, all)
	}

	// The next build uses the published file of a, and finds files that no build uses.
	im = newImages(t, dir, 0)
	ainfo = infoOf(t, im, a)
	filesOf(t, im, a, ainfo, []Variant{Published(ainfo)})
	for _, name := range []string{
		filepath.Join(filepath.Dir(fileName(dir, a.hash, Published(ainfo))), ".tmp-123"),
		filepath.Join(dir, "v0", "info", "aa", strings.Repeat("a", 64)+".json"),
		filepath.Join(dir, versionDir, "files", "zz", "unknown.jpg"),
		filepath.Join(dir, "notes.txt"),
	} {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, versionDir, "files", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := im.Prune(); err != nil {
		t.Fatal(err)
	}
	kept := treeWith(t, dir, filepath.Join(dir, "CACHEDIR.TAG"), infoName(dir, a.hash),
		fileName(dir, a.hash, Published(ainfo)))
	if got := treeOf(t, dir); !slices.Equal(got, kept) {
		t.Errorf("after Prune:\n%q\nwant\n%q", got, kept)
	}
	// The files kept are still read from the cache, and the others are made again.
	opened := a.opened.Load()
	filesOf(t, im, a, ainfo, []Variant{Published(ainfo)})
	checkOpened(t, a, opened)
	filesOf(t, im, a, ainfo, []Variant{Icon(ainfo, 32)})
	checkOpened(t, a, opened+1)
	if err := newImages(t, dir, 0).Prune(); err != nil {
		t.Fatal(err)
	}
	if got := treeOf(t, dir); !slices.Equal(got, []string{"CACHEDIR.TAG"}) {
		t.Errorf("after Prune of an Images that used nothing: %q", got)
	}
}

// TestImagesPruneWaits checks that Prune waits for the calls of Files that run.
func TestImagesPruneWaits(t *testing.T) {
	dir := t.TempDir()
	im := newImages(t, dir, 0)
	s := newSource(pngOf(t, colored(40, 30)))
	info := infoOf(t, im, s)
	entered, proceed := make(chan bool), make(chan bool)
	im.jobHook = func(string, []Variant, int64) func() {
		entered <- true
		<-proceed
		return func() {}
	}
	files := make(chan error)
	go func() {
		_, err := im.Files(t.Context(), s.hash, s.open, info, []Variant{Published(info)})
		files <- err
	}()
	<-entered
	pruned := make(chan error)
	go func() { pruned <- im.Prune() }()
	select {
	case err := <-pruned:
		t.Fatalf("Prune() = %v while Files runs", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(proceed)
	if err := <-files; err != nil {
		t.Fatal(err)
	}
	if err := <-pruned; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fileName(dir, s.hash, Published(info))); err != nil {
		t.Errorf("the file made while Prune waited: %v", err)
	}
}

func TestImagesCanceled(t *testing.T) {
	s := newSource(pngOf(t, colored(40, 30)))
	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	im := newImages(t, t.TempDir(), 1<<20)
	info := infoOf(t, im, s)
	vs := []Variant{Published(info), Icon(info, 32)}
	if files, err := im.Files(canceled, s.hash, s.open, info, vs); files != nil || !isCanceled(err) {
		t.Errorf("Files() with a canceled context = %d files, %v", len(files), err)
	}
	checkOpened(t, s, 1)

	// While it waits for the budget.
	n, err := im.budget.acquire(t.Context(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error)
	go func() {
		_, err := im.Files(ctx, s.hash, s.open, info, vs)
		done <- err
	}()
	waitFor(t, "a job waiting for the budget", func() bool { _, w := im.budget.state(); return w == 1 })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Files() canceled while it waits for the budget: %v", err)
	}
	im.budget.release(n)

	// While it waits for another call for the same original.
	unlock, err := im.lock(t.Context(), s.hash)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	go func() {
		_, err := im.Files(ctx, s.hash, s.open, info, vs)
		done <- err
	}()
	waitFor(t, "a call waiting for the original", func() bool {
		im.mu.Lock()
		defer im.mu.Unlock()
		return im.locks[s.hash].refs == 2
	})
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Files() canceled while it waits for the original: %v", err)
	}
	unlock()

	// While it makes the files.
	ctx, cancel = context.WithCancel(t.Context())
	im.jobHook = func(string, []Variant, int64) func() {
		cancel()
		return func() {}
	}
	if _, err := im.Files(ctx, s.hash, s.open, info, vs); !errors.Is(err, context.Canceled) {
		t.Errorf("Files() canceled while it makes the files: %v", err)
	}
	checkOpened(t, s, 2)
	for _, v := range vs {
		if _, err := os.Stat(fileName(im.dir, s.hash, v)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%+v: cached after Files was canceled: %v", v, err)
		}
	}
	im.mu.Lock()
	defer im.mu.Unlock()
	if len(im.locks) != 0 {
		t.Errorf("%d locks left", len(im.locks))
	}
}

// isCanceled reports whether err is context.Canceled itself, as ctx.Err() returns it, not wrapped.
func isCanceled(err error) bool {
	return errors.Is(err, context.Canceled) && err.Error() == context.Canceled.Error()
}

// countdown is a context that is canceled when its Err has been called n times.
type countdown struct {
	context.Context
	n atomic.Int64
}

func (c *countdown) Err() error {
	if c.n.Add(-1) < 0 {
		return context.Canceled
	}
	return nil
}

// TestMakeFilesCanceled checks that makeFiles stops between the files it makes when its context is done.
func TestMakeFilesCanceled(t *testing.T) {
	frame := gifImageWith(0, 0, 4, 3, 0, nil, 2, lzwOf(t, 2, indexesOf(4, 3, 4)))
	for _, data := range [][]byte{
		insertPNG(pngOf(t, colored(3, 2600)), pngChunk("eXIf", exifTIFF(6))), // larger than its published file
		gifWith("GIF89a", 4, 3, rgbTable, frame, frame),
	} {
		info, err := Inspect(data)
		if err != nil {
			t.Fatal(err)
		}
		vs := variantsOf(info)
		want, err := makeFiles(t.Context(), data, info, vs)
		if err != nil {
			t.Fatal(err)
		}
		checks := 0
		for n := int64(0); ; n++ {
			ctx := &countdown{Context: t.Context()}
			ctx.n.Store(n)
			files, err := makeFiles(ctx, data, info, vs)
			if err == nil {
				for i := range files {
					if !bytes.Equal(files[i], want[i]) {
						t.Errorf("%+v: other bytes", vs[i])
					}
				}
				break
			}
			if !isCanceled(err) || files != nil {
				t.Fatalf("makeFiles() canceled after %d checks = %d files, %v", n, len(files), err)
			}
			checks++
		}
		if checks < 3 {
			t.Errorf("%s: makeFiles() checks its context %d times", info.MIMEType, checks)
		}
	}
}

// TestImagesConcurrent checks the calls of many goroutines for overlapping variants of a few originals: the files are
// those of makeFiles, each is made once, and each original is opened once for Info and once for each job.
func TestImagesConcurrent(t *testing.T) {
	sources := testSources(t)[1:]
	im := newImages(t, t.TempDir(), 0)
	var (
		mu   sync.Mutex
		made = make(map[string]int) // by hash and variant
		jobs = make(map[string]int) // by hash
	)
	im.jobHook = func(hash string, vs []Variant, _ int64) func() {
		mu.Lock()
		defer mu.Unlock()
		jobs[hash]++
		for _, v := range vs {
			made[hash+" "+v.label()]++
		}
		return func() {}
	}
	type want struct {
		files map[Variant][]byte
		sets  [][]Variant // that the goroutines ask for
	}
	wants := make([]want, len(sources))
	for k, s := range sources {
		info, err := Inspect(s.data)
		if err != nil {
			t.Fatal(err)
		}
		vs := variantsOf(info)
		files, err := makeFiles(t.Context(), s.data, info, vs)
		if err != nil {
			t.Fatal(err)
		}
		w := want{files: make(map[Variant][]byte)}
		for i, v := range vs {
			w.files[v] = files[i]
		}
		rev := slices.Clone(vs)
		slices.Reverse(rev)
		h := len(vs) / 2
		w.sets = [][]Variant{vs[:h+1], vs[h:], rev, vs[h : h+1]}
		wants[k] = w
	}
	var wg sync.WaitGroup
	for g := range 32 {
		wg.Go(func() {
			s, w := sources[g%len(sources)], wants[g%len(sources)]
			info, err := im.Info(s.hash, s.open)
			if err != nil {
				t.Error(err)
				return
			}
			vs := w.sets[g/len(sources)%len(w.sets)]
			files, err := im.Files(t.Context(), s.hash, s.open, info, vs)
			if err != nil {
				t.Error(err)
				return
			}
			for i, v := range vs {
				if !bytes.Equal(files[i], w.files[v]) {
					t.Errorf("%+v: other bytes than makeFiles", v)
				}
			}
		})
	}
	wg.Wait()
	total := 0
	for k, s := range sources {
		total += len(wants[k].files)
		if n := s.opened.Load(); n != int64(1+jobs[s.hash]) {
			t.Errorf("an original opened %d times for %d jobs", n, jobs[s.hash])
		}
	}
	for key, n := range made {
		if n != 1 {
			t.Errorf("%s made %d times", key, n)
		}
	}
	if len(made) != total {
		t.Errorf("%d files made, want %d", len(made), total)
	}
}

// TestImagesBudget checks that the jobs of Files never take more than the budget at once, and that those larger than
// the budget run alone.
func TestImagesBudget(t *testing.T) {
	var (
		sources []*source
		largest int64
	)
	for i := range 8 {
		s := newSource(pngOf(t, colored(40+i, 30)))
		info, err := Inspect(s.data)
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, s)
		largest = max(largest, jobMemory(info, variantsOf(info)))
	}
	for _, tt := range []struct {
		name   string
		memory int64
	}{
		{"2 jobs at once", 2*largest + largest/2},
		{"jobs larger than the budget", largest / 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			im := newImages(t, t.TempDir(), tt.memory)
			var running, peak atomic.Int64
			im.jobHook = func(_ string, _ []Variant, weight int64) func() {
				n := running.Add(weight)
				if n > tt.memory {
					t.Errorf("%d bytes of jobs at once, more than the budget of %d", n, tt.memory)
				}
				for {
					if p := peak.Load(); n <= p || peak.CompareAndSwap(p, n) {
						break
					}
				}
				time.Sleep(5 * time.Millisecond) // for the jobs to overlap
				return func() { running.Add(-weight) }
			}
			var wg sync.WaitGroup
			for _, s := range sources {
				wg.Go(func() {
					info, err := im.Info(s.hash, s.open)
					if err == nil {
						_, err = im.Files(t.Context(), s.hash, s.open, info, variantsOf(info))
					}
					if err != nil {
						t.Error(err)
					}
				})
			}
			wg.Wait()
			t.Logf("%d of %d bytes at most at once", peak.Load(), tt.memory)
			if tt.memory < largest && peak.Load() != tt.memory {
				t.Errorf("jobs of %d bytes at most at once, want the whole budget of %d", peak.Load(), tt.memory)
			}
		})
	}
}
