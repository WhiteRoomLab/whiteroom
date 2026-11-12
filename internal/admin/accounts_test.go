package admin

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// object is a JSON object whose members are kept as the JSON that was sent.
type object map[string]jsontext.Value

// readObject reads the body of res, which must have the status and be a JSON object.
func readObject(t *testing.T, what string, res *http.Response, status int) object {
	t.Helper()
	text := body(t, what, res, status)
	var o object
	if err := json.Unmarshal([]byte(text), &o); err != nil {
		t.Fatalf("%s: the answer %.300s is not a JSON object: %v", what, text, err)
	}
	return o
}

func (o object) names() []string { return slices.Sorted(maps.Keys(o)) }

// str returns the string that the member name holds.
func (o object) str(t *testing.T, name string) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(o[name], &s); err != nil {
		t.Fatalf("the member %q is %s, not a string", name, o[name])
	}
	return s
}

// sub returns the object that the member name holds.
func (o object) sub(t *testing.T, name string) object {
	t.Helper()
	var s object
	if err := json.Unmarshal(o[name], &s); err != nil {
		t.Fatalf("the member %q is %s, not an object", name, o[name])
	}
	return s
}

// accountPage is the answer to GET /api/accounts.
type accountPage struct {
	Items  []object `json:"items"`
	Total  int      `json:"total"`
	Offset int      `json:"offset"`
	Limit  int      `json:"limit"`
}

func readAccountPage(t *testing.T, what string, res *http.Response) accountPage {
	t.Helper()
	text := body(t, what, res, http.StatusOK)
	var p accountPage
	if err := json.Unmarshal([]byte(text), &p); err != nil {
		t.Fatalf("%s: the answer %.300s is not a page of accounts: %v", what, text, err)
	}
	return p
}

// The members of an account as the administrator and the account itself see it, for an account that has a password, a name and
// an email address and nothing else, and as the others see it.
var (
	fullKeys  = []string{"createdAt", "credentialsChangedAt", "email", "id", "login", "name", "role", "status"}
	briefKeys = []string{"id", "name", "role"}
)

