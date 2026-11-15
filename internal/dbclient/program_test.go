package dbclient_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
)

// The programs of TestDifferential: lists of steps, each a View or an Update that does a list of calls, made from a seed.
// A program is data, so that the same one runs on a client and on an engine.

type op int

const (
	opGet op = iota
	opPut
	opDelete
	opScan
	opPutObject
	opObject
	opEvent
	opSeq
	opEvents
)

var opNames = [...]string{"Get", "Put", "Delete", "Scan", "PutObject", "Object", "AppendEvent", "Seq", "Events"}

func (o op) String() string { return opNames[o] }

// rel says which key an action of a Scan uses, from the key that the Scan is at.
type rel int

const (
	relAt rel = iota
	relAfter
	relAfterZ
	relBefore
	relOther
)

type action struct {
	op     op
	table  uint32
	rel    rel
	key    []byte // for relOther
	value  []byte
	prefix []byte // of a Scan that fn starts
}

func (a action) keyAt(cur []byte) []byte {
	switch a.rel {
	case relAt:
		return cur
	case relAfter:
		return append(slices.Clone(cur), 0)
	case relAfterZ:
		return append(slices.Clone(cur), 'z')
	case relBefore:
		if len(cur) == 0 {
			return nil
		}
		return slices.Clone(cur[:len(cur)-1])
	case relOther:
	}
	return a.key
}

// scanScript is what the function of a Scan does: the actions at each row, and the row after which it stops.
type scanScript struct {
	stopAt int // 0: never
	rows   [][]action
}

type call struct {
	op     op
	table  uint32
	key    []byte // the prefix of a Scan
	value  []byte
	object int // of a PutObject and an Object: the index in the objects of the program, or -1 for one that nothing put
	script *scanScript
}

type step struct {
	update bool
	abort  bool // the Update ends in an error
	event  bool // the Update ends with an event
	kind   string
	calls  []call
}

type program struct {
	seed    int64
	frame   int
	objects [][]byte
	steps   []step
}

// The keys of the programs: a few, with prefixes of each other and bytes that sort first and last, so that hits, misses
// and neighbors are common.
var (
	keyPool = []string{"", "a", "b", "c", "aa", "ab", "ac", "ba", "bb", "bc", "ca", "cb", "a\x00", "a\xff", "b0", "b1", "b2", "c0", "c1",
		"m", "mm", "z", "d0", "d1", "d2", "d3", "d4", "d5", "e", "ee", "f0", "f1", "f2", "g"}
	prefixes = []string{"", "", "", "a", "b", "c", "ab", "b1", "m", "z", "a\x00", "\xff", "zz", "x", "d", "f"}
	heads    = []string{"main", "audit.1", "audit_2", "A-b", "h0", "h1", "h2", "h3", "h4", "h5"}
	tables   = []uint32{2, 2, 2, 2, 2, 6, 6, 6, 1, 1, 3, 4, 5, 7, 8, 9, 10, 11, 12}
)

type generator struct {
	rng     *rand.Rand
	fresh   int // the fresh keys of the tables that only grow, so far
	missing int
	prog    *program
}

// genProgram makes the program of a seed. The calls follow the rules of the daemon, so that an engine and a daemon commit
// the same: no change of the schema row, no change or delete of a row of an append-only table, only valid rows in the
// heads, an event in each Update that writes.
func genProgram(seed int64, steps int) *program {
	g := &generator{rng: rand.New(rand.NewPCG(uint64(seed), 0x5eed)), prog: &program{seed: seed}}
	// Half of the daemons have small frames, which make pages of a few rows.
	g.prog.frame = []int{1 << 20, testFrame, 1 << 20, 1024}[seed%4]
	for _, size := range []int{0, 1, 7, 49, 50, 51, 200, 1000, 70_000, 200_000} {
		g.prog.objects = append(g.prog.objects, g.bytes(size))
	}
	for range steps {
		g.prog.steps = append(g.prog.steps, g.step())
	}
	return g.prog
}

func (g *generator) bytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(g.rng.IntN(256))
	}
	return b
}

func (g *generator) chance(percent int) bool { return g.rng.IntN(100) < percent }

