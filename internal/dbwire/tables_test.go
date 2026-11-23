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
	if AppendOnly(TableChains) {
		t.Error("the table chains is append only: the maintenance of the daemon replaces its start rows")
	}
	if AppendOnly(math.MaxUint32) {
		t.Error("the table 2^32-1 is append only")
	}
	if TableVersion != 8 || TableMeta != 1 || MetaSchema != "schema" || TableHistory != 3 || TableRevisions != 4 ||
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

	if ChainStartSize != 52 || ChainBreakSize != 44 {
		t.Errorf("the sizes are %d and %d, want 52 and 44", ChainStartSize, ChainBreakSize)
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
	for name, key := range map[string][]byte{
		"nil":                      nil,
		"a log":                    []byte("login"),
		"a log and the byte":       []byte("login\x00"),
		"nine bytes":               []byte("\x00\x00\x00\x00\x00\x00\x00\x00\x00"),
		"no byte before a number":  []byte("login\x01\x00\x00\x00\x00\x00\x00\x00\x01"),
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

	if DeletedSize != 55 || AcceptedSize != 43 || DeletedKind != 2 || AcceptedKind != 3 {
		t.Errorf("the sizes are %d and %d, the kinds %d and %d", DeletedSize, AcceptedSize, DeletedKind, AcceptedKind)
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
	}

	goodDeleted := AppendDeleted(nil, Deleted{Log: LogLogin, First: 2, Prev: prev, Cutoff: cutoff})
	goodAccepted := AppendAccepted(nil, Accepted{Log: LogLogin, N: 1, Hash: hash})
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
	for _, b := range [][]byte{goodMark, goodDeleted, goodAccepted, nanos(goodDeleted, 999999999), number(goodDeleted, 2), number(goodAccepted, math.MaxUint64)} {
		if !ValidEvent(b) {
			t.Errorf("ValidEvent(%x) is false", b)
		}
	}

	// What none of the three parsers reads, and ValidEvent refuses.
	for name, b := range map[string][]byte{
		"nil":                     nil,
		"the byte of the daemon":  {0},
		"a kind and nothing else": {0, 2},
		"the kind 4":              {0, 4, 1, 0, 0, 0, 0, 0, 0, 0, 2},
		"the kind 0":              {0, 0},
		"the kind 255":            {0, 255},
		"a record":                []byte(`{"log":"login","n":1}`),
		"the first byte 1":        {1, 1},
		"a mark of 13 bytes":      goodMark[:13],
		"a mark of 15 bytes":      append(bytes.Clone(goodMark), 0),
		"a mark of 1e9 nanos":     nanos(goodMark, 1e9),
		"a mark with the kind 4":  with(goodMark, 1, 4),
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
		"the kind of a mark":        with(goodAccepted, 1, MarkKind),
		"the first byte 1":          with(goodAccepted, 0, 1),
		"a deletion of this size":   goodDeleted[:AcceptedSize],
		"a string of x of its size": bytes.Repeat([]byte{'x'}, AcceptedSize),
	} {
		if got, ok := ParseAccepted(b); ok {
			t.Errorf("%s: ParseAccepted = %+v", name, got)
		}
	}
	if got := AppendDeleted(nil, Deleted{Log: "other", First: 2}); got[2] != 0 || ValidEvent(got) {
		t.Errorf("a deletion of a log that does not exist: %x", got)
	}
	if got := AppendAccepted(nil, Accepted{N: 1}); got[2] != 0 || ValidEvent(got) {
		t.Errorf("an acceptance of no log: %x", got)
	}
}
