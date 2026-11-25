package store

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// The tests here are the rows and the records around the first record that remains after a deletion, K, when the records before
// it that the deletion left are still in the store: VerifyAudit, whiteroom-db compact and whiteroom-db accept-break step across K
// by one rule (dbwire.ChainPass, dbwire.ChainEndPass and dbwire.ChainCross), and the note that the next compact removes those
// records is told only where it does.

// leaveDeletion closes s, runs a deletion of the old login records that stops before its compaction, so that the records that it
// drops are left before the chain start, and opens the store again.
func leaveDeletion(t *testing.T, s *Store, dir string) *Store {
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

// threeOldLeft is a store whose login records 1 to 3 are old and the records 4 to 6 are young, after a deletion that has left the
// records 1 to 3 before the chain start, which is at the record 4.
func threeOldLeft(t *testing.T) (*Store, string) {
	t.Helper()
	s, dir, _ := agedStore(t, []int{40, 39, 38, 3, 2, 1, 0}, []bool{true, true, true, true, true, true, false})
	return leaveDeletion(t, s, dir), dir
}

// allOldLeft is a store whose login records 1 to 6 are all old and left before the chain start, which is at the record 7.
func allOldLeft(t *testing.T) (*Store, string) {
	t.Helper()
	s, dir, _ := agedStore(t, []int{40, 39, 38, 37, 36, 35, 0}, []bool{true, true, true, true, true, true, false})
	return leaveDeletion(t, s, dir), dir
}

// eightRecordsLeft is a store whose login records 1 to 8 are there, the records 1 to 6 old and left before the chain start, which
// is at the record 7, and the records 7 and 8 young.
func eightRecordsLeft(t *testing.T) (*Store, string) {
	t.Helper()
	s, dir, _ := agedStore(t, []int{40, 39, 38, 37, 36, 35, 3, 2, 0}, []bool{true, true, true, true, true, true, true, true, false})
	return leaveDeletion(t, s, dir), dir
}

// sixRecordsLeft is a store whose login records 1 to 6 are there, the records 1 to 4 old and left before the chain start, which is
// at the record 5, and the records 5 and 6 young (the commits of the records 2 and 4 have no login record).
func sixRecordsLeft(t *testing.T) (*Store, string) {
	t.Helper()
	months := []int{40, 40, 30, 30, 14, 13, 11, 2, 0}
	login := []bool{true, false, true, false, true, true, true, true, false}
	s, dir, _ := agedStore(t, months, login)
	return leaveDeletion(t, s, dir), dir
}

// hashOfRecord returns the hash of the stored bytes of the record n of the login history of s.
func hashOfRecord(t *testing.T, s *Store, n uint64) [sha256.Size]byte {
	t.Helper()
	return sha256.Sum256(recordEvent(t, s, audit.LoginLog, n).Data)
}

// dropWithRow drops the login records first to last of s, which is closed and opened again, and puts a hole, or if gap an accepted
// gap, for them with the hash sum, as a repair or accept-break does.
func dropWithRow(t *testing.T, s *Store, dir string, gap bool, first, last uint64, sum [sha256.Size]byte) *Store {
	t.Helper()
	var ns []uint64
	for n := first; n <= last; n++ {
		ns = append(ns, n)
	}
	s = dropRecords(t, s, dir, audit.LoginLog, ns...)
	if gap {
		injectChains(t, s, []chainRow{gapRow("login", first, last, sum)}, gapEvents("login", first, last, sum)...)
	} else {
		injectChains(t, s, []chainRow{holeRow("login", first, last, sum)}, holeEvents("login", first, last, sum)...)
	}
	return s
}

// putRowFor adds the hole, or if gap the accepted gap, of the login records first to last with the hash sum, for records that are
// not there already.
func putRowFor(t *testing.T, s *Store, gap bool, first, last uint64, sum [sha256.Size]byte) {
	t.Helper()
	if gap {
		injectChains(t, s, []chainRow{gapRow("login", first, last, sum)}, gapEvents("login", first, last, sum)...)
	} else {
		injectChains(t, s, []chainRow{holeRow("login", first, last, sum)}, holeEvents("login", first, last, sum)...)
	}
}

// verifyOf returns the notes of VerifyAudit of s and its error, and closes nothing.
func verifyOf(t *testing.T, s *Store) ([]string, error) {
	t.Helper()
	var notes []string
	err := s.VerifyAuditNotes(t.Context(), nil, func(n string) { notes = append(notes, n) })
	return notes, err
}

// hasLeftNote returns whether notes has the note that the records before the chain start are still in the store.
func hasLeftNote(notes []string) bool {
	return slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(n, "still in the store after the deletion") })
}

// loginNumbers returns the numbers of the login records of s.
func loginNumbers(t *testing.T, s *Store) []uint64 {
	t.Helper()
	var ns []uint64
	for _, r := range allRecords(t, s) {
		if r.Log == audit.LoginLog {
			ns = append(ns, r.N)
		}
	}
	return ns
}

// compactClosed closes s, runs compact at chainTime, and opens the store again.
func compactClosed(t *testing.T, s *Store, dir string) (*Store, dbserver.Deleted) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	d, err := compactLoginOf(t, dir, chainTime, nil)
	if err != nil {
		t.Fatalf("CompactLogin() = %v", err)
	}
	return reopenLog(t, dir, logdb.Options{}), d
}

