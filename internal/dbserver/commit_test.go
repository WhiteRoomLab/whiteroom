package dbserver

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

func TestPutObject(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Limits.Object, o.Limits.Staged = 600, 1000 })
	w := f.connect(dbwire.RoleWriter)
	w.mustSnapshot()
	before := f.dump()

	err := w.do(dbwire.KindPutObject, func(e *dbwire.Enc) { e.PutObjectRequest(hashOf("other"), []byte("data")) }, dbwire.ReadEmpty)
	wantCode(t, err, dbwire.CodeHash)
	if found, _, _ := w.object(hashOf("data")); found {
		t.Error("an object with a wrong hash is staged")
	}

	a, b := strings.Repeat("a", 400), strings.Repeat("b", 400)
	for _, data := range []string{a, a, b} { // the same object twice counts once
		if _, err := w.putObject(data); err != nil {
			t.Fatalf("PutObject of %d bytes: %v", len(data), err)
		}
	}
	// 800 bytes are staged, and a third object of 300 makes 1100, over the limit of 1000; a again does not count.
	c := strings.Repeat("c", 300)
	_, err = w.putObject(c)
	wantCode(t, err, dbwire.CodeLimit)
	if _, err := w.putObject(a); err != nil {
		t.Errorf("an object that is staged already: %v", err)
	}
	if found, _, _ := w.object(hashOf(c)); found {
		t.Error("the refused object is staged")
	}
	if found, got, _ := w.object(hashOf(b)); !found || string(got) != b {
		t.Error("an object that was staged is lost after a refusal")
	}
	f.unchanged(before)
}

func TestPutObjectDropped(t *testing.T) {
	for name, drop := range map[string]func(t *testing.T, w *client, seq uint64){
		"Release":  func(t *testing.T, w *client, seq uint64) { _ = w.release() },
		"Snapshot": func(t *testing.T, w *client, seq uint64) { w.mustSnapshot() },
		"a Commit": func(t *testing.T, w *client, seq uint64) {
			if _, _, err := w.commit(seq, event("e")); err != nil {
				t.Fatal(err)
			}
		},
		"a Commit that is refused": func(t *testing.T, w *client, seq uint64) {
			_, _, err := w.commit(seq+9, event("e"))
			wantCode(t, err, dbwire.CodeSeq)
		},
		"a Commit that breaks a rule": func(t *testing.T, w *client, seq uint64) {
			_, _, err := w.commit(seq, put(2, "k", "v"))
			wantCode(t, err, dbwire.CodeRule)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, nil)
			w := f.connect(dbwire.RoleWriter)
			seq := w.mustSnapshot()
			if _, err := w.putObject("staged"); err != nil {
				t.Fatal(err)
			}
			drop(t, w, seq)
			committed := f.dump().Objects
			seq = w.mustSnapshot()
			if found, _, _ := w.object(hashOf("staged")); found && name != "a Commit" {
				t.Error("the object is still staged")
			}
			if _, _, err := w.commit(seq, event("e")); err != nil {
				t.Fatal(err)
			}
			if after := f.dump().Objects; len(after) != len(committed) {
				t.Errorf("a dropped object was committed later: %v", after)
			}
		})
	}

	t.Run("the end of the connection", func(t *testing.T) {
		f := newFixture(t, nil)
		w := f.connect(dbwire.RoleWriter)
		w.mustSnapshot()
		if _, err := w.putObject("staged"); err != nil {
			t.Fatal(err)
		}
		_ = w.conn.Close()
		f.waitSessions(0)
		w2 := f.connect(dbwire.RoleWriter)
		seq := w2.mustSnapshot()
		if found, _, _ := w2.object(hashOf("staged")); found {
			t.Error("the object of a connection that ended is staged")
		}
		if _, _, err := w2.commit(seq, event("e")); err != nil {
			t.Fatal(err)
		}
		if d := f.dump(); len(d.Objects) != 0 {
			t.Errorf("objects after the commit of a connection that staged none: %v", d.Objects)
		}
	})
}

