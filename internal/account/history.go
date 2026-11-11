package account

import (
	"context"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// Client is where a request came from, as the login history keeps it (7.4): the address of the client, which the zero
// Addr leaves out, and the User-Agent header, which is cut to audit.MaxUserAgent bytes.
type Client struct {
	Addr      netip.Addr
	UserAgent string
}

func (c Client) record() audit.Client {
	var addr string
	if c.Addr.IsValid() {
		addr = c.Addr.String()
	}
	oneLine := strings.Map(func(r rune) rune {
		if r == '\n' {
			return utf8.RuneError
		}
		return r
	}, c.UserAgent)
	return audit.Client{Addr: addr, UserAgent: audit.Truncate(oneLine, audit.MaxUserAgent)}
}

// Refusal is why the server refused a login before Login was called, which LoginRefused records.
type Refusal string

const (
	RefusedTooLong     Refusal = "too-long"
	RefusedRateLimited Refusal = "rate-limited"
	RefusedBusy        Refusal = "busy"
)

// The results of a login.failure record besides the Refusals, and of a token.failure record.
const (
	failUnknownLogin  = "unknown-login"
	failPending       = "pending"
	failDisabled      = "disabled"
	failWrongPassword = "wrong-password"
	failTooLong       = "too-long"
	failChanged       = "changed"
	failError         = "error"
	failRefused       = "refused"

	tokenMalformed = "malformed"
	tokenUnknown   = "unknown"
	tokenExpired   = "expired"
	tokenDisabled  = "disabled"
)

// The results of a session.end record.
const (
	endedIdle        = "idle"
	endedLifetime    = "lifetime"
	endedCredentials = "credentials"
	endedInactive    = "inactive"
	endedMissing     = "missing"
	endedUnreadable  = "unreadable"
	endedReplaced    = "replaced"
	endedEvicted     = "evicted"
	endedPassword    = "password"
	endedRole        = "role"
	endedDisabled    = "disabled"
	endedDeleted     = "deleted"
)

// sessionEnd is why a session ends. A session that ran out has the time at which it did.
type sessionEnd struct {
	reason  string
	account string // of the session, if its row decodes
	expired time.Time
}

const (
	// failuresPerMinute is the most failures of people who are not logged in, after their password was compared with a
	// hash, that are recorded one by one in a minute of the Manager's clock: the others are counted.
	failuresPerMinute = 60
	// refusalsPerMinute is the same for what costs no hash: a login that was refused before one was made, and a token that
	// was refused. They come as fast as the network lets them, so they have a limit of their own, and cannot use up the
	// places of the failures that cost a hash.
	refusalsPerMinute = 10
)

// recordTimeout is how long a record that stands alone waits for the store. It is a var so that tests can shorten it.
var recordTimeout = 5 * time.Second

// failureClass is which limit of failureLimit a failure counts against.
type failureClass int

const (
	classCompared failureClass = iota // the password was compared with a hash
	classRefused                      // no hash was made: a login refused before that, or a token
	numClasses
)

// failureLimits is the number of failures of each class that are recorded in a minute.
var failureLimits = [numClasses]int{classCompared: failuresPerMinute, classRefused: refusalsPerMinute}

// failure is the kind of a failure that failureLimit counts.
type failure struct {
	action audit.Action
	result string
}

// key is the key of the count of f in the detail of a login.suppressed record: the action and the result in camel case.
func (f failure) key() string {
	parts := strings.FieldsFunc(string(f.action)+"-"+f.result, func(r rune) bool { return r == '.' || r == '-' })
	for i := 1; i < len(parts); i++ {
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

// suppressed counts the failures that were not recorded, with the minutes in which they came: from the start of the
// first to the end of the last.
type suppressed struct {
	from, to time.Time
	counts   map[failure]int
}

func (s *suppressed) cover(from, to time.Time) {
	if s.counts == nil {
		s.counts = make(map[failure]int)
		s.from, s.to = from, to
		return
	}
	if from.Before(s.from) {
		s.from = from
	}
	if to.After(s.to) {
		s.to = to
	}
}

func (s *suppressed) add(minute time.Time, f failure) {
	s.cover(minute, minute.Add(time.Minute))
	s.counts[f]++
}

func (s *suppressed) merge(o *suppressed) {
	s.cover(o.from, o.to)
	for f, n := range o.counts {
		s.counts[f] += n
	}
}

// record is the login.suppressed record that tells the counts.
func (s *suppressed) record() audit.Record {
	detail := map[string]string{
		"from": s.from.UTC().Format(time.RFC3339), "to": s.to.UTC().Format(time.RFC3339),
	}
	for f, n := range s.counts {
		detail[f.key()] = strconv.Itoa(n)
	}
	return audit.Record{Log: audit.LoginLog, Action: audit.LoginSuppressed, Detail: detail}
}

// failureLimit lets failuresPerMinute failures that cost a hash and refusalsPerMinute that cost none, of people who are
// not logged in, be recorded in a minute, in the whole server, and counts the others, and the ones whose record could not
// be written, until a record of the login history takes the counts. The two classes have a place each minute of their
// own, and one count: the counts of both are in the same login.suppressed record. An Update that writes the login history
// takes the counts once in a minute of the Manager's clock (takeForUpdate), so that the failures that are recorded do not
// each bring a record of the counts; take, which FlushSuppressed calls, takes them in any minute.
type failureLimit struct {
	mu      sync.Mutex
	minute  time.Time
	written [numClasses]int
	// takenMinute is the minute in which an Update last took the counts.
	takenMinute time.Time
	held        suppressed
}

// admit reports whether a failure of the class and the kind f at now may be recorded; if not, it is counted. The place
// that it gives is not given back if the record cannot be written (see count): a store that does not answer keeps no more
// than the places of a minute waiting.
func (l *failureLimit) admit(now time.Time, class failureClass, f failure) bool {
	minute := now.Truncate(time.Minute)
	l.mu.Lock()
	defer l.mu.Unlock()
	if !minute.Equal(l.minute) {
		l.minute, l.written = minute, [numClasses]int{}
	}
	if l.written[class] < failureLimits[class] {
		l.written[class]++
		return true
	}
	l.held.add(minute, f)
	return false
}

// count counts the failure f, which admit let in at now and whose record could not be written, as one that was not
// recorded.
func (l *failureLimit) count(now time.Time, f failure) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.held.add(now.Truncate(time.Minute), f)
}

func (l *failureLimit) pending() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.held.counts) > 0
}

