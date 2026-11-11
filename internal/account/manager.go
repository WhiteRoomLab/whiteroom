package account

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"runtime"
	"slices"
	"strconv"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

const (
	invitationTTL = 7 * 24 * time.Hour
	resetTTL      = 24 * time.Hour
)

var (
	// ErrLastAdmin is wrapped by the error of an operation that would leave a store that has an active administrator
	// without one.
	ErrLastAdmin      = errors.New("the last active administrator")
	ErrInvalidToken   = errors.New("invalid or expired token")
	ErrBadCredentials = errors.New("wrong login name or password")
	// ErrConflict is wrapped by the error of an operation that found, when it came to write, that what it had checked
	// before had changed.
	ErrConflict = errors.New("changed since it was read")
)

// Manager does the account operations of 7.3 on the table accounts of a store, and the logins and sessions on the table
// sessions. Every operation that writes is one Update of the store, which reads every account first, so that the rules
// that look at all of them hold whatever else runs. An account operation that succeeds, also one that changes nothing,
// adds one record to the activity log in that Update (7.4); a refused one adds none. It computes only a few password
// hashes at once, whatever number of calls need one (see gated). It is safe for concurrent use.
type Manager struct {
	st         *store.Store
	now        func() time.Time
	ids        *content.IDSource
	rand       io.Reader // for salts and tokens; safe for concurrent use
	iterations int       // of the new password hashes
	// unknownHash is a hash of a text that nobody knows, with iterations when the Manager was made. It is set once.
	unknownHash string
	// hashes has a place for each password hash that may be computed at the same time.
	hashes chan struct{}
	// limit counts the failures of people who are not logged in, so that a flood does not fill the login history.
	limit failureLimit
	// recordError is nil unless OnRecordError sets it.
	recordError func(operation string, err error)
	// hook is nil outside the tests, which run another operation at a point of one.
	hook func(point string)
}

// New returns a Manager for s that reads the time from the system clock and random bits from crypto/rand. It makes here,
// and not at the first call, the hash that VerifyPasswordFactor and Login check for a login that no account can use, so
// that every call costs one hash of the Manager's count, the first one too. It fails only if the random bits cannot be
// read. A process makes one Manager for a store.
func New(s *store.Store) (*Manager, error) {
	return newManager(s, defaultIterations, rand.Reader)
}

// NewWithIterations is New with the count of the iterations of the password hashes. It is for the tests of other packages,
// which need cheap hashes: the real count takes about 80 ms for a hash, and ten times as long under the race detector.
// Production code uses New, and a forbidigo rule of .golangci.yml checks that no file of the module but a test refers
// to it. The count must be from 1 to 10000000.
func NewWithIterations(s *store.Store, iterations int) (*Manager, error) {
	return newManager(s, iterations, rand.Reader)
}

// newManager is New with the iteration count of the hashes and the source of the random bits, which the tests change.
func newManager(s *store.Store, iterations int, r io.Reader) (*Manager, error) {
	secret, _, err := newToken(r)
	if err != nil {
		return nil, err
	}
	hash, err := hashPassword(secret, iterations, r)
	if err != nil {
		return nil, err
	}
	return &Manager{
		st: s, now: time.Now, ids: content.NewIDSource(r), rand: r, iterations: iterations, unknownHash: hash,
		hashes: make(chan struct{}, max(1, runtime.GOMAXPROCS(0)/4)),
	}, nil
}

// SetClock sets the clock of the operations, which must be safe for concurrent use. It must be called before the
// Manager is used.
func (m *Manager) SetClock(now func() time.Time) {
	m.now = now
}

func (m *Manager) at(point string) {
	if m.hook != nil {
		m.hook(point)
	}
}

// gated runs f, which computes a password hash, once there is a place for it among the hashes that run: a Manager
// computes at most a quarter of GOMAXPROCS of them at the same time, and one at least, so that a flood of logins cannot
// take every processor for 80 ms each. It waits for a place until ctx ends, and then returns the error of ctx without
// having called f. The one hash that newManager makes is not gated.
func (m *Manager) gated(ctx context.Context, f func()) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case m.hashes <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-m.hashes }()
	m.at("hashing")
	f()
	return nil
}

