package dbserver

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// fsControl makes the calls of the files of a ctlFS wait: each read for delay, and the call of op that comes after skip
// others, until the call that blocked it is let go.
type fsControl struct {
	mu       sync.Mutex
	delay    time.Duration
	stop     string // "read", "write" or "sync"; "": no call blocks
	skip     int
	blocked  chan struct{}
	release  chan struct{}
	releases []func()
	reads    atomic.Int64
}

func (c *fsControl) setDelay(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.delay = d
}

// block makes a call of op, after skip others, wait until release is called. blocked is closed when a call waits.
func (c *fsControl) block(op string, skip int) (blocked <-chan struct{}, release func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stop, c.skip = op, skip
	c.blocked, c.release = make(chan struct{}), make(chan struct{})
	rel := c.release
	var once sync.Once
	release = func() { once.Do(func() { close(rel) }) }
	c.releases = append(c.releases, release)
	return c.blocked, release
}

func (c *fsControl) releaseAll() {
	c.mu.Lock()
	releases := slices.Clone(c.releases)
	c.mu.Unlock()
	for _, release := range releases {
		release()
	}
}

func (c *fsControl) before(op string) {
	if op == "read" {
		c.reads.Add(1)
	}
	c.mu.Lock()
	var delay time.Duration
	if op == "read" {
		delay = c.delay
	}
	var release chan struct{}
	if c.stop == op {
		if c.skip > 0 {
			c.skip--
		} else {
			release = c.release
			close(c.blocked)
			c.stop = ""
		}
	}
	c.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	if release != nil {
		<-release
	}
}

// ctlFS is a file system whose files call the before of its control ahead of each read, write and sync.
type ctlFS struct {
	vfs.FS
	c *fsControl
}

func (x *ctlFS) wrap(f vfs.File, err error) (vfs.File, error) {
	if err != nil {
		return nil, err
	}
	return &ctlFile{File: f, c: x.c}, nil
}

func (x *ctlFS) Open(name string) (vfs.File, error) { return x.wrap(x.FS.Open(name)) }

func (x *ctlFS) OpenFile(name string, flag int, perm fs.FileMode) (vfs.File, error) {
	return x.wrap(x.FS.OpenFile(name, flag, perm))
}

func (x *ctlFS) Sub(name string) (vfs.FS, error) {
	sub, err := x.FS.Sub(name)
	if err != nil {
		return nil, err
	}
	return &ctlFS{FS: sub, c: x.c}, nil
}

type ctlFile struct {
	vfs.File
	c *fsControl
}

func (f *ctlFile) Read(b []byte) (int, error) {
	f.c.before("read")
	return f.File.Read(b)
}

func (f *ctlFile) ReadAt(b []byte, off int64) (int, error) {
	f.c.before("read")
	return f.File.ReadAt(b, off)
}

func (f *ctlFile) Write(b []byte) (int, error) {
	f.c.before("write")
	return f.File.Write(b)
}

func (f *ctlFile) WriteAt(b []byte, off int64) (int, error) {
	f.c.before("write")
	return f.File.WriteAt(b, off)
}

func (f *ctlFile) Sync() error {
	f.c.before("sync")
	return f.File.Sync()
}

// pulseFixture is a fixture whose engine files wait as ctl says, and which counts the pulses that its server writes.
type pulseFixture struct {
	*fixture
	ctl    *fsControl
	pulse  time.Duration
	pulses atomic.Int64
}

// newPulseFixture makes a server that pulses at the interval pulse, with small segments in its log. mod changes the
// options; its Hook, if it sets one, is called too.
func newPulseFixture(t testing.TB, pulse time.Duration, mod func(*Options)) *pulseFixture {
	t.Helper()
	p := &pulseFixture{ctl: &fsControl{}, pulse: pulse}
	p.fixture = newFixtureWith(t, func(root vfs.FS) vfs.FS { return &ctlFS{FS: root, c: p.ctl} }, func(o *Options) {
		o.Pulse = pulse
		o.Log.SegmentBytes = 1 << 10
		if mod != nil {
			mod(o)
		}
		inner := o.Hook
		o.Hook = func(point string) error {
			if point == "reply:pulse" {
				p.pulses.Add(1)
			}
			if inner != nil {
				return inner(point)
			}
			return nil
		}
	})
	t.Cleanup(p.ctl.releaseAll) // before the server is closed: a call that is blocked does not see its context
	return p
}

// fill commits to the store what is in several segments of the log, and several objects, and closes its writer.
func (p *pulseFixture) fill() {
	p.t.Helper()
	w := p.connect(dbwire.RoleWriter)
	for i := range 8 {
		seq := w.mustSnapshot()
		if _, err := w.putObject(strings.Repeat(string(rune('a'+i)), 600)); err != nil {
			p.t.Fatal(err)
		}
		if _, _, err := w.commit(seq, put(2, fmt.Sprintf("k%d", i), "v"), event(strings.Repeat("e", 300))); err != nil {
			p.t.Fatal(err)
		}
	}
	_ = w.conn.Close()
	p.waitSessions(0)
}

// quiet waits until the engine has made no call that has returned for a while.
func (p *pulseFixture) quiet() {
	p.t.Helper()
	last, since := p.srv.moved.Load(), time.Now()
	eventually(p.t, "the engine to be quiet", func() bool {
		if now := p.srv.moved.Load(); now != last {
			last, since = now, time.Now()
		}
		return time.Since(since) > 5*p.pulse
	})
}

// A frame is a frame of a reply as it is on the wire.
type frame struct {
	kind  dbwire.Kind
	flags byte
	body  []byte
}

func (fr frame) last() bool { return fr.flags&1 != 0 }

// pulse reports whether fr is a pulse, which the last frame is not.
func (fr frame) pulse() bool { return fr.kind == dbwire.KindOK && fr.flags == 0 && len(fr.body) == 0 }

// code returns the code of a frame that is an error.
func (fr frame) code() dbwire.Code {
	if fr.kind != dbwire.KindError || len(fr.body) < 4 {
		return 0
	}
	return dbwire.Code(binary.BigEndian.Uint16(fr.body))
}

// readFrame reads one frame of a reply from conn.
func readFrame(conn io.Reader) (frame, error) {
	var h [dbwire.HeaderSize]byte
	if _, err := io.ReadFull(conn, h[:]); err != nil {
		return frame{}, err
	}
	fr := frame{kind: dbwire.Kind(h[0]), flags: h[1], body: make([]byte, binary.BigEndian.Uint32(h[4:]))}
	if _, err := io.ReadFull(conn, fr.body); err != nil {
		return frame{}, err
	}
	return fr, nil
}

// readFrames reads the frames of one reply from conn, to its last frame.
func readFrames(conn io.Reader) ([]frame, error) {
	var frames []frame
	for {
		fr, err := readFrame(conn)
		if err != nil {
			return frames, fmt.Errorf("after %d frames: %w", len(frames), err)
		}
		frames = append(frames, fr)
		if fr.last() {
			return frames, nil
		}
	}
}

// rawReply reads the frames of the reply to the request that c has sent, which the reader of c has not read.
func (c *client) rawReply() []frame {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(waitLimit))
	frames, err := readFrames(c.conn)
	if err != nil {
		c.t.Fatalf("reading a reply: %v", err)
	}
	return frames
}

// pulsesIn counts the pulses of the frames of a reply.
func pulsesIn(frames []frame) int {
	n := 0
	for _, fr := range frames[:len(frames)-1] {
		if fr.pulse() {
			n++
		}
	}
	return n
}

// slowRead is how long a read of the engine takes in the tests that need a request to last, and testPulse is the
// interval of their pulses: the engine moves every few intervals.
const (
	slowRead  = 25 * time.Millisecond
	testPulse = 10 * time.Millisecond
)

// commitBody is the body of a Commit.
func commitBody(expected uint64, ops ...dbwire.Op) func(e *dbwire.Enc) {
	return func(e *dbwire.Enc) {
		e.CommitRequest(expected)
		for _, o := range ops {
			e.CommitOp(o)
		}
		e.CommitEnd()
	}
}

