package account

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	mathrand "math/rand/v2"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

var (
	mgrTime = time.Date(2026, 11, 8, 12, 0, 0, 123456000, time.UTC)
	mgrSeed = [32]byte{'m', 'a', 'n', 'a', 'g', 'e', 'r'}
	// mgrMissingID is the ID of an account that no test makes.
	mgrMissingID = content.FormatID(content.RefUser, [16]byte{0: 0x01, 15: 0xff})
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
	return newMgrOn(s, dir)
}

func newMgrOn(s *store.Store, dir string) *mgrEnv {
	clock := &mgrClock{t: mgrTime}
	m := New(s)
	m.SetClock(clock.now)
	m.iterations = pwLow
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
	a, err := e.m.SetPassword(t.Context(), token, mgrPassword(login))
	if err != nil {
		t.Fatalf("SetPassword(%q) = %v", login, err)
	}
	return a
}

// rows returns the rows as they are stored.
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

// row returns the stored row of the account id.
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

// mgrMark is what the store keeps of the accounts at a time: the bytes of every row, and the size of the data directory,
// which grows with every commit, also one that writes the bytes that were there.
type mgrMark struct {
	rows map[string]string
	size int64
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
	return mgrMark{rows: e.raw(t), size: size}
}

// unchanged checks that the store is as it was at before, which mark returned: the same rows, and no commit since.
func (e *mgrEnv) unchanged(t *testing.T, before mgrMark) {
	t.Helper()
	after := e.mark(t)
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
	if after.size != before.size {
		t.Errorf("the data directory changed from %d to %d bytes: something was committed", before.size, after.size)
	}
}

// set writes the rows with the store's own API, which makes states that the operations cannot.
func (e *mgrEnv) set(t *testing.T, rows ...row) {
	t.Helper()
	err := e.s.Update(t.Context(), func(tx *store.Tx) error {
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
	})
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

// putAuthor puts the author profile id into the store.
func (e *mgrEnv) putAuthor(t *testing.T, id string) {
	t.Helper()
	data := `{"schemaVersion": 1, "id": "` + id + `", "slug": "` + strings.ToLower(id[len(id)-4:]) + `"}`
	err := e.s.Update(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Put(content.RefAuthor, id, []byte(data), testCreatorID)
		return err
	})
	if err != nil {
		t.Fatalf("Put(%s) = %v", id, err)
	}
}

// mgrAuthorID is the ID of an author profile that tests put, and mgrOtherAuthorID of another.
const (
	mgrAuthorID      = "aut_01JB8Y6T2RG7M4XK9P2D5W8NQ3"
	mgrOtherAuthorID = "aut_01JB8Y6T2RG7M4XK9P2D5W8NQ4"
)

// mgrProblems checks that err is content.Errors with the problems.
func mgrProblems(t *testing.T, err error, want ...problem) {
	t.Helper()
	var errs content.Errors
	if !errors.As(err, &errs) {
		t.Errorf("error = %v, want the problems %v", err, want)
		return
	}
	assertProblems(t, errs, want...)
}

