package dbserver

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

type staged struct {
	data  map[[32]byte][]byte
	order [][32]byte
	total int64
}

func (st *staged) add(hash [32]byte, data []byte) {
	if _, ok := st.data[hash]; ok {
		return
	}
	if st.data == nil {
		st.data = map[[32]byte][]byte{}
	}
	st.data[hash] = data
	st.order = append(st.order, hash)
	st.total += int64(len(data))
}

// refusal is a refusal of the daemon's own, as the error of an Update, which then commits nothing.
type refusal struct {
	code dbwire.Code
	msg  string
}

func (r *refusal) Error() string { return r.msg }

// maxCommitOps is the most ops of a Commit: the limit of bytes counts them as they are sent, and a Commit of very many
// tiny ops would cost the daemon many times its bytes in memory.
const maxCommitOps = 1 << 22

// maxStagedObjects is the most distinct objects that a writer stages before its Commit: the limit of bytes does not bound
// the memory of objects of a few bytes, which cost the daemon more than their bytes.
const maxStagedObjects = 1 << 22

// requestSlack is what a request has besides the bytes that its limit counts: the hashes, the sizes, the end mark.
const requestSlack = 64

// writeGate returns why the session may not stage or commit now, or 0. The reasons come in this order.
func (c *session) writeGate(what string) (dbwire.Code, string) {
	switch {
	case c.role != dbwire.RoleWriter:
		return dbwire.CodeDenied, "only the writer may " + what
	case c.snap == nil:
		return dbwire.CodeNoSnapshot, noSnapshotText
	case c.s.stopped.Load():
		return dbwire.CodeStopped, stoppedText
	}
	return 0, ""
}

// space returns why a write of need bytes, which may make a file if file, may not be done for the free space, or 0.
func (c *session) space(need int64, file bool) (dbwire.Code, string) {
	s := c.s
	checkBytes, checkInodes := s.limits.MinFree >= 0, file && s.limits.MinFreeInodes >= 0
	if s.o.FreeSpace == nil || !checkBytes && !checkInodes {
		return 0, ""
	}
	free, err := s.o.FreeSpace()
	if err != nil {
		s.logf("cannot read the free space (%s)", safeText(err))
		return dbwire.CodeFailed, "cannot read the free space"
	}
	switch {
	case checkBytes && free.Bytes < uint64(s.limits.MinFree)+uint64(need):
		return dbwire.CodeNoSpace, fmt.Sprintf("the free space is %d bytes; a write of %d bytes must leave %d",
			free.Bytes, need, s.limits.MinFree)
	case checkInodes && free.HasInodes && free.Inodes <= uint64(s.limits.MinFreeInodes):
		return dbwire.CodeNoSpace, fmt.Sprintf("the free inodes are %d; a write that may make a file must leave %d",
			free.Inodes, s.limits.MinFreeInodes)
	}
	return 0, ""
}

func (c *session) putObjectRequest() bool {
	l := c.s.limits
	if code, msg := c.writeGate("put an object"); code != 0 {
		return c.refuseBody(l.Object+requestSlack, code, msg)
	}
	hash, data, err := dbwire.ReadPutObjectRequest(&dbwire.Dec{R: c.rd}, l.Object)
	if err != nil {
		c.bad(err)
		return false
	}
	if _, dup := c.staged.data[hash]; !dup {
		switch {
		case c.staged.total+int64(len(data)) > l.Staged:
			return c.fail(dbwire.CodeLimit, fmt.Sprintf("the staged objects would be over %d bytes", l.Staged))
		case len(c.staged.order) >= c.s.maxStagedCount:
			return c.fail(dbwire.CodeLimit, fmt.Sprintf("more than %d objects would be staged", c.s.maxStagedCount))
		}
	}
	if sha256.Sum256(data) != hash {
		return c.fail(dbwire.CodeHash, "the data do not have the hash that was given")
	}
	if code, msg := c.space(int64(len(data)), true); code != 0 {
		return c.fail(code, msg)
	}
	c.staged.add(hash, data)
	return c.ok(nil)
}

