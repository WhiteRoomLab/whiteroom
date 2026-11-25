package store

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// The tests of the accepted gaps write the rows of the table chains and the events of the daemon for a gap into the engine as
// whiteroom-db accept-break does, and drop the records that the gap stands for with a rewrite of the engine, as the loss of
// them would: the rule that passes them is that of the holes, and no more is passed than the row says.

func gapRow(log string, first, last uint64, hash [sha256.Size]byte) chainRow {
	return chainRow{dbwire.ChainGapKey(log, first),
		dbwire.AppendChainHole(nil, dbwire.ChainHole{First: first, Last: last, Hash: hash, At: chainTime})}
}

// missingNote is the note of an accepted gap of the records first to last of log, which says after it what follows.
func missingNote(log string, first, last uint64, after string) string {
	missing := fmt.Sprintf("the records %d to %d are", first, last)
	if first == last {
		missing = fmt.Sprintf("record %d is", first)
	}
	return fmt.Sprintf(`audit log %q: %s missing, and the gap was accepted at 2026-11-20T10:20:30Z; %s`, log, missing, after)
}

// miss is the change that drops the records at the positions drop, and accepts the gap for them.
func miss(drop []uint64, log string, first, last uint64, hash [sha256.Size]byte, more ...chainRow) holeChange {
	return holeChange{append([]chainRow{gapRow(log, first, last, hash)}, more...), gapEvents(log, first, last, hash), drop, nil}
}

const unreadableGap = `audit log "login": a row of the table chains for an accepted gap cannot be read`

