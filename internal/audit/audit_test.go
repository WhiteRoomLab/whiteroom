package audit

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const (
	userA      = "usr_01JB8T7H4MA2C5E8G1J4K7N3P6"
	userB      = "usr_01JB8T7H4MA2C5E8G1J4K7N3P7"
	entryID    = "ent_01JB8W2N5KD3F6H9J1M4P7R2T5"
	termID     = "trm_01JBA4C7E2G5J8K1M4P7R2T5W8"
	mediaID    = "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5"
	authorID   = "aut_01JBA4C7N9Q3T6W9Z3C6F9H2K5"
	menuID     = "mnu_01JBD2M5N6P7Q8R9S0T1V2W3X4"
	formID     = "frm_01JB8Z3K5VP1D4G7J2M5Q8T3W6"
	redirectID = "rdr_01JBD3A4B5C6D7E8F9G0H1J2K3"
	revisionID = "rev_01JB8X9Y3ZE5G8K1N4Q7S2V6W9"
	operation  = "op_01JB8X9Y3ZE5G8K1N4Q7S2V6W9"
)

var (
	hash64  = "sha256:" + strings.Repeat("ab", 32)
	hash64b = "sha256:" + strings.Repeat("01", 32)
	session = strings.Repeat("0f", 32)
	at      = time.Date(2026, 11, 11, 7, 30, 15, 123_456_789, time.UTC)
)

// full returns a record of the login history with every field set: only the login history may have a client, a login
// name and a session.
func full() Record {
	return Record{
		Log: LoginLog, N: 12, Prev: hash64, At: at, Actor: userA, Action: LoginFailure, Result: "failed",
		Target: Target{Kind: "entry", ID: entryID}, Account: userB, Session: session,
		Client: Client{Addr: "2001:db8::1", UserAgent: "Mozilla/5.0"}, Login: "yamada", Operation: operation,
		Changed: true, Before: Version{Seq: 3, Hash: hash64}, After: Version{Seq: 4, Hash: hash64b},
		Revision: revisionID, Status: "published", Detail: map[string]string{"warnings": "2", "base": revisionID},
	}
}

// activity returns r as a record of the activity log, which has no client, login name and session.
func activity(r Record) Record {
	r.Log, r.Action = ActivityLog, EntrySaveRevision
	r.Client, r.Login, r.Session = Client{}, "", ""
	return r
}

const fullJSON = `{"log":"login","n":12,` +
	`"prev":"sha256:abababababababababababababababababababababababababababababababab",` +
	`"at":"2026-11-11T07:30:15.123456789Z","actor":"usr_01JB8T7H4MA2C5E8G1J4K7N3P6","action":"login.failure",` +
	`"result":"failed","target":{"kind":"entry","id":"ent_01JB8W2N5KD3F6H9J1M4P7R2T5"},` +
	`"account":"usr_01JB8T7H4MA2C5E8G1J4K7N3P7",` +
	`"session":"0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f",` +
	`"client":{"addr":"2001:db8::1","userAgent":"Mozilla/5.0"},"login":"yamada",` +
	`"operation":"op_01JB8X9Y3ZE5G8K1N4Q7S2V6W9","changed":true,` +
	`"before":{"seq":3,"hash":"sha256:abababababababababababababababababababababababababababababababab"},` +
	`"after":{"seq":4,"hash":"sha256:0101010101010101010101010101010101010101010101010101010101010101"},` +
	`"revision":"rev_01JB8X9Y3ZE5G8K1N4Q7S2V6W9","status":"published",` +
	`"detail":{"base":"rev_01JB8X9Y3ZE5G8K1N4Q7S2V6W9","warnings":"2"}}`

func TestEncode(t *testing.T) {
	got, err := Encode(full())
	if err != nil {
		t.Fatalf("Encode() = %v", err)
	}
	if string(got) != fullJSON {
		t.Errorf("Encode() = %s\nwant %s", got, fullJSON)
	}

	// The record that a login failure of the system needs: the keys that are empty or false are left out.
	r := Record{Log: LoginLog, N: 1, At: at.In(time.FixedZone("JST", 9*60*60)), Action: LoginFailure}
	want := `{"log":"login","n":1,"at":"2026-11-11T07:30:15.123456789Z","action":"login.failure"}`
	if got, err := Encode(r); err != nil || string(got) != want {
		t.Errorf("Encode() of a small record = %s, %v; want %s", got, err, want)
	}

	// A whole second has no fraction, and the time is in UTC whatever its zone.
	r.At = time.Date(2026, 11, 11, 16, 30, 15, 0, time.FixedZone("JST", 9*60*60))
	want = `{"log":"login","n":1,"at":"2026-11-11T07:30:15Z","action":"login.failure"}`
	if got, err := Encode(r); err != nil || string(got) != want {
		t.Errorf("Encode() with a time in another zone = %s, %v; want %s", got, err, want)
	}

	// The record is not changed.
	if r := full(); !reflect.DeepEqual(r, full()) {
		t.Errorf("the record changed")
	}
}

