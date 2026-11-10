package logdb

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// longTests reports whether WHITEROOM_LONG_TESTS asks for the long versions of the matrices.
func longTests() bool {
	return os.Getenv("WHITEROOM_LONG_TESTS") == "1"
}

// matrixSize returns n, the size of a matrix, or fewer with -race, which makes every case several times slower (the
// matrices run whole without it, as in CI), or long in the long mode.
func matrixSize(n, race, long int) int {
	switch {
	case longTests():
		return long
	case raceEnabled:
		return race
	}
	return n
}

// fewModels returns the crash models of p that a matrix checks with -race (only the one that loses every change that is
// not durable), or crashModels(p, subsets) without it.
func fewModels(p []simfs.Pending, subsets int) []simfs.Model {
	if raceEnabled && !longTests() {
		return []simfs.Model{loseAll}
	}
	return crashModels(p, subsets)
}

// tableKey is a key of a table.
type tableKey struct {
	table uint32
	key   string
}

// keyValue is a key of a table with its value.
type keyValue struct {
	tableKey
	value string
}

// update is what an Update of a scenario does, in this order: it puts keys, deletes keys, puts objects and appends
// events.
type update struct {
	puts    []keyValue
	deletes []tableKey
	objects [][]byte
	events  []string
}

func (u *update) run(tx *Tx) error {
	var errs []error
	for _, p := range u.puts {
		errs = append(errs, tx.Put(p.table, []byte(p.key), []byte(p.value)))
	}
	for _, d := range u.deletes {
		errs = append(errs, tx.Delete(d.table, []byte(d.key)))
	}
	for _, data := range u.objects {
		_, err := tx.PutObject(data)
		errs = append(errs, err)
	}
	for _, e := range u.events {
		errs = append(errs, tx.AppendEvent([]byte(e)))
	}
	return errors.Join(errs...)
}

type stepKind int

const (
	stepOpen stepKind = iota
	stepUpdate
	stepCheckpoint
	stepCompact // Maintenance.Compact with the rewrite of the step, on the store of the writer: no other process runs
	stepGC      // Maintenance.CollectGarbage, the same way
	stepClose
)

// step is a step of a scenario.
type step struct {
	name    string
	kind    stepKind
	update  *update
	rewrite Rewrite
}

// do runs s, which is not stepOpen, on db.
func (s *step) do(db *DB) error {
	ctx := context.Background()
	switch s.kind {
	case stepUpdate:
		return db.Update(ctx, s.update.run)
	case stepCheckpoint:
		return db.Checkpoint()
	case stepCompact:
		return (&Maintenance{db: db}).Compact(ctx, s.rewrite)
	case stepGC:
		_, err := (&Maintenance{db: db}).CollectGarbage(ctx)
		return err
	case stepClose:
		return db.Close()
	case stepOpen:
	}
	panic("logdb: do of an open step")
}

// result is how far a run of the steps of a scenario got: the first done steps returned nil, and the next one, if
// any, returned err.
type result struct {
	done int
	err  error
}

func (r result) String() string {
	return fmt.Sprintf("%d steps done, then %v", r.done, r.err)
}

// runSteps runs steps, which begin with stepOpen and end with stepClose, on the store in fsys, which it opens with o,
// until one fails, calling before, if not nil, before each. It closes a store that it has opened.
func runSteps(fsys vfs.FS, o Options, steps []step, before func(i int)) result {
	var db *DB
	defer func() {
		if db != nil {
			_ = db.Close()
		}
	}()
	for i := range steps {
		if before != nil {
			before(i)
		}
		s := &steps[i]
		var err error
		if s.kind == stepOpen {
			db, err = Open(fsys, o)
		} else {
			err = s.do(db)
		}
		if err != nil {
			return result{done: i, err: err}
		}
	}
	return result{done: len(steps)}
}

