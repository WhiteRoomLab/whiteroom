package account

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// sessKey is a key of the table sessions: the 64 lower case hexadecimal digits of the SHA-256 of a text. Of a token it is
// the key of the row of its session; the tests make it without sessionKey, which they test.
func sessKey(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// sessSame is a reader that reads the same bytes every time, so that every token it makes is the same.
type sessSame struct{}

func (sessSame) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(i)
	}
	return len(p), nil
}

// login logs in as the account with the password mgrPassword gives it, and returns the Principal and the token.
func (e *mgrEnv) login(t *testing.T, login string) (Principal, string) {
	t.Helper()
	p, token, err := e.m.Login(t.Context(), login, mgrPassword(login), "")
	if err != nil {
		t.Fatalf("Login(%q) = %v", login, err)
	}
	return p, token
}

// setSession writes the row s of the session with the key, with the store's own API, which makes states that Login
// cannot.
func (e *mgrEnv) setSession(t *testing.T, key string, s sessionRow) {
	t.Helper()
	data, err := encodeSession(s)
	if err != nil {
		t.Fatalf("encodeSession() = %v", err)
	}
	e.setRawSession(t, key, string(data))
}

// setRawSession writes data as the row of the session with the key as it is.
func (e *mgrEnv) setRawSession(t *testing.T, key, data string) {
	t.Helper()
	err := e.s.Update(t.Context(), func(tx *store.Tx) error { return tx.PutSession(key, []byte(data)) })
	if err != nil {
		t.Fatalf("PutSession() = %v", err)
	}
}

// sessionOf returns the stored row of the session with the token, and whether there is one.
func (e *mgrEnv) sessionOf(t *testing.T, token string) (sessionRow, bool) {
	t.Helper()
	data, ok := e.rawSessions(t)[sessKey(token)]
	if !ok {
		return sessionRow{}, false
	}
	s, err := decodeSession(sessKey(token), []byte(data))
	if err != nil {
		t.Fatal("the stored row of a session does not decode")
	}
	return s, true
}

// hasSession reports whether the token has a stored row.
func (e *mgrEnv) hasSession(t *testing.T, token string) bool {
	t.Helper()
	_, ok := e.rawSessions(t)[sessKey(token)]
	return ok
}

// edit changes the stored row of the account id with f, with the store's own API: no operation of the Manager is
// involved, so nothing removes a session meanwhile.
func (e *mgrEnv) edit(t *testing.T, id string, f func(r *row)) {
	t.Helper()
	r := e.row(t, id)
	f(&r)
	e.set(t, r)
}

// deleteRow removes the row of the account id with the store's own API.
func (e *mgrEnv) deleteRow(t *testing.T, id string) {
	t.Helper()
	if err := e.s.Update(t.Context(), func(tx *store.Tx) error { return tx.DeleteAccount(id) }); err != nil {
		t.Fatalf("DeleteAccount() = %v", err)
	}
}

// sessionFor returns the row of a session of the account r that was made at at, with the credentials that r has.
func sessionFor(r row, at time.Time) sessionRow {
	return sessionRow{
		Account: r.ID, CreatedAt: at, AuthenticatedAt: at, LastSeenAt: at, CredentialsChangedAt: r.CredentialsChangedAt,
		Factors: []Factor{FactorPassword},
	}
}

// sessionJSON is what encodeSession writes for a session of the account id with these times.
func sessionJSON(id string, created, authenticated, lastSeen, changed string) string {
	s := `{"account":"` + id + `","createdAt":"` + created + `","authenticatedAt":"` + authenticated + `","lastSeenAt":"` + lastSeen + `"`
	if changed != "" {
		s += `,"credentialsChangedAt":"` + changed + `"`
	}
	return s + `,"factors":["password"]}`
}

const sessWrong = "wrong login name or password"

func TestLogin(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	yamada := e.add(t, "yamada", RoleEditor)
	loginAt := mgrTime.Add(time.Hour)
	e.clock.set(loginAt)
	e.m.rand = bytes.NewReader(tokBytes(tokenBytes)) // the token is tokSample
	accounts := e.raw(t)

	p, token, err := e.m.Login(t.Context(), "yamada", mgrPassword("yamada"), "")
	if err != nil {
		t.Fatalf("Login() = %v", err)
	}
	if token != tokSample {
		t.Error("the token is not the one that the random bits make")
	}
	want := Principal{
		Account: e.row(t, yamada.ID).account(),
		Session: Session{AuthenticatedAt: loginAt, ExpiresAt: loginAt.Add(SessionLifetime), Factors: []Factor{FactorPassword}},
	}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("Login() returned the Principal\n%+v\nwant\n%+v", p, want)
	}
	if got := time.Date(2026, 11, 9, 1, 0, 0, 123456000, time.UTC); !p.Session.ExpiresAt.Equal(got) {
		t.Errorf("the session expires at %v, want %v: 12 hours after the login", p.Session.ExpiresAt, got)
	}

	// The row is under the SHA-256 of the text of the token, in the one form, and nothing else of the token is stored.
	rows := e.rawSessions(t)
	wantRow := sessionJSON(yamada.ID, "2026-11-08T13:00:00.123456Z", "2026-11-08T13:00:00.123456Z", "2026-11-08T13:00:00.123456Z",
		"2026-11-08T12:00:00.123456Z")
	if len(rows) != 1 || rows[sessKey(tokSample)] != wantRow {
		t.Errorf("the session rows are %d, the row of the token is %q, want one, %q", len(rows), rows[sessKey(tokSample)], wantRow)
	}
	if key, ok := sessionKey(token); !ok || key != sessKey(tokSample) || key != strings.TrimPrefix(tokSampleHash, "sha256:") {
		t.Error("the key of the session is not the hexadecimal SHA-256 of the text of the token")
	}
	// A login writes nothing to the accounts.
	for id, data := range e.raw(t) {
		if accounts[id] != data {
			t.Errorf("the row of %s changed", id)
		}
	}

	t.Run("each login is a session", func(t *testing.T) {
		e := newMgr(t)
		ids := map[string]string{"admin": e.first(t).ID, "yamada": e.add(t, "yamada", RoleEditor).ID}
		seen := make(map[string]bool)
		for _, login := range []string{"admin", "yamada", "yamada", "admin"} {
			p, token := e.login(t, login)
			if _, ok := tokenHash(token); !ok || len(token) != 43 || seen[token] {
				t.Errorf("Login(%q) returned a token that is not a new token of 43 characters", login)
			}
			seen[token] = true
			if got, _ := e.sessionOf(t, token); got.Account != p.Account.ID || p.Account.ID != ids[login] {
				t.Errorf("Login(%q) made a session of %s for the account %s", login, got.Account, p.Account.ID)
			}
		}
		if rows := e.rawSessions(t); len(rows) != 4 {
			t.Errorf("there are %d session rows, want 4", len(rows))
		}
	})

	t.Run("the login name is normalized", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		e.add(t, "yamada.taro", RoleEditor)
		for _, login := range []string{"yamada.taro", "YAMADA.Taro", " \tyamada.taro\n"} {
			p, token, err := e.m.Login(t.Context(), login, mgrPassword("yamada.taro"), "")
			if err != nil || p.Account.Login != "yamada.taro" || !e.hasSession(t, token) {
				t.Errorf("Login(%q) = %+v, %v, want a session of yamada.taro", login, p, err)
			}
		}
	})

	t.Run("the first account", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		e.clock.add(time.Second)
		p, _ := e.login(t, "admin")
		if p.Account.Role != RoleAdmin || p.Account.Status != StatusActive || !p.Session.AuthenticatedAt.Equal(mgrTime.Add(time.Second)) {
			t.Errorf("Login() = %+v, want the active administrator, authenticated a second after it was made", p)
		}
	})
}

func TestLoginFailures(t *testing.T) {
	type fixture struct {
		e                *mgrEnv
		active           Account
		token, pendingTk string // the token of a session of active, and the token of the invitation of pending
	}
	setup := func(t *testing.T) fixture {
		e := newMgr(t)
		e.first(t)
		active := e.add(t, "active", RoleEditor)
		_, pendingTk := e.invite(t, "pending", RoleEditor)
		reset := e.add(t, "reset", RoleEditor)
		if _, err := e.m.Reset(t.Context(), System, reset.ID); err != nil {
			t.Fatal(err)
		}
		off := e.add(t, "off", RoleEditor)
		if _, err := e.m.Disable(t.Context(), System, off.ID); err != nil {
			t.Fatal(err)
		}
		broken := e.add(t, "broken", RoleEditor)
		e.putRaw(t, broken.ID, "not json")
		_, token := e.login(t, "active")
		return fixture{e, active, token, pendingTk}
	}

	t.Run("every reason", func(t *testing.T) {
		f := setup(t)
		e := f.e
		// A login that works would remove these. A login that fails must leave them.
		made, _ := e.sessionOf(t, f.token)
		expired := made
		expired.CreatedAt, expired.AuthenticatedAt, expired.LastSeenAt = mgrTime.Add(-24*time.Hour), mgrTime.Add(-24*time.Hour), mgrTime.Add(-24*time.Hour)
		e.setSession(t, sessKey("expired"), expired)
		e.setRawSession(t, sessKey("garbage"), "not json")
		before := e.mark(t)
		tests := []struct{ name, login, password string }{
			{"no such login name", "nobody", mgrPassword("nobody")},
			{"the email address", "active@example.com", mgrPassword("active")},
			{"an empty login name", "", mgrPassword("active")},
			{"a wrong password", "active", mgrPassword("other")},
			{"an empty password", "active", ""},
			{"the password of another account", "active", mgrPassword("admin")},
			{"the password in upper case", "active", strings.ToUpper(mgrPassword("active"))},
			{"a pending account with the password it will have", "pending", mgrPassword("pending")},
			{"a pending account with its token", "pending", f.pendingTk},
			{"a pending account and an empty password", "pending", ""},
			{"an account that is reset, with the old password", "reset", mgrPassword("reset")},
			{"a disabled account with its password", "off", mgrPassword("off")},
			{"a row that cannot be read", "broken", mgrPassword("broken")},
			{"a password of more than 8 KiB", "active", strings.Repeat("a", maxTypedPasswordBytes+1)},
			{"a password of more than 8 KiB that starts with the password", "active", mgrPassword("active") + strings.Repeat("a", maxTypedPasswordBytes)},
		}
		for _, tt := range tests {
			p, got, err := e.m.Login(t.Context(), tt.login, tt.password, f.token)
			if !errors.Is(err, ErrBadCredentials) || err.Error() != sessWrong || !reflect.DeepEqual(p, Principal{}) || got != "" {
				t.Errorf("Login() with %s = %+v, %v, want ErrBadCredentials and no token", tt.name, p, err)
			}
		}
		e.unchanged(t, before)
		if !e.hasSession(t, f.token) {
			t.Error("a login that failed removed the session of the token that it was given")
		}
	})

	t.Run("a closed store", func(t *testing.T) {
		e := setup(t).e
		if err := e.s.Close(); err != nil {
			t.Fatal(err)
		}
		p, token, err := e.m.Login(t.Context(), "active", mgrPassword("active"), "")
		if err == nil || errors.Is(err, ErrBadCredentials) || !reflect.DeepEqual(p, Principal{}) || token != "" {
			t.Errorf("Login() on a closed store = %+v, %v, want the error of the store and no token", p, err)
		}
	})

	t.Run("a canceled context", func(t *testing.T) {
		e := setup(t).e
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		before := e.mark(t)
		p, token, err := e.m.Login(ctx, "active", mgrPassword("active"), "")
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrBadCredentials) || !reflect.DeepEqual(p, Principal{}) || token != "" {
			t.Errorf("Login() with a canceled context = %+v, %v, want context.Canceled", p, err)
		}
		e.unchanged(t, before)
	})

	// The context ends after the password has matched: the Update that would write the session is what fails.
	t.Run("the context ends after the match", func(t *testing.T) {
		for _, point := range []string{"verified", "session"} {
			e := setup(t).e
			ctx, cancel := context.WithCancel(t.Context())
			e.m.hook = func(p string) {
				if p == point {
					cancel()
				}
			}
			before := e.mark(t)
			p, token, err := e.m.Login(ctx, "active", mgrPassword("active"), "")
			if !errors.Is(err, context.Canceled) || errors.Is(err, ErrBadCredentials) || !reflect.DeepEqual(p, Principal{}) || token != "" {
				t.Errorf("Login() with a context that ends at %q = %+v, %v, want context.Canceled", point, p, err)
			}
			e.unchanged(t, before)
		}
	})

	t.Run("no random bits", func(t *testing.T) {
		e := setup(t).e
		boom := errors.New("no random bits")
		e.m.rand = iotest.ErrReader(boom)
		before := e.mark(t)
		// Only a login that matched needs a token: a login that fails reads nothing.
		if _, _, err := e.m.Login(t.Context(), "active", mgrPassword("other"), ""); !errors.Is(err, ErrBadCredentials) {
			t.Errorf("Login() with a wrong password = %v, want ErrBadCredentials", err)
		}
		p, token, err := e.m.Login(t.Context(), "active", mgrPassword("active"), "")
		if !errors.Is(err, boom) || errors.Is(err, ErrBadCredentials) || !strings.Contains(err.Error(), "cannot read a token") ||
			!reflect.DeepEqual(p, Principal{}) || token != "" {
			t.Errorf("Login() without random bits = %+v, %v, want the error of the reader as a token", p, err)
		}
		e.unchanged(t, before)
	})

	t.Run("the token is already in use", func(t *testing.T) {
		f := setup(t)
		e := f.e
		e.m.rand = sessSame{}
		_, first, err := e.m.Login(t.Context(), "active", mgrPassword("active"), "")
		if err != nil {
			t.Fatalf("Login() = %v", err)
		}
		before := e.mark(t)
		// A source of random bits that repeats must not give a second account the session of the first.
		p, token, err := e.m.Login(t.Context(), "admin", mgrPassword("admin"), "")
		if err == nil || errors.Is(err, ErrBadCredentials) || !reflect.DeepEqual(p, Principal{}) || token != "" {
			t.Errorf("Login() with a token that is in use = %+v, %v, want an error and no token", p, err)
		}
		e.unchanged(t, before)
		if s, ok := e.sessionOf(t, first); !ok || s.Account != f.active.ID {
			t.Error("the first session is gone or is not that of active")
		}
	})

	t.Run("a clock the row cannot hold", func(t *testing.T) {
		for name, at := range map[string]time.Time{
			"the year 10000": time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
			"no time":        {},
		} {
			e := setup(t).e
			e.clock.set(at)
			before := e.mark(t)
			p, token, err := e.m.Login(t.Context(), "active", mgrPassword("active"), "")
			if err == nil || errors.Is(err, ErrBadCredentials) || !reflect.DeepEqual(p, Principal{}) || token != "" {
				t.Errorf("Login() with the clock at %s = %+v, %v, want an error and no token", name, p, err)
			}
			e.unchanged(t, before)
		}
	})
}

// TestPasswordCompared checks PasswordCompared on the errors it is asked about.
func TestPasswordCompared(t *testing.T) {
	boom := errors.New("boom")
	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{"no error", nil, false},
		{"an error", boom, false},
		{"the end of a context", context.Canceled, false},
		{"a deadline, wrapped", fmt.Errorf("waiting: %w", context.DeadlineExceeded), false},
		{"ErrBadCredentials", ErrBadCredentials, true},
		{"ErrBadCredentials, wrapped", fmt.Errorf("login: %w", ErrBadCredentials), true},
		{"ErrBadCredentials, joined", errors.Join(boom, ErrBadCredentials), true},
		{"an error after the comparison", comparedError{boom}, true},
		{"an error after the comparison, wrapped", fmt.Errorf("login: %w", comparedError{boom}), true},
		{"an error after the comparison, joined", errors.Join(context.Canceled, comparedError{boom}), true},
		{"compared() of an error", compared(boom), true},
		{"compared() of the end of a context", compared(context.Canceled), true},
		{"compared() of ErrBadCredentials", compared(ErrBadCredentials), true},
	} {
		if got := PasswordCompared(tt.err); got != tt.want {
			t.Errorf("PasswordCompared() of %s = %v, want %v", tt.name, got, tt.want)
		}
	}

	// compared() keeps the text and the chain of the error, and leaves ErrBadCredentials as it is.
	marked := compared(boom)
	var c comparedError
	if !errors.As(marked, &c) || !errors.Is(marked, boom) || c.err != boom || marked.Error() != "boom" { //nolint:errorlint // the chain is what is tested
		t.Errorf("compared() of an error = %#v, want one that says boom, is boom and unwraps to it", marked)
	}
	if kept := compared(ErrBadCredentials); errors.As(kept, &c) || !errors.Is(kept, ErrBadCredentials) || kept.Error() != sessWrong {
		t.Errorf("compared() of ErrBadCredentials = %v, want ErrBadCredentials as it is", kept)
	}
}

// TestLoginPasswordCompared checks which errors of Login say that the password was compared, and that they keep their text and
// their chain: the errors before the comparison do not, and neither does the store that fails then.
func TestLoginPasswordCompared(t *testing.T) {
	boom := errors.New("no random bits")
	right, wrong := mgrPassword("yamada"), mgrPassword("other")
	type fixture struct {
		e      *mgrEnv
		yamada Account
	}
	// ends returns a context that ends at the point of the hooks of the Manager.
	ends := func(f fixture, point string) context.Context {
		ctx, cancel := context.WithCancel(t.Context())
		f.e.m.hook = func(p string) {
			if p == point {
				cancel()
			}
		}
		return ctx
	}
	login := func(ctx context.Context, f fixture, login, password string) error {
		_, _, err := f.e.m.Login(ctx, login, password, "")
		return err
	}
	// stale makes the hash of yamada one that a login makes again.
	stale := func(f fixture) { f.e.useHash(t, f.yamada.ID, right, pwLow-1) }

	for _, tt := range []struct {
		name     string
		run      func(f fixture) error
		compared bool
		is       error  // what the error is, if anything
		text     string // its text, or what it contains if it starts with ~, or anything if it is only ~
	}{
		{"a wrong password", func(f fixture) error { return login(t.Context(), f, "yamada", wrong) }, true, ErrBadCredentials, sessWrong},
		{"no such login name", func(f fixture) error { return login(t.Context(), f, "nobody", right) }, true, ErrBadCredentials, sessWrong},
		{"a password of more than 8 KiB", func(f fixture) error {
			return login(t.Context(), f, "yamada", strings.Repeat("a", maxTypedPasswordBytes+1))
		}, true, ErrBadCredentials, sessWrong},
		{"a closed store", func(f fixture) error {
			if err := f.e.s.Close(); err != nil {
				t.Fatal(err)
			}
			return login(t.Context(), f, "yamada", right)
		}, false, nil, "~"},
		{"a context that has ended", func(f fixture) error {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			return login(ctx, f, "yamada", right)
		}, false, context.Canceled, "context canceled"},
		{"a context that ends while the password is hashed", func(f fixture) error {
			ctx, cancel := context.WithCancel(t.Context())
			f.e.m.hook = func(p string) {
				if p == "hashing" {
					cancel()
				}
			}
			return login(ctx, f, "yamada", right)
		}, true, context.Canceled, "context canceled"},
		{"a context that ends after the match", func(f fixture) error {
			return login(ends(f, "verified"), f, "yamada", right)
		}, true, context.Canceled, "context canceled"},
		{"a context that ends before the session is written", func(f fixture) error {
			return login(ends(f, "session"), f, "yamada", right)
		}, true, context.Canceled, "context canceled"},
		{"no random bits for the token", func(f fixture) error {
			f.e.m.rand = iotest.ErrReader(boom)
			return login(t.Context(), f, "yamada", right)
		}, true, boom, "cannot read a token: no random bits"},
		{"a token that is in use", func(f fixture) error {
			f.e.m.rand = sessSame{}
			if err := login(t.Context(), f, "admin", mgrPassword("admin")); err != nil {
				t.Fatal(err)
			}
			return login(t.Context(), f, "yamada", right)
		}, true, nil, "cannot make a session: the token is already in use"},
		{"a clock the row cannot hold", func(f fixture) error {
			f.e.clock.set(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))
			return login(t.Context(), f, "yamada", right)
		}, true, nil, "~year outside of range"},
		{"a password that changes after the match", func(f fixture) error {
			f.e.m.hook = func(p string) {
				if p == "session" {
					if err := f.e.m.ChangePassword(t.Context(), f.yamada.ID, right, "yamada has a new password"); err != nil {
						t.Error(err)
					}
				}
			}
			return login(t.Context(), f, "yamada", right)
		}, true, ErrBadCredentials, sessWrong},
		{"no random bits for a new hash", func(f fixture) error {
			stale(f)
			f.e.m.rand = iotest.ErrReader(boom)
			return login(t.Context(), f, "yamada", right)
		}, true, boom, "cannot read a salt: no random bits"},
		{"a context that ends before a new hash", func(f fixture) error {
			stale(f)
			return login(ends(f, "verified"), f, "yamada", right)
		}, true, context.Canceled, "context canceled"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newMgr(t)
			e.first(t)
			f := fixture{e, e.add(t, "yamada", RoleEditor)}
			err := tt.run(f)
			if err == nil {
				t.Fatal("Login() succeeded, want an error")
			}
			if got := PasswordCompared(err); got != tt.compared {
				t.Errorf("PasswordCompared(%q) = %v, want %v", err, got, tt.compared)
			}
			if tt.is != nil && !errors.Is(err, tt.is) {
				t.Errorf("the error %q is not %q", err, tt.is)
			}
			if errors.Is(err, ErrBadCredentials) != errors.Is(tt.is, ErrBadCredentials) {
				t.Errorf("the error %q is ErrBadCredentials = %v, want %v", err, !errors.Is(tt.is, ErrBadCredentials), errors.Is(tt.is, ErrBadCredentials))
			}
			switch {
			case tt.text == "~":
			case strings.HasPrefix(tt.text, "~"):
				if !strings.Contains(err.Error(), tt.text[1:]) {
					t.Errorf("the error says %q, want it to contain %q", err, tt.text[1:])
				}
			case err.Error() != tt.text:
				t.Errorf("the error says %q, want %q", err, tt.text)
			}
			// An error after the comparison is marked, keeps its text, and unwraps to the error that it marks. ErrBadCredentials
			// is the answer to a password that does not match, and is not wrapped.
			var c comparedError
			marked := errors.As(err, &c)
			if want := tt.compared && !errors.Is(err, ErrBadCredentials); marked != want {
				t.Errorf("the error %q is marked = %v, want %v", err, marked, want)
			}
			if marked && (c.err == nil || errors.Unwrap(err) != c.err || err.Error() != c.err.Error()) { //nolint:errorlint // the chain is what is tested
				t.Errorf("the marked error %q does not unwrap to the error that it marks", err)
			}
		})
	}
}

