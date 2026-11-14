package account

import (
	"context"
	"crypto/rand"
	"errors"
	"io/fs"
	"maps"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// mgrHashed returns the password hash of the password with the iteration count pwLow.
func mgrHashed(t *testing.T, password string) string {
	t.Helper()
	hash, err := hashPassword(password, pwLow, rand.Reader)
	if err != nil {
		t.Fatalf("hashPassword() = %v", err)
	}
	return hash
}

// mgrActiveRow returns the row of an active account number n whose password is the password.
func mgrActiveRow(t *testing.T, n int, login string, role Role, password string) row {
	t.Helper()
	r := mgrRow(n, login, role)
	r.Password = mgrHashed(t, password)
	return r
}

func TestSetPassword(t *testing.T) {
	e := newMgr(t)
	admin := e.first(t)
	pending, token := e.invite(t, "yamada", RoleEditor)
	adminRow := e.raw(t)[admin.ID]
	e.clock.add(2 * time.Hour)
	now := mgrTime.Add(2 * time.Hour)
	a, err := e.m.SetPassword(t.Context(), mgrClient, token, mgrPassword("yamada"))
	if err != nil {
		t.Fatalf("SetPassword() = %v", err)
	}
	want := pending
	want.Status, want.CredentialsChangedAt, want.Token = StatusActive, now, TokenInfo{}
	if a != want {
		t.Errorf("SetPassword() = %+v, want %+v", a, want)
	}
	r := e.row(t, a.ID)
	if n, _, _, err := parseHash(r.Password); err != nil || n != pwLow {
		t.Errorf("the stored password = %d iterations, %v, want a hash of %d iterations", n, err, pwLow)
	}
	if !r.Token.IsZero() || !r.CredentialsChangedAt.Equal(now) || !r.CreatedAt.Equal(mgrTime) || r.Disabled {
		t.Errorf("the stored row has the token %v, the credentials time %v, the creation time %v and disabled %v",
			r.Token, r.CredentialsChangedAt, r.CreatedAt, r.Disabled)
	}
	if got, err := e.m.VerifyPasswordFactor(t.Context(), "yamada", mgrPassword("yamada")); err != nil || got != a {
		t.Errorf("VerifyPasswordFactor() = %+v, %v, want %+v", got, err, a)
	}
	if got := e.raw(t)[admin.ID]; got != adminRow {
		t.Error("the row of the other account changed")
	}
}

func TestSetPasswordOnce(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	a, token := e.invite(t, "yamada", RoleEditor)
	if _, err := e.m.SetPassword(t.Context(), mgrClient, token, mgrPassword("yamada")); err != nil {
		t.Fatal(err)
	}
	before := e.mark(t)
	_, err := e.m.SetPassword(t.Context(), mgrClient, token, "a second long password")
	mgrError(t, err, ErrInvalidToken, "invalid or expired token")
	e.recorded(t, before, "token.failure/unknown") // the token is used up: no account has it
	if _, err := e.m.VerifyPasswordFactor(t.Context(), "yamada", mgrPassword("yamada")); err != nil {
		t.Errorf("VerifyPasswordFactor() with the first password = %v, want nil", err)
	}
	if _, err := e.m.Get(t.Context(), a.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSetPasswordWrongTokens(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	_, token := e.invite(t, "yamada", RoleEditor)
	_, another := e.invite(t, "taro", RoleEditor)
	fresh, _, err := newToken(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := tokenHash(token)
	// result is what the login history records of the token: the text of a token that nobody has is another than one
	// that is none.
	tests := []struct{ name, token, result string }{
		{"a token that nobody has", fresh, "unknown"},
		{"empty", "", "malformed"},
		{"a letter", "x", "malformed"},
		{"one character short", token[:len(token)-1], "malformed"},
		{"one character more", token + "A", "malformed"},
		{"twice the token", token + token, "malformed"},
		{"a megabyte of letters", strings.Repeat("A", 1<<20), "malformed"},
		{"a line feed at the end", token + "\n", "malformed"},
		{"a space at the start", " " + token, "malformed"},
		{"the hash of the token", hash, "malformed"},
		{"the token as an ID", "usr_" + token, "malformed"},
		{"padded", token[:42] + "==", "malformed"},
		{"the standard alphabet", strings.NewReplacer("-", "+", "_", "/").Replace(token) + "+", "malformed"},
	}
	before := e.mark(t)
	hashed := 0
	e.m.hook = func(point string) {
		if point == "hashed" {
			hashed++
		}
	}
	var want []string
	for _, tt := range tests {
		// Each in a minute of its own: only the first refusalsPerMinute of a minute are recorded one by one.
		e.clock.add(time.Minute)
		a, err := e.m.SetPassword(t.Context(), mgrClient, tt.token, mgrPassword("yamada"))
		if !errors.Is(err, ErrInvalidToken) || a != (Account{}) {
			t.Errorf("SetPassword() with %s = %+v, %v, want ErrInvalidToken", tt.name, a, err)
		}
		want = append(want, "token.failure/"+tt.result)
	}
	e.recorded(t, before, want...)
	if hashed != 0 {
		t.Errorf("%d passwords were hashed for the tokens that do not work, want none", hashed)
	}
	e.m.hook = nil
	// Neither token was used up by the others.
	for login, tok := range map[string]string{"yamada": token, "taro": another} {
		if _, err := e.m.SetPassword(t.Context(), mgrClient, tok, mgrPassword(login)); err != nil {
			t.Errorf("SetPassword() of %s after the wrong tokens = %v, want nil", login, err)
		}
	}
}

func TestSetPasswordExpiry(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	a, token := e.invite(t, "yamada", RoleEditor)
	taro, late := e.invite(t, "taro", RoleEditor)
	expires := a.Token.ExpiresAt
	if !expires.Equal(mgrTime.Add(7 * 24 * time.Hour)) {
		t.Fatalf("the invitation expires at %v, want 7 days after %v", expires, mgrTime)
	}
	e.clock.set(expires.Add(-time.Nanosecond))
	if _, err := e.m.SetPassword(t.Context(), mgrClient, token, mgrPassword("yamada")); err != nil {
		t.Errorf("SetPassword() a nanosecond before the expiry = %v, want nil", err)
	}
	e.clock.set(expires)
	before := e.mark(t)
	hashed := 0
	e.m.hook = func(point string) {
		if point == "hashed" {
			hashed++
		}
	}
	if _, err := e.m.SetPassword(t.Context(), mgrClient, late, mgrPassword("taro")); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("SetPassword() at the expiry = %v, want ErrInvalidToken", err)
	}
	e.clock.set(expires.Add(24 * time.Hour))
	if _, err := e.m.SetPassword(t.Context(), mgrClient, late, mgrPassword("taro")); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("SetPassword() a day after the expiry = %v, want ErrInvalidToken", err)
	}
	e.recorded(t, before, "token.failure/expired", "token.failure/expired")
	for _, r := range e.records(t, before) {
		if r.Account != taro.ID || r.Client != mgrClient.record() || r.Log != audit.LoginLog {
			t.Errorf("the record of an expired token is %+v, want the account taro and the client of the request", r)
		}
	}
	if hashed != 0 {
		t.Errorf("%d passwords were hashed for the tokens that ended, want none", hashed)
	}
}

func TestSetPasswordDisabled(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	a, token := e.invite(t, "yamada", RoleEditor)
	if _, err := e.m.Disable(t.Context(), System, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.SetPassword(t.Context(), mgrClient, token, mgrPassword("yamada")); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("SetPassword() of a disabled account = %v, want ErrInvalidToken", err)
	}
	// Enabling does not give the token back: a reset gives a new one.
	if _, err := e.m.Enable(t.Context(), System, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.SetPassword(t.Context(), mgrClient, token, mgrPassword("yamada")); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("SetPassword() after Enable = %v, want ErrInvalidToken", err)
	}
	next, err := e.m.Reset(t.Context(), System, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := e.m.SetPassword(t.Context(), mgrClient, next, mgrPassword("yamada")); err != nil || got.Status != StatusActive {
		t.Errorf("SetPassword() with the new token = %+v, %v, want an active account", got, err)
	}
}

func TestSetPasswordProblems(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	in := Input{Login: "a-long-login-name", Name: "A Long Display Name", Email: "long.email.address@example.com", Role: RoleEditor}
	_, token, err := e.m.Invite(t.Context(), System, in)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct{ name, password, msg string }{
		{"short", "fourteen chars", pwShort(14)},
		{"long", strings.Repeat("ab", 128) + "c", pwLong(257)},
		{"a control character", mgrPassword("a") + "\t", pwControl},
		{"invalid UTF-8", "invalid \xff password", pwInvalidUTF8},
		{"the login name", "a-long-login-name", pwSame},
		{"the login name in upper case", "A-LONG-LOGIN-NAME", pwSame},
		{"the email address", "long.email.address@example.com", pwSame},
		{"the email address in upper case", "LONG.EMAIL.ADDRESS@EXAMPLE.COM", pwSame},
		{"the name", "A Long Display Name", pwSame},
		{"the name in lower case", "a long display name", pwSame},
		{"one character", strings.Repeat("z", 30), pwRepeated},
	}
	before := e.mark(t)
	hashed := 0
	e.m.hook = func(point string) {
		if point == "hashed" {
			hashed++
		}
	}
	for _, tt := range tests {
		a, err := e.m.SetPassword(t.Context(), mgrClient, token, tt.password)
		mgrProblems(t, err, problem{"/password", tt.msg})
		if a != (Account{}) {
			t.Errorf("SetPassword() with %s returned the account %+v", tt.name, a)
		}
	}
	// The token is as it was, and nothing was hashed for a password that is refused.
	e.unchanged(t, before)
	if hashed != 0 {
		t.Errorf("%d passwords were hashed for the passwords that break a rule, want none", hashed)
	}
	if a, err := e.m.SetPassword(t.Context(), mgrClient, token, "a good long password"); err != nil || a.Status != StatusActive || hashed != 1 {
		t.Errorf("SetPassword() after the problems = %+v, %v after %d hashes, want an active account after 1", a, err, hashed)
	}
}

// TestCheckPassword checks that CheckPassword finds, for an account, the problems that SetPassword finds for it, with the
// same messages, which have nothing of the password, and none for a password that SetPassword takes.
func TestCheckPassword(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	in := Input{Login: "a-long-login-name", Name: "A Long Display Name", Email: "long.email.address@example.com", Role: RoleEditor}
	a, token, err := e.m.Invite(t.Context(), System, in)
	if err != nil {
		t.Fatal(err)
	}
	good := "a good long password"
	if err := CheckPassword(good, a); err != nil {
		t.Errorf("CheckPassword(%q) = %v, want nil", good, err)
	}
	tests := []struct{ name, password, msg string }{
		{"short", "fourteen chars", pwShort(14)},
		{"the login name", "a-long-login-name", pwSame},
		{"the login name in upper case", "A-LONG-LOGIN-NAME", pwSame},
		{"the email address", "long.email.address@example.com", pwSame},
		{"the name", "A Long Display Name", pwSame},
		{"one character", strings.Repeat("z", 30), pwRepeated},
		{"a control character", mgrPassword("a") + "\t", pwControl},
		{"more than 8 KiB", strings.Repeat("ab", 4097), pwBytes(8194)},
	}
	for _, tt := range tests {
		err := CheckPassword(tt.password, a)
		mgrProblems(t, err, problem{"/password", tt.msg})
		if strings.Contains(err.Error(), tt.password) {
			t.Errorf("CheckPassword() with %s has the password in its error: %v", tt.name, err)
		}
		_, want := e.m.SetPassword(t.Context(), mgrClient, token, tt.password)
		if want == nil || err.Error() != want.Error() {
			t.Errorf("CheckPassword() with %s = %v, want the error of SetPassword, %v", tt.name, err, want)
		}
	}
	// The account of the check is the one that SetPassword sets the password of: the same text is fine for another.
	other := a
	other.Login, other.Name, other.Email = "other", "Other", "other@example.com"
	if err := CheckPassword("a-long-login-name", other); err != nil {
		t.Errorf("CheckPassword() of the login name of another account = %v, want nil", err)
	}
	if _, err := e.m.SetPassword(t.Context(), mgrClient, token, good); err != nil {
		t.Errorf("SetPassword(%q) = %v, want it to take the password that CheckPassword takes", good, err)
	}
}

func TestSetPasswordAfterReset(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	a := e.add(t, "yamada", RoleEditor)
	e.clock.add(time.Hour)
	token, err := e.m.Reset(t.Context(), System, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.clock.add(time.Hour)
	now := e.clock.now()
	got, err := e.m.SetPassword(t.Context(), mgrClient, token, "the new long password")
	if err != nil {
		t.Fatalf("SetPassword() = %v", err)
	}
	if got.Status != StatusActive || !got.CredentialsChangedAt.Equal(now) || got.Token != (TokenInfo{}) {
		t.Errorf("SetPassword() = %+v, want an active account with the credentials time %v and no token", got, now)
	}
	if _, err := e.m.VerifyPasswordFactor(t.Context(), "yamada", "the new long password"); err != nil {
		t.Errorf("VerifyPasswordFactor() with the new password = %v, want nil", err)
	}
	if _, err := e.m.VerifyPasswordFactor(t.Context(), "yamada", mgrPassword("yamada")); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("VerifyPasswordFactor() with the old password = %v, want ErrBadCredentials", err)
	}
}

func TestSetPasswordOfAnActiveAccount(t *testing.T) {
	// A token sets a password also for an account that has one: a row that the operations do not make now, but that a
	// credential of another kind will.
	e := newMgr(t)
	e.first(t)
	_, token := e.invite(t, "yamada", RoleEditor)
	r := e.rows(t)[1]
	r.Password, r.CredentialsChangedAt = mgrHashed(t, mgrPassword("old")), mgrTime
	e.set(t, r)
	e.clock.add(time.Hour)
	if _, err := e.m.SetPassword(t.Context(), mgrClient, token, mgrPassword("yamada")); err != nil {
		t.Fatalf("SetPassword() = %v", err)
	}
	if _, err := e.m.VerifyPasswordFactor(t.Context(), "yamada", mgrPassword("old")); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("VerifyPasswordFactor() with the old password = %v, want ErrBadCredentials", err)
	}
	if got := e.row(t, r.ID); !got.CredentialsChangedAt.Equal(e.clock.now()) || !got.Token.IsZero() {
		t.Errorf("the row has the credentials time %v and a token %v", got.CredentialsChangedAt, !got.Token.IsZero())
	}
}

func TestSetPasswordNormalization(t *testing.T) {
	composed := "Caf" + eAcute + " au lait, s'il vous plait"
	decomposed := "Cafe" + acute + " au lait, s'il vous plait"
	if composed == decomposed {
		t.Fatal("the two forms are the same")
	}
	for _, tt := range []struct{ name, set, verify string }{
		{"composed, then decomposed", composed, decomposed},
		{"decomposed, then composed", decomposed, composed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newMgr(t)
			e.first(t)
			_, token := e.invite(t, "yamada", RoleEditor)
			if _, err := e.m.SetPassword(t.Context(), mgrClient, token, tt.set); err != nil {
				t.Fatal(err)
			}
			if _, err := e.m.VerifyPasswordFactor(t.Context(), "yamada", tt.verify); err != nil {
				t.Errorf("VerifyPasswordFactor() = %v, want nil", err)
			}
		})
	}
}

func TestSetPasswordFailures(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	_, token := e.invite(t, "yamada", RoleEditor)
	boom := errors.New("no random bits")
	e.m.rand = iotest.ErrReader(boom)
	before := e.mark(t)
	_, err := e.m.SetPassword(t.Context(), mgrClient, token, mgrPassword("yamada"))
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "cannot read a salt") {
		t.Errorf("SetPassword() = %v, want the error of the reader as a salt", err)
	}
	e.unchanged(t, before)
	e.m.rand = rand.Reader
	if _, err := e.m.SetPassword(t.Context(), mgrClient, token, mgrPassword("yamada")); err != nil {
		t.Errorf("SetPassword() after the failure = %v, want the token to work", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := e.m.SetPassword(ctx, mgrClient, token, mgrPassword("yamada")); !errors.Is(err, context.Canceled) {
		t.Errorf("SetPassword() with a canceled context = %v, want context.Canceled", err)
	}
}

func TestSetPasswordCannotWriteTheRow(t *testing.T) {
	e := newMgr(t)
	token, hash, err := newToken(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	r := mgrRow(1, "yamada", RoleEditor)
	r.Password, r.CredentialsChangedAt = "", time.Time{}
	r.Token = tokenRow{Purpose: PurposeInvitation, Hash: hash, ExpiresAt: time.Date(5000, 1, 1, 0, 0, 0, 0, time.UTC)}
	e.set(t, r)
	before := e.mark(t)
	// The token is valid in the year -1, which the row cannot hold as the time of the credentials.
	e.clock.set(time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC))
	if _, err := e.m.SetPassword(t.Context(), mgrClient, token, mgrPassword("yamada")); err == nil || errors.Is(err, ErrInvalidToken) {
		t.Errorf("SetPassword() = %v, want an error that the row cannot be written", err)
	}
	e.unchanged(t, before)
	e.clock.set(mgrTime)
	if _, err := e.m.SetPassword(t.Context(), mgrClient, token, mgrPassword("yamada")); err != nil {
		t.Errorf("SetPassword() after the failure = %v, want the token to work", err)
	}
}

func TestPasswordOperationsCorruptRowWhileHashing(t *testing.T) {
	bad := mgrRow(7, "bad", RoleViewer).ID
	corrupt := func(t *testing.T, e *mgrEnv) {
		e.m.hook = func(point string) {
			if point != "hashed" {
				return
			}
			e.m.hook = nil
			err := e.s.Update(t.Context(), storeWrite(func(tx *store.Tx) error { return tx.PutAccount(bad, []byte("not json")) }))
			if err != nil {
				t.Error(err)
			}
		}
	}
	t.Run("SetPassword", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		_, token := e.invite(t, "yamada", RoleEditor)
		corrupt(t, e)
		if _, err := e.m.SetPassword(t.Context(), mgrClient, token, mgrPassword("yamada")); err == nil || err.Error() != invalidValue(bad) {
			t.Errorf("SetPassword() = %v, want %q", err, invalidValue(bad))
		}
	})
	t.Run("ChangePassword", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		a := e.add(t, "yamada", RoleEditor)
		corrupt(t, e)
		if err := e.m.ChangePassword(t.Context(), a.ID, mgrPassword("yamada"), "the new long password"); err == nil || err.Error() != invalidValue(bad) {
			t.Errorf("ChangePassword() = %v, want %q", err, invalidValue(bad))
		}
	})
}

