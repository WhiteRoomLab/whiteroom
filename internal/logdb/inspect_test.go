package logdb

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// frozenFS is an FS whose every call that could change the store fails and is recorded.
type frozenFS struct {
	vfs.FS
	mu    sync.Mutex
	calls []string
	open  int // files that are open
}

func (f *frozenFS) deny(call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	return errors.New("frozenFS: " + call)
}

func (f *frozenFS) denied() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *frozenFS) openFiles() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.open
}

func (f *frozenFS) Open(name string) (vfs.File, error) {
	file, err := f.FS.Open(name)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.open++
	return &frozenFile{File: file, fsys: f, name: name}, nil
}

func (f *frozenFS) OpenFile(name string, flag int, perm fs.FileMode) (vfs.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0 {
		return nil, f.deny("OpenFile " + name)
	}
	return f.Open(name)
}

func (f *frozenFS) Mkdir(name string, _ fs.FileMode) error { return f.deny("Mkdir " + name) }
func (f *frozenFS) Remove(name string) error               { return f.deny("Remove " + name) }
func (f *frozenFS) Rename(oldname, _ string) error         { return f.deny("Rename " + oldname) }
func (f *frozenFS) SyncDir(name string) error              { return f.deny("SyncDir " + name) }

type frozenFile struct {
	vfs.File
	fsys *frozenFS
	name string
}

func (f *frozenFile) Close() error {
	f.fsys.mu.Lock()
	f.fsys.open--
	f.fsys.mu.Unlock()
	return f.File.Close()
}

func (f *frozenFile) Write([]byte) (int, error)          { return 0, f.fsys.deny("Write " + f.name) }
func (f *frozenFile) WriteAt([]byte, int64) (int, error) { return 0, f.fsys.deny("WriteAt " + f.name) }
func (f *frozenFile) Truncate(int64) error               { return f.fsys.deny("Truncate " + f.name) }
func (f *frozenFile) Sync() error                        { return f.fsys.deny("Sync " + f.name) }

// treeNode is what a file or directory of a store has that Inspect must not change.
type treeNode struct {
	mode fs.FileMode
	size int64
	data string
}

// treeOf returns every directory and file of fsys, by name, with its mode, size and bytes.
func treeOf(t *testing.T, fsys vfs.FS) map[string]treeNode {
	t.Helper()
	tree := map[string]treeNode{}
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := fsys.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			name := path.Join(dir, e.Name())
			info, err := fsys.Lstat(name)
			if err != nil {
				t.Fatal(err)
			}
			n := treeNode{mode: info.Mode(), size: info.Size()}
			if info.IsDir() {
				tree[name] = n
				walk(name)
				continue
			}
			n.data = string(fileData(t, fsys, name))
			tree[name] = n
		}
	}
	walk(".")
	return tree
}

// inspectFS checks fsys with Inspect and returns the problems. It fails the test if Inspect has changed a file, or
// tried to.
func inspectFS(t *testing.T, fsys vfs.FS, o Options) []Problem {
	t.Helper()
	ctx := context.Background()
	before := treeOf(t, fsys)
	frozen := &frozenFS{FS: fsys}
	var ps []Problem
	called := false
	err := Inspect(ctx, frozen, o, func(in *Inspection) error {
		called = true
		return in.Check(ctx, func(p Problem) bool {
			ps = append(ps, p)
			return true
		})
	})
	if err != nil || !called {
		t.Fatalf("Inspect: %v, f called: %v", err, called)
	}
	if calls := frozen.denied(); len(calls) > 0 {
		t.Errorf("Inspect tried to change the store: %v", calls)
	}
	if n := frozen.openFiles(); n != 0 {
		t.Errorf("Inspect left %d files open", n)
	}
	if after := treeOf(t, fsys); !maps.Equal(before, after) {
		for name, n := range before {
			if after[name] != n {
				t.Errorf("%s was changed", name)
			}
		}
		for name := range after {
			if _, ok := before[name]; !ok {
				t.Errorf("%s was made", name)
			}
		}
	}
	for _, p := range ps {
		for _, row := range []string{"KEYMARK", "VALMARK"} {
			if strings.Contains(p.String(), row) {
				t.Errorf("the problem %q has a key or a value", p)
			}
		}
	}
	return ps
}

// inspect is inspectFS of a simulated file system, which also counts the operations that change it.
func inspect(t *testing.T, fsys *simfs.FS, o Options) []Problem {
	t.Helper()
	counted, pending := fsys.Counted(), len(fsys.Pending())
	ps := inspectFS(t, fsys, o)
	if n := fsys.Counted() - counted; n != 0 || len(fsys.Pending()) != pending {
		t.Errorf("Inspect made %d operations that change files, %d pending changes", n, len(fsys.Pending())-pending)
	}
	return ps
}

// wantProblem is a problem that Check must report: the file under the name of the file system ("." for the directory),
// the offset or -1, and a part of the reason.
type wantProblem struct {
	name   string
	off    int64
	reason string
}