func TestEncodeDecode(t *testing.T) {
	for name, r := range map[string]Record{
		"full":          full(),
		"full activity": activity(full()),
		"minimal":       {Log: LoginLog, N: 1, At: at, Action: Login},
		"a deletion": {Log: ActivityLog, N: 2, Prev: hash64, At: at, Action: EntryDelete,
			Target: Target{Kind: "entry", ID: entryID}, Before: Version{Seq: 5, Hash: hash64}, After: Version{Seq: 6}},
		"settings": {Log: ActivityLog, N: 3, At: at, Actor: System, Action: SettingsSave,
			Target: Target{Kind: KindSettings}, Changed: true},
		"a release": {Log: ActivityLog, N: 4, At: at, Actor: System, Action: Build,
			Target: Target{Kind: KindRelease, ID: "0123456789abcdef"}},
		"max n": {Log: LoginLog, N: 1<<64 - 1, At: at, Action: Logout},
	} {
		t.Run(name, func(t *testing.T) {
			data, err := Encode(r)
			if err != nil {
				t.Fatalf("Encode() = %v", err)
			}
			got, err := Decode(data)
			if err != nil {
				t.Fatalf("Decode() = %v", err)
			}
			if !got.At.Equal(r.At) || got.At.Location() != time.UTC {
				t.Errorf("Decode() has the time %v, want %v in UTC", got.At, r.At)
			}
			got.At, r.At = time.Time{}, time.Time{}
			if !reflect.DeepEqual(got, r) {
				t.Errorf("Decode(Encode(r)) = %+v, want %+v", got, r)
			}
		})
	}
}

func TestDecodeRefuses(t *testing.T) {
	const secret = "SECRET-VALUE"
	base := func(extra string) []byte {
		return []byte(`{"log":"login","n":1,"at":"2026-11-11T07:30:15Z","action":"login.failure","login":"` + secret + `"` +
			extra + `}`)
	}
	if _, err := Decode(base("")); err != nil {
		t.Fatalf("Decode() of the base = %v", err)
	}
	invalidUTF8 := append(bytes.Clone(base("")[:len(base(""))-2]), 0xff, '"', '}')
	for name, data := range map[string][]byte{
		"an unknown key":                base(`,"other":1`),
		"an unknown key in a target":    base(`,"target":{"kind":"entry","other":1}`),
		"a duplicate key":               base(`,"login":"x"`),
		"a duplicate key in the detail": base(`,"detail":{"a":"1","a":"2"}`),
		"a key in another case":         base(`,"Login":"x"`),
		"a number for a string":         base(`,"actor":1`),
		"a string for a number":         []byte(`{"log":"login","n":"1","at":"2026-11-11T07:30:15Z","action":"login"}`),
		"a negative number":             []byte(`{"log":"login","n":-1,"at":"2026-11-11T07:30:15Z","action":"login"}`),
		"a string for a boolean":        base(`,"changed":"yes"`),
		"a number in the detail":        base(`,"detail":{"a":1}`),
		"a time that is no time":        []byte(`{"log":"login","n":1,"at":"yesterday","action":"login"}`),
		"invalid UTF-8":                 invalidUTF8,
		"data after the record":         append(base(""), []byte(` {}`)...),
		"a second record":               append(base(""), base("")...),
		"no log":                        []byte(`{"n":1,"at":"2026-11-11T07:30:15Z","action":"login"}`),
		"another log":                   []byte(`{"log":"audit","n":1,"at":"2026-11-11T07:30:15Z","action":"login"}`),
		"no number":                     []byte(`{"log":"login","at":"2026-11-11T07:30:15Z","action":"login"}`),
		"the number 0":                  []byte(`{"log":"login","n":0,"at":"2026-11-11T07:30:15Z","action":"login"}`),
		"no time":                       []byte(`{"log":"login","n":1,"action":"login"}`),
		"no action":                     []byte(`{"log":"login","n":1,"at":"2026-11-11T07:30:15Z"}`),
		"an empty action":               []byte(`{"log":"login","n":1,"at":"2026-11-11T07:30:15Z","action":""}`),
		"not an object":                 []byte(`[]`),
		"null":                          []byte(`null`),
		"empty":                         nil,
		"truncated":                     base("")[:20],
	} {
		t.Run(name, func(t *testing.T) {
			r, err := Decode(data)
			if err == nil {
				t.Fatalf("Decode(%q) = %+v, want an error", data, r)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "other") {
				t.Errorf("the error %q shows the data", err)
			}
		})
	}

	// The values are for Check, which applies when a record is written: a record that has a value which Check refuses,
	// such as one of another build, is read.
	for _, extra := range []string{`,"actor":"somebody"`, `,"login":"` + strings.Repeat("x", 100) + `"`,
		`,"target":{"kind":"unknown"}`, `,"session":"x"`, `,"detail":{"1":"v"}`} {
		data := []byte(`{"log":"login","n":1,"at":"2026-11-11T07:30:15Z","action":"login.failure"` + extra + `}`)
		if _, err := Decode(data); err != nil {
			t.Errorf("Decode() with %s = %v, want a record", extra, err)
		}
	}
	other := []byte(`{"log":"activity","n":7,"at":"2026-11-11T07:30:15Z","action":"entry.teleport","actor":"somebody"}`)
	if r, err := Decode(other); err != nil || r.Action != "entry.teleport" || r.N != 7 {
		t.Errorf("Decode() of a record with an unknown action = %+v, %v; want the record", r, err)
	}
	// So is a record of the activity log that has a client, a login name and a session, which Check refuses.
	withClient := []byte(`{"log":"activity","n":8,"at":"2026-11-11T07:30:15Z","action":"entry.create","session":"` + session +
		`","client":{"addr":"192.0.2.1"},"login":"yamada"}`)
	if r, err := Decode(withClient); err != nil || r.Client.Addr != "192.0.2.1" || r.Login != "yamada" || r.Session != session {
		t.Errorf("Decode() of a record of the activity log with a client = %+v, %v; want the record", r, err)
	}
}

