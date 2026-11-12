package admin

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
)

const wrongCurrent = "this is not the password of the account"

func passwordBody(current, next string) string {
	return `{"current":` + quote(current) + `,"password":` + quote(next) + `}`
}

// putPassword sends PUT /api/session/password with the cookie of the session that has the token. The passwords are watched for.
func (a *apiServer) putPassword(token, current, next string, headers ...string) *http.Response {
	a.seen.password(current)
	a.seen.password(next)
	return asPerson{a: a, token: token}.send(http.MethodPut, "/api/session/password", passwordBody(current, next), headers...)
}

// wantWrongCurrent checks that res is the 422 that tells that the current password is wrong, with no cookie.
func wantWrongCurrent(t *testing.T, what string, res *http.Response) {
	t.Helper()
	p := readProblem(t, res)
	if res.StatusCode != http.StatusUnprocessableEntity || p.Code != "invalid" || len(p.Errors) != 1 || p.Errors[0].Pointer != "/current" ||
		p.Errors[0].Detail != "wrong password" || p.Errors[0].File != "" || p.Errors[0].Line != 0 {
		t.Errorf("%s has the status %d and %+v, want the problem invalid with the one error at /current", what, res.StatusCode, p)
	}
	checkNoCookie(t, what, res)
}

// wantWait checks that res is the 429 of a session that has to wait, with the Retry-After.
func wantWait(t *testing.T, what string, res *http.Response, retry string) {
	t.Helper()
	if got := retryAfter(t, what, res); got != retry {
		t.Errorf("%s has the Retry-After %q, want %q", what, got, retry)
	}
	checkNoCookie(t, what, res)
}

// attemptsOf returns how many attempts the table of the changes of passwords keeps count of for the session with the token, and
// how many sessions it keeps count of.
func (a *apiServer) attemptsOf(token string) (count, sessions int) {
	a.passwords.mu.Lock()
	defer a.passwords.mu.Unlock()
	if e := a.passwords.sessions.entries[sha256.Sum256([]byte(token))]; e != nil {
		count = int(e.count)
	}
	return count, len(a.passwords.sessions.entries)
}

