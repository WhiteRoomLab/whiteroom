package dbclient_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// fakeDialer connects to a server of a test, which speaks as the function serve says over a pipe.
type fakeDialer struct {
	serve func(rd *dbwire.Reader, wr *dbwire.Writer)

	mu     sync.Mutex
	dials  int
	closes int
	wg     sync.WaitGroup
}

func (d *fakeDialer) Dial(context.Context, dbwire.Role) (net.Conn, error) {
	d.mu.Lock()
	d.dials++
	d.mu.Unlock()
	cc, sc := net.Pipe()
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer func() { _ = sc.Close() }()
		d.serve(dbwire.NewReader(sc, 1<<20), dbwire.NewWriter(sc, 1<<20))
	}()
	return cc, nil
}

func (d *fakeDialer) String() string { return "fake" }

func (d *fakeDialer) Close() error {
	d.mu.Lock()
	d.closes++
	d.mu.Unlock()
	d.wg.Wait()
	return nil
}

// readHello reads the Hello that a client sends.
func readHello(t testing.TB, rd *dbwire.Reader) {
	if kind, err := rd.Next(); err != nil || kind != dbwire.KindHello {
		t.Errorf("the first message is %v, %v, want a Hello", kind, err)
	}
	_ = rd.Discard()
}

func TestOpen(t *testing.T) {
	t.Run("roles", func(t *testing.T) {
		f := newFixture(t, nil)
		w, wrec := f.open(dbwire.RoleWriter, dbclient.Options{})
		if wrec.dialed(dbwire.RoleWriter) != 1 || wrec.dialed(dbwire.RoleReader) != 0 {
			t.Errorf("a writer dialed %v", wrec.dials)
		}
		wantRequests(t, wrec, dbwire.KindHello)
		r, rrec := f.open(dbwire.RoleReader, dbclient.Options{})
		if rrec.dialed(dbwire.RoleReader) != 1 || rrec.dialed(dbwire.RoleWriter) != 0 {
			t.Errorf("a reader dialed %v", rrec.dials)
		}
		wantRequests(t, rrec, dbwire.KindHello)
		seed(t, w)
		if got := wrec.dialed(dbwire.RoleWriter); got != 1 {
			t.Errorf("the writer dialed the writer socket %d times", got)
		}
		// The writer reads its Views on the reader socket, the reader on the connection that it has.
		commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "v") })
		wantRows(t, rows(t, w, 2, ""), "k=v")
		wantRows(t, rows(t, r, 2, ""), "k=v")
		if wrec.dialed(dbwire.RoleReader) != 1 || rrec.dialed(dbwire.RoleReader) != 1 {
			t.Errorf("the dials of the Views: writer %v, reader %v", wrec.dials, rrec.dials)
		}
	})

	t.Run("a second writer", func(t *testing.T) {
		f := newFixture(t, nil)
		first, _ := f.writer(dbclient.Options{})
		_, rec, err := f.tryOpen(bg, dbwire.RoleWriter, dbclient.Options{DialWait: 120 * time.Millisecond})
		if want := "data: another process is connected as the writer"; err == nil || err.Error() != want {
			t.Fatalf("got %v, want %q", err, want)
		}
		if re := remote(t, err); re.Code != dbwire.CodeBusy {
			t.Errorf("got the code %v", re.Code)
		}
		if rec.attempts < 2 || rec.closes != 1 {
			t.Errorf("%d attempts and %d closes of the dialer", rec.attempts, rec.closes)
		}
		// It is let in as soon as the connection of the first one has ended, within the wait.
		must(t, first.Close())
		second, _, err := f.tryOpen(bg, dbwire.RoleWriter, dbclient.Options{DialWait: 5 * time.Second})
		must(t, err)
		commit(t, second, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "v") })
	})

	t.Run("a daemon of another version", func(t *testing.T) {
		for _, c := range []struct {
			name  string
			serve func(rd *dbwire.Reader, wr *dbwire.Writer)
			want  []string
		}{
			{"refused", func(rd *dbwire.Reader, wr *dbwire.Writer) {
				readHello(t, rd)
				_ = wr.Fail(dbwire.CodeVersion, "protocol version 1, but the daemon speaks version 2")
			}, []string{"version 1", "version 2"}},
			{"answered", func(rd *dbwire.Reader, wr *dbwire.Writer) {
				readHello(t, rd)
				wr.Begin(dbwire.KindOK)
				e := &dbwire.Enc{W: wr}
				e.HelloReply(dbwire.HelloReply{Version: 2, Role: dbwire.RoleWriter, Frame: 4096, MaxKey: 4096})
				_ = wr.End()
			}, []string{"version 2", "program 1"}},
		} {
			t.Run(c.name, func(t *testing.T) {
				noLeaks(t)
				d := &fakeDialer{serve: c.serve}
				_, err := dbclient.Open(bg, d, dbwire.RoleWriter, dbclient.Options{DialWait: 5 * time.Second})
				if err == nil {
					t.Fatal("Open succeeded")
				}
				for _, want := range c.want {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("got %q, want %q in it", err, want)
					}
				}
				if d.dials != 1 || d.closes != 1 {
					t.Errorf("%d dials and %d closes: a version is not tried again", d.dials, d.closes)
				}
			})
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		noLeaks(t)
		dir := t.TempDir()
		for _, c := range []struct {
			role   dbwire.Role
			socket string
		}{{dbwire.RoleWriter, dbwire.WriterSocket}, {dbwire.RoleReader, dbwire.ReaderSocket}} {
			rec := &recorder{Dialer: dbclient.SocketDir(dir)}
			start := time.Now()
			_, err := dbclient.Open(bg, rec, c.role, dbclient.Options{DialWait: 120 * time.Millisecond})
			if !errors.Is(err, dbclient.ErrUnavailable) {
				t.Fatalf("got %v, want ErrUnavailable", err)
			}
			if !strings.Contains(err.Error(), c.socket) || !strings.Contains(err.Error(), dir) {
				t.Errorf("the error does not name the socket and its directory: %v", err)
			}
			// The socket file is missing, which a caller that tests for a missing key or document must not take for one.
			if errors.Is(err, fs.ErrNotExist) {
				t.Errorf("the error of a missing socket file wraps fs.ErrNotExist: %v", err)
			}
			if elapsed := time.Since(start); elapsed < 120*time.Millisecond || rec.attempts < 2 {
				t.Errorf("gave up after %v and %d attempts", elapsed, rec.attempts)
			}
			if rec.closes != 1 {
				t.Errorf("the dialer was closed %d times", rec.closes)
			}

			rec = &recorder{Dialer: dbclient.SocketDir(dir)}
			if _, err := dbclient.Open(bg, rec, c.role, dbclient.Options{DialWait: -1}); !errors.Is(err, dbclient.ErrUnavailable) || rec.attempts != 1 {
				t.Errorf("a negative wait: %v after %d attempts", err, rec.attempts)
			}
		}
	})

	t.Run("a socket that appears", func(t *testing.T) {
		f := newFixture(t, nil)
		h := f.acquire()
		rec := &recorder{Dialer: h.Dialer(), failDial: func(attempt int) error {
			if attempt <= 2 {
				return errors.New("no socket yet")
			}
			return nil
		}}
		c, rec, err := f.tryOpenWith(bg, rec, dbwire.RoleReader, dbclient.Options{DialWait: 5 * time.Second})
		must(t, err)
		if rec.attempts != 3 {
			t.Errorf("%d attempts", rec.attempts)
		}
		wantRows(t, rows(t, c, 2, ""))
	})

	t.Run("the context ends", func(t *testing.T) {
		noLeaks(t)
		rec := &recorder{Dialer: dbclient.SocketDir(t.TempDir())}
		ctx, cancel := context.WithTimeout(bg, 50*time.Millisecond)
		defer cancel()
		_, err := dbclient.Open(ctx, rec, dbwire.RoleWriter, dbclient.Options{DialWait: time.Hour})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("got %v", err)
		}
	})

	t.Run("a role that does not exist", func(t *testing.T) {
		f := newFixture(t, nil)
		h := f.acquire()
		rec := &recorder{Dialer: h.Dialer()}
		if _, err := dbclient.Open(bg, rec, 0, dbclient.Options{}); err == nil || rec.closes != 1 {
			t.Errorf("got %v, and %d closes of the dialer", err, rec.closes)
		}
		rec = &recorder{Dialer: h.Dialer()}
		if _, err := dbclient.Open(bg, rec, dbwire.RoleWriter, dbclient.Options{Readers: -1}); err == nil || rec.closes != 1 {
			t.Errorf("got %v, and %d closes of the dialer", err, rec.closes)
		}
	})
}

