package dbserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

// acceptAt is the time of the acceptances of these tests, which is not the time of any mark of the fixtures.
var acceptAt = pruneNow.Add(36 * time.Hour)

// newChain is the records 1 to n of the audit log named log that chain.
func newChain(log string, n int) *loginChain {
	c := &loginChain{}
	prev := ""
	for i := 1; i <= n; i++ {
		data := auditRecord(log, uint64(i), prev)
		c.recs = append(c.recs, data)
		sum := sha256.Sum256(data)
		c.sums = append(c.sums, sum)
		prev = hashText(sum)
	}
	return c
}

// changedRecord is the record n of log that has the hash of record n-1 of chain, as the one of chain has, and other bytes: the
// record n+1 of chain has the hash of the one that chain has, which this has not, so that it is a break.
func changedRecord(log string, n int, chain *loginChain) []byte {
	prev := ""
	if n > 1 {
		prev = hashText(chain.sum(n - 1))
	}
	return bytes.Replace(auditRecord(log, uint64(n), prev), []byte(`"x"`), []byte(`"changed"`), 1)
}

// recordsFixture is a store whose commits each have one of the events and a mark at pruneNow, and whose rows are changed by
// the steps. Its server is closed.
func recordsFixture(t *testing.T, events [][]byte, steps ...func(t *testing.T, f *fixture)) *fixture {
	t.Helper()
	f := newFixture(t, nil)
	for _, e := range events {
		f.commitAt(pruneNow, e)
	}
	for _, step := range steps {
		step(t, f)
	}
	if err := f.srv.Close(); err != nil {
		t.Fatal(err)
	}
	return f
}

// putRow returns the step of a store that puts the row in table.
func putRow(table uint32, key, value []byte) func(t *testing.T, f *fixture) {
	return func(t *testing.T, f *fixture) {
		t.Helper()
		f.update(func(tx *logdb.Tx) {
			if err := tx.Put(table, key, value); err != nil {
				t.Error(err)
			}
		})
	}
}

// startAt is the step of a store that puts the chain start of the login history: the record first is the first that remains,
// and prev the hash of the one before it.
func startAt(first uint64, prev [sha256.Size]byte) func(t *testing.T, f *fixture) {
	return putRow(dbwire.TableChains, dbwire.ChainStartKey(dbwire.LogLogin),
		dbwire.AppendChainStart(nil, dbwire.ChainStart{First: first, Prev: prev, At: pruneNow}))
}

// storeFiles returns every file of the world of f with what it holds, except the head of the engine, which each open of the
// engine writes again.
func storeFiles(f *fixture) map[string]string {
	files := worldFiles(f.fs)
	delete(files, "logdb/head")
	return files
}

// acceptBreak runs AcceptBreak at acceptAt on the world of f, whose server is closed.
func acceptBreak(t *testing.T, f *fixture, log string, n uint64) error {
	t.Helper()
	return maintain(t.Context(), f.fs, "data", f.opts, func(m *logdb.Maintenance) error {
		return AcceptBreak(t.Context(), m, log, n, acceptAt)
	})
}

