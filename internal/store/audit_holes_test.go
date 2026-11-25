package store

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// The tests of the holes write the rows of the table chains and the events of the daemon for a hole into the engine as the
// repair of the daemon does, and drop the records that the hole stands for with a rewrite of the engine, as the repair does
// with the records that it cannot keep.

func holeRow(log string, first, last uint64, hash [sha256.Size]byte) chainRow {
	return chainRow{dbwire.ChainHoleKey(log, first),
		dbwire.AppendChainHole(nil, dbwire.ChainHole{First: first, Last: last, Hash: hash, At: chainTime})}
}

// holeEvents returns the events that the repair adds to the log for the hole.
func holeEvents(log string, first, last uint64, hash [sha256.Size]byte) [][]byte {
	return [][]byte{dbwire.AppendHole(nil, dbwire.Hole{Log: log, First: first, Last: last, Hash: hash}), dbwire.AppendMark(nil, chainTime)}
}

// gapEvents are the events that whiteroom-db accept-break adds for a gap: the event of the acceptance and a mark.
func gapEvents(log string, first, last uint64, hash [sha256.Size]byte) [][]byte {
	return [][]byte{dbwire.AppendGap(nil, dbwire.Hole{Log: log, First: first, Last: last, Hash: hash}), dbwire.AppendMark(nil, chainTime)}
}

// lostNote is the note of a hole of the records first to last of log, which says after it what follows.
func lostNote(log string, first, last uint64, after string) string {
	lost := fmt.Sprintf("the records %d to %d were", first, last)
	if first == last {
		lost = fmt.Sprintf("record %d was", first)
	}
	return fmt.Sprintf(`audit log %q: %s lost and dropped by whiteroom-db repair at 2026-11-20T10:20:30Z; %s`, log, lost, after)
}

func againFrom(n uint64) string { return fmt.Sprintf("the log is checked again from record %d", n) }

func endsWith(n uint64) string { return fmt.Sprintf("the log ends with record %d", n) }

const noRecordLeft = "the log has no record left"

// holeChange is what a test of the holes does to the fixture: it puts the rows and the events in one commit, and then
// drops, with the compaction of the engine, the records at the positions that drop lists, as rewriteRecords counts them.
type holeChange struct {
	rows    []chainRow
	events  [][]byte
	drop    []uint64
	replace map[uint64][]byte // the data of the records at the positions
}

// lose is the change that drops the records at the positions drop, and records the hole for them.
func lose(drop []uint64, log string, first, last uint64, hash [sha256.Size]byte, more ...chainRow) holeChange {
	return holeChange{append([]chainRow{holeRow(log, first, last, hash)}, more...), holeEvents(log, first, last, hash), drop, nil}
}

const (
	unreadableHole = `audit log "login": a row of the table chains for a hole cannot be read`
	noActivity     = `audit log "activity": the log has no record, but every store of the table schema 6 or later has store.create or ` +
		`store.upgrade as its first`
	// The problem of the activity log that has no record, when a hole or an accepted gap of the records 1 to 6 passes its head.
	noActivityHole = noActivity + `; the records 1 to 6 were lost (a hole that whiteroom-db repair recorded at 2026-11-20T10:20:30Z), ` +
		`and the next record that the writer adds ends this problem`
	noActivityGap = noActivity + `; the records 1 to 6 were lost (a gap that whiteroom-db accept-break accepted at 2026-11-20T10:20:30Z), ` +
		`and the next record that the writer adds ends this problem`
)