func TestSocketDir(t *testing.T) {
	f := newFixture(t, nil)
	dir := t.TempDir()
	must(t, f.root.Listen(dir))
	w, err := dbclient.Open(bg, dbclient.SocketDir(dir), dbwire.RoleWriter, dbclient.Options{})
	must(t, err)
	t.Cleanup(func() { _ = w.Close() })
	r, err := dbclient.Open(bg, dbclient.SocketDir(dir), dbwire.RoleReader, dbclient.Options{})
	must(t, err)
	t.Cleanup(func() { _ = r.Close() })
	seed(t, w)
	big := strings.Repeat("0123456789", 30_000) // 300,000 bytes: 74 frames each way
	commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "big", big) })
	must(t, r.View(bg, func(tx *dbclient.Tx) error {
		if v, ok := get(t, tx, 2, "big"); !ok || v != big {
			t.Errorf("a value of %d bytes came back as %d bytes", len(big), len(v))
		}
		return nil
	}))
	// The writer socket has one writer.
	_, err = dbclient.Open(bg, dbclient.SocketDir(dir), dbwire.RoleWriter, dbclient.Options{DialWait: -1})
	if err == nil || !strings.HasSuffix(err.Error(), ": another process is connected as the writer") {
		t.Errorf("a second writer: %v", err)
	}
}

