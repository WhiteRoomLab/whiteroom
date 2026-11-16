package dbclient

import (
	"context"
	"errors"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// A window is a run of read requests that a connection writes with one Write call and whose replies it then reads, all of
// them, before it writes anything else: the daemon takes it with one read (dbwire.WindowBytes), so neither side waits for
// the other, also over net.Pipe.

// windowHold is what the Writer holds before it writes: a window and the largest request, so that no request of a
// window is written before the Flush.
const windowHold = dbwire.WindowBytes + dbwire.HeaderSize + scanFixed + dbwire.MaxKey

// The bytes of the body of a request that a key or a prefix does not make: a table and the length of a key; and, in a
// Scan, also the length of the prefix, the flag and the length of the key after, and the limit.
const (
	getFixed  = 4 + 4
	scanFixed = 4 + 4 + 1 + 4 + 4
)

// prefetchBudget bounds the bytes that a transaction keeps of prefetched objects and pages together: the size of the
// largest document, which a store reads one at a time without a prefetch.
var prefetchBudget int64 = 64 << 20

type readKind uint8

const (
	readGet readKind = iota
	readScan
	readObject
)

// windowRead is a read of a window: a Get of key in table, the first page of a Scan of the prefix key in table, or an
// Object of hash; and what came back for it.
type windowRead struct {
	kind  readKind
	table uint32
	key   []byte
	hash  [32]byte

	found bool
	value []byte // of a Get; the data of an Object
	rows  []row
	more  bool
	err   *dbwire.RemoteError // the refusal of the daemon for this request
}

// size returns the bytes of the request, as it is sent.
func (r *windowRead) size() int {
	switch r.kind {
	case readGet:
		return dbwire.HeaderSize + getFixed + len(r.key)
	case readScan:
		return dbwire.HeaderSize + scanFixed + len(r.key)
	case readObject:
		return dbwire.HeaderSize + len(r.hash)
	}
	return 0
}

// encode begins the message of the request in w and writes its body; it does not end it.
func (r *windowRead) encode(w *dbwire.Writer) *dbwire.Enc {
	e := &dbwire.Enc{W: w}
	switch r.kind {
	case readGet:
		w.Begin(dbwire.KindGet)
		e.GetRequest(r.table, r.key)
	case readScan:
		w.Begin(dbwire.KindScan)
		e.ScanRequest(dbwire.ScanRequest{Table: r.table, Prefix: r.key})
	case readObject:
		w.Begin(dbwire.KindObject)
		e.ObjectRequest(r.hash)
	}
	return e
}

func (r *windowRead) read(d *dbwire.Dec, maxKey int) (err error) {
	switch r.kind {
	case readGet:
		r.found, r.value, err = dbwire.ReadGetReply(d, maxValue)
	case readScan:
		r.rows, r.more, err = readPage(d, maxKey)
	case readObject:
		r.found, r.value, err = dbwire.ReadObjectReply(d, maxObject)
	}
	return err
}

// readPage reads the reply to a Scan.
func readPage(d *dbwire.Dec, maxKey int) (rows []row, more bool, err error) {
	for {
		k, v, ok := dbwire.ReadScanRow(d, int64(maxKey), maxValue)
		if !ok {
			break
		}
		rows = append(rows, row{k, v})
	}
	more, err = dbwire.ReadScanEnd(d)
	return rows, more, err
}

// window sends reads in windows, and gives each reply to take as soon as it is read. A refusal of the daemon is in the err
// of its read, and the others go on; any other failure ends the connection, as for call. When take returns false, no
// window follows the one that it is in.
func (cn *conn) window(ctx context.Context, reads []windowRead, take func(*windowRead) bool) error {
	if len(reads) == 0 {
		return nil
	}
	if cn.dead.Load() {
		return cn.failure(ctx, errors.New("the connection has ended"))
	}
	stop := context.AfterFunc(ctx, func() { _ = cn.nc.SetDeadline(time.Unix(1, 0)) })
	err := cn.exchangeWindows(reads, take)
	if !stop() {
		cn.close() // the deadline may be set, or about to be
	}
	if err == nil {
		return nil
	}
	cn.close()
	return cn.failure(ctx, err)
}

func (cn *conn) exchangeWindows(reads []windowRead, take func(*windowRead) bool) error {
	for len(reads) > 0 {
		n, err := cn.writeWindow(reads)
		if err != nil {
			return err
		}
		more := true
		for i := range reads[:n] {
			r := &reads[i]
			err := cn.rd.Reply()
			if err == nil {
				err = r.read(&dbwire.Dec{R: cn.rd}, cn.hello.MaxKey)
			}
			if re, ok := errors.AsType[*dbwire.RemoteError](err); ok {
				r.err = re
			} else if err != nil {
				return err
			}
			more = take(r) && more
			r.value, r.rows = nil, nil
		}
		if !more {
			return nil
		}
		reads = reads[n:]
	}
	return nil
}

// writeWindow writes the first requests of reads that make a window, with one Write call, and returns how many. A request
// that does not fit one frame would be written in more than one Write call, so it goes in a window of its own, which is
// what a single call does.
func (cn *conn) writeWindow(reads []windowRead) (n int, err error) {
	cn.wr.SetHold(windowHold)
	for n < len(reads) && n < dbwire.WindowRequests {
		r := &reads[n]
		size := r.size()
		whole := size-dbwire.HeaderSize < cn.hello.Frame
		if n > 0 && (!whole || cn.wr.Held()+size > dbwire.WindowBytes) {
			break
		}
		if e := r.encode(cn.wr); e.Err != nil {
			return 0, e.Err // the message is left open, and the connection ends
		}
		if err := cn.wr.End(); err != nil {
			return 0, err
		}
		n++
		if !whole {
			break
		}
	}
	cn.wr.SetHold(0)
	return n, cn.wr.Flush()
}

// scanKey names the first page of a Scan that is kept.
type scanKey struct {
	table  uint32
	prefix string
}

type firstPage struct {
	rows []row
	more bool
}

// GetMany asks the daemon, in windows, for the keys of table that t has no answer for, and keeps the answers as Get does,
// so that Get of each of them asks nothing. A key that t has written, or has an answer for, or that is longer than the
// daemon's limit, is not asked, and a key that is in keys twice is asked once. A refusal of the daemon is not kept: Get
// of that key asks again and returns what the daemon says then.
func (t *Tx) GetMany(table uint32, keys [][]byte) error {
	if err := t.begin(); err != nil {
		return err
	}
	var reads []windowRead
	asked := map[string]struct{}{}
	for _, key := range keys {
		if len(key) > t.maxKey || t.pending[table].lookup(key) != nil {
			continue
		}
		if _, ok := t.answers[table][string(key)]; ok {
			continue
		}
		if _, ok := asked[string(key)]; ok {
			continue
		}
		asked[string(key)] = struct{}{}
		reads = append(reads, windowRead{kind: readGet, table: table, key: key})
	}
	return t.cn.window(t.ctx, reads, func(r *windowRead) bool {
		if r.err == nil {
			t.keep(table, r.key, r.value, r.found)
		}
		return true
	})
}

// keep remembers the answer of the daemon for a key.
func (t *Tx) keep(table uint32, key, value []byte, found bool) {
	if t.answers == nil {
		t.answers = map[uint32]map[string]answer{}
	}
	if t.answers[table] == nil {
		t.answers[table] = map[string]answer{}
	}
	t.answers[table][string(key)] = answer{value, found}
}

// PrefetchObjects asks the daemon, in windows, for the objects with the hashes, and keeps the data of those that it has,
// until Object returns them, once each, or DropPrefetched. A missing object and a refusal are not kept: Object meets them.
// A transaction keeps at most 64 MiB of prefetched objects and pages together: an answer that would pass that is read and
// dropped, Object asks the daemon for it, and the call sends no window after the one that it was in.
func (t *Tx) PrefetchObjects(hashes [][32]byte) error {
	if err := t.begin(); err != nil {
		return err
	}
	if t.kept >= prefetchBudget {
		return nil
	}
	var reads []windowRead
	asked := map[[32]byte]struct{}{}
	for _, h := range hashes {
		if _, ok := t.objects[h]; ok {
			continue
		}
		if _, ok := asked[h]; ok {
			continue
		}
		asked[h] = struct{}{}
		reads = append(reads, windowRead{kind: readObject, hash: h})
	}
	return t.cn.window(t.ctx, reads, func(r *windowRead) bool {
		if r.err != nil || !r.found {
			return true
		}
		n := int64(len(r.value))
		if t.kept+n > prefetchBudget {
			return false
		}
		if t.objects == nil {
			t.objects = map[[32]byte][]byte{}
		}
		t.objects[r.hash] = r.value
		t.kept += n
		return true
	})
}

// PrefetchScans asks the daemon, in windows, for the first page of the rows of table with each of the prefixes, and keeps
// the pages until DropPrefetched: a Scan of table with one of the prefixes starts with its page, asks the daemon only for
// the pages after it, and merges the writes of t as it does without a prefetch. A refusal is not kept: Scan meets it. A
// transaction keeps at most 64 MiB of prefetched objects and pages together: a page that would pass that is read and
// dropped, Scan asks the daemon for it, and the call sends no window after the one that it was in.
func (t *Tx) PrefetchScans(table uint32, prefixes [][]byte) error {
	if err := t.begin(); err != nil {
		return err
	}
	if t.kept >= prefetchBudget {
		return nil
	}
	var reads []windowRead
	asked := map[string]struct{}{}
	for _, p := range prefixes {
		if len(p) > t.maxKey {
			continue
		}
		if _, ok := t.pages[scanKey{table, string(p)}]; ok {
			continue
		}
		if _, ok := asked[string(p)]; ok {
			continue
		}
		asked[string(p)] = struct{}{}
		reads = append(reads, windowRead{kind: readScan, table: table, key: p})
	}
	return t.cn.window(t.ctx, reads, func(r *windowRead) bool {
		if r.err != nil {
			return true
		}
		n := int64(0)
		for _, x := range r.rows {
			n += int64(len(x.key) + len(x.value))
		}
		if t.kept+n > prefetchBudget {
			return false
		}
		if t.pages == nil {
			t.pages = map[scanKey]firstPage{}
		}
		t.pages[scanKey{table, string(r.key)}] = firstPage{r.rows, r.more}
		t.kept += n
		return true
	})
}

// DropPrefetched drops the objects and the pages that PrefetchObjects and PrefetchScans keep.
func (t *Tx) DropPrefetched() {
	t.objects, t.pages, t.kept = nil, nil, 0
}
