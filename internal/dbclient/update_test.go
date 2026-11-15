package dbclient_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver/dbtest"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

func (f *fixture) state() state {
	f.t.Helper()
	s, err := readState(f.engine())
	must(f.t, err)
	return s
}

// seq returns the sequence number of the last commit of the daemon.
func (f *fixture) seq() uint64 {
	f.t.Helper()
	var seq uint64
	must(f.t, f.engine().View(bg, func(tx *logdb.Tx) error { seq = tx.Seq(); return nil }))
	return seq
}

var errTest = errors.New("an error of the test")

func TestView(t *testing.T) {
	t.Run("the snapshot is taken at the first read", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "1") })
		var seen []string
		rec.reset()
		must(t, w.View(bg, func(tx *dbclient.Tx) error {
			wantRequests(t, rec) // the View has asked for nothing yet
			commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "2") })
			v, _ := get(t, tx, 2, "k")
			seen = append(seen, v)
			commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "3") })
			v, _ = get(t, tx, 2, "k")
			seen = append(seen, v)
			if seq := tx.Seq(); seq != 3 {
				t.Errorf("Seq is %d, want 3", seq)
			}
			return tx.Scan(2, nil, func(k, v []byte) bool {
				seen = append(seen, string(k)+"="+string(v))
				return true
			})
		}))
		if want := []string{"2", "2", "k=2"}; !reflect.DeepEqual(seen, want) {
			t.Errorf("the View saw %q, want %q", seen, want)
		}
		wantRows(t, rows(t, w, 2, ""), "k=3")
	})

	t.Run("Seq takes the snapshot", func(t *testing.T) {
		f := newFixture(t, nil)
		w, _ := f.writer(dbclient.Options{})
		must(t, w.View(bg, func(tx *dbclient.Tx) error {
			commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "1") })
			if seq := tx.Seq(); seq != 2 {
				t.Errorf("Seq is %d, want 2", seq)
			}
			commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "2") })
			if v, _ := get(t, tx, 2, "k"); v != "1" {
				t.Errorf("Get gave %q", v)
			}
			return nil
		}))
	})

	t.Run("a View that reads nothing sends nothing", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		must(t, w.View(bg, func(*dbclient.Tx) error { return nil }))
		wantRequests(t, rec)
		if rec.dialed(dbwire.RoleReader) != 0 {
			t.Error("the View dialed")
		}
	})

	t.Run("the writes of a View fail", func(t *testing.T) {
		f := newFixture(t, nil)
		w, _ := f.writer(dbclient.Options{})
		commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "1") })
		before := f.state()
		must(t, w.View(bg, func(tx *dbclient.Tx) error {
			for name, err := range map[string]error{
				"Put":         tx.Put(2, []byte("a"), []byte("1")),
				"Delete":      tx.Delete(2, []byte("k")),
				"PutObject":   tx.PutObject([32]byte{}, nil),
				"AppendEvent": tx.AppendEvent([]byte("e")),
			} {
				if !errors.Is(err, dbclient.ErrReadOnly) {
					t.Errorf("%s: %v", name, err)
				}
			}
			return nil
		}))
		if after := f.state(); !reflect.DeepEqual(before, after) {
			t.Error("a View changed the store")
		}
	})

	t.Run("the result of f", func(t *testing.T) {
		f := newFixture(t, nil)
		w, _ := f.writer(dbclient.Options{})
		if err := w.View(bg, func(tx *dbclient.Tx) error { get(t, tx, 2, "k"); return errTest }); !errors.Is(err, errTest) {
			t.Errorf("got %v", err)
		}
		ctx, cancel := context.WithCancel(bg)
		if err := w.View(ctx, func(tx *dbclient.Tx) error { cancel(); return nil }); !errors.Is(err, context.Canceled) {
			t.Errorf("a context that ended in f: %v", err)
		}
		if err := w.View(ctx, func(*dbclient.Tx) error { t.Error("f was called"); return nil }); !errors.Is(err, context.Canceled) {
			t.Errorf("a context that has ended: %v", err)
		}
	})
}

