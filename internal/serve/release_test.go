package serve

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Names of releases in the tests.
const (
	releaseA = "0123456789abcdef"
	releaseB = "fedcba9876543210"
)

// testList returns server.json of a site at the root of its host in Japanese and English, with the redirect of 6.11.7
// from the URL of a post in WordPress.
func testList() serverList {
	return serverList{
		Redirects: []serverRedirect{
			{From: "/?p=123", To: "/2026/10/26/%E6%96%B0%E3%82%B5%E3%83%BC%E3%83%93%E3%82%B9/", Status: 301},
		},
		NotFound: []notFoundPage{{Prefix: "/en/", Page: "/en/404.html"}, {Prefix: "/", Page: "/404.html"}},
		Index:    []string{"index.html", "index.xml"},
	}
}

// writeFile writes data to name, a path with "/" in dir, making the directories that it needs.
func writeFile(t *testing.T, dir, name, data string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeRelease writes the release name into out, an output directory: its public directory with files, by their paths
// with "/", and server.json with list.
func writeRelease(t *testing.T, out, name string, list serverList, files map[string]string) {
	t.Helper()
	dir := filepath.Join(out, "releases", name)
	if err := os.MkdirAll(filepath.Join(dir, "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	for p, data := range files {
		writeFile(t, filepath.Join(dir, "public"), p, data)
	}
	data, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "server.json", string(data))
}

// setCurrent points out/current to target at once, as the build does: by renaming a new symbolic link over it.
func setCurrent(t *testing.T, out, target string) {
	t.Helper()
	link := filepath.Join(out, "current.tmp")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(link, filepath.Join(out, "current")); err != nil {
		t.Fatal(err)
	}
}

// newHandler returns the Handler of out without a log, which it closes at the end of the test.
func newHandler(t *testing.T, out string) *Handler {
	t.Helper()
	h, err := New(out, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

// TestBase checks Base, and the segments of the base that requests are compared with, for sites at the root of their
// hosts and under paths.
func TestBase(t *testing.T) {
	for _, tt := range []struct {
		base string
		segs []string
	}{
		{"", nil},
		{"/blog", []string{"blog"}},
		{"/ブ%20ログ", []string{"ブ ログ"}},
		{"/a/b", []string{"a", "b"}},
		{"/a(b)", []string{"a(b)"}},
		{"/a%28b%29", []string{"a(b)"}},
		{"/a%2Fb", []string{"a/b"}},
		{"/%3F", []string{"?"}},
	} {
		out := t.TempDir()
		list := testList()
		list.Base = tt.base
		writeRelease(t, out, releaseA, list, nil)
		setCurrent(t, out, "releases/"+releaseA)
		h := newHandler(t, out)
		if got, err := h.Base(); got != tt.base || err != nil {
			t.Errorf("Base() = %q, %v, want %q", got, err, tt.base)
		}
		rel, err := h.current()
		if err != nil {
			t.Fatal(err)
		}
		if rel.name != releaseA || !slices.Equal(rel.baseSegs, tt.segs) {
			t.Errorf("base %q: release %q with the segments %q, want %q with %q", tt.base, rel.name, rel.baseSegs,
				releaseA, tt.segs)
		}
		if h.log == nil {
			t.Error("New(out, nil) has no log")
		}
	}
}

// TestNewErrors checks that New fails for output directories without a release that it can serve, and says why.
func TestNewErrors(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "out")
		if h, err := New(out, nil); h != nil || !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("New(%q) = %v, %v, want an error about the missing directory", out, h, err)
		}
	})
	t.Run("empty", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		if err := os.Mkdir("out", 0o755); err != nil {
			t.Fatal(err)
		}
		h, err := New("out", nil)
		want := "no release is published in " + filepath.Join(dir, "out")
		if h != nil || !errors.Is(err, errNoRelease) || err.Error() != want {
			t.Errorf("New(%q) = %v, %v, want %q", "out", h, err, want)
		}
	})
	link := func(target string) func(t *testing.T, out string) {
		return func(t *testing.T, out string) { setCurrent(t, out, target) }
	}
	notRelease := func(target string) string {
		return ": current points to " + strconv.Quote(target) + ", not to a release"
	}
	for _, tt := range []struct {
		what    string
		current func(t *testing.T, out string)
		want    string // the error after the path of the output directory
	}{
		{"a name", link("releases/xyz"), notRelease("releases/xyz")},
		{"elsewhere", link("/var/www"), notRelease("/var/www")},
		{"upper case", link("releases/" + strings.ToUpper(releaseA)), notRelease("releases/" + strings.ToUpper(releaseA))},
		{"a dot", link("./releases/" + releaseA), notRelease("./releases/" + releaseA)},
		{"a trailing slash", link("releases/" + releaseA + "/"), notRelease("releases/" + releaseA + "/")},
		{"the public directory", link("releases/" + releaseA + "/public"), notRelease("releases/" + releaseA + "/public")},
		{"the name alone", link(releaseA), notRelease(releaseA)},
		{
			"a directory",
			func(t *testing.T, out string) {
				if err := os.Mkdir(filepath.Join(out, "current"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			": current is not a symbolic link",
		},
		{
			"a file",
			func(t *testing.T, out string) { writeFile(t, out, "current", "releases/"+releaseA) },
			": current is not a symbolic link",
		},
		{
			"a release that does not exist",
			link("releases/" + releaseB),
			"/releases/" + releaseB + "/server.json: no such file or directory",
		},
		{
			"a release without server.json",
			func(t *testing.T, out string) {
				if err := os.Remove(filepath.Join(out, "releases", releaseA, "server.json")); err != nil {
					t.Fatal(err)
				}
				setCurrent(t, out, "releases/"+releaseA)
			},
			"/releases/" + releaseA + "/server.json: no such file or directory",
		},
		{
			"an invalid server.json",
			func(t *testing.T, out string) {
				dir := filepath.Join(out, "releases", releaseA)
				writeFile(t, dir, "server.json", `{"base": "/blog/", "index": ["index.html"]}`)
				setCurrent(t, out, "releases/"+releaseA)
			},
			`/releases/` + releaseA + `/server.json: base "/blog/" ends with "/"`,
		},
	} {
		out := t.TempDir()
		writeRelease(t, out, releaseA, testList(), nil)
		tt.current(t, out)
		h, err := New(out, nil)
		if want := out + tt.want; h != nil || err == nil || err.Error() != want {
			t.Errorf("%s: New() = %v, %v, want %q", tt.what, h, err, want)
		}
		if errors.Is(err, errNoRelease) {
			t.Errorf("%s: New() = %v, which is errNoRelease", tt.what, err)
		}
	}
}

// TestCurrentUnreadable checks the error for an output directory in which current cannot be read.
func TestCurrentUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any directory")
	}
	out := t.TempDir()
	writeRelease(t, out, releaseA, testList(), nil)
	setCurrent(t, out, "releases/"+releaseA)
	h := newHandler(t, out)
	if err := os.Chmod(out, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(out, 0o755) })
	if got, err := h.Base(); got != "" || !errors.Is(err, fs.ErrPermission) || !strings.HasPrefix(err.Error(), out+": ") {
		t.Errorf("Base() = %q, %v, want an error about the permission after %q", got, err, out+": ")
	}
}

