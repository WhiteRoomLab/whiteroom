package logdb

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/cryptotest"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// salvageOptions makes small segments, with objects in files and in records, and a checkpoint at Close.
var salvageOptions = Options{Placement: Hybrid, Threshold: 100, SegmentBytes: 300, CheckpointBytes: -1}

// salvaged is what Salvage found in a store: the plan, the kept state as inMemory writes it, and the kept events as
// eventLines writes them.
type salvaged struct {
	plan   RepairPlan
	state  string
	events []string
}

func (s *salvaged) eventLines() string { return strings.Join(s.events, "\n") }

// trySalvage runs Salvage on fsys through a file system that refuses every change, and checks that nothing changed and
// that no file stays open, also when Salvage fails. f is not called if it returns an error.
func trySalvage(t *testing.T, fsys vfs.FS, o Options) (*salvaged, error) {
	t.Helper()
	ctx := context.Background()
	before := treeOf(t, fsys)
	frozen := &frozenFS{FS: fsys}
	res := &salvaged{}
	called := false
	err := Salvage(ctx, frozen, o, func(s *Salvager) error {
		called = true
		res.plan = s.Plan()
		var lines []string
		if err := s.View(ctx, func(tx *Tx) error {
			tables := tx.snapshot().tables
			for _, table := range slices.Sorted(maps.Keys(tables)) {
				tx.Scan(table, nil, func(k, v []byte) bool {
					lines = append(lines, fmt.Sprintf("%d/%s=%s", table, k, v))
					return true
				})
			}
			var objs []string
			tx.Objects(func(h [32]byte, size int64) bool {
				objs = append(objs, fmt.Sprintf("%x:%d", h, size))
				return true
			})
			res.state = strings.Join(lines, "\n") + "\nobjects:\n" + strings.Join(objs, "\n")
			return nil
		}); err != nil {
			return err
		}
		return s.Events(ctx, func(e Event) bool {
			res.events = append(res.events, fmt.Sprintf("%d/%d=%s", e.Number, e.Seq, e.Data))
			return true
		})
	})
	if calls := frozen.denied(); len(calls) > 0 {
		t.Errorf("Salvage tried to change the store: %v", calls)
	}
	if n := frozen.openFiles(); n != 0 {
		t.Errorf("Salvage left %d files open", n)
	}
	if after := treeOf(t, fsys); !maps.Equal(before, after) {
		t.Errorf("Salvage changed the files")
	}
	noRow := func(what string) {
		for _, mark := range []string{"KEYMARK", "VALMARK"} {
			if strings.Contains(what, mark) {
				t.Errorf("%q has a key or a value", what)
			}
		}
	}
	if err != nil {
		if called {
			t.Errorf("f was called, and Salvage returned %v", err)
		}
		noRow(err.Error())
		return nil, err
	}
	if !called {
		t.Fatal("f was not called")
	}
	for _, sp := range res.plan.Spans {
		noRow(sp.String())
	}
	for _, o := range res.plan.Objects {
		noRow(o.Problem.String())
	}
	for _, p := range res.plan.Recovery {
		noRow(p.String())
	}
	if tr := res.plan.Truncation; tr != nil {
		noRow(tr.Damage.String())
	}
	return res, nil
}

func salvage(t *testing.T, fsys vfs.FS, o Options) *salvaged {
	t.Helper()
	res, err := trySalvage(t, fsys, o)
	if err != nil {
		t.Fatalf("Salvage: %v", err)
	}
	return res
}

// sStore is a closed store of n commits of change, with the dumps of the state and of the events after each.
type sStore struct {
	fsys   *simfs.FS
	o      Options
	states []string
	events []string
	n      int
}

// newSStore makes the store, with a compaction after the commit compactAt, if it is not 0.
func newSStore(t *testing.T, o Options, n, compactAt int) *sStore {
	t.Helper()
	fsys, db := newSim(t, o)
	s := &sStore{fsys: fsys, o: o, n: n, states: []string{inMemory(t, db)}, events: []string{eventLines(t, db)}}
	for i := range n {
		change(t, db, i)
		if i+1 == compactAt {
			if err := compactDB(db, Rewrite{}); err != nil {
				t.Fatal(err)
			}
		}
		s.states, s.events = append(s.states, inMemory(t, db)), append(s.events, eventLines(t, db))
	}
	closeDB(t, db)
	return s
}

// newTailStore makes the image of a store whose writer stopped after n commits, the checkpoint being at the commit
// ckAt: the log has records after it, and head is not synced.
func newTailStore(t *testing.T, o Options, ckAt, n int) *sStore {
	t.Helper()
	return newTailStoreAfter(t, o, 0, ckAt, n)
}

// newTailStoreAfter is newTailStore with a compaction after the commit compactAt, if it is not 0.
func newTailStoreAfter(t *testing.T, o Options, compactAt, ckAt, n int) *sStore {
	t.Helper()
	fsys, db := newSim(t, o)
	s := &sStore{o: o, n: n, states: []string{inMemory(t, db)}, events: []string{eventLines(t, db)}}
	for i := range n {
		change(t, db, i)
		if i+1 == compactAt {
			if err := compactDB(db, Rewrite{}); err != nil {
				t.Fatal(err)
			}
		}
		if i+1 == ckAt {
			if err := db.Checkpoint(); err != nil {
				t.Fatal(err)
			}
		}
		s.states, s.events = append(s.states, inMemory(t, db)), append(s.events, eventLines(t, db))
	}
	s.fsys = copyFS(fsys)
	if ck := ckpOf(t, s.fsys); ck.seq != seqNum(ckAt) {
		t.Fatalf("the checkpoint is at %d", ck.seq)
	}
	return s
}

func (s *sStore) copy() *simfs.FS { return copyFS(s.fsys) }

// recLoc is a record in a segment of a store.
type recLoc struct {
	name string
	off  int64
	n    int
	pad  int // bytes of padding
	seq  seqNum
	typ  recType
}

func (r recLoc) path() string { return simPath(r.name) }

// locateRecords returns the records of the generation gen of fsys, which must be sound.
func locateRecords(t *testing.T, fsys vfs.FS, gen genNum) []recLoc {
	t.Helper()
	var out []recLoc
	for _, name := range logNames(t, fsys) {
		g, index, _ := parseSegName(path.Base(name))
		if g != gen {
			continue
		}
		b := fileData(t, fsys, name)
		h, err := parseSegHeader(simPath(name), b, g, index)
		if err != nil {
			t.Fatal(err)
		}
		for off := segHeaderSize; off < len(b); {
			body, n, reason := parseFrame(b[off:], h.salt)
			if reason != "" {
				t.Fatalf("%s at %d: %s", name, off, reason)
			}
			rec, reason := parseBody(body)
			if reason != "" {
				t.Fatalf("%s at %d: %s", name, off, reason)
			}
			out = append(out, recLoc{name: name, off: int64(off), n: n, pad: n - 8 - len(body), seq: rec.seq, typ: rec.typ})
			off += n
		}
	}
	return out
}

// commitAt returns the record of the commit seq.
func commitAt(t *testing.T, recs []recLoc, seq int) recLoc {
	t.Helper()
	for _, r := range recs {
		if r.typ == recCommit && r.seq == seqNum(seq) {
			return r
		}
	}
	t.Fatalf("no commit %d", seq)
	return recLoc{}
}

func flipByte(t *testing.T, fsys vfs.FS, name string, at int64) {
	t.Helper()
	b := fileData(t, fsys, name)
	b[at] ^= 1
	setFile(t, fsys, name, b)
}

// damage is a way to make a record unreadable, with the reason that its first frame gives.
type damage struct {
	name   string
	reason string
	apply  func(t *testing.T, fsys vfs.FS, r recLoc)
}

var recordDamages = []damage{
	{"a bad CRC", "bad record CRC", func(t *testing.T, fsys vfs.FS, r recLoc) { flipByte(t, fsys, r.name, r.off+8+1) }},
	{"a bad length", "bad record length 5", func(t *testing.T, fsys vfs.FS, r recLoc) {
		b := fileData(t, fsys, r.name)
		binary.LittleEndian.PutUint32(b[r.off:], 5)
		setFile(t, fsys, r.name, b)
	}},
	{"garbage", "bad record length 2863311530", func(t *testing.T, fsys vfs.FS, r recLoc) {
		b := fileData(t, fsys, r.name)
		for i := range r.n {
			b[r.off+int64(i)] = 0xaa
		}
		setFile(t, fsys, r.name, b)
	}},
}

// withoutEvents returns the lines of events, which have the form "<number>/", without those of the numbers drop.
func withoutEvents(lines string, drop ...int) []string {
	var out []string
	for _, l := range strings.Split(lines, "\n") {
		if l == "" {
			continue
		}
		skip := false
		for _, n := range drop {
			skip = skip || strings.HasPrefix(l, fmt.Sprintf("%d/", n))
		}
		if !skip {
			out = append(out, l)
		}
	}
	return out
}

func checkPlan(t *testing.T, got, want RepairPlan) {
	t.Helper()
	if !slices.Equal(got.Spans, want.Spans) {
		t.Errorf("spans:\n%+v\nwant\n%+v", got.Spans, want.Spans)
	}
	if !slices.EqualFunc(got.Objects, want.Objects, func(a, b LostObject) bool { return a == b }) {
		t.Errorf("objects:\n%+v\nwant\n%+v", got.Objects, want.Objects)
	}
	if (got.Truncation == nil) != (want.Truncation == nil) || got.Truncation != nil && *got.Truncation != *want.Truncation {
		t.Errorf("truncation: %+v, want %+v", got.Truncation, want.Truncation)
	}
	if got.Generation != want.Generation || got.Seq != want.Seq || got.Source != want.Source || got.Checkpoint != want.Checkpoint {
		t.Errorf("generation %d, sequence %d, from %v (%s), want %d, %d, from %v (%s)", got.Generation, got.Seq, got.Source,
			got.Checkpoint, want.Generation, want.Seq, want.Source, want.Checkpoint)
	}
	if !slices.Equal(got.Recovery, want.Recovery) {
		t.Errorf("recovery:\n%v\nwant\n%v", got.Recovery, want.Recovery)
	}
	if !slices.Equal(got.Remove, want.Remove) {
		t.Errorf("remove:\n%q\nwant\n%q", got.Remove, want.Remove)
	}
}

// recoveryProblemsOf returns what Inspect says that Open would change or not use in fsys: the problems of recovery.
func recoveryProblemsOf(t *testing.T, fsys vfs.FS) []Problem {
	t.Helper()
	r, err := decide(fsys, nil, func(s segFile, off, end segOff) ([]byte, error) { return readRange(fsys, s.name, off, end, true) })
	if err != nil {
		t.Fatal(err)
	}
	return recoveryProblems(fsys.Name(), r)
}

// checkSound checks that Salvage finds nothing wrong in the store s, which is as the writer left it, and keeps what
// Open keeps of it.
func checkSound(t *testing.T, fsys vfs.FS, s *sStore, gen uint64, source StateSource) {
	t.Helper()
	got := salvage(t, fsys, s.o)
	checkPlan(t, got.plan, RepairPlan{Generation: gen, Seq: uint64(s.n), Source: source, Checkpoint: got.plan.Checkpoint})
	if !got.plan.Empty() {
		t.Errorf("the plan is not empty: %+v", got.plan)
	}
	if got.state != s.states[s.n] {
		t.Errorf("state:\n%s\nwant\n%s", got.state, s.states[s.n])
	}
	if got.eventLines() != s.events[s.n] {
		t.Errorf("events:\n%s\nwant\n%s", got.eventLines(), s.events[s.n])
	}
}

