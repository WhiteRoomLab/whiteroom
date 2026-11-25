package store

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// recordEvent returns the event of the record n of log in s.
func recordEvent(t *testing.T, s *Store, log audit.Log, n uint64) logdb.Event {
	t.Helper()
	for _, e := range rawEvents(t, s) {
		if r, err := audit.Decode(e.Data); err == nil && r.Log == log && r.N == n {
			return e
		}
	}
	t.Fatalf("no event of the record %d of the %s log", n, log)
	return logdb.Event{}
}

// rewriteRecord closes s, makes the record n of log what change makes of it, and opens the store again.
func rewriteRecord(t *testing.T, s *Store, dir string, log audit.Log, n uint64, change func(r *audit.Record)) *Store {
	t.Helper()
	e := recordEvent(t, s, log, n)
	return closeAndRewrite(t, s, dir, rewriteEvents(map[uint64][]byte{e.Number: changedRecord(t, e.Data, change)}, nil, nil))
}

// dropRecords closes s, drops the records numbered ns of log, and opens the store again.
func dropRecords(t *testing.T, s *Store, dir string, log audit.Log, ns ...uint64) *Store {
	t.Helper()
	var numbers []uint64
	for _, n := range ns {
		numbers = append(numbers, recordEvent(t, s, log, n).Number)
	}
	return closeAndRewrite(t, s, dir, rewriteEvents(nil, numbers, nil))
}

