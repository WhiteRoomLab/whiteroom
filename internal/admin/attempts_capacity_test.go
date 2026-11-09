package admin

import (
	"fmt"
	"math/rand/v2"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// idleOrder returns the ids of the login names of the pairs that may be dropped, from the most recently used; clientOf
// says the client of the pair of a login name.
func (h *harness) idleOrder(clientOf func(name int) netip.Prefix) []int {
	ids := map[pairKey]int{}
	for i := range 2000 {
		ids[newPairKey(testName(i), clientOf(i))] = i
	}
	var order []int
	for _, k := range h.a.state().idle {
		id, ok := ids[k]
		if !ok {
			h.t.Fatalf("a pair that may be dropped is not one of the login names 0 to 1999 with its client")
		}
		order = append(order, id)
	}
	return order
}

func (h *harness) hasPairOf(name [32]byte, client netip.Prefix) bool {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	_, ok := h.a.pairs.entries[newPairKey(name, client)]
	return ok
}

// waitingPair makes the pair wait with its 5 free attempts.
func (h *harness) waitingPair(name int, client netip.Prefix) {
	h.t.Helper()
	for range freeAttempts {
		h.allow("an attempt that makes the pair wait", testName(name), client)
	}
}

func TestCapacityPairsDropTheLeastRecentlyUsed(t *testing.T) {
	h := newSizedHarness(t, 3, 100, 100)
	client := testClient(1)
	clientOf := func(int) netip.Prefix { return client }
	for i := 1; i <= 3; i++ {
		h.allow("a new pair", testName(i), client)
	}
	if got, want := h.idleOrder(clientOf), []int{3, 2, 1}; !slices.Equal(got, want) {
		t.Fatalf("the pairs are in the order %v, want %v", got, want)
	}

	// Another attempt of a pair is a use of it.
	h.allow("pair 1 again", testName(1), client)
	if got, want := h.idleOrder(clientOf), []int{1, 3, 2}; !slices.Equal(got, want) {
		t.Fatalf("the pairs are in the order %v, want %v", got, want)
	}

	h.allow("a fourth pair", testName(4), client)
	if got, want := h.idleOrder(clientOf), []int{4, 1, 3}; !slices.Equal(got, want) {
		t.Errorf("after a 4th pair they are in the order %v, want %v", got, want)
	}
	if h.hasPair(2, 1) {
		t.Error("the pair that was used least recently is still counted")
	}
	h.allow("a fifth pair", testName(5), client)
	if got, want := h.idleOrder(clientOf), []int{5, 4, 1}; !slices.Equal(got, want) {
		t.Errorf("after a 5th pair they are in the order %v, want %v", got, want)
	}
	if h.hasPair(3, 1) {
		t.Error("the pair that was used least recently is still counted")
	}

	// A dropped pair starts again: it has its free attempts.
	for i := 1; i <= 5; i++ {
		h.allow(fmt.Sprintf("attempt %d of the pair that was dropped", i), testName(2), client)
	}
	h.refuse("attempt 6 of it", testName(2), client, 30*second)

	// An attempt that is taken back does not move the pair.
	h = newSizedHarness(t, 3, 100, 100)
	for i := 1; i <= 3; i++ {
		h.allow("a new pair", testName(i), client)
	}
	h.allow("pair 1 again", testName(1), client)
	h.a.aborted(testName(1), client)
	if got, want := h.idleOrder(clientOf), []int{1, 3, 2}; !slices.Equal(got, want) {
		t.Errorf("after taking back an attempt the pairs are in the order %v, want %v", got, want)
	}
}

func TestCapacityPairsKeepTheOnesThatWait(t *testing.T) {
	h := newSizedHarness(t, 3, 1000, 1000)
	client := h.fresh()
	clients := map[int]netip.Prefix{1: client}
	clientOf := func(name int) netip.Prefix { return clients[name] }
	h.waitingPair(1, client)
	for i := 2; i <= 3; i++ {
		clients[i] = h.fresh()
		h.allow("a pair", testName(i), clients[i])
	}

	// Many new pairs take each other's places, and never that of a pair that waits.
	for i := 10; i < 200; i++ {
		clients[i] = h.fresh()
		h.allow("a new pair", testName(i), clients[i])
		if !h.hasPairOf(testName(1), client) {
			t.Fatalf("the pair that waits is dropped for the new pair %d", i)
		}
	}
	h.refuse("the pair that waits", testName(1), client, 30*second)
	if got, want := h.idleOrder(clientOf), []int{199, 198}; !slices.Equal(got, want) {
		t.Errorf("the pairs that may be dropped are %v, want %v", got, want)
	}
	if got := h.warned.take(); len(got) != 0 {
		t.Errorf("warn was called with %q", got)
	}

	// When it has no wait any more it stays in the table, with the attempts that it has had.
	h.advance(time.Hour)
	h.allow("the pair that waits, an hour later", testName(1), client)
	h.refuse("the attempt after it", testName(1), client, time.Minute)
	for i := 300; i < 400; i++ {
		h.allow("a new pair", testName(i), h.fresh())
	}
	h.refuse("the attempt after them", testName(1), client, time.Minute)
}

func TestCapacityPairsAllWait(t *testing.T) {
	h := newSizedHarness(t, 3, 100, 100)
	for i := 1; i <= 3; i++ {
		h.waitingPair(i, testClient(i))
	}

	// A new pair gets 60 seconds, and warn is called; nothing is dropped or counted.
	h.refuse("a fourth pair", testName(4), h.fresh(), time.Minute)
	if got, want := h.warned.take(), []string{whatPairs}; !slices.Equal(got, want) {
		t.Errorf("warn was called with %q, want %q", got, want)
	}
	for range 5 {
		h.refuse("a fourth pair again", testName(4), h.fresh(), time.Minute)
	}
	if got := h.warned.take(); len(got) != 5 {
		t.Errorf("warn was called %d times for 5 refused attempts, want 5", len(got))
	}
	if h.hasName(4) {
		t.Error("the login name of the pair that was refused is counted")
	}

	// The pairs that are counted go on by their own waits, and warn is not called for them.
	h.refuse("pair 1", testName(1), testClient(1), 30*second)
	if got := h.warned.take(); len(got) != 0 {
		t.Errorf("warn was called with %q for a pair that is counted", got)
	}
	h.advance(30 * second)
	h.allow("pair 1, 30 seconds later", testName(1), testClient(1))
	h.allow("pair 2, 30 seconds later", testName(2), testClient(2))
	h.refuse("a fourth pair still", testName(4), h.fresh(), time.Minute)
	h.warned.take()

	// An entry whose last attempt is 24 hours old makes room.
	h.advance(24*time.Hour - 30*second - 1)
	h.refuse("a fourth pair, a nanosecond before pair 3 is forgotten", testName(4), testClient(4), time.Minute)
	h.advance(1)
	h.allow("a fourth pair when pair 3 is forgotten", testName(4), testClient(4))
	if h.hasPair(3, 3) {
		t.Error("the forgotten pair is still counted")
	}
	for _, i := range []int{1, 2, 4} {
		if !h.hasPair(i, i) {
			t.Errorf("pair %d is not counted", i)
		}
	}
}

func TestCapacityPairsExpiredBeforeLeastRecentlyUsed(t *testing.T) {
	h := newSizedHarness(t, 2, 100, 100)
	h.waitingPair(2, testClient(2))
	h.advance(time.Hour)
	h.allow("pair 1", testName(1), testClient(1))
	h.advance(23 * time.Hour)

	// Pair 2 was forgotten, pair 1 is the one that was used least recently.
	h.allow("pair 3", testName(3), testClient(3))
	if h.hasPair(2, 2) || !h.hasPair(1, 1) || !h.hasPair(3, 3) {
		t.Errorf("after pair 3: counted 1: %v, 2: %v, 3: %v, want true, false, true", h.hasPair(1, 1), h.hasPair(2, 2), h.hasPair(3, 3))
	}
}

func TestCapacityPairsAreFoundAgain(t *testing.T) {
	h := newSizedHarness(t, 2, 100, 100)
	h.waitingPair(1, testClient(1))
	h.allow("pair 2", testName(2), testClient(2))
	h.advance(24 * time.Hour)

	// A forgotten pair that comes back takes its own entry; nothing else is dropped.
	h.allow("pair 1, a day later", testName(1), testClient(1))
	if !h.hasPair(2, 2) {
		t.Error("an entry that nothing needed was dropped")
	}
	for i := 2; i <= 5; i++ {
		h.allow(fmt.Sprintf("attempt %d of pair 1, a day later", i), testName(1), testClient(1))
	}
	h.refuse("attempt 6 of it", testName(1), testClient(1), 30*second)

	// A new pair takes the place of the entry that has been forgotten.
	h.allow("pair 3", testName(3), testClient(3))
	if h.hasPair(2, 2) || !h.hasPair(1, 1) || !h.hasPair(3, 3) {
		t.Errorf("after pair 3: counted 1: %v, 2: %v, 3: %v, want true, false, true", h.hasPair(1, 1), h.hasPair(2, 2), h.hasPair(3, 3))
	}
}

func TestCapacityNames(t *testing.T) {
	h := newSizedHarness(t, 100, 3, 100)
	for i := 1; i <= 3; i++ {
		h.allow("a new login name", testName(i), h.fresh())
		h.advance(second)
	}
	// The three expire 15 minutes, and 1 and 2 seconds more, after the first attempt, and now it is 3 seconds after it.

	h.refuse("a fourth login name", testName(4), h.fresh(), time.Minute)
	if got, want := h.warned.take(), []string{whatNames}; !slices.Equal(got, want) {
		t.Errorf("warn was called with %q, want %q", got, want)
	}
	h.advance(15*time.Minute - 4*second)
	h.refuse("a fourth login name when the first is about to leave", testName(4), h.fresh(), time.Minute)
	if h.hasName(4) {
		t.Error("the login name that was refused is counted")
	}
	h.warned.take()

	// Its place goes to the next login name, and only its place.
	h.advance(second)
	h.allow("a fourth login name when the first has left", testName(4), h.fresh())
	if h.hasName(1) || !h.hasName(2) || !h.hasName(3) || !h.hasName(4) {
		t.Errorf("after login name 4: counted 1: %v, 2: %v, 3: %v, 4: %v, want false, true, true, true",
			h.hasName(1), h.hasName(2), h.hasName(3), h.hasName(4))
	}
	h.refuse("a fifth login name", testName(5), h.fresh(), time.Minute)
	h.advance(second)
	h.allow("a fifth login name when the second has left", testName(5), h.fresh())
	if h.hasName(2) || !h.hasName(3) || !h.hasName(4) || !h.hasName(5) {
		t.Errorf("after login name 5: counted 2: %v, 3: %v, 4: %v, 5: %v, want false, true, true, true",
			h.hasName(2), h.hasName(3), h.hasName(4), h.hasName(5))
	}

	// A login name whose entry has expired comes back to it: nothing else is dropped.
	h.advance(second)
	h.allow("login name 3 comes back", testName(3), h.fresh())
	if !h.hasName(4) || !h.hasName(5) {
		t.Error("an entry that nothing needed was dropped")
	}
	h.refuse("a sixth login name", testName(6), h.fresh(), time.Minute)
}

func TestCapacityNamesKeepTheOnesThatHaveAttempts(t *testing.T) {
	h := newSizedHarness(t, 1000, 2, 1000)
	h.fill(testName(1), 100)
	h.allow("a login name that is quiet", testName(2), h.fresh())
	h.advance(15 * time.Minute)

	// The quiet login name has left; a new one takes its place, and then they are all refused.
	h.allow("a new login name", testName(3), h.fresh())
	for i := 4; i < 100; i++ {
		h.refuse("a flood of login names", testName(i), h.fresh(), time.Minute)
	}
	if !h.hasName(1) {
		t.Fatal("the login name with many attempts is dropped")
	}
	h.allow("the login name has an attempt back", testName(1), h.fresh())
	h.refuse("the login name", testName(1), h.fresh(), 15*time.Minute)
	if got := h.warned.take(); len(got) != 96 || got[0] != whatNames {
		t.Errorf("warn was called %d times, with %q first, want 96 times with %q", len(got), got, whatNames)
	}
}

func TestCapacityClients(t *testing.T) {
	h := newSizedHarness(t, 100, 100, 3)
	for i := 1; i <= 3; i++ {
		h.allow("a new client", testName(i), testClient(i))
		h.advance(second)
	}
	// The three expire at 30, 31 and 32 seconds after the first attempt, and now it is 3 seconds after it.

	h.refuse("a fourth client", testName(4), testClient(4), time.Minute)
	if got, want := h.warned.take(), []string{whatClients}; !slices.Equal(got, want) {
		t.Errorf("warn was called with %q, want %q", got, want)
	}
	h.advance(26 * second)
	h.refuse("a fourth client when the first is about to leave", testName(4), testClient(4), time.Minute)
	if h.hasClient(4) {
		t.Error("the client that was refused is counted")
	}
	h.warned.take()

	// Its place goes to the next client, and only its place.
	h.advance(second)
	h.allow("a fourth client when the first has left", testName(4), testClient(4))
	if h.hasClient(1) || !h.hasClient(2) || !h.hasClient(3) || !h.hasClient(4) {
		t.Errorf("after client 4: counted 1: %v, 2: %v, 3: %v, 4: %v, want false, true, true, true",
			h.hasClient(1), h.hasClient(2), h.hasClient(3), h.hasClient(4))
	}
	h.refuse("a fifth client", testName(5), testClient(5), time.Minute)
	h.advance(second)
	h.allow("a fifth client when the second has left", testName(5), testClient(5))
	if h.hasClient(2) || !h.hasClient(3) || !h.hasClient(4) || !h.hasClient(5) {
		t.Errorf("after client 5: counted 2: %v, 3: %v, 4: %v, 5: %v, want false, true, true, true",
			h.hasClient(2), h.hasClient(3), h.hasClient(4), h.hasClient(5))
	}

	// A client whose entry has expired comes back to it: nothing else is dropped.
	h.advance(second)
	h.allow("client 3 comes back", testName(6), testClient(3))
	if !h.hasClient(4) || !h.hasClient(5) {
		t.Error("an entry that nothing needed was dropped")
	}
	h.refuse("a sixth client", testName(7), testClient(6), time.Minute)
}

func TestCapacityClientsKeepTheOnesThatWait(t *testing.T) {
	h := newSizedHarness(t, 1000, 1000, 2)
	busy := testClient(1)
	for i := range 30 {
		h.allow("the busy client", testName(i), busy)
	}
	h.allow("a client that is quiet", testName(100), testClient(2))
	h.advance(30 * second)

	// The quiet client has left; a new client takes its place, and then they are all refused.
	h.allow("a new client", testName(101), testClient(3))
	for i := 4; i < 100; i++ {
		h.refuse("a flood of clients", testName(200+i), testClient(i), time.Minute)
	}
	if !h.hasClient(1) {
		t.Fatal("the busy client is dropped")
	}
	h.allow("the busy client has an attempt back", testName(300), busy)
	h.refuse("the busy client", testName(301), busy, 30*second)
	if got := h.warned.take(); len(got) != 96 || got[0] != whatClients {
		t.Errorf("warn was called %d times, with %q first, want 96 times with %q", len(got), got, whatClients)
	}
}

func TestCapacityLevelsGetAttemptsBack(t *testing.T) {
	ends := []struct {
		name string
		f    func(*attempts, [32]byte, netip.Prefix)
	}{
		{"succeeded", (*attempts).succeeded},
		{"aborted", (*attempts).aborted},
	}
	for _, end := range ends {
		// Clients: an entry whose attempts are all back is gone, and has room for another client.
		h := newSizedHarness(t, 100, 100, 2)
		h.allow("client 1", testName(1), testClient(1))
		h.allow("client 2", testName(2), testClient(2))
		h.allow("client 2 again", testName(3), testClient(2))
		h.refuse("client 3", testName(4), testClient(3), time.Minute)
		end.f(h.a, testName(2), testClient(2))
		h.refuse(end.name+": client 3 while client 2 has an attempt", testName(4), testClient(3), time.Minute)
		end.f(h.a, testName(1), testClient(1))
		h.check()
		if h.hasClient(1) {
			t.Fatalf("%s: client 1 is still counted", end.name)
		}
		h.allow(end.name+": client 3", testName(4), testClient(3))
		if !h.hasClient(2) || !h.hasClient(3) {
			t.Errorf("%s: counted 2: %v, 3: %v, want true, true", end.name, h.hasClient(2), h.hasClient(3))
		}

		// Login names: the same.
		h = newSizedHarness(t, 100, 2, 100)
		h.allow("login name 1", testName(1), testClient(1))
		h.allow("login name 2", testName(2), testClient(2))
		h.allow("login name 2 again", testName(2), testClient(3))
		h.refuse("login name 3", testName(3), testClient(4), time.Minute)
		end.f(h.a, testName(2), testClient(2))
		h.refuse(end.name+": login name 3 while login name 2 has an attempt", testName(3), testClient(4), time.Minute)
		end.f(h.a, testName(1), testClient(1))
		h.check()
		if h.hasName(1) {
			t.Fatalf("%s: login name 1 is still counted", end.name)
		}
		h.allow(end.name+": login name 3", testName(3), testClient(4))
		if !h.hasName(2) || !h.hasName(3) {
			t.Errorf("%s: counted 2: %v, 3: %v, want true, true", end.name, h.hasName(2), h.hasName(3))
		}
	}
}

func TestCapacityRefusalsChangeNothing(t *testing.T) {
	// A new pair from a client that is refused does not take the place of a pair that may be dropped.
	h := newSizedHarness(t, 3, 1000, 100)
	busy := testClient(1)
	for i := range 30 {
		h.allow("the busy client", testName(i), busy)
	}
	h.refuse("a new pair from the busy client", testName(500), busy, 30*second)

	// A new client when the clients are full does not count the pair or the login name, and does not drop what is new.
	h = newSizedHarness(t, 100, 100, 2)
	h.allow("client 1", testName(1), testClient(1))
	h.allow("client 2", testName(2), testClient(2))
	h.allow("client 3", testName(3), testClient(2))
	h.refuse("a new client with login name 1", testName(1), testClient(3), time.Minute)
	h.refuse("a new client with a new login name", testName(4), testClient(3), time.Minute)
	if got, want := h.warned.take(), []string{whatClients, whatClients}; !slices.Equal(got, want) {
		t.Errorf("warn was called with %q, want %q", got, want)
	}

	// A new login name when the login names are full does not count the pair or the client.
	h = newSizedHarness(t, 100, 2, 100)
	h.allow("login name 1", testName(1), testClient(1))
	h.allow("login name 2", testName(2), testClient(1))
	h.refuse("a new login name with client 1", testName(3), testClient(1), time.Minute)
	h.refuse("a new login name with a new client", testName(3), testClient(2), time.Minute)
	if got, want := h.warned.take(), []string{whatNames, whatNames}; !slices.Equal(got, want) {
		t.Errorf("warn was called with %q, want %q", got, want)
	}

	// Pairs, login names and clients that are all full.
	h = newSizedHarness(t, 2, 2, 2)
	for i := 1; i <= 2; i++ {
		h.waitingPair(i, testClient(i))
	}
	h.refuse("a new login name and a new client", testName(3), testClient(3), time.Minute)
	if got, want := h.warned.take(), []string{whatPairs, whatNames, whatClients}; !slices.Equal(got, want) {
		t.Errorf("warn was called with %q, want %q", got, want)
	}
	h.refuse("a new login name and a client", testName(3), testClient(1), time.Minute)
	if got, want := h.warned.take(), []string{whatPairs, whatNames}; !slices.Equal(got, want) {
		t.Errorf("warn was called with %q, want %q", got, want)
	}
	h.refuse("a login name and a new client", testName(1), testClient(3), time.Minute)
	if got, want := h.warned.take(), []string{whatPairs, whatClients}; !slices.Equal(got, want) {
		t.Errorf("warn was called with %q, want %q", got, want)
	}
	h.refuse("a pair that waits", testName(1), testClient(1), 30*second)
	if got := h.warned.take(); len(got) != 0 {
		t.Errorf("warn was called with %q for a pair that is counted", got)
	}
}

func TestCapacityTheLongerWaitIsTheRetry(t *testing.T) {
	// A busy client that waits for 30 seconds, and a table of login names that has no room for a minute: a minute.
	h := newSizedHarness(t, 1000, 30, 100)
	busy := testClient(1)
	for i := range 30 {
		h.allow("the busy client", testName(100+i), busy)
	}
	h.refuse("a new login name from the busy client", testName(200), busy, time.Minute)
	if got, want := h.warned.take(), []string{whatNames}; !slices.Equal(got, want) {
		t.Errorf("warn was called with %q, want %q", got, want)
	}

	// A login name that is full for 15 minutes, and a table of clients that has no room for a minute: 15 minutes.
	h = newSizedHarness(t, 1000, 100, 100)
	h.fill(testName(1), 100)
	h.refuse("login name 1 from a client", testName(1), h.fresh(), 15*time.Minute)
	if got := h.warned.take(); len(got) != 1 || got[0] != whatClients {
		t.Errorf("warn was called with %q, want %q", got, whatClients)
	}
}

// TestCapacityZero shows that tables with no room refuse every attempt, and do not fail.
func TestCapacityZero(t *testing.T) {
	h := newSizedHarness(t, 0, 0, 0)
	h.refuse("a login name from a client", testName(1), testClient(1), time.Minute)
	if got, want := h.warned.take(), []string{whatPairs, whatNames, whatClients}; !slices.Equal(got, want) {
		t.Errorf("warn was called with %q, want %q", got, want)
	}
	h.a.succeeded(testName(1), testClient(1))
	h.a.aborted(testName(1), testClient(1))
	h.check()
}

// TestCapacityIsNeverExceeded sends many login names and clients, as an attack does.
func TestCapacityIsNeverExceeded(t *testing.T) {
	h := newSizedHarness(t, 10, 10, 10)
	for i := range 20000 {
		h.a.begin(testName(i), testClient(i))
		if i%97 == 0 {
			h.advance(time.Duration(i%7) * 10 * second)
		}
		if i%50 == 0 {
			h.check()
		}
	}
	h.check()
	if n := len(h.a.pairs.entries); n > 10 {
		t.Errorf("%d pairs are counted, at most 10", n)
	}
	if n := len(h.a.names.entries); n > 10 {
		t.Errorf("%d login names are counted, at most 10", n)
	}
	if n := len(h.a.clients.entries); n > 10 {
		t.Errorf("%d clients are counted, at most 10", n)
	}
}

// TestAttemptsCountsAreExact lets many goroutines try login names and clients at once, with the time standing still, and
// compares what they were allowed with what is counted.
func TestAttemptsCountsAreExact(t *testing.T) {
	// Many login names and few clients: the pairs and the clients are the limits. Few login names and many clients: the
	// pairs and the login names are.
	for _, cfg := range []struct{ names, clients int }{{20, 10}, {2, 60}} {
		const workers, steps = 16, 2000
		h := newHarness(t, 1000)
		now := int64(h.a.elapsed())
		allowedPair := make([]atomic.Int32, cfg.names*cfg.clients)
		allowedName := make([]atomic.Int32, cfg.names)
		allowedClient := make([]atomic.Int32, cfg.clients)
		var wg sync.WaitGroup
		for w := range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := rand.New(rand.NewPCG(uint64(w), 1))
				for range steps {
					n, c := r.IntN(cfg.names), r.IntN(cfg.clients)
					if _, ok := h.a.begin(testName(n), testClient(c)); ok {
						allowedPair[n*cfg.clients+c].Add(1)
						allowedName[n].Add(1)
						allowedClient[c].Add(1)
					}
				}
			}()
		}
		wg.Wait()
		h.check()

		st := h.a.state()
		total := 0
		for n := range cfg.names {
			for c := range cfg.clients {
				got := int(allowedPair[n*cfg.clients+c].Load())
				total += got
				if got > freeAttempts {
					t.Errorf("login name %d from client %d was allowed %d attempts, at most %d", n, c, got, freeAttempts)
				}
				if count := st.pairs[newPairKey(testName(n), testClient(c))][0]; int(count) != got {
					t.Errorf("login name %d from client %d was allowed %d attempts and has the count %d", n, c, got, count)
				}
			}
			got := int(allowedName[n].Load())
			if got > nameBurst {
				t.Errorf("login name %d was allowed %d attempts, at most %d", n, got, nameBurst)
			}
			want := int64(0)
			if got > 0 {
				want = now + int64(got)*int64(nameLeak)
			}
			if st.names[testName(n)] != want {
				t.Errorf("login name %d was allowed %d attempts and expires at %v, want %v", n, got, time.Duration(st.names[testName(n)]), time.Duration(want))
			}
		}
		for c := range cfg.clients {
			got := int(allowedClient[c].Load())
			if got > clientBurst {
				t.Errorf("client %d was allowed %d attempts, at most %d", c, got, clientBurst)
			}
			want := int64(0)
			if got > 0 {
				want = now + int64(got)*int64(clientLeak)
			}
			if st.clients[testClient(c)] != want {
				t.Errorf("client %d was allowed %d attempts and expires at %v, want %v", c, got, time.Duration(st.clients[testClient(c)]), time.Duration(want))
			}
		}
		if total == 0 {
			t.Error("no attempt was allowed")
		}
	}
}

// TestAttemptsConcurrentMix runs every call at once, with small tables, so that the tables are dropping entries all the
// time.
func TestAttemptsConcurrentMix(t *testing.T) {
	h := newSizedHarness(t, 12, 12, 12)
	const workers, steps = 8, 3000
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(w), 2))
			for i := range steps {
				name, client := testName(r.IntN(40)), testClient(r.IntN(40))
				switch op := r.IntN(10); {
				case op < 6:
					h.a.begin(name, client)
				case op < 7:
					h.a.succeeded(name, client)
				case op < 8:
					h.a.aborted(name, client)
				default:
					h.clock.advance(time.Duration(r.IntN(100)) * second)
				}
				if i%100 == 0 {
					if err := h.a.check(); err != nil {
						t.Errorf("the tables are not consistent: %v", err)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	h.check()
}
