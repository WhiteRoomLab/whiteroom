package logdb

import (
	"fmt"
	"maps"
)

// Tx is a transaction of View or Update. It is valid only inside the function that they call with it, and not for
// concurrent use: after that function returns, its writes fail and its reads find nothing.
type Tx struct {
	db     *DB
	st     *state // the snapshot; in a View, nil until its first read
	write  bool
	owner  *owner   // of the nodes that the writes make
	tables tableMap // with the writes, or nil before the first one
	ops    []op
	objs   map[[32]byte][]byte // the objects that the tx puts, with their data
	done   bool
}

// run calls f with tx, which ends when f returns or panics.
func (tx *Tx) run(f func(*Tx) error) error {
	defer func() { tx.done = true }()
	return f(tx)
}

func (tx *Tx) snapshot() *state {
	if tx.st == nil {
		tx.st = tx.db.cur.Load()
	}
	return tx.st
}

// view returns the tables that the reads of tx see, or nil after tx has ended.
func (tx *Tx) view() tableMap {
	switch {
	case tx.done:
		return nil
	case tx.tables != nil:
		return tx.tables
	}
	return tx.snapshot().tables
}

// Seq returns the sequence number of the last commit in the snapshot of tx, or 0 after tx has ended.
func (tx *Tx) Seq() uint64 {
	if tx.done {
		return 0
	}
	return tx.snapshot().seq
}

// Get returns the value of key in table, which must not be changed.
func (tx *Tx) Get(table uint32, key []byte) ([]byte, bool) {
	v, ok := tx.view()[table].Get(string(key))
	return v[:len(v):len(v)], ok
}

// Scan calls fn with each key of table that starts with prefix, and its value, which must not be changed, in
// ascending order of the keys, until fn returns false. It scans the table as it is when Scan is called, also if fn
// writes.
func (tx *Tx) Scan(table uint32, prefix []byte, fn func(key, value []byte) bool) {
	m := tx.view()[table]
	if tx.tables != nil {
		tx.owner = new(owner) // writes in fn must not change the nodes that m has
	}
	m.Ascend(string(prefix), func(k string, v []byte) bool {
		return fn([]byte(k), v[:len(v):len(v)])
	})
}

// Put sets key in table to value. It copies both.
func (tx *Tx) Put(table uint32, key, value []byte) error {
	if err := tx.checkWrite(table, key); err != nil {
		return err
	}
	k, v := string(key), clone(value)
	tx.writable().put(tx.owner, table, k, v)
	tx.ops = append(tx.ops, op{code: opPut, table: table, key: k, value: v})
	return nil
}

// Delete deletes key from table, if it is there.
func (tx *Tx) Delete(table uint32, key []byte) error {
	if err := tx.checkWrite(table, key); err != nil {
		return err
	}
	k := string(key)
	if _, ok := tx.view()[table].Get(k); !ok {
		return nil
	}
	tx.writable().del(tx.owner, table, k)
	tx.ops = append(tx.ops, op{code: opDelete, table: table, key: k})
	return nil
}

// checkTx returns why tx cannot write now, or nil.
func (tx *Tx) checkTx() error {
	switch {
	case tx.done:
		return errEnded
	case !tx.write:
		return ErrReadOnly
	}
	return nil
}

func (tx *Tx) checkWrite(table uint32, key []byte) error {
	if err := tx.checkTx(); err != nil {
		return err
	}
	switch {
	case table == 0:
		return fmt.Errorf("invalid table %d", table)
	case len(key) > MaxKey:
		return fmt.Errorf("key too long: %d bytes (max %d)", len(key), MaxKey)
	}
	return nil
}

// writable returns the tables of tx to write to: on the first write, a copy of the map of the snapshot.
func (tx *Tx) writable() tableMap {
	if tx.tables == nil {
		tx.tables = make(tableMap, len(tx.st.tables)+1)
		maps.Copy(tx.tables, tx.st.tables)
	}
	return tx.tables
}