func TestSalvageSound(t *testing.T) {
	withBaseBytes(t, 300)
	for _, gen := range []genNum{1, 2} {
		for _, ckp := range []bool{true, false} {
			t.Run(fmt.Sprintf("generation %d, checkpoint %v", gen, ckp), func(t *testing.T) {
				compactAt := 0
				if gen == 2 {
					compactAt = 6
				}
				s := newSStore(t, salvageOptions, 9, compactAt)
				if !ckp {
					dropCheckpoint(t, s.fsys)
				}
				source := FromCheckpoint
				if !ckp {
					source = FromLog
				}
				checkSound(t, s.copy(), s, uint64(gen), source)
				dst := simfs.New("/copy")
				if _, err := Backup(context.Background(), s.copy(), dst, nil); err != nil {
					t.Fatal(err)
				}
				got := salvage(t, dst, s.o)
				if !got.plan.Empty() || got.state != s.states[9] || got.eventLines() != s.events[9] {
					t.Errorf("the copy: %+v", got.plan)
				}
				if got.plan.Seq != 9 || got.plan.Generation != uint64(gen) {
					t.Errorf("the copy: generation %d, sequence %d", got.plan.Generation, got.plan.Seq)
				}
			})
		}
	}
	t.Run("a new store", func(t *testing.T) {
		fsys, db := newSim(t, Options{})
		closeDB(t, db)
		got := salvage(t, fsys, Options{})
		if !got.plan.Empty() || got.plan.Seq != 0 || got.plan.Generation != 1 || len(got.events) != 0 {
			t.Errorf("%+v", got)
		}
	})
}

// TestSalvageFuzzImages: the stores of FuzzOpen give an empty plan and the state, the events and the objects that Open
// gives.
func TestSalvageFuzzImages(t *testing.T) {
	for image := range uint8(4) {
		if fuzzStores[image] == nil {
			fuzzStores[image] = newFuzzStore(t, image&1 != 0, image&2 != 0)
		}
		base := fuzzStores[image]
		t.Run(fmt.Sprint(image), func(t *testing.T) {
			got := salvage(t, copyFS(base.fsys), fuzzOptions)
			db := reopen(t, copyFS(base.fsys), fuzzOptions)
			info := db.Recovery()
			if !got.plan.Empty() || len(got.plan.Recovery) != 0 || got.plan.Seq != info.Seq || got.plan.Generation != info.Generation {
				t.Errorf("plan %+v, Open: %+v", got.plan, info)
			}
			if want := inMemory(t, db); got.state != want {
				t.Errorf("state:\n%s\nwant\n%s", got.state, want)
			}
			if want := eventLines(t, db); got.eventLines() != want {
				t.Errorf("events:\n%s\nwant\n%s", got.eventLines(), want)
			}
		})
	}
}

// TestSalvageCrashImage: what a writer leaves after a crash gives what Inspect says that Open recovers, and nothing is
// dropped that cannot be read.
func TestSalvageCrashImage(t *testing.T) {
	o := Options{SegmentBytes: 100} // a segment of one record
	for _, c := range []struct {
		name  string
		mess  func(t *testing.T, fsys *simfs.FS)
		inRec string
	}{
		{"a torn tail", func(t *testing.T, fsys *simfs.FS) {
			last := lastSegment(t, fsys)
			h, _ := frames(t, fsys, last)
			torn := commitFrame(h.salt, 4, 1, "torn", "a torn value")
			setFile(t, fsys, last, slices.Concat(fileData(t, fsys, last), torn[:len(torn)/2]))
		}, "torn tail"},
		{"a torn new segment", func(t *testing.T, fsys *simfs.FS) {
			setFile(t, fsys, segName(1, 4), make([]byte, 40))
		}, "new segment"},
		{"a torn tail and a stale checkpoint", func(t *testing.T, fsys *simfs.FS) {
			last := lastSegment(t, fsys)
			h, _ := frames(t, fsys, last)
			torn := commitFrame(h.salt, 4, 1, "torn", "a torn value")
			setFile(t, fsys, last, slices.Concat(fileData(t, fsys, last), torn[:len(torn)-9]))
			dropCheckpoint(t, fsys)
		}, "torn tail"},
	} {
		t.Run(c.name, func(t *testing.T) {
			base, dumps := build(t, o, 3)
			fsys := copyFS(base)
			c.mess(t, fsys)
			got := salvage(t, copyFS(fsys), o)
			want := recoveryProblemsOf(t, copyFS(fsys))
			if len(want) != 1 || !strings.Contains(want[0].Reason, c.inRec) {
				t.Fatalf("recovery problems %v", want)
			}
			if !got.plan.Empty() || got.plan.Seq != 3 || !slices.Equal(got.plan.Recovery, want) || got.plan.Truncation != nil {
				t.Errorf("plan %+v, want only %v", got.plan, want)
			}
			db := reopen(t, copyFS(fsys), o)
			if want := inMemory(t, db); got.state != want || !strings.HasPrefix(got.state, dumps[3]+"\nobjects:") {
				t.Errorf("state:\n%s\nOpen has\n%s", got.state, want)
			}
		})
	}
}

// inlineObjects returns the objects of the store fsys that are in records, by hash.
func inlineObjects(t *testing.T, fsys *simfs.FS, o Options) map[[32]byte]objLoc {
	t.Helper()
	db := reopen(t, copyFS(fsys), o)
	out := map[[32]byte]objLoc{}
	db.cur.Load().objects.Ascend("", func(k string, loc objLoc) bool {
		if loc.placement == placeInline {
			out[[32]byte([]byte(k))] = loc
		}
		return true
	})
	return out
}

// lostIn returns the objects that overwriting the record r with 0xaa bytes loses, in the order of their hashes.
func lostIn(t *testing.T, objs map[[32]byte]objLoc, r recLoc, gen genNum) []LostObject {
	t.Helper()
	var out []LostObject
	for _, h := range slices.SortedFunc(maps.Keys(objs), func(a, b [32]byte) int { return slices.Compare(a[:], b[:]) }) {
		loc := objs[h]
		if segName(gen, loc.seg) != r.name || int64(loc.off) < r.off || int64(loc.off) >= r.off+int64(r.n) {
			continue
		}
		got := sha256.Sum256(slices.Repeat([]byte{0xaa}, int(loc.size)))
		out = append(out, LostObject{Hash: h, Problem: Problem{Path: r.path(), Offset: int64(loc.off), Reason: hashReason(h, got)}})
	}
	return out
}

// TestSalvageRecordBeforeCheckpoint: a record that cannot be read before the record of the checkpoint costs only its
// events. The state is that of the checkpoint, and its inline objects are kept if their bytes are right.
func TestSalvageRecordBeforeCheckpoint(t *testing.T) {
	withBaseBytes(t, 300)
	for _, gen := range []genNum{1, 2} {
		t.Run(fmt.Sprintf("generation %d", gen), func(t *testing.T) {
			compactAt := 0
			if gen == 2 {
				compactAt = 6
			}
			s := newSStore(t, salvageOptions, 9, compactAt)
			recs := locateRecords(t, s.fsys, gen)
			objs := inlineObjects(t, s.fsys, s.o)
			inline := 0
			for k := 1; k < 9; k++ {
				if gen == 2 && k <= 6 {
					continue // in the base
				}
				r := commitAt(t, recs, k)
				for _, d := range recordDamages {
					t.Run(fmt.Sprintf("%d, %s", k, d.name), func(t *testing.T) {
						fsys := s.copy()
						d.apply(t, fsys, r)
						if db := reopen(t, copyFS(fsys), s.o); db.Verify(context.Background()) == nil {
							t.Fatal("Verify finds nothing")
						}
						got := salvage(t, fsys, s.o)
						want := RepairPlan{Generation: uint64(gen), Seq: 9, Source: FromCheckpoint, Checkpoint: "used",
							Spans: []Span{{Problem: Problem{Path: r.path(), Offset: r.off, Reason: d.reason}, Length: int64(r.n),
								FirstSeq: uint64(k), LastSeq: uint64(k), FirstEvent: uint64(k), LastEvent: uint64(k)}}}
						if d.name == "garbage" {
							want.Objects = lostIn(t, objs, r, gen)
							inline += len(want.Objects)
						}
						checkPlan(t, got.plan, want)
						if got.state != s.states[9] {
							t.Errorf("state:\n%s\nwant\n%s", got.state, s.states[9])
						}
						if want := withoutEvents(s.events[9], k); !slices.Equal(got.events, want) {
							t.Errorf("events:\n%v\nwant\n%v", got.events, want)
						}
					})
				}
			}
			if inline == 0 {
				t.Error("no record with an inline object was overwritten")
			}
		})
	}
	t.Run("two records", func(t *testing.T) {
		s := newSStore(t, salvageOptions, 9, 0)
		recs := locateRecords(t, s.fsys, 1)
		r1, r2 := commitAt(t, recs, 1), commitAt(t, recs, 2)
		if r1.name != r2.name || r1.off+int64(r1.n) != r2.off {
			t.Fatalf("%+v, %+v", r1, r2)
		}
		fsys := s.copy()
		flipByte(t, fsys, r1.name, r1.off+9)
		flipByte(t, fsys, r2.name, r2.off+9)
		got := salvage(t, fsys, s.o)
		want := []Span{{Problem: Problem{Path: r1.path(), Offset: r1.off, Reason: "bad record CRC"}, Length: int64(r1.n + r2.n),
			FirstSeq: 1, LastSeq: 2, FirstEvent: 1, LastEvent: 2}}
		if !slices.Equal(got.plan.Spans, want) || got.state != s.states[9] || !slices.Equal(got.events, withoutEvents(s.events[9], 1, 2)) {
			t.Errorf("%+v\n%v", got.plan.Spans, got.events)
		}
	})
	t.Run("a record cut short at the end of a segment", func(t *testing.T) {
		s := newSStore(t, salvageOptions, 9, 0)
		recs := locateRecords(t, s.fsys, 1)
		r := commitAt(t, recs, 4)
		fsys := s.copy()
		b := fileData(t, fsys, r.name)
		setFile(t, fsys, r.name, b[:r.off+20])
		got := salvage(t, fsys, s.o)
		want := []Span{{Problem: Problem{Path: r.path(), Offset: r.off, Reason: "truncated record"}, Length: 20,
			FirstSeq: 4, LastSeq: 4, FirstEvent: 4, LastEvent: 4}}
		if !slices.Equal(got.plan.Spans, want) || got.state != s.states[9] {
			t.Errorf("%+v", got.plan.Spans)
		}
	})
	t.Run("a segment that ends where a record ends loses it without a damaged byte", func(t *testing.T) {
		s := newSStore(t, salvageOptions, 9, 0)
		recs := locateRecords(t, s.fsys, 1)
		r, next := commitAt(t, recs, 4), commitAt(t, recs, 5)
		fsys := s.copy()
		b := fileData(t, fsys, r.name)
		setFile(t, fsys, r.name, b[:r.off])
		got := salvage(t, fsys, s.o)
		want := []Span{{Problem: Problem{Path: next.path(), Offset: next.off, Reason: "missing records"}, Length: 0,
			FirstSeq: 4, LastSeq: 4, FirstEvent: 4, LastEvent: 4}}
		if !slices.Equal(got.plan.Spans, want) || got.state != s.states[9] || got.plan.Truncation != nil {
			t.Errorf("%+v", got.plan.Spans)
		}
	})
}

