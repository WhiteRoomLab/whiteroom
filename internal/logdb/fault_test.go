package logdb

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// namedFault is a fault of TestFaultMatrix: one, or two that a fault and the undo that it causes meet.
type namedFault struct {
	name   string
	faults []simfs.Fault
}

// faultsFor returns the faults that TestFaultMatrix injects at op, an operation of the run c, each alone: an I/O
// error at every kind of operation; no space at those that can need it, and a write that has no space after half of
// its bytes; a sync that fails and drops what it would have made durable, which then never is; and at the write of
// the record of a commit, an I/O error with a second one at the Truncate or the Sync of the undo that it causes.
func faultsFor(c *cleanRun, op simfs.Op) []namedFault {
	at := func(o simfs.Op) bool { return o.N == op.N }
	one := func(name string, f simfs.Fault) namedFault {
		f.Match = at
		return namedFault{name, []simfs.Fault{f}}
	}
	faults := []namedFault{one("eio", simfs.Fault{Err: syscall.EIO})}
	switch op.Kind {
	case simfs.OpWrite:
		faults = append(faults, one("enospc", simfs.Fault{Err: syscall.ENOSPC}))
		if op.Len >= 2 {
			faults = append(faults, one("short", simfs.Fault{Err: syscall.ENOSPC, Short: op.Len / 2}))
		}
		if c.recordWrite(op.N) {
			for _, kind := range []simfs.OpKind{simfs.OpTruncate, simfs.OpSync} {
				undo := func(o simfs.Op) bool { return o.N > op.N && o.Kind == kind && o.Path == op.Path }
				faults = append(faults, namedFault{"undo-" + kind.String(), []simfs.Fault{{Match: at, Err: syscall.EIO},
					{Match: undo, Err: syscall.EIO, Times: 1}}})
			}
		}
	case simfs.OpCreate, simfs.OpMkdir, simfs.OpRename, simfs.OpTruncate:
		faults = append(faults, one("enospc", simfs.Fault{Err: syscall.ENOSPC}))
	case simfs.OpSync, simfs.OpSyncDir:
		faults = append(faults, one("drop", simfs.Fault{Err: syscall.EIO, Drop: true}))
	case simfs.OpRemove:
	case simfs.OpOpen, simfs.OpRead, simfs.OpLstat, simfs.OpReadDir, simfs.OpLock: // not counted
	}
	return faults
}

// checkpointOp reports whether op is one of a checkpoint: of its temporary file, or the sync of the directory of the
// store after its rename. Its failure fails only the checkpoint.
func checkpointOp(op simfs.Op) bool {
	return strings.HasPrefix(op.Path, ".tmp-checkpoint-") || op.Kind == simfs.OpSyncDir && op.Path == "."
}

// stopsStore reports whether the fault f at op, an operation of the run c, must stop the store, so that every Update
// returns ErrFailed: the failure of a sync of the log or of the objects, or of the undo of a commit (spec §6), or of
// the WriteAt of the base-end of a compaction (§9). Not that of a checkpoint (§8), or of the sync of head, which only
// Close makes.
func stopsStore(c *cleanRun, op simfs.Op, f namedFault) bool {
	if end, ok := c.hooks["compact:base-end"]; ok && op.N == end-1 || len(f.faults) > 1 {
		return true // the WriteAt of the base-end, whose Sync is end; or a failed undo
	}
	switch op.Kind {
	case simfs.OpSync, simfs.OpSyncDir:
		return !checkpointOp(op) && op.Path != "head"
	case simfs.OpCreate, simfs.OpWrite, simfs.OpTruncate, simfs.OpMkdir, simfs.OpRemove, simfs.OpRename:
	case simfs.OpOpen, simfs.OpRead, simfs.OpLstat, simfs.OpReadDir, simfs.OpLock:
	}
	return false
}

