package dbwire

import (
	"encoding/binary"
	"time"
)

// The events that the daemon adds to the log start with DaemonEvent. An audit record is JSON and starts with '{', and
// the daemon refuses an event of a writer that is empty or starts with DaemonEvent.
const (
	DaemonEvent byte = 0x00
	MarkKind    byte = 0x01
	MarkSize         = 14

	// DeletedKind is the event of the deletion of the oldest records of a log, AcceptedKind that of the acceptance of a
	// break in a log, HoleKind that of a hole that a repair has made in a log, and GapKind that of the acceptance of a gap
	// in a log, which the maintenance of the daemon adds with the rows of TableChains that it writes.
	DeletedKind  byte = 0x02
	AcceptedKind byte = 0x03
	HoleKind     byte = 0x04
	GapKind      byte = 0x05
	DeletedSize       = 3 + 8 + 32 + 12
	AcceptedSize      = 3 + 8 + 32
	HoleSize          = 3 + 8 + 8 + 32
	GapSize           = HoleSize
)

// The byte that names a log in the events DeletedKind, AcceptedKind, HoleKind and GapKind.
const (
	logLoginByte    byte = 1
	logActivityByte byte = 2
)

// AppendMark appends the mark of the commit at t to dst: DaemonEvent, MarkKind, the seconds since 1970-01-01T00:00:00Z
// as an int64 and the nanoseconds as a uint32, both big-endian.
func AppendMark(dst []byte, t time.Time) []byte {
	sec, nsec := markFields(t)
	dst = append(dst, DaemonEvent, MarkKind)
	dst = binary.BigEndian.AppendUint64(dst, uint64(sec))
	return binary.BigEndian.AppendUint32(dst, nsec)
}

// ParseMark returns the time of a mark, in UTC.
func ParseMark(b []byte) (time.Time, bool) {
	if len(b) != MarkSize || b[0] != DaemonEvent || b[1] != MarkKind {
		return time.Time{}, false
	}
	return markTime(int64(binary.BigEndian.Uint64(b[2:])), binary.BigEndian.Uint32(b[10:]))
}

// Deleted is the event of the deletion of the records of the log named Log before First, the first that remains: Prev is
// the SHA-256 of the stored bytes of the record before it, and Cutoff the time that the records are older than.
type Deleted struct {
	Log    string
	First  uint64
	Prev   [32]byte
	Cutoff time.Time
}

// Accepted is the event of the acceptance of the break at record N of the log named Log, whose stored bytes have the
// SHA-256 Hash.
type Accepted struct {
	Log  string
	N    uint64
	Hash [32]byte
}

// Hole is the event of the hole of the records First to Last of the log named Log, whose last, the record Last, has the
// SHA-256 Hash of its stored bytes, which a repair has dropped. The event of an accepted gap (AppendGap) has the same fields.
type Hole struct {
	Log         string
	First, Last uint64
	Hash        [32]byte
}

// AppendDeleted appends the event d to dst: DaemonEvent, DeletedKind, the log as a byte (1 login, 2 activity; 0 for a
// name that is neither), First as a uint64, Prev, and the Cutoff as AppendMark has its time.
func AppendDeleted(dst []byte, d Deleted) []byte {
	dst = append(dst, DaemonEvent, DeletedKind, logByte(d.Log))
	dst = binary.BigEndian.AppendUint64(dst, d.First)
	dst = append(dst, d.Prev[:]...)
	return appendTime(dst, d.Cutoff)
}

// ParseDeleted returns the event that b is. It refuses a First under 2.
func ParseDeleted(b []byte) (Deleted, bool) {
	if len(b) != DeletedSize || b[0] != DaemonEvent || b[1] != DeletedKind {
		return Deleted{}, false
	}
	log, ok := logName(b[2])
	if !ok {
		return Deleted{}, false
	}
	d := Deleted{Log: log, First: binary.BigEndian.Uint64(b[3:])}
	copy(d.Prev[:], b[11:])
	cutoff, ok := parseTime(b[43:])
	if !ok || d.First < 2 {
		return Deleted{}, false
	}
	d.Cutoff = cutoff
	return d, true
}