// replyOf reads the frames of the reply to the request that c sends, from another goroutine.
type replyOf struct {
	frames []frame
	err    error
}

func (c *client) replyAsync(kind dbwire.Kind, body func(e *dbwire.Enc)) <-chan replyOf {
	done := make(chan replyOf, 1)
	sent := c.send(kind, body)
	go func() {
		frames, err := readFrames(c.conn)
		if serr := <-sent; err == nil {
			err = serr
		}
		done <- replyOf{frames, err}
	}()
	return done
}

// counted is the reply to a request that another goroutine reads, whose pulses can be counted while the reply is open.
type counted struct {
	pulses atomic.Int64
	done   chan replyOf
}

func (c *client) replyCounted(kind dbwire.Kind, body func(e *dbwire.Enc)) *counted {
	r := &counted{done: make(chan replyOf, 1)}
	sent := c.send(kind, body)
	go func() {
		var frames []frame
		var err error
		for {
			var fr frame
			if fr, err = readFrame(c.conn); err != nil {
				break
			}
			if fr.pulse() {
				r.pulses.Add(1)
			}
			frames = append(frames, fr)
			if fr.last() {
				break
			}
		}
		if serr := <-sent; err == nil {
			err = serr
		}
		r.done <- replyOf{frames, err}
	}()
	return r
}

// settled waits until n has not changed for a while, and returns it.
func (p *pulseFixture) settled(what string, n *atomic.Int64) int64 {
	p.t.Helper()
	last, since := n.Load(), time.Now()
	eventually(p.t, what, func() bool {
		if now := n.Load(); now != last {
			last, since = now, time.Now()
		}
		return time.Since(since) > 5*p.pulse
	})
	return last
}

// mover makes the files of the engine move, from the connection m, which holds a snapshot, by reading an object again and
// again until stop is called: it is the daemon at work for others, that a request of its own does not wait for.
func (p *pulseFixture) mover(m *client) (stop func()) {
	hash := hashOf(strings.Repeat("a", 600))
	quit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-quit:
				return
			default:
			}
			if found, _, err := m.object(hash); err != nil || !found {
				p.t.Errorf("reading an object: found %v, %v", found, err)
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(quit)
			<-done
		})
	}
}

// setup makes a pulse fixture whose store is filled, and whose reads are slow.
func setupPulse(t *testing.T, mod func(*Options)) *pulseFixture {
	t.Helper()
	p := newPulseFixture(t, testPulse, mod)
	p.fill()
	p.ctl.setDelay(slowRead)
	return p
}

// TestPulseNotWhenFast: a request that is answered before the interval has passed has the reply of version 2: the frames
// of its result and no pulse.
func TestPulseNotWhenFast(t *testing.T) {
	var pulses atomic.Int64
	f := newFixture(t, func(o *Options) {
		o.Hook = func(point string) error {
			if point == "reply:pulse" {
				pulses.Add(1)
			}
			return nil
		}
	})
	populate(t, f)
	w := f.connect(dbwire.RoleWriter)
	r := f.connect(dbwire.RoleReader)
	seq := w.mustSnapshot()
	r.mustSnapshot()

	t.Run("Verify", func(t *testing.T) {
		sent := r.send(dbwire.KindVerify, nil)
		frames := r.rawReply()
		if err := <-sent; err != nil {
			t.Fatal(err)
		}
		if len(frames) != 1 || frames[0].kind != dbwire.KindOK || frames[0].flags != 1 || len(frames[0].body) != 0 {
			t.Errorf("got %+v, want the one empty frame that ends an OK", frames)
		}
	})
	t.Run("Events", func(t *testing.T) {
		sent := r.send(dbwire.KindEvents, nil)
		frames := r.rawReply()
		if err := <-sent; err != nil {
			t.Fatal(err)
		}
		if n := pulsesIn(frames); n != 0 {
			t.Errorf("%d pulses in %d frames", n, len(frames))
		}
	})
	t.Run("Backup", func(t *testing.T) {
		sent := r.send(dbwire.KindBackup, nil)
		frames := r.rawReply()
		if err := <-sent; err != nil {
			t.Fatal(err)
		}
		if n := pulsesIn(frames); n != 0 {
			t.Errorf("%d pulses in %d frames", n, len(frames))
		}
	})
	t.Run("Commit", func(t *testing.T) {
		sent := w.send(dbwire.KindCommit, commitBody(seq, put(2, "z", "1"), event("z")))
		frames := w.rawReply()
		if err := <-sent; err != nil {
			t.Fatal(err)
		}
		if len(frames) != 1 || frames[0].kind != dbwire.KindOK || frames[0].flags != 1 || len(frames[0].body) == 0 {
			t.Errorf("got %+v, want one frame that has the reply of the commit", frames)
		}
	})
	if n := pulses.Load(); n != 0 {
		t.Errorf("%d pulses were written", n)
	}
	if err := r.verify(); err != nil { // the connections go on
		t.Error(err)
	}
	w.mustSnapshot()
}

// TestPulses: a request that takes longer than the interval writes a pulse while the engine moves, and what it answers is
// what it answers without.
func TestPulses(t *testing.T) {
	t.Run("Verify", func(t *testing.T) {
		p := setupPulse(t, nil)
		r := p.connect(dbwire.RoleReader)
		sent := r.send(dbwire.KindVerify, nil)
		frames := r.rawReply()
		if err := <-sent; err != nil {
			t.Fatal(err)
		}
		last := frames[len(frames)-1]
		if last.kind != dbwire.KindOK || len(last.body) != 0 {
			t.Errorf("the last frame is %+v, want the empty end of an OK", last)
		}
		if n := pulsesIn(frames); n == 0 || n != len(frames)-1 || int64(n) != p.pulses.Load() {
			t.Errorf("%d pulses in %d frames, and the server wrote %d", n, len(frames), p.pulses.Load())
		}
		if err := r.verify(); err != nil { // the connection goes on
			t.Error(err)
		}
	})

	t.Run("Events", func(t *testing.T) {
		p := setupPulse(t, nil)
		p.ctl.setDelay(0)
		want := p.dump().Events
		p.ctl.setDelay(slowRead)
		r := p.connect(dbwire.RoleReader)
		r.mustSnapshot()
		events, err := r.events()
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, ev := range events {
			got = append(got, fmt.Sprintf("%d@%d:%q", ev.Number, ev.Seq, ev.Data))
		}
		if !slices.Equal(got, want) {
			t.Errorf("the events are\n%q\nwant\n%q", got, want)
		}
		if p.pulses.Load() == 0 {
			t.Error("no pulse was written")
		}
	})

	t.Run("Backup", func(t *testing.T) {
		p := setupPulse(t, nil)
		p.ctl.setDelay(0)
		want := p.dump()
		p.ctl.setDelay(slowRead)
		r := p.connect(dbwire.RoleReader)
		copied := simfs.New("copy")
		reply, err := r.backup(copied)
		if err != nil {
			t.Fatal(err)
		}
		if p.pulses.Load() == 0 {
			t.Error("no pulse was written")
		}
		p.ctl.setDelay(0)
		checkCopy(t, p.fixture, reply, copied, &want)
	})

	// The pulses of a Backup are before the entry of its position: a pulse after it would cut a frame short, which is
	// full unless it is the last, and the engine moves while the files are sent.
	t.Run("Backup has no pulse after its position", func(t *testing.T) {
		p := setupPulse(t, nil)
		r := p.connect(dbwire.RoleReader)
		sent := r.send(dbwire.KindBackup, nil)
		frames := r.rawReply()
		if err := <-sent; err != nil {
			t.Fatal(err)
		}
		first := slices.IndexFunc(frames, func(fr frame) bool { return len(fr.body) > 0 })
		if first < 1 || pulsesIn(frames[:first+1]) != first || int64(first) != p.pulses.Load() {
			t.Fatalf("the first frame with a body is %d, with %d pulses before it, and the server wrote %d", first,
				pulsesIn(frames[:first+1]), p.pulses.Load())
		}
		for i, fr := range frames[first : len(frames)-1] {
			if len(fr.body) != p.opts.Limits.Frame {
				t.Errorf("frame %d after the position has %d bytes, want %d: a pulse cut it short", first+i, len(fr.body),
					p.opts.Limits.Frame)
			}
		}
	})

	// A Commit waits for the Verify of another connection, which holds the engine, and a Backup waits for the Commit,
	// which holds the gate: the engine moves, and each of them writes pulses while it waits.
	t.Run("Commit and Backup behind a Verify", func(t *testing.T) {
		var armed atomic.Bool
		entered := make(chan struct{}, 1)
		p := setupPulse(t, func(o *Options) {
			o.Hook = func(point string) error {
				if point == "commit:gate" && armed.Load() {
					entered <- struct{}{}
				}
				return nil
			}
		})
		w, r, b := p.connect(dbwire.RoleWriter), p.connect(dbwire.RoleReader), p.connect(dbwire.RoleReader)
		seq := w.mustSnapshot()
		reads := p.ctl.reads.Load()
		armed.Store(true)
		verified := make(chan error, 1)
		go func() { verified <- r.verify() }()
		eventually(t, "the Verify to read", func() bool { return p.ctl.reads.Load() > reads })
		commit := w.replyAsync(dbwire.KindCommit, commitBody(seq, put(2, "z", "1"), event("z")))
		await(t, entered, "the commit to reach its gate")
		backup := b.replyAsync(dbwire.KindBackup, nil)

		if err := <-verified; err != nil {
			t.Errorf("Verify: %v", err)
		}
		for _, tc := range []struct {
			name string
			ch   <-chan replyOf
		}{{"Commit", commit}, {"Backup", backup}} {
			res := <-tc.ch
			if res.err != nil {
				t.Errorf("%s: %v", tc.name, res.err)
				continue
			}
			if last := res.frames[len(res.frames)-1]; last.kind != dbwire.KindOK {
				t.Errorf("%s: the last frame is a %v", tc.name, last.kind)
			}
			if n := pulsesIn(res.frames); n == 0 {
				t.Errorf("%s: no pulse in %d frames", tc.name, len(res.frames))
			}
		}
	})
}

