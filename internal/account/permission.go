package account

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
)

// Action is what a role may or may not do (7.3). Its value is its name in the table of 7.3.
type Action string

const (
	// ViewAdmin covers the admin screen and reading the documents on it.
	ViewAdmin Action = "view"
	// CreateEntry covers CreateEntry and CreateTranslation of internal/edit.
	CreateEntry Action = "entry.create"
	// EditEntry covers SaveRevision.
	EditEntry Action = "entry.edit"
	// PublishEntry covers Publish, Unpublish, Archive and Schedule; PublishScheduled has none, as Schedule is checked.
	PublishEntry Action = "entry.publish"
	// DeleteEntry covers Delete of an entry.
	DeleteEntry Action = "entry.delete"
	// AddMedia covers AddMedia.
	AddMedia Action = "media.add"
	// EditMedia covers Save of a medium and ReplaceMedia.
	EditMedia Action = "media.edit"
	// DeleteMedia covers Delete of a medium.
	DeleteMedia Action = "media.delete"
	// CreateAuthor covers Create of an author profile.
	CreateAuthor Action = "author.create"
	// EditAuthor covers Save of an author profile.
	EditAuthor Action = "author.edit"
	// DeleteAuthor covers Delete of an author profile.
	DeleteAuthor Action = "author.delete"
	// EditTerms covers Create, Save and Delete of a term.
	EditTerms Action = "term.edit"
	// EditMenus covers Create, Save and Delete of a menu.
	EditMenus Action = "menu.edit"
	// EditForms covers Create, Save and Delete of a form.
	EditForms Action = "form.edit"
	// EditRedirects covers Create, Save and Delete of a redirect.
	EditRedirects Action = "redirect.edit"
	// EditSettings covers SaveSettings.
	EditSettings Action = "settings.edit"
	// ManageBuilds covers Rebuild, Rollback and Resume of internal/publish.
	ManageBuilds Action = "builds.manage"
	// ManageAccounts covers the account operations on other accounts.
	ManageAccounts Action = "accounts.manage"
	// EditSelf covers the name, the email address and the credentials of one's own account.
	EditSelf Action = "account.self"
	// ViewLogs covers the login history and the activity log (7.4).
	ViewLogs Action = "logs.view"
)

// Target is what an action is done to; Check reads the field that the action is about.
type Target struct {
	// Owner is the createdBy of an entry or the uploadedBy of a medium. An empty Owner is nobody's.
	Owner string
	// Author is the ID of the author profile that the action changes.
	Author string
	// Account is the ID of the account that the action changes.
	Account string
}

// Principal is who asks to do an action: the account, as it is, and the session that it asks with. Check reads the
// account's ID, role, status and link to an author profile, and nothing of the session yet. The Session tells how the
// account's session was authenticated, which later rules may require for some actions (7.3 on stronger authentication);
// callers pass the principal on as it is. A Principal that no session made, as in a test or a command, has the zero
// Session.
type Principal struct {
	Account Account
	Session Session
}

var ErrForbidden = errors.New("not allowed")

type reach uint8

const (
	reachNo  reach = iota // not at all
	reachOwn              // only on what is its own, as the rule says
	reachAll              // on anything
)

type grants struct{ admin, editor, author, viewer reach }

// of is reachNo for a role that is not one of Roles.
func (g grants) of(role Role) reach {
	switch role {
	case RoleAdmin:
		return g.admin
	case RoleEditor:
		return g.editor
	case RoleAuthor:
		return g.author
	case RoleViewer:
		return g.viewer
	}
	return reachNo
}

// via says which field of a Target makes it the account's own.
type via uint8

const (
	viaNothing via = iota
	viaOwner       // Target.Owner is the ID of the account
	viaAuthor      // Target.Author is the author profile that the account is linked to
	viaAccount     // Target.Account is the ID of the account
)

// owns reports whether t is the account's own. An empty ID or link is nobody's.
func (v via) owns(a Account, t Target) bool {
	switch v {
	case viaOwner:
		return a.ID != "" && t.Owner == a.ID
	case viaAuthor:
		return a.Author != "" && t.Author == a.Author
	case viaAccount:
		return a.ID != "" && t.Account == a.ID
	case viaNothing:
	}
	return false
}

// rule is the row of an action in the table of 7.3. A stronger authentication that an action needs (7.3) is one more
// field here, which Check reads next to the grants.
type rule struct {
	action Action
	// phrase ends the text "not allowed to ..." of a refusal.
	phrase string
	grants grants
	// own tells what is the account's own, for a role that reaches only that; others is the phrase for what is not.
	own    via
	others string
}

