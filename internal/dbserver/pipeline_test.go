package dbserver

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// requestBytes is a request as a client writes it.
func requestBytes(kind dbwire.Kind, body func(e *dbwire.Enc)) []byte {
	var buf bytes.Buffer
	w := dbwire.NewWriter(&buf, 1<<20)
	w.Begin(kind)
	e := &dbwire.Enc{W: w}
	if body != nil {
		body(e)
	}
	if err := w.End(); err != nil || e.Err != nil {
		panic(fmt.Sprint("a request is not written: ", err, e.Err))
	}
	return buf.Bytes()
}

// A window is requests that a client writes before it reads their replies, and what the replies are.
type window struct {
	bytes []byte
	kinds []dbwire.Kind
	want  []string
}

func (w *window) add(kind dbwire.Kind, body func(e *dbwire.Enc), want string) {
	w.bytes = append(w.bytes, requestBytes(kind, body)...)
	w.kinds = append(w.kinds, kind)
	w.want = append(w.want, want)
}

func (w *window) get(table uint32, key, want string) {
	w.add(dbwire.KindGet, func(e *dbwire.Enc) { e.GetRequest(table, []byte(key)) }, want)
}

func (w *window) scan(r dbwire.ScanRequest, want string) {
	w.add(dbwire.KindScan, func(e *dbwire.Enc) { e.ScanRequest(r) }, want)
}

func (w *window) object(hash [32]byte, want string) {
	w.add(dbwire.KindObject, func(e *dbwire.Enc) { e.ObjectRequest(hash) }, want)
}

// writeWindow writes b with one Write call, which returns on a net.Pipe when the daemon has read it all: the daemon takes
// a window before the client reads a reply.
func (c *client) writeWindow(b []byte) {
	c.t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := c.conn.Write(b)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			c.t.Fatalf("the window of %d bytes is not written: %v", len(b), err)
		}
	case <-time.After(waitLimit):
		c.t.Fatalf("the daemon did not take a window of %d bytes before the client read a reply", len(b))
	}
}

// readOne reads the reply to a request of kind, and describes it.
func (c *client) readOne(kind dbwire.Kind) string {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(waitLimit))
	err := c.rd.Reply()
	c.f.record(err)
	var re *dbwire.RemoteError
	switch {
	case errors.As(err, &re):
		return "refused " + re.Code.String()
	case err != nil:
		c.t.Fatalf("the reply to a %v: %v", kind, err)
	}
	d := &dbwire.Dec{R: c.rd}
	if kind == dbwire.KindGet {
		found, v, err := dbwire.ReadGetReply(d, 1<<30)
		if err != nil {
			c.t.Fatalf("the reply to a Get: %v", err)
		}
		return fmt.Sprintf("get %v %q", found, v)
	}
	if kind == dbwire.KindScan {
		var rows []string
		for {
			k, v, ok := dbwire.ReadScanRow(d, dbwire.MaxKey, 1<<30)
			if !ok {
				break
			}
			rows = append(rows, fmt.Sprintf("%s=%s", k, v))
		}
		more, err := dbwire.ReadScanEnd(d)
		if err != nil {
			c.t.Fatalf("the reply to a Scan: %v", err)
		}
		return fmt.Sprintf("scan %s more=%v", strings.Join(rows, ","), more)
	}
	if kind == dbwire.KindObject {
		found, data, err := dbwire.ReadObjectReply(d, 1<<30)
		if err != nil {
			c.t.Fatalf("the reply to an Object: %v", err)
		}
		return fmt.Sprintf("object %v %q", found, data)
	}
	if kind == dbwire.KindSnapshot {
		seq, err := dbwire.ReadSnapshotReply(d)
		if err != nil {
			c.t.Fatalf("the reply to a Snapshot: %v", err)
		}
		return fmt.Sprintf("snapshot %d", seq)
	}
	if kind == dbwire.KindCommit {
		seq, _, err := dbwire.ReadCommitReply(d)
		if err != nil {
			c.t.Fatalf("the reply to a Commit: %v", err)
		}
		return fmt.Sprintf("commit %d", seq)
	}
	if err := dbwire.ReadEmpty(d); err != nil {
		c.t.Fatalf("the reply to a %v: %v", kind, err)
	}
	return "ok"
}

