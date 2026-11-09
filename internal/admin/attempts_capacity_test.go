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

// pairOrder returns the ids of the login names of the pairs with these keys, in their order; clientOf says the client of
// the pair of a login name.
func (h *harness) pairOrder(keys []pairKey, clientOf func(name int) netip.Prefix) []int {
	ids := map[pairKey]int{}
	for i := range 2000 {
		ids[newPairKey(testName(i), clientOf(i))] = i
	}
	var order []int
	for _, k := range keys {
		id, ok := ids[k]
		if !ok {
			h.t.Fatalf("a pair in a list is not one of the login names 0 to 1999 with its client")
		}
		order = append(order, id)
	}
	return order
}

// idleOrder returns the ids of the login names of the pairs that may be dropped, from the most recently used.
func (h *harness) idleOrder(clientOf func(name int) netip.Prefix) []int {
	return h.pairOrder(h.a.state().idle, clientOf)
}

// heldOrder returns the ids of the login names of the pairs that wait, from the one whose last attempt is the latest.
func (h *harness) heldOrder(clientOf func(name int) netip.Prefix) []int {
	return h.pairOrder(h.a.state().held, clientOf)
}

// nameOrder returns the ids of the login names that may be dropped, from the most recently used.
func (h *harness) nameOrder() []int {
	ids := map[[32]byte]int{}
	for i := range 2000 {
		ids[testName(i)] = i
	}
	var order []int
	for _, k := range h.a.state().namesIdle {
		id, ok := ids[k]
		if !ok {
			h.t.Fatalf("a login name in the list is not one of 0 to 1999")
		}
		order = append(order, id)
	}
	return order
}

