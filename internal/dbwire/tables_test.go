package dbwire

import (
	"bytes"
	"math"
	"strings"
	"testing"
	"time"
)

func TestLimits(t *testing.T) {
	want := Limits{
		Frame: 1 << 20, Commit: 268435446, Object: 64 << 20, Staged: 268435446, Readers: 64,
		SnapshotIdle: 5 * time.Minute, Stall: 30 * time.Second, CommitsPerSecond: 50, MinFree: 1 << 30,
		MinFreeInodes: 1 << 16,
	}
	if got := (Limits{}).WithDefaults(); got != want {
		t.Errorf("the defaults are %+v, want %+v", got, want)
	}
	if got := want.WithDefaults(); got != want {
		t.Errorf("WithDefaults is not idempotent: %+v", got)
	}

	set := Limits{
		Frame: 7, Commit: 8, Object: 9, Staged: 10, Readers: 11,
		SnapshotIdle: time.Second, Stall: 2 * time.Second, CommitsPerSecond: 12, MinFree: 13,
		MinFreeInodes: 14,
	}
	if got := set.WithDefaults(); got != set {
		t.Errorf("a field that is set changed: %+v", got)
	}

	off := Limits{SnapshotIdle: -1, Stall: -1, CommitsPerSecond: -1, MinFree: -1, MinFreeInodes: -1}.WithDefaults()
	if off.SnapshotIdle != -1 || off.Stall != -1 || off.CommitsPerSecond != -1 || off.MinFree != -1 || off.MinFreeInodes != -1 {
		t.Errorf("a negative limit is not kept: %+v", off)
	}
	if off.Frame != want.Frame || off.Commit != want.Commit || off.Readers != want.Readers {
		t.Errorf("the other fields are not the defaults: %+v", off)
	}
	if got := (Limits{Frame: -5}).WithDefaults().Frame; got != -5 {
		t.Errorf("a negative frame changed to %d", got)
	}

	// The commit limit is what the engine allows (2^28 less the largest varint) and the key limit is the engine's.
	if want.Commit != 1<<28-10 || MaxKey != 4096 {
		t.Errorf("Commit %d, MaxKey %d", want.Commit, MaxKey)
	}
}

func TestTables(t *testing.T) {
	var appendOnly []uint32
	for table := range uint32(100) {
		if AppendOnly(table) {
			appendOnly = append(appendOnly, table)
		}
	}
	if len(appendOnly) != 3 || appendOnly[0] != 3 || appendOnly[1] != 4 || appendOnly[2] != 5 {
		t.Errorf("append only tables: %v, want 3, 4, 5", appendOnly)
	}
	if AppendOnly(math.MaxUint32) {
		t.Error("the table 2^32-1 is append only")
	}
	if TableVersion != 7 || TableMeta != 1 || MetaSchema != "schema" || TableHistory != 3 || TableRevisions != 4 ||
		TableEntryRevisions != 5 || TableHeads != 11 || MaxHeadKey != 64 || MaxHeadLine != 255 {
		t.Error("a fact about the tables is not the one of the plan")
	}

	line := "audit head login 12 sha256:" + strings.Repeat("0123456789abcdef", 4)
	for _, c := range []struct {
		name       string
		key, value string
		ok         bool
	}{
		{"a head", "login", line, true},
		{"the key with every kind of character", "aZ09._-", line, true},
		{"the shortest", "a", " ", true},
		{"the longest key", strings.Repeat("k", 64), line, true},
		{"the longest line", "login", strings.Repeat("~", 255), true},
		{"all printable", "login", " !\"#$%&'()*+,-./0123456789:;<=>?@AZ[\\]^_`az{|}~", true},
		{"an empty key", "", line, false},
		{"a key of 65 bytes", strings.Repeat("k", 65), line, false},
		{"a key with a space", "log in", line, false},
		{"a key with a slash", "a/b", line, false},
		{"a key with a newline", "a\n", line, false},
		{"a key that is not ASCII", "café", line, false},
		{"an empty line", "login", "", false},
		{"a line of 256 bytes", "login", strings.Repeat("a", 256), false},
		{"a line with a newline", "login", line + "\n", false},
		{"a line with a newline inside", "login", "a\nb", false},
		{"a line with a tab", "login", "a\tb", false},
		{"a line with 0x7F", "login", "a\x7fb", false},
		{"a line with 0x1F", "login", "a\x1fb", false},
		{"a line with a NUL", "login", "a\x00b", false},
		{"a line that is not ASCII", "login", "café", false},
	} {
		if got := ValidHeadRow([]byte(c.key), []byte(c.value)); got != c.ok {
			t.Errorf("%s: ValidHeadRow = %v, want %v", c.name, got, c.ok)
		}
	}
	if ValidHeadRow(nil, nil) || ValidHeadRow([]byte("login"), nil) {
		t.Error("nil is a valid row")
	}
}