// TestSetPasswordChangesWhileHashing runs another operation after the password is hashed and before it is written.
func TestSetPasswordChangesWhileHashing(t *testing.T) {
	setup := func(t *testing.T) (e *mgrEnv, a Account, token string) {
		e = newMgr(t)
		e.first(t)
		a, token = e.invite(t, "yamada", RoleEditor)
		return e, a, token
	}
	between := func(e *mgrEnv, f func()) {
		e.m.hook = func(point string) {
			if point == "hashed" {
				e.m.hook = nil
				f()
			}
		}
	}
	password := mgrPassword("yamada")

	t.Run("a reset", func(t *testing.T) {
		e, a, token := setup(t)
		var next string
		between(e, func() {
			var err error
			if next, err = e.m.Reset(t.Context(), System, a.ID); err != nil {
				t.Error(err)
			}
		})
		if _, err := e.m.SetPassword(t.Context(), mgrClient, token, password); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("SetPassword() = %v, want ErrInvalidToken", err)
		}
		if got := e.row(t, a.ID); got.Password != "" {
			t.Error("the account has a password")
		}
		if _, err := e.m.SetPassword(t.Context(), mgrClient, next, password); err != nil {
			t.Errorf("SetPassword() with the new token = %v, want nil", err)
		}
	})
	t.Run("a disabling", func(t *testing.T) {
		e, a, token := setup(t)
		between(e, func() {
			if _, err := e.m.Disable(t.Context(), System, a.ID); err != nil {
				t.Error(err)
			}
		})
		if _, err := e.m.SetPassword(t.Context(), mgrClient, token, password); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("SetPassword() = %v, want ErrInvalidToken", err)
		}
		if got := e.row(t, a.ID); got.Password != "" {
			t.Error("the account has a password")
		}
	})
	t.Run("a deletion", func(t *testing.T) {
		e, a, token := setup(t)
		between(e, func() {
			if err := e.m.Delete(t.Context(), System, a.ID); err != nil {
				t.Error(err)
			}
		})
		if _, err := e.m.SetPassword(t.Context(), mgrClient, token, password); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("SetPassword() = %v, want ErrInvalidToken", err)
		}
		if _, err := e.m.Get(t.Context(), a.ID); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Get() = %v, want fs.ErrNotExist", err)
		}
	})
	t.Run("the same token", func(t *testing.T) {
		e, a, token := setup(t)
		between(e, func() {
			if _, err := e.m.SetPassword(t.Context(), mgrClient, token, "the inner long password"); err != nil {
				t.Error(err)
			}
		})
		if _, err := e.m.SetPassword(t.Context(), mgrClient, token, password); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("SetPassword() = %v, want ErrInvalidToken", err)
		}
		if _, err := e.m.VerifyPasswordFactor(t.Context(), a.Login, "the inner long password"); err != nil {
			t.Errorf("VerifyPasswordFactor() with the first password = %v, want nil", err)
		}
		if _, err := e.m.VerifyPasswordFactor(t.Context(), a.Login, password); !errors.Is(err, ErrBadCredentials) {
			t.Errorf("VerifyPasswordFactor() with the second password = %v, want ErrBadCredentials", err)
		}
	})
	t.Run("the end of the token", func(t *testing.T) {
		e, a, token := setup(t)
		between(e, func() { e.clock.set(a.Token.ExpiresAt) })
		if _, err := e.m.SetPassword(t.Context(), mgrClient, token, password); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("SetPassword() = %v, want ErrInvalidToken", err)
		}
	})
	t.Run("an email address like the password", func(t *testing.T) {
		e, a, token := setup(t)
		same := "same.as.the.password@example.com"
		between(e, func() {
			if _, err := e.m.Update(t.Context(), System, a.ID, Change{Email: ptr(same)}); err != nil {
				t.Error(err)
			}
		})
		_, err := e.m.SetPassword(t.Context(), mgrClient, token, same)
		mgrProblems(t, err, problem{"/password", pwSame})
		if got := e.row(t, a.ID); got.Password != "" || got.Token.IsZero() {
			t.Error("the password was set or the token used up")
		}
	})
}

