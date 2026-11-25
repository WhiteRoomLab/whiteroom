package dbserver

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// auditStore is a store on a simulated file system whose commit i has the activity record i, the login record i/2 if i is
// even, a mark, and a row; its log has a segment for each commit, whose number is that of the commit.
type auditStore struct {
	*fixture
	act, login []*chainRec
	salt       string // what the rows of the commits end with
}

// chainRec is a record of an audit log with the hash that the next one names as its prev.
type chainRec struct {
	log  string
	n    uint64
	data []byte
	sum  [sha256.Size]byte
}

func (s *auditStore) next(log string) *chainRec {
	list := &s.act
	if log == dbwire.LogLogin {
		list = &s.login
	}
	prev := ""
	if n := len(*list); n > 0 {
		prev = hashText((*list)[n-1].sum)
	}
	r := &chainRec{log: log, n: uint64(len(*list) + 1)}
	r.data = auditRecord(log, r.n, prev)
	r.sum = sha256.Sum256(r.data)
	*list = append(*list, r)
	return r
}

func (r *chainRec) head() string {
	return fmt.Sprintf("audit head %s %d sha256:%x", r.log, r.n, r.sum)
}

// commit makes the next commit of the store.
func (s *auditStore) commit() { s.commitWith(nil) }

// commitWith makes the next commit of the store, and calls extra in it, if it is not nil.
func (s *auditStore) commitWith(extra func(tx *logdb.Tx)) {
	s.t.Helper()
	i := len(s.act) + 1
	recs := []*chainRec{s.next(dbwire.LogActivity)}
	if i%2 == 0 {
		recs = append(recs, s.next(dbwire.LogLogin))
	}
	s.update(func(tx *logdb.Tx) {
		for _, r := range recs {
			if err := tx.AppendEvent(r.data); err != nil {
				s.t.Error(err)
			}
			if err := tx.Put(dbwire.TableHeads, []byte(r.log), []byte(r.head())); err != nil {
				s.t.Error(err)
			}
		}
		if err := tx.AppendEvent(dbwire.AppendMark(nil, testBase)); err != nil {
			s.t.Error(err)
		}
		if err := tx.Put(2, []byte(fmt.Sprintf("row%d", i)), []byte(strings.Repeat("v", 20)+s.salt)); err != nil {
			s.t.Error(err)
		}
		if extra != nil {
			extra(tx)
		}
	})
}

// newAuditStore makes a store of commits commits, and closes its server, which writes the checkpoint of the last commit.
func newAuditStore(t *testing.T, commits int) *auditStore {
	t.Helper()
	f := newFixture(t, func(o *Options) { o.Log.SegmentBytes = 1; o.Log.CheckpointBytes = -1 })
	s := &auditStore{fixture: f}
	for range commits {
		s.commit()
	}
	if err := f.srv.Close(); err != nil {
		t.Fatal(err)
	}
	return s
}

func segName(i int) string { return fmt.Sprintf("logdb/log/0000000000000001-%016x.log", i) }

// flipAt flips a bit of the byte at off of the file name.
func flipAt(t testing.TB, fsys *simfs.FS, name string, off int64) {
	t.Helper()
	f, err := fsys.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err := f.ReadAt(b[:], off); err != nil {
		t.Fatal(err)
	}
	b[0] ^= 1
	if _, err := f.WriteAt(b[:], off); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// damage flips a byte in the body of the record of each commit of commits.
func (s *auditStore) damage(commits ...int) {
	s.t.Helper()
	for _, c := range commits {
		flipAt(s.t, s.fs, segName(c), 100)
	}
}

// keepOn is the keepMaker of the directory fsys, which is empty.
func keepOn(fsys *simfs.FS) keepMaker {
	return func() (*keepDir, error) { return &keepDir{fs: fsys, name: "keep"}, nil }
}

// repair runs Repair on the world of the store, with a copy in a new directory.
func (s *auditStore) repair(r RepairOptions) (RepairReport, *simfs.FS, error) {
	s.t.Helper()
	keep := simfs.New("keep")
	rep, err := repair(s.t.Context(), s.fs, "data", s.opts, r, keepOn(keep))
	return rep, keep, err
}

func (s *auditStore) apply(r RepairOptions) (RepairReport, *simfs.FS) {
	s.t.Helper()
	r.Apply, r.Keep = true, "keep"
	rep, keep, err := s.repair(r)
	if err != nil {
		s.t.Fatalf("Repair() = %v", err)
	}
	return rep, keep
}

// holeWords returns the holes of the report as "log first-last", with a "?" if no row records the hole.
func holeWords(rep RepairReport) []string {
	var out []string
	for _, h := range rep.Holes {
		out = append(out, fmt.Sprintf("%s %d-%d", h.Log, h.First, h.Last)+map[bool]string{true: "", false: "?"}[h.Row])
	}
	return out
}

func gapWords(rep RepairReport) []string {
	var out []string
	for _, g := range rep.Gaps {
		out = append(out, fmt.Sprintf("%s %d-%d", g.Log, g.First, g.Last))
	}
	return out
}

// auditView is what a store has of its audit logs: its events, the rows of its holes and the lines of its heads.
type auditView struct {
	events []logdb.Event
	holes  map[string]map[uint64]dbwire.ChainHole
	heads  map[string]string
	start  map[string]dbwire.ChainStart
}

func (f *fixture) auditView() auditView {
	f.t.Helper()
	a := auditView{holes: map[string]map[uint64]dbwire.ChainHole{}, heads: map[string]string{}, start: map[string]dbwire.ChainStart{}}
	if err := maintain(f.t.Context(), f.fs, "data", f.opts, func(m *logdb.Maintenance) error {
		if err := m.Events(f.t.Context(), func(e logdb.Event) bool {
			a.events = append(a.events, e)
			return true
		}); err != nil {
			return err
		}
		return m.View(f.t.Context(), func(tx *logdb.Tx) error {
			for _, log := range repairLogs {
				if line, ok := tx.Get(dbwire.TableHeads, []byte(log)); ok {
					a.heads[log] = string(line)
				}
				if raw, ok := tx.Get(dbwire.TableChains, dbwire.ChainStartKey(log)); ok {
					if s, valid := dbwire.ParseChainStart(raw); valid {
						a.start[log] = s
					}
				}
				tx.Scan(dbwire.TableChains, dbwire.ChainHolePrefix(log), func(k, val []byte) bool {
					_, first, _ := dbwire.ParseChainHoleKey(k)
					h, ok := dbwire.ParseChainHole(first, val)
					if !ok {
						f.t.Errorf("a row of a hole of %s cannot be read", log)
					}
					if a.holes[log] == nil {
						a.holes[log] = map[uint64]dbwire.ChainHole{}
					}
					a.holes[log][first] = h
					return true
				})
			}
			return nil
		})
	}); err != nil {
		f.t.Fatal(err)
	}
	return a
}

// checkChains checks, with the rule that VerifyAudit applies, that the records of each log follow each other or follow a hole
// that a row records, and that the head of the log is its last record or the end of a hole. It returns the holes that the
// walk passed.
func (a auditView) checkChains(t testing.TB) map[string][]string {
	t.Helper()
	passed := map[string][]string{}
	for _, log := range repairLogs {
		var last uint64
		var lastSum [sha256.Size]byte
		start := a.start[log]
		var startp *dbwire.ChainStart
		if _, ok := a.start[log]; ok {
			startp = &start
		}
		for _, e := range a.events {
			if dbwire.IsDaemonEvent(e.Data) {
				continue
			}
			l, n, prev, ok := dbwire.ReadRecordID(e.Data)
			if !ok || l != log {
				continue
			}
			expected := dbwire.ChainExpected(startp, last)
			switch {
			case last == 0 && startp != nil && n < startp.First:
			case n != expected:
				h, ok := dbwire.HoleBefore(a.holes[log], startp, last, n, prev)
				if !ok {
					t.Errorf("audit log %q: record %d (event %d) does not follow the end of the log, record %d, and no hole is before it", log, n, e.Number, last)
				} else {
					passed[log] = append(passed[log], fmt.Sprintf("%d-%d", h.First, h.Last))
				}
			case last != 0 && prev != hashText(lastSum):
				t.Errorf("audit log %q: record %d: prev is not the hash of record %d", log, n, last)
			}
			last, lastSum = n, sha256.Sum256(e.Data)
		}
		line, ok := a.heads[log]
		if !ok {
			continue
		}
		_, hn, hhash, valid := dbwire.ParseHeadLine([]byte(line))
		if !valid {
			t.Errorf("audit log %q: the head line %q cannot be read", log, line)
			continue
		}
		expected := dbwire.ChainExpected(startp, last)
		switch h, held := a.holes[log][expected]; {
		case hn == last && hhash == lastSum:
		case hn >= expected && held && h.Last == hn && h.Hash == hhash:
			passed[log] = append(passed[log], fmt.Sprintf("%d-%d", h.First, h.Last))
		default:
			t.Errorf("audit log %q: the head is record %d, the last record is %d, and no hole at %d ends with it", log, hn, last, expected)
		}
	}
	return passed
}

// shapeOf returns the events of the store in words, as the engine numbers them.
func shapeOf(a auditView) string {
	var words []string
	for _, e := range a.events {
		words = append(words, word(e.Data))
	}
	return strings.Join(words, " ")
}

// keptFiles returns the files of logdb/ of the world fsys, with what they hold.
func keptFiles(fsys *simfs.FS) map[string]string {
	m := map[string]string{}
	for name, data := range worldFiles(fsys) {
		if strings.HasPrefix(name, "logdb/") {
			m[name] = data
		}
	}
	return m
}

func TestRepairHoles(t *testing.T) {
	// The commit i has the activity record i, and the login record i/2 if i is even. The checkpoint covers all of them, so
	// that a record that cannot be read in the middle is a loss that the state does not share, and not a truncation.
	for _, tt := range []struct {
		name    string
		damaged []int
		want    []string
	}{
		{"a commit of both logs", []int{4}, []string{"login 2-2", "activity 4-4"}},
		{"a commit of the activity log", []int{3}, []string{"activity 3-3"}},
		{"two commits in a row", []int{3, 4}, []string{"login 2-2", "activity 3-4"}},
		{"the first commit", []int{1}, []string{"activity 1-1"}},
		{"the first commit of the login history", []int{2}, []string{"login 1-1", "activity 2-2"}},
		{"the last commit, which the checkpoint names", []int{8}, []string{"login 4-4", "activity 8-8"}},
		{"the last two commits", []int{7, 8}, []string{"login 4-4", "activity 7-8"}},
		{"two commits apart", []int{3, 6}, []string{"login 3-3", "activity 3-3", "activity 6-6"}},
		{"every record of the login history", []int{2, 4, 6, 8}, []string{"login 1-4", "activity 2-2", "activity 4-4", "activity 6-6", "activity 8-8"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newAuditStore(t, 8)
			s.damage(tt.damaged...)
			before := keptFiles(s.fs)

			// Without Apply it changes nothing, and tells the same holes.
			counted := s.fs.Counted()
			shown, _, err := s.repair(RepairOptions{})
			if err != nil {
				t.Fatalf("Repair() = %v", err)
			}
			if got := holeWords(shown); !slices.Equal(got, tt.want) {
				t.Errorf("the holes that it shows are %q, want %q", got, tt.want)
			}
			if n := s.fs.Counted(); n != counted || !maps.Equal(keptFiles(s.fs), before) {
				t.Errorf("a repair that is not applied made %d operations and changed the files", n-counted)
			}
			if shown.Applied || shown.Kept != "" || shown.Things() == 0 {
				t.Errorf("the report of the repair that is not applied is %+v", shown)
			}

			rep, keep := s.apply(RepairOptions{})
			if got := holeWords(rep); !slices.Equal(got, tt.want) {
				t.Errorf("the holes are %q, want %q", got, tt.want)
			}
			if len(rep.Gaps) != 0 || !rep.Applied || rep.Kept != "keep/logdb" || rep.SchemaFrom != 0 {
				t.Errorf("the report is %+v", rep)
			}
			if got := keptFiles(keep); !maps.Equal(got, before) {
				t.Errorf("the copy has %d files, want those of logdb/ as they were (%d)", len(got), len(before))
			}
			a := s.auditView()
			passed := a.checkChains(t)
			if len(a.holes[dbwire.LogLogin])+len(a.holes[dbwire.LogActivity]) != len(tt.want) {
				t.Errorf("the rows of the holes are %v, want %d of them", a.holes, len(tt.want))
			}
			for _, w := range tt.want {
				var log string
				var first, last uint64
				if _, err := fmt.Sscanf(w, "%s %d-%d", &log, &first, &last); err != nil {
					t.Fatal(err)
				}
				h, ok := a.holes[log][first]
				if !ok || h.Last != last {
					t.Errorf("no row for the hole %s", w)
					continue
				}
				list := s.act
				if log == dbwire.LogLogin {
					list = s.login
				}
				if h.Hash != list[last-1].sum || h.At.IsZero() {
					t.Errorf("the hole %s has the hash %x, want that of record %d", w, h.Hash[:4], last)
				}
				if got := passed[log]; !slices.Contains(got, fmt.Sprintf("%d-%d", first, last)) {
					t.Errorf("the walk of the log %q did not pass the hole %s: %v", log, w, got)
				}
			}
			// The events of the holes and one mark come after the kept events.
			shape := strings.Fields(shapeOf(a))
			if n := len(tt.want); len(shape) < n+1 || shape[len(shape)-1] != "M" {
				t.Errorf("the events end with %v, want the events of the %d holes and a mark", shape, n)
			} else if got := slices.Collect(func(yield func(string) bool) {
				for _, w := range shape[len(shape)-n-1 : len(shape)-1] {
					if !yield(w) {
						return
					}
				}
			}); len(got) != n || !allHoleWords(got) {
				t.Errorf("the %d words before the last mark are %v, want hole events", n, got)
			}

			// A second repair has nothing to do.
			again, _, err := s.repair(RepairOptions{})
			if err != nil || again.Things() != 0 || len(again.Holes) != 0 || len(again.Gaps) != 0 {
				t.Errorf("the second repair = %+v, %v, want nothing to repair", again, err)
			}
			// The store opens, and its writer goes on: a hole at the end of a log is then in its middle, and still passed.
			s.open()
			s.commit()
			s.commit()
			if err := s.srv.Close(); err != nil {
				t.Fatal(err)
			}
			s.auditView().checkChains(t)
		})
	}
}

func allHoleWords(words []string) bool {
	for _, w := range words {
		if !strings.HasPrefix(w, "H") {
			return false
		}
	}
	return true
}

// withoutCheckpoint removes the checkpoint of the store, so that the log is all that has its state.
func (s *auditStore) withoutCheckpoint() {
	s.t.Helper()
	if err := s.fs.Remove("logdb/checkpoint"); err != nil {
		s.t.Fatal(err)
	}
}

// reopen opens the server of the store, which a test closed.
func (s *auditStore) reopen() {
	s.t.Helper()
	s.open()
}

func TestRepairTruncation(t *testing.T) {
	// Without a checkpoint the log has the state: the commits after the commit 4, which cannot be read, can.
	s := newAuditStore(t, 8)
	s.withoutCheckpoint()
	s.damage(4)
	before := keptFiles(s.fs)
	damage := "data/logdb/log/0000000000000001-0000000000000004.log offset 64 (bad record CRC)"
	refusal := "the commits 5 to 8 come after the damage at " + damage + ": repair with -truncate to drop them, or restore the store from a copy"

	shown, _, err := s.repair(RepairOptions{})
	tr := shown.Plan.Truncation
	if err != nil || tr == nil || tr.FirstSeq != 5 || tr.LastSeq != 8 || shown.Things() == 0 || len(shown.Holes) != 0 {
		t.Fatalf("Repair() = %+v, %v, want the loss of the commits 5 to 8 and no hole", shown, err)
	}
	counted := s.fs.Counted()
	rep, keep, err := s.repair(RepairOptions{Apply: true, Keep: "keep"})
	if err == nil || err.Error() != refusal || rep.Applied || rep.Kept != "" {
		t.Fatalf("Repair(Apply) without Truncate = %+v, %v, want the refusal %q", rep, err, refusal)
	}
	if n := s.fs.Counted(); n != counted || !maps.Equal(keptFiles(s.fs), before) {
		t.Errorf("the refusal made %d operations and changed the files", n-counted)
	}
	if got := worldFiles(keep); len(got) != 0 || keep.Counted() != 0 {
		t.Errorf("the refusal made the copy: %v", slices.Sorted(maps.Keys(got)))
	}

	rep, keep = s.apply(RepairOptions{Truncate: true})
	if !rep.Applied || rep.Result.Seq != 3 || len(rep.Holes) != 0 {
		t.Errorf("Repair(Apply, Truncate) = %+v, want the state of the commit 3", rep)
	}
	if got := keptFiles(keep); !maps.Equal(got, before) {
		t.Errorf("the copy is not logdb/ as it was: %d files, want %d", len(got), len(before))
	}
	s.auditView().checkChains(t)
	// The writer goes on from the commit 3, with the records 4 and 5 again: those of the commits that were dropped are gone.
	s.act, s.login = s.act[:3], s.login[:1]
	s.reopen()
	s.commit()
	if err := s.srv.Close(); err != nil {
		t.Fatal(err)
	}
	s.auditView().checkChains(t)
	if again, _, err := s.repair(RepairOptions{}); err != nil || again.Things() != 0 {
		t.Errorf("the second repair = %+v, %v, want nothing to repair", again, err)
	}
}

func TestRepairRefusals(t *testing.T) {
	t.Run("no commit is left", func(t *testing.T) {
		s := newAuditStore(t, 3)
		s.withoutCheckpoint()
		s.damage(1)
		before := keptFiles(s.fs)
		for _, r := range []RepairOptions{{}, {Apply: true, Truncate: true, Keep: "keep"}, {Apply: true, Keep: "keep"}} {
			counted := s.fs.Counted()
			rep, keep, err := s.repair(r)
			want := "data/logdb: no commit can be kept, so a repair would leave an empty store: restore the store from a copy"
			if err == nil || err.Error() != want || rep.Applied || rep.Plan.Seq != 0 || rep.Plan.Truncation == nil {
				t.Errorf("Repair(%+v) = %+v, %v, want the refusal %q and the plan", r, rep, err, want)
			}
			if n := s.fs.Counted(); n != counted || !maps.Equal(keptFiles(s.fs), before) || keep.Counted() != 0 {
				t.Errorf("Repair(%+v) changed something", r)
			}
		}
	})

	t.Run("a table schema", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			schema  uint64
			damaged bool
			err     string
			raised  uint64
		}{
			{name: "the schema 10", schema: 10, damaged: true},
			{name: "the schema 9 is raised", schema: 9, damaged: true, raised: 9},
			{name: "the schema 8 is raised", schema: 8, damaged: true, raised: 8},
			{name: "the schema 7 has no table for the holes", schema: 7, damaged: true,
				err: "data/logdb: the repair drops audit records, and the store has the table schema 7, which has no table chains to " +
					"record the holes in: a writer that opens the store (whiteroom-db serve) upgrades it to 10, or restore the store " +
					"from a copy"},
			{name: "the schema 7 loses nothing that a hole is for", schema: 7},
			{name: "a newer schema", schema: dbwire.TableVersion + 1, damaged: true,
				err: fmt.Sprintf("data/logdb: the store schema is of version %d, newer than this program knows (%d)",
					dbwire.TableVersion+1, dbwire.TableVersion)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				s := newAuditStore(t, 8)
				s.setSchema(tc.schema)
				if tc.damaged {
					s.damage(4)
				} else if err := plantTemp(s.fs); err != nil {
					t.Fatal(err)
				}
				before := keptFiles(s.fs)
				for _, apply := range []bool{false, true} {
					r := RepairOptions{}
					if apply {
						r = RepairOptions{Apply: true, Keep: "keep"}
					}
					counted := s.fs.Counted()
					rep, keep, err := s.repair(r)
					if tc.err != "" {
						if err == nil || err.Error() != tc.err || rep.Applied {
							t.Errorf("Repair(%+v) = %v, %v, want %q", r, rep.Applied, err, tc.err)
						}
						if n := s.fs.Counted(); n != counted || !maps.Equal(keptFiles(s.fs), before) || keep.Counted() != 0 {
							t.Errorf("the refusal changed something")
						}
						continue
					}
					if err != nil || rep.Applied != apply || rep.SchemaFrom != tc.raised {
						t.Fatalf("Repair(%+v) = %+v, %v, want the schema %d raised", r, rep, err, tc.raised)
					}
					if !apply {
						continue
					}
					a := s.auditView()
					got := uint64(0)
					if err := maintain(t.Context(), s.fs, "data", s.opts, func(m *logdb.Maintenance) error {
						return m.View(t.Context(), func(tx *logdb.Tx) error {
							v, _ := tx.Get(dbwire.TableMeta, []byte(dbwire.MetaSchema))
							got, _ = schemaVersion(v)
							return nil
						})
					}); err != nil {
						t.Fatal(err)
					}
					want := tc.schema
					if tc.raised != 0 {
						want = dbwire.TableVersion
					}
					if got != want {
						t.Errorf("the schema after the repair is %d, want %d", got, want)
					}
					if tc.damaged {
						a.checkChains(t)
					}
				}
			})
		}
	})
}

