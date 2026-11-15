package store

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
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
// returns ErrFailed: the failure of a sync of the log or of the objects of the engine, not that of a checkpoint or of
// head (concept section 3.5).
func stopsStore(op simfs.Op) bool {
	switch op.Kind {
	case simfs.OpSync, simfs.OpSyncDir:
		return strings.HasPrefix(op.Path, "logdb/log") || strings.HasPrefix(op.Path, "logdb/objects")
	case simfs.OpCreate, simfs.OpWrite, simfs.OpTruncate, simfs.OpMkdir, simfs.OpRemove, simfs.OpRename:
	case simfs.OpOpen, simfs.OpRead, simfs.OpLstat, simfs.OpReadDir, simfs.OpLock:
	}
	return false
}

// tempNames returns the temporary files of fsys, as reads see them: of PutOriginal, of the objects of the engine and of its
// checkpoints.
func tempNames(fsys *simfs.FS) []string {
	var names []string
	fsys.Walk(func(p string, _ []byte) {
		if strings.HasPrefix(path.Base(p), ".tmp-") {
			names = append(names, p)
		}
	})
	return names
}

// TestOriginalAfterFailedSync checks the rule after a failed sync of PutOriginal (F2 in concept section 3.5): the sync
// may drop the names that it would have made durable, which the page cache still has, and which later syncs do not
// write. Without a stop, a PutOriginal in the same boot found the directory whose name a failed sync of files/ had
// dropped, synced files/ again, which succeeded without the name, and returned nil for an original that every crash then
// lost. The daemon stops: every PutOriginal and every Update of it fails with the stop and writes nothing, until a new
// daemon opens the directory after a restart of the machine, which a crash models; then PutOriginal saves the original,
// which a crash does not lose. A PutOriginal and an Update of one store do not run at the same time: they take turns on
// the connection of the writer (dbserver.TestOriginalAfterFailedSync has a request that began before the stop).
func TestOriginalAfterFailedSync(t *testing.T) {
	data, other := []byte("GIF89a"), []byte("GIF87a")
	isTemp := func(op simfs.Op) bool { return op.Kind == simfs.OpSync && strings.HasPrefix(op.Path, "files/.tmp-") }
	// newLog makes a store in a new world.
	newLog := func(t *testing.T) (*simfs.FS, *Store) {
		t.Helper()
		fsys := simfs.New("data")
		s, err := create("data", options{log: &logdb.Options{}, fs: fsys})
		if err != nil {
			t.Fatalf("create() = %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return fsys, s
	}
	// restart restarts the machine with s open in fsys: a new daemon, and a store of it that has no original but that
	// PutOriginal saves, whose name a crash that loses what is not durable keeps.
	restart := func(t *testing.T, fsys *simfs.FS, s *Store) {
		t.Helper()
		restarted := fsys.Crash(keepAll(fsys))
		if err := s.Close(); err != nil {
			t.Fatalf("Close() of the stopped store = %v", err)
		}
		r, err := open("data", options{log: &logdb.Options{}, fs: restarted})
		if err != nil {
			t.Fatalf("open() after a restart = %v", err)
		}
		t.Cleanup(func() { _ = r.Close() })
		putOriginal(t, r, data)
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
		c, err := open("data", options{log: &logdb.Options{}, fs: restarted.Crash(loseAll)})
		if err != nil {
			t.Fatalf("open() after a crash = %v", err)
		}
		t.Cleanup(func() { _ = c.Close() })
		if ok, err := hasOriginal(c, data); !ok || err != nil {
			t.Errorf("after a restart, the original that PutOriginal has saved is not there after a crash: %v, %v", ok, err)
		}
	}
	for _, tt := range []struct {
		name  string
		match func(op simfs.Op) bool
	}{
		{"of the temporary file", isTemp},
		{"of files/", func(op simfs.Op) bool { return op.Kind == simfs.OpSyncDir && op.Path == "files" }},
		{"of the directory", func(op simfs.Op) bool {
			return op.Kind == simfs.OpSyncDir && strings.HasPrefix(op.Path, "files/")
		}},
	} {
		t.Run("the sync "+tt.name, func(t *testing.T) {
			fsys, s := newLog(t)
			fsys.Inject(simfs.Fault{Match: tt.match, Err: syscall.EIO, Drop: true, Times: 1})
			if o, err := s.PutOriginal(t.Context(), bytes.NewReader(data)); !isStopped(err) {
				t.Fatalf("PutOriginal() with a sync that fails = %+v, %v; want the stop of the daemon", o, err)
			}
			before := fsys.Counted()
			for _, d := range [][]byte{data, other} {
				if o, err := s.PutOriginal(t.Context(), bytes.NewReader(d)); !isStopped(err) {
					t.Errorf("PutOriginal() after the failed sync = %+v, %v; want the stop of the daemon", o, err)
				}
			}
			err := s.Update(t.Context(), withRecord(putDoc(content.RefEntry, entryID, docJSON(content.RefEntry, entryID))))
			if !isStopped(err) {
				t.Errorf("Update() after the failed sync = %v, want the stop of the daemon", err)
			}
			if n := fsys.Counted() - before; n != 0 {
				t.Errorf("PutOriginal() and Update() after the failed sync have made %d operations, want none", n)
			}
			// Reads go on.
			view(t, s, func(tx *Tx) error {
				if _, err := tx.Document(content.RefEntry, entryID); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("Document() = %v, want an error that wraps fs.ErrNotExist: the Update has written nothing", err)
				}
				return nil
			})
			restart(t, fsys, s)
		})
	}
}

// TestStoreFaultMatrix runs the steps of the scenario of the matrices on the log backend in each placement with one
// fault (storeFaults) at one of their counted operations, for each operation and fault. A PutOriginal or an Update
// that the fault fails leaves no temporary file and is not seen, and the store goes on: the same step again succeeds.
// An Update fails with the stop of the daemon if the fault must stop the store (stopsStore), and only then; a stopped store fails
// every Update and its Close writes nothing. A PutOriginal fails with the stop of the daemon if the fault is of a sync,
// and only then, and so does the same step again, with no operation (TestOriginalAfterFailedSync). After a failed sync,
// of an Update, an open or a PutOriginal, the machine is restarted before the store goes on, as the errors ask: a crash
// that keeps what is pending.
// Every crash that simfs can make in the end (crashModels with 4 subsets; long: 64), and before each restart, after
// the step of the fault and after each later step (the same, but with -race only the one that loses everything that
// is not durable), opens to what the model has after the steps that have succeeded, with the failed step whole or not
// at all while its outcome is not known. A case is TestStoreFaultMatrix/<backend>/op=<n>-<kind>/<fault>.
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
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
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
		// The engine of the daemon fails to open: its own error, which the harness returns as it is.
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
		if isStopped(err) != synced {
			fail("PutOriginal returned %v; the stop of the daemon is the error of a failed sync, and only of that", err)
		}
		if synced {
			before := fsys.Counted()
			if err2 := r.do(ctx, step); !isStopped(err2) || fsys.Counted() != before {
				fail("PutOriginal returned %v, and then %v after %d operations; want the stop after none", err, err2,
					fsys.Counted()-before)
			}
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
				fail("the Update returned nil, want the stop of the daemon")
			}
			break // the fault of a write of head or of an automatic checkpoint, which fail nothing
		}
		if isStopped(err) != stops {
			fail("the Update returned %v; the stop of the daemon is the error of a failed sync of the log or the objects, "+
				"and only of that", err)
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
		if uerr := r.s.Update(ctx, func(tx *Tx) error { return tx.Record(eventRecord("stopped")) }); !isStopped(uerr) {
			fail("an Update of the stopped store returned %v, want the stop of the daemon", uerr)
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
