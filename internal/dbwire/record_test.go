package dbwire

import (
	"strings"
	"testing"
)

const (
	testPrev        = "sha256:71547e237559add83f6c9279fdb5762eafee857a1060e2d97c0316a70cefb6ba"
	activityRecord  = `{"log":"activity","n":1245,"prev":"` + testPrev + `","at":"2026-11-12T01:02:03.456789Z","actor":"usr_01JB8T7H4MA2C5E8G1J4K7N3P6","action":"entry.publish","target":{"kind":"entry","id":"ent_01JB8W2N5KD3F6H9J1M4P7R2T5"},"operation":"op_01JBG3N6Y6A9D2F5H8K1N4Q7S0","changed":true,"before":{"seq":17},"after":{"seq":18,"hash":"sha256:43cc23fa52b87b4cc1d02b5b114154151d6adddb17c9fddc06b027fa99e24008"},"detail":{"n":"7","prev":"x"}}`
	loginRecord     = `{"log":"login","n":812,"prev":"` + testPrev + `","at":"2026-11-12T01:02:03.456789Z","action":"login.failure","result":"wrong-password","client":{"addr":"203.0.113.7","userAgent":"Mozilla/5.0"},"login":"yamada"}`
	firstRecord     = `{"log":"activity","n":1,"at":"2026-11-03T08:00:00.12Z","actor":"system","action":"store.create"}`
	suppressedLogin = `{"log":"login","n":815,"prev":"` + testPrev + `","at":"2026-11-12T01:04:12.345678Z","action":"login.suppressed","detail":{"from":"2026-11-12T01:03:00Z","to":"2026-11-12T01:04:00Z"}}`
)

// minimal is what a record has besides its log and its number, at least.
const minimal = `,"at":"2026-11-03T08:00:00Z","action":"x"`

func TestReadRecordID(t *testing.T) {
	for _, c := range []struct {
		name string
		data string
		log  string
		n    uint64
		prev string
		ok   bool
	}{
		{"an activity record", activityRecord, "activity", 1245, testPrev, true},
		{"a login record", loginRecord, "login", 812, testPrev, true},
		{"the first record, which has no prev", firstRecord, "activity", 1, "", true},
		{"a suppression record", suppressedLogin, "login", 815, testPrev, true},
		{"the members in another order", `{"action":"x","prev":"p","at":"2026-11-03T08:00:00Z","n":3,"log":"login"}`, "login", 3, "p", true},
		{"a prev that is null", `{"log":"login","n":3,"prev":null` + minimal + `}`, "login", 3, "", true},
		{"an empty prev", `{"log":"login","n":3,"prev":""` + minimal + `}`, "login", 3, "", true},
		{"white space", " {\n\"log\" : \"login\" , \"n\" : 3 , \"at\" : \"2026-11-03T08:00:00Z\" , \"action\" : \"x\" }\n", "login", 3, "", true},
		{"an escaped name", `{"\u006cog":"login","n":3` + minimal + `}`, "login", 3, "", true},
		{"the largest n", `{"log":"login","n":18446744073709551615` + minimal + `}`, "login", 1<<64 - 1, "", true},
		{"a member of another type that Decode reads", `{"log":"login","n":3` + minimal + `,"changed":true,"before":{"seq":-1},"detail":{}}`, "login", 3, "", true},

		{"nil", "", "", 0, "", false},
		{"a mark", string(AppendMark(nil, t2026)), "", 0, "", false},
		{"an event of the daemon that has the text of a record", "\x00" + loginRecord, "", 0, "", false},
		{"garbage", "junk", "", 0, "", false},
		{"null", "null", "", 0, "", false},
		{"an array", `[` + loginRecord + `]`, "", 0, "", false},
		{"an empty object", `{}`, "", 0, "", false},
		{"no log", `{"n":3` + minimal + `}`, "", 0, "", false},
		{"no n", `{"log":"activity"` + minimal + `}`, "", 0, "", false},
		{"no time", `{"log":"login","n":3,"action":"x"}`, "", 0, "", false},
		{"a time that is the zero time", `{"log":"login","n":3,"at":"0001-01-01T00:00:00Z","action":"x"}`, "", 0, "", false},
		{"a time that is not one", `{"log":"login","n":3,"at":"yesterday","action":"x"}`, "", 0, "", false},
		{"a time that is null", `{"log":"login","n":3,"at":null,"action":"x"}`, "", 0, "", false},
		{"no action", `{"log":"login","n":3,"at":"2026-11-03T08:00:00Z"}`, "", 0, "", false},
		{"an empty action", `{"log":"login","n":3,"at":"2026-11-03T08:00:00Z","action":""}`, "", 0, "", false},
		{"a log that is not one of the logs", `{"log":"other","n":3` + minimal + `}`, "", 0, "", false},
		{"an unknown member", `{"log":"login","n":3` + minimal + `,"extra":"1"}`, "", 0, "", false},
		{"an unknown member inside another", `{"log":"login","n":3` + minimal + `,"client":{"addr":"a","extra":"1"}}`, "", 0, "", false},
		{"n 0", `{"log":"login","n":0` + minimal + `}`, "", 0, "", false},
		{"n as a string", `{"log":"login","n":"3"` + minimal + `}`, "", 0, "", false},
		{"n negative", `{"log":"login","n":-3` + minimal + `}`, "", 0, "", false},
		{"n with a fraction", `{"log":"login","n":3.5` + minimal + `}`, "", 0, "", false},
		{"n with a zero fraction", `{"log":"login","n":3.0` + minimal + `}`, "", 0, "", false},
		{"n in the exponent form", `{"log":"login","n":3e0` + minimal + `}`, "", 0, "", false},
		{"n over 64 bits", `{"log":"login","n":18446744073709551616` + minimal + `}`, "", 0, "", false},
		{"n null", `{"log":"login","n":null` + minimal + `}`, "", 0, "", false},
		{"log null", `{"log":null,"n":3` + minimal + `}`, "", 0, "", false},
		{"log a number", `{"log":1,"n":3` + minimal + `}`, "", 0, "", false},
		{"prev a number", `{"log":"login","n":3,"prev":5` + minimal + `}`, "", 0, "", false},
		{"detail with a value that is no string", `{"log":"login","n":3` + minimal + `,"detail":{"a":1}}`, "", 0, "", false},
		{"a name in capitals", `{"LOG":"login","n":3` + minimal + `}`, "", 0, "", false},
		{"n twice", `{"log":"login","n":3,"n":4` + minimal + `}`, "", 0, "", false},
		{"a name twice inside another member", `{"log":"login","n":3` + minimal + `,"detail":{"x":"1","x":"2"}}`, "", 0, "", false},
		{"invalid UTF-8", "{\"log\":\"login\",\"n\":3" + minimal + ",\"login\":\"a\xffb\"}", "", 0, "", false},
		{"data after the record", `{"log":"login","n":3` + minimal + `}x`, "", 0, "", false},
		{"a second object", `{"log":"login","n":3` + minimal + `}{"log":"login","n":4` + minimal + `}`, "", 0, "", false},
		{"cut short", `{"log":"login","n":3`, "", 0, "", false},
		{"cut at a string", `{"log":"log`, "", 0, "", false},
	} {
		log, n, prev, ok := ReadRecordID([]byte(c.data))
		if log != c.log || n != c.n || prev != c.prev || ok != c.ok {
			t.Errorf("%s: ReadRecordID = %q, %d, %q, %v; want %q, %d, %q, %v", c.name, log, n, prev, ok, c.log, c.n, c.prev, c.ok)
		}
	}
}