// match is matchHash with the Manager's iteration count, through the gate. A typed password that matchHash refuses
// without hashing it takes no place.
func (m *Manager) match(ctx context.Context, encoded, p string) (ok, rehash bool, err error) {
	if PasswordTooLong(p) {
		return matchHash(encoded, p, m.iterations)
	}
	var matchErr error
	if err := m.gated(ctx, func() { ok, rehash, matchErr = matchHash(encoded, p, m.iterations) }); err != nil {
		return false, false, err
	}
	return ok, rehash, matchErr
}

// newHash is hashPassword with the Manager's iteration count and random bits, through the gate.
func (m *Manager) newHash(ctx context.Context, p string) (hash string, err error) {
	var hashErr error
	if err := m.gated(ctx, func() { hash, hashErr = hashPassword(p, m.iterations, m.rand) }); err != nil {
		return "", err
	}
	return hash, hashErr
}

// Actor is who does an account operation: an account, by By, or the commands on the server, System. It does not carry how
// the session of the account was authenticated; 7.3 keeps that path for the operations that will ask for a recent
// re-authentication. The zero Actor, which By("") is too, may do nothing.
type Actor struct {
	id     string
	system bool
}

func By(id string) Actor {
	return Actor{id: id}
}

// System may do every account operation, but not what would leave a store without an active administrator, except
// Reset, which is how the commands on the server recover one (7.3).
var System = Actor{system: true}

// missingError is the error for an account or a login name that does not exist. It wraps fs.ErrNotExist.
type missingError string

func (e missingError) Error() string { return string(e) }

func (missingError) Unwrap() error { return fs.ErrNotExist }

func notFound(id string) error {
	return missingError(fmt.Sprintf("accounts %q: not found", id))
}

// loadRows reads every row of the table accounts, in the order of the IDs, for the operations that write and for List. A
// row that does not decode fails it, since the rules that look at all the accounts could not hold otherwise.
func loadRows(tx *store.Tx) ([]row, error) {
	return readRows(tx, false)
}

// readableRows reads the rows of the table accounts that decode, in the order of the IDs, and leaves out the others. It
// is for the look-up of one login name, which needs no other row: a row that cannot be read is no account. It still
// fails on an error of the store.
func readableRows(tx *store.Tx) ([]row, error) {
	return readRows(tx, true)
}

// readRows decodes every row of the table accounts, in the order of the IDs. A row that does not decode is left out if
// skip is true, and else fails it.
func readRows(tx *store.Tx, skip bool) ([]row, error) {
	stored, err := tx.Accounts()
	if err != nil {
		return nil, err
	}
	rows := make([]row, 0, len(stored))
	for _, s := range stored {
		r, err := decodeRow(s.ID, s.Data)
		switch {
		case err == nil:
			rows = append(rows, r)
		case !skip:
			return nil, err
		}
	}
	return rows, nil
}

// readAccount reads the row of the account id: ok is false for a row that is missing and for one that does not decode,
// which is no account to a login or a session, and only an error of the store is an error.
func readAccount(tx *store.Tx, id string) (r row, ok bool, err error) {
	data, ok, err := tx.Account(id)
	if err != nil || !ok {
		return row{}, false, err
	}
	r, err = decodeRow(id, data)
	return r, err == nil, nil
}

// removeUnreadable removes the row of the account id if it exists and does not decode, and reports whether it did. It
// reads no other row: System clears the rows that cannot be read one at a time, however many there are. The rule of the
// last active administrator does not apply, since it cannot count a row that cannot be read, before or after.
func removeUnreadable(tx *store.Tx, id string) (bool, error) {
	if content.CheckID(content.RefUser, id) != nil {
		return false, nil
	}
	data, ok, err := tx.Account(id)
	if err != nil || !ok {
		return false, err
	}
	if _, err := decodeRow(id, data); err == nil {
		return false, nil
	}
	if err := tx.DeleteAccount(id); err != nil {
		return false, err
	}
	return true, nil
}

func indexOfID(rows []row, id string) int {
	return slices.IndexFunc(rows, func(r row) bool { return r.ID == id })
}

