package store

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

// storeFault is a fault of TestStoreFaultMatrix.
type storeFault struct {
	name  string
	fault simfs.Fault
}

// storeFaults returns the faults that TestStoreFaultMatrix injects at op, each alone: an I/O error at every kind of
// operation; no space at those that can need it, and a write that has no space after half of its bytes; and a sync that
// fails and drops what it would have made durable, which then never is. An I/O error of a sync keeps what it would
// have made durable pending.
func storeFaults(op simfs.Op) []storeFault {
	at := func(o simfs.Op) bool { return o.N == op.N }
	faults := []storeFault{{"eio", simfs.Fault{Match: at, Err: syscall.EIO}}}
	switch op.Kind {
	case simfs.OpWrite:
		faults = append(faults, storeFault{"enospc", simfs.Fault{Match: at, Err: syscall.ENOSPC}})
		if op.Len >= 2 {
			faults = append(faults, storeFault{"short", simfs.Fault{Match: at, Err: syscall.ENOSPC, Short: op.Len / 2}})
		}
	case simfs.OpCreate, simfs.OpMkdir, simfs.OpRename, simfs.OpTruncate:
		faults = append(faults, storeFault{"enospc", simfs.Fault{Match: at, Err: syscall.ENOSPC}})
	case simfs.OpSync, simfs.OpSyncDir:
		faults = append(faults, storeFault{"drop", simfs.Fault{Match: at, Err: syscall.EIO, Drop: true}})
	case simfs.OpRemove:
	case simfs.OpOpen, simfs.OpRead, simfs.OpLstat, simfs.OpReadDir, simfs.OpLock: // not counted
	}
	return faults
}

// stopsStore reports whether a fault at op, an operation of an Update, must stop the store, so that every Update
// returns ErrFailed: the failure of a sync of the log or of the objects of the engine (spec §6), not that of a
// checkpoint (§8) or of head.
func stopsStore(op simfs.Op) bool {
	switch op.Kind {
	case simfs.OpSync, simfs.OpSyncDir:
		return strings.HasPrefix(op.Path, "logdb/log") || strings.HasPrefix(op.Path, "logdb/objects")
	case simfs.OpCreate, simfs.OpWrite, simfs.OpTruncate, simfs.OpMkdir, simfs.OpRemove, simfs.OpRename:
	case simfs.OpOpen, simfs.OpRead, simfs.OpLstat, simfs.OpReadDir, simfs.OpLock:
	}
	return false
}

// tempNames returns the temporary files of fsys, as reads see them: of PutOriginal, of the objects of the engine and
// of its checkpoints.
func tempNames(fsys *simfs.FS) []string {
	var names []string
	fsys.Walk(func(p string, _ []byte) {
		if strings.HasPrefix(path.Base(p), ".tmp-") {
			names = append(names, p)
		}
	})
	return names
}

