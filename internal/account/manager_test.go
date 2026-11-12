package account

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"maps"
	mathrand "math/rand/v2"
	"net/netip"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/nfc"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

var (
	mgrTime = time.Date(2026, 11, 8, 12, 0, 0, 123456000, time.UTC)
	mgrSeed = [32]byte{'m', 'a', 'n', 'a', 'g', 'e', 'r'}
	// mgrMissingID is the ID of an account that no test makes.
	mgrMissingID = content.FormatID(content.RefUser, [16]byte{0: 0x01, 15: 0xff})
	// mgrClient is where the requests of the tests come from.
	mgrClient = Client{Addr: netip.MustParseAddr("203.0.113.7"), UserAgent: "whiteroom-test/1.0"}
)

// mgrClock is a clock that a test sets.
type mgrClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *mgrClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *mgrClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func (c *mgrClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// mgrEnv is a Manager of a store in a temporary directory, with a clock that the test sets, a low iteration count and
// IDs that are the same in every run.
type mgrEnv struct {
	m     *Manager
	s     *store.Store
	dir   string // the data directory of s
	clock *mgrClock
}

func newMgr(t *testing.T) *mgrEnv {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	s, err := store.Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return newMgrOn(t, s, dir)
}

// newMgrOn makes a Manager of the store s whose hashes have pwLow iterations, the hash for nobody too, so that, of the
// tests of a Manager, only TestNew and TestDefaultIterations pay for a hash of the real count.
func newMgrOn(t *testing.T, s *store.Store, dir string) *mgrEnv {
	t.Helper()
	m, err := newManager(s, pwLow, rand.Reader)
	if err != nil {
		t.Fatalf("newManager() = %v", err)
	}
	clock := &mgrClock{t: mgrTime}
	m.SetClock(clock.now)
	m.ids = content.NewIDSource(mathrand.NewChaCha8(mgrSeed))
	return &mgrEnv{m: m, s: s, dir: dir, clock: clock}
}

// mgrPassword is the password that the tests give to the account with the login: it passes every rule.
func mgrPassword(login string) string {
	return login + " has a long password"
}

func mgrInput(login string, role Role) Input {
	return Input{Login: login, Name: "Name of " + login, Email: login + "@example.com", Role: role}
}

// first makes the first administrator, "admin".
func (e *mgrEnv) first(t *testing.T) Account {
	t.Helper()
	a, err := e.m.CreateFirst(t.Context(), mgrInput("admin", RoleAdmin), mgrPassword("admin"))
	if err != nil {
		t.Fatalf("CreateFirst() = %v", err)
	}
	return a
}

// invite invites the account by System, and returns it with its token.
func (e *mgrEnv) invite(t *testing.T, login string, role Role) (Account, string) {
	t.Helper()
	a, token, err := e.m.Invite(t.Context(), System, mgrInput(login, role))
	if err != nil {
		t.Fatalf("Invite(%q) = %v", login, err)
	}
	return a, token
}

// add makes the account active: it invites it by System and sets its password to mgrPassword(login).
func (e *mgrEnv) add(t *testing.T, login string, role Role) Account {
	t.Helper()
	_, token := e.invite(t, login, role)
	a, err := e.m.SetPassword(t.Context(), mgrClient, token, mgrPassword(login))
	if err != nil {
		t.Fatalf("SetPassword(%q) = %v", login, err)
	}
	return a
}

func (e *mgrEnv) rows(t *testing.T) []row {
	t.Helper()
	var rows []row
	err := e.s.View(t.Context(), func(tx *store.Tx) error {
		var err error
		rows, err = loadRows(tx)
		return err
	})
	if err != nil {
		t.Fatalf("loadRows() = %v", err)
	}
	return rows
}

func (e *mgrEnv) row(t *testing.T, id string) row {
	t.Helper()
	rows := e.rows(t)
	i := indexOfID(rows, id)
	if i < 0 {
		t.Fatalf("no row for %s", id)
	}
	return rows[i]
}

// raw returns the bytes of every stored row by ID, to see that a failed operation changed nothing. It never prints them.
func (e *mgrEnv) raw(t *testing.T) map[string]string {
	t.Helper()
	got := make(map[string]string)
	err := e.s.View(t.Context(), func(tx *store.Tx) error {
		stored, err := tx.Accounts()
		for _, s := range stored {
			got[s.ID] = string(s.Data)
		}
		return err
	})
	if err != nil {
		t.Fatalf("Accounts() = %v", err)
	}
	return got
}

// rawSessions returns the bytes of every stored session row by key, as raw does for the accounts. A key is the hash of a
// token, so tests that compare rows never print one.
func (e *mgrEnv) rawSessions(t *testing.T) map[string]string {
	t.Helper()
	got := make(map[string]string)
	err := e.s.View(t.Context(), func(tx *store.Tx) error {
		stored, err := tx.Sessions()
		for _, s := range stored {
			got[s.Key] = string(s.Data)
		}
		return err
	})
	if err != nil {
		t.Fatalf("Sessions() = %v", err)
	}
	return got
}

// mgrMark is what the store keeps of the accounts, the sessions and the audit log at a time: the bytes of every row, the
// number of the last record of each log, and the size of the data directory, which grows with every commit, also one that
// writes the bytes that were there.
type mgrMark struct {
	rows     map[string]string
	sessions map[string]string
	last     map[audit.Log]uint64
	size     int64
}

func (e *mgrEnv) mark(t *testing.T) mgrMark {
	t.Helper()
	var size int64
	err := filepath.WalkDir(e.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		size += info.Size()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	heads, err := e.s.AuditHeads(t.Context())
	if err != nil {
		t.Fatalf("AuditHeads() = %v", err)
	}
	last := make(map[audit.Log]uint64)
	for _, h := range heads {
		last[h.Log] = h.N
	}
	return mgrMark{rows: e.raw(t), sessions: e.rawSessions(t), last: last, size: size}
}

// sameRows checks that the rows of the accounts and of the sessions are as they were at before. It names a session row by
// its place in the order of the keys, never by its key.
func (e *mgrEnv) sameRows(t *testing.T, before, after mgrMark) {
	t.Helper()
	for id, data := range before.rows {
		if got, ok := after.rows[id]; !ok {
			t.Errorf("the row of %s is gone", id)
		} else if got != data {
			t.Errorf("the row of %s changed", id)
		}
	}
	for id := range after.rows {
		if _, ok := before.rows[id]; !ok {
			t.Errorf("the row of %s is new", id)
		}
	}
	for i, key := range slices.Sorted(maps.Keys(before.sessions)) {
		if got, ok := after.sessions[key]; !ok {
			t.Errorf("session row %d of %d is gone", i+1, len(before.sessions))
		} else if got != before.sessions[key] {
			t.Errorf("session row %d of %d changed", i+1, len(before.sessions))
		}
	}
	if n := len(after.sessions) - len(before.sessions); n > 0 {
		t.Errorf("%d session rows are new", n)
	}
}

// unchanged checks that the store is as it was at before, which mark returned: the same rows of the accounts and of the
// sessions, no record, and no commit since.
func (e *mgrEnv) unchanged(t *testing.T, before mgrMark) {
	t.Helper()
	after := e.mark(t)
	e.sameRows(t, before, after)
	if names := e.added(t, before); len(names) > 0 {
		t.Errorf("the audit log has the records %q, want none", names)
	}
	if after.size != before.size {
		t.Errorf("the data directory changed from %d to %d bytes: something was committed", before.size, after.size)
	}
}

// recorded checks that the rows of the accounts and of the sessions are as they were at before, and that the audit log has
// gained the records that want names, in this order.
func (e *mgrEnv) recorded(t *testing.T, before mgrMark, want ...string) {
	t.Helper()
	e.sameRows(t, before, e.mark(t))
	e.gained(t, before, want...)
}

// gained checks that the audit log has gained the records that want names since before, in this order.
func (e *mgrEnv) gained(t *testing.T, before mgrMark, want ...string) {
	t.Helper()
	if got := e.added(t, before); !slices.Equal(got, want) {
		t.Errorf("the audit log has gained the records %q, want %q", got, want)
	}
}

// loginRecords returns the records of the login history that the audit log has gained since before, from the oldest.
func (e *mgrEnv) loginRecords(t *testing.T, before mgrMark) []audit.Record {
	t.Helper()
	return e.recordsOf(t, before, audit.LoginLog)
}

// activityRecords returns the records of the activity log that the audit log has gained since before, from the oldest.
func (e *mgrEnv) activityRecords(t *testing.T, before mgrMark) []audit.Record {
	t.Helper()
	return e.recordsOf(t, before, audit.ActivityLog)
}

func (e *mgrEnv) recordsOf(t *testing.T, before mgrMark, log audit.Log) []audit.Record {
	t.Helper()
	var got []audit.Record
	for _, r := range e.records(t, before) {
		if r.Log == log {
			got = append(got, r)
		}
	}
	return got
}

// recordName names r by its action, and its result after a slash if it has one.
func recordName(r audit.Record) string {
	if r.Result == "" {
		return string(r.Action)
	}
	return string(r.Action) + "/" + r.Result
}

// added returns the names of the records that the audit log has gained since before, from the oldest.
func (e *mgrEnv) added(t *testing.T, before mgrMark) []string {
	t.Helper()
	var names []string
	for _, r := range e.records(t, before) {
		names = append(names, recordName(r))
	}
	return names
}

// records returns the records that the audit log has gained since before, from the oldest.
func (e *mgrEnv) records(t *testing.T, before mgrMark) []audit.Record {
	t.Helper()
	var got []audit.Record
	err := e.s.ReadAudit(t.Context(), audit.Filter{}, func(r audit.Record) bool {
		if r.N > before.last[r.Log] {
			got = append(got, r)
		}
		return true
	})
	if err != nil {
		t.Fatalf("ReadAudit() = %v", err)
	}
	return got
}

// hist returns a history that adds records to tx, for the tests of the helpers that take one.
func (e *mgrEnv) hist(tx *store.Tx) *history {
	return &history{m: e.m, tx: tx}
}

// storeWrite returns f followed by the record store.write, which an Update that writes with the store's own API needs
// (store.ErrUnrecorded): the tests that make states with it are not about who wrote.
func storeWrite(f func(*store.Tx) error) func(*store.Tx) error {
	return func(tx *store.Tx) error {
		if err := f(tx); err != nil {
			return err
		}
		return tx.Record(audit.Record{Log: audit.ActivityLog, Actor: audit.System, Action: audit.StoreWrite})
	}
}

// set writes the rows with the store's own API, which makes states that the operations cannot.
func (e *mgrEnv) set(t *testing.T, rows ...row) {
	t.Helper()
	err := e.s.Update(t.Context(), storeWrite(func(tx *store.Tx) error {
		for _, r := range rows {
			data, err := encodeRow(r)
			if err != nil {
				return err
			}
			if err := tx.PutAccount(r.ID, data); err != nil {
				return err
			}
		}
		return nil
	}))
	if err != nil {
		t.Fatalf("PutAccount() = %v", err)
	}
}

// mgrRow returns the row of an active account number n, whose password hash is made of the byte n.
func mgrRow(n int, login string, role Role) row {
	return row{
		ID: content.FormatID(content.RefUser, [16]byte{0: 0x01, 15: byte(n)}), Login: login, Name: "Name of " + login,
		Email: login + "@example.com", Role: role, CreatedAt: mgrTime, Password: testPasswordHash(byte(n)), CredentialsChangedAt: mgrTime,
	}
}

func (e *mgrEnv) putAuthor(t *testing.T, id string) {
	t.Helper()
	data := `{"schemaVersion": 1, "id": "` + id + `", "slug": "` + strings.ToLower(id[len(id)-4:]) + `"}`
	err := e.s.Update(t.Context(), storeWrite(func(tx *store.Tx) error {
		_, err := tx.Put(content.RefAuthor, id, []byte(data), testCreatorID)
		return err
	}))
	if err != nil {
		t.Fatalf("Put(%s) = %v", id, err)
	}
}

// mgrAuthorID is the ID of an author profile that tests put, and mgrOtherAuthorID of another.
const (
	mgrAuthorID      = "aut_01JB8Y6T2RG7M4XK9P2D5W8NQ3"
	mgrOtherAuthorID = "aut_01JB8Y6T2RG7M4XK9P2D5W8NQ4"
)

func mgrProblems(t *testing.T, err error, want ...problem) {
	t.Helper()
	var errs content.Errors
	if !errors.As(err, &errs) {
		t.Errorf("error = %v, want the problems %v", err, want)
		return
	}
	assertProblems(t, errs, want...)
}

func mgrError(t *testing.T, err, is error, text string) {
	t.Helper()
	switch {
	case err == nil:
		t.Errorf("error = nil, want %q", text)
	case !errors.Is(err, is):
		t.Errorf("error = %q, want one that wraps %q", err, is)
	case err.Error() != text:
		t.Errorf("error = %q, want %q", err, text)
	}
}

func mgrNotFound(id string) string {
	return `accounts "` + id + `": not found`
}

func mgrLastAdmin(id, ending string) string {
	return id + " is the last active administrator: " + ending
}

func mgrNotAllowed(a Account, phrase string) string {
	return permRefusalText(a, phrase)
}

func TestNew(t *testing.T) {
	s, err := store.Create(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	m, err := New(s)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if m.st != s || m.iterations != 600000 || m.rand == nil || m.ids == nil || m.hook != nil {
		t.Errorf("New() = %+v, want the store, 600000 iterations, random bits, an ID source and no hook", m)
	}
	if got, want := cap(m.hashes), max(1, runtime.GOMAXPROCS(0)/4); got != want {
		t.Errorf("New() has %d places for hashes, want %d: a quarter of GOMAXPROCS, and one at least", got, want)
	}
	// The hash for a login that no account can use is made here, with the count of the Manager.
	if n, salt, key, err := parseHash(m.unknownHash); err != nil || n != 600000 || len(salt) != saltBytes || len(key) != keyBytes {
		t.Errorf("the hash for an unknown login has %d iterations, a salt of %d bytes and a key of %d, %v, want 600000, %d, %d, nil",
			n, len(salt), len(key), err, saltBytes, keyBytes)
	}
	before := time.Now()
	got := m.now()
	if after := time.Now(); got.Before(before) || got.After(after) {
		t.Errorf("the clock = %v, want a time between %v and %v", got, before, after)
	}
	id, err := m.ids.NewID(content.RefUser, got)
	if err != nil || content.CheckID(content.RefUser, id) != nil {
		t.Errorf("NewID() = %q, %v, want a user ID", id, err)
	}
	if _, err := m.rand.Read(make([]byte, 8)); err != nil {
		t.Errorf("reading random bits = %v", err)
	}
}

// TestManagerStore checks that Store returns the store that the Manager was made for, in which Read finds its accounts.
func TestManagerStore(t *testing.T) {
	a, b := newMgr(t), newMgr(t)
	if a.m.Store() != a.s || b.m.Store() != b.s || a.m.Store() == b.m.Store() {
		t.Errorf("Store() = %p and %p, want the stores %p and %p that the Managers were made for", a.m.Store(), b.m.Store(), a.s, b.s)
	}
	first := a.first(t)
	err := a.m.Store().View(t.Context(), func(tx *store.Tx) error {
		got, err := Read(tx, first.ID)
		if err == nil && got.ID != first.ID {
			t.Errorf("Read() = %+v, want the account %s", got, first.ID)
		}
		return err
	})
	if err != nil {
		t.Errorf("Read() in a View of Store() = %v", err)
	}
	err = b.m.Store().View(t.Context(), func(tx *store.Tx) error {
		_, err := Read(tx, first.ID)
		return err
	})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Read() in a View of the store of the other Manager = %v, want an error that wraps %v", err, fs.ErrNotExist)
	}
}

type mgrCountingReader struct {
	r io.Reader
	n atomic.Int64
}

func (c *mgrCountingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// TestNewManager checks, with a low count and known bytes, what the constructor makes: the hash for nobody is of the
// text of a token and a salt, which are the only 48 bytes that it reads, and has the count that it is given.
func TestNewManager(t *testing.T) {
	s, err := store.Create(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	counter := &mgrCountingReader{r: &pwSequence{}}
	m, err := newManager(s, 3, counter)
	if err != nil {
		t.Fatalf("newManager() = %v", err)
	}
	if m.st != s || m.iterations != 3 || m.rand != counter || m.ids == nil || m.hook != nil || m.now == nil {
		t.Errorf("newManager() = %+v, want the store, 3 iterations, the reader, an ID source, the clock and no hook", m)
	}
	if got := counter.n.Load(); got != tokenBytes+saltBytes {
		t.Errorf("newManager() read %d bytes, want %d: the bytes of a token and a salt", got, tokenBytes+saltBytes)
	}
	n, salt, _, err := parseHash(m.unknownHash)
	if err != nil || n != 3 || !bytes.Equal(salt, tokBytes(tokenBytes + saltBytes)[tokenBytes:]) {
		t.Errorf("the hash for an unknown login has %d iterations, a salt that follows the token, %v, want 3 and the salt", n, err)
	}
	// It is the hash of the text of the token that the first bytes make, which a person is never given.
	if ok, _, err := matchHash(m.unknownHash, tokSample, 3); !ok || err != nil {
		t.Errorf("matchHash() of the hash for an unknown login and the text of its token = %v, %v, want true, nil", ok, err)
	}
	if ok, _, err := matchHash(m.unknownHash, mgrPassword("nobody"), 3); ok || err != nil {
		t.Errorf("matchHash() of the hash for an unknown login and a password = %v, %v, want false, nil", ok, err)
	}
	// The other reads, of IDs, salts and tokens, go to the same reader.
	if _, err := m.ids.NewID(content.RefUser, mgrTime); err != nil || counter.n.Load() <= tokenBytes+saltBytes {
		t.Errorf("NewID() = %v after %d bytes read, want the ID source to read from the reader too", err, counter.n.Load())
	}

	// Two Managers do not share a hash for nobody.
	a, err := newManager(s, pwLow, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newManager(s, pwLow, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if a.unknownHash == b.unknownHash {
		t.Error("two Managers have the same hash for an unknown login")
	}
}

func TestNewFailures(t *testing.T) {
	s, err := store.Create(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	boom := errors.New("no random bits")
	tests := []struct {
		name string
		r    io.Reader
		is   error
		want string
	}{
		{"no random bits", iotest.ErrReader(boom), boom, "cannot read a token: no random bits"},
		{"a token that is cut", bytes.NewReader(tokBytes(31)), io.ErrUnexpectedEOF, "cannot read a token: unexpected EOF"},
		{"the salt", io.MultiReader(bytes.NewReader(tokBytes(32)), iotest.ErrReader(boom)), boom, "cannot read a salt: no random bits"},
		{"a salt that is cut", io.LimitReader(&pwSequence{}, 47), io.ErrUnexpectedEOF, "cannot read a salt: unexpected EOF"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := newManager(s, pwLow, tt.r)
			if m != nil || err == nil || err.Error() != tt.want || tt.is != nil && !errors.Is(err, tt.is) {
				t.Errorf("newManager() = %v, %v, want no Manager and the error %q", m, err, tt.want)
			}
		})
	}
}

func TestSetClock(t *testing.T) {
	e := newMgr(t)
	other := time.Date(2030, 1, 2, 3, 4, 5, 6, time.UTC)
	e.clock.set(other)
	a, _ := e.invite(t, "yamada", RoleEditor)
	if !a.CreatedAt.Equal(other) || !a.Token.ExpiresAt.Equal(other.Add(7*24*time.Hour)) {
		t.Errorf("the account = %+v, want the times of the clock %v", a, other)
	}
	e.m.SetClock(func() time.Time { return mgrTime })
	b, _ := e.invite(t, "taro", RoleEditor)
	if !b.CreatedAt.Equal(mgrTime) {
		t.Errorf("CreatedAt = %v after SetClock, want %v", b.CreatedAt, mgrTime)
	}
}

// TestActorCannotBeMade guards that only this package can make a System: an Actor has no exported field.
func TestActorCannotBeMade(t *testing.T) {
	for f := range reflect.TypeOf(Actor{}).Fields() {
		if f.IsExported() {
			t.Errorf("Actor has the exported field %s", f.Name)
		}
	}
	if By("") != (Actor{}) || By(testUserID) == (Actor{}) || By(testUserID) == System || (Actor{}) == System || By("system") == System {
		t.Error("By(), System and the zero Actor are not three different kinds of Actor, or By(\"\") is not the zero Actor")
	}
}

func TestCreateFirst(t *testing.T) {
	e := newMgr(t)
	in := Input{Login: "  Admin ", Name: "Caf" + "e" + acute, Email: "Admin@Example.COM"}
	a, err := e.m.CreateFirst(t.Context(), in, mgrPassword("admin"))
	if err != nil {
		t.Fatalf("CreateFirst() = %v", err)
	}
	want := Account{
		ID: a.ID, Login: "admin", Name: "Caf" + eAcute, Email: "Admin@example.com", Role: RoleAdmin, Status: StatusActive,
		CreatedAt: mgrTime, CredentialsChangedAt: mgrTime,
	}
	if a != want {
		t.Errorf("CreateFirst() = %+v, want %+v", a, want)
	}
	if err := content.CheckID(content.RefUser, a.ID); err != nil {
		t.Errorf("the ID = %v", err)
	}
	r := e.row(t, a.ID)
	if iterations, _, _, err := parseHash(r.Password); err != nil || iterations != pwLow {
		t.Errorf("the stored password = %d iterations, %v, want a hash of %d iterations", iterations, err, pwLow)
	}
	if r.CreatedBy != "" || !r.Token.IsZero() || r.Disabled || !r.CredentialsChangedAt.Equal(mgrTime) {
		t.Errorf("the stored row has the creator %q, a token %v, disabled %v and the credentials time %v", r.CreatedBy, !r.Token.IsZero(), r.Disabled, r.CredentialsChangedAt)
	}
	if got, err := e.m.Get(t.Context(), a.ID); err != nil || got != a {
		t.Errorf("Get() = %+v, %v, want %+v", got, err, a)
	}
	if got, err := e.m.VerifyPasswordFactor(t.Context(), "admin", mgrPassword("admin")); err != nil || got != a {
		t.Errorf("VerifyPasswordFactor() = %+v, %v, want %+v", got, err, a)
	}
}

func TestCreateFirstRole(t *testing.T) {
	for _, role := range []Role{"", RoleAdmin} {
		e := newMgr(t)
		a, err := e.m.CreateFirst(t.Context(), mgrInput("admin", role), mgrPassword("admin"))
		if err != nil || a.Role != RoleAdmin || a.Status != StatusActive {
			t.Errorf("CreateFirst() with the role %q = %+v, %v, want an active administrator", role, a, err)
		}
	}
	for _, role := range []Role{RoleEditor, RoleAuthor, RoleViewer, "boss", "Admin"} {
		e := newMgr(t)
		_, err := e.m.CreateFirst(t.Context(), mgrInput("admin", role), mgrPassword("admin"))
		mgrProblems(t, err, problem{"/role", "the first account is an administrator"})
		if rows := e.raw(t); len(rows) != 0 {
			t.Errorf("CreateFirst() with the role %q left %d rows", role, len(rows))
		}
	}
}

func TestCreateFirstProblems(t *testing.T) {
	long := mgrInput("administrator-01", RoleAdmin)
	long.Name, long.Email = "Administrator Name", "administrator@example.com"
	tests := []struct {
		name     string
		in       Input
		password string
		want     []problem
	}{
		{"nothing", Input{}, "", []problem{
			{"/login", "missing login"}, {"/name", "missing name"}, {"/email", "missing email"},
			{"/password", pwShort(0)},
		}},
		{"every field", Input{Login: "Ya mada", Name: "", Email: "yamada", Role: RoleEditor, Author: "x"}, "short", []problem{
			{"/login", badLogin("ya mada")}, {"/name", "missing name"},
			{"/email", `invalid email address "yamada": needs exactly one @`},
			{"/role", "the first account is an administrator"}, {"/author", `invalid author ID "x": ` + badFormat},
			{"/password", pwShort(5)},
		}},
		{"an unknown role", Input{Login: "a", Name: "A", Email: "a@example.com", Role: "boss"}, mgrPassword("a"), []problem{
			{"/role", "the first account is an administrator"},
		}},
		{"the role after another field", Input{Login: "Bad Login", Name: "A", Email: "a@example.com", Role: RoleEditor}, mgrPassword("a"), []problem{
			{"/login", badLogin("bad login")}, {"/role", "the first account is an administrator"},
		}},
		{"the role before the password", Input{Login: "a", Name: "A", Email: "a@example.com", Role: RoleViewer}, "short", []problem{
			{"/role", "the first account is an administrator"}, {"/password", pwShort(5)},
		}},
		{"invalid UTF-8", mgrInput("admin", ""), "invalid \xff password", []problem{{"/password", pwInvalidUTF8}}},
		{"a control character", mgrInput("admin", ""), mgrPassword("admin") + "\n", []problem{{"/password", pwControl}}},
		{"too short", mgrInput("admin", ""), strings.Repeat("ab", 7), []problem{{"/password", pwShort(14)}}},
		{"too long", mgrInput("admin", ""), strings.Repeat("ab", 128) + "c", []problem{{"/password", pwLong(257)}}},
		{"the login name", long, "administrator-01", []problem{{"/password", pwSame}}},
		{"the email address", long, "administrator@example.com", []problem{{"/password", pwSame}}},
		{"the name in another case", long, "ADMINISTRATOR NAME", []problem{{"/password", pwSame}}},
		{"one character", mgrInput("admin", ""), strings.Repeat("a", 20), []problem{{"/password", pwRepeated}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newMgr(t)
			_, err := e.m.CreateFirst(t.Context(), tt.in, tt.password)
			mgrProblems(t, err, tt.want...)
			if rows := e.raw(t); len(rows) != 0 {
				t.Errorf("CreateFirst() left %d rows", len(rows))
			}
		})
	}
}

func TestCreateFirstStoreHasAccounts(t *testing.T) {
	const text = "the store has accounts: CreateFirst needs a store without any"
	t.Run("an active account", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		before := e.mark(t)
		_, err := e.m.CreateFirst(t.Context(), mgrInput("second", RoleAdmin), mgrPassword("second"))
		if err == nil || err.Error() != text {
			t.Errorf("CreateFirst() = %v, want %q", err, text)
		}
		e.unchanged(t, before)
	})
	t.Run("a pending account", func(t *testing.T) {
		e := newMgr(t)
		e.invite(t, "yamada", RoleViewer)
		before := e.mark(t)
		_, err := e.m.CreateFirst(t.Context(), mgrInput("admin", RoleAdmin), mgrPassword("admin"))
		if err == nil || err.Error() != text {
			t.Errorf("CreateFirst() = %v, want %q", err, text)
		}
		e.unchanged(t, before)
	})
	t.Run("a disabled account", func(t *testing.T) {
		e := newMgr(t)
		a, _ := e.invite(t, "yamada", RoleViewer)
		if _, err := e.m.Disable(t.Context(), System, a.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.m.CreateFirst(t.Context(), mgrInput("admin", RoleAdmin), mgrPassword("admin")); err == nil || err.Error() != text {
			t.Errorf("CreateFirst() = %v, want %q", err, text)
		}
	})
	t.Run("the problems of the input come first", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		_, err := e.m.CreateFirst(t.Context(), Input{}, "")
		mgrProblems(t, err,
			problem{"/login", "missing login"}, problem{"/name", "missing name"}, problem{"/email", "missing email"},
			problem{"/password", pwShort(0)})
	})
	t.Run("a store that has none again", func(t *testing.T) {
		e := newMgr(t)
		a, _ := e.invite(t, "yamada", RoleViewer)
		if err := e.m.Delete(t.Context(), System, a.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.m.CreateFirst(t.Context(), mgrInput("admin", RoleAdmin), mgrPassword("admin")); err != nil {
			t.Errorf("CreateFirst() = %v, want nil", err)
		}
	})
}

func TestCreateFirstAuthor(t *testing.T) {
	e := newMgr(t)
	in := mgrInput("admin", RoleAdmin)
	in.Author = mgrAuthorID
	_, err := e.m.CreateFirst(t.Context(), in, mgrPassword("admin"))
	mgrProblems(t, err, problem{"/author", `author profile "` + mgrAuthorID + `" does not exist`})
	if rows := e.raw(t); len(rows) != 0 {
		t.Errorf("CreateFirst() left %d rows", len(rows))
	}
	e.putAuthor(t, mgrAuthorID)
	a, err := e.m.CreateFirst(t.Context(), in, mgrPassword("admin"))
	if err != nil || a.Author != mgrAuthorID {
		t.Fatalf("CreateFirst() = %+v, %v, want the link to %s", a, err, mgrAuthorID)
	}
	if got := e.row(t, a.ID).Author; got != mgrAuthorID {
		t.Errorf("the stored link = %q, want %q", got, mgrAuthorID)
	}
}

func TestCreateFirstFailures(t *testing.T) {
	boom := errors.New("no random bits")
	t.Run("the salt", func(t *testing.T) {
		e := newMgr(t)
		e.m.rand = iotest.ErrReader(boom)
		_, err := e.m.CreateFirst(t.Context(), mgrInput("admin", RoleAdmin), mgrPassword("admin"))
		if !errors.Is(err, boom) || !strings.Contains(err.Error(), "cannot read a salt") {
			t.Errorf("CreateFirst() = %v, want the error of the reader as a salt", err)
		}
		if rows := e.raw(t); len(rows) != 0 {
			t.Errorf("CreateFirst() left %d rows", len(rows))
		}
	})
	t.Run("the ID", func(t *testing.T) {
		e := newMgr(t)
		e.m.ids = content.NewIDSource(iotest.ErrReader(boom))
		_, err := e.m.CreateFirst(t.Context(), mgrInput("admin", RoleAdmin), mgrPassword("admin"))
		if !errors.Is(err, boom) {
			t.Errorf("CreateFirst() = %v, want the error of the reader", err)
		}
		if rows := e.raw(t); len(rows) != 0 {
			t.Errorf("CreateFirst() left %d rows", len(rows))
		}
	})
	t.Run("a clock before 1970", func(t *testing.T) {
		e := newMgr(t)
		e.clock.set(time.Unix(-1, 0))
		_, err := e.m.CreateFirst(t.Context(), mgrInput("admin", RoleAdmin), mgrPassword("admin"))
		if err == nil || !strings.Contains(err.Error(), "is out of range for an ID") {
			t.Errorf("CreateFirst() = %v, want an error that the time is out of range", err)
		}
	})
	t.Run("a clock in the year 10000", func(t *testing.T) {
		e := newMgr(t)
		e.clock.set(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))
		if _, err := e.m.CreateFirst(t.Context(), mgrInput("admin", RoleAdmin), mgrPassword("admin")); err == nil {
			t.Error("CreateFirst() with a clock in the year 10000 = nil, want an error that the row cannot be written")
		}
		if rows := e.raw(t); len(rows) != 0 {
			t.Errorf("CreateFirst() left %d rows", len(rows))
		}
	})
	t.Run("a canceled context", func(t *testing.T) {
		e := newMgr(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := e.m.CreateFirst(ctx, mgrInput("admin", RoleAdmin), mgrPassword("admin")); !errors.Is(err, context.Canceled) {
			t.Errorf("CreateFirst() = %v, want context.Canceled", err)
		}
		if rows := e.raw(t); len(rows) != 0 {
			t.Errorf("CreateFirst() left %d rows", len(rows))
		}
	})
}

func TestStoreErrorsAreReturned(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	var ended *store.Tx
	if err := e.s.View(t.Context(), func(tx *store.Tx) error { ended = tx; return nil }); err != nil {
		t.Fatal(err)
	}
	const text = "the transaction has ended"
	if rows, err := loadRows(ended); err == nil || err.Error() != text || rows != nil {
		t.Errorf("loadRows() = %v, %v, want an error that the transaction has ended", rows, err)
	}
	// A problem that is not that the profile is missing is not one of the input.
	err := rowProblems(ended, nil, "", "", "", mgrAuthorID, true)
	var problems content.Errors
	if err == nil || err.Error() != text || errors.As(err, &problems) {
		t.Errorf("rowProblems() = %v, want the error of the store", err)
	}
}

// TestRowProblemsOwner checks that the problem of a login name or an email address that another account has says which
// account only if it is asked to.
func TestRowProblemsOwner(t *testing.T) {
	e := newMgr(t)
	rows := []row{mgrRow(1, "yamada", RoleEditor), mgrRow(2, "taro", RoleEditor)}
	err := e.s.View(t.Context(), func(tx *store.Tx) error {
		for _, tt := range []struct {
			name   string
			showID bool
			owner  string
		}{
			{"with the ID", true, rows[0].ID},
			{"without the ID", false, "another account"},
		} {
			err := rowProblems(tx, rows, rows[1].ID, "yamada", "YAMADA@example.com", "", tt.showID)
			mgrProblems(t, err,
				problem{"/login", `login name "yamada" is already used by ` + tt.owner},
				problem{"/email", `email address "YAMADA@example.com" is already used by ` + tt.owner},
			)
			if err != nil && tt.showID != strings.Contains(err.Error(), "usr_") {
				t.Errorf("the problems %s are %q, want the ID of the account to be there = %v", tt.name, err, tt.showID)
			}
			// The account that is excluded is not another account.
			if err := rowProblems(tx, rows, rows[0].ID, "yamada", "yamada@example.com", "", tt.showID); err != nil {
				t.Errorf("rowProblems() %s with the account itself = %v, want nil", tt.name, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestInvite(t *testing.T) {
	e := newMgr(t)
	admin := e.first(t)
	e.putAuthor(t, mgrAuthorID)
	e.clock.add(time.Hour)
	now := mgrTime.Add(time.Hour)
	in := Input{Login: " Yamada.Taro ", Name: "Caf" + "e" + acute, Email: "Taro@Example.COM", Role: RoleEditor, Author: mgrAuthorID}
	a, token, err := e.m.Invite(t.Context(), By(admin.ID), in)
	if err != nil {
		t.Fatalf("Invite() = %v", err)
	}
	want := Account{
		ID: a.ID, Login: "yamada.taro", Name: "Caf" + eAcute, Email: "Taro@example.com", Role: RoleEditor, Author: mgrAuthorID,
		Status: StatusPending, CreatedAt: now, CreatedBy: admin.ID,
		Token: TokenInfo{Purpose: PurposeInvitation, ExpiresAt: now.Add(7 * 24 * time.Hour)},
	}
	if a != want {
		t.Errorf("Invite() = %+v, want %+v", a, want)
	}
	if err := content.CheckID(content.RefUser, a.ID); err != nil || a.ID == admin.ID {
		t.Errorf("the ID = %q, %v, want a new user ID", a.ID, err)
	}
	hash, ok := tokenHash(token)
	if !ok {
		t.Fatalf("the token is %d characters that tokenHash refuses", len(token))
	}
	r := e.row(t, a.ID)
	if r.Token.Hash != hash || r.Token.Purpose != PurposeInvitation || !r.Token.ExpiresAt.Equal(now.Add(7*24*time.Hour)) {
		t.Errorf("the stored token = purpose %q, expiry %v, want an invitation until %v that has the hash of the token",
			r.Token.Purpose, r.Token.ExpiresAt, now.Add(7*24*time.Hour))
	}
	if r.Password != "" || !r.CredentialsChangedAt.IsZero() || r.Disabled {
		t.Errorf("the stored row has a password %v, the credentials time %v and disabled %v, want none", r.Password != "", r.CredentialsChangedAt, r.Disabled)
	}
	if strings.Contains(e.raw(t)[a.ID], token) {
		t.Error("the stored row contains the token")
	}
	if got, err := e.m.Get(t.Context(), a.ID); err != nil || got != a {
		t.Errorf("Get() = %+v, %v, want %+v", got, err, a)
	}
	if _, err := e.m.VerifyPasswordFactor(t.Context(), "yamada.taro", token); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("VerifyPasswordFactor() with the token as a password = %v, want ErrBadCredentials", err)
	}
}

func TestInviteBySystem(t *testing.T) {
	e := newMgr(t)
	a, token, err := e.m.Invite(t.Context(), System, mgrInput("yamada", RoleViewer))
	if err != nil {
		t.Fatalf("Invite() = %v", err)
	}
	if a.CreatedBy != "" || a.Status != StatusPending || a.Role != RoleViewer || token == "" {
		t.Errorf("Invite() = %+v and a token of %d characters, want a pending viewer with no creator and a token", a, len(token))
	}
	if e.row(t, a.ID).CreatedBy != "" {
		t.Error("the stored row has a creator")
	}
}

func TestInviteRoles(t *testing.T) {
	e := newMgr(t)
	for _, role := range Roles() {
		a, _ := e.invite(t, "a-"+string(role), role)
		if a.Role != role {
			t.Errorf("Invite() with the role %q = %q", role, a.Role)
		}
	}
}

func TestInviteProblems(t *testing.T) {
	e := newMgr(t)
	e.add(t, "admin", RoleAdmin)
	yamada := e.add(t, "yamada", RoleEditor)
	pending, _ := e.invite(t, "pending", RoleViewer)
	off := e.add(t, "off", RoleViewer)
	if _, err := e.m.Disable(t.Context(), System, off.ID); err != nil {
		t.Fatal(err)
	}
	// Taro's email address has an upper case letter in its local part.
	taro := Input{Login: "taro", Name: "Taro", Email: "Taro@example.com", Role: RoleAuthor}
	if _, _, err := e.m.Invite(t.Context(), System, taro); err != nil {
		t.Fatal(err)
	}
	e.putAuthor(t, mgrAuthorID)
	deleted := "aut_01JB8Y6T2RG7M4XK9P2D5W8NQ5"
	e.putAuthor(t, deleted)
	err := e.s.Update(t.Context(), storeWrite(func(tx *store.Tx) error {
		_, err := tx.Delete(content.RefAuthor, deleted, testCreatorID)
		return err
	}))
	if err != nil {
		t.Fatal(err)
	}
	loginUsed := func(login, by string) problem {
		return problem{"/login", `login name "` + login + `" is already used by ` + by}
	}
	emailUsed := func(email, by string) problem {
		return problem{"/email", `email address "` + email + `" is already used by ` + by}
	}
	// other is an input whose email address is used by no account.
	other := func(login string) Input {
		return Input{Login: login, Name: "Name", Email: "unused@example.com", Role: RoleViewer}
	}
	with := func(in Input, f func(*Input)) Input {
		f(&in)
		return in
	}
	tests := []struct {
		name string
		in   Input
		want []problem
	}{
		{"the login name of an active account", other("yamada"), []problem{loginUsed("yamada", yamada.ID)}},
		{"the login name in upper case", other("YAMADA"), []problem{loginUsed("yamada", yamada.ID)}},
		{"the login name with spaces", other("  yamada\t"), []problem{loginUsed("yamada", yamada.ID)}},
		{"the login name of a pending account", other("pending"), []problem{loginUsed("pending", pending.ID)}},
		{"the login name of a disabled account", other("off"), []problem{loginUsed("off", off.ID)}},
		{"the email address", with(other("new"), func(in *Input) { in.Email = "yamada@example.com" }), []problem{emailUsed("yamada@example.com", yamada.ID)}},
		{"the email address in another case", with(other("new"), func(in *Input) { in.Email = "YAMADA@EXAMPLE.COM" }), []problem{emailUsed("YAMADA@example.com", yamada.ID)}},
		{"the email address, a local part in another case", with(other("new"), func(in *Input) { in.Email = "taro@example.com" }), []problem{emailUsed("taro@example.com", mgrFindID(t, e, "taro"))}},
		{"the email address of a pending account", with(other("new"), func(in *Input) { in.Email = "pending@example.com" }), []problem{emailUsed("pending@example.com", pending.ID)}},
		{"both", mgrInput("yamada", RoleViewer), []problem{loginUsed("yamada", yamada.ID), emailUsed("yamada@example.com", yamada.ID)}},
		{"an invalid field comes alone", with(mgrInput("yamada", RoleViewer), func(in *Input) { in.Name = "" }), []problem{{"/name", "missing name"}}},
		{"a role that is not one", with(other("new"), func(in *Input) { in.Role = "boss" }), []problem{{"/role", `unknown role "boss"; use admin, editor, author or viewer`}}},
		{"no role", with(other("new"), func(in *Input) { in.Role = "" }), []problem{{"/role", "missing role"}}},
		{"an author profile that is missing", with(other("new"), func(in *Input) { in.Author = mgrOtherAuthorID }), []problem{{"/author", `author profile "` + mgrOtherAuthorID + `" does not exist`}}},
		{"an author profile that is deleted", with(other("new"), func(in *Input) { in.Author = deleted }), []problem{{"/author", `author profile "` + deleted + `" does not exist`}}},
		{"an author that is not an ID", with(other("new"), func(in *Input) { in.Author = "yamada" }), []problem{{"/author", `invalid author ID "yamada": ` + badFormat}}},
		{"the login name and a missing author profile", with(other("yamada"), func(in *Input) { in.Author = mgrOtherAuthorID }), []problem{loginUsed("yamada", yamada.ID), {"/author", `author profile "` + mgrOtherAuthorID + `" does not exist`}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := e.mark(t)
			a, token, err := e.m.Invite(t.Context(), System, tt.in)
			mgrProblems(t, err, tt.want...)
			if a != (Account{}) || token != "" {
				t.Errorf("Invite() returned %+v and a token of %d characters with an error", a, len(token))
			}
			e.unchanged(t, before)
		})
	}
	// An author profile that exists is fine.
	in := other("new")
	in.Author = mgrAuthorID
	if a, _, err := e.m.Invite(t.Context(), System, in); err != nil || a.Author != mgrAuthorID {
		t.Errorf("Invite() with an author profile = %+v, %v, want the link", a, err)
	}
}

func mgrFindID(t *testing.T, e *mgrEnv, login string) string {
	t.Helper()
	a, err := e.m.Find(t.Context(), login)
	if err != nil {
		t.Fatalf("Find(%q) = %v", login, err)
	}
	return a.ID
}

func TestInviteForbiddenFirst(t *testing.T) {
	e := newMgr(t)
	e.add(t, "admin", RoleAdmin)
	editor := e.add(t, "editor", RoleEditor)
	before := e.mark(t)
	// The input is invalid and the login name is used, but an actor that may not invite learns only that.
	_, _, err := e.m.Invite(t.Context(), By(editor.ID), Input{Login: "admin"})
	mgrError(t, err, ErrForbidden, mgrNotAllowed(editor, "manage accounts"))
	e.unchanged(t, before)
}

func TestInviteIDs(t *testing.T) {
	e := newMgr(t)
	var ids []string
	for i := range 5 {
		a, _ := e.invite(t, "user-"+strconv.Itoa(i), RoleViewer)
		ids = append(ids, a.ID)
		e.clock.add(time.Duration(i) * time.Millisecond)
	}
	if !slices.IsSorted(ids) || len(slices.Compact(slices.Clone(ids))) != len(ids) {
		t.Errorf("the IDs %v are not different and in the order of the invitations", ids)
	}
	got, err := e.m.List(t.Context())
	if err != nil || len(got) != len(ids) {
		t.Fatalf("List() = %d accounts, %v, want %d", len(got), err, len(ids))
	}
	for i, a := range got {
		if a.ID != ids[i] {
			t.Errorf("List()[%d] = %s, want %s", i, a.ID, ids[i])
		}
	}
}

func TestInviteFailures(t *testing.T) {
	boom := errors.New("no random bits")
	t.Run("the token", func(t *testing.T) {
		e := newMgr(t)
		e.m.rand = iotest.ErrReader(boom)
		a, token, err := e.m.Invite(t.Context(), System, mgrInput("yamada", RoleViewer))
		if !errors.Is(err, boom) || !strings.Contains(err.Error(), "cannot read a token") || a != (Account{}) || token != "" {
			t.Errorf("Invite() = %+v and a token of %d characters, %v, want the error of the reader as a token and no account", a, len(token), err)
		}
		if rows := e.raw(t); len(rows) != 0 {
			t.Errorf("Invite() left %d rows", len(rows))
		}
	})
	t.Run("the ID", func(t *testing.T) {
		e := newMgr(t)
		e.m.ids = content.NewIDSource(iotest.ErrReader(boom))
		if _, _, err := e.m.Invite(t.Context(), System, mgrInput("yamada", RoleViewer)); !errors.Is(err, boom) {
			t.Errorf("Invite() = %v, want the error of the reader", err)
		}
		if rows := e.raw(t); len(rows) != 0 {
			t.Errorf("Invite() left %d rows", len(rows))
		}
	})
	t.Run("an ID that exists", func(t *testing.T) {
		e := newMgr(t)
		first, _ := e.invite(t, "first", RoleViewer)
		// Another Manager of the store with the same random bits makes the same ID.
		other := newMgrOn(t, e.s, e.dir)
		before := e.mark(t)
		_, _, err := other.m.Invite(t.Context(), System, mgrInput("second", RoleViewer))
		if err == nil || err.Error() != "cannot make an ID: "+first.ID+" is already used" {
			t.Errorf("Invite() = %v, want an error that %s is already used", err, first.ID)
		}
		e.unchanged(t, before)
	})
	t.Run("a clock before 1970", func(t *testing.T) {
		e := newMgr(t)
		e.clock.set(time.Unix(-1, 0))
		_, _, err := e.m.Invite(t.Context(), System, mgrInput("yamada", RoleViewer))
		if err == nil || !strings.Contains(err.Error(), "is out of range for an ID") {
			t.Errorf("Invite() = %v, want an error that the time is out of range", err)
		}
	})
	t.Run("a clock in the year 10000", func(t *testing.T) {
		e := newMgr(t)
		e.clock.set(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))
		if _, _, err := e.m.Invite(t.Context(), System, mgrInput("yamada", RoleViewer)); err == nil {
			t.Error("Invite() with a clock in the year 10000 = nil, want an error that the row cannot be written")
		}
		if rows := e.raw(t); len(rows) != 0 {
			t.Errorf("Invite() left %d rows", len(rows))
		}
	})
}

func TestGet(t *testing.T) {
	e := newMgr(t)
	a := e.first(t)
	if got, err := e.m.Get(t.Context(), a.ID); err != nil || got != a {
		t.Errorf("Get() = %+v, %v, want %+v", got, err, a)
	}
	_, err := e.m.Get(t.Context(), mgrMissingID)
	mgrError(t, err, fs.ErrNotExist, mgrNotFound(mgrMissingID))
	for _, id := range []string{"", "x", "aut_01JB8Y6T2RG7M4XK9P2D5W8NQ3", strings.ToLower(a.ID)} {
		_, err := e.m.Get(t.Context(), id)
		if err == nil || errors.Is(err, fs.ErrNotExist) || !strings.HasPrefix(err.Error(), "invalid user ID "+strconv.Quote(id)) {
			t.Errorf("Get(%q) = %v, want an error that the ID is invalid", id, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := e.m.Get(ctx, a.ID); !errors.Is(err, context.Canceled) {
		t.Errorf("Get() with a canceled context = %v, want context.Canceled", err)
	}
}

func TestList(t *testing.T) {
	e := newMgr(t)
	if got, err := e.m.List(t.Context()); err != nil || len(got) != 0 {
		t.Errorf("List() of a store without accounts = %v, %v, want none", got, err)
	}
	// The rows are put out of order, in every status.
	pending, disabled := mgrRow(3, "pending", RoleViewer), mgrRow(2, "off", RoleAuthor)
	pending.Password, pending.CredentialsChangedAt, disabled.Disabled = "", time.Time{}, true
	e.set(t, pending, mgrRow(4, "last", RoleAdmin), mgrRow(1, "first", RoleEditor), disabled)
	got, err := e.m.List(t.Context())
	if err != nil {
		t.Fatalf("List() = %v", err)
	}
	var logins []string
	var statuses []Status
	for _, a := range got {
		logins = append(logins, a.Login)
		statuses = append(statuses, a.Status)
	}
	if want := []string{"first", "off", "pending", "last"}; !slices.Equal(logins, want) {
		t.Errorf("List() = %v, want %v, in the order of the IDs", logins, want)
	}
	if want := []Status{StatusActive, StatusDisabled, StatusPending, StatusActive}; !slices.Equal(statuses, want) {
		t.Errorf("the statuses = %v, want %v", statuses, want)
	}
	for i, r := range e.rows(t) {
		if got[i] != r.account() {
			t.Errorf("List()[%d] = %+v, want %+v", i, got[i], r.account())
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := e.m.List(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("List() with a canceled context = %v, want context.Canceled", err)
	}
}

func TestFind(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	yamada := e.add(t, "yamada.taro", RoleEditor)
	pending, _ := e.invite(t, "pending", RoleViewer)
	off := e.add(t, "off", RoleViewer)
	if _, err := e.m.Disable(t.Context(), System, off.ID); err != nil {
		t.Fatal(err)
	}
	off.Status = StatusDisabled
	for _, tt := range []struct {
		login string
		want  Account
	}{
		{"yamada.taro", yamada}, {"Yamada.TARO", yamada}, {"  yamada.taro\n", yamada}, {"pending", pending}, {"off", off},
	} {
		if got, err := e.m.Find(t.Context(), tt.login); err != nil || got != tt.want {
			t.Errorf("Find(%q) = %+v, %v, want %+v", tt.login, got, err, tt.want)
		}
	}
	for _, login := range []string{"nobody", "yamada", "yamada.taro2", "", "ya mada"} {
		_, err := e.m.Find(t.Context(), login)
		mgrError(t, err, fs.ErrNotExist, "no account has the login name "+strconv.Quote(login))
	}
	// The login name in the message is the normalized one.
	_, err := e.m.Find(t.Context(), " NoBody ")
	mgrError(t, err, fs.ErrNotExist, `no account has the login name "nobody"`)
	// The email address is not a login name.
	if _, err := e.m.Find(t.Context(), "yamada.taro@example.com"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Find() of an email address = %v, want fs.ErrNotExist", err)
	}
}

func TestRead(t *testing.T) {
	e := newMgr(t)
	a := e.first(t)
	t.Run("in a View", func(t *testing.T) {
		err := e.s.View(t.Context(), func(tx *store.Tx) error {
			if got, err := Read(tx, a.ID); err != nil || got != a {
				t.Errorf("Read() = %+v, %v, want %+v", got, err, a)
			}
			_, err := Read(tx, mgrMissingID)
			mgrError(t, err, fs.ErrNotExist, mgrNotFound(mgrMissingID))
			if _, err := Read(tx, "x"); err == nil || errors.Is(err, fs.ErrNotExist) {
				t.Errorf(`Read("x") = %v, want an error that the ID is invalid`, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("in an Update that checks an account", func(t *testing.T) {
		// What a save operation of #48 does: read the account and check its rights in the Update that writes.
		err := e.s.Update(t.Context(), func(tx *store.Tx) error {
			got, err := Read(tx, a.ID)
			if err != nil {
				return err
			}
			return Check(Principal{Account: got}, EditSettings, Target{})
		})
		if err != nil {
			t.Errorf("Update() = %v, want the administrator to be allowed", err)
		}
	})
	t.Run("sees the writes of its transaction", func(t *testing.T) {
		r := mgrRow(9, "inside", RoleViewer)
		err := e.s.Update(t.Context(), func(tx *store.Tx) error {
			data, err := encodeRow(r)
			if err != nil {
				return err
			}
			if err := tx.PutAccount(r.ID, data); err != nil {
				return err
			}
			got, err := Read(tx, r.ID)
			if err != nil || got != r.account() {
				t.Errorf("Read() of a row put in the same Update = %+v, %v, want %+v", got, err, r.account())
			}
			return errors.New("roll back")
		})
		if err == nil || err.Error() != "roll back" {
			t.Fatalf("Update() = %v", err)
		}
		if _, err := e.m.Get(t.Context(), r.ID); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Get() after a rolled back Update = %v, want fs.ErrNotExist", err)
		}
	})
	t.Run("a row that does not decode", func(t *testing.T) {
		bad := mgrRow(8, "bad", RoleViewer).ID
		err := e.s.Update(t.Context(), func(tx *store.Tx) error {
			if err := tx.PutAccount(bad, []byte("not json")); err != nil {
				return err
			}
			_, err := Read(tx, bad)
			if err == nil || err.Error() != invalidValue(bad) {
				t.Errorf("Read() = %v, want %q", err, invalidValue(bad))
			}
			return errors.New("roll back")
		})
		if err == nil || err.Error() != "roll back" {
			t.Fatalf("Update() = %v", err)
		}
	})
	t.Run("a transaction that has ended", func(t *testing.T) {
		var ended *store.Tx
		if err := e.s.View(t.Context(), func(tx *store.Tx) error { ended = tx; return nil }); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(ended, a.ID); err == nil || err.Error() != "the transaction has ended" {
			t.Errorf("Read() = %v, want an error that the transaction has ended", err)
		}
	})
}

// mgrOperation is an operation of the Manager with the arguments that a test has chosen for it.
type mgrOperation struct {
	name string
	do   func() error
}

// writes lists the operations that write, done by by, in a store that has the administrator admin and the pending
// account pending, whose token is token. Run once in this order, they all work; tag keeps their login names apart.
func (e *mgrEnv) writes(t *testing.T, by Actor, admin, pending Account, token, tag string) []mgrOperation {
	ctx := t.Context()
	return []mgrOperation{
		{"CreateFirst", func() error {
			_, err := e.m.CreateFirst(ctx, mgrInput("first-"+tag, RoleAdmin), mgrPassword("first-"+tag))
			return err
		}},
		{"Invite", func() error {
			_, _, err := e.m.Invite(ctx, by, mgrInput("other-"+tag, RoleViewer))
			return err
		}},
		{"SetPassword", func() error {
			_, err := e.m.SetPassword(ctx, mgrClient, token, mgrPassword("yamada"))
			return err
		}},
		{"Reset", func() error { _, err := e.m.Reset(ctx, by, pending.ID); return err }},
		{"ChangePassword", func() error {
			return e.m.ChangePassword(ctx, admin.ID, mgrPassword("admin"), mgrPassword("changed-"+tag))
		}},
		{"Update", func() error {
			_, err := e.m.Update(ctx, by, pending.ID, Change{Name: ptr("New name")})
			return err
		}},
		{"Disable", func() error { _, err := e.m.Disable(ctx, by, pending.ID); return err }},
		{"Enable", func() error { _, err := e.m.Enable(ctx, by, pending.ID); return err }},
		{"Delete", func() error { return e.m.Delete(ctx, by, pending.ID) }},
	}
}

// putRaw puts data as the row of the account id as it is, with the store's own API, which does not look at it.
func (e *mgrEnv) putRaw(t *testing.T, id, data string) {
	t.Helper()
	err := e.s.Update(t.Context(), storeWrite(func(tx *store.Tx) error { return tx.PutAccount(id, []byte(data)) }))
	if err != nil {
		t.Fatalf("PutAccount() = %v", err)
	}
}

// TestCorruptRow checks a row that does not decode, in each way that it can fail the stored format: the look-ups of a
// login name skip it, the operations that write and the reads of all accounts fail, and System can remove it.
func TestCorruptRow(t *testing.T) {
	bad := mgrRow(7, "bad", RoleViewer).ID
	unreadablePassword := mgrRow(7, "broken", RoleViewer)
	unreadablePassword.Password = "not a password hash"
	// A key that a build with another kind of credential would write.
	later := jsonOf(t, mgrRow(7, "later", RoleViewer))
	later = later[:len(later)-1] + `,"passkeys":[]}`
	kinds := []struct{ name, login, data string }{
		{"data that is not JSON", "", "not json"},
		{"a password that is not a hash", "broken", jsonOf(t, unreadablePassword)},
		{"a key that this build does not know", "later", later},
	}
	for _, kind := range kinds {
		for _, actor := range []string{"an administrator", "System"} {
			t.Run(kind.name+" by "+actor, func(t *testing.T) {
				e := newMgr(t)
				ctx := t.Context()
				admin := e.first(t)
				pending, token := e.invite(t, "yamada", RoleEditor)
				by := System
				if actor == "an administrator" {
					by = By(admin.ID)
				}
				e.putRaw(t, bad, kind.data)
				before := e.mark(t)
				want := invalidValue(bad)

				// What looks for one login name skips the row, and what reads another account does not need it.
				if got, err := e.m.VerifyPasswordFactor(ctx, "admin", mgrPassword("admin")); err != nil || got != admin {
					t.Errorf("VerifyPasswordFactor() of a good row = %+v, %v, want %+v", got, err, admin)
				}
				if got, err := e.m.Find(ctx, "yamada"); err != nil || got != pending {
					t.Errorf("Find() of a good row = %+v, %v, want %+v", got, err, pending)
				}
				if got, err := e.m.Get(ctx, admin.ID); err != nil || got != admin {
					t.Errorf("Get() of a good row = %+v, %v, want %+v", got, err, admin)
				}
				// The login name of the row is the login name of no account, and a hash is checked for it as for any.
				if kind.login != "" {
					verified := 0
					e.m.hook = func(point string) {
						if point == "verified" {
							verified++
						}
					}
					got, err := e.m.VerifyPasswordFactor(ctx, kind.login, mgrPassword(kind.login))
					if !errors.Is(err, ErrBadCredentials) || got != (Account{}) || verified != 1 {
						t.Errorf("VerifyPasswordFactor() of the login name of the row = %+v, %v after %d checks, want ErrBadCredentials after 1", got, err, verified)
					}
					e.m.hook = nil
					mgrError(t, mgrErr(e.m.Find(ctx, kind.login)), fs.ErrNotExist, "no account has the login name "+strconv.Quote(kind.login))
				}

				// Everything that writes needs every row, and so do List and the reads of the row itself.
				refused := e.writes(t, by, admin, pending, token, "before")
				refused = append(refused,
					mgrOperation{"List", func() error { _, err := e.m.List(ctx); return err }},
					mgrOperation{"Get of the row", func() error { _, err := e.m.Get(ctx, bad); return err }},
					mgrOperation{"Read of the row", func() error {
						return e.s.View(ctx, func(tx *store.Tx) error { _, err := Read(tx, bad); return err })
					}},
					mgrOperation{"Delete of the row by an administrator", func() error { return e.m.Delete(ctx, By(admin.ID), bad) }},
					mgrOperation{"Delete of an account that does not exist by System", func() error { return e.m.Delete(ctx, System, mgrMissingID) }},
				)
				for _, op := range refused {
					if err := op.do(); err == nil || err.Error() != want {
						t.Errorf("%s() = %v, want %q", op.name, err, want)
					}
				}
				e.unchanged(t, before)

				// System removes the row, and nothing else changes.
				if err := e.m.Delete(ctx, System, bad); err != nil {
					t.Fatalf("Delete() of the row by System = %v", err)
				}
				after := e.raw(t)
				if _, ok := after[bad]; ok {
					t.Error("the row is still in the store")
				}
				for id, data := range before.rows {
					if id != bad && after[id] != data {
						t.Errorf("the row of %s changed", id)
					}
				}
				_, err := e.m.Get(ctx, bad)
				mgrError(t, err, fs.ErrNotExist, mgrNotFound(bad))

				// Then every operation works.
				for _, op := range e.writes(t, by, admin, pending, token, "after") {
					err := op.do()
					switch {
					case op.name == "CreateFirst":
						if err == nil || err.Error() != "the store has accounts: CreateFirst needs a store without any" {
							t.Errorf("CreateFirst() = %v, want the error that the store has accounts", err)
						}
					case err != nil:
						t.Errorf("%s() = %v after the row was removed, want nil", op.name, err)
					}
				}
				if list, err := e.m.List(ctx); err != nil || len(list) != 2 {
					t.Errorf("List() after the row was removed = %d accounts, %v, want the administrator and the invited account", len(list), err)
				}
			})
		}
	}
}

// TestCorruptRowRepair checks how System clears rows that do not decode: each on its own, however many there are, and
// without the rule of the last active administrator, which cannot count them. A row that can be read is removed only
// when every row can be read.
func TestCorruptRowRepair(t *testing.T) {
	bad, other, third := mgrRow(7, "bad", RoleViewer).ID, mgrRow(8, "other", RoleViewer).ID, mgrRow(9, "third", RoleViewer).ID
	t.Run("the only row", func(t *testing.T) {
		e := newMgr(t)
		e.putRaw(t, bad, "not json")
		if err := e.m.Delete(t.Context(), System, bad); err != nil {
			t.Fatalf("Delete() = %v", err)
		}
		if rows := e.raw(t); len(rows) != 0 {
			t.Errorf("the store has %d rows after the removal, want none", len(rows))
		}
		e.first(t)
	})
	t.Run("a row that might have been the last administrator", func(t *testing.T) {
		e := newMgr(t)
		root := mgrRow(1, "root", RoleAdmin)
		root.Password = "not a password hash"
		editor := mgrRow(2, "editor", RoleEditor)
		e.set(t, root, editor)
		// The rule of the last active administrator cannot count a row that cannot be read, before or after.
		if err := e.m.Delete(t.Context(), System, root.ID); err != nil {
			t.Fatalf("Delete() = %v", err)
		}
		if _, err := e.m.Get(t.Context(), editor.ID); err != nil {
			t.Errorf("Get() of the other row = %v, want nil", err)
		}
	})
	t.Run("a good administrator is still the last one", func(t *testing.T) {
		e := newMgr(t)
		admin := e.first(t)
		e.putRaw(t, bad, "not json")
		// The row that cannot be read is in the way first.
		if err := e.m.Delete(t.Context(), System, admin.ID); err == nil || err.Error() != invalidValue(bad) {
			t.Errorf("Delete() of the administrator = %v, want %q", err, invalidValue(bad))
		}
		if err := e.m.Delete(t.Context(), System, bad); err != nil {
			t.Fatalf("Delete() = %v", err)
		}
		mgrError(t, e.m.Delete(t.Context(), System, admin.ID), ErrLastAdmin, mgrLastAdmin(admin.ID, "it cannot be deleted"))
	})
	t.Run("a row that can be read waits for the others", func(t *testing.T) {
		e := newMgr(t)
		ctx := t.Context()
		admin := e.first(t)
		editor := e.add(t, "editor", RoleEditor)
		e.putRaw(t, bad, "not json")
		before := e.mark(t)
		for name, by := range map[string]Actor{"an administrator": By(admin.ID), "System": System} {
			if err := e.m.Delete(ctx, by, editor.ID); err == nil || err.Error() != invalidValue(bad) {
				t.Errorf("Delete() of an account that can be read by %s = %v, want %q", name, err, invalidValue(bad))
			}
		}
		e.unchanged(t, before)
		if err := e.m.Delete(ctx, System, bad); err != nil {
			t.Fatalf("Delete() = %v", err)
		}
		if err := e.m.Delete(ctx, By(admin.ID), editor.ID); err != nil {
			t.Errorf("Delete() of an account that can be read, once every row can be = %v, want nil", err)
		}
	})
	t.Run("an ID that is not one", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		mgrError(t, e.m.Delete(t.Context(), System, "x"), fs.ErrNotExist, mgrNotFound("x"))
		// The row that cannot be read is in the way first.
		e.putRaw(t, bad, "not json")
		if err := e.m.Delete(t.Context(), System, "x"); err == nil || err.Error() != invalidValue(bad) {
			t.Errorf("Delete() = %v, want %q", err, invalidValue(bad))
		}
	})
	t.Run("rows one after another", func(t *testing.T) {
		// Two and three rows that cannot be read, removed in the order of the IDs and in other orders.
		for _, order := range [][]string{{bad, other}, {other, bad}, {bad, other, third}, {other, third, bad}, {third, other, bad}} {
			e := newMgr(t)
			ctx := t.Context()
			admin := e.first(t)
			for _, id := range order {
				e.putRaw(t, id, "not json")
			}
			remaining := slices.Clone(order)
			for _, id := range order {
				// The first of the rows that remain, in the order of the IDs, is the one that is in the way.
				inTheWay := invalidValue(slices.Min(remaining))
				if err := e.m.Delete(ctx, System, admin.ID); err == nil || err.Error() != inTheWay {
					t.Errorf("Delete() of the administrator with %d rows that cannot be read = %v, want %q", len(remaining), err, inTheWay)
				}
				// Only System clears a row, and it leaves the other rows as they are.
				if err := e.m.Delete(ctx, By(admin.ID), id); err == nil || err.Error() != inTheWay {
					t.Errorf("Delete() of a row that cannot be read by an administrator = %v, want %q", err, inTheWay)
				}
				others := e.raw(t)
				delete(others, id)
				if err := e.m.Delete(ctx, System, id); err != nil {
					t.Fatalf("Delete() of %s, one of %d rows that cannot be read = %v", id, len(remaining), err)
				}
				if got := e.raw(t); !maps.Equal(got, others) {
					t.Errorf("the rows after the removal of %s are not the other rows as they were", id)
				}
				remaining = slices.DeleteFunc(remaining, func(r string) bool { return r == id })
			}
			// Then the store works again.
			if list, err := e.m.List(ctx); err != nil || len(list) != 1 || list[0] != admin {
				t.Errorf("List() = %+v, %v, want the administrator", list, err)
			}
			mgrError(t, e.m.Delete(ctx, System, admin.ID), ErrLastAdmin, mgrLastAdmin(admin.ID, "it cannot be deleted"))
			e.invite(t, "yamada", RoleEditor)
		}
	})
}

// TestRemoveUnreadable checks the removal of the row of one account that does not decode, the first step of Delete by
// System: it removes that row and no other, reads no other row, and leaves a row that can be read, or none, alone.
func TestRemoveUnreadable(t *testing.T) {
	good := mgrRow(1, "a", RoleEditor)
	bad, other := mgrRow(2, "b", RoleViewer).ID, mgrRow(4, "d", RoleViewer).ID
	tests := []struct {
		name    string
		id      string
		removed bool
	}{
		{"a row that cannot be read", bad, true},
		{"another row that cannot be read", other, true},
		{"a row that can be read", good.ID, false},
		{"a row that does not exist", mgrMissingID, false},
		{"an ID that is not one", "x", false},
		{"an empty ID", "", false},
		{"an ID of another kind", testAuthorID, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newMgr(t)
			e.set(t, good)
			e.putRaw(t, bad, "not json")
			e.putRaw(t, other, "not json")
			want := e.raw(t)
			var removed bool
			err := e.s.Update(t.Context(), storeWrite(func(tx *store.Tx) error {
				var err error
				removed, err = removeUnreadable(tx, tt.id)
				return err
			}))
			if err != nil || removed != tt.removed {
				t.Errorf("removeUnreadable() = %v, %v, want %v, nil", removed, err, tt.removed)
			}
			if tt.removed {
				delete(want, tt.id)
			}
			if got := e.raw(t); !maps.Equal(got, want) {
				t.Errorf("the rows after removeUnreadable() are not the expected ones: %d rows, want %d", len(got), len(want))
			}
		})
	}
	t.Run("the errors of the store", func(t *testing.T) {
		e := newMgr(t)
		e.putRaw(t, bad, "not json")
		var ended *store.Tx
		if err := e.s.View(t.Context(), func(tx *store.Tx) error { ended = tx; return nil }); err != nil {
			t.Fatal(err)
		}
		const text = "the transaction has ended"
		if removed, err := removeUnreadable(ended, bad); removed || err == nil || err.Error() != text {
			t.Errorf("removeUnreadable() of a transaction that has ended = %v, %v, want false and the error %q", removed, err, text)
		}
		// A transaction that cannot write does not remove the row, and says so.
		err := e.s.View(t.Context(), func(tx *store.Tx) error {
			removed, err := removeUnreadable(tx, bad)
			if removed || err == nil {
				t.Errorf("removeUnreadable() in a read-only transaction = %v, %v, want false and an error", removed, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := e.raw(t)[bad]; !ok {
			t.Error("the row is gone")
		}
	})
}

func TestRowLoaders(t *testing.T) {
	e := newMgr(t)
	a, c := mgrRow(1, "a", RoleEditor), mgrRow(3, "c", RoleViewer)
	e.set(t, a, c)
	bad, other := mgrRow(2, "b", RoleViewer).ID, mgrRow(4, "d", RoleViewer).ID
	loaders := []struct {
		name string
		load func(tx *store.Tx) ([]row, error)
	}{{"loadRows", loadRows}, {"readableRows", readableRows}}
	// check loads the rows by each loader in a View, and compares the logins that it returns, or the text of its error,
	// "" for none, with the expected ones; wantText is by loader.
	check := func(what string, tx *store.Tx, wantLogins []string, wantText map[string]string) {
		t.Helper()
		for _, l := range loaders {
			rows, err := l.load(tx)
			var got []string
			for _, r := range rows {
				got = append(got, r.Login)
			}
			switch text := wantText[l.name]; {
			case text != "" && (err == nil || err.Error() != text || rows != nil):
				t.Errorf("%s: %s() = %v, %v, want the error %q and no rows", what, l.name, got, err, text)
			case text == "" && (err != nil || !slices.Equal(got, wantLogins)):
				t.Errorf("%s: %s() = %v, %v, want %v", what, l.name, got, err, wantLogins)
			}
		}
	}
	view := func(f func(tx *store.Tx)) {
		t.Helper()
		if err := e.s.View(t.Context(), func(tx *store.Tx) error { f(tx); return nil }); err != nil {
			t.Fatal(err)
		}
	}

	view(func(tx *store.Tx) {
		check("every row can be read", tx, []string{"a", "c"}, nil)
	})
	e.putRaw(t, bad, "not json")
	view(func(tx *store.Tx) {
		// readableRows leaves the row out; loadRows fails with it.
		check("a row that cannot be read", tx, []string{"a", "c"}, map[string]string{"loadRows": invalidValue(bad)})
	})
	e.putRaw(t, other, "not json")
	view(func(tx *store.Tx) {
		check("two rows that cannot be read", tx, []string{"a", "c"}, map[string]string{"loadRows": invalidValue(bad)})
	})

	// The errors of the store are not those of a row, and skip nothing.
	var ended *store.Tx
	if err := e.s.View(t.Context(), func(tx *store.Tx) error { ended = tx; return nil }); err != nil {
		t.Fatal(err)
	}
	const text = "the transaction has ended"
	check("a transaction that has ended", ended, nil, map[string]string{"loadRows": text, "readableRows": text})
}

// TestUnreadableRowDoesNotHideAnAccount checks that a row that cannot be read is not an account that has its login name:
// the account that has the login name logs in and is found.
func TestUnreadableRowDoesNotHideAnAccount(t *testing.T) {
	e := newMgr(t)
	ctx := t.Context()
	good := mgrActiveRow(t, 5, "twin", RoleEditor, mgrPassword("twin"))
	twin := mgrRow(4, "twin", RoleViewer) // sorts before the good row
	twin.Password = "not a password hash"
	e.set(t, twin, good)
	if got, err := e.m.Find(ctx, "twin"); err != nil || got != good.account() {
		t.Errorf("Find() = %+v, %v, want %+v", got, err, good.account())
	}
	if got, err := e.m.VerifyPasswordFactor(ctx, "twin", mgrPassword("twin")); err != nil || got != good.account() {
		t.Errorf("VerifyPasswordFactor() = %+v, %v, want %+v", got, err, good.account())
	}
}

// TestRowFromOlderUnicode checks a row whose name a build with older Unicode tables wrote, in NFC for those tables and
// not for these: the row is read and every operation works. Checking the name again when a row is read would stop every
// account until the row was removed.
func TestRowFromOlderUnicode(t *testing.T) {
	// U+1ADD was not assigned in Unicode 16.0 and is a mark of class 220 since 17.0.
	name := "q" + acute + string(rune(0x1add)) + " Yamada"
	if nfc.IsNormalized(name) {
		t.Fatalf("%q is in NFC, want a name that these tables change", name)
	}
	e := newMgr(t)
	ctx := t.Context()
	admin := e.first(t)
	old := mgrActiveRow(t, 9, "yamada", RoleEditor, mgrPassword("yamada"))
	old.Name = name
	e.set(t, old)
	account := old.account()
	storedName := func() string { return e.row(t, old.ID).Name }

	if got, err := e.m.VerifyPasswordFactor(ctx, "admin", mgrPassword("admin")); err != nil || got != admin {
		t.Errorf("VerifyPasswordFactor() of the administrator = %+v, %v, want %+v", got, err, admin)
	}
	if got, err := e.m.VerifyPasswordFactor(ctx, "yamada", mgrPassword("yamada")); err != nil || got != account {
		t.Errorf("VerifyPasswordFactor() of the account = %+v, %v, want %+v", got, err, account)
	}
	if got, err := e.m.Get(ctx, old.ID); err != nil || got != account || got.Name != name {
		t.Errorf("Get() = %+v, %v, want %+v", got, err, account)
	}
	if got, err := e.m.Find(ctx, "yamada"); err != nil || got != account {
		t.Errorf("Find() = %+v, %v, want %+v", got, err, account)
	}
	if list, err := e.m.List(ctx); err != nil || len(list) != 2 || !slices.Contains(list, account) || !slices.Contains(list, admin) {
		t.Errorf("List() = %+v, %v, want the administrator and the account", list, err)
	}

	// The operations write the row back with the name as it is stored.
	steps := []struct {
		name string
		do   func() error
	}{
		{"Update of another field by System", func() error {
			_, err := e.m.Update(ctx, System, old.ID, Change{Email: ptr("yamada.new@example.com")})
			return err
		}},
		{"Update of another field by the account", func() error {
			_, err := e.m.Update(ctx, By(old.ID), old.ID, Change{Email: ptr("yamada.newer@example.com")})
			return err
		}},
		{"Disable", func() error { _, err := e.m.Disable(ctx, By(admin.ID), old.ID); return err }},
		{"Enable", func() error { _, err := e.m.Enable(ctx, By(admin.ID), old.ID); return err }},
		{"ChangePassword", func() error {
			return e.m.ChangePassword(ctx, old.ID, mgrPassword("yamada"), mgrPassword("yamada the second"))
		}},
		{"Invite", func() error { _, _, err := e.m.Invite(ctx, By(admin.ID), mgrInput("other", RoleViewer)); return err }},
	}
	for _, step := range steps {
		if err := step.do(); err != nil {
			t.Errorf("%s = %v, want nil", step.name, err)
		}
		if got := storedName(); got != name {
			t.Errorf("after %s the stored name is %q, want it as it was", step.name, got)
		}
	}
	token, err := e.m.Reset(ctx, System, old.ID)
	if err != nil {
		t.Fatalf("Reset() = %v", err)
	}
	if a, err := e.m.SetPassword(ctx, mgrClient, token, mgrPassword("yamada the third")); err != nil || a.Name != name || a.Status != StatusActive {
		t.Errorf("SetPassword() = %+v, %v, want the account, active, with the name as it was stored", a, err)
	}
	if got := storedName(); got != name {
		t.Errorf("after Reset and SetPassword the stored name is %q, want it as it was", got)
	}

	// A name that is written is normalized with these tables, and checked.
	if a, err := e.m.Update(ctx, System, old.ID, Change{Name: &name}); err != nil || a.Name != nfc.Normalize(name) {
		t.Errorf("Update() of the name = %+v, %v, want the name in NFC for these tables", a, err)
	}
	if a, err := e.m.Update(ctx, System, old.ID, Change{Name: ptr("Yamada Taro")}); err != nil || a.Name != "Yamada Taro" {
		t.Errorf("Update() of the name = %+v, %v, want the new name", a, err)
	}
	if err := e.m.Delete(ctx, System, old.ID); err != nil {
		t.Errorf("Delete() = %v, want nil", err)
	}
}

// TestRowWithUnassignedName checks a name with a code point that is not assigned, as a build without the rule for it
// stored it: it is read, every operation works, and the name cannot be written again.
func TestRowWithUnassignedName(t *testing.T) {
	x := unassignedFrom(t, 0x1ade)
	name := "q" + acute + string(x) + " Yamada"
	e := newMgr(t)
	ctx := t.Context()
	e.first(t)
	old := mgrActiveRow(t, 9, "yamada", RoleEditor, mgrPassword("yamada"))
	old.Name = name
	e.set(t, old)
	if got, err := e.m.VerifyPasswordFactor(ctx, "yamada", mgrPassword("yamada")); err != nil || got != old.account() {
		t.Errorf("VerifyPasswordFactor() = %+v, %v, want %+v", got, err, old.account())
	}
	if a, err := e.m.Update(ctx, By(old.ID), old.ID, Change{Email: ptr("yamada.new@example.com")}); err != nil || a.Name != name {
		t.Errorf("Update() of another field = %+v, %v, want the name as it was stored", a, err)
	}
	if err := e.m.ChangePassword(ctx, old.ID, mgrPassword("yamada"), mgrPassword("yamada the second")); err != nil {
		t.Errorf("ChangePassword() = %v, want nil", err)
	}
	// Writing the name again is writing a name.
	before := e.mark(t)
	_, err := e.m.Update(ctx, By(old.ID), old.ID, Change{Name: &name})
	mgrProblems(t, err, problem{"/name", notAssigned(x)})
	e.unchanged(t, before)
}

// TestUnassignedCodePoints checks that a code point that unicode.Version has not assigned is refused where a name or a
// password is written, with the message of the rule, and that nothing is written.
func TestUnassignedCodePoints(t *testing.T) {
	x := unassignedFrom(t, 0x1ade)
	name := "q" + acute + string(x) + " Yamada"
	password := mgrPassword("yamada") + string(x)
	badName, badPassword := problem{"/name", notAssigned(x)}, problem{"/password", pwUnassigned}
	// hashed counts the passwords that were hashed: none is for a password that is refused.
	count := func(e *mgrEnv) *int {
		hashed := 0
		e.m.hook = func(point string) {
			if point == "hashed" {
				hashed++
			}
		}
		return &hashed
	}

	t.Run("CreateFirst", func(t *testing.T) {
		withName := Input{Login: "admin", Name: name, Email: "admin@example.com", Role: RoleAdmin}
		for _, tt := range []struct {
			name     string
			in       Input
			password string
			want     []problem
		}{
			{"a name", withName, mgrPassword("admin"), []problem{badName}},
			{"a password", mgrInput("admin", RoleAdmin), password, []problem{badPassword}},
			{"a name and a password", withName, password, []problem{badName, badPassword}},
		} {
			e := newMgr(t)
			_, err := e.m.CreateFirst(t.Context(), tt.in, tt.password)
			mgrProblems(t, err, tt.want...)
			if rows := e.raw(t); len(rows) != 0 {
				t.Errorf("CreateFirst() with %s left %d rows", tt.name, len(rows))
			}
		}
	})
	t.Run("Invite", func(t *testing.T) {
		e := newMgr(t)
		admin := e.first(t)
		before := e.mark(t)
		in := mgrInput("yamada", RoleEditor)
		in.Name = name
		for _, by := range []Actor{By(admin.ID), System} {
			a, token, err := e.m.Invite(t.Context(), by, in)
			mgrProblems(t, err, badName)
			if a != (Account{}) || token != "" {
				t.Errorf("Invite() returned %+v and a token of %d characters with an error", a, len(token))
			}
		}
		e.unchanged(t, before)
	})
	t.Run("Update", func(t *testing.T) {
		e := newMgr(t)
		admin := e.first(t)
		yamada := e.add(t, "yamada", RoleEditor)
		before := e.mark(t)
		for _, by := range []Actor{By(admin.ID), System, By(yamada.ID)} {
			a, err := e.m.Update(t.Context(), by, yamada.ID, Change{Name: &name, Email: ptr("new@example.com")})
			mgrProblems(t, err, badName)
			if a != (Account{}) {
				t.Errorf("Update() returned %+v with an error", a)
			}
		}
		e.unchanged(t, before)
		// The same name with an assigned code point is fine.
		fine := strings.ReplaceAll(name, string(x), "x")
		if a, err := e.m.Update(t.Context(), By(yamada.ID), yamada.ID, Change{Name: &fine}); err != nil || a.Name != fine {
			t.Errorf("Update() with an assigned code point = %+v, %v, want the new name", a, err)
		}
	})
	t.Run("SetPassword", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		_, token := e.invite(t, "yamada", RoleEditor)
		hashed := count(e)
		before := e.mark(t)
		a, err := e.m.SetPassword(t.Context(), mgrClient, token, password)
		mgrProblems(t, err, badPassword)
		if a != (Account{}) || *hashed != 0 {
			t.Errorf("SetPassword() returned %+v and hashed %d passwords with an error, want none", a, *hashed)
		}
		// The token is as it was.
		e.unchanged(t, before)
		if a, err := e.m.SetPassword(t.Context(), mgrClient, token, mgrPassword("yamada")); err != nil || a.Status != StatusActive {
			t.Errorf("SetPassword() with a password that passes = %+v, %v, want an active account", a, err)
		}
	})
	t.Run("ChangePassword", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		yamada := e.add(t, "yamada", RoleEditor)
		hashed := count(e)
		before := e.mark(t)
		mgrProblems(t, e.m.ChangePassword(t.Context(), yamada.ID, mgrPassword("yamada"), password), badPassword)
		if *hashed != 0 {
			t.Errorf("%d passwords were hashed for a password that is refused, want none", *hashed)
		}
		e.unchanged(t, before)
		if _, err := e.m.VerifyPasswordFactor(t.Context(), "yamada", mgrPassword("yamada")); err != nil {
			t.Errorf("VerifyPasswordFactor() with the old password = %v, want nil", err)
		}
	})
	t.Run("a password that is verified", func(t *testing.T) {
		// Only writing a password is subject to the rule: a typed password is checked against its hash as it is.
		e := newMgr(t)
		e.first(t)
		yamada := e.add(t, "yamada", RoleEditor)
		r := e.row(t, yamada.ID)
		r.Password = mgrHashed(t, password)
		e.set(t, r)
		if got, err := e.m.VerifyPasswordFactor(t.Context(), "yamada", password); err != nil || got != r.account() {
			t.Errorf("VerifyPasswordFactor() with a password that has an unassigned code point = %+v, %v, want %+v", got, err, r.account())
		}
		if ok, _, err := matchHash(r.Password, password, pwLow); !ok || err != nil {
			t.Errorf("matchHash() = %v, %v, want true, nil", ok, err)
		}
	})
}

// TestRowsOfAnotherBuild checks that the rules that read a row work with a row that has values that the rules for
// writing refuse, and that is in another form: a role that is not one of ours, a token of a disabled account, and
// times in another zone.
func TestRowsOfAnotherBuild(t *testing.T) {
	ctx := t.Context()
	e := newMgr(t)
	admin := e.first(t)

	t.Run("a role that is not known fails closed", func(t *testing.T) {
		moderator := mgrActiveRow(t, 5, "moderator", "moderator", mgrPassword("moderator"))
		e.set(t, moderator)
		account := moderator.account()
		if got, err := e.m.VerifyPasswordFactor(ctx, "moderator", mgrPassword("moderator")); err != nil || got != account {
			t.Errorf("VerifyPasswordFactor() = %+v, %v, want %+v", got, err, account)
		}
		before := e.mark(t)
		_, _, err := e.m.Invite(ctx, By(moderator.ID), mgrInput("other", RoleViewer))
		mgrError(t, err, ErrForbidden, mgrNotAllowed(account, "manage accounts"))
		_, err = e.m.Update(ctx, By(moderator.ID), moderator.ID, Change{Name: ptr("Moderator")})
		mgrError(t, err, ErrForbidden, mgrNotAllowed(account, "edit its own account"))
		e.unchanged(t, before)
		// It does not count as an administrator: the administrator is still the last one.
		mgrError(t, e.m.Delete(ctx, System, admin.ID), ErrLastAdmin, mgrLastAdmin(admin.ID, "it cannot be deleted"))
		// Another field changes, and the role stays as it is read.
		if a, err := e.m.Update(ctx, By(admin.ID), moderator.ID, Change{Name: ptr("Moderator")}); err != nil || a.Name != "Moderator" || a.Role != "moderator" {
			t.Errorf("Update() of another field = %+v, %v, want the new name and the role as it was", a, err)
		}
		// An administrator gives it a role that is known.
		if a, err := e.m.Update(ctx, By(admin.ID), moderator.ID, Change{Role: ptr(RoleEditor)}); err != nil || a.Role != RoleEditor {
			t.Errorf("Update() of the role = %+v, %v, want an editor", a, err)
		}
		if err := e.m.Delete(ctx, System, moderator.ID); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("an unknown role is no administrator", func(t *testing.T) {
		only := newMgr(t)
		root := mgrActiveRow(t, 5, "root", "root", mgrPassword("root"))
		only.set(t, root)
		if n := activeAdmins(only.rows(t)); n != 0 {
			t.Errorf("activeAdmins() = %d, want 0", n)
		}
		if err := only.m.Delete(ctx, System, root.ID); err != nil {
			t.Errorf("Delete() = %v, want nil: the rule of the last active administrator is not about this account", err)
		}
	})
	t.Run("the token of a disabled account", func(t *testing.T) {
		token, hash, err := newToken(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		sleeper := mgrRow(6, "sleeper", RoleEditor)
		sleeper.Password, sleeper.CredentialsChangedAt, sleeper.Disabled = "", time.Time{}, true
		sleeper.Token = tokenRow{Purpose: PurposeInvitation, Hash: hash, ExpiresAt: mgrTime.Add(time.Hour)}
		e.set(t, sleeper)
		if got, err := e.m.Get(ctx, sleeper.ID); err != nil || got.Status != StatusDisabled || got.Token.Purpose != PurposeInvitation {
			t.Errorf("Get() = %+v, %v, want a disabled account that has an invitation", got, err)
		}
		before := e.mark(t)
		if _, err := e.m.SetPassword(ctx, mgrClient, token, mgrPassword("sleeper")); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("SetPassword() with the token of a disabled account = %v, want ErrInvalidToken", err)
		}
		_, err = e.m.Reset(ctx, System, sleeper.ID)
		if want := sleeper.ID + " is disabled: enable it first"; err == nil || err.Error() != want {
			t.Errorf("Reset() = %v, want %q", err, want)
		}
		e.recorded(t, before, "token.failure/disabled")
		// Enabling removes the token, as it does for any account.
		if a, err := e.m.Enable(ctx, System, sleeper.ID); err != nil || a.Status != StatusPending || a.Token != (TokenInfo{}) {
			t.Errorf("Enable() = %+v, %v, want a pending account without a token", a, err)
		}
		if err := e.m.Delete(ctx, System, sleeper.ID); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("times in another zone and another form", func(t *testing.T) {
		r := mgrActiveRow(t, 7, "zoned", RoleViewer, mgrPassword("zoned"))
		canonical := jsonOf(t, r)
		jst := time.FixedZone("JST", 9*3600)
		utc, local := `"`+mgrTime.UTC().Format(time.RFC3339Nano)+`"`, `"`+mgrTime.In(jst).Format(time.RFC3339Nano)+`"`
		written := " " + mgrReplaceTwice(t, canonical, utc, local) + "\n"
		e.putRaw(t, r.ID, written)
		if got, err := e.m.Get(ctx, r.ID); err != nil || got != r.account() {
			t.Errorf("Get() = %+v, %v, want %+v, with its times in UTC", got, err, r.account())
		}
		// The operations write the one form.
		a, err := e.m.Update(ctx, System, r.ID, Change{Name: ptr("Zoned")})
		if err != nil || a.Name != "Zoned" {
			t.Fatalf("Update() = %+v, %v, want the new name", a, err)
		}
		r.Name = "Zoned"
		if got, want := e.raw(t)[r.ID], jsonOf(t, r); got != want {
			t.Errorf("the stored row is not in the one form: %s", got)
		}
	})
}

// mgrReplaceTwice returns s with new in place of old, which must be in s exactly twice.
func mgrReplaceTwice(t *testing.T, s, old, new string) string {
	t.Helper()
	if n := strings.Count(s, old); n != 2 {
		t.Fatalf("%q is %d times in %q, want twice", old, n, s)
	}
	return strings.ReplaceAll(s, old, new)
}

func TestPersistence(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	s, err := store.Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := newMgrOn(t, s, dir)
	admin := e.first(t)
	pending, token := e.invite(t, "pending", RoleEditor)
	author := e.add(t, "author", RoleAuthor)
	off := e.add(t, "off", RoleViewer)
	e.clock.add(time.Minute)
	if _, err := e.m.Update(t.Context(), System, author.ID, Change{Name: ptr("Renamed")}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Disable(t.Context(), System, off.ID); err != nil {
		t.Fatal(err)
	}
	before, err := e.m.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rows := e.raw(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	e2 := newMgrOn(t, s2, dir)
	e2.clock.set(mgrTime.Add(time.Hour))
	if got := e2.raw(t); !maps.Equal(got, rows) {
		t.Error("the rows are not those that were closed")
	}
	after, err := e2.m.List(t.Context())
	if err != nil || !slices.Equal(after, before) {
		t.Errorf("List() after Open = %+v, %v, want %+v", after, err, before)
	}
	if got, err := e2.m.VerifyPasswordFactor(t.Context(), "admin", mgrPassword("admin")); err != nil || got != admin {
		t.Errorf("VerifyPasswordFactor() after Open = %+v, %v, want %+v", got, err, admin)
	}
	if _, err := e2.m.VerifyPasswordFactor(t.Context(), "off", mgrPassword("off")); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("VerifyPasswordFactor() of a disabled account after Open = %v, want ErrBadCredentials", err)
	}
	// The token of an invitation works in another process, within its time.
	a, err := e2.m.SetPassword(t.Context(), mgrClient, token, mgrPassword("pending"))
	if err != nil || a.ID != pending.ID || a.Status != StatusActive {
		t.Errorf("SetPassword() after Open = %+v, %v, want the account %s, active", a, err, pending.ID)
	}
	if got, err := e2.m.Find(t.Context(), "author"); err != nil || got.Name != "Renamed" {
		t.Errorf("Find() after Open = %+v, %v, want the renamed account", got, err)
	}
}

func TestDefaultIterations(t *testing.T) {
	s, err := store.Create(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	m, err := New(s)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	a, err := m.CreateFirst(t.Context(), mgrInput("admin", RoleAdmin), mgrPassword("admin"))
	if err != nil {
		t.Fatalf("CreateFirst() = %v", err)
	}
	var data []byte
	err = s.View(t.Context(), func(tx *store.Tx) error {
		var err error
		data, _, err = tx.Account(a.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := decodeRow(a.ID, data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(r.Password, "$pbkdf2-sha256$i=600000$") {
		t.Errorf("the stored hash does not name 600000 iterations")
	}
	if got, err := m.VerifyPasswordFactor(t.Context(), "admin", mgrPassword("admin")); err != nil || got != a {
		t.Errorf("VerifyPasswordFactor() = %+v, %v, want %+v", got, err, a)
	}
	if _, err := m.VerifyPasswordFactor(t.Context(), "admin", mgrPassword("other")); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("VerifyPasswordFactor() with a wrong password = %v, want ErrBadCredentials", err)
	}
	// A login that no account has is checked against a hash of the same cost, which New made.
	made := m.unknownHash
	if _, err := m.VerifyPasswordFactor(t.Context(), "nobody", mgrPassword("nobody")); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("VerifyPasswordFactor() of an unknown login = %v, want ErrBadCredentials", err)
	}
	if n, _, _, err := parseHash(m.unknownHash); err != nil || n != 600000 || m.unknownHash != made {
		t.Errorf("the hash for an unknown login has %d iterations, %v, or changed, want 600000 and the hash that New made", n, err)
	}
}

// mgrFixture makes a store with an account of each kind of actor, and the accounts that the operations are about. The
// rows are put with the store's own API: admin and boss are active administrators, pending an administrator without a
// credential, off a disabled administrator, target an active editor and sleeper a disabled one.
func mgrFixture(t *testing.T) (*mgrEnv, map[string]Actor, map[string]Account) {
	t.Helper()
	e := newMgr(t)
	pending, off, sleeper := mgrRow(6, "pending", RoleAdmin), mgrRow(7, "off", RoleAdmin), mgrRow(9, "sleeper", RoleEditor)
	pending.Password, pending.CredentialsChangedAt = "", time.Time{}
	off.Disabled, sleeper.Disabled = true, true
	rows := []row{
		mgrRow(1, "admin", RoleAdmin), mgrRow(2, "boss", RoleAdmin), mgrRow(3, "editor", RoleEditor), mgrRow(4, "author", RoleAuthor),
		mgrRow(5, "viewer", RoleViewer), pending, off, mgrRow(8, "target", RoleEditor), sleeper,
	}
	e.set(t, rows...)
	actors := make(map[string]Actor)
	accounts := make(map[string]Account)
	for _, r := range rows {
		actors[r.Login] = By(r.ID)
		accounts[r.Login] = r.account()
	}
	actors["system"], actors["missing"], actors["garbage"], actors["zero"] = System, By(mgrMissingID), By("not an id\n"), Actor{}
	return e, actors, accounts
}

// mgrRefusal is the text of the refusal of the actor to manage accounts.
func mgrRefusal(actor string, accounts map[string]Account) string {
	switch actor {
	case "missing":
		return mgrMissingID + ": not allowed: no such account"
	case "garbage":
		return strconv.Quote("not an id\n") + ": not allowed: no such account"
	case "zero":
		return "not allowed: the operation has no actor"
	}
	return mgrNotAllowed(accounts[actor], "manage accounts")
}

func TestOperationActors(t *testing.T) {
	operations := []struct {
		name string
		do   func(e *mgrEnv, by Actor, accounts map[string]Account) error
	}{
		{"Invite", func(e *mgrEnv, by Actor, _ map[string]Account) error {
			_, _, err := e.m.Invite(t.Context(), by, mgrInput("newbie", RoleViewer))
			return err
		}},
		{"Reset", func(e *mgrEnv, by Actor, accounts map[string]Account) error {
			_, err := e.m.Reset(t.Context(), by, accounts["target"].ID)
			return err
		}},
		{"Update", func(e *mgrEnv, by Actor, accounts map[string]Account) error {
			_, err := e.m.Update(t.Context(), by, accounts["target"].ID, Change{Name: ptr("New name")})
			return err
		}},
		{"Disable", func(e *mgrEnv, by Actor, accounts map[string]Account) error {
			_, err := e.m.Disable(t.Context(), by, accounts["target"].ID)
			return err
		}},
		{"Enable", func(e *mgrEnv, by Actor, accounts map[string]Account) error {
			_, err := e.m.Enable(t.Context(), by, accounts["sleeper"].ID)
			return err
		}},
		{"Delete", func(e *mgrEnv, by Actor, accounts map[string]Account) error {
			return e.m.Delete(t.Context(), by, accounts["target"].ID)
		}},
	}
	actorNames := []string{
		"admin", "boss", "editor", "author", "viewer", "pending", "off", "target", "sleeper", "system", "missing", "garbage", "zero",
	}
	for _, op := range operations {
		for _, name := range actorNames {
			t.Run(op.name+" by "+name, func(t *testing.T) {
				e, actors, accounts := mgrFixture(t)
				before := e.mark(t)
				err := op.do(e, actors[name], accounts)
				// An account may change its own name, with EditSelf.
				allowed := name == "admin" || name == "boss" || name == "system" || op.name == "Update" && name == "target"
				if allowed {
					if err != nil {
						t.Errorf("%s by %s = %v, want nil", op.name, name, err)
					}
					return
				}
				mgrError(t, err, ErrForbidden, mgrRefusal(name, accounts))
				e.unchanged(t, before)
			})
		}
	}
}

func TestActiveAdmins(t *testing.T) {
	active, other := mgrRow(1, "a", RoleAdmin), mgrRow(2, "b", RoleAdmin)
	pending, disabled, editor := mgrRow(3, "c", RoleAdmin), mgrRow(4, "d", RoleAdmin), mgrRow(5, "e", RoleEditor)
	pending.Password = ""
	disabled.Disabled = true
	tests := []struct {
		name string
		rows []row
		want int
	}{
		{"no accounts", nil, 0},
		{"one", []row{active}, 1},
		{"two", []row{active, other}, 2},
		{"only the active one of every kind", []row{active, pending, disabled, editor}, 1},
		{"none that can sign in", []row{pending, disabled, editor}, 0},
	}
	for _, tt := range tests {
		if got := activeAdmins(tt.rows); got != tt.want {
			t.Errorf("activeAdmins(%s) = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestCheckLastAdmin(t *testing.T) {
	a, b := mgrRow(1, "a", RoleAdmin), mgrRow(2, "b", RoleAdmin)
	demoted, disabled, reset := a, a, a
	demoted.Role = RoleEditor
	disabled.Disabled = true
	reset.Password = ""
	pending := a
	pending.Password = ""
	tests := []struct {
		name          string
		before, after []row
		refused       bool
	}{
		{"deleting the last", []row{a}, nil, true},
		{"another role for the last", []row{a}, []row{demoted}, true},
		{"disabling the last", []row{a}, []row{disabled}, true},
		{"removing the credentials of the last", []row{a}, []row{reset}, true},
		{"another role with another administrator", []row{a, b}, []row{demoted, b}, false},
		{"deleting one of two", []row{a, b}, []row{b}, false},
		{"losing both", []row{a, b}, nil, true},
		{"the first one", nil, []row{a}, false},
		{"another administrator", []row{a}, []row{a, b}, false},
		{"no change", []row{a}, []row{a}, false},
		{"no administrator that can sign in before", []row{pending}, nil, false},
		{"none before and none after", nil, nil, false},
	}
	for _, tt := range tests {
		err := checkLastAdmin(a.ID, tt.before, tt.after, "it ends")
		switch {
		case tt.refused:
			mgrError(t, err, ErrLastAdmin, mgrLastAdmin(a.ID, "it ends"))
		case err != nil:
			t.Errorf("checkLastAdmin(%s) = %v, want nil", tt.name, err)
		}
	}
}

func TestPutAndRemoveKeepAnAdministrator(t *testing.T) {
	e := newMgr(t)
	a, b := mgrRow(1, "a", RoleAdmin), mgrRow(2, "b", RoleAdmin)
	e.set(t, a, b)
	err := e.s.Update(t.Context(), storeWrite(func(tx *store.Tx) error {
		rows, err := loadRows(tx)
		if err != nil {
			return err
		}
		demoted := a
		demoted.Role = RoleEditor
		if err := put(tx, rows, demoted, "it is demoted"); err != nil {
			t.Errorf("put() of one of two administrators = %v, want nil", err)
		}
		// The rule looks at the rows as the transaction has them.
		rows, err = loadRows(tx)
		if err != nil {
			return err
		}
		lost := b
		lost.Disabled = true
		mgrError(t, put(tx, rows, lost, "it is disabled"), ErrLastAdmin, mgrLastAdmin(b.ID, "it is disabled"))
		mgrError(t, remove(tx, rows, b.ID, "it is removed"), ErrLastAdmin, mgrLastAdmin(b.ID, "it is removed"))
		// A new account does not matter to the rule, and one that is not an administrator can be removed.
		extra := mgrRow(3, "c", RoleViewer)
		if err := put(tx, rows, extra, noAdminLost); err != nil {
			t.Errorf("put() of a new account = %v, want nil", err)
		}
		if err := remove(tx, rows, a.ID, "it is removed"); err != nil {
			t.Errorf("remove() of an editor = %v, want nil", err)
		}
		// putRow does not apply the rule.
		return putRow(tx, lost)
	}))
	if err != nil {
		t.Fatal(err)
	}
	rows := e.rows(t)
	if len(rows) != 2 || rows[0].Login != "b" || !rows[0].Disabled || rows[1].Login != "c" {
		t.Errorf("the rows = %d, want b disabled and c", len(rows))
	}
}

func TestPutRefusesAnUnreadableRow(t *testing.T) {
	e := newMgr(t)
	bad := mgrRow(1, "a", RoleViewer)
	for name, unreadable := range map[string]func(*row){
		"a password that is not a hash": func(r *row) { r.Password = "not a password hash" },
		"a creator that is not an ID":   func(r *row) { r.CreatedBy = "x" },
		"a token without a hash":        func(r *row) { r.Token = tokenRow{Purpose: PurposeReset, ExpiresAt: mgrTime} },
		"no role":                       func(r *row) { r.Role = "" },
	} {
		r := bad
		unreadable(&r)
		err := e.s.Update(t.Context(), func(tx *store.Tx) error { return put(tx, nil, r, noAdminLost) })
		want := "cannot write " + r.ID + ": " + invalidValue(r.ID)
		if err == nil || err.Error() != want {
			t.Errorf("put() of a row with %s = %v, want %q", name, err, want)
		}
		if rows := e.raw(t); len(rows) != 0 {
			t.Errorf("put() of a row with %s left %d rows", name, len(rows))
		}
	}
	bad.CreatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := e.s.Update(t.Context(), func(tx *store.Tx) error { return putRow(tx, bad) }); err == nil {
		t.Error("putRow() of a row that cannot be encoded = nil, want an error")
	}
}
