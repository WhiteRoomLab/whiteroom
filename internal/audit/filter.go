package audit

import (
	"errors"
	"strings"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// Filter selects records; a field that is zero selects all. A record matches if it matches in every field.
type Filter struct {
	Log      Log
	From, To time.Time // the records from From, up to but not including To
	Account  string    // the records whose actor or account is it, or whose target is the user
	Action   string    // an action, or a prefix that ends with ".", such as "entry."
	Target   Target    // the kind and the ID, either of which may be empty
}

// Check returns why f is not a filter, naming the field, or nil.
func (f Filter) Check() error {
	if f.Log != "" && f.Log != LoginLog && f.Log != ActivityLog {
		return errors.New("filter log: unknown log")
	}
	if f.Account != "" && f.Account != System && content.CheckID(content.RefUser, f.Account) != nil {
		return errors.New(`filter account: use a user ID or "system"`)
	}
	if f.Action != "" && !knownAction(f.Action) {
		return errors.New("filter action: use the name of an action, or a prefix of the names that ends with a dot")
	}
	if f.Target != (Target{}) {
		return checkTarget(f.Target.Kind, f.Target.ID, "filter target.", true)
	}
	return nil
}

// knownAction reports whether a is an action, or a prefix that ends with "." of some.
func knownAction(a string) bool {
	if !strings.HasSuffix(a, ".") {
		_, ok := logOf[Action(a)]
		return ok
	}
	for action := range logOf {
		if strings.HasPrefix(string(action), a) {
			return true
		}
	}
	return false
}

// Match reports whether r is one that f selects.
func (f Filter) Match(r Record) bool {
	switch {
	case f.Log != "" && r.Log != f.Log:
		return false
	case !f.From.IsZero() && r.At.Before(f.From):
		return false
	case !f.To.IsZero() && !r.At.Before(f.To):
		return false
	case f.Account != "" && r.Actor != f.Account && r.Account != f.Account &&
		(r.Target.Kind != string(content.RefUser) || r.Target.ID != f.Account):
		return false
	case f.Action != "" && !f.matchAction(r.Action):
		return false
	case f.Target.Kind != "" && r.Target.Kind != f.Target.Kind:
		return false
	case f.Target.ID != "" && r.Target.ID != f.Target.ID:
		return false
	}
	return true
}

func (f Filter) matchAction(a Action) bool {
	if strings.HasSuffix(f.Action, ".") {
		return strings.HasPrefix(string(a), f.Action)
	}
	return string(a) == f.Action
}
