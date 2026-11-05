package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
	"github.com/WhiteRoomLab/whiteroom/internal/sitedir"
)

// longTests reports whether WHITEROOM_LONG_TESTS asks for the long versions of the tests.
func longTests() bool {
	return os.Getenv("WHITEROOM_LONG_TESTS") == "1"
}

// reproduce returns the command that runs the test t alone.
func reproduce(t *testing.T) string {
	parts := strings.Split(t.Name(), "/")
	for i, p := range parts {
		parts[i] = "^" + regexp.QuoteMeta(p) + "$"
	}
	env, race := "", ""
	if longTests() {
		env = "WHITEROOM_LONG_TESTS=1 "
	}
	if raceEnabled {
		race = " -race"
	}
	return fmt.Sprintf("%sgo test%s -count=1 -run '%s' ./internal/store", env, race, strings.Join(parts, "/"))
}

// errAbort is the error with which the function of an Update of the generator ends to roll it back.
var errAbort = errors.New("abort")

// externalPackages are those of the errors of the OS and the engine of internal/logdb, which have messages of their
// own; externalSentinels are those of their errors that are values.
var (
	externalPackages = []string{
		"os", "io/fs", "syscall", "internal/poll", "github.com/WhiteRoomLab/whiteroom/internal/logdb",
		"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs",
		"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs",
	}
	externalSentinels = []error{
		logdb.ErrFailed, logdb.ErrReadOnly, vfs.ErrLocked, simfs.ErrCrashed, context.Canceled, context.DeadlineExceeded,
		io.EOF, io.ErrUnexpectedEOF, fs.ErrClosed, fs.ErrPermission, fs.ErrInvalid,
	}
)

// externalError reports whether err has an error of the OS or the engine in its tree.
func externalError(err error) bool {
	return inTree(err, func(e error) bool {
		t := reflect.TypeOf(e)
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		// The sentinels are comparable, so == cannot panic.
		return slices.Contains(externalPackages, t.PkgPath()) || slices.Contains(externalSentinels, e)
	})
}

// inTree reports whether f returns true for an error in the tree of err.
func inTree(err error, f func(error) bool) bool {
	if err == nil {
		return false
	}
	if f(err) {
		return true
	}
	switch u := any(err).(type) {
	case interface{ Unwrap() error }:
		return inTree(u.Unwrap(), f)
	case interface{ Unwrap() []error }:
		for _, e := range u.Unwrap() {
			if inTree(e, f) {
				return true
			}
		}
	}
	return false
}

// TestDifferentialLines checks the lines of the differential tests: which errors they take for errors of the OS or the
// engine (not those that the store makes, with their own texts), and that the texts of versions that they keep are
// those of show.
func TestDifferentialLines(t *testing.T) {
	at := testTime.UTC()
	versions := []Version{
		{Seq: 1, Kind: content.RefEntry, ID: entryID, Hash: sum(nil), SavedAt: at, SavedBy: userID},
		{Seq: 300, Kind: content.RefEntry, ID: entryID, SavedAt: at.Add(time.Millisecond), SavedBy: otherUser},
	}
	l := (&diffStore{}).lines()
	for _, v := range []any{versions, versions[:0], []Version(nil), versions, versions[1:]} {
		if got, want := l.show(v), show(v); got != want {
			t.Errorf("the text of %#v is %q, want %q", v, got, want)
		}
	}
	if len(l.shown) != len(versions) {
		t.Errorf("the texts of %d versions are kept, want %d", len(l.shown), len(versions))
	}
	for _, tt := range []struct {
		err  error
		want bool
	}{
		{notFoundError("entries/" + entryID + ".json"), false},
		{existsError("revisions/" + revisionID + ".json"), false},
		{content.Errors{{File: "site/settings.json", Msg: "unknown key"}}, false},
		{errAbort, false},
		{fmt.Errorf("version 1: %w", &time.ParseError{Value: "x"}), false},
		{errors.Join(errAbort, notFoundError("x")), false},
		{&fs.PathError{Op: "open", Path: "x", Err: fs.ErrNotExist}, true},
		{&os.LinkError{Op: "rename", Old: "a", New: "b", Err: fs.ErrExist}, true},
		{fmt.Errorf("x: %w", &logdb.CorruptError{Path: "x", Offset: -1, Reason: "bad header CRC"}), true},
		{fmt.Errorf("%w: %w", logdb.ErrFailed, io.ErrShortWrite), true},
		{errors.Join(errAbort, vfs.ErrLocked), true},
		{context.Canceled, true},
		{fmt.Errorf("x: %w", fs.ErrNotExist), false},
	} {
		if got := externalError(tt.err); got != tt.want {
			t.Errorf("externalError(%v (%T)) = %v, want %v", tt.err, tt.err, got, tt.want)
		}
	}
}

