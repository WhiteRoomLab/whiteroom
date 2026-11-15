package dbserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// maxHeadRows is the most rows that the table of the heads has: a Commit that would leave more is refused, so that what
// ExportHeads writes at each start and tick is bounded, whatever a writer puts there.
const maxHeadRows = 16

// exportTimeout is how long the last write of ExportHeads may take after its context has ended.
const exportTimeout = 5 * time.Second

type headRow struct{ key, value string }

// headRows returns the rows of the table of the heads in a View, and the sequence number of the state that they are of.
func (s *Server) headRows(ctx context.Context) (seq uint64, rows []headRow, err error) {
	err = s.db.View(ctx, func(tx *logdb.Tx) error {
		seq = tx.Seq()
		tx.Scan(dbwire.TableHeads, nil, func(key, value []byte) bool {
			rows = append(rows, headRow{string(key), string(value)})
			return len(rows) <= maxHeadRows
		})
		return nil
	})
	if err == nil && len(rows) > maxHeadRows {
		return 0, nil, sentence("the table of the heads has more than %d rows", maxHeadRows)
	}
	return seq, rows, err
}

// ExportHeads keeps the heads of the audit logs outside the store (7.4): it writes the value of each row of the table of
// the heads to w, such as the standard error of the daemon, and a newline, as one call of Write. It writes all the rows
// when it begins, and at each tick those whose value has changed since it wrote them. After ctx ends it writes once more,
// with a context of 5 seconds that the end of ctx does not cancel, and returns the error of that, which onError has had
// too: nil if it has worked, or had nothing to write.
//
// The errors of the reads and of the writes go to onError (nil: dropped), and the rows that were not written are tried
// again at the next tick. ExportHeads does not interrupt a Write that blocks. A closed ticks never fires. The server
// does not read the lines beyond their shape: a row that is not a head line is not written, and onError has it once.
func (s *Server) ExportHeads(ctx context.Context, w io.Writer, ticks <-chan time.Time, onError func(error)) error {
	written := map[string]string{}
	write := func(ctx context.Context) error {
		_, rows, err := s.headRows(ctx)
		if err != nil {
			return fmt.Errorf("cannot read the heads: %s", safeText(err))
		}
		invalid := false
		for _, r := range rows {
			if last, ok := written[r.key]; ok && last == r.value {
				continue
			}
			if !dbwire.ValidHeadRow([]byte(r.key), []byte(r.value)) {
				written[r.key], invalid = r.value, true
				continue
			}
			if _, err := io.WriteString(w, r.value+"\n"); err != nil {
				return fmt.Errorf("cannot write a head: %w", err)
			}
			written[r.key] = r.value
		}
		if invalid {
			return errors.New("a row of the table of the heads is not a head line, and is not written")
		}
		return nil
	}
	round := func() {
		if ctx.Err() != nil {
			return // the write at the end follows
		}
		if err := write(ctx); err != nil && ctx.Err() == nil && onError != nil {
			onError(err)
		}
	}
	round()
	for {
		select {
		case <-ctx.Done():
			last, cancel := context.WithTimeout(context.WithoutCancel(ctx), exportTimeout)
			defer cancel()
			err := write(last)
			if err != nil && onError != nil {
				onError(err)
			}
			return err
		case _, ok := <-ticks:
			if !ok {
				ticks = nil
				continue
			}
			round()
		}
	}
}
