package account

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"
	"time"

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

// expired reports whether the session cannot be used at now: SessionLifetime after the login, or SessionIdleTimeout
// after the last request.
func (s sessionRow) expired(now time.Time) bool {
	return !now.Before(s.AuthenticatedAt.Add(SessionLifetime)) || !now.Before(s.LastSeenAt.Add(SessionIdleTimeout))
}

// worksFor reports whether the session is still good for the account r, if it has not expired: the account is active,
// and its credentials are those that the session was made with (7.3).
func (s sessionRow) worksFor(r row) bool {
	return r.status() == StatusActive && r.CredentialsChangedAt.Equal(s.CredentialsChangedAt)
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

// sweepSessions removes the rows of the table sessions that cannot work at now, of any account, and returns the others:
// a row that does not decode, one that has expired, one whose account is missing, cannot be read or is not active, and
// one whose account has other credentials than it was made with. It reads each account once.
func sweepSessions(tx *store.Tx, now time.Time) ([]storedSession, error) {
	stored, err := tx.Sessions()
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
		works := err == nil && !s.expired(now)
		if works {
			a, seen := accounts[s.Account]
			if !seen {
				r, ok, err := readAccount(tx, s.Account)
				if err != nil {
					return nil, err
				}
				a = look{r, ok}
				accounts[s.Account] = a
			}
			works = a.ok && s.worksFor(a.r)
		}
		if !works {
			if err := tx.DeleteSession(st.Key); err != nil {
				return nil, err
			}
			continue
		}
		live = append(live, storedSession{st.Key, s})
	}
	return live, nil
}

// Login checks the password of the account with the login name, which it normalizes, as VerifyPasswordFactor does, and
// starts a session for it: the Principal of the account and its session, and the token that the client holds, which is
// the only secret of the session, since the store keeps its SHA-256. If previous is a token, its session is removed,
// whichever account it belongs to, so that a client that logs in again does not keep the old one. Of the account's
// sessions, the one used least recently goes when there would be more than SessionsPerAccount, and every session that
// cannot work any more goes, of any account. Every reason for a login to fail is ErrBadCredentials, and then nothing is
// written; only an error of the store, or the end of ctx, is another. The account is read again when the session is
// written, so that a password change or a disabling that committed after the hash was checked fails the login.
func (m *Manager) Login(ctx context.Context, login, password, previous string) (Principal, string, error) {
	found, ok, _, err := m.verify(ctx, login, password)
	if err != nil {
		return Principal{}, "", err
	}
	if !ok {
		return Principal{}, "", ErrBadCredentials
	}
	token, key, err := newSession(m.rand)
	if err != nil {
		return Principal{}, "", err
	}
	m.at("session")
	var out Principal
	err = m.st.Update(ctx, func(tx *store.Tx) error {
		r, ok, err := stillMatches(tx, found)
		switch {
		case err != nil:
			return err
		case !ok:
			return ErrBadCredentials
		}
		out, err = startSession(tx, r, key, previous, m.now())
		return err
	})
	if err != nil {
		return Principal{}, "", err
	}
	return out, token, nil
}

// startSession writes the session with the key for the account r in tx, after the removals that Login describes.
func startSession(tx *store.Tx, r row, key, previous string, now time.Time) (Principal, error) {
	live, err := sweepSessions(tx, now)
	if err != nil {
		return Principal{}, err
	}
	previousKey, hasPrevious := sessionKey(previous)
	var mine []storedSession
	for _, s := range live {
		switch {
		case hasPrevious && s.key == previousKey:
			if err := tx.DeleteSession(s.key); err != nil {
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
		if err := tx.DeleteSession(s.key); err != nil {
			return Principal{}, err
		}
	}
	s := sessionRow{
		Account: r.ID, CreatedAt: now, AuthenticatedAt: now, LastSeenAt: now,
		CredentialsChangedAt: r.CredentialsChangedAt, Factors: []Factor{FactorPassword},
	}
	if err := putSession(tx, key, s); err != nil {
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

// readSession reads the session with the key and its account in tx, and says whether the session works at now. The
// session and the account are those of a live session only.
func readSession(tx *store.Tx, key string, now time.Time) (sessionRow, row, sessionState, error) {
	data, ok, err := tx.Session(key)
	if err != nil || !ok {
		return sessionRow{}, row{}, sessionGone, err
	}
	s, err := decodeSession(key, data)
	if err != nil || s.expired(now) {
		return sessionRow{}, row{}, sessionDead, nil
	}
	r, ok, err := readAccount(tx, s.Account)
	switch {
	case err != nil:
		return sessionRow{}, row{}, sessionGone, err
	case !ok || !s.worksFor(r):
		return sessionRow{}, row{}, sessionDead, nil
	}
	return s, r, sessionLive, nil
}

// Authenticate returns the Principal of the session with this token, with the account as it is now, or ErrNoSession if
// there is none: the text is no token, no session has it, it has expired (SessionLifetime after the login, or
// SessionIdleTimeout after the last request), or its account is missing, cannot be read, is not active or has other
// credentials than the session was made with. A session that cannot work is removed. A request writes the time of the
// last request, only if the stored one is sessionTouch old or more, so that most requests only read; the store must be
// one that can write. Only an error of the store, or the end of ctx, is another error.
func (m *Manager) Authenticate(ctx context.Context, token string) (Principal, error) {
	key, ok := sessionKey(token)
	if !ok {
		return Principal{}, ErrNoSession
	}
	var s sessionRow
	var r row
	var state sessionState
	now := m.now()
	err := m.st.View(ctx, func(tx *store.Tx) (err error) {
		s, r, state, err = readSession(tx, key, now)
		return err
	})
	if err != nil {
		return Principal{}, err
	}
	m.at("authenticated")
	if state == sessionGone {
		return Principal{}, ErrNoSession
	}
	if state == sessionLive && now.Sub(s.LastSeenAt) < sessionTouch {
		return s.principal(r), nil
	}
	// The session is read again where it is written, so that what is written and what is returned are of one state.
	err = m.st.Update(ctx, func(tx *store.Tx) (err error) {
		now = m.now()
		s, r, state, err = readSession(tx, key, now)
		switch {
		case err != nil:
			return err
		case state == sessionDead:
			return tx.DeleteSession(key)
		case state == sessionLive && now.Sub(s.LastSeenAt) >= sessionTouch:
			s.LastSeenAt = now
			return putSession(tx, key, s)
		}
		return nil
	})
	if err != nil {
		return Principal{}, err
	}
	if state != sessionLive {
		return Principal{}, ErrNoSession
	}
	return s.principal(r), nil
}

// Logout removes the session with this token. A text that is not a token, and a token without a session, are not
// errors, and write nothing: the client is logged out either way.
func (m *Manager) Logout(ctx context.Context, token string) error {
	key, ok := sessionKey(token)
	if !ok {
		return nil
	}
	return m.st.Update(ctx, func(tx *store.Tx) error {
		if err := tx.DeleteSession(key); !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	})
}
