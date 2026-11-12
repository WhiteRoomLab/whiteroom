package admin

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

const someAuthor = "aut_01JBA4C7N9Q3T6W9Z3C6F9H2K5"

// roleServer is a test server with an account for each role, named admin, editor, author and viewer, who are not logged in.
type roleServer struct {
	*testServer
	people map[string]account.Account
}

func newRoleServer(t *testing.T) *roleServer {
	t.Helper()
	rs := &roleServer{testServer: newTestServer(t, httpsOrigin), people: make(map[string]account.Account)}
	for _, l := range apiLogins {
		rs.people[l.login] = rs.addAccount(t, l.login, l.role)
	}
	return rs
}

func (rs *roleServer) principal(login string) account.Principal {
	return account.Principal{Account: rs.people[login]}
}

// refusals returns the records of access.denied.
func (rs *roleServer) refusals(t *testing.T) []audit.Record { return rs.records(t, audit.AccessDenied) }

func TestAllow(t *testing.T) {
	rs := newRoleServer(t)
	termTarget := audit.Target{Kind: "term", ID: someTerm}
	try := func(login string, act account.Action, target account.Target, named audit.Target) (bool, *http.Response) {
		rec := httptest.NewRecorder()
		ok := rs.allow(rec, rs.request(http.MethodPut, "/api/test", ""), rs.principal(login), act, target, named)
		return ok, rec.Result()
	}

	ok, res := try("editor", account.EditTerms, account.Target{}, termTarget)
	if !ok || res.StatusCode != http.StatusOK || len(rs.refusals(t)) != 0 {
		t.Errorf("an editor edits terms: %v, status %d, %d refusals, want true, nothing written and none", ok, res.StatusCode, len(rs.refusals(t)))
	}

	ok, res = try("viewer", account.EditTerms, account.Target{}, termTarget)
	if ok {
		t.Error("a viewer edits terms: allow = true")
	}
	checkProblem(t, "a viewer who edits terms", res, "forbidden")
	got := rs.refusals(t)
	if len(got) != 1 {
		t.Fatalf("the refusal made %d records of access.denied, want 1", len(got))
	}
	r := got[0]
	if r.Actor != rs.people["viewer"].ID || r.Action != audit.AccessDenied || r.Target != termTarget || len(r.Detail) != 1 ||
		r.Detail["permission"] != "term.edit" || r.Log != audit.ActivityLog || r.Account != "" || r.Client != (audit.Client{}) {
		t.Errorf("the record of the refusal is %+v, want the actor %s, the permission term.edit and the target %+v", r, rs.people["viewer"].ID, termTarget)
	}

	// The target of the check is what the role is held against: an author edits what is its own, and not what is another's.
	own := account.Target{Owner: rs.people["author"].ID}
	if ok, _ := try("author", account.EditEntry, own, audit.Target{}); !ok {
		t.Error("an author edits an entry of its own: allow = false")
	}
	ok, res = try("author", account.EditEntry, account.Target{Owner: rs.people["editor"].ID}, audit.Target{Kind: "entry", ID: someEntry})
	if ok {
		t.Error("an author edits an entry of another account: allow = true")
	}
	checkProblem(t, "an author who edits an entry of another", res, "forbidden")
	got = rs.refusals(t)
	if len(got) != 2 || got[1].Detail["permission"] != "entry.edit" || got[1].Target != (audit.Target{Kind: "entry", ID: someEntry}) {
		t.Errorf("the records are %+v, want a second one for entry.edit of the entry", got)
	}

	// A target that is not an ID is left out of the record, which is still written.
	ok, res = try("viewer", account.EditEntry, account.Target{}, audit.Target{Kind: "entry", ID: "../../etc/passwd"})
	checkProblem(t, "a viewer who edits an entry that is not an ID", res, "forbidden")
	got = rs.refusals(t)
	if ok || len(got) != 3 || got[2].Target != (audit.Target{}) || got[2].Detail["permission"] != "entry.edit" {
		t.Errorf("a refusal with a target that is not an ID: %v and %+v, want a record without a target", ok, got)
	}
	if logged := rs.logs.String(); logged != "" {
		t.Errorf("the refusals have logged %q", logged)
	}
}

