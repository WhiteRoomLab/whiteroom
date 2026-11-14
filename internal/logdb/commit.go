package logdb

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"path"
)

// View runs f in a transaction that only reads, and that sees the store as it was when f first read it. A View of a
// reader first reads the commits that have become durable; if that fails, View returns the error and does not call f.
// A View that runs when Close is called can go on reading until it ends.
func (db *DB) View(ctx context.Context, f func(*Tx) error) error {
	if !db.handles.enter() {
		return errClosed
	}
	defer db.handles.leave()
	if err := ctx.Err(); err != nil {
		return err
	}
	if db.rd != nil {
		if err := db.rd.refresh(ctx, db); err != nil {
			return err
		}
	}
	if err := (&Tx{db: db}).run(f); err != nil {
		return err
	}
	return ctx.Err()
}

// Update runs f in a transaction that writes, and commits it if f returns nil and ctx has not ended; otherwise nothing
// that f has written stays. When Update returns nil, the commit is durable. Updates run one at a time: Update waits
// for the others until ctx ends. f must not call Update of db, which would wait for itself. After a sync of the engine
// has failed, every Update fails with ErrFailed until the store is opened again, after a restart of the machine.
func (db *DB) Update(ctx context.Context, f func(*Tx) error) error {
	switch {
	case db.closed.Load():
		return errClosed
	case db.rd != nil:
		return ErrReadOnly
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case db.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-db.sem }()
	switch {
	case db.closed.Load():
		return errClosed
	case ctx.Err() != nil:
		return ctx.Err()
	case db.failed != nil:
		return db.failed
	}
	tx := &Tx{db: db, st: db.cur.Load(), write: true, owner: new(owner)}
	if err := tx.run(f); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(tx.ops) == 0 {
		return nil
	}
	return db.commit(tx)
}

// commit makes the files of the new objects of tx durable, appends its record to the log and publishes its state. The
// commit point is the Sync of the record: an error before it leaves the record out, and the state is published only
// after it.
func (db *DB) commit(tx *Tx) error {
	base := tx.st
	seq := base.seq + 1
	number := base.nextEvent
	for i := range tx.ops {
		if p := &tx.ops[i]; p.code == opEvent {
			p.number, p.seq = number, seq
			number++
		}
	}
	body := appendCommit(nil, seq, tx.ops)
	if len(body) > maxBody {
		return tooLarge(len(body))
	}
	if err := db.writeObjects(tx); err != nil {
		return err
	}
	if err := db.hook("update:objects"); err != nil {
		return err
	}
	n := int64(frameLen(len(body)))
	if db.seg.records > 0 && db.seg.end+n > db.o.SegmentBytes {
		if err := db.rollover(seq); err != nil {
			return err
		}
	}
	seg := db.seg
	frame := appendFrame(make([]byte, 0, n), seg.salt, body)
	st := db.next(tx, seg, frame)
	if err := seg.append(frame); err != nil {
		return db.undo(err)
	}
	if err := db.hook("update:written"); err != nil {
		return db.undo(err)
	}
	if err := seg.f.Sync(); err != nil {
		return db.fail(err)
	}
	// The commit point: from here on nothing fails, and the hooks can only block.
	_ = db.hook("update:synced")
	seg.end += n
	seg.records++
	db.logBytes += n
	db.cur.Store(st)
	_ = db.hook("update:published")
	db.writeHead()
	db.autoCheckpoint(st)
	return nil
}

// next returns the state after the commit of tx, whose frame goes after the last record of seg: the state that
// recovery makes of the record.
func (db *DB) next(tx *Tx, seg *segment, frame []byte) *state {
	base := tx.st
	n := int64(len(frame))
	st := &state{gen: base.gen, seq: base.seq + 1, seg: seg.index, off: seg.end, end: seg.end + n,
		lastCRC: binary.LittleEndian.Uint32(frame[4:]), tables: tx.tables, objects: base.objects,
		nextEvent: base.nextEvent, events: base.events, logBytes: db.logBytes + n, segments: int(seg.index)}
	if st.tables == nil {
		st.tables = base.tables
	}
	for i := range tx.ops {
		switch p := &tx.ops[i]; p.code {
		case opPut, opDelete: // in tx.tables already
		case opInline, opFile, opEvent:
			if reason := st.applyOp(tx.owner, p, st.seq, seg.index, seg.end); reason != "" {
				panic(fmt.Sprintf("logdb: a commit that recovery would refuse: %s", reason))
			}
		}
	}
	return st
}

