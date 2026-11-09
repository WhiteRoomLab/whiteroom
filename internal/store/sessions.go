package store

import "fmt"

// maxSessionBytes is the largest row of a session, in bytes.
const maxSessionBytes = 4096

// sessionKeyLen is the length of a key of sessions, the lower case hexadecimal digits of a SHA-256.
const sessionKeyLen = 64

// SessionRow is a row of the table sessions: the key of a session and the data that internal/account keeps for it.
type SessionRow struct {
	Key  string
	Data []byte
}

// The store keeps the rows of the sessions for internal/account (7.3), by the hash of the session token, which it never
// sees, and does not read them: it checks the transaction, the key, the size and whether the row is there. The rows
// need no index, so the methods work in a store of any table schema, also one that has no index.

// checkSessionKey returns an error unless key is 64 lower case hexadecimal digits.
func checkSessionKey(key string) error {
	valid := len(key) == sessionKeyLen
	for i := 0; valid && i < len(key); i++ {
		valid = key[i] >= '0' && key[i] <= '9' || key[i] >= 'a' && key[i] <= 'f'
	}
	if !valid {
		return fmt.Errorf("invalid session key %q: use %d lower case hexadecimal digits", key, sessionKeyLen)
	}
	return nil
}

// Session returns the data that PutSession has saved for the session key, and whether there are any.
func (t *Tx) Session(key string) ([]byte, bool, error) {
	if err := t.check(false); err != nil {
		return nil, false, err
	}
	if err := checkSessionKey(key); err != nil {
		return nil, false, err
	}
	data, ok, err := t.b.indexGet(indexSessions, []byte(key))
	if err != nil || !ok {
		return nil, false, err
	}
	return append([]byte{}, data...), true, nil
}

// Sessions returns every row of the sessions, in the order of the keys. A key that is not a session key fails it.
func (t *Tx) Sessions() ([]SessionRow, error) {
	if err := t.check(false); err != nil {
		return nil, err
	}
	var rows []SessionRow
	var rerr error
	if err := t.b.indexScan(indexSessions, nil, func(key, value []byte) bool {
		k := string(key)
		if checkSessionKey(k) != nil {
			rerr = invalidKey(indexSessions, key)
			return false
		}
		rows = append(rows, SessionRow{Key: k, Data: append([]byte{}, value...)})
		return true
	}); err != nil {
		return nil, err
	}
	if rerr != nil {
		return nil, rerr
	}
	return rows, nil
}

// PutSession saves data, 1 to 4096 bytes, for the session key, in place of what it had.
func (t *Tx) PutSession(key string, data []byte) error {
	if err := t.check(true); err != nil {
		return err
	}
	if err := checkSessionKey(key); err != nil {
		return err
	}
	switch {
	case len(data) == 0:
		return fmt.Errorf("sessions %q: empty value", key)
	case len(data) > maxSessionBytes:
		return fmt.Errorf("sessions %q: too large: %d bytes (max %d)", key, len(data), maxSessionBytes)
	}
	return t.b.indexPut(indexSessions, []byte(key), data)
}

// DeleteSession deletes the row of the session key. If there is none, the error wraps fs.ErrNotExist.
func (t *Tx) DeleteSession(key string) error {
	if err := t.check(true); err != nil {
		return err
	}
	if err := checkSessionKey(key); err != nil {
		return err
	}
	_, ok, err := t.b.indexGet(indexSessions, []byte(key))
	switch {
	case err != nil:
		return err
	case !ok:
		return notFoundError(fmt.Sprintf("sessions %q", key))
	}
	return t.b.indexDelete(indexSessions, []byte(key))
}