func TestAllowView(t *testing.T) {
	rs := newRoleServer(t)
	for _, l := range apiLogins {
		rec := httptest.NewRecorder()
		if !rs.allowView(rec, rs.request(http.MethodGet, "/api/test", ""), rs.principal(l.login)) || rec.Body.Len() != 0 {
			t.Errorf("a %s views: allowView = false or an answer was written", l.login)
		}
	}
	disabled := rs.principal("viewer")
	disabled.Account.Status = account.StatusDisabled
	rec := httptest.NewRecorder()
	if rs.allowView(rec, rs.request(http.MethodGet, "/api/test", ""), disabled) {
		t.Error("an account that is not active views: allowView = true")
	}
	checkProblem(t, "an account that is not active", rec.Result(), "forbidden")
	if got := rs.refusals(t); len(got) != 1 || got[0].Detail["permission"] != "view" || got[0].Target != (audit.Target{}) {
		t.Errorf("the records are %+v, want one for view without a target", got)
	}
}

func TestAllowAsOwn(t *testing.T) {
	rs := newRoleServer(t)
	linked := rs.principal("author")
	linked.Account.Author = someAuthor
	for _, tt := range []struct {
		login string
		p     account.Principal
		act   account.Action
		ok    bool
	}{
		{"admin", rs.principal("admin"), account.EditEntry, true},
		{"editor", rs.principal("editor"), account.PublishEntry, true},
		{"an author", rs.principal("author"), account.EditEntry, true},
		{"an author", rs.principal("author"), account.DeleteEntry, true},
		{"an author", rs.principal("author"), account.EditMedia, true},
		{"an author", rs.principal("author"), account.DeleteMedia, true},
		{"an author with a profile", linked, account.EditAuthor, true},
		{"an author without a profile", rs.principal("author"), account.EditAuthor, false},
		{"a viewer", rs.principal("viewer"), account.EditEntry, false},
		{"a viewer", rs.principal("viewer"), account.DeleteMedia, false},
		{"an author", rs.principal("author"), account.EditTerms, false},
		{"an author", rs.principal("author"), account.ManageBuilds, false},
		{"an editor", rs.principal("editor"), account.EditSettings, false},
	} {
		rec := httptest.NewRecorder()
		ok := rs.allowAsOwn(rec, rs.request(http.MethodPost, "/api/test", ""), tt.p, tt.act, audit.Target{})
		if ok != tt.ok {
			t.Errorf("%s asks for %s as if it were the owner: allowAsOwn = %v, want %v", tt.login, tt.act, ok, tt.ok)
		}
		if !ok {
			checkProblem(t, tt.login+" asks for "+string(tt.act), rec.Result(), "forbidden")
		}
	}
}

// changeAccount changes the account login with c, as the system does. That ends its sessions, but not the Principals that
// were made from them.
func (rs *roleServer) changeAccount(t *testing.T, login string, c account.Change) {
	t.Helper()
	if _, err := rs.accounts.Update(t.Context(), account.System, rs.people[login].ID, c); err != nil {
		t.Fatalf("Update(%q) error = %v", login, err)
	}
}

// check runs the check of inUpdate in an Update that writes nothing, and returns its error.
func (rs *roleServer) check(t *testing.T, check func(*store.Tx) error) error {
	t.Helper()
	var err error
	if uerr := rs.db.Update(t.Context(), func(tx *store.Tx) error {
		err = check(tx)
		return nil
	}); uerr != nil {
		t.Fatalf("Update() error = %v", uerr)
	}
	return err
}