func TestClose(t *testing.T) {
	t.Run("after Close", func(t *testing.T) {
		f := newFixture(t, nil)
		c, rec := f.writer(dbclient.Options{})
		commit(t, c, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "v") })
		wantRows(t, rows(t, c, 2, ""), "k=v")
		if rec.liveConns() != 2 {
			t.Errorf("%d connections: the writer's, and the reader of the View", rec.liveConns())
		}
		must(t, c.Close())
		must(t, c.Close())
		if rec.liveConns() != 0 || rec.closes != 1 {
			t.Errorf("%d connections are left, and the dialer was closed %d times", rec.liveConns(), rec.closes)
		}
		for name, err := range map[string]error{
			"View":   c.View(bg, func(*dbclient.Tx) error { t.Error("f was called"); return nil }),
			"Update": c.Update(bg, func(*dbclient.Tx) error { t.Error("f was called"); return nil }),
			"Verify": c.Verify(bg),
		} {
			if !errors.Is(err, dbclient.ErrClosed) {
				t.Errorf("%s: got %v", name, err)
			}
		}
	})

	t.Run("a View and an Update in progress", func(t *testing.T) {
		f := newFixture(t, nil)
		c, _ := f.writer(dbclient.Options{Readers: 1})
		inView, inUpdate := make(chan struct{}), make(chan struct{})
		closed := make(chan struct{})
		viewErr, updateErr, waitErr := make(chan error, 1), make(chan error, 1), make(chan error, 1)
		go func() {
			viewErr <- c.View(bg, func(tx *dbclient.Tx) error {
				get(t, tx, 2, "k")
				close(inView)
				<-closed
				_, _, err := tx.Get(2, []byte("other"))
				return err
			})
		}()
		go func() {
			updateErr <- c.Update(bg, func(tx *dbclient.Tx) error {
				close(inUpdate)
				<-closed
				_, _, err := tx.Get(2, []byte("other"))
				return err
			})
		}()
		<-inView
		<-inUpdate
		// The only slot of the Views and the turn of the Updates are taken: these two wait.
		go func() { waitErr <- c.View(bg, func(*dbclient.Tx) error { t.Error("f was called"); return nil }) }()
		go func() { waitErr <- c.Update(bg, func(*dbclient.Tx) error { t.Error("f was called"); return nil }) }()
		must(t, c.Close())
		close(closed)
		for name, ch := range map[string]chan error{"View": viewErr, "Update": updateErr} {
			if err := <-ch; !errors.Is(err, dbclient.ErrClosed) {
				t.Errorf("%s in progress: got %v", name, err)
			}
		}
		for range 2 {
			if err := <-waitErr; !errors.Is(err, dbclient.ErrClosed) {
				t.Errorf("waiting: got %v", err)
			}
		}
	})
}

