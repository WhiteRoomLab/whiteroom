package admin

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
)

// TestSessionCrossOrigin shows that a login and a logout from another site change nothing: no session is made, no attempt
// is counted, and no session is removed.
func TestSessionCrossOrigin(t *testing.T) {
	const wrong = "not the password of the admin"
	for _, origin := range []string{httpsOrigin, localOrigin} {
		ts := newTestServer(t, origin)
		ts.addAccount(t, "admin", account.RoleAdmin)
		ts.seen.password(wrong)
		victim := ts.logIn(t, "admin")
		ts.nothingCounted(t, "a login")
		for _, tt := range []struct {
			name    string
			headers []string
		}{
			{"cross-site", []string{"Sec-Fetch-Site", "cross-site"}},
			{"same-site", []string{"Sec-Fetch-Site", "same-site"}},
			{"cross-site and the origin of the server", []string{"Sec-Fetch-Site", "cross-site", "Origin", ts.origin}},
			{"same-site and the origin of the server", []string{"Sec-Fetch-Site", "same-site", "Origin", ts.origin}},
			{"a foreign origin", []string{"Origin", "https://evil.example"}},
			{"a null origin", []string{"Origin", "null"}},
			{"the origin of the server with a slash", []string{"Origin", ts.origin + "/"}},
			{"the origin of the server in upper case", []string{"Origin", strings.ToUpper(ts.origin)}},
			{"neither header", nil},
		} {
			for _, body := range []string{loginBody("admin", testPassword("admin")), loginBody("admin", wrong)} {
				what := origin + ", " + tt.name + ", login"
				res := ts.send(http.MethodPost, "/api/session", body,
					join(tt.headers, []string{"Content-Type", "application/json"}, ts.cookie(victim))...)
				checkProblem(t, what, res, "cross-origin")
				checkNoCookie(t, what, res)
			}
			what := origin + ", " + tt.name + ", logout"
			res := ts.send(http.MethodDelete, "/api/session", "", join(tt.headers, ts.cookie(victim))...)
			checkProblem(t, what, res, "cross-origin")
			checkNoCookie(t, what, res)
		}
		if n := ts.sessionRows(t); n != 1 {
			t.Errorf("%s: the store has %d sessions, want the one of the victim", origin, n)
		}
		if got := ts.getSession(ts.cookie(victim)...).StatusCode; got != http.StatusOK {
			t.Errorf("%s: the session of the victim has the status %d, want 200", origin, got)
		}
		ts.nothingCounted(t, "the requests of other sites")
	}
}

func TestNoSession(t *testing.T) {
	for _, origin := range []string{httpsOrigin, localOrigin} {
		ts := newTestServer(t, origin)
		ts.addAccount(t, "admin", account.RoleAdmin)
		token := ts.logIn(t, "admin")
		name := ts.wantCookieName()
		other := "session"
		if name == other {
			other = "__Host-session"
		}
		for _, tt := range []struct {
			what    string
			headers []string
			cleared bool
		}{
			{"no cookie", nil, false},
			{"a cookie of another name", []string{"Cookie", "other=" + token}, false},
			{"a cookie with the name of the other origin", []string{"Cookie", other + "=" + token}, false},
			{"a text that is no token", ts.cookie("abc"), true},
			{"an empty cookie", ts.cookie(""), true},
			{"an unknown token", ts.cookie(strings.Repeat("B", 43)), true},
			{"a token with a character more", ts.cookie(token + "A"), true},
			{"a token with a character less", ts.cookie(token[1:]), true},
			{"a token in the wrong case", ts.cookie(strings.ToUpper(token)), true},
			{"the session twice in a line", []string{"Cookie", name + "=" + token + "; " + name + "=" + token}, true},
			{"the session and another in a line", []string{"Cookie", name + "=" + token + "; " + name + "=" + strings.Repeat("B", 43)}, true},
			{"the session in two lines", []string{"Cookie", name + "=" + token, "Cookie", name + "=" + token}, true},
		} {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				what := origin + ", " + method + " with " + tt.what
				res := ts.send(method, "/api/session", "", tt.headers...)
				if method == http.MethodGet {
					checkProblem(t, what, res, "unauthenticated")
				} else {
					if res.StatusCode != http.StatusUnauthorized || !equalValues(res.Header.Values("WWW-Authenticate"), "Session") {
						t.Errorf("%s has the status %d and the WWW-Authenticate %q", what, res.StatusCode, res.Header.Values("WWW-Authenticate"))
					}
				}
				got := res.Header.Values("Set-Cookie")
				switch {
				case tt.cleared && !equalValues(got, ts.clearLine()):
					t.Errorf("%s sets the cookies %q, want %q", what, redacted(got), ts.clearLine())
				case !tt.cleared && len(got) != 0:
					t.Errorf("%s sets the cookies %q, want none", what, redacted(got))
				}
				checkHeaders(t, what, res, ts.https)
			}
		}
		// A token is read from the cookie and from nowhere else: a path and a query end up in the log of an error.
		for _, query := range []string{"?session=" + token, "?token=" + token, "?" + name + "=" + token, "?access_token=" + token} {
			checkProblem(t, origin+", GET with the token in the query "+redact(query), ts.send(http.MethodGet, "/api/session"+query, ""), "unauthenticated")
		}
		for _, header := range []string{"Authorization", "X-Session", "X-Auth-Token", "X-Csrf-Token"} {
			checkProblem(t, origin+", GET with the token in "+header, ts.send(http.MethodGet, "/api/session", "", header, token), "unauthenticated")
			checkProblem(t, origin+", GET with the token in "+header+" as a bearer", ts.send(http.MethodGet, "/api/session", "", header, "Bearer "+token), "unauthenticated")
		}
		if got := ts.getSession(ts.cookie(token)...).StatusCode; got != http.StatusOK {
			t.Errorf("%s: the session has the status %d after the requests without one, want 200", origin, got)
		}
		if n := ts.sessionRows(t); n != 1 {
			t.Errorf("%s: the store has %d sessions, want 1", origin, n)
		}
	}
}

