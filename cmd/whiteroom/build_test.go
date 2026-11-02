//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	sampleSite   = "../../internal/build/testdata/site"    // the sample site of internal/build
	exampleTheme = "../../internal/theme/testdata/example" // the theme of the sample site, which it does not copy
	sampleOut    = "../../internal/build/testdata/out"     // the golden files of the release of the sample site
)

// copySample copies the sample site, with the example theme in its theme/, into a new directory, which it returns.
func copySample(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "site")
	if err := os.CopyFS(dir, os.DirFS(sampleSite)); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(filepath.Join(dir, "theme"), os.DirFS(exampleTheme)); err != nil {
		t.Fatal(err)
	}
	return dir
}

// buildSample builds the sample site with the example theme into out, with the cache directory cache, and returns what
// the command has written to stdout and stderr. It stops the test if the build fails.
func buildSample(t *testing.T, out, cache string) (stdout, stderr string) {
	t.Helper()
	code, stdout, stderr := runCmd(t, "build", "-theme", exampleTheme, "-out", out, "-cache", cache, sampleSite)
	if code != 0 {
		t.Fatalf("whiteroom build of the sample site = %d, with stderr:\n%s", code, stderr)
	}
	return stdout, stderr
}

// currentOf returns the release that out/current points to.
func currentOf(t *testing.T, out string) string {
	t.Helper()
	target, err := os.Readlink(filepath.Join(out, "current"))
	if err != nil {
		t.Fatal(err)
	}
	name, ok := strings.CutPrefix(target, "releases/")
	if !ok {
		t.Fatalf("current points to %q", target)
	}
	return name
}

// readGolden returns the golden file name of the release of the sample site, such as "manifest.txt".
func readGolden(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sampleOut, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

var manifestLine = regexp.MustCompile(`^(.+) \d+ [0-9a-f]{64}\n$`)

// sampleSummary returns the summary of a build of the sample site into out, which has published the release, if
// published is true, or found it published: the release that out/current points to, and the pages, the files and the
// warnings of the golden files of the release.
func sampleSummary(t *testing.T, out string, published bool) string {
	t.Helper()
	pages, files := 0, 0
	for line := range strings.Lines(readGolden(t, "manifest.txt")) {
		m := manifestLine.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("manifest.txt has the line %q", line)
		}
		if strings.HasPrefix(m[1], "public/") {
			files++
			if path.Ext(m[1]) == ".html" {
				pages++
			}
		}
	}
	warnings := strings.Count(readGolden(t, "warnings.txt"), "\n")
	name := currentOf(t, out)
	s := "published release " + name + " in " + out
	if !published {
		s = "release " + name + " in " + out + " is already published"
	}
	return fmt.Sprintf("%s: %d pages, %d files, %d warnings\n", s, pages, files, warnings)
}

// sampleWarnings returns the warnings of a build of the sample site, with the paths of their files from the working
// directory, for a copy in the directory site with the theme in the directory theme, or in its theme/ if theme is "".
func sampleWarnings(t *testing.T, site, theme string) string {
	t.Helper()
	var b strings.Builder
	for line := range strings.Lines(readGolden(t, "warnings.txt")) {
		file, rest, _ := strings.Cut(line, ":")
		if file == "theme" && theme != "" {
			file = theme
		} else {
			file = filepath.Join(site, filepath.FromSlash(file))
		}
		b.WriteString(file + ":" + rest)
	}
	return b.String()
}

// TestBuildSample builds the sample site with the example theme of -theme twice, into an output directory and a cache
// directory of the test: the first build publishes the release, and the second finds it published. Both print the
// summary on stdout, and the warnings on stderr, with the paths of their files from the working directory.
func TestBuildSample(t *testing.T) {
	out, cache := t.TempDir(), t.TempDir()
	warnings := sampleWarnings(t, sampleSite, exampleTheme)
	for _, published := range []bool{true, false} {
		stdout, stderr := buildSample(t, out, cache)
		if want := sampleSummary(t, out, published); stdout != want {
			t.Errorf("whiteroom build printed %q, want %q", stdout, want)
		}
		if stderr != warnings {
			t.Errorf("whiteroom build printed the warnings:\n%s\nwant:\n%s", stderr, warnings)
		}
	}
}

