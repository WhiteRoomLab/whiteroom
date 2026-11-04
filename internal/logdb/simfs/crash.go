package simfs

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"path"
	"slices"
	"strings"
)

// PendingKind is the kind of a Pending change.
type PendingKind int

const (
	PendingWrite PendingKind = iota
	PendingTruncate
	PendingEntry // a name made, removed or renamed in a directory
)

var pendingKinds = [...]string{PendingWrite: "write", PendingTruncate: "truncate", PendingEntry: "entry"}

func (k PendingKind) String() string {
	if k >= 0 && int(k) < len(pendingKinds) {
		return pendingKinds[k]
	}
	return fmt.Sprintf("PendingKind(%d)", int(k))
}

// Pending is a change that is not durable yet, which a crash may keep or lose. A write is a change for each 4096-byte
// page of the file that it writes in; a rename in one directory is one change, and a rename across directories two:
// the new name, and after it the removal of the old name.
type Pending struct {
	Kind PendingKind
	Path string // of the file or the name, from the root of New, when the change was made
	Off  int64  // PendingWrite: the offset of the bytes; PendingTruncate: the size
	Len  int    // PendingWrite: the number of bytes
}

func (p Pending) String() string {
	switch p.Kind {
	case PendingWrite:
		return fmt.Sprintf("write %s [%d,%d)", p.Path, p.Off, p.Off+int64(p.Len))
	case PendingTruncate:
		return fmt.Sprintf("truncate %s to %d", p.Path, p.Off)
	case PendingEntry:
		return "entry " + p.Path
	}
	return fmt.Sprintf("%v %s", p.Kind, p.Path)
}

const pageSize = 4096

// unit is a change in world.units, which has them in the order they were made: pending ones, and durable ones that
// come after a change of the same data or name that is still in world.units. The others are in the base state.
type unit struct {
	kind    PendingKind
	durable bool
	path    string
	n       *inode // the file of a write or a truncate, the directory of an entry
	off     int64
	data    []byte
	sets    []entrySet
	ins     *unit // of the removal of the old name of a rename across directories: its new name
}

// entrySet sets name in dir to node, or removes it if node is nil.
type entrySet struct {
	dir  *inode
	name string
	node *inode
}

func (w *world) write(n *inode, path string, off int64, p []byte) {
	n.data = (&unit{kind: PendingWrite, off: off, data: p}).apply(n.data, 0, false)
	for len(p) > 0 {
		k := min(int64(len(p)), pageSize-off%pageSize)
		w.units = append(w.units, &unit{kind: PendingWrite, path: path, n: n, off: off, data: slices.Clone(p[:k])})
		off += k
		p = p[k:]
	}
}

func (w *world) truncate(n *inode, path string, size int64) {
	n.data = resize(n.data, size)
	w.units = append(w.units, &unit{kind: PendingTruncate, path: path, n: n, off: size})
}

func (w *world) addEntry(path string, dir *inode, name string, node *inode) {
	w.units = append(w.units, &unit{kind: PendingEntry, path: path, n: dir,
		sets: []entrySet{{dir: dir, name: name, node: node}}})
}

// synced reports whether a sync of n makes u durable: a change of the data of the file n, or of an entry of the
// directory n, but not the removal of an old name whose new name is not durable yet, so that a rename by itself never
// makes a crash lose the file.
func (u *unit) synced(n *inode) bool {
	return !u.durable && u.n == n && (u.ins == nil || u.ins.durable)
}

func (w *world) sync(n *inode) {
	for _, u := range w.units {
		if u.synced(n) {
			u.durable = true
		}
	}
	w.fold()
}

// drop removes the changes that a sync of n would make durable, which then never become durable, and the removals of
// old names whose new names it removes.
func (w *world) drop(n *inode) {
	gone := map[*unit]bool{}
	for _, u := range w.units {
		if u.synced(n) || u.ins != nil && gone[u.ins] {
			gone[u] = true
		}
	}
	w.units = slices.DeleteFunc(w.units, func(u *unit) bool { return gone[u] })
	w.fold()
}

// place is the data of a file (name "") or a name in a directory, which the changes of one place must be applied to in
// the order they were made.
type place struct {
	n    *inode
	name string
}

func (u *unit) places() []place {
	if u.kind != PendingEntry {
		return []place{{n: u.n}}
	}
	places := make([]place, len(u.sets))
	for i, s := range u.sets {
		places[i] = place{n: s.dir, name: s.name}
	}
	return places
}

// fold applies to the base state, and takes out of world.units, each durable unit whose places no unit before it
// changes.
func (w *world) fold() {
	blocked := map[place]bool{}
	kept := w.units[:0]
	for _, u := range w.units {
		places := u.places()
		if u.durable && !slices.ContainsFunc(places, func(p place) bool { return blocked[p] }) {
			u.applyBase()
			continue
		}
		for _, p := range places {
			blocked[p] = true
		}
		kept = append(kept, u)
	}
	clear(w.units[len(kept):])
	w.units = kept
}

