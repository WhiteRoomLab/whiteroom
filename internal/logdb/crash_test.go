package logdb

import (
	"fmt"
	"sync"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

// pointText describes the point after the operation k of ops, the counted operations of a run without a crash.
func pointText(ops []simfs.Op, k int) string {
	s := fmt.Sprintf("after operation %d of %d", k, len(ops))
	if k > 0 {
		s += fmt.Sprintf(" (%v)", ops[k-1])
	}
	if k < len(ops) {
		s += fmt.Sprintf(", before %v", ops[k])
	}
	return s
}

// TestCrashMatrix runs each scenario of matrixScenarios in each placement and crashes it after each of its counted
// operations; then, for each crash that simfs can make of what was not durable then (crashModels with 16 subsets, 4
// with -race, 256 in the long mode: the writes of head in one group), Open must succeed, and the store must have every step that returned nil and the step that was running
// whole or not at all, every object with its data, a checkpoint that is not damaged, and pass Verify. A reader that
// opens the crash first finds the same state, and an Update after Open is durable without Close. A case is
// TestCrashMatrix/<placement>/<scenario>/k=<crash point>/<model>.
func TestCrashMatrix(t *testing.T) {
	withBaseBytes(t, 400) // base records of a few ops each, in more than one segment
	subsets := matrixSize(16, 4, 256)
	var mu sync.Mutex // the scenarios run in parallel: each has a world of its own
	points, cases := 0, 0
	found := tally{}
	t.Cleanup(func() {
		t.Logf("%d crash points, %d crashes (subsets of at most %d); recovered: %v", points, cases, subsets, found)
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
						_ = m.String() // before the crash points run in parallel
					}
					ops := runClean(t, &sc, base, o, states[len(states)-1]).ops
					scCases, scFound := 0, tally{}
					t.Cleanup(func() {
						mu.Lock()
						defer mu.Unlock()
						points += len(ops) + 1
						cases += scCases
						for k, n := range scFound {
							found[k] += n
						}
						t.Logf("%d operations, %d crash points, %d crashes; recovered: %v", len(ops), len(ops)+1, scCases,
							scFound)
					})
					for k := range len(ops) + 1 {
						t.Run(fmt.Sprintf("k=%d", k), func(t *testing.T) {
							t.Parallel()
							fsys := copyFS(base)
							fsys.CrashAt(k)
							res := runSteps(fsys, o, sc.steps, nil)
							want := states[res.done:min(res.done+2, len(states))]
							running := "none"
							if res.done < len(sc.steps) {
								running = fmt.Sprintf("%s, which returned %v", sc.steps[res.done].name, res.err)
							}
							pending := fsys.Pending()
							models := crashModels(pending, subsets)
							mu.Lock()
							scCases += len(models)
							mu.Unlock()
							for _, cm := range models {
								t.Run(cm.Name, func(t *testing.T) {
									fail := func(format string, args ...any) {
										t.Helper()
										t.Fatalf("%s\n%s; %d steps returned nil; the step running: %s\nthe model %s:\n%s\nto run it alone: %s",
											fmt.Sprintf(format, args...), "a crash "+pointText(ops, k), res.done, running, cm.Name,
											keptText(pending, cm), reproduce(t))
									}
									crashed := fsys.Crash(cm)
									db, err := Open(crashed, o)
									if err != nil {
										fail("Open: %v", err)
									}
									defer func() { _ = db.Close() }()
									info := db.Recovery()
									mu.Lock()
									scFound.add(info)
									mu.Unlock()
									// Every setup leaves a checkpoint, which only a rename replaces.
									if info.Checkpoint != "used" && info.Checkpoint != "stale" {
										fail("the checkpoint is %q, want used or stale\nrecovery: %+v", info.Checkpoint, info)
									}
									got, err := checkInvariants(db, want...)
									if err != nil {
										fail("%v\nrecovery: %+v", err, info)
									}
									// The writer has changed crashed; the reader opens a crash of its own.
									if err := checkReader(fsys.Crash(cm), got); err != nil {
										fail("%v\nrecovery: %+v", err, info)
									}
									if err := checkContinues(db, crashed, o, got); err != nil {
										fail("%v\nrecovery: %+v", err, info)
									}
								})
							}
						})
					}
				})
			}
		})
	}
}
