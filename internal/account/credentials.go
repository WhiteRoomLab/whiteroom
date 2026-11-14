package account

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// passwordErrors returns the problem of the password p for the account r as content.Errors at /password, or nil.
func passwordErrors(p string, r row) error {
	if err := checkPassword(p, r.Login, r.Email, r.Name); err != nil {
		return content.Errors{{Path: "/password", Msg: err.Error()}}
	}
	return nil
}

// CheckPassword returns the problems of p as the password of a, as content.Errors at /password, or nil: those that
// SetPassword finds, for a caller that wants to know before it changes anything.
func CheckPassword(p string, a Account) error {
	return passwordErrors(p, row{Login: a.Login, Email: a.Email, Name: a.Name})
}

// SetPassword uses the token of an invitation or a reset to set the password of the account that it was given to, and
// returns the account, which is then active. The token cannot be used again, and the account's sessions end in the same
// Update. The problems of the password are content.Errors at /password, and leave the token as it was. A token that
// cannot be used is ErrInvalidToken, and the login history records it with c, in an Update of its own (see
// LoginRefused).
func (m *Manager) SetPassword(ctx context.Context, c Client, token, password string) (Account, error) {
	out, err := m.setPassword(ctx, token, password)
	var bad invalidToken
	if errors.As(err, &bad) {
		m.tokenFailed(ctx, c, bad)
	}
	return out, err
}

func (m *Manager) setPassword(ctx context.Context, token, password string) (Account, error) {
	var target row
	err := m.st.View(ctx, func(tx *store.Tx) error {
		rows, err := loadRows(tx)
		if err != nil {
			return err
		}
		i, _, err := redeemToken(rows, token, m.now())
		if err != nil {
			return err
		}
		target = rows[i]
		return nil
	})
	if err != nil {
		return Account{}, err
	}
	if err := passwordErrors(password, target); err != nil {
		return Account{}, err
	}
	hash, err := m.newHash(ctx, password)
	if err != nil {
		return Account{}, err
	}
	m.at("hashed")
	var out Account
	err = m.logged(ctx, func(tx *store.Tx, h *history) error {
		rows, err := loadRows(tx)
		if err != nil {
			return err
		}
		now := m.now()
		i, next, err := redeemToken(rows, token, now)
		if err != nil {
			return err
		}
		if err := passwordErrors(password, next); err != nil {
			return err
		}
		next.Password, next.CredentialsChangedAt = hash, now
		if err := put(tx, rows, next, noAdminLost); err != nil {
			return err
		}
		if err := removeSessions(h, next.ID, endedPassword); err != nil {
			return err
		}
		if err := recordAccount(tx, audit.AccountSetPassword, next.ID, next.ID, changeDetail(rows[i], next)); err != nil {
			return err
		}
		out = next.account()
		return nil
	})
	if err != nil {
		return Account{}, err
	}
	return out, nil
}