// chainTable returns the rows of the table chains of the world of f, by their keys.
func chainTable(t *testing.T, f *fixture) map[string]string {
	t.Helper()
	rows := map[string]string{}
	if err := maintain(t.Context(), f.fs, "data", f.opts, func(m *logdb.Maintenance) error {
		return m.View(t.Context(), func(tx *logdb.Tx) error {
			tx.Scan(dbwire.TableChains, nil, func(k, v []byte) bool {
				rows[string(k)] = string(v)
				return true
			})
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	return rows
}

// checkAccepted checks that the store of f is what before was, with the acceptance of the break at record n of log whose
// record has the hash sum: one row of the table chains, and, at the end, the event of the acceptance and a mark in one
// commit.
func checkAccepted(t *testing.T, f *fixture, before view, log string, n uint64, sum [sha256.Size]byte) {
	t.Helper()
	after := viewOf(t, f)
	kept(t, before, after)
	checkRows(t, before, after)
	if want := before.shape + fmt.Sprintf(" B%d M", n); after.shape != want {
		t.Errorf("the store is %q, want %q", after.shape, want)
	}
	if len(after.events) < 2 {
		return
	}
	mark, accepted := after.events[len(after.events)-1], after.events[len(after.events)-2]
	if at, ok := dbwire.ParseMark(mark.Data); !ok || !at.Equal(acceptAt) {
		t.Errorf("the last event is %q, want the mark of the time of the acceptance", mark.Data)
	}
	want := dbwire.Accepted{Log: log, N: n, Hash: sum}
	if got, ok := dbwire.ParseAccepted(accepted.Data); !ok || got != want {
		t.Errorf("the event of the acceptance is %+v (%v), want %+v", got, ok, want)
	}
	if accepted.Seq != mark.Seq {
		t.Error("the event of the acceptance is not in the commit of the mark")
	}
	row := chainTable(t, f)[string(dbwire.ChainBreakKey(log, n))]
	if b, ok := dbwire.ParseChainBreak([]byte(row)); !ok || b.Hash != sum || !b.At.Equal(acceptAt) {
		t.Errorf("the row of the break is %+v (%v), want the hash %x at %v", b, ok, sum[:4], acceptAt)
	}
}

// TestAcceptBreak checks the acceptance of a real break of each log: the row, the event and the mark are written in one
// commit that is durable, nothing else changes, the other log is not looked at, and a second acceptance is refused.
func TestAcceptBreak(t *testing.T) {
	for _, log := range []string{dbwire.LogLogin, dbwire.LogActivity} {
		other := dbwire.LogLogin
		if log == other {
			other = dbwire.LogActivity
		}
		t.Run(log, func(t *testing.T) {
			chain, otherChain := newChain(log, 6), newChain(other, 6)
			// The record 4 is another one, so the record 5, which has the hash of the one that was there, is the break. The
			// other log has all its records, with a record 5 that follows.
			f := recordsFixture(t, [][]byte{chain.rec(1), otherChain.rec(1), chain.rec(2), otherChain.rec(2), chain.rec(3),
				otherChain.rec(3), changedRecord(log, 4, chain), otherChain.rec(4), chain.rec(5), otherChain.rec(5), chain.rec(6),
				otherChain.rec(6)})
			before := viewOf(t, f)
			if err := acceptBreak(t, f, other, 5); err == nil || err.Error() != "record 5 follows record 4: there is no break to accept" {
				t.Errorf("AcceptBreak() of the record 5 of the other log = %v", err)
			}
			kept(t, before, viewOf(t, f))
			if err := acceptBreak(t, f, log, 5); err != nil {
				t.Fatalf("AcceptBreak() = %v", err)
			}
			checkAccepted(t, f, before, log, 5, chain.sum(5))

			// The commit is durable: a crash that loses what the disk has not got keeps it.
			image := f.fs.Crash(loseAll)
			checkAccepted(t, &fixture{t: t, fs: image, opts: f.opts, logs: f.logs, clock: f.clock}, before, log, 5, chain.sum(5))

			// A second acceptance of it is refused, and writes nothing.
			accepted, files := viewOf(t, f), storeFiles(f)
			want := fmt.Sprintf("the break at record 5 of the audit log %s is already accepted", log)
			if err := acceptBreak(t, f, log, 5); err == nil || err.Error() != want {
				t.Errorf("a second AcceptBreak() = %v, want %q", err, want)
			}
			if got := storeFiles(f); !maps.Equal(got, files) {
				t.Error("the second acceptance changed the files of the store")
			}
			kept(t, accepted, viewOf(t, f))
		})
	}
}

// gapText is the refusal of the record n, which does not follow the record before in number.
func gapText(n, before int) string {
	return fmt.Sprintf("record %d does not follow record %d in number: a gap, which a lost record leaves, is not a break that can be "+
		"accepted, and waits for the repair of the engine", n, before)
}

// TestAcceptBreakRefused checks that each refusal says why, in the words of the command, and writes nothing.
func TestAcceptBreakRefused(t *testing.T) {
	chain := newChain(dbwire.LogLogin, 6)
	good := [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), chain.rec(4), chain.rec(5), chain.rec(6)}
	withBreak := slices.Clone(good)
	withBreak[3] = changedRecord(dbwire.LogLogin, 4, chain) // the record 5 is the break
	firstText := func(n int) string {
		return fmt.Sprintf("record %d is the first record that the audit log login is checked from: a break there is not accepted", n)
	}
	unreadable := func(log, why string) string {
		return fmt.Sprintf("the chain of the audit log %s cannot be read, so no break of it is accepted (the table chains: %s)", log, why)
	}
	for _, tt := range []struct {
		name   string
		events [][]byte
		step   func(t *testing.T, f *fixture) // changes the store before its server is closed
		log    string
		n      uint64
		want   string
	}{
		{"a log that is none", good, nil, "audit", 3, `the audit log "audit" is neither login nor activity`},
		{"record 0", good, nil, dbwire.LogLogin, 0, "the records of an audit log are numbered from 1"},
		{"a record that is not there", good, nil, dbwire.LogLogin, 7, "record 7 of the audit log login is not in the store"},
		{"a record of the other log", good, nil, dbwire.LogActivity, 3, "record 3 of the audit log activity is not in the store"},
		{"a record before the chain start", good, startAt(3, chain.sum(2)), dbwire.LogLogin, 2,
			"record 2 of the audit log login is not in the store: the records before record 3 were deleted"},
		{"a record that the deletion has dropped", good[2:], startAt(3, chain.sum(2)), dbwire.LogLogin, 1,
			"record 1 of the audit log login is not in the store: the records before record 3 were deleted"},
		{"the first record", good, nil, dbwire.LogLogin, 1, firstText(1)},
		{"a first record with the hash of one before it", [][]byte{auditRecord("login", 1, hashText(wrong32)), chain.rec(2)}, nil,
			dbwire.LogLogin, 1, firstText(1)},
		{"a first record that is not record 1", [][]byte{chain.rec(3), chain.rec(4)}, nil, dbwire.LogLogin, 3, firstText(3)},
		{"the first record that remains", good[2:], startAt(3, wrong32), dbwire.LogLogin, 3, firstText(3)},
		{"the first record that remains, with the records that a deletion left before it", good, startAt(3, wrong32),
			dbwire.LogLogin, 3, firstText(3)},
		{"a record that follows", good, nil, dbwire.LogLogin, 3, "record 3 follows record 2: there is no break to accept"},
		{"a record whose number skips", [][]byte{chain.rec(1), chain.rec(2), chain.rec(4)}, nil, dbwire.LogLogin, 4, gapText(4, 2)},
		{"a record whose number skips, with another prev", [][]byte{chain.rec(1), chain.rec(2), changedRecord(dbwire.LogLogin, 4, chain)},
			nil, dbwire.LogLogin, 4, gapText(4, 2)},
		{"a record whose number goes back", [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), chain.rec(5), chain.rec(4)}, nil,
			dbwire.LogLogin, 4, gapText(4, 5)},
		{"a record whose number skips after the chain start", [][]byte{chain.rec(3), chain.rec(5)}, startAt(3, chain.sum(2)),
			dbwire.LogLogin, 5, gapText(5, 3)},
		{"a record that follows after the chain start", good[2:], startAt(3, chain.sum(2)), dbwire.LogLogin, 4,
			"record 4 follows record 3: there is no break to accept"},
		{"a record that is there twice", append(slices.Clone(good), chain.rec(2)), nil, dbwire.LogLogin, 2,
			"record 2 of the audit log login is in the store more than once, so a break at it cannot be named by its number"},
		{"a break that is accepted already", withBreak, putRow(dbwire.TableChains, dbwire.ChainBreakKey(dbwire.LogLogin, 5),
			dbwire.AppendChainBreak(nil, dbwire.ChainBreak{Hash: chain.sum(5), At: pruneNow})), dbwire.LogLogin, 5,
			"the break at record 5 of the audit log login is already accepted"},
		{"a chain start that cannot be read", withBreak, putRow(dbwire.TableChains, dbwire.ChainStartKey(dbwire.LogLogin),
			[]byte("short")), dbwire.LogLogin, 5, unreadable("login", "the chain start cannot be read")},
		{"a row of a break that cannot be read", withBreak, putRow(dbwire.TableChains, dbwire.ChainBreakKey(dbwire.LogLogin, 2),
			[]byte("short")), dbwire.LogLogin, 5, unreadable("login", "a row of an accepted break cannot be read")},
		{"a chain start of the activity log", withBreak, putRow(dbwire.TableChains, dbwire.ChainStartKey(dbwire.LogActivity),
			dbwire.AppendChainStart(nil, dbwire.ChainStart{First: 3, Prev: wrong32, At: pruneNow})), dbwire.LogActivity, 5,
			unreadable("activity", "a chain start for a log of which only the login records are ever deleted")},
		{"an older table schema", withBreak, putRow(dbwire.TableMeta, []byte(dbwire.MetaSchema), []byte{dbwire.TableVersion - 1}),
			dbwire.LogLogin, 5, fmt.Sprintf("the store has the table schema %d, and the first writer that connects through "+
				"whiteroom-db serve (whiteroom serve admin, for one) upgrades it to %d: accept the break then", dbwire.TableVersion-1,
				dbwire.TableVersion)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var steps []func(t *testing.T, f *fixture)
			if tt.step != nil {
				steps = append(steps, tt.step)
			}
			f := recordsFixture(t, tt.events, steps...)
			before, table := viewOf(t, f), chainTable(t, f)
			files := storeFiles(f)
			if err := acceptBreak(t, f, tt.log, tt.n); err == nil || err.Error() != tt.want {
				t.Fatalf("AcceptBreak() = %v, want %q", err, tt.want)
			}
			if got := storeFiles(f); !maps.Equal(got, files) {
				t.Error("the refusal changed the files of the store")
			}
			after := viewOf(t, f)
			kept(t, before, after)
			checkRows(t, before, after)
			if got := chainTable(t, f); !maps.Equal(got, table) {
				t.Errorf("the table chains is %v after the refusal, was %v", got, table)
			}
		})
	}
}

// TestAcceptBreakWalk checks what AcceptBreak takes as the record before a record: the one before it in the order of the
// events of its log, as VerifyAudit walks them, whatever is between them.
func TestAcceptBreakWalk(t *testing.T) {
	chain := newChain(dbwire.LogLogin, 6)
	refused := func(t *testing.T, f *fixture, n uint64, want string) {
		t.Helper()
		before, files := viewOf(t, f), storeFiles(f)
		if err := acceptBreak(t, f, dbwire.LogLogin, n); err == nil || err.Error() != want {
			t.Errorf("AcceptBreak() of the record %d = %v, want %q", n, err, want)
		}
		if got := storeFiles(f); !maps.Equal(got, files) {
			t.Error("the refusal changed the files of the store")
		}
		kept(t, before, viewOf(t, f))
	}
	t.Run("a record that is missing", func(t *testing.T) {
		// The record 5 has the hash of the record 4, which is not there: its number does not follow, and a lost record is no break.
		f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), chain.rec(5), chain.rec(6)})
		refused(t, f, 5, gapText(5, 3))
		refused(t, f, 6, "record 6 follows record 5: there is no break to accept")
	})
	t.Run("a number that skips", func(t *testing.T) {
		// The record 6 has the hash of the record 4, the last one: only its number does not follow.
		skipping := auditRecord(dbwire.LogLogin, 6, hashText(chain.sum(4)))
		f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), chain.rec(4), skipping})
		refused(t, f, 6, gapText(6, 4))
	})
	t.Run("events that are no record", func(t *testing.T) {
		junk, badDaemon := []byte("junk"), []byte{dbwire.DaemonEvent, 0x7f, 1, 2}
		f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(2), junk, changedRecord(dbwire.LogLogin, 3, chain), badDaemon, chain.rec(4)})
		before := viewOf(t, f)
		if want := "L1 M L2 M ? M L3 M ? M L4 M"; before.shape != want {
			t.Fatalf("the store is %q, want %q", before.shape, want)
		}
		if err := acceptBreak(t, f, dbwire.LogLogin, 4); err != nil {
			t.Fatalf("AcceptBreak() = %v", err)
		}
		checkAccepted(t, f, before, dbwire.LogLogin, 4, chain.sum(4))
	})
	t.Run("records in another order", func(t *testing.T) {
		f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(3), chain.rec(2)})
		refused(t, f, 3, gapText(3, 1))
		// The record 2 comes after the record 3, which does not make it follow, though the record 1 is before.
		refused(t, f, 2, gapText(2, 3))
	})
	t.Run("the records that a deletion left", func(t *testing.T) {
		// The chain starts at the record 3, and the records 1 and 2 are still there.
		f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), changedRecord(dbwire.LogLogin, 4, chain), chain.rec(5)},
			startAt(3, chain.sum(2)))
		before := viewOf(t, f)
		if err := acceptBreak(t, f, dbwire.LogLogin, 5); err != nil {
			t.Fatalf("AcceptBreak() = %v", err)
		}
		checkAccepted(t, f, before, dbwire.LogLogin, 5, chain.sum(5))
	})
}

