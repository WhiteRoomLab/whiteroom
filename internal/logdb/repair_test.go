package logdb

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// repaired is what Apply did to a store.
type repaired struct {
	plan RepairPlan
	res  RepairResult
	err  error
}

// repair runs Salvage on fsys, and Apply with r in it. It fails the test if Salvage fails.
func repair(t *testing.T, fsys vfs.FS, o Options, r RepairOptions) *repaired {
	t.Helper()
	return repairCtx(t, context.Background(), fsys, o, r)
}

func repairCtx(t *testing.T, ctx context.Context, fsys vfs.FS, o Options, r RepairOptions) *repaired {
	t.Helper()
	out := &repaired{}
	err := Salvage(context.Background(), fsys, o, func(s *Salvager) error {
		out.plan = s.Plan()
		out.res, out.err = s.Apply(ctx, r)
		return nil
	})
	if err != nil {
		t.Fatalf("Salvage: %v", err)
	}
	return out
}

// outcome says what Open gives in fsys, which it changes: the error, or the generation, the sequence number, and the
// tables, objects and events as they are in memory and on disk.
func outcome(t *testing.T, fsys vfs.FS, o Options) string {
	t.Helper()
	db, err := Open(fsys, o)
	if err != nil {
		return "error: " + err.Error()
	}
	defer func() { _ = db.Close() }()
	ev, err := events(db)
	if err != nil {
		ev = "events: " + err.Error()
	}
	info := db.Recovery()
	return fmt.Sprintf("generation %d, sequence %d\n%s\nevents:\n%s", info.Generation, info.Seq, inMemory(t, db), ev)
}

// stateWithout returns the dump of a state without the objects lost.
func stateWithout(state string, lost []LostObject) string {
	rows, objs, _ := strings.Cut(state, "\nobjects:\n")
	var keep []string
	for _, l := range strings.Split(objs, "\n") {
		if l != "" && !slices.ContainsFunc(lost, func(o LostObject) bool { return strings.HasPrefix(l, fmt.Sprintf("%x:", o.Hash)) }) {
			keep = append(keep, l)
		}
	}
	return rows + "\nobjects:\n" + strings.Join(keep, "\n")
}

// withPuts returns the dump of a state with the rows of puts put in it.
func withPuts(state string, puts []RepairPut) string {
	rows, objs, _ := strings.Cut(state, "\nobjects:\n")
	type row struct {
		table uint32
		key   string
		line  string
	}
	var all []row
	set := func(table uint32, key, line string) {
		for i := range all {
			if all[i].table == table && all[i].key == key {
				all[i].line = line
				return
			}
		}
		all = append(all, row{table, key, line})
	}
	for _, l := range strings.Split(rows, "\n") {
		if l == "" {
			continue
		}
		table, rest, _ := strings.Cut(l, "/")
		key, _, _ := strings.Cut(rest, "=")
		n, err := strconv.ParseUint(table, 10, 32)
		if err != nil {
			panic(err)
		}
		set(uint32(n), key, l)
	}
	for _, p := range puts {
		set(p.Table, string(p.Key), fmt.Sprintf("%d/%s=%s", p.Table, p.Key, p.Value))
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].table != all[j].table {
			return all[i].table < all[j].table
		}
		return all[i].key < all[j].key
	})
	lines := make([]string, len(all))
	for i, r := range all {
		lines[i] = r.line
	}
	return strings.Join(lines, "\n") + "\nobjects:\n" + objs
}

// rewriteEvents returns the lines of events, as eventLines writes them, as f changes them.
func rewriteEvents(lines []string, f func(Event) ([]byte, bool)) []string {
	var out []string
	for _, l := range lines {
		head, data, _ := strings.Cut(l, "=")
		var e Event
		if _, err := fmt.Sscanf(head, "%d/%d", &e.Number, &e.Seq); err != nil {
			panic(err)
		}
		e.Data = []byte(data)
		if d, keep := f(e); keep {
			out = append(out, fmt.Sprintf("%d/%d=%s", e.Number, e.Seq, d))
		}
	}
	return out
}

// checkRepaired checks the store in fsys, which a repair of the generation gen, whose state is at the sequence number
// seq, has written: Open opens it in the next generation, from its checkpoint, Verify passes, it has the state and
// the events, no file is left that it does not need, and a second Salvage finds nothing.
func checkRepaired(t *testing.T, fsys *simfs.FS, o Options, gen, seq uint64, state, events string) {
	t.Helper()
	db := reopen(t, copyFS(fsys), o)
	want := "used"
	if seq == 0 {
		want = "none"
	}
	if info := db.Recovery(); info.Generation != gen+1 || info.Seq != seq || info.Checkpoint != want ||
		len(info.IgnoredGenerations) != 0 || info.TruncatedAt != -1 || info.RemovedSegment != "" {
		t.Errorf("Recovery: %+v, want generation %d, sequence %d, checkpoint %s", info, gen+1, seq, want)
	}
	if err := db.Verify(context.Background()); err != nil {
		t.Errorf("Verify: %v", err)
	}
	if got := inMemory(t, db); got != state {
		t.Errorf("state:\n%s\nwant\n%s", got, state)
	}
	if got := eventLines(t, db); got != events {
		t.Errorf("events:\n%s\nwant\n%s", got, events)
	}
	checkObjects(t, db)
	files := map[string]bool{}
	db.cur.Load().objects.Ascend("", func(k string, loc objLoc) bool {
		if loc.placement == placeFile {
			files[simPath(objName([32]byte([]byte(k))))] = true
		}
		return true
	})
	fsys.Walk(func(p string, _ []byte) {
		switch name := simPath(p); {
		case strings.HasPrefix(p, "log/"):
			if g, _, _ := parseSegName(path.Base(p)); uint64(g) != gen+1 {
				t.Errorf("%s is left", p)
			}
		case strings.HasPrefix(p, "objects/"):
			if !files[name] {
				t.Errorf("%s is left", p)
			}
		case strings.HasPrefix(path.Base(p), ".tmp-"):
			t.Errorf("%s is left", p)
		}
	})
	again := salvage(t, fsys, o)
	source := FromCheckpoint
	if seq == 0 {
		source = FromLog
	}
	checkPlan(t, again.plan, RepairPlan{Generation: gen + 1, Seq: seq, Source: source, Checkpoint: want})
	if !again.plan.Empty() || again.state != state || again.eventLines() != events {
		t.Errorf("a second plan: %+v\nstate:\n%s\nevents:\n%s", again.plan, again.state, again.eventLines())
	}
}

// tailImage is a store of 9 commits in segments of one or two records, with its checkpoint at the commit 3, damaged by
// damage, which is given the records of the log.
func tailImage(t *testing.T, damage func(t *testing.T, fsys *simfs.FS, recs []recLoc)) (*sStore, *simfs.FS) {
	t.Helper()
	s := newTailStore(t, salvageOptions, 3, 9)
	fsys := s.copy()
	if damage != nil {
		damage(t, fsys, locateRecords(t, fsys, 1))
	}
	return s, fsys
}

func damageCommit(seq int, d damage) func(t *testing.T, fsys *simfs.FS, recs []recLoc) {
	return func(t *testing.T, fsys *simfs.FS, recs []recLoc) { d.apply(t, fsys, commitAt(t, recs, seq)) }
}

