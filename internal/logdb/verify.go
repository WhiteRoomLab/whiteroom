package logdb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"maps"
	"slices"
)

// Verify checks the store: it reads the log of the state that Views see again from disk, into a new state, which must
// equal it, and reads every object, whose data must have its hash. Updates wait for it.
func (db *DB) Verify(ctx context.Context) error {
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
	if db.closed.Load() {
		return errClosed
	}
	st := db.cur.Load()
	log := &state{gen: st.gen, seg: 1, end: segHeaderSize, tables: tableMap{}, nextEvent: 1}
	own := new(owner)
	err := readLog(ctx, db.handles, st, func(r logRecord) (bool, error) {
		if reason := log.applyRecord(own, &r.rec, r.seg, r.off); reason != "" {
			return false, &CorruptError{Path: r.p, Offset: int64(r.off), Reason: reason}
		}
		log.seq, log.seg, log.off, log.end, log.lastCRC = r.rec.seq, r.seg, r.off, r.off+segOff(r.n), r.crc
		return true, nil
	})
	if err != nil {
		return err
	}
	if d := diffStates(log, st); d != "" {
		return fmt.Errorf("%s: the state in memory differs from the log: %s", db.fsys.Name(), d)
	}
	st.objects.Ascend("", func(k string, loc objLoc) bool {
		if err = ctx.Err(); err != nil {
			return false
		}
		hash := [32]byte([]byte(k))
		var data []byte
		if data, err = db.readObject(st.gen, hash, loc); err != nil {
			return false
		}
		if got := sha256.Sum256(data); got != hash {
			err = fmt.Errorf("object %x: the data has the hash %x", hash[:], got[:])
		}
		return err == nil
	})
	return err
}

// diffStates returns the first difference between log, the state that the log gives, and mem, the one in memory, or
// "".
func diffStates(log, mem *state) string {
	for _, f := range []struct {
		what     string
		log, mem any
	}{
		{"generation", log.gen, mem.gen},
		{"sequence", log.seq, mem.seq},
		{"segment of the last record", log.seg, mem.seg},
		{"offset of the last record", log.off, mem.off},
		{"end of the last record", log.end, mem.end},
		{"CRC of the last record", log.lastCRC, mem.lastCRC},
		{"next event", log.nextEvent, mem.nextEvent},
		{"events", log.events, mem.events},
	} {
		if f.log != f.mem {
			return fmt.Sprintf("%s: %v in the log, %v in memory", f.what, f.log, f.mem)
		}
	}
	tables := map[uint32]bool{}
	for _, m := range []tableMap{log.tables, mem.tables} {
		for id := range m {
			tables[id] = true
		}
	}
	for _, id := range slices.Sorted(maps.Keys(tables)) {
		name := func(k string) string { return fmt.Sprintf("table %d, key %q", id, short([]byte(k))) }
		if d := diffMaps(log.tables[id], mem.tables[id], name, bytes.Equal, func(v []byte) string {
			return fmt.Sprintf("%q", short(v))
		}); d != "" {
			return d
		}
	}
	name := func(k string) string { return fmt.Sprintf("object %x", k) }
	return diffMaps(log.objects, mem.objects, name, func(a, b objLoc) bool { return a == b }, func(l objLoc) string {
		if l.placement == placeInline {
			return fmt.Sprintf("at %d of segment %d (%d bytes)", l.off, l.seg, l.size)
		}
		return fmt.Sprintf("in a file (%d bytes)", l.size)
	})
}

// diffMaps returns the first difference between log and mem, with name for the keys and show for the values, or "".
func diffMaps[V any](log, mem pmap[V], name func(string) string, equal func(a, b V) bool, show func(V) string) string {
	var d string
	log.Ascend("", func(k string, v V) bool {
		switch w, ok := mem.Get(k); {
		case !ok:
			d = fmt.Sprintf("%s: %s in the log, missing in memory", name(k), show(v))
		case !equal(v, w):
			d = fmt.Sprintf("%s: %s in the log, %s in memory", name(k), show(v), show(w))
		}
		return d == ""
	})
	if d != "" {
		return d
	}
	mem.Ascend("", func(k string, v V) bool {
		if _, ok := log.Get(k); !ok {
			d = fmt.Sprintf("%s: missing in the log, %s in memory", name(k), show(v))
		}
		return d == ""
	})
	return d
}

// short returns b, or its start if it is long, for a message.
func short(b []byte) []byte {
	if len(b) > 40 {
		return append(b[:40:40], "..."...)
	}
	return b
}
