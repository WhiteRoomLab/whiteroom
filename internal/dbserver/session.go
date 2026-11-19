package dbserver

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

const closingText = "the daemon is closing"

// replyHold is how many bytes of replies to reads wait in the buffer for the requests that have arrived behind them.
const replyHold = dbwire.ReadBuffer

// writeStep bounds what one Write of the connection has to deliver within the stall limit.
const writeStep = 64 << 10

// timedConn gives every read of a request and every write of a reply the stall limit as its deadline. Between the
// requests, busy is false and the session sets the deadlines of its wait.
//
// A deadline comes from the clock of the server, which is read once a tick, and is set only when that clock has moved
// since: it is never earlier than its limit after the read or the write began, and at most four ticks later. A time-out
// that a late clock may have caused is tried once more with a deadline from the real clock.
//
// Why four ticks: a deadline that is set at the time t from the reading r0 is early only if t-r0 is more than its margin.
// The reading after r0 is stored after t, and the one after that is taken later still, so t-r0 is less than the two gaps
// after r0 together. A gap of more than two ticks is marked in lateAt before its reading is stored, and early finds the
// mark, or finds that the last reading is more than two ticks old; otherwise t-r0 is at most four ticks.
type timedConn struct {
	net.Conn
	s     *Server
	stall time.Duration
	busy  bool // used by the goroutine of the session only
	read  deadline
	write deadline
}

// deadline is what timedConn knows of the deadline that it has set.
type deadline struct {
	limit time.Duration // 0: none is set
	at    int64         // the clock of the server when it was set
	real  bool          // it was set from the real clock
}

func (c *timedConn) current(d *deadline, limit time.Duration) bool {
	return d.limit == limit && !d.real && d.at == c.s.coarse.Load()
}

// stamp makes d the deadline of limit, and returns its time.
func (c *timedConn) stamp(d *deadline, limit time.Duration) time.Time {
	at := c.s.coarse.Load()
	*d = deadline{limit: limit, at: at}
	return c.s.deadlineAfter(time.Duration(at), limit, 4*c.s.tick)
}

// deadlineAfter returns the time that is the sum of the parts after the start of the server, which are not negative; the
// zero time, which is no deadline, if the sum is more than a Duration holds: a limit that long never fires.
func (s *Server) deadlineAfter(parts ...time.Duration) time.Time {
	var sum time.Duration
	for _, p := range parts {
		if sum += p; sum < 0 {
			return time.Time{}
		}
	}
	return s.started.Add(sum)
}

// readWithin sets the read deadline limit from now, by the clock of the server, unless it is set. limit 0: no deadline,
// which reads no clock.
func (c *timedConn) readWithin(limit time.Duration) {
	switch {
	case limit <= 0:
		if c.read.limit != 0 {
			_ = c.SetReadDeadline(time.Time{})
			c.read = deadline{}
		}
	case !c.current(&c.read, limit):
		_ = c.SetReadDeadline(c.stamp(&c.read, limit))
	}
}

// early reports whether the time-out of d may have come early because the clock of the server was late, and if so sets d
// again from the real clock with set; that is final, and another time-out is believed.
func (c *timedConn) early(d *deadline, set func(time.Time) error) bool {
	if d.real || d.limit == 0 {
		return false
	}
	now := time.Since(c.s.started)
	at := c.s.coarse.Load() // before lateAt, which the clock stores first
	if c.s.lateAt.Load() <= d.at && now <= time.Duration(at)+2*c.s.tick {
		return false
	}
	_ = set(c.s.deadlineAfter(now, d.limit))
	d.real = true
	return true
}

func (c *timedConn) Read(p []byte) (int, error) {
	if c.busy { // with no stall limit this clears the idle deadline that wait set
		c.readWithin(c.stall)
	}
	n, err := c.Conn.Read(p)
	if n == 0 && errors.Is(err, os.ErrDeadlineExceeded) && c.early(&c.read, c.SetReadDeadline) {
		return c.Conn.Read(p)
	}
	return n, err
}

