package store

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// The tests here repair data directories that the daemon of the harness has written and closed, damaged as the cases of the
// issue are, with whiteroom-db repair (dbserver.Repair): the stores have small segments, a checkpoint of the last commit that
// Close writes, an activity record in every commit and a login record in every fifth.

func repairLogOptions() *logdb.Options {
	return &logdb.Options{Placement: logdb.Hybrid, Threshold: 50, SegmentBytes: 1024, WriterWait: writerWait, SyncCheckpoints: true}
}

func repairOptions() options {
	return options{log: repairLogOptions(), now: func() time.Time { return testTime }}
}

func repairPut(t *testing.T, s *Store, kind content.RefKind, id string, data []byte) {
	t.Helper()
	update(t, s, func(tx *Tx) error {
		_, err := tx.Put(kind, id, data, userID)
		return err
	})
}

// repairStore makes a store of many small segments, whose server is open.
func repairStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	s, err := create(dir, repairOptions())
	if err != nil {
		t.Fatal(err)
	}
	fillChecked(t, s)
	for i := range 20 {
		repairPut(t, s, content.RefTerm, termID, jsonf(`{"schemaVersion": 1, "id": %q, "slug": "s%d", "parent": %q}`, termID, i, term2))
		if i%5 == 0 {
			update(t, s, func(tx *Tx) error { return tx.Record(loginRecord(audit.LoginFailure, fmt.Sprint(i))) })
		}
	}
	return s, dir
}

// closedRepairStore is repairStore with the server closed.
func closedRepairStore(t *testing.T) string {
	t.Helper()
	s, dir := repairStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func repairSegments(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "logdb", "log", "*.log"))
	if err != nil || len(m) == 0 {
		t.Fatalf("the segments are %v (%v)", m, err)
	}
	slices.Sort(m)
	return m
}

func repairCopy(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "data")
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	return dst
}

// flipRecord flips a bit in the body of the first record of the segment p.
func flipRecord(t *testing.T, p string) {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	b[64+8+12] ^= 1
	writeFile(t, p, b)
}

// repairBytes returns the regular files under dir, with what they hold.
func repairBytes(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		m[filepath.ToSlash(rel)] = string(b)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return m
}

// engineProblems is what whiteroom-db check finds in the directory dir.
func engineProblems(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := dbserver.Inspect(t.Context(), dir, dbserver.Options{Log: *repairLogOptions()}, func(p dbserver.Problem) bool {
		out = append(out, strings.ReplaceAll(p.String(), dir, "DATA"))
		return true
	})
	if err != nil {
		t.Fatalf("Inspect() = %v", err)
	}
	return out
}

// doctorOf is what whiteroom doctor finds in the store s of dir, with the notes of the audit logs: the problems of Check, and
// the error of VerifyAudit, by the anchors, if it has one.
func doctorOf(t *testing.T, s *Store, dir string, anchors []audit.Head) (problems, notes []string) {
	t.Helper()
	if err := s.Check(t.Context(), func(p Problem) bool {
		problems = append(problems, strings.ReplaceAll(p.String(), dir, "DATA"))
		return true
	}); err != nil {
		t.Fatalf("Check() = %v", err)
	}
	if err := s.VerifyAuditNotes(t.Context(), anchors, func(n string) { notes = append(notes, n) }); err != nil {
		problems = append(problems, "audit: "+strings.ReplaceAll(err.Error(), dir, "DATA"))
	}
	return problems, notes
}

// repairOf shows what the repair of dir does, and changes nothing, and then repairs dir with a copy of logdb/ in a new directory,
// which it returns with the report of the repair.
func repairOf(t *testing.T, dir string, r dbserver.RepairOptions) (rep dbserver.RepairReport, keep string, before map[string]string) {
	t.Helper()
	before = repairBytes(t, dir)
	o := dbserver.Options{Log: *repairLogOptions()}
	shown, err := dbserver.Repair(t.Context(), dir, o, dbserver.RepairOptions{Truncate: r.Truncate, DropUnreadable: r.DropUnreadable})
	if err != nil {
		t.Fatalf("Repair() = %v", err)
	}
	if shown.Applied || !maps.Equal(repairBytes(t, dir), before) {
		t.Fatalf("the repair that is shown applied, or changed the files: %+v", shown)
	}
	keep = filepath.Join(t.TempDir(), "keep")
	r.Apply, r.Keep, r.Now = true, keep, chainTime
	if rep, err = dbserver.Repair(t.Context(), dir, o, r); err != nil {
		t.Fatalf("Repair(Apply) = %v", err)
	}
	if !rep.Applied || len(rep.Holes) != len(shown.Holes) || rep.Things() != shown.Things() {
		t.Errorf("the repair applied is %+v, shown %+v", rep, shown)
	}
	// The copy has the files of logdb/ as they were, and with the files/ of the data directory it is a directory that shows the
	// damage again.
	want := map[string]string{}
	for name, data := range before {
		if strings.HasPrefix(name, "logdb/") {
			want[strings.TrimPrefix(name, "logdb/")] = data
		}
	}
	if got := repairBytes(t, filepath.Join(keep, "logdb")); !maps.Equal(got, want) {
		t.Errorf("the copy has %d files, want those of logdb/ as they were (%d), byte for byte", len(got), len(want))
	}
	return rep, keep, before
}

