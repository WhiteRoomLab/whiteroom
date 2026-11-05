package logdb

import (
	"errors"
	"fmt"
)

// CorruptError reports damage in a file of the engine. Offset is that of the record in a segment, or -1 when the
// problem is in a whole file or its header.
type CorruptError struct {
	Path   string
	Offset int64
	Reason string
}

func (e *CorruptError) Error() string {
	if e.Offset < 0 {
		return e.Path + ": corrupt: " + e.Reason
	}
	return fmt.Sprintf("%s: corrupt at offset %d: %s", e.Path, e.Offset, e.Reason)
}

// VersionError reports a file of a format version newer than Known, which the engine refuses without changing a file.
type VersionError struct {
	Path           string
	Version, Known uint32
}

func (e *VersionError) Error() string {
	return fmt.Sprintf("%s: format version %d, newer than this program knows (%d)", e.Path, e.Version, e.Known)
}

var (
	// ErrFailed is the error of every Update after a Sync of the engine failed, and of Open and OpenReader when one of
	// their Syncs fails. The page cache may then show changes that never reach the disk, which a store opened again
	// before a restart of the machine would take as durable.
	ErrFailed   = errors.New("the store has stopped after a sync failed; restart the machine before opening it again")
	ErrReadOnly = errors.New("read-only transaction")

	errEnded  = errors.New("the transaction has ended")
	errClosed = errors.New("the store is closed")
)

func tooLarge(n int) error {
	return fmt.Errorf("the transaction is too large: %d bytes (max %d)", n, maxBody)
}