// rewriteLogin compacts the store of f, whose server is closed, with the record n of the login history replaced by data.
func rewriteLogin(t *testing.T, f *fixture, n uint64, data []byte) {
	t.Helper()
	if err := maintain(t.Context(), f.fs, "data", f.opts, func(m *logdb.Maintenance) error {
		return m.Compact(t.Context(), logdb.Rewrite{Event: func(e logdb.Event) ([]byte, bool) {
			if log, got, _, ok := dbwire.ReadRecordID(e.Data); ok && log == dbwire.LogLogin && got == n {
				return data, true
			}
			return e.Data, true
		}})
	}); err != nil {
		t.Fatal(err)
	}
}

// TestAcceptBreakAgain checks that a break whose record has been rewritten since it was accepted, which VerifyAudit no longer
// honors, is accepted again: the row has the new hash, and the store has the event of each acceptance.
func TestAcceptBreakAgain(t *testing.T) {
	chain := newChain(dbwire.LogLogin, 6)
	f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), changedRecord(dbwire.LogLogin, 4, chain), chain.rec(5), chain.rec(6)})
	if err := acceptBreak(t, f, dbwire.LogLogin, 5); err != nil {
		t.Fatal(err)
	}
	again := bytes.Replace(chain.rec(5), []byte(`"x"`), []byte(`"again"`), 1)
	rewriteLogin(t, f, 5, again)
	before := viewOf(t, f)
	if err := acceptBreak(t, f, dbwire.LogLogin, 5); err != nil {
		t.Fatalf("AcceptBreak() of the record that was rewritten = %v", err)
	}
	checkAccepted(t, f, before, dbwire.LogLogin, 5, sha256.Sum256(again))
	if !strings.HasSuffix(viewOf(t, f).shape, "B5 M B5 M") {
		t.Errorf("the store is %q, want the event of each acceptance", viewOf(t, f).shape)
	}
	if rows := chainTable(t, f); len(rows) != 1 {
		t.Errorf("the table chains has %d rows, want the one of the break", len(rows))
	}
	// The break that is accepted now is refused again.
	if err := acceptBreak(t, f, dbwire.LogLogin, 5); err == nil || err.Error() != "the break at record 5 of the audit log login is already accepted" {
		t.Errorf("a third AcceptBreak() = %v", err)
	}
}

