package account

import (
	"context"
	"errors"
	"io/fs"
	mathrand "math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// mgrRunTogether starts the functions at the same time and returns what each returned.
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
		calls[i] = func() error { _, err := e.m.SetPassword(t.Context(), token, passwords[i]); return err }
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
					if token != "" {
						_, err = e.m.SetPassword(ctx, token, "the long password of "+login)
					}
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
