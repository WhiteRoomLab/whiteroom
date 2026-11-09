package admin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
)

const (
	// loginBodyLimit fits a password of 8 KiB, the most that the accounts check, with every byte escaped in JSON.
	loginBodyLimit = 64 << 10
	// maxLogins is the most logins that attempts has let in and that have not ended. The accounts hash a few passwords
	// at once and the others wait, and attempts counts a login before that: without a limit, a flood would fill its tables
	// with the counts of logins that wait. The others get 503.
	maxLogins = 32

	cookieNameHTTPS = "__Host-session"
	cookieNameHTTP  = "session"
)

// sessionCookieName returns the name of the cookie of the session. For https it has the prefix __Host-, so that a browser
// takes it only from this host, with the path / and no Domain: another site of the same host cannot set it.
func sessionCookieName(https bool) string {
	if https {
		return cookieNameHTTPS
	}
	return cookieNameHTTP
}

// principalAnswer is the JSON of the answer to a login and to GET /api/session.
type principalAnswer struct {
	Account accountAnswer `json:"account"`
	Session sessionAnswer `json:"session"`
}

type accountAnswer struct {
	ID     string       `json:"id"`
	Login  string       `json:"login"`
	Name   string       `json:"name"`
	Email  string       `json:"email"`
	Role   account.Role `json:"role"`
	Author string       `json:"author,omitempty"`
}

type sessionAnswer struct {
	AuthenticatedAt time.Time        `json:"authenticatedAt"`
	ExpiresAt       time.Time        `json:"expiresAt"`
	IdleTimeout     int              `json:"idleTimeout"` // in seconds
	Factors         []account.Factor `json:"factors"`
}

func answerFor(p account.Principal) principalAnswer {
	a, s := p.Account, p.Session
	return principalAnswer{
		Account: accountAnswer{ID: a.ID, Login: a.Login, Name: a.Name, Email: a.Email, Role: a.Role, Author: a.Author},
		Session: sessionAnswer{
			AuthenticatedAt: s.AuthenticatedAt,
			ExpiresAt:       s.ExpiresAt,
			IdleTimeout:     int(account.SessionIdleTimeout / time.Second),
			Factors:         s.Factors,
		},
	}
}

// sessionCookie returns the token in the cookie of the session of r if r has exactly one such cookie: with more, one was
// not set by this server, and r has no session. sent says whether r has any.
func (s *Server) sessionCookie(r *http.Request) (token string, sent bool) {
	cookies := r.CookiesNamed(s.cookieName)
	if len(cookies) == 1 {
		token = cookies[0].Value
	}
	return token, len(cookies) > 0
}

// newCookie returns the cookie of the session. It is Secure also for http on localhost, which browsers treat as secure.
func (s *Server) newCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: s.cookieName, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: true,
		SameSite: http.SameSiteStrictMode,
	}
}

// setSessionCookie sets the cookie of the session, which has no Max-Age: the session ends on the server.
func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, s.newCookie(token, 0))
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, s.newCookie("", -1))
}

// fromAnotherSite reports whether r says, by Sec-Fetch-Site, that a page of another origin made it, which includes a page
// of another site of the same domain: a browser sends same-site or cross-site then. A request without the header is not
// told apart from one that a program sends.
func fromAnotherSite(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return false
	}
	return true
}

// withSession returns the handler that calls h with the Principal of the session in the cookie of the request, and else
// answers 401, and clears the cookie if the request had one. It is the one place that turns the cookie into a Principal:
// the routes of #48 that need a login go through it. A request from a page of another origin gets 403 before the cookie
// is read, whatever its method: a GET reads nothing across origins, but it would keep a session from going idle.
func (s *Server) withSession(h func(http.ResponseWriter, *http.Request, account.Principal)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fromAnotherSite(r) {
			writeProblem(w, http.StatusForbidden, codeCrossOrigin, detailCrossOrigin)
			return
		}
		token, sent := s.sessionCookie(r)
		p, err := s.accounts.Authenticate(r.Context(), token)
		switch {
		case err == nil:
			h(w, r, p)
		case errors.Is(err, account.ErrNoSession):
			if sent {
				s.clearSessionCookie(w)
			}
			writeProblem(w, http.StatusUnauthorized, codeUnauthenticated, detailUnauthenticated)
		default:
			s.fail(w, r, err)
		}
	})
}

func (s *Server) showSession(w http.ResponseWriter, _ *http.Request, p account.Principal) {
	writeJSON(w, http.StatusOK, answerFor(p))
}

// loginRequest is the body of POST /api/session; a pointer tells a member that is missing, or null, from an empty one.
type loginRequest struct {
	Login    *string `json:"login"`
	Password *string `json:"password"`
}

// errTooManyLogins is the error of startLogin when every place is taken.
var errTooManyLogins = errors.New("too many logins at once")

// startLogin is Login of the accounts, if there is a place for it among the logins that run (maxLogins).
func (s *Server) startLogin(ctx context.Context, login, password, previous string) (account.Principal, string, error) {
	select {
	case s.logins <- struct{}{}:
		defer func() { <-s.logins }()
	default:
		return account.Principal{}, "", errTooManyLogins
	}
	return s.accounts.Login(ctx, login, password, previous)
}

// login is POST /api/session. The order is fixed: the body, then the length of the password, then attempts.begin, then a
// place among the logins, then Login, and then succeeded or aborted. A password that no account can have is answered as a
// wrong one before begin, and counts nothing: it costs no hash, and would fill the tables for nothing. begin comes before
// the hash of the password and counts the login whether or not an account has the name. Once the password has been
// compared, the attempt stays counted whatever becomes of the login: after a wrong password, and after a right one that
// could not be answered, as when the client hangs up, so that no refund tells the two apart. Only when it was not compared
// does aborted give the attempt back: the places were taken, or Login failed before the comparison.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in loginRequest
	if !readJSON(w, r, loginBodyLimit, &in) {
		return
	}
	if in.Login == nil || in.Password == nil {
		writeProblem(w, http.StatusBadRequest, codeBadRequest, detailBadRequest)
		return
	}
	if account.PasswordTooLong(*in.Password) {
		writeProblem(w, http.StatusUnauthorized, codeBadCredentials, detailBadCredentials)
		return
	}
	name, client := nameKey(account.NormalizeLogin(*in.Login)), clientPrefix(s.clientAddr(r))
	if retry, ok := s.attempts.begin(name, client); !ok {
		writeTooManyAttempts(w, retry)
		return
	}
	previous, _ := s.sessionCookie(r)
	p, token, err := s.startLogin(r.Context(), *in.Login, *in.Password, previous)
	switch {
	case err == nil:
		s.attempts.succeeded(name, client)
		s.setSessionCookie(w, token)
		writeJSON(w, http.StatusOK, answerFor(p))
	case errors.Is(err, account.ErrBadCredentials):
		writeProblem(w, http.StatusUnauthorized, codeBadCredentials, detailBadCredentials)
	case errors.Is(err, errTooManyLogins):
		s.attempts.aborted(name, client)
		writeProblem(w, http.StatusServiceUnavailable, codeBusy, detailBusy)
	default:
		if !account.PasswordCompared(err) {
			s.attempts.aborted(name, client)
		}
		s.fail(w, r, err)
	}
}

// logout is DELETE /api/session: it ends the session in the cookie, if there is exactly one and it is a session, and
// clears the cookie. The browser is logged out either way.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	token, _ := s.sessionCookie(r)
	if err := s.accounts.Logout(r.Context(), token); err != nil {
		s.fail(w, r, err)
		return
	}
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}
