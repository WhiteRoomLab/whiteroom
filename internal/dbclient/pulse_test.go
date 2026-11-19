package dbclient_test

import (
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

// TestPulsedReplies: a reply with pulses in it, the empty frames that a daemon writes while a long request goes on, is
// read as the one that has none. The answers are the same, and so are the refusals, which come after the pulses as the
// error frame that ends an OK reply; and the connection goes on.
func TestPulsedReplies(t *testing.T) {
	const pulses, gap = 3, time.Millisecond
	k := []byte("k")
	putOne := func(c *dbclient.Client) error {
		return c.Update(bg, func(tx *dbclient.Tx) error { return tx.Put(2, k, k) })
	}

	t.Run("Verify", func(t *testing.T) {
		const msg = "logdb/objects/ab/abcdef: corrupt: has 30 bytes, want 2000"
		c, rec, pd := openPeer(t, dbwire.RoleReader, func(p *peer) {
			p.hello()
			p.expect(dbwire.KindVerify)
			p.pulses(pulses, gap)
			p.refuse(dbwire.CodeFailed, msg)
			p.expect(dbwire.KindVerify) // on the connection that was refused
			p.pulse()
			p.reply(nil)
			p.hold()
		})
		err := c.Verify(bg)
		if re := remote(t, err); re.Code != dbwire.CodeFailed || err.Error() != msg {
			t.Errorf("Verify = %v (%v), want the refusal %q", err, re.Code, msg)
		}
		must(t, c.Verify(bg))
		wantConnections(t, pd, rec, 1, 1)
	})

	t.Run("Commit", func(t *testing.T) {
		const seq, rule, limit = "the last commit is 4", "a commit that puts or deletes a row needs an audit event", "a commit of 2000 bytes"
		c, rec, pd := openPeer(t, dbwire.RoleWriter, func(p *peer) {
			p.hello()
			for _, answer := range []func(){
				func() { p.refuse(dbwire.CodeSeq, seq) },
				func() { p.refuse(dbwire.CodeRule, rule) },
				func() { p.refuse(dbwire.CodeLimit, limit) },
				func() { p.reply(func(e *dbwire.Enc) { e.CommitReply(5, time.Unix(1_700_000_000, 0)) }) },
				func() { p.reply(func(e *dbwire.Enc) { e.CommitReply(6, time.Unix(1_700_000_001, 0)) }) },
			} {
				p.snapshot(4)
				p.expect(dbwire.KindCommit)
				p.pulses(pulses, gap)
				answer()
			}
			p.hold()
		})
		err := putOne(c)
		if re := remote(t, err); re.Code != dbwire.CodeSeq || err.Error() != "the store has changed since the transaction began: "+seq {
			t.Errorf("Update = %v (%v), want the refusal of the sequence number", err, re.Code)
		}
		err = putOne(c)
		if re := remote(t, err); re.Code != dbwire.CodeRule || err.Error() != rule {
			t.Errorf("Update = %v (%v), want the refusal of the rule", err, re.Code)
		}
		err = putOne(c)
		if re := remote(t, err); re.Code != dbwire.CodeLimit || err.Error() != limit {
			t.Errorf("Update = %v (%v), want the refusal of the limit", err, re.Code)
		}
		must(t, putOne(c)) // the answers are read to their ends, or the next Snapshot would not find its own
		must(t, putOne(c))
		wantConnections(t, pd, rec, 1, 1)
	})

	t.Run("Events", func(t *testing.T) {
		c, rec, pd := openPeer(t, dbwire.RoleReader, func(p *peer) {
			p.hello()
			p.snapshot(5)
			if p.n == 0 {
				p.release()
				p.hold()
				return
			}
			p.expect(dbwire.KindEvents)
			p.pulses(pulses, gap)
			p.raw(p.wire(false, eventFrame(1)))
			p.pulses(pulses, gap)
			p.raw(p.wire(false, eventFrame(2)))
			p.pulse()
			p.raw(p.wire(true, func(e *dbwire.Enc) {
				eventFrame(3)(e)
				e.EventsEnd()
			}))
			p.release()
			p.hold()
		})
		var got []uint64
		must(t, c.View(bg, func(tx *dbclient.Tx) error {
			return tx.Events(bg, func(n uint64, data []byte) bool {
				if string(data) != "x" {
					t.Errorf("the event %d has the data %q", n, data)
				}
				got = append(got, n)
				return true
			})
		}))
		if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
			t.Errorf("the events are %v, want [1 2 3]", got)
		}
		wantConnections(t, pd, rec, 2, 2)
	})

	t.Run("Events that is refused", func(t *testing.T) {
		const msg = "logdb/00000001-000001.log: cannot read: input/output error"
		c, rec, pd := openPeer(t, dbwire.RoleReader, func(p *peer) {
			p.hello()
			p.snapshot(5)
			if p.n == 0 {
				p.release()
				p.hold()
				return
			}
			p.expect(dbwire.KindEvents)
			p.pulses(pulses, gap)
			p.raw(p.wire(false, eventFrame(1)))
			p.pulse()
			p.refuse(dbwire.CodeFailed, msg)
			p.hold()
		})
		var got []uint64
		err := c.View(bg, func(tx *dbclient.Tx) error {
			return tx.Events(bg, func(n uint64, _ []byte) bool {
				got = append(got, n)
				return true
			})
		})
		if re := remote(t, err); re.Code != dbwire.CodeFailed || err.Error() != msg {
			t.Errorf("Events = %v (%v), want the refusal %q", err, re.Code, msg)
		}
		if len(got) != 1 || got[0] != 1 {
			t.Errorf("the events are %v, want [1]", got)
		}
		wantConnections(t, pd, rec, 2, 1)
	})

	t.Run("Backup", func(t *testing.T) {
		c, rec, pd := openPeer(t, dbwire.RoleReader, func(p *peer) {
			p.hello()
			if p.n == 0 {
				p.hold()
				return
			}
			p.expect(dbwire.KindBackup)
			p.pulses(pulses, gap) // before the position
			p.raw(p.wire(false, func(e *dbwire.Enc) {
				position(e)
				e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryHead, Line: headLine(2)})
			}))
			p.pulses(pulses, gap)
			p.raw(p.wire(false, func(e *dbwire.Enc) {
				e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryDir, Path: "files"})
				e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryFile, Path: "files/x"})
				chunks := e.Chunks()
				_, _ = chunks.Write([]byte("hel"))
			}))
			p.pulse()
			p.raw(p.wire(false, func(e *dbwire.Enc) {
				chunks := e.Chunks()
				_, _ = chunks.Write([]byte("lo"))
				_ = chunks.Close()
			}))
			p.pulse()
			p.raw(p.wire(true, func(e *dbwire.Enc) { e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryEnd}) }))
			p.hold()
		})
		dst := simfs.New("copy")
		b, err := c.Backup(bg, dst)
		must(t, err)
		want := dbclient.Backup{Generation: 1, Segment: 1, End: 10, Seq: 3, CRC: 7, Heads: []string{headLine(2)}}
		if !reflect.DeepEqual(b, want) {
			t.Errorf("the position is %+v, want %+v", b, want)
		}
		f, err := dst.Open("files/x")
		must(t, err)
		defer func() { _ = f.Close() }()
		if got, err := io.ReadAll(f); err != nil || string(got) != "hello" {
			t.Errorf("the copy has %q (%v), want %q", got, err, "hello")
		}
		wantConnections(t, pd, rec, 2, 2)
	})

	t.Run("Backup that is refused", func(t *testing.T) {
		const msg = "logdb: cannot read: input/output error"
		c, rec, pd := openPeer(t, dbwire.RoleReader, func(p *peer) {
			p.hello()
			if p.n == 0 {
				p.hold()
				return
			}
			p.expect(dbwire.KindBackup)
			p.pulses(pulses, gap)
			p.refuse(dbwire.CodeFailed, msg)
			p.hold()
		})
		_, err := c.Backup(bg, simfs.New("copy"))
		if re := remote(t, err); re.Code != dbwire.CodeFailed || err.Error() != msg {
			t.Errorf("Backup = %v (%v), want the refusal %q", err, re.Code, msg)
		}
		wantConnections(t, pd, rec, 2, 2)
	})

	// The replies of the real daemon, whose pulses come as its engine reads slowly: a Verify that finds damage, and a Commit
	// that waits for a Verify and is then refused, end as they do with no pulse. The hook counts the pulses that it writes.
	t.Run("a Verify of the daemon that finds damage", func(t *testing.T) {
		s := newSlowStore(t, dbclient.Options{})
		h := s.objects[7]
		file, err := s.fs.OpenFile(fmt.Sprintf("logdb/objects/%x/%x", h[:1], h), os.O_WRONLY, 0)
		must(t, err)
		must(t, file.Truncate(30))
		_ = file.Close()
		s.ctl.delay.Store(int64(slowRead / 3))
		pulses := s.pulses.Load()
		err = s.r.Verify(bg)
		s.ctl.delay.Store(0)
		re := remote(t, err)
		if re.Code != dbwire.CodeFailed || !strings.Contains(re.Msg, ": corrupt: has 30 bytes, want ") || err.Error() != re.Msg {
			t.Errorf("Verify = %v (%v), want the refusal of the daemon, with its text", err, re.Code)
		}
		if got := s.pulses.Load() - pulses; got < 3 {
			t.Errorf("the daemon wrote %d pulses before it refused, want some", got)
		}
		wantRows(t, rows(t, s.r, 2, "k000"), "k000=v") // the connection goes on
		if got := s.rrec.dialed(dbwire.RoleReader); got != 1 {
			t.Errorf("the reader connected %d times, want 1", got)
		}
	})

	t.Run("a Commit of the daemon that waits for a Verify and is refused", func(t *testing.T) {
		s := newSlowStore(t, dbclient.Options{Stall: stallKeep})
		verified := make(chan error, 1)
		var pulses int64
		var sent time.Time
		err := s.w.Update(bg, func(tx *dbclient.Tx) error {
			put(t, tx, 2, "k", "w")
			event(t, tx, `{"e":1}`)
			// A commit that does not come from this client, after its snapshot; and a Verify that the Commit of the client
			// then waits for, as long as it reads.
			must(t, s.engine().Update(bg, func(tx *logdb.Tx) error { return tx.Put(2, []byte("x"), []byte("y")) }))
			reads := s.ctl.reads.Load()
			pulses = s.pulses.Load()
			s.ctl.delay.Store(int64(slowRead / 3))
			go func() { verified <- s.r.Verify(bg) }()
			eventually(t, "the Verify to read", func() bool { return s.ctl.reads.Load() > reads })
			sent = time.Now()
			return nil
		})
		if took := time.Since(sent); took < stallKeep {
			t.Errorf("the Commit was refused after %v, before the limit of %v: it did not wait for the Verify", took, stallKeep)
		}
		re := remote(t, err)
		if re.Code != dbwire.CodeSeq || !strings.HasPrefix(err.Error(), "the store has changed since the transaction began: ") {
			t.Errorf("Update = %v (%v), want the refusal of the sequence number", err, re.Code)
		}
		must(t, <-verified)
		if got := s.pulses.Load() - pulses; got < 3 {
			t.Errorf("the daemon wrote %d pulses, want some", got)
		}
		s.ctl.delay.Store(0)
		// The Commit was refused, and the commit of the engine is there.
		wantRows(t, rows(t, s.w, 2, "x"), "x=y")
		must(t, s.w.View(bg, func(tx *dbclient.Tx) error {
			if v, ok := get(t, tx, 2, "k"); ok {
				t.Errorf("the key k has the value %q, which the refused Commit put", v)
			}
			return nil
		}))
		commit(t, s.w, func(tx *dbclient.Tx) { put(t, tx, 2, "k", "w") })
		if got := s.wrec.dialed(dbwire.RoleWriter); got != 1 {
			t.Errorf("the writer connected %d times, want 1", got)
		}
	})

	t.Run("a Backup of the daemon whose position takes a while", func(t *testing.T) {
		s := newSlowStore(t, dbclient.Options{Stall: stallKeep})
		var seq uint64
		must(t, s.r.View(bg, func(tx *dbclient.Tx) error {
			seq = tx.Seq()
			return nil
		}))
		s.ctl.delay.Store(int64(slowRead / 2))
		pulses := s.pulses.Load()
		dst := simfs.New("copy")
		b, err := s.r.Backup(bg, dst)
		s.ctl.delay.Store(0)
		must(t, err)
		if got := s.pulses.Load() - pulses; got < 3 {
			t.Errorf("the daemon wrote %d pulses before the position, want some", got)
		}
		if b.Seq != seq || len(b.Heads) != 0 {
			t.Errorf("the backup is at %d with %d heads, want the store's %d and none", b.Seq, len(b.Heads), seq)
		}
		if entries, err := dst.ReadDir("logdb"); err != nil || len(entries) == 0 {
			t.Errorf("the copy has %d entries in logdb (%v), want the files of the engine", len(entries), err)
		}
	})
}
