// Package audit is the form of the records of the login history and the activity log (7.4): their types, the actions,
// the JSON that the Content Store keeps, the filter that selects them and the line that names the head of a chain, which
// Export writes outside the store. The store (internal/store) adds, chains, reads and verifies the records.
package audit

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Log is one of the two logs. Each is a chain of its own.
type Log string

const (
	LoginLog    Log = "login"
	ActivityLog Log = "activity"
)

// Action is what a record tells, such as "entry.create". Each belongs to one log.
type Action string

// The actions of the login history.
const (
	Login           Action = "login"
	LoginFailure    Action = "login.failure"
	LoginSuppressed Action = "login.suppressed"
	Logout          Action = "logout"
	SessionStart    Action = "session.start"
	SessionSeen     Action = "session.seen"
	SessionEnd      Action = "session.end"
	TokenFailure    Action = "token.failure"
)

// The actions of the activity log.
const (
	EntryCreate            Action = "entry.create"
	EntrySaveRevision      Action = "entry.saveRevision"
	EntryCreateTranslation Action = "entry.createTranslation"
	EntryDelete            Action = "entry.delete"
	EntryPublish           Action = "entry.publish"
	EntryUnpublish         Action = "entry.unpublish"
	EntryArchive           Action = "entry.archive"
	EntrySchedule          Action = "entry.schedule"
	EntryPublishScheduled  Action = "entry.publishScheduled"

	TermCreate Action = "term.create"
	TermSave   Action = "term.save"
	TermDelete Action = "term.delete"

	AuthorCreate Action = "author.create"
	AuthorSave   Action = "author.save"
	AuthorDelete Action = "author.delete"

	MenuCreate Action = "menu.create"
	MenuSave   Action = "menu.save"
	MenuDelete Action = "menu.delete"

	FormCreate Action = "form.create"
	FormSave   Action = "form.save"
	FormDelete Action = "form.delete"

	RedirectCreate Action = "redirect.create"
	RedirectSave   Action = "redirect.save"
	RedirectDelete Action = "redirect.delete"

	MediaAdd     Action = "media.add"
	MediaReplace Action = "media.replace"
	MediaSave    Action = "media.save"
	MediaDelete  Action = "media.delete"

	SettingsSave Action = "settings.save"
	SiteImport   Action = "site.import"

	AccountCreateFirst    Action = "account.createFirst"
	AccountInvite         Action = "account.invite"
	AccountSetPassword    Action = "account.setPassword"
	AccountReset          Action = "account.reset"
	AccountChangePassword Action = "account.changePassword"
	AccountUpdate         Action = "account.update"
	AccountDisable        Action = "account.disable"
	AccountEnable         Action = "account.enable"
	AccountDelete         Action = "account.delete"

	Build           Action = "build"
	BuildRebuild    Action = "build.rebuild"
	BuildResume     Action = "build.resume"
	ReleaseRollback Action = "release.rollback"

	// AccessDenied is a request that the permissions of its account refused.
	AccessDenied Action = "access.denied"

	StoreCreate  Action = "store.create"
	StoreUpgrade Action = "store.upgrade"
	StoreWrite   Action = "store.write"
	Backup       Action = "backup"
)

