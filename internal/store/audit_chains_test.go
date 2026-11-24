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
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// The tests of the table chains write its rows and the events of the daemon for a deletion and an acceptance straight
// into the engine of the daemon of the test, past the rules of the daemon, as the maintenance of the daemon writes them,
// and compact away the login records that a deletion drops as it does: with a Rewrite.Event that drops them.

var chainTime = time.Date(2026, 11, 20, 10, 20, 30, 0, time.UTC)

type chainRow struct{ key, value []byte }

func startRow(first uint64, prev [sha256.Size]byte) chainRow {
	return chainRow{dbwire.ChainStartKey(dbwire.LogLogin),
		dbwire.AppendChainStart(nil, dbwire.ChainStart{First: first, Prev: prev, At: chainTime})}
}

func breakRow(log string, n uint64, hash [sha256.Size]byte) chainRow {
	return chainRow{dbwire.ChainBreakKey(log, n), dbwire.AppendChainBreak(nil, dbwire.ChainBreak{Hash: hash, At: chainTime})}
}

// deletedEvents returns the events that the deletion of the login records before first adds to the log.
func deletedEvents(first uint64, prev [sha256.Size]byte) [][]byte {
	return [][]byte{
		dbwire.AppendDeleted(nil, dbwire.Deleted{Log: dbwire.LogLogin, First: first, Prev: prev, Cutoff: chainTime.AddDate(-1, 0, 0)}),
		dbwire.AppendMark(nil, chainTime),
	}
}

// acceptedEvents returns the events that the acceptance of the break at record n of log adds to the log.
func acceptedEvents(log string, n uint64, hash [sha256.Size]byte) [][]byte {
	return [][]byte{dbwire.AppendAccepted(nil, dbwire.Accepted{Log: log, N: n, Hash: hash}), dbwire.AppendMark(nil, chainTime)}
}

// injectChains puts the rows and appends the events in one commit of the engine of s.
func injectChains(t *testing.T, s *Store, rows []chainRow, events ...[]byte) {
	t.Helper()
	rawUpdate(t, s, func(tx *logdb.Tx) error {
		for _, r := range rows {
			if err := tx.Put(tableChains, r.key, r.value); err != nil {
				return err
			}
		}
		for _, e := range events {
			if err := tx.AppendEvent(e); err != nil {
				return err
			}
		}
		return nil
	})
}

// dropLogin is the Rewrite of the compaction of a deletion: it drops the records of the login history before first.
func dropLogin(first uint64) logdb.Rewrite {
	return logdb.Rewrite{Event: func(e logdb.Event) ([]byte, bool) {
		if log, n, _, ok := dbwire.ReadRecordID(e.Data); ok && log == dbwire.LogLogin && n < first {
			return nil, false
		}
		return e.Data, true
	}}
}

// recordSums returns the SHA-256 of the bytes of each record of events, by its log and number.
func recordSums(t *testing.T, events []logdb.Event) map[anchorKey][sha256.Size]byte {
	t.Helper()
	sums := map[anchorKey][sha256.Size]byte{}
	for _, e := range events {
		log, n, _, ok := dbwire.ReadRecordID(e.Data)
		if !ok {
			t.Fatalf("the event %d is no record", e.Number)
		}
		sums[anchorKey{audit.Log(log), n}] = sha256.Sum256(e.Data)
	}
	return sums
}

// chainFixture is auditFixture with what the tests of the chains need: the events of its records, as they are before
// the change, and the hash of each record. By the positions that rewriteRecords counts, its events are 1 store.create
// (activity 1), 2 event 0 (activity 2), 3 a failure (login 1), 4 event 1 (activity 3), 5 event 2 (activity 4), 6 a failure
// (login 2), 7 event 3 (activity 5), 8 event 4 (activity 6) and 9 a failure (login 3).
type chainFixture struct {
	s      *Store
	dir    string
	events []logdb.Event
	sums   map[anchorKey][sha256.Size]byte
	heads  []audit.Head
}

func newChainFixture(t *testing.T) chainFixture {
	t.Helper()
	s, dir, heads := auditFixture(t)
	events := rawEvents(t, s)
	return chainFixture{s, dir, events, recordSums(t, events), heads}
}

func (f chainFixture) login(n uint64) [sha256.Size]byte { return f.sums[anchorKey{audit.LoginLog, n}] }
func (f chainFixture) activity(n uint64) [sha256.Size]byte {
	return f.sums[anchorKey{audit.ActivityLog, n}]
}

// head returns the head of the record n of log, as the export wrote it.
func (f chainFixture) head(log audit.Log, n uint64) audit.Head {
	return audit.Head{Log: log, N: n, Hash: f.sums[anchorKey{log, n}]}
}

// wrong is a hash that no record has.
var wrong = sha256.Sum256([]byte("another record"))

func startNote(first uint64) string {
	return fmt.Sprintf(`audit log "login": the records before record %d were deleted at 2026-11-20T10:20:30Z; the log is checked `+
		`from record %d`, first, first)
}

func breakNote(log string, n uint64) string {
	return fmt.Sprintf(`audit log %q: the break at record %d was accepted at 2026-11-20T10:20:30Z; the log is checked again `+
		`from that record`, log, n)
}