// plantTemp makes a temporary file of a checkpoint in the world, which a repair removes.
func plantTemp(fsys *simfs.FS) error {
	f, err := fsys.OpenFile("logdb/.tmp-checkpoint-0123456789abcdef", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close(), fsys.SyncDir("logdb"))
}

func TestRepairNothing(t *testing.T) {
	s := newAuditStore(t, 5)
	before := worldFiles(s.fs)
	for _, r := range []RepairOptions{{}, {Apply: true, Keep: "keep"}} {
		counted := s.fs.Counted()
		rep, keep, err := s.repair(r)
		if err != nil || rep.Things() != 0 || rep.Applied || rep.Kept != "" || len(rep.Holes) != 0 || len(rep.Gaps) != 0 || rep.UnreadableTotal != 0 {
			t.Errorf("Repair(%+v) of a sound store = %+v, %v, want nothing to repair", r, rep, err)
		}
		if n := s.fs.Counted(); n != counted || !maps.Equal(worldFiles(s.fs), before) || keep.Counted() != 0 {
			t.Errorf("Repair(%+v) of a sound store changed something", r)
		}
	}
}

// TestRepairRecovery checks that what Open would do is a thing to repair: a torn tail, and the head that names a commit that
// the log does not have.
func TestRepairRecovery(t *testing.T) {
	s := newAuditStore(t, 5)
	s.withoutCheckpoint()
	last := segName(5)
	f, err := s.fs.OpenFile(last, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(f.Truncate(info.Size()-10), f.Sync(), f.Close()); err != nil {
		t.Fatal(err)
	}
	shown, _, err := s.repair(RepairOptions{})
	if err != nil || len(shown.Plan.Recovery) == 0 || !shown.Plan.Empty() || shown.Things() != len(shown.Plan.Recovery) {
		t.Fatalf("Repair() = %+v, %v, want the torn tail among the things", shown, err)
	}
	if text := shown.Plan.Recovery[0].Reason; !strings.Contains(text, "torn tail") {
		t.Errorf("the first thing that Open would do is %q, want the torn tail", text)
	}
	rep, _ := s.apply(RepairOptions{})
	if !rep.Applied || rep.Result.Seq != 4 {
		t.Errorf("Repair(Apply) = %+v, want the commit 4 kept", rep)
	}
	if again, _, err := s.repair(RepairOptions{}); err != nil || again.Things() != 0 {
		t.Errorf("the second repair = %+v, %v, want nothing to repair", again, err)
	}
	s.act, s.login = s.act[:4], s.login[:2]
	s.reopen()
	s.commit()
	if err := s.srv.Close(); err != nil {
		t.Fatal(err)
	}
	s.auditView().checkChains(t)
}

// forged makes the next commit with an activity record that dbwire.ReadRecordID refuses but that chains: the record that comes
// after it names its hash. It is what a writer that sends what the store never sends makes.
func (s *auditStore) forged() {
	s.t.Helper()
	r := s.next(dbwire.LogActivity)
	r.data = append([]byte(`{"extra":"1",`), r.data[1:]...)
	r.sum = sha256.Sum256(r.data)
	if _, _, _, ok := dbwire.ReadRecordID(r.data); ok {
		s.t.Fatal("dbwire.ReadRecordID reads the forged record")
	}
	s.update(func(tx *logdb.Tx) {
		if err := errors.Join(tx.AppendEvent(r.data), tx.AppendEvent([]byte("neither a record nor an event of the daemon")),
			tx.AppendEvent(dbwire.AppendMark(nil, testBase)), tx.Put(dbwire.TableHeads, []byte(r.log), []byte(r.head()))); err != nil {
			s.t.Error(err)
		}
	})
}

// lose makes the next commit as commit does, but the record of log, which the head names, is not in the log: a record that was
// lost before the repair.
func (s *auditStore) lose(log string) {
	s.t.Helper()
	r := s.next(log)
	s.update(func(tx *logdb.Tx) {
		if err := errors.Join(tx.AppendEvent(dbwire.AppendMark(nil, testBase)), tx.Put(dbwire.TableHeads, []byte(r.log), []byte(r.head()))); err != nil {
			s.t.Error(err)
		}
	})
}

func TestRepairUnreadable(t *testing.T) {
	build := func(t *testing.T) *auditStore {
		s := newFixtureStore(t)
		for range 3 {
			s.commit()
		}
		s.forged()
		for range 3 {
			s.commit()
		}
		if err := s.srv.Close(); err != nil {
			t.Fatal(err)
		}
		return s
	}
	t.Run("kept", func(t *testing.T) {
		s := build(t)
		before := keptFiles(s.fs)
		rep, _, err := s.repair(RepairOptions{})
		if err != nil || rep.UnreadableTotal != 2 || len(rep.Unreadable) != 2 || rep.Dropped || rep.Things() != 0 ||
			len(rep.Holes) != 0 {
			t.Fatalf("Repair() = %+v, %v, want two unreadable events and nothing to repair", rep, err)
		}
		// The forged record is no record that the repair sees, so the gap is the one that it leaves.
		if got := gapWords(rep); !slices.Equal(got, []string{"activity 4-4"}) {
			t.Errorf("the gaps are %q, want the record that the forged event takes", got)
		}
		applied, keep := s.apply(RepairOptions{})
		if applied.Applied || applied.Kept != "" || keep.Counted() != 0 || !maps.Equal(keptFiles(s.fs), before) {
			t.Errorf("Repair(Apply) = %+v, want nothing done: the unreadable events are kept", applied)
		}
	})
	t.Run("dropped", func(t *testing.T) {
		s := build(t)
		shown, _, err := s.repair(RepairOptions{DropUnreadable: true})
		if err != nil || !shown.Dropped || shown.Things() != 2 || !slices.Equal(holeWords(shown), []string{"activity 4-4"}) ||
			len(shown.Gaps) != 0 {
			t.Fatalf("Repair() = %+v, %v, want two events and the hole of the record 4", shown, err)
		}
		numbers := slices.Clone(shown.Unreadable)
		rep, _ := s.apply(RepairOptions{DropUnreadable: true})
		if !rep.Applied || !slices.Equal(holeWords(rep), []string{"activity 4-4"}) {
			t.Fatalf("Repair(Apply) = %+v", rep)
		}
		a := s.auditView()
		a.checkChains(t)
		for _, e := range a.events {
			if c := classify(e.Data); c.unreadable {
				t.Errorf("the event %d is unreadable still", e.Number)
			}
		}
		if h := a.holes[dbwire.LogActivity][4]; h.Last != 4 || h.Hash != s.act[3].sum {
			t.Errorf("the hole is %+v, want the hash of the forged record", h)
		}
		if len(numbers) != 2 {
			t.Errorf("the numbers of the events are %v", numbers)
		}
		if again, _, err := s.repair(RepairOptions{DropUnreadable: true}); err != nil || again.Things() != 0 || again.UnreadableTotal != 0 {
			t.Errorf("the second repair = %+v, %v", again, err)
		}
	})
}

// newFixtureStore makes a store that has no commit yet, on a simulated file system.
func newFixtureStore(t *testing.T) *auditStore {
	t.Helper()
	f := newFixture(t, func(o *Options) { o.Log.SegmentBytes = 1; o.Log.CheckpointBytes = -1 })
	return &auditStore{fixture: f}
}

func TestRepairGaps(t *testing.T) {
	t.Run("in the middle", func(t *testing.T) {
		s := newFixtureStore(t)
		s.commit()
		s.commit()
		s.lose(dbwire.LogActivity) // the record 3, in the commit 3
		for range 5 {
			s.commit()
		}
		if err := s.srv.Close(); err != nil {
			t.Fatal(err)
		}
		// The commit 6 is damaged, whose records are the activity 6 and the login 3, and the loss of the record 3 is not of the
		// repair.
		s.damage(6)
		rep, _ := s.apply(RepairOptions{})
		if want := []string{"login 3-3", "activity 6-6"}; !slices.Equal(holeWords(rep), want) {
			t.Errorf("the holes are %q, want %q", holeWords(rep), want)
		}
		if want := []string{"activity 3-3"}; !slices.Equal(gapWords(rep), want) {
			t.Errorf("the gaps are %q, want %q: the record 3 was lost before the repair", gapWords(rep), want)
		}
		a := s.auditView()
		if len(a.holes[dbwire.LogActivity]) != 1 || a.holes[dbwire.LogActivity][6].Last != 6 {
			t.Errorf("the rows of the holes are %v, want only the record 6: the gap has no row", a.holes)
		}
		// The gap stays as it was, and is shown again.
		again, _, err := s.repair(RepairOptions{})
		if err != nil || again.Things() != 0 || !slices.Equal(gapWords(again), []string{"activity 3-3"}) {
			t.Errorf("the second repair = %+v, %v", again, err)
		}
	})
	t.Run("at the end", func(t *testing.T) {
		s := newFixtureStore(t)
		for range 3 {
			s.commit()
		}
		s.lose(dbwire.LogActivity) // the record 4, the last
		if err := s.srv.Close(); err != nil {
			t.Fatal(err)
		}
		s.damage(2)
		rep, _ := s.apply(RepairOptions{})
		if want := []string{"login 1-1", "activity 2-2"}; !slices.Equal(holeWords(rep), want) {
			t.Errorf("the holes are %q, want %q", holeWords(rep), want)
		}
		if want := []string{"activity 4-4"}; !slices.Equal(gapWords(rep), want) {
			t.Errorf("the gaps are %q, want %q: the last record was lost before the repair", gapWords(rep), want)
		}
	})
}

// TestRepairRecorded checks that a hole that a row records already, with a repair of an earlier time, is no hole of this one:
// it is passed, and told nowhere.
func TestRepairRecorded(t *testing.T) {
	s := newAuditStore(t, 8)
	s.damage(4)
	first, _ := s.apply(RepairOptions{})
	if want := []string{"login 2-2", "activity 4-4"}; !slices.Equal(holeWords(first), want) {
		t.Fatalf("the holes are %q, want %q", holeWords(first), want)
	}
	s.reopen()
	for range 3 {
		s.commit() // the commits 9 to 11
	}
	if err := s.srv.Close(); err != nil {
		t.Fatal(err)
	}
	// The commit 10 (activity 10, login 5) is lost, in the log of the generation of the repair.
	segs := slices.Sorted(maps.Keys(filesOf(s.fs, "logdb/log")))
	flipAt(t, s.fs, segs[len(segs)-2], 100)
	rep, _ := s.apply(RepairOptions{})
	if want := []string{"login 5-5", "activity 10-10"}; !slices.Equal(holeWords(rep), want) || len(rep.Gaps) != 0 {
		t.Errorf("the holes are %q and the gaps %q, want %q and none: the first two are recorded", holeWords(rep), gapWords(rep), want)
	}
	a := s.auditView()
	passed := a.checkChains(t)
	if want := []string{"2-2", "5-5"}; !slices.Equal(passed[dbwire.LogLogin], want) {
		t.Errorf("the holes of the login history that are passed are %q, want %q", passed[dbwire.LogLogin], want)
	}
	if want := []string{"4-4", "10-10"}; !slices.Equal(passed[dbwire.LogActivity], want) {
		t.Errorf("the holes of the activity log that are passed are %q, want %q", passed[dbwire.LogActivity], want)
	}
	if n := len(a.holes[dbwire.LogLogin]) + len(a.holes[dbwire.LogActivity]); n != 4 {
		t.Errorf("the table chains has %d rows of holes, want 4", n)
	}
	if again, _, err := s.repair(RepairOptions{}); err != nil || again.Things() != 0 || len(again.Holes) != 0 || len(again.Gaps) != 0 {
		t.Errorf("the second repair = %+v, %v", again, err)
	}
}

// TestRepairWidensHole checks that a hole at the end of a log, which a row records, and which the writer has followed with a
// record, is replaced by a wider one if the record that follows it is lost too: the row has the same first record.
func TestRepairWidensHole(t *testing.T) {
	s := newAuditStore(t, 8)
	s.damage(8)
	if rep, _ := s.apply(RepairOptions{}); !slices.Equal(holeWords(rep), []string{"login 4-4", "activity 8-8"}) {
		t.Fatalf("the holes are %q", holeWords(rep))
	}
	s.reopen()
	s.commit() // the activity record 9
	s.commit() // the activity record 10 and the login record 5
	if err := s.srv.Close(); err != nil {
		t.Fatal(err)
	}
	segs := slices.Sorted(maps.Keys(filesOf(s.fs, "logdb/log")))
	flipAt(t, s.fs, segs[len(segs)-2], 100) // the commit 9
	rep, _ := s.apply(RepairOptions{})
	if want := []string{"activity 8-9"}; !slices.Equal(holeWords(rep), want) || len(rep.Gaps) != 0 {
		t.Errorf("the holes are %q and the gaps %q, want %q and none", holeWords(rep), gapWords(rep), want)
	}
	a := s.auditView()
	passed := a.checkChains(t)
	if h := a.holes[dbwire.LogActivity]; len(h) != 1 || h[8].Last != 9 || h[8].Hash != s.act[8].sum {
		t.Errorf("the rows of the holes of the activity log are %v, want one for the records 8 to 9", h)
	}
	if want := []string{"8-9"}; !slices.Equal(passed[dbwire.LogActivity], want) {
		t.Errorf("the holes that are passed are %q, want %q", passed[dbwire.LogActivity], want)
	}
	if want := []string{"4-4"}; !slices.Equal(passed[dbwire.LogLogin], want) {
		t.Errorf("the holes of the login history that are passed are %q, want %q", passed[dbwire.LogLogin], want)
	}
}

// TestRepairOldGap checks that records that were missing before the repair are no hole of it unless the repair drops as many
// events between the records around them as there are records missing: the login records 2 to k+1 never reached the log (the
// gap that whiteroom doctor reports), and the commit 3, whose two events are the activity record 3 and a mark, is damaged.
func TestRepairOldGap(t *testing.T) {
	for _, tc := range []struct {
		name        string
		lost        int // the login records that never reached the log
		holes, gaps []string
		loginRows   int // the rows of the table chains for the login history
	}{
		{"fewer records than events", 1, []string{"login 2-2", "activity 3-3"}, nil, 1},
		{"as many records as events", 2, []string{"login 2-3", "activity 3-3"}, nil, 1},
		{"one record more than events", 3, []string{"activity 3-3"}, []string{"login 2-4"}, 0},
		{"five records more than events", 5, []string{"activity 3-3"}, []string{"login 2-6"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newFixtureStore(t)
			s.commit() // the segment 1: activity 1
			s.commit() // the segment 2: activity 2, login 1
			for range tc.lost {
				s.lose(dbwire.LogLogin)
			}
			s.commit() // the activity record 3, with a mark
			s.commit() // activity 4, login tc.lost+2
			s.commit() // activity 5
			s.commit() // activity 6, login tc.lost+3
			if err := s.srv.Close(); err != nil {
				t.Fatal(err)
			}
			if before, _, err := s.repair(RepairOptions{}); err != nil || len(before.Holes) != 0 ||
				!slices.Equal(gapWords(before), []string{fmt.Sprintf("login 2-%d", tc.lost+1)}) {
				t.Fatalf("before the damage, Repair() = %+v, %v, want the gap of the lost records", before, err)
			}
			s.damage(2 + tc.lost + 1)
			rep, _ := s.apply(RepairOptions{})
			if !slices.Equal(holeWords(rep), tc.holes) || !slices.Equal(gapWords(rep), tc.gaps) {
				t.Errorf("the holes are %q and the gaps %q, want %q and %q", holeWords(rep), gapWords(rep), tc.holes, tc.gaps)
			}
			a := s.auditView()
			if rows := len(a.holes[dbwire.LogLogin]); rows != tc.loginRows {
				t.Errorf("the table chains has %d rows of holes of the login history, want %d", rows, tc.loginRows)
			}
			if tc.loginRows > 0 {
				a.checkChains(t)
				return
			}
			// The gap is as it was: no row passes it, and it is shown again.
			again, _, err := s.repair(RepairOptions{})
			if err != nil || again.Things() != 0 || !slices.Equal(gapWords(again), tc.gaps) {
				t.Errorf("the second repair = %+v, %v, want the gap again", again, err)
			}
		})
	}
}

// TestRepairOldGapAtTheEnd checks the same at the end of a log: the head of the activity log names the records after the last
// that is read, and they are a hole of the repair only if it drops as many events after that record as there are records. The
// last commit, whose events are the activity record and a mark, is damaged, and so is the login record of it if its number is
// even; the commits before it that have a mark and no record of the log are the records that never reached it.
func TestRepairOldGapAtTheEnd(t *testing.T) {
	for _, tc := range []struct {
		name        string
		commits     int // before the records that never reached the log
		lost        int // the activity records that never reached the log
		holes, gaps []string
	}{
		// 3 events (activity 6, login 3, a mark) against the records 5 and 6.
		{"more events than records", 4, 1, []string{"login 3-3", "activity 5-6"}, nil},
		// 2 events (activity 5, a mark) against the records 4 and 5.
		{"as many events as records", 3, 1, []string{"activity 4-5"}, nil},
		// 2 events (activity 7, a mark) against the records 5 to 7.
		{"one record more than events", 4, 2, nil, []string{"activity 5-7"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newFixtureStore(t)
			for range tc.commits {
				s.commit()
			}
			for range tc.lost {
				s.lose(dbwire.LogActivity)
			}
			s.commit() // the last commit
			if err := s.srv.Close(); err != nil {
				t.Fatal(err)
			}
			s.damage(tc.commits + tc.lost + 1)
			rep, _ := s.apply(RepairOptions{})
			if !slices.Equal(holeWords(rep), tc.holes) || !slices.Equal(gapWords(rep), tc.gaps) {
				t.Errorf("the holes are %q and the gaps %q, want %q and %q", holeWords(rep), gapWords(rep), tc.holes, tc.gaps)
			}
		})
	}
}

// checkpointBytes returns the checkpoint of the store, and setCheckpoint replaces it.
func (s *auditStore) checkpointBytes() []byte {
	s.t.Helper()
	var b []byte
	s.fs.Walk(func(p string, data []byte) {
		if p == "logdb/checkpoint" {
			b = data
		}
	})
	if b == nil {
		s.t.Fatal("the store has no checkpoint")
	}
	return b
}

func (s *auditStore) setCheckpoint(b []byte) {
	s.t.Helper()
	f, err := s.fs.OpenFile("logdb/checkpoint", os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		s.t.Fatal(err)
	}
	if _, err := f.Write(b); err != nil {
		s.t.Fatal(err)
	}
	if err := errors.Join(f.Sync(), f.Close()); err != nil {
		s.t.Fatal(err)
	}
}

// TestRepairOldGapAtTheEndTruncated checks that the events that a repair drops after the state that it keeps do not pay for the
// records that the head of a log names after its last record: those records were written in the commits of the kept state. The
// login records 3 to 5 never reached the log (a gap that was there before), and the head of the login history names them.
func TestRepairOldGapAtTheEndTruncated(t *testing.T) {
	// build makes the commits 1 to 4 (the activity records 1 to 4, the login records 1 and 2), the commits 5 to 7, which the head
	// of the login history names the records 3 to 5 in and have none, and the commits 8 and 9 (the activity records 5 and 6, the
	// login record 6). The checkpoint that the store is left with covers the commits up to the commit 9, and the commits 10 to 12
	// (the activity records 7 to 9, the login record 7) come after it.
	build := func(t *testing.T) *auditStore {
		s := newFixtureStore(t)
		for range 4 {
			s.commit()
		}
		for range 3 {
			s.lose(dbwire.LogLogin)
		}
		s.commit()
		s.commit()
		if err := s.srv.Close(); err != nil {
			t.Fatal(err)
		}
		covers := s.checkpointBytes()
		s.reopen()
		for range 3 {
			s.commit()
		}
		if err := s.srv.Close(); err != nil {
			t.Fatal(err)
		}
		s.setCheckpoint(covers)
		return s
	}
	for _, tc := range []struct {
		name        string
		damaged     []int
		holes, gaps []string
		keptThrough uint64
	}{
		// The damage at the commit 10 ends the kept state, the checkpoint's: the events of the commits 11 and 12 are of the commits
		// that the repair drops, and the records 3 to 6 of the login history, which the checkpoint's head names, are not in them.
		{"the lost commits before the kept state pay", []int{8, 9, 10}, []string{"login 3-6", "activity 5-6"}, nil, 9},
		// The events of the commit 9 (3 of them) are all that pays: 4 records are missing in the login history, 1 in the activity log.
		{"the truncation does not make up the rest", []int{9, 10}, []string{"activity 6-6"}, []string{"login 3-6"}, 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := build(t)
			s.damage(tc.damaged...)
			rep, _ := s.apply(RepairOptions{Truncate: true})
			p, tr := rep.Plan, rep.Plan.Truncation
			if p.Checkpoint != "used" || p.Seq != tc.keptThrough || tr == nil || tr.LastSeq != 12 || tr.FirstEvent < p.NextEvent {
				t.Fatalf("the plan is %+v (truncation %+v), want the checkpoint's state of the commit %d, and a truncation after its events",
					p, tr, tc.keptThrough)
			}
			if !slices.Equal(holeWords(rep), tc.holes) || !slices.Equal(gapWords(rep), tc.gaps) {
				t.Errorf("the holes are %q and the gaps %q, want %q and %q", holeWords(rep), gapWords(rep), tc.holes, tc.gaps)
			}
			if len(tc.gaps) == 0 {
				s.auditView().checkChains(t)
			}
		})
	}
	t.Run("the truncation does not pay", func(t *testing.T) {
		s := newFixtureStore(t)
		s.commit() // 1: activity 1
		s.commit() // 2: activity 2, login 1
		s.commit() // 3: activity 3
		s.commit() // 4: activity 4, login 2
		for range 3 {
			s.lose(dbwire.LogLogin) // 5 to 7: the head of the login history names 3, 4 and 5
		}
		for range 4 {
			s.commit() // 8 to 11: the commit 8 is damaged, and the commits after it are dropped
		}
		if err := s.srv.Close(); err != nil {
			t.Fatal(err)
		}
		s.withoutCheckpoint() // the state is read from the log, and a damage after its start drops the commits after it
		s.damage(8)
		rep, _ := s.apply(RepairOptions{Truncate: true})
		if tr := rep.Plan.Truncation; tr == nil || tr.FirstSeq != 9 || tr.LastSeq != 11 || tr.FirstEvent < rep.Plan.NextEvent {
			t.Fatalf("the truncation is %+v, want the commits 9 to 11, after the events of the kept state (%d)", tr, rep.Plan.NextEvent)
		}
		if len(rep.Holes) != 0 || !slices.Equal(gapWords(rep), []string{"login 3-5"}) {
			t.Errorf("the holes are %q and the gaps %q, want only the gap that was there before", holeWords(rep), gapWords(rep))
		}
		if a := s.auditView(); len(a.holes[dbwire.LogLogin]) != 0 {
			t.Errorf("the rows of the holes of the login history are %v, want none", a.holes[dbwire.LogLogin])
		}
	})
}

