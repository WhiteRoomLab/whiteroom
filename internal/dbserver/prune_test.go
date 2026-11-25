package dbserver

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

// The records of these tests are the smallest that dbwire.ReadRecordID reads; an event of the log of events is as the store
// writes it: the records of a commit, then its mark.

var pruneNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// monthsAgo returns the time that is n months before pruneNow.
func monthsAgo(n int) time.Time { return pruneNow.AddDate(0, -n, 0) }

func auditRecord(log string, n uint64, prev string) []byte {
	if prev == "" {
		return fmt.Appendf(nil, `{"log":%q,"n":%d,"at":"2026-01-01T00:00:00Z","action":"x"}`, log, n)
	}
	return fmt.Appendf(nil, `{"log":%q,"n":%d,"prev":%q,"at":"2026-01-01T00:00:00Z","action":"x"}`, log, n, prev)
}

// loginChain is the records 1 to N of a login history that chain.
type loginChain struct {
	recs [][]byte
	sums [][sha256.Size]byte
}

func newLoginChain(n int) *loginChain {
	c := &loginChain{}
	prev := ""
	for i := 1; i <= n; i++ {
		data := auditRecord("login", uint64(i), prev)
		c.recs = append(c.recs, data)
		sum := sha256.Sum256(data)
		c.sums = append(c.sums, sum)
		prev = hashText(sum)
	}
	return c
}

func (c *loginChain) rec(n int) []byte { return c.recs[n-1] }

func (c *loginChain) sum(n int) [sha256.Size]byte { return c.sums[n-1] }

// commitAt commits events, then the mark at at, unless at is the zero time, in one commit of the engine.
func (f *fixture) commitAt(at time.Time, events ...[]byte) {
	f.t.Helper()
	f.update(func(tx *logdb.Tx) {
		for _, e := range events {
			if err := tx.AppendEvent(e); err != nil {
				f.t.Error(err)
			}
		}
		if !at.IsZero() {
			if err := tx.AppendEvent(dbwire.AppendMark(nil, at)); err != nil {
				f.t.Error(err)
			}
		}
	})
}

// loginFixture is a store whose commit i has the activity record i, the login record i of chain, and the mark marks[i-1]
// (none for the zero time), and whose last commit has the activity record 99 and a mark at pruneNow, which makes the cutoff
// a year before pruneNow. Its server is closed.
func loginFixture(t *testing.T, chain *loginChain, marks ...time.Time) *fixture {
	t.Helper()
	f := newFixture(t, nil)
	for i, at := range marks {
		f.commitAt(at, auditRecord("activity", uint64(i+1), ""), chain.rec(i+1))
	}
	f.anchor()
	if err := f.srv.Close(); err != nil {
		t.Fatal(err)
	}
	return f
}

// anchor commits the activity record 99 with a mark at pruneNow.
func (f *fixture) anchor() {
	f.t.Helper()
	f.commitAt(pruneNow, auditRecord("activity", 99, ""))
}

// commitLater opens the closed server of f, commits events with a mark at at, and closes it.
func (f *fixture) commitLater(at time.Time, events ...[]byte) {
	f.t.Helper()
	f.open()
	f.commitAt(at, events...)
	if err := f.srv.Close(); err != nil {
		f.t.Fatal(err)
	}
}

// compactLogin runs CompactLogin at now on the world of f.
func compactLogin(t *testing.T, f *fixture, now time.Time) Deleted {
	t.Helper()
	var d Deleted
	if err := maintain(t.Context(), f.fs, "data", f.opts, func(m *logdb.Maintenance) (err error) {
		d, err = CompactLogin(t.Context(), m, now)
		return err
	}); err != nil {
		t.Fatalf("CompactLogin() = %v", err)
	}
	return d
}

// word returns what an event is, in a word: Ln and An for the login and the activity record n, M for a mark, Dn for the
// deletion before the record n, Bn for the acceptance of the break at it, Hn for the hole that starts at the record n, Gn for
// the acceptance of the gap that starts at the record n, and ? for another event.
func word(data []byte) string {
	if dbwire.IsDaemonEvent(data) {
		if _, ok := dbwire.ParseMark(data); ok {
			return "M"
		}
		if d, ok := dbwire.ParseDeleted(data); ok {
			return fmt.Sprintf("D%d", d.First)
		}
		if a, ok := dbwire.ParseAccepted(data); ok {
			return fmt.Sprintf("B%d", a.N)
		}
		if h, ok := dbwire.ParseHole(data); ok {
			return fmt.Sprintf("H%d", h.First)
		}
		if g, ok := dbwire.ParseGap(data); ok {
			return fmt.Sprintf("G%d", g.First)
		}
		return "?"
	}
	log, n, _, ok := dbwire.ReadRecordID(data)
	switch {
	case ok && log == dbwire.LogLogin:
		return fmt.Sprintf("L%d", n)
	case ok && log == dbwire.LogActivity:
		return fmt.Sprintf("A%d", n)
	}
	return "?"
}

// view is what a store has of the login history: its events, as words and as the engine numbers them, the chain start,
// and the rows of the tables other than chains.
type view struct {
	events []logdb.Event
	shape  string
	start  *dbwire.ChainStart
	rows   map[string]string
	breaks int
	holes  int
	gaps   int
}

func viewOf(t *testing.T, f *fixture) view {
	t.Helper()
	var v view
	if err := maintain(t.Context(), f.fs, "data", f.opts, func(m *logdb.Maintenance) error {
		var words []string
		if err := m.Events(t.Context(), func(e logdb.Event) bool {
			v.events = append(v.events, e)
			words = append(words, word(e.Data))
			return true
		}); err != nil {
			return err
		}
		v.shape = strings.Join(words, " ")
		v.rows = map[string]string{}
		return m.View(t.Context(), func(tx *logdb.Tx) error {
			for table := uint32(1); table <= 16; table++ {
				tx.Scan(table, nil, func(k, val []byte) bool {
					switch {
					case table == dbwire.TableChains && string(k) == dbwire.LogLogin:
						start, ok := dbwire.ParseChainStart(val)
						if ok {
							v.start = &start
						}
					case table == dbwire.TableChains:
						if _, _, ok := dbwire.ParseChainHoleKey(k); ok {
							v.holes++
						} else if _, _, ok := dbwire.ParseChainGapKey(k); ok {
							v.gaps++
						} else {
							v.breaks++
						}
					default:
						v.rows[fmt.Sprintf("%d/%q", table, k)] = string(val)
					}
					return true
				})
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("reading the store: %v", err)
	}
	return v
}

// numbered returns the events by their numbers, with their sequence numbers.
func numbered(events []logdb.Event) map[uint64]string {
	m := map[uint64]string{}
	for _, e := range events {
		m[e.Number] = fmt.Sprintf("%d:%x", e.Seq, e.Data)
	}
	return m
}

// kept checks that every event of after is the one of before with its number, and that before has no other event than
// those of dropped, the words of the events that are gone.
func kept(t *testing.T, before, after view, dropped ...string) {
	t.Helper()
	was := numbered(before.events)
	got := numbered(after.events)
	var gone []string
	for _, e := range before.events {
		if _, ok := got[e.Number]; !ok {
			gone = append(gone, word(e.Data))
		}
	}
	for number, text := range got {
		if text != was[number] && !slices.Contains(after.newEvents(before), number) {
			t.Errorf("event %d is %s, was %q", number, text, was[number])
		}
	}
	if !slices.Equal(gone, dropped) {
		t.Errorf("the events %v are gone, want %v", gone, dropped)
	}
}

// newEvents returns the numbers of the events of v that are not in before, which is a store at an earlier time.
func (v view) newEvents(before view) []uint64 {
	was := numbered(before.events)
	var out []uint64
	for _, e := range v.events {
		if _, ok := was[e.Number]; !ok {
			out = append(out, e.Number)
		}
	}
	return out
}

func checkDeleted(t *testing.T, got, want Deleted) {
	t.Helper()
	if got.First != want.First || got.Last != want.Last || got.Stopped != want.Stopped || got.Schema != want.Schema ||
		!want.Cutoff.IsZero() && !got.Cutoff.Equal(want.Cutoff) {
		t.Errorf("CompactLogin() = %+v, want %+v", got, want)
	}
}

func checkStart(t *testing.T, v view, first uint64, prev [sha256.Size]byte, at time.Time) {
	t.Helper()
	if v.start == nil || v.start.First != first || v.start.Prev != prev || !v.start.At.Equal(at) {
		t.Errorf("the chain start is %+v, want record %d after %x at %v", v.start, first, prev[:4], at)
	}
}

func checkRows(t *testing.T, before, after view) {
	t.Helper()
	if !maps.Equal(before.rows, after.rows) {
		t.Errorf("the rows of the tables are\n%v\nwere\n%v", after.rows, before.rows)
	}
}

func TestCompactLoginDeletes(t *testing.T) {
	chain := newLoginChain(6)
	f := loginFixture(t, chain, monthsAgo(40), monthsAgo(30), monthsAgo(13), monthsAgo(11), monthsAgo(1), pruneNow)
	before := viewOf(t, f)
	if want := "A1 L1 M A2 L2 M A3 L3 M A4 L4 M A5 L5 M A6 L6 M A99 M"; before.shape != want {
		t.Fatalf("the store is %q, want %q", before.shape, want)
	}
	cutoff := pruneNow.AddDate(-1, 0, 0)
	checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 3, Cutoff: cutoff})
	after := viewOf(t, f)
	if want := "A1 M A2 M A3 M A4 L4 M A5 L5 M A6 L6 M A99 M D4 M"; after.shape != want {
		t.Errorf("the store is %q, want %q", after.shape, want)
	}
	kept(t, before, after, "L1", "L2", "L3")
	checkRows(t, before, after)
	checkStart(t, after, 4, chain.sum(3), pruneNow)
	last := after.events[len(after.events)-1]
	if at, ok := dbwire.ParseMark(last.Data); !ok || !at.Equal(pruneNow) {
		t.Errorf("the last event is %q, want the mark of now", last.Data)
	}
	deleted, ok := dbwire.ParseDeleted(after.events[len(after.events)-2].Data)
	if !ok || deleted.Log != dbwire.LogLogin || deleted.First != 4 || deleted.Prev != chain.sum(3) || !deleted.Cutoff.Equal(cutoff) {
		t.Errorf("the event of the deletion is %+v (%v), want record 4 after %x, with the cutoff %v", deleted, ok, chain.sum(3), cutoff)
	}
	if after.events[len(after.events)-2].Seq != last.Seq {
		t.Error("the event of the deletion is not in the commit of the mark")
	}

	// A second call finds nothing more, and changes no event and no row.
	checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{})
	second := viewOf(t, f)
	if second.shape != after.shape || !maps.Equal(numbered(second.events), numbered(after.events)) {
		t.Errorf("a second call made the store %q, was %q", second.shape, after.shape)
	}
	checkStart(t, second, 4, chain.sum(3), pruneNow)
	checkRows(t, after, second)

	// A year later the record 4 is old, and the record 5, whose mark is at the cutoff, is not.
	later := pruneNow.AddDate(0, 11, 0)
	f.commitLater(later, auditRecord("activity", 100, ""))
	checkDeleted(t, compactLogin(t, f, later), Deleted{First: 4, Last: 4, Cutoff: later.AddDate(-1, 0, 0)})
	third := viewOf(t, f)
	if want := "A1 M A2 M A3 M A4 M A5 L5 M A6 L6 M A99 M D4 M A100 M D5 M"; third.shape != want {
		t.Errorf("the store is %q, want %q", third.shape, want)
	}
	checkStart(t, third, 5, chain.sum(4), later)
}

func TestCompactLoginNothingOld(t *testing.T) {
	chain := newLoginChain(3)
	f := loginFixture(t, chain, monthsAgo(11), monthsAgo(5), pruneNow)
	before := viewOf(t, f)
	checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{Cutoff: pruneNow.AddDate(-1, 0, 0)})
	after := viewOf(t, f)
	if after.shape != before.shape || after.start != nil {
		t.Errorf("the store is %q with the start %+v, was %q", after.shape, after.start, before.shape)
	}
	kept(t, before, after)
	checkRows(t, before, after)
}