func TestCheck(t *testing.T) {
	if err := full().Check(); err != nil {
		t.Fatalf("Check() of the full record of the login history = %v", err)
	}
	if err := activity(full()).Check(); err != nil {
		t.Fatalf("Check() of the full record of the activity log = %v", err)
	}
	// N, Prev and At are for the store.
	if err := (Record{Log: LoginLog, Action: Login}).Check(); err != nil {
		t.Errorf("Check() of a record without n, prev and at = %v, want nil", err)
	}
	if err := (Record{Log: LoginLog, N: 5, Prev: "x", At: at, Action: Login}).Check(); err != nil {
		t.Errorf("Check() of a record with n, prev and at that are not right = %v, want nil: they are not looked at", err)
	}

	const secret = "SECRET-VALUE"
	long := func(n int) string { return strings.Repeat("a", n) }
	bad := string([]byte{'a', 0xff, 'b'})
	for _, tt := range []struct {
		name   string
		change func(r *Record)
		want   string
	}{
		{"no log", func(r *Record) { r.Log = "" }, "log:"},
		{"an unknown log", func(r *Record) { r.Log = "audit" }, "log:"},
		{"an unknown action", func(r *Record) { r.Action = "entry.teleport" }, "action:"},
		{"no action", func(r *Record) { r.Action = "" }, "action:"},
		{"an action of the other log", func(r *Record) { r.Action = Build }, "action: build belongs to the activity log"},
		{"a login action in the activity log", func(r *Record) { r.Log, r.Action = ActivityLog, SessionEnd }, "action:"},
		{"an address in the activity log", func(r *Record) { *r = activity(*r); r.Client.Addr = "192.0.2.1" },
			"client: the activity log has none"},
		{"a user agent in the activity log", func(r *Record) { *r = activity(*r); r.Client.UserAgent = secret },
			"client: the activity log has none"},
		{"a login name in the activity log", func(r *Record) { *r = activity(*r); r.Login = secret },
			"login: the activity log has none"},
		{"a session in the activity log", func(r *Record) { *r = activity(*r); r.Session = session },
			"session: the activity log has none"},
		{"every field of the login history in the activity log", func(r *Record) { r.Log, r.Action = ActivityLog, Build },
			"client: the activity log has none"},
		{"an actor that is no user", func(r *Record) { r.Actor = secret }, "actor:"},
		{"an actor of another kind", func(r *Record) { r.Actor = entryID }, "actor:"},
		{"an actor in lower case", func(r *Record) { r.Actor = strings.ToLower(userA) }, "actor:"},
		{"a long result", func(r *Record) { r.Result = long(65) }, "result: too long: 65 bytes (max 64)"},
		{"a result with a newline", func(r *Record) { r.Result = "a\nb" }, "result: has a control character"},
		{"a result that is not UTF-8", func(r *Record) { r.Result = bad }, "result: not valid UTF-8"},
		{"a target without a kind", func(r *Record) { r.Target = Target{ID: entryID} }, "target.kind: missing"},
		{"a target of an unknown kind", func(r *Record) { r.Target = Target{Kind: "page", ID: entryID} }, "target.kind:"},
		{"a target of the kind revision", func(r *Record) { r.Target = Target{Kind: "revision", ID: revisionID} },
			"target.kind:"},
		{"a target with the ID of another kind", func(r *Record) { r.Target = Target{Kind: "entry", ID: termID} },
			"target.id: not an ID of the kind entry"},
		{"a target without an ID", func(r *Record) { r.Target = Target{Kind: "entry"} }, "target.id:"},
		{"the settings with an ID", func(r *Record) { r.Target = Target{Kind: KindSettings, ID: entryID} }, "target.id:"},
		{"a release without an ID", func(r *Record) { r.Target = Target{Kind: KindRelease} }, "target.id:"},
		{"a release with 15 digits", func(r *Record) { r.Target = Target{Kind: KindRelease, ID: "0123456789abcde"} },
			"target.id:"},
		{"a release in upper case", func(r *Record) { r.Target = Target{Kind: KindRelease, ID: "0123456789ABCDEF"} },
			"target.id:"},
		{"an account that is no user", func(r *Record) { r.Account = "system" }, "account:"},
		{"a session of 63 digits", func(r *Record) { r.Session = session[1:] }, "session:"},
		{"a session in upper case", func(r *Record) { r.Session = strings.ToUpper(session) }, "session:"},
		{"a session that is a token", func(r *Record) { r.Session = secret }, "session:"},
		{"an address that is no address", func(r *Record) { r.Client.Addr = "example.com" }, "client.addr:"},
		{"an address that is not in its form", func(r *Record) { r.Client.Addr = "2001:DB8::1" }, "client.addr:"},
		{"an address with a port", func(r *Record) { r.Client.Addr = "192.0.2.1:80" }, "client.addr:"},
		{"a long user agent", func(r *Record) { r.Client.UserAgent = long(513) },
			"client.userAgent: too long: 513 bytes (max 512)"},
		{"a user agent with a tab", func(r *Record) { r.Client.UserAgent = "a\tb" }, "client.userAgent: has a control"},
		{"a long login", func(r *Record) { r.Login = long(65) }, "login: too long: 65 bytes (max 64)"},
		{"a login that is not UTF-8", func(r *Record) { r.Login = bad }, "login: not valid UTF-8"},
		{"a login with a newline", func(r *Record) { r.Login = "a\nb" }, "login: has a control character"},
		{"a login with a DEL", func(r *Record) { r.Login = "a\x7fb" }, "login: has a control character"},
		{"a long operation", func(r *Record) { r.Operation = long(257) }, "operation: too long: 257 bytes (max 256)"},
		{"a negative seq", func(r *Record) { r.Before.Seq = -1 }, "before.seq: negative"},
		{"a hash in upper case", func(r *Record) { r.After.Hash = strings.ToUpper(hash64) }, "after.hash:"},
		{"a hash without its algorithm", func(r *Record) { r.After.Hash = hash64[7:] }, "after.hash:"},
		{"a short hash", func(r *Record) { r.Before.Hash = hash64[:70] }, "before.hash:"},
		{"a revision that is an entry", func(r *Record) { r.Revision = entryID }, "revision:"},
		{"a long status", func(r *Record) { r.Status = long(65) }, "status: too long"},
		{"a status with a control character", func(r *Record) { r.Status = "a\x00" }, "status: has a control"},
		{"65 keys in the detail", func(r *Record) {
			r.Detail = map[string]string{}
			for i := range 65 {
				r.Detail[fmt.Sprintf("k%d", i)] = "v"
			}
		}, "detail: too many keys: 65 (max 64)"},
		{"a key that starts with a digit", func(r *Record) { r.Detail = map[string]string{"1a": "v"} },
			"detail: invalid key"},
		{"an empty key", func(r *Record) { r.Detail = map[string]string{"": "v"} }, "detail: invalid key"},
		{"a key with a dash", func(r *Record) { r.Detail = map[string]string{"a-b": "v"} }, "detail: invalid key"},
		{"a key that is not ASCII", func(r *Record) { r.Detail = map[string]string{"a" + string(rune(0xe9)): "v"} },
			"detail: invalid key"},
		{"a long key", func(r *Record) { r.Detail = map[string]string{long(65): "v"} }, "detail: invalid key"},
		{"a long value in the detail", func(r *Record) { r.Detail = map[string]string{"a": long(2049)} },
			"detail a: too long: 2049 bytes (max 2048)"},
		{"a value in the detail that is not UTF-8", func(r *Record) { r.Detail = map[string]string{"a": bad} },
			"detail a: not valid UTF-8"},
		{"a value in the detail with a NUL", func(r *Record) { r.Detail = map[string]string{"a": "a\x00b"} },
			"detail a: has a control character"},
		{"a value in the detail with a tab", func(r *Record) { r.Detail = map[string]string{"a": "a\tb"} },
			"detail a: has a control character"},
		{"a value in the detail with a carriage return", func(r *Record) { r.Detail = map[string]string{"a": "a\rb"} },
			"detail a: has a control character"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := full()
			tt.change(&r)
			err := r.Check()
			if err == nil || !strings.HasPrefix(err.Error(), tt.want) {
				t.Fatalf("Check() = %v, want an error that starts with %q", err, tt.want)
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("the error %q shows the value", err)
			}
			if _, err := Encode(r); err == nil {
				t.Errorf("Encode() takes the record that Check() refuses")
			}
		})
	}

	// What Check takes. The changes are of the record of the login history with every field, but those of onActivity, which
	// make a record of the activity log.
	onActivity := func(a Action, kind, id string) func(r *Record) {
		return func(r *Record) {
			*r = activity(*r)
			r.Action, r.Target = a, Target{Kind: kind, ID: id}
		}
	}
	for name, change := range map[string]func(r *Record){
		"a login of 64 bytes":     func(r *Record) { r.Login = strings.Repeat("a", 64) },
		"a user agent of 512":     func(r *Record) { r.Client.UserAgent = strings.Repeat("a", 512) },
		"an operation of 256":     func(r *Record) { r.Operation = strings.Repeat("a", 256) },
		"64 keys in the detail":   func(r *Record) { r.Detail = manyKeys(64) },
		"a value of 2048":         func(r *Record) { r.Detail = map[string]string{"a": strings.Repeat("a", 2048)} },
		"a value with newlines":   func(r *Record) { r.Detail = map[string]string{"a": "line 1\nline 2\n"} },
		"a value with Japanese":   func(r *Record) { r.Detail = map[string]string{"a": "日本語"}; r.Login = "日本語" },
		"a key of 64 bytes":       func(r *Record) { r.Detail = map[string]string{"a" + strings.Repeat("b", 63): "v"} },
		"an empty value":          func(r *Record) { r.Detail = map[string]string{"a": ""} },
		"the system as the actor": func(r *Record) { r.Actor = System },
		"no actor":                func(r *Record) { r.Actor = "" },
		"an IPv4 address":         func(r *Record) { r.Client.Addr = "192.0.2.1" },
		"a mapped address":        func(r *Record) { r.Client.Addr = "::ffff:192.0.2.1" },
		"a version of seq 0":      func(r *Record) { r.Before = Version{} },
		"a deletion":              func(r *Record) { r.After = Version{Seq: 9} },
		"no target":               func(r *Record) { r.Target = Target{} },
		"an activity record":      func(r *Record) { *r = activity(*r) },
		"a user target":           onActivity(AccountUpdate, "user", userB),
		"a term target":           onActivity(TermSave, "term", termID),
		"a media target":          onActivity(MediaAdd, "media", mediaID),
		"an author target":        onActivity(AuthorSave, "author", authorID),
		"a menu target":           onActivity(MenuSave, "menu", menuID),
		"a form target":           onActivity(FormSave, "form", formID),
		"a redirect target":       onActivity(RedirectSave, "redirect", redirectID),
	} {
		t.Run(name, func(t *testing.T) {
			r := full()
			change(&r)
			if err := r.Check(); err != nil {
				t.Fatalf("Check() = %v, want nil", err)
			}
			data, err := Encode(r)
			if err != nil {
				t.Fatalf("Encode() = %v", err)
			}
			if _, err := Decode(data); err != nil {
				t.Errorf("Decode(Encode(r)) = %v", err)
			}
		})
	}
}

