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

// holeAt is the step of a store that puts the row of the hole of the records first to last of log, whose last has the hash
// hash.
func holeAt(log string, first, last uint64, hash [sha256.Size]byte) func(t *testing.T, f *fixture) {
	return putRow(dbwire.TableChains, dbwire.ChainHoleKey(log, first),
		dbwire.AppendChainHole(nil, dbwire.ChainHole{Last: last, Hash: hash, At: pruneNow}))
}

// holeText is the refusal of the record n, which comes after the hole of the records first to last.
func holeText(n, first, last int) string {
	if first == last {
		return fmt.Sprintf("record %d comes after the record %d that whiteroom-db repair dropped: there is no break to accept", n, first)
	}
	return fmt.Sprintf("record %d comes after the records %d to %d that whiteroom-db repair dropped: there is no break to accept", n,
		first, last)
}

// gapAt is the step of a store that puts the row of the accepted gap of the records first to last of log, whose last has the
// hash hash.
func gapAt(log string, first, last uint64, hash [sha256.Size]byte) func(t *testing.T, f *fixture) {
	return putRow(dbwire.TableChains, dbwire.ChainGapKey(log, first),
		dbwire.AppendChainHole(nil, dbwire.ChainHole{Last: last, Hash: hash, At: pruneNow}))
}

// headAt is the step of a store that puts the head of log in the table heads: the record n, with the hash hash.
func headAt(log string, n uint64, hash [sha256.Size]byte) func(t *testing.T, f *fixture) {
	return putRow(dbwire.TableHeads, []byte(log), fmt.Appendf(nil, "audit head %s %d sha256:%x", log, n, hash))
}

// lostRecords is "the record A" or "the records A to B", as the refusals tell what a row has.
func lostRecords(first, last int) string {
	if first == last {
		return fmt.Sprintf("the record %d", first)
	}
	return fmt.Sprintf("the records %d to %d", first, last)
}

// acceptedText is the refusal of the record n, which comes after the gap of the records first to last that is accepted already.
func acceptedText(n, first, last int) string {
	return fmt.Sprintf("record %d comes after %s whose gap is accepted already", n, lostRecords(first, last))
}

// stepsOf is the step of a store that does each of steps, in order.
func stepsOf(steps ...func(t *testing.T, f *fixture)) func(t *testing.T, f *fixture) {
	return func(t *testing.T, f *fixture) {
		for _, step := range steps {
			step(t, f)
		}
	}
}

// belowText is the refusal of the record n, which comes after the record before, whose number is above it.
func belowText(n, before int) string {
	return fmt.Sprintf("record %d does not follow record %d in number: it is below that record, which is neither a break nor a gap "+
		"that can be accepted", n, before)
}

// noHashText is the refusal of the gap before the record n, whose prev is no hash, which has the record before after it.
func noHashText(n, before int) string {
	return fmt.Sprintf("record %d does not follow record %d in number, and its prev is not the hash of a record (sha256: and 64 lower "+
		"case hexadecimal digits), so the gap before it cannot be accepted", n, before)
}

// leftoversText is the refusal of the record n, the first that remains after a deletion, while records before it that the deletion
// left are in the store.
func leftoversText(n int) string {
	return fmt.Sprintf("record %d comes after records before it that an earlier deletion left, which are still in the store: "+
		"whiteroom-db compact drops them if they are what the deletion left; accept the break at record %d then", n, n)
}

// startHashText is the refusal of the record n, the first that remains after a deletion, which has the hash that the chain
// start has of the record before it as its prev.
func startHashText(n int) string {
	return fmt.Sprintf("record %d has as its prev the hash that the chain start has of record %d: there is no break to accept", n, n-1)
}

// noPrevText is the refusal of the record 1, which has no prev, as the first record of a log has none.
const noPrevText = "record 1 has no prev, as the first record of a log has none: there is no break to accept"

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
	_, err := accept(t, f, log, n)
	return err
}

