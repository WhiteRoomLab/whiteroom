package logdb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// compactDB compacts db with r, holding the semaphore of Updates as maintenance does.
func compactDB(db *DB, r Rewrite) error {
	return compactCtx(context.Background(), db, r)
}

func compactCtx(ctx context.Context, db *DB, r Rewrite) error {
	db.sem <- struct{}{}
	defer func() { <-db.sem }()
	return db.compact(ctx, r)
}

func gensOf(t *testing.T, fsys vfs.FS) []uint64 {
	t.Helper()
	var gens []uint64
	for _, name := range logNames(t, fsys) {
		g, _, _ := parseSegName(path.Base(name))
		if gen := uint64(g); len(gens) == 0 || gens[len(gens)-1] != gen {
			gens = append(gens, gen)
		}
	}
	return gens
}

func recordsOf(t *testing.T, fsys vfs.FS) int {
	t.Helper()
	n := 0
	for _, name := range logNames(t, fsys) {
		_, offs := frames(t, fsys, name)
		n += len(offs)
	}
	return n
}

// hasTable reports whether the dump d has keys of table.
func hasTable(d string, table uint32) bool {
	for _, line := range strings.Split(d, "\n") {
		if strings.HasPrefix(line, fmt.Sprintf("%d/", table)) {
			return true
		}
	}
	return false
}

func baseRecords(t *testing.T, fsys vfs.FS) int {
	t.Helper()
	n := 0
	for _, name := range logNames(t, fsys) {
		b := fileData(t, fsys, name)
		_, offs := frames(t, fsys, name)
		for _, off := range offs {
			if recType(b[off+8]) == recBase {
				n++
			}
		}
	}
	return n
}

// withBaseBytes sets baseBytes to n until the test ends.
func withBaseBytes(t *testing.T, n int) {
	old := baseBytes
	baseBytes = n
	t.Cleanup(func() { baseBytes = old })
}

