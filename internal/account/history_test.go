package account

import (
	"errors"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
)

// histShape returns r without what the store sets, the number, the hash of the record before it and the time.
func histShape(r audit.Record) audit.Record {
	r.N, r.Prev, r.At = 0, "", time.Time{}
	return r
}

// histWant checks that got are the records want, in this order, without what the store sets.
func histWant(t *testing.T, got []audit.Record, want ...audit.Record) {
	t.Helper()
	if len(got) != len(want) {
		names := make([]string, len(got))
		for i, r := range got {
			names[i] = recordName(r)
		}
		t.Fatalf("the audit log has gained %d records %q, want %d", len(got), names, len(want))
	}
	for i := range want {
		if g := histShape(got[i]); !reflect.DeepEqual(g, want[i]) {
			t.Errorf("record %d is\n%+v\nwant\n%+v", i+1, g, want[i])
		}
	}
}

// histAt is the time as the detail of a record writes the time at which a session ran out.
func histAt(at time.Time) map[string]string {
	return map[string]string{"expiredAt": at.UTC().Format(time.RFC3339Nano)}
}

func TestClientRecord(t *testing.T) {
	long := strings.Repeat("a", 600)
	tests := []struct {
		name string
		c    Client
		want audit.Client
	}{
		{"the zero Client", Client{}, audit.Client{}},
		{"IPv4", Client{Addr: netip.MustParseAddr("203.0.113.7")}, audit.Client{Addr: "203.0.113.7"}},
		{"IPv6", Client{Addr: netip.MustParseAddr("2001:db8::1")}, audit.Client{Addr: "2001:db8::1"}},
		{"the unspecified address", Client{Addr: netip.IPv6Unspecified()}, audit.Client{Addr: "::"}},
		{"IPv4 in IPv6", Client{Addr: netip.MustParseAddr("::ffff:192.0.2.1")}, audit.Client{Addr: "::ffff:192.0.2.1"}},
		{"a zone", Client{Addr: netip.MustParseAddr("fe80::1%eth0")}, audit.Client{Addr: "fe80::1%eth0"}},
		{"a User-Agent", Client{UserAgent: "Mozilla/5.0 (X11; Linux x86_64)"}, audit.Client{UserAgent: "Mozilla/5.0 (X11; Linux x86_64)"}},
		{"a User-Agent of 512 bytes", Client{UserAgent: long[:512]}, audit.Client{UserAgent: long[:512]}},
		{"a User-Agent of more", Client{UserAgent: long}, audit.Client{UserAgent: long[:512]}},
		{"a tab", Client{UserAgent: "a\tb"}, audit.Client{UserAgent: "a" + string(rune(0xfffd)) + "b"}},
		{"a line feed", Client{UserAgent: "a\nb"}, audit.Client{UserAgent: "a" + string(rune(0xfffd)) + "b"}},
		{"invalid UTF-8", Client{UserAgent: "a\xffb"}, audit.Client{UserAgent: "a" + string(rune(0xfffd)) + "b"}},
		{"line feeds, which grow", Client{UserAgent: strings.Repeat("\n", 600)}, audit.Client{UserAgent: strings.Repeat(string(rune(0xfffd)), 170)}},
	}
	for _, tt := range tests {
		got := tt.c.record()
		if got != tt.want {
			t.Errorf("%s: record() = %+v, want %+v", tt.name, got, tt.want)
		}
		// A record with it can be written.
		r := audit.Record{Log: audit.LoginLog, Action: audit.Login, Client: got}
		if err := r.Check(); err != nil {
			t.Errorf("%s: the record is refused: %v", tt.name, err)
		}
	}
}

func TestFailureKey(t *testing.T) {
	for _, tt := range []struct {
		f    failure
		want string
	}{
		{failure{audit.LoginFailure, "wrong-password"}, "loginFailureWrongPassword"},
		{failure{audit.LoginFailure, "unknown-login"}, "loginFailureUnknownLogin"},
		{failure{audit.LoginFailure, "rate-limited"}, "loginFailureRateLimited"},
		{failure{audit.LoginFailure, "busy"}, "loginFailureBusy"},
		{failure{audit.TokenFailure, "malformed"}, "tokenFailureMalformed"},
		{failure{audit.TokenFailure, ""}, "tokenFailure"},
	} {
		if got := tt.f.key(); got != tt.want {
			t.Errorf("key() of %+v = %q, want %q", tt.f, got, tt.want)
		}
	}
}

