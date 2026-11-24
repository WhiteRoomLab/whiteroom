package dbserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// Deleted is what CompactLogin did to the login history.
type Deleted struct {
	// First and Last are the numbers of the first and of the last login record that the compaction dropped: 0 if it
	// dropped none.
	First, Last uint64
	// Cutoff is the time that the dropped records are older than.
	Cutoff time.Time
	// Stopped says where and why the deletion stopped at evidence that would have let it go further, such as
	// "record 7: prev is not the hash of record 6"; "" if it did not.
	Stopped string
	// Schema is the table schema of the store if it is older than dbwire.TableVersion and records were kept for that
	// reason, which is why First and Last are 0; else 0.
	Schema uint64
}

// CompactLogin is m.Compact that also drops the oldest records of the login history: those whose commit the daemon marked
// more than one calendar year before the earlier of now and the newest mark of the store. The activity log, and every
// event that is not a login record, are kept.
//
// The records go as a prefix in the order of their events, and only while their chain is whole. The walk stops at a record
// that is not old enough or has no mark, and at evidence: a record that does not follow the one before it (unless a break
// is accepted for it), an event that is no record, a chain start that cannot be read. At evidence it also keeps the record
// before, so that the pair stays for a repair.
//
// If records are to go, one Update first commits the new chain start, the event of the deletion and a mark of now, so that
// VerifyAudit checks the log from the first record that remains at every point. A compaction that stops before its end
// leaves records. The next call drops them, whatever their age and without a new chain start, only if they are exactly the
// records before the chain start (they follow each other, the last is the record before it, with the hash that the chain
// start has) and the store has the event of that deletion. Otherwise they are evidence, and the call keeps them and
// says where it stopped. The cutoff of a call that drops only them is that of the event of the deletion.
//
// The mark that the commit of a deletion or of an acceptance of a break appends is the time of that trace, and not the
// newest mark: a store that has had no commit since is not cut further by the compactions that follow.
//
// A store of a table schema older than dbwire.TableVersion is compacted and nothing is dropped from it: the chain start
// and the event of a deletion are of the schema 8, which the first writer that opens the store (store.Open) makes it.
// Deleted.Schema says so.
//
// It reads every event once before it compacts, and keeps the hash and the time of each login record that it walks up to
// the first that is not old enough.
func CompactLogin(ctx context.Context, m *logdb.Maintenance, now time.Time) (Deleted, error) {
	now = now.UTC()
	p := &loginPass{upper: now.AddDate(-1, 0, 0)}
	if err := m.View(ctx, p.readChains); err != nil {
		return Deleted{}, err
	}
	if err := m.Events(ctx, p.event); err != nil {
		return Deleted{}, err
	}
	p.end()
	d := p.plan(now)
	var schema uint64
	if p.schema != 0 && (d.cut > 0 || d.limit > 0) {
		schema = p.schema
		d.cut, d.limit, d.dropFirst, d.dropLast = 0, 0, 0, 0
	}
	if d.cut > 0 {
		last := p.recs[d.cut-1]
		err := m.Update(ctx, func(tx *logdb.Tx) error {
			start := dbwire.ChainStart{First: d.first, Prev: last.sum, At: now}
			if err := tx.Put(dbwire.TableChains, dbwire.ChainStartKey(dbwire.LogLogin), dbwire.AppendChainStart(nil, start)); err != nil {
				return err
			}
			deleted := dbwire.Deleted{Log: dbwire.LogLogin, First: d.first, Prev: last.sum, Cutoff: d.cutoff}
			if err := tx.AppendEvent(dbwire.AppendDeleted(nil, deleted)); err != nil {
				return err
			}
			return tx.AppendEvent(dbwire.AppendMark(nil, now))
		})
		if err != nil {
			return Deleted{}, err
		}
	}
	var r logdb.Rewrite
	if d.limit > 0 {
		r.Event = func(e logdb.Event) ([]byte, bool) {
			if e.Number <= d.limit && !dbwire.IsDaemonEvent(e.Data) {
				if log, n, _, ok := dbwire.ReadRecordID(e.Data); ok && log == dbwire.LogLogin && n < d.first {
					return nil, false
				}
			}
			return e.Data, true
		}
	}
	if err := m.Compact(ctx, r); err != nil {
		return Deleted{}, err
	}
	return Deleted{First: d.dropFirst, Last: d.dropLast, Cutoff: d.cutoff, Stopped: d.stopped, Schema: schema}, nil
}