func leftNote(left string, first uint64) string {
	return fmt.Sprintf(`audit log "login": %s still in the store after the deletion of the records before record %d; the next `+
		`whiteroom-db compact removes them`, left, first)
}

func uncheckedNote(n int) string {
	if n == 1 {
		return "1 of the heads kept outside the store names a record that the store no longer has, so it is not checked"
	}
	return fmt.Sprintf("%d of the heads kept outside the store name records that the store no longer has, so they are not "+
		"checked", n)
}

// checkVerify checks that VerifyAuditNotes of the writer s and of a reader of the data directory dir fails with want, which
// wraps audit.ErrBroken, or does not fail if want is "", and tells the notes. The numbers of the events in want are the
// positions in events, as renumber has them. VerifyAudit must fail as it does.
func checkVerify(t *testing.T, s *Store, dir string, events []logdb.Event, anchors []audit.Head, want string, notes ...string) {
	t.Helper()
	want = renumber(events, want)
	// The reader goes at the end: a later rewrite of the engine needs the daemon to be gone.
	reader, err := openLogReader(dir, options{})
	if err != nil {
		t.Fatalf("openLogReader() = %v", err)
	}
	defer func() { _ = reader.Close() }()
	for who, st := range map[string]*Store{"the writer": s, "a reader": reader} {
		var got []string
		err := st.VerifyAuditNotes(t.Context(), anchors, func(text string) { got = append(got, text) })
		switch {
		case want == "" && err != nil:
			t.Errorf("VerifyAuditNotes(%d anchors) of %s = %v, want nil", len(anchors), who, err)
		case want != "" && (err == nil || err.Error() != want || !errors.Is(err, audit.ErrBroken)):
			t.Errorf("VerifyAuditNotes(%d anchors) of %s = %v, want %q that wraps audit.ErrBroken", len(anchors), who, err, want)
		}
		if !slices.Equal(got, notes) {
			t.Errorf("the notes of VerifyAuditNotes(%d anchors) of %s are:\n%s\nwant:\n%s", len(anchors), who,
				strings.Join(got, "\n"), strings.Join(notes, "\n"))
		}
		again := st.VerifyAudit(t.Context(), anchors)
		if (err == nil) != (again == nil) || err != nil && again != nil && err.Error() != again.Error() {
			t.Errorf("VerifyAudit(%d anchors) of %s = %v, but VerifyAuditNotes = %v", len(anchors), who, again, err)
		}
	}
}