func TestInUpdate(t *testing.T) {
	named := audit.Target{Kind: "entry", ID: someEntry}
	other := account.Target{Owner: "usr_01JB8T7H4MA2C5E8G1J4K7N3P6"}

	t.Run("allowed", func(t *testing.T) {
		rs := newRoleServer(t)
		if err := rs.check(t, inUpdate(rs.principal("editor"), named, nil, account.EditEntry)); err != nil {
			t.Errorf("an editor edits entries: error = %v, want nil", err)
		}
		own := func(*store.Tx) (account.Target, error) { return account.Target{Owner: rs.people["author"].ID}, nil }
		if err := rs.check(t, inUpdate(rs.principal("author"), named, own, account.EditEntry, account.PublishEntry)); err != nil {
			t.Errorf("an author edits and publishes its own entry: error = %v, want nil", err)
		}
		// The check runs in a View too, and writes nothing.
		var err error
		if verr := rs.db.View(t.Context(), func(tx *store.Tx) error {
			err = inUpdate(rs.principal("viewer"), named, nil, account.ViewAdmin)(tx)
			return nil
		}); verr != nil || err != nil {
			t.Errorf("a viewer views in a View: %v, %v, want nil", verr, err)
		}
	})

	t.Run("the target is read in the transaction", func(t *testing.T) {
		rs := newRoleServer(t)
		var called int
		owner := func(tx *store.Tx) (account.Target, error) {
			called++
			if tx == nil {
				t.Error("the target function is called without a transaction")
			}
			return other, nil
		}
		err := rs.check(t, inUpdate(rs.principal("author"), named, owner, account.EditEntry))
		var denied *deniedError
		if !errors.Is(err, account.ErrForbidden) || !errors.As(err, &denied) || called != 1 {
			t.Fatalf("an author edits an entry of another: error = %v, target read %d times, want a refusal and 1", err, called)
		}
		if denied.action != account.EditEntry || denied.named != named {
			t.Errorf("the refusal names %q and %+v, want entry.edit and %+v", denied.action, denied.named, named)
		}
		if !strings.Contains(err.Error(), rs.people["author"].ID) {
			t.Errorf("the text of the refusal is %q, want that of account.Check", err)
		}

		missing := func(*store.Tx) (account.Target, error) {
			return account.Target{}, fmt.Errorf("entries/x.json: %w", fs.ErrNotExist)
		}
		if err := rs.check(t, inUpdate(rs.principal("editor"), named, missing, account.EditEntry)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("a target that is missing: error = %v, want the error of the target function", err)
		}
	})

	t.Run("the first action that is refused is named", func(t *testing.T) {
		rs := newRoleServer(t)
		err := rs.check(t, inUpdate(rs.principal("editor"), named, nil, account.EditEntry, account.EditHTML, account.EditSettings))
		var denied *deniedError
		if !errors.As(err, &denied) || denied.action != account.EditHTML {
			t.Errorf("an editor who needs html.edit: error = %v, want a refusal of html.edit", err)
		}
		if err := rs.check(t, inUpdate(rs.principal("admin"), named, nil, account.EditEntry, account.EditHTML, account.EditSettings)); err != nil {
			t.Errorf("an admin who needs all three: error = %v, want nil", err)
		}
	})

	t.Run("the role is the one that the store has", func(t *testing.T) {
		rs := newRoleServer(t)
		p := rs.principal("editor")
		check := inUpdate(p, named, nil, account.EditEntry)
		if err := rs.check(t, check); err != nil {
			t.Fatalf("an editor edits entries: error = %v", err)
		}
		role := account.RoleViewer
		rs.changeAccount(t, "editor", account.Change{Role: &role})
		err := rs.check(t, check)
		var denied *deniedError
		if !errors.Is(err, account.ErrForbidden) || !errors.As(err, &denied) || denied.action != account.EditEntry {
			t.Errorf("an editor who has become a viewer edits entries: error = %v, want a refusal of entry.edit", err)
		}
		if p.Account.Role != account.RoleEditor {
			t.Fatal("the principal changed")
		}
	})

	t.Run("the account is gone", func(t *testing.T) {
		rs := newRoleServer(t)
		called := false
		owner := func(*store.Tx) (account.Target, error) { called = true; return account.Target{}, nil }
		check := inUpdate(rs.principal("viewer"), named, owner, account.ViewAdmin)
		if err := rs.accounts.Delete(t.Context(), account.System, rs.people["viewer"].ID); err != nil {
			t.Fatal(err)
		}
		if err := rs.check(t, check); !errors.Is(err, errSessionChanged) || errors.Is(err, account.ErrForbidden) {
			t.Errorf("an account that is deleted: error = %v, want errSessionChanged", err)
		}
		if called {
			t.Error("the target was read for an account that is gone")
		}
	})

	t.Run("the credentials have changed", func(t *testing.T) {
		rs := newRoleServer(t)
		check := inUpdate(rs.principal("editor"), named, nil, account.EditEntry)
		rs.clock.advance(time.Minute) // the time of the credentials is that of the clock
		if _, err := rs.accounts.Reset(t.Context(), account.System, rs.people["editor"].ID); err != nil {
			t.Fatal(err)
		}
		if err := rs.check(t, check); !errors.Is(err, errSessionChanged) {
			t.Errorf("an account whose credentials were reset: error = %v, want errSessionChanged", err)
		}
	})

	t.Run("the account is disabled", func(t *testing.T) {
		rs := newRoleServer(t)
		check := inUpdate(rs.principal("editor"), named, nil, account.EditEntry)
		if _, err := rs.accounts.Disable(t.Context(), account.System, rs.people["editor"].ID); err != nil {
			t.Fatal(err)
		}
		if err := rs.check(t, check); !errors.Is(err, account.ErrForbidden) {
			t.Errorf("an account that is disabled: error = %v, want a refusal", err)
		}
	})
}