func (c *session) commitRequest() bool {
	l := c.s.limits
	if code, msg := c.writeGate("commit"); code != 0 {
		return c.refuseBody(l.Commit+requestSlack, code, msg)
	}
	d := &dbwire.Dec{R: c.rd}
	expected, err := dbwire.ReadCommitRequest(d)
	if err != nil {
		c.bad(err)
		return false
	}
	var ops []dbwire.Op
	var used int64
	for {
		o, ok := dbwire.ReadCommitOp(d, dbwire.MaxKey, max(l.Commit-used, 0))
		if !ok {
			break
		}
		if used += o.Size(); used > l.Commit {
			return c.refuseAndClose(dbwire.CodeLimit, fmt.Sprintf("the ops of a Commit are over %d bytes", l.Commit))
		}
		if len(ops) >= c.s.maxOps {
			return c.refuseAndClose(dbwire.CodeLimit, fmt.Sprintf("a Commit of more than %d ops", c.s.maxOps))
		}
		ops = append(ops, o)
	}
	if d.Err != nil {
		c.bad(d.Err)
		return false
	}
	// The request is read to its end: whatever the outcome, the snapshot and the staged objects go.
	defer c.dropSnapshot()
	if r := checkOps(ops, len(c.staged.order) > 0); r != nil {
		return c.fail(r.code, r.msg)
	}
	if code, msg := c.space(used+c.staged.total, true); code != 0 {
		return c.fail(code, msg)
	}
	c.wr.Begin(dbwire.KindOK)
	p := c.pulse(nil)
	defer p.end()
	if err := c.s.bucket.take(c.ctx); err != nil {
		return false
	}
	return c.update(expected, ops, p)
}

func checkOps(ops []dbwire.Op, hasStaged bool) *refusal {
	rows, events := false, false
	for i, o := range ops {
		switch o.Kind {
		case dbwire.OpEvent:
			events = true
			switch {
			case len(o.Value) == 0:
				return &refusal{dbwire.CodeRule, fmt.Sprintf("op %d: an event is empty", i)}
			case dbwire.IsDaemonEvent(o.Value):
				return &refusal{dbwire.CodeRule, fmt.Sprintf("op %d: an event starts with the byte that is the daemon's", i)}
			}
		case dbwire.OpPut, dbwire.OpDelete:
			rows = true
			switch {
			case o.Table == 0:
				return &refusal{dbwire.CodeLimit, fmt.Sprintf("op %d: invalid table %d", i, o.Table)}
			case o.Kind == dbwire.OpDelete && dbwire.AppendOnly(o.Table):
				return &refusal{dbwire.CodeRule, fmt.Sprintf("op %d: table %d is append-only: a row is not deleted", i, o.Table)}
			case o.Kind == dbwire.OpDelete && o.Table == dbwire.TableHeads:
				return &refusal{dbwire.CodeRule, fmt.Sprintf("op %d: a row of table %d is not deleted", i, o.Table)}
			case o.Kind == dbwire.OpPut && o.Table == dbwire.TableHeads && !dbwire.ValidHeadRow(o.Key, o.Value):
				return &refusal{dbwire.CodeRule, fmt.Sprintf("op %d: not a valid row of table %d", i, o.Table)}
			}
		}
	}
	switch {
	case rows && !events:
		return &refusal{dbwire.CodeRule, "a commit that puts or deletes a row needs an audit event"}
	case len(ops) == 0 && !hasStaged:
		return &refusal{dbwire.CodeRule, "a commit with no op and no staged object"}
	}
	return nil
}

