package serve

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
)

const htmlType = "text/html; charset=utf-8"

// response is the response to a request: the status, the Location, the Content-Type and the body.
type response struct {
	status      int
	location    string
	contentType string
	body        string
}

// served returns the response with the file name of a public directory of publicFiles.
func served(name, contentType string) response { return response{http.StatusOK, "", contentType, name} }

func servedPage(name string) response { return served(name, htmlType) }

func redirectTo(status int, loc string) response { return response{status: status, location: loc} }

// notFoundWith returns the 404 response with the 404 page page of a public directory of publicFiles.
func notFoundWith(page string) response { return response{http.StatusNotFound, "", htmlType, page} }

// plain returns the response with status and text, as http.Error writes it.
func plain(status int, text string) response {
	return response{status, "", "text/plain; charset=utf-8", text + "\n"}
}

var (
	plainNotFound = plain(http.StatusNotFound, "404 page not found")
	notAllowed    = plain(http.StatusMethodNotAllowed, "405 method not allowed")
	serverError   = plain(http.StatusInternalServerError, "500 internal server error")
	unavailable   = plain(http.StatusServiceUnavailable, "503 service unavailable")
)

// publicFiles returns the files names of a public directory for writeRelease, each with its name as its content.
func publicFiles(names ...string) map[string]string {
	files := make(map[string]string, len(names))
	for _, n := range names {
		files[n] = n
	}
	return files
}

