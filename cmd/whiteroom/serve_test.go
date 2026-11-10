//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const htmlType = "text/html; charset=utf-8"

type chanWriter chan string

func (w chanWriter) Write(p []byte) (int, error) {
	w <- string(p)
	return len(p), nil
}

// syncBuffer is a buffer that goroutines can write to while others read it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// startServe runs whiteroom serve public with ctx and args in a goroutine, and returns the line that it prints on
// stdout once it listens. wait waits for it to return once ctx is done, and checks that it returns 0, without printing
// anything more on stdout or anything on stderr.
func startServe(t *testing.T, ctx context.Context, args ...string) (line string, wait func()) {
	t.Helper()
	what := "whiteroom serve public " + strings.Join(args, " ")
	stdout, stderr := make(chanWriter, 10), new(syncBuffer)
	done := make(chan int, 1)
	go func() { done <- run(ctx, append([]string{"serve", "public"}, args...), stdout, stderr) }()
	select {
	case line = <-stdout:
	case code := <-done:
		t.Fatalf("%s = %d, with stderr:\n%s", what, code, stderr)
	case <-time.After(10 * time.Second):
		t.Fatalf("%s has printed nothing in 10 s, and on stderr:\n%s", what, stderr)
	}
	return line, func() {
		t.Helper()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("%s = %d after its context is done, want 0", what, code)
			}
		case <-time.After(15 * time.Second):
			t.Fatalf("%s has not returned in 15 s after its context is done", what)
		}
		for len(stdout) > 0 {
			t.Errorf("%s printed %q after %q", what, <-stdout, line)
		}
		if s := stderr.String(); s != "" {
			t.Errorf("%s printed on stderr:\n%s", what, s)
		}
	}
}

// originOf returns the origin of the URL in line, which whiteroom serve public prints when it serves out on a port of
// 127.0.0.1, with base, the path of the site URL, such as "http://127.0.0.1:41234". It stops the test if line is not
// that.
func originOf(t *testing.T, line, out, base string) string {
	t.Helper()
	serving := regexp.MustCompile(`^serving ` + regexp.QuoteMeta(out) + ` at (http://127\.0\.0\.1:\d+)` +
		regexp.QuoteMeta(base) + `/\n$`)
	m := serving.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("whiteroom serve public printed %q, which does not match %s", line, serving)
	}
	return m[1]
}

// newClient returns a client that does not follow redirects, so that the tests see them.
func newClient() *http.Client {
	return &http.Client{
		Transport:     &http.Transport{},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}
}

// response is the response that a request must get: the status, the Location, the Content-Type and the body.
type response struct {
	status                      int
	location, contentType, body string
}

// request is a request with method for target, a path with an optional query, and the response that it must get.
type request struct {
	method, target string
	want           response
}

// checkRequests sends requests to the server at origin, such as "http://127.0.0.1:8080", with client, and checks
// their responses, which must also have the headers of every response, and Allow for a 405.
func checkRequests(t *testing.T, client *http.Client, origin string, requests []request) {
	t.Helper()
	for _, tt := range requests {
		what := tt.method + " " + tt.target
		req, err := http.NewRequestWithContext(t.Context(), tt.method, origin+tt.target, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Errorf("%s: %v", what, err)
			continue
		}
		body, err := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if err != nil {
			t.Errorf("%s: %v", what, err)
			continue
		}
		loc, contentType := res.Header.Get("Location"), res.Header.Get("Content-Type")
		if w := tt.want; res.StatusCode != w.status || loc != w.location || contentType != w.contentType {
			t.Errorf("%s = %d, with the Location %q and the Content-Type %q, want %d, %q and %q", what, res.StatusCode,
				loc, contentType, w.status, w.location, w.contentType)
		}
		if string(body) != tt.want.body {
			t.Errorf("%s has the body:\n%s\nwant:\n%s", what, body, tt.want.body)
		}
		var allow []string
		if tt.want.status == http.StatusMethodNotAllowed {
			allow = []string{"GET, HEAD"}
		}
		for name, want := range map[string][]string{
			"Content-Security-Policy": {"script-src 'self'; style-src 'self'; object-src 'none'; base-uri 'self'"},
			"X-Content-Type-Options":  {"nosniff"},
			"Allow":                   allow,
		} {
			if got := res.Header.Values(name); !slices.Equal(got, want) {
				t.Errorf("%s has the %s %q, want %q", what, name, got, want)
			}
		}
	}
}

