package dbserver

import (
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

const noSnapshotText = "no snapshot is held; send a Snapshot first"

func (c *session) getRequest() bool {
	table, key, err := dbwire.ReadGetRequest(&dbwire.Dec{R: c.rd}, dbwire.MaxKey)
	if err != nil {
		c.bad(err)
		return false
	}
	if c.snap == nil {
		return c.fail(dbwire.CodeNoSnapshot, noSnapshotText)
	}
	var value []byte
	var found bool
	c.snap.do(func(tx *logdb.Tx) { value, found = tx.Get(table, key) })
	return c.ok(func(e *dbwire.Enc) { e.GetReply(found, value) })
}

type row struct{ key, value []byte }

func (c *session) scanRequest() bool {
	req, err := dbwire.ReadScanRequest(&dbwire.Dec{R: c.rd}, dbwire.MaxKey)
	if err != nil {
		c.bad(err)
		return false
	}
	if c.snap == nil {
		return c.fail(dbwire.CodeNoSnapshot, noSnapshotText)
	}
	var page []row
	var size int64
	more := false
	collect := func(key, value []byte) bool {
		c.s.scanned.Add(1)
		page = append(page, row{key, value})
		size += int64(9 + len(key) + len(value))
		if size >= int64(c.s.limits.Frame) || req.Limit > 0 && len(page) >= int(req.Limit) {
			more = true
			return false
		}
		return true
	}
	c.snap.do(func(tx *logdb.Tx) {
		if req.HasAfter {
			tx.ScanAfter(req.Table, req.Prefix, req.After, collect)
		} else {
			tx.Scan(req.Table, req.Prefix, collect)
		}
	})
	return c.ok(func(e *dbwire.Enc) {
		for _, r := range page {
			e.ScanRow(r.key, r.value)
		}
		e.ScanEnd(more)
	})
}

func (c *session) objectRequest() bool {
	hash, err := dbwire.ReadObjectRequest(&dbwire.Dec{R: c.rd})
	if err != nil {
		c.bad(err)
		return false
	}
	if c.snap == nil {
		return c.fail(dbwire.CodeNoSnapshot, noSnapshotText)
	}
	data, found := c.staged.data[hash]
	if !found {
		var rerr error
		c.snap.do(func(tx *logdb.Tx) {
			if found = tx.HasObject(hash); found {
				data, rerr = tx.Object(hash)
			}
		})
		if rerr != nil {
			return c.fail(dbwire.CodeFailed, safeText(rerr))
		}
	}
	return c.ok(func(e *dbwire.Enc) { e.ObjectReply(found, data) })
}

func (c *session) eventsRequest() bool {
	if err := c.empty(); err != nil {
		c.bad(err)
		return false
	}
	if c.snap == nil {
		return c.fail(dbwire.CodeNoSnapshot, noSnapshotText)
	}
	seq := c.snap.seq
	c.wr.Begin(dbwire.KindOK)
	e := &dbwire.Enc{W: c.wr}
	p := c.pulse()
	defer p.end()
	err := c.s.db.Events(c.ctx, func(ev logdb.Event) bool {
		if ev.Seq > seq {
			return false
		}
		p.lock()
		e.Event(dbwire.Event{Number: ev.Number, Seq: ev.Seq, Data: ev.Data})
		ok := e.Err == nil
		p.unlock()
		return ok
	})
	p.end()
	if err == nil {
		e.EventsEnd() // may write the frame that fills the limit, and fail
	}
	switch {
	case e.Err != nil:
		_ = c.wr.Fail(dbwire.CodeInternal, "the reply could not be written")
		return false
	case err != nil:
		return c.fail(dbwire.CodeFailed, safeText(err))
	}
	return c.wr.End() == nil
}

func (c *session) verifyRequest() bool {
	if err := c.empty(); err != nil {
		c.bad(err)
		return false
	}
	c.wr.Begin(dbwire.KindOK)
	p := c.pulse()
	defer p.end()
	err := c.s.db.Verify(c.ctx)
	p.end()
	switch {
	case c.ctx.Err() != nil:
		return false
	case err != nil:
		return c.fail(dbwire.CodeFailed, safeText(err))
	}
	return c.wr.End() == nil
}
