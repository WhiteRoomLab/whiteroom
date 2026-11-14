//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package build

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/imaging"
)

// buildSource returns the fixture with the example theme (internal/theme/testdata/example) and small originals of the
// media that its pages use: the logo, the icon and the image for Open Graph of the site, the avatar of the author, and
// the image of 6.11.5.
func buildSource(t *testing.T) *memSource {
	t.Helper()
	src := testSource(t)
	src.theme = os.DirFS("../theme/testdata/example")
	src.putOriginal(t, logoID, "logo.png", testPNG(t, 40, 10))
	src.putOriginal(t, iconID, "icon.png", testPNG(t, 48, 48))
	src.putOriginal(t, ogImageID, "ogp.jpg", testJPEG(t, 120, 63))
	src.putOriginal(t, avatarID, "yamada.png", testPNG(t, 32, 32))
	src.putOriginal(t, imageID, "会社外観.jpg", testJPEG(t, 160, 100))
	return src
}

func buildOptions(src Source, out, cache string, workers int) Options {
	return Options{Source: src, Out: out, CacheDir: cache, Workers: workers}
}

func mustBuild(t *testing.T, src Source, out, cache string, workers int) *Result {
	t.Helper()
	res, err := Build(t.Context(), buildOptions(src, out, cache, workers))
	if err != nil {
		t.Fatalf("Build() error:\n%v", err)
	}
	return res
}

// currentOf returns the release that out/current points to, which must be a relative link to releases/<name>.
func currentOf(t *testing.T, out string) string {
	t.Helper()
	target, err := os.Readlink(filepath.Join(out, "current"))
	if err != nil {
		t.Fatal(err)
	}
	name, ok := strings.CutPrefix(target, "releases/")
	if !ok || !isHex16(name) {
		t.Fatalf("current points to %q", target)
	}
	return name
}

// namesIn returns the names in dir, sorted.
func namesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names
}

// checkReleases checks that out holds only .lock, current, which points to the release current, and releases.
func checkReleases(t *testing.T, what, out, current string, releases ...string) {
	t.Helper()
	slices.Sort(releases)
	if got := namesIn(t, out); !slices.Equal(got, []string{".lock", "current", "releases"}) {
		t.Errorf("%s: the output directory holds %q", what, got)
	}
	if got := namesIn(t, filepath.Join(out, "releases")); !slices.Equal(got, releases) {
		t.Errorf("%s: the releases are %q, want %q", what, got, releases)
	}
	if got := currentOf(t, out); got != current {
		t.Errorf("%s: current points to %s, want %s", what, got, current)
	}
}

// failingBuild checks that Build with ctx and opts fails with the error want, and leaves the output directory as it
// was.
func failingBuild(t *testing.T, what string, ctx context.Context, opts Options, want string) {
	t.Helper()
	before := tree(t, opts.Out)
	res, err := Build(ctx, opts)
	if res != nil || err == nil || err.Error() != want {
		t.Errorf("%s: Build() = %+v, %v\nwant the error:\n%s", what, res, err, want)
	}
	checkTree(t, what, opts.Out, before)
}

// buildWarnings are the warnings of building buildSource.
var buildWarnings = []string{
	revisionFile(aboutID) + `:5:3: /template: the theme has no page template "standard"; rendered with entry.html`,
	`theme: no translation in en for the key "postedBy"; pages in en use its text in ja, the default language of the ` +
		"theme",
}

