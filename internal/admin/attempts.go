package admin

import (
	"crypto/sha256"
	"log"
	"net/netip"
	"sync"
	"time"
)

const (
	freeAttempts = 5                // attempts on a login name from a client before it makes the next ones wait
	firstWait    = 30 * time.Second // the wait after them, which doubles with each attempt
	maxWait      = time.Hour
	pairForget   = 24 * time.Hour // after the last attempt on a login name from a client

	nameBurst    = 100              // attempts on a login name, from all clients, that can come at once
	nameLeak     = 15 * time.Minute // one of them comes back this often
	nameHeadroom = (nameBurst - 1) * nameLeak

	clientBurst    = 30               // attempts that a client can make at once
	clientLeak     = 30 * time.Second // one of them comes back this often
	clientHeadroom = (clientBurst - 1) * clientLeak

	attemptsCapacity = 100000
	fullRetry        = time.Minute // for a new key when its table has no room for it
)

// attempts counts the attempts to log in, before the password is checked, three ways: by login name and client
// together (a pair), which makes the next attempts of that pair wait; by login name over all clients, and by client over
// all login names, which are levels that rise with each attempt and leak out. It tells nothing about the accounts: a
// login name is counted whether or not an account has it. The counts are in memory only, as hashes of login names and
// address prefixes, and a restart forgets them.
type attempts struct {
	mu      sync.Mutex
	clock   func() time.Time
	start   time.Time
	warn    func(string)
	pairs   *table[pairKey]
	names   *table[[32]byte]
	clients *table[netip.Prefix]
}

// pairKey is the key of a login name from a client: the SHA-256 of the key of the login name and of the masked prefix of
// the client, so that it has a fixed size and holds neither of them in clear.
type pairKey [32]byte

func newPairKey(name [32]byte, client netip.Prefix) pairKey {
	var b [49]byte
	copy(b[:], name[:])
	a16 := client.Addr().As16()
	copy(b[32:], a16[:])
	b[48] = byte(client.Bits())
	return sha256.Sum256(b[:])
}

// newAttempts returns attempts that keep count of at most capacity pairs, login names and clients each. When a new one
// is refused for lack of room, warn gets the name of what is full.
func newAttempts(now func() time.Time, capacity int, warn func(string)) *attempts {
	return &attempts{
		clock:   now,
		start:   now(),
		warn:    warn,
		pairs:   newTable[pairKey]("login name and client pairs", capacity),
		names:   newTable[[32]byte]("login names", capacity),
		clients: newTable[netip.Prefix]("clients", capacity),
	}
}

// nameKey returns the key of a normalized login name.
func nameKey(normalized string) [32]byte { return sha256.Sum256([]byte(normalized)) }

func (a *attempts) elapsed() time.Duration { return a.clock().Sub(a.start) }

// begin counts an attempt to log in as the login name from the client, before its password is checked. If the pair, the
// login name or the client refuses the attempt, nothing is counted, and retry is the longest of their waits, in whole
// seconds. An attempt that is allowed ends with succeeded, or with aborted if the password was not checked; after a
// wrong password nothing is called, and the attempt stays counted.
func (a *attempts) begin(name [32]byte, client netip.Prefix) (retry time.Duration, ok bool) {
	client = client.Masked()
	pair := newPairKey(name, client)
	a.mu.Lock()
	now := a.elapsed()
	p, n, c := a.pairs.entries[pair], a.names.entries[name], a.clients.entries[client]
	byPair, pairsFull := a.pairWait(p, now)
	byName, namesFull := bucketWait(a.names, n, now, nameHeadroom)
	byClient, clientsFull := bucketWait(a.clients, c, now, clientHeadroom)
	wait := max(byPair, byName, byClient)
	if wait == 0 {
		a.countPair(pair, p, now)
		countBucket(a.names, name, n, now, nameLeak)
		countBucket(a.clients, client, c, now, clientLeak)
	}
	a.mu.Unlock()

	// Outside the lock: warn writes to a log.
	if pairsFull {
		a.warn(a.pairs.what)
	}
	if namesFull {
		a.warn(a.names.what)
	}
	if clientsFull {
		a.warn(a.clients.what)
	}
	if wait > 0 {
		return (wait + time.Second - 1) / time.Second * time.Second, false
	}
	return 0, true
}

