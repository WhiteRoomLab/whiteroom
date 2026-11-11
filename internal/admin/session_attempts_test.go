package admin

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

const wrongPassword = "not the password of anybody"

// retryAfter checks that res is a 429 and returns its Retry-After.
func retryAfter(t *testing.T, what string, res *http.Response) string {
	t.Helper()
	retry := res.Header.Get("Retry-After")
	checkProblem(t, what, res, "too-many-attempts")
	return retry
}

// viaProxy returns the header with which a trusted proxy tells that the request is from the client.
func viaProxy(client string) []string {
	return []string{"X-Forwarded-For", client}
}

// wantFailure checks that a login with the password is answered with the 401 of a wrong password.
func (ts *testServer) wantFailure(t *testing.T, what, login, password string, headers ...string) {
	t.Helper()
	checkProblem(t, what, ts.postLogin(login, password, headers...), "bad-credentials")
}

// wantRetry checks that a login with the password is answered with the 429 that has the Retry-After.
func (ts *testServer) wantRetry(t *testing.T, what, login, password, retry string, headers ...string) {
	t.Helper()
	res := ts.postLogin(login, password, headers...)
	if got := retryAfter(t, what, res); got != retry {
		t.Errorf("%s has the Retry-After %q, want %q", what, got, retry)
	}
	checkNoCookie(t, what, res)
}

