package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
)

// loginRecords returns the records of the login history, from the oldest.
func (ts *testServer) loginRecords(t *testing.T) []audit.Record {
	t.Helper()
	var got []audit.Record
	err := ts.db.ReadAudit(t.Context(), audit.Filter{Log: audit.LoginLog}, func(r audit.Record) bool {
		got = append(got, r)
		return true
	})
	if err != nil {
		t.Fatalf("ReadAudit() = %v", err)
	}
	return got
}

// recordsSince returns the records of the login history after the first n, without what the store sets.
func (ts *testServer) recordsSince(t *testing.T, n int) []audit.Record {
	t.Helper()
	got := ts.loginRecords(t)
	if len(got) < n {
		t.Fatalf("the login history has %d records, fewer than the %d that it had", len(got), n)
	}
	got = got[n:]
	for i := range got {
		got[i].N, got[i].Prev, got[i].At = 0, "", time.Time{}
	}
	return got
}

func (ts *testServer) wantRecords(t *testing.T, what string, since int, want ...audit.Record) {
	t.Helper()
	got := ts.recordsSince(t, since)
	if len(got) != len(want) {
		t.Errorf("%s: the login history gained %d records %+v, want %d", what, len(got), got, len(want))
		return
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("%s: record %d is\n%+v\nwant\n%+v", what, i+1, got[i], want[i])
		}
	}
}