// TestSalvageSegmentBeforeCheckpoint: a segment before the checkpoint whose header fails its CRC is read with its
// salt if the first frame verifies with it, and is one span if not; a missing segment is one span.
func TestSalvageSegmentBeforeCheckpoint(t *testing.T) {
	s := newSStore(t, salvageOptions, 9, 0)
	recs := locateRecords(t, s.fsys, 1)
	seg2 := commitAt(t, recs, 3).name
	var inSeg2 []recLoc
	for _, r := range recs {
		if r.name == seg2 {
			inSeg2 = append(inSeg2, r)
		}
	}
	first, last := int(inSeg2[0].seq), int(inSeg2[len(inSeg2)-1].seq)
	if first != 3 || last != 4 {
		t.Fatalf("segment 2 has the records %d to %d", first, last)
	}
	size := int64(len(fileData(t, s.fsys, seg2)))
	check := func(t *testing.T, fsys *simfs.FS, spans []Span, events []string) {
		t.Helper()
		got := salvage(t, fsys, s.o)
		if !slices.Equal(got.plan.Spans, spans) || got.plan.Seq != 9 || got.plan.Truncation != nil || got.state != s.states[9] ||
			!slices.Equal(got.events, events) || got.plan.Source != FromCheckpoint {
			t.Errorf("spans\n%+v\nwant\n%+v\nevents %v", got.plan.Spans, spans, got.events)
		}
	}
	all := strings.Split(s.events[9], "\n")
	t.Run("the CRC of the header, with its salt", func(t *testing.T) {
		fsys := s.copy()
		flipByte(t, fsys, seg2, 32) // first sequence
		check(t, fsys, []Span{{Problem: Problem{Path: simPath(seg2), Offset: 0, Reason: "bad header CRC"}, Length: segHeaderSize}}, all)
		// Open reads only the segment of the checkpoint.
		if db := reopen(t, copyFS(fsys), s.o); db.Verify(context.Background()) == nil {
			t.Error("Verify finds nothing")
		}
	})
	t.Run("the first segment, with its salt", func(t *testing.T) {
		fsys := s.copy()
		flipByte(t, fsys, seg1, 60)
		check(t, fsys, []Span{{Problem: Problem{Path: simPath(seg1), Offset: 0, Reason: "bad header CRC"}, Length: segHeaderSize}}, all)
	})
	t.Run("the CRC and the salt of the header", func(t *testing.T) {
		fsys := s.copy()
		flipByte(t, fsys, seg2, 40)
		check(t, fsys, []Span{{Problem: Problem{Path: simPath(seg2), Offset: 0, Reason: "bad header CRC"}, Length: size,
			FirstSeq: 3, LastSeq: 4, FirstEvent: 3, LastEvent: 4}}, withoutEvents(s.events[9], 3, 4))
	})
	t.Run("the magic of the header", func(t *testing.T) {
		fsys := s.copy()
		flipByte(t, fsys, seg2, 0)
		check(t, fsys, []Span{{Problem: Problem{Path: simPath(seg2), Offset: 0, Reason: "not a segment of whiteroom's log"}, Length: size,
			FirstSeq: 3, LastSeq: 4, FirstEvent: 3, LastEvent: 4}}, withoutEvents(s.events[9], 3, 4))
	})
	t.Run("the header of another segment", func(t *testing.T) {
		fsys := s.copy()
		setFile(t, fsys, seg2, slices.Concat(fileData(t, fsys, seg3)[:segHeaderSize], fileData(t, fsys, seg2)[segHeaderSize:]))
		check(t, fsys, []Span{{Problem: Problem{Path: simPath(seg2), Offset: 0, Reason: "the header names generation 1, index 3"}, Length: size,
			FirstSeq: 3, LastSeq: 4, FirstEvent: 3, LastEvent: 4}}, withoutEvents(s.events[9], 3, 4))
	})
	t.Run("a missing segment", func(t *testing.T) {
		fsys := s.copy()
		removeFile(t, fsys, seg2)
		check(t, fsys, []Span{{Problem: Problem{Path: simPath(seg2), Offset: -1, Reason: "missing segment"},
			FirstSeq: 3, LastSeq: 4, FirstEvent: 3, LastEvent: 4}}, withoutEvents(s.events[9], 3, 4))
	})
	t.Run("the first segment is missing", func(t *testing.T) {
		fsys := s.copy()
		removeFile(t, fsys, seg1)
		check(t, fsys, []Span{{Problem: Problem{Path: simPath(seg1), Offset: -1, Reason: "missing segment"},
			FirstSeq: 1, LastSeq: 2, FirstEvent: 1, LastEvent: 2}}, withoutEvents(s.events[9], 1, 2))
	})
	t.Run("two segments are missing, and a record before them", func(t *testing.T) {
		fsys := s.copy()
		removeFile(t, fsys, seg3)
		removeFile(t, fsys, seg4)
		flipByte(t, fsys, seg2, inSeg2[1].off+9)
		got := salvage(t, fsys, s.o)
		want := []Span{
			{Problem: Problem{Path: inSeg2[1].path(), Offset: inSeg2[1].off, Reason: "bad record CRC"}, Length: int64(inSeg2[1].n)},
			{Problem: Problem{Path: simPath(seg3), Offset: -1, Reason: "missing segment"}},
			{Problem: Problem{Path: simPath(seg4), Offset: -1, Reason: "missing segment"}},
		}
		for i := range want { // the records around them are 3 and 7
			want[i].FirstSeq, want[i].LastSeq, want[i].FirstEvent, want[i].LastEvent = 4, 6, 4, 6
		}
		if !slices.Equal(got.plan.Spans, want) || got.state != s.states[9] || !slices.Equal(got.events, withoutEvents(s.events[9], 4, 5, 6)) {
			t.Errorf("%+v\n%v", got.plan.Spans, got.events)
		}
	})
}

// TestSalvageCheckpointRecord: the checkpoint is used when its record cannot be read, or its segment is gone, as when a
// store that its writer closed has its last record damaged, which Open refuses: the state is the checkpoint's, and
// only the events of the record are lost, and its inline objects if their bytes are.
func TestSalvageCheckpointRecord(t *testing.T) {
	for _, n := range []int{2, 9} {
		s := newSStore(t, salvageOptions, n, 0)
		recs := locateRecords(t, s.fsys, 1)
		r := commitAt(t, recs, n)
		objs := inlineObjects(t, s.fsys, s.o)
		inRecord := func(problem func(h [32]byte, loc objLoc) Problem) []LostObject {
			var out []LostObject
			for _, h := range slices.SortedFunc(maps.Keys(objs), func(a, b [32]byte) int { return slices.Compare(a[:], b[:]) }) {
				if loc := objs[h]; segName(1, loc.seg) == r.name && int64(loc.off) >= r.off {
					out = append(out, LostObject{Hash: h, Problem: problem(h, loc)})
				}
			}
			if n == 9 && len(out) == 0 {
				t.Fatal("the record has no inline object")
			}
			return out
		}
		ev := uint64(n)
		own := func(objects []LostObject, spans ...Span) RepairPlan {
			return RepairPlan{Generation: 1, Seq: uint64(n), Source: FromCheckpointAlone, Checkpoint: "used", Spans: spans, Objects: objects}
		}
		check := func(t *testing.T, fsys *simfs.FS, want RepairPlan, events []string) {
			t.Helper()
			if _, err := Open(copyFS(fsys), s.o); err == nil {
				t.Error("Open opens it")
			}
			got := salvage(t, fsys, s.o)
			checkPlan(t, got.plan, want)
			if got.state != s.states[n] || !slices.Equal(got.events, events) {
				t.Errorf("state:\n%s\nevents %v", got.state, got.events)
			}
		}
		t.Run(fmt.Sprintf("%d commits", n), func(t *testing.T) {
			for _, d := range recordDamages {
				t.Run(d.name, func(t *testing.T) {
					fsys := s.copy()
					d.apply(t, fsys, r)
					var lost []LostObject
					if d.name == "garbage" {
						lost = lostIn(t, objs, r, 1)
					}
					check(t, fsys, own(lost, Span{Problem: Problem{Path: r.path(), Offset: r.off, Reason: d.reason}, Length: int64(r.n),
						FirstSeq: ev, LastSeq: ev, FirstEvent: ev, LastEvent: ev}), withoutEvents(s.events[n], n))
				})
			}
			if n > 2 { // else the log has no segment
				t.Run("its segment is missing", func(t *testing.T) {
					fsys := s.copy()
					removeFile(t, fsys, r.name)
					if r.off != segHeaderSize {
						t.Fatal("the record is not the first of its segment")
					}
					lost := inRecord(func([32]byte, objLoc) Problem { return Problem{Path: r.path(), Offset: -1, Reason: "missing file"} })
					check(t, fsys, own(lost, Span{Problem: Problem{Path: r.path(), Offset: -1, Reason: "missing segment"},
						FirstSeq: ev, LastSeq: ev, FirstEvent: ev, LastEvent: ev}), withoutEvents(s.events[n], n))
				})
			}
			t.Run("its segment ends before it", func(t *testing.T) {
				fsys := s.copy()
				b := fileData(t, fsys, r.name)
				setFile(t, fsys, r.name, b[:r.off])
				lost := inRecord(func(_ [32]byte, loc objLoc) Problem {
					return Problem{Path: r.path(), Offset: -1,
						Reason: fmt.Sprintf("has %d bytes, too few for an object of %d bytes at offset %d", r.off, loc.size, loc.off)}
				})
				check(t, fsys, own(lost, Span{Problem: Problem{Path: r.path(), Offset: r.off, Reason: "missing records"},
					FirstSeq: ev, LastSeq: ev, FirstEvent: ev, LastEvent: ev}), withoutEvents(s.events[n], n))
			})
			t.Run("its segment has a header that cannot be read", func(t *testing.T) {
				fsys := s.copy()
				flipByte(t, fsys, r.name, 40)
				b := fileData(t, fsys, r.name)
				first := ev
				var lost []int
				for _, x := range recs {
					if x.name == r.name {
						lost = append(lost, int(x.seq))
						first = min(first, uint64(x.seq))
					}
				}
				check(t, fsys, own(nil, Span{Problem: Problem{Path: r.path(), Offset: 0, Reason: "bad header CRC"}, Length: int64(len(b)),
					FirstSeq: first, LastSeq: ev, FirstEvent: first, LastEvent: ev}), withoutEvents(s.events[n], lost...))
			})
			t.Run("its header fails the CRC, with the salt", func(t *testing.T) {
				fsys := s.copy()
				flipByte(t, fsys, r.name, 32)
				got := salvage(t, fsys, s.o)
				want := RepairPlan{Generation: 1, Seq: uint64(n), Source: FromCheckpoint, Checkpoint: "used",
					Spans: []Span{{Problem: Problem{Path: r.path(), Offset: 0, Reason: "bad header CRC"}, Length: segHeaderSize}}}
				checkPlan(t, got.plan, want)
				if got.state != s.states[n] || got.eventLines() != s.events[n] {
					t.Errorf("state:\n%s", got.state)
				}
			})
		})
	}
	t.Run("two records, the second is that of the checkpoint", func(t *testing.T) {
		s := newSStore(t, salvageOptions, 2, 0)
		recs := locateRecords(t, s.fsys, 1)
		r1, r2 := commitAt(t, recs, 1), commitAt(t, recs, 2)
		fsys := s.copy()
		flipByte(t, fsys, r1.name, r1.off+9)
		flipByte(t, fsys, r2.name, r2.off+9)
		got := salvage(t, fsys, s.o)
		want := RepairPlan{Generation: 1, Seq: 2, Source: FromCheckpointAlone, Checkpoint: "used", Spans: []Span{{
			Problem: Problem{Path: r1.path(), Offset: r1.off, Reason: "bad record CRC"}, Length: int64(r1.n + r2.n),
			FirstSeq: 1, LastSeq: 2, FirstEvent: 1, LastEvent: 2}}}
		checkPlan(t, got.plan, want)
		if got.state != s.states[2] || len(got.events) != 0 {
			t.Errorf("state:\n%s\nevents %v", got.state, got.events)
		}
	})
	t.Run("all the segments are gone", func(t *testing.T) {
		s := newSStore(t, salvageOptions, 2, 0)
		fsys := s.copy()
		removeFile(t, fsys, seg1)
		if _, err := trySalvage(t, fsys, s.o); err == nil || !strings.Contains(err.Error(), "no segments") {
			t.Errorf("Salvage: %v", err)
		}
	})
}