func (c *timedConn) Write(p []byte) (int, error) {
	total := 0
	for {
		n := min(len(p), writeStep)
		if c.stall > 0 && !c.current(&c.write, c.stall) {
			_ = c.SetWriteDeadline(c.stamp(&c.write, c.stall))
		}
		m, err := c.Conn.Write(p[:n])
		total += m
		if errors.Is(err, os.ErrDeadlineExceeded) && c.early(&c.write, c.SetWriteDeadline) {
			m, err = c.Conn.Write(p[m:n])
			total += m
		}
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
	// opening: a Snapshot request has been read and is still counted as in progress; hold answers it.
	opening bool
	// carried: the request that has ended is still counted, because its reply is held and the next request is already in
	// the read buffer; the next one takes over the count.
	carried bool
	// pulseMu is held by the pulser while it writes a pulse, and by the callbacks of an Events that write its events.
	pulseMu sync.Mutex
}

func (s *Server) newSession(conn net.Conn, role dbwire.Role) *session {
	if role != dbwire.RoleReader && role != dbwire.RoleWriter {
		return nil
	}
	tc := &timedConn{Conn: conn, s: s, stall: s.limits.Stall, busy: true}
	c := &session{s: s, conn: tc, br: bufio.NewReaderSize(tc, dbwire.ReadBuffer), role: role}
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
	for c.step() {
		for c.opening {
			if !c.hold() {
				return
			}
		}
	}
}

// step waits for a request and answers it. It also returns true, with no request answered, when the snapshot has been
// released for being idle. False ends the session.
func (c *session) step() bool {
	c.conn.busy = false
	w := c.wait()
	if w == waitEnded {
		return false
	}
	if w == waitIdle {
		return true
	}
	c.conn.busy = true
	return c.request()
}

// hold opens a snapshot, answers the Snapshot request that asked for it, and serves the requests of the session inside the
// View of the engine until the snapshot is released: a *logdb.Tx is valid only there, and a goroutine of its own for the
// snapshot would cost two goroutine switches for each read. It reports whether the session goes on.
func (c *session) hold() (alive bool) {
	c.opening = false
	called, err := c.s.openSnapshot(func(sn *snapshot) {
		c.snap = sn
		alive = c.ok(func(e *dbwire.Enc) { e.SnapshotReply(sn.seq) })
		c.s.endRequest()
		for alive && c.snap == sn {
			alive = c.step()
		}
		if c.snap == sn {
			c.dropSnapshot()
		}
	})
	if !called {
		alive = c.fail(dbwire.CodeFailed, safeText(err))
		c.s.endRequest()
	}
	return alive
}

// request reads and answers one request. A server that is shutting down refuses it.
func (c *session) request() bool {
	if c.carried {
		c.carried = false
	} else if !c.s.beginRequest() {
		c.wr.SetHold(0)
		return c.refuseAndClose(dbwire.CodeBusy, closingText)
	}
	// A Snapshot request stays in progress until hold has answered it.
	defer func() {
		if !c.opening && !c.carried {
			c.s.endRequest()
		}
	}()
	kind, err := c.rd.Next()
	if err != nil {
		c.bad(err)
		return false
	}
	// Only the replies to reads wait for the requests behind them.
	if kind == dbwire.KindGet || kind == dbwire.KindScan || kind == dbwire.KindObject {
		c.wr.SetHold(replyHold)
	} else {
		c.wr.SetHold(0)
		if c.wr.Flush() != nil {
			return false
		}
	}
	if !c.dispatch(kind) {
		return false
	}
	// The replies are written before the request ends, unless a request has arrived behind them: Shutdown, which waits for
	// the requests in progress, then does not close a connection with a reply in its buffer.
	switch {
	case c.opening:
		return true
	case c.br.Buffered() > 0 && c.wr.Held() > 0 && c.s.keepRequest():
		c.carried = true
		_ = c.s.hook("reply:held")
		return true
	}
	return c.wr.Flush() == nil
}

func (c *session) end() {
	// A reply that is still open is dropped with what is held: the connection is closing, and Flush would panic.
	if !c.wr.Open() {
		_ = c.wr.Flush()
	}
	if c.carried {
		c.carried = false
		c.s.endRequest()
	}
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

// waited is how a wait for a request ended.
type waited int

const (
	waitRequest waited = iota // a byte of a request has arrived
	waitIdle                  // the snapshot was released for being idle; the connection goes on
	waitEnded                 // the connection has ended
)

// wait returns when a byte of the next request has arrived. While a snapshot is held, it releases it after
// Limits.SnapshotIdle without a request; a reader's connection that holds none is ended after the same time, with no
// reply. A writer waits with no limit: it keeps its place.
func (c *session) wait() waited {
	if c.br.Buffered() == 0 && c.wr.Flush() != nil {
		return waitEnded
	}
	if idle := c.s.limits.SnapshotIdle; idle > 0 && (c.snap != nil || c.role == dbwire.RoleReader) {
		c.conn.readWithin(idle)
	} else {
		c.conn.readWithin(0)
	}
	_, err := c.br.Peek(1)
	if err == nil {
		return waitRequest
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		switch {
		case c.snap != nil:
			c.dropSnapshot()
			_ = c.s.hook("snapshot:idle")
			if c.s.onIdle != nil {
				c.s.onIdle()
			}
			return waitIdle
		case c.role == dbwire.RoleReader:
			_ = c.s.hook("reader:idle")
			if c.s.onIdleClose != nil {
				c.s.onIdleClose()
			}
		}
	}
	return waitEnded
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
		MaxKey: dbwire.MaxKey, DataDir: c.s.o.DataDir}
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
	return c.finish(f)
}

// finish writes the body of the reply that is open with f, and ends it.
func (c *session) finish(f func(e *dbwire.Enc)) bool {
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

// dropSnapshot lets go of the snapshot and the staged objects; the View returns when the loop of hold sees it.
func (c *session) dropSnapshot() {
	c.staged = staged{}
	c.snap = nil
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
	c.opening = true
	return true
}

func (c *session) releaseRequest() bool {
	if err := c.empty(); err != nil {
		c.bad(err)
		return false
	}
	c.dropSnapshot()
	return c.ok(nil)
}
