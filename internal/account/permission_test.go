package account

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const permOtherAuthorID = "aut_01JB8Y6T2RG7M4XK9P2D5W8NQ4"

// permissionRow is a row of the table that 7.3 prints, written out again here so that the tests do not read the table of
// the code. Each cell says what a role may do: "all" is anything, "-" nothing, and the others only what the account
// owns: "owner" by Target.Owner, "author" by Target.Author (the profile that the account is linked to) and "self" by
// Target.Account.
type permissionRow struct {
	action Action
	value  string // the value of the constant
	phrase string // the end of "not allowed to ..." for a role that may not do it at all
	others string // the end for a role that may do it only on its own, on what is not; empty if there is no such role
	admin  string
	editor string
	author string
	viewer string
}

var permissionTable = []permissionRow{
	{ViewAdmin, "view", "view the admin screen", "", "all", "all", "all", "all"},
	{CreateEntry, "entry.create", "create entries", "", "all", "all", "all", "-"},
	{EditEntry, "entry.edit", "edit entries", "edit entries that other accounts made", "all", "all", "owner", "-"},
	{PublishEntry, "entry.publish", "publish entries", "publish entries that other accounts made", "all", "all", "owner", "-"},
	{DeleteEntry, "entry.delete", "delete entries", "delete entries that other accounts made", "all", "all", "owner", "-"},
	{AddMedia, "media.add", "add media", "", "all", "all", "all", "-"},
	{EditMedia, "media.edit", "edit media", "edit media that other accounts uploaded", "all", "all", "owner", "-"},
	{DeleteMedia, "media.delete", "delete media", "delete media that other accounts uploaded", "all", "all", "owner", "-"},
	{CreateAuthor, "author.create", "create author profiles", "", "all", "all", "-", "-"},
	{EditAuthor, "author.edit", "edit author profiles", "edit author profiles other than its own", "all", "all", "author", "-"},
	{DeleteAuthor, "author.delete", "delete author profiles", "", "all", "all", "-", "-"},
	{EditTerms, "term.edit", "edit terms", "", "all", "all", "-", "-"},
	{EditMenus, "menu.edit", "edit menus", "", "all", "all", "-", "-"},
	{EditForms, "form.edit", "edit forms", "", "all", "all", "-", "-"},
	{EditRedirects, "redirect.edit", "edit redirects", "", "all", "all", "-", "-"},
	{EditSettings, "settings.edit", "edit the site settings", "", "all", "-", "-", "-"},
	{ManageBuilds, "builds.manage", "manage builds and releases", "", "all", "-", "-", "-"},
	{ManageAccounts, "accounts.manage", "manage accounts", "", "all", "-", "-", "-"},
	{EditSelf, "account.self", "edit its own account", "edit another account", "self", "self", "self", "self"},
	{ViewLogs, "logs.view", "view the logs", "", "all", "-", "-", "-"},
}

// cell returns the cell of the role; a role that the table does not have may do nothing.
func (r permissionRow) cell(role Role) string {
	switch role {
	case RoleAdmin:
		return r.admin
	case RoleEditor:
		return r.editor
	case RoleAuthor:
		return r.author
	case RoleViewer:
		return r.viewer
	}
	return "-"
}

// permTableOwns says what a cell that is not "all" or "-" means for the account and the target.
func permTableOwns(cell string, a Account, t Target) bool {
	switch cell {
	case "owner":
		return t.Owner != "" && t.Owner == a.ID
	case "author":
		return a.Author != "" && t.Author == a.Author
	case "self":
		return t.Account != "" && t.Account == a.ID
	}
	return false
}

// permRefusalText is the text of the refusal of an account: its ID, then its role or, if it is not active, its status.
func permRefusalText(a Account, phrase string) string {
	who := string(a.Role)
	if a.Status != StatusActive {
		who = string(a.Status)
	}
	return a.ID + " (" + who + "): not allowed to " + phrase
}