// TestSalvageForeignCheckpoint: a checkpoint is not used if the frame at its offset is a valid record of another
// sequence number or another CRC, which is not that log's.
func TestSalvageForeignCheckpoint(t *testing.T) {
	o := Options{Placement: Hybrid, Threshold: 100, SegmentBytes: 1200, CheckpointBytes: -1}
	s := newSStore(t, o, 9, 0)
	recs := locateRecords(t, s.fsys, 1)
	last, before := commitAt(t, recs, 9), commitAt(t, recs, 8)
	if last.name != before.name || ckpOf(t, s.fsys).off != segOff(last.off) {
		t.Fatalf("the last records are %+v and %+v", last, before)
	}
	for _, c := range []struct {
		name string
		fn   func(h *ckpHeader)
	}{
		{"another record", func(h *ckpHeader) { h.off = segOff(before.off) }},
		{"another CRC", func(h *ckpHeader) { h.recCRC++ }},
		{"another sequence number", func(h *ckpHeader) { h.seq-- }},
		{"a segment that starts after the record", func(h *ckpHeader) { h.seq = 3 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			fsys := s.copy()
			setCkpHeader(t, fsys, c.fn)
			got := salvage(t, fsys, o)
			checkPlan(t, got.plan, RepairPlan{Generation: 1, Seq: 9, Source: FromLog, Checkpoint: "not this log's"})
			if got.state != s.states[9] || got.eventLines() != s.events[9] {
				t.Errorf("state:\n%s", got.state)
			}
		})
	}
}

// cutLog cuts the log of fsys before the record r: the segment of r ends where it begins, and the later ones are gone.
func cutLog(t *testing.T, fsys vfs.FS, r recLoc) {
	t.Helper()
	b := fileData(t, fsys, r.name)
	setFile(t, fsys, r.name, b[:r.off])
	for _, name := range logNames(t, fsys) {
		if name > r.name {
			removeFile(t, fsys, name)
		}
	}
}

// forgeRecord replaces the frame of r with another that has the same length, whose body is that of r changed by f, and
// a valid CRC.
func forgeRecord(t *testing.T, fsys vfs.FS, r recLoc, f func(body []byte)) {
	t.Helper()
	b := fileData(t, fsys, r.name)
	gen, index, _ := parseSegName(path.Base(r.name))
	h, err := parseSegHeader(r.path(), b, gen, index)
	if err != nil {
		t.Fatal(err)
	}
	l := int64(binary.LittleEndian.Uint32(b[r.off:]))
	body := slices.Clone(b[r.off+8 : r.off+8+l])
	f(body)
	frame := appendFrame(nil, h.salt, body)
	if len(frame) != r.n {
		t.Fatalf("a frame of %d bytes, was %d", len(frame), r.n)
	}
	copy(b[r.off:], frame)
	setFile(t, fsys, r.name, b)
}

// span returns the span of the record r, whose first frame cannot be read for the reason.
func span(r recLoc, reason string) Span {
	return Span{Problem: Problem{Path: r.path(), Offset: r.off, Reason: reason}, Length: int64(r.n),
		FirstSeq: uint64(r.seq), LastSeq: uint64(r.seq), FirstEvent: uint64(r.seq), LastEvent: uint64(r.seq)}
}

func (s *sStore) checkKept(t *testing.T, got *salvaged, seq int) {
	t.Helper()
	if got.state != s.states[seq] {
		t.Errorf("state:\n%s\nwant that after commit %d:\n%s", got.state, seq, s.states[seq])
	}
	if got.eventLines() != s.events[seq] {
		t.Errorf("events:\n%s\nwant\n%s", got.eventLines(), s.events[seq])
	}
}

// TestSalvageRecordAfterCheckpoint: a record that cannot be read after the record of the checkpoint ends the kept
// state before it, and the records after it that can be read are the truncation: only the operator drops them.
func TestSalvageRecordAfterCheckpoint(t *testing.T) {
	s := newTailStore(t, salvageOptions, 3, 9)
	recs := locateRecords(t, s.fsys, 1)
	t.Run("a record in the middle", func(t *testing.T) {
		for k := 4; k <= 8; k++ {
			r := commitAt(t, recs, k)
			for _, d := range recordDamages {
				t.Run(fmt.Sprintf("%d, %s", k, d.name), func(t *testing.T) {
					fsys := s.copy()
					d.apply(t, fsys, r)
					if _, err := Open(copyFS(fsys), s.o); err == nil {
						t.Error("Open opens it")
					}
					got := salvage(t, fsys, s.o)
					sp := span(r, d.reason)
					checkPlan(t, got.plan, RepairPlan{Generation: 1, Seq: uint64(k - 1), Source: FromCheckpoint, Checkpoint: "used",
						Spans: []Span{sp}, Truncation: &Truncation{Damage: sp.Problem, FirstSeq: uint64(k + 1), LastSeq: 9,
							FirstEvent: uint64(k), LastEvent: 9}})
					s.checkKept(t, got, k-1)
					// It is the state that Open has of the log cut before the record.
					cut := s.copy()
					cutLog(t, cut, r)
					if db := reopen(t, cut, s.o); inMemory(t, db) != got.state || db.Recovery().Seq != uint64(k-1) {
						t.Errorf("Open of the log cut before it: %+v", db.Recovery())
					}
				})
			}
		}
	})
	t.Run("the last record is a torn tail, which Open cuts", func(t *testing.T) {
		r := commitAt(t, recs, 9)
		for _, d := range recordDamages {
			t.Run(d.name, func(t *testing.T) {
				fsys := s.copy()
				d.apply(t, fsys, r)
				got := salvage(t, fsys, s.o)
				want := recoveryProblemsOf(t, copyFS(fsys))
				if len(want) != 1 || !strings.Contains(want[0].Reason, "torn tail") {
					t.Fatalf("recovery %v", want)
				}
				checkPlan(t, got.plan, RepairPlan{Generation: 1, Seq: 8, Source: FromCheckpoint, Checkpoint: "used", Recovery: want})
				s.checkKept(t, got, 8)
				if info := recoveryOf(t, fsys, s.o); info.Seq != 8 || info.TruncatedAt != r.off {
					t.Errorf("Open: %+v", info)
				}
			})
		}
	})
	t.Run("the last record cannot be read, and is no torn tail", func(t *testing.T) {
		r := commitAt(t, recs, 9)
		fsys := s.copy()
		forgeRecord(t, fsys, r, func(body []byte) { body[0] = 9 })
		if _, err := Open(copyFS(fsys), s.o); err == nil {
			t.Error("Open opens it")
		}
		got := salvage(t, fsys, s.o)
		// Nothing that can be read follows: no truncation, and the span has no sequence numbers.
		checkPlan(t, got.plan, RepairPlan{Generation: 1, Seq: 8, Source: FromCheckpoint, Checkpoint: "used", Spans: []Span{{
			Problem: Problem{Path: r.path(), Offset: r.off, Reason: "unknown record type 9"}, Length: int64(r.n)}}})
		s.checkKept(t, got, 8)
	})
	t.Run("two records in two segments", func(t *testing.T) {
		r5, r6 := commitAt(t, recs, 5), commitAt(t, recs, 6)
		fsys := s.copy()
		flipByte(t, fsys, r5.name, r5.off+9)
		flipByte(t, fsys, r6.name, r6.off+9)
		got := salvage(t, fsys, s.o)
		both := func(r recLoc) Span {
			sp := span(r, "bad record CRC")
			sp.FirstSeq, sp.LastSeq, sp.FirstEvent, sp.LastEvent = 5, 6, 5, 6 // as they are the gap between 4 and 7
			return sp
		}
		spans := []Span{both(r5), both(r6)}
		checkPlan(t, got.plan, RepairPlan{Generation: 1, Seq: 4, Source: FromCheckpoint, Checkpoint: "used", Spans: spans,
			Truncation: &Truncation{Damage: spans[0].Problem, FirstSeq: 7, LastSeq: 9, FirstEvent: 5, LastEvent: 9}})
		s.checkKept(t, got, 4)
	})
	t.Run("a segment is missing", func(t *testing.T) {
		r := commitAt(t, recs, 6)
		fsys := s.copy()
		removeFile(t, fsys, r.name)
		got := salvage(t, fsys, s.o)
		sp := Span{Problem: Problem{Path: r.path(), Offset: -1, Reason: "missing segment"}, FirstSeq: 6, LastSeq: 6, FirstEvent: 6, LastEvent: 6}
		checkPlan(t, got.plan, RepairPlan{Generation: 1, Seq: 5, Source: FromCheckpoint, Checkpoint: "used", Spans: []Span{sp},
			Truncation: &Truncation{Damage: sp.Problem, FirstSeq: 7, LastSeq: 9, FirstEvent: 6, LastEvent: 9}})
		s.checkKept(t, got, 5)
	})
	t.Run("a segment loses its last record without a damaged byte", func(t *testing.T) {
		r4, r5 := commitAt(t, recs, 4), commitAt(t, recs, 5)
		fsys := s.copy()
		b := fileData(t, fsys, r4.name)
		setFile(t, fsys, r4.name, b[:r4.off])
		got := salvage(t, fsys, s.o)
		sp := Span{Problem: Problem{Path: r5.path(), Offset: r5.off, Reason: "missing records"}, FirstSeq: 4, LastSeq: 4, FirstEvent: 4, LastEvent: 4}
		checkPlan(t, got.plan, RepairPlan{Generation: 1, Seq: 3, Source: FromCheckpoint, Checkpoint: "used", Spans: []Span{sp},
			Truncation: &Truncation{Damage: sp.Problem, FirstSeq: 5, LastSeq: 9, FirstEvent: 4, LastEvent: 9}})
		s.checkKept(t, got, 3)
	})
	t.Run("a header that cannot be read", func(t *testing.T) {
		r := commitAt(t, recs, 6)
		fsys := s.copy()
		flipByte(t, fsys, r.name, 40)
		got := salvage(t, fsys, s.o)
		sp := Span{Problem: Problem{Path: r.path(), Offset: 0, Reason: "bad header CRC"}, Length: int64(len(fileData(t, fsys, r.name))),
			FirstSeq: 6, LastSeq: 6, FirstEvent: 6, LastEvent: 6}
		checkPlan(t, got.plan, RepairPlan{Generation: 1, Seq: 5, Source: FromCheckpoint, Checkpoint: "used", Spans: []Span{sp},
			Truncation: &Truncation{Damage: sp.Problem, FirstSeq: 7, LastSeq: 9, FirstEvent: 6, LastEvent: 9}})
		s.checkKept(t, got, 5)
	})
	t.Run("padding that is not zero loses nothing", func(t *testing.T) {
		n := 0
		for _, r := range recs {
			if r.seq < 4 || r.pad == 0 {
				continue
			}
			n++
			fsys := s.copy()
			flipByte(t, fsys, r.name, r.off+int64(r.n-r.pad))
			if _, err := Open(copyFS(fsys), s.o); err == nil {
				t.Errorf("Open opens it: %d", r.seq)
			}
			got := salvage(t, fsys, s.o)
			checkPlan(t, got.plan, RepairPlan{Generation: 1, Seq: 9, Source: FromCheckpoint, Checkpoint: "used", Spans: []Span{{
				Problem: Problem{Path: r.path(), Offset: r.off + int64(r.n-r.pad), Reason: "nonzero padding"}, Length: int64(r.pad)}}})
			s.checkKept(t, got, 9)
		}
		if n < 3 {
			t.Errorf("%d records with padding", n)
		}
	})
	t.Run("the record of the checkpoint cannot be read, the records after it can", func(t *testing.T) {
		r := commitAt(t, recs, 3)
		fsys := s.copy()
		flipByte(t, fsys, r.name, r.off+9)
		got := salvage(t, fsys, s.o)
		checkPlan(t, got.plan, RepairPlan{Generation: 1, Seq: 9, Source: FromCheckpointAlone, Checkpoint: "used", Spans: []Span{
			span(r, "bad record CRC")}})
		if got.state != s.states[9] || !slices.Equal(got.events, withoutEvents(s.events[9], 3)) {
			t.Errorf("state:\n%s\nevents %v", got.state, got.events)
		}
	})
	t.Run("the record of the checkpoint and the one after it cannot be read", func(t *testing.T) {
		r3, r4 := commitAt(t, recs, 3), commitAt(t, recs, 4)
		if r3.name != r4.name {
			t.Fatal("not in one segment")
		}
		fsys := s.copy()
		flipByte(t, fsys, r3.name, r3.off+9)
		flipByte(t, fsys, r4.name, r4.off+9)
		got := salvage(t, fsys, s.o)
		sp := span(r3, "bad record CRC")
		sp.Length = int64(r3.n + r4.n)
		sp.LastSeq, sp.LastEvent = 4, 4
		checkPlan(t, got.plan, RepairPlan{Generation: 1, Seq: 3, Source: FromCheckpointAlone, Checkpoint: "used", Spans: []Span{sp},
			Truncation: &Truncation{Damage: sp.Problem, FirstSeq: 5, LastSeq: 9, FirstEvent: 4, LastEvent: 9}})
		if got.state != s.states[3] || !slices.Equal(got.events, withoutEvents(s.events[3], 3)) {
			t.Errorf("state:\n%s\nevents %v", got.state, got.events)
		}
	})
	t.Run("an object of a record that is dropped is not checked", func(t *testing.T) {
		r := commitAt(t, recs, 5)
		fsys := s.copy()
		flipByte(t, fsys, r.name, r.off+9)
		cut := s.copy()
		cutLog(t, cut, r)
		kept := reopen(t, cut, s.o).cur.Load().objects
		var want []LostObject
		reopen(t, s.copy(), s.o).cur.Load().objects.Ascend("", func(k string, loc objLoc) bool {
			if loc.placement == placeFile {
				h := [32]byte([]byte(k))
				removeFile(t, fsys, objName(h))
				if _, ok := kept.Get(k); ok {
					want = append(want, LostObject{Hash: h, Problem: Problem{Path: simPath(objName(h)), Offset: -1, Reason: "missing file"}})
				}
			}
			return true
		})
		if len(want) == 0 {
			t.Fatal("no object")
		}
		got := salvage(t, fsys, s.o)
		if !slices.EqualFunc(got.plan.Objects, want, func(a, b LostObject) bool { return a == b }) {
			t.Errorf("objects %+v, want %+v", got.plan.Objects, want)
		}
	})
}

