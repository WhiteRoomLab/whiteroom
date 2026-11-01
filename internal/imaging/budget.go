package imaging

import (
	"container/list"
	"context"
	"sync"
)

// budget is a weighted semaphore of bytes of memory: a job acquires the bytes it takes before it runs and releases
// them after. Jobs that wait are served in their order, so that a large job is not kept waiting by smaller ones that
// come after it.
type budget struct {
	mu      sync.Mutex
	size    int64
	used    int64
	waiters list.List // of *budgetWaiter, in their order
}

type budgetWaiter struct {
	n     int64
	ready chan struct{} // closed when the bytes are acquired
}

func newBudget(size int64) *budget {
	return &budget{size: size}
}

// acquire waits until n bytes are free and takes them, or returns ctx.Err() if ctx is done first, even if they are
// free. It returns the bytes it took, to release: n, or the whole budget if n is larger, so that such a job runs alone.
func (b *budget) acquire(ctx context.Context, n int64) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	n = min(max(n, 0), b.size)
	b.mu.Lock()
	if b.waiters.Len() == 0 && b.size-b.used >= n {
		b.used += n
		b.mu.Unlock()
		return n, nil
	}
	w := &budgetWaiter{n: n, ready: make(chan struct{})}
	e := b.waiters.PushBack(w)
	b.mu.Unlock()
	select {
	case <-w.ready:
		return n, nil
	case <-ctx.Done():
		b.mu.Lock()
		defer b.mu.Unlock()
		select {
		case <-w.ready:
			// Acquired after ctx was done: give the bytes back.
			b.used -= n
		default:
			b.waiters.Remove(e)
		}
		// The waiters after it may fit now.
		b.notify()
		return 0, ctx.Err()
	}
}

// release frees n bytes that acquire took.
func (b *budget) release(n int64) {
	b.mu.Lock()
	b.used -= n
	b.notify()
	b.mu.Unlock()
}

// notify gives their bytes to the waiters from the first while they fit.
func (b *budget) notify() {
	for e := b.waiters.Front(); e != nil; e = b.waiters.Front() {
		w := e.Value.(*budgetWaiter)
		if b.size-b.used < w.n {
			return
		}
		b.used += w.n
		b.waiters.Remove(e)
		close(w.ready)
	}
}