// TestRepairKeeps: Apply keeps what Salvage shows, and nothing else: a store with a record before its checkpoint that
// cannot be read, one after it, an object that cannot be read, and no damage.
func TestRepairKeeps(t *testing.T) {
	withBaseBytes(t, 200)
	for _, c := range []struct {
		name     string
		image    func(t *testing.T) (*sStore, *simfs.FS)
		truncate bool
		seq      int
		spans    int
	}{
		{"no damage", func(t *testing.T) (*sStore, *simfs.FS) { return tailImage(t, nil) }, false, 9, 0},
		{"a record before the checkpoint", func(t *testing.T) (*sStore, *simfs.FS) {
			return tailImage(t, damageCommit(2, recordDamages[0]))
		}, false, 9, 1},
		{"a length before the checkpoint", func(t *testing.T) (*sStore, *simfs.FS) {
			return tailImage(t, damageCommit(1, recordDamages[1]))
		}, false, 9, 1},
		{"garbage before the checkpoint", func(t *testing.T) (*sStore, *simfs.FS) {
			return tailImage(t, damageCommit(3, recordDamages[2]))
		}, false, 9, 1},
		{"a record after the checkpoint", func(t *testing.T) (*sStore, *simfs.FS) {
			return tailImage(t, damageCommit(6, recordDamages[0]))
		}, true, 5, 1},
		{"the record after the checkpoint", func(t *testing.T) (*sStore, *simfs.FS) {
			return tailImage(t, damageCommit(4, recordDamages[2]))
		}, true, 3, 1},
		{"the last record", func(t *testing.T) (*sStore, *simfs.FS) {
			return tailImage(t, damageCommit(9, recordDamages[0]))
		}, false, 8, 0},
		{"a compacted store", func(t *testing.T) (*sStore, *simfs.FS) {
			s := newTailStoreAfter(t, salvageOptions, 4, 6, 9)
			fsys := s.copy()
			damageCommit(8, recordDamages[1])(t, fsys, locateRecords(t, fsys, 2))
			return s, fsys
		}, true, 7, 1},
		{"a compacted store, a record before the checkpoint", func(t *testing.T) (*sStore, *simfs.FS) {
			s := newTailStoreAfter(t, salvageOptions, 4, 6, 9)
			fsys := s.copy()
			damageCommit(5, recordDamages[0])(t, fsys, locateRecords(t, fsys, 2))
			return s, fsys
		}, false, 9, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, fsys := c.image(t)
			kept := salvage(t, fsys, s.o)
			p := kept.plan
			if len(p.Spans) != c.spans || p.Seq != uint64(c.seq) || (p.Truncation != nil) != c.truncate {
				t.Fatalf("plan %+v", p)
			}
			if c.truncate {
				if first := repair(t, copyFS(fsys), s.o, RepairOptions{}); !errors.Is(first.err, ErrTruncation) {
					t.Fatalf("Apply without Truncate: %v", first.err)
				}
			}
			files := fileMap(fsys)
			r := repair(t, fsys, s.o, RepairOptions{Truncate: c.truncate})
			if r.err != nil {
				t.Fatalf("Apply: %v", r.err)
			}
			if r.res.Generation != p.Generation+1 || r.res.Seq != p.Seq || r.res.Dropped != len(p.Objects) || r.res.Records < 2 ||
				r.res.Events != uint64(len(kept.events)) || r.res.Removed.Segments == 0 {
				t.Errorf("result %+v for plan %+v", r.res, p)
			}
			if stateWithout(s.states[c.seq], p.Objects) != stateWithout(kept.state, p.Objects) {
				t.Errorf("Salvage keeps\n%s\nnot the state at the sequence %d", kept.state, c.seq)
			}
			if !isSubsequence(kept.events, strings.Split(s.events[9], "\n")) {
				t.Errorf("events %q", kept.events)
			}
			checkRepaired(t, fsys, s.o, p.Generation, p.Seq, stateWithout(kept.state, p.Objects), kept.eventLines())
			if maps.Equal(files, fileMap(fsys)) {
				t.Errorf("no file changed")
			}
		})
	}
}

// TestRepairRows: the rows that Apply puts are in the new state in the order of their tables and keys, replacing the
// rows that have their keys, and the base records are as large as baseBytes lets them be.
func TestRepairRows(t *testing.T) {
	withBaseBytes(t, 150)
	s := newSStore(t, salvageOptions, 9, 0)
	puts := []RepairPut{
		{Table: 9, Key: []byte("z"), Value: []byte("new table, high")},
		{Table: 1, Key: []byte("k04"), Value: []byte("replaced")},
		{Table: 1, Key: []byte("a"), Value: []byte("before the keys")},
		{Table: 1, Key: []byte("k05x"), Value: []byte("between")},
		{Table: 1, Key: []byte("zz"), Value: []byte("after the keys")},
		{Table: 2, Key: []byte{3}, Value: []byte("replaced, was empty")},
		{Table: 1, Key: []byte("k04"), Value: []byte("replaced again")},
		{Table: 1, Key: nil, Value: []byte("the empty key")},
		{Table: 1, Key: []byte("empty"), Value: nil},
		{Table: 0x7fffffff, Key: bytesOf('k', MaxKey), Value: bytesOf('v', 300)},
		{Table: 1, Key: []byte("k07"), Value: []byte("last")},
		{Table: 1, Key: []byte("a"), Value: []byte("a again")},
	}
	for _, perm := range []int{0, 1} {
		t.Run(fmt.Sprintf("order %d", perm), func(t *testing.T) {
			in := slices.Clone(puts)
			if perm == 1 {
				slices.Reverse(in)
			}
			fsys := s.copy()
			kept := salvage(t, fsys, s.o)
			r := repair(t, fsys, s.o, RepairOptions{Puts: in})
			if r.err != nil {
				t.Fatalf("Apply: %v", r.err)
			}
			state := withPuts(stateWithout(kept.state, kept.plan.Objects), in) // the last put with a key stays
			checkRepaired(t, fsys, s.o, 1, 9, state, kept.eventLines())
			// The base has each key once, in order.
			var seen []string
			for _, name := range logNames(t, fsys) {
				b := fileData(t, fsys, name)
				h, offs := frames(t, fsys, name)
				for _, off := range offs {
					body, _, _ := parseFrame(b[off:], h.salt)
					rec, reason := parseBody(body)
					if reason != "" {
						t.Fatal(reason)
					}
					for _, p := range rec.ops {
						if p.code == opPut {
							seen = append(seen, fmt.Sprintf("%010d/%s", p.table, p.key))
						}
					}
				}
			}
			if !slices.IsSorted(seen) || len(slices.Compact(slices.Clone(seen))) != len(seen) {
				t.Errorf("the puts of the base are not in order, or repeat a key: %q", seen)
			}
		})
	}
	t.Run("a store without rows", func(t *testing.T) {
		fsys, db := newSim(t, Options{})
		closeDB(t, db)
		r := repair(t, fsys, Options{}, RepairOptions{Puts: puts[:3]})
		if r.err != nil {
			t.Fatal(r.err)
		}
		db = reopen(t, fsys, Options{})
		if got, want := dump(t, db), "1/a=before the keys\n1/k04=replaced\n9/z=new table, high"; got != want {
			t.Errorf("rows:\n%s", got)
		}
	})
}