// TestSalvageNoCheckpoint: with no checkpoint every record is after the start, and the first one that cannot be read
// ends the state.
func TestSalvageNoCheckpoint(t *testing.T) {
	s := newSStore(t, salvageOptions, 9, 0)
	dropCheckpoint(t, s.fsys)
	recs := locateRecords(t, s.fsys, 1)
	for _, k := range []int{1, 2, 3, 4, 5, 8} {
		r := commitAt(t, recs, k)
		for _, d := range recordDamages {
			t.Run(fmt.Sprintf("%d, %s", k, d.name), func(t *testing.T) {
				fsys := s.copy()
				d.apply(t, fsys, r)
				got := salvage(t, fsys, s.o)
				sp := span(r, d.reason)
				checkPlan(t, got.plan, RepairPlan{Generation: 1, Seq: uint64(k - 1), Source: FromLog, Checkpoint: "none", Spans: []Span{sp},
					Truncation: &Truncation{Damage: sp.Problem, FirstSeq: uint64(k + 1), LastSeq: 9, FirstEvent: uint64(k), LastEvent: 9}})
				s.checkKept(t, got, k-1)
				cut := s.copy()
				cutLog(t, cut, r)
				if db := reopen(t, cut, s.o); inMemory(t, db) != got.state {
					t.Errorf("Open of the log cut before it has\n%s", inMemory(t, db))
				}
			})
		}
	}
	t.Run("a segment is missing", func(t *testing.T) {
		r := commitAt(t, recs, 3)
		fsys := s.copy()
		removeFile(t, fsys, r.name)
		got := salvage(t, fsys, s.o)
		sp := Span{Problem: Problem{Path: r.path(), Offset: -1, Reason: "missing segment"}, FirstSeq: 3, LastSeq: 4, FirstEvent: 3, LastEvent: 4}
		checkPlan(t, got.plan, RepairPlan{Generation: 1, Seq: 2, Source: FromLog, Checkpoint: "none", Spans: []Span{sp},
			Truncation: &Truncation{Damage: sp.Problem, FirstSeq: 5, LastSeq: 9, FirstEvent: 3, LastEvent: 9}})
		s.checkKept(t, got, 2)
	})
	t.Run("the last record is a torn tail", func(t *testing.T) {
		r := commitAt(t, recs, 9)
		fsys := s.copy()
		flipByte(t, fsys, r.name, r.off+9)
		got := salvage(t, fsys, s.o)
		if got.plan.Truncation != nil || len(got.plan.Spans) != 0 || got.plan.Seq != 8 || len(got.plan.Recovery) != 1 {
			t.Errorf("plan %+v", got.plan)
		}
		s.checkKept(t, got, 8)
	})
}

// TestSalvageDamagedCheckpoint: a checkpoint that is damaged is not used, and the log is read from the start.
func TestSalvageDamagedCheckpoint(t *testing.T) {
	s := newSStore(t, salvageOptions, 9, 0)
	recs := locateRecords(t, s.fsys, 1)
	t.Run("only the checkpoint", func(t *testing.T) {
		fsys := s.copy()
		flipByte(t, fsys, "checkpoint", 100)
		got := salvage(t, fsys, s.o)
		checkPlan(t, got.plan, RepairPlan{Generation: 1, Seq: 9, Source: FromLog, Checkpoint: "damaged: bad body CRC",
			Recovery: recoveryProblemsOf(t, copyFS(fsys))})
		if len(got.plan.Recovery) != 1 || !got.plan.Empty() {
			t.Errorf("plan %+v", got.plan)
		}
		s.checkKept(t, got, 9)
	})
	t.Run("and a record", func(t *testing.T) {
		r := commitAt(t, recs, 6)
		fsys := s.copy()
		flipByte(t, fsys, "checkpoint", 100)
		flipByte(t, fsys, r.name, r.off+9)
		got := salvage(t, fsys, s.o)
		sp := span(r, "bad record CRC")
		checkPlan(t, got.plan, RepairPlan{Generation: 1, Seq: 5, Source: FromLog, Checkpoint: "damaged: bad body CRC", Spans: []Span{sp},
			Truncation: &Truncation{Damage: sp.Problem, FirstSeq: 7, LastSeq: 9, FirstEvent: 6, LastEvent: 9},
			Recovery:   []Problem{{Path: simPath("checkpoint"), Offset: -1, Reason: "damaged (bad body CRC): Open reads the log from the start of its generation"}}})
		s.checkKept(t, got, 5)
	})
	t.Run("a checkpoint of an older generation", func(t *testing.T) {
		withBaseBytes(t, 300)
		fsys, db := newSim(t, salvageOptions)
		for i := range 6 {
			change(t, db, i)
		}
		if err := db.Checkpoint(); err != nil {
			t.Fatal(err)
		}
		older := fileData(t, fsys, "checkpoint") // of generation 1
		if err := compactDB(db, Rewrite{}); err != nil {
			t.Fatal(err)
		}
		for i := 6; i < 9; i++ {
			change(t, db, i)
		}
		want := inMemory(t, db)
		closeDB(t, db)
		setFile(t, fsys, "checkpoint", older)
		got := salvage(t, fsys, salvageOptions)
		if got.plan.Checkpoint != "stale" || got.plan.Source != FromLog || got.plan.Generation != 2 || got.plan.Seq != 9 || !got.plan.Empty() ||
			len(got.plan.Recovery) != 1 || got.state != want {
			t.Errorf("plan %+v", got.plan)
		}
	})
}

// eventNumbers returns the numbers of the events of the record r of the store fsys.
func eventNumbers(t *testing.T, fsys vfs.FS, r recLoc) (lo, hi uint64) {
	t.Helper()
	b := fileData(t, fsys, r.name)
	gen, index, _ := parseSegName(path.Base(r.name))
	h, err := parseSegHeader(r.path(), b, gen, index)
	if err != nil {
		t.Fatal(err)
	}
	rec, _, _, _, reason := readFrame(b[r.off:], h.salt)
	if reason != "" {
		t.Fatal(reason)
	}
	return eventBounds(&rec)
}

// TestSalvageBase: in a generation after the first, a base record that cannot be read before the record of the
// checkpoint costs its events; with no checkpoint, there is no state: the store is refused.
func TestSalvageBase(t *testing.T) {
	withBaseBytes(t, 300)
	s := newSStore(t, salvageOptions, 9, 6)
	recs := locateRecords(t, s.fsys, 2)
	var base []recLoc
	var end recLoc
	for _, r := range recs {
		switch r.typ {
		case recBase:
			base = append(base, r)
		case recBaseEnd:
			end = r
		case recCommit:
		}
	}
	if len(base) < 4 || end.seq != 6 {
		t.Fatalf("%d base records, the base-end %+v", len(base), end)
	}
	t.Run("with the checkpoint", func(t *testing.T) {
		events := 0
		for i, r := range base {
			for _, d := range recordDamages {
				t.Run(fmt.Sprintf("%d, %s", i, d.name), func(t *testing.T) {
					fsys := s.copy()
					d.apply(t, fsys, r)
					got := salvage(t, fsys, s.o)
					lo, hi := eventNumbers(t, s.fsys, r)
					sp := Span{Problem: Problem{Path: r.path(), Offset: r.off, Reason: d.reason}, Length: int64(r.n), FirstSeq: 6, LastSeq: 6,
						FirstEvent: lo, LastEvent: hi}
					want := RepairPlan{Generation: 2, Seq: 9, Source: FromCheckpoint, Checkpoint: "used", Spans: []Span{sp}}
					if d.name == "garbage" {
						want.Objects = lostIn(t, inlineObjects(t, s.fsys, s.o), r, 2)
					}
					checkPlan(t, got.plan, want)
					var lost []int
					for e := lo; hi > 0 && e <= hi; e++ {
						lost = append(lost, int(e))
					}
					events += len(lost)
					if got.state != s.states[9] || !slices.Equal(got.events, withoutEvents(s.events[9], lost...)) {
						t.Errorf("state:\n%s\nevents %v", got.state, got.events)
					}
				})
			}
		}
		if events == 0 {
			t.Error("no base record has an event")
		}
	})
	t.Run("the base-end, with the checkpoint", func(t *testing.T) {
		fsys := s.copy()
		flipByte(t, fsys, end.name, end.off+9)
		got := salvage(t, fsys, s.o)
		sp := Span{Problem: Problem{Path: end.path(), Offset: end.off, Reason: "bad record CRC"}, Length: int64(end.n), FirstSeq: 6, LastSeq: 6}
		checkPlan(t, got.plan, RepairPlan{Generation: 2, Seq: 9, Source: FromCheckpoint, Checkpoint: "used", Spans: []Span{sp}})
		s.checkKept(t, got, 9)
	})
	t.Run("a segment of the base is missing, with the checkpoint", func(t *testing.T) {
		fsys := s.copy()
		removeFile(t, fsys, segName(2, 2))
		got := salvage(t, fsys, s.o)
		if len(got.plan.Spans) != 1 || got.plan.Spans[0].Reason != "missing segment" || got.plan.Spans[0].FirstSeq != 6 ||
			got.plan.Truncation != nil || got.state != s.states[9] {
			t.Errorf("plan %+v", got.plan)
		}
	})
	noCkp := func() *simfs.FS {
		fsys := s.copy()
		dropCheckpoint(t, fsys)
		return fsys
	}
	refusal := func(r recLoc, off int64, reason string) string {
		p := r.path()
		if off < 0 {
			return fmt.Sprintf("%s: corrupt: %s; the base of generation 2 cannot be read, and no checkpoint has its state: restore the store from a copy", p, reason)
		}
		return fmt.Sprintf("%s: corrupt at offset %d: %s; the base of generation 2 cannot be read, and no checkpoint has its state: restore the store from a copy", p, off, reason)
	}
	t.Run("without the checkpoint", func(t *testing.T) {
		for i, r := range base {
			for _, d := range recordDamages {
				fsys := noCkp()
				d.apply(t, fsys, r)
				_, err := trySalvage(t, fsys, s.o)
				var ce *CorruptError
				if !errors.As(err, &ce) || err.Error() != refusal(r, r.off, d.reason) {
					t.Errorf("base record %d, %s: %v", i, d.name, err)
				}
			}
		}
		fsys := noCkp()
		flipByte(t, fsys, end.name, end.off+9)
		if _, err := trySalvage(t, fsys, s.o); err == nil || err.Error() != refusal(end, end.off, "bad record CRC") {
			t.Errorf("the base-end: %v", err)
		}
		fsys = noCkp()
		removeFile(t, fsys, segName(2, 1))
		if _, err := trySalvage(t, fsys, s.o); err == nil || err.Error() != refusal(recLoc{name: segName(2, 1)}, -1, "missing segment") {
			t.Errorf("a missing segment: %v", err)
		}
	})
	t.Run("without the checkpoint, after the base", func(t *testing.T) {
		r := commitAt(t, recs, 8)
		fsys := noCkp()
		flipByte(t, fsys, r.name, r.off+9)
		got := salvage(t, fsys, s.o)
		sp := span(r, "bad record CRC")
		checkPlan(t, got.plan, RepairPlan{Generation: 2, Seq: 7, Source: FromLog, Checkpoint: "none", Spans: []Span{sp},
			Truncation: &Truncation{Damage: sp.Problem, FirstSeq: 9, LastSeq: 9, FirstEvent: 8, LastEvent: 9}})
		s.checkKept(t, got, 7)
	})
	t.Run("a checkpoint that is not this log's, and a base that cannot be read", func(t *testing.T) {
		fsys := s.copy()
		setCkpHeader(t, fsys, func(h *ckpHeader) { h.recCRC++ })
		flipByte(t, fsys, base[1].name, base[1].off+9)
		_, err := trySalvage(t, fsys, s.o)
		if err == nil || err.Error() != refusal(base[1], base[1].off, "bad record CRC") {
			t.Errorf("Salvage: %v", err)
		}
	})
}