func manyKeys(n int) map[string]string {
	d := map[string]string{}
	for i := range n {
		d[fmt.Sprintf("k%d", i)] = "v"
	}
	return d
}

func TestEncodeTooLarge(t *testing.T) {
	// 32 values of 2048 bytes are 64 KiB with the keys and the rest.
	r := full()
	r.Detail = map[string]string{}
	for i := range 32 {
		r.Detail[fmt.Sprintf("k%d", i)] = strings.Repeat("a", MaxDetailValue)
	}
	if err := r.Check(); err != nil {
		t.Fatalf("Check() = %v: each value is within its limit", err)
	}
	data, err := Encode(r)
	if err == nil || !strings.HasPrefix(err.Error(), "the record is too large: ") || data != nil {
		t.Errorf("Encode() of a record of more than %d bytes = %d bytes, %v; want an error", MaxRecord, len(data), err)
	}
	// Under the limit.
	r.Detail = manyKeys(10)
	for k := range r.Detail {
		r.Detail[k] = strings.Repeat("a", MaxDetailValue)
	}
	if data, err := Encode(r); err != nil || len(data) > MaxRecord {
		t.Errorf("Encode() of a record under the limit = %d bytes, %v", len(data), err)
	}
}

func TestEncodeJSONEscapes(t *testing.T) {
	// The detail may hold a newline, a quote, a backslash and characters that JSON of other packages escapes.
	r := Record{Log: ActivityLog, N: 1, At: at, Action: Build, Detail: map[string]string{"error": "a \"b\" \\ c\n<d>&e"}}
	data, err := Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	want := `"detail":{"error":"a \"b\" \\ c\n<d>&e"}}`
	if !strings.HasSuffix(string(data), want) {
		t.Errorf("Encode() = %s, want it to end with %s", data, want)
	}
	if bytes.ContainsAny(data, "\n\r") {
		t.Errorf("Encode() = %q has a line break: a record is one line", data)
	}
	got, err := Decode(data)
	if err != nil || got.Detail["error"] != r.Detail["error"] {
		t.Errorf("Decode() = %+v, %v; want the same detail", got.Detail, err)
	}
}

