package dbserver

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// Acceptance is what AcceptBreak accepted.
type Acceptance struct {
	// Gap says that a gap was accepted: a break otherwise, and nothing else is set.
	Gap bool
	// End says that the gap is at the end of the log: the head names the record Last, which the log does not have.
	End bool
	// First and Last are the first and the last record of the gap.
	First, Last uint64
	// NoRecord says that the log has no record that was read before the gap, so that it is the whole of what the log goes on
	// with: a gap at the end of an empty log.
	NoRecord bool
}

// AcceptBreak accepts the break at record n of the audit log named log (dbwire.LogLogin or dbwire.LogActivity), or the gap
// before it, which are the two places where VerifyAudit of the store fails at a record that is there. In one Update it puts the
// row of the table chains that ties the acceptance to what it is for, appends the event of the acceptance and a mark of now. From
// then on VerifyAudit checks the log again from that record, or, for a gap at the end of the log, tells that it ends there. It
// reads the records as audit.Decode does (dbwire.ReadRecordID), uses only their log, number and prev, and changes and drops
// none.
//
// A break is a record whose number is that of the one before it plus one and whose prev is not its hash. The first record that the
// walk reads is one too, if it is the record that the log starts with (1, or the first that remains after a deletion, K) and no
// record before K is in the store: record 1 with a prev, which the first record of a log has not, and K whose prev is not the hash
// that the chain start has of the record before it. The row ties the acceptance to the SHA-256 of the stored bytes of the record,
// and any change to the record undoes it. dbwire.BreakHonored decides whether it is honored, as it does for VerifyAudit of the
// store and the walk of compact.
//
// A gap is a record whose number is above the one that the walk expects after the last record that it has read (the record
// after it, counting the records that a deletion left before the chain start, or the first that remains, or 1), and that no hole
// of a repair and no gap that is accepted already passes. The records E to N-1 are missing, and the record N has a prev that
// names the hash of N-1: the row has the first E as its key, N-1 and that hash, which is the rule of a hole, and no more is
// passed than the row says. A gap at the end of the log is accepted at the number N of the head of the log (the table heads),
// when the log has no record N and none above the last that was read: the records E to N are missing, and the row has the hash
// of the head. Whether the record N is the first that the walk reads is no reason to refuse a gap before it, and a first record
// that is not the one that the log starts with is a gap, not a break.
//
// It refuses, and writes nothing, a record that is not in the store (also one before the chain start, which is not looked at,
// and one whose number the log has twice), a record that follows a hole that a repair has recorded (the table chains has it,
// with the hash that the record has as its prev: VerifyAudit passes the record, so there is nothing to accept; the hole is not
// a break, and the records that it lost are not there to be named) or a gap that is accepted already, a record whose number is
// below that of a record before it, the gap before a record whose prev is not the hash of a record, record 1 with no prev and K
// whose prev is the hash that the chain start has (no break is there), K while records before it that a deletion left are in the
// store (whiteroom-db compact drops them if they are what the deletion left), a record that follows the one before it, a break
// that is accepted already for the record as it is, a chain whose rows cannot be read, and a store of an older table schema than
// dbwire.TableVersion, for the reason of CompactLogin. An event that is no record has no number to be named by, and is not
// looked at.
func AcceptBreak(ctx context.Context, m *logdb.Maintenance, log string, n uint64, now time.Time) (Acceptance, error) {
	if log != dbwire.LogLogin && log != dbwire.LogActivity {
		return Acceptance{}, fmt.Errorf("the audit log %q is neither %s nor %s", log, dbwire.LogLogin, dbwire.LogActivity)
	}
	if n == 0 {
		return Acceptance{}, errors.New("the records of an audit log are numbered from 1")
	}
	now = now.UTC()
	var rows chainRows
	var schema uint64
	var head *auditHead
	if err := m.View(ctx, func(tx *logdb.Tx) error {
		schema = olderSchema(tx)
		rows = readChainRows(tx, log)
		head = readHead(tx, log)
		return nil
	}); err != nil {
		return Acceptance{}, err
	}
	if rows.bad == "" && log != dbwire.LogLogin && rows.start != nil {
		rows.bad = "the table chains: a chain start for a log of which only the login records are ever deleted"
	}
	switch {
	case rows.bad != "":
		return Acceptance{}, fmt.Errorf("the chain of the audit log %s cannot be read, so no break of it is accepted (%s)", log, rows.bad)
	case rows.start != nil && n < rows.start.First:
		return Acceptance{}, fmt.Errorf("record %d of the audit log %s is not in the store: the records before record %d were deleted", n, log,
			rows.start.First)
	}
	p := &breakPass{log: log, n: n, start: rows.start, holes: rows.holes, gaps: rows.gaps}
	if err := m.Events(ctx, p.event); err != nil {
		return Acceptance{}, err
	}
	acc, hash, err := p.decide(rows, head, schema)
	if err != nil {
		return Acceptance{}, err
	}
	err = m.Update(ctx, func(tx *logdb.Tx) error {
		if acc.Gap {
			row := dbwire.ChainHole{Last: acc.Last, Hash: hash, At: now}
			if err := tx.Put(dbwire.TableChains, dbwire.ChainGapKey(log, acc.First), dbwire.AppendChainHole(nil, row)); err != nil {
				return err
			}
			gap := dbwire.Hole{Log: log, First: acc.First, Last: acc.Last, Hash: hash}
			if err := tx.AppendEvent(dbwire.AppendGap(nil, gap)); err != nil {
				return err
			}
			return tx.AppendEvent(dbwire.AppendMark(nil, now))
		}
		accepted := dbwire.ChainBreak{Hash: p.sum, At: now}
		if err := tx.Put(dbwire.TableChains, dbwire.ChainBreakKey(log, n), dbwire.AppendChainBreak(nil, accepted)); err != nil {
			return err
		}
		if err := tx.AppendEvent(dbwire.AppendAccepted(nil, dbwire.Accepted{Log: log, N: n, Hash: p.sum})); err != nil {
			return err
		}
		return tx.AppendEvent(dbwire.AppendMark(nil, now))
	})
	if err != nil {
		return Acceptance{}, err
	}
	return acc, nil
}

