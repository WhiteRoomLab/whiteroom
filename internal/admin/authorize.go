package admin

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

const (
	// denialsPerMinute is the most refusals that are recorded one by one in a minute of the clock of the Server, as the
	// refusals of logins that cost no hash are (7.4): the others are counted, and told by the next record.
	denialsPerMinute = 10
	// denialWait is how long the record of a refusal waits for the store.
	denialWait = 5 * time.Second
)

// errSessionChanged is the error of the check of inUpdate that finds the account of the request gone, or with other
// credentials than it had when the request was authenticated: the session is over.
var errSessionChanged = errors.New("the session has changed since the request was authenticated")

// attempt is what a request tried, for the record of a refusal whose error does not say what it refused.
type attempt struct {
	action account.Action
	target audit.Target
}

// deniedError is the error of the check of inUpdate that refused: it wraps account.ErrForbidden, and tells the action and
// the target that the record of the refusal names.
type deniedError struct {
	action account.Action
	named  audit.Target
	err    error
}

func (e *deniedError) Error() string { return e.err.Error() }
func (e *deniedError) Unwrap() error { return e.err }

// allow reports whether the principal may do act to target (account.Check). If not, it has recorded the refusal and
// answered 403, and returns false. named is what the record of a refusal names as its target, or the zero Target. It reads
// nothing of the store: a check that needs the owner of a document is inUpdate.
func (s *Server) allow(w http.ResponseWriter, r *http.Request, p account.Principal, act account.Action, target account.Target, named audit.Target) bool {
	if account.Check(p, act, target) == nil {
		return true
	}
	s.refuse(w, r, p, attempt{action: act, target: named})
	return false
}

// allowView is allow for the reading of documents and definitions.
func (s *Server) allowView(w http.ResponseWriter, r *http.Request, p account.Principal) bool {
	return s.allow(w, r, p, account.ViewAdmin, account.Target{}, audit.Target{})
}

// allowAsOwn is allow for an action that depends on who owns what it is done to, before the body is received and the
// owner is read (inUpdate does that): it asks as if the target were the account's own, so it refuses only a role that
// can do the action to nothing.
func (s *Server) allowAsOwn(w http.ResponseWriter, r *http.Request, p account.Principal, act account.Action, named audit.Target) bool {
	own := account.Target{Owner: p.Account.ID, Author: p.Account.Author, Account: p.Account.ID}
	return s.allow(w, r, p, act, own, named)
}

// refuse records the refusal of tried and answers 403.
func (s *Server) refuse(w http.ResponseWriter, r *http.Request, p account.Principal, tried attempt) {
	s.recordDenied(r.Context(), p, tried)
	writeForbidden(w)
}

// inUpdate returns the check that an operation calls in its Update (edit.Op.Check, and the check of Rebuild, Rollback and
// Resume of internal/publish), before it reads or writes anything else, so that the check and the change are one state. It
// reads the account of the principal again, with the role it has now: the error is errSessionChanged if there is none or
// its credentials are not those of the session, and else, if the account may not do each of acts to the target that target
// returns (nil for the zero account.Target) in this transaction, an error that wraps account.ErrForbidden, which names the
// first act that it may not and, as the target of the record of the refusal, named.
func inUpdate(p account.Principal, named audit.Target, target func(tx *store.Tx) (account.Target, error), acts ...account.Action) func(tx *store.Tx) error {
	return func(tx *store.Tx) error {
		now, err := account.Read(tx, p.Account.ID)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return errSessionChanged
		case err != nil:
			return err
		case !now.CredentialsChangedAt.Equal(p.Account.CredentialsChangedAt):
			return errSessionChanged
		}
		var t account.Target
		if target != nil {
			if t, err = target(tx); err != nil {
				return err
			}
		}
		current := account.Principal{Account: now, Session: p.Session}
		for _, act := range acts {
			if err := account.Check(current, act, t); err != nil {
				return &deniedError{action: act, named: named, err: err}
			}
		}
		return nil
	}
}

// denials counts the refusals that are recorded and those that are not, in the minute of the clock of the Server.
type denials struct {
	mu      sync.Mutex
	minute  time.Time
	written int // the refusals that are recorded in this minute
	held    int // the refusals that were not recorded since the last record
}

// admit reports whether a refusal at now is to be recorded, up to denialsPerMinute in a minute; if so, taken is the number
// of refusals that were not recorded since the last record, which this one tells of. A refusal that is not recorded is held.
func (d *denials) admit(now time.Time) (ok bool, taken int) {
	minute := now.Truncate(time.Minute)
	d.mu.Lock()
	defer d.mu.Unlock()
	if !minute.Equal(d.minute) {
		d.minute, d.written = minute, 0
	}
	if d.written >= denialsPerMinute {
		d.held++
		return false, 0
	}
	d.written++
	taken, d.held = d.held, 0
	return true, taken
}

// failed holds a refusal whose record could not be written, and the refusals that the record was to tell of. The place that
// the refusal took in the minute stays taken: a store that does not answer keeps no more than the places of a minute waiting.
func (d *denials) failed(taken int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.held += taken + 1
}

// recordDenied records the refusal of tried to p in the activity log (7.4), in an Update that only adds the record, and
// that the end of ctx does not stop; it waits for the store for s.denialWait. The record has the actor from the Principal,
// the permission that was refused, the target if it is known, and, if refusals were not recorded since the last one, how many.
// If it cannot be written it is logged, and counted with the others that are not recorded: the answer is the same.
func (s *Server) recordDenied(ctx context.Context, p account.Principal, tried attempt) {
	ok, taken := s.denials.admit(s.now())
	if !ok {
		return
	}
	rec := audit.Record{Log: audit.ActivityLog, Actor: p.Account.ID, Action: audit.AccessDenied, Target: tried.target}
	detail := make(map[string]string, 2)
	if tried.action != "" {
		detail["permission"] = string(tried.action)
	}
	if taken > 0 {
		detail["suppressed"] = strconv.Itoa(taken)
	}
	if len(detail) > 0 {
		rec.Detail = detail
	}
	if rec.Check() != nil {
		// The target comes from the path of the request, which names nothing that the log can name if it is not an ID.
		rec.Target = audit.Target{}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.denialWait)
	defer cancel()
	if err := s.store.Update(ctx, func(tx *store.Tx) error { return tx.Record(rec) }); err != nil {
		s.denials.failed(taken)
		s.log.Printf("cannot record %s: %v", audit.AccessDenied, err)
	}
}