// TestVerifyAuditHoles checks that VerifyAudit and VerifyAuditNotes pass the records that a hole of the table chains
// stands for, exactly as the row says and no further, and tell each hole at its place in the order of the reads.
func TestVerifyAuditHoles(t *testing.T) {
	// The records are, by position (the number of the record in its log): 1 activity 1, 2 activity 2, 3 login 1, 4 activity 3,
	// 5 activity 4, 6 login 2, 7 activity 5, 8 activity 6, 9 login 3.
	const (
		L = string(audit.LoginLog)
		A = string(audit.ActivityLog)
	)
	for _, tt := range []struct {
		name   string
		change func(t *testing.T, f chainFixture) holeChange
		want   string
		notes  func() []string
	}{
		// What a hole passes.
		{"a record in the middle of the login history", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{6}, L, 2, 2, f.login(2))
		}, "", func() []string { return []string{lostNote(L, 2, 2, againFrom(3))} }},
		{"a record in the middle of the activity log", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{4}, A, 3, 3, f.activity(3))
		}, "", func() []string { return []string{lostNote(A, 3, 3, againFrom(4))} }},
		{"two records in the middle", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{4, 5}, A, 3, 4, f.activity(4))
		}, "", func() []string { return []string{lostNote(A, 3, 4, againFrom(5))} }},
		{"the first records of the activity log", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{1, 2}, A, 1, 2, f.activity(2))
		}, "", func() []string { return []string{lostNote(A, 1, 2, againFrom(3))} }},
		{"the first record of the login history", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{3}, L, 1, 1, f.login(1))
		}, "", func() []string { return []string{lostNote(L, 1, 1, againFrom(2))} }},
		{"the last record of the login history", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{9}, L, 3, 3, f.login(3))
		}, "", func() []string { return []string{lostNote(L, 3, 3, endsWith(2))} }},
		{"the last record of the activity log", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{8}, A, 6, 6, f.activity(6))
		}, "", func() []string { return []string{lostNote(A, 6, 6, endsWith(5))} }},
		{"the last two records of the activity log", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{7, 8}, A, 5, 6, f.activity(6))
		}, "", func() []string { return []string{lostNote(A, 5, 6, endsWith(4))} }},
		{"every record of the login history", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{3, 6, 9}, L, 1, 3, f.login(3))
		}, "", func() []string { return []string{lostNote(L, 1, 3, noRecordLeft)} }},
		{"a hole at the end of each log", func(t *testing.T, f chainFixture) holeChange {
			c := lose([]uint64{8, 9}, A, 6, 6, f.activity(6), holeRow(L, 3, 3, f.login(3)))
			c.events = slices.Concat(c.events, holeEvents(L, 3, 3, f.login(3)))
			return c
		}, "", func() []string { return []string{lostNote(L, 3, 3, endsWith(2)), lostNote(A, 6, 6, endsWith(5))} }},
		{"a hole in each log, told in the order of the reads", func(t *testing.T, f chainFixture) holeChange {
			c := lose([]uint64{4, 6}, A, 3, 3, f.activity(3), holeRow(L, 2, 2, f.login(2)))
			c.events = slices.Concat(c.events, holeEvents(L, 2, 2, f.login(2)))
			return c
		}, "", func() []string { return []string{lostNote(A, 3, 3, againFrom(4)), lostNote(L, 2, 2, againFrom(3))} }},
		{"two holes in a log", func(t *testing.T, f chainFixture) holeChange {
			c := lose([]uint64{2, 5}, A, 2, 2, f.activity(2), holeRow(A, 4, 4, f.activity(4)))
			c.events = slices.Concat(c.events, holeEvents(A, 4, 4, f.activity(4)))
			return c
		}, "", func() []string { return []string{lostNote(A, 2, 2, againFrom(3)), lostNote(A, 4, 4, againFrom(5))} }},
		{"a hole in the middle and a hole at the end", func(t *testing.T, f chainFixture) holeChange {
			c := lose([]uint64{2, 8}, A, 2, 2, f.activity(2), holeRow(A, 6, 6, f.activity(6)))
			c.events = slices.Concat(c.events, holeEvents(A, 6, 6, f.activity(6)))
			return c
		}, "", func() []string { return []string{lostNote(A, 2, 2, againFrom(3)), lostNote(A, 6, 6, endsWith(5))} }},
		{"a hole at the chain start of the login history", func(t *testing.T, f chainFixture) holeChange {
			// The chain starts at the record 2, and the record 2 is lost: the first record that is read is the record 3.
			c := lose([]uint64{3, 6}, L, 2, 2, f.login(2), startRow(2, f.login(1)))
			c.events = slices.Concat(deletedEvents(2, f.login(1)), c.events)
			return c
		}, "", func() []string { return []string{startNote(2), lostNote(L, 2, 2, againFrom(3))} }},
		{"a hole at the end of the login history after the chain start", func(t *testing.T, f chainFixture) holeChange {
			// The records 1 and 2 are deleted and the record 3, the last, is lost: no record of the log is left.
			c := lose([]uint64{3, 6, 9}, L, 3, 3, f.login(3), startRow(3, f.login(2)))
			c.events = slices.Concat(deletedEvents(3, f.login(2)), c.events)
			return c
		}, "", func() []string { return []string{startNote(3), lostNote(L, 3, 3, noRecordLeft)} }},
		{"a hole after the records that a deletion left", func(t *testing.T, f chainFixture) holeChange {
			// The chain starts at the record 3 and the records 1 and 2 are still there; the record 3 is lost, and no record follows.
			c := lose([]uint64{9}, L, 3, 3, f.login(3), startRow(3, f.login(2)))
			c.events = slices.Concat(deletedEvents(3, f.login(2)), c.events)
			return c
		}, "", func() []string {
			return []string{startNote(3), lostNote(L, 3, 3, endsWith(2)), leftNote("the records 1 to 2 are", 3)}
		}},
		{"a hole, and then a break", func(t *testing.T, f chainFixture) holeChange {
			// The record 2 is lost, and the record 4 is another one, so that the record 5 is a break that is accepted.
			c := lose([]uint64{2}, A, 2, 2, f.activity(2), breakRow(A, 5, f.activity(5)))
			c.events = slices.Concat(c.events, acceptedEvents(A, 5, f.activity(5)))
			c.replace = map[uint64][]byte{5: changedRecord(t, f.events[4].Data, func(r *audit.Record) { r.Detail = map[string]string{"event": "X"} })}
			return c
		}, "", func() []string { return []string{lostNote(A, 2, 2, againFrom(3)), breakNote(A, 5)} }},
		{"a break that is accepted for the record after a hole is of no use", func(t *testing.T, f chainFixture) holeChange {
			c := lose([]uint64{2}, A, 2, 2, f.activity(2), breakRow(A, 3, f.activity(3)))
			c.events = slices.Concat(c.events, acceptedEvents(A, 3, f.activity(3)))
			return c
		}, "", func() []string { return []string{lostNote(A, 2, 2, againFrom(3))} }},

		// What it does not pass.
		{"a row that is one record wider", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{4}, A, 3, 4, f.activity(4))
		}, `audit log "activity": record 4 (event 5): the number does not follow that of record 2`, nil},
		{"a row that is one record narrower", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{4, 5}, A, 3, 3, f.activity(3))
		}, `audit log "activity": record 5 (event 7): the number does not follow that of record 2`, nil},
		{"a row that starts one record earlier", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{4}, A, 2, 3, f.activity(3))
		}, `audit log "activity": record 4 (event 5): the number does not follow that of record 2`, nil},
		{"a row that starts one record later", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{4}, A, 4, 4, f.activity(3))
		}, `audit log "activity": record 4 (event 5): the number does not follow that of record 2`, nil},
		{"a row with the hash of another record", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{4}, A, 3, 3, f.activity(4))
		}, `audit log "activity": record 4 (event 5): the number does not follow that of record 2`, nil},
		{"a row with a hash that no record has", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{4}, A, 3, 3, wrong)
		}, `audit log "activity": record 4 (event 5): the number does not follow that of record 2`, nil},
		{"a row of the other log", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{4}, L, 3, 3, f.activity(3))
		}, `audit log "activity": record 4 (event 5): the number does not follow that of record 2`, nil},
		{"a row of the other log, with the number of a record that it has", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{6}, A, 2, 2, f.login(2))
		}, `audit log "login": record 3 (event 9): the number does not follow that of record 1`, nil},
		{"the prev of the record after the hole was rewritten", func(t *testing.T, f chainFixture) holeChange {
			c := lose([]uint64{4}, A, 3, 3, f.activity(3))
			c.replace = map[uint64][]byte{5: changedRecord(t, f.events[4].Data, func(r *audit.Record) { r.Prev = hashText(wrong[:]) })}
			return c
		}, `audit log "activity": record 4 (event 5): the number does not follow that of record 2`, nil},
		{"the record after the hole was rewritten, which the record after it does not follow", func(t *testing.T, f chainFixture) holeChange {
			c := lose([]uint64{2}, A, 2, 2, f.activity(2))
			c.replace = map[uint64][]byte{4: changedRecord(t, f.events[3].Data, func(r *audit.Record) { r.Detail = map[string]string{"event": "X"} })}
			return c
		}, `audit log "activity": record 4 (event 5): prev is not the hash of record 3`,
			func() []string { return []string{lostNote(A, 2, 2, againFrom(3))} }},
		{"the record at the start of the log, with a row for the one before it", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{1, 2}, A, 1, 1, f.activity(2))
		}, `audit log "activity": record 3 (event 4): the first record of a log is number 1`, nil},
		{"a hole at the end with another hash", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{9}, L, 3, 3, wrong)
		}, `audit log "login": the head in the table is record 3, but the last record is record 2 (event 6)`, nil},
		{"a hole at the end with the hash of the record before", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{9}, L, 3, 3, f.login(2))
		}, `audit log "login": the head in the table is record 3, but the last record is record 2 (event 6)`, nil},
		{"a hole at the end that ends before the head", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{7, 8}, A, 5, 5, f.activity(5))
		}, `audit log "activity": the head in the table is record 6, but the last record is record 4 (event 5)`, nil},
		{"a hole at the end that starts after the last record", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{7, 8}, A, 6, 6, f.activity(6))
		}, `audit log "activity": the head in the table is record 6, but the last record is record 4 (event 5)`, nil},
		{"a hole at the end that starts before the last record", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{8}, A, 5, 6, f.activity(6))
		}, `audit log "activity": the head in the table is record 6, but the last record is record 5 (event 7)`, nil},
		{"a hole at the end of the other log", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{9}, A, 3, 3, f.login(3))
		}, `audit log "login": the head in the table is record 3, but the last record is record 2 (event 6)`, nil},
		{"a hole at the end that is passed by the head of the other log", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{9}, L, 3, 3, f.activity(6))
		}, `audit log "login": the head in the table is record 3, but the last record is record 2 (event 6)`, nil},
		{"every record of the activity log, which a hole does not excuse", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{1, 2, 4, 5, 7, 8}, A, 1, 6, f.activity(6))
		}, noActivityHole, func() []string { return nil }},
		{"every record of the login history, with a hole that does not cover the head", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{3, 6, 9}, L, 1, 2, f.login(2))
		}, `audit log "login": the table has the head of record 3, but the log has no record`, nil},
		{"a hole at the end, with the chain start past it", func(t *testing.T, f chainFixture) holeChange {
			c := lose([]uint64{3, 6, 9}, L, 3, 3, f.login(3), startRow(4, f.login(3)))
			c.events = slices.Concat(deletedEvents(4, f.login(3)), c.events)
			return c
		}, "", func() []string { return []string{startNote(4)} }},

		// A row that cannot be read.
		{"a row with a value that is no hole", func(t *testing.T, f chainFixture) holeChange {
			return holeChange{rows: []chainRow{{dbwire.ChainHoleKey(L, 2), []byte("abc")}}}
		}, unreadableHole, func() []string { return nil }},
		{"a row with the value of a break", func(t *testing.T, f chainFixture) holeChange {
			return holeChange{rows: []chainRow{{dbwire.ChainHoleKey(L, 2),
				dbwire.AppendChainBreak(nil, dbwire.ChainBreak{Hash: f.login(2), At: chainTime})}}}
		}, unreadableHole, func() []string { return nil }},
		{"a row of the record 0", func(t *testing.T, f chainFixture) holeChange {
			row := holeRow(L, 1, 2, f.login(2))
			row.key = dbwire.ChainHoleKey(L, 0)
			return holeChange{rows: []chainRow{row}}
		}, unreadableHole, func() []string { return nil }},
		{"a row that ends before it starts", func(t *testing.T, f chainFixture) holeChange {
			return holeChange{rows: []chainRow{{dbwire.ChainHoleKey(L, 5),
				dbwire.AppendChainHole(nil, dbwire.ChainHole{Last: 4, Hash: f.login(2), At: chainTime})}}}
		}, unreadableHole, func() []string { return nil }},
		{"a row with a key that is too short", func(t *testing.T, f chainFixture) holeChange {
			return holeChange{rows: []chainRow{{dbwire.ChainHoleKey(L, 2)[:12], holeRow(L, 2, 2, f.login(2)).value}}}
		}, unreadableHole, func() []string { return nil }},
		{"a row with a key that is too long", func(t *testing.T, f chainFixture) holeChange {
			return holeChange{rows: []chainRow{{append(dbwire.ChainHoleKey(L, 2), 0), holeRow(L, 2, 2, f.login(2)).value}}}
		}, unreadableHole, func() []string { return nil }},
		{"a row of a log with a longer name", func(t *testing.T, f chainFixture) holeChange {
			return holeChange{rows: []chainRow{{append([]byte("login\x01\x01"), dbwire.ChainHoleKey(L, 2)[6:]...),
				holeRow(L, 2, 2, f.login(2)).value}}}
		}, unreadableHole, func() []string { return nil }},
		{"a row of a break and a row of a hole that cannot be read", func(t *testing.T, f chainFixture) holeChange {
			return holeChange{rows: []chainRow{{dbwire.ChainBreakKey(L, 2), []byte("abc")}, {dbwire.ChainHoleKey(L, 2), []byte("abc")}}}
		}, `audit log "login": a row of the table chains for an accepted break cannot be read`, func() []string { return nil }},
		{"a row of the activity log that cannot be read", func(t *testing.T, f chainFixture) holeChange {
			return holeChange{rows: []chainRow{{dbwire.ChainHoleKey(A, 2), []byte("abc")}}}
		}, `audit log "activity": a row of the table chains for a hole cannot be read`, func() []string { return nil }},
		{"a row that cannot be read, after one that passes", func(t *testing.T, f chainFixture) holeChange {
			return holeChange{rows: []chainRow{holeRow(L, 2, 2, f.login(2)), {dbwire.ChainHoleKey(L, 3), []byte("abc")}},
				events: holeEvents(L, 2, 2, f.login(2)), drop: []uint64{6}}
		}, unreadableHole, func() []string { return nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newChainFixture(t)
			c := tt.change(t, f)
			injectChains(t, f.s, c.rows, c.events...)
			s := f.s
			if len(c.drop) > 0 || len(c.replace) > 0 {
				s = closeAndRewrite(t, s, f.dir, rewriteRecords(c.replace, c.drop, nil))
			}
			var notes []string
			if tt.notes != nil {
				notes = tt.notes()
			}
			checkVerify(t, s, f.dir, f.events, nil, tt.want, notes...)
			if tt.want == "" {
				// ReadAudit skips the events of the daemon, and reads the records that are there; Check has nothing to say.
				if got, want := len(allRecords(t, s)), 9-len(c.drop); got != want {
					t.Errorf("ReadAudit() reads %d records, want %d", got, want)
				}
				checkNoProblem(t, s)
				checkNoProblem(t, openReaderOf(t, f.dir))
			}
		})
	}
}