func TestDenialLimit(t *testing.T) {
	rs := newRoleServer(t)
	refuse := func(i int) {
		rec := httptest.NewRecorder()
		target := audit.Target{Kind: "term", ID: someTerm}
		if rs.allow(rec, rs.request(http.MethodPut, "/api/test", ""), rs.principal("viewer"), account.EditTerms, account.Target{}, target) {
			t.Fatalf("refusal %d: allow = true", i)
		}
		if rec.Code != http.StatusForbidden {
			t.Fatalf("refusal %d has the status %d, want 403 whether it is recorded or not", i, rec.Code)
		}
	}
	suppressed := func(r audit.Record) string { return r.Detail["suppressed"] }

	// denialsPerMinute are recorded in a minute, and the others are counted.
	for i := range denialsPerMinute + 5 {
		refuse(i)
	}
	got := rs.refusals(t)
	if len(got) != denialsPerMinute {
		t.Fatalf("%d refusals in a minute made %d records, want %d", denialsPerMinute+5, len(got), denialsPerMinute)
	}
	for _, r := range got {
		if suppressed(r) != "" {
			t.Errorf("a record in the first minute tells of suppressed refusals: %+v", r)
		}
	}

	// The next minute's first record tells of the refusals that were not recorded, and the one after it of none.
	rs.clock.advance(time.Minute)
	refuse(100)
	refuse(101)
	got = rs.refusals(t)
	if len(got) != denialsPerMinute+2 {
		t.Fatalf("the next minute made %d records, want 2", len(got)-denialsPerMinute)
	}
	if first, second := got[denialsPerMinute], got[denialsPerMinute+1]; suppressed(first) != "5" || suppressed(second) != "" ||
		first.Detail["permission"] != "term.edit" {
		t.Errorf("the records of the next minute are %+v and %+v, want suppressed 5 in the first, and none in the second", first, second)
	}

	// A flood in a minute is counted up to the next record, however many minutes later it comes.
	rs.clock.advance(time.Minute)
	recorded := len(rs.refusals(t))
	for i := range denialsPerMinute + 3 {
		refuse(200 + i)
	}
	rs.clock.advance(5 * time.Minute)
	refuse(400)
	got = rs.refusals(t)
	if last := got[len(got)-1]; suppressed(last) != "3" || len(got) != recorded+denialsPerMinute+1 {
		t.Errorf("after a flood the last record is %+v among %d, want suppressed 3 among %d", last, len(got), recorded+denialsPerMinute+1)
	}
	if logged := rs.logs.String(); logged != "" {
		t.Errorf("the refusals have logged %q", logged)
	}
}