// copyBlog copies the sample site as copySample does, with the site URL https://example.com/blog.
func copyBlog(t *testing.T) string {
	t.Helper()
	dir := copySample(t)
	settings := filepath.Join(dir, "site", "settings.json")
	data, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	blog := strings.Replace(string(data), `"url": "https://example.com",`, `"url": "https://example.com/blog",`, 1)
	if blog == string(data) {
		t.Fatalf("%s has no url https://example.com", settings)
	}
	if err := os.WriteFile(settings, []byte(blog), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestServe serves the sample site with whiteroom serve public, on a free port, and checks its pages, files, redirects
// and 404 pages over HTTP; then builds a copy of the site at the site URL https://example.com/blog into the same output
// directory, whose release the server serves from the next request; and stops the server with its context.
func TestServe(t *testing.T) {
	out, cache := t.TempDir(), t.TempDir()
	buildSample(t, out, cache)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	line, wait := startServe(t, ctx, "-addr", "127.0.0.1:0", out)
	origin := originOf(t, line, out, "")
	client := newClient()

	golden := func(name string) string { return readGolden(t, "public/"+name) }
	published := func(name string) string {
		data, err := os.ReadFile(filepath.Join(out, "current", "public", filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	ok := func(contentType, body string) response { return response{http.StatusOK, "", contentType, body} }
	movedTo := func(loc string) response { return response{status: http.StatusMovedPermanently, location: loc} }
	notFound := func(page string) response { return response{http.StatusNotFound, "", htmlType, page} }
	plain := func(status int, text string) response {
		return response{status, "", "text/plain; charset=utf-8", text + "\n"}
	}
	const (
		post = "/2026/10/26/%E6%96%B0%E3%82%B5%E3%83%BC%E3%83%93%E3%82%B9/"
		pdf  = "/media/med_01JB8Z3K5TM8A1C5F9V3J6K2N7/%E4%BC%9A%E7%A4%BE%E6%A1%88%E5%86%85.pdf"
	)
	checkRequests(t, client, origin, []request{
		{http.MethodGet, "/", ok(htmlType, golden("index.html"))},
		{http.MethodHead, "/", ok(htmlType, "")},
		{http.MethodGet, "/about/", ok(htmlType, golden("about/index.html"))},
		{http.MethodGet, "/about", movedTo("/about/")},
		{http.MethodGet, post, ok(htmlType, golden("2026/10/26/新サービス/index.html"))},
		{http.MethodGet, "/?p=123", movedTo(post)},
		{http.MethodGet, "/company/", movedTo("/about/")},
		{http.MethodGet, "/recruit/", response{status: http.StatusFound, location: "https://recruit.example.com/"}},
		{http.MethodGet, "/wp-content/uploads/2024/04/%E4%BC%9A%E7%A4%BE%E6%A1%88%E5%86%85.pdf", movedTo(pdf)},
		{http.MethodGet, pdf, ok("application/pdf", published("media/med_01JB8Z3K5TM8A1C5F9V3J6K2N7/会社案内.pdf"))},
		{http.MethodGet, "/feed/", ok("application/xml", golden("feed/index.xml"))},
		{http.MethodGet, "/css/site.css", ok("text/css; charset=utf-8", golden("css/site.css"))},
		{http.MethodGet, "/missing/", notFound(golden("404.html"))},
		{http.MethodGet, "/en/missing", notFound(golden("en/404.html"))},
		{http.MethodGet, "/.htaccess", notFound(golden("404.html"))},
		{http.MethodPost, "/", plain(http.StatusMethodNotAllowed, "405 method not allowed")},
	})

	dir := copyBlog(t)
	if code, _, stderr := runCmd(t, "build", "-out", out, "-cache", cache, dir); code != 0 {
		t.Fatalf("whiteroom build %s = %d, with stderr:\n%s", dir, code, stderr)
	}
	checkRequests(t, client, origin, []request{
		{http.MethodGet, "/blog/", ok(htmlType, published("index.html"))},
		{http.MethodGet, "/blog", movedTo("/blog/")},
		{http.MethodGet, "/blog/?p=123", movedTo("/blog" + post)},
		{http.MethodGet, "/blog/missing", notFound(published("404.html"))},
		{http.MethodGet, "/", plain(http.StatusNotFound, "404 page not found")},
	})

	client.CloseIdleConnections()
	cancel()
	wait()
}

// TestServeDefaults builds a copy of the sample site at the site URL https://example.com/blog with only the directory
// of the site, into its out/, and serves it from the site directory without OUT, whose default is out: the command
// prints it as it is, with the path of the site URL.
func TestServeDefaults(t *testing.T) {
	dir := copyBlog(t)
	if code, _, stderr := runCmd(t, "build", dir); code != 0 {
		t.Fatalf("whiteroom build %s = %d, with stderr:\n%s", dir, code, stderr)
	}
	t.Chdir(dir)
	index, err := os.ReadFile(filepath.Join("out", "current", "public", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	line, wait := startServe(t, ctx, "-addr", "127.0.0.1:0")
	client := newClient()
	checkRequests(t, client, originOf(t, line, "out", "/blog"), []request{
		{http.MethodGet, "/blog/", response{http.StatusOK, "", htmlType, string(index)}},
	})
	client.CloseIdleConnections()
	cancel()
	wait()
}

// TestServeShutdown checks that whiteroom serve public, once its context is done, lets a download in progress
// complete before it returns.
func TestServeShutdown(t *testing.T) {
	out := t.TempDir()
	buildSample(t, out, t.TempDir())
	// Larger than the buffers of the connection, so that the server still sends it once the context is done.
	const size = 64 << 20
	f, err := os.Create(filepath.Join(out, "current", "public", "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	err = f.Truncate(size)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	line, wait := startServe(t, ctx, "-addr", "127.0.0.1:0", out)
	client := newClient()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, originOf(t, line, out, "")+"/large.bin", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	n, err := io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || n != size || err != nil {
		t.Errorf("GET /large.bin, while the server stops = %d, with %d bytes (%v), want %d, with %d bytes",
			res.StatusCode, n, err, http.StatusOK, size)
	}
	client.CloseIdleConnections()
	wait()
}

// failingServe runs whiteroom serve public with args, and checks that it fails at once, with the error want on stderr
// and nothing on stdout.
func failingServe(t *testing.T, what, want string, args ...string) {
	t.Helper()
	// So that the test fails, rather than waits, if it serves.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	code := run(ctx, append([]string{"serve", "public"}, args...), &stdout, &stderr)
	if code != 1 || stdout.Len() > 0 || stderr.String() != want {
		t.Errorf("%s: whiteroom serve public %s = %d, with stdout %q and stderr:\n%s\nwant 1, with stderr:\n%s", what,
			strings.Join(args, " "), code, stdout.String(), stderr.String(), want)
	}
}

// TestServeErrors checks that whiteroom serve public fails for an output directory without a release, for a missing
// one and for an address in use, with the error on stderr.
func TestServeErrors(t *testing.T) {
	empty := t.TempDir()
	failingServe(t, "no release", "whiteroom serve public: no release is published in "+empty+"\n",
		"-addr", "127.0.0.1:0", empty)
	missing := filepath.Join(t.TempDir(), "missing")
	failingServe(t, "a missing output directory", "whiteroom serve public: open "+missing+": no such file or directory\n",
		"-addr", "127.0.0.1:0", missing)

	out := t.TempDir()
	buildSample(t, out, t.TempDir())
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	addr := l.Addr().String()
	failingServe(t, "an address in use", "whiteroom serve public: listen tcp "+addr+": bind: address already in use\n",
		"-addr", addr, out)
}