func TestRepairFailedSync(t *testing.T) {
	boom := syscall.EIO
	for _, tc := range []struct {
		name string
		arm  func(t *testing.T, s *auditStore, o *Options)
	}{
		{name: "the first sync of the new generation",
			arm: func(t *testing.T, s *auditStore, o *Options) {
				s.fs.Inject(simfs.Fault{Match: func(op simfs.Op) bool {
					return op.Kind == simfs.OpSync && strings.HasPrefix(op.Path, "logdb/log/0000000000000002-")
				}, Err: boom, Drop: true, Times: 1})
			}},
		{name: "the sync of the end of the base",
			arm: func(t *testing.T, s *auditStore, o *Options) {
				var based atomic.Bool
				o.Log.Hook = func(point string) error {
					if point == "compact:base" {
						based.Store(true)
					}
					return nil
				}
				s.fs.Inject(simfs.Fault{Match: func(op simfs.Op) bool {
					return based.Load() && op.Kind == simfs.OpSync && strings.HasPrefix(op.Path, "logdb/log/0000000000000002-")
				}, Err: boom, Drop: true, Times: 1})
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newAuditStore(t, 8)
			s.damage(4)
			before := keptFiles(s.fs)
			o := s.opts
			tc.arm(t, s, &o)
			marked := 0
			hook := o.Hook
			o.Hook = func(point string) error {
				if point == "maintain:marked" {
					marked++
				}
				if hook != nil {
					return hook(point)
				}
				return nil
			}
			keep := simfs.New("keep")
			rep, err := repair(t.Context(), s.fs, "data", o, RepairOptions{Apply: true, Keep: "keep"}, keepOn(keep))
			if !errors.Is(err, logdb.ErrFailed) || rep.Applied || marked != 1 {
				t.Fatalf("Repair() = %+v, %v after %d marks, want an error that wraps logdb.ErrFailed and one mark", rep, err, marked)
			}
			mark, ok := worldFile(s.fs, "stopped")
			if !ok || markLine(mark, "boot") != testBoot || !strings.Contains(markLine(mark, "cause"), "input/output error") {
				t.Errorf("the mark is %q, want the boot %s and the cause", mark, testBoot)
			}
			if got := keptFiles(keep); !maps.Equal(got, before) || rep.Kept != "keep/logdb" {
				t.Errorf("the copy has %d files, want %d, as before the repair (kept %q)", len(got), len(before), rep.Kept)
			}
			// No process of this boot opens the directory again, and the others do after a restart.
			if _, err := openClosing(t, s.fixture, s.opts); !errors.Is(err, ErrNotRestarted) {
				t.Errorf("Open in the same boot = %v, want ErrNotRestarted", err)
			}
		})
	}

	// A sync that fails in the copy is no failure of the data directory: no mark, nothing changed, and the copy is removed.
	t.Run("the copy", func(t *testing.T) {
		s := newAuditStore(t, 8)
		s.damage(4)
		before := worldFiles(s.fs)
		counted := s.fs.Counted()
		keep := simfs.New("keep")
		keep.Inject(simfs.Fault{Match: func(op simfs.Op) bool { return op.Kind == simfs.OpSync && strings.HasPrefix(op.Path, "logdb/log/") },
			Err: boom, Times: 1})
		rep, err := repair(t.Context(), s.fs, "data", s.opts, RepairOptions{Apply: true, Keep: "keep"}, keepOn(keep))
		if err == nil || errors.Is(err, logdb.ErrFailed) || rep.Applied || rep.Kept != "" {
			t.Fatalf("Repair() = %+v, %v, want a plain error and no copy", rep, err)
		}
		if _, ok := worldFile(s.fs, "stopped"); ok {
			t.Error("a failed sync of the copy left the mark")
		}
		if n := s.fs.Counted(); n != counted || !maps.Equal(worldFiles(s.fs), before) {
			t.Errorf("the data directory changed: %d operations", n-counted)
		}
		if got := worldFiles(keep); len(got) != 0 {
			t.Errorf("the failed copy left %v", slices.Sorted(maps.Keys(got)))
		}
	})
}