func TestDenialRecordFails(t *testing.T) {
	refuse := func(rs *roleServer) *http.Response {
		rec := httptest.NewRecorder()
		ok := rs.allow(rec, rs.request(http.MethodPut, "/api/test", ""), rs.principal("viewer"), account.EditTerms, account.Target{},
			audit.Target{Kind: "term", ID: someTerm})
		if ok {
			t.Fatal("allow = true")
		}
		return rec.Result()
	}

	t.Run("a closed store", func(t *testing.T) {
		rs := newRoleServer(t)
		if err := rs.db.Close(); err != nil {
			t.Fatal(err)
		}
		checkProblem(t, "a refusal that cannot be recorded", refuse(rs), "forbidden")
		if logged := rs.logs.String(); !strings.HasPrefix(logged, "cannot record access.denied: ") || strings.Count(logged, "\n") != 1 {
			t.Errorf("the log is %q, want one line that starts with cannot record access.denied: ", logged)
		}
		if ok, taken := rs.denials.admit(rs.clock.now()); !ok || taken != 1 {
			t.Errorf("after the failure the next record is admitted: %v and tells of %d refusals, want true and 1", ok, taken)
		}
	})

	t.Run("a store that is busy", func(t *testing.T) {
		rs := newRoleServer(t)
		rs.denialWait = 50 * time.Millisecond
		started, release := make(chan struct{}), make(chan struct{})
		done := async(func() error {
			return rs.db.Update(t.Context(), func(*store.Tx) error {
				close(started)
				<-release
				return nil
			})
		})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		t.Cleanup(unblock)
		<-started

		begin := time.Now()
		checkProblem(t, "a refusal that waits for the store", refuse(rs), "forbidden")
		if took := time.Since(begin); took < 40*time.Millisecond || took > 5*time.Second {
			t.Errorf("the refusal waited %v for the store, want 50ms", took)
		}
		if logged := rs.logs.String(); !strings.HasPrefix(logged, "cannot record access.denied: ") || !strings.Contains(logged, "deadline") {
			t.Errorf("the log is %q, want that the record could not be written in time", logged)
		}
		if n := rs.denials.heldNow(); n != 1 {
			t.Errorf("the refusals that are not recorded: %d, want 1", n)
		}

		unblock()
		if err := receive(t, done, "the Update that holds the store"); err != nil {
			t.Fatal(err)
		}
		rs.logs.Reset()
		checkProblem(t, "the next refusal", refuse(rs), "forbidden")
		got := rs.refusals(t)
		if len(got) != 1 || got[0].Detail["suppressed"] != "1" {
			t.Errorf("the record after the failure is %+v, want one that tells of 1 refusal that was not recorded", got)
		}
		if logged := rs.logs.String(); logged != "" {
			t.Errorf("the log is %q after the store answers again, want nothing", logged)
		}
	})
}

// heldNow returns the number of refusals that are not recorded since the last record.
func (d *denials) heldNow() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.held
}

// TestInUpdateOfASession shows the check for the Principal of a session, which is how a route gets it: the credentials
// that the session saw are those of the account until they are changed.
func TestInUpdateOfASession(t *testing.T) {
	a := newAPIServer(t)
	named := audit.Target{Kind: "entry", ID: someEntry}
	p := a.principal(t, "editor")
	if p.Account.CredentialsChangedAt.IsZero() {
		t.Fatal("the account of a session has no time of its credentials")
	}
	check := inUpdate(p, named, nil, account.EditEntry)
	var err error
	if verr := a.db.View(t.Context(), func(tx *store.Tx) error {
		err = check(tx)
		return nil
	}); verr != nil || err != nil {
		t.Fatalf("the editor of a session edits entries: %v, %v, want nil", verr, err)
	}

	a.clock.advance(time.Minute)
	if _, err := a.accounts.Reset(t.Context(), account.System, a.people["editor"].account.ID); err != nil {
		t.Fatal(err)
	}
	if verr := a.db.View(t.Context(), func(tx *store.Tx) error {
		err = check(tx)
		return nil
	}); verr != nil || !errors.Is(err, errSessionChanged) {
		t.Errorf("an editor whose credentials were reset: %v, %v, want errSessionChanged", verr, err)
	}
}
