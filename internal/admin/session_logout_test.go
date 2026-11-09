package admin

import (
	"net/http"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
)

func TestLoginReplacesTheSession(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ts.addAccount(t, "admin", account.RoleAdmin)
	ts.addAccount(t, "editor", account.RoleEditor)
	name := ts.wantCookieName()
	alive := func(what, token string, want bool) {
		t.Helper()
		res := ts.getSession(ts.cookie(token)...)
		if got := res.StatusCode == http.StatusOK; got != want {
			t.Errorf("%s: GET with its token has the status %d, want alive %v", what, res.StatusCode, want)
		}
	}

	// A login with exactly one cookie ends the session in it, whichever account it is of.
	first := ts.logIn(t, "admin")
	second := ts.logIn(t, "admin", ts.cookie(first)...)
	alive("the first session after a login that carried it", first, false)
	alive("the second", second, true)
	third := ts.logIn(t, "editor", ts.cookie(second)...)
	alive("the second session after a login of another account", second, false)
	alive("the third", third, true)
	if n := ts.sessionRows(t); n != 1 {
		t.Errorf("the store has %d sessions, want 1", n)
	}

	// With more than one cookie of the name the request has no previous token, so nothing ends.
	a, b := ts.logIn(t, "admin"), ts.logIn(t, "admin")
	c := ts.logIn(t, "admin", "Cookie", name+"="+a+"; "+name+"="+b)
	for what, token := range map[string]string{"a": a, "b": b, "c": c, "the editor's": third} {
		alive("the session "+what+" after a login with two cookies", token, true)
	}
	d := ts.logIn(t, "admin", "Cookie", name+"="+a, "Cookie", name+"="+b)
	alive("the session a after a login with two lines of cookies", a, true)
	alive("the session b after a login with two lines of cookies", b, true)
	alive("the session of a login with two lines of cookies", d, true)

	// A cookie that is no token, an unknown token, and a cookie of another name end nothing.
	e := ts.logIn(t, "admin", ts.cookie("abc")...)
	f := ts.logIn(t, "admin", ts.cookie(strings.Repeat("A", 43))...)
	g := ts.logIn(t, "admin", "Cookie", "other="+a)
	alive("the session a after logins with cookies that end nothing", a, true)
	for _, token := range []string{e, f, g} {
		alive("a session of a login with a cookie that ends nothing", token, true)
	}

	// A login that fails ends nothing and keeps the cookie, and so does a login that is refused.
	res := ts.postLogin("admin", "not the password of the admin", ts.cookie(a)...)
	checkProblem(t, "login with a wrong password and a session", res, "bad-credentials")
	checkNoCookie(t, "login with a wrong password and a session", res)
	alive("the session after a failed login", a, true)
	res = ts.send(http.MethodPost, "/api/session", loginBody("admin", testPassword("admin")),
		join(ts.cookie(a), []string{"Origin", "https://evil.example", "Content-Type", "application/json"})...)
	checkProblem(t, "login from another origin", res, "cross-origin")
	alive("the session after a login from another origin", a, true)
}

func TestLogout(t *testing.T) {
	for _, origin := range []string{httpsOrigin, localOrigin} {
		ts := newTestServer(t, origin)
		ts.addAccount(t, "admin", account.RoleAdmin)
		name := ts.wantCookieName()
		token := ts.logIn(t, "admin")
		spare := ts.logIn(t, "admin")

		what := origin + " logout"
		res := ts.deleteSession(ts.cookie(token)...)
		if res.StatusCode != http.StatusNoContent {
			t.Errorf("%s has the status %d, want 204", what, res.StatusCode)
		}
		if got := bodyOf(t, res); got != "" {
			t.Errorf("%s has the body %q, want none", what, got)
		}
		if got := res.Header.Values("Set-Cookie"); !equalValues(got, ts.clearLine()) {
			t.Errorf("%s sets the cookies %q, want %q", what, redacted(got), ts.clearLine())
		}
		checkHeaders(t, what, res, ts.https)
		checkProblem(t, what+", then GET", ts.getSession(ts.cookie(token)...), "unauthenticated")
		if n := ts.sessionRows(t); n != 1 {
			t.Errorf("%s: the store has %d sessions, want the other one", what, n)
		}

		// The same answer without a cookie, with a token that no session has, with a text that is no token, with an empty
		// cookie, with a cookie of another name, and again with the token that has gone.
		for _, tt := range []struct {
			what    string
			headers []string
		}{
			{"no cookie", nil},
			{"an unknown token", ts.cookie(strings.Repeat("B", 43))},
			{"a text that is no token", ts.cookie("abc")},
			{"an empty cookie", ts.cookie("")},
			{"the token that has gone", ts.cookie(token)},
			{"a cookie of another name", []string{"Cookie", "other=" + spare}},
		} {
			res := ts.deleteSession(tt.headers...)
			what := what + " with " + tt.what
			if res.StatusCode != http.StatusNoContent {
				t.Errorf("%s has the status %d, want 204", what, res.StatusCode)
			}
			if got := res.Header.Values("Set-Cookie"); !equalValues(got, ts.clearLine()) {
				t.Errorf("%s sets the cookies %q, want %q", what, redacted(got), ts.clearLine())
			}
		}
		if got := ts.getSession(ts.cookie(spare)...).StatusCode; got != http.StatusOK {
			t.Errorf("%s: a logout that carried no session has ended the other one: GET has the status %d", what, got)
		}

		// With more than one cookie of the name there is no token to end: the cookie is cleared, and the session stays.
		res = ts.deleteSession("Cookie", name+"="+spare+"; "+name+"="+token)
		if res.StatusCode != http.StatusNoContent || !equalValues(res.Header.Values("Set-Cookie"), ts.clearLine()) {
			t.Errorf("%s with two cookies has the status %d and sets %q", what, res.StatusCode, redacted(res.Header.Values("Set-Cookie")))
		}
		if got := ts.getSession(ts.cookie(spare)...).StatusCode; got != http.StatusOK {
			t.Errorf("%s with two cookies has ended the session of one of them: GET has the status %d", what, got)
		}

		res = ts.deleteSession(ts.cookie(spare)...)
		if got := ts.sessionRows(t); got != 0 || res.StatusCode != http.StatusNoContent {
			t.Errorf("%s: %d sessions left and the status %d, want none and 204", what, got, res.StatusCode)
		}
	}
}
