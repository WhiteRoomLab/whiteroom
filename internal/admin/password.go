package admin

import (
	"crypto/sha256"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

const (
	// passwordAttemptsCapacity is the most sessions whose wrong current passwords passwordAttempts keeps count of.
	passwordAttemptsCapacity = 10000
	// msgWrongCurrent is the problem of the field current when the password is not that of the account.
	msgWrongCurrent = "wrong password"
)

// passwordRoutes registers the routes that set a password: the change of one's own, and the use of a token. Neither uses the
// place of heavy work: the work of a request is a few hashes, which the Manager of the accounts limits.
func (s *Server) passwordRoutes() {
	s.api("PUT /api/session/password", s.changeOwnPassword)
	s.handle("POST /api/password", http.HandlerFunc(s.setPassword))
}

// passwordAttempts counts the wrong current passwords that each session has tried in a change of its account's password, so
// that a session that was stolen cannot be used to guess the password as fast as the hashes go. It counts as attempts counts
// the logins of a login name from a client: five attempts are free, then the waits begin at 30 seconds and double up to an
// hour, and the count is forgotten 24 hours after the last attempt, or when the password is right. A session is named by the
// SHA-256 of its token, so that no token is kept. It is in memory only; a restart forgets it. When the table is full, the
// session that is forgotten is one that has expired, else the least recently used one with fewer than freeAttempts attempts,
// else the one whose last attempt is the oldest: a session is never refused for lack of room.
type passwordAttempts struct {
	mu       sync.Mutex
	clock    func() time.Time
	start    time.Time
	sessions *table[[32]byte]
}

func newPasswordAttempts(now func() time.Time, capacity int) *passwordAttempts {
	return &passwordAttempts{clock: now, start: now(), sessions: newTable[[32]byte](max(capacity, 1))}
}

// begin counts an attempt of the session with the key, before its current password is checked. If the session has to wait,
// nothing is counted, and retry is the wait in whole seconds. An attempt that is allowed ends with succeeded, or with aborted
// if the password was not checked; after a wrong password nothing is called, and the attempt stays counted.
func (a *passwordAttempts) begin(key [32]byte) (retry time.Duration, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.clock().Sub(a.start)
	e := a.sessions.entries[key]
	if e != nil && e.expires > now && e.count >= freeAttempts {
		if until := e.expires - pairForget + waitAfter(e.count); until > now {
			return (until - now + time.Second - 1) / time.Second * time.Second, false
		}
	}
	expires := now + pairForget
	switch {
	case e == nil:
		e = a.sessions.insert(key, expires, now)
	case e.expires <= now:
		e.count = 0
		a.sessions.update(e, expires)
	default:
		a.sessions.update(e, expires)
	}
	e.count++
	if e.count < freeAttempts {
		a.sessions.idle.pushFront(e)
	} else {
		a.sessions.held.pushFront(e)
	}
	return 0, true
}

// succeeded forgets the session: its current password was right.
func (a *passwordAttempts) succeeded(key [32]byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if e := a.sessions.entries[key]; e != nil {
		a.sessions.remove(e)
	}
}

// aborted gives back the attempt that begin counted, when the current password was not checked. The session keeps the time of
// its last attempt.
func (a *passwordAttempts) aborted(key [32]byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e := a.sessions.entries[key]
	if e == nil {
		return
	}
	e.count--
	switch e.count {
	case 0:
		a.sessions.remove(e)
	case freeAttempts - 1:
		a.sessions.idle.pushFront(e)
	}
}

// changePasswordRequest is the body of PUT /api/session/password.
type changePasswordRequest struct {
	Current  *string `json:"current"`
	Password *string `json:"password"`
}

// changeOwnPassword is PUT /api/session/password: the account of the session changes its password, if the current one is
// right, and goes on with a new session, whose cookie replaces the old one; every other session of the account ends. The wrong
// current passwords of a session are counted (passwordAttempts), and a session that has to wait is answered with 429 before
// the account is asked, so that a password that is right does not shorten the wait. A current password of more than 8 KiB is
// answered as a wrong one, and counts nothing.
func (s *Server) changeOwnPassword(w http.ResponseWriter, r *http.Request, p account.Principal) {
	self := audit.Target{Kind: string(content.RefUser), ID: p.Account.ID}
	if !s.allow(w, r, p, account.EditSelf, account.Target{Account: p.Account.ID}, self) || !noOperationID(w, r) {
		return
	}
	if _, ok := readQuery(w, r, nil); !ok {
		return
	}
	var in changePasswordRequest
	if !readJSON(w, r, smallBodyLimit, &in) {
		return
	}
	if in.Current == nil || in.Password == nil {
		writeProblem(w, http.StatusBadRequest, codeBadRequest, detailBadRequest)
		return
	}
	if account.PasswordTooLong(*in.Current) {
		writeInvalid(w, content.Errors{{Path: "/current", Msg: msgWrongCurrent}})
		return
	}
	token, _ := s.sessionCookie(r)
	key := sha256.Sum256([]byte(token))
	if retry, ok := s.passwords.begin(key); !ok {
		writeTooManyAttempts(w, retry)
		return
	}
	s.at("checked")
	fresh, newToken, err := s.accounts.ChangeOwnPassword(r.Context(), s.client(r), token, *in.Current, *in.Password)
	switch {
	case err == nil:
		s.passwords.succeeded(key)
		s.setSessionCookie(w, newToken)
		writeJSON(w, http.StatusOK, answerFor(fresh))
		return
	case errors.Is(err, account.ErrBadCredentials):
		// The attempt stays counted.
	default:
		s.passwords.aborted(key)
	}
	s.answerError(w, r, p, err, attempt{action: account.EditSelf, target: self})
}

// setPasswordRequest is the body of POST /api/password.
type setPasswordRequest struct {
	Token    *string `json:"token"`
	Password *string `json:"password"`
}

// passwordAccountAnswer is the answer to POST /api/password: the account whose password is set, which is then active.
type passwordAccountAnswer struct {
	Account accountAnswer `json:"account"`
}

// setPassword is POST /api/password: the holder of the token of an invitation or a reset sets the first password of the
// account, and needs no session. The token is in the body and nowhere in the URL. A token that cannot be used is 401, and the
// accounts record it in the login history; the token has 256 bits, so no limit of attempts is needed to keep it from being
// guessed.
func (s *Server) setPassword(w http.ResponseWriter, r *http.Request) {
	if !noOperationID(w, r) {
		return
	}
	if _, ok := readQuery(w, r, nil); !ok {
		return
	}
	var in setPasswordRequest
	if !readJSON(w, r, smallBodyLimit, &in) {
		return
	}
	if in.Token == nil || in.Password == nil {
		writeProblem(w, http.StatusBadRequest, codeBadRequest, detailBadRequest)
		return
	}
	a, err := s.accounts.SetPassword(r.Context(), s.client(r), *in.Token, *in.Password)
	if err != nil {
		s.answerError(w, r, account.Principal{}, err, attempt{})
		return
	}
	writeJSON(w, http.StatusOK, passwordAccountAnswer{
		Account: accountAnswer{ID: a.ID, Login: a.Login, Name: a.Name, Email: a.Email, Role: a.Role, Author: a.Author},
	})
}