// readWindow reads the replies to w, and fails the test at the first that is not the one that it wants.
func (c *client) readWindow(w *window) {
	c.t.Helper()
	for i, kind := range w.kinds {
		if got := c.readOne(kind); got != w.want[i] {
			c.t.Fatalf("reply %d, to a %v: %s, want %s", i, kind, got, w.want[i])
		}
	}
}

var (
	pipeObject = "an object of the window"
	pipeHash   = hashOf(pipeObject)
	pipeMissed = hashOf("an object that is not there")
)

// seedWindow puts the rows and the object that the windows read: k000 to k199 in table 2, a/1 to a/9 and b/1, b/2 in table
// 3.
func seedWindow(f *fixture) {
	f.update(func(tx *logdb.Tx) {
		for i := range 200 {
			_ = tx.Put(2, fmt.Appendf(nil, "k%03d", i), fmt.Appendf(nil, "v%03d", i))
		}
		for i := 1; i <= 9; i++ {
			_ = tx.Put(3, fmt.Appendf(nil, "a/%d", i), fmt.Appendf(nil, "x%d", i))
		}
		_ = tx.Put(3, []byte("b/1"), []byte("y1"))
		_ = tx.Put(3, []byte("b/2"), []byte("y2"))
		_, _ = tx.PutObject([]byte(pipeObject))
	})
}

// TestPipeline: requests that arrive before the replies to those before them are read are answered in their order, each
// with its own reply, with few Write calls.
func TestPipeline(t *testing.T) {
	t.Run("a mixed window", func(t *testing.T) {
		f := newFixture(t, nil)
		seedWindow(f)
		c := f.connect(dbwire.RoleReader)
		c.mustSnapshot()
		var w window
		for i := range 300 {
			switch i % 6 {
			case 0:
				w.get(2, fmt.Sprintf("k%03d", i%200), fmt.Sprintf("get true %q", fmt.Sprintf("v%03d", i%200)))
			case 1:
				w.get(2, fmt.Sprintf("missing%d", i), `get false ""`)
			case 2:
				w.scan(dbwire.ScanRequest{Table: 3, Prefix: []byte("a/"), Limit: 3}, "scan a/1=x1,a/2=x2,a/3=x3 more=true")
			case 3:
				w.object(pipeHash, fmt.Sprintf("object true %q", pipeObject))
			case 4:
				w.object(pipeMissed, `object false ""`)
			case 5:
				w.scan(dbwire.ScanRequest{Table: 3, Prefix: []byte("b/")}, "scan b/1=y1,b/2=y2 more=false")
			}
		}
		if len(w.bytes) > dbwire.WindowBytes {
			t.Fatalf("the window has %d bytes, more than the daemon reads at once", len(w.bytes))
		}
		c.writeWindow(w.bytes)
		c.readWindow(&w)
		if found, v := c.mustGet(2, "k007"); !found || v != "v007" {
			t.Errorf("a Get after the window: %v %q", found, v)
		}
	})

	t.Run("write calls", func(t *testing.T) {
		f := newFixture(t, nil)
		seedWindow(f)
		c, counted := f.connectCounted(dbwire.RoleReader)
		c.mustSnapshot()
		var w window
		for i := range 200 {
			w.get(2, fmt.Sprintf("k%03d", i), fmt.Sprintf("get true %q", fmt.Sprintf("v%03d", i)))
		}
		before := counted.writes.Load()
		c.writeWindow(w.bytes)
		c.readWindow(&w)
		if n := counted.writes.Load() - before; n > 3 {
			t.Errorf("200 Gets that arrived in one write were answered with %d Write calls", n)
		}

		before = counted.writes.Load()
		if found, _ := c.mustGet(2, "k001"); !found {
			t.Error("a Get alone")
		}
		if n := counted.writes.Load() - before; n != 1 {
			t.Errorf("a Get alone was answered with %d Write calls", n)
		}
	})
}

