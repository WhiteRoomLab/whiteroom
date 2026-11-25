package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// The store keeps the login history and the activity log (7.4) as the audit events of the engine: each record is one
// event, to which the store gives its number in its log, the hash of the record before it, and the time of the Update.
// The head of each log, the number and the hash of its last record, is in meta, and its line for the export is in heads
// (the schema 7), which VerifyAudit checks. A record is added in the Update of the change that it tells of (an Update
// that changes anything and adds none fails with ErrUnrecorded), and the store has no way to change or remove one. The
// daemon adds events of its own, its marks (dbwire.AppendMark) and, in its maintenance, those of the deletion of the
// oldest records of the login history, of the acceptance of a break, of a hole that a repair has made and of the acceptance
// of a gap (dbwire.AppendDeleted, dbwire.AppendAccepted, dbwire.AppendHole, dbwire.AppendGap), between the records: the reads
// skip them, and an event that starts as one of them does and is not one is broken, as is one that is not a record. That
// maintenance also writes the rows of the table chains (the schema 8, with the holes in the schema 9 and the accepted gaps in
// the schema 10), which VerifyAudit reads: the chain start of a log, from which its records are checked after a deletion, the
// accepted breaks, at which it is checked again, and the holes and the accepted gaps, which tell the records that are lost and
// at which the log is checked again as well.

// ErrNoAuditLog is the error of the reads of the audit log in a store of the table schema before 6, which has none:
// opening it as its writer once gives it one.
var ErrNoAuditLog = errors.New(
	"the store has a table schema before 6, which has no audit log; open it as its writer once to upgrade it")

// auditLogs are the logs, in the order in which AuditHeads returns their heads.
var auditLogs = []audit.Log{audit.LoginLog, audit.ActivityLog}

// brokenError is the error of what is wrong in the audit log: it wraps audit.ErrBroken.
type brokenError string

func (e brokenError) Error() string { return string(e) }

func (brokenError) Unwrap() error { return audit.ErrBroken }

func brokenEvent(number uint64) error {
	return brokenError(fmt.Sprintf("audit event %d: not a record of the audit log", number))
}

// skipEvent reports whether the event number, data, is an event of the daemon that the reads of the audit log skip: a
// mark, or the event of a deletion, of an acceptance or of a hole. It returns the error of brokenEvent for another event
// of the daemon.
func skipEvent(number uint64, data []byte) (skip bool, err error) {
	if !dbwire.IsDaemonEvent(data) {
		return false, nil
	}
	if !dbwire.ValidEvent(data) {
		return false, brokenEvent(number)
	}
	return true, nil
}

func brokenRecord(log audit.Log, n, number uint64, reason string) error {
	return brokenError(fmt.Sprintf("audit log %q: record %d (event %d): %s", log, n, number, reason))
}

// Record adds r to the log r.Log in the Update of t: the record is there if and only if the other writes of the Update
// are. The store sets the number of r, the hash of the record before it, which the first has none of, and its time, the
// time at which the Update began (as the versions have it); r must have none of them. It fails, and adds nothing, if
// r.Check does, and in a View.
func (t *Tx) Record(r audit.Record) error {
	if err := t.check(true); err != nil {
		return err
	}
	if r.N != 0 || r.Prev != "" || !r.At.IsZero() {
		return errors.New("audit record: the store sets n, prev and at")
	}
	if err := r.Check(); err != nil {
		return fmt.Errorf("audit record: %w", err)
	}
	at, err := parseSavedAt(t.savedAt)
	if err != nil {
		return err
	}
	head, ok, err := t.b.auditHead(r.Log)
	if err != nil {
		return err
	}
	r.N, r.At = head.N+1, at
	if ok {
		r.Prev = hashText(head.Hash[:])
	}
	data, err := audit.Encode(r)
	if err != nil {
		return fmt.Errorf("audit record: %w", err)
	}
	if err := t.b.addEvent(data); err != nil {
		return err
	}
	if err := t.b.setAuditHead(audit.Head{Log: r.Log, N: r.N, Hash: sha256.Sum256(data)}); err != nil {
		return err
	}
	t.records++
	return nil
}

