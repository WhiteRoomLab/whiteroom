package logdb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// change makes the Update i, with every kind of change: puts in two tables, a delete from i = 3 on, an object, which
// with Hybrid and a Threshold of 100 goes in a file for odd i, and an event.
func change(t *testing.T, db *DB, i int) {
	t.Helper()
	if err := db.Update(context.Background(), changeF(i)); err != nil {
		t.Fatal(err)
	}
}

func changeF(i int) func(*Tx) error {
	return func(tx *Tx) error {
		_, err := tx.PutObject(objData(i, 50+100*(i%2)))
		errs := []error{err, tx.Put(1, []byte(fmt.Sprintf("k%02d", i)), []byte(fmt.Sprintf("value %d", i))),
			tx.Put(2, []byte{byte(i)}, nil), tx.AppendEvent([]byte(fmt.Sprintf("event %d", i)))}
		if i >= 3 {
			errs = append(errs, tx.Delete(1, []byte(fmt.Sprintf("k%02d", i-3))))
		}
		return errors.Join(errs...)
	}
}

// ckpOf returns the header of the checkpoint of the store in fsys.
func ckpOf(t *testing.T, fsys vfs.FS) ckpHeader {
	t.Helper()
	h, _, err := parseCheckpoint(path.Join(fsys.Name(), "checkpoint"), fileData(t, fsys, "checkpoint"))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// setCkpHeader changes the header of the checkpoint of fsys with fn, and keeps its CRC valid.
func setCkpHeader(t *testing.T, fsys vfs.FS, fn func(h *ckpHeader)) {
	t.Helper()
	b := fileData(t, fsys, "checkpoint")
	h := ckpOf(t, fsys)
	fn(&h)
	setFile(t, fsys, "checkpoint", slices.Concat(appendCkpHeader(nil, h), b[ckpHeaderSize:]))
}

// rawCkp returns a checkpoint with the header h and body, whose length and CRC it sets.
func rawCkp(h ckpHeader, body []byte) []byte {
	h.bodyLen, h.bodyCRC = int64(len(body)), crc32c(body)
	return slices.Concat(appendCkpHeader(nil, h), body)
}

// uv returns the uvarints of vs.
func uv(vs ...uint64) []byte {
	var b []byte
	for _, v := range vs {
		b = binary.AppendUvarint(b, v)
	}
	return b
}

// kv returns a key and a plain value as a checkpoint body has them.
func kv(k, v string) []byte {
	return slices.Concat(uv(uint64(len(k))), []byte(k), []byte{envPlain}, uv(uint64(len(v))), []byte(v))
}

// ckpTemps returns the temporary files of checkpoints in fsys.
func ckpTemps(fsys *simfs.FS) []string {
	var names []string
	fsys.Walk(func(p string, _ []byte) {
		if strings.HasPrefix(p, ".tmp-checkpoint-") {
			names = append(names, p)
		}
	})
	return names
}

// ckpTemp returns a Fault.Match for the operations of kind on temporary files of checkpoints.
func ckpTemp(kind simfs.OpKind) func(simfs.Op) bool {
	return func(op simfs.Op) bool { return op.Kind == kind && strings.HasPrefix(op.Path, ".tmp-checkpoint-") }
}

// wait waits for ch, or fails the test after 10 s.
func wait[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("no %s", what)
	}
	panic("unreachable")
}

