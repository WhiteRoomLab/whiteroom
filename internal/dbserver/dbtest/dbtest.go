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
		so.Limits = dbwire.Limits{CommitsPerSecond: -1, MinFree: -1}
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
	srv  *dbserver.Server
	refs int // the handles that are not released

	mu        sync.Mutex
	gone      bool // released or abandoned: no new connection
	listeners []net.Listener
	serving   sync.WaitGroup // the Serve calls of Listen, and the sessions that Dial started
}

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
// share it, and o is not looked at.
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
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if d, ok := registry.m[key]; ok {
		d.refs++
		return &Daemon{d: d}, nil
	}
	var srv *dbserver.Server
	var err error
	if o.FS != nil {
		srv, err = dbserver.Open(o.FS, dir, o.server())
	} else {
		srv, err = dbserver.OpenDir(dir, o.server())
	}
	if err != nil {
		return nil, err
	}
	d := &daemon{key: key, dir: dir, srv: srv, refs: 1}
	if registry.m == nil {
		registry.m = map[any]*daemon{}
	}
	registry.m[key] = d
	return &Daemon{d: d}, nil
}

// Release gives up the handle; it does nothing the second time. The last release of a daemon closes its listeners, lets
// the requests in progress end for a short while, closes the connections and the engine, and returns the error of that.
func (h *Daemon) Release() error {
	if h.released.Swap(true) {
		return nil
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	d := h.d
	if d.refs--; d.refs > 0 {
		return nil
	}
	if registry.m[d.key] == d { // an abandoned daemon has left already
		delete(registry.m, d.key)
	}
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
	registry.mu.Lock()
	defer registry.mu.Unlock()
	d := h.d
	if registry.m[d.key] == d {
		delete(registry.m, d.key)
	}
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