func (u *unit) applyBase() {
	if u.kind != PendingEntry {
		u.n.base = u.apply(u.n.base, 0, false)
		return
	}
	for _, s := range u.sets {
		if s.node == nil {
			delete(s.dir.baseEntries, s.name)
		} else {
			s.dir.baseEntries[s.name] = s.node
		}
	}
}

// apply applies the write or truncate u to b. A write keeps only its first torn bytes if torn is in (0, its length),
// and if zero is true, it makes b larger as it would, with zeros, but leaves the old bytes of b as they are.
func (u *unit) apply(b []byte, torn int, zero bool) []byte {
	if u.kind == PendingTruncate {
		return resize(b, u.off)
	}
	data := u.data
	if torn > 0 && torn < len(data) {
		data = data[:torn]
	}
	end := u.off + int64(len(data))
	if end > int64(len(b)) {
		b = resize(b, end)
	}
	if !zero {
		copy(b[u.off:end], data)
	}
	return b
}

// resize returns b with size bytes, the new ones zeros.
func resize(b []byte, size int64) []byte {
	n := int(size)
	if n <= len(b) {
		return b[:n]
	}
	old := len(b)
	b = slices.Grow(b, n-old)[:n]
	clear(b[old:])
	return b
}

// Pending returns the changes that are not durable, in the order they were made, which Model.Keep chooses from.
func (s *FS) Pending() []Pending {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	var pending []Pending
	for _, u := range s.w.units {
		if !u.durable {
			pending = append(pending, Pending{Kind: u.kind, Path: u.path, Off: u.off, Len: len(u.data)})
		}
	}
	return pending
}

// Model is a crash: which pending changes it keeps, and what happens to the last write that it keeps.
type Model struct {
	Name     string
	Keep     []bool // by the index in Pending; nil keeps none
	TornLast int    // if > 0: the last write kept has only its first TornLast bytes
	ZeroLast bool   // the last write kept makes the file larger as it would, with zeros, and changes no old byte
}

// Crash returns the root of a new file system that holds what the crash m can leave: the durable state, with the
// pending changes that m keeps applied in the order they were made, except a removal of an old name whose new name is
// neither durable nor kept. Every change in it is durable, and it has no locks, faults or numbered operations. s is
// not changed. Crash panics if m.Keep is not nil and has not an entry for each pending change.
func (s *FS) Crash(m Model) *FS {
	w := s.w
	w.mu.Lock()
	defer w.mu.Unlock()
	var pending []*unit
	for _, u := range w.units {
		if !u.durable {
			pending = append(pending, u)
		}
	}
	if m.Keep != nil && len(m.Keep) != len(pending) {
		panic(fmt.Sprintf("simfs: the model %q keeps from %d changes, but %d are pending", m.Name, len(m.Keep), len(pending)))
	}
	if m.TornLast < 0 {
		panic(fmt.Sprintf("simfs: the model %q has a negative TornLast", m.Name))
	}
	keep := map[*unit]bool{}
	var last *unit
	for i, u := range pending {
		if m.Keep != nil && m.Keep[i] {
			keep[u] = true
			if u.kind == PendingWrite {
				last = u
			}
		}
	}
	c := copier{}
	root := c.of(w.root)
	applied := map[*unit]bool{}
	for _, u := range w.units {
		if !u.durable && !keep[u] || u.ins != nil && !u.ins.durable && !applied[u.ins] {
			continue
		}
		applied[u] = true
		switch u.kind {
		case PendingWrite, PendingTruncate:
			n := c.of(u.n)
			if u == last {
				n.data = u.apply(n.data, m.TornLast, m.ZeroLast)
			} else {
				n.data = u.apply(n.data, 0, false)
			}
		case PendingEntry:
			for _, set := range u.sets {
				d := c.of(set.dir)
				if set.node == nil {
					delete(d.entries, set.name)
				} else {
					d.entries[set.name] = c.of(set.node)
				}
			}
		}
	}
	settle(root)
	return &FS{w: &world{name: w.name, root: root}, dir: root, name: w.name, prefix: "."}
}

// copier copies the inodes of a world with their durable state, each once.
type copier map[*inode]*inode

func (c copier) of(n *inode) *inode {
	if m := c[n]; m != nil {
		return m
	}
	m := &inode{dir: n.dir, perm: n.perm, data: slices.Clone(n.base)}
	c[n] = m
	if n.dir {
		m.entries = make(map[string]*inode, len(n.baseEntries))
		for name, child := range n.baseEntries {
			m.entries[name] = c.of(child)
		}
	}
	return m
}