// TestVerifyAuditAcceptedGaps checks that VerifyAudit and VerifyAuditNotes pass the records that a row of an accepted gap stands
// for, exactly as the row says and no further, and tell each gap at its place in the order of the reads.
func TestVerifyAuditAcceptedGaps(t *testing.T) {
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
		// What an accepted gap passes.
		{"a record in the middle of the login history", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{6}, L, 2, 2, f.login(2))
		}, "", func() []string { return []string{missingNote(L, 2, 2, againFrom(3))} }},
		{"a record in the middle of the activity log", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{4}, A, 3, 3, f.activity(3))
		}, "", func() []string { return []string{missingNote(A, 3, 3, againFrom(4))} }},
		{"two records in the middle", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{4, 5}, A, 3, 4, f.activity(4))
		}, "", func() []string { return []string{missingNote(A, 3, 4, againFrom(5))} }},
		{"the first records of the activity log", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{1, 2}, A, 1, 2, f.activity(2))
		}, "", func() []string { return []string{missingNote(A, 1, 2, againFrom(3))} }},
		{"the first record of the login history", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{3}, L, 1, 1, f.login(1))
		}, "", func() []string { return []string{missingNote(L, 1, 1, againFrom(2))} }},
		{"the last record of the login history", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{9}, L, 3, 3, f.login(3))
		}, "", func() []string { return []string{missingNote(L, 3, 3, endsWith(2))} }},
		{"the last record of the activity log", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{8}, A, 6, 6, f.activity(6))
		}, "", func() []string { return []string{missingNote(A, 6, 6, endsWith(5))} }},
		{"the last two records of the activity log", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{7, 8}, A, 5, 6, f.activity(6))
		}, "", func() []string { return []string{missingNote(A, 5, 6, endsWith(4))} }},
		{"every record of the login history", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{3, 6, 9}, L, 1, 3, f.login(3))
		}, "", func() []string { return []string{missingNote(L, 1, 3, noRecordLeft)} }},
		{"a gap at the end of each log", func(t *testing.T, f chainFixture) holeChange {
			c := miss([]uint64{8, 9}, A, 6, 6, f.activity(6), gapRow(L, 3, 3, f.login(3)))
			c.events = slices.Concat(c.events, gapEvents(L, 3, 3, f.login(3)))
			return c
		}, "", func() []string { return []string{missingNote(L, 3, 3, endsWith(2)), missingNote(A, 6, 6, endsWith(5))} }},
		{"a gap in each log, told in the order of the reads", func(t *testing.T, f chainFixture) holeChange {
			c := miss([]uint64{4, 6}, A, 3, 3, f.activity(3), gapRow(L, 2, 2, f.login(2)))
			c.events = slices.Concat(c.events, gapEvents(L, 2, 2, f.login(2)))
			return c
		}, "", func() []string {
			return []string{missingNote(A, 3, 3, againFrom(4)), missingNote(L, 2, 2, againFrom(3))}
		}},
		{"two gaps in a log", func(t *testing.T, f chainFixture) holeChange {
			c := miss([]uint64{2, 5}, A, 2, 2, f.activity(2), gapRow(A, 4, 4, f.activity(4)))
			c.events = slices.Concat(c.events, gapEvents(A, 4, 4, f.activity(4)))
			return c
		}, "", func() []string {
			return []string{missingNote(A, 2, 2, againFrom(3)), missingNote(A, 4, 4, againFrom(5))}
		}},
		{"a gap in the middle and a gap at the end", func(t *testing.T, f chainFixture) holeChange {
			c := miss([]uint64{2, 8}, A, 2, 2, f.activity(2), gapRow(A, 6, 6, f.activity(6)))
			c.events = slices.Concat(c.events, gapEvents(A, 6, 6, f.activity(6)))
			return c
		}, "", func() []string {
			return []string{missingNote(A, 2, 2, againFrom(3)), missingNote(A, 6, 6, endsWith(5))}
		}},
		{"a hole and a gap in one log", func(t *testing.T, f chainFixture) holeChange {
			c := miss([]uint64{2, 5}, A, 4, 4, f.activity(4), holeRow(A, 2, 2, f.activity(2)))
			c.events = slices.Concat(c.events, holeEvents(A, 2, 2, f.activity(2)))
			return c
		}, "", func() []string { return []string{lostNote(A, 2, 2, againFrom(3)), missingNote(A, 4, 4, againFrom(5))} }},
		{"a hole in one log and a gap in the other", func(t *testing.T, f chainFixture) holeChange {
			c := miss([]uint64{6, 7}, L, 2, 2, f.login(2), holeRow(A, 5, 5, f.activity(5)))
			c.events = slices.Concat(c.events, holeEvents(A, 5, 5, f.activity(5)))
			return c
		}, "", func() []string { return []string{lostNote(A, 5, 5, againFrom(6)), missingNote(L, 2, 2, againFrom(3))} }},
		{"a hole that does not pass, and a gap at the same first that does", func(t *testing.T, f chainFixture) holeChange {
			c := miss([]uint64{4, 5}, A, 3, 4, f.activity(4), holeRow(A, 3, 3, f.activity(3)))
			c.events = slices.Concat(c.events, holeEvents(A, 3, 3, f.activity(3)))
			return c
		}, "", func() []string { return []string{missingNote(A, 3, 4, againFrom(5))} }},
		{"a hole and a gap that both pass: the hole is told", func(t *testing.T, f chainFixture) holeChange {
			c := miss([]uint64{4}, A, 3, 3, f.activity(3), holeRow(A, 3, 3, f.activity(3)))
			c.events = slices.Concat(c.events, holeEvents(A, 3, 3, f.activity(3)))
			return c
		}, "", func() []string { return []string{lostNote(A, 3, 3, againFrom(4))} }},
		{"a gap at the chain start of the login history", func(t *testing.T, f chainFixture) holeChange {
			// The chain starts at the record 2, and the record 2 is lost: the first record that is read is the record 3.
			c := miss([]uint64{3, 6}, L, 2, 2, f.login(2), startRow(2, f.login(1)))
			c.events = slices.Concat(deletedEvents(2, f.login(1)), c.events)
			return c
		}, "", func() []string { return []string{startNote(2), missingNote(L, 2, 2, againFrom(3))} }},
		{"a gap at the end of the login history after the chain start", func(t *testing.T, f chainFixture) holeChange {
			// The records 1 and 2 are deleted and the record 3, the last, is lost: no record of the log is left.
			c := miss([]uint64{3, 6, 9}, L, 3, 3, f.login(3), startRow(3, f.login(2)))
			c.events = slices.Concat(deletedEvents(3, f.login(2)), c.events)
			return c
		}, "", func() []string { return []string{startNote(3), missingNote(L, 3, 3, noRecordLeft)} }},
		{"a gap after the records that a deletion left", func(t *testing.T, f chainFixture) holeChange {
			// The chain starts at the record 3 and the records 1 and 2 are still there; the record 3 is lost, and no record follows.
			c := miss([]uint64{9}, L, 3, 3, f.login(3), startRow(3, f.login(2)))
			c.events = slices.Concat(deletedEvents(3, f.login(2)), c.events)
			return c
		}, "", func() []string {
			return []string{startNote(3), missingNote(L, 3, 3, endsWith(2)), leftNote("the records 1 to 2 are", 3)}
		}},
		{"a gap, and then a break", func(t *testing.T, f chainFixture) holeChange {
			// The record 2 is lost, and the record 4 is another one, so that the record 5 is a break that is accepted.
			c := miss([]uint64{2}, A, 2, 2, f.activity(2), breakRow(A, 5, f.activity(5)))
			c.events = slices.Concat(c.events, acceptedEvents(A, 5, f.activity(5)))
			c.replace = map[uint64][]byte{5: changedRecord(t, f.events[4].Data, func(r *audit.Record) { r.Detail = map[string]string{"event": "X"} })}
			return c
		}, "", func() []string { return []string{missingNote(A, 2, 2, againFrom(3)), breakNote(A, 5)} }},
		{"a break that is accepted for the record after a gap is of no use", func(t *testing.T, f chainFixture) holeChange {
			c := miss([]uint64{2}, A, 2, 2, f.activity(2), breakRow(A, 3, f.activity(3)))
			c.events = slices.Concat(c.events, acceptedEvents(A, 3, f.activity(3)))
			return c
		}, "", func() []string { return []string{missingNote(A, 2, 2, againFrom(3))} }},

		// What it does not pass.
		{"a row that is one record wider", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{4}, A, 3, 4, f.activity(4))
		}, `audit log "activity": record 4 (event 5): the number does not follow that of record 2`, nil},
		{"a row that is one record narrower", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{4, 5}, A, 3, 3, f.activity(3))
		}, `audit log "activity": record 5 (event 7): the number does not follow that of record 2`, nil},
		{"a row that starts one record earlier", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{4}, A, 2, 3, f.activity(3))
		}, `audit log "activity": record 4 (event 5): the number does not follow that of record 2`, nil},
		{"a row that starts one record later", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{4}, A, 4, 4, f.activity(3))
		}, `audit log "activity": record 4 (event 5): the number does not follow that of record 2`, nil},
		{"a row with the hash of another record", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{4}, A, 3, 3, f.activity(4))
		}, `audit log "activity": record 4 (event 5): the number does not follow that of record 2`, nil},
		{"a row with a hash that no record has", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{4}, A, 3, 3, wrong)
		}, `audit log "activity": record 4 (event 5): the number does not follow that of record 2`, nil},
		{"a row of the other log", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{4}, L, 3, 3, f.activity(3))
		}, `audit log "activity": record 4 (event 5): the number does not follow that of record 2`, nil},
		{"a row of the other log, with the number of a record that it has", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{6}, A, 2, 2, f.login(2))
		}, `audit log "login": record 3 (event 9): the number does not follow that of record 1`, nil},
		{"the prev of the record after the gap was rewritten", func(t *testing.T, f chainFixture) holeChange {
			c := miss([]uint64{4}, A, 3, 3, f.activity(3))
			c.replace = map[uint64][]byte{5: changedRecord(t, f.events[4].Data, func(r *audit.Record) { r.Prev = hashText(wrong[:]) })}
			return c
		}, `audit log "activity": record 4 (event 5): the number does not follow that of record 2`, nil},
		{"the record after the gap was rewritten, which the record after it does not follow", func(t *testing.T, f chainFixture) holeChange {
			c := miss([]uint64{2}, A, 2, 2, f.activity(2))
			c.replace = map[uint64][]byte{4: changedRecord(t, f.events[3].Data, func(r *audit.Record) { r.Detail = map[string]string{"event": "X"} })}
			return c
		}, `audit log "activity": record 4 (event 5): prev is not the hash of record 3`,
			func() []string { return []string{missingNote(A, 2, 2, againFrom(3))} }},
		{"the record at the start of the log, with a row for the one before it", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{1, 2}, A, 1, 1, f.activity(2))
		}, `audit log "activity": record 3 (event 4): the first record of a log is number 1`, nil},
		{"a gap at the end with another hash", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{9}, L, 3, 3, wrong)
		}, `audit log "login": the head in the table is record 3, but the last record is record 2 (event 6)`, nil},
		{"a gap at the end with the hash of the record before", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{9}, L, 3, 3, f.login(2))
		}, `audit log "login": the head in the table is record 3, but the last record is record 2 (event 6)`, nil},
		{"a gap at the end that ends before the head", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{7, 8}, A, 5, 5, f.activity(5))
		}, `audit log "activity": the head in the table is record 6, but the last record is record 4 (event 5)`, nil},
		{"a gap at the end that starts after the last record", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{7, 8}, A, 6, 6, f.activity(6))
		}, `audit log "activity": the head in the table is record 6, but the last record is record 4 (event 5)`, nil},
		{"a gap at the end that starts before the last record", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{8}, A, 5, 6, f.activity(6))
		}, `audit log "activity": the head in the table is record 6, but the last record is record 5 (event 7)`, nil},
		{"a gap at the end of the other log", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{9}, A, 3, 3, f.login(3))
		}, `audit log "login": the head in the table is record 3, but the last record is record 2 (event 6)`, nil},
		{"a gap at the end that is passed by the head of the other log", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{9}, L, 3, 3, f.activity(6))
		}, `audit log "login": the head in the table is record 3, but the last record is record 2 (event 6)`, nil},
		{"every record of the activity log, which a gap does not excuse", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{1, 2, 4, 5, 7, 8}, A, 1, 6, f.activity(6))
		}, noActivity, func() []string { return nil }},
		{"every record of the login history, with a gap that does not cover the head", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{3, 6, 9}, L, 1, 2, f.login(2))
		}, `audit log "login": the table has the head of record 3, but the log has no record`, nil},
		{"a gap at the end, with the chain start past it", func(t *testing.T, f chainFixture) holeChange {
			c := miss([]uint64{3, 6, 9}, L, 3, 3, f.login(3), startRow(4, f.login(3)))
			c.events = slices.Concat(deletedEvents(4, f.login(3)), c.events)
			return c
		}, "", func() []string { return []string{startNote(4)} }},

		// A row that cannot be read.
		{"a row with a value that is no gap", func(t *testing.T, f chainFixture) holeChange {
			return holeChange{rows: []chainRow{{dbwire.ChainGapKey(L, 2), []byte("abc")}}}
		}, unreadableGap, func() []string { return nil }},
		{"a row with the value of a break", func(t *testing.T, f chainFixture) holeChange {
			return holeChange{rows: []chainRow{{dbwire.ChainGapKey(L, 2),
				dbwire.AppendChainBreak(nil, dbwire.ChainBreak{Hash: f.login(2), At: chainTime})}}}
		}, unreadableGap, func() []string { return nil }},
		{"a row of the record 0", func(t *testing.T, f chainFixture) holeChange {
			row := gapRow(L, 1, 2, f.login(2))
			row.key = dbwire.ChainGapKey(L, 0)
			return holeChange{rows: []chainRow{row}}
		}, unreadableGap, func() []string { return nil }},
		{"a row that ends before it starts", func(t *testing.T, f chainFixture) holeChange {
			return holeChange{rows: []chainRow{{dbwire.ChainGapKey(L, 5),
				dbwire.AppendChainHole(nil, dbwire.ChainHole{Last: 4, Hash: f.login(2), At: chainTime})}}}
		}, unreadableGap, func() []string { return nil }},
		{"a row with a key that is too short", func(t *testing.T, f chainFixture) holeChange {
			return holeChange{rows: []chainRow{{dbwire.ChainGapKey(L, 2)[:12], gapRow(L, 2, 2, f.login(2)).value}}}
		}, unreadableGap, func() []string { return nil }},
		{"a row with a key that is too long", func(t *testing.T, f chainFixture) holeChange {
			return holeChange{rows: []chainRow{{append(dbwire.ChainGapKey(L, 2), 0), gapRow(L, 2, 2, f.login(2)).value}}}
		}, unreadableGap, func() []string { return nil }},
		{"a row of a log with a longer name", func(t *testing.T, f chainFixture) holeChange {
			return holeChange{rows: []chainRow{{append([]byte("login\x02\x02"), dbwire.ChainGapKey(L, 2)[6:]...),
				gapRow(L, 2, 2, f.login(2)).value}}}
		}, unreadableGap, func() []string { return nil }},
		{"a row of a break and a row of a gap that cannot be read", func(t *testing.T, f chainFixture) holeChange {
			return holeChange{rows: []chainRow{{dbwire.ChainBreakKey(L, 2), []byte("abc")}, {dbwire.ChainGapKey(L, 2), []byte("abc")}}}
		}, `audit log "login": a row of the table chains for an accepted break cannot be read`, func() []string { return nil }},
		{"a row of a hole and a row of a gap that cannot be read", func(t *testing.T, f chainFixture) holeChange {
			return holeChange{rows: []chainRow{{dbwire.ChainHoleKey(L, 2), []byte("abc")}, {dbwire.ChainGapKey(L, 2), []byte("abc")}}}
		}, unreadableHole, func() []string { return nil }},
		{"a row of the activity log that cannot be read", func(t *testing.T, f chainFixture) holeChange {
			return holeChange{rows: []chainRow{{dbwire.ChainGapKey(A, 2), []byte("abc")}}}
		}, `audit log "activity": a row of the table chains for an accepted gap cannot be read`, func() []string { return nil }},
		{"a row that cannot be read, after one that passes", func(t *testing.T, f chainFixture) holeChange {
			return holeChange{rows: []chainRow{gapRow(L, 2, 2, f.login(2)), {dbwire.ChainGapKey(L, 3), []byte("abc")}},
				events: gapEvents(L, 2, 2, f.login(2)), drop: []uint64{6}}
		}, unreadableGap, func() []string { return nil }},
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