func TestActions(t *testing.T) {
	login := []Action{Login, LoginFailure, LoginSuppressed, Logout, SessionStart, SessionSeen, SessionEnd, TokenFailure}
	activity := []Action{
		EntryCreate, EntrySaveRevision, EntryCreateTranslation, EntryDelete, EntryPublish, EntryUnpublish, EntryArchive,
		EntrySchedule, EntryPublishScheduled,
		TermCreate, TermSave, TermDelete, AuthorCreate, AuthorSave, AuthorDelete, MenuCreate, MenuSave, MenuDelete,
		FormCreate, FormSave, FormDelete, RedirectCreate, RedirectSave, RedirectDelete,
		MediaAdd, MediaReplace, MediaSave, MediaDelete, SettingsSave, SiteImport,
		AccountCreateFirst, AccountInvite, AccountSetPassword, AccountReset, AccountChangePassword, AccountUpdate,
		AccountDisable, AccountEnable, AccountDelete,
		Build, BuildRebuild, BuildResume, ReleaseRollback, AccessDenied, StoreCreate, StoreUpgrade, StoreWrite, Backup,
	}
	names := map[Action]string{
		Login: "login", LoginFailure: "login.failure", LoginSuppressed: "login.suppressed", Logout: "logout",
		SessionStart: "session.start", SessionSeen: "session.seen", SessionEnd: "session.end",
		TokenFailure: "token.failure",
		EntryCreate:  "entry.create", EntrySaveRevision: "entry.saveRevision",
		EntryCreateTranslation: "entry.createTranslation", EntryDelete: "entry.delete", EntryPublish: "entry.publish",
		EntryUnpublish: "entry.unpublish", EntryArchive: "entry.archive", EntrySchedule: "entry.schedule",
		EntryPublishScheduled: "entry.publishScheduled",
		TermCreate:            "term.create", TermSave: "term.save", TermDelete: "term.delete",
		AuthorCreate: "author.create", AuthorSave: "author.save", AuthorDelete: "author.delete",
		MenuCreate: "menu.create", MenuSave: "menu.save", MenuDelete: "menu.delete",
		FormCreate: "form.create", FormSave: "form.save", FormDelete: "form.delete",
		RedirectCreate: "redirect.create", RedirectSave: "redirect.save", RedirectDelete: "redirect.delete",
		MediaAdd: "media.add", MediaReplace: "media.replace", MediaSave: "media.save", MediaDelete: "media.delete",
		SettingsSave: "settings.save", SiteImport: "site.import",
		AccountCreateFirst: "account.createFirst", AccountInvite: "account.invite",
		AccountSetPassword: "account.setPassword", AccountReset: "account.reset",
		AccountChangePassword: "account.changePassword", AccountUpdate: "account.update",
		AccountDisable: "account.disable", AccountEnable: "account.enable", AccountDelete: "account.delete",
		Build: "build", BuildRebuild: "build.rebuild", BuildResume: "build.resume", ReleaseRollback: "release.rollback",
		StoreCreate: "store.create", StoreUpgrade: "store.upgrade", StoreWrite: "store.write", Backup: "backup",
		AccessDenied: "access.denied",
	}
	if len(logOf) != len(login)+len(activity) || len(names) != len(logOf) {
		t.Errorf("the table of actions has %d, the lists %d and %d, the names %d", len(logOf), len(login), len(activity),
			len(names))
	}
	for log, actions := range map[Log][]Action{LoginLog: login, ActivityLog: activity} {
		for _, a := range actions {
			if logOf[a] != log {
				t.Errorf("the log of %s is %q, want %q", a, logOf[a], log)
			}
			if string(a) != names[a] {
				t.Errorf("the action %s has the name %q", a, names[a])
			}
			// The record of every action can be written to its log, and to no other.
			r := Record{Log: log, Action: a}
			if err := r.Check(); err != nil {
				t.Errorf("Check() of a record of %s = %v", a, err)
			}
			other := LoginLog
			if log == LoginLog {
				other = ActivityLog
			}
			if err := (Record{Log: other, Action: a}).Check(); err == nil {
				t.Errorf("Check() takes %s in the %s log", a, other)
			}
		}
	}
	seen := map[string]bool{}
	for a := range logOf {
		if seen[string(a)] {
			t.Errorf("the action %s twice", a)
		}
		seen[string(a)] = true
	}
}