func TestLoginAttempts(t *testing.T) {
	t.Run("a login name from a client", func(t *testing.T) {
		ts := newTestServer(t, httpsOrigin)
		admin := ts.addAccount(t, "admin", account.RoleAdmin)
		for i := 1; i <= 5; i++ {
			ts.wantFailure(t, fmt.Sprintf("wrong password %d", i), "admin", wrongPassword)
		}
		if p, n, c := ts.counted(); p != 1 || n != 1 || c != 1 {
			t.Errorf("after five wrong passwords the attempts keep count of %d pairs, %d login names and %d clients, want 1 each", p, n, c)
		}
		// The sixth attempt waits 30 seconds, and the right password does not shorten it.
		ts.wantRetry(t, "the sixth attempt", "admin", wrongPassword, "30")
		ts.wantRetry(t, "the sixth attempt with the right password", "admin", testPassword("admin"), "30")
		if n := ts.sessionRows(t); n != 0 {
			t.Errorf("logins that are refused have made %d sessions", n)
		}
		ts.clock.advance(29 * time.Second)
		ts.wantRetry(t, "after 29 seconds", "admin", wrongPassword, "1")
		ts.clock.advance(time.Second)
		ts.wantFailure(t, "the sixth attempt after the wait", "admin", wrongPassword)
		// Each attempt doubles the wait.
		ts.wantRetry(t, "the seventh attempt", "admin", wrongPassword, "60")
		ts.clock.advance(time.Minute)
		ts.wantFailure(t, "the seventh attempt after the wait", "admin", wrongPassword)
		ts.wantRetry(t, "the eighth attempt", "admin", wrongPassword, "120")
		ts.clock.advance(2 * time.Minute)
		ts.checkAnswer(t, "the right password after the wait", ts.postLogin("admin", testPassword("admin")), admin, ts.clock.now(), anyLoginCookie)
		if p, _, _ := ts.counted(); p != 0 {
			t.Errorf("after a login that worked the attempts keep count of %d pairs, want none", p)
		}
	})

	t.Run("the right password forgets the pair", func(t *testing.T) {
		ts := newTestServer(t, httpsOrigin)
		admin := ts.addAccount(t, "admin", account.RoleAdmin)
		for range 4 {
			ts.wantFailure(t, "a wrong password", "admin", wrongPassword)
		}
		ts.checkAnswer(t, "the right password", ts.postLogin("admin", testPassword("admin")), admin, ts.clock.now(), anyLoginCookie)
		for range 5 {
			ts.wantFailure(t, "a wrong password after the right one", "admin", wrongPassword)
		}
		ts.wantRetry(t, "the sixth wrong password after the right one", "admin", wrongPassword, "30")
	})

	t.Run("another client", func(t *testing.T) {
		ts := newTestServer(t, httpsOrigin, "192.0.2.0/24")
		admin := ts.addAccount(t, "admin", account.RoleAdmin)
		for range 5 {
			ts.wantFailure(t, "a wrong password", "admin", wrongPassword, viaProxy("203.0.113.1")...)
		}
		ts.wantRetry(t, "the sixth attempt of the client", "admin", wrongPassword, "30", viaProxy("203.0.113.1")...)
		// The login name is not locked: the person who has the password logs in from another address.
		ts.checkAnswer(t, "the right password from another client", ts.postLogin("admin", testPassword("admin"), viaProxy("203.0.113.2")...),
			admin, ts.clock.now(), anyLoginCookie)
		ts.wantFailure(t, "a wrong password from another client", "admin", wrongPassword, viaProxy("203.0.113.3")...)

		// A client of IPv6 is a /64: the addresses of one share the attempts, and another /64 has its own.
		for range 5 {
			ts.wantFailure(t, "a wrong password from IPv6", "admin", wrongPassword, viaProxy("2001:db8:1:2::1")...)
		}
		ts.wantRetry(t, "an attempt from another address of the /64", "admin", wrongPassword, "30", viaProxy("2001:db8:1:2:ffff::9")...)
		ts.wantFailure(t, "an attempt from another /64", "admin", wrongPassword, viaProxy("2001:db8:1:3::1")...)
	})

	t.Run("a login name from every client", func(t *testing.T) {
		ts := newTestServer(t, httpsOrigin, "192.0.2.0/24")
		ts.addAccount(t, "admin", account.RoleAdmin)
		ts.addAccount(t, "editor", account.RoleEditor)
		// Twenty clients with five attempts each make a hundred, which is as many as a login name has at once.
		for c := 1; c <= 20; c++ {
			for range 5 {
				ts.wantFailure(t, "a wrong password", "admin", wrongPassword, viaProxy(fmt.Sprintf("203.0.113.%d", c))...)
			}
		}
		ts.wantRetry(t, "an attempt of a new client", "admin", wrongPassword, "900", viaProxy("203.0.113.21")...)
		ts.wantRetry(t, "the right password of a new client", "admin", testPassword("admin"), "900", viaProxy("203.0.113.22")...)
		ts.wantFailure(t, "another login name from the same client", "editor", wrongPassword, viaProxy("203.0.113.21")...)
		// One attempt comes back every 15 minutes.
		ts.clock.advance(14*time.Minute + 59*time.Second)
		ts.wantRetry(t, "an attempt a second early", "admin", wrongPassword, "1", viaProxy("203.0.113.21")...)
		ts.clock.advance(time.Second)
		ts.wantFailure(t, "an attempt after the quarter of an hour", "admin", wrongPassword, viaProxy("203.0.113.21")...)
		ts.wantRetry(t, "the next attempt", "admin", wrongPassword, "900", viaProxy("203.0.113.23")...)
	})

	t.Run("a client over every login name", func(t *testing.T) {
		ts := newTestServer(t, httpsOrigin)
		ts.addAccount(t, "admin", account.RoleAdmin)
		for i := 1; i <= 30; i++ {
			ts.wantFailure(t, "a wrong password", fmt.Sprintf("user%d", i), wrongPassword)
		}
		ts.wantRetry(t, "the thirty-first attempt", "user31", wrongPassword, "30")
		ts.wantRetry(t, "the right password of an account", "admin", testPassword("admin"), "30")
		// Another client has its own attempts.
		ts.wantFailure(t, "a wrong password from another client", "user1", wrongPassword, from("192.0.2.77:4000")...)
		ts.clock.advance(30 * time.Second)
		ts.wantFailure(t, "an attempt after the wait", "user32", wrongPassword)
		ts.wantRetry(t, "the next attempt", "user33", wrongPassword, "30")
	})

	t.Run("the headers of an untrusted peer are not read", func(t *testing.T) {
		ts := newTestServer(t, httpsOrigin)
		for i := 1; i <= 30; i++ {
			ts.wantFailure(t, "a wrong password", fmt.Sprintf("user%d", i), wrongPassword,
				"X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i), "Forwarded", fmt.Sprintf("for=198.51.100.%d", i),
				"X-Real-IP", fmt.Sprintf("198.51.100.%d", i))
		}
		ts.wantRetry(t, "an attempt with another forged address", "user31", wrongPassword, "30", viaProxy("198.51.100.99")...)
		ts.wantRetry(t, "an attempt with a forged chain", "user32", wrongPassword, "30", viaProxy("198.51.100.98, 198.51.100.97")...)
	})

	t.Run("only the last address that a proxy adds is read", func(t *testing.T) {
		ts := newTestServer(t, httpsOrigin, "192.0.2.0/24", "10.0.0.0/8")
		for i := 1; i <= 5; i++ {
			ts.wantFailure(t, "a wrong password", "admin", wrongPassword, viaProxy(fmt.Sprintf("198.51.100.%d, 203.0.113.9, 10.1.1.%d", i, i))...)
		}
		ts.wantRetry(t, "the sixth attempt", "admin", wrongPassword, "30", viaProxy("198.51.100.77, 203.0.113.9")...)
		ts.wantFailure(t, "an attempt of the forged address", "admin", wrongPassword, viaProxy("203.0.113.10, 198.51.100.1")...)
	})

	t.Run("the spellings of a login name", func(t *testing.T) {
		ts := newTestServer(t, httpsOrigin)
		ts.addAccount(t, "admin", account.RoleAdmin)
		for _, login := range []string{"admin", "Admin", "ADMIN", " admin\n", "ad\x00min"} {
			ts.wantFailure(t, fmt.Sprintf("a wrong password for %q", login), login, wrongPassword)
		}
		ts.wantRetry(t, "the sixth spelling", "aDmIn", wrongPassword, "30")
		ts.wantRetry(t, "the right password", "\tADMIN", testPassword("admin"), "30")
		if p, n, c := ts.counted(); p != 1 || n != 1 || c != 1 {
			t.Errorf("the spellings of a login name are counted as %d pairs, %d login names and %d clients, want 1 each", p, n, c)
		}
	})

	t.Run("a login name that has no account", func(t *testing.T) {
		ts := newTestServer(t, httpsOrigin)
		for range 5 {
			ts.wantFailure(t, "a wrong password for nobody", "nobody", wrongPassword)
		}
		ts.wantRetry(t, "the sixth attempt for nobody", "nobody", wrongPassword, "30")
		if p, n, c := ts.counted(); p != 1 || n != 1 || c != 1 {
			t.Errorf("a login name that has no account is counted as %d pairs, %d login names and %d clients, want 1 each", p, n, c)
		}
	})

	t.Run("a refusal does not reach the accounts", func(t *testing.T) {
		ts := newTestServer(t, httpsOrigin)
		ts.addAccount(t, "admin", account.RoleAdmin)
		for range 5 {
			ts.wantFailure(t, "a wrong password", "admin", wrongPassword)
		}
		if err := ts.db.Close(); err != nil {
			t.Fatal(err)
		}
		// A closed store fails a login that is let in, so a 429 was answered without the accounts.
		ts.wantRetry(t, "the sixth attempt with a closed store", "admin", testPassword("admin"), "30")
		checkProblem(t, "an attempt of another login name with a closed store", ts.postLogin("other", wrongPassword), "internal")
	})
}