// TestVerifyAuditGapBeforeChainStart checks the gap between the records that a deletion left and the chain start, which only an
// accepted gap passes: VerifyAudit expects the record after the last record that it has read, which is before the chain start.
func TestVerifyAuditGapBeforeChainStart(t *testing.T) {
	const L = string(audit.LoginLog)
	// The chain starts at the record 3, the record 1 is still there, and the record 2 is lost.
	build := func(t *testing.T, row bool) chainFixture {
		f := newChainFixture(t)
		rows := []chainRow{startRow(3, f.login(2))}
		events := deletedEvents(3, f.login(2))
		if row {
			rows = append(rows, gapRow(L, 2, 2, f.login(2)))
			events = slices.Concat(events, gapEvents(L, 2, 2, f.login(2)))
		}
		injectChains(t, f.s, rows, events...)
		f.s = closeAndRewrite(t, f.s, f.dir, rewriteRecords(nil, []uint64{6}, nil))
		return f
	}
	f := build(t, false)
	checkVerify(t, f.s, f.dir, f.events, nil, `audit log "login": record 3 (event 9): the number does not follow that of record 1`, startNote(3))
	f = build(t, true)
	// The note of the records that the deletion left counts the record 2, which is missing, among them: the walk of VerifyAudit
	// takes the records from the first that it read to the one before the chain start for what the deletion left.
	checkVerify(t, f.s, f.dir, f.events, nil, "", startNote(3), missingNote(L, 2, 2, againFrom(3)), leftNote("the records 1 to 2 are", 3))
}

