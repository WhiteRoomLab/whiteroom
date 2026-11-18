package dbclient_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

const (
	// stallCut is the limit of the tests that wait for the client to cut a connection: it must not be before the limit.
	stallCut = 100 * time.Millisecond
	// stallKeep is the limit of the tests that check that the client does not: no time is checked against it, and the
	// longer it is, the less a busy machine can make them fail.
	stallKeep = 400 * time.Millisecond
	// stallLong is how long the daemon of those tests keeps a reply or a request back.
	stallLong = 4 * stallKeep

	// What the error of a cut says that the daemon did not do, for a Read of a reply and for a Write of a request.
	notSent = "sent nothing"
	notRead = "not read the request"
)

// hold reads what the client sends and gives no reply, until the client has closed the connection.
func (p *peer) hold() {
	for range p.reqs {
	}
}

// wire returns a frame of an OK reply, whose body build writes, with the end flag if last: a test writes the frames of a
// reply with raw, and so can stop in the middle of one.
func (p *peer) wire(last bool, build func(e *dbwire.Enc)) []byte {
	p.t.Helper()
	var b bytes.Buffer
	w := dbwire.NewWriter(&b, 1<<20)
	w.Begin(dbwire.KindOK)
	e := &dbwire.Enc{W: w}
	build(e)
	if err := w.End(); err != nil || e.Err != nil {
		p.t.Errorf("a frame of the peer: %v, %v", err, e.Err)
		p.stop()
	}
	out := b.Bytes()
	if !last {
		out[1] &^= 1
	}
	return out
}

// deafDialer is a Dialer of daemons that answer the Hello and the Snapshot of a connection and then read nothing.
type deafDialer struct {
	mu    sync.Mutex
	dials int
	wg    sync.WaitGroup
	quit  chan struct{}
}

func (d *deafDialer) Dial(_ context.Context, role dbwire.Role) (net.Conn, error) {
	d.mu.Lock()
	d.dials++
	d.mu.Unlock()
	cc, sc := net.Pipe()
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer func() { _ = sc.Close() }()
		rd, wr := dbwire.NewReader(sc, 1<<20), dbwire.NewWriter(sc, 1<<20)
		answer := func(kind dbwire.Kind, body func(e *dbwire.Enc)) bool {
			if k, err := rd.Next(); err != nil || k != kind || rd.Discard() != nil {
				return false
			}
			wr.Begin(dbwire.KindOK)
			body(&dbwire.Enc{W: wr})
			return wr.End() == nil
		}
		if !answer(dbwire.KindHello, func(e *dbwire.Enc) {
			e.HelloReply(dbwire.HelloReply{Version: dbwire.Version, Role: role, Frame: testFrame, Commit: 1 << 20,
				Object: 1 << 20, Staged: 1 << 20, MaxKey: dbwire.MaxKey})
		}) || !answer(dbwire.KindSnapshot, func(e *dbwire.Enc) { e.SnapshotReply(1) }) {
			return
		}
		<-d.quit
	}()
	return cc, nil
}

func (d *deafDialer) String() string { return "peer" }

func (d *deafDialer) Close() error {
	close(d.quit)
	d.wg.Wait()
	return nil
}

// wantStall checks the error of a request that the client has cut after the daemon had not done what did says (notSent or
// notRead) for the limit stallCut, cuts times in a row from began. The error says that the daemon is unavailable, and the
// request did not end before the limit, which no context had set.
func wantStall(t testing.TB, err error, began time.Time, did string, cuts int) {
	t.Helper()
	took := time.Since(began)
	want := "peer: cannot reach the daemon: the daemon has " + did + " for 100ms"
	switch {
	case err == nil:
		t.Fatal("the request did not fail")
	case !errors.Is(err, dbclient.ErrUnavailable):
		t.Errorf("the error is %q, want one that wraps ErrUnavailable", err)
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		t.Errorf("the error is %q, want one that is not the error of a context", err)
	case !strings.Contains(err.Error(), want):
		t.Errorf("the error is %q, want one with %q", err, want)
	}
	if limit := time.Duration(cuts) * stallCut; took < limit {
		t.Errorf("the request ended after %v, before the limit of %v", took, limit)
	}
}