// changedRecord returns the data of a record that is that of data, with change made to it.
func changedRecord(t *testing.T, data []byte, change func(r *audit.Record)) []byte {
	t.Helper()
	r, err := audit.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	change(&r)
	out, err := audit.Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// checkNoProblem checks that Check finds no problem in s.
func checkNoProblem(t *testing.T, s *Store) {
	t.Helper()
	if err := s.Check(t.Context(), func(p Problem) bool {
		t.Errorf("Check() found %s", p)
		return true
	}); err != nil {
		t.Errorf("Check() = %v", err)
	}
}

func TestVerifyAuditChainStart(t *testing.T) {
	type change struct {
		rows    []chainRow
		events  [][]byte
		rewrite *logdb.Rewrite // the compaction after the commit of the rows and the events; nil for none
		dropped uint64         // how many login records the compaction drops
	}
	rw := func(r logdb.Rewrite) *logdb.Rewrite { return &r }
	only := func(first uint64) func(chainFixture) []string {
		return func(chainFixture) []string { return []string{startNote(first)} }
	}
	for _, tt := range []struct {
		name   string
		change func(t *testing.T, f chainFixture) change
		want   string
		notes  func(f chainFixture) []string
	}{
		{"the first login record deleted", func(t *testing.T, f chainFixture) change {
			return change{[]chainRow{startRow(2, f.login(1))}, deletedEvents(2, f.login(1)), rw(dropLogin(2)), 1}
		}, "", only(2)},
		{"two login records deleted", func(t *testing.T, f chainFixture) change {
			return change{[]chainRow{startRow(3, f.login(2))}, deletedEvents(3, f.login(2)), rw(dropLogin(3)), 2}
		}, "", only(3)},
		{"every login record deleted", func(t *testing.T, f chainFixture) change {
			return change{[]chainRow{startRow(4, f.login(3))}, deletedEvents(4, f.login(3)), rw(dropLogin(4)), 3}
		}, "", only(4)},
		{"the deletion stopped before its compaction", func(t *testing.T, f chainFixture) change {
			return change{[]chainRow{startRow(3, f.login(2))}, deletedEvents(3, f.login(2)), nil, 0}
		}, "", func(chainFixture) []string { return []string{startNote(3), leftNote("the records 1 to 2 are", 3)} }},
		{"the deletion of the one record stopped before its compaction", func(t *testing.T, f chainFixture) change {
			return change{[]chainRow{startRow(2, f.login(1))}, deletedEvents(2, f.login(1)), nil, 0}
		}, "", func(chainFixture) []string { return []string{startNote(2), leftNote("record 1 is", 2)} }},
		{"the deletion of every record stopped before its compaction", func(t *testing.T, f chainFixture) change {
			return change{[]chainRow{startRow(4, f.login(3))}, deletedEvents(4, f.login(3)), nil, 0}
		}, "", func(chainFixture) []string { return []string{startNote(4), leftNote("the records 1 to 3 are", 4)} }},

		{"the deletion of every record stopped before its compaction, with another hash", func(t *testing.T, f chainFixture) change {
			return change{[]chainRow{startRow(4, wrong)}, deletedEvents(4, wrong), nil, 0}
		}, `audit log "login": record 3 (event 9): its hash is not the hash that the chain start has of it`, only(4)},
		{"the start names a record that is not the first one left", func(t *testing.T, f chainFixture) change {
			return change{[]chainRow{startRow(2, f.login(1))}, deletedEvents(2, f.login(1)), rw(dropLogin(3)), 2}
		}, `audit log "login": record 3 (event 9): the chain starts at record 2, which is not there`, only(2)},
		{"the hash of the record before the first is not the one of the start", func(t *testing.T, f chainFixture) change {
			return change{[]chainRow{startRow(2, wrong)}, deletedEvents(2, wrong), rw(dropLogin(2)), 1}
		}, `audit log "login": record 2 (event 6): prev is not the hash that the chain start has of record 1`, only(2)},
		{"the hash of the start is not that of the record before it, which is still there", func(t *testing.T, f chainFixture) change {
			return change{[]chainRow{startRow(2, wrong)}, deletedEvents(2, wrong), nil, 0}
		}, `audit log "login": record 2 (event 6): prev is not the hash that the chain start has of record 1`, only(2)},
		{"the start has the hash of the wrong record", func(t *testing.T, f chainFixture) change {
			return change{[]chainRow{startRow(3, f.login(1))}, deletedEvents(3, f.login(1)), rw(dropLogin(3)), 2}
		}, `audit log "login": record 3 (event 9): prev is not the hash that the chain start has of record 2`, only(3)},
		{"a start beyond the head, with the records still there", func(t *testing.T, f chainFixture) change {
			return change{[]chainRow{startRow(5, f.login(3))}, deletedEvents(5, f.login(3)), nil, 0}
		}, `audit log "login": the chain starts at record 5, but the log ends with record 3 (event 9)`, only(5)},
		{"a start beyond the head, with no record left", func(t *testing.T, f chainFixture) change {
			return change{[]chainRow{startRow(5, f.login(3))}, deletedEvents(5, f.login(3)), rw(dropLogin(5)), 3}
		}, `audit log "login": the chain starts at record 5, but the head in the table is record 3`, only(5)},
		{"a start that the head has reached, with no record left", func(t *testing.T, f chainFixture) change {
			return change{[]chainRow{startRow(3, f.login(2))}, deletedEvents(3, f.login(2)), rw(dropLogin(4)), 3}
		}, `audit log "login": the table has the head of record 3, but the log has no record`, only(3)},
		{"no record left, and the head has another hash", func(t *testing.T, f chainFixture) change {
			return change{[]chainRow{startRow(4, wrong)}, deletedEvents(4, wrong), rw(dropLogin(4)), 3}
		}, `audit log "login": the hash of the head in the table, record 3, is not the hash that the chain start has`, only(4)},
		{"no record left, and no head", func(t *testing.T, f chainFixture) change {
			return change{[]chainRow{startRow(4, f.login(3))}, deletedEvents(4, f.login(3)),
				rw(rewriteRecords(nil, []uint64{3, 6, 9}, map[audit.Log]*audit.Head{audit.LoginLog: nil})), 3}
		}, `audit log "login": the chain starts at record 4, but the table has no head`, only(4)},
		{"a record is missing after the start", func(t *testing.T, f chainFixture) change {
			return change{[]chainRow{startRow(2, f.login(1))}, deletedEvents(2, f.login(1)),
				rw(rewriteRecords(nil, []uint64{3, 9}, nil)), 2}
		}, `audit log "login": the head in the table is record 3, but the last record is record 2 (event 6)`, only(2)},
		{"a record is missing before the start, which is still there", func(t *testing.T, f chainFixture) change {
			return change{[]chainRow{startRow(4, f.login(3))}, deletedEvents(4, f.login(3)),
				rw(rewriteRecords(nil, []uint64{6}, nil)), 1}
		}, `audit log "login": record 3 (event 9): the number does not follow that of record 1`, only(4)},
		{"a start for the activity log", func(t *testing.T, f chainFixture) change {
			row := chainRow{dbwire.ChainStartKey(dbwire.LogActivity),
				dbwire.AppendChainStart(nil, dbwire.ChainStart{First: 2, Prev: f.activity(1), At: chainTime})}
			return change{[]chainRow{row}, nil, nil, 0}
		}, `audit log "activity": the table chains has a chain start for it, but only the oldest records of the login history ` +
			`are ever deleted`, func(chainFixture) []string { return nil }},
		{"a start that cannot be read", func(t *testing.T, f chainFixture) change {
			return change{[]chainRow{{dbwire.ChainStartKey(dbwire.LogLogin), []byte("abc")}}, nil, nil, 0}
		}, `audit log "login": the chain start in the table chains cannot be read`, func(chainFixture) []string { return nil }},
		{"a start of the record 1", func(t *testing.T, f chainFixture) change {
			return change{[]chainRow{startRow(1, f.login(1))}, nil, nil, 0}
		}, `audit log "login": the chain start in the table chains cannot be read`, func(chainFixture) []string { return nil }},
		{"a start that has the value of a break", func(t *testing.T, f chainFixture) change {
			value := dbwire.AppendChainBreak(nil, dbwire.ChainBreak{Hash: f.login(1), At: chainTime})
			return change{[]chainRow{{dbwire.ChainStartKey(dbwire.LogLogin), value}}, nil, nil, 0}
		}, `audit log "login": the chain start in the table chains cannot be read`, func(chainFixture) []string { return nil }},
		{"the first record left has a prev that it cannot have", func(t *testing.T, f chainFixture) change {
			data := changedRecord(t, f.events[2].Data, func(r *audit.Record) { r.Prev = hashText(wrong[:]) })
			return change{[]chainRow{startRow(2, sha256.Sum256(data))}, nil, rw(rewriteRecords(map[uint64][]byte{3: data}, nil, nil)), 0}
		}, `audit log "login": record 1 (event 3): the first record has the hash of one before it`, only(2)},
		{"a record left before the start has no prev", func(t *testing.T, f chainFixture) change {
			data := changedRecord(t, f.events[5].Data, func(r *audit.Record) { r.Prev = "" })
			return change{[]chainRow{startRow(3, sha256.Sum256(data))}, nil,
				rw(rewriteRecords(map[uint64][]byte{6: data}, []uint64{3}, nil)), 1}
		}, `audit log "login": record 2 (event 6): the record has no hash of the one before it, which only the first record has not`,
			only(3)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newChainFixture(t)
			c := tt.change(t, f)
			injectChains(t, f.s, c.rows, c.events...)
			s := f.s
			if c.rewrite != nil {
				s = closeAndRewrite(t, s, f.dir, *c.rewrite)
			}
			checkVerify(t, s, f.dir, f.events, nil, tt.want, tt.notes(f)...)
			if tt.want == "" {
				// ReadAudit skips the events of the daemon and reads the records that are there, those of the activity log
				// all; Check has nothing to say.
				if got, want := len(allRecords(t, s)), 9-int(c.dropped); got != want {
					t.Errorf("ReadAudit() reads %d records, want %d", got, want)
				}
				checkNoProblem(t, s)
				checkNoProblem(t, openReaderOf(t, f.dir))
			}
		})
	}
}