// checkStuck waits until a call of the engine has blocked, and then checks that no pulse is written while the engine makes
// no progress: after the pulse that is due for the call that returned last, and the stretch of 20 intervals.
func (p *pulseFixture) checkStuck(t *testing.T, blocked <-chan struct{}) {
	t.Helper()
	select {
	case <-blocked:
	case <-time.After(waitLimit):
		t.Fatal("the engine did not block")
	}
	last, since := p.pulses.Load(), time.Now()
	eventually(t, "the pulses to stop", func() bool {
		if now := p.pulses.Load(); now != last {
			last, since = now, time.Now()
		}
		return time.Since(since) > 5*p.pulse
	})
	time.Sleep(20 * p.pulse)
	if now := p.pulses.Load(); now != last {
		t.Errorf("%d pulses were written while the engine made no progress", now-last)
	}
}

// TestPulseNotWhenStuck: a request whose engine is stuck in a call that does not return, and one that has called no engine
// at all, write no pulse once the engine has stopped moving; the reply of each ends when they are let go.
func TestPulseNotWhenStuck(t *testing.T) {
	t.Run("Verify", func(t *testing.T) {
		p := setupPulse(t, nil)
		r := p.connect(dbwire.RoleReader)
		p.quiet()
		blocked, release := p.ctl.block("read", 4)
		done := make(chan error, 1)
		go func() { done <- r.verify() }()
		p.checkStuck(t, blocked)
		if p.pulses.Load() == 0 {
			t.Error("no pulse was written before the engine was stuck")
		}
		release()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})

	t.Run("Events", func(t *testing.T) {
		p := setupPulse(t, nil)
		r := p.connect(dbwire.RoleReader)
		r.mustSnapshot()
		p.quiet()
		blocked, release := p.ctl.block("read", 2)
		done := make(chan error, 1)
		go func() {
			_, err := r.events()
			done <- err
		}()
		p.checkStuck(t, blocked)
		if p.pulses.Load() == 0 {
			t.Error("no pulse was written before the engine was stuck")
		}
		release()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})

	t.Run("Backup before its position", func(t *testing.T) {
		var gate atomic.Int64
		p := setupPulse(t, func(o *Options) {
			o.Hook = func(point string) error {
				if point == "backup:gate" {
					gate.Add(1)
				}
				return nil
			}
		})
		r := p.connect(dbwire.RoleReader)
		p.quiet()
		blocked, release := p.ctl.block("read", 2)
		done := make(chan error, 1)
		go func() {
			_, err := r.backup(nil)
			done <- err
		}()
		p.checkStuck(t, blocked)
		if p.pulses.Load() == 0 {
			t.Error("no pulse was written before the engine was stuck")
		}
		if gate.Load() != 0 {
			t.Error("the Backup knew its position")
		}
		release()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})

	t.Run("Commit in a sync", func(t *testing.T) {
		p := setupPulse(t, nil)
		w := p.connect(dbwire.RoleWriter)
		seq := w.mustSnapshot()
		p.quiet()
		blocked, release := p.ctl.block("sync", 0)
		done := make(chan error, 1)
		go func() {
			_, _, err := w.commit(seq, put(2, "z", "1"), event("z"))
			done <- err
		}()
		p.checkStuck(t, blocked)
		release()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})

	// The gate of the commit and that of the backup are hooks: no call of the engine is in progress, and the engine has not
	// moved since the request began, so that not a pulse is written.
	for _, point := range []string{"commit:gate", "backup:gate"} {
		t.Run("hook "+point, func(t *testing.T) {
			var armed atomic.Bool
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			p := setupPulse(t, func(o *Options) {
				o.Hook = func(at string) error {
					if at == point && armed.Load() {
						entered <- struct{}{}
						<-release
					}
					return nil
				}
			})
			t.Cleanup(unblock) // before the server is closed: a hook does not see the context
			p.ctl.setDelay(0)
			var send func() <-chan replyOf
			if point == "commit:gate" {
				w := p.connect(dbwire.RoleWriter)
				seq := w.mustSnapshot()
				send = func() <-chan replyOf {
					return w.replyAsync(dbwire.KindCommit, commitBody(seq, put(2, "z", "1"), event("z")))
				}
			} else {
				r := p.connect(dbwire.RoleReader)
				send = func() <-chan replyOf { return r.replyAsync(dbwire.KindBackup, nil) }
			}
			p.quiet()
			armed.Store(true)
			done := send()
			await(t, entered, "the request to reach its gate")
			p.checkStuck(t, closedChan)
			if point == "commit:gate" && p.pulses.Load() != 0 {
				t.Errorf("%d pulses were written before any call of the engine", p.pulses.Load())
			}
			unblock()
			if res := <-done; res.err != nil {
				t.Error(res.err)
			}
		})
	}
}

// await receives from ch, and fails the test after waitLimit.
func await(t testing.TB, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(waitLimit):
		t.Fatalf("gave up waiting for %s", what)
	}
}

var closedChan = func() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}()

