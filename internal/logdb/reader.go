package logdb

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// reader is what a DB that OpenReader has opened keeps to follow the log, which another process writes.
type reader struct {
	turn chan struct{} // held by the refresh that runs, and by Close
	head vfs.File
	// Guarded by turn.
	tail   tail
	failed error // after a Sync failed: the error of every refresh
}

// tail is where a reader goes on reading the log: in the segment index, whose header it has checked, after end, the
// end of the records that it has read there (64 if none). before is the size of the segments before it.
type tail struct {
	index    segNum
	firstSeq seqNum
	salt     uint64
	end      segOff
	before   int64
}

func tailOf(seg *segment, st *state) tail {
	return tail{index: seg.index, firstSeq: seg.firstSeq, salt: seg.salt, end: seg.end, before: st.logBytes - int64(seg.end)}
}

// OpenReader opens the store in fsys to read it, in a process other than its writer, which may run or not. Its Views
// see the commits that are durable when they begin; it changes no file. Update and Checkpoint return ErrReadOnly.
// Maintain waits until it is closed.
func OpenReader(fsys vfs.FS, o Options) (*DB, error) {
	if err := o.fill(); err != nil {
		return nil, err
	}
	if err := checkLayout(fsys); err != nil {
		return nil, err
	}
	unlock, err := acquireReader(fsys)
	if err != nil {
		return nil, err
	}
	head, err := fsys.Open("head")
	if err != nil {
		_ = unlock()
		return nil, err
	}
	db := &DB{fsys: fsys, o: o, sem: make(chan struct{}, 1), handles: newHandles(fsys), unlock: unlock,
		rd: &reader{turn: make(chan struct{}, 1), head: head}}
	opened := false
	defer func() {
		if !opened {
			_ = db.handles.close()
			_ = head.Close()
			_ = unlock()
		}
	}()
	if err := db.rd.load(db); err != nil {
		return nil, err
	}
	if o.Accept != nil {
		if err := (&Tx{db: db, st: db.cur.Load()}).run(o.Accept); err != nil {
			return nil, err
		}
	}
	opened = true
	return db, nil
}

// load reads the state of the store as Open recovers it, without changing a file. If no writer runs, it reads the log
// to its last valid record, holding lock/writer so that none starts meanwhile, and syncs what it has read, which a
// writer that was killed may have left in the page cache only. If a writer runs, it reads only what head names.
func (rd *reader) load(db *DB) error {
	read := func(s segFile, off, end segOff) ([]byte, error) {
		return readRange(db.fsys, s.name, off, end, true)
	}
	var r *recovery
	unlock, err := db.fsys.Lock("lock/writer", vfs.Shared)
	switch {
	case err == nil:
		if r, err = decide(db.fsys, nil, read); err == nil {
			err = rd.sync(db, r.st.gen, r.applied)
		}
		err = errors.Join(err, unlock())
	case errors.Is(err, vfs.ErrLocked):
		var h headSlot
		if h, err = readHeadAgain(rd.head, path.Join(db.fsys.Name(), "head")); err == nil {
			if r, err = decide(db.fsys, &h, read); err == nil {
				err = rd.sync(db, r.st.gen, r.applied)
			}
		}
	}
	if err != nil {
		return err
	}
	r.info.TruncatedAt, r.info.RemovedSegment = -1, ""
	db.info, rd.tail = r.info, tailOf(r.seg, r.st)
	db.cur.Store(r.st)
	return nil
}

// refresh reads the commits that have become durable since the last refresh, and publishes the state after them. It
// waits for a refresh that runs until ctx ends.
func (rd *reader) refresh(ctx context.Context, db *DB) error {
	select {
	case rd.turn <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-rd.turn }()
	switch {
	case db.closed.Load():
		return errClosed
	case rd.failed != nil:
		return rd.failed
	case ctx.Err() != nil:
		return ctx.Err()
	}
	unlock, err := db.fsys.Lock("lock/writer", vfs.Shared)
	switch {
	case err == nil:
		// No writer runs, and none starts while the reader holds the lock.
		return errors.Join(rd.toEnd(db), unlock())
	case errors.Is(err, vfs.ErrLocked):
		return rd.toHead(db)
	}
	return err
}

// toEnd reads the log from the tail to its last valid record, as recovery would, and syncs the segments that it has
// read records in.
func (rd *reader) toEnd(db *DB) error {
	cur := db.cur.Load()
	segs, err := listSegments(db.fsys)
	if err != nil {
		return err
	}
	g := generation{gen: cur.gen}
	for _, s := range segs {
		if s.gen == cur.gen {
			g.segs = append(g.segs, s)
		}
	}
	if err := g.consecutive(db.fsys); err != nil {
		return err
	}
	if segNum(len(g.segs)) < rd.tail.index {
		msg := fmt.Sprintf("missing segment %d of generation %d", len(g.segs)+1, g.gen)
		return corruptFile(path.Join(db.fsys.Name(), "log"), msg)
	}
	g.segs = g.segs[rd.tail.index-1:]
	next, out, err := rd.scan(db, cur, g, nil)
	if err != nil {
		return err
	}
	if err := rd.sync(db, cur.gen, out.applied); err != nil {
		return err
	}
	rd.publish(db, next, out)
	return nil
}