func TestSuppressedRecord(t *testing.T) {
	var s suppressed
	at := time.Date(2026, 11, 8, 12, 3, 0, 0, time.UTC)
	s.add(at, failure{audit.LoginFailure, failWrongPassword})
	s.add(at, failure{audit.LoginFailure, failWrongPassword})
	s.add(at.Add(2*time.Minute), failure{audit.TokenFailure, tokenUnknown})
	s.add(at.Add(-time.Minute), failure{audit.LoginFailure, failBusy()})
	want := audit.Record{Log: audit.LoginLog, Action: audit.LoginSuppressed, Detail: map[string]string{
		"from": "2026-11-08T12:02:00Z", "to": "2026-11-08T12:06:00Z",
		"loginFailureWrongPassword": "2", "tokenFailureUnknown": "1", "loginFailureBusy": "1",
	}}
	if got := s.record(); !reflect.DeepEqual(got, want) {
		t.Errorf("record() = %+v, want %+v", got, want)
	}
	if err := want.Check(); err != nil {
		t.Errorf("the record is refused: %v", err)
	}

	// merge adds the counts and widens the minutes.
	var other suppressed
	other.add(at.Add(5*time.Minute), failure{audit.LoginFailure, failWrongPassword})
	s.merge(&other)
	if s.counts[failure{audit.LoginFailure, failWrongPassword}] != 3 || !s.from.Equal(at.Add(-time.Minute)) || !s.to.Equal(at.Add(6*time.Minute)) {
		t.Errorf("after merge the counts are %v from %v to %v, want 3 wrong passwords from 12:02 to 12:09", s.counts, s.from, s.to)
	}
}

func failBusy() string { return string(RefusedBusy) }

func TestLoginRecord(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	yamada := e.add(t, "yamada", RoleEditor)
	before := e.mark(t)
	p, token, err := e.m.Login(t.Context(), mgrClient, " YAMADA\t", mgrPassword("yamada"), "")
	if err != nil {
		t.Fatalf("Login() = %v", err)
	}
	histWant(t, e.records(t, before), audit.Record{
		Log: audit.LoginLog, Action: audit.Login, Account: yamada.ID, Session: sessKey(token),
		Client: audit.Client{Addr: "203.0.113.7", UserAgent: "whiteroom-test/1.0"},
	})
	if p.Account.ID != yamada.ID {
		t.Fatalf("Login() is the account %s, want %s", p.Account.ID, yamada.ID)
	}

	t.Run("a client that is not known", func(t *testing.T) {
		before := e.mark(t)
		_, token, err := e.m.Login(t.Context(), Client{}, "yamada", mgrPassword("yamada"), "")
		if err != nil {
			t.Fatal(err)
		}
		histWant(t, e.records(t, before), audit.Record{Log: audit.LoginLog, Action: audit.Login, Account: yamada.ID, Session: sessKey(token)})
	})

	t.Run("the time is the time of the store", func(t *testing.T) {
		// The clock of the store is the system clock here, and the one of the Manager stands still in 2026.
		before := e.mark(t)
		e.login(t, "yamada")
		records := e.records(t, before)
		if len(records) != 1 || time.Since(records[0].At) > time.Minute || records[0].At.After(time.Now()) {
			t.Errorf("the login has made the records %+v, want one that is a moment old by the clock of the store", records)
		}
	})
}

// TestLoginRecordsRehash checks that the record of a login that made the password hash again says so, since the row of
// the account is written with it, and that no other record of the start of a session does.
func TestLoginRecordsRehash(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	yamada := e.add(t, "yamada", RoleEditor)
	// The Manager's hashes have pwLow iterations: this one has fewer, and the login makes it again.
	e.useHash(t, yamada.ID, mgrPassword("yamada"), pwLow-1)
	before := e.mark(t)
	_, token, err := e.m.Login(t.Context(), mgrClient, "yamada", mgrPassword("yamada"), "")
	if err != nil {
		t.Fatalf("Login() = %v", err)
	}
	if got := e.iterationsOf(t, yamada.ID); got != pwLow {
		t.Fatalf("the stored hash has %d iterations after the login, want %d: the hash was made again", got, pwLow)
	}
	histWant(t, e.records(t, before), audit.Record{
		Log: audit.LoginLog, Action: audit.Login, Account: yamada.ID, Session: sessKey(token), Client: mgrClient.record(),
		Detail: map[string]string{"rehashed": "true"},
	})

	// The next login finds a hash that is as new as the Manager's, which is not written, and the record does not say it.
	before = e.mark(t)
	_, token, err = e.m.Login(t.Context(), mgrClient, "yamada", mgrPassword("yamada"), "")
	if err != nil {
		t.Fatalf("Login() = %v", err)
	}
	histWant(t, e.records(t, before), audit.Record{
		Log: audit.LoginLog, Action: audit.Login, Account: yamada.ID, Session: sessKey(token), Client: mgrClient.record(),
	})

	// The session that a change of the password starts is not a login that made a hash again, and its record does not
	// say so, whichever hash the account had.
	e.useHash(t, yamada.ID, mgrPassword("yamada"), pwLow-1)
	before = e.mark(t)
	_, fresh, err := e.m.ChangeOwnPassword(t.Context(), mgrClient, token, mgrPassword("yamada"), "yamada has another long password")
	if err != nil {
		t.Fatalf("ChangeOwnPassword() = %v", err)
	}
	var started []audit.Record
	for _, r := range e.loginRecords(t, before) {
		if r.Action == audit.SessionStart {
			started = append(started, r)
		}
	}
	histWant(t, started, audit.Record{
		Log: audit.LoginLog, Action: audit.SessionStart, Account: yamada.ID, Session: sessKey(fresh), Client: mgrClient.record(),
	})
}

