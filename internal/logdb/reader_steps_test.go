package logdb

import (
	"context"
	"fmt"
	"io/fs"
	"slices"
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

// kill stops the writer, runs one that is killed after j more counted operations, in its Open or in an Update, which
// is then there or not, and starts one, which recovers and commits.
func (w *rival) kill(j int) {
	w.stop()
	p := w.fsys.KillAt(w.fsys.Counted() + j)
	if db, err := Open(p.FS(), w.o); err == nil {
		for i := w.n; db.Update(context.Background(), changeF(i)) == nil; {
			i++
		}
		_ = db.Close()
	}
	w.start()
	w.n = int(seqOf(w.t, w.db))
	w.commit()
}

type rivalMove struct {
	name string
	do   func(w *rival)
}

// rivalMoves are what the writer does between two operations of a reader.
func rivalMoves() []rivalMove {
	moves := []rivalMove{
		{"commit", (*rival).commit},
		{"segment", (*rival).segment},
		{"checkpoint", (*rival).checkpoint},
		{"segment, checkpoint", func(w *rival) { w.segment(); w.checkpoint() }},
		{"segment, checkpoint, commit", func(w *rival) { w.segment(); w.checkpoint(); w.commit() }},
		{"segment, close", func(w *rival) { w.segment(); w.stop() }},
		{"restart, segment, checkpoint", func(w *rival) { w.stop(); w.start(); w.segment(); w.checkpoint() }},
	}
	for j := 1; j <= 16; j += matrixSize(1, 4, 1) {
		moves = append(moves, rivalMove{fmt.Sprintf("kill after %d operations", j), func(w *rival) { w.kill(j) }})
	}
	return moves
}

// TestReaderBesideWriter lets the writer move before each operation of a reader that opens the store, of a View that
// refreshes it, and of a Backup: the writer commits, begins a segment, writes a checkpoint, closes, starts again, or is
// killed after each of its first 16 counted operations (with -race, each fourth), in its Open or in one of its first
// Updates, and recovers. The reader never finds the store corrupt: it opens at a commit that was durable when it began
// or later, its Views then see the last one, and the copy of Backup opens to the commit of its position.
func TestReaderBesideWriter(t *testing.T) {
	ctx := context.Background()
	o := Options{Placement: Hybrid, Threshold: 100, SegmentBytes: 300, CheckpointBytes: -1}
	states := changeStates(t, 40)
	check := func(t *testing.T, db *DB, seq uint64) error {
		if seq >= uint64(len(states)) {
			return fmt.Errorf("commit %d: the test has the states of %d", seq, len(states)-1)
		}
		if got := stateOf(t, db); got != states[seq] {
			return fmt.Errorf("at commit %d:\n%s\nwant:\n%s", seq, got, states[seq])
		}
		return nil
	}
	starts := []rivalMove{
		{"no checkpoint", func(*rival) {}},
		{"a checkpoint", func(w *rival) { w.checkpoint(); w.commit() }},
		{"a checkpoint of the last commit", (*rival).checkpoint},
	}
	for _, sc := range []struct {
		name string
		need []string // operations that the reader must make
		// run reads the store of w through a stepFS that calls step, while the writer may move.
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
			if seq := rd.Recovery().Seq; seq < first || seq > uint64(w.n) {
				return fmt.Errorf("the reader opened at commit %d, want %d..%d", seq, first, w.n)
			}
			return check(w.t, rd, uint64(w.n))
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
			switch {
			case err != nil:
				return err
			case seq < first || seq > uint64(w.n):
				return fmt.Errorf("the View saw commit %d, want %d..%d", seq, first, w.n)
			}
			return check(w.t, rd, uint64(w.n))
		}},
		{"backup", []string{"lstat checkpoint", "readdir log"}, func(w *rival, step func(op string)) error {
			first := uint64(w.n)
			dst := simfs.New("/backup")
			p, err := Backup(ctx, stepFS{w.fsys, step}, dst, nil)
			switch {
			case err != nil:
				return err
			case p.Seq < first || p.Seq > uint64(w.n):
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
			return check(w.t, cp, p.Seq)
		}},
	} {
		for _, start := range starts {
			t.Run(sc.name+"/"+start.name, func(t *testing.T) {
				t.Parallel()
				// run makes a store and reads it as sc does, with step, which gets its writer, before each operation.
				run := func(step func(w *rival, op string)) error {
					fsys := simfs.New(simDir)
					db, err := Create(fsys, o)
					if err != nil {
						t.Fatal(err)
					}
					w := &rival{t: t, o: o, fsys: fsys, db: db}
					defer func() {
						if w.db != nil {
							_ = w.db.Close()
						}
					}()
					for range 5 {
						w.commit()
					}
					start.do(w)
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