// wantEnded fails the test unless every method of tx fails as that of a transaction that has ended.
func wantEnded(t *testing.T, tx *dbclient.Tx) {
	t.Helper()
	const want = "the transaction has ended"
	check := func(name string, err error) {
		if err == nil || err.Error() != want {
			t.Errorf("%s after the end: got %v", name, err)
		}
	}
	if seq := tx.Seq(); seq != 0 {
		t.Errorf("Seq after the end is %d", seq)
	}
	_, _, err := tx.Get(2, []byte("k"))
	check("Get", err)
	check("Scan", tx.Scan(2, nil, func(_, _ []byte) bool { t.Error("Scan called fn after the end"); return true }))
	check("Put", tx.Put(2, []byte("k"), nil))
	check("Delete", tx.Delete(2, []byte("k")))
	check("PutObject", tx.PutObject([32]byte{}, nil))
	_, _, err = tx.Object([32]byte{})
	check("Object", err)
	check("AppendEvent", tx.AppendEvent([]byte("e")))
	check("Events", tx.Events(bg, func(uint64, []byte) bool { t.Error("Events called fn after the end"); return true }))
}

func TestTxAfterTheEnd(t *testing.T) {
	f := newFixture(t, nil)
	w, _ := f.writer(dbclient.Options{})
	var inView, inUpdate *dbclient.Tx
	must(t, w.View(bg, func(tx *dbclient.Tx) error { inView = tx; get(t, tx, 2, "k"); return nil }))
	wantEnded(t, inView)
	must(t, w.Update(bg, func(tx *dbclient.Tx) error { inUpdate = tx; return nil }))
	wantEnded(t, inUpdate)
	// The connection that they had is used by others by now.
	commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "v") })
	wantEnded(t, inView)
	wantEnded(t, inUpdate)
	wantRows(t, rows(t, w, 2, ""), "k=v")
}