// loginRecords returns the records of the login history whose action is the one and whose account is the one.
func (a *apiServer) loginRecords(t *testing.T, action audit.Action, id string) []audit.Record {
	t.Helper()
	var out []audit.Record
	f := audit.Filter{Log: audit.LoginLog, Action: string(action), Account: id}
	if err := a.db.ReadAudit(t.Context(), f, func(r audit.Record) bool { out = append(out, r); return true }); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestChangeOwnPassword shows the change of one's own password: the new session replaces the old one in the cookie, every
// other session of the account ends, and the old password is the old one no more.
func TestChangeOwnPassword(t *testing.T) {
	a := newAPIServer(t)
	for _, login := range []string{"editor", "viewer"} { // every role may
		t.Run(login, func(t *testing.T) {
			p := a.people[login]
			id := p.account.ID
			second := a.logIn(t, login)
			next := login + " has a new long password"
			a.clock.advance(time.Minute)

			res := a.putPassword(p.token, testPassword(login), next)
			fresh := a.tokenOf(t, res)
			acct, err := a.accounts.Get(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			a.checkAnswer(t, "PUT /api/session/password", res, acct, a.clock.now(), anyLoginCookie)
			if fresh == p.token || fresh == second {
				t.Error("the new session has the token of an old one")
			}
			a.checkAnswer(t, "GET /api/session with the new token", a.getSession(a.cookie(fresh)...), acct, a.clock.now(), "")
			for what, token := range map[string]string{"the session that asked": p.token, "the other session": second} {
				res := a.getSession(a.cookie(token)...)
				checkProblem(t, "GET /api/session with "+what, res, "unauthenticated")
			}
			checkProblem(t, "login with the old password", a.postLogin(login, testPassword(login)), "bad-credentials")
			a.seen.password(next)
			if got := a.postLogin(login, next); got.StatusCode != http.StatusOK {
				t.Errorf("login with the new password has the status %d, want 200", got.StatusCode)
			}
			if r := a.lastRecord(t, audit.AccountChangePassword); r.Actor != id || r.Target.ID != id {
				t.Errorf("the last record of account.changePassword is %+v, want the account's own", r)
			}
			if got := a.loginRecords(t, audit.SessionStart, id); len(got) != 1 {
				t.Errorf("%d records of session.start, want the one of the new session", len(got))
			}
			ended := 0
			for _, r := range a.loginRecords(t, audit.SessionEnd, id) {
				if r.Result == "password" {
					ended++
				}
			}
			if ended != 2 {
				t.Errorf("%d sessions ended for the password, want the 2 that were there", ended)
			}
			if count, sessions := a.attemptsOf(p.token); count != 0 || sessions != 0 {
				t.Errorf("the table keeps %d attempts of the old session and %d sessions, want nothing: a right password is forgotten", count, sessions)
			}
			p.token = fresh
		})
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestChangeOwnPasswordAttempts shows the limit of the wrong current passwords of a session: five are free, and then the
// session waits, 30 seconds at first and twice as long after each, and the wait is told before the account is asked.
func TestChangeOwnPasswordAttempts(t *testing.T) {
	a := newAPIServer(t)
	p := a.people["editor"]
	const next = "editor has a new long password"

	for i := 1; i <= freeAttempts; i++ {
		wantWrongCurrent(t, fmt.Sprintf("wrong current password %d", i), a.putPassword(p.token, wrongCurrent, next))
	}
	if count, sessions := a.attemptsOf(p.token); count != freeAttempts || sessions != 1 {
		t.Errorf("the table keeps %d attempts of the session in %d sessions, want %d in 1", count, sessions, freeAttempts)
	}
	// The sixth waits, whatever it brings: the right password is not tried, so it does not change the password.
	wantWait(t, "the sixth attempt", a.putPassword(p.token, wrongCurrent, next), "30")
	wantWait(t, "the sixth attempt with the right password", a.putPassword(p.token, testPassword("editor"), next), "30")
	a.clock.advance(29 * time.Second)
	wantWait(t, "the attempt a second before the end of the wait", a.putPassword(p.token, testPassword("editor"), next), "1")
	if got := a.postLogin("editor", testPassword("editor")); got.StatusCode != http.StatusOK {
		t.Errorf("login with the password after the refused attempts has the status %d, want 200: it was not changed", got.StatusCode)
	}
	body(t, "GET /api/session of the session that waits", a.getSession(a.cookie(p.token)...), http.StatusOK)

	// After the wait it may try again, and a wrong password makes the wait twice as long.
	a.clock.advance(time.Second)
	wantWrongCurrent(t, "the sixth attempt after the wait", a.putPassword(p.token, wrongCurrent, next))
	wantWait(t, "the seventh attempt", a.putPassword(p.token, wrongCurrent, next), "60")
	a.clock.advance(59 * time.Second)
	wantWait(t, "the seventh attempt a second before the end", a.putPassword(p.token, wrongCurrent, next), "1")
	a.clock.advance(time.Second)

	// The right password is a change, and the count is forgotten.
	res := a.putPassword(p.token, testPassword("editor"), next)
	fresh := a.tokenOf(t, res)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the right password after the wait has the status %d, want 200", res.StatusCode)
	}
	if count, sessions := a.attemptsOf(p.token); count != 0 || sessions != 0 {
		t.Errorf("the table keeps %d attempts and %d sessions after the change, want nothing", count, sessions)
	}
	// The new session starts with its five free attempts.
	for i := 1; i <= freeAttempts; i++ {
		wantWrongCurrent(t, fmt.Sprintf("wrong current password %d of the new session", i), a.putPassword(fresh, wrongCurrent, next))
	}
	wantWait(t, "the sixth attempt of the new session", a.putPassword(fresh, next, next+" again"), "30")
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestChangeOwnPasswordPerSession shows that the limit is the session's: another session of the account does not wait, nor
// does the session of another account, and nothing of a session is kept but the hash of its token.
func TestChangeOwnPasswordPerSession(t *testing.T) {
	a := newAPIServer(t)
	first, admin := a.people["editor"].token, a.people["admin"].token
	second := a.logIn(t, "editor")
	const next = "editor has a new long password"
	for range freeAttempts {
		wantWrongCurrent(t, "a wrong current password", a.putPassword(first, wrongCurrent, next))
	}
	wantWait(t, "the sixth attempt of the first session", a.putPassword(first, wrongCurrent, next), "30")
	wantWrongCurrent(t, "an attempt of the other session of the account", a.putPassword(second, wrongCurrent, next))
	wantWrongCurrent(t, "an attempt of the session of another account", a.putPassword(admin, wrongCurrent, next))
	for token, want := range map[string]int{first: freeAttempts, second: 1, admin: 1} {
		if count, sessions := a.attemptsOf(token); count != want || sessions != 3 {
			t.Errorf("the table keeps %d attempts of a session in %d sessions, want %d in 3", count, sessions, want)
		}
	}
	// The other session changes the password, which ends them all, the one that waits too: the wait is of no more use.
	body(t, "the other session changes the password", a.putPassword(second, testPassword("editor"), next), http.StatusOK)
	checkProblem(t, "the session that waited", a.getSession(a.cookie(first)...), "unauthenticated")
	if count, _ := a.attemptsOf(second); count != 0 {
		t.Errorf("the table keeps %d attempts of the session that changed the password, want none", count)
	}
}

// TestChangeOwnPasswordBounds shows what counts as an attempt: a password that is not compared, because it is longer than a
// password can be, does not, and neither does a change that fails for another reason than the current password.
func TestChangeOwnPasswordBounds(t *testing.T) {
	a := newAPIServer(t)
	p := a.people["editor"]
	const next = "editor has a new long password"

	// More than 8 KiB is a wrong password, said at once, and counted as nothing.
	long := strings.Repeat("p", 8193)
	for range 10 {
		wantWrongCurrent(t, "a current password of 8193 bytes", a.putPassword(p.token, long, next))
	}
	if count, sessions := a.attemptsOf(p.token); count != 0 || sessions != 0 {
		t.Errorf("the table keeps %d attempts and %d sessions after passwords of 8193 bytes, want nothing", count, sessions)
	}
	// 8192 bytes are compared, and are an attempt.
	wantWrongCurrent(t, "a current password of 8192 bytes", a.putPassword(p.token, strings.Repeat("p", 8192), next))
	if count, _ := a.attemptsOf(p.token); count != 1 {
		t.Errorf("the table keeps %d attempts after a password of 8192 bytes, want 1", count)
	}
	// A new password that the rules refuse is the problem of the field password, if the current one is right, and it is no
	// attempt; if the current one is wrong, that is what is told.
	res := a.putPassword(p.token, testPassword("editor"), tinyPass)
	pr := readProblem(t, res)
	if res.StatusCode != http.StatusUnprocessableEntity || pr.Code != "invalid" || len(pr.Errors) != 1 || pr.Errors[0].Pointer != "/password" ||
		strings.Contains(pr.Errors[0].Detail, tinyPass) || strings.Contains(pr.Errors[0].Detail, testPassword("editor")) {
		t.Errorf("a new password that is too short has the status %d and %+v, want the problem invalid at /password", res.StatusCode, pr)
	}
	checkNoCookie(t, "a new password that is too short", res)
	if count, _ := a.attemptsOf(p.token); count != 1 {
		t.Errorf("the table keeps %d attempts after a new password that is refused, want the 1 it had: the attempt is given back", count)
	}
	wantWrongCurrent(t, "a wrong current password with a new one that is refused", a.putPassword(p.token, wrongCurrent, tinyPass))
	if count, _ := a.attemptsOf(p.token); count != 2 {
		t.Errorf("the table keeps %d attempts, want 2", count)
	}

	// What is not the body of a change is refused before anything is counted.
	good := passwordBody(testPassword("editor"), next)
	as := asPerson{a: a, token: p.token}
	for _, tt := range []struct{ name, body, code string }{
		{"no current password", `{"password":"` + next + `"}`, "bad-request"},
		{"no new password", `{"current":"` + wrongCurrent + `"}`, "bad-request"},
		{"null for the current password", `{"current":null,"password":"` + next + `"}`, "bad-request"},
		{"a number for the new password", `{"current":"` + wrongCurrent + `","password":5}`, "bad-request"},
		{"another member", `{"current":"` + wrongCurrent + `","password":"` + next + `","token":"x"}`, "bad-request"},
		{"null", `null`, "bad-request"},
		{"an array", `[]`, "bad-request"},
		{"a body that is too large", `{"current":"` + wrongCurrent + `","password":"` + strings.Repeat("p", smallBodyLimit) + `"}`, "too-large"},
	} {
		a.seen.password(wrongCurrent)
		checkProblem(t, "PUT with "+tt.name, as.send(http.MethodPut, "/api/session/password", tt.body), tt.code)
	}
	checkProblem(t, "PUT of another media type", as.send(http.MethodPut, "/api/session/password", good, "Content-Type", "text/plain"), "unsupported-media-type")
	checkProblem(t, "PUT with a key", as.send(http.MethodPut, "/api/session/password", good, key(1)...), "bad-header")
	checkProblem(t, "PUT with a query", as.send(http.MethodPut, "/api/session/password?x=1", good), "bad-query")
	checkProblem(t, "POST", as.post("/api/session/password", good), "method-not-allowed")
	if count, _ := a.attemptsOf(p.token); count != 2 {
		t.Errorf("the table keeps %d attempts after those, want the 2 it had", count)
	}

	// No session is a login to ask for, and the cookie of a session that is gone is cleared.
	checkProblem(t, "PUT without a cookie", a.send(http.MethodPut, "/api/session/password", good, "Origin", a.origin, "Content-Type", "application/json"), "unauthenticated")
	res = asPerson{a: a, token: strings.Repeat("Z", 43)}.send(http.MethodPut, "/api/session/password", good)
	checkProblem(t, "PUT with a cookie that is no session", res, "unauthenticated")
	if got := res.Header.Values("Set-Cookie"); !equalValues(got, a.clearLine()) {
		t.Errorf("PUT with a cookie that is no session sets the cookies %q, want the one that clears it", got)
	}
	checkProblem(t, "PUT from another site", as.send(http.MethodPut, "/api/session/password", good, "Sec-Fetch-Site", "cross-site"), "cross-origin")
	checkProblem(t, "PUT with the origin of another site", a.send(http.MethodPut, "/api/session/password", good, join(a.cookie(p.token), []string{
		"Origin", "https://evil.example", "Content-Type", "application/json"})...), "cross-origin")
	if _, sessions := a.attemptsOf(p.token); sessions != 1 {
		t.Errorf("the table keeps %d sessions, want only the one", sessions)
	}
	if got := a.postLogin("editor", testPassword("editor")); got.StatusCode != http.StatusOK {
		t.Errorf("login with the password after those has the status %d, want 200: it was not changed", got.StatusCode)
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestChangeOwnPasswordGivesAttemptBack shows that an attempt which did not compare the password is given back: the end of the
// request, and a session that ended while the request waited.
func TestChangeOwnPasswordGivesAttemptBack(t *testing.T) {
	a := newAPIServer(t)
	p := a.people["editor"]
	const next = "editor has a new long password"
	headers := join(a.cookie(p.token), []string{"Origin", a.origin, "Content-Type", "application/json"})

	// The context ends after the attempt was counted and before the account was asked.
	ctx, cancel := context.WithCancel(t.Context())
	a.hook = func(point string) {
		if point == "checked" {
			cancel()
		}
	}
	a.seen.password(wrongCurrent)
	a.seen.password(next)
	res := a.sendWithContext(ctx, http.MethodPut, "/api/session/password", passwordBody(wrongCurrent, next), headers...)
	a.hook = nil
	checkProblem(t, "PUT of a request that ended", res, "busy")
	if count, sessions := a.attemptsOf(p.token); count != 0 || sessions != 0 {
		t.Errorf("the table keeps %d attempts and %d sessions after a request that ended, want nothing", count, sessions)
	}

	// The session ends while the request waits: it is not a session any more.
	a.hook = func(point string) {
		if point == "checked" {
			a.deleteSession(a.cookie(p.token)...)
		}
	}
	res = a.putPassword(p.token, wrongCurrent, next)
	a.hook = nil
	checkProblem(t, "PUT of a session that ended meanwhile", res, "unauthenticated")
	if got := res.Header.Values("Set-Cookie"); !equalValues(got, a.clearLine()) {
		t.Errorf("PUT of a session that ended meanwhile sets the cookies %q, want the one that clears it", got)
	}
	if count, sessions := a.attemptsOf(p.token); count != 0 || sessions != 0 {
		t.Errorf("the table keeps %d attempts and %d sessions after a session that ended, want nothing", count, sessions)
	}
	if got := a.postLogin("editor", testPassword("editor")); got.StatusCode != http.StatusOK {
		t.Errorf("login with the password has the status %d, want 200: it was not changed", got.StatusCode)
	}
}

// TestChangeOwnPasswordStoreFails shows that a failure of the store while the password is changed is a 500 that shows no
// password in the answer or in the log, and gives the attempt back.
func TestChangeOwnPasswordStoreFails(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ts.addAccount(t, "editor", account.RoleEditor)
	token := ts.logIn(t, "editor")
	const next = "editor has a new long password"
	ts.seen.password(next)
	ts.seen.password(wrongCurrent)
	ts.hook = func(point string) {
		if point == "checked" {
			_ = ts.db.Close()
		}
	}
	res := ts.send(http.MethodPut, "/api/session/password", passwordBody(wrongCurrent, next),
		join(ts.cookie(token), []string{"Origin", ts.origin, "Content-Type", "application/json"})...)
	ts.hook = nil
	checkProblem(t, "PUT with a store that fails", res, "internal")
	ts.passwords.mu.Lock()
	kept := len(ts.passwords.sessions.entries)
	ts.passwords.mu.Unlock()
	if kept != 0 {
		t.Errorf("the table keeps %d sessions after a failure of the store, want none", kept)
	}
	if logged := ts.logs.String(); !strings.Contains(logged, "/api/session/password") {
		t.Errorf("the log is %q, want the line of the failure", logged)
	}
}

// TestPasswordAttempts shows the table of the attempts by itself: the waits, the forgetting, the giving back, and what is
// forgotten when it is full.
func TestPasswordAttempts(t *testing.T) {
	key := func(n byte) [32]byte { return [32]byte{n} }
	newTable := func(capacity int) (*passwordAttempts, *fakeClock) {
		clock := newFakeClock()
		return newPasswordAttempts(clock.now, capacity), clock
	}
	held := func(a *passwordAttempts, n byte) bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.sessions.entries[key(n)] != nil
	}

	t.Run("the waits", func(t *testing.T) {
		a, clock := newTable(10)
		for i := 1; i <= freeAttempts; i++ {
			if retry, ok := a.begin(key(1)); !ok || retry != 0 {
				t.Fatalf("attempt %d waits %v, want no wait", i, retry)
			}
		}
		for _, want := range []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute,
			32 * time.Minute, time.Hour, time.Hour} {
			if retry, ok := a.begin(key(1)); ok || retry != want {
				t.Fatalf("the attempt waits %v (allowed %v), want %v", retry, ok, want)
			}
			clock.advance(want - 500*time.Millisecond)
			if retry, ok := a.begin(key(1)); ok || retry != time.Second {
				t.Fatalf("the attempt half a second before the end waits %v (allowed %v), want the second that rounds it up", retry, ok)
			}
			clock.advance(500 * time.Millisecond)
			if retry, ok := a.begin(key(1)); !ok || retry != 0 {
				t.Fatalf("the attempt after the wait of %v waits %v", want, retry)
			}
		}
	})

	t.Run("forgetting", func(t *testing.T) {
		a, clock := newTable(10)
		for range freeAttempts {
			a.begin(key(1))
		}
		if _, ok := a.begin(key(1)); ok {
			t.Fatal("the sixth attempt does not wait")
		}
		clock.advance(pairForget - time.Second)
		if _, ok := a.begin(key(1)); !ok {
			t.Fatal("the attempt a day after the last one waits")
		}
		if e := a.sessions.entries[key(1)]; e == nil || e.count != freeAttempts+1 {
			t.Errorf("the count is %+v after the wait, want %d", e, freeAttempts+1)
		}
		clock.advance(pairForget)
		a.begin(key(1))
		if e := a.sessions.entries[key(1)]; e == nil || e.count != 1 {
			t.Errorf("the count is %+v a day after the last attempt, want 1", e)
		}
		a.succeeded(key(1))
		a.succeeded(key(2)) // none
		if held(a, 1) {
			t.Error("the session is kept after it succeeded")
		}
	})

	t.Run("giving back", func(t *testing.T) {
		a, _ := newTable(10)
		a.aborted(key(1)) // none
		for range freeAttempts {
			a.begin(key(1))
		}
		a.aborted(key(1))
		if retry, ok := a.begin(key(1)); !ok {
			t.Fatalf("the attempt that was given back is still counted: the next waits %v", retry)
		}
		if _, ok := a.begin(key(1)); ok {
			t.Error("the attempt after five does not wait, though one was given back and taken again")
		}
		b, _ := newTable(10)
		b.begin(key(2))
		b.aborted(key(2))
		if held(b, 2) {
			t.Error("a session with no attempt is kept")
		}
	})

	t.Run("a full table", func(t *testing.T) {
		a, clock := newTable(3)
		wait := func(n byte) { // five attempts, which makes the next one wait
			for range freeAttempts {
				a.begin(key(n))
			}
			clock.advance(time.Second)
		}
		wait(1)
		wait(2)
		wait(3)
		// All three wait. The one that is forgotten is the one whose last attempt is the oldest, and the new one is let in.
		if _, ok := a.begin(key(4)); !ok {
			t.Fatal("a new session is refused when the table is full")
		}
		if held(a, 1) || !held(a, 2) || !held(a, 3) || !held(a, 4) {
			t.Errorf("the table keeps sessions 1 to 4 as %v %v %v %v, want 1 forgotten", held(a, 1), held(a, 2), held(a, 3), held(a, 4))
		}
		// The new one has made one attempt, so it may be forgotten before those which wait.
		if _, ok := a.begin(key(5)); !ok {
			t.Fatal("a new session is refused when the table is full")
		}
		if held(a, 4) || !held(a, 2) || !held(a, 3) || !held(a, 5) {
			t.Errorf("the table keeps sessions 2 to 5 as %v %v %v %v, want 4 forgotten", held(a, 2), held(a, 3), held(a, 4), held(a, 5))
		}
		// A session that has expired is forgotten before the others.
		clock.advance(pairForget - 3*time.Second)
		a.begin(key(3))
		clock.advance(5 * time.Second)
		if _, ok := a.begin(key(6)); !ok {
			t.Fatal("a new session is refused when the table is full")
		}
		if held(a, 2) || !held(a, 3) || !held(a, 5) || !held(a, 6) {
			t.Errorf("the table keeps sessions 2 3 5 6 as %v %v %v %v, want the first of the expired, 2, forgotten", held(a, 2), held(a, 3), held(a, 5), held(a, 6))
		}
		if a.sessions.capacity != 3 || len(a.sessions.entries) != 3 {
			t.Errorf("the table has %d sessions, with the capacity %d, want 3 of 3", len(a.sessions.entries), a.sessions.capacity)
		}
	})

	if got := newPasswordAttempts(time.Now, 0).sessions.capacity; got != 1 {
		t.Errorf("a table of capacity 0 has the capacity %d, want 1", got)
	}
	ts := newTestServer(t, httpsOrigin)
	if got := ts.passwords.sessions.capacity; got != 10000 {
		t.Errorf("the Server keeps count of %d sessions, want 10000", got)
	}
}