// rules lists the actions in the order of the table of 7.3 and of Actions.
var rules = []rule{
	{action: ViewAdmin, phrase: "view the admin screen", grants: grants{reachAll, reachAll, reachAll, reachAll}},
	{action: CreateEntry, phrase: "create entries", grants: grants{reachAll, reachAll, reachAll, reachNo}},
	{
		action: EditEntry, phrase: "edit entries", grants: grants{reachAll, reachAll, reachOwn, reachNo},
		own: viaOwner, others: "edit entries that other accounts made",
	},
	{
		action: PublishEntry, phrase: "publish entries", grants: grants{reachAll, reachAll, reachOwn, reachNo},
		own: viaOwner, others: "publish entries that other accounts made",
	},
	{
		action: DeleteEntry, phrase: "delete entries", grants: grants{reachAll, reachAll, reachOwn, reachNo},
		own: viaOwner, others: "delete entries that other accounts made",
	},
	{action: AddMedia, phrase: "add media", grants: grants{reachAll, reachAll, reachAll, reachNo}},
	{
		action: EditMedia, phrase: "edit media", grants: grants{reachAll, reachAll, reachOwn, reachNo},
		own: viaOwner, others: "edit media that other accounts uploaded",
	},
	{
		action: DeleteMedia, phrase: "delete media", grants: grants{reachAll, reachAll, reachOwn, reachNo},
		own: viaOwner, others: "delete media that other accounts uploaded",
	},
	{action: CreateAuthor, phrase: "create author profiles", grants: grants{reachAll, reachAll, reachNo, reachNo}},
	{
		action: EditAuthor, phrase: "edit author profiles", grants: grants{reachAll, reachAll, reachOwn, reachNo},
		own: viaAuthor, others: "edit author profiles other than its own",
	},
	{action: DeleteAuthor, phrase: "delete author profiles", grants: grants{reachAll, reachAll, reachNo, reachNo}},
	{action: EditTerms, phrase: "edit terms", grants: grants{reachAll, reachAll, reachNo, reachNo}},
	{action: EditMenus, phrase: "edit menus", grants: grants{reachAll, reachAll, reachNo, reachNo}},
	{action: EditForms, phrase: "edit forms", grants: grants{reachAll, reachAll, reachNo, reachNo}},
	{action: EditRedirects, phrase: "edit redirects", grants: grants{reachAll, reachAll, reachNo, reachNo}},
	{action: EditSettings, phrase: "edit the site settings", grants: grants{reachAll, reachNo, reachNo, reachNo}},
	{action: ManageBuilds, phrase: "manage builds and releases", grants: grants{reachAll, reachNo, reachNo, reachNo}},
	{action: ManageAccounts, phrase: "manage accounts", grants: grants{reachAll, reachNo, reachNo, reachNo}},
	{
		action: EditSelf, phrase: "edit its own account", grants: grants{reachOwn, reachOwn, reachOwn, reachOwn},
		own: viaAccount, others: "edit another account",
	},
	{action: ViewLogs, phrase: "view the logs", grants: grants{reachAll, reachNo, reachNo, reachNo}},
}

// Actions returns the actions in the order of the table of 7.3.
func Actions() []Action {
	actions := make([]Action, len(rules))
	for i, r := range rules {
		actions[i] = r.action
	}
	return actions
}

func findRule(act Action) *rule {
	for i := range rules {
		if rules[i].action == act {
			return &rules[i]
		}
	}
	return nil
}

// Check returns nil if the principal may do the action to the target, and else an error that wraps ErrForbidden. It
// allows nothing that it does not know: an account that is not active, a role that is not one of Roles, and an action
// that is not one of Actions.
func Check(p Principal, act Action, t Target) error {
	a := p.Account
	r := findRule(act)
	switch {
	case r == nil:
		return refuse(a, fmt.Sprintf("do %q", act))
	case a.Status != StatusActive:
		return refuse(a, r.phrase)
	}
	g := r.grants.of(a.Role)
	switch {
	case g == reachAll || (g == reachOwn && r.own.owns(a, t)):
		return nil
	case g == reachOwn:
		return refuse(a, r.others)
	}
	return refuse(a, r.phrase)
}

func Allowed(p Principal, act Action, t Target) bool {
	return Check(p, act, t) == nil
}

// refuse's text has the ID of the account, its role or, if it is not active, its status, and the phrase: nothing else of
// the account or the target. A role that is not one of Roles, which a row of another build can have, is quoted.
func refuse(a Account, phrase string) error {
	who := string(a.Role)
	if !slices.Contains(Roles(), a.Role) {
		who = strconv.Quote(who)
	}
	if a.Status != StatusActive {
		who = string(a.Status)
	}
	return fmt.Errorf("%s (%s): %w to %s", a.ID, who, ErrForbidden, phrase)
}