// TestParseRelease checks that parseRelease takes server.json as the build writes it, such as that of the sample
// site, whose redirects then match.
func TestParseRelease(t *testing.T) {
	data, err := os.ReadFile("../build/testdata/out/server.json")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := parseRelease(releaseA, data)
	if err != nil {
		t.Fatalf("parseRelease() = %v", err)
	}
	want := []notFoundPage{{Prefix: "/en/", Page: "/en/404.html"}, {Prefix: "/", Page: "/404.html"}}
	if rel.name != releaseA || rel.base != "" || rel.baseSegs != nil || !slices.Equal(rel.notFound, want) ||
		!slices.Equal(rel.index, []string{"index.html", "index.xml"}) {
		t.Errorf("parseRelease() = %+v", rel)
	}
	for _, tt := range []struct{ target, from string }{
		{"/?utm_source=x&p=123", "/?p=123"},
		{"/company", "/company/"},
		{"/wp-content/uploads/2024/04/%E4%BC%9A%E7%A4%BE%E6%A1%88%E5%86%85.pdf", "/wp-content/uploads/2024/04/会社案内.pdf"},
		{"/about/", ""},
	} {
		if got := fromOf(rel.redirects.match(tt.target)); got != tt.from {
			t.Errorf("match(%q) = the redirect from %q, want %q", tt.target, got, tt.from)
		}
	}

	// A site under a path in Japanese with a space, with prefixDefaultLanguage and no redirects.
	data = []byte(`{
  "base": "/ブ%20ログ",
  "redirects": null,
  "notFound": [
    {"prefix": "/ja/", "page": "/ja/404.html"},
    {"prefix": "/en/", "page": "/en/404.html"},
    {"prefix": "/", "page": "/ja/404.html"}
  ],
  "index": ["index.html", "index.xml"]
}`)
	if rel, err = parseRelease(releaseB, data); err != nil {
		t.Fatalf("parseRelease() = %v", err)
	}
	if rel.base != "/ブ%20ログ" || !slices.Equal(rel.baseSegs, []string{"ブ ログ"}) || len(rel.notFound) != 3 ||
		rel.redirects.match("/") != nil {
		t.Errorf("parseRelease() = %+v", rel)
	}
}

