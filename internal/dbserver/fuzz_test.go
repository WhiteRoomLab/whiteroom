package dbserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// stream is the wire bytes of the messages that each call of msgs writes, with a frame of 64 bytes at most.
func stream(msgs ...func(w *dbwire.Writer, e *dbwire.Enc)) []byte {
	var buf bytes.Buffer
	w := dbwire.NewWriter(&buf, 64)
	for _, m := range msgs {
		m(w, &dbwire.Enc{W: w})
	}
	return buf.Bytes()
}

func request(kind dbwire.Kind, body func(e *dbwire.Enc)) func(w *dbwire.Writer, e *dbwire.Enc) {
	return func(w *dbwire.Writer, e *dbwire.Enc) {
		w.Begin(kind)
		if body != nil {
			body(e)
		}
		_ = w.End()
	}
}

// feedConn is the server end of a connection whose peer has sent its bytes already: Read gives them, and the first Read
// after the last of them closes drained, which says that the session has handled all that it was sent and wants more. What
// the session writes is dropped. It reads until it is closed, and then reports the end of the stream.
type feedConn struct {
	in      *bytes.Reader
	drained chan struct{}
	closed  chan struct{}
	drain   sync.Once
	close   sync.Once
}

func newFeedConn(data []byte) *feedConn {
	return &feedConn{in: bytes.NewReader(data), drained: make(chan struct{}), closed: make(chan struct{})}
}

func (c *feedConn) Read(p []byte) (int, error) {
	if c.in.Len() > 0 {
		return c.in.Read(p)
	}
	c.drain.Do(func() { close(c.drained) })
	<-c.closed
	return 0, io.EOF
}

func (c *feedConn) Write(p []byte) (int, error) {
	select {
	case <-c.closed:
		return 0, io.ErrClosedPipe
	default:
		return len(p), nil
	}
}

func (c *feedConn) Close() error {
	c.close.Do(func() { close(c.closed) })
	return nil
}

func (c *feedConn) LocalAddr() net.Addr              { return pipeAddr{} }
func (c *feedConn) RemoteAddr() net.Addr             { return pipeAddr{} }
func (c *feedConn) SetDeadline(time.Time) error      { return nil }
func (c *feedConn) SetReadDeadline(time.Time) error  { return nil }
func (c *feedConn) SetWriteDeadline(time.Time) error { return nil }