// model is what a store must have after steps: the generation and the sequence number of its log, its tables, its
// objects with their data, its events and the number of its next event.
type model struct {
	gen, seq  uint64
	tables    map[uint32]map[string]string
	objects   map[[32]byte][]byte
	events    []Event
	nextEvent uint64
	text      string // of String, once it has been called: a model is not changed once it is made
}

func newModel() *model {
	return &model{gen: 1, tables: map[uint32]map[string]string{}, objects: map[[32]byte][]byte{}, nextEvent: 1}
}

func (m *model) clone() *model {
	n := *m
	n.text = ""
	n.tables = make(map[uint32]map[string]string, len(m.tables))
	for id, keys := range m.tables {
		n.tables[id] = maps.Clone(keys)
	}
	n.objects = maps.Clone(m.objects)
	n.events = slices.Clone(m.events)
	return &n
}

// after returns the model after s.
func (m *model) after(s *step) *model {
	switch s.kind {
	case stepUpdate:
		return m.update(s.update)
	case stepCompact:
		return m.compact(s.rewrite)
	case stepOpen, stepCheckpoint, stepGC, stepClose:
	}
	return m
}

// update returns the model after u, which commits if it has an op, as the engine counts them: every put, a delete of
// a key that is there, an object that the store does not have, and every event.
func (m *model) update(u *update) *model {
	n := m.clone()
	ops := 0
	for _, p := range u.puts {
		if n.tables[p.table] == nil {
			n.tables[p.table] = map[string]string{}
		}
		n.tables[p.table][p.key] = p.value
		ops++
	}
	for _, d := range u.deletes {
		if _, ok := n.tables[d.table][d.key]; ok {
			delete(n.tables[d.table], d.key)
			if len(n.tables[d.table]) == 0 {
				delete(n.tables, d.table)
			}
			ops++
		}
	}
	for _, data := range u.objects {
		if h := sha256.Sum256(data); !hasKey(n.objects, h) {
			n.objects[h] = data
			ops++
		}
	}
	for _, e := range u.events {
		n.events = append(n.events, Event{Number: n.nextEvent, Seq: m.seq + 1, Data: []byte(e)})
		n.nextEvent++
		ops++
	}
	if ops > 0 {
		n.seq++
	}
	return n
}

func hasKey[K comparable, V any](m map[K]V, k K) bool {
	_, ok := m[k]
	return ok
}

// compact returns the model after a compaction with r: the next generation, with what r keeps.
func (m *model) compact(r Rewrite) *model {
	if m.seq == 0 {
		return m
	}
	n := m.clone()
	n.gen++
	for id, keys := range n.tables {
		for k, v := range keys {
			if r.Value == nil {
				break
			}
			if nv, keep := r.Value(id, []byte(k), []byte(v)); keep {
				keys[k] = string(nv)
			} else {
				delete(keys, k)
			}
		}
		if len(keys) == 0 {
			delete(n.tables, id)
		}
	}
	for h := range n.objects {
		if r.Object != nil && !r.Object(h) {
			delete(n.objects, h)
		}
	}
	n.events = nil
	for _, e := range m.events {
		if r.Event != nil {
			data, keep := r.Event(Event{Number: e.Number, Seq: e.Seq, Data: slices.Clone(e.Data)})
			if !keep {
				continue
			}
			e.Data = slices.Clone(data)
		}
		n.events = append(n.events, e)
	}
	return n
}

// String returns m as observe returns a store.
func (m *model) String() string {
	if m.text != "" {
		return m.text
	}
	var b strings.Builder
	fmt.Fprintf(&b, "generation %d, sequence %d, next event %d\n", m.gen, m.seq, m.nextEvent)
	for _, id := range slices.Sorted(maps.Keys(m.tables)) {
		keys := m.tables[id]
		for _, k := range slices.Sorted(maps.Keys(keys)) {
			fmt.Fprintf(&b, "%d %q=%s\n", id, k, valueText([]byte(keys[k])))
		}
	}
	for _, h := range slices.SortedFunc(maps.Keys(m.objects), func(a, b [32]byte) int { return bytes.Compare(a[:], b[:]) }) {
		fmt.Fprintf(&b, "object %x: %d bytes\n", h[:6], len(m.objects[h]))
	}
	for _, e := range m.events {
		fmt.Fprintf(&b, "event %d/%d=%q\n", e.Number, e.Seq, e.Data)
	}
	m.text = b.String()
	return m.text
}

