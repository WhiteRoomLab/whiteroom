package dbserver

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// lineWriter is the writer of the tests of ExportHeads: it keeps what each call of Write has, and fails when fail says so.
type lineWriter struct {
	mu    sync.Mutex
	lines []string
	fail  func(lines int) error // nil: never; else the error of the call that comes after that many lines
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fail != nil {
		if err := w.fail(len(w.lines)); err != nil {
			return 0, err
		}
	}
	w.lines = append(w.lines, string(p))
	return len(p), nil
}

func (w *lineWriter) all() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.lines)
}

func (w *lineWriter) setFail(fail func(lines int) error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.fail = fail
}

// exporter runs ExportHeads of f, which has no sessions: its rows are put with f.putHeads.
type exporter struct {
	f      *fixture
	w      *lineWriter
	ticks  chan time.Time
	cancel context.CancelFunc
	done   chan error
	errs   chan error
	last   error // what ExportHeads returned at the end of the test
}

func startExport(t *testing.T, f *fixture, w *lineWriter) *exporter {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	x := &exporter{f: f, w: w, ticks: make(chan time.Time), cancel: cancel, done: make(chan error, 1), errs: make(chan error, 16)}
	go func() {
		x.done <- f.srv.ExportHeads(ctx, w, x.ticks, func(err error) { x.errs <- err })
	}()
	t.Cleanup(func() {
		cancel()
		x.last = <-x.done
	})
	return x
}

// barrier makes two ticks: the second is taken only after the round of the first is done, which has written what there was
// to write, and the round of the second has nothing to write.
func (x *exporter) barrier() {
	x.ticks <- time.Time{}
	x.ticks <- time.Time{}
}

// putHeads sets the rows of the table of the heads, behind the rules of the daemon.
func (f *fixture) putHeads(rows map[string]string) {
	f.t.Helper()
	f.update(func(tx *logdb.Tx) {
		for k, v := range rows {
			_ = tx.Put(dbwire.TableHeads, []byte(k), []byte(v))
		}
	})
}

