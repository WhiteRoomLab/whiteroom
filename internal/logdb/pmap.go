package logdb

import (
	"fmt"
	"hash/maphash"
	"strings"
)

// owner marks the nodes that one writer has made: its later changes modify them in place instead of copying them.
// A writer that keeps a version for others must use a new owner after it. The field makes owners distinct.
type owner struct{ _ byte }

// pmap is a persistent map, ordered by key: a treap whose changes copy the path to the changed node, unless the
// node belongs to the owner of the change. A nil owner always copies.
type pmap[V any] struct {
	root *pnode[V]
	n    int
}

type pnode[V any] struct {
	key         string
	val         V
	prio        uint64
	left, right *pnode[V]
	owner       *owner
}

// pmapSeed is random in each process, so that no set of keys can be chosen to unbalance a pmap.
var pmapSeed = maphash.MakeSeed()

func priority(key string) uint64 {
	return maphash.String(pmapSeed, key)
}

// above reports whether a is above b in the heap order of priorities; equal priorities go by key.
func above[V any](a, b *pnode[V]) bool {
	return a.prio > b.prio || a.prio == b.prio && a.key < b.key
}

func (t *pnode[V]) own(o *owner) *pnode[V] {
	if o != nil && t.owner == o {
		return t
	}
	c := *t
	c.owner = o
	return &c
}

func (m pmap[V]) Len() int {
	return m.n
}

func (m pmap[V]) Get(key string) (V, bool) {
	for t := m.root; t != nil; {
		switch c := strings.Compare(key, t.key); {
		case c < 0:
			t = t.left
		case c > 0:
			t = t.right
		default:
			return t.val, true
		}
	}
	var zero V
	return zero, false
}

func (m pmap[V]) Put(o *owner, key string, val V) pmap[V] {
	root, added := put(o, m.root, key, val, priority(key))
	if added {
		m.n++
	}
	return pmap[V]{root: root, n: m.n}
}

func put[V any](o *owner, t *pnode[V], key string, val V, prio uint64) (*pnode[V], bool) {
	if t == nil {
		return &pnode[V]{key: key, val: val, prio: prio, owner: o}, true
	}
	c := strings.Compare(key, t.key)
	if c == 0 {
		t = t.own(o)
		t.val = val
		return t, false
	}
	t = t.own(o)
	var added bool
	if c < 0 {
		var l *pnode[V]
		l, added = put(o, t.left, key, val, prio)
		t.left = l
		if above(l, t) {
			t.left, l.right = l.right, t
			return l, added
		}
	} else {
		var r *pnode[V]
		r, added = put(o, t.right, key, val, prio)
		t.right = r
		if above(r, t) {
			t.right, r.left = r.left, t
			return r, added
		}
	}
	return t, added
}

// Delete removes key and reports whether it was there; if not, m is returned unchanged.
func (m pmap[V]) Delete(o *owner, key string) (pmap[V], bool) {
	root, found := del(o, m.root, key)
	if !found {
		return m, false
	}
	return pmap[V]{root: root, n: m.n - 1}, true
}

func del[V any](o *owner, t *pnode[V], key string) (*pnode[V], bool) {
	if t == nil {
		return nil, false
	}
	c := strings.Compare(key, t.key)
	if c == 0 {
		return merge(o, t.left, t.right), true
	}
	var sub *pnode[V]
	var found bool
	if c < 0 {
		sub, found = del(o, t.left, key)
	} else {
		sub, found = del(o, t.right, key)
	}
	if !found {
		return t, false
	}
	t = t.own(o)
	if c < 0 {
		t.left = sub
	} else {
		t.right = sub
	}
	return t, true
}

// merge joins a and b, every key of a being before every key of b.
func merge[V any](o *owner, a, b *pnode[V]) *pnode[V] {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case above(a, b):
		a = a.own(o)
		a.right = merge(o, a.right, b)
		return a
	default:
		b = b.own(o)
		b.left = merge(o, a, b.left)
		return b
	}
}

// Ascend calls fn for each key with prefix, in key order, until fn returns false.
func (m pmap[V]) Ascend(prefix string, fn func(key string, val V) bool) {
	ascend(m.root, prefix, fn)
}

// ascend returns false when the walk is over: fn returned false, or a key after the prefix was reached.
func ascend[V any](t *pnode[V], prefix string, fn func(string, V) bool) bool {
	for t != nil {
		if t.key < prefix {
			t = t.right
			continue
		}
		if !ascend(t.left, prefix, fn) || !strings.HasPrefix(t.key, prefix) || !fn(t.key, t.val) {
			return false
		}
		t = t.right
	}
	return true
}

// fromSorted builds the pmap of keys, which must be strictly ascending, and vals, in linear time. Its nodes belong
// to o. It is the pmap that putting the keys one by one makes.
func fromSorted[V any](o *owner, keys []string, vals []V) (pmap[V], error) {
	if len(keys) != len(vals) {
		panic(fmt.Sprintf("logdb: fromSorted with %d keys and %d values", len(keys), len(vals)))
	}
	var spine []*pnode[V] // the right spine of the tree so far, from the root
	for i, key := range keys {
		if i > 0 && key <= keys[i-1] {
			return pmap[V]{}, fmt.Errorf("keys out of order: %q after %q", key, keys[i-1])
		}
		t := &pnode[V]{key: key, val: vals[i], prio: priority(key), owner: o}
		for len(spine) > 0 && above(t, spine[len(spine)-1]) {
			t.left = spine[len(spine)-1]
			spine = spine[:len(spine)-1]
		}
		if len(spine) > 0 {
			spine[len(spine)-1].right = t
		}
		spine = append(spine, t)
	}
	if len(spine) == 0 {
		return pmap[V]{}, nil
	}
	return pmap[V]{root: spine[0], n: len(keys)}, nil
}
