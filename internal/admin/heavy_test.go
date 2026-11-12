package admin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
)

// async runs f in a goroutine, and returns the channel of its result.
func async[T any](f func() T) <-chan T {
	ch := make(chan T, 1)
	go func() { ch <- f() }()
	return ch
}

// receive waits for the result of async, which the tests expect within seconds.
func receive[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
	var zero T
	return zero
}

// quiet checks that ch has no result after a short while: what would send one has to wait.
func quiet[T any](t *testing.T, ch <-chan T, what string) {
	t.Helper()
	select {
	case <-ch:
		t.Error(what)
	case <-time.After(100 * time.Millisecond):
	}
}

// gate stops the first request that is in the place of heavy work, at its hook "checked", until it is opened.
type gate struct {
	held    chan struct{} // gets a value when a request is stopped
	opened  chan struct{}
	calls   atomic.Int32 // the requests that have reached "checked"
	openOne sync.Once
}

func newGate(t *testing.T, ts *testServer) *gate {
	t.Helper()
	g := &gate{held: make(chan struct{}, 1), opened: make(chan struct{})}
	ts.hook = func(point string) {
		if point == "checked" && g.calls.Add(1) == 1 {
			g.held <- struct{}{}
			<-g.opened
		}
	}
	t.Cleanup(g.open)
	return g
}

func (g *gate) open() { g.openOne.Do(func() { close(g.opened) }) }

func (g *gate) waitHeld(t *testing.T) {
	t.Helper()
	select {
	case <-g.held:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a request to have the place")
	}
}

// workServer is a test server with the accounts alice, bob and carol, who are logged in, and the routes
// GET /api/work, which works in the place and answers {"ok":true},
// POST /api/work, which receives a body of at most bodyLimit bytes, reads in the place the name that it carries, and answers it,
// GET /api/big, which answers bigAnswer bytes.
// The work counts the pieces of work that begin.
type workServer struct {
	*testServer
	begun  atomic.Int32
	tokens map[string]string
	people map[string]account.Account
	// duringWork is the number of temporary files that the last POST /api/work saw when its work began.
	duringWork atomic.Int32
}

const bigAnswer = 3 * spoolMemory // an answer that does not stay in memory

func newWorkServer(t *testing.T, ts *testServer, bodyLimit int64) *workServer {
	t.Helper()
	ws := &workServer{testServer: ts, tokens: make(map[string]string), people: make(map[string]account.Account)}
	for _, login := range []string{"alice", "bob", "carol"} {
		ws.people[login] = ts.addAccount(t, login, account.RoleViewer)
		ws.tokens[login] = ts.logIn(t, login)
	}
	ts.apiHeavy("GET /api/work", func(w http.ResponseWriter, r *http.Request, p account.Principal) {
		ts.heavyWork(w, r, p, 0, func(_ context.Context, _, out *spool) (int, http.Header, error) {
			ws.begun.Add(1)
			return http.StatusOK, nil, writeAnswer(out, reply{OK: true})
		})
	})
	ts.apiHeavy("POST /api/work", func(w http.ResponseWriter, r *http.Request, p account.Principal) {
		ts.heavyWork(w, r, p, bodyLimit, func(_ context.Context, in, out *spool) (int, http.Header, error) {
			ws.begun.Add(1)
			ws.duringWork.Store(int32(len(ts.tempFiles(t))))
			var body testIn
			if err := readDocument(in, &body); err != nil {
				return 0, nil, err
			}
			return http.StatusOK, nil, writeAnswer(out, body)
		})
	})
	ts.apiHeavy("GET /api/big", func(w http.ResponseWriter, r *http.Request, p account.Principal) {
		ts.heavyWork(w, r, p, 0, func(_ context.Context, _, out *spool) (int, http.Header, error) {
			ws.begun.Add(1)
			return http.StatusOK, nil, writeBig(out, bigAnswer)
		})
	})
	return ws
}

// writeBig writes a JSON string of n bytes in all.
func writeBig(out io.Writer, n int) error {
	chunk := bytes.Repeat([]byte("a"), 1<<20)
	if _, err := out.Write([]byte(`"`)); err != nil {
		return err
	}
	for rest := n - 2; rest > 0; rest -= len(chunk) {
		if _, err := out.Write(chunk[:min(rest, len(chunk))]); err != nil {
			return err
		}
	}
	_, err := out.Write([]byte(`"`))
	return err
}

