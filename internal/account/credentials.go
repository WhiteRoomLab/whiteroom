package account

import (
	"context"
	"fmt"
	"slices"

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

// SetPassword uses the token of an invitation or a reset to set the password of the account that it was given to, and
// returns the account, which is then active. The token cannot be used again. The problems of the password are
// content.Errors at /password, and leave the token as it was.
func (m *Manager) SetPassword(ctx context.Context, token, password string) (Account, error) {
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
	hash, err := hashPassword(password, m.iterations, m.rand)
	if err != nil {
		return Account{}, err
	}
	m.at("hashed")
	var out Account
	err = m.st.Update(ctx, func(tx *store.Tx) error {
		rows, err := loadRows(tx)
		if err != nil {
			return err
		}
		now := m.now()
		_, next, err := redeemToken(rows, token, now)
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
// It replaces an older token. The account must not be disabled. Only an administrator, or System, may; the last
// active administrator can be reset by System only.
func (m *Manager) Reset(ctx context.Context, by Actor, id string) (string, error) {
	var token string
	err := m.st.Update(ctx, func(tx *store.Tx) error {
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
			return fmt.Errorf("%s is disabled: enable it first", id)
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
// content.Errors at /password. If the password changed after current was checked, the error wraps ErrConflict.
func (m *Manager) ChangePassword(ctx context.Context, id, current, next string) error {
	var stored row
	err := m.st.View(ctx, func(tx *store.Tx) error {
		rows, err := loadRows(tx)
		if err != nil {
			return err
		}
		i := indexOfID(rows, id)
		if i < 0 {
			return notFound(id)
		}
		stored = rows[i]
		return nil
	})
	if err != nil {
		return err
	}
	if stored.status() != StatusActive || stored.Password == "" {
		return ErrBadCredentials
	}
	ok, _, err := matchHash(stored.Password, current, m.iterations)
	if err != nil {
		return err
	}
	if !ok {
		return ErrBadCredentials
	}
	if err := passwordErrors(next, stored); err != nil {
		return err
	}
	hash, err := hashPassword(next, m.iterations, m.rand)
	if err != nil {
		return err
	}
	m.at("hashed")
	return m.st.Update(ctx, func(tx *store.Tx) error {
		rows, err := loadRows(tx)
		if err != nil {
			return err
		}
		i := indexOfID(rows, id)
		if i < 0 {
			return notFound(id)
		}
		changed := rows[i]
		switch {
		case changed.Password != stored.Password:
			return fmt.Errorf("the password of %s %w", id, ErrConflict)
		case changed.status() != StatusActive:
			return ErrBadCredentials
		}
		if err := passwordErrors(next, changed); err != nil {
			return err
		}
		changed.Password, changed.CredentialsChangedAt, changed.Token = hash, m.now(), tokenRow{}
		return put(tx, rows, changed, noAdminLost)
	})
}

// VerifyPasswordFactor checks the password of the account with the login name, which it normalizes: one factor of a
// login, which signs nobody in. #46 combines it with the other factors that the account has (none yet), the limits of
// attempts and the session. Every failure is ErrBadCredentials. When no active account with a password has the login
// name it still checks a hash that no password matches, so that the time does not tell the cases apart. It does not
// limit attempts and does not hash again (#46).
func (m *Manager) VerifyPasswordFactor(ctx context.Context, login, password string) (Account, error) {
	login = normalizeLogin(login)
	var found row
	var usable bool
	err := m.st.View(ctx, func(tx *store.Tx) error {
		rows, err := loadRows(tx)
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
		return Account{}, err
	}
	stored := found.Password
	if !usable {
		if stored, err = m.unknownPasswordHash(); err != nil {
			return Account{}, err
		}
	}
	ok, _, err := matchHash(stored, password, m.iterations)
	m.at("verified")
	if err != nil {
		return Account{}, err
	}
	if !usable || !ok {
		return Account{}, ErrBadCredentials
	}
	return found.account(), nil
}

// unknownPasswordHash returns a hash of a text that nobody knows, made with the iterations of m.
func (m *Manager) unknownPasswordHash() (string, error) {
	m.unknownMu.Lock()
	defer m.unknownMu.Unlock()
	if m.unknownHash == "" || m.unknownIterations != m.iterations {
		secret, _, err := newToken(m.rand)
		if err != nil {
			return "", err
		}
		hash, err := hashPassword(secret, m.iterations, m.rand)
		if err != nil {
			return "", err
		}
		m.unknownHash, m.unknownIterations = hash, m.iterations
	}
	return m.unknownHash, nil
}