func TestUpdate(t *testing.T) {
	t.Run("an error of f commits nothing", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "base", "0") })
		before := f.state()
		data := []byte(strings.Repeat("an object that is not kept ", 10))
		rec.reset()
		err := w.Update(bg, func(tx *dbclient.Tx) error {
			put(t, tx, 2, "k", "v")
			del(t, tx, 2, "base")
			must(t, tx.PutObject(sha256.Sum256(data), data))
			event(t, tx, `{"e":1}`)
			return errTest
		})
		if !errors.Is(err, errTest) {
			t.Fatalf("got %v", err)
		}
		// The snapshot is released, and with it what the daemon staged.
		wantRequests(t, rec, dbwire.KindSnapshot, dbwire.KindGet, dbwire.KindPutObject, dbwire.KindRelease)
		if after := f.state(); !reflect.DeepEqual(before, after) {
			t.Errorf("the store changed:\n before %+v\n after  %+v", before, after)
		}
		// The next Update does not find the object that was put.
		rec.reset()
		commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "k2", "v2") })
		wantRequests(t, rec, dbwire.KindSnapshot, dbwire.KindCommit)
		if got := f.state(); len(got.Objects) != 0 {
			t.Errorf("the store has the objects %v", got.Objects)
		}
		if rec.dialed(dbwire.RoleWriter) != 1 {
			t.Error("the writer connected again")
		}
	})

	t.Run("no op, no Commit", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "v") })
		seq := f.state().Seq
		for _, c := range []struct {
			name string
			f    func(tx *dbclient.Tx)
			want []dbwire.Kind
		}{
			{"nothing", func(*dbclient.Tx) {}, []dbwire.Kind{dbwire.KindSnapshot, dbwire.KindRelease}},
			{"reads", func(tx *dbclient.Tx) { get(t, tx, 2, "k"); get(t, tx, 2, "x") },
				[]dbwire.Kind{dbwire.KindSnapshot, dbwire.KindGet, dbwire.KindGet, dbwire.KindRelease}},
			{"a delete of a missing key", func(tx *dbclient.Tx) { del(t, tx, 2, "missing"); del(t, tx, 2, "missing") },
				[]dbwire.Kind{dbwire.KindSnapshot, dbwire.KindGet, dbwire.KindRelease}},
		} {
			rec.reset()
			must(t, w.Update(bg, func(tx *dbclient.Tx) error { c.f(tx); return nil }))
			wantRequests(t, rec, c.want...)
			if got := f.state().Seq; got != seq {
				t.Errorf("%s: the store is at %d, was at %d", c.name, got, seq)
			}
		}
	})

	t.Run("one at a time", func(t *testing.T) {
		f := newFixture(t, nil)
		w, _ := f.writer(dbclient.Options{})
		inA, releaseA := make(chan struct{}), make(chan struct{})
		var order []string
		var mu sync.Mutex
		note := func(s string) {
			mu.Lock()
			order = append(order, s)
			mu.Unlock()
		}
		done := make(chan error, 2)
		go func() {
			done <- w.Update(bg, func(tx *dbclient.Tx) error {
				note("A starts")
				close(inA)
				<-releaseA
				put(t, tx, 2, "k", "A")
				note("A ends")
				return tx.AppendEvent([]byte(`{"a":1}`))
			})
		}()
		<-inA
		started := make(chan struct{})
		go func() {
			done <- w.Update(bg, func(tx *dbclient.Tx) error {
				note("B starts")
				close(started)
				if v, _ := get(t, tx, 2, "k"); v != "A" {
					t.Errorf("B saw %q: it began before A ended", v)
				}
				put(t, tx, 2, "k", "B")
				return tx.AppendEvent([]byte(`{"b":1}`))
			})
		}()
		select {
		case <-started:
			t.Error("B began while A ran")
		case <-time.After(50 * time.Millisecond):
		}
		close(releaseA)
		for range 2 {
			must(t, <-done)
		}
		if want := []string{"A starts", "A ends", "B starts"}; !reflect.DeepEqual(order, want) {
			t.Errorf("order %q, want %q", order, want)
		}
		wantRows(t, rows(t, w, 2, ""), "k=B")
	})

	t.Run("a waiting Update ends with its context", func(t *testing.T) {
		f := newFixture(t, nil)
		w, _ := f.writer(dbclient.Options{})
		inA, releaseA := make(chan struct{}), make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- w.Update(bg, func(*dbclient.Tx) error {
				close(inA)
				<-releaseA
				return nil
			})
		}()
		<-inA
		ctx, cancel := context.WithTimeout(bg, 30*time.Millisecond)
		defer cancel()
		err := w.Update(ctx, func(*dbclient.Tx) error { t.Error("f was called"); return nil })
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("got %v", err)
		}
		close(releaseA)
		must(t, <-done)
		commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "v") }) // the turn is free
	})

	t.Run("the order of the calls", func(t *testing.T) {
		f := newFixture(t, nil)
		w, _ := f.writer(dbclient.Options{})
		must(t, w.Update(bg, func(tx *dbclient.Tx) error {
			event(t, tx, "e1")
			put(t, tx, 2, "a", "1")
			event(t, tx, "e2")
			del(t, tx, 2, "a")
			put(t, tx, 2, "a", "2")
			put(t, tx, 2, "b", "1")
			event(t, tx, "e3")
			del(t, tx, 2, "b")
			return nil
		}))
		var got []string
		for _, ev := range f.state().Events {
			if !dbwire.IsDaemonEvent(ev.Data) && ev.Seq == 2 {
				got = append(got, string(ev.Data))
			}
		}
		if want := []string{"e1", "e2", "e3"}; !reflect.DeepEqual(got, want) {
			t.Errorf("the events %q, want %q", got, want)
		}
		wantRows(t, rows(t, w, 2, ""), "a=2")
	})

	t.Run("the context ends in f", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		before := f.state()
		ctx, cancel := context.WithCancel(bg)
		rec.reset()
		err := w.Update(ctx, func(tx *dbclient.Tx) error {
			put(t, tx, 2, "k", "v")
			event(t, tx, `{"e":1}`)
			cancel()
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("got %v", err)
		}
		wantRequests(t, rec, dbwire.KindSnapshot, dbwire.KindRelease)
		if after := f.state(); !reflect.DeepEqual(before, after) {
			t.Error("the store changed")
		}
		commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "v") })
		if rec.dialed(dbwire.RoleWriter) != 1 {
			t.Error("the connection was not kept")
		}
	})

	t.Run("a panic in f", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{DialWait: 5 * time.Second})
		func() {
			defer func() {
				if recover() == nil {
					t.Error("the panic was swallowed")
				}
			}()
			_ = w.Update(bg, func(tx *dbclient.Tx) error {
				put(t, tx, 2, "k", "lost")
				panic("a panic of the test")
			})
		}()
		commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "v") })
		wantRows(t, rows(t, w, 2, ""), "k=v")
		// The new connection may be refused for a moment, until the session of the old one has ended.
		if rec.dialed(dbwire.RoleWriter) < 2 {
			t.Errorf("%d connections of the writer: the one of the panic is not reused", rec.dialed(dbwire.RoleWriter))
		}
		must(t, w.View(bg, func(tx *dbclient.Tx) error {
			func() {
				defer func() { _ = recover() }()
				_ = w.View(bg, func(tx *dbclient.Tx) error { get(t, tx, 2, "k"); panic("a panic of the test") })
			}()
			return nil
		}))
		wantRows(t, rows(t, w, 2, ""), "k=v")
	})
}