// TestPipelineOtherKinds: a request of another kind among reads is answered in its place, and what is held is written
// before it is handled.
func TestPipelineOtherKinds(t *testing.T) {
	t.Run("a Commit that waits", func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		var once, unblock sync.Once
		f := newFixture(t, func(o *Options) {
			o.Hook = func(point string) error {
				if point == "commit:gate" {
					once.Do(func() { close(entered) })
					<-release
				}
				return nil
			}
		})
		t.Cleanup(func() { unblock.Do(func() { close(release) }) })
		seedWindow(f)
		w := f.connect(dbwire.RoleWriter)
		seq := w.mustSnapshot()
		var win window
		win.get(2, "k001", `get true "v001"`)
		win.get(2, "k002", `get true "v002"`)
		win.add(dbwire.KindCommit, func(e *dbwire.Enc) {
			e.CommitRequest(seq)
			e.CommitOp(put(2, "new", "x"))
			e.CommitOp(event("e"))
			e.CommitEnd()
		}, fmt.Sprintf("commit %d", seq+1))
		w.writeWindow(win.bytes)
		// The Commit waits at the gate, and the replies before it are on the wire.
		for i := range 2 {
			if got := w.readOne(dbwire.KindGet); got != win.want[i] {
				t.Fatalf("reply %d: %s, want %s", i, got, win.want[i])
			}
		}
		select {
		case <-entered:
		case <-time.After(waitLimit):
			t.Fatal("the Commit did not reach the gate")
		}
		unblock.Do(func() { close(release) })
		if got := w.readOne(dbwire.KindCommit); got != win.want[2] {
			t.Errorf("the Commit: %s, want %s", got, win.want[2])
		}
	})

	t.Run("Snapshot and Release", func(t *testing.T) {
		f := newFixture(t, nil)
		seedWindow(f)
		c := f.connect(dbwire.RoleReader)
		c.mustSnapshot()
		var w window
		w.get(2, "k003", `get true "v003"`)
		w.add(dbwire.KindSnapshot, nil, "snapshot 1")
		w.get(2, "k004", `get true "v004"`)
		w.add(dbwire.KindRelease, nil, "ok")
		w.get(2, "k005", "refused no snapshot")
		c.writeWindow(w.bytes)
		c.readWindow(&w)
		c.mustSnapshot()
		if found, v := c.mustGet(2, "k006"); !found || v != "v006" {
			t.Errorf("a Get after a new Snapshot: %v %q", found, v)
		}
	})
}

// TestPipelineWindow: a window of the bytes that dbwire.WindowBytes says, written with one Write call on a net.Pipe, whose
// Write returns only when the peer has read everything, returns before the client reads a reply.
func TestPipelineWindow(t *testing.T) {
	f := newFixture(t, nil)
	seedWindow(f)
	c := f.connect(dbwire.RoleReader)
	c.mustSnapshot()
	var w window
	size := 0
	for size < dbwire.WindowBytes-300 {
		w.get(2, "k"+strings.Repeat("x", 120), `get false ""`)
		size = len(w.bytes)
	}
	// The last request has the key that makes the window exactly the bytes of the constant: its header, table and the
	// length of its key have 16 bytes.
	w.get(2, "k"+strings.Repeat("x", dbwire.WindowBytes-size-16-1), `get false ""`)
	if len(w.bytes) != dbwire.WindowBytes {
		t.Fatalf("the window has %d bytes, want %d", len(w.bytes), dbwire.WindowBytes)
	}
	c.writeWindow(w.bytes)
	c.readWindow(&w)
}

