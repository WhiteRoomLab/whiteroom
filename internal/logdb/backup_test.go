package logdb

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// pairF returns a function for Update that makes the commit n: a pair of keys, an object and an event, all with n.
func pairF(n int) func(*Tx) error {
	return func(tx *Tx) error {
		v := []byte(strconv.Itoa(n))
		_, err := tx.PutObject(pairObject(n))
		return errors.Join(err, tx.Put(1, []byte("a"), v), tx.Put(2, []byte("b"), v), tx.AppendEvent(v))
	}
}

func pairObject(n int) []byte {
	return objData(n, 40+n%3*40)
}

// checkPairs checks that db has the state after the commits 1..n of pairF.
func checkPairs(t *testing.T, db *DB, n int) {
	t.Helper()
	v := strconv.Itoa(n)
	if got, want := dump(t, db), "1/a="+v+"\n2/b="+v; n > 0 && got != want {
		t.Errorf("dump:\n%s\nwant:\n%s", got, want)
	}
	if err := db.View(context.Background(), func(tx *Tx) error {
		count := 0
		tx.Objects(func([32]byte, int64) bool { count++; return true })
		if count != n {
			return fmt.Errorf("%d objects", count)
		}
		for i := 1; i <= n; i++ {
			h := sha256.Sum256(pairObject(i))
			if data, err := tx.Object(h); err != nil || sha256.Sum256(data) != h {
				return fmt.Errorf("object %d: %w", i, err)
			}
		}
		return nil
	}); err != nil {
		t.Error(err)
	}
	events := 0
	if err := db.Events(context.Background(), func(e Event) bool {
		events++
		if string(e.Data) != strconv.Itoa(int(e.Seq)) {
			t.Errorf("event %+v", e)
		}
		return true
	}); err != nil || events != n {
		t.Errorf("Events: %d, %v", events, err)
	}
	if err := db.Verify(context.Background()); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

// TestBackupWhileWriting takes backups while the writer commits: each copy opens to the state of one commit, the one
// of its position.
func TestBackupWhileWriting(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name string
		o    Options
	}{
		{"inline", Options{SegmentBytes: 1024, CheckpointBytes: 2048}},
		{"hybrid", Options{Placement: Hybrid, Threshold: 80, SegmentBytes: 1024, CheckpointBytes: 2048}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "store")
			if err := os.Mkdir(src, 0o700); err != nil {
				t.Fatal(err)
			}
			db, err := Create(osFS(t, src), c.o)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			var stop atomic.Bool
			done := make(chan error, 1)
			var commits atomic.Int64
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
			type copied struct {
				p   Position
				dir string
			}
			var copies []copied
			for i := range 5 {
				for commits.Load() < int64(10*i) {
					time.Sleep(time.Millisecond)
				}
				dst := filepath.Join(dir, fmt.Sprintf("copy-%d", i))
				if err := os.Mkdir(dst, 0o700); err != nil {
					t.Fatal(err)
				}
				p, err := Backup(ctx, osFS(t, src), osFS(t, dst), nil)
				if err != nil {
					t.Fatal(err)
				}
				copies = append(copies, copied{p, dst})
			}
			stop.Store(true)
			if err := wait(t, done, "writer"); err != nil {
				t.Fatal(err)
			}
			for _, cp := range copies {
				db := reopen(t, osFS(t, cp.dir), c.o)
				if info := db.Recovery(); info.Seq != cp.p.Seq || info.TruncatedAt != -1 {
					t.Errorf("copy at %+v: %+v", cp.p, info)
				}
				checkPairs(t, db, int(cp.p.Seq))
				closeDB(t, db)
			}
			t.Logf("positions %v", func() []uint64 {
				var seqs []uint64
				for _, cp := range copies {
					seqs = append(seqs, cp.p.Seq)
				}
				return seqs
			}())
		})
	}
}

