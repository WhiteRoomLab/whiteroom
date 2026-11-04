package logdb

import (
	"fmt"
	"maps"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

// checkTree checks the order, the priorities and the size of m, and returns its keys in order.
func checkTree[V any](t *testing.T, m pmap[V]) []string {
	t.Helper()
	var keys []string
	var walk func(n *pnode[V])
	walk = func(n *pnode[V]) {
		if n == nil {
			return
		}
		if n.prio != priority(n.key) {
			t.Fatalf("key %q: priority %d, want %d", n.key, n.prio, priority(n.key))
		}
		if n.left != nil && above(n.left, n) || n.right != nil && above(n.right, n) {
			t.Fatalf("key %q is below a child", n.key)
		}
		walk(n.left)
		keys = append(keys, n.key)
		walk(n.right)
	}
	walk(m.root)
	for i := 1; i < len(keys); i++ {
		if keys[i-1] >= keys[i] {
			t.Fatalf("key %q before %q", keys[i-1], keys[i])
		}
	}
	if len(keys) != m.Len() {
		t.Fatalf("%d keys, Len %d", len(keys), m.Len())
	}
	return keys
}

func depth[V any](n *pnode[V]) int {
	if n == nil {
		return 0
	}
	return 1 + max(depth(n.left), depth(n.right))
}

func nodes[V any](n *pnode[V], set map[*pnode[V]]bool) map[*pnode[V]]bool {
	if n != nil {
		set[n] = true
		nodes(n.left, set)
		nodes(n.right, set)
	}
	return set
}

func checkEqual(t *testing.T, m pmap[int], want map[string]int) {
	t.Helper()
	if keys := checkTree(t, m); !slices.Equal(keys, slices.Sorted(maps.Keys(want))) {
		t.Fatalf("keys %q, want %q", keys, slices.Sorted(maps.Keys(want)))
	}
	for k, v := range want {
		if got, ok := m.Get(k); !ok || got != v {
			t.Fatalf("Get(%q) = %d, %v, want %d", k, got, ok, v)
		}
	}
	var got []string
	m.Ascend("", func(k string, v int) bool {
		if want[k] != v {
			t.Fatalf("Ascend: %q = %d, want %d", k, v, want[k])
		}
		got = append(got, k)
		return true
	})
	if len(got) != len(want) {
		t.Fatalf("Ascend gave %d keys, want %d", len(got), len(want))
	}
}

func TestPmapPersistent(t *testing.T) {
	rng := rand.New(rand.NewPCG(63, 2))
	type version struct {
		m    pmap[int]
		want map[string]int
	}
	var versions []version
	want := map[string]int{}
	var m pmap[int]
	var o *owner
	for i := range 100_000 {
		if i%1000 == 0 {
			versions = append(versions, version{m, maps.Clone(want)})
			o = new(owner)
			if i%5000 == 0 {
				o = nil
			}
		}
		k := fmt.Sprintf("k%04d", rng.IntN(3000))
		if rng.IntN(3) == 0 {
			var found bool
			m, found = m.Delete(o, k)
			if _, ok := want[k]; found != ok {
				t.Fatalf("op %d: Delete(%q) found %v, want %v", i, k, found, ok)
			}
			delete(want, k)
		} else {
			v := rng.Int()
			m = m.Put(o, k, v)
			want[k] = v
		}
		if _, ok := m.Get("missing"); ok {
			t.Fatal("Get of a missing key")
		}
	}
	versions = append(versions, version{m, want})
	for i, v := range versions {
		t.Run(fmt.Sprint("version ", i), func(t *testing.T) { checkEqual(t, v.m, v.want) })
	}
	if n := len(versions[len(versions)-1].want); n < 1000 {
		t.Errorf("only %d keys at the end", n)
	}
}

func TestPmapOwner(t *testing.T) {
	o1, o2 := new(owner), new(owner)
	if o1 == o2 {
		t.Fatal("owners are equal")
	}
	var m1 pmap[string]
	for _, k := range []string{"a", "b", "c", "d", "e", "f"} {
		m1 = m1.Put(o1, k, "1")
	}
	before := nodes(m1.root, map[*pnode[string]]bool{})

	// Changes of the owner of the nodes change them in place.
	m2 := m1.Put(o1, "b", "2")
	if v, _ := m1.Get("b"); v != "2" {
		t.Errorf("a put of the owner copied: the version before it has %q", v)
	}
	if after := nodes(m2.root, map[*pnode[string]]bool{}); !maps.Equal(after, before) {
		t.Errorf("a put of the owner made %d nodes, had %d", len(after), len(before))
	}
	m2 = m2.Put(o1, "g", "2")
	if after := nodes(m2.root, map[*pnode[string]]bool{}); len(after) != len(before)+1 {
		t.Errorf("an insert of the owner made %d nodes, had %d", len(after), len(before))
	}
	m2, _ = m2.Delete(o1, "a")
	checkStrings := func(m pmap[string], want map[string]string) {
		t.Helper()
		if keys := checkTree(t, m); !slices.Equal(keys, slices.Sorted(maps.Keys(want))) {
			t.Fatalf("keys %q, want %q", keys, slices.Sorted(maps.Keys(want)))
		}
		for k, v := range want {
			if got, _ := m.Get(k); got != v {
				t.Fatalf("%q = %q, want %q", k, got, v)
			}
		}
	}
	want := map[string]string{"b": "2", "c": "1", "d": "1", "e": "1", "f": "1", "g": "2"}
	checkStrings(m2, want)

	// Another owner, or none, never changes the nodes of a version it did not make.
	snapshot := nodes(m2.root, map[*pnode[string]]bool{})
	for _, o := range []*owner{o2, nil} {
		m3 := m2.Put(o, "b", "3").Put(o, "h", "3").Put(o, "c", "3")
		m3, _ = m3.Delete(o, "d")
		m3, _ = m3.Delete(o, "e")
		m3 = m3.Put(o, "b", "4")
		checkStrings(m2, want)
		checkStrings(m3, map[string]string{"b": "4", "c": "3", "f": "1", "g": "2", "h": "3"})
		for n := range snapshot {
			if n.owner != o1 {
				t.Fatalf("node %q of o1 now belongs to %p", n.key, n.owner)
			}
		}
		for n := range nodes(m3.root, map[*pnode[string]]bool{}) {
			if !snapshot[n] && n.owner != o {
				t.Fatalf("new node %q belongs to %p, want %p", n.key, n.owner, o)
			}
		}
	}

	// A nil owner copies its own nodes too.
	m4 := pmap[string]{}.Put(nil, "a", "1")
	m5 := m4.Put(nil, "a", "2")
	if v, _ := m4.Get("a"); v != "1" || m4.root == m5.root {
		t.Errorf("a nil owner changed a node in place: %q", v)
	}
}

func TestPmapDelete(t *testing.T) {
	var empty pmap[int]
	if m, found := empty.Delete(nil, "a"); found || m.root != nil || m.Len() != 0 {
		t.Errorf("Delete in an empty map: %v, %+v", found, m)
	}
	if _, ok := empty.Get(""); ok {
		t.Error("Get in an empty map")
	}
	o := new(owner)
	var m pmap[int]
	for i := range 100 {
		m = m.Put(o, fmt.Sprint(i), i)
	}
	frozen := m
	if got, found := frozen.Delete(new(owner), "missing"); found || got != frozen {
		t.Errorf("Delete of a missing key changed the map: %v", found)
	}
	o = new(owner)
	for i := range 100 {
		var found bool
		m, found = m.Delete(o, fmt.Sprint(i))
		if !found || m.Len() != 99-i {
			t.Fatalf("Delete %d: %v, Len %d", i, found, m.Len())
		}
		checkTree(t, m)
	}
	if m.root != nil {
		t.Error("nodes after deleting every key")
	}
	if frozen.Len() != 100 || len(checkTree(t, frozen)) != 100 {
		t.Error("deletes of another owner changed the map")
	}
}

func TestPmapAscend(t *testing.T) {
	keys := []string{"", "a", "ab", "abc", "abd", "b", "ba", "c", "c\x00", "c\xff"}
	vals := make([]int, len(keys))
	for i := range vals {
		vals[i] = i
	}
	sorted, err := fromSorted(nil, keys, vals)
	if err != nil {
		t.Fatal(err)
	}
	var put pmap[int]
	for _, i := range rand.New(rand.NewPCG(63, 3)).Perm(len(keys)) {
		put = put.Put(nil, keys[i], i)
	}
	for _, m := range []pmap[int]{sorted, put} {
		for prefix, want := range map[string][]string{
			"":      keys,
			"a":     {"a", "ab", "abc", "abd"},
			"ab":    {"ab", "abc", "abd"},
			"abc":   {"abc"},
			"abe":   nil,
			"b":     {"b", "ba"},
			"c":     {"c", "c\x00", "c\xff"},
			"c\x00": {"c\x00"},
			"d":     nil,
			"\x00":  nil,
		} {
			var got []string
			m.Ascend(prefix, func(k string, v int) bool {
				if keys[v] != k {
					t.Fatalf("key %q with value %d", k, v)
				}
				got = append(got, k)
				return true
			})
			if !slices.Equal(got, want) {
				t.Errorf("prefix %q: %q, want %q", prefix, got, want)
			}
		}
		for stop := range 4 {
			var got []string
			m.Ascend("a", func(k string, _ int) bool {
				got = append(got, k)
				return len(got) <= stop
			})
			if want := []string{"a", "ab", "abc", "abd"}[:stop+1]; !slices.Equal(got, want) {
				t.Errorf("stop after %d: %q, want %q", stop+1, got, want)
			}
		}
	}

	var big pmap[int]
	for i := range 10_000 {
		big = big.Put(nil, fmt.Sprintf("p%05d", i), i)
	}
	n := 0
	big.Ascend("p012", func(k string, v int) bool {
		if k != fmt.Sprintf("p%05d", 1200+n) || v != 1200+n {
			t.Fatalf("key %q, value %d after %d keys", k, v, n)
		}
		n++
		return true
	})
	if n != 100 {
		t.Errorf("%d keys with prefix p012, want 100", n)
	}
}

func TestFromSorted(t *testing.T) {
	if m, err := fromSorted[int](nil, nil, nil); err != nil || m.root != nil || m.Len() != 0 {
		t.Errorf("no keys: %+v, %v", m, err)
	}
	for _, keys := range [][]string{{"b", "a"}, {"a", "a"}, {"a", "c", "b"}} {
		if _, err := fromSorted(nil, keys, make([]int, len(keys))); err == nil ||
			err.Error() != fmt.Sprintf("keys out of order: %q after %q", keys[len(keys)-1], keys[len(keys)-2]) {
			t.Errorf("%q: %v", keys, err)
		}
	}
	func() {
		defer func() {
			if r := recover(); r != "logdb: fromSorted with 2 keys and 1 values" {
				t.Errorf("lengths differ: %v", r)
			}
		}()
		_, _ = fromSorted(nil, []string{"a", "b"}, []int{1})
	}()

	const n = 100_000
	keys := make([]string, n)
	vals := make([]int, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("key %08d", i*7)
		vals[i] = i
	}
	o := new(owner)
	sorted, err := fromSorted(o, keys, vals)
	if err != nil {
		t.Fatal(err)
	}
	checkTree(t, sorted)
	var put pmap[int]
	po := new(owner)
	for _, i := range rand.New(rand.NewPCG(63, 4)).Perm(n) {
		put = put.Put(po, keys[i], i)
	}
	var same func(a, b *pnode[int]) bool
	same = func(a, b *pnode[int]) bool {
		if a == nil || b == nil {
			return a == b
		}
		return a.key == b.key && a.val == b.val && same(a.left, b.left) && same(a.right, b.right)
	}
	if !same(sorted.root, put.root) || sorted.Len() != put.Len() {
		t.Fatal("fromSorted made another tree than Put")
	}
	limit := int(4*math.Log2(n) + 8)
	if d := depth(sorted.root); d > limit {
		t.Errorf("depth %d for %d keys, want at most %d", d, n, limit)
	}

	// The nodes belong to the owner given.
	if sorted.root.owner != o {
		t.Fatal("the root does not belong to the owner")
	}
	root := sorted.root
	if sorted = sorted.Put(o, root.key, -1); sorted.root != root {
		t.Error("a put of the owner copied the root")
	}
	if other := sorted.Put(new(owner), root.key, -2); other.root == root || root.val != -1 {
		t.Error("a put of another owner changed the root")
	}
}