// succeeded ends an attempt whose password was right: the pair is forgotten, and the login name and the client each get
// this attempt back. The attempts of other clients, and the failures before it, stay counted in the levels.
func (a *attempts) succeeded(name [32]byte, client netip.Prefix) {
	client = client.Masked()
	a.mu.Lock()
	defer a.mu.Unlock()
	if p := a.pairs.entries[newPairKey(name, client)]; p != nil {
		a.pairs.remove(p)
	}
	now := a.elapsed()
	giveBack(a.names, name, now, nameLeak)
	giveBack(a.clients, client, now, clientLeak)
}

// aborted ends an attempt whose password was not checked, for a reason of the server: the pair, the login name and the
// client get the attempt back. The pair keeps the time of its last attempt.
func (a *attempts) aborted(name [32]byte, client netip.Prefix) {
	client = client.Masked()
	a.mu.Lock()
	defer a.mu.Unlock()
	if p := a.pairs.entries[newPairKey(name, client)]; p != nil {
		p.count--
		switch p.count {
		case 0:
			a.pairs.remove(p)
		case freeAttempts - 1:
			a.pairs.idle.pushFront(p)
		}
	}
	now := a.elapsed()
	giveBack(a.names, name, now, nameLeak)
	giveBack(a.clients, client, now, clientLeak)
}

// pairWait returns how long an attempt of the pair has to wait, for its entry p if it has one. full reports that the
// pair has no entry and the table no room for one.
func (a *attempts) pairWait(p *entry[pairKey], now time.Duration) (wait time.Duration, full bool) {
	switch {
	case p == nil:
		if !a.pairs.room(now) {
			return fullRetry, true
		}
	case p.expires > now && p.count >= freeAttempts:
		if until := p.expires - pairForget + waitAfter(p.count); until > now {
			return until - now, false
		}
	}
	return 0, false
}

// waitAfter returns how long a pair makes the next attempt wait after count attempts.
func waitAfter(count int32) time.Duration {
	if count < freeAttempts {
		return 0
	}
	wait := firstWait
	for i := int32(freeAttempts); i < count && wait < maxWait; i++ {
		wait *= 2
	}
	return min(wait, maxWait)
}

// bucketWait is pairWait for a level, the login name's or the client's, in the table t. Every attempt adds the leak of
// the level to when the entry expires, so its level, the attempts that have not come back yet, is (expires - now) / leak
// rounded up. An attempt is refused when the level is at its burst, which is when the entry expires more than headroom
// from now.
func bucketWait[K comparable](t *table[K], e *entry[K], now, headroom time.Duration) (wait time.Duration, full bool) {
	switch {
	case e == nil:
		if !t.room(now) {
			return fullRetry, true
		}
	case e.expires > now:
		if left := e.expires - now; left > headroom {
			return left - headroom, false
		}
	}
	return 0, false
}

func (a *attempts) countPair(key pairKey, p *entry[pairKey], now time.Duration) {
	expires := now + pairForget
	switch {
	case p == nil:
		p = a.pairs.insert(key, expires, now)
	case p.expires <= now:
		p.count = 0
		a.pairs.update(p, expires)
	default:
		a.pairs.update(p, expires)
	}
	p.count++
	if p.count < freeAttempts {
		a.pairs.idle.pushFront(p)
	} else {
		a.pairs.idle.unlink(p)
	}
}

func countBucket[K comparable](t *table[K], key K, e *entry[K], now, leak time.Duration) {
	if e == nil {
		t.insert(key, now+leak, now)
		return
	}
	t.update(e, max(e.expires, now)+leak)
}

func giveBack[K comparable](t *table[K], key K, now, leak time.Duration) {
	e := t.entries[key]
	if e == nil {
		return
	}
	expires := e.expires - leak
	if expires <= now {
		t.remove(e)
		return
	}
	t.update(e, expires)
}

// warnEveryMinute returns a warn for newAttempts that logs at most one line a minute.
func warnEveryMinute(l *log.Logger, now func() time.Time) func(string) {
	var mu sync.Mutex
	var next time.Time
	return func(what string) {
		mu.Lock()
		t := now()
		due := !t.Before(next)
		if due {
			next = t.Add(time.Minute)
		}
		mu.Unlock()
		if due {
			l.Printf("too many %s to keep count of; refusing new ones", what)
		}
	}
}