// TestParseReleaseJSON checks that parseRelease rejects data that is not JSON, or that has members that server.json
// does not have, or values of other types.
func TestParseReleaseJSON(t *testing.T) {
	syntactic := func(err error) bool {
		var e *jsontext.SyntacticError
		return errors.As(err, &e)
	}
	semantic := func(err error) bool {
		var e *json.SemanticError
		return errors.As(err, &e)
	}
	unknown := func(err error) bool { return errors.Is(err, json.ErrUnknownName) }
	for _, tt := range []struct {
		data string
		is   func(error) bool
	}{
		{"", syntactic},
		{`{"index": ["index.html"]`, syntactic},
		{`{"index": ["index.html"]} {}`, syntactic},
		{`{"index": ["index.html"], "index": ["index.xml"]}`, syntactic},
		{"{\"base\": \"/\xff\", \"index\": [\"index.html\"]}", syntactic},
		{`[]`, semantic},
		{`{"base": 1, "index": ["index.html"]}`, semantic},
		{`{"redirects": [{"from": "/a/", "to": "/b/", "status": "301"}], "index": ["index.html"]}`, semantic},
		{`{"index": ["index.html"], "extra": 1}`, unknown},
		{`{"Index": ["index.html"]}`, unknown},
		{`{"redirects": [{"from": "/a/", "to": "/b/", "status": 301, "x": 1}], "index": ["index.html"]}`, unknown},
		{`{"notFound": [{"prefix": "/", "page": "/404.html", "language": "ja"}], "index": ["index.html"]}`, unknown},
	} {
		if rel, err := parseRelease(releaseA, []byte(tt.data)); rel != nil || !tt.is(err) {
			t.Errorf("parseRelease(%q) = %+v, %v", tt.data, rel, err)
		}
	}
}