// TestLoginChecksAgain runs another operation after the password has matched and before the session is written, and
// checks that the login goes by the account as it is then: a new password, a reset, a disabling and a deletion fail it,
// and nothing is written; a change of another field shows in the account that it returns.
func TestLoginChecksAgain(t *testing.T) {
	const second = "yamada has a new password now"
	setup := func(t *testing.T) (e *mgrEnv, admin, a Account) {
		e = newMgr(t)
		admin = e.first(t)
		a = e.add(t, "yamada", RoleEditor)
		return e, admin, a
	}
	// attempt logs in as yamada with the right password and runs gap once, after the password matched. The mark is the
	// state of the store after the gap.
	attempt := func(t *testing.T, e *mgrEnv, gap func()) (p Principal, token string, after mgrMark, err error) {
		t.Helper()
		var ran bool
		e.m.hook = func(point string) {
			if point == "session" && !ran {
				ran = true
				gap()
				after = e.mark(t)
			}
		}
		p, token, err = e.m.Login(t.Context(), "yamada", mgrPassword("yamada"), "")
		if !ran {
			t.Fatal("the password was not checked")
		}
		return p, token, after, err
	}
	type gap func(t *testing.T, e *mgrEnv, admin, a Account)
	check := func(t *testing.T, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("the operation between the match and the session = %v", err)
		}
	}

	fails := []struct {
		name string
		gap  gap
	}{
		{"the password is changed", func(t *testing.T, e *mgrEnv, _, a Account) {
			check(t, e.m.ChangePassword(t.Context(), a.ID, mgrPassword("yamada"), second))
		}},
		{"the password is changed to the same one", func(t *testing.T, e *mgrEnv, _, a Account) {
			check(t, e.m.ChangePassword(t.Context(), a.ID, mgrPassword("yamada"), mgrPassword("yamada")))
		}},
		{"the password is reset by an administrator", func(t *testing.T, e *mgrEnv, admin, a Account) {
			_, err := e.m.Reset(t.Context(), By(admin.ID), a.ID)
			check(t, err)
		}},
		{"the password is reset by System", func(t *testing.T, e *mgrEnv, _, a Account) {
			_, err := e.m.Reset(t.Context(), System, a.ID)
			check(t, err)
		}},
		{"the password is reset and set again", func(t *testing.T, e *mgrEnv, _, a Account) {
			token, err := e.m.Reset(t.Context(), System, a.ID)
			check(t, err)
			_, err = e.m.SetPassword(t.Context(), token, second)
			check(t, err)
		}},
		{"the account is disabled by an administrator", func(t *testing.T, e *mgrEnv, admin, a Account) {
			_, err := e.m.Disable(t.Context(), By(admin.ID), a.ID)
			check(t, err)
		}},
		{"the account is disabled by System", func(t *testing.T, e *mgrEnv, _, a Account) {
			_, err := e.m.Disable(t.Context(), System, a.ID)
			check(t, err)
		}},
		{"the account is deleted by an administrator", func(t *testing.T, e *mgrEnv, admin, a Account) {
			check(t, e.m.Delete(t.Context(), By(admin.ID), a.ID))
		}},
		{"the account is deleted by System", func(t *testing.T, e *mgrEnv, _, a Account) {
			check(t, e.m.Delete(t.Context(), System, a.ID))
		}},
		{"the row cannot be read", func(t *testing.T, e *mgrEnv, _, a Account) {
			e.putRaw(t, a.ID, "not json")
		}},
	}
	for _, tt := range fails {
		t.Run(tt.name, func(t *testing.T) {
			e, admin, a := setup(t)
			p, token, after, err := attempt(t, e, func() { tt.gap(t, e, admin, a) })
			if !errors.Is(err, ErrBadCredentials) || err.Error() != sessWrong || !reflect.DeepEqual(p, Principal{}) || token != "" {
				t.Errorf("Login() = %+v, %v, want ErrBadCredentials and no token", p, err)
			}
			e.unchanged(t, after)
		})
	}

	works := []struct {
		name    string
		gap     gap
		differs bool // the account that is returned is not the one that the first look found
	}{
		{"the name is changed", func(t *testing.T, e *mgrEnv, _, a Account) {
			_, err := e.m.Update(t.Context(), By(a.ID), a.ID, Change{Name: ptr("New Name")})
			check(t, err)
		}, true},
		{"the role is changed", func(t *testing.T, e *mgrEnv, _, a Account) {
			e.edit(t, a.ID, func(r *row) { r.Role = RoleViewer })
		}, true},
		{"the email address is changed", func(t *testing.T, e *mgrEnv, _, a Account) {
			_, err := e.m.Update(t.Context(), By(a.ID), a.ID, Change{Email: ptr("new.address@example.com")})
			check(t, err)
		}, true},
		{"the login name is changed", func(t *testing.T, e *mgrEnv, admin, a Account) {
			_, err := e.m.Update(t.Context(), By(admin.ID), a.ID, Change{Login: ptr("taro")})
			check(t, err)
		}, true},
		{"the time of the credentials changes and the password does not", func(t *testing.T, e *mgrEnv, _, a Account) {
			e.edit(t, a.ID, func(r *row) { r.CredentialsChangedAt = r.CredentialsChangedAt.Add(time.Hour) })
		}, true},
		{"the account is disabled and enabled again", func(t *testing.T, e *mgrEnv, _, a Account) {
			e.edit(t, a.ID, func(r *row) { r.Disabled = true })
			e.edit(t, a.ID, func(r *row) { r.Disabled = false })
		}, false},
		{"another account is invited", func(t *testing.T, e *mgrEnv, _, _ Account) {
			_, _, err := e.m.Invite(t.Context(), System, mgrInput("taro", RoleViewer))
			check(t, err)
		}, false},
		{"the password of another account is changed", func(t *testing.T, e *mgrEnv, admin, _ Account) {
			check(t, e.m.ChangePassword(t.Context(), admin.ID, mgrPassword("admin"), second))
		}, false},
	}
	for _, tt := range works {
		t.Run(tt.name, func(t *testing.T) {
			e, admin, a := setup(t)
			p, token, _, err := attempt(t, e, func() { tt.gap(t, e, admin, a) })
			now := e.row(t, a.ID)
			if err != nil || !reflect.DeepEqual(p.Account, now.account()) {
				t.Fatalf("Login() = %+v, %v, want the account as it is now, %+v", p, err, now.account())
			}
			if tt.differs && p.Account == a {
				t.Error("Login() returned the account as the first look found it")
			}
			// The session keeps the credentials that the account has when it is written.
			s, ok := e.sessionOf(t, token)
			if !ok || s.Account != a.ID || !s.CredentialsChangedAt.Equal(now.CredentialsChangedAt) {
				t.Errorf("the session of the login is %+v, %v, want one of %s with the credentials of the account as it is now", s, ok, a.ID)
			}
		})
	}
}

// TestLoginHashes checks that every login costs one hash, and a password of more than 8 KiB none, whatever the reason to
// fail, and that the other operations that hash go through the same gate.
func TestLoginHashes(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	active := e.add(t, "active", RoleEditor)
	_, pendingToken := e.invite(t, "pending", RoleEditor)
	off := e.add(t, "off", RoleEditor)
	if _, err := e.m.Disable(t.Context(), System, off.ID); err != nil {
		t.Fatal(err)
	}
	var hashing, verified int
	e.m.hook = func(point string) {
		switch point {
		case "hashing":
			hashing++
		case "verified":
			verified++
		}
	}
	long := strings.Repeat("a", maxTypedPasswordBytes+1)
	tests := []struct {
		name, login, password string
		hashes                int
		works                 bool
	}{
		{"the right password", "active", mgrPassword("active"), 1, true},
		{"a wrong password", "active", mgrPassword("other"), 1, false},
		{"no such login name", "nobody", mgrPassword("nobody"), 1, false},
		{"an empty login name", "", mgrPassword("active"), 1, false},
		{"a pending account", "pending", mgrPassword("pending"), 1, false},
		{"a pending account with its token", "pending", pendingToken, 1, false},
		{"a disabled account", "off", mgrPassword("off"), 1, false},
		{"8 KiB", "active", strings.Repeat("a", maxTypedPasswordBytes), 1, false},
		{"more than 8 KiB for an account", "active", long, 0, false},
		{"more than 8 KiB for no account", "nobody", long, 0, false},
	}
	for _, tt := range tests {
		hashing, verified = 0, 0
		_, _, err := e.m.Login(t.Context(), tt.login, tt.password, "")
		if (err == nil) != tt.works || err != nil && !errors.Is(err, ErrBadCredentials) {
			t.Errorf("Login() with %s = %v, want nil = %v, and else ErrBadCredentials", tt.name, err, tt.works)
		}
		if hashing != tt.hashes || verified != 1 {
			t.Errorf("Login() with %s computed %d hashes and checked %d times, want %d and 1", tt.name, hashing, verified, tt.hashes)
		}
		hashing = 0
		_, err = e.m.VerifyPasswordFactor(t.Context(), tt.login, tt.password)
		if (err == nil) != tt.works || hashing != tt.hashes {
			t.Errorf("VerifyPasswordFactor() with %s = %v after %d hashes, want nil = %v after %d", tt.name, err, hashing, tt.works, tt.hashes)
		}
	}

	// The operations that make or check a hash of a typed password.
	counts := []struct {
		name   string
		do     func() error
		hashes int
	}{
		{"ChangePassword with a wrong password", func() error {
			return e.m.ChangePassword(t.Context(), active.ID, mgrPassword("other"), "a new long password")
		}, 1},
		{"ChangePassword with a current password of more than 8 KiB", func() error {
			return e.m.ChangePassword(t.Context(), active.ID, long, "a new long password")
		}, 0},
		{"ChangePassword", func() error {
			return e.m.ChangePassword(t.Context(), active.ID, mgrPassword("active"), "a new long password")
		}, 2},
		{"SetPassword", func() error {
			_, err := e.m.SetPassword(t.Context(), pendingToken, mgrPassword("pending"))
			return err
		}, 1},
		{"Invite", func() error {
			_, _, err := e.m.Invite(t.Context(), System, mgrInput("taro", RoleViewer))
			return err
		}, 0},
		{"Reset", func() error {
			_, err := e.m.Reset(t.Context(), System, active.ID)
			return err
		}, 0},
	}
	for _, tt := range counts {
		hashing = 0
		if err := tt.do(); err != nil && !errors.Is(err, ErrBadCredentials) {
			t.Errorf("%s = %v", tt.name, err)
		}
		if hashing != tt.hashes {
			t.Errorf("%s computed %d hashes, want %d", tt.name, hashing, tt.hashes)
		}
	}
	e2 := newMgr(t)
	e2.m.hook = e.m.hook
	hashing = 0
	e2.first(t)
	if hashing != 1 {
		t.Errorf("CreateFirst computed %d hashes, want 1", hashing)
	}
}

// TestLoginUsesNothingUnusable makes the hash that a login with no usable account is checked against match a password,
// and checks that the password still logs nobody in: not as that login, and not as an account that is pending or
// disabled.
func TestLoginUsesNothingUnusable(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	e.invite(t, "pending", RoleEditor)
	off := e.add(t, "off", RoleEditor)
	if _, err := e.m.Disable(t.Context(), System, off.ID); err != nil {
		t.Fatal(err)
	}
	const guess = "the password of the hash for nobody"
	e.m.unknownHash = mgrHashed(t, guess)
	before := e.mark(t)
	for _, login := range []string{"nobody", "pending", "off", ""} {
		p, token, err := e.m.Login(t.Context(), login, guess, "")
		if !errors.Is(err, ErrBadCredentials) || !reflect.DeepEqual(p, Principal{}) || token != "" {
			t.Errorf("Login() of %q = %+v, %v, want ErrBadCredentials and no token", login, p, err)
		}
	}
	e.unchanged(t, before)
}

// TestLoginPrevious checks which sessions a login removes besides its own.
func TestLoginPrevious(t *testing.T) {
	setup := func(t *testing.T) *mgrEnv {
		e := newMgr(t)
		e.first(t)
		e.add(t, "yamada", RoleEditor)
		return e
	}
	loginWith := func(t *testing.T, e *mgrEnv, login, previous string) string {
		t.Helper()
		_, token, err := e.m.Login(t.Context(), login, mgrPassword(login), previous)
		if err != nil {
			t.Fatalf("Login(%q) = %v", login, err)
		}
		return token
	}

	t.Run("the session of previous is replaced", func(t *testing.T) {
		e := setup(t)
		_, t1 := e.login(t, "yamada")
		_, t2 := e.login(t, "yamada")
		t3 := loginWith(t, e, "yamada", t1)
		if e.hasSession(t, t1) || !e.hasSession(t, t2) || !e.hasSession(t, t3) || len(e.rawSessions(t)) != 2 {
			t.Errorf("after a login with the first token as previous, the first session is %v, the second %v, the new %v, and there are %d rows, want false, true, true, 2",
				e.hasSession(t, t1), e.hasSession(t, t2), e.hasSession(t, t3), len(e.rawSessions(t)))
		}
	})

	t.Run("the session of another account", func(t *testing.T) {
		e := setup(t)
		_, ty := e.login(t, "yamada")
		_, ta := e.login(t, "admin")
		tn := loginWith(t, e, "admin", ty)
		if e.hasSession(t, ty) || !e.hasSession(t, ta) || !e.hasSession(t, tn) {
			t.Errorf("after the administrator logged in with a session of yamada as previous, that session is %v, the administrator's first %v, the new %v, want false, true, true",
				e.hasSession(t, ty), e.hasSession(t, ta), e.hasSession(t, tn))
		}
	})

	t.Run("what is not a session", func(t *testing.T) {
		for _, previous := range []string{"", "junk", tokSample[:42], tokSample + "A", tokSample, strings.Repeat("A", 43)} {
			e := setup(t)
			_, kept := e.login(t, "yamada")
			token := loginWith(t, e, "yamada", previous)
			if !e.hasSession(t, kept) || !e.hasSession(t, token) || len(e.rawSessions(t)) != 2 {
				t.Errorf("a login with a previous that is no session of the store removed or lost a session, there are %d rows, want 2", len(e.rawSessions(t)))
			}
		}
	})

	// The session of previous has expired, so the sweep has removed it before it is looked for.
	t.Run("a session that has expired", func(t *testing.T) {
		e := setup(t)
		_, old := e.login(t, "yamada")
		e.clock.add(SessionIdleTimeout)
		token := loginWith(t, e, "yamada", old)
		if e.hasSession(t, old) || !e.hasSession(t, token) || len(e.rawSessions(t)) != 1 {
			t.Errorf("there are %d rows, the old session is %v, want one row, the new one", len(e.rawSessions(t)), e.hasSession(t, old))
		}
	})

	t.Run("at the limit", func(t *testing.T) {
		e := setup(t)
		var tokens []string
		for range SessionsPerAccount {
			e.clock.add(time.Minute)
			_, token := e.login(t, "yamada")
			tokens = append(tokens, token)
		}
		// Replacing a session that is not the oldest removes it and no other.
		e.clock.add(time.Minute)
		fresh := loginWith(t, e, "yamada", tokens[4])
		for i, token := range tokens {
			if want := i != 4; e.hasSession(t, token) != want {
				t.Errorf("session %d: has a row = %v, want %v", i, e.hasSession(t, token), want)
			}
		}
		if !e.hasSession(t, fresh) || len(e.rawSessions(t)) != SessionsPerAccount {
			t.Errorf("there are %d rows, want %d with the new session", len(e.rawSessions(t)), SessionsPerAccount)
		}
	})
}

// TestLoginSweep checks which rows a login removes: those that cannot work, of any account, and no other.
func TestLoginSweep(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	accounts := make(map[string]Account)
	for _, name := range []string{"login", "live", "other", "disabled", "pending", "changed", "deleted", "broken"} {
		accounts[name] = e.add(t, name, RoleEditor)
	}
	now := mgrTime.Add(30 * time.Hour)
	e.clock.set(now.Add(-10 * time.Minute))
	tokens := make(map[string]string)
	for _, name := range []string{"live", "other", "disabled", "pending", "changed", "deleted", "broken"} {
		_, tokens[name] = e.login(t, name)
	}
	e.clock.set(now)
	live := e.row(t, accounts["live"].ID)

	// Rows that work and rows that do not, around the boundaries to the nanosecond.
	crafted := make(map[string]sessionRow)
	craft := func(name string, f func(s *sessionRow)) {
		s := sessionFor(live, now.Add(-time.Minute))
		f(&s)
		crafted[name] = s
		e.setSession(t, sessKey(name), s)
	}
	craft("idle edge", func(s *sessionRow) { s.LastSeenAt = now.Add(-SessionIdleTimeout + 1) })
	craft("idle over", func(s *sessionRow) { s.LastSeenAt = now.Add(-SessionIdleTimeout) })
	craft("lifetime edge", func(s *sessionRow) { s.AuthenticatedAt = now.Add(-SessionLifetime + 1) })
	craft("lifetime over", func(s *sessionRow) { s.AuthenticatedAt = now.Add(-SessionLifetime) })
	craft("created long ago", func(s *sessionRow) {
		s.CreatedAt, s.AuthenticatedAt = now.Add(-SessionLifetime-time.Hour), now.Add(-time.Hour)
	})
	craft("another account", func(s *sessionRow) { s.Account = accounts["other"].ID })
	craft("an account that is not there", func(s *sessionRow) { s.Account = mgrMissingID })
	craft("credentials of another time", func(s *sessionRow) { s.CredentialsChangedAt = live.CredentialsChangedAt.Add(time.Nanosecond) })
	craft("credentials of no time", func(s *sessionRow) { s.CredentialsChangedAt = time.Time{} })
	crafted["garbage"] = sessionRow{}
	e.setRawSession(t, sessKey("garbage"), "not json")
	crafted["unknown key"] = sessionRow{}
	e.setRawSession(t, sessKey("unknown key"), `{"account":"`+live.ID+`","extra":1}`)
	crafted["empty factors"] = sessionRow{}
	e.setRawSession(t, sessKey("empty factors"),
		strings.Replace(sessionJSON(live.ID, "2026-11-09T17:00:00Z", "2026-11-09T17:00:00Z", "2026-11-09T17:00:00Z", ""), `["password"]`, `[]`, 1))

	// The accounts change under their sessions with the store's own API, so that no operation removes a session.
	e.edit(t, accounts["disabled"].ID, func(r *row) { r.Disabled = true })
	e.edit(t, accounts["pending"].ID, func(r *row) { r.Password = "" })
	e.edit(t, accounts["changed"].ID, func(r *row) { r.CredentialsChangedAt = r.CredentialsChangedAt.Add(time.Second) })
	e.deleteRow(t, accounts["deleted"].ID)
	e.putRaw(t, accounts["broken"].ID, "not json")
	// A row that cannot be read does not stop the account that logs in, which is read as usual.
	_, token, err := e.m.Login(t.Context(), "login", mgrPassword("login"), "")
	if err != nil {
		t.Fatalf("Login() = %v", err)
	}

	got := e.rawSessions(t)
	want := map[string]bool{
		sessKey(tokens["live"]): true, sessKey(tokens["other"]): true, sessKey("idle edge"): true, sessKey("lifetime edge"): true,
		sessKey("another account"): true, sessKey("created long ago"): true, sessKey(token): true,
	}
	for key := range got {
		if !want[key] {
			t.Errorf("a row that cannot work stayed: %s", rowName(key, tokens, crafted))
		}
	}
	for key := range want {
		if _, ok := got[key]; !ok {
			t.Errorf("a row that works was removed: %s", rowName(key, tokens, crafted))
		}
	}
	// What stayed is what it was.
	if s, ok := e.sessionOf(t, tokens["live"]); !ok || !s.LastSeenAt.Equal(now.Add(-10*time.Minute)) {
		t.Error("the session of live changed")
	}
}

// rowName names a key of the table sessions for a failure, by the token or the crafted row that it belongs to: a key is
// the hash of a token, and the tests never print one.
func rowName(key string, tokens map[string]string, crafted map[string]sessionRow) string {
	for name, token := range tokens {
		if sessKey(token) == key {
			return "the session of " + name
		}
	}
	for name := range crafted {
		if sessKey(name) == key {
			return "the crafted row " + name
		}
	}
	return "a row that is none of those"
}