// diffLines are the lines of what a step of a differential run has done on a store, a line for each call: the result as
// show has it, and the text of the error with whether it wraps fs.ErrNotExist or fs.ErrExist and whether it is
// content.Errors. An error of the OS or the engine has a text of its own, so its line has only its kind; none is
// expected, and external keeps the first, which fails the test.
type diffLines struct {
	lines    []string
	shown    map[Version]string // the texts of versions, by version
	external error
	at       int   // the index of the line of external
	errs     int   // the calls in an Update that have failed
	update   error // the error of the Update
}

func (l *diffLines) add(desc string, v any, err error) {
	line := desc + " = " + l.show(v)
	if err != nil {
		var es content.Errors
		kind := fmt.Sprintf("(not exist %v, exist %v, content.Errors %v)", errors.Is(err, fs.ErrNotExist),
			errors.Is(err, fs.ErrExist), errors.As(err, &es))
		if externalError(err) {
			if l.external == nil {
				l.external, l.at = err, len(l.lines)
			}
			line += " error of the OS or the engine " + kind
		} else {
			line += fmt.Sprintf(" error %q %s", err, kind)
		}
	}
	l.lines = append(l.lines, line)
}

// show is show(v), which makes the text of each version of a History once with l.shown, since every View has them.
func (l *diffLines) show(v any) string {
	versions, ok := v.([]Version)
	if !ok || versions == nil || l.shown == nil {
		return show(v)
	}
	texts := make([]string, len(versions))
	for i, ver := range versions {
		if texts[i], ok = l.shown[ver]; !ok {
			texts[i] = show(ver)
			l.shown[ver] = texts[i]
		}
	}
	return "[" + strings.Join(texts, " ") + "]"
}

// diffStore is a store of a differential run: the model, or the log backend with log in a directory of the OS or, if
// fsys is not nil, in that simfs world. The clock of its Updates gives each one a millisecond more.
type diffStore struct {
	name   string
	log    *logdb.Options // nil for the model
	model  *modelBackend  // of the model; nil for the log backend
	dir    string         // the data directory; with fsys only its name in errors
	fsys   *simfs.FS
	s      *Store // nil while closed
	reader *Store // of the log backend, open since the store was created: its Views read the new commits
	ticks  int    // the calls of the clock so far
	shown  map[Version]string
}

// options returns the options of create, open and openLogReader of d, the log backend.
func (d *diffStore) options() options {
	if d.fsys == nil {
		return options{log: d.log}
	}
	return options{log: d.log, fs: d.fsys}
}

// open creates the store of d if fresh is true, or opens it.
func (d *diffStore) open(t *testing.T, fresh bool) {
	t.Helper()
	var s *Store
	var err error
	switch {
	case d.log == nil:
		if fresh {
			d.model = newModelBackend()
		}
		s = &Store{b: d.model, files: simfs.New("files")}
	case fresh:
		s, err = create(d.dir, d.options())
	default:
		s, err = open(d.dir, d.options())
	}
	if err != nil {
		t.Fatalf("%s: opening the store: %v", d.name, err)
	}
	s.now = func() time.Time {
		d.ticks++
		return testTime.Add(time.Duration(d.ticks) * time.Millisecond)
	}
	d.s = s
}