// want returns whether the table allows the action to the active account, and if not the text of the refusal.
func (r permissionRow) want(a Account, t Target) (allowed bool, text string) {
	cell := r.cell(a.Role)
	if cell == "all" || cell != "-" && permTableOwns(cell, a, t) {
		return true, ""
	}
	if cell != "-" {
		return false, permRefusalText(a, r.others)
	}
	return false, permRefusalText(a, r.phrase)
}

// permissionAccount returns an active account that has something in every field, so that a check that reads a field it
// should not shows up. linked is the author profile that it is linked to, or "".
func permissionAccount(role Role, linked string) Account {
	return Account{
		ID: testUserID, Login: "login-marker", Name: "name-marker", Email: "email-marker@example.com", Role: role,
		Author: linked, Status: StatusActive, CreatedAt: testTime, CreatedBy: testCreatorID,
		CredentialsChangedAt: testTime.Add(time.Hour), Token: TokenInfo{Purpose: PurposeReset, ExpiresAt: testTime.Add(2 * time.Hour)},
	}
}

// permissionTargets returns every target made of an Owner, an Author and an Account that are the account's own, another
// one's or empty, so that the reading of a wrong field changes the answer.
func permissionTargets() []Target {
	owners := []string{testUserID, testCreatorID, ""}
	authors := []string{testAuthorID, permOtherAuthorID, ""}
	accounts := []string{testUserID, testCreatorID, ""}
	var targets []Target
	for _, o := range owners {
		for _, au := range authors {
			for _, ac := range accounts {
				targets = append(targets, Target{Owner: o, Author: au, Account: ac})
			}
		}
	}
	return targets
}

func permAllRoles() []Role {
	return append(Roles(), Role("root"), Role(""))
}

func TestCheckMatrix(t *testing.T) {
	targets := permissionTargets()
	if len(targets) != 27 {
		t.Fatalf("permissionTargets() has %d targets, want 27", len(targets))
	}
	for _, role := range Roles() {
		for _, linked := range []string{testAuthorID, ""} {
			a := permissionAccount(role, linked)
			p := Principal{Account: a}
			for _, row := range permissionTable {
				for _, tg := range targets {
					allowed, text := row.want(a, tg)
					name := string(role) + " linked to " + strconv.Quote(linked) + " " + string(row.action) + " " + strconv.Quote(tg.Owner+" "+tg.Author+" "+tg.Account)
					err := Check(p, row.action, tg)
					switch {
					case allowed && err != nil:
						t.Errorf("%s: Check() = %v, want nil", name, err)
					case !allowed && err == nil:
						t.Errorf("%s: Check() = nil, want %q", name, text)
					case !allowed && err.Error() != text:
						t.Errorf("%s: Check() = %q, want %q", name, err, text)
					case !allowed && !errors.Is(err, ErrForbidden):
						t.Errorf("%s: Check() = %v, which does not wrap ErrForbidden", name, err)
					}
					if got := Allowed(p, row.action, tg); got != allowed {
						t.Errorf("%s: Allowed() = %v, want %v", name, got, allowed)
					}
				}
			}
		}
	}
}