// TestPipelineErrors: a read that is refused is answered in its place, and the connection goes on unless the refusal ends it.
func TestPipelineErrors(t *testing.T) {
	t.Run("no snapshot", func(t *testing.T) {
		f := newFixture(t, nil)
		seedWindow(f)
		c := f.connect(dbwire.RoleReader)
		var w window
		w.get(2, "k001", "refused no snapshot")
		w.scan(dbwire.ScanRequest{Table: 3}, "refused no snapshot")
		w.object(pipeHash, "refused no snapshot")
		c.writeWindow(w.bytes)
		c.readWindow(&w)
		c.mustSnapshot()
		if found, v := c.mustGet(2, "k001"); !found || v != "v001" {
			t.Errorf("a Get after the refusals: %v %q", found, v)
		}
	})

	t.Run("a refusal that closes the connection", func(t *testing.T) {
		f := newFixture(t, nil)
		seedWindow(f)
		c := f.connect(dbwire.RoleReader)
		c.mustSnapshot()
		var w window
		w.get(2, "k001", `get true "v001"`)
		w.get(2, "k002", `get true "v002"`)
		bad := requestBytes(dbwire.KindGet, func(e *dbwire.Enc) { e.GetRequest(2, make([]byte, dbwire.MaxKey+1)) })
		// The daemon does not read the key that it refuses, so the client writes in its own goroutine.
		done := c.writeRaw(append(bytes.Clone(w.bytes), bad...))
		c.readWindow(&w)
		wantCode(t, c.readReply(), dbwire.CodeLimit)
		c.wantClosed()
		<-done
	})
}

// TestPipelineStall: replies that the client does not take end the session after the stall limit, with its snapshot, and
// the daemon serves a new connection.
func TestPipelineStall(t *testing.T) {
	const stall = 150 * time.Millisecond
	f := newFixture(t, func(o *Options) { o.Limits.Stall = stall })
	seedWindow(f)
	c := f.connect(dbwire.RoleReader)
	c.mustSnapshot()
	var w window
	for i := range 20 {
		w.get(2, fmt.Sprintf("k%03d", i), "")
	}
	c.writeWindow(w.bytes)
	start := time.Now()
	f.waitSessions(0)
	if d := time.Since(start); d < stall/2 {
		t.Errorf("the session ended after %v, before the limit of %v", d, stall)
	}
	eventually(t, "the snapshot to be released", func() bool { return len(stacksWith(snapshotMarker)) == 0 })

	r := f.connect(dbwire.RoleReader)
	r.mustSnapshot()
	if found, v := r.mustGet(2, "k001"); !found || v != "v001" {
		t.Errorf("a Get on a new connection: %v %q", found, v)
	}
}

// TestPipelineClientCloses: a client that closes its end after it has written a window ends its session, and nothing of it
// is left.
func TestPipelineClientCloses(t *testing.T) {
	f := newFixture(t, nil)
	seedWindow(f)
	c := f.connect(dbwire.RoleReader)
	c.mustSnapshot()
	var w window
	for i := range 50 {
		w.get(2, fmt.Sprintf("k%03d", i), "")
	}
	c.writeWindow(w.bytes)
	_ = c.conn.Close()
	f.waitSessions(0)
	eventually(t, "the snapshot to be released", func() bool { return len(stacksWith(snapshotMarker)) == 0 })

	r := f.connect(dbwire.RoleReader)
	r.mustSnapshot()
	if found, v := r.mustGet(2, "k001"); !found || v != "v001" {
		t.Errorf("a Get on a new connection: %v %q", found, v)
	}
}

// TestClockDeadlines: a request that follows another reads no clock and sets no deadline unless a tick has passed.
func TestClockDeadlines(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Limits.Stall, o.Limits.SnapshotIdle = 30*time.Second, 5*time.Minute })
	seedWindow(f)
	c, counted := f.connectCounted(dbwire.RoleReader)
	c.mustSnapshot()
	for range 2000 {
		if found, _ := c.mustGet(2, "k001"); !found {
			t.Fatal("a Get did not find its row")
		}
	}
	if r, w := counted.readDeadlines.Load(), counted.writeDeadlines.Load(); r >= 20 || w >= 20 {
		t.Errorf("2,000 Gets set %d read deadlines and %d write deadlines", r, w)
	}
}

