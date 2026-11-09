package admin

import (
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
)

// TestBrowser logs in, asks, logs in again and logs out over TLS with a client that keeps cookies, as a browser does.
func TestBrowser(t *testing.T) {
	ls := newLiveServer(t, true)
	admin := ls.addAccount(t, "admin", account.RoleAdmin)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := ls.client(jar)
	u, err := url.Parse(ls.url)
	if err != nil {
		t.Fatal(err)
	}
	held := func() []string {
		var held []string
		for _, ck := range jar.Cookies(u) {
			held = append(held, ck.Name+"="+ck.Value)
		}
		return held
	}
	if ls.wantCookieName() != "__Host-session" || !ls.https {
		t.Fatalf("the test server is not an https origin: cookie %q", ls.wantCookieName())
	}

	checkProblem(t, "GET before the login", ls.do(t, c, http.MethodGet, "/api/session", ""), "unauthenticated")
	start := ls.clock.now()
	res := ls.do(t, c, http.MethodPost, "/api/session", loginBody("admin", testPassword("admin")))
	ls.checkAnswer(t, "login", res, admin, start, anyLoginCookie)
	first := held()
	if len(first) != 1 || !strings.HasPrefix(first[0], "__Host-session=") || len(first[0]) != len("__Host-session=")+43 {
		t.Fatalf("the client holds %d cookies after the login, want the one of the session", len(first))
	}

	ls.clock.advance(10 * time.Minute)
	get := ls.do(t, c, http.MethodGet, "/api/session", "")
	ls.checkAnswer(t, "GET", get, admin, start, "")
	head := ls.do(t, c, http.MethodHead, "/api/session", "")
	if b, _ := io.ReadAll(head.Body); head.StatusCode != http.StatusOK || len(b) != 0 || head.ContentLength != get.ContentLength ||
		head.ContentLength <= 0 ||
		head.Header.Get("Content-Type") != "application/json" || len(head.Header.Values("Set-Cookie")) != 0 {
		t.Errorf("HEAD has the status %d, %d bytes of body, the length %d and the Content-Type %q", head.StatusCode, len(b), head.ContentLength, head.Header.Get("Content-Type"))
	}
	checkHeaders(t, "HEAD", head, true)

	// A new login ends the session of the cookie that it carries, and replaces the cookie.
	second := ls.do(t, c, http.MethodPost, "/api/session", loginBody("admin", testPassword("admin")))
	ls.checkAnswer(t, "second login", second, admin, ls.clock.now(), anyLoginCookie)
	if now := held(); len(now) != 1 || now[0] == first[0] {
		t.Errorf("the client holds %d cookies after the second login, want a new one of the session", len(now))
	}
	stale := ls.client(nil)
	checkProblem(t, "GET with the first token", ls.do(t, stale, http.MethodGet, "/api/session", "", "Cookie", first[0]), "unauthenticated")

	// Logout clears the cookie of the client.
	out := ls.do(t, c, http.MethodDelete, "/api/session", "")
	if out.StatusCode != http.StatusNoContent || !equalValues(out.Header.Values("Set-Cookie"), ls.clearLine()) {
		t.Errorf("logout has the status %d and sets %q, want 204 and %q", out.StatusCode, redacted(out.Header.Values("Set-Cookie")), ls.clearLine())
	}
	checkHeaders(t, "logout", out, true)
	if now := held(); len(now) != 0 {
		t.Errorf("the client holds %d cookies after the logout, want none", len(now))
	}
	res = ls.do(t, c, http.MethodGet, "/api/session", "")
	checkProblem(t, "GET after the logout", res, "unauthenticated")
	checkNoCookie(t, "GET after the logout", res)
	if n := ls.sessionRows(t); n != 0 {
		t.Errorf("the store has %d sessions after the logout, want none", n)
	}
}

