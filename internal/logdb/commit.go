package logdb

import (
	"context"
	"encoding/binary"
	"fmt"
)

// View runs f in a transaction that only reads, and that sees the store as it was when f first read it.
func (db *DB) View(ctx context.Context, f func(*Tx) error) error {
	if db.closed.Load() {
		return errClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := (&Tx{db: db}).run(f); err != nil {
		return err
	}
	return ctx.Err()
}

// Update runs f in a transaction that writes, and commits it if f returns nil and ctx has not ended; otherwise nothing
// that f has written stays. When Update returns nil, the commit is durable. Updates run one at a time: Update waits
// for the others until ctx ends. f must not call Update of db, which would wait for itself. After a sync of the engine
// has failed, every Update fails with ErrFailed until the store is opened again.
func (db *DB) Update(ctx context.Context, f func(*Tx) error) error {
	if db.closed.Load() {
		return errClosed
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

// commit appends the record of tx to the log and publishes its state. The commit point is the Sync of the record:
// an error before it leaves the record out, and the state is published only after it.
func (db *DB) commit(tx *Tx) error {
	base := tx.st
	body := appendCommit(nil, base.seq+1, tx.ops)
	if len(body) > maxBody {
		return tooLarge(len(body))
	}
	n := int64(frameLen(len(body)))
	if db.seg.records > 0 && db.seg.end+n > db.o.SegmentBytes {
		if err := db.rollover(base.seq + 1); err != nil {
			return err
		}
	}
	seg := db.seg
	frame := appendFrame(make([]byte, 0, n), seg.salt, body)
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
	db.cur.Store(&state{gen: base.gen, seq: base.seq + 1, seg: seg.index, end: seg.end,
		lastCRC: binary.LittleEndian.Uint32(frame[4:]), tables: tx.tables, logBytes: db.logBytes})
	_ = db.hook("update:published")
	db.writeHead()
	return nil
}

// rollover goes on in a new segment, whose first record has the sequence number first. Its header and its name are
// durable before a record goes in it.
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
	_ = db.seg.f.Close()
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