// TestBuildDefaults builds a copy of the sample site, with the example theme in its theme/, with only the directory of
// the site: into its out/, with the cache directory cache/ in it, which the site directory ignores, so that the second
// build finds the release published.
func TestBuildDefaults(t *testing.T) {
	dir := copySample(t)
	out := filepath.Join(dir, "out")
	warnings := sampleWarnings(t, dir, "")
	for _, published := range []bool{true, false} {
		code, stdout, stderr := runCmd(t, "build", dir)
		if code != 0 {
			t.Fatalf("whiteroom build %s = %d, with stderr:\n%s", dir, code, stderr)
		}
		if want := sampleSummary(t, out, published); stdout != want {
			t.Errorf("whiteroom build %s printed %q, want %q", dir, stdout, want)
		}
		if stderr != warnings {
			t.Errorf("whiteroom build %s printed the warnings:\n%s\nwant:\n%s", dir, stderr, warnings)
		}
	}
	for _, name := range []string{"out/current/public/index.html", "cache/CACHEDIR.TAG"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Errorf("whiteroom build %s: %v", dir, err)
		}
	}
}

// failingBuild runs whiteroom build with args and ctx, and checks that it fails, with the errors want on stderr and
// nothing on stdout.
func failingBuild(t *testing.T, what string, ctx context.Context, want string, args ...string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(ctx, append([]string{"build"}, args...), &stdout, &stderr)
	if code != 1 || stdout.Len() > 0 || stderr.String() != want {
		t.Errorf("%s: whiteroom build %s = %d, with stdout %q and stderr:\n%s\nwant 1, with stderr:\n%s", what,
			strings.Join(args, " "), code, stdout.String(), stderr.String(), want)
	}
}

// checkMissing checks that name does not exist.
func checkMissing(t *testing.T, what, name string) {
	t.Helper()
	if _, err := os.Lstat(name); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s: %s exists (%v)", what, name, err)
	}
}

// TestBuildErrors checks builds that fail: of a site with a problem in a document, after a build, which leaves current
// as it was; of directories that are not sites or have no theme, which make no output or cache directory in them; of
// a missing site or theme directory; and of a canceled context. They print the errors on stderr, with the paths of
// the files from the working directory, and nothing on stdout.
func TestBuildErrors(t *testing.T) {
	dir := copySample(t)
	if code, _, stderr := runCmd(t, "build", dir); code != 0 {
		t.Fatalf("whiteroom build %s = %d, with stderr:\n%s", dir, code, stderr)
	}
	release := currentOf(t, filepath.Join(dir, "out"))
	revision := filepath.Join(dir, "revisions", "rev_01JB8Y6T2RG7M4XK9P2D5W8NQ3.json")
	data, err := os.ReadFile(revision)
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(data), "  \"title\": \"サービス\",\n", "", 1)
	if broken == string(data) {
		t.Fatalf("%s has no title サービス", revision)
	}
	if err := os.WriteFile(revision, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	failingBuild(t, "a revision without a title", t.Context(), revision+":1:1: /title: missing title\n", dir)
	if got := currentOf(t, filepath.Join(dir, "out")); got != release {
		t.Errorf("a revision without a title: current points to %s, want %s", got, release)
	}

	empty := t.TempDir()
	failingBuild(t, "an empty directory", t.Context(), filepath.Join(empty, "site", "schema.yaml")+": missing file\n"+
		filepath.Join(empty, "site", "settings.json")+": missing file\n", empty)
	if entries, err := os.ReadDir(empty); err != nil || len(entries) > 0 {
		t.Errorf("an empty directory: it holds %v (%v)", entries, err)
	}

	noTheme := copySample(t)
	if err := os.RemoveAll(filepath.Join(noTheme, "theme")); err != nil {
		t.Fatal(err)
	}
	failingBuild(t, "a site without a theme", t.Context(), filepath.Join(noTheme, "theme")+": missing directory\n",
		noTheme)
	checkMissing(t, "a site without a theme", filepath.Join(noTheme, "out"))
	checkMissing(t, "a site without a theme", filepath.Join(noTheme, "cache"))

	missing := filepath.Join(t.TempDir(), "missing")
	failingBuild(t, "a missing site", t.Context(), "whiteroom build: open "+missing+": no such file or directory\n",
		missing)
	out, cache := filepath.Join(t.TempDir(), "out"), t.TempDir()
	failingBuild(t, "a missing theme", t.Context(), "whiteroom build: open "+missing+": no such file or directory\n",
		"-theme", missing, "-out", out, "-cache", cache, sampleSite)
	checkMissing(t, "a missing theme", out)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	failingBuild(t, "canceled", ctx, "whiteroom build: interrupted\n",
		"-theme", exampleTheme, "-out", out, "-cache", cache, sampleSite)
	checkMissing(t, "canceled", out)
}