func (d *diffStore) close(t *testing.T) {
	t.Helper()
	if d.s == nil {
		return
	}
	err := d.s.Close()
	d.s = nil
	if err != nil {
		t.Fatalf("%s: Close() = %v", d.name, err)
	}
}

// removeCheckpoint removes the checkpoint of d, a closed store of the log backend, so that the next open reads the
// whole log.
func (d *diffStore) removeCheckpoint(t *testing.T) {
	t.Helper()
	var err error
	if d.fsys != nil {
		err = d.fsys.Remove("logdb/checkpoint")
	} else {
		err = os.Remove(filepath.Join(d.dir, "logdb", "checkpoint"))
	}
	if err != nil {
		t.Fatalf("%s: removing the checkpoint: %v", d.name, err)
	}
}

// update runs u in an Update of d.
func (d *diffStore) update(t *testing.T, u diffUpdate) diffLines {
	l := d.lines()
	err := d.s.Update(t.Context(), func(tx *Tx) error {
		for _, op := range u.ops {
			v, err := op.run(tx)
			l.add(op.desc, v, err)
			if err != nil {
				l.errs++
				if u.end == endFirstError {
					return err
				}
			}
		}
		if u.end == endAbort {
			return errAbort
		}
		return nil
	})
	l.add("Update()", nil, err)
	l.update = err
	return l
}

// lines returns lines for d to add to.
func (d *diffStore) lines() diffLines {
	if d.shown == nil {
		d.shown = map[Version]string{}
	}
	return diffLines{shown: d.shown}
}

// dump returns the lines of the reads r in a View of s, d or one of its readers.
func (d *diffStore) dump(t *testing.T, s *Store, r diffReads) diffLines {
	l := d.lines()
	err := s.View(t.Context(), func(tx *Tx) error {
		r.run(tx, l.add)
		return nil
	})
	l.add("View()", nil, err)
	return l
}

func (r diffReads) run(tx *Tx, line func(desc string, v any, err error)) {
	for _, kind := range r.kinds {
		docs, err := tx.Documents(kind)
		line(fmt.Sprintf("Documents(%q)", kind), docs, err)
	}
	for _, d := range r.docs {
		doc, err := tx.Document(d.k.kind, d.id)
		line(fmt.Sprintf("Document(%q, %q)", d.k.kind, d.id), doc, err)
		versions, err := tx.History(d.k.kind, d.id)
		line(fmt.Sprintf("History(%q, %q)", d.k.kind, d.id), versions, err)
	}
	for _, id := range r.revs {
		rev, err := tx.Revision(id)
		line(fmt.Sprintf("Revision(%q)", id), rev, err)
	}
	for _, entry := range r.entries {
		revs, err := tx.Revisions(entry)
		line(fmt.Sprintf("Revisions(%q)", entry), revs, err)
	}
	for _, h := range r.hashes {
		data, err := tx.Object(h)
		line(fmt.Sprintf("Object(%q)", h), data, err)
	}
}

// diffCounts are what a differential run has done in each store: the Updates, and those that returned nil, errAbort or
// another error; the calls in the Updates, and those that failed; the calls in the Views; the times that the stores
// were opened again, also without the checkpoints and after crashes, and the opens after crashes that replayed records
// after a checkpoint; the times that readers read everything.
type diffCounts struct {
	updates, commits, aborted, failed          int
	ops, opErrors                              int
	reads                                      int
	reopens, replays, crashes, afterCheckpoint int
	readers                                    int
}

// diffRun runs the same steps on the model and on the log backend in each placement, and checks that each log store has
// the lines of the model.
type diffRun struct {
	t      *testing.T
	about  string // the run in the messages
	ref    *diffStore
	logs   []*diffStore
	reads  func(invalid bool) diffReads // of the dumps, with the invalid names if invalid is true
	counts diffCounts
}

