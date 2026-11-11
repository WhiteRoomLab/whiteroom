package account

import (
	"strings"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// maxRecordedValue is the most bytes of a login name, a role or an author ID that a record keeps. A row that another
// build wrote may hold more, and a record that cannot be written would fail the operation.
const maxRecordedValue = 64

// recorded is who a is in a record of the activity log (7.4).
func (a Actor) recorded() string {
	if a.system {
		return audit.System
	}
	return a.id
}

// accountField is an attribute of an account that a record names when an operation changes it. A record keeps the value
// of an attribute that has a value function, before and after, and of the others only that they changed: a name and an
// email address are personal, and a password and a token are secret.
type accountField struct {
	name   string
	differ func(a, b row) bool
	value  func(r row) string
}

// accountFields are in the order of their names, which is the order of the names in the detail "fields".
var accountFields = []accountField{
	{"author", func(a, b row) bool { return a.Author != b.Author }, func(r row) string { return r.Author }},
	{"email", func(a, b row) bool { return a.Email != b.Email }, nil},
	{"login", func(a, b row) bool { return a.Login != b.Login }, func(r row) string { return r.Login }},
	{"name", func(a, b row) bool { return a.Name != b.Name }, nil},
	{"password", func(a, b row) bool { return a.Password != b.Password }, nil},
	{"role", func(a, b row) bool { return a.Role != b.Role }, func(r row) string { return string(r.Role) }},
	{"status", func(a, b row) bool { return a.status() != b.status() }, func(r row) string { return string(r.status()) }},
	{"token", func(a, b row) bool { return a.Token != b.Token }, nil},
}

// setValue adds a value that is not empty to a detail, in a form that a record accepts.
func setValue(detail map[string]string, key, value string) {
	if value != "" {
		detail[key] = audit.Truncate(value, maxRecordedValue)
	}
}

// changeDetail is the detail of the record of an operation that took the row of an account from before to after: the
// names of the attributes that it changed, as "fields", and the values before and after of those whose values a record
// keeps, as "loginBefore" and "loginAfter" and so on. A value that is empty is left out.
func changeDetail(before, after row) map[string]string {
	detail := make(map[string]string)
	var names []string
	for _, f := range accountFields {
		if !f.differ(before, after) {
			continue
		}
		names = append(names, f.name)
		if f.value != nil {
			setValue(detail, f.name+"Before", f.value(before))
			setValue(detail, f.name+"After", f.value(after))
		}
	}
	if len(names) > 0 {
		detail["fields"] = strings.Join(names, ", ")
	}
	return detail
}

// valuesDetail is the detail of the record of an operation that makes or deletes the account of r: the values that a
// record keeps, with the suffix.
func valuesDetail(r row, suffix string) map[string]string {
	detail := make(map[string]string)
	for _, f := range accountFields {
		if f.value != nil {
			setValue(detail, f.name+suffix, f.value(r))
		}
	}
	return detail
}

// createdDetail is the detail of the record of the making of the account of r.
func createdDetail(r row) map[string]string {
	return valuesDetail(r, "After")
}

// deletedDetail is the detail of the record of the deletion of the account of r.
func deletedDetail(r row) map[string]string {
	return valuesDetail(r, "Before")
}

// addToken adds to a detail what a record tells of a token that an operation made: what it is for and when it stops
// working, never the token or its hash.
func addToken(detail map[string]string, t tokenRow) {
	detail["tokenPurpose"] = string(t.Purpose)
	detail["tokenExpiresAt"] = t.ExpiresAt.UTC().Format(time.RFC3339Nano)
}

// recordAccount adds to the activity log the record of the operation action that actor did to the account id. It is the
// last write of an operation, so that the record tells whether the operation wrote anything but records.
func recordAccount(tx *store.Tx, action audit.Action, actor, id string, detail map[string]string) error {
	if len(detail) == 0 {
		detail = nil
	}
	return tx.Record(audit.Record{
		Log: audit.ActivityLog, Actor: actor, Action: action, Target: audit.Target{Kind: string(content.RefUser), ID: id},
		Changed: tx.Changed(), Detail: detail,
	})
}
