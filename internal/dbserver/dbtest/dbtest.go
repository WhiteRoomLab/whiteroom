// Package dbtest runs DB daemons inside a test process: a daemon is the engine of internal/logdb served through
// internal/dbserver, and the clients of internal/dbclient connect to it over net.Pipe, or over socket files that Listen
// makes. It is for tests only.
package dbtest

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/fspath"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
	"github.com/WhiteRoomLab/whiteroom/internal/unixsock"
)

// releaseWait is how long the last Release lets the requests in progress end.
const releaseWait = time.Second

// Options are the options of Init and Acquire. FS, if it is set, is the data directory, and dir only the name that the
// errors give to it; else dir is a directory on the OS.
type Options struct {
	FS vfs.FS
	// Log are the options of the engine; nil: logdb.Options{Placement: logdb.Hybrid}.
	Log *logdb.Options
	// Limits are the limits of the daemon; nil: no limit of commits per second and no check of the free space.
	Limits *dbwire.Limits
	Now    func() time.Time
	Hook   func(point string) error
	Logf   func(format string, args ...any)
}

func (o Options) server() dbserver.Options {
	so := dbserver.Options{Now: o.Now, Hook: o.Hook, Logf: o.Logf}
	if o.Log != nil {
		so.Log = *o.Log
	} else {
		so.Log = logdb.Options{Placement: logdb.Hybrid}
	}
	if o.Limits != nil {
		so.Limits = *o.Limits
	} else {
		so.Limits = dbwire.Limits{CommitsPerSecond: -1, MinFree: -1, MinFreeInodes: -1}
	}
	return so
}

// Init makes the data directory dir, which must be missing or empty.
func Init(dir string, o Options) error {
	if o.FS != nil {
		return dbserver.Create(o.FS, dir, o.server())
	}
	return dbserver.CreateDir(dir, o.server())
}

// daemon is the server of a data directory, which the handles of Acquire share.
type daemon struct {
	key  any
	dir  string
	srv  *dbserver.Server // set before ready is closed
	refs int              // the handles that are not released, under registry.mu

	ready   chan struct{} // closed when srv is open, or the open has failed (err)
	err     error
	closing bool          // the last handle has been released, under registry.mu
	closed  chan struct{} // closed when it has been closed

	mu        sync.Mutex
	gone      bool // released or abandoned: no new connection
	listeners []net.Listener
	serving   sync.WaitGroup // the Serve calls of Listen, and the sessions that Dial started
}

// registry has the daemons by their keys; its lock is held only to read or change the map, and never while an engine
// opens or closes, so that a daemon that hangs does not stop the others.
var registry struct {
	mu sync.Mutex
	m  map[any]*daemon
}

// Daemon is a handle of a daemon. The daemon of a data directory is one in the process at a time, and the last of its
// handles to be released closes it.
type Daemon struct {
	d        *daemon
	released atomic.Bool
}

// Acquire returns a handle of the daemon of the data directory dir, which the first Acquire opens with o: later ones
// share it, and o is not looked at. While the daemon of dir is being closed, Acquire waits for it.
func Acquire(dir string, o Options) (*Daemon, error) {
	var key any
	if o.FS != nil {
		key = o.FS
	} else {
		abs, err := fspath.Abs(dir)
		if err != nil {
			return nil, err
		}
		key = abs
	}
	for {
		registry.mu.Lock()
		d, ok := registry.m[key]
		switch {
		case !ok:
			d = &daemon{key: key, dir: dir, refs: 1, ready: make(chan struct{}), closed: make(chan struct{})}
			if registry.m == nil {
				registry.m = map[any]*daemon{}
			}
			registry.m[key] = d
			registry.mu.Unlock()
			return d.open(o)
		case d.closing:
			registry.mu.Unlock()
			<-d.closed
		default:
			d.refs++
			registry.mu.Unlock()
			<-d.ready
			if d.err == nil {
				return &Daemon{d: d}, nil
			}
		}
	}
}

