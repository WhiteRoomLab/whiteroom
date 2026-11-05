package logdb

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

// The openers of TestKillMatrix, after the kill of the process that ran the steps.
const (
	killWriter = "writer"        // Open, an Update, and a crash after it
	killReader = "reader"        // OpenReader
	killHung   = "reader-hung"   // OpenReader beside the process, which hangs with its locks
	killBefore = "reader-before" // a reader that opened after the first step, and refreshes after the kill
)

// TestKillMatrix runs each scenario of matrixScenarios in each placement in a process that is killed after each of
// its counted operations (simfs.FS.KillAt), which leaves the page cache as it is. Then a writer, or a reader, opens the
// store: it has the steps that returned nil and the step that was running whole or not at all, and everything that it
// shows is durable: for each crash that simfs can make then (crashModels with 16 subsets; with -race only the one that
// loses everything that is not durable; 256 in the long mode), Open finds that state; an Update of the writer is
// durable too. A reader beside the process, which hangs with its locks (simfs.FS.HangAt), shows only what is durable;
// so does a reader that has opened before the kill when it refreshes after it. The readers are not open during a
// compaction or a GC, which maintenance runs. A case is TestKillMatrix/<placement>/<scenario>/k=<kill point>/<opener>/
// <model>.
func TestKillMatrix(t *testing.T) {
	withBaseBytes(t, 400)
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
						_ = m.String() // before the kill points run in parallel
					}
					ops := runClean(t, &sc, base, o, states[len(states)-1]).ops
					mu.Lock()
					points += len(ops) + 1
					mu.Unlock()
					for k := range len(ops) + 1 {
						t.Run(fmt.Sprintf("k=%d", k), func(t *testing.T) {
							t.Parallel()
							for _, opener := range []string{killWriter, killReader, killHung, killBefore} {
								t.Run(opener, func(t *testing.T) {
									n := killAndOpen(t, base, o, sc.steps, states, ops, k, opener, subsets)
									mu.Lock()
									cases += n
									mu.Unlock()
								})
							}
						})
					}
				})
			}
		})
	}
}

// maintenance reports whether s runs in maintenance, when no reader can have the store open.
func maintenance(s *step) bool {
	return s.kind == stepCompact || s.kind == stepGC
}

// killAndOpen runs the case of TestKillMatrix of the kill after the counted operation k of steps, whose run without a
// kill has the counted operations ops and the states, with opener. It returns the number of crashes that it has
// checked.
func killAndOpen(t *testing.T, base *simfs.FS, o Options, steps []step, states []*model, ops []simfs.Op, k int,
	opener string, subsets int) int {
	sim := copyFS(base)
	var before *DB // of killBefore
	defer func() {
		if before != nil {
			_ = before.Close()
		}
	}()
	if opener == killHung && k == 0 {
		t.Skip("no process hangs before it begins")
	}
	proc := sim.KillAt(k)
	if opener == killHung {
		proc = sim.HangAt(k)
	}
	var res result
	var berr error
	running := 0 // the step that the process runs
	ran := make(chan result, 1)
	go func() {
		ran <- runSteps(proc.FS(), o, steps, func(i int) {
			running = i
			if opener == killBefore && i == 1 {
				before, berr = OpenReader(sim, Options{})
			}
		})
	}()
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
	if berr != nil {
		t.Fatalf("OpenReader after the first step: %v", berr)
	}
	begun := steps[:min(res.done+1, len(steps))]
	switch {
	case opener == killHung && res.done < len(steps) && maintenance(&steps[res.done]):
		t.Skip("a compaction or a GC runs in maintenance, without readers")
	case opener == killBefore && (before == nil || slices.ContainsFunc(begun[1:], func(s step) bool { return maintenance(&s) })):
		t.Skip("a reader is open only after the first step, and never in maintenance")
	}
	want := states[res.done:min(res.done+2, len(states))]
	how := "was killed"
	if opener == killHung {
		how = "hung"
	}
	where := fmt.Sprintf("the process %s %s; %d steps had returned nil", how, pointText(ops, k), res.done)
	var db *DB
	var err error
	switch opener {
	case killWriter:
		db, err = Open(sim, o)
	case killReader, killHung:
		db, err = OpenReader(sim, Options{})
	case killBefore:
		db, before = before, nil
		err = db.View(context.Background(), func(*Tx) error { return nil })
	}
	if err != nil {
		t.Fatalf("the %s: %v\n%s\nto run it alone: %s", opener, err, where, reproduce(t))
	}
	defer func() { _ = db.Close() }()
	got, err := checkInvariants(db, want...)
	if err != nil {
		t.Fatalf("the %s: %v\n%s\nto run it alone: %s", opener, err, where, reproduce(t))
	}
	// What a crash may leave: what the opener shows, or, beside a process that hangs after it has synced a commit that
	// head does not name yet, that commit.
	durable := []*model{got}
	if opener == killHung && len(want) == 2 && got == want[0] {
		durable = want
	}
	pending := sim.Pending()
	models := fewModels(pending, subsets)
	for _, cm := range models {
		t.Run(cm.Name, func(t *testing.T) {
			db, err := Open(sim.Crash(cm), o)
			if err == nil {
				defer func() { _ = db.Close() }()
				_, err = checkState(db, durable...)
			}
			if err != nil {
				t.Fatalf("%v\n%s; then the %s opened, and then the crash %s:\n%s\nto run it alone: %s", err, where, opener,
					cm.Name, keptText(pending, cm), reproduce(t))
			}
		})
	}
	if opener == killWriter {
		if err := checkContinues(db, sim, o, got); err != nil {
			t.Fatalf("%v\n%s; then the writer opened\nto run it alone: %s", err, where, reproduce(t))
		}
	}
	return len(models)
}