// authorize checks that by may do act, which is about the account target, with the accounts as they are in rows:
// System always; an account if it is one of rows and Check allows it.
func authorize(rows []row, by Actor, act Action, target string) error {
	switch {
	case by.system:
		return nil
	case by.id == "":
		return fmt.Errorf("%w: the operation has no actor", ErrForbidden)
	}
	i := indexOfID(rows, by.id)
	if i < 0 {
		who := by.id
		if content.CheckID(content.RefUser, who) != nil {
			who = strconv.Quote(who)
		}
		return fmt.Errorf("%s: %w: no such account", who, ErrForbidden)
	}
	return Check(Principal{Account: rows[i].account()}, act, Target{Account: target})
}

// invalidToken is the error for a token that cannot be used: it is ErrInvalidToken, with what the login history records
// of it, which a caller of the Manager does not see.
type invalidToken struct {
	result  string
	account string // of the token, if some account has it
}

func (invalidToken) Error() string { return ErrInvalidToken.Error() }

func (invalidToken) Unwrap() error { return ErrInvalidToken }

// redeemToken finds the account that token was given to, and returns the index of its row in rows and that row without
// the token, which is thereby used up. It is how every token is used, whatever credential it sets up: a password now,
// and later a passkey, by the same rules (7.3). The token must not be expired at now and its account must not be
// disabled; anything else, also a text that is not a token, is ErrInvalidToken.
func redeemToken(rows []row, token string, now time.Time) (int, row, error) {
	hash, ok := tokenHash(token)
	if !ok {
		return 0, row{}, invalidToken{result: tokenMalformed}
	}
	// Every row is compared, so that the time does not tell which one has the token.
	found := -1
	for i, r := range rows {
		if equalHash(r.Token.Hash, hash) && found < 0 {
			found = i
		}
	}
	switch {
	case found < 0:
		return 0, row{}, invalidToken{result: tokenUnknown}
	case rows[found].Disabled:
		return 0, row{}, invalidToken{result: tokenDisabled, account: rows[found].ID}
	case !now.Before(rows[found].Token.ExpiresAt):
		return 0, row{}, invalidToken{result: tokenExpired, account: rows[found].ID}
	}
	used := rows[found]
	used.Token = tokenRow{}
	return found, used, nil
}

// activeAdmins counts the administrators that can sign in: those that are not disabled and have a credential.
func activeAdmins(rows []row) int {
	n := 0
	for _, r := range rows {
		if r.Role == RoleAdmin && r.status() == StatusActive {
			n++
		}
	}
	return n
}

// checkLastAdmin is the rule of the last active administrator: a change of the rows from before to after that leaves
// no active administrator in a store that had one is refused. id is the account that changes, and ending completes the
// error. Every operation that writes goes through it, by put and remove, except Reset by System; a later operation that
// removes a credential, such as the last passkey of an administrator that has no password, must go through it too.
func checkLastAdmin(id string, before, after []row, ending string) error {
	if activeAdmins(before) > 0 && activeAdmins(after) == 0 {
		return fmt.Errorf("%s is %w: %s", id, ErrLastAdmin, ending)
	}
	return nil
}

// noAdminLost completes the error of checkLastAdmin for an operation that cannot lose an administrator.
const noAdminLost = "it would leave no active administrator"

// put writes next, the new row of one account, which may be new, after the rule of the last active administrator.
func put(tx *store.Tx, rows []row, next row, ending string) error {
	after := slices.Clone(rows)
	if i := indexOfID(after, next.ID); i >= 0 {
		after[i] = next
	} else {
		after = append(after, next)
	}
	if err := checkLastAdmin(next.ID, rows, after, ending); err != nil {
		return err
	}
	return putRow(tx, next)
}

// putRow writes next without the rule of the last active administrator, which only Reset by System does without. It
// checks that the row can be read, since a row that cannot would fail every later operation.
func putRow(tx *store.Tx, next row) error {
	data, err := encodeRow(next)
	if err != nil {
		return err
	}
	if _, err := decodeRow(next.ID, data); err != nil {
		return fmt.Errorf("cannot write %s: %w", next.ID, err)
	}
	return tx.PutAccount(next.ID, data)
}

// remove deletes the row of the account id, after the rule of the last active administrator.
func remove(tx *store.Tx, rows []row, id, ending string) error {
	after := slices.DeleteFunc(slices.Clone(rows), func(r row) bool { return r.ID == id })
	if err := checkLastAdmin(id, rows, after, ending); err != nil {
		return err
	}
	return tx.DeleteAccount(id)
}