// TestVerifyAuditHoleAnchors checks that a head kept outside the store that names a record that a hole stands for is a problem,
// as is one that names a record that the log does not have, and that the anchors of the records that are there are checked.
func TestVerifyAuditHoleAnchors(t *testing.T) {
	const (
		L = string(audit.LoginLog)
		A = string(audit.ActivityLog)
	)
	for _, tt := range []struct {
		name    string
		change  func(t *testing.T, f chainFixture) holeChange
		anchors func(f chainFixture) []audit.Head
		want    string
		notes   func() []string
	}{
		{"the hash of a record in a hole", func(t *testing.T, f chainFixture) holeChange { return lose([]uint64{6}, L, 2, 2, f.login(2)) },
			func(f chainFixture) []audit.Head { return []audit.Head{f.head(audit.LoginLog, 2)} },
			`audit log "login": the head kept outside the store is record 2, which the log does not have`,
			func() []string { return []string{lostNote(L, 2, 2, againFrom(3))} }},
		{"the hash of a record in a hole of two", func(t *testing.T, f chainFixture) holeChange { return lose([]uint64{4, 5}, A, 3, 4, f.activity(4)) },
			func(f chainFixture) []audit.Head { return []audit.Head{f.head(audit.ActivityLog, 3)} },
			`audit log "activity": the head kept outside the store is record 3, which the log does not have`,
			func() []string { return []string{lostNote(A, 3, 4, againFrom(5))} }},
		{"the heads of the records that are there", func(t *testing.T, f chainFixture) holeChange { return lose([]uint64{6}, L, 2, 2, f.login(2)) },
			func(f chainFixture) []audit.Head {
				return []audit.Head{f.head(audit.LoginLog, 1), f.head(audit.LoginLog, 3), f.head(audit.ActivityLog, 6)}
			}, "", func() []string { return []string{lostNote(L, 2, 2, againFrom(3))} }},
		{"the hash of a record after the hole, which is another one", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{6}, L, 2, 2, f.login(2))
		}, func(f chainFixture) []audit.Head { return []audit.Head{{Log: audit.LoginLog, N: 3, Hash: wrong}} },
			`audit log "login": record 3 (event 9): its hash is not that of the head kept outside the store`,
			func() []string { return []string{lostNote(L, 2, 2, againFrom(3))} }},
		{"the head of a record at the end that is lost", func(t *testing.T, f chainFixture) holeChange { return lose([]uint64{9}, L, 3, 3, f.login(3)) },
			func(f chainFixture) []audit.Head { return f.heads },
			`audit log "login": the head kept outside the store is record 3, but the log ends with record 2 (event 6)`,
			func() []string { return []string{lostNote(L, 3, 3, endsWith(2))} }},
		{"the head of a record at the end of the activity log that is lost", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{7, 8}, A, 5, 6, f.activity(6))
		}, func(f chainFixture) []audit.Head { return []audit.Head{f.head(audit.ActivityLog, 5)} },
			`audit log "activity": the head kept outside the store is record 5, but the log ends with record 4 (event 5)`,
			func() []string { return []string{lostNote(A, 5, 6, endsWith(4))} }},
		{"the head of a record before a hole at the end", func(t *testing.T, f chainFixture) holeChange { return lose([]uint64{9}, L, 3, 3, f.login(3)) },
			func(f chainFixture) []audit.Head { return []audit.Head{f.head(audit.LoginLog, 2)} }, "",
			func() []string { return []string{lostNote(L, 3, 3, endsWith(2))} }},
		{"the head of a record when no record is left", func(t *testing.T, f chainFixture) holeChange {
			return lose([]uint64{3, 6, 9}, L, 1, 3, f.login(3))
		}, func(f chainFixture) []audit.Head { return []audit.Head{f.head(audit.LoginLog, 3)} },
			`audit log "login": the head kept outside the store is record 3, but the log has no record`,
			func() []string { return []string{lostNote(L, 1, 3, noRecordLeft)} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newChainFixture(t)
			c := tt.change(t, f)
			injectChains(t, f.s, c.rows, c.events...)
			s := closeAndRewrite(t, f.s, f.dir, rewriteRecords(nil, c.drop, nil))
			checkVerify(t, s, f.dir, f.events, tt.anchors(f), tt.want, tt.notes()...)
		})
	}
}

