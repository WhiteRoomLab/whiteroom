package imaging

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// waitFor waits until cond holds, or fails the test after 10 seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// state returns the bytes taken from b and its waiters.
func (b *budget) state() (used int64, waiters int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used, b.waiters.Len()
}

// acquireAsync acquires n bytes of b in a goroutine, and sends name, or the error, to got when it returns.
func acquireAsync(ctx context.Context, b *budget, name string, n int64, got chan<- string) {
	go func() {
		if _, err := b.acquire(ctx, n); err != nil {
			got <- err.Error()
			return
		}
		got <- name
	}()
}

func receive(t *testing.T, got <-chan string, want string) {
	t.Helper()
	select {
	case name := <-got:
		if name != want {
			t.Fatalf("%s returned, want %s", name, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", want)
	}
}

// receiveAll receives what each of the jobs of want sends, in any order.
func receiveAll(t *testing.T, got <-chan string, want ...string) {
	t.Helper()
	want = slices.Sorted(slices.Values(want))
	var names []string
	for range want {
		select {
		case name := <-got:
			names = append(names, name)
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for %s", strings.Join(want, " and "))
		}
	}
	if slices.Sort(names); !slices.Equal(names, want) {
		t.Fatalf("%s returned, want %s", strings.Join(names, " and "), strings.Join(want, " and "))
	}
}

func checkState(t *testing.T, b *budget, used int64, waiters int) {
	t.Helper()
	if u, w := b.state(); u != used || w != waiters {
		t.Fatalf("%d bytes used and %d waiters, want %d and %d", u, w, used, waiters)
	}
}

func TestBudgetOrder(t *testing.T) {
	ctx := t.Context()
	b := newBudget(10)
	if n, err := b.acquire(ctx, 6); n != 6 || err != nil {
		t.Fatalf("acquire(6) = %d, %v", n, err)
	}
	got := make(chan string, 2)
	acquireAsync(ctx, b, "8", 8, got)
	waitFor(t, "a waiter of 8 bytes", func() bool { _, w := b.state(); return w == 1 })
	// 3 bytes are free, but the job of 8 bytes comes first.
	acquireAsync(ctx, b, "3", 3, got)
	waitFor(t, "a waiter of 3 bytes", func() bool { _, w := b.state(); return w == 2 })
	b.release(6)
	receive(t, got, "8")
	checkState(t, b, 8, 1)
	b.release(8)
	receive(t, got, "3")
	checkState(t, b, 3, 0)
	b.release(3)
	checkState(t, b, 0, 0)
}

func TestBudgetLargeJob(t *testing.T) {
	ctx := t.Context()
	b := newBudget(10)
	if n, err := b.acquire(ctx, 6); n != 6 || err != nil {
		t.Fatalf("acquire(6) = %d, %v", n, err)
	}
	got := make(chan string, 1)
	acquireAsync(ctx, b, "100", 100, got)
	waitFor(t, "a waiter of 100 bytes", func() bool { _, w := b.state(); return w == 1 })
	b.release(6)
	// It takes the whole budget, and runs alone.
	receive(t, got, "100")
	checkState(t, b, 10, 0)
	acquireAsync(ctx, b, "1", 1, got)
	waitFor(t, "a waiter of 1 byte", func() bool { _, w := b.state(); return w == 1 })
	b.release(10)
	receive(t, got, "1")
	b.release(1)
	if n, err := b.acquire(ctx, 0); n != 0 || err != nil {
		t.Errorf("acquire(0) = %d, %v", n, err)
	}
	checkState(t, b, 0, 0)
}

func TestBudgetCancel(t *testing.T) {
	ctx := t.Context()
	b := newBudget(10)
	if _, err := b.acquire(ctx, 6); err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 2)
	canceled, cancel := context.WithCancel(ctx)
	acquireAsync(canceled, b, "8", 8, got)
	waitFor(t, "a waiter of 8 bytes", func() bool { _, w := b.state(); return w == 1 })
	acquireAsync(ctx, b, "3", 3, got)
	waitFor(t, "a waiter of 3 bytes", func() bool { _, w := b.state(); return w == 2 })
	// The first waiter leaves, and the next one fits; they return in either order.
	cancel()
	receiveAll(t, got, context.Canceled.Error(), "3")
	checkState(t, b, 9, 0)
	// A done context is an error even when the bytes are free.
	b.release(9)
	if n, err := b.acquire(canceled, 1); n != 0 || !errors.Is(err, context.Canceled) {
		t.Errorf("acquire() with a done context = %d, %v", n, err)
	}
	checkState(t, b, 0, 0)
}

// TestBudgetConcurrent checks that many jobs never take more than the budget together, and all of them run.
func TestBudgetConcurrent(t *testing.T) {
	const size = 100
	b := newBudget(size)
	var (
		ctx  = t.Context()
		done = make(chan int64)
	)
	for i := range 200 {
		go func() {
			n, err := b.acquire(ctx, int64(i%150))
			if err != nil {
				t.Error(err)
			}
			if used, _ := b.state(); used > size {
				t.Errorf("%d bytes used of %d", used, size)
			}
			time.Sleep(time.Duration(i%3) * time.Millisecond)
			b.release(n)
			done <- n
		}()
	}
	for range 200 {
		<-done
	}
	checkState(t, b, 0, 0)
}