// wantConnections checks how many connections the client dialed and how many of them it has not closed.
func wantConnections(t testing.TB, pd *peerDialer, rec *recorder, dialed, live int) {
	t.Helper()
	if got := pd.dialed(); got != dialed {
		t.Errorf("the client dialed %d connections, want %d", got, dialed)
	}
	if got := rec.liveConns(); got != live {
		t.Errorf("%d connections are open, want %d", got, live)
	}
}

// TestStall: a connection that the daemon moves no byte of, for Options.Stall, in a Read of a reply or a Write of a request
// that is under the limit, is cut, and the request fails with an error that wraps ErrUnavailable. It is not tried again
// on a new connection, as the failure of a connection that the daemon has ended is.
func TestStall(t *testing.T) {
	cut := dbclient.Options{DialWait: -1, Stall: stallCut}
	k := []byte("k")

	t.Run("the Hello", func(t *testing.T) {
		noLeaks(t)
		pd := &peerDialer{t: t, script: func(p *peer) {
			p.expect(dbwire.KindHello)
			p.hold()
		}}
		began := time.Now()
		// DialWait is its default: the Hello is not tried again, as a failing dial is.
		c, err := dbclient.Open(bg, &recorder{Dialer: pd}, dbwire.RoleReader, dbclient.Options{Stall: stallCut})
		if err == nil {
			_ = c.Close()
			t.Fatal("Open succeeded")
		}
		wantStall(t, err, began, notSent, 1)
		if got := pd.dialed(); got != 1 {
			t.Errorf("the client dialed %d connections, want 1", got)
		}
	})

	t.Run("the first request of a pooled connection", func(t *testing.T) {
		c, rec, pd := openPeerWith(t, dbwire.RoleReader, cut, func(p *peer) {
			p.hello()
			p.expect(dbwire.KindSnapshot)
			p.hold()
		})
		began := time.Now()
		err := c.View(bg, func(tx *dbclient.Tx) error {
			_, _, err := tx.Get(2, k)
			return err
		})
		wantStall(t, err, began, notSent, 1)
		wantConnections(t, pd, rec, 1, 0)
	})

	reads := []struct {
		name string
		kind dbwire.Kind
		ask  func(tx *dbclient.Tx) error
	}{
		{"Get", dbwire.KindGet, func(tx *dbclient.Tx) error {
			_, _, err := tx.Get(2, k)
			return err
		}},
		{"Scan", dbwire.KindScan, func(tx *dbclient.Tx) error {
			return tx.Scan(2, nil, func(_, _ []byte) bool { return true })
		}},
		{"GetMany", dbwire.KindGet, func(tx *dbclient.Tx) error {
			return tx.GetMany(2, [][]byte{[]byte("a"), []byte("b")})
		}},
		{"Object", dbwire.KindObject, func(tx *dbclient.Tx) error {
			_, _, err := tx.Object([32]byte{1})
			return err
		}},
	}
	for _, r := range reads {
		t.Run("the reply to a "+r.name, func(t *testing.T) {
			c, rec, pd := openPeerWith(t, dbwire.RoleReader, cut, func(p *peer) {
				p.hello()
				p.snapshot(1)
				p.expect(r.kind)
				p.hold()
			})
			began := time.Now()
			err := c.View(bg, r.ask)
			wantStall(t, err, began, notSent, 1)
			wantConnections(t, pd, rec, 1, 0)
		})
	}

	t.Run("the body of an original", func(t *testing.T) {
		c, rec, pd := openPeerWith(t, dbwire.RoleReader, cut, func(p *peer) {
			p.hello()
			p.expect(dbwire.KindOriginal)
			head := len(p.wire(true, func(e *dbwire.Enc) { e.OriginalReply(100) }))
			whole := p.wire(true, func(e *dbwire.Enc) {
				e.OriginalReply(100)
				e.Raw(make([]byte, 100))
			})
			p.raw(whole[:head+10]) // the header of the frame says that 90 bytes more come
			p.hold()
		})
		o, err := c.Original(bg, [32]byte{1})
		must(t, err)
		began := time.Now()
		got, err := io.ReadAll(o)
		wantStall(t, err, began, notSent, 1)
		if len(got) != 10 {
			t.Errorf("the reads gave %d bytes, want the 10 that came", len(got))
		}
		if err := o.Close(); err != nil && !errors.Is(err, dbclient.ErrUnavailable) {
			t.Errorf("Close = %v", err)
		}
		wantConnections(t, pd, rec, 1, 0)
	})

	t.Run("the end of an original that the caller has read to its last byte", func(t *testing.T) {
		c, rec, pd := openPeerWith(t, dbwire.RoleReader, cut, func(p *peer) {
			p.hello()
			p.expect(dbwire.KindOriginal)
			// Every byte of the original, and no frame that ends the reply: only Close waits for that.
			p.raw(p.wire(false, func(e *dbwire.Enc) {
				e.OriginalReply(100)
				e.Raw(make([]byte, 100))
			}))
			p.hold()
		})
		// The context ends long after the limit: a Close that is not under it fails the test, and does not hang.
		ctx, cancel := context.WithTimeout(bg, 5*time.Second)
		defer cancel()
		o, err := c.Original(ctx, [32]byte{1})
		must(t, err)
		if _, err := io.ReadFull(o, make([]byte, 100)); err != nil {
			t.Fatalf("the reads gave %v, want the 100 bytes", err)
		}
		began := time.Now()
		wantStall(t, o.Close(), began, notSent, 1)
		wantConnections(t, pd, rec, 1, 0)
	})

	t.Run("a Backup after its first frame", func(t *testing.T) {
		c, rec, pd := openPeerWith(t, dbwire.RoleReader, cut, func(p *peer) {
			p.hello()
			if p.n == 0 {
				p.hold()
				return
			}
			p.expect(dbwire.KindBackup)
			p.raw(p.wire(false, func(e *dbwire.Enc) {
				e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryPosition, Position: dbwire.BackupPosition{Seq: 3}})
			}))
			p.hold()
		})
		began := time.Now()
		_, err := c.Backup(bg, simfs.New("copy"))
		wantStall(t, err, began, notSent, 1)
		wantConnections(t, pd, rec, 2, 1) // the one of the pool is still open
	})

	t.Run("a PutObject that the daemon does not read", func(t *testing.T) {
		noLeaks(t)
		dd := &deafDialer{quit: make(chan struct{})}
		c, err := dbclient.Open(bg, dd, dbwire.RoleWriter, cut)
		must(t, err)
		t.Cleanup(func() { _ = c.Close() })
		data := bytes.Repeat([]byte("an object "), 1<<20/len("an object "))
		began := time.Now()
		err = c.Update(bg, func(tx *dbclient.Tx) error { return tx.PutObject(sha256.Sum256(data), data) })
		wantStall(t, err, began, notRead, 1)
		dd.mu.Lock()
		defer dd.mu.Unlock()
		if dd.dials != 2 {
			t.Errorf("the client dialed %d connections, want 2: the writer is made again at the end of the Update", dd.dials)
		}
	})

	t.Run("a writer that gets no snapshot, and the connection that it makes again", func(t *testing.T) {
		c, _, pd := openPeerWith(t, dbwire.RoleWriter, cut, func(p *peer) {
			if p.n == 0 {
				p.hello()
				p.expect(dbwire.KindSnapshot)
				p.hold()
				return
			}
			p.expect(dbwire.KindHello) // the connection that the Update makes at its end, for the next one
			p.hold()
		})
		began := time.Now()
		err := c.Update(bg, func(tx *dbclient.Tx) error { return tx.Put(2, k, k) })
		wantStall(t, err, began, notSent, 2) // the cut of the Snapshot, and that of the Hello after it
		if got := pd.dialed(); got != 2 {
			t.Errorf("the client dialed %d connections, want 2: the one that it made again, and no other for the Snapshot", got)
		}
	})
}

