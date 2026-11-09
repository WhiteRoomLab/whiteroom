package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// sessionKeyOf returns the key of a session for s: the lower case hexadecimal digits of its SHA-256, as
// internal/account makes the key of a session token.
func sessionKeyOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func sameSession(a, b SessionRow) bool {
	return a.Key == b.Key && bytes.Equal(a.Data, b.Data)
}

// sessionsText returns rows as text, with the data of 32 bytes or more by their length.
func sessionsText(rows []SessionRow) string {
	var parts []string
	for _, r := range rows {
		data := fmt.Sprintf("%q", r.Data)
		if len(r.Data) >= 32 {
			data = fmt.Sprintf("%d bytes", len(r.Data))
		}
		parts = append(parts, r.Key+" "+data)
	}
	return "[" + strings.Join(parts, "; ") + "]"
}

// checkSessions checks that a View of s reads want from Sessions, in this order, and from Session for each of them, and
// that Session finds none of the keys in missing.
func checkSessions(t *testing.T, s *Store, want []SessionRow, missing ...string) {
	t.Helper()
	view(t, s, func(tx *Tx) error {
		rows, err := tx.Sessions()
		if err != nil || !slices.EqualFunc(rows, want, sameSession) {
			t.Errorf("Sessions() = %s, %v; want %s", sessionsText(rows), err, sessionsText(want))
		}
		for _, w := range want {
			if data, ok, err := tx.Session(w.Key); err != nil || !ok || !bytes.Equal(data, w.Data) {
				t.Errorf("Session(%q) = %s, %v, %v; want %s", w.Key, sessionsText([]SessionRow{{w.Key, data}}), ok, err,
					sessionsText([]SessionRow{w}))
			}
		}
		for _, key := range missing {
			if data, ok, err := tx.Session(key); data != nil || ok || err != nil {
				t.Errorf("Session(%q) of a session that is missing = %q, %v, %v; want none", key, data, ok, err)
			}
		}
		return nil
	})
}