func TestRedeemToken(t *testing.T) {
	token, hash, err := newToken(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	second, secondHash, err := newToken(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := mgrTime
	a, b, plain := mgrRow(1, "a", RoleEditor), mgrRow(2, "b", RoleEditor), mgrRow(3, "c", RoleEditor)
	a.Token = tokenRow{Purpose: PurposeReset, Hash: hash, ExpiresAt: now.Add(time.Hour)}
	b.Token = tokenRow{Purpose: PurposeInvitation, Hash: secondHash, ExpiresAt: now.Add(time.Hour)}
	rows := []row{plain, a, b}

	i, used, err := redeemToken(rows, token, now)
	want := a
	want.Token = tokenRow{}
	if err != nil || i != 1 || used != want {
		t.Errorf("redeemToken() = %d, %v, %v, want the row 1 without its token", i, used.Login, err)
	}
	if rows[1] != a {
		t.Error("redeemToken() changed the rows")
	}
	if i, used, err := redeemToken(rows, second, now); err != nil || i != 2 || used.Login != "b" || !used.Token.IsZero() {
		t.Errorf("redeemToken() of the other token = %d, %v, %v, want the row 2", i, used.Login, err)
	}

	tests := []struct {
		name  string
		rows  []row
		token string
		now   time.Time
		ok    bool
	}{
		{"a token that is valid", rows, token, now, true},
		{"a nanosecond before the end", rows, token, a.Token.ExpiresAt.Add(-time.Nanosecond), true},
		{"the end", rows, token, a.Token.ExpiresAt, false},
		{"after the end", rows, token, a.Token.ExpiresAt.Add(time.Hour), false},
		{"another end for another token", rows, second, b.Token.ExpiresAt.Add(-time.Second), true},
		{"a token that nobody has", []row{plain, b}, token, now, false},
		{"no rows", nil, token, now, false},
		{"empty", rows, "", now, false},
		{"not a token", rows, "x", now, false},
		{"the hash", rows, hash, now, false},
	}
	for _, tt := range tests {
		_, _, err := redeemToken(tt.rows, tt.token, tt.now)
		switch {
		case tt.ok && err != nil:
			t.Errorf("redeemToken(%s) = %v, want nil", tt.name, err)
		case !tt.ok && !errors.Is(err, ErrInvalidToken):
			t.Errorf("redeemToken(%s) = %v, want ErrInvalidToken", tt.name, err)
		}
	}
	// The token of a disabled account does not work, also in a row that has both: decodeRow reads such a row.
	off := a
	off.Disabled = true
	if _, _, err := redeemToken([]row{off}, token, now); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("redeemToken() of a disabled account = %v, want ErrInvalidToken", err)
	}
}

func TestReset(t *testing.T) {
	e := newMgr(t)
	admin := e.first(t)
	editor := e.add(t, "editor", RoleEditor)
	other := e.add(t, "other", RoleViewer)
	e.clock.add(3 * time.Hour)
	now := mgrTime.Add(3 * time.Hour)
	otherRow := e.raw(t)[other.ID]
	token, err := e.m.Reset(t.Context(), By(admin.ID), editor.ID)
	if err != nil {
		t.Fatalf("Reset() = %v", err)
	}
	if hash, ok := tokenHash(token); !ok || e.row(t, editor.ID).Token.Hash != hash {
		t.Error("the token is not the one whose hash the row keeps")
	}
	want := editor
	want.Status, want.CredentialsChangedAt = StatusPending, now
	want.Token = TokenInfo{Purpose: PurposeReset, ExpiresAt: now.Add(24 * time.Hour)}
	if got, err := e.m.Get(t.Context(), editor.ID); err != nil || got != want {
		t.Errorf("Get() = %+v, %v, want %+v", got, err, want)
	}
	if r := e.row(t, editor.ID); r.Password != "" || strings.Contains(e.raw(t)[editor.ID], token) {
		t.Error("the row has a password or the token")
	}
	if _, err := e.m.VerifyPasswordFactor(t.Context(), "editor", mgrPassword("editor")); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("VerifyPasswordFactor() with the old password = %v, want ErrBadCredentials at once", err)
	}
	if e.raw(t)[other.ID] != otherRow {
		t.Error("the row of another account changed")
	}
	e.clock.add(time.Hour)
	got, err := e.m.SetPassword(t.Context(), mgrClient, token, "the new long password")
	if err != nil || got.Status != StatusActive || !got.CredentialsChangedAt.Equal(now.Add(time.Hour)) {
		t.Errorf("SetPassword() = %+v, %v, want an active account with the credentials time %v", got, err, now.Add(time.Hour))
	}
	if _, err := e.m.VerifyPasswordFactor(t.Context(), "editor", "the new long password"); err != nil {
		t.Errorf("VerifyPasswordFactor() with the new password = %v, want nil", err)
	}
}

