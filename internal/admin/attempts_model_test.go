package admin

import (
	"fmt"
	"math"
	"math/rand/v2"
	"net/netip"
	"slices"
	"testing"
	"time"
)

// model is a plain version of attempts, with maps that it searches, for the tests to compare attempts with. It keeps
// only the entries that have not expired, and the level of a login name and of a client as whole units that come back
// from the time of the entry.
type model struct {
	capacity  int
	now       time.Duration
	seq       uint64
	pairs     map[[2]int]*modelPair
	names     map[int]*modelLevel
	clients   map[int]*modelLevel
	nameKey   [][32]byte         // the key of each id of a login name
	clientKey []netip.Prefix     // the key of each id of a client
	idOf      map[pairKey][2]int // the ids of the pairs that the test uses
}

type modelPair struct {
	count int
	last  time.Duration
	use   uint64
}

// modelLevel is a count of attempts, level, of which one comes back every leak after at.
type modelLevel struct {
	level int
	at    time.Duration
}

func newModel(capacity, names, clients int) *model {
	m := &model{
		capacity: capacity, pairs: map[[2]int]*modelPair{}, names: map[int]*modelLevel{}, clients: map[int]*modelLevel{},
		idOf: map[pairKey][2]int{},
	}
	for id := range names {
		m.nameKey = append(m.nameKey, testName(id))
	}
	for id := range clients {
		m.clientKey = append(m.clientKey, testClient(id))
	}
	for n := range names {
		for c := range clients {
			m.idOf[newPairKey(m.nameKey[n], m.clientKey[c])] = [2]int{n, c}
		}
	}
	return m
}

func modelWait(count int) time.Duration {
	if count < 5 {
		return 0
	}
	return min(30*time.Second<<min(count-5, 7), time.Hour)
}

func wholeSeconds(d time.Duration) time.Duration {
	return time.Duration(math.Ceil(float64(d)/float64(time.Second))) * time.Second
}

// settle drops the attempts that have come back, and returns whether any are left.
func (l *modelLevel) settle(now, leak time.Duration) bool {
	back := int((now - l.at) / leak)
	if back >= l.level {
		return false
	}
	l.level -= back
	l.at += time.Duration(back) * leak
	return true
}

// advance moves the time, and drops what has expired.
func (m *model) advance(d time.Duration) {
	m.now += d
	for id, p := range m.pairs {
		if m.now-p.last >= 24*time.Hour {
			delete(m.pairs, id)
		}
	}
	for id, l := range m.names {
		if !l.settle(m.now, 15*time.Minute) {
			delete(m.names, id)
		}
	}
	for id, l := range m.clients {
		if !l.settle(m.now, 30*time.Second) {
			delete(m.clients, id)
		}
	}
}

// leastRecentlyUsed returns the pair that may be dropped and was used least recently, or false.
func (m *model) leastRecentlyUsed() ([2]int, bool) {
	var best [2]int
	found := false
	for id, p := range m.pairs {
		if p.count < 5 && (!found || p.use < m.pairs[best].use) {
			best, found = id, true
		}
	}
	return best, found
}

// wait returns how long an attempt waits for the level l, which is nil if there is none: it is refused when the level
// is at the burst.
func levelWait(l *modelLevel, burst int, leak, now time.Duration) time.Duration {
	if l != nil && l.level >= burst {
		return l.at + leak - now
	}
	return 0
}

