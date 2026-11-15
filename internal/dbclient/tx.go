package dbclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// maxOps is the most ops that the daemon takes in a Commit.
const maxOps = 1 << 22

// Tx is a transaction of View or Update. It is valid only inside the function that they call with it, and not for
// concurrent use: after that function returns, its methods fail. A View reads the snapshot that its first read takes; an
// Update has the snapshot of its start, reads its own writes, and sends them as one Commit when the function returns nil.
type Tx struct {
	c     *Client
	ctx   context.Context
	write bool
	ended bool

	cn      *conn
	taken   bool
	takeErr error
	seq     uint64

	maxKey                          int
	commitMax, objectMax, stagedMax int64

	ops        []dbwire.Op // the puts, deletes and events, in the order of the calls
	size       int64       // of ops as they are sent
	pending    map[uint32]*pendingTable
	answers    map[uint32]map[string]answer
	sent       map[[32]byte]struct{} // the objects put
	stagedSize int64
}

// pendingWrite is the last write of a key.
type pendingWrite struct {
	key, value []byte
	deleted    bool
}

// pendingTable has the last write of each key that the transaction has written to a table.
type pendingTable struct {
	byKey  map[string]*pendingWrite
	keys   []*pendingWrite // sorted by key if sorted
	sorted bool
}

func (p *pendingTable) lookup(key []byte) *pendingWrite {
	if p == nil {
		return nil
	}
	return p.byKey[string(key)]
}

func (p *pendingTable) set(key, value []byte, deleted bool) {
	if w := p.byKey[string(key)]; w != nil {
		w.value, w.deleted = value, deleted
		return
	}
	w := &pendingWrite{key: key, value: value, deleted: deleted}
	p.byKey[string(key)] = w
	if n := len(p.keys); n > 0 && bytes.Compare(p.keys[n-1].key, key) >= 0 {
		p.sorted = false
	}
	p.keys = append(p.keys, w)
}

// rangeCopy returns a copy of the writes of the keys with the prefix, in the order of the keys.
func (p *pendingTable) rangeCopy(prefix []byte) []pendingWrite {
	if p == nil || len(p.keys) == 0 {
		return nil
	}
	if !p.sorted {
		slices.SortFunc(p.keys, func(a, b *pendingWrite) int { return bytes.Compare(a.key, b.key) })
		p.sorted = true
	}
	i, _ := slices.BinarySearchFunc(p.keys, prefix, func(w *pendingWrite, prefix []byte) int { return bytes.Compare(w.key, prefix) })
	var out []pendingWrite
	for ; i < len(p.keys) && bytes.HasPrefix(p.keys[i].key, prefix); i++ {
		out = append(out, *p.keys[i])
	}
	return out
}

// answer is what the daemon has said for a key: its value, or that it is missing.
type answer struct {
	value []byte
	found bool
}

type row struct{ key, value []byte }

// attach makes cn, which holds the snapshot at seq, the connection of the transaction.
func (t *Tx) attach(cn *conn, seq uint64) {
	h := cn.hello
	t.cn, t.seq, t.taken = cn, seq, true
	t.maxKey, t.commitMax, t.objectMax, t.stagedMax = h.MaxKey, h.Commit, h.Object, h.Staged
}

// take takes the snapshot of a View, at its first read.
func (t *Tx) take() error {
	if t.taken {
		return nil
	}
	if t.takeErr != nil {
		return t.takeErr
	}
	cn, seq, err := t.c.snapshotReader(t.ctx)
	if err != nil {
		t.takeErr = err
		return err
	}
	t.attach(cn, seq)
	return nil
}

// begin is the start of a read: it fails after the end of the transaction, and takes the snapshot if it has none.
func (t *Tx) begin() error {
	if t.ended {
		return errEnded
	}
	return t.take()
}

// Seq returns the sequence number of the last commit in the snapshot of t, which a View takes if it has none, or 0 after t
// has ended or if the snapshot cannot be taken.
func (t *Tx) Seq() uint64 {
	if t.begin() != nil {
		return 0
	}
	return t.seq
}

// Get returns the value of key in table, which must not be changed. It reads the writes of t first, then what the daemon
// has answered for the key before, and then asks the daemon: a key is asked for at most once.
func (t *Tx) Get(table uint32, key []byte) ([]byte, bool, error) {
	if err := t.begin(); err != nil {
		return nil, false, err
	}
	v, ok, err := t.get(table, key)
	if err != nil {
		return nil, false, err
	}
	return v[:len(v):len(v)], ok, nil
}