// TestReadRecordIDDoesNotReadInside checks that a member of another member is not the log, the number or the prev.
func TestReadRecordIDDoesNotReadInside(t *testing.T) {
	data := `{"log":"login","n":9` + minimal + `,"detail":{"log":"activity","n":"1","prev":"x"}}`
	if log, n, prev, ok := ReadRecordID([]byte(data)); !ok || log != "login" || n != 9 || prev != "" {
		t.Errorf("ReadRecordID = %q, %d, %q, %v", log, n, prev, ok)
	}
	long := `{"log":"login","n":4` + minimal + `,"detail":{"x":"` + strings.Repeat("y", 1<<16) + `"}}`
	if log, n, _, ok := ReadRecordID([]byte(long)); !ok || log != "login" || n != 4 {
		t.Errorf("ReadRecordID of a long record = %q, %d, %v", log, n, ok)
	}
}

func FuzzReadRecordID(f *testing.F) {
	for _, s := range []string{
		activityRecord, loginRecord, firstRecord, suppressedLogin, "", "{}", "null", "[]", `{"log":"login","n":1` + minimal + `}`,
		`{"log":"login","n":0` + minimal + `}`, `{"log":"login","n":"1"` + minimal + `}`, `{"log":null,"n":1,"prev":null}`,
		`{"log":"login","n":1,"n":2` + minimal + `}`, `{"log":"login","n":1,"prev":"a"` + minimal + `}x`, "\x00", "\x00\x01",
		string(AppendMark(nil, t2026)), `{"log":"a\ud800","n":1` + minimal + `}`, "{\"log\":\"\xff\",\"n\":1" + minimal + "}",
		`{"log":"login","n":18446744073709551616` + minimal + `}`, `{"log":"login","n":-0` + minimal + `}`,
		`{"log":"login","n":1e2` + minimal + `}`, `{"log":"login","n":1,"at":"x","action":"x"}`, `{"log":"login","n":1,"extra":1` + minimal + `}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		log, n, prev, ok := ReadRecordID(data)
		if !ok && (log != "" || n != 0 || prev != "") {
			t.Fatalf("not ok, and %q, %d, %q", log, n, prev)
		}
		if ok && n == 0 {
			t.Fatalf("ok with n 0")
		}
	})
}
