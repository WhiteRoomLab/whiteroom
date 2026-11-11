package admin

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

const (
	httpsOrigin = "https://admin.example.com"
	localOrigin = "http://localhost:8081"
)

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

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.b.Reset()
}

// fakeClock is a clock that moves only when a test says so.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

type reply struct {
	OK bool `json:"ok"`
}

type testIn struct {
	Name string `json:"name"`
}

// testAccounts returns a Manager for a store in a temporary directory, and the store. The hashes have 2 iterations: the
// real count takes about a second under the race detector, and only account.NewWithIterations lets a test change it.
func testAccounts(tb testing.TB) (*account.Manager, *store.Store) {
	tb.Helper()
	s, err := store.Create(filepath.Join(tb.TempDir(), "data"))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = s.Close() })
	m, err := account.NewWithIterations(s, 2)
	if err != nil {
		tb.Fatal(err)
	}
	return m, s
}

// storeWrite returns f followed by the record store.write, which an Update that writes with the store's own API needs
// (store.ErrUnrecorded): the tests that make states with it are not about who wrote.
func storeWrite(f func(*store.Tx) error) func(*store.Tx) error {
	return func(tx *store.Tx) error {
		if err := f(tx); err != nil {
			return err
		}
		return tx.Record(audit.Record{Log: audit.ActivityLog, Actor: audit.System, Action: audit.StoreWrite})
	}
}

func testConfig(m *account.Manager, origin string, proxies ...netip.Prefix) Config {
	return Config{Origin: origin, TrustedProxies: proxies, Accounts: m}
}

// testServer is a Server with routes for the tests, the buffer of its log, and the store of its Manager.
type testServer struct {
	*Server
	logs    *syncBuffer
	clock   *fakeClock
	db      *store.Store
	seen    *secretWatch
	reached atomic.Int32 // the requests that POST and DELETE /api/test have taken
	left    atomic.Int64 // the time left of the context of GET /api/deadline, in nanoseconds
}

