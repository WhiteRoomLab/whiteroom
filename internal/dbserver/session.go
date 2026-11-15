package dbserver

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

const closingText = "the daemon is closing"

// readBuffer is the size of the buffer that dbwire.NewReader makes, so that it reads from the buffer of the session
// itself, whose Peek waits for the first byte of a request.
const readBuffer = 32 << 10

// writeStep bounds what one Write of the connection has to deliver within the stall limit.
const writeStep = 64 << 10

// timedConn gives every read of a request and every write of a reply the stall limit as its deadline. Between the
// requests, busy is false and the session sets the deadlines of its wait.
type timedConn struct {
	net.Conn
	stall time.Duration
	busy  bool // used by the goroutine of the session only
}

func (c *timedConn) Read(p []byte) (int, error) {
	if c.busy && c.stall > 0 {
		_ = c.SetReadDeadline(time.Now().Add(c.stall))
	}
	return c.Conn.Read(p)
}

func (c *timedConn) Write(p []byte) (int, error) {
	total := 0
	for {
		n := min(len(p), writeStep)
		if c.stall > 0 {
			_ = c.SetWriteDeadline(time.Now().Add(c.stall))
		}
		m, err := c.Conn.Write(p[:n])
		total += m
		if err != nil {
			return total, err
		}
		if p = p[n:]; len(p) == 0 {
			return total, nil
		}
	}
}

type session struct {
	s      *Server
	conn   *timedConn
	br     *bufio.Reader
	rd     *dbwire.Reader
	wr     *dbwire.Writer
	role   dbwire.Role
	ctx    context.Context // ended with the session, or when the server closes
	cancel context.CancelFunc
	hello  bool  // counted in the roles of the server
	deny   error // not nil: the peer is not let in, and the Hello is answered with it
	snap   *snapshot
	staged staged
}

func (s *Server) newSession(conn net.Conn, role dbwire.Role) *session {
	if role != dbwire.RoleReader && role != dbwire.RoleWriter {
		return nil
	}
	tc := &timedConn{Conn: conn, stall: s.limits.Stall, busy: true}
	c := &session{s: s, conn: tc, br: bufio.NewReaderSize(tc, readBuffer), role: role}
	c.rd = dbwire.NewReader(c.br, dbwire.Limits{}.WithDefaults().Frame)
	c.wr = dbwire.NewWriter(tc, dbwire.Limits{}.WithDefaults().Frame)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return nil
	}
	c.ctx, c.cancel = context.WithCancel(s.ctx)
	s.sessions[c] = struct{}{}
	s.wg.Add(1)
	return c
}

func (c *session) serve() {
	defer c.end()
	if !c.handshake() {
		return
	}
	for {
		c.conn.busy = false
		if !c.wait() {
			return
		}
		c.conn.busy = true
		if !c.request() {
			return
		}
	}
}

// request reads and answers one request. A server that is shutting down refuses it.
func (c *session) request() bool {
	if !c.s.beginRequest() {
		return c.refuseAndClose(dbwire.CodeBusy, closingText)
	}
	defer c.s.endRequest()
	kind, err := c.rd.Next()
	if err != nil {
		c.bad(err)
		return false
	}
	return c.dispatch(kind)
}

func (c *session) end() {
	c.cancel()
	c.dropSnapshot()
	c.s.mu.Lock()
	delete(c.s.sessions, c)
	if c.hello {
		if c.role == dbwire.RoleWriter {
			c.s.writers--
		} else {
			c.s.readers--
		}
	}
	c.s.mu.Unlock()
	_ = c.conn.Close()
	c.s.wg.Done()
}

// wait returns when a byte of the next request has arrived, and false when the connection has ended. While a snapshot
// is held, it releases it after Limits.SnapshotIdle without a request, and goes on waiting.
func (c *session) wait() bool {
	for {
		var deadline time.Time
		if c.snap != nil && c.s.limits.SnapshotIdle > 0 {
			deadline = time.Now().Add(c.s.limits.SnapshotIdle)
		}
		_ = c.conn.SetReadDeadline(deadline)
		_, err := c.br.Peek(1)
		switch {
		case err == nil:
			return true
		case c.snap != nil && errors.Is(err, os.ErrDeadlineExceeded):
			c.dropSnapshot()
			if c.s.onIdle != nil {
				c.s.onIdle()
			}
		default:
			return false
		}
	}
}

