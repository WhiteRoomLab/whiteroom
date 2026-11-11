package account

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// Factor is a kind of credential that a login used.
type Factor string

// FactorPassword is the password, the only factor for now.
const FactorPassword Factor = "password"

// Session tells how the session of an account was authenticated, which Check does not read yet (7.3).
type Session struct {
	// AuthenticatedAt is when the account proved who it is for this session, which is its login.
	AuthenticatedAt time.Time
	// ExpiresAt is when the session ends whatever it is used for: SessionLifetime after AuthenticatedAt.
	ExpiresAt time.Time
	// Factors are the factors that the login used.
	Factors []Factor
}

const (
	// SessionIdleTimeout is how long a session may go without a request before it ends.
	SessionIdleTimeout = time.Hour
	// SessionLifetime is how long a session lasts from the login, whatever it is used for.
	SessionLifetime = 12 * time.Hour
	// SessionsPerAccount is the most sessions an account has: a login that would make one more removes the one that was
	// used least recently.
	SessionsPerAccount = 10

	// sessionTouch is how old the last request of a session in the store may get before a request writes the time again.
	sessionTouch = 5 * time.Minute
)

// ErrNoSession is the error of Authenticate for a text that is no session: not a token, a token without a session, and a
// session that has ended, whatever the reason.
var ErrNoSession = errors.New("no valid session")

// sessionRow is the value of a session in the table sessions of the store, under the key that sessionKey makes of its
// token: JSON that encodeSession writes in one form and decodeSession reads in any form that has the same keys, types
// and values. It holds no token, and nothing of the request that made the session. Its keys are the stored format,
// which changes only with a new schema of the table (logSchemaVersion of internal/store).
type sessionRow struct {
	Account         string    `json:"account"`
	CreatedAt       time.Time `json:"createdAt"`
	AuthenticatedAt time.Time `json:"authenticatedAt"`
	LastSeenAt      time.Time `json:"lastSeenAt"`
	// CredentialsChangedAt is the value of the account when the session was made, which the session ends without.
	CredentialsChangedAt time.Time `json:"credentialsChangedAt,omitzero"`
	Factors              []Factor  `json:"factors"`
}

// String and GoString keep what fmt prints of a row to the account.
func (s sessionRow) String() string {
	return fmt.Sprintf("session row of %q", s.Account)
}

func (s sessionRow) GoString() string {
	return s.String()
}

// encodeSession writes s in the one form that the operations write, with its times in UTC.
func encodeSession(s sessionRow) ([]byte, error) {
	s.CreatedAt = s.CreatedAt.UTC()
	s.AuthenticatedAt = s.AuthenticatedAt.UTC()
	s.LastSeenAt = s.LastSeenAt.UTC()
	s.CredentialsChangedAt = s.CredentialsChangedAt.UTC()
	return json.Marshal(s)
}

// decodeSession reads the row of the session with the key. Unknown and duplicate keys, a value of the wrong type,
// invalid UTF-8 and data after the row fail it, and so does a row that check refuses. Its error shows nothing of data.
func decodeSession(key string, data []byte) (sessionRow, error) {
	var s sessionRow
	if err := json.Unmarshal(data, &s, json.RejectUnknownMembers(true)); err != nil || !s.check() {
		return sessionRow{}, fmt.Errorf("sessions %q: invalid value", key)
	}
	return s, nil
}

// check reports whether s has the stored format: the ID of a user, three times that are set, at least one factor and no
// factor that is empty, and times in the years that encodeSession can write, so that a row that is read can be written
// again. What the factors are, and whether the account exists, are for the operations to see.
func (s sessionRow) check() bool {
	if content.CheckID(content.RefUser, s.Account) != nil {
		return false
	}
	if s.CreatedAt.IsZero() || s.AuthenticatedAt.IsZero() || s.LastSeenAt.IsZero() {
		return false
	}
	if len(s.Factors) == 0 || slices.Contains(s.Factors, "") {
		return false
	}
	return inYears(s.CreatedAt) && inYears(s.AuthenticatedAt) && inYears(s.LastSeenAt) && inYears(s.CredentialsChangedAt)
}

