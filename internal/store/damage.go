package store

import (
	"context"
	"fmt"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// DropIndexRow deletes the row of the current document of kind with id from summaries, and nothing else: the rows of
// references and the document stay, and no record is added. It fails if there is no such row. It is for the tests of
// other packages, which need a store that Check finds a problem in, and for no one else: a forbidigo rule of
// .golangci.yml checks that no file of the module but a test refers to it.
func DropIndexRow(ctx context.Context, s *Store, kind content.RefKind, id string) error {
	// Past Update, which would refuse the commit that has no record.
	return s.b.update(ctx, func(b backendTx) error {
		key := docKey(kind, id)
		switch _, ok, err := b.indexGet(indexSummaries, key); {
		case err != nil:
			return err
		case !ok:
			return fmt.Errorf("%s: no row of summaries", docLabel(kind, id))
		}
		return b.indexDelete(indexSummaries, key)
	})
}