func TestCommitRefused(t *testing.T) {
	var hook atomic.Pointer[func() error]
	f := newFixture(t, func(o *dbtest.Options) {
		o.Hook = func(point string) error {
			if h := hook.Load(); point == "commit:gate" && h != nil {
				return (*h)()
			}
			return nil
		}
	})
	w, rec := f.writer(dbclient.Options{})
	commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 3, "h1", "v") })
	before := f.state()

	t.Run("a rule of the daemon", func(t *testing.T) {
		err := w.Update(bg, func(tx *dbclient.Tx) error {
			put(t, tx, 2, "k", "v") // and no event
			return nil
		})
		re := remote(t, err)
		if re.Code != dbwire.CodeRule || err.Error() != "a commit that puts or deletes a row needs an audit event" {
			t.Errorf("got %v: %v", re.Code, err)
		}
		err = w.Update(bg, func(tx *dbclient.Tx) error {
			put(t, tx, 3, "h1", "other")
			return tx.AppendEvent([]byte(`{"e":1}`))
		})
		if re := remote(t, err); re.Code != dbwire.CodeRule || !strings.Contains(re.Msg, "append-only") {
			t.Errorf("got %v: %v", re.Code, err)
		}
		if after := f.state(); !reflect.DeepEqual(before, after) {
			t.Error("a refused commit changed the store")
		}
		// The client goes on, on the same connection.
		commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "v") })
		if rec.dialed(dbwire.RoleWriter) != 1 {
			t.Error("the writer connected again")
		}
	})

	t.Run("the store has changed", func(t *testing.T) {
		engine := dbserver.EngineOf(f.root.Server())
		var once sync.Once
		fn := func() error {
			once.Do(func() {
				// A commit that does not come from this client, between its snapshot and its commit.
				must(t, engine.Update(bg, func(tx *logdb.Tx) error { return tx.Put(2, []byte("x"), []byte("y")) }))
			})
			return nil
		}
		hook.Store(&fn)
		defer hook.Store(nil)
		err := w.Update(bg, func(tx *dbclient.Tx) error {
			put(t, tx, 2, "k", "w")
			return tx.AppendEvent([]byte(`{"e":2}`))
		})
		re := remote(t, err)
		if re.Code != dbwire.CodeSeq || !strings.HasPrefix(err.Error(), "the store has changed since the transaction began: ") {
			t.Errorf("got %v: %v", re.Code, err)
		}
		hook.Store(nil)
		commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "w") })
		wantRows(t, rows(t, w, 2, ""), "k=w", "x=y")
	})

	t.Run("an object with another hash", func(t *testing.T) {
		data := []byte("an object")
		must(t, w.Update(bg, func(tx *dbclient.Tx) error {
			err := tx.PutObject(sha256.Sum256([]byte("another")), data)
			if re := remote(t, err); re.Code != dbwire.CodeHash {
				t.Errorf("got %v: %v", re.Code, err)
			}
			// The transaction goes on, and the object was not kept.
			put(t, tx, 2, "k", "after")
			if _, found, err := tx.Object(sha256.Sum256(data)); err != nil || found {
				t.Errorf("Object: %v, %v", found, err)
			}
			return tx.AppendEvent([]byte(`{"e":3}`))
		}))
		wantRows(t, rows(t, w, 2, "k"), "k=after")
		if got := f.state().Objects; len(got) != 0 {
			t.Errorf("the store has the objects %v", got)
		}
	})
}