// wantVerify checks that VerifyAudit of s fails with an error that has want in it, or, if want is "", passes.
func wantVerify(t *testing.T, s *Store, want string) {
	t.Helper()
	err := s.VerifyAudit(t.Context(), nil)
	switch {
	case want == "" && err != nil:
		t.Errorf("VerifyAudit() = %v, want it to pass", err)
	case want != "" && (err == nil || !strings.Contains(err.Error(), want) || !errors.Is(err, audit.ErrBroken)):
		t.Errorf("VerifyAudit() = %v, want an error with %q", err, want)
	}
}

// wantDeleted checks that compact dropped the login records first to last, or, if last is 0, none.
func wantDeleted(t *testing.T, d dbserver.Deleted, first, last uint64, stopped string) {
	t.Helper()
	if d.First != first || d.Last != last || d.Stopped != stopped {
		t.Errorf("CompactLogin() dropped the records %d to %d and stopped at %q, want %d to %d and %q", d.First, d.Last, d.Stopped, first, last,
			stopped)
	}
}

// TestAuditGapAcrossChainStart checks the gap that accept-break accepts across K, from records that a deletion left: VerifyAudit
// passes, and compact drops the records, which it can, since a gap that holds K-1 and K leaves no hash of K-1 to check; one after
// a record K-1 that is not the one that the chain start has is refused, and so VerifyAudit and compact still fail on it.
func TestAuditGapAcrossChainStart(t *testing.T) {
	t.Run("a gap that holds K", func(t *testing.T) {
		// The records 1 and 2 are left, the records 3 and 4 are lost, and the chain starts at the record 4.
		s, dir := threeOldLeft(t)
		s = dropRecords(t, s, dir, audit.LoginLog, 3, 4)
		wantVerify(t, s, "record 5 (event")
		s = closeAcceptReopen(t, s, dir, audit.LoginLog, 5)
		notes, verr := verifyOf(t, s)
		if verr != nil || !hasLeftNote(notes) {
			t.Fatalf("VerifyAudit() = %v with the notes %q, want it to pass and tell the records that are left", verr, notes)
		}
		if want := missingNote("login", 3, 4, againFrom(5)); !slices.Contains(notes, want) {
			t.Errorf("the notes are %q, want %q", notes, want)
		}
		if want := leftNote("the records 1 to 2 are", 4); !slices.Contains(notes, want) {
			t.Errorf("the notes are %q, want %q", notes, want)
		}
		s, d := compactClosed(t, s, dir)
		wantDeleted(t, d, 1, 2, "")
		wantVerify(t, s, "")
		if got := loginNumbers(t, s); !slices.Equal(got, []uint64{5, 6}) {
			t.Errorf("the login records are %v, want 5 and 6", got)
		}
		notes, _ = verifyOf(t, s)
		if hasLeftNote(notes) {
			t.Errorf("the notes after compact are %q, want no records left", notes)
		}
		checkNoProblem(t, s)
	})
	t.Run("a gap at K, after the record before K that a deletion left", func(t *testing.T) {
		s, dir := threeOldLeft(t)
		s = dropRecords(t, s, dir, audit.LoginLog, 4)
		s = closeAcceptReopen(t, s, dir, audit.LoginLog, 5)
		notes, verr := verifyOf(t, s)
		if verr != nil || !hasLeftNote(notes) {
			t.Fatalf("VerifyAudit() = %v with the notes %q, want it to pass and tell the records that are left", verr, notes)
		}
		s, d := compactClosed(t, s, dir)
		wantDeleted(t, d, 1, 3, "")
		wantVerify(t, s, "")
	})
	t.Run("a gap at K, after a record K-1 that is not the one that the chain start has", func(t *testing.T) {
		// The record 3 has been changed since the deletion, and the record 4 is lost: a gap at K would hide that the record 3 is not
		// the one that the deletion left, so that it is not accepted, and VerifyAudit goes on failing at the record 5.
		s, dir := threeOldLeft(t)
		s = rewriteRecord(t, s, dir, audit.LoginLog, 3, func(r *audit.Record) { r.Login = "forged" })
		s = dropRecords(t, s, dir, audit.LoginLog, 4)
		want := `audit log "login": record 5 (event ` + fmt.Sprint(eventOf(t, s, audit.LoginLog, 5)) + `): the number does not follow that of record 3`
		wantVerify(t, s, want)
		events, rows := allEvents(t, s), len(rawTable(t, s, tableChains))
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		_, err := acceptOf(t, dir, audit.LoginLog, 5)
		if refusal := "record 5 comes after records before it that an earlier deletion left, the last of which, record 3, does not have " +
			"the hash that the chain start has of it: the gap before record 5 is not accepted"; err == nil || err.Error() != refusal {
			t.Fatalf("AcceptBreak(login, 5) = %v, want %q", err, refusal)
		}
		s = reopenLog(t, dir, logdb.Options{})
		if !eventsSame(events, allEvents(t, s)) || len(rawTable(t, s, tableChains)) != rows {
			t.Error("the refusal changed the store")
		}
		wantVerify(t, s, want)
		s, d := compactClosed(t, s, dir)
		wantDeleted(t, d, 0, 0, "record 3: its hash is not the hash that the chain start has of it")
		wantVerify(t, s, want)
		if got := loginNumbers(t, s); !slices.Equal(got, []uint64{1, 2, 3, 5, 6}) {
			t.Errorf("the login records are %v, want 1, 2, 3, 5 and 6", got)
		}
	})
	t.Run("a hole at K, after a record K-1 that is not the one that the chain start has", func(t *testing.T) {
		s, dir := threeOldLeft(t)
		sum := hashOfRecord(t, s, 4)
		s = rewriteRecord(t, s, dir, audit.LoginLog, 3, func(r *audit.Record) { r.Login = "forged" })
		s = dropWithRow(t, s, dir, false, 4, 4, sum)
		want := `audit log "login": record 3 (event ` + fmt.Sprint(eventOf(t, s, audit.LoginLog, 3)) +
			`): its hash is not the hash that the chain start has of it`
		wantVerify(t, s, want)
		s, d := compactClosed(t, s, dir)
		wantDeleted(t, d, 0, 0, "record 3: its hash is not the hash that the chain start has of it")
		wantVerify(t, s, want)
		if got := loginNumbers(t, s); !slices.Equal(got, []uint64{1, 2, 3, 5, 6}) {
			t.Errorf("the login records are %v, want 1, 2, 3, 5 and 6", got)
		}
	})
	t.Run("a hole at K, after the record K-1 that is the one that the chain start has", func(t *testing.T) {
		s, dir := threeOldLeft(t)
		s = dropWithRow(t, s, dir, false, 4, 4, hashOfRecord(t, s, 4))
		notes, verr := verifyOf(t, s)
		if verr != nil || !hasLeftNote(notes) {
			t.Fatalf("VerifyAudit() = %v with the notes %q, want it to pass and tell the records that are left", verr, notes)
		}
		s, d := compactClosed(t, s, dir)
		wantDeleted(t, d, 1, 3, "")
		wantVerify(t, s, "")
	})
}

