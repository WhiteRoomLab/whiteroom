package store

import (
	"context"
	"fmt"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// DropIndexRow deletes the row of the current document of kind with id from summaries, and nothing else of the index:
// the rows of references and the document stay. It adds the record store.write by the system, as every Update that
// writes does. It fails if there is no such row. It is for the tests of other packages, which need a store that Check
// finds a problem in, and for no one else: a forbidigo rule of .golangci.yml checks that no file of the module but a
// test refers to it.
func DropIndexRow(ctx context.Context, s *Store, kind content.RefKind, id string) error {
	return s.Update(ctx, func(t *Tx) error {
		key := docKey(kind, id)
		switch _, ok, err := t.b.indexGet(indexSummaries, key); {
		case err != nil:
			return err
		case !ok:
			return fmt.Errorf("%s: no row of summaries", docLabel(kind, id))
		}
		if err := t.b.indexDelete(indexSummaries, key); err != nil {
			return err
		}
		return t.Record(audit.Record{Log: audit.ActivityLog, Actor: audit.System, Action: audit.StoreWrite})
	})
}