func TestResetInvited(t *testing.T) {
	e := newMgr(t)
	admin := e.first(t)
	a, old := e.invite(t, "yamada", RoleEditor)
	e.clock.add(24 * time.Hour)
	now := mgrTime.Add(24 * time.Hour)
	token, err := e.m.Reset(t.Context(), By(admin.ID), a.ID)
	if err != nil {
		t.Fatalf("Reset() = %v", err)
	}
	want := a
	want.Token = TokenInfo{Purpose: PurposeInvitation, ExpiresAt: now.Add(7 * 24 * time.Hour)}
	if got, err := e.m.Get(t.Context(), a.ID); err != nil || got != want {
		t.Errorf("Get() = %+v, %v, want %+v: a new invitation, and the credentials time still zero", got, err, want)
	}
	if _, err := e.m.SetPassword(t.Context(), mgrClient, old, mgrPassword("yamada")); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("SetPassword() with the old token = %v, want ErrInvalidToken", err)
	}
	if got, err := e.m.SetPassword(t.Context(), mgrClient, token, mgrPassword("yamada")); err != nil || got.Status != StatusActive {
		t.Errorf("SetPassword() with the new token = %+v, %v, want an active account", got, err)
	}
}

func TestResetTwice(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	a := e.add(t, "yamada", RoleEditor)
	e.clock.add(time.Hour)
	first, err := e.m.Reset(t.Context(), System, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	resetAt := e.clock.now()
	e.clock.add(time.Hour)
	second, err := e.m.Reset(t.Context(), System, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.m.Get(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	// No credential was left to remove the second time, so the time of the credentials is still that of the first.
	want := TokenInfo{Purpose: PurposeReset, ExpiresAt: e.clock.now().Add(24 * time.Hour)}
	if got.Token != want || !got.CredentialsChangedAt.Equal(resetAt) || got.Status != StatusPending {
		t.Errorf("Get() = %+v, want the token %+v and the credentials time %v", got, want, resetAt)
	}
	if first == second {
		t.Error("the two tokens are the same")
	}
	if _, err := e.m.SetPassword(t.Context(), mgrClient, first, mgrPassword("yamada")); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("SetPassword() with the first token = %v, want ErrInvalidToken", err)
	}
	if _, err := e.m.SetPassword(t.Context(), mgrClient, second, mgrPassword("yamada")); err != nil {
		t.Errorf("SetPassword() with the second token = %v, want nil", err)
	}
}

func TestResetDisabled(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	a := e.add(t, "yamada", RoleEditor)
	bystander := e.add(t, "bystander", RoleEditor)
	if _, err := e.m.Disable(t.Context(), System, a.ID); err != nil {
		t.Fatal(err)
	}
	before := e.mark(t)
	for _, by := range []Actor{System, e.actor(t, "admin")} {
		token, err := e.m.Reset(t.Context(), by, a.ID)
		if err == nil || err.Error() != a.ID+" is disabled: enable it first" || token != "" {
			t.Errorf("Reset() = a token of %d characters, %v, want the error that %s is disabled", len(token), err, a.ID)
		}
		if !errors.Is(err, ErrDisabled) {
			t.Errorf("Reset() of an account that is disabled = %v, want an error that wraps ErrDisabled", err)
		}
	}
	// An actor that may not reset is refused before it learns that the account is disabled.
	if _, err := e.m.Reset(t.Context(), By(bystander.ID), a.ID); !errors.Is(err, ErrForbidden) || errors.Is(err, ErrDisabled) {
		t.Errorf("Reset() of an account that is disabled by an editor = %v, want a refusal that does not wrap ErrDisabled", err)
	}
	e.unchanged(t, before)
	if _, err := e.m.Enable(t.Context(), System, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Reset(t.Context(), System, a.ID); err != nil {
		t.Errorf("Reset() after Enable = %v, want nil", err)
	}
}

func (e *mgrEnv) actor(t *testing.T, login string) Actor {
	t.Helper()
	a, err := e.m.Find(t.Context(), login)
	if err != nil {
		t.Fatalf("Find(%q) = %v", login, err)
	}
	return By(a.ID)
}

func TestResetErrors(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	editor := e.add(t, "editor", RoleEditor)
	other := e.add(t, "other", RoleEditor)
	before := e.mark(t)
	_, err := e.m.Reset(t.Context(), System, mgrMissingID)
	mgrError(t, err, fs.ErrNotExist, mgrNotFound(mgrMissingID))
	_, err = e.m.Reset(t.Context(), e.actor(t, "admin"), "x")
	if err == nil || !errors.Is(err, fs.ErrNotExist) || err.Error() != mgrNotFound("x") {
		t.Errorf("Reset() of an ID that is not one = %v, want %q", err, mgrNotFound("x"))
	}
	// An actor that may not reset learns nothing about the account.
	_, err = e.m.Reset(t.Context(), By(editor.ID), mgrMissingID)
	mgrError(t, err, ErrForbidden, mgrNotAllowed(editor, "manage accounts"))
	_, err = e.m.Reset(t.Context(), By(editor.ID), other.ID)
	mgrError(t, err, ErrForbidden, mgrNotAllowed(editor, "manage accounts"))
	_, err = e.m.Reset(t.Context(), By(editor.ID), editor.ID)
	mgrError(t, err, ErrForbidden, mgrNotAllowed(editor, "manage accounts"))
	_, err = e.m.Reset(t.Context(), Actor{}, other.ID)
	mgrError(t, err, ErrForbidden, "not allowed: the operation has no actor")
	e.unchanged(t, before)
}

func TestResetLastAdmin(t *testing.T) {
	ending := "its credentials cannot be reset"
	t.Run("by itself", func(t *testing.T) {
		e := newMgr(t)
		admin := e.first(t)
		before := e.mark(t)
		token, err := e.m.Reset(t.Context(), By(admin.ID), admin.ID)
		mgrError(t, err, ErrLastAdmin, mgrLastAdmin(admin.ID, ending))
		if token != "" {
			t.Error("Reset() returned a token with an error")
		}
		e.unchanged(t, before)
		if _, err := e.m.VerifyPasswordFactor(t.Context(), "admin", mgrPassword("admin")); err != nil {
			t.Errorf("VerifyPasswordFactor() = %v, want the password to work", err)
		}
	})
	t.Run("by System, who recovers it", func(t *testing.T) {
		e := newMgr(t)
		admin := e.first(t)
		token, err := e.m.Reset(t.Context(), System, admin.ID)
		if err != nil {
			t.Fatalf("Reset() = %v", err)
		}
		if _, err := e.m.VerifyPasswordFactor(t.Context(), "admin", mgrPassword("admin")); !errors.Is(err, ErrBadCredentials) {
			t.Errorf("VerifyPasswordFactor() = %v, want ErrBadCredentials", err)
		}
		if got, err := e.m.Get(t.Context(), admin.ID); err != nil || got.Status != StatusPending || got.Token.Purpose != PurposeReset {
			t.Errorf("Get() = %+v, %v, want a pending account with a reset token", got, err)
		}
		got, err := e.m.SetPassword(t.Context(), mgrClient, token, "the recovered long password")
		if err != nil || got.Status != StatusActive || got.Role != RoleAdmin {
			t.Errorf("SetPassword() = %+v, %v, want an active administrator", got, err)
		}
	})
	t.Run("by a non-administrator", func(t *testing.T) {
		e := newMgr(t)
		admin := e.first(t)
		editor := e.add(t, "editor", RoleEditor)
		before := e.mark(t)
		_, err := e.m.Reset(t.Context(), By(editor.ID), admin.ID)
		mgrError(t, err, ErrForbidden, mgrNotAllowed(editor, "manage accounts"))
		e.unchanged(t, before)
	})
	t.Run("one of two", func(t *testing.T) {
		e := newMgr(t)
		a, b := e.first(t), e.add(t, "boss", RoleAdmin)
		if _, err := e.m.Reset(t.Context(), By(a.ID), b.ID); err != nil {
			t.Fatalf("Reset() of one of two administrators = %v, want nil", err)
		}
		// Now a is the last one, and b has no credential: it cannot act, but a reset of it is allowed.
		_, err := e.m.Reset(t.Context(), By(a.ID), a.ID)
		mgrError(t, err, ErrLastAdmin, mgrLastAdmin(a.ID, ending))
		if _, err := e.m.Reset(t.Context(), By(a.ID), b.ID); err != nil {
			t.Errorf("Reset() of an administrator that is pending = %v, want nil", err)
		}
	})
	t.Run("a pending administrator does not count", func(t *testing.T) {
		e := newMgr(t)
		a := e.first(t)
		e.invite(t, "boss", RoleAdmin)
		_, err := e.m.Reset(t.Context(), By(a.ID), a.ID)
		mgrError(t, err, ErrLastAdmin, mgrLastAdmin(a.ID, ending))
	})
	t.Run("a disabled administrator does not count", func(t *testing.T) {
		e := newMgr(t)
		a, b := e.first(t), e.add(t, "boss", RoleAdmin)
		if _, err := e.m.Disable(t.Context(), System, b.ID); err != nil {
			t.Fatal(err)
		}
		_, err := e.m.Reset(t.Context(), By(a.ID), a.ID)
		mgrError(t, err, ErrLastAdmin, mgrLastAdmin(a.ID, ending))
	})
	t.Run("a store without an active administrator", func(t *testing.T) {
		e := newMgr(t)
		a, _ := e.invite(t, "boss", RoleAdmin)
		if _, err := e.m.Reset(t.Context(), System, a.ID); err != nil {
			t.Errorf("Reset() of the only administrator, who is pending = %v, want nil", err)
		}
	})
}

func TestResetFailures(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	a := e.add(t, "yamada", RoleEditor)
	before := e.mark(t)
	boom := errors.New("no random bits")
	e.m.rand = iotest.ErrReader(boom)
	token, err := e.m.Reset(t.Context(), System, a.ID)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "cannot read a token") || token != "" {
		t.Errorf("Reset() = a token of %d characters, %v, want the error of the reader as a token", len(token), err)
	}
	e.unchanged(t, before)
	if _, err := e.m.VerifyPasswordFactor(t.Context(), "yamada", mgrPassword("yamada")); err != nil {
		t.Errorf("VerifyPasswordFactor() after the failed reset = %v, want the password to work", err)
	}
}

func TestRemoveCredentials(t *testing.T) {
	r := mgrRow(1, "a", RoleEditor)
	r.Token = tokenRow{Purpose: PurposeReset, Hash: testTokenHash(1), ExpiresAt: mgrTime.Add(time.Hour)}
	if !r.hasCredential() || !r.removeCredentials() {
		t.Error("removeCredentials() of a row with a password = false, want true")
	}
	if r.hasCredential() || r.Password != "" {
		t.Error("the row still has a credential")
	}
	if r.Token.IsZero() || r.Disabled || !r.CredentialsChangedAt.Equal(mgrTime) {
		t.Error("removeCredentials() changed more than the credentials")
	}
	if r.removeCredentials() {
		t.Error("removeCredentials() of a row without a credential = true, want false")
	}
}

func TestChangePassword(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	a := e.add(t, "yamada", RoleEditor)
	before := e.row(t, a.ID)
	others := e.raw(t)
	delete(others, a.ID)
	e.clock.add(5 * time.Hour)
	now := e.clock.now()
	if err := e.m.ChangePassword(t.Context(), a.ID, mgrPassword("yamada"), "the new long password"); err != nil {
		t.Fatalf("ChangePassword() = %v", err)
	}
	if _, err := e.m.VerifyPasswordFactor(t.Context(), "yamada", mgrPassword("yamada")); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("VerifyPasswordFactor() with the old password = %v, want ErrBadCredentials", err)
	}
	got, err := e.m.VerifyPasswordFactor(t.Context(), "yamada", "the new long password")
	want := a
	want.CredentialsChangedAt = now
	if err != nil || got != want {
		t.Errorf("VerifyPasswordFactor() with the new password = %+v, %v, want %+v", got, err, want)
	}
	after := e.row(t, a.ID)
	if after.Password == before.Password || !after.CreatedAt.Equal(before.CreatedAt) || after.Login != before.Login || !after.Token.IsZero() {
		t.Error("the row changed in more than the password and the credentials time, or the password did not change")
	}
	if n, _, _, err := parseHash(after.Password); err != nil || n != pwLow {
		t.Errorf("the new hash has %d iterations, %v, want %d", n, err, pwLow)
	}
	rest := e.raw(t)
	delete(rest, a.ID)
	if !maps.Equal(rest, others) {
		t.Error("the rows of the other accounts changed")
	}
}

func TestChangePasswordRemovesTheToken(t *testing.T) {
	e := newMgr(t)
	// A token and a password at once is a state that no operation makes now, but a credential of another kind may.
	r := mgrActiveRow(t, 1, "yamada", RoleEditor, mgrPassword("yamada"))
	r.Token = tokenRow{Purpose: PurposeInvitation, Hash: testTokenHash(1), ExpiresAt: mgrTime.Add(time.Hour)}
	e.set(t, r)
	if err := e.m.ChangePassword(t.Context(), r.ID, mgrPassword("yamada"), "the new long password"); err != nil {
		t.Fatalf("ChangePassword() = %v", err)
	}
	if got := e.row(t, r.ID); !got.Token.IsZero() {
		t.Error("the row has a token after the change, want none")
	}
}

func TestChangePasswordToTheSame(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	a := e.add(t, "yamada", RoleEditor)
	old := e.row(t, a.ID).Password
	e.clock.add(time.Hour)
	if err := e.m.ChangePassword(t.Context(), a.ID, mgrPassword("yamada"), mgrPassword("yamada")); err != nil {
		t.Fatalf("ChangePassword() = %v", err)
	}
	got := e.row(t, a.ID)
	if got.Password == old || !got.CredentialsChangedAt.Equal(e.clock.now()) {
		t.Error("the hash or the credentials time did not change")
	}
	if _, err := e.m.VerifyPasswordFactor(t.Context(), "yamada", mgrPassword("yamada")); err != nil {
		t.Errorf("VerifyPasswordFactor() = %v, want nil", err)
	}
}

func TestChangePasswordBadCredentials(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	active := e.add(t, "active", RoleEditor)
	pending, _ := e.invite(t, "pending", RoleEditor)
	off := e.add(t, "off", RoleEditor)
	if _, err := e.m.Disable(t.Context(), System, off.ID); err != nil {
		t.Fatal(err)
	}
	fresh := "the new long password"
	tests := []struct {
		name    string
		id      string
		current string
	}{
		{"a wrong password", active.ID, mgrPassword("other")},
		{"an empty password", active.ID, ""},
		{"the new password", active.ID, fresh},
		{"the password in another case", active.ID, strings.ToUpper(mgrPassword("active"))},
		{"the password with a space", active.ID, mgrPassword("active") + " "},
		{"the password of another account", active.ID, mgrPassword("off")},
		{"a pending account", pending.ID, mgrPassword("pending")},
		{"a pending account and an empty password", pending.ID, ""},
		{"a disabled account", off.ID, mgrPassword("off")},
	}
	before := e.mark(t)
	hashed := 0
	e.m.hook = func(point string) {
		if point == "hashed" {
			hashed++
		}
	}
	for _, tt := range tests {
		if err := e.m.ChangePassword(t.Context(), tt.id, tt.current, fresh); !errors.Is(err, ErrBadCredentials) {
			t.Errorf("ChangePassword() with %s = %v, want ErrBadCredentials", tt.name, err)
		}
	}
	e.unchanged(t, before)
	if hashed != 0 {
		t.Errorf("%d passwords were hashed for the calls that fail to log in, want none", hashed)
	}
	e.m.hook = nil
	err := e.m.ChangePassword(t.Context(), mgrMissingID, mgrPassword("active"), fresh)
	mgrError(t, err, fs.ErrNotExist, mgrNotFound(mgrMissingID))
	if err := e.m.ChangePassword(t.Context(), "x", mgrPassword("active"), fresh); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf(`ChangePassword() of "x" = %v, want fs.ErrNotExist`, err)
	}
}