// TestCheckpointLayout pins the body of a checkpoint to the layout of concept section 3.5.
func TestCheckpointLayout(t *testing.T) {
	fsys, db := newSim(t, Options{Placement: Hybrid, Threshold: 100})
	inline, file := objData(1, 10), objData(2, 200)
	if err := db.Update(context.Background(), func(tx *Tx) error {
		_, err1 := tx.PutObject(inline)
		_, err2 := tx.PutObject(file)
		return errors.Join(err1, err2, tx.Put(7, []byte("k"), []byte("v")), tx.Put(3, []byte("b"), []byte("xy")),
			tx.Put(3, []byte("a"), nil), tx.AppendEvent([]byte("e")))
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	b := fileData(t, fsys, "checkpoint")
	want := ckpHeader{gen: 1, seq: 1, seg: 1, off: segHeaderSize, recCRC: crcAt(t, fsys, seg1, segHeaderSize),
		bodyLen: int64(len(b) - ckpHeaderSize), bodyCRC: crc32c(b[ckpHeaderSize:])}
	if h := ckpOf(t, fsys); h != want {
		t.Errorf("header %+v, want %+v", h, want)
	}
	ih, fh := sha256.Sum256(inline), sha256.Sum256(file)
	objs := [][]byte{
		slices.Concat(ih[:], []byte{placeInline}, uv(1, uint64(inlineOff(db, ih)), 10)),
		slices.Concat(fh[:], []byte{placeFile}, uv(200)),
	}
	if bytes.Compare(fh[:], ih[:]) < 0 {
		objs[0], objs[1] = objs[1], objs[0]
	}
	body := slices.Concat(uv(2, 1), uv(2), uv(3, 2), kv("a", ""), kv("b", "xy"), uv(7, 1), kv("k", "v"), uv(2),
		objs[0], objs[1])
	if !bytes.Equal(b[ckpHeaderSize:], body) {
		t.Errorf("body\n%x, want\n%x", b[ckpHeaderSize:], body)
	}
}

func TestCheckpointUsed(t *testing.T) {
	o := Options{SegmentBytes: 300, Placement: Hybrid, Threshold: 100}
	fsys, db := newSim(t, o)
	for i := range 6 {
		change(t, db, i)
	}
	if err := db.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	st := db.cur.Load()
	if h := ckpOf(t, fsys); h.gen != 1 || h.seq != 6 || h.seg != st.seg || h.off != st.off ||
		h.recCRC != crcAt(t, fsys, segName(1, st.seg), st.off) || st.seg < 2 {
		t.Errorf("checkpoint %+v of the state at %d of segment %d", h, st.off, st.seg)
	}
	for i := 6; i < 9; i++ {
		change(t, db, i)
	}
	want, stats := stateOf(t, db), db.Stats()
	// The writer is killed: Close does not write a checkpoint.
	crashed := fsys.Crash(loseAll)
	db = reopen(t, crashed, o)
	if info := db.Recovery(); !equalInfo(info, RecoveryInfo{Generation: 1, Seq: 9, Checkpoint: "used", Replayed: 3, TruncatedAt: -1}) {
		t.Errorf("Recovery: %+v", info)
	}
	if got := stateOf(t, db); got != want {
		t.Errorf("state:\n%s\nwant:\n%s", got, want)
	}
	// The values do not keep the checkpoint in memory (those of table 2 are from it).
	for table, m := range db.cur.Load().tables {
		m.Ascend("", func(k string, v []byte) bool {
			if cap(v) > len(v)+8 {
				t.Errorf("the value of %d/%q has a capacity of %d", table, k, cap(v))
			}
			return true
		})
	}
	if got := db.Stats(); fmt.Sprint(got) != fmt.Sprint(stats) {
		t.Errorf("Stats:\n%+v\nwant:\n%+v", got, stats)
	}
	// It is the state of the whole log.
	full := copyFS(crashed)
	dropCheckpoint(t, full)
	db = reopen(t, full, o)
	if info := db.Recovery(); info.Checkpoint != "none" || info.Replayed != 9 {
		t.Errorf("Recovery without the checkpoint: %+v", info)
	}
	if got := stateOf(t, db); got != want {
		t.Errorf("state without the checkpoint:\n%s\nwant:\n%s", got, want)
	}
}

// TestCheckpointDamaged checks that Open replays the whole log when the checkpoint cannot be read, as no crash can
// leave it so.
func TestCheckpointDamaged(t *testing.T) {
	o := Options{Placement: Hybrid, Threshold: 100}
	base, db := newSim(t, o)
	for i := range 5 {
		change(t, db, i)
	}
	want := stateOf(t, db)
	closeDB(t, db)
	good := fileData(t, base, "checkpoint")
	h := ckpOf(t, base)
	flip := func(i int) []byte {
		b := slices.Clone(good)
		b[i] ^= 1
		return b
	}
	hash := func(b byte) []byte { x := hashOf(b); return x[:] }
	for _, c := range []struct {
		name string
		data []byte
		why  string
	}{
		{"a byte of the body", flip(ckpHeaderSize + 3), "bad body CRC"},
		{"the last byte", flip(len(good) - 1), "bad body CRC"},
		{"a byte of the header", flip(20), "bad header CRC"},
		{"the magic", flip(0), "not a checkpoint of whiteroom's log"},
		{"version 0", func() []byte { b := slices.Clone(good); b[8] = 0; return b }(), "bad format version 0"},
		{"short", good[:30], "short file"},
		{"empty", nil, "short file"},
		{"cut", good[:len(good)-1], fmt.Sprintf("has %d bytes, want %d", len(good)-1, len(good))},
		{"longer", append(slices.Clone(good), 0), fmt.Sprintf("has %d bytes, want %d", len(good)+1, len(good))},
		{"bad header fields", rawCkp(ckpHeader{gen: 1, seq: 0, seg: 1, off: 64}, nil), "bad header fields"},
		// Bodies whose CRC is valid, which no version of the engine writes.
		{"truncated", rawCkp(h, uv(6)), "truncated body"},
		{"trailing bytes", rawCkp(h, slices.Concat(uv(1, 0, 0, 0), []byte{0})), "trailing bytes"},
		{"bad uvarint", rawCkp(h, []byte{0x80, 0}), "bad uvarint"},
		{"table 0", rawCkp(h, uv(1, 0, 1, 0, 1)), "invalid table 0"},
		{"tables out of order", rawCkp(h, slices.Concat(uv(1, 0, 2, 2, 1), kv("a", ""), uv(1, 1), kv("a", ""))),
			"tables out of order: 1 after 2"},
		{"the same table twice", rawCkp(h, slices.Concat(uv(1, 0, 2, 2, 1), kv("a", ""), uv(2, 1), kv("b", ""))),
			"tables out of order: 2 after 2"},
		{"a table without keys", rawCkp(h, uv(1, 0, 1, 3, 0)), "table 3 has no keys"},
		{"keys out of order", rawCkp(h, slices.Concat(uv(1, 0, 1, 1, 2), kv("b", ""), kv("a", ""))),
			`keys out of order: "a" after "b"`},
		{"a key too long", rawCkp(h, slices.Concat(uv(1, 0, 1, 1, 1), kv(strings.Repeat("k", MaxKey+1), ""))),
			"key too long: 4097 bytes (max 4096)"},
		{"a sealed value", rawCkp(h, slices.Concat(uv(1, 0, 1, 1, 1, 1), []byte("k"), []byte{envSealed}, uv(0))),
			"sealed values are not supported by this version"},
		{"an unknown envelope", rawCkp(h, slices.Concat(uv(1, 0, 1, 1, 1, 1), []byte("k"), []byte{2}, uv(0))),
			"unknown value envelope 2"},
		{"objects out of order", rawCkp(h, slices.Concat(uv(1, 0, 0, 2), hash(2), []byte{placeFile}, uv(1), hash(1),
			[]byte{placeFile}, uv(1))), fmt.Sprintf("object %x out of order", hash(1))},
		{"an unknown placement", rawCkp(h, slices.Concat(uv(1, 0, 0, 1), hash(1), []byte{3})), "unknown placement 3"},
		{"an object in segment 0", rawCkp(h, slices.Concat(uv(1, 0, 0, 1), hash(1), []byte{placeInline}, uv(0, 100, 1))),
			fmt.Sprintf("bad location of object %x", hash(1))},
		{"an object after the segment of the checkpoint", rawCkp(h, slices.Concat(uv(1, 0, 0, 1), hash(1),
			[]byte{placeInline}, uv(h.seg+1, 100, 1))), fmt.Sprintf("bad location of object %x", hash(1))},
		{"an object in a header", rawCkp(h, slices.Concat(uv(1, 0, 0, 1), hash(1), []byte{placeInline}, uv(1, 71, 1))),
			fmt.Sprintf("bad location of object %x", hash(1))},
		{"an object beyond int64", rawCkp(h, slices.Concat(uv(1, 0, 0, 1), hash(1), []byte{placeInline},
			uv(1, 100, math.MaxInt64-99))), fmt.Sprintf("bad location of object %x", hash(1))},
		{"a file object too large", rawCkp(h, slices.Concat(uv(1, 0, 0, 1), hash(1), []byte{placeFile},
			uv(math.MaxInt64+1))), fmt.Sprintf("bad object size %d", uint64(math.MaxInt64+1))},
		{"next event 0", rawCkp(h, uv(0, 0, 0, 0)), "bad event counts: 0 events, next event 0"},
		{"more events than numbers", rawCkp(h, uv(3, 3, 0, 0)), "bad event counts: 3 events, next event 3"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fsys := copyFS(base)
			setFile(t, fsys, "checkpoint", c.data)
			db := reopen(t, fsys, o)
			if info := db.Recovery(); info.Checkpoint != "damaged: "+c.why || info.Replayed != 5 || info.Seq != 5 {
				t.Errorf("Recovery: %+v, want damaged: %s", info, c.why)
			}
			if got := stateOf(t, db); got != want {
				t.Errorf("state:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

// TestCheckpointStale crashes a compaction after its base-end, before its checkpoint: the checkpoint of the old
// generation is then stale, and Open takes the new one, without the data that the compaction dropped. The newest
// complete generation wins over the generation of the checkpoint.
func TestCheckpointStale(t *testing.T) {
	fsys := simfs.New(simDir)
	armed := false
	o := Options{Hook: func(p string) error {
		if armed && p == "compact:base-end" {
			fsys.CrashAt(fsys.Counted())
		}
		return nil
	}}
	db, err := Create(fsys, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for i := range 4 {
		change(t, db, i)
	}
	if err := db.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	armed = true
	err = compactDB(db, Rewrite{Value: func(table uint32, _, v []byte) ([]byte, bool) { return v, table != 2 }})
	if !errors.Is(err, simfs.ErrCrashed) {
		t.Fatalf("compact: %v", err)
	}
	crashed := fsys.Crash(loseAll)
	if h := ckpOf(t, crashed); h.gen != 1 || !slices.Equal(gensOf(t, crashed), []uint64{1, 2}) {
		t.Fatalf("checkpoint of generation %d, generations %d", h.gen, gensOf(t, crashed))
	}
	records := 0
	for _, name := range logNames(t, crashed) {
		if strings.HasPrefix(name, "log/0000000000000002-") {
			_, offs := frames(t, crashed, name)
			records += len(offs)
		}
	}
	db = reopen(t, crashed, Options{})
	if info := db.Recovery(); !equalInfo(info, RecoveryInfo{Generation: 2, Seq: 4, Checkpoint: "stale", Replayed: records, TruncatedAt: -1}) {
		t.Errorf("Recovery: %+v", info)
	}
	if got := dump(t, db); hasTable(got, 2) || !hasTable(got, 1) {
		t.Errorf("dump:\n%s", got)
	}
	checkObjects(t, db)
	closeDB(t, db)
	if h := ckpOf(t, crashed); h.gen != 2 || h.seq != 4 {
		t.Errorf("checkpoint after Close: %+v", h)
	}
	if info := reopen(t, crashed, Options{}).Recovery(); info.Generation != 2 || info.Checkpoint != "used" {
		t.Errorf("Recovery after Close: %+v", info)
	}
}

// TestLogEndsBeforeCheckpoint checks that a checkpoint bounds the damage that Open would take for a torn tail: a log
// that has lost the record of the checkpoint is corrupt, and Open changes nothing.
func TestLogEndsBeforeCheckpoint(t *testing.T) {
	o := Options{SegmentBytes: 100} // a segment of one record
	fsys, db := newSim(t, o)
	for i := range 3 {
		putKey(t, db, 1, fmt.Sprintf("k%d", i), "v")
	}
	if err := db.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	putKey(t, db, 1, "after", "v")
	multi := fsys.Crash(loseAll)
	// The checkpoint is of the last record, at the end of the only segment.
	fsys, db = newSim(t, Options{})
	for i := range 3 {
		putKey(t, db, 1, fmt.Sprintf("k%d", i), "v")
	}
	closeDB(t, db)
	_, offs := frames(t, fsys, seg1)
	one := fileData(t, fsys, seg1)
	does := func(seq uint64, name string, off int64) string {
		return fmt.Sprintf("the log does not have sequence %d at %s offset %d", seq, simPath(name), off)
	}
	for _, c := range []struct {
		name   string
		base   *simfs.FS
		change func(fsys *simfs.FS)
		reason string
	}{
		{"the last record torn", fsys, func(fsys *simfs.FS) { setFile(t, fsys, seg1, one[:len(one)-8]) },
			does(3, seg1, offs[2])},
		{"the last record cut off", fsys, func(fsys *simfs.FS) { setFile(t, fsys, seg1, one[:offs[2]]) },
			does(3, seg1, offs[2])},
		{"the log cut before the last record", fsys, func(fsys *simfs.FS) { setFile(t, fsys, seg1, one[:offs[1]+8]) },
			does(3, seg1, offs[2])},
		{"the last record damaged", fsys, func(fsys *simfs.FS) {
			b := slices.Clone(one)
			b[offs[2]+20] ^= 1
			setFile(t, fsys, seg1, b)
		}, does(3, seg1, offs[2])},
		{"the segment of the record lost", multi, func(fsys *simfs.FS) {
			removeFile(t, fsys, seg4)
			removeFile(t, fsys, seg3)
		}, does(3, seg3, segHeaderSize)},
		{"the segment of the record torn", multi, func(fsys *simfs.FS) {
			removeFile(t, fsys, seg4)
			setFile(t, fsys, seg3, fileData(t, fsys, seg3)[:20])
		}, does(3, seg3, segHeaderSize)},
		{"the segment of the record without records", multi, func(fsys *simfs.FS) {
			setFile(t, fsys, seg3, fileData(t, fsys, seg3)[:segHeaderSize])
		}, does(3, seg3, segHeaderSize)},
	} {
		t.Run(c.name, func(t *testing.T) {
			fsys := copyFS(c.base)
			c.change(fsys)
			checkRefused(t, fsys, "checkpoint", -1, c.reason)
		})
	}
	// A segment after that of the record may be lost: the checkpoint bounds only what is before it.
	lost := copyFS(multi)
	removeFile(t, lost, seg4)
	if db := reopen(t, lost, o); seqOf(t, db) != 3 || db.Recovery().Checkpoint != "used" {
		t.Errorf("seq %d, %+v", seqOf(t, db), db.Recovery())
	}
}

// TestCheckpointRecordMismatch checks that a checkpoint pairs only with the record that it names.
func TestCheckpointRecordMismatch(t *testing.T) {
	base, db := newSim(t, Options{})
	for i := range 3 {
		putKey(t, db, 1, fmt.Sprintf("k%d", i), "v")
	}
	closeDB(t, db)
	h := ckpOf(t, base)
	_, offs := frames(t, base, seg1)
	for _, c := range []struct {
		name   string
		change func(fsys *simfs.FS)
		h      ckpHeader // that the message names
	}{
		{"another CRC", func(fsys *simfs.FS) { setCkpHeader(t, fsys, func(h *ckpHeader) { h.recCRC++ }) }, h},
		{"the offset of another record", func(fsys *simfs.FS) {
			setCkpHeader(t, fsys, func(h *ckpHeader) { h.off = offs[1] })
		}, ckpHeader{gen: 1, seq: 3, seg: 1, off: offs[1]}},
		{"an offset in a record", func(fsys *simfs.FS) {
			setCkpHeader(t, fsys, func(h *ckpHeader) { h.off += 8 })
		}, ckpHeader{gen: 1, seq: 3, seg: 1, off: h.off + 8}},
		{"an offset after the log", func(fsys *simfs.FS) {
			setCkpHeader(t, fsys, func(h *ckpHeader) { h.off += 4096 })
		}, ckpHeader{gen: 1, seq: 3, seg: 1, off: h.off + 4096}},
		{"another sequence number", func(fsys *simfs.FS) {
			setCkpHeader(t, fsys, func(h *ckpHeader) { h.seq = 2 })
		}, ckpHeader{gen: 1, seq: 2, seg: 1, off: h.off}},
		{"a segment that is not there", func(fsys *simfs.FS) {
			setCkpHeader(t, fsys, func(h *ckpHeader) { h.seg = 2 })
		}, ckpHeader{gen: 1, seq: 3, seg: 2, off: h.off}},
		{"another record at its place", func(fsys *simfs.FS) {
			sh, _ := frames(t, fsys, seg1)
			setFile(t, fsys, seg1, slices.Concat(fileData(t, fsys, seg1)[:offs[2]], commitFrame(sh.salt, 3, 1, "k2", "W")))
		}, h},
		{"the header of its segment", func(fsys *simfs.FS) {
			b := fileData(t, fsys, seg1)
			b[30] ^= 1
			setFile(t, fsys, seg1, b)
		}, h},
		{"a first sequence after it", func(fsys *simfs.FS) {
			sh, _ := frames(t, fsys, seg1)
			sh.firstSeq = 4
			setFile(t, fsys, seg1, slices.Concat(appendSegHeader(nil, sh), fileData(t, fsys, seg1)[segHeaderSize:]))
		}, h},
	} {
		t.Run(c.name, func(t *testing.T) {
			fsys := copyFS(base)
			c.change(fsys)
			reason := fmt.Sprintf("the log does not have sequence %d at %s offset %d", c.h.seq,
				simPath(segName(c.h.gen, c.h.seg)), c.h.off)
			checkRefused(t, fsys, "checkpoint", -1, reason)
		})
	}
	// A checkpoint of the first record names a segment whose first sequence is its own.
	fsys, db := newSim(t, Options{})
	putKey(t, db, 1, "k", "v")
	closeDB(t, db)
	sh, _ := frames(t, fsys, seg1)
	sh.firstSeq = 0
	setFile(t, fsys, seg1, slices.Concat(appendSegHeader(nil, sh), fileData(t, fsys, seg1)[segHeaderSize:]))
	checkRefused(t, fsys, "checkpoint", -1, "the log does not have sequence 1 at "+simPath(seg1)+" offset 64")
}

func TestCheckpointNewerVersion(t *testing.T) {
	fsys, db := newSim(t, Options{})
	putKey(t, db, 1, "k", "v")
	closeDB(t, db)
	b := fileData(t, fsys, "checkpoint")
	binary.LittleEndian.PutUint32(b[8:], 2)
	setFile(t, fsys, "checkpoint", b)
	before := fileMap(fsys)
	_, err := Open(fsys, Options{})
	checkVersion(t, err, simPath("checkpoint"), 2)
	checkFilesUnchanged(t, fsys, before)
	// A newer version of the segment of its record refuses too.
	fsys, db = newSim(t, Options{SegmentBytes: 100})
	for i := range 3 {
		putKey(t, db, 1, fmt.Sprintf("k%d", i), "v")
	}
	closeDB(t, db)
	b = fileData(t, fsys, seg3)
	binary.LittleEndian.PutUint32(b[8:], 2)
	setFile(t, fsys, seg3, b)
	before = fileMap(fsys)
	_, err = Open(fsys, Options{})
	checkVersion(t, err, simPath(seg3), 2)
	checkFilesUnchanged(t, fsys, before)
}

func TestCheckpointFileType(t *testing.T) {
	fsys := simfs.New(simDir)
	db, err := Create(fsys, Options{})
	if err != nil {
		t.Fatal(err)
	}
	closeDB(t, db)
	if err := fsys.Mkdir("checkpoint", 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(fsys, Options{}); err == nil || err.Error() != simPath("checkpoint")+": must be a regular file" {
		t.Errorf("Open with a directory: %v", err)
	}
	dir := t.TempDir()
	db, err = Create(osFS(t, dir), Options{})
	if err != nil {
		t.Fatal(err)
	}
	putKey(t, db, 1, "k", "v")
	closeDB(t, db)
	if err := os.Rename(filepath.Join(dir, "checkpoint"), filepath.Join(dir, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere", filepath.Join(dir, "checkpoint")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(osFS(t, dir), Options{}); err == nil || err.Error() != dir+"/checkpoint: symbolic links are not allowed" {
		t.Errorf("Open with a link: %v", err)
	}
}

func TestAutoCheckpoint(t *testing.T) {
	value := strings.Repeat("v", 30)
	n := int64(frameLen(len(appendCommit(nil, 1, []op{{code: opPut, table: 1, key: "k0", value: []byte(value)}}))))
	put := func(t *testing.T, db *DB, i int) {
		t.Helper()
		putKey(t, db, 1, fmt.Sprintf("k%d", i%10), value)
	}
	t.Run("in the Update", func(t *testing.T) {
		var seqs []uint64
		var fsys *simfs.FS
		o := Options{CheckpointBytes: 200, SyncCheckpoints: true, Hook: func(p string) error {
			if p == "checkpoint:done" {
				seqs = append(seqs, ckpOf(t, fsys).seq)
			}
			return nil
		}}
		fsys = simfs.New(simDir)
		db, err := Create(fsys, o)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		// A checkpoint begins when the log has grown by 200 bytes since the last one began.
		var want []uint64
		size, from := int64(segHeaderSize), int64(0)
		for i := range 10 {
			put(t, db, i)
			if size += n; size-from >= 200 {
				want, from = append(want, uint64(i+1)), size
			}
		}
		if !slices.Equal(seqs, want) || len(want) < 2 {
			t.Errorf("checkpoints at %d, want %d", seqs, want)
		}
		db = reopen(t, fsys.Crash(loseAll), Options{})
		if info := db.Recovery(); info.Checkpoint != "used" || info.Replayed != 10-int(want[len(want)-1]) {
			t.Errorf("Recovery: %+v", info)
		}
	})
	t.Run("in the background", func(t *testing.T) {
		synced, release, done := make(chan struct{}, 10), make(chan struct{}), make(chan struct{}, 10)
		o := Options{CheckpointBytes: 200, Hook: func(p string) error {
			switch p {
			case "checkpoint:synced":
				synced <- struct{}{}
				<-release
			case "checkpoint:done":
				done <- struct{}{}
			}
			return nil
		}}
		fsys, db := newSim(t, o)
		defer func() {
			select {
			case <-release:
			default:
				close(release) // so that Close does not wait forever when the test fails
			}
		}()
		for i := range 3 {
			put(t, db, i)
		}
		wait(t, synced, "checkpoint")
		// The Updates go on while it runs, and no other one begins.
		for i := 3; i < 12; i++ {
			put(t, db, i)
		}
		select {
		case <-synced:
			t.Error("a second checkpoint began while one ran")
		default:
		}
		close(release)
		wait(t, done, "end of the checkpoint")
		db.ckptWG.Wait() // the checkpoint has ended
		if h := ckpOf(t, fsys); h.seq != 3 {
			t.Errorf("checkpoint of %d", h.seq)
		}
		put(t, db, 12) // the log has grown by more than 200 bytes since the last one began
		wait(t, done, "second checkpoint")
		if h := ckpOf(t, fsys); h.seq != 13 {
			t.Errorf("checkpoint of %d", h.seq)
		}
		if got := dump(t, reopen(t, fsys.Crash(loseAll), Options{})); got != dump(t, db) {
			t.Errorf("after a crash:\n%s", got)
		}
	})
	t.Run("Close waits for it", func(t *testing.T) {
		// It is held before it takes its turn: Close must not write and release the store before it.
		synced, release := make(chan struct{}, 10), make(chan struct{})
		o := Options{CheckpointBytes: 200, Hook: func(p string) error {
			if p == "checkpoint:begin" {
				synced <- struct{}{}
				<-release
			}
			return nil
		}}
		fsys, db := newSim(t, o)
		for i := range 4 {
			put(t, db, i)
		}
		wait(t, synced, "checkpoint")
		closed := make(chan error)
		go func() { closed <- db.Close() }()
		select {
		case err := <-closed:
			t.Errorf("Close returned %v while a checkpoint ran", err)
		case <-time.After(20 * time.Millisecond):
		}
		close(release)
		if err := wait(t, closed, "Close"); err != nil {
			t.Fatal(err)
		}
		if h := ckpOf(t, fsys); h.seq != 4 {
			t.Errorf("checkpoint of %d after Close", h.seq)
		}
	})
	t.Run("only Checkpoint and Close", func(t *testing.T) {
		fsys, db := newSim(t, Options{CheckpointBytes: -1, SyncCheckpoints: true})
		for i := range 20 {
			put(t, db, i)
		}
		if exists(t, fsys, "checkpoint") {
			t.Error("an automatic checkpoint")
		}
		if err := db.Checkpoint(); err != nil {
			t.Fatal(err)
		}
		if h := ckpOf(t, fsys); h.seq != 20 {
			t.Errorf("checkpoint of %d", h.seq)
		}
	})
	t.Run("after Open", func(t *testing.T) {
		// The growth counts from the checkpoint that Open has used.
		fsys, db := newSim(t, Options{CheckpointBytes: -1})
		put(t, db, 0)
		if err := db.Checkpoint(); err != nil {
			t.Fatal(err)
		}
		for i := 1; i < 4; i++ {
			put(t, db, i)
		}
		crashed := fsys.Crash(loseAll)
		db = reopen(t, crashed, Options{CheckpointBytes: 5 * n, SyncCheckpoints: true})
		put(t, db, 4) // 4 records after the checkpoint
		if h := ckpOf(t, crashed); h.seq != 1 {
			t.Errorf("checkpoint of %d", h.seq)
		}
		put(t, db, 5)
		if h := ckpOf(t, crashed); h.seq != 6 {
			t.Errorf("checkpoint of %d", h.seq)
		}
	})
}

// TestCheckpointErrorsDoNotFail checks that a checkpoint that fails leaves the store usable and as it was: an
// automatic one is counted in Stats, Checkpoint returns the error, and no temporary file is left.
func TestCheckpointErrorsDoNotFail(t *testing.T) {
	hookErr := errors.New("hook failed")
	for _, c := range []struct {
		name     string
		fault    simfs.Fault
		hook     bool // the hook checkpoint:synced fails instead
		replaced bool // the new checkpoint is in place
	}{
		{name: "create", fault: simfs.Fault{Match: ckpTemp(simfs.OpCreate), Err: syscall.ENOSPC}},
		{name: "write", fault: simfs.Fault{Match: ckpTemp(simfs.OpWrite), Err: syscall.EIO}},
		{name: "short write", fault: simfs.Fault{Match: ckpTemp(simfs.OpWrite), Err: syscall.ENOSPC, Short: 10}},
		{name: "sync", fault: simfs.Fault{Match: ckpTemp(simfs.OpSync), Err: syscall.EIO}},
		{name: "dropped sync", fault: simfs.Fault{Match: ckpTemp(simfs.OpSync), Err: syscall.EIO, Drop: true}},
		{name: "rename", fault: simfs.Fault{Match: ckpTemp(simfs.OpRename), Err: syscall.EIO}},
		{name: "hook", hook: true},
		{name: "sync of the directory", fault: simfs.Fault{Match: on(simfs.OpSyncDir, "."), Err: syscall.EIO},
			replaced: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			failHook := false
			o := Options{CheckpointBytes: 100, SyncCheckpoints: true, Hook: func(p string) error {
				if failHook && p == "checkpoint:synced" {
					return hookErr
				}
				return nil
			}}
			fsys, db := newSim(t, o)
			putKey(t, db, 1, "a", strings.Repeat("1", 50))
			putKey(t, db, 1, "b", strings.Repeat("2", 50))
			before := ckpOf(t, fsys)
			cause := c.fault.Err
			if c.hook {
				failHook, cause = true, hookErr
			} else {
				f := c.fault
				f.Times = 1
				fsys.Inject(f)
			}
			putKey(t, db, 1, "c", strings.Repeat("3", 100)) // its checkpoint fails
			failHook = false
			s := db.Stats()
			if s.CheckpointErrors != 1 || !strings.Contains(s.LastCheckpointError, cause.Error()) {
				t.Errorf("Stats: %d, %q", s.CheckpointErrors, s.LastCheckpointError)
			}
			if names := ckpTemps(fsys); len(names) != 0 {
				t.Errorf("temporary files are left: %q", names)
			}
			if h := ckpOf(t, fsys); c.replaced != (h != before) {
				t.Errorf("checkpoint of %d, before %d", h.seq, before.seq)
			}
			putKey(t, db, 1, "d", strings.Repeat("4", 100)) // the next one does not fail
			if h, s := ckpOf(t, fsys), db.Stats(); h.seq != 4 || s.CheckpointErrors != 1 {
				t.Errorf("checkpoint of %d, %d errors", h.seq, s.CheckpointErrors)
			}
			checkObjects(t, db)
			want := dump(t, db)
			db = reopen(t, fsys.Crash(loseAll), o)
			if got := dump(t, db); got != want || db.Recovery().Checkpoint != "used" {
				t.Errorf("after a crash: %+v\n%s", db.Recovery(), got)
			}
		})
	}
	t.Run("Checkpoint", func(t *testing.T) {
		fsys, db := newSim(t, Options{})
		putKey(t, db, 1, "a", "1")
		fsys.Inject(simfs.Fault{Match: ckpTemp(simfs.OpSync), Err: syscall.EIO, Times: 1})
		if err := db.Checkpoint(); !errors.Is(err, syscall.EIO) {
			t.Errorf("Checkpoint: %v", err)
		}
		if s := db.Stats(); s.CheckpointErrors != 0 || len(ckpTemps(fsys)) != 0 || exists(t, fsys, "checkpoint") {
			t.Errorf("Stats: %+v, temps %q", s, ckpTemps(fsys))
		}
		putKey(t, db, 1, "b", "2")
		if err := db.Checkpoint(); err != nil || ckpOf(t, fsys).seq != 2 {
			t.Errorf("Checkpoint again: %v", err)
		}
	})
}

func TestCloseWritesCheckpoint(t *testing.T) {
	o := Options{SegmentBytes: 100}
	fsys, db := newSim(t, o)
	closeDB(t, db)
	if exists(t, fsys, "checkpoint") {
		t.Error("a checkpoint of no commit")
	}
	db = reopen(t, fsys, o)
	for i := range 3 {
		putKey(t, db, 1, fmt.Sprintf("k%d", i), "v")
	}
	st := db.cur.Load()
	closeDB(t, db)
	h := ckpOf(t, fsys)
	if h.gen != 1 || h.seq != 3 || h.seg != 3 || h.off != segHeaderSize || h.recCRC != crcAt(t, fsys, seg3, segHeaderSize) ||
		h.seg != st.seg || h.off != st.off {
		t.Errorf("checkpoint %+v", h)
	}
	// After a crash, the checkpoint of Close is there.
	db = reopen(t, fsys.Crash(loseAll), o)
	if info := db.Recovery(); info.Checkpoint != "used" || info.Replayed != 0 || info.Seq != 3 {
		t.Errorf("Recovery: %+v", info)
	}
	// Close of a store whose checkpoint has the state writes only head.
	db = reopen(t, fsys, o)
	before, counted := fileData(t, fsys, "checkpoint"), fsys.Counted()
	closeDB(t, db)
	if n := fsys.Counted() - counted; n != 2 || !bytes.Equal(fileData(t, fsys, "checkpoint"), before) {
		t.Errorf("Close made %d operations", n)
	}
	// So does a Close after Checkpoint, and a Checkpoint after another.
	db = reopen(t, fsys, o)
	putKey(t, db, 1, "k3", "v")
	if err := db.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	counted = fsys.Counted()
	if err := db.Checkpoint(); err != nil || fsys.Counted() != counted {
		t.Errorf("a second Checkpoint: %v, %d operations", err, fsys.Counted()-counted)
	}
	closeDB(t, db)
	if n := fsys.Counted() - counted; n != 2 || ckpOf(t, fsys).seq != 4 {
		t.Errorf("Close after Checkpoint made %d operations", n)
	}
	// The error of the checkpoint of Close is that of Close, which still writes head and releases the store.
	db = reopen(t, fsys, o)
	putKey(t, db, 1, "k4", "v")
	fsys.Inject(simfs.Fault{Match: ckpTemp(simfs.OpSync), Err: syscall.EIO, Times: 1})
	if err := db.Close(); !errors.Is(err, syscall.EIO) {
		t.Errorf("Close: %v", err)
	}
	if s := readHeadFile(t, fsys); s.seq != 5 || len(ckpTemps(fsys)) != 0 {
		t.Errorf("head after Close: %+v", s)
	}
	before = fileData(t, fsys, "checkpoint")
	// Close of a store that has failed writes nothing.
	db = reopen(t, fsys, o)
	fsys.Inject(simfs.Fault{Match: func(op simfs.Op) bool {
		return op.Kind == simfs.OpSync && strings.HasPrefix(op.Path, "log/")
	}, Err: syscall.EIO, Times: 1})
	checkFailed(t, db, db.Update(context.Background(), putF(1, "x", "y")), syscall.EIO)
	if err := db.Checkpoint(); !errors.Is(err, ErrFailed) {
		t.Errorf("Checkpoint after the failure: %v", err)
	}
	counted = fsys.Counted()
	closeDB(t, db)
	if n := fsys.Counted() - counted; n != 0 || !bytes.Equal(fileData(t, fsys, "checkpoint"), before) {
		t.Errorf("Close after the failure made %d operations", n)
	}
	if err := db.Checkpoint(); !errors.Is(err, errClosed) {
		t.Errorf("Checkpoint after Close: %v", err)
	}
}

// TestCrashDuringCheckpoint crashes after each operation of a Checkpoint, and checks the crash models that simfs.Models
// gives: the checkpoint is the old one or the new one, never a damaged one, and Open recovers the same state.
func TestCrashDuringCheckpoint(t *testing.T) {
	o := Options{Placement: Hybrid, Threshold: 100, SegmentBytes: 300}
	for _, earlier := range []bool{false, true} {
		t.Run(fmt.Sprintf("over a checkpoint %v", earlier), func(t *testing.T) {
			setup := func() (*simfs.FS, *DB) {
				fsys, db := newSim(t, o)
				for i := range 6 {
					if i == 3 && earlier {
						if err := db.Checkpoint(); err != nil {
							t.Fatal(err)
						}
					}
					change(t, db, i)
				}
				return fsys, db
			}
			fsys, db := setup()
			want, start := stateOf(t, db), fsys.Counted()
			if err := db.Checkpoint(); err != nil {
				t.Fatal(err)
			}
			ops := fsys.Counted() - start
			crashes := 0
			for k := range ops + 1 {
				fsys, db := setup()
				fsys.CrashAt(fsys.Counted() + k)
				err := db.Checkpoint()
				for _, m := range simfs.Models(fsys.Pending(), 16, nil) {
					db, oerr := Open(fsys.Crash(m), o)
					if oerr != nil {
						t.Fatalf("a crash after %d of %d operations (%v), %s: Open: %v", k, ops, err, m.Name, oerr)
					}
					info := db.Recovery()
					if strings.HasPrefix(info.Checkpoint, "damaged") || earlier && info.Checkpoint != "used" {
						t.Errorf("a crash after %d of %d operations, %s: %+v", k, ops, m.Name, info)
					}
					if got := stateOf(t, db); got != want {
						t.Errorf("a crash after %d of %d operations, %s:\n%s", k, ops, m.Name, got)
					}
					closeDB(t, db)
					crashes++
				}
			}
			t.Logf("%d crash points, %d crashes", ops+1, crashes)
		})
	}
}