// sessionKeyOf is the key of the session of a token in the table sessions, which is how a record names the session.
func sessionKeyOf(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

const testAgent = "Mozilla/5.0 (X11; Linux x86_64) whiteroom-test"

func clientRecord(addr, agent string) audit.Client {
	return audit.Client{Addr: addr, UserAgent: agent}
}

func TestLoginHistoryRecordsLogins(t *testing.T) {
	ts := newTestServer(t, httpsOrigin, "192.0.2.0/24")
	admin := ts.addAccount(t, "admin", account.RoleAdmin)
	pending := ts.inviteAccount(t, "pending", account.RoleViewer)
	left := ts.addAccount(t, "left", account.RoleViewer)
	if _, err := ts.accounts.Disable(t.Context(), account.By(admin.ID), left.ID); err != nil {
		t.Fatal(err)
	}
	peer := func(addr string) []string { return []string{"RemoteAddr", addr + ":4000", "User-Agent", testAgent} }

	t.Run("a login", func(t *testing.T) {
		n := len(ts.loginRecords(t))
		res := ts.postLogin(" Admin ", testPassword("admin"), peer("198.51.100.20")...)
		token := ts.tokenOf(t, res)
		ts.wantRecords(t, "a login", n, audit.Record{
			Log: audit.LoginLog, Action: audit.Login, Account: admin.ID, Session: sessionKeyOf(token),
			Client: clientRecord("198.51.100.20", testAgent),
		})
	})

	t.Run("a login through a proxy", func(t *testing.T) {
		n := len(ts.loginRecords(t))
		// The address is that of the client behind the proxy, and a peer that is no proxy cannot say it.
		token := ts.logIn(t, "admin", join(peer("192.0.2.1"), viaProxy("203.0.113.5"))...)
		ts.wantRecords(t, "a login through a proxy", n, audit.Record{
			Log: audit.LoginLog, Action: audit.Login, Account: admin.ID, Session: sessionKeyOf(token),
			Client: clientRecord("203.0.113.5", testAgent),
		})
		n = len(ts.loginRecords(t))
		token = ts.logIn(t, "admin", join(peer("198.51.100.21"), viaProxy("203.0.113.6"))...)
		ts.wantRecords(t, "a login from a peer that is no proxy", n, audit.Record{
			Log: audit.LoginLog, Action: audit.Login, Account: admin.ID, Session: sessionKeyOf(token),
			Client: clientRecord("198.51.100.21", testAgent),
		})
	})

	t.Run("a login that is replaced by the next", func(t *testing.T) {
		first := ts.logIn(t, "admin", peer("198.51.100.22")...)
		n := len(ts.loginRecords(t))
		second := ts.logIn(t, "admin", join(peer("198.51.100.23"), ts.cookie(first))...)
		ts.wantRecords(t, "a login with a cookie", n,
			audit.Record{Log: audit.LoginLog, Action: audit.SessionEnd, Result: "replaced", Account: admin.ID, Session: sessionKeyOf(first)},
			audit.Record{Log: audit.LoginLog, Action: audit.Login, Account: admin.ID, Session: sessionKeyOf(second),
				Client: clientRecord("198.51.100.23", testAgent)},
		)
	})

	t.Run("the failures", func(t *testing.T) {
		for _, tt := range []struct {
			what, login, password, result, account string
			typed                                  string
		}{
			{"a wrong password", "admin", wrongPassword, "wrong-password", admin.ID, "admin"},
			{"a wrong password and a login name in upper case", "ADMIN ", wrongPassword, "wrong-password", admin.ID, "admin"},
			{"a login name that nobody has", "nobody", wrongPassword, "unknown-login", "", "nobody"},
			{"an empty login name", "", "", "unknown-login", "", ""},
			{"a pending account", "pending", testPassword("pending"), "pending", pending.ID, "pending"},
			{"a disabled account", "left", testPassword("left"), "disabled", left.ID, "left"},
			{"a password of more than 8 KiB", "admin", strings.Repeat("p", 8193), "too-long", "", "admin"},
			{"a password of more than 8 KiB for nobody", "nobody", strings.Repeat("p", 8193), "too-long", "", "nobody"},
		} {
			n := len(ts.loginRecords(t))
			ts.wantFailure(t, tt.what, tt.login, tt.password, peer("198.51.100.30")...)
			ts.wantRecords(t, tt.what, n, audit.Record{
				Log: audit.LoginLog, Action: audit.LoginFailure, Result: tt.result, Account: tt.account, Login: tt.typed,
				Client: clientRecord("198.51.100.30", testAgent),
			})
			ts.clock.advance(time.Hour) // the attempts of the client are forgiven
		}
	})

	t.Run("a client that waits", func(t *testing.T) {
		const addr = "198.51.100.40"
		for range freeAttempts {
			ts.wantFailure(t, "a wrong password", "admin", wrongPassword, peer(addr)...)
		}
		n := len(ts.loginRecords(t))
		ts.wantRetry(t, "the sixth attempt", "admin", wrongPassword, "30", peer(addr)...)
		ts.wantRecords(t, "the sixth attempt", n, audit.Record{
			Log: audit.LoginLog, Action: audit.LoginFailure, Result: "rate-limited", Login: "admin", Client: clientRecord(addr, testAgent),
		})
		// The right password is refused alike, and recorded alike: nothing tells that it was the right one.
		n = len(ts.loginRecords(t))
		ts.wantRetry(t, "the right password while the pair waits", "admin", testPassword("admin"), "30", peer(addr)...)
		ts.wantRecords(t, "the right password while the pair waits", n, audit.Record{
			Log: audit.LoginLog, Action: audit.LoginFailure, Result: "rate-limited", Login: "admin", Client: clientRecord(addr, testAgent),
		})
		ts.clock.advance(time.Hour)
	})

	t.Run("no place for the login", func(t *testing.T) {
		for range cap(ts.logins) {
			ts.logins <- struct{}{}
		}
		n := len(ts.loginRecords(t))
		res := ts.postLogin("admin", testPassword("admin"), peer("198.51.100.50")...)
		checkProblem(t, "a login when the places are taken", res, "busy")
		for range cap(ts.logins) {
			<-ts.logins
		}
		ts.wantRecords(t, "a login when the places are taken", n, audit.Record{
			Log: audit.LoginLog, Action: audit.LoginFailure, Result: "busy", Login: "admin", Client: clientRecord("198.51.100.50", testAgent),
		})
		ts.clock.advance(time.Hour)
	})

	t.Run("a request that is no login records nothing", func(t *testing.T) {
		n := len(ts.loginRecords(t))
		checkProblem(t, "a login from another site", ts.send(http.MethodPost, "/api/session", loginBody("admin", wrongPassword),
			"Content-Type", "application/json", "Sec-Fetch-Site", "cross-site"), "cross-origin")
		checkProblem(t, "a body that is no login", ts.sendJSON("/api/session", "application/json", `{"login":"admin"}`), "bad-request")
		checkProblem(t, "a login of the wrong media type", ts.sendJSON("/api/session", "text/plain", loginBody("admin", wrongPassword)), "unsupported-media-type")
		checkProblem(t, "GET without a cookie", ts.getSession(), "unauthenticated")
		checkProblem(t, "GET with a text that is no token", ts.getSession(ts.cookie("abc")...), "unauthenticated")
		checkProblem(t, "GET with a token that has no session", ts.getSession(ts.cookie(strings.Repeat("B", 43))...), "unauthenticated")
		if got := ts.deleteSession().StatusCode; got != http.StatusNoContent {
			t.Errorf("DELETE without a cookie has the status %d, want 204", got)
		}
		if got := len(ts.loginRecords(t)); got != n {
			t.Errorf("requests that are no login, or have no session, have made %d records", got-n)
		}
	})
}

func TestLoginHistoryRecordsSessions(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	admin := ts.addAccount(t, "admin", account.RoleAdmin)
	headers := func(token string) []string {
		return join(ts.cookie(token), []string{"RemoteAddr", "198.51.100.60:4000", "User-Agent", testAgent})
	}
	client := clientRecord("198.51.100.60", testAgent)
	token := ts.logIn(t, "admin")

	// A request that finds the time of the session recent writes nothing.
	n := len(ts.loginRecords(t))
	ts.clock.advance(4 * time.Minute)
	if got := ts.getSession(headers(token)...).StatusCode; got != http.StatusOK {
		t.Fatalf("GET has the status %d, want 200", got)
	}
	if got := len(ts.loginRecords(t)); got != n {
		t.Errorf("a request within five minutes of the last has made %d records", got-n)
	}

	// The time of the last request is written once it is old, with the client of that request.
	ts.clock.advance(time.Minute)
	if got := ts.getSession(headers(token)...).StatusCode; got != http.StatusOK {
		t.Fatalf("GET has the status %d, want 200", got)
	}
	ts.wantRecords(t, "the time of the last request", n, audit.Record{
		Log: audit.LoginLog, Action: audit.SessionSeen, Account: admin.ID, Session: sessionKeyOf(token), Client: client,
	})

	// A session that has ended is removed by the request that finds it, which the record names.
	n = len(ts.loginRecords(t))
	last := ts.clock.now()
	ts.clock.advance(account.SessionIdleTimeout)
	res := ts.getSession(headers(token)...)
	checkProblem(t, "GET with a session that was idle", res, "unauthenticated")
	if got := res.Header.Values("Set-Cookie"); !equalValues(got, ts.clearLine()) {
		t.Errorf("GET sets the cookies %q, want %q", redacted(got), ts.clearLine())
	}
	ts.wantRecords(t, "a session that was idle", n, audit.Record{
		Log: audit.LoginLog, Action: audit.SessionEnd, Result: "idle", Account: admin.ID, Session: sessionKeyOf(token), Client: client,
		Detail: map[string]string{"expiredAt": last.Add(account.SessionIdleTimeout).UTC().Format(time.RFC3339Nano)},
	})

	// A logout names the session and the client; one without a session makes no record.
	token = ts.logIn(t, "admin")
	n = len(ts.loginRecords(t))
	if got := ts.deleteSession(headers(token)...).StatusCode; got != http.StatusNoContent {
		t.Fatalf("DELETE has the status %d, want 204", got)
	}
	ts.wantRecords(t, "a logout", n, audit.Record{
		Log: audit.LoginLog, Action: audit.Logout, Account: admin.ID, Session: sessionKeyOf(token), Client: client,
	})
	n = len(ts.loginRecords(t))
	if got := ts.deleteSession(headers(token)...).StatusCode; got != http.StatusNoContent {
		t.Fatalf("DELETE again has the status %d, want 204", got)
	}
	ts.wantRecords(t, "a logout of a session that is gone", n)
}

// TestLoginHistoryClientOfAUnixSocket shows that the client of a request over a Unix socket without X-Forwarded-For is the
// address that clientAddr gives, the unspecified one, in the record.
func TestLoginHistoryClientOfAUnixSocket(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ts.addAccount(t, "admin", account.RoleAdmin)
	n := len(ts.loginRecords(t))
	ctx := context.WithValue(t.Context(), unixKey{}, true)
	res := ts.sendWithContext(ctx, http.MethodPost, "/api/session", loginBody("admin", wrongPassword),
		"Origin", ts.origin, "Content-Type", "application/json")
	checkProblem(t, "a login over a Unix socket", res, "bad-credentials")
	records := ts.recordsSince(t, n)
	if len(records) != 1 || records[0].Client != clientRecord("::", "") {
		t.Errorf("the record of a login over a Unix socket is %+v, want the client ::", records)
	}
}

// TestLoginHistoryRefusedWhenTheStoreFails shows that a record that cannot be written changes no answer, and is logged: a
// line with the action and the error of the store, and nothing typed.
func TestLoginHistoryRefusedWhenTheStoreFails(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ts.addAccount(t, "admin", account.RoleAdmin)
	const typed = "the login name that was typed"
	if err := ts.db.Close(); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("p", 8193)
	ts.wantFailure(t, "a password of more than 8 KiB with a store that fails", typed, long)
	lines := strings.Split(strings.TrimSuffix(ts.logs.String(), "\n"), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "cannot record login.failure: ") || strings.Contains(lines[0], typed) {
		t.Errorf("a refusal with a store that fails has logged %q, want one line: cannot record login.failure and the error", lines)
	}
}