// logOf is the log of each action: the only place that tells it.
var logOf = map[Action]Log{
	Login: LoginLog, LoginFailure: LoginLog, LoginSuppressed: LoginLog, Logout: LoginLog, SessionStart: LoginLog,
	SessionSeen: LoginLog, SessionEnd: LoginLog, TokenFailure: LoginLog,

	EntryCreate: ActivityLog, EntrySaveRevision: ActivityLog, EntryCreateTranslation: ActivityLog,
	EntryDelete: ActivityLog, EntryPublish: ActivityLog, EntryUnpublish: ActivityLog, EntryArchive: ActivityLog,
	EntrySchedule: ActivityLog, EntryPublishScheduled: ActivityLog,
	TermCreate: ActivityLog, TermSave: ActivityLog, TermDelete: ActivityLog,
	AuthorCreate: ActivityLog, AuthorSave: ActivityLog, AuthorDelete: ActivityLog,
	MenuCreate: ActivityLog, MenuSave: ActivityLog, MenuDelete: ActivityLog,
	FormCreate: ActivityLog, FormSave: ActivityLog, FormDelete: ActivityLog,
	RedirectCreate: ActivityLog, RedirectSave: ActivityLog, RedirectDelete: ActivityLog,
	MediaAdd: ActivityLog, MediaReplace: ActivityLog, MediaSave: ActivityLog, MediaDelete: ActivityLog,
	SettingsSave: ActivityLog, SiteImport: ActivityLog,
	AccountCreateFirst: ActivityLog, AccountInvite: ActivityLog, AccountSetPassword: ActivityLog,
	AccountReset: ActivityLog, AccountChangePassword: ActivityLog, AccountUpdate: ActivityLog,
	AccountDisable: ActivityLog, AccountEnable: ActivityLog, AccountDelete: ActivityLog,
	Build: ActivityLog, BuildRebuild: ActivityLog, BuildResume: ActivityLog, ReleaseRollback: ActivityLog,
	StoreCreate: ActivityLog, StoreUpgrade: ActivityLog, StoreWrite: ActivityLog, Backup: ActivityLog,
	AccessDenied: ActivityLog,
}

// System is the actor of what the system does, such as the build and the store itself.
const System = "system"

// The kinds of a target besides those of content.RefKind (entry, term, media, author, menu, form, redirect and user).
const (
	KindSettings = "settings"
	KindRelease  = "release"
)

// The limits of a record, in bytes unless they say otherwise.
const (
	MaxRecord      = 1 << 16
	MaxUserAgent   = 512
	MaxLogin       = 64
	MaxOperation   = 256
	MaxDetailKeys  = 64 // keys
	MaxDetailValue = 2048

	maxWord = 64 // of a result and a status, and of a key of the detail
)

// Record is a record of a log, whose JSON has the keys in the order of the fields. The store sets N, Prev and At.
type Record struct {
	Log       Log               `json:"log"`
	N         uint64            `json:"n"`
	Prev      string            `json:"prev,omitempty"`
	At        time.Time         `json:"at"`
	Actor     string            `json:"actor,omitempty"`
	Action    Action            `json:"action"`
	Result    string            `json:"result,omitempty"`
	Target    Target            `json:"target,omitzero"`
	Account   string            `json:"account,omitempty"`
	Session   string            `json:"session,omitempty"`
	Client    Client            `json:"client,omitzero"`
	Login     string            `json:"login,omitempty"`
	Operation string            `json:"operation,omitempty"`
	Changed   bool              `json:"changed,omitzero"`
	Before    Version           `json:"before,omitzero"`
	After     Version           `json:"after,omitzero"`
	Revision  string            `json:"revision,omitempty"`
	Status    string            `json:"status,omitempty"`
	Detail    map[string]string `json:"detail,omitempty"`
}

// Target is what a record is about. The ID of a settings document is empty, and that of a release is 16 lower case
// hexadecimal digits.
type Target struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
}

// Client is where a request came from.
type Client struct {
	Addr      string `json:"addr,omitempty"`
	UserAgent string `json:"userAgent,omitempty"`
}

// Version is a version of a document: its seq and its hash, which a deletion does not have.
type Version struct {
	Seq  int64  `json:"seq"`
	Hash string `json:"hash,omitempty"`
}

// Truncate returns s cut at the boundary of a character to at most n bytes, with U+FFFD for each invalid byte and each
// control character but the newline, which only a value of the detail may have.
func Truncate(s string, n int) string {
	var b strings.Builder
	b.Grow(min(len(s), max(n, 0)))
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 1 || unicode.IsControl(r) && r != '\n' {
			r = utf8.RuneError
		}
		if b.Len()+utf8.RuneLen(r) > n {
			break
		}
		b.WriteRune(r)
		s = s[size:]
	}
	return b.String()
}