// Changed reports whether t has written anything but records and their heads. A Put of the data that the document has
// already, and a RebuildIndex of an index that is right, write nothing.
func (t *Tx) Changed() bool {
	return t.b.changed()
}

// checkAudit returns ErrNoAuditLog if the store has no audit log.
func (t *Tx) checkAudit() error {
	ready, err := t.b.auditReady()
	switch {
	case err != nil:
		return err
	case !ready:
		return ErrNoAuditLog
	}
	return nil
}

// auditHeads returns the heads of the logs that have records, in the order of auditLogs.
func (t *Tx) auditHeads() ([]audit.Head, error) {
	var heads []audit.Head
	for _, log := range auditLogs {
		h, ok, err := t.b.auditHead(log)
		if err != nil {
			return nil, err
		}
		if ok {
			heads = append(heads, h)
		}
	}
	return heads, nil
}

// ReadAudit calls fn with each record that f selects, from the oldest, until fn returns false. A store that OpenReader
// has opened first reads the commits that have become durable, as a View does. A store of the table schema before 6
// fails with ErrNoAuditLog, and an event that is neither a record nor a mark of the daemon with an error that wraps
// audit.ErrBroken; the marks are not passed to fn.
func (s *Store) ReadAudit(ctx context.Context, f audit.Filter, fn func(audit.Record) bool) error {
	if err := f.Check(); err != nil {
		return err
	}
	return s.View(ctx, func(t *Tx) error {
		if err := t.checkAudit(); err != nil {
			return err
		}
		var broken error
		if err := t.b.events(ctx, func(number uint64, data []byte) bool {
			skip, err := skipEvent(number, data)
			if err != nil {
				broken = err
				return false
			}
			if skip {
				return true
			}
			r, err := audit.Decode(data)
			if err != nil {
				broken = brokenEvent(number)
				return false
			}
			return !f.Match(r) || fn(r)
		}); err != nil {
			return err
		}
		return broken
	})
}

// AuditHeads returns the head of each log that has records, the login history first.
func (s *Store) AuditHeads(ctx context.Context) ([]audit.Head, error) {
	var heads []audit.Head
	err := s.View(ctx, func(t *Tx) error {
		if err := t.checkAudit(); err != nil {
			return err
		}
		var err error
		heads, err = t.auditHeads()
		return err
	})
	if err != nil {
		return nil, err
	}
	return heads, nil
}

// anchorKey names the record that a head kept outside the store is the hash of.
type anchorKey struct {
	log audit.Log
	n   uint64
}

// chainInfo is what the table chains has of a log: its chain start, if records before it were deleted, the breaks that are
// accepted, by the number of their records, and the holes that a repair has made and the gaps that are accepted, by the number
// of their first records.
type chainInfo struct {
	start  *dbwire.ChainStart
	breaks map[uint64]dbwire.ChainBreak
	holes  map[uint64]dbwire.ChainHole
	gaps   map[uint64]dbwire.ChainHole
}