// request sends a request as the account login, from the origin of the server, with a body of JSON unless headers say another.
func (ws *workServer) request(login, method, path, body string, headers ...string) *http.Response {
	all := append(ws.cookie(ws.tokens[login]), "Origin", ws.origin)
	if body != "" && !hasHeader(headers, "Content-Type") {
		all = append(all, "Content-Type", "application/json")
	}
	return ws.send(method, path, body, append(all, headers...)...)
}

func (ws *workServer) work(login string) *http.Response {
	return ws.request(login, http.MethodGet, "/api/work", "")
}

func checkOK(t *testing.T, what string, res *http.Response) {
	t.Helper()
	if got := bodyOf(t, res); res.StatusCode != http.StatusOK || got != `{"ok":true}` {
		t.Errorf("%s: %d %s, want 200 {\"ok\":true}", what, res.StatusCode, got)
	}
}

func TestHeavyOneAtATime(t *testing.T) {
	ws := newWorkServer(t, newTestServer(t, httpsOrigin), 1000)
	g := newGate(t, ws.testServer)

	first := async(func() *http.Response { return ws.work("alice") })
	g.waitHeld(t)
	if n := len(ws.heavy); n != 1 {
		t.Errorf("the place holds %d requests while one works in it, want 1", n)
	}
	second := async(func() *http.Response { return ws.work("bob") })
	quiet(t, second, "a request was answered while another had the place")
	if n := g.calls.Load(); n != 1 {
		t.Errorf("%d requests have worked in the place at once, want 1", n)
	}

	g.open()
	checkOK(t, "the first request", receive(t, first, "the first request"))
	checkOK(t, "the second request", receive(t, second, "the second request"))
	if n := len(ws.heavy); n != 0 {
		t.Errorf("the place holds %d requests at the end, want none", n)
	}
	if n := ws.begun.Load(); n != 2 {
		t.Errorf("%d pieces of work have begun, want 2", n)
	}
}

func TestHeavyWaitEnds(t *testing.T) {
	ws := newWorkServer(t, newTestServer(t, httpsOrigin), 1000)
	ws.placeWait = 50 * time.Millisecond
	g := newGate(t, ws.testServer)
	first := async(func() *http.Response { return ws.work("alice") })
	g.waitHeld(t)

	start := time.Now()
	res := ws.work("bob")
	checkProblem(t, "a request that waits for the place for longer than it may", res, "busy")
	if took := time.Since(start); took < 40*time.Millisecond {
		t.Errorf("the request waited %v for the place, want 50ms", took)
	}
	if n := g.calls.Load(); n != 1 || ws.begun.Load() != 0 {
		t.Errorf("the request that gave up has worked: %d reached the place, %d pieces of work have begun", n, ws.begun.Load())
	}
	if n := len(ws.heavy); n != 1 {
		t.Errorf("the place holds %d requests after the other one gave up, want the first only", n)
	}
	if got := ws.logs.String(); got != "" {
		t.Errorf("a request that gave up logged %q", got)
	}

	g.open()
	checkOK(t, "the first request", receive(t, first, "the first request"))
	checkOK(t, "a request after them", ws.work("bob"))
	if n := ws.inflight.count(ws.people["bob"].ID); n != 0 {
		t.Errorf("bob has %d requests counted at the end, want none", n)
	}
}