// TestAuditEndGapAcrossChainStart checks the gap at the end of a log that accept-break accepts: when it holds K-1 and K, VerifyAudit
// passes it, and compact drops the records that a deletion left; when it would end before K-1 it is refused, since the log would
// still end before the chain start, and VerifyAudit and compact go on reporting it.
func TestAuditEndGapAcrossChainStart(t *testing.T) {
	t.Run("a gap at the end that holds K-1 and K", func(t *testing.T) {
		// The records 1 to 5 are left, the records 6 to 8 are lost, and the chain starts at the record 7.
		s, dir := eightRecordsLeft(t)
		s = dropRecords(t, s, dir, audit.LoginLog, 6, 7, 8)
		wantVerify(t, s, "the head in the table is record 8, but the last record is record 5")
		s = closeAcceptReopen(t, s, dir, audit.LoginLog, 8)
		notes, verr := verifyOf(t, s)
		if verr != nil || !hasLeftNote(notes) {
			t.Fatalf("VerifyAudit() = %v with the notes %q, want it to pass and tell the records that are left", verr, notes)
		}
		if want := missingNote("login", 6, 8, endsWith(5)); !slices.Contains(notes, want) {
			t.Errorf("the notes are %q, want %q", notes, want)
		}
		s, d := compactClosed(t, s, dir)
		wantDeleted(t, d, 1, 5, "")
		wantVerify(t, s, "")
		if got := loginNumbers(t, s); len(got) != 0 {
			t.Errorf("the login records are %v, want none", got)
		}
		checkNoProblem(t, s)
	})
	t.Run("a gap at the end that would end with K-1", func(t *testing.T) {
		// The records 1 to 5 are left and the record 6, which the chain start has as the last that was deleted, is lost: accept-break
		// does not accept a record before the chain start (the repair does record the hole, which the test of it has).
		s, dir := allOldLeft(t)
		s = dropRecords(t, s, dir, audit.LoginLog, 6)
		want := "the head in the table is record 6, but the last record is record 5"
		wantVerify(t, s, want)
		events, rows := allEvents(t, s), len(rawTable(t, s, tableChains))
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		_, err := acceptOf(t, dir, audit.LoginLog, 6)
		if refusal := "record 6 of the audit log login is not in the store: the records before record 7 were deleted"; err == nil ||
			err.Error() != refusal {
			t.Fatalf("AcceptBreak(login, 6) = %v, want %q", err, refusal)
		}
		s = reopenLog(t, dir, logdb.Options{})
		if !eventsSame(events, allEvents(t, s)) || len(rawTable(t, s, tableChains)) != rows {
			t.Error("the refusal changed the store")
		}
		wantVerify(t, s, want)
		s, d := compactClosed(t, s, dir)
		wantDeleted(t, d, 0, 0, "record 5: the chain starts at record 7, so the records before it end with record 6")
		wantVerify(t, s, want)
		if got := loginNumbers(t, s); !slices.Equal(got, []uint64{1, 2, 3, 4, 5}) {
			t.Errorf("the login records are %v, want 1 to 5", got)
		}
	})
	t.Run("a gap at the end that ends before K-1", func(t *testing.T) {
		s, dir := allOldLeft(t)
		s = dropRecords(t, s, dir, audit.LoginLog, 5, 6)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		for _, n := range []uint64{5, 6} {
			if _, err := acceptOf(t, dir, audit.LoginLog, n); err == nil || !strings.Contains(err.Error(), "is not in the store") {
				t.Errorf("AcceptBreak(login, %d) = %v, want a record that is not in the store", n, err)
			}
		}
	})
}

