package logdb

import (
	"fmt"
	"math"
)

// state is the state of the store after the record seq of the generation gen, whose frame is at off in the segment
// seg and ends at end (seq 0: no record, end 64 in segment 1). In a generation after the first, the base records and
// the base-end have the sequence number of the last commit before it. A published state never changes: an Update
// makes a new one.
type state struct {
	gen, seq  uint64
	seg       uint64
	off, end  int64
	lastCRC   uint32 // the CRC of the frame of the record seq, or 0
	tables    tableMap
	objects   pmap[objLoc] // by the 32 bytes of their SHA-256
	nextEvent uint64       // the number of the next event; the first is 1
	events    uint64       // in the log of the generation
	logBytes  int64        // the size of the segments of the generation
	segments  int          // of the generation; the last may have no record
}

// objLoc is where an object is: in a file of objects/, or at off in the segment seg of the generation, in a record.
type objLoc struct {
	placement byte // placeInline or placeFile
	seg       uint64
	off, size int64
}

// tableMap holds tables by their IDs. A table without keys is not in it.
type tableMap map[uint32]pmap[[]byte]

func (t tableMap) put(o *owner, table uint32, key string, value []byte) {
	t[table] = t[table].Put(o, key, value)
}

func (t tableMap) del(o *owner, table uint32, key string) {
	m, found := t[table].Delete(o, key)
	switch {
	case !found:
	case m.Len() == 0:
		delete(t, table)
	default:
		t[table] = m
	}
}

// applyRecord applies rec, a record of the generation of s whose frame is at off in the segment seg, to s, which is
// not published yet, with nodes of o, or says why it cannot. The order of the records is for the caller to check.
func (s *state) applyRecord(o *owner, rec *record, seg uint64, off int64) string {
	switch rec.typ {
	case recBase:
		for i := range rec.ops {
			if reason := s.applyBaseOp(o, &rec.ops[i], rec.seq, seg, off); reason != "" {
				return reason
			}
		}
		return ""
	case recBaseEnd:
		return s.endBase(rec.end)
	}
	return s.apply(o, rec.ops, rec.seq, seg, off)
}

// apply applies ops, those of the commit seq whose frame is at off in the segment seg, to s, which is not published
// yet, with nodes of o, or says why it cannot.
func (s *state) apply(o *owner, ops []op, seq, seg uint64, off int64) string {
	for i := range ops {
		if reason := s.applyOp(o, &ops[i], seq, seg, off); reason != "" {
			return reason
		}
	}
	return ""
}

func (s *state) applyOp(o *owner, p *op, seq, seg uint64, off int64) string {
	switch p.code {
	case opPut:
		s.tables.put(o, p.table, p.key, clone(p.value))
	case opDelete:
		s.tables.del(o, p.table, p.key)
	case opInline, opFile:
		key := string(p.hash[:])
		if _, ok := s.objects.Get(key); ok {
			return fmt.Sprintf("object %x is in the store already", p.hash[:])
		}
		loc := objLoc{placement: placeFile, size: p.size}
		if p.code == opInline {
			loc = objLoc{placement: placeInline, seg: seg, off: off + 8 + int64(p.valueOff), size: int64(len(p.value))}
		}
		s.objects = s.objects.Put(o, key, loc)
	case opEvent:
		switch {
		case p.number != s.nextEvent:
			return fmt.Sprintf("event number %d, want %d", p.number, s.nextEvent)
		case p.seq != seq:
			return fmt.Sprintf("event sequence %d, want %d", p.seq, seq)
		}
		s.nextEvent++
		s.events++
	default:
		return fmt.Sprintf("unknown op %d", p.code)
	}
	return ""
}

// applyBaseOp applies p, an op of a base record with the sequence number seq: a put or an object as in a commit, or
// an event, which keeps its number and the sequence number of its commit. The numbers increase, with gaps where a
// compaction dropped events.
func (s *state) applyBaseOp(o *owner, p *op, seq, seg uint64, off int64) string {
	switch p.code {
	case opPut, opInline, opFile:
		return s.applyOp(o, p, seq, seg, off)
	case opEvent:
		switch {
		case p.number < s.nextEvent || p.number == math.MaxUint64:
			return fmt.Sprintf("event number %d, want %d or more", p.number, s.nextEvent)
		case p.seq == 0 || p.seq > seq:
			return fmt.Sprintf("event sequence %d, want 1 to %d", p.seq, seq)
		}
		s.nextEvent = p.number + 1
		s.events++
		return ""
	}
	return fmt.Sprintf("op %d in a base record", p.code)
}

// endBase applies e, the base-end of the generation of s, whose base s has: its counts must be those of s.
func (s *state) endBase(e baseEnd) string {
	keys := 0
	for _, m := range s.tables {
		keys += m.Len()
	}
	if e.keys != uint64(keys) || e.objects != uint64(s.objects.Len()) || e.events != s.events || e.nextEvent < s.nextEvent {
		return fmt.Sprintf("the base of generation %d does not match its end record", s.gen)
	}
	s.nextEvent = e.nextEvent
	return ""
}

// clone copies a value, which may alias a buffer that must not stay alive. An empty value is nil.
func clone(b []byte) []byte {
	return append([]byte(nil), b...)
}