// TestPulseThenRefusal: a request that has pulsed is refused with the Error frame after the frames of its OK reply, and the
// connection goes on; a refusal before a pulse is the same one frame as without pulses.
func TestPulseThenRefusal(t *testing.T) {
	t.Run("Verify of a damaged store", func(t *testing.T) {
		p := setupPulse(t, nil)
		file, err := p.fs.OpenFile(objectPath(strings.Repeat("a", 600)), os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		info, _ := file.Stat()
		if _, err := file.WriteAt([]byte{'X'}, info.Size()-1); err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
		r := p.connect(dbwire.RoleReader)
		sent := r.send(dbwire.KindVerify, nil)
		frames := r.rawReply()
		if err := <-sent; err != nil {
			t.Fatal(err)
		}
		last := frames[len(frames)-1]
		if last.kind != dbwire.KindError || last.code() != dbwire.CodeFailed || pulsesIn(frames) == 0 || pulsesIn(frames) != len(frames)-1 {
			t.Errorf("the reply is %d frames with %d pulses, and ends with %+v", len(frames), pulsesIn(frames), last)
		}
		r.mustSnapshot() // the connection goes on
		p.ctl.setDelay(0)
		wantCode(t, r.verify(), dbwire.CodeFailed)
	})

	t.Run("Commit that is not at the last sequence number", func(t *testing.T) {
		var armed atomic.Bool
		entered := make(chan struct{}, 1)
		p := setupPulse(t, func(o *Options) {
			o.Hook = func(point string) error {
				if point == "commit:gate" && armed.Load() {
					entered <- struct{}{}
				}
				return nil
			}
		})
		p.ctl.setDelay(0)
		before := p.dump()
		p.ctl.setDelay(slowRead)
		w, r := p.connect(dbwire.RoleWriter), p.connect(dbwire.RoleReader)
		seq := w.mustSnapshot()
		reads := p.ctl.reads.Load()
		armed.Store(true)
		verified := make(chan error, 1)
		go func() { verified <- r.verify() }()
		eventually(t, "the Verify to read", func() bool { return p.ctl.reads.Load() > reads })
		commit := w.replyAsync(dbwire.KindCommit, commitBody(seq+5, put(2, "z", "1"), event("z")))
		await(t, entered, "the commit to reach its gate")
		if err := <-verified; err != nil {
			t.Error(err)
		}
		res := <-commit
		if res.err != nil {
			t.Fatal(res.err)
		}
		last := res.frames[len(res.frames)-1]
		if last.kind != dbwire.KindError || last.code() != dbwire.CodeSeq || pulsesIn(res.frames) == 0 {
			t.Errorf("the reply is %d frames with %d pulses, and ends with %+v", len(res.frames), pulsesIn(res.frames), last)
		}
		p.ctl.setDelay(0)
		p.unchanged(before)
		w.mustSnapshot() // the connection goes on
	})

	// A request that is refused before it writes a pulse has the one frame of the reply of version 2.
	t.Run("refused at once", func(t *testing.T) {
		p := setupPulse(t, nil)
		w := p.connect(dbwire.RoleWriter)
		seq := w.mustSnapshot()
		sent := w.send(dbwire.KindCommit, commitBody(seq, put(2, "z", "1"))) // rows without an event
		frames := w.rawReply()
		if err := <-sent; err != nil {
			t.Fatal(err)
		}
		if len(frames) != 1 || frames[0].kind != dbwire.KindError || frames[0].code() != dbwire.CodeRule {
			t.Errorf("got %+v, want one frame with the code Rule", frames)
		}
	})
}

// TestNoPulseAfterReply: a pulse is written into the reply that a request is answering, and never after the last frame of
// it, which would be taken for the start of the reply to the next request: a Get has no pulse, so that none of the Gets
// that follow Verifies, Events, or Backups that are refused, whose engine moves all the time, may have another frame than
// its own.
func TestNoPulseAfterReply(t *testing.T) {
	// repeat sends the request n times on a connection with a snapshot, each followed by a Get. It checks that ends
	// holds for the last frame of the reply of the request, and that the reply to the Get is one frame, which is its own.
	repeat := func(t *testing.T, p *pulseFixture, n int, kind dbwire.Kind, ends func(fr frame) bool) {
		t.Helper()
		p.fill()
		p.ctl.setDelay(200 * time.Microsecond)
		r := p.connect(dbwire.RoleReader)
		r.mustSnapshot()
		for i := range n {
			sent := r.send(kind, nil)
			frames := r.rawReply()
			if err := <-sent; err != nil {
				t.Fatal(err)
			}
			if last := frames[len(frames)-1]; !ends(last) {
				t.Fatalf("%v %d: the last frame is %+v", kind, i, last)
			}
			sent = r.send(dbwire.KindGet, func(e *dbwire.Enc) { e.GetRequest(2, []byte("k0")) })
			frames = r.rawReply()
			if err := <-sent; err != nil {
				t.Fatal(err)
			}
			if len(frames) != 1 || frames[0].kind != dbwire.KindOK || frames[0].flags != 1 || len(frames[0].body) == 0 {
				t.Fatalf("Get %d after a %v: %d frames, the first %+v", i, kind, len(frames), frames[0])
			}
		}
		if p.pulses.Load() == 0 {
			t.Error("no pulse was written")
		}
	}

	t.Run("Verify", func(t *testing.T) {
		repeat(t, newPulseFixture(t, time.Millisecond, nil), 200, dbwire.KindVerify, func(fr frame) bool {
			return fr.kind == dbwire.KindOK && fr.flags == 1 && len(fr.body) == 0
		})
	})

	t.Run("Events", func(t *testing.T) {
		repeat(t, newPulseFixture(t, time.Millisecond, nil), 150, dbwire.KindEvents, func(fr frame) bool {
			return fr.kind == dbwire.KindOK && fr.flags == 1
		})
	})

	// A Backup that is refused after its pulses, at its gate: its pulser ends before the Error frame.
	t.Run("Backup that is refused", func(t *testing.T) {
		p := newPulseFixture(t, time.Millisecond, func(o *Options) {
			o.Hook = func(point string) error {
				if point == "backup:gate" {
					return errors.New("refused at the gate")
				}
				return nil
			}
		})
		repeat(t, p, 150, dbwire.KindBackup, func(fr frame) bool { return fr.kind == dbwire.KindError })
	})
}

// abandon sends a request, which is long, on a connection of its own, waits until the engine is stuck in the read that
// comes after skip others, which the client does not wait for any more, and until a write of the daemon to it has failed:
// a pulse, as nothing else is written while the reply is open and the engine does not move. It returns what lets the engine
// go on.
func (p *pulseFixture) abandon(c *client, wc *countConn, kind dbwire.Kind, body func(e *dbwire.Enc), skip int) (release func()) {
	p.t.Helper()
	blocked, release := p.ctl.block("read", skip)
	if err := <-c.send(kind, body); err != nil {
		p.t.Fatal(err)
	}
	await(p.t, blocked, "the engine to be stuck")
	_ = c.conn.Close()
	eventually(p.t, "a pulse to fail", func() bool { return wc.failed.Load() > 0 })
	eventually(p.t, "the pulse that failed to return", func() bool { return pulsers() == 0 }) // after its abort
	return release
}

// TestPulseWriteFails: when a pulse cannot be written, the connection is broken: the Verify, the Events and the Backup
// before its position end at the next record or file that the engine reads, and so let go of the engine and the gate,
// instead of reading what nobody will read the answer of. A Commit is not stopped: its Update runs to its end, and it is made.
func TestPulseWriteFails(t *testing.T) {
	// stuck is the reads that return before the engine is stuck in the next one: after it lets go, the request may make one
	// more read at most, and not the rest.
	const stuck = 2

	t.Run("Verify", func(t *testing.T) {
		p := setupPulse(t, nil)
		p.ctl.setDelay(0)
		r0 := p.connect(dbwire.RoleReader)
		before := p.ctl.reads.Load()
		if err := r0.verify(); err != nil {
			t.Fatal(err)
		}
		full := p.ctl.reads.Load() - before
		_ = r0.conn.Close()
		p.waitSessions(0)

		r, wc := p.connectCounted(dbwire.RoleReader)
		before = p.ctl.reads.Load()
		release := p.abandon(r, wc, dbwire.KindVerify, nil, stuck)
		release()
		p.waitSessions(0)
		if got := p.ctl.reads.Load() - before; got > stuck+2 {
			t.Errorf("the Verify made %d reads after its client was gone; all of them are %d", got, full)
		}
	})

	t.Run("Events", func(t *testing.T) {
		p := setupPulse(t, nil)
		p.ctl.setDelay(0)
		r0 := p.connect(dbwire.RoleReader)
		r0.mustSnapshot()
		before := p.ctl.reads.Load()
		if _, err := r0.events(); err != nil {
			t.Fatal(err)
		}
		full := p.ctl.reads.Load() - before
		_ = r0.conn.Close()
		p.waitSessions(0)

		r, wc := p.connectCounted(dbwire.RoleReader)
		r.mustSnapshot()
		before = p.ctl.reads.Load()
		release := p.abandon(r, wc, dbwire.KindEvents, nil, 1)
		release()
		p.waitSessions(0)
		if got := p.ctl.reads.Load() - before; got > 1+2 { // the Events has fewer reads: it is stuck in the second
			t.Errorf("the Events made %d reads after its client was gone; all of them are %d", got, full)
		}
	})

	// The engine opens its reader without a context: the Backup ends when that has returned, at its position.
	t.Run("Backup before its position", func(t *testing.T) {
		var sending atomic.Int64
		p := setupPulse(t, func(o *Options) {
			o.Hook = func(point string) error {
				if point == "backup:sending" {
					sending.Add(1)
				}
				return nil
			}
		})
		p.ctl.setDelay(0)
		r0 := p.connect(dbwire.RoleReader)
		if _, err := r0.backup(nil); err != nil {
			t.Fatal(err)
		}
		if sending.Load() != 1 {
			t.Fatalf("the Backup began to send %d times, want 1", sending.Load())
		}
		_ = r0.conn.Close()
		p.waitSessions(0)

		r, wc := p.connectCounted(dbwire.RoleReader)
		release := p.abandon(r, wc, dbwire.KindBackup, nil, stuck)
		release()
		p.waitSessions(0)
		if sending.Load() != 1 {
			t.Error("the Backup went on to send, with its client gone")
		}
		// The gate and the engine are let go: a Commit goes through.
		if _, _, err := p.connect(dbwire.RoleWriter).tryCommit(put(2, "z", "1")); err != nil {
			t.Error(err)
		}
	})

	// The Commit waits for the Verify of another connection, which is stuck: its pulse is written when the Verify has
	// moved, and fails. The Verify goes on, and the Commit is made after it.
	t.Run("Commit", func(t *testing.T) {
		entered := make(chan struct{}, 1)
		var armed atomic.Bool
		p := setupPulse(t, func(o *Options) {
			o.Hook = func(point string) error {
				if point == "commit:gate" && armed.Load() {
					entered <- struct{}{}
				}
				return nil
			}
		})
		p.ctl.setDelay(0)
		w, wc := p.connectCounted(dbwire.RoleWriter)
		r := p.connect(dbwire.RoleReader)
		seq := w.mustSnapshot()
		stuck1, go1 := p.ctl.block("read", stuck)
		verified := make(chan error, 1)
		go func() { verified <- r.verify() }()
		await(t, stuck1, "the Verify to be stuck")

		armed.Store(true)
		if err := <-w.send(dbwire.KindCommit, commitBody(seq, put(2, "z", "1"), event("z"))); err != nil {
			t.Fatal(err)
		}
		await(t, entered, "the commit to reach its gate")
		_ = w.conn.Close()
		stuck2, go2 := p.ctl.block("read", 1)
		go1()
		await(t, stuck2, "the Verify to be stuck again")
		eventually(t, "a pulse of the Commit to fail", func() bool { return wc.failed.Load() > 0 })
		go2()
		if err := <-verified; err != nil {
			t.Errorf("Verify: %v", err)
		}
		p.waitSessions(1) // the session of the Commit has ended: its client is gone, and it was answered

		var again *client
		eventually(t, "the writer to be let in again", func() bool {
			c := p.dial(dbwire.RoleWriter)
			if err := c.sayHello(); err != nil {
				_ = c.conn.Close()
				return false
			}
			again = c
			return true
		})
		if again.hello.Seq != seq+1 {
			t.Errorf("the last sequence number is %d, want %d: the Commit was not made", again.hello.Seq, seq+1)
		}
	})

	t.Run("the pulser", func(t *testing.T) {
		f := newFixture(t, func(o *Options) { o.Pulse = time.Millisecond })
		server, peer := net.Pipe()
		t.Cleanup(func() { _ = server.Close(); _ = peer.Close() })
		c := &session{s: f.srv, wr: dbwire.NewWriter(server, 4096)}
		c.wr.Begin(dbwire.KindOK)
		var aborted atomic.Int64
		p := c.pulse(func() { aborted.Add(1) })
		_ = peer.Close()
		f.srv.moved.Add(1)
		eventually(t, "the pulse that fails to abort", func() bool { return aborted.Load() > 0 })
		f.srv.moved.Add(1)
		time.Sleep(20 * time.Millisecond)
		p.end()
		if n := aborted.Load(); n != 1 {
			t.Errorf("abort was called %d times, want 1", n)
		}
	})
}

// pulsers counts the goroutines that are writing a pulse: the timer of a request starts one only when it fires, and a
// goroutine that the runtime has made but that has not run yet has the method value of fire as its function.
func pulsers() int {
	return len(stacksWith("dbserver.(*pulser).fire"))
}

// TestPulseEnds: no goroutine of the pulses is left when its request has ended or its session has: not when the server is
// closed or shut down while a pulse is being written, and not when the peer is gone; and no timer of them fires after.
func TestPulseEnds(t *testing.T) {
	// stuckPulse starts a Verify, and does not read its reply: the first pulse is a write that waits for the peer.
	stuckPulse := func(t *testing.T) (*pulseFixture, *client) {
		p := setupPulse(t, nil)
		r := p.connect(dbwire.RoleReader)
		_ = r.send(dbwire.KindVerify, nil)
		eventually(t, "a pulse to wait for the peer", func() bool {
			return blockedIn("dbserver.(*pulser).fire", "net.(*pipe).Write")
		})
		if n := pulsers(); n != 1 {
			t.Fatalf("%d goroutines of pulses, want 1", n)
		}
		return p, r
	}

	t.Run("Close", func(t *testing.T) {
		p, _ := stuckPulse(t)
		_ = p.srv.Close()
		eventually(t, "the goroutine of the pulse to return after Close", func() bool { return pulsers() == 0 })
	})

	t.Run("Shutdown", func(t *testing.T) {
		p, _ := stuckPulse(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		p.srv.Shutdown(ctx)
		p.waitSessions(0)
		eventually(t, "the goroutine of the pulse to return after Shutdown", func() bool { return pulsers() == 0 })
	})

	t.Run("the peer is gone", func(t *testing.T) {
		p, r := stuckPulse(t)
		_ = r.conn.Close()
		p.waitSessions(0)
		eventually(t, "the goroutine of the pulse to return after the session has ended", func() bool { return pulsers() == 0 })
	})

	t.Run("the request has ended", func(t *testing.T) {
		p := setupPulse(t, nil)
		r := p.connect(dbwire.RoleReader)
		if err := r.verify(); err != nil {
			t.Fatal(err)
		}
		eventually(t, "the goroutine of the pulse to return after the reply", func() bool { return pulsers() == 0 })
	})
}

func TestPulseOption(t *testing.T) {
	fsys := simfs.New("data")
	if err := Create(fsys, "data", testOptions()); err != nil {
		t.Fatal(err)
	}
	o := testOptions()
	o.Pulse = -time.Second
	if srv, err := Open(fsys, "data", o); err == nil {
		_ = srv.Close()
		t.Fatal("Open() accepted a negative pulse interval")
	} else if got, want := err.Error(), "the pulse interval is negative (-1s)"; got != want {
		t.Errorf("Open() = %q, want %q", got, want)
	}
	for _, tc := range []struct{ set, want time.Duration }{{0, defaultPulse}, {time.Second, time.Second}} {
		o.Pulse = tc.set
		srv, err := Open(fsys, "data", o)
		if err != nil {
			t.Fatal(err)
		}
		if srv.pulse != tc.want {
			t.Errorf("the pulse interval of Pulse %v is %v, want %v", tc.set, srv.pulse, tc.want)
		}
		if err := srv.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if defaultPulse != 5*time.Second {
		t.Errorf("the default pulse interval is %v, want 5s", defaultPulse)
	}
}

// TestProgressFS: each call of the file system and of its files is counted once, when it has returned, whether it has failed
// or not, and a Sub and a file that is opened are counted as the file system is; the calls that are not asked for are not.
func TestProgressFS(t *testing.T) {
	var moved atomic.Uint64
	root := &progressFS{FS: simfs.New("data"), moved: &moved}
	count := func(name string, want uint64, call func()) {
		t.Helper()
		before := moved.Load()
		call()
		if got := moved.Load() - before; got != want {
			t.Errorf("%s was counted %d times, want %d", name, got, want)
		}
	}
	var sub vfs.FS
	count("Mkdir", 1, func() {
		if err := root.Mkdir("d", 0o700); err != nil {
			t.Fatal(err)
		}
	})
	count("Mkdir that fails", 1, func() {
		if err := root.Mkdir("d", 0o700); err == nil {
			t.Fatal("Mkdir() of a directory that exists succeeded")
		}
	})
	count("Sub", 1, func() {
		var err error
		if sub, err = root.Sub("d"); err != nil {
			t.Fatal(err)
		}
	})
	count("Sub that fails", 1, func() {
		if s, err := root.Sub("missing"); err == nil || s != nil {
			t.Fatalf("Sub() of a directory that is missing = %v, %v", s, err)
		}
	})
	count("Name", 0, func() { _ = sub.Name() })

	var file vfs.File
	count("OpenFile of the Sub", 1, func() {
		var err error
		if file, err = sub.OpenFile("f", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600); err != nil {
			t.Fatal(err)
		}
	})
	count("Open that fails", 1, func() {
		if f, err := sub.Open("missing"); err == nil || f != nil {
			t.Fatalf("Open() of a file that is missing = %v, %v", f, err)
		}
	})
	count("OpenFile that fails", 1, func() {
		if f, err := root.OpenFile("d/f", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600); err == nil || f != nil {
			t.Fatalf("OpenFile() of a file that exists = %v, %v", f, err)
		}
	})
	buf := make([]byte, 8)
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"Write", func() error { _, err := file.Write([]byte("0123456789")); return err }},
		{"WriteAt", func() error { _, err := file.WriteAt([]byte("ab"), 2); return err }},
		{"Sync", func() error { return file.Sync() }},
		{"Seek", func() error { _, err := file.Seek(0, io.SeekStart); return err }},
		{"Read", func() error { _, err := file.Read(buf); return err }},
		{"ReadAt", func() error { _, err := file.ReadAt(buf, 1); return err }},
		{"Stat", func() error { _, err := file.Stat(); return err }},
		{"Truncate", func() error { return file.Truncate(4) }},
		{"Close", func() error { return file.Close() }},
	} {
		count("File."+tc.name, 1, func() {
			if err := tc.call(); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
		})
	}
	count("File.Close that fails", 1, func() { _ = file.Close() })
	var dir vfs.File
	count("Open of a directory", 1, func() {
		var err error
		if dir, err = root.Open("d"); err != nil {
			t.Fatal(err)
		}
	})
	count("File.ReadDir", 1, func() {
		if _, err := dir.ReadDir(-1); err != nil {
			t.Fatal(err)
		}
	})
	_ = dir.Close()
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"Lstat", func() error { _, err := root.Lstat("d/f"); return err }},
		{"ReadDir", func() error { _, err := root.ReadDir("d"); return err }},
		{"SyncDir", func() error { return root.SyncDir("d") }},
		{"Lock", func() error {
			unlock, err := root.Lock("d/f", vfs.Shared)
			if err == nil {
				err = unlock()
			}
			return err
		}},
		{"Rename", func() error { return root.Rename("d/f", "d/g") }},
		{"Remove", func() error { return root.Remove("d/g") }},
	} {
		count("FS."+tc.name, 1, func() {
			if err := tc.call(); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
		})
	}
	count("Lstat that fails", 1, func() {
		if _, err := root.Lstat("d/g"); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Lstat() of a file that was removed: %v", err)
		}
	})

	// A call that has not returned is not counted: the engine has not moved until it has.
	t.Run("a call is counted after it has returned", func(t *testing.T) {
		ctl := &fsControl{}
		var moved atomic.Uint64
		root := &progressFS{FS: &ctlFS{FS: simfs.New("data"), c: ctl}, moved: &moved}
		f, err := root.OpenFile("f", os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		if _, err := f.Write([]byte("abcdef")); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name, op string
			call     func() error
		}{
			{"ReadAt", "read", func() error { _, err := f.ReadAt(make([]byte, 2), 1); return err }},
			{"Read", "read", func() error {
				if _, err := f.Read(make([]byte, 2)); err != nil && !errors.Is(err, io.EOF) { // the offset is at the end
					return err
				}
				return nil
			}},
			{"Write", "write", func() error { _, err := f.Write([]byte("x")); return err }},
			{"WriteAt", "write", func() error { _, err := f.WriteAt([]byte("x"), 0); return err }},
			{"Sync", "sync", func() error { return f.Sync() }},
		} {
			blocked, release := ctl.block(tc.op, 0)
			before := moved.Load()
			done := make(chan error, 1)
			go func() { done <- tc.call() }()
			await(t, blocked, tc.name+" to be called")
			time.Sleep(5 * time.Millisecond)
			if got := moved.Load() - before; got != 0 {
				t.Errorf("%s was counted %d times before it returned", tc.name, got)
			}
			release()
			if err := <-done; err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if got := moved.Load() - before; got != 1 {
				t.Errorf("%s was counted %d times after it returned, want 1", tc.name, got)
			}
		}
	})
}