// TestClockDeadlineBounds: a deadline is at most its limit and four ticks after it was set, and never earlier than its
// limit, while the last reading of the clock is not more than four ticks old.
func TestClockDeadlineBounds(t *testing.T) {
	const limit = 160 * time.Millisecond
	still := &Server{started: time.Now(), tick: 10 * time.Millisecond}
	still.coarse.Store(int64(time.Minute))
	var d deadline
	if got, want := (&timedConn{s: still}).stamp(&d, limit).Sub(still.started), time.Minute+limit+4*still.tick; got != want {
		t.Errorf("a deadline of %v, set a minute after the start with a tick of %v, is %v after the start, want %v", limit, still.tick, got, want)
	}

	f := newFixture(t, func(o *Options) { o.Limits.Stall = limit })
	tick := f.srv.tick
	tc := &timedConn{s: f.srv, stall: limit}
	checked := 0
	for range 300 {
		time.Sleep(time.Millisecond)
		var d deadline
		at := tc.stamp(&d, limit).Sub(f.srv.started)
		after := time.Since(f.srv.started)
		// The clock may be read while stamp runs, so the bound counts from its end.
		if at > after+limit+4*tick {
			t.Fatalf("the deadline is %v, and the time was %v: it is more than %v after the time", at, after, limit+4*tick)
		}
		if after-time.Duration(d.at) > 4*tick {
			continue // the goroutine of the clock did not run in time
		}
		checked++
		if at < after+limit {
			t.Fatalf("the deadline is %v, and the time was %v: it is less than %v after the time", at, after, limit)
		}
	}
	if checked < 100 {
		t.Errorf("only %d of 300 deadlines were checked", checked)
	}
}

// TestClockEarly: a time-out is believed unless a reading of the clock came late after the one that the deadline was set
// from, or the last reading is more than two ticks old; one that is not believed gets its limit again from the real clock,
// once.
func TestClockEarly(t *testing.T) {
	const tick, limit = 2 * time.Second, 30 * time.Second
	for _, tc := range []struct {
		name   string
		behind time.Duration // the age of the last reading of the clock
		set    time.Duration // the age of the reading that the deadline was set from
		late   time.Duration // the age of the last late reading; 0: none
		real   bool
		limit  time.Duration
		want   bool
	}{
		{name: "the clock is on time", behind: tick, set: 40 * time.Second, limit: limit},
		{name: "a late reading before the deadline was set", behind: tick, set: 40 * time.Second, late: 50 * time.Second, limit: limit},
		{name: "the deadline was set from the late reading", behind: tick, set: 40 * time.Second, late: 40 * time.Second, limit: limit},
		{name: "a late reading after the deadline was set", behind: tick, set: 40 * time.Second, late: 20 * time.Second, limit: limit, want: true},
		{name: "the last reading is old", behind: 3 * tick, set: 40 * time.Second, limit: limit, want: true},
		{name: "a deadline from the real clock", behind: 3 * tick, set: 40 * time.Second, late: 20 * time.Second, real: true, limit: limit},
		{name: "no deadline", behind: 3 * tick, set: 40 * time.Second, late: 20 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{started: time.Now().Add(-time.Hour), tick: tick}
			now := time.Since(s.started)
			s.coarse.Store(int64(now - tc.behind))
			if tc.late > 0 {
				s.lateAt.Store(int64(now - tc.late))
			}
			c := &timedConn{s: s, stall: limit}
			d := deadline{limit: tc.limit, at: int64(now - tc.set), real: tc.real}
			before := d
			var got []time.Time
			set := func(at time.Time) error {
				got = append(got, at)
				return nil
			}
			if early := c.early(&d, set); early != tc.want {
				t.Fatalf("early: %v, want %v", early, tc.want)
			}
			if !tc.want {
				if len(got) != 0 || d != before {
					t.Errorf("a time-out that is believed set %d deadlines, and the deadline is %+v, was %+v", len(got), d, before)
				}
				return
			}
			if len(got) != 1 || !d.real {
				t.Fatalf("a time-out that is not believed set %d deadlines, from the real clock: %v", len(got), d.real)
			}
			if left := time.Until(got[0]); left > limit || left < limit-tick {
				t.Errorf("the new deadline is %v from now, want the limit of %v", left, limit)
			}
			if c.early(&d, set) || len(got) != 1 {
				t.Error("a second time-out of the same deadline is not believed")
			}
		})
	}
}