// chainInfoOf reads what the table chains has of log. A row that cannot be read, and a chain start of the activity log,
// which is never deleted, are broken.
func (t *Tx) chainInfoOf(log audit.Log) (chainInfo, error) {
	var ci chainInfo
	v, ok, err := t.b.chainRow(dbwire.ChainStartKey(string(log)))
	if err != nil {
		return ci, err
	}
	if ok {
		start, valid := dbwire.ParseChainStart(v)
		switch {
		case !valid:
			return ci, brokenError(fmt.Sprintf("audit log %q: the chain start in the table chains cannot be read", log))
		case log != audit.LoginLog:
			return ci, brokenError(fmt.Sprintf("audit log %q: the table chains has a chain start for it, but only the oldest "+
				"records of the login history are ever deleted", log))
		}
		ci.start = &start
	}
	var bad error
	if err := t.b.chainRows(dbwire.ChainBreakPrefix(string(log)), func(key, value []byte) bool {
		name, n, keyOK := dbwire.ParseChainBreakKey(key)
		b, valueOK := dbwire.ParseChainBreak(value)
		if !keyOK || !valueOK || name != string(log) || n == 0 {
			bad = brokenError(fmt.Sprintf("audit log %q: a row of the table chains for an accepted break cannot be read", log))
			return false
		}
		if ci.breaks == nil {
			ci.breaks = map[uint64]dbwire.ChainBreak{}
		}
		ci.breaks[n] = b
		return true
	}); err != nil {
		return ci, err
	}
	if bad != nil {
		return ci, bad
	}
	if err := t.b.chainRows(dbwire.ChainHolePrefix(string(log)), func(key, value []byte) bool {
		name, first, keyOK := dbwire.ParseChainHoleKey(key)
		h, valueOK := dbwire.ParseChainHole(first, value)
		if !keyOK || !valueOK || name != string(log) {
			bad = brokenError(fmt.Sprintf("audit log %q: a row of the table chains for a hole cannot be read", log))
			return false
		}
		if ci.holes == nil {
			ci.holes = map[uint64]dbwire.ChainHole{}
		}
		ci.holes[first] = h
		return true
	}); err != nil {
		return ci, err
	}
	if bad != nil {
		return ci, bad
	}
	if err := t.b.chainRows(dbwire.ChainGapPrefix(string(log)), func(key, value []byte) bool {
		name, first, keyOK := dbwire.ParseChainGapKey(key)
		g, valueOK := dbwire.ParseChainHole(first, value)
		if !keyOK || !valueOK || name != string(log) {
			bad = brokenError(fmt.Sprintf("audit log %q: a row of the table chains for an accepted gap cannot be read", log))
			return false
		}
		if ci.gaps == nil {
			ci.gaps = map[uint64]dbwire.ChainHole{}
		}
		ci.gaps[first] = g
		return true
	}); err != nil {
		return ci, err
	}
	return ci, bad
}

// holeNote tells the hole h of log: after is what follows it in the log.
func holeNote(log audit.Log, h dbwire.ChainHole, after string) string {
	lost := fmt.Sprintf("the records %d to %d were", h.First, h.Last)
	if h.First == h.Last {
		lost = fmt.Sprintf("record %d was", h.First)
	}
	return fmt.Sprintf("audit log %q: %s lost and dropped by whiteroom-db repair at %s; %s", log, lost, noteTime(h.At), after)
}

// gapNote tells the gap g of log, which whiteroom-db accept-break accepted: after is what follows it in the log.
func gapNote(log audit.Log, g dbwire.ChainHole, after string) string {
	missing := fmt.Sprintf("the records %d to %d are", g.First, g.Last)
	if g.First == g.Last {
		missing = fmt.Sprintf("record %d is", g.First)
	}
	return fmt.Sprintf("audit log %q: %s missing, and the gap was accepted at %s; %s", log, missing, noteTime(g.At), after)
}

// passNote tells the row that passes a record or the head of log: a hole or, if gap, an accepted gap.
func passNote(log audit.Log, row dbwire.ChainHole, gap bool, after string) string {
	if gap {
		return gapNote(log, row, after)
	}
	return holeNote(log, row, after)
}

// chainEnd is the last record that VerifyAudit has read of a log, and what the table chains says of the log.
type chainEnd struct {
	chainInfo
	n, event, first uint64 // the number of the last record, the number of its event, and the number of the first one
	hash            [sha256.Size]byte
}

// follow checks that r, the record of the event number with the hash sum, follows the end c of its log, and makes it
// the end. Nothing is checked of a record whose break is honored: the log goes on from it.
func (c *chainEnd) follow(r audit.Record, number uint64, sum [sha256.Size]byte, honored bool) error {
	if !honored {
		if err := c.check(r, number); err != nil {
			return err
		}
	}
	if c.first == 0 {
		c.first = r.N
	}
	c.n, c.event, c.hash = r.N, number, sum
	return nil
}

// pass makes the end of the log the last record of the hole or gap h, which a record that follows it then follows as it would
// the record Last that is lost. The first record that is read after a hole at the start is not the first of the log.
func (c *chainEnd) pass(h dbwire.ChainHole) {
	c.n, c.hash = h.Last, h.Hash
}