// runOut reports whether the session cannot be used at now: SessionLifetime after the login, or SessionIdleTimeout
// after the last request. If both have passed, it tells the one that passed first.
func (s sessionRow) runOut(now time.Time) (sessionEnd, bool) {
	idle, lifetime := s.LastSeenAt.Add(SessionIdleTimeout), s.AuthenticatedAt.Add(SessionLifetime)
	idlePassed, lifetimePassed := !now.Before(idle), !now.Before(lifetime)
	switch {
	case !idlePassed && !lifetimePassed:
		return sessionEnd{}, false
	case lifetimePassed && (!idlePassed || !idle.Before(lifetime)):
		return sessionEnd{reason: endedLifetime, account: s.Account, expired: lifetime}, true
	}
	return sessionEnd{reason: endedIdle, account: s.Account, expired: idle}, true
}

// accountEnd reports whether the session is no longer good for its account r, which is missing if ok is false: the
// account is active, and its credentials are those that the session was made with (7.3).
func (s sessionRow) accountEnd(r row, ok bool) (sessionEnd, bool) {
	end := sessionEnd{account: s.Account}
	switch {
	case !ok:
		end.reason = endedMissing
	case r.status() != StatusActive:
		end.reason = endedInactive
	case !r.CredentialsChangedAt.Equal(s.CredentialsChangedAt):
		end.reason = endedCredentials
	default:
		return sessionEnd{}, false
	}
	return end, true
}

// principal is who a request with this session is, for the account r that it belongs to.
func (s sessionRow) principal(r row) Principal {
	authenticated := s.AuthenticatedAt.UTC()
	return Principal{
		Account: r.account(),
		Session: Session{
			AuthenticatedAt: authenticated, ExpiresAt: authenticated.Add(SessionLifetime), Factors: slices.Clone(s.Factors),
		},
	}
}

// sessionKey returns the key of the row of the session with this token: the 64 lower case hexadecimal digits of the
// SHA-256 of its text. It is false for a text that is not a token, which has no row.
func sessionKey(token string) (string, bool) {
	hash, ok := tokenHash(token)
	return strings.TrimPrefix(hash, tokenHashPrefix), ok
}

// newSession reads a new token from r, and returns it with the key of its row.
func newSession(r io.Reader) (token, key string, err error) {
	token, hash, err := newToken(r)
	if err != nil {
		return "", "", err
	}
	return token, strings.TrimPrefix(hash, tokenHashPrefix), nil
}

// putSession writes the row s of the session with the key, after it has checked that the row can be read.
func putSession(tx *store.Tx, key string, s sessionRow) error {
	data, err := encodeSession(s)
	if err != nil {
		return err
	}
	if _, err := decodeSession(key, data); err != nil {
		return fmt.Errorf("cannot write a session of %s: invalid value", s.Account)
	}
	return tx.PutSession(key, data)
}

// storedSession is a row of the table sessions that decodes, with its key.
type storedSession struct {
	key string
	sessionRow
}

// olderFirst orders the sessions by their last request, then by their creation, then by their keys.
func olderFirst(a, b storedSession) int {
	return cmp.Or(a.LastSeenAt.Compare(b.LastSeenAt), a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.key, b.key))
}

// sweepSessions removes the rows of the table sessions that cannot work at now, of any account, with a record of why,
// and returns the others: a row that does not decode, one that has expired, one whose account is missing, cannot be read
// or is not active, and one whose account has other credentials than it was made with. It reads each account once.
func sweepSessions(h *history, now time.Time) ([]storedSession, error) {
	stored, err := h.tx.Sessions()
	if err != nil {
		return nil, err
	}
	type look struct {
		r  row
		ok bool
	}
	accounts := make(map[string]look)
	var live []storedSession
	for _, st := range stored {
		s, err := decodeSession(st.Key, st.Data)
		var end sessionEnd
		var ended bool
		if err != nil {
			end, ended = sessionEnd{reason: endedUnreadable}, true
		} else if end, ended = s.runOut(now); !ended {
			a, seen := accounts[s.Account]
			if !seen {
				r, ok, err := readAccount(h.tx, s.Account)
				if err != nil {
					return nil, err
				}
				a = look{r, ok}
				accounts[s.Account] = a
			}
			end, ended = s.accountEnd(a.r, a.ok)
		}
		if ended {
			if err := h.endSession(st.Key, end, Client{}); err != nil {
				return nil, err
			}
			continue
		}
		live = append(live, storedSession{st.Key, s})
	}
	return live, nil
}