// newTestServer returns the Server for origin, with the trusted proxies, which are prefixes, and these routes: GET,
// POST (which reads a testIn of at most 1024 bytes) and DELETE /api/test, GET /api/wait (waits for its context),
// /api/fail (answers an error), /api/addr (answers the client address), /api/deadline, GET and POST /api/panic, and
// GET /api/abort, /api/halfway and /api/panic-after-* (which panic after they write something).
func newTestServer(t *testing.T, origin string, proxies ...string) *testServer {
	t.Helper()
	ts := &testServer{logs: new(syncBuffer), clock: newFakeClock(), seen: newSecretWatch()}
	var prefixes []netip.Prefix
	for _, p := range proxies {
		prefixes = append(prefixes, netip.MustParsePrefix(p))
	}
	m, db := testAccounts(t)
	m.SetClock(ts.clock.now)
	ts.db = db
	c := testConfig(m, origin, prefixes...)
	c.ErrorLog = log.New(ts.logs, "", 0)
	c.Now = ts.clock.now
	s, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	ts.Server = s
	t.Cleanup(func() { ts.seen.check(t, ts.logs.String()) })
	route := func(pattern string, f http.HandlerFunc) { s.handle(pattern, f) }
	route("GET /api/test", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, reply{OK: true})
	})
	route("POST /api/test", func(w http.ResponseWriter, r *http.Request) {
		ts.reached.Add(1)
		var in testIn
		if readJSON(w, r, 1024, &in) {
			writeJSON(w, http.StatusOK, in)
		}
	})
	route("DELETE /api/test", func(w http.ResponseWriter, _ *http.Request) {
		ts.reached.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	route("GET /api/wait", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		s.fail(w, r, r.Context().Err())
	})
	route("GET /api/fail", func(w http.ResponseWriter, r *http.Request) {
		s.fail(w, r, errors.New("the secret text of an error"))
	})
	route("GET /api/panic", func(http.ResponseWriter, *http.Request) { panic("boom") })
	route("POST /api/panic", func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		panic("boom")
	})
	route("GET /api/abort", func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })
	route("GET /api/halfway", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "partial")
		_ = http.NewResponseController(w).Flush()
		panic("boom")
	})
	route("GET /api/panic-after-write", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "partial")
		panic("boom")
	})
	route("GET /api/panic-after-header", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		panic("boom")
	})
	route("GET /api/panic-after-flush", func(w http.ResponseWriter, _ *http.Request) {
		_ = http.NewResponseController(w).Flush()
		panic("boom")
	})
	route("GET /api/addr", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.clientAddr(r).String())
	})
	route("GET /api/deadline", func(w http.ResponseWriter, r *http.Request) {
		if d, ok := r.Context().Deadline(); ok {
			ts.left.Store(int64(time.Until(d)))
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return ts
}

// request returns a request for path to ts, as a server reads it. headers are names and values; the name Host sets the
// Host of the request, and the name RemoteAddr the address of the peer.
func (ts *testServer) request(method, path, body string, headers ...string) *http.Request {
	req := httptest.NewRequest(method, ts.origin+path, strings.NewReader(body))
	req.RequestURI = path
	for i := 0; i < len(headers); i += 2 {
		switch headers[i] {
		case "Host":
			req.Host = headers[i+1]
		case "RemoteAddr":
			req.RemoteAddr = headers[i+1]
		default:
			req.Header.Add(headers[i], headers[i+1])
		}
	}
	return req
}

// send returns the response to the request that request returns. The test server watches what it answers for the secrets
// of the test (see secretWatch).
func (ts *testServer) send(method, path, body string, headers ...string) *http.Response {
	req := ts.request(method, path, body, headers...)
	ts.seen.watchRequest(req.Header)
	rec := httptest.NewRecorder()
	ts.ServeHTTP(rec, req)
	ts.seen.record(method+" "+path, rec.Code, rec.Header(), rec.Body.Bytes())
	return rec.Result()
}

// sendJSON sends the body as JSON, in a request that comes from the origin of ts.
func (ts *testServer) sendJSON(path, contentType, body string) *http.Response {
	headers := []string{"Origin", ts.origin}
	if contentType != "" {
		headers = append(headers, "Content-Type", contentType)
	}
	return ts.send(http.MethodPost, path, body, headers...)
}

func bodyOf(t *testing.T, res *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// problems has the status and the detail of each code of problem.
var problems = map[string]struct {
	status int
	detail string
}{
	"misdirected":            {http.StatusMisdirectedRequest, "the Host of the request is not that of this server"},
	"not-found":              {http.StatusNotFound, "there is no such resource"},
	"method-not-allowed":     {http.StatusMethodNotAllowed, "the resource does not take this method"},
	"cross-origin":           {http.StatusForbidden, "the request does not come from the origin of this server"},
	"too-large":              {http.StatusRequestEntityTooLarge, "the body is too large"},
	"unsupported-media-type": {http.StatusUnsupportedMediaType, "the body must be application/json"},
	"bad-request":            {http.StatusBadRequest, "the body is not the JSON that this resource takes"},
	"too-many-attempts":      {http.StatusTooManyRequests, "too many failed logins; try again later"},
	"bad-credentials":        {http.StatusUnauthorized, "wrong login name or password"},
	"unauthenticated":        {http.StatusUnauthorized, "not logged in"},
	"busy":                   {http.StatusServiceUnavailable, "the server could not answer in time; try again later"},
	"internal":               {http.StatusInternalServerError, "the server failed to answer"},
}

func problemJSON(code string) string {
	p := problems[code]
	return fmt.Sprintf(`{"type":"about:blank","title":%q,"status":%d,"detail":%q,"code":%q}`,
		http.StatusText(p.status), p.status, p.detail, code)
}

// checkProblem checks that res, the response to what, is the problem with code, with the headers that go with it.
func checkProblem(t *testing.T, what string, res *http.Response, code string) {
	t.Helper()
	p, ok := problems[code]
	if !ok {
		t.Fatalf("%s: unknown code %q", what, code)
	}
	if res.StatusCode != p.status {
		t.Errorf("%s has the status %d, want %d", what, res.StatusCode, p.status)
	}
	if got := res.Header.Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("%s has the Content-Type %q, want application/problem+json", what, got)
	}
	if got, want := bodyOf(t, res), problemJSON(code); got != want {
		t.Errorf("%s has the body %s, want %s", what, got, want)
	}
	retry := res.Header.Values("Retry-After")
	switch code {
	case "busy":
		if !equalValues(retry, "5") {
			t.Errorf("%s has the Retry-After %q, want 5", what, retry)
		}
	case "too-many-attempts":
		// The wait is in whole seconds, without a sign or a unit.
		if n, err := strconv.Atoi(strings.Join(retry, ",")); err != nil || len(retry) != 1 || n < 1 || retry[0] != strconv.Itoa(n) {
			t.Errorf("%s has the Retry-After %q, want one number of seconds from 1", what, retry)
		}
	default:
		if len(retry) != 0 {
			t.Errorf("%s has the Retry-After %q, want none", what, retry)
		}
	}
	challenge := res.Header.Values("WWW-Authenticate")
	if p.status == http.StatusUnauthorized {
		if !equalValues(challenge, "Session") {
			t.Errorf("%s has the WWW-Authenticate %q, want Session", what, challenge)
		}
	} else if len(challenge) != 0 {
		t.Errorf("%s has the WWW-Authenticate %q, want none", what, challenge)
	}
}

func equalValues(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// checkHeaders checks that res, the response to what, has the headers of every response, and the Strict-Transport-
// Security header only if https.
func checkHeaders(t *testing.T, what string, res *http.Response, https bool) {
	t.Helper()
	want := map[string]string{
		"Content-Security-Policy":      "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'",
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
		"Referrer-Policy":              "no-referrer",
		"Cache-Control":                "no-store",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
	}
	if https {
		want["Strict-Transport-Security"] = "max-age=31536000"
	}
	for name, v := range want {
		if got := res.Header.Values(name); !equalValues(got, v) {
			t.Errorf("%s has the %s %q, want %q", what, name, got, v)
		}
	}
	if !https {
		if got := res.Header.Values("Strict-Transport-Security"); len(got) != 0 {
			t.Errorf("%s has the Strict-Transport-Security %q, want none", what, got)
		}
	}
}

// listen serves h behind NewHTTPServer on a loopback port until the end of the test, and returns its address.
func listen(t *testing.T, h http.Handler, errorLog *log.Logger) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := NewHTTPServer(h, errorLog)
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return l.Addr().String()
}

// rawExchange sends request, bytes as they go on a connection, to addr, and returns the response.
func rawExchange(t *testing.T, addr, request string) *http.Response {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatal(err)
	}
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestNewOrigins(t *testing.T) {
	m, _ := testAccounts(t)
	for _, tt := range []struct{ in, want string }{
		{"https://admin.example.com", "https://admin.example.com"},
		{"https://admin.example.com/", "https://admin.example.com"},
		{"HTTPS://Admin.Example.COM", "https://admin.example.com"},
		{"https://admin.example.com:443", "https://admin.example.com"},
		{"https://admin.example.com:8443", "https://admin.example.com:8443"},
		{"https://admin.example.com:08443/", "https://admin.example.com:8443"},
		{"https://admin.example.com:80", "https://admin.example.com:80"},
		{"https://xn--wgv71a119e.example.com", "https://xn--wgv71a119e.example.com"},
		{"https://intranet", "https://intranet"},
		{"https://a_b-c.example.com", "https://a_b-c.example.com"},
		{"https://192.0.2.10:8443", "https://192.0.2.10:8443"},
		{"https://[2001:DB8::1]:8443", "https://[2001:db8::1]:8443"},
		{"https://[2001:0db8:0:0:0:0:0:1]", "https://[2001:db8::1]"},
		{"https://localhost:443", "https://localhost"},
		{"http://localhost", "http://localhost"},
		{"http://localhost:80", "http://localhost"},
		{"http://LOCALHOST:8081/", "http://localhost:8081"},
		{"http://127.0.0.1:8081", "http://127.0.0.1:8081"},
		{"http://127.255.255.254", "http://127.255.255.254"},
		{"http://[::1]:8081", "http://[::1]:8081"},
		{"http://[0:0:0:0:0:0:0:1]", "http://[::1]"},
	} {
		s, err := New(testConfig(m, tt.in))
		if err != nil {
			t.Errorf("New(%q) failed: %v", tt.in, err)
			continue
		}
		if s.origin != tt.want {
			t.Errorf("New(%q) has the origin %q, want %q", tt.in, s.origin, tt.want)
		}
		if https := strings.HasPrefix(tt.want, "https:"); s.https != https {
			t.Errorf("New(%q) has https %v, want %v", tt.in, s.https, https)
		}
	}

	const (
		scheme = "use https, or http on localhost"
		parts  = "use only a scheme, a host and a port"
		noHost = "it has no host"
		port   = "invalid port"
	)
	for _, tt := range []struct{ in, reason string }{
		{"", scheme},
		{"admin.example.com", scheme},
		{"localhost:8081", scheme},
		{"//admin.example.com", scheme},
		{"ftp://admin.example.com", scheme},
		{" https://admin.example.com", scheme},
		{"ftp://user@admin.example.com/path", scheme},
		{"http://admin.example.com", scheme},
		{"http://admin.example.com:8081", scheme},
		{"http://foo.localhost", scheme},
		{"http://192.0.2.1", scheme},
		{"http://128.0.0.1", scheme},
		{"http://0.0.0.0", scheme},
		{"http://[::2]", scheme},
		{"http://[::ffff:127.0.0.1]", scheme},

		{"https://user@admin.example.com", parts},
		{"https://user:password@admin.example.com", parts},
		{"https://admin.example.com/path", parts},
		{"https://admin.example.com//", parts},
		{"https://admin.example.com/?q=1", parts},
		{"https://admin.example.com?q=1", parts},
		{"https://admin.example.com#top", parts},
		{"https://admin.example.com/#", parts},
		{"https://admin.example.com:8443/path", parts},
		{"https://admin.example.com:8443?q=1", parts},
		{"https://admin.example.com:8443#top", parts},
		{"https://admin.example.com\\", parts},
		{"https://admin example.com", parts},
		{"https://adm!n.example.com", parts},
		{"https://管理.example.com", parts},
		{"https://a..example.com", parts},
		{"https://.example.com", parts},
		{"https://admin.example.com.", parts},
		{"https://" + strings.Repeat("a", 64) + ".example.com", parts},
		{"https://" + strings.Repeat("a.", 130) + "com", parts},
		{"https://::1", parts},
		{"https://example.com:80:80", parts},
		{"https://example.com:80:80:80", parts},
		{"https://[::1", parts},
		{"https://[::1]x", parts},
		{"https://[::1]]", parts},
		{"https://[]", parts},
		{"https://[1.2.3.4]", parts},
		{"https://[fe80::1%25eth0]", parts},
		{"https://ex[a]mple.com", parts},
		{"https://127.1", parts},
		{"https://1.2.3", parts},
		{"https://1.2.3.4.5", parts},
		{"https://01.2.3.4", parts},
		{"https://256.1.1.1", parts},
		{"https://12345", parts},
		{"https://example.1", parts},
		{"http://localhost.", parts},

		{"https://", noHost},
		{"https:///", noHost},
		{"https://:8443", noHost},
		{"http://:8081", noHost},

		{"https://admin.example.com:", port},
		{"https://admin.example.com:0", port},
		{"https://admin.example.com:65536", port},
		{"https://admin.example.com:123456", port},
		{"https://admin.example.com:http", port},
		{"https://admin.example.com:80a", port},
		{"https://admin.example.com:-1", port},
		{"https://[::1]:", port},
		{"http://localhost:99999", port},
		{"http://admin.example.com:99999", port},
	} {
		_, err := New(testConfig(m, tt.in))
		want := fmt.Sprintf("admin: invalid origin %q: %s", tt.in, tt.reason)
		if err == nil || err.Error() != want {
			t.Errorf("New(%q) = %v, want the error %q", tt.in, err, want)
		}
	}
}

func TestNewTrustedProxies(t *testing.T) {
	m, _ := testAccounts(t)
	_, err := New(testConfig(m, httpsOrigin, netip.MustParsePrefix("10.0.0.0/8"), netip.Prefix{}))
	if want := "admin: invalid trusted proxy invalid Prefix"; err == nil || err.Error() != want {
		t.Errorf("New with an invalid prefix = %v, want the error %q", err, want)
	}

	// The Server has its own copy.
	proxies := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	s, err := New(testConfig(m, httpsOrigin, proxies...))
	if err != nil {
		t.Fatal(err)
	}
	proxies[0] = netip.MustParsePrefix("192.0.2.0/24")
	if !s.trusted(netip.MustParseAddr("10.1.2.3")) || s.trusted(netip.MustParseAddr("192.0.2.1")) {
		t.Error("the trusted proxies of a Server change with the slice that New was given")
	}
}

func TestHeaders(t *testing.T) {
	for _, origin := range []string{httpsOrigin, localOrigin} {
		ts := newTestServer(t, origin)
		ts.deadline = 20 * time.Millisecond
		own := []string{"Origin", ts.origin}
		for _, tt := range []struct {
			name         string
			status       int
			method, path string
			body         string
			headers      []string
		}{
			{"ok", http.StatusOK, http.MethodGet, "/api/test", "", nil},
			{"head", http.StatusOK, http.MethodHead, "/api/test", "", nil},
			{"no content", http.StatusNoContent, http.MethodDelete, "/api/test", "", own},
			{"not found", http.StatusNotFound, http.MethodGet, "/nope", "", nil},
			{"path", http.StatusNotFound, http.MethodGet, "/api/./test", "", nil},
			{"method not allowed", http.StatusMethodNotAllowed, http.MethodPut, "/api/test", "", own},
			{"misdirected", http.StatusMisdirectedRequest, http.MethodGet, "/api/test", "", []string{"Host", "evil.example"}},
			{"cross-origin", http.StatusForbidden, http.MethodPost, "/api/test", "{}", nil},
			{"too large", http.StatusRequestEntityTooLarge, http.MethodPost, "/api/test", strings.Repeat("a", 2000),
				[]string{"Origin", ts.origin, "Content-Type", "application/json"}},
			{"media type", http.StatusUnsupportedMediaType, http.MethodPost, "/api/test", "{}",
				[]string{"Origin", ts.origin, "Content-Type", "text/plain"}},
			{"bad request", http.StatusBadRequest, http.MethodPost, "/api/test", "{",
				[]string{"Origin", ts.origin, "Content-Type", "application/json"}},
			{"busy", http.StatusServiceUnavailable, http.MethodGet, "/api/wait", "", nil},
			{"internal", http.StatusInternalServerError, http.MethodGet, "/api/fail", "", nil},
			{"panic", http.StatusInternalServerError, http.MethodGet, "/api/panic", "", nil},
		} {
			what := origin + " " + tt.name
			res := ts.send(tt.method, tt.path, tt.body, tt.headers...)
			if res.StatusCode != tt.status {
				t.Errorf("%s has the status %d, want %d", what, res.StatusCode, tt.status)
			}
			checkHeaders(t, what, res, strings.HasPrefix(origin, "https:"))
		}
	}
}

func TestProblem(t *testing.T) {
	const notFound = `{"type":"about:blank","title":"Not Found","status":404,"detail":"there is no such resource","code":"not-found"}`
	rec := httptest.NewRecorder()
	writeProblem(rec, http.StatusNotFound, "not-found", "there is no such resource")
	if got := rec.Body.String(); got != notFound {
		t.Errorf("writeProblem wrote %s, want %s", got, notFound)
	}

	for code, p := range problems {
		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Length", "123")
		if code == "too-many-attempts" {
			rec.Header().Set("Retry-After", "30")
		}
		writeProblem(rec, p.status, code, p.detail)
		res := rec.Result()
		checkProblem(t, code, res, code)
		if got := res.Header.Get("Content-Length"); got == "123" {
			t.Errorf("%s keeps the Content-Length of another body", code)
		}
		if got := res.Header.Get("Allow"); got != "" {
			t.Errorf("writeProblem set the Allow %q for %s", got, code)
		}
	}

	// A detail that is not UTF-8 has no JSON form: the answer is a plain 500.
	rec = httptest.NewRecorder()
	writeProblem(rec, http.StatusBadRequest, "bad-request", "\xff")
	if got := rec.Header().Get("Content-Type"); rec.Code != http.StatusInternalServerError || !strings.HasPrefix(got, "text/plain") {
		t.Errorf("writeProblem of a detail that is not UTF-8 wrote %d %q, want a plain 500", rec.Code, got)
	}
}

func TestHost(t *testing.T) {
	for _, tt := range []struct {
		origin, host string
		ok           bool
	}{
		{httpsOrigin, "admin.example.com", true},
		{httpsOrigin, "ADMIN.Example.COM", true},
		{httpsOrigin, "admin.example.com:443", true},
		{httpsOrigin, "admin.example.com:80", false},
		{httpsOrigin, "admin.example.com:8443", false},
		{httpsOrigin, "admin.example.com:", false},
		{httpsOrigin, "admin.example.com.", false},
		{httpsOrigin, "admin.example.com.evil.example", false},
		{httpsOrigin, "xadmin.example.com", false},
		{httpsOrigin, "evil.example", false},
		{httpsOrigin, "", false},
		{localOrigin, "localhost:8081", true},
		{localOrigin, "LocalHost:8081", true},
		{localOrigin, "localhost", false},
		{localOrigin, "localhost:80", false},
		{localOrigin, "localhost:8082", false},
		{localOrigin, "127.0.0.1:8081", false},
		{"http://localhost", "localhost", true},
		{"http://localhost", "localhost:80", true},
		{"http://localhost", "localhost:8080", false},
		{"http://[::1]:8081", "[::1]:8081", true},
		{"http://[::1]:8081", "::1:8081", false},
		{"https://[2001:db8::1]", "[2001:db8::1]", true},
		{"https://[2001:db8::1]", "[2001:DB8::1]:443", true},
		{"https://192.0.2.10", "192.0.2.10", true},
		{"https://192.0.2.10", "192.0.2.11", false},
		{"https://a_b-c.example.com", "a_b-c.example.com", true},
		{"https://a_b-c.example.com", "A_B-C.EXAMPLE.COM", true},
		{"https://a_b-c.example.com", "a_b-c.example.coM:443", true},
	} {
		ts := newTestServer(t, tt.origin)
		what := fmt.Sprintf("GET with the Host %q for %s", tt.host, tt.origin)
		res := ts.send(http.MethodGet, "/api/test", "", "Host", tt.host)
		if tt.ok {
			if res.StatusCode != http.StatusOK {
				t.Errorf("%s has the status %d, want 200", what, res.StatusCode)
			}
			continue
		}
		checkProblem(t, what, res, "misdirected")
	}

	// The Host is the first check: before the path, the origin of the request and the routes.
	ts := newTestServer(t, httpsOrigin)
	for _, tt := range []struct{ method, path string }{
		{http.MethodGet, "//api/test"},
		{http.MethodPost, "/api/test"},
		{http.MethodDelete, "/api/test"},
		{http.MethodGet, "/nope"},
		{http.MethodPut, "/api/test"},
	} {
		res := ts.send(tt.method, tt.path, "", "Host", "evil.example")
		checkProblem(t, tt.method+" "+tt.path+" for another Host", res, "misdirected")
	}
	if n := ts.reached.Load(); n != 0 {
		t.Errorf("%d requests for another Host have reached a handler", n)
	}
}

// TestProxyHeadersAreNotRead shows that the host and the scheme come from the request and from the origin, whatever the
// headers of a proxy say.
func TestProxyHeadersAreNotRead(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	res := ts.send(http.MethodGet, "/api/test", "", "Host", "evil.example", "X-Forwarded-Host", "admin.example.com",
		"Forwarded", "host=admin.example.com;proto=https", "X-Forwarded-Proto", "https")
	checkProblem(t, "GET with the Host of another server and the X-Forwarded-Host of this one", res, "misdirected")

	ts = newTestServer(t, localOrigin)
	res = ts.send(http.MethodGet, "/api/test", "", "X-Forwarded-Proto", "https", "Forwarded", "proto=https",
		"X-Forwarded-Ssl", "on")
	if res.StatusCode != http.StatusOK {
		t.Errorf("GET over http with X-Forwarded-Proto has the status %d, want 200", res.StatusCode)
	}
	checkHeaders(t, "GET over http with X-Forwarded-Proto", res, false)
}

func TestPaths(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	for _, path := range []string{
		"//api/test", "/api//test", "/api/test//", "/api/./test", "/./api/test", "/api/../api/test", "/api/test/.",
		"/api/test/..", "/api/test/", "/api/%2e%2e/test", "/api/%2E/test", "/api/%2e%2E/test", "/API/test", "/api/test;x",
		"/api/te%2Fst", "/", "/nope", "/api", "/api/",
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			res := ts.send(method, path, "")
			if method == http.MethodGet {
				checkProblem(t, "GET "+path, res, "not-found")
			} else if res.StatusCode != http.StatusNotFound {
				t.Errorf("HEAD %s has the status %d, want 404", path, res.StatusCode)
			}
			if got := res.Header.Get("Location"); got != "" {
				t.Errorf("%s %s has the Location %q, want none", method, path, got)
			}
		}
	}
	// The path comes before the check of the origin.
	res := ts.send(http.MethodPost, "//api/test", "{}")
	checkProblem(t, "POST //api/test without an origin", res, "not-found")

	for _, path := range []string{"/api/test", "/api/test?x=1", "/api/test?x=//..", "/api/te%73t"} {
		if res := ts.send(http.MethodGet, path, ""); res.StatusCode != http.StatusOK {
			t.Errorf("GET %s has the status %d, want 200", path, res.StatusCode)
		}
	}
	if n := ts.reached.Load(); n != 0 {
		t.Errorf("%d requests for paths that are not routes have reached a handler", n)
	}
}

// TestPathsOnTheWire sends the requests that net/http answers or hands over in ways that httptest does not show: the
// default handler of OPTIONS * would send a redirect.
func TestPathsOnTheWire(t *testing.T) {
	ts := newTestServer(t, localOrigin)
	addr := listen(t, ts.Server, nil)
	for _, request := range []string{
		"OPTIONS * HTTP/1.1\r\nHost: localhost:8081\r\nConnection: close\r\n\r\n",
		"CONNECT localhost:8081 HTTP/1.1\r\nHost: localhost:8081\r\nConnection: close\r\n\r\n",
		"GET /api/../api/test HTTP/1.1\r\nHost: localhost:8081\r\nConnection: close\r\n\r\n",
		"GET http://localhost:8081//api/test HTTP/1.1\r\nHost: localhost:8081\r\nConnection: close\r\n\r\n",
		"GET http://localhost:8081 HTTP/1.1\r\nHost: localhost:8081\r\nConnection: close\r\n\r\n",
		"GET * HTTP/1.1\r\nHost: localhost:8081\r\nConnection: close\r\n\r\n",
	} {
		what := strings.SplitN(request, "\r\n", 2)[0]
		res := rawExchange(t, addr, request)
		checkProblem(t, what, res, "not-found")
		checkHeaders(t, what, res, false)
		if got := res.Header.Get("Location"); got != "" {
			t.Errorf("%s has the Location %q, want none", what, got)
		}
	}
}

func TestMethodNotAllowed(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	for _, tt := range []struct {
		method, path, allow string
	}{
		{http.MethodPut, "/api/test", "DELETE, GET, HEAD, POST"},
		{http.MethodPatch, "/api/test", "DELETE, GET, HEAD, POST"},
		{http.MethodOptions, "/api/test", "DELETE, GET, HEAD, POST"},
		{http.MethodTrace, "/api/test", "DELETE, GET, HEAD, POST"},
		{"PROPFIND", "/api/test", "DELETE, GET, HEAD, POST"},
		{http.MethodPost, "/api/addr", "GET, HEAD"},
		{http.MethodPut, "/api/panic", "GET, HEAD, POST"},
		{http.MethodOptions, "/api/panic", "GET, HEAD, POST"},
	} {
		what := tt.method + " " + tt.path
		res := ts.send(tt.method, tt.path, "", "Origin", ts.origin)
		if got := res.Header.Values("Allow"); !equalValues(got, tt.allow) {
			t.Errorf("%s has the Allow %q, want %q", what, got, tt.allow)
		}
		checkProblem(t, what, res, "method-not-allowed")
	}
	if n := ts.reached.Load(); n != 0 {
		t.Errorf("%d requests with a method that is not allowed have reached a handler", n)
	}

	// Allow is only for 405.
	if got := ts.send(http.MethodGet, "/nope", "").Header.Get("Allow"); got != "" {
		t.Errorf("GET /nope has the Allow %q, want none", got)
	}
}

func TestHandle(t *testing.T) {
	for _, pattern := range []string{"", "/x", "GET", "GET x", " /x", "GET  /x", "GET*/x"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("handle(%q) did not panic", pattern)
				}
			}()
			ts := newTestServer(t, httpsOrigin)
			ts.handle(pattern, http.NotFoundHandler())
		}()
	}

	// ServeMux takes both of "GET /api/accounts/me" and "GET /api/accounts/{id}"; a pattern without a method for the
	// 405 of each path would conflict with the other one.
	ts := newTestServer(t, httpsOrigin)
	value := func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, r.PathValue("id")) }
	ts.handle("GET /api/accounts/me", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, "me")
	}))
	ts.handle("GET /api/accounts/{id}", http.HandlerFunc(value))
	ts.handle("PUT /api/accounts/{id}", http.HandlerFunc(value))
	ts.handle("POST /api/accounts/{id}/enable", http.HandlerFunc(value))
	for _, tt := range []struct {
		method, path string
		status       int
		allow, body  string
	}{
		{http.MethodGet, "/api/accounts/me", 200, "", `"me"`},
		{http.MethodGet, "/api/accounts/42", 200, "", `"42"`},
		{http.MethodGet, "/api/accounts/a%2Fb", 200, "", `"a/b"`},
		{http.MethodGet, "/api/accounts/a%2F..%2Fb", 200, "", `"a/../b"`},
		{http.MethodGet, "/api/accounts/%2e", 404, "", ""},
		{http.MethodGet, "/api/accounts/%2E%2e", 404, "", ""},
		{http.MethodGet, "/api/accounts/.", 404, "", ""},
		{http.MethodGet, "/api/accounts/..", 404, "", ""},
		{http.MethodGet, "/api/accounts//", 404, "", ""},
		{http.MethodHead, "/api/accounts/42", 200, "", ``},
		{http.MethodPut, "/api/accounts/me", 200, "", `"me"`},
		{http.MethodPost, "/api/accounts/me", 405, "GET, HEAD, PUT", ""},
		{http.MethodDelete, "/api/accounts/42", 405, "GET, HEAD, PUT", ""},
		{http.MethodGet, "/api/accounts/42/enable", 405, "POST", ""},
		{http.MethodPost, "/api/accounts/42/enable", 200, "", `"42"`},
		{http.MethodGet, "/api/accounts/42/other", 404, "", ""},
		{http.MethodGet, "/api/accounts", 404, "", ""},
	} {
		what := tt.method + " " + tt.path
		res := ts.send(tt.method, tt.path, "", "Origin", ts.origin)
		if res.StatusCode != tt.status {
			t.Errorf("%s has the status %d, want %d", what, res.StatusCode, tt.status)
		}
		if got := res.Header.Get("Allow"); got != tt.allow {
			t.Errorf("%s has the Allow %q, want %q", what, got, tt.allow)
		}
		if tt.status == 200 && tt.method != http.MethodHead {
			if got := bodyOf(t, res); got != tt.body {
				t.Errorf("%s has the body %s, want %s", what, got, tt.body)
			}
		}
	}
}