// takeForUpdate returns the counts that are not recorded, or nil if there are none, and forgets them, for an Update that
// writes the login history at now. An Update takes them once in a minute of the Manager's clock: if one has taken them in
// this minute, it returns nil, and the counts wait for the next minute, or for take. minute is the minute that it marked
// as that of the take, for restore; it is the zero time if it took nothing, which marks nothing.
func (l *failureLimit) takeForUpdate(now time.Time) (taken *suppressed, minute time.Time) {
	minute = now.Truncate(time.Minute)
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.held.counts) == 0 || minute.Equal(l.takenMinute) {
		return nil, time.Time{}
	}
	l.takenMinute = minute
	return l.takeHeld(), minute
}

// take returns the counts that are not recorded, or nil if there are none, and forgets them, in any minute. It does not
// mark the minute: an Update takes once in a minute, whether or not take was called in it.
func (l *failureLimit) take() *suppressed {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.takeHeld()
}

func (l *failureLimit) takeHeld() *suppressed {
	if len(l.held.counts) == 0 {
		return nil
	}
	taken := l.held
	l.held = suppressed{}
	return &taken
}

// restore counts again what takeForUpdate or take returned, for an Update that did not commit. minute is what
// takeForUpdate returned: the minute is free again, so that the next Update of it can take the counts, unless an Update of
// another minute has marked it since.
func (l *failureLimit) restore(s *suppressed, minute time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.held.merge(s)
	if !minute.IsZero() && minute.Equal(l.takenMinute) {
		l.takenMinute = time.Time{}
	}
}

// history adds the records of the login history to one Update. The first record of an Update follows the record of the
// failures that were counted and not recorded, if there are any, and no Update has taken them in this minute; the Update
// of FlushSuppressed, which sets flush, takes them in any minute.
type history struct {
	m       *Manager
	tx      *store.Tx
	flush   bool
	started bool
	taken   *suppressed
	marked  time.Time // the minute that taking the counts has marked, for restore
}

func (h *history) begin() error {
	if h.started {
		return nil
	}
	h.started = true
	if h.flush {
		h.taken = h.m.limit.take()
	} else {
		h.taken, h.marked = h.m.limit.takeForUpdate(h.m.now())
	}
	if h.taken != nil {
		return h.tx.Record(h.taken.record())
	}
	return nil
}

func (h *history) add(r audit.Record) error {
	if err := h.begin(); err != nil {
		return err
	}
	return h.tx.Record(r)
}