// TestSessionsPerAccount checks the limit of ten sessions to an account: the one used least recently goes, by the time of
// its last request, then of its creation, then by its key, and only the account's own sessions count.
func TestSessionsPerAccount(t *testing.T) {
	t.Run("the least recently used goes", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		e.add(t, "yamada", RoleEditor)
		var admin, tokens []string
		for range 3 {
			e.clock.add(time.Second)
			_, token := e.login(t, "admin")
			admin = append(admin, token)
		}
		for range SessionsPerAccount {
			e.clock.add(time.Minute)
			_, token := e.login(t, "yamada")
			tokens = append(tokens, token)
		}
		if rows := e.rawSessions(t); len(rows) != SessionsPerAccount+3 {
			t.Fatalf("there are %d rows after %d logins of yamada and 3 of the administrator, want %d", len(rows), SessionsPerAccount, SessionsPerAccount+3)
		}
		check := func(removed []string, fresh ...string) {
			t.Helper()
			for i, token := range tokens {
				if want := !slices.Contains(removed, token); e.hasSession(t, token) != want {
					t.Errorf("session %d of yamada: has a row = %v, want %v", i, e.hasSession(t, token), want)
				}
			}
			for _, token := range append(slices.Clone(fresh), admin...) {
				if !e.hasSession(t, token) {
					t.Error("a session that is newer, or of another account, was removed")
				}
			}
			if n := len(e.rawSessions(t)); n != SessionsPerAccount+3 {
				t.Errorf("there are %d rows, want %d: the limit of yamada and the sessions of the administrator", n, SessionsPerAccount+3)
			}
		}
		e.clock.add(time.Minute)
		_, eleventh := e.login(t, "yamada")
		check(tokens[:1], eleventh)

		// A request of the second session, which is old enough to be written, makes it newer than the third.
		e.clock.add(sessionTouch)
		if _, err := e.m.Authenticate(t.Context(), tokens[1]); err != nil {
			t.Fatal(err)
		}
		_, twelfth := e.login(t, "yamada")
		check([]string{tokens[0], tokens[2]}, eleventh, twelfth)
	})

	t.Run("ties", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		yamada := e.row(t, e.add(t, "yamada", RoleEditor).ID)
		now := mgrTime.Add(10 * time.Minute)
		e.clock.set(now)
		seen, created := now.Add(-5*time.Minute), now.Add(-8*time.Minute)
		low, high := sessKey("tie a"), sessKey("tie b")
		if low > high {
			low, high = high, low
		}
		type spec struct {
			key           string
			created, seen time.Time
		}
		// These three go in this order: with the same last request and creation, by the key; then one that was created
		// later.
		out := []spec{{low, created, seen}, {high, created, seen}, {sessKey("tie c"), created.Add(time.Minute), seen}}
		for _, s := range out {
			row := sessionFor(yamada, s.created)
			row.LastSeenAt = s.seen
			e.setSession(t, s.key, row)
		}
		keep := make(map[string]bool)
		for i := range SessionsPerAccount - len(out) {
			row := sessionFor(yamada, created)
			row.LastSeenAt = seen.Add(time.Duration(i+1) * time.Second)
			key := sessKey("later " + strconv.Itoa(i))
			e.setSession(t, key, row)
			keep[key] = true
		}
		for i := range out {
			e.login(t, "yamada")
			rows := e.rawSessions(t)
			if len(rows) != SessionsPerAccount {
				t.Fatalf("login %d: there are %d rows, want %d", i+1, len(rows), SessionsPerAccount)
			}
			for j, s := range out {
				if _, there := rows[s.key]; there != (j > i) {
					t.Errorf("after login %d, the tied row %d has a row = %v, want %v", i+1, j, there, j > i)
				}
			}
			for key := range keep {
				if _, ok := rows[key]; !ok {
					t.Errorf("after login %d, a row with a later request was removed", i+1)
				}
			}
		}
	})

	// Rows put past the API can be more than the limit: all but the newest go. The times fall in five groups that are mixed
	// in the order of the keys, and the keys decide within a group, whatever the size of the sort.
	t.Run("many rows with times in a few groups", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		yamada := e.row(t, e.add(t, "yamada", RoleEditor).ID)
		now := mgrTime.Add(10 * time.Minute)
		e.clock.set(now)
		type kept struct {
			key  string
			seen time.Time
		}
		const n = 100
		rows := make([]kept, n)
		for i := range rows {
			rows[i] = kept{sessKey("group " + strconv.Itoa(i)), now.Add(-time.Duration(1+i*37%5) * time.Minute)}
			s := sessionFor(yamada, now.Add(-9*time.Minute))
			s.LastSeenAt = rows[i].seen
			e.setSession(t, rows[i].key, s)
		}
		slices.SortFunc(rows, func(a, b kept) int { return cmp.Or(a.seen.Compare(b.seen), strings.Compare(a.key, b.key)) })
		e.login(t, "yamada")
		got := e.rawSessions(t)
		if len(got) != SessionsPerAccount {
			t.Fatalf("there are %d rows, want %d", len(got), SessionsPerAccount)
		}
		for i, r := range rows {
			if _, there := got[r.key]; there != (i >= n-(SessionsPerAccount-1)) {
				t.Errorf("row %d of %d, from the oldest request and the lowest key: has a row = %v, want %v", i, n, there, !there)
			}
		}
	})
}

func TestReadAccount(t *testing.T) {
	e := newMgr(t)
	admin := e.first(t)
	broken := e.add(t, "broken", RoleEditor)
	e.putRaw(t, broken.ID, "not json")
	var ended *store.Tx
	err := e.s.View(t.Context(), func(tx *store.Tx) error {
		ended = tx
		if r, ok, err := readAccount(tx, admin.ID); err != nil || !ok || r.ID != admin.ID || r.Login != "admin" {
			t.Errorf("readAccount() of an account = %v, %v, %v, want its row", r.ID, ok, err)
		}
		for name, id := range map[string]string{"an account that cannot be read": broken.ID, "an account that is not there": mgrMissingID} {
			if r, ok, err := readAccount(tx, id); err != nil || ok || r != (row{}) {
				t.Errorf("readAccount() of %s = %v, %v, %v, want no row and no error", name, r.ID, ok, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if r, ok, err := readAccount(ended, admin.ID); err == nil || err.Error() != "the transaction has ended" || ok || r != (row{}) {
		t.Errorf("readAccount() in a transaction that has ended = %v, %v, %v, want the error of the store", r.ID, ok, err)
	}
}

func TestAuthenticate(t *testing.T) {
	setup := func(t *testing.T) (e *mgrEnv, a Account, p Principal, token string) {
		t.Helper()
		e = newMgr(t)
		e.first(t)
		a = e.add(t, "yamada", RoleEditor)
		p, token = e.login(t, "yamada")
		return e, a, p, token
	}
	// ended checks that the session has ended: Authenticate says so, and the store has no row for it.
	ended := func(t *testing.T, e *mgrEnv, token, what string) {
		t.Helper()
		got, err := e.m.Authenticate(t.Context(), token)
		if !errors.Is(err, ErrNoSession) || err.Error() != "no valid session" || !reflect.DeepEqual(got, Principal{}) {
			t.Errorf("Authenticate() %s = %+v, %v, want ErrNoSession", what, got, err)
		}
		if e.hasSession(t, token) {
			t.Errorf("the row of the session stayed %s", what)
		}
	}
	works := func(t *testing.T, e *mgrEnv, token string, want Principal, what string) {
		t.Helper()
		got, err := e.m.Authenticate(t.Context(), token)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("Authenticate() %s = %+v, %v, want %+v", what, got, err, want)
		}
	}

	t.Run("a valid session", func(t *testing.T) {
		e, _, p, token := setup(t)
		before := e.mark(t)
		for _, d := range []time.Duration{0, time.Second, sessionTouch - 1} {
			e.clock.set(mgrTime.Add(d))
			works(t, e, token, p, d.String()+" after the login")
		}
		// A request that does not need to write, writes nothing.
		e.unchanged(t, before)
	})

	t.Run("the account as it is now", func(t *testing.T) {
		e, a, p, token := setup(t)
		e.clock.add(time.Minute)
		if _, err := e.m.Update(t.Context(), System, a.ID, Change{Name: ptr("New Name"), Email: ptr("new@example.com")}); err != nil {
			t.Fatal(err)
		}
		e.edit(t, a.ID, func(r *row) { r.Role = RoleViewer })
		got, err := e.m.Authenticate(t.Context(), token)
		want := e.row(t, a.ID).account()
		if err != nil || !reflect.DeepEqual(got.Account, want) || !reflect.DeepEqual(got.Session, p.Session) ||
			got.Account.Name != "New Name" || got.Account.Role != RoleViewer || got.Account.Email != "new@example.com" {
			t.Errorf("Authenticate() = %+v, %v, want the account as it is now, %+v, and the session of the login", got, err, want)
		}
	})

	t.Run("the time of the last request", func(t *testing.T) {
		e, _, p, token := setup(t)
		at := func(d time.Duration) { e.clock.set(mgrTime.Add(d)) }
		lastSeen := func() time.Time {
			s, _ := e.sessionOf(t, token)
			return s.LastSeenAt
		}
		original, _ := e.sessionOf(t, token)

		// Written when it is sessionTouch old, and not before.
		at(sessionTouch - 1)
		before := e.mark(t)
		works(t, e, token, p, "just before it is old enough")
		e.unchanged(t, before)

		at(sessionTouch)
		before = e.mark(t)
		works(t, e, token, p, "when it is old enough")
		if after := e.mark(t); after.size <= before.size {
			t.Error("the time of the last request is not written when it is old enough")
		}
		touched := original
		touched.LastSeenAt = mgrTime.Add(sessionTouch)
		if got, _ := e.sessionOf(t, token); !reflect.DeepEqual(got, touched) {
			t.Errorf("the row after the request is %+v, want the row of the login with only the last request at %v", got, touched.LastSeenAt)
		}

		// Counted from the time that was written.
		at(2*sessionTouch - 1)
		before = e.mark(t)
		works(t, e, token, p, "just before the written time is old enough")
		e.unchanged(t, before)
		at(2 * sessionTouch)
		works(t, e, token, p, "when the written time is old enough")
		if got := lastSeen(); !got.Equal(mgrTime.Add(2 * sessionTouch)) {
			t.Errorf("the last request is %v, want %v", got, mgrTime.Add(2*sessionTouch))
		}

		// It never goes back, whatever the clock does.
		for _, d := range []time.Duration{sessionTouch, 0, -time.Hour} {
			at(d)
			before = e.mark(t)
			works(t, e, token, p, "after the clock went back to "+d.String())
			e.unchanged(t, before)
			if got := lastSeen(); !got.Equal(mgrTime.Add(2 * sessionTouch)) {
				t.Errorf("after the clock went back to %v the last request is %v, want %v", d, got, mgrTime.Add(2*sessionTouch))
			}
		}
	})

	t.Run("requests that find the time old write it once", func(t *testing.T) {
		e, _, p, token := setup(t)
		e.clock.add(sessionTouch)
		m0 := e.mark(t)
		works(t, e, token, p, "the first time that the time is old")
		m1 := e.mark(t)
		one := m1.size - m0.size
		if one <= 0 {
			t.Fatal("a request that finds the time old writes nothing")
		}
		e.clock.add(sessionTouch)
		const n = 8
		var arrived atomic.Int32
		release := make(chan struct{})
		// Every request has looked, and found the time old, before one of them writes.
		e.m.hook = func(point string) {
			if point == "authenticated" {
				if arrived.Add(1) == n {
					close(release)
				}
				<-release
			}
		}
		results := make([]Principal, n)
		calls := make([]func() error, n)
		for i := range calls {
			calls[i] = func() (err error) {
				results[i], err = e.m.Authenticate(t.Context(), token)
				return err
			}
		}
		for i, err := range mgrRunTogether(calls...) {
			if err != nil || !reflect.DeepEqual(results[i], p) {
				t.Errorf("request %d = %+v, %v, want the Principal of the login", i, results[i], err)
			}
		}
		if grown := e.mark(t).size - m1.size; grown != one {
			t.Errorf("%d requests grew the data directory by %d bytes, want %d: one write", n, grown, one)
		}
		if s, _ := e.sessionOf(t, token); !s.LastSeenAt.Equal(mgrTime.Add(2 * sessionTouch)) {
			t.Errorf("the last request is %v, want %v", s.LastSeenAt, mgrTime.Add(2*sessionTouch))
		}
	})

	t.Run("the idle timeout", func(t *testing.T) {
		e, _, p, token := setup(t)
		_, other := e.login(t, "yamada")
		e.clock.add(SessionIdleTimeout - 1)
		works(t, e, token, p, "a nanosecond before the idle timeout")
		e.clock.add(1)
		ended(t, e, other, "at the idle timeout")
		// The request of a nanosecond ago counts.
		works(t, e, token, p, "after a request a nanosecond ago")
	})

	t.Run("the lifetime", func(t *testing.T) {
		e, _, p, a := setup(t)
		_, b := e.login(t, "yamada")
		// Both are used every half hour, so that the idle timeout does not end them.
		for range 23 {
			e.clock.add(30 * time.Minute)
			works(t, e, a, p, "in use")
			works(t, e, b, p, "in use")
		}
		if got := p.Session.ExpiresAt; !got.Equal(mgrTime.Add(SessionLifetime)) {
			t.Fatalf("the session expires at %v, want %v", got, mgrTime.Add(SessionLifetime))
		}
		e.clock.add(30*time.Minute - 1)
		works(t, e, a, p, "a nanosecond before the lifetime")
		e.clock.add(1)
		ended(t, e, b, "at the lifetime")
		ended(t, e, a, "at the lifetime, though it was used a nanosecond ago")
	})

	t.Run("the factors of the session", func(t *testing.T) {
		e, a, p, _ := setup(t)
		if !slices.Equal(p.Session.Factors, []Factor{FactorPassword}) {
			t.Errorf("the factors of a login are %q, want the password", p.Session.Factors)
		}
		// Whatever the row says, in its order, as a later kind of login will write it.
		s := sessionFor(e.row(t, a.ID), mgrTime)
		s.Factors = []Factor{"totp", FactorPassword}
		e.setSession(t, sessKey(tokSample), s)
		got, err := e.m.Authenticate(t.Context(), tokSample)
		if err != nil || !slices.Equal(got.Session.Factors, []Factor{"totp", FactorPassword}) {
			t.Errorf("Authenticate() = %q, %v, want the factors of the row", got.Session.Factors, err)
		}
	})

	// A row made before the login that it records, as a later kind of login will make it: the lifetime and the time that
	// the Principal tells are those of the login.
	t.Run("the lifetime counts from the login", func(t *testing.T) {
		e, a, _, _ := setup(t)
		now := mgrTime.Add(14 * time.Hour)
		e.clock.set(now)
		s := sessionFor(e.row(t, a.ID), now.Add(-SessionLifetime-time.Hour))
		s.AuthenticatedAt = now.Add(-SessionLifetime + time.Hour)
		s.LastSeenAt = now.Add(-time.Minute)
		e.setSession(t, sessKey(tokSample), s)
		got, err := e.m.Authenticate(t.Context(), tokSample)
		if err != nil || !got.Session.AuthenticatedAt.Equal(s.AuthenticatedAt) || !got.Session.ExpiresAt.Equal(now.Add(time.Hour)) {
			t.Errorf("Authenticate() = %+v, %v, want the session of a login 11 hours ago, which ends in an hour", got.Session, err)
		}
		e.clock.add(30 * time.Minute)
		if _, err := e.m.Authenticate(t.Context(), tokSample); err != nil {
			t.Errorf("Authenticate() half an hour later = %v, want nil", err)
		}
		e.clock.add(30 * time.Minute)
		ended(t, e, tokSample, "when the lifetime from the login is over")
	})

	// The account changes under its session with the store's own API, so that no operation removes the session.
	t.Run("the account", func(t *testing.T) {
		states := []struct {
			name   string
			change func(t *testing.T, e *mgrEnv, a Account)
		}{
			{"disabled", func(t *testing.T, e *mgrEnv, a Account) { e.edit(t, a.ID, func(r *row) { r.Disabled = true }) }},
			{"pending", func(t *testing.T, e *mgrEnv, a Account) { e.edit(t, a.ID, func(r *row) { r.Password = "" }) }},
			{"with other credentials", func(t *testing.T, e *mgrEnv, a Account) {
				e.edit(t, a.ID, func(r *row) { r.CredentialsChangedAt = r.CredentialsChangedAt.Add(time.Second) })
			}},
			{"with credentials of no time", func(t *testing.T, e *mgrEnv, a Account) {
				e.edit(t, a.ID, func(r *row) { r.CredentialsChangedAt = time.Time{} })
			}},
			{"deleted", func(t *testing.T, e *mgrEnv, a Account) { e.deleteRow(t, a.ID) }},
			{"unreadable", func(t *testing.T, e *mgrEnv, a Account) { e.putRaw(t, a.ID, "not json") }},
		}
		for _, tt := range states {
			t.Run(tt.name, func(t *testing.T) {
				e, a, _, token := setup(t)
				admin, adminToken := e.login(t, "admin")
				tt.change(t, e, a)
				ended(t, e, token, "of an account that is "+tt.name)
				works(t, e, adminToken, admin, "of another account")
				// An account that is as it was again does not bring the session back.
				if tt.name == "disabled" {
					e.edit(t, a.ID, func(r *row) { r.Disabled = false })
				}
				ended(t, e, token, "after the account is as it was")
			})
		}
	})

	t.Run("a row that does not decode", func(t *testing.T) {
		for name, data := range map[string]string{
			"not json":       "not json",
			"an unknown key": `{"account":"` + testUserID + `","extra":1}`,
			"no factor":      strings.Replace(sessionJSON(testUserID, "2026-11-08T12:00:00Z", "2026-11-08T12:00:00Z", "2026-11-08T12:00:00Z", ""), `["password"]`, `[]`, 1),
		} {
			e, _, _, token := setup(t)
			_, other := e.login(t, "admin")
			e.setRawSession(t, sessKey(token), data)
			ended(t, e, token, "with "+name)
			if !e.hasSession(t, other) {
				t.Errorf("with %s, the row of another session was removed", name)
			}
		}
	})

	t.Run("no session", func(t *testing.T) {
		e, _, _, token := setup(t)
		before := e.mark(t)
		for _, text := range []string{tokSample, strings.Repeat("A", 43)} {
			got, err := e.m.Authenticate(t.Context(), text)
			if !errors.Is(err, ErrNoSession) || !reflect.DeepEqual(got, Principal{}) {
				t.Errorf("Authenticate() of a token without a session = %+v, %v, want ErrNoSession", got, err)
			}
		}
		e.unchanged(t, before)
		if !e.hasSession(t, token) {
			t.Error("the session of another token was removed")
		}
	})

	// Nothing is read for a text that is no token, so a closed store does not matter; for a token it is an error.
	t.Run("a closed store", func(t *testing.T) {
		e, _, _, token := setup(t)
		if err := e.s.Close(); err != nil {
			t.Fatal(err)
		}
		notTokens := []string{"", "x", tokSample[:42], tokSample + "A", strings.Repeat("!", 43), tokSample[:42] + "9", " " + tokSample[1:]}
		for _, text := range notTokens {
			if got, err := e.m.Authenticate(t.Context(), text); !errors.Is(err, ErrNoSession) || !reflect.DeepEqual(got, Principal{}) {
				t.Errorf("Authenticate() of a text that is no token (%d bytes) = %+v, %v, want ErrNoSession", len(text), got, err)
			}
		}
		for _, text := range []string{token, tokSample} {
			if got, err := e.m.Authenticate(t.Context(), text); err == nil || errors.Is(err, ErrNoSession) || !reflect.DeepEqual(got, Principal{}) {
				t.Errorf("Authenticate() of a token on a closed store = %+v, %v, want the error of the store", got, err)
			}
		}
	})

	t.Run("a canceled context", func(t *testing.T) {
		e, _, _, token := setup(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		before := e.mark(t)
		if got, err := e.m.Authenticate(ctx, token); !errors.Is(err, context.Canceled) || errors.Is(err, ErrNoSession) || !reflect.DeepEqual(got, Principal{}) {
			t.Errorf("Authenticate() with a canceled context = %+v, %v, want context.Canceled", got, err)
		}
		e.unchanged(t, before)
	})

	// Another request, or an operation, commits after the first look and before the write.
	t.Run("between the look and the write", func(t *testing.T) {
		old := func(t *testing.T) (e *mgrEnv, a Account, p Principal, token string) {
			e, a, p, token = setup(t)
			e.clock.add(sessionTouch)
			return e, a, p, token
		}
		// gap makes f run once, after the look.
		gap := func(e *mgrEnv, f func()) {
			e.m.hook = func(point string) {
				if point == "authenticated" {
					e.m.hook = nil
					f()
				}
			}
		}
		t.Run("the time is written by another request", func(t *testing.T) {
			e, _, p, token := old(t)
			var after mgrMark
			gap(e, func() {
				works(t, e, token, p, "in the gap")
				after = e.mark(t)
			})
			works(t, e, token, p, "with the time written in the gap")
			e.unchanged(t, after)
		})
		t.Run("the session is removed", func(t *testing.T) {
			e, _, _, token := old(t)
			var after mgrMark
			gap(e, func() {
				if err := e.m.Logout(t.Context(), token); err != nil {
					t.Error(err)
				}
				after = e.mark(t)
			})
			got, err := e.m.Authenticate(t.Context(), token)
			if !errors.Is(err, ErrNoSession) || !reflect.DeepEqual(got, Principal{}) {
				t.Errorf("Authenticate() = %+v, %v, want ErrNoSession", got, err)
			}
			e.unchanged(t, after)
		})
		t.Run("the session expires", func(t *testing.T) {
			e, _, _, token := old(t)
			gap(e, func() { e.clock.add(SessionIdleTimeout) })
			ended(t, e, token, "that expired in the gap")
		})
		t.Run("the account is disabled", func(t *testing.T) {
			e, a, _, token := old(t)
			gap(e, func() { e.edit(t, a.ID, func(r *row) { r.Disabled = true }) })
			ended(t, e, token, "of an account that was disabled in the gap")
		})
		t.Run("the credentials change", func(t *testing.T) {
			e, a, _, token := old(t)
			gap(e, func() {
				e.edit(t, a.ID, func(r *row) { r.CredentialsChangedAt = r.CredentialsChangedAt.Add(time.Second) })
			})
			ended(t, e, token, "of an account whose credentials changed in the gap")
		})
		t.Run("the row stops decoding", func(t *testing.T) {
			e, _, _, token := old(t)
			gap(e, func() { e.setRawSession(t, sessKey(token), "not json") })
			ended(t, e, token, "that stopped decoding in the gap")
		})
		t.Run("the context ends", func(t *testing.T) {
			e, _, _, token := old(t)
			ctx, cancel := context.WithCancel(t.Context())
			var after mgrMark
			gap(e, func() {
				cancel()
				after = e.mark(t)
			})
			got, err := e.m.Authenticate(ctx, token)
			if !errors.Is(err, context.Canceled) || errors.Is(err, ErrNoSession) || !reflect.DeepEqual(got, Principal{}) {
				t.Errorf("Authenticate() with a context that ends in the gap = %+v, %v, want context.Canceled", got, err)
			}
			e.unchanged(t, after)
		})
	})
}

func TestLogout(t *testing.T) {
	setup := func(t *testing.T) (e *mgrEnv, tokens []string) {
		e = newMgr(t)
		e.first(t)
		e.add(t, "yamada", RoleEditor)
		for _, login := range []string{"yamada", "yamada", "admin"} {
			_, token := e.login(t, login)
			tokens = append(tokens, token)
		}
		return e, tokens
	}

	t.Run("the session ends", func(t *testing.T) {
		e, tokens := setup(t)
		if err := e.m.Logout(t.Context(), tokens[0]); err != nil {
			t.Fatalf("Logout() = %v", err)
		}
		if e.hasSession(t, tokens[0]) || !e.hasSession(t, tokens[1]) || !e.hasSession(t, tokens[2]) || len(e.rawSessions(t)) != 2 {
			t.Errorf("after the logout of the first session, it has a row = %v, the others %v and %v, and there are %d rows, want false, true, true, 2",
				e.hasSession(t, tokens[0]), e.hasSession(t, tokens[1]), e.hasSession(t, tokens[2]), len(e.rawSessions(t)))
		}
		if got, err := e.m.Authenticate(t.Context(), tokens[0]); !errors.Is(err, ErrNoSession) || !reflect.DeepEqual(got, Principal{}) {
			t.Errorf("Authenticate() after the logout = %+v, %v, want ErrNoSession", got, err)
		}
		if _, err := e.m.Authenticate(t.Context(), tokens[1]); err != nil {
			t.Errorf("Authenticate() of another session = %v, want nil", err)
		}
	})

	t.Run("nothing to remove", func(t *testing.T) {
		e, tokens := setup(t)
		if err := e.m.Logout(t.Context(), tokens[0]); err != nil {
			t.Fatal(err)
		}
		before := e.mark(t)
		for _, text := range []string{tokens[0], tokSample, strings.Repeat("A", 43), "", "junk", tokSample[:42], tokSample + "A"} {
			if err := e.m.Logout(t.Context(), text); err != nil {
				t.Errorf("Logout() of a text with no session (%d bytes) = %v, want nil", len(text), err)
			}
		}
		e.unchanged(t, before)
	})

	t.Run("a session that has expired", func(t *testing.T) {
		e, tokens := setup(t)
		e.clock.add(SessionLifetime)
		if err := e.m.Logout(t.Context(), tokens[0]); err != nil {
			t.Fatal(err)
		}
		if e.hasSession(t, tokens[0]) {
			t.Error("the row of a session that expired stayed")
		}
	})

	t.Run("a row that does not decode", func(t *testing.T) {
		e, tokens := setup(t)
		e.setRawSession(t, sessKey(tokens[0]), "not json")
		if err := e.m.Logout(t.Context(), tokens[0]); err != nil || e.hasSession(t, tokens[0]) {
			t.Errorf("Logout() of a row that does not decode = %v, row there = %v, want nil and no row", err, e.hasSession(t, tokens[0]))
		}
	})

	t.Run("a closed store", func(t *testing.T) {
		e, tokens := setup(t)
		if err := e.s.Close(); err != nil {
			t.Fatal(err)
		}
		if err := e.m.Logout(t.Context(), tokens[0]); err == nil {
			t.Error("Logout() of a token on a closed store = nil, want the error of the store")
		}
		// Nothing is written for a text that is no token, so there is no error.
		if err := e.m.Logout(t.Context(), "junk"); err != nil {
			t.Errorf("Logout() of a text that is no token on a closed store = %v, want nil", err)
		}
	})

	t.Run("a canceled context", func(t *testing.T) {
		e, tokens := setup(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		before := e.mark(t)
		if err := e.m.Logout(ctx, tokens[0]); !errors.Is(err, context.Canceled) {
			t.Errorf("Logout() with a canceled context = %v, want context.Canceled", err)
		}
		e.unchanged(t, before)
	})
}

// sessTestRow is a session row with every key set.
func sessTestRow() sessionRow {
	return sessionRow{
		Account: testUserID, CreatedAt: testTime, AuthenticatedAt: testTime.Add(time.Minute), LastSeenAt: testTime.Add(2 * time.Minute),
		CredentialsChangedAt: testTime.Add(-time.Hour), Factors: []Factor{FactorPassword},
	}
}

// sessTestJSON is what encodeSession writes for sessTestRow.
const sessTestJSON = `{"account":"` + testUserID + `","createdAt":"2026-11-08T01:02:03.456789Z","authenticatedAt":"2026-11-08T01:03:03.456789Z",` +
	`"lastSeenAt":"2026-11-08T01:04:03.456789Z","credentialsChangedAt":"2026-11-08T00:02:03.456789Z","factors":["password"]}`

// sameSessionRow compares two rows by the instants of their times, which two decodings of one instant may write in
// other zones.
func sameSessionRow(a, b sessionRow) bool {
	return a.Account == b.Account && a.CreatedAt.Equal(b.CreatedAt) && a.AuthenticatedAt.Equal(b.AuthenticatedAt) &&
		a.LastSeenAt.Equal(b.LastSeenAt) && a.CredentialsChangedAt.Equal(b.CredentialsChangedAt) && slices.Equal(a.Factors, b.Factors)
}

func sessInvalid(key string) string {
	return `sessions "` + key + `": invalid value`
}

func TestEncodeSession(t *testing.T) {
	jst := time.FixedZone("JST", 9*3600)
	zone := sessTestRow()
	zone.CreatedAt, zone.AuthenticatedAt = zone.CreatedAt.In(jst), zone.AuthenticatedAt.In(jst)
	zone.LastSeenAt, zone.CredentialsChangedAt = zone.LastSeenAt.In(jst), zone.CredentialsChangedAt.In(jst)
	noChange := sessTestRow()
	noChange.CredentialsChangedAt = time.Time{}
	fractions := sessTestRow()
	fractions.CreatedAt = time.Date(2026, 11, 8, 1, 2, 3, 100000000, time.UTC)
	fractions.AuthenticatedAt = time.Date(2026, 11, 8, 1, 2, 4, 0, time.UTC)
	fractions.LastSeenAt = time.Date(2026, 11, 8, 1, 2, 5, 123456789, time.UTC)
	factors := sessTestRow()
	factors.Factors = []Factor{FactorPassword, "totp"}
	tests := []struct {
		name string
		row  sessionRow
		want string
	}{
		{"every key", sessTestRow(), sessTestJSON},
		{"times in another zone", zone, sessTestJSON},
		{"credentials of no time", noChange, strings.Replace(sessTestJSON, `"credentialsChangedAt":"2026-11-08T00:02:03.456789Z",`, "", 1)},
		{"the fraction of a second to its last digit", fractions,
			`{"account":"` + testUserID + `","createdAt":"2026-11-08T01:02:03.1Z","authenticatedAt":"2026-11-08T01:02:04Z",` +
				`"lastSeenAt":"2026-11-08T01:02:05.123456789Z","credentialsChangedAt":"2026-11-08T00:02:03.456789Z","factors":["password"]}`},
		{"two factors", factors, strings.Replace(sessTestJSON, `["password"]`, `["password","totp"]`, 1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := encodeSession(tt.row)
			if err != nil || string(got) != tt.want {
				t.Errorf("encodeSession() =\n%s, %v\nwant\n%s", got, err, tt.want)
			}
		})
	}

	for name, bad := range map[string]func(s *sessionRow){
		"a year that RFC 3339 does not have": func(s *sessionRow) { s.LastSeenAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) },
		"invalid UTF-8":                      func(s *sessionRow) { s.Factors = []Factor{"a\xffb"} },
	} {
		s := sessTestRow()
		bad(&s)
		if got, err := encodeSession(s); err == nil {
			t.Errorf("encodeSession() of %s = %s, want an error", name, got)
		}
	}
}

func TestDecodeSessionAccepts(t *testing.T) {
	key := sessKey("row")
	backslash := "\\"
	with := func(f func(s *sessionRow)) sessionRow {
		s := sessTestRow()
		f(&s)
		return s
	}
	json := func(old, new string) string { return replaceOnce(t, sessTestJSON, old, new) }
	tests := []struct {
		name string
		data string
		want sessionRow
	}{
		{"the form that is written", sessTestJSON, sessTestRow()},
		{"no credentials", json(`"credentialsChangedAt":"2026-11-08T00:02:03.456789Z",`, ""), with(func(s *sessionRow) { s.CredentialsChangedAt = time.Time{} })},
		{"null credentials", json(`"2026-11-08T00:02:03.456789Z"`, `null`), with(func(s *sessionRow) { s.CredentialsChangedAt = time.Time{} })},
		{"another order of the keys", `{"factors":["password"],"lastSeenAt":"2026-11-08T01:04:03.456789Z","credentialsChangedAt":"2026-11-08T00:02:03.456789Z",` +
			`"authenticatedAt":"2026-11-08T01:03:03.456789Z","createdAt":"2026-11-08T01:02:03.456789Z","account":"` + testUserID + `"}`, sessTestRow()},
		{"spaces and line breaks", " \n" + strings.ReplaceAll(sessTestJSON, ",", ",\n  ") + "\n", sessTestRow()},
		{"times in another zone", json(`"2026-11-08T01:02:03.456789Z"`, `"2026-11-08T10:02:03.456789+09:00"`), sessTestRow()},
		{"times without a fraction", json(`"2026-11-08T01:02:03.456789Z"`, `"2026-11-08T01:02:03Z"`),
			with(func(s *sessionRow) { s.CreatedAt = time.Date(2026, 11, 8, 1, 2, 3, 0, time.UTC) })},
		{"an escape in the account", json(`"usr_`, `"`+backslash+`u0075sr_`), sessTestRow()},
		{"another factor", json(`["password"]`, `["totp"]`), with(func(s *sessionRow) { s.Factors = []Factor{"totp"} })},
		{"two factors", json(`["password"]`, `["password","totp"]`), with(func(s *sessionRow) { s.Factors = []Factor{FactorPassword, "totp"} })},
		{"the first year", json(`"2026-11-08T01:02:03.456789Z"`, `"0000-01-01T00:00:00Z"`), with(func(s *sessionRow) { s.CreatedAt = time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC) })},
		{"the last year", json(`"2026-11-08T01:04:03.456789Z"`, `"9999-12-31T23:59:59.999999999Z"`),
			with(func(s *sessionRow) { s.LastSeenAt = time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC) })},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeSession(key, []byte(tt.data))
			if err != nil || !sameSessionRow(got, tt.want) {
				t.Errorf("decodeSession() = %+v, %v, want %+v", got, err, tt.want)
			}
		})
	}
}

func TestDecodeSessionRejects(t *testing.T) {
	key := sessKey("row")
	json := func(old, new string) string { return replaceOnce(t, sessTestJSON, old, new) }
	dropped := func(key string) string {
		start := strings.Index(sessTestJSON, `"`+key+`":`)
		end := start + strings.IndexAny(sessTestJSON[start:], ",}") + 1
		if sessTestJSON[end-1] == '}' {
			return strings.Replace(sessTestJSON[:start-1]+"}", ",}", "}", 1)
		}
		return sessTestJSON[:start] + sessTestJSON[end:]
	}
	id := func(v string) string { return json(testUserID, v) }
	zero := `"0001-01-01T00:00:00Z"`
	beyond := `"9999-12-31T23:59:59-01:00"` // the year 10000 in UTC
	before := `"0000-01-01T00:00:00+01:00"` // the year -1 in UTC
	tests := []struct{ name, data string }{
		{"not JSON", "not json"},
		{"nothing", ""},
		{"null", "null"},
		{"an array", "[]"},
		{"an empty object", "{}"},
		{"a string", `"` + testUserID + `"`},
		{"an unknown key", json(`"factors"`, `"extra":1,"factors"`)},
		{"a key in another case", json(`"account"`, `"Account"`)},
		{"a duplicate key", json(`"account"`, `"account":"`+testUserID+`","account"`)},
		{"an account that is a number", json(`"`+testUserID+`"`, `5`)},
		{"a time that is a number", json(`"2026-11-08T01:02:03.456789Z"`, `5`)},
		{"a time that is no time", json(`"2026-11-08T01:02:03.456789Z"`, `"yesterday"`)},
		{"credentials that are no time", json(`"2026-11-08T00:02:03.456789Z"`, `"x"`)},
		{"factors that are a string", json(`["password"]`, `"password"`)},
		{"a factor that is a number", json(`["password"]`, `[1]`)},
		{"invalid UTF-8", json(`["password"]`, "[\"\xff\"]")},
		{"data after the row", sessTestJSON + " x"},
		{"two rows", sessTestJSON + sessTestJSON},
		{"no account", dropped("account")},
		{"an empty account", id("")},
		{"an account that is no ID", id("usr_x")},
		{"an account in lower case", id(strings.ToLower(testUserID))},
		{"the ID of an author profile", id("aut_01JBA4C7N9Q3T6W9Z3C6F9H2K5")},
		{"an account with a space", id(" " + testUserID)},
		{"no creation", dropped("createdAt")},
		{"no authentication", dropped("authenticatedAt")},
		{"no last request", dropped("lastSeenAt")},
		{"a creation of no time", json(`"2026-11-08T01:02:03.456789Z"`, zero)},
		{"an authentication of no time", json(`"2026-11-08T01:03:03.456789Z"`, zero)},
		{"a last request of no time", json(`"2026-11-08T01:04:03.456789Z"`, zero)},
		{"a creation that is null", json(`"2026-11-08T01:02:03.456789Z"`, `null`)},
		{"no factors", dropped("factors")},
		{"factors that are null", json(`["password"]`, `null`)},
		{"an empty list of factors", json(`["password"]`, `[]`)},
		{"an empty factor", json(`["password"]`, `[""]`)},
		{"an empty factor after a factor", json(`["password"]`, `["password",""]`)},
		{"a creation after the year 9999", json(`"2026-11-08T01:02:03.456789Z"`, beyond)},
		{"an authentication after the year 9999", json(`"2026-11-08T01:03:03.456789Z"`, beyond)},
		{"a last request after the year 9999", json(`"2026-11-08T01:04:03.456789Z"`, beyond)},
		{"credentials after the year 9999", json(`"2026-11-08T00:02:03.456789Z"`, beyond)},
		{"a creation before the year 0", json(`"2026-11-08T01:02:03.456789Z"`, before)},
		{"credentials before the year 0", json(`"2026-11-08T00:02:03.456789Z"`, before)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeSession(key, []byte(tt.data))
			if err == nil || err.Error() != sessInvalid(key) || !reflect.DeepEqual(got, sessionRow{}) {
				t.Errorf("decodeSession() = %+v, %v, want only the error %q", got, err, sessInvalid(key))
			}
		})
	}
}

