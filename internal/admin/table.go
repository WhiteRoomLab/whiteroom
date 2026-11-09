package admin

import (
	"container/heap"
	"time"
)

// entry is what a table keeps of one key. Times are offsets from the start of the clock of the attempts.
type entry[K comparable] struct {
	key        K
	count      int32         // login names: the attempts counted; clients: unused
	index      int32         // in the queue
	expires    time.Duration // when the entry says nothing any more
	prev, next *entry[K]     // in the list of entries that may be dropped before they expire
}

// table keeps at most capacity entries by key. The queue finds the entry that expires first, and idle the least
// recently used of those that may be dropped before they expire. It is not safe for concurrent use.
type table[K comparable] struct {
	what     string
	capacity int
	entries  map[K]*entry[K]
	queue    expiryQueue[K] // the soonest to expire first
	idle     list[K]        // the most recently used first
}

func newTable[K comparable](what string, capacity int) *table[K] {
	t := &table[K]{what: what, capacity: capacity, entries: make(map[K]*entry[K])}
	t.idle.root.prev, t.idle.root.next = &t.idle.root, &t.idle.root
	return t
}

// victim returns the entry to drop for a key that has none when the table is full: one that has expired, else the least
// recently used of those that may be dropped. It returns nil if there is none.
func (t *table[K]) victim(now time.Duration) *entry[K] {
	if len(t.queue) > 0 && t.queue[0].expires <= now {
		return t.queue[0]
	}
	return t.idle.back()
}

// room reports whether a key that has no entry can get one.
func (t *table[K]) room(now time.Duration) bool {
	return len(t.entries) < t.capacity || t.victim(now) != nil
}

// insert adds an entry for a key that has none. The caller has checked that there is room.
func (t *table[K]) insert(key K, expires, now time.Duration) *entry[K] {
	if len(t.entries) >= t.capacity {
		t.remove(t.victim(now))
	}
	e := &entry[K]{key: key, expires: expires}
	t.entries[key] = e
	heap.Push(&t.queue, e)
	return e
}

func (t *table[K]) update(e *entry[K], expires time.Duration) {
	e.expires = expires
	heap.Fix(&t.queue, int(e.index))
}

func (t *table[K]) remove(e *entry[K]) {
	delete(t.entries, e.key)
	heap.Remove(&t.queue, int(e.index))
	t.idle.unlink(e)
}

// expiryQueue is a heap of entries ordered by when they expire.
type expiryQueue[K comparable] []*entry[K]

func (q expiryQueue[K]) Len() int           { return len(q) }
func (q expiryQueue[K]) Less(i, j int) bool { return q[i].expires < q[j].expires }

func (q expiryQueue[K]) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]
	q[i].index, q[j].index = int32(i), int32(j)
}

func (q *expiryQueue[K]) Push(x any) {
	e := x.(*entry[K])
	e.index = int32(len(*q))
	*q = append(*q, e)
}

func (q *expiryQueue[K]) Pop() any {
	old := *q
	last := len(old) - 1
	e := old[last]
	old[last] = nil
	*q = old[:last]
	e.index = -1
	return e
}

// list is a circular doubly linked list through the entries, with root as its sentinel. An entry that is in no list
// has nil links.
type list[K comparable] struct{ root entry[K] }

func (l *list[K]) back() *entry[K] {
	if e := l.root.prev; e != &l.root {
		return e
	}
	return nil
}

// pushFront puts e at the front of the list, moving it if it is there.
func (l *list[K]) pushFront(e *entry[K]) {
	l.unlink(e)
	e.prev, e.next = &l.root, l.root.next
	e.prev.next, e.next.prev = e, e
}

func (l *list[K]) unlink(e *entry[K]) {
	if e.prev == nil {
		return
	}
	e.prev.next, e.next.prev = e.next, e.prev
	e.prev, e.next = nil, nil
}
