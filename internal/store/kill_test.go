package store

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// The openers of TestStoreKillMatrix, after the kill of the process that ran the steps. The process is the daemon and
// the store that is its client, in one process of simfs, whose file system dies with it.
const (
	killWriter = "writer"        // a new daemon opens the store, a writer client probes it, and a crash follows
	killReader = "reader"        // a new daemon opens it, and a reader client reads it
	killHung   = "reader-hung"   // a reader client of the daemon, which hangs with its locks
	killBefore = "reader-before" // a reader client that opened after the first step, and goes on with the daemon after the kill
)

// killLimit is how long the steps of a case may take, which a daemon that hangs would let them take for ever.
const killLimit = time.Minute

// TestStoreKillMatrix runs the steps of the scenario of the matrices on the log backend in each placement in a process
// that is killed after each of their counted operations (simfs.FS.KillAt), which leaves the page cache as it is. Then
// a new daemon opens the store, and a writer client, or a reader client, reads it: it has what the model has after the
// steps that returned nil and the step that was running, whole or not at all, with its audit events and the originals
// that checkState wants, and everything that it shows is durable: for each crash that simfs can make then (crashModels
// with 16 subsets; with -race only the one that loses everything that is not durable; 256 in the long mode), the store
// opens to that state. The probe of the writer is durable too: a PutOriginal in a directory that the killed process may
// have made without syncing its name, and the Update after it. A reader client that has connected after the first step
// goes on with the new daemon when the old one has been killed (it dials the same sockets), and shows that state. A
// reader client of the daemon that hangs (simfs.FS.HangAt), while the requests of the writer do not return, shows the
// last commit that is durable or the one after it, and the last commit that a step has returned for; then every crash
// that simfs can make of the world that the hang leaves opens to a state that is not before the one that the reader
// has seen. A case is TestStoreKillMatrix/<backend>/k=<kill point>/<opener>/<model>.
func TestStoreKillMatrix(t *testing.T) {
	t.Parallel() // with the other matrices, after the tests that do not run in parallel
	refs, err := theRefs()
	if err != nil {
		t.Fatal(err)
	}
	subsets := matrixSize(16, 0, 256) // with -race, only the crash that loses everything that is not durable
	var mu sync.Mutex
	points, cases := 0, 0
	t.Cleanup(func() {
		models := fmt.Sprintf("subsets of at most %d", subsets)
		if raceEnabled && !longTests() {
			models = "with -race, only the crash that loses everything that is not durable"
		}
		t.Logf("%d kill points, %d crashes after them (%s)", points, cases, models)
	})
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		t.Parallel()
		o := matrixOptions(tb)
		base := newMatrixBase(t, o)
		clean := runMatrixClean(t, base, o, refs)
		mu.Lock()
		points += len(clean.ops) + 1
		mu.Unlock()
		for k := range len(clean.ops) + 1 {
			t.Run(fmt.Sprintf("k=%d", k), func(t *testing.T) {
				t.Parallel()
				// No process hangs before it begins, and a reader client connects when the second step begins, which it
				// does if the first one, the open, has not been cut.
				openers := []string{killWriter, killReader}
				if k > 0 {
					openers = append(openers, killHung)
				}
				if k > clean.starts[1] {
					openers = append(openers, killBefore)
				}
				for _, opener := range openers {
					t.Run(opener, func(t *testing.T) {
						n := killAndOpen(t, base, o, refs, clean, k, opener, subsets)
						mu.Lock()
						cases += n
						mu.Unlock()
					})
				}
			})
		}
	})
}

