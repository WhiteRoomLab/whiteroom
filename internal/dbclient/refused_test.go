package dbclient_test

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver/dbtest"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

const closingText = "the daemon is closing"

// wantRefused checks that err is the refusal busy of the daemon with its text, which says that the daemon cannot be reached.
func wantRefused(t *testing.T, err error, text string) {
	t.Helper()
	if !errors.Is(err, dbclient.ErrUnavailable) || !strings.Contains(err.Error(), text) {
		t.Errorf("got %v, want an error that wraps ErrUnavailable and says %q", err, text)
	}
	if re, ok := errors.AsType[*dbwire.RemoteError](err); !ok || re.Code != dbwire.CodeBusy {
		t.Errorf("got %v, want the refusal busy of the daemon in the chain", err)
	}
	if n := strings.Count(err.Error(), dbclient.ErrUnavailable.Error()); n != 1 {
		t.Errorf("got %v, want its text to say once that the daemon cannot be reached", err)
	}
}

// TestRefusedRequest: a daemon that shuts down refuses a request on a connection that it has with the code busy, and ends the
// connection; the client says that the daemon is unavailable.
func TestRefusedRequest(t *testing.T) {
	// refuse answers the first request after the Snapshot as a daemon that is closing does.
	refuse := func(p *peer) {
		p.hello()
		if p.n > 0 {
			p.request()
			return
		}
		p.snapshot(1)
		p.request()
		p.refuse(dbwire.CodeBusy, closingText)
	}

	t.Run("a Get", func(t *testing.T) {
		c, rec, pd := openPeer(t, dbwire.RoleReader, refuse)
		err := c.View(bg, func(tx *dbclient.Tx) error {
			_, _, err := tx.Get(2, []byte("k"))
			return err
		})
		wantRefused(t, err, closingText)
		// Not sent again, not on a connection that the daemon has ended.
		if got := pd.dialed(); got != 1 {
			t.Errorf("%d connections were made, want the first alone", got)
		}
		if got := rec.liveConns(); got != 0 {
			t.Errorf("%d connections are open, want none", got)
		}
	})

	t.Run("a GetMany", func(t *testing.T) {
		c, _, _ := openPeer(t, dbwire.RoleReader, refuse)
		err := c.View(bg, func(tx *dbclient.Tx) error {
			return tx.GetMany(2, [][]byte{[]byte("k")})
		})
		wantRefused(t, err, closingText)
	})

	t.Run("an Original", func(t *testing.T) {
		c, _, _ := openPeer(t, dbwire.RoleReader, func(p *peer) {
			p.hello()
			if p.n > 0 {
				p.request()
				return
			}
			p.expect(dbwire.KindOriginal)
			p.refuse(dbwire.CodeBusy, closingText)
		})
		_, err := c.Original(bg, [32]byte{1})
		wantRefused(t, err, closingText)
	})

	t.Run("a Commit", func(t *testing.T) {
		c, _, _ := openPeer(t, dbwire.RoleWriter, refuse)
		err := c.Update(bg, func(tx *dbclient.Tx) error {
			put(t, tx, 2, "k", "v")
			return tx.AppendEvent([]byte(`{"e":1}`))
		})
		wantRefused(t, err, closingText)
		// The request was refused, and not made: it is known.
		if strings.Contains(err.Error(), "it is not known whether the commit was made") {
			t.Errorf("got %v, want the refusal as it is", err)
		}
	})
}

// TestRefusedReader: a daemon that serves the most readers that it takes refuses the Hello of one more with the code busy,
// which is unavailable after the wait. The refusal of a writer, for another writer, is not (TestOpen).
func TestRefusedReader(t *testing.T) {
	f := newFixture(t, func(o *dbtest.Options) {
		l := *o.Limits
		l.Readers = 1
		o.Limits = &l
	})
	f.open(dbwire.RoleReader, dbclient.Options{})
	_, rec, err := f.tryOpen(bg, dbwire.RoleReader, dbclient.Options{DialWait: 120 * time.Millisecond})
	wantRefused(t, err, "1 readers are connected")
	if rec.attempts < 2 {
		t.Errorf("%d attempts, want the connection tried again within the wait", rec.attempts)
	}
}

// TestClosingDaemon: the requests that reach a daemon that is shutting down, which waits for a Commit, are unavailable.
func TestClosingDaemon(t *testing.T) {
	gate, at := make(chan struct{}), make(chan struct{})
	var blocked atomic.Bool
	var once sync.Once
	f := newFixture(t, func(o *dbtest.Options) {
		o.Hook = func(point string) error {
			if point == "commit:gate" && blocked.Load() {
				once.Do(func() { close(at) })
				<-gate
			}
			return nil
		}
	})
	w, _ := f.writer(dbclient.Options{DialWait: 100 * time.Millisecond})
	r, _ := f.open(dbwire.RoleReader, dbclient.Options{DialWait: 100 * time.Millisecond})
	read := func() error {
		return r.View(bg, func(tx *dbclient.Tx) error {
			_, _, err := tx.Get(2, []byte("x"))
			return err
		})
	}
	must(t, read()) // the connection that the View used is idle
	blocked.Store(true)
	updated := make(chan error, 1)
	go func() {
		updated <- w.Update(bg, func(tx *dbclient.Tx) error {
			put(t, tx, 2, "k", "v")
			return tx.AppendEvent([]byte(`{"e":1}`))
		})
	}()
	<-at
	shut := make(chan struct{})
	go func() {
		defer close(shut)
		f.root.Server().Shutdown(bg)
	}()
	var err error
	eventually(t, "a refused View", func() bool { err = read(); return err != nil })
	close(gate)
	<-shut
	wantRefused(t, err, closingText)
	if err := <-updated; err != nil {
		t.Errorf("the Update that the daemon waited for: %v", err)
	}
}
