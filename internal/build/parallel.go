package build

import (
	"context"
	"sync"
	"sync/atomic"
)

// forEach calls f with each index below n in at most workers goroutines (at least one), which take the indexes in
// increasing order, and returns ctx.Err() if ctx is done, or else the error of the lowest index for which f returned
// one. The goroutines stop taking indexes once ctx is done or f has returned an error; f is called with every index
// taken, so with every index below that of an error, and the error returned is the same however the calls interleave.
func forEach(ctx context.Context, n, workers int, f func(i int) error) error {
	errs := make([]error, n)
	var next atomic.Int64
	var failed atomic.Bool
	var wg sync.WaitGroup
	for range min(max(workers, 1), n) {
		wg.Go(func() {
			for !failed.Load() && ctx.Err() == nil {
				i := int(next.Add(1) - 1)
				if i >= n {
					return
				}
				if errs[i] = f(i); errs[i] != nil {
					failed.Store(true)
				}
			}
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// onceMap holds the results of functions by key, each called once for all the callers with its key. The zero value is
// empty and ready to use. It is safe for concurrent use.
type onceMap[K comparable, V any] struct {
	mu sync.Mutex
	m  map[K]func() V
}

// get returns the result of f for k: the first caller with k calls f, and the others with k wait for it and get the
// same result.
func (o *onceMap[K, V]) get(k K, f func() V) V {
	o.mu.Lock()
	g := o.m[k]
	if g == nil {
		if o.m == nil {
			o.m = make(map[K]func() V)
		}
		g = sync.OnceValue(f)
		o.m[k] = g
	}
	o.mu.Unlock()
	return g()
}