// valueText returns v for a state: whole if it is short, else its size and a hash.
func valueText(v []byte) string {
	if len(v) <= 40 {
		return fmt.Sprintf("%q", v)
	}
	h := sha256.Sum256(v)
	return fmt.Sprintf("%d bytes %x", len(v), h[:6])
}

// observe returns the state of db as model.String does. It reads the data of every object, which must have its hash
// and its size.
func observe(db *DB) (string, error) {
	ctx := context.Background()
	var b strings.Builder
	err := db.View(ctx, func(tx *Tx) error {
		st := tx.snapshot()
		fmt.Fprintf(&b, "generation %d, sequence %d, next event %d\n", st.gen, tx.Seq(), st.nextEvent)
		for _, id := range slices.Sorted(maps.Keys(st.tables)) {
			tx.Scan(id, nil, func(k, v []byte) bool {
				fmt.Fprintf(&b, "%d %q=%s\n", id, k, valueText(v))
				return true
			})
		}
		var err error
		tx.Objects(func(h [32]byte, size int64) bool {
			var data []byte
			if data, err = tx.Object(h); err != nil {
				return false
			}
			if got := sha256.Sum256(data); got != h || int64(len(data)) != size {
				err = fmt.Errorf("object %x of %d bytes: %d bytes with the hash %x", h, size, len(data), got)
				return false
			}
			fmt.Fprintf(&b, "object %x: %d bytes\n", h[:6], size)
			return true
		})
		return err
	})
	if err != nil {
		return "", err
	}
	err = db.Events(ctx, func(e Event) bool {
		fmt.Fprintf(&b, "event %d/%d=%q\n", e.Number, e.Seq, e.Data)
		return true
	})
	return b.String(), err
}

// checkInvariants checks db, which Open has recovered after a crash or a fault: its checkpoint was not damaged, its
// state is that of one of want, the data of every object has its hash, and Verify passes. It returns the model that
// the state is that of, or what is wrong.
func checkInvariants(db *DB, want ...*model) (*model, error) {
	if info := db.Recovery(); strings.HasPrefix(info.Checkpoint, "damaged") {
		return nil, fmt.Errorf("the checkpoint is %s", info.Checkpoint)
	}
	m, err := checkState(db, want...)
	if err != nil {
		return nil, err
	}
	if err := db.Verify(context.Background()); err != nil {
		return nil, fmt.Errorf("Verify: %w", err)
	}
	return m, nil
}

// checkState returns the model of want whose state db has, or what is wrong.
func checkState(db *DB, want ...*model) (*model, error) {
	got, err := observe(db)
	if err != nil {
		return nil, fmt.Errorf("reading the state: %w", err)
	}
	for _, m := range want {
		if m.String() == got {
			return m, nil
		}
	}
	texts := make([]string, len(want))
	for i, m := range want {
		texts[i] = m.String()
	}
	return nil, fmt.Errorf("the state is\n%s\nwant one of these:\n%s", got, strings.Join(texts, "--\n"))
}

// probe is an Update that checkContinues makes after a crash.
var probe = &update{puts: []keyValue{setKey(9, "probe", "after the crash")}, events: []string{"probe"}}

// checkContinues makes the Update probe in db, which Open has recovered from crashed with o, to the state of m, and
// checks that a crash that then loses every change that is not durable has m with probe: what Open has changed, and
// the record of probe after it, are durable.
func checkContinues(db *DB, crashed *simfs.FS, o Options, m *model) error {
	if err := db.Update(context.Background(), probe.run); err != nil {
		return fmt.Errorf("an Update after the crash: %w", err)
	}
	again, err := Open(crashed.Crash(loseAll), o)
	if err != nil {
		return fmt.Errorf("Open after a crash after the Update: %w", err)
	}
	defer func() { _ = again.Close() }()
	if _, err := checkState(again, m.update(probe)); err != nil {
		return fmt.Errorf("after a crash after the Update: %w", err)
	}
	return nil
}