func TestCompactLoginEveryRecord(t *testing.T) {
	chain := newLoginChain(3)
	f := loginFixture(t, chain, monthsAgo(40), monthsAgo(30), monthsAgo(20))
	before := viewOf(t, f)
	checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 3, Cutoff: pruneNow.AddDate(-1, 0, 0)})
	after := viewOf(t, f)
	if want := "A1 M A2 M A3 M A99 M D4 M"; after.shape != want {
		t.Errorf("the store is %q, want %q", after.shape, want)
	}
	checkStart(t, after, 4, chain.sum(3), pruneNow)
	kept(t, before, after, "L1", "L2", "L3")
	checkRows(t, before, after)
	checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{})
}

// TestCompactLoginCutoff checks that the cutoff is a calendar year before the earlier of the time of the process and
// that of the newest mark: a clock that is far ahead, or one that is set back, does not make records old.
func TestCompactLoginCutoff(t *testing.T) {
	marks := []time.Time{monthsAgo(30), monthsAgo(8), monthsAgo(1)}
	newest := marks[2]
	build := func(t *testing.T) (*fixture, *loginChain) {
		chain := newLoginChain(3)
		f := newFixture(t, nil)
		for i, at := range marks {
			f.commitAt(at, chain.rec(i+1))
		}
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		return f, chain
	}
	t.Run("a clock far ahead", func(t *testing.T) {
		f, chain := build(t)
		now := pruneNow.AddDate(5, 0, 0)
		// The newest mark, a month before pruneNow, makes the cutoff 13 months before it: only the record 1 is older.
		checkDeleted(t, compactLogin(t, f, now), Deleted{First: 1, Last: 1, Cutoff: newest.AddDate(-1, 0, 0)})
		checkStart(t, viewOf(t, f), 2, chain.sum(1), now)
	})
	t.Run("a clock set back", func(t *testing.T) {
		f, chain := build(t)
		early := monthsAgo(25) // the cutoff is 37 months before pruneNow: no record is older
		checkDeleted(t, compactLogin(t, f, early), Deleted{Cutoff: early.AddDate(-1, 0, 0)})
		if v := viewOf(t, f); v.start != nil {
			t.Errorf("the chain start is %+v after a call with a clock that is set back", v.start)
		}
		now := monthsAgo(7) // before the newest mark, so that the cutoff is 19 months before pruneNow
		checkDeleted(t, compactLogin(t, f, now), Deleted{First: 1, Last: 1, Cutoff: now.AddDate(-1, 0, 0)})
		checkStart(t, viewOf(t, f), 2, chain.sum(1), now)
	})
	t.Run("a year is a calendar year", func(t *testing.T) {
		leap := time.Date(2028, 2, 29, 10, 0, 0, 0, time.UTC)
		cutoff := leap.AddDate(-1, 0, 0)
		if want := time.Date(2027, 3, 1, 10, 0, 0, 0, time.UTC); !cutoff.Equal(want) {
			t.Fatalf("a year before %v is %v, want %v", leap, cutoff, want)
		}
		// A record at the cutoff stays; one a nanosecond older goes.
		for _, tt := range []struct {
			at   time.Time
			want Deleted
		}{
			{cutoff.Add(-time.Nanosecond), Deleted{First: 1, Last: 1, Cutoff: cutoff}},
			{cutoff, Deleted{Cutoff: cutoff}},
		} {
			chain := newLoginChain(2)
			f := newFixture(t, nil)
			f.commitAt(tt.at, chain.rec(1))
			f.commitAt(leap, chain.rec(2))
			if err := f.srv.Close(); err != nil {
				t.Fatal(err)
			}
			checkDeleted(t, compactLogin(t, f, leap), tt.want)
		}
	})
	t.Run("a year is not 365 days", func(t *testing.T) {
		// 2024-02-29 is between: a year before 2024-03-10 10:00 is 2023-03-10 10:00, and 365 days before it is a day later.
		now := time.Date(2024, 3, 10, 10, 0, 0, 0, time.UTC)
		chain := newLoginChain(2)
		f := newFixture(t, nil)
		f.commitAt(time.Date(2023, 3, 10, 20, 0, 0, 0, time.UTC), chain.rec(1))
		f.commitAt(now, chain.rec(2))
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		checkDeleted(t, compactLogin(t, f, now), Deleted{Cutoff: time.Date(2023, 3, 10, 10, 0, 0, 0, time.UTC)})
	})
	t.Run("the time of the process is in UTC", func(t *testing.T) {
		chain := newLoginChain(2)
		f := newFixture(t, nil)
		f.commitAt(monthsAgo(13), chain.rec(1))
		f.commitAt(pruneNow, chain.rec(2))
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		zone := time.FixedZone("far", 14*3600)
		d := compactLogin(t, f, pruneNow.In(zone))
		if d.Cutoff.Location() != time.UTC || !d.Cutoff.Equal(pruneNow.AddDate(-1, 0, 0)) {
			t.Errorf("the cutoff is %v", d.Cutoff)
		}
		if v := viewOf(t, f); v.start == nil || v.start.At.Location() != time.UTC {
			t.Errorf("the chain start is %+v", v.start)
		}
	})
}

// TestCompactLoginPrefix checks that the records go as a prefix, the oldest first, and that a record is not dropped
// because it is old if one before it is not: after a clock that went back, or a commit that has no mark.
func TestCompactLoginPrefix(t *testing.T) {
	for _, tt := range []struct {
		name    string
		marks   []time.Time
		first   uint64 // the first record that remains; 0: no deletion
		shape   string
		stopped string
	}{
		{"old, young, old", []time.Time{monthsAgo(30), monthsAgo(2), monthsAgo(25)}, 2,
			"A1 M A2 L2 M A3 L3 M A99 M D2 M", ""},
		{"a clock that went back", []time.Time{monthsAgo(20), pruneNow, monthsAgo(40), monthsAgo(40)}, 2,
			"A1 M A2 L2 M A3 L3 M A4 L4 M A99 M D2 M", ""},
		{"no mark in the middle", []time.Time{monthsAgo(30), {}, monthsAgo(25)}, 2,
			"A1 M A2 L2 A3 L3 M A99 M D2 M", ""},
		{"records that predate the marks", []time.Time{{}, {}, monthsAgo(25)}, 0, "A1 L1 A2 L2 A3 L3 M A99 M", ""},
		{"no mark at the end", []time.Time{monthsAgo(30), monthsAgo(25), {}}, 3, "A1 M A2 M A3 L3 A99 M D3 M", ""},
		{"a mark at the cutoff", []time.Time{monthsAgo(30), monthsAgo(12), monthsAgo(25)}, 2,
			"A1 M A2 L2 M A3 L3 M A99 M D2 M", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			chain := newLoginChain(len(tt.marks))
			f := loginFixture(t, chain, tt.marks...)
			before := viewOf(t, f)
			want := Deleted{Cutoff: pruneNow.AddDate(-1, 0, 0)}
			var dropped []string
			if tt.first != 0 {
				want.First, want.Last = 1, tt.first-1
				for n := uint64(1); n < tt.first; n++ {
					dropped = append(dropped, fmt.Sprintf("L%d", n))
				}
			}
			checkDeleted(t, compactLogin(t, f, pruneNow), want)
			after := viewOf(t, f)
			if after.shape != tt.shape {
				t.Errorf("the store is %q, want %q", after.shape, tt.shape)
			}
			kept(t, before, after, dropped...)
			if tt.first == 0 && after.start != nil {
				t.Errorf("the chain start is %+v, want none", after.start)
			}
			if tt.first != 0 {
				checkStart(t, after, tt.first, chain.sum(int(tt.first)-1), pruneNow)
			}
		})
	}
}

// TestCompactLoginCommits checks that the records of one commit share the mark of the commit.
func TestCompactLoginCommits(t *testing.T) {
	chain := newLoginChain(5)
	f := newFixture(t, nil)
	f.commitAt(monthsAgo(30), auditRecord("activity", 1, ""), chain.rec(1), chain.rec(2))
	f.commitAt(monthsAgo(20), chain.rec(3))
	f.commitAt(monthsAgo(2), chain.rec(4), chain.rec(5))
	f.anchor()
	if err := f.srv.Close(); err != nil {
		t.Fatal(err)
	}
	before := viewOf(t, f)
	checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 3, Cutoff: pruneNow.AddDate(-1, 0, 0)})
	after := viewOf(t, f)
	if want := "A1 M M L4 L5 M A99 M D4 M"; after.shape != want {
		t.Errorf("the store is %q, want %q", after.shape, want)
	}
	kept(t, before, after, "L1", "L2", "L3")
	checkStart(t, after, 4, chain.sum(3), pruneNow)
}

