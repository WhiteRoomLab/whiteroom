package admin

import (
	"fmt"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
)

// These tests fill a table of the attempts the way an attack does, in a table small enough for a test, and show that the
// user still gets in: a new pair is never refused, and a new login name or client is refused only when every entry of its
// table has five attempts or more.

// wrong sends a wrong password for the login name from the address, which the attempts have to let in: the answer is 401.
func (ts *testServer) wrong(t *testing.T, login, addr string) {
	t.Helper()
	ts.wantFailure(t, fmt.Sprintf("a wrong password for %s from %s", login, addr), login, wrongPassword, from(addr)...)
}

// TestLoginWhenThePairsAreFull makes every pair of the table wait, from clients that stay within their limits, and checks
// that the user, from an address that has not tried, is let in at once.
func TestLoginWhenThePairsAreFull(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	admin := ts.addAccount(t, "admin", account.RoleAdmin)
	const pairs = 6
	ts.attempts.pairs.capacity = pairs
	for i := range pairs {
		for range freeAttempts {
			ts.wrong(t, fmt.Sprintf("ghost%d", i), fmt.Sprintf("192.0.2.%d:4000", 10+i))
		}
	}
	if p, _, _ := ts.counted(); p != pairs {
		t.Fatalf("the attempts keep count of %d pairs, want %d: a pair each, all of them waiting", p, pairs)
	}

	res := ts.postLogin("admin", testPassword("admin"), from("198.51.100.9:5000")...)
	ts.checkAnswer(t, "the right password when the table of pairs is full", res, admin, ts.clock.now(), anyLoginCookie)

	// The pair that waited longest was forgotten for it, and has its free attempts again; the others still wait.
	ts.wrong(t, "ghost0", "192.0.2.10:4000")
	ts.wantRetry(t, "ghost1 from the address that made it wait", "ghost1", wrongPassword, "30", from("192.0.2.11:4000")...)
	for range freeAttempts {
		ts.wrong(t, "ghost99", "198.51.100.77:4000")
	}
	ts.wantRetry(t, "the sixth attempt of a new login name", "ghost99", wrongPassword, "30", from("198.51.100.77:4000")...)
	if got := ts.logs.String(); got != "" {
		t.Errorf("a table of pairs that is full has logged %q: a pair is never refused for lack of room", redact(got))
	}
}

// TestLoginWhenThePairsAreFilledSlowly does it from the four /64s of one /56, each of which makes one attempt every 30
// seconds, which the limit of a client lets through for ever, and so fills the table and keeps it full. The table has room
// for two pairs of each client.
func TestLoginWhenThePairsAreFilledSlowly(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	admin := ts.addAccount(t, "admin", account.RoleAdmin)
	const pairs, clients = 8, 4
	ts.attempts.pairs.capacity = pairs
	round := 0
	next := func() {
		for c := range clients {
			// Five attempts on one login name from a client make its pair wait, and then it is the next login name.
			ts.wrong(t, fmt.Sprintf("ghost%d-%d", round/freeAttempts, c), fmt.Sprintf("[2001:db8:0:%x::1]:4000", c))
		}
		round++
		ts.clock.advance(30 * time.Second)
	}
	for range 3 * freeAttempts {
		next()
	}
	if p, _, _ := ts.counted(); p != pairs {
		t.Fatalf("the attempts keep count of %d pairs after %d rounds, want %d: all of them waiting", p, round, pairs)
	}
	for hour := range 3 {
		for range 120 {
			next()
		}
		what := fmt.Sprintf("the right password in hour %d of the attack", hour+1)
		res := ts.postLogin("admin", testPassword("admin"), from(fmt.Sprintf("198.51.100.%d:5000", 10+hour))...)
		ts.checkAnswer(t, what, res, admin, ts.clock.now(), anyLoginCookie)
	}
	if got := ts.logs.String(); got != "" {
		t.Errorf("the attack has logged %q", redact(got))
	}
}

