package dbwire

import (
	"encoding/binary"
	"time"
)

// TableVersion is the table schema that this protocol version works with.
const TableVersion = 8

// The tables of internal/store that the daemon acts on without reading rows.
const (
	TableMeta           uint32 = 1
	TableHistory        uint32 = 3
	TableRevisions      uint32 = 4
	TableEntryRevisions uint32 = 5

	// TableHeads has the name of an audit log as the key and the line of its head as the value, which the daemon
	// writes out as it is.
	TableHeads uint32 = 11

	// TableChains has the chain starts and the accepted breaks of the audit logs (ChainStart, ChainBreak), which only the
	// maintenance of the daemon writes: the daemon refuses a Commit that puts or deletes one of its rows.
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

// The sizes of the values of TableChains, in bytes.
const (
	ChainStartSize = 8 + 32 + 8 + 4
	ChainBreakSize = 32 + 8 + 4
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