// TestSalvageObjects: an object of the kept state whose data cannot be read or does not have its hash is lost, with the
// problem that VerifyEach finds; the state still names it.
func TestSalvageObjects(t *testing.T) {
	v := newVerifyStore(t)
	for i, dmg := range objectDamages {
		t.Run(dmg.name, func(t *testing.T) {
			fsys := copyFS(v.fsys)
			h := v.files[i%len(v.files)]
			p, _ := dmg.apply(t, fsys, h)
			if dmg.name == "a header of a newer version" {
				_, err := trySalvage(t, fsys, verifyOptions)
				var ve *VersionError
				if !errors.As(err, &ve) || ve.Path != simPath(objName(h)) {
					t.Errorf("Salvage: %v", err)
				}
				return
			}
			got := salvage(t, fsys, verifyOptions)
			checkPlan(t, got.plan, RepairPlan{Generation: 1, Seq: 10, Source: FromCheckpoint, Checkpoint: "used",
				Objects: []LostObject{{Hash: h, Problem: p}}})
			db := reopen(t, copyFS(v.fsys), verifyOptions)
			if got.state != inMemory(t, db) {
				t.Errorf("the state differs from that of the sound store")
			}
		})
	}
	t.Run("an inline object whose data is changed in a record that can be read", func(t *testing.T) {
		fsys := copyFS(v.fsys)
		h := v.afterFirst(t)
		p, _ := damageInline(t, fsys, v, h)
		got := salvage(t, fsys, verifyOptions)
		checkPlan(t, got.plan, RepairPlan{Generation: 1, Seq: 10, Source: FromCheckpoint, Checkpoint: "used", Objects: []LostObject{{Hash: h, Problem: p}}})
	})
	t.Run("a record and the four kinds of damage of the files, in the order of the hashes", func(t *testing.T) {
		fsys := copyFS(v.fsys)
		want, _ := damageAll(t, fsys, v)
		got := salvage(t, fsys, verifyOptions)
		var lost []Problem
		for _, o := range got.plan.Objects {
			lost = append(lost, o.Problem)
		}
		if !slices.Equal(lost, want[1:]) || len(got.plan.Spans) != 1 || got.plan.Spans[0].Problem != want[0] {
			t.Errorf("objects:%s\nwant:%s\nspans %+v", problemLines(lost), problemLines(want[1:]), got.plan.Spans)
		}
	})
	t.Run("the record with a row whose key and value are marked", func(t *testing.T) {
		r := commitAt(t, locateRecords(t, v.fsys, 1), 10)
		for _, d := range recordDamages {
			fsys := copyFS(v.fsys)
			d.apply(t, fsys, r)
			got := salvage(t, fsys, verifyOptions) // the problems have no key and no value
			if len(got.plan.Spans) != 1 || got.plan.Source != FromCheckpointAlone || got.plan.Seq != 10 {
				t.Errorf("%s: plan %+v", d.name, got.plan)
			}
		}
	})
	t.Run("a file that this process may not read stops it", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads anywhere")
		}
		dir := t.TempDir()
		db, err := Create(osFS(t, dir), salvageOptions)
		if err != nil {
			t.Fatal(err)
		}
		for i := range 4 {
			change(t, db, i)
		}
		closeDB(t, db)
		var file string
		_ = filepath.WalkDir(filepath.Join(dir, "objects"), func(p string, e fs.DirEntry, err error) error {
			if err == nil && e.Type().IsRegular() {
				file = p
			}
			return nil
		})
		if file == "" {
			t.Fatal("no object file")
		}
		if err := os.Chmod(file, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(file, 0o600) })
		err = Salvage(context.Background(), osFS(t, dir), salvageOptions, func(*Salvager) error {
			t.Error("f is called")
			return nil
		})
		if !errors.Is(err, fs.ErrPermission) {
			t.Errorf("Salvage: %v", err)
		}
	})
}

// TestSalvageGenerations: the segments of the other generations, and the temporary files, are what a repair removes; a
// generation that is newer and incomplete is not used.
func TestSalvageGenerations(t *testing.T) {
	withBaseBytes(t, 200)
	t.Run("an incomplete newer generation", func(t *testing.T) {
		fsys := simfs.New(simDir)
		armed := false
		o := Options{SegmentBytes: 400, Placement: Hybrid, Threshold: 100, Hook: func(p string) error {
			if armed && p == "compact:base" {
				fsys.CrashAt(fsys.Counted())
			}
			return nil
		}}
		db, err := Create(fsys, o)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		for i := range 8 {
			change(t, db, i)
		}
		if err := db.Checkpoint(); err != nil {
			t.Fatal(err)
		}
		want := inMemory(t, db)
		armed = true
		if err := compactDB(db, Rewrite{}); !errors.Is(err, simfs.ErrCrashed) {
			t.Fatalf("compact: %v", err)
		}
		crashed := fsys.Crash(loseAll)
		o.Hook = nil
		if gens := gensOf(t, crashed); !slices.Equal(gens, []uint64{1, 2}) {
			t.Fatalf("generations %v", gens)
		}
		got := salvage(t, crashed, o)
		var remove []string
		for _, name := range logNames(t, crashed) {
			if strings.HasPrefix(path.Base(name), "0000000000000002-") {
				remove = append(remove, simPath(name))
			}
		}
		checkPlan(t, got.plan, RepairPlan{Generation: 1, Seq: 8, Source: FromCheckpoint, Checkpoint: "used", Remove: remove,
			Recovery: recoveryProblemsOf(t, copyFS(crashed))})
		if len(got.plan.Recovery) != 1 || !strings.Contains(got.plan.Recovery[0].Reason, "generation 2 is newer") || len(remove) == 0 {
			t.Errorf("plan %+v", got.plan)
		}
		if got.state != want {
			t.Errorf("state:\n%s\nwant\n%s", got.state, want)
		}
	})
	t.Run("the segments of the older generation, and temporary files", func(t *testing.T) {
		fsys, db := newSim(t, salvageOptions)
		for i := range 5 {
			change(t, db, i)
		}
		old := map[string][]byte{}
		for _, name := range logNames(t, fsys) {
			old[name] = fileData(t, fsys, name)
		}
		if err := compactDB(db, Rewrite{}); err != nil {
			t.Fatal(err)
		}
		for i := 5; i < 9; i++ {
			change(t, db, i)
		}
		want := inMemory(t, db)
		closeDB(t, db)
		for name, data := range old {
			setFile(t, fsys, name, data)
		}
		setFile(t, fsys, ".tmp-checkpoint-0123456789abcdef", []byte("x"))
		if err := fsys.Mkdir("objects/ab", 0o700); err != nil && !exists(t, fsys, "objects/ab") {
			t.Fatal(err)
		}
		setFile(t, fsys, "objects/ab/.tmp-0123456789abcdef", []byte("x"))
		got := salvage(t, fsys, salvageOptions)
		var remove []string
		for _, name := range slices.Sorted(maps.Keys(old)) {
			remove = append(remove, simPath(name))
		}
		remove = append(remove, simPath(".tmp-checkpoint-0123456789abcdef"), simPath("objects/ab/.tmp-0123456789abcdef"))
		checkPlan(t, got.plan, RepairPlan{Generation: 2, Seq: 9, Source: FromCheckpoint, Checkpoint: "used", Remove: remove})
		if got.state != want {
			t.Errorf("state:\n%s", got.state)
		}
	})
}

// TestSalvageCheckpointGeneration: a checkpoint names a generation of which no segment is left: it is the kept one, and
// every segment of it is missing.
func TestSalvageCheckpointGeneration(t *testing.T) {
	withBaseBytes(t, 300)
	fsys, db := newSim(t, salvageOptions)
	for i := range 5 {
		change(t, db, i)
	}
	old := map[string][]byte{}
	for _, name := range logNames(t, fsys) {
		old[name] = fileData(t, fsys, name)
	}
	if err := compactDB(db, Rewrite{}); err != nil {
		t.Fatal(err)
	}
	for i := 5; i < 9; i++ {
		change(t, db, i)
	}
	want := inMemory(t, db)
	closeDB(t, db)
	ck := ckpOf(t, fsys)
	for _, name := range logNames(t, fsys) {
		if strings.Contains(name, "0000000000000002-") {
			removeFile(t, fsys, name)
		}
	}
	for name, data := range old {
		setFile(t, fsys, name, data)
	}
	got := salvage(t, fsys, salvageOptions)
	if got.plan.Generation != 2 || got.plan.Seq != 9 || got.plan.Source != FromCheckpointAlone || len(got.plan.Spans) != int(ck.seg) ||
		len(got.plan.Remove) != len(old) || len(got.events) != 0 || got.state != want {
		t.Fatalf("plan %+v", got.plan)
	}
	for i, sp := range got.plan.Spans {
		if sp.Path != simPath(segName(2, segNum(i+1))) || sp.Offset != -1 || sp.Reason != "missing segment" || sp.FirstEvent != 1 || sp.LastEvent != 9 ||
			sp.FirstSeq != 0 || sp.LastSeq != 0 {
			t.Errorf("span %d: %+v", i, sp)
		}
	}
	t.Run("too many missing segments", func(t *testing.T) {
		far := copyFS(fsys)
		setCkpHeader(t, far, func(h *ckpHeader) { h.seg = 1 << 40 })
		if _, err := trySalvage(t, far, salvageOptions); err == nil || !strings.Contains(err.Error(), "too many missing segments") {
			t.Errorf("Salvage: %v", err)
		}
	})
}

