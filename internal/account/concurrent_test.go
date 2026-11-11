package account

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	mathrand "math/rand/v2"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

func mgrRunTogether(calls ...func() error) []error {
	errs := make([]error, len(calls))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = call()
		}()
	}
	close(start)
	wg.Wait()
	return errs
}

// TestConcurrentAdministrators runs two operations at the same time, each of which is allowed alone and which together
// would leave no active administrator. Exactly one must succeed, in every round and whichever runs first.
func TestConcurrentAdministrators(t *testing.T) {
	e := newMgr(t)
	a, b := mgrRow(1, "first", RoleAdmin), mgrRow(2, "second", RoleAdmin)
	ctx := t.Context()
	tests := []struct {
		name string
		do   func(a, b string) (func() error, func() error)
		want error // what the operation that does not succeed returns
	}{
		{"disable each other", func(a, b string) (func() error, func() error) {
			return func() error { _, err := e.m.Disable(ctx, By(a), b); return err },
				func() error { _, err := e.m.Disable(ctx, By(b), a); return err }
		}, ErrForbidden},
		{"System disables both", func(a, b string) (func() error, func() error) {
			return func() error { _, err := e.m.Disable(ctx, System, a); return err },
				func() error { _, err := e.m.Disable(ctx, System, b); return err }
		}, ErrLastAdmin},
		{"System deletes both", func(a, b string) (func() error, func() error) {
			return func() error { return e.m.Delete(ctx, System, a) }, func() error { return e.m.Delete(ctx, System, b) }
		}, ErrLastAdmin},
		{"System changes both roles", func(a, b string) (func() error, func() error) {
			return func() error { _, err := e.m.Update(ctx, System, a, Change{Role: ptr(RoleEditor)}); return err },
				func() error { _, err := e.m.Update(ctx, System, b, Change{Role: ptr(RoleViewer)}); return err }
		}, ErrLastAdmin},
		{"System disables one and deletes the other", func(a, b string) (func() error, func() error) {
			return func() error { _, err := e.m.Disable(ctx, System, a); return err }, func() error { return e.m.Delete(ctx, System, b) }
		}, ErrLastAdmin},
		{"reset each other", func(a, b string) (func() error, func() error) {
			return func() error { _, err := e.m.Reset(ctx, By(a), b); return err },
				func() error { _, err := e.m.Reset(ctx, By(b), a); return err }
		}, ErrForbidden},
		{"reset themselves", func(a, b string) (func() error, func() error) {
			return func() error { _, err := e.m.Reset(ctx, By(a), a); return err },
				func() error { _, err := e.m.Reset(ctx, By(b), b); return err }
		}, ErrLastAdmin},
		{"delete and disable each other", func(a, b string) (func() error, func() error) {
			return func() error { return e.m.Delete(ctx, By(a), b) },
				func() error { _, err := e.m.Disable(ctx, By(b), a); return err }
		}, ErrForbidden},
	}
	rounds := 25
	if testing.Short() {
		rounds = 5
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for round := range rounds {
				// Both are active administrators again, whatever the last round did.
				e.set(t, a, b)
				f, g := tt.do(a.ID, b.ID)
				errs := mgrRunTogether(f, g)
				succeeded := 0
				for _, err := range errs {
					if err == nil {
						succeeded++
					} else if !errors.Is(err, tt.want) {
						t.Errorf("round %d: an operation returned %v, want nil or one that wraps %v", round, err, tt.want)
					}
				}
				if succeeded != 1 {
					t.Errorf("round %d: %d operations succeeded (%v), want exactly 1", round, succeeded, errs)
				}
				if n := activeAdmins(e.rows(t)); n < 1 {
					t.Fatalf("round %d: the store has %d active administrators", round, n)
				}
			}
		})
	}
}

