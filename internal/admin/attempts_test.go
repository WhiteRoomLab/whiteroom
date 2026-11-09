package admin

import (
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const second = time.Second

type warnings struct {
	mu  sync.Mutex
	got []string
}

func (w *warnings) add(what string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.got = append(w.got, what)
}

// take returns the calls of warn since the last take.
func (w *warnings) take() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	got := w.got
	w.got = nil
	return got
}

// The names that warn gets.
const (
	whatPairs   = "login name and client pairs"
	whatNames   = "login names"
	whatClients = "clients"
)

// harness is attempts with a clock that the test moves, and the calls of warn. It checks the tables after every attempt.
type harness struct {
	t       *testing.T
	a       *attempts
	clock   *fakeClock
	warned  *warnings
	clients int
}

func newHarness(t *testing.T, capacity int) *harness {
	h := &harness{t: t, clock: newFakeClock(), warned: new(warnings)}
	h.a = newAttempts(h.clock.now, capacity, h.warned.add)
	// What the attempts remember is a time since their start, and the tests should not run at 0.
	h.advance(time.Hour)
	return h
}

// newSizedHarness is newHarness with other capacities for the tables of pairs, of login names and of clients.
func newSizedHarness(t *testing.T, pairs, names, clients int) *harness {
	h := newHarness(t, pairs)
	h.a.names.capacity = names
	h.a.clients.capacity = clients
	return h
}

func testName(i int) [32]byte { return nameKey(fmt.Sprintf("user%d", i)) }

func testClient(i int) netip.Prefix {
	return clientPrefix(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}))
}

// fresh returns a client that has not been used.
func (h *harness) fresh() netip.Prefix {
	h.clients++
	return testClient(1000000 + h.clients)
}

func (h *harness) advance(d time.Duration) { h.clock.advance(d) }

func (h *harness) check() {
	h.t.Helper()
	if err := h.a.check(); err != nil {
		h.t.Fatalf("the tables are not consistent: %v", err)
	}
}

func (h *harness) allow(what string, name [32]byte, client netip.Prefix) {
	h.t.Helper()
	if retry, ok := h.a.begin(name, client); !ok || retry != 0 {
		h.t.Errorf("%s: begin = %v, %v, want 0, true", what, retry, ok)
	}
	h.check()
}

func (h *harness) refuse(what string, name [32]byte, client netip.Prefix, retry time.Duration) {
	h.t.Helper()
	before := h.a.state()
	if got, ok := h.a.begin(name, client); ok || got != retry {
		h.t.Errorf("%s: begin = %v, %v, want %v, false", what, got, ok, retry)
	}
	h.check()
	if after := h.a.state(); !equalStates(before, after) {
		h.t.Errorf("%s: a refused attempt changed what is remembered:\nbefore %+v\nafter  %+v", what, before, after)
	}
}

// reach counts attempts of the pair until it has the count, waiting when it has to.
func (h *harness) reach(name [32]byte, client netip.Prefix, count int) {
	h.t.Helper()
	for range count {
		retry, ok := h.a.begin(name, client)
		if !ok {
			h.advance(retry)
			if again, ok := h.a.begin(name, client); !ok {
				h.t.Fatalf("an attempt after the wait of %v is refused for %v", retry, again)
			}
		}
	}
	h.check()
}

// fill counts count attempts on the login name, each from a client of its own, and returns the clients.
func (h *harness) fill(name [32]byte, count int) []netip.Prefix {
	h.t.Helper()
	clients := make([]netip.Prefix, count)
	for i := range clients {
		clients[i] = h.fresh()
		h.allow("an attempt that fills the login name", name, clients[i])
	}
	return clients
}

func (h *harness) hasPair(name, client int) bool {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	_, ok := h.a.pairs.entries[newPairKey(testName(name), testClient(client))]
	return ok
}

func (h *harness) hasName(i int) bool {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	_, ok := h.a.names.entries[testName(i)]
	return ok
}

func (h *harness) hasClient(i int) bool {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	_, ok := h.a.clients.entries[testClient(i)]
	return ok
}

// level returns the number of attempts that an entry which expires at expires has not got back yet.
func level(expires, now, leak time.Duration) int {
	if expires <= now {
		return 0
	}
	return int((expires - now + leak - 1) / leak)
}

// nameLevel returns the level of the login name over all clients.
func (h *harness) nameLevel(i int) int {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	e := h.a.names.entries[testName(i)]
	if e == nil {
		return 0
	}
	return level(e.expires, h.a.elapsed(), nameLeak)
}

func (h *harness) clientLevel(i int) int {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	e := h.a.clients.entries[testClient(i)]
	if e == nil {
		return 0
	}
	return level(e.expires, h.a.elapsed(), clientLeak)
}

// check reports how the tables are not consistent: the entries, the queue by expiry and the list of the entries that
// may be dropped have to agree.
func (a *attempts) check() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := checkTable(a.pairs, true); err != nil {
		return fmt.Errorf("pairs: %w", err)
	}
	if err := checkTable(a.names, false); err != nil {
		return fmt.Errorf("login names: %w", err)
	}
	if err := checkTable(a.clients, false); err != nil {
		return fmt.Errorf("clients: %w", err)
	}
	return nil
}

func checkTable[K comparable](t *table[K], counted bool) error {
	if len(t.entries) > t.capacity {
		return fmt.Errorf("%d entries, capacity %d", len(t.entries), t.capacity)
	}
	if len(t.queue) != len(t.entries) {
		return fmt.Errorf("%d entries, %d in the queue", len(t.entries), len(t.queue))
	}
	for i, e := range t.queue {
		switch {
		case int(e.index) != i:
			return fmt.Errorf("the entry at %d in the queue says %d", i, e.index)
		case t.entries[e.key] != e:
			return fmt.Errorf("the entry at %d in the queue is not that of its key", i)
		case i > 0 && t.queue[(i-1)/2].expires > e.expires:
			return fmt.Errorf("the queue is not ordered at %d", i)
		}
	}
	listed := 0
	for e := t.idle.root.next; e != &t.idle.root; e = e.next {
		listed++
		switch {
		case listed > len(t.entries):
			return fmt.Errorf("the list of entries that may be dropped has more than %d", len(t.entries))
		case e.next.prev != e || e.prev.next != e:
			return fmt.Errorf("the links of the list of entries that may be dropped are broken")
		case t.entries[e.key] != e:
			return fmt.Errorf("an entry in the list of entries that may be dropped is not that of its key")
		}
	}
	idle := 0
	for _, e := range t.entries {
		mayDrop := counted && e.count >= 1 && e.count < freeAttempts
		switch {
		case counted && e.count < 1:
			return fmt.Errorf("an entry has the count %d", e.count)
		case !counted && e.count != 0:
			return fmt.Errorf("an entry of a level has the count %d", e.count)
		case (e.prev != nil) != mayDrop || (e.next != nil) != mayDrop:
			return fmt.Errorf("an entry with the count %d is in the list of entries that may be dropped: %v", e.count, e.prev != nil)
		}
		if mayDrop {
			idle++
		}
	}
	if idle != listed {
		return fmt.Errorf("%d entries may be dropped, %d are in the list", idle, listed)
	}
	return nil
}

