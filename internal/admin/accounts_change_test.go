package admin

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
)

const missingUser = "usr_01JB8T7H4MA2C5E8G1J4K7N3ZZ" // the form of the ID of an account, and none that the store has

// patch sends PATCH to the account id.
func (p asPerson) patch(id, body string, headers ...string) *http.Response {
	return p.send(http.MethodPatch, "/api/accounts/"+id, body, headers...)
}

// lastRecord returns the last record of the activity log whose action is the one.
func (a *apiServer) lastRecord(t *testing.T, action audit.Action) audit.Record {
	t.Helper()
	records := a.records(t, action)
	if len(records) == 0 {
		t.Fatalf("the activity log has no record of %s", action)
	}
	return records[len(records)-1]
}

// TestAccountUpdate shows who changes what of an account with PATCH: an account changes its own name and email address, and an
// administrator changes anything of any account, and every refusal is recorded with the permission that the change needed.
func TestAccountUpdate(t *testing.T) {
	a := newAPIServer(t)
	editorID, adminID := a.people["editor"].account.ID, a.people["admin"].account.ID
	editor, admin := a.as("editor"), a.as("admin")
	user := func(id string) audit.Target { return audit.Target{Kind: "user", ID: id} }

	// An account changes its own name and its own email address.
	reply := readObject(t, "PATCH its own name and email", editor.patch(editorID, `{"name":"Edited Editor","email":"Edited@Example.com"}`), http.StatusOK)
	if got := reply.names(); !slices.Equal(got, []string{"account"}) {
		t.Errorf("PATCH answers the members %v, want account", got)
	}
	acct := reply.sub(t, "account")
	for name, want := range map[string]string{"id": editorID, "login": "editor", "name": "Edited Editor", "email": "Edited@example.com", "role": "editor", "status": "active"} {
		if got := acct.str(t, name); got != want {
			t.Errorf("the account has the %s %q, want %q", name, got, want)
		}
	}
	if got := acct.names(); !slices.Equal(got, fullKeys) {
		t.Errorf("the account has the members %v, want %v", got, fullKeys)
	}
	if got := readObject(t, "GET /api/session", a.getSession(a.cookie(a.people["editor"].token)...), http.StatusOK).sub(t, "account").str(t, "name"); got != "Edited Editor" {
		t.Errorf("the session shows the name %q, want the new one", got)
	}
	if r := a.lastRecord(t, audit.AccountUpdate); r.Actor != editorID || r.Target != user(editorID) || r.Detail["fields"] != "email, name" || !r.Changed {
		t.Errorf("the record of account.update is %+v, want the editor's change of email and name", r)
	}
	// A change of nothing is allowed to everybody for its own account, and says so in the record.
	body(t, "PATCH {} by a viewer", a.as("viewer").patch(a.people["viewer"].account.ID, `{}`), http.StatusOK)
	if r := a.lastRecord(t, audit.AccountUpdate); r.Changed || len(r.Detail) != 0 {
		t.Errorf("the record of a change of nothing is %+v, want no change", r)
	}
	// An email address that another account has is a problem that does not say which account.
	res := editor.patch(editorID, `{"email":"admin@example.com"}`)
	if p := readProblem(t, res); res.StatusCode != http.StatusUnprocessableEntity || len(p.Errors) != 1 || p.Errors[0].Pointer != "/email" ||
		strings.Contains(p.Errors[0].Detail, adminID) || !strings.Contains(p.Errors[0].Detail, "another account") {
		t.Errorf("an email address that another has: the status %d and %+v, want a problem at /email that does not say whose", res.StatusCode, p)
	}

	// What needs accounts.manage is refused to the account itself, and nothing of it is changed.
	for _, tt := range []struct{ name, body string }{
		{"its role", `{"role":"admin"}`},
		{"its login name", `{"login":"boss"}`},
		{"its author profile", `{"author":"` + yamada + `"}`},
		{"its name and its role", `{"name":"Boss","role":"admin"}`},
	} {
		a.wantForbidden(t, "editor", write{"change " + tt.name, http.MethodPatch, "/api/accounts/" + editorID, tt.body, account.ManageAccounts, user(editorID)})
	}
	a.wantForbidden(t, "editor", write{"change the name of another", http.MethodPatch, "/api/accounts/" + adminID, `{"name":"Mine"}`, account.ManageAccounts, user(adminID)})
	a.wantForbidden(t, "viewer", write{"change the name of another", http.MethodPatch, "/api/accounts/" + editorID, `{}`, account.ManageAccounts, user(editorID)})
	if got, err := a.accounts.Get(t.Context(), editorID); err != nil || got.Role != account.RoleEditor || got.Login != "editor" || got.Name != "Edited Editor" {
		t.Errorf("the editor is %+v, with the error %v, want it as it was after its own change", got, err)
	}

	// An administrator changes the login name, which keeps the session, and the author profile and back.
	reply = readObject(t, "PATCH the login name", admin.patch(editorID, `{"login":"Editor.Two"}`), http.StatusOK)
	if got := reply.sub(t, "account").str(t, "login"); got != "editor.two" {
		t.Errorf("the login name is %q, want the normalized one", got)
	}
	body(t, "GET /api/session after the change of the login name", a.getSession(a.cookie(a.people["editor"].token)...), http.StatusOK)
	reply = readObject(t, "PATCH the author profile", admin.patch(editorID, `{"author":"`+yamada+`"}`), http.StatusOK)
	if got := reply.sub(t, "account").str(t, "author"); got != yamada {
		t.Errorf("the author profile is %q, want %q", got, yamada)
	}
	reply = readObject(t, "PATCH without the author profile", admin.patch(editorID, `{"author":""}`), http.StatusOK)
	if _, there := reply.sub(t, "account")["author"]; there {
		t.Errorf("the account has an author profile after it was set to empty: %v", reply.sub(t, "account"))
	}
	if got, err := a.accounts.Get(t.Context(), editorID); err != nil || got.Author != "" {
		t.Errorf("the account is %+v, with the error %v, want no author profile", got, err)
	}

	// A change of the role ends the sessions of the account, and the next login is the new role.
	reply = readObject(t, "PATCH the role", admin.patch(editorID, `{"role":"author"}`), http.StatusOK)
	if got := reply.sub(t, "account").str(t, "role"); got != "author" {
		t.Errorf("the role is %q, want author", got)
	}
	res = a.getSession(a.cookie(a.people["editor"].token)...)
	checkProblem(t, "GET /api/session after the change of the role", res, "unauthenticated")
	if r := a.lastRecord(t, audit.AccountUpdate); r.Actor != adminID || r.Target != user(editorID) || r.Detail["roleBefore"] != "editor" || r.Detail["roleAfter"] != "author" {
		t.Errorf("the record of account.update is %+v, want the change of the role by the admin", r)
	}

	// The problems of the values are those of the field, and a change that has one changes nothing.
	for _, tt := range []struct{ name, body, pointer string }{
		{"a login name that another has", `{"login":"VIEWER"}`, "/login"},
		{"a login name that is none", `{"login":"Not A Login!"}`, "/login"},
		{"an empty name", `{"name":""}`, "/name"},
		{"an email address that is none", `{"email":"nobody"}`, "/email"},
		{"an email address that another has", `{"email":"Viewer@example.com"}`, "/email"},
		{"a role that is none", `{"role":"owner"}`, "/role"},
		{"an empty role", `{"role":""}`, "/role"},
		{"an author profile that is not there", `{"author":"aut_01JBA4C7N9Q3T6W9Z3C6F9H2ZZ"}`, "/author"},
	} {
		res := admin.patch(editorID, tt.body)
		if p := readProblem(t, res); res.StatusCode != http.StatusUnprocessableEntity || p.Code != "invalid" || len(p.Errors) != 1 || p.Errors[0].Pointer != tt.pointer {
			t.Errorf("a change with %s has the status %d and %+v, want the problem invalid at %s", tt.name, res.StatusCode, p, tt.pointer)
		}
	}
	// The administrator is the only one: it cannot give up its role.
	checkProblem(t, "PATCH the role of the last administrator", admin.patch(adminID, `{"role":"editor"}`), "last-admin")
	if got, err := a.accounts.Get(t.Context(), adminID); err != nil || got.Role != account.RoleAdmin {
		t.Errorf("the administrator is %+v, with the error %v, want it as it was", got, err)
	}

	// What is not a change.
	for _, tt := range []struct{ name, body string }{
		{"null for the name", `{"name":null}`},
		{"a number for the name", `{"name":5}`},
		{"a list for the role", `{"role":["admin"]}`},
		{"null for the author", `{"author":null}`},
		{"another member", `{"name":"x","password":"y"}`},
		{"a member twice", `{"name":"x","name":"y"}`},
		{"an array", `[]`},
		{"null", `null`},
		{"a string", `"name"`},
		{"an empty body", ``},
	} {
		checkProblem(t, "PATCH with "+tt.name, admin.send(http.MethodPatch, "/api/accounts/"+editorID, tt.body, withBody(tt.body)...), "bad-request")
	}
	checkProblem(t, "PATCH with a key", admin.patch(editorID, `{}`, key(1)...), "bad-header")
	checkProblem(t, "PATCH with a query", admin.send(http.MethodPatch, "/api/accounts/"+editorID+"?x=1", `{}`), "bad-query")
	checkProblem(t, "PATCH of another media type", admin.patch(editorID, `{}`, "Content-Type", "text/plain"), "unsupported-media-type")
	checkProblem(t, "PATCH with a body that is too large", admin.patch(editorID, strings.Repeat(" ", smallBodyLimit+1)), "too-large")
	checkProblem(t, "PATCH of an account that is not there", admin.patch(missingUser, `{"name":"x"}`), "not-found")
	checkProblem(t, "PATCH of a word", admin.patch("x", `{"name":"x"}`), "not-found")
	checkProblem(t, "PATCH of an account that is not there by a viewer", a.as("viewer").patch(missingUser, `{"name":"x"}`), "forbidden")
	checkProblem(t, "PATCH without a session", a.send(http.MethodPatch, "/api/accounts/"+editorID, `{}`, "Origin", a.origin, "Content-Type", "application/json"), "unauthenticated")
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestAccountOperations shows the operations on an account that are the administrator's: what each does to the account and
// to its sessions, the token that a reset gives, and the refusals.
func TestAccountOperations(t *testing.T) {
	a := newAPIServer(t)
	admin := a.as("admin")
	adminID, viewerID := a.people["admin"].account.ID, a.people["viewer"].account.ID
	item := "/api/accounts/" + viewerID
	recorded := func(action audit.Action, id string) {
		t.Helper()
		if r := a.lastRecord(t, action); r.Actor != adminID || r.Target.ID != id {
			t.Errorf("the last record of %s is %+v, want the admin's, about %s", action, r, id)
		}
	}

	// Disabling ends the sessions, and an account that is disabled cannot be reset until it is enabled.
	reply := readObject(t, "disable", admin.post(item+"/disable", ""), http.StatusOK)
	if got := reply.names(); !slices.Equal(got, []string{"account"}) || reply.sub(t, "account").str(t, "status") != "disabled" {
		t.Errorf("the disabling answers %v, want the account as a disabled one", reply)
	}
	recorded(audit.AccountDisable, viewerID)
	checkProblem(t, "GET /api/session of a disabled account", a.getSession(a.cookie(a.people["viewer"].token)...), "unauthenticated")
	checkProblem(t, "login of a disabled account", a.postLogin("viewer", testPassword("viewer")), "bad-credentials")
	checkProblem(t, "the reset of a disabled account", admin.post(item+"/reset", ""), "disabled")
	if got := readObject(t, "GET the disabled account", admin.get(item), http.StatusOK); got.str(t, "status") != "disabled" || got["token"] != nil {
		t.Errorf("the account is %v, want a disabled one without a token", got)
	}
	body(t, "disable again", admin.post(item+"/disable", ""), http.StatusOK)

	// Enabling gives the account its password back, and no session.
	reply = readObject(t, "enable", admin.post(item+"/enable", ""), http.StatusOK)
	if reply.sub(t, "account").str(t, "status") != "active" {
		t.Errorf("the enabling answers %v, want an active account", reply)
	}
	recorded(audit.AccountEnable, viewerID)
	checkProblem(t, "GET /api/session of an account that is enabled again", a.getSession(a.cookie(a.people["viewer"].token)...), "unauthenticated")
	a.people["viewer"].token = a.logIn(t, "viewer")
	body(t, "enable again", admin.post(item+"/enable", ""), http.StatusOK)

	// A reset takes the password away and ends the sessions, and its token sets a new one.
	reset := readObject(t, "reset", admin.post(item+"/reset", ""), http.StatusOK)
	if got := reset.names(); !slices.Equal(got, []string{"expiresAt", "token"}) {
		t.Errorf("the reset answers the members %v, want expiresAt and token", got)
	}
	token := reset.str(t, "token")
	if !tokenText.MatchString(token) {
		t.Errorf("the reset gives a token of %d characters, want one of 43 that base64url writes", len(token))
	}
	if got := reset.str(t, "expiresAt"); got != a.clock.now().Add(24*time.Hour).Format(time.RFC3339) {
		t.Errorf("the reset expires at %s, want a day from the clock", got)
	}
	recorded(audit.AccountReset, viewerID)
	checkProblem(t, "GET /api/session after a reset", a.getSession(a.cookie(a.people["viewer"].token)...), "unauthenticated")
	checkProblem(t, "login after a reset", a.postLogin("viewer", testPassword("viewer")), "bad-credentials")
	waiting := readObject(t, "GET the account after the reset", admin.get(item), http.StatusOK)
	if waiting.str(t, "status") != "pending" || waiting.sub(t, "token").str(t, "purpose") != "reset" ||
		waiting.sub(t, "token").str(t, "expiresAt") != reset.str(t, "expiresAt") {
		t.Errorf("the account is %v after the reset, want a pending one with a reset token that ends when the reset says", waiting)
	}
	if text := bodyOf(t, admin.get("/api/accounts")); strings.Contains(text, token) {
		t.Error("GET /api/accounts shows the token of the reset")
	}
	const afterReset = "the viewer has a password again"
	body(t, "POST /api/password with the token of the reset", a.postPassword(token, afterReset), http.StatusOK)
	if got := a.postLogin("viewer", afterReset); got.StatusCode != http.StatusOK {
		t.Errorf("the login with the new password has the status %d, want 200", got.StatusCode)
	}

	// An account that has never had a credential is reset for a week, as an invitation is.
	pending := a.inviteAccount(t, "pending", account.RoleViewer)
	reset = readObject(t, "reset of an invitation", admin.post("/api/accounts/"+pending.ID+"/reset", ""), http.StatusOK)
	if got := reset.str(t, "expiresAt"); got != a.clock.now().Add(7*24*time.Hour).Format(time.RFC3339) {
		t.Errorf("the reset of an account that never had a credential expires at %s, want a week from the clock", got)
	}

	// Deleting removes the account, and its sessions with it.
	res := admin.del(item)
	if text := bodyOf(t, res); res.StatusCode != http.StatusNoContent || text != "" || res.Header.Get("Content-Type") != "" {
		t.Errorf("DELETE has the status %d, the body %q and the Content-Type %q, want 204 with nothing", res.StatusCode, text, res.Header.Get("Content-Type"))
	}
	recorded(audit.AccountDelete, viewerID)
	checkProblem(t, "GET the deleted account", admin.get(item), "not-found")
	checkProblem(t, "DELETE the deleted account", admin.del(item), "not-found")
	checkProblem(t, "GET /api/session of the deleted account", a.getSession(a.cookie(a.people["viewer"].token)...), "unauthenticated")

	// The last administrator cannot be lost by its own hand, in any way.
	for what, res := range map[string]*http.Response{
		"disabling": admin.post("/api/accounts/"+adminID+"/disable", ""),
		"deleting":  admin.del("/api/accounts/" + adminID),
		"reset":     admin.post("/api/accounts/"+adminID+"/reset", ""),
	} {
		checkProblem(t, what+" the last administrator", res, "last-admin")
	}
	if got, err := a.accounts.Get(t.Context(), adminID); err != nil || got.Status != account.StatusActive || got.Role != account.RoleAdmin {
		t.Errorf("the administrator is %+v, with the error %v, want it as it was", got, err)
	}
	body(t, "GET /api/session of the administrator", a.getSession(a.cookie(a.people["admin"].token)...), http.StatusOK)

	// An account that is not there, and a path that is not an ID.
	for _, op := range []struct{ method, suffix string }{
		{http.MethodPost, "/disable"}, {http.MethodPost, "/enable"}, {http.MethodPost, "/reset"}, {http.MethodDelete, ""},
	} {
		for what, id := range map[string]string{"an account that is not there": missingUser, "a word": "x", "an ID of another kind": yamada} {
			checkProblem(t, op.method+" "+op.suffix+" of "+what, admin.send(op.method, "/api/accounts/"+id+op.suffix, ""), "not-found")
		}
		path := "/api/accounts/" + adminID + op.suffix
		checkProblem(t, op.method+" "+op.suffix+" with a key", admin.send(op.method, path, "", key(1)...), "bad-header")
		checkProblem(t, op.method+" "+op.suffix+" with a query", admin.send(op.method, path+"?x=1", ""), "bad-query")
		checkProblem(t, op.method+" "+op.suffix+" without a session", a.send(op.method, path, "", "Origin", a.origin), "unauthenticated")
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestAccountOperationsForbidden shows that every operation on an account is refused to the roles that are not the
// administrator, whoever the account is, and that the refusal is recorded with the permission and the account.
func TestAccountOperationsForbidden(t *testing.T) {
	a := newAPIServer(t)
	adminID, viewerID, editorID := a.people["admin"].account.ID, a.people["viewer"].account.ID, a.people["editor"].account.ID
	for _, login := range []string{"editor", "author", "viewer"} {
		own := a.people[login].account.ID
		for _, id := range []string{adminID, viewerID, own} {
			target := audit.Target{Kind: "user", ID: id}
			item := "/api/accounts/" + id
			for _, w := range []write{
				{"disable", http.MethodPost, item + "/disable", "", account.ManageAccounts, target},
				{"enable", http.MethodPost, item + "/enable", "", account.ManageAccounts, target},
				{"reset", http.MethodPost, item + "/reset", "", account.ManageAccounts, target},
				{"delete", http.MethodDelete, item, "", account.ManageAccounts, target},
			} {
				a.wantForbidden(t, login, w)
			}
		}
		// The target of the record is only what the path names, if it is an account's ID.
		a.wantForbidden(t, login, write{"delete a word", http.MethodDelete, "/api/accounts/x", "", account.ManageAccounts, audit.Target{}})
	}
	for _, id := range []string{adminID, viewerID, editorID} {
		if got, err := a.accounts.Get(t.Context(), id); err != nil || got.Status != account.StatusActive {
			t.Errorf("the account %s is %+v, with the error %v, want it active", id, got, err)
		}
	}
	for _, login := range []string{"admin", "editor", "author", "viewer"} {
		body(t, "GET /api/session of "+login, a.getSession(a.cookie(a.people[login].token)...), http.StatusOK)
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestAccountsRoleChangesInUpdate shows that the accounts check the permission again in their Update: an administrator who
// is demoted after the route has checked its request but before the operation is refused, and nothing is changed.
func TestAccountsRoleChangesInUpdate(t *testing.T) {
	a := newAPIServer(t)
	victim := a.people["viewer"].account.ID
	item := "/api/accounts/" + victim
	target := audit.Target{Kind: "user", ID: victim}
	for i, op := range []struct {
		name, method, path, body string
		target                   audit.Target
	}{
		{"invite", http.MethodPost, "/api/accounts", newbieBody, audit.Target{}},
		{"update", http.MethodPatch, item, `{"name":"Changed"}`, target},
		{"disable", http.MethodPost, item + "/disable", "", target},
		{"enable", http.MethodPost, item + "/enable", "", target},
		{"reset", http.MethodPost, item + "/reset", "", target},
		{"delete", http.MethodDelete, item, "", target},
	} {
		t.Run(op.name, func(t *testing.T) {
			login := "boss" + strconv.Itoa(i)
			boss := a.victim(t, login, account.RoleAdmin)
			before, err := a.accounts.List(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var fired atomic.Int32
			a.hook = func(point string) {
				if point == "checked" && fired.Add(1) == 1 {
					down := account.RoleEditor
					if _, err := a.accounts.Update(t.Context(), account.System, boss, account.Change{Role: &down}); err != nil {
						t.Errorf("Update() error = %v", err)
					}
				}
			}
			a.clock.advance(time.Minute)
			n := len(a.records(t, audit.AccessDenied))
			checkProblem(t, op.name+" by an administrator who is demoted", a.as(login).send(op.method, op.path, op.body), "forbidden")
			a.hook = nil
			if fired.Load() != 1 {
				t.Fatalf("the hook ran %d times, want 1", fired.Load())
			}
			records := a.records(t, audit.AccessDenied)
			if len(records) != n+1 {
				t.Fatalf("the refusal made %d records of access.denied, want 1", len(records)-n)
			}
			if r := records[n]; r.Actor != boss || r.Target != op.target || r.Detail["permission"] != string(account.ManageAccounts) {
				t.Errorf("the record is %+v, want the actor %s, the permission accounts.manage and the target %+v", r, boss, op.target)
			}
			after, err := a.accounts.List(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			// The demoted account differs; nothing else may, and no account is made.
			want := slices.DeleteFunc(slices.Clone(before), func(x account.Account) bool { return x.ID == boss })
			got := slices.DeleteFunc(slices.Clone(after), func(x account.Account) bool { return x.ID == boss })
			if !slices.Equal(got, want) {
				t.Errorf("the accounts are\n%+v\nwant\n%+v", got, want)
			}
		})
	}
}

// TestAccountRoutesRecordNothingMore shows that the routes that read the accounts, and the refusals of requests that are not
// well formed, write no record: only a refusal of a permission and an operation that is done are recorded.
func TestAccountRoutesRecordNothingMore(t *testing.T) {
	a := newAPIServer(t)
	admin := a.as("admin")
	count := func() int {
		n := 0
		for _, prefix := range []string{"account.", "access."} {
			err := a.db.ReadAudit(t.Context(), audit.Filter{Log: audit.ActivityLog, Action: prefix}, func(audit.Record) bool { n++; return true })
			if err != nil {
				t.Fatal(err)
			}
		}
		return n
	}
	before := count()
	for _, path := range []string{"/api/accounts", "/api/accounts/" + a.people["editor"].account.ID, "/api/accounts?limit=x", "/api/accounts/x"} {
		admin.get(path)
	}
	admin.post("/api/accounts", `{"login":5}`)
	admin.post("/api/accounts", newbieBody, key(1)...)
	admin.patch("x", `{}`)
	if got := count(); got != before {
		t.Errorf("reading and requests that were refused wrote %d records, want none", got-before)
	}
}