// loginRecord is a login record that the walk has accepted.
type loginRecord struct {
	n, event uint64
	sum      [sha256.Size]byte
	at       time.Time // the time of the mark of its commit; zero if the commit has none
}

// loginPass is the pass of CompactLogin over the events.
type loginPass struct {
	upper  time.Time // a record that is not older than this is never dropped, so the walk does not keep any after it
	start  *dbwire.ChainStart
	breaks map[uint64][sha256.Size]byte // by record: the hash that the accepted break at it is for
	schema uint64                       // the table schema of the store if it is older than dbwire.TableVersion, else 0

	recs    []loginRecord // the walked records, in the order of their events
	group   int           // recs[group:] are the records of the commit seq, whose mark has not been read
	seq     uint64
	blocked bool // the last commit has no mark or is not older than upper: the walk goes no further
	// stop is what ended the walk at evidence, and stopLogin is whether a login record was at or after it.
	stop      string
	stopLogin bool
	left      []leftover // the login records before the chain start that come before the first walked record
	deleted   *dbwire.Deleted
	newest    time.Time
	haveMark  bool
	// trace is whether the commit of the sequence number traceSeq has a deletion or an acceptance of a break: its mark is
	// the time of that trace, which is not that of a commit of the writer.
	trace    bool
	traceSeq uint64
}

// leftover is a login record before the chain start: one that an earlier deletion left, if the chain start says so.
type leftover struct {
	n, event uint64
	sum      [sha256.Size]byte
}

func (p *loginPass) readChains(tx *logdb.Tx) error {
	p.schema = olderSchema(tx)
	rows := readChainRows(tx, dbwire.LogLogin)
	p.start, p.breaks, p.stop = rows.start, rows.breaks, rows.bad
	return nil
}

// chainRows is what the table chains has of one log: its chain start, and the hash that each accepted break is for, by the
// number of its record.
type chainRows struct {
	start  *dbwire.ChainStart
	breaks map[uint64][sha256.Size]byte
	bad    string // why a row cannot be read, "" if every row can
}

// readChainRows reads the rows of the table chains of the log named log. A row that cannot be read stops the read.
func readChainRows(tx *logdb.Tx, log string) chainRows {
	var rows chainRows
	if v, ok := tx.Get(dbwire.TableChains, dbwire.ChainStartKey(log)); ok {
		start, valid := dbwire.ParseChainStart(v)
		if !valid {
			rows.bad = "the table chains: the chain start cannot be read"
			return rows
		}
		rows.start = &start
	}
	tx.Scan(dbwire.TableChains, dbwire.ChainBreakPrefix(log), func(k, v []byte) bool {
		name, n, keyOK := dbwire.ParseChainBreakKey(k)
		b, valueOK := dbwire.ParseChainBreak(v)
		if !keyOK || !valueOK || name != log || n == 0 {
			rows.bad = "the table chains: a row of an accepted break cannot be read"
			return false
		}
		if rows.breaks == nil {
			rows.breaks = map[uint64][sha256.Size]byte{}
		}
		rows.breaks[n] = b.Hash
		return true
	})
	return rows
}

// olderSchema returns the table schema of the store if it is older than dbwire.TableVersion, else 0. A row that is invalid
// or newer is refused by Maintain, and a store with no commit has none.
func olderSchema(tx *logdb.Tx) uint64 {
	if v, ok := tx.Get(dbwire.TableMeta, []byte(dbwire.MetaSchema)); ok {
		if version, valid := schemaVersion(v); valid && version < dbwire.TableVersion {
			return version
		}
	}
	return 0
}

func (p *loginPass) event(e logdb.Event) bool {
	if p.group < len(p.recs) && e.Seq != p.seq {
		p.settle(time.Time{})
	}
	if dbwire.IsDaemonEvent(e.Data) {
		switch t, ok := dbwire.ParseMark(e.Data); {
		case ok:
			if (!p.haveMark || t.After(p.newest)) && (!p.trace || e.Seq != p.traceSeq) {
				p.newest, p.haveMark = t, true
			}
			if p.group < len(p.recs) {
				p.settle(t)
			}
		case !dbwire.ValidEvent(e.Data):
			p.fail(fmt.Sprintf("event %d: it is an event of the daemon that cannot be read", e.Number))
		default:
			p.trace, p.traceSeq = true, e.Seq
			if d, valid := dbwire.ParseDeleted(e.Data); valid && d.Log == dbwire.LogLogin && p.start != nil && d.First == p.start.First &&
				d.Prev == p.start.Prev && p.deleted == nil {
				p.deleted = &d
			}
		}
		return true
	}
	if p.blocked {
		return true // the age has ended the walk: only the marks matter now
	}
	switch log, n, prev, ok := dbwire.ReadRecordID(e.Data); {
	case !ok:
		p.fail(fmt.Sprintf("event %d: it is not an audit record", e.Number))
	case log == dbwire.LogLogin:
		p.login(e, n, prev)
	}
	return true
}