// TestCheckMatrixOwn spells out, case by case and apart from permissionTable, the answers that 7.3 gives.
func TestCheckMatrixOwn(t *testing.T) {
	own := Target{Owner: testUserID, Author: testAuthorID, Account: testUserID}
	other := Target{Owner: testCreatorID, Author: permOtherAuthorID, Account: testCreatorID}
	tests := []struct {
		name   string
		role   Role
		linked string
		action Action
		target Target
		want   bool
	}{
		{"author edits its own entry", RoleAuthor, testAuthorID, EditEntry, own, true},
		{"author publishes its own entry", RoleAuthor, testAuthorID, PublishEntry, own, true},
		{"author deletes its own entry", RoleAuthor, testAuthorID, DeleteEntry, own, true},
		{"author edits the entry of another account", RoleAuthor, testAuthorID, EditEntry, other, false},
		{"author publishes the entry of another account", RoleAuthor, testAuthorID, PublishEntry, other, false},
		{"author deletes the entry of another account", RoleAuthor, testAuthorID, DeleteEntry, other, false},
		{"author edits an entry without a creator", RoleAuthor, testAuthorID, EditEntry, Target{}, false},
		{"author edits its own medium", RoleAuthor, testAuthorID, EditMedia, own, true},
		{"author deletes its own medium", RoleAuthor, testAuthorID, DeleteMedia, own, true},
		{"author edits the medium of another account", RoleAuthor, testAuthorID, EditMedia, other, false},
		{"author deletes the medium of another account", RoleAuthor, testAuthorID, DeleteMedia, other, false},
		{"author adds a medium", RoleAuthor, testAuthorID, AddMedia, Target{}, true},
		{"author creates an entry", RoleAuthor, testAuthorID, CreateEntry, Target{}, true},
		{"author edits its linked profile", RoleAuthor, testAuthorID, EditAuthor, own, true},
		{"author edits another profile", RoleAuthor, testAuthorID, EditAuthor, other, false},
		{"author without a profile edits a profile", RoleAuthor, "", EditAuthor, own, false},
		{"author without a profile edits no profile", RoleAuthor, "", EditAuthor, Target{}, false},
		{"author creates a profile", RoleAuthor, testAuthorID, CreateAuthor, own, false},
		{"author deletes its linked profile", RoleAuthor, testAuthorID, DeleteAuthor, own, false},
		{"author edits a term", RoleAuthor, testAuthorID, EditTerms, Target{}, false},
		{"author edits a menu", RoleAuthor, testAuthorID, EditMenus, Target{}, false},
		{"author edits a form", RoleAuthor, testAuthorID, EditForms, Target{}, false},
		{"author edits a redirect", RoleAuthor, testAuthorID, EditRedirects, Target{}, false},
		{"author edits the settings", RoleAuthor, testAuthorID, EditSettings, Target{}, false},
		{"author rebuilds", RoleAuthor, testAuthorID, ManageBuilds, Target{}, false},
		{"author manages accounts", RoleAuthor, testAuthorID, ManageAccounts, own, false},
		{"author reads the logs", RoleAuthor, testAuthorID, ViewLogs, Target{}, false},
		{"editor edits the entry of another account", RoleEditor, "", EditEntry, other, true},
		{"editor publishes an entry without a creator", RoleEditor, "", PublishEntry, Target{}, true},
		{"editor deletes the medium of another account", RoleEditor, "", DeleteMedia, other, true},
		{"editor edits another profile", RoleEditor, "", EditAuthor, other, true},
		{"editor creates a profile", RoleEditor, "", CreateAuthor, Target{}, true},
		{"editor deletes a profile", RoleEditor, "", DeleteAuthor, Target{}, true},
		{"editor edits terms", RoleEditor, "", EditTerms, Target{}, true},
		{"editor edits menus", RoleEditor, "", EditMenus, Target{}, true},
		{"editor edits forms", RoleEditor, "", EditForms, Target{}, true},
		{"editor edits redirects", RoleEditor, "", EditRedirects, Target{}, true},
		{"editor edits the settings", RoleEditor, "", EditSettings, Target{}, false},
		{"editor rebuilds", RoleEditor, "", ManageBuilds, Target{}, false},
		{"editor manages accounts", RoleEditor, "", ManageAccounts, other, false},
		{"editor reads the logs", RoleEditor, "", ViewLogs, Target{}, false},
		{"admin edits the settings", RoleAdmin, "", EditSettings, Target{}, true},
		{"admin rebuilds", RoleAdmin, "", ManageBuilds, Target{}, true},
		{"admin manages accounts", RoleAdmin, "", ManageAccounts, other, true},
		{"admin reads the logs", RoleAdmin, "", ViewLogs, Target{}, true},
		{"admin edits any entry", RoleAdmin, "", EditEntry, other, true},
		{"viewer views", RoleViewer, "", ViewAdmin, Target{}, true},
		{"viewer creates an entry", RoleViewer, "", CreateEntry, Target{}, false},
		{"viewer edits its own entry", RoleViewer, "", EditEntry, own, false},
		{"viewer adds a medium", RoleViewer, "", AddMedia, Target{}, false},
		{"viewer edits its linked profile", RoleViewer, testAuthorID, EditAuthor, own, false},
		{"viewer edits its own account", RoleViewer, "", EditSelf, own, true},
		{"author edits its own account", RoleAuthor, "", EditSelf, own, true},
		{"editor edits its own account", RoleEditor, "", EditSelf, own, true},
		{"admin edits its own account", RoleAdmin, "", EditSelf, own, true},
		{"admin edits another account as itself", RoleAdmin, "", EditSelf, other, false},
		{"editor edits another account as itself", RoleEditor, "", EditSelf, other, false},
		{"author edits another account as itself", RoleAuthor, "", EditSelf, other, false},
		{"viewer edits another account as itself", RoleViewer, "", EditSelf, other, false},
		{"admin edits no account as itself", RoleAdmin, "", EditSelf, Target{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Principal{Account: permissionAccount(tt.role, tt.linked)}
			if got := Allowed(p, tt.action, tt.target); got != tt.want {
				t.Errorf("Allowed(%s, %s, %+v) = %v, want %v", tt.role, tt.action, tt.target, got, tt.want)
			}
			err := Check(p, tt.action, tt.target)
			if (err == nil) != tt.want {
				t.Errorf("Check(%s, %s, %+v) = %v, want allowed %v", tt.role, tt.action, tt.target, err, tt.want)
			}
		})
	}
}

