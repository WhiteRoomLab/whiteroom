//go:build !race

package admin

import (
	"net/netip"
	"runtime"
	"testing"
	"time"
)

// TestAttemptsMemory fills the three tables with the most that an attack can make them keep, pairs, login names and
// clients that all wait, and measures the memory and the time. The race detector makes both of them larger, so it does
// not run under it.
func TestAttemptsMemory(t *testing.T) {
	const n = attemptsCapacity
	const maxGrowth = 64 << 20
	// The attempts that drop an entry are slower than the ones that refuse; a bound of seconds is for a machine that is busy,
	// and shows that nothing takes time in proportion to the size of a table.
	const maxDropping = 5 * time.Second
	names := make([][32]byte, 2*n)
	clients := make([]netip.Prefix, 2*n)
	for i := range names {
		names[i], clients[i] = testName(i), testClient(i)
	}
	clock := newFakeClock()
	var warned []string

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	a := newAttempts(clock.now, n, func(what string) { warned = append(warned, what) })
	clock.advance(time.Hour)
	filling := time.Now()
	for i := range n {
		for range freeAttempts {
			if retry, ok := a.begin(names[i], clients[i]); !ok {
				t.Fatalf("attempt on login name %d is refused for %v", i, retry)
			}
		}
	}
	filled := time.Since(filling)

	runtime.GC()
	runtime.ReadMemStats(&after)
	growth := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("%d pairs, login names and clients, with %d attempts each: the heap grew by %.1f MiB (%d bytes for a pair, a login name and a client), in %v",
		n, freeAttempts, float64(growth)/(1<<20), growth/n, filled.Round(time.Millisecond))
	if growth > maxGrowth {
		t.Errorf("the heap grew by %.1f MiB, at most %d MiB", float64(growth)/(1<<20), maxGrowth>>20)
	}
	if len(a.pairs.entries) != n || len(a.names.entries) != n || len(a.clients.entries) != n {
		t.Fatalf("%d pairs, %d login names and %d clients are counted, want %d of each",
			len(a.pairs.entries), len(a.names.entries), len(a.clients.entries), n)
	}

	// New pairs, login names and clients, when the tables are full of ones that wait: all are refused.
	refusing := time.Now()
	for i := n; i < 2*n; i++ {
		if retry, ok := a.begin(names[i], clients[i]); ok || retry != time.Minute {
			t.Fatalf("begin of a new login name and client = %v, %v, want 1m0s, false", retry, ok)
		}
	}
	refused := time.Since(refusing)
	t.Logf("%d attempts of new login names and clients on full tables, all refused, took %v (%d calls of warn)",
		n, refused.Round(time.Millisecond), len(warned))
	if refused > time.Second {
		t.Errorf("%d attempts on full tables took %v, want less than 1s", n, refused)
	}
	if want := 3 * n; len(warned) != want {
		t.Errorf("warn was called %d times, want %d", len(warned), want)
	}
	if len(a.pairs.entries) != n || len(a.names.entries) != n || len(a.clients.entries) != n {
		t.Errorf("after the refusals %d pairs, %d login names and %d clients are counted, want %d of each",
			len(a.pairs.entries), len(a.names.entries), len(a.clients.entries), n)
	}

	// When everything is forgotten, a new one takes the place of the one that has been gone longest, one for one.
	clock.advance(25 * time.Hour)
	dropping := time.Now()
	for i := n; i < 2*n; i++ {
		if retry, ok := a.begin(names[i], clients[i]); !ok {
			t.Fatalf("attempt on the new login name %d is refused for %v", i, retry)
		}
	}
	dropped := time.Since(dropping)
	t.Logf("%d attempts of new login names and clients on full tables of ones that are forgotten took %v", n, dropped.Round(time.Millisecond))
	if dropped > maxDropping {
		t.Errorf("%d attempts on full tables of ones that are forgotten took %v, want less than %v", n, dropped, maxDropping)
	}

	// When the login names and the clients have left but the pairs have not, the pairs are the ones that wait, and the
	// least recently used of them is dropped.
	clock.advance(16 * time.Minute)
	replacing := time.Now()
	for i := range n {
		if retry, ok := a.begin(names[i], clients[i]); !ok {
			t.Fatalf("attempt on the login name %d is refused for %v", i, retry)
		}
	}
	replaced := time.Since(replacing)
	t.Logf("%d attempts of new login names and clients on full tables of pairs with 1 attempt, and login names and clients that have left, took %v", n, replaced.Round(time.Millisecond))
	if replaced > maxDropping {
		t.Errorf("%d attempts of that kind took %v, want less than %v", n, replaced, maxDropping)
	}
	for i := n; i < 2*n; i++ {
		if _, ok := a.pairs.entries[newPairKey(names[i], clients[i])]; ok {
			t.Fatalf("pair %d, the least recently used, is still counted", i)
		}
	}
	if err := a.check(); err != nil {
		t.Error(err)
	}
	runtime.KeepAlive(a)
}
