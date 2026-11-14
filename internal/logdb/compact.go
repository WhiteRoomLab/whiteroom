package logdb

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"maps"
	"slices"
)

// Rewrite says what a compaction keeps of the state, and how it changes it. A nil function keeps everything.
type Rewrite struct {
	// Value returns the value to keep for key in table, or false to drop the key. It must not change value.
	Value func(table uint32, key, value []byte) (newValue []byte, keep bool)
	// Event returns the data to keep for the event e, which keeps its number and sequence number, or false to drop it.
	Event func(e Event) (data []byte, keep bool)
	// Object reports whether to keep the object hash. The file of an object that it drops stays until a GC.
	Object func(hash [32]byte) (keep bool)
}

// baseBytes is about the size of the ops of a base record; an op that is larger goes alone. It is a var so that tests
// can lower it.
var baseBytes = 4 << 20

// compact writes the state, as r changes it, as the base of a new generation, goes on in that generation, and removes
// the old one. The caller holds sem, and no other process uses the store. A crash at any point leaves the old
// generation, with the new one incomplete, or the new one complete, which Open then takes. An error before the
// base-end is written leaves the store in the old generation and usable, unless a sync failed; from its WriteAt on, an
// error stops the store, as a failed sync does: whether the new generation is complete is then not known.
func (db *DB) compact(ctx context.Context, r Rewrite) error {
	switch {
	case db.closed.Load():
		return errClosed
	case db.failed != nil:
		return db.failed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	old := db.cur.Load()
	if old.seq == 0 {
		return nil // nothing to keep, and no checkpoint of it
	}
	if err := db.removeOthers(old.gen); err != nil {
		return err
	}
	c := &compaction{db: db, old: old, r: r, own: new(owner),
		st: &state{gen: old.gen + 1, seq: old.seq, tables: tableMap{}, nextEvent: 1}}
	if err := c.writeBase(ctx); err != nil {
		c.abandon()
		return err
	}
	if err := c.writeEnd(); err != nil {
		c.abandon()
		return err
	}
	return c.finish()
}

// removeOthers removes the segments of the generations other than gen: incomplete newer ones, which a compaction that
// failed has left, and older ones. It removes the last one first and syncs log/ after each, so that a crash leaves no
// gap in a newer generation.
func (db *DB) removeOthers(gen genNum) error {
	segs, err := listSegments(db.fsys)
	if err != nil {
		return err
	}
	for _, s := range slices.Backward(segs) {
		if s.gen == gen {
			continue
		}
		if err := db.fsys.Remove(s.name); err != nil {
			return err
		}
		if err := db.fsys.SyncDir("log"); err != nil {
			return db.fail(err)
		}
	}
	return nil
}

// compaction writes the base of a new generation.
type compaction struct {
	db   *DB
	old  *state
	r    Rewrite
	st   *state // the state of the new generation, as recovery makes it of the records written so far
	own  *owner
	segs []*segment // of the new generation
	part uint64
	ops  []op // of the next base record
	size int
}

func (c *compaction) last() *segment {
	return c.segs[len(c.segs)-1]
}

// abandon closes the files of the new generation, which is not complete. They stay, for the next compaction to remove.
func (c *compaction) abandon() {
	for _, s := range c.segs {
		_ = s.f.Close()
	}
}

// writeBase writes the base records of the new generation, and syncs them: the tables, the objects and the events of
// the old one, as c.r changes them.
func (c *compaction) writeBase(ctx context.Context) error {
	seg, err := createSegment(c.db.fsys, c.st.gen, 1, c.st.seq)
	if err != nil {
		return err
	}
	c.segs = append(c.segs, seg)
	c.st.logBytes = segHeaderSize
	if err := c.durable(seg); err != nil {
		return err
	}
	for _, add := range []func(context.Context) error{c.addTables, c.addObjects, c.addEvents, c.flush} {
		if err := add(ctx); err != nil {
			return err
		}
	}
	if err := c.last().f.Sync(); err != nil {
		return c.db.fail(err)
	}
	return c.db.hook("compact:base")
}

func (c *compaction) addTables(ctx context.Context) error {
	for _, id := range slices.Sorted(maps.Keys(c.old.tables)) {
		var err error
		c.old.tables[id].Ascend("", func(k string, v []byte) bool {
			v = v[:len(v):len(v)]
			if c.r.Value != nil {
				nv, keep := c.r.Value(id, []byte(k), v)
				if !keep {
					return true
				}
				v = clone(nv)
			}
			err = c.add(ctx, op{code: opPut, table: id, key: k, value: v})
			return err == nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// addObjects adds the objects that c.r keeps: those in records with their data, which it reads from the old segments
// and checks, so that a damaged byte does not get a valid CRC in the new generation; those in files with their size.
func (c *compaction) addObjects(ctx context.Context) error {
	var err error
	c.old.objects.Ascend("", func(k string, loc objLoc) bool {
		hash := [32]byte([]byte(k))
		if c.r.Object != nil && !c.r.Object(hash) {
			return true
		}
		p := op{code: opFile, hash: hash, size: loc.size}
		if loc.placement == placeInline {
			var data []byte
			if data, err = c.db.readObject(c.old.gen, hash, loc); err != nil {
				return false
			}
			if got := sha256.Sum256(data); got != hash {
				err = fmt.Errorf("object %x: the data has the hash %x", hash[:], got[:])
				return false
			}
			p = op{code: opInline, hash: hash, value: data}
		}
		err = c.add(ctx, p)
		return err == nil
	})
	return err
}

// addEvents adds the events that c.r keeps, which it reads from the log of the old generation.
func (c *compaction) addEvents(ctx context.Context) error {
	return readLog(ctx, c.db.handles, c.old, func(lr logRecord) (bool, error) {
		for _, p := range lr.rec.ops {
			if p.code != opEvent {
				continue
			}
			data := p.value[:len(p.value):len(p.value)]
			if c.r.Event != nil {
				d, keep := c.r.Event(Event{Number: p.number, Seq: uint64(p.seq), Data: clone(p.value)})
				if !keep {
					continue
				}
				data = clone(d)
			}
			if err := c.add(ctx, op{code: opEvent, number: p.number, seq: p.seq, value: data}); err != nil {
				return false, err
			}
		}
		return true, nil
	})
}

// add adds p to the next base record, which it writes first if p would take it past baseBytes.
func (c *compaction) add(ctx context.Context, p op) error {
	n := len(p.key) + len(p.value) + 64 // with the hash and the varints of an op
	if len(c.ops) > 0 && c.size+n > baseBytes {
		if err := c.flush(ctx); err != nil {
			return err
		}
	}
	c.ops = append(c.ops, p)
	c.size += n
	return nil
}

// flush writes the ops that add has collected as a base record.
func (c *compaction) flush(ctx context.Context) error {
	if len(c.ops) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	rec := record{typ: recBase, seq: c.st.seq, part: c.part, ops: c.ops}
	if err := c.write(appendBase(nil, c.st.seq, c.part, c.ops), &rec, false); err != nil {
		return err
	}
	c.part++
	clear(c.ops)
	c.ops, c.size = c.ops[:0], 0
	return nil
}

// writeEnd writes the base-end and syncs it: the commit point of the compaction.
func (c *compaction) writeEnd() error {
	keys := 0
	for _, m := range c.st.tables {
		keys += m.Len()
	}
	e := baseEnd{parts: c.part, keys: uint64(keys), objects: uint64(c.st.objects.Len()), events: c.st.events,
		nextEvent: c.old.nextEvent}
	rec := record{typ: recBaseEnd, seq: c.st.seq, end: e}
	if err := c.write(appendBaseEnd(nil, c.st.seq, e), &rec, true); err != nil {
		return err
	}
	if err := c.last().f.Sync(); err != nil {
		return c.db.fail(err)
	}
	// The new generation is complete: from here on the hooks can only block.
	_ = c.db.hook("compact:base-end")
	return nil
}

// write appends the frame of body, which holds rec, to the new generation, in a new segment if it does not fit in the
// last one, and applies rec to c.st as recovery does. If end, rec is the base-end, and an error of its WriteAt stops
// the store.
func (c *compaction) write(body []byte, rec *record, end bool) error {
	if len(body) > maxBody {
		return fmt.Errorf("a base record is too large: %d bytes (max %d)", len(body), maxBody)
	}
	n := int64(frameLen(len(body)))
	if seg := c.last(); seg.records > 0 && int64(seg.end)+n > c.db.o.SegmentBytes {
		if err := c.rollover(); err != nil {
			return err
		}
	}
	seg := c.last()
	frame := appendFrame(make([]byte, 0, n), seg.salt, body)
	if reason := c.st.applyRecord(c.own, rec, seg.index, seg.end); reason != "" {
		panic(fmt.Sprintf("logdb: a base that recovery would refuse: %s", reason))
	}
	if err := seg.append(frame); err != nil {
		if end {
			return c.db.fail(err)
		}
		return err
	}
	c.st.seg, c.st.off, c.st.lastCRC = seg.index, seg.end, binary.LittleEndian.Uint32(frame[4:])
	seg.end += segOff(n)
	seg.records++
	c.st.end = seg.end
	c.st.logBytes += n
	return nil
}

// rollover goes on in a new segment of the new generation, once the last one is synced.
func (c *compaction) rollover() error {
	seg := c.last()
	if err := seg.f.Sync(); err != nil {
		return c.db.fail(err)
	}
	next, err := createSegment(c.db.fsys, seg.gen, seg.index+1, c.st.seq)
	if err != nil {
		return err
	}
	c.segs = append(c.segs, next)
	c.st.logBytes += segHeaderSize
	return c.durable(next)
}

// durable syncs seg, a new segment, and log/, which has its name.
func (c *compaction) durable(seg *segment) error {
	if err := seg.f.Sync(); err != nil {
		return c.db.fail(err)
	}
	if err := c.db.fsys.SyncDir("log"); err != nil {
		return c.db.fail(err)
	}
	return nil
}

// finish goes on in the new generation, which is complete: it publishes its state, writes its checkpoint and head, and
// removes the old generation, unless the checkpoint failed.
func (c *compaction) finish() error {
	db, st := c.db, c.st
	st.segments = int(c.last().index)
	for _, s := range c.segs {
		db.handles.add(s.gen, s.index, s.f)
	}
	db.seg, db.logBytes, db.autoFrom = c.last(), st.logBytes, st.logBytes
	db.cur.Store(st)
	db.ckptMu.Lock()
	err := db.checkpointLocked(st)
	db.ckptMu.Unlock()
	if err == nil {
		_ = db.hook("compact:checkpoint")
	}
	db.writeHead()
	if err != nil {
		return err
	}
	return db.removeGeneration(c.old.gen)
}

// removeGeneration removes the segments of the old generation gen. It opens them first, so that the Views that read
// the old state can go on reading objects in them; the handles close them with the store.
func (db *DB) removeGeneration(gen genNum) error {
	segs, err := listSegments(db.fsys)
	if err != nil {
		return err
	}
	var old []segFile
	for _, s := range segs {
		if s.gen == gen {
			if _, err := db.handles.file(s.gen, s.index); err != nil {
				return err
			}
			old = append(old, s)
		}
	}
	for _, s := range slices.Backward(old) {
		_ = db.hook("compact:delete")
		if err := db.fsys.Remove(s.name); err != nil {
			return err
		}
	}
	if err := db.fsys.SyncDir("log"); err != nil {
		return db.fail(err)
	}
	return nil
}
