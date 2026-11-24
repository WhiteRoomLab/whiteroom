package store

import (
	"crypto/sha256"
	"encoding/binary"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// The tests here are the cases that a review of whiteroom-db compact and accept-break found: the maintenance must not take
// away what VerifyAudit reports, and an accepted break must not be honored where the chain has no break.

// replaceHead puts the head of log, in meta and in the heads for the export, to the record n with the hash sum, in one commit
// of the writer of s, as a writer that sends what the store never sends does.
func replaceHead(t *testing.T, s *Store, log audit.Log, n uint64, sum [sha256.Size]byte, events ...[]byte) {
	t.Helper()
	h := audit.Head{Log: log, N: n, Hash: sum}
	if err := s.b.(*logBackend).c.Update(t.Context(), func(tx *dbclient.Tx) error {
		for _, e := range events {
			if err := tx.AppendEvent(e); err != nil {
				return err
			}
		}
		if err := tx.Put(tableMeta, []byte(metaHeads[log]), append(binary.AppendUvarint(nil, n), sum[:]...)); err != nil {
			return err
		}
		return tx.Put(tableHeads, []byte(log), []byte(h.String()))
	}); err != nil {
		t.Fatalf("the commit of the writer = %v", err)
	}
}

// TestAuditUndecodableRecordIsEvidence checks that an event that audit.Decode refuses, which VerifyAudit reports as no
// record, is no record to whiteroom-db compact either, though it has a log, a number and a prev that chain: the call keeps
// it and the records around it, and says where it stopped.
func TestAuditUndecodableRecordIsEvidence(t *testing.T) {
	var clock atomic.Value
	clock.Store(chainTime.AddDate(0, -40, 0))
	o := options{log: &logdb.Options{Placement: logdb.Hybrid}, now: func() time.Time { return clock.Load().(time.Time) }}
	dir := filepath.Join(t.TempDir(), "data")
	s, err := create(dir, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	update(t, s, func(tx *Tx) error { return tx.Record(loginRecord(audit.LoginFailure, "a")) })
	first := loginEventOf(t, s, 1).Data
	sum1 := sha256.Sum256(first)

	// A writer commits a record with a member that the record has not, which has the hash of record 1 as its prev, and sets the
	// head to it: the daemon's rules allow it, and the next record of the store has its hash.
	clock.Store(chainTime.AddDate(0, -38, 0))
	r, err := audit.Decode(first)
	if err != nil {
		t.Fatal(err)
	}
	r.N, r.Prev = 2, hashText(sum1[:])
	enc, err := audit.Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	forged := append([]byte(`{"extra":"1",`), enc[1:]...)
	if _, err := audit.Decode(forged); err == nil {
		t.Fatal("audit.Decode reads the forged record")
	}
	if _, _, _, ok := dbwire.ReadRecordID(forged); ok {
		t.Error("dbwire.ReadRecordID reads what audit.Decode refuses")
	}
	replaceHead(t, s, audit.LoginLog, 2, sha256.Sum256(forged), forged)
	clock.Store(chainTime.AddDate(0, -36, 0))
	update(t, s, func(tx *Tx) error { return tx.Record(loginRecord(audit.LoginFailure, "b")) })
	clock.Store(chainTime)
	update(t, s, func(tx *Tx) error { return tx.Record(eventRecord("now")) })
	verr := s.VerifyAudit(t.Context(), nil)
	if verr == nil {
		t.Fatal("VerifyAudit() passes with the forged record")
	}
	before := allEvents(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	d, err := compactLoginOf(t, dir, chainTime, nil)
	if err != nil || d.First != 0 || !strings.HasSuffix(d.Stopped, ": it is not an audit record") {
		t.Fatalf("CompactLogin() = %+v, %v, want nothing dropped, and the stop at the event that is no record", d, err)
	}
	s = reopenLog(t, dir, logdb.Options{})
	if got := s.VerifyAudit(t.Context(), nil); got == nil || got.Error() != verr.Error() {
		t.Errorf("VerifyAudit() after the call = %v, want %v", got, verr)
	}
	if !eventsSame(before, allEvents(t, s)) {
		t.Error("the call changed the events of the store")
	}
}

// TestAuditGapIsNoBreak checks that a record that a gap leaves, where the record before it is lost, is not accepted as a
// break: whiteroom-db accept-break refuses it, and writes nothing, so that VerifyAudit goes on reporting it.
func TestAuditGapIsNoBreak(t *testing.T) {
	s, dir, heads := auditFixture(t)
	// Position 6 is login record 2.
	s = closeAndRewrite(t, s, dir, rewriteRecords(nil, []uint64{6}, nil))
	verr := s.VerifyAudit(t.Context(), heads)
	if verr == nil || !strings.Contains(verr.Error(), "the number does not follow that of record 1") {
		t.Fatalf("VerifyAudit() with login record 2 lost = %v", verr)
	}
	before, rows := allEvents(t, s), rawTable(t, s, tableChains)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	want := "record 3 does not follow record 1 in number: a gap, which a lost record leaves, is not a break that can be accepted, " +
		"and waits for the repair of the engine"
	if err := acceptBreakOf(t, dir, audit.LoginLog, 3); err == nil || err.Error() != want {
		t.Fatalf("AcceptBreak(login, 3) = %v, want %q", err, want)
	}
	s = reopenLog(t, dir, logdb.Options{})
	if got := s.VerifyAudit(t.Context(), heads); got == nil || got.Error() != verr.Error() {
		t.Errorf("VerifyAudit() after the refusal = %v, want %v", got, verr)
	}
	if !eventsSame(before, allEvents(t, s)) || len(rawTable(t, s, tableChains)) != len(rows) {
		t.Error("the refusal changed the store")
	}
}

// TestAuditAcceptedBreakIsNotReplayed checks that the acceptance of a break is honored for the record that it was made for,
// once: a writer that commits the bytes of that record again, with the head set to it, makes a fork, which VerifyAudit reports.
func TestAuditAcceptedBreakIsNotReplayed(t *testing.T) {
	s, dir, _ := auditFixture(t)
	events := rawEvents(t, s)
	// Position 3 is login record 1: rewrite it, so that login record 2 (position 6) is a break.
	data := replaced(t, events[2].Data, `"login":"0"`, `"login":"X"`)
	s = closeAndRewrite(t, s, dir, rewriteRecords(map[uint64][]byte{3: data}, nil, nil))
	s = closeAcceptReopen(t, s, dir, audit.LoginLog, 2)
	if err := s.VerifyAudit(t.Context(), nil); err != nil {
		t.Fatalf("VerifyAudit() after the acceptance = %v", err)
	}
	rec2 := events[5].Data
	if l, n, _, _ := dbwire.ReadRecordID(rec2); l != "login" || n != 2 {
		t.Fatalf("position 6 is %s %d", l, n)
	}
	replaceHead(t, s, audit.LoginLog, 2, sha256.Sum256(rec2), rec2)
	// The store goes on from the replayed record: a new record 3 with the hash of record 2.
	update(t, s, func(tx *Tx) error { return tx.Record(loginRecord(audit.LoginFailure, "forked")) })
	err := s.VerifyAudit(t.Context(), nil)
	if err == nil || !strings.Contains(err.Error(), "record 2 (event ") || !strings.Contains(err.Error(), "the number does not follow that of record 3") {
		t.Errorf("VerifyAudit() with the records 1, 2, 3, 2, 3 of the login history = %v", err)
	}
}

// TestAuditAcceptedBreakDoesNotExcuseLostPrefix checks that an accepted break does not excuse a record that is the first of
// its log that is read, as the records before it are lost: the activity log, which is never deleted, still starts at record
// 1, and the heads that were kept outside the store of the lost records do not become a note.
func TestAuditAcceptedBreakDoesNotExcuseLostPrefix(t *testing.T) {
	s, dir, heads := auditFixture(t)
	events := rawEvents(t, s)
	// Position 4 is activity record 3: rewrite it, so that activity record 4 (position 5) is a break.
	data := replaced(t, events[3].Data, `"event":"1"`, `"event":"X"`)
	s = closeAndRewrite(t, s, dir, rewriteRecords(map[uint64][]byte{4: data}, nil, nil))
	s = closeAcceptReopen(t, s, dir, audit.ActivityLog, 4)
	if err := s.VerifyAudit(t.Context(), heads); err != nil {
		t.Fatalf("VerifyAudit() after the acceptance = %v", err)
	}
	// The head of activity record 2, kept outside the store before.
	sum2 := sha256.Sum256(events[1].Data)
	anchors := append(slices.Clone(heads), audit.Head{Log: audit.ActivityLog, N: 2, Hash: sum2})
	if err := s.VerifyAudit(t.Context(), anchors); err != nil {
		t.Fatalf("VerifyAudit() with the anchor of activity 2 = %v", err)
	}
	// Activity records 1 to 3 (positions 1, 2, 4) are lost.
	s = closeAndRewrite(t, s, dir, rewriteRecords(nil, []uint64{1, 2, 4}, nil))
	var notes []string
	err := s.VerifyAuditNotes(t.Context(), anchors, func(n string) { notes = append(notes, n) })
	if err == nil || !strings.Contains(err.Error(), "the first record of a log is number 1") {
		t.Errorf("VerifyAudit() after the loss of activity 1 to 3 = %v, notes %q", err, notes)
	}
}

// TestAuditCutAtAcceptedBreak checks that a deletion whose cut would land at a record that the walk took through an accepted
// break keeps the record before it: a break at the first record that remains is not honored, and the store would fail
// VerifyAudit, with nothing that can repair it.
func TestAuditCutAtAcceptedBreak(t *testing.T) {
	// Login records 1 and 2 are old, 3 (the break) and 4 are young.
	s, dir, _ := agedStore(t, []int{40, 39, 5, 4, 0}, []bool{true, true, true, true, false})
	e := loginEventOf(t, s, 2)
	data := changedRecord(t, e.Data, func(r *audit.Record) { r.Login = "forged" })
	s = closeAndRewrite(t, s, dir, rewriteEvents(map[uint64][]byte{e.Number: data}, nil, nil))
	if err := s.VerifyAudit(t.Context(), nil); err == nil {
		t.Fatal("VerifyAudit() passes before the acceptance: there is no break")
	}
	s = closeAcceptReopen(t, s, dir, audit.LoginLog, 3)
	checkVerify(t, s, dir, nil, nil, "", breakNote("login", 3))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Only the record 1 goes: the record 2 is the record before the break, and the chain starts at it.
	if d, err := compactLoginOf(t, dir, chainTime, nil); err != nil || d.First != 1 || d.Last != 1 || d.Stopped != "" {
		t.Fatalf("CompactLogin() = %+v, %v, want the record 1", d, err)
	}
	s = reopenLog(t, dir, logdb.Options{})
	checkVerify(t, s, dir, nil, nil, "", startNote(2), breakNote("login", 3))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if d, err := compactLoginOf(t, dir, chainTime, nil); err != nil || d.First != 0 || d.Stopped != "" {
		t.Errorf("the second CompactLogin() = %+v, %v, want nothing", d, err)
	}
}