// TestLostCommitReply cuts the connection around a Commit, as a daemon that is killed does, and asks what Update says.
func TestLostCommitReply(t *testing.T) {
	big := strings.Repeat("v", 3*testFrame)
	run := func(t *testing.T, o dbclient.Options, setup func(rec *recorder, cancel context.CancelFunc)) (*fixture, *dbclient.Client, *recorder, error) {
		f := newFixture(t, nil)
		w, rec := f.writer(o)
		commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "base", "0") })
		ctx, cancel := context.WithCancel(bg)
		defer cancel()
		setup(rec, cancel)
		err := w.Update(ctx, func(tx *dbclient.Tx) error {
			put(t, tx, 2, "k", big)
			return tx.AppendEvent([]byte(`{"e":1}`))
		})
		return f, w, rec, err
	}
	cutAfterCommit := func(rec *recorder, _ context.CancelFunc) {
		rec.after = func(c *recConn, kind dbwire.Kind, _ int, last bool) {
			if kind == dbwire.KindCommit && last {
				_ = c.Close()
			}
		}
	}
	wantMade := func(t *testing.T, f *fixture, w *dbclient.Client, rec *recorder, made bool) {
		t.Helper()
		rec.after, rec.before, rec.failDial = nil, nil, nil
		want := []string{"base=0"}
		if made {
			want = []string{"base=0", "k=" + big}
		}
		wantRows(t, rows(t, w, 2, ""), want...)
		marks := 0
		for _, ev := range f.state().Events {
			if _, ok := dbwire.ParseMark(ev.Data); ok {
				marks++
			}
		}
		commits := 2
		if made {
			commits = 3
		}
		if got := f.state().Seq; got != uint64(commits) || marks != commits {
			t.Errorf("the store is at %d with %d marks, want %d", got, marks, commits)
		}
		// The client goes on.
		commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "next", "1") })
	}

	t.Run("the reply is lost", func(t *testing.T) {
		f, w, rec, err := run(t, dbclient.Options{DialWait: 5 * time.Second}, cutAfterCommit)
		must(t, err)
		// The new connection may be refused for a moment, until the session of the old one has ended.
		if rec.dialed(dbwire.RoleWriter) < 2 {
			t.Errorf("%d connections of the writer", rec.dialed(dbwire.RoleWriter))
		}
		wantMade(t, f, w, rec, true)
	})

	t.Run("the request is cut", func(t *testing.T) {
		f, w, rec, err := run(t, dbclient.Options{DialWait: 5 * time.Second}, func(rec *recorder, _ context.CancelFunc) {
			rec.before = func(_ *recConn, kind dbwire.Kind, frame int, _ bool) error {
				if kind == dbwire.KindCommit && frame == 1 {
					return errors.New("the connection is cut")
				}
				return nil
			}
		})
		if !errors.Is(err, dbclient.ErrUnavailable) || !strings.Contains(err.Error(), "the transaction was not committed") {
			t.Errorf("got %v", err)
		}
		wantMade(t, f, w, rec, false)
	})

	t.Run("the daemon is gone", func(t *testing.T) {
		f, w, rec, err := run(t, dbclient.Options{DialWait: 100 * time.Millisecond}, func(rec *recorder, cancel context.CancelFunc) {
			rec.after = func(c *recConn, kind dbwire.Kind, _ int, last bool) {
				if kind == dbwire.KindCommit && last {
					rec.mu.Lock()
					rec.failDial = func(int) error { return errors.New("the daemon is gone") }
					rec.mu.Unlock()
					_ = c.Close()
				}
			}
		})
		if !errors.Is(err, dbclient.ErrUnavailable) || !strings.Contains(err.Error(), "it is not known whether the commit was made") {
			t.Errorf("got %v", err)
		}
		if strings.Contains(err.Error(), "was not committed") {
			t.Errorf("the error says that the commit was not made: %v", err)
		}
		wantMade(t, f, w, rec, true) // it was: the daemon had the request
	})

	t.Run("the context ends", func(t *testing.T) {
		f, w, rec, err := run(t, dbclient.Options{DialWait: 5 * time.Second}, func(rec *recorder, cancel context.CancelFunc) {
			rec.after = func(_ *recConn, kind dbwire.Kind, _ int, last bool) {
				if kind == dbwire.KindCommit && last {
					cancel()
				}
			}
		})
		// The answer of the daemon, which the client asks for with a context of its own, is that it was made.
		if err != nil {
			t.Errorf("got %v", err)
		}
		wantMade(t, f, w, rec, true)
	})

	t.Run("the context ends and the daemon is gone", func(t *testing.T) {
		f, w, rec, err := run(t, dbclient.Options{DialWait: 100 * time.Millisecond}, func(rec *recorder, cancel context.CancelFunc) {
			rec.after = func(c *recConn, kind dbwire.Kind, _ int, last bool) {
				if kind == dbwire.KindCommit && last {
					rec.mu.Lock()
					rec.failDial = func(int) error { return errors.New("the daemon is gone") }
					rec.mu.Unlock()
					cancel()
					_ = c.Close() // else the reply may come before the context is looked at
				}
			}
		})
		if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "it is not known whether the commit was made") {
			t.Errorf("got %v", err)
		}
		wantMade(t, f, w, rec, true)
	})
}