// TestLoginRecordsWhatItRemoves checks that a login says why each session that it removes is gone: those that cannot
// work, with the reason and, for a session that ran out, the time at which it did, and then the new session.
func TestLoginRecordsWhatItRemoves(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	accounts := make(map[string]Account)
	for _, name := range []string{"login", "live", "disabled", "pending", "changed", "deleted", "broken"} {
		accounts[name] = e.add(t, name, RoleEditor)
	}
	now := mgrTime.Add(48 * time.Hour)
	e.clock.set(now)
	live := e.row(t, accounts["live"].ID)

	type removed struct {
		key, reason, account string
		detail               map[string]string
	}
	var want []removed
	craft := func(name, reason string, s sessionRow, expired time.Time) {
		e.setSession(t, sessKey(name), s)
		r := removed{key: sessKey(name), reason: reason, account: s.Account}
		if !expired.IsZero() {
			r.detail = histAt(expired)
		}
		want = append(want, r)
	}
	// The times are around the edges and one against the other: when both limits have passed, the first is the reason.
	idle := sessionFor(live, now.Add(-2*time.Hour))
	idle.LastSeenAt = now.Add(-time.Hour)
	craft("idle", endedIdle, idle, now)
	lifetime := sessionFor(live, now.Add(-SessionLifetime))
	lifetime.LastSeenAt = now.Add(-time.Minute)
	craft("lifetime", endedLifetime, lifetime, now)
	both := sessionFor(live, now.Add(-13*time.Hour))
	both.LastSeenAt = now.Add(-5 * time.Hour)
	craft("both and the idle limit first", endedIdle, both, now.Add(-4*time.Hour))
	both.AuthenticatedAt, both.LastSeenAt = now.Add(-20*time.Hour), now.Add(-2*time.Hour)
	craft("both and the lifetime first", endedLifetime, both, now.Add(-8*time.Hour))
	both.AuthenticatedAt, both.LastSeenAt = now.Add(-13*time.Hour), now.Add(-2*time.Hour)
	craft("both at once", endedLifetime, both, now.Add(-time.Hour))
	changed := sessionFor(live, now.Add(-time.Minute))
	changed.CredentialsChangedAt = live.CredentialsChangedAt.Add(time.Nanosecond)
	craft("another credentials", endedCredentials, changed, time.Time{})
	craft("an account that is not there", endedMissing, sessionRow{
		Account: mgrMissingID, CreatedAt: now, AuthenticatedAt: now, LastSeenAt: now, Factors: []Factor{FactorPassword},
	}, time.Time{})
	e.setRawSession(t, sessKey("garbage"), "not json")
	want = append(want, removed{key: sessKey("garbage"), reason: endedUnreadable})
	// The accounts change under their sessions with the store's own API, so that no operation removes a session.
	craft("disabled", endedInactive, sessionFor(e.row(t, accounts["disabled"].ID), now.Add(-time.Minute)), time.Time{})
	craft("pending", endedInactive, sessionFor(e.row(t, accounts["pending"].ID), now.Add(-time.Minute)), time.Time{})
	craft("changed", endedCredentials, sessionFor(e.row(t, accounts["changed"].ID), now.Add(-time.Minute)), time.Time{})
	craft("deleted", endedMissing, sessionFor(e.row(t, accounts["deleted"].ID), now.Add(-time.Minute)), time.Time{})
	craft("broken", endedMissing, sessionFor(e.row(t, accounts["broken"].ID), now.Add(-time.Minute)), time.Time{})
	e.edit(t, accounts["disabled"].ID, func(r *row) { r.Disabled = true })
	e.edit(t, accounts["pending"].ID, func(r *row) { r.Password = "" })
	e.edit(t, accounts["changed"].ID, func(r *row) { r.CredentialsChangedAt = r.CredentialsChangedAt.Add(time.Second) })
	e.deleteRow(t, accounts["deleted"].ID)
	e.putRaw(t, accounts["broken"].ID, "not json")
	// Sessions that work stay and are not recorded.
	e.setSession(t, sessKey("works"), sessionFor(live, now.Add(-time.Minute)))
	e.setSession(t, sessKey("works at the edge"), func() sessionRow {
		s := sessionFor(live, now.Add(-SessionLifetime+1))
		s.LastSeenAt = now.Add(-SessionIdleTimeout + 1)
		return s
	}())

	before := e.mark(t)
	_, token, err := e.m.Login(t.Context(), mgrClient, "login", mgrPassword("login"), "")
	if err != nil {
		t.Fatalf("Login() = %v", err)
	}
	slices.SortFunc(want, func(a, b removed) int { return strings.Compare(a.key, b.key) })
	var records []audit.Record
	for _, r := range want {
		records = append(records, audit.Record{
			Log: audit.LoginLog, Action: audit.SessionEnd, Result: r.reason, Account: r.account, Session: r.key, Detail: r.detail,
		})
	}
	// The request is not the client of an end that it causes: the login tells it.
	records = append(records, audit.Record{
		Log: audit.LoginLog, Action: audit.Login, Account: accounts["login"].ID, Session: sessKey(token), Client: mgrClient.record(),
	})
	histWant(t, e.records(t, before), records...)
}

