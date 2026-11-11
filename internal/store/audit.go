package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
)

// The store keeps the login history and the activity log (7.4) as the audit events of the engine: each record is one
// event, to which the store gives its number in its log, the hash of the record before it, and the time of the Update.
// The head of each log, the number and the hash of its last record, is in meta. A record is added in the Update of the
// change that it tells of (an Update that changes anything and adds none fails with ErrUnrecorded), and the store has no
// way to change or remove one.

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
// fails with ErrNoAuditLog, and an event that is not a record with an error that wraps audit.ErrBroken.
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

// chainEnd is the last record that VerifyAudit has read of a log.
type chainEnd struct {
	n, event uint64
	hash     [sha256.Size]byte
}

// follow checks that r, the record of the event number with the hash sum, follows the end c of its log, and makes it
// the end.
func (c *chainEnd) follow(r audit.Record, number uint64, sum [sha256.Size]byte) error {
	switch {
	case c.n == 0 && r.N != 1:
		return brokenRecord(r.Log, r.N, number, "the first record of a log is number 1")
	case c.n != 0 && r.N != c.n+1:
		return brokenRecord(r.Log, r.N, number, fmt.Sprintf("the number does not follow that of record %d", c.n))
	case c.n == 0 && r.Prev != "":
		return brokenRecord(r.Log, r.N, number, "the first record has the hash of one before it")
	case c.n != 0 && r.Prev != hashText(c.hash[:]):
		return brokenRecord(r.Log, r.N, number, fmt.Sprintf("prev is not the hash of record %d", c.n))
	}
	c.n, c.event, c.hash = r.N, number, sum
	return nil
}

// VerifyAudit reads every record of the audit log, and checks that it is as the store has written it: that each
// record can be read, that the numbers of a log are 1, 2 and so on, that each record has the hash of the one before it,
// and that the last of each log is the head in the table. It fails with an error that wraps audit.ErrBroken, and names
// the log, the number of the record and that of the event, if one of them is not. A rewrite of the records that also
// makes the chain and the heads of the table agree is found only by anchors, the heads that were kept outside the store
// (audit.ParseHead): each must be the hash of the record of its number. It returns the errors of the reads, and the end
// of ctx, as they are.
func (s *Store) VerifyAudit(ctx context.Context, anchors []audit.Head) error {
	byRecord := map[anchorKey][][sha256.Size]byte{}
	for i, a := range anchors {
		if a.Log != audit.LoginLog && a.Log != audit.ActivityLog || a.N < 1 {
			return fmt.Errorf("anchor %d: not the head of an audit log", i+1)
		}
		key := anchorKey{a.Log, a.N}
		byRecord[key] = append(byRecord[key], a.Hash)
	}
	return s.View(ctx, func(t *Tx) error {
		if err := t.checkAudit(); err != nil {
			return err
		}
		heads, err := t.auditHeads()
		if err != nil {
			return err
		}
		ends := map[audit.Log]*chainEnd{}
		var broken error
		if err := t.b.events(ctx, func(number uint64, data []byte) bool {
			r, err := audit.Decode(data)
			if err != nil {
				broken = brokenEvent(number)
				return false
			}
			end := ends[r.Log]
			if end == nil {
				end = new(chainEnd)
				ends[r.Log] = end
			}
			sum := sha256.Sum256(data)
			if broken = end.follow(r, number, sum); broken != nil {
				return false
			}
			for _, want := range byRecord[anchorKey{r.Log, r.N}] {
				if want != sum {
					broken = brokenRecord(r.Log, r.N, number, "its hash is not that of the head kept outside the store")
					return false
				}
			}
			return true
		}); err != nil {
			return err
		}
		if broken != nil {
			return broken
		}
		return checkEnds(ends, heads, anchors)
	})
}

// checkEnds checks the last record of each log, ends, against its head in the table, in heads, and against anchors.
func checkEnds(ends map[audit.Log]*chainEnd, heads, anchors []audit.Head) error {
	byLog := map[audit.Log]audit.Head{}
	for _, h := range heads {
		byLog[h.Log] = h
	}
	for _, log := range auditLogs {
		end := ends[log]
		head, hasHead := byLog[log]
		switch {
		case end == nil && !hasHead:
		case end == nil:
			return brokenError(fmt.Sprintf("audit log %q: the table has the head of record %d, but the log has no record",
				log, head.N))
		case !hasHead:
			return brokenError(fmt.Sprintf("audit log %q: the table has no head, but the log has records, the last of "+
				"which is record %d (event %d)", log, end.n, end.event))
		case head.N != end.n:
			return brokenError(fmt.Sprintf("audit log %q: the head in the table is record %d, but the last record is "+
				"record %d (event %d)", log, head.N, end.n, end.event))
		case head.Hash != end.hash:
			return brokenRecord(log, end.n, end.event, "its hash is not that of the head in the table")
		}
	}
	for _, a := range anchors {
		switch end := ends[a.Log]; {
		case end == nil:
			return brokenError(fmt.Sprintf("audit log %q: the head kept outside the store is record %d, but the log has "+
				"no record", a.Log, a.N))
		case a.N > end.n:
			return brokenError(fmt.Sprintf("audit log %q: the head kept outside the store is record %d, but the log "+
				"ends with record %d (event %d)", a.Log, a.N, end.n, end.event))
		}
	}
	return nil
}
