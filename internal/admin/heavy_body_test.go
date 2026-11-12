package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
)

// bodyRequest returns a request of alice for POST /api/work whose body is r, of the length that the request says, which is
// unknown if length is negative.
func (ws *workServer) bodyRequest(r io.Reader, length int64) *http.Request {
	headers := append(ws.cookie(ws.tokens["alice"]), "Origin", ws.origin, "Content-Type", "application/json")
	req := ws.rawRequest(http.MethodPost, "/api/work", headers...)
	req.Body = io.NopCloser(r)
	req.ContentLength = length
	return req
}

// rawRequest returns a request without a body to the server, as ts.request does.
func (ws *workServer) rawRequest(method, path string, headers ...string) *http.Request {
	return ws.testServer.request(method, path, "", headers...)
}

// serve answers req with the server.
func (ws *workServer) serve(req *http.Request) *http.Response {
	rec := httptest.NewRecorder()
	ws.ServeHTTP(rec, req)
	return rec.Result()
}

func TestHeavyBody(t *testing.T) {
	const limit = 1000
	ts := newTestServer(t, httpsOrigin)
	ws := newWorkServer(t, ts, limit)
	name := func(n int) string { return `{"name":"` + strings.Repeat("a", n-len(`{"name":""}`)) + `"}` }
	post := func(body string, headers ...string) *http.Response {
		return ws.request("alice", http.MethodPost, "/api/work", body, headers...)
	}

	// A body of limit bytes is received, and read in the place.
	res := post(name(limit))
	if got := bodyOf(t, res); res.StatusCode != http.StatusOK || got != name(limit) {
		t.Errorf("a body of %d bytes: %d %.60s, want 200 and the same JSON", limit, res.StatusCode, got)
	}
	if ws.begun.Load() != 1 {
		t.Errorf("%d pieces of work have begun, want 1", ws.begun.Load())
	}

	// What is refused before the place is had never works in it.
	for _, tt := range []struct {
		name string
		do   func() *http.Response
		code string
	}{
		{"a body that is too large", func() *http.Response { return post(name(limit + 1)) }, "too-large"},
		{"a body that is too large and has no length", func() *http.Response {
			return ws.serve(ws.bodyRequest(strings.NewReader(name(limit+1)), -1))
		}, "too-large"},
		{"text", func() *http.Response { return post(name(50), "Content-Type", "text/plain") }, "unsupported-media-type"},
		{"no content type", func() *http.Response { return post(name(50), "Content-Type", "") }, "unsupported-media-type"},
		{"a body that cannot be read", func() *http.Response {
			return ws.serve(ws.bodyRequest(iotest.ErrReader(errors.New("the secret text of a read error")), -1))
		}, "bad-request"},
	} {
		before := ws.begun.Load()
		checkProblem(t, tt.name, tt.do(), tt.code)
		if got := ws.begun.Load(); got != before {
			t.Errorf("%s: a piece of work has begun", tt.name)
		}
	}

	// What is read in the place and is not the JSON of the route is 400.
	for _, body := range []string{
		`{`, `[]`, `{"name":5}`, `{"name":"a","extra":1}`, `{"name":"a"} {}`, `{"name":"a"}x`, "{\"name\":\"\xff\"}",
	} {
		before := ws.begun.Load()
		checkProblem(t, fmt.Sprintf("the body %q", body), post(body), "bad-request")
		if got := ws.begun.Load(); got != before+1 {
			t.Errorf("the body %q was not read in the place", body)
		}
	}
	if got := ws.logs.String(); got != "" {
		t.Errorf("bodies that are refused have logged %q", got)
	}
	if n, counted := len(ws.heavy), ws.inflight.count(ws.people["alice"].ID); n != 0 || counted != 0 {
		t.Errorf("the place holds %d requests and alice has %d counted, want none", n, counted)
	}
	if files := ts.tempFiles(t); len(files) != 0 {
		t.Errorf("%d temporary files stay after the requests", len(files))
	}
}