// checkReader opens a reader on crashed, a store after a crash that no writer has opened, which must have the state of
// m, as the writer recovers it.
func checkReader(crashed *simfs.FS, m *model) error {
	rd, err := OpenReader(crashed, Options{})
	if err != nil {
		return fmt.Errorf("OpenReader: %w", err)
	}
	defer func() { _ = rd.Close() }()
	if _, err := checkState(rd, m); err != nil {
		return fmt.Errorf("the reader: %w", err)
	}
	return nil
}

// tally counts what the recoveries of a matrix have found.
type tally map[string]int

func (c tally) add(info RecoveryInfo) {
	why, _, _ := strings.Cut(info.Checkpoint, ":")
	c["checkpoint "+why]++
	c[fmt.Sprintf("generation %d", info.Generation)]++
	if info.TruncatedAt >= 0 {
		c["a tail cut"]++
	}
	if info.RemovedSegment != "" {
		c["a segment removed"]++
	}
	if len(info.IgnoredGenerations) > 0 {
		c["a generation ignored"]++
	}
}

func (c tally) String() string {
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(c)) {
		parts = append(parts, fmt.Sprintf("%s %d", k, c[k]))
	}
	return strings.Join(parts, ", ")
}

// crashModels returns the crash models of simfs for the pending changes p, in which the writes of head, which a writer
// does not sync while it runs and which recovery does not need, form one group that a crash keeps or loses whole: each
// of them alone would multiply the models without reaching another state of the log. The names are those of
// simfs.Models for the groups.
func crashModels(p []simfs.Pending, maxSubsets int) []simfs.Model {
	var groups [][]int
	head := -1
	for i, u := range p {
		if u.Path != "head" {
			groups = append(groups, []int{i})
			continue
		}
		if head < 0 {
			head = len(groups)
			groups = append(groups, nil)
		}
		groups[head] = append(groups[head], i)
	}
	// In the order of their last changes, the last change of a prefix of groups is that of its last group, which the
	// torn models of simfs tear.
	slices.SortStableFunc(groups, func(a, b []int) int { return cmp.Compare(a[len(a)-1], b[len(b)-1]) })
	gp := make([]simfs.Pending, len(groups))
	for i, g := range groups {
		gp[i] = p[g[len(g)-1]]
	}
	models := simfs.Models(gp, maxSubsets, nil)
	for i, m := range models {
		if m.Keep == nil {
			continue
		}
		keep := make([]bool, len(p))
		for j, g := range groups {
			for _, u := range g {
				keep[u] = m.Keep[j]
			}
		}
		models[i].Keep = keep
	}
	return models
}

// keptText describes what the model m keeps of the pending changes p.
func keptText(p []simfs.Pending, m simfs.Model) string {
	var lines []string
	for i, u := range p {
		mark := "lost"
		if m.Keep != nil && m.Keep[i] {
			mark = "kept"
		}
		lines = append(lines, fmt.Sprintf("  %s %v", mark, u))
	}
	if m.TornLast > 0 || m.ZeroLast {
		lines = append(lines, fmt.Sprintf("  the last kept write: torn to %d bytes, zeros %v", m.TornLast, m.ZeroLast))
	}
	return strings.Join(lines, "\n")
}

// runPattern returns the -run pattern of the test t alone.
func runPattern(t *testing.T) string {
	parts := strings.Split(t.Name(), "/")
	for i, p := range parts {
		parts[i] = "^" + regexp.QuoteMeta(p) + "$"
	}
	return strings.Join(parts, "/")
}

// reproduce returns how to run the test t alone.
func reproduce(t *testing.T) string {
	race := ""
	if raceEnabled {
		race = " -race"
	}
	return fmt.Sprintf("go test%s -count=1 -run '%s' ./internal/logdb", race, runPattern(t))
}