// TestCompactLoginKeeps checks that nothing but the login records is dropped: not the activity records, which are older
// than the cutoff too, not the marks, and not what is no record, which stops the walk.
func TestCompactLoginKeeps(t *testing.T) {
	chain := newLoginChain(5)
	f := newFixture(t, nil)
	junk := []byte("not a record")
	f.commitAt(monthsAgo(40), auditRecord("activity", 1, ""), chain.rec(1))
	f.commitAt(monthsAgo(39), auditRecord("activity", 2, ""), chain.rec(2))
	f.commitAt(monthsAgo(38), auditRecord("activity", 3, ""), chain.rec(3))
	f.commitAt(monthsAgo(37), junk, auditRecord("activity", 4, ""), chain.rec(4))
	f.commitAt(monthsAgo(36), auditRecord("activity", 5, ""), chain.rec(5))
	f.anchor()
	if err := f.srv.Close(); err != nil {
		t.Fatal(err)
	}
	before := viewOf(t, f)
	// The walk stops at the event that is no record: the records before it go, except the last of them.
	checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 2, Cutoff: pruneNow.AddDate(-1, 0, 0),
		Stopped: "event 10: it is not an audit record"})
	after := viewOf(t, f)
	if want := "A1 M A2 M A3 L3 M ? A4 L4 M A5 L5 M A99 M D3 M"; after.shape != want {
		t.Errorf("the store is %q, want %q", after.shape, want)
	}
	kept(t, before, after, "L1", "L2")
}

// TestCompactLoginEvidence checks that the walk never takes away what shows that the chain is broken: it stops at the
// first record that does not follow its predecessor and at the first event that is no record, drops only what is before
// the record that precedes that one, says where, and drops no record that comes after.
func TestCompactLoginEvidence(t *testing.T) {
	chain := newLoginChain(6)
	other := func(log string, n uint64, prev string) []byte { return auditRecord(log, n, prev) }
	for _, tt := range []struct {
		name    string
		records [][]byte
		want    Deleted
		start   uint64 // the first record that remains; 0: no deletion
		shape   string
	}{
		{
			name:    "a record whose prev is not the hash of the one before it",
			records: [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), other("login", 4, hashText(chain.sum(1))), chain.rec(5)},
			want:    Deleted{First: 1, Last: 2, Stopped: "record 4: prev is not the hash of record 3"},
			start:   3,
			shape:   "A1 M A2 M A3 L3 M A4 L4 M A5 L5 M A99 M D3 M",
		},
		{
			name:    "a record whose number does not follow",
			records: [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), chain.rec(5), chain.rec(6)},
			want:    Deleted{First: 1, Last: 2, Stopped: "record 5: the number does not follow that of record 3"},
			start:   3,
			shape:   "A1 M A2 M A3 L3 M A4 L5 M A5 L6 M A99 M D3 M",
		},
		{
			name:    "the first record is not the first",
			records: [][]byte{chain.rec(2), chain.rec(3)},
			want:    Deleted{Stopped: "record 2: the first record of a log is number 1"},
			shape:   "A1 L2 M A2 L3 M A99 M",
		},
		{
			name:    "the first record has a prev",
			records: [][]byte{other("login", 1, hashText(chain.sum(1))), chain.rec(2)},
			want:    Deleted{Stopped: "record 1: the first record has the hash of one before it"},
			shape:   "A1 L1 M A2 L2 M A99 M",
		},
		{
			name:    "a record twice, the second after the others",
			records: [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), chain.rec(2)},
			want:    Deleted{First: 1, Last: 2, Stopped: "record 2: the number does not follow that of record 3"},
			start:   3,
			shape:   "A1 M A2 M A3 L3 M A4 L2 M A99 M D3 M",
		},
		{
			name:    "an event that is no record between the records",
			records: [][]byte{chain.rec(1), chain.rec(2), []byte("junk"), chain.rec(3), chain.rec(4)},
			want:    Deleted{First: 1, Last: 1, Stopped: "event 8: it is not an audit record"},
			start:   2,
			shape:   "A1 M A2 L2 M A3 ? M A4 L3 M A5 L4 M A99 M D2 M",
		},
		{
			name:    "a record of a log that is neither",
			records: [][]byte{chain.rec(1), chain.rec(2), other("other", 3, ""), chain.rec(3)},
			want:    Deleted{First: 1, Last: 1, Stopped: "event 8: it is not an audit record"},
			start:   2,
			shape:   "A1 M A2 L2 M A3 ? M A4 L3 M A99 M D2 M",
		},
		{
			name:    "an event of the daemon that cannot be read",
			records: [][]byte{chain.rec(1), chain.rec(2), {dbwire.DaemonEvent, 0x7f}, chain.rec(3)},
			want:    Deleted{First: 1, Last: 1, Stopped: "event 8: it is an event of the daemon that cannot be read"},
			start:   2,
			shape:   "A1 M A2 L2 M A3 ? M A4 L3 M A99 M D2 M",
		},
		{
			name:    "a record without a number",
			records: [][]byte{chain.rec(1), chain.rec(2), []byte(`{"log":"login","prev":"x"}`), chain.rec(3)},
			want:    Deleted{First: 1, Last: 1, Stopped: "event 8: it is not an audit record"},
			start:   2,
			shape:   "A1 M A2 L2 M A3 ? M A4 L3 M A99 M D2 M",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, nil)
			for i, rec := range tt.records {
				f.commitAt(monthsAgo(40), auditRecord("activity", uint64(i+1), ""), rec)
			}
			f.anchor()
			if err := f.srv.Close(); err != nil {
				t.Fatal(err)
			}
			before := viewOf(t, f)
			tt.want.Cutoff = pruneNow.AddDate(-1, 0, 0)
			checkDeleted(t, compactLogin(t, f, pruneNow), tt.want)
			after := viewOf(t, f)
			if after.shape != tt.shape {
				t.Errorf("the store is %q, want %q", after.shape, tt.shape)
			}
			var dropped []string
			for n := tt.want.First; n != 0 && n <= tt.want.Last; n++ {
				dropped = append(dropped, fmt.Sprintf("L%d", n))
			}
			kept(t, before, after, dropped...)
			if tt.start == 0 {
				if after.start != nil {
					t.Errorf("the chain start is %+v, want none", after.start)
				}
			} else {
				checkStart(t, after, tt.start, chain.sum(int(tt.start)-1), pruneNow)
			}
			// A second call finds the same stop, and deletes nothing.
			checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{Stopped: tt.want.Stopped})
			if again := viewOf(t, f); again.shape != after.shape {
				t.Errorf("a second call made the store %q, was %q", again.shape, after.shape)
			}
		})
	}
}

// TestCompactLoginStopped checks when the walk says that it stopped: when the evidence ended it, and not when the age of
// the records did, and not for an event that is no record with no login record after it.
func TestCompactLoginStopped(t *testing.T) {
	chain := newLoginChain(4)
	cutoff := pruneNow.AddDate(-1, 0, 0)
	t.Run("the age ends the deletion first", func(t *testing.T) {
		// The newest mark is 20 months old, so that the cutoff is 32 months old: the record 2, 25 months old, is not old enough,
		// but is older than a year, so that the walk goes on to the record 3, which does not follow.
		bad := auditRecord("login", 3, hashText(chain.sum(1)))
		f := newFixture(t, nil)
		f.commitAt(monthsAgo(40), chain.rec(1))
		f.commitAt(monthsAgo(25), chain.rec(2))
		f.commitAt(monthsAgo(25), bad)
		f.commitAt(monthsAgo(20), auditRecord("activity", 99, ""))
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 1, Cutoff: monthsAgo(20).AddDate(-1, 0, 0)})
		checkStart(t, viewOf(t, f), 2, chain.sum(1), pruneNow)
	})
	t.Run("an event that is no record, with no login record after it", func(t *testing.T) {
		f := newFixture(t, nil)
		for i := 1; i <= 3; i++ {
			f.commitAt(monthsAgo(40), chain.rec(i))
		}
		f.commitAt(monthsAgo(40), []byte("junk"), auditRecord("activity", 1, ""))
		f.anchor()
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 2, Cutoff: cutoff})
		checkStart(t, viewOf(t, f), 3, chain.sum(2), pruneNow)
	})
	t.Run("an event that is no record, before every login record", func(t *testing.T) {
		f := newFixture(t, nil)
		f.commitAt(monthsAgo(40), []byte("junk"))
		for i := 1; i <= 3; i++ {
			f.commitAt(monthsAgo(40), chain.rec(i))
		}
		f.anchor()
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		before := viewOf(t, f)
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{Cutoff: cutoff, Stopped: "event 1: it is not an audit record"})
		kept(t, before, viewOf(t, f))
	})
}

// setSchema puts the schema row of the store of f, whose server is closed, to version.
func (f *fixture) setSchema(version uint64) {
	f.t.Helper()
	f.open()
	if err := f.db().Update(f.t.Context(), func(tx *logdb.Tx) error {
		return tx.Put(dbwire.TableMeta, []byte(dbwire.MetaSchema), binary.AppendUvarint(nil, version))
	}); err != nil {
		f.t.Fatal(err)
	}
	if err := f.srv.Close(); err != nil {
		f.t.Fatal(err)
	}
}

// TestCompactLoginOlderSchema checks that a store of a table schema older than dbwire.TableVersion is compacted and loses no
// login record, whatever their age: the chain start and the event of a deletion are of the newest schema, which the first
// writer that opens the store makes it. The call says so only if it would have dropped something.
func TestCompactLoginOlderSchema(t *testing.T) {
	older := uint64(dbwire.TableVersion - 1)
	cutoff := pruneNow.AddDate(-1, 0, 0)
	generation := func(f *fixture) string {
		var names []string
		for name := range filesOf(f.fs, "logdb/log") {
			names = append(names, name)
		}
		slices.Sort(names)
		return strings.Join(names, " ")
	}
	t.Run("old records", func(t *testing.T) {
		chain := newLoginChain(6)
		f := loginFixture(t, chain, monthsAgo(40), monthsAgo(30), monthsAgo(13), monthsAgo(11), monthsAgo(1), pruneNow)
		f.setSchema(older)
		before, was := viewOf(t, f), generation(f)
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{Cutoff: cutoff, Schema: older})
		after := viewOf(t, f)
		kept(t, before, after)
		checkRows(t, before, after)
		if after.start != nil || after.shape != before.shape {
			t.Errorf("the store is %q with the chain start %+v, was %q", after.shape, after.start, before.shape)
		}
		if got := generation(f); got == was {
			t.Errorf("the log has the segments %q after the call: want a new generation", got)
		}
		// The call says it again, and the store is the same.
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{Cutoff: cutoff, Schema: older})
		kept(t, after, viewOf(t, f))
	})
	t.Run("nothing old", func(t *testing.T) {
		chain := newLoginChain(3)
		f := loginFixture(t, chain, monthsAgo(11), monthsAgo(5), pruneNow)
		f.setSchema(older)
		before := viewOf(t, f)
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{Cutoff: cutoff})
		kept(t, before, viewOf(t, f))
	})
	t.Run("a stop that the schema does not hide", func(t *testing.T) {
		chain := newLoginChain(4)
		bad := auditRecord("login", 4, hashText(chain.sum(1)))
		f := newFixture(t, nil)
		for i, rec := range [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), bad} {
			f.commitAt(monthsAgo(40), auditRecord("activity", uint64(i+1), ""), rec)
		}
		f.anchor()
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		f.setSchema(older)
		before := viewOf(t, f)
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{Cutoff: cutoff, Schema: older,
			Stopped: "record 4: prev is not the hash of record 3"})
		kept(t, before, viewOf(t, f))
	})
	t.Run("the records that a deletion left", func(t *testing.T) {
		chain := newLoginChain(5)
		f := newFixture(t, nil)
		for i := 1; i <= 5; i++ {
			f.commitAt(monthsAgo(i), auditRecord("activity", uint64(i), ""), chain.rec(i))
		}
		f.commitStart(3, chain.sum(2), cutoff)
		f.anchor()
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		f.setSchema(older)
		before := viewOf(t, f)
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{Cutoff: cutoff, Schema: older})
		kept(t, before, viewOf(t, f))
	})
	t.Run("the schema of the program", func(t *testing.T) {
		chain := newLoginChain(3)
		f := loginFixture(t, chain, monthsAgo(40), monthsAgo(30), monthsAgo(1))
		f.setSchema(dbwire.TableVersion)
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 2, Cutoff: cutoff})
	})
	t.Run("a store with no commit", func(t *testing.T) {
		f := newFixture(t, nil)
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{Cutoff: pruneNow.AddDate(-1, 0, 0)})
	})
}