func (c *session) handshake() bool {
	kind, err := c.rd.Next()
	if err != nil {
		c.bad(err)
		return false
	}
	if kind != dbwire.KindHello {
		return c.refuseAndClose(dbwire.CodeProtocol, fmt.Sprintf("the first message must be a Hello, not a %v", kind))
	}
	d := &dbwire.Dec{R: c.rd}
	version, err := dbwire.ReadHelloRequest(d)
	switch {
	case err != nil:
		c.bad(err)
		return false
	case c.deny != nil:
		return c.refuseAndClose(dbwire.CodeDenied, c.deny.Error())
	case version != dbwire.Version:
		return c.refuseAndClose(dbwire.CodeVersion,
			fmt.Sprintf("protocol version %d, but the daemon speaks version %d", version, dbwire.Version))
	}
	if err := d.End(); err != nil {
		c.bad(err)
		return false
	}
	if code, msg := c.register(); code != 0 {
		return c.refuseAndClose(code, msg)
	}
	seq, err := c.s.lastSeq()
	if err != nil {
		return c.refuseAndClose(dbwire.CodeFailed, safeText(err))
	}
	l := c.s.limits
	h := dbwire.HelloReply{Version: dbwire.Version, Role: c.role, BootID: c.s.bootID, Seq: seq,
		Stopped: c.s.stopped.Load(), Frame: l.Frame, Commit: l.Commit, Object: l.Object, Staged: l.Staged,
		MaxKey: dbwire.MaxKey}
	if !c.ok(func(e *dbwire.Enc) { e.HelloReply(h) }) {
		return false
	}
	c.rd.SetMaxFrame(l.Frame)
	c.wr.SetMaxFrame(l.Frame)
	return true
}

func (c *session) register() (dbwire.Code, string) {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closing, s.draining:
		return dbwire.CodeBusy, closingText
	case c.role == dbwire.RoleWriter && s.writers > 0:
		return dbwire.CodeBusy, "another writer is connected"
	case c.role == dbwire.RoleReader && s.readers >= s.limits.Readers:
		return dbwire.CodeBusy, fmt.Sprintf("%d readers are connected, the most that the daemon serves", s.readers)
	case c.role == dbwire.RoleWriter:
		s.writers++
	default:
		s.readers++
	}
	c.hello = true
	return 0, ""
}

func (c *session) ok(f func(e *dbwire.Enc)) bool {
	c.wr.Begin(dbwire.KindOK)
	e := &dbwire.Enc{W: c.wr}
	if f != nil {
		f(e)
	}
	if e.Err != nil {
		_ = c.wr.Fail(dbwire.CodeInternal, "the reply could not be written")
		return false
	}
	return c.wr.End() == nil
}

func (c *session) fail(code dbwire.Code, msg string) bool {
	return c.wr.Fail(code, msg) == nil
}

func (c *session) refuseAndClose(code dbwire.Code, msg string) bool {
	_ = c.wr.Fail(code, msg)
	return false
}

// bad answers a request that could not be read, if the peer can still be told why: the connection is closed after it.
func (c *session) bad(err error) {
	switch {
	case errors.Is(err, dbwire.ErrLimit):
		_ = c.wr.Fail(dbwire.CodeLimit, err.Error())
	case errors.Is(err, dbwire.ErrProtocol):
		_ = c.wr.Fail(dbwire.CodeProtocol, err.Error())
	}
}

// refuseBody reads what is left of a request that is not acted on, at most max bytes of it, and answers with code. A
// body that is longer ends the session.
func (c *session) refuseBody(max int64, code dbwire.Code, msg string) bool {
	n, err := io.Copy(io.Discard, io.LimitReader(c.rd, max+1))
	switch {
	case err != nil:
		c.bad(err)
		return false
	case n > max:
		return c.refuseAndClose(dbwire.CodeLimit, fmt.Sprintf("a request of more than %d bytes", max))
	}
	return c.fail(code, msg)
}

func (c *session) dropSnapshot() {
	c.staged = staged{}
	if c.snap != nil {
		c.snap.release()
		c.snap = nil
	}
}

var handlers = map[dbwire.Kind]func(*session) bool{
	dbwire.KindSnapshot:    (*session).snapshotRequest,
	dbwire.KindRelease:     (*session).releaseRequest,
	dbwire.KindGet:         (*session).getRequest,
	dbwire.KindScan:        (*session).scanRequest,
	dbwire.KindObject:      (*session).objectRequest,
	dbwire.KindOriginal:    (*session).originalRequest,
	dbwire.KindEvents:      (*session).eventsRequest,
	dbwire.KindVerify:      (*session).verifyRequest,
	dbwire.KindBackup:      (*session).backupRequest,
	dbwire.KindPutObject:   (*session).putObjectRequest,
	dbwire.KindPutOriginal: (*session).putOriginalRequest,
	dbwire.KindCommit:      (*session).commitRequest,
}

func (c *session) dispatch(kind dbwire.Kind) bool {
	h := handlers[kind]
	if h == nil { // a Hello again, or a reply, which only the daemon sends
		return c.refuseAndClose(dbwire.CodeProtocol, fmt.Sprintf("a %v where a request is due", kind))
	}
	return h(c)
}

func (c *session) empty() error {
	return dbwire.ReadEmpty(&dbwire.Dec{R: c.rd})
}

func (c *session) snapshotRequest() bool {
	if err := c.empty(); err != nil {
		c.bad(err)
		return false
	}
	c.dropSnapshot()
	sn, err := c.s.openSnapshot()
	if err != nil {
		return c.fail(dbwire.CodeFailed, safeText(err))
	}
	c.snap = sn
	return c.ok(func(e *dbwire.Enc) { e.SnapshotReply(sn.seq) })
}

func (c *session) releaseRequest() bool {
	if err := c.empty(); err != nil {
		c.bad(err)
		return false
	}
	c.dropSnapshot()
	return c.ok(nil)
}
