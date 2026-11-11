package audit

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// Encode checks r and returns its JSON: one line without spaces, with the keys in the order of Record and the keys of
// the detail sorted. The store keeps these bytes, and the hash of the next record is that of them.
func Encode(r Record) ([]byte, error) {
	if err := r.Check(); err != nil {
		return nil, err
	}
	r.At = r.At.UTC()
	data, err := json.Marshal(r, json.Deterministic(true))
	if err != nil {
		return nil, fmt.Errorf("cannot encode the record: %w", err)
	}
	if len(data) > MaxRecord {
		return nil, fmt.Errorf("the record is too large: %d bytes (max %d)", len(data), MaxRecord)
	}
	return data, nil
}

var errInvalidRecord = errors.New("not a record of the audit log")

// Decode reads the record that Encode has written. Unknown and duplicate keys, a value of the wrong type, invalid UTF-8
// and data after the record fail it, and so does a record without a log, a number, a time or an action. The rules for
// the values (Record.Check) apply when a record is written, not when it is read, so that a record that another build
// wrote, with other rules, can still be read. Its error shows nothing of data.
func Decode(data []byte) (Record, error) {
	var r Record
	if err := json.Unmarshal(data, &r, json.RejectUnknownMembers(true)); err != nil {
		return Record{}, errInvalidRecord
	}
	if r.Log != LoginLog && r.Log != ActivityLog || r.N < 1 || r.At.IsZero() || r.Action == "" {
		return Record{}, errInvalidRecord
	}
	r.At = r.At.UTC()
	return r, nil
}

// targetKinds are the kinds of a target besides the settings and a release, which are the kinds of content.RefKind
// that the store keeps documents or accounts of.
var targetKinds = []content.RefKind{
	content.RefEntry, content.RefTerm, content.RefMedia, content.RefAuthor, content.RefMenu, content.RefForm,
	content.RefRedirect, content.RefUser,
}

// Check returns why r cannot be written, naming the field, or nil. It does not look at N, Prev and At, which the store
// sets, and its errors show no value.
func (r Record) Check() error {
	want, known := logOf[r.Action]
	switch {
	case r.Log != LoginLog && r.Log != ActivityLog:
		return errors.New("log: unknown log")
	case !known:
		return errors.New("action: unknown action")
	case want != r.Log:
		return fmt.Errorf("action: %s belongs to the %s log, not to the %s log", r.Action, want, r.Log)
	case r.Actor != "" && r.Actor != System && content.CheckID(content.RefUser, r.Actor) != nil:
		return errors.New(`actor: use a user ID or "system"`)
	}
	if err := checkText("result", r.Result, maxWord, false); err != nil {
		return err
	}
	if err := r.Target.check(); err != nil {
		return err
	}
	switch {
	case r.Account != "" && content.CheckID(content.RefUser, r.Account) != nil:
		return errors.New("account: not a user ID")
	case r.Session != "" && !isHex(r.Session, 64):
		return errors.New("session: use 64 lower case hexadecimal digits")
	}
	if err := r.Client.check(); err != nil {
		return err
	}
	if err := checkText("login", r.Login, MaxLogin, false); err != nil {
		return err
	}
	if err := checkText("operation", r.Operation, MaxOperation, false); err != nil {
		return err
	}
	if err := r.Before.check("before"); err != nil {
		return err
	}
	if err := r.After.check("after"); err != nil {
		return err
	}
	if r.Revision != "" && content.CheckID(content.RefRevision, r.Revision) != nil {
		return errors.New("revision: not a revision ID")
	}
	if err := checkText("status", r.Status, maxWord, false); err != nil {
		return err
	}
	return checkDetail(r.Detail)
}

func (t Target) check() error {
	switch {
	case t == Target{}:
		return nil
	case t.Kind == "":
		return errors.New("target.kind: missing")
	}
	return checkTarget(t.Kind, t.ID, "target.", false)
}