func bytesOf(c byte, n int) []byte { return slices.Repeat([]byte{c}, n) }

// TestRepairEvents: the events that Apply appends are numbered from the next event number of the kept state, and
// have the sequence number of its last commit; the kept events are those that Rewrite.Event keeps, as it changes them.
func TestRepairEvents(t *testing.T) {
	withBaseBytes(t, 150)
	added := [][]byte{[]byte("first"), []byte("second"), nil, []byte("fourth")}
	t.Run("no damage", func(t *testing.T) {
		s := newSStore(t, salvageOptions, 9, 0)
		fsys := s.copy()
		kept := salvage(t, fsys, s.o)
		r := repair(t, fsys, s.o, RepairOptions{Events: added})
		if r.err != nil {
			t.Fatal(r.err)
		}
		want := append(slices.Clone(kept.events), "10/9=first", "11/9=second", "12/9=", "13/9=fourth")
		checkRepaired(t, fsys, s.o, 1, 9, stateWithout(kept.state, kept.plan.Objects), strings.Join(want, "\n"))
		if r.res.Events != 13 {
			t.Errorf("result %+v", r.res)
		}
	})
	t.Run("rewritten", func(t *testing.T) {
		s := newSStore(t, salvageOptions, 9, 0)
		fsys := s.copy()
		kept := salvage(t, fsys, s.o)
		rewrite := func(e Event) ([]byte, bool) {
			switch {
			case e.Number >= 8: // the last events, which the added ones must not be numbered after
				return nil, false
			case e.Number%2 == 0:
				return []byte(fmt.Sprintf("changed %d", e.Number)), true
			}
			return e.Data, true
		}
		r := repair(t, fsys, s.o, RepairOptions{Rewrite: Rewrite{Event: rewrite}, Events: added[:2]})
		if r.err != nil {
			t.Fatal(r.err)
		}
		want := append(rewriteEvents(kept.events, rewrite), "10/9=first", "11/9=second")
		checkRepaired(t, fsys, s.o, 1, 9, stateWithout(kept.state, kept.plan.Objects), strings.Join(want, "\n"))
	})
	t.Run("after a truncation, the numbers of the commits that it drops", func(t *testing.T) {
		s, fsys := tailImage(t, damageCommit(6, recordDamages[0]))
		kept := salvage(t, fsys, s.o)
		if kept.plan.Seq != 5 || kept.plan.Truncation == nil {
			t.Fatalf("plan %+v", kept.plan)
		}
		r := repair(t, fsys, s.o, RepairOptions{Truncate: true, Events: added[:2]})
		if r.err != nil {
			t.Fatal(r.err)
		}
		want := append(slices.Clone(kept.events), "6/5=first", "7/5=second")
		checkRepaired(t, fsys, s.o, 1, 5, stateWithout(kept.state, kept.plan.Objects), strings.Join(want, "\n"))
		db := reopen(t, copyFS(fsys), s.o)
		change(t, db, 100) // the writer goes on after them
		if got := eventLines(t, db); !strings.HasSuffix(got, "\n8/6=event 100") {
			t.Errorf("events:\n%s", got)
		}
	})
	t.Run("after a record of the checkpoint that is lost, the numbers of its events", func(t *testing.T) {
		s, fsys := tailImage(t, damageCommit(3, recordDamages[0]))
		kept := salvage(t, fsys, s.o)
		r := repair(t, fsys, s.o, RepairOptions{Events: added[:1]})
		if r.err != nil {
			t.Fatal(r.err)
		}
		want := append(slices.Clone(kept.events), "10/9=first")
		checkRepaired(t, fsys, s.o, 1, 9, stateWithout(kept.state, kept.plan.Objects), strings.Join(want, "\n"))
	})
	t.Run("a state without a commit", func(t *testing.T) {
		fsys, db := newSim(t, Options{})
		closeDB(t, db)
		before := fileMap(fsys)
		r := repair(t, fsys, Options{}, RepairOptions{Events: added[:1]})
		if r.err == nil || !strings.Contains(r.err.Error(), "no commit") {
			t.Errorf("Apply: %v", r.err)
		}
		checkFilesUnchanged(t, fsys, before)
	})
}