// TestCompactLoginBreaks checks the accepted breaks of the table chains: the walk goes on across a record that has one
// for its hash and whose number follows that of the record before it, and not across one whose hash differs, or for
// another record, or whose number goes back or skips, or the first that is walked.
func TestCompactLoginBreaks(t *testing.T) {
	chain := newLoginChain(6)
	broken := auditRecord("login", 4, hashText(chain.sum(1)))         // does not follow record 3
	after := auditRecord("login", 5, hashText(sha256.Sum256(broken))) // follows it
	build := func(t *testing.T, records [][]byte, marks []time.Time, breakAt uint64, breakHash [sha256.Size]byte,
		steps ...func(t *testing.T, f *fixture)) *fixture {
		f := newFixture(t, nil)
		for i, rec := range records {
			f.commitAt(marks[i], auditRecord("activity", uint64(i+1), ""), rec)
		}
		f.update(func(tx *logdb.Tx) {
			if err := tx.Put(dbwire.TableChains, dbwire.ChainBreakKey(dbwire.LogLogin, breakAt),
				dbwire.AppendChainBreak(nil, dbwire.ChainBreak{Hash: breakHash, At: monthsAgo(1)})); err != nil {
				t.Error(err)
			}
			if err := tx.AppendEvent(dbwire.AppendAccepted(nil, dbwire.Accepted{Log: dbwire.LogLogin, N: breakAt, Hash: breakHash})); err != nil {
				t.Error(err)
			}
			if err := tx.AppendEvent(dbwire.AppendMark(nil, monthsAgo(1))); err != nil {
				t.Error(err)
			}
		})
		for _, step := range steps {
			step(t, f)
		}
		f.anchor()
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		return f
	}
	old := func(n int) []time.Time {
		var marks []time.Time
		for range n {
			marks = append(marks, monthsAgo(40))
		}
		return marks
	}
	cutoff := pruneNow.AddDate(-1, 0, 0)
	t.Run("an accepted break lets the walk go on", func(t *testing.T) {
		f := build(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), broken, after}, old(5), 4, sha256.Sum256(broken))
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 5, Cutoff: cutoff})
		v := viewOf(t, f)
		// The row and the event of the acceptance stay: the record 4 is gone, and they are what is left of the break.
		checkStart(t, v, 6, sha256.Sum256(after), pruneNow)
		if v.breaks != 1 || !strings.Contains(v.shape, "B4") {
			t.Errorf("the store is %q with %d breaks, want the acceptance of the break at 4 to stay", v.shape, v.breaks)
		}
	})
	t.Run("a break for another hash does not", func(t *testing.T) {
		f := build(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), broken, chain.rec(5)}, old(5), 4, chain.sum(4))
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 2, Cutoff: cutoff,
			Stopped: "record 4: prev is not the hash of record 3"})
		checkStart(t, viewOf(t, f), 3, chain.sum(2), pruneNow)
	})
	t.Run("a break for another record does not", func(t *testing.T) {
		f := build(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), broken, chain.rec(5)}, old(5), 5, sha256.Sum256(chain.rec(5)))
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 2, Cutoff: cutoff,
			Stopped: "record 4: prev is not the hash of record 3"})
	})
	t.Run("a number that goes back is not accepted", func(t *testing.T) {
		again := chain.rec(2)
		f := build(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), again}, old(4), 2, sha256.Sum256(again))
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 2, Cutoff: cutoff,
			Stopped: "record 2: the number does not follow that of record 3"})
	})
	t.Run("a gap is not accepted", func(t *testing.T) {
		// The record 4 is missing, and the break at the record 5 is accepted: a lost record is no break.
		f := build(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), chain.rec(5), chain.rec(6)}, old(5), 5, chain.sum(5))
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 2, Cutoff: cutoff,
			Stopped: "record 5: the number does not follow that of record 3"})
		checkStart(t, viewOf(t, f), 3, chain.sum(2), pruneNow)
	})
	t.Run("a number that skips with another prev is not accepted", func(t *testing.T) {
		skipping := auditRecord("login", 5, hashText(chain.sum(3)))
		f := build(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), skipping}, old(4), 5, sha256.Sum256(skipping))
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 2, Cutoff: cutoff,
			Stopped: "record 5: the number does not follow that of record 3"})
	})
	t.Run("a break at the first record that is walked is not accepted", func(t *testing.T) {
		f := build(t, [][]byte{chain.rec(2), chain.rec(3)}, old(2), 2, chain.sum(2))
		before := viewOf(t, f)
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{Cutoff: cutoff, Stopped: "record 2: the first record of a log is number 1"})
		kept(t, before, viewOf(t, f))
	})
	t.Run("the cut that lands at an accepted break goes back: the record before it stays", func(t *testing.T) {
		// The record 4 does not have the hash of the record 3, and a break at it is accepted: it cannot be the first that
		// remains, as VerifyAudit does not honor a break at the chain start.
		broken5 := auditRecord("login", 5, hashText(chain.sum(1)))
		six := auditRecord("login", 6, hashText(sha256.Sum256(broken5)))
		breakAt5 := putRow(dbwire.TableChains, dbwire.ChainBreakKey(dbwire.LogLogin, 5),
			dbwire.AppendChainBreak(nil, dbwire.ChainBreak{Hash: sha256.Sum256(broken5), At: monthsAgo(1)}))
		for _, tt := range []struct {
			name    string
			records [][]byte
			marks   []time.Time
			steps   []func(t *testing.T, f *fixture)
			want    Deleted
			first   uint64 // the first record that remains, and the hash of the record before it
			prev    [sha256.Size]byte
		}{
			{"at it", [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), broken, after}, append(old(3), monthsAgo(1), monthsAgo(1)), nil,
				Deleted{First: 1, Last: 2, Cutoff: cutoff}, 3, chain.sum(2)},
			{"just after it", [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), broken, after}, append(old(4), monthsAgo(1)), nil,
				Deleted{First: 1, Last: 4, Cutoff: cutoff}, 5, sha256.Sum256(broken)},
			{"after two of them in a row", [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), broken, broken5, six},
				append(old(4), monthsAgo(1), monthsAgo(1)), []func(t *testing.T, f *fixture){breakAt5},
				Deleted{First: 1, Last: 2, Cutoff: cutoff}, 3, chain.sum(2)},
		} {
			t.Run(tt.name, func(t *testing.T) {
				f := build(t, tt.records, tt.marks, 4, sha256.Sum256(broken), tt.steps...)
				before := viewOf(t, f)
				checkDeleted(t, compactLogin(t, f, pruneNow), tt.want)
				after := viewOf(t, f)
				var gone []string
				for n := uint64(1); n < tt.first; n++ {
					gone = append(gone, fmt.Sprintf("L%d", n))
				}
				kept(t, before, after, gone...)
				checkStart(t, after, tt.first, tt.prev, pruneNow)
			})
		}
	})
	t.Run("a break at the first record that remains is not accepted", func(t *testing.T) {
		// The chain starts at the record 3, and the records 1 and 2 are still there; the record 3 does not have the hash that
		// the chain start has of the record 2, and a break at it is accepted.
		first := auditRecord("login", 3, hashText(wrong32))
		f := build(t, [][]byte{chain.rec(1), chain.rec(2), first, auditRecord("login", 4, hashText(sha256.Sum256(first)))}, old(4), 3,
			sha256.Sum256(first), startAt(3, chain.sum(2)))
		before := viewOf(t, f)
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{Cutoff: cutoff,
			Stopped: "record 3: prev is not the hash that the chain start has of record 2"})
		kept(t, before, viewOf(t, f))
	})
}

// putHole is the step of a store that puts the row and the event of the hole of the login records first to last, whose last
// has the hash hash, with a mark, as the repair of the daemon does.
func putHole(first, last uint64, hash [sha256.Size]byte) func(t *testing.T, f *fixture) {
	return func(t *testing.T, f *fixture) {
		t.Helper()
		f.update(func(tx *logdb.Tx) {
			row := dbwire.AppendChainHole(nil, dbwire.ChainHole{Last: last, Hash: hash, At: monthsAgo(1)})
			if err := tx.Put(dbwire.TableChains, dbwire.ChainHoleKey(dbwire.LogLogin, first), row); err != nil {
				t.Error(err)
			}
			event := dbwire.AppendHole(nil, dbwire.Hole{Log: dbwire.LogLogin, First: first, Last: last, Hash: hash})
			if err := tx.AppendEvent(event); err != nil {
				t.Error(err)
			}
			if err := tx.AppendEvent(dbwire.AppendMark(nil, monthsAgo(1))); err != nil {
				t.Error(err)
			}
		})
	}
}

// putGap is the step of a store that puts the row and the event of the accepted gap of the login records first to last, whose
// last has the hash hash, with a mark, as whiteroom-db accept-break does.
func putGap(first, last uint64, hash [sha256.Size]byte) func(t *testing.T, f *fixture) {
	return func(t *testing.T, f *fixture) {
		t.Helper()
		f.update(func(tx *logdb.Tx) {
			row := dbwire.AppendChainHole(nil, dbwire.ChainHole{Last: last, Hash: hash, At: monthsAgo(1)})
			if err := tx.Put(dbwire.TableChains, dbwire.ChainGapKey(dbwire.LogLogin, first), row); err != nil {
				t.Error(err)
			}
			event := dbwire.AppendGap(nil, dbwire.Hole{Log: dbwire.LogLogin, First: first, Last: last, Hash: hash})
			if err := tx.AppendEvent(event); err != nil {
				t.Error(err)
			}
			if err := tx.AppendEvent(dbwire.AppendMark(nil, monthsAgo(1))); err != nil {
				t.Error(err)
			}
		})
	}
}