// TestSessions checks the rows of the sessions that the store keeps for internal/account: the order of the keys, the
// copies, the checks of the transaction, the key and the size in their order, and that an Update that fails or is
// rebuilt leaves them.
func TestSessions(t *testing.T) {
	low, mid, high := strings.Repeat("0", 64), sessionKeyOf("a"), strings.Repeat("f", 64)
	badKey := func(key string) string {
		return fmt.Sprintf("invalid session key %q: use 64 lower case hexadecimal digits", key)
	}
	// An empty key, a short one, 63 digits, 65 digits, the digits in upper case, a letter that is not a digit, an
	// algorithm in front of 57 digits, the ID of a user, and 64 bytes of which 2 are one character.
	badKeys := []string{"", "x", mid[:63], mid + "0", strings.ToUpper(mid), mid[:63] + "g", "sha256:" + mid[:57], userID,
		mid[:62] + "\xc3\xa9"}
	first, replaced := SessionRow{low, []byte("first")}, SessionRow{low, []byte("replaced")}
	other, third := SessionRow{mid, []byte("other")}, SessionRow{high, []byte("third")}

	eachStore(t, func(t *testing.T, s *Store) {
		checkSessions(t, s, nil, low, mid)

		// Put in the reverse order of the keys, and read in the order of the keys, in the Update too. The store keeps a
		// copy of what it is given.
		data := bytes.Clone(first.Data)
		update(t, s, func(tx *Tx) error {
			if err := errors.Join(tx.PutSession(other.Key, other.Data), tx.PutSession(first.Key, data)); err != nil {
				return err
			}
			data[0] = 'X'
			if rows, err := tx.Sessions(); err != nil || !slices.EqualFunc(rows, []SessionRow{first, other}, sameSession) {
				t.Errorf("Sessions() in the Update = %s, %v; want the rows put, in the order of the keys", sessionsText(rows),
					err)
			}
			for _, w := range []SessionRow{first, other} {
				if got, ok, err := tx.Session(w.Key); err != nil || !ok || !bytes.Equal(got, w.Data) {
					t.Errorf("Session(%q) in the Update = %q, %v, %v; want %q", w.Key, got, ok, err, w.Data)
				}
			}
			return nil
		})
		checkSessions(t, s, []SessionRow{first, other}, high)

		// A second PutSession replaces the row.
		update(t, s, func(tx *Tx) error { return tx.PutSession(replaced.Key, replaced.Data) })
		checkSessions(t, s, []SessionRow{replaced, other}, high)

		// What the reads return are copies, also in the Update that has put the rows.
		view(t, s, func(tx *Tx) error {
			got, _, _ := tx.Session(low)
			rows, _ := tx.Sessions()
			if len(got) == 0 || len(rows) != 2 {
				t.Fatalf("Session() and Sessions() = %q and %s; want the rows", got, sessionsText(rows))
			}
			got[0], rows[0].Data[0], rows[1].Data[0] = 'X', 'Y', 'Y'
			return nil
		})
		checkSessions(t, s, []SessionRow{replaced, other}, high)
		update(t, s, func(tx *Tx) error {
			if err := tx.PutSession(third.Key, third.Data); err != nil {
				return err
			}
			got, _, _ := tx.Session(high)
			rows, _ := tx.Sessions()
			if len(got) == 0 || len(rows) != 3 {
				t.Fatalf("Session() and Sessions() in the Update = %q and %s; want the rows", got, sessionsText(rows))
			}
			got[0], rows[2].Data[0] = 'X', 'Y'
			return nil
		})
		checkSessions(t, s, []SessionRow{replaced, other, third})

		// DeleteSession removes a row, and fails for one that is missing, with an error that wraps fs.ErrNotExist.
		update(t, s, func(tx *Tx) error { return tx.DeleteSession(low) })
		checkSessions(t, s, []SessionRow{other, third}, low)
		update(t, s, func(tx *Tx) error {
			err := tx.DeleteSession(low)
			if want := fmt.Sprintf("sessions %q: not found", low); err == nil || err.Error() != want ||
				!errors.Is(err, fs.ErrNotExist) {
				t.Errorf("DeleteSession() of a missing row = %v, want an error %q that wraps fs.ErrNotExist", err, want)
			}
			return nil
		})
		checkSessions(t, s, []SessionRow{other, third}, low)

		// Every method that takes a key refuses the keys that are not 64 lower case hexadecimal digits, with the error of
		// the key.
		update(t, s, func(tx *Tx) error {
			for _, key := range badKeys {
				_, _, getErr := tx.Session(key)
				for _, c := range []errorCall{
					{"Session", getErr},
					{"PutSession", tx.PutSession(key, []byte("x"))},
					{"DeleteSession", tx.DeleteSession(key)},
				} {
					if want := badKey(key); c.err == nil || c.err.Error() != want || errors.Is(c.err, fs.ErrNotExist) {
						t.Errorf("%s(%q) = %v, want an error %q that does not wrap fs.ErrNotExist", c.name, key, c.err, want)
					}
				}
			}
			// The key comes before the size.
			if err := tx.PutSession("x", nil); err == nil || err.Error() != badKey("x") {
				t.Errorf("PutSession() of an invalid key and no data = %v, want the error of the key", err)
			}
			return nil
		})
		checkSessions(t, s, []SessionRow{other, third}, low)

		// No data and more than 4096 bytes are refused, and 4096 bytes are kept. A row that is refused is not changed.
		largest := bytes.Repeat([]byte("L"), 4096)
		update(t, s, func(tx *Tx) error {
			for _, tt := range []struct {
				name string
				data []byte
				want string
			}{
				{"nil", nil, fmt.Sprintf("sessions %q: empty value", mid)},
				{"no bytes", []byte{}, fmt.Sprintf("sessions %q: empty value", mid)},
				{"4097 bytes", make([]byte, 4097), fmt.Sprintf("sessions %q: too large: 4097 bytes (max 4096)", mid)},
			} {
				if err := tx.PutSession(mid, tt.data); err == nil || err.Error() != tt.want || errors.Is(err, fs.ErrNotExist) {
					t.Errorf("PutSession() of %s = %v, want an error %q", tt.name, err, tt.want)
				}
			}
			return nil
		})
		checkSessions(t, s, []SessionRow{other, third}, low)
		update(t, s, func(tx *Tx) error { return tx.PutSession(mid, largest) })
		big := SessionRow{mid, largest}
		checkSessions(t, s, []SessionRow{big, third}, low)

		// A View reads, and cannot write, whatever the arguments are.
		view(t, s, func(tx *Tx) error {
			for _, c := range []errorCall{
				{"PutSession", tx.PutSession(low, []byte("x"))},
				{"PutSession of an invalid key", tx.PutSession("x", nil)},
				{"DeleteSession", tx.DeleteSession(mid)},
				{"DeleteSession of an invalid key", tx.DeleteSession("x")},
			} {
				if want := "read-only transaction"; c.err == nil || c.err.Error() != want {
					t.Errorf("%s() in a View = %v, want %q", c.name, c.err, want)
				}
			}
			return nil
		})
		checkSessions(t, s, []SessionRow{big, third}, low)

		// The methods of a transaction that has ended fail with that, before the key is checked.
		var kept []*Tx
		keep := func(tx *Tx) error {
			kept = append(kept, tx)
			return nil
		}
		update(t, s, keep)
		view(t, s, keep)
		for i, tx := range kept {
			_, _, getErr := tx.Session("x")
			_, listErr := tx.Sessions()
			for _, c := range []errorCall{
				{"Session", getErr},
				{"Sessions", listErr},
				{"PutSession", tx.PutSession("x", nil)},
				{"DeleteSession", tx.DeleteSession("x")},
			} {
				if want := "the transaction has ended"; c.err == nil || c.err.Error() != want {
					t.Errorf("%s() of transaction %d after f = %v, want %q", c.name, i, c.err, want)
				}
			}
		}
		checkSessions(t, s, []SessionRow{big, third}, low)

		// An Update that returns an error leaves none of its writes.
		errTest := errors.New("test")
		if err := s.Update(t.Context(), func(tx *Tx) error {
			return errors.Join(tx.PutSession(low, []byte("lost")), tx.PutSession(high, []byte("lost")),
				tx.DeleteSession(mid), errTest)
		}); !errors.Is(err, errTest) {
			t.Errorf("Update() = %v, want the error of f", err)
		}
		checkSessions(t, s, []SessionRow{big, third}, low)

		// RebuildIndex leaves the rows, and writes nothing to them.
		update(t, s, func(tx *Tx) error { return tx.RebuildIndex() })
		checkSessions(t, s, []SessionRow{big, third}, low)
		if w := rebuildWrites(t, s); len(w) != 0 {
			t.Errorf("RebuildIndex() wrote %q with rows in sessions, want nothing", w)
		}
		checkSessions(t, s, []SessionRow{big, third}, low)

		// The accounts and the sessions are tables of their own: Accounts reads only the account, and Sessions only the
		// sessions, also when both are put in one Update.
		account := AccountRow{userID, []byte("account")}
		update(t, s, func(tx *Tx) error { return tx.PutAccount(account.ID, account.Data) })
		checkAccounts(t, s, []AccountRow{account})
		checkSessions(t, s, []SessionRow{big, third}, low)
		update(t, s, func(tx *Tx) error {
			return errors.Join(tx.DeleteSession(high), tx.DeleteSession(mid), tx.DeleteAccount(userID),
				tx.PutAccount(otherUser, []byte("other account")), tx.PutSession(low, []byte("session")))
		})
		checkAccounts(t, s, []AccountRow{{otherUser, []byte("other account")}}, userID)
		checkSessions(t, s, []SessionRow{{low, []byte("session")}}, mid, high)
	})

	t.Run("order of the keys", func(t *testing.T) {
		eachStore(t, func(t *testing.T, s *Store) {
			// 40 keys, put in an order that is not that of the keys.
			const n = 40
			key := func(i int) string { return fmt.Sprintf("%064x", i) }
			var want []SessionRow
			for i := range n {
				want = append(want, SessionRow{key(i), []byte{byte(i + 1)}})
			}
			update(t, s, func(tx *Tx) error {
				for i := range n {
					r := want[i*7%n]
					if err := tx.PutSession(r.Key, r.Data); err != nil {
						return err
					}
				}
				return nil
			})
			checkSessions(t, s, want)
		})
	})

	// The engine keeps the rows: they stay when the store is closed and opened again, and a reader reads them. A key
	// that is not a session key is an error of Sessions, which RebuildIndex does not repair.
	t.Run("engine", func(t *testing.T) {
		rows := []SessionRow{first, other}
		forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
			s, dir := newStore(t, tb)
			update(t, s, func(tx *Tx) error {
				for _, r := range rows {
					if err := tx.PutSession(r.Key, r.Data); err != nil {
						return err
					}
				}
				return nil
			})
			want := [][2]string{{low, "first"}, {mid, "other"}}
			if got := rawTable(t, s, tableSessions); !slices.Equal(got, want) {
				t.Errorf("sessions = %q, want %q: the key and the data as they are", got, want)
			}
			if err := s.Close(); err != nil {
				t.Fatalf("Close() = %v", err)
			}
			s = reopenLog(t, dir, *tb.log)
			checkSessions(t, s, rows, high)
			r, err := openLogReader(dir, tb.options())
			if err != nil {
				t.Fatalf("openLogReader() = %v", err)
			}
			t.Cleanup(func() { _ = r.Close() })
			checkSessions(t, r, rows, high)

			for _, key := range badKeys {
				rawUpdate(t, s, func(tx *logdb.Tx) error { return tx.Put(tableSessions, []byte(key), []byte("v")) })
				view(t, s, func(tx *Tx) error {
					got, err := tx.Sessions()
					if want := fmt.Sprintf("sessions %q: invalid key", key); got != nil || err == nil ||
						err.Error() != want || errors.Is(err, fs.ErrNotExist) {
						t.Errorf("Sessions() with the key %q = %s, %v; want an error %q that does not wrap fs.ErrNotExist", key,
							sessionsText(got), err, want)
					}
					// The rows with the keys of sessions are still read one by one.
					if data, ok, err := tx.Session(low); err != nil || !ok || string(data) != "first" {
						t.Errorf("Session() with the key %q in sessions = %q, %v, %v; want the row", key, data, ok, err)
					}
					return nil
				})
				update(t, s, func(tx *Tx) error { return tx.RebuildIndex() })
				if got := rawTable(t, s, tableSessions); len(got) != 3 {
					t.Errorf("sessions after RebuildIndex() = %q, want the key %q to stay with the two rows", got, key)
				}
				rawUpdate(t, s, func(tx *logdb.Tx) error { return tx.Delete(tableSessions, []byte(key)) })
				checkSessions(t, s, rows, high)
			}
		})
	})
}