// fail ends the walk at what, unless the walk has ended.
func (p *loginPass) fail(what string) {
	if p.stop == "" && !p.blocked {
		p.stop = what
	}
}

// settle gives the records of the commit that ends the mark at, which is zero if it has none.
func (p *loginPass) settle(at time.Time) {
	for i := p.group; i < len(p.recs); i++ {
		p.recs[i].at = at
	}
	if at.IsZero() || !at.Before(p.upper) {
		p.blocked = true
	}
	p.group = len(p.recs)
}

// end gives the records of the last commit, which has no mark if none has followed, and ends the records before the chain
// start if no walked record has.
func (p *loginPass) end() {
	if p.group < len(p.recs) {
		p.settle(time.Time{})
	}
	if len(p.recs) == 0 && p.stop == "" && p.start != nil {
		if why := p.leftEnd(0); why != "" {
			p.stop, p.stopLogin = why, true
		}
	}
}

func (p *loginPass) login(e logdb.Event, n uint64, prev string) {
	switch {
	case p.stop != "":
		p.stopLogin = true
		return
	case p.blocked:
		return
	}
	sum := sha256.Sum256(e.Data)
	if len(p.recs) == 0 && p.start != nil {
		if n < p.start.First {
			if why := p.leftFollow(n, prev, sum); why != "" {
				p.stop, p.stopLogin = fmt.Sprintf("record %d: %s", n, why), true
				return
			}
			p.left = append(p.left, leftover{n: n, event: e.Number, sum: sum})
			return
		}
		if why := p.leftEnd(n); why != "" {
			p.stop, p.stopLogin = why, true
			return
		}
	}
	if why := p.follow(n, prev); why != "" && !p.accepted(n, sum) {
		p.stop, p.stopLogin = fmt.Sprintf("record %d: %s", n, why), true
		return
	}
	if p.group == len(p.recs) {
		p.seq = e.Seq
	}
	p.recs = append(p.recs, loginRecord{n: n, event: e.Number, sum: sum})
}

// leftFollow returns why the record n with prev, whose stored bytes have the hash sum, does not follow the records before
// the chain start that were met, "" if it does: a break that is accepted for it lets it go, as VerifyAudit has it. The first
// of them is not checked: the record before it was deleted.
func (p *loginPass) leftFollow(n uint64, prev string, sum [sha256.Size]byte) string {
	if len(p.left) == 0 {
		return ""
	}
	last := p.left[len(p.left)-1]
	switch {
	case n != last.n+1:
		return fmt.Sprintf("the number does not follow that of record %d", last.n)
	case prev != hashText(last.sum) && !p.breakFor(n, sum):
		return fmt.Sprintf("prev is not the hash of record %d", last.n)
	}
	return ""
}

// leftEnd returns why the records before the chain start that were met are not what an earlier deletion leaves, which is
// the end of the records before the chain start, "" if they are or there are none. next is the number of the record that
// comes after them, 0 if none does.
func (p *loginPass) leftEnd(next uint64) string {
	if len(p.left) == 0 {
		return ""
	}
	last := p.left[len(p.left)-1]
	switch {
	case last.n+1 != p.start.First && next != 0:
		return fmt.Sprintf("record %d: the number does not follow that of record %d", next, last.n)
	case last.n+1 != p.start.First:
		return fmt.Sprintf("record %d: the chain starts at record %d, so the records before it end with record %d", last.n,
			p.start.First, p.start.First-1)
	case last.sum != p.start.Prev:
		return fmt.Sprintf("record %d: its hash is not the hash that the chain start has of it", last.n)
	}
	return ""
}

// follow returns why the record n with prev does not follow the walked records, "" if it does.
func (p *loginPass) follow(n uint64, prev string) string {
	var last chainLast
	if len(p.recs) > 0 {
		r := p.recs[len(p.recs)-1]
		last = chainLast{n: r.n, sum: r.sum, ok: true}
	}
	return followText(p.start, last, n, prev)
}