// ranges is a kind of row that tells records that are not there: the hole that a repair records, or the gap that
// whiteroom-db accept-break accepts, which the walk of CompactLogin passes by one rule.
type ranges struct {
	name string
	put  func(first, last uint64, hash [sha256.Size]byte) func(t *testing.T, f *fixture)
	// rows returns how many rows of the kind the store has, and word is the letter of its events in the shape.
	rows func(v view) int
	word string
}

var (
	holeRanges = ranges{"holes", putHole, func(v view) int { return v.holes }, "H"}
	gapRanges  = ranges{"gaps", putGap, func(v view) int { return v.gaps }, "G"}
)

// TestCompactLoginHoles checks the holes of the table chains that a repair records, and the gaps that whiteroom-db accept-break
// accepts: the walk goes on across a record that follows one as VerifyAudit has it, the record after one is never the first
// that remains, and a row that is not for the gap that the log has, as it is, passes nothing.
func TestCompactLoginHoles(t *testing.T) {
	for _, r := range []ranges{holeRanges, gapRanges} {
		t.Run(r.name, func(t *testing.T) { checkCompactLoginRanges(t, r) })
	}
	t.Run("a hole and a gap in one log", checkCompactLoginBoth)
}

// checkCompactLoginBoth checks a hole and a gap in one log, each passed by its own row.
func checkCompactLoginBoth(t *testing.T) {
	chain := newLoginChain(7)
	f := newFixture(t, nil)
	for i, n := range []int{1, 2, 4, 6, 7} {
		f.commitAt(monthsAgo(40), auditRecord("activity", uint64(i+1), ""), chain.rec(n))
	}
	putHole(3, 3, chain.sum(3))(t, f)
	putGap(5, 5, chain.sum(5))(t, f)
	f.anchor()
	if err := f.srv.Close(); err != nil {
		t.Fatal(err)
	}
	before := viewOf(t, f)
	checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 7, Cutoff: pruneNow.AddDate(-1, 0, 0)})
	after := viewOf(t, f)
	kept(t, before, after, "L1", "L2", "L4", "L6", "L7")
	checkStart(t, after, 8, chain.sum(7), pruneNow)
	if after.holes != 1 || after.gaps != 1 {
		t.Errorf("the store has %d rows of holes and %d of gaps, want the one of each that it had", after.holes, after.gaps)
	}
}

func checkCompactLoginRanges(t *testing.T, r ranges) {
	put := r.put
	chain := newLoginChain(6)
	cutoff := pruneNow.AddDate(-1, 0, 0)
	// build commits the login records, each with an activity record and the mark that marks says, and then the steps.
	build := func(t *testing.T, ns []int, marks []time.Time, steps ...func(t *testing.T, f *fixture)) *fixture {
		t.Helper()
		f := newFixture(t, nil)
		for i, n := range ns {
			f.commitAt(marks[i], auditRecord("activity", uint64(i+1), ""), chain.rec(n))
		}
		for _, step := range steps {
			step(t, f)
		}
		f.anchor()
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		return f
	}
	old := func(n int) []time.Time {
		var marks []time.Time
		for range n {
			marks = append(marks, monthsAgo(40))
		}
		return marks
	}
	dropped := func(ns ...int) []string {
		var words []string
		for _, n := range ns {
			words = append(words, fmt.Sprintf("L%d", n))
		}
		return words
	}
	for _, tt := range []struct {
		name  string
		ns    []int // the records that the store has
		marks []time.Time
		steps []func(t *testing.T, f *fixture)
		want  Deleted
		gone  []int  // the records that are gone after the call
		first uint64 // the first record that remains, and the hash of the one before it, 0 if the call writes no start
		prev  [sha256.Size]byte
	}{
		{"a hole lets the walk go on", []int{1, 2, 4, 5, 6}, old(5), []func(t *testing.T, f *fixture){put(3, 3, chain.sum(3))},
			Deleted{First: 1, Last: 6, Cutoff: cutoff}, []int{1, 2, 4, 5, 6}, 7, chain.sum(6)},
		{"a hole of two records", []int{1, 2, 5, 6}, old(4), []func(t *testing.T, f *fixture){put(3, 4, chain.sum(4))},
			Deleted{First: 1, Last: 6, Cutoff: cutoff}, []int{1, 2, 5, 6}, 7, chain.sum(6)},
		{"a hole at the start of the log", []int{2, 3, 4, 5, 6}, old(5), []func(t *testing.T, f *fixture){put(1, 1, chain.sum(1))},
			Deleted{First: 2, Last: 6, Cutoff: cutoff}, []int{2, 3, 4, 5, 6}, 7, chain.sum(6)},
		{"a hole at the end of the log", []int{1, 2, 3, 4, 5}, old(5), []func(t *testing.T, f *fixture){put(6, 6, chain.sum(6))},
			Deleted{First: 1, Last: 5, Cutoff: cutoff}, []int{1, 2, 3, 4, 5}, 6, chain.sum(5)},
		{"a hole at the chain start", []int{5, 6}, old(2), []func(t *testing.T, f *fixture){
			startAt(3, chain.sum(2)), put(3, 4, chain.sum(4))},
			Deleted{First: 5, Last: 6, Cutoff: cutoff}, []int{5, 6}, 7, chain.sum(6)},
		{"the cut that lands at the record after a hole goes back", []int{1, 2, 4, 5, 6},
			append(old(2), monthsAgo(5), monthsAgo(4), monthsAgo(3)), []func(t *testing.T, f *fixture){put(3, 3, chain.sum(3))},
			Deleted{First: 1, Last: 1, Cutoff: cutoff}, []int{1}, 2, chain.sum(1)},
		{"the cut that lands just after the record after a hole", []int{1, 2, 4, 5}, append(old(3), monthsAgo(5)),
			[]func(t *testing.T, f *fixture){put(3, 3, chain.sum(3))},
			Deleted{First: 1, Last: 4, Cutoff: cutoff}, []int{1, 2, 4}, 5, chain.sum(4)},
		{"two holes in a row of records", []int{1, 3, 5, 6}, old(4), []func(t *testing.T, f *fixture){
			put(2, 2, chain.sum(2)), put(4, 4, chain.sum(4))},
			Deleted{First: 1, Last: 6, Cutoff: cutoff}, []int{1, 3, 5, 6}, 7, chain.sum(6)},

		// A row that is not for the gap that the log has passes nothing: the walk stops at the record after it.
		{"a row that is one record wider", []int{1, 2, 4, 5, 6}, old(5), []func(t *testing.T, f *fixture){put(3, 4, chain.sum(4))},
			Deleted{First: 1, Last: 1, Cutoff: cutoff, Stopped: "record 4: the number does not follow that of record 2"}, []int{1}, 2,
			chain.sum(1)},
		{"a row that is one record narrower", []int{1, 2, 5, 6}, old(4), []func(t *testing.T, f *fixture){put(3, 3, chain.sum(3))},
			Deleted{First: 1, Last: 1, Cutoff: cutoff, Stopped: "record 5: the number does not follow that of record 2"}, []int{1}, 2,
			chain.sum(1)},
		{"a row that starts one record later", []int{1, 2, 4, 5, 6}, old(5), []func(t *testing.T, f *fixture){put(4, 4, chain.sum(3))},
			Deleted{First: 1, Last: 1, Cutoff: cutoff, Stopped: "record 4: the number does not follow that of record 2"}, []int{1}, 2,
			chain.sum(1)},
		{"a row with another hash", []int{1, 2, 4, 5, 6}, old(5), []func(t *testing.T, f *fixture){put(3, 3, wrong32)},
			Deleted{First: 1, Last: 1, Cutoff: cutoff, Stopped: "record 4: the number does not follow that of record 2"}, []int{1}, 2,
			chain.sum(1)},
		{"a row with the hash of the record before", []int{1, 2, 4, 5, 6}, old(5),
			[]func(t *testing.T, f *fixture){put(3, 3, chain.sum(2))},
			Deleted{First: 1, Last: 1, Cutoff: cutoff, Stopped: "record 4: the number does not follow that of record 2"}, []int{1}, 2,
			chain.sum(1)},
		{"a row that is one record narrower, at the chain start", []int{5, 6}, old(2), []func(t *testing.T, f *fixture){
			startAt(3, chain.sum(2)), put(3, 3, chain.sum(3))},
			Deleted{Cutoff: cutoff, Stopped: "record 5: the chain starts at record 3, which is not there"}, nil, 0, [sha256.Size]byte{}},
		{"a hole for the log that has no gap", []int{1, 2, 3, 4, 5, 6}, old(6), []func(t *testing.T, f *fixture){put(3, 3, chain.sum(3))},
			Deleted{First: 1, Last: 6, Cutoff: cutoff}, []int{1, 2, 3, 4, 5, 6}, 7, chain.sum(6)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := build(t, tt.ns, tt.marks, tt.steps...)
			before := viewOf(t, f)
			checkDeleted(t, compactLogin(t, f, pruneNow), tt.want)
			after := viewOf(t, f)
			kept(t, before, after, dropped(tt.gone...)...)
			if tt.first == 0 {
				if after.start != nil && before.start == nil {
					t.Errorf("the chain start is %+v, want none written", after.start)
				}
			} else {
				checkStart(t, after, tt.first, tt.prev, pruneNow)
			}
			// The rows and the events stay, whatever is dropped: the records that they tell of are not there.
			if r.rows(after) != r.rows(before) || strings.Count(after.shape, r.word) != strings.Count(before.shape, r.word) {
				t.Errorf("the store has %d rows of %s and the events %q, want the %d and the events that it had", r.rows(after), r.name,
					after.shape, r.rows(before))
			}
		})
	}
}

// TestCompactLoginHolesLeftover checks a hole or an accepted gap among the records that a deletion left before the chain start:
// they are dropped as VerifyAudit reads them, and a row that does not match stops the walk at the record after it.
func TestCompactLoginHolesLeftover(t *testing.T) {
	for _, r := range []ranges{holeRanges, gapRanges} {
		t.Run(r.name, func(t *testing.T) { checkCompactLoginLeftover(t, r) })
	}
}

