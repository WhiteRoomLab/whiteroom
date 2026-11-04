package logdb

import "fmt"

// state is the state of the store after the commit seq, whose record ends at end in the segment seg (seq 0: no
// record, end 64 in segment 1). A published state never changes: an Update makes a new one.
type state struct {
	gen, seq uint64
	seg      uint64
	end      int64
	lastCRC  uint32 // the CRC of the frame of the commit seq, or 0
	tables   tableMap
	logBytes int64 // the size of the segments of the generation
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

// apply applies ops, those of a record read from the log, to s, which is not published yet, with nodes of o, or says
// why it cannot.
func (s *state) apply(o *owner, ops []op) string {
	for _, p := range ops {
		switch p.code {
		case opPut:
			s.tables.put(o, p.table, p.key, clone(p.value))
		case opDelete:
			s.tables.del(o, p.table, p.key)
		default:
			return fmt.Sprintf("op %d is not supported yet", p.code)
		}
	}
	return ""
}

// clone copies a value, which may alias a buffer that must not stay alive. An empty value is nil.
func clone(b []byte) []byte {
	return append([]byte(nil), b...)
}