// TestRepairRefuses: what Apply refuses, it refuses before it changes a file, and it can be called again.
func TestRepairRefuses(t *testing.T) {
	s, fsys := tailImage(t, damageCommit(6, recordDamages[0]))
	before := fileMap(fsys)
	frozen := &frozenFS{FS: fsys}
	tr := salvage(t, fsys, s.o).plan.Truncation
	if tr == nil {
		t.Fatal("no truncation")
	}
	for _, c := range []struct {
		name string
		r    RepairOptions
		want string
	}{
		{"truncation", RepairOptions{}, fmt.Sprintf("the repair drops commits that can be read: the commits %d to %d come after the damage at %s",
			tr.FirstSeq, tr.LastSeq, tr.Damage)},
		{"a rewrite of the rows", RepairOptions{Truncate: true, Rewrite: Rewrite{Value: func(_ uint32, _, v []byte) ([]byte, bool) { return v, true }}},
			"keeps the rows and the objects"},
		{"a rewrite of the objects", RepairOptions{Truncate: true, Rewrite: Rewrite{Object: func([32]byte) bool { return true }}},
			"keeps the rows and the objects"},
		{"table 0", RepairOptions{Truncate: true, Puts: []RepairPut{{Table: 1, Key: []byte("a")}, {Table: 0, Key: []byte("b")}}},
			"put 1: invalid table 0"},
		{"a long key", RepairOptions{Truncate: true, Puts: []RepairPut{{Table: 1, Key: bytesOf('K', MaxKey+1)}}},
			fmt.Sprintf("put 0: key too long: %d bytes (max %d)", MaxKey+1, MaxKey)},
	} {
		t.Run(c.name, func(t *testing.T) {
			var err error
			salvageErr := Salvage(context.Background(), frozen, s.o, func(sv *Salvager) error {
				_, err = sv.Apply(context.Background(), c.r)
				if c.name == "truncation" && !errors.Is(err, ErrTruncation) {
					t.Errorf("Apply: %v", err)
				}
				return nil
			})
			if salvageErr != nil || err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Apply: %v (Salvage: %v), want %q", err, salvageErr, c.want)
			}
			if calls := frozen.denied(); len(calls) > 0 {
				t.Errorf("Apply tried to change the store: %v", calls)
			}
			checkFilesUnchanged(t, fsys, before)
		})
	}
	t.Run("a row that is too large, an event that is too large", func(t *testing.T) {
		defer func(n int) { maxBody = n }(maxBody)
		maxBody = 600
		for _, r := range []RepairOptions{
			{Truncate: true, Puts: []RepairPut{{Table: 1, Key: []byte("a"), Value: bytesOf('v', 600)}}},
			{Truncate: true, Events: [][]byte{bytesOf('e', 600)}},
		} {
			err := Salvage(context.Background(), frozen, s.o, func(sv *Salvager) error {
				if _, err := sv.Apply(context.Background(), r); err == nil || !strings.Contains(err.Error(), "is too large for a base record") {
					t.Errorf("Apply: %v", err)
				}
				return nil
			})
			if err != nil || len(frozen.denied()) > 0 {
				t.Errorf("Salvage: %v, calls %v", err, frozen.denied())
			}
		}
	})
	t.Run("the second call", func(t *testing.T) {
		fsys := copyFS(fsys)
		err := Salvage(context.Background(), fsys, s.o, func(sv *Salvager) error {
			ctx := context.Background()
			if _, err := sv.Apply(ctx, RepairOptions{}); !errors.Is(err, ErrTruncation) {
				t.Errorf("Apply: %v", err)
			}
			if _, err := sv.Apply(ctx, RepairOptions{Truncate: true}); err != nil { // the refusal wrote nothing
				t.Errorf("Apply after a refusal: %v", err)
			}
			if _, err := sv.Apply(ctx, RepairOptions{Truncate: true}); err == nil || !strings.Contains(err.Error(), "applied already") {
				t.Errorf("a second Apply: %v", err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a file system that refuses to write", func(t *testing.T) {
		fsys := copyFS(fsys)
		frozen := &frozenFS{FS: fsys}
		before := fileMap(fsys)
		var err error
		if serr := Salvage(context.Background(), frozen, s.o, func(sv *Salvager) error {
			_, err = sv.Apply(context.Background(), RepairOptions{Truncate: true})
			return nil
		}); serr != nil || err == nil || len(frozen.denied()) == 0 {
			t.Errorf("Apply: %v (Salvage: %v), %d calls", err, serr, len(frozen.denied()))
		}
		if n := frozen.openFiles(); n != 0 {
			t.Errorf("%d files are open", n)
		}
		checkFilesUnchanged(t, fsys, before)
	})
	t.Run("a canceled context", func(t *testing.T) {
		fsys := copyFS(fsys)
		before, counted := fileMap(fsys), fsys.Counted()
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		r := repairCtx(t, canceled, fsys, s.o, RepairOptions{Truncate: true})
		if !errors.Is(r.err, context.Canceled) || fsys.Counted() != counted {
			t.Errorf("Apply: %v, %d operations", r.err, fsys.Counted()-counted)
		}
		checkFilesUnchanged(t, fsys, before)
	})
}

// TestRepairStateWithoutCommits: a repair that drops every commit gives a store that has no commit, but the rows that
// it is given, and that goes on.
func TestRepairStateWithoutCommits(t *testing.T) {
	s := newSStore(t, salvageOptions, 6, 0)
	fsys := s.copy()
	dropCheckpoint(t, fsys)
	flipByte(t, fsys, seg1, commitAt(t, locateRecords(t, fsys, 1), 1).off+9)
	kept := salvage(t, fsys, s.o)
	if kept.plan.Seq != 0 || kept.plan.Truncation == nil || len(kept.events) != 0 {
		t.Fatalf("plan %+v", kept.plan)
	}
	r := repair(t, fsys, s.o, RepairOptions{Truncate: true, Puts: []RepairPut{{Table: 5, Key: []byte("only"), Value: []byte("row")}}})
	if r.err != nil {
		t.Fatal(r.err)
	}
	checkRepaired(t, fsys, s.o, 1, 0, withPuts(stateWithout(kept.state, kept.plan.Objects), []RepairPut{{Table: 5, Key: []byte("only"), Value: []byte("row")}}), "")
	db := reopen(t, fsys, s.o)
	putKey(t, db, 1, "next", "v")
	if st := db.cur.Load(); st.seq != 1 || st.gen != 2 {
		t.Errorf("generation %d, sequence %d", st.gen, st.seq)
	}
	closeDB(t, db)
	if got := outcome(t, copyFS(fsys), s.o); !strings.HasPrefix(got, "generation 2, sequence 1\n1/next=v\n5/only=row\n") {
		t.Errorf("after a commit: %s", got)
	}
}

// TestRepairObjects: an object whose data cannot be read is not in the new state, nor is its file left, and the other
// objects are.
func TestRepairObjects(t *testing.T) {
	v := newVerifyStore(t)
	for i, dmg := range objectDamages {
		if dmg.name == "a header of a newer version" {
			continue
		}
		t.Run(dmg.name, func(t *testing.T) {
			fsys := copyFS(v.fsys)
			h := v.files[i%len(v.files)]
			dmg.apply(t, fsys, h)
			kept := salvage(t, fsys, verifyOptions)
			if len(kept.plan.Objects) != 1 || kept.plan.Objects[0].Hash != h {
				t.Fatalf("plan %+v", kept.plan)
			}
			r := repair(t, fsys, verifyOptions, RepairOptions{})
			if r.err != nil || r.res.Dropped != 1 {
				t.Fatalf("Apply: %v, %+v", r.err, r.res)
			}
			want := stateWithout(kept.state, kept.plan.Objects)
			if strings.Contains(want, fmt.Sprintf("%x:", h)) || want == kept.state {
				t.Fatalf("the state keeps the object")
			}
			if dmg.name == "a directory" {
				// What is not a file of the engine stays: the directory is no file.
				removeFile(t, fsys, objName(h))
			}
			checkRepaired(t, fsys, verifyOptions, 1, 10, want, kept.eventLines())
		})
	}
	t.Run("an inline object", func(t *testing.T) {
		fsys := copyFS(v.fsys)
		h := v.afterFirst(t)
		damageInline(t, fsys, v, h)
		kept := salvage(t, fsys, verifyOptions)
		r := repair(t, fsys, verifyOptions, RepairOptions{})
		if r.err != nil || r.res.Dropped != 1 {
			t.Fatalf("Apply: %v, %+v", r.err, r.res)
		}
		checkRepaired(t, fsys, verifyOptions, 1, 10, stateWithout(kept.state, kept.plan.Objects), kept.eventLines())
	})
	t.Run("a record and every kind of damage of the objects", func(t *testing.T) {
		fsys := copyFS(v.fsys)
		damageAll(t, fsys, v)
		kept := salvage(t, fsys, verifyOptions)
		if len(kept.plan.Objects) != 5 || len(kept.plan.Spans) != 1 {
			t.Fatalf("plan %+v", kept.plan)
		}
		removeFile(t, fsys, objName(v.files[5%len(v.files)])) // the directory
		r := repair(t, fsys, verifyOptions, RepairOptions{})
		if r.err != nil || r.res.Dropped != 5 {
			t.Fatalf("Apply: %v, %+v", r.err, r.res)
		}
		checkRepaired(t, fsys, verifyOptions, 1, 10, stateWithout(kept.state, kept.plan.Objects), kept.eventLines())
	})
}

// leftoverImage is a store in the generation 2 whose segments of the generation 1, temporary files, and files of
// objects that it does not have are left, and whose second record is damaged. The files that are not the engine's are
// returned.
func leftoverImage(t *testing.T) (fsys *simfs.FS, own map[string]string) {
	t.Helper()
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
	closeDB(t, db)
	recs := locateRecords(t, fsys, 2)
	for name, data := range old {
		setFile(t, fsys, name, data)
	}
	setFile(t, fsys, ".tmp-checkpoint-0123456789abcdef", []byte("x"))
	setFile(t, fsys, ".tmp-checkpoint-fedcba9876543210", []byte("y"))
	unused := fmt.Sprintf("%x", sha256.Sum256([]byte("unused")))
	for _, name := range []string{"objects/ab/.tmp-0123456789abcdef", "objects/" + unused[:2] + "/" + unused} {
		if err := fsys.Mkdir(path.Dir(name), 0o700); err != nil && !exists(t, fsys, path.Dir(name)) {
			t.Fatal(err)
		}
		setFile(t, fsys, name, []byte("x"))
	}
	own = map[string]string{"notes.txt": "mine", "objects/ab/not-an-object": "mine", "objects/cd/" + unused[:62]: "mine"}
	for name, data := range own {
		if err := fsys.Mkdir(path.Dir(name), 0o700); err != nil && !exists(t, fsys, path.Dir(name)) && path.Dir(name) != "." {
			t.Fatal(err)
		}
		setFile(t, fsys, name, []byte(data))
	}
	recordDamages[0].apply(t, fsys, commitAt(t, recs, 6))
	return fsys, own
}

// TestRepairLeftovers: Apply removes the segments of the older generations, of an incomplete newer one, the temporary
// files, and the files of objects that the state does not have, and no other file.
func TestRepairLeftovers(t *testing.T) {
	withBaseBytes(t, 200)
	t.Run("an older generation, temporary files and unused objects", func(t *testing.T) {
		fsys, own := leftoverImage(t)
		kept := salvage(t, fsys, salvageOptions)
		if kept.plan.Generation != 2 || kept.plan.Seq != 9 || len(kept.plan.Spans) != 1 || len(kept.plan.Remove) < 5 {
			t.Fatalf("plan %+v", kept.plan)
		}
		oldSegments := 0
		fsys.Walk(func(p string, _ []byte) {
			if strings.HasPrefix(p, "log/0000000000000001-") {
				oldSegments++
			}
		})
		keptSegments := len(logNames(t, fsys)) - oldSegments
		r := repair(t, fsys, salvageOptions, RepairOptions{})
		if r.err != nil {
			t.Fatal(r.err)
		}
		if got, want := r.res.Removed, (GCResult{Segments: oldSegments + keptSegments, Objects: 1, Temps: 3}); got != want {
			t.Errorf("removed %+v, want %+v", got, want)
		}
		after := fileMap(fsys)
		for name, data := range own {
			if after[name] != data {
				t.Errorf("%s: %q", name, after[name])
			}
		}
		checkRepairedFiles(t, fsys, salvageOptions, 2, 9, stateWithout(kept.state, kept.plan.Objects), kept.eventLines(), own)
	})
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
		armed = true
		if err := compactDB(db, Rewrite{}); !errors.Is(err, simfs.ErrCrashed) {
			t.Fatalf("compact: %v", err)
		}
		crashed := fsys.Crash(loseAll)
		o.Hook = nil
		if gens := gensOf(t, crashed); !slices.Equal(gens, []uint64{1, 2}) {
			t.Fatalf("generations %v", gens)
		}
		recordDamages[1].apply(t, crashed, commitAt(t, locateRecords(t, crashed, 1), 2))
		kept := salvage(t, crashed, o)
		if len(kept.plan.Spans) != 1 || len(kept.plan.Recovery) != 1 {
			t.Fatalf("plan %+v", kept.plan)
		}
		r := repair(t, crashed, o, RepairOptions{})
		if r.err != nil || r.res.Generation != 2 {
			t.Fatalf("Apply: %v, %+v", r.err, r.res)
		}
		checkRepaired(t, crashed, o, 1, 8, stateWithout(kept.state, kept.plan.Objects), kept.eventLines())
	})
}

// checkRepairedFiles is checkRepaired for a store with files of its own, which it does not complain of.
func checkRepairedFiles(t *testing.T, fsys *simfs.FS, o Options, gen, seq uint64, state, events string, own map[string]string) {
	t.Helper()
	clean := copyFS(fsys)
	for name := range own {
		removeFile(t, clean, name)
	}
	checkRepaired(t, clean, o, gen, seq, state, events)
}

// TestRepairHead: head is written again, whether it is valid, damaged or missing; and a store without it is not open
// before the base is complete.
func TestRepairHead(t *testing.T) {
	for _, c := range []struct {
		name  string
		mess  func(t *testing.T, fsys *simfs.FS)
		error string
	}{
		{"valid", func(t *testing.T, fsys *simfs.FS) {}, ""},
		{"a bad slot", func(t *testing.T, fsys *simfs.FS) { flipByte(t, fsys, "head", 3) }, ""},
		{"short", func(t *testing.T, fsys *simfs.FS) { setFile(t, fsys, "head", []byte("short")) }, ""},
		{"empty", func(t *testing.T, fsys *simfs.FS) { setFile(t, fsys, "head", nil) }, ""},
		{"missing", func(t *testing.T, fsys *simfs.FS) { removeFile(t, fsys, "head") }, "head: missing file"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, fsys := tailImage(t, damageCommit(2, recordDamages[0]))
			c.mess(t, fsys)
			if got := outcome(t, copyFS(fsys), s.o); c.error != "" && !strings.HasSuffix(got, c.error) {
				t.Fatalf("Open: %s", got)
			}
			kept := salvage(t, fsys, s.o)
			wantRecovery := c.name != "valid"
			if (len(kept.plan.Recovery) > 0) != wantRecovery && c.name != "a bad slot" {
				t.Errorf("recovery %v", kept.plan.Recovery)
			}
			r := repair(t, fsys, s.o, RepairOptions{})
			if r.err != nil {
				t.Fatal(r.err)
			}
			checkRepaired(t, fsys, s.o, 1, 9, stateWithout(kept.state, kept.plan.Objects), kept.eventLines())
			if h := readHeadFile(t, fsys); h.gen != 2 || h.seq != 9 {
				t.Errorf("head: %+v", h)
			}
		})
	}
}