// TestSalvageLayout: a store that lacks a file of its layout is refused as Open refuses it, except for head, which a
// repair writes again, and which may be damaged.
func TestSalvageLayout(t *testing.T) {
	ctx := context.Background()
	base, db := newSim(t, Options{})
	putKey(t, db, 1, "a", "1")
	closeDB(t, db)
	for _, name := range []string{"lock/entry", "lock/readers", "lock/writer", "objects", "log"} {
		t.Run(name, func(t *testing.T) {
			fsys := copyFS(base)
			if name == "log" {
				for _, seg := range logNames(t, fsys) {
					removeFile(t, fsys, seg)
				}
			}
			removeFile(t, fsys, name)
			_, openErr := Open(copyFS(fsys), Options{})
			if openErr == nil {
				t.Fatal("Open opens it")
			}
			called := false
			err := Salvage(ctx, fsys, Options{}, func(*Salvager) error {
				called = true
				return nil
			})
			if err == nil || err.Error() != openErr.Error() || called {
				t.Errorf("Salvage: %v, f called: %v; Open: %v", err, called, openErr)
			}
		})
	}
	for _, c := range []struct {
		name string
		mess func(t *testing.T, fsys *simfs.FS)
	}{
		{"head is missing", func(t *testing.T, fsys *simfs.FS) { removeFile(t, fsys, "head") }},
		{"head is damaged", func(t *testing.T, fsys *simfs.FS) {
			flipByte(t, fsys, "head", 20)
			flipByte(t, fsys, "head", headSlotSize+20)
		}},
		{"head is short", func(t *testing.T, fsys *simfs.FS) { setFile(t, fsys, "head", []byte("whrm")) }},
		{"head is empty", func(t *testing.T, fsys *simfs.FS) { setFile(t, fsys, "head", nil) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			fsys := copyFS(base)
			c.mess(t, fsys)
			got := salvage(t, fsys, Options{})
			if !got.plan.Empty() || got.plan.Seq != 1 || got.state != "1/a=1\nobjects:\n" {
				t.Errorf("plan %+v, state %q", got.plan, got.state)
			}
			if r := got.plan.Recovery; len(r) != 1 || r[0].Path != simPath("head") || r[0].Offset != -1 {
				t.Errorf("recovery %v", r)
			}
		})
	}
	t.Run("a slot of head is damaged", func(t *testing.T) {
		fsys := copyFS(base)
		flipByte(t, fsys, "head", 20)
		if got := salvage(t, fsys, Options{}); !got.plan.Empty() || len(got.plan.Recovery) != 0 {
			t.Errorf("plan %+v", got.plan)
		}
	})
	t.Run("head is a directory", func(t *testing.T) {
		fsys := copyFS(base)
		removeFile(t, fsys, "head")
		if err := fsys.Mkdir("head", 0o700); err != nil {
			t.Fatal(err)
		}
		_, openErr := Open(copyFS(fsys), Options{})
		if _, err := trySalvage(t, fsys, Options{}); err == nil || openErr == nil || err.Error() != openErr.Error() {
			t.Errorf("Salvage: %v; Open: %v", err, openErr)
		}
	})
	t.Run("a name in log/ that is not a segment's", func(t *testing.T) {
		fsys := copyFS(base)
		setFile(t, fsys, "log/notes", []byte("x"))
		_, openErr := Open(copyFS(fsys), Options{})
		if _, err := trySalvage(t, fsys, Options{}); err == nil || openErr == nil || err.Error() != openErr.Error() ||
			!strings.Contains(err.Error(), "not the name of a segment") {
			t.Errorf("Salvage: %v; Open: %v", err, openErr)
		}
	})
	t.Run("options", func(t *testing.T) {
		_, openErr := Open(copyFS(base), Options{Placement: 99})
		err := Salvage(ctx, copyFS(base), Options{Placement: 99}, func(*Salvager) error { return errors.New("called") })
		if err == nil || openErr == nil || err.Error() != openErr.Error() {
			t.Errorf("Salvage: %v; Open: %v", err, openErr)
		}
	})
}

// TestSalvageNewerVersion: a file of a newer version is refused, also if Open does not read it.
func TestSalvageNewerVersion(t *testing.T) {
	v := newVerifyStore(t)
	for _, name := range []string{seg1, lastSegment(t, v.fsys), "checkpoint", "head"} {
		t.Run(name, func(t *testing.T) {
			fsys := copyFS(v.fsys)
			versionBump(t, fsys, name)
			_, err := trySalvage(t, fsys, verifyOptions)
			var ve *VersionError
			if !errors.As(err, &ve) || ve.Path != simPath(name) {
				t.Errorf("Salvage: %v", err)
			}
		})
	}
}