// update runs the Update of a Commit, and answers into the reply that is open; p is the pulser of that reply, which it ends
// before it writes.
func (c *session) update(expected uint64, ops []dbwire.Op, p *pulser) bool {
	s := c.s
	var mark time.Time
	err := func() error {
		s.gate.Lock()
		defer s.gate.Unlock()
		if err := s.hook("commit:gate"); err != nil {
			return err
		}
		select {
		case s.turn <- struct{}{}:
		case <-c.ctx.Done():
			return c.ctx.Err()
		}
		defer func() { <-s.turn }()
		return s.db.Update(c.ctx, func(tx *logdb.Tx) error {
			if have := tx.Seq(); have != expected {
				return &refusal{dbwire.CodeSeq, fmt.Sprintf("the last sequence number is %d, not the expected %d", have, expected)}
			}
			return c.apply(tx, ops, func() []byte {
				mark = s.now().UTC()
				return dbwire.AppendMark(nil, mark)
			})
		})
	}()
	p.end()
	var r *refusal
	switch {
	case err == nil:
		return c.finish(func(e *dbwire.Enc) { e.CommitReply(expected+1, mark) })
	case errors.As(err, &r):
		return c.fail(r.code, r.msg)
	case errors.Is(err, logdb.ErrFailed):
		s.stop(err)
		return c.fail(dbwire.CodeStopped, stoppedText)
	case c.ctx.Err() != nil:
		return false
	case transactionText.MatchString(err.Error()):
		return c.fail(dbwire.CodeLimit, safeText(err))
	}
	return c.fail(dbwire.CodeFailed, safeText(err))
}

func (c *session) apply(tx *logdb.Tx, ops []dbwire.Op, markFn func() []byte) error {
	var before uint64 // the schema version that the store has, 0 if it has none
	if v, ok := tx.Get(dbwire.TableMeta, []byte(dbwire.MetaSchema)); ok {
		before, _ = schemaVersion(v)
	}
	for _, hash := range c.staged.order {
		if _, err := tx.PutObject(c.staged.data[hash]); err != nil {
			return err
		}
	}
	heads := false
	for i, o := range ops {
		var err error
		switch o.Kind {
		case dbwire.OpPut:
			if dbwire.AppendOnly(o.Table) {
				if _, ok := tx.Get(o.Table, o.Key); ok {
					return &refusal{dbwire.CodeRule, fmt.Sprintf("op %d: table %d is append-only: a row is not changed", i, o.Table)}
				}
			}
			heads = heads || o.Table == dbwire.TableHeads
			err = tx.Put(o.Table, o.Key, o.Value)
		case dbwire.OpDelete:
			err = tx.Delete(o.Table, o.Key)
		case dbwire.OpEvent:
			err = tx.AppendEvent(o.Value)
		}
		if err != nil {
			return err
		}
	}
	if r := checkSchema(tx, before); r != nil {
		return r
	}
	if heads && countRows(tx, dbwire.TableHeads, maxHeadRows+1) > maxHeadRows {
		return &refusal{dbwire.CodeRule, fmt.Sprintf("the table of the heads would have more than %d rows", maxHeadRows)}
	}
	return tx.AppendEvent(markFn())
}

// checkSchema returns the refusal of a commit that would leave the row meta/schema as the daemon's own Open would refuse
// it, or as a lower version than before, the version that the store had: the daemon never commits a state that it would
// not open again.
func checkSchema(tx *logdb.Tx, before uint64) *refusal {
	v, ok := tx.Get(dbwire.TableMeta, []byte(dbwire.MetaSchema))
	if !ok {
		return &refusal{dbwire.CodeRule, "the commit would leave the store without its schema version"}
	}
	switch version, valid := schemaVersion(v); {
	case !valid:
		return &refusal{dbwire.CodeRule, "the commit would leave an invalid schema version"}
	case version > dbwire.TableVersion:
		return &refusal{dbwire.CodeRule, fmt.Sprintf("the commit would set the schema version %d, newer than this daemon knows (%d)",
			version, dbwire.TableVersion)}
	case version < before:
		return &refusal{dbwire.CodeRule, fmt.Sprintf("the commit would lower the schema version from %d to %d", before, version)}
	}
	return nil
}

// countRows counts the rows of table, and stops after max of them.
func countRows(tx *logdb.Tx, table uint32, max int) int {
	n := 0
	tx.Scan(table, nil, func(_, _ []byte) bool {
		n++
		return n < max
	})
	return n
}