// writeObjects writes the files of the objects that tx puts in files, before its record names them: each is synced
// and renamed into place, then the directories that changed are synced, each once.
func (db *DB) writeObjects(tx *Tx) error {
	var dirs []string
	seen := map[string]bool{}
	syncObjects := false
	for _, p := range tx.ops {
		if p.code != opFile {
			continue
		}
		name := objName(p.hash)
		dir := path.Dir(name)
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
			// A directory that is there already may be one whose name is not durable yet.
			if !db.objDirs[dir] {
				if err := db.fsys.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
					return err
				}
				syncObjects = true
			}
		}
		if err := db.writeObjectFile(name, tx.objs[p.hash]); err != nil {
			return err
		}
	}
	if syncObjects {
		if err := db.fsys.SyncDir("objects"); err != nil {
			return db.fail(err)
		}
		for _, dir := range dirs {
			db.objDirs[dir] = true
		}
	}
	for _, dir := range dirs {
		if err := db.fsys.SyncDir(dir); err != nil {
			return db.fail(err)
		}
	}
	return nil
}

// writeObjectFile writes data as the object file name: it writes a temporary file in the same directory, syncs it,
// and renames it to name, which it replaces.
func (db *DB) writeObjectFile(name string, data []byte) error {
	f, tmp, err := createTemp(db.fsys, path.Dir(name)+"/.tmp-")
	if err != nil {
		return err
	}
	b := append(appendObjHeader(make([]byte, 0, objHeaderSize+len(data))), data...)
	if err := writeAt(f, b, 0); err != nil {
		_ = f.Close()
		_ = db.fsys.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return db.fail(err)
	}
	if err := f.Close(); err != nil {
		_ = db.fsys.Remove(tmp)
		return err
	}
	if err := db.fsys.Rename(tmp, name); err != nil {
		_ = db.fsys.Remove(tmp)
		return err
	}
	return nil
}

// rollover goes on in a new segment, whose first record has the sequence number first. Its header and its name are
// durable before a record goes in it. The file of the old segment stays open for reads.
func (db *DB) rollover(first uint64) error {
	seg, err := createSegment(db.fsys, db.seg.gen, db.seg.index+1, first)
	if err != nil {
		return err
	}
	if err := seg.f.Sync(); err != nil {
		_ = seg.f.Close()
		return db.fail(err)
	}
	if err := db.fsys.SyncDir("log"); err != nil {
		_ = seg.f.Close()
		return db.fail(err)
	}
	db.handles.add(seg.gen, seg.index, seg.f)
	db.seg = seg
	db.logBytes += segHeaderSize
	return nil
}

// undo cuts off what a commit that failed before its commit point may have written, and returns err. If it cannot,
// the engine stops writing.
func (db *DB) undo(err error) error {
	if terr := db.seg.truncate(db.seg.end); terr != nil {
		return db.fail(terr)
	}
	return err
}

// fail stops the engine writing after cause, the error of a sync or of an undo, and returns the error of every Update
// from then on. A failed sync is never tried again: Linux may then report success for data that it has lost.
func (db *DB) fail(cause error) error {
	db.failed = fmt.Errorf("%w: %w", ErrFailed, cause)
	return db.failed
}

// writeHead writes the published state to head. Its errors are counted, and fail nothing.
func (db *DB) writeHead() {
	if err := db.head.write(db.cur.Load()); err != nil {
		db.headErrors.Add(1)
	}
}

func (db *DB) hook(point string) error {
	if db.o.Hook == nil {
		return nil
	}
	return db.o.Hook(point)
}