// TestAccessDeniedRecord checks the records of requests that were refused, as the admin API writes them: the account is the
// actor, the target is what the request was about, and the detail has the permission and the number of records left out.
func TestAccessDeniedRecord(t *testing.T) {
	for name, r := range map[string]Record{
		"an entry": {Actor: userA, Target: Target{Kind: "entry", ID: entryID}, Detail: map[string]string{"permission": "entry.edit"}},
		"a medium": {Actor: userA, Target: Target{Kind: "media", ID: mediaID}, Detail: map[string]string{"permission": "media.delete"}},
		"the settings": {
			Actor: userA, Target: Target{Kind: KindSettings}, Detail: map[string]string{"permission": "settings.edit"},
		},
		"a release": {
			Actor: userA, Target: Target{Kind: KindRelease, ID: "0123456789abcdef"},
			Detail: map[string]string{"permission": "builds.manage"},
		},
		"an account": {
			Actor: userA, Target: Target{Kind: "user", ID: userB}, Detail: map[string]string{"permission": "accounts.manage"},
		},
		"no target, and left out records": {
			Actor: userA, Detail: map[string]string{"permission": "logs.view", "suppressed": "12"},
		},
		"no actor": {Detail: map[string]string{"permission": "view"}},
	} {
		t.Run(name, func(t *testing.T) {
			r.Log, r.Action, r.N, r.At = ActivityLog, AccessDenied, 1, at
			if err := r.Check(); err != nil {
				t.Fatalf("Check() = %v", err)
			}
			data, err := Encode(r)
			if err != nil {
				t.Fatalf("Encode() = %v", err)
			}
			got, err := Decode(data)
			if err != nil || !reflect.DeepEqual(got, r) {
				t.Errorf("Decode(Encode(r)) = %+v, %v; want %+v", got, err, r)
			}
		})
	}
	// Like the others of the activity log, it tells nothing of where the request came from.
	for name, r := range map[string]Record{
		"a client":  {Client: Client{Addr: "192.0.2.1"}},
		"a login":   {Login: "yamada"},
		"a session": {Session: session},
	} {
		r.Log, r.Action = ActivityLog, AccessDenied
		if err := r.Check(); err == nil {
			t.Errorf("Check() takes %s in a record of access.denied", name)
		}
	}
}

func TestTruncate(t *testing.T) {
	bad := string([]byte{0xff})
	for _, tt := range []struct {
		name string
		s    string
		n    int
		want string
	}{
		{"short", "abc", 10, "abc"},
		{"exact", "abc", 3, "abc"},
		{"cut", "abcdef", 3, "abc"},
		{"empty", "", 3, ""},
		{"zero", "abc", 0, ""},
		{"negative", "abc", -1, ""},
		{"at a character", "あいう", 6, "あい"},
		{"before a character", "あいう", 5, "あ"},
		{"in a character", "あいう", 2, ""},
		{"a 4 byte character", "a\U0001F600b", 4, "a"},
		{"a 4 byte character fits", "a\U0001F600b", 5, "a\U0001F600"},
		{"an invalid byte", "a" + bad + "b", 10, "a\ufffdb"},
		{"an invalid byte that does not fit", "a" + bad + "b", 3, "a"},
		{"two invalid bytes", bad + bad, 10, "\ufffd\ufffd"},
		{"a truncated character", "a\xe3\x81", 10, "a\ufffd\ufffd"},
		{"a tab", "a\tb", 10, "a\ufffdb"},
		{"a NUL", "a\x00b", 10, "a\ufffdb"},
		{"a carriage return", "a\rb", 10, "a\ufffdb"},
		{"a C1 control", "a\u0085b", 10, "a\ufffdb"},
		{"a newline", "a\nb", 10, "a\nb"},
		{"a replacement character", "a\ufffdb", 10, "a\ufffdb"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := Truncate(tt.s, tt.n)
			if got != tt.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tt.s, tt.n, got, tt.want)
			}
			if !utf8.ValidString(got) || len(got) > max(tt.n, 0) {
				t.Errorf("Truncate(%q, %d) = %q is not valid UTF-8 of at most %d bytes", tt.s, tt.n, got, tt.n)
			}
		})
	}

	// What it returns passes Check in the field that it was made for.
	ua := Truncate("Agent\t"+strings.Repeat("あ", 300)+bad, MaxUserAgent)
	r := Record{Log: LoginLog, Action: Login, Client: Client{UserAgent: ua}}
	if err := r.Check(); err != nil || len(ua) > MaxUserAgent {
		t.Errorf("Check() of a truncated user agent of %d bytes = %v", len(ua), err)
	}
}