// TestProgressOfServer: the engine runs on a file system that counts, and the originals, in files/, do not.
func TestProgressOfServer(t *testing.T) {
	f := newFixture(t, nil)
	w := f.connect(dbwire.RoleWriter)
	before := f.srv.moved.Load()
	w.mustCommit(put(2, "k", "v"))
	if f.srv.moved.Load() == before {
		t.Error("a commit made no call of the files of the engine that was counted")
	}
	last, since := f.srv.moved.Load(), time.Now()
	eventually(t, "the engine to be quiet", func() bool {
		if now := f.srv.moved.Load(); now != last {
			last, since = now, time.Now()
		}
		return time.Since(since) > 50*time.Millisecond
	})
	if _, err := w.putOriginal([]byte("an original")); err != nil {
		t.Fatal(err)
	}
	if now := f.srv.moved.Load(); now != last {
		t.Errorf("an original moved the count by %d", now-last)
	}
	if f.srv.logFS.(*progressFS).moved != f.srv.moved {
		t.Error("the file system of the engine does not count into the count of the server")
	}
}

// TestPulserEnd: end waits for a pulse that is being written, and a pulse that waited for the lock is not written after it.
func TestPulserEnd(t *testing.T) {
	setup := func(t *testing.T) (*fixture, *session, net.Conn, *pulser) {
		f := newFixture(t, func(o *Options) { o.Pulse = time.Millisecond })
		server, peer := net.Pipe()
		t.Cleanup(func() { _ = server.Close(); _ = peer.Close() })
		c := &session{s: f.srv, wr: dbwire.NewWriter(server, 4096)}
		c.wr.Begin(dbwire.KindOK)
		return f, c, peer, c.pulse(nil)
	}
	// silent fails the test unless nothing is written to the peer in a while.
	silent := func(t *testing.T, peer net.Conn) {
		t.Helper()
		_ = peer.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
		if n, err := peer.Read(make([]byte, dbwire.HeaderSize)); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("the peer read %d bytes (%v) after the pulser had ended", n, err)
		}
	}

	t.Run("waits for a pulse that it is writing", func(t *testing.T) {
		f, _, peer, p := setup(t)
		f.srv.moved.Add(1)
		eventually(t, "a pulse to wait for the peer", func() bool {
			return blockedIn("dbserver.(*pulser).fire", "net.(*pipe).Write")
		})
		ended := make(chan struct{})
		go func() {
			p.end()
			close(ended)
		}()
		select {
		case <-ended:
			t.Fatal("end returned while a pulse was being written")
		case <-time.After(50 * time.Millisecond):
		}
		var pulse [dbwire.HeaderSize]byte
		if _, err := io.ReadFull(peer, pulse[:]); err != nil {
			t.Fatal(err)
		}
		if want := [dbwire.HeaderSize]byte{byte(dbwire.KindOK)}; pulse != want {
			t.Errorf("the pulse is %x, want %x", pulse, want)
		}
		select {
		case <-ended:
		case <-time.After(waitLimit):
			t.Fatal("end did not return after the pulse was written")
		}
		silent(t, peer)
	})

	t.Run("stops the timer", func(t *testing.T) {
		f := newFixture(t, func(o *Options) { o.Pulse = time.Hour })
		server, peer := net.Pipe()
		t.Cleanup(func() { _ = server.Close(); _ = peer.Close() })
		c := &session{s: f.srv, wr: dbwire.NewWriter(server, 4096)}
		c.wr.Begin(dbwire.KindOK)
		p := c.pulse(nil)
		p.end()
		if p.t.Stop() {
			t.Error("the timer was still pending after end")
		}
	})

	t.Run("does not write a pulse that waited for the lock", func(t *testing.T) {
		f, c, peer, p := setup(t)
		c.pulseMu.Lock()
		f.srv.moved.Add(1)
		eventually(t, "the timer to wait for the lock", func() bool {
			return blockedIn("dbserver.(*pulser).fire", "Mutex).lockSlow")
		})
		p.stopped = true
		c.pulseMu.Unlock()
		eventually(t, "the timer to return", func() bool { return pulsers() == 0 })
		silent(t, peer)
		p.end()
	})
}

