package storetest

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// A Watch is the Dialer of the sockets of a Daemon that sees what the connections to the reader socket carry, and can make
// them give no more bytes, as a daemon does whose disk blocks.
type Watch struct {
	inner      dbclient.Dialer
	stallAfter atomic.Int64 // 0: no stall

	mu      sync.Mutex
	readers []*WatchConn
}

// Watch returns a Watch of the sockets of d.
func (d *Daemon) Watch() *Watch { return &Watch{inner: d.Dialer()} }

func (w *Watch) Dial(ctx context.Context, role dbwire.Role) (net.Conn, error) {
	nc, err := w.inner.Dial(ctx, role)
	if err != nil {
		return nil, err
	}
	c := &WatchConn{Conn: nc, w: w, closed: make(chan struct{}), past: make(chan struct{})}
	if role == dbwire.RoleReader {
		w.mu.Lock()
		w.readers = append(w.readers, c)
		w.mu.Unlock()
	}
	return c, nil
}

func (w *Watch) String() string { return fmt.Sprint(w.inner) }

// Arm makes each reader connection give no more bytes once it has read limit bytes from now on, until its deadline is past
// or it is closed.
func (w *Watch) Arm(limit int64) {
	w.mu.Lock()
	for _, c := range w.readers {
		c.base.Store(c.read.Load())
	}
	w.mu.Unlock()
	w.stallAfter.Store(limit)
}

// Stalls returns how many reader connections have stopped giving bytes.
func (w *Watch) Stalls() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, c := range w.readers {
		if c.stalled.Load() {
			n++
		}
	}
	return n
}

// Marks is how many bytes each reader connection had read when Mark was called.
type Marks map[*WatchConn]int64

// Mark returns what the reader connections have read so far.
func (w *Watch) Mark() Marks {
	w.mu.Lock()
	defer w.mu.Unlock()
	m := make(Marks, len(w.readers))
	for _, c := range w.readers {
		m[c] = c.read.Load()
	}
	return m
}

// ReadSince returns how many bytes the reader connections have read since m, and the connection that has read the most.
func (w *Watch) ReadSince(m Marks) (total int64, most *WatchConn) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var best int64
	for _, c := range w.readers {
		n := c.read.Load() - m[c]
		total += n
		if n > best {
			best, most = n, c
		}
	}
	return total, most
}

// A WatchConn is a connection of a Watch. A stalled Read returns when a deadline that is past is set, as the client does when
// the context of a read ends, or when the connection is closed.
type WatchConn struct {
	net.Conn
	w      *Watch
	read   atomic.Int64
	base   atomic.Int64
	writes atomic.Int64

	stalled   atomic.Bool
	closed    chan struct{}
	past      chan struct{}
	closeOnce sync.Once
	pastOnce  sync.Once
}

// Writes returns how many Write calls the connection has had.
func (c *WatchConn) Writes() int64 { return c.writes.Load() }

func (c *WatchConn) Read(p []byte) (int, error) {
	if limit := c.w.stallAfter.Load(); limit > 0 && c.read.Load()-c.base.Load() >= limit {
		c.stalled.Store(true)
		select {
		case <-c.closed:
			return 0, net.ErrClosed
		case <-c.past:
			return 0, os.ErrDeadlineExceeded
		}
	}
	n, err := c.Conn.Read(p)
	c.read.Add(int64(n))
	return n, err
}

func (c *WatchConn) Write(p []byte) (int, error) {
	c.writes.Add(1)
	return c.Conn.Write(p)
}

func (c *WatchConn) deadline(t time.Time) {
	if !t.IsZero() && !t.After(time.Now()) {
		c.pastOnce.Do(func() { close(c.past) })
	}
}

func (c *WatchConn) SetDeadline(t time.Time) error {
	c.deadline(t)
	return c.Conn.SetDeadline(t)
}

func (c *WatchConn) SetReadDeadline(t time.Time) error {
	c.deadline(t)
	return c.Conn.SetReadDeadline(t)
}

func (c *WatchConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}
