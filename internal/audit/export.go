package audit

import (
	"context"
	"fmt"
	"io"
	"time"
)

// HeadSource is where Export reads the heads from: the Content Store (internal/store) is one.
type HeadSource interface {
	AuditHeads(ctx context.Context) ([]Head, error)
}

// exportTimeout is how long the last write of Export may take after ctx has ended.
const exportTimeout = 5 * time.Second

// Export keeps the heads of the audit logs outside the store (7.4): it writes them to w, such as the standard error of
// the process, a line each (Head.String, which holds no personal information). Whoever rewrites the store can make its
// chains and its table agree, but cannot change what w has kept. Export writes the heads of the logs that have records
// when it begins, and at each tick of ticks those that have changed since it wrote them. After ctx ends it writes once
// more, with a context of 5 seconds that the end of ctx does not cancel, and returns the error of that, which onError has
// had too: nil if it has worked, or had nothing to write.
//
// The errors of the reads and of the writes go to onError (nil: dropped), and the heads that were not written are tried
// again at the next tick. A line is one call of Write, and Export does not interrupt a Write that blocks. A closed ticks
// never fires.
func Export(ctx context.Context, src HeadSource, w io.Writer, ticks <-chan time.Time, onError func(error)) error {
	e := &exporter{src: src, w: w, written: make(map[Log]Head)}
	round := func() {
		if ctx.Err() != nil {
			return // the write at the end follows
		}
		if err := e.write(ctx); err != nil && ctx.Err() == nil && onError != nil {
			onError(err)
		}
	}
	round()
	for {
		select {
		case <-ctx.Done():
			last, cancel := context.WithTimeout(context.WithoutCancel(ctx), exportTimeout)
			defer cancel()
			err := e.write(last)
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

// exporter is what Export has written: the heads, by log.
type exporter struct {
	src     HeadSource
	w       io.Writer
	written map[Log]Head
}

// write writes the heads that have changed since they were written, and stops at the first error, after which it tries
// again with the heads that are left.
func (e *exporter) write(ctx context.Context) error {
	heads, err := e.src.AuditHeads(ctx)
	if err != nil {
		return fmt.Errorf("cannot read the heads of the audit logs: %w", err)
	}
	for _, h := range heads {
		if last, ok := e.written[h.Log]; ok && last == h {
			continue
		}
		if _, err := io.WriteString(e.w, h.String()+"\n"); err != nil {
			return fmt.Errorf("cannot write the head of the %s log: %w", h.Log, err)
		}
		e.written[h.Log] = h
	}
	return nil
}
