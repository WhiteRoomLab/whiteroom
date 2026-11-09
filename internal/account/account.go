// Package account holds the login accounts of the admin screen and their roles (7.3), which the table accounts of
// internal/store keeps. The public profile of an author is content.AuthorProfile, which an account may link to.
package account

import "time"

type Role string

const (
	RoleAdmin  Role = "admin"
	RoleEditor Role = "editor"
	RoleAuthor Role = "author"
	RoleViewer Role = "viewer"
)

// Roles returns the roles in the order admin, editor, author, viewer.
func Roles() []Role {
	return []Role{RoleAdmin, RoleEditor, RoleAuthor, RoleViewer}
}

type Status string

const (
	// StatusActive is an enabled account with at least one credential, which for now can only be a password.
	StatusActive Status = "active"
	// StatusPending is an enabled account without any credential yet: invited, or reset.
	StatusPending Status = "pending"
	// StatusDisabled is an account that cannot log in, whatever credentials it has.
	StatusDisabled Status = "disabled"
)

// Purpose says what a token is for. Whoever holds a token may set up a credential for the account that it belongs to,
// which for now is a password and later also a passkey, by the same rules.
type Purpose string

const (
	PurposeInvitation Purpose = "invitation"
	PurposeReset      Purpose = "reset"
)

// Account is a login account as the admin screen shows it. It holds no password, no password hash and no token.
type Account struct {
	ID    string
	Login string
	Name  string
	Email string
	Role  Role
	// Author is the ID of the author profile that the account is linked to, or "".
	Author    string
	Status    Status
	CreatedAt time.Time
	// CreatedBy is the ID of the account that invited this one, or "" for the first account and for a command on the server.
	CreatedBy string
	// CredentialsChangedAt is the last time that a credential was set, changed or removed, which for now is the
	// password; it is zero if the account has never had one. A session of #46 keeps the value that VerifyPasswordFactor
	// returned and ends when the account's value differs from it; it is not compared with the time of the session.
	// Disable does not change it, so #46 removes the sessions of an account that is disabled, and enabling the account
	// again revives none.
	CredentialsChangedAt time.Time
	Token                TokenInfo
}

// TokenInfo says whether an account has a token, and when it stops working. The token itself is known only to the
// person that it was given to.
type TokenInfo struct {
	Purpose   Purpose
	ExpiresAt time.Time
}

// Input is what an invitation sets: the login name and the email address, which are unique, a name to show, the role,
// and perhaps the ID of the author profile to link to.
type Input struct {
	Login  string
	Name   string
	Email  string
	Role   Role
	Author string
}

// Change is what a change of an account sets. A nil field is left as it is; an Author that points to "" removes the
// link to the author profile.
type Change struct {
	Login  *string
	Name   *string
	Email  *string
	Role   *Role
	Author *string
}
