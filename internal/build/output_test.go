//go:build unix

package build

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Names of releases and random parts of temporary names in the tests.
const (
	releaseA = "0123456789abcdef"
	releaseB = "fedcba9876543210"
	releaseC = "00000000000000ff"
	randomA  = "aaaaaaaaaaaaaaaa"
	randomB  = "bbbbbbbbbbbbbbbb"
)

// tree returns a line for each file, directory and symbolic link in dir and under it, in the order of their paths: the
// path, the mode, and the SHA-256 of a file or the target of a link.
func tree(t *testing.T, dir string) []string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		line := filepath.ToSlash(rel) + " " + info.Mode().String()
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			line += " " + target
		case info.Mode().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			line += fmt.Sprintf(" %x", sha256.Sum256(data))
		}
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

// checkTree checks that dir holds what tree returned as want.
func checkTree(t *testing.T, what, dir string, want []string) {
	t.Helper()
	if got := tree(t, dir); !slices.Equal(got, want) {
		t.Errorf("%s: %s holds:\n%s\nwant:\n%s", what, dir, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// makeDir makes the entries in a new directory, which it returns: a name that ends with "/" is a directory, "<name> ->
// <target>" a symbolic link, and any other name a file with the content "data".
func makeDir(t *testing.T, entries ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, e := range entries {
		name, target, link := strings.Cut(e, " -> ")
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		var err error
		switch {
		case link:
			err = os.Symlink(target, p)
		case strings.HasSuffix(name, "/"):
			err = os.MkdirAll(p, 0o755)
		default:
			err = os.WriteFile(p, []byte("data"), 0o644)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// openRoot opens dir as a root, which it closes at the end of the test.
func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

// TestCheckLayout checks what checkLayout finds in output directories that hold only what the build makes.
func TestCheckLayout(t *testing.T) {
	for _, tt := range []struct {
		what    string
		entries []string
		want    layout
	}{
		{"empty", nil, layout{}},
		{"first build", []string{".lock", "releases/"}, layout{}},
		{
			"releases",
			[]string{
				".lock",
				"releases/" + releaseA + "/public/index.html",
				"releases/" + releaseB + "/",
				"releases/" + releaseC + "/",
				"current -> releases/" + releaseB,
			},
			layout{current: releaseB, releases: []string{releaseC, releaseA, releaseB}},
		},
		{
			"stale",
			[]string{
				".lock",
				"releases/" + releaseA + "/",
				"releases/.tmp-" + randomB + "/public/",
				"releases/.tmp-" + randomA + "/",
				"current -> releases/" + releaseA,
				"current.tmp-" + randomA + " -> releases/" + releaseA,
				"current.tmp-" + randomB + " -> releases/" + releaseB,
			},
			layout{
				current:  releaseA,
				releases: []string{releaseA},
				stale: []string{
					"current.tmp-" + randomA, "current.tmp-" + randomB,
					"releases/.tmp-" + randomA, "releases/.tmp-" + randomB,
				},
			},
		},
		{
			"a current to a release that does not exist",
			[]string{".lock", "releases/" + releaseA + "/", "current -> releases/" + releaseB},
			layout{releases: []string{releaseA}},
		},
		{"a current without releases", []string{"current -> releases/" + releaseA}, layout{}},
	} {
		dir := makeDir(t, tt.entries...)
		before := tree(t, dir)
		l, err := checkLayout(openRoot(t, dir))
		if err != nil {
			t.Errorf("%s: checkLayout() = %v", tt.what, err)
			continue
		}
		if !reflect.DeepEqual(*l, tt.want) {
			t.Errorf("%s: checkLayout() = %+v, want %+v", tt.what, *l, tt.want)
		}
		checkTree(t, tt.what, dir, before)
	}
}

// TestCheckLayoutForeign checks that checkLayout reports everything in an output directory that the build has not
// made, sorted by path, and leaves it as it is.
func TestCheckLayoutForeign(t *testing.T) {
	const (
		lockMust    = "it must be a regular file"
		currentMust = "it must be a symbolic link to a release"
	)
	for _, tt := range []struct {
		what    string
		entries []string
		want    []string // the paths in the output directory and what the errors require of them
	}{
		{"a file", []string{"README", ".lock"}, []string{"README: " + outMust}},
		{"a hidden file", []string{".htaccess"}, []string{".htaccess: " + outMust}},
		{"a directory", []string{"public/index.html"}, []string{"public: " + outMust}},
		{"a link", []string{"www -> releases/" + releaseA}, []string{"www: " + outMust}},
		{"a link named like a release", []string{randomA + " -> releases/" + releaseA}, []string{randomA + ": " + outMust}},
		{"a directory .lock", []string{".lock/"}, []string{".lock: " + lockMust}},
		{"a link .lock", []string{".lock -> README", "README"}, []string{".lock: " + lockMust, "README: " + outMust}},
		{"a file releases", []string{"releases"}, []string{"releases: it must be a directory"}},
		{
			"a link releases",
			[]string{"releases -> other", "other/" + releaseA + "/"},
			[]string{"other: " + outMust, "releases: it must be a directory"},
		},
		{"a directory current", []string{"current/"}, []string{"current: " + currentMust}},
		{"a file current", []string{"current"}, []string{"current: " + currentMust}},
		{
			"a current to elsewhere",
			[]string{"current -> /srv/www", "releases/" + releaseA + "/"},
			[]string{`current: it points to "/srv/www", not to a release`},
		},
		{
			"a current to a path in a release",
			[]string{"current -> releases/" + releaseA + "/public", "releases/" + releaseA + "/public/"},
			[]string{`current: it points to "releases/` + releaseA + `/public", not to a release`},
		},
		{
			"a current to another name",
			[]string{"current -> releases/latest", "releases/latest/"},
			[]string{`current: it points to "releases/latest", not to a release`, "releases/latest: " + releasesMust},
		},
		{
			"a current with a dot",
			[]string{"current -> ./releases/" + releaseA, "releases/" + releaseA + "/"},
			[]string{`current: it points to "./releases/` + releaseA + `", not to a release`},
		},
		{
			"a current to a name",
			[]string{"current -> " + releaseA, "releases/" + releaseA + "/"},
			[]string{`current: it points to "` + releaseA + `", not to a release`},
		},
		{
			"temporary names that the build does not make",
			[]string{
				"current.tmp-" + strings.ToUpper(randomA) + " -> releases/" + releaseA,
				"current.tmp-" + randomA,
				"current.tmp-" + randomB + "/",
				"releases/.tmp-" + randomA,
				"releases/.tmp-" + randomB + "0/",
				"releases/.tmp-x/",
				"releases/tmp-" + randomA + "/",
			},
			[]string{
				"current.tmp-" + strings.ToUpper(randomA) + ": " + outMust,
				"current.tmp-" + randomA + ": " + outMust,
				"current.tmp-" + randomB + ": " + outMust,
				"releases/.tmp-" + randomA + ": " + releasesMust,
				"releases/.tmp-" + randomB + "0: " + releasesMust,
				"releases/.tmp-x: " + releasesMust,
				"releases/tmp-" + randomA + ": " + releasesMust,
			},
		},
		{
			"releases that the build does not make",
			[]string{
				"releases/" + releaseA,
				"releases/" + strings.ToUpper(releaseB) + "/",
				"releases/" + releaseC + "0/",
				"releases/" + releaseA[:15] + "/",
				"releases/old -> " + releaseC,
				"releases/" + releaseC + "/",
			},
			[]string{
				"releases/" + releaseC + "0: " + releasesMust,
				"releases/" + releaseA[:15] + ": " + releasesMust,
				"releases/" + releaseA + ": " + releasesMust,
				"releases/" + strings.ToUpper(releaseB) + ": " + releasesMust,
				"releases/old: " + releasesMust,
			},
		},
		{
			"sorted by path", // "releases-old" and "releases.bak" before "releases/x"
			[]string{"releases/x", "releases-old/", "releases.bak", "current/", "README", ".lock"},
			[]string{
				"README: " + outMust,
				"current: " + currentMust,
				"releases-old: " + outMust,
				"releases.bak: " + outMust,
				"releases/x: " + releasesMust,
			},
		},
	} {
		dir := makeDir(t, tt.entries...)
		before := tree(t, dir)
		l, err := checkLayout(openRoot(t, dir))
		var want []string
		for _, w := range tt.want {
			name, must, _ := strings.Cut(w, ": ")
			want = append(want, filepath.Join(dir, name)+": not made by the build; "+must)
		}
		if l != nil || err == nil || err.Error() != strings.Join(want, "\n") {
			t.Errorf("%s: checkLayout() = %+v, %v\nwant the errors:\n%s", tt.what, l, err, strings.Join(want, "\n"))
		}
		checkTree(t, tt.what, dir, before)
	}
}

// TestUnique checks that unique tries names of the prefix and 16 random hex digits until create does not find one.
func TestUnique(t *testing.T) {
	pattern := regexp.MustCompile(`^x-[0-9a-f]{16}$`)
	var names []string
	name, err := unique("x-", func(name string) error {
		names = append(names, name)
		if len(names) < 3 {
			return &fs.PathError{Op: "mkdirat", Path: name, Err: fs.ErrExist}
		}
		return nil
	})
	if err != nil || len(names) != 3 || name != names[2] || names[0] == names[1] || names[1] == names[2] {
		t.Errorf("unique() = %q, %v after %q", name, err, names)
	}
	for _, n := range names {
		if !pattern.MatchString(n) {
			t.Errorf("unique() tried %q", n)
		}
	}

	names = nil
	if name, err := unique("x-", func(name string) error {
		names = append(names, name)
		return fs.ErrExist
	}); name != "" || !errors.Is(err, fs.ErrExist) || len(names) != 10 {
		t.Errorf("unique() = %q, %v after %d names, want the error after 10", name, err, len(names))
	}
	failure := errors.New("failure")
	if name, err := unique("x-", func(string) error { return failure }); !pattern.MatchString(name) ||
		!errors.Is(err, failure) {
		t.Errorf("unique() = %q, %v, want a name and %v", name, err, failure)
	}
}

// TestNewRelease checks the temporary directories of releases, with their public directories.
func TestNewRelease(t *testing.T) {
	dir := t.TempDir()
	root := openRoot(t, dir)
	pattern := regexp.MustCompile(`^releases/\.tmp-[0-9a-f]{16}$`)
	var tmps []string
	for range 2 {
		tmp, rel, err := newRelease(root)
		if err != nil {
			t.Fatal(err)
		}
		if !pattern.MatchString(tmp) || rel.Name() != filepath.Join(dir, tmp) {
			t.Errorf("newRelease() = %q, a root of %q", tmp, rel.Name())
		}
		if err := writeFile(rel, "public/index.html", []byte("data")); err != nil {
			t.Error(err)
		}
		_ = rel.Close()
		tmps = append(tmps, tmp)
	}
	if tmps[0] == tmps[1] {
		t.Errorf("newRelease() made %s twice", tmps[0])
	}
	checkTree(t, "newRelease()", dir, tree(t, makeDir(t, tmps[0]+"/public/index.html", tmps[1]+"/public/index.html")))
}

// TestReleaseName checks that the name of a release depends on the paths and the bytes of its files only, with the
// paths in the order of bytes.
func TestReleaseName(t *testing.T) {
	files := map[string]string{
		"public/index.html":             "<p>index</p>\n",
		"public/css/site.css":           "p { color: red; }\n",
		"public/media/med_1/写真.jpg":     "\xff\xd8\xff",
		"public/a-b":                    "a-b", // before public/a/b, which a walk visits first
		"public/a/b":                    "a/b",
		"public/.htaccess":              "DirectoryIndex index.html index.xml\n",
		"public/news/page/2/index.html": "",
		"server.json":                   "{}\n",
		"nginx.conf":                    "index index.html index.xml;\n",
	}
	paths := slices.Sorted(maps.Keys(files))
	var list strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&list, "%s\t%x\n", p, sha256.Sum256([]byte(files[p])))
	}
	want := fmt.Sprintf("%x", sha256.Sum256([]byte(list.String())))[:16]

	// nameOf writes files into a new directory in the order of order, and returns the name of the release in it.
	nameOf := func(files map[string]string, order []string) string {
		t.Helper()
		root := openRoot(t, t.TempDir())
		for _, p := range order {
			if err := writeFile(root, p, []byte(files[p])); err != nil {
				t.Fatal(err)
			}
		}
		public := 0
		for p := range files {
			if strings.HasPrefix(p, "public/") {
				public++
			}
		}
		var names []string
		for _, workers := range []int{1, 4} {
			name, n, err := releaseName(t.Context(), root, workers)
			if err != nil || n != public {
				t.Errorf("releaseName() with %d workers = %q, %d, %v, want %d files", workers, name, n, err, public)
			}
			names = append(names, name)
		}
		if names[0] != names[1] {
			t.Errorf("releaseName() = %q with 1 worker and %q with 4", names[0], names[1])
		}
		return names[0]
	}
	reversed := slices.Clone(paths)
	slices.Reverse(reversed)
	for _, order := range [][]string{paths, reversed} {
		if got := nameOf(files, order); got != want {
			t.Errorf("releaseName() = %q, want %q", got, want)
		}
	}
	for _, tt := range []struct {
		what   string
		change func(files map[string]string)
	}{
		{"one byte", func(files map[string]string) { files["public/index.html"] = "<p>index</p>\r" }},
		{"another path", func(files map[string]string) {
			files["public/a-c"] = files["public/a-b"]
			delete(files, "public/a-b")
		}},
		{"an empty file", func(files map[string]string) { files["public/empty"] = "" }},
		{"a file beside public", func(files map[string]string) { files["build.log"] = "" }},
	} {
		changed := maps.Clone(files)
		tt.change(changed)
		if got := nameOf(changed, slices.Sorted(maps.Keys(changed))); got == want || !isHex16(got) {
			t.Errorf("releaseName() with %s = %q, want another name than %q", tt.what, got, want)
		}
	}

	dir := t.TempDir()
	root := openRoot(t, dir)
	for _, p := range paths {
		if err := writeFile(root, p, []byte(files[p])); err != nil {
			t.Fatal(err)
		}
	}
	if err := root.MkdirAll("public/empty/dir", 0o755); err != nil {
		t.Fatal(err)
	}
	if got, _, err := releaseName(t.Context(), root, 2); got != want || err != nil {
		t.Errorf("releaseName() with an empty directory = %q, %v, want %q", got, err, want)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, _, err := releaseName(ctx, root, 2); got != "" || err == nil || err.Error() != context.Canceled.Error() {
		t.Errorf("releaseName() canceled = %q, %v, want %v", got, err, context.Canceled)
	}
	if err := os.Symlink("index.html", filepath.Join(dir, "public", "link")); err != nil {
		t.Fatal(err)
	}
	wantErr := dir + ": public/link: not a regular file"
	if got, _, err := releaseName(t.Context(), root, 2); got != "" || err == nil || err.Error() != wantErr {
		t.Errorf("releaseName() with a link = %q, %v, want the error %q", got, err, wantErr)
	}
}

// TestPublish checks that publish renames a temporary release, or removes it if its release exists, and points current
// to the release with a relative link.
func TestPublish(t *testing.T) {
	const tmp = "releases/.tmp-" + randomA
	base := []string{".lock", "releases/" + releaseA + "/public/a", "releases/" + releaseB + "/public/b"}
	for _, tt := range []struct {
		what     string
		current  string // that current points to before, if any
		name     string
		changed  bool
		want     []string // what the output directory holds then
		previous string
	}{
		{
			"a new release", releaseA, releaseC, true,
			[]string{"releases/" + releaseC + "/public/new", "current -> releases/" + releaseC},
			releaseA,
		},
		{"the release of current", releaseA, releaseA, false, []string{"current -> releases/" + releaseA}, releaseA},
		{"another release", releaseA, releaseB, true, []string{"current -> releases/" + releaseB}, releaseA},
		{"the first release", "", releaseC, true,
			[]string{"releases/" + releaseC + "/public/new", "current -> releases/" + releaseC}, ""},
		{"no current yet", "", releaseB, true, []string{"current -> releases/" + releaseB}, ""},
	} {
		entries := append(slices.Clone(base), tmp+"/public/new")
		if tt.current != "" {
			entries = append(entries, "current -> releases/"+tt.current)
		}
		dir := makeDir(t, entries...)
		changed, err := publish(openRoot(t, dir), tmp, tt.name, tt.previous)
		if changed != tt.changed || err != nil {
			t.Errorf("%s: publish() = %v, %v, want %v", tt.what, changed, err, tt.changed)
		}
		checkTree(t, tt.what, dir, tree(t, makeDir(t, append(slices.Clone(base), tt.want...)...)))
	}

	dir := makeDir(t, ".lock", "releases/"+releaseA, tmp+"/public/new")
	before := tree(t, dir)
	wantErr := filepath.Join(dir, "releases", releaseA) + ": not made by the build; " + releasesMust
	if changed, err := publish(openRoot(t, dir), tmp, releaseA, ""); changed || err == nil || err.Error() != wantErr {
		t.Errorf("publish() over a file = %v, %v, want the error %q", changed, err, wantErr)
	}
	checkTree(t, "publish() over a file", dir, before)
}

// TestRemoveOld checks that removeOld removes the releases that it does not keep and what builds that did not end left,
// and nothing else.
func TestRemoveOld(t *testing.T) {
	entries := []string{
		".lock",
		"releases/" + releaseA + "/public/index.html",
		"releases/" + releaseB + "/public/index.html",
		"releases/" + releaseC + "/public/index.html",
		"releases/.tmp-" + randomA + "/public/index.html",
		"current -> releases/" + releaseB,
		"current.tmp-" + randomB + " -> releases/" + releaseC,
	}
	for _, tt := range []struct {
		keep []string
		want []string
	}{
		{
			[]string{releaseB, releaseC},
			[]string{
				".lock", "releases/" + releaseB + "/public/index.html", "releases/" + releaseC + "/public/index.html",
				"current -> releases/" + releaseB, "README",
			},
		},
		{
			[]string{releaseB, ""},
			[]string{".lock", "releases/" + releaseB + "/public/index.html", "current -> releases/" + releaseB, "README"},
		},
		{
			[]string{releaseA, releaseB, releaseC},
			[]string{
				".lock", "releases/" + releaseA + "/public/index.html", "releases/" + releaseB + "/public/index.html",
				"releases/" + releaseC + "/public/index.html", "current -> releases/" + releaseB, "README",
			},
		},
	} {
		dir := makeDir(t, entries...)
		root := openRoot(t, dir)
		l, err := checkLayout(root)
		if err != nil {
			t.Fatal(err)
		}
		// Not in l: removeOld leaves it.
		if err := os.WriteFile(filepath.Join(dir, "README"), []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := removeOld(root, l, tt.keep); err != nil {
			t.Errorf("removeOld(%q) = %v", tt.keep, err)
		}
		checkTree(t, fmt.Sprintf("removeOld(%q)", tt.keep), dir, tree(t, makeDir(t, tt.want...)))
	}
}