func TestViaOwns(t *testing.T) {
	a := Account{ID: testUserID, Author: testAuthorID}
	unlinked := Account{ID: testUserID}
	nobody := Account{}
	mine := Target{Owner: testUserID, Author: testAuthorID, Account: testUserID}
	others := Target{Owner: testCreatorID, Author: permOtherAuthorID, Account: testCreatorID}
	tests := []struct {
		name string
		v    via
		a    Account
		t    Target
		want bool
	}{
		{"nothing is owned", viaNothing, a, mine, false},
		{"a via that is not defined owns nothing", via(200), a, mine, false},
		{"owner", viaOwner, a, Target{Owner: testUserID}, true},
		{"owner of another", viaOwner, a, others, false},
		{"owner is not read from the other fields", viaOwner, a, Target{Author: testAuthorID, Account: testUserID}, false},
		{"empty owner", viaOwner, a, Target{}, false},
		{"empty owner of an account without an ID", viaOwner, nobody, Target{}, false},
		{"author", viaAuthor, a, Target{Author: testAuthorID}, true},
		{"author of another", viaAuthor, a, others, false},
		{"author is not read from the other fields", viaAuthor, a, Target{Owner: testUserID, Account: testUserID}, false},
		{"empty author of an unlinked account", viaAuthor, unlinked, Target{}, false},
		{"a profile for an unlinked account", viaAuthor, unlinked, Target{Author: testAuthorID}, false},
		{"account", viaAccount, a, Target{Account: testUserID}, true},
		{"account of another", viaAccount, a, others, false},
		{"account is not read from the other fields", viaAccount, a, Target{Owner: testUserID, Author: testAuthorID}, false},
		{"empty account", viaAccount, a, Target{}, false},
		{"empty account of an account without an ID", viaAccount, nobody, Target{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.v.owns(tt.a, tt.t); got != tt.want {
				t.Errorf("owns() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCheckInactive(t *testing.T) {
	statuses := []Status{StatusPending, StatusDisabled, "", "locked", "Active", "active "}
	targets := []Target{
		{Owner: testUserID, Author: testAuthorID, Account: testUserID},
		{Owner: testCreatorID, Author: permOtherAuthorID, Account: testCreatorID},
		{},
	}
	for _, status := range statuses {
		for _, role := range permAllRoles() {
			a := permissionAccount(role, testAuthorID)
			a.Status = status
			for _, row := range permissionTable {
				for _, tg := range targets {
					err := Check(Principal{Account: a}, row.action, tg)
					want := a.ID + " (" + string(status) + "): not allowed to " + row.phrase
					if err == nil || err.Error() != want || !errors.Is(err, ErrForbidden) {
						t.Errorf("Check(%q account of the role %q, %s, %+v) = %v, want %q", status, role, row.action, tg, err, want)
					}
					if Allowed(Principal{Account: a}, row.action, tg) {
						t.Errorf("Allowed(%q account of the role %q, %s, %+v) = true", status, role, row.action, tg)
					}
				}
			}
		}
	}
}

func TestCheckUnknownRole(t *testing.T) {
	targets := permissionTargets()
	for _, role := range []Role{"", "root", "Admin", "ADMIN", "admin ", " admin", "viewer2", "admin,editor", "*"} {
		a := permissionAccount(role, testAuthorID)
		for _, row := range permissionTable {
			for _, tg := range targets {
				err := Check(Principal{Account: a}, row.action, tg)
				want := a.ID + " (" + string(role) + "): not allowed to " + row.phrase
				if err == nil || err.Error() != want || !errors.Is(err, ErrForbidden) {
					t.Fatalf("Check(role %q, %s, %+v) = %v, want %q", role, row.action, tg, err, want)
				}
			}
		}
	}
}

func TestCheckUnknownAction(t *testing.T) {
	actions := []Action{
		"", "entry", "entry.fly", "VIEW", "view ", " view", "entry.edit ", "Entry.Edit", "entries.edit", "accounts", "a\"b", "a\nb",
		"\x00", "view\x00", "logs.view,view", "*", "all",
	}
	own := Target{Owner: testUserID, Author: testAuthorID, Account: testUserID}
	for _, role := range Roles() {
		a := permissionAccount(role, testAuthorID)
		for _, act := range actions {
			err := Check(Principal{Account: a}, act, own)
			want := a.ID + " (" + string(role) + "): not allowed to do " + strconv.Quote(string(act))
			if err == nil || err.Error() != want || !errors.Is(err, ErrForbidden) {
				t.Errorf("Check(%s, %q) = %v, want %q", role, act, err, want)
			} else if strings.ContainsAny(err.Error(), "\n\x00") {
				t.Errorf("Check(%s, %q) = %q, which has a line break or a NUL", role, act, err)
			}
			if Allowed(Principal{Account: a}, act, own) {
				t.Errorf("Allowed(%s, %q) = true", role, act)
			}
		}
	}
	// An account that is not active gets its status in the text, for an unknown action too.
	for _, status := range []Status{StatusPending, StatusDisabled} {
		a := permissionAccount(RoleAdmin, "")
		a.Status = status
		err := Check(Principal{Account: a}, "entry.fly", Target{})
		if want := a.ID + " (" + string(status) + `): not allowed to do "entry.fly"`; err == nil || err.Error() != want {
			t.Errorf("Check(%s admin, an unknown action) = %v, want %q", status, err, want)
		}
	}
}

func TestCheckZeroPrincipal(t *testing.T) {
	for _, row := range permissionTable {
		err := Check(Principal{}, row.action, Target{})
		want := " (): not allowed to " + row.phrase
		if err == nil || err.Error() != want || !errors.Is(err, ErrForbidden) {
			t.Errorf("Check(Principal{}, %s) = %v, want %q", row.action, err, want)
		}
	}
	// An active account without an ID or a link owns nothing, not even what has an empty owner, profile or account.
	for _, role := range Roles() {
		a := Account{Role: role, Status: StatusActive}
		for _, act := range []Action{EditEntry, PublishEntry, DeleteEntry, EditMedia, DeleteMedia, EditAuthor, EditSelf} {
			if row := findRule(act); row.grants.of(role) == reachOwn && Allowed(Principal{Account: a}, act, Target{}) {
				t.Errorf("Allowed(a %s without an ID or a link, %s, an empty target) = true", role, act)
			}
		}
	}
}

func TestCheckTexts(t *testing.T) {
	account := func(role Role, status Status) Account {
		a := permissionAccount(role, testAuthorID)
		a.Status = status
		return a
	}
	own := Target{Owner: testUserID, Author: testAuthorID, Account: testUserID}
	other := Target{Owner: testCreatorID, Author: permOtherAuthorID, Account: testCreatorID}
	const id = testUserID
	tests := []struct {
		a      Account
		action Action
		target Target
		want   string
	}{
		{account(RoleViewer, StatusActive), EditEntry, own, id + " (viewer): not allowed to edit entries"},
		{account(RoleViewer, StatusActive), CreateEntry, own, id + " (viewer): not allowed to create entries"},
		{account(RoleAuthor, StatusActive), EditEntry, other, id + " (author): not allowed to edit entries that other accounts made"},
		{account(RoleAuthor, StatusActive), PublishEntry, other, id + " (author): not allowed to publish entries that other accounts made"},
		{account(RoleAuthor, StatusActive), DeleteEntry, other, id + " (author): not allowed to delete entries that other accounts made"},
		{account(RoleAuthor, StatusActive), EditMedia, other, id + " (author): not allowed to edit media that other accounts uploaded"},
		{account(RoleAuthor, StatusActive), DeleteMedia, other, id + " (author): not allowed to delete media that other accounts uploaded"},
		{account(RoleAuthor, StatusActive), EditAuthor, other, id + " (author): not allowed to edit author profiles other than its own"},
		{account(RoleAuthor, StatusActive), EditAuthor, Target{}, id + " (author): not allowed to edit author profiles other than its own"},
		{account(RoleAuthor, StatusActive), CreateAuthor, own, id + " (author): not allowed to create author profiles"},
		{account(RoleAuthor, StatusActive), EditTerms, own, id + " (author): not allowed to edit terms"},
		{account(RoleViewer, StatusActive), EditAuthor, own, id + " (viewer): not allowed to edit author profiles"},
		{account(RoleEditor, StatusActive), EditSettings, own, id + " (editor): not allowed to edit the site settings"},
		{account(RoleEditor, StatusActive), ManageBuilds, own, id + " (editor): not allowed to manage builds and releases"},
		{account(RoleEditor, StatusActive), ManageAccounts, other, id + " (editor): not allowed to manage accounts"},
		{account(RoleEditor, StatusActive), ViewLogs, own, id + " (editor): not allowed to view the logs"},
		{account(RoleAdmin, StatusActive), EditSelf, other, id + " (admin): not allowed to edit another account"},
		{account(RoleViewer, StatusActive), EditSelf, other, id + " (viewer): not allowed to edit another account"},
		{account(RoleAdmin, StatusActive), "entry.fly", own, id + ` (admin): not allowed to do "entry.fly"`},
		{account(RoleEditor, StatusDisabled), PublishEntry, own, id + " (disabled): not allowed to publish entries"},
		{account(RoleAdmin, StatusDisabled), ManageAccounts, own, id + " (disabled): not allowed to manage accounts"},
		{account(RoleAdmin, StatusPending), ViewAdmin, own, id + " (pending): not allowed to view the admin screen"},
		{account(RoleAuthor, StatusPending), EditSelf, own, id + " (pending): not allowed to edit its own account"},
		{account(RoleAuthor, StatusDisabled), EditEntry, other, id + " (disabled): not allowed to edit entries"},
		{account("root", StatusActive), ViewAdmin, own, id + " (root): not allowed to view the admin screen"},
	}
	for _, tt := range tests {
		err := Check(Principal{Account: tt.a}, tt.action, tt.target)
		if err == nil || err.Error() != tt.want {
			t.Errorf("Check(%s %s, %s) = %v, want %q", tt.a.Status, tt.a.Role, tt.action, err, tt.want)
			continue
		}
		if !errors.Is(err, ErrForbidden) {
			t.Errorf("Check(%s %s, %s) = %v, which does not wrap ErrForbidden", tt.a.Status, tt.a.Role, tt.action, err)
		}
	}
	if ErrForbidden.Error() != "not allowed" {
		t.Errorf("ErrForbidden = %q, want %q", ErrForbidden, "not allowed")
	}
}

// TestCheckTextsHideThePerson checks that a refusal says nothing of the account but its ID, its role or status, and
// nothing of the target.
func TestCheckTextsHideThePerson(t *testing.T) {
	secrets := []string{"login-marker", "name-marker", "email-marker", "example.com", testCreatorID, testAuthorID, permOtherAuthorID, "reset", "2026"}
	tg := Target{Owner: testCreatorID, Author: permOtherAuthorID, Account: testCreatorID}
	for _, status := range []Status{StatusActive, StatusPending, StatusDisabled} {
		for _, role := range Roles() {
			a := permissionAccount(role, testAuthorID)
			a.Status = status
			for _, row := range permissionTable {
				err := Check(Principal{Account: a}, row.action, tg)
				if err == nil {
					continue
				}
				for _, s := range secrets {
					if strings.Contains(err.Error(), s) {
						t.Errorf("Check(%s %s, %s) = %q, which contains %q", status, role, row.action, err, s)
					}
				}
			}
		}
	}
}

func TestActions(t *testing.T) {
	var want []Action
	for _, row := range permissionTable {
		want = append(want, row.action)
		if string(row.action) != row.value {
			t.Errorf("the action in the table is %q, want %q", row.action, row.value)
		}
	}
	got := Actions()
	if !slices.Equal(got, want) {
		t.Errorf("Actions() = %v, want %v", got, want)
	}
	if len(got) != 20 {
		t.Errorf("Actions() has %d actions, want 20", len(got))
	}
	seen := map[Action]bool{}
	for _, act := range got {
		if seen[act] {
			t.Errorf("%q is in Actions() twice", act)
		}
		seen[act] = true
	}
	// Each call returns a slice of its own.
	got[0] = "changed"
	if again := Actions(); !slices.Equal(again, want) {
		t.Errorf("Actions() after a change of an earlier result = %v, want %v", again, want)
	}
	if got[0] != "changed" {
		t.Errorf("a later call of Actions() changed the slice of an earlier call to %q", got[0])
	}
}

// TestActionConstants reads permission.go for its constants of type Action: each is in Actions() once and in the order
// of the file, so that a constant added without a row in the table of the code is found.
func TestActionConstants(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "permission.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing permission.go: %v", err)
	}
	var names, values []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			if typ, ok := vs.Type.(*ast.Ident); !ok || typ.Name != "Action" {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					t.Fatalf("%s has no value", name.Name)
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("the value of %s is not a string literal", name.Name)
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("the value of %s: %v", name.Name, err)
				}
				names = append(names, name.Name)
				values = append(values, value)
			}
		}
	}
	var wantValues []string
	for _, act := range Actions() {
		wantValues = append(wantValues, string(act))
	}
	if !slices.Equal(values, wantValues) {
		t.Errorf("the constants of type Action in permission.go (%v) have the values %q, but Actions() has %q", names, values, wantValues)
	}
	if len(names) != len(permissionTable) {
		t.Errorf("permission.go has %d constants of type Action, want %d", len(names), len(permissionTable))
	}
}

