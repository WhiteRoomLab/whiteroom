package dbserver

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

func TestHello(t *testing.T) {
	t.Run("the reply", func(t *testing.T) {
		f := newFixture(t, func(o *Options) {
			o.Limits.Frame, o.Limits.Commit, o.Limits.Object, o.Limits.Staged = 512, 1000, 2000, 3000
		})
		w := f.connect(dbwire.RoleWriter)
		w.mustCommit(schemaOp(), put(2, "a", "1"))
		r := f.connect(dbwire.RoleReader)
		h := r.hello
		if h.Version != dbwire.Version || h.Role != dbwire.RoleReader || h.Seq != 1 || h.Stopped {
			t.Errorf("reader reply: %+v", h)
		}
		if h.Frame != 512 || h.Commit != 1000 || h.Object != 2000 || h.Staged != 3000 || h.MaxKey != dbwire.MaxKey {
			t.Errorf("limits in the reply: %+v", h)
		}
		if w.hello.Role != dbwire.RoleWriter || w.hello.Seq != 0 {
			t.Errorf("writer reply: %+v", w.hello)
		}
		if h.BootID != w.hello.BootID || h.BootID == ([16]byte{}) {
			t.Errorf("boot IDs %x and %x of one server", h.BootID, w.hello.BootID)
		}
		f.reopen()
		again := f.connect(dbwire.RoleReader)
		if again.hello.BootID == h.BootID {
			t.Errorf("the boot ID %x is the same after a new Open", h.BootID)
		}
		if again.hello.Seq != 1 {
			t.Errorf("the sequence number after a new Open is %d, want 1", again.hello.Seq)
		}
	})

	t.Run("another version", func(t *testing.T) {
		f := newFixture(t, nil)
		c := f.dial(dbwire.RoleReader)
		done := c.writeRaw(hello(2, dbwire.Magic[:]))
		re := wantCode(t, c.readReply(), dbwire.CodeVersion)
		if want := "protocol version 2, but the daemon speaks version 1"; re.Msg != want {
			t.Errorf("got %q, want %q", re.Msg, want)
		}
		<-done
		c.wantClosed()
		// A Hello of another version may have another body: the daemon does not read past the version.
		c = f.dial(dbwire.RoleReader)
		long := binary.BigEndian.AppendUint32(slices.Clone(dbwire.Magic[:]), 3)
		done = c.writeRaw(rawFrame(byte(dbwire.KindHello), 1, append(long, "more of a newer hello"...)))
		wantCode(t, c.readReply(), dbwire.CodeVersion)
		<-done
	})

	t.Run("not a Hello", func(t *testing.T) {
		f := newFixture(t, nil)
		for name, first := range map[string][]byte{
			"a wrong magic":   hello(dbwire.Version, []byte("NOPE")),
			"a short Hello":   rawFrame(byte(dbwire.KindHello), 1, []byte("WR")),
			"a Snapshot":      rawFrame(byte(dbwire.KindSnapshot), 1, nil),
			"a Commit":        rawFrame(byte(dbwire.KindCommit), 1, make([]byte, 9)),
			"a reply":         rawFrame(byte(dbwire.KindOK), 1, nil),
			"an unknown kind": rawFrame(0x42, 1, nil),
			"a long Hello":    rawFrame(byte(dbwire.KindHello), 1, append(slices.Clone(helloBytes[8:]), 0)),
			"reserved bytes":  {byte(dbwire.KindHello), 1, 1, 0, 0, 0, 0, 0},
		} {
			t.Run(name, func(t *testing.T) {
				c := f.dial(dbwire.RoleWriter)
				done := c.writeRaw(first)
				err := c.readReply()
				if name != "a short Hello" {
					wantCode(t, err, dbwire.CodeProtocol)
				} else if err == nil {
					t.Error("a Hello with a short body was answered")
				}
				<-done
				c.wantClosed()
			})
		}
		// None of them took the place of the writer.
		if _, err := f.dial(dbwire.RoleWriter).conn.Write(nil); err != nil {
			t.Fatal(err)
		}
		f.connect(dbwire.RoleWriter)
	})

	t.Run("a frame over the limit", func(t *testing.T) {
		f := newFixture(t, nil)
		c := f.dial(dbwire.RoleReader)
		done := c.writeRaw([]byte{byte(dbwire.KindHello), 1, 0, 0, 0xff, 0xff, 0xff, 0xff})
		wantCode(t, c.readReply(), dbwire.CodeLimit)
		<-done
		c.wantClosed()
	})

	t.Run("a second Hello", func(t *testing.T) {
		f := newFixture(t, nil)
		c := f.connect(dbwire.RoleReader)
		err := c.do(dbwire.KindHello, func(e *dbwire.Enc) { e.HelloRequest() }, nil)
		wantCode(t, err, dbwire.CodeProtocol)
		c.wantClosed()
	})

	t.Run("a message that only the daemon sends", func(t *testing.T) {
		f := newFixture(t, nil)
		for _, kind := range []dbwire.Kind{dbwire.KindOK} {
			c := f.connect(dbwire.RoleReader)
			wantCode(t, c.do(kind, nil, nil), dbwire.CodeProtocol)
			c.wantClosed()
		}
		c := f.connect(dbwire.RoleReader)
		done := c.writeRaw(rawFrame(byte(dbwire.KindError), 1, []byte{0, 1, 0, 0}))
		wantCode(t, c.readReply(), dbwire.CodeProtocol)
		<-done
		c.wantClosed()
	})

	t.Run("silence", func(t *testing.T) {
		f := newFixture(t, func(o *Options) { o.Limits.Stall = 50 * time.Millisecond })
		c := f.dial(dbwire.RoleReader)
		c.wantClosed()
	})
}

