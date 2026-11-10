package admin

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// minSecretLength is the shortest password that secretWatch watches for: a shorter text is in too many answers.
const minSecretLength = 8

// tokenLength is the length of a token of the accounts: 32 bytes in unpadded base64url.
const tokenLength = 43

var tokenValue = regexp.MustCompile(`=[A-Za-z0-9_-]{43}`)

// redact hides what may be a token in a text that a failure message shows: the value of a cookie of the length of one.
// A test never prints a secret, not even one of its own.
func redact(s string) string {
	return tokenValue.ReplaceAllString(s, "=<token>")
}

// redacted is redact for the lines of a header.
func redacted(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = redact(l)
	}
	return out
}

// secretWatch collects what a test server answers, and the secrets of the test: every password that a login carried, and
// every token in a cookie or in a Set-Cookie. At the end of the test it checks that no password is in any answer or in the
// log, and that no token is in any answer but the Set-Cookie of the login that made it, or in the log. A failure says which
// answer shows a secret and of what kind, and never the secret.
type secretWatch struct {
	mu        sync.Mutex
	passwords map[string]bool
	tokens    map[string]bool
	answers   []watched
}

type watched struct{ what, text string }

func newSecretWatch() *secretWatch {
	return &secretWatch{passwords: make(map[string]bool), tokens: make(map[string]bool)}
}

