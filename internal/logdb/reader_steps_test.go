package logdb

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// stepFS calls step before each operation that a reader reads, locks or syncs the store with.
type stepFS struct {
	vfs.FS
	step func(op string)
}

func (s stepFS) Open(name string) (vfs.File, error) {
	s.step("open " + name)
	f, err := s.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return stepFile{f, s.step, name}, nil
}

func (s stepFS) Lstat(name string) (fs.FileInfo, error) {
	s.step("lstat " + name)
	return s.FS.Lstat(name)
}

func (s stepFS) ReadDir(name string) ([]fs.DirEntry, error) {
	s.step("readdir " + name)
	return s.FS.ReadDir(name)
}

func (s stepFS) Lock(name string, mode vfs.LockMode) (func() error, error) {
	s.step("lock " + name)
	return s.FS.Lock(name, mode)
}

type stepFile struct {
	vfs.File
	step func(op string)
	name string
}

func (f stepFile) ReadAt(b []byte, off int64) (int, error) {
	f.step("read " + f.name)
	return f.File.ReadAt(b, off)
}

func (f stepFile) Stat() (fs.FileInfo, error) {
	f.step("stat " + f.name)
	return f.File.Stat()
}

func (f stepFile) Sync() error {
	f.step("sync " + f.name)
	return f.File.Sync()
}

// gapFS lists log/ without the segment *hidden, as a file system may list a directory that a file is made in meanwhile.
type gapFS struct {
	vfs.FS
	hidden *string
}

func (g gapFS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := g.FS.ReadDir(name)
	return slices.DeleteFunc(entries, func(e fs.DirEntry) bool { return name+"/"+e.Name() == *g.hidden }), err
}

// rival is the writer of a store on simfs, which moves between the operations of a reader. Its commit i+1 is change i.
type rival struct {
	t    *testing.T
	o    Options
	fsys *simfs.FS
	db   *DB // nil while no writer runs
	n    int // commits
}

func (w *rival) commit() {
	change(w.t, w.db, w.n)
	w.n++
}

// add commits in the last segment, which must have room.
func (w *rival) add() {
	n := w.db.Stats().Segments
	w.commit()
	if w.db.Stats().Segments != n {
		w.t.Fatal("the commit began a segment")
	}
}

// segment commits until the writer has begun a segment.
func (w *rival) segment() {
	for n := w.db.Stats().Segments; w.db.Stats().Segments == n; {
		w.commit()
	}
}

func (w *rival) checkpoint() {
	if err := w.db.Checkpoint(); err != nil {
		w.t.Fatal(err)
	}
}

func (w *rival) stop() {
	if w.db != nil {
		closeDB(w.t, w.db)
		w.db = nil
	}
}

func (w *rival) start() {
	db, err := Open(w.fsys, w.o)
	if err != nil {
		w.t.Fatal(err)
	}
	w.db = db
}

// kill stops the writer and runs one that is killed after j more counted operations, in its Open or in an Update, which
// is then there or not. No writer runs after it, until settle.
func (w *rival) kill(j int) {
	w.stop()
	p := w.fsys.KillAt(w.fsys.Counted() + j)
	if db, err := Open(p.FS(), w.o); err == nil {
		for i := w.n; db.Update(context.Background(), changeF(i)) == nil; {
			i++
		}
		_ = db.Close()
	}
}

// settle starts a writer if none runs, which recovers what one that was killed has left, and counts the commits.
func (w *rival) settle() {
	if w.db == nil {
		w.start()
	}
	w.n = int(seqOf(w.t, w.db))
}

type rivalMove struct {
	name string
	do   func(w *rival)
}

// rivalMoves are what the writer does between two operations of a reader.
func rivalMoves() []rivalMove {
	moves := []rivalMove{
		{"commit", (*rival).add},
		{"segment", (*rival).segment},
		{"checkpoint", (*rival).checkpoint},
		{"segment, checkpoint", func(w *rival) { w.segment(); w.checkpoint() }},
		{"segment, checkpoint, commit", func(w *rival) { w.segment(); w.checkpoint(); w.commit() }},
		{"segment, close", func(w *rival) { w.segment(); w.stop() }},
		{"restart, segment, checkpoint", func(w *rival) { w.stop(); w.start(); w.segment(); w.checkpoint() }},
	}
	for j := matrixSize(1, 2, 1); j <= 16; j += matrixSize(1, 4, 1) {
		moves = append(moves, rivalMove{fmt.Sprintf("kill after %d operations", j), func(w *rival) { w.kill(j) }})
	}
	return moves
}

