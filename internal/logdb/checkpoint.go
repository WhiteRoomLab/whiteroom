package logdb

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io/fs"
	"maps"
	"math"
	"path"
	"slices"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// ckpMark names the state of a checkpoint: its generation and the sequence number of its record.
type ckpMark struct{ gen, seq uint64 }

func (m ckpMark) before(st *state) bool {
	return m.gen < st.gen || m.gen == st.gen && m.seq < st.seq
}

// checkpoint is a checkpoint file whose header and body are valid: the state after the record that its header names,
// whose nodes belong to own.
type checkpoint struct {
	path string
	h    ckpHeader
	st   *state
	own  *owner
}

func (ck *checkpoint) incomplete() error {
	return corruptFile(ck.path, fmt.Sprintf("names generation %d, which is incomplete", ck.h.gen))
}

func (ck *checkpoint) notInLog(fsys vfs.FS) error {
	seg := path.Join(fsys.Name(), segName(ck.h.gen, ck.h.seg))
	return corruptFile(ck.path, fmt.Sprintf("the log does not have sequence %d at %s offset %d", ck.h.seq, seg, ck.h.off))
}

// readCheckpoint reads the file checkpoint of the store in fsys. If it is missing or damaged, it returns nil and why:
// "none" or "damaged: " and the reason. A crash cannot damage it, as it is renamed into place after its Sync, and the
// log has everything that it has, so a damaged checkpoint is not used. A newer version fails.
func readCheckpoint(fsys vfs.FS) (*checkpoint, string, error) {
	p := path.Join(fsys.Name(), "checkpoint")
	info, err := fsys.Lstat("checkpoint")
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, "none", nil
	case err != nil:
		return nil, "", entryError(p, err, false)
	}
	if err := checkType(p, info.Mode(), false); err != nil {
		return nil, "", err
	}
	b, err := readFile(fsys, "checkpoint")
	if err != nil {
		return nil, "", err
	}
	h, body, err := parseCheckpoint(p, b)
	var ce *CorruptError
	switch {
	case errors.As(err, &ce):
		return nil, "damaged: " + ce.Reason, nil
	case err != nil:
		return nil, "", err
	}
	own := new(owner)
	st, reason := decodeCheckpoint(h, body, own)
	if reason != "" {
		return nil, "damaged: " + reason, nil
	}
	return &checkpoint{path: p, h: h, st: st, own: own}, "", nil
}

// decodeCheckpoint returns the state in body, the body of a checkpoint whose header is h, with nodes of own, or says
// why it cannot. The end, the segments and the size of the log are for the caller to set.
func decodeCheckpoint(h ckpHeader, body []byte, own *owner) (*state, string) {
	d := &decoder{b: body, short: "truncated body"}
	st := &state{gen: h.gen, seq: h.seq, seg: h.seg, off: h.off, lastCRC: h.recCRC, tables: tableMap{}}
	st.nextEvent = d.uvarint()
	st.events = d.uvarint()
	tables := d.uvarint()
	var prev uint32
	for range tables {
		if d.reason != "" {
			break
		}
		id := d.table()
		if d.reason == "" && id <= prev {
			d.fail(fmt.Sprintf("tables out of order: %d after %d", id, prev))
		}
		m := d.ckpTable(id, own)
		if d.reason == "" {
			st.tables[id], prev = m, id
		}
	}
	st.objects = d.ckpObjects(h, own)
	switch {
	case d.reason == "" && d.i != len(body):
		d.fail("trailing bytes")
	case d.reason == "" && (st.nextEvent == 0 || st.events >= st.nextEvent):
		d.fail(fmt.Sprintf("bad event counts: %d events, next event %d", st.events, st.nextEvent))
	}
	if d.reason != "" {
		return nil, d.reason
	}
	return st, ""
}

// left returns how many bytes d has not read.
func (d *decoder) left() uint64 {
	return uint64(len(d.b) - d.i)
}

