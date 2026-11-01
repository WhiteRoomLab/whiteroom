package build

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestForEach(t *testing.T) {
	for _, workers := range []int{-1, 0, 1, 3, 200} {
		var calls [50]atomic.Int32
		err := forEach(t.Context(), len(calls), workers, func(i int) error {
			calls[i].Add(1)
			return nil
		})
		if err != nil {
			t.Errorf("forEach() with %d workers = %v", workers, err)
		}
		for i := range calls {
			if n := calls[i].Load(); n != 1 {
				t.Errorf("forEach() with %d workers called f(%d) %d times, want once", workers, i, n)
			}
		}
	}

	if err := forEach(t.Context(), 0, 4, func(i int) error {
		t.Errorf("f(%d) called for no indexes", i)
		return nil
	}); err != nil {
		t.Errorf("forEach() of no indexes = %v", err)
	}

	// The error of the lowest index, which every index below it has run before.
	for _, workers := range []int{1, 2, 8} {
		var calls [100]atomic.Int32
		err := forEach(t.Context(), len(calls), workers, func(i int) error {
			calls[i].Add(1)
			if i == 30 || i == 70 {
				return fmt.Errorf("error %d", i)
			}
			return nil
		})
		if err == nil || err.Error() != "error 30" {
			t.Errorf("forEach() with %d workers = %v, want error 30", workers, err)
		}
		for i := range 31 {
			if n := calls[i].Load(); n != 1 {
				t.Errorf("forEach() with %d workers called f(%d) %d times, want once", workers, i, n)
			}
		}
		if n := calls[70].Load(); workers == 1 && n != 0 {
			t.Errorf("forEach() with 1 worker called f(70) after the error of f(30)")
		}
	}

	// The error of the lower index, although the higher one returns first.
	ran31 := make(chan struct{})
	err := forEach(t.Context(), 100, 4, func(i int) error {
		switch i {
		case 30:
			<-ran31
			return errors.New("error 30")
		case 31:
			close(ran31)
			return errors.New("error 31")
		}
		return nil
	})
	if err == nil || err.Error() != "error 30" {
		t.Errorf("forEach() with errors at 30 and then 31 = %v, want error 30", err)
	}

	var running, most atomic.Int32
	err = forEach(t.Context(), 40, 4, func(int) error {
		n := running.Add(1)
		for {
			if m := most.Load(); n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		running.Add(-1)
		return nil
	})
	if n := most.Load(); err != nil || n > 4 {
		t.Errorf("forEach() with 4 workers = %v, with %d calls at once", err, n)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := forEach(ctx, 10, 2, func(i int) error {
		t.Errorf("f(%d) called after the context was done", i)
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Errorf("forEach() with a done context = %v, want %v", err, context.Canceled)
	}

	// The other goroutine may have taken an index before the context was done, but no more.
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	var late atomic.Int32
	err = forEach(ctx, 1000, 2, func(i int) error {
		if ctx.Err() != nil {
			late.Add(1)
		}
		if i == 10 {
			cancel()
		}
		return nil
	})
	if n := late.Load(); !errors.Is(err, context.Canceled) || n > 1 {
		t.Errorf("forEach() canceled by f(10) = %v, with %d calls after that, want %v after one at most",
			err, n, context.Canceled)
	}
}

// TestOnceMap checks that onceMap calls the function of each key once, while the other callers with the key wait for
// its result.
func TestOnceMap(t *testing.T) {
	var m onceMap[int, string]
	var calls [3]atomic.Int32
	release := make(chan struct{})
	got := make([]string, 30)
	var wg sync.WaitGroup
	for i := range got {
		wg.Go(func() {
			k := i % len(calls)
			got[i] = m.get(k, func() string {
				calls[k].Add(1)
				<-release
				return fmt.Sprint("value ", k)
			})
		})
	}
	close(release)
	wg.Wait()
	for i, s := range got {
		if want := fmt.Sprint("value ", i%len(calls)); s != want {
			t.Errorf("get(%d) = %q, want %q", i%len(calls), s, want)
		}
	}
	for k := range calls {
		if n := calls[k].Load(); n != 1 {
			t.Errorf("the function of %d was called %d times, want once", k, n)
		}
	}
	if s := m.get(1, func() string { return "again" }); s != "value 1" {
		t.Errorf("get(1) after it was called = %q, want %q", s, "value 1")
	}
	if s := m.get(3, func() string { return "new" }); s != "new" {
		t.Errorf("get(3) = %q, want %q", s, "new")
	}
}