// TestLoginAccounting shows how many attempts each kind of end of a login leaves counted: a wrong password leaves its attempt,
// a login that could not check the password gives its attempt back once, and a right password forgets the pair and gives back
// its own attempt to the login name and the client.
func TestLoginAccounting(t *testing.T) {
	const addr = "192.0.2.1"
	ts := newTestServer(t, httpsOrigin)
	ts.addAccount(t, "admin", account.RoleAdmin)
	want := func(what string, pair, name, client int) {
		t.Helper()
		if p, n, c := ts.levels("admin", addr); p != pair || n != name || c != client {
			t.Errorf("%s: the attempts count %d for the pair, %d for the login name and %d for the client, want %d, %d and %d",
				what, p, n, c, pair, name, client)
		}
	}
	want("at the start", 0, 0, 0)
	ts.wantFailure(t, "a wrong password", "admin", wrongPassword)
	want("after a wrong password", 1, 1, 1)
	ts.wantFailure(t, "a wrong password", "admin", wrongPassword)
	want("after two wrong passwords", 2, 2, 2)

	// A login that could not check the password takes back its attempt, once.
	ts.deadline = time.Nanosecond
	checkProblem(t, "login after the deadline", ts.postLogin("admin", wrongPassword), "busy")
	checkProblem(t, "login after the deadline", ts.postLogin("admin", testPassword("admin")), "busy")
	want("after logins that were not answered in time", 2, 2, 2)
	ts.deadline = defaultDeadline

	// A body that is refused and a request from another origin count nothing.
	ts.sendJSON("/api/session", "application/json", "{")
	ts.send(http.MethodPost, "/api/session", loginBody("admin", wrongPassword), "Content-Type", "application/json")
	want("after requests that count nothing", 2, 2, 2)

	// A login with the right password forgets the pair and takes back its own attempt.
	if got := ts.postLogin("admin", testPassword("admin")).StatusCode; got != http.StatusOK {
		t.Fatalf("login has the status %d, want 200", got)
	}
	want("after a login that worked", 0, 2, 2)

	ts.wantFailure(t, "a wrong password", "admin", wrongPassword)
	want("after a wrong password after the login", 1, 3, 3)
	if err := ts.db.Close(); err != nil {
		t.Fatal(err)
	}
	checkProblem(t, "login with a closed store", ts.postLogin("admin", wrongPassword), "internal")
	want("after a login that the store failed", 1, 3, 3)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestLoginPlaces shows that the logins which attempts has let in, and which wait for the hashes or the store, are
// limited: the next one is answered with 503 and its attempt is given back.
func TestLoginPlaces(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ts.addAccount(t, "admin", account.RoleAdmin)
	if got := cap(ts.logins); got != 32 {
		t.Fatalf("the Server has %d places for logins, want 32", got)
	}
	places := cap(ts.logins)
	// A client that has used its five attempts is refused while the places are taken: attempts comes before the places.
	for range 5 {
		ts.wantFailure(t, "a wrong password", "other", wrongPassword, from("203.0.113.2:5000")...)
	}

	// A login that has checked its password waits for the writer of the store to write its session. The test holds it.
	held, release := make(chan struct{}), make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)
	done := make(chan error, 1)
	go func() {
		done <- ts.db.Update(t.Context(), func(*store.Tx) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	responses := make(chan *http.Response, places)
	var wg sync.WaitGroup
	for i := range places {
		wg.Go(func() {
			responses <- ts.postLogin("admin", testPassword("admin"), from(fmt.Sprintf("198.51.100.%d:5000", i+1))...)
		})
	}
	waitFor(t, "every place for a login to be taken", func() bool { return len(ts.logins) == places })
	pairs, names, clients := ts.counted()
	if pairs != places+1 || names != 2 || clients != places+1 {
		t.Errorf("the attempts keep count of %d pairs, %d login names and %d clients, want %d, 2 and %d", pairs, names, clients, places+1, places+1)
	}

	// One login more is answered with 503, as a context that ends is, and its attempt is given back once: the login name
	// keeps the attempts of the logins that wait. The sixth attempt of a client is answered with 429 meanwhile. The
	// refusals are recorded, but the test holds the writer of the store, so each record waits for it as long as a record
	// that stands alone may, and then is not written: the answers are the same, and come when that time is over.
	if _, name, _ := ts.levels("admin", "203.0.113.1"); name != places {
		t.Errorf("the logins that wait have %d attempts counted for the login name, want %d", name, places)
	}
	extra, limited := make(chan *http.Response, 1), make(chan *http.Response, 1)
	go func() { extra <- ts.postLogin("admin", testPassword("admin"), from("203.0.113.1:5000")...) }()
	go func() { limited <- ts.postLogin("other", wrongPassword, from("203.0.113.2:5000")...) }()
	var res, refused *http.Response
	for res == nil || refused == nil {
		select {
		case res = <-extra:
		case refused = <-limited:
		case <-time.After(20 * time.Second):
			t.Fatal("a login that is refused while the places are taken is not answered: it waits for a place")
		}
	}
	checkProblem(t, "the login after the places are taken", res, "busy")
	checkNoCookie(t, "the login after the places are taken", res)
	if got := retryAfter(t, "the sixth attempt of a client while the places are taken", refused); got != "30" {
		t.Errorf("the sixth attempt of a client while the places are taken has the Retry-After %q, want 30", got)
	}
	checkNoCookie(t, "the sixth attempt of a client while the places are taken", refused)
	if p, n, c := ts.counted(); p != pairs || n != names || c != clients {
		t.Errorf("after the login that found no place the attempts keep count of %d pairs, %d login names and %d clients, want %d, %d and %d",
			p, n, c, pairs, names, clients)
	}
	if pair, name, client := ts.levels("admin", "203.0.113.1"); pair != 0 || name != places || client != 0 {
		t.Errorf("after the login that found no place the attempts count %d for its pair, %d for the login name and %d for its client, want 0, %d and 0",
			pair, name, client, places)
	}
	const lost = "cannot record login.failure: context deadline exceeded\n"
	if got := ts.logs.String(); got != lost+lost {
		t.Errorf("the logins that were refused have logged %q, want two lines for the records that could not be written", redact(got))
	}

	releaseOnce()
	wg.Wait()
	close(responses)
	n := 0
	for res := range responses {
		n++
		if res.StatusCode != http.StatusOK {
			t.Errorf("a login that waited has the status %d, want 200", res.StatusCode)
		}
	}
	if n != places {
		t.Errorf("%d logins were answered, want %d", n, places)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := len(ts.logins); got != 0 {
		t.Errorf("%d places for logins are still taken", got)
	}
	// The places are free again, and the client that was turned away has all its attempts.
	if got := ts.postLogin("admin", testPassword("admin"), from("203.0.113.1:5000")...).StatusCode; got != http.StatusOK {
		t.Errorf("a login when the places are free has the status %d, want 200", got)
	}
	// An account keeps its ten most recently used sessions.
	if n := ts.sessionRows(t); n != account.SessionsPerAccount {
		t.Errorf("the store has %d sessions, want %d", n, account.SessionsPerAccount)
	}
}

// TestLoginFailuresLookAlike shows that the answers to the logins that fail do not tell why: an unknown login name, a wrong
// password, a pending account, a disabled account and a password of more than 8 KiB have the same status, the same headers
// but Date, and the same body, and so have the answers that refuse them for too many attempts.
func TestLoginFailuresLookAlike(t *testing.T) {
	ls := newLiveServer(t, false)
	admin := ls.addAccount(t, "admin", account.RoleAdmin)
	ls.addAccount(t, "editor", account.RoleEditor)
	ls.inviteAccount(t, "invited", account.RoleViewer)
	left := ls.addAccount(t, "left", account.RoleViewer)
	if _, err := ls.accounts.Disable(t.Context(), account.By(admin.ID), left.ID); err != nil {
		t.Fatal(err)
	}
	c := ls.client(nil)
	type answer struct {
		status int
		header http.Header
		body   string
	}
	post := func(login, password string) answer {
		t.Helper()
		ls.seen.password(password)
		res := ls.do(t, c, http.MethodPost, "/api/session", loginBody(login, password))
		body := bodyOf(t, res)
		res.Header.Del("Date")
		return answer{res.StatusCode, res.Header, body}
	}

	want := post("nobody", testPassword("nobody"))
	if want.status != http.StatusUnauthorized || want.body != problemJSON("bad-credentials") ||
		!equalValues(want.header.Values("Www-Authenticate"), "Session") || len(want.header.Values("Set-Cookie")) != 0 {
		t.Errorf("a login of an unknown name is answered with %d %s and the headers %v", want.status, want.body, want.header)
	}
	for _, tt := range []struct{ what, login, password string }{
		{"a wrong password", "editor", "not the password of the editor"},
		{"the right password of a pending account", "invited", testPassword("invited")},
		{"a password for a pending account", "invited", wrongPassword},
		{"the right password of a disabled account", "left", testPassword("left")},
		{"a wrong password for a disabled account", "left", wrongPassword},
		{"a password of more than 8 KiB", "editor", strings.Repeat("p", 9000)},
		{"a password of more than 8 KiB for an unknown name", "nobody", strings.Repeat("p", 9000)},
		{"an empty login name and password", "", ""},
		{"a login name with control characters", "ed\x01itor", testPassword("editor") + "x"},
		{"a login name that is a long text", strings.Repeat("e", 5000), wrongPassword},
	} {
		if got := post(tt.login, tt.password); !reflect.DeepEqual(got, want) {
			t.Errorf("%s is answered with %d %s and the headers %v, want %d %s and %v",
				tt.what, got.status, got.body, got.header, want.status, want.body, want.header)
		}
	}
}

func TestLoginRefusalsLookAlike(t *testing.T) {
	ls := newLiveServer(t, false)
	ls.addAccount(t, "editor", account.RoleEditor)
	c := ls.client(nil)
	post := func(login, password string) (int, http.Header, string) {
		t.Helper()
		ls.seen.password(password)
		res := ls.do(t, c, http.MethodPost, "/api/session", loginBody(login, password))
		body := bodyOf(t, res)
		res.Header.Del("Date")
		return res.StatusCode, res.Header, body
	}
	type answer struct {
		status int
		header http.Header
		body   string
	}
	refused := map[string]answer{}
	for _, login := range []string{"nobody", "editor"} {
		for range 5 {
			post(login, wrongPassword)
		}
		status, header, body := post(login, wrongPassword)
		refused[login] = answer{status, header, body}
	}
	if got, want := refused["nobody"], refused["editor"]; !reflect.DeepEqual(got, want) {
		t.Errorf("an unknown login name is refused with %d %s and the headers %v, an account with %d %s and %v",
			got.status, got.body, got.header, want.status, want.body, want.header)
	}
	if a := refused["editor"]; a.status != http.StatusTooManyRequests || a.body != problemJSON("too-many-attempts") ||
		a.header.Get("Retry-After") != "30" {
		t.Errorf("a login that is refused is answered with %d %s and Retry-After %q, want 429 and 30", a.status, a.body, a.header.Get("Retry-After"))
	}
	// The right password is refused alike.
	status, header, body := post("editor", testPassword("editor"))
	if got, want := (answer{status, header, body}), refused["editor"]; !reflect.DeepEqual(got, want) {
		t.Errorf("the right password is refused with %d %s and %v, a wrong one with %d %s and %v",
			got.status, got.body, got.header, want.status, want.body, want.header)
	}
}