// TestVerifyAuditHoleThenWriter checks that a hole at the end of a log stays passed when the writer goes on: its next record
// has the hash of the record that was lost, which the head in the table still has, as its prev, so that the hole is now one in
// the middle, and the row that the repair wrote is still the one that passes it.
func TestVerifyAuditHoleThenWriter(t *testing.T) {
	const (
		L = string(audit.LoginLog)
		A = string(audit.ActivityLog)
	)
	f := newChainFixture(t)
	// The last records of both logs are lost.
	events := slices.Concat(holeEvents(L, 3, 3, f.login(3)), holeEvents(A, 6, 6, f.activity(6)))
	injectChains(t, f.s, []chainRow{holeRow(L, 3, 3, f.login(3)), holeRow(A, 6, 6, f.activity(6))}, events...)
	s := closeAndRewrite(t, f.s, f.dir, rewriteRecords(nil, []uint64{8, 9}, nil))
	checkVerify(t, s, f.dir, f.events, nil, "", lostNote(L, 3, 3, endsWith(2)), lostNote(A, 6, 6, endsWith(5)))

	// The writer goes on: the next records are 4 and 7, and have the hashes of the records that were lost.
	update(t, s, func(tx *Tx) error {
		if err := tx.Record(eventRecord("after")); err != nil {
			return err
		}
		return tx.Record(loginRecord(audit.LoginFailure, "after"))
	})
	for _, c := range []struct {
		log  audit.Log
		n    uint64
		prev [sha256.Size]byte
	}{{audit.LoginLog, 4, f.login(3)}, {audit.ActivityLog, 7, f.activity(6)}} {
		var last audit.Record
		for _, r := range allRecords(t, s) {
			if r.Log == c.log {
				last = r
			}
		}
		if last.N != c.n || last.Prev != hashText(c.prev[:]) {
			t.Errorf("the last record of the %s log is %d with prev %s, want %d with prev %s", c.log, last.N, last.Prev, c.n,
				hashText(c.prev[:]))
		}
	}
	// The events that the writer added are after the hole, and the holes are told where they are in the log now.
	checkVerify(t, s, f.dir, f.events, nil, "", lostNote(A, 6, 6, againFrom(7)), lostNote(L, 3, 3, againFrom(4)))
	checkNoProblem(t, s)
	checkNoProblem(t, openReaderOf(t, f.dir))

	// The heads of the store are those of the new records.
	heads := headsOf(t, s)
	if err := s.VerifyAudit(t.Context(), heads); err != nil {
		t.Errorf("VerifyAudit() with the heads of the store = %v", err)
	}
}

