package dbserver

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
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
	// with: a gap at the end of an empty log. The activity log always has a record, so for it VerifyAudit goes on reporting the log
	// as empty until the writer has added one.
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
// of a repair and no gap that is accepted already passes. The holes and the gaps that lead on from the record that the walk
// expects (dbwire.ChainReach) account for the records up to their end, R: the records E to N-1, with E = R+1, are missing, and the
// record N has a prev that names the hash of N-1: the row has the first E as its key, N-1 and that hash, which is the rule of a
// hole, and no more is passed than the row says. It is the gap that whiteroom-db repair tells in its "gap:" line. A gap at the end
// of the log is accepted at the number N of the head of the log (the table heads), when the log has no record N and none above the
// last that was read: the records E to N are missing, E counted in the same way, and the row has the hash of the head. If the last
// of the rows reaches the record before N (at the end, the head) without passing it, because the prev of N (the hash of the head)
// is not its hash, which a rewrite of N (or a change of the head) leaves, the acceptance is of that row again: the gap is the
// range of that row, with the new hash. A gap row at that place is replaced by it, and the event of the acceptance is added once
// more; a hole row stays, and the gap row is put beside it, at its own key. The gap is refused if it would lie over another row
// that is there, which is wider, or starts inside it. Whether the record N is the first that the walk reads is no reason to refuse a gap before it, and a first record
// that is not the one that the log starts with is a gap, not a break. A gap that goes across the first record that remains after a
// deletion (K), from the records that the deletion left or from none, is accepted if what the walk knows of the record K-1 has the
// hash that the chain start has (dbwire.ChainCross, as VerifyAudit and the walk of compact step across K): the last record that
// was read, if it is K-1, a row before the gap that ends with K-1, or the prev of the record after the gap, if the gap ends with
// K-1; a gap that holds K-1 and K leaves none to check.
//
// It refuses, and writes nothing, a record that is not in the store (also one before the chain start, which is not looked at,
// and one whose number the log has twice), a record that follows a hole that a repair has recorded (the table chains has it,
// with the hash that the record has as its prev: VerifyAudit passes the record, so there is nothing to accept; the hole is not
// a break, and the records that it lost are not there to be named) or a gap that is accepted already, or rows of them that
// follow one another, which the refusal names, a record whose number is below that of a record before it, the gap before a
// record whose prev is not the hash of a record, a gap across K whose hash of K-1 is not the chain start's (the problem would
// stay, since VerifyAudit fails at it), a gap that would lie over a hole or a gap that is there, or that no records are missing
// for (the rows lead on to the record before the one that is named, which has not their hash as its prev: planGap; a row is never
// replaced), record 1 with no prev and K whose prev is the hash that the chain start has (no break is
// there), K while records before it that a deletion left are in the store (whiteroom-db compact drops them if they are what the
// deletion left), a record that follows the one before it, a break that is accepted already for the record as it is, a chain
// whose rows cannot be read, and a store of an older table schema than dbwire.TableVersion, for the reason of CompactLogin. An event that is no record has no number to be named by, and is not
// looked at. The row of a gap is checked to read back (gapValue) before anything is written.
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
	var value []byte
	if acc.Gap {
		if value, err = gapValue(acc.First, acc.Last, hash, now); err != nil {
			return Acceptance{}, err
		}
	}
	err = m.Update(ctx, func(tx *logdb.Tx) error {
		if acc.Gap {
			if err := tx.Put(dbwire.TableChains, dbwire.ChainGapKey(log, acc.First), value); err != nil {
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

// gapValue returns the value of the row of the gap of the records first to last, whose record after it has the prev hash. It
// refuses a row that VerifyAudit could not read back: a row that is written is never one that stops every walk of the log.
func gapValue(first, last uint64, hash [sha256.Size]byte, at time.Time) ([]byte, error) {
	value := dbwire.AppendChainHole(nil, dbwire.ChainHole{Last: last, Hash: hash, At: at})
	if _, ok := dbwire.ParseChainHole(first, value); !ok {
		return nil, fmt.Errorf("the row of the gap of the records %d to %d could not be read back, so it is not written", first, last)
	}
	return value, nil
}

// gapWhy is why a gap is not accepted.
type gapWhy int

const (
	gapOK gapWhy = iota
	// gapOverlaps: a row that is there lies over the records of the gap.
	gapOverlaps
	// gapCrosses: the hash of the record K-1 that is known is not the chain start's.
	gapCrosses
)

// gapPlan is the gap that accept-break writes before the record n, or at the end of the log, and the rows before it.
type gapPlan struct {
	why  gapWhy
	rows []dbwire.PassedRow // the holes and the gaps that lead on from the record that the walk expects, before the gap
	gap  dbwire.PassedRow   // the gap: from the record after the rows, with the hash of the record after it (of the head)
	// again is the last row, which the gap is the acceptance of again: it reaches the record before the gap's end, and does not pass
	// the record after it. The gap has its range, and is not one of rows. nil if the gap is new.
	again *dbwire.PassedRow
	// row is the row that the gap would lie over (gapOverlaps), or the row with another hash than the chain start's (gapCrosses);
	// nil for the last record that was read.
	row *dbwire.PassedRow
}

// planGap returns the gap before the record n of a log with the rows holes and gaps and the chain start start (nil if none), whose
// prev names the hash hash, for a walk whose last record read is last (0 if none; lastSum is its hash), or, if end, the gap at the
// end of the log, whose head is n and has the hash hash. The gap starts after the rows that lead on from the record that the walk
// expects (dbwire.ChainReach), and ends with the record before n (with n, at the end); if the last of them ends there, it is the
// range of that row, which is accepted again. It is the gap that whiteroom-db repair tells, and it is refused where accept-break
// refuses it.
func planGap(holes, gaps map[uint64]dbwire.ChainHole, start *dbwire.ChainStart, last uint64, lastSum [sha256.Size]byte, n uint64, hash [sha256.Size]byte,
	end bool) gapPlan {
	lastMissing := n - 1
	if end {
		lastMissing = n
	}
	p := gapPlan{rows: dbwire.ChainReach(holes, gaps, start, last, lastMissing+1)}
	first := dbwire.ChainExpected(start, last)
	if k := len(p.rows); k > 0 {
		if row := p.rows[k-1]; row.Last == lastMissing {
			p.again, first, p.rows = &row, row.First, p.rows[:k-1]
		} else {
			first = row.Last + 1
		}
	}
	p.gap = dbwire.PassedRow{ChainHole: dbwire.ChainHole{First: first, Last: lastMissing, Hash: hash}, Gap: true}
	if row := overlapping(holes, gaps, first, lastMissing, p.again != nil); row != nil {
		p.why, p.row = gapOverlaps, row
		return p
	}
	all := append(slices.Clone(p.rows), p.gap)
	if ok, bad := dbwire.ChainCross(start, last, lastSum, all); !ok {
		p.why, p.row = gapCrosses, bad
	}
	return p
}

// overlapping returns a row, the one with the lowest first record, a hole before a gap, that holds a record from first to last, nil
// if none does. If again, a row that starts with first and ends with last is the row that the gap is the acceptance of again, and is
// not one.
func overlapping(holes, gaps map[uint64]dbwire.ChainHole, first, last uint64, again bool) *dbwire.PassedRow {
	var found *dbwire.PassedRow
	look := func(rows map[uint64]dbwire.ChainHole, gap bool) {
		for key, row := range rows {
			if again && key == first && row.Last == last {
				continue
			}
			if key <= last && row.Last >= first && (found == nil || key < found.First) {
				row.First = key
				found = &dbwire.PassedRow{ChainHole: row, Gap: gap}
			}
		}
	}
	look(holes, false)
	look(gaps, true)
	return found
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
	// are not, and reach the number of the last record of any kind that it has read, which they are, with the SHA-256 of its stored
	// bytes: what a gap is a gap of.
	last     chainLast
	reach    uint64
	reachSum [sha256.Size]byte

	seen      int // how many records of the log have the number n
	sum       [sha256.Size]byte
	prev      string // the prev of the first of them
	why       string // why the first of them does not follow the walk before it, "" if it does
	first     bool   // it is the first record of the walk
	before    uint64 // the number of the last record read before it, counting those that a deletion left; 0 if none
	beforeSum [sha256.Size]byte
	// expected is the number that the walk expects at the first of them: the record after the one before it.
	expected uint64
	// rows are the holes and the accepted gaps that pass it, as VerifyAudit passes it, nil if none does.
	rows []dbwire.PassedRow
	// inside is the number of the first record M above n that comes right after a record below n, and insideFirst the number that
	// the walk expects at it: if n is not there, it is in the gap before M, unless a row passes M (insidePassed) or the gap would
	// not be accepted (insideRefused).
	inside, insideFirst uint64
	insidePassed        bool
	insideRefused       bool
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
		p.reach, p.reachSum = n, sum // a record that an earlier deletion left before the chain start
		return true
	}
	expected := dbwire.ChainExpected(p.start, p.reach)
	switch {
	case n == p.n:
		if p.seen++; p.seen > 1 {
			return false
		}
		p.sum, p.prev, p.first, p.why = sum, prev, !p.last.ok, followText(p.start, p.last, n, prev)
		p.before, p.beforeSum, p.expected = p.reach, p.reachSum, expected
		p.rows = dbwire.ChainPass(p.holes, p.gaps, p.start, p.reach, n, prev)
	case p.seen == 0 && p.inside == 0 && n > p.n && expected <= p.n:
		p.inside, p.insideFirst = n, expected
		p.insidePassed = dbwire.ChainPass(p.holes, p.gaps, p.start, p.reach, n, prev) != nil
		if hash, ok := parseHash(prev); ok {
			plan := planGap(p.holes, p.gaps, p.start, p.reach, p.reachSum, n, hash, false)
			p.insideFirst, p.insideRefused = plan.gap.First, plan.why != gapOK
		}
	}
	p.last, p.reach, p.reachSum = chainLast{n: n, sum: sum, ok: true}, n, sum
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
	case p.rows != nil:
		return Acceptance{}, [sha256.Size]byte{}, errors.New(passedText(n, p.rows))
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
		}
		plan := planGap(p.holes, p.gaps, p.start, p.before, p.beforeSum, n, hash, false)
		gap := plan.gap
		switch {
		case plan.why == gapOverlaps:
			return Acceptance{}, [sha256.Size]byte{}, fmt.Errorf("the gap of %s before record %d would lie over %s: no gap is accepted over a row "+
				"that is there", recordsWords(gap.First, gap.Last), n, rowWords(*plan.row))
		case plan.why == gapCrosses && plan.row == nil:
			return Acceptance{}, [sha256.Size]byte{}, fmt.Errorf("record %d comes after records before it that an earlier deletion left, the last "+
				"of which, record %d, does not have the hash that the chain start has of it: the gap before record %d is not accepted", n, p.before, n)
		case plan.why == gapCrosses && plan.row.First == gap.First:
			return Acceptance{}, [sha256.Size]byte{}, fmt.Errorf("the gap of %s before record %d is not accepted: record %d does not have as its "+
				"prev the hash that the chain start has of record %d", recordsWords(gap.First, gap.Last), n, n, gap.Last)
		case plan.why == gapCrosses:
			return Acceptance{}, [sha256.Size]byte{}, fmt.Errorf("the gap of %s before record %d is not accepted: %s does not have the hash that the "+
				"chain start has of record %d", recordsWords(gap.First, gap.Last), n, rowWords(*plan.row), plan.row.Last)
		case schema != 0:
			return Acceptance{}, [sha256.Size]byte{}, olderSchemaRefusal(schema, "gap")
		}
		return Acceptance{Gap: true, First: gap.First, Last: gap.Last}, hash, nil
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
		passedRows := dbwire.ChainEndPass(rows.holes, rows.gaps, p.start, p.reach, head.n, head.hash)
		plan := planGap(rows.holes, rows.gaps, p.start, p.reach, p.reachSum, n, head.hash, true)
		gap := plan.gap
		switch {
		case passedRows != nil && passedRows[len(passedRows)-1].Gap:
			return Acceptance{}, [sha256.Size]byte{}, fmt.Errorf("the gap of %s at the end of the audit log %s is accepted already",
				lostWords(passedRows[len(passedRows)-1].ChainHole), log)
		case passedRows != nil:
			return Acceptance{}, [sha256.Size]byte{}, notThere
		case plan.why == gapOverlaps:
			return Acceptance{}, [sha256.Size]byte{}, fmt.Errorf("the gap of %s at the end of the audit log %s would lie over %s: no gap is accepted "+
				"over a row that is there", recordsWords(gap.First, gap.Last), log, rowWords(*plan.row))
		case plan.why == gapCrosses && plan.row == nil:
			// The head n is at or after the chain start (a record before it is refused first), so the gap does not end with K-1: only the
			// record that was read before it can have the hash that is not the chain start's.
			return Acceptance{}, [sha256.Size]byte{}, fmt.Errorf("the gap at the end of the audit log %s comes after records before it that an "+
				"earlier deletion left, the last of which, record %d, does not have the hash that the chain start has of it: the gap is not accepted",
				log, p.reach)
		case plan.why == gapCrosses:
			return Acceptance{}, [sha256.Size]byte{}, fmt.Errorf("the gap at the end of the audit log %s is not accepted: %s does not have the hash "+
				"that the chain start has of record %d", log, rowWords(*plan.row), plan.row.Last)
		case schema != 0:
			return Acceptance{}, [sha256.Size]byte{}, olderSchemaRefusal(schema, "gap")
		}
		return Acceptance{Gap: true, End: true, First: gap.First, Last: n, NoRecord: p.reach == 0}, head.hash, nil
	}
	if p.inside != 0 && !p.insidePassed && !p.insideRefused && p.insideFirst <= n {
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

// passedText is the refusal of the record n that rows pass: there is no break or gap to accept.
func passedText(n uint64, rows []dbwire.PassedRow) string {
	var holes, gaps bool
	var parts []string
	for _, row := range rows {
		if row.Gap {
			gaps = true
			parts = append(parts, lostWords(row.ChainHole)+" whose gap is accepted already")
		} else {
			holes = true
			parts = append(parts, lostWords(row.ChainHole)+" that whiteroom-db repair dropped")
		}
	}
	// One kind of rows are told by their records and then by what they are, as one row is.
	if !holes || !gaps {
		words := make([]string, len(rows))
		for i, row := range rows {
			words[i] = lostWords(row.ChainHole)
		}
		suffix := " whose gap is accepted already"
		if !gaps {
			suffix = " that whiteroom-db repair dropped: there is no break to accept"
		}
		return fmt.Sprintf("record %d comes after %s%s", n, joinWords(words), suffix)
	}
	return fmt.Sprintf("record %d comes after %s: there is no break to accept", n, joinWords(parts))
}

// joinWords joins words as a list, with a comma and then "and" for the last: "a", "a and b", "a, b and c".
func joinWords(words []string) string {
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// rowWords tells the row that passes a record or the head: "the hole of the records A to B", or "the accepted gap of ...".
func rowWords(row dbwire.PassedRow) string {
	kind := "hole"
	if row.Gap {
		kind = "accepted gap"
	}
	if row.First == row.Last {
		return fmt.Sprintf("the %s of record %d", kind, row.First)
	}
	return fmt.Sprintf("the %s of the records %d to %d", kind, row.First, row.Last)
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
