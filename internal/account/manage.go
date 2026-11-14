package account

import (
	"context"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// Update changes the fields of the account id that c sets, and returns the account. The name and the email address of
// one's own account need EditSelf; the login name, the role and the link to an author profile, and any field of another
// account, need ManageAccounts (or System). The role of the last active administrator cannot be changed. A change of the
// role ends the account's sessions in the same Update, so that the next login is authenticated as the new role; a change
// of the login name, the name, the email address or the link to an author profile keeps them. A Change that changes
// nothing writes no row, and its record says so. The problems of c are content.Errors; for a change that needs only
// EditSelf, the problem of an email address that another account has does not say which.
func (m *Manager) Update(ctx context.Context, by Actor, id string, c Change) (Account, error) {
	c.normalize()
	var out Account
	err := m.logged(ctx, func(tx *store.Tx, h *history) error {
		rows, err := loadRows(tx)
		if err != nil {
			return err
		}
		act := ManageAccounts
		if by.id == id && c.Login == nil && c.Role == nil && c.Author == nil {
			act = EditSelf
		}
		if err := authorize(rows, by, act, id); err != nil {
			return err
		}
		i := indexOfID(rows, id)
		if i < 0 {
			return notFound(id)
		}
		if errs := c.check(); len(errs) > 0 {
			return errs
		}
		cur := rows[i]
		next := cur
		if c.Login != nil {
			next.Login = *c.Login
		}
		if c.Name != nil {
			next.Name = *c.Name
		}
		if c.Email != nil {
			next.Email = *c.Email
		}
		if c.Role != nil {
			next.Role = *c.Role
		}
		if c.Author != nil {
			next.Author = *c.Author
		}
		out = next.account()
		if next == cur {
			return recordAccount(tx, audit.AccountUpdate, by.recorded(), id, nil)
		}
		// A link to an author profile that is deleted later is left as it is: only a new link is checked.
		var login, email, author string
		if next.Login != cur.Login {
			login = next.Login
		}
		if next.Email != cur.Email {
			email = next.Email
		}
		if next.Author != cur.Author {
			author = next.Author
		}
		if err := rowProblems(tx, rows, id, login, email, author, act != EditSelf); err != nil {
			return err
		}
		if err := put(tx, rows, next, "its role cannot be changed"); err != nil {
			return err
		}
		if next.Role != cur.Role {
			if err := removeSessions(h, id, endedRole); err != nil {
				return err
			}
		}
		return recordAccount(tx, audit.AccountUpdate, by.recorded(), id, changeDetail(cur, next))
	})
	if err != nil {
		return Account{}, err
	}
	return out, nil
}

// Disable makes the account id unable to log in. It keeps everything of the account but its token, CredentialsChangedAt
// too, so it removes the sessions of the account in the same Update (see Account.CredentialsChangedAt): enabling the
// account again revives none. The last active administrator cannot be disabled, also not by System. Only an
// administrator, or System, may.
func (m *Manager) Disable(ctx context.Context, by Actor, id string) (Account, error) {
	return m.setDisabled(ctx, by, id, true)
}

// Enable lets the account id log in again: it is active if it has a credential, and else pending. It removes no
// session. Only an administrator, or System, may.
func (m *Manager) Enable(ctx context.Context, by Actor, id string) (Account, error) {
	return m.setDisabled(ctx, by, id, false)
}

// setDisabled disables or enables the account id, and returns it as it is, with a record that it changed nothing, if it
// already is. Disabling removes the account's sessions after the checks, in the same Update.
func (m *Manager) setDisabled(ctx context.Context, by Actor, id string, disabled bool) (Account, error) {
	action := audit.AccountEnable
	if disabled {
		action = audit.AccountDisable
	}
	var out Account
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
		cur := rows[i]
		if cur.Disabled == disabled {
			out = cur.account()
			return recordAccount(tx, action, by.recorded(), id, nil)
		}
		next := cur
		next.Disabled, next.Token = disabled, tokenRow{}
		ending := noAdminLost
		if disabled {
			ending = "it cannot be disabled"
		}
		if err := put(tx, rows, next, ending); err != nil {
			return err
		}
		out = next.account()
		if disabled {
			if err := removeSessions(h, id, endedDisabled); err != nil {
				return err
			}
		}
		return recordAccount(tx, action, by.recorded(), id, changeDetail(cur, next))
	})
	if err != nil {
		return Account{}, err
	}
	return out, nil
}

// Delete removes the account id, with every credential in it, and its sessions in the same Update. Its login name and
// email address can be used again. It changes no document, so that createdBy, uploadedBy and the versions' SavedBy stay.
// The last active administrator cannot be deleted, also not by System. Only an administrator, or System, may. System can
// also remove a row that cannot be read, however many there are, one at a time and without reading the others, which is
// how whiteroom account delete clears them; the rule of the last active administrator cannot count such a row, and the
// sessions that name it go too, and the record says that the row could not be read, and tells nothing else of it. Any
// other removal needs every row to be readable, as every operation that writes does.
func (m *Manager) Delete(ctx context.Context, by Actor, id string) error {
	return m.logged(ctx, func(tx *store.Tx, h *history) error {
		if by.system {
			removed, err := removeUnreadable(tx, id)
			if removed {
				if err = removeSessions(h, id, endedDeleted); err == nil {
					err = recordAccount(tx, audit.AccountDelete, audit.System, id, map[string]string{"unreadable": "true"})
				}
			}
			if removed || err != nil {
				return err
			}
		}
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
		if err := remove(tx, rows, id, "it cannot be deleted"); err != nil {
			return err
		}
		if err := removeSessions(h, id, endedDeleted); err != nil {
			return err
		}
		return recordAccount(tx, audit.AccountDelete, by.recorded(), id, deletedDetail(rows[i]))
	})
}