func (w *secretWatch) password(p string) {
	if len(p) < minSecretLength {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.passwords[p] = true
}

// token watches for v if it is a token.
func (w *secretWatch) token(v string) {
	if len(v) != tokenLength {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tokens[v] = true
}

// watchRequest watches for the tokens in the cookies of a request.
func (w *secretWatch) watchRequest(h http.Header) {
	for _, c := range (&http.Request{Header: h}).Cookies() {
		w.token(c.Value)
	}
}

// record keeps an answer. The tokens in its Set-Cookie are watched for, and left out of what is kept.
func (w *secretWatch) record(what string, status int, header http.Header, body []byte) {
	var b strings.Builder
	fmt.Fprintf(&b, "%d\n", status)
	for _, name := range slices.Sorted(maps.Keys(header)) {
		for _, v := range header[name] {
			if name == "Set-Cookie" {
				if c, err := http.ParseSetCookie(v); err == nil {
					w.token(c.Value)
				}
				continue
			}
			fmt.Fprintf(&b, "%s: %s\n", name, v)
		}
	}
	b.Write(body)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.answers = append(w.answers, watched{what, b.String()})
}

// watching returns how many passwords, tokens and answers are kept.
func (w *secretWatch) watching() (passwords, tokens, answers int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.passwords), len(w.tokens), len(w.answers)
}

// problems returns where a secret is shown, in the answers that were kept and in the log.
func (w *secretWatch) problems(logs string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var found []string
	for kind, secrets := range map[string]map[string]bool{"password": w.passwords, "token": w.tokens} {
		for secret := range secrets {
			if strings.Contains(logs, secret) {
				found = append(found, "the log has a "+kind+" of the test")
			}
			for _, a := range w.answers {
				if strings.Contains(a.text, secret) {
					found = append(found, "the answer to "+a.what+" has a "+kind+" of the test")
				}
			}
		}
	}
	slices.Sort(found)
	return found
}

func (w *secretWatch) check(t *testing.T, logs string) {
	t.Helper()
	for _, p := range w.problems(logs) {
		t.Error(p)
	}
}

// testPassword is the password of the account with the login name in the tests: it passes every rule of the accounts.
func testPassword(login string) string {
	return login + " has a long password"
}

// addAccount makes an active account: it is invited by System, and its password is set to testPassword(login).
func (ts *testServer) addAccount(t *testing.T, login string, role account.Role) account.Account {
	t.Helper()
	in := account.Input{Login: login, Name: "Name of " + login, Email: login + "@example.com", Role: role}
	_, token, err := ts.accounts.Invite(t.Context(), account.System, in)
	if err != nil {
		t.Fatalf("Invite(%q) = %v", login, err)
	}
	ts.seen.password(testPassword(login))
	a, err := ts.accounts.SetPassword(t.Context(), token, testPassword(login))
	if err != nil {
		t.Fatalf("SetPassword(%q) = %v", login, err)
	}
	return a
}

// inviteAccount makes a pending account, which has no password.
func (ts *testServer) inviteAccount(t *testing.T, login string, role account.Role) account.Account {
	t.Helper()
	in := account.Input{Login: login, Name: "Name of " + login, Email: login + "@example.com", Role: role}
	a, _, err := ts.accounts.Invite(t.Context(), account.System, in)
	if err != nil {
		t.Fatalf("Invite(%q) = %v", login, err)
	}
	return a
}

func loginBody(login, password string) string {
	body, err := json.Marshal(map[string]string{"login": login, "password": password})
	if err != nil {
		panic(err)
	}
	return string(body)
}

// postLogin sends a login from the origin of the server. The password is watched for.
func (ts *testServer) postLogin(login, password string, headers ...string) *http.Response {
	ts.seen.password(password)
	headers = append([]string{"Origin", ts.origin, "Content-Type", "application/json"}, headers...)
	return ts.send(http.MethodPost, "/api/session", loginBody(login, password), headers...)
}

// wantCookieName is the name of the cookie of the session that the contract gives, and not the field of the Server.
func (ts *testServer) wantCookieName() string {
	if ts.https {
		return "__Host-session"
	}
	return "session"
}

// cookie returns the header of a request that carries the token in the cookie of the session.
func (ts *testServer) cookie(token string) []string {
	return []string{"Cookie", ts.wantCookieName() + "=" + token}
}

// from returns the header that makes ts.request send the request from the address, the way a proxy or a peer is.
func from(addr string) []string {
	return []string{"RemoteAddr", addr}
}

func join(headers ...[]string) []string {
	return slices.Concat(headers...)
}

// getSession sends GET /api/session with the headers.
func (ts *testServer) getSession(headers ...string) *http.Response {
	return ts.send(http.MethodGet, "/api/session", "", headers...)
}

// deleteSession sends DELETE /api/session from the origin of the server.
func (ts *testServer) deleteSession(headers ...string) *http.Response {
	return ts.send(http.MethodDelete, "/api/session", "", append([]string{"Origin", ts.origin}, headers...)...)
}

// tokenOf returns the token that a login response sets in the cookie of the session.
func (ts *testServer) tokenOf(t *testing.T, res *http.Response) string {
	t.Helper()
	for _, c := range res.Cookies() {
		if c.Name == ts.wantCookieName() && c.Value != "" {
			return c.Value
		}
	}
	t.Fatalf("the response (status %d) does not set a session cookie: %q", res.StatusCode, redacted(res.Header.Values("Set-Cookie")))
	return ""
}

// logIn logs in as the account with its testPassword, expects 200, and returns the token.
func (ts *testServer) logIn(t *testing.T, login string, headers ...string) string {
	t.Helper()
	res := ts.postLogin(login, testPassword(login), headers...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login of %q has the status %d, want 200: %s", login, res.StatusCode, bodyOf(t, res))
	}
	return ts.tokenOf(t, res)
}

// setCookieLine is the Set-Cookie that a login sets, and clearLine the one that clears the cookie.
func (ts *testServer) setCookieLine(token string) string {
	return ts.wantCookieName() + "=" + token + "; Path=/; HttpOnly; Secure; SameSite=Strict"
}

func (ts *testServer) clearLine() string {
	return ts.wantCookieName() + "=; Path=/; Max-Age=0; HttpOnly; Secure; SameSite=Strict"
}

// answerJSON returns the body that a login and GET /api/session answer for the account whose session was
// authenticated at the time.
func answerJSON(a account.Account, authenticated time.Time) string {
	utc := authenticated.UTC()
	return fmt.Sprintf(
		`{"account":{"id":%q,"login":%q,"name":%q,"email":%q,"role":%q%s},"session":{"authenticatedAt":%q,"expiresAt":%q,"idleTimeout":3600,"factors":["password"]}}`,
		a.ID, a.Login, a.Name, a.Email, a.Role, authorMember(a.Author),
		utc.Format(time.RFC3339Nano), utc.Add(12*time.Hour).Format(time.RFC3339Nano))
}

func authorMember(author string) string {
	if author == "" {
		return ""
	}
	return fmt.Sprintf(",%q:%q", "author", author)
}

// anyLoginCookie stands for the cookie of a login, whatever its token is, in checkAnswer.
const anyLoginCookie = "*"

// checkAnswer checks that res is the 200 answer for the account, with the headers of every response, and no cookie unless
// cookie says that one is expected: a line, or anyLoginCookie.
func (ts *testServer) checkAnswer(t *testing.T, what string, res *http.Response, a account.Account, authenticated time.Time, cookie string) {
	t.Helper()
	if res.StatusCode != http.StatusOK {
		t.Errorf("%s has the status %d, want 200", what, res.StatusCode)
	}
	if got := res.Header.Values("Content-Type"); !equalValues(got, "application/json") {
		t.Errorf("%s has the Content-Type %q, want application/json", what, got)
	}
	if got, want := bodyOf(t, res), answerJSON(a, authenticated); got != want {
		t.Errorf("%s has the body\n%s\nwant\n%s", what, got, want)
	}
	got := res.Header.Values("Set-Cookie")
	switch cookie {
	case "":
		if len(got) != 0 {
			t.Errorf("%s sets the cookies %q, want none", what, redacted(got))
		}
	case anyLoginCookie:
		pattern := regexp.MustCompile("^" + regexp.QuoteMeta(ts.wantCookieName()) + `=[A-Za-z0-9_-]{43}; Path=/; HttpOnly; Secure; SameSite=Strict$`)
		if len(got) != 1 || !pattern.MatchString(got[0]) {
			t.Errorf("%s sets the cookies %d times, want one cookie of the session with a new token", what, len(got))
		}
	default:
		if !equalValues(got, cookie) {
			t.Errorf("%s sets the cookies %q, want %q", what, redacted(got), redact(cookie))
		}
	}
	checkHeaders(t, what, res, ts.https)
}

func checkNoCookie(t *testing.T, what string, res *http.Response) {
	t.Helper()
	if got := res.Header.Values("Set-Cookie"); len(got) != 0 {
		t.Errorf("%s sets the cookies %q, want none", what, redacted(got))
	}
}

// sessionRows returns how many rows the table sessions of the store has.
func (ts *testServer) sessionRows(t *testing.T) int {
	t.Helper()
	var n int
	err := ts.db.View(t.Context(), func(tx *store.Tx) error {
		rows, err := tx.Sessions()
		n = len(rows)
		return err
	})
	if err != nil {
		t.Fatalf("Sessions() = %v", err)
	}
	return n
}

// counted returns how many pairs, login names and clients the attempts keep count of.
func (ts *testServer) counted() (pairs, names, clients int) {
	ts.attempts.mu.Lock()
	defer ts.attempts.mu.Unlock()
	return len(ts.attempts.pairs.entries), len(ts.attempts.names.entries), len(ts.attempts.clients.entries)
}

// levels returns the attempts that are counted for the login name from the client at the address: those of the pair, those of
// the login name over all clients, and those of the client over all login names.
func (ts *testServer) levels(login, addr string) (pair, name, client int) {
	ts.attempts.mu.Lock()
	defer ts.attempts.mu.Unlock()
	key, prefix := nameKey(account.NormalizeLogin(login)), clientPrefix(netip.MustParseAddr(addr))
	now := ts.attempts.elapsed()
	units := func(expires, leak time.Duration) int { return int((expires - now + leak - 1) / leak) }
	if p := ts.attempts.pairs.entries[newPairKey(key, prefix)]; p != nil {
		pair = int(p.count)
	}
	if n := ts.attempts.names.entries[key]; n != nil {
		name = units(n.expires, nameLeak)
	}
	if c := ts.attempts.clients.entries[prefix]; c != nil {
		client = units(c.expires, clientLeak)
	}
	return pair, name, client
}

func (ts *testServer) nothingCounted(t *testing.T, what string) {
	t.Helper()
	if p, n, c := ts.counted(); p != 0 || n != 0 || c != 0 {
		t.Errorf("after %s the attempts keep count of %d pairs, %d login names and %d clients, want none", what, p, n, c)
	}
}

// recordingTransport records what the server answers to a client, for the secrets, as ts.send does.
type recordingTransport struct {
	base http.RoundTripper
	seen *secretWatch
}

func (rt recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.seen.watchRequest(req.Header)
	res, err := rt.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(res.Body)
	_ = res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(body))
	rt.seen.record(req.Method+" "+req.URL.Path, res.StatusCode, res.Header, body)
	return res, err
}