func TestPutObjectLimits(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Limits.Object, o.Limits.Staged = 600, 1000 })
	before := f.dump()
	w := f.connect(dbwire.RoleWriter)
	w.mustSnapshot()
	// The size is over the limit: the request is refused before its bytes are read, and the connection ends.
	var body []byte
	body = append(body, make([]byte, 32)...)
	body = binary.BigEndian.AppendUint64(body, 1<<40)
	done := w.writeRaw(rawFrame(byte(dbwire.KindPutObject), 1, body))
	wantCode(t, w.readReply(), dbwire.CodeLimit)
	<-done
	w.wantClosed()

	w = f.connect(dbwire.RoleWriter)
	w.mustSnapshot()
	_, err := w.putObject(strings.Repeat("x", 601))
	wantCode(t, err, dbwire.CodeLimit)
	w.wantClosed()
	f.unchanged(before)
}

func TestCommit(t *testing.T) {
	f := newFixture(t, nil)
	w := f.connect(dbwire.RoleWriter)
	seq := w.mustSnapshot()
	staged := strings.Repeat("an object ", 100)
	if _, err := w.putObject(staged); err != nil {
		t.Fatal(err)
	}
	got, mark, err := w.commit(seq,
		put(2, "a", "1"), put(2, "b", "2"), del(2, "a"), put(2, "a", "3"), put(2, "gone", "x"), del(2, "gone"),
		event("first"), put(3, "h", "history"), event("second"))
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 || !mark.Equal(testBase) || mark.Location() != time.UTC {
		t.Errorf("the reply is %d at %v, want 1 at %v", got, mark, testBase)
	}
	d := f.dump()
	if want := []string{`"a"="3"`, `"b"="2"`}; !slices.Equal(d.Tables[2], want) {
		t.Errorf("table 2 is %v, want %v (the ops are applied in order)", d.Tables[2], want)
	}
	if want := []string{`"h"="history"`}; !slices.Equal(d.Tables[3], want) {
		t.Errorf("table 3 is %v", d.Tables[3])
	}
	if len(d.Objects) != 1 || !strings.HasPrefix(d.Objects[0], hashHex(staged)+":") {
		t.Errorf("objects %v, want the staged one", d.Objects)
	}
	wantEvents := []string{
		`1@1:"first"`, `2@1:"second"`, fmt.Sprintf("3@1:%q", dbwire.AppendMark(nil, testBase)),
	}
	if !slices.Equal(d.Events, wantEvents) {
		t.Errorf("events %v, want %v", d.Events, wantEvents)
	}

	// The snapshot and the staged objects are dropped by a Commit.
	_, _, err = w.get(2, "a")
	wantCode(t, err, dbwire.CodeNoSnapshot)
	seq = w.mustSnapshot()
	if found, _, _ := w.object(hashOf(staged)); !found {
		t.Error("the committed object is not found")
	}
	_, _, err = w.commit(seq)
	wantCode(t, err, dbwire.CodeRule) // nothing staged any more, and no op

	t.Run("events only, objects only", func(t *testing.T) {
		seq := w.mustSnapshot()
		got, mark, err := w.commit(seq, event("only"))
		if err != nil || got != 2 || !mark.Equal(testBase.Add(time.Second)) {
			t.Errorf("events only: %d %v %v", got, mark, err)
		}
		seq = w.mustSnapshot()
		if _, err := w.putObject("another object"); err != nil {
			t.Fatal(err)
		}
		if got, _, err := w.commit(seq); err != nil || got != 3 {
			t.Errorf("objects only: %d %v", got, err)
		}
		last := f.dump().Events
		if want := fmt.Sprintf("6@3:%q", dbwire.AppendMark(nil, testBase.Add(2*time.Second))); last[len(last)-1] != want {
			t.Errorf("the last event is %s, want %s", last[len(last)-1], want)
		}
	})

	t.Run("a clock that goes back", func(t *testing.T) {
		back := testBase.Add(-48 * time.Hour)
		f.clock.set(back)
		w.mustCommit(put(2, "k", "v"))
		last := f.dump().Events
		if want := fmt.Sprintf("%q", dbwire.AppendMark(nil, back)); !strings.HasSuffix(last[len(last)-1], want) {
			t.Errorf("the mark is %s, want the time %v as it is", last[len(last)-1], back)
		}
	})

	t.Run("the marks follow the events", func(t *testing.T) {
		events, err := func() ([]dbwire.Event, error) {
			r := f.connect(dbwire.RoleReader)
			r.mustSnapshot()
			return r.events()
		}()
		if err != nil {
			t.Fatal(err)
		}
		marks := 0
		for i, e := range events {
			if e.Number != uint64(i+1) {
				t.Errorf("event %d has the number %d", i+1, e.Number)
			}
			if _, ok := dbwire.ParseMark(e.Data); ok {
				marks++
				if i+1 < len(events) && events[i+1].Seq == e.Seq {
					t.Errorf("the mark of commit %d is not its last event", e.Seq)
				}
			}
		}
		if marks != 4 {
			t.Errorf("%d marks, want 4 (one for each commit)", marks)
		}
	})
}

