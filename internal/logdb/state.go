package logdb

import "fmt"

// state is the state of the store after the commit seq, whose record ends at end in the segment seg (seq 0: no
// record, end 64 in segment 1). A published state never changes: an Update makes a new one.
type state struct {
	gen, seq  uint64
	seg       uint64
	end       int64
	lastCRC   uint32 // the CRC of the frame of the commit seq, or 0
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

// apply applies ops, those of the record seq whose frame is at off in the segment seg, to s, which is not published
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

// clone copies a value, which may alias a buffer that must not stay alive. An empty value is nil.
func clone(b []byte) []byte {
	return append([]byte(nil), b...)
}