// TestBackupCopiesCheckpointAndObjects copies the files of the objects, and the checkpoint only if it pairs with the
// log up to the position: not one that the writer has written after it.
func TestBackupCopiesCheckpointAndObjects(t *testing.T) {
	ctx := context.Background()
	o := Options{Placement: Files, CheckpointBytes: -1, SegmentBytes: 600}
	fsys, db := newSim(t, o)
	for i := range 5 {
		change(t, db, i)
	}
	if err := db.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	change(t, db, 5)
	change(t, db, 6)
	st := db.cur.Load()
	want := Position{Generation: 1, Segment: st.seg, End: st.end, Seq: 7, CRC: st.lastCRC}
	dst := simfs.New("/backup")
	p, err := Backup(ctx, fsys, dst, func(p Position) error {
		if p != want {
			return fmt.Errorf("position %+v, want %+v", p, want)
		}
		return nil
	})
	if err != nil || p != want {
		t.Fatalf("Backup: %+v, %v", p, err)
	}
	if st.seg < 2 {
		t.Fatalf("one segment")
	}
	state := stateOf(t, db)
	for _, c := range []*simfs.FS{dst, dst.Crash(loseAll)} {
		copyDB := reopen(t, c, o)
		if info := copyDB.Recovery(); info.Checkpoint != "used" || info.Seq != 7 || info.Replayed != 2 {
			t.Errorf("Recovery of the copy: %+v", info)
		}
		if got := stateOf(t, copyDB); got != state {
			t.Errorf("the copy has:\n%s\nwant:\n%s", got, state)
		}
		closeDB(t, copyDB)
	}
	// Only the files of the store are copied.
	if got := fileNames(dst); len(got) != len(fileNames(fsys)) {
		t.Errorf("files of the copy %v, of the store %v", got, fileNames(fsys))
	}

	// The writer commits and writes a checkpoint after the position: the copy has neither.
	dst = simfs.New("/backup2")
	p, err = Backup(ctx, fsys, dst, func(Position) error {
		putKey(t, db, 3, "x", "y")
		return db.Checkpoint()
	})
	if err != nil || p != want {
		t.Fatalf("Backup: %+v, %v", p, err)
	}
	if ckpOf(t, fsys).seq != 8 || fileSize(t, dst, segName(1, p.Segment)) != p.End ||
		fileSize(t, fsys, segName(1, p.Segment)) <= p.End {
		t.Errorf("the copy is not cut at the position: %d bytes of %d", fileSize(t, dst, segName(1, p.Segment)),
			fileSize(t, fsys, segName(1, p.Segment)))
	}
	copyDB := reopen(t, dst, o)
	if info := copyDB.Recovery(); info.Checkpoint != "none" || info.Seq != 7 {
		t.Errorf("Recovery of the copy: %+v", info)
	}
	if got := stateOf(t, copyDB); got != state {
		t.Errorf("the copy has:\n%s\nwant:\n%s", got, state)
	}

	// Without a writer, and after a compaction.
	if err := compactDB(db, Rewrite{}); err != nil {
		t.Fatal(err)
	}
	state = stateOf(t, db)
	closeDB(t, db)
	dst = simfs.New("/backup3")
	if p, err = Backup(ctx, fsys, dst, nil); err != nil || p.Generation != 2 || p.Seq != 8 {
		t.Fatalf("Backup: %+v, %v", p, err)
	}
	copyDB = reopen(t, dst, o)
	if info := copyDB.Recovery(); info.Checkpoint != "used" || info.Generation != 2 {
		t.Errorf("Recovery of the copy: %+v", info)
	}
	if got := stateOf(t, copyDB); got != state {
		t.Errorf("the copy has:\n%s\nwant:\n%s", got, state)
	}
}