func checkCompactLoginLeftover(t *testing.T, r ranges) {
	chain := newLoginChain(6)
	deletedCutoff := monthsAgo(20)
	cutoff := pruneNow.AddDate(-1, 0, 0)
	// The records 1, 2 and 4 are the records before the chain start, which is at the record 5, and the record 3 is lost.
	mk := func(t *testing.T, hole func(t *testing.T, f *fixture)) *fixture {
		f := newFixture(t, nil)
		for i, n := range []int{1, 2, 4, 5, 6} {
			f.commitAt(monthsAgo(1), auditRecord("activity", uint64(i+1), ""), chain.rec(n))
		}
		hole(t, f)
		f.commitStart(5, chain.sum(4), deletedCutoff)
		f.anchor()
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		return f
	}
	t.Run("a row among them", func(t *testing.T) {
		f := mk(t, r.put(3, 3, chain.sum(3)))
		before := viewOf(t, f)
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 4, Cutoff: deletedCutoff})
		after := viewOf(t, f)
		kept(t, before, after, "L1", "L2", "L4")
		checkStart(t, after, 5, chain.sum(4), pruneNow)
		if r.rows(after) != 1 {
			t.Errorf("the store has %d rows of %s, want the one that it had", r.rows(after), r.name)
		}
	})
	t.Run("a row that does not match", func(t *testing.T) {
		f := mk(t, r.put(3, 3, wrong32))
		before := viewOf(t, f)
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{Cutoff: cutoff, Stopped: "record 4: the number does not follow that of record 2"})
		kept(t, before, viewOf(t, f))
	})
	t.Run("no row", func(t *testing.T) {
		f := mk(t, func(*testing.T, *fixture) {})
		before := viewOf(t, f)
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{Cutoff: cutoff, Stopped: "record 4: the number does not follow that of record 2"})
		kept(t, before, viewOf(t, f))
	})
}

// TestCompactLoginUnreadableRows checks that a row of the table chains that cannot be read stops the walk: nothing is
// deleted.
func TestCompactLoginUnreadableRows(t *testing.T) {
	chain := newLoginChain(3)
	for _, tt := range []struct {
		name       string
		key, value []byte
		stopped    string
	}{
		{"a chain start", dbwire.ChainStartKey(dbwire.LogLogin), []byte("short"), "the table chains: the chain start cannot be read"},
		{"a break", dbwire.ChainBreakKey(dbwire.LogLogin, 2), []byte("short"),
			"the table chains: a row of an accepted break cannot be read"},
		{"a break of record 0", dbwire.ChainBreakKey(dbwire.LogLogin, 0), dbwire.AppendChainBreak(nil, dbwire.ChainBreak{At: pruneNow}),
			"the table chains: a row of an accepted break cannot be read"},
		{"a break with a key that is too long", append(dbwire.ChainBreakKey(dbwire.LogLogin, 2), 0),
			dbwire.AppendChainBreak(nil, dbwire.ChainBreak{At: pruneNow}), "the table chains: a row of an accepted break cannot be read"},
		{"a hole", dbwire.ChainHoleKey(dbwire.LogLogin, 2), []byte("short"), "the table chains: a row of a hole cannot be read"},
		{"a hole of record 0", dbwire.ChainHoleKey(dbwire.LogLogin, 0), dbwire.AppendChainHole(nil, dbwire.ChainHole{Last: 2, At: pruneNow}),
			"the table chains: a row of a hole cannot be read"},
		{"a hole that ends before it starts", dbwire.ChainHoleKey(dbwire.LogLogin, 3), dbwire.AppendChainHole(nil, dbwire.ChainHole{Last: 2, At: pruneNow}),
			"the table chains: a row of a hole cannot be read"},
		{"a hole with a key that is too long", append(dbwire.ChainHoleKey(dbwire.LogLogin, 2), 0),
			dbwire.AppendChainHole(nil, dbwire.ChainHole{Last: 2, At: pruneNow}), "the table chains: a row of a hole cannot be read"},
		{"a hole with the value of a break", dbwire.ChainHoleKey(dbwire.LogLogin, 2), dbwire.AppendChainBreak(nil, dbwire.ChainBreak{At: pruneNow}),
			"the table chains: a row of a hole cannot be read"},
		{"a hole of a log with a longer name", append([]byte("login\x01\x01"), dbwire.ChainHoleKey(dbwire.LogLogin, 2)[6:]...),
			dbwire.AppendChainHole(nil, dbwire.ChainHole{Last: 2, At: pruneNow}), "the table chains: a row of a hole cannot be read"},
		{"a gap", dbwire.ChainGapKey(dbwire.LogLogin, 2), []byte("short"), "the table chains: a row of an accepted gap cannot be read"},
		{"a gap of record 0", dbwire.ChainGapKey(dbwire.LogLogin, 0), dbwire.AppendChainHole(nil, dbwire.ChainHole{Last: 2, At: pruneNow}),
			"the table chains: a row of an accepted gap cannot be read"},
		{"a gap that ends before it starts", dbwire.ChainGapKey(dbwire.LogLogin, 3),
			dbwire.AppendChainHole(nil, dbwire.ChainHole{Last: 2, At: pruneNow}), "the table chains: a row of an accepted gap cannot be read"},
		{"a gap with a key that is too long", append(dbwire.ChainGapKey(dbwire.LogLogin, 2), 0),
			dbwire.AppendChainHole(nil, dbwire.ChainHole{Last: 2, At: pruneNow}), "the table chains: a row of an accepted gap cannot be read"},
		{"a gap with the value of a break", dbwire.ChainGapKey(dbwire.LogLogin, 2), dbwire.AppendChainBreak(nil, dbwire.ChainBreak{At: pruneNow}),
			"the table chains: a row of an accepted gap cannot be read"},
		{"a gap of a log with a longer name", append([]byte("login\x02\x02"), dbwire.ChainGapKey(dbwire.LogLogin, 2)[6:]...),
			dbwire.AppendChainHole(nil, dbwire.ChainHole{Last: 2, At: pruneNow}), "the table chains: a row of an accepted gap cannot be read"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, nil)
			for i := 1; i <= 3; i++ {
				f.commitAt(monthsAgo(40), chain.rec(i))
			}
			f.update(func(tx *logdb.Tx) {
				if err := tx.Put(dbwire.TableChains, tt.key, tt.value); err != nil {
					t.Error(err)
				}
			})
			f.anchor()
			if err := f.srv.Close(); err != nil {
				t.Fatal(err)
			}
			before := viewOf(t, f)
			checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{Cutoff: pruneNow.AddDate(-1, 0, 0), Stopped: tt.stopped})
			kept(t, before, viewOf(t, f))
		})
	}
}

// TestCompactLoginUnreadableRowsTogether checks that a row of a break, of a hole and of a gap that cannot be read are told by
// the first of them in that order, as VerifyAudit tells it.
func TestCompactLoginUnreadableRowsTogether(t *testing.T) {
	chain := newLoginChain(3)
	f := newFixture(t, nil)
	for i := 1; i <= 3; i++ {
		f.commitAt(monthsAgo(40), chain.rec(i))
	}
	f.update(func(tx *logdb.Tx) {
		for _, key := range [][]byte{dbwire.ChainHoleKey(dbwire.LogLogin, 2), dbwire.ChainBreakKey(dbwire.LogLogin, 2)} {
			if err := tx.Put(dbwire.TableChains, key, []byte("short")); err != nil {
				t.Error(err)
			}
		}
	})
	f.anchor()
	if err := f.srv.Close(); err != nil {
		t.Fatal(err)
	}
	checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{Cutoff: pruneNow.AddDate(-1, 0, 0),
		Stopped: "the table chains: a row of an accepted break cannot be read"})

	// A row of a hole and a row of a gap: the hole is told.
	g := newFixture(t, nil)
	for i := 1; i <= 3; i++ {
		g.commitAt(monthsAgo(40), chain.rec(i))
	}
	g.update(func(tx *logdb.Tx) {
		for _, key := range [][]byte{dbwire.ChainGapKey(dbwire.LogLogin, 2), dbwire.ChainHoleKey(dbwire.LogLogin, 2)} {
			if err := tx.Put(dbwire.TableChains, key, []byte("short")); err != nil {
				t.Error(err)
			}
		}
	})
	g.anchor()
	if err := g.srv.Close(); err != nil {
		t.Fatal(err)
	}
	checkDeleted(t, compactLogin(t, g, pruneNow), Deleted{Cutoff: pruneNow.AddDate(-1, 0, 0),
		Stopped: "the table chains: a row of a hole cannot be read"})
}