// TestClockHugeLimit: a limit so long that its deadline is beyond what a Duration holds is no deadline: it does not fire
// at once, as the sum that overflows would make it.
func TestClockHugeLimit(t *testing.T) {
	const tick = 500 * time.Millisecond
	limits := []time.Duration{math.MaxInt64, math.MaxInt64 - time.Second, math.MaxInt64 - 4*tick, 100 * 365 * 24 * time.Hour}

	t.Run("the deadline", func(t *testing.T) {
		s := &Server{started: time.Now().Add(-time.Hour), tick: tick}
		s.coarse.Store(int64(time.Minute))
		s.lateAt.Store(int64(time.Hour)) // a time-out is then believed to be early, which sets the deadline again
		c := &timedConn{s: s}
		for _, limit := range limits {
			huge := limit > math.MaxInt64-time.Hour // its sum with the time since the start overflows
			var d deadline
			at := c.stamp(&d, limit)
			if at.IsZero() != huge {
				t.Errorf("the deadline of %v is %v: a deadline that is beyond what a Duration holds is none", limit, at)
			}
			if !huge && at.Sub(s.started) != time.Minute+limit+4*tick {
				t.Errorf("the deadline of %v is %v after the start", limit, at.Sub(s.started))
			}
			var got []time.Time
			d = deadline{limit: limit}
			if !c.early(&d, func(at time.Time) error { got = append(got, at); return nil }) || len(got) != 1 {
				t.Fatalf("early with the limit %v: %d deadlines set", limit, len(got))
			}
			if got[0].IsZero() != huge || !huge && !got[0].After(time.Now()) {
				t.Errorf("the deadline of %v from the real clock is %v", limit, got[0])
			}
		}
	})

	for name, set := range map[string]func(o *Options, limit time.Duration){
		"the idle limit of a snapshot": func(o *Options, limit time.Duration) { o.Limits.SnapshotIdle = limit },
		"the stall limit":              func(o *Options, limit time.Duration) { o.Limits.Stall = limit },
	} {
		t.Run(name, func(t *testing.T) {
			for _, limit := range limits {
				t.Run(limit.String(), func(t *testing.T) {
					f := newFixture(t, func(o *Options) { set(o, limit) })
					seedWindow(f)
					c := f.connect(dbwire.RoleReader)
					c.mustSnapshot()
					// A request behind the wait for it with the limit is answered, and the snapshot is still there.
					if found, v := c.mustGet(2, "k001"); !found || v != "v001" {
						t.Errorf("a Get: %v %q", found, v)
					}
				})
			}
		})
	}
}

// TestClockTick: the clock is read by one goroutine, once a sixteenth of the shorter limit of time, between a millisecond
// and half a second, and by none when no limit of time is on.
func TestClockTick(t *testing.T) {
	const marker = "dbserver.(*Server).startClock"
	for _, tc := range []struct {
		stall, idle, tick time.Duration
	}{
		{30 * time.Second, 5 * time.Minute, 500 * time.Millisecond},
		{160 * time.Millisecond, 5 * time.Minute, 10 * time.Millisecond},
		{30 * time.Second, 800 * time.Millisecond, 50 * time.Millisecond},
		{8 * time.Millisecond, -1, time.Millisecond},
		{10 * time.Minute, -1, 500 * time.Millisecond},
		{-1, 3200 * time.Millisecond, 200 * time.Millisecond},
		{-1, -1, 0},
	} {
		t.Run(fmt.Sprintf("stall %v idle %v", tc.stall, tc.idle), func(t *testing.T) {
			f := newFixture(t, func(o *Options) { o.Limits.Stall, o.Limits.SnapshotIdle = tc.stall, tc.idle })
			if f.srv.tick != tc.tick {
				t.Errorf("the tick is %v, want %v", f.srv.tick, tc.tick)
			}
			if n := len(stacksWith(marker)); (n == 1) != (tc.tick > 0) || n > 1 {
				t.Errorf("%d goroutines of the clock, with the tick %v", n, tc.tick)
			}
			if tc.stall < 0 || tc.stall >= 100*time.Millisecond { // a limit of a few milliseconds is not for a loaded machine
				c := f.connect(dbwire.RoleReader) // the limits that are off do not stop a connection
				c.mustSnapshot()
			}
			if tc.tick > 0 && tc.tick <= 50*time.Millisecond {
				first := f.srv.coarse.Load()
				eventually(t, "the clock to move", func() bool { return f.srv.coarse.Load() > first+int64(tc.tick) })
			}
			if err := f.srv.Close(); err != nil {
				t.Fatal(err)
			}
			// Close has waited for the goroutine, which may still be on its way out.
			eventually(t, "the goroutine of the clock to end", func() bool { return len(stacksWith(marker)) == 0 })
		})
	}
}