func TestCrossOrigin(t *testing.T) {
	const foreign = "https://evil.example"
	for _, c := range []struct{ origin, otherScheme, otherPort string }{
		{httpsOrigin, "http://admin.example.com", "https://admin.example.com:8443"},
		{localOrigin, "https://localhost:8081", "http://localhost:8082"},
	} {
		ts := newTestServer(t, c.origin)
		o := ts.origin
		host := ts.request(http.MethodGet, "/", "").Host
		for _, tt := range []struct {
			name, method string
			headers      []string
			want         int
		}{
			{"Sec-Fetch-Site same-origin", http.MethodPost, []string{"Sec-Fetch-Site", "same-origin"}, 200},
			{"Sec-Fetch-Site none", http.MethodPost, []string{"Sec-Fetch-Site", "none"}, 200},
			{"Sec-Fetch-Site same-site", http.MethodPost, []string{"Sec-Fetch-Site", "same-site"}, 403},
			{"Sec-Fetch-Site cross-site", http.MethodPost, []string{"Sec-Fetch-Site", "cross-site"}, 403},
			{"Sec-Fetch-Site cross-site and this origin", http.MethodPost,
				[]string{"Sec-Fetch-Site", "cross-site", "Origin", o}, 403},
			{"Sec-Fetch-Site same-site and this origin", http.MethodPost,
				[]string{"Sec-Fetch-Site", "same-site", "Origin", o}, 403},
			{"this origin", http.MethodPost, []string{"Origin", o}, 200},
			{"another origin", http.MethodPost, []string{"Origin", foreign}, 403},
			{"another scheme", http.MethodPost, []string{"Origin", c.otherScheme}, 403},
			{"another port", http.MethodPost, []string{"Origin", c.otherPort}, 403},
			{"null origin", http.MethodPost, []string{"Origin", "null"}, 403},
			{"origin with a slash", http.MethodPost, []string{"Origin", o + "/"}, 403},
			{"origin in upper case", http.MethodPost, []string{"Origin", strings.ToUpper(o)}, 403},
			{"host and origin in upper case", http.MethodPost,
				[]string{"Host", strings.ToUpper(host), "Origin", strings.ToUpper(o)}, 403},
			{"neither header", http.MethodPost, nil, 403},
			{"delete without headers", http.MethodDelete, nil, 403},
			{"delete from this origin", http.MethodDelete, []string{"Origin", o}, 204},
			{"delete from another origin", http.MethodDelete, []string{"Origin", foreign}, 403},
			{"delete cross-site", http.MethodDelete, []string{"Sec-Fetch-Site", "cross-site"}, 403},
			{"delete same-origin", http.MethodDelete, []string{"Sec-Fetch-Site", "same-origin"}, 204},
			{"put from this origin", http.MethodPut, []string{"Origin", o}, 405},
			{"put from another origin", http.MethodPut, []string{"Origin", foreign}, 403},
			{"put without headers", http.MethodPut, nil, 403},
			{"get", http.MethodGet, nil, 200},
			{"get from another origin", http.MethodGet, []string{"Origin", foreign}, 200},
			{"get cross-site", http.MethodGet, []string{"Sec-Fetch-Site", "cross-site", "Origin", foreign}, 200},
			{"head", http.MethodHead, nil, 200},
			{"head from another origin", http.MethodHead, []string{"Origin", foreign}, 200},
			{"head cross-site", http.MethodHead, []string{"Sec-Fetch-Site", "cross-site", "Origin", foreign}, 200},
			{"options", http.MethodOptions, nil, 405},
			{"options from another origin", http.MethodOptions, []string{"Origin", foreign}, 405},
			{"options cross-site", http.MethodOptions, []string{"Sec-Fetch-Site", "cross-site", "Origin", foreign}, 405},
		} {
			what := o + " " + tt.name
			headers := tt.headers
			body := ""
			if tt.method == http.MethodPost {
				headers = append([]string{"Content-Type", "application/json"}, headers...)
				body = `{"name":"a"}`
			}
			before := ts.reached.Load()
			res := ts.send(tt.method, "/api/test", body, headers...)
			if res.StatusCode != tt.want {
				t.Errorf("%s has the status %d, want %d", what, res.StatusCode, tt.want)
			}
			if tt.want == http.StatusForbidden {
				checkProblem(t, what, res, "cross-origin")
				if n := ts.reached.Load(); n != before {
					t.Errorf("%s has reached a handler", what)
				}
			}
		}
	}
}