func TestLoginRecordsReplacedAndEvicted(t *testing.T) {
	e := newMgr(t)
	admin := e.first(t)
	yamada := e.add(t, "yamada", RoleEditor)
	var tokens []string
	for range SessionsPerAccount {
		e.clock.add(time.Minute)
		_, token := e.login(t, "yamada")
		tokens = append(tokens, token)
	}
	_, adminToken := e.login(t, "admin")
	e.clock.add(time.Minute)

	// The session of previous is of another account here, and the oldest of the ten of yamada goes for the new one.
	before := e.mark(t)
	_, fresh, err := e.m.Login(t.Context(), mgrClient, "yamada", mgrPassword("yamada"), adminToken)
	if err != nil {
		t.Fatal(err)
	}
	histWant(t, e.records(t, before),
		audit.Record{Log: audit.LoginLog, Action: audit.SessionEnd, Result: endedReplaced, Account: admin.ID, Session: sessKey(adminToken)},
		audit.Record{Log: audit.LoginLog, Action: audit.SessionEnd, Result: endedEvicted, Account: yamada.ID, Session: sessKey(tokens[0])},
		audit.Record{Log: audit.LoginLog, Action: audit.Login, Account: yamada.ID, Session: sessKey(fresh), Client: mgrClient.record()},
	)

	// With previous that is a session of the account, the limit is not passed and none goes for it.
	before = e.mark(t)
	e.clock.add(time.Minute)
	_, again, err := e.m.Login(t.Context(), mgrClient, "yamada", mgrPassword("yamada"), tokens[3])
	if err != nil {
		t.Fatal(err)
	}
	histWant(t, e.records(t, before),
		audit.Record{Log: audit.LoginLog, Action: audit.SessionEnd, Result: endedReplaced, Account: yamada.ID, Session: sessKey(tokens[3])},
		audit.Record{Log: audit.LoginLog, Action: audit.Login, Account: yamada.ID, Session: sessKey(again), Client: mgrClient.record()},
	)
}

func TestLoginFailureRecordsWhatWasTyped(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	yamada := e.add(t, "yamada", RoleEditor)
	long := strings.Repeat("x", 100)
	tests := []struct {
		name, typed, want string
	}{
		{"the login name", "yamada", "yamada"},
		{"normalized as an operation reads it", " \x00YaMaDa\n\t", "yamada"},
		{"a name that is not one", "Not A Login", "not a login"},
		{"a name of 100 bytes", long, long[:64]},
		{"invalid UTF-8", "a\xffb", "a" + string(rune(0xfffd)) + "b"},
		{"multibyte characters cut between", strings.Repeat(string(rune(0x3042)), 30), strings.Repeat(string(rune(0x3042)), 21)},
		{"an empty name", "", ""},
	}
	for _, tt := range tests {
		before := e.mark(t)
		if _, _, err := e.m.Login(t.Context(), mgrClient, tt.typed, mgrPassword("other"), ""); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("%s: Login() = %v, want ErrBadCredentials", tt.name, err)
		}
		records := e.records(t, before)
		if len(records) != 1 || records[0].Login != tt.want {
			t.Errorf("%s: the record is %+v, want the login name %q", tt.name, records, tt.want)
		}
	}
	// The name of an account is the same typed in any case, and the record names the account.
	before := e.mark(t)
	_, _, _ = e.m.Login(t.Context(), mgrClient, "YAMADA", mgrPassword("other"), "")
	if r := e.records(t, before); len(r) != 1 || r[0].Account != yamada.ID {
		t.Errorf("the record of a wrong password is %+v, want the account of yamada", r)
	}
}