// TestAuditBreakRule pins together the three places that decide whether a break that is accepted for a record is honored, and
// whether a hole that a repair recorded, or a gap that is accepted, passes the record after it: whiteroom-db accept-break,
// VerifyAudit and whiteroom-db compact. A break is a record whose number is that of the one before it plus one and whose prev is
// not its hash, and the first record that is read when it is the one that the log starts with (record 1 with a prev, or the first
// that remains after a deletion, whose prev is not what the chain start has), unless records before it that the deletion left
// are still there. A hole passes the
// record that comes after the records that its row says are lost, with the hash of the last of them as its prev, where the log is
// expected to go on; so does a gap that accept-break accepted, which any record whose number is above that and that no row passes
// can have: the number gaps (in the middle of a log, at its start, at the chain start, at its end, of two records), the rows
// that do not match, which the acceptance replaces, and a gap after a hole, or one that is accepted twice, or whose record has no
// hash as its prev, which it refuses. Each case is run through all three, and they must agree: a record that accept-break
// accepts is gone by VerifyAudit and passed by the walk of compact; one that it refuses is not, if its row is put in all the same,
// as one that the record had before it was changed or its log was, unless a hole or a gap passes it, and compact never turns a
// store that VerifyAudit fails into one that it passes, but for the break at the first record that remains when records before it
// are still there: compact drops them, and the break is honored. The rows and the records around that first record (K) are
// cases of their own: rows that follow one another, a row that holds K, a record K-1 that is not the one that the chain start
// has, and the rows at the end of the log, whether the records that a deletion left are before K or not, so that the three
// places go on stepping across K by one rule.
func TestAuditBreakRule(t *testing.T) {
	loginChange := func(r *audit.Record) { r.Login = "forged" }
	activityChange := func(r *audit.Record) { r.Detail = map[string]string{"event": "X"} }
	withPrev := func(r *audit.Record) { r.Prev = hashText(wrong[:]) }
	noHash := func(r *audit.Record) { r.Prev = "sha256:" + strings.Repeat("A", sha256.Size*2) }
	acceptedGap := func(n, first, last int) string {
		if first == last {
			return fmt.Sprintf("record %d comes after the record %d whose gap is accepted already", n, first)
		}
		return fmt.Sprintf("record %d comes after the records %d to %d whose gap is accepted already", n, first, last)
	}
	noHashText := func(n, before int) string {
		return fmt.Sprintf("record %d does not follow record %d in number, and its prev is not the hash of a record (sha256: and 64 lower "+
			"case hexadecimal digits), so the gap before it cannot be accepted", n, before)
	}
	holeText := func(n, first, last int) string {
		if first == last {
			return fmt.Sprintf("record %d comes after the record %d that whiteroom-db repair dropped: there is no break to accept", n, first)
		}
		return fmt.Sprintf("record %d comes after the records %d to %d that whiteroom-db repair dropped: there is no break to accept",
			n, first, last)
	}
	absent := func(log string, n int) string {
		return fmt.Sprintf("record %d of the audit log %s is not in the store", n, log)
	}
	// holeOf drops the records ns of log, and puts the row for the records first to last of rowLog with the hash that the record
	// hashOf has in log (the hash that no record has if it is 0), as the repair would for the records that it has dropped. The
	// row is made before the records go.
	holeOf := func(rowLog, log audit.Log, ns []uint64, first, last, hashOf uint64) func(t *testing.T, s *Store, dir string) *Store {
		return func(t *testing.T, s *Store, dir string) *Store {
			t.Helper()
			sum := wrong
			if hashOf != 0 {
				sum = sha256.Sum256(recordEvent(t, s, log, hashOf).Data)
			}
			s = dropRecords(t, s, dir, log, ns...)
			injectChains(t, s, []chainRow{holeRow(string(rowLog), first, last, sum)}, holeEvents(string(rowLog), first, last, sum)...)
			return s
		}
	}
	// gapOf is holeOf for the row of an accepted gap and its event.
	gapOf := func(rowLog, log audit.Log, ns []uint64, first, last, hashOf uint64) func(t *testing.T, s *Store, dir string) *Store {
		return func(t *testing.T, s *Store, dir string) *Store {
			t.Helper()
			sum := wrong
			if hashOf != 0 {
				sum = sha256.Sum256(recordEvent(t, s, log, hashOf).Data)
			}
			s = dropRecords(t, s, dir, log, ns...)
			injectChains(t, s, []chainRow{gapRow(string(rowLog), first, last, sum)}, gapEvents(string(rowLog), first, last, sum)...)
			return s
		}
	}
	// drop drops the records ns of log, which makes a gap.
	drop := func(log audit.Log, ns ...uint64) func(t *testing.T, s *Store, dir string) *Store {
		return func(t *testing.T, s *Store, dir string) *Store { return dropRecords(t, s, dir, log, ns...) }
	}
	// accepting is a prepare that makes what inner makes, and accepts what is at the record n of log, as whiteroom-db accept-break
	// does.
	accepting := func(inner func(t *testing.T, s *Store, dir string) *Store, log audit.Log, n uint64) func(t *testing.T, s *Store, dir string) *Store {
		return func(t *testing.T, s *Store, dir string) *Store {
			t.Helper()
			return closeAcceptReopen(t, inner(t, s, dir), dir, log, n)
		}
	}
	// writes adds a login record, as the writer does.
	writes := func(t *testing.T, s *Store, dir string) *Store {
		t.Helper()
		update(t, s, func(tx *Tx) error { return tx.Record(loginRecord(audit.LoginFailure, "after")) })
		return s
	}
	// lost is holeOf for the hole that is right: the row is of the log, and for the records that are dropped, with the hash of
	// the last of them.
	lost := func(log audit.Log, first, last uint64) func(t *testing.T, s *Store, dir string) *Store {
		var ns []uint64
		for n := first; n <= last; n++ {
			ns = append(ns, n)
		}
		return holeOf(log, log, ns, first, last, last)
	}
	leftText := func(n int) string {
		return fmt.Sprintf("record %d comes after records before it that an earlier deletion left, which are still in the store: "+
			"whiteroom-db compact drops them if they are what the deletion left; accept the break at record %d then", n, n)
	}
	startHashText := func(n int) string {
		return fmt.Sprintf("record %d has as its prev the hash that the chain start has of record %d: there is no break to accept", n, n-1)
	}
	// alsoAccept is an after that accepts the records ns of log, whose prev the rewrite of the one before them has made another.
	alsoAccept := func(log audit.Log, ns ...uint64) func(t *testing.T, s *Store, dir string) *Store {
		return func(t *testing.T, s *Store, dir string) *Store {
			t.Helper()
			for _, n := range ns {
				s = closeAcceptReopen(t, s, dir, log, n)
			}
			return s
		}
	}
	// Six login records, 40 to 35 months old, and the activity records that go with them, in a store that is of today.
	allOld := func(t *testing.T) (*Store, string) {
		s, dir, _ := agedStore(t, []int{40, 39, 38, 37, 36, 35, 0}, []bool{true, true, true, true, true, true, false})
		return s, dir
	}
	// The same, but the records 4 to 6 are young: a deletion drops the records 1 to 3, and the chain starts at the record 4.
	threeOld := func(t *testing.T) (*Store, string) {
		s, dir, _ := agedStore(t, []int{40, 39, 38, 3, 2, 1, 0}, []bool{true, true, true, true, true, true, false})
		return s, dir
	}
	// The records 1 and 2 are old, and the records 3 and 4 are young; and the records 1 to 3 are old, and the records 4 and 5 are
	// young: the cut of a deletion lands at the record 3 and just after it.
	twoOld := func(t *testing.T) (*Store, string) {
		s, dir, _ := agedStore(t, []int{40, 39, 5, 4, 0}, []bool{true, true, true, true, false})
		return s, dir
	}
	threeOldOfFive := func(t *testing.T) (*Store, string) {
		s, dir, _ := agedStore(t, []int{40, 39, 38, 5, 4, 0}, []bool{true, true, true, true, true, false})
		return s, dir
	}
	deleteThree := func(t *testing.T, s *Store, dir string) *Store {
		t.Helper()
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if d, err := compactLoginOf(t, dir, chainTime, nil); err != nil || d.First != 1 || d.Last != 3 {
			t.Fatalf("CompactLogin() = %+v, %v, want the records 1 to 3", d, err)
		}
		return reopenLog(t, dir, logdb.Options{})
	}
	leaveThree := func(t *testing.T, s *Store, dir string) *Store {
		t.Helper()
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		hookErr := errors.New("hook failed")
		if _, err := compactLoginOf(t, dir, chainTime, func(point string) error {
			if point == "compact:base" {
				return hookErr
			}
			return nil
		}); !errors.Is(err, hookErr) {
			t.Fatalf("CompactLogin() = %v, want the error of the hook", err)
		}
		return reopenLog(t, dir, logdb.Options{})
	}
	// Eight login records, 40 to 35 months old and then 3 and 2: the chain starts at the record 7 when the records 1 to 6 go.
	eightOld := func(t *testing.T) (*Store, string) {
		s, dir, _ := agedStore(t, []int{40, 39, 38, 37, 36, 35, 3, 2, 0}, []bool{true, true, true, true, true, true, true, true, false})
		return s, dir
	}
	// rows are the holes (or the gaps) of the records first to last that the repair (or accept-break) leaves, with the hash of the
	// record whose number is hashOf, for the records that are dropped.
	type rowSpec struct{ first, last, hashOf uint64 }
	// across is a prepare that leaves the records before the chain start (leaveThree), drops the records ns, and puts the rows, by the
	// hash that the records have before they are dropped.
	across := func(gap bool, ns []uint64, rows ...rowSpec) func(t *testing.T, s *Store, dir string) *Store {
		return func(t *testing.T, s *Store, dir string) *Store {
			t.Helper()
			s = leaveThree(t, s, dir)
			sums := make([][sha256.Size]byte, len(rows))
			for i, r := range rows {
				sums[i] = wrong
				if r.hashOf != 0 {
					sums[i] = sha256.Sum256(recordEvent(t, s, audit.LoginLog, r.hashOf).Data)
				}
			}
			s = dropRecords(t, s, dir, audit.LoginLog, ns...)
			for i, r := range rows {
				if gap {
					injectChains(t, s, []chainRow{gapRow("login", r.first, r.last, sums[i])}, gapEvents("login", r.first, r.last, sums[i])...)
				} else {
					injectChains(t, s, []chainRow{holeRow("login", r.first, r.last, sums[i])}, holeEvents("login", r.first, r.last, sums[i])...)
				}
			}
			return s
		}
	}
	notInStore := func(n, k int) string {
		return fmt.Sprintf("record %d of the audit log login is not in the store: the records before record %d were deleted", n, k)
	}
	changed3 := func(r *audit.Record) { r.Login = "forged" }
	crossing := "record 5 comes after records before it that an earlier deletion left, the last of which, record 3, does not have the " +
		"hash that the chain start has of it: the gap before record 5 is not accepted"
	notHash := "its hash is not the hash that the chain start has of it"
	replay4 := func(t *testing.T, s *Store, dir string) *Store {
		t.Helper()
		rec := recordEvent(t, s, audit.LoginLog, 4).Data
		replaceHead(t, s, audit.LoginLog, 4, sha256.Sum256(rec), rec)
		update(t, s, func(tx *Tx) error { return tx.Record(loginRecord(audit.LoginFailure, "forked")) })
		return s
	}
	const none = "-" // the log has no deletion, so that no walk of compact is made

	for _, tt := range []struct {
		name  string
		log   audit.Log
		build func(t *testing.T) (*Store, string)
		// prepare makes the record n the one that VerifyAudit reports, and returns the open store.
		prepare func(t *testing.T, s *Store, dir string) *Store
		n       uint64
		refused string // the error of AcceptBreak, "" if it accepts the break or the gap
		// accepted is what AcceptBreak accepts if it does: the zero value is a break.
		accepted dbserver.Acceptance
		// plant puts the row that the acceptance would have put, for a case that it refuses and that is not a break; nil: the row
		// of the break at the record n.
		plant func(t *testing.T, s *Store)
		// after changes the store when the acceptance, if there is one, has been made.
		after  func(t *testing.T, s *Store, dir string) *Store
		verify string // the error of VerifyAudit after that, up to the number of the event, "" if it passes
		// stop is where the walk of compact stops, as Deleted.Stopped says it (its start), "" if it does not.
		stop string
		// last is the last record that compact drops if the walk does not stop (6 if 0), and remains the first login record that
		// is left then (0: not looked at). A deletion does not leave a break at the start of the chain, which is not honored.
		last, remains uint64
		// mends is that compact makes VerifyAudit pass, which fails before it, as the break at the first record that remains is
		// honored once the records before it are gone.
		mends bool
		// holed is whether VerifyAudit passes the store before any acceptance, which a hole that is right does. from is the first
		// record that compact drops (1 if 0), and nothing is that it drops none and does not stop. gone is that the record n is
		// not in the store, so that there is no row to put in for it.
		holed, nothing, gone bool
		from                 uint64
	}{
		{name: "prev rewritten, the number follows", log: audit.LoginLog, build: allOld, n: 4,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, s, dir, audit.LoginLog, 3, loginChange)
			}},
		{name: "prev rewritten, the number follows, in the activity log", log: audit.ActivityLog, build: allOld, n: 4, stop: none,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, s, dir, audit.ActivityLog, 3, activityChange)
			}},
		{name: "a duplicate of an accepted record", log: audit.LoginLog, build: allOld, n: 4,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, s, dir, audit.LoginLog, 3, loginChange)
			},
			after: replay4, verify: "the number does not follow that of record 6", stop: "record 4: the number does not follow that of record 6"},
		{name: "the cut of a deletion lands at the record of an accepted break", log: audit.LoginLog, build: twoOld, n: 3, last: 1, remains: 2,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, s, dir, audit.LoginLog, 2, loginChange)
			}},
		{name: "the cut of a deletion lands at the record of an accepted break, with the records before it old", log: audit.LoginLog,
			build: threeOld, n: 4, last: 2, remains: 3,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, s, dir, audit.LoginLog, 3, loginChange)
			}},
		{name: "the cut of a deletion lands just after the record of an accepted break", log: audit.LoginLog, build: threeOldOfFive, n: 3,
			last: 3, remains: 4,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, s, dir, audit.LoginLog, 2, loginChange)
			}},
		{name: "an accepted record that later is the first that is read", log: audit.LoginLog, build: allOld, n: 4,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, s, dir, audit.LoginLog, 3, loginChange)
			},
			after: func(t *testing.T, s *Store, dir string) *Store {
				return dropRecords(t, s, dir, audit.LoginLog, 1, 2, 3)
			},
			verify: "the first record of a log is number 1", stop: "record 4: the first record of a log is number 1"},
		{name: "an accepted record that later is the first that is read, in the activity log", log: audit.ActivityLog, build: allOld, n: 4,
			stop: none,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, s, dir, audit.ActivityLog, 3, activityChange)
			},
			after: func(t *testing.T, s *Store, dir string) *Store {
				return dropRecords(t, s, dir, audit.ActivityLog, 1, 2, 3)
			},
			verify: "the first record of a log is number 1"},
		// A break at the first record that is read: record 1 with a prev, or the first that remains after a deletion. The record
		// after it has the hash of the record as it was, so that it is a break as well.
		{name: "a break at the first record that remains", log: audit.LoginLog, build: threeOld, n: 4, nothing: true,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, deleteThree(t, s, dir), dir, audit.LoginLog, 4, withPrev)
			},
			after: alsoAccept(audit.LoginLog, 5)},
		{name: "a break at the first record that remains, with the records before it still there", log: audit.LoginLog, build: threeOld,
			n: 4, refused: leftText(4),
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, leaveThree(t, s, dir), dir, audit.LoginLog, 4, withPrev)
			},
			// whiteroom-db compact drops the records before it, which are what the deletion left, and stops at it: the row that is put
			// in is then honored, as no record is before it, and VerifyAudit passes.
			after:  alsoAccept(audit.LoginLog, 5),
			verify: "prev is not the hash of record 3", stop: "record 4: prev is not the hash that the chain start has of record 3",
			remains: 4, mends: true},
		{name: "the first record that remains has the hash of the chain start, with the records before it still there", log: audit.LoginLog,
			build: threeOld, n: 4, refused: startHashText(4), holed: true, last: 3, remains: 4, prepare: leaveThree},
		{name: "a break at record 1", log: audit.LoginLog, build: allOld, n: 1,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, s, dir, audit.LoginLog, 1, withPrev)
			},
			after: alsoAccept(audit.LoginLog, 2)},
		{name: "a break at record 1, in the activity log", log: audit.ActivityLog, build: allOld, n: 1, stop: none,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, s, dir, audit.ActivityLog, 1, withPrev)
			},
			after: alsoAccept(audit.ActivityLog, 2)},
		{name: "a break at record 1 that is rewritten again", log: audit.LoginLog, build: allOld, n: 1,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return closeAcceptReopen(t, rewriteRecord(t, s, dir, audit.LoginLog, 1, withPrev), dir, audit.LoginLog, 2)
			},
			after: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, s, dir, audit.LoginLog, 1, func(r *audit.Record) { r.Prev, r.Login = hashText(wrong[:]), "again" })
			},
			verify: "the first record has the hash of one before it", stop: "record 1: the first record has the hash of one before it"},
		{name: "a record 1 that has no prev", log: audit.LoginLog, build: allOld, n: 1, holed: true,
			refused: "record 1 has no prev, as the first record of a log has none: there is no break to accept",
			prepare: func(t *testing.T, s *Store, dir string) *Store { return s }},
		{name: "a record 1 that has no prev, in the activity log", log: audit.ActivityLog, build: allOld, n: 1, holed: true, stop: none,
			refused: "record 1 has no prev, as the first record of a log has none: there is no break to accept",
			prepare: func(t *testing.T, s *Store, dir string) *Store { return s }},

		// A gap that is accepted passes the record after it, as a hole does.
		{name: "a number gap", log: audit.LoginLog, build: allOld, n: 4, prepare: drop(audit.LoginLog, 3),
			accepted: dbserver.Acceptance{Gap: true, First: 3, Last: 3}},
		{name: "a number gap, in the activity log", log: audit.ActivityLog, build: allOld, n: 4, stop: none, prepare: drop(audit.ActivityLog, 3),
			accepted: dbserver.Acceptance{Gap: true, First: 3, Last: 3}},
		{name: "a gap of two records", log: audit.LoginLog, build: allOld, n: 5, prepare: drop(audit.LoginLog, 3, 4),
			accepted: dbserver.Acceptance{Gap: true, First: 3, Last: 4}},
		{name: "a gap at the start of the login history", log: audit.LoginLog, build: allOld, n: 2, from: 2, prepare: drop(audit.LoginLog, 1),
			accepted: dbserver.Acceptance{Gap: true, First: 1, Last: 1}},
		{name: "a gap at the start of the activity log", log: audit.ActivityLog, build: allOld, n: 3, stop: none,
			prepare:  drop(audit.ActivityLog, 1, 2),
			accepted: dbserver.Acceptance{Gap: true, First: 1, Last: 2}},
		{name: "a gap at the end of the login history", log: audit.LoginLog, build: allOld, n: 6, last: 5, prepare: drop(audit.LoginLog, 6),
			accepted: dbserver.Acceptance{Gap: true, End: true, First: 6, Last: 6}},
		{name: "a gap at the end of the activity log", log: audit.ActivityLog, build: allOld, n: 8, stop: none, prepare: drop(audit.ActivityLog, 8),
			accepted: dbserver.Acceptance{Gap: true, End: true, First: 8, Last: 8}},
		{name: "a gap at the end of the login history, and then the writer's next record", log: audit.LoginLog, build: allOld, n: 6, last: 4,
			prepare:  drop(audit.LoginLog, 6),
			accepted: dbserver.Acceptance{Gap: true, End: true, First: 6, Last: 6}, after: writes},
		{name: "a gap at the chain start", log: audit.LoginLog, build: threeOld, n: 5, nothing: true,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return dropRecords(t, deleteThree(t, s, dir), dir, audit.LoginLog, 4)
			},
			accepted: dbserver.Acceptance{Gap: true, First: 4, Last: 4}},
		{name: "a gap between the records that a deletion left and the chain start", log: audit.LoginLog, build: threeOld, n: 4,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return dropRecords(t, leaveThree(t, s, dir), dir, audit.LoginLog, 3)
			},
			accepted: dbserver.Acceptance{Gap: true, First: 3, Last: 3},
			// whiteroom-db compact passes the gap as VerifyAudit does: the records before it are what the deletion left, and go.
			last: 2, remains: 4},
		{name: "a gap, and then a break", log: audit.LoginLog, build: allOld, n: 5,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, drop(audit.LoginLog, 3)(t, s, dir), dir, audit.LoginLog, 4, loginChange)
			},
			after: func(t *testing.T, s *Store, dir string) *Store {
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				acc, err := acceptOf(t, dir, audit.LoginLog, 4)
				if want := (dbserver.Acceptance{Gap: true, First: 3, Last: 3}); err != nil || acc != want {
					t.Fatalf("AcceptBreak(login, 4) = %+v, %v, want the gap of the record 3", acc, err)
				}
				return reopenLog(t, dir, logdb.Options{})
			}},
		{name: "a record after a hole that the writer rewrote", log: audit.LoginLog, build: allOld, n: 4,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, lost(audit.LoginLog, 3, 3)(t, s, dir), dir, audit.LoginLog, 4, withPrev)
			},
			accepted: dbserver.Acceptance{Gap: true, First: 3, Last: 3},
			// The record 5 does not follow the record 4 that was rewritten: the gap is accepted, and the break there is not yet.
			verify: "prev is not the hash of record 4", stop: "record 5: prev is not the hash of record 4"},
		{name: "the cut of a deletion lands at the record after an accepted gap", log: audit.LoginLog, build: threeOld, n: 4, holed: true,
			last: 1, remains: 2, refused: acceptedGap(4, 3, 3), prepare: accepting(drop(audit.LoginLog, 3), audit.LoginLog, 4)},
		{name: "the cut of a deletion lands just after the record after an accepted gap", log: audit.LoginLog, build: threeOldOfFive, n: 3,
			holed: true, last: 3, remains: 4, refused: acceptedGap(3, 2, 2), prepare: accepting(drop(audit.LoginLog, 2), audit.LoginLog, 3)},
		{name: "a gap row for a record that a hole passes", log: audit.LoginLog, build: allOld, n: 4, holed: true, refused: holeText(4, 3, 3),
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				sum := sha256.Sum256(recordEvent(t, s, audit.LoginLog, 3).Data)
				s = lost(audit.LoginLog, 3, 3)(t, s, dir)
				injectChains(t, s, []chainRow{gapRow("login", 3, 3, sum)}, gapEvents("login", 3, 3, sum)...)
				return s
			}},
		{name: "a gap that is accepted already", log: audit.LoginLog, build: allOld, n: 4, holed: true,
			refused: acceptedGap(4, 3, 3), prepare: accepting(drop(audit.LoginLog, 3), audit.LoginLog, 4)},
		{name: "a gap of two records that is accepted already", log: audit.LoginLog, build: allOld, n: 5, holed: true,
			refused: acceptedGap(5, 3, 4), prepare: accepting(drop(audit.LoginLog, 3, 4), audit.LoginLog, 5)},
		{name: "a gap at the start of the activity log that is accepted already", log: audit.ActivityLog, build: allOld, n: 3, holed: true,
			stop: none, refused: acceptedGap(3, 1, 2), prepare: accepting(drop(audit.ActivityLog, 1, 2), audit.ActivityLog, 3)},
		{name: "a gap at the end that is accepted already", log: audit.LoginLog, build: allOld, n: 6, holed: true, gone: true, last: 5,
			refused: "the gap of the record 6 at the end of the audit log login is accepted already",
			prepare: accepting(drop(audit.LoginLog, 6), audit.LoginLog, 6)},
		{name: "a gap after a hole", log: audit.LoginLog, build: allOld, n: 6,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return dropRecords(t, lost(audit.LoginLog, 3, 3)(t, s, dir), dir, audit.LoginLog, 5)
			},
			accepted: dbserver.Acceptance{Gap: true, First: 5, Last: 5}},
		{name: "a record whose prev is no hash", log: audit.LoginLog, build: allOld, n: 4, refused: noHashText(4, 2),
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, dropRecords(t, s, dir, audit.LoginLog, 3), dir, audit.LoginLog, 4, noHash)
			},
			plant: func(t *testing.T, s *Store) {
				injectChains(t, s, []chainRow{gapRow("login", 3, 3, wrong)}, gapEvents("login", 3, 3, wrong)...)
			},
			verify: "the number does not follow that of record 2", stop: "record 4: the number does not follow that of record 2"},
		{name: "a record whose prev is no hash, in the activity log", log: audit.ActivityLog, build: allOld, n: 4, refused: noHashText(4, 2),
			stop: none,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, dropRecords(t, s, dir, audit.ActivityLog, 3), dir, audit.ActivityLog, 4, noHash)
			},
			plant: func(t *testing.T, s *Store) {
				injectChains(t, s, []chainRow{gapRow("activity", 3, 3, wrong)}, gapEvents("activity", 3, 3, wrong)...)
			},
			verify: "the number does not follow that of record 2"},

		// A row of a gap that does not match is not passed by any of the places, and the acceptance replaces it.
		{name: "a row of a gap that is one record wider", log: audit.LoginLog, build: allOld, n: 4,
			prepare:  gapOf(audit.LoginLog, audit.LoginLog, []uint64{3}, 3, 4, 4),
			accepted: dbserver.Acceptance{Gap: true, First: 3, Last: 3}},
		{name: "a row of a gap that is one record narrower", log: audit.LoginLog, build: allOld, n: 5,
			prepare:  gapOf(audit.LoginLog, audit.LoginLog, []uint64{3, 4}, 3, 3, 3),
			accepted: dbserver.Acceptance{Gap: true, First: 3, Last: 4}},
		{name: "a row of a gap that starts one record later", log: audit.LoginLog, build: allOld, n: 4,
			prepare:  gapOf(audit.LoginLog, audit.LoginLog, []uint64{3}, 4, 4, 3),
			accepted: dbserver.Acceptance{Gap: true, First: 3, Last: 3}},
		{name: "a row of a gap with the hash of another record", log: audit.LoginLog, build: allOld, n: 4,
			prepare:  gapOf(audit.LoginLog, audit.LoginLog, []uint64{3}, 3, 3, 2),
			accepted: dbserver.Acceptance{Gap: true, First: 3, Last: 3}},
		{name: "a row of a gap with a hash that no record has", log: audit.LoginLog, build: allOld, n: 4,
			prepare:  gapOf(audit.LoginLog, audit.LoginLog, []uint64{3}, 3, 3, 0),
			accepted: dbserver.Acceptance{Gap: true, First: 3, Last: 3}},
		{name: "a row of a gap of the other log", log: audit.LoginLog, build: allOld, n: 4,
			prepare:  gapOf(audit.ActivityLog, audit.LoginLog, []uint64{3}, 3, 3, 3),
			accepted: dbserver.Acceptance{Gap: true, First: 3, Last: 3}},
		{name: "a row of a gap that cannot be read", log: audit.LoginLog, build: allOld, n: 4,
			refused: "the chain of the audit log login cannot be read, so no break of it is accepted (the table chains: a row of an " +
				"accepted gap cannot be read)",
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				s = rewriteRecord(t, s, dir, audit.LoginLog, 3, loginChange)
				injectChains(t, s, []chainRow{{dbwire.ChainGapKey("login", 2), []byte("abc")}})
				return s
			},
			verify: "a row of the table chains for an accepted gap cannot be read",
			stop:   "the table chains: a row of an accepted gap cannot be read"},

		// A hole that the repair recorded passes the record after it; a break is no use there.
		{name: "a hole in the middle", log: audit.LoginLog, build: allOld, n: 4, holed: true, refused: holeText(4, 3, 3),
			prepare: lost(audit.LoginLog, 3, 3)},
		{name: "a hole in the middle, in the activity log", log: audit.ActivityLog, build: allOld, n: 4, holed: true, stop: none,
			refused: holeText(4, 3, 3), prepare: lost(audit.ActivityLog, 3, 3)},
		{name: "a hole of two records", log: audit.LoginLog, build: allOld, n: 5, holed: true, refused: holeText(5, 3, 4),
			prepare: lost(audit.LoginLog, 3, 4)},
		{name: "a hole at the start of the activity log", log: audit.ActivityLog, build: allOld, n: 3, holed: true, stop: none,
			refused: holeText(3, 1, 2), prepare: lost(audit.ActivityLog, 1, 2)},
		{name: "a hole at the start of the login history", log: audit.LoginLog, build: allOld, n: 2, holed: true, from: 2,
			refused: holeText(2, 1, 1), prepare: lost(audit.LoginLog, 1, 1)},
		{name: "a hole at the end of the login history", log: audit.LoginLog, build: allOld, n: 6, holed: true, gone: true, last: 5,
			refused: absent("login", 6), prepare: lost(audit.LoginLog, 6, 6)},
		{name: "a hole at the end of the activity log", log: audit.ActivityLog, build: allOld, n: 7, holed: true, gone: true,
			stop: none, refused: absent("activity", 7), prepare: lost(audit.ActivityLog, 7, 7)},
		{name: "a hole at the chain start", log: audit.LoginLog, build: threeOld, n: 5, holed: true, nothing: true,
			refused: holeText(5, 4, 4),
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return lost(audit.LoginLog, 4, 4)(t, deleteThree(t, s, dir), dir)
			}},
		{name: "a hole, and then a break", log: audit.LoginLog, build: allOld, n: 5,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, lost(audit.LoginLog, 3, 3)(t, s, dir), dir, audit.LoginLog, 4, loginChange)
			}},
		{name: "the cut of a deletion lands at the record after a hole", log: audit.LoginLog, build: threeOld, n: 4, holed: true,
			last: 1, remains: 2, refused: holeText(4, 3, 3), prepare: lost(audit.LoginLog, 3, 3)},
		{name: "the cut of a deletion lands just after the record after a hole", log: audit.LoginLog, build: threeOldOfFive, n: 3,
			holed: true, last: 3, remains: 4, refused: holeText(3, 2, 2), prepare: lost(audit.LoginLog, 2, 2)},

		// A hole that the row does not record, as it is, is a gap, which the acceptance of the gap passes.
		{name: "a row that is one record wider", log: audit.LoginLog, build: allOld, n: 4,
			prepare:  holeOf(audit.LoginLog, audit.LoginLog, []uint64{3}, 3, 4, 4),
			accepted: dbserver.Acceptance{Gap: true, First: 3, Last: 3}},
		{name: "a row that is one record wider, in the activity log", log: audit.ActivityLog, build: allOld, n: 4, stop: none,
			prepare:  holeOf(audit.ActivityLog, audit.ActivityLog, []uint64{3}, 3, 4, 4),
			accepted: dbserver.Acceptance{Gap: true, First: 3, Last: 3}},
		{name: "a row that is one record narrower", log: audit.LoginLog, build: allOld, n: 5,
			prepare:  holeOf(audit.LoginLog, audit.LoginLog, []uint64{3, 4}, 3, 3, 3),
			accepted: dbserver.Acceptance{Gap: true, First: 3, Last: 4}},
		{name: "a row that starts one record later", log: audit.LoginLog, build: allOld, n: 4,
			prepare:  holeOf(audit.LoginLog, audit.LoginLog, []uint64{3}, 4, 4, 3),
			accepted: dbserver.Acceptance{Gap: true, First: 3, Last: 3}},
		{name: "a row with the hash of another record", log: audit.LoginLog, build: allOld, n: 4,
			prepare:  holeOf(audit.LoginLog, audit.LoginLog, []uint64{3}, 3, 3, 2),
			accepted: dbserver.Acceptance{Gap: true, First: 3, Last: 3}},
		{name: "a row with a hash that no record has", log: audit.LoginLog, build: allOld, n: 4,
			prepare:  holeOf(audit.LoginLog, audit.LoginLog, []uint64{3}, 3, 3, 0),
			accepted: dbserver.Acceptance{Gap: true, First: 3, Last: 3}},
		{name: "a row of the other log", log: audit.LoginLog, build: allOld, n: 4,
			prepare:  holeOf(audit.ActivityLog, audit.LoginLog, []uint64{3}, 3, 3, 3),
			accepted: dbserver.Acceptance{Gap: true, First: 3, Last: 3}},
		{name: "a row that cannot be read", log: audit.LoginLog, build: allOld, n: 4,
			refused: "the chain of the audit log login cannot be read, so no break of it is accepted (the table chains: a row of a hole " +
				"cannot be read)",
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				s = rewriteRecord(t, s, dir, audit.LoginLog, 3, loginChange)
				injectChains(t, s, []chainRow{{dbwire.ChainHoleKey("login", 2), []byte("abc")}})
				return s
			},
			verify: "a row of the table chains for a hole cannot be read", stop: "the table chains: a row of a hole cannot be read"},

		// The rows and the records around the first record that remains, K, when the records that a deletion left are before it
		// (the chain starts at the record 4, and the records 1 to 3, or 1 and 2, are left): VerifyAudit, compact and accept-break
		// step across K by one rule, and compact drops the records that were left where VerifyAudit passes them.
		{name: "a gap that holds K, after the records that a deletion left", log: audit.LoginLog, build: threeOld, n: 5,
			accepted: dbserver.Acceptance{Gap: true, First: 3, Last: 4}, last: 2, remains: 5,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return dropRecords(t, leaveThree(t, s, dir), dir, audit.LoginLog, 3, 4)
			}},
		{name: "a hole that holds K, after the records that a deletion left", log: audit.LoginLog, build: threeOld, n: 5, holed: true,
			refused: holeText(5, 3, 4), last: 2, remains: 5, prepare: across(false, []uint64{3, 4}, rowSpec{3, 4, 4})},
		{name: "a gap that is accepted and holds K, after the records that a deletion left", log: audit.LoginLog, build: threeOld, n: 5,
			holed: true, refused: acceptedGap(5, 3, 4), last: 2, remains: 5, prepare: across(true, []uint64{3, 4}, rowSpec{3, 4, 4})},
		{name: "a hole that holds K, with no record before it", log: audit.LoginLog, build: threeOld, n: 6, holed: true, nothing: true,
			refused: holeText(6, 3, 5),
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return holeOf(audit.LoginLog, audit.LoginLog, []uint64{4, 5}, 3, 5, 5)(t, deleteThree(t, s, dir), dir)
			}},
		{name: "a hole that ends with K-1, after the records that a deletion left", log: audit.LoginLog, build: threeOld, n: 4, holed: true,
			refused: holeText(4, 3, 3), last: 2, remains: 4, prepare: across(false, []uint64{3}, rowSpec{3, 3, 3})},
		{name: "a hole that ends with K-1 with another hash, after the records that a deletion left", log: audit.LoginLog, build: threeOld, n: 4,
			accepted: dbserver.Acceptance{Gap: true, First: 3, Last: 3}, last: 2, remains: 4,
			prepare: across(false, []uint64{3}, rowSpec{3, 3, 0})},
		{name: "a hole at K, after the record K-1 that a deletion left", log: audit.LoginLog, build: threeOld, n: 5, holed: true,
			refused: holeText(5, 4, 4), last: 3, remains: 5, prepare: across(false, []uint64{4}, rowSpec{4, 4, 4})},
		{name: "a hole at K, after a record K-1 that is not the one that the chain start has", log: audit.LoginLog, build: threeOld, n: 5,
			refused: holeText(5, 4, 4), verify: notHash, stop: "record 3: " + notHash, remains: 1,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, across(false, []uint64{4}, rowSpec{4, 4, 4})(t, s, dir), dir, audit.LoginLog, 3, changed3)
			}},
		{name: "a gap at K, after a record K-1 that is not the one that the chain start has", log: audit.LoginLog, build: threeOld, n: 5,
			refused: crossing, verify: "the number does not follow that of record 3", stop: "record 3: " + notHash, remains: 1,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, dropRecords(t, leaveThree(t, s, dir), dir, audit.LoginLog, 4), dir, audit.LoginLog, 3, changed3)
			}},
		{name: "two holes that follow one another across K", log: audit.LoginLog, build: threeOld, n: 5, holed: true,
			refused: "record 5 comes after the record 3 and the record 4 that whiteroom-db repair dropped: there is no break to accept",
			last:    2, remains: 5, prepare: across(false, []uint64{3, 4}, rowSpec{3, 3, 3}, rowSpec{4, 4, 4})},
		{name: "a hole and a gap that follow one another across K", log: audit.LoginLog, build: threeOld, n: 5, holed: true,
			refused: "record 5 comes after the record 3 that whiteroom-db repair dropped and the record 4 whose gap is accepted already: there " +
				"is no break to accept",
			last: 2, remains: 5, prepare: func(t *testing.T, s *Store, dir string) *Store {
				s = leaveThree(t, s, dir)
				sum3, sum4 := sha256.Sum256(recordEvent(t, s, audit.LoginLog, 3).Data), sha256.Sum256(recordEvent(t, s, audit.LoginLog, 4).Data)
				s = dropRecords(t, s, dir, audit.LoginLog, 3, 4)
				injectChains(t, s, []chainRow{holeRow("login", 3, 3, sum3)}, holeEvents("login", 3, 3, sum3)...)
				injectChains(t, s, []chainRow{gapRow("login", 4, 4, sum4)}, gapEvents("login", 4, 4, sum4)...)
				return s
			}},
		{name: "two holes across K, the first ending with K-1 with another hash", log: audit.LoginLog, build: threeOld, n: 5,
			refused: "record 5 comes after the record 3 and the record 4 that whiteroom-db repair dropped: there is no break to accept",
			verify:  "the hole of record 3 does not have the hash that the chain start has of record 3",
			stop:    "record 5: the hole of record 3 does not have the hash that the chain start has of record 3", remains: 1,
			prepare: across(false, []uint64{3, 4}, rowSpec{3, 3, 0}, rowSpec{4, 4, 4})},
		{name: "a gap that is the record before K, after the records that a deletion left", log: audit.LoginLog, build: threeOld, n: 4,
			accepted: dbserver.Acceptance{Gap: true, First: 3, Last: 3}, last: 2, remains: 4,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return dropRecords(t, leaveThree(t, s, dir), dir, audit.LoginLog, 3)
			}},

		// The same at the end of the log, which a row ends as it ends with a record, and the head is.
		{name: "a hole at the end that ends with K-1", log: audit.LoginLog, build: allOld, n: 6, holed: true, gone: true, last: 5,
			refused: notInStore(6, 7), prepare: across(false, []uint64{6}, rowSpec{6, 6, 6})},
		{name: "a gap at the end that is accepted and ends with K-1", log: audit.LoginLog, build: allOld, n: 6, holed: true, gone: true, last: 5,
			refused: notInStore(6, 7), prepare: across(true, []uint64{6}, rowSpec{6, 6, 6})},
		{name: "a hole at the end that ends with K-1 with another hash", log: audit.LoginLog, build: allOld, n: 6, gone: true,
			refused: notInStore(6, 7), remains: 1,
			verify: "the hole of record 6 does not have the hash that the chain start has of record 6",
			stop:   "record 5: the hole of record 6 does not have the hash that the chain start has of record 6",
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				head := audit.Head{Log: audit.LoginLog, N: 6, Hash: wrong}
				s = closeAndRewrite(t, s, dir, rewriteEvents(nil, nil, map[audit.Log]*audit.Head{audit.LoginLog: &head}))
				return across(false, []uint64{6}, rowSpec{6, 6, 0})(t, s, dir)
			}},
		{name: "a hole at the end that ends before K-1", log: audit.LoginLog, build: allOld, n: 5, gone: true, remains: 1,
			refused: notInStore(5, 7), verify: "the chain starts at record 7, but the log ends with the hole of record 5",
			stop: "record 4: the chain starts at record 7, so the records before it end with record 6",
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				// The head names the record 5, which the row records as lost.
				s = leaveThree(t, s, dir)
				sum := sha256.Sum256(recordEvent(t, s, audit.LoginLog, 5).Data)
				s = dropRecords(t, s, dir, audit.LoginLog, 5, 6)
				injectChains(t, s, []chainRow{holeRow("login", 5, 5, sum)}, holeEvents("login", 5, 5, sum)...)
				head := audit.Head{Log: audit.LoginLog, N: 5, Hash: sum}
				return closeAndRewrite(t, s, dir, rewriteEvents(nil, nil, map[audit.Log]*audit.Head{audit.LoginLog: &head}))
			}},
		{name: "a hole at the end that holds K", log: audit.LoginLog, build: eightOld, n: 8, holed: true, gone: true, last: 5, refused: absent("login", 8),
			prepare: across(false, []uint64{6, 7, 8}, rowSpec{6, 8, 8})},
		{name: "a gap at the end that holds K, which accept-break accepts", log: audit.LoginLog, build: eightOld, n: 8, gone: true, last: 5,
			accepted: dbserver.Acceptance{Gap: true, End: true, First: 6, Last: 8},
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return dropRecords(t, leaveThree(t, s, dir), dir, audit.LoginLog, 6, 7, 8)
			}},
		{name: "a hole at the end that starts at K", log: audit.LoginLog, build: eightOld, n: 8, holed: true, gone: true, last: 6,
			refused: absent("login", 8), prepare: across(false, []uint64{7, 8}, rowSpec{7, 8, 8})},
		{name: "two holes at the end that follow one another across K", log: audit.LoginLog, build: eightOld, n: 8, holed: true, gone: true,
			last: 5, refused: absent("login", 8), prepare: across(false, []uint64{6, 7, 8}, rowSpec{6, 6, 6}, rowSpec{7, 8, 8})},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, dir := tt.build(t)
			s = tt.prepare(t, s, dir)
			if err := s.VerifyAudit(t.Context(), nil); (err == nil) != tt.holed {
				t.Fatalf("VerifyAudit() = %v before the acceptance, want it to pass if and only if a hole passes the record: %v",
					err, tt.holed)
			}
			events, rows := allEvents(t, s), len(rawTable(t, s, tableChains))
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}

			// accept-break.
			acc, err := acceptOf(t, dir, tt.log, tt.n)
			switch {
			case tt.refused == "" && err != nil:
				t.Fatalf("AcceptBreak(%s, %d) = %v, want it accepted", tt.log, tt.n, err)
			case tt.refused == "" && acc != tt.accepted:
				t.Fatalf("AcceptBreak(%s, %d) accepted %+v, want %+v", tt.log, tt.n, acc, tt.accepted)
			case tt.refused != "" && (err == nil || err.Error() != tt.refused):
				t.Fatalf("AcceptBreak(%s, %d) = %v, want %q", tt.log, tt.n, err, tt.refused)
			}
			s = reopenLog(t, dir, logdb.Options{})
			if tt.refused != "" {
				if !eventsSame(events, allEvents(t, s)) || len(rawTable(t, s, tableChains)) != rows {
					t.Error("the refusal changed the store")
				}
				// The row is put in all the same, as the row of an acceptance that was made before the log was changed would be.
				if tt.plant != nil {
					tt.plant(t, s)
				} else if !tt.gone {
					sum := sha256.Sum256(recordEvent(t, s, tt.log, tt.n).Data)
					injectChains(t, s, []chainRow{breakRow(string(tt.log), tt.n, sum)}, acceptedEvents(string(tt.log), tt.n, sum)...)
				}
			}
			if tt.after != nil {
				s = tt.after(t, s, dir)
			}

			// VerifyAudit.
			verr := s.VerifyAudit(t.Context(), nil)
			switch {
			case tt.verify == "" && verr != nil:
				t.Errorf("VerifyAudit() = %v, want it to pass", verr)
			case tt.verify != "" && (verr == nil || !strings.Contains(verr.Error(), tt.verify) || !errors.Is(verr, audit.ErrBroken)):
				t.Errorf("VerifyAudit() = %v, want an error with %q", verr, tt.verify)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if tt.stop == none {
				return
			}

			// The walk of compact: it passes the records, or stops at the one that VerifyAudit fails at.
			last := tt.last
			if last == 0 {
				last = 6
			}
			from := tt.from
			if from == 0 {
				from = 1
			}
			d, err := compactLoginOf(t, dir, chainTime, nil)
			switch {
			case err != nil:
				t.Fatalf("CompactLogin() = %+v, %v", d, err)
			case tt.nothing && (d.Stopped != "" || d.First != 0):
				t.Errorf("CompactLogin() = %+v, want nothing dropped and no stop", d)
			case tt.nothing:
			case tt.stop == "" && (d.Stopped != "" || d.First != from || d.Last != last):
				t.Errorf("CompactLogin() = %+v, want the records %d to %d, with no stop", d, from, last)
			case tt.stop != "" && !strings.HasPrefix(d.Stopped, tt.stop):
				t.Errorf("CompactLogin() = %+v, want the stop %q", d, tt.stop)
			}
			s = reopenLog(t, dir, logdb.Options{})
			again := s.VerifyAudit(t.Context(), nil)
			switch {
			case tt.mends && (verr == nil || again != nil):
				t.Errorf("VerifyAudit() = %v before whiteroom-db compact and %v after it, want it to fail before and pass after", verr, again)
			case !tt.mends && (verr == nil) != (again == nil):
				t.Errorf("VerifyAudit() = %v before whiteroom-db compact and %v after it", verr, again)
			}
			if got := recordsOf(t, s, audit.LoginLog); tt.remains != 0 && (len(got) == 0 || !strings.HasPrefix(got[0], fmt.Sprintf("login %d ", tt.remains))) {
				t.Errorf("the login records after compact are %v, want the first to be record %d", got, tt.remains)
			}
		})
	}
}