// chainLast is the last record that a walk of the records of a log has accepted: its number and the SHA-256 of its stored
// bytes. The zero value is no record.
type chainLast struct {
	n   uint64
	sum [sha256.Size]byte
	ok  bool
}

// followText returns why the record n with prev does not follow the walk that is at last, "" if it does: the first record is
// the chain start (or 1), and each of the others follows its predecessor in number and in hash.
func followText(start *dbwire.ChainStart, last chainLast, n uint64, prev string) string {
	if !last.ok {
		switch {
		case start == nil && n != 1:
			return "the first record of a log is number 1"
		case start == nil && prev != "":
			return "the first record has the hash of one before it"
		case start != nil && n > start.First:
			return fmt.Sprintf("the chain starts at record %d, which is not there", start.First)
		case start != nil && prev != hashText(start.Prev):
			return fmt.Sprintf("prev is not the hash that the chain start has of record %d", start.First-1)
		}
		return ""
	}
	switch {
	case n != last.n+1:
		return fmt.Sprintf("the number does not follow that of record %d", last.n)
	case prev != hashText(last.sum):
		return fmt.Sprintf("prev is not the hash of record %d", last.n)
	}
	return ""
}

// accepted reports whether a break is accepted for the record n whose stored bytes have the hash sum: it is not the first
// record that was walked, and its number follows that of the record before it, as VerifyAudit has it.
func (p *loginPass) accepted(n uint64, sum [sha256.Size]byte) bool {
	return p.breakFor(n, sum) && len(p.recs) > 0 && n == p.recs[len(p.recs)-1].n+1
}

// breakFor reports whether a break is accepted for the record n whose stored bytes have the hash sum.
func (p *loginPass) breakFor(n uint64, sum [sha256.Size]byte) bool {
	h, ok := p.breaks[n]
	return ok && h == sum
}

func hashText(sum [sha256.Size]byte) string {
	return "sha256:" + hex.EncodeToString(sum[:])
}

// deletion is what CompactLogin does after the pass.
type deletion struct {
	cutoff time.Time
	cut    int    // how many of the walked records are dropped
	first  uint64 // the first record that remains: the new chain start, or the one that is there if cut is 0
	// limit is the number of the last event that may be dropped, and 0 if none may: an event after it is no record that
	// the walk has passed, whatever its number says.
	limit               uint64
	dropFirst, dropLast uint64
	stopped             string
}

func (p *loginPass) plan(now time.Time) deletion {
	ref := now
	if p.haveMark && p.newest.Before(ref) {
		ref = p.newest
	}
	cutoff := ref.AddDate(-1, 0, 0)
	d := deletion{cutoff: cutoff}
	if p.start != nil {
		d.first = p.start.First
	}
	// The records before the chain start go if the walk has not stopped before its first record, which would make them the
	// record before the stop: they are exactly the end of the records before the chain start, and the store has the event
	// of that deletion.
	left := len(p.left) > 0 && (p.stop == "" || len(p.recs) > 0)
	if left && p.deleted == nil {
		d.stopped = fmt.Sprintf("record %d: the store has no event of a deletion that left the records before record %d", p.left[0].n,
			p.start.First)
		return d
	}
	if left {
		d.limit, d.dropFirst, d.dropLast = p.left[len(p.left)-1].event, p.left[0].n, p.left[len(p.left)-1].n
		d.cutoff = p.deleted.Cutoff
	}
	old := 0
	for old < len(p.recs) && !p.recs[old].at.IsZero() && p.recs[old].at.Before(cutoff) {
		old++
	}
	// The record before one that does not follow its predecessor stays, with it.
	eligible := len(p.recs)
	if p.stop != "" && eligible > 0 {
		eligible--
	}
	cut := min(old, eligible)
	if cut > 0 {
		d.cut = cut
		d.cutoff = cutoff
		d.first = p.recs[cut-1].n + 1
		if cut < len(p.recs) {
			d.first = p.recs[cut].n
		}
		d.limit = p.recs[cut-1].event
		d.dropLast = p.recs[cut-1].n
		if !left {
			d.dropFirst = p.recs[0].n
		}
	}
	if p.stop != "" && (p.stopLogin || (len(p.left) > 0 && len(p.recs) == 0)) && old == len(p.recs) {
		d.stopped = p.stop
	}
	return d
}