// failingRemove makes the removal of the segments of the generation 1 fail once, after the repair has made the generation 2 the
// store.
func failingRemove(s *auditStore) {
	s.fs.Inject(simfs.Fault{Match: func(op simfs.Op) bool {
		return op.Kind == simfs.OpRemove && strings.HasPrefix(op.Path, "logdb/log/0000000000000001-")
	}, Err: syscall.EIO, Times: 1})
}

// TestRepairRerun checks that a repair that fails after the end of its base, which leaves the repaired store and files that
// are no use, is finished by a second one that adds no hole row and no event.
func TestRepairRerun(t *testing.T) {
	for _, tc := range []struct {
		name string
		arm  func(s *auditStore, o *Options)
		left int // the files that are left, at least
	}{
		{"the removal of the old generation", func(s *auditStore, o *Options) { failingRemove(s) }, 1},
		{"the collection of a temporary file", func(s *auditStore, o *Options) {
			if err := plantTemp(s.fs); err != nil {
				s.t.Fatal(err)
			}
			done := false
			o.Log.Hook = func(point string) error {
				if point == "gc:remove" && !done {
					done = true
					return errors.New("boom")
				}
				return nil
			}
		}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newAuditStore(t, 8)
			s.damage(4)
			o := s.opts
			tc.arm(s, &o)
			rep, err := repair(t.Context(), s.fs, "data", o, RepairOptions{Apply: true, Keep: "keep"}, keepOn(simfs.New("keep")))
			if err == nil || errors.Is(err, logdb.ErrFailed) || rep.Applied || rep.Result.Generation != 2 || len(rep.Holes) != 2 {
				t.Fatalf("Repair() = %+v, %v, want a plain error after the base of the generation 2", rep, err)
			}
			if _, ok := worldFile(s.fs, "stopped"); ok {
				t.Error("the error left the mark")
			}
			a := s.auditView()
			a.checkChains(t)
			shape := shapeOf(a)
			rows := len(a.holes[dbwire.LogLogin]) + len(a.holes[dbwire.LogActivity])
			if rows != 2 {
				t.Fatalf("the rows of the holes are %d, want 2", rows)
			}

			again, _, err := s.repair(RepairOptions{})
			if err != nil || again.Things() < tc.left || len(again.Holes) != 0 || len(again.Gaps) != 0 || !again.Plan.Empty() {
				t.Fatalf("the second repair shows %+v, %v, want only what is left", again, err)
			}
			fin, _ := s.apply(RepairOptions{})
			if !fin.Applied || len(fin.Holes) != 0 || fin.Result.Generation == 0 {
				t.Errorf("the second repair = %+v, want it done, with no hole", fin)
			}
			b := s.auditView()
			b.checkChains(t)
			if got := shapeOf(b); got != shape {
				t.Errorf("the events are %q after the second repair, were %q", got, shape)
			}
			if n := len(b.holes[dbwire.LogLogin]) + len(b.holes[dbwire.LogActivity]); n != 2 {
				t.Errorf("the rows of the holes are %d after the second repair, want 2", n)
			}
			if third, _, err := s.repair(RepairOptions{}); err != nil || third.Things() != 0 {
				t.Errorf("the third repair = %+v, %v, want nothing to repair", third, err)
			}
		})
	}
}