func TestChangePasswordProblems(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	in := Input{Login: "a-long-login-name", Name: "A Long Display Name", Email: "long.email.address@example.com", Role: RoleEditor}
	_, token, err := e.m.Invite(t.Context(), System, in)
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.m.SetPassword(t.Context(), mgrClient, token, "the first long password")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct{ name, password, msg string }{
		{"short", "fourteen chars", pwShort(14)},
		{"long", strings.Repeat("ab", 128) + "c", pwLong(257)},
		{"a control character", mgrPassword("a") + "\n", pwControl},
		{"invalid UTF-8", "invalid \xff password", pwInvalidUTF8},
		{"the login name", "A-Long-Login-Name", pwSame},
		{"the email address", "long.email.address@example.com", pwSame},
		{"the name", "A Long Display Name", pwSame},
		{"one character", strings.Repeat(eAcute, 30), pwRepeated},
	}
	before := e.mark(t)
	hashed := 0
	e.m.hook = func(point string) {
		if point == "hashed" {
			hashed++
		}
	}
	for _, tt := range tests {
		err := e.m.ChangePassword(t.Context(), a.ID, "the first long password", tt.password)
		mgrProblems(t, err, problem{"/password", tt.msg})
	}
	e.unchanged(t, before)
	if hashed != 0 {
		t.Errorf("%d passwords were hashed for the passwords that break a rule, want none", hashed)
	}
	e.m.hook = nil
	// A wrong password comes before the rules.
	err = e.m.ChangePassword(t.Context(), a.ID, "a wrong long password", "short")
	if !errors.Is(err, ErrBadCredentials) {
		t.Errorf("ChangePassword() with a wrong password and a short one = %v, want ErrBadCredentials", err)
	}
}

