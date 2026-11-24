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

// AcceptBreak accepts the break at record n of the audit log named log (dbwire.LogLogin or dbwire.LogActivity): a record
// whose number is that of the one before it plus one and whose prev is not its hash, which is where VerifyAudit of the store
// fails. In one Update it puts the row of the table chains that ties the acceptance to the SHA-256 of the stored bytes of
// the record, appends the event of the acceptance and a mark of now. From then on VerifyAudit checks the log again from that
// record, and any change to the record undoes the acceptance. It reads the records as audit.Decode does
// (dbwire.ReadRecordID), uses only their log, number and prev, and changes and drops none.
//
// It refuses, and writes nothing, a record that is not in the store (also one before the chain start, and one whose number
// the log has twice), the first record that the log is checked from, which has no record before it to break from, a record
// whose number does not follow that of the one before it (a gap, which a lost record leaves: the repair of the engine
// handles it), a record that follows the one before it, a break that is accepted already for the record as it is, a chain
// whose rows cannot be read, and a store of an older table schema than dbwire.TableVersion, for the reason of CompactLogin.
// An event that is no record has no number to be named by, and is not looked at.
func AcceptBreak(ctx context.Context, m *logdb.Maintenance, log string, n uint64, now time.Time) error {
	if log != dbwire.LogLogin && log != dbwire.LogActivity {
		return fmt.Errorf("the audit log %q is neither %s nor %s", log, dbwire.LogLogin, dbwire.LogActivity)
	}
	if n == 0 {
		return errors.New("the records of an audit log are numbered from 1")
	}
	now = now.UTC()
	var rows chainRows
	var schema uint64
	if err := m.View(ctx, func(tx *logdb.Tx) error {
		schema = olderSchema(tx)
		rows = readChainRows(tx, log)
		return nil
	}); err != nil {
		return err
	}
	if rows.bad == "" && log != dbwire.LogLogin && rows.start != nil {
		rows.bad = "the table chains: a chain start for a log of which only the login records are ever deleted"
	}
	switch {
	case rows.bad != "":
		return fmt.Errorf("the chain of the audit log %s cannot be read, so no break of it is accepted (%s)", log, rows.bad)
	case rows.start != nil && n < rows.start.First:
		return fmt.Errorf("record %d of the audit log %s is not in the store: the records before record %d were deleted", n, log,
			rows.start.First)
	}
	p := &breakPass{log: log, n: n, start: rows.start}
	if err := m.Events(ctx, p.event); err != nil {
		return err
	}
	switch {
	case p.seen == 0:
		return fmt.Errorf("record %d of the audit log %s is not in the store", n, log)
	case p.seen > 1:
		return fmt.Errorf("record %d of the audit log %s is in the store more than once, so a break at it cannot be named by its "+
			"number", n, log)
	case p.first:
		return fmt.Errorf("record %d is the first record that the audit log %s is checked from: a break there is not accepted", n, log)
	case p.gap:
		return fmt.Errorf("record %d does not follow record %d in number: a gap, which a lost record leaves, is not a break that can "+
			"be accepted, and waits for the repair of the engine", n, p.before)
	case p.why == "":
		return fmt.Errorf("record %d follows record %d: there is no break to accept", n, n-1)
	case rows.accepted(n, p.sum):
		return fmt.Errorf("the break at record %d of the audit log %s is already accepted", n, log)
	case schema != 0:
		return fmt.Errorf("the store has the table schema %d, and the first writer that connects through whiteroom-db serve "+
			"(whiteroom serve admin, for one) upgrades it to %d: accept the break then", schema, dbwire.TableVersion)
	}
	return m.Update(ctx, func(tx *logdb.Tx) error {
		accepted := dbwire.ChainBreak{Hash: p.sum, At: now}
		if err := tx.Put(dbwire.TableChains, dbwire.ChainBreakKey(log, n), dbwire.AppendChainBreak(nil, accepted)); err != nil {
			return err
		}
		if err := tx.AppendEvent(dbwire.AppendAccepted(nil, dbwire.Accepted{Log: log, N: n, Hash: p.sum})); err != nil {
			return err
		}
		return tx.AppendEvent(dbwire.AppendMark(nil, now))
	})
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
	last  chainLast

	seen   int // how many records of the log have the number n
	sum    [sha256.Size]byte
	why    string // why the first of them does not follow the walk before it, "" if it does
	first  bool   // it is the first record of the walk
	gap    bool   // its number does not follow that of the record before it
	before uint64 // the number of the record before it
}

func (p *breakPass) event(e logdb.Event) bool {
	if dbwire.IsDaemonEvent(e.Data) {
		return true
	}
	log, n, prev, ok := dbwire.ReadRecordID(e.Data)
	if !ok || log != p.log {
		return true
	}
	if !p.last.ok && p.start != nil && n < p.start.First {
		return true // a record that an earlier deletion left before the chain start
	}
	sum := sha256.Sum256(e.Data)
	if n == p.n {
		if p.seen++; p.seen > 1 {
			return false
		}
		p.sum, p.first, p.why = sum, !p.last.ok, followText(p.start, p.last, n, prev)
		p.gap, p.before = p.last.ok && n != p.last.n+1, p.last.n
	}
	p.last = chainLast{n: n, sum: sum, ok: true}
	return true
}
