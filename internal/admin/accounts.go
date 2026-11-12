package admin

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// accountRoutes registers the routes of the accounts (7.3). An account is a small row: none of them uses the place of heavy
// work, the place of the logs or the operation IDs, and the accounts do the work in an Update of their own.
func (s *Server) accountRoutes() {
	s.api("GET /api/accounts", s.listAccounts)
	s.api("POST /api/accounts", s.createAccount)
	s.api("GET /api/accounts/{id}", s.showAccount)
	s.api("PATCH /api/accounts/{id}", s.updateAccount)
	s.api("DELETE /api/accounts/{id}", s.deleteAccount)
	s.api("POST /api/accounts/{id}/disable", s.disableAccount)
	s.api("POST /api/accounts/{id}/enable", s.enableAccount)
	s.api("POST /api/accounts/{id}/reset", s.resetAccount)
}

// tokenAnswer says that an account has a token, and for what and until when. The token itself is not in it.
type tokenAnswer struct {
	Purpose   account.Purpose `json:"purpose"`
	ExpiresAt time.Time       `json:"expiresAt"`
}

// accountView is an account as the administrators, and the account itself, see it: all of it but the secrets, which an
// account does not hold.
type accountView struct {
	ID                   string         `json:"id"`
	Login                string         `json:"login"`
	Name                 string         `json:"name"`
	Email                string         `json:"email"`
	Role                 account.Role   `json:"role"`
	Author               string         `json:"author,omitempty"`
	Status               account.Status `json:"status"`
	CreatedAt            time.Time      `json:"createdAt,omitzero"`
	CreatedBy            string         `json:"createdBy,omitempty"`
	CredentialsChangedAt time.Time      `json:"credentialsChangedAt,omitzero"`
	Token                *tokenAnswer   `json:"token,omitempty"`
}

// accountBrief is an account as the others see it: enough to show who made or saved something.
type accountBrief struct {
	ID     string       `json:"id"`
	Name   string       `json:"name"`
	Role   account.Role `json:"role"`
	Author string       `json:"author,omitempty"`
}

func viewOf(a account.Account) accountView {
	v := accountView{
		ID: a.ID, Login: a.Login, Name: a.Name, Email: a.Email, Role: a.Role, Author: a.Author, Status: a.Status,
		CreatedAt: a.CreatedAt.UTC(), CreatedBy: a.CreatedBy, CredentialsChangedAt: a.CredentialsChangedAt.UTC(),
	}
	if a.Token.Purpose != "" {
		v.Token = &tokenAnswer{Purpose: a.Token.Purpose, ExpiresAt: a.Token.ExpiresAt.UTC()}
	}
	return v
}

// seenBy returns a as the principal sees it: the whole account for an administrator and for the account itself, and else the
// brief (7.3 and 7.4 keep the login names and the email addresses of the others from the other roles).
func seenBy(p account.Principal, a account.Account) any {
	if a.ID == p.Account.ID || account.Allowed(p, account.ManageAccounts, account.Target{}) {
		return viewOf(a)
	}
	return accountBrief{ID: a.ID, Name: a.Name, Role: a.Role, Author: a.Author}
}

// accountList is the answer to GET /api/accounts.
type accountList struct {
	Items  []any `json:"items"`
	Total  int   `json:"total"`
	Offset int   `json:"offset"`
	Limit  int   `json:"limit"`
}

// listAccounts is GET /api/accounts: a page of the accounts in the order of their IDs.
func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request, p account.Principal) {
	if !s.allowView(w, r, p) || !noOperationID(w, r) {
		return
	}
	pg, _, ok := readListQuery(w, r, nil)
	if !ok {
		return
	}
	all, err := s.accounts.List(r.Context())
	if err != nil {
		s.answerError(w, r, p, err, attempt{})
		return
	}
	from, to := pg.bounds(len(all))
	items := make([]any, 0, to-from)
	for _, a := range all[from:to] {
		items = append(items, seenBy(p, a))
	}
	writeJSON(w, http.StatusOK, accountList{Items: items, Total: len(all), Offset: pg.offset, Limit: pg.limit})
}

// showAccount is GET /api/accounts/{id}.
func (s *Server) showAccount(w http.ResponseWriter, r *http.Request, p account.Principal) {
	if !s.allowView(w, r, p) || !noOperationID(w, r) {
		return
	}
	if _, ok := readQuery(w, r, nil); !ok {
		return
	}
	id := r.PathValue("id")
	if !validID(w, content.RefUser, id) {
		return
	}
	a, err := s.accounts.Get(r.Context(), id)
	if err != nil {
		s.answerError(w, r, p, err, attempt{})
		return
	}
	writeJSON(w, http.StatusOK, seenBy(p, a))
}