// TestRulesWellFormed checks the table of the code against itself: a rule that reaches only what is the role's own says
// how to tell it and what to say if it is not, and a rule that does not has neither.
func TestRulesWellFormed(t *testing.T) {
	if len(rules) != len(permissionTable) {
		t.Fatalf("the code has %d rules, the test table %d", len(rules), len(permissionTable))
	}
	phrases := map[string]Action{}
	for i, r := range rules {
		row := permissionTable[i]
		if r.action != row.action {
			t.Errorf("rule %d is %q, want %q", i, r.action, row.action)
		}
		if r.phrase == "" {
			t.Errorf("%s has no phrase", r.action)
		}
		if prev, ok := phrases[r.phrase]; ok {
			t.Errorf("%s and %s have the same phrase %q", prev, r.action, r.phrase)
		}
		phrases[r.phrase] = r.action
		cells := []reach{r.grants.admin, r.grants.editor, r.grants.author, r.grants.viewer}
		hasOwn := false
		for _, c := range cells {
			switch c {
			case reachNo, reachAll:
			case reachOwn:
				hasOwn = true
			default:
				t.Errorf("%s has the reach %d, which is not defined", r.action, c)
			}
		}
		if hasOwn && (r.own == viaNothing || r.others == "") {
			t.Errorf("%s reaches only what is the role's own, but it has own %d and others %q", r.action, r.own, r.others)
		}
		if !hasOwn && (r.own != viaNothing || r.others != "") {
			t.Errorf("%s reaches nothing only on what is the role's own, but it has own %d and others %q", r.action, r.own, r.others)
		}
		if r.others == r.phrase {
			t.Errorf("%s has the same phrase %q for a refusal of a role and for a refusal on a target", r.action, r.phrase)
		}
	}
}