// AppendAccepted appends the event a to dst: DaemonEvent, AcceptedKind, the log as AppendDeleted has it, N as a uint64
// and Hash.
func AppendAccepted(dst []byte, a Accepted) []byte {
	dst = append(dst, DaemonEvent, AcceptedKind, logByte(a.Log))
	dst = binary.BigEndian.AppendUint64(dst, a.N)
	return append(dst, a.Hash[:]...)
}

// ParseAccepted returns the event that b is. It refuses an N of 0.
func ParseAccepted(b []byte) (Accepted, bool) {
	if len(b) != AcceptedSize || b[0] != DaemonEvent || b[1] != AcceptedKind {
		return Accepted{}, false
	}
	log, ok := logName(b[2])
	if !ok {
		return Accepted{}, false
	}
	a := Accepted{Log: log, N: binary.BigEndian.Uint64(b[3:])}
	copy(a.Hash[:], b[11:])
	if a.N == 0 {
		return Accepted{}, false
	}
	return a, true
}

// AppendHole appends the event h to dst: DaemonEvent, HoleKind, the log as AppendDeleted has it, First and Last as uint64s
// and Hash.
func AppendHole(dst []byte, h Hole) []byte { return appendRange(dst, HoleKind, h) }

// ParseHole returns the event that b is. It refuses a First of 0 and a Last under First.
func ParseHole(b []byte) (Hole, bool) { return parseRange(b, HoleKind) }

// AppendGap appends the event of the acceptance of the gap h to dst: as AppendHole, with GapKind.
func AppendGap(dst []byte, h Hole) []byte { return appendRange(dst, GapKind, h) }

// ParseGap returns the event that b is. It refuses what ParseHole refuses and any other kind.
func ParseGap(b []byte) (Hole, bool) { return parseRange(b, GapKind) }

func appendRange(dst []byte, kind byte, h Hole) []byte {
	dst = append(dst, DaemonEvent, kind, logByte(h.Log))
	dst = binary.BigEndian.AppendUint64(dst, h.First)
	dst = binary.BigEndian.AppendUint64(dst, h.Last)
	return append(dst, h.Hash[:]...)
}

func parseRange(b []byte, kind byte) (Hole, bool) {
	if len(b) != HoleSize || b[0] != DaemonEvent || b[1] != kind {
		return Hole{}, false
	}
	log, ok := logName(b[2])
	if !ok {
		return Hole{}, false
	}
	h := Hole{Log: log, First: binary.BigEndian.Uint64(b[3:]), Last: binary.BigEndian.Uint64(b[11:])}
	copy(h.Hash[:], b[19:])
	if h.First == 0 || h.Last < h.First {
		return Hole{}, false
	}
	return h, true
}

// ValidEvent reports whether b is an event that the daemon adds: a mark, or a DeletedKind, AcceptedKind, HoleKind or
// GapKind event whose fields are valid.
func ValidEvent(b []byte) bool {
	if len(b) < 2 || b[0] != DaemonEvent {
		return false
	}
	switch b[1] {
	case MarkKind:
		_, ok := ParseMark(b)
		return ok
	case DeletedKind:
		_, ok := ParseDeleted(b)
		return ok
	case AcceptedKind:
		_, ok := ParseAccepted(b)
		return ok
	case HoleKind:
		_, ok := ParseHole(b)
		return ok
	case GapKind:
		_, ok := ParseGap(b)
		return ok
	}
	return false
}

func logByte(name string) byte {
	switch name {
	case LogLogin:
		return logLoginByte
	case LogActivity:
		return logActivityByte
	}
	return 0
}

func logName(b byte) (string, bool) {
	switch b {
	case logLoginByte:
		return LogLogin, true
	case logActivityByte:
		return LogActivity, true
	}
	return "", false
}

// IsDaemonEvent reports whether b is an event of the daemon.
func IsDaemonEvent(b []byte) bool {
	return len(b) > 0 && b[0] == DaemonEvent
}

func markFields(t time.Time) (sec int64, nsec uint32) {
	return t.Unix(), uint32(t.Nanosecond())
}

// markTime returns the time at sec and nsec, in UTC, if nsec is a number of nanoseconds in a second.
func markTime(sec int64, nsec uint32) (time.Time, bool) {
	if nsec >= 1e9 {
		return time.Time{}, false
	}
	return time.Unix(sec, int64(nsec)).UTC(), true
}