func TestCompactKeepsState(t *testing.T) {
	withBaseBytes(t, 300) // several base records
	ctx := context.Background()
	for _, c := range []struct {
		name string
		o    Options
	}{
		{"inline", Options{SegmentBytes: 400}},
		{"files", Options{Placement: Files, SegmentBytes: 400}},
		{"hybrid", Options{Placement: Hybrid, Threshold: 100, SegmentBytes: 400}},
	} {
		t.Run(c.name, func(t *testing.T) {
			fsys, db := newSim(t, c.o)
			for i := range 8 {
				change(t, db, i)
			}
			before, stats, seq, old := stateOf(t, db), db.Stats(), seqOf(t, db), logNames(t, fsys)
			// A View that took its snapshot before the compaction reads its objects after the old segments are removed.
			read, compacted, done := make(chan struct{}), make(chan struct{}), make(chan error)
			go func() {
				done <- db.View(ctx, func(tx *Tx) error {
					_ = tx.Seq()
					close(read)
					<-compacted
					var err error
					tx.Objects(func(h [32]byte, size int64) bool {
						var data []byte
						if data, err = tx.Object(h); err == nil && sha256.Sum256(data) != h {
							err = fmt.Errorf("object %x: another hash", h)
						}
						return err == nil
					})
					return err
				})
			}()
			// So does an Events of the old state.
			evRead, evDone := make(chan struct{}), make(chan error)
			var evLines []string
			go func() {
				evDone <- db.Events(ctx, func(e Event) bool {
					if len(evLines) == 0 {
						close(evRead)
						<-compacted
					}
					evLines = append(evLines, fmt.Sprintf("%d/%d=%s", e.Number, e.Seq, e.Data))
					return true
				})
			}()
			<-read
			<-evRead
			err := compactDB(db, Rewrite{})
			close(compacted)
			if verr := <-done; verr != nil {
				t.Errorf("a View of the old state: %v", verr)
			}
			if everr := <-evDone; everr != nil || "\nevents:\n"+strings.Join(evLines, "\n") != before[strings.Index(before, "\nevents:\n"):] {
				t.Errorf("Events of the old state: %v\n%s", everr, strings.Join(evLines, "\n"))
			}
			if err != nil {
				t.Fatal(err)
			}
			names := logNames(t, fsys)
			if !slices.Equal(gensOf(t, fsys), []uint64{2}) || len(names) < 2 || baseRecords(t, fsys) < 3 {
				t.Errorf("segments %q, %d base records", names, baseRecords(t, fsys))
			}
			for _, name := range old {
				if exists(t, fsys, name) || exists(t, fsys.Crash(loseAll), name) {
					t.Errorf("%s is left", name)
				}
			}
			if got := stateOf(t, db); got != before {
				t.Errorf("state:\n%s\nwant:\n%s", got, before)
			}
			var size int64
			for _, name := range names {
				size += fileSize(t, fsys, name)
			}
			if h, st := readHeadFile(t, fsys), db.cur.Load(); h.gen != 2 || uint64(h.seq) != seq || h.seg != st.seg || h.end != st.end ||
				h.crc != st.lastCRC {
				t.Errorf("head after the compaction: %+v", h)
			}
			s := db.Stats()
			if st := db.cur.Load(); st.gen != 2 || uint64(st.seq) != seq || s.LogBytes != size || s.Segments != len(names) {
				t.Errorf("generation %d, seq %d, Stats %+v", st.gen, st.seq, s)
			}
			s.LogBytes, s.Segments = stats.LogBytes, stats.Segments
			if fmt.Sprint(s) != fmt.Sprint(stats) {
				t.Errorf("Stats:\n%+v\nwant:\n%+v", s, stats)
			}
			// The checkpoint is of the base-end.
			h := ckpOf(t, fsys)
			b := fileData(t, fsys, segName(2, h.seg))
			sh, _ := frames(t, fsys, segName(2, h.seg))
			body, _, reason := parseFrame(b[h.off:], sh.salt)
			if rec, _ := parseBody(body); h.gen != 2 || uint64(h.seq) != seq || reason != "" || rec.typ != recBaseEnd {
				t.Errorf("checkpoint %+v of a record of type %d", h, rec.typ)
			}
			// Commits go on at S+1.
			change(t, db, 8)
			if seqOf(t, db) != seq+1 {
				t.Errorf("seq %d", seqOf(t, db))
			}
			after := stateOf(t, db)
			crashed := fsys.Crash(loseAll)
			db = reopen(t, crashed, c.o)
			if info := db.Recovery(); !equalInfo(info, RecoveryInfo{Generation: 2, Seq: seq + 1, Checkpoint: "used", Replayed: 1, TruncatedAt: -1}) {
				t.Errorf("Recovery: %+v", info)
			}
			if got := stateOf(t, db); got != after {
				t.Errorf("after reopening:\n%s\nwant:\n%s", got, after)
			}
			// The generation replays from its start: its base, then the commit.
			full := copyFS(crashed)
			dropCheckpoint(t, full)
			db = reopen(t, full, c.o)
			if info := db.Recovery(); info.Generation != 2 || info.Checkpoint != "none" || info.Replayed != recordsOf(t, full) {
				t.Errorf("Recovery without the checkpoint: %+v", info)
			}
			if got := stateOf(t, db); got != after {
				t.Errorf("from the base:\n%s\nwant:\n%s", got, after)
			}
			// A compaction of generation 2.
			if err := compactDB(db, Rewrite{}); err != nil {
				t.Fatal(err)
			}
			if got := stateOf(t, db); got != after || !slices.Equal(gensOf(t, full), []uint64{3}) {
				t.Errorf("generations %d:\n%s", gensOf(t, full), got)
			}
			closeDB(t, db)
			db = reopen(t, full, c.o)
			if got := stateOf(t, db); got != after || db.Recovery().Generation != 3 {
				t.Errorf("generation 3 after reopening: %+v\n%s", db.Recovery(), got)
			}
		})
	}
	t.Run("an empty store", func(t *testing.T) {
		fsys, db := newSim(t, Options{})
		counted := fsys.Counted()
		if err := compactDB(db, Rewrite{}); err != nil || fsys.Counted() != counted {
			t.Errorf("compact of no commit: %v, %d operations", err, fsys.Counted()-counted)
		}
		// A state without keys, objects and events is a base of no records.
		putKey(t, db, 1, "a", "1")
		delKey(t, db, 1, "a")
		if err := compactDB(db, Rewrite{}); err != nil {
			t.Fatal(err)
		}
		if recordsOf(t, fsys) != 1 || dump(t, db) != "" || seqOf(t, db) != 2 {
			t.Errorf("%d records, seq %d", recordsOf(t, fsys), seqOf(t, db))
		}
		closeDB(t, db)
		dropCheckpoint(t, fsys)
		if db := reopen(t, fsys, Options{}); seqOf(t, db) != 2 || db.Recovery().Generation != 2 {
			t.Errorf("Recovery: %+v", db.Recovery())
		}
	})
}