// TestSessionRowPrinting checks that fmt prints nothing of a session row but its account.
func TestSessionRowPrinting(t *testing.T) {
	s := sessTestRow()
	stored := storedSession{key: sessKey("row"), sessionRow: s}
	want := `session row of "` + testUserID + `"`
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		for _, v := range []any{s, &s, stored, &stored} {
			if got := fmt.Sprintf(verb, v); got != want && got != "&"+want {
				t.Errorf("Sprintf(%q) of a %T = %q, want %q", verb, v, got, want)
			}
		}
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		got := fmt.Sprintf(verb, []sessionRow{s})
		if strings.Contains(got, "2026") || strings.Contains(got, "password") || !strings.Contains(got, testUserID) {
			t.Errorf("Sprintf(%q) of a list of rows = %q, want the account and nothing more", verb, got)
		}
	}
}

// FuzzDecodeSession checks that every error is the one text, and that a row that is read can be written again in the one
// form, which reads as the same row.
func FuzzDecodeSession(f *testing.F) {
	for _, s := range []string{
		sessTestJSON, "", "null", "{}", "[]", sessTestJSON + "\n", " " + sessTestJSON,
		strings.Replace(sessTestJSON, `"account"`, `"account":"`+testUserID+`","account"`, 1),
		strings.Replace(sessTestJSON, `"factors"`, `"extra":1,"factors"`, 1),
		strings.Replace(sessTestJSON, `["password"]`, `[]`, 1),
		strings.Replace(sessTestJSON, `["password"]`, `["password","totp"]`, 1),
		strings.Replace(sessTestJSON, `"2026-11-08T01:02:03.456789Z"`, `"2026-11-08T10:02:03.456789+09:00"`, 1),
		strings.Replace(sessTestJSON, `"2026-11-08T01:02:03.456789Z"`, `"9999-12-31T23:59:59-01:00"`, 1),
		strings.Replace(sessTestJSON, `"credentialsChangedAt":"2026-11-08T00:02:03.456789Z",`, "", 1),
	} {
		f.Add([]byte(s))
	}
	key := sessKey("fuzz")
	f.Fuzz(func(t *testing.T, data []byte) {
		s, err := decodeSession(key, data)
		if err != nil {
			if err.Error() != sessInvalid(key) {
				t.Fatalf("decodeSession() = %v, want %q", err, sessInvalid(key))
			}
			return
		}
		enc, err := encodeSession(s)
		if err != nil {
			t.Fatalf("encodeSession() of the row read from %q = %v", data, err)
		}
		again, err := decodeSession(key, enc)
		if err != nil {
			t.Fatalf("decodeSession() of %q, which encodeSession wrote for the row read from %q = %v", enc, data, err)
		}
		if !sameSessionRow(again, s) {
			t.Fatalf("the row read from %q and the row read from %q differ", data, enc)
		}
		if enc2, err := encodeSession(again); err != nil || !bytes.Equal(enc, enc2) {
			t.Fatalf("encodeSession() writes %q and then %q, %v, want one form", enc, enc2, err)
		}
	})
}