// Reset removes every credential of the account id at once, which is today its password, and returns a token for
// setting up a new one with SetPassword: valid for 24 hours, or for 7 days if the account has never had a credential.
// It replaces an older token, and ends the account's sessions in the same Update. The account must not be disabled: the
// error wraps ErrDisabled. Only an administrator, or System, may; the last active administrator can be reset by System only.
func (m *Manager) Reset(ctx context.Context, by Actor, id string) (string, error) {
	var token string
	err := m.logged(ctx, func(tx *store.Tx, h *history) error {
		rows, err := loadRows(tx)
		if err != nil {
			return err
		}
		if err := authorize(rows, by, ManageAccounts, id); err != nil {
			return err
		}
		i := indexOfID(rows, id)
		if i < 0 {
			return notFound(id)
		}
		next := rows[i]
		if next.Disabled {
			return disabledError(fmt.Sprintf("%s is disabled: enable it first", id))
		}
		now := m.now()
		if next.removeCredentials() {
			next.CredentialsChangedAt = now
		}
		purpose, ttl := PurposeReset, resetTTL
		if next.CredentialsChangedAt.IsZero() {
			purpose, ttl = PurposeInvitation, invitationTTL
		}
		tok, hash, err := newToken(m.rand)
		if err != nil {
			return err
		}
		next.Token = tokenRow{Purpose: purpose, Hash: hash, ExpiresAt: now.Add(ttl)}
		if by.system {
			err = putRow(tx, next)
		} else {
			err = put(tx, rows, next, "its credentials cannot be reset")
		}
		if err != nil {
			return err
		}
		if err := removeSessions(h, id, endedPassword); err != nil {
			return err
		}
		detail := changeDetail(rows[i], next)
		addToken(detail, next.Token)
		if err := recordAccount(tx, audit.AccountReset, by.recorded(), id, detail); err != nil {
			return err
		}
		token = tok
		return nil
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// ChangePassword changes the password of the account id to next if current is its password. The account must be active
// and have a password; if it does not, or current is wrong, the error is ErrBadCredentials. The problems of next are
// content.Errors at /password. If the password changed after current was checked, the error wraps ErrConflict. Every
// session of the account ends in the Update that writes the password, whoever asked: ChangeOwnPassword is the change
// that goes on with a new session.
func (m *Manager) ChangePassword(ctx context.Context, id, current, next string) error {
	stored, ok, err := m.storedRow(ctx, id)
	switch {
	case err != nil:
		return err
	case !ok:
		return notFound(id)
	}
	hash, err := m.hashNewPassword(ctx, stored, current, next)
	if err != nil {
		return err
	}
	return m.logged(ctx, func(tx *store.Tx, h *history) error {
		rows, err := loadRows(tx)
		if err != nil {
			return err
		}
		before, after, err := rewritePassword(tx, rows, id, stored, next, hash, m.now())
		if err != nil {
			return err
		}
		if err := removeSessions(h, id, endedPassword); err != nil {
			return err
		}
		return recordAccount(tx, audit.AccountChangePassword, id, id, changeDetail(before, after))
	})
}

// storedRow reads the row of the account id with those of every other account, as an operation that writes does, and
// reports whether there is one.
func (m *Manager) storedRow(ctx context.Context, id string) (stored row, ok bool, err error) {
	err = m.st.View(ctx, func(tx *store.Tx) error {
		rows, err := loadRows(tx)
		if err != nil {
			return err
		}
		if i := indexOfID(rows, id); i >= 0 {
			stored, ok = rows[i], true
		}
		return nil
	})
	return stored, ok, err
}

// hashNewPassword is the part of ChangePassword and ChangeOwnPassword before the Update: it checks that the account
// stored can log in with a password, that current is that password and that next is good for it, and returns the hash of
// next, which it makes outside the Update because it takes long. Both hashes wait for their places (see gated).
func (m *Manager) hashNewPassword(ctx context.Context, stored row, current, next string) (string, error) {
	if stored.status() != StatusActive || stored.Password == "" {
		return "", ErrBadCredentials
	}
	ok, _, err := m.match(ctx, stored.Password, current)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrBadCredentials
	}
	if err := passwordErrors(next, stored); err != nil {
		return "", err
	}
	hash, err := m.newHash(ctx, next)
	if err != nil {
		return "", err
	}
	m.at("hashed")
	return hash, nil
}

// rewritePassword is the part of the Update of ChangePassword and ChangeOwnPassword that needs the rows: it looks at the
// account id again, as stored was found before the hash was made, and writes the hash with now as the time of the
// change, without a token, and returns the row as it was and the row it wrote. The password must be the one that was
// checked, and the account still active.
func rewritePassword(tx *store.Tx, rows []row, id string, stored row, next, hash string, now time.Time) (row, row, error) {
	i := indexOfID(rows, id)
	if i < 0 {
		return row{}, row{}, notFound(id)
	}
	before := rows[i]
	switch {
	case before.Password != stored.Password:
		return row{}, row{}, fmt.Errorf("the password of %s %w", id, ErrConflict)
	case before.status() != StatusActive:
		return row{}, row{}, ErrBadCredentials
	}
	if err := passwordErrors(next, before); err != nil {
		return row{}, row{}, err
	}
	after := before
	after.Password, after.CredentialsChangedAt, after.Token = hash, now, tokenRow{}
	if err := put(tx, rows, after, noAdminLost); err != nil {
		return row{}, row{}, err
	}
	return before, after, nil
}

// VerifyPasswordFactor checks the password of the account with the login name, which it normalizes: one factor of a
// login, which signs nobody in (Login does, with the session). After the password matches it reads the account again,
// since a change of the password or a disabling may have committed meanwhile: it succeeds only if the account still has
// the password that matched and is still active, and returns the account as it is then. Every reason for a login to fail
// is ErrBadCredentials; only an error of the store, or the end of ctx, is another. Every call costs one hash of the
// Manager's count, which waits for its place among the hashes that run (see gated): when no active account with a
// password has the login name it checks a hash that no password matches, so that the time does not tell the cases
// apart. Only a password of more than maxTypedPasswordBytes bytes costs none, for every login alike. A row that cannot be
// read is no account, and does not stop the others from logging in. It does not limit attempts and does not hash again.
func (m *Manager) VerifyPasswordFactor(ctx context.Context, login, password string) (Account, error) {
	found, ok, _, _, err := m.verify(ctx, login, password)
	if err != nil {
		return Account{}, err
	}
	if !ok {
		return Account{}, ErrBadCredentials
	}
	var out Account
	var kept bool
	err = m.st.View(ctx, func(tx *store.Tx) error {
		r, ok, err := stillMatches(tx, found)
		if err == nil && ok {
			out, kept = r.account(), true
		}
		return err
	})
	if err != nil {
		return Account{}, err
	}
	if !kept {
		return Account{}, ErrBadCredentials
	}
	return out, nil
}

// verify is the first part of VerifyPasswordFactor and of Login: it finds the row of the account with the login name,
// which it normalizes, and checks the password against its hash, or against the hash for nobody if no active account
// with a password has the login name. ok is true only for an active account with a password that matches; found is then
// its row as the look found it, and rehash says that its hash has fewer iterations than the Manager's. The reasons to
// fail are not told apart to the caller of the Manager: only an error of the store, or the end of ctx, is an error. why
// is what the login history records of a failure.
func (m *Manager) verify(ctx context.Context, login, password string) (found row, ok, rehash bool, why loginFailure, err error) {
	login = NormalizeLogin(login)
	var usable bool
	err = m.st.View(ctx, func(tx *store.Tx) error {
		rows, err := readableRows(tx)
		if err != nil {
			return err
		}
		if i := slices.IndexFunc(rows, func(r row) bool { return r.Login == login }); i >= 0 {
			found = rows[i]
			usable = found.status() == StatusActive && found.Password != ""
		}
		return nil
	})
	if err != nil {
		return row{}, false, false, loginFailure{}, err
	}
	stored := m.unknownHash
	if usable {
		stored = found.Password
	}
	matched, rehash, err := m.match(ctx, stored, password)
	m.at("verified")
	if err != nil {
		return row{}, false, false, loginFailure{}, err
	}
	if !usable || !matched {
		return row{}, false, false, whyNotLoggedIn(found, password), nil
	}
	return found, true, rehash, loginFailure{}, nil
}

// loginFailure is why a login failed, and the account if some account has the login name.
type loginFailure struct{ result, account string }

// whyNotLoggedIn tells why a password that does not log in as the account found, which is the zero row if none has the
// login name, does not. A password that is too long is refused whatever the account is.
func whyNotLoggedIn(found row, password string) loginFailure {
	why := loginFailure{account: found.ID}
	switch {
	case PasswordTooLong(password):
		why.result = failTooLong
	case found.ID == "":
		why.result = failUnknownLogin
	case found.status() == StatusDisabled:
		why.result = failDisabled
	case found.status() == StatusPending:
		why.result = failPending
	default:
		why.result = failWrongPassword
	}
	return why
}

// stillMatches reads the row of the account that verify found again, and returns it if it is still active and has the
// password hash that matched. A row that is gone or cannot be read, and any other change of the password or the status,
// is not a match. Of an error of the store it returns the error.
func stillMatches(tx *store.Tx, found row) (row, bool, error) {
	r, ok, err := readAccount(tx, found.ID)
	if err != nil || !ok || r.Password != found.Password || r.status() != StatusActive {
		return row{}, false, err
	}
	return r, true, nil
}