// TestBuild checks builds into one output directory one after another: the first one; the same site again, which
// changes nothing; changes, which keep the previous release and remove older ones; a change back to a release that was
// kept, which is used again; and stale temporary files, which a build removes.
func TestBuild(t *testing.T) {
	src := buildSource(t)
	out, cache := t.TempDir(), t.TempDir()
	first := mustBuild(t, src, out, cache, 4)
	if !isHex16(first.Release) || !first.Changed {
		t.Errorf("Build() = %+v, want a new release", first)
	}
	checkReleases(t, "first build", out, first.Release, first.Release)
	dir := filepath.Join(out, "releases", first.Release)
	if got := namesIn(t, dir); !slices.Equal(got, []string{"nginx.conf", "public", "server.json"}) {
		t.Errorf("the release holds %q", got)
	}
	public := listFiles(t, filepath.Join(dir, "public"))
	for _, name := range []string{
		"index.html", "about/index.html", "en/index.html", "404.html", "en/404.html", "feed/index.xml",
		"en/feed/index.xml", "sitemap.xml", "css/site.css", "assets/css/style.css", ".htaccess",
		"media/" + logoID + "/logo.png", "media/" + iconID + "/icon-32x32.png", "media/" + imageID + "/会社外観.jpg",
	} {
		if !slices.Contains(public, name) {
			t.Errorf("the public directory has no %s", name)
		}
	}
	if b := mustLoad(t, src); first.Pages != len(b.pages) || first.Files != len(public) {
		t.Errorf("Build() wrote %d pages and %d files, want %d and %d", first.Pages, first.Files, len(b.pages), len(public))
	}
	if name, _, err := releaseName(t.Context(), openRoot(t, dir), 2); name != first.Release || err != nil {
		t.Errorf("the release %s has the name %q (%v)", first.Release, name, err)
	}
	if got := errorTexts(t, content.Errors(first.Warnings)); !slices.Equal(got, buildWarnings) {
		t.Errorf("warnings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(buildWarnings, "\n"))
	}
	before := tree(t, out)

	again := mustBuild(t, src, out, cache, 4)
	want := Result{Release: first.Release, Pages: first.Pages, Files: first.Files, Warnings: first.Warnings}
	if !reflect.DeepEqual(*again, want) {
		t.Errorf("Build() of the same site = %+v, want %+v", *again, want)
	}
	checkTree(t, "the same site again", out, before)

	title := func(s string) {
		src.editRevision(t, aboutID, func(r *content.Revision) { r.Title = s })
	}
	title("会社案内")
	second := mustBuild(t, src, out, cache, 4)
	if second.Release == first.Release || !second.Changed {
		t.Errorf("Build() with a new title = %+v, want a new release", second)
	}
	checkReleases(t, "a new title", out, second.Release, first.Release, second.Release)
	if res := mustBuild(t, src, out, cache, 4); res.Release != second.Release || res.Changed {
		t.Errorf("Build() of the same site = %+v, want %s unchanged", res, second.Release)
	}
	checkReleases(t, "the same site again", out, second.Release, first.Release, second.Release)

	title("私たちについて")
	third := mustBuild(t, src, out, cache, 4)
	checkReleases(t, "another title", out, third.Release, second.Release, third.Release)

	title("会社案内")
	if res := mustBuild(t, src, out, cache, 4); res.Release != second.Release || !res.Changed {
		t.Errorf("Build() of the second site again = %+v, want %s, changed", res, second.Release)
	}
	checkReleases(t, "back to the second site", out, second.Release, second.Release, third.Release)

	for _, name := range []string{"releases/.tmp-" + randomA + "/public", "releases/.tmp-" + randomB} {
		if err := os.MkdirAll(filepath.Join(out, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("releases/"+third.Release, filepath.Join(out, "current.tmp-"+randomA)); err != nil {
		t.Fatal(err)
	}
	if res := mustBuild(t, src, out, cache, 4); res.Release != second.Release || res.Changed {
		t.Errorf("Build() of the same site = %+v, want %s unchanged", res, second.Release)
	}
	checkReleases(t, "stale temporary files", out, second.Release, second.Release, third.Release)
}

// TestBuildErrors checks that builds of sites with problems fail with them, before or after they have written their
// releases, and leave the output directory as it was, with no temporary directory. The problems of the site are
// content.Errors, and the errors of the source are wrapped. Those found before writing make no release directory in a
// new output directory.
func TestBuildErrors(t *testing.T) {
	cache := t.TempDir()
	failure := errors.New("disk failure")
	brokenTheme := testTheme()
	brokenTheme["templates/entry.html"] = &fstest.MapFile{Data: []byte("{{.Titel}}\n")}
	for _, tt := range []struct {
		what     string
		change   func(t *testing.T, src *memSource) []string // changes src and returns the errors of building it
		problems bool                                        // the errors are content.Errors
		early    bool                                        // found before writing
	}{
		{
			what: "a document that does not validate",
			change: func(t *testing.T, src *memSource) []string {
				src.editRevision(t, servicesID, func(r *content.Revision) { r.Slug = "Services" })
				return []string{revisionFile(servicesID) + ":4:3: /slug: contains upper case letters; normalize it first"}
			},
			problems: true, early: true,
		},
		{
			what: "a post at the path of a page",
			change: func(t *testing.T, src *memSource) []string {
				src.schema.Data = []byte(strings.Replace(testSchema, "/{yyyy}/{mm}/{dd}/{slug}/", "/{slug}/", 1))
				id := newID("ent", 1)
				src.putEntry(t, newEntry(id, 201, content.TypePost, "ja", groupOf(id)), content.Revision{
					Title: "About", Slug: "about", Date: "2026-10-27T09:00:00+09:00",
				})
				return []string{
					revisionFile(id) + `:4:3: /slug: output path "/about/" is also that of ` + revisionFile(aboutID),
				}
			},
			problems: true, early: true,
		},
		{
			what: "a redirect from the path of a page",
			change: func(t *testing.T, src *memSource) []string {
				src.addRedirect(t, 1, "/about", "https://example.com/")
				return []string{
					fileOf(content.RefRedirect, newID("rdr", 1)) + `:4:3: /from: from "/about" is the output path ` +
						`"/about/" of ` + revisionFile(aboutID),
				}
			},
			problems: true, early: true,
		},
		{
			what: "an original of another size",
			change: func(t *testing.T, src *memSource) []string {
				size := 0
				edit(t, src, content.RefMedia, logoID, func(m *content.Media) {
					size = m.Size
					m.Size++
				})
				return []string{
					fileOf(content.RefMedia, logoID) + ":7:3: /size: the original has " + strconv.Itoa(size) +
						" bytes, not " + strconv.Itoa(size+1),
				}
			},
			problems: true,
		},
		{
			what: "a redirect from the path of a file of a medium",
			change: func(t *testing.T, src *memSource) []string {
				path := "/media/" + logoID + "/logo.png"
				src.addRedirect(t, 1, path, "/")
				return []string{
					fileOf(content.RefRedirect, newID("rdr", 1)) + `:4:3: /from: from "` + path + `" is the output path "` +
						path + `" of ` + fileOf(content.RefMedia, logoID),
				}
			},
			problems: true,
		},
		{
			what: "a template that cannot be executed",
			change: func(t *testing.T, src *memSource) []string {
				src.theme = brokenTheme
				return []string{
					`rendering the pages: page /about/: template: templates/entry.html:1:2: executing "entry.html" at ` +
						"<.Titel>: can't evaluate field Titel in type *build.Page",
				}
			},
		},
		{
			what: "a source that cannot read a revision",
			change: func(t *testing.T, src *memSource) []string {
				src.fail = map[string]error{"Revision": failure}
				return []string{failure.Error()}
			},
			early: true,
		},
		{
			what: "a source that cannot open an original",
			change: func(t *testing.T, src *memSource) []string {
				src.fail = map[string]error{"OpenMedia": failure}
				return []string{"publishing the media: " + failure.Error()}
			},
		},
	} {
		src := buildSource(t)
		out := t.TempDir()
		mustBuild(t, src, out, cache, 4)
		before := tree(t, out)
		want := strings.Join(tt.change(t, src), "\n")
		res, err := Build(t.Context(), buildOptions(src, out, cache, 4))
		var es content.Errors
		switch {
		case res != nil || err == nil || err.Error() != want:
			t.Errorf("%s: Build() = %+v, %v\nwant the errors:\n%s", tt.what, res, err, want)
		case errors.As(err, &es) != tt.problems:
			t.Errorf("%s: Build() = %#v, want content.Errors: %v", tt.what, err, tt.problems)
		case src.fail != nil && !errors.Is(err, failure):
			t.Errorf("%s: Build() = %v, want an error that wraps %v", tt.what, err, failure)
		}
		checkTree(t, tt.what, out, before)

		if tt.early {
			out := t.TempDir()
			if _, err := Build(t.Context(), buildOptions(src, out, cache, 4)); err == nil || err.Error() != want {
				t.Errorf("%s: Build() into a new directory = %v, want the errors:\n%s", tt.what, err, want)
			}
			if got := namesIn(t, out); !slices.Equal(got, []string{".lock"}) {
				t.Errorf("%s: Build() into a new directory made %q, want only .lock", tt.what, got)
			}
		}
	}
}

// TestBuildReadsRevisions checks that a build reads each published revision again to render it, after it has read it to
// validate it, and no other revision.
func TestBuildReadsRevisions(t *testing.T) {
	src := buildSource(t)
	src.addPosts(t, "ja", 3)
	mustBuild(t, src, t.TempDir(), t.TempDir(), 4)
	reads := make(map[string]int)
	for _, id := range src.read {
		reads[id]++
	}
	published := 0
	for id, f := range src.docs[content.RefEntry] {
		var e content.Entry
		if err := e.UnmarshalJSON(f.Data); err != nil {
			t.Fatalf("decoding the entry %s: %v", id, err)
		}
		if e.Status != content.StatusPublished {
			continue
		}
		published++
		if n := reads[e.PublishedRevision]; n < 2 {
			t.Errorf("Build() read the revision %s of the published entry %s %d times, want at least 2",
				e.PublishedRevision, id, n)
		}
	}
	if len(reads) != published {
		t.Errorf("Build() read %d revisions, want the %d of the published entries", len(reads), published)
	}
}

// rereadSource is a Source that returns, for the revision id, the file that it has from the second time that it is
// read, or err; the first time, which is that of validating it, it returns the file of Source.
type rereadSource struct {
	Source
	id    string
	file  content.SiteFile
	err   error
	mu    sync.Mutex
	reads int
}

func (s *rereadSource) Revision(id string) (content.SiteFile, error) {
	f, err := s.Source.Revision(id)
	if err != nil || id != s.id {
		return f, err
	}
	s.mu.Lock()
	s.reads++
	again := s.reads > 1
	s.mu.Unlock()
	switch {
	case !again:
		return f, nil
	case s.err != nil:
		return content.SiteFile{}, s.err
	}
	return s.file, nil
}

// TestBuildRevisionChanged checks that a build fails if a revision that it has validated is not the same when it reads
// it again to render it, or cannot be read, and leaves the output directory as it was, with no temporary directory.
func TestBuildRevisionChanged(t *testing.T) {
	failure := errors.New("disk failure")
	changed := buildSource(t)
	changed.editRevision(t, aboutID, func(r *content.Revision) { r.Title = "会社案内" })
	for _, tt := range []struct {
		what string
		file content.SiteFile
		err  error
		want string
	}{
		{
			what: "a revision that has changed",
			file: changed.revisions[revisionID(aboutID)],
			want: "rendering the pages: " + revisionFile(aboutID) + ": changed during the build",
		},
		{
			what: "a revision that cannot be read again",
			err:  failure,
			want: "rendering the pages: " + failure.Error(),
		},
	} {
		src := buildSource(t)
		out, cache := t.TempDir(), t.TempDir()
		mustBuild(t, src, out, cache, 4)
		before := tree(t, out)
		reread := &rereadSource{Source: src, id: revisionID(aboutID), file: tt.file, err: tt.err}
		res, err := Build(t.Context(), buildOptions(reread, out, cache, 4))
		switch {
		case res != nil || err == nil || err.Error() != tt.want:
			t.Errorf("%s: Build() = %+v, %v\nwant the error:\n%s", tt.what, res, err, tt.want)
		case tt.err != nil && !errors.Is(err, tt.err):
			t.Errorf("%s: Build() = %v, want an error that wraps %v", tt.what, err, tt.err)
		}
		checkTree(t, tt.what, out, before)
	}
}

// TestBuildForeign checks that a build refuses an output directory that holds what the build has not made, and leaves
// it as it is, without a lock file.
func TestBuildForeign(t *testing.T) {
	src := buildSource(t)
	out := makeDir(t, "index.html", "releases/old/index.html", "releases/.tmp-"+randomA+"/")
	want := strings.Join([]string{
		filepath.Join(out, "index.html") + ": not made by the build; " + outMust,
		filepath.Join(out, "releases", "old") + ": not made by the build; " + releasesMust,
	}, "\n")
	failingBuild(t, "foreign files", t.Context(), buildOptions(src, out, t.TempDir(), 4), want)

	out = t.TempDir()
	mustBuild(t, src, out, t.TempDir(), 4)
	if err := os.WriteFile(filepath.Join(out, "releases", "README"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	want = filepath.Join(out, "releases", "README") + ": not made by the build; " + releasesMust
	failingBuild(t, "a foreign file after a build", t.Context(), buildOptions(src, out, t.TempDir(), 4), want)

	// The path keeps link/.., which is b.
	base := makeDir(t, "b/out/index.html", "b/entries/", "link -> b/entries")
	out = filepath.Join(base, "link") + filepath.FromSlash("/../out")
	want = out + string(filepath.Separator) + "index.html: not made by the build; " + outMust
	failingBuild(t, "a foreign file through a symbolic link", t.Context(), buildOptions(src, out, t.TempDir(), 4), want)
}

// TestBuildCanceled checks that a build whose context is done before it starts makes nothing, and that one canceled
// while it writes its release removes it and leaves the output directory as it was.
func TestBuildCanceled(t *testing.T) {
	src := buildSource(t)
	out, cache := filepath.Join(t.TempDir(), "out"), t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if res, err := Build(ctx, buildOptions(src, out, cache, 4)); res != nil || !errors.Is(err, context.Canceled) {
		t.Errorf("Build() canceled = %+v, %v, want %v", res, err, context.Canceled)
	}
	if _, err := os.Lstat(out); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Build() canceled made %s (%v)", out, err)
	}

	mustBuild(t, src, out, cache, 4)
	src.editRevision(t, aboutID, func(r *content.Revision) { r.Title = "会社案内" })
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	var once sync.Once
	var during []os.DirEntry
	// With the cache of the first build, the originals are opened first to publish the media.
	hooked := hookedSource{Source: src, hook: func(string) {
		once.Do(func() { during, _ = os.ReadDir(filepath.Join(out, "releases")) })
		cancel()
	}}
	failingBuild(t, "canceled", ctx, buildOptions(hooked, out, cache, 4), context.Canceled.Error())
	if !slices.ContainsFunc(during, func(e os.DirEntry) bool { return isTemp(e.Name(), tmpPrefix) }) {
		t.Errorf("the releases were %v when the build was canceled, want a temporary one", during)
	}
}

// TestLockOut checks that a lock of an output directory keeps another from being taken until it is unlocked.
func TestLockOut(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockOut(openRoot(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	want := "another build is running in " + dir
	if _, err := lockOut(openRoot(t, dir)); err == nil || err.Error() != want {
		t.Errorf("lockOut() while locked = %v, want the error %q", err, want)
	}
	unlock()
	if unlock, err = lockOut(openRoot(t, dir)); err != nil {
		t.Errorf("lockOut() after unlock = %v", err)
	} else {
		unlock()
	}
	if fi, err := os.Lstat(filepath.Join(dir, ".lock")); err != nil || !fi.Mode().IsRegular() || fi.Size() != 0 {
		t.Errorf("the lock file is %v (%v), want an empty file", fi, err)
	}
}

// TestBuildLocked checks that a build fails at once when another holds the lock of its output directory, and leaves
// the output directory as it was.
func TestBuildLocked(t *testing.T) {
	src := buildSource(t)
	out, cache := t.TempDir(), t.TempDir()
	first := mustBuild(t, src, out, cache, 4)
	unlock, err := lockOut(openRoot(t, out))
	if err != nil {
		t.Fatal(err)
	}
	src.editRevision(t, aboutID, func(r *content.Revision) { r.Title = "会社案内" })
	want := "another build is running in " + out
	failingBuild(t, "locked", t.Context(), buildOptions(src, out, cache, 4), want)
	unlock()

	// The build holds the lock until it ends, also after a collection of garbage.
	var once sync.Once
	var during error
	hooked := hookedSource{Source: src, hook: func(string) {
		once.Do(func() {
			runtime.GC()
			root, err := os.OpenRoot(out)
			if err != nil {
				during = err
				return
			}
			defer func() { _ = root.Close() }()
			unlock, err := lockOut(root)
			if err == nil {
				unlock()
			}
			during = err
		})
	}}
	if res := mustBuild(t, hooked, out, cache, 4); res.Release == first.Release || !res.Changed {
		t.Errorf("Build() after the lock = %+v, want a new release", res)
	}
	if during == nil || during.Error() != want {
		t.Errorf("lockOut() during a build = %v, want the error %q", during, want)
	}
}

// TestBuildFileErrors checks that a build that cannot write its release or switch current to it leaves the output
// directory as it was.
func TestBuildFileErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may write in any directory")
	}
	src := buildSource(t)
	out, cache := t.TempDir(), t.TempDir()
	mustBuild(t, src, out, cache, 4)
	src.editRevision(t, aboutID, func(r *content.Revision) { r.Title = "会社案内" })
	readOnly := func(dir string) {
		t.Helper()
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	}

	releases := filepath.Join(out, "releases")
	readOnly(releases)
	pattern := regexp.QuoteMeta(out) + `: mkdirat releases/\.tmp-[0-9a-f]{16}: permission denied`
	checkFileError(t, "no new releases", src, out, cache, pattern)
	if err := os.Chmod(releases, 0o755); err != nil {
		t.Fatal(err)
	}

	readOnly(out)
	pattern = regexp.QuoteMeta(out) + `: symlinkat releases/[0-9a-f]{16} current\.tmp-[0-9a-f]{16}: permission denied`
	checkFileError(t, "no new current", src, out, cache, pattern)
}

// checkFileError checks that building src into out fails with an error of the file system that matches pattern, and
// leaves out as it was.
func checkFileError(t *testing.T, what string, src Source, out, cache, pattern string) {
	t.Helper()
	before := tree(t, out)
	res, err := Build(t.Context(), buildOptions(src, out, cache, 4))
	if res != nil || !errors.Is(err, fs.ErrPermission) || !regexp.MustCompile("^"+pattern+"$").MatchString(err.Error()) {
		t.Errorf("%s: Build() = %+v, %v, want an error that matches %s", what, res, err, pattern)
	}
	checkTree(t, what, out, before)
}

// ioFailures is a Source whose originals fail to read or to close with the errors that it has.
type ioFailures struct {
	Source
	read, closing error
}

func (s ioFailures) OpenMedia(hash string) (fs.File, error) {
	f, err := s.Source.OpenMedia(hash)
	if err != nil {
		return nil, err
	}
	return ioFailure{File: f, read: s.read, closing: s.closing}, nil
}

type ioFailure struct {
	fs.File
	read, closing error
}

func (f ioFailure) Read(p []byte) (int, error) {
	if f.read != nil {
		return 0, f.read
	}
	return f.File.Read(p)
}

func (f ioFailure) Close() error {
	err := f.File.Close()
	if f.closing != nil {
		return f.closing
	}
	return err
}

// TestBuildImageIOErrors checks that a build that cannot read the original of an image, or write the image cache, fails with
// an error that wraps imaging.ErrIO and is not the problems of the site (content.Errors), and leaves the output directory as
// it was.
func TestBuildImageIOErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may write in any directory")
	}
	const (
		doc  = `media/med_[0-9A-Z]{26}\.json`
		hash = `sha256:[0-9a-f]{64}`
	)
	broken := errors.New("input/output error")
	readOnly := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	}
	tests := []struct {
		name string
		// run makes what the failing build needs, and returns its options and the pattern of the error.
		run func(t *testing.T, src *memSource) (opts Options, out, want string)
	}{
		{"a cache that cannot be written for what is read of the originals", func(t *testing.T, src *memSource) (Options, string, string) {
			out, cache := t.TempDir(), t.TempDir()
			mustBuild(t, src, out, t.TempDir(), 4)
			newTestImages(t, cache) // makes the tag in it
			readOnly(t, cache)
			want := `^publishing the media: ` + doc + `: cannot read the original: ` + hash + `: mkdir ` + regexp.QuoteMeta(cache) +
				`/v[0-9]+: permission denied$`
			return buildOptions(src, out, cache, 4), out, want
		}},
		{"a cache that cannot be written for the files", func(t *testing.T, src *memSource) (Options, string, string) {
			out, cache := t.TempDir(), t.TempDir()
			mustBuild(t, src, out, cache, 4)
			versions, err := filepath.Glob(filepath.Join(cache, "v[0-9]*"))
			if err != nil || len(versions) != 1 {
				t.Fatalf("the cache holds %q, %v, want one version", versions, err)
			}
			if err := os.RemoveAll(filepath.Join(versions[0], "files")); err != nil {
				t.Fatal(err)
			}
			readOnly(t, versions[0])
			want := `^publishing the media: ` + doc + `: cannot make the files: ` + hash + `: .*: mkdir ` +
				regexp.QuoteMeta(filepath.Join(versions[0], "files")) + `: permission denied$`
			return buildOptions(src, out, cache, 4), out, want
		}},
		{"an original that cannot be read", func(t *testing.T, src *memSource) (Options, string, string) {
			out := t.TempDir()
			mustBuild(t, src, out, t.TempDir(), 4)
			want := `^publishing the media: ` + doc + `: cannot read the original: ` + hash + `: input/output error$`
			return buildOptions(ioFailures{Source: src, read: broken}, out, t.TempDir(), 4), out, want
		}},
		{"an original that cannot be closed", func(t *testing.T, src *memSource) (Options, string, string) {
			out := t.TempDir()
			mustBuild(t, src, out, t.TempDir(), 4)
			want := `^publishing the media: ` + doc + `: cannot read the original: ` + hash + `: input/output error$`
			return buildOptions(ioFailures{Source: src, closing: broken}, out, t.TempDir(), 4), out, want
		}},
		{"the original of a redirect that cannot be read", func(t *testing.T, src *memSource) (Options, string, string) {
			src.addRedirect(t, 1, "/wp-content/uploads/logo.png", logoID)
			out := t.TempDir()
			mustBuild(t, src, out, t.TempDir(), 4)
			want := `^inspecting the originals of the redirects: ` + hash + `: input/output error$`
			return buildOptions(ioFailures{Source: src, read: broken}, out, t.TempDir(), 4), out, want
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, out, want := tt.run(t, buildSource(t))
			before := tree(t, out)
			res, err := Build(t.Context(), opts)
			var problems content.Errors
			switch {
			case res != nil || err == nil:
				t.Fatalf("Build() = %+v, %v, want an error", res, err)
			case errors.As(err, &problems):
				t.Errorf("Build() fails with the problems of the site, want a fault of the files:\n%v", err)
			case !errors.Is(err, imaging.ErrIO):
				t.Errorf("Build() error %q does not wrap imaging.ErrIO", err)
			case !regexp.MustCompile(want).MatchString(err.Error()):
				t.Errorf("Build() error %q does not match %s", err, want)
			}
			checkTree(t, tt.name, out, before)
		})
	}
}

// TestBuildMissingOriginalOfRedirect checks that an original that is not there is a problem of the site (content.Errors), and
// not a fault of the files, also when the build comes upon it at the redirect to its medium, before it writes anything.
func TestBuildMissingOriginalOfRedirect(t *testing.T) {
	src := buildSource(t)
	logo := src.putOriginal(t, logoID, "logo.png", testPNG(t, 41, 10))
	digits := strings.TrimPrefix(logo.Hash, "sha256:")
	delete(src.files, digits)
	src.addRedirect(t, 1, "/wp-content/uploads/logo.png", logoID)
	out := t.TempDir()
	res, err := Build(t.Context(), buildOptions(src, out, t.TempDir(), 4))
	var problems content.Errors
	if res != nil || !errors.As(err, &problems) || errors.Is(err, imaging.ErrIO) {
		t.Fatalf("Build() = %+v, %v, want the problems of the site that are no fault of the files", res, err)
	}
	want := "/to: cannot make the path: cannot read the original: " + logo.Hash + ": open " + digits + ": file does not exist"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("Build() error:\n%v\nwant a problem with %q", err, want)
	}
}

// TestBuildRemoveError checks that a build that has published its release but cannot remove an old one returns the
// result and the error, and leaves the rest of the old release under a temporary name, not as a release.
func TestBuildRemoveError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may write in any directory")
	}
	src := buildSource(t)
	out, cache := t.TempDir(), t.TempDir()
	first := mustBuild(t, src, out, cache, 4)
	src.editRevision(t, aboutID, func(r *content.Revision) { r.Title = "会社案内" })
	second := mustBuild(t, src, out, cache, 4)
	src.editRevision(t, aboutID, func(r *content.Revision) { r.Title = "私たちについて" })
	makeReadOnly(t, out, filepath.Join(out, "releases", first.Release, "public"))
	res, err := Build(t.Context(), buildOptions(src, out, cache, 4))
	pattern := "^" + regexp.QuoteMeta(out+": RemoveAll releases/.tmp-") + "[0-9a-f]{16}: permission denied$"
	if res == nil || !errors.Is(err, fs.ErrPermission) || !regexp.MustCompile(pattern).MatchString(err.Error()) {
		t.Fatalf("Build() = %+v, %v, want a result and an error that matches %s", res, err, pattern)
	}
	if !isHex16(res.Release) || !res.Changed || slices.Contains([]string{first.Release, second.Release}, res.Release) {
		t.Errorf("Build() = %+v, want a new release", res)
	}
	left := staleNames(t, out)
	if len(left) != 1 {
		t.Fatalf("the temporary directories are %q, want the one that holds the rest of the first release", left)
	}
	// The rest is among the names in releases, but not as the first release.
	checkReleases(t, "an old release not removed", out, res.Release, left[0], second.Release, res.Release)
}