// checkTarget checks the kind and the ID of a target, naming the fields after prefix. For a filter, the kind and the
// ID may be empty, and an ID without a kind may be that of any kind.
func checkTarget(kind, id, prefix string, filter bool) error {
	switch {
	case kind == "" && id == "":
		return nil
	case filter && id == "":
		if kind != KindSettings && kind != KindRelease && !slices.Contains(targetKinds, content.RefKind(kind)) {
			return errors.New(prefix + "kind: unknown kind")
		}
		return nil
	case kind == "":
		if slices.ContainsFunc(targetKinds, func(k content.RefKind) bool { return content.CheckID(k, id) == nil }) ||
			isHex(id, 16) {
			return nil
		}
		return errors.New(prefix + "id: not the ID of any kind")
	}
	switch kind {
	case KindSettings:
		if id != "" {
			return errors.New(prefix + "id: the settings have no ID")
		}
	case KindRelease:
		if !isHex(id, 16) {
			return errors.New(prefix + "id: use 16 lower case hexadecimal digits")
		}
	default:
		if !slices.Contains(targetKinds, content.RefKind(kind)) {
			return errors.New(prefix + "kind: unknown kind")
		}
		if content.CheckID(content.RefKind(kind), id) != nil {
			return fmt.Errorf("%sid: not an ID of the kind %s", prefix, kind)
		}
	}
	return nil
}

func (c Client) check() error {
	if c.Addr != "" {
		if a, err := netip.ParseAddr(c.Addr); err != nil || a.String() != c.Addr {
			return errors.New("client.addr: not an IP address in the form of netip.Addr.String")
		}
	}
	return checkText("client.userAgent", c.UserAgent, MaxUserAgent, false)
}

func (v Version) check(field string) error {
	switch {
	case v.Seq < 0:
		return fmt.Errorf("%s.seq: negative", field)
	case v.Hash != "" && !isHash(v.Hash):
		return fmt.Errorf(`%s.hash: use "sha256:" and 64 lower case hexadecimal digits`, field)
	}
	return nil
}

// checkText checks that s, the value of field, has at most limit bytes, is valid UTF-8 and has no control character,
// but a newline if newlines is true.
func checkText(field, s string, limit int, newlines bool) error {
	switch {
	case len(s) > limit:
		return fmt.Errorf("%s: too long: %d bytes (max %d)", field, len(s), limit)
	case !utf8.ValidString(s):
		return fmt.Errorf("%s: not valid UTF-8", field)
	}
	for _, r := range s {
		if unicode.IsControl(r) && (r != '\n' || !newlines) {
			return fmt.Errorf("%s: has a control character", field)
		}
	}
	return nil
}

func checkDetail(d map[string]string) error {
	if len(d) > MaxDetailKeys {
		return fmt.Errorf("detail: too many keys: %d (max %d)", len(d), MaxDetailKeys)
	}
	for _, k := range slices.Sorted(maps.Keys(d)) {
		if !isDetailKey(k) {
			return fmt.Errorf("detail: invalid key %q: use up to %d ASCII letters and digits, starting with a letter", k,
				maxWord)
		}
		if err := checkText("detail "+k, d[k], MaxDetailValue, true); err != nil {
			return err
		}
	}
	return nil
}

func isDetailKey(k string) bool {
	if k == "" || len(k) > maxWord || !isLetter(k[0]) {
		return false
	}
	for i := 1; i < len(k); i++ {
		if !isLetter(k[i]) && (k[i] < '0' || k[i] > '9') {
			return false
		}
	}
	return true
}

func isLetter(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// isHex reports whether s is n lower case hexadecimal digits.
func isHex(s string, n int) bool {
	return len(s) == n && strings.Trim(s, "0123456789abcdef") == ""
}

// isHash reports whether s is "sha256:" and 64 lower case hexadecimal digits.
func isHash(s string) bool {
	digits, ok := strings.CutPrefix(s, "sha256:")
	return ok && isHex(digits, 64)
}
