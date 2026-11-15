package dbclient

import (
	"context"
	"errors"
	"fmt"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// cleanupContext returns ctx, or if it has ended a context that lasts a while longer, for the request that ends a
// transaction.
func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Err() == nil {
		return ctx, func() {}
	}
	return context.WithTimeout(context.WithoutCancel(ctx), cleanupWait)
}

// View runs f in a transaction that only reads, and that sees the store as it was when f first read it. Views run at the
// same time, each on a connection of the reader socket, at most Options.Readers of them; the others wait until ctx ends.
func (c *Client) View(ctx context.Context, f func(*Tx) error) error {
	if c.isClosed() {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.acquireSlot(ctx); err != nil {
		return err
	}
	defer c.releaseSlot()
	t := &Tx{c: c, ctx: ctx}
	finished := false
	defer func() {
		if !finished { // f has panicked
			t.ended = true
			if t.cn != nil {
				t.cn.close()
			}
		}
	}()
	err := f(t)
	finished = true
	t.ended = true
	if t.cn != nil {
		c.endReader(ctx, t.cn)
	}
	if err != nil {
		return err
	}
	return ctx.Err()
}

// Update runs f in a transaction that writes, and commits it if f returns nil and ctx has not ended; otherwise nothing
// that f has written stays. When Update returns nil, the commit is durable. Updates run one at a time: Update waits for
// the others until ctx ends. f must not call Update of c, which would wait for itself. A client of a reader fails with
// ErrReadOnly.
func (c *Client) Update(ctx context.Context, f func(*Tx) error) error {
	switch {
	case c.isClosed():
		return ErrClosed
	case c.role != dbwire.RoleWriter:
		return ErrReadOnly
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.acquireTurn(ctx); err != nil {
		return err
	}
	defer c.releaseTurn()
	cn, seq, err := c.snapshotWriter(ctx)
	if err != nil {
		return err
	}
	t := &Tx{c: c, ctx: ctx, write: true}
	t.attach(cn, seq)
	finished := false
	defer func() {
		if !finished { // f has panicked
			t.ended = true
			cn.close()
			c.wconn = nil
		}
	}()
	ferr := f(t)
	finished = true
	t.ended = true
	switch {
	case ferr != nil:
		c.endWriter(ctx, cn)
		return ferr
	case ctx.Err() != nil:
		c.endWriter(ctx, cn)
		return ctx.Err()
	case len(t.ops) == 0 && len(t.sent) == 0:
		c.endWriter(ctx, cn)
		return nil
	}
	return c.commit(ctx, cn, t)
}

// endWriter releases the snapshot of the writer connection.
func (c *Client) endWriter(ctx context.Context, cn *conn) {
	ctx, cancel := cleanupContext(ctx)
	defer cancel()
	if cn.release(ctx) != nil {
		cn.close()
		c.wconn = nil
	}
}

// commit sends the ops of t, and returns when the daemon has answered. It leaves the writer connection without a
// snapshot: the daemon drops it with any answer to a Commit.
func (c *Client) commit(ctx context.Context, cn *conn, t *Tx) error {
	err := cn.call(ctx, dbwire.KindCommit, func(e *dbwire.Enc) {
		e.CommitRequest(t.seq)
		for _, o := range t.ops {
			e.CommitOp(o)
		}
		e.CommitEnd()
	}, func(d *dbwire.Dec) error {
		_, _, err := dbwire.ReadCommitReply(d)
		return err
	})
	if err == nil {
		return nil
	}
	if re, ok := errors.AsType[*dbwire.RemoteError](err); ok {
		if re.Code == dbwire.CodeSeq {
			return fmt.Errorf("the store has changed since the transaction began: %w", err)
		}
		return err
	}
	return c.resolve(ctx, t.seq, err)
}

// resolve finds out whether a Commit was made, after the connection failed while it was being sent or answered: it
// connects again as the writer, which the daemon lets in only after the session of the old connection has ended, and
// compares the last sequence number of the new Hello with seq, that of the snapshot. cause is the failure.
func (c *Client) resolve(ctx context.Context, seq uint64, cause error) error {
	c.wconn = nil
	unknown := func(why error) error {
		return fmt.Errorf("%s: it is not known whether the commit was made (%w): %w", c.name, cause, why)
	}
	// The question is answered also if ctx has ended: whether the commit was made does not depend on it.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupWait+max(c.o.DialWait, 0))
	defer cancel()
	cn, err := c.connectWait(rctx, dbwire.RoleWriter, c.o.DialWait)
	if err != nil {
		return unknown(err)
	}
	c.wconn = cn
	switch last := cn.hello.Seq; last {
	case seq + 1:
		return nil
	case seq:
		if _, ok := errors.AsType[*ioError](cause); ok {
			return fmt.Errorf("the transaction was not committed: %w", cause)
		}
		return cause
	default:
		return unknown(fmt.Errorf("the last commit is %d after the snapshot at %d", last, seq))
	}
}
