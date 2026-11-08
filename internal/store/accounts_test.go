package store

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// thirdUser follows userID and otherUser in the order of the IDs.
const thirdUser = "usr_01JE0000000000000000000000"

func sameRow(a, b AccountRow) bool {
	return a.ID == b.ID && bytes.Equal(a.Data, b.Data)
}

// rowsText returns rows as text, with the data of 32 bytes or more by their length.
func rowsText(rows []AccountRow) string {
	var parts []string
	for _, r := range rows {
		data := fmt.Sprintf("%q", r.Data)
		if len(r.Data) >= 32 {
			data = fmt.Sprintf("%d bytes", len(r.Data))
		}
		parts = append(parts, r.ID+" "+data)
	}
	return "[" + strings.Join(parts, "; ") + "]"
}

// checkAccounts checks that a View of s reads want from Accounts, in this order, and from Account for each of them, and
// that Account finds none of the IDs in missing.
func checkAccounts(t *testing.T, s *Store, want []AccountRow, missing ...string) {
	t.Helper()
	view(t, s, func(tx *Tx) error {
		rows, err := tx.Accounts()
		if err != nil || !slices.EqualFunc(rows, want, sameRow) {
			t.Errorf("Accounts() = %s, %v; want %s", rowsText(rows), err, rowsText(want))
		}
		for _, w := range want {
			if data, ok, err := tx.Account(w.ID); err != nil || !ok || !bytes.Equal(data, w.Data) {
				t.Errorf("Account(%q) = %s, %v, %v; want %s", w.ID, rowsText([]AccountRow{{w.ID, data}}), ok, err,
					rowsText([]AccountRow{w}))
			}
		}
		for _, id := range missing {
			if data, ok, err := tx.Account(id); data != nil || ok || err != nil {
				t.Errorf("Account(%q) of an account that is missing = %q, %v, %v; want none", id, data, ok, err)
			}
		}
		return nil
	})
}

// errorCall is the error of a call of a method of Tx, by the name of the call.
type errorCall struct {
	name string
	err  error
}

