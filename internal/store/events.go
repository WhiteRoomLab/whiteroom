package store

import (
	"context"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// Audit events are not a part of the API of the Content Store yet: #47 makes that of the audit log from them (3.5).

// appendEvent appends an audit event with data in the Update of t: the event is there if and only if the other writes
// of the Update are.
func (t *Tx) appendEvent(data []byte) error {
	if err := t.check(true); err != nil {
		return err
	}
	return t.b.addEvent(data)
}

// events calls fn with the number and the data of each audit event of s, in order, until fn returns false. A store
// that OpenReader has opened first reads the commits that have become durable, as a View does.
func (s *Store) events(ctx context.Context, fn func(number uint64, data []byte) bool) error {
	return s.b.events(ctx, fn)
}

func (b *logBackend) events(ctx context.Context, fn func(number uint64, data []byte) bool) error {
	return b.db.Events(ctx, func(e logdb.Event) bool { return fn(e.Number, e.Data) })
}