func TestReadOnlyClient(t *testing.T) {
	f := newFixture(t, nil)
	w, _ := f.writer(dbclient.Options{})
	commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "v") })
	r, rec := f.open(dbwire.RoleReader, dbclient.Options{})
	rec.reset()
	err := r.Update(bg, func(*dbclient.Tx) error {
		t.Error("f was called")
		return nil
	})
	if !errors.Is(err, dbclient.ErrReadOnly) {
		t.Errorf("Update of a reader: %v", err)
	}
	wantRequests(t, rec)
	wantRows(t, rows(t, r, 2, ""), "k=v")
	must(t, r.View(bg, func(tx *dbclient.Tx) error {
		for name, err := range map[string]error{
			"Put":         tx.Put(2, []byte("a"), nil),
			"Delete":      tx.Delete(2, []byte("k")),
			"PutObject":   tx.PutObject([32]byte{}, nil),
			"AppendEvent": tx.AppendEvent([]byte("e")),
		} {
			if !errors.Is(err, dbclient.ErrReadOnly) {
				t.Errorf("%s in a View: %v", name, err)
			}
		}
		// The checks of a write come after that of the mode.
		if err := tx.Put(0, nil, nil); !errors.Is(err, dbclient.ErrReadOnly) {
			t.Errorf("Put to table 0 in a View: %v", err)
		}
		return nil
	}))
	must(t, r.Verify(bg))
	if rec.dialed(dbwire.RoleWriter) != 0 {
		t.Error("a reader dialed the writer socket")
	}
}

func TestVerify(t *testing.T) {
	large := strings.Repeat("an object in a file ", 100)
	setup := func(t *testing.T) (*fixture, *dbclient.Client, *recorder) {
		f := newFixture(t, nil)
		c, rec := f.writer(dbclient.Options{})
		must(t, c.Update(bg, func(tx *dbclient.Tx) error {
			must(t, tx.PutObject(sha256.Sum256([]byte(large)), []byte(large)))
			put(t, tx, 2, "k", "v")
			return tx.AppendEvent([]byte(`{"e":1}`))
		}))
		rec.reset()
		return f, c, rec
	}
	t.Run("a good store", func(t *testing.T) {
		_, c, rec := setup(t)
		must(t, c.Verify(bg))
		must(t, c.Verify(bg))
		wantRequests(t, rec, dbwire.KindHello, dbwire.KindVerify, dbwire.KindVerify)
		if rec.dialed(dbwire.RoleReader) != 1 {
			t.Errorf("Verify went to %v connections, want the one of a reader", rec.dials)
		}
	})
	t.Run("a store with damage", func(t *testing.T) {
		f, c, _ := setup(t)
		h := sha256.Sum256([]byte(large))
		file, err := f.fs.OpenFile(fmt.Sprintf("logdb/objects/%x/%x", h[:1], h), os.O_WRONLY, 0)
		must(t, err)
		must(t, file.Truncate(30))
		_ = file.Close()
		err = c.Verify(bg)
		re := remote(t, err)
		if re.Code != dbwire.CodeFailed || !strings.Contains(re.Msg, ": corrupt: has 30 bytes, want ") {
			t.Errorf("got %v: %q", re.Code, re.Msg)
		}
		if err.Error() != re.Msg {
			t.Errorf("the error is %q, not the text of the daemon", err)
		}
		// The connection goes on.
		wantRows(t, rows(t, c, 2, ""), "k=v")
	})
}
