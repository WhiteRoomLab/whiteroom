package logdb

import (
	"context"
	"encoding/binary"
	"path"
)

// Event is an audit event in the log. Number counts the events of the store from 1, and Seq is the sequence number of
// the commit that appended it.
type Event struct {
	Number, Seq uint64
	Data        []byte
}

// AppendEvent appends an event with data to the log, in the commit of the Update: the event is there if and only if
// the other writes of the Update are. It copies data.
func (tx *Tx) AppendEvent(data []byte) error {
	if err := tx.checkTx(); err != nil {
		return err
	}
	tx.ops = append(tx.ops, op{code: opEvent, value: clone(data)})
	return nil
}

// Events calls fn with each event of the store that Views see now, in order, until fn returns false. It reads them
// from the log of the generation, those of its base first: the engine keeps only their number in memory. Like a View,
// it can go on when Close is called. A reader first reads the commits that have become durable, as a View does.
func (db *DB) Events(ctx context.Context, fn func(Event) bool) error {
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
	return readLog(ctx, db.handles, db.cur.Load(), func(r logRecord) (bool, error) {
		for _, p := range r.rec.ops {
			if p.code == opEvent && !fn(Event{Number: p.number, Seq: uint64(p.seq), Data: clone(p.value)}) {
				return false, nil
			}
		}
		return true, nil
	})
}

// logRecord is a record that readLog has read: rec, in the frame of n bytes at off in the segment seg, which is at the
// path p. crc is the CRC of the frame.
type logRecord struct {
	p   string
	seg segNum
	off segOff
	n   int64
	crc uint32
	rec record
}

// readLog reads the records of the log of the generation of st from disk, through h, up to the last record of st,
// and calls fn with each, in order, until fn returns false or an error. It checks the headers and the frames, that the
// records follow each other as sequence says, and ctx before each record; the meaning of the ops is for fn to check.
func readLog(ctx context.Context, h *handles, st *state, fn func(r logRecord) (bool, error)) error {
	q := newSequence(st.gen)
	for index := segNum(1); index <= st.seg; index++ {
		p := path.Join(h.fsys.Name(), segName(st.gen, index))
		end := segOff(-1) // a segment before the last one ends with its file
		if index == st.seg {
			end = st.end
		}
		b, err := h.readPrefix(st.gen, index, end) // beyond st.end, an Update may be writing
		if err != nil {
			return err
		}
		sh, err := parseSegHeader(p, b, st.gen, index)
		if err != nil {
			return err
		}
		if err := q.first(p, sh); err != nil {
			return err
		}
		if end < 0 {
			end = segOff(len(b))
		}
		records := 0
		for off := segOff(segHeaderSize); off < end; {
			if err := ctx.Err(); err != nil {
				return err
			}
			body, n, reason := parseFrame(b[off:], sh.salt)
			var rec record
			if reason == "" {
				rec, reason = parseBody(body)
			}
			if reason == "" {
				reason = q.check(&rec)
			}
			if reason != "" {
				return &CorruptError{Path: p, Offset: int64(off), Reason: reason}
			}
			more, err := fn(logRecord{p: p, seg: index, off: off, n: int64(n), crc: binary.LittleEndian.Uint32(b[off+4:]), rec: rec})
			if err != nil || !more {
				return err
			}
			q.advance(&rec)
			off += segOff(n)
			records++
		}
		if index < st.seg && records == 0 {
			return corruptFile(p, "no records")
		}
	}
	return nil
}