func FuzzSession(f *testing.F) {
	h := hashOf("object")
	snapshot := request(dbwire.KindSnapshot, nil)
	// The store of the target has the commit 1, which put the schema row: the first commit of a stream expects 1.
	commit := func(expected uint64, ops ...dbwire.Op) func(w *dbwire.Writer, e *dbwire.Enc) {
		return request(dbwire.KindCommit, func(e *dbwire.Enc) {
			e.CommitRequest(expected)
			for _, o := range ops {
				e.CommitOp(o)
			}
			e.CommitEnd()
		})
	}
	// After a Snapshot, so that a commit, which ends it, has a snapshot of its own.
	committing := func(expected uint64, ops ...dbwire.Op) []func(w *dbwire.Writer, e *dbwire.Enc) {
		return []func(w *dbwire.Writer, e *dbwire.Enc){snapshot, commit(expected, ops...)}
	}
	join := func(parts ...[]func(w *dbwire.Writer, e *dbwire.Enc)) []func(w *dbwire.Writer, e *dbwire.Enc) {
		return slices.Concat(parts...)
	}
	f.Add(byte(0), stream(snapshot,
		request(dbwire.KindGet, func(e *dbwire.Enc) { e.GetRequest(2, []byte("k")) }),
		request(dbwire.KindGet, func(e *dbwire.Enc) { e.GetRequest(2, []byte("missing")) }),
		request(dbwire.KindScan, func(e *dbwire.Enc) { e.ScanRequest(dbwire.ScanRequest{Table: 2, Limit: 1}) }),
		request(dbwire.KindScan, func(e *dbwire.Enc) {
			e.ScanRequest(dbwire.ScanRequest{Table: 2, HasAfter: true, After: []byte("k")})
		}),
		request(dbwire.KindObject, func(e *dbwire.Enc) { e.ObjectRequest(h) }),
		request(dbwire.KindObject, func(e *dbwire.Enc) { e.ObjectRequest(hashOf("missing")) }),
		request(dbwire.KindEvents, nil), request(dbwire.KindVerify, nil), request(dbwire.KindRelease, nil),
		snapshot, request(dbwire.KindBackup, nil), request(dbwire.KindOriginal, func(e *dbwire.Enc) { e.OriginalRequest(h) })))
	f.Add(byte(1), stream(join(
		[]func(w *dbwire.Writer, e *dbwire.Enc){snapshot,
			request(dbwire.KindPutObject, func(e *dbwire.Enc) { e.PutObjectRequest(hashOf("staged"), []byte("staged")) }),
			request(dbwire.KindObject, func(e *dbwire.Enc) { e.ObjectRequest(hashOf("staged")) }),
			commit(1, put(2, "k2", "v2"), put(3, "h2", "x"), event("e"),
				put(11, "activity", "audit head activity 1 sha256:00"))},
		// The refusals, each with a snapshot of its own.
		committing(2, put(3, "h", "again"), event("e")), committing(2, del(3, "h"), event("e")),
		committing(2, put(11, "bad key", "line"), event("e")), committing(2, event("\x00")),
		committing(2, put(0, "k", "v"), event("e")), committing(2), committing(1, event("e")),
		[]func(w *dbwire.Writer, e *dbwire.Enc){request(dbwire.KindCommit, nil)},
		committing(2, put(2, "k3", "v3"), event("e")))...))
	putOriginal := func(data string, hash [32]byte, size int64) func(w *dbwire.Writer, e *dbwire.Enc) {
		return request(dbwire.KindPutOriginal, func(e *dbwire.Enc) {
			c := e.Chunks()
			_, _ = c.Write([]byte(data))
			_ = c.Close()
			e.PutOriginalTrailer(hash, size)
		})
	}
	f.Add(byte(1), stream(putOriginal("original", hashOf("original"), 8), putOriginal("original", hashOf("other"), 8),
		putOriginal("original", hashOf("original"), 9), putOriginal("", hashOf(""), 0),
		request(dbwire.KindOriginal, func(e *dbwire.Enc) { e.OriginalRequest(hashOf("original")) }),
		snapshot, request(dbwire.KindEvents, nil), request(dbwire.KindBackup, nil)))
	f.Add(byte(1), stream(request(dbwire.KindPutOriginal, func(e *dbwire.Enc) {
		e.U32(1 << 31)
		e.Raw(make([]byte, 300))
	})))
	f.Add(byte(0), stream(request(dbwire.KindHello, func(e *dbwire.Enc) { e.HelloRequest() })))
	f.Add(byte(0), []byte{0x42, 1, 0, 0, 0, 0, 0, 0})
	f.Add(byte(1), []byte{byte(dbwire.KindCommit), 0, 0, 0, 0, 0, 0, 1, 0, byte(dbwire.KindCommit), 1, 0, 0, 0, 0, 0, 0})

	f.Fuzz(func(t *testing.T, role byte, data []byte) {
		fx := newFixture(t, func(o *Options) {
			o.Limits = dbwire.Limits{Frame: 64, Commit: 512, Object: 256, Staged: 512, Readers: 2, SnapshotIdle: -1,
				Stall: 2 * time.Second, CommitsPerSecond: -1, MinFree: -1}
		})
		fx.update(func(tx *logdb.Tx) {
			_ = tx.Put(2, []byte("k"), []byte("v"))
			_ = tx.Put(dbwire.TableHistory, []byte("h"), []byte("old"))
			_, _ = tx.PutObject([]byte("object"))
		})
		r := dbwire.RoleReader
		if role&1 == 1 {
			r = dbwire.RoleWriter
		}
		conn := newFeedConn(append(slices.Clone(helloBytes), data...))
		done := make(chan struct{})
		go func() {
			fx.srv.ServeConn(conn, r)
			close(done)
		}()
		// The session ends when it has handled the whole input and reads the end of the stream, or before that, on its own.
		select {
		case <-conn.drained:
		case <-done:
		case <-time.After(waitLimit):
			t.Fatal("the session did not handle its input")
		}
		_ = conn.Close()
		select {
		case <-done:
		case <-time.After(waitLimit):
			t.Fatal("the session did not end")
		}
		// Whatever the peer sent, files/ has originals alone, each with the name of its hash.
		for name, data := range filesOf(fx.fs, "files") {
			if sum := sha256.Sum256([]byte(data)); name != originalPath(sum) {
				t.Fatalf("files/ has %s with the data of %x", name, sum)
			}
		}
		if err := fx.db().Verify(context.Background()); err != nil {
			t.Fatalf("the store is damaged: %v", err)
		}
		if err := fx.srv.Close(); err != nil {
			t.Fatal(err)
		}
	})
}