// newDiffRun creates the stores of a run: the model, and the log backend in each placement with the options of
// logBackends changed by change, on simfs if sim is true, else on the OS.
func newDiffRun(t *testing.T, change func(o *logdb.Options), sim bool) *diffRun {
	t.Helper()
	r := &diffRun{t: t, ref: &diffStore{name: "model"}}
	for _, tb := range logBackends {
		o := *tb.log
		change(&o)
		d := &diffStore{name: tb.name, log: &o, dir: "data"}
		if sim {
			d.fsys = simfs.New("data")
		} else {
			d.dir = filepath.Join(t.TempDir(), "data")
		}
		r.logs = append(r.logs, d)
	}
	for _, d := range r.stores() {
		d.open(t, true)
		t.Cleanup(func() {
			if d.s != nil {
				_ = d.s.Close()
			}
		})
		if d.log == nil {
			continue
		}
		var err error
		if d.reader, err = openLogReader(d.dir, d.options()); err != nil {
			t.Fatalf("%s: openLogReader() = %v", d.name, err)
		}
		t.Cleanup(func() { _ = d.reader.Close() })
	}
	return r
}

func (r *diffRun) stores() []*diffStore {
	return append([]*diffStore{r.ref}, r.logs...)
}

// compare runs f on each store, and stops the test at the first line in which a log store differs from the model, or at
// an error of the OS or the engine. It returns the lines of the model.
func (r *diffRun) compare(step string, f func(d *diffStore) diffLines) diffLines {
	r.t.Helper()
	want := f(r.ref)
	var problems []string
	external := func(d *diffStore, l diffLines) {
		if l.external != nil {
			problems = append(problems, fmt.Sprintf(
				"%s: line %d has an error of the OS or the engine: %v\n  %s", d.name, l.at+1, l.external,
				l.lines[l.at]))
		}
	}
	external(r.ref, want)
	for _, d := range r.logs {
		got := f(d)
		external(d, got)
		for i := range max(len(got.lines), len(want.lines)) {
			if g, w := line(got.lines, i), line(want.lines, i); g != w {
				problems = append(problems, fmt.Sprintf(
					"%s differs from the model at line %d of %d:\n  %s: %s\n  model: %s", d.name, i+1, len(want.lines),
					d.name, g, w))
				break
			}
		}
	}
	if problems != nil {
		r.t.Fatalf("%s, %s:\n%s\nrun it alone: %s", r.about, step, strings.Join(problems, "\n"), reproduce(r.t))
	}
	return want
}

// line returns lines[i], or a text that no line has if lines has fewer.
func line(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return "(no line)"
}

// update runs u on each store.
func (r *diffRun) update(step string, u diffUpdate) {
	r.t.Helper()
	l := r.compare(step, func(d *diffStore) diffLines { return d.update(r.t, u) })
	r.counts.updates++
	r.counts.ops += len(l.lines) - 1
	r.counts.opErrors += l.errs
	switch {
	case l.update == nil:
		r.counts.commits++
	case errors.Is(l.update, errAbort):
		r.counts.aborted++
	default:
		r.counts.failed++
	}
}

// view reads everything in a View of each store, with the invalid names if invalid is true.
func (r *diffRun) view(step string, invalid bool) {
	r.t.Helper()
	reads := r.reads(invalid)
	l := r.compare(step, func(d *diffStore) diffLines { return d.dump(r.t, d.s, reads) })
	r.counts.reads += len(l.lines) - 1
}

