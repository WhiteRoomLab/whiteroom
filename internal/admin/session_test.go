package admin

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
)

func TestNewRequiresAccounts(t *testing.T) {
	const want = "admin: Config.Accounts is required"
	if _, err := New(Config{Origin: httpsOrigin}); err == nil || err.Error() != want {
		t.Errorf("New without Accounts = %v, want the error %q", err, want)
	}
	if _, err := New(Config{Origin: localOrigin, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}); err == nil || err.Error() != want {
		t.Errorf("New with proxies and without Accounts = %v, want the error %q", err, want)
	}

	// The origin and the trusted proxies are checked first.
	m, _ := testAccounts(t)
	if _, err := New(Config{Origin: "ftp://admin.example.com"}); err == nil || !strings.HasPrefix(err.Error(), "admin: invalid origin") {
		t.Errorf("New with an invalid origin and without Accounts = %v, want the error of the origin", err)
	}
	if _, err := New(Config{Origin: httpsOrigin, TrustedProxies: []netip.Prefix{{}}}); err == nil || !strings.HasPrefix(err.Error(), "admin: invalid trusted proxy") {
		t.Errorf("New with an invalid proxy and without Accounts = %v, want the error of the proxy", err)
	}
	if _, err := New(testConfig(m, httpsOrigin)); err != nil {
		t.Errorf("New with Accounts = %v", err)
	}
}

func TestSessionRoutes(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodOptions, http.MethodTrace, "PROPFIND"} {
		what := method + " /api/session"
		res := ts.send(method, "/api/session", "", "Origin", ts.origin)
		if got := res.Header.Values("Allow"); !equalValues(got, "DELETE, GET, HEAD, POST") {
			t.Errorf("%s has the Allow %q, want DELETE, GET, HEAD, POST", what, got)
		}
		checkProblem(t, what, res, "method-not-allowed")
	}
	for _, path := range []string{"/api/session/", "/api/session/x", "/api/sessions", "/api/session/me/x"} {
		checkProblem(t, "GET "+path, ts.send(http.MethodGet, path, ""), "not-found")
	}
}

func TestSessionCookie(t *testing.T) {
	for _, origin := range []string{httpsOrigin, localOrigin} {
		ts := newTestServer(t, origin)
		name := ts.wantCookieName()
		other := "session"
		if name == other {
			other = "__Host-session"
		}
		for _, tt := range []struct {
			what    string
			cookies []string
			token   string
			sent    bool
		}{
			{"no cookie", nil, "", false},
			{"cookies of other names", []string{"a=b; c=d"}, "", false},
			{"the name of the other origin", []string{other + "=abc"}, "", false},
			{"a name that ends with it", []string{"x" + name + "=abc"}, "", false},
			{"a name that starts with it", []string{name + "x=abc"}, "", false},
			{"a name in another case", []string{strings.ToUpper(name) + "=abc"}, "", false},
			{"one", []string{name + "=abc"}, "abc", true},
			{"one among others", []string{"a=b; " + name + "=abc; c=d"}, "abc", true},
			{"one in each of two lines", []string{"a=b", name + "=abc"}, "abc", true},
			{"two in a line", []string{name + "=abc; " + name + "=def"}, "", true},
			{"two in two lines", []string{name + "=abc", name + "=def"}, "", true},
			{"two that are equal", []string{name + "=abc; " + name + "=abc"}, "", true},
			{"three", []string{name + "=a; " + name + "=b", name + "=c"}, "", true},
		} {
			req := ts.request(http.MethodGet, "/api/session", "")
			for _, c := range tt.cookies {
				req.Header.Add("Cookie", c)
			}
			token, sent := ts.sessionCookie(req)
			if token != tt.token || sent != tt.sent {
				t.Errorf("%s, %s: sessionCookie = %q, %v, want %q, %v", origin, tt.what, token, sent, tt.token, tt.sent)
			}
		}
	}
}

func TestLogin(t *testing.T) {
	for _, tt := range []struct{ origin, cookie string }{
		{httpsOrigin, "__Host-session"},
		{localOrigin, "session"},
	} {
		ts := newTestServer(t, tt.origin)
		admin := ts.addAccount(t, "admin", account.RoleAdmin)
		start := ts.clock.now()
		what := tt.origin + " login"

		res := ts.postLogin("admin", testPassword("admin"))
		token := ts.tokenOf(t, res)
		if len(token) != 43 || strings.Trim(token, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
			t.Errorf("%s: the token is not 43 characters of base64url", what)
		}
		wantCookie := tt.cookie + "=" + token + "; Path=/; HttpOnly; Secure; SameSite=Strict"
		ts.checkAnswer(t, what, res, admin, start, wantCookie)

		// GET and HEAD show the same, with no cookie.
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			what := tt.origin + " " + method + " with the session"
			res := ts.send(method, "/api/session", "", "Cookie", tt.cookie+"="+token)
			if method == http.MethodGet {
				ts.checkAnswer(t, what, res, admin, start, "")
			} else {
				if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "application/json" {
					t.Errorf("%s has the status %d and the Content-Type %q", what, res.StatusCode, res.Header.Get("Content-Type"))
				}
				checkNoCookie(t, what, res)
			}
		}

		// A request that is not from the origin of the server is refused whatever it carries.
		res = ts.send(http.MethodPost, "/api/session", loginBody("admin", testPassword("admin")),
			"Content-Type", "application/json", "Cookie", tt.cookie+"="+token)
		checkProblem(t, what+" without an origin", res, "cross-origin")
		checkNoCookie(t, what+" without an origin", res)
	}
}