func checkProblems(t *testing.T, fsys vfs.FS, got []Problem, want ...wantProblem) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%d problems, want %d:%s", len(got), len(want), problemLines(got))
		return
	}
	for i, w := range want {
		if g := got[i]; g.Path != path.Join(fsys.Name(), w.name) || g.Offset != w.off || !strings.Contains(g.Reason, w.reason) {
			t.Errorf("problem %d is %q, want %s at offset %d, with %q", i, g, w.name, w.off, w.reason)
		}
	}
}

func lastSegment(t *testing.T, fsys vfs.FS) string {
	t.Helper()
	names := logNames(t, fsys)
	return names[len(names)-1]
}

// versionBump makes the version of the file name, which has a header of whiteroom's files, newer than the program's.
func versionBump(t *testing.T, fsys vfs.FS, name string) {
	t.Helper()
	b := fileData(t, fsys, name)
	binary.LittleEndian.PutUint32(b[8:], formatVersion+1)
	setFile(t, fsys, name, b)
}

func TestInspectSound(t *testing.T) {
	ctx := context.Background()
	for _, pl := range []struct {
		name string
		p    Placement
	}{{"inline", Inline}, {"files", Files}, {"hybrid", Hybrid}} {
		for _, gen := range []genNum{1, 2} {
			for _, ckp := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s, generation %d, checkpoint %v", pl.name, gen, ckp), func(t *testing.T) {
					o := Options{Placement: pl.p, Threshold: 100, SegmentBytes: 300}
					fsys, db := newSim(t, o)
					for i := range 9 {
						change(t, db, i)
					}
					if gen == 2 {
						if err := compactDB(db, Rewrite{}); err != nil {
							t.Fatal(err)
						}
						for i := 9; i < 12; i++ {
							change(t, db, i)
						}
					}
					closeDB(t, db)
					if !ckp {
						dropCheckpoint(t, fsys)
					}
					closed := copyFS(fsys)
					if info := recoveryOf(t, closed, o); info.Generation != uint64(gen) || (info.Checkpoint == "used") != ckp {
						t.Fatalf("Recovery: %+v", info)
					}
					checkProblems(t, closed, inspect(t, closed, o))
					dst := simfs.New("/copy")
					if _, err := Backup(ctx, copyFS(fsys), dst, nil); err != nil {
						t.Fatal(err)
					}
					checkProblems(t, dst, inspect(t, dst, o))
				})
			}
		}
	}
	t.Run("a new store", func(t *testing.T) {
		fsys, db := newSim(t, Options{})
		closeDB(t, db)
		checkProblems(t, fsys, inspect(t, fsys, Options{}))
	})
}

func TestInspectBackupWhileWriting(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "store")
	if err := os.Mkdir(src, 0o700); err != nil {
		t.Fatal(err)
	}
	o := Options{Placement: Hybrid, Threshold: 80, SegmentBytes: 1024, CheckpointBytes: 2048}
	db, err := Create(osFS(t, src), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var stop atomic.Bool
	var commits atomic.Int64
	done := make(chan error, 1)
	go func() {
		for n := 1; !stop.Load() || n <= 10; n++ {
			if err := db.Update(ctx, pairF(n)); err != nil {
				done <- err
				return
			}
			commits.Store(int64(n))
		}
		done <- nil
	}()
	var copies []string
	for i := range 4 {
		for commits.Load() < int64(10*i) {
			time.Sleep(time.Millisecond)
		}
		dst := filepath.Join(dir, fmt.Sprintf("copy-%d", i))
		if err := os.Mkdir(dst, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Backup(ctx, osFS(t, src), osFS(t, dst), nil); err != nil {
			t.Fatal(err)
		}
		copies = append(copies, dst)
	}
	stop.Store(true)
	if err := wait(t, done, "writer"); err != nil {
		t.Fatal(err)
	}
	for _, dst := range copies {
		fsys := osFS(t, dst)
		checkProblems(t, fsys, inspectFS(t, fsys, o))
	}
}

func TestInspectReadOnlyDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	dir := t.TempDir()
	o := Options{Placement: Hybrid, Threshold: 100, SegmentBytes: 300}
	db, err := Create(osFS(t, dir), o)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		change(t, db, i)
	}
	closeDB(t, db)
	chmod := func(file, dirs fs.FileMode) {
		if err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() {
				return os.Chmod(p, dirs)
			}
			return os.Chmod(p, file)
		}); err != nil {
			t.Fatal(err)
		}
	}
	chmod(0o400, 0o500)
	t.Cleanup(func() { chmod(0o600, 0o700) })
	fsys := osFS(t, dir)
	checkProblems(t, fsys, inspectFS(t, fsys, o))
}

// skewStore is a copy that Backup made of a store whose writer has written since its last checkpoint, and whose last
// commit the copy does not have: it has an older checkpoint, which pairs with the log up to the position, and records
// after it in several segments.
type skewStore struct {
	fsys *simfs.FS
	pos  Position
}