func TestHeavyBodyIsSpooled(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ws := newWorkServer(t, ts, documentBodyLimit)
	small := `{"name":"` + strings.Repeat("a", 1000) + `"}`
	big := `{"name":"` + strings.Repeat("b", spoolMemory+1000) + `"}`

	res := ws.request("alice", http.MethodPost, "/api/work", small)
	if got := bodyOf(t, res); res.StatusCode != http.StatusOK || got != small {
		t.Fatalf("a small body: %d %.40s", res.StatusCode, got)
	}
	if n := ws.duringWork.Load(); n != 0 {
		t.Errorf("a body of %d bytes was in %d temporary files, want none: it stays in memory up to %d", len(small), n, spoolMemory)
	}

	res = ws.request("alice", http.MethodPost, "/api/work", big)
	got := bodyOf(t, res)
	if res.StatusCode != http.StatusOK || got != big {
		t.Errorf("a body of %d bytes: %d, %d bytes, want 200 and the same JSON", len(big), res.StatusCode, len(got))
	}
	if n := ws.duringWork.Load(); n != 1 {
		t.Errorf("a body of %d bytes was in %d temporary files when the work began, want 1", len(big), n)
	}
	if files := ts.tempFiles(t); len(files) != 0 {
		t.Errorf("%d temporary files stay after the request: %v", len(files), files)
	}
	if got, want := res.Header.Get("Content-Length"), fmt.Sprint(len(big)); got != want {
		t.Errorf("the answer has the Content-Length %q, want %q", got, want)
	}
}

// TestReceiveBodyLimits shows that the body of a request that carries a document may have the most that a document has
// (content.MaxDocumentBytes) and what wraps it, and no byte more.
func TestReceiveBodyLimits(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	receiveOf := func(n int, length bool) (received int64, refused *http.Response) {
		req := httptest.NewRequest(http.MethodPost, ts.origin+"/api/test", io.NopCloser(&repeatReader{n: n}))
		req.RequestURI = "/api/test"
		req.Header.Set("Content-Type", "application/json")
		req.ContentLength = -1
		if length {
			req.ContentLength = int64(n)
		}
		rec := httptest.NewRecorder()
		in := ts.receiveBody(rec, req, documentBodyLimit)
		if in == nil {
			return -1, rec.Result()
		}
		defer func() { _ = in.Close() }()
		return in.Len(), nil
	}
	for _, length := range []bool{false, true} {
		if got, res := receiveOf(documentBodyLimit, length); got != documentBodyLimit {
			t.Errorf("a body of the most bytes (length told: %v): %d bytes received, answer %v, want all", length, got, res)
		}
		got, res := receiveOf(documentBodyLimit+1, length)
		if got != -1 {
			t.Fatalf("a body of one byte more (length told: %v): %d bytes received, want 413", length, got)
		}
		checkProblem(t, fmt.Sprintf("a body of one byte more (length told: %v)", length), res, "too-large")
	}
	if files := ts.tempFiles(t); len(files) != 0 {
		t.Errorf("%d temporary files stay: %v", len(files), files)
	}
}

// repeatReader reads n bytes of "a".
type repeatReader struct{ n int }

func (z *repeatReader) Read(p []byte) (int, error) {
	if z.n == 0 {
		return 0, io.EOF
	}
	n := min(len(p), z.n)
	for i := range n {
		p[i] = 'a'
	}
	z.n -= n
	return n, nil
}