// newLogged returns the Handler of out with the buffer of its log, and closes it at the end of the test.
func newLogged(t *testing.T, out string) (*Handler, *bytes.Buffer) {
	t.Helper()
	logs := new(bytes.Buffer)
	h, err := New(out, log.New(logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h, logs
}

func do(h http.Handler, req *http.Request) *http.Response {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

// checkResponse checks that res, the response to what, is want, and that it has the headers of every response.
func checkResponse(t *testing.T, what string, res *http.Response, want response) {
	t.Helper()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	got := response{res.StatusCode, res.Header.Get("Location"), res.Header.Get("Content-Type"), string(body)}
	if got != want {
		t.Errorf("%s = %#v, want %#v", what, got, want)
	}
	for name, want := range map[string]string{
		"Content-Security-Policy": "script-src 'self'; style-src 'self'; object-src 'none'; base-uri 'self'",
		"X-Content-Type-Options":  "nosniff",
	} {
		if got := res.Header.Values(name); len(got) != 1 || got[0] != want {
			t.Errorf("%s has the %s %q, want %q", what, name, got, want)
		}
	}
}

// check checks the response of h to a request with method for target, which is sent as it is.
func check(t *testing.T, h http.Handler, method, target string, want response) {
	t.Helper()
	checkResponse(t, method+" "+target, do(h, httptest.NewRequest(method, target, nil)), want)
}

// getCase is a GET request for target, sent as it is, and the response that it must get.
type getCase struct {
	target string
	want   response
}

// TestServe checks the responses for a site at the root of its host: its pages, files and directory index, the
// redirects, which come first, the trailing "/" of directories, and the 404 pages, which hidden files, dot segments
// and the like get; and that none of them is logged.
func TestServe(t *testing.T) {
	const (
		post     = "/2026/10/26/%E6%96%B0%E3%82%B5%E3%83%BC%E3%83%93%E3%82%B9/"
		postFile = "2026/10/26/新サービス/index.html"
	)
	out := t.TempDir()
	list := testList()
	list.Redirects = []serverRedirect{
		{From: "/?a=1", To: "/to/a/", Status: 301},
		{From: "/?a=1&b=2", To: "/to/ab/", Status: 302},
		{From: "/?p=123", To: post, Status: 301},
		{From: "/a%7Cb/%24", To: "/to/pipe/", Status: 301},
		{From: "/about/?ref=old", To: "/about/", Status: 301},
		{From: "/old", To: "https://example.org/new/?x=1#y", Status: 301},
		{From: "/recruit/", To: "https://recruit.example.com/", Status: 302},
		{From: "/会社/a%2Fb", To: "/to/slash/", Status: 301},
		{From: "/旧/", To: "/about/", Status: 301},
	}
	writeRelease(t, out, releaseA, list, publicFiles(
		"index.html", "about/index.html", postFile, "feed/index.xml", "404.html", "en/index.html", "en/404.html",
		"a.html", "a/b.html", "css/x.css", "noindex/x.txt", "d/index.html/x.html", "d/index.xml", "dir.html/index.html",
		"x.HTML", "x.unknown", "LICENSE", ".htaccess", "a/.git/config",
	))
	setCurrent(t, out, "releases/"+releaseA)
	h, logs := newLogged(t, out)
	for _, tt := range []getCase{
		{"/", servedPage("index.html")},
		{"http://example.com", servedPage("index.html")},
		{"http://example.com/about/", servedPage("about/index.html")},
		{"/index.html", servedPage("index.html")},
		{"/about/", servedPage("about/index.html")},
		{post, servedPage(postFile)},
		{lowerHex(post), servedPage(postFile)},
		{"/2026/10/26/新サービス/", servedPage(postFile)},
		{"/feed/", served("feed/index.xml", "application/xml")},
		{"/en/", servedPage("en/index.html")},
		{"/d/", served("d/index.xml", "application/xml")},
		{"/noindex/", notFoundWith("404.html")},
		{"/about", redirectTo(301, "/about/")},
		{"/about?x=1", redirectTo(301, "/about/?x=1")},
		{"/feed", redirectTo(301, "/feed/")},
		{strings.TrimSuffix(post, "/"), redirectTo(301, post)},
		{"/2026/10/26/新サービス", redirectTo(301, "/2026/10/26/新サービス/")},
		{"/dir.html", redirectTo(301, "/dir.html/")},
		{"/dir.html/", servedPage("dir.html/index.html")},
		{"/a.html", servedPage("a.html")},
		{"/a/b.html", servedPage("a/b.html")},
		{"/css/x.css", served("css/x.css", "text/css; charset=utf-8")},
		{"/x.HTML", servedPage("x.HTML")},
		{"/x.unknown", served("x.unknown", "application/octet-stream")},
		{"/LICENSE", served("LICENSE", "application/octet-stream")},
		{"/a.html/", notFoundWith("404.html")},
		{"/a.html/x", notFoundWith("404.html")},
		{"/missing", notFoundWith("404.html")},
		{"/en/missing/", notFoundWith("en/404.html")},

		// The redirects come before the files.
		{"/?p=123", redirectTo(301, post)},
		{"/?utm_source=x&p=123", redirectTo(301, post)},
		{"/?p=123#x", redirectTo(301, post)},
		{"/?p=1234", servedPage("index.html")},
		{"/?b=2&a=1", redirectTo(302, "/to/ab/")},
		{"/?a=1&c=3", redirectTo(301, "/to/a/")},
		{"/?b=2", servedPage("index.html")},
		{"/about/?ref=old", redirectTo(301, "/about/")},
		{"/about?ref=old", redirectTo(301, "/about/")},
		{"/about/?ref=new", servedPage("about/index.html")},
		{"/old", redirectTo(301, "https://example.org/new/?x=1#y")},
		{"/old/?x=2", redirectTo(301, "https://example.org/new/?x=1#y")},
		{"/recruit/?utm=1", redirectTo(302, "https://recruit.example.com/")},
		{"/%E6%97%A7", redirectTo(301, "/about/")},
		{"/%e6%97%a7/", redirectTo(301, "/about/")},
		// The paths as they are sent, which browsers send with "|" and clients such as curl with UTF-8.
		{"/a|b/%24", redirectTo(301, "/to/pipe/")},
		{"/a%7Cb/%24", redirectTo(301, "/to/pipe/")},
		{"/a|b/$", notFoundWith("404.html")},
		{"/会社/a%2Fb", redirectTo(301, "/to/slash/")},
		{"/%E4%BC%9A%E7%A4%BE/a%2Fb", redirectTo(301, "/to/slash/")},
		{"/会社/a/b", notFoundWith("404.html")},

		// Hidden files, dot and empty segments, an encoded "/" and names that the build never makes.
		{"/.htaccess", notFoundWith("404.html")},
		{"/.htaccess?x=1", notFoundWith("404.html")},
		{"/%2Ehtaccess", notFoundWith("404.html")},
		{"/%2ehtaccess", notFoundWith("404.html")},
		{"/a/.git/config", notFoundWith("404.html")},
		{"/en/.htaccess", notFoundWith("en/404.html")},
		{"/a/../about/", notFoundWith("404.html")},
		{"/a/%2E%2E/about/", notFoundWith("404.html")},
		{"/about/..", notFoundWith("404.html")},
		{"/./", notFoundWith("404.html")},
		{"/.", notFoundWith("404.html")},
		{"//", notFoundWith("404.html")},
		{"//x", notFoundWith("404.html")},
		{"//about/", notFoundWith("404.html")},
		{"/about//", notFoundWith("404.html")},
		{"/a%2Fb.html", notFoundWith("404.html")},
		{"/a%2fb.html", notFoundWith("404.html")},
		{"/a%00b", notFoundWith("404.html")},
		{"/a%01b/", notFoundWith("404.html")},
		{"/%FF", notFoundWith("404.html")},
	} {
		check(t, h, http.MethodGet, tt.target, tt.want)
	}
	if logs.Len() > 0 {
		t.Errorf("the requests have logged %q", logs)
	}
}

// TestServeBase checks the responses for sites under paths, which requests must start with, decoded segment by
// segment, and which they get with the trailing "/" before the redirects; other paths get a plain 404.
func TestServeBase(t *testing.T) {
	for _, tt := range []struct {
		base  string // in server.json
		link  string // in the links of the site, as render.EncodeURL writes it
		cases []getCase
	}{
		{"/blog", "/blog", []getCase{
			{"/%62log/", servedPage("index.html")},
			{"/Blog/", plainNotFound},
			{"/blog//", notFoundWith("404.html")},
		}},
		{"/ブ%20ログ", "/%E3%83%96%20%E3%83%AD%E3%82%B0", []getCase{
			{"/%e3%83%96%20%e3%83%ad%e3%82%b0/", servedPage("index.html")},
			{"/%e3%83%96%20%e3%83%ad%e3%82%b0", redirectTo(301, "/%e3%83%96%20%e3%83%ad%e3%82%b0/")},
			{"/ブ%20ログ/about/", servedPage("about/index.html")},
			{"/%E3%83%96+%E3%83%AD%E3%82%B0/", plainNotFound},
		}},
		{"/a(b)", "/a%28b%29", []getCase{
			{"/a(b)/", servedPage("index.html")},
			{"/a(b)", redirectTo(301, "/a(b)/")},
			{"/a(b)/old/", redirectTo(301, "/a%28b%29/new/")},
			{"/a%28b%29x/", plainNotFound},
			{"/a/", plainNotFound},
		}},
		{"/a%2Fb", "/a%2Fb", []getCase{
			{"/a%2fb/", servedPage("index.html")},
			{"/a/b/", plainNotFound},
			{"/a/", plainNotFound},
		}},
		{"/a/b", "/a/b", []getCase{
			{"/a/b/c/", notFoundWith("404.html")},
			{"/a/", plainNotFound},
			{"/a", plainNotFound},
			{"/a%2Fb/", plainNotFound},
		}},
	} {
		out := t.TempDir()
		list := serverList{
			Base: tt.base,
			Redirects: []serverRedirect{
				{From: "/?p=123", To: tt.link + "/new/", Status: 301},
				{From: "/old/", To: tt.link + "/new/", Status: 301},
			},
			NotFound: []notFoundPage{{Prefix: "/", Page: "/404.html"}},
			Index:    []string{"index.html", "index.xml"},
		}
		writeRelease(t, out, releaseA, list, publicFiles("index.html", "about/index.html", "404.html"))
		setCurrent(t, out, "releases/"+releaseA)
		h, logs := newLogged(t, out)
		l := tt.link
		for _, c := range append([]getCase{
			{l + "/", servedPage("index.html")},
			{l, redirectTo(301, l+"/")},
			{l + "?x=1", redirectTo(301, l+"/?x=1")},
			{l + "?p=123", redirectTo(301, l+"/?p=123")},
			{l + "/?p=123", redirectTo(301, l+"/new/")},
			{l + "/about", redirectTo(301, l+"/about/")},
			{l + "/about?x=1", redirectTo(301, l+"/about/?x=1")},
			{l + "/about/", servedPage("about/index.html")},
			{l + "/old", redirectTo(301, l+"/new/")},
			{l + "/old/?x=1", redirectTo(301, l+"/new/")},
			{l + "/missing", notFoundWith("404.html")},
			{l + "/.htaccess", notFoundWith("404.html")},
			{"/", plainNotFound},
			{"/?p=123", plainNotFound},
			{"/missing/", plainNotFound},
			{"/x" + l + "/", plainNotFound},
			{l + "x/", plainNotFound},
		}, tt.cases...) {
			check(t, h, http.MethodGet, c.target, c.want)
		}
		if logs.Len() > 0 {
			t.Errorf("base %q: the requests have logged %q", tt.base, logs)
		}
	}
}

func TestCutBase(t *testing.T) {
	blog := []string{"blog"}
	for _, tt := range []struct {
		base       []string
		p          string
		rest       string
		atBase, ok bool
	}{
		{nil, "/", "/", false, true},
		{nil, "/%E6%96%B0/%e3%81%82", "/新/あ", false, true},
		{nil, "/a%2Fb/", "/a%2Fb/", false, true},
		{nil, "//x", "//x", false, true},
		{nil, "*", "*", false, true},
		{blog, "/blog", "", true, true},
		{blog, "/blog/", "/", false, true},
		{blog, "/blog/x/%E6%96%B0", "/x/新", false, true},
		{blog, "/blog//x", "//x", false, true},
		{blog, "/%62log/", "/", false, true},
		{blog, "/%62%6C%6F%67", "", true, true},
		{blog, "/", "", false, false},
		{blog, "/blogx/", "", false, false},
		{blog, "/Blog/", "", false, false},
		{blog, "/x/blog/", "", false, false},
		{blog, "//blog/", "", false, false},
		{blog, "/blog%2F", "", false, false},
		{blog, "/blog%zz/", "", false, false},
		{blog, "*", "", false, false},
		{blog, "xblog/", "", false, false},
		{[]string{"ブ ログ"}, "/%E3%83%96%20%E3%83%AD%E3%82%B0/", "/", false, true},
		{[]string{"ブ ログ"}, "/%e3%83%96%20%e3%83%ad%e3%82%b0", "", true, true},
		{[]string{"ブ ログ"}, "/ブ%20ログ/%E6%96%B0/", "/新/", false, true},
		{[]string{"ブ ログ"}, "/ブ+ログ/", "", false, false},
		{[]string{"a(b)"}, "/a%28b%29/", "/", false, true},
		{[]string{"a(b)"}, "/a(b)/x", "/x", false, true},
		{[]string{"a(b)"}, "/a%28b%29", "", true, true},
		{[]string{"a(b)"}, "/a%28b%29x/", "", false, false},
		{[]string{"a/b"}, "/a%2Fb/", "/", false, true},
		{[]string{"a/b"}, "/a%2fb", "", true, true},
		{[]string{"a/b"}, "/a/b/", "", false, false},
		{[]string{"a", "b"}, "/a/b/c", "/c", false, true},
		{[]string{"a", "b"}, "/a/b", "", true, true},
		{[]string{"a", "b"}, "/a/", "", false, false},
		{[]string{"a", "b"}, "/a", "", false, false},
		{[]string{"a", "b"}, "/a%2Fb/", "", false, false},
	} {
		rest, atBase, ok := cutBase(tt.base, tt.p)
		if rest != tt.rest || atBase != tt.atBase || ok != tt.ok {
			t.Errorf("cutBase(%q, %q) = %q, %t, %t, want %q, %t, %t", tt.base, tt.p, rest, atBase, ok, tt.rest, tt.atBase,
				tt.ok)
		}
	}
}

// TestServeNotFound checks that a path without a file gets the 404 page of the first prefix that it starts with, and
// a plain 404 without one or if its page is not a file.
func TestServeNotFound(t *testing.T) {
	for _, tt := range []struct {
		what  string
		pages []notFoundPage
		cases map[string]string // the 404 page of each target; "" for a plain 404
	}{
		{"languages", []notFoundPage{{"/en/", "/en/404.html"}, {"/", "/404.html"}}, map[string]string{
			"/missing":      "404.html",
			"/en/missing":   "en/404.html",
			"/en/a/b/c/":    "en/404.html",
			"/%65n/missing": "en/404.html",
			"/enx/missing":  "404.html",
			"/x/en/missing": "404.html",
			"/EN/missing":   "404.html",
		}},
		{
			"prefixDefaultLanguage",
			[]notFoundPage{{"/ja/", "/ja/404.html"}, {"/en/", "/en/404.html"}, {"/", "/ja/404.html"}},
			map[string]string{"/ja/missing": "ja/404.html", "/en/missing": "en/404.html", "/missing": "ja/404.html"},
		},
		{"one language", []notFoundPage{{"/", "/404.html"}}, map[string]string{"/en/missing": "404.html"}},
		{"none", nil, map[string]string{"/missing": "", "/en/missing": ""}},
		{"a missing page", []notFoundPage{{"/", "/missing.html"}}, map[string]string{"/missing": ""}},
		{"a directory", []notFoundPage{{"/", "/dir.html"}}, map[string]string{"/missing": ""}},
		{
			"the first prefix only",
			[]notFoundPage{{"/en/", "/en/missing.html"}, {"/", "/404.html"}},
			map[string]string{"/en/missing": "", "/missing": "404.html"},
		},
	} {
		out := t.TempDir()
		list := testList()
		list.NotFound = tt.pages
		writeRelease(t, out, releaseA, list, publicFiles(
			"index.html", "404.html", "ja/404.html", "en/404.html", "dir.html/index.html",
		))
		setCurrent(t, out, "releases/"+releaseA)
		h, logs := newLogged(t, out)
		for target, page := range tt.cases {
			want := plainNotFound
			if page != "" {
				want = notFoundWith(page)
			}
			checkResponse(t, tt.what+": GET "+target, do(h, httptest.NewRequest(http.MethodGet, target, nil)), want)
		}
		if logs.Len() > 0 {
			t.Errorf("%s: the requests have logged %q", tt.what, logs)
		}
	}
}

// TestServeMethods checks that only GET and HEAD are answered, that HEAD gets the headers of GET without the body,
// and that the files are served with ranges and conditional requests.
func TestServeMethods(t *testing.T) {
	out := t.TempDir()
	writeRelease(t, out, releaseA, testList(), publicFiles("index.html", "404.html"))
	setCurrent(t, out, "releases/"+releaseA)
	h := newHandler(t, out)
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/", nil),
		httptest.NewRequest(http.MethodPost, "/_forms/contact", strings.NewReader("name=x")),
		httptest.NewRequest(http.MethodPost, "/?p=123", nil),
		httptest.NewRequest(http.MethodPut, "/index.html", nil),
		httptest.NewRequest(http.MethodPatch, "/", nil),
		httptest.NewRequest(http.MethodDelete, "/", nil),
		httptest.NewRequest(http.MethodOptions, "/", nil),
	} {
		what := req.Method + " " + req.RequestURI
		res := do(h, req)
		checkResponse(t, what, res, notAllowed)
		if got := res.Header.Values("Allow"); len(got) != 1 || got[0] != "GET, HEAD" {
			t.Errorf("%s has the Allow %q, want %q", what, got, "GET, HEAD")
		}
	}

	for _, tt := range []struct {
		target string
		want   response
		length string // the Content-Length
	}{
		{"/", response{http.StatusOK, "", htmlType, ""}, "10"},
		{"/missing", response{http.StatusNotFound, "", htmlType, ""}, "8"},
		{"/?p=123", redirectTo(301, "/2026/10/26/%E6%96%B0%E3%82%B5%E3%83%BC%E3%83%93%E3%82%B9/"), ""},
	} {
		res := do(h, httptest.NewRequest(http.MethodHead, tt.target, nil))
		checkResponse(t, "HEAD "+tt.target, res, tt.want)
		if got := res.Header.Get("Content-Length"); got != tt.length {
			t.Errorf("HEAD %s has the Content-Length %q, want %q", tt.target, got, tt.length)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Range", "bytes=0-3")
	res := do(h, req)
	checkResponse(t, "GET / with a range", res, response{http.StatusPartialContent, "", htmlType, "inde"})
	if got := res.Header.Get("Content-Range"); got != "bytes 0-3/10" {
		t.Errorf("GET / with a range has the Content-Range %q, want %q", got, "bytes 0-3/10")
	}
	lastModified := do(h, httptest.NewRequest(http.MethodGet, "/", nil)).Header.Get("Last-Modified")
	if lastModified == "" {
		t.Fatal("GET / has no Last-Modified")
	}
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-Modified-Since", lastModified)
	checkResponse(t, "GET / if modified since "+lastModified, do(h, req), response{status: http.StatusNotModified})
}

// TestServeLinks checks that symbolic links in the public directory are followed only to files in it: a link that
// leaves it is an error, which is logged, and its target is never served.
func TestServeLinks(t *testing.T) {
	out := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.html")
	writeFile(t, filepath.Dir(secret), "secret.html", "secret")
	writeRelease(t, out, releaseA, testList(), publicFiles("index.html", "a.html", "404.html"))
	pub := filepath.Join(out, "releases", releaseA, "public")
	for name, target := range map[string]string{
		"inside.html":   "a.html",
		"absolute.html": filepath.Join(pub, "a.html"),
		"server.json":   "../server.json",
		"secret.html":   secret,
		"up":            "..",
		"loop.html":     "loop.html",
		"en/404.html":   "../../server.json",
	} {
		link := filepath.Join(pub, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	setCurrent(t, out, "releases/"+releaseA)
	h, logs := newLogged(t, out)
	const escapes = "path escapes from parent"
	for _, tt := range []struct {
		target string
		want   response
		file   string // that the log names, with the error
		err    string
	}{
		{"/inside.html", servedPage("a.html"), "", ""},
		{"/absolute.html", serverError, "absolute.html", escapes},
		{"/server.json", serverError, "server.json", escapes},
		{"/secret.html", serverError, "secret.html", escapes},
		{"/up/server.json", serverError, "up/server.json", escapes},
		{"/up/", serverError, "up/index.html", escapes},
		{"/loop.html", serverError, "loop.html", syscall.ELOOP.Error()},
		{"/en/missing", plainNotFound, "en/404.html", escapes},
		{"/missing", notFoundWith("404.html"), "", ""},
	} {
		logs.Reset()
		check(t, h, http.MethodGet, tt.target, tt.want)
		want := ""
		if tt.file != "" {
			want = "GET " + strconv.Quote(tt.target) + ": " + filepath.Join(pub, filepath.FromSlash(tt.file)) + ": " +
				tt.err + "\n"
		}
		if logs.String() != want {
			t.Errorf("GET %s has logged %q, want %q", tt.target, logs, want)
		}
	}
}

// TestServeUnreadable checks the responses for files and directories in the public directory that cannot be read,
// which are logged.
func TestServeUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any file")
	}
	out := t.TempDir()
	writeRelease(t, out, releaseA, testList(), publicFiles(
		"index.html", "a.html", "private/index.html", "404.html", "en/404.html",
	))
	pub := filepath.Join(out, "releases", releaseA, "public")
	for _, name := range []string{"a.html", "private", "en/404.html"} {
		if err := os.Chmod(filepath.Join(pub, filepath.FromSlash(name)), 0); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(pub, "private"), 0o755) })
	setCurrent(t, out, "releases/"+releaseA)
	h, logs := newLogged(t, out)
	for _, tt := range []struct {
		target string
		want   response
		file   string // that the log names
	}{
		{"/", servedPage("index.html"), ""},
		{"/a.html", serverError, "a.html"},
		{"/private/", serverError, "private/index.html"},
		{"/en/missing", plainNotFound, "en/404.html"},
	} {
		logs.Reset()
		check(t, h, http.MethodGet, tt.target, tt.want)
		want := ""
		if tt.file != "" {
			want = "GET " + strconv.Quote(tt.target) + ": " + filepath.Join(pub, filepath.FromSlash(tt.file)) + ": " +
				syscall.EACCES.Error() + "\n"
		}
		if logs.String() != want {
			t.Errorf("GET %s has logged %q, want %q", tt.target, logs, want)
		}
	}
}

// TestServeRelease checks that each request is served from the release that current points to then, and that
// requests without a release that can be served get 503 or 500, with a line of the log each.
func TestServeRelease(t *testing.T) {
	const releaseC = "00000000000000c0"
	out := t.TempDir()
	writeRelease(t, out, releaseA, testList(), map[string]string{"index.html": "A"})
	list := testList()
	list.Base = "/blog"
	writeRelease(t, out, releaseB, list, map[string]string{"index.html": "B"})
	writeFile(t, filepath.Join(out, "releases", releaseC), "server.json", "{}")
	setCurrent(t, out, "releases/"+releaseA)
	h, logs := newLogged(t, out)
	get := func(target string, want response, errs ...string) {
		t.Helper()
		logs.Reset()
		check(t, h, http.MethodGet, target, want)
		wantLog := ""
		for _, err := range errs {
			wantLog += "GET " + strconv.Quote(target) + ": " + err + "\n"
		}
		if logs.String() != wantLog {
			t.Errorf("GET %s has logged %q, want %q", target, logs, wantLog)
		}
	}
	get("/", servedPage("A"))
	setCurrent(t, out, "releases/"+releaseB)
	get("/", plainNotFound)
	get("/blog/", servedPage("B"))
	get("/blog", redirectTo(301, "/blog/"))

	if err := os.Remove(filepath.Join(out, "current")); err != nil {
		t.Fatal(err)
	}
	get("/", unavailable, "no release is published in "+out)
	get("/blog/?x=1", unavailable, "no release is published in "+out)
	// The request-target is quoted, so that what it has cannot change how the log looks.
	override := "/" + string(rune(0x202E)) + "x"
	get(override, unavailable, "no release is published in "+out)
	if strings.ContainsRune(logs.String(), 0x202E) {
		t.Errorf("GET %q has logged the character U+202E as it is: %q", override, logs)
	}
	logs.Reset()
	check(t, h, http.MethodPost, "/", notAllowed)
	if logs.Len() > 0 {
		t.Errorf("POST / has logged %q", logs)
	}

	setCurrent(t, out, "releases/xyz")
	get("/a", serverError, out+`: current points to "releases/xyz", not to a release`)
	setCurrent(t, out, "releases/"+releaseC)
	invalid := filepath.Join(out, "releases", releaseC, "server.json") + ": index is empty"
	get("/", serverError, invalid)
	get("/", serverError, invalid)
	if err := os.RemoveAll(filepath.Join(out, "releases", releaseB, "public")); err != nil {
		t.Fatal(err)
	}
	setCurrent(t, out, "releases/"+releaseB)
	get("/blog/", serverError, filepath.Join(out, "releases", releaseB, "public")+": no such file or directory")
	get("/blog", redirectTo(301, "/blog/"))
	get("/", plainNotFound)

	setCurrent(t, out, "releases/"+releaseA)
	get("/", servedPage("A"))
}

// TestServeThroughLink serves b/out as link/../out, where link is a symbolic link to b/entries: the out next to link is
// not opened.
func TestServeThroughLink(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "b", "out")
	writeRelease(t, out, releaseA, testList(), map[string]string{"index.html": "A"})
	setCurrent(t, out, "releases/"+releaseA)
	if err := os.Mkdir(filepath.Join(dir, "b", "entries"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "b", "entries"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	h, _ := newLogged(t, filepath.Join(dir, "link")+filepath.FromSlash("/../out"))
	check(t, h, http.MethodGet, "/", servedPage("A"))
}

// TestServeConcurrent sends requests from 4 goroutines, at least 100 each and more until the last switch of current
// between releases with other bases. It checks that each is served from one release, with its server.json and its
// files, and that none fails while a rename replaces current, because the handler reads the link itself and opens the
// release by its name (4.3, 9.5). A response is checked as it is; no request is sent again.
func TestServeConcurrent(t *testing.T) {
	out := t.TempDir()
	writeRelease(t, out, releaseA, testList(), map[string]string{"index.html": "A"})
	list := testList()
	list.Base = "/b"
	writeRelease(t, out, releaseB, list, map[string]string{"index.html": "B"})
	setCurrent(t, out, "releases/"+releaseA)
	h := newHandler(t, out)
	plain := "404 " + plainNotFound.body
	want := map[string][]string{"/": {"200 A", plain}, "/b/": {"200 B", plain}}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	// Deferred, so that the requests also stop if setCurrent ends the test.
	defer func() {
		close(stop)
		wg.Wait()
	}()
	for range 4 {
		wg.Go(func() {
			for i := 0; ; i++ {
				// At least 100 requests each, so that they are checked also where goroutines cannot run in parallel.
				if i >= 100 {
					select {
					case <-stop:
						return
					default:
					}
				}
				target := []string{"/", "/b/"}[i%2]
				res := do(h, httptest.NewRequest(http.MethodGet, target, nil))
				body, _ := io.ReadAll(res.Body)
				if got := strconv.Itoa(res.StatusCode) + " " + string(body); !slices.Contains(want[target], got) {
					t.Errorf("GET %s = %q, want one of %q", target, got, want[target])
					return
				}
			}
		})
	}
	for i := range 200 {
		setCurrent(t, out, "releases/"+[]string{releaseB, releaseA}[i%2])
	}
}

func TestContentType(t *testing.T) {
	for _, tt := range []struct{ name, want string }{
		{"index.html", "text/html; charset=utf-8"},
		{"a.htm", "text/html; charset=utf-8"},
		{"css/site.css", "text/css; charset=utf-8"},
		{"a.js", "text/javascript; charset=utf-8"},
		{"a.mjs", "text/javascript; charset=utf-8"},
		{"a.json", "application/json"},
		{"a.js.map", "application/json"},
		{"site.webmanifest", "application/manifest+json"},
		{"feed/index.xml", "application/xml"},
		{"robots.txt", "text/plain; charset=utf-8"},
		{"a.csv", "text/csv; charset=utf-8"},
		{"media/med_01JB8Z3K5TM8A1C5F9V3J6K2N7/会社案内.pdf", "application/pdf"},
		{"a.jpg", "image/jpeg"},
		{"a.jpeg", "image/jpeg"},
		{"a.png", "image/png"},
		{"a.gif", "image/gif"},
		{"a.webp", "image/webp"},
		{"a.avif", "image/avif"},
		{"a.svg", "image/svg+xml"},
		{"favicon.ico", "image/x-icon"},
		{"a.woff", "font/woff"},
		{"a.woff2", "font/woff2"},
		{"a.ttf", "font/ttf"},
		{"a.otf", "font/otf"},
		{"a.mp4", "video/mp4"},
		{"a.webm", "video/webm"},
		{"a.mp3", "audio/mpeg"},
		{"a.wasm", "application/wasm"},
		{"A.HTML", "text/html; charset=utf-8"},
		{"a.Css", "text/css; charset=utf-8"},
		{"a.JPG", "image/jpeg"},
		{"LICENSE", "application/octet-stream"},
		{"a.", "application/octet-stream"},
		{"a.exe", "application/octet-stream"},
		{"a.html.gz", "application/octet-stream"},
		{"a.html/b", "application/octet-stream"},
		{"a.ｈｔｍｌ", "application/octet-stream"},
	} {
		if got := contentType(tt.name); got != tt.want {
			t.Errorf("contentType(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}