// TestLoginWhenTheLoginNamesAreFlooded sends one attempt for each of many login names that nobody has, from addresses that stay
// within their limits: the table fills with entries that have one attempt, which take each other's places.
func TestLoginWhenTheLoginNamesAreFlooded(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	admin := ts.addAccount(t, "admin", account.RoleAdmin)
	const names = 8
	ts.attempts.names.capacity = names
	for i := range 5 * names {
		ts.wrong(t, fmt.Sprintf("ghost%d", i), fmt.Sprintf("192.0.2.%d:4000", 10+i/10))
	}
	if _, n, _ := ts.counted(); n != names {
		t.Fatalf("the attempts keep count of %d login names, want %d", n, names)
	}
	res := ts.postLogin("admin", testPassword("admin"), from("198.51.100.9:5000")...)
	ts.checkAnswer(t, "the right password when the table of login names is full", res, admin, ts.clock.now(), anyLoginCookie)
	if got := ts.logs.String(); got != "" {
		t.Errorf("the flood has logged %q: nothing was refused", redact(got))
	}
}

// TestLoginWhenEveryLoginNameHasFiveAttempts is what is left of it: a login name with no entry is refused, and says so once,
// until the attempts have come back. The login names that have attempts are what caps the guesses, so none is forgotten.
func TestLoginWhenEveryLoginNameHasFiveAttempts(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	admin := ts.addAccount(t, "admin", account.RoleAdmin)
	const names = 8
	ts.attempts.names.capacity = names
	for i := range names {
		for range freeAttempts {
			ts.wrong(t, fmt.Sprintf("ghost%d", i), fmt.Sprintf("192.0.2.%d:4000", 10+i))
		}
	}
	ts.wantRetry(t, "the right password when every login name has five attempts", "admin", testPassword("admin"), "60", from("198.51.100.9:5000")...)
	if got, want := ts.logs.String(), "too many login names to keep count of; refusing new ones\n"; got != want {
		t.Errorf("the log says %q, want %q", redact(got), want)
	}
	// The five attempts of a login name are back after five quarters of an hour.
	ts.clock.advance(5*15*time.Minute - 1)
	ts.wantRetry(t, "a nanosecond before the attempts are back", "admin", testPassword("admin"), "60", from("198.51.100.9:5000")...)
	ts.clock.advance(1)
	res := ts.postLogin("admin", testPassword("admin"), from("198.51.100.9:5000")...)
	ts.checkAnswer(t, "the right password when the attempts are back", res, admin, ts.clock.now(), anyLoginCookie)
}

// TestLoginWhenTheClientsAreFlooded is the same for clients: one attempt from each of many addresses.
func TestLoginWhenTheClientsAreFlooded(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	admin := ts.addAccount(t, "admin", account.RoleAdmin)
	const clients = 8
	ts.attempts.clients.capacity = clients
	for i := range 5 * clients {
		ts.wrong(t, fmt.Sprintf("ghost%d", i), fmt.Sprintf("192.0.2.%d:4000", 10+i))
	}
	if _, _, c := ts.counted(); c != clients {
		t.Fatalf("the attempts keep count of %d clients, want %d", c, clients)
	}
	res := ts.postLogin("admin", testPassword("admin"), from("198.51.100.9:5000")...)
	ts.checkAnswer(t, "the right password when the table of clients is full", res, admin, ts.clock.now(), anyLoginCookie)
	if got := ts.logs.String(); got != "" {
		t.Errorf("the flood has logged %q: nothing was refused", redact(got))
	}
}

func TestLoginWhenEveryClientHasFiveAttempts(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	admin := ts.addAccount(t, "admin", account.RoleAdmin)
	const clients = 8
	ts.attempts.clients.capacity = clients
	for c := range clients {
		for i := range freeAttempts {
			ts.wrong(t, fmt.Sprintf("ghost%d", i), fmt.Sprintf("192.0.2.%d:4000", 10+c))
		}
	}
	ts.wantRetry(t, "the right password when every client has five attempts", "admin", testPassword("admin"), "60", from("198.51.100.9:5000")...)
	if got, want := ts.logs.String(), "too many clients to keep count of; refusing new ones\n"; got != want {
		t.Errorf("the log says %q, want %q", redact(got), want)
	}
	// The five attempts of a client are back after five times 30 seconds.
	ts.clock.advance(5*30*time.Second - 1)
	ts.wantRetry(t, "a nanosecond before the attempts are back", "admin", testPassword("admin"), "60", from("198.51.100.9:5000")...)
	ts.clock.advance(1)
	res := ts.postLogin("admin", testPassword("admin"), from("198.51.100.9:5000")...)
	ts.checkAnswer(t, "the right password when the attempts are back", res, admin, ts.clock.now(), anyLoginCookie)
}