func TestHeavyAnswer(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ws := newWorkServer(t, ts, 1000)
	for name, f := range map[string]func() (int, http.Header){
		"created":      func() (int, http.Header) { return http.StatusCreated, http.Header{"Location": {"/api/things/1"}} },
		"another-type": func() (int, http.Header) { return http.StatusOK, http.Header{"Content-Type": {"application/x-test"}} },
		"no-content":   func() (int, http.Header) { return http.StatusNoContent, nil },
		"headers": func() (int, http.Header) {
			return http.StatusOK, http.Header{"X-Test": {"a", "b"}, "Set-Cookie": {"x=1"}}
		},
	} {
		ts.apiHeavy("GET /api/"+name, func(w http.ResponseWriter, r *http.Request, p account.Principal) {
			ts.heavyWork(w, r, p, 0, func(_ context.Context, _, out *spool) (int, http.Header, error) {
				status, header := f()
				if status != http.StatusNoContent {
					if err := writeAnswer(out, reply{OK: true}); err != nil {
						return 0, nil, err
					}
				}
				return status, header, nil
			})
		})
	}
	get := func(name string) *http.Response { return ws.request("alice", http.MethodGet, "/api/"+name, "") }

	res := get("created")
	if res.StatusCode != http.StatusCreated || res.Header.Get("Location") != "/api/things/1" || res.Header.Get("Content-Type") != "application/json" ||
		res.Header.Get("Content-Length") != "11" || bodyOf(t, res) != `{"ok":true}` {
		t.Errorf("created: %d %v", res.StatusCode, res.Header)
	}
	res = get("another-type")
	if got := res.Header.Values("Content-Type"); !equalValues(got, "application/x-test") {
		t.Errorf("another type: the Content-Type is %q", got)
	}
	res = get("no-content")
	if b := bodyOf(t, res); res.StatusCode != http.StatusNoContent || b != "" || res.Header.Get("Content-Length") != "" {
		t.Errorf("no content: %d %q with the length %q, want 204 and nothing", res.StatusCode, b, res.Header.Get("Content-Length"))
	}
	res = get("headers")
	if got := res.Header.Values("X-Test"); !equalValues(got, "a", "b") || !equalValues(res.Header.Values("Set-Cookie"), "x=1") {
		t.Errorf("the headers: X-Test %q, Set-Cookie %q, want a b and x=1", got, res.Header.Values("Set-Cookie"))
	}
	checkHeaders(t, "an answer of the work", res, true)

	// HEAD gets the headers of GET, and no body: the work is done all the same.
	before := ws.begun.Load()
	res = ws.request("alice", http.MethodHead, "/api/work", "")
	if b := bodyOf(t, res); res.StatusCode != http.StatusOK || b != "" || res.Header.Get("Content-Length") != "11" {
		t.Errorf("HEAD: %d %q with the length %q, want 200, no body and 11", res.StatusCode, b, res.Header.Get("Content-Length"))
	}
	if ws.begun.Load() != before+1 {
		t.Error("HEAD did not do the work")
	}

	// An answer that is more than the memory of a spool goes through a temporary file, and is the same.
	res = ws.request("alice", http.MethodGet, "/api/big", "")
	body := bodyOf(t, res)
	if res.StatusCode != http.StatusOK || len(body) != bigAnswer || body[0] != '"' || body[len(body)-1] != '"' ||
		strings.Trim(body[1:len(body)-1], "a") != "" || res.Header.Get("Content-Length") != fmt.Sprint(bigAnswer) {
		t.Errorf("a big answer: %d, %d bytes, want 200 and %d", res.StatusCode, len(body), bigAnswer)
	}
	if files := ts.tempFiles(t); len(files) != 0 {
		t.Errorf("%d temporary files stay after the answers: %v", len(files), files)
	}
}

func TestHeavyDeadlines(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ws := newWorkServer(t, ts, documentBodyLimit)
	ts.apiHeavy("GET /api/conflict", func(w http.ResponseWriter, r *http.Request, p account.Principal) {
		ts.heavyWork(w, r, p, 0, func(context.Context, *spool, *spool) (int, http.Header, error) { return 0, nil, edit.ErrConflict })
	})
	do := func(req *http.Request) (rec *deadlineRecorder, before, after time.Time) {
		rec = newDeadlineRecorder()
		before = time.Now()
		ts.ServeHTTP(rec, req)
		return rec, before, time.Now()
	}
	get := func(path string) *http.Request {
		return ws.rawRequest(http.MethodGet, path, append(ws.cookie(ws.tokens["alice"]), "Origin", ts.origin)...)
	}
	post := func(body string, headers ...string) *http.Request {
		all := append(ws.cookie(ws.tokens["alice"]), "Origin", ts.origin)
		if !hasHeader(headers, "Content-Type") {
			all = append(all, "Content-Type", "application/json")
		}
		return ts.request(http.MethodPost, "/api/work", body, append(all, headers...)...)
	}
	small := `{"name":"a"}`
	big := `{"name":"` + strings.Repeat("b", spoolMemory) + `"}`

	rec, before, after := do(get("/api/work"))
	read, write := rec.deadlines()
	if len(read) != 0 || len(write) != 1 {
		t.Fatalf("a request without a body set %d read deadlines and %d write deadlines, want 0 and 1", len(read), len(write))
	}
	checkDeadline(t, "the write of a small answer", write[0], before, after, writeTimeout)

	rec, before, after = do(get("/api/big"))
	read, write = rec.deadlines()
	if len(read) != 0 || len(write) != 1 {
		t.Fatalf("a big answer set %d read deadlines and %d write deadlines, want 0 and 1", len(read), len(write))
	}
	checkDeadline(t, "the write of a big answer", write[0], before, after, slowTransfer)

	rec, before, after = do(post(small))
	read, write = rec.deadlines()
	if len(read) != 1 || len(write) != 1 {
		t.Fatalf("a request with a body set %d read deadlines and %d write deadlines, want 1 and 1", len(read), len(write))
	}
	checkDeadline(t, "the read of a body", read[0], before, after, slowTransfer)
	checkDeadline(t, "the write of the answer to a body", write[0], before, after, writeTimeout)

	rec, before, after = do(post(big))
	read, write = rec.deadlines()
	if len(read) != 1 || len(write) != 1 {
		t.Fatalf("a big body set %d read deadlines and %d write deadlines, want 1 and 1", len(read), len(write))
	}
	checkDeadline(t, "the read of a big body", read[0], before, after, slowTransfer)
	checkDeadline(t, "the write of the answer to a big body", write[0], before, after, slowTransfer)

	// An error is written with a new time, since the request may have taken long to receive.
	rec, before, after = do(get("/api/conflict"))
	if _, write = rec.deadlines(); len(write) != 1 || rec.Code != http.StatusConflict {
		t.Fatalf("an error set %d write deadlines and has the status %d, want 1 and 409", len(write), rec.Code)
	}
	checkDeadline(t, "the write of an error", write[0], before, after, writeTimeout)

	// A body that is refused before it is read does not get the time to be received.
	rec, _, _ = do(post(small, "Content-Type", "text/plain"))
	read, write = rec.deadlines()
	if len(read) != 0 || len(write) != 0 || rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("a body of the wrong type set %d read deadlines and %d write deadlines, and has the status %d, want 0, 0 and 415",
			len(read), len(write), rec.Code)
	}

	// A body that fails in the middle of its reading is answered with a new time to write.
	req := ws.bodyRequest(iotest.TimeoutReader(strings.NewReader(small)), -1)
	rec, before, after = do(req)
	read, write = rec.deadlines()
	if len(read) != 1 || len(write) != 1 || rec.Code != http.StatusBadRequest {
		t.Fatalf("a body that times out set %d read deadlines and %d write deadlines, and has the status %d, want 1, 1 and 400",
			len(read), len(write), rec.Code)
	}
	checkDeadline(t, "the write of the answer to a body that times out", write[0], before, after, writeTimeout)
}