// clientOrder returns the ids of the clients that may be dropped, from the most recently used.
func (h *harness) clientOrder() []int {
	ids := map[netip.Prefix]int{}
	for i := range 2000 {
		ids[testClient(i)] = i
	}
	var order []int
	for _, k := range h.a.state().clientsIdle {
		id, ok := ids[k]
		if !ok {
			h.t.Fatalf("a client in the list is not one of 0 to 1999")
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

	// Many new pairs take each other's places, and never that of a pair that waits, as long as there is one that does not.
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
	if got, want := h.heldOrder(clientOf), []int{1}; !slices.Equal(got, want) {
		t.Errorf("the pairs that wait are %v, want %v", got, want)
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

// TestCapacityPairsAreNeverRefused fills the table with pairs that wait, as an attack does: a new pair is let in, and takes
// the place of the pair whose last attempt is the oldest.
func TestCapacityPairsAreNeverRefused(t *testing.T) {
	h := newSizedHarness(t, 3, 1000, 1000)
	clients := map[int]netip.Prefix{}
	clientOf := func(name int) netip.Prefix { return clients[name] }
	wait := func(name int) {
		t.Helper()
		clients[name] = h.fresh()
		h.waitingPair(name, clients[name])
		h.advance(second)
	}
	counted := func(names ...int) {
		t.Helper()
		for name := 1; name <= 10; name++ {
			if want := slices.Contains(names, name); h.hasPairOf(testName(name), clients[name]) != want {
				t.Errorf("pair %d is counted = %v, want %v", name, !want, want)
			}
		}
	}
	for i := 1; i <= 3; i++ {
		wait(i)
	}
	// The pairs 1, 2 and 3 wait, and their last attempts are a second apart.
	if got, want := h.heldOrder(clientOf), []int{3, 2, 1}; !slices.Equal(got, want) {
		t.Fatalf("the pairs that wait are in the order %v, want %v", got, want)
	}
	if got := h.idleOrder(clientOf); len(got) != 0 {
		t.Fatalf("the pairs that may be dropped are %v, want none", got)
	}

	// A new pair is let in, and the one with the oldest last attempt goes, then the next.
	wait(4)
	counted(2, 3, 4)
	wait(5)
	counted(3, 4, 5)
	if got, want := h.heldOrder(clientOf), []int{5, 4, 3}; !slices.Equal(got, want) {
		t.Errorf("the pairs that wait are in the order %v, want %v", got, want)
	}

	// An attempt of a pair that waits is a use of it: it is the pair 4 that goes next, and not the pair 3.
	h.advance(time.Minute)
	h.allow("the pair 3, after its wait", testName(3), clients[3])
	if got, want := h.heldOrder(clientOf), []int{3, 5, 4}; !slices.Equal(got, want) {
		t.Errorf("after an attempt of the pair 3 they are in the order %v, want %v", got, want)
	}
	wait(6)
	counted(3, 5, 6)

	// A pair that was dropped is new when it comes back: it has its free attempts again, and it takes the place of another.
	for i := 1; i <= 5; i++ {
		h.allow(fmt.Sprintf("attempt %d of the pair 1, which was dropped", i), testName(1), clients[1])
	}
	h.refuse("attempt 6 of it", testName(1), clients[1], 30*second)
	counted(1, 3, 6)
	if got := h.warned.take(); len(got) != 0 {
		t.Errorf("warn was called with %q: a pair is never refused for lack of room", got)
	}

	// The login names and the clients of the pairs that were dropped are counted as before.
	if got := h.nameLevel(2); got != freeAttempts {
		t.Errorf("the login name of the pair that was dropped has the level %d, want %d", got, freeAttempts)
	}
}

// TestCapacityPairsWithNoRoomAtAll shows that a table of pairs keeps one pair at least, whatever capacity it is given.
func TestCapacityPairsWithNoRoomAtAll(t *testing.T) {
	h := newSizedHarness(t, 0, 100, 100)
	if h.a.pairs.capacity != 1 {
		t.Errorf("the table of pairs has the capacity %d, want 1", h.a.pairs.capacity)
	}
	for i := 1; i <= 5; i++ {
		h.allow("a pair", testName(i), testClient(i))
	}
	if n := len(h.a.pairs.entries); n != 1 {
		t.Errorf("%d pairs are counted, want 1", n)
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

// levelKind is a kind of level under test: the login names, or the clients. Its tests are the same for both.
type levelKind struct {
	name    string
	what    string // what warn gets when the table is full
	leak    time.Duration
	burst   int
	harness func(t *testing.T, capacity int) *harness // with the capacity of this table
	args    func(h *harness, key int) ([32]byte, netip.Prefix)
	has     func(h *harness, key int) bool
	level   func(h *harness, key int) int
	order   func(h *harness) []int
}

var levelKinds = []levelKind{
	{
		name: "login names", what: whatNames, leak: nameLeak, burst: nameBurst,
		harness: func(t *testing.T, capacity int) *harness { return newSizedHarness(t, 1000, capacity, 1000) },
		args:    func(h *harness, key int) ([32]byte, netip.Prefix) { return testName(key), h.fresh() },
		has:     (*harness).hasName,
		level:   (*harness).nameLevel,
		order:   (*harness).nameOrder,
	},
	{
		name: "clients", what: whatClients, leak: clientLeak, burst: clientBurst,
		harness: func(t *testing.T, capacity int) *harness { return newSizedHarness(t, 1000, 1000, capacity) },
		args:    func(h *harness, key int) ([32]byte, netip.Prefix) { return h.freshName(), testClient(key) },
		has:     (*harness).hasClient,
		level:   (*harness).clientLevel,
		order:   (*harness).clientOrder,
	},
}

// count counts n attempts that have the key as their login name, or their client, which have to be let in.
func (k levelKind) count(h *harness, key, n int) {
	h.t.Helper()
	for range n {
		name, client := k.args(h, key)
		h.allow(fmt.Sprintf("an attempt of %s %d", k.name, key), name, client)
	}
}

// refuse checks that an attempt of the key is refused for lack of room.
func (k levelKind) refuse(h *harness, what string, key int) {
	h.t.Helper()
	name, client := k.args(h, key)
	h.refuse(what, name, client, fullRetry)
}

// end ends an attempt of the key with f, which gives its attempt back.
func (k levelKind) end(h *harness, f func(*attempts, [32]byte, netip.Prefix), key int) {
	h.t.Helper()
	name, client := k.args(h, key)
	f(h.a, name, client)
	h.check()
}

func TestCapacityLevelsDropTheLeastRecentlyUsed(t *testing.T) {
	for _, k := range levelKinds {
		t.Run(k.name, func(t *testing.T) {
			h := k.harness(t, 3)
			for key := 1; key <= 3; key++ {
				k.count(h, key, 1)
				h.advance(second)
			}
			if got, want := k.order(h), []int{3, 2, 1}; !slices.Equal(got, want) {
				t.Fatalf("the entries are in the order %v, want %v", got, want)
			}

			// The table is full of entries with one attempt, which may be dropped: a fourth key takes the place of the one
			// that was used least recently, and nothing is refused.
			k.count(h, 4, 1)
			if k.has(h, 1) || !k.has(h, 2) || !k.has(h, 3) || !k.has(h, 4) {
				t.Errorf("after key 4: counted 1: %v, 2: %v, 3: %v, 4: %v, want false, true, true, true",
					k.has(h, 1), k.has(h, 2), k.has(h, 3), k.has(h, 4))
			}
			if got, want := k.order(h), []int{4, 3, 2}; !slices.Equal(got, want) {
				t.Errorf("after key 4 the entries are in the order %v, want %v", got, want)
			}

			// Another attempt is a use of the key.
			k.count(h, 2, 1)
			if got, want := k.order(h), []int{2, 4, 3}; !slices.Equal(got, want) {
				t.Errorf("after another attempt of key 2 the entries are in the order %v, want %v", got, want)
			}
			k.count(h, 5, 1)
			if k.has(h, 3) || !k.has(h, 2) || !k.has(h, 4) || !k.has(h, 5) {
				t.Errorf("after key 5: counted 2: %v, 3: %v, 4: %v, 5: %v, want true, false, true, true",
					k.has(h, 2), k.has(h, 3), k.has(h, 4), k.has(h, 5))
			}

			// A key that was dropped starts again.
			k.count(h, 1, 1)
			if got := k.level(h, 1); got != 1 {
				t.Errorf("key 1, which was dropped, has the level %d after an attempt, want 1", got)
			}
			if got := h.warned.take(); len(got) != 0 {
				t.Errorf("warn was called with %q: nothing was refused", got)
			}
		})
	}
}

func TestCapacityLevelsDropWhatHasExpiredFirst(t *testing.T) {
	for _, k := range levelKinds {
		t.Run(k.name, func(t *testing.T) {
			h := k.harness(t, 2)
			k.count(h, 1, 3) // expires in three leaks
			h.advance(k.leak / 10)
			k.count(h, 2, 1) // used later than key 1, and expires in one leak
			h.advance(2 * k.leak)

			// Key 2 has expired and key 1 has not, and key 1 is the one that was used least recently.
			k.count(h, 3, 1)
			if !k.has(h, 1) || k.has(h, 2) || !k.has(h, 3) {
				t.Errorf("after key 3: counted 1: %v, 2: %v, 3: %v, want true, false, true", k.has(h, 1), k.has(h, 2), k.has(h, 3))
			}
		})
	}
}

// TestCapacityLevelsKeepTheOnesThatHaveAttempts shows the rule that caps the guesses on a login name: an entry with
// freeAttempts attempts or more is never dropped for a new key, however many come, and a new key is refused only when every
// entry is of that kind.
func TestCapacityLevelsKeepTheOnesThatHaveAttempts(t *testing.T) {
	for _, k := range levelKinds {
		t.Run(k.name, func(t *testing.T) {
			h := k.harness(t, 3)
			k.count(h, 1, k.burst)        // the most that it has at once
			k.count(h, 2, freeAttempts)   // the least that is kept
			k.count(h, 3, freeAttempts-1) // one less: it may be dropped
			if got := k.level(h, 2); got != freeAttempts {
				t.Fatalf("key 2 has the level %d, want %d", got, freeAttempts)
			}

			// A new key takes the place of key 3, which may be dropped, and a flood of new keys takes each other's places.
			last := 0
			for key := 4; key < 50; key++ {
				k.count(h, key, 1)
				if !k.has(h, 1) || !k.has(h, 2) || !k.has(h, key) {
					t.Fatalf("after key %d: counted 1: %v, 2: %v, %d: %v, want true each", key, k.has(h, 1), k.has(h, 2), key, k.has(h, key))
				}
				// The one that may be dropped is the newest; key 3 and the keys before it are gone.
				if got := k.order(h); !slices.Equal(got, []int{key}) {
					t.Fatalf("after key %d the entries that may be dropped are %v, want only that key", key, got)
				}
				last = key
			}
			if got := h.warned.take(); len(got) != 0 {
				t.Errorf("warn was called with %q: nothing was refused", got)
			}
			if got := k.level(h, 1); got != k.burst {
				t.Errorf("key 1 has the level %d, want %d: its attempts stay counted", got, k.burst)
			}

			// When the last one has freeAttempts attempts too, every entry is kept, and a new key is refused.
			k.count(h, last, freeAttempts-1)
			k.refuse(h, "a new key when every entry has five attempts or more", 100)
			k.refuse(h, "another new key", 101)
			if got, want := h.warned.take(), []string{k.what, k.what}; !slices.Equal(got, want) {
				t.Errorf("warn was called with %q, want %q", got, want)
			}
			if !k.has(h, 1) || !k.has(h, 2) || !k.has(h, last) {
				t.Error("an entry that is kept is dropped")
			}
		})
	}
}

// TestCapacityLevelsAreUpToDate shows that the entries which may be dropped are kept up to date when an attempt is counted
// and when one is given back.
func TestCapacityLevelsAreUpToDate(t *testing.T) {
	ends := []struct {
		name string
		f    func(*attempts, [32]byte, netip.Prefix)
	}{
		{"succeeded", (*attempts).succeeded},
		{"aborted", (*attempts).aborted},
	}
	for _, k := range levelKinds {
		for _, end := range ends {
			t.Run(k.name+", "+end.name, func(t *testing.T) {
				h := k.harness(t, 2)
				k.count(h, 1, freeAttempts)
				k.count(h, 2, freeAttempts-1)
				if got, want := k.order(h), []int{2}; !slices.Equal(got, want) {
					t.Fatalf("the entries that may be dropped are %v, want %v", got, want)
				}

				// The attempt that makes freeAttempts takes the entry out of the list: now every entry is kept.
				k.count(h, 2, 1)
				if got := k.order(h); len(got) != 0 {
					t.Errorf("the entries that may be dropped are %v, want none", got)
				}
				k.refuse(h, "a new key when both have five attempts", 3)
				h.warned.take()

				// An attempt that is given back puts it in again.
				k.end(h, end.f, 2)
				if got, want := k.order(h), []int{2}; !slices.Equal(got, want) {
					t.Errorf("after an attempt of key 2 was given back the entries that may be dropped are %v, want %v", got, want)
				}
				k.count(h, 3, 1)
				if !k.has(h, 1) || k.has(h, 2) || !k.has(h, 3) {
					t.Errorf("after key 3: counted 1: %v, 2: %v, 3: %v, want true, false, true", k.has(h, 1), k.has(h, 2), k.has(h, 3))
				}
				if got := h.warned.take(); len(got) != 0 {
					t.Errorf("warn was called with %q", got)
				}

				// An entry that is in the list stays where it is when an attempt is given back, and one that comes in is the
				// most recently used.
				h = k.harness(t, 3)
				k.count(h, 1, 2)
				k.count(h, 2, 2)
				k.count(h, 3, 2)
				if got, want := k.order(h), []int{3, 2, 1}; !slices.Equal(got, want) {
					t.Fatalf("the entries that may be dropped are %v, want %v", got, want)
				}
				k.end(h, end.f, 1)
				if got, want := k.order(h), []int{3, 2, 1}; !slices.Equal(got, want) {
					t.Errorf("after an attempt of key 1 was given back the entries are in the order %v, want %v", got, want)
				}
				h = k.harness(t, 3)
				k.count(h, 1, freeAttempts)
				k.count(h, 2, 1)
				k.count(h, 3, 1)
				k.end(h, end.f, 1)
				if got, want := k.order(h), []int{1, 3, 2}; !slices.Equal(got, want) {
					t.Errorf("after an attempt of key 1 was given back, which made its level %d, the entries are in the order %v, want %v",
						freeAttempts-1, got, want)
				}
				k.count(h, 4, 1)
				if k.has(h, 2) || !k.has(h, 1) || !k.has(h, 3) || !k.has(h, 4) {
					t.Errorf("after key 4: counted 1: %v, 2: %v, 3: %v, 4: %v, want true, false, true, true",
						k.has(h, 1), k.has(h, 2), k.has(h, 3), k.has(h, 4))
				}
			})
		}
	}
}

// TestCapacityLevelsStayKeptUntilTheyExpire shows what the list being kept up to date only when an attempt is counted or
// given back means: a level that has leaked below freeAttempts, with no attempt since, is kept as it was until it expires.
func TestCapacityLevelsStayKeptUntilTheyExpire(t *testing.T) {
	for _, k := range levelKinds {
		t.Run(k.name, func(t *testing.T) {
			h := k.harness(t, 1)
			k.count(h, 1, freeAttempts)
			h.advance(k.leak)
			if got := k.level(h, 1); got != freeAttempts-1 {
				t.Fatalf("key 1 has the level %d after a leak, want %d", got, freeAttempts-1)
			}
			k.refuse(h, "a new key when the level has leaked to four", 2)
			h.advance((freeAttempts-1)*k.leak - 1)
			k.refuse(h, "a new key a nanosecond before the entry expires", 2)
			h.advance(1)
			k.count(h, 2, 1)
			if k.has(h, 1) || !k.has(h, 2) {
				t.Errorf("after key 2: counted 1: %v, 2: %v, want false, true", k.has(h, 1), k.has(h, 2))
			}
		})
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
	for _, k := range levelKinds {
		for _, end := range ends {
			t.Run(k.name+", "+end.name, func(t *testing.T) {
				h := k.harness(t, 2)
				k.count(h, 1, freeAttempts)
				k.count(h, 2, freeAttempts+1)
				k.refuse(h, "a new key", 3)

				// Key 2 has five attempts after one is given back, and is still kept.
				k.end(h, end.f, 2)
				k.refuse(h, end.name+": a new key while key 2 has five attempts", 3)

				// An entry whose attempts are all back is gone, and has room for a new key that takes nobody's place.
				for range freeAttempts {
					k.end(h, end.f, 1)
				}
				if k.has(h, 1) {
					t.Fatalf("%s: key 1 is still counted", end.name)
				}
				k.count(h, 3, 1)
				if !k.has(h, 2) || !k.has(h, 3) {
					t.Errorf("%s: counted 2: %v, 3: %v, want true, true", end.name, k.has(h, 2), k.has(h, 3))
				}
			})
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

	// A new client when the clients are full of ones with five attempts does not count the pair or the login name.
	h = newSizedHarness(t, 100, 100, 2)
	for i := 1; i <= 2; i++ {
		h.waitingPair(i, testClient(i))
	}
	h.refuse("a new client with login name 1", testName(1), testClient(3), time.Minute)
	h.refuse("a new client with a new login name", testName(4), testClient(3), time.Minute)
	if got, want := h.warned.take(), []string{whatClients, whatClients}; !slices.Equal(got, want) {
		t.Errorf("warn was called with %q, want %q", got, want)
	}

	// A new login name when the login names are full does not count the pair or the client.
	h = newSizedHarness(t, 100, 2, 100)
	for i := 1; i <= 2; i++ {
		h.waitingPair(i, testClient(i))
	}
	h.refuse("a new login name with client 1", testName(3), testClient(1), time.Minute)
	h.refuse("a new login name with a new client", testName(3), testClient(3), time.Minute)
	if got, want := h.warned.take(), []string{whatNames, whatNames}; !slices.Equal(got, want) {
		t.Errorf("warn was called with %q, want %q", got, want)
	}

	// Pairs, login names and clients that are all full of ones that wait.
	h = newSizedHarness(t, 2, 2, 2)
	for i := 1; i <= 2; i++ {
		h.waitingPair(i, testClient(i))
	}
	h.refuse("a new login name and a new client", testName(3), testClient(3), time.Minute)
	if got, want := h.warned.take(), []string{whatNames, whatClients}; !slices.Equal(got, want) {
		t.Errorf("warn was called with %q, want %q", got, want)
	}
	h.refuse("a new login name and a client", testName(3), testClient(1), time.Minute)
	if got, want := h.warned.take(), []string{whatNames}; !slices.Equal(got, want) {
		t.Errorf("warn was called with %q, want %q", got, want)
	}
	h.refuse("a login name and a new client", testName(1), testClient(3), time.Minute)
	if got, want := h.warned.take(), []string{whatClients}; !slices.Equal(got, want) {
		t.Errorf("warn was called with %q, want %q", got, want)
	}
	h.refuse("a pair that waits", testName(1), testClient(1), 30*second)
	if got := h.warned.take(); len(got) != 0 {
		t.Errorf("warn was called with %q for a pair that is counted", got)
	}

	// A new pair of a login name and a client that are counted is let in, though the table of pairs is full of ones that wait:
	// it takes the place of the pair whose last attempt is the oldest, and says nothing.
	h.allow("a new pair of counted ones", testName(1), testClient(2))
	if h.hasPair(1, 1) || !h.hasPair(1, 2) || !h.hasPair(2, 2) {
		t.Errorf("after the new pair: counted 1/1: %v, 1/2: %v, 2/2: %v, want false, true, true", h.hasPair(1, 1), h.hasPair(1, 2), h.hasPair(2, 2))
	}
	if got := h.warned.take(); len(got) != 0 {
		t.Errorf("warn was called with %q for a new pair", got)
	}
}

func TestCapacityTheLongerWaitIsTheRetry(t *testing.T) {
	// A busy client that waits for 30 seconds, and a table of login names that has no room for a minute: a minute. The
	// login names of the busy client are kept by other clients, which give them five attempts each.
	h := newSizedHarness(t, 1000, 1000, 1000)
	busy := testClient(1)
	for i := range 30 {
		h.allow("the busy client", testName(100+i), busy)
	}
	for range freeAttempts - 1 {
		other := h.fresh()
		for i := range 30 {
			h.allow("another client", testName(100+i), other)
		}
	}
	h.a.names.capacity = 30
	h.refuse("a new login name from the busy client", testName(200), busy, time.Minute)
	if got, want := h.warned.take(), []string{whatNames}; !slices.Equal(got, want) {
		t.Errorf("warn was called with %q, want %q", got, want)
	}

	// A login name that is full for 15 minutes, and a table of clients that has no room for a minute: 15 minutes. The
	// clients are kept by attempts on login names that are full too.
	h = newSizedHarness(t, 10000, 100, 1000)
	var clients []netip.Prefix
	for range 100 {
		client := h.fresh()
		clients = append(clients, client)
		for name := 1; name <= freeAttempts; name++ {
			h.allow("an attempt that keeps the client", testName(name), client)
		}
	}
	h.a.clients.capacity = len(clients)
	h.refuse("login name 1 from a new client", testName(1), h.fresh(), 15*time.Minute)
	if got, want := h.warned.take(), []string{whatClients}; !slices.Equal(got, want) {
		t.Errorf("warn was called with %q, want %q", got, want)
	}
}

// TestCapacityZero shows that tables with no room refuse every new login name and client, and do not fail; the table of
// pairs keeps one pair at least, since a new pair is not refused.
func TestCapacityZero(t *testing.T) {
	h := newHarness(t, 0)
	if h.a.pairs.capacity != 1 || h.a.names.capacity != 0 || h.a.clients.capacity != 0 {
		t.Fatalf("the tables have the capacities %d, %d and %d, want 1, 0 and 0", h.a.pairs.capacity, h.a.names.capacity, h.a.clients.capacity)
	}
	h.refuse("a login name from a client", testName(1), testClient(1), time.Minute)
	if got, want := h.warned.take(), []string{whatNames, whatClients}; !slices.Equal(got, want) {
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
	if n := len(h.a.pairs.entries); n != 10 {
		t.Errorf("%d pairs are counted, want 10", n)
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
