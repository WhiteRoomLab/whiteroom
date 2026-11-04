package logdb

import (
	"context"
	"encoding/binary"
	"fmt"
	"path"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
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
// from the log: the engine keeps only their number in memory.
func (db *DB) Events(ctx context.Context, fn func(Event) bool) error {
	if db.closed.Load() {
		return errClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return readLog(ctx, db.fsys, db.cur.Load(), func(r logRecord) (bool, error) {
		for _, p := range r.rec.ops {
			if p.code == opEvent && !fn(Event{Number: p.number, Seq: p.seq, Data: clone(p.value)}) {
				return false, nil
			}
		}
		return true, nil
	})
}

// logRecord is a record that readLog has read: rec, in the frame of n bytes at off in the segment seg, which is at the
// path p. crc is the CRC of the frame.
type logRecord struct {
	p      string
	seg    uint64
	off, n int64
	crc    uint32
	rec    record
}

// readLog reads the records of the log of st from disk, up to the last record of st, and calls fn with each, in order,
// until fn returns false or an error. It checks the headers and the frames, that the sequence numbers follow each other,
// and ctx before each record; the meaning of the ops is for fn to check.
func readLog(ctx context.Context, fsys vfs.FS, st *state, fn func(r logRecord) (bool, error)) error {
	seq := uint64(0)
	for index := uint64(1); index <= st.seg; index++ {
		name := segName(st.gen, index)
		p := path.Join(fsys.Name(), name)
		end := int64(-1) // a segment before the last one ends with its file
		if index == st.seg {
			end = st.end
		}
		b, err := readPrefix(fsys, name, end) // beyond st.end, an Update may be writing
		if err != nil {
			return err
		}
		h, err := parseSegHeader(p, b, st.gen, index)
		if err != nil {
			return err
		}
		if h.firstSeq != seq+1 {
			return corruptFile(p, fmt.Sprintf("the header names first sequence %d, want %d", h.firstSeq, seq+1))
		}
		if end < 0 {
			end = int64(len(b))
		}
		records := 0
		for off := int64(segHeaderSize); off < end; {
			if err := ctx.Err(); err != nil {
				return err
			}
			body, n, reason := parseFrame(b[off:], h.salt)
			var rec record
			if reason == "" {
				rec, reason = nextRecord(body, seq+1)
			}
			if reason != "" {
				return &CorruptError{Path: p, Offset: off, Reason: reason}
			}
			more, err := fn(logRecord{p: p, seg: index, off: off, n: int64(n), crc: binary.LittleEndian.Uint32(b[off+4:]), rec: rec})
			if err != nil || !more {
				return err
			}
			seq = rec.seq
			off += int64(n)
			records++
		}
		if index < st.seg && records == 0 {
			return corruptFile(p, "no records")
		}
	}
	return nil
}