// TestRepairHooks: the points of a compaction are those of a repair, in the same order, and the hooks after its
// base-end can only block.
func TestRepairHooks(t *testing.T) {
	withBaseBytes(t, 200)
	hookErr := errors.New("hook failed")
	var points []string
	fail := ""
	plain, o := salvageOptions, salvageOptions
	o.Hook = func(p string) error {
		points = append(points, p)
		if p == fail {
			return hookErr
		}
		return nil
	}
	image, _ := leftoverImage(t)
	older := 0
	image.Walk(func(p string, _ []byte) {
		if strings.HasPrefix(p, "log/0000000000000001-") {
			older++
		}
	})
	for _, p := range []string{"", "compact:base", "compact:base-end", "checkpoint:renamed", "checkpoint:done", "compact:checkpoint",
		"compact:delete", "gc:remove"} {
		t.Run(p, func(t *testing.T) {
			fsys := copyFS(image)
			before := outcome(t, copyFS(fsys), plain)
			points, fail = nil, p
			r := repair(t, fsys, o, RepairOptions{})
			fail = ""
			switch p {
			case "compact:base":
				if !errors.Is(r.err, hookErr) {
					t.Errorf("Apply: %v", r.err)
				}
				if got := outcome(t, copyFS(fsys), plain); got != before {
					t.Errorf("the store is not as it was:\n%s", got)
				}
				return
			case "gc:remove":
				if !errors.Is(r.err, hookErr) || errors.Is(r.err, ErrFailed) {
					t.Errorf("Apply: %v", r.err)
				}
				if got := outcome(t, copyFS(fsys), plain); !strings.HasPrefix(got, "generation 3, sequence 9\n") {
					t.Errorf("the store is not repaired:\n%s", got)
				}
				return
			}
			if r.err != nil {
				t.Fatalf("Apply with the hook %q failing: %v", p, r.err)
			}
			want := []string{"compact:base", "compact:base-end", "checkpoint:synced", "checkpoint:renamed", "checkpoint:done",
				"compact:checkpoint"}
			for range r.res.Removed.Segments - older { // the older generation goes before the base is written
				want = append(want, "compact:delete")
			}
			for range r.res.Removed.Objects + r.res.Removed.Temps {
				want = append(want, "gc:remove")
			}
			if !slices.Equal(points, want) {
				t.Errorf("points %q, want %q", points, want)
			}
		})
	}
}

