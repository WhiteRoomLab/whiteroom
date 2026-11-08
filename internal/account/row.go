package account

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"strings"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// row is the value of an account in the table accounts of the store: JSON that decodeRow reads and encodeRow writes, in
// one form only. Its keys are the stored format, which changes only with a new schema of the table (logSchemaVersion of
// internal/store). A later kind of credential is a new field with omitzero, so that the rows written before it stay valid.
type row struct {
	ID                   string    `json:"id"`
	Login                string    `json:"login"`
	Name                 string    `json:"name"`
	Email                string    `json:"email"`
	Role                 Role      `json:"role"`
	Author               string    `json:"author,omitempty"`
	Disabled             bool      `json:"disabled,omitzero"`
	CreatedAt            time.Time `json:"createdAt"`
	CreatedBy            string    `json:"createdBy,omitempty"`
	Password             string    `json:"password,omitempty"` // the hash, which names its algorithm
	CredentialsChangedAt time.Time `json:"credentialsChangedAt,omitzero"`
	Token                tokenRow  `json:"token,omitzero"`
}

// tokenRow is the token of an account: what it is for, the SHA-256 of the token as tokenHash writes it, and the time
// from which it does not work.
type tokenRow struct {
	Purpose   Purpose   `json:"purpose"`
	Hash      string    `json:"hash"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (t tokenRow) IsZero() bool {
	return t.Purpose == "" && t.Hash == "" && t.ExpiresAt.IsZero()
}

// valid reports whether t is no token, or a token that has a purpose, a hash and an expiry.
func (t tokenRow) valid() bool {
	if t.IsZero() {
		return true
	}
	return (t.Purpose == PurposeInvitation || t.Purpose == PurposeReset) && isTokenHash(t.Hash) && !t.ExpiresAt.IsZero()
}

// isTokenHash reports whether s is "sha256:" and 64 lower case hexadecimal digits.
func isTokenHash(s string) bool {
	digits, ok := strings.CutPrefix(s, "sha256:")
	if !ok || len(digits) != 64 {
		return false
	}
	return strings.Trim(digits, "0123456789abcdef") == ""
}

// String and GoString keep what fmt prints of a row from showing the hashes in it.
func (r row) String() string {
	return fmt.Sprintf("account row %q", r.ID)
}

func (r row) GoString() string {
	return r.String()
}

// hasCredential reports whether the account has a credential to log in with. It is the only place that lists the kinds
// of credentials, which for now is only a password (7.3): the status and the rules that use it do not change when a
// later kind, such as a passkey, is added here.
func (r row) hasCredential() bool {
	return r.Password != ""
}

// removeCredentials removes every credential of r and reports whether it had any. Like hasCredential, it lists the
// kinds of credentials.
func (r *row) removeCredentials() bool {
	had := r.hasCredential()
	r.Password = ""
	return had
}

func (r row) status() Status {
	switch {
	case r.Disabled:
		return StatusDisabled
	case !r.hasCredential():
		return StatusPending
	}
	return StatusActive
}

func (r row) account() Account {
	return Account{
		ID: r.ID, Login: r.Login, Name: r.Name, Email: r.Email, Role: r.Role, Author: r.Author, Status: r.status(),
		CreatedAt: r.CreatedAt.UTC(), CreatedBy: r.CreatedBy, CredentialsChangedAt: r.CredentialsChangedAt.UTC(),
		Token: TokenInfo{Purpose: r.Token.Purpose, ExpiresAt: r.Token.ExpiresAt.UTC()},
	}
}

// encodeRow writes r, with its times in UTC, as decodeRow requires.
func encodeRow(r row) ([]byte, error) {
	r.CreatedAt = r.CreatedAt.UTC()
	r.CredentialsChangedAt = r.CredentialsChangedAt.UTC()
	r.Token.ExpiresAt = r.Token.ExpiresAt.UTC()
	return json.Marshal(r)
}

// decodeRow reads the row of the account id. Unknown and duplicate keys, a value that is not valid, and any form other
// than the one encodeRow writes, such as an empty value, another order of the keys or spaces, fail it. Its error shows
// nothing of data.
func decodeRow(id string, data []byte) (row, error) {
	var r row
	if err := json.Unmarshal(data, &r, json.RejectUnknownMembers(true)); err != nil || !r.check(id) {
		return row{}, invalidRow(id)
	}
	if enc, err := encodeRow(r); err != nil || !bytes.Equal(enc, data) {
		return row{}, invalidRow(id)
	}
	return r, nil
}

func invalidRow(id string) error {
	return fmt.Errorf("accounts %q: invalid value", id)
}

// check reports whether r can be the row of the account id: its ID, its fields as an Input that normalize leaves
// as it is, who made it and when, its password hash and its token.
func (r row) check(id string) bool {
	if r.ID != id || content.CheckID(content.RefUser, id) != nil {
		return false
	}
	in := Input{Login: r.Login, Name: r.Name, Email: r.Email, Role: r.Role, Author: r.Author}
	normalized := in
	normalized.normalize()
	if normalized != in || len(in.check()) > 0 {
		return false
	}
	if r.CreatedBy != "" && content.CheckID(content.RefUser, r.CreatedBy) != nil || r.CreatedAt.IsZero() {
		return false
	}
	if _, _, _, err := parseHash(r.Password); r.Password != "" && err != nil {
		return false
	}
	return r.Token.valid() && (!r.Disabled || r.Token.IsZero())
}
