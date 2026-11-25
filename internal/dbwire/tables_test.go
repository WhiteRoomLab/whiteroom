package dbwire

import (
	"bytes"
	"encoding/hex"
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
	if AppendOnly(TableChains) {
		t.Error("the table chains is append only: the maintenance of the daemon replaces its start rows")
	}
	if AppendOnly(math.MaxUint32) {
		t.Error("the table 2^32-1 is append only")
	}
	if TableVersion != 10 || TableMeta != 1 || MetaSchema != "schema" || TableHistory != 3 || TableRevisions != 4 ||
		TableEntryRevisions != 5 || TableHeads != 11 || TableChains != 12 || MaxHeadKey != 64 || MaxHeadLine != 255 {
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

func TestChainRows(t *testing.T) {
	var prev, hash [32]byte
	for i := range prev {
		prev[i], hash[i] = byte(i+1), byte(0xF0-i)
	}
	at := time.Date(2026, 11, 23, 10, 20, 30, 123456789, time.UTC)
	atBytes := appendTime(nil, at)
	if want := []byte{0, 0, 0, 0, byte(at.Unix() >> 24), byte(at.Unix() >> 16), byte(at.Unix() >> 8), byte(at.Unix()), 0x07, 0x5B, 0xCD, 0x15}; !bytes.Equal(atBytes, want) {
		t.Fatalf("the bytes of the time are %x, want %x", atBytes, want)
	}

	if ChainStartSize != 52 || ChainBreakSize != 44 || ChainHoleSize != 52 {
		t.Errorf("the sizes are %d, %d and %d, want 52, 44 and 52", ChainStartSize, ChainBreakSize, ChainHoleSize)
	}
	start := ChainStart{First: 0x0102030405060708, Prev: prev, At: at}
	b := AppendChainStart(nil, start)
	want := append(append([]byte{1, 2, 3, 4, 5, 6, 7, 8}, prev[:]...), atBytes...)
	if !bytes.Equal(b, want) || len(b) != ChainStartSize {
		t.Errorf("the value of a chain start is %x, want %x", b, want)
	}
	if got, ok := ParseChainStart(b); !ok || got.First != start.First || got.Prev != prev || !got.At.Equal(at) || got.At.Location() != time.UTC {
		t.Errorf("ParseChainStart = %+v, %v, want %+v", got, ok, start)
	}
	if got := AppendChainStart([]byte("xy"), start); string(got[:2]) != "xy" || !bytes.Equal(got[2:], b) {
		t.Errorf("AppendChainStart does not keep dst: %x", got)
	}

	brk := ChainBreak{Hash: hash, At: at}
	b = AppendChainBreak(nil, brk)
	want = append(bytes.Clone(hash[:]), atBytes...)
	if !bytes.Equal(b, want) || len(b) != ChainBreakSize {
		t.Errorf("the value of a break is %x, want %x", b, want)
	}
	if got, ok := ParseChainBreak(b); !ok || got.Hash != hash || !got.At.Equal(at) || got.At.Location() != time.UTC {
		t.Errorf("ParseChainBreak = %+v, %v, want %+v", got, ok, brk)
	}
	if got := AppendChainBreak([]byte("xy"), brk); string(got[:2]) != "xy" || !bytes.Equal(got[2:], b) {
		t.Errorf("AppendChainBreak does not keep dst: %x", got)
	}

	// A time of any seconds is read, and written as it was.
	for _, sec := range []int64{math.MaxInt64, math.MinInt64, 1 << 40, -1 << 40, 0, -1} {
		c := ChainStart{First: 2, At: time.Unix(sec, 5)}
		if got, ok := ParseChainStart(AppendChainStart(nil, c)); !ok || !bytes.Equal(AppendChainStart(nil, got), AppendChainStart(nil, c)) {
			t.Errorf("a start at %d seconds: %v, %v", sec, got, ok)
		}
		k := ChainBreak{At: time.Unix(sec, 5)}
		if got, ok := ParseChainBreak(AppendChainBreak(nil, k)); !ok || !bytes.Equal(AppendChainBreak(nil, got), AppendChainBreak(nil, k)) {
			t.Errorf("a break at %d seconds: %v, %v", sec, got, ok)
		}
	}

	good, goodBreak := AppendChainStart(nil, start), AppendChainBreak(nil, brk)
	withNanos := func(b []byte, n uint32) []byte {
		b = bytes.Clone(b)
		for i := range 4 {
			b[len(b)-1-i] = byte(n >> (8 * i))
		}
		return b
	}
	withFirst := func(n uint64) []byte {
		b := bytes.Clone(good)
		for i := range 8 {
			b[7-i] = byte(n >> (8 * i))
		}
		return b
	}
	for name, b := range map[string][]byte{
		"nil":                  nil,
		"empty":                {},
		"51 bytes":             good[:51],
		"53 bytes":             append(bytes.Clone(good), 0),
		"two rows":             append(bytes.Clone(good), good...),
		"the value of a break": goodBreak,
		"1e9 nanoseconds":      withNanos(good, 1e9),
		"all nanoseconds":      withNanos(good, math.MaxUint32),
		"First 0":              withFirst(0),
		"First 1":              withFirst(1),
	} {
		if got, ok := ParseChainStart(b); ok {
			t.Errorf("%s: ParseChainStart = %+v", name, got)
		}
	}
	if got, ok := ParseChainStart(withFirst(2)); !ok || got.First != 2 {
		t.Errorf("First 2: ParseChainStart = %+v, %v", got, ok)
	}
	if got, ok := ParseChainStart(withFirst(math.MaxUint64)); !ok || got.First != math.MaxUint64 {
		t.Errorf("the largest First: ParseChainStart = %+v, %v", got, ok)
	}
	if _, ok := ParseChainStart(withNanos(good, 999999999)); !ok {
		t.Error("999999999 nanoseconds are refused")
	}
	for name, b := range map[string][]byte{
		"nil":                  nil,
		"43 bytes":             goodBreak[:43],
		"45 bytes":             append(bytes.Clone(goodBreak), 0),
		"the value of a start": good,
		"1e9 nanoseconds":      withNanos(goodBreak, 1e9),
		"all nanoseconds":      withNanos(goodBreak, math.MaxUint32),
	} {
		if got, ok := ParseChainBreak(b); ok {
			t.Errorf("%s: ParseChainBreak = %+v", name, got)
		}
	}
	if _, ok := ParseChainBreak(withNanos(goodBreak, 999999999)); !ok {
		t.Error("999999999 nanoseconds are refused")
	}

	// The value of a hole: Last, Hash and the time. The row has First in its key.
	hole := ChainHole{First: 3, Last: 0x0102030405060708, Hash: hash, At: at}
	b = AppendChainHole(nil, hole)
	want = append(append([]byte{1, 2, 3, 4, 5, 6, 7, 8}, hash[:]...), atBytes...)
	if !bytes.Equal(b, want) || len(b) != ChainHoleSize {
		t.Errorf("the value of a hole is %x, want %x", b, want)
	}
	const goldenHole = "0000000000000009" + "f0efeeedecebeae9e8e7e6e5e4e3e2e1e0dfdedddcdbdad9d8d7d6d5d4d3d2d1" + "000000006b04136e075bcd15"
	if got := AppendChainHole(nil, ChainHole{First: 9, Last: 9, Hash: hash, At: at}); hex.EncodeToString(got) != goldenHole {
		t.Errorf("the value of a hole is %x, want %s", got, goldenHole)
	}
	if got, ok := ParseChainHole(3, b); !ok || got != (ChainHole{First: 3, Last: hole.Last, Hash: hash, At: got.At}) || !got.At.Equal(at) || got.At.Location() != time.UTC {
		t.Errorf("ParseChainHole = %+v, %v, want %+v", got, ok, hole)
	}
	if got := AppendChainHole([]byte("xy"), hole); string(got[:2]) != "xy" || !bytes.Equal(got[2:], b) {
		t.Errorf("AppendChainHole does not keep dst: %x", got)
	}
	// A hole of one record, and the largest.
	if got, ok := ParseChainHole(7, AppendChainHole(nil, ChainHole{Last: 7, At: at})); !ok || got.First != 7 || got.Last != 7 {
		t.Errorf("a hole of one record: %+v, %v", got, ok)
	}
	if got, ok := ParseChainHole(1, AppendChainHole(nil, ChainHole{Last: math.MaxUint64, At: at})); !ok || got.Last != math.MaxUint64 {
		t.Errorf("the largest hole: %+v, %v", got, ok)
	}
	goodHole := AppendChainHole(nil, ChainHole{Last: 9, Hash: hash, At: at})
	for name, c := range map[string]struct {
		first uint64
		b     []byte
	}{
		"nil":                       {3, nil},
		"51 bytes":                  {3, goodHole[:51]},
		"53 bytes":                  {3, append(bytes.Clone(goodHole), 0)},
		"two rows":                  {3, append(bytes.Clone(goodHole), goodHole...)},
		"the value of a break":      {3, goodBreak},
		"1e9 nanoseconds":           {3, withNanos(goodHole, 1e9)},
		"all nanoseconds":           {3, withNanos(goodHole, math.MaxUint32)},
		"First 0":                   {0, goodHole},
		"Last under First":          {10, goodHole},
		"Last 0":                    {3, AppendChainHole(nil, ChainHole{Last: 0, Hash: hash, At: at})},
		"the largest First, Last 9": {math.MaxUint64, goodHole},
	} {
		if got, ok := ParseChainHole(c.first, c.b); ok {
			t.Errorf("%s: ParseChainHole = %+v", name, got)
		}
	}
	if _, ok := ParseChainHole(9, goodHole); !ok {
		t.Error("First 9, Last 9 is refused")
	}
	if _, ok := ParseChainHole(3, withNanos(goodHole, 999999999)); !ok {
		t.Error("999999999 nanoseconds are refused")
	}
	// The value of a hole has the size and the layout of that of a chain start: the keys tell them apart. It is no break.
	if _, ok := ParseChainBreak(goodHole); ok {
		t.Error("the value of a hole is read as a break")
	}
	// The value of an accepted gap is that of a hole, with the first record of the key of the gap.
	gapKey := ChainGapKey(LogActivity, 4)
	if _, first, ok := ParseChainGapKey(gapKey); !ok || first != 4 {
		t.Fatalf("ParseChainGapKey(%q) = %d, %v", gapKey, first, ok)
	}
	gap := ChainHole{First: 4, Last: 6, Hash: hash, At: at}
	if got, ok := ParseChainHole(4, AppendChainHole(nil, gap)); !ok || got != (ChainHole{First: 4, Last: 6, Hash: hash, At: got.At}) || !got.At.Equal(at) {
		t.Errorf("the value of a gap is read as %+v, %v, want %+v", got, ok, gap)
	}
}

func TestChainKeys(t *testing.T) {
	if got := ChainStartKey(LogLogin); string(got) != "login" {
		t.Errorf("ChainStartKey = %q", got)
	}
	if got, want := ChainBreakKey(LogActivity, 0x0102030405060708), []byte("activity\x00\x01\x02\x03\x04\x05\x06\x07\x08"); !bytes.Equal(got, want) {
		t.Errorf("ChainBreakKey = %q, want %q", got, want)
	}
	if got := ChainBreakPrefix(LogLogin); string(got) != "login\x00" {
		t.Errorf("ChainBreakPrefix = %q", got)
	}
	for _, c := range []struct {
		log string
		n   uint64
	}{{LogLogin, 1}, {LogActivity, math.MaxUint64}, {LogLogin, 0}, {"x", 7}} {
		key := ChainBreakKey(c.log, c.n)
		// The key of a start is no prefix of the keys of the breaks, and the prefix of the breaks no key of a start.
		if !bytes.HasPrefix(key, ChainBreakPrefix(c.log)) || bytes.HasPrefix(ChainStartKey(c.log), ChainBreakPrefix(c.log)) {
			t.Errorf("%q: the key %q and the prefixes", c.log, key)
		}
		if log, n, ok := ParseChainBreakKey(key); !ok || log != c.log || n != c.n {
			t.Errorf("ParseChainBreakKey(%q) = %q, %d, %v", key, log, n, ok)
		}
	}
	if got, want := ChainHoleKey(LogActivity, 0x0102030405060708), []byte("activity\x01\x01\x02\x03\x04\x05\x06\x07\x08"); !bytes.Equal(got, want) {
		t.Errorf("ChainHoleKey = %q, want %q", got, want)
	}
	if got := ChainHolePrefix(LogLogin); string(got) != "login\x01" {
		t.Errorf("ChainHolePrefix = %q", got)
	}
	for _, c := range []struct {
		log   string
		first uint64
	}{{LogLogin, 1}, {LogActivity, math.MaxUint64}, {"x", 7}} {
		key := ChainHoleKey(c.log, c.first)
		// The prefixes of the holes and of the breaks, and the key of a start, are none a prefix of the keys of another.
		if !bytes.HasPrefix(key, ChainHolePrefix(c.log)) || bytes.HasPrefix(key, ChainBreakPrefix(c.log)) ||
			bytes.HasPrefix(ChainBreakKey(c.log, c.first), ChainHolePrefix(c.log)) || bytes.HasPrefix(ChainStartKey(c.log), ChainHolePrefix(c.log)) {
			t.Errorf("%q: the key %q and the prefixes", c.log, key)
		}
		if log, first, ok := ParseChainHoleKey(key); !ok || log != c.log || first != c.first {
			t.Errorf("ParseChainHoleKey(%q) = %q, %d, %v", key, log, first, ok)
		}
		// A key of a hole is no key of a break, and a key of a break is no key of a hole.
		if log, n, ok := ParseChainBreakKey(key); ok {
			t.Errorf("ParseChainBreakKey(%q) = %q, %d", key, log, n)
		}
		if log, first, ok := ParseChainHoleKey(ChainBreakKey(c.log, c.first)); ok {
			t.Errorf("ParseChainHoleKey(a break) = %q, %d", log, first)
		}
	}
	if got, want := ChainGapKey(LogActivity, 0x0102030405060708), []byte("activity\x02\x01\x02\x03\x04\x05\x06\x07\x08"); !bytes.Equal(got, want) {
		t.Errorf("ChainGapKey = %q, want %q", got, want)
	}
	if got, want := ChainGapKey(LogLogin, 3), []byte("login\x02\x00\x00\x00\x00\x00\x00\x00\x03"); !bytes.Equal(got, want) {
		t.Errorf("ChainGapKey = %q, want %q", got, want)
	}
	if got := ChainGapPrefix(LogLogin); string(got) != "login\x02" {
		t.Errorf("ChainGapPrefix = %q", got)
	}
	for _, c := range []struct {
		log   string
		first uint64
	}{{LogLogin, 1}, {LogActivity, math.MaxUint64}, {"x", 7}} {
		key := ChainGapKey(c.log, c.first)
		// The prefix of the gaps is none of those of the holes and of the breaks, and the key of a start is no prefix of it.
		if !bytes.HasPrefix(key, ChainGapPrefix(c.log)) || bytes.HasPrefix(key, ChainHolePrefix(c.log)) || bytes.HasPrefix(key, ChainBreakPrefix(c.log)) ||
			bytes.HasPrefix(ChainHoleKey(c.log, c.first), ChainGapPrefix(c.log)) || bytes.HasPrefix(ChainBreakKey(c.log, c.first), ChainGapPrefix(c.log)) ||
			bytes.HasPrefix(ChainStartKey(c.log), ChainGapPrefix(c.log)) {
			t.Errorf("%q: the key %q and the prefixes", c.log, key)
		}
		if log, first, ok := ParseChainGapKey(key); !ok || log != c.log || first != c.first {
			t.Errorf("ParseChainGapKey(%q) = %q, %d, %v", key, log, first, ok)
		}
		// A key of a gap is no key of a hole or of a break, and theirs are no key of a gap.
		if log, first, ok := ParseChainHoleKey(key); ok {
			t.Errorf("ParseChainHoleKey(a gap) = %q, %d", log, first)
		}
		if log, n, ok := ParseChainBreakKey(key); ok {
			t.Errorf("ParseChainBreakKey(a gap) = %q, %d", log, n)
		}
		if log, first, ok := ParseChainGapKey(ChainHoleKey(c.log, c.first)); ok {
			t.Errorf("ParseChainGapKey(a hole) = %q, %d", log, first)
		}
		if log, first, ok := ParseChainGapKey(ChainBreakKey(c.log, c.first)); ok {
			t.Errorf("ParseChainGapKey(a break) = %q, %d", log, first)
		}
	}
	for name, key := range map[string][]byte{
		"nil":                      nil,
		"a log":                    []byte("login"),
		"a log and the byte":       []byte("login\x02"),
		"nine bytes":               []byte("\x02\x00\x00\x00\x00\x00\x00\x00\x00"),
		"the number 0":             []byte("login\x02\x00\x00\x00\x00\x00\x00\x00\x00"),
		"the byte of a hole":       []byte("login\x01\x00\x00\x00\x00\x00\x00\x00\x01"),
		"the byte of a break":      []byte("login\x00\x00\x00\x00\x00\x00\x00\x00\x01"),
		"the byte 3":               []byte("login\x03\x00\x00\x00\x00\x00\x00\x00\x01"),
		"a log and a short number": []byte("login\x02\x00\x00\x00\x00\x00\x00\x01"),
	} {
		if log, first, ok := ParseChainGapKey(key); ok {
			t.Errorf("%s: ParseChainGapKey = %q, %d", name, log, first)
		}
	}
	for name, key := range map[string][]byte{
		"nil":                      nil,
		"a log":                    []byte("login"),
		"a log and the byte":       []byte("login\x01"),
		"nine bytes":               []byte("\x01\x00\x00\x00\x00\x00\x00\x00\x00"),
		"the number 0":             []byte("login\x01\x00\x00\x00\x00\x00\x00\x00\x00"),
		"the byte of a break":      []byte("login\x00\x00\x00\x00\x00\x00\x00\x00\x01"),
		"the byte of a gap":        []byte("login\x02\x00\x00\x00\x00\x00\x00\x00\x01"),
		"a log and a short number": []byte("login\x01\x00\x00\x00\x00\x00\x00\x01"),
	} {
		if log, first, ok := ParseChainHoleKey(key); ok {
			t.Errorf("%s: ParseChainHoleKey = %q, %d", name, log, first)
		}
	}
	for name, key := range map[string][]byte{
		"nil":                      nil,
		"a log":                    []byte("login"),
		"a log and the byte":       []byte("login\x00"),
		"nine bytes":               []byte("\x00\x00\x00\x00\x00\x00\x00\x00\x00"),
		"no byte before a number":  []byte("login\x01\x00\x00\x00\x00\x00\x00\x00\x01"),
		"the byte of a gap":        []byte("login\x02\x00\x00\x00\x00\x00\x00\x00\x01"),
		"a log and a short number": []byte("login\x00\x00\x00\x00\x00\x00\x00\x01"),
	} {
		if log, n, ok := ParseChainBreakKey(key); ok {
			t.Errorf("%s: ParseChainBreakKey = %q, %d", name, log, n)
		}
	}
}

func TestDaemonEvents(t *testing.T) {
	var prev, hash [32]byte
	for i := range prev {
		prev[i], hash[i] = byte(i+1), byte(0xF0-i)
	}
	cutoff := time.Date(2025, 11, 23, 10, 20, 30, 123456789, time.UTC)

	if DeletedSize != 55 || AcceptedSize != 43 || HoleSize != 51 || GapSize != 51 || DeletedKind != 2 || AcceptedKind != 3 ||
		HoleKind != 4 || GapKind != 5 {
		t.Errorf("the sizes are %d, %d, %d and %d, the kinds %d, %d, %d and %d", DeletedSize, AcceptedSize, HoleSize, GapSize,
			DeletedKind, AcceptedKind, HoleKind, GapKind)
	}
	for log, code := range map[string]byte{LogLogin: 1, LogActivity: 2} {
		d := Deleted{Log: log, First: 0x0102030405060708, Prev: prev, Cutoff: cutoff}
		b := AppendDeleted(nil, d)
		want := append(append([]byte{0, 2, code, 1, 2, 3, 4, 5, 6, 7, 8}, prev[:]...), appendTime(nil, cutoff)...)
		if !bytes.Equal(b, want) || len(b) != DeletedSize || !IsDaemonEvent(b) || !ValidEvent(b) {
			t.Errorf("the deletion event of %s is %x, want %x", log, b, want)
		}
		if got, ok := ParseDeleted(b); !ok || got.Log != log || got.First != d.First || got.Prev != prev || !got.Cutoff.Equal(cutoff) || got.Cutoff.Location() != time.UTC {
			t.Errorf("ParseDeleted = %+v, %v, want %+v", got, ok, d)
		}
		if _, ok := ParseMark(b); ok {
			t.Error("a deletion event is read as a mark")
		}
		if _, ok := ParseAccepted(b); ok {
			t.Error("a deletion event is read as an acceptance")
		}
		if got := AppendDeleted([]byte("xy"), d); string(got[:2]) != "xy" || !bytes.Equal(got[2:], b) {
			t.Errorf("AppendDeleted does not keep dst: %x", got)
		}

		a := Accepted{Log: log, N: 0x0102030405060708, Hash: hash}
		b = AppendAccepted(nil, a)
		want = append([]byte{0, 3, code, 1, 2, 3, 4, 5, 6, 7, 8}, hash[:]...)
		if !bytes.Equal(b, want) || len(b) != AcceptedSize || !IsDaemonEvent(b) || !ValidEvent(b) {
			t.Errorf("the acceptance event of %s is %x, want %x", log, b, want)
		}
		if got, ok := ParseAccepted(b); !ok || got != a {
			t.Errorf("ParseAccepted = %+v, %v, want %+v", got, ok, a)
		}
		if _, ok := ParseMark(b); ok {
			t.Error("an acceptance event is read as a mark")
		}
		if _, ok := ParseDeleted(b); ok {
			t.Error("an acceptance event is read as a deletion")
		}
		if got := AppendAccepted([]byte("xy"), a); string(got[:2]) != "xy" || !bytes.Equal(got[2:], b) {
			t.Errorf("AppendAccepted does not keep dst: %x", got)
		}

		h := Hole{Log: log, First: 0x0102030405060708, Last: 0x1112131415161718, Hash: hash}
		b = AppendHole(nil, h)
		want = append([]byte{0, 4, code, 1, 2, 3, 4, 5, 6, 7, 8, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18}, hash[:]...)
		if !bytes.Equal(b, want) || len(b) != HoleSize || !IsDaemonEvent(b) || !ValidEvent(b) {
			t.Errorf("the hole event of %s is %x, want %x", log, b, want)
		}
		if got, ok := ParseHole(b); !ok || got != h {
			t.Errorf("ParseHole = %+v, %v, want %+v", got, ok, h)
		}
		if _, ok := ParseMark(b); ok {
			t.Error("a hole event is read as a mark")
		}
		if _, ok := ParseDeleted(b); ok {
			t.Error("a hole event is read as a deletion")
		}
		if _, ok := ParseAccepted(b); ok {
			t.Error("a hole event is read as an acceptance")
		}
		if got := AppendHole([]byte("xy"), h); string(got[:2]) != "xy" || !bytes.Equal(got[2:], b) {
			t.Errorf("AppendHole does not keep dst: %x", got)
		}

		g := Hole{Log: log, First: 0x0102030405060708, Last: 0x1112131415161718, Hash: hash}
		gb := AppendGap(nil, g)
		want = append([]byte{0, 5, code, 1, 2, 3, 4, 5, 6, 7, 8, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18}, hash[:]...)
		if !bytes.Equal(gb, want) || len(gb) != GapSize || !IsDaemonEvent(gb) || !ValidEvent(gb) {
			t.Errorf("the gap event of %s is %x, want %x", log, gb, want)
		}
		if got, ok := ParseGap(gb); !ok || got != g {
			t.Errorf("ParseGap = %+v, %v, want %+v", got, ok, g)
		}
		// A gap and a hole of the same fields differ in the kind alone, and no parser reads the other's.
		if !bytes.Equal(gb[2:], b[2:]) || gb[1] != GapKind || b[1] != HoleKind {
			t.Errorf("the gap event %x and the hole event %x differ in more than the kind", gb, b)
		}
		if got, ok := ParseHole(gb); ok {
			t.Errorf("ParseHole(a gap event) = %+v", got)
		}
		if got, ok := ParseGap(b); ok {
			t.Errorf("ParseGap(a hole event) = %+v", got)
		}
		if _, ok := ParseMark(gb); ok {
			t.Error("a gap event is read as a mark")
		}
		if _, ok := ParseDeleted(gb); ok {
			t.Error("a gap event is read as a deletion")
		}
		if _, ok := ParseAccepted(gb); ok {
			t.Error("a gap event is read as an acceptance")
		}
		if got := AppendGap([]byte("xy"), g); string(got[:2]) != "xy" || !bytes.Equal(got[2:], gb) {
			t.Errorf("AppendGap does not keep dst: %x", got)
		}
	}
	const goldenGapEvent = "0005" + "01" + "0000000000000002" + "0000000000000009" +
		"f0efeeedecebeae9e8e7e6e5e4e3e2e1e0dfdedddcdbdad9d8d7d6d5d4d3d2d1"
	if got := AppendGap(nil, Hole{Log: LogLogin, First: 2, Last: 9, Hash: hash}); hex.EncodeToString(got) != goldenGapEvent {
		t.Errorf("the gap event is %x, want %s", got, goldenGapEvent)
	}
	const goldenHoleEvent = "0004" + "02" + "0000000000000003" + "0000000000000004" +
		"f0efeeedecebeae9e8e7e6e5e4e3e2e1e0dfdedddcdbdad9d8d7d6d5d4d3d2d1"
	if got := AppendHole(nil, Hole{Log: LogActivity, First: 3, Last: 4, Hash: hash}); hex.EncodeToString(got) != goldenHoleEvent {
		t.Errorf("the hole event is %x, want %s", got, goldenHoleEvent)
	}

	goodDeleted := AppendDeleted(nil, Deleted{Log: LogLogin, First: 2, Prev: prev, Cutoff: cutoff})
	goodAccepted := AppendAccepted(nil, Accepted{Log: LogLogin, N: 1, Hash: hash})
	goodHole := AppendHole(nil, Hole{Log: LogLogin, First: 2, Last: 2, Hash: hash})
	goodGap := AppendGap(nil, Hole{Log: LogLogin, First: 2, Last: 2, Hash: hash})
	goodMark := AppendMark(nil, cutoff)
	with := func(b []byte, i int, v byte) []byte {
		b = bytes.Clone(b)
		b[i] = v
		return b
	}
	nanos := func(b []byte, n uint32) []byte {
		b = bytes.Clone(b)
		for i := range 4 {
			b[len(b)-1-i] = byte(n >> (8 * i))
		}
		return b
	}
	number := func(b []byte, n uint64) []byte {
		b = bytes.Clone(b)
		for i := range 8 {
			b[10-i] = byte(n >> (8 * i))
		}
		return b
	}
	for _, b := range [][]byte{goodMark, goodDeleted, goodAccepted, goodHole, goodGap, nanos(goodDeleted, 999999999), number(goodDeleted, 2),
		number(goodAccepted, math.MaxUint64), AppendHole(nil, Hole{Log: LogActivity, First: 1, Last: math.MaxUint64}),
		AppendGap(nil, Hole{Log: LogActivity, First: 1, Last: math.MaxUint64})} {
		if !ValidEvent(b) {
			t.Errorf("ValidEvent(%x) is false", b)
		}
	}

	// What none of the four parsers reads, and ValidEvent refuses.
	for name, b := range map[string][]byte{
		"nil":                     nil,
		"the byte of the daemon":  {0},
		"a kind and nothing else": {0, 2},
		"the kind 6":              {0, 6, 1, 0, 0, 0, 0, 0, 0, 0, 2},
		"the kind 6 of a gap":     with(goodGap, 1, 6),
		"the kind 4 of 11 bytes":  {0, 4, 1, 0, 0, 0, 0, 0, 0, 0, 2},
		"the kind 5 of 11 bytes":  {0, 5, 1, 0, 0, 0, 0, 0, 0, 0, 2},
		"a hole of 50 bytes":      goodHole[:50],
		"a hole of 52 bytes":      append(bytes.Clone(goodHole), 0),
		"a hole and a mark":       append(bytes.Clone(goodHole), goodMark...),
		"a gap of 50 bytes":       goodGap[:50],
		"a gap of 52 bytes":       append(bytes.Clone(goodGap), 0),
		"a gap and a mark":        append(bytes.Clone(goodGap), goodMark...),
		"the kind 0":              {0, 0},
		"the kind 255":            {0, 255},
		"a record":                []byte(`{"log":"login","n":1}`),
		"the first byte 1":        {1, 1},
		"a mark of 13 bytes":      goodMark[:13],
		"a mark of 15 bytes":      append(bytes.Clone(goodMark), 0),
		"a mark of 1e9 nanos":     nanos(goodMark, 1e9),
		"a mark with the kind 4":  with(goodMark, 1, 4),
		"a mark with the kind 5":  with(goodMark, 1, 5),
		"a mark with the kind 6":  with(goodMark, 1, 6),
	} {
		if ValidEvent(b) {
			t.Errorf("%s: ValidEvent is true for %x", name, b)
		}
	}
	for name, b := range map[string][]byte{
		"nil":                        nil,
		"54 bytes":                   goodDeleted[:54],
		"56 bytes":                   append(bytes.Clone(goodDeleted), 0),
		"a deletion and a mark":      append(bytes.Clone(goodDeleted), goodMark...),
		"with a prefix":              append([]byte{'x'}, goodDeleted...),
		"the log 0":                  with(goodDeleted, 2, 0),
		"the log 3":                  with(goodDeleted, 2, 3),
		"the log 255":                with(goodDeleted, 2, 255),
		"1e9 nanoseconds":            nanos(goodDeleted, 1e9),
		"all nanoseconds":            nanos(goodDeleted, math.MaxUint32),
		"the first record 0":         number(goodDeleted, 0),
		"the first record 1":         number(goodDeleted, 1),
		"the kind of a mark":         with(goodDeleted, 1, MarkKind),
		"the kind of an acceptance":  with(goodDeleted, 1, AcceptedKind),
		"the kind of a hole":         with(goodDeleted, 1, HoleKind),
		"the kind of a gap":          with(goodDeleted, 1, GapKind),
		"the first byte 1":           with(goodDeleted, 0, 1),
		"a mark of the size of one":  bytes.Repeat([]byte{'x'}, DeletedSize),
		"an acceptance of this size": append(bytes.Clone(goodAccepted), make([]byte, DeletedSize-AcceptedSize)...),
	} {
		if got, ok := ParseDeleted(b); ok {
			t.Errorf("%s: ParseDeleted = %+v", name, got)
		}
		if ValidEvent(b) && !bytes.Equal(b, goodMark) {
			t.Errorf("%s: ValidEvent is true", name)
		}
	}
	for name, b := range map[string][]byte{
		"nil":                       nil,
		"42 bytes":                  goodAccepted[:42],
		"44 bytes":                  append(bytes.Clone(goodAccepted), 0),
		"with a prefix":             append([]byte{'x'}, goodAccepted...),
		"the log 0":                 with(goodAccepted, 2, 0),
		"the log 3":                 with(goodAccepted, 2, 3),
		"record 0":                  number(goodAccepted, 0),
		"the kind of a deletion":    with(goodAccepted, 1, DeletedKind),
		"the kind of a hole":        with(goodAccepted, 1, HoleKind),
		"the kind of a gap":         with(goodAccepted, 1, GapKind),
		"the kind of a mark":        with(goodAccepted, 1, MarkKind),
		"the first byte 1":          with(goodAccepted, 0, 1),
		"a deletion of this size":   goodDeleted[:AcceptedSize],
		"a string of x of its size": bytes.Repeat([]byte{'x'}, AcceptedSize),
	} {
		if got, ok := ParseAccepted(b); ok {
			t.Errorf("%s: ParseAccepted = %+v", name, got)
		}
	}
	holeFirst := func(b []byte, n uint64) []byte {
		b = bytes.Clone(b)
		for i := range 8 {
			b[10-i] = byte(n >> (8 * i))
		}
		return b
	}
	holeLast := func(b []byte, n uint64) []byte {
		b = bytes.Clone(b)
		for i := range 8 {
			b[18-i] = byte(n >> (8 * i))
		}
		return b
	}
	for name, b := range map[string][]byte{
		"nil":                         nil,
		"50 bytes":                    goodHole[:50],
		"52 bytes":                    append(bytes.Clone(goodHole), 0),
		"with a prefix":               append([]byte{'x'}, goodHole...),
		"the log 0":                   with(goodHole, 2, 0),
		"the log 3":                   with(goodHole, 2, 3),
		"the log 255":                 with(goodHole, 2, 255),
		"the first record 0":          holeFirst(goodHole, 0),
		"the last record under first": holeLast(goodHole, 1),
		"the last record 0":           holeLast(goodHole, 0),
		"the kind of a deletion":      with(goodHole, 1, DeletedKind),
		"the kind of an acceptance":   with(goodHole, 1, AcceptedKind),
		"the kind of a mark":          with(goodHole, 1, MarkKind),
		"the kind of a gap":           with(goodHole, 1, GapKind),
		"the first byte 1":            with(goodHole, 0, 1),
		"an acceptance of this size":  append(bytes.Clone(goodAccepted), make([]byte, HoleSize-AcceptedSize)...),
		"a deletion of this size":     goodDeleted[:HoleSize],
		"a string of x of its size":   bytes.Repeat([]byte{'x'}, HoleSize),
	} {
		if got, ok := ParseHole(b); ok {
			t.Errorf("%s: ParseHole = %+v", name, got)
		}
	}
	// A hole is no deletion or acceptance, however it is padded or cut to their sizes.
	for _, b := range [][]byte{goodHole, append(bytes.Clone(goodHole), make([]byte, DeletedSize-HoleSize)...), goodHole[:AcceptedSize]} {
		if got, ok := ParseDeleted(b); ok {
			t.Errorf("ParseDeleted(a hole of %d bytes) = %+v", len(b), got)
		}
		if got, ok := ParseAccepted(b); ok {
			t.Errorf("ParseAccepted(a hole of %d bytes) = %+v", len(b), got)
		}
	}
	if got, ok := ParseHole(holeLast(holeFirst(goodHole, 7), 7)); !ok || got.First != 7 || got.Last != 7 {
		t.Errorf("a hole of the record 7: %+v, %v", got, ok)
	}
	if got := AppendHole(nil, Hole{Log: "other", First: 2, Last: 2}); got[2] != 0 || ValidEvent(got) {
		t.Errorf("a hole of a log that does not exist: %x", got)
	}
	if got := AppendDeleted(nil, Deleted{Log: "other", First: 2}); got[2] != 0 || ValidEvent(got) {
		t.Errorf("a deletion of a log that does not exist: %x", got)
	}
	if got := AppendAccepted(nil, Accepted{N: 1}); got[2] != 0 || ValidEvent(got) {
		t.Errorf("an acceptance of no log: %x", got)
	}
}

func TestParseHeadLine(t *testing.T) {
	hex64 := strings.Repeat("0123456789abcdef", 4)
	var hash [32]byte
	for i := range hash {
		hash[i] = byte(i * 7)
	}
	for _, c := range []struct {
		line string
		log  string
		n    uint64
	}{
		{"audit head login 1 sha256:" + hex64, "login", 1},
		{"audit head activity 12 sha256:" + hex64, "activity", 12},
		{"audit head activity 18446744073709551615 sha256:" + hex64, "activity", math.MaxUint64},
	} {
		log, n, h, ok := ParseHeadLine([]byte(c.line))
		if !ok || log != c.log || n != c.n || hex.EncodeToString(h[:]) != hex64 {
			t.Errorf("ParseHeadLine(%q) = %q, %d, %x, %v", c.line, log, n, h, ok)
		}
		if !validHeadLine([]byte(c.line)) {
			t.Errorf("%q is no valid row of the heads", c.line)
		}
	}
	line := "audit head login 3 sha256:" + hex.EncodeToString(hash[:])
	if log, n, h, ok := ParseHeadLine([]byte(line)); !ok || log != "login" || n != 3 || h != hash {
		t.Errorf("ParseHeadLine(%q) = %q, %d, %x, %v", line, log, n, h, ok)
	}
	for name, line := range map[string]string{
		"nil":                        "",
		"no prefix":                  "login 1 sha256:" + hex64,
		"a prefix of a logger":       "journal: audit head login 1 sha256:" + hex64,
		"a log that is not one":      "audit head other 1 sha256:" + hex64,
		"a log in upper case":        "audit head Login 1 sha256:" + hex64,
		"the number 0":               "audit head login 0 sha256:" + hex64,
		"a leading zero":             "audit head login 01 sha256:" + hex64,
		"a plus sign":                "audit head login +1 sha256:" + hex64,
		"a negative number":          "audit head login -1 sha256:" + hex64,
		"a number over 64 bits":      "audit head login 18446744073709551616 sha256:" + hex64,
		"a number that is no number": "audit head login x sha256:" + hex64,
		"hash digits in upper case":  "audit head login 1 sha256:" + strings.ToUpper(hex64),
		"a digit that is not hex":    "audit head login 1 sha256:" + hex64[:63] + "g",
		"63 digits":                  "audit head login 1 sha256:" + hex64[:63],
		"65 digits":                  "audit head login 1 sha256:" + hex64 + "0",
		"another algorithm":          "audit head login 1 sha512:" + hex64,
		"no algorithm":               "audit head login 1 " + hex64,
		"two spaces":                 "audit head login  1 sha256:" + hex64,
		"a tab":                      "audit head login\t1 sha256:" + hex64,
		"a space at the end":         "audit head login 1 sha256:" + hex64 + " ",
		"a newline at the end":       "audit head login 1 sha256:" + hex64 + "\n",
		"a field more":               "audit head login 1 sha256:" + hex64 + " x",
		"a field less":               "audit head login sha256:" + hex64,
		"the prefix in upper case":   "AUDIT HEAD login 1 sha256:" + hex64,
		"the prefix without a space": "audit headlogin 1 sha256:" + hex64,
		"a space before the prefix":  " audit head login 1 sha256:" + hex64,
	} {
		if log, n, h, ok := ParseHeadLine([]byte(line)); ok {
			t.Errorf("%s: ParseHeadLine(%q) = %q, %d, %x", name, line, log, n, h)
		}
	}
}

func TestChainHoleFollows(t *testing.T) {
	var hash, other [32]byte
	hash[0], other[0] = 1, 2
	text := "sha256:01" + strings.Repeat("00", 31)
	h := ChainHole{First: 3, Last: 5, Hash: hash}
	for _, c := range []struct {
		name string
		n    uint64
		prev string
		want bool
	}{
		{"the record after the hole", 6, text, true},
		{"the record Last", 5, text, false},
		{"two records after", 7, text, false},
		{"the record First", 3, text, false},
		{"the record 0", 0, text, false},
		{"another hash", 6, "sha256:02" + strings.Repeat("00", 31), false},
		{"no hash", 6, "", false},
		{"the hash in upper case", 6, strings.ToUpper(text), false},
		{"a hash without the algorithm", 6, text[len("sha256:"):], false},
	} {
		if got := h.Follows(c.n, c.prev); got != c.want {
			t.Errorf("%s: Follows(%d, %q) = %v, want %v", c.name, c.n, c.prev, got, c.want)
		}
	}
	// The largest hole: the record after it has no number, so none follows it.
	if (ChainHole{First: 1, Last: math.MaxUint64, Hash: hash}).Follows(0, text) {
		t.Error("the record 0 follows the hole that ends with the largest number")
	}
	if !(ChainHole{First: 1, Last: 1, Hash: other}).Follows(2, "sha256:02"+strings.Repeat("00", 31)) {
		t.Error("the record 2 does not follow the hole of the record 1")
	}
}

func TestHoleBefore(t *testing.T) {
	var h3, h6 [32]byte
	h3[0], h6[0] = 3, 6
	text := func(sum [32]byte) string { return "sha256:" + hex.EncodeToString(sum[:]) }
	holes := map[uint64]ChainHole{
		3: {First: 3, Last: 4, Hash: h3},
		6: {First: 6, Last: 6, Hash: h6},
		1: {First: 1, Last: 2, Hash: h3},
	}
	start := &ChainStart{First: 3}
	for _, c := range []struct {
		name  string
		start *ChainStart
		last  uint64
		n     uint64
		prev  string
		want  uint64 // the first record of the hole that is passed, 0 for none
	}{
		{"after the record before the hole", nil, 2, 5, text(h3), 3},
		{"a hole of one record", nil, 5, 7, text(h6), 6},
		{"before any record, from the record 1", nil, 0, 3, text(h3), 1},
		{"before any record, with the chain start at 3", start, 0, 5, text(h3), 3},
		{"before any record, with a chain start that is after the hole", &ChainStart{First: 5}, 0, 3, text(h3), 0},
		{"the record that the walk expects", nil, 2, 3, text(h3), 0},
		{"the record after the hole, with another hash", nil, 2, 5, text(h6), 0},
		{"the record after the hole, with no hash", nil, 2, 5, "", 0},
		{"two records after the hole", nil, 2, 6, text(h3), 0},
		{"the last record of the hole", nil, 2, 4, text(h3), 0},
		{"a walk that is not where a hole starts", nil, 3, 5, text(h3), 0},
		{"the first record of the walk, with the chain start at 3, is the chain start's", start, 0, 3, text(h3), 0},
	} {
		got, ok := HoleBefore(holes, c.start, c.last, c.n, c.prev)
		if want := c.want != 0; ok != want || ok && got.First != c.want {
			t.Errorf("%s: HoleBefore(last %d, n %d) = %+v, %v, want the hole at %d", c.name, c.last, c.n, got, ok, c.want)
		}
	}
	if _, ok := HoleBefore(nil, nil, 0, 5, text(h3)); ok {
		t.Error("a walk with no holes passes a record")
	}
	for _, c := range []struct {
		start *ChainStart
		last  uint64
		want  uint64
	}{{nil, 0, 1}, {start, 0, 3}, {nil, 4, 5}, {start, 4, 5}, {nil, 1, 2}} {
		if got := ChainExpected(c.start, c.last); got != c.want {
			t.Errorf("ChainExpected(%v, %d) = %d, want %d", c.start, c.last, got, c.want)
		}
	}
}
