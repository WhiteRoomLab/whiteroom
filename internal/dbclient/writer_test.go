package dbclient_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

// wantWriterBusy checks that the daemon has a writer, which is not the process of the test: a writer that connects is
// refused.
func wantWriterBusy(t *testing.T, f *fixture) {
	t.Helper()
	_, _, err := f.tryOpen(bg, dbwire.RoleWriter, dbclient.Options{DialWait: 120 * time.Millisecond})
	if re, ok := errors.AsType[*dbwire.RemoteError](err); !ok || re.Code != dbwire.CodeBusy ||
		!strings.Contains(err.Error(), "another process is connected as the writer") {
		t.Errorf("a second writer: %v, want the refusal that says that another process is the writer", err)
	}
}

// failAfterFirstBlock is a reader whose second Read ends ctx, as the context of a request ends in the middle of it.
type failAfterFirstBlock struct {
	cancel context.CancelFunc
	reads  int
}

func (r *failAfterFirstBlock) Read(p []byte) (int, error) {
	if r.reads++; r.reads == 2 {
		r.cancel()
	}
	if r.reads > 3 {
		return 0, io.EOF
	}
	return copy(p, strings.Repeat("o", len(p))), nil
}

// TestWriterConnectionKept: the daemon has no writer between the end of the connection of a writer and the next connection,
// and another process may take the place; so a client that ends its writer connection in an Update or a PutOriginal
// connects again before it returns, and goes on holding the place.
func TestWriterConnectionKept(t *testing.T) {
	cutDuringGet := func(rec *recorder, cancel context.CancelFunc, gone bool) {
		rec.after = func(_ *recConn, kind dbwire.Kind, _ int, last bool) {
			if kind != dbwire.KindGet || !last {
				return
			}
			if gone {
				rec.mu.Lock()
				rec.failDial = func(int) error { return errors.New("the daemon is gone") }
				rec.mu.Unlock()
			}
			cancel()
		}
	}
	// The Get is cut, or its reply has come already; either way the connection ends with the context.
	update := func(ctx context.Context, w *dbclient.Client) error {
		return w.Update(ctx, func(tx *dbclient.Tx) error {
			_, _, err := tx.Get(2, []byte("k"))
			return err
		})
	}

	t.Run("an Update whose context ends in a request", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{DialWait: 5 * time.Second})
		ctx, cancel := context.WithCancel(bg)
		defer cancel()
		cutDuringGet(rec, cancel, false)
		if err := update(ctx, w); !errors.Is(err, context.Canceled) {
			t.Fatalf("the Update: %v", err)
		}
		rec.after = nil
		// No write of w has come since: the client holds the place for itself.
		if got := rec.liveConns(); got != 1 {
			t.Errorf("%d connections are open, want the new one of the writer", got)
		}
		wantWriterBusy(t, f)
		commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "v") })
		wantRows(t, rows(t, w, 2, ""), "k=v")
	})

	t.Run("a PutOriginal whose context ends in the request", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{DialWait: 5 * time.Second})
		ctx, cancel := context.WithCancel(bg)
		defer cancel()
		if _, _, err := w.PutOriginal(ctx, &failAfterFirstBlock{cancel: cancel}); !errors.Is(err, context.Canceled) {
			t.Fatalf("the PutOriginal: %v", err)
		}
		if got := rec.liveConns(); got != 1 {
			t.Errorf("%d connections are open, want the new one of the writer", got)
		}
		wantWriterBusy(t, f)
		if _, _, err := w.PutOriginal(bg, strings.NewReader("an original")); err != nil {
			t.Errorf("the next PutOriginal: %v", err)
		}
	})

	// The daemon ends the session of a writer after it has refused an original whose temporary file it could not write.
	failTemp := func(f *fixture) {
		f.fs.Inject(simfs.Fault{Match: func(op simfs.Op) bool {
			return op.Kind == simfs.OpWrite && strings.Contains(op.Path, ".tmp-")
		}, Err: syscall.EIO, Times: 1})
	}
	wantRefused := func(t *testing.T, err error) {
		t.Helper()
		if re, ok := errors.AsType[*dbwire.RemoteError](err); !ok || re.Code != dbwire.CodeFailed || errors.Is(err, dbclient.ErrUnavailable) {
			t.Fatalf("the PutOriginal: %v, want the refusal of the daemon, with the code failed", err)
		}
	}

	t.Run("a PutOriginal that the daemon refuses, and ends the session", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{DialWait: 5 * time.Second})
		failTemp(f)
		_, _, err := w.PutOriginal(bg, strings.NewReader("an original"))
		wantRefused(t, err)
		if got := rec.liveConns(); got != 1 {
			t.Errorf("%d connections are open, want the new one of the writer", got)
		}
		wantWriterBusy(t, f)
		if _, _, err := w.PutOriginal(bg, strings.NewReader("an original")); err != nil {
			t.Errorf("the next PutOriginal: %v", err)
		}
	})

	// Over socket files, an original that is larger than the buffers of a socket is refused while the client is sending it:
	// the write fails, and the reply of the daemon says why.
	t.Run("a PutOriginal that the daemon refuses while it is sent", func(t *testing.T) {
		f := newFixture(t, nil)
		dir, err := os.MkdirTemp("", "wr") // the path of a socket is short
		must(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		must(t, f.root.Listen(dir))
		open := func(wait time.Duration) (*dbclient.Client, error) {
			return dbclient.Open(bg, dbclient.SocketDir(dir), dbwire.RoleWriter, dbclient.Options{DialWait: wait})
		}
		w, err := open(5 * time.Second)
		must(t, err)
		t.Cleanup(func() { _ = w.Close() })
		seed(t, w)
		failTemp(f)
		_, _, err = w.PutOriginal(bg, strings.NewReader(strings.Repeat("o", 4<<20)))
		wantRefused(t, err)
		second, err := open(120 * time.Millisecond)
		if err == nil {
			_ = second.Close()
		}
		if err == nil || !strings.HasSuffix(err.Error(), ": another process is connected as the writer") {
			t.Errorf("a second writer: %v, want the refusal that says that another process is the writer", err)
		}
		if _, _, err := w.PutOriginal(bg, strings.NewReader("an original")); err != nil {
			t.Errorf("the next PutOriginal: %v", err)
		}
	})

	t.Run("the daemon is away", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{DialWait: 100 * time.Millisecond})
		ctx, cancel := context.WithCancel(bg)
		defer cancel()
		cutDuringGet(rec, cancel, true)
		if err := update(ctx, w); !errors.Is(err, context.Canceled) {
			t.Fatalf("the Update: %v", err)
		}
		if got := rec.liveConns(); got != 0 {
			t.Errorf("%d connections are open while the daemon is away, want none", got)
		}
		// The next write connects, as it did before.
		rec.mu.Lock()
		rec.after, rec.failDial = nil, nil
		rec.mu.Unlock()
		commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "v") })
		wantRows(t, rows(t, w, 2, ""), "k=v")
		if got := rec.liveConns(); got < 1 {
			t.Errorf("%d connections are open after the next Update, want the writer's", got)
		}
	})

	t.Run("the daemon goes away between two writes", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{DialWait: -1})
		rec.mu.Lock()
		rec.failDial = func(int) error { return errors.New("the daemon is gone") }
		dials := rec.attempts
		rec.mu.Unlock()
		rec.cutAll()
		if err := update(bg, w); !errors.Is(err, dbclient.ErrUnavailable) {
			t.Fatalf("the Update: %v, want an error that is ErrUnavailable", err)
		}
		rec.mu.Lock()
		defer rec.mu.Unlock()
		// A connection that could not be made is left to the next write: the Update does not wait for the daemon twice.
		if got := rec.attempts - dials; got != 1 {
			t.Errorf("%d attempts to connect, want 1", got)
		}
	})

	t.Run("a client that is closed", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{DialWait: 5 * time.Second})
		ctx, cancel := context.WithCancel(bg)
		defer cancel()
		rec.after = func(_ *recConn, kind dbwire.Kind, _ int, last bool) {
			if kind == dbwire.KindGet && last {
				must(t, w.Close())
				cancel()
			}
		}
		rec.mu.Lock()
		dials := rec.attempts
		rec.mu.Unlock()
		if err := update(ctx, w); err == nil {
			t.Fatal("the Update of a client that is closed succeeded")
		}
		rec.mu.Lock()
		defer rec.mu.Unlock()
		if rec.attempts != dials || rec.live != 0 {
			t.Errorf("%d attempts to connect and %d connections open after Close, want none", rec.attempts-dials, rec.live)
		}
	})
}

