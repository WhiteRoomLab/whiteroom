package dbserver

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/unixsock"
)

// Config is what Run needs.
type Config struct {
	// Data is the data directory, and Sockets the directory of the two sockets, dbwire.WriterSocket and
	// dbwire.ReaderSocket, which must exist.
	Data, Sockets string
	// WriterUID is the Unix user that the peer of the writer socket must have.
	WriterUID uint32
	// WriterGID and ReaderGID are the groups of the two socket files; -1 leaves the group that the file has.
	WriterGID, ReaderGID int
	Options              Options
	// Heads is where the lines of the heads go: at the start, at each tick and at the stop. Nil: they are not written.
	Heads io.Writer
	// Ticks returns the ticks of the export of the heads, and the function that stops them; nil: one a minute.
	Ticks func() (<-chan time.Time, func())
	// Ready is called once, when both sockets listen.
	Ready func()

	shutdownWait time.Duration // for the tests: how long the requests in progress are waited for; 0: shutdownWait
}

// shutdownWait is how long Run lets the requests in progress end after its context has ended.
const shutdownWait = 10 * time.Second

// connectionSlack is how many connections beyond its sessions a listener has open at most: those that have not said
// Hello, which each last no longer than Limits.Stall.
const connectionSlack = 16

// errNotLinux is the error of Run on a system that does not tell the user of a peer.
var errNotLinux = errors.New("runs on Linux only: it needs SO_PEERCRED to know the user of a peer")

// Run is the daemon: it opens the data directory, serves the writer socket to the connections whose peer has the Unix
// user cfg.WriterUID, and the reader socket, as readers, to every peer, and exports the heads of the audit logs to
// cfg.Heads, until ctx ends. Then it stops listening, which removes the socket files, lets the requests in progress end
// for up to 10 seconds, writes the heads a last time, closes the engine, and returns nil.
func Run(ctx context.Context, cfg Config) error {
	if !peerCreds {
		return errNotLinux
	}
	for _, name := range []string{dbwire.WriterSocket, dbwire.ReaderSocket} {
		if err := unixsock.CheckPath(filepath.Join(cfg.Sockets, name)); err != nil {
			return err
		}
	}
	s, err := OpenDir(cfg.Data, cfg.Options)
	if err != nil {
		return err
	}
	var listeners []net.Listener
	closeListeners := func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}
	for _, sock := range []struct {
		name string
		gid  int
	}{{dbwire.WriterSocket, cfg.WriterGID}, {dbwire.ReaderSocket, cfg.ReaderGID}} {
		p := filepath.Join(cfg.Sockets, sock.name)
		l, err := unixsock.Listen(p)
		if err == nil {
			listeners = append(listeners, l)
			if sock.gid >= 0 {
				err = os.Chown(p, -1, sock.gid)
			}
		}
		if err != nil {
			closeListeners()
			return errors.Join(err, s.Close())
		}
	}

	var serving sync.WaitGroup
	failed := make(chan error, 2)
	serve := func(l net.Listener, role dbwire.Role, check func(net.Conn) error) {
		serving.Add(1)
		go func() {
			defer serving.Done()
			if err := s.Serve(l, role, check); err != nil {
				failed <- err
			}
		}()
	}
	serve(listeners[0], dbwire.RoleWriter, func(conn net.Conn) error {
		switch uid, err := peerUID(conn); {
		case err != nil:
			return fmt.Errorf("cannot tell the user of the peer: %w", err)
		case uid != cfg.WriterUID:
			return fmt.Errorf("the user %d is not the writer of this daemon", uid)
		}
		return nil
	})
	serve(listeners[1], dbwire.RoleReader, nil)

	stopExport := func() {}
	if cfg.Heads != nil {
		var ticks <-chan time.Time
		stopTicks := func() {}
		if cfg.Ticks != nil {
			ticks, stopTicks = cfg.Ticks()
		} else {
			t := time.NewTicker(time.Minute)
			ticks, stopTicks = t.C, t.Stop
		}
		// Its own context: the last write needs the engine, which stays open until the requests have ended.
		exportCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		exported := make(chan struct{})
		go func() {
			defer close(exported)
			_ = s.ExportHeads(exportCtx, cfg.Heads, ticks, func(err error) { s.logf("%v", err) })
		}()
		stopExport = func() {
			cancel()
			<-exported
			stopTicks()
		}
	}
	if cfg.Ready != nil {
		cfg.Ready()
	}

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-failed:
	}
	closeListeners()
	waited, cancel := context.WithTimeout(context.Background(), cmp.Or(cfg.shutdownWait, shutdownWait))
	s.Shutdown(waited)
	cancel()
	stopExport()
	closeErr := s.Close()
	serving.Wait()
	return errors.Join(serveErr, closeErr)
}