// TestAcceptBreakThenCompact checks that CompactLogin goes on across a break that AcceptBreak accepted, drops the records on
// both sides of it, and leaves the row and the event of the acceptance, which VerifyAudit ignores once the record is gone.
func TestAcceptBreakThenCompact(t *testing.T) {
	chain := newChain(dbwire.LogLogin, 6)
	f := newFixture(t, nil)
	for i, rec := range [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), changedRecord(dbwire.LogLogin, 4, chain), chain.rec(5), chain.rec(6)} {
		f.commitAt(monthsAgo(40+i), rec)
	}
	f.anchor()
	if err := f.srv.Close(); err != nil {
		t.Fatal(err)
	}
	before := viewOf(t, f)
	// Without the acceptance the deletion stops at the break, and keeps the record before it.
	if got := compactLoginWorld(t, f.fs.Crash(keepAll(f.fs)), f, pruneNow); got.First != 1 || got.Last != 3 || got.Stopped == "" {
		t.Errorf("CompactLogin() before the acceptance = %+v, want the records 1 to 3 and a stop", got)
	}
	if err := acceptBreak(t, f, dbwire.LogLogin, 5); err != nil {
		t.Fatalf("AcceptBreak() = %v", err)
	}
	checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 6, Cutoff: pruneNow.AddDate(-1, 0, 0)})
	after := viewOf(t, f)
	checkStart(t, after, 7, chain.sum(6), pruneNow)
	kept(t, before, after, "L1", "L2", "L3", "L4", "L5", "L6")
	if want := "M M M M M M A99 M B5 M D7 M"; after.shape != want {
		t.Errorf("the store is %q, want %q", after.shape, want)
	}
	if rows := chainTable(t, f); len(rows) != 2 {
		t.Errorf("the table chains has %d rows, want the start and the break", len(rows))
	}
}