func TestCommitRules(t *testing.T) {
	f := newFixture(t, nil)
	f.update(func(tx *logdb.Tx) {
		_ = tx.Put(2, []byte("k"), []byte("v"))
		_ = tx.Put(dbwire.TableHistory, []byte("h1"), []byte("old"))
		_ = tx.Put(dbwire.TableRevisions, []byte("r1"), []byte("old"))
		_ = tx.Put(dbwire.TableEntryRevisions, []byte("e1"), []byte("old"))
		_ = tx.Put(dbwire.TableHeads, []byte("activity"), []byte("audit head activity 3 sha256:00"))
	})
	w := f.connect(dbwire.RoleWriter)
	good := "audit head activity 4 sha256:00"
	for _, tc := range []struct {
		name     string
		code     dbwire.Code
		ops      []dbwire.Op
		expected int64  // added to the sequence number
		unstaged bool   // no object is staged first
		text     string // in it, the last sequence number and the expected one, if it has verbs
	}{
		{name: "a sequence number that is ahead", code: dbwire.CodeSeq, ops: []dbwire.Op{event("e")}, expected: 4, text: "%d, not the expected %d"},
		{name: "a sequence number that is behind", code: dbwire.CodeSeq, ops: []dbwire.Op{event("e")}, expected: -1, text: "%d, not the expected %d"},
		{name: "a put and no event", code: dbwire.CodeRule, ops: []dbwire.Op{put(2, "x", "y")}, text: "needs an audit event"},
		{name: "a delete and no event", code: dbwire.CodeRule, ops: []dbwire.Op{del(2, "k")}, text: "needs an audit event"},
		{name: "a delete of a key that is not there, and no event", code: dbwire.CodeRule, ops: []dbwire.Op{del(2, "none")}, text: "needs an audit event"},
		{name: "an empty event", code: dbwire.CodeRule, ops: []dbwire.Op{put(2, "x", "y"), event("e"), event("")}, text: "is empty"},
		{name: "an event that is the byte of the daemon", code: dbwire.CodeRule, ops: []dbwire.Op{event("\x00")}, text: "daemon's"},
		{name: "an event that is a mark", code: dbwire.CodeRule, ops: []dbwire.Op{event(string(dbwire.AppendMark(nil, testBase)))}, text: "daemon's"},
		{name: "an event of the daemon after other ops", code: dbwire.CodeRule, ops: []dbwire.Op{put(2, "x", "y"), event("e"), event("\x00text")}, text: "daemon's"},
		{name: "a put in history of a key that is there", code: dbwire.CodeRule, ops: []dbwire.Op{put(3, "h1", "new"), event("e")}, text: "append-only"},
		{name: "a put in revisions of a key that is there", code: dbwire.CodeRule, ops: []dbwire.Op{put(4, "r1", "new"), event("e")}, text: "append-only"},
		{name: "a put in entryRevisions of a key that is there", code: dbwire.CodeRule, ops: []dbwire.Op{put(5, "e1", "new"), event("e")}, text: "append-only"},
		{name: "a put of the same new key twice in history", code: dbwire.CodeRule, ops: []dbwire.Op{put(3, "h2", "a"), put(3, "h2", "b"), event("e")}, text: "append-only"},
		{name: "a put after a delete in history", code: dbwire.CodeRule, ops: []dbwire.Op{del(3, "h1"), put(3, "h1", "b"), event("e")}, text: "append-only"},
		{name: "a delete in history", code: dbwire.CodeRule, ops: []dbwire.Op{del(3, "h1"), event("e")}, text: "append-only"},
		{name: "a delete in revisions", code: dbwire.CodeRule, ops: []dbwire.Op{del(4, "r1"), event("e")}, text: "append-only"},
		{name: "a delete in entryRevisions of a key that is not there", code: dbwire.CodeRule, ops: []dbwire.Op{del(5, "zzz"), event("e")}, text: "append-only"},
		{name: "a row of heads with a key that has a space", code: dbwire.CodeRule, ops: []dbwire.Op{put(11, "bad key", good), event("e")}, text: "not a valid row of table 11"},
		{name: "a row of heads with no key", code: dbwire.CodeRule, ops: []dbwire.Op{put(11, "", good), event("e")}, text: "not a valid row of table 11"},
		{name: "a row of heads with a key of 65 bytes", code: dbwire.CodeRule, ops: []dbwire.Op{put(11, strings.Repeat("k", 65), good), event("e")}, text: "not a valid row of table 11"},
		{name: "a row of heads with a slash in the key", code: dbwire.CodeRule, ops: []dbwire.Op{put(11, "a/b", good), event("e")}, text: "not a valid row of table 11"},
		{name: "a row of heads with a line that has a control character", code: dbwire.CodeRule, ops: []dbwire.Op{put(11, "activity", "audit\nhead"), event("e")}, text: "not a valid row of table 11"},
		{name: "a row of heads with a line that is not ASCII", code: dbwire.CodeRule, ops: []dbwire.Op{put(11, "activity", "audit head é"), event("e")}, text: "not a valid row of table 11"},
		{name: "a row of heads with an empty line", code: dbwire.CodeRule, ops: []dbwire.Op{put(11, "activity", ""), event("e")}, text: "not a valid row of table 11"},
		{name: "a row of heads with a line of 256 bytes", code: dbwire.CodeRule, ops: []dbwire.Op{put(11, "activity", strings.Repeat("l", 256)), event("e")}, text: "not a valid row of table 11"},
		{name: "a delete in heads", code: dbwire.CodeRule, ops: []dbwire.Op{del(11, "activity"), event("e")}, text: "is not deleted"},
		{name: "no op and nothing staged", code: dbwire.CodeRule, unstaged: true, text: "no op and no staged object"},
		{name: "table 0", code: dbwire.CodeLimit, ops: []dbwire.Op{put(0, "k", "v"), event("e")}, text: "invalid table 0"},
		{name: "a delete in table 0", code: dbwire.CodeLimit, ops: []dbwire.Op{del(0, "k"), event("e")}, text: "invalid table 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, counted := f.dump(), f.fs.Counted()
			seq := w.mustSnapshot()
			if !tc.unstaged {
				if _, err := w.putObject("staged for " + tc.name); err != nil {
					t.Fatal(err)
				}
			}
			expected := uint64(int64(seq) + tc.expected)
			_, _, err := w.commit(expected, tc.ops...)
			re := wantCode(t, err, tc.code)
			want := tc.text
			if strings.Contains(want, "%d") {
				want = fmt.Sprintf(want, seq, expected)
			}
			if !strings.Contains(re.Msg, want) {
				t.Errorf("the text is %q, want %q in it", re.Msg, want)
			}
			f.unchanged(before)
			if n := f.fs.Counted(); n != counted {
				t.Errorf("the refusal made %d changes in the files", n-counted)
			}
			// The writer goes on: the snapshot and the staged object were dropped.
			_, _, err = w.get(2, "k")
			wantCode(t, err, dbwire.CodeNoSnapshot)
			if _, _, err := w.tryCommit(put(2, "after", "ok")); err != nil {
				t.Fatalf("a good commit after the refusal: %v", err)
			}
			if d := f.dump(); len(d.Objects) != 0 {
				t.Errorf("the staged object of a refusal was committed later: %v", d.Objects)
			}
			f.update(func(tx *logdb.Tx) { _ = tx.Delete(2, []byte("after")) })
		})
	}

	t.Run("what is allowed", func(t *testing.T) {
		for _, ops := range [][]dbwire.Op{
			{put(3, "h-new", "x"), event("e")},
			{put(4, "r-new", "x"), put(5, "e-new", "x"), event("e")},
			{put(11, "activity", good), put(11, "login.log_2", good), event("e")},
			{put(11, strings.Repeat("k", 64), strings.Repeat("l", 255)), event("e")},
			{del(2, "k"), event("e")},
			{put(2, "k", "again"), put(2, "k", "twice"), event("e")},
			{event("e")},
		} {
			if _, _, err := w.tryCommit(ops...); err != nil {
				t.Errorf("%v: %v", ops, err)
			}
		}
	})

	t.Run("a key over the limit", func(t *testing.T) {
		before := f.dump()
		w := f.connectAfter(w)
		seq := w.mustSnapshot()
		_, _, err := w.commit(seq, put(2, strings.Repeat("k", dbwire.MaxKey+1), "v"), event("e"))
		wantCode(t, err, dbwire.CodeLimit)
		w.wantClosed()
		f.unchanged(before)
	})
}