func TestAuthenticateRecords(t *testing.T) {
	other := Client{Addr: netip.MustParseAddr("2001:db8::1"), UserAgent: "another client"}
	setup := func(t *testing.T) (e *mgrEnv, a Account, token string) {
		t.Helper()
		e = newMgr(t)
		e.first(t)
		a = e.add(t, "yamada", RoleEditor)
		_, token = e.login(t, "yamada")
		return e, a, token
	}

	t.Run("a request that writes nothing records nothing", func(t *testing.T) {
		e, _, token := setup(t)
		before := e.mark(t)
		e.clock.add(sessionTouch - 1)
		if _, err := e.m.Authenticate(t.Context(), other, token); err != nil {
			t.Fatal(err)
		}
		for _, text := range []string{tokSample, "junk", ""} {
			if _, err := e.m.Authenticate(t.Context(), other, text); !errors.Is(err, ErrNoSession) {
				t.Errorf("Authenticate(%q) = %v, want ErrNoSession", text, err)
			}
		}
		e.unchanged(t, before)
	})

	t.Run("the time of the last request", func(t *testing.T) {
		e, a, token := setup(t)
		before := e.mark(t)
		e.clock.add(sessionTouch)
		if _, err := e.m.Authenticate(t.Context(), other, token); err != nil {
			t.Fatal(err)
		}
		// It is the client of this request that is recorded, not that of the login.
		histWant(t, e.records(t, before), audit.Record{
			Log: audit.LoginLog, Action: audit.SessionSeen, Account: a.ID, Session: sessKey(token), Client: other.record(),
		})
	})

	// The sessions that cannot work end with the reason, the account if the row decodes, and the client of the request.
	t.Run("a session that ended", func(t *testing.T) {
		tests := []struct {
			name string
			// ends makes the session of the account a end, and returns the token of the session that is to be found ended.
			ends   func(t *testing.T, e *mgrEnv, a Account, token string) string
			reason string
			detail func() map[string]string
		}{
			{"the idle timeout", func(_ *testing.T, e *mgrEnv, _ Account, token string) string {
				e.clock.add(SessionIdleTimeout)
				return token
			}, endedIdle, func() map[string]string { return histAt(mgrTime.Add(SessionIdleTimeout)) }},
			{"the lifetime", func(t *testing.T, e *mgrEnv, a Account, _ string) string {
				s := sessionFor(e.row(t, a.ID), mgrTime)
				s.LastSeenAt = mgrTime.Add(SessionLifetime - time.Minute)
				e.setSession(t, sessKey(tokSample), s)
				e.clock.set(mgrTime.Add(SessionLifetime))
				return tokSample
			}, endedLifetime, func() map[string]string { return histAt(mgrTime.Add(SessionLifetime)) }},
			{"a disabled account", func(t *testing.T, e *mgrEnv, a Account, token string) string {
				e.edit(t, a.ID, func(r *row) { r.Disabled = true })
				return token
			}, endedInactive, nil},
			{"other credentials", func(t *testing.T, e *mgrEnv, a Account, token string) string {
				e.edit(t, a.ID, func(r *row) { r.CredentialsChangedAt = r.CredentialsChangedAt.Add(time.Second) })
				return token
			}, endedCredentials, nil},
			{"a deleted account", func(t *testing.T, e *mgrEnv, a Account, token string) string {
				e.deleteRow(t, a.ID)
				return token
			}, endedMissing, nil},
			{"an account that cannot be read", func(t *testing.T, e *mgrEnv, a Account, token string) string {
				e.putRaw(t, a.ID, "not json")
				return token
			}, endedMissing, nil},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				e, a, token := setup(t)
				token = tt.ends(t, e, a, token)
				before := e.mark(t)
				if _, err := e.m.Authenticate(t.Context(), other, token); !errors.Is(err, ErrNoSession) {
					t.Fatalf("Authenticate() = %v, want ErrNoSession", err)
				}
				want := audit.Record{
					Log: audit.LoginLog, Action: audit.SessionEnd, Result: tt.reason, Account: a.ID, Session: sessKey(token), Client: other.record(),
				}
				if tt.detail != nil {
					want.Detail = tt.detail()
				}
				histWant(t, e.records(t, before), want)
				// The row is gone, and the next request finds nothing to record.
				before = e.mark(t)
				if _, err := e.m.Authenticate(t.Context(), other, token); !errors.Is(err, ErrNoSession) {
					t.Fatalf("Authenticate() again = %v, want ErrNoSession", err)
				}
				e.unchanged(t, before)
			})
		}
	})

	t.Run("a row that does not decode", func(t *testing.T) {
		e, _, token := setup(t)
		e.setRawSession(t, sessKey(token), "not json")
		before := e.mark(t)
		if _, err := e.m.Authenticate(t.Context(), other, token); !errors.Is(err, ErrNoSession) {
			t.Fatalf("Authenticate() = %v, want ErrNoSession", err)
		}
		histWant(t, e.records(t, before), audit.Record{
			Log: audit.LoginLog, Action: audit.SessionEnd, Result: endedUnreadable, Session: sessKey(token), Client: other.record(),
		})
	})

	t.Run("a session that another request ended meanwhile", func(t *testing.T) {
		// The request that finds the row gone in its Update records nothing: the logout has.
		e, _, token := setup(t)
		e.clock.add(sessionTouch)
		e.m.hook = func(point string) {
			if point == "authenticated" {
				e.m.hook = nil
				if err := e.m.Logout(t.Context(), mgrClient, token); err != nil {
					t.Error(err)
				}
			}
		}
		before := e.mark(t)
		if _, err := e.m.Authenticate(t.Context(), other, token); !errors.Is(err, ErrNoSession) {
			t.Fatalf("Authenticate() = %v, want ErrNoSession", err)
		}
		e.gained(t, before, "logout")
	})

	t.Run("the time is written once for the requests that find it old", func(t *testing.T) {
		e, _, token := setup(t)
		e.clock.add(sessionTouch)
		before := e.mark(t)
		const n = 8
		calls := make([]func() error, n)
		for i := range calls {
			calls[i] = func() error { _, err := e.m.Authenticate(t.Context(), mgrClient, token); return err }
		}
		for i, err := range mgrRunTogether(calls...) {
			if err != nil {
				t.Errorf("request %d = %v", i, err)
			}
		}
		if got := e.added(t, before); len(got) != 1 || got[0] != "session.seen" {
			t.Errorf("%d requests have made the records %q, want one session.seen", n, got)
		}
	})
}

