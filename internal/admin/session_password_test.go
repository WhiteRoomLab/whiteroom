package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
)

// sendWithContext is send for a request that has the context ctx, which a test can end as a client that hangs up does.
func (ts *testServer) sendWithContext(ctx context.Context, method, path, body string, headers ...string) *http.Response {
	req := ts.request(method, path, body, headers...).WithContext(ctx)
	ts.seen.watchRequest(req.Header)
	rec := httptest.NewRecorder()
	ts.ServeHTTP(rec, req)
	ts.seen.record(method+" "+path, rec.Code, rec.Header(), rec.Body.Bytes())
	return rec.Result()
}

// TestLoginPasswordTooLong shows that a password of more than 8 KiB is answered as a wrong password, and counts nothing:
// it reaches neither the attempts nor the accounts, so it cannot fill a table, and it costs no hash.
func TestLoginPasswordTooLong(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	admin := ts.addAccount(t, "admin", account.RoleAdmin)
	long := strings.Repeat("p", 8193)
	const addr = "198.51.100.9"

	// A login name that has an account and one that has none are answered alike, from the same client any number of times.
	for range 40 {
		ts.wantFailure(t, "a password of 8193 bytes", "admin", long, from(addr+":4000")...)
		ts.wantFailure(t, "a password of 8193 bytes for nobody", "nobody", long, from(addr+":4000")...)
	}
	ts.nothingCounted(t, "passwords of 8193 bytes")
	ts.checkAnswer(t, "the right password after them", ts.postLogin("admin", testPassword("admin"), from(addr+":4000")...), admin,
		ts.clock.now(), anyLoginCookie)

	// A pair that waits is not told: the answer is 401, as for any password of this length, and not 429.
	for range freeAttempts {
		ts.wantFailure(t, "a wrong password", "admin", wrongPassword, from(addr+":4000")...)
	}
	ts.wantRetry(t, "the sixth wrong password", "admin", wrongPassword, "30", from(addr+":4000")...)
	ts.wantFailure(t, "a password of 8193 bytes while the pair waits", "admin", long, from(addr+":4000")...)

	// Nor is the store read: a login that is let in fails with a closed store.
	if err := ts.db.Close(); err != nil {
		t.Fatal(err)
	}
	ts.wantFailure(t, "a password of 8193 bytes with a closed store", "admin", long, from("198.51.100.10:4000")...)
	checkProblem(t, "a password of 8192 bytes with a closed store", ts.postLogin("admin", strings.Repeat("p", 8192), from("198.51.100.10:4000")...), "internal")
}

// TestLoginPasswordBound shows where the bound is: 8192 bytes are a password that is counted and compared, whatever they
// are made of, and 8193 are not.
func TestLoginPasswordBound(t *testing.T) {
	emoji := string(rune(0x1F600)) // 4 bytes in UTF-8
	for _, tt := range []struct {
		what     string
		password string
		counted  int
	}{
		{"8192 bytes", strings.Repeat("p", 8192), 1},
		{"8193 bytes", strings.Repeat("p", 8193), 0},
		{"2048 characters of 4 bytes", strings.Repeat(emoji, 2048), 1},
		{"2049 characters of 4 bytes", strings.Repeat(emoji, 2049), 0},
		{"2048 characters of 4 bytes and one byte", strings.Repeat(emoji, 2048) + "p", 0},
	} {
		ts := newTestServer(t, httpsOrigin)
		ts.addAccount(t, "admin", account.RoleAdmin)
		ts.wantFailure(t, tt.what, "admin", tt.password, from("198.51.100.9:4000")...)
		if pair, name, client := ts.levels("admin", "198.51.100.9"); pair != tt.counted || name != tt.counted || client != tt.counted {
			t.Errorf("a password of %s counts %d for the pair, %d for the login name and %d for the client, want %d each",
				tt.what, pair, name, client, tt.counted)
		}
	}
}

// TestLoginPasswordTooLongComesAfterTheBody shows the order: the media type, the size and the shape of the body come before the
// length of the password, and so do the members that are missing.
func TestLoginPasswordTooLongComesAfterTheBody(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ts.addAccount(t, "admin", account.RoleAdmin)
	long := strings.Repeat("p", 8193)
	post := func(body string) *http.Response { return ts.sendJSON("/api/session", "application/json", body) }
	checkProblem(t, "a long password and no login name", post(`{"password":"`+long+`"}`), "bad-request")
	checkProblem(t, "a long password and a login name that is null", post(`{"login":null,"password":"`+long+`"}`), "bad-request")
	checkProblem(t, "a long password and a member that is not known", post(`{"login":"admin","password":"`+long+`","extra":1}`), "bad-request")
	checkProblem(t, "a long password in a body of the wrong type", ts.sendJSON("/api/session", "text/plain", loginBody("admin", long)),
		"unsupported-media-type")
	checkProblem(t, "a password in a body that is too large", post(loginBody("admin", strings.Repeat("p", 70000))), "too-large")
	checkProblem(t, "a long password in a request from another origin",
		ts.send(http.MethodPost, "/api/session", loginBody("admin", long), "Content-Type", "application/json", "Sec-Fetch-Site", "cross-site"),
		"cross-origin")
	checkProblem(t, "a long password", post(loginBody("admin", long)), "bad-credentials")
	checkProblem(t, "a long password and an empty login name", post(loginBody("", long)), "bad-credentials")
}