// value returns a value of n bytes that starts with prefix.
func value(prefix string, n int) string {
	if n <= len(prefix) {
		return prefix[:n]
	}
	return prefix + strings.Repeat(string(rune('a'+len(prefix)%26)), n-len(prefix))
}

// scenario is a run of steps, from stepOpen to stepClose, on a store that setup has made and closed: the matrices crash
// or fail it after each of its operations.
type scenario struct {
	name   string
	setup  func(t *testing.T, o Options) (*simfs.FS, *model)
	steps  []step
	covers []string // what a run without a crash must do, as coverage counts it
	files  []string // and, in a placement other than Inline, these too
}

// coverage counts what a run has done: the hook points that it has reached, and, from ops, its operations, "segment"
// for each segment that it has made and "object file" for each file of an object.
func coverage(ops []simfs.Op, hooks []string) map[string]int {
	c := map[string]int{}
	for _, p := range hooks {
		c[p]++
	}
	for _, op := range ops {
		switch {
		case op.Kind == simfs.OpCreate && strings.HasPrefix(op.Path, "log/"):
			c["segment"]++
		case op.Kind == simfs.OpRename && strings.HasPrefix(op.Path, "objects/"):
			c["object file"]++
		}
	}
	return c
}

// cleanRun is what a run of a scenario without a crash or a fault has done.
type cleanRun struct {
	ops     []simfs.Op     // the counted operations, in their order: the operation n is at n-1
	starts  []int          // the number of counted operations before each step
	hooks   map[string]int // the number of counted operations before the last call of each hook point
	written map[int]bool   // the numbers of the operations that wrote the record of a commit
}

// recordWrite reports whether the counted operation n wrote the record of a commit, after which the hook point
// update:written comes.
func (c *cleanRun) recordWrite(n int) bool {
	return c.written[n]
}

// stepOf returns the index of the step that made the counted operation n.
func (c *cleanRun) stepOf(n int) int {
	i := 0
	for i+1 < len(c.starts) && c.starts[i+1] < n {
		i++
	}
	return i
}

// runClean runs sc on a copy of base with o, without a crash, checks that it ends with the state of want and does what
// sc covers, and returns what it has done.
func runClean(t *testing.T, sc *scenario, base *simfs.FS, o Options, want *model) *cleanRun {
	t.Helper()
	fsys := copyFS(base)
	c := &cleanRun{hooks: map[string]int{}, written: map[int]bool{}}
	var hooks []string
	o.Hook = func(p string) error {
		hooks = append(hooks, p)
		c.hooks[p] = fsys.Counted()
		if p == "update:written" {
			c.written[fsys.Counted()] = true
		}
		return nil
	}
	record := recordOps(fsys)
	if res := runSteps(fsys, o, sc.steps, func(int) { c.starts = append(c.starts, fsys.Counted()) }); res.done != len(sc.steps) {
		t.Fatalf("the run without a crash: %v", res)
	}
	ops := record()
	o.Hook = nil
	db := reopen(t, fsys, o)
	if _, err := checkInvariants(db, want); err != nil {
		t.Fatalf("after the run without a crash: %v", err)
	}
	closeDB(t, db)
	done := coverage(ops, hooks)
	covers := sc.covers
	if o.Placement != Inline {
		covers = slices.Concat(covers, sc.files, []string{"object file"})
	}
	for _, what := range covers {
		if done[what] == 0 {
			t.Fatalf("the run without a crash has no %s: %v", what, done)
		}
	}
	for _, op := range ops {
		if op.N > 0 {
			c.ops = append(c.ops, op)
		}
	}
	return c
}

// states returns the models of the store of sc after each prefix of its steps, from base, the model of its setup.
func (sc *scenario) states(base *model) []*model {
	states := []*model{base}
	for i := range sc.steps {
		states = append(states, states[i].after(&sc.steps[i]))
	}
	return states
}