// TestNoSessionWithoutTheStore shows that a request without a cookie, or with a cookie that is no token, is answered
// without the store, and that a token of the right form needs it.
func TestNoSessionWithoutTheStore(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ts.addAccount(t, "admin", account.RoleAdmin)
	token := ts.logIn(t, "admin")
	if err := ts.db.Close(); err != nil {
		t.Fatal(err)
	}
	checkProblem(t, "GET without a cookie", ts.getSession(), "unauthenticated")
	checkProblem(t, "GET with a text that is no token", ts.getSession(ts.cookie("abc")...), "unauthenticated")
	if got := ts.deleteSession().StatusCode; got != http.StatusNoContent {
		t.Errorf("DELETE without a cookie has the status %d, want 204", got)
	}
	if got := ts.deleteSession(ts.cookie("abc")...).StatusCode; got != http.StatusNoContent {
		t.Errorf("DELETE with a text that is no token has the status %d, want 204", got)
	}
	if got := ts.getSession(ts.cookie(token)...).StatusCode; got != http.StatusInternalServerError {
		t.Errorf("GET with a token of the right form has the status %d, want 500", got)
	}
}

func TestWithSession(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	admin := ts.addAccount(t, "admin", account.RoleAdmin)
	editor := ts.addAccount(t, "editor", account.RoleEditor)
	var calls []account.Principal
	handler := func(w http.ResponseWriter, _ *http.Request, p account.Principal) {
		calls = append(calls, p)
		writeJSON(w, http.StatusOK, p.Account.Login)
	}
	ts.handle("GET /api/me", ts.withSession(handler))
	ts.handle("PUT /api/me", ts.withSession(handler))

	// Without a session the handler is not called, and the answer is the same as GET /api/session gives.
	for _, headers := range [][]string{nil, ts.cookie("abc"), ts.cookie(strings.Repeat("B", 43))} {
		res := ts.send(http.MethodGet, "/api/me", "", headers...)
		checkProblem(t, "GET /api/me without a session", res, "unauthenticated")
		res = ts.send(http.MethodPut, "/api/me", "", join([]string{"Origin", ts.origin}, headers)...)
		checkProblem(t, "PUT /api/me without a session", res, "unauthenticated")
	}
	if len(calls) != 0 {
		t.Fatalf("the handler was called %d times without a session", len(calls))
	}

	start := ts.clock.now()
	adminToken, editorToken := ts.logIn(t, "admin"), ts.logIn(t, "editor")
	ts.clock.advance(10 * time.Minute)
	for _, tt := range []struct {
		who   account.Account
		token string
	}{{admin, adminToken}, {editor, editorToken}} {
		calls = nil
		res := ts.send(http.MethodGet, "/api/me", "", ts.cookie(tt.token)...)
		if got, want := bodyOf(t, res), `"`+tt.who.Login+`"`; res.StatusCode != http.StatusOK || got != want {
			t.Errorf("GET /api/me as %s = %d %s, want 200 %s", tt.who.Login, res.StatusCode, got, want)
		}
		if len(calls) != 1 {
			t.Fatalf("the handler was called %d times, want once", len(calls))
		}
		p := calls[0]
		if p.Account.ID != tt.who.ID || p.Account.Role != tt.who.Role || p.Account.Status != account.StatusActive {
			t.Errorf("the handler got the account %+v, want the account %s as it is", p.Account, tt.who.ID)
		}
		if !p.Session.AuthenticatedAt.Equal(start) || !p.Session.ExpiresAt.Equal(start.Add(12*time.Hour)) ||
			len(p.Session.Factors) != 1 || p.Session.Factors[0] != account.FactorPassword {
			t.Errorf("the handler got the session %+v, want one that was authenticated at %v with the password", p.Session, start)
		}
	}

	// The check of the origin is before it: a request that changes something comes from the origin of the server.
	calls = nil
	res := ts.send(http.MethodPut, "/api/me", "", ts.cookie(adminToken)...)
	checkProblem(t, "PUT /api/me from no origin", res, "cross-origin")
	if len(calls) != 0 {
		t.Errorf("the handler was called %d times for a request from no origin", len(calls))
	}
}