// logRecords returns the segments of the generation gen and their records, as text, without the CRCs and the salts.
func logRecords(t *testing.T, fsys vfs.FS, gen genNum) []string {
	t.Helper()
	var out []string
	for _, name := range logNames(t, fsys) {
		g, index, _ := parseSegName(path.Base(name))
		if g != gen {
			continue
		}
		b := fileData(t, fsys, name)
		h, offs := frames(t, fsys, name)
		out = append(out, fmt.Sprintf("segment %d, first sequence %d", index, h.firstSeq))
		for _, off := range offs {
			body, _, _ := parseFrame(b[off:], h.salt)
			rec, reason := parseBody(body)
			if reason != "" {
				t.Fatal(reason)
			}
			out = append(out, fmt.Sprintf("%+v", rec))
		}
	}
	return out
}

// TestRepairLikeCompact: for a store that is sound, Apply writes the records, and the checkpoint, that Compact writes.
func TestRepairLikeCompact(t *testing.T) {
	withBaseBytes(t, 200)
	for _, c := range []struct {
		name      string
		compactAt int
	}{{"generation 1", 0}, {"generation 2", 5}} {
		t.Run(c.name, func(t *testing.T) {
			s := newSStore(t, salvageOptions, 9, c.compactAt)
			gen := genNum(1 + min(c.compactAt, 1))
			compacted := s.copy()
			db := reopen(t, compacted, s.o)
			if err := compactDB(db, Rewrite{}); err != nil {
				t.Fatal(err)
			}
			closeDB(t, db)
			fsys := s.copy()
			if r := repair(t, fsys, s.o, RepairOptions{}); r.err != nil {
				t.Fatal(r.err)
			}
			want, got := logRecords(t, compacted, gen+1), logRecords(t, fsys, gen+1)
			if len(want) < 4 || !slices.Equal(got, want) {
				t.Errorf("records:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			if a, b := fileData(t, compacted, "checkpoint"), fileData(t, fsys, "checkpoint"); string(a[ckpHeaderSize:]) != string(b[ckpHeaderSize:]) {
				t.Errorf("the checkpoints differ")
			}
		})
	}
}

// TestRepairContext: a context that ends while Apply runs leaves the store as it was, or repaired.
func TestRepairContext(t *testing.T) {
	withBaseBytes(t, 200)
	s, image := tailImage(t, damageCommit(6, recordDamages[0]))
	before := outcome(t, copyFS(image), s.o)
	clean := copyFS(image)
	if r := repair(t, clean, s.o, RepairOptions{Truncate: true}); r.err != nil {
		t.Fatal(r.err)
	}
	after := outcome(t, copyFS(clean), s.o)
	ended := 0
	for left := 0; ; left++ {
		fsys := copyFS(image)
		r := repairCtx(t, &countdownContext{Context: context.Background(), left: left}, fsys, s.o, RepairOptions{Truncate: true})
		got := outcome(t, copyFS(fsys), s.o)
		if r.err == nil {
			if got != after {
				t.Fatalf("after %d checks: %s", left, got)
			}
			break
		}
		if !errors.Is(r.err, context.Canceled) {
			t.Fatalf("after %d checks: Apply: %v", left, r.err)
		}
		ended++
		if got != before && got != after {
			t.Fatalf("after %d checks, the store is neither as it was nor repaired:\n%s", left, got)
		}
		if left > 1000 {
			t.Fatal("Apply does not end")
		}
	}
	if ended < 10 {
		t.Errorf("the context ended %d times", ended)
	}
}

// repairImage is a store to repair, and what to repair it with.
type repairImage struct {
	name  string
	image func(t *testing.T) *simfs.FS
	o     Options
	r     RepairOptions
}

var repairRows = []RepairPut{
	{Table: 7, Key: []byte("hole"), Value: []byte("row")},
	{Table: 1, Key: []byte("k05"), Value: []byte("put over a row")},
}

var repairEvents = [][]byte{[]byte("hole event"), []byte("mark")}

// repairImages are damaged stores of the three kinds of state that a repair keeps: the log from a checkpoint, with
// older files left; the log alone; and objects that cannot be read, without head.
func repairImages(t *testing.T) []repairImage {
	t.Helper()
	v := newVerifyStore(t)
	return []repairImage{
		{"a record after the checkpoint", func(t *testing.T) *simfs.FS {
			_, fsys := tailImage(t, damageCommit(6, recordDamages[0]))
			return fsys
		}, salvageOptions, RepairOptions{Truncate: true, Puts: repairRows, Events: repairEvents}},
		{"a record before the checkpoint, older files left", func(t *testing.T) *simfs.FS {
			fsys, _ := leftoverImage(t)
			return fsys
		}, salvageOptions, RepairOptions{Puts: repairRows, Events: repairEvents}},
		{"no checkpoint", func(t *testing.T) *simfs.FS {
			_, fsys := tailImage(t, damageCommit(5, recordDamages[1]))
			dropCheckpoint(t, fsys)
			return fsys
		}, salvageOptions, RepairOptions{Truncate: true, Puts: repairRows, Events: repairEvents}},
		{"objects that cannot be read, no head", func(t *testing.T) *simfs.FS {
			fsys := copyFS(v.fsys)
			objectDamages[1].apply(t, fsys, v.files[0])
			objectDamages[0].apply(t, fsys, v.files[1])
			removeFile(t, fsys, "head")
			return fsys
		}, verifyOptions, RepairOptions{Puts: repairRows, Events: repairEvents}},
	}
}

// TestCrashDuringRepair crashes after each operation of Apply, and checks the crash models that simfs.Models gives:
// Open gives what it gave before, or what it gives of the repaired store, never another state.
func TestCrashDuringRepair(t *testing.T) {
	withBaseBytes(t, 200)
	for _, c := range repairImages(t) {
		t.Run(c.name, func(t *testing.T) {
			image := c.image(t)
			before := outcome(t, copyFS(image), c.o)
			clean := copyFS(image)
			start := clean.Counted()
			if r := repair(t, clean, c.o, c.r); r.err != nil {
				t.Fatal(r.err)
			}
			ops := clean.Counted() - start
			after := outcome(t, copyFS(clean), c.o)
			if before == after || strings.HasPrefix(after, "error") {
				t.Fatalf("before:\n%s\nafter:\n%s", before, after)
			}
			if got := outcome(t, clean.Crash(loseAll), c.o); got != after {
				t.Fatalf("what the repair leaves is not all durable:\n%s\nwant\n%s", got, after)
			}
			crashes := 0
			for k := range ops + 1 {
				fsys := copyFS(image)
				fsys.CrashAt(fsys.Counted() + k)
				_ = Salvage(context.Background(), fsys, c.o, func(s *Salvager) error {
					_, _ = s.Apply(context.Background(), c.r)
					return nil
				})
				for _, m := range simfs.Models(fsys.Pending(), 4, nil) {
					if got := outcome(t, fsys.Crash(m), c.o); got != before && got != after {
						t.Fatalf("a crash after %d of %d operations, %s:\n%s\nbefore:\n%s\nafter:\n%s", k, ops, m.Name, got, before, after)
					}
					crashes++
				}
			}
			t.Logf("%d crash points, %d crashes", ops+1, crashes)
		})
	}
}

// TestRepairFaults checks what an operation of Apply that fails leaves: before the base-end, the store as it was;
// after it, the repaired store; and that a second repair finishes the work.
func TestRepairFaults(t *testing.T) {
	withBaseBytes(t, 200)
	hookErr := errors.New("hook failed")
	gen := func(g int, kind simfs.OpKind) func(simfs.Op) bool {
		prefix := fmt.Sprintf("log/%016x-", g)
		return func(op simfs.Op) bool { return op.Kind == kind && strings.HasPrefix(op.Path, prefix) }
	}
	records := func(op simfs.Op) bool { return gen(2, simfs.OpWrite)(op) && op.Off > 0 }
	under := func(kind simfs.OpKind, prefix string) func(simfs.Op) bool {
		return func(op simfs.Op) bool { return op.Kind == kind && strings.HasPrefix(op.Path, prefix) }
	}
	images := map[string]repairImage{}
	for _, c := range repairImages(t) {
		images[c.name] = c
	}
	const (
		tail     = "a record after the checkpoint"
		leftover = "a record before the checkpoint, older files left"
		noHead   = "objects that cannot be read, no head"
	)
	for _, c := range []struct {
		name      string
		image     string
		fault     simfs.Fault
		after     string // the fault fires from this hook point on
		hook      string // that fails instead
		failed    bool   // the error wraps ErrFailed
		committed bool   // the new generation is complete
		opens     bool   // and Open gives the repaired store
		drop      bool   // the sync that fails may have made the base-end durable
	}{
		{name: "create", image: tail, fault: simfs.Fault{Match: gen(2, simfs.OpCreate), Err: syscall.ENOSPC}},
		{name: "header write", image: tail, fault: simfs.Fault{Match: gen(2, simfs.OpWrite), Err: syscall.EIO}},
		{name: "base write", image: tail, fault: simfs.Fault{Match: records, Err: syscall.ENOSPC, Short: 7}},
		{name: "rollover", image: tail, fault: simfs.Fault{Match: on(simfs.OpCreate, segName(2, 2)), Err: syscall.ENOSPC}},
		{name: "hook compact:base", image: tail, hook: "compact:base"},
		{name: "sync of a segment", image: tail, fault: simfs.Fault{Match: gen(2, simfs.OpSync), Err: syscall.EIO}, failed: true},
		{name: "sync of log", image: tail, fault: simfs.Fault{Match: on(simfs.OpSyncDir, "log"), Err: syscall.EIO}, failed: true},
		{name: "base-end write", image: tail, fault: simfs.Fault{Match: records, Err: syscall.EIO}, after: "compact:base", failed: true},
		{name: "base-end sync", image: tail, fault: simfs.Fault{Match: gen(2, simfs.OpSync), Err: syscall.EIO, Drop: true},
			after: "compact:base", failed: true, drop: true},
		{name: "checkpoint", image: tail, fault: simfs.Fault{Match: ckpTemp(simfs.OpSync), Err: syscall.EIO}, committed: true, opens: true},
		{name: "delete", image: tail, fault: simfs.Fault{Match: gen(1, simfs.OpRemove), Err: syscall.EIO}, committed: true, opens: true},
		{name: "sync of log after the deletes", image: tail, fault: simfs.Fault{Match: on(simfs.OpSyncDir, "log"), Err: syscall.EIO},
			after: "compact:delete", failed: true, committed: true, opens: true},
		{name: "remove of an older generation", image: leftover, fault: simfs.Fault{Match: gen(1, simfs.OpRemove), Err: syscall.EIO}},
		{name: "sync of log after an older generation", image: leftover, fault: simfs.Fault{Match: on(simfs.OpSyncDir, "log"), Err: syscall.EIO},
			failed: true},
		{name: "remove of an unused object", image: leftover, fault: simfs.Fault{Match: under(simfs.OpRemove, "objects/"), Err: syscall.EIO},
			committed: true, opens: true},
		{name: "sync of an objects directory", image: leftover, fault: simfs.Fault{Match: under(simfs.OpSyncDir, "objects/"), Err: syscall.EIO},
			failed: true, committed: true, opens: true},
		{name: "remove of a checkpoint temporary file", image: leftover,
			fault: simfs.Fault{Match: under(simfs.OpRemove, ".tmp-checkpoint-"), Err: syscall.EIO}, committed: true, opens: true},
		{name: "hook gc:remove", image: leftover, hook: "gc:remove", committed: true, opens: true},
		{name: "head", image: noHead, fault: simfs.Fault{Match: on(simfs.OpCreate, "head"), Err: syscall.ENOSPC}, committed: true},
		{name: "sync of the directory after head", image: noHead, fault: simfs.Fault{Match: on(simfs.OpSyncDir, "."), Err: syscall.EIO},
			after: "compact:base-end", failed: true, committed: true, opens: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			img := images[c.image]
			image := img.image(t)
			before := outcome(t, copyFS(image), img.o)
			clean := copyFS(image)
			if r := repair(t, clean, img.o, img.r); r.err != nil {
				t.Fatal(r.err)
			}
			after := outcome(t, copyFS(clean), img.o)
			armed, reached := false, false
			o := img.o
			o.Hook = func(p string) error {
				if armed && p == c.after {
					reached = true
				}
				if armed && p == c.hook {
					return hookErr
				}
				return nil
			}
			fsys := copyFS(image)
			cause := hookErr
			if c.fault.Match != nil {
				f := c.fault
				f.Times, cause = 1, f.Err
				if c.after != "" {
					match := f.Match
					f.Match = func(op simfs.Op) bool { return reached && match(op) }
				}
				fsys.Inject(f)
			}
			armed = true
			r := repair(t, fsys, o, img.r)
			armed = false
			if !errors.Is(r.err, cause) || errors.Is(r.err, ErrFailed) != c.failed {
				t.Fatalf("Apply: %v, want %v (failed: %v)", r.err, cause, c.failed)
			}
			got := outcome(t, copyFS(fsys), img.o)
			if c.opens && got != after {
				t.Errorf("the store is not repaired:\n%s\nwant\n%s", got, after)
			} else if !c.opens && got != before {
				t.Errorf("the store is not as it was:\n%s\nwant\n%s", got, before)
			}
			for _, m := range []simfs.Model{keepAll(fsys), loseAll} {
				switch got := outcome(t, fsys.Crash(m), img.o); {
				case got != before && got != after:
					t.Errorf("after the crash %s:\n%s", m.Name, got)
				case !c.committed && !c.drop && got != before:
					t.Errorf("after the crash %s, the store is not as it was:\n%s", m.Name, got)
				case c.committed && !c.drop && m.Name == "lose-all" && c.image != noHead && got != after:
					t.Errorf("after the crash %s, the store is not repaired:\n%s", m.Name, got)
				}
			}
			// A second repair finishes the work: with the rows and the events if the first did not write them.
			second := img.r
			if c.committed {
				second = RepairOptions{Truncate: true}
			}
			again := copyFS(fsys)
			if r := repair(t, again, img.o, second); r.err != nil {
				t.Fatalf("the second Apply: %v", r.err)
			}
			final := outcome(t, copyFS(again), img.o)
			if !c.committed && final != after {
				t.Errorf("after the second repair:\n%s\nwant\n%s", final, after)
			}
			_, finalState, _ := strings.Cut(final, "\n")
			if _, afterState, _ := strings.Cut(after, "\n"); finalState != afterState {
				t.Errorf("after the second repair:\n%s\nwant\n%s", final, after)
			}
			if p := salvage(t, again, img.o).plan; !p.Empty() || len(p.Recovery) != 0 || len(p.Remove) != 0 {
				t.Errorf("after the second repair, the plan is %+v", p)
			}
		})
	}
}

