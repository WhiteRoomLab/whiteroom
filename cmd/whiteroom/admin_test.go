//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"io/fs"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// adminWait is how long the tests wait for a start, a stop or a build of serve admin.
const adminWait = 60 * time.Second

// adminSite is a data directory that whiteroom init has made, and the directories that serve admin works with.
type adminSite struct {
	data, site, out, cache string
	theme                  string // the argument of -theme, "" for SITE/theme
}

// initSample makes a data directory with the sample site imported by whiteroom init, from a copy of the sample site, which
// is the site directory of the result, with its theme. The output and the cache directories are new ones of the test.
func initSample(t *testing.T) adminSite {
	t.Helper()
	site := copySample(t)
	data := filepath.Join(t.TempDir(), "data")
	if code, _, stderr := runInitCmd(t, initPassword+"\n", adminFlags("-data", data, site)...); code != 0 {
		t.Fatalf("whiteroom init of the sample site = %d, with stderr:\n%s", code, stderr)
	}
	return adminSite{
		data: data, site: site, out: filepath.Join(t.TempDir(), "out"), cache: filepath.Join(t.TempDir(), "cache"),
	}
}

// initEmpty makes a data directory with the first administrator and no document, and a site directory that has only the
// schema file of the sample site, which the example theme of -theme goes with: the builds of its server fail, soon.
func initEmpty(t *testing.T) adminSite {
	t.Helper()
	data := filepath.Join(t.TempDir(), "data")
	if code, _, stderr := runInitCmd(t, initPassword+"\n", adminFlags("-data", data)...); code != 0 {
		t.Fatalf("whiteroom init = %d, with stderr:\n%s", code, stderr)
	}
	schema, err := os.ReadFile(filepath.Join(sampleSite, "site", "schema.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	site := t.TempDir()
	if err := os.Mkdir(filepath.Join(site, "site"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(site, "site", "schema.yaml"), schema, 0o644); err != nil {
		t.Fatal(err)
	}
	return adminSite{
		data: data, site: site, out: filepath.Join(t.TempDir(), "out"), cache: filepath.Join(t.TempDir(), "cache"),
		theme: exampleTheme,
	}
}

// args returns the arguments of whiteroom serve admin for s, with the flags more before SITE.
func (s adminSite) args(more ...string) []string {
	args := []string{"-data", s.data, "-out", s.out, "-cache", s.cache}
	if s.theme != "" {
		args = append(args, "-theme", s.theme)
	}
	return append(append(args, more...), s.site)
}

// config writes a config file with the items of s, as absolute paths, and the lines more, and returns its path.
func (s adminSite) config(t *testing.T, more ...string) string {
	t.Helper()
	return s.configIn(t, t.TempDir(), more...)
}

// configIn is config, with the file in the directory dir.
func (s adminSite) configIn(t *testing.T, dir string, more ...string) string {
	t.Helper()
	lines := []string{"data: " + strconv.Quote(s.data), "site: " + strconv.Quote(s.site), "out: " + strconv.Quote(s.out),
		"cache: " + strconv.Quote(s.cache)}
	if s.theme != "" {
		theme, err := filepath.Abs(s.theme)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, "theme: "+strconv.Quote(theme))
	}
	return writeConfig(t, dir, "whiteroom.yaml", strings.Join(append(lines, more...), "\n")+"\n")
}

// socketDir returns a new directory for a socket, whose path with a name of one byte is short enough for a socket: a
// directory of the test, or, if that is too long, as under a long TMPDIR, one in /tmp, which is removed after the test.
func socketDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if len(dir) > 100 {
		var err error
		if dir, err = os.MkdirTemp("/tmp", "wr"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
	}
	return dir
}

// socketPath returns the path of a socket in a new directory.
func socketPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(socketDir(t), "s")
}

// adminProc is a whiteroom serve admin that runs in a goroutine and listens.
type adminProc struct {
	line          string // the first line that it printed on stdout
	origin        string // that which the line tells
	network, addr string // "tcp" and the address, or "unix" and the path of the socket, as the line tells
	base          string // the URL of the origin, but http, for the requests
	client        *http.Client
	stderr        *syncBuffer
	cancel        context.CancelFunc
	exited        chan struct{} // closed when the command has returned, with code set
	code          int
}

var adminStartLine = regexp.MustCompile(`^serving admin of (.+) at (\S+) \(listening on (socket )?(\S+)\)\n$`)

// startAdminArgs runs whiteroom serve admin with args and a context of ctx, in a goroutine, and returns it when it has
// printed its first line, which must be that of a server that listens. Its client connects to the address or the socket
// that the line tells, whatever the URL says.
func startAdminArgs(t *testing.T, ctx context.Context, args ...string) *adminProc {
	t.Helper()
	what := "whiteroom serve admin " + strings.Join(args, " ")
	ctx, cancel := context.WithCancel(ctx)
	stdout, stderr := make(chanWriter, 10), new(syncBuffer)
	a := &adminProc{stderr: stderr, cancel: cancel, exited: make(chan struct{})}
	go func() {
		a.code = run(ctx, append([]string{"serve", "admin"}, args...), testEnv(strings.NewReader(""), stdout, stderr))
		close(a.exited)
	}()
	t.Cleanup(func() {
		cancel()
		a.client.CloseIdleConnections()
		select {
		case <-time.After(adminWait):
			t.Errorf("%s has not returned in %v after its context is done", what, adminWait)
		case <-a.exited:
		}
	})
	a.client = &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       adminWait,
	}
	select {
	case a.line = <-stdout:
	case <-a.exited:
		t.Fatalf("%s = %d, with stderr:\n%s", what, a.code, stderr)
	case <-time.After(adminWait):
		t.Fatalf("%s has printed nothing in %v, and on stderr:\n%s", what, adminWait, stderr)
	}
	m := adminStartLine.FindStringSubmatch(a.line)
	if m == nil {
		t.Fatalf("%s printed %q, which does not match %s", what, a.line, adminStartLine)
	}
	a.origin, a.network, a.addr = m[2], "tcp", m[4]
	if m[3] != "" {
		a.network = "unix"
	}
	u, err := url.Parse(a.origin)
	if err != nil {
		t.Fatalf("%s tells the origin %q: %v", what, a.origin, err)
	}
	a.base = "http://" + u.Host
	a.client.Transport = &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return new(net.Dialer).DialContext(ctx, a.network, a.addr)
		},
		ExpectContinueTimeout: adminWait,
	}
	return a
}

// startAdmin is startAdminArgs with -addr 127.0.0.1:0, the system's choice of a port on the loopback interface, if args
// have neither -addr nor -socket. No test listens on a fixed port.
func startAdmin(t *testing.T, ctx context.Context, args ...string) *adminProc {
	t.Helper()
	if !slices.Contains(args, "-addr") && !slices.Contains(args, "-socket") {
		args = append([]string{"-addr", "127.0.0.1:0"}, args...)
	}
	return startAdminArgs(t, ctx, args...)
}

// stop ends the context of the command, waits for it to return, and returns its exit code.
func (a *adminProc) stop(t *testing.T) int {
	t.Helper()
	a.cancel()
	a.client.CloseIdleConnections()
	select {
	case <-a.exited:
		return a.code
	case <-time.After(adminWait):
		t.Fatalf("whiteroom serve admin has not returned in %v after its context is done", adminWait)
		return -1
	}
}

// newRequest returns a request to the server for the path, with the Origin of the server and the headers, name and value
// by turns; the header Host sets the Host of the request.
func (a *adminProc) newRequest(ctx context.Context, method, path string, body io.Reader, headers ...string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Origin", a.origin)
	for i := 0; i+1 < len(headers); i += 2 {
		if strings.EqualFold(headers[i], "Host") {
			req.Host = headers[i+1]
			continue
		}
		req.Header.Set(headers[i], headers[i+1])
	}
	return req, nil
}

// exchange sends a request, with a body of the JSON text body, if it is not "", and returns the status and the body of the
// response.
func (a *adminProc) exchange(t *testing.T, method, path, body string, headers ...string) (status int, answer []byte) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
		headers = append(headers, "Content-Type", "application/json")
	}
	req, err := a.newRequest(t.Context(), method, path, r, headers...)
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()
	answer, err = io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return res.StatusCode, answer
}