// TestVerifyAuditSecondDeletion checks a deletion after another, and the records after a deletion: the first record that
// remains is not the first of the log, and a deletion that stopped before its compaction leaves the records that the
// first one has left.
func TestVerifyAuditSecondDeletion(t *testing.T) {
	f := newChainFixture(t)
	injectChains(t, f.s, []chainRow{startRow(2, f.login(1))}, deletedEvents(2, f.login(1))...)
	s := closeAndRewrite(t, f.s, f.dir, dropLogin(2))
	checkVerify(t, s, f.dir, f.events, nil, "", startNote(2))

	// The second has set its start, and stopped before its compaction: record 2 is the first, and before the start.
	injectChains(t, s, []chainRow{startRow(3, f.login(2))}, deletedEvents(3, f.login(2))...)
	checkVerify(t, s, f.dir, f.events, nil, "", startNote(3), leftNote("record 2 is", 3))

	// Its compaction.
	s = closeAndRewrite(t, s, f.dir, dropLogin(3))
	checkVerify(t, s, f.dir, f.events, nil, "", startNote(3))
	checkNoProblem(t, s)

	// A record after it has the hash of the record 3 as its prev, as always.
	update(t, s, func(tx *Tx) error { return tx.Record(loginRecord(audit.Login, "later")) })
	checkVerify(t, s, f.dir, f.events, nil, "", startNote(3))
	if got := recordLines(allRecords(t, s)); !slices.Contains(got, "login 4 login ") || slices.Contains(got, "login 2 login ") {
		t.Errorf("the records are %q, want login 4 and not login 2", got)
	}

	// Every login record deleted, and then a record that follows: the start names the one that is not there yet.
	events := rawEvents(t, s)
	sum4 := recordSums(t, events)[anchorKey{audit.LoginLog, 4}]
	injectChains(t, s, []chainRow{startRow(5, sum4)}, deletedEvents(5, sum4)...)
	s = closeAndRewrite(t, s, f.dir, dropLogin(5))
	checkVerify(t, s, f.dir, f.events, nil, "", startNote(5))
	update(t, s, func(tx *Tx) error { return tx.Record(loginRecord(audit.Login, "after all")) })
	checkVerify(t, s, f.dir, f.events, nil, "", startNote(5))
	if got := recordLines(allRecords(t, s)); !slices.Contains(got, "login 5 login ") {
		t.Errorf("the records are %q, want login 5", got)
	}
}