// TestPulseOwnProgress: a Verify, an Events and a Backup before its position, whose own call of the engine does not
// return, write no pulse while another connection makes the engine of the daemon move; the reply of each ends when they
// are let go.
func TestPulseOwnProgress(t *testing.T) {
	for _, tc := range []struct {
		name    string
		skip    int
		prepare func(p *pulseFixture) (send func() (wait func() error))
	}{
		{"Verify", 4, func(p *pulseFixture) func() func() error {
			r := p.connect(dbwire.RoleReader)
			return func() func() error {
				done := make(chan error, 1)
				go func() { done <- r.verify() }()
				return func() error { return <-done }
			}
		}},
		{"Events", 2, func(p *pulseFixture) func() func() error {
			r := p.connect(dbwire.RoleReader)
			r.mustSnapshot()
			return func() func() error {
				done := make(chan error, 1)
				go func() {
					_, err := r.events()
					done <- err
				}()
				return func() error { return <-done }
			}
		}},
		{"Backup before its position", 2, func(p *pulseFixture) func() func() error {
			r := p.connect(dbwire.RoleReader)
			return func() func() error {
				done := make(chan error, 1)
				go func() {
					_, err := r.backup(nil)
					done <- err
				}()
				return func() error { return <-done }
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := setupPulse(t, nil)
			send := tc.prepare(p)
			m := p.connect(dbwire.RoleReader)
			m.mustSnapshot()
			p.quiet()
			blocked, release := p.ctl.block("read", tc.skip)
			wait := send()
			await(t, blocked, "the engine to block")
			p.ctl.setDelay(0)
			stop := p.mover(m)
			defer stop()
			moved := p.srv.moved.Load()
			p.checkStuck(t, blocked)
			if got := p.srv.moved.Load() - moved; got < 20 {
				t.Errorf("the engine of the daemon moved %d times while the request was stuck, want at least 20", got)
			}
			if p.pulses.Load() == 0 {
				t.Error("no pulse was written before the engine was stuck")
			}
			stop()
			release()
			if err := wait(); err != nil {
				t.Error(err)
			}
		})
	}
}

// TestPulseWhileWaiting: a Verify stuck in the engine holds the turn of the daemon, and the requests behind it, which have no
// call of the engine of their own to wait for, write pulses while the daemon moves: a Verify and a Commit (inside the gate)
// that wait for the turn, and a Backup that waits for the gate. The Verify that is stuck writes none.
func TestPulseWhileWaiting(t *testing.T) {
	var armed atomic.Bool
	entered := make(chan struct{}, 1)
	p := setupPulse(t, func(o *Options) {
		o.Limits.Readers = 8
		o.Hook = func(point string) error {
			if point == "commit:gate" && armed.Load() {
				entered <- struct{}{}
			}
			return nil
		}
	})
	w, ra, rb, rd, m := p.connect(dbwire.RoleWriter), p.connect(dbwire.RoleReader), p.connect(dbwire.RoleReader),
		p.connect(dbwire.RoleReader), p.connect(dbwire.RoleReader)
	seq := w.mustSnapshot()
	m.mustSnapshot()
	p.quiet()
	blocked, release := p.ctl.block("read", 4)

	a := ra.replyCounted(dbwire.KindVerify, nil)
	await(t, blocked, "the Verify to block")
	p.ctl.setDelay(0)
	stuck := p.settled("the pulses of the Verify to stop", &a.pulses)
	b := rb.replyCounted(dbwire.KindVerify, nil)
	armed.Store(true)
	c := w.replyCounted(dbwire.KindCommit, commitBody(seq, put(2, "z", "1"), event("z")))
	await(t, entered, "the commit to reach its gate")
	d := rd.replyCounted(dbwire.KindBackup, nil) // after the Commit has the gate: it waits for the Commit, and not the other way

	b0, c0, d0 := b.pulses.Load(), c.pulses.Load(), d.pulses.Load()
	stop := p.mover(m)
	defer stop()
	eventually(t, "the requests that wait to pulse", func() bool {
		return b.pulses.Load() >= b0+3 && c.pulses.Load() >= c0+3 && d.pulses.Load() >= d0+3
	})
	if got := a.pulses.Load(); got != stuck {
		t.Errorf("the Verify that is stuck wrote %d pulses while the daemon moved", got-stuck)
	}
	stop()
	release()

	for _, tc := range []struct {
		name string
		r    *counted
	}{{"Verify", a}, {"Verify that waited", b}, {"Commit", c}, {"Backup", d}} {
		res := <-tc.r.done
		if res.err != nil {
			t.Errorf("%s: %v", tc.name, res.err)
			continue
		}
		if last := res.frames[len(res.frames)-1]; last.kind != dbwire.KindOK {
			t.Errorf("%s: the last frame is a %v", tc.name, last.kind)
		}
	}
	if _, _, err := w.tryCommit(put(2, "y", "1")); err != nil {
		t.Errorf("a Commit after them: %v", err)
	}
}

// stepPulses lets the blocked call of op go and blocks the next one, n times, and each time waits until r writes a pulse.
// While the engine is blocked nothing else moves it, so the pulse is for the call that went. It returns the release of the
// call that is blocked at the end.
func (p *pulseFixture) stepPulses(t *testing.T, op string, release func(), r *counted, n int) func() {
	t.Helper()
	for i := range n {
		base := p.settled("the pulses to stop", &r.pulses)
		blocked, next := p.ctl.block(op, 0)
		release()
		await(t, blocked, fmt.Sprintf("the call %d of the engine after the first to block", i+1))
		eventually(t, fmt.Sprintf("a pulse for the call %d of the engine", i+1), func() bool { return r.pulses.Load() > base })
		release = next
	}
	return release
}

// checkEnded checks that each reply ended with an OK.
func checkEnded(t *testing.T, replies map[string]*counted) {
	t.Helper()
	for name, r := range replies {
		res := <-r.done
		if res.err != nil {
			t.Errorf("%s: %v", name, res.err)
			continue
		}
		if last := res.frames[len(res.frames)-1]; last.kind != dbwire.KindOK {
			t.Errorf("%s: the last frame is a %v", name, last.kind)
		}
	}
}

// TestPulseBehindUpdate: a Verify that comes while the Update of a Commit runs waits for the turn, which the Update holds,
// and writes a pulse for each call of the files of the engine that the Update makes, until it ends: the engine would make
// the Verify wait for the Update with no call of its own to count.
func TestPulseBehindUpdate(t *testing.T) {
	p := setupPulse(t, nil)
	p.ctl.setDelay(0)
	w, r := p.connect(dbwire.RoleWriter), p.connect(dbwire.RoleReader)
	seq := w.mustSnapshot()
	for i := range 4 {
		if _, err := w.putObject(strings.Repeat(fmt.Sprint(i), 600)); err != nil {
			t.Fatal(err)
		}
	}
	p.quiet()
	blocked, release := p.ctl.block("write", 0)
	c := w.replyCounted(dbwire.KindCommit, commitBody(seq, put(2, "z", "1"), event("z")))
	await(t, blocked, "the Update to block in a write")
	v := r.replyCounted(dbwire.KindVerify, nil)
	eventually(t, "the Verify to wait", func() bool { return blockedIn("dbserver.(*session).verifyRequest", "[select") })
	p.stepPulses(t, "write", release, v, 3)()
	checkEnded(t, map[string]*counted{"Commit": c, "Verify": v})
	if err := r.verify(); err != nil {
		t.Errorf("a Verify after them: %v", err)
	}
}

// TestPulseBehindBackupReader: a Commit that waits at the gate while the reader of a Backup opens writes a pulse for each
// call of the files of the engine that the reader makes: the reader counts for the server too, and not only for its request.
func TestPulseBehindBackupReader(t *testing.T) {
	p := setupPulse(t, nil)
	p.ctl.setDelay(0)
	w, b := p.connect(dbwire.RoleWriter), p.connect(dbwire.RoleReader)
	seq := w.mustSnapshot()
	p.quiet()
	blocked, release := p.ctl.block("read", 0)
	bk := b.replyCounted(dbwire.KindBackup, nil)
	await(t, blocked, "the reader of the Backup to block")
	c := w.replyCounted(dbwire.KindCommit, commitBody(seq, put(2, "z", "1"), event("z")))
	eventually(t, "the Commit to wait for the gate", func() bool {
		return blockedIn("dbserver.(*session).update", "Mutex).lockSlow")
	})
	p.stepPulses(t, "read", release, c, 3)()
	checkEnded(t, map[string]*counted{"Backup": bk, "Commit": c})
}

// TestVerifyTurn: a Verify that waits for the turn ends with its session when its pulse cannot be written, and leaves the
// turn to the others.
func TestVerifyTurn(t *testing.T) {
	p := setupPulse(t, nil)
	ra, m := p.connect(dbwire.RoleReader), p.connect(dbwire.RoleReader)
	rb, wc := p.connectCounted(dbwire.RoleReader)
	m.mustSnapshot()
	p.quiet()
	blocked, release := p.ctl.block("read", 4)
	a := ra.replyCounted(dbwire.KindVerify, nil)
	await(t, blocked, "the Verify to block")
	p.ctl.setDelay(0)
	b := rb.replyCounted(dbwire.KindVerify, nil)
	stop := p.mover(m)
	defer stop()
	eventually(t, "the Verify that waits to pulse", func() bool { return b.pulses.Load() > 0 })
	_ = rb.conn.Close()
	eventually(t, "a pulse to fail", func() bool { return wc.failed.Load() > 0 })
	eventually(t, "the pulse that failed to return", func() bool { return pulsers() == 0 })
	p.waitSessions(2) // the Verify that is stuck, and the one that moves the daemon
	stop()
	release()
	if res := <-a.done; res.err != nil {
		t.Error(res.err)
	}
	if err := p.connect(dbwire.RoleReader).verify(); err != nil {
		t.Errorf("a Verify after them: %v", err)
	}
}

// TestCommitTurn: a Commit that waits for the turn ends with the Close of the server while the Verify that has the turn is
// stuck, and does not wait for the turn first.
func TestCommitTurn(t *testing.T) {
	var armed atomic.Bool
	entered := make(chan struct{}, 1)
	p := setupPulse(t, func(o *Options) {
		o.Hook = func(point string) error {
			if point == "commit:gate" && armed.Load() {
				entered <- struct{}{}
			}
			return nil
		}
	})
	w, r := p.connect(dbwire.RoleWriter), p.connect(dbwire.RoleReader)
	seq := w.mustSnapshot()
	p.quiet()
	blocked, release := p.ctl.block("read", 4)
	v := r.replyCounted(dbwire.KindVerify, nil)
	await(t, blocked, "the Verify to block")
	armed.Store(true)
	c := w.replyCounted(dbwire.KindCommit, commitBody(seq, put(2, "z", "1"), event("z")))
	await(t, entered, "the Commit to reach its gate")
	closed := make(chan struct{})
	go func() {
		_ = p.srv.Close()
		close(closed)
	}()
	p.waitSessions(1) // the Verify that is stuck
	release()
	<-closed
	if res := <-c.done; res.err == nil && res.frames[len(res.frames)-1].kind == dbwire.KindOK {
		t.Error("the Commit that the Close ended answered OK")
	}
	<-v.done
}

// TestPulserOwn: a pulser compares the count of the server until useOwn, and its own count after it, from where that is.
func TestPulserOwn(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Pulse = time.Millisecond })
	server, peer := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = peer.Close() })
	c := &session{s: f.srv, wr: dbwire.NewWriter(server, 4096)}
	c.wr.Begin(dbwire.KindOK)
	// pulse reads the pulse that the peer is sent, or fails the test if none comes.
	pulse := func(t *testing.T) {
		t.Helper()
		_ = peer.SetReadDeadline(time.Now().Add(waitLimit))
		var got [dbwire.HeaderSize]byte
		if _, err := io.ReadFull(peer, got[:]); err != nil {
			t.Fatalf("waiting for a pulse: %v", err)
		}
		if want := [dbwire.HeaderSize]byte{byte(dbwire.KindOK)}; got != want {
			t.Fatalf("the pulse is %x, want %x", got, want)
		}
	}
	silent := func(t *testing.T) {
		t.Helper()
		_ = peer.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
		if n, err := peer.Read(make([]byte, dbwire.HeaderSize)); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("the peer read %d bytes (%v) while the count that the pulser compares did not move", n, err)
		}
	}

	f.srv.moved.Add(5)
	p := c.pulse(nil)
	f.srv.moved.Add(1)
	pulse(t)
	p.own.Add(3)
	p.useOwn()
	f.srv.moved.Add(1)
	silent(t)
	p.own.Add(1)
	pulse(t)
	f.srv.moved.Add(1)
	silent(t)
	p.end()
}

