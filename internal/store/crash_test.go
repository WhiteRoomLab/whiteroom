package store

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// The matrices of the store (this file, kill_test.go and faultlog_test.go) run a scenario of originals and Updates on
// the log backend in each placement on simfs, and crash it, kill it or make one of its operations fail after each of
// its counted operations. What a store must then have is what the model (model_test.go) has after the same steps
// (matrixRefs), with the audit events of the Updates that it has, and every original that a PutOriginal that has
// returned nil, or a media document, names.

// matrixSize returns n, the size of a matrix, or fewer with -race, which makes every case several times slower, or
// long in the long mode.
func matrixSize(n, race, long int) int {
	switch {
	case longTests():
		return long
	case raceEnabled:
		return race
	}
	return n
}

// fewModels returns the crash models of p that a matrix checks with -race (only the one that loses every change that
// is not durable), or crashModels(p, subsets) without it.
func fewModels(p []simfs.Pending, subsets int) []simfs.Model {
	if raceEnabled && !longTests() {
		return []simfs.Model{loseAll}
	}
	return crashModels(p, subsets)
}

var loseAll = simfs.Model{Name: "lose-all"}

// keepAll returns the crash model that keeps every change of fsys that is not durable: the process is killed, or the
// machine restarts after its page cache has reached the disk.
func keepAll(fsys *simfs.FS) simfs.Model {
	keep := make([]bool, len(fsys.Pending()))
	for i := range keep {
		keep[i] = true
	}
	return simfs.Model{Name: "keep-all", Keep: keep}
}

// copyWorld returns a new world with what fsys has now, all of it durable.
func copyWorld(fsys *simfs.FS) *simfs.FS {
	return fsys.Crash(keepAll(fsys))
}