// TestVerifyAuditGapAnchors checks that a head kept outside the store that names a record that an accepted gap stands for is a
// problem, as is one that names a record that the log does not have, and that the anchors of the records that are there are
// checked.
func TestVerifyAuditGapAnchors(t *testing.T) {
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
		{"the hash of a record in a gap", func(t *testing.T, f chainFixture) holeChange { return miss([]uint64{6}, L, 2, 2, f.login(2)) },
			func(f chainFixture) []audit.Head { return []audit.Head{f.head(audit.LoginLog, 2)} },
			`audit log "login": the head kept outside the store is record 2, which the log does not have`,
			func() []string { return []string{missingNote(L, 2, 2, againFrom(3))} }},
		{"the hash of a record in a gap of two", func(t *testing.T, f chainFixture) holeChange { return miss([]uint64{4, 5}, A, 3, 4, f.activity(4)) },
			func(f chainFixture) []audit.Head { return []audit.Head{f.head(audit.ActivityLog, 3)} },
			`audit log "activity": the head kept outside the store is record 3, which the log does not have`,
			func() []string { return []string{missingNote(A, 3, 4, againFrom(5))} }},
		{"the heads of the records that are there", func(t *testing.T, f chainFixture) holeChange { return miss([]uint64{6}, L, 2, 2, f.login(2)) },
			func(f chainFixture) []audit.Head {
				return []audit.Head{f.head(audit.LoginLog, 1), f.head(audit.LoginLog, 3), f.head(audit.ActivityLog, 6)}
			}, "", func() []string { return []string{missingNote(L, 2, 2, againFrom(3))} }},
		{"the hash of a record after the gap, which is another one", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{6}, L, 2, 2, f.login(2))
		}, func(f chainFixture) []audit.Head { return []audit.Head{{Log: audit.LoginLog, N: 3, Hash: wrong}} },
			`audit log "login": record 3 (event 9): its hash is not that of the head kept outside the store`,
			func() []string { return []string{missingNote(L, 2, 2, againFrom(3))} }},
		{"the head of a record at the end that is missing", func(t *testing.T, f chainFixture) holeChange { return miss([]uint64{9}, L, 3, 3, f.login(3)) },
			func(f chainFixture) []audit.Head { return f.heads },
			`audit log "login": the head kept outside the store is record 3, but the log ends with record 2 (event 6)`,
			func() []string { return []string{missingNote(L, 3, 3, endsWith(2))} }},
		{"the head of a record at the end of the activity log that is missing", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{7, 8}, A, 5, 6, f.activity(6))
		}, func(f chainFixture) []audit.Head { return []audit.Head{f.head(audit.ActivityLog, 5)} },
			`audit log "activity": the head kept outside the store is record 5, but the log ends with record 4 (event 5)`,
			func() []string { return []string{missingNote(A, 5, 6, endsWith(4))} }},
		{"the head of a record before a gap at the end", func(t *testing.T, f chainFixture) holeChange { return miss([]uint64{9}, L, 3, 3, f.login(3)) },
			func(f chainFixture) []audit.Head { return []audit.Head{f.head(audit.LoginLog, 2)} }, "",
			func() []string { return []string{missingNote(L, 3, 3, endsWith(2))} }},
		{"the head of a record when no record is left", func(t *testing.T, f chainFixture) holeChange {
			return miss([]uint64{3, 6, 9}, L, 1, 3, f.login(3))
		}, func(f chainFixture) []audit.Head { return []audit.Head{f.head(audit.LoginLog, 3)} },
			`audit log "login": the head kept outside the store is record 3, but the log has no record`,
			func() []string { return []string{missingNote(L, 1, 3, noRecordLeft)} }},
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

