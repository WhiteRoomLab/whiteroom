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

// buildOptions returns the options to build src into out with the cache directory cache and workers.
func buildOptions(src Source, out, cache string, workers int) Options {
	return Options{Source: src, Out: out, CacheDir: cache, Workers: workers}
}

// mustBuild builds src into out with the cache directory cache and workers, and returns the result, failing on errors.
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

// TestBuildRemoveError checks that a build that has published its release but cannot remove an old one returns the
// result and the error.
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
	public := filepath.Join(out, "releases", first.Release, "public")
	if err := os.Chmod(public, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(public, 0o755) })
	res, err := Build(t.Context(), buildOptions(src, out, cache, 4))
	pattern := "^" + regexp.QuoteMeta(out+": RemoveAll releases/"+first.Release+": permission denied") + "$"
	if res == nil || !errors.Is(err, fs.ErrPermission) || !regexp.MustCompile(pattern).MatchString(err.Error()) {
		t.Fatalf("Build() = %+v, %v, want a result and an error that matches %s", res, err, pattern)
	}
	if !isHex16(res.Release) || !res.Changed || slices.Contains([]string{first.Release, second.Release}, res.Release) {
		t.Errorf("Build() = %+v, want a new release", res)
	}
	checkReleases(t, "an old release not removed", out, res.Release, first.Release, second.Release, res.Release)
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
	} {
		if res, err := Build(t.Context(), tt.opts); res != nil || err == nil || err.Error() != tt.want {
			t.Errorf("Build(%+v) = %+v, %v, want the error %q", tt.opts, res, err, tt.want)
		}
	}
	if got := namesIn(t, dir); !slices.Equal(got, []string{"cache", "link"}) {
		t.Errorf("Build() made %q", got)
	}
	if got := namesIn(t, cache); len(got) > 0 {
		t.Errorf("Build() made %q in the cache directory", got)
	}
}