func TestRoles(t *testing.T) {
	f := newFixture(t, nil)
	f.update(func(tx *logdb.Tx) { _ = tx.Put(2, []byte("k"), []byte("v")) })
	r := f.connect(dbwire.RoleReader)
	seq := r.mustSnapshot()
	before := f.dump()
	hash := hashOf("object")

	for name, call := range map[string]func() error{
		"PutObject": func() error { _, err := r.putObject("object"); return err },
		"Commit": func() error {
			_, _, err := r.commit(seq, put(2, "k", "x"), event("e"))
			return err
		},
		"PutOriginal": func() error {
			return r.do(dbwire.KindPutOriginal, func(e *dbwire.Enc) {
				c := e.Chunks()
				_, _ = c.Write([]byte("original"))
				_ = c.Close()
				e.PutOriginalTrailer(hashOf("original"), 8)
			}, nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			wantCode(t, call(), dbwire.CodeDenied)
			f.unchanged(before)
			// The connection stays usable, and so does its snapshot.
			if found, v := r.mustGet(2, "k"); !found || v != "v" {
				t.Errorf("Get after the refusal: %v %q", found, v)
			}
		})
	}
	if found, _, err := r.object(hash); err != nil || found {
		t.Errorf("the denied object is in the store: %v %v", found, err)
	}

	t.Run("the writer may", func(t *testing.T) {
		w := f.connect(dbwire.RoleWriter)
		if _, err := w.snapshot(); err != nil {
			t.Fatal(err)
		}
		if _, err := w.putObject("object"); err != nil {
			t.Fatal(err)
		}
	})
}

// TestRoleDeniedLimit: the body of a request that is denied is read only up to the limit of its kind.
func TestRoleDeniedLimit(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Limits.Commit, o.Limits.Object = 1000, 1000 })
	for name, call := range map[string]func(c *client) error{
		"Commit": func(c *client) error {
			return c.do(dbwire.KindCommit, func(e *dbwire.Enc) {
				e.CommitRequest(0)
				e.CommitOp(dbwire.Op{Kind: dbwire.OpEvent, Value: make([]byte, 5000)})
				e.CommitEnd()
			}, nil)
		},
		"PutObject": func(c *client) error {
			return c.do(dbwire.KindPutObject, func(e *dbwire.Enc) { e.PutObjectRequest(hashOf("x"), make([]byte, 5000)) }, nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := f.connect(dbwire.RoleReader)
			c.mustSnapshot()
			wantCode(t, call(c), dbwire.CodeLimit)
			c.wantClosed()
		})
	}
}