// TestStallExempt: what is not under the limit is not cut, whatever the time: the wait for the reply to a Verify, a Commit,
// an Events, and for the first frame of a Backup; the time that the caller takes between the Reads of an original and
// between the blocks of an original that it puts; and a reply that comes in small pieces, each within the limit.
func TestStallExempt(t *testing.T) {
	keep := dbclient.Options{DialWait: -1, Stall: stallKeep}
	k := []byte("k")
	// ran checks that the test took as long as the daemon kept back, and that the client made the connections that it was
	// to make, and no other.
	ran := func(t *testing.T, began time.Time, pd *peerDialer, dialed int) {
		t.Helper()
		if took := time.Since(began); took < 3*stallKeep {
			t.Errorf("the test took %v, less than the daemon was to keep back", took)
		}
		if got := pd.dialed(); got != dialed {
			t.Errorf("the client dialed %d connections, want %d", got, dialed)
		}
	}

	t.Run("the reply to a Verify", func(t *testing.T) {
		t.Parallel()
		c, _, pd := openPeerWith(t, dbwire.RoleReader, keep, func(p *peer) {
			p.hello()
			p.expect(dbwire.KindVerify)
			time.Sleep(stallLong)
			p.reply(nil)
			p.hold()
		})
		began := time.Now()
		must(t, c.Verify(bg))
		ran(t, began, pd, 1)
	})

	t.Run("the reply to a Commit", func(t *testing.T) {
		t.Parallel()
		c, _, pd := openPeerWith(t, dbwire.RoleWriter, keep, func(p *peer) {
			p.hello()
			p.snapshot(1)
			p.expect(dbwire.KindCommit)
			time.Sleep(stallLong)
			p.reply(func(e *dbwire.Enc) { e.CommitReply(2, time.Unix(1_700_000_000, 0)) })
			p.hold()
		})
		began := time.Now()
		must(t, c.Update(bg, func(tx *dbclient.Tx) error { return tx.Put(2, k, k) }))
		ran(t, began, pd, 1)
	})

	t.Run("the frames of an Events", func(t *testing.T) {
		t.Parallel()
		event := func(n uint64) func(e *dbwire.Enc) {
			return func(e *dbwire.Enc) { e.Event(dbwire.Event{Number: n, Seq: 5, Data: []byte("x")}) }
		}
		c, _, pd := openPeerWith(t, dbwire.RoleReader, keep, func(p *peer) {
			p.hello()
			p.snapshot(5)
			if p.n == 0 {
				p.release()
				p.hold()
				return
			}
			p.expect(dbwire.KindEvents)
			p.raw(p.wire(false, event(1)))
			time.Sleep(stallLong)
			p.raw(p.wire(true, func(e *dbwire.Enc) {
				event(2)(e)
				e.EventsEnd()
			}))
			p.release()
			p.hold()
		})
		var got []uint64
		began := time.Now()
		must(t, c.View(bg, func(tx *dbclient.Tx) error {
			return tx.Events(bg, func(n uint64, _ []byte) bool {
				got = append(got, n)
				return true
			})
		}))
		if len(got) != 2 || got[0] != 1 || got[1] != 2 {
			t.Errorf("the events are %v, want [1 2]", got)
		}
		ran(t, began, pd, 2)
	})

	t.Run("the first frame of a Backup", func(t *testing.T) {
		t.Parallel()
		c, _, pd := openPeerWith(t, dbwire.RoleReader, keep, func(p *peer) {
			p.hello()
			if p.n == 0 {
				p.hold()
				return
			}
			p.expect(dbwire.KindBackup)
			time.Sleep(stallLong)
			p.raw(p.wire(true, func(e *dbwire.Enc) {
				e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryPosition, Position: dbwire.BackupPosition{Seq: 3}})
				e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryEnd})
			}))
			p.hold()
		})
		began := time.Now()
		b, err := c.Backup(bg, simfs.New("copy"))
		must(t, err)
		if b.Seq != 3 {
			t.Errorf("the backup has the sequence number %d, want 3", b.Seq)
		}
		ran(t, began, pd, 2)
	})

	t.Run("an original that the caller reads late", func(t *testing.T) {
		t.Parallel()
		data := bytes.Repeat([]byte("o"), 6000)
		c, _, pd := openPeerWith(t, dbwire.RoleReader, keep, func(p *peer) {
			p.hello()
			p.expect(dbwire.KindOriginal)
			p.raw(p.wire(false, func(e *dbwire.Enc) {
				e.OriginalReply(int64(len(data)))
				e.Raw(data[:3000])
			}))
			p.raw(p.wire(true, func(e *dbwire.Enc) { e.Raw(data[3000:]) })) // the client reads it when it likes
			p.hold()
		})
		o, err := c.Original(bg, [32]byte{1})
		must(t, err)
		began := time.Now()
		time.Sleep(stallLong)
		got, err := io.ReadAll(o)
		must(t, err)
		must(t, o.Close())
		if !bytes.Equal(got, data) {
			t.Errorf("the original has %d bytes, want %d", len(got), len(data))
		}
		ran(t, began, pd, 1)
	})

	t.Run("a reply that comes in small pieces", func(t *testing.T) {
		t.Parallel()
		c, _, pd := openPeerWith(t, dbwire.RoleReader, keep, func(p *peer) {
			p.hello()
			p.snapshot(1)
			p.expect(dbwire.KindScan)
			page := p.wire(true, func(e *dbwire.Enc) {
				for _, key := range []string{"a", "b", "c", "d", "e"} {
					e.ScanRow([]byte(key), bytes.Repeat([]byte(key), 20))
				}
				e.ScanEnd(false)
			})
			// Forty pieces, one every tenth of the limit: the reply takes four times the limit, and a Read of the client
			// never waits for more than a tenth of it.
			piece := (len(page) + 39) / 40
			for off := 0; off < len(page); off += piece {
				p.raw(page[off:min(off+piece, len(page))])
				time.Sleep(stallKeep / 10)
			}
			p.release()
			p.hold()
		})
		var got int
		began := time.Now()
		must(t, c.View(bg, func(tx *dbclient.Tx) error {
			return tx.Scan(2, nil, func(_, _ []byte) bool {
				got++
				return true
			})
		}))
		if got != 5 {
			t.Errorf("the scan gave %d rows, want 5", got)
		}
		ran(t, began, pd, 1)
	})

	t.Run("an original that the caller puts slowly", func(t *testing.T) {
		t.Parallel()
		blocks := [][]byte{bytes.Repeat([]byte("a"), testFrame), []byte("the last block")}
		c, _, pd := openPeerWith(t, dbwire.RoleWriter, keep, func(p *peer) {
			p.hello()
			p.snapshot(1)
			p.release()
			p.expect(dbwire.KindPutOriginal)
			p.reply(nil)
			p.hold()
		})
		var all []byte
		for _, b := range blocks {
			all = append(all, b...)
		}
		began := time.Now()
		sum, size, err := c.PutOriginal(bg, &pausingReader{blocks: blocks, pause: stallLong})
		must(t, err)
		if sum != sha256.Sum256(all) || size != int64(len(all)) {
			t.Errorf("PutOriginal gave %x and %d, want %x and %d", sum, size, sha256.Sum256(all), len(all))
		}
		ran(t, began, pd, 1)
	})
}