// hashesOf returns the hash of the password and the hash of the token that the store keeps for the account, which no answer
// may show.
func (a *apiServer) hashesOf(t *testing.T, id string) []string {
	t.Helper()
	var data []byte
	err := a.db.View(t.Context(), func(tx *store.Tx) error {
		d, ok, err := tx.Account(id)
		if err == nil && !ok {
			t.Fatalf("the store has no row of %s", id)
		}
		data = d
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var row struct {
		Password string `json:"password"`
		Token    struct {
			Hash string `json:"hash"`
		} `json:"token"`
	}
	if err := json.Unmarshal(data, &row); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, h := range []string{row.Password, row.Token.Hash, strings.TrimPrefix(row.Token.Hash, "sha256:")} {
		if h != "" {
			out = append(out, h)
		}
	}
	return out
}

// noSecrets checks that the text of the answer to what shows none of the hashes that the store keeps for the accounts.
func (a *apiServer) noSecrets(t *testing.T, what, text string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		for _, h := range a.hashesOf(t, id) {
			if strings.Contains(text, h) {
				t.Errorf("%s shows a hash of %s", what, id)
			}
		}
	}
}

var secretMember = regexp.MustCompile(`(?i)"[a-z]*(?:password|hash|secret)[a-z]*"\s*:`)

// noSecretMembers checks that the answer to what, an account or a list of them, has no member whose name says that it holds a
// password, a hash or a secret.
func noSecretMembers(t *testing.T, what, text string) {
	t.Helper()
	if m := secretMember.FindString(text); m != "" {
		t.Errorf("%s has the member %s", what, m)
	}
}

// TestAccountsRead shows what each role sees of the accounts: the administrator and the account itself see all of an account
// but its secrets, and the others only who it is, enough to show who made or saved something.
func TestAccountsRead(t *testing.T) {
	a := newAPIServer(t)
	ids := map[string]string{}
	for login, p := range a.people {
		ids[login] = p.account.ID
	}
	if _, err := a.accounts.Update(t.Context(), account.System, ids["author"], account.Change{Author: ptr(yamada)}); err != nil {
		t.Fatal(err)
	}
	pending := a.inviteAccount(t, "pending", account.RoleViewer)
	ids["pending"] = pending.ID
	all := make([]string, 0, len(ids))
	for _, id := range ids {
		all = append(all, id)
	}
	slices.Sort(all)

	// The administrator sees all of every account.
	res := a.as("admin").get("/api/accounts")
	text := bodyOf(t, res)
	a.noSecrets(t, "GET /api/accounts as admin", text, all...)
	noSecretMembers(t, "GET /api/accounts as admin", text)
	var list accountPage
	if err := json.Unmarshal([]byte(text), &list); err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/accounts as admin has the status %d and %.300s: %v", res.StatusCode, text, err)
	}
	if list.Total != 5 || list.Offset != 0 || list.Limit != 50 || len(list.Items) != 5 {
		t.Fatalf("GET /api/accounts as admin has %d items of %d at %d with the limit %d, want 5 of 5 at 0 with 50", len(list.Items), list.Total, list.Offset, list.Limit)
	}
	var seen []string
	for _, item := range list.Items {
		id := item.str(t, "id")
		seen = append(seen, id)
		var want []string
		switch id {
		case ids["author"]:
			want = slices.Sorted(slices.Values(append(slices.Clone(fullKeys), "author")))
		case ids["pending"]:
			want = []string{"createdAt", "email", "id", "login", "name", "role", "status", "token"}
		default:
			want = fullKeys
		}
		if got := item.names(); !slices.Equal(got, want) {
			t.Errorf("the item of %s as admin has the members %v, want %v", id, got, want)
		}
	}
	if !slices.Equal(seen, all) {
		t.Errorf("GET /api/accounts lists %v, want the accounts in the order of their IDs: %v", seen, all)
	}
	editor := readObject(t, "GET the editor as admin", a.as("admin").get("/api/accounts/"+ids["editor"]), http.StatusOK)
	for name, want := range map[string]string{
		"id": ids["editor"], "login": "editor", "name": "Name of editor", "email": "editor@example.com", "role": "editor", "status": "active",
	} {
		if got := editor.str(t, name); got != want {
			t.Errorf("the editor has the %s %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{"createdAt", "credentialsChangedAt"} {
		if _, err := time.Parse(time.RFC3339, editor.str(t, name)); err != nil {
			t.Errorf("the editor has the %s %q, want a time: %v", name, editor.str(t, name), err)
		}
	}
	waiting := readObject(t, "GET the pending account as admin", a.as("admin").get("/api/accounts/"+ids["pending"]), http.StatusOK)
	token := waiting.sub(t, "token")
	if got := token.names(); !slices.Equal(got, []string{"expiresAt", "purpose"}) || token.str(t, "purpose") != "invitation" ||
		waiting.str(t, "status") != "pending" {
		t.Errorf("the pending account has the token %v and the status %q, want an invitation with the purpose and the expiry only", token, waiting.str(t, "status"))
	}
	if got := token.str(t, "expiresAt"); got != a.clock.now().Add(7*24*time.Hour).Format(time.RFC3339) {
		t.Errorf("the invitation expires at %s, want seven days from the clock", got)
	}

	// The others, an editor and a viewer, see who the accounts are and nothing of their login names and email addresses; and
	// all of their own.
	for _, login := range []string{"editor", "viewer"} {
		res := a.as(login).get("/api/accounts")
		text := bodyOf(t, res)
		a.noSecrets(t, "GET /api/accounts as "+login, text, all...)
		noSecretMembers(t, "GET /api/accounts as "+login, text)
		var list accountPage
		if err := json.Unmarshal([]byte(text), &list); err != nil || res.StatusCode != http.StatusOK || len(list.Items) != 5 {
			t.Fatalf("GET /api/accounts as %s has the status %d and %.300s: %v", login, res.StatusCode, text, err)
		}
		for _, item := range list.Items {
			id := item.str(t, "id")
			want := briefKeys
			switch id {
			case ids[login]:
				want = fullKeys
			case ids["author"]:
				want = []string{"author", "id", "name", "role"}
			}
			if got := item.names(); !slices.Equal(got, want) {
				t.Errorf("the item of %s as %s has the members %v, want %v", id, login, got, want)
			}
		}
		for _, other := range []string{"admin", "pending"} {
			if strings.Contains(text, other+"@example.com") {
				t.Errorf("GET /api/accounts as %s shows the email address of %s", login, other)
			}
		}
		for other, id := range ids {
			one := readObject(t, "GET "+other+" as "+login, a.as(login).get("/api/accounts/"+id), http.StatusOK)
			want := briefKeys
			switch other {
			case login:
				want = fullKeys
			case "author":
				want = []string{"author", "id", "name", "role"}
			}
			if got := one.names(); !slices.Equal(got, want) {
				t.Errorf("GET %s as %s has the members %v, want %v", other, login, got, want)
			}
			if other != login && one.str(t, "name") != "Name of "+other {
				t.Errorf("GET %s as %s has the name %q", other, login, one.str(t, "name"))
			}
		}
	}
	if got := readObject(t, "the author as viewer", a.as("viewer").get("/api/accounts/"+ids["author"]), http.StatusOK).str(t, "author"); got != yamada {
		t.Errorf("the author profile of the author account is %q as the viewer sees it, want %q", got, yamada)
	}
	if got := len(a.records(t, audit.AccessDenied)); got != 0 {
		t.Errorf("reading made %d records of access.denied, want none", got)
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestAccountsPages shows the pages of the list, and the answers to what the routes do not take.
func TestAccountsPages(t *testing.T) {
	a := newAPIServer(t)
	for _, login := range []string{"e", "f", "g"} {
		a.inviteAccount(t, "extra-"+login, account.RoleViewer)
	}
	as := a.as("viewer")
	var seen []string
	for offset := 0; offset < 8; offset += 3 {
		page := readAccountPage(t, "a page of the accounts", as.get("/api/accounts?limit=3&offset="+strconv.Itoa(offset)))
		if page.Total != 7 || page.Offset != offset || page.Limit != 3 || len(page.Items) != min(3, 7-offset) {
			t.Errorf("the page at %d has %d items of %d, offset %d, limit %d", offset, len(page.Items), page.Total, page.Offset, page.Limit)
		}
		for _, item := range page.Items {
			seen = append(seen, item.str(t, "id"))
		}
	}
	if len(seen) != 7 || !slices.IsSorted(seen) || len(slices.Compact(slices.Clone(seen))) != 7 {
		t.Errorf("the pages list %v, want the 7 accounts once each in the order of their IDs", seen)
	}
	if text := body(t, "a page beyond the end", as.get("/api/accounts?offset=100"), http.StatusOK); !strings.Contains(text, `"items":[]`) {
		t.Errorf("a page beyond the end is %s, want an empty list and not null", text)
	}

	for _, q := range []string{"?limit=0", "?limit=101", "?limit=x", "?offset=-1", "?offset=x", "?limit=1&limit=2", "?q=a", "?role=admin"} {
		checkProblem(t, "GET /api/accounts"+q, as.get("/api/accounts"+q), "bad-query")
	}
	checkProblem(t, "GET /api/accounts with a key", as.send(http.MethodGet, "/api/accounts", "", key(1)...), "bad-header")
	checkProblem(t, "GET an account with a key", as.send(http.MethodGet, "/api/accounts/"+a.people["admin"].account.ID, "", key(1)...), "bad-header")
	checkProblem(t, "GET an account with a query", as.get("/api/accounts/"+a.people["admin"].account.ID+"?x=1"), "bad-query")
	for name, path := range map[string]string{
		"an account that is not there": "/api/accounts/usr_01JB8T7H4MA2C5E8G1J4K7N3ZZ",
		"a word":                       "/api/accounts/x",
		"an ID of another kind":        "/api/accounts/" + yamada,
	} {
		checkProblem(t, "GET "+name, as.get(path), "not-found")
	}
	checkProblem(t, "no session", a.send(http.MethodGet, "/api/accounts", ""), "unauthenticated")
	checkProblem(t, "no session for an account", a.send(http.MethodGet, "/api/accounts/"+a.people["admin"].account.ID, ""), "unauthenticated")
}

var tokenText = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// postPassword sends POST /api/password, which needs no session, from the origin of the server. The password is watched for.
func (a *apiServer) postPassword(token, password string, headers ...string) *http.Response {
	a.seen.password(password)
	headers = append([]string{"Origin", a.origin, "Content-Type", "application/json"}, headers...)
	return a.send(http.MethodPost, "/api/password", `{"token":`+quote(token)+`,"password":`+quote(password)+`}`, headers...)
}

func quote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// invitation reads the answer to an invitation, 201.
func invitation(t *testing.T, res *http.Response) (a object, token string) {
	t.Helper()
	location := res.Header.Get("Location")
	o := readObject(t, "POST /api/accounts", res, http.StatusCreated)
	if got := o.names(); !slices.Equal(got, []string{"account", "expiresAt", "token"}) {
		t.Fatalf("the invitation has the members %v, want account, expiresAt and token", got)
	}
	acct := o.sub(t, "account")
	if want := "/api/accounts/" + acct.str(t, "id"); location != want {
		t.Errorf("the invitation has the Location %q, want %q", location, want)
	}
	token = o.str(t, "token")
	if !tokenText.MatchString(token) {
		t.Errorf("the invitation has a token of %d characters, want one of 43 that base64url writes", len(token))
	}
	if got, want := o.str(t, "expiresAt"), acct.sub(t, "token").str(t, "expiresAt"); got != want {
		t.Errorf("the invitation expires at %s, but the account says %s", got, want)
	}
	return acct, token
}

const (
	newbieBody = `{"login":"newbie","name":"New Person","email":"newbie@example.com","role":"editor"}`
	newbiePass = "newbie has a long password"
	tinyPass   = "tiny-pw1" // a password that the rules refuse as too short
)

// TestAccountInvite shows the invitation: the answer holds the token, the account is pending until the password is set with
// the token, and the token is in no other answer.
func TestAccountInvite(t *testing.T) {
	a := newAPIServer(t)
	admin := a.as("admin")
	adminID := a.people["admin"].account.ID

	acct, token := invitation(t, admin.post("/api/accounts", newbieBody))
	id := acct.str(t, "id")
	want := map[string]string{
		"login": "newbie", "name": "New Person", "email": "newbie@example.com", "role": "editor", "status": "pending", "createdBy": adminID,
	}
	for name, v := range want {
		if got := acct.str(t, name); got != v {
			t.Errorf("the account has the %s %q, want %q", name, got, v)
		}
	}
	if got := acct.names(); !slices.Equal(got, []string{"createdAt", "createdBy", "email", "id", "login", "name", "role", "status", "token"}) {
		t.Errorf("the account has the members %v", got)
	}
	if got := acct.sub(t, "token"); got.str(t, "purpose") != "invitation" ||
		got.str(t, "expiresAt") != a.clock.now().Add(7*24*time.Hour).Format(time.RFC3339) {
		t.Errorf("the account has the token %v, want an invitation that expires in seven days", got)
	}
	if records := a.records(t, audit.AccountInvite); len(records) != 5 || records[4].Actor != adminID || records[4].Target.ID != id {
		t.Errorf("the records of account.invite are %+v, want the invitation by the admin as the last of 5", records)
	}

	// The token is in the answer to the invitation and nowhere else: the account tells that it has one, and for what.
	for what, res := range map[string]*http.Response{
		"GET the account":   admin.get("/api/accounts/" + id),
		"GET the accounts":  admin.get("/api/accounts"),
		"GET the logs":      admin.get("/api/logs/activity?action=account."),
		"GET the login log": admin.get("/api/logs/login"),
	} {
		text := bodyOf(t, res)
		if strings.Contains(text, token) {
			t.Errorf("%s shows the token", what)
		}
		a.noSecrets(t, what, text, id)
	}

	// The holder of the token sets the password, with no session.
	res := a.postPassword(token, newbiePass)
	set := readObject(t, "POST /api/password", res, http.StatusOK)
	if got := set.names(); !slices.Equal(got, []string{"account"}) {
		t.Errorf("POST /api/password answers the members %v, want account", got)
	}
	account := set.sub(t, "account")
	if got := account.names(); !slices.Equal(got, []string{"email", "id", "login", "name", "role"}) || account.str(t, "id") != id {
		t.Errorf("POST /api/password answers the account %v, want id, login, name, email and role of %s", account, id)
	}
	checkNoCookie(t, "POST /api/password", res)
	checkHeaders(t, "POST /api/password", res, true)
	if got := a.postLogin("newbie", newbiePass); got.StatusCode != http.StatusOK {
		t.Errorf("the login with the new password has the status %d, want 200", got.StatusCode)
	}
	if got := readObject(t, "GET the account", admin.get("/api/accounts/"+id), http.StatusOK); got.str(t, "status") != "active" || got["token"] != nil {
		t.Errorf("the account is %v after the password was set, want an active account without a token", got)
	}
	if got := a.records(t, audit.AccountSetPassword); len(got) != 5 || got[4].Actor != id {
		t.Errorf("the records of account.setPassword are %+v, want the last by the account itself", got)
	}

	// The token cannot be used twice, and no token that is not one is any better; each is recorded in the login history.
	for what, token := range map[string]string{"the used token": token, "a token that nobody has": strings.Repeat("A", 43), "a word": "token"} {
		checkProblem(t, "POST /api/password with "+what, a.postPassword(token, newbiePass+" again"), "invalid-token")
	}
	failures := map[string]int{}
	err := a.db.ReadAudit(t.Context(), audit.Filter{Log: audit.LoginLog, Action: string(audit.TokenFailure)}, func(r audit.Record) bool {
		failures[r.Result]++
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if failures["unknown"] != 2 || failures["malformed"] != 1 || len(failures) != 2 {
		t.Errorf("the login history has the failures of tokens %v, want 2 unknown and 1 malformed", failures)
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestSetPasswordProblems shows what a request with a token asks for: a password that the rules refuse leaves the token as it
// was, and a request that is not one is refused before the token is looked at.
func TestSetPasswordProblems(t *testing.T) {
	a := newAPIServer(t)
	_, token := invitation(t, a.as("admin").post("/api/accounts", newbieBody))

	res := a.postPassword(token, tinyPass)
	p := readProblem(t, res)
	if res.StatusCode != http.StatusUnprocessableEntity || p.Code != "invalid" || len(p.Errors) != 1 || p.Errors[0].Pointer != "/password" {
		t.Errorf("a password that is too short has the status %d and %+v, want the problem invalid at /password", res.StatusCode, p)
	}
	if strings.Contains(p.Errors[0].Detail, tinyPass) {
		t.Errorf("the problem %q shows the password", p.Errors[0].Detail)
	}

	jsonType, fromOrigin := []string{"Content-Type", "application/json"}, []string{"Origin", a.origin}
	good := `{"token":"` + token + `","password":"` + newbiePass + `"}`
	for _, tt := range []struct {
		name, path, body string
		headers          []string
		code             string
	}{
		{"a body without the token", "", `{"password":"` + newbiePass + `"}`, join(fromOrigin, jsonType), "bad-request"},
		{"a body without the password", "", `{"token":"` + token + `"}`, join(fromOrigin, jsonType), "bad-request"},
		{"null for the token", "", `{"token":null,"password":"` + newbiePass + `"}`, join(fromOrigin, jsonType), "bad-request"},
		{"a number for the password", "", `{"token":"` + token + `","password":5}`, join(fromOrigin, jsonType), "bad-request"},
		{"another member", "", `{"token":"` + token + `","password":"` + newbiePass + `","login":"x"}`, join(fromOrigin, jsonType), "bad-request"},
		{"a body that is not an object", "", `"` + token + `"`, join(fromOrigin, jsonType), "bad-request"},
		{"a body that is null", "", `null`, join(fromOrigin, jsonType), "bad-request"},
		{"a body that is not JSON", "", `token`, join(fromOrigin, jsonType), "bad-request"},
		{"a body that is too large", "", `{"token":"` + token + `","password":"` + strings.Repeat("p", smallBodyLimit) + `"}`,
			join(fromOrigin, jsonType), "too-large"},
		{"another media type", "", good, join(fromOrigin, []string{"Content-Type", "text/plain"}), "unsupported-media-type"},
		{"a key", "", good, join(fromOrigin, jsonType, key(1)), "bad-header"},
		{"a query", "?token=" + token, good, join(fromOrigin, jsonType), "bad-query"},
		{"the origin of another site", "", good, join([]string{"Origin", "https://evil.example"}, jsonType), "cross-origin"},
		{"no origin", "", good, jsonType, "cross-origin"},
	} {
		a.seen.password(newbiePass)
		checkProblem(t, "POST /api/password with "+tt.name, a.send(http.MethodPost, "/api/password"+tt.path, tt.body, tt.headers...), tt.code)
	}
	// Nothing of that has used the token: it still sets the password.
	if got := a.postPassword(token, newbiePass); got.StatusCode != http.StatusOK {
		t.Errorf("POST /api/password with the token after those has the status %d, want 200", got.StatusCode)
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestAccountInviteProblems shows the answers to the invitations that cannot be made, and that none makes an account.
func TestAccountInviteProblems(t *testing.T) {
	a := newAPIServer(t)
	admin := a.as("admin")
	const prefix, suffix = `{"login":"newbie","name":"New Person","email":"newbie@example.com","role":"editor"`, `}`
	for _, tt := range []struct {
		name, body, pointer string
	}{
		{"no login", `{"name":"N","email":"n@example.com","role":"editor"}`, "/login"},
		{"an empty login", `{"login":"","name":"N","email":"n@example.com","role":"editor"}`, "/login"},
		{"a login with a space inside", `{"login":"new man","name":"N","email":"n@example.com","role":"editor"}`, "/login"},
		{"a login that an account has", `{"login":"Admin","name":"N","email":"n@example.com","role":"editor"}`, "/login"},
		{"no name", `{"login":"n","email":"n@example.com","role":"editor"}`, "/name"},
		{"no email", `{"login":"n","name":"N","role":"editor"}`, "/email"},
		{"an email address that is none", `{"login":"n","name":"N","email":"nobody","role":"editor"}`, "/email"},
		{"an email address that an account has", `{"login":"n","name":"N","email":"ADMIN@example.com","role":"editor"}`, "/email"},
		{"no role", `{"login":"n","name":"N","email":"n@example.com"}`, "/role"},
		{"a role that is none", `{"login":"n","name":"N","email":"n@example.com","role":"owner"}`, "/role"},
		{"an author profile that is not there", prefix + `,"author":"aut_01JBA4C7N9Q3T6W9Z3C6F9H2ZZ"` + suffix, "/author"},
		{"an author that is not an ID", prefix + `,"author":"yamada"` + suffix, "/author"},
	} {
		res := admin.post("/api/accounts", tt.body)
		p := readProblem(t, res)
		if res.StatusCode != http.StatusUnprocessableEntity || p.Code != "invalid" || len(p.Errors) == 0 || p.Errors[0].Pointer != tt.pointer {
			t.Errorf("an invitation with %s has the status %d and %+v, want the problem invalid at %s", tt.name, res.StatusCode, p, tt.pointer)
		}
	}
	for _, tt := range []struct{ name, body string }{
		{"a login that is a number", `{"login":5,"name":"N","email":"n@example.com","role":"editor"}`},
		{"a login that is null", `{"login":null,"name":"N","email":"n@example.com","role":"editor"}`},
		{"a role that is a list", `{"login":"n","name":"N","email":"n@example.com","role":["editor"]}`},
		{"an author that is null", prefix + `,"author":null` + suffix},
		{"another member", prefix + `,"password":"x"` + suffix},
		{"a member twice", prefix + `,"login":"other"` + suffix},
		{"an array", `[]`},
		{"null", `null`},
		{"an empty body", ``},
	} {
		checkProblem(t, "an invitation with "+tt.name, admin.send(http.MethodPost, "/api/accounts", tt.body, withBody(tt.body)...), "bad-request")
	}
	checkProblem(t, "an invitation with a key", admin.post("/api/accounts", newbieBody, key(1)...), "bad-header")
	checkProblem(t, "an invitation with a query", admin.post("/api/accounts?x=1", newbieBody), "bad-query")
	checkProblem(t, "an invitation of another media type", admin.post("/api/accounts", newbieBody, "Content-Type", "text/plain"), "unsupported-media-type")
	checkProblem(t, "an invitation with a body that is too large", admin.post("/api/accounts", strings.Repeat(" ", smallBodyLimit+1)), "too-large")
	if got, err := a.accounts.List(t.Context()); err != nil || len(got) != 4 {
		t.Errorf("the accounts are %d after those, with the error %v, want the 4 of the test", len(got), err)
	}

	// A person who may not invite is refused, and the refusal is recorded with the permission and no target.
	for _, login := range []string{"editor", "author", "viewer"} {
		a.wantForbidden(t, login, write{"invite", http.MethodPost, "/api/accounts", newbieBody, account.ManageAccounts, audit.Target{}})
	}
	if got, err := a.accounts.List(t.Context()); err != nil || len(got) != 4 {
		t.Errorf("the accounts are %d after the refusals, with the error %v, want 4", len(got), err)
	}

	// The author profile of the sample may be linked, and the account shows it.
	acct, _ := invitation(t, admin.post("/api/accounts", prefix+`,"author":"`+yamada+`"`+suffix))
	if acct.str(t, "author") != yamada {
		t.Errorf("the account has the author profile %q, want %q", acct.str(t, "author"), yamada)
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestAccountRoutesWithoutPublisher shows that the routes of the accounts, the passwords and the logs do not need a
// Publisher, and that those which call the Editor are not there.
func TestAccountRoutesWithoutPublisher(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	admin := ts.addAccount(t, "admin", account.RoleAdmin)
	cookie := ts.cookie(ts.logIn(t, "admin"))
	send := func(method, path, body string) *http.Response {
		headers := append(slices.Clone(cookie), "Origin", ts.origin)
		if body != "" {
			headers = append(headers, "Content-Type", "application/json")
		}
		return ts.send(method, path, body, headers...)
	}
	if page := readAccountPage(t, "GET /api/accounts", send(http.MethodGet, "/api/accounts", "")); page.Total != 1 || page.Items[0].str(t, "id") != admin.ID {
		t.Errorf("GET /api/accounts lists %v, want the one account", page)
	}
	readObject(t, "GET /api/logs/login", send(http.MethodGet, "/api/logs/login", ""), http.StatusOK)
	readObject(t, "GET /api/logs/activity", send(http.MethodGet, "/api/logs/activity", ""), http.StatusOK)
	checkProblem(t, "POST /api/password", ts.send(http.MethodPost, "/api/password", `{"token":"x","password":"y"}`,
		"Origin", ts.origin, "Content-Type", "application/json"), "invalid-token")
	ts.seen.password("not the password")
	res := send(http.MethodPut, "/api/session/password", `{"current":"not the password","password":"another long password"}`)
	if p := readProblem(t, res); res.StatusCode != http.StatusUnprocessableEntity || p.Code != "invalid" || len(p.Errors) != 1 || p.Errors[0].Pointer != "/current" {
		t.Errorf("PUT /api/session/password has the status %d and %+v, want the problem invalid at /current", res.StatusCode, p)
	}
	for _, path := range []string{"/api/entries", "/api/schema", "/api/builds", "/api/media"} {
		checkProblem(t, "GET "+path, send(http.MethodGet, path, ""), "not-found")
	}
}