// TestHoleEventsAreSkipped checks that ReadAudit and VerifyAudit skip the event of the daemon for a hole when it is valid,
// and find the ones that are not: as for the other events of the daemon, one that starts as it does and is not one is broken.
func TestHoleEventsAreSkipped(t *testing.T) {
	good := holeEvents(dbwire.LogLogin, 2, 2, wrong)
	f := newChainFixture(t)
	want := recordLines(allRecords(t, f.s))
	appendRawEvents(t, f.s, good...)
	for who, st := range map[string]*Store{"the writer": f.s, "a reader": openReaderOf(t, f.dir)} {
		if got := recordLines(allRecords(t, st)); !slices.Equal(got, want) {
			t.Errorf("the records read by %s = %q, want %q", who, got, want)
		}
	}
	checkVerify(t, f.s, f.dir, f.events, nil, "")

	hole := good[0]
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{"a hole cut short", hole[:len(hole)-1]},
		{"a hole with a byte more", append(slices.Clone(hole), 0)},
		{"a hole of no log", append([]byte{0, 4, 0}, hole[3:]...)},
		{"a hole of a log that does not exist", append([]byte{0, 4, 9}, hole[3:]...)},
		{"a hole that has no first record", append(append([]byte{0, 4, 1}, make([]byte, 8)...), hole[11:]...)},
		{"a hole that ends before it starts", append(append(slices.Clone(hole[:11]), make([]byte, 8)...), hole[19:]...)},
		{"a hole of the kind that is not there", append([]byte{0, 6}, hole[2:]...)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := newChainFixture(t)
			appendRawEvents(t, g.s, tt.data)
			bad := fmt.Sprintf("audit event %d: not a record of the audit log", nextEvent(t, g.s)-1)
			if err := g.s.VerifyAudit(t.Context(), nil); !errors.Is(err, audit.ErrBroken) || err == nil || err.Error() != bad {
				t.Errorf("VerifyAudit() = %v, want %q that wraps audit.ErrBroken", err, bad)
			}
			err := g.s.ReadAudit(t.Context(), audit.Filter{}, func(audit.Record) bool { return true })
			if !errors.Is(err, audit.ErrBroken) || err == nil || err.Error() != bad {
				t.Errorf("ReadAudit() = %v, want %q that wraps audit.ErrBroken", err, bad)
			}
		})
	}
}