func TestChangePasswordNormalization(t *testing.T) {
	composed := "Caf" + eAcute + " au lait, s'il vous plait"
	decomposed := "Cafe" + acute + " au lait, s'il vous plait"
	e := newMgr(t)
	e.first(t)
	_, token := e.invite(t, "yamada", RoleEditor)
	a, err := e.m.SetPassword(t.Context(), mgrClient, token, composed)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.m.ChangePassword(t.Context(), a.ID, decomposed, "Cre"+acute+"me brulee, merci beaucoup"); err != nil {
		t.Fatalf("ChangePassword() with the decomposed form of the password = %v, want nil", err)
	}
	if _, err := e.m.VerifyPasswordFactor(t.Context(), "yamada", "Cre"+acute+"me brulee, merci beaucoup"); err != nil {
		t.Errorf("VerifyPasswordFactor() with the decomposed form of the new password = %v, want nil", err)
	}
	if _, err := e.m.VerifyPasswordFactor(t.Context(), "yamada", "Cr"+eAcute+"me brulee, merci beaucoup"); err != nil {
		t.Errorf("VerifyPasswordFactor() with the composed form of the new password = %v, want nil", err)
	}
}

func TestChangePasswordFailures(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	a := e.add(t, "yamada", RoleEditor)
	before := e.mark(t)
	boom := errors.New("no random bits")
	e.m.rand = iotest.ErrReader(boom)
	err := e.m.ChangePassword(t.Context(), a.ID, mgrPassword("yamada"), "the new long password")
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "cannot read a salt") {
		t.Errorf("ChangePassword() = %v, want the error of the reader as a salt", err)
	}
	e.unchanged(t, before)
	e.m.rand = rand.Reader
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := e.m.ChangePassword(ctx, a.ID, mgrPassword("yamada"), "the new long password"); !errors.Is(err, context.Canceled) {
		t.Errorf("ChangePassword() with a canceled context = %v, want context.Canceled", err)
	}
	e.unchanged(t, before)
}

// TestChangePasswordChangesWhileHashing runs another operation after the new password is hashed and before it is
// written.
func TestChangePasswordChangesWhileHashing(t *testing.T) {
	const next = "the new long password"
	setup := func(t *testing.T) (*mgrEnv, Account) {
		e := newMgr(t)
		e.first(t)
		return e, e.add(t, "yamada", RoleEditor)
	}
	between := func(e *mgrEnv, f func()) {
		e.m.hook = func(point string) {
			if point == "hashed" {
				e.m.hook = nil
				f()
			}
		}
	}
	conflict := func(a Account) string { return "the password of " + a.ID + " changed since it was read" }

	t.Run("another change", func(t *testing.T) {
		e, a := setup(t)
		between(e, func() {
			if err := e.m.ChangePassword(t.Context(), a.ID, mgrPassword("yamada"), "the inner long password"); err != nil {
				t.Error(err)
			}
		})
		mgrError(t, e.m.ChangePassword(t.Context(), a.ID, mgrPassword("yamada"), next), ErrConflict, conflict(a))
		if _, err := e.m.VerifyPasswordFactor(t.Context(), "yamada", "the inner long password"); err != nil {
			t.Errorf("VerifyPasswordFactor() with the password of the inner change = %v, want nil", err)
		}
	})
	t.Run("a reset", func(t *testing.T) {
		e, a := setup(t)
		between(e, func() {
			if _, err := e.m.Reset(t.Context(), System, a.ID); err != nil {
				t.Error(err)
			}
		})
		mgrError(t, e.m.ChangePassword(t.Context(), a.ID, mgrPassword("yamada"), next), ErrConflict, conflict(a))
		if got := e.row(t, a.ID); got.Password != "" {
			t.Error("the account has a password after the reset")
		}
	})
	t.Run("a disabling", func(t *testing.T) {
		e, a := setup(t)
		old := e.row(t, a.ID).Password
		between(e, func() {
			if _, err := e.m.Disable(t.Context(), System, a.ID); err != nil {
				t.Error(err)
			}
		})
		if err := e.m.ChangePassword(t.Context(), a.ID, mgrPassword("yamada"), next); !errors.Is(err, ErrBadCredentials) {
			t.Errorf("ChangePassword() = %v, want ErrBadCredentials", err)
		}
		if e.row(t, a.ID).Password != old {
			t.Error("the password changed")
		}
	})
	t.Run("a deletion", func(t *testing.T) {
		e, a := setup(t)
		between(e, func() {
			if err := e.m.Delete(t.Context(), System, a.ID); err != nil {
				t.Error(err)
			}
		})
		mgrError(t, e.m.ChangePassword(t.Context(), a.ID, mgrPassword("yamada"), next), fs.ErrNotExist, mgrNotFound(a.ID))
	})
	t.Run("an email address like the password", func(t *testing.T) {
		e, a := setup(t)
		same := "same.as.the.password@example.com"
		between(e, func() {
			if _, err := e.m.Update(t.Context(), System, a.ID, Change{Email: ptr(same)}); err != nil {
				t.Error(err)
			}
		})
		mgrProblems(t, e.m.ChangePassword(t.Context(), a.ID, mgrPassword("yamada"), same), problem{"/password", pwSame})
		if _, err := e.m.VerifyPasswordFactor(t.Context(), "yamada", mgrPassword("yamada")); err != nil {
			t.Errorf("VerifyPasswordFactor() with the old password = %v, want nil", err)
		}
	})
}