// TestVerifyAuditGapThenWriter checks that a gap at the end of a log stays passed when the writer goes on: its next record has the
// hash of the record that is missing, which the head in the table still has, as its prev, so that the gap is now one in the
// middle, and the row that whiteroom-db accept-break wrote is still the one that passes it.
func TestVerifyAuditGapThenWriter(t *testing.T) {
	const (
		L = string(audit.LoginLog)
		A = string(audit.ActivityLog)
	)
	f := newChainFixture(t)
	// The last records of both logs are missing.
	events := slices.Concat(gapEvents(L, 3, 3, f.login(3)), gapEvents(A, 6, 6, f.activity(6)))
	injectChains(t, f.s, []chainRow{gapRow(L, 3, 3, f.login(3)), gapRow(A, 6, 6, f.activity(6))}, events...)
	s := closeAndRewrite(t, f.s, f.dir, rewriteRecords(nil, []uint64{8, 9}, nil))
	checkVerify(t, s, f.dir, f.events, nil, "", missingNote(L, 3, 3, endsWith(2)), missingNote(A, 6, 6, endsWith(5)))

	// The writer goes on: the next records are 4 and 7, and have the hashes of the records that are missing.
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
	// The events that the writer added are after the gap, and the gaps are told where they are in the log now.
	checkVerify(t, s, f.dir, f.events, nil, "", missingNote(A, 6, 6, againFrom(7)), missingNote(L, 3, 3, againFrom(4)))
	checkNoProblem(t, s)
	checkNoProblem(t, openReaderOf(t, f.dir))

	// The heads of the store are those of the new records.
	if err := s.VerifyAudit(t.Context(), headsOf(t, s)); err != nil {
		t.Errorf("VerifyAudit() with the heads of the store = %v", err)
	}
}