func TestFilterCheck(t *testing.T) {
	for _, f := range []Filter{
		{},
		{Log: LoginLog},
		{Log: ActivityLog},
		{Account: userA},
		{Account: System},
		{Action: "entry.create"},
		{Action: "entry."},
		{Action: "login"},
		{Action: "login."},
		{Action: "session."},
		{Action: "login.failure"},
		{Target: Target{Kind: "entry"}},
		{Target: Target{ID: entryID}},
		{Target: Target{ID: "0123456789abcdef"}},
		{Target: Target{Kind: "entry", ID: entryID}},
		{Target: Target{Kind: KindSettings}},
		{Target: Target{Kind: KindRelease, ID: "0123456789abcdef"}},
		{Target: Target{Kind: "user", ID: userB}},
		{From: at, To: at.Add(time.Hour)},
		{From: at.Add(time.Hour), To: at}, // an empty range selects nothing
		{Log: ActivityLog, Account: userA, Action: "entry.", Target: Target{Kind: "entry", ID: entryID}, From: at},
	} {
		if err := f.Check(); err != nil {
			t.Errorf("Check() of %+v = %v, want nil", f, err)
		}
	}
	for _, tt := range []struct {
		f    Filter
		want string
	}{
		{Filter{Log: "audit"}, "filter log:"},
		{Filter{Account: "alice"}, "filter account:"},
		{Filter{Account: entryID}, "filter account:"},
		{Filter{Action: "entry"}, "filter action:"},
		{Filter{Action: "entry.teleport"}, "filter action:"},
		{Filter{Action: "entr"}, "filter action:"},
		{Filter{Action: "ent."}, "filter action:"},
		{Filter{Action: "."}, "filter action:"},
		{Filter{Action: "entry.create."}, "filter action:"},
		{Filter{Action: "Entry."}, "filter action:"},
		{Filter{Target: Target{Kind: "page"}}, "filter target.kind:"},
		{Filter{Target: Target{ID: "x"}}, "filter target.id:"},
		{Filter{Target: Target{Kind: "entry", ID: termID}}, "filter target.id:"},
		{Filter{Target: Target{Kind: KindSettings, ID: "x"}}, "filter target.id:"},
		{Filter{Target: Target{Kind: KindRelease, ID: "x"}}, "filter target.id:"},
	} {
		if err := tt.f.Check(); err == nil || !strings.HasPrefix(err.Error(), tt.want) {
			t.Errorf("Check() of %+v = %v, want an error that starts with %q", tt.f, err, tt.want)
		}
	}
}