// pausingReader gives the blocks one at a time, and waits for pause before each but the first.
type pausingReader struct {
	blocks [][]byte
	pause  time.Duration
	next   int
}

func (r *pausingReader) Read(p []byte) (int, error) {
	if r.next == len(r.blocks) {
		return 0, io.EOF
	}
	if r.next > 0 {
		time.Sleep(r.pause)
	}
	n := copy(p, r.blocks[r.next])
	r.next++
	return n, nil
}

// stalls counts the goroutines of the clients that watch their connections.
func stalls() int {
	buf := make([]byte, 1<<20)
	return strings.Count(string(buf[:runtime.Stack(buf, true)]), "(*Client).watchStalls")
}

// TestStallOff: a limit that is negative is no limit: the client waits for the context, and does not watch.
func TestStallOff(t *testing.T) {
	t.Run("the Hello", func(t *testing.T) {
		noLeaks(t)
		pd := &peerDialer{t: t, script: func(p *peer) {
			p.expect(dbwire.KindHello)
			p.hold()
		}}
		ctx, cancel := context.WithTimeout(bg, 4*stallCut)
		defer cancel()
		began := time.Now()
		c, err := dbclient.Open(ctx, &recorder{Dialer: pd}, dbwire.RoleReader, dbclient.Options{Stall: -1})
		if err == nil {
			_ = c.Close()
			t.Fatal("Open succeeded")
		}
		if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, dbclient.ErrUnavailable) {
			t.Errorf("Open = %v, want the error of the context", err)
		}
		if took := time.Since(began); took < 4*stallCut {
			t.Errorf("Open returned after %v, before its context ended", took)
		}
	})

	t.Run("no goroutine", func(t *testing.T) {
		before := stalls()
		off, _, _ := openPeerWith(t, dbwire.RoleReader, dbclient.Options{DialWait: -1, Stall: -1}, func(p *peer) {
			p.hello()
			p.hold()
		})
		if got := stalls(); got != before {
			t.Errorf("a client with no limit has made %d goroutines that watch", got-before)
		}
		on, _, _ := openPeerWith(t, dbwire.RoleReader, dbclient.Options{DialWait: -1}, func(p *peer) {
			p.hello()
			p.hold()
		})
		if got := stalls(); got != before+1 {
			t.Errorf("a client with the default limit has made %d goroutines that watch, want 1", got-before)
		}
		_ = on.Close()
		_ = off.Close()
		eventually(t, "the goroutine that watches to end", func() bool { return stalls() == before })
	})
}

