package logdb

// Stats are counts of a store: of the state that Views see now, and of the work of the engine since Open.
type Stats struct {
	Keys                       map[uint32]int // by table
	KeyBytes, ValueBytes       int64
	InlineObjects, FileObjects int
	Events                     uint64
	LogBytes                   int64 // of the segments of the generation
	Segments                   int
	HeadErrors                 int64  // failed writes of head
	CheckpointErrors           int64  // failed automatic checkpoints
	LastCheckpointError        string // of the last one
}

// Stats returns the counts of the store. It walks the tables; of the events, it has only their number.
func (db *DB) Stats() Stats {
	st := db.cur.Load()
	s := Stats{Keys: map[uint32]int{}, Events: st.events, LogBytes: st.logBytes, Segments: st.segments,
		HeadErrors: db.headErrors.Load(), CheckpointErrors: db.ckptErrors.Load()}
	if msg := db.ckptLastError.Load(); msg != nil {
		s.LastCheckpointError = *msg
	}
	for table, m := range st.tables {
		s.Keys[table] = m.Len()
		m.Ascend("", func(k string, v []byte) bool {
			s.KeyBytes += int64(len(k))
			s.ValueBytes += int64(len(v))
			return true
		})
	}
	st.objects.Ascend("", func(_ string, loc objLoc) bool {
		if loc.placement == placeInline {
			s.InlineObjects++
		} else {
			s.FileObjects++
		}
		return true
	})
	return s
}