// TestBuildDeterministic checks that builds of a site give the same release, with the same files and warnings,
// however many workers they have, and with or without the files of the media in the cache.
func TestBuildDeterministic(t *testing.T) {
	src := buildSource(t)
	caches := []string{t.TempDir(), t.TempDir()}
	var first *Result
	var files []string
	for i, workers := range []int{1, 8, 0, 3} {
		out := t.TempDir()
		res := mustBuild(t, src, out, caches[i%2], workers)
		release := tree(t, filepath.Join(out, "releases", res.Release))
		if first == nil {
			first, files = res, release
			continue
		}
		if !reflect.DeepEqual(res, first) || !slices.Equal(release, files) {
			t.Errorf("Build() with %d workers and the cache %d = %+v, with the files:\n%s\nwant %+v, with:\n%s",
				workers, i%2, res, strings.Join(release, "\n"), first, strings.Join(files, "\n"))
		}
	}
}

// TestBuildPrune checks that a build that succeeds removes the files that it has not used from the cache directory,
// and that one that fails, before or after it has written its release, leaves them.
func TestBuildPrune(t *testing.T) {
	src := buildSource(t)
	out, cache := t.TempDir(), t.TempDir()
	mustBuild(t, src, out, cache, 4)
	stale := filepath.Join(cache, "old", "stale")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(){
		func() { src.editRevision(t, servicesID, func(r *content.Revision) { r.Slug = "Services" }) },
		func() { edit(t, src, content.RefMedia, logoID, func(m *content.Media) { m.Size++ }) },
	} {
		change()
		var es content.Errors
		if _, err := Build(t.Context(), buildOptions(src, out, cache, 4)); !errors.As(err, &es) {
			t.Fatalf("Build() = %v, want the problems of the site", err)
		}
		if _, err := os.Stat(stale); err != nil {
			t.Errorf("a failed build removed %s (%v)", stale, err)
		}
		src = buildSource(t)
	}
	mustBuild(t, src, out, cache, 4)
	if _, err := os.Stat(filepath.Dir(stale)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a build left %s (%v)", filepath.Dir(stale), err)
	}
	if _, err := os.Stat(filepath.Join(cache, "CACHEDIR.TAG")); err != nil {
		t.Errorf("a build removed the tag of the cache directory (%v)", err)
	}
}