// TestParseReleaseErrors checks that parseRelease rejects server.json that the build would not write, and names what
// is wrong.
func TestParseReleaseErrors(t *testing.T) {
	redirects := func(rs ...serverRedirect) func(l *serverList) {
		return func(l *serverList) { l.Redirects = append(l.Redirects, rs...) }
	}
	notFound := func(prefix, page string) func(l *serverList) {
		return func(l *serverList) { l.NotFound = append(l.NotFound, notFoundPage{prefix, page}) }
	}
	index := func(names ...string) func(l *serverList) {
		return func(l *serverList) { l.Index = names }
	}
	base := func(s string) func(l *serverList) {
		return func(l *serverList) { l.Base = s }
	}
	for _, tt := range []struct {
		change func(l *serverList)
		want   string
	}{
		{base("/blog/"), `base "/blog/" ends with "/"`},
		{base("/"), `base "/" ends with "/"`},
		{base("blog"), `base "blog" does not start with "/"`},
		{base("/ブ ログ"), `base "/ブ ログ" is not in the canonical form`},
		{base("/a%2f"), `base "/a%2f" is not in the canonical form`},
		{base("/a?b"), `base "/a?b" has a query`},
		{base("/a?"), `base "/a?" has a query`},
		{base("/a%zz"), `base "/a%zz" has an invalid percent-encoding`},
		{base("/a/%"), `base "/a/%" has an invalid percent-encoding`},
		{base("//x"), `base "//x" has an empty or a dot segment`},
		{base("/a//b"), `base "/a//b" has an empty or a dot segment`},
		{base("/a/./b"), `base "/a/./b" has an empty or a dot segment`},
		{base("/.."), `base "/.." has an empty or a dot segment`},
		{
			redirects(serverRedirect{"/a/", "/b/", 301}, serverRedirect{"/b?b=1&a=1", "/c/", 301}),
			`redirects[2]: from "/b?b=1&a=1" is not in the canonical form`,
		},
		{redirects(serverRedirect{"/%E6%96%B0/", "/b/", 301}), `redirects[1]: from "/%E6%96%B0/" is not in the canonical form`},
		{redirects(serverRedirect{"about/", "/b/", 301}), `redirects[1]: from "about/" does not start with "/"`},
		{redirects(serverRedirect{"", "/b/", 301}), `redirects[1]: from "" does not start with "/"`},
		{redirects(serverRedirect{"/a/", "", 301}), `redirects[1]: to is empty`},
		{redirects(serverRedirect{"/a/", "/b/", 307}), `redirects[1]: status 307 is not 301 or 302`},
		{redirects(serverRedirect{From: "/a/", To: "/b/"}), `redirects[1]: status 0 is not 301 or 302`},
		{notFound("en/", "/en/404.html"), `notFound[2]: prefix "en/" is not a path that ends with "/"`},
		{notFound("/en", "/en/404.html"), `notFound[2]: prefix "/en" is not a path that ends with "/"`},
		{notFound("", "/404.html"), `notFound[2]: prefix "" is not a path that ends with "/"`},
		{notFound("/en/", "/en/"), `notFound[2]: page "/en/" is not the path of a file to serve`},
		{notFound("/en/", "/"), `notFound[2]: page "/" is not the path of a file to serve`},
		{notFound("/en/", "/.x.html"), `notFound[2]: page "/.x.html" is not the path of a file to serve`},
		{notFound("/en/", "en/404.html"), `notFound[2]: page "en/404.html" is not the path of a file to serve`},
		{notFound("/en/", "/en//404.html"), `notFound[2]: page "/en//404.html" is not the path of a file to serve`},
		{notFound("/en/", ""), `notFound[2]: page "" is not the path of a file to serve`},
		{index(), "index is empty"},
		{index(".htaccess"), `index[0]: ".htaccess" is not the name of a file to serve`},
		{index("a/b"), `index[0]: "a/b" is not the name of a file to serve`},
		{index("."), `index[0]: "." is not the name of a file to serve`},
		{index("index.html", ""), `index[1]: "" is not the name of a file to serve`},
		{index("index.html", "a\x00b"), `index[1]: "a\x00b" is not the name of a file to serve`},
	} {
		list := testList()
		tt.change(&list)
		data, err := json.Marshal(list)
		if err != nil {
			t.Fatal(err)
		}
		if rel, err := parseRelease(releaseA, data); rel != nil || err == nil || err.Error() != tt.want {
			t.Errorf("parseRelease(%s) = %+v, %v, want %q", data, rel, err, tt.want)
		}
	}
	for _, data := range []string{`{"index": null}`, `{"redirects": [], "notFound": []}`, "{}", "null"} {
		if rel, err := parseRelease(releaseA, []byte(data)); rel != nil || err == nil || err.Error() != "index is empty" {
			t.Errorf("parseRelease(%s) = %+v, %v, want %q", data, rel, err, "index is empty")
		}
	}
}

// TestReleaseCache checks that the Handler reads server.json again only for another release than the one that it has
// loaded last, and keeps only valid releases.
func TestReleaseCache(t *testing.T) {
	out := t.TempDir()
	list := testList()
	list.Base = "/a"
	writeRelease(t, out, releaseA, list, nil)
	list.Base = "/b"
	writeRelease(t, out, releaseB, list, nil)
	setCurrent(t, out, "releases/"+releaseA)
	h := newHandler(t, out)
	checkBase := func(want string) {
		t.Helper()
		if got, err := h.Base(); got != want || err != nil {
			t.Errorf("Base() = %q, %v, want %q", got, err, want)
		}
	}
	checkBase("/a")
	list.Base = "/x"
	writeRelease(t, out, releaseA, list, nil)
	checkBase("/a")
	setCurrent(t, out, "releases/"+releaseB)
	checkBase("/b")
	setCurrent(t, out, "releases/"+releaseA)
	checkBase("/x")

	writeFile(t, filepath.Join(out, "releases", releaseB), "server.json", "{}")
	setCurrent(t, out, "releases/"+releaseB)
	want := filepath.Join(out, "releases", releaseB, "server.json") + ": index is empty"
	if got, err := h.Base(); got != "" || err == nil || err.Error() != want {
		t.Errorf("Base() = %q, %v, want the error %q", got, err, want)
	}
	list.Base = "/c"
	writeRelease(t, out, releaseB, list, nil)
	checkBase("/c")

	if err := os.Remove(filepath.Join(out, "current")); err != nil {
		t.Fatal(err)
	}
	want = "no release is published in " + out
	if got, err := h.Base(); got != "" || !errors.Is(err, errNoRelease) || err.Error() != want {
		t.Errorf("Base() = %q, %v, want the error %q", got, err, want)
	}
	setCurrent(t, out, "releases/"+releaseB)
	checkBase("/c")
}