// TestLoginTimesAreUTC shows that the times are in UTC whatever zone the clock of the accounts has.
func TestLoginTimesAreUTC(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	admin := ts.addAccount(t, "admin", account.RoleAdmin)
	jst := time.FixedZone("JST", 9*60*60)
	at := time.Date(2026, time.November, 9, 21, 30, 15, 123456789, jst)
	ts.clock.set(at)
	res := ts.postLogin("admin", testPassword("admin"))
	token := ts.tokenOf(t, res)
	ts.checkAnswer(t, "login", res, admin, at, ts.setCookieLine(token))
	if !strings.Contains(answerJSON(admin, at), `"authenticatedAt":"2026-11-09T12:30:15.123456789Z","expiresAt":"2026-11-10T00:30:15.123456789Z"`) {
		t.Error("the test does not expect UTC times")
	}
	ts.checkAnswer(t, "GET", ts.getSession(ts.cookie(token)...), admin, at, "")
}

func TestLoginNormalizesTheLoginName(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	admin := ts.addAccount(t, "admin", account.RoleAdmin)
	start := ts.clock.now()
	for _, login := range []string{"admin", "Admin", "ADMIN", "  admin\t", "\x00a\x07dmin\n"} {
		res := ts.postLogin(login, testPassword("admin"))
		token := ts.tokenOf(t, res)
		ts.checkAnswer(t, fmt.Sprintf("login as %q", login), res, admin, start, ts.setCookieLine(token))
	}
}

// TestLoginDoesNotFoldUnicode shows that the login name is normalized the way the accounts do, and not with the case mapping
// of Unicode, which would let the dotless i and the Kelvin sign log in as other names.
func TestLoginDoesNotFoldUnicode(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ts.addAccount(t, "admin", account.RoleAdmin)
	ts.addAccount(t, "kim", account.RoleEditor)
	dotless := "adm" + string(rune(0x131)) + "n" // strings.ToUpper gives ADMIN
	kelvin := string(rune(0x212A)) + "im"        // strings.ToLower gives kim
	ts.wantFailure(t, "a login name with the dotless i", dotless, testPassword("admin"))
	ts.wantFailure(t, "a login name with the Kelvin sign", kelvin, testPassword("kim"))
	if n := ts.sessionRows(t); n != 0 {
		t.Errorf("the store has %d sessions, want none", n)
	}
}

func TestLoginBody(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ts.addAccount(t, "admin", account.RoleAdmin)
	pw := testPassword("admin")
	ts.seen.password(pw)
	with := func(login, password string) string {
		return fmt.Sprintf(`{"login":%s,"password":%s}`, login, password)
	}
	for _, body := range []string{
		``, ` `, `{`, `}`, `{}`, `[]`, `null`, `NULL`, `""`, `"admin"`, `5`, `true`, `[{"login":"admin","password":"` + pw + `"}]`,
		`{"login":"admin"}`, `{"password":"` + pw + `"}`,
		with(`null`, `"`+pw+`"`), with(`"admin"`, `null`), with(`null`, `null`),
		with(`5`, `"`+pw+`"`), with(`"admin"`, `5`), with(`true`, `"`+pw+`"`), with(`["admin"]`, `"`+pw+`"`),
		with(`{"a":1}`, `"`+pw+`"`), with(`"admin"`, `["`+pw+`"]`), with(`"admin"`, `{"a":"`+pw+`"}`),
		`{"login":"admin","password":"` + pw + `","extra":1}`, `{"login":"admin","password":"` + pw + `","extra":null}`,
		`{"login":"admin","login":"admin","password":"` + pw + `"}`, `{"Login":"admin","Password":"` + pw + `"}`,
		`{"LOGIN":"admin","PASSWORD":"` + pw + `"}`, `{"login":"admin","password":"` + pw + `"} {}`,
		`{"login":"admin","password":"` + pw + `"}x`, `{"login":"admin","password":"` + pw + `",}`, `{'login':'admin','password':'` + pw + `'}`,
		"{\"login\":\"admin\",\"password\":\"" + pw + "\xff\"}",
	} {
		res := ts.sendJSON("/api/session", "application/json", body)
		what := fmt.Sprintf("login with the body %.60q", body)
		checkProblem(t, what, res, "bad-request")
		checkNoCookie(t, what, res)
	}

	for _, contentType := range []string{
		"", "text/plain", "text/json", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", "application/jsonx",
		"application/json, text/plain",
	} {
		res := ts.sendJSON("/api/session", contentType, loginBody("admin", pw))
		checkProblem(t, fmt.Sprintf("login with the Content-Type %q", contentType), res, "unsupported-media-type")
	}
	for _, contentType := range []string{"application/json", "application/json; charset=utf-8", "APPLICATION/JSON"} {
		res := ts.sendJSON("/api/session", contentType, loginBody("admin", pw))
		if res.StatusCode != http.StatusOK {
			t.Errorf("login with the Content-Type %q has the status %d, want 200", contentType, res.StatusCode)
		}
	}

	// The body may have 65536 bytes, and no more.
	body := loginBody("admin", pw)
	res := ts.sendJSON("/api/session", "application/json", body+strings.Repeat(" ", 65536-len(body)))
	if res.StatusCode != http.StatusOK {
		t.Errorf("login with a body of 65536 bytes has the status %d, want 200", res.StatusCode)
	}
	res = ts.sendJSON("/api/session", "application/json", body+strings.Repeat(" ", 65537-len(body)))
	checkProblem(t, "login with a body of 65537 bytes", res, "too-large")
	checkNoCookie(t, "login with a body of 65537 bytes", res)
	res = ts.sendJSON("/api/session", "application/json", loginBody("admin", strings.Repeat("a", 70000)))
	checkProblem(t, "login with a password of 70000 bytes", res, "too-large")

	// None of them was counted, and the logins that worked have been forgotten.
	ts.nothingCounted(t, "the logins that the body refuses")
}
