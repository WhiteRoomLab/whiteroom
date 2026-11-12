package admin

import (
	"maps"
	"net/http"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// sessionData returns the rows of the table sessions as the store has them, by key.
func (ts *testServer) sessionData(t *testing.T) map[string]string {
	t.Helper()
	rows := make(map[string]string)
	err := ts.db.View(t.Context(), func(tx *store.Tx) error {
		stored, err := tx.Sessions()
		for _, s := range stored {
			rows[s.Key] = string(s.Data)
		}
		return err
	})
	if err != nil {
		t.Fatalf("Sessions() = %v", err)
	}
	return rows
}

// TestSessionFromAnotherSite shows that a page of another site cannot touch a session: a request that Sec-Fetch-Site says
// is from the same site, or from another, is refused before the cookie is read. It does not keep a session from going idle,
// as a request of the user does, and it does not remove a session that has ended.
func TestSessionFromAnotherSite(t *testing.T) {
	for _, origin := range []string{httpsOrigin, localOrigin} {
		ts := newTestServer(t, origin)
		admin := ts.addAccount(t, "admin", account.RoleAdmin)
		token := ts.logIn(t, "admin")
		start := ts.clock.now()
		rows := ts.sessionData(t)

		// Every value but the two that a request of the user has, and no value at all.
		for _, site := range []string{"same-site", "cross-site", "Same-Origin", "NONE", "other", "same-origin, cross-site"} {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				what := origin + ", " + method + " with Sec-Fetch-Site: " + site
				res := ts.send(method, "/api/session", "", join([]string{"Sec-Fetch-Site", site}, ts.cookie(token))...)
				if method == http.MethodGet {
					checkProblem(t, what, res, "cross-origin")
				} else if res.StatusCode != http.StatusForbidden {
					t.Errorf("%s has the status %d, want 403", what, res.StatusCode)
				}
				checkNoCookie(t, what, res)
				checkHeaders(t, what, res, ts.https)
			}
		}
		for _, site := range []string{"same-origin", "none", ""} {
			res := ts.send(http.MethodGet, "/api/session", "", join([]string{"Sec-Fetch-Site", site}, ts.cookie(token))...)
			ts.checkAnswer(t, origin+", GET with Sec-Fetch-Site: "+site, res, admin, start, "")
		}
		if got := ts.sessionData(t); !maps.Equal(got, rows) {
			t.Errorf("%s: the rows of the sessions changed", origin)
		}

		// A page of the same site that asks every five minutes does not keep the session alive: it ends an hour after the last
		// request of the user, which was at the start.
		for range 13 {
			ts.clock.advance(5 * time.Minute)
			res := ts.send(http.MethodGet, "/api/session", "", join([]string{"Sec-Fetch-Site", "same-site"}, ts.cookie(token))...)
			checkProblem(t, origin+", GET from a page of the same site", res, "cross-origin")
			checkNoCookie(t, origin+", GET from a page of the same site", res)
		}
		if got := ts.sessionData(t); !maps.Equal(got, rows) {
			t.Errorf("%s: the rows of the sessions changed after the requests of a page of the same site", origin)
		}
		// The session has ended, and the request of a page of the same site does not remove it or clear the cookie; the
		// request of the user does both.
		res := ts.send(http.MethodGet, "/api/session", "", join([]string{"Sec-Fetch-Site", "cross-site"}, ts.cookie(token))...)
		checkProblem(t, origin+", GET from another site after the session has ended", res, "cross-origin")
		checkNoCookie(t, origin+", GET from another site after the session has ended", res)
		if n := ts.sessionRows(t); n != 1 {
			t.Errorf("%s: the store has %d sessions after the request from another site, want the one that has ended", origin, n)
		}
		res = ts.getSession(ts.cookie(token)...)
		checkProblem(t, origin+", GET of the user after 65 minutes", res, "unauthenticated")
		if got := res.Header.Values("Set-Cookie"); !equalValues(got, ts.clearLine()) {
			t.Errorf("%s: GET of the user after 65 minutes sets the cookies %q, want %q", origin, redacted(got), ts.clearLine())
		}
		if n := ts.sessionRows(t); n != 0 {
			t.Errorf("%s: the store has %d sessions after the request of the user, want none", origin, n)
		}
	}
}

// TestWithSessionFromAnotherSite shows the same for a route of the admin API that goes through withSession: its handler is
// not called.
func TestWithSessionFromAnotherSite(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ts.addAccount(t, "admin", account.RoleAdmin)
	token := ts.logIn(t, "admin")
	calls := 0
	ts.handle("GET /api/me", ts.withSession(func(w http.ResponseWriter, _ *http.Request, _ account.Principal) {
		calls++
		writeJSON(w, http.StatusOK, "me")
	}))
	for _, site := range []string{"same-site", "cross-site"} {
		checkProblem(t, "GET /api/me with Sec-Fetch-Site: "+site, ts.send(http.MethodGet, "/api/me", "", join([]string{"Sec-Fetch-Site", site}, ts.cookie(token))...), "cross-origin")
		// Without a session the answer is the same: the site is checked first.
		checkProblem(t, "GET /api/me with Sec-Fetch-Site: "+site+" and no session", ts.send(http.MethodGet, "/api/me", "", "Sec-Fetch-Site", site), "cross-origin")
	}
	if calls != 0 {
		t.Fatalf("the handler was called %d times for requests from another site", calls)
	}
	for _, site := range []string{"same-origin", "none"} {
		if res := ts.send(http.MethodGet, "/api/me", "", join([]string{"Sec-Fetch-Site", site}, ts.cookie(token))...); res.StatusCode != http.StatusOK {
			t.Errorf("GET /api/me with Sec-Fetch-Site: %s has the status %d, want 200", site, res.StatusCode)
		}
	}
	if calls != 2 {
		t.Errorf("the handler was called %d times for requests from this origin, want 2", calls)
	}
}