// toHead reads the log from the tail up to the record that head names, which the writer that runs has made durable.
// head can name an older state than the reader has: the writer may have been killed before it wrote head, and a writer
// that starts writes it only at the end of its Open.
func (rd *reader) toHead(db *DB) error {
	h, err := readHeadAgain(rd.head, path.Join(db.fsys.Name(), "head"))
	if err != nil {
		return err
	}
	cur := db.cur.Load()
	switch {
	case h.gen < cur.gen, h.gen == cur.gen && h.seq < cur.seq:
		return nil
	case h.gen > cur.gen, h.seq == cur.seq && !atHead(cur, h):
		return headMismatch(db.fsys)
	case h.seq == cur.seq:
		return nil
	case h.seg < rd.tail.index:
		return headMismatch(db.fsys)
	}
	g := generation{gen: cur.gen, segs: []segFile{{name: segName(cur.gen, rd.tail.index), gen: cur.gen, index: rd.tail.index}}}
	if h.seg > rd.tail.index {
		segs, err := listSegments(db.fsys)
		if err != nil {
			return err
		}
		g.segs = nil
		for _, s := range segs {
			if s.gen == cur.gen && s.index >= rd.tail.index && s.index <= h.seg {
				g.segs = append(g.segs, s)
			}
		}
		if segNum(len(g.segs)) != h.seg-rd.tail.index+1 || g.segs[0].index != rd.tail.index {
			return headMismatch(db.fsys)
		}
	}
	next, out, err := rd.scan(db, cur, g, &h)
	switch {
	case err != nil:
		return err
	case !atHead(next, h):
		return headMismatch(db.fsys)
	}
	rd.publish(db, next, out)
	return nil
}

// scan reads the log of the generation of cur from the tail on, in the segments of g, the first of which is that of
// the tail, up to lim if it is not nil, and returns the state after the records that it has read. It reads the
// segments through the handles, which keep only those whose header it has found valid.
func (rd *reader) scan(db *DB, cur *state, g generation, lim *headSlot) (*state, replayed, error) {
	next := *cur
	next.tables = maps.Clone(cur.tables)
	next.logBytes = rd.tail.before
	q := sequence{gen: cur.gen, seq: cur.seq}
	sc := &scan{fsys: db.fsys, g: g, st: &next, q: &q, own: new(owner), limit: lim,
		read: func(s segFile, off, end segOff) ([]byte, error) {
			f, err := db.handles.file(s.gen, s.index)
			if err != nil {
				return nil, err
			}
			return readFrom(f, path.Join(db.fsys.Name(), s.name), off, end, true)
		}}
	h := segHeader{gen: cur.gen, index: rd.tail.index, firstSeq: rd.tail.firstSeq, salt: rd.tail.salt}
	out, err := sc.run(0, h, rd.tail.end, nil)
	keep := rd.tail.index
	if err == nil {
		keep = out.seg.index
	}
	for _, s := range g.segs {
		if s.index > keep {
			db.handles.drop(s.gen, s.index) // a segment whose header was not valid may be made again
		}
	}
	if err != nil {
		return nil, out, err
	}
	next.segments = int(out.seg.index)
	return &next, out, nil
}

// publish makes next, the state that a scan has found, what Views see, unless the scan has found nothing new, and goes
// on after it.
func (rd *reader) publish(db *DB, next *state, out replayed) {
	if t := tailOf(out.seg, next); out.records > 0 || t != rd.tail {
		rd.tail = t
		db.cur.Store(next)
	}
}

// sync makes durable the records that the reader has read in the segments indexes of the generation gen: a writer that
// was killed may have left them in the page cache only. After a Sync failed, the reader stops, as a writer does.
func (rd *reader) sync(db *DB, gen genNum, indexes []segNum) error {
	for _, i := range indexes {
		f, err := db.handles.file(gen, i)
		if err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			rd.failed = fmt.Errorf("%w: %w", ErrFailed, err)
			return rd.failed
		}
	}
	return nil
}

// close closes the reader db. It waits for the refresh that runs; the Views that run go on until they end.
func (rd *reader) close(db *DB) error {
	rd.turn <- struct{}{}
	defer func() { <-rd.turn }()
	if db.closed.Load() {
		return nil
	}
	db.closed.Store(true)
	return errors.Join(db.handles.close(), rd.head.Close(), db.unlock())
}