// state is what attempts remember, in a form that tests compare.
type state struct {
	pairs   map[pairKey][2]int64 // the count and when the entry expires
	names   map[[32]byte]int64
	clients map[netip.Prefix]int64
	idle    []pairKey // from the most recently used
}

func (a *attempts) state() state {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := state{pairs: map[pairKey][2]int64{}, names: map[[32]byte]int64{}, clients: map[netip.Prefix]int64{}}
	for k, e := range a.pairs.entries {
		s.pairs[k] = [2]int64{int64(e.count), int64(e.expires)}
	}
	for k, e := range a.names.entries {
		s.names[k] = int64(e.expires)
	}
	for k, e := range a.clients.entries {
		s.clients[k] = int64(e.expires)
	}
	for e := a.pairs.idle.root.next; e != &a.pairs.idle.root; e = e.next {
		s.idle = append(s.idle, e.key)
	}
	return s
}

func equalStates(x, y state) bool {
	if len(x.pairs) != len(y.pairs) || len(x.names) != len(y.names) || len(x.clients) != len(y.clients) || len(x.idle) != len(y.idle) {
		return false
	}
	for k, v := range x.pairs {
		if w, ok := y.pairs[k]; !ok || w != v {
			return false
		}
	}
	for k, v := range x.names {
		if w, ok := y.names[k]; !ok || w != v {
			return false
		}
	}
	for k, v := range x.clients {
		if w, ok := y.clients[k]; !ok || w != v {
			return false
		}
	}
	for i := range x.idle {
		if x.idle[i] != y.idle[i] {
			return false
		}
	}
	return true
}