// accept is acceptBreak that also returns what it accepted.
func accept(t *testing.T, f *fixture, log string, n uint64) (acc Acceptance, err error) {
	t.Helper()
	err = maintain(t.Context(), f.fs, "data", f.opts, func(m *logdb.Maintenance) (err error) {
		acc, err = AcceptBreak(t.Context(), m, log, n, acceptAt)
		return err
	})
	return acc, err
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

// checkGapAccepted checks that the store of f is what before was, with the acceptance of the gap of the records first to last of
// log, whose last has the hash sum: one row of the table chains, and, at the end, the event of the acceptance and a mark in one
// commit.
func checkGapAccepted(t *testing.T, f *fixture, before view, log string, first, last uint64, sum [sha256.Size]byte) {
	t.Helper()
	after := viewOf(t, f)
	kept(t, before, after)
	checkRows(t, before, after)
	if want := strings.TrimPrefix(before.shape+fmt.Sprintf(" G%d M", first), " "); after.shape != want {
		t.Errorf("the store is %q, want %q", after.shape, want)
	}
	if after.breaks != before.breaks || after.holes != before.holes {
		t.Errorf("the table chains has %d rows of breaks and %d of holes, were %d and %d", after.breaks, after.holes, before.breaks,
			before.holes)
	}
	if len(after.events) < 2 {
		return
	}
	mark, event := after.events[len(after.events)-1], after.events[len(after.events)-2]
	if at, ok := dbwire.ParseMark(mark.Data); !ok || !at.Equal(acceptAt) {
		t.Errorf("the last event is %q, want the mark of the time of the acceptance", mark.Data)
	}
	want := dbwire.Hole{Log: log, First: first, Last: last, Hash: sum}
	if got, ok := dbwire.ParseGap(event.Data); !ok || got != want {
		t.Errorf("the event of the acceptance is %+v (%v), want %+v", got, ok, want)
	}
	if event.Seq != mark.Seq {
		t.Error("the event of the acceptance is not in the commit of the mark")
	}
	row := chainTable(t, f)[string(dbwire.ChainGapKey(log, first))]
	if g, ok := dbwire.ParseChainHole(first, []byte(row)); !ok || g.Last != last || g.Hash != sum || !g.At.Equal(acceptAt) {
		t.Errorf("the row of the gap is %+v (%v), want the records %d to %d with the hash %x at %v", g, ok, first, last, sum[:4], acceptAt)
	}
}

// TestAcceptGap checks the acceptance of a gap in each place of each log: the row, the event and the mark are written in one
// commit that is durable, nothing else changes, and what is accepted is told.
func TestAcceptGap(t *testing.T) {
	chain, activity := newChain(dbwire.LogLogin, 8), newChain(dbwire.LogActivity, 8)
	login := func(ns ...int) [][]byte {
		var out [][]byte
		for _, n := range ns {
			out = append(out, chain.rec(n))
		}
		return out
	}
	act := func(ns ...int) [][]byte {
		var out [][]byte
		for _, n := range ns {
			out = append(out, activity.rec(n))
		}
		return out
	}
	both := func(step ...func(t *testing.T, f *fixture)) func(t *testing.T, f *fixture) {
		return func(t *testing.T, f *fixture) {
			for _, s := range step {
				s(t, f)
			}
		}
	}
	const L, A = dbwire.LogLogin, dbwire.LogActivity
	for _, tt := range []struct {
		name   string
		events [][]byte
		step   func(t *testing.T, f *fixture)
		log    string
		n      uint64
		want   Acceptance
		sum    [sha256.Size]byte
		// rows is how many rows of the table chains the store has afterwards, with those that it had.
		rows int
	}{
		{"one record in the middle", login(1, 2, 4, 5), nil, L, 4, Acceptance{Gap: true, First: 3, Last: 3}, chain.sum(3), 1},
		{"two records in the middle", login(1, 2, 5, 6), nil, L, 5, Acceptance{Gap: true, First: 3, Last: 4}, chain.sum(4), 1},
		{"one record in the middle of the activity log", act(1, 2, 4, 5), nil, A, 4, Acceptance{Gap: true, First: 3, Last: 3}, activity.sum(3), 1},
		{"the first records of the login history", login(3, 4), nil, L, 3, Acceptance{Gap: true, First: 1, Last: 2}, chain.sum(2), 1},
		{"the first record of the login history", login(2, 3), nil, L, 2, Acceptance{Gap: true, First: 1, Last: 1}, chain.sum(1), 1},
		{"the first records of the activity log", act(4, 5), nil, A, 4, Acceptance{Gap: true, First: 1, Last: 3}, activity.sum(3), 1},
		{"the records after the chain start", login(5, 6), startAt(3, chain.sum(2)), L, 5, Acceptance{Gap: true, First: 3, Last: 4},
			chain.sum(4), 2},
		{"the records after the records that a deletion left", login(1, 2, 5, 6), startAt(5, chain.sum(4)), L, 5,
			Acceptance{Gap: true, First: 3, Last: 4}, chain.sum(4), 2},
		{"a record after the record that follows the chain start", login(3, 5), startAt(3, chain.sum(2)), L, 5,
			Acceptance{Gap: true, First: 4, Last: 4}, chain.sum(4), 2},
		{"a record when the other log has a gap too", login(1, 2, 4), gapAt(A, 3, 3, activity.sum(3)), L, 4,
			Acceptance{Gap: true, First: 3, Last: 3}, chain.sum(3), 2},
		{"a record when a hole of another place is there", login(1, 2, 4), holeAt(L, 7, 7, chain.sum(7)), L, 4,
			Acceptance{Gap: true, First: 3, Last: 3}, chain.sum(3), 2},
		{"a record that a hole that is narrower does not pass: the gap starts after it", login(1, 2, 5), holeAt(L, 3, 3, chain.sum(3)), L, 5,
			Acceptance{Gap: true, First: 4, Last: 4}, chain.sum(4), 2},
		{"a record that a gap that is narrower does not pass: the gap starts after it", login(1, 2, 5), gapAt(L, 3, 3, chain.sum(3)), L, 5,
			Acceptance{Gap: true, First: 4, Last: 4}, chain.sum(4), 2},
		{"a record that a hole with another hash does not pass: accepted again, beside the hole", login(1, 2, 4), holeAt(L, 3, 3, wrong32), L, 4,
			Acceptance{Gap: true, First: 3, Last: 3}, chain.sum(3), 2},
		{"a record that a gap with another hash does not pass: accepted again, replacing the row", login(1, 2, 4), gapAt(L, 3, 3, wrong32), L, 4,
			Acceptance{Gap: true, First: 3, Last: 3}, chain.sum(3), 1},
		{"a record that a hole and a gap with another hash do not pass: the gap is replaced", login(1, 2, 4),
			both(holeAt(L, 3, 3, wrong32), gapAt(L, 3, 3, wrong32)), L, 4, Acceptance{Gap: true, First: 3, Last: 3}, chain.sum(3), 2},
		{"a record that a gap of two records with another hash does not pass", login(1, 2, 5), gapAt(L, 3, 4, wrong32), L, 5,
			Acceptance{Gap: true, First: 3, Last: 4}, chain.sum(4), 1},
		{"a record that two rows do not pass, the second a gap with another hash: it is accepted again", login(1, 2, 5),
			both(holeAt(L, 3, 3, chain.sum(3)), gapAt(L, 4, 4, wrong32)), L, 5, Acceptance{Gap: true, First: 4, Last: 4}, chain.sum(4), 2},
		{"a record that two rows do not pass, the second a hole with another hash: the gap is added", login(1, 2, 5),
			both(gapAt(L, 3, 3, chain.sum(3)), holeAt(L, 4, 4, wrong32)), L, 5, Acceptance{Gap: true, First: 4, Last: 4}, chain.sum(4), 3},
		{"the first records of a log, when a gap with another hash is there", login(3, 4), gapAt(L, 1, 2, wrong32), L, 3,
			Acceptance{Gap: true, First: 1, Last: 2}, chain.sum(2), 1},
		{"a record that two rows that are narrower do not pass", login(1, 2, 7), both(holeAt(L, 3, 3, chain.sum(3)), gapAt(L, 4, 5, chain.sum(5))),
			L, 7, Acceptance{Gap: true, First: 6, Last: 6}, chain.sum(6), 3},
		{"a record that a hole that is narrower does not pass, and a gap that has the same first", login(1, 2, 6),
			both(holeAt(L, 3, 3, chain.sum(3)), gapAt(L, 3, 4, chain.sum(4))), L, 6, Acceptance{Gap: true, First: 5, Last: 5}, chain.sum(5), 3},
		{"a record that a gap of the other log does not pass", login(1, 2, 4), gapAt(A, 3, 3, chain.sum(3)), L, 4,
			Acceptance{Gap: true, First: 3, Last: 3}, chain.sum(3), 2},
		{"a record after a gap that is accepted for the records before it", login(1, 2, 4, 6), gapAt(L, 3, 3, chain.sum(3)), L, 6,
			Acceptance{Gap: true, First: 5, Last: 5}, chain.sum(5), 2},
		{"a gap that holds the chain start", login(1, 2, 7, 8), startAt(5, chain.sum(4)), L, 7,
			Acceptance{Gap: true, First: 3, Last: 6}, chain.sum(6), 2},
		{"a gap that is the chain start", login(1, 2, 3, 4, 7, 8), startAt(5, chain.sum(4)), L, 7,
			Acceptance{Gap: true, First: 5, Last: 6}, chain.sum(6), 2},
		{"a record that a break is accepted for", login(1, 2, 4, 5), putRow(dbwire.TableChains,
			dbwire.ChainBreakKey(dbwire.LogLogin, 4), dbwire.AppendChainBreak(nil, dbwire.ChainBreak{Hash: chain.sum(4), At: pruneNow})),
			L, 4, Acceptance{Gap: true, First: 3, Last: 3}, chain.sum(3), 2},

		// A gap at the end is at the number of the head.
		{"one record at the end", login(1, 2, 3), headAt(L, 4, chain.sum(4)), L, 4,
			Acceptance{Gap: true, End: true, First: 4, Last: 4}, chain.sum(4), 1},
		{"two records at the end", login(1, 2, 3), headAt(L, 5, chain.sum(5)), L, 5,
			Acceptance{Gap: true, End: true, First: 4, Last: 5}, chain.sum(5), 1},
		{"two records at the end of the activity log", act(1, 2, 3), headAt(A, 5, activity.sum(5)), A, 5,
			Acceptance{Gap: true, End: true, First: 4, Last: 5}, activity.sum(5), 1},
		{"the records of a log that has none", nil, headAt(L, 3, chain.sum(3)), L, 3,
			Acceptance{Gap: true, End: true, NoRecord: true, First: 1, Last: 3}, chain.sum(3), 1},
		{"the records after the chain start, when the log has none", nil, both(startAt(4, chain.sum(3)), headAt(L, 5, chain.sum(5))), L, 5,
			Acceptance{Gap: true, End: true, NoRecord: true, First: 4, Last: 5}, chain.sum(5), 2},
		{"the records after the records that a deletion left, when the log has no other", login(1, 2, 3), both(startAt(4, chain.sum(3)),
			headAt(L, 5, chain.sum(5))), L, 5, Acceptance{Gap: true, End: true, First: 4, Last: 5}, chain.sum(5), 2},
		{"the records at the end that hold the chain start", login(1, 2), both(startAt(5, chain.sum(4)), headAt(L, 7, chain.sum(7))), L, 7,
			Acceptance{Gap: true, End: true, First: 3, Last: 7}, chain.sum(7), 2},
		{"the records at the end that hold the chain start, when no record is before it", nil, both(startAt(5, chain.sum(4)),
			headAt(L, 7, chain.sum(7))), L, 7, Acceptance{Gap: true, End: true, NoRecord: true, First: 5, Last: 7}, chain.sum(7), 2},
		{"the records at the end, when a hole is in the middle", login(1, 2, 4), both(holeAt(L, 3, 3, chain.sum(3)),
			headAt(L, 5, chain.sum(5))), L, 5, Acceptance{Gap: true, End: true, First: 5, Last: 5}, chain.sum(5), 2},
		{"the records at the end, when a hole that does not end with the head is there", login(1, 2, 3), both(holeAt(L, 4, 4, chain.sum(4)),
			headAt(L, 5, chain.sum(5))), L, 5, Acceptance{Gap: true, End: true, First: 5, Last: 5}, chain.sum(5), 2},
		{"the records at the end, when a gap that does not end with the head is there", login(1, 2, 3), both(gapAt(L, 4, 4, chain.sum(4)),
			headAt(L, 6, chain.sum(6))), L, 6, Acceptance{Gap: true, End: true, First: 5, Last: 6}, chain.sum(6), 2},
		{"the records at the end, when a hole that ends with the head has another hash: accepted again, beside the hole", login(1, 2, 3),
			both(holeAt(L, 4, 5, wrong32), headAt(L, 5, chain.sum(5))), L, 5, Acceptance{Gap: true, End: true, First: 4, Last: 5}, chain.sum(5), 2},
		{"the records at the end, when a gap that ends with the head has another hash: accepted again, replacing the row", login(1, 2, 3),
			both(gapAt(L, 4, 5, wrong32), headAt(L, 5, chain.sum(5))), L, 5, Acceptance{Gap: true, End: true, First: 4, Last: 5}, chain.sum(5), 1},
		{"the records at the end, when the second of two rows ends with the head and has another hash", login(1, 2),
			both(holeAt(L, 3, 3, chain.sum(3)), gapAt(L, 4, 5, wrong32), headAt(L, 5, chain.sum(5))), L, 5,
			Acceptance{Gap: true, End: true, First: 4, Last: 5}, chain.sum(5), 2},
		{"the records at the end, when two rows that do not end with the head are there", login(1, 2), both(holeAt(L, 3, 3, chain.sum(3)),
			gapAt(L, 4, 4, chain.sum(4)), headAt(L, 6, chain.sum(6))), L, 6, Acceptance{Gap: true, End: true, First: 5, Last: 6}, chain.sum(6), 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var steps []func(t *testing.T, f *fixture)
			if tt.step != nil {
				steps = append(steps, tt.step)
			}
			f := recordsFixture(t, tt.events, steps...)
			before := viewOf(t, f)
			acc, err := accept(t, f, tt.log, tt.n)
			if err != nil || acc != tt.want {
				t.Fatalf("AcceptBreak() = %+v, %v, want %+v", acc, err, tt.want)
			}
			checkGapAccepted(t, f, before, tt.log, tt.want.First, tt.want.Last, tt.sum)
			if got := len(chainTable(t, f)); got != tt.rows {
				t.Errorf("the table chains has %d rows, want %d", got, tt.rows)
			}

			// The commit is durable: a crash that loses what the disk has not got keeps it.
			image := f.fs.Crash(loseAll)
			checkGapAccepted(t, &fixture{t: t, fs: image, opts: f.opts, logs: f.logs, clock: f.clock}, before, tt.log, tt.want.First,
				tt.want.Last, tt.sum)

			// A second acceptance of it is refused, and writes nothing.
			accepted, files := viewOf(t, f), storeFiles(f)
			if _, err := accept(t, f, tt.log, tt.n); err == nil {
				t.Error("a second AcceptBreak() accepts it again")
			}
			if got := storeFiles(f); !maps.Equal(got, files) {
				t.Error("the second acceptance changed the files of the store")
			}
			kept(t, accepted, viewOf(t, f))
		})
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

// TestAcceptBreakFirst checks the acceptance of a break at the first record that is read, when it is the one that the log starts
// with: record 1 with a prev, which the first record of a log has not, and the first record that remains after a deletion (K),
// whose prev is not the hash that the chain start has, with no record before it in the store. The row, the event and the mark are
// those of any break, and a second acceptance is refused.
func TestAcceptBreakFirst(t *testing.T) {
	chain, activity := newChain(dbwire.LogLogin, 5), newChain(dbwire.LogActivity, 5)
	loginFirst := auditRecord("login", 1, hashText(wrong32))
	activityFirst := auditRecord("activity", 1, hashText(wrong32))
	for _, tt := range []struct {
		name   string
		events [][]byte
		step   func(t *testing.T, f *fixture)
		log    string
		n      uint64
		sum    [sha256.Size]byte
	}{
		{"record 1", [][]byte{loginFirst, chain.rec(2), chain.rec(3)}, nil, dbwire.LogLogin, 1, sha256.Sum256(loginFirst)},
		{"record 1 of the activity log", [][]byte{activityFirst, activity.rec(2)}, nil, dbwire.LogActivity, 1, sha256.Sum256(activityFirst)},
		{"record 1, among the records of the other log", [][]byte{activity.rec(1), loginFirst, activity.rec(2), chain.rec(2)}, nil,
			dbwire.LogLogin, 1, sha256.Sum256(loginFirst)},
		{"the first record that remains", [][]byte{chain.rec(3), chain.rec(4)}, startAt(3, wrong32), dbwire.LogLogin, 3, chain.sum(3)},
		{"the first record that remains, whose prev is not a hash", [][]byte{auditRecord("login", 3, "nonsense"), chain.rec(4)},
			startAt(3, chain.sum(2)), dbwire.LogLogin, 3, sha256.Sum256(auditRecord("login", 3, "nonsense"))},
		{"the first record that remains, with no prev", [][]byte{auditRecord("login", 3, ""), chain.rec(4)}, startAt(3, chain.sum(2)),
			dbwire.LogLogin, 3, sha256.Sum256(auditRecord("login", 3, ""))},
		{"the first record that remains, among the records of the other log", [][]byte{activity.rec(1), chain.rec(3), activity.rec(2),
			chain.rec(4)}, startAt(3, wrong32), dbwire.LogLogin, 3, chain.sum(3)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var steps []func(t *testing.T, f *fixture)
			if tt.step != nil {
				steps = append(steps, tt.step)
			}
			f := recordsFixture(t, tt.events, steps...)
			before := viewOf(t, f)
			acc, err := accept(t, f, tt.log, tt.n)
			if err != nil || acc != (Acceptance{}) {
				t.Fatalf("AcceptBreak() = %+v, %v, want the break", acc, err)
			}
			checkAccepted(t, f, before, tt.log, tt.n, tt.sum)

			// The commit is durable.
			image := f.fs.Crash(loseAll)
			checkAccepted(t, &fixture{t: t, fs: image, opts: f.opts, logs: f.logs, clock: f.clock}, before, tt.log, tt.n, tt.sum)

			files := storeFiles(f)
			want := fmt.Sprintf("the break at record %d of the audit log %s is already accepted", tt.n, tt.log)
			if err := acceptBreak(t, f, tt.log, tt.n); err == nil || err.Error() != want {
				t.Errorf("a second AcceptBreak() = %v, want %q", err, want)
			}
			if got := storeFiles(f); !maps.Equal(got, files) {
				t.Error("the second acceptance changed the files of the store")
			}
		})
	}
}

// TestAcceptBreakRefused checks that each refusal says why, in the words of the command, and writes nothing.
func TestAcceptBreakRefused(t *testing.T) {
	chain, activity := newChain(dbwire.LogLogin, 8), newChain(dbwire.LogActivity, 6)
	good := [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), chain.rec(4), chain.rec(5), chain.rec(6)}
	withBreak := slices.Clone(good)
	withBreak[3] = changedRecord(dbwire.LogLogin, 4, chain) // the record 5 is the break
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
		{"the first record", good, nil, dbwire.LogLogin, 1, noPrevText},
		{"the first record of the activity log", [][]byte{activity.rec(1), activity.rec(2)}, nil, dbwire.LogActivity, 1, noPrevText},
		{"the first record that remains, with the hash of the chain start", good[2:], startAt(3, chain.sum(2)), dbwire.LogLogin, 3,
			startHashText(3)},
		{"the first record that remains, with the hash of the chain start and the records that a deletion left before it", good,
			startAt(3, chain.sum(2)), dbwire.LogLogin, 3, startHashText(3)},
		{"the first record that remains, with the records that a deletion left before it", good, startAt(3, wrong32), dbwire.LogLogin, 3,
			leftoversText(3)},
		{"the first record that remains, with the records that a deletion left before it, and a break that is accepted already", good,
			func(t *testing.T, f *fixture) {
				startAt(3, wrong32)(t, f)
				putRow(dbwire.TableChains, dbwire.ChainBreakKey(dbwire.LogLogin, 3),
					dbwire.AppendChainBreak(nil, dbwire.ChainBreak{Hash: chain.sum(3), At: pruneNow}))(t, f)
			}, dbwire.LogLogin, 3, leftoversText(3)},
		{"a record that follows", good, nil, dbwire.LogLogin, 3, "record 3 follows record 2: there is no break to accept"},
		{"a record whose number goes back", [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), chain.rec(5), chain.rec(4)}, nil,
			dbwire.LogLogin, 4, belowText(4, 5)},
		{"a record whose number skips, with no hash as its prev", [][]byte{chain.rec(1), chain.rec(2), auditRecord("login", 4, "nonsense")},
			nil, dbwire.LogLogin, 4, noHashText(4, 2)},
		{"a record whose number skips, with the hash in upper case", [][]byte{chain.rec(1), chain.rec(2),
			auditRecord("login", 4, "sha256:"+strings.ToUpper(fmt.Sprintf("%x", chain.sum(3))))}, nil, dbwire.LogLogin, 4, noHashText(4, 2)},
		{"a record whose number skips, with a hash that is too short", [][]byte{chain.rec(1), chain.rec(2),
			auditRecord("login", 4, hashText(chain.sum(3))[:20])}, nil, dbwire.LogLogin, 4, noHashText(4, 2)},
		{"a record whose number skips, with no prev", [][]byte{chain.rec(1), chain.rec(2), auditRecord("login", 4, "")}, nil,
			dbwire.LogLogin, 4, noHashText(4, 2)},
		{"the first record, which is not record 1, with no hash as its prev", [][]byte{auditRecord("login", 3, ""), chain.rec(4)}, nil,
			dbwire.LogLogin, 3, "record 3 is not record 1, which the audit log login starts with, and its prev is not the hash of a " +
				"record (sha256: and 64 lower case hexadecimal digits), so the gap before it cannot be accepted"},
		{"the first record after the chain start, which is not it, with no hash as its prev", [][]byte{auditRecord("login", 5, "")},
			startAt(3, chain.sum(2)), dbwire.LogLogin, 5, "record 5 is not record 3, which the audit log login starts with, and its prev " +
				"is not the hash of a record (sha256: and 64 lower case hexadecimal digits), so the gap before it cannot be accepted"},
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
		{"a record after a hole", [][]byte{chain.rec(1), chain.rec(2), chain.rec(4)}, holeAt(dbwire.LogLogin, 3, 3, chain.sum(3)),
			dbwire.LogLogin, 4, holeText(4, 3, 3)},
		{"a record after a hole of two records", [][]byte{chain.rec(1), chain.rec(2), chain.rec(5)},
			holeAt(dbwire.LogLogin, 3, 4, chain.sum(4)), dbwire.LogLogin, 5, holeText(5, 3, 4)},
		{"a record after a gap that is accepted", [][]byte{chain.rec(1), chain.rec(2), chain.rec(4)},
			gapAt(dbwire.LogLogin, 3, 3, chain.sum(3)), dbwire.LogLogin, 4, acceptedText(4, 3, 3)},
		{"a record after a gap of two records that is accepted", [][]byte{chain.rec(1), chain.rec(2), chain.rec(5)},
			gapAt(dbwire.LogLogin, 3, 4, chain.sum(4)), dbwire.LogLogin, 5, acceptedText(5, 3, 4)},
		{"a record after a gap that is accepted, in the activity log", [][]byte{activity.rec(1), activity.rec(3)},
			gapAt(dbwire.LogActivity, 2, 2, activity.sum(2)), dbwire.LogActivity, 3, acceptedText(3, 2, 2)},
		{"the first record after a gap that is accepted at the start", [][]byte{chain.rec(3), chain.rec(4)},
			gapAt(dbwire.LogLogin, 1, 2, chain.sum(2)), dbwire.LogLogin, 3, acceptedText(3, 1, 2)},
		{"the first record after a gap that is accepted at the chain start", [][]byte{chain.rec(5), chain.rec(6)}, func(t *testing.T, f *fixture) {
			startAt(3, chain.sum(2))(t, f)
			gapAt(dbwire.LogLogin, 3, 4, chain.sum(4))(t, f)
		}, dbwire.LogLogin, 5, acceptedText(5, 3, 4)},
		{"a record after a hole and a gap that are both there", [][]byte{chain.rec(1), chain.rec(2), chain.rec(4)}, func(t *testing.T, f *fixture) {
			holeAt(dbwire.LogLogin, 3, 3, chain.sum(3))(t, f)
			gapAt(dbwire.LogLogin, 3, 3, chain.sum(3))(t, f)
		}, dbwire.LogLogin, 4, holeText(4, 3, 3)},
		{"a record that follows the one after a gap that is accepted", [][]byte{chain.rec(1), chain.rec(2), chain.rec(4), chain.rec(5)},
			gapAt(dbwire.LogLogin, 3, 3, chain.sum(3)), dbwire.LogLogin, 5, "record 5 follows record 4: there is no break to accept"},
		{"the gap at the end that is accepted", [][]byte{chain.rec(1), chain.rec(2), chain.rec(3)}, func(t *testing.T, f *fixture) {
			headAt(dbwire.LogLogin, 5, chain.sum(5))(t, f)
			gapAt(dbwire.LogLogin, 4, 5, chain.sum(5))(t, f)
		}, dbwire.LogLogin, 5, "the gap of the records 4 to 5 at the end of the audit log login is accepted already"},
		{"the gap of one record at the end that is accepted", [][]byte{chain.rec(1), chain.rec(2), chain.rec(3)}, func(t *testing.T, f *fixture) {
			headAt(dbwire.LogLogin, 4, chain.sum(4))(t, f)
			gapAt(dbwire.LogLogin, 4, 4, chain.sum(4))(t, f)
		}, dbwire.LogLogin, 4, "the gap of the record 4 at the end of the audit log login is accepted already"},
		{"the hole at the end of the log", [][]byte{chain.rec(1), chain.rec(2), chain.rec(3)}, func(t *testing.T, f *fixture) {
			headAt(dbwire.LogLogin, 5, chain.sum(5))(t, f)
			holeAt(dbwire.LogLogin, 4, 5, chain.sum(5))(t, f)
		}, dbwire.LogLogin, 5, "record 5 of the audit log login is not in the store"},
		{"the head of a log that has records above it", [][]byte{chain.rec(1), chain.rec(2), chain.rec(3)}, headAt(dbwire.LogLogin, 2, chain.sum(2)),
			dbwire.LogLogin, 2, "record 2 follows record 1: there is no break to accept"},
		{"a record above the head", [][]byte{chain.rec(1), chain.rec(2), chain.rec(3)}, headAt(dbwire.LogLogin, 3, chain.sum(3)),
			dbwire.LogLogin, 4, "record 4 of the audit log login is not in the store"},
		{"a record below the head, which is not there", [][]byte{chain.rec(1), chain.rec(2), chain.rec(3)},
			headAt(dbwire.LogLogin, 5, chain.sum(5)), dbwire.LogLogin, 4, "record 4 of the audit log login is not in the store"},
		{"a record when the head is the one of the other log", [][]byte{chain.rec(1), chain.rec(2), chain.rec(3)},
			headAt(dbwire.LogActivity, 4, activity.sum(4)), dbwire.LogLogin, 4, "record 4 of the audit log login is not in the store"},
		{"the head with no hash that can be read", [][]byte{chain.rec(1), chain.rec(2), chain.rec(3)},
			putRow(dbwire.TableHeads, []byte("login"), []byte("audit head login 4 sha256:abc")), dbwire.LogLogin, 4,
			"record 4 of the audit log login is not in the store"},
		{"the head of a record that the log has below the last that was read", [][]byte{chain.rec(1), chain.rec(2), chain.rec(3)},
			headAt(dbwire.LogLogin, 2, chain.sum(2)), dbwire.LogLogin, 4, "record 4 of the audit log login is not in the store"},
		{"a record that a gap leaves out", [][]byte{chain.rec(1), chain.rec(2), chain.rec(4), chain.rec(5)}, nil, dbwire.LogLogin, 3,
			"record 3 of the audit log login is not in the store: it is in the gap of record 3 before record 4; accept the gap with -record 4"},
		{"a record that a gap of two records leaves out", [][]byte{chain.rec(1), chain.rec(2), chain.rec(5)}, nil, dbwire.LogLogin, 4,
			"record 4 of the audit log login is not in the store: it is in the gap of the records 3 to 4 before record 5; accept the gap " +
				"with -record 5"},
		{"the first record that a gap leaves out", [][]byte{chain.rec(3), chain.rec(4)}, nil, dbwire.LogLogin, 2,
			"record 2 of the audit log login is not in the store: it is in the gap of the records 1 to 2 before record 3; accept the gap " +
				"with -record 3"},
		{"a record that a gap leaves out, after the chain start", [][]byte{chain.rec(3), chain.rec(6)}, startAt(3, chain.sum(2)),
			dbwire.LogLogin, 5, "record 5 of the audit log login is not in the store: it is in the gap of the records 4 to 5 before record 6; " +
				"accept the gap with -record 6"},
		{"a record that a hole leaves out", [][]byte{chain.rec(1), chain.rec(2), chain.rec(4)}, holeAt(dbwire.LogLogin, 3, 3, chain.sum(3)),
			dbwire.LogLogin, 3, "record 3 of the audit log login is not in the store"},
		{"a record that a hole leaves out, before a gap", [][]byte{chain.rec(1), chain.rec(2), chain.rec(5)}, holeAt(dbwire.LogLogin, 3, 3, chain.sum(3)),
			dbwire.LogLogin, 3, "record 3 of the audit log login is not in the store"},
		{"a record that the gap after a hole leaves out", [][]byte{chain.rec(1), chain.rec(2), chain.rec(5)}, holeAt(dbwire.LogLogin, 3, 3, chain.sum(3)),
			dbwire.LogLogin, 4, "record 4 of the audit log login is not in the store: it is in the gap of record 4 before record 5; accept the gap " +
				"with -record 5"},
		{"a record after a hole that is wider", [][]byte{chain.rec(1), chain.rec(2), chain.rec(4)}, holeAt(dbwire.LogLogin, 3, 4, chain.sum(4)),
			dbwire.LogLogin, 4, "the gap of record 3 before record 4 would lie over the hole of the records 3 to 4: no gap is accepted over a " +
				"row that is there"},
		{"a record after a gap that is wider", [][]byte{chain.rec(1), chain.rec(2), chain.rec(4)}, gapAt(dbwire.LogLogin, 3, 4, chain.sum(4)),
			dbwire.LogLogin, 4, "the gap of record 3 before record 4 would lie over the accepted gap of the records 3 to 4: no gap is accepted " +
				"over a row that is there"},
		{"a record after a hole, when another hole lies in the gap", [][]byte{chain.rec(1), chain.rec(2),
			chain.rec(6)}, stepsOf(holeAt(dbwire.LogLogin, 3, 3, chain.sum(3)), holeAt(dbwire.LogLogin, 5, 5, chain.sum(5))), dbwire.LogLogin, 6,
			"the gap of the records 4 to 5 before record 6 would lie over the hole of record 5: no gap is accepted over a row that is there"},
		{"a record after a hole with another hash, across the chain start with another hash", [][]byte{chain.rec(1), chain.rec(2), chain.rec(4)},
			stepsOf(startAt(4, wrong32), holeAt(dbwire.LogLogin, 3, 3, wrong32)), dbwire.LogLogin, 4,
			"the gap of record 3 before record 4 is not accepted: record 4 does not have as its prev the hash that the chain start has of " +
				"record 3"},
		{"a record after a hole with another hash, and a gap at the same place that is wider", [][]byte{chain.rec(1), chain.rec(2), chain.rec(4)},
			stepsOf(holeAt(dbwire.LogLogin, 3, 3, wrong32), gapAt(dbwire.LogLogin, 3, 4, chain.sum(4))), dbwire.LogLogin, 4,
			"the gap of record 3 before record 4 would lie over the accepted gap of the records 3 to 4: no gap is accepted over a row that is " +
				"there"},
		{"a record after a gap with another hash, and a hole inside the gap's end", [][]byte{chain.rec(1), chain.rec(2), chain.rec(5)},
			stepsOf(gapAt(dbwire.LogLogin, 3, 4, wrong32), holeAt(dbwire.LogLogin, 4, 4, chain.sum(4))), dbwire.LogLogin, 5,
			"the gap of the records 3 to 4 before record 5 would lie over the hole of record 4: no gap is accepted over a row that is there"},
		{"a gap at the end that a row that is wider lies over", [][]byte{chain.rec(1), chain.rec(2), chain.rec(3)},
			stepsOf(headAt(dbwire.LogLogin, 5, chain.sum(5)), gapAt(dbwire.LogLogin, 4, 6, chain.sum(6))), dbwire.LogLogin, 5,
			"the gap of the records 4 to 5 at the end of the audit log login would lie over the accepted gap of the records 4 to 6: no gap is " +
				"accepted over a row that is there"},
		{"a gap after a row that ends with the record before the chain start, with another hash", [][]byte{chain.rec(1), chain.rec(2),
			chain.rec(7)}, stepsOf(startAt(5, chain.sum(4)), holeAt(dbwire.LogLogin, 3, 4, wrong32)), dbwire.LogLogin, 7,
			"the gap of the records 5 to 6 before record 7 is not accepted: the hole of the records 3 to 4 does not have the hash that the " +
				"chain start has of record 4"},
		{"a gap at the end after a row that ends with the record before the chain start, with another hash", [][]byte{chain.rec(1), chain.rec(2)},
			stepsOf(startAt(5, chain.sum(4)), holeAt(dbwire.LogLogin, 3, 4, wrong32), headAt(dbwire.LogLogin, 7, chain.sum(7))), dbwire.LogLogin, 7,
			"the gap at the end of the audit log login is not accepted: the hole of the records 3 to 4 does not have the hash that the chain " +
				"start has of record 4"},
		{"a record that a gap that is accepted leaves out", [][]byte{chain.rec(1), chain.rec(2), chain.rec(4)},
			gapAt(dbwire.LogLogin, 3, 3, chain.sum(3)), dbwire.LogLogin, 3, "record 3 of the audit log login is not in the store"},
		{"a record that a gap leaves out, before the record that follows another", [][]byte{chain.rec(1), chain.rec(2), chain.rec(5),
			chain.rec(6)}, nil, dbwire.LogLogin, 3, "record 3 of the audit log login is not in the store: it is in the gap of the records 3 " +
			"to 4 before record 5; accept the gap with -record 5"},
		{"the first record after a hole at the start", [][]byte{chain.rec(2), chain.rec(3)}, holeAt(dbwire.LogLogin, 1, 1, chain.sum(1)),
			dbwire.LogLogin, 2, holeText(2, 1, 1)},
		{"the first record after a hole at the chain start", [][]byte{chain.rec(5), chain.rec(6)}, func(t *testing.T, f *fixture) {
			startAt(3, chain.sum(2))(t, f)
			holeAt(dbwire.LogLogin, 3, 4, chain.sum(4))(t, f)
		}, dbwire.LogLogin, 5, holeText(5, 3, 4)},
		{"a record after a hole, in the activity log", [][]byte{activity.rec(1), activity.rec(3)},
			holeAt(dbwire.LogActivity, 2, 2, activity.sum(2)), dbwire.LogActivity, 3, holeText(3, 2, 2)},
		{"a record that follows the one after a hole", [][]byte{chain.rec(1), chain.rec(2), chain.rec(4), chain.rec(5)},
			holeAt(dbwire.LogLogin, 3, 3, chain.sum(3)), dbwire.LogLogin, 5, "record 5 follows record 4: there is no break to accept"},
		{"a row of an accepted gap that cannot be read", withBreak, putRow(dbwire.TableChains, dbwire.ChainGapKey(dbwire.LogLogin, 2),
			[]byte("short")), dbwire.LogLogin, 5, unreadable("login", "a row of an accepted gap cannot be read")},
		{"a row of an accepted gap of record 0", withBreak, putRow(dbwire.TableChains, dbwire.ChainGapKey(dbwire.LogLogin, 0),
			dbwire.AppendChainHole(nil, dbwire.ChainHole{First: 1, Last: 1, At: pruneNow})), dbwire.LogLogin, 5,
			unreadable("login", "a row of an accepted gap cannot be read")},
		{"a row of an accepted gap that ends before it starts", withBreak, putRow(dbwire.TableChains, dbwire.ChainGapKey(dbwire.LogLogin, 4),
			dbwire.AppendChainBreak(nil, dbwire.ChainBreak{Hash: wrong32, At: pruneNow})), dbwire.LogLogin, 5,
			unreadable("login", "a row of an accepted gap cannot be read")},
		{"a row of a hole that cannot be read", withBreak, putRow(dbwire.TableChains, dbwire.ChainHoleKey(dbwire.LogLogin, 2),
			[]byte("short")), dbwire.LogLogin, 5, unreadable("login", "a row of a hole cannot be read")},
		{"a chain start of the activity log", withBreak, putRow(dbwire.TableChains, dbwire.ChainStartKey(dbwire.LogActivity),
			dbwire.AppendChainStart(nil, dbwire.ChainStart{First: 3, Prev: wrong32, At: pruneNow})), dbwire.LogActivity, 5,
			unreadable("activity", "a chain start for a log of which only the login records are ever deleted")},
		{"an older table schema", withBreak, putRow(dbwire.TableMeta, []byte(dbwire.MetaSchema), []byte{dbwire.TableVersion - 1}),
			dbwire.LogLogin, 5, fmt.Sprintf("the store has the table schema %d, and the first writer that connects through "+
				"whiteroom-db serve (whiteroom serve admin, for one) upgrades it to %d: accept the break then", dbwire.TableVersion-1,
				dbwire.TableVersion)},
		{"an older table schema, for a gap", [][]byte{chain.rec(1), chain.rec(2), chain.rec(4)},
			putRow(dbwire.TableMeta, []byte(dbwire.MetaSchema), []byte{dbwire.TableVersion - 1}), dbwire.LogLogin, 4,
			fmt.Sprintf("the store has the table schema %d, and the first writer that connects through whiteroom-db serve "+
				"(whiteroom serve admin, for one) upgrades it to %d: accept the gap then", dbwire.TableVersion-1, dbwire.TableVersion)},
		{"an older table schema, for a gap at the end", [][]byte{chain.rec(1), chain.rec(2), chain.rec(3)}, func(t *testing.T, f *fixture) {
			headAt(dbwire.LogLogin, 4, chain.sum(4))(t, f)
			putRow(dbwire.TableMeta, []byte(dbwire.MetaSchema), []byte{dbwire.TableVersion - 1})(t, f)
		}, dbwire.LogLogin, 4, fmt.Sprintf("the store has the table schema %d, and the first writer that connects through whiteroom-db "+
			"serve (whiteroom serve admin, for one) upgrades it to %d: accept the gap then", dbwire.TableVersion-1, dbwire.TableVersion)},
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
		// The record 5 has the hash of the record 4, which is not there: its number does not follow, and a lost record is no break,
		// but a gap.
		f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), chain.rec(5), chain.rec(6)})
		refused(t, f, 6, "record 6 follows record 5: there is no break to accept")
		refused(t, f, 4, "record 4 of the audit log login is not in the store: it is in the gap of record 4 before record 5; accept the gap "+
			"with -record 5")
		before := viewOf(t, f)
		if acc, err := accept(t, f, dbwire.LogLogin, 5); err != nil || acc != (Acceptance{Gap: true, First: 4, Last: 4}) {
			t.Fatalf("AcceptBreak() of the record 5 = %+v, %v, want the gap of the record 4", acc, err)
		}
		checkGapAccepted(t, f, before, dbwire.LogLogin, 4, 4, chain.sum(4))
		// The gap is accepted, and the record that it leaves out is where it is told to be.
		refused(t, f, 4, "record 4 of the audit log login is not in the store")
		refused(t, f, 5, acceptedText(5, 4, 4))
	})
	t.Run("a number that skips", func(t *testing.T) {
		// The record 6 has the hash of the record 4, the last one: only its number does not follow.
		skipping := auditRecord(dbwire.LogLogin, 6, hashText(chain.sum(4)))
		f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), chain.rec(4), skipping})
		before := viewOf(t, f)
		if acc, err := accept(t, f, dbwire.LogLogin, 6); err != nil || acc != (Acceptance{Gap: true, First: 5, Last: 5}) {
			t.Fatalf("AcceptBreak() = %+v, %v, want the gap of the record 5", acc, err)
		}
		checkGapAccepted(t, f, before, dbwire.LogLogin, 5, 5, chain.sum(4))
	})
	t.Run("a gap, and then a break after it", func(t *testing.T) {
		// The record 3 is lost, and the record 5 is another one, so that the record 6 is a break: the gap and the break are both
		// accepted, each by its own rule, in either order.
		f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(4), changedRecord(dbwire.LogLogin, 5, chain), chain.rec(6)})
		before := viewOf(t, f)
		if acc, err := accept(t, f, dbwire.LogLogin, 4); err != nil || acc != (Acceptance{Gap: true, First: 3, Last: 3}) {
			t.Fatalf("AcceptBreak() of the record 4 = %+v, %v, want the gap of the record 3", acc, err)
		}
		refused(t, f, 4, acceptedText(4, 3, 3))
		refused(t, f, 5, "record 5 follows record 4: there is no break to accept")
		if acc, err := accept(t, f, dbwire.LogLogin, 6); err != nil || acc.Gap {
			t.Fatalf("AcceptBreak() of the record 6 = %+v, %v, want the break", acc, err)
		}
		after := viewOf(t, f)
		kept(t, before, after)
		if want := before.shape + " G3 M B6 M"; after.shape != want {
			t.Errorf("the store is %q, want %q", after.shape, want)
		}
		if after.gaps != 1 || after.breaks != 1 || after.holes != 0 {
			t.Errorf("the table chains has %d rows of gaps, %d of breaks and %d of holes, want 1, 1 and 0", after.gaps, after.breaks,
				after.holes)
		}
	})
	t.Run("a gap after a hole of a repair", func(t *testing.T) {
		// The records 3 and 5 are lost: the repair recorded the hole of the record 3, and the record 5 is a gap.
		f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(4), chain.rec(6)}, holeAt(dbwire.LogLogin, 3, 3, chain.sum(3)))
		before := viewOf(t, f)
		if acc, err := accept(t, f, dbwire.LogLogin, 6); err != nil || acc != (Acceptance{Gap: true, First: 5, Last: 5}) {
			t.Fatalf("AcceptBreak() = %+v, %v, want the gap of the record 5", acc, err)
		}
		checkGapAccepted(t, f, before, dbwire.LogLogin, 5, 5, chain.sum(5))
		if rows := chainTable(t, f); len(rows) != 2 {
			t.Errorf("the table chains has %d rows, want the hole and the gap", len(rows))
		}
	})
	t.Run("a break after a hole", func(t *testing.T) {
		// The record 3 is lost, and the record 5 is another one, so that the record 6 is a break: the record 4, which comes after
		// the hole, has the hash of the record 3, and the walk goes on from it.
		f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(4), changedRecord(dbwire.LogLogin, 5, chain), chain.rec(6)},
			holeAt(dbwire.LogLogin, 3, 3, chain.sum(3)))
		before := viewOf(t, f)
		if err := acceptBreak(t, f, dbwire.LogLogin, 4); err == nil || err.Error() != holeText(4, 3, 3) {
			t.Errorf("AcceptBreak() of the record after the hole = %v, want %q", err, holeText(4, 3, 3))
		}
		kept(t, before, viewOf(t, f))
		if err := acceptBreak(t, f, dbwire.LogLogin, 6); err != nil {
			t.Fatalf("AcceptBreak() = %v", err)
		}
		after := viewOf(t, f)
		kept(t, before, after)
		if want := before.shape + " B6 M"; after.shape != want {
			t.Errorf("the store is %q, want %q", after.shape, want)
		}
		rows := chainTable(t, f)
		if len(rows) != 2 || rows[string(dbwire.ChainBreakKey(dbwire.LogLogin, 6))] == "" ||
			rows[string(dbwire.ChainHoleKey(dbwire.LogLogin, 3))] == "" {
			t.Errorf("the table chains has %d rows, want the hole and the break at the record 6", len(rows))
		}
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
		// The record 2 comes after the record 3, which does not make it follow, though the record 1 is before.
		refused(t, f, 2, belowText(2, 3))
		// The record 3 is a gap after the record 1, whatever comes after it.
		before := viewOf(t, f)
		if acc, err := accept(t, f, dbwire.LogLogin, 3); err != nil || acc != (Acceptance{Gap: true, First: 2, Last: 2}) {
			t.Fatalf("AcceptBreak() of the record 3 = %+v, %v, want the gap of the record 2", acc, err)
		}
		checkGapAccepted(t, f, before, dbwire.LogLogin, 2, 2, chain.sum(2))
		refused(t, f, 2, belowText(2, 3))
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
	t.Run("the records that a deletion left, with a gap before the chain start", func(t *testing.T) {
		// The chain starts at the record 5, the records 1 and 2 are still there, and the records 3 and 4 are lost: the record 5 is a
		// gap after the record 2, whose number is that of the chain start. The records that the deletion left count for what the
		// walk expects.
		f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(5), chain.rec(6)}, startAt(5, chain.sum(4)))
		before := viewOf(t, f)
		if acc, err := accept(t, f, dbwire.LogLogin, 5); err != nil || acc != (Acceptance{Gap: true, First: 3, Last: 4}) {
			t.Fatalf("AcceptBreak() = %+v, %v, want the gap of the records 3 to 4", acc, err)
		}
		checkGapAccepted(t, f, before, dbwire.LogLogin, 3, 4, chain.sum(4))
		refused(t, f, 5, acceptedText(5, 3, 4))
		// The record before the chain start is not in the store as far as accept-break goes.
		refused(t, f, 2, "record 2 of the audit log login is not in the store: the records before record 5 were deleted")
	})
	t.Run("the first record that remains, after the records that a deletion left", func(t *testing.T) {
		// The records 1 to 4 are there, and the chain starts at the record 5: nothing is missing, and the record 5 has the hash
		// that the chain start has of the record 4, so that there is no break at it.
		f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), chain.rec(4), chain.rec(5)}, startAt(5, chain.sum(4)))
		refused(t, f, 5, startHashText(5))
	})
	t.Run("the first record that remains, which is a break, after the records that a deletion left", func(t *testing.T) {
		// The record 5 does not have the hash that the chain start has of the record 4, and the records 1 to 4 are still there:
		// the record 5 is checked against them and against the chain start, and no break is accepted at it until they are gone.
		f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), chain.rec(4), chain.rec(5)}, startAt(5, wrong32))
		refused(t, f, 5, leftoversText(5))
		// The same store with the records gone, which the compaction of the deletion does: the break is accepted.
		f = recordsFixture(t, [][]byte{chain.rec(5), chain.rec(6)}, startAt(5, wrong32))
		before := viewOf(t, f)
		if err := acceptBreak(t, f, dbwire.LogLogin, 5); err != nil {
			t.Fatalf("AcceptBreak() = %v", err)
		}
		checkAccepted(t, f, before, dbwire.LogLogin, 5, chain.sum(5))
	})
	t.Run("the first record that remains, after records that a deletion left that end before it", func(t *testing.T) {
		// The records 1 and 2 are there, the record 3 is lost, and the chain starts at the record 4: the record 4 is a gap after
		// the record 2, whatever its prev is, and not a break, if its prev is the hash that the chain start has.
		f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(4), chain.rec(5)}, startAt(4, chain.sum(3)))
		before := viewOf(t, f)
		if acc, err := accept(t, f, dbwire.LogLogin, 4); err != nil || acc != (Acceptance{Gap: true, First: 3, Last: 3}) {
			t.Fatalf("AcceptBreak() = %+v, %v, want the gap of the record 3", acc, err)
		}
		checkGapAccepted(t, f, before, dbwire.LogLogin, 3, 3, chain.sum(3))
	})
	t.Run("the first record that remains, after records that end before it, with another prev", func(t *testing.T) {
		// The gap would end with the record 3 and the hash that the record 4 has as its prev, which is not the chain start's: the
		// record 4 would still be a break, which VerifyAudit fails at.
		f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(4), chain.rec(5)}, startAt(4, wrong32))
		refused(t, f, 4, "the gap of record 3 before record 4 is not accepted: record 4 does not have as its prev the hash that the chain "+
			"start has of record 3")
	})
}