// switchDialer connects to the daemon that it has now: a test makes another one in its place.
type switchDialer struct {
	mu  sync.Mutex
	cur *dbtest.Daemon
}

func (s *switchDialer) set(d *dbtest.Daemon) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cur = d
}

func (s *switchDialer) Dial(ctx context.Context, role dbwire.Role) (net.Conn, error) {
	s.mu.Lock()
	d := s.cur
	s.mu.Unlock()
	return d.Dialer().Dial(ctx, role)
}

func (s *switchDialer) String() string { return "switch" }

func (s *switchDialer) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur.Release()
}

func TestReconnect(t *testing.T) {
	noLeaks(t)
	fsys := simfs.New("data")
	opts := testOptions(fsys)
	must(t, dbtest.Init("data", opts))
	acquire := func() *dbtest.Daemon {
		d, err := dbtest.Acquire("data", opts)
		must(t, err)
		return d
	}
	first := acquire()
	sw := &switchDialer{}
	sw.set(first)
	rec := &recorder{Dialer: sw}
	c, err := dbclient.Open(bg, rec, dbwire.RoleWriter, dbclient.Options{DialWait: 5 * time.Second})
	must(t, err)
	t.Cleanup(func() { _ = c.Close() })
	seed(t, c)
	commit(t, c, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "1") })
	wantRows(t, rows(t, c, 2, ""), "k=1")

	restart := func(d *dbtest.Daemon) *dbtest.Daemon {
		must(t, d.Release())
		next := acquire()
		sw.set(next)
		return next
	}
	// The connections that the client keeps are of the daemon that is gone: a View and an Update go on without an error.
	d := restart(first)
	wantRows(t, rows(t, c, 2, ""), "k=1")
	d = restart(d)
	commit(t, c, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "2") })
	d = restart(d)
	must(t, c.Update(bg, func(tx *dbclient.Tx) error {
		if v, _ := get(t, tx, 2, "k"); v != "2" {
			t.Errorf("the Update read %q", v)
		}
		put(t, tx, 2, "k", "3")
		return tx.AppendEvent([]byte(`{"e":1}`))
	}))
	wantRows(t, rows(t, c, 2, ""), "k=3")
	_ = d
	if got := rec.dialed(dbwire.RoleWriter); got != 3 {
		t.Errorf("%d connections of the writer, want one for each daemon", got)
	}
}