// TestFaultMatrix runs each scenario of matrixScenarios in each placement with one fault (faultsFor) at one of its
// counted operations, for each operation and fault. The step that the fault hits returns an error or nil, and Views
// see it whole or not at all; then an Update fails with ErrFailed if the fault must stop the store (stopsStore), and
// succeeds if not, and so do the steps after it; a stopped store fails them all, and its Close writes nothing. After
// each step that has succeeded after the fault (with -race, only the crash that loses everything that is not
// durable), and in the end, every crash of crashModels (4 subsets; long: 64) of what is not durable then opens to the
// state of the steps that have succeeded, with the step of the fault whole or not at all if it has stopped the store. A failed Open is opened again, after a restart if a sync has failed. A case
// is TestFaultMatrix/<placement>/<scenario>/op=<n>-<kind>/<fault>.
func TestFaultMatrix(t *testing.T) {
	withBaseBytes(t, 400)
	var mu sync.Mutex
	cases, stopped, crashes := 0, 0, 0
	t.Cleanup(func() {
		t.Logf("%d faults, %d of them stopped the store; %d crashes after them", cases, stopped, crashes)
	})
	for _, pl := range matrixPlacements {
		t.Run(pl.name, func(t *testing.T) {
			t.Parallel()
			o := matrixOptions(pl.p)
			for _, sc := range matrixScenarios() {
				t.Run(sc.name, func(t *testing.T) {
					t.Parallel()
					base, m := sc.setup(t, o)
					states := sc.states(m)
					for _, m := range states {
						_ = m.String() // before the cases run in parallel
					}
					c := runClean(t, &sc, base, o, states[len(states)-1])
					n, scStopped := 0, 0
					for _, op := range c.ops {
						for _, f := range faultsFor(c, op) {
							n++
							// A failed Open is opened again, which goes on.
							stops := stopsStore(c, op, f) && sc.steps[c.stepOf(op.N)].kind != stepOpen
							if stops {
								scStopped++
							}
							t.Run(fmt.Sprintf("op=%d-%v/%s", op.N, op.Kind, f.name), func(t *testing.T) {
								t.Parallel()
								n := runFault(t, &sc, c, base, o, states, op, f, stops)
								mu.Lock()
								crashes += n
								mu.Unlock()
							})
						}
					}
					mu.Lock()
					cases += n
					stopped += scStopped
					mu.Unlock()
					t.Logf("%d operations, %d faults, %d of them stopped the store", len(c.ops), n, scStopped)
				})
			}
		})
	}
}

// runFault runs the case of TestFaultMatrix of the fault f at op, an operation of c, the run of sc without a fault from
// base with o, whose states it has. It returns the number of crashes that it has checked.
func runFault(t *testing.T, sc *scenario, c *cleanRun, base *simfs.FS, o Options, states []*model, op simfs.Op,
	f namedFault, stops bool) int {
	ctx := context.Background()
	at := c.stepOf(op.N)
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf("%s\nthe fault %s at %v, in the step %s\nto run it alone: %s", fmt.Sprintf(format, args...), f.name,
			op, sc.steps[at].name, reproduce(t))
	}
	subsets := matrixSize(4, 4, 64)
	fsys := copyFS(base)
	for _, flt := range f.faults {
		fsys.Inject(flt)
	}
	// crashes checks that every crash that fsys can have now (those of fewModels, unless all) opens to one of want.
	checked := 0
	crashes := func(when string, all bool, want ...*model) {
		t.Helper()
		pending := fsys.Pending()
		models := crashModels(pending, subsets)
		if !all {
			models = fewModels(pending, subsets)
		}
		for _, m := range models {
			checked++
			db, err := Open(fsys.Crash(m), o)
			if err != nil {
				fail("Open after the crash %s %s:\n%s\n%v", m.Name, when, keptText(pending, m), err)
			}
			_, err = checkInvariants(db, want...)
			info := db.Recovery()
			_ = db.Close()
			if err != nil {
				fail("after the crash %s %s:\n%s\n%v\nrecovery: %+v", m.Name, when, keptText(pending, m), err, info)
			}
		}
	}
	var db *DB
	defer func() {
		if db != nil {
			_ = db.Close()
		}
	}()
	run := func(s *step) error {
		if s.kind == stepOpen {
			var err error
			db, err = Open(fsys, o)
			return err
		}
		return s.do(db)
	}
	for i := range at {
		if err := run(&sc.steps[i]); err != nil {
			fail("the step %s before the fault: %v", sc.steps[i].name, err)
		}
	}
	cur, s := states[at], &sc.steps[at]
	var alt *model // a stopped store may have the step of the fault, which Views do not see
	err := run(s)
	switch {
	case s.kind == stepOpen && err != nil:
		if op.Kind == simfs.OpSync || op.Kind == simfs.OpSyncDir {
			// After a failed sync, the machine is restarted before the store is opened again: the page cache may have
			// what will never be durable (TestReopenAfterFailedSync).
			fsys = fsys.Crash(keepAll(fsys))
		}
		if db, err = Open(fsys, o); err != nil {
			fail("Open after a failed Open: %v", err)
		}
	case s.kind == stepClose:
		db = nil // closed, whatever Close has returned
	default:
		got, verr := checkState(db, cur, cur.after(s))
		if verr != nil {
			fail("the step returned %v; after it: %v", err, verr)
		}
		if got != cur {
			cur = got
		} else {
			alt = cur.after(s)
		}
		perr := db.Update(ctx, probe.run)
		switch {
		case stops && !errors.Is(perr, ErrFailed):
			fail("the step returned %v; an Update after it returned %v, want ErrFailed", err, perr)
		case !stops && perr != nil:
			fail("the step returned %v; an Update after it returned %v, want nil", err, perr)
		case !stops:
			cur = cur.update(probe)
			crashes("after the Update after the fault", false, cur)
		}
	}
	for i := at + 1; i < len(sc.steps) && db != nil; i++ {
		s := &sc.steps[i]
		if !stops {
			if err := run(s); err != nil {
				fail("the step %s after the fault: %v", s.name, err)
			}
			cur = cur.after(s)
			crashes("after the step "+s.name, false, cur)
			continue
		}
		before := fsys.Counted()
		err := run(s)
		switch {
		case s.kind != stepClose && !errors.Is(err, ErrFailed):
			fail("the step %s of the stopped store returned %v, want ErrFailed", s.name, err)
		case s.kind == stepClose && (err != nil || fsys.Counted() != before):
			fail("Close of the stopped store returned %v after %d operations, want nil after none", err,
				fsys.Counted()-before)
		}
	}
	db = nil
	switch {
	case stops && alt != nil:
		crashes("in the end", true, cur, alt)
	case stops || s.kind == stepClose:
		crashes("in the end", true, cur)
	}
	return checked
}