// TestAuditCompactLoginInterruptedAcrossBreak checks that a deletion that went across an accepted break, and stopped before
// its compaction, leaves a store that VerifyAudit passes and the next call finishes: the break is honored in the records that
// the deletion left, as it was in the walk.
func TestAuditCompactLoginInterruptedAcrossBreak(t *testing.T) {
	s, dir, _ := agedStore(t, []int{40, 39, 38, 37, 36, 35, 0}, []bool{true, true, true, true, true, true, false})
	s = rewriteRecord(t, s, dir, audit.LoginLog, 3, func(r *audit.Record) { r.Login = "forged" })
	s = closeAcceptReopen(t, s, dir, audit.LoginLog, 4)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	hookErr := errors.New("hook failed")
	if _, err := compactLoginOf(t, dir, chainTime, func(point string) error {
		if point == "compact:base" {
			return hookErr
		}
		return nil
	}); !errors.Is(err, hookErr) {
		t.Fatalf("CompactLogin() = %v, want the error of the hook", err)
	}
	s = reopenLog(t, dir, logdb.Options{})
	checkVerify(t, s, dir, nil, nil, "", startNote(7), leftNote("the records 1 to 6 are", 7))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if d, err := compactLoginOf(t, dir, chainTime, nil); err != nil || d.First != 1 || d.Last != 6 || d.Stopped != "" {
		t.Fatalf("the second CompactLogin() = %+v, %v, want the records 1 to 6", d, err)
	}
	s = reopenLog(t, dir, logdb.Options{})
	checkVerify(t, s, dir, nil, nil, "", startNote(7))
}