// repairOf returns the options of a repair that is shown, or that is applied with a copy in the directory keep.
func repairOf(apply bool) RepairOptions {
	if apply {
		return RepairOptions{Apply: true, Keep: "keep"}
	}
	return RepairOptions{}
}

// repairWith is repair with the options o, and a copy in a new directory.
func (s *auditStore) repairWith(o Options, r RepairOptions) (RepairReport, *simfs.FS, error) {
	s.t.Helper()
	keep := simfs.New("keep")
	rep, err := repair(s.t.Context(), s.fs, "data", o, r, keepOn(keep))
	return rep, keep, err
}

func TestRepairMark(t *testing.T) {
	const at, cause = "2026-11-15T09:29:00Z", "sync data/logdb/log/x.log: input/output error"
	valid := markWith(testBoot, "time: "+at+"\n", "cause: "+cause+"\n")
	for _, apply := range []bool{false, true} {
		t.Run(fmt.Sprintf("apply %v", apply), func(t *testing.T) {
			t.Run("refuses", func(t *testing.T) {
				for _, tc := range []struct {
					name, mark, want string
					notRestarted     bool
				}{
					{"a mark of this boot", valid, "data: a sync failed at " + at + " (" + cause + ")" + refusedTail, true},
					{"a mark that cannot be read", "", unreadable, false},
				} {
					t.Run(tc.name, func(t *testing.T) {
						s := newAuditStore(t, 6)
						s.damage(3)
						putMark(t, s.fs, tc.mark)
						counted, before := s.fs.Counted(), worldFiles(s.fs)
						rep, keep, err := s.repair(repairOf(apply))
						if err == nil || err.Error() != tc.want || errors.Is(err, ErrNotRestarted) != tc.notRestarted {
							t.Errorf("Repair() = %v, want %q, wrapping ErrNotRestarted: %v", err, tc.want, tc.notRestarted)
						}
						if rep.Plan.Generation != 0 || rep.Applied || keep.Counted() != 0 {
							t.Errorf("the refusal went on: %+v", rep)
						}
						if n := s.fs.Counted(); n != counted || !maps.Equal(worldFiles(s.fs), before) {
							t.Errorf("the refusal made %d operations", n-counted)
						}
						// It took no lock: a repair of another boot enters at once.
						o := s.opts
						o.Boot = machineWith(anotherBoot)
						if tc.notRestarted {
							start := time.Now()
							if _, _, err := s.repairWith(o, RepairOptions{}); err != nil || time.Since(start) > lockWait/2 {
								t.Errorf("Repair() in another boot = %v after %v, want it at once", err, time.Since(start))
							}
						}
					})
				}
			})

			t.Run("an earlier boot", func(t *testing.T) {
				s := newAuditStore(t, 6)
				s.damage(3)
				putMark(t, s.fs, valid)
				o := s.opts
				o.Boot = machineWith(anotherBoot)
				rep, _, err := s.repairWith(o, repairOf(apply))
				if err != nil || rep.Applied != apply || len(rep.Holes) != 1 {
					t.Fatalf("Repair() = %+v, %v", rep, err)
				}
				_, left := worldFile(s.fs, "stopped")
				lines := s.logs.all()
				if apply {
					if left || len(lines) != 1 || lines[0] != removalLine(at, cause) {
						t.Errorf("the mark is there: %v, log lines %q, want it removed and said", left, lines)
					}
				} else if !left || len(lines) != 0 {
					t.Errorf("the mark is there: %v, log lines %q, want it left where it is, and nothing said", left, lines)
				}
			})

			// A mark that appears after the first look, while the repair waits for the daemon: nothing is read of what the page
			// cache may have, and nothing is written.
			for _, tc := range []struct {
				name, mark, want string
				notRestarted     bool
			}{
				{"a mark after the first look", valid, "data: a sync failed at " + at + " (" + cause + ")" + refusedTail, true},
				{"an empty mark after the first look", "", unreadable, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					s := newAuditStore(t, 6)
					s.reopen()
					done := make(chan error, 1)
					keep := simfs.New("keep")
					go func() {
						_, err := repair(t.Context(), s.fs, "data", s.opts, repairOf(apply), keepOn(keep))
						done <- err
					}()
					waitsForWorld(t, s.fs)
					putMark(t, s.fs, tc.mark)
					before := keptFiles(s.fs)
					if err := s.srv.Close(); err != nil {
						t.Fatal(err)
					}
					err := <-done
					if err == nil || err.Error() != tc.want || errors.Is(err, ErrNotRestarted) != tc.notRestarted {
						t.Errorf("Repair() = %v, want %q, wrapping ErrNotRestarted: %v", err, tc.want, tc.notRestarted)
					}
					after := keptFiles(s.fs)
					delete(before, "logdb/checkpoint")
					delete(before, "logdb/head")
					delete(after, "logdb/checkpoint")
					delete(after, "logdb/head")
					if !maps.Equal(before, after) || keep.Counted() != 0 {
						t.Errorf("the refused repair changed logdb/")
					}
					if got, _ := worldFile(s.fs, "stopped"); got != tc.mark {
						t.Errorf("the mark is %q, want it as it was written", got)
					}
				})
			}
		})
	}
}

// waitsForWorld returns when a process waits for those that have the engine in the world fsys open: it has lock/entry.
func waitsForWorld(t testing.TB, fsys *simfs.FS) {
	t.Helper()
	logFS, err := fsys.Sub("logdb")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logFS.Close() }()
	eventually(t, "the repair to wait for the daemon", func() bool {
		unlock, err := logFS.Lock("lock/entry", vfs.Shared)
		if err == nil {
			_ = unlock()
			return false
		}
		if !errors.Is(err, vfs.ErrLocked) {
			t.Fatalf("Lock() = %v", err)
		}
		return true
	})
}

func TestRepairInUse(t *testing.T) {
	for _, apply := range []bool{false, true} {
		t.Run(fmt.Sprintf("apply %v", apply), func(t *testing.T) {
			s := newAuditStore(t, 4)
			s.reopen()
			before, counted := worldFiles(s.fs), s.fs.Counted()
			start := time.Now()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			keep := simfs.New("keep")
			rep, err := repair(ctx, s.fs, "data", s.opts, repairOf(apply), keepOn(keep))
			if elapsed := time.Since(start); elapsed < lockWait-100*time.Millisecond || elapsed > lockWait+3*time.Second {
				t.Errorf("Repair() of a directory in use took %v, want about %v", elapsed, lockWait)
			}
			if want := "data: the data directory is in use"; !errors.Is(err, ErrInUse) || err.Error() != want {
				t.Errorf("Repair() of a directory in use = %v, want %q, wrapping ErrInUse", err, want)
			}
			if rep.Plan.Generation != 0 || keep.Counted() != 0 || s.fs.Counted() != counted || !maps.Equal(worldFiles(s.fs), before) {
				t.Errorf("Repair() of a directory in use went on or changed something: %+v", rep)
			}
			if _, ok := worldFile(s.fs, "stopped"); ok {
				t.Error("Repair() of a directory in use left a mark")
			}
			s.commit() // the daemon goes on

			// A caller that ends sooner is not told that the directory is in use.
			canceled, cancelAfter := context.WithCancel(t.Context())
			time.AfterFunc(100*time.Millisecond, cancelAfter)
			timed, stop := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer stop()
			for _, tt := range []struct {
				name string
				ctx  context.Context
				want error
			}{{"canceled", canceled, context.Canceled}, {"timed out", timed, context.DeadlineExceeded}} {
				start = time.Now()
				_, err = repair(tt.ctx, s.fs, "data", s.opts, repairOf(apply), keepOn(simfs.New("keep")))
				if !errors.Is(err, tt.want) || errors.Is(err, ErrInUse) {
					t.Errorf("Repair() with a context that is %s = %v, want %v and not ErrInUse", tt.name, err, tt.want)
				}
				if elapsed := time.Since(start); elapsed > lockWait/2 {
					t.Errorf("Repair() with a context that is %s after 100 ms took %v", tt.name, elapsed)
				}
			}
		})
	}
}

// TestRepairWaits checks that a repair waits for the daemon, and goes on when it has closed.
func TestRepairWaits(t *testing.T) {
	s := newAuditStore(t, 6)
	s.damage(3)
	s.reopen() // an open that the damage does not stop, as the log is read from the checkpoint
	done := make(chan error, 1)
	var rep RepairReport
	go func() {
		var err error
		rep, err = repair(t.Context(), s.fs, "data", s.opts, repairOf(true), keepOn(simfs.New("keep")))
		done <- err
	}()
	waitsForWorld(t, s.fs)
	select {
	case err := <-done:
		t.Fatalf("Repair() ended with %v while the daemon had the directory open", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := s.srv.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil || !rep.Applied {
		t.Errorf("Repair() after the daemon closed = %+v, %v", rep, err)
	}
}

// slowFS is a file system whose first read of a segment, after it has been locked as a maintenance locks it, takes a while.
type slowFS struct {
	vfs.FS
	st *slowState
}

type slowState struct {
	locked atomic.Bool
	once   sync.Once
	delay  time.Duration
}

func (f *slowFS) Sub(name string) (vfs.FS, error) {
	sub, err := f.FS.Sub(name)
	if err != nil {
		return nil, err
	}
	return &slowFS{sub, f.st}, nil
}

func (f *slowFS) Lock(name string, mode vfs.LockMode) (func() error, error) {
	unlock, err := f.FS.Lock(name, mode)
	if err == nil && name == "lock/writer" {
		f.st.locked.Store(true)
	}
	return unlock, err
}

func (f *slowFS) wait(name string) {
	if f.st.locked.Load() && strings.HasPrefix(name, "log/") {
		f.st.once.Do(func() { time.Sleep(f.st.delay) })
	}
}

func (f *slowFS) Open(name string) (vfs.File, error) {
	f.wait(name)
	return f.FS.Open(name)
}

func (f *slowFS) OpenFile(name string, flag int, perm fs.FileMode) (vfs.File, error) {
	f.wait(name)
	return f.FS.OpenFile(name, flag, perm)
}

// TestRepairSlowRead checks that the two seconds that a repair waits for the other processes end when it holds the locks:
// reading a large store takes longer, and is no sign that the directory is in use.
func TestRepairSlowRead(t *testing.T) {
	for _, apply := range []bool{false, true} {
		t.Run(fmt.Sprintf("apply %v", apply), func(t *testing.T) {
			s := newAuditStore(t, 6)
			s.damage(3)
			root := &slowFS{FS: s.fs, st: &slowState{delay: lockWait + 300*time.Millisecond}}
			start := time.Now()
			rep, err := repair(t.Context(), root, "data", s.opts, repairOf(apply), keepOn(simfs.New("keep")))
			if err != nil || rep.Applied != apply || len(rep.Holes) != 1 {
				t.Fatalf("Repair() = %+v, %v, want the repair that is not stopped by the time that it takes", rep, err)
			}
			if time.Since(start) < lockWait {
				t.Errorf("Repair() took %v: the read was not slow", time.Since(start))
			}
		})
	}
}

// TestRepairRefusedBeforeLocks checks that what the engine refuses before it calls f is its own error, not that of a directory
// in use.
func TestRepairRefusedBeforeLocks(t *testing.T) {
	for _, apply := range []bool{false, true} {
		t.Run(fmt.Sprintf("apply %v", apply), func(t *testing.T) {
			s := newAuditStore(t, 3)
			f, err := s.fs.OpenFile("logdb/log/stray", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if err := errors.Join(f.Sync(), f.Close(), s.fs.SyncDir("logdb/log")); err != nil {
				t.Fatal(err)
			}
			before, counted := worldFiles(s.fs), s.fs.Counted()
			start := time.Now()
			rep, _, err := s.repair(repairOf(apply))
			if err == nil || errors.Is(err, ErrInUse) || !strings.Contains(err.Error(), "stray") || time.Since(start) > lockWait/2 {
				t.Errorf("Repair() = %v after %v, want the error of the engine about the stray file", err, time.Since(start))
			}
			if rep.Applied || s.fs.Counted() != counted || !maps.Equal(worldFiles(s.fs), before) {
				t.Errorf("the refusal went on or changed something")
			}
		})
	}
}

// auditDir makes a store on a data directory on the OS, which has the commits that auditStore makes, and closes its server.
func newAuditDir(t *testing.T, commits int) *auditStore {
	t.Helper()
	f := newDirFixture(t, func(o *Options) { o.Log.SegmentBytes = 1; o.Log.CheckpointBytes = -1 })
	s := &auditStore{fixture: f}
	for range commits {
		s.commit()
	}
	if err := f.srv.Close(); err != nil {
		t.Fatal(err)
	}
	return s
}

// damageDir flips a byte in the record of each commit of commits in the data directory on the OS.
func (s *auditStore) damageDir(commits ...int) {
	s.t.Helper()
	for _, c := range commits {
		editFile(s.t, filepath.Join(s.dir, filepath.FromSlash(segName(c))), flipByte(100))
	}
}

// putOriginalFile puts a file of an original in files/ of the data directory dir.
func putOriginalFile(t testing.TB, dir, data string) string {
	t.Helper()
	sum := hexSum(data)
	p := filepath.Join(dir, "files", sum[:2], sum)
	mkdir(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRepairShowReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the superuser writes where it likes")
	}
	s := newAuditDir(t, 6)
	putOriginalFile(t, s.dir, "an original")
	s.damageDir(3)
	// The files first, and then the directories, deepest first: one that cannot be entered cannot be walked.
	var dirs []string
	if err := filepath.WalkDir(s.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
			return nil
		}
		return os.Chmod(p, 0o400)
	}); err != nil {
		t.Fatal(err)
	}
	for _, p := range slices.Backward(dirs) {
		if err := os.Chmod(p, 0o500); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, p := range dirs {
			_ = os.Chmod(p, 0o700)
		}
	})
	before := contents(t, s.dir)
	rep, err := Repair(t.Context(), s.dir, s.opts, RepairOptions{})
	if err != nil || rep.Applied || !slices.Equal(holeWords(rep), []string{"activity 3-3"}) || rep.Things() == 0 {
		t.Fatalf("Repair() of a directory that it cannot write in = %+v, %v", rep, err)
	}
	if after := contents(t, s.dir); !maps.Equal(after, before) {
		t.Errorf("Repair() changed the data directory:\n%v\nwas\n%v", after, before)
	}

	// With Apply it refuses the root that it cannot write in, as Maintain does, and changes nothing.
	keep := filepath.Join(t.TempDir(), "keep")
	_, err = Repair(t.Context(), s.dir, s.opts, RepairOptions{Apply: true, Keep: keep})
	want := s.dir + ": whiteroom-db cannot write in the data directory (permission denied); it must be able to leave the " +
		"mark of a failed sync there: give the directory to the user of the daemon"
	if err == nil || err.Error() != want {
		t.Errorf("Repair(Apply) of a directory that it cannot write in = %v, want %q", err, want)
	}
	if _, err := os.Lstat(keep); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the refusal made the copy: %v", err)
	}
	if after := contents(t, s.dir); !maps.Equal(after, before) {
		t.Errorf("Repair(Apply) changed the data directory:\n%v\nwas\n%v", after, before)
	}
}