// TestAuditRowsAtEndOfLog checks the rows that a repair or an acceptance leaves at the end of the log, when the records that a
// deletion left are before the chain start: VerifyAudit passes the head through them where they reach K-1 with the hash that the
// chain start has, or hold K-1 and K, which then lets compact drop those records; where the row ends before K-1, or ends with K-1
// with another hash, the log is not whole, VerifyAudit fails, and compact keeps the records and says why.
func TestAuditRowsAtEndOfLog(t *testing.T) {
	for _, kind := range []string{"hole", "gap"} {
		gap := kind == "gap"
		rowWord := "the hole of record 6"
		if gap {
			rowWord = "the accepted gap of record 6"
		}
		note := func(first, last uint64, after string) string {
			if gap {
				return missingNote("login", first, last, after)
			}
			return lostNote("login", first, last, after)
		}
		t.Run("a "+kind+" that ends with K-1 and has the hash that the chain start has", func(t *testing.T) {
			s, dir := allOldLeft(t)
			s = dropWithRow(t, s, dir, gap, 6, 6, hashOfRecord(t, s, 6))
			notes, verr := verifyOf(t, s)
			if verr != nil || !hasLeftNote(notes) {
				t.Fatalf("VerifyAudit() = %v with the notes %q, want it to pass and tell the records that are left", verr, notes)
			}
			if want := note(6, 6, endsWith(5)); !slices.Contains(notes, want) {
				t.Errorf("the notes are %q, want %q", notes, want)
			}
			s, d := compactClosed(t, s, dir)
			wantDeleted(t, d, 1, 5, "")
			wantVerify(t, s, "")
			if got := loginNumbers(t, s); len(got) != 0 {
				t.Errorf("the login records are %v, want none", got)
			}
		})
		t.Run("a "+kind+" that ends with K-1 and has another hash", func(t *testing.T) {
			// The head of the log is the hash that the row has, which is not the chain start's.
			s, dir := allOldLeft(t)
			s = rewriteHead(t, s, dir, 6, wrong)
			s = dropWithRow(t, s, dir, gap, 6, 6, wrong)
			want := `audit log "login": ` + rowWord + ` does not have the hash that the chain start has of record 6`
			wantVerify(t, s, want)
			s, d := compactClosed(t, s, dir)
			wantDeleted(t, d, 0, 0, "record 5: "+rowWord+" does not have the hash that the chain start has of record 6")
			wantVerify(t, s, want)
			if got := loginNumbers(t, s); !slices.Equal(got, []uint64{1, 2, 3, 4, 5}) {
				t.Errorf("the login records are %v, want 1 to 5", got)
			}
		})
		t.Run("a "+kind+" that ends before K-1", func(t *testing.T) {
			// The head names the record 5, which the row records as lost, and the chain start has the record 6 as the last that was
			// deleted: the log ends before it.
			s, dir := allOldLeft(t)
			sum := hashOfRecord(t, s, 5)
			s = dropRecords(t, s, dir, audit.LoginLog, 6)
			s = dropWithRow(t, s, dir, gap, 5, 5, sum)
			s = rewriteHead(t, s, dir, 5, sum)
			wantVerify(t, s, `audit log "login": the chain starts at record 7, but the log ends with `+strings.Replace(rowWord, "6", "5", 1))
			s, d := compactClosed(t, s, dir)
			wantDeleted(t, d, 0, 0, "record 4: the chain starts at record 7, so the records before it end with record 6")
			if got := loginNumbers(t, s); !slices.Equal(got, []uint64{1, 2, 3, 4}) {
				t.Errorf("the login records are %v, want 1 to 4", got)
			}
		})
		t.Run("a "+kind+" that holds K-1 and K", func(t *testing.T) {
			s, dir := eightRecordsLeft(t)
			s = dropWithRow(t, s, dir, gap, 6, 8, hashOfRecord(t, s, 8))
			notes, verr := verifyOf(t, s)
			if verr != nil || !hasLeftNote(notes) {
				t.Fatalf("VerifyAudit() = %v with the notes %q, want it to pass and tell the records that are left", verr, notes)
			}
			s, d := compactClosed(t, s, dir)
			wantDeleted(t, d, 1, 5, "")
			wantVerify(t, s, "")
			notes, _ = verifyOf(t, s)
			if want := note(6, 8, "the log has no record left"); !slices.Contains(notes, want) {
				t.Errorf("the notes after compact are %q, want %q", notes, want)
			}
		})
		t.Run("a "+kind+" that starts at K", func(t *testing.T) {
			s, dir := eightRecordsLeft(t)
			s = dropWithRow(t, s, dir, gap, 7, 8, hashOfRecord(t, s, 8))
			notes, verr := verifyOf(t, s)
			if verr != nil || !hasLeftNote(notes) {
				t.Fatalf("VerifyAudit() = %v with the notes %q, want it to pass and tell the records that are left", verr, notes)
			}
			s, d := compactClosed(t, s, dir)
			wantDeleted(t, d, 1, 6, "")
			wantVerify(t, s, "")
		})
		t.Run("two "+kind+"s that follow one another to the head, across K", func(t *testing.T) {
			s, dir := eightRecordsLeft(t)
			s = dropWithRow(t, s, dir, gap, 6, 6, hashOfRecord(t, s, 6))
			sum := hashOfRecord(t, s, 8)
			s = dropRecords(t, s, dir, audit.LoginLog, 7, 8)
			putRowFor(t, s, gap, 7, 8, sum)
			notes, verr := verifyOf(t, s)
			if verr != nil || !hasLeftNote(notes) {
				t.Fatalf("VerifyAudit() = %v with the notes %q, want it to pass and tell the records that are left", verr, notes)
			}
			if want := note(6, 6, "the next row, which starts with record 7, follows it at once"); !slices.Contains(notes, want) {
				t.Errorf("the notes are %q, want %q", notes, want)
			}
			if want := note(7, 8, endsWith(5)); !slices.Contains(notes, want) {
				t.Errorf("the notes are %q, want %q", notes, want)
			}
			s, d := compactClosed(t, s, dir)
			wantDeleted(t, d, 1, 5, "")
			wantVerify(t, s, "")
		})
	}
}