// reopen closes every log store, reads everything in the readers that are open since the start, which read the log to
// its end while no writer runs, and opens every store again, a log store without its checkpoint if replay is true, so
// that it reads its whole log; then it reads everything, and checks the stores.
func (r *diffRun) reopen(step string, replay bool) {
	r.t.Helper()
	for _, d := range r.logs {
		d.close(r.t)
	}
	reads := r.reads(true)
	r.compare(step+", in the readers open since the start while the writers are closed",
		func(d *diffStore) diffLines {
			if d.log == nil {
				return d.dump(r.t, d.s, reads)
			}
			return d.dump(r.t, d.reader, reads)
		})
	want := "used"
	if replay {
		want = "none"
	}
	for _, d := range r.stores() {
		d.close(r.t)
		if d.log == nil {
			d.open(r.t, false)
			continue
		}
		if replay {
			d.removeCheckpoint(r.t)
		}
		d.open(r.t, false)
		if got := logDB(r.t, d.s).Recovery().Checkpoint; got != want {
			r.t.Fatalf("%s, %s: %s has opened with the checkpoint %q, want %q", r.about, step, d.name, got, want)
		}
	}
	r.view(step+", the View after opening the stores again", true)
	for _, d := range r.logs {
		checkIntegrity(r.t, d.s)
		checkConsistent(r.t, d.s)
	}
	if replay {
		r.counts.replays++
	} else {
		r.counts.reopens++
	}
}

// crash crashes the world of each log store as model does: keep-all as when its process is killed (the page cache
// stays), lose-all as when the machine loses power (only what was synced stays). Every Update has returned, so each
// must have its commits. It opens the store in the new world, with a new reader, and the model again; then it reads
// everything, also in the new readers, and checks the log stores.
func (r *diffRun) crash(step, model string) {
	r.t.Helper()
	for _, d := range r.stores() {
		if d.fsys == nil {
			d.close(r.t)
			d.open(r.t, false)
			continue
		}
		var keep []bool // nil: lose all
		if model == "keep-all" {
			keep = make([]bool, len(d.fsys.Pending()))
			for i := range keep {
				keep[i] = true
			}
		}
		world := d.fsys.Crash(simfs.Model{Name: model, Keep: keep})
		d.close(r.t)
		if err := d.reader.Close(); err != nil {
			r.t.Fatalf("%s, %s: Close() of the reader of %s = %v", r.about, step, d.name, err)
		}
		d.fsys = world
		d.open(r.t, false)
		var err error
		if d.reader, err = openLogReader(d.dir, d.options()); err != nil {
			r.t.Fatalf("%s, %s: openLogReader() of %s = %v", r.about, step, d.name, err)
		}
		switch info := logDB(r.t, d.s).Recovery(); {
		case info.Checkpoint != "used" && info.Checkpoint != "none":
			r.t.Fatalf("%s, %s: %s has opened after the crash with the checkpoint %q", r.about, step, d.name,
				info.Checkpoint)
		case info.Checkpoint == "used" && info.Replayed > 0:
			r.counts.afterCheckpoint++
		}
	}
	r.view(step+", the View after the crash", true)
	reads := r.reads(true)
	r.compare(step+", in the readers opened after the crash", func(d *diffStore) diffLines {
		if d.log == nil {
			return d.dump(r.t, d.s, reads)
		}
		return d.dump(r.t, d.reader, reads)
	})
	for _, d := range r.logs {
		checkIntegrity(r.t, d.s)
		checkConsistent(r.t, d.s)
	}
	r.counts.crashes++
}

// readers reads everything in readers of each log store, beside its writer if that is open, and in the model: in the
// reader open since the store was created, and in a new one.
func (r *diffRun) readers(step string) {
	r.t.Helper()
	reads := r.reads(true)
	l := r.compare(step+", in the readers open since the start", func(d *diffStore) diffLines {
		if d.log == nil {
			return d.dump(r.t, d.s, reads)
		}
		return d.dump(r.t, d.reader, reads)
	})
	r.compare(step+", in new readers", func(d *diffStore) diffLines {
		if d.log == nil {
			return l
		}
		s, err := openLogReader(d.dir, d.options())
		if err != nil {
			r.t.Fatalf("%s, %s: openLogReader() of %s = %v", r.about, step, d.name, err)
		}
		defer func() {
			if err := s.Close(); err != nil {
				r.t.Errorf("%s, %s: Close() of the reader of %s = %v", r.about, step, d.name, err)
			}
		}()
		return d.dump(r.t, s, reads)
	})
	r.counts.reads += 2 * (len(l.lines) - 1)
	r.counts.readers++
}

