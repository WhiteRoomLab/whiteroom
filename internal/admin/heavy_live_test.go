package admin

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
)

// dialAndSend dials addr, sends request, which is the bytes of an HTTP request, and returns the connection, which the
// test closes. It reads nothing of the answer.
func dialAndSend(t *testing.T, addr, request string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatal(err)
	}
	return conn
}

// slowRequest is a request whose body the test sends as slowly as it likes.
type slowRequest struct {
	body   *io.PipeWriter
	result <-chan slowResult
}

type slowResult struct {
	status int
	body   string
	err    error
}

// slowPost starts a POST of path as the account with token, and sends the first part of its body.
func (ls *liveServer) slowPost(t *testing.T, path, token, first string) slowRequest {
	t.Helper()
	return ls.slowSend(t, http.MethodPost, path, token, first)
}

// slowSend starts a request of method to path as the account with token, and sends the first part of its body. It returns
// when the client has taken that part, so what finish sends comes after it.
func (ls *liveServer) slowSend(t *testing.T, method, path, token, first string) slowRequest {
	t.Helper()
	pr, pw := io.Pipe()
	req, err := http.NewRequestWithContext(t.Context(), method, ls.url+path, pr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", ls.origin)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", ls.wantCookieName()+"="+token)
	client := ls.srv.Client()
	result := async(func() slowResult {
		res, err := client.Do(req)
		if err != nil {
			return slowResult{err: err}
		}
		defer func() { _ = res.Body.Close() }()
		b, err := io.ReadAll(res.Body)
		return slowResult{status: res.StatusCode, body: string(b), err: err}
	})
	t.Cleanup(func() { _ = pw.Close() })
	written := async(func() struct{} { _, _ = io.WriteString(pw, first); return struct{}{} })
	receive(t, written, "the first part of the body to be taken")
	return slowRequest{body: pw, result: result}
}

// finish sends the rest of the body, which ends it.
func (s slowRequest) finish(rest string) {
	_, _ = io.WriteString(s.body, rest)
	_ = s.body.Close()
}

// TestHeavySlowSender shows that a body that is sent slowly is received outside the place, and counts for its account
// alone: the others work, and the account has as many such requests as it may.
func TestHeavySlowSender(t *testing.T) {
	ls := newLiveServer(t, false)
	ws := newWorkServer(t, ls.testServer, 1000)
	alice := ws.people["alice"].ID

	var slow [heavyPerAccount]slowRequest
	for i := range slow {
		slow[i] = ls.slowPost(t, "/api/work", ws.tokens["alice"], `{"name":"`)
	}
	waitFor(t, "four slow requests of alice to be counted", func() bool { return ls.inflight.count(alice) == heavyPerAccount })

	// Nobody has the place, so the others are answered at once.
	checkOK(t, "bob while alice sends slowly", receive(t, async(func() *http.Response { return ws.work("bob") }), "the request of bob"))
	if n := len(ls.heavy); n != 0 {
		t.Errorf("the place holds %d requests while bodies are being received, want none", n)
	}
	if n := ws.begun.Load(); n != 1 {
		t.Errorf("%d pieces of work have begun, want 1 (bob's)", n)
	}
	// The fifth request of alice is refused at once, whatever it is.
	checkProblem(t, "the fifth request of alice", ws.work("alice"), "too-many-requests")
	checkProblem(t, "a fifth body of alice", ws.request("alice", http.MethodPost, "/api/work", `{"name":"x"}`), "too-many-requests")

	for i, s := range slow {
		s.finish(fmt.Sprintf(`s%d"}`, i))
	}
	for i, s := range slow {
		r := receive(t, s.result, "a slow request")
		if want := fmt.Sprintf(`{"name":"s%d"}`, i); r.err != nil || r.status != http.StatusOK || r.body != want {
			t.Errorf("slow request %d: %d %q %v, want 200 %s", i, r.status, r.body, r.err, want)
		}
	}
	waitFor(t, "the requests of alice to end", func() bool { return ls.inflight.count(alice) == 0 })
	checkOK(t, "alice after her slow requests", ws.work("alice"))
	if files := ls.tempFiles(t); len(files) != 0 {
		t.Errorf("%d temporary files stay: %v", len(files), files)
	}
}

// TestHeavySlowReader shows that an answer that is not read does not hold the place: the work is done and the place is
// given back before the answer is written, and the answer waits in a temporary file for the connection, which counts for its
// account alone.
func TestHeavySlowReader(t *testing.T) {
	ls := newLiveServer(t, false)
	ws := newWorkServer(t, ls.testServer, 1000)
	alice := ws.people["alice"].ID
	const huge = 64 << 20 // more than the buffers of the sockets of both ends hold
	handled := make(chan struct{}, 4)
	ls.apiHeavy("GET /api/huge", func(w http.ResponseWriter, r *http.Request, p account.Principal) {
		defer func() { handled <- struct{}{} }()
		ls.heavyWork(w, r, p, 0, func(_ context.Context, _, out *spool) (int, http.Header, error) {
			ws.begun.Add(1)
			return http.StatusOK, nil, writeBig(out, huge)
		})
	})
	var holding atomic.Int32
	sending := make(chan struct{}, 8)
	ls.hook = func(point string) {
		if point == "sending" {
			if len(ls.heavy) != 0 {
				holding.Add(1)
			}
			sending <- struct{}{}
		}
	}

	host := strings.TrimPrefix(ls.url, "http://")
	request := "GET /api/huge HTTP/1.1\r\nHost: " + host + "\r\nCookie: " + ls.wantCookieName() + "=" + ws.tokens["alice"] +
		"\r\nConnection: close\r\n\r\n"
	conn := dialAndSend(t, host, request)
	receive(t, sending, "the answer to be ready")
	if n := len(ls.heavy); n != 0 {
		t.Errorf("the place holds %d requests while an answer waits to be sent, want none", n)
	}

	// The connection does not read, and the handler is writing; yet the others work.
	checkOK(t, "bob while an answer is not read", receive(t, async(func() *http.Response { return ws.work("bob") }), "the request of bob"))
	select {
	case <-handled:
		t.Error("the handler ended while the answer could not be written")
	default:
	}
	if n := ls.inflight.count(alice); n != 1 {
		t.Errorf("alice has %d requests counted while the answer is not read, want 1", n)
	}

	// The answer that is not read is gone when its connection is.
	_ = conn.Close()
	receive(t, handled, "the handler to end")
	waitFor(t, "the request of alice to end", func() bool { return ls.inflight.count(alice) == 0 })
	if n := holding.Load(); n != 0 {
		t.Errorf("the hook \"sending\" found the place taken %d times", n)
	}
	if files := ls.tempFiles(t); len(files) != 0 {
		t.Errorf("%d temporary files stay after the connection closed: %v", len(files), files)
	}
}