func TestVerifyAuditChainStartAnchors(t *testing.T) {
	for _, tt := range []struct {
		name    string
		rewrite bool // the compaction was done
		first   uint64
		anchors func(f chainFixture) []audit.Head
		want    string
		notes   []string // after the notes of the start, and of the records that the deletion has left
	}{
		{"the heads that were exported before the deletion", true, 3, func(f chainFixture) []audit.Head { return f.heads }, "", nil},
		{"the head of the record before the first", true, 3, func(f chainFixture) []audit.Head {
			return []audit.Head{f.head(audit.LoginLog, 2)}
		}, "", nil},
		{"the head of the record before the first, with another hash", true, 3, func(f chainFixture) []audit.Head {
			return []audit.Head{{Log: audit.LoginLog, N: 2, Hash: wrong}}
		}, `audit log "login": the head kept outside the store is record 2, whose hash is not the hash that the chain start has`, nil},
		{"the head of a record that is deleted", true, 3, func(f chainFixture) []audit.Head {
			return []audit.Head{f.head(audit.LoginLog, 1)}
		}, "", []string{uncheckedNote(1)}},
		{"the head of a record that is deleted, with another hash", true, 3, func(f chainFixture) []audit.Head {
			return []audit.Head{{Log: audit.LoginLog, N: 1, Hash: wrong}}
		}, "", []string{uncheckedNote(1)}},
		{"heads of records that are deleted, and others", true, 3, func(f chainFixture) []audit.Head {
			return []audit.Head{f.head(audit.LoginLog, 1), f.head(audit.LoginLog, 1), f.head(audit.LoginLog, 2),
				f.head(audit.LoginLog, 3), f.head(audit.ActivityLog, 1)}
		}, "", []string{uncheckedNote(2)}},
		{"the head of a record that is there, with another hash", true, 3, func(f chainFixture) []audit.Head {
			return []audit.Head{{Log: audit.LoginLog, N: 3, Hash: wrong}}
		}, `audit log "login": record 3 (event 9): its hash is not that of the head kept outside the store`, nil},
		{"the head past the end", true, 3, func(f chainFixture) []audit.Head {
			return []audit.Head{{Log: audit.LoginLog, N: 4, Hash: wrong}}
		}, `audit log "login": the head kept outside the store is record 4, but the log ends with record 3 (event 9)`, nil},
		{"a head of the activity log, which is not deleted", true, 3, func(f chainFixture) []audit.Head {
			return []audit.Head{{Log: audit.ActivityLog, N: 2, Hash: wrong}}
		}, `audit log "activity": record 2 (event 2): its hash is not that of the head kept outside the store`, nil},
		{"the heads before the deletion, with the records still there", false, 3, func(f chainFixture) []audit.Head {
			return []audit.Head{f.head(audit.LoginLog, 1), f.head(audit.LoginLog, 2), f.head(audit.LoginLog, 3)}
		}, "", nil},
		{"a head of a record that is still there, with another hash", false, 3, func(f chainFixture) []audit.Head {
			return []audit.Head{{Log: audit.LoginLog, N: 1, Hash: wrong}}
		}, `audit log "login": record 1 (event 3): its hash is not that of the head kept outside the store`, nil},
		{"every record deleted: the head of the last", true, 4, func(f chainFixture) []audit.Head {
			return []audit.Head{f.head(audit.LoginLog, 3)}
		}, "", nil},
		{"every record deleted: the head of the last, with another hash", true, 4, func(f chainFixture) []audit.Head {
			return []audit.Head{{Log: audit.LoginLog, N: 3, Hash: wrong}}
		}, `audit log "login": the head kept outside the store is record 3, whose hash is not the hash that the chain start has`, nil},
		{"every record deleted: the head of an older one", true, 4, func(f chainFixture) []audit.Head {
			return []audit.Head{f.head(audit.LoginLog, 2)}
		}, "", []string{uncheckedNote(1)}},
		{"every record deleted: a head past the start", true, 4, func(f chainFixture) []audit.Head {
			return []audit.Head{{Log: audit.LoginLog, N: 4, Hash: wrong}}
		}, `audit log "login": the head kept outside the store is record 4, but the log has no record`, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newChainFixture(t)
			prev := f.login(tt.first - 1)
			injectChains(t, f.s, []chainRow{startRow(tt.first, prev)}, deletedEvents(tt.first, prev)...)
			s := f.s
			notes := []string{startNote(tt.first)}
			if tt.rewrite {
				s = closeAndRewrite(t, s, f.dir, dropLogin(tt.first))
			} else if tt.want == "" {
				notes = append(notes, leftNote(fmt.Sprintf("the records 1 to %d are", tt.first-1), tt.first))
			}
			if tt.want == "" {
				notes = append(notes, tt.notes...)
			}
			checkVerify(t, s, f.dir, f.events, tt.anchors(f), tt.want, notes...)
		})
	}
}