func TestReadJSON(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	name := func(n int) string { return `{"name":"` + strings.Repeat("a", n) + `"}` }

	for _, tt := range []struct{ contentType, body, want string }{
		{"application/json", `{"name":"a"}`, `{"name":"a"}`},
		{"application/json; charset=utf-8", `{"name":"b"}`, `{"name":"b"}`},
		{"APPLICATION/JSON", `{"name":"c"}`, `{"name":"c"}`},
		{"Application/Json;charset=UTF-8", `{"name":"d"}`, `{"name":"d"}`},
		{"application/json;", `{"name":"e"}`, `{"name":"e"}`},
		{"application/json", " \n{\"name\":\"f\"}\n ", `{"name":"f"}`},
		{"application/json", `{}`, `{"name":""}`},
		{"application/json", `null`, `{"name":""}`},
		{"application/json", name(1013), name(1013)},
	} {
		res := ts.sendJSON("/api/test", tt.contentType, tt.body)
		if got := bodyOf(t, res); res.StatusCode != http.StatusOK || got != tt.want {
			t.Errorf("POST %q %q: %d %s, want 200 %s", tt.contentType, tt.body, res.StatusCode, got, tt.want)
		}
	}

	for _, contentType := range []string{
		"", "text/plain", "text/json", "application/jsonx", "application/ld+json", "application/x-www-form-urlencoded",
		"multipart/form-data; boundary=x", "application/json, text/plain", "application/json; charset", "json", "/",
	} {
		res := ts.sendJSON("/api/test", contentType, `{"name":"a"}`)
		checkProblem(t, fmt.Sprintf("POST with the Content-Type %q", contentType), res, "unsupported-media-type")
	}

	res := ts.sendJSON("/api/test", "application/json", name(1014))
	checkProblem(t, "POST of 1025 bytes", res, "too-large")
	res = ts.sendJSON("/api/test", "application/json", strings.Repeat(" ", 5000))
	checkProblem(t, "POST of 5000 spaces", res, "too-large")

	for _, body := range []string{
		``, ` `, `{`, `{"name":`, `{"name":"a"`, `[]`, `"a"`, `5`, `true`, `{"name":5}`, `{"name":null,"x":1}`,
		`{"name":"a","extra":1}`, `{"NAME":"a"}`, `{"name":"a","name":"b"}`, `{"name":"a"} {}`, `{"name":"a"}x`,
		`{"name":"a"}{"name":"b"}`, "{\"name\":\"\xff\"}", `{"name":"a",}`, `{'name':'a'}`,
	} {
		res := ts.sendJSON("/api/test", "application/json", body)
		checkProblem(t, fmt.Sprintf("POST of %q", body), res, "bad-request")
	}

	// The answer says nothing of the body: not a member name, and not what the decoder says about it.
	const secret = "SECRET-MEMBER-NAME"
	res = ts.sendJSON("/api/test", "application/json", `{"name":"a","`+secret+`":1}`)
	body := bodyOf(t, res)
	if res.StatusCode != http.StatusBadRequest || strings.Contains(body, secret) || strings.Contains(body, "unknown") ||
		strings.Contains(body, "testIn") {
		t.Errorf("POST with an unknown member: %d %s", res.StatusCode, body)
	}
	if got := ts.logs.String(); got != "" {
		t.Errorf("requests with bodies that are refused have logged %q", got)
	}

	// A body that cannot be read is not the JSON that the resource takes, and its error is not shown.
	req := ts.request(http.MethodPost, "/api/test", "", "Origin", ts.origin, "Content-Type", "application/json")
	req.Body = io.NopCloser(iotest.ErrReader(errors.New("the secret text of a read error")))
	rec := httptest.NewRecorder()
	ts.ServeHTTP(rec, req)
	checkProblem(t, "POST with a body that fails to be read", rec.Result(), "bad-request")
}