// mgrError checks that err wraps is and has the text.
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
	m := New(s)
	if m.st != s || m.iterations != 600000 || m.rand == nil || m.ids == nil || m.hook != nil {
		t.Errorf("New() = %+v, want the store, 600000 iterations, random bits, an ID source and no hook", m)
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
	err := rowProblems(ended, nil, "", "", "", mgrAuthorID)
	var problems content.Errors
	if err == nil || err.Error() != text || errors.As(err, &problems) {
		t.Errorf("rowProblems() = %v, want the error of the store", err)
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
	err := e.s.Update(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Delete(content.RefAuthor, deleted, testCreatorID)
		return err
	})
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

// mgrFindID returns the ID of the account with the login.
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
		other := newMgrOn(e.s, e.dir)
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

func TestCorruptRow(t *testing.T) {
	e := newMgr(t)
	admin := e.first(t)
	pending, token := e.invite(t, "yamada", RoleEditor)
	bad := mgrRow(7, "bad", RoleViewer).ID
	err := e.s.Update(t.Context(), func(tx *store.Tx) error { return tx.PutAccount(bad, []byte("not json")) })
	if err != nil {
		t.Fatal(err)
	}
	before := e.mark(t)
	ctx := t.Context()
	want := invalidValue(bad)
	other := Input{Login: "other", Name: "Other", Email: "other@example.com", Role: RoleViewer}
	operations := []struct {
		name string
		do   func() error
	}{
		{"CreateFirst", func() error {
			_, err := e.m.CreateFirst(ctx, mgrInput("second", RoleAdmin), mgrPassword("second"))
			return err
		}},
		{"Invite", func() error { _, _, err := e.m.Invite(ctx, System, other); return err }},
		{"SetPassword", func() error { _, err := e.m.SetPassword(ctx, token, mgrPassword("yamada")); return err }},
		{"Reset", func() error { _, err := e.m.Reset(ctx, System, pending.ID); return err }},
		{"ChangePassword", func() error { return e.m.ChangePassword(ctx, admin.ID, mgrPassword("admin"), mgrPassword("changed")) }},
		{"VerifyPasswordFactor", func() error { _, err := e.m.VerifyPasswordFactor(ctx, "admin", mgrPassword("admin")); return err }},
		{"Update", func() error { _, err := e.m.Update(ctx, System, pending.ID, Change{Name: ptr("New")}); return err }},
		{"Disable", func() error { _, err := e.m.Disable(ctx, System, pending.ID); return err }},
		{"Enable", func() error { _, err := e.m.Enable(ctx, System, pending.ID); return err }},
		{"Delete", func() error { return e.m.Delete(ctx, System, pending.ID) }},
		{"Get", func() error { _, err := e.m.Get(ctx, bad); return err }},
		{"List", func() error { _, err := e.m.List(ctx); return err }},
		{"Find", func() error { _, err := e.m.Find(ctx, "yamada"); return err }},
	}
	for _, op := range operations {
		if err := op.do(); err == nil || err.Error() != want {
			t.Errorf("%s() = %v, want %q", op.name, err, want)
		}
	}
	e.unchanged(t, before)
	// The reads of one account do not look at the others.
	if got, err := e.m.Get(ctx, admin.ID); err != nil || got != admin {
		t.Errorf("Get() of a good row = %+v, %v, want %+v", got, err, admin)
	}
}

func TestPersistence(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	s, err := store.Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := newMgrOn(s, dir)
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
	e2 := newMgrOn(s2, dir)
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
	a, err := e2.m.SetPassword(t.Context(), token, mgrPassword("pending"))
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
	m := New(s)
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
	// A login that no account has is checked against a hash of the same cost.
	if _, err := m.VerifyPasswordFactor(t.Context(), "nobody", mgrPassword("nobody")); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("VerifyPasswordFactor() of an unknown login = %v, want ErrBadCredentials", err)
	}
	if n, _, _, err := parseHash(m.unknownHash); err != nil || n != 600000 {
		t.Errorf("the hash for an unknown login has %d iterations, %v, want 600000", n, err)
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
	err := e.s.Update(t.Context(), func(tx *store.Tx) error {
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
	})
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
	bad.Login = "UPPER CASE" // not a login name
	err := e.s.Update(t.Context(), func(tx *store.Tx) error { return put(tx, nil, bad, noAdminLost) })
	want := "cannot write " + bad.ID + ": " + invalidValue(bad.ID)
	if err == nil || err.Error() != want {
		t.Errorf("put() = %v, want %q", err, want)
	}
	if rows := e.raw(t); len(rows) != 0 {
		t.Errorf("put() left %d rows", len(rows))
	}
	bad.CreatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := e.s.Update(t.Context(), func(tx *store.Tx) error { return putRow(tx, bad) }); err == nil {
		t.Error("putRow() of a row that cannot be encoded = nil, want an error")
	}
}