// open opens the engine of the new daemon d, which is in the registry, and returns its first handle.
func (d *daemon) open(o Options) (*Daemon, error) {
	var err error
	if o.FS != nil {
		d.srv, err = dbserver.Open(o.FS, d.dir, o.server())
	} else {
		d.srv, err = dbserver.OpenDir(d.dir, o.server())
	}
	if err != nil {
		d.err = err
		registry.mu.Lock()
		delete(registry.m, d.key)
		registry.mu.Unlock()
		close(d.ready)
		close(d.closed)
		return nil, err
	}
	close(d.ready)
	return &Daemon{d: d}, nil
}

// Release gives up the handle; it does nothing the second time. The last release of a daemon closes its listeners, lets
// the requests in progress end for a short while, closes the connections and the engine, and returns the error of that.
func (h *Daemon) Release() error {
	if h.released.Swap(true) {
		return nil
	}
	d := h.d
	registry.mu.Lock()
	if d.refs--; d.refs > 0 {
		registry.mu.Unlock()
		return nil
	}
	d.closing = true
	registry.mu.Unlock()
	defer func() {
		registry.mu.Lock()
		if registry.m[d.key] == d { // an abandoned daemon has left already
			delete(registry.m, d.key)
		}
		registry.mu.Unlock()
		close(d.closed)
	}()
	if d.stop() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), releaseWait)
	d.srv.Shutdown(ctx)
	cancel()
	err := d.srv.Close()
	d.serving.Wait()
	return err
}

// stop ends the new connections, and reports whether the daemon had been abandoned.
func (d *daemon) stop() (abandoned bool) {
	d.mu.Lock()
	abandoned = d.gone
	d.gone = true
	listeners := d.listeners
	d.listeners = nil
	d.mu.Unlock()
	for _, l := range listeners {
		_ = l.Close()
	}
	return abandoned
}

// Abandon drops the daemon from the process and closes its connections and listeners, but leaves its engine open, as a
// process that has been killed or has hung does: for the tests of a crash on a simulated file system. Its handles
// release nothing from then on.
func (h *Daemon) Abandon() {
	d := h.d
	registry.mu.Lock()
	if registry.m[d.key] == d {
		delete(registry.m, d.key)
	}
	registry.mu.Unlock()
	d.stop()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d.srv.Shutdown(ctx)
}

// Server returns the server of the daemon.
func (h *Daemon) Server() *dbserver.Server { return h.d.srv }

// Dialer returns the dialer of the handle: it connects over net.Pipe, and its Close releases the handle.
func (h *Daemon) Dialer() dbclient.Dialer { return dialer{h} }

type dialer struct{ h *Daemon }

func (x dialer) Dial(_ context.Context, role dbwire.Role) (net.Conn, error) {
	d := x.h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gone || x.h.released.Load() {
		return nil, fmt.Errorf("the daemon of %s has been released", d.dir)
	}
	client, server := net.Pipe()
	d.serving.Add(1)
	go func() {
		defer d.serving.Done()
		d.srv.ServeConn(server, role)
	}()
	return client, nil
}

func (x dialer) String() string { return x.h.d.dir }

func (x dialer) Close() error { return x.h.Release() }

// Listen makes the sockets of the daemon in the directory sockets, dbwire.WriterSocket and dbwire.ReaderSocket, for the
// clients of other processes, and serves them until the daemon is released. The writer socket has no check of the user.
func (h *Daemon) Listen(sockets string) error {
	d := h.d
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gone {
		return fmt.Errorf("the daemon of %s has been released", d.dir)
	}
	var ls []net.Listener
	roles := []dbwire.Role{dbwire.RoleWriter, dbwire.RoleReader}
	for i, name := range []string{dbwire.WriterSocket, dbwire.ReaderSocket} {
		l, err := unixsock.Listen(filepath.Join(sockets, name))
		if err != nil {
			for _, l := range ls {
				_ = l.Close()
			}
			return err
		}
		ls = append(ls, l)
		d.serving.Add(1)
		go func() {
			defer d.serving.Done()
			_ = d.srv.Serve(l, roles[i], nil)
		}()
	}
	d.listeners = append(d.listeners, ls...)
	return nil
}