func TestReadJSONClosesConnection(t *testing.T) {
	ts := newTestServer(t, localOrigin)
	addr := listen(t, ts.Server, nil)
	client := &http.Client{Transport: &http.Transport{}}
	t.Cleanup(client.CloseIdleConnections)

	post := func(body string) *http.Response {
		req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/api/test", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "localhost:8081"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", ts.origin)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = res.Body.Close() })
		return res
	}
	res := post(`{"name":"a"}`)
	if got := bodyOf(t, res); res.StatusCode != http.StatusOK || got != `{"name":"a"}` {
		t.Errorf("POST of a body that fits: %d %s, want 200 {\"name\":\"a\"}", res.StatusCode, got)
	}
	if res.Close {
		t.Error("the response to a body that fits closes the connection")
	}
	res = post(`{"name":"` + strings.Repeat("a", 4000) + `"}`)
	checkProblem(t, "POST of a body that is too large", res, "too-large")
	if !res.Close {
		t.Error("the response to a body that is too large does not close the connection")
	}
}

func TestDeadline(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	if got := ts.deadline; got != 30*time.Second {
		t.Errorf("the deadline of a new Server is %v, want 30s", got)
	}
	if res := ts.send(http.MethodGet, "/api/deadline", ""); res.StatusCode != http.StatusNoContent {
		t.Fatalf("GET /api/deadline has the status %d, want 204", res.StatusCode)
	}
	if left := time.Duration(ts.left.Load()); left < 25*time.Second || left > 30*time.Second {
		t.Errorf("the context of a request has %v left, want about 30s", left)
	}

	ts.deadline = 50 * time.Millisecond
	start := time.Now()
	res := ts.send(http.MethodGet, "/api/wait", "")
	checkProblem(t, "GET /api/wait after its deadline", res, "busy")
	if took := time.Since(start); took < 40*time.Millisecond {
		t.Errorf("GET /api/wait took %v, want its deadline of 50ms", took)
	}
	if got := ts.logs.String(); got != "" {
		t.Errorf("a context that ended has logged %q", got)
	}
}