// fileBytes returns the regular files under root, with what they hold, by their paths from root.
func fileBytes(t testing.TB, root string) map[string]string {
	t.Helper()
	m := map[string]string{}
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		m[filepath.ToSlash(rel)] = string(data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRepairKeepDir(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing %v", existing), func(t *testing.T) {
			s := newAuditDir(t, 8)
			putOriginalFile(t, s.dir, "an original")
			s.damageDir(4, 8)
			if problems := problemLines(t, s.dir, s.opts); len(problems) == 0 {
				t.Fatal("whiteroom-db check finds nothing in the damaged directory")
			}
			keep := filepath.Join(t.TempDir(), "keep")
			if existing {
				mkdir(t, keep)
			}
			logdbBefore, filesBefore := fileBytes(t, filepath.Join(s.dir, "logdb")), contents(t, filepath.Join(s.dir, "files"))
			rep, err := Repair(t.Context(), s.dir, s.opts, RepairOptions{Apply: true, Keep: keep})
			if err != nil || !rep.Applied || rep.Kept != keep+"/logdb" || len(rep.Holes) != 4 {
				t.Fatalf("Repair() = %+v, %v", rep, err)
			}
			if info, err := os.Stat(keep); err != nil || !info.IsDir() || (!existing && info.Mode().Perm() != 0o700) {
				t.Errorf("the directory of the copy is %v, %v", info, err)
			}
			if got := fileBytes(t, filepath.Join(keep, "logdb")); !maps.Equal(got, logdbBefore) {
				t.Errorf("the copy has %d files, want those of logdb/ as they were (%d), byte for byte", len(got), len(logdbBefore))
			}
			if after := contents(t, filepath.Join(s.dir, "files")); !maps.Equal(after, filesBefore) {
				t.Errorf("the repair changed files/:\n%v\nwas\n%v", after, filesBefore)
			}
			if after := problemLines(t, s.dir, s.opts); len(after) != 0 {
				t.Errorf("whiteroom-db check of the repaired directory finds %q, want nothing", after)
			}
			// The copy, with files/, is a data directory that shows the damage again.
			kd := filepath.Join(t.TempDir(), "kept")
			mkdir(t, kd)
			if err := os.CopyFS(filepath.Join(kd, "logdb"), os.DirFS(filepath.Join(keep, "logdb"))); err != nil {
				t.Fatal(err)
			}
			mkdir(t, filepath.Join(kd, "files"))
			if problems := problemLines(t, kd, s.opts); len(problems) == 0 {
				t.Error("whiteroom-db check of the copy finds nothing, want the damage again")
			}
			// The repaired directory is a store that opens.
			s.open()
			s.commit()
			if err := s.srv.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRepairKeepRefused(t *testing.T) {
	s := newAuditDir(t, 6)
	s.damageDir(3)
	before := contents(t, s.dir)
	root := t.TempDir()
	notEmpty := filepath.Join(root, "full")
	mkdir(t, notEmpty)
	if err := os.WriteFile(filepath.Join(notEmpty, "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	aFile := filepath.Join(root, "file")
	if err := os.WriteFile(aFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	noParent := filepath.Join(root, "missing", "keep")
	for _, tc := range []struct {
		name, keep, want string
		notExist         bool
	}{
		{"not empty", notEmpty, notEmpty + ": not empty; -keep needs a missing or empty directory", false},
		{"a file", aFile, aFile + ": not a directory; -keep needs a missing or empty directory", false},
		{"the data directory", s.dir, s.dir + ": -keep is the data directory: the copy of logdb/ must be kept apart from it", false},
		{"in the data directory", filepath.Join(s.dir, "keep"),
			filepath.Join(s.dir, "keep") + ": -keep is in the data directory: the copy of logdb/ must be kept apart from it", false},
		{"in logdb", filepath.Join(s.dir, "logdb", "log", "keep"),
			filepath.Join(s.dir, "logdb", "log", "keep") + ": -keep is in the data directory: the copy of logdb/ must be kept apart from it", false},
		{"without a parent", noParent, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rep, err := Repair(t.Context(), s.dir, s.opts, RepairOptions{Apply: true, Keep: tc.keep})
			switch {
			case tc.notExist && !errors.Is(err, fs.ErrNotExist):
				t.Errorf("Repair() = %v, want an error that wraps fs.ErrNotExist", err)
			case !tc.notExist && (err == nil || err.Error() != tc.want):
				t.Errorf("Repair() = %v, want %q", err, tc.want)
			}
			if rep.Applied || rep.Kept != "" {
				t.Errorf("Repair() went on: %+v", rep)
			}
			if after := contents(t, s.dir); !maps.Equal(after, before) {
				t.Errorf("the refusal changed the data directory:\n%v\nwas\n%v", after, before)
			}
			if tc.notExist {
				if _, err := os.Lstat(filepath.Dir(tc.keep)); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("the refusal made the parent: %v", err)
				}
			}
		})
	}
	if got := fileBytes(t, notEmpty); len(got) != 1 {
		t.Errorf("the refusal changed the directory that was not empty: %v", got)
	}

	// The directory of the copy is refused before the repair looks at the store, so that it is refused for a store that has nothing
	// to repair too.
	sound := newAuditDir(t, 4)
	if _, err := Repair(t.Context(), sound.dir, sound.opts, RepairOptions{Apply: true, Keep: notEmpty}); err == nil ||
		err.Error() != notEmpty+": not empty; -keep needs a missing or empty directory" {
		t.Errorf("Repair() of a store that has nothing to repair, with a directory that is not empty = %v", err)
	}

	// The directory of the copy is made only after every refusal of the repair, and a store that has nothing to repair makes none.
	for _, tc := range []struct {
		name  string
		s     func(t *testing.T) *auditStore
		r     RepairOptions
		isErr bool
	}{
		{"nothing to repair", func(t *testing.T) *auditStore { return newAuditDir(t, 4) }, RepairOptions{}, false},
		{"a truncation", func(t *testing.T) *auditStore {
			s := newAuditDir(t, 6)
			if err := os.Remove(filepath.Join(s.dir, "logdb", "checkpoint")); err != nil {
				t.Fatal(err)
			}
			s.damageDir(3)
			return s
		}, RepairOptions{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.s(t)
			keep := filepath.Join(t.TempDir(), "keep")
			tc.r.Apply, tc.r.Keep = true, keep
			before := contents(t, s.dir)
			rep, err := Repair(t.Context(), s.dir, s.opts, tc.r)
			if (err != nil) != tc.isErr || rep.Applied {
				t.Errorf("Repair() = %+v, %v", rep, err)
			}
			if _, err := os.Lstat(keep); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("the directory of the copy is made: %v", err)
			}
			if after := contents(t, s.dir); !maps.Equal(after, before) {
				t.Errorf("the data directory changed")
			}
		})
	}
}

// TestRepairKeepFails checks that a copy that fails removes what it made, and that the data directory is as it was.
func TestRepairKeepFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the superuser reads where it likes")
	}
	s := newAuditDir(t, 6)
	s.damageDir(3)
	// A file that the engine does not read, which the copy cannot.
	stray := filepath.Join(s.dir, "logdb", "objects", "ab")
	mkdir(t, stray)
	strayFile := filepath.Join(stray, strings.Repeat("ab", 32))
	if err := os.WriteFile(strayFile, []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	snapshot := func() map[string]string {
		t.Helper()
		if err := os.Chmod(strayFile, 0o600); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Chmod(strayFile, 0o000) }()
		return contents(t, s.dir)
	}
	before := snapshot()
	for _, existing := range []bool{false, true} {
		keep := filepath.Join(t.TempDir(), "keep")
		if existing {
			mkdir(t, keep)
		}
		rep, err := Repair(t.Context(), s.dir, s.opts, RepairOptions{Apply: true, Keep: keep})
		if err == nil || !errors.Is(err, fs.ErrPermission) || errors.Is(err, logdb.ErrFailed) || rep.Applied || rep.Kept != "" {
			t.Errorf("Repair() = %+v, %v, want a plain error of the copy", rep, err)
		}
		entries, err := os.ReadDir(keep)
		switch {
		case existing && (err != nil || len(entries) != 0):
			t.Errorf("the directory of the copy has %v (%v) after the failure, want it empty", entries, err)
		case !existing && !errors.Is(err, fs.ErrNotExist):
			t.Errorf("the directory of the copy that the repair made is there: %v", err)
		}
	}
	if after := snapshot(); !maps.Equal(after, before) {
		t.Errorf("the failed copy changed the data directory")
	}
	if _, err := os.Lstat(filepath.Join(s.dir, "stopped")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a failed copy left the mark: %v", err)
	}
}

// TestRepairKeepFailsKeepsOthers checks that a copy that fails removes only what it made: a logdb/ that is in the directory of
// the copy already, which another process made after the check that it was empty, is not the copy's to remove.
func TestRepairKeepFailsKeepsOthers(t *testing.T) {
	s := newAuditStore(t, 8)
	s.damage(4)
	keep := simfs.New("keep")
	// What the other process has copied so far: another store's copy.
	if err := keep.Mkdir("logdb", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := keep.Mkdir("logdb/log", 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := keep.OpenFile("logdb/log/0000000000000001-0000000000000001.log", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("the only copy of another store")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	before := worldFiles(keep)
	dataBefore := keptFiles(s.fs)
	rep, err := repair(t.Context(), s.fs, "data", s.opts, RepairOptions{Apply: true, Keep: "keep"}, keepOn(keep))
	if err == nil || rep.Applied || rep.Kept != "" {
		t.Fatalf("Repair() = %+v, %v, want the error of the copy in a logdb/ that is there", rep, err)
	}
	if after := worldFiles(keep); !maps.Equal(after, before) {
		t.Errorf("the failed copy removed files that it did not make: %v, was %v", slices.Sorted(maps.Keys(after)), slices.Sorted(maps.Keys(before)))
	}
	if after := keptFiles(s.fs); !maps.Equal(after, dataBefore) {
		t.Errorf("the failed copy changed logdb/ of the data directory")
	}
}

// TestRepairKeepDurable checks that the copy, which is the only pre-repair state once the repair has removed the old generation,
// is whole after a crash that loses every change that was not synced: its files, and the names of its files and directories.
func TestRepairKeepDurable(t *testing.T) {
	s := newAuditStore(t, 8)
	s.damage(4)
	before := keptFiles(s.fs)
	rep, keep := s.apply(RepairOptions{})
	if !rep.Applied || rep.Kept != "keep/logdb" {
		t.Fatalf("Repair() = %+v", rep)
	}
	if got := keptFiles(keep.Crash(loseAll)); !maps.Equal(got, before) {
		t.Errorf("after a crash that loses what is not synced the copy has %d files, want those of logdb/ as they were (%d)", len(got), len(before))
	}
}

// TestRepairKeepMadeDurable checks that the directory that the repair makes for the copy is there after a crash that loses what
// is not synced.
func TestRepairKeepMadeDurable(t *testing.T) {
	parent := simfs.New("top")
	if _, _, err := mkdirDurable(parent, "keep"); err != nil {
		t.Fatal(err)
	}
	if info, err := parent.Crash(loseAll).Lstat("keep"); err != nil || !info.IsDir() {
		t.Errorf("after a crash the directory is %v, %v", info, err)
	}
}

// TestRepairKeepDiscardKeepsOthers checks that a copy that fails removes the directory that it made only while it is that
// directory: what another process has put in its place is not the copy's to remove.
func TestRepairKeepDiscardKeepsOthers(t *testing.T) {
	for _, tc := range []struct {
		name  string
		other func(t *testing.T, w *keepWorld, keep string)
	}{
		{"a file", func(t *testing.T, w *keepWorld, keep string) {
			if err := os.Remove(keep); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(keep, []byte("another copy"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"another empty directory", func(t *testing.T, w *keepWorld, keep string) {
			if err := os.Rename(keep, w.path("old")); err != nil {
				t.Fatal(err)
			}
			mkdir(t, keep)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newKeepWorld(t)
			keep := w.path("keep")
			c, err := checkKeep(w.data, keep)
			if err != nil || c.parent == nil {
				t.Fatalf("checkKeep() = %+v, %v", c, err)
			}
			k, err := openKeep(w.info, keep, c)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = k.fs.Close() }()
			tc.other(t, w, keep)
			k.discard()
			if _, err := os.Lstat(keep); err != nil {
				t.Errorf("the failed copy removed what another process put in the place of its directory: %v", err)
			}
		})
	}
}

// TestRepairKeepIsData checks that a copy whose directory is the data directory fails without removing the store: the logdb/
// that its Mkdir meets is not the copy's.
func TestRepairKeepIsData(t *testing.T) {
	s := newAuditStore(t, 8)
	s.damage(4)
	before := keptFiles(s.fs)
	data := func() (*keepDir, error) { return &keepDir{fs: s.fs, name: "keep"}, nil }
	rep, err := repair(t.Context(), s.fs, "data", s.opts, RepairOptions{Apply: true, Keep: "keep"}, data)
	if err == nil || rep.Applied {
		t.Fatalf("Repair() = %+v, %v, want the error of the copy", rep, err)
	}
	if after := keptFiles(s.fs); !maps.Equal(after, before) {
		t.Errorf("logdb/ of the data directory has %d files, and had %d", len(after), len(before))
	}
}

// keepWorld is a data directory on the OS that has a logdb/ of a few files, and the directories that the tests make around it.
type keepWorld struct {
	top, data string
	root      vfs.FS      // the data directory
	info      fs.FileInfo // of the data directory
	before    map[string]string
}

func newKeepWorld(t *testing.T) *keepWorld {
	t.Helper()
	w := &keepWorld{top: t.TempDir()}
	w.data = filepath.Join(w.top, "data")
	for _, d := range []string{w.data, filepath.Join(w.data, "logdb"), filepath.Join(w.data, "logdb", "log"), filepath.Join(w.data, "sub")} {
		mkdir(t, d)
	}
	for name, data := range map[string]string{"logdb/log/1.log": "a segment", "logdb/checkpoint": "a checkpoint", "logdb/head": "a head"} {
		if err := os.WriteFile(filepath.Join(w.data, filepath.FromSlash(name)), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	if w.root, err = vfs.OpenRoot(w.data); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.root.Close() })
	if w.info, err = w.root.Lstat("."); err != nil {
		t.Fatal(err)
	}
	w.before = contents(t, w.data)
	return w
}

func (w *keepWorld) path(name string) string { return filepath.Join(w.top, name) }

func (w *keepWorld) link(t *testing.T, name, target string) string {
	t.Helper()
	if err := os.Symlink(target, w.path(name)); err != nil {
		t.Fatal(err)
	}
	return w.path(name)
}

// unchanged checks that the data directory is as it was.
func (w *keepWorld) unchanged(t *testing.T) {
	t.Helper()
	if after := contents(t, w.data); !maps.Equal(after, w.before) {
		t.Errorf("the data directory changed:\n%v\nwas\n%v", after, w.before)
	}
}

// TestRepairKeepSymlinks checks the directory of the copy that is, or is under, a symbolic link: the directory itself may not be
// one, which another process can point elsewhere between the check and its use, and the directories that hold it may.
func TestRepairKeepSymlinks(t *testing.T) {
	w := newKeepWorld(t)
	elsewhere := w.path("elsewhere")
	mkdir(t, elsewhere)
	empty := w.path("empty")
	mkdir(t, empty)
	toData, toSub, toEmpty := w.link(t, "to-data", w.data), w.link(t, "to-sub", filepath.Join(w.data, "sub")), w.link(t, "to-empty", empty)
	dangling, danglingInData := w.link(t, "dangling", w.path("nothing")), w.link(t, "dangling-in-data", filepath.Join(w.data, "missing"))
	parent := w.link(t, "parent", elsewhere)
	const dataWhat = ": -keep is the data directory: the copy of logdb/ must be kept apart from it"
	const inDataWhat = ": -keep is in the data directory: the copy of logdb/ must be kept apart from it"
	const linkWhat = ": a symbolic link; -keep needs a missing or empty directory itself, not a link to one"
	for _, tc := range []struct{ name, keep, want string }{
		{"a link to the data directory", toData, toData + dataWhat},
		{"a link into the data directory", toSub, toSub + inDataWhat},
		{"a missing directory under a link to the data directory", filepath.Join(toData, "new"), filepath.Join(toData, "new") + inDataWhat},
		{"a dangling link into the data directory", danglingInData, danglingInData + linkWhat},
		{"a link to an empty directory", toEmpty, toEmpty + linkWhat},
		{"a link to an empty directory, with a slash", toEmpty + "/", toEmpty + linkWhat},
		{"a link to nothing", dangling, dangling + linkWhat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := checkKeep(w.data, tc.keep)
			if err == nil || err.Error() != tc.want {
				t.Errorf("checkKeep() = %v, want %q", err, tc.want)
			}
			if k, err := osKeep(w.root, tc.keep)(); err == nil || err.Error() != tc.want {
				if err == nil {
					_ = k.fs.Close()
				}
				t.Errorf("osKeep()() = %v, want %q", err, tc.want)
			}
			w.unchanged(t)
			if entries, err := os.ReadDir(empty); err != nil || len(entries) != 0 {
				t.Errorf("the empty directory behind the link has %v (%v)", entries, err)
			}
		})
	}
	if _, err := os.Lstat(filepath.Join(w.data, "missing")); err == nil {
		t.Error("the dangling link made a directory in the data directory")
	}

	// The directories that hold the directory of the copy may be links: it is made, or taken, where they lead.
	t.Run("a missing directory under a link", func(t *testing.T) {
		keep := filepath.Join(parent, "new")
		k, err := osKeep(w.root, keep)()
		if err != nil {
			t.Fatalf("osKeep()() = %v", err)
		}
		if info, err := os.Lstat(filepath.Join(elsewhere, "new")); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Errorf("the directory that the repair made is %v, %v", info, err)
		}
		if err := k.fs.Close(); err != nil {
			t.Error(err)
		}
	})
	t.Run("an empty directory under a link", func(t *testing.T) {
		mkdir(t, filepath.Join(elsewhere, "ready"))
		k, err := osKeep(w.root, filepath.Join(parent, "ready"))()
		if err != nil {
			t.Fatalf("osKeep()() = %v", err)
		}
		if k.made != nil {
			t.Error("the repair made a directory that was there")
		}
		if err := k.fs.Close(); err != nil {
			t.Error(err)
		}
	})
	w.unchanged(t)
}

// TestRepairKeepChanged checks that the directory that is opened is the one that was checked: whatever the name leads to by then,
// the copy does not go into another directory, the data directory, or one that has something in it, and nothing is written.
func TestRepairKeepChanged(t *testing.T) {
	const changed = ": changed while the repair opened it: run the repair again"
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, w *keepWorld, keep string)
		want   func(w *keepWorld, keep string) string
	}{
		{"another empty directory", func(t *testing.T, w *keepWorld, keep string) {
			if err := os.Rename(keep, w.path("old")); err != nil {
				t.Fatal(err)
			}
			mkdir(t, keep)
		}, func(w *keepWorld, keep string) string { return keep + changed }},
		{"an empty directory in the data directory", func(t *testing.T, w *keepWorld, keep string) {
			if err := os.Remove(keep); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(w.data, "sub"), keep); err != nil {
				t.Fatal(err)
			}
		}, func(w *keepWorld, keep string) string { return keep + changed }},
		{"a link to an empty directory", func(t *testing.T, w *keepWorld, keep string) {
			other := w.path("other")
			mkdir(t, other)
			if err := os.Remove(keep); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(other, keep); err != nil {
				t.Fatal(err)
			}
		}, func(w *keepWorld, keep string) string { return keep + changed }},
		{"the data directory", func(t *testing.T, w *keepWorld, keep string) {
			if err := os.Remove(keep); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(w.data, keep); err != nil {
				t.Fatal(err)
			}
		}, func(w *keepWorld, keep string) string {
			return keep + ": -keep is the data directory: the copy of logdb/ must be kept apart from it"
		}},
		{"a file in it", func(t *testing.T, w *keepWorld, keep string) {
			if err := os.WriteFile(filepath.Join(keep, "x"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, func(w *keepWorld, keep string) string {
			return keep + ": not empty; -keep needs a missing or empty directory"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newKeepWorld(t)
			keep := w.path("keep")
			mkdir(t, keep)
			c, err := checkKeep(w.data, keep)
			if err != nil || c.info == nil {
				t.Fatalf("checkKeep() = %+v, %v", c, err)
			}
			tc.change(t, w, keep)
			k, err := openKeep(w.info, keep, c)
			if want := tc.want(w, keep); err == nil || err.Error() != want {
				if err == nil {
					_ = k.fs.Close()
				}
				t.Fatalf("openKeep() = %v, want %q", err, want)
			}
			w.unchanged(t)
			for _, name := range []string{"sub", "other", "old"} {
				if _, err := os.Stat(filepath.Join(w.path(name), "logdb")); err == nil {
					t.Errorf("a copy was started in %s", name)
				}
			}
		})
	}

	// A directory that was missing is made where the check looked, and nothing that is there instead is touched.
	t.Run("a missing directory that is there", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			make func(t *testing.T, w *keepWorld, keep string)
		}{
			{"a link to an empty directory", func(t *testing.T, w *keepWorld, keep string) {
				mkdir(t, w.path("target"))
				if err := os.Symlink(w.path("target"), keep); err != nil {
					t.Fatal(err)
				}
			}},
			{"a directory that has a file", func(t *testing.T, w *keepWorld, keep string) {
				mkdir(t, keep)
				if err := os.WriteFile(filepath.Join(keep, "x"), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				w := newKeepWorld(t)
				keep := w.path("keep")
				c, err := checkKeep(w.data, keep)
				if err != nil || c.info != nil || c.parent == nil {
					t.Fatalf("checkKeep() = %+v, %v", c, err)
				}
				tc.make(t, w, keep)
				before := contents(t, w.top)
				if k, err := openKeep(w.info, keep, c); err == nil {
					_ = k.fs.Close()
					t.Fatal("openKeep() takes what is there")
				}
				if after := contents(t, w.top); !maps.Equal(after, before) {
					t.Errorf("the refusal changed what is there:\n%v\nwas\n%v", after, before)
				}
			})
		}
	})

	// The directory that holds a missing one is the one that was checked, also if its name leads elsewhere by now.
	t.Run("another parent", func(t *testing.T) {
		for _, to := range []string{"other", "data"} {
			t.Run(to, func(t *testing.T) {
				w := newKeepWorld(t)
				mkdir(t, w.path("first"))
				mkdir(t, w.path("other"))
				parent := w.link(t, "parent", w.path("first"))
				keep := filepath.Join(parent, "keep")
				c, err := checkKeep(w.data, keep)
				if err != nil || c.info != nil {
					t.Fatalf("checkKeep() = %+v, %v", c, err)
				}
				target := w.path("other")
				if to == "data" {
					target = w.data
				}
				if err := os.Remove(parent); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, parent); err != nil {
					t.Fatal(err)
				}
				if k, err := openKeep(w.info, keep, c); err == nil || err.Error() != filepath.Dir(keep)+changed {
					if err == nil {
						_ = k.fs.Close()
					}
					t.Errorf("openKeep() = %v, want %q", err, filepath.Dir(keep)+changed)
				}
				for _, name := range []string{"first", "other"} {
					if entries, err := os.ReadDir(w.path(name)); err != nil || len(entries) != 0 {
						t.Errorf("%s has %v (%v) after the refusal", name, entries, err)
					}
				}
				w.unchanged(t)
			})
		}
	})
}

// TestRepairKeepRace checks, on the OS, that the directory of the copy is never the data directory while another process
// turns the name from an empty directory to a link to the data directory and back, and that a copy that is made and discarded
// in what it gets leaves the data directory as it was.
func TestRepairKeepRace(t *testing.T) {
	w := newKeepWorld(t)
	keep := w.path("keep")
	src, err := w.root.Sub("logdb")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			time.Sleep(20 * time.Microsecond)
			_ = os.Remove(keep)
			if i%2 == 0 {
				_ = os.Mkdir(keep, 0o700)
			} else {
				_ = os.Symlink(w.data, keep)
			}
		}
	}()
	opened, refused := 0, 0
	calls := 0
	for ; calls < 1500 || (opened < 20 && calls < 30000); calls++ {
		k, err := osKeep(w.root, keep)()
		if err != nil {
			refused++
			continue
		}
		opened++
		if info, err := k.fs.Lstat("."); err != nil || os.SameFile(info, w.info) {
			t.Errorf("the directory of the copy is the data directory (%v)", err)
			_ = k.fs.Close()
			continue
		}
		if opened <= 50 {
			// The directory may be removed under it by now: that fails the copy, which is not the point.
			_ = k.copyLogdb(t.Context(), src)
			k.discard()
		}
		_ = k.fs.Close()
	}
	close(stop)
	wg.Wait()
	t.Logf("%d calls: %d refused, %d opened", calls, refused, opened)
	if opened == 0 || refused == 0 {
		t.Errorf("%d calls were refused and %d opened a directory: want both", refused, opened)
	}
	w.unchanged(t)
}

