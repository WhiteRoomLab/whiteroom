package store

import (
	"fmt"
	"sync"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

// The openers of TestStoreKillMatrix, after the kill of the process that ran the steps.
const (
	killWriter = "writer"        // open, the probe, and a crash after it
	killReader = "reader"        // openLogReader
	killHung   = "reader-hung"   // openLogReader beside the process, which hangs with its locks
	killBefore = "reader-before" // a reader that opened after the first step, and refreshes after the kill
)

// TestStoreKillMatrix runs the steps of the scenario of the matrices on the log backend in each placement in a process
// that is killed after each of their counted operations (simfs.FS.KillAt), which leaves the page cache as it is. Then
// a writer, or a reader, opens the store: it has what the model has after the steps that returned nil and the step that
// was running, whole or not at all, with its audit events and the originals that checkState wants, and everything
// that it shows is durable: for each crash that simfs can make then (crashModels with 16 subsets; with -race only the
// one that loses everything that is not durable; 256 in the long mode), the store opens to that state. The probe of
// the writer is durable too: a PutOriginal in a directory that the killed process may have made without syncing its
// name, and the Update after it. A reader beside the process, which hangs with its locks (simfs.FS.HangAt), shows
// only what is durable; so does a reader that has opened after the first step when it refreshes after the kill. A
// case is TestStoreKillMatrix/<backend>/k=<kill point>/<opener>/<model>.
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
	forBackends(t, logBackends, func(t *testing.T, tb testBackend) {
		t.Parallel()
		o := matrixOptions(tb)
		base := newMatrixBase(t, o)
		ops := runMatrixClean(t, base, o, refs).ops
		mu.Lock()
		points += len(ops) + 1
		mu.Unlock()
		for k := range len(ops) + 1 {
			t.Run(fmt.Sprintf("k=%d", k), func(t *testing.T) {
				t.Parallel()
				for _, opener := range []string{killWriter, killReader, killHung, killBefore} {
					t.Run(opener, func(t *testing.T) {
						n := killAndOpen(t, base, o, refs, ops, k, opener, subsets)
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
// scenario on a copy of base with o, whose run without a kill has the counted operations ops, with opener. It returns
// the number of crashes that it has checked.
func killAndOpen(t *testing.T, base *simfs.FS, o logdb.Options, refs *matrixRefs, ops []simfs.Op, k int, opener string,
	subsets int) int {
	sc := theScenario()
	if opener == killHung && k == 0 {
		t.Skip("no process hangs before it begins")
	}
	sim := copyWorld(base)
	var before *Store // of killBefore
	defer func() {
		if before != nil {
			_ = before.Close()
		}
	}()
	proc := sim.KillAt(k)
	if opener == killHung {
		proc = sim.HangAt(k)
	}
	r := writer(proc.FS(), o)
	var berr error
	running := 0 // the step that the process runs
	ran := make(chan result, 1)
	go func() {
		res := r.run(sc.steps, func(i int) {
			running = i
			if opener == killBefore && i == 1 {
				before, berr = openReader(sim, o)
			}
		})
		r.close()
		ran <- res
	}()
	var res result
	if opener == killHung {
		select {
		case <-proc.Hung():
		case res = <-ran:
			t.Fatalf("the process has ended before operation %d: %v", k, res)
		}
		res = result{done: running} // what it returns after it is released does not count
		defer func() {
			proc.Release()
			<-ran
		}()
	} else {
		res = <-ran
	}
	switch {
	case berr != nil:
		t.Fatalf("openLogReader() after the first step: %v", berr)
	case opener == killBefore && before == nil:
		t.Skip("a reader is open only after the first step")
	}
	want := []wanted{stateAt(refs, res.done, res.done)}
	if res.done < len(sc.steps) {
		want = append(want, stateAt(refs, res.done+1, res.done))
	}
	how := "was killed"
	if opener == killHung {
		how = "hung"
	}
	where := fmt.Sprintf("the process %s %s; %d steps had returned nil", how, pointText(ops, k), res.done)
	var s *Store
	var err error
	switch opener {
	case killWriter:
		s, err = open("data", options{log: &o, fs: sim})
	case killReader, killHung:
		s, err = openReader(sim, o)
	case killBefore:
		s, before = before, nil // its View refreshes
	}
	if err != nil {
		t.Fatalf("the %s: %v\n%s\nto run it alone: %s", opener, err, where, reproduce(t))
	}
	defer func() { _ = s.Close() }()
	w, _, err := checkState(t, s, want...)
	if err != nil {
		t.Fatalf("the %s: %v\n%s\nto run it alone: %s", opener, err, where, reproduce(t))
	}
	// What a crash may leave: what the opener shows, or, beside a process that hangs after it has synced a commit that
	// head does not name yet, that commit.
	durable := []wanted{w}
	if opener == killHung && len(want) == 2 && w.stateRef == want[0].stateRef {
		durable = want
	}
	pending := sim.Pending()
	models := fewModels(pending, subsets)
	for _, cm := range models {
		t.Run(cm.Name, func(t *testing.T) {
			c, err := open("data", options{log: &o, fs: sim.Crash(cm)})
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
		if err := checkProbe(t, s, sim, o, refs, w, res.done); err != nil {
			t.Fatalf("%v\n%s; then the writer opened\nto run it alone: %s", err, where, reproduce(t))
		}
	}
	return len(models)
}