// TestReaderBesideWriter lets the writer move before each operation of a reader that opens the store, of a View that
// refreshes it, and of a Backup: the writer commits in its segment, begins a segment, writes a checkpoint, closes,
// starts again, or is killed after each of its first 16 counted operations (with -race, the 2nd, 6th, 10th and 14th),
// in its Open or in an Update, and no writer runs after it. The reader never finds the store corrupt: it opens at a
// commit that was durable when it began or later, its Views then see what a writer that starts has, and the next
// commit of that writer, and the copy of Backup opens to the commit of its position.
func TestReaderBesideWriter(t *testing.T) {
	ctx := context.Background()
	// After each start, a segment has room for the commit of the move "commit"; after "a checkpoint", for no second
	// one, so that the writer that is killed there begins a segment.
	o := Options{Placement: Hybrid, Threshold: 100, SegmentBytes: 950, CheckpointBytes: -1}
	states := changeStates(t, 40)
	// same checks that got is the state after seq commits.
	same := func(got string, seq uint64) error {
		switch {
		case seq >= uint64(len(states)):
			return fmt.Errorf("commit %d: the test has the states of %d", seq, len(states)-1)
		case got != states[seq]:
			return fmt.Errorf("not the state of commit %d:\n%s\nwant:\n%s", seq, got, states[seq])
		}
		return nil
	}
	starts := []rivalMove{
		{"no checkpoint", func(*rival) {}},
		{"a checkpoint", func(w *rival) { w.checkpoint(); w.commit() }},
		{"a checkpoint of the last commit", (*rival).checkpoint},
	}
	// begin makes a store, and starts its writer as start does.
	begin := func(t *testing.T, start rivalMove) *rival {
		fsys := simfs.New(simDir)
		db, err := Create(fsys, o)
		if err != nil {
			t.Fatal(err)
		}
		w := &rival{t: t, o: o, fsys: fsys, db: db}
		for range 5 {
			w.commit()
		}
		start.do(w)
		return w
	}
	tuned := begin(t, starts[1])
	tuned.add()
	segments := tuned.db.Stats().Segments
	tuned.commit()
	if tuned.db.Stats().Segments == segments {
		t.Fatal(`the second commit after "a checkpoint" did not begin a segment`)
	}
	tuned.stop()
	for _, sc := range []struct {
		name string
		need []string // operations that the reader must make
		// run reads the store of w through a stepFS that calls step, while the writer may move, and settles w.
		run func(w *rival, step func(op string)) error
	}{
		{"open", []string{"lstat checkpoint", "readdir log"}, func(w *rival, step func(op string)) error {
			first := uint64(w.n)
			moving := true
			rd, err := OpenReader(stepFS{w.fsys, func(op string) {
				if moving {
					step(op)
				}
			}}, Options{})
			moving = false
			if err != nil {
				return err
			}
			defer func() { _ = rd.Close() }()
			got := stateOf(w.t, rd)
			w.settle()
			if seq := rd.Recovery().Seq; seq < first || seq > uint64(w.n) {
				return fmt.Errorf("the reader opened at commit %d, want %d..%d", seq, first, w.n)
			}
			if err := same(got, uint64(w.n)); err != nil {
				return err
			}
			w.commit()
			return same(stateOf(w.t, rd), uint64(w.n))
		}},
		{"view", []string{"read head"}, func(w *rival, step func(op string)) error {
			moving := false
			rd, err := OpenReader(stepFS{w.fsys, func(op string) {
				if moving {
					step(op)
				}
			}}, Options{})
			if err != nil {
				return err
			}
			defer func() { _ = rd.Close() }()
			w.segment()
			w.commit()
			first, seq := uint64(w.n), uint64(0)
			moving = true
			err = rd.View(ctx, func(tx *Tx) error {
				seq = tx.Seq()
				return nil
			})
			moving = false
			if err != nil {
				return err
			}
			got := stateOf(w.t, rd)
			w.settle()
			if seq < first || seq > uint64(w.n) {
				return fmt.Errorf("the View saw commit %d, want %d..%d", seq, first, w.n)
			}
			if err := same(got, uint64(w.n)); err != nil {
				return err
			}
			w.commit()
			return same(stateOf(w.t, rd), uint64(w.n))
		}},
		{"backup", []string{"lstat checkpoint", "readdir log"}, func(w *rival, step func(op string)) error {
			first := uint64(w.n)
			dst := simfs.New("/backup")
			p, err := Backup(ctx, stepFS{w.fsys, step}, dst, nil)
			if err != nil {
				return err
			}
			w.settle()
			if p.Seq < first || p.Seq > uint64(w.n) {
				return fmt.Errorf("the position is commit %d, want %d..%d", p.Seq, first, w.n)
			}
			cp, err := Open(dst, o)
			if err != nil {
				return fmt.Errorf("the copy: %w", err)
			}
			defer func() { _ = cp.Close() }()
			if info := cp.Recovery(); info.Seq != p.Seq || info.TruncatedAt != -1 {
				return fmt.Errorf("the copy at %+v: %+v", p, info)
			}
			return same(stateOf(w.t, cp), p.Seq)
		}},
	} {
		for _, start := range starts {
			t.Run(sc.name+"/"+start.name, func(t *testing.T) {
				t.Parallel()
				// run makes a store and reads it as sc does, with step, which gets its writer, before each operation.
				run := func(step func(w *rival, op string)) error {
					w := begin(t, start)
					defer func() {
						if w.db != nil {
							_ = w.db.Close()
						}
					}()
					return sc.run(w, func(op string) { step(w, op) })
				}
				at, done := "no move", false
				t.Cleanup(func() {
					if !done {
						t.Logf("stopped at: %s", at)
					}
				})
				var ops []string
				if err := run(func(_ *rival, op string) { ops = append(ops, op) }); err != nil {
					t.Fatal(err)
				}
				for _, op := range sc.need {
					if !slices.Contains(ops, op) {
						t.Fatalf("the reader does not %s: %v", op, ops)
					}
				}
				for _, mv := range rivalMoves() {
					for k, op := range ops {
						at = fmt.Sprintf("%s before operation %d (%s)", mv.name, k, op)
						n := 0
						if err := run(func(w *rival, _ string) {
							if n == k {
								mv.do(w)
							}
							n++
						}); err != nil {
							t.Errorf("%s: %v", at, err)
						}
					}
				}
				done = true
			})
		}
	}
}