// Serve serves the connections that the listener l accepts, with the role, until l is closed, and then returns nil; an
// error of Accept that cannot be waited out ends it with that error. It keeps at most Limits.Readers + 16 connections of
// a listener of readers, and at most 1 + 16 of one of the writer, open at a time, from Accept to the end of the session:
// while it is at that number it does not accept, and the kernel's backlog holds the rest. check, if it is not nil, is
// called with each connection, and a connection for which it returns an error is denied: the session reads its Hello,
// answers CodeDenied with the text of the error and closes it. Serve does not wait for its sessions, which Close and
// Shutdown end.
func (s *Server) Serve(l net.Listener, role dbwire.Role, check func(net.Conn) error) error {
	if role != dbwire.RoleReader && role != dbwire.RoleWriter {
		return fmt.Errorf("cannot serve the role %d", role)
	}
	bound := 1 + connectionSlack
	if role == dbwire.RoleReader {
		// The cap keeps the sum from overflowing, which would make the channel panic.
		bound = min(s.limits.Readers, math.MaxInt-connectionSlack) + connectionSlack
	}
	slots := make(chan struct{}, bound)
	var backoff time.Duration
	for {
		if !s.takeSlot(l, slots) {
			return nil
		}
		conn, err := l.Accept()
		if err != nil {
			<-slots
			switch {
			case errors.Is(err, net.ErrClosed):
				return nil
			case waitable(err):
				backoff = min(max(2*backoff, 5*time.Millisecond), time.Second)
				if sleepContext(s.ctx, backoff) != nil {
					return nil
				}
				continue
			}
			return err
		}
		backoff = 0
		if !s.track() {
			_ = conn.Close()
			<-slots
			return nil
		}
		go func() {
			defer s.wg.Done()
			defer func() { <-slots }()
			var deny error
			if check != nil {
				deny = check(conn)
			}
			s.serveConn(conn, role, deny)
		}()
	}
}

// slotPoll is how often Serve, which waits for a session to end, looks whether its listener has been closed.
const slotPoll = 50 * time.Millisecond

// takeSlot waits for a free slot, and returns false if Serve is to end instead: the server has closed, or the listener.
func (s *Server) takeSlot(l net.Listener, slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	default:
	}
	tick := time.NewTicker(slotPoll)
	defer tick.Stop()
	for {
		select {
		case slots <- struct{}{}:
			return true
		case <-s.ctx.Done():
			return false
		case <-tick.C:
			if listenerClosed(l) {
				return false
			}
		}
	}
}

// listenerClosed tells whether l is closed, for a listener that has a file descriptor: it looks at the descriptor and
// does not accept.
func listenerClosed(l net.Listener) bool {
	sc, ok := l.(syscall.Conn)
	if !ok {
		return false
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		return true
	}
	return rc.Control(func(uintptr) {}) != nil
}

// waitable reports whether the error of Accept is one that goes by: no descriptor or memory left for now.
func waitable(err error) bool {
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		return true
	}
	for _, errno := range []syscall.Errno{syscall.EMFILE, syscall.ENFILE, syscall.ENOBUFS, syscall.ENOMEM} {
		if errors.Is(err, errno) {
			return true
		}
	}
	return false
}

// track counts a connection that Serve is about to serve, unless the server is closing.
func (s *Server) track() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return false
	}
	s.wg.Add(1)
	return true
}