// TestReopenAfterFailedSync shows why a store whose sync has failed may be opened again only after a restart of the
// machine, the residual risk of a failed fsync: the page cache can then hold a change that the sync has dropped and
// that never becomes durable, and an Open in the same boot takes it as it is and goes on after it. In each case, the
// steps after such an Open, and a crash, leave a log damaged before its last record: Verify fails, and so does Open
// without the checkpoint. After a restart, which a crash models, the same steps leave a store that is whole.
func TestReopenAfterFailedSync(t *testing.T) {
	ctx := context.Background()
	big := value("big ", 3000) // a record that goes in a new segment
	for _, c := range []struct {
		name string
		// fail makes a store, and in it a sync that fails and drops its changes: that of an Update, or that of the
		// cut of a torn tail by Open.
		fail    func(t *testing.T) *simfs.FS
		want    string // after the steps, with a restart
		damaged string // without a restart: the segment that Verify finds damaged
	}{
		{"the record of an Update", func(t *testing.T) *simfs.FS {
			fsys, db := newSim(t, Options{})
			putKey(t, db, 1, "a", "1")
			fsys.Inject(simfs.Fault{Match: on(simfs.OpSync, seg1), Err: syscall.EIO, Drop: true, Times: 1})
			if err := db.Update(ctx, putF(1, "b", "2")); !errors.Is(err, ErrFailed) {
				t.Fatalf("Update: %v", err)
			}
			closeDB(t, db)
			return fsys
		}, "1/a=1\n1/c=" + big, seg2}, // its header names the sequence after that of the dropped record
		{"the cut of a torn tail", func(t *testing.T) *simfs.FS {
			fsys, _ := build(t, Options{}, 1)
			h, _ := frames(t, fsys, seg1)
			torn := commitFrame(h.salt, 2, 1, "torn", value("torn ", 300))
			setFile(t, fsys, seg1, slices.Concat(fileData(t, fsys, seg1), torn[:len(torn)/2]))
			fsys.Inject(simfs.Fault{Match: on(simfs.OpSync, seg1), Err: syscall.EIO, Drop: true, Times: 1})
			if db, err := Open(fsys, Options{}); err == nil {
				_ = db.Close()
				t.Fatal("Open has cut the tail although its sync has failed")
			}
			return fsys
		}, "1/c=" + big + "\n1/k1=value 1.", seg1}, // the torn tail is back, in a segment that is not the last
	} {
		t.Run(c.name, func(t *testing.T) {
			steps := func(fsys *simfs.FS) *simfs.FS {
				db := reopen(t, fsys, Options{SegmentBytes: 2048})
				putKey(t, db, 1, "c", big)
				closeDB(t, db)
				return fsys.Crash(loseAll)
			}
			restarted := steps(c.fail(t).Crash(loseAll))
			db := reopen(t, restarted, Options{})
			if err := db.Verify(ctx); err != nil {
				t.Errorf("after a restart: Verify: %v", err)
			}
			checkDump(t, db, c.want)
			closeDB(t, db)

			fsys := steps(c.fail(t))
			db = reopen(t, fsys, Options{}) // from the checkpoint, after the damage
			var ce *CorruptError
			if err := db.Verify(ctx); !errors.As(err, &ce) || ce.Path != simPath(c.damaged) {
				t.Errorf("without a restart: Verify: %v, want damage in %s", err, c.damaged)
			}
			closeDB(t, db)
			dropCheckpoint(t, fsys)
			if _, err := Open(fsys, Options{}); !errors.As(err, &ce) || ce.Path != simPath(c.damaged) {
				t.Errorf("without a restart and the checkpoint: Open: %v, want damage in %s", err, c.damaged)
			}
		})
	}
}