// connectAfter returns a writer connection after the writer c has gone.
func (f *fixture) connectAfter(c *client) *client {
	f.t.Helper()
	_ = c.conn.Close()
	eventually(f.t, "the writer to be gone", func() bool {
		f.srv.mu.Lock()
		defer f.srv.mu.Unlock()
		return f.srv.writers == 0
	})
	return f.connect(dbwire.RoleWriter)
}

func TestCommitLimits(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Limits.Commit = 100 })
	opsOf := func(size int) []dbwire.Op {
		// One event of size bytes in all: an event is 5 bytes and its data.
		return []dbwire.Op{event(strings.Repeat("e", size-5))}
	}
	w := f.connect(dbwire.RoleWriter)
	if w.hello.Commit != 100 {
		t.Fatalf("Hello says %d", w.hello.Commit)
	}
	seq := w.mustSnapshot()
	if _, _, err := w.commit(seq, opsOf(100)...); err != nil {
		t.Fatalf("ops of exactly the limit: %v", err)
	}
	before := f.dump()
	for name, ops := range map[string][]dbwire.Op{
		"one byte more":     opsOf(101),
		"many small ops":    slices.Repeat([]dbwire.Op{event("12345")}, 21), // 21 * 10 bytes
		"a value that lies": nil,
	} {
		t.Run(name, func(t *testing.T) {
			w := f.connectAfter(w)
			seq := w.mustSnapshot()
			var err error
			if ops != nil {
				_, _, err = w.commit(seq, ops...)
				wantCode(t, err, dbwire.CodeLimit)
			} else {
				// The claim of a value of 10 MB is refused when it is made, before any of its bytes are read.
				body := binary.BigEndian.AppendUint64(nil, seq)
				body = append(body, byte(dbwire.OpPut))
				body = binary.BigEndian.AppendUint32(body, 2)
				body = binary.BigEndian.AppendUint32(body, 1)
				body = append(body, 'k')
				body = binary.BigEndian.AppendUint32(body, 10_000_000)
				done := w.writeRaw(rawFrame(byte(dbwire.KindCommit), 1, body))
				wantCode(t, w.readReply(), dbwire.CodeLimit)
				<-done
			}
			w.wantClosed()
			f.unchanged(before)
		})
	}
}