func (t *Tx) get(table uint32, key []byte) (value []byte, found bool, err error) {
	if w := t.pending[table].lookup(key); w != nil {
		return w.value, !w.deleted, nil
	}
	if a, ok := t.answers[table][string(key)]; ok {
		return a.value, a.found, nil
	}
	if len(key) <= t.maxKey {
		err = t.cn.call(t.ctx, dbwire.KindGet, func(e *dbwire.Enc) { e.GetRequest(table, key) }, func(d *dbwire.Dec) (err error) {
			found, value, err = dbwire.ReadGetReply(d, maxValue)
			return err
		})
		if err != nil {
			return nil, false, err
		}
	}
	if t.answers == nil {
		t.answers = map[uint32]map[string]answer{}
	}
	if t.answers[table] == nil {
		t.answers[table] = map[string]answer{}
	}
	t.answers[table][string(key)] = answer{value, found}
	return value, found, nil
}

// Scan calls fn with each key of table that starts with prefix, and its value, which must not be changed, in ascending
// order of the keys, until fn returns false. It scans the table as it is when Scan is called, also if fn writes: the rows
// of the snapshot, with the writes of t at that time, are given, and the daemon is asked for a page of rows only when the
// next one is due.
func (t *Tx) Scan(table uint32, prefix []byte, fn func(key, value []byte) bool) error {
	if err := t.begin(); err != nil {
		return err
	}
	if len(prefix) > t.maxKey {
		return nil
	}
	prefix = bytes.Clone(prefix)
	pend := t.pending[table].rangeCopy(prefix)
	var (
		page    []row
		after   []byte
		started bool
		more    bool
		i, j    int // the next row of page, and the next write of pend
	)
	for {
		if err := t.ctx.Err(); err != nil {
			return err
		}
		if i >= len(page) && (!started || more) {
			var err error
			if page, more, err = t.page(table, prefix, after, started); err != nil {
				return err
			}
			i, started = 0, true
			if len(page) > 0 {
				after = bytes.Clone(page[len(page)-1].key)
			} else if more {
				return fmt.Errorf("%s: the daemon sent an empty page that is not the last", t.c.name)
			}
		}
		var k, v []byte
		switch haveRow, haveWrite := i < len(page), j < len(pend); {
		case !haveRow && !haveWrite:
			return nil
		case haveRow && (!haveWrite || bytes.Compare(page[i].key, pend[j].key) < 0):
			k, v = page[i].key, page[i].value
			i++
		default:
			w := pend[j]
			if haveRow && bytes.Equal(page[i].key, w.key) {
				i++
			}
			j++
			if w.deleted {
				continue
			}
			k, v = bytes.Clone(w.key), w.value
		}
		if !fn(k, v[:len(v):len(v)]) {
			return nil
		}
	}
}

// page asks the daemon for the rows of the table after the key after, if hasAfter.
func (t *Tx) page(table uint32, prefix, after []byte, hasAfter bool) (rows []row, more bool, err error) {
	req := dbwire.ScanRequest{Table: table, Prefix: prefix, HasAfter: hasAfter, After: after}
	err = t.cn.call(t.ctx, dbwire.KindScan, func(e *dbwire.Enc) { e.ScanRequest(req) }, func(d *dbwire.Dec) (err error) {
		for {
			k, v, ok := dbwire.ReadScanRow(d, int64(t.maxKey), maxValue)
			if !ok {
				break
			}
			rows = append(rows, row{k, v})
		}
		more, err = dbwire.ReadScanEnd(d)
		return err
	})
	return rows, more, err
}

// checkWrite returns why t cannot write key to table now, or nil.
func (t *Tx) checkWrite(table uint32, key []byte) error {
	switch {
	case t.ended:
		return errEnded
	case !t.write:
		return ErrReadOnly
	case table == 0:
		return fmt.Errorf("invalid table %d", table)
	case len(key) > t.maxKey:
		return fmt.Errorf("key too long: %d bytes (max %d)", len(key), t.maxKey)
	}
	return nil
}

func (t *Tx) addOp(o dbwire.Op) error {
	size := t.size + o.Size()
	switch {
	case size > t.commitMax:
		return fmt.Errorf("the transaction is too large: %d bytes (max %d)", size, t.commitMax)
	case len(t.ops) >= maxOps:
		return fmt.Errorf("the transaction has too many ops: more than %d", maxOps)
	}
	t.ops = append(t.ops, o)
	t.size = size
	return nil
}

func (t *Tx) setPending(table uint32, key, value []byte, deleted bool) {
	if t.pending == nil {
		t.pending = map[uint32]*pendingTable{}
	}
	p := t.pending[table]
	if p == nil {
		p = &pendingTable{byKey: map[string]*pendingWrite{}, sorted: true}
		t.pending[table] = p
	}
	p.set(key, value, deleted)
}