// TestVerifyAuditBreaks checks that the break that is accepted for a record, with its hash, lets VerifyAudit go on
// from it.
func TestVerifyAuditBreaks(t *testing.T) {
	type change struct {
		replace map[uint64][]byte // the records that are rewritten, by position
		drop    []uint64
		rows    []chainRow
		events  [][]byte
	}
	rewrite := func(c change) logdb.Rewrite { return rewriteRecords(c.replace, c.drop, nil) }
	// Login record 2 is made another: record 3 does not have its hash as prev.
	login2 := func(t *testing.T, f chainFixture) map[uint64][]byte {
		return map[uint64][]byte{6: changedRecord(t, f.events[5].Data, func(r *audit.Record) { r.Login = "forged" })}
	}
	// Activity record 3 is made another: record 4 does not have its hash.
	activity3 := func(t *testing.T, f chainFixture) map[uint64][]byte {
		return map[uint64][]byte{4: changedRecord(t, f.events[3].Data, func(r *audit.Record) { r.Detail = map[string]string{"event": "X"} })}
	}
	const login3 = `audit log "login": record 3 (event 9): prev is not the hash of record 2`
	const activity4 = `audit log "activity": record 4 (event 5): prev is not the hash of record 3`
	for _, tt := range []struct {
		name   string
		change func(t *testing.T, f chainFixture) change
		want   string
		notes  []string
	}{
		{"a break that is not accepted", func(t *testing.T, f chainFixture) change {
			return change{replace: login2(t, f)}
		}, login3, nil},
		{"a break that is accepted", func(t *testing.T, f chainFixture) change {
			return change{replace: login2(t, f), rows: []chainRow{breakRow(dbwire.LogLogin, 3, f.login(3))},
				events: acceptedEvents(dbwire.LogLogin, 3, f.login(3))}
		}, "", []string{breakNote("login", 3)}},
		{"a break of the activity log that is accepted", func(t *testing.T, f chainFixture) change {
			return change{replace: activity3(t, f), rows: []chainRow{breakRow(dbwire.LogActivity, 4, f.activity(4))},
				events: acceptedEvents(dbwire.LogActivity, 4, f.activity(4))}
		}, "", []string{breakNote("activity", 4)}},
		{"a break of the activity log that is not accepted", func(t *testing.T, f chainFixture) change {
			return change{replace: activity3(t, f), rows: []chainRow{breakRow(dbwire.LogLogin, 4, f.activity(4))}}
		}, activity4, nil},
		{"a break with the hash of another record", func(t *testing.T, f chainFixture) change {
			return change{replace: login2(t, f), rows: []chainRow{breakRow(dbwire.LogLogin, 3, f.login(2))}}
		}, login3, nil},
		{"a break with a hash that no record has", func(t *testing.T, f chainFixture) change {
			return change{replace: login2(t, f), rows: []chainRow{breakRow(dbwire.LogLogin, 3, wrong)}}
		}, login3, nil},
		{"a break of the other log", func(t *testing.T, f chainFixture) change {
			return change{replace: login2(t, f), rows: []chainRow{breakRow(dbwire.LogActivity, 3, f.login(3))}}
		}, login3, nil},
		{"a break of the record before", func(t *testing.T, f chainFixture) change {
			return change{replace: login2(t, f), rows: []chainRow{breakRow(dbwire.LogLogin, 2, sha256.Sum256(login2(t, f)[6]))}}
		}, login3, []string{breakNote("login", 2)}},
		{"the accepted record is rewritten", func(t *testing.T, f chainFixture) change {
			replace := login2(t, f)
			replace[9] = changedRecord(t, f.events[8].Data, func(r *audit.Record) { r.Login = "forged too" })
			return change{replace: replace, rows: []chainRow{breakRow(dbwire.LogLogin, 3, f.login(3))}}
		}, login3, nil},
		{"another record is rewritten after the acceptance", func(t *testing.T, f chainFixture) change {
			replace := login2(t, f)
			replace[5] = changedRecord(t, f.events[4].Data, func(r *audit.Record) { r.Detail = map[string]string{"event": "X"} })
			return change{replace: replace, rows: []chainRow{breakRow(dbwire.LogLogin, 3, f.login(3))}}
		}, `audit log "activity": record 5 (event 7): prev is not the hash of record 4`, nil},
		{"another break is not accepted", func(t *testing.T, f chainFixture) change {
			replace := login2(t, f)
			replace[7] = changedRecord(t, f.events[6].Data, func(r *audit.Record) { r.Detail = map[string]string{"event": "X"} })
			return change{replace: replace, rows: []chainRow{breakRow(dbwire.LogLogin, 3, f.login(3))}}
		}, `audit log "activity": record 6 (event 8): prev is not the hash of record 5`, nil},
		{"a break of a record that is not there", func(t *testing.T, f chainFixture) change {
			return change{rows: []chainRow{breakRow(dbwire.LogLogin, 99, wrong)}}
		}, "", nil},
		{"a break that the chain does not need", func(t *testing.T, f chainFixture) change {
			return change{rows: []chainRow{breakRow(dbwire.LogLogin, 2, f.login(2))}}
		}, "", []string{breakNote("login", 2)}},
		{"a record that is missing, and an acceptance", func(t *testing.T, f chainFixture) change {
			return change{drop: []uint64{6}, rows: []chainRow{breakRow(dbwire.LogLogin, 3, f.login(3))}}
		}, `audit log "login": record 3 (event 9): the number does not follow that of record 1`, nil},
		{"a record that is missing, and no acceptance", func(t *testing.T, f chainFixture) change {
			return change{drop: []uint64{6}}
		}, `audit log "login": record 3 (event 9): the number does not follow that of record 1`, nil},
		{"a break row that cannot be read", func(t *testing.T, f chainFixture) change {
			return change{rows: []chainRow{{dbwire.ChainBreakKey(dbwire.LogLogin, 3), []byte("abc")}}}
		}, `audit log "login": a row of the table chains for an accepted break cannot be read`, nil},
		{"a break row with a key that is not one", func(t *testing.T, f chainFixture) change {
			return change{rows: []chainRow{{[]byte("login\x00abc"), dbwire.AppendChainBreak(nil, dbwire.ChainBreak{Hash: wrong})}}}
		}, `audit log "login": a row of the table chains for an accepted break cannot be read`, nil},
		{"a break row with the key of a longer name", func(t *testing.T, f chainFixture) change {
			key := dbwire.ChainBreakKey("login\x00abc", 3)
			return change{rows: []chainRow{{key, dbwire.AppendChainBreak(nil, dbwire.ChainBreak{Hash: wrong})}}}
		}, `audit log "login": a row of the table chains for an accepted break cannot be read`, nil},
		{"a break of the record 0", func(t *testing.T, f chainFixture) change {
			return change{rows: []chainRow{breakRow(dbwire.LogLogin, 0, wrong)}}
		}, `audit log "login": a row of the table chains for an accepted break cannot be read`, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newChainFixture(t)
			c := tt.change(t, f)
			injectChains(t, f.s, c.rows, c.events...)
			s := f.s
			if c.replace != nil || c.drop != nil {
				s = closeAndRewrite(t, s, f.dir, rewrite(c))
			}
			// The heads were set when the records were written, and a rewrite of the records, which no head follows, shows in
			// the head of the log only if its last record is the one that is rewritten.
			checkVerify(t, s, f.dir, f.events, nil, tt.want, tt.notes...)
		})
	}
}

