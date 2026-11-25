package dbwire

import (
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// TableVersion is the table schema that this protocol version works with.
const TableVersion = 10

// The tables of internal/store that the daemon acts on without reading rows.
const (
	TableMeta           uint32 = 1
	TableHistory        uint32 = 3
	TableRevisions      uint32 = 4
	TableEntryRevisions uint32 = 5

	// TableHeads has the name of an audit log as the key and the line of its head as the value, which the daemon
	// writes out as it is.
	TableHeads uint32 = 11

	// TableChains has the chain starts, the accepted breaks, the holes and the accepted gaps of the audit logs (ChainStart,
	// ChainBreak, and ChainHole, which a hole and a gap both use), which only the maintenance of the daemon writes: the daemon
	// refuses a Commit that puts or deletes one of its rows.
	TableChains uint32 = 12
)

// The names of the audit logs in the keys of TableChains.
const (
	LogLogin    = "login"
	LogActivity = "activity"
)

// MetaSchema is the key of the schema in TableMeta; its value is a uvarint.
const MetaSchema = "schema"

// The bounds of a row of TableHeads, in bytes.
const (
	MaxHeadKey  = 64
	MaxHeadLine = 255
)

// AppendOnly reports whether the daemon refuses to change or delete a row of table.
func AppendOnly(table uint32) bool {
	return table == TableHistory || table == TableRevisions || table == TableEntryRevisions
}

// ValidHeadRow reports whether key and value may be a row of TableHeads.
func ValidHeadRow(key, value []byte) bool {
	if len(key) < 1 || len(key) > MaxHeadKey {
		return false
	}
	for _, c := range key {
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return validHeadLine(value)
}

func validHeadLine(line []byte) bool {
	if len(line) < 1 || len(line) > MaxHeadLine {
		return false
	}
	for _, c := range line {
		if c < 0x20 || c > 0x7E {
			return false
		}
	}
	return true
}

// ParseHeadLine returns the log, the number and the hash of the head that line tells, in the form that audit.Head.String
// writes and no other: "audit head ", the log (login or activity), the number from 1 in decimal digits without a leading
// zero, "sha256:" and 64 lower case hexadecimal digits, each after one space.
func ParseHeadLine(line []byte) (log string, n uint64, hash [32]byte, ok bool) {
	rest, found := strings.CutPrefix(string(line), "audit head ")
	if !found {
		return "", 0, hash, false
	}
	fields := strings.Split(rest, " ")
	if len(fields) != 3 || fields[0] != LogLogin && fields[0] != LogActivity {
		return "", 0, hash, false
	}
	n, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil || n < 1 || strconv.FormatUint(n, 10) != fields[1] {
		return "", 0, hash, false
	}
	digits, found := strings.CutPrefix(fields[2], "sha256:")
	if !found || len(digits) != 2*len(hash) || strings.Trim(digits, "0123456789abcdef") != "" {
		return "", 0, hash, false
	}
	if _, err := hex.Decode(hash[:], []byte(digits)); err != nil {
		return "", 0, [32]byte{}, false
	}
	return fields[0], n, hash, true
}

// ChainExpected returns the number of the record that a walk of a log expects next, after the record numbered last, which is 0
// if the walk has read none: the record after it, or, before any, the first that remains after a deletion (start.First), or 1.
func ChainExpected(start *ChainStart, last uint64) uint64 {
	switch {
	case last != 0:
		return last + 1
	case start != nil:
		return start.First
	}
	return 1
}

// HoleBefore returns the hole of holes, by their first records, that the record n with the prev text prev follows in a walk
// of a log that has read the record numbered last (0 if none) and has the chain start start (nil if none): the hole that has
// the record that the walk expects as its first, and that ChainHole.Follows says n follows. It is the one rule by which
// VerifyAudit of the store, whiteroom-db compact and accept-break pass a record through a hole.
func HoleBefore(holes map[uint64]ChainHole, start *ChainStart, last, n uint64, prev string) (ChainHole, bool) {
	h, ok := holes[ChainExpected(start, last)]
	return h, ok && h.Follows(n, prev)
}

// ChainStart is the row of a log in TableChains, with the log name as its key: the deletion of the oldest records of
// the log has left First as its first record, and Prev is the SHA-256 of the stored bytes of the record before it.
type ChainStart struct {
	First uint64
	Prev  [32]byte
	At    time.Time
}

// ChainBreak is a row of TableChains, with ChainBreakKey as its key: the break at the record of that number is accepted
// as long as the SHA-256 of the stored bytes of that record is Hash.
type ChainBreak struct {
	Hash [32]byte
	At   time.Time
}

// ChainHole is a row of TableChains, with ChainHoleKey as its key: the records of a log from First, the number in the key,
// to Last were lost and dropped by a repair. Hash is the SHA-256 of the stored bytes of the record Last, which is the prev of
// the record after the hole, or, for a hole at the end of the log, the hash of its head. The record after it follows the hole
// as if it were the record Last. The row of an accepted gap (ChainGapKey) has the same value.
type ChainHole struct {
	First, Last uint64
	Hash        [32]byte
	At          time.Time
}

// The sizes of the values of TableChains, in bytes.
const (
	ChainStartSize = 8 + 32 + 8 + 4
	ChainBreakSize = 32 + 8 + 4
	ChainHoleSize  = 8 + 32 + 8 + 4
)

// ChainStartKey returns the key of the chain start of the log named log.
func ChainStartKey(log string) []byte { return []byte(log) }

// ChainBreakKey returns the key of the accepted break at record n of the log named log: the name, 0x00 and n as 8 bytes,
// big-endian. The keys of the breaks of a log start with ChainBreakPrefix.
func ChainBreakKey(log string, n uint64) []byte {
	return binary.BigEndian.AppendUint64(ChainBreakPrefix(log), n)
}

// ChainBreakPrefix returns what the keys of the accepted breaks of the log named log start with.
func ChainBreakPrefix(log string) []byte { return append([]byte(log), 0) }

// ParseChainBreakKey returns the log and the record number of the key of an accepted break.
func ParseChainBreakKey(key []byte) (log string, n uint64, ok bool) {
	if len(key) < 10 || key[len(key)-9] != 0 {
		return "", 0, false
	}
	return string(key[:len(key)-9]), binary.BigEndian.Uint64(key[len(key)-8:]), true
}

// ChainHoleKey returns the key of the hole of the log named log that starts at record first: the name, 0x01 and first as 8
// bytes, big-endian. The keys of the holes of a log start with ChainHolePrefix.
func ChainHoleKey(log string, first uint64) []byte {
	return binary.BigEndian.AppendUint64(ChainHolePrefix(log), first)
}

// ChainHolePrefix returns what the keys of the holes of the log named log start with.
func ChainHolePrefix(log string) []byte { return append([]byte(log), 1) }

// ParseChainHoleKey returns the log and the first record of the key of a hole. It refuses a first of 0.
func ParseChainHoleKey(key []byte) (log string, first uint64, ok bool) {
	return parseChainRangeKey(key, 1)
}

// ChainGapKey returns the key of the gap of the log named log that starts at record first, which whiteroom-db accept-break
// accepts: the name, 0x02 and first as 8 bytes, big-endian. The keys of the gaps of a log start with ChainGapPrefix. The
// value is that of a hole (AppendChainHole, ParseChainHole): First, the number in the key, is the first record that is
// missing, Last the last one, and Hash the SHA-256 of the stored bytes of the record Last, which is the prev that the record
// after the gap has, or, for a gap at the end of the log, the hash of its head.
func ChainGapKey(log string, first uint64) []byte {
	return binary.BigEndian.AppendUint64(ChainGapPrefix(log), first)
}

// ChainGapPrefix returns what the keys of the accepted gaps of the log named log start with.
func ChainGapPrefix(log string) []byte { return append([]byte(log), 2) }

// ParseChainGapKey returns the log and the first record of the key of an accepted gap. It refuses a first of 0.
func ParseChainGapKey(key []byte) (log string, first uint64, ok bool) {
	return parseChainRangeKey(key, 2)
}

// parseChainRangeKey reads the key of a row that has a first record in it: the name, the byte kind and first.
func parseChainRangeKey(key []byte, kind byte) (log string, first uint64, ok bool) {
	if len(key) < 10 || key[len(key)-9] != kind {
		return "", 0, false
	}
	first = binary.BigEndian.Uint64(key[len(key)-8:])
	return string(key[:len(key)-9]), first, first != 0
}

// AppendChainStart appends the value of the chain start c to dst: First as a uint64, Prev, and the seconds since
// 1970-01-01T00:00:00Z as an int64 and the nanoseconds as a uint32 of At, all big-endian.
func AppendChainStart(dst []byte, c ChainStart) []byte {
	dst = binary.BigEndian.AppendUint64(dst, c.First)
	dst = append(dst, c.Prev[:]...)
	return appendTime(dst, c.At)
}

// ParseChainStart returns the chain start that b is the value of. It refuses a First under 2: the deletion of a prefix
// leaves a record that has one before it.
func ParseChainStart(b []byte) (ChainStart, bool) {
	if len(b) != ChainStartSize {
		return ChainStart{}, false
	}
	c := ChainStart{First: binary.BigEndian.Uint64(b)}
	copy(c.Prev[:], b[8:])
	at, ok := parseTime(b[40:])
	if !ok || c.First < 2 {
		return ChainStart{}, false
	}
	c.At = at
	return c, true
}

// AppendChainBreak appends the value of the accepted break c to dst: Hash, and At as AppendChainStart has it.
func AppendChainBreak(dst []byte, c ChainBreak) []byte {
	return appendTime(append(dst, c.Hash[:]...), c.At)
}

// ParseChainBreak returns the accepted break that b is the value of.
func ParseChainBreak(b []byte) (ChainBreak, bool) {
	if len(b) != ChainBreakSize {
		return ChainBreak{}, false
	}
	var c ChainBreak
	copy(c.Hash[:], b)
	at, ok := parseTime(b[32:])
	if !ok {
		return ChainBreak{}, false
	}
	c.At = at
	return c, true
}

// AppendChainHole appends the value of the hole c to dst, which has First in its key: Last as a uint64, Hash, and At as
// AppendChainStart has it.
func AppendChainHole(dst []byte, c ChainHole) []byte {
	dst = binary.BigEndian.AppendUint64(dst, c.Last)
	dst = append(dst, c.Hash[:]...)
	return appendTime(dst, c.At)
}

// ParseChainHole returns the hole that b is the value of, with first, the number of its key. It refuses a first of 0 and a
// Last under first.
func ParseChainHole(first uint64, b []byte) (ChainHole, bool) {
	if len(b) != ChainHoleSize {
		return ChainHole{}, false
	}
	c := ChainHole{First: first, Last: binary.BigEndian.Uint64(b)}
	copy(c.Hash[:], b[8:])
	at, ok := parseTime(b[40:])
	if !ok || c.First == 0 || c.Last < c.First {
		return ChainHole{}, false
	}
	c.At = at
	return c, true
}

// Follows reports whether the record n with the prev text prev, which a walk of a log meets when the record that it expects is
// First, follows the hole c: it is the one after Last, and its prev is the hash Hash. No more is passed than the hole records.
func (c ChainHole) Follows(n uint64, prev string) bool {
	return n > c.Last && n-1 == c.Last && prev == "sha256:"+hex.EncodeToString(c.Hash[:])
}

// appendTime appends the seconds of t as an int64 and its nanoseconds as a uint32, big-endian.
func appendTime(dst []byte, t time.Time) []byte {
	sec, nsec := markFields(t)
	dst = binary.BigEndian.AppendUint64(dst, uint64(sec))
	return binary.BigEndian.AppendUint32(dst, nsec)
}

// parseTime reads the 12 bytes that appendTime writes, as a time in UTC.
func parseTime(b []byte) (time.Time, bool) {
	return markTime(int64(binary.BigEndian.Uint64(b)), binary.BigEndian.Uint32(b[8:]))
}