// ckpTable reads the keys of the table id in a checkpoint body.
func (d *decoder) ckpTable(id uint32, own *owner) pmap[[]byte] {
	count := d.uvarint()
	if d.reason == "" && count == 0 {
		d.fail(fmt.Sprintf("table %d has no keys", id))
	}
	keys := make([]string, 0, min(count, d.left()/3)) // a key has at least 3 bytes
	vals := make([][]byte, 0, cap(keys))
	for range count {
		if d.reason != "" {
			break
		}
		k := d.key()
		v, _ := d.value(0)
		keys, vals = append(keys, k), append(vals, clone(v))
	}
	if d.reason != "" {
		return pmap[[]byte]{}
	}
	m, err := fromSorted(own, keys, vals)
	if err != nil {
		d.fail(err.Error())
	}
	return m
}

// ckpObjects reads the objects of a checkpoint body whose header is h.
func (d *decoder) ckpObjects(h ckpHeader, own *owner) pmap[objLoc] {
	count := d.uvarint()
	keys := make([]string, 0, min(count, d.left()/34)) // an object has at least 34 bytes
	locs := make([]objLoc, 0, cap(keys))
	for range count {
		if d.reason != "" {
			break
		}
		hash := d.hash()
		if k := string(hash[:]); d.reason == "" && len(keys) > 0 && k <= keys[len(keys)-1] {
			d.fail(fmt.Sprintf("object %x out of order", hash[:]))
		}
		loc := objLoc{placement: objPlace(d.u8())}
		switch loc.placement {
		case placeInline:
			seg, off, size := d.uvarint(), d.uvarint(), d.uvarint()
			if d.reason == "" && (seg == 0 || seg > h.seg || off < segHeaderSize+8 || off > math.MaxInt64 ||
				size > math.MaxInt64-off) {
				d.fail(fmt.Sprintf("bad location of object %x", hash[:]))
			}
			loc.seg, loc.off, loc.size = seg, int64(off), int64(size)
		case placeFile:
			size := d.uvarint()
			if d.reason == "" && size > math.MaxInt64 {
				d.fail(fmt.Sprintf("bad object size %d", size))
			}
			loc.size = int64(size)
		default:
			d.fail(fmt.Sprintf("unknown placement %d", loc.placement))
		}
		keys, locs = append(keys, string(hash[:])), append(locs, loc)
	}
	if d.reason != "" {
		return pmap[objLoc]{}
	}
	m, err := fromSorted(own, keys, locs)
	if err != nil {
		d.fail(err.Error())
	}
	return m
}

// Checkpoint writes a checkpoint of the state that Views see now, unless the last checkpoint has it. It waits for the
// Update that runs and for a checkpoint that runs. Its errors leave the store as it was.
func (db *DB) Checkpoint() error {
	if db.rd != nil {
		if db.closed.Load() {
			return errClosed
		}
		return ErrReadOnly
	}
	db.sem <- struct{}{}
	closed, failed, st := db.closed.Load(), db.failed, db.cur.Load()
	if !closed && failed == nil {
		db.autoFrom = st.logBytes
	}
	<-db.sem
	switch {
	case closed:
		return errClosed
	case failed != nil:
		return failed
	}
	db.ckptMu.Lock()
	defer db.ckptMu.Unlock()
	if db.closed.Load() {
		return errClosed
	}
	return db.checkpointLocked(st)
}

// autoCheckpoint starts a checkpoint of st, which an Update has just published, if the log has grown by
// CheckpointBytes since the last one began and none runs: in the background, or before it returns with
// SyncCheckpoints. Its errors are only counted. The caller holds sem.
func (db *DB) autoCheckpoint(st *state) {
	if db.o.CheckpointBytes < 0 || st.logBytes-db.autoFrom < db.o.CheckpointBytes || !db.ckptRuns.CompareAndSwap(false, true) {
		return
	}
	db.autoFrom = st.logBytes
	if db.o.SyncCheckpoints {
		db.runCheckpoint(st)
		return
	}
	db.ckptWG.Go(func() { db.runCheckpoint(st) })
}