func TestVerifyPasswordFactor(t *testing.T) {
	e := newMgr(t)
	admin := e.first(t)
	editor := e.add(t, "yamada.taro", RoleEditor)
	for _, tt := range []struct {
		name, login, password string
		want                  Account
	}{
		{"the first account", "admin", mgrPassword("admin"), admin},
		{"another account", "yamada.taro", mgrPassword("yamada.taro"), editor},
		{"the login name in upper case", "YAMADA.Taro", mgrPassword("yamada.taro"), editor},
		{"the login name with spaces", " \tyamada.taro\n", mgrPassword("yamada.taro"), editor},
	} {
		got, err := e.m.VerifyPasswordFactor(t.Context(), tt.login, tt.password)
		if err != nil || got != tt.want {
			t.Errorf("VerifyPasswordFactor() with %s = %+v, %v, want %+v", tt.name, got, err, tt.want)
		}
	}
}

func TestVerifyPasswordFactorFails(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	e.add(t, "active", RoleEditor)
	_, token := e.invite(t, "pending", RoleEditor)
	reset := e.add(t, "reset", RoleEditor)
	if _, err := e.m.Reset(t.Context(), System, reset.ID); err != nil {
		t.Fatal(err)
	}
	off := e.add(t, "off", RoleEditor)
	if _, err := e.m.Disable(t.Context(), System, off.ID); err != nil {
		t.Fatal(err)
	}
	var verified int
	e.m.hook = func(point string) {
		if point == "verified" {
			verified++
		}
	}
	tests := []struct{ name, login, password string }{
		{"no such login name", "nobody", mgrPassword("nobody")},
		{"the email address", "active@example.com", mgrPassword("active")},
		{"an empty login name", "", mgrPassword("active")},
		{"a wrong password", "active", mgrPassword("other")},
		{"an empty password", "active", ""},
		{"the password of another account", "active", mgrPassword("admin")},
		{"the password in upper case", "active", strings.ToUpper(mgrPassword("active"))},
		{"a pending account with the password it will have", "pending", mgrPassword("pending")},
		{"a pending account with its token", "pending", token},
		{"a pending account and an empty password", "pending", ""},
		{"an account that is reset, with the old password", "reset", mgrPassword("reset")},
		{"a disabled account with its password", "off", mgrPassword("off")},
	}
	before := e.mark(t)
	for i, tt := range tests {
		got, err := e.m.VerifyPasswordFactor(t.Context(), tt.login, tt.password)
		if !errors.Is(err, ErrBadCredentials) || got != (Account{}) {
			t.Errorf("VerifyPasswordFactor() with %s = %+v, %v, want ErrBadCredentials", tt.name, got, err)
		}
		if err != nil && err.Error() != "wrong login name or password" {
			t.Errorf("the error = %q, want one text for every failure", err)
		}
		// A hash is checked every time, so that the time does not tell the cases apart.
		if verified != i+1 {
			t.Errorf("after %s, %d hashes were checked in %d calls, want one for each", tt.name, verified, i+1)
		}
	}
	e.unchanged(t, before)
}

func TestVerifyPasswordFactorChecksAHash(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	var verified int
	e.m.hook = func(point string) {
		if point == "verified" {
			verified++
		}
	}
	// The hash for a login that no account can use is the Manager's, made with its count, and is never made again.
	made := e.m.unknownHash
	if n, _, _, err := parseHash(made); err != nil || n != pwLow {
		t.Errorf("the hash for an unknown login has %d iterations, %v, want %d", n, err, pwLow)
	}
	if _, err := e.m.VerifyPasswordFactor(t.Context(), "admin", mgrPassword("admin")); err != nil || verified != 1 {
		t.Errorf("VerifyPasswordFactor() = %v after %d checks, want nil after 1", err, verified)
	}
	if _, err := e.m.VerifyPasswordFactor(t.Context(), "nobody", mgrPassword("admin")); !errors.Is(err, ErrBadCredentials) || verified != 2 {
		t.Errorf("VerifyPasswordFactor() = %v after %d checks, want ErrBadCredentials after 2", err, verified)
	}
	for range 3 {
		if _, err := e.m.VerifyPasswordFactor(t.Context(), "nobody", mgrPassword("admin")); !errors.Is(err, ErrBadCredentials) {
			t.Fatal(err)
		}
	}
	if e.m.unknownHash != made {
		t.Error("the hash for an unknown login changed")
	}
	// The passwords that were set with an earlier count still work, whatever the count of the Manager is now.
	e.m.iterations = pwLow + 1
	if _, err := e.m.VerifyPasswordFactor(t.Context(), "admin", mgrPassword("admin")); err != nil {
		t.Errorf("VerifyPasswordFactor() after the change of the iterations = %v, want nil", err)
	}
}

// TestVerifyPasswordFactorUsesNothingUnusable makes the hash that a login with no account is checked against match a
// password, and checks that the password still logs nobody in: not as that login, and not as an account that is pending
// or disabled.
func TestVerifyPasswordFactorUsesNothingUnusable(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	e.invite(t, "pending", RoleEditor)
	off := e.add(t, "off", RoleEditor)
	if _, err := e.m.Disable(t.Context(), System, off.ID); err != nil {
		t.Fatal(err)
	}
	const guess = "the password of the hash for nobody"
	e.m.unknownHash = mgrHashed(t, guess)
	for _, login := range []string{"nobody", "pending", "off", ""} {
		got, err := e.m.VerifyPasswordFactor(t.Context(), login, guess)
		if !errors.Is(err, ErrBadCredentials) || got != (Account{}) {
			t.Errorf("VerifyPasswordFactor() of %q = %+v, %v, want ErrBadCredentials", login, got, err)
		}
	}
}

func TestVerifyPasswordFactorOtherIterations(t *testing.T) {
	e := newMgr(t)
	logins := []string{"user-a", "user-b", "user-c", "user-d"}
	for i, iterations := range []int{1, 2, 3, 50} {
		r := mgrRow(i+1, logins[i], RoleEditor)
		hash, err := hashPassword(mgrPassword(logins[i]), iterations, rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		r.Password = hash
		e.set(t, r)
	}
	before := e.mark(t)
	for _, login := range logins {
		if _, err := e.m.VerifyPasswordFactor(t.Context(), login, mgrPassword(login)); err != nil {
			t.Errorf("VerifyPasswordFactor() of %s = %v, want nil", login, err)
		}
		if _, err := e.m.VerifyPasswordFactor(t.Context(), login, mgrPassword("x"+login)); !errors.Is(err, ErrBadCredentials) {
			t.Errorf("VerifyPasswordFactor() of %s with a wrong password = %v, want ErrBadCredentials", login, err)
		}
	}
	// It does not hash again.
	e.unchanged(t, before)
}

func TestVerifyPasswordFactorFailures(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := e.m.VerifyPasswordFactor(ctx, "admin", mgrPassword("admin")); !errors.Is(err, context.Canceled) {
		t.Errorf("VerifyPasswordFactor() with a canceled context = %v, want context.Canceled", err)
	}
	// The context ends after the password matched: that is an error of the look at the account again, not a failure to
	// log in.
	ctx, cancel = context.WithCancel(t.Context())
	e.m.hook = func(point string) {
		if point == "verified" {
			cancel()
		}
	}
	got, err := e.m.VerifyPasswordFactor(ctx, "admin", mgrPassword("admin"))
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrBadCredentials) || got != (Account{}) {
		t.Errorf("VerifyPasswordFactor() with a context that ends after the match = %+v, %v, want context.Canceled", got, err)
	}
}