func TestLogoutRecord(t *testing.T) {
	other := Client{Addr: netip.MustParseAddr("2001:db8::1"), UserAgent: "another client"}
	setup := func(t *testing.T) (e *mgrEnv, a Account, token string) {
		t.Helper()
		e = newMgr(t)
		e.first(t)
		a = e.add(t, "yamada", RoleEditor)
		_, token = e.login(t, "yamada")
		return e, a, token
	}

	t.Run("a session", func(t *testing.T) {
		e, a, token := setup(t)
		before := e.mark(t)
		if err := e.m.Logout(t.Context(), other, token); err != nil {
			t.Fatal(err)
		}
		histWant(t, e.records(t, before), audit.Record{
			Log: audit.LoginLog, Action: audit.Logout, Account: a.ID, Session: sessKey(token), Client: other.record(),
		})
	})

	t.Run("a session that has run out is a logout too", func(t *testing.T) {
		e, a, token := setup(t)
		e.clock.add(SessionLifetime)
		before := e.mark(t)
		if err := e.m.Logout(t.Context(), other, token); err != nil {
			t.Fatal(err)
		}
		histWant(t, e.records(t, before), audit.Record{
			Log: audit.LoginLog, Action: audit.Logout, Account: a.ID, Session: sessKey(token), Client: other.record(),
		})
	})

	t.Run("a row that does not decode", func(t *testing.T) {
		e, _, token := setup(t)
		e.setRawSession(t, sessKey(token), "not json")
		before := e.mark(t)
		if err := e.m.Logout(t.Context(), other, token); err != nil {
			t.Fatal(err)
		}
		histWant(t, e.records(t, before), audit.Record{
			Log: audit.LoginLog, Action: audit.Logout, Session: sessKey(token), Client: other.record(),
		})
		if e.hasSession(t, token) {
			t.Error("the row stayed")
		}
	})

	t.Run("no session", func(t *testing.T) {
		e, _, token := setup(t)
		if err := e.m.Logout(t.Context(), other, token); err != nil {
			t.Fatal(err)
		}
		before := e.mark(t)
		for _, text := range []string{token, tokSample, "junk", ""} {
			if err := e.m.Logout(t.Context(), other, text); err != nil {
				t.Errorf("Logout(%q) = %v, want nil", text, err)
			}
		}
		e.unchanged(t, before)
	})
}