// TestLoginHangUp shows that a client that hangs up after its password has been compared leaves the attempt counted, as a
// wrong password does, whether the password was right or not: giving it back would tell the two apart.
func TestLoginHangUp(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ts.addAccount(t, "admin", account.RoleAdmin)
	// The client hangs up when the accounts first read their clock, which they do to write the session, after the password
	// has been compared.
	hangUp := func(password, addr string) *http.Response {
		t.Helper()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		ts.accounts.SetClock(func() time.Time {
			cancel()
			return ts.clock.now()
		})
		defer ts.accounts.SetClock(ts.clock.now)
		ts.seen.password(password)
		return ts.sendWithContext(ctx, http.MethodPost, "/api/session", loginBody("admin", password),
			join([]string{"Origin", ts.origin, "Content-Type", "application/json"}, from(addr+":4000"))...)
	}

	right := hangUp(testPassword("admin"), "192.0.2.1")
	checkProblem(t, "the right password, when the client hangs up", right, "busy")
	checkNoCookie(t, "the right password, when the client hangs up", right)
	if n := ts.sessionRows(t); n != 0 {
		t.Errorf("the login of a client that hung up has made %d sessions, want none", n)
	}
	// A wrong password does not get as far as the clock, and so the client does not hang up: the answer is the usual one.
	checkProblem(t, "a wrong password", hangUp(wrongPassword, "192.0.2.2"), "bad-credentials")

	// Both stay counted, the same, for the pair and for the client; the login name has both.
	rightPair, rightName, rightClient := ts.levels("admin", "192.0.2.1")
	wrongPair, wrongName, wrongClient := ts.levels("admin", "192.0.2.2")
	if rightPair != 1 || wrongPair != 1 || rightClient != 1 || wrongClient != 1 || rightName != 2 || wrongName != 2 {
		t.Errorf("after a hang-up with the right password the attempts count %d for the pair, %d for the client, and after a wrong password %d and %d; "+
			"the login name has %d and %d: want 1 and 1, 1 and 1, 2 and 2", rightPair, rightClient, wrongPair, wrongClient, rightName, wrongName)
	}
	if got := ts.logs.String(); got != "" {
		t.Errorf("a client that hung up has caused the log %q", redact(got))
	}

	// The attempt that stays counted is the guess: five hang-ups with the right password, and the sixth attempt waits.
	for range freeAttempts - 1 {
		checkProblem(t, "the right password, when the client hangs up", hangUp(testPassword("admin"), "192.0.2.1"), "busy")
	}
	ts.wantRetry(t, "the sixth attempt of the pair", "admin", wrongPassword, "30", from("192.0.2.1:4000")...)
}

// TestLoginFailsAfterTheComparison shows the same for a failure that is not the end of the context: a right password whose
// session cannot be written is answered with 500 and stays counted, as a wrong password does.
func TestLoginFailsAfterTheComparison(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ts.addAccount(t, "admin", account.RoleAdmin)
	// A row cannot hold the year 10000, so the session is not written.
	ts.accounts.SetClock(func() time.Time { return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) })
	right := ts.postLogin("admin", testPassword("admin"), from("192.0.2.1:4000")...)
	checkProblem(t, "the right password when the session cannot be written", right, "internal")
	checkNoCookie(t, "the right password when the session cannot be written", right)
	if n := ts.sessionRows(t); n != 0 {
		t.Errorf("the login that failed has made %d sessions, want none", n)
	}
	if got := ts.logs.String(); !strings.HasPrefix(got, `POST "/api/session": `) || strings.Count(got, "\n") != 1 {
		t.Errorf("the login that failed has logged %q, want one line of POST \"/api/session\": and the error", redact(got))
	}
	ts.wantFailure(t, "a wrong password", "admin", wrongPassword, from("192.0.2.2:4000")...)
	for _, addr := range []string{"192.0.2.1", "192.0.2.2"} {
		if pair, name, client := ts.levels("admin", addr); pair != 1 || name != 2 || client != 1 {
			t.Errorf("from %s the attempts count %d for the pair, %d for the login name and %d for the client, want 1, 2 and 1", addr, pair, name, client)
		}
	}
}