func TestMark(t *testing.T) {
	utc := time.UTC
	for _, c := range []struct {
		name string
		t    time.Time
	}{
		{"now", time.Now()},
		{"2026", time.Date(2026, 11, 15, 10, 20, 30, 123456789, utc)},
		{"the epoch", time.Unix(0, 0)},
		{"a nanosecond after the epoch", time.Unix(0, 1)},
		{"a nanosecond before the epoch", time.Unix(0, -1)},
		{"1969", time.Date(1969, 12, 31, 23, 59, 59, 999999999, utc)},
		{"1900", time.Date(1900, 1, 1, 0, 0, 0, 1, utc)},
		{"the year 1", time.Time{}},
		{"the year 9999", time.Date(9999, 12, 31, 23, 59, 59, 999999999, utc)},
		{"a time of another zone", time.Date(2026, 1, 2, 3, 4, 5, 6, time.FixedZone("x", -9*3600))},
		{"the largest nanoseconds", time.Unix(1, 999999999)},
		{"a second before the epoch", time.Unix(-1, 999999999)},
	} {
		b := AppendMark(nil, c.t)
		if len(b) != MarkSize || b[0] != DaemonEvent || b[1] != MarkKind {
			t.Errorf("%s: the mark is %x", c.name, b)
		}
		got, ok := ParseMark(b)
		if !ok || !got.Equal(c.t) || got.Location() != time.UTC || got.Nanosecond() != c.t.Nanosecond() {
			t.Errorf("%s: ParseMark = %v, %v, want %v in UTC", c.name, got, ok, c.t)
		}
		if !IsDaemonEvent(b) {
			t.Errorf("%s: the mark is not an event of the daemon", c.name)
		}
		if again := AppendMark(nil, got); !bytes.Equal(again, b) {
			t.Errorf("%s: the mark is %x when written again, was %x", c.name, again, b)
		}
	}

	if got, want := AppendMark(nil, time.Unix(0x0102030405, 0x0A0B0C0D)), []byte{0, 1, 0, 0, 0, 1, 2, 3, 4, 5, 0x0A, 0x0B, 0x0C, 0x0D}; !bytes.Equal(got, want) {
		t.Errorf("the layout: %x, want %x", got, want)
	}
	if got, want := AppendMark(nil, time.Unix(-2, 3)), []byte{0, 1, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFE, 0, 0, 0, 3}; !bytes.Equal(got, want) {
		t.Errorf("a time before 1970: %x, want %x", got, want)
	}
	if got := AppendMark([]byte("xy"), time.Unix(1, 2)); string(got[:2]) != "xy" || len(got) != 2+MarkSize || !IsDaemonEvent(got[2:]) {
		t.Errorf("AppendMark does not keep dst: %x", got)
	}

	good := AppendMark(nil, time.Unix(1, 2))
	if _, ok := ParseMark(good); !ok {
		t.Fatal("the mark of a test is not read")
	}
	badNanos := func(n uint32) []byte {
		b := bytes.Clone(good)
		b[10], b[11], b[12], b[13] = byte(n>>24), byte(n>>16), byte(n>>8), byte(n)
		return b
	}
	for name, b := range map[string][]byte{
		"nil":                     nil,
		"empty":                   {},
		"13 bytes":                good[:13],
		"15 bytes":                append(bytes.Clone(good), 0),
		"the first byte 1":        append([]byte{1}, good[1:]...),
		"the first byte '{'":      append([]byte{'{'}, good[1:]...),
		"the kind 0":              append([]byte{0, 0}, good[2:]...),
		"the kind 2":              append([]byte{0, 2}, good[2:]...),
		"1e9 nanoseconds":         badNanos(1e9),
		"1e9+1 nanoseconds":       badNanos(1e9 + 1),
		"all nanoseconds":         badNanos(math.MaxUint32),
		"an audit record":         []byte(`{"n":1,"a":"x"}`),
		"a mark and a record":     append(bytes.Clone(good), `{"n":1}`...),
		"two marks":               append(bytes.Clone(good), good...),
		"a mark with a prefix":    append([]byte{'x'}, good...),
		"the size of a mark of x": bytes.Repeat([]byte{'x'}, MarkSize),
	} {
		if got, ok := ParseMark(b); ok {
			t.Errorf("%s: ParseMark = %v", name, got)
		}
	}
	if _, ok := ParseMark(badNanos(999999999)); !ok {
		t.Error("999999999 nanoseconds are refused")
	}

	// A mark of any seconds is read, and written as it was.
	for _, sec := range []int64{math.MaxInt64, math.MinInt64, 1 << 40, -1 << 40} {
		b := AppendMark(nil, time.Unix(sec, 5))
		if got, ok := ParseMark(b); !ok || !bytes.Equal(AppendMark(nil, got), b) {
			t.Errorf("%d seconds: %v, %v", sec, got, ok)
		}
	}

	for _, c := range []struct {
		b    []byte
		want bool
	}{
		{nil, false}, {[]byte{}, false}, {[]byte{0}, true}, {[]byte{0, 1}, true}, {[]byte("{}"), false},
		{[]byte(`{"n":1}`), false}, {[]byte{1}, false}, {[]byte{0xFF}, false}, {good, true},
	} {
		if got := IsDaemonEvent(c.b); got != c.want {
			t.Errorf("IsDaemonEvent(%q) = %v, want %v", c.b, got, c.want)
		}
	}
}
