package account

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	testUserID    = "usr_01JB8T7H4MA2C5E8G1J4K7N3P6"
	testCreatorID = "usr_01JB8T7H4MA2C5E8G1J4K7N3P7"
	testAuthorID  = "aut_01JB8Y6T2RG7M4XK9P2D5W8NQ3"
)

// testTime has a fraction of a second, which a row keeps.
var testTime = time.Date(2026, 11, 8, 1, 2, 3, 456789000, time.UTC)

// testPasswordHash returns a hash in the form of the hashes of passwords whose salt and key are made of the byte b, so
// that the tests can look for it in text.
func testPasswordHash(b byte) string {
	enc := base64.RawStdEncoding
	return "$pbkdf2-sha256$i=600000$" + enc.EncodeToString(bytes.Repeat([]byte{b}, 16)) + "$" + enc.EncodeToString(bytes.Repeat([]byte{b}, 32))
}

func testTokenHash(b byte) string {
	return "sha256:" + hex.EncodeToString(bytes.Repeat([]byte{b}, 32))
}

// testRow returns the row of an active editor that has every optional field but disabled: a link to an author profile,
// a creator, a password, the time of the last change of the credentials and a token.
func testRow() row {
	return row{
		ID: testUserID, Login: "yamada", Name: "山田 太郎", Email: "yamada@example.com", Role: RoleEditor, Author: testAuthorID,
		CreatedAt: testTime, CreatedBy: testCreatorID, Password: testPasswordHash(0xab),
		CredentialsChangedAt: testTime.Add(10 * time.Hour),
		Token:                tokenRow{Purpose: PurposeReset, Hash: testTokenHash(0xcd), ExpiresAt: testTime.Add(34 * time.Hour)},
	}
}

// testMinimalRow returns the row of a pending viewer that has only the required fields.
func testMinimalRow() row {
	return row{ID: testUserID, Login: "a", Name: "A", Email: "a@example.com", Role: RoleViewer, CreatedAt: testTime}
}

func ptr[T any](v T) *T {
	return &v
}

func TestRoles(t *testing.T) {
	want := []Role{"admin", "editor", "author", "viewer"}
	got := Roles()
	if !slices.Equal(got, want) {
		t.Errorf("Roles() = %v, want %v", got, want)
	}
	if RoleAdmin != "admin" || RoleEditor != "editor" || RoleAuthor != "author" || RoleViewer != "viewer" {
		t.Errorf("the roles are %q, %q, %q and %q", RoleAdmin, RoleEditor, RoleAuthor, RoleViewer)
	}
	// Each call returns a slice of its own.
	got[0] = "changed"
	if again := Roles(); !slices.Equal(again, want) {
		t.Errorf("Roles() after a change of an earlier result = %v, want %v", again, want)
	}
}

func TestConstants(t *testing.T) {
	if StatusActive != "active" || StatusPending != "pending" || StatusDisabled != "disabled" {
		t.Errorf("the statuses are %q, %q and %q", StatusActive, StatusPending, StatusDisabled)
	}
	if PurposeInvitation != "invitation" || PurposeReset != "reset" {
		t.Errorf("the purposes are %q and %q", PurposeInvitation, PurposeReset)
	}
}

func TestRowAccount(t *testing.T) {
	other := time.FixedZone("JST", 9*3600)
	tests := []struct {
		name   string
		modify func(*row)
		status Status
	}{
		{"pending without a credential", func(r *row) {}, StatusPending},
		{"pending with an invitation", func(r *row) {
			r.Token = tokenRow{Purpose: PurposeInvitation, Hash: testTokenHash(1), ExpiresAt: testTime.Add(7 * 24 * time.Hour)}
		}, StatusPending},
		{"pending with a reset after a password", func(r *row) {
			r.CredentialsChangedAt = testTime.Add(time.Hour)
			r.Token = tokenRow{Purpose: PurposeReset, Hash: testTokenHash(2), ExpiresAt: testTime.Add(25 * time.Hour)}
		}, StatusPending},
		{"active with a password", func(r *row) {
			r.Password = testPasswordHash(1)
			r.CredentialsChangedAt = testTime.Add(time.Hour)
		}, StatusActive},
		{"active with a password and a token", func(r *row) {
			r.Password = testPasswordHash(1)
			r.Token = tokenRow{Purpose: PurposeInvitation, Hash: testTokenHash(3), ExpiresAt: testTime.Add(time.Hour)}
		}, StatusActive},
		{"disabled with a password", func(r *row) {
			r.Password = testPasswordHash(1)
			r.Disabled = true
		}, StatusDisabled},
		{"disabled without a credential", func(r *row) { r.Disabled = true }, StatusDisabled},
		{"times in another zone", func(r *row) {
			r.CreatedAt = r.CreatedAt.In(other)
			r.CredentialsChangedAt = testTime.Add(time.Hour).In(other)
			r.Token = tokenRow{Purpose: PurposeReset, Hash: testTokenHash(4), ExpiresAt: testTime.Add(2 * time.Hour).In(other)}
		}, StatusPending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := testMinimalRow()
			r.Author, r.CreatedBy = testAuthorID, testCreatorID
			tt.modify(&r)
			a := r.account()
			want := Account{
				ID: r.ID, Login: "a", Name: "A", Email: "a@example.com", Role: RoleViewer, Author: testAuthorID, Status: tt.status,
				CreatedAt: testTime, CreatedBy: testCreatorID, CredentialsChangedAt: r.CredentialsChangedAt.UTC(),
				Token: TokenInfo{Purpose: r.Token.Purpose, ExpiresAt: r.Token.ExpiresAt.UTC()},
			}
			if a != want {
				t.Errorf("account() = %+v, want %+v", a, want)
			}
			// Times are in UTC whatever zone the row has, so that two Accounts compare as they are read from the store.
			for name, tm := range map[string]time.Time{"CreatedAt": a.CreatedAt, "CredentialsChangedAt": a.CredentialsChangedAt, "ExpiresAt": a.Token.ExpiresAt} {
				if tm.Location() != time.UTC {
					t.Errorf("%s is in %v, want UTC", name, tm.Location())
				}
			}
		})
	}
}