func TestFail(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	res := ts.send(http.MethodGet, "/api/fail", "")
	body := bodyOf(t, res)
	if want := problemJSON("internal"); res.StatusCode != http.StatusInternalServerError || body != want {
		t.Errorf("GET /api/fail: %d %s, want 500 %s", res.StatusCode, body, want)
	}
	if got, want := ts.logs.String(), "GET \"/api/fail\": the secret text of an error\n"; got != want {
		t.Errorf("the log of GET /api/fail is %q, want %q", got, want)
	}

	// A context that has ended is a busy server, however the error says so.
	ts = newTestServer(t, httpsOrigin)
	for _, err := range []error{
		context.Canceled, context.DeadlineExceeded, fmt.Errorf("reading: %w", context.Canceled),
		fmt.Errorf("writing: %w", context.DeadlineExceeded),
	} {
		rec := httptest.NewRecorder()
		ts.fail(rec, ts.request(http.MethodGet, "/api/test", ""), err)
		checkProblem(t, fmt.Sprintf("fail(%v)", err), rec.Result(), "busy")
	}
	if got := ts.logs.String(); got != "" {
		t.Errorf("errors of a context that ended have logged %q", got)
	}
}

func TestPanic(t *testing.T) {
	const (
		cookie = "session=SECRET-COOKIE"
		token  = "Bearer SECRET-TOKEN"
		body   = "SECRET-BODY"
	)
	for _, origin := range []string{httpsOrigin, localOrigin} {
		ts := newTestServer(t, origin)
		for _, tt := range []struct {
			method, body string
			headers      []string
		}{
			{http.MethodGet, "", nil},
			{http.MethodPost, body, []string{"Origin", ts.origin}},
		} {
			what := tt.method + " /api/panic?q=1"
			headers := append([]string{"Cookie", cookie, "Authorization", token, "X-Forwarded-For", "203.0.113.9"}, tt.headers...)
			res := ts.send(tt.method, "/api/panic?q=1", tt.body, headers...)
			text := bodyOf(t, res)
			if want := problemJSON("internal"); res.StatusCode != http.StatusInternalServerError || text != want {
				t.Errorf("%s: %d %s, want 500 %s", what, res.StatusCode, text, want)
			}
			checkHeaders(t, what, res, strings.HasPrefix(origin, "https:"))
			if got := res.Header.Get("Content-Type"); got != "application/problem+json" {
				t.Errorf("%s has the Content-Type %q", what, got)
			}

			logged := ts.logs.String()
			if want := fmt.Sprintf("panic serving %s \"/api/panic?q=1\": boom\ngoroutine ", tt.method); !strings.HasPrefix(logged, want) {
				t.Errorf("%s: the log starts with %q, want %q", what, firstLine(logged), want)
			}
			if n := strings.Count(logged, "panic serving"); n != 1 {
				t.Errorf("%s: %d log entries, want 1", what, n)
			}
			for _, secret := range []string{"SECRET", "Cookie", "Authorization", "Bearer", "203.0.113.9", "X-Forwarded-For"} {
				if strings.Contains(logged, secret) || strings.Contains(text, secret) {
					t.Errorf("%s: the log or the response has %q", what, secret)
				}
			}
			ts.logs.Reset()
		}
	}

	// A panic with http.ErrAbortHandler goes on, for net/http to close the connection, and is not logged.
	ts := newTestServer(t, httpsOrigin)
	func() {
		defer func() {
			err, _ := recover().(error)
			if !errors.Is(err, http.ErrAbortHandler) {
				t.Errorf("GET /api/abort panicked with %v, want http.ErrAbortHandler", err)
			}
		}()
		ts.ServeHTTP(httptest.NewRecorder(), ts.request(http.MethodGet, "/api/abort", ""))
		t.Error("GET /api/abort did not panic")
	}()
	if got := ts.logs.String(); got != "" {
		t.Errorf("GET /api/abort has logged %q", got)
	}

	// Without a log, a panic is answered all the same.
	m, _ := testAccounts(t)
	s, err := New(testConfig(m, httpsOrigin))
	if err != nil {
		t.Fatal(err)
	}
	s.handle("GET /panic", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpsOrigin+"/panic", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("a panic with no log has the status %d, want 500", rec.Code)
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// TestPanicAfterTheResponseBegan shows that a panic after a route wrote, sent the header, or flushed is not answered
// with 500, which would follow the part that is gone: the connection is aborted.
func TestPanicAfterTheResponseBegan(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	for _, path := range []string{"/api/panic-after-write", "/api/panic-after-header", "/api/panic-after-flush"} {
		ts.logs.Reset()
		rec := httptest.NewRecorder()
		func() {
			defer func() {
				err, _ := recover().(error)
				if !errors.Is(err, http.ErrAbortHandler) {
					t.Errorf("GET %s panicked with %v, want http.ErrAbortHandler", path, err)
				}
			}()
			ts.ServeHTTP(rec, ts.request(http.MethodGet, path, ""))
			t.Errorf("GET %s did not panic", path)
		}()
		if got, want := ts.logs.String(), "panic serving GET \""+path+"\": boom\n"; !strings.HasPrefix(got, want) {
			t.Errorf("GET %s: the log starts with %q, want %q", path, firstLine(got), firstLine(want))
		}
		if body := rec.Body.String(); strings.Contains(body, "internal") {
			t.Errorf("GET %s: the problem follows the part of the response: %q", path, body)
		}
	}
}

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusCreated, testIn{Name: "a"})
	res := rec.Result()
	if got := bodyOf(t, res); res.StatusCode != http.StatusCreated || got != `{"name":"a"}` {
		t.Errorf("writeJSON wrote %d %s, want 201 {\"name\":\"a\"}", res.StatusCode, got)
	}
	if got := res.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("writeJSON wrote the Content-Type %q, want application/json", got)
	}

	// A value that has no JSON form is a fault of the server.
	rec = httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, make(chan int))
	checkProblem(t, "writeJSON of a channel", rec.Result(), "internal")
}