// endSession removes the session with the key and records why. c is the request that found it ended, if one did.
func (h *history) endSession(key string, e sessionEnd, c Client) error {
	if err := h.tx.DeleteSession(key); err != nil {
		return err
	}
	r := audit.Record{
		Log: audit.LoginLog, Action: audit.SessionEnd, Result: e.reason, Account: e.account, Session: key, Client: c.record(),
	}
	if !e.expired.IsZero() {
		r.Detail = map[string]string{"expiredAt": e.expired.UTC().Format(time.RFC3339Nano)}
	}
	return h.add(r)
}

// sessionRecord is a record of the login history about the session with the key of the account.
func sessionRecord(action audit.Action, key, account string, c Client) audit.Record {
	return audit.Record{Log: audit.LoginLog, Action: action, Account: account, Session: key, Client: c.record()}
}

// logged is Update of the store for an operation that writes records of the login history: if the Update does not
// commit, the counts that it took are counted again, and the minute that it marked is free again.
func (m *Manager) logged(ctx context.Context, f func(tx *store.Tx, h *history) error) error {
	h := &history{m: m}
	err := m.st.Update(ctx, func(tx *store.Tx) error {
		h.tx = tx
		return f(tx, h)
	})
	if err != nil && h.taken != nil {
		m.limit.restore(h.taken, h.marked)
	}
	return err
}

// OnRecordError sets the function that is called when a record that stands alone cannot be written: that of a login
// that failed or was refused, or of a token that was refused. The request is answered as it would have been, and the
// failure is counted, as one that was not recorded, for the next login.suppressed record. operation is the name of the
// action. The error is that of the store, which shows no password and no token. It must be called before the Manager is
// used.
func (m *Manager) OnRecordError(f func(operation string, err error)) {
	m.recordError = f
}

// failed records the failure f of someone who is not logged in, which counts against the limit of its class, in an
// Update of its own that the end of ctx does not stop, if the limit lets it. A failure to record does not change the
// answer to the request. The failure that could not be recorded is counted, in the minute of its place, and the place is
// not given back.
func (m *Manager) failed(ctx context.Context, class failureClass, f failure, r audit.Record) {
	now := m.now()
	if !m.limit.admit(now, class, f) {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()
	if err := m.logged(ctx, func(_ *store.Tx, h *history) error { return h.add(r) }); err != nil {
		m.limit.count(now, f)
		if m.recordError != nil {
			m.recordError(string(f.action), err)
		}
	}
}

// loginFailed records a failed login of the class: the login name as typed, normalized and cut, the account if it is
// known, and the reason.
func (m *Manager) loginFailed(ctx context.Context, class failureClass, c Client, login, accountID, result string) {
	m.failed(ctx, class, failure{audit.LoginFailure, result}, audit.Record{
		Log: audit.LoginLog, Action: audit.LoginFailure, Result: result, Account: accountID, Client: c.record(),
		Login: audit.Truncate(NormalizeLogin(login), audit.MaxLogin),
	})
}

// LoginRefused records a login that the server refused before it called Login: login is the login name as typed. It costs
// no hash, so it counts against the limit of the refusals, apart from that of the failures of Login. A failure to record
// it is reported to the function of OnRecordError.
func (m *Manager) LoginRefused(ctx context.Context, c Client, login string, reason Refusal) {
	result := string(reason)
	switch reason {
	case RefusedTooLong, RefusedRateLimited, RefusedBusy:
	default:
		result = failRefused
	}
	m.loginFailed(ctx, classRefused, c, login, "", result)
}

// tokenFailed records a token that SetPassword refused, against the limit of the refusals.
func (m *Manager) tokenFailed(ctx context.Context, c Client, e invalidToken) {
	m.failed(ctx, classRefused, failure{audit.TokenFailure, e.result}, audit.Record{
		Log: audit.LoginLog, Action: audit.TokenFailure, Result: e.result, Account: e.account, Client: c.record(),
	})
}

// FlushSuppressed records the failures that were counted and not recorded, if there are any, as one login.suppressed
// record, in any minute. An Update that writes the login history takes the counts along, but once in a minute of the
// Manager's clock, and what is counted after that waits for the next minute or for this, so the server calls it every
// minute, and when it stops, to record what is left of a flood, after which nothing else may be written. It does not mark
// the minute: a minute has the record of the Update that takes the counts, and one for each call of this.
func (m *Manager) FlushSuppressed(ctx context.Context) error {
	if !m.limit.pending() {
		return nil
	}
	return m.logged(ctx, func(_ *store.Tx, h *history) error {
		h.flush = true
		return h.begin()
	})
}