// removeSessions removes every row of the table sessions that decodes and names the account id, with a record of the
// reason. An operation that changes what the sessions of an account rely on calls it in the Update that writes the
// change, after every check of the operation has passed, so that no session outlives the state it was made for, and a
// refused operation writes nothing. A row that does not decode names nobody, and stays for the next sweep.
func removeSessions(h *history, id, reason string) error {
	stored, err := h.tx.Sessions()
	if err != nil {
		return err
	}
	for _, st := range stored {
		if s, err := decodeSession(st.Key, st.Data); err == nil && s.Account == id {
			if err := h.endSession(st.Key, sessionEnd{reason: reason, account: id}, Client{}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Login checks the password of the account with the login name, which it normalizes, as VerifyPasswordFactor does, and
// starts a session for it: the Principal of the account and its session, and the token that the client holds, which is
// the only secret of the session, since the store keeps its SHA-256. If previous is a token, its session is removed,
// whichever account it belongs to, so that a client that logs in again does not keep the old one. Of the account's
// sessions, the one used least recently goes when there would be more than SessionsPerAccount, and every session that
// cannot work any more goes, of any account. Every reason for a login to fail is ErrBadCredentials, and then only the
// login history is written, with the reason, in an Update of its own that the end of ctx does not stop (7.4); only an
// error of the store, or the end of ctx, is another. The account is read again when the session is written, so that a
// password change or a disabling that committed after the hash was checked fails the login. An error that comes after
// the password was compared with the hash, such as the end of ctx while the session is written, is marked for
// PasswordCompared and keeps its text and its chain.
//
// A password hash with fewer iterations than the Manager's is made again, with the password that matched, and written in
// the Update that writes the session: CredentialsChangedAt and the account's other sessions stay as they are. A login
// or a VerifyPasswordFactor that matched the old hash and reads the account after that fails, once.
func (m *Manager) Login(ctx context.Context, c Client, login, password, previous string) (Principal, string, error) {
	found, ok, rehash, why, err := m.verify(ctx, login, password)
	switch {
	case err != nil:
		return Principal{}, "", err
	case !ok:
		m.loginFailed(ctx, c, login, why.account, why.result)
		return Principal{}, "", ErrBadCredentials
	}
	out, token, err := m.openSession(ctx, c, found, rehash, password, previous)
	if err != nil {
		result := failError
		if errors.Is(err, ErrBadCredentials) {
			result = failChanged
		}
		m.loginFailed(ctx, c, login, found.ID, result)
		return Principal{}, "", compared(err)
	}
	return out, token, nil
}

// openSession is the part of Login after the password matched the row found: it makes the hash again if rehash, and
// writes the session.
func (m *Manager) openSession(ctx context.Context, c Client, found row, rehash bool, password, previous string) (Principal, string, error) {
	var upgraded string
	if rehash {
		var err error
		if upgraded, err = m.newHash(ctx, password); err != nil {
			return Principal{}, "", err
		}
	}
	token, key, err := newSession(m.rand)
	if err != nil {
		return Principal{}, "", err
	}
	m.at("session")
	var out Principal
	err = m.logged(ctx, func(tx *store.Tx, h *history) error {
		r, ok, err := stillMatches(tx, found)
		switch {
		case err != nil:
			return err
		case !ok:
			return ErrBadCredentials
		}
		if rehash {
			r.Password = upgraded
			if err := putRow(tx, r); err != nil {
				return err
			}
		}
		out, err = startSession(h, r, key, previous, m.now(), audit.Login, c)
		return err
	})
	if err != nil {
		return Principal{}, "", err
	}
	return out, token, nil
}

// comparedError is an error of Login that came after the typed password was compared with the stored hash. It has the
// text and the chain of the error it wraps.
type comparedError struct{ err error }

func (e comparedError) Error() string { return e.err.Error() }

func (e comparedError) Unwrap() error { return e.err }

// compared marks err, which Login met after the comparison. ErrBadCredentials is the answer to a password that does not
// match, and stays as it is.
func compared(err error) error {
	if errors.Is(err, ErrBadCredentials) {
		return err
	}
	return comparedError{err}
}

// PasswordCompared reports whether an attempt whose Login ended with err must stay counted against a limit on the guesses of
// a password, which it must once the password was compared with a hash: err is ErrBadCredentials, which is also the answer
// to a password that is too long (see PasswordTooLong), or Login met it after the comparison, as when ctx ends while the
// session is written. An attempt that was given back then would let a client that hangs up after a right password tell it
// from a wrong one. An error of the store, or the end of ctx, before the comparison is not compared.
func PasswordCompared(err error) bool {
	var c comparedError
	return errors.Is(err, ErrBadCredentials) || errors.As(err, &c)
}

// startSession writes the session with the key for the account r in h, after the removals that Login describes, and
// records them and the start of the session: started is audit.Login for a login and audit.SessionStart for a session
// that takes the place of another.
func startSession(h *history, r row, key, previous string, now time.Time, started audit.Action, c Client) (Principal, error) {
	live, err := sweepSessions(h, now)
	if err != nil {
		return Principal{}, err
	}
	previousKey, hasPrevious := sessionKey(previous)
	var mine []storedSession
	for _, s := range live {
		switch {
		case hasPrevious && s.key == previousKey:
			if err := h.endSession(s.key, sessionEnd{reason: endedReplaced, account: s.Account}, Client{}); err != nil {
				return Principal{}, err
			}
		case s.key == key:
			return Principal{}, errors.New("cannot make a session: the token is already in use")
		case s.Account == r.ID:
			mine = append(mine, s)
		}
	}
	slices.SortFunc(mine, olderFirst)
	for _, s := range mine[:max(0, len(mine)-(SessionsPerAccount-1))] {
		if err := h.endSession(s.key, sessionEnd{reason: endedEvicted, account: s.Account}, Client{}); err != nil {
			return Principal{}, err
		}
	}
	s := sessionRow{
		Account: r.ID, CreatedAt: now, AuthenticatedAt: now, LastSeenAt: now,
		CredentialsChangedAt: r.CredentialsChangedAt, Factors: []Factor{FactorPassword},
	}
	if err := putSession(h.tx, key, s); err != nil {
		return Principal{}, err
	}
	if err := h.add(sessionRecord(started, key, r.ID, c)); err != nil {
		return Principal{}, err
	}
	return s.principal(r), nil
}

// sessionState says what a read of a session found.
type sessionState int

const (
	sessionGone sessionState = iota // there is no row
	sessionDead                     // the row cannot work any more, and is to be removed
	sessionLive
)

// sessionLook is what readSession found: the row of the session if it decodes, and the row of its account and the
// state if it is live, or else why it ended.
type sessionLook struct {
	state   sessionState
	session sessionRow
	account row
	end     sessionEnd
}

// readSession reads the session with the key and its account in tx, and says whether the session works at now.
func readSession(tx *store.Tx, key string, now time.Time) (sessionLook, error) {
	data, ok, err := tx.Session(key)
	if err != nil || !ok {
		return sessionLook{}, err
	}
	s, err := decodeSession(key, data)
	if err != nil {
		return sessionLook{state: sessionDead, end: sessionEnd{reason: endedUnreadable}}, nil
	}
	if end, ended := s.runOut(now); ended {
		return sessionLook{state: sessionDead, session: s, end: end}, nil
	}
	r, ok, err := readAccount(tx, s.Account)
	if err != nil {
		return sessionLook{}, err
	}
	if end, ended := s.accountEnd(r, ok); ended {
		return sessionLook{state: sessionDead, session: s, end: end}, nil
	}
	return sessionLook{state: sessionLive, session: s, account: r}, nil
}

// Authenticate returns the Principal of the session with this token, with the account as it is now, or ErrNoSession if
// there is none: the text is no token, no session has it, it has expired (SessionLifetime after the login, or
// SessionIdleTimeout after the last request), or its account is missing, cannot be read, is not active or has other
// credentials than the session was made with. A session that cannot work is removed, and its end is recorded with c. A
// request writes the time of the last request, and records it with c, only if the stored one is sessionTouch old or
// more, so that most requests only read; the store must be one that can write. Only an error of the store, or the end of
// ctx, is another error.
func (m *Manager) Authenticate(ctx context.Context, c Client, token string) (Principal, error) {
	key, ok := sessionKey(token)
	if !ok {
		return Principal{}, ErrNoSession
	}
	var look sessionLook
	now := m.now()
	err := m.st.View(ctx, func(tx *store.Tx) (err error) {
		look, err = readSession(tx, key, now)
		return err
	})
	if err != nil {
		return Principal{}, err
	}
	m.at("authenticated")
	if look.state == sessionGone {
		return Principal{}, ErrNoSession
	}
	if look.state == sessionLive && now.Sub(look.session.LastSeenAt) < sessionTouch {
		return look.session.principal(look.account), nil
	}
	// The session is read again where it is written, so that what is written and what is returned are of one state.
	err = m.logged(ctx, func(tx *store.Tx, h *history) (err error) {
		now = m.now()
		look, err = readSession(tx, key, now)
		switch {
		case err != nil:
			return err
		case look.state == sessionDead:
			return h.endSession(key, look.end, c)
		case look.state == sessionLive && now.Sub(look.session.LastSeenAt) >= sessionTouch:
			look.session.LastSeenAt = now
			if err := putSession(tx, key, look.session); err != nil {
				return err
			}
			return h.add(sessionRecord(audit.SessionSeen, key, look.session.Account, c))
		}
		return nil
	})
	if err != nil {
		return Principal{}, err
	}
	if look.state != sessionLive {
		return Principal{}, ErrNoSession
	}
	return look.session.principal(look.account), nil
}

// Logout removes the session with this token, and records it with c. A text that is not a token, and a token without a
// session, are not errors, and write nothing: the client is logged out either way.
func (m *Manager) Logout(ctx context.Context, c Client, token string) error {
	key, ok := sessionKey(token)
	if !ok {
		return nil
	}
	return m.logged(ctx, func(tx *store.Tx, h *history) error {
		data, ok, err := tx.Session(key)
		if err != nil || !ok {
			return err
		}
		if err := tx.DeleteSession(key); err != nil {
			return err
		}
		var account string
		if s, err := decodeSession(key, data); err == nil {
			account = s.Account
		}
		return h.add(sessionRecord(audit.Logout, key, account, c))
	})
}

// ChangeOwnPassword changes the password of the account that the session with this token belongs to, to next, if current
// is its password, as ChangePassword does, and returns the Principal and the token of a new session that goes on in
// place of the old one: it is authenticated now, and every session of the account ends in the same Update, the one that
// asked too, so that a Cookie that was stolen does not outlive the change. The new session is recorded with c. A text
// that is no token, and a session that does not work (see Authenticate), at the first look or in the Update, are
// ErrNoSession, which comes before every other reason to refuse, since the session and the account are read together; an
// account that Check does not allow to edit itself is refused with an error that wraps ErrForbidden. The hashes are made
// outside the Update, and wait for their places as in ChangePassword. A call that is refused writes nothing, and leaves
// every session as it was.
func (m *Manager) ChangeOwnPassword(ctx context.Context, c Client, token, current, next string) (Principal, string, error) {
	oldKey, ok := sessionKey(token)
	if !ok {
		return Principal{}, "", ErrNoSession
	}
	var stored row
	err := m.st.View(ctx, func(tx *store.Tx) (err error) {
		_, stored, _, err = lookAtOwnAccount(tx, oldKey, m.now())
		return err
	})
	if err != nil {
		return Principal{}, "", err
	}
	hash, err := m.hashNewPassword(ctx, stored, current, next)
	if err != nil {
		return Principal{}, "", err
	}
	fresh, key, err := newSession(m.rand)
	if err != nil {
		return Principal{}, "", err
	}
	var out Principal
	err = m.logged(ctx, func(tx *store.Tx, h *history) error {
		now := m.now()
		_, r, rows, err := lookAtOwnAccount(tx, oldKey, now)
		if err != nil {
			return err
		}
		changed, err := rewritePassword(tx, rows, r.ID, stored, next, hash, now)
		if err != nil {
			return err
		}
		if err := removeSessions(h, r.ID, endedPassword); err != nil {
			return err
		}
		out, err = startSession(h, changed, key, "", now, audit.SessionStart, c)
		return err
	})
	if err != nil {
		return Principal{}, "", err
	}
	return out, fresh, nil
}

// lookAtOwnAccount is what ChangeOwnPassword reads, first and again in its Update, from one state of the store: the
// session with this key and the row of its account, if the session works at now, and every row of the accounts, as an
// operation that writes reads them. It is ErrNoSession if the session does not work, and else an error that wraps
// ErrForbidden if Check does not allow the account to edit itself.
func lookAtOwnAccount(tx *store.Tx, key string, now time.Time) (sessionRow, row, []row, error) {
	look, err := readSession(tx, key, now)
	switch {
	case err != nil:
		return sessionRow{}, row{}, nil, err
	case look.state != sessionLive:
		return sessionRow{}, row{}, nil, ErrNoSession
	}
	s, r := look.session, look.account
	rows, err := loadRows(tx)
	if err != nil {
		return sessionRow{}, row{}, nil, err
	}
	if err := Check(s.principal(r), EditSelf, Target{Account: r.ID}); err != nil {
		return sessionRow{}, row{}, nil, err
	}
	return s, r, rows, nil
}