// TestAuditCompactThenAcceptBreakAtFirstRemaining checks the way out of a break at the first record that remains (K) while the
// records that a deletion left are before it: whiteroom-db accept-break refuses it, and says that whiteroom-db compact drops
// those records; compact drops them and stops at K, which it keeps; accept-break then accepts the break, VerifyAudit passes and
// tells it, and the next compact goes through K with the records after it, as it does through any accepted break.
func TestAuditCompactThenAcceptBreakAtFirstRemaining(t *testing.T) {
	s, dir, _ := agedStore(t, []int{40, 39, 38, 37, 36, 35, 0}, []bool{true, true, true, true, true, true, false})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// A deletion at a time when only the records 1 to 3 are old stops before its compaction: the chain starts at the record 4.
	hookErr := errors.New("hook failed")
	if _, err := compactLoginOf(t, dir, chainTime.AddDate(0, -25, 0), func(point string) error {
		if point == "compact:base" {
			return hookErr
		}
		return nil
	}); !errors.Is(err, hookErr) {
		t.Fatalf("CompactLogin() = %v, want the error of the hook", err)
	}
	s = reopenLog(t, dir, logdb.Options{})
	s = rewriteRecord(t, s, dir, audit.LoginLog, 4, func(r *audit.Record) { r.Prev = hashText(wrong[:]) })
	// The chain start has the time of that deletion.
	start4 := strings.Replace(startNote(4), "2026-11-20T10:20:30Z", chainTime.AddDate(0, -25, 0).Format(time.RFC3339), 1)
	failure := func(n uint64, why string) string {
		return fmt.Sprintf(`audit log "login": record %d (event %d): %s`, n, eventOf(t, s, audit.LoginLog, n), why)
	}
	checkVerify(t, s, dir, nil, nil, failure(4, "prev is not the hash of record 3"), start4)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// The break at the record 4 is refused while the records 1 to 3 are there.
	want := "record 4 comes after records before it that an earlier deletion left, which are still in the store: whiteroom-db compact " +
		"drops them if they are what the deletion left; accept the break at record 4 then"
	if err := acceptBreakOf(t, dir, audit.LoginLog, 4); err == nil || err.Error() != want {
		t.Fatalf("AcceptBreak(login, 4) = %v, want %q", err, want)
	}

	// Compact drops them, and stops at the record 4, which stays.
	d, err := compactLoginOf(t, dir, chainTime, nil)
	if err != nil || d.First != 1 || d.Last != 3 || d.LeftBefore != 0 ||
		d.Stopped != "record 4: prev is not the hash that the chain start has of record 3" {
		t.Fatalf("CompactLogin() = %+v, %v, want the records 1 to 3, and the stop at the record 4", d, err)
	}
	s = reopenLog(t, dir, logdb.Options{})
	if got := recordsOf(t, s, audit.LoginLog); len(got) != 3 || !strings.HasPrefix(got[0], "login 4 ") {
		t.Errorf("the login records are %v, want 4 to 6", got)
	}
	checkVerify(t, s, dir, nil, nil, failure(4, "prev is not the hash that the chain start has of record 3"), start4)
	// A second call finds the same stop and nothing to drop.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if again, err := compactLoginOf(t, dir, chainTime, nil); err != nil || again.First != 0 || again.Stopped != d.Stopped {
		t.Fatalf("the second CompactLogin() = %+v, %v, want the stop and nothing dropped", again, err)
	}

	// Accept-break accepts it now, and the record 5, which has the hash of the record 4 as it was.
	s = reopenLog(t, dir, logdb.Options{})
	s = closeAcceptReopen(t, s, dir, audit.LoginLog, 4)
	checkVerify(t, s, dir, nil, nil, failure(5, "prev is not the hash of record 4"), start4, breakNote("login", 4))
	s = closeAcceptReopen(t, s, dir, audit.LoginLog, 5)
	checkVerify(t, s, dir, nil, nil, "", start4, breakNote("login", 4), breakNote("login", 5))
	checkNoProblem(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// The next compact goes through the records 4 and 5, with the old record 6, and drops them.
	d, err = compactLoginOf(t, dir, chainTime, nil)
	if err != nil || d.First != 4 || d.Last != 6 || d.Stopped != "" {
		t.Fatalf("the third CompactLogin() = %+v, %v, want the records 4 to 6, with no stop", d, err)
	}
	s = reopenLog(t, dir, logdb.Options{})
	checkVerify(t, s, dir, nil, nil, "", startNote(7))
	if got := recordsOf(t, s, audit.LoginLog); len(got) != 0 {
		t.Errorf("the login records are %v, want none", got)
	}
}
