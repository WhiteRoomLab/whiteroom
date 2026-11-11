package admin

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
)

// reach says which sessions an ending ends, out of the session that is used for it, another session of the same account,
// and a session of another account.
type reach int

const (
	reachSession reach = iota // only the session that logs out
	reachAccount              // every session of the account
	reachTime                 // every session, since the time has passed
)

// ending is a way for a session to end.
type ending struct {
	name  string
	reach reach
	// end ends the session of a with the token; it returns the token of a session that goes on in its place, or "".
	end func(t *testing.T, ts *testServer, admin, a account.Account, token string) string
	// then checks what a login does afterwards, if it is more than usual.
	then func(t *testing.T, ts *testServer, a account.Account)
}

func mustNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

var endings = []ending{
	{name: "logout", reach: reachSession, end: func(t *testing.T, ts *testServer, _, _ account.Account, token string) string {
		if res := ts.deleteSession(ts.cookie(token)...); res.StatusCode != http.StatusNoContent {
			t.Errorf("logout has the status %d, want 204", res.StatusCode)
		}
		return ""
	}},
	{name: "an hour without a request", reach: reachTime, end: func(_ *testing.T, ts *testServer, _, _ account.Account, _ string) string {
		ts.clock.advance(account.SessionIdleTimeout)
		return ""
	}},
	{name: "twelve hours since the login, with a request every half hour", reach: reachTime,
		end: func(t *testing.T, ts *testServer, _, _ account.Account, token string) string {
			for i := 1; i < 24; i++ {
				ts.clock.advance(30 * time.Minute)
				if res := ts.getSession(ts.cookie(token)...); res.StatusCode != http.StatusOK {
					t.Fatalf("the session has ended after %d half hours, with a request in each", i)
				}
			}
			ts.clock.advance(30 * time.Minute)
			return ""
		}},
	{name: "Disable", reach: reachAccount, end: func(t *testing.T, ts *testServer, admin, a account.Account, _ string) string {
		_, err := ts.accounts.Disable(t.Context(), account.By(admin.ID), a.ID)
		mustNoErr(t, err)
		return ""
	}, then: func(t *testing.T, ts *testServer, a account.Account) {
		checkProblem(t, "login of a disabled account", ts.postLogin(a.Login, testPassword(a.Login)), "bad-credentials")
	}},
	{name: "Disable and Enable", reach: reachAccount, end: func(t *testing.T, ts *testServer, admin, a account.Account, _ string) string {
		_, err := ts.accounts.Disable(t.Context(), account.By(admin.ID), a.ID)
		mustNoErr(t, err)
		_, err = ts.accounts.Enable(t.Context(), account.By(admin.ID), a.ID)
		mustNoErr(t, err)
		return ""
	}, then: func(t *testing.T, ts *testServer, a account.Account) {
		if got := ts.postLogin(a.Login, testPassword(a.Login)).StatusCode; got != http.StatusOK {
			t.Errorf("login after Enable has the status %d, want 200", got)
		}
	}},
	{name: "Delete", reach: reachAccount, end: func(t *testing.T, ts *testServer, admin, a account.Account, _ string) string {
		mustNoErr(t, ts.accounts.Delete(t.Context(), account.By(admin.ID), a.ID))
		return ""
	}, then: func(t *testing.T, ts *testServer, a account.Account) {
		checkProblem(t, "login of a deleted account", ts.postLogin(a.Login, testPassword(a.Login)), "bad-credentials")
	}},
	{name: "a change of the role", reach: reachAccount, end: func(t *testing.T, ts *testServer, admin, a account.Account, _ string) string {
		role := account.RoleViewer
		_, err := ts.accounts.Update(t.Context(), account.By(admin.ID), a.ID, account.Change{Role: &role})
		mustNoErr(t, err)
		return ""
	}, then: func(t *testing.T, ts *testServer, a account.Account) {
		res := ts.postLogin(a.Login, testPassword(a.Login))
		if body := bodyOf(t, res); !strings.Contains(body, `"role":"viewer"`) {
			t.Errorf("login after a change of the role answers %s, want the role viewer", body)
		}
	}},
	{name: "ChangePassword", reach: reachAccount, end: func(t *testing.T, ts *testServer, _, a account.Account, _ string) string {
		next := "another long password for " + a.Login
		ts.seen.password(next)
		mustNoErr(t, ts.accounts.ChangePassword(t.Context(), a.ID, testPassword(a.Login), next))
		return ""
	}, then: func(t *testing.T, ts *testServer, a account.Account) {
		checkProblem(t, "login with the old password", ts.postLogin(a.Login, testPassword(a.Login)), "bad-credentials")
		if got := ts.postLogin(a.Login, "another long password for "+a.Login).StatusCode; got != http.StatusOK {
			t.Errorf("login with the new password has the status %d, want 200", got)
		}
	}},
	{name: "ChangeOwnPassword", reach: reachAccount, end: func(t *testing.T, ts *testServer, _, a account.Account, token string) string {
		next := "another long password for " + a.Login
		ts.seen.password(next)
		_, fresh, err := ts.accounts.ChangeOwnPassword(t.Context(), account.Client{}, token, testPassword(a.Login), next)
		mustNoErr(t, err)
		return fresh
	}},
	{name: "Reset", reach: reachAccount, end: func(t *testing.T, ts *testServer, admin, a account.Account, _ string) string {
		_, err := ts.accounts.Reset(t.Context(), account.By(admin.ID), a.ID)
		mustNoErr(t, err)
		return ""
	}, then: func(t *testing.T, ts *testServer, a account.Account) {
		checkProblem(t, "login after Reset", ts.postLogin(a.Login, testPassword(a.Login)), "bad-credentials")
	}},
}