// TestSessionHelpersOnABadTransaction runs the helpers on a transaction that has ended, and on one that cannot write,
// and checks that their errors are those of the store.
func TestSessionHelpersOnABadTransaction(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	yamada := e.row(t, e.add(t, "yamada", RoleEditor).ID)
	now := mgrTime.Add(time.Minute)
	live := sessionFor(yamada, now)

	var ended *store.Tx
	if err := e.s.View(t.Context(), func(tx *store.Tx) error { ended = tx; return nil }); err != nil {
		t.Fatal(err)
	}
	key := sessKey("row")
	const gone = "the transaction has ended"
	if rows, err := sweepSessions(ended, now); err == nil || err.Error() != gone || rows != nil {
		t.Errorf("sweepSessions() = %v, %v, want an error that the transaction has ended", rows, err)
	}
	if _, _, state, err := readSession(ended, key, now); err == nil || err.Error() != gone || state == sessionLive {
		t.Errorf("readSession() = %v, %v, want an error that the transaction has ended", state, err)
	}
	if p, err := startSession(ended, yamada, key, "", now); err == nil || err.Error() != gone || !reflect.DeepEqual(p, Principal{}) {
		t.Errorf("startSession() = %+v, %v, want an error that the transaction has ended", p, err)
	}
	if err := putSession(ended, key, live); err == nil || err.Error() != gone {
		t.Errorf("putSession() = %v, want an error that the transaction has ended", err)
	}

	// A View cannot write: whatever the helpers would remove or put is an error of the store.
	const readOnly = "read-only transaction"
	view := func(t *testing.T, f func(tx *store.Tx) error) error {
		t.Helper()
		return e.s.View(t.Context(), f)
	}
	t.Run("a row to remove", func(t *testing.T) {
		e.setRawSession(t, sessKey("garbage"), "not json")
		err := view(t, func(tx *store.Tx) error {
			_, err := sweepSessions(tx, now)
			return err
		})
		if err == nil || err.Error() != readOnly {
			t.Errorf("sweepSessions() of a row that cannot work in a View = %v, want %q", err, readOnly)
		}
		if err := e.s.Update(t.Context(), func(tx *store.Tx) error { return tx.DeleteSession(sessKey("garbage")) }); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("the session of previous", func(t *testing.T) {
		token := tokSample
		e.setSession(t, sessKey(token), live)
		err := view(t, func(tx *store.Tx) error {
			_, err := startSession(tx, yamada, sessKey("new"), token, now)
			return err
		})
		if err == nil || err.Error() != readOnly {
			t.Errorf("startSession() with a session to remove in a View = %v, want %q", err, readOnly)
		}
		if err := e.s.Update(t.Context(), func(tx *store.Tx) error { return tx.DeleteSession(sessKey(token)) }); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a session over the limit", func(t *testing.T) {
		for i := range SessionsPerAccount {
			e.setSession(t, sessKey("over "+strconv.Itoa(i)), live)
		}
		err := view(t, func(tx *store.Tx) error {
			_, err := startSession(tx, yamada, sessKey("new"), "", now)
			return err
		})
		if err == nil || err.Error() != readOnly {
			t.Errorf("startSession() with a session to remove for the limit in a View = %v, want %q", err, readOnly)
		}
		err = e.s.Update(t.Context(), func(tx *store.Tx) error {
			for i := range SessionsPerAccount {
				if err := tx.DeleteSession(sessKey("over " + strconv.Itoa(i))); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("the new session", func(t *testing.T) {
		err := view(t, func(tx *store.Tx) error {
			_, err := startSession(tx, yamada, sessKey("new"), "", now)
			return err
		})
		if err == nil || err.Error() != readOnly {
			t.Errorf("startSession() in a View = %v, want %q", err, readOnly)
		}
		if rows := e.rawSessions(t); len(rows) != 0 {
			t.Errorf("there are %d session rows, want none", len(rows))
		}
	})
}

// gateProbe is a context that tells when the hash gate begins to wait on it: the gate asks for the Done channel of its
// context in the select that takes a place, even when one is free; the tests take every place first, so that this is
// when it waits. With endOnWait the context ends itself then, so that the wait ends at once.
type gateProbe struct {
	context.Context
	cancel    context.CancelFunc
	reached   chan struct{}
	endOnWait bool
	once      sync.Once
}

func newGateProbe(parent context.Context, endOnWait bool) *gateProbe {
	ctx, cancel := context.WithCancel(parent)
	return &gateProbe{Context: ctx, cancel: cancel, reached: make(chan struct{}), endOnWait: endOnWait}
}

func (p *gateProbe) Done() <-chan struct{} {
	p.once.Do(func() {
		close(p.reached)
		if p.endOnWait {
			p.cancel()
		}
	})
	return p.Context.Done()
}

// waited reports whether the gate has waited on the probe.
func (p *gateProbe) waited() bool {
	select {
	case <-p.reached:
		return true
	default:
		return false
	}
}

// sessWait waits for a value from ch, and fails the test if it takes a minute: a test whose goroutines do not meet fails
// and does not hang.
func sessWait[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(time.Minute):
		t.Fatalf("timed out waiting for %s", what)
	}
	var none T
	return none
}

func TestHashGate(t *testing.T) {
	newEnv := func(t *testing.T, places int) (*mgrEnv, Account) {
		t.Helper()
		e := newMgr(t)
		e.first(t)
		active := e.add(t, "active", RoleEditor)
		e.m.hashes = make(chan struct{}, places)
		return e, active
	}

	t.Run("the number of places", func(t *testing.T) {
		s, err := store.Create(filepath.Join(t.TempDir(), "data"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		old := runtime.GOMAXPROCS(0)
		defer runtime.GOMAXPROCS(old)
		for _, procs := range []int{1, 2, 3, 4, 5, 7, 8, 9, 16, 33} {
			runtime.GOMAXPROCS(procs)
			m, err := NewWithIterations(s, 1)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := cap(m.hashes), max(1, procs/4); got != want || len(m.hashes) != 0 {
				t.Errorf("with GOMAXPROCS %d the gate has %d places, %d taken, want %d, 0", procs, got, len(m.hashes), want)
			}
		}
	})

	t.Run("at most as many hashes as places", func(t *testing.T) {
		e, _ := newEnv(t, 2)
		var running, peak, started atomic.Int32
		entered := make(chan struct{}, 8)
		release := make(chan struct{})
		var wg sync.WaitGroup
		for range 5 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := e.m.gated(t.Context(), func() {
					n := running.Add(1)
					for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
					}
					started.Add(1)
					entered <- struct{}{}
					<-release
					running.Add(-1)
				})
				if err != nil {
					t.Errorf("gated() = %v, want nil", err)
				}
			}()
		}
		sessWait(t, entered, "the first hash to begin")
		sessWait(t, entered, "the second hash to begin")
		// A third call finds both places taken, and does not run while its context lasts.
		probe := newGateProbe(t.Context(), false)
		var ranThird atomic.Bool
		done := make(chan error, 1)
		go func() { done <- e.m.gated(probe, func() { ranThird.Store(true) }) }()
		sessWait(t, probe.reached, "the third call to wait")
		probe.cancel()
		if err := sessWait(t, done, "the third call to return"); !errors.Is(err, context.Canceled) || ranThird.Load() {
			t.Errorf("the call that waited for a place returned %v, ran = %v, want context.Canceled and not run", err, ranThird.Load())
		}
		close(release)
		wg.Wait()
		if peak.Load() != 2 || started.Load() != 5 || len(e.m.hashes) != 0 {
			t.Errorf("%d hashes ran at once at most, %d began, %d places stay taken, want 2, 5, 0", peak.Load(), started.Load(), len(e.m.hashes))
		}
	})

	t.Run("a context that is done", func(t *testing.T) {
		e, _ := newEnv(t, 1)
		var hashing int
		e.m.hook = func(point string) {
			if point == "hashing" {
				hashing++
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		// There is a free place, and the call does not take it: a select that has two things to do picks one at random,
		// so the call is made many times.
		for range 64 {
			called := false
			if err := e.m.gated(ctx, func() { called = true }); !errors.Is(err, context.Canceled) || called || hashing != 0 || len(e.m.hashes) != 0 {
				t.Fatalf("gated() with a context that is done = %v, ran = %v, %d hashes, %d places taken, want context.Canceled, not run, 0, 0",
					err, called, hashing, len(e.m.hashes))
			}
		}
	})

	t.Run("a place is freed when the hash panics", func(t *testing.T) {
		e, _ := newEnv(t, 1)
		func() {
			defer func() { _ = recover() }()
			_ = e.m.gated(t.Context(), func() { panic("a hash that panics") })
		}()
		called := false
		if err := e.m.gated(t.Context(), func() { called = true }); err != nil || !called {
			t.Errorf("gated() after a hash that panicked = %v, ran = %v, want nil and run", err, called)
		}
	})

	t.Run("a context that ends while waiting", func(t *testing.T) {
		e, _ := newEnv(t, 1)
		e.m.hashes <- struct{}{} // the one place is taken
		var hashing atomic.Int32
		e.m.hook = func(point string) {
			if point == "hashing" {
				hashing.Add(1)
			}
		}
		probe := newGateProbe(t.Context(), false)
		done := make(chan error, 1)
		go func() {
			_, err := e.m.VerifyPasswordFactor(probe, "active", mgrPassword("active"))
			done <- err
		}()
		sessWait(t, probe.reached, "the hash to wait for a place")
		probe.cancel()
		if err := sessWait(t, done, "the call to return"); !errors.Is(err, context.Canceled) || errors.Is(err, ErrBadCredentials) || hashing.Load() != 0 {
			t.Errorf("VerifyPasswordFactor() = %v after %d hashes, want context.Canceled, which is no wrong password, after none", err, hashing.Load())
		}
	})

	t.Run("every kind of hash waits for a place", func(t *testing.T) {
		e, active := newEnv(t, 1)
		_, token := e.invite(t, "pending", RoleEditor)
		_, session := e.login(t, "active")
		e.m.hashes <- struct{}{} // the one place is taken
		var hashing atomic.Int32
		e.m.hook = func(point string) {
			if point == "hashing" {
				hashing.Add(1)
			}
		}
		before := e.mark(t)
		ops := []struct {
			name string
			do   func(ctx context.Context) error
		}{
			{"VerifyPasswordFactor", func(ctx context.Context) error {
				_, err := e.m.VerifyPasswordFactor(ctx, "active", mgrPassword("active"))
				return err
			}},
			{"Login", func(ctx context.Context) error {
				_, _, err := e.m.Login(ctx, "active", mgrPassword("active"), "")
				return err
			}},
			{"Login of no account", func(ctx context.Context) error {
				_, _, err := e.m.Login(ctx, "nobody", mgrPassword("active"), "")
				return err
			}},
			{"ChangePassword", func(ctx context.Context) error {
				return e.m.ChangePassword(ctx, active.ID, mgrPassword("active"), "a new long password")
			}},
			{"ChangeOwnPassword", func(ctx context.Context) error {
				_, _, err := e.m.ChangeOwnPassword(ctx, session, mgrPassword("active"), "a new long password")
				return err
			}},
			{"SetPassword", func(ctx context.Context) error {
				_, err := e.m.SetPassword(ctx, token, mgrPassword("pending"))
				return err
			}},
		}
		for _, op := range ops {
			probe := newGateProbe(t.Context(), true)
			if err := op.do(probe); !errors.Is(err, context.Canceled) || errors.Is(err, ErrBadCredentials) || !probe.waited() {
				t.Errorf("%s = %v, waited = %v, want context.Canceled after it waited for a place", op.name, err, probe.waited())
			}
		}
		if hashing.Load() != 0 {
			t.Errorf("%d hashes were computed while the place was taken", hashing.Load())
		}
		e.unchanged(t, before)

		empty := newMgr(t)
		empty.m.hashes = make(chan struct{}, 1)
		empty.m.hashes <- struct{}{}
		probe := newGateProbe(t.Context(), true)
		_, err := empty.m.CreateFirst(probe, mgrInput("admin", RoleAdmin), mgrPassword("admin"))
		if !errors.Is(err, context.Canceled) || !probe.waited() || len(empty.raw(t)) != 0 {
			t.Errorf("CreateFirst() = %v, waited = %v, %d rows, want context.Canceled after it waited for a place, and no row", err, probe.waited(), len(empty.raw(t)))
		}
	})

	t.Run("a hash waits for the one before it", func(t *testing.T) {
		e, _ := newEnv(t, 1)
		var calls atomic.Int32
		held, release := make(chan struct{}), make(chan struct{})
		e.m.hook = func(point string) {
			if point == "hashing" && calls.Add(1) == 1 {
				close(held)
				<-release
			}
		}
		first := make(chan error, 1)
		go func() {
			_, err := e.m.VerifyPasswordFactor(t.Context(), "active", mgrPassword("active"))
			first <- err
		}()
		sessWait(t, held, "the first hash to begin")
		probe := newGateProbe(t.Context(), false)
		second := make(chan error, 1)
		go func() {
			_, _, err := e.m.Login(probe, "active", mgrPassword("active"), "")
			second <- err
		}()
		sessWait(t, probe.reached, "the second hash to wait")
		// The second login has not begun to hash, and cannot while the first has the place.
		if got := calls.Load(); got != 1 {
			t.Errorf("%d hashes have begun while the first one has the place, want 1", got)
		}
		close(release)
		if err := sessWait(t, first, "the first call to return"); err != nil {
			t.Errorf("VerifyPasswordFactor() = %v, want nil", err)
		}
		if err := sessWait(t, second, "the second call to return"); err != nil {
			t.Errorf("Login() = %v, want nil", err)
		}
		if got := calls.Load(); got != 2 || len(e.m.hashes) != 0 {
			t.Errorf("%d hashes began and %d places stay taken, want 2 and 0", got, len(e.m.hashes))
		}
	})

	// A typed password of more than 8 KiB is refused without a hash, so it needs no place.
	t.Run("a typed password that is too long needs no place", func(t *testing.T) {
		e, active := newEnv(t, 1)
		_, session := e.login(t, "active")
		e.m.hashes <- struct{}{}
		long := strings.Repeat("a", maxTypedPasswordBytes+1)
		probe := newGateProbe(t.Context(), true)
		if _, _, err := e.m.Login(probe, "active", long, ""); !errors.Is(err, ErrBadCredentials) || probe.waited() {
			t.Errorf("Login() with a password of more than 8 KiB = %v, waited = %v, want ErrBadCredentials without a wait", err, probe.waited())
		}
		probe = newGateProbe(t.Context(), true)
		if err := e.m.ChangePassword(probe, active.ID, long, "a new long password"); !errors.Is(err, ErrBadCredentials) || probe.waited() {
			t.Errorf("ChangePassword() with a current password of more than 8 KiB = %v, waited = %v, want ErrBadCredentials without a wait", err, probe.waited())
		}
		probe = newGateProbe(t.Context(), true)
		if _, _, err := e.m.ChangeOwnPassword(probe, session, long, "a new long password"); !errors.Is(err, ErrBadCredentials) || probe.waited() {
			t.Errorf("ChangeOwnPassword() with a current password of more than 8 KiB = %v, waited = %v, want ErrBadCredentials without a wait", err, probe.waited())
		}
	})
}

func TestNewWithIterations(t *testing.T) {
	s, err := store.Create(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, n := range []int{1, 2, 3} {
		m, err := NewWithIterations(s, n)
		if err != nil {
			t.Fatalf("NewWithIterations(%d) = %v", n, err)
		}
		got, _, _, perr := parseHash(m.unknownHash)
		if m.st != s || m.iterations != n || m.rand != rand.Reader || perr != nil || got != n || cap(m.hashes) < 1 {
			t.Errorf("NewWithIterations(%d) made a Manager with %d iterations, a hash for nobody of %d, %v, want the store, the system's random bits and %d",
				n, m.iterations, got, perr, n)
		}
	}
	for _, n := range []int{0, -1, maxIterations + 1} {
		m, err := NewWithIterations(s, n)
		if want := fmt.Sprintf("invalid iteration count %d", n); m != nil || err == nil || err.Error() != want {
			t.Errorf("NewWithIterations(%d) = %v, %v, want no Manager and %q", n, m, err, want)
		}
	}

	// It makes accounts and sessions like New, with hashes of its count.
	m, err := NewWithIterations(s, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreateFirst(t.Context(), mgrInput("admin", RoleAdmin), mgrPassword("admin")); err != nil {
		t.Fatalf("CreateFirst() = %v", err)
	}
	p, token, err := m.Login(t.Context(), "admin", mgrPassword("admin"), "")
	if err != nil {
		t.Fatalf("Login() = %v", err)
	}
	if got, err := m.Authenticate(t.Context(), token); err != nil || !reflect.DeepEqual(got, p) {
		t.Errorf("Authenticate() = %+v, %v, want the Principal of the login", got, err)
	}
	err = s.View(t.Context(), func(tx *store.Tx) error {
		data, _, err := tx.Account(p.Account.ID)
		if err != nil {
			return err
		}
		if r, err := decodeRow(p.Account.ID, data); err != nil || !strings.HasPrefix(r.Password, "$pbkdf2-sha256$i=2$") {
			t.Errorf("the password hash of the first account has another count, or cannot be read: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestSessionsPersist checks that a session outlives the store being closed and opened again, as a restart of the
// server does, with the time of its last request.
func TestSessionsPersist(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	s, err := store.Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := newMgrOn(t, s, dir)
	e.first(t)
	e.add(t, "yamada", RoleEditor)
	p, token := e.login(t, "yamada")
	_, gone := e.login(t, "yamada")
	if err := e.m.Logout(t.Context(), gone); err != nil {
		t.Fatal(err)
	}
	e.clock.add(sessionTouch)
	if _, err := e.m.Authenticate(t.Context(), token); err != nil {
		t.Fatal(err)
	}
	rows := e.rawSessions(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	e2 := newMgrOn(t, s2, dir)
	e2.clock.set(mgrTime.Add(sessionTouch + time.Minute))
	if got := e2.rawSessions(t); !maps.Equal(got, rows) || len(got) != 1 {
		t.Errorf("the session rows are %d, not the one that was closed", len(got))
	}
	if got, err := e2.m.Authenticate(t.Context(), token); err != nil || !reflect.DeepEqual(got, p) {
		t.Errorf("Authenticate() after Open = %+v, %v, want the Principal of the login", got, err)
	}
	if got, err := e2.m.Authenticate(t.Context(), gone); !errors.Is(err, ErrNoSession) || !reflect.DeepEqual(got, Principal{}) {
		t.Errorf("Authenticate() of the session that was ended = %+v, %v, want ErrNoSession", got, err)
	}
	if s, _ := e2.sessionOf(t, token); !s.LastSeenAt.Equal(mgrTime.Add(sessionTouch)) {
		t.Errorf("the last request is %v after Open, want the one that was written, %v", s.LastSeenAt, mgrTime.Add(sessionTouch))
	}
}

// TestConcurrentSessions runs logins, requests and logouts of several accounts at the same time, with the clock moving
// under them, and checks what holds whatever order they happen in.
func TestConcurrentSessions(t *testing.T) {
	logins := []string{"admin", "yamada", "taro"}
	setup := func(t *testing.T) (*mgrEnv, map[string]string) {
		e := newMgr(t)
		ids := map[string]string{"admin": e.first(t).ID}
		for _, login := range logins[1:] {
			ids[login] = e.add(t, login, RoleEditor).ID
		}
		return e, ids
	}

	t.Run("every kind of call at once", func(t *testing.T) {
		e, ids := setup(t)
		// Every call to the clock is two seconds later, so that some requests find the time of the last request old
		// enough to write it, and the sessions do not expire in the time of the test.
		var ticks atomic.Int64
		e.m.SetClock(func() time.Time { return mgrTime.Add(time.Duration(ticks.Add(1)) * 2 * time.Second) })
		workers, rounds := 8, 40
		if testing.Short() {
			rounds = 10
		}
		var mu sync.Mutex
		issued := make(map[string]string) // token by login
		ended := make(map[string]bool)
		var order []string
		pick := func(k int) (token, login string, ok bool) {
			mu.Lock()
			defer mu.Unlock()
			if len(order) == 0 {
				return "", "", false
			}
			token = order[k%len(order)]
			return token, issued[token], true
		}
		record := func(token, login string, previous string) {
			mu.Lock()
			defer mu.Unlock()
			issued[token] = login
			order = append(order, token)
			if previous != "" {
				ended[previous] = true
			}
		}
		end := func(token string) {
			mu.Lock()
			defer mu.Unlock()
			ended[token] = true
		}
		failures := make(chan string, workers*rounds)
		var wg sync.WaitGroup
		for w := range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				last := ""
				for j := range rounds {
					login := logins[(w+j)%len(logins)]
					switch (w*5 + j) % 4 {
					case 0, 1:
						previous := ""
						if (w+j)%2 == 0 {
							previous = last
						}
						p, token, err := e.m.Login(t.Context(), login, mgrPassword(login), previous)
						if err != nil || p.Account.ID != ids[login] {
							failures <- fmt.Sprintf("Login(%q) = %+v, %v", login, p, err)
							return
						}
						record(token, login, previous)
						last = token
					case 2:
						token, owner, ok := pick(w*31 + j)
						if !ok {
							continue
						}
						got, err := e.m.Authenticate(t.Context(), token)
						if err == nil && got.Account.ID != ids[owner] {
							failures <- fmt.Sprintf("Authenticate() of a session of %s returned the account of %s", owner, got.Account.Login)
						} else if err != nil && !errors.Is(err, ErrNoSession) {
							failures <- fmt.Sprintf("Authenticate() = %v", err)
						}
					case 3:
						token, _, ok := pick(w*17 + j)
						if !ok {
							continue
						}
						if err := e.m.Logout(t.Context(), token); err != nil {
							failures <- fmt.Sprintf("Logout() = %v", err)
						}
						end(token)
					}
				}
			}()
		}
		wg.Wait()
		close(failures)
		for f := range failures {
			t.Error(f)
		}

		// The rows are those of tokens that were issued and not ended, in numbers within the limit.
		byKey := make(map[string]string)
		for token := range issued {
			byKey[sessKey(token)] = token
		}
		perAccount := make(map[string]int)
		for key, data := range e.rawSessions(t) {
			s, err := decodeSession(key, []byte(data))
			token, known := byKey[key]
			switch {
			case err != nil:
				t.Error("a session row does not decode")
			case !known:
				t.Error("a session row belongs to no token that was issued")
			case ended[token]:
				t.Error("a session row belongs to a token that was ended")
			case s.Account != ids[issued[token]]:
				t.Error("a session row belongs to another account than its token's")
			}
			perAccount[s.Account]++
		}
		for login, id := range ids {
			if perAccount[id] > SessionsPerAccount {
				t.Errorf("%s has %d sessions, want at most %d", login, perAccount[id], SessionsPerAccount)
			}
		}
		// A token works if its row is there, and only then.
		for token, login := range issued {
			got, err := e.m.Authenticate(t.Context(), token)
			has := e.hasSession(t, token)
			switch {
			case err == nil && (!has || ended[token] || got.Account.ID != ids[login]):
				t.Errorf("a token of %s works though its row is there = %v, ended = %v", login, has, ended[token])
			case err != nil && (!errors.Is(err, ErrNoSession) || has):
				t.Errorf("a token of %s does not work: %v, though its row is there = %v", login, err, has)
			}
		}
		if len(issued) <= SessionsPerAccount*len(logins) {
			t.Errorf("the test issued %d tokens, want more than the limits of the accounts, %d, so that the limit is reached", len(issued), SessionsPerAccount*len(logins))
		}
	})

	// With no more sessions at a time than the limit, every token works until its session is ended, whatever order the
	// calls happen in.
	t.Run("within the limit", func(t *testing.T) {
		e, ids := setup(t)
		const workers = 8
		tokens := make([][]string, workers) // by worker, then by login
		calls := make([]func() error, workers)
		for w := range calls {
			calls[w] = func() error {
				for _, login := range logins {
					_, token, err := e.m.Login(t.Context(), login, mgrPassword(login), "")
					if err != nil {
						return err
					}
					tokens[w] = append(tokens[w], token)
				}
				return nil
			}
		}
		for w, err := range mgrRunTogether(calls...) {
			if err != nil {
				t.Fatalf("worker %d: %v", w, err)
			}
		}
		// Half the sessions end while the others are used.
		var checks []func() error
		for w := range tokens {
			for i, token := range tokens[w] {
				if (w+i)%2 == 0 {
					checks = append(checks, func() error { return e.m.Logout(t.Context(), token) })
					continue
				}
				checks = append(checks, func() error {
					got, err := e.m.Authenticate(t.Context(), token)
					if err == nil && got.Account.ID != ids[logins[i]] {
						return errors.New("the session is of another account")
					}
					return err
				})
			}
		}
		for i, err := range mgrRunTogether(checks...) {
			if err != nil {
				t.Errorf("call %d: %v", i, err)
			}
		}
		for w := range tokens {
			for i, token := range tokens[w] {
				if want := (w+i)%2 != 0; e.hasSession(t, token) != want {
					t.Errorf("worker %d, %s: has a row = %v, want %v", w, logins[i], e.hasSession(t, token), want)
				}
			}
		}
	})
}

// sessionsOf returns the stored session rows that name the account id, by key. A row that does not decode names nobody.
func (e *mgrEnv) sessionsOf(t *testing.T, id string) map[string]sessionRow {
	t.Helper()
	got := make(map[string]sessionRow)
	for key, data := range e.rawSessions(t) {
		if s, err := decodeSession(key, []byte(data)); err == nil && s.Account == id {
			got[key] = s
		}
	}
	return got
}

// sessGone checks that the session of the token has no row, and that Authenticate refuses the token. It looks at the row
// first, since Authenticate removes the row of a session that cannot work.
func sessGone(t *testing.T, e *mgrEnv, token, what string) {
	t.Helper()
	if e.hasSession(t, token) {
		t.Errorf("the row of the session %s stayed", what)
	}
	if got, err := e.m.Authenticate(t.Context(), token); !errors.Is(err, ErrNoSession) || !reflect.DeepEqual(got, Principal{}) {
		t.Errorf("Authenticate() of the session %s = %+v, %v, want ErrNoSession", what, got, err)
	}
}

// sessLives checks that the session of the token has its row, and that Authenticate accepts the token.
func sessLives(t *testing.T, e *mgrEnv, token, what string) {
	t.Helper()
	if !e.hasSession(t, token) {
		t.Errorf("the row of the session %s is gone", what)
	}
	if _, err := e.m.Authenticate(t.Context(), token); err != nil {
		t.Errorf("Authenticate() of the session %s = %v, want nil", what, err)
	}
}

// ghostSession writes a row of a session that names the account id, which need not exist, with the store's own API.
func (e *mgrEnv) ghostSession(t *testing.T, id, name string) {
	t.Helper()
	e.setSession(t, sessKey(name), sessionRow{
		Account: id, CreatedAt: mgrTime, AuthenticatedAt: mgrTime, LastSeenAt: mgrTime, CredentialsChangedAt: mgrTime,
		Factors: []Factor{FactorPassword},
	})
}

// endFixture is a store with two administrators and two accounts that are not, each with sessions, and two rows that
// cannot work, which an operation on an account other than taro leaves and a sweep (a login, ChangeOwnPassword)
// removes: one that does not decode and one of taro that has ended. The clock stands still, so the time of the
// credentials is the same after a password is set again, and a session ends only if an operation removes it.
type endFixture struct {
	e                         *mgrEnv
	admin, boss, yamada, taro Account
	tokens                    map[string][]string // by login name
}

func newEndFixture(t *testing.T) *endFixture {
	t.Helper()
	e := newMgr(t)
	f := &endFixture{e: e, tokens: make(map[string][]string)}
	f.admin = e.first(t)
	f.boss = e.add(t, "boss", RoleAdmin)
	f.yamada = e.add(t, "yamada", RoleEditor)
	f.taro = e.add(t, "taro", RoleAuthor)
	for _, login := range []string{"admin", "boss", "yamada", "yamada", "taro", "taro"} {
		_, token := e.login(t, login)
		f.tokens[login] = append(f.tokens[login], token)
	}
	e.setRawSession(t, sessKey("garbage"), "not json")
	dead := sessionFor(e.row(t, f.taro.ID), mgrTime)
	dead.CredentialsChangedAt = dead.CredentialsChangedAt.Add(time.Second)
	e.setSession(t, sessKey("dead"), dead)
	return f
}

// idOf returns the ID of the account with the login name.
func (f *endFixture) idOf(login string) string {
	return map[string]string{"admin": f.admin.ID, "boss": f.boss.ID, "yamada": f.yamada.ID, "taro": f.taro.ID}[login]
}

// ended checks that an operation removed every session row that names the account id and no other row, and that the
// tokens of the fixture agree: those of the account are refused, and the others work. before is rawSessions from before
// the operation.
func (f *endFixture) ended(t *testing.T, id string, before map[string]string) {
	t.Helper()
	after := f.e.rawSessions(t)
	named := 0
	for key, data := range before {
		s, err := decodeSession(key, []byte(data))
		mine := err == nil && s.Account == id
		got, there := after[key]
		switch {
		case mine && there:
			t.Error("a session row of the account stayed")
		case !mine && !there:
			t.Error("a row that is no session of the account was removed")
		case !mine && got != data:
			t.Error("a row that is no session of the account changed")
		}
		if mine {
			named++
		}
	}
	if named == 0 {
		t.Error("the account had no session row to end: the test checks nothing")
	}
	for key := range after {
		if _, was := before[key]; !was {
			t.Error("a session row is new")
		}
	}
	for login, tokens := range f.tokens {
		for i, token := range tokens {
			what := fmt.Sprintf("%d of %s", i+1, login)
			if f.idOf(login) == id {
				sessGone(t, f.e, token, what)
			} else {
				sessLives(t, f.e, token, what)
			}
		}
	}
}

// pend resets yamada by System and puts two session rows of the account, which the operations do not make for a pending
// account: with the clock standing still, they work again as soon as the account has a password. It returns the token of
// the reset.
func (f *endFixture) pend(t *testing.T) string {
	t.Helper()
	token, err := f.e.m.Reset(t.Context(), System, f.yamada.ID)
	if err != nil {
		t.Fatalf("Reset() = %v", err)
	}
	f.tokens["yamada"] = []string{tokSample, strings.Repeat("A", tokenChars)}
	for _, tok := range f.tokens["yamada"] {
		f.e.setSession(t, sessKey(tok), sessionFor(f.e.row(t, f.yamada.ID), mgrTime))
	}
	return token
}

// dropBoss deletes the second administrator, so that admin is the last one.
func (f *endFixture) dropBoss(t *testing.T) {
	t.Helper()
	if err := f.e.m.Delete(t.Context(), System, f.boss.ID); err != nil {
		t.Fatalf("Delete() = %v", err)
	}
	delete(f.tokens, "boss")
}

const endPassword = "a new password for the tests"

// TestOperationsEndSessions runs every operation that ends the sessions of an account, and checks that its sessions are
// gone and the rows of every other account and of no account are as they were.
func TestOperationsEndSessions(t *testing.T) {
	type action = func() error
	tests := []struct {
		name    string
		prepare func(t *testing.T, f *endFixture) action
	}{
		{"Disable by an administrator", func(t *testing.T, f *endFixture) action {
			return func() error { _, err := f.e.m.Disable(t.Context(), By(f.admin.ID), f.yamada.ID); return err }
		}},
		{"Disable by System", func(t *testing.T, f *endFixture) action {
			return func() error { _, err := f.e.m.Disable(t.Context(), System, f.yamada.ID); return err }
		}},
		{"Delete by an administrator", func(t *testing.T, f *endFixture) action {
			return func() error { return f.e.m.Delete(t.Context(), By(f.admin.ID), f.yamada.ID) }
		}},
		{"Delete by System", func(t *testing.T, f *endFixture) action {
			return func() error { return f.e.m.Delete(t.Context(), System, f.yamada.ID) }
		}},
		{"Delete of a row that cannot be read, by System", func(t *testing.T, f *endFixture) action {
			f.e.putRaw(t, f.yamada.ID, "not json")
			return func() error { return f.e.m.Delete(t.Context(), System, f.yamada.ID) }
		}},
		{"a change of the role by System", func(t *testing.T, f *endFixture) action {
			return func() error {
				_, err := f.e.m.Update(t.Context(), System, f.yamada.ID, Change{Role: ptr(RoleAdmin)})
				return err
			}
		}},
		{"ChangePassword", func(t *testing.T, f *endFixture) action {
			return func() error {
				return f.e.m.ChangePassword(t.Context(), f.yamada.ID, mgrPassword("yamada"), endPassword)
			}
		}},
		{"ChangePassword to the same password", func(t *testing.T, f *endFixture) action {
			return func() error {
				return f.e.m.ChangePassword(t.Context(), f.yamada.ID, mgrPassword("yamada"), mgrPassword("yamada"))
			}
		}},
		{"Reset by an administrator", func(t *testing.T, f *endFixture) action {
			return func() error { _, err := f.e.m.Reset(t.Context(), By(f.admin.ID), f.yamada.ID); return err }
		}},
		{"Reset by System", func(t *testing.T, f *endFixture) action {
			return func() error { _, err := f.e.m.Reset(t.Context(), System, f.yamada.ID); return err }
		}},
		{"SetPassword", func(t *testing.T, f *endFixture) action {
			token := f.pend(t)
			return func() error { _, err := f.e.m.SetPassword(t.Context(), token, endPassword); return err }
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newEndFixture(t)
			act := tt.prepare(t, f)
			before := f.e.rawSessions(t)
			if err := act(); err != nil {
				t.Fatalf("the operation = %v", err)
			}
			f.ended(t, f.yamada.ID, before)
		})
	}
}

// disable disables the account with the login name by System, which removes its sessions.
func (f *endFixture) disable(t *testing.T, login string) {
	t.Helper()
	if _, err := f.e.m.Disable(t.Context(), System, f.idOf(login)); err != nil {
		t.Fatalf("Disable() = %v", err)
	}
	delete(f.tokens, login)
}

// TestOperationsEndNothingWhenRefused runs the operations that end sessions with every kind of refusal, and with a
// Disable that has nothing to do, and checks that the store is as it was: the rows of the accounts and of the sessions,
// and no commit.
func TestOperationsEndNothingWhenRefused(t *testing.T) {
	problems := func(err error) bool {
		var errs content.Errors
		return errors.As(err, &errs)
	}
	is := func(target error) func(error) bool {
		return func(err error) bool { return errors.Is(err, target) }
	}
	disabled := func(err error) bool {
		return err != nil && strings.HasSuffix(err.Error(), " is disabled: enable it first")
	}
	type action = func() error
	tests := []struct {
		name    string
		prepare func(t *testing.T, f *endFixture) action
		want    func(error) bool
	}{
		{"Disable by an editor", func(t *testing.T, f *endFixture) action {
			return func() error { _, err := f.e.m.Disable(t.Context(), By(f.yamada.ID), f.taro.ID); return err }
		}, is(ErrForbidden)},
		{"Disable of the last administrator", func(t *testing.T, f *endFixture) action {
			f.dropBoss(t)
			return func() error { _, err := f.e.m.Disable(t.Context(), System, f.admin.ID); return err }
		}, is(ErrLastAdmin)},
		{"Disable of an account that is not there", func(t *testing.T, f *endFixture) action {
			f.e.ghostSession(t, mgrMissingID, "ghost")
			return func() error { _, err := f.e.m.Disable(t.Context(), System, mgrMissingID); return err }
		}, is(fs.ErrNotExist)},
		{"Disable of an account that is disabled", func(t *testing.T, f *endFixture) action {
			f.disable(t, "taro")
			f.e.ghostSession(t, f.taro.ID, "ghost")
			return func() error { _, err := f.e.m.Disable(t.Context(), System, f.taro.ID); return err }
		}, func(err error) bool { return err == nil }},
		{"Delete by an editor", func(t *testing.T, f *endFixture) action {
			return func() error { return f.e.m.Delete(t.Context(), By(f.yamada.ID), f.taro.ID) }
		}, is(ErrForbidden)},
		{"Delete of the last administrator", func(t *testing.T, f *endFixture) action {
			f.dropBoss(t)
			return func() error { return f.e.m.Delete(t.Context(), System, f.admin.ID) }
		}, is(ErrLastAdmin)},
		{"Delete of an account that is not there", func(t *testing.T, f *endFixture) action {
			f.e.ghostSession(t, mgrMissingID, "ghost")
			return func() error { return f.e.m.Delete(t.Context(), By(f.admin.ID), mgrMissingID) }
		}, is(fs.ErrNotExist)},
		{"Delete of a row that is not there, by System", func(t *testing.T, f *endFixture) action {
			f.e.ghostSession(t, mgrMissingID, "ghost")
			return func() error { return f.e.m.Delete(t.Context(), System, mgrMissingID) }
		}, is(fs.ErrNotExist)},
		{"the role by an editor", func(t *testing.T, f *endFixture) action {
			return func() error {
				_, err := f.e.m.Update(t.Context(), By(f.yamada.ID), f.yamada.ID, Change{Role: ptr(RoleAdmin)})
				return err
			}
		}, is(ErrForbidden)},
		{"the role of the last administrator", func(t *testing.T, f *endFixture) action {
			f.dropBoss(t)
			return func() error {
				_, err := f.e.m.Update(t.Context(), System, f.admin.ID, Change{Role: ptr(RoleEditor)})
				return err
			}
		}, is(ErrLastAdmin)},
		{"a role that is none", func(t *testing.T, f *endFixture) action {
			return func() error {
				_, err := f.e.m.Update(t.Context(), By(f.admin.ID), f.yamada.ID, Change{Role: ptr(Role("owner"))})
				return err
			}
		}, problems},
		{"a role and a login name that is not one", func(t *testing.T, f *endFixture) action {
			return func() error {
				_, err := f.e.m.Update(t.Context(), By(f.admin.ID), f.yamada.ID, Change{Role: ptr(RoleViewer), Login: ptr("Not A Login")})
				return err
			}
		}, problems},
		{"a role and a login name that another account has", func(t *testing.T, f *endFixture) action {
			return func() error {
				_, err := f.e.m.Update(t.Context(), By(f.admin.ID), f.yamada.ID, Change{Role: ptr(RoleViewer), Login: ptr("taro")})
				return err
			}
		}, problems},
		{"the role of an account that is not there", func(t *testing.T, f *endFixture) action {
			f.e.ghostSession(t, mgrMissingID, "ghost")
			return func() error {
				_, err := f.e.m.Update(t.Context(), System, mgrMissingID, Change{Role: ptr(RoleViewer)})
				return err
			}
		}, is(fs.ErrNotExist)},
		{"ChangePassword with a wrong password", func(t *testing.T, f *endFixture) action {
			return func() error { return f.e.m.ChangePassword(t.Context(), f.yamada.ID, mgrPassword("other"), endPassword) }
		}, is(ErrBadCredentials)},
		{"ChangePassword to a password that is too short", func(t *testing.T, f *endFixture) action {
			return func() error { return f.e.m.ChangePassword(t.Context(), f.yamada.ID, mgrPassword("yamada"), "short") }
		}, problems},
		{"ChangePassword of an account that is not there", func(t *testing.T, f *endFixture) action {
			f.e.ghostSession(t, mgrMissingID, "ghost")
			return func() error {
				return f.e.m.ChangePassword(t.Context(), mgrMissingID, mgrPassword("yamada"), endPassword)
			}
		}, is(fs.ErrNotExist)},
		{"Reset by an editor", func(t *testing.T, f *endFixture) action {
			return func() error { _, err := f.e.m.Reset(t.Context(), By(f.yamada.ID), f.taro.ID); return err }
		}, is(ErrForbidden)},
		{"Reset of the last administrator by an administrator", func(t *testing.T, f *endFixture) action {
			f.dropBoss(t)
			return func() error { _, err := f.e.m.Reset(t.Context(), By(f.admin.ID), f.admin.ID); return err }
		}, is(ErrLastAdmin)},
		{"Reset of an account that is disabled", func(t *testing.T, f *endFixture) action {
			f.disable(t, "taro")
			f.e.ghostSession(t, f.taro.ID, "ghost")
			return func() error { _, err := f.e.m.Reset(t.Context(), System, f.taro.ID); return err }
		}, disabled},
		{"Reset of an account that is not there", func(t *testing.T, f *endFixture) action {
			f.e.ghostSession(t, mgrMissingID, "ghost")
			return func() error { _, err := f.e.m.Reset(t.Context(), System, mgrMissingID); return err }
		}, is(fs.ErrNotExist)},
		{"SetPassword with a password that is too short", func(t *testing.T, f *endFixture) action {
			token := f.pend(t)
			delete(f.tokens, "yamada") // the rows are of a pending account: they cannot work, and the test compares the rows
			return func() error { _, err := f.e.m.SetPassword(t.Context(), token, "short"); return err }
		}, problems},
		{"SetPassword with a token that is none", func(t *testing.T, f *endFixture) action {
			f.pend(t)
			delete(f.tokens, "yamada")
			return func() error { _, err := f.e.m.SetPassword(t.Context(), tokSample, endPassword); return err }
		}, is(ErrInvalidToken)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newEndFixture(t)
			act := tt.prepare(t, f)
			before := f.e.mark(t)
			err := act()
			if !tt.want(err) {
				t.Errorf("the operation = %v, which is not the refusal that the test expects", err)
			}
			f.e.unchanged(t, before)
			for login, tokens := range f.tokens {
				for i, token := range tokens {
					sessLives(t, f.e, token, fmt.Sprintf("%d of %s", i+1, login))
				}
			}
		})
	}

	// The password changes after it was checked and before the Update: the Update finds it out and writes nothing.
	t.Run("ChangePassword with a password that changed meanwhile", func(t *testing.T) {
		f := newEndFixture(t)
		var after mgrMark
		f.e.m.hook = func(point string) {
			if point == "hashed" {
				f.e.m.hook = nil
				f.e.edit(t, f.yamada.ID, func(r *row) { r.Password = mgrHashed(t, "a password that someone set") })
				after = f.e.mark(t)
			}
		}
		err := f.e.m.ChangePassword(t.Context(), f.yamada.ID, mgrPassword("yamada"), endPassword)
		if !errors.Is(err, ErrConflict) {
			t.Errorf("ChangePassword() = %v, want ErrConflict", err)
		}
		f.e.unchanged(t, after)
		for i, token := range f.tokens["yamada"] {
			sessLives(t, f.e, token, fmt.Sprintf("%d of yamada", i+1))
		}
	})
}

// TestDisableEnableRevivesNone checks that a session made before a Disable does not work after the account is enabled
// again, though Disable leaves CredentialsChangedAt as it is, and that a login after the Enable does.
func TestDisableEnableRevivesNone(t *testing.T) {
	for _, tt := range []struct {
		name string
		by   func(f *endFixture) Actor
	}{
		{"an administrator", func(f *endFixture) Actor { return By(f.admin.ID) }},
		{"System", func(f *endFixture) Actor { return System }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newEndFixture(t)
			e, ctx, actor := f.e, t.Context(), tt.by(f)
			before := e.rawSessions(t)
			if _, err := e.m.Disable(ctx, actor, f.yamada.ID); err != nil {
				t.Fatal(err)
			}
			f.ended(t, f.yamada.ID, before)
			if _, _, err := e.m.Login(ctx, "yamada", mgrPassword("yamada"), ""); !errors.Is(err, ErrBadCredentials) {
				t.Errorf("Login() of a disabled account = %v, want ErrBadCredentials", err)
			}
			if _, err := e.m.Enable(ctx, actor, f.yamada.ID); err != nil {
				t.Fatal(err)
			}
			for i, token := range f.tokens["yamada"] {
				sessGone(t, e, token, fmt.Sprintf("%d of yamada after the account is enabled", i+1))
			}
			if rows := e.sessionsOf(t, f.yamada.ID); len(rows) != 0 {
				t.Errorf("the account has %d session rows after Enable, want none", len(rows))
			}
			p, token, err := e.m.Login(ctx, "yamada", mgrPassword("yamada"), "")
			if err != nil || p.Account.ID != f.yamada.ID {
				t.Fatalf("Login() after Enable = %+v, %v, want a session of yamada", p, err)
			}
			sessLives(t, e, token, "of the login after Enable")
			for i, token := range f.tokens["yamada"] {
				sessGone(t, e, token, fmt.Sprintf("%d of yamada, again after the login", i+1))
			}
		})
	}

	// Enable and a second Disable remove nothing: a row that names the account stays.
	t.Run("only a Disable that disables removes", func(t *testing.T) {
		f := newEndFixture(t)
		e, ctx := f.e, t.Context()
		if _, err := e.m.Disable(ctx, System, f.yamada.ID); err != nil {
			t.Fatal(err)
		}
		e.ghostSession(t, f.yamada.ID, "ghost")
		before := e.mark(t)
		if _, err := e.m.Disable(ctx, System, f.yamada.ID); err != nil {
			t.Fatal(err)
		}
		e.unchanged(t, before)
		if _, err := e.m.Enable(ctx, System, f.yamada.ID); err != nil {
			t.Fatal(err)
		}
		if !maps.Equal(e.rawSessions(t), before.sessions) {
			t.Error("Enable removed or changed a session row")
		}
		before = e.mark(t)
		if _, err := e.m.Enable(ctx, System, f.yamada.ID); err != nil {
			t.Fatal(err)
		}
		e.unchanged(t, before)
	})
}

// TestRoleChangeEndsSessions checks that a change of the role ends the sessions of the account, whichever way it goes and
// whoever makes it, and that the next login is the new role.
func TestRoleChangeEndsSessions(t *testing.T) {
	tests := []struct {
		name   string
		login  string
		to     Role
		by     func(f *endFixture) Actor
		change func(to Role) Change
	}{
		{"an editor is made a viewer", "yamada", RoleViewer, func(f *endFixture) Actor { return By(f.admin.ID) },
			func(to Role) Change { return Change{Role: &to} }},
		{"an editor is made an administrator", "yamada", RoleAdmin, func(f *endFixture) Actor { return By(f.admin.ID) },
			func(to Role) Change { return Change{Role: &to} }},
		{"an author is made an editor by System", "taro", RoleEditor, func(f *endFixture) Actor { return System },
			func(to Role) Change { return Change{Role: &to} }},
		{"an administrator is made an editor", "boss", RoleEditor, func(f *endFixture) Actor { return By(f.admin.ID) },
			func(to Role) Change { return Change{Role: &to} }},
		{"an administrator is made a viewer by itself", "boss", RoleViewer, func(f *endFixture) Actor { return By(f.boss.ID) },
			func(to Role) Change { return Change{Role: &to} }},
		{"the role and the name", "yamada", RoleAuthor, func(f *endFixture) Actor { return By(f.admin.ID) },
			func(to Role) Change { return Change{Role: &to, Name: ptr("Another Name")} }},
		{"the role and the login name", "yamada", RoleAuthor, func(f *endFixture) Actor { return System },
			func(to Role) Change { return Change{Role: &to, Login: ptr("yamada.new")} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newEndFixture(t)
			e, ctx := f.e, t.Context()
			id := f.idOf(tt.login)
			before := e.rawSessions(t)
			a, err := e.m.Update(ctx, tt.by(f), id, tt.change(tt.to))
			if err != nil || a.Role != tt.to {
				t.Fatalf("Update() = %+v, %v, want the account with the role %s", a, err, tt.to)
			}
			f.ended(t, id, before)
			p, token, err := e.m.Login(ctx, a.Login, mgrPassword(tt.login), "")
			if err != nil || p.Account.Role != tt.to {
				t.Fatalf("Login() after the change = %+v, %v, want a session with the role %s", p, err, tt.to)
			}
			if got, err := e.m.Authenticate(ctx, token); err != nil || got.Account.Role != tt.to {
				t.Errorf("Authenticate() = %+v, %v, want the role %s", got, err, tt.to)
			}
		})
	}

	// A session that is made after the change is the new role's, and the permissions follow.
	t.Run("the permissions follow the role", func(t *testing.T) {
		f := newEndFixture(t)
		e, ctx := f.e, t.Context()
		_, token := f.e.login(t, "yamada")
		p, err := e.m.Authenticate(ctx, token)
		if err != nil || Allowed(p, EditSettings, Target{}) {
			t.Fatalf("an editor before the change: Authenticate() = %v, allowed to edit the settings = %v", err, Allowed(p, EditSettings, Target{}))
		}
		if _, err := e.m.Update(ctx, By(f.admin.ID), f.yamada.ID, Change{Role: ptr(RoleAdmin)}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.m.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
			t.Errorf("Authenticate() of the session before the change = %v, want ErrNoSession", err)
		}
		p, _ = e.login(t, "yamada")
		if !Allowed(p, EditSettings, Target{}) {
			t.Error("an administrator after the change is not allowed to edit the settings")
		}
	})
}

// TestOtherChangesKeepSessions checks the changes that leave the sessions of an account alone: another field of the
// account, a role that stays, an Enable, a change that changes nothing, and every operation on another account.
func TestOtherChangesKeepSessions(t *testing.T) {
	tests := []struct {
		name   string
		victim string // the login name whose sessions the operation ends, if any: it is an operation on another account
		do     func(t *testing.T, f *endFixture) error
	}{
		{"the login name", "", func(t *testing.T, f *endFixture) error {
			_, err := f.e.m.Update(t.Context(), By(f.admin.ID), f.yamada.ID, Change{Login: ptr("yamada.new")})
			return err
		}},
		{"the name by itself", "", func(t *testing.T, f *endFixture) error {
			_, err := f.e.m.Update(t.Context(), By(f.yamada.ID), f.yamada.ID, Change{Name: ptr("New Name")})
			return err
		}},
		{"the email address by itself", "", func(t *testing.T, f *endFixture) error {
			_, err := f.e.m.Update(t.Context(), By(f.yamada.ID), f.yamada.ID, Change{Email: ptr("new.address@example.com")})
			return err
		}},
		{"the name and the email address by an administrator", "", func(t *testing.T, f *endFixture) error {
			_, err := f.e.m.Update(t.Context(), By(f.admin.ID), f.yamada.ID, Change{Name: ptr("New Name"), Email: ptr("other@example.com")})
			return err
		}},
		{"the link to an author profile", "", func(t *testing.T, f *endFixture) error {
			f.e.putAuthor(t, mgrAuthorID)
			_, err := f.e.m.Update(t.Context(), By(f.admin.ID), f.yamada.ID, Change{Author: ptr(mgrAuthorID)})
			return err
		}},
		{"the role set to the one it is", "", func(t *testing.T, f *endFixture) error {
			_, err := f.e.m.Update(t.Context(), By(f.admin.ID), f.yamada.ID, Change{Role: ptr(RoleEditor)})
			return err
		}},
		{"a change that changes nothing", "", func(t *testing.T, f *endFixture) error {
			_, err := f.e.m.Update(t.Context(), By(f.admin.ID), f.yamada.ID, Change{})
			return err
		}},
		{"Enable of an account that is enabled", "", func(t *testing.T, f *endFixture) error {
			_, err := f.e.m.Enable(t.Context(), System, f.yamada.ID)
			return err
		}},
		{"an invitation and its SetPassword", "", func(t *testing.T, f *endFixture) error {
			_, token, err := f.e.m.Invite(t.Context(), System, mgrInput("newcomer", RoleViewer))
			if err != nil {
				return err
			}
			_, err = f.e.m.SetPassword(t.Context(), token, endPassword)
			return err
		}},
		{"VerifyPasswordFactor", "", func(t *testing.T, f *endFixture) error {
			_, err := f.e.m.VerifyPasswordFactor(t.Context(), "yamada", mgrPassword("yamada"))
			return err
		}},
		{"the role of another account", "taro", func(t *testing.T, f *endFixture) error {
			_, err := f.e.m.Update(t.Context(), System, f.taro.ID, Change{Role: ptr(RoleViewer)})
			return err
		}},
		{"Disable of another account", "taro", func(t *testing.T, f *endFixture) error {
			_, err := f.e.m.Disable(t.Context(), System, f.taro.ID)
			return err
		}},
		{"Delete of another account", "taro", func(t *testing.T, f *endFixture) error {
			return f.e.m.Delete(t.Context(), System, f.taro.ID)
		}},
		{"Reset of another account", "taro", func(t *testing.T, f *endFixture) error {
			_, err := f.e.m.Reset(t.Context(), System, f.taro.ID)
			return err
		}},
		{"ChangePassword of another account", "taro", func(t *testing.T, f *endFixture) error {
			return f.e.m.ChangePassword(t.Context(), f.taro.ID, mgrPassword("taro"), endPassword)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newEndFixture(t)
			before := f.e.rawSessions(t)
			if err := tt.do(t, f); err != nil {
				t.Fatalf("the operation = %v", err)
			}
			after := f.e.rawSessions(t)
			for _, token := range f.tokens["yamada"] {
				if key := sessKey(token); before[key] == "" || after[key] != before[key] {
					t.Error("the row of a session of yamada changed or is gone")
				}
				sessLives(t, f.e, token, "of yamada")
			}
			if tt.victim != "" {
				// The operation ended the sessions of the other account, which shows that this test would see an end.
				f.ended(t, f.idOf(tt.victim), before)
				return
			}
			if !maps.Equal(before, after) {
				t.Error("a session row changed or is gone")
			}
			for login, tokens := range f.tokens {
				for i, token := range tokens {
					sessLives(t, f.e, token, fmt.Sprintf("%d of %s", i+1, login))
				}
			}
		})
	}
}

// ownNext is the password that the tests of ChangeOwnPassword change to: it passes every rule.
const ownNext = "my own new password"

// craftSession writes a live row of a session of the account r under a token that the test makes of the byte b, with the
// store's own API, and returns the token. A login would sweep the rows that cannot work.
func (e *mgrEnv) craftSession(t *testing.T, r row, b byte) string {
	t.Helper()
	token, _, err := newToken(bytes.NewReader(bytes.Repeat([]byte{b}, tokenBytes)))
	if err != nil {
		t.Fatalf("newToken() = %v", err)
	}
	e.setSession(t, sessKey(token), sessionFor(r, mgrTime))
	return token
}

// hashCounter makes the Manager count the hashes it computes, and returns the counter.
func hashCounter(e *mgrEnv) *int {
	n := new(int)
	e.m.hook = func(point string) {
		if point == "hashing" {
			*n++
		}
	}
	return n
}

func TestChangeOwnPassword(t *testing.T) {
	t.Run("the session goes on", func(t *testing.T) {
		f := newEndFixture(t)
		e, ctx := f.e, t.Context()
		// Three more sessions of yamada, written so that they do not sweep the rows that cannot work.
		for i := range 3 {
			f.tokens["yamada"] = append(f.tokens["yamada"], e.craftSession(t, e.row(t, f.yamada.ID), byte(0x21+i)))
		}
		// A token that is pending is used up by the change of the password, as it is by ChangePassword.
		e.edit(t, f.yamada.ID, func(r *row) {
			r.Token = tokenRow{Purpose: PurposeReset, Hash: testTokenHash(0xcd), ExpiresAt: mgrTime.Add(time.Hour)}
		})
		old := e.row(t, f.yamada.ID)
		oldTokens := slices.Clone(f.tokens["yamada"])
		e.clock.add(10 * time.Minute)
		now := mgrTime.Add(10 * time.Minute)
		before := e.rawSessions(t)
		hashes := hashCounter(e)

		p, token, err := e.m.ChangeOwnPassword(ctx, oldTokens[2], mgrPassword("yamada"), ownNext)
		if err != nil {
			t.Fatalf("ChangeOwnPassword() = %v", err)
		}
		if *hashes != 2 {
			t.Errorf("ChangeOwnPassword() computed %d hashes, want 2: the current password and the new one", *hashes)
		}

		// The account has the new hash and the time of the change, and no token; nothing else of it changed.
		r := e.row(t, f.yamada.ID)
		want := old
		want.Password, want.CredentialsChangedAt, want.Token = r.Password, now, tokenRow{}
		if !reflect.DeepEqual(r, want) {
			t.Errorf("the stored row of the account is not the old row with the time of the change, now %v, and no token", now)
		}
		if n, _, _, perr := parseHash(r.Password); perr != nil || n != pwLow || r.Password == old.Password {
			t.Errorf("the new hash has %d iterations, %v, and is the old hash = %v, want a new one with %d", n, perr, r.Password == old.Password, pwLow)
		}
		if ok, _, err := matchHash(r.Password, ownNext, pwLow); !ok || err != nil {
			t.Errorf("the new hash does not match the new password: %v", err)
		}

		// The Principal and the token are those of the new session, which is authenticated now.
		wantP := Principal{
			Account: r.account(),
			Session: Session{AuthenticatedAt: now, ExpiresAt: now.Add(SessionLifetime), Factors: []Factor{FactorPassword}},
		}
		if !reflect.DeepEqual(p, wantP) {
			t.Errorf("ChangeOwnPassword() returned the Principal\n%+v\nwant\n%+v", p, wantP)
		}
		if _, ok := tokenHash(token); !ok || slices.Contains(oldTokens, token) {
			t.Error("ChangeOwnPassword() returned a token that is not a new token")
		}
		rows := e.sessionsOf(t, f.yamada.ID)
		wantRow := sessionJSON(f.yamada.ID, "2026-11-08T12:10:00.123456Z", "2026-11-08T12:10:00.123456Z", "2026-11-08T12:10:00.123456Z",
			"2026-11-08T12:10:00.123456Z")
		if _, ok := rows[sessKey(token)]; !ok || len(rows) != 1 || e.rawSessions(t)[sessKey(token)] != wantRow {
			t.Errorf("yamada has %d session rows, want only the row of the new token, in the form %s", len(rows), wantRow)
		}

		// Every other session of the account ended, the one that asked too; the rows of the others are as they were, but those
		// that cannot work, which a new session sweeps as a login does.
		after := e.rawSessions(t)
		for key, data := range before {
			s, err := decodeSession(key, []byte(data))
			switch {
			case err == nil && s.Account == f.yamada.ID:
				if _, there := after[key]; there {
					t.Error("a session row of yamada stayed")
				}
			case key == sessKey("garbage") || key == sessKey("dead"):
				if _, there := after[key]; there {
					t.Error("a row that cannot work stayed")
				}
			case after[key] != data:
				t.Error("a session row of another account changed or is gone")
			}
		}
		if len(after) != len(before)-len(oldTokens)-2+1 {
			t.Errorf("there are %d session rows after, want the %d before, less 5 of yamada and 2 that cannot work, and the new one", len(after), len(before))
		}
		for i, tok := range oldTokens {
			sessGone(t, e, tok, fmt.Sprintf("%d of yamada before the change", i+1))
		}
		if got, err := e.m.Authenticate(ctx, token); err != nil || !reflect.DeepEqual(got, p) {
			t.Errorf("Authenticate() of the new token = %+v, %v, want the Principal that ChangeOwnPassword returned", got, err)
		}
		for login, tokens := range f.tokens {
			if login != "yamada" {
				for i, tok := range tokens {
					sessLives(t, e, tok, fmt.Sprintf("%d of %s", i+1, login))
				}
			}
		}

		// The new password logs in, the old one does not.
		if _, _, err := e.m.Login(ctx, "yamada", ownNext, ""); err != nil {
			t.Errorf("Login() with the new password = %v", err)
		}
		if _, _, err := e.m.Login(ctx, "yamada", mgrPassword("yamada"), ""); !errors.Is(err, ErrBadCredentials) {
			t.Errorf("Login() with the old password = %v, want ErrBadCredentials", err)
		}
	})

	// The Update reads the clock once: the time of the change of the password and the times of the new session are the same.
	t.Run("one reading of the clock", func(t *testing.T) {
		f := newEndFixture(t)
		e := f.e
		var ticks atomic.Int64
		e.m.SetClock(func() time.Time { return mgrTime.Add(time.Duration(ticks.Add(1)) * time.Second) })
		p, token, err := e.m.ChangeOwnPassword(t.Context(), f.tokens["yamada"][0], mgrPassword("yamada"), ownNext)
		if err != nil {
			t.Fatalf("ChangeOwnPassword() = %v", err)
		}
		s, ok := e.sessionOf(t, token)
		changed := e.row(t, f.yamada.ID).CredentialsChangedAt
		if !ok || !s.CreatedAt.Equal(changed) || !s.AuthenticatedAt.Equal(changed) || !s.LastSeenAt.Equal(changed) || !s.CredentialsChangedAt.Equal(changed) ||
			!p.Session.AuthenticatedAt.Equal(changed) || !p.Account.CredentialsChangedAt.Equal(changed) {
			t.Errorf("the time of the change is %v, and the new session has %v, %v, %v and %v: want one time for all", changed, s.CreatedAt, s.AuthenticatedAt, s.LastSeenAt, s.CredentialsChangedAt)
		}
	})

	t.Run("every role", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		for _, role := range Roles() {
			login := "r-" + string(role)
			e.add(t, login, role)
			p, token := e.login(t, login)
			got, fresh, err := e.m.ChangeOwnPassword(t.Context(), token, mgrPassword(login), ownNext)
			if err != nil || got.Account.ID != p.Account.ID || got.Account.Role != role {
				t.Errorf("ChangeOwnPassword() of %s = %+v, %v, want a session of the same account", role, got, err)
			}
			sessLives(t, e, fresh, "of the role "+string(role))
		}
	})

	t.Run("a text that is no session", func(t *testing.T) {
		f := newEndFixture(t)
		e := f.e
		hashes := hashCounter(e)
		before := e.mark(t)
		for _, text := range []string{"", "junk", tokSample[:42], tokSample + "A", strings.Repeat("!", tokenChars), tokSample, strings.Repeat("A", tokenChars)} {
			p, token, err := e.m.ChangeOwnPassword(t.Context(), text, mgrPassword("yamada"), ownNext)
			if !errors.Is(err, ErrNoSession) || err.Error() != "no valid session" || !reflect.DeepEqual(p, Principal{}) || token != "" {
				t.Errorf("ChangeOwnPassword() of a text with no session (%d bytes) = %+v, %v, want ErrNoSession and no token", len(text), p, err)
			}
		}
		e.unchanged(t, before)
		if *hashes != 0 {
			t.Errorf("%d hashes were computed for tokens without a session, want none", *hashes)
		}
	})

	t.Run("a session that ended", func(t *testing.T) {
		ends := []struct {
			name string
			end  func(t *testing.T, f *endFixture)
		}{
			{"a logout", func(t *testing.T, f *endFixture) {
				if err := f.e.m.Logout(t.Context(), f.tokens["yamada"][0]); err != nil {
					t.Fatal(err)
				}
			}},
			{"the idle timeout", func(t *testing.T, f *endFixture) { f.e.clock.add(SessionIdleTimeout) }},
			{"the lifetime", func(t *testing.T, f *endFixture) { f.e.clock.add(SessionLifetime) }},
			{"a disabled account", func(t *testing.T, f *endFixture) {
				f.e.edit(t, f.yamada.ID, func(r *row) { r.Disabled = true })
			}},
			{"other credentials", func(t *testing.T, f *endFixture) {
				f.e.edit(t, f.yamada.ID, func(r *row) { r.CredentialsChangedAt = r.CredentialsChangedAt.Add(time.Second) })
			}},
			{"an account that is gone", func(t *testing.T, f *endFixture) { f.e.deleteRow(t, f.yamada.ID) }},
			{"a row that cannot be read", func(t *testing.T, f *endFixture) { f.e.putRaw(t, f.yamada.ID, "not json") }},
		}
		for _, tt := range ends {
			t.Run(tt.name, func(t *testing.T) {
				f := newEndFixture(t)
				e := f.e
				hashes := hashCounter(e)
				tt.end(t, f)
				accounts := e.raw(t)
				p, token, err := e.m.ChangeOwnPassword(t.Context(), f.tokens["yamada"][0], mgrPassword("yamada"), ownNext)
				if !errors.Is(err, ErrNoSession) || !reflect.DeepEqual(p, Principal{}) || token != "" {
					t.Errorf("ChangeOwnPassword() = %+v, %v, want ErrNoSession and no token", p, err)
				}
				if !maps.Equal(e.raw(t), accounts) {
					t.Error("an account row changed")
				}
				if *hashes != 0 {
					t.Errorf("%d hashes were computed for a session that had ended, want none", *hashes)
				}
			})
		}
	})

	t.Run("the reasons to refuse", func(t *testing.T) {
		long := strings.Repeat("a", maxTypedPasswordBytes+1)
		wrong := []struct {
			name, current string
		}{
			{"a wrong password", mgrPassword("other")},
			{"an empty password", ""},
			{"the password of another account", mgrPassword("taro")},
			{"the new password", ownNext},
			{"a password of more than 8 KiB", long},
		}
		for _, tt := range wrong {
			t.Run(tt.name, func(t *testing.T) {
				f := newEndFixture(t)
				e := f.e
				hashes := hashCounter(e)
				before := e.mark(t)
				p, token, err := e.m.ChangeOwnPassword(t.Context(), f.tokens["yamada"][0], tt.current, ownNext)
				if !errors.Is(err, ErrBadCredentials) || err.Error() != sessWrong || !reflect.DeepEqual(p, Principal{}) || token != "" {
					t.Errorf("ChangeOwnPassword() = %+v, %v, want ErrBadCredentials and no token", p, err)
				}
				// The current password is the one hash, and a text that cannot be a password takes none.
				if want := map[bool]int{true: 0, false: 1}[tt.current == long]; *hashes != want {
					t.Errorf("%d hashes were computed, want %d", *hashes, want)
				}
				e.unchanged(t, before)
				sessLives(t, e, f.tokens["yamada"][0], "that asked")
			})
		}

		bad := []struct {
			name, next, msg string
		}{
			{"too short", "short", "too short: 5 characters (min 15)"},
			{"the email address", "yamada@example.com", "the same as the login name, the email address or the name"},
			{"one character repeated", strings.Repeat("a", 20), "one character repeated"},
			{"a control character", "a password with\ttab", "contains control characters"},
			{"invalid UTF-8", "a password with " + string([]byte{0xff}) + " in it", "invalid UTF-8"},
			{"more than 8 KiB", long, "too long: 8193 bytes (max 8192)"},
		}
		for _, tt := range bad {
			t.Run("a new password that is "+tt.name, func(t *testing.T) {
				f := newEndFixture(t)
				e := f.e
				hashes := hashCounter(e)
				before := e.mark(t)
				p, token, err := e.m.ChangeOwnPassword(t.Context(), f.tokens["yamada"][0], mgrPassword("yamada"), tt.next)
				mgrProblems(t, err, problem{"/password", tt.msg})
				if !reflect.DeepEqual(p, Principal{}) || token != "" {
					t.Errorf("ChangeOwnPassword() returned %+v and a token that is not empty, want neither", p)
				}
				if *hashes != 1 {
					t.Errorf("%d hashes were computed, want 1: the current password, and not the new one", *hashes)
				}
				e.unchanged(t, before)
				sessLives(t, e, f.tokens["yamada"][0], "that asked")
			})
		}

		// A row that cannot be read stops every operation that writes, as it does ChangePassword, before a hash is made.
		t.Run("a row of another account that cannot be read", func(t *testing.T) {
			f := newEndFixture(t)
			e := f.e
			e.putRaw(t, f.taro.ID, "not json")
			hashes := hashCounter(e)
			before := e.mark(t)
			p, token, err := e.m.ChangeOwnPassword(t.Context(), f.tokens["yamada"][0], mgrPassword("yamada"), ownNext)
			if want := `accounts "` + f.taro.ID + `": invalid value`; err == nil || err.Error() != want || !reflect.DeepEqual(p, Principal{}) || token != "" {
				t.Errorf("ChangeOwnPassword() = %+v, %v, want the error %q and no token", p, err, want)
			}
			if *hashes != 0 {
				t.Errorf("%d hashes were computed before the row that cannot be read stopped the call, want none", *hashes)
			}
			e.unchanged(t, before)
		})

		// Check does not allow an account with a role it does not know to edit itself, and no hash is made for it.
		t.Run("an account that Check does not allow", func(t *testing.T) {
			f := newEndFixture(t)
			e := f.e
			f.e.edit(t, f.yamada.ID, func(r *row) { r.Role = "owner" })
			hashes := hashCounter(e)
			before := e.mark(t)
			p, token, err := e.m.ChangeOwnPassword(t.Context(), f.tokens["yamada"][0], mgrPassword("yamada"), ownNext)
			mgrError(t, err, ErrForbidden, mgrNotAllowed(e.row(t, f.yamada.ID).account(), "edit its own account"))
			if !reflect.DeepEqual(p, Principal{}) || token != "" || *hashes != 0 {
				t.Errorf("ChangeOwnPassword() = %+v after %d hashes, want no Principal, no token and no hash", p, *hashes)
			}
			e.unchanged(t, before)
		})
	})

	// The clock gives a time that a session cannot hold, so the Update fails when it writes the new session: the new hash,
	// and the end of the old sessions, are not written either.
	t.Run("a session that cannot be written", func(t *testing.T) {
		f := newEndFixture(t)
		e := f.e
		e.clock.set(time.Time{})
		before := e.mark(t)
		p, token, err := e.m.ChangeOwnPassword(t.Context(), f.tokens["yamada"][0], mgrPassword("yamada"), ownNext)
		if err == nil || errors.Is(err, ErrBadCredentials) || errors.Is(err, ErrNoSession) || !strings.Contains(err.Error(), "cannot write a session of "+f.yamada.ID) ||
			!reflect.DeepEqual(p, Principal{}) || token != "" {
			t.Errorf("ChangeOwnPassword() = %+v, %v, want the error of the session and no token", p, err)
		}
		e.unchanged(t, before)
		for i, tok := range f.tokens["yamada"] {
			sessLives(t, e, tok, fmt.Sprintf("%d of yamada", i+1))
		}
	})

	t.Run("no random bits", func(t *testing.T) {
		boom := errors.New("no random bits")
		for name, tt := range map[string]struct {
			r    io.Reader
			text string
		}{
			"for the salt":  {iotest.ErrReader(boom), "cannot read a salt"},
			"for the token": {io.MultiReader(bytes.NewReader(make([]byte, saltBytes)), iotest.ErrReader(boom)), "cannot read a token"},
		} {
			f := newEndFixture(t)
			e := f.e
			e.m.rand = tt.r
			before := e.mark(t)
			p, token, err := e.m.ChangeOwnPassword(t.Context(), f.tokens["yamada"][0], mgrPassword("yamada"), ownNext)
			if !errors.Is(err, boom) || !strings.Contains(err.Error(), tt.text) || !reflect.DeepEqual(p, Principal{}) || token != "" {
				t.Errorf("ChangeOwnPassword() without random bits %s = %+v, %v, want an error that wraps the reader's and says %q", name, p, err, tt.text)
			}
			e.unchanged(t, before)
		}
	})

	t.Run("the token is already in use", func(t *testing.T) {
		f := newEndFixture(t)
		e := f.e
		e.m.rand = sessSame{}
		e.setSession(t, sessKey(tokSample), sessionFor(e.row(t, f.taro.ID), mgrTime))
		before := e.mark(t)
		p, token, err := e.m.ChangeOwnPassword(t.Context(), f.tokens["yamada"][0], mgrPassword("yamada"), ownNext)
		if err == nil || errors.Is(err, ErrBadCredentials) || errors.Is(err, ErrNoSession) || !reflect.DeepEqual(p, Principal{}) || token != "" {
			t.Errorf("ChangeOwnPassword() with a token that is in use = %+v, %v, want an error and no token", p, err)
		}
		e.unchanged(t, before)
	})

	t.Run("the store", func(t *testing.T) {
		f := newEndFixture(t)
		e := f.e
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		before := e.mark(t)
		if _, _, err := e.m.ChangeOwnPassword(ctx, f.tokens["yamada"][0], mgrPassword("yamada"), ownNext); !errors.Is(err, context.Canceled) {
			t.Errorf("ChangeOwnPassword() with a canceled context = %v, want context.Canceled", err)
		}
		e.unchanged(t, before)

		// The context ends after both hashes: the Update fails, and nothing is written.
		ctx, cancel = context.WithCancel(t.Context())
		e.m.hook = func(point string) {
			if point == "hashed" {
				cancel()
			}
		}
		if _, _, err := e.m.ChangeOwnPassword(ctx, f.tokens["yamada"][0], mgrPassword("yamada"), ownNext); !errors.Is(err, context.Canceled) {
			t.Errorf("ChangeOwnPassword() with a context that ends after the hashes = %v, want context.Canceled", err)
		}
		e.unchanged(t, before)

		if err := e.s.Close(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := e.m.ChangeOwnPassword(t.Context(), "junk", mgrPassword("yamada"), ownNext); !errors.Is(err, ErrNoSession) {
			t.Errorf("ChangeOwnPassword() of a text that is no token on a closed store = %v, want ErrNoSession", err)
		}
		if _, _, err := e.m.ChangeOwnPassword(t.Context(), f.tokens["yamada"][0], mgrPassword("yamada"), ownNext); err == nil || errors.Is(err, ErrNoSession) {
			t.Errorf("ChangeOwnPassword() on a closed store = %v, want the error of the store", err)
		}
	})
}

// deleteRowOfSession removes the session row under the key that sessKey makes of name, with the store's own API.
func (e *mgrEnv) deleteRowOfSession(t *testing.T, name string) {
	t.Helper()
	if err := e.s.Update(t.Context(), func(tx *store.Tx) error { return tx.DeleteSession(sessKey(name)) }); err != nil {
		t.Fatalf("DeleteSession() = %v", err)
	}
}

// TestChangeOwnPasswordGaps runs another operation after both hashes are made and before the Update, and checks that the
// Update goes by the state it finds: a session that ended is ErrNoSession, which comes before every other reason, a
// password that changed without ending the session is ErrConflict, and an operation on another account changes nothing.
func TestChangeOwnPasswordGaps(t *testing.T) {
	check := func(t *testing.T, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("the operation in the gap = %v", err)
		}
	}
	tests := []struct {
		name  string
		gap   func(t *testing.T, f *endFixture)
		works bool
		is    error                                    // what the error wraps
		text  func(t *testing.T, f *endFixture) string // the text of the error, if it says more than that
	}{
		{name: "Reset", gap: func(t *testing.T, f *endFixture) {
			_, err := f.e.m.Reset(t.Context(), System, f.yamada.ID)
			check(t, err)
		}, is: ErrNoSession},
		{name: "ChangePassword by ID", gap: func(t *testing.T, f *endFixture) {
			check(t, f.e.m.ChangePassword(t.Context(), f.yamada.ID, mgrPassword("yamada"), "a password that won the race"))
		}, is: ErrNoSession},
		{name: "Disable", gap: func(t *testing.T, f *endFixture) {
			_, err := f.e.m.Disable(t.Context(), System, f.yamada.ID)
			check(t, err)
		}, is: ErrNoSession},
		{name: "Delete", gap: func(t *testing.T, f *endFixture) {
			check(t, f.e.m.Delete(t.Context(), System, f.yamada.ID))
		}, is: ErrNoSession},
		{name: "a change of the role", gap: func(t *testing.T, f *endFixture) {
			_, err := f.e.m.Update(t.Context(), System, f.yamada.ID, Change{Role: ptr(RoleViewer)})
			check(t, err)
		}, is: ErrNoSession},
		{name: "a logout of the session", gap: func(t *testing.T, f *endFixture) {
			check(t, f.e.m.Logout(t.Context(), f.tokens["yamada"][0]))
		}, is: ErrNoSession},
		{name: "the row of the session removed", gap: func(t *testing.T, f *endFixture) {
			f.e.deleteRowOfSession(t, f.tokens["yamada"][0])
		}, is: ErrNoSession},
		{name: "the idle timeout", gap: func(t *testing.T, f *endFixture) { f.e.clock.add(SessionIdleTimeout) }, is: ErrNoSession},
		{name: "a disabled account, with the sessions left", gap: func(t *testing.T, f *endFixture) {
			f.e.edit(t, f.yamada.ID, func(r *row) { r.Disabled = true })
		}, is: ErrNoSession},
		{name: "other credentials, with the sessions left", gap: func(t *testing.T, f *endFixture) {
			f.e.edit(t, f.yamada.ID, func(r *row) { r.CredentialsChangedAt = r.CredentialsChangedAt.Add(time.Second) })
		}, is: ErrNoSession},
		{name: "ChangeOwnPassword from another session", gap: func(t *testing.T, f *endFixture) {
			_, _, err := f.e.m.ChangeOwnPassword(t.Context(), f.tokens["yamada"][1], mgrPassword("yamada"), "a password that won the race")
			check(t, err)
		}, is: ErrNoSession},
		{name: "another password, with the sessions left", gap: func(t *testing.T, f *endFixture) {
			f.e.edit(t, f.yamada.ID, func(r *row) { r.Password = mgrHashed(t, "a password that someone set") })
		}, is: ErrConflict, text: func(t *testing.T, f *endFixture) string {
			return "the password of " + f.yamada.ID + " changed since it was read"
		}},
		{name: "a role that Check does not allow, with the sessions left", gap: func(t *testing.T, f *endFixture) {
			f.e.edit(t, f.yamada.ID, func(r *row) { r.Role = "owner" })
		}, is: ErrForbidden, text: func(t *testing.T, f *endFixture) string {
			return mgrNotAllowed(f.e.row(t, f.yamada.ID).account(), "edit its own account")
		}},
		{name: "a row of another account that cannot be read", gap: func(t *testing.T, f *endFixture) {
			f.e.putRaw(t, f.taro.ID, "not json")
		}, text: func(t *testing.T, f *endFixture) string { return `accounts "` + f.taro.ID + `": invalid value` }},
		{name: "operations on other accounts", gap: func(t *testing.T, f *endFixture) {
			_, err := f.e.m.Disable(t.Context(), System, f.taro.ID)
			check(t, err)
			check(t, f.e.m.ChangePassword(t.Context(), f.boss.ID, mgrPassword("boss"), "a password that boss set"))
			_, _, err = f.e.m.Invite(t.Context(), System, mgrInput("newcomer", RoleViewer))
			check(t, err)
		}, works: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newEndFixture(t)
			e := f.e
			var after mgrMark
			e.m.hook = func(point string) {
				if point == "hashed" {
					e.m.hook = nil
					tt.gap(t, f)
					after = e.mark(t)
				}
			}
			p, token, err := e.m.ChangeOwnPassword(t.Context(), f.tokens["yamada"][0], mgrPassword("yamada"), ownNext)
			if tt.works {
				if err != nil || p.Account.ID != f.yamada.ID {
					t.Fatalf("ChangeOwnPassword() = %+v, %v, want a session of yamada", p, err)
				}
				sessLives(t, e, token, "of the new password")
				return
			}
			switch {
			case err == nil:
				t.Fatalf("ChangeOwnPassword() = %+v, nil, want an error", p)
			case tt.is != nil && !errors.Is(err, tt.is):
				t.Errorf("ChangeOwnPassword() = %v, want an error that wraps %v", err, tt.is)
			case tt.text != nil && err.Error() != tt.text(t, f):
				t.Errorf("ChangeOwnPassword() = %q, want %q", err, tt.text(t, f))
			}
			if !reflect.DeepEqual(p, Principal{}) || token != "" {
				t.Errorf("ChangeOwnPassword() returned a Principal %+v or a token with an error", p)
			}
			e.unchanged(t, after)
		})
	}
}

// useHash makes the stored hash of the account id the hash of the password with this many iterations, with the store's
// own API, and returns it.
func (e *mgrEnv) useHash(t *testing.T, id, password string, iterations int) string {
	t.Helper()
	hash, err := hashPassword(password, iterations, rand.Reader)
	if err != nil {
		t.Fatalf("hashPassword() = %v", err)
	}
	e.edit(t, id, func(r *row) { r.Password = hash })
	return hash
}

// iterationsOf returns the iteration count of the stored hash of the account id.
func (e *mgrEnv) iterationsOf(t *testing.T, id string) int {
	t.Helper()
	n, _, _, err := parseHash(e.row(t, id).Password)
	if err != nil {
		t.Fatalf("the stored hash does not parse: %v", err)
	}
	return n
}

// TestLoginRehash checks that a login makes a password hash with fewer iterations than the Manager's again, in the Update
// that writes the session, and changes nothing else of the account.
func TestLoginRehash(t *testing.T) {
	// setup makes yamada with a session and the hash of its password with this many iterations; the Manager's count is pwLow.
	setup := func(t *testing.T, iterations int) (e *mgrEnv, a Account, session string) {
		t.Helper()
		e = newMgr(t)
		e.first(t)
		a = e.add(t, "yamada", RoleEditor)
		_, session = e.login(t, "yamada")
		e.useHash(t, a.ID, mgrPassword("yamada"), iterations)
		e.clock.add(time.Minute)
		return e, a, session
	}

	t.Run("a hash with fewer iterations is made again", func(t *testing.T) {
		e, a, session := setup(t, pwLow-1)
		old := e.row(t, a.ID)
		hashes := hashCounter(e)
		p, token, err := e.m.Login(t.Context(), "yamada", mgrPassword("yamada"), "")
		if err != nil {
			t.Fatalf("Login() = %v", err)
		}
		if *hashes != 2 {
			t.Errorf("Login() computed %d hashes, want 2: the one that matched and the new one, both through the gate", *hashes)
		}
		now := e.row(t, a.ID)
		want := old
		want.Password = now.Password
		if !reflect.DeepEqual(now, want) {
			t.Error("the stored row changed in more than the hash: CredentialsChangedAt and the other fields must stay")
		}
		if now.Password == old.Password || e.iterationsOf(t, a.ID) != pwLow {
			t.Errorf("the stored hash has %d iterations, and is the old one = %v, want a new one with %d", e.iterationsOf(t, a.ID), now.Password == old.Password, pwLow)
		}
		if ok, _, err := matchHash(now.Password, mgrPassword("yamada"), pwLow); !ok || err != nil {
			t.Errorf("the new hash does not match the password: %v", err)
		}
		if !reflect.DeepEqual(p.Account, now.account()) {
			t.Errorf("Login() returned the account %+v, want the stored one %+v", p.Account, now.account())
		}
		// The sessions of the account go on: the one made before, and the new one.
		sessLives(t, e, session, "made before the hash was made again")
		sessLives(t, e, token, "of the login")
		if rows := e.sessionsOf(t, a.ID); len(rows) != 2 {
			t.Errorf("the account has %d session rows, want 2", len(rows))
		}
		s, _ := e.sessionOf(t, token)
		if !s.CredentialsChangedAt.Equal(old.CredentialsChangedAt) {
			t.Error("the new session keeps another time of the credentials than the account has")
		}

		// A hash with the Manager's count is not made again.
		*hashes = 0
		before := e.raw(t)
		if _, _, err := e.m.Login(t.Context(), "yamada", mgrPassword("yamada"), ""); err != nil {
			t.Fatal(err)
		}
		if *hashes != 1 || !maps.Equal(e.raw(t), before) {
			t.Errorf("the next login computed %d hashes and left the rows as they were = %v, want 1 and true", *hashes, maps.Equal(e.raw(t), before))
		}
	})

	t.Run("the count of the Manager", func(t *testing.T) {
		e, a, _ := setup(t, pwLow)
		e.m.iterations = pwLow + 3
		if _, _, err := e.m.Login(t.Context(), "yamada", mgrPassword("yamada"), ""); err != nil {
			t.Fatal(err)
		}
		if got := e.iterationsOf(t, a.ID); got != pwLow+3 {
			t.Errorf("the stored hash has %d iterations, want the Manager's, %d", got, pwLow+3)
		}
	})

	t.Run("a hash that is not older is not written", func(t *testing.T) {
		for _, iterations := range []int{pwLow, pwLow + 1, 50} {
			e, _, _ := setup(t, iterations)
			hashes := hashCounter(e)
			before := e.raw(t)
			if _, _, err := e.m.Login(t.Context(), "yamada", mgrPassword("yamada"), ""); err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(e.raw(t), before) || *hashes != 1 {
				t.Errorf("a login with a hash of %d iterations left the rows as they were = %v after %d hashes, want true and 1",
					iterations, maps.Equal(e.raw(t), before), *hashes)
			}
		}
	})

	// The rules for a new password do not apply: the hash is of the password that was typed and matched.
	t.Run("a password that the rules refuse now", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		a := e.add(t, "yamada", RoleEditor)
		e.useHash(t, a.ID, "short", 1)
		if _, _, err := e.m.Login(t.Context(), "yamada", "short", ""); err != nil {
			t.Fatalf("Login() = %v", err)
		}
		if ok, _, err := matchHash(e.row(t, a.ID).Password, "short", pwLow); !ok || err != nil || e.iterationsOf(t, a.ID) != pwLow {
			t.Errorf("the new hash matches the password = %v, %v, with %d iterations, want true and %d", ok, err, e.iterationsOf(t, a.ID), pwLow)
		}
	})

	t.Run("a password in another form", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		a := e.add(t, "yamada", RoleEditor)
		composed, decomposed := "Caf"+eAcute+" marker nine password", "Cafe"+acute+" marker nine password"
		e.useHash(t, a.ID, composed, 1)
		if _, _, err := e.m.Login(t.Context(), "yamada", decomposed, ""); err != nil {
			t.Fatalf("Login() = %v", err)
		}
		for _, typed := range []string{composed, decomposed} {
			if ok, _, err := matchHash(e.row(t, a.ID).Password, typed, pwLow); !ok || err != nil {
				t.Errorf("the new hash does not match a form of the password: %v", err)
			}
		}
	})

	t.Run("a login that fails writes no hash", func(t *testing.T) {
		e, _, _ := setup(t, pwLow-1)
		off := e.add(t, "off", RoleEditor)
		e.useHash(t, off.ID, mgrPassword("off"), pwLow-1)
		e.edit(t, off.ID, func(r *row) { r.Disabled = true })
		before := e.mark(t)
		fails := []struct{ name, login, password string }{
			{"a wrong password", "yamada", mgrPassword("other")},
			{"a disabled account", "off", mgrPassword("off")},
		}
		for _, tt := range fails {
			if _, _, err := e.m.Login(t.Context(), tt.login, tt.password, ""); !errors.Is(err, ErrBadCredentials) {
				t.Errorf("Login() with %s = %v, want ErrBadCredentials", tt.name, err)
			}
		}
		e.unchanged(t, before)
	})

	t.Run("VerifyPasswordFactor does not make it again", func(t *testing.T) {
		e, _, _ := setup(t, pwLow-1)
		hashes := hashCounter(e)
		before := e.mark(t)
		if _, err := e.m.VerifyPasswordFactor(t.Context(), "yamada", mgrPassword("yamada")); err != nil {
			t.Fatal(err)
		}
		if *hashes != 1 {
			t.Errorf("VerifyPasswordFactor() computed %d hashes, want 1", *hashes)
		}
		e.unchanged(t, before)
	})

	// The new hash is written with the session or not at all.
	t.Run("the context ends before the Update", func(t *testing.T) {
		e, _, _ := setup(t, pwLow-1)
		ctx, cancel := context.WithCancel(t.Context())
		e.m.hook = func(point string) {
			if point == "session" {
				cancel()
			}
		}
		before := e.mark(t)
		if _, _, err := e.m.Login(ctx, "yamada", mgrPassword("yamada"), ""); !errors.Is(err, context.Canceled) {
			t.Errorf("Login() = %v, want context.Canceled", err)
		}
		e.unchanged(t, before)
	})

	t.Run("no random bits for the new hash", func(t *testing.T) {
		e, _, _ := setup(t, pwLow-1)
		boom := errors.New("no random bits")
		e.m.rand = iotest.ErrReader(boom)
		before := e.mark(t)
		p, token, err := e.m.Login(t.Context(), "yamada", mgrPassword("yamada"), "")
		if !errors.Is(err, boom) || errors.Is(err, ErrBadCredentials) || !reflect.DeepEqual(p, Principal{}) || token != "" {
			t.Errorf("Login() = %+v, %v, want the error of the reader and no token", p, err)
		}
		e.unchanged(t, before)
	})

	// A session cannot be written, so the hash is not written either.
	t.Run("a session that cannot be written", func(t *testing.T) {
		e, _, _ := setup(t, pwLow-1)
		e.m.rand = sessSame{}
		taro := e.add(t, "taro", RoleEditor)
		e.setSession(t, sessKey(tokSample), sessionFor(e.row(t, taro.ID), e.clock.now()))
		before := e.mark(t)
		if _, _, err := e.m.Login(t.Context(), "yamada", mgrPassword("yamada"), ""); err == nil || errors.Is(err, ErrBadCredentials) {
			t.Errorf("Login() with a token that is in use = %v, want the error of the session", err)
		}
		e.unchanged(t, before)
	})

	t.Run("the account is read again for the new hash", func(t *testing.T) {
		e, a, _ := setup(t, pwLow-1)
		var ran bool
		e.m.hook = func(point string) {
			if point == "session" && !ran {
				ran = true
				if _, err := e.m.Update(t.Context(), By(a.ID), a.ID, Change{Name: ptr("New Name")}); err != nil {
					t.Error(err)
				}
			}
		}
		p, _, err := e.m.Login(t.Context(), "yamada", mgrPassword("yamada"), "")
		if err != nil || p.Account.Name != "New Name" {
			t.Fatalf("Login() = %+v, %v, want the account with the new name", p.Account, err)
		}
		if got := e.row(t, a.ID); got.Name != "New Name" || e.iterationsOf(t, a.ID) != pwLow {
			t.Errorf("the stored row has the name %q and a hash of %d iterations, want the new name and %d iterations", got.Name, e.iterationsOf(t, a.ID), pwLow)
		}
	})

	// A check that follows a match of the old hash fails once the new hash is written: the password changed since it matched.
	t.Run("a call that matched the old hash fails once", func(t *testing.T) {
		calls := []struct {
			name string
			do   func(e *mgrEnv) error
		}{
			{"VerifyPasswordFactor", func(e *mgrEnv) error {
				_, err := e.m.VerifyPasswordFactor(t.Context(), "yamada", mgrPassword("yamada"))
				return err
			}},
			{"Login", func(e *mgrEnv) error {
				_, _, err := e.m.Login(t.Context(), "yamada", mgrPassword("yamada"), "")
				return err
			}},
		}
		for _, point := range []string{"verified", "session"} {
			for _, tt := range calls {
				if tt.name == "VerifyPasswordFactor" && point == "session" {
					continue // a point that only Login has
				}
				t.Run(tt.name+" at "+point, func(t *testing.T) {
					e, a, _ := setup(t, pwLow-1)
					ran := false
					var inner error
					e.m.hook = func(p string) {
						if p == point && !ran {
							ran = true
							_, _, inner = e.m.Login(t.Context(), "yamada", mgrPassword("yamada"), "")
						}
					}
					rows := len(e.rawSessions(t))
					if err := tt.do(e); !errors.Is(err, ErrBadCredentials) {
						t.Errorf("%s that matched the old hash = %v, want ErrBadCredentials", tt.name, err)
					}
					if inner != nil || e.iterationsOf(t, a.ID) != pwLow {
						t.Errorf("the login in the gap = %v, the stored hash has %d iterations, want nil and %d", inner, e.iterationsOf(t, a.ID), pwLow)
					}
					// Only the login in the gap made a session, and the hash that it wrote is not written over.
					if got := len(e.rawSessions(t)); got != rows+1 {
						t.Errorf("there are %d session rows, want %d: the login in the gap made one", got, rows+1)
					}
					e.m.hook = nil
					if err := tt.do(e); err != nil {
						t.Errorf("%s again = %v, want nil: the new hash matches the password", tt.name, err)
					}
				})
			}
		}
	})

	t.Run("logins at the same time", func(t *testing.T) {
		const n = 6
		rounds := 15
		if testing.Short() {
			rounds = 4
		}
		e, a, _ := setup(t, pwLow-1)
		for round := range rounds {
			e.useHash(t, a.ID, mgrPassword("yamada"), pwLow-1)
			old := e.row(t, a.ID)
			calls := make([]func() error, n)
			for i := range calls {
				calls[i] = func() error {
					_, _, err := e.m.Login(t.Context(), "yamada", mgrPassword("yamada"), "")
					return err
				}
			}
			succeeded := 0
			for _, err := range mgrRunTogether(calls...) {
				switch {
				case err == nil:
					succeeded++
				case !errors.Is(err, ErrBadCredentials):
					t.Errorf("round %d: Login() = %v, want nil or ErrBadCredentials", round, err)
				}
			}
			now := e.row(t, a.ID)
			want := old
			want.Password = now.Password
			if succeeded == 0 || e.iterationsOf(t, a.ID) != pwLow || !reflect.DeepEqual(now, want) {
				t.Errorf("round %d: %d logins succeeded, the hash has %d iterations and the rest of the row is as it was = %v, want at least one, %d and true",
					round, succeeded, e.iterationsOf(t, a.ID), reflect.DeepEqual(now, want), pwLow)
			}
		}
	})
}

// TestRemoveSessions checks which rows removeSessions removes: every row that decodes and names the account, live or not,
// and no other.
func TestRemoveSessions(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	yamada := e.row(t, e.add(t, "yamada", RoleEditor).ID)
	taro := e.row(t, e.add(t, "taro", RoleEditor).ID)
	expired := sessionFor(yamada, mgrTime.Add(-3*time.Hour))
	changed := sessionFor(yamada, mgrTime)
	changed.CredentialsChangedAt = changed.CredentialsChangedAt.Add(time.Second)
	rows := []struct {
		name    string
		session sessionRow
		raw     string
		removed bool
	}{
		{name: "a live session of yamada", session: sessionFor(yamada, mgrTime), removed: true},
		{name: "a session of yamada that has expired", session: expired, removed: true},
		{name: "a session of yamada with other credentials", session: changed, removed: true},
		{name: "a live session of taro", session: sessionFor(taro, mgrTime)},
		{name: "a session of an account that is not there", session: sessionRow{
			Account: mgrMissingID, CreatedAt: mgrTime, AuthenticatedAt: mgrTime, LastSeenAt: mgrTime, Factors: []Factor{FactorPassword},
		}},
		{name: "a row that is not JSON", raw: "not json"},
		{name: "a row of yamada with a key that is not known", raw: `{"account":"` + yamada.ID + `","extra":1}`},
	}
	for _, r := range rows {
		if r.raw != "" {
			e.setRawSession(t, sessKey(r.name), r.raw)
		} else {
			e.setSession(t, sessKey(r.name), r.session)
		}
	}
	removeFor := func(id string) error {
		return e.s.Update(t.Context(), func(tx *store.Tx) error { return removeSessions(tx, id) })
	}
	if err := removeFor(yamada.ID); err != nil {
		t.Fatalf("removeSessions() = %v", err)
	}
	got := e.rawSessions(t)
	for _, r := range rows {
		if _, there := got[sessKey(r.name)]; there == r.removed {
			t.Errorf("%s: has a row = %v, want %v", r.name, there, !r.removed)
		}
	}

	// Nothing to remove writes nothing.
	before := e.mark(t)
	nobody := content.FormatID(content.RefUser, [16]byte{0: 0x02, 15: 0xfe}) // that no row names
	for _, id := range []string{yamada.ID, nobody} {
		if err := removeFor(id); err != nil {
			t.Fatalf("removeSessions() with nothing to remove = %v", err)
		}
	}
	e.unchanged(t, before)

	// The errors of the store are returned: a transaction that has ended, and one that cannot write.
	var ended *store.Tx
	if err := e.s.View(t.Context(), func(tx *store.Tx) error { ended = tx; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := removeSessions(ended, yamada.ID); err == nil || err.Error() != "the transaction has ended" {
		t.Errorf("removeSessions() in a transaction that has ended = %v, want an error that it has ended", err)
	}
	view := func(id string) error {
		return e.s.View(t.Context(), func(tx *store.Tx) error { return removeSessions(tx, id) })
	}
	if err := view(taro.ID); err == nil || err.Error() != "read-only transaction" {
		t.Errorf("removeSessions() of a row to remove in a View = %v, want the error of the store", err)
	}
	if err := view(yamada.ID); err != nil {
		t.Errorf("removeSessions() of no row in a View = %v, want nil: it writes nothing", err)
	}
}

// TestChangePasswordWithAClockTheRowCannotHold makes ChangePassword write a time that the row cannot hold, and checks that
// nothing is written: the account is not changed and its sessions stay.
func TestChangePasswordWithAClockTheRowCannotHold(t *testing.T) {
	f := newEndFixture(t)
	e := f.e
	e.clock.set(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))
	before := e.mark(t)
	err := e.m.ChangePassword(t.Context(), f.taro.ID, mgrPassword("taro"), endPassword)
	if err == nil || errors.Is(err, ErrBadCredentials) || !strings.Contains(err.Error(), "year outside of range") {
		t.Errorf("ChangePassword() with a clock the row cannot hold = %v, want the error of the encoding of the row", err)
	}
	e.unchanged(t, before)
}

// TestChangePasswordRefusesBeforeAHash checks that a row that cannot be read stops ChangePassword before it makes a hash, as
// it stops every operation that writes.
func TestChangePasswordRefusesBeforeAHash(t *testing.T) {
	f := newEndFixture(t)
	e := f.e
	e.putRaw(t, f.taro.ID, "not json")
	hashes := hashCounter(e)
	before := e.mark(t)
	err := e.m.ChangePassword(t.Context(), f.yamada.ID, mgrPassword("yamada"), endPassword)
	if want := `accounts "` + f.taro.ID + `": invalid value`; err == nil || err.Error() != want {
		t.Errorf("ChangePassword() = %v, want the error %q", err, want)
	}
	if *hashes != 0 {
		t.Errorf("%d hashes were computed before the row that cannot be read stopped the call, want none", *hashes)
	}
	e.unchanged(t, before)
}