// TestCompactLoginInterrupted checks that a deletion that is stopped between its commit and the end of its compaction
// leaves a store that the next call finishes, with the start that the first one wrote and no second one: the hook of the
// engine fails at the compaction, and the store is copied as a crash would leave it at each point.
func TestCompactLoginInterrupted(t *testing.T) {
	chain := newLoginChain(6)
	marks := []time.Time{monthsAgo(40), monthsAgo(30), monthsAgo(13), monthsAgo(11), monthsAgo(1), pruneNow}
	full := loginFixture(t, chain, marks...)
	beforeAll := viewOf(t, full)
	compactLogin(t, full, pruneNow)
	want := viewOf(t, full)
	if want.shape != "A1 M A2 M A3 M A4 L4 M A5 L5 M A6 L6 M A99 M D4 M" {
		t.Fatalf("the store after the deletion is %q", want.shape)
	}

	// finish calls CompactLogin on the store f, which a deletion has left in some state, and checks that it ends as full.
	finish := func(t *testing.T, f *fixture) {
		t.Helper()
		second := compactLogin(t, f, pruneNow)
		if second.First != 0 && (second.First != 1 || second.Last != 3) {
			t.Errorf("the second call dropped the records %d to %d, want 1 to 3 or none", second.First, second.Last)
		}
		got := viewOf(t, f)
		if got.shape != want.shape || !maps.Equal(numbered(got.events), numbered(want.events)) {
			t.Errorf("after the second call the store is %q, want %q", got.shape, want.shape)
		}
		checkStart(t, got, 4, chain.sum(3), pruneNow)
		checkRows(t, beforeAll, got)
		if third := compactLogin(t, f, pruneNow); third.First != 0 {
			t.Errorf("a third call dropped the records %d to %d", third.First, third.Last)
		}
	}

	hookErr := errors.New("hook failed")
	t.Run("the compaction fails", func(t *testing.T) {
		f := loginFixture(t, chain, marks...)
		o := f.opts
		o.Log.Hook = func(p string) error {
			if p == "compact:base" {
				return hookErr
			}
			return nil
		}
		err := maintain(t.Context(), f.fs, "data", o, func(m *logdb.Maintenance) error {
			_, err := CompactLogin(t.Context(), m, pruneNow)
			return err
		})
		if !errors.Is(err, hookErr) {
			t.Fatalf("CompactLogin() = %v, want the error of the hook", err)
		}
		// The start and the event are committed, and no record is gone.
		v := viewOf(t, f)
		if want := "A1 L1 M A2 L2 M A3 L3 M A4 L4 M A5 L5 M A6 L6 M A99 M D4 M"; v.shape != want {
			t.Errorf("the store is %q, want %q", v.shape, want)
		}
		checkStart(t, v, 4, chain.sum(3), pruneNow)
		finish(t, f)
	})

	t.Run("a crash at each point", func(t *testing.T) {
		f := loginFixture(t, chain, marks...)
		type image struct {
			point string
			world *simfs.FS
			model string
		}
		var images []image
		o := f.opts
		o.Log.Hook = func(p string) error {
			for _, m := range []simfs.Model{keepAll(f.fs), loseAll} {
				images = append(images, image{p, f.fs.Crash(m), m.Name})
			}
			return nil
		}
		if err := maintain(t.Context(), f.fs, "data", o, func(m *logdb.Maintenance) error {
			_, err := CompactLogin(t.Context(), m, pruneNow)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		points := map[string]bool{}
		for _, im := range images {
			points[im.point] = true
		}
		for _, p := range []string{"update:written", "update:synced", "compact:base", "compact:base-end", "compact:checkpoint",
			"compact:delete"} {
			if !points[p] {
				t.Errorf("the engine did not call the hook %q: %v", p, slices.Sorted(maps.Keys(points)))
			}
		}
		for _, im := range images {
			t.Run(im.point+" "+im.model, func(t *testing.T) {
				finish(t, &fixture{t: t, fs: im.world, opts: f.opts, logs: f.logs, clock: f.clock})
			})
		}
	})
}

// TestCompactLoginFailure checks that an error of the commit and one of the compaction, which stop the store, leave the
// mark of a failed sync, as Maintain does for any maintenance, and that nothing is written after the failure.
func TestCompactLoginFailure(t *testing.T) {
	chain := newLoginChain(4)
	marks := []time.Time{monthsAgo(40), monthsAgo(30), monthsAgo(1), pruneNow}
	inGen := func(gen int) func(simfs.Op) bool {
		return func(op simfs.Op) bool {
			return op.Kind == simfs.OpSync && strings.HasPrefix(op.Path, fmt.Sprintf("logdb/log/%016x-", gen))
		}
	}
	for _, tt := range []struct {
		name string
		gen  int // the generation whose sync fails
	}{
		{"the sync of the commit", 1},
		{"the sync of the compaction", 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := loginFixture(t, chain, marks...)
			var after map[string]string
			err := maintain(t.Context(), f.fs, "data", f.opts, func(m *logdb.Maintenance) error {
				f.fs.Inject(simfs.Fault{Match: inGen(tt.gen), Err: syscall.EIO, Times: 1})
				_, err := CompactLogin(t.Context(), m, pruneNow)
				after = filesOf(f.fs, "logdb")
				return err
			})
			if !errors.Is(err, logdb.ErrFailed) || !errors.Is(err, syscall.EIO) {
				t.Fatalf("Maintain() = %v, want an error that wraps ErrFailed and EIO", err)
			}
			if text, ok := worldFile(f.fs, "stopped"); !ok || !strings.Contains(text, "input/output error") {
				t.Errorf("the mark is %q (%v), want the mark of the failed sync", text, ok)
			}
			if got := filesOf(f.fs, "logdb"); !maps.Equal(got, after) {
				t.Errorf("the engine wrote after the failure:\n%v\nwas\n%v", slicesOf(got), slicesOf(after))
			}
			// The directory is refused until the machine has restarted.
			called, err := maintainCalled(t, f, f.opts)
			if called || !errors.Is(err, ErrNotRestarted) {
				t.Errorf("maintain after the failure: called %v, %v, want ErrNotRestarted", called, err)
			}
		})
	}

	t.Run("an error of the commit that is no sync", func(t *testing.T) {
		hookErr := errors.New("hook failed")
		f := loginFixture(t, chain, marks...)
		before := viewOf(t, f)
		o := f.opts
		o.Log.Hook = func(p string) error {
			if p == "update:objects" {
				return hookErr
			}
			return nil
		}
		err := maintain(t.Context(), f.fs, "data", o, func(m *logdb.Maintenance) error {
			_, err := CompactLogin(t.Context(), m, pruneNow)
			return err
		})
		if !errors.Is(err, hookErr) {
			t.Fatalf("CompactLogin() = %v", err)
		}
		if _, ok := worldFile(f.fs, "stopped"); ok {
			t.Error("the error that is no failed sync left a mark")
		}
		after := viewOf(t, f)
		if after.shape != before.shape || after.start != nil {
			t.Errorf("the store is %q with the start %+v, was %q", after.shape, after.start, before.shape)
		}
	})
}

// wrong32 is a hash that no record has.
var wrong32 = sha256.Sum256([]byte("another record"))

// commitChain commits the chain start before the record first, whose predecessor has the hash prev, the events, and a mark of
// pruneNow in one commit.
func (f *fixture) commitChain(first uint64, prev [sha256.Size]byte, events ...[]byte) {
	f.t.Helper()
	f.update(func(tx *logdb.Tx) {
		start := dbwire.ChainStart{First: first, Prev: prev, At: pruneNow}
		if err := tx.Put(dbwire.TableChains, dbwire.ChainStartKey(dbwire.LogLogin), dbwire.AppendChainStart(nil, start)); err != nil {
			f.t.Error(err)
		}
		for _, e := range append(events, dbwire.AppendMark(nil, pruneNow)) {
			if err := tx.AppendEvent(e); err != nil {
				f.t.Error(err)
			}
		}
	})
}

// commitStart is commitChain with the event of the deletion that made the start, which has cutoff, as CompactLogin commits it.
func (f *fixture) commitStart(first uint64, prev [sha256.Size]byte, cutoff time.Time) {
	f.t.Helper()
	f.commitChain(first, prev, dbwire.AppendDeleted(nil, dbwire.Deleted{Log: dbwire.LogLogin, First: first, Prev: prev, Cutoff: cutoff}))
}

// TestCompactLoginLeftover checks the login records before the chain start, which a deletion that stopped before the end of
// its compaction has left: a call that has nothing new to delete drops them, whatever their age, with the cutoff of that
// deletion, if they are exactly the end of the records before the chain start and the store has the event of the deletion;
// the walk goes on from the start; and a record before the start that comes after the first that remains is not dropped.
func TestCompactLoginLeftover(t *testing.T) {
	chain := newLoginChain(5)
	deletedCutoff := monthsAgo(20) // that of the deletion that left the records, which is not the cutoff of these calls
	mkWith := func(t *testing.T, marks []time.Time, startPrev [sha256.Size]byte, tail ...[]byte) *fixture {
		f := newFixture(t, nil)
		for i, at := range marks {
			f.commitAt(at, auditRecord("activity", uint64(i+1), ""), chain.rec(i+1))
		}
		for _, rec := range tail {
			f.commitAt(monthsAgo(1), rec)
		}
		f.commitStart(3, startPrev, deletedCutoff)
		f.anchor()
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		return f
	}
	mk := func(t *testing.T, marks []time.Time, tail ...[]byte) *fixture {
		return mkWith(t, marks, chain.sum(2), tail...)
	}
	old := []time.Time{monthsAgo(40), monthsAgo(39), monthsAgo(38), monthsAgo(37), monthsAgo(36)}
	young := []time.Time{monthsAgo(5), monthsAgo(4), monthsAgo(3), monthsAgo(2), monthsAgo(1)}
	cutoff := pruneNow.AddDate(-1, 0, 0)
	t.Run("a new deletion goes on from the start", func(t *testing.T) {
		marks := slices.Clone(old)
		marks[3], marks[4] = monthsAgo(3), monthsAgo(2)
		f := mk(t, marks)
		before := viewOf(t, f)
		// The cutoff of the call, not that of the deletion that left the records.
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 3, Cutoff: cutoff})
		after := viewOf(t, f)
		checkStart(t, after, 4, chain.sum(3), pruneNow)
		kept(t, before, after, "L1", "L2", "L3")
		if want := "A1 M A2 M A3 M A4 L4 M A5 L5 M D3 M A99 M D4 M"; after.shape != want {
			t.Errorf("the store is %q, want %q", after.shape, want)
		}
	})
	t.Run("the first record that remains is not that of the start", func(t *testing.T) {
		f := mkWith(t, old, wrong32)
		before := viewOf(t, f)
		want := Deleted{Cutoff: cutoff, Stopped: "record 2: its hash is not the hash that the chain start has of it"}
		checkDeleted(t, compactLogin(t, f, pruneNow), want)
		after := viewOf(t, f)
		kept(t, before, after)
		checkStart(t, after, 3, wrong32, pruneNow)
		checkDeleted(t, compactLogin(t, f, pruneNow), want)
		kept(t, before, viewOf(t, f))
	})
	t.Run("nothing new to delete", func(t *testing.T) {
		f := mk(t, young)
		before := viewOf(t, f)
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 2, Cutoff: deletedCutoff})
		after := viewOf(t, f)
		kept(t, before, after, "L1", "L2")
		checkStart(t, after, 3, chain.sum(2), pruneNow)
		if len(after.events) != len(before.events)-2 {
			t.Errorf("the store has %d events, want %d: no deletion and no mark are added", len(after.events), len(before.events)-2)
		}
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{})
	})
	t.Run("a record before the start after the first that remains", func(t *testing.T) {
		f := mk(t, young, chain.rec(2)) // a second record 2, after the record 5
		before := viewOf(t, f)
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 2, Cutoff: deletedCutoff})
		after := viewOf(t, f)
		kept(t, before, after, "L1", "L2")
		if want := "A1 M A2 M A3 L3 M A4 L4 M A5 L5 M L2 M D3 M A99 M"; after.shape != want {
			t.Errorf("the store is %q, want %q: the second record 2 must be there", after.shape, want)
		}
	})
	t.Run("and the walk stops at it", func(t *testing.T) {
		f := mk(t, old, chain.rec(2))
		before := viewOf(t, f)
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 4, Cutoff: cutoff,
			Stopped: "record 2: the number does not follow that of record 5"})
		after := viewOf(t, f)
		kept(t, before, after, "L1", "L2", "L3", "L4")
		checkStart(t, after, 5, chain.sum(4), pruneNow)
		if want := "A1 M A2 M A3 M A4 M A5 L5 M L2 M D3 M A99 M D5 M"; after.shape != want {
			t.Errorf("the store is %q, want %q", after.shape, want)
		}
	})
	t.Run("a break that is accepted among them", func(t *testing.T) {
		// The deletion went across the break at the record 4, as the walk of a call does, and stopped before its compaction.
		f := newFixture(t, nil)
		for i, rec := range [][]byte{chain.rec(1), chain.rec(2), changedRecord(dbwire.LogLogin, 3, chain), chain.rec(4), chain.rec(5)} {
			f.commitAt(append(slices.Clone(old[:4]), monthsAgo(1))[i], auditRecord("activity", uint64(i+1), ""), rec)
		}
		f.update(func(tx *logdb.Tx) {
			row := dbwire.AppendChainBreak(nil, dbwire.ChainBreak{Hash: chain.sum(4), At: monthsAgo(1)})
			if err := tx.Put(dbwire.TableChains, dbwire.ChainBreakKey(dbwire.LogLogin, 4), row); err != nil {
				t.Error(err)
			}
		})
		f.commitStart(5, chain.sum(4), deletedCutoff)
		f.anchor()
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		before := viewOf(t, f)
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 4, Cutoff: deletedCutoff})
		kept(t, before, viewOf(t, f), "L1", "L2", "L3", "L4")
	})
	t.Run("an event that is no record stops the walk after its first record", func(t *testing.T) {
		f := newFixture(t, nil)
		for i, rec := range [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), []byte("junk"), chain.rec(4)} {
			f.commitAt(monthsAgo(40), auditRecord("activity", uint64(i+1), ""), rec)
		}
		f.commitStart(3, chain.sum(2), deletedCutoff)
		f.anchor()
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		before := viewOf(t, f)
		// The records before the start go, and the record 3 stays, with the event that is no record: it is the record before it.
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 2, Cutoff: deletedCutoff,
			Stopped: "event 11: it is not an audit record"})
		kept(t, before, viewOf(t, f), "L1", "L2")
	})
}