// TestCheckReadsOnlyThePrincipalsRoleStatusAndLinks checks that the other fields of the account change no answer.
func TestCheckReadsOnlyThePrincipalsRoleStatusAndLinks(t *testing.T) {
	targets := permissionTargets()
	for _, status := range []Status{StatusActive, StatusPending, StatusDisabled} {
		for _, role := range permAllRoles() {
			for _, linked := range []string{testAuthorID, ""} {
				full := permissionAccount(role, linked)
				full.Status = status
				bare := Account{ID: full.ID, Role: full.Role, Status: full.Status, Author: full.Author}
				for _, row := range permissionTable {
					for _, tg := range targets {
						a, b := Check(Principal{Account: full}, row.action, tg), Check(Principal{Account: bare}, row.action, tg)
						if (a == nil) != (b == nil) || a != nil && a.Error() != b.Error() {
							t.Fatalf("%s %s linked to %q, %s, %+v: %v with every field, %v with only the ID, role, status and link",
								status, role, linked, row.action, tg, a, b)
						}
					}
				}
			}
		}
	}
}

func FuzzPermissionCheck(f *testing.F) {
	f.Add(testUserID, "editor", "active", testAuthorID, "entry.edit", testUserID, testAuthorID, testUserID)
	f.Add(testUserID, "author", "active", testAuthorID, "entry.edit", testCreatorID, "", "")
	f.Add(testUserID, "author", "active", "", "author.edit", "", "", "")
	f.Add(testUserID, "author", "active", testAuthorID, "author.edit", "", testAuthorID, "")
	f.Add(testUserID, "viewer", "active", "", "account.self", "", "", testUserID)
	f.Add(testUserID, "admin", "active", "", "accounts.manage", "", "", "")
	f.Add(testUserID, "admin", "disabled", "", "view", "", "", "")
	f.Add("", "admin", "active", "", "account.self", "", "", "")
	f.Add("", "", "", "", "", "", "", "")
	f.Add("a (b): not allowed to c", "Admin", "pending", "x", "view\n", "x", "x", "x")
	f.Fuzz(func(t *testing.T, id, role, status, linked, action, owner, author, account string) {
		a := Account{ID: id, Role: Role(role), Status: Status(status), Author: linked}
		act := Action(action)
		tg := Target{Owner: owner, Author: author, Account: account}
		err := Check(Principal{Account: a}, act, tg)
		if allowed := Allowed(Principal{Account: a}, act, tg); allowed != (err == nil) {
			t.Fatalf("Allowed() = %v, but Check() = %v", allowed, err)
		}
		if err != nil {
			who := role
			if status != string(StatusActive) {
				who = status
			}
			if !errors.Is(err, ErrForbidden) || !strings.HasPrefix(err.Error(), id+" ("+who+"): not allowed to ") {
				t.Fatalf("Check() = %q, want an error that wraps ErrForbidden and starts with the ID and %q", err, who)
			}
			return
		}
		// Whatever was asked, an allowed check means an active account, a known role and a known action, and the
		// viewer has only two actions.
		if a.Status != StatusActive || !slices.Contains(Roles(), a.Role) || !slices.Contains(Actions(), act) {
			t.Fatalf("Check() allowed the action %q to the %q account of the role %q", act, a.Status, a.Role)
		}
		if a.Role == RoleViewer && act != ViewAdmin && act != EditSelf {
			t.Fatalf("Check() allowed the viewer %q", act)
		}
		if act == EditSelf && (a.ID == "" || account != a.ID) {
			t.Fatalf("Check() allowed EditSelf on the account %q to %q", account, a.ID)
		}
		if slices.Contains([]Action{EditSettings, ManageBuilds, ManageAccounts, ViewLogs}, act) && a.Role != RoleAdmin {
			t.Fatalf("Check() allowed %q to the role %q", act, a.Role)
		}
	})
}