// TestLostCommitOtherWriter: a Commit whose connection ends is made if the store has one commit more than the snapshot of the
// Update when the client connects again, and not made if it has none (TestLostCommitReply); with more than one it is not
// known, as the commits of another writer are among them.
func TestLostCommitOtherWriter(t *testing.T) {
	f := newFixture(t, nil)
	a, rec := f.writer(dbclient.Options{DialWait: 5 * time.Second})
	commit(t, a, func(tx *dbclient.Tx) { put(t, tx, 2, "base", "0") })
	snapshot := f.seq()
	// The connection of a ends before a byte of its Commit is sent. Another writer, another process, connects in the gap,
	// commits twice and leaves.
	rec.before = func(c *recConn, kind dbwire.Kind, frame int, _ bool) error {
		if kind != dbwire.KindCommit || frame != 0 {
			return nil
		}
		rec.mu.Lock()
		rec.before = nil
		rec.mu.Unlock()
		_ = c.Close()
		b, _ := f.open(dbwire.RoleWriter, dbclient.Options{DialWait: 5 * time.Second})
		commit(t, b, func(tx *dbclient.Tx) { put(t, tx, 2, "other1", "1") })
		commit(t, b, func(tx *dbclient.Tx) { put(t, tx, 2, "other2", "2") })
		must(t, b.Close())
		return errors.New("the connection is cut")
	}
	err := a.Update(bg, func(tx *dbclient.Tx) error {
		put(t, tx, 2, "mine", "1")
		return tx.AppendEvent([]byte(`{"e":1}`))
	})
	want := fmt.Sprintf("the last commit is %d after the snapshot at %d", snapshot+2, snapshot)
	if !errors.Is(err, dbclient.ErrUnavailable) || err == nil ||
		!strings.Contains(err.Error(), "it is not known whether the commit was made") || !strings.Contains(err.Error(), want) {
		t.Errorf("the Update: %v, want an error that says that it is not known whether the commit was made: %s", err, want)
	}
	wantRows(t, rows(t, a, 2, ""), "base=0", "other1=1", "other2=2")
	// The client goes on.
	commit(t, a, func(tx *dbclient.Tx) { put(t, tx, 2, "next", "1") })
}