// TestRoleDeniedNeverEnds: a reader that sends a request that does not end is answered after the bytes that a request of
// its kind may have, and not when it stops.
func TestRoleDeniedNeverEnds(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Limits.Commit, o.Limits.Stall = 1000, time.Second })
	r := f.connect(dbwire.RoleReader)
	r.mustSnapshot()
	frame := rawFrame(byte(dbwire.KindCommit), 0, make([]byte, 1000)) // without the end flag
	var written atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for written.Load() < 4<<20 {
			n, err := r.conn.Write(frame)
			written.Add(int64(n))
			if err != nil {
				return
			}
		}
	}()
	wantCode(t, r.readReply(), dbwire.CodeLimit)
	<-done
	r.wantClosed()
	if n := written.Load(); n > 1000+64+64<<10 {
		t.Errorf("the server read %d bytes of a request that may have %d", n, 1000+requestSlack)
	}
}

func TestSingleWriter(t *testing.T) {
	f := newFixture(t, nil)
	first := f.connect(dbwire.RoleWriter)
	second := f.dial(dbwire.RoleWriter)
	err := second.sayHello()
	re := wantCode(t, err, dbwire.CodeBusy)
	if re.Msg != "another writer is connected" {
		t.Errorf("got %q", re.Msg)
	}
	second.wantClosed()
	first.mustSnapshot() // the first is not disturbed

	// A reader is not a writer.
	f.connect(dbwire.RoleReader)

	// When the writer has gone, another may come.
	_ = first.conn.Close()
	eventually(t, "the writer to be gone", func() bool {
		f.srv.mu.Lock()
		defer f.srv.mu.Unlock()
		return f.srv.writers == 0
	})
	f.connect(dbwire.RoleWriter)
}

func TestReadersLimit(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Limits.Readers = 2 })
	a := f.connect(dbwire.RoleReader)
	f.connect(dbwire.RoleReader)
	f.connect(dbwire.RoleWriter) // the writer is not counted
	third := f.dial(dbwire.RoleReader)
	wantCode(t, third.sayHello(), dbwire.CodeBusy)
	third.wantClosed()

	_ = a.conn.Close()
	eventually(t, "a reader to be gone", func() bool {
		f.srv.mu.Lock()
		defer f.srv.mu.Unlock()
		return f.srv.readers == 1
	})
	f.connect(dbwire.RoleReader)
}

func TestProtocolErrors(t *testing.T) {
	f := newFixture(t, nil)
	type c struct {
		name  string
		bytes func() []byte
		code  dbwire.Code
	}
	getBody := func(extra ...byte) []byte {
		b := binary.BigEndian.AppendUint32(nil, 2) // table
		b = binary.BigEndian.AppendUint32(b, 1)    // key length
		return append(append(b, 'k'), extra...)
	}
	for _, tc := range []c{
		{"an unknown kind", func() []byte { return rawFrame(0x42, 1, nil) }, dbwire.CodeProtocol},
		{"flags", func() []byte { return rawFrame(byte(dbwire.KindGet), 3, getBody()) }, dbwire.CodeProtocol},
		{"reserved bytes", func() []byte { return []byte{byte(dbwire.KindGet), 1, 0, 1, 0, 0, 0, 0} }, dbwire.CodeProtocol},
		{"a frame over the limit", func() []byte {
			return slices.Concat([]byte{byte(dbwire.KindGet), 1, 0, 0}, binary.BigEndian.AppendUint32(nil, 1<<20))
		}, dbwire.CodeLimit},
		{"a byte after a Get", func() []byte { return rawFrame(byte(dbwire.KindGet), 1, getBody(0)) }, dbwire.CodeProtocol},
		{"a byte after a Snapshot", func() []byte { return rawFrame(byte(dbwire.KindSnapshot), 1, []byte{0}) }, dbwire.CodeProtocol},
		{"a Get that is cut short", func() []byte { return rawFrame(byte(dbwire.KindGet), 1, getBody()[:6]) }, 0},
		{"a flag that is not 0 or 1", func() []byte {
			b := binary.BigEndian.AppendUint32(nil, 2)
			b = binary.BigEndian.AppendUint32(b, 0) // the prefix
			b = append(b, 7)                        // HasAfter
			b = binary.BigEndian.AppendUint32(b, 0)
			b = binary.BigEndian.AppendUint32(b, 0)
			return rawFrame(byte(dbwire.KindScan), 1, b)
		}, dbwire.CodeProtocol},
		{"a key over the limit", func() []byte {
			b := binary.BigEndian.AppendUint32(nil, 2)
			b = binary.BigEndian.AppendUint32(b, dbwire.MaxKey+1)
			return rawFrame(byte(dbwire.KindGet), 1, append(b, make([]byte, dbwire.MaxKey+1)...))
		}, dbwire.CodeLimit},
		{"a Get as a continuation of another kind", func() []byte {
			return slices.Concat(rawFrame(byte(dbwire.KindGet), 0, getBody()[:4]), rawFrame(byte(dbwire.KindScan), 1, getBody()[4:]))
		}, dbwire.CodeProtocol},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := f.connect(dbwire.RoleReader)
			conn.mustSnapshot()
			done := conn.writeRaw(tc.bytes())
			err := conn.readReply()
			if tc.code == 0 {
				// The peer stopped in the middle of a frame: nothing is answered.
				if err == nil {
					t.Fatal("answered")
				}
				return
			}
			wantCode(t, err, tc.code)
			<-done
			conn.wantClosed()
		})
	}

	t.Run("a request that is cut short and then closed", func(t *testing.T) {
		conn := f.connect(dbwire.RoleReader)
		_, _ = conn.conn.Write(rawFrame(byte(dbwire.KindGet), 1, getBody())[:10])
		_ = conn.conn.Close()
	})
}