// TestAcceptBreakAcrossChainStart checks the gap that goes across the first record that remains, K, from the records that a
// deletion left, or from none: the hash of the record K-1 that the walk has must be the chain start's, whatever row passes the
// gap or comes after it.
func TestAcceptBreakAcrossChainStart(t *testing.T) {
	chain := newChain(dbwire.LogLogin, 9)
	const L = dbwire.LogLogin
	changed := changedRecord(L, 3, chain) // the record 3 as it is not: its hash is not the chain start's
	refused := func(t *testing.T, f *fixture, n uint64, want string) {
		t.Helper()
		before, files := viewOf(t, f), storeFiles(f)
		if err := acceptBreak(t, f, L, n); err == nil || err.Error() != want {
			t.Errorf("AcceptBreak() of the record %d = %v, want %q", n, err, want)
		}
		if got := storeFiles(f); !maps.Equal(got, files) {
			t.Error("the refusal changed the files of the store")
		}
		kept(t, before, viewOf(t, f))
	}
	both := func(steps ...func(t *testing.T, f *fixture)) func(t *testing.T, f *fixture) {
		return func(t *testing.T, f *fixture) {
			for _, step := range steps {
				step(t, f)
			}
		}
	}
	t.Run("a gap after a record before the chain start that is not the one that it has", func(t *testing.T) {
		// The records 1 to 3 are there and the chain starts at the record 4, whose record is lost: the gap is the record 4, and the
		// record 5 has the hash of it as its prev. The record 3 is not the one that the chain start has.
		want := "record 5 comes after records before it that an earlier deletion left, the last of which, record 3, does not have the " +
			"hash that the chain start has of it: the gap before record 5 is not accepted"
		f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(2), changed, chain.rec(5), chain.rec(6)}, startAt(4, chain.sum(3)))
		refused(t, f, 5, want)
		// The record 4, which is the gap, has no hint to accept the gap with the record 5, which would not be accepted.
		refused(t, f, 4, "record 4 of the audit log login is not in the store")
	})
	t.Run("a gap after the record before the chain start that is the one that it has", func(t *testing.T) {
		f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), chain.rec(5), chain.rec(6)}, startAt(4, chain.sum(3)))
		before := viewOf(t, f)
		if acc, err := accept(t, f, L, 5); err != nil || acc != (Acceptance{Gap: true, First: 4, Last: 4}) {
			t.Fatalf("AcceptBreak() = %+v, %v, want the gap of the record 4", acc, err)
		}
		checkGapAccepted(t, f, before, L, 4, 4, chain.sum(4))
	})
	t.Run("a gap that holds the chain start", func(t *testing.T) {
		// The records 3 to 6 are lost, and the chain starts at the record 5, so that no hash of the record 4 is known.
		f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(7), chain.rec(8)}, startAt(5, wrong32))
		before := viewOf(t, f)
		if acc, err := accept(t, f, L, 7); err != nil || acc != (Acceptance{Gap: true, First: 3, Last: 6}) {
			t.Fatalf("AcceptBreak() = %+v, %v, want the gap of the records 3 to 6", acc, err)
		}
		checkGapAccepted(t, f, before, L, 3, 6, chain.sum(6))
	})
	t.Run("a gap that holds the chain start, when no record is before it", func(t *testing.T) {
		f := recordsFixture(t, [][]byte{chain.rec(7), chain.rec(8)}, startAt(5, wrong32))
		before := viewOf(t, f)
		if acc, err := accept(t, f, L, 7); err != nil || acc != (Acceptance{Gap: true, First: 5, Last: 6}) {
			t.Fatalf("AcceptBreak() = %+v, %v, want the gap of the records 5 to 6", acc, err)
		}
		checkGapAccepted(t, f, before, L, 5, 6, chain.sum(6))
	})
	t.Run("a gap that ends with the record before the chain start, and the chain start has another hash", func(t *testing.T) {
		f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(5)}, startAt(5, wrong32))
		refused(t, f, 5, "the gap of the records 3 to 4 before record 5 is not accepted: record 5 does not have as its prev the hash that "+
			"the chain start has of record 4")
	})
	t.Run("a gap at the end that holds the chain start", func(t *testing.T) {
		f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(2)}, both(startAt(5, chain.sum(4)), headAt(L, 7, chain.sum(7))))
		before := viewOf(t, f)
		if acc, err := accept(t, f, L, 7); err != nil || acc != (Acceptance{Gap: true, End: true, First: 3, Last: 7}) {
			t.Fatalf("AcceptBreak() = %+v, %v, want the gap of the records 3 to 7 at the end", acc, err)
		}
		checkGapAccepted(t, f, before, L, 3, 7, chain.sum(7))
	})
	t.Run("a gap at the end after a record before the chain start that is not the one that it has", func(t *testing.T) {
		f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(2), changed}, both(startAt(4, chain.sum(3)), headAt(L, 5, chain.sum(5))))
		refused(t, f, 5, "the gap at the end of the audit log login comes after records before it that an earlier deletion left, the last "+
			"of which, record 3, does not have the hash that the chain start has of it: the gap is not accepted")
	})
	t.Run("a gap at the end that would end before the record before the chain start", func(t *testing.T) {
		// The head names a record that the chain start leaves out: no gap is accepted for it.
		f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(2)}, both(startAt(6, chain.sum(5)), headAt(L, 4, chain.sum(4))))
		refused(t, f, 4, "record 4 of the audit log login is not in the store: the records before record 6 were deleted")
	})
	t.Run("a record after rows that follow one another", func(t *testing.T) {
		for _, tt := range []struct {
			name string
			n    int // the record that comes after them
			rows []func(t *testing.T, f *fixture)
			want string
		}{
			{"two holes", 5, []func(t *testing.T, f *fixture){holeAt(L, 3, 3, chain.sum(3)), holeAt(L, 4, 4, chain.sum(4))},
				"record 5 comes after the record 3 and the record 4 that whiteroom-db repair dropped: there is no break to accept"},
			{"a hole of two records and a hole", 6, []func(t *testing.T, f *fixture){holeAt(L, 3, 4, chain.sum(4)), holeAt(L, 5, 5, chain.sum(5))},
				"record 6 comes after the records 3 to 4 and the record 5 that whiteroom-db repair dropped: there is no break to accept"},
			{"two gaps", 5, []func(t *testing.T, f *fixture){gapAt(L, 3, 3, chain.sum(3)), gapAt(L, 4, 4, chain.sum(4))},
				"record 5 comes after the record 3 and the record 4 whose gap is accepted already"},
			{"a hole and a gap", 5, []func(t *testing.T, f *fixture){holeAt(L, 3, 3, chain.sum(3)), gapAt(L, 4, 4, chain.sum(4))},
				"record 5 comes after the record 3 that whiteroom-db repair dropped and the record 4 whose gap is accepted already: there is " +
					"no break to accept"},
			{"a gap, a hole and a gap", 6, []func(t *testing.T, f *fixture){gapAt(L, 3, 3, chain.sum(3)), holeAt(L, 4, 4, chain.sum(4)),
				gapAt(L, 5, 5, chain.sum(5))},
				"record 6 comes after the record 3 whose gap is accepted already, the record 4 that whiteroom-db repair dropped and the " +
					"record 5 whose gap is accepted already: there is no break to accept"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(tt.n)}, both(tt.rows...))
				refused(t, f, uint64(tt.n), tt.want)
			})
		}
	})
	t.Run("a record after a row that holds the chain start", func(t *testing.T) {
		f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(7)}, both(startAt(5, chain.sum(4)), holeAt(L, 3, 6, chain.sum(6))))
		refused(t, f, 7, holeText(7, 3, 6))
		f = recordsFixture(t, [][]byte{chain.rec(7)}, both(startAt(5, chain.sum(4)), gapAt(L, 3, 6, chain.sum(6))))
		refused(t, f, 7, acceptedText(7, 3, 6))
	})
	t.Run("a gap before a record that a row that holds the chain start does not pass", func(t *testing.T) {
		// The row ends with the record 6, and the record 8 has the hash of the record 7: the gap is the record 7, after the row.
		f := recordsFixture(t, [][]byte{chain.rec(8), chain.rec(9)}, both(startAt(5, chain.sum(4)), holeAt(L, 3, 6, chain.sum(6))))
		before := viewOf(t, f)
		if acc, err := accept(t, f, L, 8); err != nil || acc != (Acceptance{Gap: true, First: 7, Last: 7}) {
			t.Fatalf("AcceptBreak() = %+v, %v, want the gap of the record 7", acc, err)
		}
		checkGapAccepted(t, f, before, L, 7, 7, chain.sum(7))
	})
	t.Run("a head behind a record that is absent", func(t *testing.T) {
		// The records 1, 2, 4, 5 and 6 are there and the head names the record 3, which a writer that has been broken into can set:
		// the gap is the record 3 before the record 4, and it is accepted with the record 4. A gap at the end that has the record 3 as
		// its last would have a first above it, which no row can have.
		f := recordsFixture(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(4), chain.rec(5), chain.rec(6)}, headAt(L, 3, chain.sum(3)))
		refused(t, f, 3, "record 3 of the audit log login is not in the store: it is in the gap of record 3 before record 4; accept the gap "+
			"with -record 4")
		before := viewOf(t, f)
		if acc, err := accept(t, f, L, 4); err != nil || acc != (Acceptance{Gap: true, First: 3, Last: 3}) {
			t.Fatalf("AcceptBreak() of the record 4 = %+v, %v, want the gap of the record 3", acc, err)
		}
		checkGapAccepted(t, f, before, L, 3, 3, chain.sum(3))
	})
}