// TestVerifyAuditBreakBelowStart checks that a break of a record before the chain start is honored while the record is
// still there, and is no news, and does nothing when the record is gone.
func TestVerifyAuditBreakBelowStart(t *testing.T) {
	f := newChainFixture(t)
	data := changedRecord(t, f.events[5].Data, func(r *audit.Record) { r.Login = "forged" })
	s := closeAndRewrite(t, f.s, f.dir, rewriteRecords(map[uint64][]byte{6: data}, nil, nil))
	checkVerify(t, s, f.dir, f.events, nil, `audit log "login": record 3 (event 9): prev is not the hash of record 2`)
	injectChains(t, s, []chainRow{breakRow(dbwire.LogLogin, 3, f.login(3))}, acceptedEvents(dbwire.LogLogin, 3, f.login(3))...)
	checkVerify(t, s, f.dir, f.events, nil, "", breakNote("login", 3))

	// The records before the record 4 are to go: the break is of a record that is there, and is not told.
	injectChains(t, s, []chainRow{startRow(4, f.login(3))}, deletedEvents(4, f.login(3))...)
	checkVerify(t, s, f.dir, f.events, nil, "", startNote(4), leftNote("the records 1 to 3 are", 4))

	// They are gone: so is the use of the break.
	s = closeAndRewrite(t, s, f.dir, dropLogin(4))
	checkVerify(t, s, f.dir, f.events, nil, "", startNote(4))
	if rows := rawTable(t, s, tableChains); len(rows) != 2 {
		t.Errorf("the table chains has %d rows, want the start and the break", len(rows))
	}
}

// TestCheckEndsAnchors checks the anchors of the records that the read of a log has not met: one before the chain start is
// counted, as it cannot be checked, and one after it, or of a log that has no chain start, is a problem, since the read of a
// log that has a gap fails before it, and a break does not let a gap go.
func TestCheckEndsAnchors(t *testing.T) {
	last := sha256.Sum256([]byte("the last record"))
	heads := []audit.Head{{Log: audit.LoginLog, N: 3, Hash: last}, {Log: audit.ActivityLog, N: 1, Hash: last}}
	ends := func() map[audit.Log]*chainEnd {
		return map[audit.Log]*chainEnd{audit.LoginLog: {n: 3, event: 9, hash: last}, audit.ActivityLog: {n: 1, event: 1, hash: last}}
	}
	login2 := audit.Head{Log: audit.LoginLog, N: 2, Hash: wrong}
	met := map[anchorKey]bool{{audit.LoginLog, 2}: true}
	for _, tt := range []struct {
		name    string
		start   *dbwire.ChainStart
		matched map[anchorKey]bool
		want    string
		counted int
	}{
		{"a record that the read has met", nil, met, "", 0},
		{"a record that the read has not met", nil, nil,
			`audit log "login": the head kept outside the store is record 2, which the log does not have`, 0},
		{"a record before the chain start", &dbwire.ChainStart{First: 4, Prev: last}, nil, "", 1},
		{"a record before the chain start, which the read has met", &dbwire.ChainStart{First: 4, Prev: last}, met, "", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			chains := map[audit.Log]chainInfo{audit.LoginLog: {start: tt.start}}
			counted, err := checkEnds(ends(), chains, heads, []audit.Head{login2}, tt.matched)
			switch {
			case tt.want == "" && err != nil:
				t.Errorf("checkEnds() = %v", err)
			case tt.want != "" && (err == nil || err.Error() != tt.want || !errors.Is(err, audit.ErrBroken)):
				t.Errorf("checkEnds() = %v, want %q that wraps audit.ErrBroken", err, tt.want)
			case counted != tt.counted:
				t.Errorf("checkEnds() counts %d anchors that it cannot check, want %d", counted, tt.counted)
			}
		})
	}
}