// accountBody is the body of an invitation and of a change of an account. A member is kept as the JSON that was sent, so that
// one that is not there is told from one that is null.
type accountBody struct {
	Login  jsontext.Value `json:"login"`
	Name   jsontext.Value `json:"name"`
	Email  jsontext.Value `json:"email"`
	Role   jsontext.Value `json:"role"`
	Author jsontext.Value `json:"author"`
}

// stringOf returns the string that the member v holds, or nil if the member was not sent. ok is false if it is null or not a
// string.
func stringOf(v jsontext.Value) (s *string, ok bool) {
	if len(v) == 0 {
		return nil, true
	}
	var str string
	if v.Kind() != '"' || json.Unmarshal(v, &str) != nil {
		return nil, false
	}
	return &str, true
}

// change returns the change that the body asks for: the members that were sent. ok is false for a member that is not a string.
func (b accountBody) change() (c account.Change, ok bool) {
	var role *string
	for _, m := range []struct {
		member jsontext.Value
		into   **string
	}{{b.Login, &c.Login}, {b.Name, &c.Name}, {b.Email, &c.Email}, {b.Author, &c.Author}, {b.Role, &role}} {
		s, ok := stringOf(m.member)
		if !ok {
			return account.Change{}, false
		}
		*m.into = s
	}
	if role != nil {
		r := account.Role(*role)
		c.Role = &r
	}
	return c, true
}

// input returns the invitation that the body asks for: a member that was not sent is empty, and the accounts say what is
// missing.
func (b accountBody) input() (in account.Input, ok bool) {
	c, ok := b.change()
	if !ok {
		return account.Input{}, false
	}
	text := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	in = account.Input{Login: text(c.Login), Name: text(c.Name), Email: text(c.Email), Author: text(c.Author)}
	if c.Role != nil {
		in.Role = *c.Role
	}
	return in, true
}

// accountReply is the answer to a change of an account: the account as the administrator or the account itself sees it.
type accountReply struct {
	Account accountView `json:"account"`
}

// invitationReply is the answer to an invitation. It is, with the answer to a reset, the one answer that holds a token: the
// token is the right to set up a credential for the account, and nothing keeps it but the one who is told (7.3).
type invitationReply struct {
	Account   accountView `json:"account"`
	Token     string      `json:"token"`
	ExpiresAt time.Time   `json:"expiresAt"`
}

// resetReply is the answer to a reset: the token for setting a new credential, and until when it works. ExpiresAt is left out
// if the account has no token when it is read, because it was used meanwhile.
type resetReply struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
}

// userTarget returns the target of the account id in the record of a refusal.
func userTarget(id string) audit.Target { return audit.Target{Kind: string(content.RefUser), ID: id} }

// createAccount is POST /api/accounts: it makes a pending account, and tells the token with which its holder sets the
// password, 201 with the Location of the account.
func (s *Server) createAccount(w http.ResponseWriter, r *http.Request, p account.Principal) {
	if !s.allow(w, r, p, account.ManageAccounts, account.Target{}, audit.Target{}) || !noOperationID(w, r) {
		return
	}
	if _, ok := readQuery(w, r, nil); !ok {
		return
	}
	var body *accountBody
	if !readJSON(w, r, smallBodyLimit, &body) {
		return
	}
	var in account.Input
	var ok bool
	if body != nil {
		in, ok = body.input()
	}
	if !ok {
		writeProblem(w, http.StatusBadRequest, codeBadRequest, detailBadRequest)
		return
	}
	s.at("checked")
	a, token, err := s.accounts.Invite(r.Context(), account.By(p.Account.ID), in)
	if err != nil {
		s.answerError(w, r, p, err, attempt{action: account.ManageAccounts})
		return
	}
	w.Header().Set("Location", "/api/accounts/"+a.ID)
	writeJSON(w, http.StatusCreated, invitationReply{Account: viewOf(a), Token: token, ExpiresAt: a.Token.ExpiresAt.UTC()})
}