// TestVerifyPasswordFactorReadsNoRandomBits checks that no call makes the hash for a login that no account can use: the
// Manager has it from the start, so even the first call for such a login costs one hash and reads nothing.
func TestVerifyPasswordFactorReadsNoRandomBits(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	e.add(t, "active", RoleEditor)
	e.invite(t, "pending", RoleEditor)
	off := e.add(t, "off", RoleEditor)
	if _, err := e.m.Disable(t.Context(), System, off.ID); err != nil {
		t.Fatal(err)
	}
	counter := &mgrCountingReader{r: rand.Reader}
	e.m.rand = counter
	for _, tt := range []struct {
		login string
		works bool
	}{
		{"nobody", false}, {"nobody", false}, {"pending", false}, {"off", false}, {"active", true}, {"admin", true},
		{"nobody-else", false}, {"active", true},
	} {
		_, err := e.m.VerifyPasswordFactor(t.Context(), tt.login, mgrPassword(tt.login))
		if (err == nil) != tt.works || err != nil && !errors.Is(err, ErrBadCredentials) {
			t.Errorf("VerifyPasswordFactor() of %q = %v, want nil = %v, and else ErrBadCredentials", tt.login, err, tt.works)
		}
		if n := counter.n.Load(); n != 0 {
			t.Fatalf("VerifyPasswordFactor() of %q has read %d random bytes in all, want none", tt.login, n)
		}
	}
}

// TestVerifyPasswordFactorChecksAgain runs another operation after the password has matched and before
// VerifyPasswordFactor looks at the account again, and checks that the login goes by the account as it is then: a new
// password, a disabling and a deletion fail it, and a change of another field shows in the account that it returns.
func TestVerifyPasswordFactorChecksAgain(t *testing.T) {
	const second = "yamada has a new password now"
	setup := func(t *testing.T) (e *mgrEnv, admin, a Account) {
		e = newMgr(t)
		admin = e.first(t)
		a = e.add(t, "yamada", RoleEditor)
		return e, admin, a
	}
	// verify logs in as yamada with the right password and runs gap once, after the password matched. The login must
	// write nothing.
	verify := func(t *testing.T, e *mgrEnv, gap func()) (Account, error) {
		t.Helper()
		var ran bool
		var after mgrMark
		e.m.hook = func(point string) {
			if point == "verified" && !ran {
				ran = true
				gap()
				after = e.mark(t)
			}
		}
		got, err := e.m.VerifyPasswordFactor(t.Context(), "yamada", mgrPassword("yamada"))
		if !ran {
			t.Fatal("the password was not checked")
		}
		e.unchanged(t, after)
		return got, err
	}
	type gap func(t *testing.T, e *mgrEnv, admin, a Account)
	check := func(t *testing.T, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("the operation between the match and the second look = %v", err)
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
			_, err = e.m.SetPassword(t.Context(), mgrClient, token, second)
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
			got, err := verify(t, e, func() { tt.gap(t, e, admin, a) })
			if got != (Account{}) || !errors.Is(err, ErrBadCredentials) || err.Error() != "wrong login name or password" {
				t.Errorf("VerifyPasswordFactor() = %+v, %v, want ErrBadCredentials", got, err)
			}
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
		{"the role is changed", func(t *testing.T, e *mgrEnv, admin, a Account) {
			_, err := e.m.Update(t.Context(), By(admin.ID), a.ID, Change{Role: ptr(RoleViewer)})
			check(t, err)
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
			r := e.row(t, a.ID)
			r.CredentialsChangedAt = r.CredentialsChangedAt.Add(time.Hour)
			e.set(t, r)
		}, true},
		{"the account is disabled and enabled again", func(t *testing.T, e *mgrEnv, _, a Account) {
			_, err := e.m.Disable(t.Context(), System, a.ID)
			check(t, err)
			_, err = e.m.Enable(t.Context(), System, a.ID)
			check(t, err)
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
			got, err := verify(t, e, func() { tt.gap(t, e, admin, a) })
			if want := e.row(t, a.ID).account(); err != nil || got != want {
				t.Errorf("VerifyPasswordFactor() = %+v, %v, want the account as it is now, %+v", got, err, want)
			}
			if tt.differs && got == a {
				t.Error("VerifyPasswordFactor() returned the account as the first look found it")
			}
		})
	}
}

// TestTypedPasswordBound checks the bound on a typed password for each operation that takes one: more than 8 KiB is
// refused before it is put in NFC or hashed, and 8 KiB are judged by the other rules.
func TestTypedPasswordBound(t *testing.T) {
	over := strings.Repeat("a", maxTypedPasswordBytes+1)
	at := strings.Repeat("a", maxTypedPasswordBytes)
	tooLong := problem{"/password", pwBytes(len(over))}

	t.Run("VerifyPasswordFactor", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		before := e.mark(t)
		// The same for a login that exists and one that does not: the length is the caller's, not the account's.
		for _, login := range []string{"admin", "nobody"} {
			for _, p := range []string{over, at, over + mgrPassword("admin"), mgrPassword("admin") + over} {
				got, err := e.m.VerifyPasswordFactor(t.Context(), login, p)
				if got != (Account{}) || !errors.Is(err, ErrBadCredentials) || err.Error() != "wrong login name or password" {
					t.Errorf("VerifyPasswordFactor() of %q with %d bytes = %+v, %v, want ErrBadCredentials", login, len(p), got, err)
				}
			}
		}
		e.unchanged(t, before)
		if _, err := e.m.VerifyPasswordFactor(t.Context(), "admin", mgrPassword("admin")); err != nil {
			t.Errorf("VerifyPasswordFactor() with the right password = %v, want nil", err)
		}
		// A megabyte of combining marks is not put in NFC, which would allocate about ten times as much.
		marks := strings.Repeat(acute, 1<<19)
		var start, end runtime.MemStats
		runtime.ReadMemStats(&start)
		_, err := e.m.VerifyPasswordFactor(t.Context(), "admin", marks)
		runtime.ReadMemStats(&end)
		if allocated := end.TotalAlloc - start.TotalAlloc; !errors.Is(err, ErrBadCredentials) || allocated > 256<<10 {
			t.Errorf("VerifyPasswordFactor() with %d bytes = %v after %d bytes allocated, want ErrBadCredentials after at most 256 KiB", len(marks), err, allocated)
		}
	})
	t.Run("the current password of ChangePassword", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		a := e.add(t, "yamada", RoleEditor)
		var hashed int
		e.m.hook = func(point string) {
			if point == "hashed" {
				hashed++
			}
		}
		before := e.mark(t)
		for _, current := range []string{over, at} {
			if err := e.m.ChangePassword(t.Context(), a.ID, current, "the new long password"); !errors.Is(err, ErrBadCredentials) {
				t.Errorf("ChangePassword() with a current password of %d bytes = %v, want ErrBadCredentials", len(current), err)
			}
		}
		e.unchanged(t, before)
		if hashed != 0 {
			t.Errorf("%d passwords were hashed for the current passwords that do not work, want none", hashed)
		}
	})
	t.Run("SetPassword", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		_, token := e.invite(t, "yamada", RoleEditor)
		before := e.mark(t)
		_, err := e.m.SetPassword(t.Context(), mgrClient, token, over)
		mgrProblems(t, err, tooLong)
		_, err = e.m.SetPassword(t.Context(), mgrClient, token, at)
		mgrProblems(t, err, problem{"/password", pwLong(len(at))})
		e.unchanged(t, before)
		if _, err := e.m.SetPassword(t.Context(), mgrClient, token, mgrPassword("yamada")); err != nil {
			t.Errorf("SetPassword() after the refusals = %v, want the token to work", err)
		}
	})
	t.Run("the new password of ChangePassword", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		a := e.add(t, "yamada", RoleEditor)
		before := e.mark(t)
		err := e.m.ChangePassword(t.Context(), a.ID, mgrPassword("yamada"), over)
		mgrProblems(t, err, tooLong)
		err = e.m.ChangePassword(t.Context(), a.ID, mgrPassword("yamada"), at)
		mgrProblems(t, err, problem{"/password", pwLong(len(at))})
		e.unchanged(t, before)
		if _, err := e.m.VerifyPasswordFactor(t.Context(), "yamada", mgrPassword("yamada")); err != nil {
			t.Errorf("VerifyPasswordFactor() with the old password = %v, want nil", err)
		}
	})
	t.Run("CreateFirst", func(t *testing.T) {
		e := newMgr(t)
		_, err := e.m.CreateFirst(t.Context(), mgrInput("admin", RoleAdmin), over)
		mgrProblems(t, err, tooLong)
		if rows := e.raw(t); len(rows) != 0 {
			t.Errorf("CreateFirst() left %d rows", len(rows))
		}
		_, err = e.m.CreateFirst(t.Context(), mgrInput("admin", RoleAdmin), at)
		mgrProblems(t, err, problem{"/password", pwLong(len(at))})
	})
}