// honors reports whether the break that is accepted for the record n, whose hash is that of the break, is honored, by the rule
// of dbwire.BreakHonored: the record is the one that the log is expected to go on with, and not the first that remains after a
// deletion when a record before it has been read.
func (c *chainEnd) honors(n uint64) bool {
	return dbwire.BreakHonored(c.start, c.n, n)
}

// check returns the error of a record that does not follow the end of its log. The first record of a log is number 1,
// or, after a deletion, the first record that remains (K), or one before it, which a deletion that stopped before its
// compaction has left: the records from there to K are checked as the others, and K has the hash that the chain start
// has of the one before it.
func (c *chainEnd) check(r audit.Record, number uint64) error {
	switch {
	case c.n == 0 && c.start != nil && r.N > c.start.First:
		return brokenRecord(r.Log, r.N, number, fmt.Sprintf("the chain starts at record %d, which is not there", c.start.First))
	case c.n == 0 && c.start != nil && r.N < c.start.First && r.N == 1 && r.Prev != "":
		return brokenRecord(r.Log, r.N, number, "the first record has the hash of one before it")
	case c.n == 0 && c.start != nil && r.N < c.start.First && r.N != 1 && r.Prev == "":
		return brokenRecord(r.Log, r.N, number, "the record has no hash of the one before it, which only the first record has not")
	case c.n == 0 && c.start == nil && r.N != 1:
		return brokenRecord(r.Log, r.N, number, "the first record of a log is number 1")
	case c.n == 0 && c.start == nil && r.Prev != "":
		return brokenRecord(r.Log, r.N, number, "the first record has the hash of one before it")
	case c.n != 0 && r.N != c.n+1:
		return brokenRecord(r.Log, r.N, number, fmt.Sprintf("the number does not follow that of record %d", c.n))
	case c.n != 0 && r.Prev != hashText(c.hash[:]):
		return brokenRecord(r.Log, r.N, number, fmt.Sprintf("prev is not the hash of record %d", c.n))
	case c.start != nil && r.N == c.start.First && r.Prev != hashText(c.start.Prev[:]):
		return brokenRecord(r.Log, r.N, number, fmt.Sprintf("prev is not the hash that the chain start has of record %d",
			r.N-1))
	}
	return nil
}

// VerifyAudit reads every record of the audit log, and checks that it is as the store has written it: that each
// record can be read (the marks and the other events of the daemon are skipped), that the numbers of a log are 1, 2 and
// so on, that each record has the hash of the one before it, and that the last of each log is the head in the table. In
// a store of the table schema 7 or later it also checks that the row of heads of each log is the line of that head, and
// that a log without a head has none. The activity log has a record, since every store of the table schema 6 or later
// has store.create or store.upgrade in it (which of them it has is not looked at); the login history may have none. It
// fails with an error that wraps audit.ErrBroken, and names the log, the number of the record and that of the event, if
// one of them is not. A rewrite of the records that also makes the chain, the heads of the table and their rows agree
// is found only by anchors, the heads that were kept outside the store (audit.ParseHead): each must be the hash of the
// record of its number. It returns the errors of the reads, and the end of ctx, as they are.
//
// The table chains (the schema 8) changes what it checks. After a deletion of the oldest records of the login history
// the log starts at the record K that the chain start of the log names, whose prev is the hash P of the record before
// it, and a log that has no record left has the head (K-1, P). The records before K, which a deletion that stopped
// before its compaction has left, are checked as ever. A break that is accepted for a record, with the hash that the
// record has, lets the record go unchecked against the one before it: the log is checked again from it. It does so if the
// record is the one that the log is expected to go on with (the one after the last that was read, which counts the records
// before K, or, before any, 1 or K), and it is not K when a record before K was read, which a deletion left (dbwire.BreakHonored).
// A break at the first record that is read is honored, then: at record 1, which has a prev, or at K, whose prev is not the hash
// that the chain start has. A gap is no break, and neither is a record that does not follow the last that was read in number.
// An anchor of a record before K that the store no longer has is not checked, unless it is
// that of record K-1, whose hash is P. A store of a table schema before 8 has no such row.
//
// A hole (the schema 9) is a gap that it passes: the records A to B of a log were lost, and whiteroom-db repair dropped what
// remained of them, with the row that has A as its key, B, the hash H of the stored bytes of record B and the time. The record
// that the log is expected to go on with is the one after the last record that was read (or K, or 1, before any); if the hole
// has that number as its first, and the record read is B+1 and has H as its prev, the record follows the hole as it would follow
// record B, and the log goes on from it. A hole at the end of a log is passed in the same way by the head in the table: it
// names record B, with the hash H, after the last record that was read (or none). A hole is passed exactly as it is recorded: a
// row that is wider or narrower, has another hash, or is for another log passes nothing. The activity log that has no record
// left fails still. A head kept outside the store that names a record of a hole is a problem, as one that names a record that
// the log does not have.
//
// An accepted gap (the schema 10) is passed by the same rule: the records A to B of a log are missing, and whiteroom-db
// accept-break accepted that, once an operator had looked into it, with a row of its own (the key that has A, the same value as
// a hole) that is looked at after the holes. A record after the gap, or a head after the last record, is passed exactly as a
// hole passes it: a row that is wider or narrower, has another hash, or is for another log passes nothing, the activity log
// with no record left fails still, and a head kept outside the store that names a record of the gap is a problem. An accepted
// gap is told as a note, as a hole is.
func (s *Store) VerifyAudit(ctx context.Context, anchors []audit.Head) error {
	return s.VerifyAuditNotes(ctx, anchors, nil)
}