func TestSessionEnds(t *testing.T) {
	for _, origin := range []string{httpsOrigin, localOrigin} {
		ts := newTestServer(t, origin)
		admin := ts.addAccount(t, "admin", account.RoleAdmin)
		for i, e := range endings {
			what := origin + ", after " + e.name
			login := fmt.Sprintf("user%d", i)
			a := ts.addAccount(t, login, account.RoleEditor)
			token, other := ts.logIn(t, login), ts.logIn(t, login)
			bystander := ts.addAccount(t, login+"x", account.RoleEditor)
			bystanderToken := ts.logIn(t, bystander.Login)
			for _, tok := range []string{token, other, bystanderToken} {
				if got := ts.getSession(ts.cookie(tok)...).StatusCode; got != http.StatusOK {
					t.Fatalf("%s: GET before has the status %d, want 200", what, got)
				}
			}

			works := e.end(t, ts, admin, a, token)

			res := ts.getSession(ts.cookie(token)...)
			checkProblem(t, what+", GET with the cookie", res, "unauthenticated")
			if got := res.Header.Values("Set-Cookie"); !equalValues(got, ts.clearLine()) {
				t.Errorf("%s: GET with the cookie sets %q, want %q", what, redacted(got), ts.clearLine())
			}
			for who, tok := range map[string]struct {
				token string
				ends  bool
			}{
				"another session of the account": {other, e.reach != reachSession},
				"the session of another account": {bystanderToken, e.reach == reachTime},
			} {
				res := ts.getSession(ts.cookie(tok.token)...)
				if tok.ends {
					checkProblem(t, what+", GET with "+who, res, "unauthenticated")
				} else if res.StatusCode != http.StatusOK {
					t.Errorf("%s: GET with %s has the status %d, want 200", what, who, res.StatusCode)
				}
			}
			if works != "" {
				if got := ts.getSession(ts.cookie(works)...).StatusCode; got != http.StatusOK {
					t.Errorf("%s: GET with the session that goes on has the status %d, want 200", what, got)
				}
			}
			if e.then != nil {
				e.then(t, ts, a)
			}
		}
	}
}

// TestSessionLasts shows the times at which a session does not end yet.
func TestSessionLasts(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	admin := ts.addAccount(t, "admin", account.RoleAdmin)
	token := ts.logIn(t, "admin")
	get := func(what string, wantOK bool) {
		t.Helper()
		res := ts.getSession(ts.cookie(token)...)
		if got := res.StatusCode == http.StatusOK; got != wantOK {
			t.Errorf("%s: GET has the status %d, want alive %v", what, res.StatusCode, wantOK)
		}
	}

	// A request keeps the session alive for another hour, however long ago the login was.
	ts.clock.advance(time.Hour - 1)
	get("just under an hour after the login", true)
	ts.clock.advance(time.Hour - 1)
	get("just under an hour after the request", true)
	ts.clock.advance(time.Hour)
	get("an hour after the request", false)

	// A change of the name keeps the session.
	token = ts.logIn(t, "admin")
	name := "Another name"
	if _, err := ts.accounts.Update(t.Context(), account.By(admin.ID), admin.ID, account.Change{Name: &name}); err != nil {
		t.Fatal(err)
	}
	get("after a change of the name", true)

	// The session lasts 12 hours from the login, and the answer says when it ends.
	started := ts.clock.now()
	for i := 1; i <= 22; i++ {
		ts.clock.advance(30 * time.Minute)
		get(fmt.Sprintf("%d half hours after the login", i), true)
	}
	ts.clock.advance(time.Hour - 1)
	res := ts.getSession(ts.cookie(token)...)
	if res.StatusCode != http.StatusOK {
		t.Errorf("just under 12 hours after the login: GET has the status %d, want 200", res.StatusCode)
	}
	if body := bodyOf(t, res); !strings.Contains(body, `"expiresAt":"`+started.Add(12*time.Hour).UTC().Format(time.RFC3339Nano)+`"`) {
		t.Errorf("the answer %s does not end at 12 hours after the login", body)
	}
	ts.clock.advance(1)
	get("12 hours after the login", false)
}