func TestUnwrap(t *testing.T) {
	rec := httptest.NewRecorder()
	nested := &trackedWriter{ResponseWriter: &trackedWriter{ResponseWriter: rec}}
	for _, w := range []http.ResponseWriter{rec, &trackedWriter{ResponseWriter: rec}, nested} {
		if got := unwrap(w); got != http.ResponseWriter(rec) {
			t.Errorf("unwrap(%T) = %T, want the recorder under the wrappers", w, got)
		}
	}
}

// TestPanicOnTheWire shows what the client sees of a panic: a connection closed before the response, or after a part
// of it, and not a response that looks whole.
func TestPanicOnTheWire(t *testing.T) {
	ts := newTestServer(t, localOrigin)
	addr := listen(t, ts.Server, log.New(ts.logs, "net/http: ", 0))
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	get := func(path string) (*http.Response, error) {
		req, err := http.NewRequest(http.MethodGet, "http://"+addr+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "localhost:8081"
		return client.Do(req)
	}

	res, err := get("/api/panic")
	if err != nil {
		t.Fatal(err)
	}
	if want := problemJSON("internal"); res.StatusCode != http.StatusInternalServerError || bodyOf(t, res) != want {
		t.Errorf("GET /api/panic has the status %d, want 500 and %s", res.StatusCode, want)
	}
	if got := strings.Count(ts.logs.String(), "panic serving GET \"/api/panic\": boom\n"); got != 1 {
		t.Errorf("GET /api/panic has logged %d entries of the panic, want 1; the log is %q", got, ts.logs.String())
	}

	ts.logs.Reset()
	if res, err := get("/api/abort"); !errors.Is(err, io.EOF) {
		t.Errorf("GET /api/abort = %v, %v, want the error EOF", res, err)
	}
	if got := ts.logs.String(); got != "" {
		t.Errorf("GET /api/abort has logged %q", got)
	}

	res, err = get("/api/halfway")
	if err != nil {
		t.Fatal(err)
	}
	part, err := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || string(part) != "partial" || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("GET /api/halfway = %d %q, %v, want 200 %q and the error %v",
			res.StatusCode, part, err, "partial", io.ErrUnexpectedEOF)
	}
	if got := strings.Count(ts.logs.String(), "panic serving GET \"/api/halfway\": boom\n"); got != 1 {
		t.Errorf("GET /api/halfway has logged %d entries of the panic, want 1; the log is %q", got, ts.logs.String())
	}
}