func TestExportHeads(t *testing.T) {
	a1, a2 := "audit head activity 1 sha256:aa", "audit head activity 2 sha256:ab"
	b1 := "audit head logins 5 sha256:bb"

	t.Run("the rows when it begins, and the changed ones at each tick", func(t *testing.T) {
		f := newFixture(t, nil)
		f.putHeads(map[string]string{"activity": a1, "logins": b1})
		w := &lineWriter{}
		x := startExport(t, f, w)
		eventually(t, "the first lines", func() bool { return len(w.all()) == 2 })
		if got, want := w.all(), []string{a1 + "\n", b1 + "\n"}; !slices.Equal(got, want) {
			t.Fatalf("the lines are %q, want %q, one call of Write each, in the order of the keys", got, want)
		}
		x.barrier() // nothing has changed
		if n := len(w.all()); n != 2 {
			t.Errorf("a tick with no change wrote %d lines", n-2)
		}
		f.putHeads(map[string]string{"activity": a2})
		x.barrier()
		if got, want := w.all()[2:], []string{a2 + "\n"}; !slices.Equal(got, want) {
			t.Errorf("after a change the lines are %q, want %q", got, want)
		}
		f.putHeads(map[string]string{"activity": a1}) // back to what it was before: a change
		x.barrier()
		if got := w.all()[3:]; !slices.Equal(got, []string{a1 + "\n"}) {
			t.Errorf("after a change back the lines are %q", got)
		}
	})

	t.Run("a last write after the context ends", func(t *testing.T) {
		f := newFixture(t, nil)
		f.putHeads(map[string]string{"activity": a1})
		w := &lineWriter{}
		x := startExport(t, f, w)
		eventually(t, "the first line", func() bool { return len(w.all()) == 1 })
		f.putHeads(map[string]string{"activity": a2, "logins": b1})
		x.cancel()
		if err := <-x.done; err != nil {
			t.Fatalf("ExportHeads = %v", err)
		}
		if got, want := w.all()[1:], []string{a2 + "\n", b1 + "\n"}; !slices.Equal(got, want) {
			t.Errorf("the lines after the end are %q, want %q", got, want)
		}
		x.done <- nil // for the cleanup
	})

	t.Run("a writer that fails, and the lines again at the next tick", func(t *testing.T) {
		f := newFixture(t, nil)
		f.putHeads(map[string]string{"activity": a1, "logins": b1})
		w := &lineWriter{fail: func(int) error { return errors.New("full") }}
		x := startExport(t, f, w)
		err := <-x.errs
		if err == nil || !strings.Contains(err.Error(), "cannot write a head: full") {
			t.Fatalf("the error is %v", err)
		}
		if n := len(w.all()); n != 0 {
			t.Errorf("a failing writer has %d lines", n)
		}
		w.setFail(nil)
		x.ticks <- time.Time{}
		eventually(t, "the lines", func() bool { return len(w.all()) == 2 })
		if got, want := w.all(), []string{a1 + "\n", b1 + "\n"}; !slices.Equal(got, want) {
			t.Errorf("the lines are %q, want %q", got, want)
		}
		// One that fails after the first line: the second is tried again at the next tick, and the first is not.
		f.putHeads(map[string]string{"activity": a2, "logins": "audit head logins 6 sha256:bc"})
		w.setFail(func(lines int) error {
			if lines >= 3 {
				return errors.New("full again")
			}
			return nil
		})
		x.ticks <- time.Time{}
		if err := <-x.errs; err == nil || !strings.Contains(err.Error(), "full again") {
			t.Fatalf("the error is %v", err)
		}
		w.setFail(nil)
		x.ticks <- time.Time{}
		eventually(t, "the lines", func() bool { return len(w.all()) == 4 })
		if got, want := w.all()[2:], []string{a2 + "\n", "audit head logins 6 sha256:bc\n"}; !slices.Equal(got, want) {
			t.Errorf("the lines are %q, want %q", got, want)
		}
	})

	t.Run("a closed channel of ticks", func(t *testing.T) {
		f := newFixture(t, nil)
		f.putHeads(map[string]string{"activity": a1})
		w := &lineWriter{}
		ctx, cancel := context.WithCancel(context.Background())
		ticks := make(chan time.Time)
		close(ticks)
		done := make(chan error, 1)
		go func() { done <- f.srv.ExportHeads(ctx, w, ticks, nil) }()
		eventually(t, "the first line", func() bool { return len(w.all()) == 1 })
		f.putHeads(map[string]string{"activity": a2})
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if got, want := w.all(), []string{a1 + "\n", a2 + "\n"}; !slices.Equal(got, want) {
			t.Errorf("the lines are %q, want %q", got, want)
		}
	})

	t.Run("a row that is not a head line", func(t *testing.T) {
		f := newFixture(t, nil)
		// The daemon does not let a writer put it, but the file may have been written by another.
		f.putHeads(map[string]string{"activity": a1, "bad": "audit\nhead " + secret})
		w := &lineWriter{}
		x := startExport(t, f, w)
		err := <-x.errs
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("the error is %v", err)
		}
		eventually(t, "the good line", func() bool { return len(w.all()) == 1 })
		x.barrier()
		select {
		case err := <-x.errs:
			t.Errorf("the same row is told again: %v", err)
		default:
		}
		if got := w.all(); !slices.Equal(got, []string{a1 + "\n"}) {
			t.Errorf("the lines are %q", got)
		}
	})

	t.Run("more rows than the table has", func(t *testing.T) {
		f := newFixture(t, nil)
		rows := map[string]string{}
		for i := range maxHeadRows + 1 {
			rows["log"+string(rune('a'+i))] = "audit head x 1 sha256:00"
		}
		f.putHeads(rows)
		w := &lineWriter{}
		x := startExport(t, f, w)
		if err := <-x.errs; err == nil || !strings.Contains(err.Error(), "more than 16 rows") {
			t.Errorf("the error is %v", err)
		}
		if n := len(w.all()); n != 0 {
			t.Errorf("%d lines are written of a table that has too many", n)
		}
	})

	t.Run("an engine that is closed", func(t *testing.T) {
		f := newFixture(t, nil)
		f.putHeads(map[string]string{"activity": a1})
		if err := f.srv.Close(); err != nil {
			t.Fatal(err)
		}
		w := &lineWriter{}
		x := startExport(t, f, w)
		err := <-x.errs
		if err == nil || !strings.HasPrefix(err.Error(), "cannot read the heads: ") {
			t.Errorf("the error is %v", err)
		}
	})
}