// VerifyAuditNotes is VerifyAudit that also tells what it has found that is no problem, to note, a line of text each
// and in the order in which it has found them, if note is not nil: the chain start of a log, the records before it that
// a deletion has left, each break that is accepted, each hole that a repair has made, each gap that is accepted, and how many
// of the anchors it could not check. The notes have nothing but the names of the logs, numbers, and times. They are passed
// after the reads, also when it fails, but the last two only if it finds nothing wrong.
func (s *Store) VerifyAuditNotes(ctx context.Context, anchors []audit.Head, note func(string)) error {
	byRecord := map[anchorKey][][sha256.Size]byte{}
	for i, a := range anchors {
		if a.Log != audit.LoginLog && a.Log != audit.ActivityLog || a.N < 1 {
			return fmt.Errorf("anchor %d: not the head of an audit log", i+1)
		}
		key := anchorKey{a.Log, a.N}
		byRecord[key] = append(byRecord[key], a.Hash)
	}
	var notes []string
	err := s.View(ctx, func(t *Tx) error {
		notes = notes[:0]
		if err := t.checkAudit(); err != nil {
			return err
		}
		heads, err := t.auditHeads()
		if err != nil {
			return err
		}
		chains := map[audit.Log]chainInfo{}
		for _, log := range auditLogs {
			ci, err := t.chainInfoOf(log)
			if err != nil {
				return err
			}
			chains[log] = ci
			if ci.start != nil {
				notes = append(notes, fmt.Sprintf("audit log %q: the records before record %d were deleted at %s; the log is "+
					"checked from record %d", log, ci.start.First, noteTime(ci.start.At), ci.start.First))
			}
		}
		ends := map[audit.Log]*chainEnd{}
		matched := map[anchorKey]bool{}
		var broken error
		if err := t.b.events(ctx, func(number uint64, data []byte) bool {
			skip, err := skipEvent(number, data)
			if err != nil {
				broken = err
				return false
			}
			if skip {
				return true
			}
			r, err := audit.Decode(data)
			if err != nil {
				broken = brokenEvent(number)
				return false
			}
			end := ends[r.Log]
			if end == nil {
				end = &chainEnd{chainInfo: chains[r.Log]}
				ends[r.Log] = end
			}
			sum := sha256.Sum256(data)
			b, honored := end.breaks[r.N]
			// A break of a record before the chain start is honored, since the records before it are there until the
			// deletion has been compacted, and is no news.
			if honored = honored && b.Hash == sum && end.honors(r.N); honored && (end.start == nil || r.N >= end.start.First) {
				notes = append(notes, fmt.Sprintf("audit log %q: the break at record %d was accepted at %s; the log is checked "+
					"again from that record", r.Log, r.N, noteTime(b.At)))
			}
			// A record after a hole or a gap has not the number after the last that was read, so it is never honored as a break.
			if row, gap, passed := dbwire.ChainPass(end.holes, end.gaps, end.start, end.n, r.N, r.Prev); passed {
				notes = append(notes, passNote(r.Log, row, gap, fmt.Sprintf("the log is checked again from record %d", r.N)))
				end.pass(row)
			}
			if broken = end.follow(r, number, sum, honored); broken != nil {
				return false
			}
			key := anchorKey{r.Log, r.N}
			if wants := byRecord[key]; len(wants) > 0 {
				matched[key] = true
				for _, want := range wants {
					if want != sum {
						broken = brokenRecord(r.Log, r.N, number, "its hash is not that of the head kept outside the store")
						return false
					}
				}
			}
			return true
		}); err != nil {
			return err
		}
		if broken != nil {
			return broken
		}
		unchecked, err := checkEnds(ends, chains, heads, anchors, matched, func(text string) { notes = append(notes, text) })
		if err != nil {
			return err
		}
		if err := t.checkHeadLines(heads); err != nil {
			return err
		}
		for _, log := range auditLogs {
			if end := ends[log]; end != nil && end.start != nil && end.first < end.start.First {
				left := fmt.Sprintf("the records %d to %d are", end.first, end.start.First-1)
				if end.first == end.start.First-1 {
					left = fmt.Sprintf("record %d is", end.first)
				}
				notes = append(notes, fmt.Sprintf("audit log %q: %s still in the store after the deletion of the records before "+
					"record %d; the next whiteroom-db compact removes them", log, left, end.start.First))
			}
		}
		if unchecked > 0 {
			names := fmt.Sprintf("%d of the heads kept outside the store name records that the store no longer has, so they "+
				"are not checked", unchecked)
			if unchecked == 1 {
				names = "1 of the heads kept outside the store names a record that the store no longer has, so it is not checked"
			}
			notes = append(notes, names)
		}
		return nil
	})
	if note != nil {
		for _, text := range notes {
			note(text)
		}
	}
	return err
}