// TestGapValue checks that the row of a gap is one that the walks can read back: a gap that has its last before its first is
// refused, as is one that a record number of 0 would name.
func TestGapValue(t *testing.T) {
	if v, err := gapValue(3, 4, chain32(4), acceptAt); err != nil {
		t.Errorf("gapValue(3, 4) = %v", err)
	} else if g, ok := dbwire.ParseChainHole(3, v); !ok || g.Last != 4 || g.Hash != chain32(4) || !g.At.Equal(acceptAt) {
		t.Errorf("the value of gapValue(3, 4) reads back as %+v (%v)", g, ok)
	}
	for _, c := range []struct{ first, last uint64 }{{7, 3}, {4, 3}, {0, 3}} {
		if v, err := gapValue(c.first, c.last, chain32(1), acceptAt); err == nil {
			t.Errorf("gapValue(%d, %d) = %x, nil, want an error", c.first, c.last, v)
		} else if want := fmt.Sprintf("the row of the gap of the records %d to %d could not be read back, so it is not written", c.first,
			c.last); err.Error() != want {
			t.Errorf("gapValue(%d, %d) = %v, want %q", c.first, c.last, err, want)
		}
	}
}

// chain32 is a hash that depends on n alone.
func chain32(n byte) (h [sha256.Size]byte) {
	h[0], h[1] = n, 0xc3
	return h
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
			_, err := AcceptBreak(t.Context(), m, dbwire.LogLogin, 5, acceptAt)
			return err
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
			_, err := AcceptBreak(ctx, m, dbwire.LogLogin, 5, acceptAt)
			return err
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