// TestLiveHTTP does the same over plain HTTP on a loopback address, where a client does not send a Secure cookie by itself,
// so the cookie is carried by hand.
func TestLiveHTTP(t *testing.T) {
	ls := newLiveServer(t, false)
	admin := ls.addAccount(t, "admin", account.RoleAdmin)
	c := ls.client(nil)
	if ls.wantCookieName() != "session" || ls.https {
		t.Fatalf("the test server is not an http origin: cookie %q", ls.wantCookieName())
	}
	start := ls.clock.now()
	res := ls.do(t, c, http.MethodPost, "/api/session", loginBody("admin", testPassword("admin")))
	token := ls.tokenOf(t, res)
	ls.checkAnswer(t, "login", res, admin, start, ls.setCookieLine(token))
	ls.checkAnswer(t, "GET", ls.do(t, c, http.MethodGet, "/api/session", "", ls.cookie(token)...), admin, start, "")
	checkProblem(t, "GET without the cookie", ls.do(t, c, http.MethodGet, "/api/session", ""), "unauthenticated")
	out := ls.do(t, c, http.MethodDelete, "/api/session", "", ls.cookie(token)...)
	if out.StatusCode != http.StatusNoContent || !equalValues(out.Header.Values("Set-Cookie"), ls.clearLine()) {
		t.Errorf("logout has the status %d and sets %q, want 204 and %q", out.StatusCode, redacted(out.Header.Values("Set-Cookie")), ls.clearLine())
	}
	checkProblem(t, "GET after the logout", ls.do(t, c, http.MethodGet, "/api/session", "", ls.cookie(token)...), "unauthenticated")

	// The other methods are 405 with the list of the ones that the path takes, and a request from another site is 403.
	res = ls.do(t, c, http.MethodPut, "/api/session", "")
	if got := res.Header.Values("Allow"); !equalValues(got, "DELETE, GET, HEAD, POST") {
		t.Errorf("PUT has the Allow %q, want DELETE, GET, HEAD, POST", got)
	}
	checkProblem(t, "PUT", res, "method-not-allowed")
	res = ls.do(t, c, http.MethodPost, "/api/session", loginBody("admin", testPassword("admin")), "Origin", "https://evil.example")
	checkProblem(t, "login from another origin", res, "cross-origin")
	res = ls.do(t, c, http.MethodPost, "/api/session", loginBody("admin", testPassword("admin")), "Sec-Fetch-Site", "cross-site")
	checkProblem(t, "login with Sec-Fetch-Site cross-site", res, "cross-origin")
}

