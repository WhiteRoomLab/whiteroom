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

func (c *session) space(need int64) (dbwire.Code, string) {
	s := c.s
	if s.o.FreeSpace == nil || s.limits.MinFree < 0 {
		return 0, ""
	}
	free, err := s.o.FreeSpace()
	if err != nil {
		s.logf("cannot read the free space (%s)", safeText(err))
		return dbwire.CodeFailed, "cannot read the free space"
	}
	if free < uint64(s.limits.MinFree)+uint64(need) {
		return dbwire.CodeNoSpace, fmt.Sprintf("the free space is %d bytes; a write of %d bytes must leave %d",
			free, need, s.limits.MinFree)
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
	if _, dup := c.staged.data[hash]; !dup && c.staged.total+int64(len(data)) > l.Staged {
		return c.fail(dbwire.CodeLimit, fmt.Sprintf("the staged objects would be over %d bytes", l.Staged))
	}
	if sha256.Sum256(data) != hash {
		return c.fail(dbwire.CodeHash, "the data do not have the hash that was given")
	}
	if code, msg := c.space(int64(len(data))); code != 0 {
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
	if code, msg := c.space(used + c.staged.total); code != 0 {
		return c.fail(code, msg)
	}
	if err := c.s.bucket.take(c.ctx); err != nil {
		return false
	}
	return c.update(expected, ops)
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

func (c *session) update(expected uint64, ops []dbwire.Op) bool {
	s := c.s
	var mark time.Time
	err := func() error {
		s.gate.Lock()
		defer s.gate.Unlock()
		if err := s.hook("commit:gate"); err != nil {
			return err
		}
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
	var r *refusal
	switch {
	case err == nil:
		return c.ok(func(e *dbwire.Enc) { e.CommitReply(expected+1, mark) })
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
	for _, hash := range c.staged.order {
		if _, err := tx.PutObject(c.staged.data[hash]); err != nil {
			return err
		}
	}
	for i, o := range ops {
		var err error
		switch o.Kind {
		case dbwire.OpPut:
			if dbwire.AppendOnly(o.Table) {
				if _, ok := tx.Get(o.Table, o.Key); ok {
					return &refusal{dbwire.CodeRule, fmt.Sprintf("op %d: table %d is append-only: a row is not changed", i, o.Table)}
				}
			}
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
	return tx.AppendEvent(markFn())
}
