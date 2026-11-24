package store

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// loginEventOf returns the event of the login record n of s.
func loginEventOf(t *testing.T, s *Store, n uint64) logdb.Event {
	t.Helper()
	for _, e := range rawEvents(t, s) {
		if log, got, _, ok := dbwire.ReadRecordID(e.Data); ok && log == dbwire.LogLogin && got == n {
			return e
		}
	}
	t.Fatalf("no event of the login record %d", n)
	return logdb.Event{}
}

// eventsSame reports whether the events of a and b are the same, with their numbers.
func eventsSame(a, b []logdb.Event) bool {
	return slices.EqualFunc(a, b, func(x, y logdb.Event) bool {
		return x.Number == y.Number && x.Seq == y.Seq && string(x.Data) == string(y.Data)
	})
}

// TestAuditCompactLoginIdle checks the cutoff of a store that its writer has not committed to for a long time: the mark of
// the commit of a deletion or of an acceptance of a break is not the newest mark, so that the calls that follow cut at the
// same time, whatever the time of the maintenance; and a commit of the writer that has only staged objects moves it.
func TestAuditCompactLoginIdle(t *testing.T) {
	// The login records 1 to 4 are 40, 35, 25 and 20 months old, and the last commit of the writer, which has no login record,
	// 18 months: the cutoff is 30 months before chainTime.
	months := []int{40, 35, 25, 20, 18}
	login := []bool{true, true, true, true, false}
	cutoff := chainTime.AddDate(0, -18, 0).AddDate(-1, 0, 0)

	t.Run("a deletion", func(t *testing.T) {
		s, dir, _ := agedStore(t, months, login)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		d, err := compactLoginOf(t, dir, chainTime, nil)
		if err != nil || d.First != 1 || d.Last != 2 || !d.Cutoff.Equal(cutoff) {
			t.Fatalf("CompactLogin() = %+v, %v, want the records 1 to 2, from before %v", d, err, cutoff)
		}
		// The second call has no commit of the writer between: it finds the same cutoff, and drops nothing.
		d, err = compactLoginOf(t, dir, chainTime, nil)
		if err != nil || d.First != 0 || !d.Cutoff.Equal(cutoff) {
			t.Errorf("the second CompactLogin() = %+v, %v, want nothing, from before %v", d, err, cutoff)
		}
		s = reopenLog(t, dir, logdb.Options{})
		checkVerify(t, s, dir, nil, nil, "", startNote(3))
		if got := recordsOf(t, s, audit.LoginLog); len(got) != 2 || !strings.HasPrefix(got[0], "login 3 ") {
			t.Errorf("the login records are %v, want the records 3 and 4", got)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}

		// A writer that opens the store and commits nothing does not move it either; one whose commit has staged objects and no
		// op does, as the commit of the daemon has its mark.
		later := chainTime.AddDate(0, -1, 0)
		o := options{log: &logdb.Options{Placement: logdb.Hybrid}, now: func() time.Time { return later }}
		w, err := open(dir, o)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if d, err := compactLoginOf(t, dir, chainTime, nil); err != nil || d.First != 0 || !d.Cutoff.Equal(cutoff) {
			t.Fatalf("CompactLogin() after the open of a writer = %+v, %v, want nothing, from before %v", d, err, cutoff)
		}
		w, err = open(dir, o)
		if err != nil {
			t.Fatal(err)
		}
		object := []byte("an object that no row names")
		if err := w.b.(*logBackend).c.Update(t.Context(), func(tx *dbclient.Tx) error {
			return tx.PutObject(sha256.Sum256(object), object)
		}); err != nil {
			t.Fatalf("the commit of the writer = %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		d, err = compactLoginOf(t, dir, chainTime, nil)
		if want := later.AddDate(-1, 0, 0); err != nil || d.First != 3 || d.Last != 4 || !d.Cutoff.Equal(want) {
			t.Errorf("CompactLogin() after the commit of the writer = %+v, %v, want the records 3 to 4, from before %v", d, err, want)
		}
	})

	t.Run("the acceptance of a break", func(t *testing.T) {
		s, dir, _ := agedStore(t, months, login)
		// The record 3 is another one, so that the record 4 is a break.
		e := loginEventOf(t, s, 3)
		data := changedRecord(t, e.Data, func(r *audit.Record) { r.Login = "forged" })
		s = closeAndRewrite(t, s, dir, rewriteEvents(map[uint64][]byte{e.Number: data}, nil, nil))
		s = closeAcceptReopen(t, s, dir, audit.LoginLog, 4)
		checkVerify(t, s, dir, nil, nil, "", breakNote("login", 4))
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		for i := range 2 {
			d, err := compactLoginOf(t, dir, chainTime, nil)
			want := dbserver.Deleted{First: 1, Last: 2}
			if i == 1 {
				want = dbserver.Deleted{}
			}
			if err != nil || d.First != want.First || d.Last != want.Last || !d.Cutoff.Equal(cutoff) {
				t.Errorf("CompactLogin() number %d after the acceptance = %+v, %v, want %+v, from before %v", i+1, d, err, want, cutoff)
			}
		}
		s = reopenLog(t, dir, logdb.Options{})
		checkVerify(t, s, dir, nil, nil, "", startNote(3), breakNote("login", 4))
	})
}

// TestAuditCompactLoginLeftoverEvidence checks that whiteroom-db compact keeps the login records before the chain start
// when they are not the end of the records that a deletion left, and says where it stopped: a record that a writer commits
// below the chain start, whatever its age, is what VerifyAudit reports, and compact does not drop it; nor does it drop a
// record that is changed after the deletion stopped, which VerifyAudit reports too, or the one before the chain start.
func TestAuditCompactLoginLeftoverEvidence(t *testing.T) {
	t.Run("a record of a deleted number after a deletion that left none", func(t *testing.T) {
		s, dir, _ := agedStore(t, []int{40, 38, 36, 0}, []bool{true, true, true, false})
		replayed := loginEventOf(t, s, 1).Data
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if d, err := compactLoginOf(t, dir, chainTime, nil); err != nil || d.First != 1 || d.Last != 3 {
			t.Fatalf("CompactLogin() = %+v, %v, want the records 1 to 3", d, err)
		}
		s = reopenLog(t, dir, logdb.Options{})
		checkVerify(t, s, dir, nil, nil, "", startNote(4))
		// The writer commits the bytes of the deleted record 1, which the daemon marks with the time of the commit.
		if err := s.b.(*logBackend).c.Update(t.Context(), func(tx *dbclient.Tx) error { return tx.AppendEvent(replayed) }); err != nil {
			t.Fatalf("the commit of the writer = %v", err)
		}
		verr := s.VerifyAudit(t.Context(), nil)
		if verr == nil {
			t.Fatal("VerifyAudit() passes with a login record before the chain start, and no record after it")
		}
		before := allEvents(t, s)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		d, err := compactLoginOf(t, dir, chainTime, nil)
		want := "record 1: the chain starts at record 4, so the records before it end with record 3"
		if err != nil || d.First != 0 || d.Last != 0 || d.Stopped != want {
			t.Fatalf("CompactLogin() = %+v, %v, want nothing dropped, and the stop %q", d, err, want)
		}
		s = reopenLog(t, dir, logdb.Options{})
		if got := s.VerifyAudit(t.Context(), nil); got == nil || got.Error() != verr.Error() {
			t.Errorf("VerifyAudit() after the call = %v, want %v", got, verr)
		}
		if got := recordsOf(t, s, audit.LoginLog); len(got) != 1 || !strings.HasPrefix(got[0], "login 1 ") {
			t.Errorf("the login records are %v, want the record that the writer committed", got)
		}
		if !eventsSame(before, allEvents(t, s)) {
			t.Error("the call changed the events of the store")
		}
	})

	// A deletion that stopped before its compaction leaves the records 1 to 4, and the chain start is before the record 5.
	months := []int{40, 40, 30, 30, 14, 13, 11, 2, 0}
	login := []bool{true, false, true, false, true, true, true, true, false}
	for _, tt := range []struct {
		name    string
		changed uint64 // the number of the login record that is changed after the deletion stopped
		verify  string // what VerifyAudit reports of it, with the number of the event of the record that fails
		stopped string
	}{
		{"a record before the chain start is changed", 2, `audit log "login": record 3 (event %d): prev is not the hash of record 2`,
			"record 3: prev is not the hash of record 2"},
		{"the record before the chain start is changed", 4, `audit log "login": record 5 (event %d): prev is not the hash of record 4`,
			"record 4: its hash is not the hash that the chain start has of it"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, dir, _ := agedStore(t, months, login)
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
			checkVerify(t, s, dir, nil, nil, "", startNote(5), leftNote("the records 1 to 4 are", 5))
			e := loginEventOf(t, s, tt.changed)
			data := changedRecord(t, e.Data, func(r *audit.Record) { r.Login = "forged" })
			s = closeAndRewrite(t, s, dir, rewriteEvents(map[uint64][]byte{e.Number: data}, nil, nil))
			next := eventOf(t, s, audit.LoginLog, tt.changed+1)
			verr := s.VerifyAudit(t.Context(), nil)
			if verr == nil || verr.Error() != fmt.Sprintf(tt.verify, next) {
				t.Fatalf("VerifyAudit() = %v, want %q", verr, fmt.Sprintf(tt.verify, next))
			}
			before, records := allEvents(t, s), recordsOf(t, s, audit.LoginLog)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			d, err := compactLoginOf(t, dir, chainTime, nil)
			if err != nil || d.First != 0 || d.Last != 0 || d.Stopped != tt.stopped {
				t.Fatalf("CompactLogin() = %+v, %v, want nothing dropped, and the stop %q", d, err, tt.stopped)
			}
			s = reopenLog(t, dir, logdb.Options{})
			if got := s.VerifyAudit(t.Context(), nil); got == nil || got.Error() != verr.Error() {
				t.Errorf("VerifyAudit() after the call = %v, want %v", got, verr)
			}
			if got := recordsOf(t, s, audit.LoginLog); !slices.Equal(got, records) {
				t.Errorf("the login records are %v, want all of %v", got, records)
			}
			if !eventsSame(before, allEvents(t, s)) {
				t.Error("the call changed the events of the store")
			}
		})
	}
}