// TestVerifyAuditNotesNil checks that a nil note is no problem, and that a store that has none of the new rows gives no
// note.
func TestVerifyAuditNotesNil(t *testing.T) {
	f := newChainFixture(t)
	injectChains(t, f.s, []chainRow{startRow(2, f.login(1))}, deletedEvents(2, f.login(1))...)
	if err := f.s.VerifyAuditNotes(t.Context(), nil, nil); err != nil {
		t.Errorf("VerifyAuditNotes() with no note = %v", err)
	}
	g := newChainFixture(t)
	checkVerify(t, g.s, g.dir, g.events, g.heads, "")
	checkVerify(t, g.s, g.dir, g.events, nil, "")
}

// TestAuditEventsOfTheDaemonAreSkipped checks that ReadAudit and VerifyAudit skip the events that the daemon adds for a
// deletion and an acceptance when they are valid, and find the ones that are not: an event of the daemon that is not one
// of the kinds, or whose fields cannot be read, is broken.
func TestAuditEventsOfTheDaemonAreSkipped(t *testing.T) {
	good := append(deletedEvents(2, wrong), acceptedEvents(dbwire.LogLogin, 2, wrong)...)
	f := newChainFixture(t)
	want := recordLines(allRecords(t, f.s))
	appendRawEvents(t, f.s, good...)
	for who, st := range map[string]*Store{"the writer": f.s, "a reader": openReaderOf(t, f.dir)} {
		if got := recordLines(allRecords(t, st)); !slices.Equal(got, want) {
			t.Errorf("the records read by %s = %q, want %q", who, got, want)
		}
	}
	checkVerify(t, f.s, f.dir, f.events, nil, "")

	deleted, accepted := good[0], good[2]
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{"a deletion cut short", deleted[:len(deleted)-1]},
		{"a deletion with a byte more", append(slices.Clone(deleted), 0)},
		{"a deletion of the log 3", append([]byte{0, 2, 3}, deleted[3:]...)},
		{"a deletion that deletes nothing", append(append([]byte{0, 2, 1}, make([]byte, 8)...), deleted[11:]...)},
		{"an acceptance cut short", accepted[:len(accepted)-1]},
		{"an acceptance of the record 0", append(append([]byte{0, 3, 1}, make([]byte, 8)...), accepted[11:]...)},
		{"an acceptance of the log 0", append([]byte{0, 3, 0}, accepted[3:]...)},
		{"the kind 4", []byte{0, 4, 1, 2, 3}},
		{"the byte of the daemon alone", []byte{0}},
		{"the kind of a mark and the size of a deletion", append([]byte{0, 1}, deleted[2:]...)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newChainFixture(t)
			appendRawEvents(t, f.s, tt.data)
			number := nextEvent(t, f.s) - 1
			want := fmt.Sprintf("audit event %d: not a record of the audit log", number)
			for who, st := range map[string]*Store{"the writer": f.s, "a reader": openReaderOf(t, f.dir)} {
				if err := st.VerifyAudit(t.Context(), nil); err == nil || err.Error() != want || !errors.Is(err, audit.ErrBroken) {
					t.Errorf("VerifyAudit() of %s = %v, want %q that wraps audit.ErrBroken", who, err, want)
				}
				err := st.ReadAudit(t.Context(), audit.Filter{}, func(audit.Record) bool { return true })
				if err == nil || err.Error() != want || !errors.Is(err, audit.ErrBroken) {
					t.Errorf("ReadAudit() of %s = %v, want %q that wraps audit.ErrBroken", who, err, want)
				}
			}
		})
	}
}

// TestAuditReadRecordIDAgrees checks that dbwire.ReadRecordID reads the log, the number and the prev that audit.Decode reads, of
// each record that the store has written, and that it cannot read what the marks and the other events of the daemon are.
func TestAuditReadRecordIDAgrees(t *testing.T) {
	f := newChainFixture(t)
	update(t, f.s, func(tx *Tx) error {
		return errors.Join(tx.Record(loginRecord(audit.Login, "x")), tx.Record(eventRecord("a")))
	})
	n := 0
	for _, e := range allEvents(t, f.s) {
		log, number, prev, ok := dbwire.ReadRecordID(e.Data)
		if dbwire.IsDaemonEvent(e.Data) {
			if ok {
				t.Errorf("ReadRecordID reads the event %d of the daemon as %q %d", e.Number, log, number)
			}
			continue
		}
		r, err := audit.Decode(e.Data)
		if err != nil {
			t.Fatal(err)
		}
		if !ok || log != string(r.Log) || number != r.N || prev != r.Prev {
			t.Errorf("ReadRecordID of the event %d = %q, %d, %q, %v; audit.Decode has %q, %d, %q", e.Number, log, number, prev, ok,
				r.Log, r.N, r.Prev)
		}
		n++
	}
	if n != 11 {
		t.Errorf("%d records were read, want 11", n)
	}
}