func TestCommitRate(t *testing.T) {
	t.Run("the bucket", func(t *testing.T) {
		b := newBucket(5)
		now := time.Unix(1000, 0)
		var sleeps []time.Duration
		b.now = func() time.Time { return now }
		b.sleep = func(_ context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			now = now.Add(d)
			return nil
		}
		take := func() {
			t.Helper()
			if err := b.take(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		for range 5 { // a burst of the capacity does not wait
			take()
		}
		if len(sleeps) != 0 {
			t.Fatalf("a burst of 5 waited: %v", sleeps)
		}
		for range 3 { // the next ones are spaced at the rate: a fifth of a second
			take()
		}
		if want := []time.Duration{200 * time.Millisecond, 200 * time.Millisecond, 200 * time.Millisecond}; !slices.Equal(sleeps, want) {
			t.Errorf("waits %v, want %v", sleeps, want)
		}
		sleeps = nil
		now = now.Add(time.Hour) // the bucket fills up to its capacity, and no more
		for range 5 {
			take()
		}
		if len(sleeps) != 0 {
			t.Errorf("a full bucket waited: %v", sleeps)
		}
		take()
		if len(sleeps) != 1 {
			t.Errorf("the sixth after a pause: %v", sleeps)
		}
		sleeps = nil
		now = now.Add(100 * time.Millisecond) // half a token
		take()
		if want := []time.Duration{100 * time.Millisecond}; !slices.Equal(sleeps, want) {
			t.Errorf("waits %v, want %v", sleeps, want)
		}
		// The clock going back does not make the bucket wait for it, nor give it tokens.
		sleeps = nil
		now = now.Add(-time.Hour)
		take()
		if want := []time.Duration{300 * time.Millisecond}; !slices.Equal(sleeps, want) {
			t.Errorf("after the clock went back: %v, want %v", sleeps, want)
		}
	})

	t.Run("no limit", func(t *testing.T) {
		for _, rate := range []int{-1, 0} {
			b := newBucket(rate)
			b.sleep = func(context.Context, time.Duration) error { t.Fatal("slept"); return nil }
			for range 1000 {
				if err := b.take(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
		}
	})

	t.Run("a wait that is ended", func(t *testing.T) {
		b := newBucket(1)
		b.sleep = func(ctx context.Context, d time.Duration) error { return context.Canceled }
		if err := b.take(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := b.take(context.Background()); !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
		// The token that was reserved is given back: the next one waits for one token, not two.
		var waits []time.Duration
		b.sleep = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return nil }
		if err := b.take(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(waits) != 1 || waits[0] > time.Second+time.Millisecond {
			t.Errorf("waits %v, want one of a second at most", waits)
		}
	})

	t.Run("concurrent commits", func(t *testing.T) {
		b := newBucket(10)
		now := time.Unix(1000, 0)
		b.now = func() time.Time { return now }
		var mu sync.Mutex
		var waits []time.Duration
		b.sleep = func(_ context.Context, d time.Duration) error {
			mu.Lock()
			defer mu.Unlock()
			waits = append(waits, d)
			return nil
		}
		var wg sync.WaitGroup
		for range 13 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = b.take(context.Background())
			}()
		}
		wg.Wait()
		slices.Sort(waits)
		if want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond}; !slices.Equal(waits, want) {
			t.Errorf("waits %v, want %v", waits, want)
		}
	})

	t.Run("in the server", func(t *testing.T) {
		f := newFixture(t, func(o *Options) { o.Limits.CommitsPerSecond = 2 })
		now := time.Unix(1000, 0)
		var sleeps []time.Duration
		f.srv.bucket.now = func() time.Time { return now }
		f.srv.bucket.sleep = func(_ context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			now = now.Add(d)
			return nil
		}
		w := f.connect(dbwire.RoleWriter)
		for i := range 4 {
			if _, _, err := w.tryCommit(put(2, fmt.Sprint(i), "v")); err != nil {
				t.Fatalf("commit %d: %v", i, err)
			}
		}
		// The first two are the burst; the next two wait for a half second each, and are not refused.
		if want := []time.Duration{500 * time.Millisecond, 500 * time.Millisecond}; !slices.Equal(sleeps, want) {
			t.Errorf("waits %v, want %v", sleeps, want)
		}
		if d := f.dump(); d.Seq != 4 {
			t.Errorf("the sequence number is %d", d.Seq)
		}
	})

	t.Run("Close ends a wait", func(t *testing.T) {
		f := newFixture(t, func(o *Options) { o.Limits.CommitsPerSecond = 1 })
		waiting := make(chan struct{})
		f.srv.bucket.sleep = func(ctx context.Context, d time.Duration) error {
			close(waiting)
			<-ctx.Done()
			return ctx.Err()
		}
		w := f.connect(dbwire.RoleWriter)
		w.mustCommit(put(2, "a", "1"))
		res := make(chan error, 1)
		go func() {
			_, _, err := w.tryCommit(put(2, "b", "2"))
			res <- err
		}()
		select {
		case <-waiting:
		case <-time.After(waitLimit):
			t.Fatal("the commit did not wait")
		}
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		if err := <-res; err == nil {
			t.Error("the commit of a closed server was answered")
		}
	})
}

func TestMinFree(t *testing.T) {
	var free atomic.Uint64
	var ferr atomic.Pointer[error]
	f := newFixture(t, func(o *Options) {
		o.Limits.MinFree = 1000
		o.FreeSpace = func() (uint64, error) {
			if e := ferr.Load(); e != nil {
				return 0, *e
			}
			return free.Load(), nil
		}
	})
	w := f.connect(dbwire.RoleWriter)
	object := strings.Repeat("o", 100)
	seq := w.mustSnapshot()
	before, counted := f.dump(), f.fs.Counted()

	free.Store(1099) // an object of 100 bytes needs 1100
	_, err := w.putObject(object)
	re := wantCode(t, err, dbwire.CodeNoSpace)
	if want := "the free space is 1099 bytes; a write of 100 bytes must leave 1000"; re.Msg != want {
		t.Errorf("got %q, want %q", re.Msg, want)
	}
	if found, _, _ := w.object(hashOf(object)); found {
		t.Error("an object that was refused is staged")
	}
	free.Store(1100)
	if _, err := w.putObject(object); err != nil {
		t.Fatalf("with exactly enough: %v", err)
	}

	// The commit needs the free space for its ops (13+1+100 for the put, 5+1 for the event, 120 in all) and for what it commits.
	free.Store(1000 + 120 + 100 - 1)
	_, _, err = w.commit(seq, put(2, "k", strings.Repeat("v", 100)), event("e"))
	wantCode(t, err, dbwire.CodeNoSpace)
	f.unchanged(before)
	if f.fs.Counted() != counted {
		t.Error("the refusal wrote")
	}
	seq = w.mustSnapshot()
	if _, err := w.putObject(object); err != nil {
		t.Fatal(err)
	}
	free.Store(1000 + 120 + 100)
	if _, _, err := w.commit(seq, put(2, "k", strings.Repeat("v", 100)), event("e")); err != nil {
		t.Fatalf("with exactly enough: %v", err)
	}

	t.Run("an error of the file system", func(t *testing.T) {
		e := errors.New("no answer: " + secret)
		ferr.Store(&e)
		w.mustSnapshot()
		_, err := w.putObject("x")
		re := wantCode(t, err, dbwire.CodeFailed)
		if strings.Contains(re.Msg, secret) {
			t.Errorf("got %q", re.Msg)
		}
		ferr.Store(nil)
	})
}

func TestNoFreeSpaceCheck(t *testing.T) {
	// Without FreeSpace, or with a negative MinFree, nothing is refused for the space.
	for name, mod := range map[string]func(*Options){
		"no function": func(o *Options) { o.Limits.MinFree = 1 << 60 },
		"negative": func(o *Options) {
			o.Limits.MinFree = -1
			o.FreeSpace = func() (uint64, error) { return 0, nil }
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, mod)
			w := f.connect(dbwire.RoleWriter)
			seq := w.mustSnapshot()
			if _, err := w.putObject("x"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := w.commit(seq, event("e")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStopsAfterSyncFailure(t *testing.T) {
	syncOf := func(prefix string) simfs.Fault {
		return simfs.Fault{
			Match: func(op simfs.Op) bool { return op.Kind == simfs.OpSync && strings.HasPrefix(op.Path, prefix) },
			Err:   syscall.EIO, Times: 1,
		}
	}
	for _, tc := range []struct {
		name   string
		prefix string
		object string
	}{
		{"the log", "logdb/log/", ""},
		{"an object file", "logdb/objects/", strings.Repeat("an object in a file ", 50)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, nil)
			w := f.connect(dbwire.RoleWriter)
			w.mustCommit(schemaOp(), put(2, "a", "1"))
			seq := w.mustSnapshot()
			if tc.object != "" {
				if _, err := w.putObject(tc.object); err != nil {
					t.Fatal(err)
				}
			}
			before := f.dump()
			f.fs.Inject(syncOf(tc.prefix))
			_, _, err := w.commit(seq, put(2, "b", "2"+secret), event("e"+secret))
			re := wantCode(t, err, dbwire.CodeStopped)
			if want := "the store has stopped after a sync failed; restart the machine before starting the daemon again"; re.Msg != want {
				t.Errorf("got %q", re.Msg)
			}
			if lines := f.logs.all(); len(lines) != 1 || !strings.Contains(lines[0], "sync failed") || strings.Contains(lines[0], secret) {
				t.Errorf("log lines: %q", lines)
			}

			// Every later write is refused, whatever it is, and Hello says that the store has stopped.
			seq = w.mustSnapshot() // reads go on
			_, err = w.putObject("x")
			wantCode(t, err, dbwire.CodeStopped)
			_, _, err = w.commit(seq, event("e"))
			wantCode(t, err, dbwire.CodeStopped)
			_, _, err = w.commit(seq, put(2, "c", "3"), event("e"))
			wantCode(t, err, dbwire.CodeStopped)
			if found, v := w.mustGet(2, "a"); !found || v != "1" {
				t.Errorf("a read after the stop: %v %q", found, v)
			}
			r := f.connect(dbwire.RoleReader)
			if !r.hello.Stopped || r.hello.Seq != 1 {
				t.Errorf("Hello after the stop: %+v", r.hello)
			}
			r.mustSnapshot()
			if found, _ := r.mustGet(2, "b"); found {
				t.Error("a reader sees the commit that failed")
			}
			if found, v := r.mustGet(2, "a"); !found || v != "1" {
				t.Errorf("a reader does not see the last commit: %v %q", found, v)
			}
			f.unchanged(before)
			if lines := f.logs.all(); len(lines) != 1 {
				t.Errorf("log lines: %q, want one", lines)
			}
			// A new writer is stopped too.
			w2 := f.connectAfter(w)
			if !w2.hello.Stopped {
				t.Error("a new writer is not told that the store has stopped")
			}
			w2.mustSnapshot()
			_, err = w2.putObject("x")
			wantCode(t, err, dbwire.CodeStopped)
		})
	}
}

func TestStopOnce(t *testing.T) {
	f := newFixture(t, nil)
	cause := &fs.PathError{Op: "sync", Path: "data/logdb/log/x.log", Err: syscall.EIO}
	f.srv.stop(fmt.Errorf("%w: %w", logdb.ErrFailed, cause))
	f.srv.stop(fmt.Errorf("%w: %w", logdb.ErrFailed, errors.New(secret)))
	lines := f.logs.all()
	if !f.srv.stopped.Load() || len(lines) != 1 || !strings.Contains(lines[0], cause.Error()) || strings.Contains(lines[0], secret) {
		t.Errorf("stopped %v, log lines %q", f.srv.stopped.Load(), lines)
	}
}

func TestHookFailure(t *testing.T) {
	var fail atomic.Bool
	f := newFixture(t, func(o *Options) {
		o.Hook = func(point string) error {
			if point == "commit:gate" && fail.Load() {
				return errors.New("hook failed " + secret)
			}
			return nil
		}
	})
	w := f.connect(dbwire.RoleWriter)
	before := f.dump()
	fail.Store(true)
	_, _, err := w.tryCommit(put(2, "k", "v"))
	re := wantCode(t, err, dbwire.CodeFailed)
	if re.Msg != "the engine failed" {
		t.Errorf("got %q", re.Msg)
	}
	f.unchanged(before)
	fail.Store(false)
	w.mustCommit(put(2, "k", "v"))
}