// TestHandleWithDeadline shows that a route has the deadline that it was registered with, and the others have the
// deadline of the server.
func TestHandleWithDeadline(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ws := newWorkServer(t, ts, 1000)
	left := make(map[string]time.Duration)
	var mu sync.Mutex
	note := func(name string, r *http.Request) {
		d, ok := r.Context().Deadline()
		mu.Lock()
		defer mu.Unlock()
		left[name] = -1
		if ok {
			left[name] = time.Until(d)
		}
	}
	handler := func(name string) func(http.ResponseWriter, *http.Request, account.Principal) {
		return func(w http.ResponseWriter, r *http.Request, _ account.Principal) {
			note(name, r)
			w.WriteHeader(http.StatusNoContent)
		}
	}
	ts.api("GET /api/d/plain", handler("plain"))
	ts.apiHeavy("GET /api/d/heavy", handler("heavy"))
	ts.apiHeavy("GET /api/d/heavy/{id}", handler("heavy id"))
	ts.apiWithin("GET /api/d/within", 90*time.Second, handler("within"))
	ts.handleWithDeadline("GET /api/d/bare", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		note("bare", r)
		w.WriteHeader(http.StatusNoContent)
	}), 5*time.Second)

	for _, tt := range []struct {
		name, path string
		want       time.Duration
	}{
		{"plain", "/api/d/plain", defaultDeadline},
		{"heavy", "/api/d/heavy", heavyDeadline},
		{"heavy id", "/api/d/heavy/42", heavyDeadline},
		{"within", "/api/d/within", 90 * time.Second},
		{"bare", "/api/d/bare", 5 * time.Second},
	} {
		if res := ws.request("alice", http.MethodGet, tt.path, ""); res.StatusCode != http.StatusNoContent {
			t.Fatalf("GET %s has the status %d, want 204", tt.path, res.StatusCode)
		}
		mu.Lock()
		got := left[tt.name]
		mu.Unlock()
		if got < tt.want-5*time.Second || got > tt.want {
			t.Errorf("GET %s: the context has %v left, want about %v", tt.path, got, tt.want)
		}
	}
	if heavyDeadline != 21*time.Minute {
		t.Errorf("the deadline of a heavy route is %v, want 21 minutes: receive 10, wait 30 seconds, work 30 seconds, send 10", heavyDeadline)
	}

	// The deadline of the server is for the routes that have none of their own.
	ts.deadline = 20 * time.Millisecond
	ts.api("GET /api/d/wait", func(w http.ResponseWriter, r *http.Request, _ account.Principal) {
		<-r.Context().Done()
		ts.fail(w, r, r.Context().Err())
	})
	checkProblem(t, "a route with the deadline of the server", ws.request("alice", http.MethodGet, "/api/d/wait", ""), "busy")
	if res := ws.request("alice", http.MethodGet, "/api/d/within", ""); res.StatusCode != http.StatusNoContent {
		t.Errorf("a route with a deadline of its own has the status %d after the deadline of the server was shortened, want 204", res.StatusCode)
	}
}