// cancelOnClose ends a context when the client closes the connection, as it does when it cuts it: the context has ended by
// the time that the request that was blocked on the connection fails.
type cancelOnClose struct {
	net.Conn
	cancel func()
}

func (c *cancelOnClose) Close() error {
	c.cancel()
	return c.Conn.Close()
}

type cancelDialer struct {
	*peerDialer
	cancel func()
}

func (d *cancelDialer) Dial(ctx context.Context, role dbwire.Role) (net.Conn, error) {
	nc, err := d.peerDialer.Dial(ctx, role)
	if err != nil {
		return nil, err
	}
	return &cancelOnClose{Conn: nc, cancel: d.cancel}, nil
}

// TestStallContext: the end of a context is what the request returns, when it comes before the limit or with the cut, and
// the limit does not make a context that has not ended fail.
func TestStallContext(t *testing.T) {
	t.Run("the Hello", func(t *testing.T) {
		noLeaks(t)
		pd := &peerDialer{t: t, script: func(p *peer) {
			p.expect(dbwire.KindHello)
			p.hold()
		}}
		ctx, cancel := context.WithTimeout(bg, stallCut)
		defer cancel()
		c, err := dbclient.Open(ctx, &recorder{Dialer: pd}, dbwire.RoleReader, dbclient.Options{Stall: time.Hour})
		if err == nil {
			_ = c.Close()
			t.Fatal("Open succeeded")
		}
		if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, dbclient.ErrUnavailable) {
			t.Errorf("Open = %v, want the error of the context", err)
		}
	})

	t.Run("a read", func(t *testing.T) {
		c, _, _ := openPeerWith(t, dbwire.RoleReader, dbclient.Options{DialWait: -1, Stall: stallKeep}, func(p *peer) {
			p.hello()
			p.snapshot(1)
			p.expect(dbwire.KindGet)
			p.hold()
		})
		ctx, cancel := context.WithTimeout(bg, stallKeep/8)
		defer cancel()
		err := c.View(ctx, func(tx *dbclient.Tx) error {
			_, _, err := tx.Get(2, []byte("k"))
			return err
		})
		if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, dbclient.ErrUnavailable) {
			t.Errorf("View = %v, want the error of the context", err)
		}
	})

	t.Run("a context that ends as the limit cuts the connection", func(t *testing.T) {
		noLeaks(t)
		ctx, cancel := context.WithCancel(bg)
		defer cancel()
		pd := &peerDialer{t: t, script: func(p *peer) {
			p.hello()
			p.snapshot(1)
			p.expect(dbwire.KindGet)
			p.hold()
		}}
		c, err := dbclient.Open(bg, &cancelDialer{peerDialer: pd, cancel: cancel}, dbwire.RoleReader,
			dbclient.Options{DialWait: -1, Stall: stallCut})
		must(t, err)
		t.Cleanup(func() { _ = c.Close() })
		began := time.Now()
		err = c.View(ctx, func(tx *dbclient.Tx) error {
			_, _, err := tx.Get(2, []byte("k"))
			return err
		})
		if !errors.Is(err, context.Canceled) || errors.Is(err, dbclient.ErrUnavailable) {
			t.Errorf("View = %v, want the error of the context, which the cut ended", err)
		}
		if took := time.Since(began); took < stallCut {
			t.Errorf("View ended after %v, before the limit of %v: the context did not end with the cut", took, stallCut)
		}
	})
}