func TestHeavyRunEnds(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ws := newWorkServer(t, ts, 1000)
	var left atomic.Int64
	work := func(wait bool) func(http.ResponseWriter, *http.Request, account.Principal) {
		return func(w http.ResponseWriter, r *http.Request, p account.Principal) {
			ts.heavyWork(w, r, p, 0, func(ctx context.Context, _, out *spool) (int, http.Header, error) {
				d, ok := ctx.Deadline()
				if !ok {
					return 0, nil, errors.New("the context of the work has no deadline")
				}
				left.Store(int64(time.Until(d)))
				if wait {
					<-ctx.Done()
					return 0, nil, fmt.Errorf("reading: %w", ctx.Err())
				}
				return http.StatusOK, nil, writeAnswer(out, reply{OK: true})
			})
		}
	}
	ts.apiHeavy("GET /api/stuck", work(true))
	ts.apiHeavy("GET /api/quick", work(false))
	get := func(path string) *http.Response { return ws.request("alice", http.MethodGet, path, "") }

	// The work has heavyRun from the time that it has the place.
	checkOK(t, "a piece of work in time", get("/api/quick"))
	if d := time.Duration(left.Load()); d < heavyRun-5*time.Second || d > heavyRun {
		t.Errorf("the context of the work has %v left when it begins, want about %v", d, heavyRun)
	}

	ws.placeRun = 50 * time.Millisecond
	start := time.Now()
	checkProblem(t, "a piece of work that does not end in time", get("/api/stuck"), "busy")
	if took := time.Since(start); took < 40*time.Millisecond || took > 5*time.Second {
		t.Errorf("the work was ended after %v, want 50ms", took)
	}
	if n := len(ws.heavy); n != 0 {
		t.Errorf("the place holds %d requests after the work ended, want none", n)
	}
	if got := ws.logs.String(); got != "" {
		t.Errorf("a context that ended logged %q", got)
	}
}

func TestHeavyPanic(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ws := newWorkServer(t, ts, 1000)
	ts.apiHeavy("GET /api/boom", func(w http.ResponseWriter, r *http.Request, p account.Principal) {
		ts.heavyWork(w, r, p, 0, func(context.Context, *spool, *spool) (int, http.Header, error) { panic("boom") })
	})
	checkProblem(t, "a piece of work that panics", ws.request("alice", http.MethodGet, "/api/boom", ""), "internal")
	if !strings.HasPrefix(ts.logs.String(), "panic serving GET \"/api/boom\": boom\n") {
		t.Errorf("the log of the panic is %q", firstLine(ts.logs.String()))
	}
	if n, counted := len(ws.heavy), ws.inflight.count(ws.people["alice"].ID); n != 0 || counted != 0 {
		t.Errorf("after a panic the place holds %d requests and alice has %d counted, want none", n, counted)
	}
	if files := ts.tempFiles(t); len(files) != 0 {
		t.Errorf("after a panic %d temporary files stay", len(files))
	}
	ts.logs.Reset()
	checkOK(t, "a request after the panic", ws.work("alice"))
}

// TestHeavyPoints shows where the hooks of the tests are called: "checked" in the place, "sending" after it is given back
// and before the answer is written, also when the answer is an error, and neither if the place was not had.
func TestHeavyPoints(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ws := newWorkServer(t, ts, 1000)
	var mu sync.Mutex
	var points []string
	ts.hook = func(point string) {
		mu.Lock()
		defer mu.Unlock()
		points = append(points, fmt.Sprintf("%s:%d", point, len(ts.heavy)))
	}
	seen := func() string {
		mu.Lock()
		defer mu.Unlock()
		defer func() { points = nil }()
		return strings.Join(points, " ")
	}
	checkOK(t, "a request", ws.work("alice"))
	if got, want := seen(), "checked:1 sending:0"; got != want {
		t.Errorf("the hooks of a request were called as %q, want %q", got, want)
	}

	ts.apiHeavy("GET /api/conflict", func(w http.ResponseWriter, r *http.Request, p account.Principal) {
		ts.heavyWork(w, r, p, 0, func(context.Context, *spool, *spool) (int, http.Header, error) { return 0, nil, edit.ErrConflict })
	})
	checkProblem(t, "a conflict", ws.request("alice", http.MethodGet, "/api/conflict", ""), "conflict")
	if got, want := seen(), "checked:1 sending:0"; got != want {
		t.Errorf("the hooks of a request that fails were called as %q, want %q", got, want)
	}

	// A request that gives up on the place has no point to call.
	ws.placeWait = time.Millisecond
	ts.heavy <- struct{}{}
	checkProblem(t, "a request that finds no place", ws.work("alice"), "busy")
	<-ts.heavy
	if got := seen(); got != "" {
		t.Errorf("the hooks of a request that finds no place were called as %q, want none", got)
	}
}