// finish reads everything in readers beside the log stores, crashes them if they are on simfs, reads everything in
// readers after they have closed, then opens the stores again with and without the checkpoints, and logs the counts of
// the run.
func (r *diffRun) finish() {
	r.t.Helper()
	r.readers("at the end, beside the writers")
	if r.logs[0].fsys != nil {
		r.crash("at the end, after a power loss", "lose-all")
	}
	for _, d := range r.logs {
		d.close(r.t)
	}
	r.readers("at the end, after the writers have closed")
	r.reopen("at the end", false)
	r.reopen("at the end, without the checkpoints", true)
	var stats []string
	for _, d := range r.logs {
		st := logDB(r.t, d.s).Stats()
		stats = append(stats, fmt.Sprintf("%s %d segments, %d inline and %d file objects", d.name, st.Segments,
			st.InlineObjects, st.FileObjects))
	}
	c := r.counts
	r.t.Logf("%s: %d updates (%d committed, %d aborted, %d failed), %d calls in them (%d failed), %d reads; "+
		"%d versions, %d revisions; opened again %d times, %d without the checkpoints, %d times after crashes (%d "+
		"replaying records after a checkpoint), read %d times in readers; %s", r.about, c.updates, c.commits, c.aborted,
		c.failed, c.ops, c.opErrors, c.reads, count(r.t, r.logs[0].s, "versions"), count(r.t, r.logs[0].s, "revisions"),
		c.reopens, c.replays, c.crashes, c.afterCheckpoint, c.readers, strings.Join(stats, ", "))
}

// The sizes of the segments and the checkpoints of the log stores of a seed (0: the defaults, 64 MiB), so that some
// runs have many segments and checkpoints.
var (
	diffSegmentBytes    = []int64{0, 1024, 4096}
	diffCheckpointBytes = []int64{0, 2048, 8192}
)

// runDiff runs updates Updates of the generator of seed, each with a View that reads everything after it, on the model
// and on the log backend in each placement on simfs.
func runDiff(t *testing.T, seed uint64, updates int) {
	segment, checkpoint := diffSegmentBytes[seed%3], diffCheckpointBytes[seed/3%3]
	r := newDiffRun(t, func(o *logdb.Options) { o.SegmentBytes, o.CheckpointBytes = segment, checkpoint }, true)
	g := newDiffGen(seed)
	r.reads = g.reads
	r.about = fmt.Sprintf("seed %d (log segments of %d bytes, checkpoints every %d bytes; 0: 64 MiB)", seed, segment,
		checkpoint)
	for i := range updates {
		step := fmt.Sprintf("update %d", i+1)
		r.update(step, g.update())
		r.view("the View after "+step, false)
		switch {
		case g.chance(0.03):
			r.reopen("after "+step, false)
		case g.chance(0.02):
			r.reopen("after "+step+", without the checkpoints", true)
		case g.chance(0.03):
			r.readers("after " + step)
		case g.chance(0.02):
			r.crash("after "+step+", killed", "keep-all")
		case g.chance(0.02):
			r.crash("after "+step+", after a power loss", "lose-all")
		}
	}
	r.finish()
}

// TestDifferential runs the same seeded random Updates of valid and invalid operations on the model and on the log
// backend in each placement, with a View that reads everything after each, and checks that every result and every
// error is that of the model: in the Updates, after the stores are opened again (with and without the checkpoints of
// the log stores) and after crashes of the log stores, and in readers of the log stores.
func TestDifferential(t *testing.T) {
	seeds, updates := 6, 120
	switch {
	case longTests():
		seeds, updates = 200, 500
	case raceEnabled:
		seeds, updates = 3, 80
	}
	for seed := range uint64(seeds) {
		t.Run(fmt.Sprintf("seed=%d", seed+1), func(t *testing.T) {
			t.Parallel()
			runDiff(t, seed+1, updates)
		})
	}
}