// TestSnapshotIdleTimes: a snapshot is released after the idle limit and not before, and each request puts it off.
func TestSnapshotIdleTimes(t *testing.T) {
	const idle = 300 * time.Millisecond
	f := newFixture(t, func(o *Options) { o.Limits.SnapshotIdle = idle })
	released := make(chan time.Time, 8)
	f.srv.onIdle = func() { released <- time.Now() }
	seedWindow(f)
	c := f.connect(dbwire.RoleReader)

	start := time.Now()
	c.mustSnapshot()
	select {
	case at := <-released:
		if d := at.Sub(start); d < idle {
			t.Errorf("the snapshot was released %v after its request, before the limit of %v", d, idle)
		}
	case <-time.After(waitLimit):
		t.Fatal("the snapshot was not released")
	}
	_, _, err := c.get(2, "k001")
	wantCode(t, err, dbwire.CodeNoSnapshot)

	c.mustSnapshot()
	for range 30 {
		time.Sleep(idle / 10)
		if found, _ := c.mustGet(2, "k001"); !found {
			t.Fatal("a Get did not find its row")
		}
	}
	select {
	case <-released:
		t.Error("a snapshot that a request used every tenth of the limit was released")
	default:
	}
}

// TestReaderIdleTimes: a reader's connection that holds no snapshot is ended, with no reply, after the idle limit and not
// before, and each request puts it off; one that held a snapshot is ended the idle limit after the snapshot was released.
func TestReaderIdleTimes(t *testing.T) {
	const idle = 300 * time.Millisecond
	newTimed := func(t *testing.T) (f *fixture, released, ended chan time.Time) {
		f = newFixture(t, func(o *Options) { o.Limits.SnapshotIdle = idle })
		released, ended = make(chan time.Time, 8), make(chan time.Time, 8)
		f.srv.onIdle = func() { released <- time.Now() }
		f.srv.onIdleClose = func() { ended <- time.Now() }
		return f, released, ended
	}

	t.Run("a Hello and nothing after it", func(t *testing.T) {
		f, released, ended := newTimed(t)
		start := time.Now()
		c := f.connect(dbwire.RoleReader)
		select {
		case at := <-ended:
			if d := at.Sub(start); d < idle {
				t.Errorf("the connection was ended %v after its Hello, before the limit of %v", d, idle)
			}
		case <-time.After(waitLimit):
			t.Fatal("the connection was not ended")
		}
		c.wantClosed()
		f.waitSessions(0)
		if len(released) != 0 {
			t.Error("a snapshot was released on a connection that held none")
		}
	})

	t.Run("each request puts it off", func(t *testing.T) {
		f, _, ended := newTimed(t)
		c := f.connect(dbwire.RoleReader)
		for range 30 {
			time.Sleep(idle / 10)
			if err := c.release(); err != nil {
				t.Fatalf("a Release: %v", err)
			}
		}
		select {
		case <-ended:
			t.Error("a connection that a request used every tenth of the limit was ended")
		default:
		}
	})

	t.Run("after its snapshot was released", func(t *testing.T) {
		f, released, ended := newTimed(t)
		c := f.connect(dbwire.RoleReader)
		start := time.Now()
		c.mustSnapshot()
		var at time.Time
		select {
		case at = <-released:
			if d := at.Sub(start); d < idle {
				t.Errorf("the snapshot was released %v after its request, before the limit of %v", d, idle)
			}
		case <-time.After(waitLimit):
			t.Fatal("the snapshot was not released")
		}
		select {
		case end := <-ended:
			if d := end.Sub(at); d < idle {
				t.Errorf("the connection was ended %v after its snapshot was released, before the limit of %v", d, idle)
			}
		case <-time.After(waitLimit):
			t.Fatal("the connection was not ended")
		}
		c.wantClosed()
		f.waitSessions(0)
	})
}