// TestCompactRewrite shows that a compaction can remove data from the files of the store: a value that it changes, an
// event and an object that it drops are in none of them afterwards, but the file of a dropped object, which GC
// removes.
func TestCompactRewrite(t *testing.T) {
	const marker = "secret-4c1e"
	ctx := context.Background()
	for _, onOS := range []bool{false, true} {
		t.Run(fmt.Sprintf("os %v", onOS), func(t *testing.T) {
			var fsys vfs.FS
			sim, dir := simfs.New(simDir), t.TempDir()
			fsys = sim
			if onOS {
				fsys = osFS(t, dir)
			}
			o := Options{Placement: Hybrid, Threshold: 100, SegmentBytes: 400}
			db, err := Create(fsys, o)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			inline, file, kept := []byte("inline "+marker), []byte(strings.Repeat("file "+marker+" ", 10)), objData(5, 150)
			ih, fh, kh := sha256.Sum256(inline), sha256.Sum256(file), sha256.Sum256(kept)
			if err := db.Update(ctx, func(tx *Tx) error {
				var errs []error
				for _, d := range [][]byte{inline, file, kept} {
					_, err := tx.PutObject(d)
					errs = append(errs, err)
				}
				return errors.Join(append(errs, tx.Put(1, []byte("alice"), []byte("name: "+marker)),
					tx.Put(1, []byte("bob"), []byte("name: bob")), tx.AppendEvent([]byte("login "+marker)),
					tx.AppendEvent([]byte("logout bob")))...)
			}); err != nil {
				t.Fatal(err)
			}
			for i := range 5 {
				change(t, db, i)
			}
			if err := db.Update(ctx, appendF("last "+marker)); err != nil {
				t.Fatal(err)
			}
			if err := db.Checkpoint(); err != nil { // it has the marker too
				t.Fatal(err)
			}
			err = compactDB(db, Rewrite{
				Value: func(_ uint32, _, value []byte) ([]byte, bool) {
					if bytes.Contains(value, []byte(marker)) {
						return []byte("name: [redacted]"), true
					}
					return value, true
				},
				Event:  func(e Event) ([]byte, bool) { return e.Data, !bytes.Contains(e.Data, []byte(marker)) },
				Object: func(h [32]byte) bool { return h != ih && h != fh },
			})
			if err != nil {
				t.Fatal(err)
			}
			check := func(db *DB) {
				t.Helper()
				got := dump(t, db)
				if !strings.Contains(got, "1/alice=name: [redacted]\n") || strings.Contains(got, marker) {
					t.Errorf("dump:\n%s", got)
				}
				events := eventLines(t, db)
				if strings.Contains(events, marker) || !strings.HasPrefix(events, "2/1=logout bob\n3/2=event 0") {
					t.Errorf("events:\n%s", events)
				}
				if err := db.View(ctx, func(tx *Tx) error {
					for _, h := range [][32]byte{ih, fh} {
						if _, err := tx.Object(h); tx.HasObject(h) || !errors.Is(err, fs.ErrNotExist) {
							t.Errorf("the dropped object %x: %v", h, err)
						}
					}
					got, err := tx.Object(kh)
					if err == nil && !bytes.Equal(got, kept) {
						err = errors.New("other data")
					}
					return err
				}); err != nil {
					t.Error(err)
				}
				checkObjects(t, db)
			}
			check(db)
			// The marker is in the file of the dropped file object only.
			var with []string
			if onOS {
				err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
					if err != nil || d.IsDir() {
						return err
					}
					b, err := os.ReadFile(p)
					if bytes.Contains(b, []byte(marker)) {
						rel, _ := filepath.Rel(dir, p)
						with = append(with, filepath.ToSlash(rel))
					}
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				for _, w := range []*simfs.FS{sim, sim.Crash(loseAll)} {
					w.Walk(func(p string, data []byte) {
						if bytes.Contains(data, []byte(marker)) {
							with = append(with, p)
						}
					})
				}
				with = slices.Compact(with)
			}
			if !slices.Equal(with, []string{objName(fh)}) {
				t.Errorf("the marker is in %q", with)
			}
			// New events go on after the last number, which was dropped.
			if err := db.Update(ctx, appendF("next")); err != nil {
				t.Fatal(err)
			}
			if events := eventLines(t, db); !strings.HasSuffix(events, "\n7/6=event 4\n9/8=next") {
				t.Errorf("events:\n%s", events)
			}
			closeDB(t, db)
			db = reopen(t, fsys, o)
			check(db)
		})
	}
}