// TestReaderListGap gives a reader that opens beside a writer the list of log/ that a file system may give while the
// writer begins two segments: with the later one, without the earlier. The reader does not read the segments after
// those that head and the checkpoint name, so it opens; it refuses a list without a segment up to those.
func TestReaderListGap(t *testing.T) {
	for _, c := range []struct {
		name       string
		checkpoint bool   // the writer writes one, in the next segment, after the reader has read head
		hide       segNum // the segment that the list lacks; head names segment 4
		seq        uint64 // of the commit that the reader opens at
		want       string // in the error of OpenReader, if it refuses
	}{
		{"after that of head", false, 5, 4, ""},
		{"after that of the checkpoint", true, 6, 5, ""},
		{"before that of head", false, 3, 0, "missing segment 3 of generation 1"},
		{"that of head", false, 4, 0, "the log does not match head"},
		{"that of the checkpoint", true, 5, 0, "the log does not have sequence 5"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fsys, db := newSim(t, Options{SegmentBytes: 1, CheckpointBytes: -1}) // each commit begins a segment
			n := 0
			commit := func() {
				n++
				putKey(t, db, 1, fmt.Sprintf("k%d", n), "v")
			}
			for range 4 {
				commit()
			}
			hidden, moved := "", map[string]bool{}
			rd, err := OpenReader(gapFS{stepFS{fsys, func(op string) {
				switch {
				case moved[op]:
				case op == "lstat checkpoint" && c.checkpoint:
					commit()
					if err := db.Checkpoint(); err != nil {
						t.Fatal(err)
					}
				case op == "readdir log":
					commit()
					commit()
					hidden = segName(1, c.hide)
				}
				moved[op] = true
			}}, &hidden}, Options{})
			if c.want != "" {
				var ce *CorruptError
				if !errors.As(err, &ce) || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("OpenReader: %v, want %q", err, c.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rd.Close() }()
			if seq := rd.Recovery().Seq; seq != c.seq {
				t.Errorf("the reader opened at commit %d, want %d", seq, c.seq)
			}
			hidden = ""
			checkDump(t, rd, dump(t, db))
		})
	}
}

