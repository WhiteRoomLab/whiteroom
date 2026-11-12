package account

import (
	"context"
	"fmt"
	"slices"

	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// The reads check no actor: what a caller may read is the caller's to decide (the admin API does, 9.7).

// Get returns the account id. If there is none, the error wraps fs.ErrNotExist.
func (m *Manager) Get(ctx context.Context, id string) (Account, error) {
	var a Account
	err := m.st.View(ctx, func(tx *store.Tx) error {
		var err error
		a, err = Read(tx, id)
		return err
	})
	if err != nil {
		return Account{}, err
	}
	return a, nil
}

// List returns every account, in the order of the IDs. It fails while a row cannot be read.
func (m *Manager) List(ctx context.Context) ([]Account, error) {
	var accounts []Account
	err := m.st.View(ctx, func(tx *store.Tx) error {
		rows, err := loadRows(tx)
		if err != nil {
			return err
		}
		for _, r := range rows {
			accounts = append(accounts, r.account())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return accounts, nil
}

// Find returns the account whose login name is login, which it normalizes as an invitation does. If there is none, the
// error wraps fs.ErrNotExist; a row that cannot be read is not found.
func (m *Manager) Find(ctx context.Context, login string) (Account, error) {
	login = NormalizeLogin(login)
	var a Account
	err := m.st.View(ctx, func(tx *store.Tx) error {
		rows, err := readableRows(tx)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(rows, func(r row) bool { return r.Login == login })
		if i < 0 {
			return missingError(fmt.Sprintf("no account has the login name %q", login))
		}
		a = rows[i].account()
		return nil
	})
	if err != nil {
		return Account{}, err
	}
	return a, nil
}

// Read returns the account id as the transaction tx sees it, for a caller that checks an account inside its own Update
// (the admin API does, 9.7). If there is none, the error wraps fs.ErrNotExist.
func Read(tx *store.Tx, id string) (Account, error) {
	data, ok, err := tx.Account(id)
	switch {
	case err != nil:
		return Account{}, err
	case !ok:
		return Account{}, notFound(id)
	}
	r, err := decodeRow(id, data)
	if err != nil {
		return Account{}, err
	}
	return r.account(), nil
}
