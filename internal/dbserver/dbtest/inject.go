package dbtest

import (
	"context"

	"github.com/WhiteRoomLab/whiteroom/internal/dbserver"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// Row is a row that Inject puts.
type Row struct {
	Table      uint32
	Key, Value []byte
}

// Inject puts the rows and appends the events in one commit of the engine of the daemon, past the rules of the daemon, as
// its maintenance does with what a client may not write (the table chains, the events of the daemon). It is for the tests
// of what that maintenance leaves in a store, which the tests of the packages that cannot open the engine cannot make.
func (h *Daemon) Inject(ctx context.Context, rows []Row, events [][]byte) error {
	//nolint:forbidigo // dbtest is a base for tests, which the rule is not for
	return dbserver.EngineOf(h.Server()).Update(ctx, func(tx *logdb.Tx) error {
		for _, r := range rows {
			if err := tx.Put(r.Table, r.Key, r.Value); err != nil {
				return err
			}
		}
		for _, e := range events {
			if err := tx.AppendEvent(e); err != nil {
				return err
			}
		}
		return nil
	})
}