// matrixOptions returns the options of the matrices for a placement: segments of 2048 bytes, and checkpoints that the
// Updates write before they return, once the log has grown by 1200 bytes.
func matrixOptions(p Placement) Options {
	return Options{Placement: p, Threshold: 100, SegmentBytes: 2048, CheckpointBytes: 1200, SyncCheckpoints: true}
}

// matrixPlacements are the placements of the matrices. The objects of the scenarios have sizes around the threshold
// of Hybrid, 100.
var matrixPlacements = []struct {
	name string
	p    Placement
}{{"inline", Inline}, {"files", Files}, {"hybrid", Hybrid}}

// newBase makes a store with o, makes the updates in it and closes it, and returns it with its model.
func newBase(t *testing.T, o Options, updates ...*update) (*simfs.FS, *model) {
	t.Helper()
	fsys := simfs.New(simDir)
	db, err := Create(fsys, o)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel()
	for _, u := range updates {
		if err := db.Update(context.Background(), u.run); err != nil {
			t.Fatal(err)
		}
		m = m.update(u)
	}
	closeDB(t, db)
	return fsys, m
}

func setKey(table uint32, key, value string) keyValue {
	return keyValue{tableKey{table, key}, value}
}

func openStep() step  { return step{name: "open", kind: stepOpen} }
func closeStep() step { return step{name: "close", kind: stepClose} }

func updateStep(name string, u *update) step {
	return step{name: name, kind: stepUpdate, update: u}
}

// matrixScenarios are the scenarios of the matrices.
func matrixScenarios() []scenario {
	return []scenario{commitsScenario(), compactionScenario(), tornTailScenario(), tornSegmentScenario()}
}

// commitsScenario has Updates with every kind of op, objects in a directory that an earlier Update has made, ones that
// the store has, an Update without ops, rollovers, a record of more than one page, a Checkpoint and an automatic
// checkpoint.
func commitsScenario() scenario {
	mates := sameDir(2, 150)
	for _, d := range [][]byte{objData(1, 80), objData(10, 60), objData(11, 150), objData(12, 99), objData(13, 100)} {
		if path.Dir(objName(sha256.Sum256(d))) == path.Dir(objName(sha256.Sum256(mates[0]))) {
			panic("logdb: the directory of the mates is not new") // the objects of the scenario would need a change
		}
	}
	return scenario{
		name: "commits",
		setup: func(t *testing.T, o Options) (*simfs.FS, *model) {
			return newBase(t, o,
				&update{puts: []keyValue{setKey(1, "k1", value("k1 ", 60)), setKey(1, "k2", "two"), setKey(1, "k3", value("k3 ", 30))},
					objects: [][]byte{objData(1, 80)}, events: []string{"base 1"}},
				&update{puts: []keyValue{setKey(2, "m", "m")}, events: []string{"base 2"}})
		},
		steps: []step{
			openStep(),
			updateStep("objects", &update{
				puts:    []keyValue{setKey(1, "a", value("a1 ", 120)), setKey(2, "b", "b1")},
				objects: [][]byte{objData(10, 60), objData(11, 150), objData(12, 99), objData(13, 100), mates[0]},
				events:  []string{"e1"}}),
			// Alone, so that no new directory makes objects/ synced: a failed Update may have made the directory.
			updateStep("same-directory", &update{puts: []keyValue{setKey(2, "m2", "m2")}, objects: [][]byte{mates[1]}}),
			updateStep("deletes", &update{
				puts:    []keyValue{setKey(1, "a", value("a2 ", 130))},
				deletes: []tableKey{{1, "k1"}, {1, "missing"}},
				objects: [][]byte{objData(10, 60), objData(1, 80), objData(14, 101)},
				events:  []string{"e2", ""}}),
			updateStep("no-ops", &update{deletes: []tableKey{{1, "missing"}}, objects: [][]byte{objData(10, 60)}}),
			updateStep("rollover", &update{
				puts:    []keyValue{setKey(1, "c", value("c ", 700)), setKey(3, "x", value("x ", 400))},
				objects: [][]byte{objData(15, 300)},
				events:  []string{"e4"}}),
			{name: "checkpoint", kind: stepCheckpoint},
			updateStep("after-checkpoint", &update{
				puts:    []keyValue{setKey(1, "d", value("d ", 300))},
				deletes: []tableKey{{2, "b"}, {2, "m"}},
				objects: [][]byte{objData(16, 400), objData(17, 40)},
				events:  []string{"e5"}}),
			updateStep("more", &update{
				puts:    []keyValue{setKey(1, "e", value("e ", 600))},
				objects: [][]byte{objData(18, 120)},
				events:  []string{"e6"}}),
			// A record of more than two pages, alone in a segment, and a checkpoint of more than one page.
			updateStep("large", &update{
				puts:    []keyValue{setKey(1, "large", value("large ", 5000))},
				objects: [][]byte{objData(19, 4500)},
				events:  []string{"e7"}}),
			updateStep("after-large", &update{puts: []keyValue{setKey(2, "f", "f")}, events: []string{"e8"}}),
			closeStep(),
		},
		covers: []string{"segment", "checkpoint:begin", "checkpoint:renamed"},
	}
}