// TestClockHeld: the limits never fire early when the goroutine of the clock does not run, as when the process of the
// daemon has been stopped for a while.
func TestClockHeld(t *testing.T) {
	const stall, idle = 150 * time.Millisecond, 300 * time.Millisecond
	var held atomic.Bool
	blocked := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	let := func() { once.Do(func() { close(release) }) }
	f := newFixture(t, func(o *Options) {
		o.Limits.Stall, o.Limits.SnapshotIdle = stall, idle
		o.Hook = func(point string) error {
			if point == "clock:tick" && held.Load() {
				select {
				case blocked <- struct{}{}:
				default:
				}
				<-release
			}
			return nil
		}
	})
	t.Cleanup(let) // before the server is closed, which waits for the goroutine of the clock
	released := make(chan time.Time, 8)
	f.srv.onIdle = func() { released <- time.Now() }
	seedWindow(f)

	held.Store(true)
	select {
	case <-blocked:
	case <-time.After(waitLimit):
		t.Fatal("the clock did not tick")
	}
	time.Sleep(time.Second)
	if behind := time.Since(f.srv.started) - time.Duration(f.srv.coarse.Load()); behind < 900*time.Millisecond {
		t.Fatalf("the clock is %v behind, the test did not hold it", behind)
	}

	t.Run("a snapshot is not released early", func(t *testing.T) {
		c := f.connect(dbwire.RoleReader)
		defer func() { _ = c.conn.Close() }()
		c.mustSnapshot()
		time.Sleep(idle / 3)
		if found, v := c.mustGet(2, "k001"); !found || v != "v001" {
			t.Errorf("a Get after a third of the limit: %v %q", found, v)
		}
	})

	t.Run("a reader that holds no snapshot is not ended early", func(t *testing.T) {
		c := f.connect(dbwire.RoleReader)
		defer func() { _ = c.conn.Close() }()
		time.Sleep(idle / 3)
		if err := c.release(); err != nil {
			t.Errorf("a Release after a third of the limit: %v", err)
		}
	})

	t.Run("a request that goes on is not cut", func(t *testing.T) {
		c := f.connect(dbwire.RoleReader)
		defer func() { _ = c.conn.Close() }()
		c.mustSnapshot()
		req := requestBytes(dbwire.KindGet, func(e *dbwire.Enc) { e.GetRequest(2, []byte("k002")) })
		if _, err := c.conn.Write(req[:len(req)/2]); err != nil {
			t.Fatal(err)
		}
		time.Sleep(stall * 2 / 3)
		if _, err := c.conn.Write(req[len(req)/2:]); err != nil {
			t.Fatal(err)
		}
		if got := c.readOne(dbwire.KindGet); got != `get true "v002"` {
			t.Errorf("the reply: %s", got)
		}
	})

	t.Run("a request that stops is cut after the limit", func(t *testing.T) {
		c := f.connect(dbwire.RoleReader)
		c.mustSnapshot()
		start := time.Now()
		if _, err := c.conn.Write(requestBytes(dbwire.KindGet, func(e *dbwire.Enc) { e.GetRequest(2, []byte("k002")) })[:12]); err != nil {
			t.Fatal(err)
		}
		c.wantClosed()
		if d := time.Since(start); d < stall {
			t.Errorf("a request that stopped was cut after %v, before the limit of %v", d, stall)
		}
	})

	t.Run("a snapshot that is not used is released after the limit", func(t *testing.T) {
		for len(released) > 0 { // the snapshots of the connections above that were not closed
			<-released
		}
		c := f.connect(dbwire.RoleReader)
		defer func() { _ = c.conn.Close() }()
		start := time.Now()
		c.mustSnapshot()
		select {
		case at := <-released:
			if d := at.Sub(start); d < idle {
				t.Errorf("the snapshot was released %v after its request, before the limit of %v", d, idle)
			}
		case <-time.After(waitLimit):
			t.Fatal("the snapshot was not released")
		}
	})

	let()
	eventually(t, "the clock to mark its late reading", func() bool { return f.srv.lateAt.Load() > 0 })
	c := f.connect(dbwire.RoleReader)
	c.mustSnapshot()
	if found, v := c.mustGet(2, "k003"); !found || v != "v003" {
		t.Errorf("a Get after the clock goes on: %v %q", found, v)
	}
}