// TestConcurrentSessionsHTTP logs accounts in and out and asks for their sessions at the same time, and shows that the
// answer to a token is always about its own account, that a token which has been ended never works again, and that nothing is
// left behind. It is for the race detector too.
func TestConcurrentSessionsHTTP(t *testing.T) {
	ls := newLiveServer(t, false, "127.0.0.0/8")
	const workers = 8
	rounds := 6
	if testing.Short() {
		rounds = 2
	}
	logins := make([]string, workers)
	for i := range logins {
		logins[i] = fmt.Sprintf("user%d", i)
		ls.addAccount(t, logins[i], account.RoleEditor)
	}
	// The tokens that the workers have been given, for the others to ask about.
	var mu sync.Mutex
	owner := map[string]string{} // token to login
	var failures []string
	fail := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		failures = append(failures, fmt.Sprintf(format, args...))
	}
	loginOf := func(body string) string {
		_, rest, _ := strings.Cut(body, `"login":"`)
		login, _, _ := strings.Cut(rest, `"`)
		return login
	}
	tokenIn := func(res *http.Response) string {
		for _, ck := range res.Cookies() {
			if ck.Name == ls.wantCookieName() {
				return ck.Value
			}
		}
		return ""
	}

	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			c := ls.client(nil)
			client := []string{"X-Forwarded-For", fmt.Sprintf("203.0.113.%d", w+1)}
			do := func(method, path, body string, headers ...string) (*http.Response, string) {
				req, err := http.NewRequestWithContext(t.Context(), method, ls.url+path, strings.NewReader(body))
				if err != nil {
					fail("%v", err)
					return nil, ""
				}
				if method != http.MethodGet {
					req.Header.Set("Origin", ls.origin)
					req.Header.Set("Content-Type", "application/json")
				}
				for i := 0; i < len(headers); i += 2 {
					req.Header.Set(headers[i], headers[i+1])
				}
				res, err := c.Do(req)
				if err != nil {
					fail("%s %s: %v", method, path, err)
					return nil, ""
				}
				b, _ := io.ReadAll(res.Body)
				_ = res.Body.Close()
				return res, string(b)
			}
			with := func(token string) []string { return append(ls.cookie(token), client...) }
			login := logins[w]
			var previous string
			for round := range rounds {
				// A wrong password first, then the right one; on odd rounds with the cookie of the session of the round before.
				if res, _ := do(http.MethodPost, "/api/session", loginBody(login, wrongPassword), client...); res != nil && res.StatusCode != http.StatusUnauthorized {
					fail("%s: a wrong password has the status %d", login, res.StatusCode)
				}
				headers := client
				if previous != "" {
					headers = with(previous)
				}
				res, body := do(http.MethodPost, "/api/session", loginBody(login, testPassword(login)), headers...)
				token := ""
				if res != nil {
					token = tokenIn(res)
				}
				if res == nil || res.StatusCode != http.StatusOK || token == "" {
					fail("%s: login in round %d failed", login, round)
					return
				}
				mu.Lock()
				owner[token] = login
				mu.Unlock()
				if got := loginOf(body); got != login {
					fail("%s: the login answer is about %q", login, got)
				}
				if previous != "" {
					if res, _ := do(http.MethodGet, "/api/session", "", with(previous)...); res != nil && res.StatusCode != http.StatusUnauthorized {
						fail("%s: the token that a login carried still works (status %d)", login, res.StatusCode)
					}
				}
				// The session answers for its own account, and the sessions of the others do for theirs.
				if res, body := do(http.MethodGet, "/api/session", "", with(token)...); res == nil || res.StatusCode != http.StatusOK || loginOf(body) != login {
					fail("%s: GET with its token is not about it", login)
				}
				mu.Lock()
				var someone, theirs string
				for tok, l := range owner {
					if l != login {
						someone, theirs = tok, l
						break
					}
				}
				mu.Unlock()
				if someone != "" {
					res, body := do(http.MethodGet, "/api/session", "", with(someone)...)
					if res != nil && res.StatusCode == http.StatusOK && loginOf(body) != theirs {
						fail("%s: the token of %s answers for %q", login, theirs, loginOf(body))
					}
				}
				previous = token
				if round%2 == 1 {
					if res, _ := do(http.MethodDelete, "/api/session", "", with(token)...); res == nil || res.StatusCode != http.StatusNoContent {
						fail("%s: logout failed", login)
					}
					if res, _ := do(http.MethodGet, "/api/session", "", with(token)...); res != nil && res.StatusCode != http.StatusUnauthorized {
						fail("%s: the token still works after the logout (status %d)", login, res.StatusCode)
					}
					previous = ""
				}
			}
			if previous != "" {
				_, _ = do(http.MethodDelete, "/api/session", "", with(previous)...)
			}
		})
	}
	wg.Wait()
	for _, f := range failures {
		t.Error(f)
	}
	if n := ls.sessionRows(t); n != 0 {
		t.Errorf("the store has %d sessions after every worker has logged out, want none", n)
	}
	if got := len(ls.logins); got != 0 {
		t.Errorf("%d places for logins are taken after every request has been answered", got)
	}
	if p, _, _ := ls.counted(); p != 0 {
		t.Errorf("the attempts keep count of %d pairs after every worker has logged in, want none", p)
	}
	if got := ls.logs.String(); got != "" {
		t.Errorf("the requests have logged %q", redact(got))
	}
}