// noteTime returns t as a note tells it.
func noteTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// checkHeadLines checks that heads, the heads in meta, have each a row in heads of their line, and that no other log
// has one. A store of a table schema before 7 has none.
func (t *Tx) checkHeadLines(heads []audit.Head) error {
	ready, err := t.b.exportReady()
	if err != nil || !ready {
		return err
	}
	byLog := map[audit.Log]audit.Head{}
	for _, h := range heads {
		byLog[h.Log] = h
	}
	for _, log := range auditLogs {
		line, hasLine, err := t.b.headLine(log)
		if err != nil {
			return err
		}
		head, hasHead := byLog[log]
		if hasLine != hasHead || hasHead && line != head.String() {
			return brokenError(fmt.Sprintf("audit log %q: the row of the head for the export is not the head in the table",
				log))
		}
	}
	return nil
}

// checkEnds checks the last record of each log, ends, against its head in the table, in heads, against the chain start
// of the log, in chains, and against anchors, of which matched has those whose records the read of the log has met. It
// returns how many of the anchors name a record before the chain start, which the log does not have, and that no check of
// the chain start covers; one that names a record after it that the log does not have is a problem, also when a hole or an
// accepted gap of the log records it as lost. A head that names the records that a hole or an accepted gap at the end of its
// log has lost is passed, and tells the hole or the gap to note.
func checkEnds(ends map[audit.Log]*chainEnd, chains map[audit.Log]chainInfo, heads, anchors []audit.Head,
	matched map[anchorKey]bool, note func(string)) (unchecked int, err error) {
	byLog := map[audit.Log]audit.Head{}
	for _, h := range heads {
		byLog[h.Log] = h
	}
	for _, log := range auditLogs {
		end, start := ends[log], chains[log].start
		head, hasHead := byLog[log]
		var last uint64
		if end != nil {
			last = end.n
		}
		var row dbwire.ChainHole
		var gap, holed bool
		if hasHead {
			row, gap, holed = dbwire.ChainEndPass(chains[log].holes, chains[log].gaps, start, last, head.N, head.Hash)
		}
		switch {
		case end == nil && start != nil:
			// No record is left: the head in the table is the last record that was deleted, or the last that a repair dropped.
			switch {
			case !hasHead:
				return 0, brokenError(fmt.Sprintf("audit log %q: the chain starts at record %d, but the table has no head", log,
					start.First))
			case head.N+1 < start.First:
				return 0, brokenError(fmt.Sprintf("audit log %q: the chain starts at record %d, but the head in the table is "+
					"record %d", log, start.First, head.N))
			case holed: // the head is the last record that a repair dropped
			case head.N >= start.First:
				return 0, brokenError(fmt.Sprintf("audit log %q: the table has the head of record %d, but the log has no record",
					log, head.N))
			case head.Hash != start.Prev:
				return 0, brokenError(fmt.Sprintf("audit log %q: the hash of the head in the table, record %d, is not the hash "+
					"that the chain start has", log, head.N))
			}
		case end == nil && log == audit.ActivityLog && (!hasHead || holed):
			return 0, brokenError(fmt.Sprintf("audit log %q: the log has no record, but every store of the table schema 6 or "+
				"later has store.create or store.upgrade as its first", log))
		case end == nil && !hasHead:
		case end == nil && holed:
		case end == nil:
			return 0, brokenError(fmt.Sprintf("audit log %q: the table has the head of record %d, but the log has no record",
				log, head.N))
		case !hasHead:
			return 0, brokenError(fmt.Sprintf("audit log %q: the table has no head, but the log has records, the last of "+
				"which is record %d (event %d)", log, end.n, end.event))
		case !holed && head.N != end.n:
			return 0, brokenError(fmt.Sprintf("audit log %q: the head in the table is record %d, but the last record is "+
				"record %d (event %d)", log, head.N, end.n, end.event))
		case !holed && head.Hash != end.hash:
			return 0, brokenRecord(log, end.n, end.event, "its hash is not that of the head in the table")
		case start != nil && end.n+1 < start.First:
			return 0, brokenError(fmt.Sprintf("audit log %q: the chain starts at record %d, but the log ends with record %d "+
				"(event %d)", log, start.First, end.n, end.event))
		case start != nil && end.n+1 == start.First && end.hash != start.Prev:
			return 0, brokenRecord(log, end.n, end.event, "its hash is not the hash that the chain start has of it")
		}
		if holed {
			after := "the log has no record left"
			if end != nil {
				after = fmt.Sprintf("the log ends with record %d", end.n)
			}
			note(passNote(log, row, gap, after))
		}
	}
	for _, a := range anchors {
		start, end := chains[a.Log].start, ends[a.Log]
		switch {
		case start != nil && a.N == start.First-1:
			if a.Hash != start.Prev {
				return 0, brokenError(fmt.Sprintf("audit log %q: the head kept outside the store is record %d, whose hash is not "+
					"the hash that the chain start has", a.Log, a.N))
			}
		case start != nil && a.N < start.First-1 && !matched[anchorKey{a.Log, a.N}]:
			unchecked++
		case end == nil:
			return 0, brokenError(fmt.Sprintf("audit log %q: the head kept outside the store is record %d, but the log has "+
				"no record", a.Log, a.N))
		case a.N > end.n:
			return 0, brokenError(fmt.Sprintf("audit log %q: the head kept outside the store is record %d, but the log "+
				"ends with record %d (event %d)", a.Log, a.N, end.n, end.event))
		case !matched[anchorKey{a.Log, a.N}]:
			return 0, brokenError(fmt.Sprintf("audit log %q: the head kept outside the store is record %d, which the log does not have",
				a.Log, a.N))
		}
	}
	return unchecked, nil
}