// TestSalvageWaits: Salvage waits for the writer and the readers to close, and for maintenance to end, until its
// context ends.
func TestSalvageWaits(t *testing.T) {
	ctx := context.Background()
	fsys, db := newSim(t, Options{})
	putKey(t, db, 1, "a", "1")
	closeDB(t, db)
	var called atomic.Bool
	f := func(*Salvager) error {
		called.Store(true)
		return nil
	}
	short := func(what string) {
		t.Helper()
		before, counted := treeOf(t, fsys), fsys.Counted()
		start := time.Now()
		c, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		err := Salvage(c, fsys, Options{}, f)
		if d := time.Since(start); d < 100*time.Millisecond {
			t.Errorf("%s: Salvage waited %v", what, d)
		}
		if !errors.Is(err, context.DeadlineExceeded) || called.Load() {
			t.Errorf("%s: Salvage: %v, f called: %v", what, err, called.Load())
		}
		if !maps.Equal(before, treeOf(t, fsys)) || fsys.Counted() != counted {
			t.Errorf("%s: Salvage changed files", what)
		}
	}
	writer := reopen(t, fsys, Options{})
	short("a writer")
	closeDB(t, writer)
	reader := openReader(t, fsys, Options{})
	short("a reader")
	closeDB(t, reader)
	release := lockFile(t, fsys, "lock/writer", vfs.Shared)
	short("lock/writer")
	release()
	for _, other := range []struct {
		name string
		run  func(ctx context.Context, f func() error) error
	}{
		{"Maintain", func(ctx context.Context, f func() error) error {
			return Maintain(ctx, fsys, Options{}, func(*Maintenance) error { return f() })
		}},
		{"Inspect", func(ctx context.Context, f func() error) error {
			return Inspect(ctx, fsys, Options{}, func(*Inspection) error { return f() })
		}},
		{"Salvage", func(ctx context.Context, f func() error) error {
			return Salvage(ctx, fsys, Options{}, func(*Salvager) error { return f() })
		}},
	} {
		entered, block, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		go func() {
			done <- other.run(ctx, func() error {
				close(entered)
				<-block
				return nil
			})
		}()
		wait(t, entered, other.name)
		short(other.name)
		close(block)
		if err := wait(t, done, other.name); err != nil {
			t.Errorf("%s: %v", other.name, err)
		}
	}
	// It goes on once they are closed.
	reader = openReader(t, fsys, Options{})
	done := make(chan error, 1)
	go func() { done <- Salvage(ctx, fsys, Options{}, f) }()
	select {
	case err := <-done:
		t.Fatalf("Salvage did not wait for the reader: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	closeDB(t, reader)
	if err := wait(t, done, "Salvage"); err != nil || !called.Load() {
		t.Errorf("Salvage: %v, f called: %v", err, called.Load())
	}
}

// TestSalvageOpenersDuring: while Salvage runs, or waits for the processes that have the store open, the others fail
// at once.
func TestSalvageOpenersDuring(t *testing.T) {
	ctx := context.Background()
	s := newSStore(t, salvageOptions, 5, 0)
	fsys := s.fsys
	refused := func(what string, err error) {
		t.Helper()
		if err == nil || err.Error() != simDir+": maintenance is running" {
			t.Errorf("%s during Salvage: %v", what, err)
		}
	}
	openers := func() {
		t.Helper()
		_, err := Open(fsys, Options{WriterWait: time.Second})
		refused("Open", err)
		_, err = OpenReader(fsys, Options{})
		refused("OpenReader", err)
		_, err = Backup(ctx, fsys, simfs.New("/backup"), nil)
		refused("Backup", err)
	}
	err := Salvage(ctx, fsys, s.o, func(sv *Salvager) error {
		openers()
		if err := sv.Events(ctx, func(Event) bool { return true }); err != nil {
			return err
		}
		openers()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	reader := openReader(t, fsys, s.o)
	done := make(chan error, 1)
	go func() { done <- Salvage(ctx, fsys, s.o, func(*Salvager) error { return nil }) }()
	waitLocked(t, fsys, "lock/entry")
	openers()
	closeDB(t, reader)
	if err := wait(t, done, "Salvage"); err != nil {
		t.Error(err)
	}
	closeDB(t, reopen(t, fsys, Options{WriterWait: time.Second})) // and they open it again
}

func TestSalvageLockReleases(t *testing.T) {
	ctx := context.Background()
	fsys, db := newSim(t, Options{})
	closeDB(t, db)
	boom := errors.New("boom")
	if err := Salvage(ctx, fsys, Options{}, func(*Salvager) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("Salvage: %v", err)
	}
	closeDB(t, reopen(t, fsys, Options{WriterWait: time.Second}))
	func() {
		defer func() {
			if r := recover(); r != "boom" {
				t.Errorf("recovered %v", r)
			}
		}()
		_ = Salvage(ctx, fsys, Options{}, func(*Salvager) error { panic("boom") })
	}()
	closeDB(t, reopen(t, fsys, Options{WriterWait: time.Second}))
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	called := false
	if err := Salvage(canceled, fsys, Options{}, func(*Salvager) error { called = true; return nil }); !errors.Is(err, context.Canceled) || called {
		t.Errorf("Salvage with a canceled context: %v, f called: %v", err, called)
	}
	closeDB(t, reopen(t, fsys, Options{WriterWait: time.Second}))
	// A store that is refused is released too.
	removeFile(t, fsys, lastSegment(t, fsys))
	if err := Salvage(ctx, fsys, Options{}, func(*Salvager) error { called = true; return nil }); err == nil || called {
		t.Errorf("Salvage of a store without segments: %v", err)
	}
	if _, err := Open(fsys, Options{WriterWait: time.Second}); err == nil || err.Error() == simDir+": maintenance is running" {
		t.Errorf("Open: %v", err)
	}
}

// TestSalvageContext: whenever the context ends, Salvage returns its error and does not call f, and the locks are
// released.
func TestSalvageContext(t *testing.T) {
	ctx := context.Background()
	s := newSStore(t, salvageOptions, 9, 0)
	recs := locateRecords(t, s.fsys, 1)
	fsys := s.copy()
	flipByte(t, fsys, recs[2].name, recs[2].off+9)
	h := v2Hash(t, s)
	objectDamages[0].apply(t, fsys, h)
	want := salvage(t, fsys, s.o)
	for n := 0; ; n++ {
		called := false
		err := Salvage(&countdownContext{Context: ctx, left: n}, fsys, s.o, func(sv *Salvager) error {
			called = true
			if !slices.Equal(sv.Plan().Spans, want.plan.Spans) {
				t.Errorf("the spans differ after %d calls", n)
			}
			return nil
		})
		if err == nil {
			if !called {
				t.Fatal("f is not called")
			}
			break
		}
		if !errors.Is(err, context.Canceled) || called {
			t.Fatalf("Salvage with a context that ends after %d calls: %v, f called: %v", n, err, called)
		}
		if n > 10000 {
			t.Fatal("the context does not end Salvage")
		}
	}
	closeDB(t, reopen(t, fsys, Options{WriterWait: time.Second}))
	// The context of Events and View.
	err := Salvage(ctx, fsys, s.o, func(sv *Salvager) error {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if err := sv.Events(canceled, func(Event) bool { t.Error("fn is called"); return true }); !errors.Is(err, context.Canceled) {
			t.Errorf("Events: %v", err)
		}
		if err := sv.View(canceled, func(*Tx) error { t.Error("f is called"); return nil }); !errors.Is(err, context.Canceled) {
			t.Errorf("View: %v", err)
		}
		n := 0
		if err := sv.Events(ctx, func(Event) bool { n++; return n < 3 }); err != nil || n != 3 {
			t.Errorf("Events stopped: %v, %d events", err, n)
		}
		if err := sv.View(ctx, func(tx *Tx) error {
			if err := tx.Put(1, []byte("a"), []byte("b")); !errors.Is(err, ErrReadOnly) {
				t.Errorf("Put: %v", err)
			}
			if tx.Seq() != 9 {
				t.Errorf("Seq: %d", tx.Seq())
			}
			return nil
		}); err != nil {
			t.Errorf("View: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Error(err)
	}
}

// v2Hash returns the hash of an object in a file of the store.
func v2Hash(t *testing.T, s *sStore) [32]byte {
	t.Helper()
	var h [32]byte
	found := false
	reopen(t, s.copy(), s.o).cur.Load().objects.Ascend("", func(k string, loc objLoc) bool {
		if loc.placement == placeFile {
			h, found = [32]byte([]byte(k)), true
		}
		return !found
	})
	if !found {
		t.Fatal("no object in a file")
	}
	return h
}

// TestSalvageReadOnlyDirectory: a store whose directory and files cannot be written, and whose record before the
// checkpoint is damaged, is read.
func TestSalvageReadOnlyDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	dir := t.TempDir()
	o := salvageOptions
	db, err := Create(osFS(t, dir), o)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		change(t, db, i)
	}
	closeDB(t, db)
	segs := logNames(t, osFS(t, dir))
	b, err := os.ReadFile(filepath.Join(dir, segs[1]))
	if err != nil {
		t.Fatal(err)
	}
	b[segHeaderSize+9] ^= 1
	if err := os.WriteFile(filepath.Join(dir, segs[1]), b, 0o600); err != nil {
		t.Fatal(err)
	}
	chmod := func(file, dirs fs.FileMode) {
		if err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() {
				return os.Chmod(p, dirs)
			}
			return os.Chmod(p, file)
		}); err != nil {
			t.Fatal(err)
		}
	}
	chmod(0o400, 0o500)
	t.Cleanup(func() { chmod(0o600, 0o700) })
	fsys := osFS(t, dir)
	before := treeOf(t, fsys)
	got, err := trySalvage(t, fsys, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.plan.Spans) != 1 || got.plan.Seq != 6 || got.plan.Truncation != nil || !maps.Equal(before, treeOf(t, fsys)) {
		t.Errorf("plan %+v", got.plan)
	}
}

// matrixImage is a store that TestSalvageDamageMatrix damages.
type matrixImage struct {
	fsys   *simfs.FS
	o      Options
	names  []string
	states []string
	events []string
}

func matrixImages(t *testing.T) []matrixImage {
	t.Helper()
	cryptotest.SetGlobalRandom(t, 1) // the salts are those of the seed 1, so that a failing damage fails again
	var out []matrixImage
	for image := range uint8(4) {
		if fuzzStores[image] == nil {
			fuzzStores[image] = newFuzzStore(t, image&1 != 0, image&2 != 0)
		}
		b := fuzzStores[image]
		out = append(out, matrixImage{fsys: b.fsys, o: fuzzOptions, names: b.names, states: b.states, events: b.events})
	}
	withBaseBytes(t, 300)
	for _, tail := range []*sStore{newTailStore(t, salvageOptions, 3, 9), newTailStoreAfter(t, salvageOptions, 4, 6, 9)} {
		img := matrixImage{fsys: tail.fsys, o: tail.o, states: tail.states, events: tail.events}
		tail.fsys.Walk(func(p string, _ []byte) { img.names = append(img.names, p) })
		out = append(out, img)
	}
	return out
}

func isSubsequence(got, want []string) bool {
	i := 0
	for _, l := range want {
		if i < len(got) && got[i] == l {
			i++
		}
	}
	return i == len(got)
}

// TestSalvageDamageMatrix damages one file of a store in the ways of FuzzOpen, and checks what Salvage does: it refuses
// with a CorruptError or a VersionError, or keeps the state after one of the commits of the store, and the events and
// the objects of it that it can read, and changes no file. If Open opens the store, Salvage keeps what Open does.
func TestSalvageDamageMatrix(t *testing.T) {
	images := matrixImages(t)
	iterations := 200
	if os.Getenv("WHITEROOM_LONG_TESTS") != "" {
		iterations = 20000
	}
	rng := rand.New(rand.NewPCG(7, 11))
	patterns := [][]byte{{0xff}, {0}, {1}, make([]byte, 40), []byte("whrm-log\x02\x00\x00\x00"), {0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa}}
	refused, kept, spans, truncated := 0, 0, 0, 0
	for n := range iterations * len(images) {
		img := images[n%len(images)]
		fsys := copyFS(img.fsys)
		name := img.names[rng.IntN(len(img.names))]
		b := fileData(t, fsys, name)
		at := rng.IntN(len(b) + 1)
		data := patterns[rng.IntN(len(patterns))]
		if rng.IntN(3) == 0 {
			data = make([]byte, 1+rng.IntN(60))
			for i := range data {
				data[i] = byte(rng.IntN(256))
			}
		}
		mode := rng.IntN(5)
		removed := ""
		switch mode {
		case 0:
			b = slices.Concat(b[:at], data, b[min(len(b), at+len(data)):])
		case 1:
			b = b[:at]
		case 2:
			b = data
		case 3:
			b = slices.Concat(b, data)
		case 4:
			removed = name
		}
		if removed != "" {
			removeFile(t, fsys, name)
		} else {
			setFile(t, fsys, name, b)
		}
		what := fmt.Sprintf("image %d, %s, mode %d, offset %d, data %x", n%len(images), name, mode, at, data)
		res, err := trySalvage(t, fsys, img.o)
		db, openErr := Open(copyFS(fsys), img.o)
		if err != nil {
			var ve *VersionError
			if !isRefusal(err, removed) {
				t.Fatalf("%s: Salvage: %v (%T)", what, err, err)
			}
			if openErr == nil && !errors.As(err, &ve) {
				t.Fatalf("%s: Open opens the store, and Salvage refuses it: %v", what, err)
			}
			if openErr == nil {
				_ = db.Close()
			}
			refused++
			continue
		}
		kept++
		p := res.plan
		if p.Seq >= uint64(len(img.states)) {
			t.Fatalf("%s: plan %+v", what, p)
		}
		if res.state != img.states[p.Seq] {
			t.Fatalf("%s: the state at sequence %d is\n%s\nwant\n%s\nplan %+v", what, p.Seq, res.state, img.states[p.Seq], p)
		}
		all := strings.Split(img.events[p.Seq], "\n")
		if !isSubsequence(res.events, all) || p.Empty() && len(res.events) != len(withoutEvents(img.events[p.Seq])) {
			t.Fatalf("%s: events\n%v\nof\n%v\nplan %+v", what, res.events, all, p)
		}
		if p.Truncation != nil {
			truncated++
			if !slices.ContainsFunc(p.Spans, func(s Span) bool { return s.Problem == p.Truncation.Damage }) {
				t.Fatalf("%s: the damage of the truncation is no span: %+v", what, p)
			}
		}
		// Every event of the commits that the state has, and that is not kept, is in a span; every event of the commits
		// that the truncation drops is in it or in a span.
		inSpan := func(n uint64) bool {
			return slices.ContainsFunc(p.Spans, func(s Span) bool { return s.FirstEvent != 0 && s.FirstEvent <= n && n <= s.LastEvent })
		}
		for _, line := range strings.Split(img.events[len(img.events)-1], "\n") {
			var number, seq uint64
			if _, err := fmt.Sscanf(line, "%d/%d=", &number, &seq); err != nil || slices.Contains(res.events, line) {
				continue
			}
			switch tr := p.Truncation; {
			case seq <= p.Seq && !inSpan(number):
				t.Fatalf("%s: the event %q is lost, and is in no span: %+v", what, line, p)
			case seq > p.Seq && tr != nil && seq <= tr.LastSeq && !inSpan(number) && (number < tr.FirstEvent || number > tr.LastEvent):
				t.Fatalf("%s: the event %q is dropped, and is in no span and not in the truncation: %+v", what, line, p)
			}
		}
		for _, s := range p.Spans {
			spans++
			if s.Path == "" || s.Offset < -1 || s.Length < 0 || s.FirstSeq > s.LastSeq || s.FirstEvent > s.LastEvent {
				t.Fatalf("%s: span %+v", what, s)
			}
		}
		if openErr == nil {
			info := db.Recovery()
			if info.Seq != p.Seq || info.Generation != p.Generation || inMemory(t, db) != res.state || p.Truncation != nil {
				t.Fatalf("%s: Salvage keeps sequence %d of generation %d, Open %+v\nplan %+v", what, p.Seq, p.Generation, info, p)
			}
			if db.Verify(context.Background()) == nil && eventLines(t, db) != res.eventLines() {
				t.Fatalf("%s: events\n%v\nOpen has\n%s", what, res.events, eventLines(t, db))
			}
			_ = db.Close()
		}
	}
	t.Logf("%d refused, %d kept, with %d spans, %d truncations", refused, kept, spans, truncated)
	if refused == 0 || kept == 0 || spans == 0 || truncated == 0 {
		t.Error("the damage does not cover the cases")
	}
}

// TestSalvageInconsistentRecord: a record whose frame is right and whose content is not, as an event with the sequence
// number of another commit, cannot be read: after the start it is the first damage, and the state keeps nothing of it.
func TestSalvageInconsistentRecord(t *testing.T) {
	s := newTailStore(t, salvageOptions, 3, 9)
	recs := locateRecords(t, s.fsys, 1)
	forge := func(t *testing.T, r recLoc) *simfs.FS {
		t.Helper()
		fsys := s.copy()
		forgeRecord(t, fsys, r, func(body []byte) {
			rec, reason := parseBody(body)
			if reason != "" {
				t.Fatal(reason)
			}
			for _, p := range rec.ops {
				if p.code == opEvent {
					body[p.valueOff-3]++ // the sequence number of the event
				}
			}
		})
		return fsys
	}
	t.Run("after the checkpoint", func(t *testing.T) {
		r := commitAt(t, recs, 6)
		got := salvage(t, forge(t, r), s.o)
		sp := span(r, "event sequence 7, want 6")
		checkPlan(t, got.plan, RepairPlan{Generation: 1, Seq: 5, Source: FromCheckpoint, Checkpoint: "used", Spans: []Span{sp},
			Truncation: &Truncation{Damage: sp.Problem, FirstSeq: 7, LastSeq: 9, FirstEvent: 6, LastEvent: 9}})
		s.checkKept(t, got, 5) // none of the changes of the record, which has an object and two puts before the event
	})
	t.Run("before the checkpoint", func(t *testing.T) {
		r := commitAt(t, recs, 2)
		got := salvage(t, forge(t, r), s.o)
		checkPlan(t, got.plan, RepairPlan{Generation: 1, Seq: 9, Source: FromCheckpoint, Checkpoint: "used",
			Spans: []Span{span(r, "event sequence 3, want 2")}})
		if got.state != s.states[9] || !slices.Equal(got.events, withoutEvents(s.events[9], 2)) {
			t.Errorf("state:\n%s\nevents %v", got.state, got.events)
		}
	})
}

// TestSalvageEventsAroundCheckpoint: the events that the checkpoint covers are those before its next event: a span that
// it covers does not take the events of a record after it, though no event comes between.
func TestSalvageEventsAroundCheckpoint(t *testing.T) {
	ctx := context.Background()
	fsys, db := newSim(t, salvageOptions)
	states := []string{inMemory(t, db)}
	commit := func(f func(*Tx) error) {
		t.Helper()
		if err := db.Update(ctx, f); err != nil {
			t.Fatal(err)
		}
		states = append(states, inMemory(t, db))
	}
	for i := range 3 {
		commit(changeF(i))
	}
	if err := db.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	commit(putF(1, "no event", "x")) // 4
	commit(changeF(4))               // 5, the event 4
	commit(changeF(5))               // 6, the event 5
	img := copyFS(fsys)
	recs := locateRecords(t, img, 1)
	r3, r5 := commitAt(t, recs, 3), commitAt(t, recs, 5)
	flipByte(t, img, r3.name, r3.off+9)
	flipByte(t, img, r5.name, r5.off+9)
	got := salvage(t, img, salvageOptions)
	a, b := span(r3, "bad record CRC"), span(r5, "bad record CRC")
	b.FirstEvent, b.LastEvent = 4, 4
	checkPlan(t, got.plan, RepairPlan{Generation: 1, Seq: 4, Source: FromCheckpointAlone, Checkpoint: "used", Spans: []Span{a, b},
		Truncation: &Truncation{Damage: b.Problem, FirstSeq: 6, LastSeq: 6, FirstEvent: 4, LastEvent: 5}})
	if got.state != states[4] || !slices.Equal(got.events, []string{"1/1=event 0", "2/2=event 1"}) {
		t.Errorf("state:\n%s\nevents %v", got.state, got.events)
	}
}
