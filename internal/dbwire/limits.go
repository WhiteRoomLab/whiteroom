package dbwire

import "time"

// MaxKey is the largest key of the engine, in bytes.
const MaxKey = 4096

// A window is the reads (Get, Scan, Object) that a client sends on a connection before it reads their replies: at most
// WindowRequests requests of at most WindowBytes bytes together as they are sent. WindowBytes is the size of the buffer
// that a Reader reads its stream into, so the daemon takes a window with one read, whatever the buffers of the
// connection are.
const (
	ReadBuffer     = 32 << 10
	WindowRequests = 256
	WindowBytes    = ReadBuffer
)

// Limits are the limits that the daemon keeps, and tells its clients in the reply to a Hello. A zero field is the
// default; a negative SnapshotIdle, Stall, CommitsPerSecond, MinFree or MinFreeInodes turns that limit off.
type Limits struct {
	// Frame is the largest payload of a frame.
	Frame int
	// Commit bounds the bytes of the ops of a Commit as they are sent: the sum of the op entries of the body, without
	// the expected seq and the end byte.
	Commit int64
	// Object is the largest object or original.
	Object int64
	// Staged bounds the sum of the sizes of the objects that a writer has put since its Snapshot.
	Staged           int64
	Readers          int
	SnapshotIdle     time.Duration
	Stall            time.Duration
	CommitsPerSecond int
	MinFree          int64
	MinFreeInodes    int64
}

// WithDefaults returns l with each zero field replaced by its default. A negative field stays as it is.
func (l Limits) WithDefaults() Limits {
	if l.Frame == 0 {
		l.Frame = 1 << 20
	}
	if l.Commit == 0 {
		l.Commit = 268435446
	}
	if l.Object == 0 {
		l.Object = 64 << 20
	}
	if l.Staged == 0 {
		l.Staged = 268435446
	}
	if l.Readers == 0 {
		l.Readers = 64
	}
	if l.SnapshotIdle == 0 {
		l.SnapshotIdle = 5 * time.Minute
	}
	if l.Stall == 0 {
		l.Stall = 30 * time.Second
	}
	if l.CommitsPerSecond == 0 {
		l.CommitsPerSecond = 50
	}
	if l.MinFree == 0 {
		l.MinFree = 1 << 30
	}
	if l.MinFreeInodes == 0 {
		l.MinFreeInodes = 1 << 16
	}
	return l
}