func TestStall(t *testing.T) {
	const stall = 150 * time.Millisecond
	withStall := func(o *Options) { o.Limits.Stall = stall }

	t.Run("a request that stops", func(t *testing.T) {
		f := newFixture(t, withStall)
		c := f.connect(dbwire.RoleReader)
		start := time.Now()
		if _, err := c.conn.Write(rawFrame(byte(dbwire.KindGet), 1, make([]byte, 100))[:12]); err != nil {
			t.Fatal(err)
		}
		c.wantClosed()
		if d := time.Since(start); d < stall/2 {
			t.Errorf("closed after %v, before the limit of %v", d, stall)
		}
	})

	t.Run("a reply that is not taken", func(t *testing.T) {
		f := newFixture(t, func(o *Options) { withStall(o); o.Limits.Frame = 64 })
		f.update(func(tx *logdb.Tx) {
			for i := range 200 {
				_ = tx.Put(2, []byte{byte(i)}, make([]byte, 100))
			}
		})
		c := f.connect(dbwire.RoleReader)
		c.mustSnapshot()
		sent := c.send(dbwire.KindScan, func(e *dbwire.Enc) { e.ScanRequest(dbwire.ScanRequest{Table: 2}) })
		if err := <-sent; err != nil {
			t.Fatal(err)
		}
		// The reply is many frames and the client reads none of them.
		start := time.Now()
		f.waitSessions(0)
		if d := time.Since(start); d < stall/2 {
			t.Errorf("closed after %v, before the limit of %v", d, stall)
		}
		c.wantClosed()
	})

	t.Run("a peer that goes on is not cut", func(t *testing.T) {
		f := newFixture(t, func(o *Options) { o.Limits.Stall = 400 * time.Millisecond })
		c := f.connect(dbwire.RoleReader)
		c.mustSnapshot()
		req := rawFrame(byte(dbwire.KindGet), 1, slices.Concat(binary.BigEndian.AppendUint32(nil, 2), binary.BigEndian.AppendUint32(nil, 1), []byte("k")))
		for i := 0; i < len(req); i += 3 {
			if _, err := c.conn.Write(req[i:min(i+3, len(req))]); err != nil {
				t.Fatal(err)
			}
			time.Sleep(60 * time.Millisecond)
		}
		if err := c.rd.Reply(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a connection between requests", func(t *testing.T) {
		f := newFixture(t, withStall)
		c := f.connect(dbwire.RoleReader)
		time.Sleep(3 * stall)
		c.mustSnapshot()
	})
}

func TestSnapshotIdle(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Limits.SnapshotIdle = 40 * time.Millisecond })
	idle := make(chan struct{}, 8)
	f.srv.onIdle = func() { idle <- struct{}{} }
	w := f.connect(dbwire.RoleWriter)
	seq := w.mustSnapshot()
	if _, err := w.putObject("staged"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-idle:
	case <-time.After(waitLimit):
		t.Fatal("the snapshot was not released")
	}
	// The connection stays open, and the snapshot and what was staged are gone.
	_, _, err := w.get(2, "k")
	wantCode(t, err, dbwire.CodeNoSnapshot)
	_, err = w.putObject("again")
	wantCode(t, err, dbwire.CodeNoSnapshot)
	_, _, err = w.commit(seq, event("e"))
	wantCode(t, err, dbwire.CodeNoSnapshot)
	if got := w.mustSnapshot(); got != seq {
		t.Errorf("a new snapshot has the sequence number %d, want %d", got, seq)
	}
	if found, _, err := w.object(hashOf("staged")); err != nil || found {
		t.Errorf("a staged object outlived the snapshot: %v %v", found, err)
	}
	_ = w.release() // so that it does not time out again
	r := f.connect(dbwire.RoleReader)
	r.mustSnapshot()
	<-idle
	if _, _, err := r.get(2, "k"); err == nil {
		t.Error("the reader's snapshot was not released")
	}
}