func TestRowAccountWithoutToken(t *testing.T) {
	a := testMinimalRow().account()
	if a.Token != (TokenInfo{}) || !a.CredentialsChangedAt.IsZero() || a.Author != "" || a.CreatedBy != "" {
		t.Errorf("account() of a minimal row = %+v, want no token, no credential time, no author and no creator", a)
	}
}

func TestHasCredential(t *testing.T) {
	r := testMinimalRow()
	if r.hasCredential() {
		t.Error("a row without a password has a credential")
	}
	r.Token = tokenRow{Purpose: PurposeInvitation, Hash: testTokenHash(1), ExpiresAt: testTime.Add(time.Hour)}
	r.CredentialsChangedAt = testTime
	if r.hasCredential() {
		t.Error("a row with a token and a credential time but no password has a credential")
	}
	r.Password = testPasswordHash(1)
	if !r.hasCredential() {
		t.Error("a row with a password has no credential")
	}
	r.Disabled = true
	if !r.hasCredential() {
		t.Error("a disabled row with a password has no credential: the status is not the credential")
	}
}

// TestAccountFields guards the public Account: it holds no password, no hash and no token, only the facts below.
func TestAccountFields(t *testing.T) {
	fields := func(v any) []string {
		var names []string
		for f := range reflect.TypeOf(v).Fields() {
			names = append(names, f.Name+" "+f.Type.String())
		}
		return names
	}
	wantAccount := []string{
		"ID string", "Login string", "Name string", "Email string", "Role account.Role", "Author string", "Status account.Status",
		"CreatedAt time.Time", "CreatedBy string", "CredentialsChangedAt time.Time", "Token account.TokenInfo",
	}
	if got := fields(Account{}); !slices.Equal(got, wantAccount) {
		t.Errorf("the fields of Account = %v, want %v", got, wantAccount)
	}
	wantToken := []string{"Purpose account.Purpose", "ExpiresAt time.Time"}
	if got := fields(TokenInfo{}); !slices.Equal(got, wantToken) {
		t.Errorf("the fields of TokenInfo = %v, want %v", got, wantToken)
	}
}

func TestPrintedAccountHasNoSecrets(t *testing.T) {
	r := testRow()
	a := r.account()
	secrets := map[string]string{
		"the hash of the password": r.Password, "the salt of the hash": strings.Split(r.Password, "$")[3],
		"the key of the hash": strings.Split(r.Password, "$")[4], "the hash of the token": r.Token.Hash,
		"the digits of the hash of the token": strings.TrimPrefix(r.Token.Hash, "sha256:"),
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		for what, v := range map[string]any{"Account": a, "*Account": &a, "[]Account": []Account{a}, "TokenInfo": a.Token} {
			out := fmt.Sprintf(verb, v)
			for name, secret := range secrets {
				if strings.Contains(out, secret) {
					t.Errorf("%s of a %s contains %s", verb, what, name)
				}
			}
		}
	}
	// The row hides its hashes too, for a failure message that prints one.
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		for what, v := range map[string]any{"row": r, "*row": &r, "[]row": []row{r}, "a struct with a row": struct{ R row }{r}} {
			out := fmt.Sprintf(verb, v)
			for name, secret := range secrets {
				if strings.Contains(out, secret) {
					t.Errorf("%s of a %s contains %s", verb, what, name)
				}
			}
			if !strings.Contains(out, testUserID) {
				t.Errorf("%s of a %s = %q, want the ID of the account", verb, what, out)
			}
		}
	}
}
