// Package storetest opens stores by their data directories, for the tests of other packages: each function starts the
// DB daemon of the directory inside the process (internal/dbserver/dbtest), or joins the one that runs, and connects a
// store to it. The daemon ends when the last store of it is closed. It is for tests only.
package storetest

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver/dbtest"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// Create makes the data directory dir, which must be missing or an empty directory, as whiteroom-db init does, and the
// store in it, as store.Create does.
func Create(dir string) (*store.Store, error) {
	if err := dbtest.Init(dir, dbtest.Options{}); err != nil {
		return nil, err
	}
	d, err := Dialer(dir)
	if err != nil {
		return nil, err
	}
	return store.Create(context.Background(), d)
}

// Open opens the store of the data directory dir as its writer, as store.Open does.
func Open(dir string) (*store.Store, error) {
	d, err := Dialer(dir)
	if err != nil {
		return nil, err
	}
	return store.Open(context.Background(), d)
}

// OpenReader opens the store of the data directory dir to read it, as store.OpenReader does.
func OpenReader(dir string) (*store.Store, error) {
	d, err := Dialer(dir)
	if err != nil {
		return nil, err
	}
	return store.OpenReader(context.Background(), d)
}

// Backup copies the store of the data directory srcDir to dstDir, as store.Backup does.
func Backup(ctx context.Context, srcDir, dstDir string) ([]audit.Head, error) {
	d, err := Dialer(srcDir)
	if err != nil {
		return nil, err
	}
	return store.Backup(ctx, d, dstDir)
}

// Dialer returns the Dialer of the daemon of the data directory dir, which it starts or joins. Closing it, which a
// store or a client that was opened with it does, gives up the place that it has in the daemon.
func Dialer(dir string) (dbclient.Dialer, error) {
	h, err := dbtest.Acquire(dir, dbtest.Options{})
	if err != nil {
		return nil, err
	}
	return h.Dialer(), nil
}

// A Daemon is the daemon of a data directory that a test stops and starts again. It listens on the sockets of a directory of
// its own, which Dialer connects to, so that the stores that were opened with Dialer stay open while it is away, fail with
// errors that are store.ErrUnavailable, and work again once it is started. A store that was opened with Dialer(dir) of this
// package, or a function that opens one by the data directory, would start a daemon of its own while this one is away.
type Daemon struct {
	data, sockets string

	mu sync.Mutex
	h  *dbtest.Daemon // nil while the daemon is stopped
}

// Serve starts the daemon of the data directory dir, which no other daemon of the process serves, and makes it listen on
// sockets in a new directory. A directory that is missing or empty is made first, as whiteroom-db init makes it, and the
// caller makes the store in it with store.Create(ctx, d.Dialer()); Close ends the daemon and removes the directory of the sockets.
func Serve(dir string) (*Daemon, error) {
	if entries, err := os.ReadDir(dir); errors.Is(err, fs.ErrNotExist) || err == nil && len(entries) == 0 {
		if err := dbtest.Init(dir, dbtest.Options{}); err != nil {
			return nil, err
		}
	}
	sockets, err := os.MkdirTemp("", "wr")
	if err != nil {
		return nil, err
	}
	d := &Daemon{data: dir, sockets: sockets}
	if n := len(filepath.Join(sockets, dbwire.WriterSocket)); n > 100 {
		_ = os.RemoveAll(sockets)
		return nil, fmt.Errorf("the path of a socket would have %d bytes: %s", n, sockets)
	}
	if err := d.Start(); err != nil {
		_ = os.RemoveAll(sockets)
		return nil, err
	}
	return d, nil
}

// Dialer returns the Dialer of the sockets of d: it connects to the daemon while one runs, and fails while it is stopped.
func (d *Daemon) Dialer() dbclient.Dialer { return dbclient.SocketDir(d.sockets) }

// Stop ends the daemon: the requests that run end for a moment, the connections are closed, and the sockets are gone. The
// data directory is as the daemon left it.
func (d *Daemon) Stop() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.h == nil {
		return nil
	}
	h := d.h
	d.h = nil
	return h.Release()
}

// Start starts the daemon again on the same sockets, after Stop. It does nothing if the daemon runs.
func (d *Daemon) Start() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.h != nil {
		return nil
	}
	h, err := dbtest.Acquire(d.data, dbtest.Options{})
	if err != nil {
		return err
	}
	if err := h.Listen(d.sockets); err != nil {
		_ = h.Release()
		return err
	}
	d.h = h
	return nil
}

// Close stops the daemon, and removes the directory of its sockets.
func (d *Daemon) Close() error {
	err := d.Stop()
	return errors.Join(err, os.RemoveAll(d.sockets))
}

// CountWrites returns a Dialer that dials through d and counts the Write calls of the connections that it makes, and the
// function that returns the count so far. A request, and a window of reads that the client sends before it reads their
// replies, are one Write call each while they fit a frame, so the count is the round trips. It passes on String and Close
// of d.
func CountWrites(d dbclient.Dialer) (dbclient.Dialer, func() int) {
	c := &countDialer{Dialer: d}
	return c, func() int { return int(c.writes.Load()) }
}

type countDialer struct {
	dbclient.Dialer
	writes atomic.Int64
}

func (c *countDialer) Dial(ctx context.Context, role dbwire.Role) (net.Conn, error) {
	conn, err := c.Dialer.Dial(ctx, role)
	if err != nil {
		return nil, err
	}
	return &countConn{Conn: conn, writes: &c.writes}, nil
}

func (c *countDialer) String() string {
	if s, ok := c.Dialer.(fmt.Stringer); ok {
		return s.String()
	}
	return fmt.Sprintf("%T", c.Dialer)
}

func (c *countDialer) Close() error {
	if cl, ok := c.Dialer.(interface{ Close() error }); ok {
		return cl.Close()
	}
	return nil
}

type countConn struct {
	net.Conn
	writes *atomic.Int64
}

func (c *countConn) Write(p []byte) (int, error) {
	c.writes.Add(1)
	return c.Conn.Write(p)
}