// crashModels returns the crash models of simfs for the pending changes p, in which the writes of logdb/head, which the
// engine does not sync while it runs and which recovery does not need, form one group that a crash keeps or loses
// whole, as in the matrices of internal/logdb. The names are those of simfs.Models for the groups.
func crashModels(p []simfs.Pending, maxSubsets int) []simfs.Model {
	var groups [][]int
	head := -1
	for i, u := range p {
		if u.Path != "logdb/head" {
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

// recordOps records the operations of fsys from now on, and returns a function that returns them.
func recordOps(fsys *simfs.FS) func() []simfs.Op {
	var mu sync.Mutex
	var ops []simfs.Op
	fsys.Inject(simfs.Fault{Err: errors.New("never"), Match: func(op simfs.Op) bool {
		mu.Lock()
		defer mu.Unlock()
		ops = append(ops, op)
		return false
	}})
	return func() []simfs.Op {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(ops)
	}
}

// mstepKind is the kind of a step of the scenario of the matrices.
type mstepKind int

const (
	mOpen     mstepKind = iota // open; create in the setup
	mClose                     // Close
	mOriginal                  // PutOriginal
	mUpdate                    // Update
)

// mstep is a step of the scenario of the matrices.
type mstep struct {
	name     string
	kind     mstepKind
	original []byte             // of mOriginal
	update   func(tx *Tx) error // of mUpdate
	event    string             // mUpdate: the data of the audit event that it appends on the log backend; "" for none
	clock    int                // mUpdate: its versions are saved at testTime and clock milliseconds
}

// matrixScenario is the scenario of the matrices: setup makes a store and closes it, the steps open it, put originals
// and make Updates, close it, open it again and close it, and the probe puts an original and a media document that
// names it in a store that has been opened after a crash. Every Update with an event changes a document.
type matrixScenario struct {
	setup, steps, probe []mstep
	originals           [][]byte // of the setup, the steps and the probe, in their order
	media               []string // the IDs of the media documents
	reads               diffReads
}

// matrixOriginals returns the originals of the scenario: the first two in one directory of files/, and the other two
// in another one.
func matrixOriginals() [][]byte {
	data := func(i int) []byte { return bytes.Repeat(fmt.Appendf(nil, "original %d of the matrices ", i), 8) }
	dir := func(b []byte) string { return strings.TrimPrefix(sum(b), "sha256:")[:2] }
	originals := [][]byte{data(0)}
	i := 1
	find := func(same bool, d string) {
		for ; ; i++ {
			if b := data(i); (dir(b) == d) == same {
				originals = append(originals, b)
				i++
				return
			}
		}
	}
	find(true, dir(originals[0]))
	find(false, dir(originals[0]))
	find(true, dir(originals[2]))
	return originals
}

// mediaJSON returns the JSON of the media document with id of the original data.
func mediaJSON(id string, data []byte) []byte {
	return fmt.Appendf(nil, `{"schemaVersion": 1, "id": "%s", "hash": "%s", "size": %d}`, id, sum(data), len(data))
}

// putDoc returns an update that puts data as the document of kind with id.
func putDoc(kind content.RefKind, id string, data []byte) func(tx *Tx) error {
	return func(tx *Tx) error {
		_, err := tx.Put(kind, id, data, userID)
		return err
	}
}

// updates returns an update that makes each of us in their order.
func updates(us ...func(tx *Tx) error) func(tx *Tx) error {
	return func(tx *Tx) error {
		for _, u := range us {
			if err := u(tx); err != nil {
				return err
			}
		}
		return nil
	}
}

// theScenario is the scenario of the matrices.
var theScenario = sync.OnceValue(func() *matrixScenario {
	o := matrixOriginals()
	media := []string{mediaID, "med_01JE0000000000000000000001", "med_01JE0000000000000000000002"}
	deleted := "trm_01JE0000000000000000000001"
	rev2 := "rev_01JE0000000000000000000001"
	entry2 := []byte(`{"schemaVersion": 1, "id": "` + entryID + `", "type": "news"}`)
	pairUpdate := func(n int) func(tx *Tx) error { return func(tx *Tx) error { return putPair(tx, n) } }
	addRevision := func(id string, data []byte) func(tx *Tx) error {
		return func(tx *Tx) error {
			_, err := tx.AddRevision(entryID, id, data, userID)
			return err
		}
	}
	sc := &matrixScenario{
		setup: []mstep{
			{name: "create", kind: mOpen},
			{name: "setup", kind: mUpdate, event: "setup", update: updates(pairUpdate(1),
				putDoc(content.RefTerm, deleted, docJSON(content.RefTerm, deleted)))},
			{name: "original 0", kind: mOriginal, original: o[0]},
			{name: "setup media", kind: mUpdate, event: "setup media",
				update: putDoc(content.RefMedia, media[0], mediaJSON(media[0], o[0]))},
			{name: "close", kind: mClose},
		},
		steps: []mstep{
			{name: "open", kind: mOpen},
			// In the directory of the original of the setup, which this process has not synced.
			{name: "original 1", kind: mOriginal, original: o[1]},
			{name: "media", kind: mUpdate, event: "media",
				update: putDoc(content.RefMedia, media[1], mediaJSON(media[1], o[1]))},
			{name: "same", kind: mUpdate, update: putDoc(content.RefMedia, media[1], mediaJSON(media[1], o[1]))},
			// In a new directory.
			{name: "original 2", kind: mOriginal, original: o[2]},
			{name: "entry", kind: mUpdate, event: "entry", update: updates(
				putDoc(content.RefEntry, entryID, docJSON(content.RefEntry, entryID)),
				addRevision(revisionID, revJSON("first")),
				putDoc(content.RefMedia, media[1], mediaJSON(media[1], o[2])))},
			{name: "pair", kind: mUpdate, event: "pair", update: updates(func(tx *Tx) error {
				_, err := tx.Delete(content.RefTerm, deleted, userID)
				return err
			}, pairUpdate(2))},
			{name: "close", kind: mClose},
			{name: "open again", kind: mOpen},
			{name: "after open", kind: mUpdate, event: "after open", update: updates(pairUpdate(3),
				putDoc(content.RefEntry, entryID, entry2), addRevision(rev2, revJSON("second")))},
			{name: "close again", kind: mClose},
		},
		probe: []mstep{
			// In the directory of the original of a step, which a killed process may have made without a durable name.
			{name: "original 3", kind: mOriginal, original: o[3]},
			{name: "probe", kind: mUpdate, event: "probe", update: updates(
				putDoc(content.RefMedia, media[2], mediaJSON(media[2], o[3])), pairUpdate(4))},
		},
		originals: o,
		media:     media,
	}
	clock := 0
	for _, steps := range [][]mstep{sc.setup, sc.steps, sc.probe} {
		for i := range steps {
			clock++
			steps[i].clock = clock
		}
	}
	r := &sc.reads
	r.kinds = []content.RefKind{content.RefEntry, content.RefTerm, content.RefMedia}
	doc := func(kind content.RefKind, id string) diffDoc { return diffDoc{diffKind{kind: kind}, id} }
	for _, id := range media {
		r.docs = append(r.docs, doc(content.RefMedia, id))
	}
	r.docs = append(r.docs, doc(content.RefEntry, entryID), doc(content.RefTerm, pair[0]),
		doc(content.RefTerm, pair[1]), doc(content.RefTerm, deleted))
	r.revs = []string{revisionID, rev2}
	r.entries = []string{entryID}
	var data [][]byte
	for i, id := range media {
		data = append(data, mediaJSON(id, o[i]))
	}
	data = append(data, mediaJSON(media[1], o[2]), docJSON(content.RefEntry, entryID), entry2, revJSON("first"),
		revJSON("second"), docJSON(content.RefTerm, deleted), []byte("missing"))
	for n := 1; n <= 4; n++ {
		for _, id := range pair {
			data = append(data, []byte(`{"schemaVersion": 1, "id": "`+id+`", "name": "`+fmt.Sprint(n)+`"}`))
		}
	}
	for _, d := range data {
		r.hashes = append(r.hashes, sum(d))
	}
	return sc
})

// eventsAt returns the audit events of the store after the setup and the first j steps.
func (sc *matrixScenario) eventsAt(j int) []string {
	var events []string
	for _, st := range slices.Concat(sc.setup, sc.steps[:j]) {
		if st.kind == mUpdate && st.event != "" {
			events = append(events, st.event)
		}
	}
	return events
}

// ackedAt returns which originals of the scenario PutOriginal has put after the setup and the first done steps.
func (sc *matrixScenario) ackedAt(done int) []bool {
	acked := make([]bool, len(sc.originals))
	for _, st := range slices.Concat(sc.setup, sc.steps[:done]) {
		if st.kind == mOriginal {
			acked[slices.IndexFunc(sc.originals, func(o []byte) bool { return bytes.Equal(o, st.original) })] = true
		}
	}
	return acked
}

// matrixRefs are the reads of the scenario in the model: states[j] after the setup and the first j steps, probed[j]
// after them and the probe.
type matrixRefs struct {
	states, probed []string
}

// theRefs returns the reads of the scenario in the model.
var theRefs = sync.OnceValues(func() (*matrixRefs, error) {
	sc := theScenario()
	refs := &matrixRefs{}
	for j := range len(sc.steps) + 1 {
		s := newModelStore()
		r := &mrun{s: s}
		for _, st := range slices.Concat(sc.setup, sc.steps[:j]) {
			if st.kind == mOriginal || st.kind == mUpdate {
				if err := r.do(context.Background(), &st); err != nil {
					return nil, errors.Join(fmt.Errorf("%s in the model: %w", st.name, err), s.Close())
				}
			}
		}
		text, _, err := viewState(s)
		if err != nil {
			return nil, errors.Join(err, s.Close())
		}
		refs.states = append(refs.states, text)
		for _, st := range sc.probe {
			if err := r.do(context.Background(), &st); err != nil {
				return nil, errors.Join(fmt.Errorf("%s in the model: %w", st.name, err), s.Close())
			}
		}
		if text, _, err = viewState(s); err != nil {
			return nil, errors.Join(err, s.Close())
		}
		refs.probed = append(refs.probed, text)
		if err := s.Close(); err != nil {
			return nil, err
		}
	}
	return refs, nil
})

// viewState returns, from one View of s, the text of the reads of the scenario, and the hashes of the originals that
// the versions of its media documents name.
func viewState(s *Store) (text string, named []string, err error) {
	sc := theScenario()
	err = s.View(context.Background(), func(tx *Tx) error {
		text = readsText(tx, sc.reads)
		for _, id := range sc.media {
			versions, err := tx.History(content.RefMedia, id)
			if err != nil {
				return err
			}
			for _, v := range versions {
				if v.Hash == "" {
					continue
				}
				data, err := tx.Object(v.Hash)
				if err != nil {
					return err
				}
				var m content.Media
				if err := m.UnmarshalJSON(data); err != nil {
					return err
				}
				named = append(named, m.Hash)
			}
		}
		return nil
	})
	return text, named, err
}

// readsText returns what the reads r give in tx, a line for each: the versions and the revisions, with the sizes of
// their data, which the store checks against their hashes, or the error, with whether it wraps fs.ErrNotExist.
func readsText(tx *Tx, r diffReads) string {
	var b []byte
	end := func(err error) {
		if err != nil {
			b = append(b, " error "...)
			b = append(b, err.Error()...)
			if errors.Is(err, fs.ErrNotExist) {
				b = append(b, " (not exist)"...)
			}
		}
		b = append(b, '\n')
	}
	size := func(data []byte) {
		b = append(b, ", "...)
		b = strconv.AppendInt(b, int64(len(data)), 10)
		b = append(b, " bytes"...)
	}
	for _, kind := range r.kinds {
		docs, err := tx.Documents(kind)
		b = append(b, "Documents "+string(kind)...)
		for _, d := range docs {
			b = textVersion(append(b, "; "...), d.Version)
			size(d.Data)
		}
		end(err)
	}
	for _, d := range r.docs {
		doc, err := tx.Document(d.k.kind, d.id)
		b = append(b, "Document "+d.id...)
		if err == nil {
			b = textVersion(append(b, ": "...), doc.Version)
			size(doc.Data)
		}
		end(err)
		versions, err := tx.History(d.k.kind, d.id)
		b = append(b, "History "+d.id...)
		for _, v := range versions {
			b = textVersion(append(b, "; "...), v)
		}
		end(err)
	}
	revision := func(rev Revision) {
		b = strconv.AppendInt(b, rev.Seq, 10)
		b = append(b, " "+rev.ID+" of "+rev.Entry+" "+rev.Hash+" "+rev.SavedBy+" "...)
		b = rev.SavedAt.AppendFormat(b, time.RFC3339Nano)
	}
	for _, id := range r.revs {
		rev, err := tx.Revision(id)
		b = append(b, "Revision "+id...)
		if err == nil {
			revision(rev)
			size(rev.Data)
		}
		end(err)
	}
	for _, entry := range r.entries {
		revs, err := tx.Revisions(entry)
		b = append(b, "Revisions "+entry...)
		for _, rev := range revs {
			b = append(b, "; "...)
			revision(rev)
		}
		end(err)
	}
	for _, h := range r.hashes {
		ok, err := hasObject(tx, h)
		b = append(b, "Object "+h...)
		if err == nil && !ok {
			b = append(b, " missing"...)
		}
		end(err)
	}
	return string(b)
}

// hasObject reports whether tx has the object hash: on the log backend, whether its engine has it, without reading the
// data, which Verify of checkStore reads and checks against its hash; in the model, by reading it.
func hasObject(tx *Tx, hash string) (bool, error) {
	if lt, ok := tx.b.(*logTx); ok {
		d, err := digest(hash)
		return err == nil && lt.tx.HasObject(d), err
	}
	_, err := tx.Object(hash)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// textVersion appends v to b.
func textVersion(b []byte, v Version) []byte {
	b = strconv.AppendInt(b, v.Seq, 10)
	b = append(b, " "+string(v.Kind)+" "+v.ID+" "+v.Hash+" "+v.SavedBy+" "...)
	return v.SavedAt.AppendFormat(b, time.RFC3339Nano)
}

// mrun runs steps of the scenario on a store.
type mrun struct {
	open func() (*Store, error) // of mOpen
	log  bool                   // the store is of the log backend: its Updates append their events
	hook func(point string) error
	s    *Store // nil while closed
}

// do runs st on the store of r.
func (r *mrun) do(ctx context.Context, st *mstep) error {
	switch st.kind {
	case mOpen:
		s, err := r.open()
		if err != nil {
			return err
		}
		s.hook = r.hook
		r.s = s
		return nil
	case mClose:
		err := r.s.Close()
		r.s = nil // closed, whatever Close has returned
		return err
	case mOriginal:
		_, err := r.s.PutOriginal(ctx, bytes.NewReader(st.original))
		return err
	case mUpdate:
		at := testTime.Add(time.Duration(st.clock) * time.Millisecond)
		r.s.now = func() time.Time { return at }
		return r.s.Update(ctx, func(tx *Tx) error {
			if err := st.update(tx); err != nil {
				return err
			}
			if r.log && st.event != "" {
				return tx.appendEvent([]byte(st.event))
			}
			return nil
		})
	}
	panic(fmt.Sprintf("store: a step of the kind %d", st.kind))
}

// close closes the store of r, if it is open.
func (r *mrun) close() {
	if r.s != nil {
		_ = r.s.Close()
		r.s = nil
	}
}

// result is how far a run of steps got: the first done steps returned nil, and the next one, if any, returned err.
type result struct {
	done int
	err  error
}

func (r result) String() string {
	return fmt.Sprintf("%d steps done, then %v", r.done, r.err)
}

// run runs steps on r until one fails, calling before, if not nil, before each.
func (r *mrun) run(steps []mstep, before func(i int)) result {
	for i := range steps {
		if before != nil {
			before(i)
		}
		if err := r.do(context.Background(), &steps[i]); err != nil {
			return result{done: i, err: err}
		}
	}
	return result{done: len(steps)}
}

// matrixOptions returns the options of the matrices for the log backend tb: segments of 2048 bytes, and checkpoints
// that the Updates write before they return, once the log has grown by 2048 bytes.
func matrixOptions(tb testBackend) logdb.Options {
	o := *tb.log
	o.SegmentBytes, o.CheckpointBytes = 2048, 2048
	return o
}

// newMatrixBase makes the store of the setup of the scenario with o in a new world, all of it durable.
func newMatrixBase(t *testing.T, o logdb.Options) *simfs.FS {
	t.Helper()
	sc := theScenario()
	fsys := simfs.New("data")
	r := &mrun{log: true, open: func() (*Store, error) { return create("data", options{log: &o, fs: fsys}) }}
	defer r.close()
	if res := r.run(sc.setup, nil); res.done != len(sc.setup) {
		t.Fatalf("the setup: %v", res)
	}
	return copyWorld(fsys)
}

// writer returns a run of steps of the log backend with o in the data directory fsys.
func writer(fsys vfs.FS, o logdb.Options) *mrun {
	return &mrun{log: true, open: func() (*Store, error) { return open("data", options{log: &o, fs: fsys}) }}
}

// openReader opens a reader of the store with o in the data directory fsys.
func openReader(fsys vfs.FS, o logdb.Options) (*Store, error) {
	return openLogReader("data", options{log: &o, fs: fsys})
}

// matrixState is what a store of the scenario has: the lines of the reads of the scenario, its audit events, and which
// originals of the scenario it has, whole.
type matrixState struct {
	reads     string
	events    []string
	originals []bool
}

// observe returns the state of s. Every original that it has must be whole, and so must that of every version of every
// media document.
func observe(t *testing.T, s *Store) (matrixState, error) {
	t.Helper()
	sc := theScenario()
	var st matrixState
	reads, named, err := viewState(s)
	if err != nil {
		return st, err
	}
	st.reads = reads
	var number uint64
	if err := s.events(t.Context(), func(n uint64, data []byte) bool {
		if number++; n != number {
			st.events = append(st.events, fmt.Sprintf("(the event %d after %d)", n, number-1))
			return false
		}
		st.events = append(st.events, string(data))
		return true
	}); err != nil {
		return st, fmt.Errorf("events: %w", err)
	}
	for _, data := range sc.originals {
		ok, err := hasOriginal(s, data)
		if err != nil {
			return st, err
		}
		st.originals = append(st.originals, ok)
	}
	for _, hash := range named {
		i := slices.IndexFunc(sc.originals, func(o []byte) bool { return sum(o) == hash })
		if i < 0 || !st.originals[i] {
			return st, fmt.Errorf("a media document names the original %s, which the store does not have", hash)
		}
	}
	return st, nil
}

// hasOriginal reports whether s has the original data, whole: an original that is there with other data, or that
// cannot be read, is an error.
func hasOriginal(s *Store, data []byte) (bool, error) {
	f, err := s.OpenOriginal(sum(data))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	got, err := io.ReadAll(f)
	if err := errors.Join(err, f.Close()); err != nil {
		return false, err
	}
	if !bytes.Equal(got, data) {
		return false, fmt.Errorf("the original %s has %d other bytes", sum(data), len(got))
	}
	return true, nil
}

// wanted is a state that a store must have: the reads of refs, the events of the scenario, and the originals of acked.
type wanted struct {
	reads    string
	events   []string
	acked    []bool // the originals that it must have; it may have the others or not
	at       string // which state it is, in messages
	stateRef int    // j of matrixRefs.states, or -1
}

// stateAt returns the state after the setup and the first j steps, of which the first done have returned nil.
func stateAt(refs *matrixRefs, j, done int) wanted {
	sc := theScenario()
	return wanted{reads: refs.states[j], events: sc.eventsAt(j), acked: sc.ackedAt(done),
		at: fmt.Sprintf("the state after %d steps", j), stateRef: j}
}

// probedAt returns the state after the setup, the first j steps, of which the first done have returned nil, and the
// probe.
func probedAt(refs *matrixRefs, j, done int) wanted {
	sc := theScenario()
	acked := sc.ackedAt(done)
	acked[len(acked)-1] = true
	return wanted{reads: refs.probed[j], events: append(sc.eventsAt(j), sc.probe[len(sc.probe)-1].event), acked: acked,
		at: fmt.Sprintf("the state after %d steps and the probe", j), stateRef: -1}
}

// matches reports whether st is the state w.
func (w wanted) matches(st matrixState) bool {
	if st.reads != w.reads || !slices.Equal(st.events, w.events) {
		return false
	}
	for i, ok := range w.acked {
		if ok && !st.originals[i] {
			return false
		}
	}
	return true
}

// checkState returns the state of want that s has, and checks it as checkStore does, or returns what is wrong.
func checkState(t *testing.T, s *Store, want ...wanted) (wanted, matrixState, error) {
	t.Helper()
	st, err := observe(t, s)
	if err != nil {
		return wanted{}, st, fmt.Errorf("reading the state: %w", err)
	}
	for _, w := range want {
		if w.matches(st) {
			return w, st, checkStore(t, s)
		}
	}
	return wanted{}, st, fmt.Errorf("the state is not one of %s", describeWanted(st, want))
}

// describeWanted describes how st differs from each of want.
func describeWanted(st matrixState, want []wanted) string {
	var b strings.Builder
	for i, w := range want {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(w.at)
	}
	fmt.Fprintf(&b, "\nthe store has the events %q and the originals %v of the scenario", st.events, st.originals)
	for _, w := range want {
		fmt.Fprintf(&b, "\nagainst %s:", w.at)
		if !slices.Equal(st.events, w.events) {
			fmt.Fprintf(&b, "\n  the events are not %q", w.events)
		}
		for i, ok := range w.acked {
			if ok && !st.originals[i] {
				fmt.Fprintf(&b, "\n  the original %d is missing: %s", i, sum(theScenario().originals[i]))
			}
		}
		got, ref := strings.Split(st.reads, "\n"), strings.Split(w.reads, "\n")
		for i := range max(len(got), len(ref)) {
			if g, r := line(got, i), line(ref, i); g != r {
				fmt.Fprintf(&b, "\n  line %d of the reads is\n    %s\n  want\n    %s", i+1, g, r)
				break
			}
		}
	}
	return b.String()
}

// checkStore checks what the tests look past the API at in s, a store of the log backend: its checkpoint was not
// damaged, Verify of the engine passes (the state is that of the log, and every object has the data of its hash), and
// the current version of every document is its latest one.
func checkStore(t *testing.T, s *Store) error {
	t.Helper()
	d := logDB(t, s)
	if info := d.Recovery(); strings.HasPrefix(info.Checkpoint, "damaged") {
		return fmt.Errorf("the checkpoint is %s", info.Checkpoint)
	}
	if err := d.Verify(t.Context()); err != nil {
		return fmt.Errorf("Verify: %w", err)
	}
	switch n, err := inconsistentLog(t, s); {
	case err != nil:
		return err
	case n != 0:
		return fmt.Errorf("%d documents are not at their latest versions", n)
	}
	return nil
}

// matrixClean is what a run of the steps of the scenario without a crash or a fault has done.
type matrixClean struct {
	ops    []simfs.Op     // the counted operations, in their order: the operation n is at n-1
	starts []int          // the number of counted operations before each step
	hooks  map[string]int // the number of counted operations before the last call of each hook point
}

// stepOf returns the index of the step that made the counted operation n.
func (c *matrixClean) stepOf(n int) int {
	i := 0
	for i+1 < len(c.starts) && c.starts[i+1] < n {
		i++
	}
	return i
}

// runMatrixClean runs the steps of the scenario on a copy of base with o, without a crash, checks that the store ends
// with the last state of refs and that the run does what the matrices need, and returns what it has done.
func runMatrixClean(t *testing.T, base *simfs.FS, o logdb.Options, refs *matrixRefs) *matrixClean {
	t.Helper()
	sc := theScenario()
	fsys := copyWorld(base)
	c := &matrixClean{hooks: map[string]int{}}
	r := writer(fsys, o)
	r.hook = func(p string) error {
		c.hooks[p] = fsys.Counted()
		return nil
	}
	record := recordOps(fsys)
	res := r.run(sc.steps, func(int) { c.starts = append(c.starts, fsys.Counted()) })
	r.close()
	if res.done != len(sc.steps) {
		t.Fatalf("the run without a crash: %v", res)
	}
	for _, op := range record() {
		if op.N > 0 {
			c.ops = append(c.ops, op)
		}
	}
	s, err := open("data", options{log: &o, fs: copyWorld(fsys)})
	if err != nil {
		t.Fatalf("open() after the run without a crash: %v", err)
	}
	defer func() { _ = s.Close() }()
	if _, _, err := checkState(t, s, stateAt(refs, len(sc.steps), len(sc.steps))); err != nil {
		t.Fatalf("after the run without a crash: %v", err)
	}
	done := map[string]int{}
	for _, op := range c.ops {
		switch {
		case op.Kind == simfs.OpCreate && strings.HasPrefix(op.Path, "logdb/log/"):
			done["new segment"]++
		case op.Kind == simfs.OpRename && strings.HasPrefix(op.Path, "logdb/objects/"):
			done["object file"]++
		case op.Kind == simfs.OpMkdir && strings.HasPrefix(op.Path, "files/"):
			done["Mkdir in files/"]++
		case op.Kind == simfs.OpRename && strings.HasPrefix(op.Path, "files/"):
			done["original"]++
		}
	}
	if _, ok := c.hooks["logdb:checkpoint:begin"]; ok {
		done["automatic checkpoint"]++
	}
	covers := []string{"new segment", "automatic checkpoint"}
	if o.Placement != logdb.Inline {
		covers = append(covers, "object file")
	}
	for _, what := range covers {
		if done[what] == 0 {
			t.Fatalf("the run without a crash has no %s: %v", what, done)
		}
	}
	if done["Mkdir in files/"] != 2 || done["original"] != 2 {
		t.Fatalf("the run without a crash has %v, want two originals, each with a Mkdir", done)
	}
	return c
}

// tally counts what the recoveries of a matrix have found.
type tally map[string]int

func (c tally) add(info logdb.RecoveryInfo) {
	why, _, _ := strings.Cut(info.Checkpoint, ":")
	c["checkpoint "+why]++
	if info.Replayed > 0 {
		c["records replayed"]++
	}
	if info.TruncatedAt >= 0 {
		c["a tail cut"]++
	}
	if info.RemovedSegment != "" {
		c["a segment removed"]++
	}
}

func (c tally) String() string {
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(c)) {
		parts = append(parts, fmt.Sprintf("%s %d", k, c[k]))
	}
	return strings.Join(parts, ", ")
}

// checkProbe makes the probe of the scenario in s, a store in fsys with o that has opened after a crash in the state w
// of a run whose first done steps returned nil, and checks that a crash that then loses every change that is not
// durable has w with the probe: what open has changed, the original of the probe and the record that names it are
// durable.
func checkProbe(t *testing.T, s *Store, fsys *simfs.FS, o logdb.Options, refs *matrixRefs, w wanted, done int) error {
	t.Helper()
	sc := theScenario()
	r := &mrun{log: true, s: s}
	if res := r.run(sc.probe, nil); res.done != len(sc.probe) {
		return fmt.Errorf("the probe after the crash: %v", res)
	}
	again, err := open("data", options{log: &o, fs: fsys.Crash(loseAll)})
	if err != nil {
		return fmt.Errorf("open() after a crash after the probe: %w", err)
	}
	defer func() { _ = again.Close() }()
	if _, _, err := checkState(t, again, probedAt(refs, w.stateRef, done)); err != nil {
		return fmt.Errorf("after a crash after the probe: %w", err)
	}
	return nil
}

// TestStoreCrashMatrix runs the steps of the scenario of the matrices on the log backend in each placement, and
// crashes it after each of their counted operations; then, for each crash that simfs can make of what was not durable
// then (crashModels with 16 subsets, 4 with -race, 256 in the long mode: the writes of logdb/head in one group), the
// store opens with its checkpoint and has what the model has after the steps that returned nil and the step that was
// running, whole or not at all, with the audit events of its Updates and every original that a PutOriginal that
// returned nil has put or that a media document names, and passes checkStore. A reader that opens the crash first has
// the same state, and the probe of the scenario is durable without Close (with -race, these two only after lose-all
// and keep-all). A case is TestStoreCrashMatrix/<backend>/k=<crash point>/<model>.
func TestStoreCrashMatrix(t *testing.T) {
	t.Parallel() // with the other matrices, after the tests that do not run in parallel
	refs, err := theRefs()
	if err != nil {
		t.Fatal(err)
	}
	sc := theScenario()
	subsets := matrixSize(16, 4, 256)
	var mu sync.Mutex // the backends run in parallel: each has worlds of its own
	points, cases := 0, 0
	found := tally{}
	t.Cleanup(func() {
		t.Logf("%d crash points, %d crashes (subsets of at most %d); recovered: %v", points, cases, subsets, found)
	})
	forBackends(t, logBackends, func(t *testing.T, tb testBackend) {
		t.Parallel()
		o := matrixOptions(tb)
		base := newMatrixBase(t, o)
		ops := runMatrixClean(t, base, o, refs).ops
		tbCases, tbFound := 0, tally{}
		t.Cleanup(func() {
			mu.Lock()
			defer mu.Unlock()
			points += len(ops) + 1
			cases += tbCases
			for k, n := range tbFound {
				found[k] += n
			}
			t.Logf("%d operations, %d crash points, %d crashes; recovered: %v", len(ops), len(ops)+1, tbCases, tbFound)
		})
		for k := range len(ops) + 1 {
			t.Run(fmt.Sprintf("k=%d", k), func(t *testing.T) {
				t.Parallel()
				fsys := copyWorld(base)
				fsys.CrashAt(k)
				r := writer(fsys, o)
				res := r.run(sc.steps, nil)
				r.close()
				want := []wanted{stateAt(refs, res.done, res.done)}
				running := "none"
				if res.done < len(sc.steps) {
					want = append(want, stateAt(refs, res.done+1, res.done))
					running = fmt.Sprintf("%s, which returned %v", sc.steps[res.done].name, res.err)
				}
				pending := fsys.Pending()
				models := crashModels(pending, subsets)
				mu.Lock()
				tbCases += len(models)
				mu.Unlock()
				for _, cm := range models {
					t.Run(cm.Name, func(t *testing.T) {
						fail := func(format string, args ...any) {
							t.Helper()
							t.Fatalf("%s\na crash %s; %d steps returned nil; the step running: %s\nthe model %s:\n"+
								"%s\nto run it alone: %s", fmt.Sprintf(format, args...), pointText(ops, k), res.done,
								running, cm.Name, keptText(pending, cm), reproduce(t))
						}
						crashed := fsys.Crash(cm)
						s, err := open("data", options{log: &o, fs: crashed})
						if err != nil {
							fail("open(): %v", err)
						}
						defer func() { _ = s.Close() }()
						info := logDB(t, s).Recovery()
						mu.Lock()
						tbFound.add(info)
						mu.Unlock()
						// The setup leaves a checkpoint, which only a rename replaces.
						if info.Checkpoint != "used" {
							fail("the checkpoint is %q, want used\nrecovery: %+v", info.Checkpoint, info)
						}
						w, st, err := checkState(t, s, want...)
						if err != nil {
							fail("%v\nrecovery: %+v", err, info)
						}
						if raceEnabled && !longTests() && cm.Name != "lose-all" && cm.Name != "keep-all" {
							return
						}
						// The writer has changed crashed; the reader opens a crash of its own.
						rd, err := openReader(fsys.Crash(cm), o)
						if err != nil {
							fail("openLogReader(): %v", err)
						}
						rst, err := observe(t, rd)
						_ = rd.Close()
						switch {
						case err != nil:
							fail("the reader: %v", err)
						case rst.reads != st.reads || !slices.Equal(rst.events, st.events) ||
							!slices.Equal(rst.originals, st.originals):
							fail("the reader has another state than the writer, %s:\n%s", w.at,
								describeWanted(rst, []wanted{w}))
						}
						if err := checkProbe(t, s, crashed, o, refs, w, res.done); err != nil {
							fail("%v", err)
						}
					})
				}
			})
		}
	})
}
