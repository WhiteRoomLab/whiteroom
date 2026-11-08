package account

import (
	"context"

	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// Update changes the fields of the account id that c sets, and returns the account. The name and the email address of
// one's own account need EditSelf; the login name, the role and the link to an author profile, and any field of another
// account, need ManageAccounts (or System). The role of the last active administrator cannot be changed. A Change that
// changes nothing writes nothing. The problems of c are content.Errors.
func (m *Manager) Update(ctx context.Context, by Actor, id string, c Change) (Account, error) {
	c.normalize()
	var out Account
	err := m.st.Update(ctx, func(tx *store.Tx) error {
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
			return nil
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
		if err := rowProblems(tx, rows, id, login, email, author); err != nil {
			return err
		}
		return put(tx, rows, next, "its role cannot be changed")
	})
	if err != nil {
		return Account{}, err
	}
	return out, nil
}

// Disable makes the account id unable to log in. It keeps everything of the account but its token. The last active
// administrator cannot be disabled, also not by System. Only an administrator, or System, may.
func (m *Manager) Disable(ctx context.Context, by Actor, id string) (Account, error) {
	return m.setDisabled(ctx, by, id, true)
}

// Enable lets the account id log in again: it is active if it has a credential, and else pending. Only an
// administrator, or System, may.
func (m *Manager) Enable(ctx context.Context, by Actor, id string) (Account, error) {
	return m.setDisabled(ctx, by, id, false)
}

// setDisabled disables or enables the account id, and returns it as it is if it already is.
func (m *Manager) setDisabled(ctx context.Context, by Actor, id string, disabled bool) (Account, error) {
	var out Account
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
		if next.Disabled == disabled {
			out = next.account()
			return nil
		}
		next.Disabled, next.Token = disabled, tokenRow{}
		ending := noAdminLost
		if disabled {
			ending = "it cannot be disabled"
		}
		if err := put(tx, rows, next, ending); err != nil {
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

// Delete removes the account id, with every credential in it. Its login name and email address can be used again. It
// changes no document, so that createdBy, uploadedBy and the versions' SavedBy stay. The last active administrator
// cannot be deleted, also not by System. Only an administrator, or System, may.
func (m *Manager) Delete(ctx context.Context, by Actor, id string) error {
	return m.st.Update(ctx, func(tx *store.Tx) error {
		rows, err := loadRows(tx)
		if err != nil {
			return err
		}
		if err := authorize(rows, by, ManageAccounts, id); err != nil {
			return err
		}
		if indexOfID(rows, id) < 0 {
			return notFound(id)
		}
		return remove(tx, rows, id, "it cannot be deleted")
	})
}