// TestAccounts checks the rows of the accounts that the store keeps for internal/account: the order of the IDs, the
// copies, the checks of the transaction, the ID and the size in their order, and that an Update that fails or is
// rebuilt leaves them.
func TestAccounts(t *testing.T) {
	const use = ` and a ULID (26 characters in upper case)`
	// A short ID, the ID of an entry, an empty one, one in lower case and one above the largest ULID are not IDs of users.
	badIDs := []string{"usr_x", entryID, "", strings.ToLower(userID), "usr_8" + userID[5:]}
	badIDErr := func(id string) string { return fmt.Sprintf("invalid user ID %q: use \"usr_\"%s", id, use) }
	first, second := AccountRow{userID, []byte("first")}, AccountRow{userID, []byte("second")}
	other, third := AccountRow{otherUser, []byte("other")}, AccountRow{thirdUser, []byte("third")}

	eachStore(t, func(t *testing.T, s *Store) {
		checkAccounts(t, s, nil, userID, otherUser)

		// Put in the reverse order of the IDs, and read in the order of the IDs, in the Update too. The store keeps a copy
		// of what it is given.
		data := bytes.Clone(first.Data)
		update(t, s, func(tx *Tx) error {
			if err := errors.Join(tx.PutAccount(other.ID, other.Data), tx.PutAccount(first.ID, data)); err != nil {
				return err
			}
			data[0] = 'X'
			if rows, err := tx.Accounts(); err != nil || !slices.EqualFunc(rows, []AccountRow{first, other}, sameRow) {
				t.Errorf("Accounts() in the Update = %s, %v; want the rows put, in the order of the IDs", rowsText(rows), err)
			}
			return nil
		})
		checkAccounts(t, s, []AccountRow{first, other}, thirdUser)

		// A second PutAccount replaces the row.
		update(t, s, func(tx *Tx) error { return tx.PutAccount(second.ID, second.Data) })
		checkAccounts(t, s, []AccountRow{second, other}, thirdUser)

		// What the reads return are copies, also in the Update that has put the rows.
		view(t, s, func(tx *Tx) error {
			got, _, _ := tx.Account(userID)
			rows, _ := tx.Accounts()
			if len(got) == 0 || len(rows) != 2 {
				t.Fatalf("Account() and Accounts() = %q and %s; want the rows", got, rowsText(rows))
			}
			got[0], rows[0].Data[0], rows[1].Data[0] = 'X', 'Y', 'Y'
			return nil
		})
		checkAccounts(t, s, []AccountRow{second, other}, thirdUser)
		update(t, s, func(tx *Tx) error {
			if err := tx.PutAccount(third.ID, third.Data); err != nil {
				return err
			}
			got, _, _ := tx.Account(thirdUser)
			rows, _ := tx.Accounts()
			if len(got) == 0 || len(rows) != 3 {
				t.Fatalf("Account() and Accounts() in the Update = %q and %s; want the rows", got, rowsText(rows))
			}
			got[0], rows[2].Data[0] = 'X', 'Y'
			return nil
		})
		checkAccounts(t, s, []AccountRow{second, other, third})

		// DeleteAccount removes a row, and fails for one that is missing, with an error that wraps fs.ErrNotExist.
		update(t, s, func(tx *Tx) error { return tx.DeleteAccount(userID) })
		checkAccounts(t, s, []AccountRow{other, third}, userID)
		update(t, s, func(tx *Tx) error {
			err := tx.DeleteAccount(userID)
			if want := fmt.Sprintf("accounts %q: not found", userID); err == nil || err.Error() != want ||
				!errors.Is(err, fs.ErrNotExist) {
				t.Errorf("DeleteAccount() of a missing row = %v, want an error %q that wraps fs.ErrNotExist", err, want)
			}
			return nil
		})
		checkAccounts(t, s, []AccountRow{other, third}, userID)

		// Every method that takes an ID refuses the IDs that are not those of users, with the error of the ID.
		update(t, s, func(tx *Tx) error {
			for _, id := range badIDs {
				_, _, getErr := tx.Account(id)
				for _, c := range []errorCall{
					{"Account", getErr},
					{"PutAccount", tx.PutAccount(id, []byte("x"))},
					{"DeleteAccount", tx.DeleteAccount(id)},
				} {
					if want := badIDErr(id); c.err == nil || c.err.Error() != want || errors.Is(c.err, fs.ErrNotExist) {
						t.Errorf("%s(%q) = %v, want an error %q that does not wrap fs.ErrNotExist", c.name, id, c.err, want)
					}
				}
			}
			// The ID comes before the size.
			if err := tx.PutAccount("usr_x", nil); err == nil || err.Error() != badIDErr("usr_x") {
				t.Errorf("PutAccount() of an invalid ID and no data = %v, want the error of the ID", err)
			}
			return nil
		})
		checkAccounts(t, s, []AccountRow{other, third}, userID)

		// No data and more than 65536 bytes are refused, and 65536 bytes are kept. A row that is refused is not changed.
		largest := bytes.Repeat([]byte("L"), 65536)
		update(t, s, func(tx *Tx) error {
			for _, tt := range []struct {
				name string
				data []byte
				want string
			}{
				{"nil", nil, fmt.Sprintf("accounts %q: empty value", otherUser)},
				{"no bytes", []byte{}, fmt.Sprintf("accounts %q: empty value", otherUser)},
				{"65537 bytes", make([]byte, 65537),
					fmt.Sprintf("accounts %q: too large: 65537 bytes (max 65536)", otherUser)},
			} {
				if err := tx.PutAccount(otherUser, tt.data); err == nil || err.Error() != tt.want ||
					errors.Is(err, fs.ErrNotExist) {
					t.Errorf("PutAccount() of %s = %v, want an error %q", tt.name, err, tt.want)
				}
			}
			return nil
		})
		checkAccounts(t, s, []AccountRow{other, third}, userID)
		update(t, s, func(tx *Tx) error { return tx.PutAccount(otherUser, largest) })
		big := AccountRow{otherUser, largest}
		checkAccounts(t, s, []AccountRow{big, third}, userID)

		// A View reads, and cannot write, whatever the arguments are.
		view(t, s, func(tx *Tx) error {
			for _, c := range []errorCall{
				{"PutAccount", tx.PutAccount(userID, []byte("x"))},
				{"PutAccount of an invalid ID", tx.PutAccount("usr_x", nil)},
				{"DeleteAccount", tx.DeleteAccount(otherUser)},
				{"DeleteAccount of an invalid ID", tx.DeleteAccount("usr_x")},
			} {
				if want := "read-only transaction"; c.err == nil || c.err.Error() != want {
					t.Errorf("%s() in a View = %v, want %q", c.name, c.err, want)
				}
			}
			return nil
		})
		checkAccounts(t, s, []AccountRow{big, third}, userID)

		// The methods of a transaction that has ended fail with that, before the ID is checked.
		var kept []*Tx
		keep := func(tx *Tx) error {
			kept = append(kept, tx)
			return nil
		}
		update(t, s, keep)
		view(t, s, keep)
		for i, tx := range kept {
			_, _, getErr := tx.Account("usr_x")
			_, listErr := tx.Accounts()
			for _, c := range []errorCall{
				{"Account", getErr},
				{"Accounts", listErr},
				{"PutAccount", tx.PutAccount("usr_x", nil)},
				{"DeleteAccount", tx.DeleteAccount("usr_x")},
			} {
				if want := "the transaction has ended"; c.err == nil || c.err.Error() != want {
					t.Errorf("%s() of transaction %d after f = %v, want %q", c.name, i, c.err, want)
				}
			}
		}
		checkAccounts(t, s, []AccountRow{big, third}, userID)

		// An Update that returns an error leaves none of its writes.
		errTest := errors.New("test")
		if err := s.Update(t.Context(), func(tx *Tx) error {
			return errors.Join(tx.PutAccount(userID, []byte("lost")), tx.PutAccount(thirdUser, []byte("lost")),
				tx.DeleteAccount(otherUser), errTest)
		}); !errors.Is(err, errTest) {
			t.Errorf("Update() = %v, want the error of f", err)
		}
		checkAccounts(t, s, []AccountRow{big, third}, userID)

		// RebuildIndex leaves the rows, and writes nothing to them.
		update(t, s, func(tx *Tx) error { return tx.RebuildIndex() })
		checkAccounts(t, s, []AccountRow{big, third}, userID)
		if w := rebuildWrites(t, s); len(w) != 0 {
			t.Errorf("RebuildIndex() wrote %q with rows in accounts, want nothing", w)
		}
		checkAccounts(t, s, []AccountRow{big, third}, userID)
	})

	t.Run("order of the IDs", func(t *testing.T) {
		eachStore(t, func(t *testing.T, s *Store) {
			// 40 IDs, put in an order that is not that of the IDs.
			const n = 40
			id := func(i int) string { return fmt.Sprintf("usr_01JF%022d", i) }
			var want []AccountRow
			for i := range n {
				want = append(want, AccountRow{id(i), []byte{byte(i)}})
			}
			update(t, s, func(tx *Tx) error {
				for i := range n {
					r := want[i*7%n]
					if err := tx.PutAccount(r.ID, r.Data); err != nil {
						return err
					}
				}
				return nil
			})
			checkAccounts(t, s, want)
		})
	})

	// The engine keeps the rows: they stay when the store is closed and opened again, and a reader reads them. A key that
	// is not the ID of a user is an error of Accounts, which RebuildIndex does not repair.
	t.Run("engine", func(t *testing.T) {
		rows := []AccountRow{first, other}
		forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
			s, dir := newStore(t, tb)
			update(t, s, func(tx *Tx) error {
				for _, r := range rows {
					if err := tx.PutAccount(r.ID, r.Data); err != nil {
						return err
					}
				}
				return nil
			})
			want := [][2]string{{userID, "first"}, {otherUser, "other"}}
			if got := rawTable(t, s, tableAccounts); !slices.Equal(got, want) {
				t.Errorf("accounts = %q, want %q: the ID and the data as they are", got, want)
			}
			if err := s.Close(); err != nil {
				t.Fatalf("Close() = %v", err)
			}
			s = reopenLog(t, dir, *tb.log)
			checkAccounts(t, s, rows, thirdUser)
			r, err := openLogReader(dir, tb.options())
			if err != nil {
				t.Fatalf("openLogReader() = %v", err)
			}
			t.Cleanup(func() { _ = r.Close() })
			checkAccounts(t, r, rows, thirdUser)

			for _, key := range []string{"x", "", strings.ToLower(userID), entryID, userID + "0", userID + "\x00",
				userID[:len(userID)-1]} {
				rawUpdate(t, s, func(tx *logdb.Tx) error { return tx.Put(tableAccounts, []byte(key), []byte("v")) })
				view(t, s, func(tx *Tx) error {
					got, err := tx.Accounts()
					if want := fmt.Sprintf("accounts %q: invalid key", key); got != nil || err == nil ||
						err.Error() != want || errors.Is(err, fs.ErrNotExist) {
						t.Errorf("Accounts() with the key %q = %s, %v; want an error %q that does not wrap fs.ErrNotExist",
							key, rowsText(got), err, want)
					}
					// The rows with the IDs of users are still read one by one.
					if data, ok, err := tx.Account(userID); err != nil || !ok || string(data) != "first" {
						t.Errorf("Account() with the key %q in accounts = %q, %v, %v; want the row", key, data, ok, err)
					}
					return nil
				})
				update(t, s, func(tx *Tx) error { return tx.RebuildIndex() })
				if got := rawTable(t, s, tableAccounts); len(got) != 3 {
					t.Errorf("accounts after RebuildIndex() = %q, want the key %q to stay with the two rows", got, key)
				}
				rawUpdate(t, s, func(tx *logdb.Tx) error { return tx.Delete(tableAccounts, []byte(key)) })
				checkAccounts(t, s, rows, thirdUser)
			}
		})
	})
}
