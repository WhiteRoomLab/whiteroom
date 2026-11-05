package store

import (
	"context"
	"errors"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// Audit events are a part of the PoC of #63 (3.5) that only the log backend has, and not a part of the API of the
// Content Store.

var errNoEvents = errors.New("audit events need the log backend")

// appendEvent appends an audit event with data in the Update of t: the event is there if and only if the other writes
// of the Update are.
func (t *Tx) appendEvent(data []byte) error {
	if err := t.check(true); err != nil {
		return err
	}
	return t.b.addEvent(data)
}

// events calls fn with the number and the data of each audit event of s, in order, until fn returns false. A store
// that openLogReader has opened first reads the commits that have become durable, as a View does.
func (s *Store) events(ctx context.Context, fn func(number uint64, data []byte) bool) error {
	b, ok := s.b.(*logBackend)
	if !ok {
		return errNoEvents
	}
	return b.db.Events(ctx, func(e logdb.Event) bool { return fn(e.Number, e.Data) })
}