func TestFilterMatch(t *testing.T) {
	r := Record{
		Log: ActivityLog, N: 5, At: at, Actor: userA, Action: EntrySaveRevision, Account: userB,
		Target: Target{Kind: "entry", ID: entryID},
	}
	userTarget := Record{Log: ActivityLog, N: 6, At: at, Actor: System, Action: AccountUpdate,
		Target: Target{Kind: "user", ID: userB}}
	entryOfUser := Record{Log: ActivityLog, N: 7, At: at, Actor: System, Action: EntryDelete,
		Target: Target{Kind: "entry", ID: userB}} // not a user target: the kind is entry
	login := Record{Log: LoginLog, N: 1, At: at, Action: LoginFailure, Login: "x"}
	for _, tt := range []struct {
		name string
		f    Filter
		r    Record
		want bool
	}{
		{"all", Filter{}, r, true},
		{"all of the login history", Filter{}, login, true},
		{"the log", Filter{Log: ActivityLog}, r, true},
		{"another log", Filter{Log: LoginLog}, r, false},
		{"the login history", Filter{Log: LoginLog}, login, true},
		{"from the time", Filter{From: at}, r, true},
		{"from after the time", Filter{From: at.Add(time.Nanosecond)}, r, false},
		{"to the time", Filter{To: at}, r, false},
		{"to after the time", Filter{To: at.Add(time.Nanosecond)}, r, true},
		{"in the range", Filter{From: at.Add(-time.Hour), To: at.Add(time.Hour)}, r, true},
		{"before the range", Filter{From: at.Add(time.Hour), To: at.Add(2 * time.Hour)}, r, false},
		{"an empty range", Filter{From: at, To: at}, r, false},
		{"the range in another zone", Filter{From: at.In(time.FixedZone("JST", 9*3600))}, r, true},
		{"the actor", Filter{Account: userA}, r, true},
		{"the account", Filter{Account: userB}, r, true},
		{"another account", Filter{Account: "usr_01JB8T7H4MA2C5E8G1J4K7N3P8"}, r, false},
		{"the target user", Filter{Account: userB}, userTarget, true},
		{"the system", Filter{Account: System}, userTarget, true},
		{"the entry that has the ID of the user", Filter{Account: userB}, entryOfUser, false},
		{"the action", Filter{Action: "entry.saveRevision"}, r, true},
		{"another action", Filter{Action: "entry.create"}, r, false},
		{"the prefix", Filter{Action: "entry."}, r, true},
		{"another prefix", Filter{Action: "term."}, r, false},
		{"a prefix that is not at a dot", Filter{Action: "entry.save"}, r, false},
		{"a prefix of the login history", Filter{Action: "login."}, login, true},
		{"the name that is also a prefix", Filter{Action: "login"}, Record{Action: Login}, true},
		{"the name that is also a prefix of others", Filter{Action: "login"}, Record{Action: LoginFailure}, false},
		{"the kind", Filter{Target: Target{Kind: "entry"}}, r, true},
		{"another kind", Filter{Target: Target{Kind: "term"}}, r, false},
		{"the ID", Filter{Target: Target{ID: entryID}}, r, true},
		{"another ID", Filter{Target: Target{ID: termID}}, r, false},
		{"the kind and the ID", Filter{Target: Target{Kind: "entry", ID: entryID}}, r, true},
		{"the ID of another kind", Filter{Target: Target{Kind: "term", ID: entryID}}, r, false},
		{"a target in a record without one", Filter{Target: Target{Kind: "entry"}}, login, false},
		{"every field", Filter{Log: ActivityLog, Account: userA, Action: "entry.", From: at,
			Target: Target{Kind: "entry", ID: entryID}}, r, true},
		{"every field but one", Filter{Log: ActivityLog, Account: userA, Action: "entry.", From: at,
			Target: Target{Kind: "entry", ID: termID}}, r, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.f.Match(tt.r); got != tt.want {
				t.Errorf("Match() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHead(t *testing.T) {
	sum := sha256.Sum256([]byte("record"))
	h := Head{Log: ActivityLog, N: 42, Hash: sum}
	want := fmt.Sprintf("audit head activity 42 sha256:%x", sum)
	if got := h.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	for _, line := range []string{
		want,
		want + "\n",
		want + " \r\n",
		"Nov 11 16:00:00 host whiteroom[123]: " + want,
		"2026-11-11T07:30:15Z INFO " + want,
		"<30>1 2026-11-11T07:30:15Z host whiteroom 123 - - " + want,
		"audit head login 1 sha256:" + strings.Repeat("0", 64) + "  ",
	} {
		got, err := ParseHead(line)
		if err != nil {
			t.Errorf("ParseHead(%q) = %v", line, err)
			continue
		}
		if strings.Contains(line, "login") {
			if got != (Head{Log: LoginLog, N: 1}) {
				t.Errorf("ParseHead(%q) = %+v", line, got)
			}
		} else if got != h {
			t.Errorf("ParseHead(%q) = %+v, want %+v", line, got, h)
		}
	}
	last := Head{Log: LoginLog, N: 1<<64 - 1, Hash: sum}
	if got, err := ParseHead(last.String()); err != nil || got != last {
		t.Errorf("ParseHead(%q) = %+v, %v; want %+v", last.String(), got, err, last)
	}

	digits := strings.Repeat("a", 64)
	for _, line := range []string{
		"",
		"activity 42 sha256:" + digits,
		"audit head",
		"audit head ",
		"audit head activity 42",
		"audit head activity sha256:" + digits,
		"audit head activity 42 sha256:" + digits + " extra",
		"audit head audit 42 sha256:" + digits,
		"audit head Activity 42 sha256:" + digits,
		"audit head activity 0 sha256:" + digits,
		"audit head activity 042 sha256:" + digits,
		"audit head activity +42 sha256:" + digits,
		"audit head activity -1 sha256:" + digits,
		"audit head activity 18446744073709551616 sha256:" + digits,
		"audit head activity 4x sha256:" + digits,
		"audit head activity 42 sha256:" + digits[1:],
		"audit head activity 42 sha256:" + digits + "a",
		"audit head activity 42 sha256:" + strings.ToUpper(digits),
		"audit head activity 42 sha256:" + strings.Repeat("g", 64),
		"audit head activity 42 sha1:" + digits,
		"audit head activity 42 " + digits,
		"audit  head activity 42 sha256:" + digits,
	} {
		if got, err := ParseHead(line); err == nil {
			t.Errorf("ParseHead(%q) = %+v, want an error", line, got)
		}
	}
}

func TestErrBroken(t *testing.T) {
	err := fmt.Errorf("audit log %q: record 2: %w", LoginLog, ErrBroken)
	if !errors.Is(err, ErrBroken) {
		t.Errorf("errors.Is() of a wrapped ErrBroken is false")
	}
}

func TestRecordKeys(t *testing.T) {
	// The keys of the JSON are those of the fields of Record, in the order of the fields, and the keys of its parts.
	data, err := Encode(full())
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	rest := string(data)
	for _, k := range []string{"log", "n", "prev", "at", "actor", "action", "result", "target", "account", "session",
		"client", "login", "operation", "changed", "before", "after", "revision", "status", "detail"} {
		i := strings.Index(rest, `"`+k+`":`)
		if i < 0 {
			t.Fatalf("the key %q is missing or out of order in %s", k, data)
		}
		keys = append(keys, k)
		rest = rest[i+len(k)+3:]
	}
	typ := reflect.TypeOf(Record{})
	if typ.NumField() != len(keys) {
		t.Errorf("Record has %d fields, the test %d keys", typ.NumField(), len(keys))
	}
	keysOfDetail := slices.Sorted(maps.Keys(full().Detail))
	if !slices.IsSorted(keysOfDetail) || !strings.Contains(string(data), `"detail":{"base"`) {
		t.Errorf("the keys of the detail are not sorted in %s", data)
	}
}