// checkKept checks that the copy of the files of logdb/ in keep and the files/ of dir show the damage that the repair has
// mended.
func checkKept(t *testing.T, dir, keep string) {
	t.Helper()
	kd := filepath.Join(t.TempDir(), "kept")
	if err := os.CopyFS(filepath.Join(kd, "logdb"), os.DirFS(filepath.Join(keep, "logdb"))); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(filepath.Join(kd, "files"), os.DirFS(filepath.Join(dir, "files"))); err != nil {
		t.Fatal(err)
	}
	if got := engineProblems(t, kd); len(got) == 0 {
		t.Error("whiteroom-db check of the copy and the files finds nothing, want the damage again")
	}
}

// holeNotes returns the notes of VerifyAudit that tell a hole.
func holeNotes(notes []string) []string {
	var out []string
	for _, n := range notes {
		if strings.Contains(n, "dropped by whiteroom-db repair") {
			out = append(out, n)
		}
	}
	return out
}

// mendedOpen checks the store that a repair has left in dir: whiteroom-db check finds nothing, a second repair has nothing to do,
// the store opens, and the notes of the audit logs that tell a hole are wantHoles. It returns the store, which is open, the notes
// of the holes, and what whiteroom doctor finds.
func mendedOpen(t *testing.T, dir string, wantHoles int) (s *Store, notes, problems []string) {
	t.Helper()
	if got := engineProblems(t, dir); len(got) != 0 {
		t.Errorf("whiteroom-db check of the repaired directory finds %q, want nothing", got)
	}
	again, err := dbserver.Repair(t.Context(), dir, dbserver.Options{Log: *repairLogOptions()}, dbserver.RepairOptions{})
	if err != nil || again.Things() != 0 || len(again.Holes) != 0 {
		t.Errorf("the second repair = %+v, %v, want nothing to repair", again, err)
	}
	s, err = open(dir, repairOptions())
	if err != nil {
		t.Fatalf("open() of the repaired directory = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	problems, all := doctorOf(t, s, dir, nil)
	if got := holeNotes(all); len(got) != wantHoles {
		t.Errorf("the notes of the holes are %q, want %d of them", got, wantHoles)
	}
	return s, holeNotes(all), problems
}

// mended is mendedOpen for a store that whiteroom doctor finds nothing in.
func mended(t *testing.T, dir string, wantHoles int) (*Store, []string) {
	t.Helper()
	s, notes, problems := mendedOpen(t, dir, wantHoles)
	if len(problems) != 0 {
		t.Errorf("whiteroom doctor finds %q after the repair, want nothing", problems)
	}
	return s, notes
}

func TestRepairRecordBeforeCheckpoint(t *testing.T) {
	dir := closedRepairStore(t)
	segs := repairSegments(t, dir)
	if len(segs) != 27 {
		t.Fatalf("the store has %d segments, want 27", len(segs))
	}
	// The segment 1 has the first record of the activity log, store.create, and the last has the last record that the store has.
	for _, tc := range []struct {
		name string
		seg  int
		want string // the hole, and the note that tells it
	}{
		{"the first segment", 0, "the log is checked again from record 2"},
		{"a segment in the middle", len(segs) / 2, "the log is checked again from record 13"},
		{"the last segment", len(segs) - 1, "the log ends with record 22"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := repairCopy(t, dir)
			flipRecord(t, repairSegments(t, d)[tc.seg])
			if len(engineProblems(t, d)) == 0 {
				t.Fatal("whiteroom-db check finds nothing in the damaged directory")
			}
			rep, keep, _ := repairOf(t, d, dbserver.RepairOptions{})
			if len(rep.Holes) != 1 || rep.Holes[0].Log != dbwire.LogActivity || rep.Holes[0].First != rep.Holes[0].Last || !rep.Holes[0].Row ||
				len(rep.Gaps) != 0 || len(rep.Plan.Spans) != 1 || rep.Plan.Truncation != nil {
				t.Errorf("the repair = %+v, want the one span, and the hole of one record of the activity log", rep)
			}
			_, notes := mended(t, d, 1)
			if len(notes) != 1 || !strings.HasSuffix(notes[0], tc.want) {
				t.Errorf("the notes are %q, want one that ends with %q", notes, tc.want)
			}
			checkKept(t, d, keep)
		})
	}
}

// checkpointed makes a store that has commits after its checkpoint, as a backup copy of a running store has them, and returns
// the data directory of the copy and the heads of its audit logs that were kept outside the store.
func checkpointed(t *testing.T) (dir string, heads []audit.Head) {
	t.Helper()
	s, src := repairStore(t)
	if err := s.Close(); err != nil { // the checkpoint of this commit
		t.Fatal(err)
	}
	s, err := open(src, repairOptions())
	if err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		repairPut(t, s, content.RefTerm, term2, jsonf(`{"schemaVersion": 1, "id": %q, "slug": "after%d"}`, term2, i))
	}
	dir = filepath.Join(t.TempDir(), "copy")
	if heads, err = backup(t.Context(), src, nil, dir, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return dir, heads
}

func TestRepairRecordAfterCheckpoint(t *testing.T) {
	cp, heads := checkpointed(t)
	segs := repairSegments(t, cp)

	t.Run("a record that is not the last", func(t *testing.T) {
		d := repairCopy(t, cp)
		flipRecord(t, repairSegments(t, d)[len(segs)-4])
		// Without -truncate the repair refuses, and changes nothing.
		before := repairBytes(t, d)
		keep := filepath.Join(t.TempDir(), "keep")
		_, err := dbserver.Repair(t.Context(), d, dbserver.Options{Log: *repairLogOptions()}, dbserver.RepairOptions{Apply: true, Keep: keep})
		if err == nil || !strings.Contains(err.Error(), "repair with -truncate to drop them, or restore the store from a copy") {
			t.Fatalf("Repair() without -truncate = %v, want the refusal", err)
		}
		if _, err := os.Lstat(keep); !errors.Is(err, fs.ErrNotExist) || !maps.Equal(repairBytes(t, d), before) {
			t.Errorf("the refusal made the copy (%v) or changed the files", err)
		}
		rep, keep, _ := repairOf(t, d, dbserver.RepairOptions{Truncate: true})
		if rep.Plan.Truncation == nil || len(rep.Holes) != 0 {
			t.Errorf("the repair = %+v, want the commits after the damage dropped, and no hole", rep)
		}
		s, _ := mended(t, d, 0)
		// The heads that were kept outside the store name records that the repair has dropped: that stays a problem.
		if err := s.VerifyAudit(t.Context(), heads); err == nil || !strings.Contains(err.Error(), "head kept outside the store") {
			t.Errorf("VerifyAudit() with the heads of the copy = %v, want the anchors of the records that were dropped", err)
		}
		checkKept(t, d, keep)
	})

	t.Run("the last record", func(t *testing.T) {
		d := repairCopy(t, cp)
		flipRecord(t, repairSegments(t, d)[len(segs)-1])
		rep, keep, _ := repairOf(t, d, dbserver.RepairOptions{})
		if len(rep.Plan.Recovery) == 0 || rep.Plan.Truncation != nil || len(rep.Holes) != 0 {
			t.Errorf("the repair = %+v, want the torn tail that Open would cut off, and nothing dropped that can be read", rep)
		}
		s, _ := mended(t, d, 0)
		if err := s.VerifyAudit(t.Context(), heads); err == nil || !strings.Contains(err.Error(), "head kept outside the store") {
			t.Errorf("VerifyAudit() with the heads of the copy = %v, want the anchors of the last commit", err)
		}
		checkKept(t, d, keep)
	})

	t.Run("no checkpoint", func(t *testing.T) {
		d := repairCopy(t, cp)
		if err := os.Remove(filepath.Join(d, "logdb", "checkpoint")); err != nil {
			t.Fatal(err)
		}
		flipRecord(t, repairSegments(t, d)[1])
		rep, keep, _ := repairOf(t, d, dbserver.RepairOptions{Truncate: true})
		if tr := rep.Plan.Truncation; tr == nil || tr.LastSeq-tr.FirstSeq < 20 || len(rep.Holes) != 0 {
			t.Errorf("the repair = %+v, want most of the commits dropped, which came after the damage", rep.Plan.Truncation)
		}
		mended(t, d, 0)
		checkKept(t, d, keep)
	})

	t.Run("a checkpoint that is damaged", func(t *testing.T) {
		d := repairCopy(t, cp)
		b, err := os.ReadFile(filepath.Join(d, "logdb", "checkpoint"))
		if err != nil {
			t.Fatal(err)
		}
		b[100] ^= 1
		writeFile(t, filepath.Join(d, "logdb", "checkpoint"), b)
		was := repairCopy(t, d)
		sd, err := open(was, repairOptions())
		if err != nil {
			t.Fatal(err)
		}
		events := allEvents(t, sd)
		if err := sd.Close(); err != nil {
			t.Fatal(err)
		}
		rep, keep, _ := repairOf(t, d, dbserver.RepairOptions{})
		if len(rep.Plan.Recovery) == 0 || !rep.Plan.Empty() || len(rep.Holes) != 0 {
			t.Errorf("the repair = %+v, want the checkpoint that Open does not use, and nothing dropped", rep)
		}
		s, _ := mended(t, d, 0)
		if after := allEvents(t, s); !eventsSame(events, after) {
			t.Errorf("the repair changed the events of the store")
		}
		if err := s.VerifyAudit(t.Context(), heads); err != nil {
			t.Errorf("VerifyAudit() with the heads of the copy = %v", err)
		}
		checkKept(t, d, keep)
	})
}

// objectFile returns an object file of the data directory dir.
func objectFile(t *testing.T, dir string) string {
	t.Helper()
	var obj string
	_ = filepath.WalkDir(filepath.Join(dir, "logdb", "objects"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && obj == "" {
			obj = p
		}
		return err
	})
	if obj == "" {
		t.Fatal("no object file")
	}
	return obj
}

func TestRepairObjects(t *testing.T) {
	dir := closedRepairStore(t)
	for _, how := range []string{"flip", "truncate", "remove"} {
		t.Run(how, func(t *testing.T) {
			d := repairCopy(t, dir)
			obj := objectFile(t, d)
			b, err := os.ReadFile(obj)
			if err != nil {
				t.Fatal(err)
			}
			switch how {
			case "flip":
				b[len(b)-1] ^= 1
				writeFile(t, obj, b)
			case "truncate":
				writeFile(t, obj, b[:len(b)-1])
			case "remove":
				if err := os.Remove(obj); err != nil {
					t.Fatal(err)
				}
			}
			if len(engineProblems(t, d)) != 1 {
				t.Fatalf("whiteroom-db check finds %q, want one problem", engineProblems(t, d))
			}
			rep, keep, _ := repairOf(t, d, dbserver.RepairOptions{})
			if len(rep.Plan.Objects) != 1 || len(rep.Plan.Spans) != 0 || len(rep.Holes) != 0 {
				t.Errorf("the repair = %+v, want one object dropped", rep.Plan)
			}
			// What is left are the rows that point at the dropped object, which the repair leaves to the content model.
			_, _, problems := mendedOpen(t, d, 0)
			if len(problems) != 2 || !strings.HasPrefix(problems[0], "index: ") || !strings.HasPrefix(problems[1], "history: ") ||
				!strings.Contains(problems[0], "is missing") || !strings.Contains(problems[1], "is missing") {
				t.Errorf("whiteroom doctor finds %q, want the rows of the index and of the history that point at the dropped object", problems)
			}
			checkKept(t, d, keep)
		})
	}
}

func TestRepairInlineObject(t *testing.T) {
	o := options{log: &logdb.Options{Placement: logdb.Inline, SegmentBytes: 1024, WriterWait: writerWait, SyncCheckpoints: true},
		now: func() time.Time { return testTime }}
	dir := filepath.Join(t.TempDir(), "data")
	s, err := create(dir, o)
	if err != nil {
		t.Fatal(err)
	}
	fillChecked(t, s)
	repairPut(t, s, content.RefTerm, termID, jsonf(`{"schemaVersion": 1, "id": %q, "slug": "x"}`, termID))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	want := docJSON(content.RefMedia, medium2)
	found := false
	for _, p := range repairSegments(t, dir) {
		b, _ := os.ReadFile(p)
		if i := bytes.Index(b, want); i >= 0 {
			b[i+len(want)-3] ^= 1
			writeFile(t, p, b)
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no inline object")
	}
	if len(engineProblems(t, dir)) == 0 {
		t.Fatal("whiteroom-db check finds nothing")
	}
	opts := dbserver.Options{Log: *o.log}
	keep := filepath.Join(t.TempDir(), "keep")
	rep, err := dbserver.Repair(t.Context(), dir, opts, dbserver.RepairOptions{Apply: true, Keep: keep})
	if err != nil || !rep.Applied || len(rep.Plan.Spans) == 0 || len(rep.Plan.Objects) == 0 {
		t.Fatalf("Repair() = %+v, %v, want the record and its inline object dropped", rep, err)
	}
	if got := engineProblems(t, dir); len(got) != 0 {
		t.Errorf("whiteroom-db check of the repaired directory finds %q", got)
	}
	s, err = open(dir, o)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	problems, notes := doctorOf(t, s, dir, nil)
	if len(problems) != 1 || !strings.HasPrefix(problems[0], "history: ") || !strings.Contains(problems[0], "is missing") || len(holeNotes(notes)) != 1 {
		t.Errorf("whiteroom doctor finds %q, with the notes %q, want the row of the history that points at the dropped object", problems, notes)
	}
}

func TestRepairOriginals(t *testing.T) {
	s, dir := repairStore(t)
	putOriginal(t, s, []byte("y"))
	repairPut(t, s, content.RefMedia, mediaID, jsonf(`{"schemaVersion": 1, "id": %q, "hash": %q, "mimeType": "image/jpeg"}`, mediaID, sum([]byte("y"))))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(originalPath(dir, sum([]byte("y")))); err != nil {
		t.Fatal(err)
	}
	if got := engineProblems(t, dir); len(got) != 0 {
		t.Fatalf("whiteroom-db check finds %q: it does not look for an original that is missing", got)
	}
	s, err := open(dir, repairOptions())
	if err != nil {
		t.Fatal(err)
	}
	was, _ := doctorOf(t, s, dir, nil)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if len(was) != 1 || !strings.Contains(was[0], "original") {
		t.Fatalf("whiteroom doctor finds %q, want the original that is missing", was)
	}
	before := repairBytes(t, dir)
	rep, err := dbserver.Repair(t.Context(), dir, dbserver.Options{Log: *repairLogOptions()},
		dbserver.RepairOptions{Apply: true, Keep: filepath.Join(t.TempDir(), "keep")})
	if err != nil || rep.Things() != 0 || rep.Applied || rep.Kept != "" {
		t.Errorf("Repair() = %+v, %v, want nothing done: the daemon has no copy of the original to restore", rep, err)
	}
	if !maps.Equal(repairBytes(t, dir), before) {
		t.Error("the repair changed the data directory")
	}
	s, err = open(dir, repairOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if after, _ := doctorOf(t, s, dir, nil); !slices.Equal(after, was) {
		t.Errorf("whiteroom doctor finds %q after the repair, was %q", after, was)
	}
}

// forgedRecord commits, through the writer, a record of log that audit.Decode refuses but that chains (n and prev), as the next
// record of the log, and moves the head to it.
func forgedRecord(t *testing.T, s *Store, log audit.Log) uint64 {
	t.Helper()
	h, ok := headOf(t, s, log)
	if !ok {
		t.Fatal("no head")
	}
	var last []byte
	for _, e := range rawEvents(t, s) {
		if l, n, _, ok := dbwire.ReadRecordID(e.Data); ok && l == string(log) && n == h.N {
			last = e.Data
		}
	}
	r, err := audit.Decode(last)
	if err != nil {
		t.Fatal(err)
	}
	r.N, r.Prev = h.N+1, hashText(h.Hash[:])
	enc, err := audit.Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	forged := append([]byte(`{"extra":"1",`), enc[1:]...)
	replaceHead(t, s, log, h.N+1, sha256.Sum256(forged), forged)
	return h.N + 1
}

func TestRepairForgedRecord(t *testing.T) {
	for _, log := range []audit.Log{audit.ActivityLog, audit.LoginLog} {
		t.Run(string(log), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			s, err := create(dir, options{log: &logdb.Options{Placement: logdb.Hybrid}, now: func() time.Time { return chainTime }})
			if err != nil {
				t.Fatal(err)
			}
			for i := range 4 {
				update(t, s, func(tx *Tx) error {
					if err := tx.Record(eventRecord(fmt.Sprint(i))); err != nil {
						return err
					}
					return tx.Record(loginRecord(audit.LoginFailure, fmt.Sprint(i)))
				})
			}
			n := forgedRecord(t, s, log)
			for i := range 3 {
				update(t, s, func(tx *Tx) error {
					if err := tx.Record(eventRecord(fmt.Sprint("after", i))); err != nil {
						return err
					}
					return tx.Record(loginRecord(audit.LoginFailure, fmt.Sprint("after", i)))
				})
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			opts := dbserver.Options{Log: logdb.Options{Placement: logdb.Hybrid}}
			s, err = open(dir, options{log: &opts.Log, now: func() time.Time { return chainTime }})
			if err != nil {
				t.Fatal(err)
			}
			err = s.VerifyAudit(t.Context(), nil)
			if err == nil || !strings.Contains(err.Error(), "not a record of the audit log") {
				t.Fatalf("VerifyAudit() = %v, want the stop at the event that is no record", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			// Without the flag the event is kept, and the repair does nothing; the gap is told, and doctor goes on reporting it.
			rep, err := dbserver.Repair(t.Context(), dir, opts, dbserver.RepairOptions{})
			if err != nil || rep.Things() != 0 || rep.UnreadableTotal != 1 || len(rep.Holes) != 0 || len(rep.Gaps) != 1 {
				t.Fatalf("Repair() = %+v, %v, want the unreadable event kept and the gap that stays", rep, err)
			}
			keep := filepath.Join(t.TempDir(), "keep")
			rep, err = dbserver.Repair(t.Context(), dir, opts, dbserver.RepairOptions{Apply: true, Keep: keep, DropUnreadable: true, Now: chainTime})
			if err != nil || !rep.Applied || len(rep.Holes) != 1 || rep.Holes[0].First != n || rep.Holes[0].Last != n || !rep.Holes[0].Row {
				t.Fatalf("Repair(DropUnreadable) = %+v, %v, want the hole of the record %d", rep, err, n)
			}
			s, err = open(dir, options{log: &opts.Log, now: func() time.Time { return chainTime }})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			var notes []string
			if err := s.VerifyAuditNotes(t.Context(), nil, func(n string) { notes = append(notes, n) }); err != nil {
				t.Errorf("VerifyAudit() after the repair = %v", err)
			}
			want := fmt.Sprintf(`audit log %q: record %d was lost and dropped by whiteroom-db repair at 2026-11-20T10:20:30Z; the log is checked again from record %d`,
				log, n, n+1)
			if got := holeNotes(notes); len(got) != 1 || got[0] != want {
				t.Errorf("the notes are %q, want %q", got, want)
			}
			if problems, _ := doctorOf(t, s, dir, nil); len(problems) != 0 {
				t.Errorf("whiteroom doctor finds %q", problems)
			}
			// The events are those that the repair kept, and the events of the hole and its mark.
			for _, e := range allEvents(t, s) {
				if c, _, _, ok := dbwire.ReadRecordID(e.Data); !dbwire.IsDaemonEvent(e.Data) && !ok {
					t.Errorf("event %d is no record (%v) after the repair", e.Number, c)
				}
			}
		})
	}
}

// TestRepairGapIsNotTheRepairs checks that a record that was lost before the repair stays a gap that whiteroom doctor reports:
// the repair tells it, with the command that accepts it, and writes no row for it; once whiteroom-db accept-break has accepted it,
// whiteroom doctor finds nothing and tells the gap in a note, and a second repair has no gap to tell.
func TestRepairGapIsNotTheRepairs(t *testing.T) {
	s, dir, heads := auditFixture(t)
	// Position 4 is activity record 3.
	s = closeAndRewrite(t, s, dir, rewriteRecords(nil, []uint64{4}, nil))
	was, _ := doctorOf(t, s, dir, heads)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if len(was) == 0 || !strings.Contains(was[0], "the number does not follow that of record 2") {
		t.Fatalf("whiteroom doctor finds %q, want the gap", was)
	}
	opts := dbserver.Options{Log: logdb.Options{Placement: logdb.Hybrid}}
	rep, err := dbserver.Repair(t.Context(), dir, opts, dbserver.RepairOptions{Apply: true, Keep: filepath.Join(t.TempDir(), "keep")})
	if err != nil || rep.Things() != 0 || rep.Applied || len(rep.Holes) != 0 || len(rep.Gaps) != 1 || rep.Gaps[0].First != 3 || rep.Gaps[0].Last != 3 {
		t.Fatalf("Repair() = %+v, %v, want the gap of the record 3 told, and nothing done", rep, err)
	}
	s = reopenLog(t, dir, logdb.Options{})
	if after, _ := doctorOf(t, s, dir, heads); !slices.Equal(after, was) {
		t.Errorf("whiteroom doctor finds %q after the repair, was %q", after, was)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// whiteroom-db accept-break -log activity -record 4 accepts the gap.
	acc, err := acceptOf(t, dir, audit.ActivityLog, 4)
	if want := (dbserver.Acceptance{Gap: true, First: 3, Last: 3}); err != nil || acc != want {
		t.Fatalf("AcceptBreak(activity, 4) = %+v, %v, want %+v", acc, err, want)
	}
	s = reopenLog(t, dir, logdb.Options{})
	problems, notes := doctorOf(t, s, dir, heads)
	if len(problems) != 0 {
		t.Errorf("whiteroom doctor finds %q after the acceptance", problems)
	}
	if want := missingNote("activity", 3, 3, againFrom(4)); !slices.Contains(notes, want) {
		t.Errorf("the notes are %q, want %q among them", notes, want)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if again, err := dbserver.Repair(t.Context(), dir, opts, dbserver.RepairOptions{}); err != nil || again.Things() != 0 || len(again.Holes) != 0 ||
		len(again.Gaps) != 0 {
		t.Errorf("the second repair = %+v, %v, want nothing to repair and no gap to tell", again, err)
	}
}

// TestRepairHoleAtTheEnd checks that a hole at the end of the activity log, which the repair makes when the last record of the
// store is damaged after it was closed, is passed after the writer's next commits, with the note of the hole.
func TestRepairHoleAtTheEnd(t *testing.T) {
	dir := closedRepairStore(t)
	segs := repairSegments(t, dir)
	flipRecord(t, segs[len(segs)-1])
	rep, keep, _ := repairOf(t, dir, dbserver.RepairOptions{})
	if len(rep.Holes) != 1 || rep.Holes[0].Log != dbwire.LogActivity || rep.Holes[0].First != 23 || rep.Holes[0].Last != 23 {
		t.Fatalf("the repair made the holes %+v, want the last record of the activity log, which is the record 23", rep.Holes)
	}
	s, notes := mended(t, dir, 1)
	if want := `audit log "activity": record 23 was lost and dropped by whiteroom-db repair at 2026-11-20T10:20:30Z; the log ends with record 22`; len(notes) != 1 || notes[0] != want {
		t.Errorf("the notes are %q, want %q", notes, want)
	}
	// The writer commits again: the hole is then in the middle, and still passed.
	for i := range 3 {
		repairPut(t, s, content.RefTerm, termID, jsonf(`{"schemaVersion": 1, "id": %q, "slug": "more%d"}`, termID, i))
	}
	problems, notes := doctorOf(t, s, dir, nil)
	if len(problems) != 0 {
		t.Errorf("whiteroom doctor finds %q after the writer has committed", problems)
	}
	if want := `audit log "activity": record 23 was lost and dropped by whiteroom-db repair at 2026-11-20T10:20:30Z; the log is checked again from record 24`; len(holeNotes(notes)) != 1 || holeNotes(notes)[0] != want {
		t.Errorf("the notes are %q, want %q: the hole at the end is in the middle now", notes, want)
	}
	checkKept(t, dir, keep)
}

// TestRepairHoleBetweenLeftoversAndChainStart checks a repair of a deletion that stopped before its compaction when the record
// before the chain start is unreadable: the hole is recorded, whiteroom doctor finds nothing, the note of the records that are
// still there names the last that is, and the next compact drops them, as VerifyAudit passes them. It could not before, and
// stopped at the record after the hole for ever.
func TestRepairHoleBetweenLeftoversAndChainStart(t *testing.T) {
	months := []int{40, 40, 30, 30, 14, 13, 11, 2, 0}
	login := []bool{true, false, true, false, true, true, true, true, false}
	s, dir, _ := agedStore(t, months, login)
	loginBefore := recordsOf(t, s, audit.LoginLog)
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
	// The records 1 to 4 are left, the chain starts at the record 5, and the record 4 is unreadable.
	s = reopenLog(t, dir, logdb.Options{})
	e4 := recordEvent(t, s, audit.LoginLog, 4)
	s = closeAndRewrite(t, s, dir, rewriteEvents(map[uint64][]byte{e4.Number: []byte("not a record")}, nil, nil))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	opts := dbserver.Options{Log: logdb.Options{Placement: logdb.Hybrid}}
	rep, err := dbserver.Repair(t.Context(), dir, opts, dbserver.RepairOptions{Apply: true, Keep: filepath.Join(t.TempDir(), "keep"),
		DropUnreadable: true, Now: chainTime})
	if err != nil || !rep.Applied || len(rep.Holes) != 1 || rep.Holes[0].Log != dbwire.LogLogin || rep.Holes[0].First != 4 ||
		rep.Holes[0].Last != 4 || !rep.Holes[0].Row {
		t.Fatalf("Repair() = %+v, %v, want the hole of the record 4, recorded", rep, err)
	}
	s = reopenLog(t, dir, logdb.Options{})
	problems, notes := doctorOf(t, s, dir, nil)
	wantNotes := []string{startNote(5), lostNote("login", 4, 4, againFrom(5)), leftNote("the records 1 to 3 are", 5)}
	if len(problems) != 0 || !slices.Equal(notes, wantNotes) {
		t.Errorf("whiteroom doctor finds %q with the notes %q, want nothing, with the notes %q", problems, notes, wantNotes)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	d, err := compactLoginOf(t, dir, chainTime, nil)
	if err != nil || d.First != 1 || d.Last != 3 || d.Stopped != "" || d.LeftBefore != 0 {
		t.Fatalf("CompactLogin() = %+v, %v, want the records 1 to 3, with no stop", d, err)
	}
	s = reopenLog(t, dir, logdb.Options{})
	problems, notes = doctorOf(t, s, dir, nil)
	if len(problems) != 0 || !slices.Equal(notes, []string{startNote(5)}) {
		t.Errorf("whiteroom doctor finds %q with the notes %q after the compact, want nothing, with the start of the chain", problems, notes)
	}
	// The record 4 is gone, with the records 1 to 3, as the record 4 is lost; the records 5 and 6 are there.
	if got, want := recordsOf(t, s, audit.LoginLog), loginBefore[4:]; !slices.Equal(got, want) {
		t.Errorf("the login records are %v, want %v", got, want)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if again, err := compactLoginOf(t, dir, chainTime, nil); err != nil || again.First != 0 || again.Stopped != "" {
		t.Errorf("the second CompactLogin() = %+v, %v, want nothing to do", again, err)
	}
}

// TestRepairActivityLogWithNoRecordLeft checks the repair of an activity log whose every record is unreadable: the hole is
// recorded, whiteroom doctor reports the log, which always has a record, and says that the next record that the writer adds ends
// the problem, which it does.
func TestRepairActivityLogWithNoRecordLeft(t *testing.T) {
	s, dir, _ := auditFixture(t)
	replace := map[uint64][]byte{}
	for _, e := range rawEvents(t, s) {
		if log, _, _, ok := dbwire.ReadRecordID(e.Data); ok && log == dbwire.LogActivity {
			replace[e.Number] = []byte("not a record")
		}
	}
	s = closeAndRewrite(t, s, dir, rewriteEvents(replace, nil, nil))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	opts := dbserver.Options{Log: logdb.Options{Placement: logdb.Hybrid}}
	rep, err := dbserver.Repair(t.Context(), dir, opts, dbserver.RepairOptions{Apply: true, Keep: filepath.Join(t.TempDir(), "keep"),
		DropUnreadable: true, Now: chainTime})
	if err != nil || !rep.Applied || len(rep.Holes) != 1 || rep.Holes[0].Log != dbwire.LogActivity || rep.Holes[0].First != 1 ||
		rep.Holes[0].Last != 6 || !rep.Holes[0].Row {
		t.Fatalf("Repair() = %+v, %v, want the hole of the records 1 to 6, recorded", rep, err)
	}
	s = reopenLog(t, dir, logdb.Options{})
	problems, _ := doctorOf(t, s, dir, nil)
	if want := "audit: " + noActivityHole; !slices.Equal(problems, []string{want}) {
		t.Errorf("whiteroom doctor finds %q, want %q", problems, want)
	}
	update(t, s, func(tx *Tx) error { return tx.Record(eventRecord("next")) })
	problems, notes := doctorOf(t, s, dir, nil)
	if len(problems) != 0 {
		t.Errorf("whiteroom doctor finds %q after the writer's next record, want nothing", problems)
	}
	if want := lostNote("activity", 1, 6, againFrom(7)); !slices.Contains(notes, want) {
		t.Errorf("the notes are %q, want %q among them", notes, want)
	}
}