// accepted reports whether the break at record n is accepted for the record whose stored bytes have the SHA-256 sum.
func (r chainRows) accepted(n uint64, sum [sha256.Size]byte) bool {
	h, ok := r.breaks[n]
	return ok && h == sum
}

// breakPass is the pass of AcceptBreak over the events: the walk of the records of one log in the order of their events,
// as VerifyAudit makes it, which looks at the record n only.
type breakPass struct {
	log   string
	n     uint64
	start *dbwire.ChainStart
	holes map[uint64]dbwire.ChainHole
	gaps  map[uint64]dbwire.ChainHole
	// last is the last record that the walk has accepted from the chain start, which the records that a deletion left before it
	// are not, and reach the number of the last record of any kind that it has read, which they are: what a gap is a gap of.
	last  chainLast
	reach uint64

	seen   int // how many records of the log have the number n
	sum    [sha256.Size]byte
	prev   string // the prev of the first of them
	why    string // why the first of them does not follow the walk before it, "" if it does
	first  bool   // it is the first record of the walk
	before uint64 // the number of the last record read before it, counting those that a deletion left; 0 if none
	// expected is the number that the walk expects at the first of them: the record after the one before it.
	expected uint64
	// row is the hole, or the accepted gap if rowGap, that it follows, as VerifyAudit passes it; passed is whether there is one.
	row    dbwire.ChainHole
	rowGap bool
	passed bool
	// inside is the number of the first record M above n that comes right after a record below n, and insideFirst the number that
	// the walk expects at it: if n is not there, it is in the gap before M, unless a row passes M (insidePassed).
	inside, insideFirst uint64
	insidePassed        bool
}

func (p *breakPass) event(e logdb.Event) bool {
	if dbwire.IsDaemonEvent(e.Data) {
		return true
	}
	log, n, prev, ok := dbwire.ReadRecordID(e.Data)
	if !ok || log != p.log {
		return true
	}
	sum := sha256.Sum256(e.Data)
	if !p.last.ok && p.start != nil && n < p.start.First {
		p.reach = n // a record that an earlier deletion left before the chain start
		return true
	}
	expected := dbwire.ChainExpected(p.start, p.reach)
	switch {
	case n == p.n:
		if p.seen++; p.seen > 1 {
			return false
		}
		p.sum, p.prev, p.first, p.why = sum, prev, !p.last.ok, followText(p.start, p.last, n, prev)
		p.before, p.expected = p.reach, expected
		p.row, p.rowGap, p.passed = dbwire.ChainPass(p.holes, p.gaps, p.start, p.reach, n, prev)
	case p.seen == 0 && p.inside == 0 && n > p.n && expected <= p.n:
		p.inside, p.insideFirst = n, expected
		_, _, p.insidePassed = dbwire.ChainPass(p.holes, p.gaps, p.start, p.reach, n, prev)
	}
	p.last, p.reach = chainLast{n: n, sum: sum, ok: true}, n
	return true
}