// TestBackupDuringMaintenance: Maintain waits for a Backup, whose reader keeps it out.
func TestBackupDuringMaintenance(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "store")
	if err := os.Mkdir(src, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := Create(osFS(t, src), Options{})
	if err != nil {
		t.Fatal(err)
	}
	putKey(t, db, 1, "a", "1")
	closeDB(t, db)
	entered, release := make(chan struct{}), make(chan struct{})
	backup := make(chan error, 1)
	go func() {
		_, err := Backup(ctx, osFS(t, src), osFS(t, dir), func(Position) error {
			close(entered)
			<-release
			return nil
		})
		backup <- err
	}()
	wait(t, entered, "between")
	maintained := make(chan error, 1)
	go func() {
		maintained <- Maintain(ctx, osFS(t, src), Options{}, func(*Maintenance) error { return nil })
	}()
	select {
	case err := <-maintained:
		t.Fatalf("Maintain did not wait for the backup: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	// dir has the directory of the store: the backup fails, and Maintain goes on after it.
	if err := wait(t, backup, "Backup"); err == nil || err.Error() != dir+": not empty" {
		t.Errorf("Backup into a directory that is not empty: %v", err)
	}
	if err := wait(t, maintained, "Maintain"); err != nil {
		t.Error(err)
	}
}

func TestBackupErrors(t *testing.T) {
	ctx := context.Background()
	fsys, db := newSim(t, Options{Placement: Files})
	change(t, db, 0)
	h := sha256.Sum256(objData(0, 50))
	betweenErr := errors.New("between failed")
	if _, err := Backup(ctx, fsys, simfs.New("/backup"), func(Position) error { return betweenErr }); !errors.Is(err, betweenErr) {
		t.Errorf("Backup: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Backup(canceled, fsys, simfs.New("/backup"), nil); !errors.Is(err, context.Canceled) {
		t.Errorf("Backup with a canceled context: %v", err)
	}
	cc := &countdownContext{Context: ctx, left: 2}
	if _, err := Backup(cc, fsys, simfs.New("/backup"), nil); !errors.Is(err, context.Canceled) {
		t.Errorf("Backup that is canceled: %v", err)
	}
	setFile(t, fsys, objName(h), []byte(objectFile(objData(0, 49))))
	if _, err := Backup(ctx, fsys, simfs.New("/backup"), nil); err == nil ||
		err.Error() != simDir+"/"+objName(h)+": corrupt: has 65 bytes, want 66" {
		t.Errorf("Backup with a short object file: %v", err)
	}
	removeFile(t, fsys, objName(h))
	if _, err := Backup(ctx, fsys, simfs.New("/backup"), nil); err == nil || err.Error() != simDir+"/"+objName(h)+": missing file" {
		t.Errorf("Backup without an object file: %v", err)
	}
	if _, err := Backup(ctx, simfs.New("/empty"), simfs.New("/backup"), nil); err == nil ||
		err.Error() != "/empty/lock: missing directory" {
		t.Errorf("Backup of no store: %v", err)
	}
	// The reader of a Backup that failed is closed.
	closeDB(t, db)
	for _, name := range []string{"lock/entry", "lock/readers"} {
		lockFile(t, fsys, name, vfs.Exclusive)()
	}
}

// TestBackupCheckpointRule copies a checkpoint only if its record is in the copy of the log.
func TestBackupCheckpointRule(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name    string
		setup   func(t *testing.T, fsys *simfs.FS, db *DB)
		between func(t *testing.T, fsys *simfs.FS)
		want    string
	}{
		{"of the generation", nil, nil, "used"},
		{"of an older generation", func(t *testing.T, fsys *simfs.FS, db *DB) {
			// A compaction whose checkpoint failed leaves that of the old generation; Close would replace it.
			old := fileData(t, fsys, "checkpoint")
			fsys.Inject(simfs.Fault{Match: ckpTemp(simfs.OpSync), Err: errors.New("no"), Times: 1})
			if err := compactDB(db, Rewrite{}); err == nil {
				t.Fatal("the checkpoint did not fail")
			}
			closeDB(t, db)
			setFile(t, fsys, "checkpoint", old)
		}, nil, "none"},
		{"whose record is not in the log", nil, func(t *testing.T, fsys *simfs.FS) {
			setCkpHeader(t, fsys, func(h *ckpHeader) { h.recCRC++ })
		}, "none"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fsys, db := newSim(t, Options{CheckpointBytes: -1})
			change(t, db, 0)
			if err := db.Checkpoint(); err != nil { // its record is at the start of segment 1 of each generation
				t.Fatal(err)
			}
			change(t, db, 1)
			change(t, db, 2)
			if c.setup != nil {
				c.setup(t, fsys, db)
			}
			dst := simfs.New("/backup")
			if _, err := Backup(ctx, fsys, dst, func(Position) error {
				if c.between != nil {
					c.between(t, fsys)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if got := reopen(t, dst, Options{}).Recovery().Checkpoint; got != c.want {
				t.Errorf("the checkpoint of the copy is %q, want %q", got, c.want)
			}
		})
	}
}
