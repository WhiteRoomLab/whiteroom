package logdb

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

// TestOperationID shows how a caller makes an operation idempotent with an ID, with no feature of the engine for it:
// the Update that makes the change also puts ops/<id>. When the reply is lost, as in a crash right after the commit,
// the caller finds out after reopening whether the change was committed, and does not make it twice.
func TestOperationID(t *testing.T) {
	ctx := context.Background()
	const id = "op-1"
	// deposit adds 10 to the balance and appends an event, unless the operation id has been done.
	deposit := func(db *DB) (done bool, err error) {
		err = db.Update(ctx, func(tx *Tx) error {
			if _, ok := tx.Get(1, []byte("ops/"+id)); ok {
				done = true
				return nil
			}
			v, _ := tx.Get(1, []byte("balance"))
			n, _ := strconv.Atoi(string(v))
			return errors.Join(tx.Put(1, []byte("ops/"+id), []byte("done")),
				tx.Put(1, []byte("balance"), []byte(strconv.Itoa(n+10))), tx.AppendEvent([]byte("deposit "+id)))
		})
		return done, err
	}
	// check returns whether the operation is done, and checks that the balance and the events agree.
	check := func(t *testing.T, db *DB) bool {
		t.Helper()
		var done bool
		var balance string
		if err := db.View(ctx, func(tx *Tx) error {
			_, done = tx.Get(1, []byte("ops/"+id))
			v, _ := tx.Get(1, []byte("balance"))
			balance = string(v)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		events := eventLines(t, db)
		switch {
		case done && balance == "10" && events == "1/1=deposit "+id:
		case !done && balance == "" && events == "":
		default:
			t.Errorf("done %v, balance %q, events %q", done, balance, events)
		}
		checkObjects(t, db)
		return done
	}
	for _, c := range []struct {
		name, point string // the hook where the process crashes; "": after Update has returned
		committed   string // "yes", "no" or "either"
	}{
		{"after the reply", "", "yes"},
		{"before the record", "update:objects", "no"},
		{"with the record written", "update:written", "either"},
		{"with the record synced", "update:synced", "yes"},
	} {
		for _, m := range []string{"keep-all", "lose-all"} {
			t.Run(c.name+", "+m, func(t *testing.T) {
				fsys := simfs.New(simDir)
				db, err := Create(fsys, Options{Hook: func(point string) error {
					if point == c.point {
						fsys.CrashAt(fsys.Counted())
					}
					return nil
				}})
				if err != nil {
					t.Fatal(err)
				}
				_, err = deposit(db)
				if (err == nil) != (c.committed == "yes") {
					t.Errorf("the first deposit: %v", err)
				}
				_ = db.Close()
				// The reply is lost: the caller cannot tell whether the deposit was made.
				model := loseAll
				if m == "keep-all" {
					model = keepAll(fsys)
				}
				crashed := fsys.Crash(model)
				db = reopen(t, crashed, Options{})
				done := check(t, db)
				if c.committed == "yes" && !done || c.committed == "no" && done {
					t.Errorf("after the crash, the deposit is done: %v", done)
				}
				again, err := deposit(db)
				if err != nil || again != done {
					t.Errorf("the deposit again: done before %v, %v", again, err)
				}
				if !check(t, db) {
					t.Error("the deposit is not done")
				}
				closeDB(t, db)
				if !check(t, reopen(t, crashed.Crash(loseAll), Options{})) {
					t.Error("the deposit is not durable")
				}
			})
		}
	}
}