// Put sets key in table to value. It copies both.
func (t *Tx) Put(table uint32, key, value []byte) error {
	if err := t.checkWrite(table, key); err != nil {
		return err
	}
	k, v := bytes.Clone(key), bytes.Clone(value)
	if err := t.addOp(dbwire.Op{Kind: dbwire.OpPut, Table: table, Key: k, Value: v}); err != nil {
		return err
	}
	t.setPending(table, k, v, false)
	return nil
}

// Delete deletes key from table, if t sees it there: for a key that it does not see, it does nothing.
func (t *Tx) Delete(table uint32, key []byte) error {
	if err := t.checkWrite(table, key); err != nil {
		return err
	}
	if err := t.take(); err != nil {
		return err
	}
	_, found, err := t.get(table, key)
	if err != nil || !found {
		return err
	}
	k := bytes.Clone(key)
	if err := t.addOp(dbwire.Op{Kind: dbwire.OpDelete, Table: table, Key: k}); err != nil {
		return err
	}
	t.setPending(table, k, nil, true)
	return nil
}

// AppendEvent appends an event with data to the log, in the commit of the Update. It copies data.
func (t *Tx) AppendEvent(data []byte) error {
	switch {
	case t.ended:
		return errEnded
	case !t.write:
		return ErrReadOnly
	}
	return t.addOp(dbwire.Op{Kind: dbwire.OpEvent, Value: bytes.Clone(data)})
}

// PutObject sends the object with the SHA-256 hash to the daemon, which checks the hash and keeps the object for the
// Commit of the Update. An object that the store has is not added again, but the Update commits.
func (t *Tx) PutObject(hash [32]byte, data []byte) error {
	switch {
	case t.ended:
		return errEnded
	case !t.write:
		return ErrReadOnly
	case int64(len(data)) > t.objectMax:
		return fmt.Errorf("the object has %d bytes (max %d)", len(data), t.objectMax)
	}
	if _, ok := t.sent[hash]; ok {
		return nil
	}
	if t.stagedSize+int64(len(data)) > t.stagedMax {
		return fmt.Errorf("the staged objects would be over %d bytes", t.stagedMax)
	}
	err := t.cn.call(t.ctx, dbwire.KindPutObject, func(e *dbwire.Enc) { e.PutObjectRequest(hash, data) }, dbwire.ReadEmpty)
	if err != nil {
		return err
	}
	if t.sent == nil {
		t.sent = map[[32]byte]struct{}{}
	}
	t.sent[hash] = struct{}{}
	t.stagedSize += int64(len(data))
	return nil
}

// Object returns the data of the object hash, which an Update has put or the store has, and whether there is one.
func (t *Tx) Object(hash [32]byte) (data []byte, found bool, err error) {
	if err := t.begin(); err != nil {
		return nil, false, err
	}
	err = t.cn.call(t.ctx, dbwire.KindObject, func(e *dbwire.Enc) { e.ObjectRequest(hash) }, func(d *dbwire.Dec) (err error) {
		found, data, err = dbwire.ReadObjectReply(d, maxObject)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return data, found, nil
}

// Events calls fn with the number and the data of each event of the commits up to the snapshot of t, the marks of the
// daemon included, until fn returns false. It reads them on a connection of its own, so fn may use t.
func (t *Tx) Events(ctx context.Context, fn func(number uint64, data []byte) bool) error {
	if err := t.begin(); err != nil {
		return err
	}
	return t.c.events(ctx, t.seq, fn)
}

// events reads the events up to seq on a reader connection that holds a snapshot of its own: the events up to seq do not
// change. What follows seq is read and dropped, so that the connection can be used again.
func (c *Client) events(ctx context.Context, seq uint64, fn func(number uint64, data []byte) bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cn, _, err := c.snapshotReader(ctx)
	if err != nil {
		return err
	}
	finished := false
	defer func() {
		if !finished {
			cn.close()
		}
	}()
	skip := false
	err = cn.call(ctx, dbwire.KindEvents, nil, func(d *dbwire.Dec) error {
		for {
			ev, ok := dbwire.ReadEvent(d, maxValue)
			switch {
			case !ok:
				return d.Err
			case skip:
			case ev.Seq > seq:
				skip = true
			case !fn(ev.Number, ev.Data):
				return errStop
			}
		}
	})
	finished = true
	if err != nil && !errors.Is(err, errStop) {
		cn.close()
		return err
	}
	c.endReader(ctx, cn)
	return nil
}

// endReader releases the snapshot of cn, which was read to the end, and gives it back; or closes it.
func (c *Client) endReader(ctx context.Context, cn *conn) {
	if cn.dead.Load() {
		cn.close()
		return
	}
	ctx, cancel := cleanupContext(ctx)
	defer cancel()
	if cn.release(ctx) != nil {
		cn.close()
		return
	}
	c.putReader(cn)
}