// FuzzRepair damages one file of a store, as FuzzOpen does, and repairs it, dropping what can be read after the damage
// if need be. Salvage refuses the store, and nothing changes, or Apply leaves a store that Open opens, whose Verify
// passes, whose tables are those after one of its commits, whose objects are those of that commit but for the objects
// that cannot be read, whose events are those that Salvage showed, and that Salvage finds nothing wrong with.
func FuzzRepair(f *testing.F) {
	for i := range uint8(32) {
		f.Add(i, i, uint8(0), uint32(70), []byte{0xff})
		f.Add(i, i, uint8(1), uint32(100), []byte{})
		f.Add(i, i, uint8(2), uint32(0), []byte("whrm-log\x02\x00\x00\x00"))
		f.Add(i, i, uint8(3), uint32(0), make([]byte, 40))
		f.Add(i, i, uint8(4), uint32(0), []byte{})
	}
	f.Add(uint8(3), uint8(7), uint8(1), uint32(10), []byte{})
	f.Fuzz(func(t *testing.T, image, file, mode uint8, off uint32, data []byte) {
		if fuzzStores[image&3] == nil {
			fuzzStores[image&3] = newFuzzStore(t, image&1 != 0, image&2 != 0)
		}
		base := fuzzStores[image&3]
		fsys := copyFS(base.fsys)
		name := base.names[int(file)%len(base.names)]
		b := fileData(t, fsys, name)
		at := int(off % uint32(len(b)+1))
		removed := ""
		switch mode % 5 {
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
		before := fileMap(fsys)
		kept, err := trySalvage(t, copyFS(fsys), fuzzOptions)
		var res *repaired
		applied := false
		serr := Salvage(context.Background(), fsys, fuzzOptions, func(s *Salvager) error {
			applied = true
			res = &repaired{plan: s.Plan()}
			res.res, res.err = s.Apply(context.Background(), RepairOptions{Truncate: true})
			return nil
		})
		if err != nil || serr != nil {
			if (err == nil) != (serr == nil) || applied || !isRefusal(serr, removed) {
				t.Fatalf("Salvage refuses with %v, Salvage with Apply with %v (Apply called: %v)", err, serr, applied)
			}
			checkFilesUnchanged(t, fsys, before)
			return
		}
		if res.err != nil {
			t.Fatalf("Apply: %v", res.err)
		}
		p := res.plan
		if p.Seq >= uint64(len(base.states)) {
			t.Fatalf("plan %+v", p)
		}
		want := stateWithout(base.states[p.Seq], p.Objects)
		db, err := Open(copyFS(fsys), fuzzOptions)
		if err != nil {
			t.Fatalf("Open after the repair: %v", err)
		}
		defer func() { _ = db.Close() }()
		if info := db.Recovery(); info.Seq != p.Seq || info.Generation != p.Generation+1 {
			t.Fatalf("recovery %+v, plan %+v", info, p)
		}
		if got := inMemory(t, db); got != want {
			t.Fatalf("after %s was damaged, the state is\n%s\nwant\n%s\nplan %+v", name, got, want, p)
		}
		if err := db.Verify(context.Background()); err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if got := eventLines(t, db); got != kept.eventLines() {
			t.Fatalf("events\n%s\nSalvage showed\n%s", got, kept.eventLines())
		}
		again, err := trySalvage(t, fsys, fuzzOptions)
		if err != nil {
			t.Fatalf("Salvage after the repair: %v", err)
		}
		if a := again.plan; !a.Empty() || len(a.Recovery) != 0 || len(a.Remove) != 0 || a.Seq != p.Seq || a.Generation != p.Generation+1 ||
			again.state != want || again.eventLines() != kept.eventLines() {
			t.Fatalf("the plan after the repair: %+v", a)
		}
	})
}

// TestRepairOS repairs a store in a real directory whose head is missing.
func TestRepairOS(t *testing.T) {
	withBaseBytes(t, 200)
	dir := t.TempDir()
	o := salvageOptions
	db, err := Create(osFS(t, dir), o)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 9 {
		change(t, db, i)
	}
	closeDB(t, db)
	fsys := osFS(t, dir)
	recordDamages[0].apply(t, fsys, commitAt(t, locateRecords(t, fsys, 1), 3))
	removeFile(t, fsys, "head")
	kept := salvage(t, fsys, o)
	if len(kept.plan.Spans) != 1 || len(kept.plan.Recovery) != 1 || kept.plan.Seq != 9 {
		t.Fatalf("plan %+v", kept.plan)
	}
	r := repair(t, fsys, o, RepairOptions{Puts: repairRows, Events: repairEvents})
	if r.err != nil {
		t.Fatal(r.err)
	}
	again := osFS(t, dir)
	db = reopen(t, again, o)
	if err := db.Verify(context.Background()); err != nil {
		t.Errorf("Verify: %v", err)
	}
	if got, want := inMemory(t, db), withPuts(stateWithout(kept.state, kept.plan.Objects), repairRows); got != want {
		t.Errorf("state:\n%s\nwant\n%s", got, want)
	}
	want := append(slices.Clone(kept.events), "10/9=hole event", "11/9=mark")
	if got := eventLines(t, db); got != strings.Join(want, "\n") {
		t.Errorf("events:\n%s\nwant\n%s", got, strings.Join(want, "\n"))
	}
	if h := readHeadFile(t, again); h.gen != 2 || h.seq != 9 {
		t.Errorf("head %+v", h)
	}
	closeDB(t, db)
	if p := salvage(t, again, o).plan; !p.Empty() || len(p.Recovery) != 0 || len(p.Remove) != 0 || p.Generation != 2 {
		t.Errorf("plan %+v", p)
	}
	for _, name := range logNames(t, again) {
		if g, _, _ := parseSegName(path.Base(name)); g != 2 {
			t.Errorf("%s is left", name)
		}
	}
}