func TestAttemptsNameKey(t *testing.T) {
	for _, tt := range []struct{ name, want string }{
		{"", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"abc", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
	} {
		k := nameKey(tt.name)
		if got := hex.EncodeToString(k[:]); got != tt.want {
			t.Errorf("nameKey(%q) = %s, want %s", tt.name, got, tt.want)
		}
	}
	if x, y := nameKey("alice"), nameKey("alice"); x != y {
		t.Error("the same login name has two keys")
	}
	if nameKey("alice") == nameKey("Alice") || nameKey("alice") == nameKey("alice ") || nameKey("a") == nameKey("b") {
		t.Error("different login names have the same key")
	}
	if nameKey(strings.Repeat("x", 1<<20)) == nameKey("x") {
		t.Error("a long login name has no key of its own")
	}
}

func TestAttemptsPairKey(t *testing.T) {
	name := testName(1)
	prefixes := []netip.Prefix{
		netip.MustParsePrefix("192.0.2.1/32"),
		netip.MustParsePrefix("192.0.2.2/32"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("::ffff:192.0.2.1/128"), // the 16 bytes of the first one
		netip.MustParsePrefix("2001:db8::/64"),
		netip.MustParsePrefix("2001:db8::/48"),
		netip.MustParsePrefix("2001:db8:0:1::/64"),
		netip.MustParsePrefix("::/0"),
		netip.MustParsePrefix("0.0.0.0/0"),
		{},
	}
	seen := map[pairKey]string{}
	add := func(k pairKey, what string) {
		if other, ok := seen[k]; ok {
			t.Errorf("%s and %s have the same key", what, other)
		}
		seen[k] = what
	}
	for _, p := range prefixes {
		add(newPairKey(name, p), "the login name 1 from "+p.String())
		add(newPairKey(testName(2), p), "the login name 2 from "+p.String())
	}
	if len(seen) != 2*len(prefixes) {
		t.Errorf("%d keys, want %d", len(seen), 2*len(prefixes))
	}
	if newPairKey(name, prefixes[4]) != newPairKey(name, netip.MustParsePrefix("2001:db8::/64")) {
		t.Error("the same login name and prefix have two keys")
	}
}

func TestAttemptsWaitAfter(t *testing.T) {
	for _, tt := range []struct {
		count int32
		want  time.Duration
	}{
		{0, 0}, {1, 0}, {4, 0},
		{5, 30 * second}, {6, time.Minute}, {7, 2 * time.Minute}, {8, 4 * time.Minute}, {9, 8 * time.Minute},
		{10, 16 * time.Minute}, {11, 32 * time.Minute}, {12, time.Hour}, {13, time.Hour}, {100, time.Hour},
		{math.MaxInt32, time.Hour},
	} {
		if got := waitAfter(tt.count); got != tt.want {
			t.Errorf("waitAfter(%d) = %v, want %v", tt.count, got, tt.want)
		}
	}
}

// TestAttemptsPairSchedule follows one login name from one client through its waits.
func TestAttemptsPairSchedule(t *testing.T) {
	h := newHarness(t, 100)
	name, client := testName(1), testClient(1)

	for i := 1; i <= 5; i++ {
		h.allow(fmt.Sprintf("attempt %d", i), name, client)
	}
	for i, wait := range []time.Duration{
		30 * second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute,
		time.Hour, time.Hour, time.Hour, time.Hour,
	} {
		what := fmt.Sprintf("attempt %d", 6+i)
		h.refuse(what+" at once", name, client, wait)
		h.advance(wait / 2)
		h.refuse(what+" after half the wait", name, client, wait/2)
		h.advance(wait/2 - 1)
		h.refuse(what+" a nanosecond early", name, client, second)
		h.advance(1)
		h.allow(what, name, client)
	}
	h.refuse("the attempt after the last", name, client, time.Hour)

	// Another client has attempts of its own on the same login name, at once.
	other := h.fresh()
	for i := 1; i <= 5; i++ {
		h.allow(fmt.Sprintf("attempt %d from another client", i), name, other)
	}
	h.refuse("attempt 6 from another client", name, other, 30*second)
	h.refuse("the attempt of the first client", name, client, time.Hour)

	// Waiting longer than the wait does not make the next one shorter.
	h.advance(10 * time.Hour)
	h.allow("attempt after 10 hours", name, client)
	h.refuse("the attempt after it", name, client, time.Hour)

	// A login name that is tried every hour for years never waits more than an hour.
	for range 2000 {
		h.advance(time.Hour)
		h.allow("an attempt every hour", name, client)
	}
	h.refuse("the attempt after them", name, client, time.Hour)
}

func TestAttemptsRetryIsInWholeSeconds(t *testing.T) {
	for _, tt := range []struct{ passed, retry time.Duration }{
		{0, 30 * second},
		{1, 30 * second},
		{500 * time.Millisecond, 30 * second},
		{500*time.Millisecond + 1, 30 * second},
		{second, 29 * second},
		{second + 1, 29 * second},
		{1500 * time.Millisecond, 29 * second},
		{29 * second, second},
		{29*second + 999*time.Millisecond, second},
		{30*second - 1, second},
	} {
		h := newHarness(t, 100)
		name, client := testName(1), testClient(1)
		h.reach(name, client, 5)
		h.advance(tt.passed)
		h.refuse(fmt.Sprintf("an attempt %v after the fifth", tt.passed), name, client, tt.retry)
	}
}

func TestAttemptsPairIsForgotten(t *testing.T) {
	// The 24 hours run from the last attempt.
	h := newHarness(t, 100)
	name, client := testName(1), testClient(1)
	h.reach(name, client, 12)
	h.advance(23 * time.Hour)
	h.allow("attempt 13, 23 hours after the 12th", name, client)
	h.advance(24*time.Hour - 1)
	h.allow("attempt 14, 24 hours less a nanosecond after the 13th", name, client)
	h.refuse("attempt 15", name, client, time.Hour)

	// A pair whose last attempt is 24 hours old counts again from nothing.
	h.advance(24 * time.Hour)
	for i := 1; i <= 5; i++ {
		h.allow(fmt.Sprintf("attempt %d after 24 hours", i), name, client)
	}
	h.refuse("attempt 6 after 24 hours", name, client, 30*second)

	// This holds for a pair that has not come to wait too.
	h = newHarness(t, 100)
	h.reach(name, client, 4)
	h.advance(24*time.Hour - 1)
	h.allow("5th attempt", name, client)
	h.refuse("6th attempt", name, client, 30*second)
	h.advance(30*second + 24*time.Hour)
	for i := 1; i <= 5; i++ {
		h.allow(fmt.Sprintf("attempt %d after 24 hours", i), name, client)
	}
	h.refuse("6th attempt after 24 hours", name, client, 30*second)
}

func TestAttemptsPairsAreCountedApart(t *testing.T) {
	h := newHarness(t, 100)
	a, b := testClient(1), testClient(2)
	h.reach(testName(1), a, 5)
	h.reach(testName(2), a, 3)
	h.refuse("login name 1 from client a", testName(1), a, 30*second)
	h.allow("login name 1 from client b", testName(1), b)
	h.allow("login name 2 from client a", testName(2), a)
	h.allow("login name 2 from client a again", testName(2), a)
	h.refuse("login name 2 from client a once more", testName(2), a, 30*second)
	h.allow("login name 3 from client a", testName(3), a)
	h.allow("login name 2 from client b", testName(2), b)
}

func TestAttemptsPairSucceeded(t *testing.T) {
	h := newHarness(t, 100)
	name, other := testName(1), testName(2)
	a, b := testClient(1), testClient(2)
	h.reach(name, a, 8)
	h.reach(name, b, 3)
	h.reach(other, a, 3)
	h.refuse("before the success", name, a, 4*time.Minute)
	nameBefore, clientBefore := h.nameLevel(1), h.clientLevel(1)

	h.a.succeeded(name, a)
	h.check()
	if h.hasPair(1, 1) {
		t.Error("the pair is still counted after a success")
	}
	// The login name and the client get one attempt back, and no more.
	if got, want := h.nameLevel(1), nameBefore-1; got != want {
		t.Errorf("the login name has the level %d after a success, want %d", got, want)
	}
	if got, want := h.clientLevel(1), clientBefore-1; got != want {
		t.Errorf("the client has the level %d after a success, want %d", got, want)
	}
	for i := 1; i <= 5; i++ {
		h.allow(fmt.Sprintf("attempt %d after the success", i), name, a)
	}
	h.refuse("attempt 6 after the success", name, a, 30*second)

	// The same login name from another client, and other login names, stay as they are.
	h.allow("the same login name from client b", name, b)
	h.allow("the same login name from client b again", name, b)
	h.refuse("the same login name from client b once more", name, b, 30*second)
	h.allow("another login name from client a", other, a)
	h.allow("another login name from client a again", other, a)
	h.refuse("another login name from client a once more", other, a, 30*second)

	// A success of a pair that is not counted gives back what it can, and counts nothing.
	h = newHarness(t, 100)
	h.a.succeeded(name, a)
	h.check()
	if h.hasPair(1, 1) || h.hasName(1) || h.hasClient(1) {
		t.Error("a success has counted something")
	}
}

func TestAttemptsPairAborted(t *testing.T) {
	name, client := testName(1), testClient(1)

	// The attempt that is taken back does not count.
	h := newHarness(t, 100)
	h.reach(name, client, 4)
	h.allow("5th attempt", name, client)
	h.a.aborted(name, client)
	h.check()
	h.allow("5th attempt again", name, client)
	h.refuse("6th attempt", name, client, 30*second)

	// The pair keeps the time of the attempt that was taken back.
	h = newHarness(t, 100)
	h.reach(name, client, 5)
	h.advance(30 * second)
	h.allow("6th attempt", name, client)
	h.a.aborted(name, client)
	h.check()
	h.refuse("7th attempt", name, client, 30*second)
	h.advance(30*second - 1)
	h.refuse("7th attempt a nanosecond early", name, client, second)
	h.advance(1)
	h.allow("7th attempt", name, client)

	// An attempt that is taken back many times takes back no more than it counted: the count is never below 0.
	h = newHarness(t, 100)
	h.a.aborted(name, client)
	h.check()
	if h.hasPair(1, 1) || h.hasName(1) || h.hasClient(1) {
		t.Error("taking back an attempt has counted something")
	}
	h.allow("1st attempt", name, client)
	h.a.aborted(name, client)
	h.a.aborted(name, client)
	h.a.aborted(name, client)
	h.check()
	if h.hasPair(1, 1) || h.hasName(1) || h.hasClient(1) {
		t.Error("a pair with no attempt is counted")
	}
	for i := 1; i <= 5; i++ {
		h.allow(fmt.Sprintf("attempt %d", i), name, client)
	}
	h.refuse("attempt 6", name, client, 30*second)

	// Each attempt that is taken back moves the pair one step back along its waits, from the same time.
	h = newHarness(t, 100)
	h.reach(name, client, 13)
	h.a.aborted(name, client)
	h.refuse("after taking back the 13th attempt", name, client, time.Hour)
	h.a.aborted(name, client)
	h.refuse("after taking back the 12th attempt", name, client, 32*time.Minute)

	// The login name and the client get the attempt back too, and the other pairs of the login name keep theirs.
	h = newHarness(t, 100)
	other := testClient(2)
	h.reach(name, other, 3)
	h.reach(name, client, 2)
	nameBefore, clientBefore := h.nameLevel(1), h.clientLevel(1)
	h.a.aborted(name, client)
	h.check()
	if got, want := h.nameLevel(1), nameBefore-1; got != want {
		t.Errorf("the login name has the level %d after taking back an attempt, want %d", got, want)
	}
	if got, want := h.clientLevel(1), clientBefore-1; got != want {
		t.Errorf("the client has the level %d after taking back an attempt, want %d", got, want)
	}
	h.allow("attempt 4 from the other client", name, other)
	h.allow("attempt 5 from the other client", name, other)
	h.refuse("attempt 6 from the other client", name, other, 30*second)
}

func TestAttemptsClientLevel(t *testing.T) {
	h := newHarness(t, 100)
	client := testClient(1)
	n := 0
	name := func() [32]byte { n++; return testName(n) }

	for i := 1; i <= 30; i++ {
		h.allow(fmt.Sprintf("attempt %d at once", i), name(), client)
	}
	h.refuse("attempt 31 at once", name(), client, 30*second)
	h.advance(10 * second)
	h.refuse("attempt 31, 10 seconds later", name(), client, 20*second)
	h.advance(20*second - 1)
	h.refuse("attempt 31, a nanosecond early", name(), client, second)
	h.advance(1)
	h.allow("attempt 31, 30 seconds later", name(), client)
	h.refuse("attempt 32", name(), client, 30*second)

	// One attempt comes back every 30 seconds, and two attempts in 60 seconds.
	h.advance(60 * second)
	h.allow("attempt 32, 60 seconds later", name(), client)
	h.allow("attempt 33, 60 seconds later", name(), client)
	h.refuse("attempt 34", name(), client, 30*second)

	// A long rest gives back all 30 attempts, and no more.
	h.advance(24 * time.Hour)
	for i := 1; i <= 30; i++ {
		h.allow(fmt.Sprintf("attempt %d after a day", i), name(), client)
	}
	h.refuse("attempt 31 after a day", name(), client, 30*second)

	// The attempts come back at a pace of their own, whatever the attempts in between.
	h.advance(90 * second)
	h.allow("one of three", name(), client)
	h.allow("two of three", name(), client)
	h.allow("three of three", name(), client)
	h.refuse("the fourth", name(), client, 30*second)
	h.advance(15 * second)
	h.refuse("the fourth, 15 seconds later", name(), client, 15*second)
}

func TestAttemptsClientRetryIsInWholeSeconds(t *testing.T) {
	for _, tt := range []struct{ passed, retry time.Duration }{
		{0, 30 * second},
		{1, 30 * second},
		{500 * time.Millisecond, 30 * second},
		{second, 29 * second},
		{1500 * time.Millisecond, 29 * second},
		{29*second + 1, second},
		{30*second - 1, second},
	} {
		h := newHarness(t, 100)
		client := testClient(1)
		for i := range 30 {
			h.allow("attempt", testName(i), client)
		}
		h.advance(tt.passed)
		h.refuse(fmt.Sprintf("an attempt %v after the 30th", tt.passed), testName(99), client, tt.retry)
	}
}

func TestAttemptsClientsAreCountedApart(t *testing.T) {
	h := newHarness(t, 100)
	for i := range 30 {
		h.allow("client 1", testName(i), testClient(1))
	}
	h.refuse("client 1", testName(100), testClient(1), 30*second)
	h.allow("client 2", testName(100), testClient(2))

	// The attempts of an IPv6 client count against its /64, and the /32 of an IPv4 client against itself.
	a, b, c := netip.MustParseAddr("2001:db8:1:2::1"), netip.MustParseAddr("2001:db8:1:2:ffff::7"), netip.MustParseAddr("2001:db8:1:3::1")
	for i := range 15 {
		h.allow("2001:db8:1:2::1", testName(i), clientPrefix(a))
		h.allow("2001:db8:1:2:ffff::7", testName(i), clientPrefix(b))
	}
	h.refuse("2001:db8:1:2::1", testName(100), clientPrefix(a), 30*second)
	h.refuse("2001:db8:1:2:ffff::7", testName(100), clientPrefix(b), 30*second)
	h.allow("2001:db8:1:3::1", testName(100), clientPrefix(c))
	h.allow("192.0.2.1", testName(100), clientPrefix(netip.MustParseAddr("192.0.2.1")))
	h.allow("192.0.2.2", testName(100), clientPrefix(netip.MustParseAddr("192.0.2.2")))

	// A prefix that has bits after its length is the range it names.
	h = newHarness(t, 100)
	for i := range 30 {
		h.allow("2001:db8::/64", testName(i), netip.MustParsePrefix("2001:db8::/64"))
	}
	h.refuse("2001:db8::1/64", testName(100), netip.MustParsePrefix("2001:db8::1/64"), 30*second)
	h.refuse("2001:db8::ffff/64", testName(100), netip.MustParsePrefix("2001:db8::ffff/64"), 30*second)
	h.allow("2001:db8:0:1::/64", testName(100), netip.MustParsePrefix("2001:db8:0:1::/64"))

	// The attempts of a pair count together, whatever the bits of the prefix after its length.
	h = newHarness(t, 100)
	for i := 1; i <= 5; i++ {
		h.allow("2001:db8::/64", testName(1), netip.MustParsePrefix("2001:db8::/64"))
	}
	h.refuse("2001:db8::1/64", testName(1), netip.MustParsePrefix("2001:db8::1/64"), 30*second)

	// An attempt that ends gives back to the range that a prefix names, whatever the bits after its length.
	for _, end := range []func(*attempts, [32]byte, netip.Prefix){(*attempts).succeeded, (*attempts).aborted} {
		h := newHarness(t, 100)
		h.allow("2001:db8::/64", testName(1), netip.MustParsePrefix("2001:db8::/64"))
		end(h.a, testName(1), netip.MustParsePrefix("2001:db8::1/64"))
		h.check()
		if s := h.a.state(); len(s.clients) != 0 || len(s.pairs) != 0 || len(s.names) != 0 {
			t.Errorf("%d clients, %d pairs and %d login names are counted after the attempt ended, from another spelling of the prefix",
				len(s.clients), len(s.pairs), len(s.names))
		}
	}

	// The zero Prefix is a client like another.
	h = newHarness(t, 100)
	for i := range 30 {
		h.allow("zero", testName(i), netip.Prefix{})
	}
	h.refuse("zero", testName(100), netip.Prefix{}, 30*second)
	h.allow("another client", testName(100), testClient(1))
}

func TestAttemptsClientGetsAttemptsBack(t *testing.T) {
	ends := []struct {
		name string
		f    func(*attempts, [32]byte, netip.Prefix)
	}{
		{"succeeded", (*attempts).succeeded},
		{"aborted", (*attempts).aborted},
	}

	// An attempt that ends gives back its own, and not the failures before it.
	for _, end := range ends {
		h := newHarness(t, 100)
		client := testClient(1)
		for i := range 20 {
			h.allow("a failure", testName(i), client)
		}
		for i := range 40 {
			h.allow(end.name+": an attempt", testName(100+i), client)
			end.f(h.a, testName(100+i), client)
			h.check()
		}
		for i := range 10 {
			h.allow(end.name+": a failure after them", testName(200+i), client)
		}
		h.refuse(end.name+": the attempt after 30 failures", testName(300), client, 30*second)
	}

	// What an attempt gets back is never more than there is: the level of a client is never below 0.
	for _, end := range ends {
		h := newHarness(t, 100)
		client := testClient(1)
		for range 50 {
			end.f(h.a, testName(1), client)
		}
		h.check()
		if h.hasClient(1) {
			t.Error("giving an attempt back has counted a client")
		}
		for i := range 2 {
			h.allow("an attempt", testName(i), client)
		}
		for range 5 {
			end.f(h.a, testName(1), client)
		}
		h.check()
		if h.hasClient(1) {
			t.Error("a client with no attempt is counted")
		}
		for i := range 30 {
			h.allow("an attempt after them", testName(i), client)
		}
		h.refuse("the 31st attempt", testName(100), client, 30*second)
	}

	// An attempt that has leaked out has nothing to give back.
	h := newHarness(t, 100)
	client := testClient(1)
	h.allow("an attempt", testName(1), client)
	h.advance(30 * second)
	h.a.succeeded(testName(1), client)
	h.check()
	if h.hasClient(1) {
		t.Error("a client whose attempt has come back is counted")
	}

	// The attempt that is given back is one of the level, whenever it was made.
	h = newHarness(t, 100)
	for i := range 3 {
		h.allow("an attempt", testName(i), client)
	}
	h.advance(45 * second)
	h.a.succeeded(testName(1), client)
	h.check()
	for i := range 29 {
		h.allow("an attempt after the success", testName(10+i), client)
	}
	h.refuse("one more", testName(100), client, 15*second)
}

func TestAttemptsNameLevel(t *testing.T) {
	h := newHarness(t, 1000)
	name := testName(1)
	n := 0
	client := func() netip.Prefix { n++; return testClient(n) }

	// 100 attempts from 100 clients, each client with one.
	for i := 1; i <= 100; i++ {
		h.allow(fmt.Sprintf("attempt %d at once", i), name, client())
	}
	h.refuse("attempt 101 at once", name, client(), 15*time.Minute)
	h.advance(5 * time.Minute)
	h.refuse("attempt 101, 5 minutes later", name, client(), 10*time.Minute)
	h.advance(10*time.Minute - 1)
	h.refuse("attempt 101, a nanosecond early", name, client(), second)
	h.advance(1)
	h.allow("attempt 101, 15 minutes later", name, client())
	h.refuse("attempt 102", name, client(), 15*time.Minute)

	// One attempt comes back every 15 minutes, and two attempts in 30 minutes.
	h.advance(30 * time.Minute)
	h.allow("attempt 102, 30 minutes later", name, client())
	h.allow("attempt 103, 30 minutes later", name, client())
	h.refuse("attempt 104", name, client(), 15*time.Minute)

	// A long rest gives back all 100 attempts, and no more.
	h.advance(48 * time.Hour)
	for i := 1; i <= 100; i++ {
		h.allow(fmt.Sprintf("attempt %d after two days", i), name, client())
	}
	h.refuse("attempt 101 after two days", name, client(), 15*time.Minute)

	// Another login name has attempts of its own.
	h.allow("another login name", testName(2), client())

	// The attempts come back at a pace of their own, whatever the attempts in between.
	h.advance(3*time.Hour + 5*time.Minute)
	for i := 1; i <= 12; i++ {
		h.allow(fmt.Sprintf("attempt %d of 12", i), name, client())
	}
	h.refuse("the 13th", name, client(), 10*time.Minute)
	h.advance(7 * time.Minute)
	h.refuse("the 13th, 7 minutes later", name, client(), 3*time.Minute)
}

func TestAttemptsNameRetryIsInWholeSeconds(t *testing.T) {
	for _, tt := range []struct{ passed, retry time.Duration }{
		{0, 15 * time.Minute},
		{1, 15 * time.Minute},
		{500 * time.Millisecond, 15 * time.Minute},
		{second, 15*time.Minute - second},
		{second + 1, 15*time.Minute - second},
		{1500 * time.Millisecond, 15*time.Minute - second},
		{15*time.Minute - second, second},
		{15*time.Minute - 1, second},
	} {
		h := newHarness(t, 1000)
		name := testName(1)
		h.fill(name, 100)
		h.advance(tt.passed)
		h.refuse(fmt.Sprintf("an attempt %v after the 100th", tt.passed), name, h.fresh(), tt.retry)
	}
}

func TestAttemptsNameGetsAttemptsBack(t *testing.T) {
	ends := []struct {
		name string
		f    func(*attempts, [32]byte, netip.Prefix)
	}{
		{"succeeded", (*attempts).succeeded},
		{"aborted", (*attempts).aborted},
	}
	name := testName(1)

	// An attempt that ends gives back its own, and not the attempts of the other clients.
	for _, end := range ends {
		h := newHarness(t, 1000)
		h.fill(name, 20)
		for range 40 {
			client := h.fresh()
			h.allow(end.name+": an attempt", name, client)
			end.f(h.a, name, client)
			h.check()
		}
		if got := h.nameLevel(1); got != 20 {
			t.Errorf("%s: the login name has the level %d, want 20", end.name, got)
		}
		h.fill(name, 80)
		h.refuse(end.name+": the attempt after 100 failures", name, h.fresh(), 15*time.Minute)
	}

	// What an attempt gets back is never more than there is: the level of a login name is never below 0.
	for _, end := range ends {
		h := newHarness(t, 1000)
		for range 50 {
			end.f(h.a, name, h.fresh())
		}
		h.check()
		if h.hasName(1) {
			t.Error("giving an attempt back has counted a login name")
		}
		h.fill(name, 2)
		for range 5 {
			end.f(h.a, name, h.fresh())
		}
		h.check()
		if h.hasName(1) {
			t.Error("a login name with no attempt is counted")
		}
		h.fill(name, 100)
		h.refuse("the 101st attempt", name, h.fresh(), 15*time.Minute)
	}

	// An attempt that has leaked out has nothing to give back.
	h := newHarness(t, 1000)
	client := h.fresh()
	h.allow("an attempt", name, client)
	h.advance(15 * time.Minute)
	h.a.succeeded(name, client)
	h.check()
	if h.hasName(1) {
		t.Error("a login name whose attempt has come back is counted")
	}
}

// TestAttemptsOneClientDoesNotShutOutAnother shows what the pairs are for: a client that tries a login name for ever
// makes only its own attempts wait.
func TestAttemptsOneClientDoesNotShutOutAnother(t *testing.T) {
	h := newHarness(t, 1000)
	name := testName(1)
	attacker, user := testClient(1), testClient(2)

	h.reach(name, attacker, 13)
	h.refuse("the attacker", name, attacker, time.Hour)

	// The real user is allowed at once, and a success of theirs is a success.
	h.allow("the real user", name, user)
	h.a.succeeded(name, user)
	h.check()
	if h.hasPair(1, 2) {
		t.Error("the pair of the real user is still counted after a success")
	}
	h.refuse("the attacker after the success", name, attacker, time.Hour)

	// This goes on for as long as the attacker does: he tries once an hour, and the real user logs in.
	for i := range 20 {
		h.advance(time.Hour)
		h.allow(fmt.Sprintf("the attacker, hour %d", i), name, attacker)
		h.allow(fmt.Sprintf("the real user, hour %d", i), name, user)
		h.a.succeeded(name, user)
		h.refuse(fmt.Sprintf("the attacker, hour %d, once more", i), name, attacker, time.Hour)
	}
	h.check()

	// The real user's wrong passwords make only the real user's pair wait.
	for i := 1; i <= 5; i++ {
		h.allow(fmt.Sprintf("a wrong password %d of the real user", i), name, user)
	}
	h.refuse("the real user, 6th wrong password", name, user, 30*second)
	h.allow("a third client", name, h.fresh())
}

// TestAttemptsClientsFillTheLevelOfAName shows what is left of the weakness: many clients fill the level of a login name,
// and one attempt comes back in 15 minutes.
func TestAttemptsClientsFillTheLevelOfAName(t *testing.T) {
	h := newHarness(t, 1000)
	name := testName(1)

	// 20 clients with 5 free attempts each. The sixth attempt of a client waits for its pair, and for the login name when the
	// 20th client has filled it.
	for i := range 20 {
		client := h.fresh()
		for j := 1; j <= 5; j++ {
			h.allow(fmt.Sprintf("attempt %d of client %d", j, i), name, client)
		}
		wait := 30 * second
		if i == 19 {
			wait = 15 * time.Minute
		}
		h.refuse(fmt.Sprintf("attempt 6 of client %d", i), name, client, wait)
	}
	if got := h.nameLevel(1); got != 100 {
		t.Fatalf("the login name has the level %d after 20 clients with 5 attempts, want 100", got)
	}

	// The 21st client is refused with the wait of the level, and so is the real user from a client of their own.
	user := h.fresh()
	h.refuse("the 21st client", name, user, 15*time.Minute)
	h.advance(15*time.Minute - 1)
	h.refuse("the 21st client, a nanosecond early", name, user, second)
	h.advance(1)
	h.allow("the 21st client, 15 minutes later", name, user)
	h.refuse("the 22nd client", name, h.fresh(), 15*time.Minute)

	// A success of the real user does not clear the attempts of the others: it gives back the one it used.
	h.a.succeeded(name, user)
	if got := h.nameLevel(1); got != 99 {
		t.Errorf("the login name has the level %d after the success of the real user, want 99", got)
	}
	h.allow("the real user, once more", name, user)
	h.refuse("the real user, and then", name, h.fresh(), 15*time.Minute)
}

// TestAttemptsSuccessKeepsWhatOthersAdded is the same, with the real user's success in the middle of an attack.
func TestAttemptsSuccessKeepsWhatOthersAdded(t *testing.T) {
	h := newHarness(t, 1000)
	name := testName(1)
	h.fill(name, 60)

	user := h.fresh()
	for range 10 {
		h.allow("the real user", name, user)
		h.a.succeeded(name, user)
	}
	if got := h.nameLevel(1); got != 60 {
		t.Fatalf("the login name has the level %d after 60 attempts of others and 10 successes, want 60", got)
	}
	h.fill(name, 40)
	h.refuse("the attempt after 100 of others and 10 successes", name, user, 15*time.Minute)
}

func TestAttemptsAllMustAllow(t *testing.T) {
	h := newHarness(t, 1000)
	waiting, full, free := testName(1), testName(2), testName(3)
	busy, quiet, third := testClient(1), testClient(2), testClient(3)

	h.reach(waiting, quiet, 5)
	h.fill(full, 95)
	h.reach(full, third, 5)
	for i := range 30 {
		h.allow("the busy client", testName(100+i), busy)
	}

	// Each alone.
	h.refuse("the pair waits", waiting, quiet, 30*second)
	h.refuse("the login name is full", full, h.fresh(), 15*time.Minute)
	h.refuse("the client is busy", free, busy, 30*second)

	// Another client of the login name that waits is allowed, and so is another login name of the client that waits.
	h.allow("another client of the login name that waits", waiting, h.fresh())
	h.allow("another login name of the client that waits", free, quiet)

	// Together: the longest wait is the retry.
	h.refuse("the login name is full and the client busy", full, busy, 15*time.Minute)
	h.refuse("the login name is full and the pair waits", full, third, 15*time.Minute)

	// A pair that waits for an hour, from a client that is busy, on a login name that is full.
	h = newHarness(t, 1000)
	h.reach(waiting, quiet, 13)
	for h.nameLevel(1) < 100 {
		h.allow("an attempt that fills the login name", waiting, h.fresh())
	}
	for i := 0; h.clientLevel(2) < 30; i++ {
		h.allow("an attempt that makes the client busy", testName(100+i), quiet)
	}
	h.refuse("the pair waits an hour, the login name is full, the client is busy", waiting, quiet, time.Hour)

	// The waits are parts of seconds: each is rounded up, and the longest is the retry.
	h = newHarness(t, 1000)
	h.reach(waiting, quiet, 5)
	h.advance(25*second + 500*time.Millisecond)
	h.refuse("4.5 seconds of the pair", waiting, quiet, 5*second)
	for i := range 30 {
		h.allow("the busy client", testName(100+i), busy)
	}
	h.refuse("the client is busy for 30 seconds", free, busy, 30*second)
	h.advance(10*second + 500*time.Millisecond)
	h.refuse("19.5 seconds of the client", free, busy, 20*second)
}

func TestAttemptsRefusalsChangeNothing(t *testing.T) {
	h := newHarness(t, 1000)
	waitsBusy, waitsQuiet, idleOld, idleNew, full := testName(1), testName(5), testName(2), testName(3), testName(4)
	busy, quiet, third := testClient(1), testClient(2), testClient(3)

	h.reach(waitsBusy, busy, 5)
	for i := range 25 {
		h.allow("the busy client", testName(100+i), busy)
	}
	h.reach(idleOld, quiet, 2)
	h.reach(waitsQuiet, quiet, 5)
	h.reach(idleNew, quiet, 4)
	h.fill(full, 95)
	h.reach(full, third, 5)

	// refuse compares what is remembered before and after.
	for i := range 3 {
		wait := 30*second - time.Duration(i)*second
		level := 15*time.Minute - time.Duration(i)*second
		h.refuse("the pair waits", waitsQuiet, quiet, wait)
		h.refuse("the client is busy", idleOld, busy, wait)
		h.refuse("the pair waits and the client is busy", waitsBusy, busy, wait)
		h.refuse("the client is busy, with a new login name", testName(500), busy, wait)
		h.refuse("the login name is full", full, h.fresh(), level)
		h.refuse("the login name is full and the client is busy", full, busy, level)
		h.refuse("the login name is full and the pair waits", full, third, level)
		h.advance(second)
	}
}

func TestAttemptsAtTheSameTime(t *testing.T) {
	// run calls f from n goroutines at once, and counts the attempts that were allowed and those refused for wait.
	run := func(n int, wait time.Duration, f func(i int) (time.Duration, bool)) (allowed, refused int) {
		var yes, no atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if retry, ok := f(i); ok {
					yes.Add(1)
				} else if retry == wait {
					no.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		return int(yes.Load()), int(no.Load())
	}

	// 100 attempts of one pair at once: 5 get through.
	h := newHarness(t, 1000)
	allowed, refused := run(100, 30*second, func(int) (time.Duration, bool) { return h.a.begin(testName(1), testClient(1)) })
	if allowed != 5 || refused != 95 {
		t.Errorf("100 attempts of a pair at once: %d allowed, %d refused for 30 seconds, want 5 and 95", allowed, refused)
	}
	h.check()

	// 150 attempts on one login name at once, from 150 clients: 100 get through.
	h = newHarness(t, 1000)
	allowed, refused = run(150, 15*time.Minute, func(i int) (time.Duration, bool) { return h.a.begin(testName(1), testClient(i)) })
	if allowed != 100 || refused != 50 {
		t.Errorf("150 attempts on a login name at once: %d allowed, %d refused for 15 minutes, want 100 and 50", allowed, refused)
	}
	h.check()

	// 100 attempts from one client at once, on 100 login names: 30 get through.
	h = newHarness(t, 1000)
	allowed, refused = run(100, 30*second, func(i int) (time.Duration, bool) { return h.a.begin(testName(i), testClient(1)) })
	if allowed != 30 || refused != 70 {
		t.Errorf("100 attempts from a client at once: %d allowed, %d refused for 30 seconds, want 30 and 70", allowed, refused)
	}
	h.check()

	// 1000 attempts on 10 login names from 10 clients: 5 per pair, and so 50.
	h = newHarness(t, 1000)
	allowed, _ = run(1000, 30*second, func(i int) (time.Duration, bool) { return h.a.begin(testName(i%10), testClient(i%10)) })
	if allowed != 50 {
		t.Errorf("1000 attempts on 10 login names from 10 clients at once: %d allowed, want 50", allowed)
	}
	h.check()
}

func TestAttemptsWarnsWithoutTheLock(t *testing.T) {
	clock := newFakeClock()
	var a *attempts
	var calls atomic.Int32
	a = newAttempts(clock.now, 1, func(string) {
		calls.Add(1)
		// A warn that needs the lock of attempts would wait for ever, as the one that begin holds.
		a.succeeded(testName(2), testClient(2))
		a.aborted(testName(2), testClient(2))
	})
	for range 5 {
		a.begin(testName(1), testClient(1))
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.begin(testName(3), testClient(1))
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("begin does not return: warn is called with the lock held")
	}
	// The pair and the login name are new, and their tables are full.
	if got := calls.Load(); got != 2 {
		t.Errorf("warn was called %d times, want 2", got)
	}
}

func TestAttemptsServer(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	for what, got := range map[string]int{
		"pairs": ts.attempts.pairs.capacity, "login names": ts.attempts.names.capacity, "clients": ts.attempts.clients.capacity,
	} {
		if got != 100000 {
			t.Errorf("the table of %s has the capacity %d, want 100000", what, got)
		}
	}

	// The attempts keep the time of Config.Now.
	name, client := testName(1), testClient(1)
	for range 5 {
		if retry, ok := ts.attempts.begin(name, client); !ok {
			t.Fatalf("a free attempt is refused for %v", retry)
		}
	}
	if retry, ok := ts.attempts.begin(name, client); ok || retry != 30*second {
		t.Errorf("the 6th attempt = %v, %v, want 30s, false", retry, ok)
	}
	ts.clock.advance(30 * second)
	if retry, ok := ts.attempts.begin(name, client); !ok {
		t.Errorf("the 6th attempt after 30 seconds is refused for %v", retry)
	}

	// Without Config.Now, it is time.Now.
	s, err := New(testConfig(httpsOrigin))
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(s.attempts.start); d < 0 || d > time.Minute {
		t.Errorf("the attempts of a Server without Now start %v ago, want now", d)
	}
	if d := s.attempts.elapsed(); d < 0 || d > time.Minute {
		t.Errorf("the attempts of a Server without Now say that %v has passed, want nearly nothing", d)
	}
}

func TestAttemptsServerWarnsOncePerMinute(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	// One table at a time has no room: its capacity is 1, with its first entry in it.
	ts.attempts.pairs.capacity = 1
	for range 5 {
		ts.attempts.begin(testName(1), testClient(1))
	}
	const (
		pairs   = "too many login name and client pairs to keep count of; refusing new ones\n"
		names   = "too many login names to keep count of; refusing new ones\n"
		clients = "too many clients to keep count of; refusing new ones\n"
	)

	if retry, ok := ts.attempts.begin(testName(2), testClient(1)); ok || retry != time.Minute {
		t.Fatalf("a new pair when the table is full = %v, %v, want 1m0s, false", retry, ok)
	}
	if got := ts.logs.String(); got != pairs {
		t.Errorf("the log says %q, want %q", got, pairs)
	}
	ts.attempts.begin(testName(3), testClient(1))
	ts.attempts.begin(testName(2), testClient(2))
	if got := ts.logs.String(); got != pairs {
		t.Errorf("the log within the minute says %q, want the one line %q", got, pairs)
	}

	ts.clock.advance(time.Minute - 1)
	ts.attempts.begin(testName(2), testClient(2))
	if got := ts.logs.String(); got != pairs {
		t.Errorf("the log a nanosecond early says %q, want the one line %q", got, pairs)
	}

	// After the minute, the tables of login names and of clients are the ones that are full, one after the other.
	ts.clock.advance(1)
	ts.attempts.pairs.capacity = 100000
	ts.attempts.names.capacity = 1
	if retry, ok := ts.attempts.begin(testName(2), testClient(1)); ok || retry != time.Minute {
		t.Fatalf("a new login name when the table is full = %v, %v, want 1m0s, false", retry, ok)
	}
	if got := ts.logs.String(); got != pairs+names {
		t.Errorf("the log after the minute says %q, want %q", got, pairs+names)
	}
	ts.clock.advance(time.Minute)
	ts.attempts.names.capacity = 100000
	ts.attempts.clients.capacity = 1
	if retry, ok := ts.attempts.begin(testName(1), testClient(2)); ok || retry != time.Minute {
		t.Fatalf("a new client when the table is full = %v, %v, want 1m0s, false", retry, ok)
	}
	if got := ts.logs.String(); got != pairs+names+clients {
		t.Errorf("the log after another minute says %q, want %q", got, pairs+names+clients)
	}
}

func TestAttemptsWarnEveryMinute(t *testing.T) {
	clock := newFakeClock()
	logs := new(syncBuffer)
	warn := warnEveryMinute(log.New(logs, "", 0), clock.now)
	warn(whatNames)
	warn(whatClients)
	if got, want := logs.String(), "too many login names to keep count of; refusing new ones\n"; got != want {
		t.Errorf("the log says %q, want %q", got, want)
	}
	clock.advance(time.Minute - 1)
	warn(whatClients)
	if n := strings.Count(logs.String(), "\n"); n != 1 {
		t.Errorf("a warning a nanosecond early has logged: %q", logs.String())
	}
	clock.advance(1)
	warn(whatClients)
	warn(whatClients)
	if got, want := logs.String(), "too many login names to keep count of; refusing new ones\ntoo many clients to keep count of; refusing new ones\n"; got != want {
		t.Errorf("the log says %q, want %q", got, want)
	}
	clock.advance(time.Hour)
	warn(whatPairs)
	if got, want := logs.String(), "too many login names to keep count of; refusing new ones\ntoo many clients to keep count of; refusing new ones\n"+
		"too many login name and client pairs to keep count of; refusing new ones\n"; got != want {
		t.Errorf("the log says %q, want %q", got, want)
	}

	// Warnings from many goroutines at once give one line.
	clock.advance(time.Hour)
	logs.Reset()
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			warn(whatClients)
		}()
	}
	wg.Wait()
	if n := strings.Count(logs.String(), "\n"); n != 1 {
		t.Errorf("100 warnings at once have logged %d lines, want 1", n)
	}
}

func TestAttemptsClockStepsBack(t *testing.T) {
	// A step back of a few minutes: the attempts that are free stay free, and the pairs that wait wait for longer.
	h := newHarness(t, 100)
	name, client := testName(1), testClient(1)
	h.reach(name, client, 2)
	h.reach(testName(2), client, 5)
	h.advance(-10 * time.Minute)
	h.allow("a free attempt", name, client)
	h.refuse("a pair that waits", testName(2), client, 10*time.Minute+30*second)
	h.advance(10*time.Minute + 30*second)
	h.allow("a pair that waited", testName(2), client)

	// A step back of an hour: a client that was counted later has a level that is too high to allow another attempt.
	h = newHarness(t, 100)
	h.allow("a client", testName(3), testClient(2))
	h.advance(-time.Hour)
	h.refuse("a client that was counted later", testName(4), testClient(2), 2760*second)
}
