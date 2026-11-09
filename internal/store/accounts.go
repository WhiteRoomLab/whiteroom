package store

import (
	"fmt"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// maxAccountBytes is the largest row of an account, in bytes.
const maxAccountBytes = 1 << 16

// AccountRow is a row of the table accounts: the ID of a login account and the data that internal/account keeps for it.
type AccountRow struct {
	ID   string
	Data []byte
}

// The store keeps the rows of the login accounts for internal/account (7.3), by account ID, as it keeps the operations,
// and does not read them: it checks the transaction, the ID, the size and whether the row is there. The rows need no
// index, so the methods work in a store of any table schema, also one that has no index.

// Account returns the data that PutAccount has saved for the account id, and whether there are any.
func (t *Tx) Account(id string) ([]byte, bool, error) {
	if err := t.check(false); err != nil {
		return nil, false, err
	}
	if err := content.CheckID(content.RefUser, id); err != nil {
		return nil, false, err
	}
	data, ok, err := t.b.indexGet(indexAccounts, []byte(id))
	if err != nil || !ok {
		return nil, false, err
	}
	return append([]byte{}, data...), true, nil
}

// Accounts returns every row of the accounts, in the order of the IDs. A key that is not a user ID fails it.
func (t *Tx) Accounts() ([]AccountRow, error) {
	if err := t.check(false); err != nil {
		return nil, err
	}
	var rows []AccountRow
	var rerr error
	if err := t.b.indexScan(indexAccounts, nil, func(key, value []byte) bool {
		id := string(key)
		if content.CheckID(content.RefUser, id) != nil {
			rerr = invalidKey(indexAccounts, key)
			return false
		}
		rows = append(rows, AccountRow{ID: id, Data: append([]byte{}, value...)})
		return true
	}); err != nil {
		return nil, err
	}
	if rerr != nil {
		return nil, rerr
	}
	return rows, nil
}

// PutAccount saves data, 1 to 65536 bytes, for the account id, in place of what it had.
func (t *Tx) PutAccount(id string, data []byte) error {
	if err := t.check(true); err != nil {
		return err
	}
	if err := content.CheckID(content.RefUser, id); err != nil {
		return err
	}
	switch {
	case len(data) == 0:
		return fmt.Errorf("accounts %q: empty value", id)
	case len(data) > maxAccountBytes:
		return fmt.Errorf("accounts %q: too large: %d bytes (max %d)", id, len(data), maxAccountBytes)
	}
	return t.b.indexPut(indexAccounts, []byte(id), data)
}

// DeleteAccount deletes the row of the account id. If there is none, the error wraps fs.ErrNotExist.
func (t *Tx) DeleteAccount(id string) error {
	if err := t.check(true); err != nil {
		return err
	}
	if err := content.CheckID(content.RefUser, id); err != nil {
		return err
	}
	_, ok, err := t.b.indexGet(indexAccounts, []byte(id))
	switch {
	case err != nil:
		return err
	case !ok:
		return notFoundError(fmt.Sprintf("accounts %q", id))
	}
	return t.b.indexDelete(indexAccounts, []byte(id))
}