func TestConcurrentInvites(t *testing.T) {
	const n = 8
	t.Run("the same login name", func(t *testing.T) {
		e := newMgr(t)
		calls := make([]func() error, n)
		for i := range calls {
			in := Input{Login: "same", Name: "Name", Email: "user-" + strconv.Itoa(i) + "@example.com", Role: RoleViewer}
			calls[i] = func() error { _, _, err := e.m.Invite(t.Context(), System, in); return err }
		}
		succeeded := 0
		for _, err := range mgrRunTogether(calls...) {
			if err == nil {
				succeeded++
			} else if !strings.HasPrefix(err.Error(), `/login: login name "same" is already used by usr_`) {
				t.Errorf("Invite() = %v, want the problem that the login name is used", err)
			}
		}
		if rows := e.rows(t); succeeded != 1 || len(rows) != 1 {
			t.Errorf("%d invitations succeeded and the store has %d accounts, want 1 and 1", succeeded, len(rows))
		}
	})
	t.Run("the same email address", func(t *testing.T) {
		e := newMgr(t)
		calls := make([]func() error, n)
		for i := range calls {
			// The case of the local part differs, and the addresses are the same.
			email := []string{"user@example.com", "User@example.com"}[i%2]
			in := Input{Login: "user-" + strconv.Itoa(i), Name: "Name", Email: email, Role: RoleViewer}
			calls[i] = func() error { _, _, err := e.m.Invite(t.Context(), System, in); return err }
		}
		succeeded := 0
		for _, err := range mgrRunTogether(calls...) {
			if err == nil {
				succeeded++
			} else if !strings.HasPrefix(err.Error(), "/email: email address ") || !strings.Contains(err.Error(), " is already used by usr_") {
				t.Errorf("Invite() = %v, want the problem that the email address is used", err)
			}
		}
		if rows := e.rows(t); succeeded != 1 || len(rows) != 1 {
			t.Errorf("%d invitations succeeded and the store has %d accounts, want 1 and 1", succeeded, len(rows))
		}
	})
	t.Run("different accounts", func(t *testing.T) {
		e := newMgr(t)
		calls := make([]func() error, n)
		for i := range calls {
			in := mgrInput("user-"+strconv.Itoa(i), RoleViewer)
			calls[i] = func() error { _, _, err := e.m.Invite(t.Context(), System, in); return err }
		}
		for _, err := range mgrRunTogether(calls...) {
			if err != nil {
				t.Errorf("Invite() = %v, want nil", err)
			}
		}
		rows := e.rows(t)
		ids := make([]string, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}
		if different := len(slices.Compact(slices.Sorted(slices.Values(ids)))); len(rows) != n || different != n {
			t.Errorf("the store has %d accounts with %d different IDs, want %d", len(rows), different, n)
		}
	})
}