// rewriteHead sets the head of the login history of s, which is closed and opened again, to the record n with the hash sum.
func rewriteHead(t *testing.T, s *Store, dir string, n uint64, sum [sha256.Size]byte) *Store {
	t.Helper()
	head := audit.Head{Log: audit.LoginLog, N: n, Hash: sum}
	return closeAndRewrite(t, s, dir, rewriteEvents(nil, nil, map[audit.Log]*audit.Head{audit.LoginLog: &head}))
}

// TestAuditRealRepairAcrossChainStart checks what a repair of an honest store leaves, when the deletion left the records 1 to 4
// before the chain start, which is at the record 5, and the repair drops records that are unreadable around K: whatever it
// records, the doctor (VerifyAudit and Check) has nothing to report, the next compact drops the records that the deletion left in
// one call, and the writer's next login record does not change that.
func TestAuditRealRepairAcrossChainStart(t *testing.T) {
	for _, tt := range []struct {
		name       string
		unreadable []uint64
		hole       string // the hole that the repair records: its first, its last
		left       string // the login records that remain after the first compact
	}{
		{"the records before K and K (a hole that holds K)", []uint64{4, 5}, "4 5", "[6]"},
		{"K (a hole at K)", []uint64{5}, "5 5", "[6]"},
		{"the records before K (a hole that ends with K-1)", []uint64{3, 4}, "3 4", "[5 6]"},
		{"the records at the end that hold K", []uint64{4, 5, 6}, "4 6", "[]"},
		{"the records at the end from K", []uint64{5, 6}, "5 6", "[]"},
		{"the last record before K, at the end of a log of old records", []uint64{4}, "4 4", "[]"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var s *Store
			var dir string
			if tt.hole == "4 4" {
				// Every login record is old: the records 1 to 4 are left, the chain starts at the record 5, and the head is the record 4.
				s, dir, _ = agedStore(t, []int{40, 40, 30, 30, 14, 13, 0}, []bool{true, false, true, false, true, true, false})
				s = leaveDeletion(t, s, dir)
			} else {
				s, dir = sixRecordsLeft(t)
			}
			replace := map[uint64][]byte{}
			for _, n := range tt.unreadable {
				replace[recordEvent(t, s, audit.LoginLog, n).Number] = []byte("not a record")
			}
			s = closeAndRewrite(t, s, dir, rewriteEvents(replace, nil, nil))
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			opts := dbserver.Options{Log: logdb.Options{Placement: logdb.Hybrid}}
			rep, err := dbserver.Repair(t.Context(), dir, opts, dbserver.RepairOptions{Apply: true, Keep: filepath.Join(t.TempDir(), "keep"),
				DropUnreadable: true, Now: chainTime})
			if err != nil || len(rep.Holes) != 1 || len(rep.Gaps) != 0 || !rep.Applied {
				t.Fatalf("Repair() = holes %+v gaps %+v applied %v, %v, want one hole", rep.Holes, rep.Gaps, rep.Applied, err)
			}
			if got := fmt.Sprintf("%d %d", rep.Holes[0].First, rep.Holes[0].Last); got != tt.hole {
				t.Fatalf("the repair recorded the hole %s, want %s", got, tt.hole)
			}
			s = reopenLog(t, dir, logdb.Options{})
			problems, notes := doctorOf(t, s, dir, nil)
			if len(problems) != 0 || !hasLeftNote(notes) {
				t.Fatalf("doctor finds %q with the notes %q, want no problem and the records that are left", problems, notes)
			}
			s, d := compactClosed(t, s, dir)
			if d.First != 1 || d.Stopped != "" {
				t.Errorf("the first CompactLogin() dropped the records %d to %d and stopped at %q, want the records that the deletion left",
					d.First, d.Last, d.Stopped)
			}
			if got := fmt.Sprint(loginNumbers(t, s)); got != tt.left {
				t.Errorf("the login records are %s, want %s", got, tt.left)
			}
			problems, notes = doctorOf(t, s, dir, nil)
			if len(problems) != 0 || hasLeftNote(notes) {
				t.Errorf("doctor finds %q with the notes %q after compact, want no problem", problems, notes)
			}
			// The writer adds its next login record.
			update(t, s, func(tx *Tx) error { return tx.Record(loginRecord(audit.LoginFailure, "next")) })
			if problems, notes = doctorOf(t, s, dir, nil); len(problems) != 0 {
				t.Errorf("doctor finds %q with the notes %q after the next login record, want no problem", problems, notes)
			}
			s, d = compactClosed(t, s, dir)
			if d.First != 0 || d.Stopped != "" {
				t.Errorf("the second CompactLogin() = %+v, want nothing to drop", d)
			}
			if problems, notes = doctorOf(t, s, dir, nil); len(problems) != 0 {
				t.Errorf("doctor finds %q with the notes %q at the end, want no problem", problems, notes)
			}
		})
	}
}

// TestAuditDropLeftoversKeepsPassing checks what makes compact's drop of the records that a deletion left safe: the store that
// VerifyAudit passes with them passes without them, whatever hole or gap holds K or ends the log.
func TestAuditDropLeftoversKeepsPassing(t *testing.T) {
	t.Run("an end hole that ends with K-1", func(t *testing.T) {
		s, dir, _ := agedStore(t, []int{40, 40, 30, 30, 14, 13, 0}, []bool{true, false, true, false, true, true, false})
		s = leaveDeletion(t, s, dir)
		s = dropWithRow(t, s, dir, false, 4, 4, hashOfRecord(t, s, 4))
		wantVerify(t, s, "")
		s = dropRecords(t, s, dir, audit.LoginLog, 1, 2, 3)
		wantVerify(t, s, "")
	})
	t.Run("a hole that holds K", func(t *testing.T) {
		s, dir := sixRecordsLeft(t)
		s = dropWithRow(t, s, dir, false, 4, 5, hashOfRecord(t, s, 5))
		wantVerify(t, s, "")
		s = dropRecords(t, s, dir, audit.LoginLog, 1, 2, 3)
		wantVerify(t, s, "")
	})
}