func TestChangeOwnPasswordRecords(t *testing.T) {
	other := Client{Addr: netip.MustParseAddr("198.51.100.4"), UserAgent: "the client that changes the password"}
	e := newMgr(t)
	admin := e.first(t)
	yamada := e.add(t, "yamada", RoleEditor)
	_, first := e.login(t, "yamada")
	_, second := e.login(t, "yamada")
	_, adminToken := e.login(t, "admin")
	// Two sessions that have run out: the password ends the one of yamada with the others of the account, whatever state it
	// is in, and the sweep that makes the new session removes the one of the administrator.
	oldYamada := sessionFor(e.row(t, yamada.ID), mgrTime.Add(-2*time.Hour))
	e.setSession(t, sessKey("old session of yamada"), oldYamada)
	oldAdmin := sessionFor(e.row(t, admin.ID), mgrTime.Add(-2*time.Hour))
	e.setSession(t, sessKey("old session of admin"), oldAdmin)

	before := e.mark(t)
	_, fresh, err := e.m.ChangeOwnPassword(t.Context(), other, first, mgrPassword("yamada"), "yamada has another long password")
	if err != nil {
		t.Fatalf("ChangeOwnPassword() = %v", err)
	}
	var ends []audit.Record
	for _, key := range []string{sessKey(first), sessKey(second), sessKey("old session of yamada")} {
		ends = append(ends, audit.Record{Log: audit.LoginLog, Action: audit.SessionEnd, Result: endedPassword, Account: yamada.ID, Session: key})
	}
	slices.SortFunc(ends, func(a, b audit.Record) int { return strings.Compare(a.Session, b.Session) })
	// The sessions that the password ends come first, then what the sweep for the new session removes, then the new session.
	// The change has a record in the activity log too (TestAccountRecords).
	histWant(t, e.loginRecords(t, before), append(ends,
		audit.Record{Log: audit.LoginLog, Action: audit.SessionEnd, Result: endedIdle, Account: admin.ID, Session: sessKey("old session of admin"),
			Detail: histAt(oldAdmin.LastSeenAt.Add(SessionIdleTimeout))},
		audit.Record{Log: audit.LoginLog, Action: audit.SessionStart, Account: yamada.ID, Session: sessKey(fresh), Client: other.record()},
	)...)
	if !e.hasSession(t, adminToken) || !e.hasSession(t, fresh) {
		t.Error("the session of another account, or the new one, has no row")
	}

	t.Run("a refused change records nothing", func(t *testing.T) {
		before := e.mark(t)
		for _, tt := range []struct{ token, current, next string }{
			{fresh, mgrPassword("other"), "yamada has a third long password"},
			{fresh, "yamada has another long password", "short"},
			{tokSample, "yamada has another long password", "yamada has a third long password"},
			{first, "yamada has another long password", "yamada has a third long password"},
		} {
			if _, _, err := e.m.ChangeOwnPassword(t.Context(), other, tt.token, tt.current, tt.next); err == nil {
				t.Errorf("ChangeOwnPassword() with a token and passwords that are refused = nil")
			}
		}
		e.unchanged(t, before)
	})
}

