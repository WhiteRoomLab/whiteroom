package dbclient_test

import (
	"bytes"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver/dbtest"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// idleFixture is a daemon that has the idle limit of its connections, and counts the readers' connections that it has
// ended for it, with a writer that has put the row k=v and an original in the store.
func idleFixture(t *testing.T, readers int, idle time.Duration) (f *fixture, ended *atomic.Int64, original [32]byte) {
	t.Helper()
	ended = new(atomic.Int64)
	f = newFixture(t, func(o *dbtest.Options) {
		o.Limits.Readers, o.Limits.SnapshotIdle = readers, idle
		o.Hook = func(point string) error {
			if point == "reader:idle" {
				ended.Add(1)
			}
			return nil
		}
	})
	w, _ := f.writer(dbclient.Options{})
	commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "v") })
	return f, ended, putOriginal(t, w, []byte("an original of the idle test"))
}

// TestIdleReaders: a reader's connection that nothing uses is ended by the daemon, which has its place free for another
// reader; a client that finds the connection of its pool ended makes a new one, and its call succeeds.
func TestIdleReaders(t *testing.T) {
	const idle = 200 * time.Millisecond
	rd := dbwire.RoleReader

	// operations are calls that begin with the connection of the pool, and say what they have read.
	type operation struct {
		name string
		do   func(t *testing.T, c *dbclient.Client, original [32]byte)
	}
	operations := []operation{
		{"View", func(t *testing.T, c *dbclient.Client, _ [32]byte) {
			must(t, c.View(bg, func(tx *dbclient.Tx) error {
				if v, ok := get(t, tx, 2, "k"); !ok || v != "v" {
					t.Errorf("Get k: %q, %v", v, ok)
				}
				return nil
			}))
		}},
		{"Verify", func(t *testing.T, c *dbclient.Client, _ [32]byte) { must(t, c.Verify(bg)) }},
		{"Original", func(t *testing.T, c *dbclient.Client, original [32]byte) {
			if got := readOriginal(t, c, original); !bytes.Equal(got, []byte("an original of the idle test")) {
				t.Errorf("the original has %d bytes", len(got))
			}
		}},
	}

	t.Run("the places of the readers", func(t *testing.T) {
		f, ended, original := idleFixture(t, 2, idle)
		type opened struct {
			name string
			c    *dbclient.Client
			rec  *recorder
		}
		var readers []opened
		viewed, vrec := f.open(rd, dbclient.Options{})
		must(t, viewed.View(bg, func(*dbclient.Tx) error { return nil }))
		readers = append(readers, opened{"a reader that has viewed", viewed, vrec})
		opens, orec := f.open(rd, dbclient.Options{}) // with a Hello and nothing after it
		readers = append(readers, opened{"a reader that has opened", opens, orec})

		// The two readers have the two places, until the daemon ends their connections.
		eventually(t, "a third reader to be let in", func() bool {
			c, rec, err := f.tryOpen(bg, rd, dbclient.Options{DialWait: -1})
			if err == nil {
				readers = append(readers, opened{"the third reader", c, rec})
			}
			return err == nil
		})

		quiet := func(t *testing.T) {
			t.Helper()
			eventually(t, "the daemon to end the connections that nothing uses", func() bool {
				n := 0
				for _, r := range readers {
					n += r.rec.dialed(rd)
				}
				return ended.Load() >= int64(n)
			})
		}
		for _, r := range readers[:2] {
			for _, op := range operations {
				t.Run(op.name+" of "+r.name, func(t *testing.T) {
					quiet(t) // the connection that the client has pooled is the one that the daemon has ended
					before := r.rec.dialed(rd)
					op.do(t, r.c, original)
					if got := r.rec.dialed(rd) - before; got != 1 {
						t.Errorf("%s made %d connections, want 1", op.name, got)
					}
				})
			}
		}
	})

	t.Run("a socket", func(t *testing.T) {
		// A short path: that of a socket is at most 107 bytes, and the name of a test is long. It goes after the daemon.
		dir, err := os.MkdirTemp("", "wr")
		must(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		f, ended, _ := idleFixture(t, 4, idle)
		must(t, f.root.Listen(dir))
		rec := &recorder{Dialer: dbclient.SocketDir(dir)}
		c, _, err := f.tryOpenWith(bg, rec, rd, dbclient.Options{})
		must(t, err)
		eventually(t, "the daemon to end the connection", func() bool { return ended.Load() >= 1 })
		before := rec.dialed(rd)
		operations[0].do(t, c, [32]byte{})
		if got := rec.dialed(rd) - before; got != 1 {
			t.Errorf("a View made %d connections, want 1", got)
		}
	})
}
