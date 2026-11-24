package logdb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
)

// Problem is something that VerifyEach found wrong in the store. Path is the file under the name of the file system (for
// a state that differs from the log, the name itself), or "" if the error does not name one; Offset is in Path, or -1.
// Reason holds no key or value of a row.
type Problem struct {
	Path   string
	Offset int64
	Reason string
}

func (p Problem) String() string {
	switch {
	case p.Path != "" && p.Offset >= 0:
		return fmt.Sprintf("%s at offset %d: %s", p.Path, p.Offset, p.Reason)
	case p.Path != "":
		return p.Path + ": " + p.Reason
	case p.Offset >= 0:
		return fmt.Sprintf("offset %d: %s", p.Offset, p.Reason)
	}
	return p.Reason
}

// Verify checks the store: it reads the log of the state that Views see again from disk, into a new state, which must
// equal it, and reads every object, whose data must have its hash. Updates wait for it. It returns the error of the
// first problem that VerifyEach finds.
func (db *DB) Verify(ctx context.Context) error {
	var first error
	if err := db.verifyEach(ctx, func(_ Problem, err error) bool {
		first = err
		return false
	}); err != nil {
		return err
	}
	return first
}

// VerifyEach checks the store as Verify does, but calls found with each problem, until it returns false, and then
// returns nil. First comes the problem of the log, if any: it is read to its first damage, and the state is then not
// compared with it. Next come the differences of the state in memory from the log, and last the problems of the
// objects, in the order of their hashes. An error is for the end of ctx or of the store. Updates wait for it, also while
// found runs, which must not use db.
func (db *DB) VerifyEach(ctx context.Context, found func(Problem) bool) error {
	return db.verifyEach(ctx, func(p Problem, _ error) bool { return found(p) })
}

// verifyEach is VerifyEach with, for each problem, the error that Verify returns for it.
func (db *DB) verifyEach(ctx context.Context, found func(Problem, error) bool) error {
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
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, errClosed):
		return err
	case err != nil:
		if !found(problemOf(err, ""), err) {
			return nil
		}
	default:
		const differs = "the state in memory differs from the log: "
		if !diffStates(log, st, func(reason string) bool {
			return found(Problem{Path: db.fsys.Name(), Offset: -1, Reason: differs + reason},
				fmt.Errorf("%s: %s%s", db.fsys.Name(), differs, reason))
		}) {
			return nil
		}
	}
	return db.verifyObjects(ctx, st, found)
}

// verifyObjects reads every object of st, whose data must have its hash.
func (db *DB) verifyObjects(ctx context.Context, st *state, found func(Problem, error) bool) error {
	var err error
	st.objects.Ascend("", func(k string, loc objLoc) bool {
		if err = ctx.Err(); err != nil {
			return false
		}
		hash := [32]byte([]byte(k))
		data, rerr := db.readObject(st.gen, hash, loc)
		if errors.Is(rerr, errClosed) {
			err = rerr
			return false
		}
		if rerr != nil {
			p, _ := db.objectPlace(st.gen, hash, loc)
			return found(problemOf(rerr, p), rerr)
		}
		if got := sha256.Sum256(data); got != hash {
			reason := fmt.Sprintf("object %x: the data has the hash %x", hash[:], got[:])
			p, off := db.objectPlace(st.gen, hash, loc)
			return found(Problem{Path: p, Offset: off, Reason: reason}, errors.New(reason))
		}
		return true
	})
	return err
}

// objectPlace returns the file that has the data of the object hash, which is at loc, and the offset of the data in it,
// or -1.
func (db *DB) objectPlace(gen genNum, hash [32]byte, loc objLoc) (string, int64) {
	if loc.placement != placeInline {
		return path.Join(db.fsys.Name(), objName(hash)), -1
	}
	return path.Join(db.fsys.Name(), segName(gen, loc.seg)), int64(loc.off)
}

// problemOf returns the problem that err, an error of the engine about the file p or about a file that it does not
// know (p is ""), tells.
func problemOf(err error, p string) Problem {
	var ce *CorruptError
	var ve *VersionError
	switch {
	case errors.As(err, &ce):
		return Problem{Path: ce.Path, Offset: ce.Offset, Reason: ce.Reason}
	case errors.As(err, &ve):
		p = ve.Path
	}
	reason := err.Error()
	if p != "" {
		reason = strings.TrimPrefix(reason, p+": ")
	}
	return Problem{Path: p, Offset: -1, Reason: reason}
}

// diffStates calls report with each difference between log, the state that the log gives, and mem, the one in memory,
// until it returns false, and returns whether it has not. The differences of a table are one: how many of its keys are
// missing or have another value. Those of the objects are by hash, first the objects of the log.
func diffStates(log, mem *state, report func(reason string) bool) bool {
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
		if f.log != f.mem && !report(fmt.Sprintf("%s: %v in the log, %v in memory", f.what, f.log, f.mem)) {
			return false
		}
	}
	tables := map[uint32]bool{}
	for _, m := range []tableMap{log.tables, mem.tables} {
		for id := range m {
			tables[id] = true
		}
	}
	for _, id := range slices.Sorted(maps.Keys(tables)) {
		var another, noMem, noLog int
		diffMaps(log.tables[id], mem.tables[id], bytes.Equal, func(_ string, _, _ []byte, inLog, inMem bool) bool {
			switch {
			case !inMem:
				noMem++
			case !inLog:
				noLog++
			default:
				another++
			}
			return true
		})
		if another+noMem+noLog > 0 && !report(fmt.Sprintf("table %d, keys: %d with another value, %d missing in memory, %d missing in the log",
			id, another, noMem, noLog)) {
			return false
		}
	}
	show := func(l objLoc, in bool) string {
		switch {
		case !in:
			return "missing"
		case l.placement == placeInline:
			return fmt.Sprintf("at %d of segment %d (%d bytes)", l.off, l.seg, l.size)
		}
		return fmt.Sprintf("in a file (%d bytes)", l.size)
	}
	return diffMaps(log.objects, mem.objects, func(a, b objLoc) bool { return a == b },
		func(k string, l, m objLoc, inLog, inMem bool) bool {
			return report(fmt.Sprintf("object %x: %s in the log, %s in memory", k, show(l, inLog), show(m, inMem)))
		})
}

// diffMaps calls report with each key of log or mem whose value differs, or that only one has, until it returns false,
// and returns whether it has not. First come the keys of log, then those that only mem has.
func diffMaps[V any](log, mem pmap[V], equal func(a, b V) bool, report func(k string, l, m V, inLog, inMem bool) bool) bool {
	ok := true
	log.Ascend("", func(k string, v V) bool {
		if w, in := mem.Get(k); !in || !equal(v, w) {
			ok = report(k, v, w, true, in)
		}
		return ok
	})
	if !ok {
		return false
	}
	mem.Ascend("", func(k string, w V) bool {
		if _, in := log.Get(k); !in {
			var v V
			ok = report(k, v, w, false, true)
		}
		return ok
	})
	return ok
}