func TestHeavyCounts(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ws := newWorkServer(t, ts, 1000)
	alice := ws.people["alice"].ID
	g := newGate(t, ts)

	// One has the place and three wait for it: all four are counted.
	var four [heavyPerAccount]<-chan *http.Response
	four[0] = async(func() *http.Response { return ws.work("alice") })
	g.waitHeld(t)
	for i := 1; i < heavyPerAccount; i++ {
		four[i] = async(func() *http.Response { return ws.work("alice") })
	}
	waitFor(t, "four requests of alice to be counted", func() bool { return ws.inflight.count(alice) == heavyPerAccount })

	start := time.Now()
	checkProblem(t, "the fifth request of an account", ws.work("alice"), "too-many-requests")
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("the fifth request waited %v, want no wait", took)
	}
	if n := ws.inflight.count(alice); n != heavyPerAccount {
		t.Errorf("alice has %d requests counted after the fifth was refused, want %d", n, heavyPerAccount)
	}
	if n := ws.inflight.count(ws.people["bob"].ID); n != 0 {
		t.Errorf("bob has %d requests counted, want none", n)
	}

	// The others are not refused: they wait for the place, in the order in which they came.
	bob := async(func() *http.Response { return ws.work("bob") })
	waitFor(t, "the request of bob to be counted", func() bool { return ws.inflight.count(ws.people["bob"].ID) == 1 })

	g.open()
	for i, ch := range four {
		checkOK(t, fmt.Sprintf("request %d of alice", i+1), receive(t, ch, "a request of alice"))
	}
	checkOK(t, "the request of bob", receive(t, bob, "the request of bob"))
	if n := ws.inflight.count(alice); n != 0 {
		t.Errorf("alice has %d requests counted at the end, want none", n)
	}
	if len(ws.inflight.n) != 0 {
		t.Errorf("the counts keep %d accounts at the end, want none", len(ws.inflight.n))
	}
	checkOK(t, "a request after them", ws.work("alice"))
}

func TestHeavyCountsAfterABusyAnswer(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ws := newWorkServer(t, ts, 1000)
	alice := ws.people["alice"].ID
	ws.placeWait = 50 * time.Millisecond
	g := newGate(t, ts)

	holder := async(func() *http.Response { return ws.work("alice") })
	g.waitHeld(t)
	var waiting [heavyPerAccount - 1]<-chan *http.Response
	for i := range waiting {
		waiting[i] = async(func() *http.Response { return ws.work("alice") })
	}
	for _, ch := range waiting {
		checkProblem(t, "a request that gives up on the place", receive(t, ch, "a request that gives up"), "busy")
	}
	if n := ws.inflight.count(alice); n != 1 {
		t.Errorf("alice has %d requests counted after three gave up, want the one that works", n)
	}
	g.open()
	checkOK(t, "the request that worked", receive(t, holder, "the request that worked"))
	if n := ws.inflight.count(alice); n != 0 {
		t.Errorf("alice has %d requests counted at the end, want none", n)
	}
}

// TestHeavySending shows that the place is given back before the answer is written: a request that is stopped when its
// answer is made and not yet written, which is what a reader that does not read looks like to the others, does not keep the
// others from the place.
func TestHeavySending(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ws := newWorkServer(t, ts, 1000)
	stopped, opened := make(chan struct{}, 1), make(chan struct{})
	var calls atomic.Int32
	ts.hook = func(point string) {
		if point == "sending" && calls.Add(1) == 1 {
			stopped <- struct{}{}
			<-opened
		}
	}
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(opened) }) })

	first := async(func() *http.Response { return ws.work("alice") })
	receive(t, stopped, "the first answer to be made")
	if n := len(ws.heavy); n != 0 {
		t.Errorf("the place holds %d requests while an answer waits to be written, want none", n)
	}
	checkOK(t, "bob while the answer of alice is not written", receive(t, async(func() *http.Response { return ws.work("bob") }), "the request of bob"))
	quiet(t, first, "the first request was answered while it was stopped before it wrote")
	if n := ws.inflight.count(ws.people["alice"].ID); n != 1 {
		t.Errorf("alice has %d requests counted while her answer waits, want 1", n)
	}

	once.Do(func() { close(opened) })
	checkOK(t, "the first request", receive(t, first, "the first request"))
	if n := ws.inflight.count(ws.people["alice"].ID); n != 0 {
		t.Errorf("alice has %d requests counted at the end, want none", n)
	}
}