// TestReaderListGapStaleCheckpoint: a checkpoint of an older generation, which names a later segment than head does,
// does not keep the names after that of head, where the list of generation 2 has a gap.
func TestReaderListGapStaleCheckpoint(t *testing.T) {
	fsys, db := newSim(t, Options{SegmentBytes: 1, CheckpointBytes: -1}) // each commit begins a segment
	n := 0
	commit := func() {
		n++
		putKey(t, db, 1, fmt.Sprintf("k%d", n), "v")
	}
	for range 6 {
		commit()
	}
	if err := db.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	// A compaction whose checkpoint fails leaves that of generation 1; the writer goes on in generation 2.
	fsys.Inject(simfs.Fault{Match: ckpTemp(simfs.OpSync), Err: errors.New("no"), Times: 1})
	if err := compactDB(db, Rewrite{}); err == nil {
		t.Fatal("the checkpoint did not fail")
	}
	commit()
	head, ck := readHeadFile(t, fsys), ckpOf(t, fsys)
	if head.gen != 2 || ck.gen != 1 || ck.seg <= head.seg+1 {
		t.Fatalf("head %+v, checkpoint %+v", head, ck)
	}
	hidden, moved := "", false
	rd, err := OpenReader(gapFS{stepFS{fsys, func(op string) {
		if op == "readdir log" && !moved {
			moved = true
			commit()
			commit()
			hidden = segName(2, head.seg+1)
		}
	}}, &hidden}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rd.Close() }()
	if info := rd.Recovery(); info.Seq != 7 || info.Generation != 2 || info.Checkpoint != "stale" {
		t.Errorf("Recovery: %+v", info)
	}
	hidden = ""
	checkDump(t, rd, dump(t, db))
}

// TestReaderListBase: a reader that opens while a writer opens a store whose compaction was killed after the base-end
// reads the whole base of the new generation: its list is not cut at the segment that head names in the old one.
func TestReaderListBase(t *testing.T) {
	withBaseBytes(t, 50)
	fsys, db := newSim(t, Options{})
	for i := range 4 {
		putKey(t, db, 1, fmt.Sprintf("k%d", i), "a value")
	}
	want := dump(t, db)
	var snap *simfs.FS
	db.o.SegmentBytes = 1 // each base record begins a segment
	db.o.Hook = func(p string) error {
		if p == "compact:base-end" {
			snap = copyFS(fsys)
		}
		return nil
	}
	if err := compactDB(db, Rewrite{}); err != nil {
		t.Fatal(err)
	}
	closeDB(t, db)
	if s := readHeadFile(t, snap); s.gen != 1 || s.seg != 1 || !exists(t, snap, segName(2, 2)) {
		t.Fatalf("head %+v, segments %v", s, logNames(t, snap))
	}
	lockFile(t, snap, "lock/writer", vfs.Exclusive)
	rd := openReader(t, snap, Options{})
	if info := rd.Recovery(); info.Generation != 2 || info.IgnoredGenerations != nil {
		t.Errorf("Recovery: %+v", info)
	}
	checkDump(t, rd, want)
}

// changeStates returns the states of a store after 0..n commits of change.
func changeStates(t *testing.T, n int) []string {
	t.Helper()
	_, db := newSim(t, Options{})
	states := []string{stateOf(t, db)}
	for i := range n {
		change(t, db, i)
		states = append(states, stateOf(t, db))
	}
	return states
}