// TestProgressCtx: the engine calls Err of the context of a Verify before each record of the log and each object, and
// of an Events before each record, so that the steps of the request are at least as many; a wait for Done is no step.
func TestProgressCtx(t *testing.T) {
	p := setupPulse(t, nil)
	p.ctl.setDelay(0)
	var records, objects uint64
	if err := p.db().View(context.Background(), func(tx *logdb.Tx) error {
		records = tx.Seq()
		tx.Objects(func([32]byte, int64) bool { objects++; return true })
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if records < 2 || objects < 2 {
		t.Fatalf("the store has %d records and %d objects", records, objects)
	}
	// An engine that checks the context once per segment would pass the counts below with one record in each.
	if segments := p.db().Stats().Segments; 2*uint64(segments) > records {
		t.Fatalf("the store has %d records in %d segments, want at least two in each", records, segments)
	}

	// If the engine calls Err less often, or makes a context of its own, the pulses of the requests are fewer.
	var n atomic.Uint64
	ctx := progressCtx{context.Background(), &n}
	if err := p.db().Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if got := n.Load(); got < records+objects {
		t.Errorf("Verify called Err %d times, want at least %d (%d records and %d objects)", got, records+objects, records, objects)
	}
	n.Store(0)
	if err := p.db().Events(ctx, func(logdb.Event) bool { return true }); err != nil {
		t.Fatal(err)
	}
	if got := n.Load(); got < records {
		t.Errorf("Events called Err %d times, want at least %d (the records)", got, records)
	}

	n.Store(0)
	parent, cancel := context.WithCancel(context.Background())
	pc := progressCtx{parent, &n}
	_ = pc.Done()
	_, _ = pc.Deadline()
	_ = pc.Value(nil)
	if got := n.Load(); got != 0 {
		t.Errorf("Done, Deadline and Value were counted %d times", got)
	}
	cancel()
	select {
	case <-pc.Done():
	default:
		t.Error("Done is not closed after the context ended")
	}
	if err := pc.Err(); !errors.Is(err, context.Canceled) || n.Load() != 1 {
		t.Errorf("Err() = %v, counted %d times, want context.Canceled, once", err, n.Load())
	}
}