// readFaultBases are the stores of TestReadFaultsDuringOpen, in which Open has something to do that a read error must
// keep it from doing: setup makes the durable part, and cache, if not nil, writes what a killed writer has left in the
// page cache of a copy.
var readFaultBases = []struct {
	name  string
	setup func(t *testing.T, o Options) *simfs.FS
	cache func(t *testing.T, fsys *simfs.FS)
}{
	{"cache", func(t *testing.T, o Options) *simfs.FS {
		fsys, _ := newBase(t, o, &update{puts: []keyValue{setKey(1, "a", value("a ", 100))},
			objects: [][]byte{objData(40, 150)}, events: []string{"base"}})
		return fsys
	}, func(t *testing.T, fsys *simfs.FS) {
		// A record of the writer that was killed, and a torn one after it.
		name := logNames(t, fsys)[len(logNames(t, fsys))-1]
		h, _ := frames(t, fsys, name)
		torn := commitFrame(h.salt, 3, 1, "torn", value("torn ", 300))
		appendRaw(t, fsys, name, slices.Concat(commitFrame(h.salt, 2, 1, "cached", "x"), torn[:len(torn)/2]))
	}},
	{"torn-segment", func(t *testing.T, o Options) *simfs.FS {
		fsys, _ := tornSegmentScenario().setup(t, o)
		return fsys
	}, nil},
	{"generations", func(t *testing.T, o Options) *simfs.FS {
		fsys, _ := compactionScenario().setup(t, o)
		return fsys
	}, nil},
}

// TestReadFaultsDuringOpen makes each operation of Open that only reads fail with EIO, once, in stores where Open
// has a record to sync, a tail to cut, a segment to remove or a generation to ignore: Open fails, and no file has
// changed, in what reads see or in what is durable. The same for OpenReader, with and without a writer that runs. A
// case is TestReadFaultsDuringOpen/<store>/<opener>/op=<n>-<kind>.
func TestReadFaultsDuringOpen(t *testing.T) {
	withBaseBytes(t, 400)
	o := matrixOptions(Hybrid)
	openers := []struct {
		name   string
		writer bool // a writer runs: the test holds lock/writer
		open   func(fsys *simfs.FS) (*DB, error)
	}{
		{"open", false, func(fsys *simfs.FS) (*DB, error) { return Open(fsys, o) }},
		{"reader", false, func(fsys *simfs.FS) (*DB, error) { return OpenReader(fsys, o) }},
		{"reader-with-a-writer", true, func(fsys *simfs.FS) (*DB, error) { return OpenReader(fsys, o) }},
	}
	total := 0
	for _, b := range readFaultBases {
		t.Run(b.name, func(t *testing.T) {
			base := b.setup(t, o)
			for _, op := range openers {
				t.Run(op.name, func(t *testing.T) {
					prepare := func() *simfs.FS {
						fsys := copyFS(base)
						if b.cache != nil {
							b.cache(t, fsys)
						}
						if op.writer {
							lockFile(t, fsys, "lock/writer", vfs.Exclusive)
						}
						return fsys
					}
					fsys := prepare()
					record := recordOps(fsys)
					db, err := op.open(fsys)
					if err != nil {
						t.Fatalf("without a fault: %v", err)
					}
					ops := record()
					closeDB(t, db)
					var reads []simfs.Op
					for _, r := range ops {
						if r.N == 0 {
							reads = append(reads, r)
						}
					}
					total += len(reads)
					for _, r := range reads {
						t.Run(fmt.Sprintf("op=%d-%v", r.All, r.Kind), func(t *testing.T) {
							fsys := prepare()
							files, pending := fileMap(fsys), fsys.Pending()
							fsys.Inject(simfs.Fault{Match: func(o simfs.Op) bool { return o.All == r.All }, Err: syscall.EIO})
							if db, err := op.open(fsys); err == nil {
								_ = db.Close()
								t.Fatalf("%v failed with EIO, and %s succeeded\nto run it alone: %s", r, op.name, reproduce(t))
							}
							if got := fileMap(fsys); !maps.Equal(got, files) {
								t.Errorf("%v failed: the files changed\nto run it alone: %s", r, reproduce(t))
							}
							if got := fsys.Pending(); !slices.Equal(got, pending) {
								t.Errorf("%v failed: what is durable changed: %v, then %v\nto run it alone: %s", r, pending, got,
									reproduce(t))
							}
						})
					}
				})
			}
		})
	}
	t.Logf("%d reads failed", total)
}
