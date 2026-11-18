package store

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver/dbtest"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// silentDaemon is the Dialer of a daemon that takes what a client sends and, if hello is true, answers its Hello and
// nothing else, as a process that is stopped, or blocked on its disk, does.
type silentDaemon struct {
	hello bool
	wg    sync.WaitGroup
}

func (d *silentDaemon) Dial(_ context.Context, role dbwire.Role) (net.Conn, error) {
	cc, sc := net.Pipe()
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer func() { _ = sc.Close() }()
		if d.hello {
			rd, wr := dbwire.NewReader(sc, 1<<20), dbwire.NewWriter(sc, 1<<20)
			if kind, err := rd.Next(); err != nil || kind != dbwire.KindHello || rd.Discard() != nil {
				return
			}
			wr.Begin(dbwire.KindOK)
			e := &dbwire.Enc{W: wr}
			e.HelloReply(dbwire.HelloReply{Version: dbwire.Version, Role: role, Frame: 4096, Commit: 1 << 20,
				Object: 1 << 20, Staged: 1 << 20, MaxKey: dbwire.MaxKey})
			if wr.End() != nil {
				return
			}
		}
		_, _ = io.Copy(io.Discard, sc) // until the client closes its end
	}()
	return cc, nil
}

func (d *silentDaemon) String() string { return "silent" }

func (d *silentDaemon) Close() error {
	d.wg.Wait()
	return nil
}

// TestOpenContext checks that the context of Open, OpenReader and Create ends them, wherever the daemon stops answering:
// at the Hello, and at the first request that follows (the check of the schema, the creation).
func TestOpenContext(t *testing.T) {
	opens := []struct {
		name string
		open func(context.Context, dbclient.Dialer) (*Store, error)
	}{{"Open", Open}, {"OpenReader", OpenReader}, {"Create", Create}}
	for _, stage := range []struct {
		name  string
		hello bool
	}{{"the Hello", false}, {"the first request", true}} {
		for _, o := range opens {
			t.Run(o.name+" with no answer to "+stage.name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
				defer cancel()
				type result struct {
					s   *Store
					err error
				}
				done := make(chan result, 1)
				go func() {
					s, err := o.open(ctx, &silentDaemon{hello: stage.hello})
					done <- result{s, err}
				}()
				select {
				case r := <-done:
					if r.s != nil || !errors.Is(r.err, context.DeadlineExceeded) {
						t.Errorf("%s() = %v, %v, want no store and the error of the context", o.name, r.s, r.err)
					}
				case <-time.After(waitLimit):
					t.Fatalf("%s() has not returned %v after its context has ended", o.name, waitLimit)
				}
			})
		}
	}
}

// TestOpenStall checks that Open, OpenReader and Create fail with an error that wraps ErrUnavailable, with no context that
// ends, when the daemon stops answering at the Hello or at the first request that follows, for the stall limit of the
// client; and the same for an original that stops coming.
func TestOpenStall(t *testing.T) {
	const stall = 100 * time.Millisecond
	opens := []struct {
		name string
		open func(context.Context, dbclient.Dialer) (*Store, error)
	}{
		{"Open", func(ctx context.Context, d dbclient.Dialer) (*Store, error) {
			return openOn(ctx, d, dbwire.RoleWriter, time.Now, dbclient.Options{Stall: stall})
		}},
		{"OpenReader", func(ctx context.Context, d dbclient.Dialer) (*Store, error) {
			return openOn(ctx, d, dbwire.RoleReader, time.Now, dbclient.Options{Stall: stall})
		}},
		{"Create", func(ctx context.Context, d dbclient.Dialer) (*Store, error) {
			return createOn(ctx, d, time.Now, dbclient.Options{Stall: stall})
		}},
	}
	for _, stage := range []struct {
		name  string
		hello bool
	}{{"the Hello", false}, {"the first request", true}} {
		for _, o := range opens {
			t.Run(o.name+" with no answer to "+stage.name, func(t *testing.T) {
				type result struct {
					s   *Store
					err error
				}
				done := make(chan result, 1)
				began := time.Now()
				go func() {
					s, err := o.open(t.Context(), &silentDaemon{hello: stage.hello})
					done <- result{s, err}
				}()
				select {
				case r := <-done:
					if r.s != nil || !errors.Is(r.err, ErrUnavailable) || !strings.Contains(r.err.Error(), "the daemon has sent nothing for 100ms") {
						t.Errorf("%s() = %v, %v, want no store and ErrUnavailable, with the time that the daemon was silent", o.name, r.s, r.err)
					}
					if took := time.Since(began); took < stall {
						t.Errorf("%s() failed after %v, before the limit of %v", o.name, took, stall)
					}
				case <-time.After(waitLimit):
					t.Fatalf("%s() has not returned %v after the limit of %v", o.name, waitLimit, stall)
				}
			})
		}
	}
	t.Run("OpenOriginal", openOriginalStall)
}