// TestCompactHooks checks the points of a compaction, and that the hooks after its base-end can only block.
func TestCompactHooks(t *testing.T) {
	withBaseBytes(t, 200)
	hookErr := errors.New("hook failed")
	var points []string
	fail := ""
	o := Options{SegmentBytes: 400, Hook: func(p string) error {
		points = append(points, p)
		if p == fail {
			return hookErr
		}
		return nil
	}}
	for _, p := range []string{"", "compact:base-end", "checkpoint:renamed", "checkpoint:done", "compact:checkpoint",
		"compact:delete"} {
		fsys, db := newSim(t, o)
		for i := range 8 {
			change(t, db, i)
		}
		points, fail = nil, p
		old := len(logNames(t, fsys))
		if err := compactDB(db, Rewrite{}); err != nil {
			t.Errorf("compact with the hook %s failing: %v", p, err)
		}
		fail = ""
		want := []string{"compact:base", "compact:base-end", "checkpoint:synced", "checkpoint:renamed", "checkpoint:done",
			"compact:checkpoint"}
		for range old {
			want = append(want, "compact:delete")
		}
		if !slices.Equal(points, want) || !slices.Equal(gensOf(t, fsys), []uint64{2}) {
			t.Errorf("with the hook %s failing: points %q, generations %d", p, points, gensOf(t, fsys))
		}
	}
}

// TestCompactDamagedObject checks that a compaction does not copy a damaged object into a record with a valid CRC.
func TestCompactDamagedObject(t *testing.T) {
	fsys, db := newSim(t, Options{})
	data := objData(1, 300)
	h := putObjects(t, db, data)[0]
	putKey(t, db, 1, "k", "v")
	b := fileData(t, fsys, seg1)
	b[inlineOff(db, h)+10] ^= 1
	setFile(t, fsys, seg1, b)
	changed := sha256.Sum256(func() []byte { d := slices.Clone(data); d[10] ^= 1; return d }())
	err := compactDB(db, Rewrite{})
	if want := fmt.Sprintf("object %x: the data has the hash %x", h, changed); err == nil || err.Error() != want {
		t.Errorf("compact: %v, want %s", err, want)
	}
	putKey(t, db, 1, "after", "v") // the store goes on in generation 1
	if st := db.cur.Load(); st.gen != 1 || st.seq != 3 {
		t.Errorf("generation %d, seq %d", st.gen, st.seq)
	}
}