// TestGapEventsAreSkipped checks that ReadAudit and VerifyAudit skip the event of the daemon for an accepted gap when it is
// valid, as they skip that of a hole, and find the ones that are not.
func TestGapEventsAreSkipped(t *testing.T) {
	for _, log := range []string{dbwire.LogLogin, dbwire.LogActivity} {
		good := gapEvents(log, 2, 2, wrong)
		f := newChainFixture(t)
		want := recordLines(allRecords(t, f.s))
		appendRawEvents(t, f.s, good...)
		for who, st := range map[string]*Store{"the writer": f.s, "a reader": openReaderOf(t, f.dir)} {
			if got := recordLines(allRecords(t, st)); !slices.Equal(got, want) {
				t.Errorf("the records read by %s = %q, want %q", who, got, want)
			}
		}
		checkVerify(t, f.s, f.dir, f.events, nil, "")
	}

	gap := gapEvents(dbwire.LogLogin, 2, 2, wrong)[0]
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{"a gap cut short", gap[:len(gap)-1]},
		{"a gap with a byte more", append(slices.Clone(gap), 0)},
		{"a gap of no log", append([]byte{0, 5, 0}, gap[3:]...)},
		{"a gap of a log that does not exist", append([]byte{0, 5, 9}, gap[3:]...)},
		{"a gap that has no first record", append(append([]byte{0, 5, 1}, make([]byte, 8)...), gap[11:]...)},
		{"a gap that ends before it starts", append(append(slices.Clone(gap[:11]), make([]byte, 8)...), gap[19:]...)},
		{"the kind 5 of the size of a mark", append([]byte{0, 5}, make([]byte, dbwire.MarkSize-2)...)},
		{"the kind 6 of the size of a gap", append([]byte{0, 6}, gap[2:]...)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := newChainFixture(t)
			appendRawEvents(t, g.s, tt.data)
			bad := fmt.Sprintf("audit event %d: not a record of the audit log", nextEvent(t, g.s)-1)
			if err := g.s.VerifyAudit(t.Context(), nil); !errors.Is(err, audit.ErrBroken) || err == nil || err.Error() != bad {
				t.Errorf("VerifyAudit() = %v, want %q that wraps audit.ErrBroken", err, bad)
			}
			err := g.s.ReadAudit(t.Context(), audit.Filter{}, func(audit.Record) bool { return true })
			if !errors.Is(err, audit.ErrBroken) || err == nil || err.Error() != bad {
				t.Errorf("ReadAudit() = %v, want %q that wraps audit.ErrBroken", err, bad)
			}
		})
	}
}