// TestCompactLoginLeftoverEvidence checks that the records before the chain start are kept, with everything else, and that the
// call says where it stopped, when they are not exactly what an earlier deletion leaves: they are evidence then, as any record
// of the login history that is not what the chain start says.
func TestCompactLoginLeftoverEvidence(t *testing.T) {
	chain := newLoginChain(6)
	cutoff := pruneNow.AddDate(-1, 0, 0)
	changed := func(n int) []byte { return changedRecord("login", n, chain) }
	const noEvent = "record 1: the store has no event of a deletion that left the records before record 3"
	for _, tt := range []struct {
		name    string
		records [][]byte
		first   uint64 // the chain start is before this record, and has the hash that chain has of the one before it
		// The events that the commit of the chain start has: the event of the deletion that made it, unless this says otherwise.
		other   *dbwire.Deleted
		none    bool // no event
		stopped string
	}{
		{
			name:    "a record that does not follow its predecessor",
			records: [][]byte{changed(1), chain.rec(2), chain.rec(3), chain.rec(4)},
			first:   3,
			stopped: "record 2: prev is not the hash of record 1",
		},
		{
			name:    "the record before the start is changed",
			records: [][]byte{chain.rec(1), changed(2), chain.rec(3), chain.rec(4)},
			first:   3,
			stopped: "record 2: its hash is not the hash that the chain start has of it",
		},
		{
			name:    "a gap among them",
			records: [][]byte{chain.rec(1), chain.rec(3), chain.rec(4), chain.rec(5)},
			first:   4,
			stopped: "record 3: the number does not follow that of record 1",
		},
		{
			name:    "they end before the record before the start",
			records: [][]byte{chain.rec(1), chain.rec(2), chain.rec(4), chain.rec(5)},
			first:   4,
			stopped: "record 4: the number does not follow that of record 2",
		},
		{
			name:    "they end before the record before the start, and no record follows",
			records: [][]byte{chain.rec(1), chain.rec(2)},
			first:   4,
			stopped: "record 2: the chain starts at record 4, so the records before it end with record 3",
		},
		{
			name:    "the store has no event of the deletion",
			records: [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), chain.rec(4)},
			first:   3,
			none:    true,
			stopped: noEvent,
		},
		{
			name:    "the event is of a deletion that has another hash",
			records: [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), chain.rec(4)},
			first:   3,
			other:   &dbwire.Deleted{Log: dbwire.LogLogin, First: 3, Prev: wrong32, Cutoff: cutoff},
			stopped: noEvent,
		},
		{
			name:    "the event is of a deletion before another record",
			records: [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), chain.rec(4)},
			first:   3,
			other:   &dbwire.Deleted{Log: dbwire.LogLogin, First: 4, Prev: chain.sum(2), Cutoff: cutoff},
			stopped: noEvent,
		},
		{
			name:    "the event is of the other log",
			records: [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), chain.rec(4)},
			first:   3,
			other:   &dbwire.Deleted{Log: dbwire.LogActivity, First: 3, Prev: chain.sum(2), Cutoff: cutoff},
			stopped: noEvent,
		},
		{
			name:    "an event that is no record, before the first record that remains",
			records: [][]byte{chain.rec(1), chain.rec(2), []byte("junk"), chain.rec(3), chain.rec(4)},
			first:   3,
			stopped: "event 8: it is not an audit record",
		},
		{
			name:    "the first record is record 1 and has a prev",
			records: [][]byte{auditRecord("login", 1, hashText(wrong32)), chain.rec(2), chain.rec(3), chain.rec(4)},
			first:   3,
			stopped: "record 1: the first record has the hash of one before it",
		},
		{
			name:    "the first record is not record 1 and has no prev",
			records: [][]byte{auditRecord("login", 2, ""), chain.rec(3), chain.rec(4), chain.rec(5)},
			first:   4,
			stopped: "record 2: the record has no hash of the one before it, which only the first record has not",
		},
		{
			name:    "an event that is no record, with no record after it",
			records: [][]byte{chain.rec(1), chain.rec(2), []byte("junk")},
			first:   3,
			stopped: "event 8: it is not an audit record",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, nil)
			for i, rec := range tt.records {
				f.commitAt(monthsAgo(40), auditRecord("activity", uint64(i+1), ""), rec)
			}
			prev := chain.sum(int(tt.first) - 1)
			switch {
			case tt.none:
				f.commitChain(tt.first, prev)
			case tt.other != nil:
				f.commitChain(tt.first, prev, dbwire.AppendDeleted(nil, *tt.other))
			default:
				f.commitStart(tt.first, prev, cutoff)
			}
			f.anchor()
			if err := f.srv.Close(); err != nil {
				t.Fatal(err)
			}
			before := viewOf(t, f)
			want := Deleted{Cutoff: cutoff, Stopped: tt.stopped}
			checkDeleted(t, compactLogin(t, f, pruneNow), want)
			after := viewOf(t, f)
			kept(t, before, after)
			checkRows(t, before, after)
			checkStart(t, after, tt.first, prev, pruneNow)
			// A second call finds the same, and changes nothing.
			checkDeleted(t, compactLogin(t, f, pruneNow), want)
			if again := viewOf(t, f); again.shape != after.shape {
				t.Errorf("a second call made the store %q, was %q", again.shape, after.shape)
			}
		})
	}

	// A record of a number that was deleted, which the writer commits after a deletion that has left no login record, is no
	// tail of the deleted records, whatever its age: VerifyAudit reports it, and the next call keeps it.
	t.Run("a record of a deleted number, after a deletion that has left none", func(t *testing.T) {
		chain := newLoginChain(3)
		f := loginFixture(t, chain, monthsAgo(40), monthsAgo(38), monthsAgo(36))
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 3, Cutoff: cutoff})
		f.commitLater(pruneNow, chain.rec(1))
		before := viewOf(t, f)
		want := Deleted{Cutoff: cutoff, Stopped: "record 1: the chain starts at record 4, so the records before it end with record 3"}
		checkDeleted(t, compactLogin(t, f, pruneNow), want)
		kept(t, before, viewOf(t, f))
	})
}

// TestCompactLoginMaintenanceMark checks that the mark in the commit of a deletion, of an acceptance of a break, or of a hole, is not the
// newest mark of the cutoff: a store that its writer has not committed to since is not cut further by the calls that follow,
// whatever the time of the maintenance. A commit of the writer that has only a mark moves it.
func TestCompactLoginMaintenanceMark(t *testing.T) {
	chain := newLoginChain(5)
	// The records 1 to 4 are 40, 35, 25 and 20 months old; the last commit of the writer, 18 months ago, has no record.
	idle := func(t *testing.T, records [][]byte) *fixture {
		f := newFixture(t, nil)
		for i, rec := range records {
			f.commitAt([]time.Time{monthsAgo(40), monthsAgo(35), monthsAgo(25), monthsAgo(20)}[i], rec)
		}
		f.commitAt(monthsAgo(18), auditRecord("activity", 1, ""))
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		return f
	}
	cutoff := monthsAgo(18).AddDate(-1, 0, 0)
	t.Run("a deletion", func(t *testing.T) {
		f := idle(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), chain.rec(4)})
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 2, Cutoff: cutoff})
		after := viewOf(t, f)
		// The next call has the same newest mark, that of the writer: it drops nothing, and finds the same cutoff, also at a
		// time of the maintenance that is far ahead.
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{Cutoff: cutoff})
		checkDeleted(t, compactLogin(t, f, pruneNow.AddDate(3, 0, 0)), Deleted{Cutoff: cutoff})
		kept(t, after, viewOf(t, f))
		// A commit of the writer with only a mark, which one with staged objects and no op is, is the newest mark.
		f.commitLater(monthsAgo(1))
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 3, Last: 4, Cutoff: monthsAgo(1).AddDate(-1, 0, 0)})
	})
	t.Run("the acceptance of a break", func(t *testing.T) {
		// The record 3 is another one, so that the record 4 is a break.
		f := idle(t, [][]byte{chain.rec(1), chain.rec(2), changedRecord("login", 3, chain), chain.rec(4)})
		if err := acceptBreak(t, f, dbwire.LogLogin, 4); err != nil {
			t.Fatal(err)
		}
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 2, Cutoff: cutoff})
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{Cutoff: cutoff})
	})
	t.Run("the hole of a repair", func(t *testing.T) {
		// The record 4 is lost, and its hole is recorded after the last commit of the writer, with a mark of a year from the
		// time of that commit.
		f := idle(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), chain.rec(5)})
		f.open()
		putHole(4, 4, chain.sum(4))(t, f)
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 2, Cutoff: cutoff})
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{Cutoff: cutoff})
	})
	t.Run("a mark that is not in the commit of one of them", func(t *testing.T) {
		f := idle(t, [][]byte{chain.rec(1), chain.rec(2), chain.rec(3), chain.rec(4)})
		// The mark of a commit of its own is a mark like any other, whatever the event before it.
		f.open()
		f.commitAt(time.Time{}, dbwire.AppendAccepted(nil, dbwire.Accepted{Log: dbwire.LogLogin, N: 2, Hash: chain.sum(2)}))
		f.commitAt(pruneNow)
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		checkDeleted(t, compactLogin(t, f, pruneNow), Deleted{First: 1, Last: 4, Cutoff: pruneNow.AddDate(-1, 0, 0)})
	})
}