// TestIncompleteGenerationIgnored crashes a compaction after its base records, before its base-end: Open goes on in
// the old generation and ignores the new one, which the next compaction removes.
func TestIncompleteGenerationIgnored(t *testing.T) {
	withBaseBytes(t, 200)
	fsys := simfs.New(simDir)
	armed := false
	o := Options{SegmentBytes: 400, Placement: Hybrid, Threshold: 100, Hook: func(p string) error {
		if armed && p == "compact:base" {
			fsys.CrashAt(fsys.Counted())
		}
		return nil
	}}
	db, err := Create(fsys, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for i := range 8 {
		change(t, db, i)
	}
	if err := db.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	before := stateOf(t, db)
	drop := Rewrite{Value: func(table uint32, _, value []byte) ([]byte, bool) { return value, table != 2 }}
	armed = true
	if err := compactDB(db, drop); !errors.Is(err, simfs.ErrCrashed) {
		t.Fatalf("compact: %v", err)
	}
	crashed := fsys.Crash(loseAll)
	if gens := gensOf(t, crashed); !slices.Equal(gens, []uint64{1, 2}) || !exists(t, crashed, segName(2, 2)) {
		t.Fatalf("segments %q", logNames(t, crashed))
	}
	o.Hook = nil
	db = reopen(t, crashed, o)
	if info := db.Recovery(); info.Generation != 1 || info.Checkpoint != "used" ||
		!slices.Equal(info.IgnoredGenerations, []uint64{2}) {
		t.Errorf("Recovery: %+v", info)
	}
	if got := stateOf(t, db); got != before {
		t.Errorf("state:\n%s\nwant:\n%s", got, before)
	}
	change(t, db, 8) // the store goes on in generation 1
	if err := compactDB(db, drop); err != nil {
		t.Fatal(err)
	}
	want := stateOf(t, db)
	if hasTable(dump(t, db), 2) || !slices.Equal(gensOf(t, crashed), []uint64{2}) {
		t.Errorf("generations %d:\n%s", gensOf(t, crashed), want)
	}
	closeDB(t, db)
	db = reopen(t, crashed, o)
	if info := db.Recovery(); info.Generation != 2 || len(info.IgnoredGenerations) != 0 || stateOf(t, db) != want {
		t.Errorf("Recovery: %+v", info)
	}
}

// TestCompactFaults checks what a compaction that fails leaves: before its base-end is written, the old generation
// and a store that goes on, unless a sync failed; from then on, a store that has stopped writing. Either way, Open
// finds the old or the new generation whole.
func TestCompactFaults(t *testing.T) {
	withBaseBytes(t, 200)
	hookErr := errors.New("hook failed")
	gen2 := func(kind simfs.OpKind) func(simfs.Op) bool {
		return func(op simfs.Op) bool {
			return op.Kind == kind && strings.HasPrefix(op.Path, "log/0000000000000002-")
		}
	}
	gen1 := func(kind simfs.OpKind) func(simfs.Op) bool {
		return func(op simfs.Op) bool {
			return op.Kind == kind && strings.HasPrefix(op.Path, "log/0000000000000001-")
		}
	}
	records := func(op simfs.Op) bool { return gen2(simfs.OpWrite)(op) && op.Off > 0 }
	for _, c := range []struct {
		name   string
		fault  simfs.Fault
		after  string // the fault fires from this hook point on
		hook   string // that fails instead
		failed bool
		gen    uint64 // in memory afterwards, and after a crash that loses what is not durable
		gens   []uint64
	}{
		{name: "create", fault: simfs.Fault{Match: gen2(simfs.OpCreate), Err: syscall.ENOSPC}, gen: 1},
		{name: "header write", fault: simfs.Fault{Match: gen2(simfs.OpWrite), Err: syscall.EIO}, gen: 1},
		{name: "base write", fault: simfs.Fault{Match: records, Err: syscall.ENOSPC, Short: 7}, gen: 1},
		{name: "rollover", fault: simfs.Fault{Match: on(simfs.OpCreate, segName(2, 2)), Err: syscall.ENOSPC}, gen: 1},
		{name: "hook compact:base", hook: "compact:base", gen: 1},
		{name: "sync of a segment", fault: simfs.Fault{Match: gen2(simfs.OpSync), Err: syscall.EIO}, failed: true, gen: 1},
		{name: "sync of log", fault: simfs.Fault{Match: on(simfs.OpSyncDir, "log"), Err: syscall.EIO}, failed: true, gen: 1},
		{name: "base-end write", fault: simfs.Fault{Match: records, Err: syscall.EIO}, after: "compact:base", failed: true,
			gen: 1},
		{name: "base-end sync", fault: simfs.Fault{Match: gen2(simfs.OpSync), Err: syscall.EIO, Drop: true},
			after: "compact:base", failed: true, gen: 1},
		{name: "checkpoint", fault: simfs.Fault{Match: ckpTemp(simfs.OpSync), Err: syscall.EIO}, gen: 2, gens: []uint64{1, 2}},
		{name: "delete", fault: simfs.Fault{Match: gen1(simfs.OpRemove), Err: syscall.EIO}, gen: 2},
		{name: "sync of log after the deletes", fault: simfs.Fault{Match: on(simfs.OpSyncDir, "log"), Err: syscall.EIO},
			after: "compact:delete", failed: true, gen: 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			armed, reached := false, false
			o := Options{SegmentBytes: 400, Placement: Hybrid, Threshold: 100, Hook: func(p string) error {
				if armed && p == c.after {
					reached = true
				}
				if armed && p == c.hook {
					return hookErr
				}
				return nil
			}}
			fsys, db := newSim(t, o)
			for i := range 8 {
				change(t, db, i)
			}
			before := stateOf(t, db)
			drop := Rewrite{Value: func(table uint32, _, value []byte) ([]byte, bool) { return value, table != 2 }}
			cause := hookErr
			if c.fault.Match != nil {
				f := c.fault
				f.Times, cause = 1, f.Err
				if c.after != "" {
					match := f.Match
					f.Match = func(op simfs.Op) bool { return reached && match(op) }
				}
				fsys.Inject(f)
			}
			armed = true
			err := compactDB(db, drop)
			armed = false
			if c.failed {
				checkFailed(t, db, err, cause)
				if again := compactDB(db, drop); again == nil || again.Error() != err.Error() {
					t.Errorf("a compaction after the failure: %v", again)
				}
			} else if !errors.Is(err, cause) || errors.Is(err, ErrFailed) {
				t.Fatalf("compact: %v", err)
			}
			if h := readHeadFile(t, fsys); !c.failed && uint64(h.gen) != c.gen {
				t.Errorf("head of generation %d", h.gen)
			}
			if st := db.cur.Load(); uint64(st.gen) != c.gen || c.gen == 1 && stateOf(t, db) != before ||
				c.gen == 2 && hasTable(dump(t, db), 2) {
				t.Errorf("generation %d:\n%s", st.gen, stateOf(t, db))
			}
			if c.gens != nil && !slices.Equal(gensOf(t, fsys), c.gens) {
				t.Errorf("generations %d", gensOf(t, fsys))
			}
			if !c.failed {
				change(t, db, 8) // the store goes on
			}
			after := stateOf(t, db)
			for _, m := range []simfs.Model{keepAll(fsys), loseAll} {
				db, err := Open(fsys.Crash(m), Options{})
				if err != nil {
					t.Fatalf("after the crash %s: Open: %v", m.Name, err)
				}
				gen, got := db.Recovery().Generation, stateOf(t, db)
				switch {
				case gen == c.gen && got != after:
					t.Errorf("after the crash %s:\n%s\nwant:\n%s", m.Name, got, after)
				case gen != c.gen && (m.Name == "lose-all" || gen != 2 || hasTable(dump(t, db), 2)):
					t.Errorf("after the crash %s: generation %d:\n%s", m.Name, gen, got)
				}
				if err := compactDB(db, drop); err != nil { // the next compaction works
					t.Errorf("after the crash %s: compact: %v", m.Name, err)
				}
				closeDB(t, db)
			}
		})
	}
	t.Run("canceled", func(t *testing.T) {
		fsys, db := newSim(t, Options{SegmentBytes: 400})
		for i := range 8 {
			change(t, db, i)
		}
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		counted := fsys.Counted()
		if err := compactCtx(canceled, db, Rewrite{}); !errors.Is(err, context.Canceled) || fsys.Counted() != counted {
			t.Errorf("compact with a canceled context: %v, %d operations", err, fsys.Counted()-counted)
		}
		// ctx is checked at the start, and before each base record and each record of the events.
		if err := compactCtx(&countdownContext{Context: context.Background(), left: 2}, db, Rewrite{}); !errors.Is(err, context.Canceled) {
			t.Errorf("compact with a context that ends: %v", err)
		}
		if n := baseRecords(t, fsys); n != 1 {
			t.Errorf("%d base records before the end of the context", n)
		}
		change(t, db, 8)
		if st := db.cur.Load(); st.gen != 1 || !slices.Equal(gensOf(t, fsys), []uint64{1, 2}) {
			t.Errorf("generation %d, generations %d", st.gen, gensOf(t, fsys))
		}
		closeDB(t, db)
		db = reopen(t, fsys, Options{})
		if info := db.Recovery(); info.Generation != 1 || !slices.Equal(info.IgnoredGenerations, []uint64{2}) {
			t.Errorf("Recovery: %+v", info)
		}
	})
	t.Run("a base record too large", func(t *testing.T) {
		defer func(n int) { maxBody = n }(maxBody)
		maxBody = 400
		_, db := newSim(t, Options{})
		putKey(t, db, 1, "k", strings.Repeat("v", 380)) // a commit of 397 bytes, whose base record has 402
		err := compactDB(db, Rewrite{Value: func(_ uint32, _, v []byte) ([]byte, bool) { return append(v, "more"...), true }})
		if err == nil || !strings.HasPrefix(err.Error(), "a base record is too large: ") {
			t.Errorf("compact: %v", err)
		}
		putKey(t, db, 1, "after", "v")
	})
}