// dropDeletedEvent closes s, drops the event of the deletion from its log, which a loss of events does, and opens it again.
func dropDeletedEvent(t *testing.T, s *Store, dir string) *Store {
	t.Helper()
	var numbers []uint64
	for _, e := range allEvents(t, s) {
		if _, ok := dbwire.ParseDeleted(e.Data); ok {
			numbers = append(numbers, e.Number)
		}
	}
	if len(numbers) != 1 {
		t.Fatalf("the log has %d events of a deletion, want 1", len(numbers))
	}
	return closeAndRewrite(t, s, dir, rewriteEvents(nil, numbers, nil))
}

// leftoverShape is one shape of the rows and the records around the first record that remains after a deletion, K, for
// TestAuditLeftoverNoteMatchesCompact.
type leftoverShape struct {
	name string
	// build makes the store, with the records that a deletion left before K, and prepare changes it, with a hole or, if gap, an
	// accepted gap for what it drops.
	build   func(t *testing.T) (*Store, string)
	prepare func(t *testing.T, s *Store, dir string, gap bool) *Store
	// accept is the record that accept-break is asked to accept, if not 0, and refused the refusal, "" if it accepts.
	accept  uint64
	refused string
	// verify is whether VerifyAudit passes then, drops the last record that compact drops, of those that start with the record 1
	// (0: none), stop is where compact stops, "" if it does not, and after is whether VerifyAudit passes after compact, which is
	// what it did before, but where mends says that compact makes it pass: the records that a deletion left are what it drops, and
	// the log, which then starts at K, is one that a row passes.
	verify, after, mends bool
	drops                uint64
	stop                 string
}