func TestPool(t *testing.T) {
	f := newFixture(t, nil)
	w, rec := f.writer(dbclient.Options{Readers: 2})
	commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "v") })
	read := func() {
		must(t, w.View(bg, func(tx *dbclient.Tx) error { get(t, tx, 2, "k"); return nil }))
	}
	for range 10 {
		read()
	}
	if got := rec.dialed(dbwire.RoleReader); got != 1 {
		t.Errorf("ten Views one after the other dialed %d times", got)
	}

	started, hold := make(chan struct{}, 2), make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			must(t, w.View(bg, func(tx *dbclient.Tx) error {
				get(t, tx, 2, "k")
				started <- struct{}{}
				<-hold
				return nil
			}))
		}()
	}
	<-started
	<-started
	ctx, cancel := context.WithTimeout(bg, 30*time.Millisecond)
	defer cancel()
	if err := w.View(ctx, func(*dbclient.Tx) error { t.Error("f was called"); return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a third View: %v", err)
	}
	third := make(chan struct{})
	go func() {
		defer close(third)
		read()
	}()
	select {
	case <-third:
		t.Error("a third View ran while two had the connections")
	case <-time.After(30 * time.Millisecond):
	}
	close(hold)
	wg.Wait()
	<-third
	for range 10 {
		read()
	}
	if got := rec.dialed(dbwire.RoleReader); got != 2 {
		t.Errorf("%d connections of the readers, want 2", got)
	}
	if got := rec.liveConns(); got != 3 {
		t.Errorf("%d connections are open, want the writer's and two of the readers", got)
	}
}