// decide returns what is to be accepted, with the hash of the record Last of a gap, or why nothing is, once the walk is done.
func (p *breakPass) decide(rows chainRows, head *auditHead, schema uint64) (Acceptance, [sha256.Size]byte, error) {
	log, n := p.log, p.n
	switch {
	case p.seen == 0:
		return p.absent(rows, head, schema)
	case p.seen > 1:
		return Acceptance{}, [sha256.Size]byte{}, fmt.Errorf("record %d of the audit log %s is in the store more than once, so a break at it cannot be "+
			"named by its number", n, log)
	case p.passed && !p.rowGap:
		return Acceptance{}, [sha256.Size]byte{}, fmt.Errorf("record %d comes after %s that whiteroom-db repair dropped: there is no break to accept", n,
			lostWords(p.row))
	case p.passed:
		return Acceptance{}, [sha256.Size]byte{}, fmt.Errorf("record %d comes after %s whose gap is accepted already", n, lostWords(p.row))
	case n < p.expected:
		return Acceptance{}, [sha256.Size]byte{}, fmt.Errorf("record %d does not follow record %d in number: it is below that record, which is "+
			"neither a break nor a gap that can be accepted", n, p.before)
	case n > p.expected:
		hash, ok := parseHash(p.prev)
		switch {
		case !ok && p.before == 0:
			return Acceptance{}, [sha256.Size]byte{}, fmt.Errorf("record %d is not record %d, which the audit log %s starts with, and its prev is not the "+
				"hash of a record (sha256: and 64 lower case hexadecimal digits), so the gap before it cannot be accepted", n, p.expected, log)
		case !ok:
			return Acceptance{}, [sha256.Size]byte{}, fmt.Errorf("record %d does not follow record %d in number, and its prev is not the hash of a record "+
				"(sha256: and 64 lower case hexadecimal digits), so the gap before it cannot be accepted", n, p.before)
		case schema != 0:
			return Acceptance{}, [sha256.Size]byte{}, olderSchemaRefusal(schema, "gap")
		}
		return Acceptance{Gap: true, First: p.expected, Last: n - 1}, hash, nil
	case p.first && p.why == "" && p.start == nil:
		return Acceptance{}, [sha256.Size]byte{}, fmt.Errorf("record %d has no prev, as the first record of a log has none: there is no break to accept", n)
	case p.first && p.why == "":
		return Acceptance{}, [sha256.Size]byte{}, fmt.Errorf("record %d has as its prev the hash that the chain start has of record %d: there is no break to accept", n, n-1)
	case !dbwire.BreakHonored(p.start, p.before, n):
		return Acceptance{}, [sha256.Size]byte{}, fmt.Errorf("record %d comes after records before it that an earlier deletion left, which are still in the store: "+
			"whiteroom-db compact drops them if they are what the deletion left; accept the break at record %d then", n, n)
	case p.why == "":
		return Acceptance{}, [sha256.Size]byte{}, fmt.Errorf("record %d follows record %d: there is no break to accept", n, n-1)
	case rows.accepted(n, p.sum):
		return Acceptance{}, [sha256.Size]byte{}, fmt.Errorf("the break at record %d of the audit log %s is already accepted", n, log)
	case schema != 0:
		return Acceptance{}, [sha256.Size]byte{}, olderSchemaRefusal(schema, "break")
	}
	return Acceptance{}, [sha256.Size]byte{}, nil
}

// absent is decide for a record that the log does not have: it is the head of the log, the gap at its end, or it is not there.
func (p *breakPass) absent(rows chainRows, head *auditHead, schema uint64) (Acceptance, [sha256.Size]byte, error) {
	log, n := p.log, p.n
	expected := dbwire.ChainExpected(p.start, p.reach)
	notThere := fmt.Errorf("record %d of the audit log %s is not in the store", n, log)
	if head != nil && head.n == n && n >= expected {
		row, gap, passed := dbwire.ChainEndPass(rows.holes, rows.gaps, p.start, p.reach, head.n, head.hash)
		switch {
		case passed && gap:
			return Acceptance{}, [sha256.Size]byte{}, fmt.Errorf("the gap of %s at the end of the audit log %s is accepted already", lostWords(row), log)
		case passed:
			return Acceptance{}, [sha256.Size]byte{}, notThere
		case schema != 0:
			return Acceptance{}, [sha256.Size]byte{}, olderSchemaRefusal(schema, "gap")
		}
		return Acceptance{Gap: true, End: true, First: expected, Last: n, NoRecord: p.reach == 0}, head.hash, nil
	}
	if p.inside != 0 && !p.insidePassed {
		return Acceptance{}, [sha256.Size]byte{}, fmt.Errorf("%w: it is in the gap of %s before record %d; accept the gap with -record %d", notThere,
			recordsWords(p.insideFirst, p.inside-1), p.inside, p.inside)
	}
	return Acceptance{}, [sha256.Size]byte{}, notThere
}

// olderSchemaRefusal is the refusal of a store of the table schema schema, which is older than dbwire.TableVersion: what is to
// be accepted (a break or a gap) is accepted when a writer has upgraded the store.
func olderSchemaRefusal(schema uint64, what string) error {
	return fmt.Errorf("the store has the table schema %d, and the first writer that connects through "+
		"whiteroom-db serve (whiteroom serve admin, for one) upgrades it to %d: accept the %s then", schema, dbwire.TableVersion, what)
}

// lostWords returns "the records A to B", or "the record A" if they are one, for the records of h.
func lostWords(h dbwire.ChainHole) string {
	if h.First == h.Last {
		return fmt.Sprintf("the record %d", h.First)
	}
	return fmt.Sprintf("the records %d to %d", h.First, h.Last)
}

// recordsWords returns "the records A to B", or "record A" if they are one.
func recordsWords(first, last uint64) string {
	if first == last {
		return fmt.Sprintf("record %d", first)
	}
	return fmt.Sprintf("the records %d to %d", first, last)
}