func leftoverShapes() []leftoverShape {
	rowOf := func(t *testing.T, s *Store, dir string, gap bool, first, last uint64) *Store {
		t.Helper()
		return dropWithRow(t, s, dir, gap, first, last, hashOfRecord(t, s, last))
	}
	const kPrev = "record 4: the number does not follow that of record 2"
	same := func(t *testing.T, s *Store, dir string, gap bool) *Store { return s }
	return []leftoverShape{
		{name: "the records that a deletion left", build: threeOldLeft, verify: true, after: true, drops: 3, prepare: same},
		{name: "the records that a deletion left, without the event of the deletion", build: threeOldLeft, verify: true, after: true, drops: 3,
			prepare: func(t *testing.T, s *Store, dir string, gap bool) *Store { return dropDeletedEvent(t, s, dir) }},
		{name: "a row that ends with K-1 with the hash that the chain start has", build: threeOldLeft, verify: true, after: true, drops: 2,
			prepare: func(t *testing.T, s *Store, dir string, gap bool) *Store { return rowOf(t, s, dir, gap, 3, 3) }},
		{name: "a row that ends with K-1 with the hash that the chain start has, without the event of the deletion", build: threeOldLeft,
			verify: true, after: true, drops: 2,
			prepare: func(t *testing.T, s *Store, dir string, gap bool) *Store {
				return dropDeletedEvent(t, rowOf(t, s, dir, gap, 3, 3), dir)
			}},
		{name: "a row that ends with K-1 with another hash", build: threeOldLeft, stop: kPrev,
			prepare: func(t *testing.T, s *Store, dir string, gap bool) *Store {
				return dropWithRow(t, s, dir, gap, 3, 3, wrong)
			}},
		{name: "a row that ends with K-1 with another hash, which is replaced by the acceptance of the gap", build: threeOldLeft,
			verify: true, after: true, drops: 2, accept: 4,
			prepare: func(t *testing.T, s *Store, dir string, gap bool) *Store {
				return dropWithRow(t, s, dir, gap, 3, 3, wrong)
			}},
		{name: "a row that ends with K-1 with another hash, which the record K has as its prev", build: threeOldLeft, stop: kPrev,
			prepare: func(t *testing.T, s *Store, dir string, gap bool) *Store {
				s = rewriteRecord(t, s, dir, audit.LoginLog, 4, func(r *audit.Record) { r.Prev = hashText(wrong[:]) })
				return dropWithRow(t, s, dir, gap, 3, 3, wrong)
			}},
		{name: "a row at K after the record K-1 that the deletion left", build: threeOldLeft, verify: true, after: true, drops: 3,
			prepare: func(t *testing.T, s *Store, dir string, gap bool) *Store { return rowOf(t, s, dir, gap, 4, 4) }},
		{name: "a row at K after the record K-1 that the deletion left, without the event of the deletion", build: threeOldLeft, verify: true,
			after: true, drops: 3,
			prepare: func(t *testing.T, s *Store, dir string, gap bool) *Store {
				return dropDeletedEvent(t, rowOf(t, s, dir, gap, 4, 4), dir)
			}},
		{name: "a row at K after a record K-1 that has been changed", build: threeOldLeft,
			stop: "record 3: its hash is not the hash that the chain start has of it",
			prepare: func(t *testing.T, s *Store, dir string, gap bool) *Store {
				sum := hashOfRecord(t, s, 4)
				s = rewriteRecord(t, s, dir, audit.LoginLog, 3, func(r *audit.Record) { r.Login = "forged" })
				return dropWithRow(t, s, dir, gap, 4, 4, sum)
			}},
		{name: "a gap at K after a record K-1 that has been changed, which is not accepted", build: threeOldLeft, accept: 5,
			stop: "record 3: its hash is not the hash that the chain start has of it",
			refused: "record 5 comes after records before it that an earlier deletion left, the last of which, record 3, does not have the " +
				"hash that the chain start has of it: the gap before record 5 is not accepted",
			prepare: func(t *testing.T, s *Store, dir string, gap bool) *Store {
				s = rewriteRecord(t, s, dir, audit.LoginLog, 3, func(r *audit.Record) { r.Login = "forged" })
				return dropRecords(t, s, dir, audit.LoginLog, 4)
			}},
		{name: "a row that holds K after the records that a deletion left", build: threeOldLeft, verify: true, after: true, drops: 2,
			prepare: func(t *testing.T, s *Store, dir string, gap bool) *Store { return rowOf(t, s, dir, gap, 3, 4) }},
		{name: "a gap that holds K after the records that a deletion left, which is accepted", build: threeOldLeft, verify: true, after: true,
			drops: 2, accept: 5,
			prepare: func(t *testing.T, s *Store, dir string, gap bool) *Store {
				return dropRecords(t, s, dir, audit.LoginLog, 3, 4)
			}},
		{name: "a row that holds K, without the event of the deletion", build: threeOldLeft, verify: true, after: true, drops: 2,
			prepare: func(t *testing.T, s *Store, dir string, gap bool) *Store {
				return dropDeletedEvent(t, rowOf(t, s, dir, gap, 3, 4), dir)
			}},
		{name: "a row that holds K and starts with a record that is there, which compact mends", build: threeOldLeft, drops: 3, after: true,
			mends: true, stop: "record 5: the chain starts at record 4, which is not there",
			prepare: func(t *testing.T, s *Store, dir string, gap bool) *Store {
				// The record 3 is there: a row that starts with it is not at the record after the last that was read, so that the
				// records that a deletion left, which compact drops, are what VerifyAudit fails at; the log then starts at K.
				sum := hashOfRecord(t, s, 4)
				s = dropRecords(t, s, dir, audit.LoginLog, 4)
				putRowFor(t, s, gap, 3, 4, sum)
				return s
			}},
		{name: "two rows that follow one another across K", build: threeOldLeft, verify: true, after: true, drops: 2,
			prepare: func(t *testing.T, s *Store, dir string, gap bool) *Store {
				sum := hashOfRecord(t, s, 4)
				s = rowOf(t, s, dir, gap, 3, 3)
				s = dropRecords(t, s, dir, audit.LoginLog, 4)
				putRowFor(t, s, gap, 4, 4, sum)
				return s
			}},
		{name: "two rows that follow one another across K, the first ending with K-1 with another hash", build: threeOldLeft,
			stop: "record 5: the hole of record 3 does not have the hash that the chain start has of record 3",
			prepare: func(t *testing.T, s *Store, dir string, gap bool) *Store {
				sum := hashOfRecord(t, s, 4)
				s = dropWithRow(t, s, dir, gap, 3, 3, wrong)
				s = dropRecords(t, s, dir, audit.LoginLog, 4)
				putRowFor(t, s, gap, 4, 4, sum)
				return s
			}},

		// The end of the log, where the head is what a row ends with.
		{name: "the records of a log that a deletion left", build: allOldLeft, verify: true, after: true, drops: 6, prepare: same},
		{name: "a row at the end that ends with K-1 with the hash that the chain start has", build: allOldLeft, verify: true, after: true, drops: 5,
			prepare: func(t *testing.T, s *Store, dir string, gap bool) *Store { return rowOf(t, s, dir, gap, 6, 6) }},
		{name: "a row at the end that ends with K-1 with another hash", build: allOldLeft,
			stop: "record 5: the hole of record 6 does not have the hash that the chain start has of record 6",
			prepare: func(t *testing.T, s *Store, dir string, gap bool) *Store {
				return dropWithRow(t, rewriteHead(t, s, dir, 6, wrong), dir, gap, 6, 6, wrong)
			}},
		{name: "a row at the end that ends before K-1", build: allOldLeft,
			stop: "record 4: the chain starts at record 7, so the records before it end with record 6",
			prepare: func(t *testing.T, s *Store, dir string, gap bool) *Store {
				sum := hashOfRecord(t, s, 5)
				s = dropRecords(t, s, dir, audit.LoginLog, 6)
				return rewriteHead(t, dropWithRow(t, s, dir, gap, 5, 5, sum), dir, 5, sum)
			}},
		{name: "no row at the end, and the record K-1 is lost", build: allOldLeft, accept: 6,
			refused: "record 6 of the audit log login is not in the store: the records before record 7 were deleted",
			stop:    "record 5: the chain starts at record 7, so the records before it end with record 6",
			prepare: func(t *testing.T, s *Store, dir string, gap bool) *Store {
				return dropRecords(t, s, dir, audit.LoginLog, 6)
			}},
		{name: "a row at the end that holds K", build: eightRecordsLeft, verify: true, after: true, drops: 5,
			prepare: func(t *testing.T, s *Store, dir string, gap bool) *Store { return rowOf(t, s, dir, gap, 6, 8) }},
		{name: "a gap at the end that holds K, which is accepted", build: eightRecordsLeft, verify: true, after: true, drops: 5, accept: 8,
			prepare: func(t *testing.T, s *Store, dir string, gap bool) *Store {
				return dropRecords(t, s, dir, audit.LoginLog, 6, 7, 8)
			}},
		{name: "a row at the end that starts at K", build: eightRecordsLeft, verify: true, after: true, drops: 6,
			prepare: func(t *testing.T, s *Store, dir string, gap bool) *Store { return rowOf(t, s, dir, gap, 7, 8) }},
		{name: "two rows at the end that follow one another across K", build: eightRecordsLeft, verify: true, after: true, drops: 5,
			prepare: func(t *testing.T, s *Store, dir string, gap bool) *Store {
				s = rowOf(t, s, dir, gap, 6, 6)
				sum := hashOfRecord(t, s, 8)
				s = dropRecords(t, s, dir, audit.LoginLog, 7, 8)
				putRowFor(t, s, gap, 7, 8, sum)
				return s
			}},
	}
}