// liveServer is a test server that listens on a loopback port, as a server of its origin: that has the address of the
// listener, since the Host of a request is checked.
type liveServer struct {
	*testServer
	srv *httptest.Server
	url string
}

// newLiveServer starts a test server on a loopback port, over TLS if secure.
func newLiveServer(t *testing.T, secure bool, proxies ...string) *liveServer {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	scheme := "http"
	if secure {
		scheme = "https"
	}
	url := scheme + "://" + srv.Listener.Addr().String()
	ts := newTestServer(t, url, proxies...)
	srv.Config.Handler = ts.Server
	if secure {
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	return &liveServer{testServer: ts, srv: srv, url: url}
}

// client returns a client of the server that records what it answers. With a jar it keeps cookies as a browser does.
func (ls *liveServer) client(jar http.CookieJar) *http.Client {
	return &http.Client{
		Transport: recordingTransport{base: ls.srv.Client().Transport, seen: ls.seen},
		Jar:       jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// do sends a request to the server with c. A request that is not safe comes from the origin of the server, as a browser
// sends it, and a body is JSON.
func (ls *liveServer) do(t *testing.T, c *http.Client, method, path, body string, headers ...string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, ls.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if !isSafeMethod(method) {
		req.Header.Set("Origin", ls.origin)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return res
}