// tryLogin posts a login and returns the status of the response and, for a login that succeeds, the cookie of the session,
// as the "name=value" that a request sends: the cookie is Secure, so a client that keeps cookies does not send it over http.
func (a *adminProc) tryLogin(t *testing.T, login, password string, headers ...string) (status int, cookie string) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"login": login, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	req, err := a.newRequest(t.Context(), http.MethodPost, "/api/session", bytes.NewReader(body),
		append([]string{"Content-Type", "application/json"}, headers...)...)
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.client.Do(req)
	if err != nil {
		t.Fatalf("POST /api/session: %v", err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	if lines := res.Header.Values("Set-Cookie"); res.StatusCode == http.StatusOK && len(lines) == 1 {
		cookie, _, _ = strings.Cut(lines[0], ";")
	}
	return res.StatusCode, cookie
}

// login logs in as the first administrator, and returns the cookie.
func (a *adminProc) login(t *testing.T, headers ...string) string {
	t.Helper()
	status, cookie := a.tryLogin(t, initLogin, initPassword, headers...)
	if status != http.StatusOK || cookie == "" {
		t.Fatalf("the login of the administrator = %d, with the cookie %t, want 200 and one\nstderr:\n%s", status, cookie != "",
			a.stderr)
	}
	return cookie
}

// buildsBody is the part of the answer of GET /api/builds that the tests read.
type buildsBody struct {
	Running json.RawMessage `json:"running"`
	Pending bool            `json:"pending"`
	Last    *struct {
		Release  string `json:"release"`
		Failed   *bool  `json:"failed"`
		Error    string `json:"error"`
		Requests []struct {
			Reason string `json:"reason"`
		} `json:"requests"`
	} `json:"last"`
}

// builds returns the state of the builds, for the session of cookie.
func (a *adminProc) builds(t *testing.T, cookie string) buildsBody {
	t.Helper()
	status, answer := a.exchange(t, http.MethodGet, "/api/builds", "", "Cookie", cookie)
	var b buildsBody
	if err := json.Unmarshal(answer, &b); status != http.StatusOK || err != nil {
		t.Fatalf("GET /api/builds = %d (%v), with the body %s", status, err, answer)
	}
	return b
}

// waitBuilt waits until no build runs or is wanted, and one has ended, and returns the state of the builds.
func (a *adminProc) waitBuilt(t *testing.T, cookie string) buildsBody {
	t.Helper()
	deadline := time.Now().Add(adminWait)
	for {
		if b := a.builds(t, cookie); len(b.Running) == 0 && !b.Pending && b.Last != nil {
			return b
		}
		if time.Now().After(deadline) {
			t.Fatalf("no build has ended in %v\nstderr:\n%s", adminWait, a.stderr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// failedBuild reports whether the last build of b has failed.
func failedBuild(b buildsBody) bool { return b.Last != nil && b.Last.Failed != nil && *b.Last.Failed }

// cancelWriter cancels a context when it is written to, and drops what it gets.
type cancelWriter struct{ cancel context.CancelFunc }

func (w cancelWriter) Write(p []byte) (int, error) {
	w.cancel()
	return len(p), nil
}

// runAdminCmd runs whiteroom serve admin with args, to the end. If the server starts, it prints its line, and that stops it
// at once, so that a test of a failure does not wait for a server that it should not have got.
func runAdminCmd(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), adminWait)
	defer cancel()
	var out, e bytes.Buffer
	env := testEnv(strings.NewReader(""), io.MultiWriter(&out, cancelWriter{cancel}), &e)
	code = run(ctx, append([]string{"serve", "admin"}, args...), env)
	return code, out.String(), e.String()
}

// checkUnlocked checks that the data directory can be opened as the writer, which is not so while a process that has it open
// has not closed it.
func checkUnlocked(t *testing.T, what, data string) {
	t.Helper()
	s, err := store.Open(data)
	if err != nil {
		t.Errorf("%s: %v", what, err)
		return
	}
	if err := s.Close(); err != nil {
		t.Error(err)
	}
}

// manifestOf returns the manifest of the files of dir, as the manifest.txt of the golden files has it: a line for each
// file, with its size and its SHA-256.
func manifestOf(t *testing.T, dir string) string {
	t.Helper()
	var names []string
	err := fs.WalkDir(os.DirFS(dir), ".", func(name string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			names = append(names, name)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	var b strings.Builder
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s %d %x\n", name, len(data), sha256.Sum256(data))
	}
	return b.String()
}

// activityActions returns the actions of the records of the activity log in the data directory, which no process has open.
func activityActions(t *testing.T, data string) []audit.Action {
	t.Helper()
	s, err := store.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var actions []audit.Action
	err = s.ReadAudit(t.Context(), audit.Filter{Log: audit.ActivityLog}, func(r audit.Record) bool {
		actions = append(actions, r.Action)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	return actions
}

// loginClients returns the addresses of the clients of the logins that succeeded, from the login history of the data
// directory, which a server may have open.
func loginClients(t *testing.T, data string) []string {
	t.Helper()
	s, err := store.OpenReader(data)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var clients []string
	err = s.ReadAudit(t.Context(), audit.Filter{Log: audit.LoginLog}, func(r audit.Record) bool {
		if r.Action == audit.Login {
			clients = append(clients, r.Client.Addr)
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	return clients
}

// freePort returns an address on 127.0.0.1 that no process listens on, as far as the system tells.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// TestServeAdmin starts the server on the data directory that whiteroom init has made of the sample site, on a port of
// 127.0.0.1 that the system chooses, without an origin: its first line tells the origin and the address, which are that
// port. A login works, the start builds the release that whiteroom build makes of the same site, into the output
// directory, and the context ends the server with 0: its port is closed, and the data directory can be opened again.
func TestServeAdmin(t *testing.T) {
	s := initSample(t)
	a := startAdmin(t, t.Context(), s.args()...)
	m := regexp.MustCompile(`^serving admin of ` + regexp.QuoteMeta(s.data) +
		` at http://127\.0\.0\.1:([1-9][0-9]*) \(listening on 127\.0\.0\.1:([1-9][0-9]*)\)\n$`).FindStringSubmatch(a.line)
	if m == nil || m[1] != m[2] {
		t.Fatalf("whiteroom serve admin printed %q, want the line of the data directory %s with the origin and the address on one port",
			a.line, s.data)
	}

	cookie := a.login(t)
	built := a.waitBuilt(t, cookie)
	if failedBuild(built) || built.Last.Release == "" {
		t.Fatalf("the start build has %+v, want a release\nstderr:\n%s", built.Last, a.stderr)
	}
	release := currentOf(t, s.out)
	if built.Last.Release != release {
		t.Errorf("the build says that it has published %s, and current is %s", built.Last.Release, release)
	}
	if got, want := manifestOf(t, filepath.Join(s.out, "releases", release)), readGolden(t, "manifest.txt"); got != want {
		t.Errorf("the release of the start differs from the golden files of the sample site")
	}
	out := t.TempDir()
	buildSample(t, out, t.TempDir())
	if want := currentOf(t, out); release != want {
		t.Errorf("the start publishes the release %s, whiteroom build of the site %s", release, want)
	}

	if code := a.stop(t); code != 0 {
		t.Errorf("whiteroom serve admin = %d after its context is done, want 0", code)
	}
	if conn, err := net.DialTimeout("tcp", a.addr, 2*time.Second); err == nil {
		_ = conn.Close()
		t.Errorf("%s accepts a connection after the server has stopped", a.addr)
	}
	checkUnlocked(t, "after the stop", s.data)
	if got := a.stderr.String(); got != "" {
		t.Errorf("whiteroom serve admin printed on stderr:\n%s", got)
	}
}

// TestServeAdminEmpty starts the server where there is nothing to build, which it serves: on a data directory without a
// document, of a site directory that has only its schema file, with a theme of -theme, and on one that has no account. A
// login works for the first, and fails with 401 for the second, which has no account. The build of the first fails.
func TestServeAdminEmpty(t *testing.T) {
	s := initEmpty(t)
	a := startAdmin(t, t.Context(), s.args()...)
	cookie := a.login(t)
	built := a.waitBuilt(t, cookie)
	if !failedBuild(built) {
		t.Errorf("the start build of a store without documents has %+v, want a failure", built.Last)
	}
	if code := a.stop(t); code != 0 {
		t.Errorf("whiteroom serve admin = %d after its context is done, want 0", code)
	}
	if _, err := os.Lstat(filepath.Join(s.site, "out")); err == nil {
		t.Errorf("%s has an out/, though -out is given", s.site)
	}

	data := filepath.Join(t.TempDir(), "data")
	st, err := store.Create(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	none := s
	none.data = data
	b := startAdmin(t, t.Context(), none.args()...)
	if status, cookie := b.tryLogin(t, initLogin, initPassword); status != http.StatusUnauthorized || cookie != "" {
		t.Errorf("a login to a store without accounts = %d, with a cookie %t, want 401 and none", status, cookie != "")
	}
	if code := b.stop(t); code != 0 {
		t.Errorf("whiteroom serve admin = %d after its context is done, want 0", code)
	}
}

// adminPNG returns a PNG of 64 by 64 pixels that is larger than the 4096 bytes that the shutdown test sends first.
func adminPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	rng := rand.New(rand.NewPCG(64, 64))
	for i := range img.Pix {
		img.Pix[i] = byte(rng.UintN(256))
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	if b.Len() < 8192 {
		t.Fatalf("the PNG has %d bytes", b.Len())
	}
	return b.Bytes()
}

// TestServeAdminShutdown ends the context of the server while an upload is in progress, which the server lets end before
// it returns: the response is 201, and then it returns 0. It then ends the context while the start build runs, which the
// server cancels: it returns 0 too, and the activity log has the record of the build, which the Publisher writes before it
// returns, so that the data directory is not closed before.
func TestServeAdminShutdown(t *testing.T) {
	s := initSample(t)
	a := startAdmin(t, t.Context(), s.args()...)
	cookie := a.login(t)
	a.waitBuilt(t, cookie)

	// The server answers Expect: 100-continue when its handler first reads the body, which tells that the handler is at it.
	file := adminPNG(t)
	pr, pw := io.Pipe()
	got100 := make(chan struct{})
	trace := &httptrace.ClientTrace{Got100Continue: func() { close(got100) }}
	req, err := a.newRequest(httptrace.WithClientTrace(t.Context(), trace), http.MethodPost, "/api/media?fileName=a.png", pr,
		"Content-Type", "image/png", "Cookie", cookie, "Expect", "100-continue")
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		status int
		err    error
	}
	answered := make(chan result, 1)
	go func() {
		res, err := a.client.Do(req)
		if err != nil {
			answered <- result{err: err}
			return
		}
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
		answered <- result{status: res.StatusCode}
	}()
	select {
	case <-got100:
	case r := <-answered:
		t.Fatalf("the upload was answered with %d (%v) before its handler read the body\nstderr:\n%s", r.status, r.err, a.stderr)
	case <-time.After(adminWait):
		t.Fatalf("the server has not asked for the body of the upload in %v", adminWait)
	}
	if _, err := pw.Write(file[:4096]); err != nil {
		t.Fatal(err)
	}
	a.cancel()
	// The server has not returned in this time, which is not a proof that it waits, but the upload would not end otherwise.
	select {
	case <-a.exited:
		t.Fatalf("whiteroom serve admin returned %d while an upload is in progress\nstderr:\n%s", a.code, a.stderr)
	case <-time.After(500 * time.Millisecond):
	}
	if _, err := pw.Write(file[4096:]); err != nil {
		t.Fatal(err)
	}
	if err := pw.Close(); err != nil {
		t.Fatal(err)
	}
	if r := <-answered; r.err != nil || r.status != http.StatusCreated {
		t.Errorf("the upload that was in progress when the server was stopped = %d (%v), want 201", r.status, r.err)
	}
	if code := a.stop(t); code != 0 {
		t.Errorf("whiteroom serve admin = %d after its context is done, want 0", code)
	}
	checkUnlocked(t, "after the stop with an upload in progress", s.data)

	// A build runs when the context ends.
	s = initSample(t)
	b := startAdmin(t, t.Context(), s.args()...)
	cookie = b.login(t)
	deadline := time.Now().Add(adminWait)
	for st := b.builds(t, cookie); len(st.Running) == 0 && st.Last == nil; st = b.builds(t, cookie) {
		if time.Now().After(deadline) {
			t.Fatalf("no build has started in %v\nstderr:\n%s", adminWait, b.stderr)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if code := b.stop(t); code != 0 {
		t.Errorf("whiteroom serve admin = %d after its context is done while a build runs, want 0\nstderr:\n%s", code, b.stderr)
	}
	if got := b.stderr.String(); got != "" {
		t.Errorf("whiteroom serve admin printed on stderr, after its context is done while a build runs:\n%s", got)
	}
	if !slices.Contains(activityActions(t, s.data), audit.Build) {
		t.Errorf("the activity log has no record of the build that was running when the server stopped")
	}
}

// adminHelp returns the usage that whiteroom serve admin -h prints, which the usage errors print too.
func adminHelp(t *testing.T) string {
	t.Helper()
	code, stdout, help := runCmd(t, "serve", "admin", "-h")
	if code != 0 || stdout != "" || !strings.HasPrefix(help, serveAdminUsage) {
		t.Fatalf("whiteroom serve admin -h = %d, with stdout %q and stderr:\n%s\nwant 0, and the usage on stderr", code, stdout, help)
	}
	return help
}

// notLoopback is what the commands tell of the address addr that is not a loopback IP address and a port.
func notLoopback(addr string) string {
	return strconv.Quote(addr) + " is not a loopback IP address and a port, such as 127.0.0.1:8081 or [::1]:8081"
}

// stopsWith checks that the server a returns 0 once its context is done.
func stopsWith(t *testing.T, what string, a *adminProc) {
	t.Helper()
	if code := a.stop(t); code != 0 {
		t.Errorf("%s: whiteroom serve admin = %d after its context is done, want 0", what, code)
	}
}

// socketMode checks that path is a socket file with the mode 0660.
func socketMode(t *testing.T, what, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Errorf("%s: %v", what, err)
		return
	}
	if info.Mode().Type() != os.ModeSocket || info.Mode().Perm() != 0o660 {
		t.Errorf("%s: %s has the mode %v, want a socket with 0660", what, path, info.Mode())
	}
}

// TestServeAdminSocket starts the server on a Unix domain socket: it answers on the socket, which has the mode 0660, and
// removes the file when it stops. A socket file that nobody listens on is replaced. Any other file, and a socket that
// another process listens on, is left as it is, with the command failing before it comes to the socket if it is not the
// first on the data directory. A path that a socket cannot have is a failure with the reason. None leaves the data
// directory locked.
func TestServeAdminSocket(t *testing.T) {
	s := initEmpty(t)
	origin := []string{"-origin", "http://localhost"}

	sock := socketPath(t)
	a := startAdmin(t, t.Context(), s.args(append([]string{"-socket", sock}, origin...)...)...)
	if want := "serving admin of " + s.data + " at http://localhost (listening on socket " + sock + ")\n"; a.line != want {
		t.Errorf("the server on a socket printed %q, want %q", a.line, want)
	}
	cookie := a.login(t)
	if built := a.waitBuilt(t, cookie); !failedBuild(built) {
		t.Errorf("the start build of a store without documents, on a socket, has %+v, want a failure", built.Last)
	}
	socketMode(t, "the socket of the server", sock)
	stopsWith(t, "the socket", a)
	noFile(t, "after the server has stopped", sock)
	checkUnlocked(t, "after the server on a socket has stopped", s.data)
	if got := a.stderr.String(); got != "" {
		t.Errorf("whiteroom serve admin on a socket printed on stderr:\n%s", got)
	}

	// A socket file that a killed server has left: no one listens on it.
	stale := socketPath(t)
	l, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(stale); err != nil || info.Mode().Type() != os.ModeSocket {
		t.Fatalf("the socket file that was left is %v (%v), want a socket", info, err)
	}
	b := startAdmin(t, t.Context(), s.args(append([]string{"-socket", stale}, origin...)...)...)
	b.login(t)
	socketMode(t, "the socket that replaced a stale one", stale)
	stopsWith(t, "a stale socket", b)
	noFile(t, "after the server on a stale socket has stopped", stale)

	// Files that the server leaves as they are, and says why: a socket that another process listens on, and what is not a
	// socket. The listener goes on to accept connections.
	live := socketPath(t)
	ll, err := net.Listen("unix", live)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ll.Close() }()
	liveInfo, err := os.Lstat(live)
	if err != nil {
		t.Fatal(err)
	}
	fail := func(what, path, reason string) {
		t.Helper()
		code, stdout, stderr := runAdminCmd(t, s.args(append([]string{"-socket", path}, origin...)...)...)
		if want := "whiteroom serve admin: listen unix " + path + ": " + reason + "\n"; code != 1 || stdout != "" || stderr != want {
			t.Errorf("%s: whiteroom serve admin = %d, with stdout %q and stderr:\n%s\nwant 1, with stderr:\n%s", what, code, stdout, stderr,
				want)
		}
		checkUnlocked(t, what, s.data)
	}
	fail("a socket that another process listens on", live, "another process listens on the socket, which is left as it is")
	if now, err := os.Lstat(live); err != nil || !os.SameFile(liveInfo, now) {
		t.Errorf("the socket that another process listens on is %v (%v) after the command, want the same file", now, err)
	}
	if conn, err := net.Dial("unix", live); err != nil {
		t.Errorf("the socket that another process listens on does not take a connection after the command: %v", err)
	} else {
		_ = conn.Close()
	}

	notSocket := "the path exists and is not a socket, and is left as it is"
	file := filepath.Join(socketDir(t), "s")
	if err := os.WriteFile(file, []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	fail("a regular file", file, notSocket)
	if got, err := os.ReadFile(file); err != nil || string(got) != "kept" {
		t.Errorf("the regular file is %q (%v) after the command, want it as it was", got, err)
	}
	dir := filepath.Join(socketDir(t), "s")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	fail("a directory", dir, notSocket)
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		t.Errorf("the directory is %v (%v) after the command, want it as it was", info, err)
	}
	for _, tt := range []struct{ what, target string }{
		{"a symbolic link to a socket that another process listens on", live},
		{"a symbolic link to nothing", filepath.Join(socketDir(t), "none")},
	} {
		link := filepath.Join(socketDir(t), "s")
		if err := os.Symlink(tt.target, link); err != nil {
			t.Fatal(err)
		}
		fail(tt.what, link, notSocket)
		if got, err := os.Readlink(link); err != nil || got != tt.target {
			t.Errorf("%s: the link is to %q (%v) after the command, want %q", tt.what, got, err, tt.target)
		}
	}
	if conn, err := net.Dial("unix", live); err != nil {
		t.Errorf("the socket that another process listens on does not take a connection after the commands: %v", err)
	} else {
		_ = conn.Close()
	}
	fail("a path that starts with @", "@s", "a path that starts with @ is of an abstract socket, which has no file: start the "+
		"path of a file with ./")

	// A socket file that cannot be tried for a listener, as one that the user may not write to cannot: it is not replaced.
	if os.Geteuid() != 0 {
		locked := socketPath(t)
		m, err := net.Listen("unix", locked)
		if err != nil {
			t.Fatal(err)
		}
		m.(*net.UnixListener).SetUnlinkOnClose(false)
		if err := m.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		fail("a socket file that cannot be written to", locked, "cannot tell whether another process listens on the socket, "+
			"which is left as it is: dial unix "+locked+": connect: permission denied")
		if info, err := os.Lstat(locked); err != nil || info.Mode().Type() != os.ModeSocket || info.Mode().Perm() != 0 {
			t.Errorf("the socket file that cannot be written to is %v (%v) after the command, want it as it was", info, err)
		}
	}

	// A socket that cannot be made: no directory to make it in, and a path that is too long.
	missing := filepath.Join(socketDir(t), "missing", "s")
	fail("no directory", missing, "bind: no such file or directory")
	noFile(t, "no directory", filepath.Dir(missing))
	long := socketDir(t)
	for _, n := range []int{maxSocketPath + 1, maxSocketPath + 20} {
		path := filepath.Join(long, strings.Repeat("x", n-len(long)-1))
		if len(path) != n {
			t.Fatalf("the path has %d bytes, want %d", len(path), n)
		}
		fail(fmt.Sprintf("a path of %d bytes", n), path, fmt.Sprintf("bind: invalid argument (the path of a socket has at most %d bytes, "+
			"and this one has %d)", maxSocketPath, n))
		noFile(t, "a path that is too long", path)
	}
}

// TestServeAdminListen checks where the server listens: the choice among -addr, -socket, addr and socket of the config
// file and the default, the addresses that it takes, which are those of the loopback interfaces only, and those that it
// refuses, all before the data directory is opened; that a flag wins over the config file, and that -addr and -socket are
// not both given; and that an address in use fails the command, which leaves the data directory open to others.
func TestServeAdminListen(t *testing.T) {
	for _, tt := range []struct {
		what               string
		addr               string
		addrGiven          bool
		socket             string
		socketGiven        bool
		cfgAddr, cfgSocket string
		want               listenChoice
		err                error
	}{
		{what: "none", want: listenChoice{network: "tcp", address: "127.0.0.1:8081"}},
		{what: "-addr", addr: "127.0.0.1:9", addrGiven: true, want: listenChoice{network: "tcp", address: "127.0.0.1:9"}},
		{what: "addr of the config file", cfgAddr: "127.0.0.1:7",
			want: listenChoice{network: "tcp", address: "127.0.0.1:7", fromConfig: true}},
		{what: "-addr and addr of the config file", addr: "127.0.0.1:9", addrGiven: true, cfgAddr: "127.0.0.1:7",
			want: listenChoice{network: "tcp", address: "127.0.0.1:9"}},
		{what: "-addr with an empty value", addrGiven: true, cfgAddr: "127.0.0.1:7", want: listenChoice{network: "tcp"}},
		{what: "-socket", socket: "/run/a.sock", socketGiven: true, want: listenChoice{network: "unix", address: "/run/a.sock"}},
		{what: "-socket with an empty value", socketGiven: true, cfgSocket: "/run/c.sock", want: listenChoice{network: "unix"}},
		{what: "socket of the config file", cfgSocket: "/run/c.sock",
			want: listenChoice{network: "unix", address: "/run/c.sock", fromConfig: true}},
		{what: "-socket and socket of the config file", socket: "/run/a.sock", socketGiven: true, cfgSocket: "/run/c.sock",
			want: listenChoice{network: "unix", address: "/run/a.sock"}},
		{what: "-addr and -socket", addr: "127.0.0.1:9", addrGiven: true, socket: "/run/a.sock", socketGiven: true,
			cfgAddr: "127.0.0.1:7", err: errBothFlags},
		{what: "-addr and socket of the config file", addr: "127.0.0.1:9", addrGiven: true, cfgSocket: "/run/c.sock",
			want: listenChoice{network: "tcp", address: "127.0.0.1:9"}},
		{what: "-socket and addr of the config file", socket: "/run/a.sock", socketGiven: true, cfgAddr: "127.0.0.1:7",
			want: listenChoice{network: "unix", address: "/run/a.sock"}},
		{what: "-addr and both of the config file", addr: "127.0.0.1:9", addrGiven: true, cfgAddr: "127.0.0.1:7",
			cfgSocket: "/run/c.sock", want: listenChoice{network: "tcp", address: "127.0.0.1:9"}},
		{what: "-socket and both of the config file", socket: "/run/a.sock", socketGiven: true, cfgAddr: "127.0.0.1:7",
			cfgSocket: "/run/c.sock", want: listenChoice{network: "unix", address: "/run/a.sock"}},
		{what: "both of the config file", cfgAddr: "127.0.0.1:7", cfgSocket: "/run/c.sock", err: errBothConfig},
	} {
		got, err := chooseAdminListen(tt.addr, tt.addrGiven, tt.socket, tt.socketGiven, tt.cfgAddr, tt.cfgSocket)
		if got != tt.want || !errors.Is(err, tt.err) {
			t.Errorf("%s: chooseAdminListen(%q, %t, %q, %t, %q, %q) = %+v, %v, want %+v, %v", tt.what, tt.addr, tt.addrGiven,
				tt.socket, tt.socketGiven, tt.cfgAddr, tt.cfgSocket, got, err, tt.want, tt.err)
		}
	}
	for _, addr := range []string{
		"127.0.0.1:8081", "127.0.0.1:0", "127.9.9.9:1", "127.0.0.1:65535", "[::1]:8081", "127.255.255.255:80", "[::1]:0",
		"[0:0:0:0:0:0:0:1]:8081",
	} {
		if err := checkAdminAddr(addr); err != nil {
			t.Errorf("checkAdminAddr(%q) = %v, want nil", addr, err)
		}
	}
	for _, addr := range []string{
		"localhost:8081", ":8081", "0.0.0.0:8081", "[::]:8081", "192.0.2.1:8081", "[::ffff:127.0.0.1]:8081", "[::1%lo]:8081",
		"127.1:8081", "127.0.0.1", "127.0.0.1:", "127.0.0.1:http", "127.0.0.1:65536", "127.0.0.1:-1", "127.0.0.1:+80",
		"unix:/run/x.sock", "", "[127.0.0.1]:8081", "::1:8081", "[::1]", "[::1]:", "126.255.255.255:80", "128.0.0.1:80",
		"[::2]:80", "[::ffff:7f00:1]:80", " 127.0.0.1:8081", "127.0.0.1:8081 ", "127.0.0.1:8_1", "localhost.:8081",
		"127.0.0.1:8081:1",
	} {
		if err := checkAdminAddr(addr); err == nil || err.Error() != notLoopback(addr) {
			t.Errorf("checkAdminAddr(%q) = %v, want the error %q", addr, err, notLoopback(addr))
		}
	}

	s := initEmpty(t)
	help := adminHelp(t)
	for _, addr := range []string{"0.0.0.0:0", "localhost:0"} {
		code, stdout, stderr := runAdminCmd(t, s.args("-addr", addr)...)
		if want := "whiteroom serve admin: -addr " + notLoopback(addr) + "\n" + help; code != 2 || stdout != "" || stderr != want {
			t.Errorf("-addr %s = %d, with stdout %q and stderr:\n%s\nwant 2, with stderr:\n%s", addr, code, stdout, stderr, want)
		}
		checkUnlocked(t, "-addr "+addr, s.data)
	}
	file := s.config(t, "addr: 0.0.0.0:8081")
	code, stdout, stderr := runAdminCmd(t, "-config", file)
	if want := file + ": /addr: " + notLoopback("0.0.0.0:8081") + "\n"; code != 2 || stdout != "" || stderr != want {
		t.Errorf("addr of the config file = %d, with stdout %q and stderr:\n%s\nwant 2, with stderr:\n%s", code, stdout, stderr, want)
	}
	checkUnlocked(t, "addr of the config file", s.data)

	// The address of the config file only.
	a := startAdminArgs(t, t.Context(), "-config", s.config(t, "addr: 127.0.0.1:0"))
	if host, port, err := net.SplitHostPort(a.addr); err != nil || host != "127.0.0.1" || port == "0" || port == "8081" {
		t.Errorf("addr: 127.0.0.1:0 of the config file: the server listens on %q, want a port of the system's choice", a.addr)
	}
	stopsWith(t, "addr of the config file", a)

	// An address in use: -addr wins over the config file, whose address is the one in use; and in use as -addr or as addr.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()
	inUse := busy.Addr().String()
	b := startAdminArgs(t, t.Context(), "-config", s.config(t, "addr: "+inUse), "-addr", "127.0.0.1:0")
	if b.addr == inUse {
		t.Errorf("the server listens on %s, the address of the config file, though -addr is given", b.addr)
	}
	stopsWith(t, "-addr over addr", b)
	want := "whiteroom serve admin: listen tcp " + inUse + ": bind: address already in use\n"
	for _, tt := range []struct {
		what string
		args []string
	}{
		{"-addr", s.args("-addr", inUse)},
		{"addr of the config file", []string{"-config", s.config(t, "addr: "+inUse)}},
	} {
		if code, stdout, stderr := runAdminCmd(t, tt.args...); code != 1 || stdout != "" || stderr != want {
			t.Errorf("%s of an address in use = %d, with stdout %q and stderr:\n%s\nwant 1, with stderr:\n%s", tt.what, code, stdout,
				stderr, want)
		}
		checkUnlocked(t, tt.what+" of an address in use", s.data)
	}

	// -addr and -socket are one or the other: both are refused, on the command line and in the config file, before the data
	// directory is opened, and no socket file is made.
	sock := socketPath(t)
	code, stdout, stderr = runAdminCmd(t, s.args("-addr", "127.0.0.1:0", "-socket", sock, "-origin", "http://localhost")...)
	both := "-addr and -socket are both given: give one of them, as the server listens on one"
	if want := "whiteroom serve admin: " + both + "\n" + help; code != 2 || stdout != "" || stderr != want {
		t.Errorf("-addr and -socket = %d, with stdout %q and stderr:\n%s\nwant 2, with stderr:\n%s", code, stdout, stderr, want)
	}
	checkUnlocked(t, "-addr and -socket", s.data)
	noFile(t, "-addr and -socket", sock)
	file = s.config(t, "addr: 127.0.0.1:0", "socket: "+strconv.Quote(sock), "origin: http://localhost")
	code, stdout, stderr = runAdminCmd(t, "-config", file)
	if want := file + ": addr and socket are both given: give one of them, as the server listens on one\n"; code != 2 ||
		stdout != "" || stderr != want {
		t.Errorf("addr and socket of the config file = %d, with stdout %q and stderr:\n%s\nwant 2, with stderr:\n%s", code, stdout,
			stderr, want)
	}
	checkUnlocked(t, "addr and socket of the config file", s.data)
	noFile(t, "addr and socket of the config file", sock)
	code, stdout, stderr = runAdminCmd(t, s.args("-socket", "", "-origin", "http://localhost")...)
	if want := "whiteroom serve admin: -socket must not be empty\n" + help; code != 2 || stdout != "" || stderr != want {
		t.Errorf("-socket \"\" = %d, with stdout %q and stderr:\n%s\nwant 2, with stderr:\n%s", code, stdout, stderr, want)
	}
	checkUnlocked(t, "-socket with an empty value", s.data)

	// The flag wins over the other item of the config file: -socket over addr, and -addr over socket.
	c := startAdminArgs(t, t.Context(), "-config", s.config(t, "addr: 127.0.0.1:0", "origin: http://localhost"), "-socket", sock)
	if c.network != "unix" || c.addr != sock {
		t.Errorf("-socket with addr of the config file: the server listens on the %s %q, want the socket %s", c.network, c.addr, sock)
	}
	stopsWith(t, "-socket over addr", c)
	noFile(t, "-socket over addr, after the stop", sock)
	other := socketPath(t)
	d := startAdminArgs(t, t.Context(), "-config", s.config(t, "socket: "+strconv.Quote(other), "origin: http://localhost"),
		"-addr", "127.0.0.1:0")
	if _, port, err := net.SplitHostPort(d.addr); d.network != "tcp" || err != nil || port == "0" {
		t.Errorf("-addr with socket of the config file: the server listens on the %s %q, want a TCP port", d.network, d.addr)
	}
	noFile(t, "-addr over socket", other)
	stopsWith(t, "-addr over socket", d)
}

// noFile checks that nothing is at path.
func noFile(t *testing.T, what, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err == nil {
		t.Errorf("%s: %s exists", what, path)
	}
}

// TestServeAdminOrigin checks the origin that the server answers for: that of the address that it listens on, if none is
// given, also for the system's choice of the port 0; and the one of -origin, of which only the requests with its Host and
// its Origin are served. An origin that the server refuses ends the command, which frees what it opened.
func TestServeAdminOrigin(t *testing.T) {
	for _, tt := range []struct{ addr, want string }{
		{"127.0.0.1:8081", "http://127.0.0.1:8081"},
		{"[::1]:8081", "http://[::1]:8081"},
		{"127.9.9.9:80", "http://127.9.9.9:80"},
		{"127.0.0.1:41234", "http://127.0.0.1:41234"},
	} {
		if got := listenOrigin(netip.MustParseAddrPort(tt.addr)); got != tt.want {
			t.Errorf("listenOrigin(%s) = %q, want %q", tt.addr, got, tt.want)
		}
	}
	s := initEmpty(t)
	status := func(a *adminProc, headers ...string) int {
		t.Helper()
		code, _ := a.exchange(t, http.MethodGet, "/api/session", "", headers...)
		return code
	}

	// No -origin: that of the address, which the Host of a request must be.
	a := startAdmin(t, t.Context(), s.args()...)
	_, port, err := net.SplitHostPort(a.addr)
	if err != nil || a.origin != "http://127.0.0.1:"+port || port == "0" {
		t.Fatalf("the server listens on %q and tells the origin %q, want that of a port of 127.0.0.1", a.addr, a.origin)
	}
	for host, want := range map[string]int{
		"127.0.0.1:" + port: http.StatusUnauthorized, "localhost:" + port: http.StatusMisdirectedRequest,
		"127.0.0.1": http.StatusMisdirectedRequest, "[::1]:" + port: http.StatusMisdirectedRequest,
	} {
		if got := status(a, "Host", host); got != want {
			t.Errorf("GET /api/session with the Host %s = %d, want %d", host, got, want)
		}
	}
	stopsWith(t, "no -origin", a)

	// An address of ::1, on a machine that has it.
	if l, err := net.Listen("tcp", "[::1]:0"); err != nil {
		t.Logf("no ::1 to listen on: %v", err)
	} else {
		_ = l.Close()
		b := startAdmin(t, t.Context(), s.args("-addr", "[::1]:0")...)
		_, port, err := net.SplitHostPort(b.addr)
		if err != nil || b.origin != "http://[::1]:"+port || !strings.HasSuffix(b.line, " (listening on [::1]:"+port+")\n") {
			t.Errorf("the server on [::1]:0 printed %q, want the origin http://[::1]:PORT and the address [::1]:PORT", b.line)
		}
		if got := status(b); got != http.StatusUnauthorized {
			t.Errorf("GET /api/session on [::1] = %d, want 401", got)
		}
		stopsWith(t, "[::1]:0", b)
	}

	// -origin http://localhost:9, whatever address it listens on.
	c := startAdmin(t, t.Context(), s.args("-origin", "http://localhost:9")...)
	_, port, _ = net.SplitHostPort(c.addr)
	if c.origin != "http://localhost:9" || !strings.Contains(c.line, " at http://localhost:9 (listening on 127.0.0.1:"+port+")\n") {
		t.Errorf("the server with -origin http://localhost:9 printed %q", c.line)
	}
	for host, want := range map[string]int{
		"localhost:9": http.StatusUnauthorized, "127.0.0.1:" + port: http.StatusMisdirectedRequest, "localhost": http.StatusMisdirectedRequest,
	} {
		if got := status(c, "Host", host); got != want {
			t.Errorf("GET /api/session with the Host %s, at -origin http://localhost:9 = %d, want %d", host, got, want)
		}
	}
	stopsWith(t, "-origin http://localhost:9", c)

	// -origin https://admin.example: the URL is http, as there is no TLS, and its Host and its Origin are those of the origin.
	d := startAdmin(t, t.Context(), s.args("-origin", "https://admin.example", "-trusted-proxy", "127.0.0.1")...)
	if d.origin != "https://admin.example" || d.base != "http://admin.example" {
		t.Errorf("the server with -origin https://admin.example tells the origin %q", d.origin)
	}
	login := func(origin string) int {
		t.Helper()
		got, _ := d.exchange(t, http.MethodPost, "/api/session", `{"login":"x","password":"y"}`, "Origin", origin)
		return got
	}
	if got := login("https://admin.example"); got != http.StatusUnauthorized {
		t.Errorf("a login with the Origin of the server = %d, want 401, as the login is wrong", got)
	}
	for _, origin := range []string{"http://admin.example", "https://other.example", "https://admin.example:8443"} {
		if got := login(origin); got != http.StatusForbidden {
			t.Errorf("a login with the Origin %s = %d, want 403", origin, got)
		}
	}
	if got := status(d, "Host", "other.example"); got != http.StatusMisdirectedRequest {
		t.Errorf("GET /api/session with the Host other.example = %d, want 421", got)
	}
	stopsWith(t, "-origin https://admin.example", d)

	// An origin that the server refuses: the command fails, and the address and the data directory are free again.
	addr := freePort(t)
	code, stdout, stderr := runAdminCmd(t, s.args("-addr", addr, "-origin", "http://example.com")...)
	want := "whiteroom serve admin: admin: invalid origin \"http://example.com\": use https, or http on localhost\n"
	if code != 1 || stdout != "" || stderr != want {
		t.Errorf("-origin http://example.com = %d, with stdout %q and stderr:\n%s\nwant 1, with stderr:\n%s", code, stdout, stderr, want)
	}
	e := startAdmin(t, t.Context(), s.args("-addr", addr, "-origin", "http://localhost:9")...)
	if e.addr != addr {
		t.Errorf("the server listens on %s, want %s, which the command has freed", e.addr, addr)
	}
	stopsWith(t, "the address that a failed command has freed", e)

	// A socket: no origin is made of an address, so it is given, and the requests have its Host and its Origin.
	sock := socketPath(t)
	code, stdout, stderr = runAdminCmd(t, s.args("-socket", sock)...)
	want = "whiteroom serve admin: no origin: a socket has no address that the origin can be made of: give -origin URL, or " +
		"origin in the config file\n" + adminHelp(t)
	if code != 2 || stdout != "" || stderr != want {
		t.Errorf("-socket without -origin = %d, with stdout %q and stderr:\n%s\nwant 2, with stderr:\n%s", code, stdout, stderr, want)
	}
	checkUnlocked(t, "-socket without -origin", s.data)
	noFile(t, "-socket without -origin", sock)

	f := startAdmin(t, t.Context(), s.args("-socket", sock, "-origin", "http://localhost")...)
	if want := "serving admin of " + s.data + " at http://localhost (listening on socket " + sock + ")\n"; f.line != want ||
		f.network != "unix" {
		t.Errorf("the server on a socket printed %q, want %q", f.line, want)
	}
	for host, want := range map[string]int{
		"localhost": http.StatusUnauthorized, "localhost:80": http.StatusUnauthorized, "127.0.0.1": http.StatusMisdirectedRequest,
		"localhost:8081": http.StatusMisdirectedRequest, "other.example": http.StatusMisdirectedRequest,
	} {
		if got := status(f, "Host", host); got != want {
			t.Errorf("GET /api/session with the Host %s, on a socket at -origin http://localhost = %d, want %d", host, got, want)
		}
	}
	for _, origin := range []string{"http://127.0.0.1", "https://localhost", "http://localhost:8081"} {
		if got, _ := f.exchange(t, http.MethodPost, "/api/session", `{"login":"x","password":"y"}`, "Origin", origin); got != http.StatusForbidden {
			t.Errorf("a login on a socket with the Origin %s = %d, want 403", origin, got)
		}
	}
	if got, _ := f.exchange(t, http.MethodPost, "/api/session", `{"login":"x","password":"y"}`); got != http.StatusUnauthorized {
		t.Errorf("a login on a socket with the Origin of the server = %d, want 401, as the login is wrong", got)
	}
	stopsWith(t, "a socket and -origin http://localhost", f)

	g := startAdmin(t, t.Context(), s.args("-socket", socketPath(t), "-origin", "https://admin.example")...)
	if g.origin != "https://admin.example" || g.base != "http://admin.example" || g.network != "unix" {
		t.Errorf("the server on a socket with -origin https://admin.example tells the origin %q", g.origin)
	}
	loginOn := func(origin string) int {
		t.Helper()
		got, _ := g.exchange(t, http.MethodPost, "/api/session", `{"login":"x","password":"y"}`, "Origin", origin)
		return got
	}
	if got := loginOn("https://admin.example"); got != http.StatusUnauthorized {
		t.Errorf("a login on a socket with the Origin of the server = %d, want 401, as the login is wrong", got)
	}
	for _, origin := range []string{"http://admin.example", "https://other.example", "https://admin.example:8443"} {
		if got := loginOn(origin); got != http.StatusForbidden {
			t.Errorf("a login on a socket with the Origin %s = %d, want 403", origin, got)
		}
	}
	if got := status(g, "Host", "other.example"); got != http.StatusMisdirectedRequest {
		t.Errorf("GET /api/session on a socket with the Host other.example = %d, want 421", got)
	}
	stopsWith(t, "a socket and -origin https://admin.example", g)

	// An origin that the server refuses: the command fails, and the socket file is not left, so that the same arguments
	// with the origin made right start.
	bad := socketPath(t)
	code, stdout, stderr = runAdminCmd(t, s.args("-socket", bad, "-origin", "http://example.com")...)
	want = "whiteroom serve admin: admin: invalid origin \"http://example.com\": use https, or http on localhost\n"
	if code != 1 || stdout != "" || stderr != want {
		t.Errorf("a socket and -origin http://example.com = %d, with stdout %q and stderr:\n%s\nwant 1, with stderr:\n%s", code, stdout,
			stderr, want)
	}
	noFile(t, "a socket and an origin that is refused", bad)
	checkUnlocked(t, "a socket and an origin that is refused", s.data)
	h := startAdmin(t, t.Context(), s.args("-socket", bad, "-origin", "http://localhost")...)
	if h.addr != bad {
		t.Errorf("the server listens on %s, want %s, which the command has freed", h.addr, bad)
	}
	stopsWith(t, "the socket that a failed command has freed", h)
}

// TestServeAdminWarning checks the warning that the origin is https while no proxy is trusted.
func TestServeAdminWarning(t *testing.T) {
	proxy := []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	for _, tt := range []struct {
		network, origin string
		proxies         []netip.Prefix
		want            bool
	}{
		{"tcp", "https://admin.example", nil, true},
		{"tcp", "HTTPS://admin.example", nil, true},
		{"tcp", "https://admin.example", proxy, false},
		{"tcp", "http://localhost:8081", nil, false},
		{"tcp", "http://localhost:8081", proxy, false},
		{"tcp", "", nil, false},
		{"unix", "https://admin.example", nil, false},
		{"unix", "https://admin.example", proxy, false},
		{"unix", "http://localhost", nil, false},
	} {
		if got := warnNoProxy(tt.network, tt.origin, tt.proxies); got != tt.want {
			t.Errorf("warnNoProxy(%q, %q, %v) = %t, want %t", tt.network, tt.origin, tt.proxies, got, tt.want)
		}
	}

	for _, hint := range []string{"-trusted-proxy", "trustedProxies"} {
		if !strings.Contains(noProxyWarning, hint) {
			t.Errorf("the warning does not name %s: %s", hint, noProxyWarning)
		}
	}

	s := initEmpty(t)
	for _, tt := range []struct {
		what string
		args []string
		want bool
	}{
		{"an https origin", s.args("-origin", "https://admin.example"), true},
		{"an https origin and -trusted-proxy", s.args("-origin", "https://admin.example", "-trusted-proxy", "127.0.0.1"), false},
		{"an https origin and trustedProxies", []string{"-config", s.config(t, "origin: https://admin.example",
			`trustedProxies: ["127.0.0.1"]`, "addr: 127.0.0.1:0")}, false},
		{"an https origin of the config file", []string{"-config", s.config(t, "origin: https://admin.example",
			"addr: 127.0.0.1:0")}, true},
		{"no origin", s.args(), false},
		{"an http origin", s.args("-origin", "http://localhost:9"), false},
		{"a socket and an https origin", s.args("-socket", socketPath(t), "-origin", "https://admin.example"), false},
		{"a socket of the config file and an https origin", []string{"-config", s.configIn(t, socketDir(t),
			"socket: s", "origin: https://admin.example")}, false},
		{"a socket and an http origin", s.args("-socket", socketPath(t), "-origin", "http://localhost"), false},
	} {
		var a *adminProc
		if slices.Contains(tt.args, "-config") {
			a = startAdminArgs(t, t.Context(), tt.args...)
		} else {
			a = startAdmin(t, t.Context(), tt.args...)
		}
		if got := strings.Contains(a.stderr.String(), noProxyWarning); got != tt.want {
			t.Errorf("%s: the warning is on stderr: %t, want %t; stderr:\n%s", tt.what, got, tt.want, a.stderr)
		}
		if got, want := strings.Count(a.stderr.String(), "warning"), map[bool]int{true: 1, false: 0}[tt.want]; got != want {
			t.Errorf("%s: stderr has %d lines with warning, want %d:\n%s", tt.what, got, want, a.stderr)
		}
		stopsWith(t, tt.what, a)
	}
}

// TestServeAdminConfig starts the server from a config file only, whose paths are relative to its directory: the data
// directory, the site directory without a theme, the theme, the output and the cache directories, the address and the
// origin. The flags win over it, and its trusted proxies are used unless -trusted-proxy is given. Those that cannot be read,
// and that cover every address, are refused.
func TestServeAdminConfig(t *testing.T) {
	s := initSample(t)
	if err := os.RemoveAll(filepath.Join(s.site, "theme")); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	rel := func(p string) string {
		t.Helper()
		abs, err := filepath.Abs(p)
		if err != nil {
			t.Fatal(err)
		}
		r, err := filepath.Rel(dir, abs)
		if err != nil {
			t.Fatal(err)
		}
		return strconv.Quote(r)
	}
	file := writeConfig(t, dir, "whiteroom.yaml", strings.Join([]string{
		"data: " + rel(s.data), "site: " + rel(s.site), "theme: " + rel(exampleTheme), "addr: 127.0.0.1:0",
		"origin: http://localhost:9", "out: out", "cache: cache",
	}, "\n")+"\n")
	a := startAdminArgs(t, t.Context(), "-config", file)
	if _, port, err := net.SplitHostPort(a.addr); err != nil || port == "0" || port == "8081" || a.origin != "http://localhost:9" {
		t.Errorf("the server of the config file printed %q, want the origin http://localhost:9, on a port of the system's choice",
			a.line)
	}
	cookie := a.login(t)
	if built := a.waitBuilt(t, cookie); failedBuild(built) || currentOf(t, filepath.Join(dir, "out")) != built.Last.Release {
		t.Errorf("the start build of the config file is %+v, and out/ of the config file has %s", built.Last,
			currentOf(t, filepath.Join(dir, "out")))
	}
	stopsWith(t, "the config file", a)
	if _, err := os.Stat(filepath.Join(dir, "cache", "CACHEDIR.TAG")); err != nil {
		t.Errorf("the cache directory of the config file: %v", err)
	}

	// The flags win: -origin and -out.
	other := filepath.Join(t.TempDir(), "other")
	b := startAdminArgs(t, t.Context(), "-config", file, "-origin", "http://localhost:10", "-out", other)
	if b.origin != "http://localhost:10" {
		t.Errorf("the server with -origin http://localhost:10 tells the origin %q", b.origin)
	}
	cookie = b.login(t)
	if built := b.waitBuilt(t, cookie); failedBuild(built) || currentOf(t, other) != built.Last.Release {
		t.Errorf("the start build with -out is %+v, and %s has %s", built.Last, other, currentOf(t, other))
	}
	stopsWith(t, "the flags over the config file", b)

	// A socket in place of the address: its path is relative to the directory of the file, and -socket wins over it.
	e := initEmpty(t)
	sdir := socketDir(t)
	viaSocket := e.configIn(t, sdir, "socket: s", "origin: http://localhost")
	f := startAdminArgs(t, t.Context(), "-config", viaSocket)
	if f.network != "unix" || f.addr != filepath.Join(sdir, "s") || f.origin != "http://localhost" {
		t.Errorf("the server of the config file with socket printed %q, want the socket %s and the origin http://localhost", f.line,
			filepath.Join(sdir, "s"))
	}
	f.login(t)
	stopsWith(t, "the socket of the config file", f)
	noFile(t, "the socket of the config file, after the stop", filepath.Join(sdir, "s"))
	over := socketPath(t)
	g := startAdminArgs(t, t.Context(), "-config", viaSocket, "-socket", over)
	if g.network != "unix" || g.addr != over {
		t.Errorf("the server with -socket over the config file listens on the %s %q, want the socket %s", g.network, g.addr, over)
	}
	g.login(t)
	noFile(t, "-socket over the socket of the config file", filepath.Join(sdir, "s"))
	stopsWith(t, "-socket over the socket of the config file", g)

	// The trusted proxies of the file, and -trusted-proxy over them, as the client of the login tells.
	proxies := e.config(t, "addr: 127.0.0.1:0", "trustedProxies:", "  - 127.0.0.1")
	for _, tt := range []struct {
		what string
		args []string
		want string
	}{
		{"trustedProxies", []string{"-config", proxies}, "203.0.113.9"},
		{"-trusted-proxy over trustedProxies", []string{"-config", proxies, "-trusted-proxy", "10.0.0.0/8"}, "127.0.0.1"},
	} {
		c := startAdminArgs(t, t.Context(), tt.args...)
		c.login(t, "X-Forwarded-For", "203.0.113.9")
		stopsWith(t, tt.what, c)
		clients := loginClients(t, e.data)
		if got := clients[len(clients)-1]; got != tt.want {
			t.Errorf("%s: the client of the login is %s, want %s", tt.what, got, tt.want)
		}
	}

	// The trusted proxies of the file that are refused, each with its place in the file.
	for _, tt := range []struct {
		what  string
		lines []string
		want  []string
	}{
		{"a name", []string{`  - "x"`}, []string{`/trustedProxies/0: "x": not an IP address or a CIDR range`}},
		{"a range of every address", []string{`  - 0.0.0.0/0`}, []string{`/trustedProxies/0: "0.0.0.0/0": covers every address`}},
		{"an IPv6 range of every address", []string{`  - "::/0"`}, []string{`/trustedProxies/0: "::/0": covers every address`}},
		{"a bad one after a good one", []string{`  - 10.0.0.0/8`, `  - 300.0.0.1`},
			[]string{`/trustedProxies/1: "300.0.0.1": not an IP address or a CIDR range`}},
		{"two bad ones", []string{`  - 10.0.0.0/33`, `  - 10.0.0.1`, `  - 0.0.0.0/0`}, []string{
			`/trustedProxies/0: "10.0.0.0/33": not an IP address or a CIDR range`, `/trustedProxies/2: "0.0.0.0/0": covers every address`,
		}},
	} {
		cfg := e.config(t, append([]string{"trustedProxies:"}, tt.lines...)...)
		code, stdout, stderr := runAdminCmd(t, "-config", cfg)
		want := ""
		for _, l := range tt.want {
			want += cfg + ": " + l + "\n"
		}
		if code != 2 || stdout != "" || stderr != want {
			t.Errorf("%s: whiteroom serve admin = %d, with stdout %q and stderr:\n%s\nwant 2, with stderr:\n%s", tt.what, code, stdout,
				stderr, want)
		}
		checkUnlocked(t, tt.what, e.data)
	}
}

// TestServeAdminClient checks the client address that the login history gets: that of the connection, unless the
// connection is that of a trusted proxy, and then the address that X-Forwarded-For has, if it has one.
func TestServeAdminClient(t *testing.T) {
	s := initEmpty(t)
	onSocket := func(more ...string) []string {
		return append([]string{"-socket", socketPath(t), "-origin", "http://localhost"}, more...)
	}
	for i, tt := range []struct {
		what string
		args []string
		xff  string
		want string
	}{
		{"no proxy, with the header", nil, "203.0.113.9", "127.0.0.1"},
		{"no proxy, without the header", nil, "", "127.0.0.1"},
		{"the proxy, with the header", []string{"-trusted-proxy", "127.0.0.1"}, "203.0.113.9", "203.0.113.9"},
		{"the proxy, without the header", []string{"-trusted-proxy", "127.0.0.1"}, "", "127.0.0.1"},
		{"a range of the proxy", []string{"-trusted-proxy", "127.0.0.0/8"}, "203.0.113.9", "203.0.113.9"},
		{"another proxy", []string{"-trusted-proxy", "10.0.0.1"}, "203.0.113.9", "127.0.0.1"},
		{"another address of the same range", []string{"-trusted-proxy", "127.0.0.2"}, "203.0.113.9", "127.0.0.1"},
		{"two proxies", []string{"-trusted-proxy", "10.0.0.1", "-trusted-proxy", "127.0.0.1"}, "203.0.113.9", "203.0.113.9"},
		{"a proxy in the header", []string{"-trusted-proxy", "127.0.0.1", "-trusted-proxy", "198.51.100.0/24"},
			"203.0.113.9, 198.51.100.7", "203.0.113.9"},
		// The peer of a socket is a proxy always: the client is in the header, or none can be told.
		{"a socket, with the header", onSocket(), "203.0.113.9", "203.0.113.9"},
		{"a socket, with a header of two addresses", onSocket(), "203.0.113.9, 10.0.0.5", "10.0.0.5"},
		{"a socket, with a header of two addresses and a proxy before it", onSocket("-trusted-proxy", "10.0.0.0/8"),
			"203.0.113.9, 10.0.0.5", "203.0.113.9"},
		{"a socket, without the header", onSocket(), "", "::"},
	} {
		a := startAdmin(t, t.Context(), s.args(tt.args...)...)
		var headers []string
		if tt.xff != "" {
			headers = []string{"X-Forwarded-For", tt.xff}
		}
		a.login(t, headers...)
		stopsWith(t, tt.what, a)
		clients := loginClients(t, s.data)
		if len(clients) != i+1 || clients[i] != tt.want {
			t.Errorf("%s: the clients of the logins are %v, want the last to be %s", tt.what, clients, tt.want)
		}
	}
}

// TestServeAdminErrors checks the commands that fail: with a usage error, 2, and without opening the data directory; and
// with 1, for a data directory, a site directory, a theme and a second server that cannot be used, each with its message.
// None leaves the data directory open.
func TestServeAdminErrors(t *testing.T) {
	s := initEmpty(t)
	help := adminHelp(t)
	usage := func(msg string) string { return "whiteroom serve admin: " + msg + "\n" + help }
	tooMany := usage("too many arguments: give at most one SITE, after the flags")
	memory := func(m string) string { return usage("-memory " + m + " is out of range (0 to 8796093022207)") }
	badProxy := func(v, why string) string {
		return "invalid value " + strconv.Quote(v) + " for flag -trusted-proxy: " + why + "\n" + help
	}
	notAddress := "not an IP address or a CIDR range"
	for _, tt := range []struct {
		what   string
		args   []string
		stderr string
	}{
		{"no data directory", []string{"-theme", exampleTheme, s.site},
			usage("no data directory: give -data DIR, or data in the config file")},
		{"two arguments", s.args("x"), tooMany},
		{"two arguments, one after a flag", []string{"-data", s.data, "a", "-out", "b"}, tooMany},
		{"workers below 0", s.args("-workers", "-1"), usage("-workers -1 is out of range (0 or more)")},
		{"memory below 0", s.args("-memory", "-1"), memory("-1")},
		{"memory above the limit", s.args("-memory", "8796093022208"), memory("8796093022208")},
		{"a flag that is not defined", s.args("-nope"), "flag provided but not defined: -nope\n" + help},
		{"a proxy that is a name", s.args("-trusted-proxy", "proxy.example"), badProxy("proxy.example", notAddress)},
		{"a proxy that is an empty text", s.args("-trusted-proxy", ""), badProxy("", notAddress)},
		{"a range that is too long", s.args("-trusted-proxy", "10.0.0.0/33"), badProxy("10.0.0.0/33", notAddress)},
		{"a range of every IPv4 address", s.args("-trusted-proxy", "0.0.0.0/0"), badProxy("0.0.0.0/0", "covers every address")},
		{"a range of every IPv6 address", s.args("-trusted-proxy", "::/0"), badProxy("::/0", "covers every address")},
		{"a good proxy and a range of every address", s.args("-trusted-proxy", "127.0.0.1", "-trusted-proxy", "10.0.0.0/0"),
			badProxy("10.0.0.0/0", "covers every address")},
	} {
		code, stdout, stderr := runAdminCmd(t, tt.args...)
		if code != 2 || stdout != "" || stderr != tt.stderr {
			t.Errorf("%s: whiteroom serve admin %s = %d, with stdout %q and stderr:\n%s\nwant 2, with stderr:\n%s", tt.what,
				strings.Join(tt.args, " "), code, stdout, stderr, tt.stderr)
		}
		checkUnlocked(t, tt.what, s.data)
	}

	// What fails when it opens what it needs, in the order that it opens it.
	missing := filepath.Join(t.TempDir(), "missing")
	_, dataErr := store.Open(missing)
	notStore := t.TempDir()
	_, notStoreErr := store.Open(notStore)
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, fileErr := store.Open(file)
	_, rootErr := os.OpenRoot(missing)
	if dataErr == nil || notStoreErr == nil || fileErr == nil || rootErr == nil {
		t.Fatalf("the errors of the missing things are %v, %v, %v and %v", dataErr, notStoreErr, fileErr, rootErr)
	}
	noSchema := t.TempDir()
	noTheme := t.TempDir()
	if err := os.MkdirAll(filepath.Join(noTheme, "site"), 0o755); err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile(filepath.Join(s.site, "site", "schema.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noTheme, "site", "schema.yaml"), schema, 0o644); err != nil {
		t.Fatal(err)
	}
	brokenTheme := t.TempDir()
	if err := os.WriteFile(filepath.Join(brokenTheme, "theme.yaml"), []byte("schemaVersion: 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Each fails the same on a TCP address and on a socket, and leaves no socket file.
	sockFail := socketPath(t)
	for _, listen := range [][]string{{"-addr", "127.0.0.1:0"}, {"-socket", sockFail, "-origin", "http://localhost"}} {
		with := func(data, site string, flags ...string) []string {
			args := append(slices.Clone(listen), "-data", data, "-out", s.out, "-cache", s.cache)
			return append(append(args, flags...), site)
		}
		for _, tt := range []struct {
			what   string
			args   []string
			stderr string // the whole of it, or "" for the start of it that is prefix
			prefix string
		}{
			{"a missing data directory", with(missing, s.site, "-theme", exampleTheme), "whiteroom serve admin: " + dataErr.Error() + "\n", ""},
			{"a data directory that is not a store", with(notStore, s.site, "-theme", exampleTheme),
				"whiteroom serve admin: " + notStoreErr.Error() + "\n", ""},
			{"a data directory that is a file", with(file, s.site, "-theme", exampleTheme),
				"whiteroom serve admin: " + fileErr.Error() + "\n", ""},
			{"a missing site", with(s.data, missing, "-theme", exampleTheme), "whiteroom serve admin: " + rootErr.Error() + "\n", ""},
			{"a site without a schema", with(s.data, noSchema, "-theme", exampleTheme),
				filepath.Join(noSchema, "site", "schema.yaml") + ": missing file\n", ""},
			{"a site without a theme", with(s.data, noTheme), filepath.Join(noTheme, "theme") + ": missing directory\n", ""},
			{"a missing theme", with(s.data, noTheme, "-theme", missing), "whiteroom serve admin: " + rootErr.Error() + "\n", ""},
			{"a theme that is broken", with(s.data, noTheme, "-theme", brokenTheme), "", brokenTheme},
		} {
			code, stdout, stderr := runAdminCmd(t, tt.args...)
			if code != 1 || stdout != "" || stderr == "" || (tt.stderr != "" && stderr != tt.stderr) ||
				!strings.HasPrefix(stderr, tt.prefix) || strings.Contains(stderr, "Usage:") {
				t.Errorf("%s: whiteroom serve admin %s = %d, with stdout %q and stderr:\n%s\nwant 1, with stderr %q%s", tt.what,
					strings.Join(tt.args, " "), code, stdout, stderr, tt.stderr, tt.prefix)
			}
			checkUnlocked(t, tt.what, s.data)
			noFile(t, tt.what, sockFail)
		}
	}

	// A second server on the data directory, which the first has open: it waits for the lock and fails with its error,
	// whatever the address that it is given, and does not listen. The first goes on.
	first := startAdmin(t, t.Context(), s.args()...)
	for _, addr := range []string{first.addr, "127.0.0.1:0"} {
		code, stdout, stderr := runAdminCmd(t, s.args("-addr", addr)...)
		if code != 1 || stdout != "" || !strings.HasSuffix(stderr, ": another process holds the writer lock\n") ||
			!strings.HasPrefix(stderr, "whiteroom serve admin: ") || strings.Count(stderr, "\n") != 1 {
			t.Errorf("a second server with -addr %s = %d, with stdout %q and stderr:\n%s\nwant 1, with the error of the writer lock",
				addr, code, stdout, stderr)
		}
	}
	first.login(t)
	stopsWith(t, "the first server", first)
	checkUnlocked(t, "after the first server", s.data)

	// The same of a server on a socket: the second fails with the lock before it comes to the socket file, whether it is
	// that of the first or another, and leaves the file of the first, which goes on, as it is.
	sock := socketPath(t)
	one := startAdmin(t, t.Context(), s.args("-socket", sock, "-origin", "http://localhost")...)
	made, err := os.Lstat(sock)
	if err != nil {
		t.Fatal(err)
	}
	another := socketPath(t)
	for _, path := range []string{sock, another} {
		code, stdout, stderr := runAdminCmd(t, s.args("-socket", path, "-origin", "http://localhost")...)
		if code != 1 || stdout != "" || !strings.HasSuffix(stderr, ": another process holds the writer lock\n") ||
			!strings.HasPrefix(stderr, "whiteroom serve admin: ") || strings.Count(stderr, "\n") != 1 {
			t.Errorf("a second server with -socket %s = %d, with stdout %q and stderr:\n%s\nwant 1, with the error of the writer lock",
				path, code, stdout, stderr)
		}
		noFile(t, "a second server that fails", another)
		if now, err := os.Lstat(sock); err != nil || !os.SameFile(made, now) || now.Mode() != made.Mode() {
			t.Errorf("after a second server with -socket %s, the socket file of the first is %v (%v), want the file that it made", path,
				now, err)
		}
	}
	one.login(t)
	stopsWith(t, "the first server on a socket", one)
	noFile(t, "after the first server on a socket", sock)
	checkUnlocked(t, "after the first server on a socket", s.data)
}

// brokenListener is a listener whose Accept fails.
type brokenListener struct{ net.Listener }

func (brokenListener) Accept() (net.Conn, error) { return nil, errors.New("accept failed") }

// openForRun opens what serve admin serves, for the data directory of s, as the command does.
func openForRun(t *testing.T, s adminSite) *adminServer {
	t.Helper()
	settings := adminSettings{
		buildSettings: buildSettings{site: s.site, out: s.out, cache: s.cache, theme: s.theme}, data: s.data, network: "tcp",
		addr: "127.0.0.1:0",
	}
	a, err := openAdmin(testEnv(strings.NewReader(""), io.Discard, io.Discard), settings, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.close)
	return a
}

// runFailing runs a as the command does, with a context that does not end, and returns the exit code and what it prints
// on stderr, which it must give in time.
func runFailing(t *testing.T, a *adminServer) (code int, stderr string) {
	t.Helper()
	var b bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- reportRun(t.Context(), a, errorPrinter{w: &b, command: "whiteroom serve admin"}) }()
	select {
	case code := <-done:
		return code, b.String()
	case <-time.After(adminWait):
		t.Fatalf("the server has not returned in %v, though the HTTP server or the Publisher has failed", adminWait)
		return 0, ""
	}
}

// TestRunAdminFailure checks that the server stops, and fails with 1 and its error on stderr, when the HTTP server stops
// by itself, as when it cannot accept connections, and when the Publisher does, as when it has run already: with the
// other stopped, so that nothing listens any more.
func TestRunAdminFailure(t *testing.T) {
	a := openForRun(t, initEmpty(t))
	addr := a.listener.Addr().String()
	a.listener = brokenListener{a.listener}
	if code, stderr := runFailing(t, a); code != 1 || stderr != "whiteroom serve admin: accept failed\n" {
		t.Errorf("the server, when the listener fails = %d, with stderr %q, want 1 and the error of the listener", code, stderr)
	}
	if conn, err := net.DialTimeout("tcp", addr, 2*time.Second); err == nil {
		_ = conn.Close()
		t.Errorf("%s accepts a connection after the HTTP server has failed", addr)
	}

	b := openForRun(t, initEmpty(t))
	addr = b.listener.Addr().String()
	ended, cancel := context.WithCancel(t.Context())
	cancel()
	if err := b.publisher.Run(ended); err != nil {
		t.Fatal(err)
	}
	if code, stderr := runFailing(t, b); code != 1 || stderr != "whiteroom serve admin: the publisher has run already\n" {
		t.Errorf("the server, when the Publisher fails = %d, with stderr %q, want 1 and its error", code, stderr)
	}
	if conn, err := net.DialTimeout("tcp", addr, 2*time.Second); err == nil {
		_ = conn.Close()
		t.Errorf("%s accepts a connection after the Publisher has failed", addr)
	}
}

// TestParseProxy checks the trusted proxies: an IP address stands for itself alone, as an IPv4 address if it is a mapped
// one, and without a zone; a range as it is; and no other text, and no range of every address.
func TestParseProxy(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"127.0.0.1", "127.0.0.1/32"},
		{"10.1.2.3/8", "10.1.2.3/8"},
		{"10.0.0.0/8", "10.0.0.0/8"},
		{"::1", "::1/128"},
		{"2001:db8::/32", "2001:db8::/32"},
		{"::ffff:10.0.0.1", "10.0.0.1/32"},
		{"fe80::1%eth0", "fe80::1/128"},
		{"0.0.0.0/1", "0.0.0.0/1"},
		{"::/1", "::/1"},
		{"10.0.0.1/32", "10.0.0.1/32"},
	} {
		got, err := parseProxy(tt.in)
		if err != nil || got.String() != tt.want {
			t.Errorf("parseProxy(%q) = %v, %v, want %s", tt.in, got, err, tt.want)
		}
	}
	for in, want := range map[string]string{
		"": "not an IP address or a CIDR range", "x": "not an IP address or a CIDR range", "10.0.0.0/33": "not an IP address or a CIDR range",
		"10.0.0.1/": "not an IP address or a CIDR range", "10.0.0.1/8/8": "not an IP address or a CIDR range",
		"fe80::1%eth0/64": "not an IP address or a CIDR range", " 10.0.0.1": "not an IP address or a CIDR range",
		"0.0.0.0/0": "covers every address", "::/0": "covers every address", "10.0.0.0/0": "covers every address",
	} {
		if got, err := parseProxy(in); err == nil || err.Error() != want {
			t.Errorf("parseProxy(%q) = %v, %v, want the error %q", in, got, err, want)
		}
	}
}
