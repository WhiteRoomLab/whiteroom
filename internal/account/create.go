package account

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// CreateFirst makes the first account of a store that has none: an active administrator with the password. in.Role is
// admin or empty. The problems of in and of the password, which is at /password, are content.Errors.
func (m *Manager) CreateFirst(ctx context.Context, in Input, password string) (Account, error) {
	in.normalize()
	notAdmin := in.Role != "" && in.Role != RoleAdmin
	in.Role = RoleAdmin
	errs := in.check()
	if notAdmin {
		at := slices.IndexFunc(errs, func(e *content.Error) bool { return e.Path == "/author" })
		if at < 0 {
			at = len(errs)
		}
		errs = slices.Insert(errs, at, &content.Error{Path: "/role", Msg: "the first account is an administrator"})
	}
	errs = addProblem(errs, "/password", checkPassword(password, in.Login, in.Email, in.Name))
	if len(errs) > 0 {
		return Account{}, errs
	}
	hash, err := hashPassword(password, m.iterations, m.rand)
	if err != nil {
		return Account{}, err
	}
	var out Account
	err = m.st.Update(ctx, func(tx *store.Tx) error {
		rows, err := loadRows(tx)
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			return errors.New("the store has accounts: CreateFirst needs a store without any")
		}
		if err := rowProblems(tx, rows, "", in.Login, in.Email, in.Author, true); err != nil {
			return err
		}
		now := m.now()
		r, err := m.newRow(rows, in, now)
		if err != nil {
			return err
		}
		r.Password, r.CredentialsChangedAt = hash, now
		if err := put(tx, rows, r, noAdminLost); err != nil {
			return err
		}
		out = r.account()
		return nil
	})
	if err != nil {
		return Account{}, err
	}
	return out, nil
}

// Invite makes a pending account for in, and returns it with a token, which is the right to set up its first
// credential within 7 days (SetPassword) and which nothing keeps but the caller. Only an administrator, or System,
// may. The problems of in are content.Errors.
func (m *Manager) Invite(ctx context.Context, by Actor, in Input) (Account, string, error) {
	in.normalize()
	var out Account
	var token string
	err := m.st.Update(ctx, func(tx *store.Tx) error {
		rows, err := loadRows(tx)
		if err != nil {
			return err
		}
		if err := authorize(rows, by, ManageAccounts, ""); err != nil {
			return err
		}
		if errs := in.check(); len(errs) > 0 {
			return errs
		}
		if err := rowProblems(tx, rows, "", in.Login, in.Email, in.Author, true); err != nil {
			return err
		}
		now := m.now()
		r, err := m.newRow(rows, in, now)
		if err != nil {
			return err
		}
		tok, hash, err := newToken(m.rand)
		if err != nil {
			return err
		}
		r.CreatedBy = by.id
		r.Token = tokenRow{Purpose: PurposeInvitation, Hash: hash, ExpiresAt: now.Add(invitationTTL)}
		if err := put(tx, rows, r, noAdminLost); err != nil {
			return err
		}
		out, token = r.account(), tok
		return nil
	})
	if err != nil {
		return Account{}, "", err
	}
	return out, token, nil
}

// newRow returns the row of a new account for in, which has no problem, with a new ID that no row has.
func (m *Manager) newRow(rows []row, in Input, now time.Time) (row, error) {
	id, err := m.ids.NewID(content.RefUser, now)
	if err != nil {
		return row{}, err
	}
	if indexOfID(rows, id) >= 0 {
		return row{}, fmt.Errorf("cannot make an ID: %s is already used", id)
	}
	return row{
		ID: id, Login: in.Login, Name: in.Name, Email: in.Email, Role: in.Role, Author: in.Author, CreatedAt: now,
	}, nil
}

// rowProblems returns the problems that need the store, as content.Errors, or another error if it cannot tell: a login
// name or an email address, which ignores the case of ASCII letters, that a row other than the account exclude has, and
// an author profile that is not a document. It checks only the values that are not "". The problem of a login name or an
// email address says which account has it only if showID is true.
func rowProblems(tx *store.Tx, rows []row, exclude, login, email, author string, showID bool) error {
	var errs content.Errors
	owner := func(id string) string {
		if showID {
			return id
		}
		return "another account"
	}
	// usedBy returns the ID of the first row other than exclude for which same is true, or "".
	usedBy := func(same func(r row) bool) string {
		for _, r := range rows {
			if r.ID != exclude && same(r) {
				return r.ID
			}
		}
		return ""
	}
	if login != "" {
		if id := usedBy(func(r row) bool { return r.Login == login }); id != "" {
			errs = addProblem(errs, "/login", fmt.Errorf("login name %q is already used by %s", login, owner(id)))
		}
	}
	if email != "" {
		if id := usedBy(func(r row) bool { return strings.EqualFold(r.Email, email) }); id != "" {
			errs = addProblem(errs, "/email", fmt.Errorf("email address %q is already used by %s", email, owner(id)))
		}
	}
	if author != "" {
		_, err := tx.Document(content.RefAuthor, author)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			errs = addProblem(errs, "/author", fmt.Errorf("author profile %q does not exist", author))
		case err != nil:
			return err
		}
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}