// TestDifferentialSampleSite puts the documents and the revisions of the sample site of internal/build on the model and
// on the log backend in each placement, on the OS, and checks that the results and the reads of everything are those of
// the model, also after the stores are opened again and in readers.
func TestDifferentialSampleSite(t *testing.T) {
	updates, reads := sampleSiteSteps(t)
	r := newDiffRun(t, func(*logdb.Options) {}, false)
	r.reads = func(bool) diffReads { return reads }
	r.about = "the sample site"
	for i, u := range updates {
		r.update(fmt.Sprintf("update %d", i+1), u)
	}
	r.view("the View after the updates", true)
	r.finish()
	if r.counts.commits != len(updates) || r.counts.opErrors != 0 {
		t.Errorf("%d of %d updates committed, %d calls failed; want all and none", r.counts.commits, len(updates),
			r.counts.opErrors)
	}
}

// sampleSiteSteps returns Updates that put the documents of the sample site of internal/build, a kind in each, and
// then add its revisions with the entries that publish them, and the reads of everything that they put.
func sampleSiteSteps(t *testing.T) ([]diffUpdate, diffReads) {
	t.Helper()
	site := filepath.Join("..", "build", "testdata", "site")
	d, err := sitedir.Open(site)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	var updates []diffUpdate
	var reads diffReads
	hashes := map[string]bool{}
	see := func(data []byte) {
		if h := hashOf(data); !hashes[h] {
			hashes[h] = true
			reads.hashes = append(reads.hashes, h)
		}
	}
	publisher := map[string]string{} // the entries by the revisions that they publish
	for _, tt := range testDocs {
		var docs map[string]content.SiteFile
		if tt.kind == SettingsKind {
			settings, _, err := d.Settings()
			if err != nil {
				t.Fatalf("Settings() = %v", err)
			}
			docs = map[string]content.SiteFile{"": settings}
		} else if docs, err = d.Documents(tt.kind); err != nil || len(docs) == 0 {
			t.Fatalf("Documents(%q) of the sample site = %d documents, %v", tt.kind, len(docs), err)
		}
		reads.kinds = append(reads.kinds, tt.kind)
		var u diffUpdate
		// Backwards, so that Documents has to sort them.
		for _, id := range slices.Backward(slices.Sorted(maps.Keys(docs))) {
			data, kind := docs[id].Data, tt.kind
			see(data)
			reads.docs = append(reads.docs, diffDoc{diffKind{kind: kind}, id})
			u.ops = append(u.ops, diffOp{
				desc: fmt.Sprintf("Put(%q, %q, %s, %q)", kind, id, describe(docs[id].Name, data), userID),
				run:  func(tx *Tx) (any, error) { return tx.Put(kind, id, data, userID) },
			})
			if kind != content.RefEntry {
				continue
			}
			reads.entries = append(reads.entries, id)
			var e content.Entry
			if err := e.UnmarshalJSON(data); err != nil {
				t.Fatalf("%s: %v", docs[id].Name, err)
			}
			if e.PublishedRevision != "" {
				publisher[e.PublishedRevision] = id
			}
		}
		updates = append(updates, u)
	}
	files, err := os.ReadDir(filepath.Join(site, "revisions"))
	if err != nil {
		t.Fatal(err)
	}
	var u diffUpdate
	for _, f := range files {
		id := strings.TrimSuffix(f.Name(), ".json")
		entry, ok := publisher[id]
		if !ok {
			t.Fatalf("no entry of the sample site publishes %s", f.Name())
		}
		rev, err := d.Revision(id)
		if err != nil {
			t.Fatalf("Revision(%q) of the sample site = %v", id, err)
		}
		see(rev.Data)
		reads.revs = append(reads.revs, id)
		u.ops = append(u.ops, diffOp{
			desc: fmt.Sprintf("AddRevision(%q, %q, %s, %q)", entry, id, describe(rev.Name, rev.Data), userID),
			run:  func(tx *Tx) (any, error) { return tx.AddRevision(entry, id, rev.Data, userID) },
		})
	}
	if len(u.ops) == 0 {
		t.Fatal("the sample site has no revisions")
	}
	return append(updates, u), reads
}