func (db *DB) runCheckpoint(st *state) {
	defer db.ckptRuns.Store(false)
	_ = db.hook("checkpoint:begin")
	db.ckptMu.Lock()
	err := db.checkpointLocked(st)
	db.ckptMu.Unlock()
	if err != nil {
		db.ckptErrors.Add(1)
		msg := err.Error()
		db.ckptLastError.Store(&msg)
	}
}

// checkpointLocked writes a checkpoint of st, a published state, unless the last checkpoint has it or a newer one: in a
// temporary file, which it syncs and renames to checkpoint. The caller holds ckptMu.
func (db *DB) checkpointLocked(st *state) error {
	if st.seq == 0 || !db.ckp.before(st) {
		return nil
	}
	f, tmp, err := createTemp(db.fsys, ".tmp-checkpoint-")
	if err != nil {
		return err
	}
	err = writeCheckpoint(f, st)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = db.hook("checkpoint:synced")
	}
	if err == nil {
		err = db.fsys.Rename(tmp, "checkpoint")
	}
	if err != nil {
		_ = db.fsys.Remove(tmp)
		return err
	}
	// The checkpoint is in place: the hooks from here on can only block.
	_ = db.hook("checkpoint:renamed")
	if err := db.fsys.SyncDir("."); err != nil {
		return err
	}
	db.ckp = ckpMark{gen: st.gen, seq: st.seq}
	_ = db.hook("checkpoint:done")
	return nil
}

// writeCheckpoint writes the checkpoint of st to f, a new file, and syncs it: 64 zero bytes and the body, which it
// streams, and then the header, which has the length and the CRC of the body.
func writeCheckpoint(f vfs.File, st *state) error {
	w := &ckpWriter{w: bufio.NewWriterSize(f, 64<<10)}
	_, _ = w.w.Write(make([]byte, ckpHeaderSize))
	w.uvarint(st.nextEvent)
	w.uvarint(st.events)
	w.uvarint(uint64(len(st.tables)))
	for _, id := range slices.Sorted(maps.Keys(st.tables)) {
		m := st.tables[id]
		w.uvarint(uint64(id))
		w.uvarint(uint64(m.Len()))
		m.Ascend("", func(k string, v []byte) bool {
			w.uvarint(uint64(len(k)))
			w.str(k)
			w.u8(byte(envPlain))
			w.uvarint(uint64(len(v)))
			w.write(v)
			return true
		})
	}
	w.uvarint(uint64(st.objects.Len()))
	st.objects.Ascend("", func(k string, loc objLoc) bool {
		w.str(k)
		w.u8(byte(loc.placement))
		if loc.placement == placeInline {
			w.uvarint(loc.seg)
			w.uvarint(uint64(loc.off))
		}
		w.uvarint(uint64(loc.size))
		return true
	})
	if err := w.w.Flush(); err != nil {
		return err
	}
	h := ckpHeader{gen: st.gen, seq: st.seq, seg: st.seg, off: st.off, recCRC: st.lastCRC, bodyLen: w.n, bodyCRC: w.crc}
	if err := writeAt(f, appendCkpHeader(nil, h), 0); err != nil {
		return err
	}
	return f.Sync()
}

// ckpWriter writes the body of a checkpoint to w, and keeps its length and its CRC. The first error of w is that of
// its Flush.
type ckpWriter struct {
	w   *bufio.Writer
	n   int64
	crc uint32
	buf []byte
}

func (c *ckpWriter) write(b []byte) {
	c.n += int64(len(b))
	c.crc = crc32.Update(c.crc, castagnoli, b)
	_, _ = c.w.Write(b)
}

func (c *ckpWriter) str(s string) {
	c.buf = append(c.buf[:0], s...)
	c.write(c.buf)
}

func (c *ckpWriter) u8(b byte) {
	c.buf = append(c.buf[:0], b)
	c.write(c.buf)
}

func (c *ckpWriter) uvarint(v uint64) {
	c.buf = binary.AppendUvarint(c.buf[:0], v)
	c.write(c.buf)
}