// settle makes the tree of root, the root of a crashed world, durable as it is, with one name for each directory.
func settle(root *inode) {
	seen := map[*inode]bool{root: true}
	var walk func(d *inode)
	walk = func(d *inode) {
		for _, name := range slices.Sorted(maps.Keys(d.entries)) {
			n := d.entries[name]
			switch {
			case n.dir && seen[n]:
				delete(d.entries, name)
			case n.dir:
				seen[n] = true
				n.parent = d
				walk(n)
			default:
				n.base = slices.Clone(n.data)
			}
		}
		d.baseEntries = maps.Clone(d.entries)
	}
	walk(root)
}

// Models returns crash models for the pending changes p: "lose-all", "keep-all", each prefix "prefix-<k>" (the first
// k changes), the variants of a prefix that ends in a write with that write torn after 1 byte, half of it and all but
// 1 byte ("prefix-<k>-torn-<bytes>") or with zeros ("prefix-<k>-zero"), and subsets named "subset-" with a 1 for each
// change kept and else a 0: all of them if the subsets that are not prefixes are at most maxSubsets, or else
// maxSubsets of them chosen with rng (nil: a fixed seed), which include "only the last" and "all but the first". The
// names are unique and printable, and the same arguments give the same models.
func Models(p []Pending, maxSubsets int, rng *rand.Rand) []Model {
	n := len(p)
	prefix := func(k int) []bool {
		keep := make([]bool, n)
		for i := range k {
			keep[i] = true
		}
		return keep
	}
	models := []Model{{Name: "lose-all"}, {Name: "keep-all", Keep: prefix(n)}}
	seen := map[string]bool{bits(prefix(0)): true, bits(prefix(n)): true}
	for k := 1; k < n; k++ {
		keep := prefix(k)
		seen[bits(keep)] = true
		models = append(models, Model{Name: fmt.Sprintf("prefix-%d", k), Keep: keep})
	}
	for k := 1; k <= n; k++ {
		if p[k-1].Kind != PendingWrite {
			continue
		}
		size := p[k-1].Len
		var torn []int
		for _, t := range []int{1, size / 2, size - 1} {
			if t > 0 && t < size && !slices.Contains(torn, t) {
				torn = append(torn, t)
			}
		}
		for _, t := range torn {
			models = append(models, Model{Name: fmt.Sprintf("prefix-%d-torn-%d", k, t), Keep: prefix(k), TornLast: t})
		}
		models = append(models, Model{Name: fmt.Sprintf("prefix-%d-zero", k), Keep: prefix(k), ZeroLast: true})
	}
	subsets := 0
	subset := func(keep []bool) {
		if key := bits(keep); !seen[key] {
			seen[key] = true
			models = append(models, Model{Name: "subset-" + key, Keep: keep})
			subsets++
		}
	}
	if maxSubsets <= 0 {
		return models
	}
	if n < 63 && uint64(1)<<n-uint64(n+1) <= uint64(maxSubsets) {
		for mask := range uint64(1) << n {
			keep := make([]bool, n)
			for i := range keep {
				keep[i] = mask>>i&1 == 1
			}
			subset(keep)
		}
		return models
	}
	onlyLast, allButFirst := make([]bool, n), prefix(n)
	onlyLast[n-1], allButFirst[0] = true, false
	for _, keep := range [][]bool{onlyLast, allButFirst} {
		if subsets < maxSubsets {
			subset(keep)
		}
	}
	if rng == nil {
		rng = rand.New(rand.NewPCG(1, 2))
	}
	for tries := 100*maxSubsets + 1000; subsets < maxSubsets && tries > 0; tries-- {
		keep := make([]bool, n)
		for i := range keep {
			keep[i] = rng.IntN(2) == 1
		}
		subset(keep)
	}
	return models
}

func bits(keep []bool) string {
	var b strings.Builder
	for _, k := range keep {
		if k {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
	}
	return b.String()
}

// Walk calls fn for each file that has a name, in the order of the paths from the root of New, with what reads of it
// see. fn may use the file system: data is a copy.
func (s *FS) Walk(fn func(path string, data []byte)) {
	type found struct {
		path string
		data []byte
	}
	var files []found
	s.w.mu.Lock()
	seen := map[*inode]bool{}
	var walk func(d *inode, dir string)
	walk = func(d *inode, dir string) {
		seen[d] = true
		for name, n := range d.entries {
			p := path.Join(dir, name)
			switch {
			case !n.dir:
				files = append(files, found{path: p, data: slices.Clone(n.data)})
			case !seen[n]:
				walk(n, p)
			}
		}
	}
	walk(s.w.root, "")
	s.w.mu.Unlock()
	slices.SortFunc(files, func(a, b found) int { return strings.Compare(a.path, b.path) })
	for _, f := range files {
		fn(f.path, f.data)
	}
}