func (g *generator) table() uint32 { return tables[g.rng.IntN(len(tables))] }

func (g *generator) poolKey() []byte { return []byte(keyPool[g.rng.IntN(len(keyPool))]) }

func (g *generator) freshKey() []byte {
	g.fresh++
	return fmt.Appendf(nil, "%c%04d", 'a'+g.rng.IntN(3), g.fresh)
}

// readKey returns a key that a Get may find: those of the pool, and in the tables that only grow, the fresh keys.
func (g *generator) readKey(table uint32) []byte {
	switch table {
	case 3, 4, 5:
		return fmt.Appendf(nil, "%c%04d", 'a'+g.rng.IntN(3), g.rng.IntN(g.fresh+2))
	case 11:
		return []byte(heads[g.rng.IntN(len(heads))])
	}
	return g.poolKey()
}

func (g *generator) missingKey() []byte {
	g.missing++
	return fmt.Appendf(nil, "zz-missing-%d", g.missing)
}

func (g *generator) value(table uint32) []byte {
	if table == 11 {
		b := make([]byte, 1+g.rng.IntN(40))
		for i := range b {
			b[i] = byte(0x20 + g.rng.IntN(0x5f))
		}
		return b
	}
	switch r := g.rng.IntN(100); {
	case r < 10:
		return nil
	case r < 45:
		return g.bytes(1 + g.rng.IntN(16))
	case r < 70:
		return g.bytes(80 + g.rng.IntN(300))
	case r < 92:
		return g.bytes(600 + g.rng.IntN(900))
	}
	return g.bytes(3000 + g.rng.IntN(4000))
}

func (g *generator) eventData() []byte {
	g.missing++
	return fmt.Appendf(nil, `{"e":%d,"s":%d}`, g.missing, g.prog.seed)
}

// write returns a call that writes: a Put, or a Delete of a key that the table does not keep, which the rules of the
// daemon let through.
func (g *generator) write() call {
	table := g.table()
	if g.chance(30) {
		if table == 3 || table == 4 || table == 5 || table == 11 || g.chance(30) {
			return call{op: opDelete, table: table, key: g.missingKey()}
		}
		return call{op: opDelete, table: table, key: g.readKey(table)}
	}
	switch table {
	case 3, 4, 5:
		return call{op: opPut, table: table, key: g.freshKey(), value: g.value(table)}
	case 11:
		return call{op: opPut, table: table, key: []byte(heads[g.rng.IntN(len(heads))]), value: g.value(table)}
	}
	return call{op: opPut, table: table, key: g.poolKey(), value: g.value(table)}
}

func (g *generator) action(table uint32) action {
	other := g.chance(35)
	if other {
		table = g.table()
	}
	r := g.rng.IntN(100)
	a := action{table: table, rel: rel(g.rng.IntN(int(relOther) + 1))}
	if a.rel == relOther {
		a.key = g.readKey(table)
	}
	switch {
	case r < 35:
		a.op, a.value = opPut, g.value(table)
		if table == 11 {
			a.rel, a.key = relOther, []byte(heads[g.rng.IntN(len(heads))])
		}
	case r < 60:
		a.op = opDelete
		if table == 11 {
			a.rel, a.key = relOther, g.missingKey()
		}
	case r < 85:
		a.op = opGet
	default:
		a.op, a.prefix = opScan, []byte(prefixes[g.rng.IntN(len(prefixes))])
	}
	return a
}

func (g *generator) script(table uint32, writes bool) *scanScript {
	s := &scanScript{}
	if g.chance(30) {
		s.stopAt = 1 + g.rng.IntN(30)
	}
	for range 40 {
		var acts []action
		if g.chance(50) {
			for range 1 + g.rng.IntN(3) {
				a := g.action(table)
				if !writes && (a.op == opPut || a.op == opDelete) {
					a.op = opGet
				}
				acts = append(acts, a)
			}
		}
		s.rows = append(s.rows, acts)
	}
	return s
}

func (g *generator) scan(writes bool) call {
	table := g.table()
	return call{op: opScan, table: table, key: []byte(prefixes[g.rng.IntN(len(prefixes))]), script: g.script(table, writes)}
}