// TestOriginalAfterFailedSync shows why the store must be opened again after a restart of the machine also when a sync
// of PutOriginal has failed (finding F2 of phase 12), as after ErrFailed of the engine: the sync of files/ after the
// Mkdir of the directory of an original fails and drops the new name, which the page cache still has. A PutOriginal of
// the same original in the same boot finds the directory, syncs files/ again, which succeeds without the name, and
// returns nil; every crash then loses the original. After a restart, which a crash models, PutOriginal makes the
// directory again, and the original stays.
func TestOriginalAfterFailedSync(t *testing.T) {
	data := []byte("GIF89a")
	fsys := simfs.New("data")
	if err := fsys.Mkdir("files", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := fsys.SyncDir("."); err != nil {
		t.Fatal(err)
	}
	files, err := fsys.Sub("files")
	if err != nil {
		t.Fatal(err)
	}
	fsys.Inject(simfs.Fault{Match: func(op simfs.Op) bool { return op.Kind == simfs.OpSyncDir && op.Path == "files" },
		Err: syscall.EIO, Drop: true, Times: 1})
	s := &Store{files: files}
	if o, err := s.PutOriginal(t.Context(), bytes.NewReader(data)); !errors.Is(err, syscall.EIO) {
		t.Fatalf("PutOriginal() with a sync of files/ that fails = %+v, %v; want an error that wraps EIO", o, err)
	}
	// putAndCrash puts the original again with s, a store of files/ of fsys, and reports whether it is there after a
	// crash that keeps every change that is pending.
	putAndCrash := func(fsys *simfs.FS, s *Store) bool {
		t.Helper()
		putOriginal(t, s, data)
		files, err := fsys.Crash(keepAll(fsys)).Sub("files")
		if err != nil {
			t.Fatal(err)
		}
		ok, err := hasOriginal(&Store{files: files}, data)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	restarted := fsys.Crash(keepAll(fsys))
	files, err = restarted.Sub("files")
	if err != nil {
		t.Fatal(err)
	}
	if !putAndCrash(restarted, &Store{files: files}) {
		t.Error("after a restart, the original that PutOriginal has put again is missing after a crash")
	}
	if putAndCrash(fsys, s) {
		t.Error("without a restart, the original that PutOriginal has put again is there after a crash: F2 is fixed, " +
			"and TestStoreFaultMatrix may go on without a restart after a failed sync of PutOriginal")
	}
}

// TestStoreFaultMatrix runs the steps of the scenario of the matrices on the log backend in each placement with one
// fault (storeFaults) at one of their counted operations, for each operation and fault. A PutOriginal or an Update
// that the fault fails leaves no temporary file and is not seen, and the store goes on: the same step again succeeds.
// An Update fails with ErrFailed if the fault must stop the store (stopsStore), and only then; a stopped store fails
// every Update and its Close writes nothing. After a failed sync, of an Update, an open or a PutOriginal (finding F2 of
// phase 12), the machine is restarted before the store goes on, as ErrFailed asks: a crash that keeps what is pending.
// Every crash that simfs can make in the end (crashModels with 4 subsets; long: 64), and before each restart, after
// the step of the fault and after each later step (the same, but with -race only the one that loses everything that
// is not durable), opens to what SQLite has after the steps that have succeeded, with the failed step whole or not at
// all while its outcome is not known. A case is TestStoreFaultMatrix/<backend>/op=<n>-<kind>/<fault>.
func TestStoreFaultMatrix(t *testing.T) {
	t.Parallel() // with the other matrices, after the tests that do not run in parallel
	refs, err := theRefs()
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	cases, crashes := 0, 0
	t.Cleanup(func() {
		t.Logf("%d faults; %d crashes after them", cases, crashes)
	})
	forBackends(t, logBackends, func(t *testing.T, tb testBackend) {
		t.Parallel()
		o := matrixOptions(tb)
		base := newMatrixBase(t, o)
		c := runMatrixClean(t, base, o, refs)
		n := 0
		for _, op := range c.ops {
			for _, f := range storeFaults(op) {
				n++
				t.Run(fmt.Sprintf("op=%d-%v/%s", op.N, op.Kind, f.name), func(t *testing.T) {
					t.Parallel()
					checked := runStoreFault(t, c, base, o, refs, op, f)
					mu.Lock()
					crashes += checked
					mu.Unlock()
				})
			}
		}
		mu.Lock()
		cases += n
		mu.Unlock()
		t.Logf("%d operations, %d faults", len(c.ops), n)
	})
}

// runStoreFault runs the case of TestStoreFaultMatrix of the fault f at op, an operation of c, the run of the steps of
// the scenario without a fault from base with o. It returns the number of crashes that it has checked.
func runStoreFault(t *testing.T, c *matrixClean, base *simfs.FS, o logdb.Options, refs *matrixRefs, op simfs.Op,
	f storeFault) int {
	sc := theScenario()
	ctx := t.Context()
	at := c.stepOf(op.N)
	step := &sc.steps[at]
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf("%s\nthe fault %s at %v, in the step %s\nto run it alone: %s", fmt.Sprintf(format, args...), f.name,
			op, step.name, reproduce(t))
	}
	subsets := matrixSize(4, 4, 64)
	fsys := copyWorld(base)
	var fired atomic.Bool
	flt := f.fault
	flt.Match = func(x simfs.Op) bool {
		if !f.fault.Match(x) {
			return false
		}
		fired.Store(true)
		return true
	}
	fsys.Inject(flt)
	r := writer(fsys, o)
	defer func() { r.close() }()
	checked := 0
	// crashes checks that every crash that fsys can have now (those of fewModels, unless all) opens to one of want.
	crashes := func(when string, all bool, want ...wanted) {
		t.Helper()
		pending := fsys.Pending()
		models := crashModels(pending, subsets)
		if !all {
			models = fewModels(pending, subsets)
		}
		for _, m := range models {
			checked++
			s, err := open("data", options{log: &o, fs: fsys.Crash(m)})
			if err != nil {
				fail("open() after the crash %s %s:\n%s\n%v", m.Name, when, keptText(pending, m), err)
			}
			_, _, err = checkState(t, s, want...)
			info := logDB(t, s).Recovery()
			_ = s.Close()
			if err != nil {
				fail("after the crash %s %s:\n%s\n%v\nrecovery: %+v", m.Name, when, keptText(pending, m), err, info)
			}
		}
	}
	// restart closes the store, restarts the machine, and opens the store again.
	restart := func(when string) {
		t.Helper()
		r.close()
		fsys = copyWorld(fsys)
		r = writer(fsys, o)
		if err := r.do(ctx, &mstep{name: "open", kind: mOpen}); err != nil {
			fail("open() after the restart %s: %v", when, err)
		}
	}
	// noTemps checks that what has failed with err has left no temporary file.
	noTemps := func(err error) {
		t.Helper()
		if temps := tempNames(fsys); temps != nil {
			fail("the step returned %v, and left %q", err, temps)
		}
	}
	for i := range at {
		if err := r.do(ctx, &sc.steps[i]); err != nil {
			fail("the step %s before the fault: %v", sc.steps[i].name, err)
		}
	}
	synced := op.Kind == simfs.OpSync || op.Kind == simfs.OpSyncDir
	err := r.do(ctx, step)
	if !fired.Load() {
		fail("the step has returned %v, and the fault has not fired", err)
	}
	switch step.kind {
	case mOpen:
		if err == nil {
			break // the fault of a write of head, which fails nothing
		}
		if !errors.Is(err, logdb.ErrFailed) || !synced {
			fail("open() returned %v; ErrFailed is the error of a failed sync, and only of that", err)
		}
		crashes("after the failed open", false, stateAt(refs, at, at))
		restart("after the failed open")
	case mOriginal:
		if err == nil {
			fail("PutOriginal returned nil")
		}
		noTemps(err)
		if _, herr := hasOriginal(r.s, step.original); herr != nil {
			fail("PutOriginal returned %v; after it: %v", err, herr)
		}
		if synced {
			// F2: the page cache may have a directory whose name never becomes durable, which PutOriginal would
			// take as durable after it has synced files/ again (TestOriginalAfterFailedSync).
			crashes("after the failed PutOriginal", false, stateAt(refs, at, at))
			restart("after the failed PutOriginal")
		}
		if err2 := r.do(ctx, step); err2 != nil {
			fail("PutOriginal returned %v, and then %v", err, err2)
		}
	case mUpdate:
		stops := stopsStore(op)
		if err == nil {
			if stops {
				fail("the Update returned nil, want ErrFailed")
			}
			break // the fault of a write of head or of an automatic checkpoint, which fail nothing
		}
		if errors.Is(err, logdb.ErrFailed) != stops {
			fail("the Update returned %v; ErrFailed is the error of a failed sync of the log or the objects, and "+
				"only of that", err)
		}
		if _, _, verr := checkState(t, r.s, stateAt(refs, at, at)); verr != nil {
			fail("the Update returned %v; after it: %v", err, verr)
		}
		if !stops {
			noTemps(err)
			if err2 := r.do(ctx, step); err2 != nil {
				fail("the Update returned %v, and then %v", err, err2)
			}
			break
		}
		if uerr := r.s.Update(ctx, func(*Tx) error { return errors.New("called") }); !errors.Is(uerr, logdb.ErrFailed) {
			fail("an Update of the stopped store returned %v, want ErrFailed", uerr)
		}
		before := fsys.Counted()
		cerr := r.s.Close()
		r.s = nil
		if cerr != nil || fsys.Counted() != before {
			fail("Close of the stopped store returned %v after %d operations, want nil after none", cerr,
				fsys.Counted()-before)
		}
		// Its outcome is not known: it may be durable, or not, which Views did not see.
		crashes("after the stop", false, stateAt(refs, at, at), stateAt(refs, at+1, at))
		restart("after the stop")
		w, _, verr := checkState(t, r.s, stateAt(refs, at, at), stateAt(refs, at+1, at))
		if verr != nil {
			fail("after the restart: %v", verr)
		}
		if w.stateRef == at {
			if err := r.do(ctx, step); err != nil {
				fail("the Update again after the restart: %v", err)
			}
		}
	case mClose:
		if err != nil {
			noTemps(err) // the checkpoint or head has failed; the store is closed
		}
	}
	crashes("after the step "+step.name, false, stateAt(refs, at+1, at+1))
	for i := at + 1; i < len(sc.steps); i++ {
		st := &sc.steps[i]
		if err := r.do(ctx, st); err != nil {
			fail("the step %s after the fault: %v", st.name, err)
		}
		crashes("after the step "+st.name, false, stateAt(refs, i+1, i+1))
	}
	crashes("in the end", true, stateAt(refs, len(sc.steps), len(sc.steps)))
	return checked
}