// TestCrashDuringCompaction crashes after each operation of a compaction, and checks the crash models that simfs.Models
// gives: Open finds generation 1 with the old state or generation 2 with the rewritten one, never a mix.
func TestCrashDuringCompaction(t *testing.T) {
	withBaseBytes(t, 200)
	rewrite := Rewrite{
		Value: func(table uint32, key, value []byte) ([]byte, bool) {
			if table == 1 && string(key) == "k05" {
				return []byte("redacted"), true
			}
			return value, table != 2
		},
		Event: func(e Event) ([]byte, bool) { return e.Data, e.Number != 2 },
	}
	for _, c := range []struct {
		name     string
		o        Options
		leftover bool // a compaction that crashed has left an incomplete generation 2
	}{
		{"segments", Options{SegmentBytes: 400, Placement: Hybrid, Threshold: 100}, false},
		{"over an incomplete generation", Options{SegmentBytes: 400, Placement: Hybrid, Threshold: 100}, true},
		{"one segment", Options{Placement: Hybrid, Threshold: 100}, false},
	} {
		o, leftover := c.o, c.leftover
		t.Run(c.name, func(t *testing.T) {
			setup := func() (*simfs.FS, *DB) {
				fsys, db := newSim(t, o)
				for i := range 8 {
					change(t, db, i)
				}
				// Closed and opened again: the compaction begins with a checkpoint, and nothing that is not durable.
				closeDB(t, db)
				db = reopen(t, fsys, o)
				if !leftover {
					return fsys, db
				}
				// A compaction that crashed after its base records.
				armed := true
				db.o.Hook = func(p string) error {
					if armed && p == "compact:base" {
						fsys.CrashAt(fsys.Counted())
					}
					return nil
				}
				if err := compactDB(db, rewrite); !errors.Is(err, simfs.ErrCrashed) {
					t.Fatalf("compact: %v", err)
				}
				armed = false
				fsys = fsys.Crash(loseAll)
				db = reopen(t, fsys, o)
				if ignored := db.Recovery().IgnoredGenerations; !slices.Equal(ignored, []uint64{2}) {
					t.Fatalf("ignored %d", ignored)
				}
				return fsys, db
			}
			state := func(fsys *simfs.FS) (uint64, string) {
				db, err := Open(fsys, o)
				if err != nil {
					t.Fatalf("Open: %v", err)
				}
				defer closeDB(t, db)
				return db.Recovery().Generation, stateOf(t, db)
			}
			fsys, db := setup()
			before, start := stateOf(t, db), fsys.Counted()
			if err := compactDB(db, rewrite); err != nil {
				t.Fatal(err)
			}
			ops := fsys.Counted() - start
			gen, after := state(copyFS(fsys))
			if gen != 2 || after == before {
				t.Fatalf("generation %d", gen)
			}
			crashes := 0
			for k := range ops + 1 {
				fsys, db := setup()
				fsys.CrashAt(fsys.Counted() + k)
				err := compactDB(db, rewrite)
				for _, m := range simfs.Models(fsys.Pending(), 4, nil) {
					gen, got := state(fsys.Crash(m))
					if whole := gen == 1 && got == before || gen == 2 && got == after; !whole {
						t.Errorf("a crash after %d of %d operations (%v), %s: generation %d:\n%s", k, ops, err, m.Name, gen, got)
					}
					crashes++
				}
			}
			t.Logf("%d crash points, %d crashes", ops+1, crashes)
		})
	}
}