// killAndOpen runs the case of TestStoreKillMatrix of the kill after the counted operation k of the steps of the
// scenario on a copy of base with o, whose run without a kill is clean, with opener. It returns the number of crashes
// that it has checked.
func killAndOpen(t *testing.T, base *simfs.FS, o logdb.Options, refs *matrixRefs, clean *matrixClean, k int,
	opener string, subsets int) int {
	sc := theScenario()
	sim := copyWorld(base)
	proc := sim.KillAt(k)
	if opener == killHung {
		proc = sim.HangAt(k)
	}
	pfs := proc.FS() // the one file system of the process: its daemons are found by it
	r := writer(pfs, o)
	ctx, cancel := context.WithTimeout(t.Context(), killLimit)
	defer cancel()
	r.ctx = ctx
	var running atomic.Int64 // the step that the process runs
	var before *Store        // of killBefore
	var berr error
	var sockets string
	if opener == killBefore {
		sockets = socketDir(t)
	}
	defer func() {
		if before != nil {
			_ = before.Close()
		}
	}()
	ran := make(chan result, 1)
	go func() {
		res := r.run(sc.steps, func(i int) {
			running.Store(int64(i))
			if opener == killBefore && i == 1 {
				before, berr = connectBefore(r.s, sockets)
			}
		})
		r.close()
		ran <- res
	}()
	var res result
	var seen []uint64 // of killHung: the last commit that a reader sees while the daemon hangs
	if opener == killHung {
		select {
		case <-proc.Hung():
		case res = <-ran:
			t.Fatalf("the process has ended before operation %d: %v", k, res)
		}
		defer func() {
			proc.Release()
			<-ran
		}()
		res = result{done: int(running.Load())} // what it returns after it is released does not count
		// A daemon that is being opened or closed has no reader to connect to.
		if kind := sc.steps[res.done].kind; kind == mOriginal || kind == mUpdate {
			n, err := hungReader(ctx, pfs, o)
			if err != nil {
				t.Fatalf("a reader beside the daemon that hangs %s: %v", pointText(clean.ops, k), err)
			}
			seen = append(seen, n)
		}
	} else {
		res = <-ran
	}
	switch {
	case berr != nil:
		t.Fatalf("the reader client after the first step: %v", berr)
	case opener == killBefore && before == nil:
		t.Fatalf("no reader client after the first step, which the kill %s has not cut", pointText(clean.ops, k))
	}
	want := []wanted{stateAt(refs, res.done, res.done)}
	if res.done < len(sc.steps) {
		want = append(want, stateAt(refs, res.done+1, res.done))
	}
	how := "was killed"
	if opener == killHung {
		how = "hung"
	}
	where := fmt.Sprintf("the process %s %s; %d steps had returned nil", how, pointText(clean.ops, k), res.done)
	// durable are the states that every crash that follows must open to.
	var durable []wanted
	var s *Store
	var err error
	switch opener {
	case killWriter:
		s, err = open("data", options{log: &o, fs: sim, wait: writerWait})
	case killReader, killBefore:
		s, err = openReader(sim, o)
	}
	switch opener {
	case killHung:
		durable = hungDurable(t, clean, res.done, want, seen, where)
	default:
		if err != nil {
			t.Fatalf("the %s: %v\n%s\nto run it alone: %s", opener, err, where, reproduce(t))
		}
		defer func() { _ = s.Close() }()
		w, _, err := checkState(t, s, want...)
		if err != nil {
			t.Fatalf("the %s: %v\n%s\nto run it alone: %s", opener, err, where, reproduce(t))
		}
		durable = []wanted{w}
		if opener == killBefore {
			// The daemon that was killed is gone and the reader client has not been told: its next View is the
			// first request of the new daemon, whose sockets are those of the old one, and it shows what the new one does.
			if err := daemonOf(t, s).Listen(sockets); err != nil {
				t.Fatalf("Listen() = %v", err)
			}
			bw, _, err := matchState(t, before, want...)
			if err != nil {
				t.Fatalf("the reader client of the daemon that was killed: %v\n%s\nto run it alone: %s", err, where, reproduce(t))
			}
			if bw.stateRef != w.stateRef {
				t.Fatalf("the reader client of the daemon that was killed has %s, the new daemon has %s\n%s\nto run it alone: %s",
					bw.at, w.at, where, reproduce(t))
			}
		}
	}
	pending := sim.Pending()
	models := fewModels(pending, subsets)
	for _, cm := range models {
		t.Run(cm.Name, func(t *testing.T) {
			c, err := open("data", options{log: &o, fs: sim.Crash(cm), wait: writerWait})
			if err == nil {
				defer func() { _ = c.Close() }()
				_, _, err = checkState(t, c, durable...)
			}
			if err != nil {
				t.Fatalf("%v\n%s; then the %s opened, and then the crash %s:\n%s\nto run it alone: %s", err, where,
					opener, cm.Name, keptText(pending, cm), reproduce(t))
			}
		})
	}
	if opener == killWriter {
		if err := checkProbe(t, s, sim, o, refs, durable[0], res.done); err != nil {
			t.Fatalf("%v\n%s; then the writer opened\nto run it alone: %s", err, where, reproduce(t))
		}
	}
	return len(models)
}

// connectBefore makes the reader client of the daemon of s, a writer store that has just opened, which connects to the
// sockets that the daemon listens on in the directory sockets.
func connectBefore(s *Store, sockets string) (*Store, error) {
	info, err := lookup(s)
	if err != nil {
		return nil, err
	}
	if err := info.h.Listen(sockets); err != nil {
		return nil, err
	}
	return openOn(context.Background(), dbclient.SocketDir(sockets), dbwire.RoleReader, time.Now, dbclient.Options{})
}

// hungReader returns the number of the last commit that a new reader client of the daemon of fsys, which hangs, sees.
func hungReader(ctx context.Context, fsys vfs.FS, o logdb.Options) (uint64, error) {
	_, d, err := acquire("data", options{log: &o, fs: fsys})
	if err != nil {
		return 0, err
	}
	c, err := dbclient.Open(ctx, d, dbwire.RoleReader, dbclient.Options{DialWait: -1})
	if err != nil {
		return 0, err
	}
	defer func() { _ = c.Close() }()
	var seq uint64
	err = c.View(ctx, func(tx *dbclient.Tx) error {
		seq = tx.Seq()
		return nil
	})
	return seq, err
}

// hungDurable returns the states that every crash of the world of a daemon that hangs, after done steps that returned
// nil, must open to, when a reader client has seen the commit seen (if any): its state, or one after it; the states of
// want that the commit does not tell apart are all of them.
func hungDurable(t *testing.T, clean *matrixClean, done int, want []wanted, seen []uint64, where string) []wanted {
	t.Helper()
	if seen == nil {
		return want
	}
	n := seen[0]
	lo, hi := clean.seqs[done], clean.seqs[done+1]
	switch {
	case n < lo || n > hi:
		t.Fatalf("a reader beside the daemon that hangs sees the commit %d, want one from %d, that of the steps that "+
			"returned nil, to %d\n%s", n, lo, hi, where)
	case n == hi && lo < hi:
		return want[1:]
	}
	return want
}