// TestResponseController shows that the wrapper of the response lets http.ResponseController reach the writer of
// net/http, as the routes of #48 will need.
func TestResponseController(t *testing.T) {
	ts := newTestServer(t, localOrigin)
	result := func(err error) string {
		if err == nil {
			return "ok"
		}
		return err.Error()
	}
	ts.handle("GET /api/rc", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		rc := http.NewResponseController(w)
		writeJSON(w, http.StatusOK, []string{
			result(rc.SetWriteDeadline(time.Now().Add(time.Hour))),
			result(rc.SetReadDeadline(time.Now().Add(time.Hour))),
			result(rc.Flush()),
		})
	}))
	addr := listen(t, ts.Server, nil)
	res := rawExchange(t, addr, "GET /api/rc HTTP/1.1\r\nHost: localhost:8081\r\nConnection: close\r\n\r\n")
	if got, want := bodyOf(t, res), `["ok","ok","ok"]`; res.StatusCode != http.StatusOK || got != want {
		t.Errorf("GET /api/rc = %d %s, want 200 %s", res.StatusCode, got, want)
	}

	ts.handle("GET /api/flusher", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.(http.Flusher).Flush()
	}))
	for _, path := range []string{"/api/rc", "/api/flusher"} {
		rec := httptest.NewRecorder()
		ts.ServeHTTP(rec, ts.request(http.MethodGet, path, ""))
		if !rec.Flushed {
			t.Errorf("a flush of GET %s does not reach the writer under the wrapper", path)
		}
	}
}