func TestBusy(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ts.addAccount(t, "admin", account.RoleAdmin)
	token := ts.logIn(t, "admin")
	const wrong = "not the password of the admin"
	ts.seen.password(wrong)

	ts.deadline = time.Nanosecond
	for _, password := range []string{testPassword("admin"), wrong} {
		for range 10 {
			res := ts.postLogin("admin", password)
			checkProblem(t, "login after the deadline", res, "busy")
			checkNoCookie(t, "login after the deadline", res)
		}
	}
	// The password was not compared, so the attempts have been given back.
	ts.nothingCounted(t, "the logins that were not answered in time")

	res := ts.getSession(ts.cookie(token)...)
	checkProblem(t, "GET after the deadline", res, "busy")
	checkNoCookie(t, "GET after the deadline", res)
	res = ts.deleteSession(ts.cookie(token)...)
	checkProblem(t, "DELETE after the deadline", res, "busy")
	checkNoCookie(t, "DELETE after the deadline", res)
	if got := ts.logs.String(); got != "" {
		t.Errorf("requests that were not answered in time have logged %q", redact(got))
	}

	// The session has not ended, and all five free attempts are still there.
	ts.deadline = defaultDeadline
	if got := ts.getSession(ts.cookie(token)...).StatusCode; got != http.StatusOK {
		t.Errorf("GET after the busy requests has the status %d, want 200", got)
	}
	for i := 1; i <= 5; i++ {
		checkProblem(t, "a wrong password", ts.postLogin("admin", wrong), "bad-credentials")
	}
	checkProblem(t, "the sixth wrong password", ts.postLogin("admin", wrong), "too-many-attempts")
}

// TestStoreFailure shows that a store that fails is a 500 with one line of the log and no secret, and that the attempt of a
// login that could not check the password is given back.
func TestStoreFailure(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ts.addAccount(t, "admin", account.RoleAdmin)
	token := ts.logIn(t, "admin")
	if err := ts.db.Close(); err != nil {
		t.Fatal(err)
	}

	for range 3 {
		res := ts.postLogin("admin", testPassword("admin"), ts.cookie(token)...)
		checkProblem(t, "login with a closed store", res, "internal")
		checkNoCookie(t, "login with a closed store", res)
	}
	ts.nothingCounted(t, "the logins that could not check the password")
	lines := strings.Split(strings.TrimSuffix(ts.logs.String(), "\n"), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], `POST "/api/session": `) {
		t.Errorf("three logins with a closed store have logged %q, want three lines of POST \"/api/session\": and the error", redact(ts.logs.String()))
	}

	ts.logs.Reset()
	res := ts.getSession(ts.cookie(token)...)
	checkProblem(t, "GET with a closed store", res, "internal")
	checkNoCookie(t, "GET with a closed store", res)
	if got := ts.logs.String(); !strings.HasPrefix(got, `GET "/api/session": `) || strings.Count(got, "\n") != 1 {
		t.Errorf("GET with a closed store has logged %q, want one line of GET \"/api/session\": and the error", redact(got))
	}

	ts.logs.Reset()
	res = ts.deleteSession(ts.cookie(token)...)
	checkProblem(t, "DELETE with a closed store", res, "internal")
	checkNoCookie(t, "DELETE with a closed store", res)
	if got := ts.logs.String(); !strings.HasPrefix(got, `DELETE "/api/session": `) || strings.Count(got, "\n") != 1 {
		t.Errorf("DELETE with a closed store has logged %q, want one line of DELETE \"/api/session\": and the error", redact(got))
	}
}