func TestClose(t *testing.T) {
	t.Run("idle sessions and snapshots", func(t *testing.T) {
		f := newFixture(t, nil)
		idle := f.connect(dbwire.RoleReader)
		held := f.connect(dbwire.RoleReader)
		held.mustSnapshot()
		w := f.connect(dbwire.RoleWriter)
		w.mustSnapshot()
		if _, err := w.putObject("staged"); err != nil {
			t.Fatal(err)
		}
		pre := f.dial(dbwire.RoleReader) // no Hello yet
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		for _, c := range []*client{idle, held, w, pre} {
			c.wantClosed()
		}
		if err := f.srv.Close(); err != nil {
			t.Errorf("a second Close: %v", err)
		}
		if err := f.db().View(t.Context(), func(*logdb.Tx) error { return nil }); err == nil {
			t.Error("the engine is still open")
		}
		// A connection that comes after Close is closed at once.
		cc, sc := net.Pipe()
		go f.srv.ServeConn(sc, dbwire.RoleReader)
		_ = cc.SetReadDeadline(time.Now().Add(waitLimit))
		if _, err := cc.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
			t.Errorf("read after Close: %v", err)
		}
		_ = cc.Close()
	})

	t.Run("a request in progress", func(t *testing.T) {
		gate := make(chan struct{})
		entered := make(chan struct{})
		var once sync.Once
		f := newFixture(t, func(o *Options) {
			o.Hook = func(point string) error {
				if point == "commit:gate" {
					once.Do(func() { close(entered) })
					<-gate
				}
				return nil
			}
		})
		w := f.connect(dbwire.RoleWriter)
		seq := w.mustSnapshot()
		res := make(chan error, 1)
		go func() {
			_, _, err := w.commit(seq, put(2, "k", "v"), event("e"))
			res <- err
		}()
		<-entered

		// A reader that does not take the reply of a Scan is in progress too.
		f.update(func(tx *logdb.Tx) {
			for i := range 100 {
				_ = tx.Put(3, []byte{byte(i)}, make([]byte, 100))
			}
		})
		slow := f.connect(dbwire.RoleReader)
		slow.mustSnapshot()
		if err := <-slow.send(dbwire.KindScan, func(e *dbwire.Enc) { e.ScanRequest(dbwire.ScanRequest{Table: 3}) }); err != nil {
			t.Fatal(err)
		}

		closed := make(chan error, 1)
		go func() { closed <- f.srv.Close() }()
		select {
		case err := <-closed:
			t.Fatalf("Close returned (%v) while a commit was in progress", err)
		case <-time.After(50 * time.Millisecond):
		}
		close(gate)
		if err := <-closed; err != nil {
			t.Fatal(err)
		}
		if err := <-res; err == nil {
			t.Error("the commit of a closed server was answered")
		}
		slow.wantClosed()
	})
}

func TestUnixSocket(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Limits.Stall = 5 * time.Second })
	sock := filepath.Join(t.TempDir(), "w.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("no unix sockets: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				f.srv.ServeConn(conn, dbwire.RoleWriter)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		_ = f.srv.Close()
		wg.Wait()
	})
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	c := &client{t: t, f: f, conn: conn, rd: dbwire.NewReader(conn, 1<<20), wr: dbwire.NewWriter(conn, 1<<20)}
	if err := c.sayHello(); err != nil {
		t.Fatal(err)
	}
	big := string(make([]byte, 300_000)) // several frames each way
	c.mustCommit(put(2, "k", big))
	c.mustSnapshot()
	if found, v := c.mustGet(2, "k"); !found || v != big {
		t.Errorf("a value of %d bytes came back as %d bytes", len(big), len(v))
	}
	if _, err := os.Stat(sock); err != nil {
		t.Error(err)
	}
}