// compactionRewrite is the rewrite of compactionScenario: it redacts a value, drops a table, drops two objects and
// two events, the last one among them, and changes the data of another.
var compactionRewrite = Rewrite{
	Value: func(table uint32, key, value []byte) ([]byte, bool) {
		if table == 1 && string(key) == "k2" {
			return []byte("redacted"), true
		}
		return value, table != 3
	},
	Event: func(e Event) ([]byte, bool) {
		if e.Number == 3 {
			return []byte("changed"), true
		}
		return e.Data, e.Number != 2 && e.Number != 4
	},
	Object: func(hash [32]byte) bool {
		return hash != sha256.Sum256(objData(20, 150)) && hash != sha256.Sum256(objData(21, 60))
	},
}

// compactionScenario compacts a store over an incomplete generation, which a compaction that crashed has left, puts
// again an object that the compaction dropped, rolls over in the new generation and collects the garbage.
func compactionScenario() scenario {
	return scenario{
		name: "compaction",
		setup: func(t *testing.T, o Options) (*simfs.FS, *model) {
			fsys, m := newBase(t, o,
				&update{
					puts: []keyValue{setKey(1, "k1", value("k1 ", 50)), setKey(1, "k2", value("k2 secret ", 50)),
						setKey(1, "k3", value("k3 ", 50)), setKey(3, "t1", "t"), setKey(3, "t2", "u")},
					objects: [][]byte{objData(20, 150), objData(21, 60), objData(22, 110)},
					events:  []string{"base 1", "base 2"}},
				&update{puts: []keyValue{setKey(1, "k4", value("k4 ", 80)), setKey(4, "big", value("big ", 1800))},
					objects: [][]byte{objData(23, 90)}, events: []string{"base 3"}})
			// A compaction in segments of 300 bytes that crashed after its base: the generation 2 that it has left is
			// incomplete.
			small := o
			small.SegmentBytes = 300
			db, err := Open(fsys, small)
			if err != nil {
				t.Fatal(err)
			}
			db.o.Hook = func(p string) error {
				if p == "compact:base" {
					fsys.CrashAt(fsys.Counted())
				}
				return nil
			}
			if err := compactDB(db, Rewrite{}); !errors.Is(err, simfs.ErrCrashed) {
				t.Fatalf("compact: %v", err)
			}
			_ = db.Close()
			fsys = fsys.Crash(loseAll)
			if gens := gensOf(t, fsys); !slices.Equal(gens, []uint64{1, 2}) || len(logNames(t, fsys)) < 3 {
				t.Fatalf("the crashed compaction has left %q", logNames(t, fsys))
			}
			db = reopen(t, fsys, o)
			if ignored := db.Recovery().IgnoredGenerations; !slices.Equal(ignored, []uint64{2}) {
				t.Fatalf("ignored generations %v", ignored)
			}
			closeDB(t, db)
			return fsys, m
		},
		steps: []step{
			openStep(),
			updateStep("before", &update{
				puts:    []keyValue{setKey(1, "k2", value("k2 secret again ", 70)), setKey(2, "z", "z")},
				objects: [][]byte{objData(24, 130)},
				events:  []string{"e1"}}),
			{name: "compact", kind: stepCompact, rewrite: compactionRewrite},
			updateStep("after", &update{
				puts:    []keyValue{setKey(1, "k5", value("k5 ", 90))},
				objects: [][]byte{objData(21, 60), objData(25, 200)},
				events:  []string{"e2"}}),
			updateStep("rollover", &update{
				puts:    []keyValue{setKey(1, "big", value("big ", 1500))},
				objects: [][]byte{objData(26, 500)},
				events:  []string{"e3"}}),
			{name: "gc", kind: stepGC},
			closeStep(),
		},
		covers: []string{"compact:base-end", "compact:delete", "segment"},
		files:  []string{"gc:remove"},
	}
}