const waitLimit = 20 * time.Second

// TestOpenContextEndsAfterOpen checks that a store does not depend on the context that Open, OpenReader and Create had: it
// works when that has ended.
func TestOpenContextEndsAfterOpen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	o := oneBackend[0].options()
	if err := dbtest.Init(dir, o.daemonOptions(nil)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	s, err := start(dir, o, true, func(d dbclient.Dialer) (*Store, error) {
		return createOn(ctx, d, o.clock(), o.client())
	})
	if err != nil {
		t.Fatalf("createOn() = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	r, err := start(dir, o, false, func(d dbclient.Dialer) (*Store, error) {
		return openOn(ctx, d, dbwire.RoleReader, o.clock(), o.client())
	})
	if err != nil {
		t.Fatalf("openOn() = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	cancel()
	update(t, s, func(tx *Tx) error {
		_, err := tx.Put(content.RefTerm, aheadTerm(0), docJSON(content.RefTerm, aheadTerm(0)), userID)
		return err
	})
	view(t, r, func(tx *Tx) error {
		if _, err := tx.Document(content.RefTerm, aheadTerm(0)); err != nil {
			t.Errorf("Document() of a store whose opening context has ended = %v", err)
		}
		return nil
	})
}

// stallConn is a connection of a client whose reads, once stall is set, give no byte until a deadline has passed or the
// connection is closed: the daemon holds its reply.
type stallConn struct {
	net.Conn
	d     *stallDialer
	once  sync.Once
	ended chan struct{}
}

func (c *stallConn) end() { c.once.Do(func() { close(c.ended) }) }

func (c *stallConn) SetDeadline(t time.Time) error {
	if !t.IsZero() && !t.After(time.Now()) {
		c.end()
	}
	return c.Conn.SetDeadline(t)
}

func (c *stallConn) Read(p []byte) (int, error) {
	if c.d.stall.Load() {
		select {
		case c.d.blocked <- struct{}{}:
		default:
		}
		<-c.ended
		return 0, os.ErrDeadlineExceeded
	}
	return c.Conn.Read(p)
}

func (c *stallConn) Close() error {
	c.end()
	return c.Conn.Close()
}

// stallDialer makes the connections of a client that are stallConns. blocked gets a value when a read starts to wait.
type stallDialer struct {
	dbclient.Dialer
	stall   atomic.Bool
	blocked chan struct{}
}

func (d *stallDialer) Dial(ctx context.Context, role dbwire.Role) (net.Conn, error) {
	conn, err := d.Dialer.Dial(ctx, role)
	if err != nil {
		return nil, err
	}
	return &stallConn{Conn: conn, d: d, ended: make(chan struct{})}, nil
}

func (d *stallDialer) String() string { return d.Dialer.(interface{ String() string }).String() }

func (d *stallDialer) Close() error { return d.Dialer.(interface{ Close() error }).Close() }

// TestOpenOriginalContext checks that the context of OpenOriginal bounds the open, and every read of the file.
func TestOpenOriginalContext(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	o := oneBackend[0].options()
	if err := dbtest.Init(dir, o.daemonOptions(nil)); err != nil {
		t.Fatal(err)
	}
	var d *stallDialer
	s, err := start(dir, o, true, func(inner dbclient.Dialer) (*Store, error) {
		d = &stallDialer{Dialer: inner, blocked: make(chan struct{}, 1)}
		return createOn(context.Background(), d, o.clock(), o.client())
	})
	if err != nil {
		t.Fatalf("createOn() = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	data := bytes.Repeat([]byte("an original "), 200<<10) // 2.4 MB, more than the daemon sends in a frame
	original, err := s.PutOriginal(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("PutOriginal() = %v", err)
	}
	warm(t, s) // a connection for the reads is idle: the Hello is not what the daemon holds

	t.Run("the open", func(t *testing.T) {
		d.stall.Store(true)
		defer d.stall.Store(false)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		type result struct {
			f   fs.File
			err error
		}
		done := make(chan result, 1)
		go func() {
			f, err := s.OpenOriginal(ctx, original.Hash)
			done <- result{f, err}
		}()
		<-d.blocked // the daemon holds the reply
		cancel()
		select {
		case r := <-done:
			if r.f != nil {
				_ = r.f.Close()
			}
			if !errors.Is(r.err, context.Canceled) {
				t.Errorf("OpenOriginal() = %v, %v, want the error of the context", r.f, r.err)
			}
		case <-time.After(waitLimit):
			t.Fatalf("OpenOriginal() has not returned %v after its context has ended", waitLimit)
		}
	})

	t.Run("the reads", func(t *testing.T) {
		warm(t, s)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		f, err := s.OpenOriginal(ctx, original.Hash)
		if err != nil {
			t.Fatalf("OpenOriginal() = %v", err)
		}
		defer func() { _ = f.Close() }()
		buf := make([]byte, 1<<10)
		if _, err := io.ReadFull(f, buf); err != nil {
			t.Fatalf("the first read = %v", err)
		}
		d.stall.Store(true)
		defer d.stall.Store(false)
		done := make(chan error, 1)
		go func() {
			_, err := io.Copy(io.Discard, f)
			done <- err
		}()
		<-d.blocked // the daemon gives no more bytes
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("the reads of the file after its context has ended = %v, want the error of the context", err)
			}
		case <-time.After(waitLimit):
			t.Fatalf("the reads of the file have not returned %v after the context has ended", waitLimit)
		}
	})
}

// openOriginalStall checks that an original whose daemon gives no more bytes, in the open or in the reads, fails with
// ErrUnavailable when the stall limit of the client has passed, with no context that ends.
func openOriginalStall(t *testing.T) {
	// Longer than that of the Hello tests: the daemon of this one answers, and its reads must not be cut by a slow machine.
	const stall = 250 * time.Millisecond
	dir := filepath.Join(t.TempDir(), "data")
	o := oneBackend[0].options()
	if err := dbtest.Init(dir, o.daemonOptions(nil)); err != nil {
		t.Fatal(err)
	}
	// The writer has the default limit: the daemon syncs an original before it answers, which may take longer than the
	// short limit of the reader that is tested.
	w, err := start(dir, o, true, func(d dbclient.Dialer) (*Store, error) {
		return createOn(context.Background(), d, o.clock(), o.client())
	})
	if err != nil {
		t.Fatalf("createOn() = %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	data := bytes.Repeat([]byte("an original "), 200<<10) // 2.4 MB, more than the daemon sends in a frame
	original, err := w.PutOriginal(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("PutOriginal() = %v", err)
	}
	var d *stallDialer
	s, err := start(dir, o, false, func(inner dbclient.Dialer) (*Store, error) {
		d = &stallDialer{Dialer: inner, blocked: make(chan struct{}, 1)}
		opts := o.client()
		opts.Stall = stall
		return openOn(context.Background(), d, dbwire.RoleReader, o.clock(), opts)
	})
	if err != nil {
		t.Fatalf("openOn() = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	want := func(t *testing.T, err error, began time.Time) {
		t.Helper()
		if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "the daemon has sent nothing for "+stall.String()) {
			t.Errorf("error = %v, want ErrUnavailable, with the time that the daemon was silent", err)
		}
		if took := time.Since(began); took < stall {
			t.Errorf("failed after %v, before the limit of %v", took, stall)
		}
	}

	t.Run("the open", func(t *testing.T) {
		warm(t, s)
		d.stall.Store(true)
		defer d.stall.Store(false)
		began := time.Now()
		f, err := s.OpenOriginal(t.Context(), original.Hash)
		if f != nil {
			_ = f.Close()
		}
		want(t, err, began)
	})

	t.Run("the reads", func(t *testing.T) {
		warm(t, s)
		f, err := s.OpenOriginal(t.Context(), original.Hash)
		if err != nil {
			t.Fatalf("OpenOriginal() = %v", err)
		}
		defer func() { _ = f.Close() }()
		if _, err := io.ReadFull(f, make([]byte, 1<<10)); err != nil {
			t.Fatalf("the first read = %v", err)
		}
		d.stall.Store(true)
		defer d.stall.Store(false)
		began := time.Now()
		_, err = io.Copy(io.Discard, f)
		want(t, err, began)
	})
}