// TestRepairUnrecordable checks that a hole whose record after it names no hash that can be read is told and not recorded: the
// repair writes no row and no event for it, and does not raise the schema.
func TestRepairUnrecordable(t *testing.T) {
	s := newFixtureStore(t)
	s.commit() // act1
	s.commit() // act2, login1
	s.commit() // act3
	// The activity record 4 has a prev that is no hash.
	bad := auditRecord(dbwire.LogActivity, 4, "sha256:not-a-hash")
	s.update(func(tx *logdb.Tx) {
		if err := errors.Join(tx.AppendEvent(bad), tx.AppendEvent(dbwire.AppendMark(nil, testBase)),
			tx.Put(dbwire.TableHeads, []byte(dbwire.LogActivity), []byte(fmt.Sprintf("audit head activity 4 sha256:%x", sha256.Sum256(bad))))); err != nil {
			s.t.Error(err)
		}
	})
	if err := s.srv.Close(); err != nil {
		t.Fatal(err)
	}
	s.damage(3)
	rep, _ := s.apply(RepairOptions{})
	if got := holeWords(rep); !slices.Equal(got, []string{"activity 3-3?"}) || rep.SchemaFrom != 0 || !rep.Applied {
		t.Fatalf("the holes are %q (schema from %d), want the hole of the record 3 that no row records", got, rep.SchemaFrom)
	}
	a := s.auditView()
	if len(a.holes[dbwire.LogActivity]) != 0 {
		t.Errorf("the rows of the holes are %v, want none", a.holes)
	}
	for _, e := range a.events {
		if _, ok := dbwire.ParseHole(e.Data); ok {
			t.Errorf("the event %d is an event of a hole", e.Number)
		}
	}
}