// TestVerifyAuditGapAtTheEndOfAnEmptyLog checks the gap at the end of a log that has no record at all: the login history passes,
// and the activity log, which always has a record, is a problem until the writer has added the next one.
func TestVerifyAuditGapAtTheEndOfAnEmptyLog(t *testing.T) {
	const (
		L = string(audit.LoginLog)
		A = string(audit.ActivityLog)
	)
	f := newChainFixture(t)
	events := slices.Concat(gapEvents(L, 1, 3, f.login(3)), gapEvents(A, 1, 6, f.activity(6)))
	injectChains(t, f.s, []chainRow{gapRow(L, 1, 3, f.login(3)), gapRow(A, 1, 6, f.activity(6))}, events...)
	s := closeAndRewrite(t, f.s, f.dir, rewriteRecords(nil, []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9}, nil))
	checkVerify(t, s, f.dir, f.events, nil, noActivity, missingNote(L, 1, 3, noRecordLeft))
	// The writer's next record is the record 7 of the activity log, and the record 4 of the login history.
	update(t, s, func(tx *Tx) error {
		if err := tx.Record(eventRecord("after")); err != nil {
			return err
		}
		return tx.Record(loginRecord(audit.LoginFailure, "after"))
	})
	checkVerify(t, s, f.dir, f.events, nil, "", missingNote(A, 1, 6, againFrom(7)), missingNote(L, 1, 3, againFrom(4)))
}