func TestConcurrentSetPassword(t *testing.T) {
	const n = 6
	e := newMgr(t)
	e.first(t)
	a, token := e.invite(t, "yamada", RoleEditor)
	passwords := make([]string, n)
	calls := make([]func() error, n)
	for i := range calls {
		passwords[i] = "the long password number " + strconv.Itoa(i)
		calls[i] = func() error { _, err := e.m.SetPassword(t.Context(), mgrClient, token, passwords[i]); return err }
	}
	winner, succeeded := -1, 0
	for i, err := range mgrRunTogether(calls...) {
		switch {
		case err == nil:
			winner, succeeded = i, succeeded+1
		case !errors.Is(err, ErrInvalidToken):
			t.Errorf("SetPassword() number %d = %v, want nil or ErrInvalidToken", i, err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("%d calls of SetPassword succeeded, want 1", succeeded)
	}
	for i, p := range passwords {
		_, err := e.m.VerifyPasswordFactor(t.Context(), a.Login, p)
		if (err == nil) != (i == winner) {
			t.Errorf("VerifyPasswordFactor() with the password number %d = %v, want it to work only for the call that succeeded (%d)", i, err, winner)
		}
	}
}

func TestConcurrentChangePassword(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	a := e.add(t, "yamada", RoleEditor)
	passwords := []string{"the first new long password", "the second new long password"}
	errs := mgrRunTogether(
		func() error { return e.m.ChangePassword(t.Context(), a.ID, mgrPassword("yamada"), passwords[0]) },
		func() error { return e.m.ChangePassword(t.Context(), a.ID, mgrPassword("yamada"), passwords[1]) },
	)
	winner := -1
	for i, err := range errs {
		switch {
		case err == nil:
			if winner >= 0 {
				t.Fatal("both calls of ChangePassword succeeded")
			}
			winner = i
		case !errors.Is(err, ErrConflict) && !errors.Is(err, ErrBadCredentials):
			t.Errorf("ChangePassword() number %d = %v, want nil, ErrConflict or ErrBadCredentials", i, err)
		}
	}
	if winner < 0 {
		t.Fatalf("neither call of ChangePassword succeeded: %v", errs)
	}
	for i, p := range passwords {
		if _, err := e.m.VerifyPasswordFactor(t.Context(), "yamada", p); (err == nil) != (i == winner) {
			t.Errorf("VerifyPasswordFactor() with the password number %d = %v, want it to work only for the call that succeeded (%d)", i, err, winner)
		}
	}
}

// TestConcurrentStress runs many operations from several goroutines on a few login names and email addresses, while
// another goroutine reads the accounts, and checks the rules in every snapshot that it reads and at the end.
func TestConcurrentStress(t *testing.T) {
	workers, steps := 6, 40
	if testing.Short() {
		workers, steps = 3, 10
	}
	e := newMgr(t)
	ctx := t.Context()
	admin := e.first(t)
	boss := e.add(t, "boss", RoleAdmin)
	logins := []string{"one", "two", "three", "four", "five"}
	emails := []string{"a@example.com", "B@example.com", "c@example.com", "d@example.com", "E@example.com"}
	started := e.mark(t)

	var mu sync.Mutex
	succeeded := make(map[int]int) // how many operations of each kind succeeded
	ids, tokens := []string{admin.ID, boss.ID}, []string{}
	remember := func(id, token string) {
		mu.Lock()
		defer mu.Unlock()
		if id != "" {
			ids = append(ids, id)
		}
		if token != "" {
			tokens = append(tokens, token)
		}
	}

	done := make(chan struct{})
	var checker sync.WaitGroup
	checker.Add(1)
	go func() {
		defer checker.Done()
		for {
			select {
			case <-done:
				return
			case <-time.After(time.Millisecond):
			}
			if msg := mgrInvariants(e.s); msg != "" {
				t.Error(msg)
				return
			}
		}
	}()

	var running sync.WaitGroup
	for w := range workers {
		running.Add(1)
		go func() {
			defer running.Done()
			r := mathrand.New(mathrand.NewPCG(uint64(w), 7))
			for range steps {
				mu.Lock()
				id, actor := ids[r.IntN(len(ids))], ids[r.IntN(len(ids))]
				token := ""
				if len(tokens) > 0 {
					token = tokens[r.IntN(len(tokens))]
				}
				mu.Unlock()
				login, email := logins[r.IntN(len(logins))], emails[r.IntN(len(emails))]
				role := Roles()[r.IntN(len(Roles()))]
				var err error
				kind := r.IntN(9)
				switch kind {
				case 0, 1:
					var a Account
					var tok string
					a, tok, err = e.m.Invite(ctx, System, Input{Login: login, Name: "Name " + login, Email: email, Role: role})
					if err == nil {
						remember(a.ID, tok)
					}
				case 2:
					_, err = e.m.Update(ctx, System, id, Change{Login: &login, Email: &email, Role: &role})
				case 3:
					_, err = e.m.Update(ctx, By(actor), id, Change{Role: &role})
				case 4:
					_, err = e.m.Disable(ctx, System, id)
				case 5:
					_, err = e.m.Enable(ctx, System, id)
				case 6:
					err = e.m.Delete(ctx, System, id)
				case 7:
					var tok string
					tok, err = e.m.Reset(ctx, By(actor), id)
					if err == nil {
						remember("", tok)
					}
				case 8:
					if token == "" {
						continue
					}
					_, err = e.m.SetPassword(ctx, mgrClient, token, "the long password of "+login)
				}
				if err != nil && !mgrStressExpected(err) {
					t.Errorf("an operation failed with %v", err)
				}
				if err == nil {
					mu.Lock()
					succeeded[kind]++
					mu.Unlock()
				}
			}
		}()
	}
	running.Wait()
	total := 0
	for _, n := range succeeded {
		total += n
	}
	t.Logf("%d of %d operations succeeded, by kind: %v", total, workers*steps, succeeded)
	if total < workers*steps/10 {
		t.Errorf("only %d of %d operations succeeded: the test does not try the rules", total, workers*steps)
	}
	close(done)
	checker.Wait()
	if msg := mgrInvariants(e.s); msg != "" {
		t.Error(msg)
	}

	// Each operation that succeeded made one record in the activity log, and each that was refused made none.
	made := make(map[audit.Action]int)
	for _, r := range e.activityRecords(t, started) {
		made[r.Action]++
	}
	for _, c := range []struct {
		action audit.Action
		kinds  []int
	}{
		{audit.AccountInvite, []int{0, 1}}, {audit.AccountUpdate, []int{2, 3}}, {audit.AccountDisable, []int{4}},
		{audit.AccountEnable, []int{5}}, {audit.AccountDelete, []int{6}}, {audit.AccountReset, []int{7}},
		{audit.AccountSetPassword, []int{8}},
	} {
		want := 0
		for _, kind := range c.kinds {
			want += succeeded[kind]
		}
		if made[c.action] != want {
			t.Errorf("the activity log has %d records of %s, want %d: the number of operations that succeeded", made[c.action], c.action, want)
		}
		delete(made, c.action)
	}
	if len(made) != 0 {
		t.Errorf("the activity log has records of other actions than the operations that ran: %v", made)
	}
	if err := e.s.VerifyAudit(ctx, nil); err != nil {
		t.Errorf("VerifyAudit() = %v", err)
	}
}

// mgrStressExpected reports whether err is a refusal that the stress test makes happen: an account that another goroutine
// deleted, a token that was used or replaced, a problem of the input such as a login name that another account has, or
// a rule of the accounts. Anything else is a failure.
func mgrStressExpected(err error) bool {
	var problems content.Errors
	switch {
	case errors.Is(err, ErrLastAdmin), errors.Is(err, ErrForbidden), errors.Is(err, ErrInvalidToken), errors.Is(err, fs.ErrNotExist),
		errors.As(err, &problems):
		return true
	}
	return strings.HasSuffix(err.Error(), " is disabled: enable it first")
}

// mgrInvariants reads the accounts in one snapshot and returns what is wrong with them, or "". There is always an
// active administrator, because the operations of the test never leave none, and no two accounts have the same login
// name or the same email address in another case.
func mgrInvariants(s *store.Store) string {
	var rows []row
	err := s.View(context.Background(), func(tx *store.Tx) error {
		var err error
		rows, err = loadRows(tx)
		return err
	})
	if err != nil {
		return "the accounts cannot be read: " + err.Error()
	}
	if activeAdmins(rows) < 1 {
		return "the store has no active administrator"
	}
	logins, emails := make(map[string]bool), make(map[string]bool)
	for _, r := range rows {
		email := strings.ToLower(r.Email)
		if logins[r.Login] || emails[email] {
			return "two accounts have the same login name or email address"
		}
		logins[r.Login], emails[email] = true, true
	}
	return ""
}

func sessionRounds() int {
	if testing.Short() {
		return 5
	}
	return 25
}

// TestConcurrentLoginAndDisable races logins with a Disable, at each gap of a login and free-running, and checks that no
// session made before the Disable commits works after the account is enabled again.
func TestConcurrentLoginAndDisable(t *testing.T) {
	setup := func(t *testing.T) (*mgrEnv, Account) {
		t.Helper()
		e := newMgr(t)
		e.first(t)
		return e, e.add(t, "yamada", RoleEditor)
	}
	// finish enables the account again, which revives nothing: no row names it, and no token is accepted.
	finish := func(t *testing.T, e *mgrEnv, a Account, tokens []string, what string) {
		t.Helper()
		if _, err := e.m.Enable(t.Context(), System, a.ID); err != nil {
			t.Fatalf("%s: Enable() = %v", what, err)
		}
		if rows := e.sessionsOf(t, a.ID); len(rows) != 0 {
			t.Errorf("%s: the account has %d session rows after Enable, want none", what, len(rows))
		}
		for _, token := range tokens {
			if _, err := e.m.Authenticate(t.Context(), mgrClient, token); !errors.Is(err, ErrNoSession) {
				t.Errorf("%s: Authenticate() of a session made before the Disable = %v, want ErrNoSession", what, err)
			}
		}
	}

	for _, point := range []string{"hashing", "verified", "session"} {
		t.Run("a Disable at the gap "+point, func(t *testing.T) {
			e, a := setup(t)
			_, older := e.login(t, "yamada")
			ran := false
			e.m.hook = func(p string) {
				if p == point && !ran {
					ran = true
					if _, err := e.m.Disable(t.Context(), System, a.ID); err != nil {
						t.Errorf("Disable() = %v", err)
					}
				}
			}
			p, token, err := e.m.Login(t.Context(), mgrClient, "yamada", mgrPassword("yamada"), "")
			if !ran || !errors.Is(err, ErrBadCredentials) || !reflect.DeepEqual(p, Principal{}) || token != "" {
				t.Errorf("Login() disabled at %q = %+v, %v, want ErrBadCredentials and no token (the Disable ran = %v)", point, p, err, ran)
			}
			finish(t, e, a, []string{older}, "a Disable at "+point)
		})
	}

	t.Run("free-running", func(t *testing.T) {
		e, a := setup(t)
		const logins = 3
		for round := range sessionRounds() {
			_, older := e.login(t, "yamada")
			tokens := make([]string, logins)
			calls := []func() error{func() error { _, err := e.m.Disable(t.Context(), System, a.ID); return err }}
			for i := range logins {
				calls = append(calls, func() error {
					var err error
					_, tokens[i], err = e.m.Login(t.Context(), mgrClient, "yamada", mgrPassword("yamada"), "")
					return err
				})
			}
			made := []string{older}
			for i, err := range mgrRunTogether(calls...) {
				switch {
				case i == 0 && err != nil:
					t.Errorf("round %d: Disable() = %v", round, err)
				case i > 0 && err == nil:
					made = append(made, tokens[i-1])
				case i > 0 && !errors.Is(err, ErrBadCredentials):
					t.Errorf("round %d: Login() = %v, want nil or ErrBadCredentials", round, err)
				}
			}
			finish(t, e, a, made, fmt.Sprintf("round %d", round))
		}
	})
}

// TestConcurrentLoginAndRoleChange races logins with a change of the role: a login that was made before the change has
// the old role and does not work after it, and one that was made after has the new role and works.
func TestConcurrentLoginAndRoleChange(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	a := e.add(t, "yamada", RoleEditor)
	roles := []Role{RoleViewer, RoleAuthor, RoleEditor} // each differs from the one before it, and from editor
	const logins = 4
	worked := 0
	for round := range sessionRounds() {
		to := roles[round%len(roles)]
		results := make([]Principal, logins)
		tokens := make([]string, logins)
		calls := []func() error{func() error { _, err := e.m.Update(t.Context(), System, a.ID, Change{Role: &to}); return err }}
		for i := range logins {
			calls = append(calls, func() error {
				var err error
				results[i], tokens[i], err = e.m.Login(t.Context(), mgrClient, "yamada", mgrPassword("yamada"), "")
				return err
			})
		}
		kept := 0
		for i, err := range mgrRunTogether(calls...) {
			switch {
			case i == 0 && err != nil:
				t.Fatalf("round %d: Update() = %v", round, err)
			case i == 0:
			case err != nil:
				t.Errorf("round %d: Login() = %v, want nil: the password is right", round, err)
			default:
				// A login that read the account before the change is the old role, and its session must be gone.
				want := results[i-1].Account.Role == to
				_, authErr := e.m.Authenticate(t.Context(), mgrClient, tokens[i-1])
				if has := e.hasSession(t, tokens[i-1]); has != want || (authErr == nil) != want {
					t.Errorf("round %d: a login of the role %s after a change to %s has a row = %v and works = %v, want both %v",
						round, results[i-1].Account.Role, to, has, authErr == nil, want)
				}
				if want {
					kept++
				}
			}
		}
		if rows := e.sessionsOf(t, a.ID); len(rows) != kept {
			t.Errorf("round %d: the account has %d session rows, want %d: those of the logins made after the change", round, len(rows), kept)
		}
		worked += kept
	}
	if worked == 0 {
		t.Error("no login was made after a change of the role in any round: the test does not try both orders")
	}
}

// TestConcurrentLoginAndChangePassword races logins with the old password and with the new one against ChangePassword: a
// login that matched the old password and was written before the change is ended by it, and one that is written after
// fails; only a login with the new password has a session after the change.
func TestConcurrentLoginAndChangePassword(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	a := e.add(t, "yamada", RoleEditor)
	current := mgrPassword("yamada")
	const logins = 3
	for round := range sessionRounds() {
		next := fmt.Sprintf("the password of round %d", round)
		type login struct {
			password string
			token    string
		}
		results := make([]login, 2*logins)
		calls := []func() error{func() error { return e.m.ChangePassword(t.Context(), a.ID, current, next) }}
		for i := range results {
			results[i].password = current
			if i >= logins {
				results[i].password = next
			}
			calls = append(calls, func() error {
				var err error
				_, results[i].token, err = e.m.Login(t.Context(), mgrClient, "yamada", results[i].password, "")
				return err
			})
		}
		kept := 0
		for i, err := range mgrRunTogether(calls...) {
			switch {
			case i == 0 && err != nil:
				t.Fatalf("round %d: ChangePassword() = %v", round, err)
			case i == 0:
			case err != nil && !errors.Is(err, ErrBadCredentials):
				t.Errorf("round %d: Login() = %v, want nil or ErrBadCredentials", round, err)
			case err == nil:
				r := results[i-1]
				// The new password matches only after the change commits, so its session works; a session made with the old
				// password was written before the change, and the change ended it.
				want := r.password == next
				_, authErr := e.m.Authenticate(t.Context(), mgrClient, r.token)
				if has := e.hasSession(t, r.token); has != want || (authErr == nil) != want {
					t.Errorf("round %d: a login with the password of the %s has a row = %v and works = %v, want both %v",
						round, map[bool]string{true: "new", false: "old"}[want], has, authErr == nil, want)
				}
				if want {
					kept++
				}
			}
		}
		if rows := e.sessionsOf(t, a.ID); len(rows) != kept {
			t.Errorf("round %d: the account has %d session rows, want %d: those of the logins with the new password", round, len(rows), kept)
		}
		current = next
	}
}

// TestConcurrentLoginAndChangeOwnPassword races two ChangeOwnPassword calls of the same session, logins with the old
// password and logins with each new one: exactly one change wins and the other is ErrNoSession, and the only sessions
// after it are the winner's new one and those of logins with the winner's password.
func TestConcurrentLoginAndChangeOwnPassword(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	a := e.add(t, "yamada", RoleEditor)
	current := mgrPassword("yamada")
	_, token := e.login(t, "yamada")
	const logins = 2
	wins := [2]int{}
	for round := range sessionRounds() {
		nexts := [2]string{fmt.Sprintf("the first password of round %d", round), fmt.Sprintf("the second password of round %d", round)}
		type result struct {
			password, token string
		}
		fresh := [2]string{}
		results := make([]result, 3*logins)
		calls := make([]func() error, 0, 2+len(results))
		for w := range nexts {
			calls = append(calls, func() error {
				var err error
				_, fresh[w], err = e.m.ChangeOwnPassword(t.Context(), mgrClient, token, current, nexts[w])
				return err
			})
		}
		for i := range results {
			results[i].password = []string{current, nexts[0], nexts[1]}[i/logins]
			calls = append(calls, func() error {
				var err error
				_, results[i].token, err = e.m.Login(t.Context(), mgrClient, "yamada", results[i].password, "")
				return err
			})
		}
		errs := mgrRunTogether(calls...)
		winner := -1
		for w := range nexts {
			switch {
			case errs[w] == nil && winner < 0:
				winner = w
			case errs[w] == nil:
				t.Fatalf("round %d: both calls of ChangeOwnPassword succeeded", round)
			case !errors.Is(errs[w], ErrNoSession):
				t.Errorf("round %d: ChangeOwnPassword() = %v, want nil or ErrNoSession", round, errs[w])
			}
		}
		if winner < 0 {
			t.Fatalf("round %d: neither call of ChangeOwnPassword succeeded: %v", round, errs[:2])
		}
		wins[winner]++
		kept := 1 // the new session of the winner
		sessLives(t, e, fresh[winner], fmt.Sprintf("of the winner in round %d", round))
		sessGone(t, e, token, fmt.Sprintf("that asked in round %d", round))
		for i, err := range errs[2:] {
			r := results[i]
			switch {
			case err != nil && !errors.Is(err, ErrBadCredentials):
				t.Errorf("round %d: Login() = %v, want nil or ErrBadCredentials", round, err)
			case err == nil && r.password == nexts[1-winner]:
				t.Errorf("round %d: a login with the password that lost succeeded", round)
			case err == nil && r.password == current:
				sessGone(t, e, r.token, fmt.Sprintf("of a login with the old password in round %d", round))
			case err == nil:
				sessLives(t, e, r.token, fmt.Sprintf("of a login with the new password in round %d", round))
				kept++
			}
		}
		if rows := e.sessionsOf(t, a.ID); len(rows) != kept {
			t.Errorf("round %d: the account has %d session rows, want %d", round, len(rows), kept)
		}
		current, token = nexts[winner], fresh[winner]
	}
	t.Logf("the first call won %d rounds and the second %d", wins[0], wins[1])
}

// sessionInvariants reads the accounts and the sessions in one snapshot and returns what is wrong with them, or "": a
// session row that does not decode, or that names an account that is missing, is not active or has other credentials than
// the session was made with. The tests that use it never let a session expire, so every row must work.
func sessionInvariants(s *store.Store) string {
	msg := ""
	err := s.View(context.Background(), func(tx *store.Tx) error {
		stored, err := tx.Sessions()
		if err != nil {
			return err
		}
		for _, st := range stored {
			sess, err := decodeSession(st.Key, st.Data)
			if err != nil {
				msg = "a session row does not decode"
				return nil
			}
			r, ok, err := readAccount(tx, sess.Account)
			switch {
			case err != nil:
				return err
			case !ok:
				msg = "a session row names an account that is missing or cannot be read"
				return nil
			}
			if _, ended := sess.accountEnd(r, true); ended {
				msg = "a session row names an account that is not active or has other credentials than the session was made with"
				return nil
			}
		}
		return nil
	})
	if err != nil {
		return "the sessions cannot be read: " + err.Error()
	}
	return msg
}

// TestConcurrentSessionRules runs logins, requests, logouts and every operation that ends sessions from several goroutines
// on a few accounts, with a clock that moves on every reading so that the credentials of an account differ after each
// change, while another goroutine reads the store: in every snapshot, each session row must work, which is to say that no
// operation commits a change without ending the sessions that rely on what it changed.
func TestConcurrentSessionRules(t *testing.T) {
	workers, steps := 6, 60
	if testing.Short() {
		workers, steps = 3, 12
	}
	e := newMgr(t)
	var ticks atomic.Int64
	e.m.SetClock(func() time.Time { return mgrTime.Add(time.Duration(ticks.Add(1)) * time.Millisecond) })
	ctx := t.Context()
	logins := []string{"one", "two", "three", "four"}
	everyone := append([]string{"admin", "boss"}, logins...)
	type state struct {
		op       sync.Mutex // held by an operation that changes the password for as long as it runs, so that they do not overlap
		mu       sync.Mutex // guards password, and is held briefly
		password string     // the password that the account has, if the test knows it
		id       string
	}
	accounts := map[string]*state{
		"admin": {password: mgrPassword("admin"), id: e.first(t).ID},
		"boss":  {password: mgrPassword("boss"), id: e.add(t, "boss", RoleAdmin).ID},
	}
	for _, login := range logins {
		accounts[login] = &state{password: mgrPassword(login), id: e.add(t, login, RoleEditor).ID}
	}
	current := func(st *state) string {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.password
	}
	set := func(st *state, password string) {
		st.mu.Lock()
		defer st.mu.Unlock()
		st.password = password
	}

	var mu sync.Mutex
	var tokens []string
	owner := make(map[string]string) // the login name of a token
	remember := func(token, login string) {
		mu.Lock()
		defer mu.Unlock()
		tokens = append(tokens, token)
		owner[token] = login
	}
	pick := func(r *mathrand.Rand) (token, login string) {
		mu.Lock()
		defer mu.Unlock()
		if len(tokens) == 0 {
			return "", ""
		}
		token = tokens[r.IntN(len(tokens))]
		return token, owner[token]
	}
	// pickOf returns a token that a login of this account made, or "" if there is none.
	pickOf := func(r *mathrand.Rand, login string) string {
		mu.Lock()
		defer mu.Unlock()
		if len(tokens) == 0 {
			return ""
		}
		start := r.IntN(len(tokens))
		for i := range tokens {
			if token := tokens[(start+i)%len(tokens)]; owner[token] == login {
				return token
			}
		}
		return ""
	}
	var changes atomic.Int64

	done := make(chan struct{})
	var checker sync.WaitGroup
	checker.Add(1)
	go func() {
		defer checker.Done()
		for {
			select {
			case <-done:
				return
			case <-time.After(time.Millisecond):
			}
			if msg := sessionInvariants(e.s); msg != "" {
				t.Error(msg)
				return
			}
		}
	}()

	var successes sync.Map
	var running sync.WaitGroup
	for w := range workers {
		running.Add(1)
		go func() {
			defer running.Done()
			r := mathrand.New(mathrand.NewPCG(uint64(w), 11))
			for range steps {
				login := everyone[r.IntN(len(everyone))]
				target := logins[r.IntN(len(logins))] // the accounts that are changed: the administrators are not
				st := accounts[target]
				kind := r.IntN(11)
				var err error
				switch kind {
				case 0, 1:
					previous, _ := pick(r)
					var token string
					_, token, err = e.m.Login(ctx, mgrClient, login, current(accounts[login]), previous)
					if err == nil {
						remember(token, login)
					}
				case 2:
					token, _ := pick(r)
					_, err = e.m.Authenticate(ctx, mgrClient, token)
				case 3:
					token, _ := pick(r)
					err = e.m.Logout(ctx, mgrClient, token)
				case 4:
					_, err = e.m.Disable(ctx, System, st.id)
				case 5:
					_, err = e.m.Enable(ctx, System, st.id)
				case 6:
					role := Roles()[r.IntN(len(Roles()))]
					_, err = e.m.Update(ctx, System, st.id, Change{Role: &role})
				case 7:
					st.op.Lock()
					next := fmt.Sprintf("%s changes to number %d", target, changes.Add(1))
					if err = e.m.ChangePassword(ctx, st.id, current(st), next); err == nil {
						set(st, next)
					}
					st.op.Unlock()
				case 8:
					// A session that was made earlier, or one that a login makes now: either may have ended meanwhile.
					token := pickOf(r, target)
					st.op.Lock()
					if r.IntN(2) == 0 {
						if _, token, err = e.m.Login(ctx, mgrClient, target, current(st), ""); err == nil {
							remember(token, target)
						}
					}
					if err == nil {
						next := fmt.Sprintf("%s changes itself to number %d", target, changes.Add(1))
						var fresh string
						if _, fresh, err = e.m.ChangeOwnPassword(ctx, mgrClient, token, current(st), next); err == nil {
							set(st, next)
							remember(fresh, target)
						}
					}
					st.op.Unlock()
				case 9:
					st.op.Lock()
					var token string
					if token, err = e.m.Reset(ctx, System, st.id); err == nil {
						set(st, "") // the account has none until the token is used
						next := fmt.Sprintf("%s starts again with number %d", target, changes.Add(1))
						if _, err = e.m.SetPassword(ctx, mgrClient, token, next); err == nil {
							set(st, next)
						}
					}
					st.op.Unlock()
				case 10:
					// An account that is made, logs in and is deleted while the others go on.
					n := changes.Add(1)
					in := mgrInput(fmt.Sprintf("temp-%d", n), RoleViewer)
					var a Account
					var token string
					if a, token, err = e.m.Invite(ctx, System, in); err != nil {
						break
					}
					if _, err = e.m.SetPassword(ctx, mgrClient, token, fmt.Sprintf("the password of temp number %d", n)); err != nil {
						break
					}
					if _, token, err = e.m.Login(ctx, mgrClient, in.Login, fmt.Sprintf("the password of temp number %d", n), ""); err != nil {
						break
					}
					if err = e.m.Delete(ctx, System, a.ID); err != nil {
						break
					}
					if _, err = e.m.Authenticate(ctx, mgrClient, token); !errors.Is(err, ErrNoSession) {
						t.Errorf("Authenticate() of a session of an account that was deleted = %v, want ErrNoSession", err)
					}
					err = nil
				}
				if err != nil && !sessionStressExpected(err) {
					t.Errorf("operation %d failed with %v", kind, err)
				}
				if err == nil {
					n, _ := successes.LoadOrStore(kind, new(atomic.Int64))
					n.(*atomic.Int64).Add(1)
				}
			}
		}()
	}
	running.Wait()
	close(done)
	checker.Wait()

	total := int64(0)
	by := make(map[int]int64)
	successes.Range(func(k, v any) bool {
		by[k.(int)] = v.(*atomic.Int64).Load()
		total += by[k.(int)]
		return true
	})
	t.Logf("%d of %d operations succeeded, by kind: %v", total, workers*steps, by)
	if total < int64(workers*steps/4) {
		t.Errorf("only %d of %d operations succeeded: the test does not try the rules", total, workers*steps)
	}
	if msg := sessionInvariants(e.s); msg != "" {
		t.Error(msg)
	}
	// A token works if its row is there, and only then.
	for _, token := range tokens {
		has := e.hasSession(t, token)
		if _, err := e.m.Authenticate(ctx, mgrClient, token); (err == nil) != has {
			t.Errorf("a token of %s works = %v, though its row is there = %v", owner[token], err == nil, has)
		}
	}
	if msg := mgrInvariants(e.s); msg != "" {
		t.Error(msg)
	}
}

// sessionStressExpected reports whether err is a refusal that TestConcurrentSessionRules makes happen: those of
// mgrStressExpected, a password that is not right any more or an account that is not active, a session that has ended,
// and a password that changed while the hash was made.
func sessionStressExpected(err error) bool {
	return mgrStressExpected(err) || errors.Is(err, ErrBadCredentials) || errors.Is(err, ErrNoSession) || errors.Is(err, ErrConflict)
}