// TestRepairKeepsUnreadable checks that the events that are no records stay when a repair that is applied without the flag has
// something else to do, and that they go with it.
func TestRepairKeepsUnreadable(t *testing.T) {
	for _, drop := range []bool{false, true} {
		t.Run(fmt.Sprintf("drop %v", drop), func(t *testing.T) {
			s := newFixtureStore(t)
			for range 3 {
				s.commit()
			}
			s.forged()
			for range 3 {
				s.commit()
			}
			if err := s.srv.Close(); err != nil {
				t.Fatal(err)
			}
			s.damage(2) // the commit 2: activity 2 and login 1
			rep, _ := s.apply(RepairOptions{DropUnreadable: drop})
			if rep.UnreadableTotal != 2 || rep.Dropped != drop {
				t.Errorf("the report is %+v, want two unreadable events, dropped: %v", rep, drop)
			}
			unreadable := 0
			for _, e := range s.auditView().events {
				if classify(e.Data).unreadable {
					unreadable++
				}
			}
			if want := map[bool]int{false: 2, true: 0}[drop]; unreadable != want {
				t.Errorf("%d unreadable events are left, want %d", unreadable, want)
			}
			wantHoles := []string{"login 1-1", "activity 2-2"}
			if drop {
				wantHoles = append(wantHoles, "activity 4-4")
			}
			if got := holeWords(rep); !slices.Equal(got, wantHoles) {
				t.Errorf("the holes are %q, want %q", got, wantHoles)
			}
		})
	}
}

// TestRepairForeignCheckpoint checks that a checkpoint that is valid and of the generation, but whose commit is not the one that
// the log has, is a thing to repair, which the repair says as it says what Open would change.
func TestRepairForeignCheckpoint(t *testing.T) {
	a := newAuditStore(t, 5)
	b := newFixtureStore(t)
	b.salt = "other"
	for range 3 {
		b.commit()
	}
	if err := b.srv.Close(); err != nil {
		t.Fatal(err)
	}
	var other []byte
	b.fs.Walk(func(p string, data []byte) {
		if p == "logdb/checkpoint" {
			other = data
		}
	})
	f, err := a.fs.OpenFile("logdb/checkpoint", os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(other); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(f.Sync(), f.Close()); err != nil {
		t.Fatal(err)
	}
	shown, _, err := a.repair(RepairOptions{})
	if err != nil || shown.Plan.Checkpoint != "not this log's" || shown.Things() != 1 || len(shown.Plan.Recovery) != 1 ||
		shown.Plan.Recovery[0].Path != "data/logdb/checkpoint" || shown.Plan.Recovery[0].Offset != -1 {
		t.Fatalf("Repair() = %+v, %v, want the checkpoint that is not the log's as the one thing", shown, err)
	}
	rep, _ := a.apply(RepairOptions{})
	if !rep.Applied || rep.Result.Seq != 5 {
		t.Errorf("Repair(Apply) = %+v, want the state of the log, of the commit 5", rep)
	}
	if again, _, err := a.repair(RepairOptions{}); err != nil || again.Things() != 0 {
		t.Errorf("the second repair = %+v, %v", again, err)
	}
}

// TestRepairReadOnlyFS checks that the file system that a maintenance that only looks gives the engine refuses every write,
// and the one of a repair that is applied does not.
func TestRepairReadOnlyFS(t *testing.T) {
	for _, readOnly := range []bool{true, false} {
		t.Run(fmt.Sprintf("read only %v", readOnly), func(t *testing.T) {
			s := newAuditStore(t, 2)
			counted := s.fs.Counted()
			var wrote error
			err := maintainWith(t.Context(), s.fs, "data", s.opts, readOnly, func(ctx context.Context, fsys vfs.FS, entered func(),
				f func(struct{}) error) error {
				entered()
				file, err := fsys.OpenFile("probe", os.O_WRONLY|os.O_CREATE, 0o600)
				if err == nil {
					_ = file.Close()
					_ = fsys.Remove("probe")
				}
				wrote = err
				if err := fsys.Mkdir("probe-dir", 0o700); err == nil {
					_ = fsys.Remove("probe-dir")
				} else if wrote == nil {
					wrote = err
				}
				return f(struct{}{})
			}, func(struct{}) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			if readOnly != errors.Is(wrote, errFrozen) {
				t.Errorf("the engine's file system answers a write with %v, want errFrozen: %v", wrote, readOnly)
			}
			if readOnly && s.fs.Counted() != counted {
				t.Errorf("the maintenance that only looks changed the world: %d operations", s.fs.Counted()-counted)
			}
		})
	}
}

// TestRepairRandom repairs stores that are damaged in a few of their segments, with or without the checkpoint, and checks what
// the repair must leave whatever it dropped: each log is whole or passed by a hole that a row records, a second repair has
// nothing to do, and the writer goes on.
func TestRepairRandom(t *testing.T) {
	rnd := rand.New(rand.NewPCG(144, 70))
	const commits = 10
	refused, repaired, truncated := 0, 0, 0
	for i := range 200 {
		s := newAuditStore(t, commits)
		withCheckpoint := rnd.IntN(3) != 0
		if !withCheckpoint {
			s.withoutCheckpoint()
		}
		var damaged []int
		for range 1 + rnd.IntN(3) {
			damaged = append(damaged, 1+rnd.IntN(commits))
		}
		s.damage(damaged...)
		keep := simfs.New("keep")
		rep, err := repair(t.Context(), s.fs, "data", s.opts, RepairOptions{Apply: true, Keep: "keep", Truncate: true, DropUnreadable: true}, keepOn(keep))
		if err != nil {
			if strings.Contains(err.Error(), "no commit can be kept") {
				refused++
				continue
			}
			t.Fatalf("case %d (damaged %v, checkpoint %v): Repair() = %v", i, damaged, withCheckpoint, err)
		}
		repaired++
		if rep.Plan.Truncation != nil {
			truncated++
		}
		a := s.auditView()
		a.checkChains(t)
		if t.Failed() {
			t.Fatalf("case %d (damaged %v, checkpoint %v): the holes %q, the gaps %q, the plan %+v", i, damaged, withCheckpoint,
				holeWords(rep), gapWords(rep), rep.Plan)
		}
		again, _, err := s.repair(RepairOptions{})
		if err != nil || again.Things() != 0 || len(again.Holes) != 0 || len(again.Gaps) != 0 {
			t.Fatalf("case %d (damaged %v, checkpoint %v): the second repair = %+v, %v", i, damaged, withCheckpoint, again, err)
		}
		// The writer goes on from the state that was kept: its next record follows the head of each log, which is the last record
		// that is kept, or the end of a hole at the end of the log.
		s.act, s.login = s.act[:a.headN(dbwire.LogActivity)], s.login[:a.headN(dbwire.LogLogin)]
		s.reopen()
		s.commit()
		if err := s.srv.Close(); err != nil {
			t.Fatal(err)
		}
		s.auditView().checkChains(t)
		if t.Failed() {
			t.Fatalf("case %d (damaged %v, checkpoint %v): after the writer's commit, the holes %q", i, damaged, withCheckpoint, holeWords(rep))
		}
	}
	t.Logf("%d repaired (%d with a truncation), %d refused", repaired, truncated, refused)
	if repaired < 100 || truncated == 0 || refused == 0 {
		t.Errorf("the cases made %d repairs (%d with a truncation) and %d refusals: want a mix", repaired, truncated, refused)
	}
}

// headN returns the number of the record that the head of log names, 0 if the log has none.
func (a auditView) headN(log string) int {
	line, ok := a.heads[log]
	if !ok {
		return 0
	}
	_, n, _, valid := dbwire.ParseHeadLine([]byte(line))
	if !valid {
		return 0
	}
	return int(n)
}

// TestRepairWrongRowIsReplaced checks that a row of a hole at the end of a log that has the hash of another record than the
// head's records nothing: the repair makes the hole, and its row takes the place of that one.
func TestRepairWrongRowIsReplaced(t *testing.T) {
	s := newFixtureStore(t)
	for range 7 {
		s.commit()
	}
	// The commit 8 has a row for the hole of the record 8 of the activity log with the hash of the record 7.
	s.commitWith(func(tx *logdb.Tx) {
		wrong := dbwire.ChainHole{Last: 8, Hash: s.act[6].sum, At: testBase}
		if err := tx.Put(dbwire.TableChains, dbwire.ChainHoleKey(dbwire.LogActivity, 8), dbwire.AppendChainHole(nil, wrong)); err != nil {
			t.Error(err)
		}
	})
	if err := s.srv.Close(); err != nil {
		t.Fatal(err)
	}
	s.damage(8)
	rep, _ := s.apply(RepairOptions{})
	if want := []string{"login 4-4", "activity 8-8"}; !slices.Equal(holeWords(rep), want) {
		t.Fatalf("the holes are %q, want %q", holeWords(rep), want)
	}
	a := s.auditView()
	a.checkChains(t)
	if h := a.holes[dbwire.LogActivity][8]; h.Last != 8 || h.Hash != s.act[7].sum {
		t.Errorf("the row of the hole is %+v, want the hash of the record 8", h)
	}
}
