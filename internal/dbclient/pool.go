package dbclient

import (
	"context"
	"errors"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// acquireSlot waits for the right to use a reader connection for a View.
func (c *Client) acquireSlot(ctx context.Context) error {
	select {
	case c.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return ErrClosed
	}
}

func (c *Client) releaseSlot() { <-c.slots }

// takeReader returns an idle reader connection, or dials one. dialed tells that it was dialed now. It does not wait for a
// slot: a View has one already.
func (c *Client) takeReader(ctx context.Context) (cn *conn, dialed bool, err error) {
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return nil, false, ErrClosed
		}
		n := len(c.idle)
		if n == 0 {
			c.mu.Unlock()
			break
		}
		cn = c.idle[n-1]
		c.idle = c.idle[:n-1]
		c.mu.Unlock()
		if !cn.dead.Load() {
			return cn, false, nil
		}
	}
	cn, err = c.connect(ctx, dbwire.RoleReader)
	return cn, true, err
}

// putReader gives back a reader connection that has been read to the end and holds no snapshot.
func (c *Client) putReader(cn *conn) {
	c.mu.Lock()
	keep := !c.closed && !cn.dead.Load() && len(c.idle) < c.o.Readers
	if keep {
		c.idle = append(c.idle, cn)
	}
	c.mu.Unlock()
	if !keep {
		cn.close()
	}
}

// withReader runs f with a reader connection that holds no snapshot, outside the count of the Views.
func (c *Client) withReader(ctx context.Context, f func(cn *conn) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cn, dialed, err := c.takeReader(ctx)
	if err != nil {
		return err
	}
	err = f(cn)
	if _, ok := errors.AsType[*ioError](err); ok && !dialed {
		// A connection that was idle may be one that the daemon has ended; the request did not take effect.
		cn, err = c.connect(ctx, dbwire.RoleReader)
		if err != nil {
			return err
		}
		err = f(cn)
	}
	c.putReader(cn)
	return err
}

// snapshotReader returns a reader connection that holds a snapshot, and its sequence number. A connection from the pool
// that fails on that first request is replaced once.
func (c *Client) snapshotReader(ctx context.Context) (*conn, uint64, error) {
	cn, dialed, err := c.takeReader(ctx)
	if err != nil {
		return nil, 0, err
	}
	seq, err := cn.snapshot(ctx)
	if _, ok := errors.AsType[*ioError](err); ok && !dialed {
		if cn, err = c.connect(ctx, dbwire.RoleReader); err != nil {
			return nil, 0, err
		}
		seq, err = cn.snapshot(ctx)
	}
	if err != nil {
		cn.close()
		return nil, 0, err
	}
	return cn, seq, nil
}

// dialWriter connects as the writer and keeps the connection, for the holder of the turn. A connection that fails leaves
// none, which restoreWriter does not try again.
func (c *Client) dialWriter(ctx context.Context) (*conn, error) {
	cn, err := c.connect(ctx, dbwire.RoleWriter)
	if err != nil {
		c.wconn = nil
		return nil, err
	}
	c.wconn = cn
	return cn, nil
}

// snapshotWriter returns the writer connection with a snapshot, for the holder of the turn. As snapshotReader, it
// replaces a connection that fails on that first request.
func (c *Client) snapshotWriter(ctx context.Context) (*conn, uint64, error) {
	cn, dialed := c.wconn, false
	if cn == nil || cn.dead.Load() {
		var err error
		if cn, err = c.dialWriter(ctx); err != nil {
			return nil, 0, err
		}
		dialed = true
	}
	seq, err := cn.snapshot(ctx)
	if _, ok := errors.AsType[*ioError](err); ok && !dialed {
		cn.close()
		if cn, err = c.dialWriter(ctx); err != nil {
			return nil, 0, err
		}
		seq, err = cn.snapshot(ctx)
	}
	if err != nil {
		cn.close()
		return nil, 0, err
	}
	return cn, seq, nil
}

// liveWriter returns the writer connection with no snapshot, for the holder of the turn. A connection that it has kept is
// tried with a Snapshot and a Release, and replaced if that fails: the request that follows carries bytes of a reader,
// which cannot be sent again.
func (c *Client) liveWriter(ctx context.Context) (*conn, error) {
	if cn := c.wconn; cn != nil && !cn.dead.Load() {
		_, err := cn.snapshot(ctx)
		if err == nil {
			err = cn.release(ctx)
		}
		if err == nil {
			return cn, nil
		}
		cn.close()
		if _, ok := errors.AsType[*ioError](err); !ok {
			return nil, err
		}
	}
	return c.dialWriter(ctx)
}

func (c *Client) acquireTurn(ctx context.Context) error {
	select {
	case c.turn <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return ErrClosed
	}
}

func (c *Client) releaseTurn() { <-c.turn }