// TestBuildOptions checks the errors of options that Build cannot build with, which it reports before it makes
// anything.
func TestBuildOptions(t *testing.T) {
	src := buildSource(t)
	dir := t.TempDir()
	out, cache := filepath.Join(dir, "out"), filepath.Join(dir, "cache")
	if err := os.Mkdir(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink("cache", link); err != nil {
		t.Fatal(err)
	}
	// up/.. is sub, not dir.
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(filepath.Join(sub, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(sub, "inner"), filepath.Join(dir, "up")); err != nil {
		t.Fatal(err)
	}
	throughUp := filepath.Join(dir, "up") + filepath.FromSlash("/../out")
	apart := func(out, cache string) string {
		return "the output directory " + out + " and the cache directory " + cache +
			" must not be the same, or one in the other"
	}
	for _, tt := range []struct {
		opts Options
		want string
	}{
		{Options{Out: out, CacheDir: cache}, "no source"},
		{Options{Source: src, CacheDir: cache}, "no output directory"},
		{Options{Source: src, Out: out}, "no cache directory"},
		{Options{Source: src, Out: out, CacheDir: cache, Memory: -1}, "memory -1 is out of range (0 or more)"},
		{Options{Source: src, Out: out, CacheDir: cache, Workers: -1}, "workers -1 is out of range (0 or more)"},
		{Options{Source: src, Out: out, CacheDir: out + "/"}, apart(out, out+"/")},
		{Options{Source: src, Out: out, CacheDir: filepath.Join(out, "cache")}, apart(out, filepath.Join(out, "cache"))},
		{Options{Source: src, Out: filepath.Join(cache, "out"), CacheDir: cache}, apart(filepath.Join(cache, "out"), cache)},
		{Options{Source: src, Out: filepath.Join(link, "out"), CacheDir: cache}, apart(filepath.Join(link, "out"), cache)},
		{Options{Source: src, Out: dir, CacheDir: link}, apart(dir, link)},
		{Options{Source: src, Out: throughUp, CacheDir: filepath.Join(sub, "out", "cache")},
			apart(throughUp, filepath.Join(sub, "out", "cache"))},
		{Options{Source: src, Out: filepath.Join(sub, "out"), CacheDir: throughUp + filepath.FromSlash("/cache")},
			apart(filepath.Join(sub, "out"), throughUp+filepath.FromSlash("/cache"))},
	} {
		if res, err := Build(t.Context(), tt.opts); res != nil || err == nil || err.Error() != tt.want {
			t.Errorf("Build(%+v) = %+v, %v, want the error %q", tt.opts, res, err, tt.want)
		}
	}
	if got := namesIn(t, dir); !slices.Equal(got, []string{"cache", "link", "sub", "up"}) {
		t.Errorf("Build() made %q", got)
	}
	if got := namesIn(t, cache); len(got) > 0 {
		t.Errorf("Build() made %q in the cache directory", got)
	}
}

// TestBuildURL checks Options.URL with the sample site, whose url is https://example.com: the URL replaces that of the
// settings in the absolute URLs of the pages, the feeds and the sitemap, and in the path that the pages and server.json
// take from it; the same URL gives the same release, and no URL the release of the settings; a redirect that leads back
// to itself or loops only under the URL is a problem only with it; a URL that does not pass is refused before Build makes
// anything; and the documents of the Source stay as they are.
func TestBuildURL(t *testing.T) {
	src := openSample(t)
	settings, _, err := src.Settings()
	if err != nil {
		t.Fatal(err)
	}
	settings.Data = slices.Clone(settings.Data)
	cache := t.TempDir()
	built := func(src Source, url string) (string, *Result) {
		t.Helper()
		opts := buildOptions(src, t.TempDir(), cache, 4)
		opts.URL = url
		res, err := Build(t.Context(), opts)
		if err != nil {
			t.Fatalf("Build() with the URL %q error:\n%v", url, err)
		}
		return opts.Out, res
	}
	read := func(out string, res *Result, name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(out, "releases", res.Release, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	_, base := built(src, "")
	t.Run("the URL of the settings", func(t *testing.T) {
		if _, res := built(src, "https://example.com"); res.Release != base.Release {
			t.Errorf("Build() with the URL of the settings is the release %s, want %s, that without a URL", res.Release, base.Release)
		}
		_, plain := built(src, "")
		if plain.Release != base.Release {
			t.Errorf("Build() without a URL again is the release %s, want %s", plain.Release, base.Release)
		}
	})

	t.Run("another host", func(t *testing.T) {
		out, res := built(src, stagingURL)
		if res.Release == base.Release {
			t.Fatalf("Build() with the URL %s is the release %s of the URL of the settings", stagingURL, res.Release)
		}
		for name, wants := range map[string][]string{
			"public/index.html": {
				`<link rel="canonical" href="https://staging.example.com/">`,
				`<link rel="alternate" hreflang="ja" href="https://staging.example.com/">`,
				`<link rel="alternate" hreflang="en" href="https://staging.example.com/en/">`,
				`<link rel="alternate" type="application/rss+xml" title="株式会社〇〇" href="https://staging.example.com/feed/">`,
				`<meta property="og:url" content="https://staging.example.com/">`,
				`"url":"https://staging.example.com/"`,
			},
			"public/feed/index.xml": {
				"<link>https://staging.example.com/</link>",
				`<atom:link href="https://staging.example.com/feed/" rel="self" type="application/rss+xml"/>`,
				"<link>https://staging.example.com/2026/10/29/price/</link>",
			},
			"public/sitemap.xml": {
				"<loc>https://staging.example.com/</loc>",
				"<loc>https://staging.example.com/2026/10/29/price/</loc>",
			},
		} {
			got := read(out, res, name)
			for _, want := range wants {
				if !strings.Contains(got, want) {
					t.Errorf("%s of the release with the URL %s does not have %s", name, stagingURL, want)
				}
			}
			if strings.Contains(got, "https://example.com") {
				t.Errorf("%s of the release with the URL %s has the URL of the settings", name, stagingURL)
			}
		}
		if got := read(out, res, "server.json"); !strings.Contains(got, `"base": ""`) {
			t.Errorf("server.json of the release with the URL %s does not have the base of the root", stagingURL)
		}

		if _, again := built(src, stagingURL); again.Release != res.Release {
			t.Errorf("Build() with the URL %s again is the release %s, want %s", stagingURL, again.Release, res.Release)
		}
		if _, back := built(src, ""); back.Release != base.Release {
			t.Errorf("Build() without a URL after one with it is the release %s, want %s", back.Release, base.Release)
		}
	})

	t.Run("a path", func(t *testing.T) {
		out, res := built(src, stagingURL+"/blog")
		if res.Release == base.Release {
			t.Fatalf("Build() with the URL %s/blog is the release of the URL of the settings", stagingURL)
		}
		if got := read(out, res, "public/index.html"); !strings.Contains(got, `href="/blog/`) ||
			!strings.Contains(got, `<link rel="canonical" href="https://staging.example.com/blog/">`) {
			t.Errorf("index.html of the release with the URL %s/blog has no link under /blog/ or no canonical URL:\n%s", stagingURL, got)
		}
		if got := read(out, res, "server.json"); !strings.Contains(got, `"base": "/blog"`) {
			t.Errorf("server.json of the release with the URL %s/blog does not have the base /blog:\n%.200s", stagingURL, got)
		}
	})

	t.Run("redirects", func(t *testing.T) {
		const itself, loopA, loopB = "rdr_01JC0000000000000000000009", "rdr_01JC000000000000000000000A", "rdr_01JC000000000000000000000B"
		redirect := func(id, from, to string) content.SiteFile {
			return content.SiteFile{Name: "redirects/" + id + ".json", Data: []byte(`{
  "schemaVersion": 1,
  "id": "` + id + `",
  "from": "` + from + `",
  "to": "` + to + `",
  "status": 301
}
`)}
		}
		for _, tt := range []struct {
			what  string
			files []content.SiteFile
			want  []string // of the build with stagingURL
		}{
			{
				what:  "a redirect that leads back to itself",
				files: []content.SiteFile{redirect(itself, "/staging-only", stagingURL+"/staging-only")},
				want:  []string{"redirects/" + itself + ".json:5:3: /to: redirects to itself"},
			},
			{
				what: "a loop of redirects",
				files: []content.SiteFile{
					redirect(loopA, "/loop-a", stagingURL+"/loop-b"), redirect(loopB, "/loop-b", stagingURL+"/loop-a"),
				},
				want: []string{
					"redirects/" + loopA + ".json:5:3: /to: redirect loop: " + strings.Join([]string{loopA, loopB, loopA}, " -> "),
					"redirects/" + loopB + ".json:5:3: /to: redirect loop: " + strings.Join([]string{loopA, loopB, loopA}, " -> "),
				},
			},
		} {
			changed := changedSource{Source: src, files: tt.files}
			out := t.TempDir()
			opts := buildOptions(changed, out, cache, 4)
			if _, err := Build(t.Context(), opts); err != nil {
				t.Errorf("%s: Build() without a URL error:\n%v", tt.what, err)
				continue
			}
			opts.URL = stagingURL
			before := tree(t, out)
			res, err := Build(t.Context(), opts)
			if res != nil || err == nil {
				t.Errorf("%s: Build() with the URL %s = %+v, %v, want an error", tt.what, stagingURL, res, err)
				continue
			}
			if got := errorTexts(t, err); !slices.Equal(got, tt.want) {
				t.Errorf("%s: Build() errors:\n%s\nwant:\n%s", tt.what, strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
			checkTree(t, tt.what, out, before)
		}
	})

	t.Run("a URL that does not pass", func(t *testing.T) {
		for _, bad := range badSiteURLs {
			dir := t.TempDir()
			out, cache := filepath.Join(dir, "out"), filepath.Join(dir, "cache")
			opts := buildOptions(src, out, cache, 4)
			opts.URL = bad
			res, err := Build(t.Context(), opts)
			if res != nil {
				t.Errorf("Build() with the URL %q = %+v", bad, res)
			}
			checkBadURL(t, "Build()", err, bad)
			if got := namesIn(t, dir); len(got) > 0 {
				t.Errorf("Build() with the URL %q made %q", bad, got)
			}
		}
	})

	if after, _, err := src.Settings(); err != nil || !slices.Equal(after.Data, settings.Data) {
		t.Errorf("the settings of the Source after the builds = %q, %v, want those from before", after.Data, err)
	}
}