func TestContext(t *testing.T) {
	t.Run("in the middle of a Scan", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		commit(t, w, func(tx *dbclient.Tx) {
			for i := range 300 {
				put(t, tx, 2, "k"+string(rune('0'+i/100))+string(rune('0'+i/10%10))+string(rune('0'+i%10)), strings.Repeat("v", 60))
			}
		})
		ctx, cancel := context.WithCancel(bg)
		n := 0
		err := w.View(ctx, func(tx *dbclient.Tx) error {
			return tx.Scan(2, nil, func(_, _ []byte) bool {
				if n++; n == 10 {
					cancel()
				}
				return true
			})
		})
		if !errors.Is(err, context.Canceled) || n != 10 {
			t.Errorf("got %v after %d rows", err, n)
		}
		rec.reset()
		wantRows(t, rows(t, w, 2, "k00")[:1], "k000="+strings.Repeat("v", 60))
	})

	t.Run("in the middle of a request", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		commit(t, w, func(tx *dbclient.Tx) {
			for i := range 300 {
				put(t, tx, 2, "k"+string(rune('0'+i/100))+string(rune('0'+i/10%10))+string(rune('0'+i%10)), strings.Repeat("v", 60))
			}
		})
		must(t, w.View(bg, func(tx *dbclient.Tx) error { get(t, tx, 2, "k000"); return nil })) // a reader connection is idle
		ctx, cancel := context.WithCancel(bg)
		rec.before = func(_ *recConn, kind dbwire.Kind, frame int, _ bool) error {
			if kind == dbwire.KindScan && rec.count(dbwire.KindScan) == 2 && frame == 0 {
				cancel() // the second page of the Scan
			}
			return nil
		}
		err := w.View(ctx, func(tx *dbclient.Tx) error {
			return tx.Scan(2, nil, func(_, _ []byte) bool { return true })
		})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("got %v", err)
		}
		rec.before = nil
		// The connection that was cut is not used again, and not kept.
		if got := rec.liveConns(); got != 1 {
			t.Errorf("%d connections are open, want the writer's", got)
		}
		dials := rec.dialed(dbwire.RoleReader)
		wantRows(t, rows(t, w, 2, "k00")[:1], "k000="+strings.Repeat("v", 60))
		if got := rec.dialed(dbwire.RoleReader); got != dials+1 {
			t.Errorf("the next View dialed %d times", got-dials)
		}
	})

	t.Run("a Commit that hangs", func(t *testing.T) {
		gate := make(chan struct{})
		var blocked atomic.Bool
		f := newFixture(t, func(o *dbtest.Options) {
			o.Hook = func(point string) error {
				if point == "commit:gate" && blocked.Load() {
					<-gate
				}
				return nil
			}
		})
		w, _ := f.writer(dbclient.Options{DialWait: 100 * time.Millisecond})
		blocked.Store(true)
		ctx, cancel := context.WithTimeout(bg, 50*time.Millisecond)
		defer cancel()
		err := w.Update(ctx, func(tx *dbclient.Tx) error {
			put(t, tx, 2, "k", "v")
			return tx.AppendEvent([]byte(`{"e":1}`))
		})
		close(gate)
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "it is not known whether the commit was made") {
			t.Errorf("got %v", err)
		}
		// The daemon went on with the commit.
		eventually(t, "the commit", func() bool { return len(f.state().Tables[2]) == 1 })
		commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "w") })
	})
}

func TestConcurrent(t *testing.T) {
	f := newFixture(t, nil)
	w, rec := f.writer(dbclient.Options{Readers: 3})
	const workers, rounds = 6, 12
	var wg sync.WaitGroup
	for g := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range rounds {
				key := fmt.Sprintf("g%d-%02d", g, i)
				var err error
				switch (g + i) % 3 {
				case 0:
					err = w.Update(bg, func(tx *dbclient.Tx) error {
						if err := tx.Put(2, []byte(key), []byte("v")); err != nil {
							return err
						}
						return tx.AppendEvent([]byte(`{"c":1}`))
					})
				case 1:
					err = w.View(bg, func(tx *dbclient.Tx) error {
						return tx.Scan(2, []byte("g"), func(_, _ []byte) bool { return true })
					})
				default:
					err = w.View(bg, func(tx *dbclient.Tx) error {
						_, _, err := tx.Get(2, []byte(key))
						return err
					})
				}
				if err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	want := 0
	for g := range workers {
		for i := range rounds {
			if (g+i)%3 == 0 {
				want++
			}
		}
	}
	if got := len(rows(t, w, 2, "g")); got != want {
		t.Errorf("%d rows, want %d", got, want)
	}
	if got := rec.dialed(dbwire.RoleReader); got > 3 {
		t.Errorf("%d reader connections, want at most 3", got)
	}
}

func eventually(t testing.TB, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(waitLimit); !cond(); {
		if time.Now().After(deadline) {
			t.Fatalf("gave up waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}