var skewOptions = Options{Placement: Hybrid, Threshold: 100, SegmentBytes: 300, CheckpointBytes: -1}

func newSkewStore(t *testing.T) *skewStore {
	t.Helper()
	src, db := newSim(t, skewOptions)
	for i := range 3 {
		change(t, db, i)
	}
	if err := db.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	for i := 3; i < 9; i++ {
		change(t, db, i)
	}
	dst := simfs.New("/copy")
	pos, err := Backup(context.Background(), src, dst, func(Position) error {
		change(t, db, 9) // the copy does not have it
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, index, _ := parseSegName(path.Base(lastSegment(t, dst)))
	if ck := ckpOf(t, dst); ck.seg+2 >= index || uint64(index) != pos.Segment || seqOf(t, db) != pos.Seq+1 {
		t.Fatalf("the checkpoint is in segment %d of %d, position %+v", ck.seg, index, pos)
	}
	return &skewStore{fsys: dst, pos: pos}
}

func headBehind(head, log uint64) wantProblem {
	return wantProblem{"head", -1, fmt.Sprintf("head names sequence %d, but the log ends with sequence %d", head, log)}
}

// TestInspectShortCopy: a copy that lacks the end of the log, which Open opens as it is, is a problem, as it is that
// head names a commit that the log does not have.
func TestInspectShortCopy(t *testing.T) {
	s := newSkewStore(t)
	last := lastSegment(t, s.fsys)
	hdr, offs := frames(t, s.fsys, last)
	segs := len(logNames(t, s.fsys))
	t.Run("the sound copy", func(t *testing.T) {
		checkProblems(t, s.fsys, inspect(t, copyFS(s.fsys), skewOptions))
	})
	for _, ckp := range []bool{true, false} {
		for _, removed := range []int{1, 2} {
			t.Run(fmt.Sprintf("%d segments removed, checkpoint %v", removed, ckp), func(t *testing.T) {
				fsys := copyFS(s.fsys)
				first := hdr.firstSeq
				for _, name := range slices.Backward(logNames(t, fsys)) {
					if removed == 0 {
						break
					}
					h, _ := frames(t, fsys, name)
					first = h.firstSeq
					removeFile(t, fsys, name)
					removed--
				}
				if !ckp {
					dropCheckpoint(t, fsys)
				}
				if got := len(logNames(t, fsys)); got >= segs {
					t.Fatalf("%d segments of %d", got, segs)
				}
				ps := inspect(t, fsys, skewOptions)
				checkProblems(t, fsys, ps, headBehind(s.pos.Seq, uint64(first)-1))
				if info := recoveryOf(t, fsys, skewOptions); info.Seq != uint64(first)-1 || info.TruncatedAt != -1 {
					t.Errorf("Open: %+v", info) // it opens the copy as it is
				}
			})
		}
	}
	t.Run("the checkpoint names the removed segment", func(t *testing.T) {
		v := newVerifyStore(t)
		fsys := copyFS(v.fsys)
		removeFile(t, fsys, lastSegment(t, fsys))
		checkProblems(t, fsys, inspect(t, fsys, verifyOptions), wantProblem{"checkpoint", -1, "the log does not have sequence"})
		if _, err := Open(copyFS(fsys), verifyOptions); err == nil {
			t.Error("Open opens it")
		}
	})
	t.Run("the last record is cut short", func(t *testing.T) {
		for _, cut := range []int{1, 8, 16} {
			fsys := copyFS(s.fsys)
			b := fileData(t, fsys, last)
			setFile(t, fsys, last, b[:len(b)-cut])
			ps := inspect(t, fsys, skewOptions)
			checkProblems(t, fsys, ps, wantProblem{last, offs[len(offs)-1], "torn tail"}, headBehind(s.pos.Seq, s.pos.Seq-1))
			if info := recoveryOf(t, fsys, skewOptions); info.TruncatedAt != offs[len(offs)-1] || info.Seq != s.pos.Seq-1 {
				t.Errorf("cut %d: Open: %+v", cut, info)
			}
		}
	})
	t.Run("a record is cut and the one before it is gone", func(t *testing.T) {
		if len(offs) < 2 {
			t.Skip("one record in the last segment")
		}
		fsys := copyFS(s.fsys)
		b := fileData(t, fsys, last)
		setFile(t, fsys, last, b[:offs[len(offs)-1]-8])
		ps := inspect(t, fsys, skewOptions)
		checkProblems(t, fsys, ps, wantProblem{last, offs[len(offs)-2], "torn tail"}, headBehind(s.pos.Seq, s.pos.Seq-2))
	})
}

// TestInspectRecovery: what Open changes or does not use is a problem, as Recovery tells.
func TestInspectRecovery(t *testing.T) {
	o := Options{SegmentBytes: 100} // a segment of one record
	base, _ := build(t, o, 3)
	t.Run("a torn new segment", func(t *testing.T) {
		header := appendSegHeader(nil, segHeader{gen: 1, index: 4, firstSeq: 4, salt: 7})
		for _, c := range []struct {
			name string
			data []byte
		}{
			{"empty", nil},
			{"torn", header[:30]},
			{"zeros", make([]byte, segHeaderSize)},
			{"a page of zeros", make([]byte, 4096)},
			{"torn, then zeros", append(slices.Clone(header[:20]), make([]byte, 200)...)},
		} {
			t.Run(c.name, func(t *testing.T) {
				fsys := copyFS(base)
				setFile(t, fsys, seg4, c.data)
				ps := inspect(t, fsys, o)
				checkProblems(t, fsys, ps, wantProblem{seg4, -1, "Open would remove"})
				if info := recoveryOf(t, fsys, o); info.RemovedSegment != simPath(seg4) {
					t.Errorf("Open removes %q", info.RemovedSegment)
				}
			})
		}
	})
	t.Run("a torn tail", func(t *testing.T) {
		fsys := copyFS(base)
		b := fileData(t, fsys, seg3)
		setFile(t, fsys, seg3, b[:len(b)-8])
		ps := inspect(t, fsys, o)
		checkProblems(t, fsys, ps, wantProblem{seg3, segHeaderSize, "torn tail"}, headBehind(3, 2))
		if info := recoveryOf(t, fsys, o); info.TruncatedAt != segHeaderSize {
			t.Errorf("Open: %+v", info)
		}
	})
	t.Run("newer generations that are incomplete", func(t *testing.T) {
		fsys := copyFS(base)
		setFile(t, fsys, segName(2, 1), appendSegHeader(nil, segHeader{gen: 2, index: 1, firstSeq: 3}))
		ps := inspect(t, fsys, o)
		checkProblems(t, fsys, ps, wantProblem{"log", -1, "generation 2 is newer"})
		setFile(t, fsys, segName(3, 1), appendSegHeader(nil, segHeader{gen: 3, index: 1, firstSeq: 3}))
		ps = inspect(t, fsys, o)
		checkProblems(t, fsys, ps, wantProblem{"log", -1, "generation 2 is newer"}, wantProblem{"log", -1, "generation 3 is newer"})
		if info := recoveryOf(t, fsys, o); !slices.Equal(info.IgnoredGenerations, []uint64{2, 3}) {
			t.Errorf("Open ignores %v", info.IgnoredGenerations)
		}
	})
}

func TestInspectCheckpoint(t *testing.T) {
	o := Options{Placement: Hybrid, Threshold: 100, SegmentBytes: 300}
	base, db := newSim(t, o)
	for i := range 5 {
		change(t, db, i)
	}
	closeDB(t, db)
	good := fileData(t, base, "checkpoint")
	h := ckpOf(t, base)
	for _, c := range []struct {
		name   string
		data   []byte
		reason string
	}{
		{"a byte of the body", func() []byte { b := slices.Clone(good); b[ckpHeaderSize+3] ^= 1; return b }(), "damaged (bad body CRC)"},
		{"a byte of the header", func() []byte { b := slices.Clone(good); b[20] ^= 1; return b }(), "damaged (bad header CRC)"},
		{"empty", nil, "damaged (short file)"},
		{"keys out of order", rawCkp(h, slices.Concat(uv(1, 0, 1, 1, 2), kv("KEYBBB", "VALBBB"), kv("KEYAAA", "VALAAA"))),
			"damaged (keys out of order)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fsys := copyFS(base)
			setFile(t, fsys, "checkpoint", c.data)
			ps := inspect(t, fsys, o)
			checkProblems(t, fsys, ps, wantProblem{"checkpoint", -1, c.reason})
			for _, p := range ps {
				if strings.Contains(p.String(), "KEY") || strings.Contains(p.String(), "VAL") {
					t.Errorf("the problem has a key or a value: %q", p)
				}
			}
			if info := recoveryOf(t, fsys, o); !strings.HasPrefix(info.Checkpoint, "damaged") || info.Seq != 5 {
				t.Errorf("Open: %+v", info)
			}
		})
	}
	t.Run("none", func(t *testing.T) {
		fsys := copyFS(base)
		dropCheckpoint(t, fsys)
		checkProblems(t, fsys, inspect(t, fsys, o))
	})
	t.Run("of an older generation", func(t *testing.T) {
		fsys := copyFS(base)
		db := reopen(t, fsys, o)
		if err := compactDB(db, Rewrite{}); err != nil {
			t.Fatal(err)
		}
		closeDB(t, db)
		setFile(t, fsys, "checkpoint", good)
		ps := inspect(t, fsys, o)
		checkProblems(t, fsys, ps, wantProblem{"checkpoint", -1, "of an older generation"})
		if info := recoveryOf(t, fsys, o); info.Checkpoint != "stale" || info.Generation != 2 {
			t.Errorf("Open: %+v", info)
		}
	})
	t.Run("a state that differs from the log", func(t *testing.T) {
		fsys, db := newSim(t, Options{})
		putKey(t, db, 1, "k", "v")
		closeDB(t, db)
		h := ckpOf(t, fsys)
		same := slices.Concat(uv(1, 0, 1, 1, 1), kv("k", "v"), uv(0))
		if !slices.Equal(rawCkp(h, same), fileData(t, fsys, "checkpoint")) {
			t.Fatal("the checkpoint is not as rebuilt")
		}
		setFile(t, fsys, "checkpoint", rawCkp(h, slices.Concat(uv(1, 0, 1, 1, 1), kv("k", "w"), uv(0))))
		ps := inspect(t, fsys, Options{})
		checkProblems(t, fsys, ps, wantProblem{".", -1, "table 1, keys: 1 with another value, 0 missing in memory, 0 missing in the log"})
	})
}

// TestInspectNewerVersion: a file of a newer version is the only problem, and nothing after it is read.
func TestInspectNewerVersion(t *testing.T) {
	v := newVerifyStore(t)
	for _, name := range []string{lastSegment(t, v.fsys), "checkpoint", "head"} {
		t.Run(name, func(t *testing.T) {
			fsys := copyFS(v.fsys)
			objectDamages[0].apply(t, fsys, v.files[0]) // it is not reported
			versionBump(t, fsys, name)
			ps := inspect(t, fsys, verifyOptions)
			checkProblems(t, fsys, ps, wantProblem{name, -1, "newer than this program knows"})
		})
	}
}

func TestInspectHead(t *testing.T) {
	v := newVerifyStore(t)
	t.Run("a slot is damaged", func(t *testing.T) {
		for _, slot := range []int{0, 1} {
			fsys := copyFS(v.fsys)
			b := fileData(t, fsys, "head")
			b[slot*headSlotSize+20] ^= 1
			setFile(t, fsys, "head", b)
			checkProblems(t, fsys, inspect(t, fsys, verifyOptions))
		}
	})
	t.Run("no slot is valid", func(t *testing.T) {
		fsys := copyFS(v.fsys)
		b := fileData(t, fsys, "head")
		b[20] ^= 1
		b[headSlotSize+20] ^= 1
		setFile(t, fsys, "head", b)
		p, _ := objectDamages[0].apply(t, fsys, v.files[0]) // Check goes on
		ps := inspect(t, fsys, verifyOptions)
		checkProblems(t, fsys, ps, wantProblem{"head", -1, "no valid slot"}, wantProblem{strings.TrimPrefix(p.Path, simDir+"/"), -1, p.Reason})
	})
	t.Run("the wrong size", func(t *testing.T) {
		fsys := copyFS(v.fsys)
		setFile(t, fsys, "head", fileData(t, fsys, "head")[:headSize-1])
		checkProblems(t, fsys, inspect(t, fsys, verifyOptions), wantProblem{"head", -1, fmt.Sprintf("has %d bytes, want %d", headSize-1, headSize)})
	})
	t.Run("another record", func(t *testing.T) {
		s := readHeadFile(t, v.fsys)
		for _, c := range []struct {
			name   string
			change func(s *headSlot)
			reason string
		}{
			{"a later sequence", func(s *headSlot) { s.seq += 3 }, fmt.Sprintf("head names sequence %d, but the log ends with sequence %d", s.seq+3, s.seq)},
			{"an earlier sequence", func(s *headSlot) { s.seq-- }, fmt.Sprintf("head names sequence %d, but the log ends with sequence %d", s.seq-1, s.seq)},
			{"a generation", func(s *headSlot) { s.gen++ }, fmt.Sprintf("head names generation %d, but the log ends in generation %d", s.gen+1, s.gen)},
			{"an end", func(s *headSlot) { s.end += 8 }, "head names the end"},
			{"a segment", func(s *headSlot) { s.seg-- }, "head names the end"},
			{"a CRC", func(s *headSlot) { s.crc++ }, "head names the end"},
		} {
			t.Run(c.name, func(t *testing.T) {
				fsys := copyFS(v.fsys)
				h := s
				c.change(&h)
				setFile(t, fsys, "head", headFile(h))
				checkProblems(t, fsys, inspect(t, fsys, verifyOptions), wantProblem{"head", -1, c.reason})
			})
		}
	})
}

// TestInspectVerify: the problems that VerifyEach finds are Check's, which finds them in the store as the state that
// Open recovers has it.
func TestInspectVerify(t *testing.T) {
	v := newVerifyStore(t)
	checkProblems(t, v.fsys, inspect(t, copyFS(v.fsys), verifyOptions))
	for i, c := range objectDamages {
		t.Run(c.name, func(t *testing.T) {
			fsys := copyFS(v.fsys)
			p, _ := c.apply(t, fsys, v.files[i%len(v.files)])
			if got := inspect(t, fsys, verifyOptions); !slices.Equal(got, []Problem{p}) {
				t.Errorf("problems:%s\nwant %q", problemLines(got), p)
			}
		})
	}
	t.Run("a record before the checkpoint", func(t *testing.T) {
		fsys := copyFS(v.fsys)
		p, _ := damageRecord(t, fsys)
		if got := inspect(t, fsys, verifyOptions); !slices.Equal(got, []Problem{p}) {
			t.Errorf("problems:%s\nwant %q", problemLines(got), p)
		}
	})
	t.Run("a record that Open reads", func(t *testing.T) {
		fsys := copyFS(v.fsys)
		dropCheckpoint(t, fsys)
		p, _ := damageRecord(t, fsys)
		objectDamages[0].apply(t, fsys, v.files[0]) // it is not reported: the log cannot be read
		if got := inspect(t, fsys, verifyOptions); !slices.Equal(got, []Problem{p}) {
			t.Errorf("problems:%s\nwant %q", problemLines(got), p)
		}
		if _, err := Open(copyFS(fsys), verifyOptions); err == nil {
			t.Error("Open opens it")
		}
	})
	t.Run("everything", func(t *testing.T) {
		fsys := copyFS(v.fsys)
		want, _ := damageAll(t, fsys, v)
		if got := inspect(t, fsys, verifyOptions); !slices.Equal(got, want) {
			t.Errorf("problems:%s\nwant:%s", problemLines(got), problemLines(want))
		}
	})
}

func TestInspectLayout(t *testing.T) {
	ctx := context.Background()
	base, db := newSim(t, Options{})
	putKey(t, db, 1, "a", "1")
	closeDB(t, db)
	for _, name := range []string{"head", "lock/entry", "lock/readers", "lock/writer", "objects", "log"} {
		t.Run(name, func(t *testing.T) {
			fsys := copyFS(base)
			if name == "log" {
				for _, seg := range logNames(t, fsys) {
					removeFile(t, fsys, seg)
				}
			}
			removeFile(t, fsys, name)
			_, openErr := Open(copyFS(fsys), Options{})
			if openErr == nil {
				t.Fatal("Open opens it")
			}
			called := false
			err := Inspect(ctx, fsys, Options{}, func(*Inspection) error {
				called = true
				return nil
			})
			if err == nil || err.Error() != openErr.Error() || called {
				t.Errorf("Inspect: %v, f called: %v; Open: %v", err, called, openErr)
			}
		})
	}
	t.Run("options", func(t *testing.T) {
		_, openErr := Open(copyFS(base), Options{Placement: 99})
		err := Inspect(ctx, copyFS(base), Options{Placement: 99}, func(*Inspection) error { return errors.New("called") })
		if err == nil || openErr == nil || err.Error() != openErr.Error() {
			t.Errorf("Inspect: %v; Open: %v", err, openErr)
		}
	})
}

// TestInspectWaits: Inspect waits for the writer and the readers to close, and for maintenance to end, until its context
// ends.
func TestInspectWaits(t *testing.T) {
	ctx := context.Background()
	fsys, db := newSim(t, Options{})
	putKey(t, db, 1, "a", "1")
	closeDB(t, db)
	var called atomic.Bool
	f := func(*Inspection) error {
		called.Store(true)
		return nil
	}
	short := func(what string) {
		t.Helper()
		before, counted := treeOf(t, fsys), fsys.Counted()
		start := time.Now() // before the deadline is set
		c, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		err := Inspect(c, fsys, Options{}, f)
		if d := time.Since(start); d < 100*time.Millisecond {
			t.Errorf("%s: Inspect waited %v", what, d)
		}
		if !errors.Is(err, context.DeadlineExceeded) || called.Load() {
			t.Errorf("%s: Inspect: %v, f called: %v", what, err, called.Load())
		}
		if !maps.Equal(before, treeOf(t, fsys)) || fsys.Counted() != counted {
			t.Errorf("%s: Inspect changed files", what)
		}
	}
	writer := reopen(t, fsys, Options{})
	short("a writer")
	closeDB(t, writer)
	reader := openReader(t, fsys, Options{})
	short("a reader")
	closeDB(t, reader)
	release := lockFile(t, fsys, "lock/writer", vfs.Shared)
	short("lock/writer")
	release()
	for _, other := range []struct {
		name string
		run  func(ctx context.Context, f func() error) error
	}{
		{"Maintain", func(ctx context.Context, f func() error) error {
			return Maintain(ctx, fsys, Options{}, func(*Maintenance) error { return f() })
		}},
		{"Inspect", func(ctx context.Context, f func() error) error {
			return Inspect(ctx, fsys, Options{}, func(*Inspection) error { return f() })
		}},
	} {
		entered, block, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		go func() {
			done <- other.run(ctx, func() error {
				close(entered)
				<-block
				return nil
			})
		}()
		wait(t, entered, other.name)
		short(other.name)
		close(block)
		if err := wait(t, done, other.name); err != nil {
			t.Errorf("%s: %v", other.name, err)
		}
	}
	// It goes on once they are closed.
	reader = openReader(t, fsys, Options{})
	done := make(chan error, 1)
	go func() { done <- Inspect(ctx, fsys, Options{}, f) }()
	select {
	case err := <-done:
		t.Fatalf("Inspect did not wait for the reader: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	closeDB(t, reader)
	if err := wait(t, done, "Inspect"); err != nil || !called.Load() {
		t.Errorf("Inspect: %v, f called: %v", err, called.Load())
	}
}

// TestInspectBlocksOpeners: while Inspect runs, or waits for the processes that have the store open, the others fail at
// once.
func TestInspectBlocksOpeners(t *testing.T) {
	ctx := context.Background()
	fsys, db := newSim(t, Options{})
	putKey(t, db, 1, "a", "1")
	closeDB(t, db)
	refused := func(what string, err error) {
		t.Helper()
		if err == nil || err.Error() != simDir+": maintenance is running" {
			t.Errorf("%s during Inspect: %v", what, err)
		}
	}
	openers := func() {
		t.Helper()
		_, err := Open(fsys, Options{WriterWait: time.Second})
		refused("Open", err)
		_, err = OpenReader(fsys, Options{})
		refused("OpenReader", err)
		_, err = Backup(ctx, fsys, simfs.New("/backup"), nil)
		refused("Backup", err)
	}
	err := Inspect(ctx, fsys, Options{}, func(in *Inspection) error {
		openers()
		if err := in.Check(ctx, func(p Problem) bool { return true }); err != nil {
			return err
		}
		openers()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	reader := openReader(t, fsys, Options{})
	done := make(chan error, 1)
	go func() { done <- Inspect(ctx, fsys, Options{}, func(*Inspection) error { return nil }) }()
	waitLocked(t, fsys, "lock/entry")
	openers()
	checkDump(t, reader, "1/a=1") // the reader that is open goes on
	closeDB(t, reader)
	if err := wait(t, done, "Inspect"); err != nil {
		t.Error(err)
	}
	// And they open it again.
	closeDB(t, reopen(t, fsys, Options{WriterWait: time.Second}))
}

func TestInspectReleases(t *testing.T) {
	ctx := context.Background()
	fsys, db := newSim(t, Options{})
	closeDB(t, db)
	boom := errors.New("boom")
	if err := Inspect(ctx, fsys, Options{}, func(*Inspection) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("Inspect: %v", err)
	}
	closeDB(t, reopen(t, fsys, Options{WriterWait: time.Second}))
	func() {
		defer func() {
			if r := recover(); r != "boom" {
				t.Errorf("recovered %v", r)
			}
		}()
		_ = Inspect(ctx, fsys, Options{}, func(*Inspection) error { panic("boom") })
	}()
	closeDB(t, reopen(t, fsys, Options{WriterWait: time.Second}))
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	called := false
	if err := Inspect(canceled, fsys, Options{}, func(*Inspection) error { called = true; return nil }); !errors.Is(err, context.Canceled) || called {
		t.Errorf("Inspect with a canceled context: %v, f called: %v", err, called)
	}
	closeDB(t, reopen(t, fsys, Options{WriterWait: time.Second}))
}

// multiStage is a store with a torn tail, a head ahead of the log, and the four kinds of damage of the files of
// objects, and the problems of Check in order.
func multiStage(t *testing.T) (*simfs.FS, []Problem) {
	t.Helper()
	v := newVerifyStore(t)
	fsys := copyFS(v.fsys)
	dropCheckpoint(t, fsys)
	last := lastSegment(t, fsys)
	_, offs := frames(t, fsys, last)
	b := fileData(t, fsys, last)
	setFile(t, fsys, last, b[:len(b)-8])
	head := readHeadFile(t, fsys)
	want := []Problem{
		{Path: simPath(last), Offset: offs[len(offs)-1], Reason: "a torn tail, which Open would cut off"},
		{Path: simPath("head"), Offset: -1, Reason: fmt.Sprintf("head names sequence %d, but the log ends with sequence %d", head.seq, head.seq-1)},
	}
	byHash := map[[32]byte]Problem{}
	for i, h := range v.files {
		byHash[h], _ = objectDamages[i].apply(t, fsys, h)
	}
	for _, h := range v.hashes {
		if p, ok := byHash[h]; ok {
			want = append(want, p)
		}
	}
	return fsys, want
}

func TestInspectMultiStage(t *testing.T) {
	fsys, want := multiStage(t)
	if got := inspect(t, fsys, verifyOptions); !slices.Equal(got, want) {
		t.Errorf("problems:%s\nwant:%s", problemLines(got), problemLines(want))
	}
}

// TestInspectStops: Check looks at nothing more once found returns false.
func TestInspectStops(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	fsys, want := multiStage(t)
	for stop := 1; stop <= len(want); stop++ {
		var got []Problem
		err := Inspect(ctx, fsys, verifyOptions, func(in *Inspection) error {
			return in.Check(ctx, func(p Problem) bool {
				got = append(got, p)
				return len(got) < stop
			})
		})
		if err != nil || !slices.Equal(got, want[:stop]) {
			t.Errorf("stop after %d: %v, problems:%s", stop, err, problemLines(got))
		}
	}
	// A problem that stops the log from being read is the only one, whatever found says.
	v := newVerifyStore(t)
	broken := copyFS(v.fsys)
	removeFile(t, broken, lastSegment(t, broken))
	calls := 0
	err := Inspect(ctx, broken, verifyOptions, func(in *Inspection) error {
		return in.Check(ctx, func(Problem) bool {
			calls++
			return false
		})
	})
	if err != nil || calls != 1 {
		t.Errorf("Check: %v, %d calls", err, calls)
	}
}

func TestInspectContext(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), time.Minute)
	defer stop()
	fsys, want := multiStage(t)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	err := Inspect(ctx, fsys, verifyOptions, func(in *Inspection) error {
		return in.Check(canceled, func(Problem) bool {
			t.Error("found is called")
			return true
		})
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Check with a canceled context: %v", err)
	}
	// Whenever the context ends, Check returns its error, after the problems that come first.
	ends := map[bool]int{}
	for n := 0; ; n++ {
		var got []Problem
		err := Inspect(ctx, fsys, verifyOptions, func(in *Inspection) error {
			return in.Check(&countdownContext{Context: ctx, left: n}, func(p Problem) bool {
				got = append(got, p)
				return true
			})
		})
		if err == nil {
			if !slices.Equal(got, want) {
				t.Errorf("the problems when the context does not end:%s", problemLines(got))
			}
			break
		}
		if !errors.Is(err, context.Canceled) || !slices.Equal(got, want[:len(got)]) {
			t.Fatalf("Check with a context that ends after %d calls: %v, problems:%s", n, err, problemLines(got))
		}
		ends[len(got) == 0]++
		if n > 10000 {
			t.Fatal("the context does not end Check")
		}
	}
	// Before the first problem: at the start, and at each of the segments that decide reads.
	if segs := len(logNames(t, fsys)); ends[true] != 1+segs || ends[false] == 0 {
		t.Errorf("the context ended Check before the first problem %d times (%d segments), after it %d times", ends[true], segs, ends[false])
	}
}

// TestInspectCrashes: Check reports every change that Open makes to recover the store that a crash leaves, and nothing
// else that is not a difference of head, and finds nothing wrong in what Open recovers.
func TestInspectCrashes(t *testing.T) {
	ctx := context.Background()
	o := Options{Placement: Hybrid, Threshold: 100, SegmentBytes: 300, CheckpointBytes: -1}
	setup := func() (*simfs.FS, *DB) {
		fsys, db := newSim(t, o)
		for i := range 3 {
			change(t, db, i)
		}
		if err := db.Checkpoint(); err != nil {
			t.Fatal(err)
		}
		return fsys, db
	}
	step := func(db *DB) error {
		for i := 3; i < 6; i++ {
			if err := db.Update(ctx, changeF(i)); err != nil {
				return err
			}
		}
		return nil
	}
	fsys, db := setup()
	start := fsys.Counted()
	if err := step(db); err != nil {
		t.Fatal(err)
	}
	ops := fsys.Counted() - start
	repaired, sound := 0, 0
	for k := range ops + 1 {
		fsys, db := setup()
		fsys.CrashAt(fsys.Counted() + k)
		_ = step(db)
		for _, m := range simfs.Models(fsys.Pending(), 4, nil) {
			crashed := fsys.Crash(m)
			ps := inspect(t, crashed, o)
			opened, err := Open(copyFS(crashed), o)
			if err != nil {
				if len(ps) == 0 {
					t.Errorf("after %d operations, %s: Open fails with %v, and Check finds nothing", k, m.Name, err)
				}
				continue
			}
			info := opened.Recovery()
			if err := opened.Verify(ctx); err != nil {
				t.Errorf("after %d operations, %s: Verify: %v", k, m.Name, err)
			}
			closeDB(t, opened)
			repairs := info.TruncatedAt >= 0 || info.RemovedSegment != "" || len(info.IgnoredGenerations) > 0 ||
				strings.HasPrefix(info.Checkpoint, "damaged") || info.Checkpoint == "stale"
			if repairs {
				repaired++
			}
			if len(ps) == 0 {
				sound++
			}
			if repairs && len(ps) == 0 {
				t.Errorf("after %d operations, %s: Open recovers %+v, and Check finds nothing", k, m.Name, info)
			}
			for _, p := range ps {
				if p.Path != path.Join(simDir, "head") && !strings.Contains(p.Reason, "Open ") {
					t.Errorf("after %d operations, %s: the problem %q", k, m.Name, p)
				}
			}
		}
	}
	if repaired == 0 || sound == 0 {
		t.Errorf("%d crashes that Open repairs, %d that it does not", repaired, sound)
	}
	t.Logf("%d crash points, %d crashes that Open repairs, %d that it does not", ops+1, repaired, sound)
}
