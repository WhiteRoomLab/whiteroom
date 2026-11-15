package dbserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"net"
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

func FuzzSession(f *testing.F) {
	h := hashOf("object")
	snapshot := request(dbwire.KindSnapshot, nil)
	commit := func(ops ...dbwire.Op) func(w *dbwire.Writer, e *dbwire.Enc) {
		return request(dbwire.KindCommit, func(e *dbwire.Enc) {
			e.CommitRequest(0)
			for _, o := range ops {
				e.CommitOp(o)
			}
			e.CommitEnd()
		})
	}
	f.Add(byte(0), stream(snapshot,
		request(dbwire.KindGet, func(e *dbwire.Enc) { e.GetRequest(2, []byte("k")) }),
		request(dbwire.KindScan, func(e *dbwire.Enc) { e.ScanRequest(dbwire.ScanRequest{Table: 2, Limit: 1}) }),
		request(dbwire.KindScan, func(e *dbwire.Enc) {
			e.ScanRequest(dbwire.ScanRequest{Table: 2, HasAfter: true, After: []byte("k")})
		}),
		request(dbwire.KindObject, func(e *dbwire.Enc) { e.ObjectRequest(h) }),
		request(dbwire.KindEvents, nil), request(dbwire.KindVerify, nil), request(dbwire.KindRelease, nil),
		request(dbwire.KindBackup, nil), request(dbwire.KindOriginal, func(e *dbwire.Enc) { e.OriginalRequest(h) })))
	f.Add(byte(1), stream(snapshot,
		request(dbwire.KindPutObject, func(e *dbwire.Enc) { e.PutObjectRequest(h, []byte("object")) }),
		request(dbwire.KindObject, func(e *dbwire.Enc) { e.ObjectRequest(h) }),
		commit(put(2, "k", "v"), put(3, "h", "x"), event("e"), put(11, "activity", "line")),
		snapshot, commit(put(3, "h", "again"), event("e")), snapshot, commit(del(3, "h"), event("e")),
		commit(put(11, "bad key", "line"), event("e")), commit(event("\x00")), commit(put(0, "k", "v"), event("e")),
		commit(), request(dbwire.KindCommit, nil)))
	f.Add(byte(1), stream(snapshot, request(dbwire.KindPutOriginal, func(e *dbwire.Enc) {
		c := e.Chunks()
		_, _ = c.Write([]byte("original"))
		_ = c.Close()
		e.PutOriginalTrailer(hashOf("original"), 8)
	})))
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
		request(dbwire.KindBackup, nil)))
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
		})
		r := dbwire.RoleReader
		if role&1 == 1 {
			r = dbwire.RoleWriter
		}
		cc, sc := net.Pipe()
		done := make(chan struct{})
		go func() {
			fx.srv.ServeConn(sc, r)
			close(done)
		}()
		drained := make(chan struct{})
		go func() {
			_, _ = io.Copy(io.Discard, cc)
			close(drained)
		}()
		_ = cc.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := cc.Write(helloBytes); err == nil {
			_, _ = cc.Write(data)
		}
		_ = cc.Close()
		select {
		case <-done:
		case <-time.After(waitLimit):
			t.Fatal("the session did not end")
		}
		<-drained
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
