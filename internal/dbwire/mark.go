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