// TestAuditLeftoverNoteMatchesCompact checks, for every shape of the rows and the records around the first record that remains
// after a deletion (K), the note of VerifyAudit that the next whiteroom-db compact removes the records that the deletion left
// before it: it is told if and only if compact drops them, and compact does not change whether VerifyAudit passes. Where a shape
// has a record that VerifyAudit fails at, whiteroom-db accept-break is asked to accept it, if the shape says so.
func TestAuditLeftoverNoteMatchesCompact(t *testing.T) {
	for _, tt := range leftoverShapes() {
		for _, gap := range []bool{false, true} {
			name := tt.name + ", with a hole"
			if gap {
				name = tt.name + ", with an accepted gap"
			}
			// A shape that has no row is run once.
			if gap && !strings.Contains(tt.name, "row") {
				continue
			}
			t.Run(name, func(t *testing.T) {
				s, dir := tt.build(t)
				s = tt.prepare(t, s, dir, gap)
				if tt.accept != 0 {
					events, rows := allEvents(t, s), len(rawTable(t, s, tableChains))
					if err := s.Close(); err != nil {
						t.Fatal(err)
					}
					_, err := acceptOf(t, dir, audit.LoginLog, tt.accept)
					switch {
					case tt.refused == "" && err != nil:
						t.Fatalf("AcceptBreak(login, %d) = %v, want it accepted", tt.accept, err)
					case tt.refused != "" && (err == nil || err.Error() != tt.refused):
						t.Fatalf("AcceptBreak(login, %d) = %v, want %q", tt.accept, err, tt.refused)
					}
					s = reopenLog(t, dir, logdb.Options{})
					if tt.refused != "" && (!eventsSame(events, allEvents(t, s)) || len(rawTable(t, s, tableChains)) != rows) {
						t.Error("the refusal changed the store")
					}
				}
				notes, verr := verifyOf(t, s)
				if (verr == nil) != tt.verify {
					t.Fatalf("VerifyAudit() = %v, want it to pass: %v", verr, tt.verify)
				}
				// The note is told if and only if compact drops the records that were left, where VerifyAudit passes.
				if got := hasLeftNote(notes); got != (tt.verify && tt.drops != 0) {
					t.Errorf("the notes are %q: the records that are left are told: %v, but compact drops them: %v", notes, got, tt.drops != 0)
				}
				if !tt.verify && tt.drops != 0 && !tt.mends {
					t.Error("the shape is one that compact drops the records of, which VerifyAudit fails at")
				}
				s, d := compactClosed(t, s, dir)
				stop := tt.stop
				if gap {
					stop = strings.Replace(stop, "the hole of", "the accepted gap of", 1)
				}
				wantDeleted(t, d, min(tt.drops, 1), tt.drops, stop)
				if again := s.VerifyAudit(t.Context(), nil); (again == nil) != tt.after {
					t.Errorf("VerifyAudit() after compact = %v, want it to pass: %v", again, tt.after)
				}
				if tt.verify != tt.after && !tt.mends {
					t.Fatal("the shape has a store that compact makes pass or fail")
				}
			})
		}
	}
}

// TestAuditLeftoverNoteOnOlderSchema checks that the note of the records that a deletion left promises that the next compact removes
// them only in a store of the current table schema, which is where compact drops them: in an older one, which compact leaves as it
// is, the note says that it removes them after a writer has upgraded the store, and then it does.
func TestAuditLeftoverNoteOnOlderSchema(t *testing.T) {
	s, dir := threeOldLeft(t)
	rawUpdate(t, s, func(tx *logdb.Tx) error { return tx.Put(tableMeta, []byte(metaSchema), []byte{9}) })
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := openLogReader(dir, logOpts(logdb.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	notes, verr := verifyOf(t, r)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	const left = `audit log "login": the records 1 to 3 are still in the store after the deletion of the records before record 4; `
	if want := []string{startNote(4), left + "whiteroom-db compact removes them after a writer has upgraded the store"}; verr != nil ||
		!slices.Equal(notes, want) {
		t.Errorf("VerifyAudit() of the older store = %v with the notes %q, want it to pass, with %q", verr, notes, want)
	}
	if d, err := compactLoginOf(t, dir, chainTime, nil); err != nil || d.Schema != 9 || d.First != 0 || d.Last != 0 {
		t.Fatalf("CompactLogin() of the older store = %+v, %v, want nothing dropped, and the schema 9 told", d, err)
	}

	// The first writer that opens the store upgrades it: the note is the one that compact keeps, and compact drops the records.
	s = reopenLog(t, dir, logdb.Options{})
	notes, verr = verifyOf(t, s)
	if want := []string{startNote(4), left + "the next whiteroom-db compact removes them"}; verr != nil || !slices.Equal(notes, want) {
		t.Errorf("VerifyAudit() after the upgrade = %v with the notes %q, want it to pass, with %q", verr, notes, want)
	}
	s, d := compactClosed(t, s, dir)
	wantDeleted(t, d, 1, 3, "")
	if err := s.VerifyAudit(t.Context(), nil); err != nil {
		t.Errorf("VerifyAudit() after compact = %v", err)
	}
}