// compactLoginWorld runs CompactLogin at now on the world, with the options of f.
func compactLoginWorld(t *testing.T, world *simfs.FS, f *fixture, now time.Time) Deleted {
	t.Helper()
	return compactLogin(t, &fixture{t: t, fs: world, opts: f.opts, logs: f.logs, clock: f.clock}, now)
}

// TestAcceptBreakFailure checks that a failed sync of the commit leaves the mark of a failed sync, as for any maintenance,
// and that an error of the context is returned with nothing written.
func TestAcceptBreakFailure(t *testing.T) {
	chain := newChain(dbwire.LogLogin, 6)
	events := [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), changedRecord(dbwire.LogLogin, 4, chain), chain.rec(5)}
	t.Run("a sync fails", func(t *testing.T) {
		f := recordsFixture(t, events)
		err := maintain(t.Context(), f.fs, "data", f.opts, func(m *logdb.Maintenance) error {
			f.fs.Inject(simfs.Fault{Match: func(op simfs.Op) bool {
				return op.Kind == simfs.OpSync && strings.HasPrefix(op.Path, "logdb/log/")
			}, Err: syscall.EIO, Times: 1})
			return AcceptBreak(t.Context(), m, dbwire.LogLogin, 5, acceptAt)
		})
		if !errors.Is(err, logdb.ErrFailed) || !errors.Is(err, syscall.EIO) {
			t.Fatalf("AcceptBreak() = %v, want an error that wraps ErrFailed and EIO", err)
		}
		if text, ok := worldFile(f.fs, "stopped"); !ok || !strings.Contains(text, "input/output error") {
			t.Errorf("the mark is %q (%v), want the mark of the failed sync", text, ok)
		}
	})
	t.Run("the context ends", func(t *testing.T) {
		f := recordsFixture(t, events)
		before, files := viewOf(t, f), storeFiles(f)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		err := maintain(t.Context(), f.fs, "data", f.opts, func(m *logdb.Maintenance) error {
			return AcceptBreak(ctx, m, dbwire.LogLogin, 5, acceptAt)
		})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("AcceptBreak() = %v, want the error of the context", err)
		}
		if got := storeFiles(f); !maps.Equal(got, files) {
			t.Error("the files of the store changed")
		}
		kept(t, before, viewOf(t, f))
	})
}
