package store

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
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

// TestAuditBreakRule pins together the three places that decide whether a break that is accepted for a record is honored:
// whiteroom-db accept-break, VerifyAudit and whiteroom-db compact. A break is a record whose number is that of the one before
// it plus one and whose prev is not its hash, and which is neither the first record that is read nor the first that remains
// after a deletion. Each case is run through all three, and they must agree: a record that accept-break accepts is gone by
// VerifyAudit and passed by the walk of compact; one that it refuses is not, if its row is put in all the same, as one that
// the record had before it was changed or its log was, and compact never turns a store that VerifyAudit fails into one that
// it passes.
func TestAuditBreakRule(t *testing.T) {
	loginChange := func(r *audit.Record) { r.Login = "forged" }
	activityChange := func(r *audit.Record) { r.Detail = map[string]string{"event": "X"} }
	withPrev := func(r *audit.Record) { r.Prev = hashText(wrong[:]) }
	gap := func(n, before int) string {
		return fmt.Sprintf("record %d does not follow record %d in number: a gap, which a lost record leaves, is not a break that "+
			"can be accepted, and waits for the repair of the engine", n, before)
	}
	first := func(log string, n int) string {
		return fmt.Sprintf("record %d is the first record that the audit log %s is checked from: a break there is not accepted", n, log)
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
		refused string // the error of AcceptBreak, "" if it accepts the break
		// after changes the store when the acceptance, if there is one, has been made.
		after  func(t *testing.T, s *Store, dir string) *Store
		verify string // the error of VerifyAudit after that, up to the number of the event, "" if it passes
		// stop is where the walk of compact stops, as Deleted.Stopped says it (its start), "" if it does not.
		stop string
		// last is the last record that compact drops if the walk does not stop (6 if 0), and remains the first login record that
		// is left then (0: not looked at). A deletion does not leave a break at the start of the chain, which is not honored.
		last, remains uint64
	}{
		{name: "prev rewritten, the number follows", log: audit.LoginLog, build: allOld, n: 4,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, s, dir, audit.LoginLog, 3, loginChange)
			}},
		{name: "prev rewritten, the number follows, in the activity log", log: audit.ActivityLog, build: allOld, n: 4, stop: none,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, s, dir, audit.ActivityLog, 3, activityChange)
			}},
		{name: "a number gap", log: audit.LoginLog, build: allOld, n: 4, refused: gap(4, 2),
			prepare: func(t *testing.T, s *Store, dir string) *Store { return dropRecords(t, s, dir, audit.LoginLog, 3) },
			verify:  "the number does not follow that of record 2", stop: "record 4: the number does not follow that of record 2"},
		{name: "a number gap, in the activity log", log: audit.ActivityLog, build: allOld, n: 4, refused: gap(4, 2), stop: none,
			prepare: func(t *testing.T, s *Store, dir string) *Store { return dropRecords(t, s, dir, audit.ActivityLog, 3) },
			verify:  "the number does not follow that of record 2"},
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
		{name: "a break at the first record that remains", log: audit.LoginLog, build: threeOld, n: 4, refused: first("login", 4),
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, deleteThree(t, s, dir), dir, audit.LoginLog, 4, withPrev)
			},
			verify: "prev is not the hash that the chain start has of record 3",
			stop:   "record 4: prev is not the hash that the chain start has of record 3"},
		{name: "a break at the first record that remains, with the records before it still there", log: audit.LoginLog, build: threeOld,
			n: 4, refused: first("login", 4),
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, leaveThree(t, s, dir), dir, audit.LoginLog, 4, withPrev)
			},
			verify: "prev is not the hash of record 3", stop: "record 4: prev is not the hash that the chain start has of record 3"},
		{name: "a break at record 1", log: audit.LoginLog, build: allOld, n: 1, refused: first("login", 1),
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, s, dir, audit.LoginLog, 1, withPrev)
			},
			verify: "the first record has the hash of one before it", stop: "record 1: the first record has the hash of one before it"},
		{name: "a break at record 1, in the activity log", log: audit.ActivityLog, build: allOld, n: 1, refused: first("activity", 1),
			stop: none,
			prepare: func(t *testing.T, s *Store, dir string) *Store {
				return rewriteRecord(t, s, dir, audit.ActivityLog, 1, withPrev)
			},
			verify: "the first record has the hash of one before it"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, dir := tt.build(t)
			s = tt.prepare(t, s, dir)
			if err := s.VerifyAudit(t.Context(), nil); err == nil {
				t.Fatal("VerifyAudit() passes before the acceptance: there is no break")
			}
			events, rows := allEvents(t, s), len(rawTable(t, s, tableChains))
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}

			// accept-break.
			err := acceptBreakOf(t, dir, tt.log, tt.n)
			switch {
			case tt.refused == "" && err != nil:
				t.Fatalf("AcceptBreak(%s, %d) = %v, want it accepted", tt.log, tt.n, err)
			case tt.refused != "" && (err == nil || err.Error() != tt.refused):
				t.Fatalf("AcceptBreak(%s, %d) = %v, want %q", tt.log, tt.n, err, tt.refused)
			}
			s = reopenLog(t, dir, logdb.Options{})
			if tt.refused != "" {
				if !eventsSame(events, allEvents(t, s)) || len(rawTable(t, s, tableChains)) != rows {
					t.Error("the refusal changed the store")
				}
				// The row is put in all the same, as the row of an acceptance that was made before the log was changed would be.
				sum := sha256.Sum256(recordEvent(t, s, tt.log, tt.n).Data)
				injectChains(t, s, []chainRow{breakRow(string(tt.log), tt.n, sum)}, acceptedEvents(string(tt.log), tt.n, sum)...)
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
			d, err := compactLoginOf(t, dir, chainTime, nil)
			switch {
			case err != nil:
				t.Fatalf("CompactLogin() = %+v, %v", d, err)
			case tt.stop == "" && (d.Stopped != "" || d.First != 1 || d.Last != last):
				t.Errorf("CompactLogin() = %+v, want the records 1 to %d, with no stop", d, last)
			case tt.stop != "" && !strings.HasPrefix(d.Stopped, tt.stop):
				t.Errorf("CompactLogin() = %+v, want the stop %q", d, tt.stop)
			}
			s = reopenLog(t, dir, logdb.Options{})
			if again := s.VerifyAudit(t.Context(), nil); (verr == nil) != (again == nil) {
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