// updateAccount is PATCH /api/accounts/{id}: it changes the members that the body has. The name and the email address of one's
// own account need account.self, and anything else accounts.manage, as the accounts decide in their Update; the route asks
// first, after it has read the body, which says what is changed, so that a refusal is recorded with the permission.
func (s *Server) updateAccount(w http.ResponseWriter, r *http.Request, p account.Principal) {
	if !noOperationID(w, r) {
		return
	}
	if _, ok := readQuery(w, r, nil); !ok {
		return
	}
	id := r.PathValue("id")
	if !validID(w, content.RefUser, id) {
		return
	}
	var body *accountBody
	if !readJSON(w, r, smallBodyLimit, &body) {
		return
	}
	var c account.Change
	var ok bool
	if body != nil {
		c, ok = body.change()
	}
	if !ok {
		writeProblem(w, http.StatusBadRequest, codeBadRequest, detailBadRequest)
		return
	}
	act := account.ManageAccounts
	if id == p.Account.ID && c.Login == nil && c.Role == nil && c.Author == nil {
		act = account.EditSelf
	}
	if !s.allow(w, r, p, act, account.Target{Account: id}, userTarget(id)) {
		return
	}
	s.at("checked")
	a, err := s.accounts.Update(r.Context(), account.By(p.Account.ID), id, c)
	if err != nil {
		s.answerError(w, r, p, err, attempt{action: act, target: userTarget(id)})
		return
	}
	writeJSON(w, http.StatusOK, accountReply{Account: viewOf(a)})
}

// changeAccount checks a request that an administrator makes to the account of the path, in the order of the other routes: the
// permission, the absence of an operation ID, the absence of a query, and the form of the ID. If one fails it has answered, and
// ok is false.
func (s *Server) changeAccount(w http.ResponseWriter, r *http.Request, p account.Principal) (id string, ok bool) {
	id = r.PathValue("id")
	if !s.allow(w, r, p, account.ManageAccounts, account.Target{Account: id}, userTarget(id)) || !noOperationID(w, r) {
		return "", false
	}
	if _, ok := readQuery(w, r, nil); !ok {
		return "", false
	}
	if !validID(w, content.RefUser, id) {
		return "", false
	}
	return id, true
}

// toggleAccount is the route of an operation on the account of the path that answers with the account: disable or enable.
func (s *Server) toggleAccount(w http.ResponseWriter, r *http.Request, p account.Principal,
	do func(ctx context.Context, by account.Actor, id string) (account.Account, error)) {
	id, ok := s.changeAccount(w, r, p)
	if !ok {
		return
	}
	s.at("checked")
	a, err := do(r.Context(), account.By(p.Account.ID), id)
	if err != nil {
		s.answerError(w, r, p, err, attempt{action: account.ManageAccounts, target: userTarget(id)})
		return
	}
	writeJSON(w, http.StatusOK, accountReply{Account: viewOf(a)})
}

// disableAccount is POST /api/accounts/{id}/disable, which ends the sessions of the account and keeps the rest of it.
func (s *Server) disableAccount(w http.ResponseWriter, r *http.Request, p account.Principal) {
	s.toggleAccount(w, r, p, s.accounts.Disable)
}

// enableAccount is POST /api/accounts/{id}/enable.
func (s *Server) enableAccount(w http.ResponseWriter, r *http.Request, p account.Principal) {
	s.toggleAccount(w, r, p, s.accounts.Enable)
}

// deleteAccount is DELETE /api/accounts/{id}, 204.
func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request, p account.Principal) {
	id, ok := s.changeAccount(w, r, p)
	if !ok {
		return
	}
	s.at("checked")
	if err := s.accounts.Delete(r.Context(), account.By(p.Account.ID), id); err != nil {
		s.answerError(w, r, p, err, attempt{action: account.ManageAccounts, target: userTarget(id)})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resetAccount is POST /api/accounts/{id}/reset: it removes the credentials of the account, ends its sessions, and tells the
// token with which its holder sets a new password. The account is read after the reset for the time at which the token stops
// working.
func (s *Server) resetAccount(w http.ResponseWriter, r *http.Request, p account.Principal) {
	id, ok := s.changeAccount(w, r, p)
	if !ok {
		return
	}
	s.at("checked")
	token, err := s.accounts.Reset(r.Context(), account.By(p.Account.ID), id)
	if err != nil {
		s.answerError(w, r, p, err, attempt{action: account.ManageAccounts, target: userTarget(id)})
		return
	}
	a, err := s.accounts.Get(r.Context(), id)
	if err != nil {
		s.answerError(w, r, p, err, attempt{})
		return
	}
	reply := resetReply{Token: token}
	if a.Token.Purpose != "" {
		reply.ExpiresAt = a.Token.ExpiresAt.UTC()
	}
	writeJSON(w, http.StatusOK, reply)
}