func (m *model) begin(name, client int) (retry time.Duration, ok bool, warned []string) {
	pairID := [2]int{name, client}
	var byPair, byName, byClient time.Duration
	if p, ok := m.pairs[pairID]; ok {
		if until := p.last + modelWait(p.count); p.count >= 5 && until > m.now {
			byPair = until - m.now
		}
	} else if _, free := m.leastRecentlyUsed(); len(m.pairs) >= m.capacity && !free {
		byPair = time.Minute
		warned = append(warned, whatPairs)
	}
	if l, ok := m.names[name]; ok {
		byName = levelWait(l, 100, 15*time.Minute, m.now)
	} else if len(m.names) >= m.capacity {
		byName = time.Minute
		warned = append(warned, whatNames)
	}
	if l, ok := m.clients[client]; ok {
		byClient = levelWait(l, 30, 30*time.Second, m.now)
	} else if len(m.clients) >= m.capacity {
		byClient = time.Minute
		warned = append(warned, whatClients)
	}
	if wait := max(byPair, byName, byClient); wait > 0 {
		return wholeSeconds(wait), false, warned
	}

	p, ok := m.pairs[pairID]
	if !ok {
		if len(m.pairs) >= m.capacity {
			victim, _ := m.leastRecentlyUsed()
			delete(m.pairs, victim)
		}
		p = &modelPair{}
		m.pairs[pairID] = p
	}
	p.count++
	p.last = m.now
	m.seq++
	p.use = m.seq

	for _, level := range []struct {
		levels map[int]*modelLevel
		id     int
	}{{m.names, name}, {m.clients, client}} {
		l, ok := level.levels[level.id]
		if !ok {
			l = &modelLevel{at: m.now}
			level.levels[level.id] = l
		}
		l.level++
	}
	return 0, true, warned
}

func giveBackLevel(levels map[int]*modelLevel, id int) {
	if l, ok := levels[id]; ok {
		l.level--
		if l.level == 0 {
			delete(levels, id)
		}
	}
}

func (m *model) succeeded(name, client int) {
	delete(m.pairs, [2]int{name, client})
	giveBackLevel(m.names, name)
	giveBackLevel(m.clients, client)
}

func (m *model) aborted(name, client int) {
	pairID := [2]int{name, client}
	if p, ok := m.pairs[pairID]; ok {
		p.count--
		switch p.count {
		case 0:
			delete(m.pairs, pairID)
		case 4:
			m.seq++
			p.use = m.seq
		}
	}
	giveBackLevel(m.names, name)
	giveBackLevel(m.clients, client)
}

// compareLevels reports how the levels of the model differ from the expiry times of the entries that attempts keep, of
// which the ones that have expired do not count.
func (m *model) compareLevels(what string, expiries []int64, levels map[int]*modelLevel, leak time.Duration, lookup func(id int) (int64, bool)) error {
	count := 0
	for _, expires := range expiries {
		if time.Duration(expires) > m.now {
			count++
		}
	}
	if count != len(levels) {
		return fmt.Errorf("%d %ss are counted, the model has %d", count, what, len(levels))
	}
	for id, l := range levels {
		want := int64(l.at + time.Duration(l.level)*leak)
		if got, ok := lookup(id); !ok || got != want {
			return fmt.Errorf("%s %d expires at %v, the model says %v", what, id, time.Duration(got), time.Duration(want))
		}
	}
	return nil
}

// compare reports how what attempts remember differs from what the model remembers: the entries that have not
// expired, with their counts and times, and the order in which pairs are dropped.
func (m *model) compare(a *attempts) error {
	st := a.state()
	live := func(expires int64) bool { return time.Duration(expires) > m.now }

	pairs := map[pairKey][2]int64{}
	for k, v := range st.pairs {
		if live(v[1]) {
			pairs[k] = v
		}
	}
	if len(pairs) != len(m.pairs) {
		return fmt.Errorf("%d pairs are counted, the model has %d", len(pairs), len(m.pairs))
	}
	for id, p := range m.pairs {
		key := newPairKey(m.nameKey[id[0]], m.clientKey[id[1]])
		if got, want := pairs[key], [2]int64{int64(p.count), int64(p.last + 24*time.Hour)}; got != want {
			return fmt.Errorf("pair %v has %v, the model has %v", id, got, want)
		}
	}

	var nameExpiries, clientExpiries []int64
	for _, expires := range st.names {
		nameExpiries = append(nameExpiries, expires)
	}
	for _, expires := range st.clients {
		clientExpiries = append(clientExpiries, expires)
	}
	err := m.compareLevels("login name", nameExpiries, m.names, 15*time.Minute, func(id int) (int64, bool) {
		expires, ok := st.names[m.nameKey[id]]
		return expires, ok
	})
	if err != nil {
		return err
	}
	err = m.compareLevels("client", clientExpiries, m.clients, 30*time.Second, func(id int) (int64, bool) {
		expires, ok := st.clients[m.clientKey[id]]
		return expires, ok
	})
	if err != nil {
		return err
	}

	var order [][2]int
	for _, k := range st.idle {
		if live(st.pairs[k][1]) {
			order = append(order, m.idOf[k])
		}
	}
	var want [][2]int
	for id, p := range m.pairs {
		if p.count < 5 {
			want = append(want, id)
		}
	}
	slices.SortFunc(want, func(x, y [2]int) int { return int(m.pairs[y].use) - int(m.pairs[x].use) })
	if !slices.Equal(order, want) {
		return fmt.Errorf("pairs are dropped in the order %v, the model says %v", order, want)
	}
	return nil
}