// recoveryOf returns what Open recovers from a copy of fsys.
func recoveryOf(t *testing.T, fsys *simfs.FS, o Options) RecoveryInfo {
	t.Helper()
	db := reopen(t, copyFS(fsys), o)
	defer closeDB(t, db)
	return db.Recovery()
}

// tornTailScenario begins with a store whose last segment ends with a torn record, which Open cuts off, and whose
// first Update goes in a new segment: a cut that was not durable would come back in a segment that is not the last.
func tornTailScenario() scenario {
	return scenario{
		name: "torn-tail",
		setup: func(t *testing.T, o Options) (*simfs.FS, *model) {
			fsys, m := newBase(t, o, &update{puts: []keyValue{setKey(1, "a", value("a ", 100))},
				objects: [][]byte{objData(30, 120)}, events: []string{"base"}})
			name := logNames(t, fsys)[len(logNames(t, fsys))-1]
			h, _ := frames(t, fsys, name)
			torn := commitFrame(h.salt, m.seq+1, 1, "torn", value("torn ", 300))
			setFile(t, fsys, name, slices.Concat(fileData(t, fsys, name), torn[:len(torn)/2]))
			if info := recoveryOf(t, fsys, o); info.TruncatedAt < 0 {
				t.Fatalf("Open cuts nothing: %+v", info)
			}
			return fsys, m
		},
		steps: []step{
			openStep(),
			updateStep("rollover", &update{puts: []keyValue{setKey(1, "c", value("c ", 1800))}, events: []string{"e1"}}),
			updateStep("after", &update{puts: []keyValue{setKey(1, "b", value("b ", 200))},
				objects: [][]byte{objData(31, 150)}, events: []string{"e2"}}),
			closeStep(),
		},
		covers: []string{"segment"},
	}
}

// tornSegmentScenario begins with a store whose last segment is torn in its header, which Open removes, and whose
// first Update makes a segment with the same name again.
func tornSegmentScenario() scenario {
	return scenario{
		name: "torn-segment",
		setup: func(t *testing.T, o Options) (*simfs.FS, *model) {
			fsys, m := newBase(t, o, &update{puts: []keyValue{setKey(1, "a", value("a ", 1500))},
				objects: [][]byte{objData(32, 90)}, events: []string{"base"}})
			setFile(t, fsys, segName(1, uint64(len(logNames(t, fsys))+1)), make([]byte, 40))
			if info := recoveryOf(t, fsys, o); info.RemovedSegment == "" {
				t.Fatalf("Open removes nothing: %+v", info)
			}
			return fsys, m
		},
		steps: []step{
			openStep(),
			updateStep("rollover", &update{puts: []keyValue{setKey(1, "b", value("b ", 900))},
				objects: [][]byte{objData(33, 110)}, events: []string{"e1"}}),
			updateStep("after", &update{puts: []keyValue{setKey(2, "c", "c")}, events: []string{"e2"}}),
			closeStep(),
		},
		covers: []string{"segment"},
	}
}