func (g *generator) object() int {
	if g.chance(15) {
		return -1
	}
	// Mostly the small ones.
	if g.chance(85) {
		return g.rng.IntN(7)
	}
	return g.rng.IntN(len(g.prog.objects))
}

// read returns a call that only reads.
func (g *generator) read() call {
	switch r := g.rng.IntN(100); {
	case r < 45:
		table := g.table()
		return call{op: opGet, table: table, key: g.readKey(table)}
	case r < 75:
		return g.scan(false)
	case r < 85:
		return call{op: opObject, object: g.object()}
	case r < 92:
		return call{op: opSeq}
	case r < 96:
		return call{op: opEvents}
	}
	return g.invalid()
}

// invalid returns a call that the engine and the client both refuse, or that finds nothing: table 0, a key that is too long.
func (g *generator) invalid() call {
	long := bytes.Repeat([]byte("k"), 4097)
	switch g.rng.IntN(6) {
	case 0:
		return call{op: opPut, table: 0, key: g.poolKey(), value: []byte("v")}
	case 1:
		return call{op: opDelete, table: 0, key: g.poolKey()}
	case 2:
		return call{op: opGet, table: 0, key: g.poolKey()}
	case 3:
		return call{op: opPut, table: 2, key: long, value: []byte("v")}
	case 4:
		return call{op: opGet, table: 2, key: long}
	}
	return call{op: opScan, table: 2, key: long, script: &scanScript{}}
}

func (g *generator) updateCall() call {
	switch r := g.rng.IntN(100); {
	case r < 40:
		return g.write()
	case r < 60:
		return g.read()
	case r < 72:
		return g.scan(true)
	case r < 77:
		return call{op: opPutObject, object: g.rng.IntN(len(g.prog.objects))}
	case r < 82:
		return call{op: opEvent, value: g.eventData()}
	}
	return g.read()
}

func (g *generator) step() step {
	switch r := g.rng.IntN(100); {
	case r < 25: // a View, with a few writes that it refuses
		st := step{kind: "view"}
		for range 1 + g.rng.IntN(5) {
			if g.chance(8) {
				st.calls = append(st.calls, g.write())
			} else {
				st.calls = append(st.calls, g.read())
			}
		}
		return st
	case r < 31: // an Update with no op: it commits nothing, and needs no event
		st := step{update: true, kind: "no op"}
		for range 1 + g.rng.IntN(4) {
			switch g.rng.IntN(3) {
			case 0:
				st.calls = append(st.calls, call{op: opDelete, table: g.table(), key: g.missingKey()})
			case 1:
				st.calls = append(st.calls, call{op: opSeq})
			default:
				st.calls = append(st.calls, g.read())
			}
		}
		return st
	case r < 44: // an Update that fills a table, so that it takes more than a page
		st := step{update: true, kind: "fills", event: true, abort: g.chance(25)}
		table := []uint32{1, 2, 2, 6, 6, 7, 8, 9, 10, 12}[g.rng.IntN(10)]
		for _, k := range keyPool {
			if g.chance(70) {
				st.calls = append(st.calls, call{op: opPut, table: table, key: []byte(k), value: g.value(table)})
			}
		}
		st.calls = append(st.calls, call{op: opScan, table: table, key: nil, script: g.script(table, true)})
		return st
	case r < 49: // an Update that reads
		st := step{update: true, kind: "reads"}
		for range 1 + g.rng.IntN(4) {
			st.calls = append(st.calls, g.read())
		}
		return st
	}
	st := step{update: true, kind: "writes", event: true, abort: g.chance(25)}
	for range 1 + g.rng.IntN(12) {
		st.calls = append(st.calls, g.updateCall())
	}
	return st
}

// describe says what a step is, for a message.
func (s step) describe() string {
	kind := "View"
	if s.update {
		kind = "Update"
	}
	return fmt.Sprintf("%s (%s, %d calls, abort %v)", kind, s.kind, len(s.calls), s.abort)
}

func hashOfObject(data []byte) [32]byte { return sha256.Sum256(data) }

// sum describes a list of strings in a line.
func sum(parts []string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return fmt.Sprintf("%d#%x", len(parts), h[:4])
}