func TestSetPasswordRecords(t *testing.T) {
	other := Client{Addr: netip.MustParseAddr("198.51.100.4"), UserAgent: "the client that sets the password"}

	t.Run("a token that cannot be used", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		expiring, expiringTk := e.invite(t, "hanako", RoleEditor)
		e.clock.add(8 * 24 * time.Hour) // an invitation lasts 7 days
		// A disabled account has no token, so the row is made with the store's own API.
		disabledTk, hash, err := newToken(e.m.rand)
		if err != nil {
			t.Fatal(err)
		}
		sleeper := mgrRow(6, "sleeper", RoleEditor)
		sleeper.Password, sleeper.CredentialsChangedAt, sleeper.Disabled = "", time.Time{}, true
		sleeper.Token = tokenRow{Purpose: PurposeInvitation, Hash: hash, ExpiresAt: e.clock.now().Add(time.Hour)}
		e.set(t, sleeper)

		before := e.mark(t)
		for _, token := range []string{"x", tokSample, expiringTk, disabledTk} {
			if _, err := e.m.SetPassword(t.Context(), other, token, mgrPassword("hanako")); !errors.Is(err, ErrInvalidToken) || err.Error() != "invalid or expired token" {
				t.Fatalf("SetPassword() = %v, want ErrInvalidToken and its text", err)
			}
		}
		histWant(t, e.records(t, before),
			audit.Record{Log: audit.LoginLog, Action: audit.TokenFailure, Result: "malformed", Client: other.record()},
			audit.Record{Log: audit.LoginLog, Action: audit.TokenFailure, Result: "unknown", Client: other.record()},
			audit.Record{Log: audit.LoginLog, Action: audit.TokenFailure, Result: "expired", Account: expiring.ID, Client: other.record()},
			audit.Record{Log: audit.LoginLog, Action: audit.TokenFailure, Result: "disabled", Account: sleeper.ID, Client: other.record()},
		)
	})

	t.Run("the problems of the password are no failure of the token", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		_, token := e.invite(t, "yamada", RoleEditor)
		before := e.mark(t)
		if _, err := e.m.SetPassword(t.Context(), other, token, "short"); err == nil || errors.Is(err, ErrInvalidToken) {
			t.Fatalf("SetPassword() with a short password = %v, want the problems of the password", err)
		}
		e.unchanged(t, before)
	})

	t.Run("a token that is used up while the password is hashed", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		_, token := e.invite(t, "yamada", RoleEditor)
		e.m.hook = func(point string) {
			if point == "hashed" {
				e.m.hook = nil
				if _, err := e.m.SetPassword(t.Context(), mgrClient, token, mgrPassword("yamada")); err != nil {
					t.Error(err)
				}
			}
		}
		before := e.mark(t)
		if _, err := e.m.SetPassword(t.Context(), other, token, "yamada has another long password"); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("SetPassword() = %v, want ErrInvalidToken", err)
		}
		// The call in the hook set the password, and this one found the token gone, in its Update.
		e.gained(t, before, "account.setPassword", "token.failure/unknown")
	})

	t.Run("the sessions of the account end, without a client", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		a := e.add(t, "yamada", RoleEditor)
		token, err := e.m.Reset(t.Context(), System, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		// A reset ends the sessions; a row that is written after it, as a crash or the store's own API leaves one, goes with
		// the new password.
		e.setSession(t, sessKey("left behind"), sessionFor(e.row(t, a.ID), mgrTime))
		before := e.mark(t)
		if _, err := e.m.SetPassword(t.Context(), other, token, "yamada has another long password"); err != nil {
			t.Fatal(err)
		}
		histWant(t, e.loginRecords(t, before), audit.Record{
			Log: audit.LoginLog, Action: audit.SessionEnd, Result: endedPassword, Account: a.ID, Session: sessKey("left behind"),
		})
	})
}

// TestVerifyPasswordFactorRecordsNothing shows that checking a password as one factor of a login writes nothing, however
// it ends: no caller has a use for a record yet.
func TestVerifyPasswordFactorRecordsNothing(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	e.add(t, "yamada", RoleEditor)
	before := e.mark(t)
	for _, tt := range []struct{ login, password string }{
		{"yamada", mgrPassword("other")},
		{"nobody", mgrPassword("other")},
		{"yamada", strings.Repeat("a", maxTypedPasswordBytes+1)},
	} {
		if _, err := e.m.VerifyPasswordFactor(t.Context(), tt.login, tt.password); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("VerifyPasswordFactor() = %v, want ErrBadCredentials", err)
		}
	}
	if _, err := e.m.VerifyPasswordFactor(t.Context(), "yamada", mgrPassword("yamada")); err != nil {
		t.Fatalf("VerifyPasswordFactor() = %v", err)
	}
	e.unchanged(t, before)
}

// TestLoginWithAUserAgentThatNeedsCleaning shows that a User-Agent that a record would refuse is made fit for one, and the
// login and the failure of a login go on: a request is never refused for what its client says of itself.
func TestLoginWithAUserAgentThatNeedsCleaning(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	e.add(t, "yamada", RoleEditor)
	c := Client{Addr: netip.MustParseAddr("203.0.113.9"), UserAgent: "a\nb\x00c\xff" + strings.Repeat("d", 600)}
	bad := string(rune(0xfffd))
	want := audit.Client{Addr: "203.0.113.9", UserAgent: "a" + bad + "b" + bad + "c" + bad + strings.Repeat("d", 500)}

	before := e.mark(t)
	if _, _, err := e.m.Login(t.Context(), c, "yamada", mgrPassword("other"), ""); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("Login() with a wrong password = %v, want ErrBadCredentials", err)
	}
	if _, _, err := e.m.Login(t.Context(), c, "yamada", mgrPassword("yamada"), ""); err != nil {
		t.Fatalf("Login() = %v", err)
	}
	records := e.records(t, before)
	if len(records) != 2 || records[0].Client != want || records[1].Client != want {
		t.Errorf("the records are %+v, want two with the client %+v", records, want)
	}
}