func TestAttemptsModel(t *testing.T) {
	long := []time.Duration{
		1, second, 5 * second, 29 * second, 30 * second, 31 * second, time.Minute, 10 * time.Minute, time.Hour,
		24*time.Hour - 1, 24 * time.Hour, 25 * time.Hour,
	}
	short := []time.Duration{1, second, 2 * second, 29 * second, 30 * second, 31 * second}
	mid := []time.Duration{
		1, second, 30 * second, 15*time.Minute - 1, 15 * time.Minute, 15*time.Minute + 1, 30 * time.Minute, time.Hour,
		24*time.Hour - 1, 24 * time.Hour, 26 * time.Hour,
	}
	for _, cfg := range []struct {
		capacity, names, clients int
		moves                    []time.Duration // how the time moves
		often                    int             // how often it moves, in tenths of the steps
	}{
		{4, 8, 8, long, 4},
		{3, 6, 6, long, 4},
		{8, 6, 6, long, 4},
		{1, 3, 3, long, 4},
		{2, 2, 2, long, 4},
		{5, 20, 20, long, 4},
		// Few clients and many login names, and the time standing still: the clients come to their limit.
		{50, 200, 3, short, 1},
		{4, 200, 6, short, 1},
		{50, 200, 3, long, 1},
		// Few login names and many clients: the login names come to their limit, and the time passes in parts of 15 minutes.
		{300, 2, 150, mid, 1},
		{200, 3, 100, mid, 2},
		{40, 3, 100, mid, 2},
	} {
		for seed := range 3 {
			t.Run(fmt.Sprintf("capacity %d, %d names, %d clients, seed %d", cfg.capacity, cfg.names, cfg.clients, seed), func(t *testing.T) {
				h := newHarness(t, cfg.capacity)
				m := newModel(cfg.capacity, cfg.names, cfg.clients)
				m.now = h.a.elapsed()
				r := rand.New(rand.NewPCG(uint64(seed), uint64(cfg.capacity*100+cfg.names)))
				for step := range 4000 {
					if r.IntN(10) < cfg.often {
						d := cfg.moves[r.IntN(len(cfg.moves))]
						h.advance(d)
						m.advance(d)
					}
					name, client := r.IntN(cfg.names), r.IntN(cfg.clients)
					what := fmt.Sprintf("step %d", step)
					switch op := r.IntN(20); {
					case op < 14:
						what += fmt.Sprintf(": begin(%d, %d) at %v", name, client, m.now)
						retry, ok, warned := m.begin(name, client)
						gotRetry, gotOK := h.a.begin(m.nameKey[name], m.clientKey[client])
						if gotRetry != retry || gotOK != ok {
							t.Fatalf("%s = %v, %v, the model says %v, %v", what, gotRetry, gotOK, retry, ok)
						}
						if got := h.warned.take(); !slices.Equal(got, warned) {
							t.Fatalf("%s: warn was called with %q, the model says %q", what, got, warned)
						}
					case op < 17:
						what += fmt.Sprintf(": succeeded(%d, %d)", name, client)
						m.succeeded(name, client)
						h.a.succeeded(m.nameKey[name], m.clientKey[client])
					default:
						what += fmt.Sprintf(": aborted(%d, %d)", name, client)
						m.aborted(name, client)
						h.a.aborted(m.nameKey[name], m.clientKey[client])
					}
					if err := h.a.check(); err != nil {
						t.Fatalf("%s: the tables are not consistent: %v", what, err)
					}
					// Whatever is not compared here shows in the result of a later begin.
					if step < 500 || step%7 == 0 {
						if err := m.compare(h.a); err != nil {
							t.Fatalf("%s: %v", what, err)
						}
					}
				}
			})
		}
	}
}
