package audit

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sync"
	"testing"
	"time"
)

// headOf returns a head of log with the number n, whose hash is one that nothing else has.
func headOf(log Log, n uint64) Head {
	return Head{Log: log, N: n, Hash: sha256.Sum256([]byte(fmt.Sprintf("%s %d", log, n)))}
}

// lineOf is the line that Export writes for h.
func lineOf(h Head) string {
	return h.String() + "\n"
}

// headsAnswer is what a call of AuditHeads of fakeSource returns: heads, err, or the error of its context.
type headsAnswer struct {
	heads    []Head
	err      error
	ctxError bool
}

// headsCall is what fakeSource has seen of a call of AuditHeads.
type headsCall struct {
	ctx      context.Context
	ended    error         // the error of the context when it was called
	deadline bool          // the context has a deadline
	left     time.Duration // the time to it when it was called
}

// fakeSource is a HeadSource that answers each call of AuditHeads from answers, in order, and cancels the context of the
// Export under test when it is called the call-th time, so that the end of the context falls at a call and not between.
type fakeSource struct {
	mu      sync.Mutex
	answers []headsAnswer
	calls   []headsCall
	cancel  context.CancelFunc
	call    int            // the number of the call that cancels; -1: none
	onCall  func(call int) // if not nil, run in each call
}

func (f *fakeSource) AuditHeads(ctx context.Context) ([]Head, error) {
	f.mu.Lock()
	i := len(f.calls)
	c := headsCall{ctx: ctx, ended: ctx.Err()}
	if d, ok := ctx.Deadline(); ok {
		c.deadline, c.left = true, time.Until(d)
	}
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	if i == f.call {
		f.cancel()
	}
	if f.onCall != nil {
		f.onCall(i)
	}
	if i >= len(f.answers) {
		return nil, fmt.Errorf("read %d: the test has no answer for it", i)
	}
	a := f.answers[i]
	if a.ctxError {
		return nil, ctx.Err()
	}
	return a.heads, a.err
}

// fakeWriter is an io.Writer that keeps what it is given, one string for each call of Write, and fails the calls whose
// numbers failures names.
type fakeWriter struct {
	mu       sync.Mutex
	lines    []string
	calls    int
	failures map[int]error
}

func (w *fakeWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	i := w.calls
	w.calls++
	if err := w.failures[i]; err != nil {
		return 0, err
	}
	w.lines = append(w.lines, string(p))
	return len(p), nil
}

// errorList keeps the errors that onError has been given.
type errorList struct {
	mu   sync.Mutex
	errs []error
}

func (l *errorList) add(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errs = append(l.errs, err)
}

func (l *errorList) texts() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var texts []string
	for _, err := range l.errs {
		texts = append(texts, err.Error())
	}
	return texts
}

// exportRun is one run of Export: it sends ticks to it, and the source ends its context during the last of their reads.
type exportRun struct {
	answers  []headsAnswer
	failures map[int]error // of the writes
	ticks    int
	onError  bool // give Export a function for its errors
}

type exportResult struct {
	lines []string
	calls []headsCall
	errs  []string // the texts of what onError was given
	ret   error
}

func (r exportRun) run(t *testing.T) exportResult {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	defer time.AfterFunc(5*time.Second, cancel).Stop() // a fault that keeps Export from ending is a failure, no hang
	src := &fakeSource{answers: r.answers, cancel: cancel, call: r.ticks}
	w := &fakeWriter{failures: r.failures}
	var errs errorList
	var onError func(error)
	if r.onError {
		onError = errs.add
	}
	ticks := make(chan time.Time)
	done := make(chan error, 1)
	go func() { done <- Export(ctx, src, w, ticks, onError) }()
	for range r.ticks {
		select {
		case ticks <- time.Time{}:
		case err := <-done:
			t.Fatalf("Export() returned %v before the tick", err)
		}
	}
	ret := <-done
	return exportResult{lines: w.lines, calls: src.calls, errs: errs.texts(), ret: ret}
}

func TestExport(t *testing.T) {
	login := func(n uint64) Head { return headOf(LoginLog, n) }
	activity := func(n uint64) Head { return headOf(ActivityLog, n) }
	both := func(l, a uint64) headsAnswer { return headsAnswer{heads: []Head{login(l), activity(a)}} }
	lines := func(hs ...Head) []string {
		var out []string
		for _, h := range hs {
			out = append(out, lineOf(h))
		}
		return out
	}
	errBoom, errFull := errors.New("boom"), errors.New("disk full")
	for _, tt := range []struct {
		name string
		run  exportRun
		want []string // the lines
		errs []string
		ret  string // the error that Export returns; "" for nil
	}{
		{
			name: "the heads at the start, and what has changed at each tick, and at the end",
			run: exportRun{ticks: 2, onError: true, answers: []headsAnswer{
				both(1, 1), both(1, 2), both(1, 2), both(2, 2),
			}},
			want: lines(login(1), activity(1), activity(2), login(2)),
		},
		{
			name: "nothing again when nothing has changed",
			run:  exportRun{ticks: 3, onError: true, answers: []headsAnswer{both(4, 7), both(4, 7), both(4, 7), both(4, 7), both(4, 7)}},
			want: lines(login(4), activity(7)),
		},
		{
			name: "a log without records is not written",
			run: exportRun{ticks: 1, onError: true, answers: []headsAnswer{
				{heads: []Head{activity(1)}}, {heads: []Head{activity(1)}}, {heads: []Head{login(1), activity(1)}},
			}},
			want: lines(activity(1), login(1)),
		},
		{
			name: "no log has records",
			run:  exportRun{ticks: 1, onError: true, answers: []headsAnswer{{}, {}, {}}},
			want: nil,
		},
		{
			name: "a head that has gone back is written",
			run: exportRun{ticks: 1, onError: true, answers: []headsAnswer{
				{heads: []Head{activity(5)}}, {heads: []Head{activity(3)}}, {heads: []Head{activity(5)}},
			}},
			want: lines(activity(5), activity(3), activity(5)),
		},
		{
			name: "a head with the same number and another hash is written",
			run: exportRun{ticks: 1, onError: true, answers: []headsAnswer{
				{heads: []Head{activity(5)}},
				{heads: []Head{{Log: ActivityLog, N: 5, Hash: headOf(ActivityLog, 6).Hash}}},
				{heads: []Head{activity(5)}},
			}},
			want: []string{lineOf(activity(5)), lineOf(Head{Log: ActivityLog, N: 5, Hash: headOf(ActivityLog, 6).Hash}),
				lineOf(activity(5))},
		},
		{
			name: "a head with another number and the same hash is written",
			run: exportRun{ticks: 1, onError: true, answers: []headsAnswer{
				{heads: []Head{activity(5)}},
				{heads: []Head{{Log: ActivityLog, N: 6, Hash: activity(5).Hash}}},
				{heads: []Head{{Log: ActivityLog, N: 6, Hash: activity(5).Hash}}},
			}},
			want: []string{lineOf(activity(5)), lineOf(Head{Log: ActivityLog, N: 6, Hash: activity(5).Hash})},
		},
		{
			name: "a read that fails is reported, and tried again at the next tick",
			run: exportRun{ticks: 2, onError: true, answers: []headsAnswer{
				{err: errBoom}, both(1, 1), both(1, 1), both(1, 2),
			}},
			want: lines(login(1), activity(1), activity(2)),
			errs: []string{"cannot read the heads of the audit logs: boom"},
		},
		{
			name: "a write that fails is reported, and written again at the next tick",
			run: exportRun{ticks: 1, onError: true, failures: map[int]error{1: errFull}, answers: []headsAnswer{
				both(1, 1), both(1, 1), both(1, 1),
			}},
			want: lines(login(1), activity(1)),
			errs: []string{"cannot write the head of the activity log: disk full"},
		},
		{
			name: "the first write that fails ends the round, and the heads that are left wait",
			run: exportRun{ticks: 1, onError: true, failures: map[int]error{0: errFull}, answers: []headsAnswer{
				both(1, 1), both(1, 1), both(1, 1),
			}},
			want: lines(login(1), activity(1)),
			errs: []string{"cannot write the head of the login log: disk full"},
		},
		{
			name: "a write that fails with a head that has changed again",
			run: exportRun{ticks: 1, onError: true, failures: map[int]error{0: errFull}, answers: []headsAnswer{
				{heads: []Head{activity(1)}}, {heads: []Head{activity(2)}}, {heads: []Head{activity(2)}},
			}},
			want: lines(activity(2)),
			errs: []string{"cannot write the head of the activity log: disk full"},
		},
		{
			name: "the write at the end that fails is returned, and reported",
			run: exportRun{ticks: 0, onError: true, failures: map[int]error{2: errFull}, answers: []headsAnswer{
				both(1, 1), both(2, 1),
			}},
			want: lines(login(1), activity(1)),
			errs: []string{"cannot write the head of the login log: disk full"},
			ret:  "cannot write the head of the login log: disk full",
		},
		{
			name: "the write at the end that fails, with no function for the errors",
			run: exportRun{ticks: 0, onError: false, failures: map[int]error{2: errFull}, answers: []headsAnswer{
				both(1, 1), both(2, 1),
			}},
			want: lines(login(1), activity(1)),
			ret:  "cannot write the head of the login log: disk full",
		},
		{
			name: "the read at the end that fails is returned, and reported",
			run: exportRun{ticks: 1, onError: true, answers: []headsAnswer{
				both(1, 1), both(1, 1), {err: errBoom},
			}},
			want: lines(login(1), activity(1)),
			errs: []string{"cannot read the heads of the audit logs: boom"},
			ret:  "cannot read the heads of the audit logs: boom",
		},
		{
			name: "an error that overlaps the end of the context is not reported, and the end tries again",
			run: exportRun{ticks: 1, onError: true, failures: map[int]error{0: errFull, 1: errFull, 2: errFull},
				answers: []headsAnswer{both(1, 1), both(1, 1), both(1, 1)}},
			want: nil,
			errs: []string{"cannot write the head of the login log: disk full", "cannot write the head of the login log: disk full"},
			ret:  "cannot write the head of the login log: disk full",
		},
		{
			name: "an error that the end of the context has caused is not reported",
			run: exportRun{ticks: 1, onError: true, answers: []headsAnswer{
				both(1, 1), {ctxError: true}, both(2, 1),
			}},
			want: lines(login(1), activity(1), login(2)),
		},
		{
			name: "no function for the errors",
			run: exportRun{ticks: 1, onError: false, failures: map[int]error{0: errFull}, answers: []headsAnswer{
				{err: errBoom}, both(1, 1), both(1, 1),
			}},
			want: lines(login(1), activity(1)),
		},
		{
			name: "the end only, with the heads that have not been written",
			run:  exportRun{ticks: 0, onError: true, answers: []headsAnswer{{}, both(3, 4)}},
			want: lines(login(3), activity(4)),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.run.run(t)
			if !slices.Equal(got.lines, tt.want) {
				t.Errorf("the lines = %q, want %q", got.lines, tt.want)
			}
			if wantCalls := tt.run.ticks + 2; len(got.calls) != wantCalls {
				t.Errorf("the reads = %d, want %d: the start, each tick and the end", len(got.calls), wantCalls)
			}
			if tt.run.onError && !slices.Equal(got.errs, tt.errs) {
				t.Errorf("the errors given to onError = %q, want %q", got.errs, tt.errs)
			}
			switch {
			case tt.ret == "" && got.ret != nil:
				t.Errorf("Export() = %v, want nil", got.ret)
			case tt.ret != "" && (got.ret == nil || got.ret.Error() != tt.ret):
				t.Errorf("Export() = %v, want %q", got.ret, tt.ret)
			}
		})
	}
}

// TestExportErrorsWrap checks that the errors of the source and of the writer can be found in what Export returns and
// reports.
func TestExportErrorsWrap(t *testing.T) {
	errBoom := errors.New("boom")
	var errs errorList
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	src := &fakeSource{answers: []headsAnswer{{}, {err: errBoom}}, cancel: cancel, call: 0}
	err := Export(ctx, src, &fakeWriter{}, make(chan time.Time), errs.add)
	if !errors.Is(err, errBoom) || len(errs.errs) != 1 || !errors.Is(errs.errs[0], errBoom) {
		t.Errorf("Export() = %v and onError got %v, want errors that wrap %v", err, errs.errs, errBoom)
	}

	errFull := errors.New("disk full")
	errs = errorList{}
	ctx, cancel = context.WithTimeout(t.Context(), 10*time.Second)
	src = &fakeSource{answers: []headsAnswer{{heads: []Head{headOf(LoginLog, 1)}}, {heads: []Head{headOf(LoginLog, 2)}}},
		cancel: cancel, call: 0}
	err = Export(ctx, src, &fakeWriter{failures: map[int]error{1: errFull}}, make(chan time.Time), errs.add)
	if !errors.Is(err, errFull) || len(errs.errs) != 1 || !errors.Is(errs.errs[0], errFull) {
		t.Errorf("Export() = %v and onError got %v, want errors that wrap %v", err, errs.errs, errFull)
	}
}

// TestExportLine checks the line, which a person who keeps the log of a process reads, and that ParseHead reads it.
func TestExportLine(t *testing.T) {
	h := headOf(ActivityLog, 12)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	src := &fakeSource{answers: []headsAnswer{{heads: []Head{h}}, {heads: []Head{h}}}, cancel: cancel, call: 0}
	w := &fakeWriter{}
	if err := Export(ctx, src, w, make(chan time.Time), nil); err != nil {
		t.Fatalf("Export() = %v", err)
	}
	if len(w.lines) != 1 {
		t.Fatalf("the lines = %q, want one", w.lines)
	}
	if !regexp.MustCompile(`^audit head (login|activity) [1-9][0-9]* sha256:[0-9a-f]{64}\n$`).MatchString(w.lines[0]) {
		t.Errorf("the line = %q, want the log, the number and the hash alone, and a newline", w.lines[0])
	}
	if got, err := ParseHead("May 01 12:00:00 whiteroom[1]: " + w.lines[0]); err != nil || got != h {
		t.Errorf("ParseHead() of the line in a journal = %+v, %v; want %+v", got, err, h)
	}
}

// TestExportContexts checks the contexts of the reads: the start and the ticks use that of Export, and the end uses one
// that its end does not cancel and that has a deadline in 5 seconds.
func TestExportContexts(t *testing.T) {
	got := exportRun{ticks: 2, answers: []headsAnswer{{}, {}, {}, {}}}.run(t)
	if len(got.calls) != 4 {
		t.Fatalf("the reads = %d, want 4", len(got.calls))
	}
	for i, c := range got.calls[:3] {
		if c.ended != nil || c.deadline {
			t.Errorf("the context of read %d: ended %v, has a deadline %v; want the context of Export, which has not ended", i, c.ended, c.deadline)
		}
		// It is the context of Export, which has ended since.
		if c.ctx.Err() == nil {
			t.Errorf("the context of read %d has not ended with the context of Export", i)
		}
	}
	// The context of Export has ended when the end writes, and the context of that write has not.
	last := got.calls[3]
	if last.ended != nil {
		t.Errorf("the context of the last read has ended: %v", last.ended)
	}
	if !last.deadline || last.left <= 4*time.Second || last.left > 5*time.Second {
		t.Errorf("the context of the last read has a deadline in %v (%v), want 5 seconds", last.left, last.deadline)
	}
}

// TestExportEndedBeforeItBegins checks that Export, whose context has ended before it begins, reads the heads once, at
// its end, with a context that works.
func TestExportEndedBeforeItBegins(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	h := headOf(LoginLog, 3)
	src := &fakeSource{answers: []headsAnswer{{heads: []Head{h}}}, cancel: cancel, call: -1}
	w := &fakeWriter{}
	var errs errorList
	if err := Export(ctx, src, w, make(chan time.Time), errs.add); err != nil {
		t.Fatalf("Export() = %v", err)
	}
	if len(src.calls) != 1 || src.calls[0].ended != nil || !src.calls[0].deadline {
		t.Errorf("the reads = %+v, want one, with a context that has not ended", src.calls)
	}
	if want := []string{lineOf(h)}; !slices.Equal(w.lines, want) {
		t.Errorf("the lines = %q, want %q", w.lines, want)
	}
	if len(errs.errs) != 0 {
		t.Errorf("the errors given to onError = %q, want none", errs.texts())
	}
}

// TestExportTicksClosed checks that a ticks that is closed fires no more, and does not make Export read without end.
func TestExportTicksClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// The context ends 50 milliseconds after the read at the start, in which a loop on a closed ticks would read often.
	src := &fakeSource{answers: []headsAnswer{{}, {}}, cancel: func() {}, call: -1, onCall: func(call int) {
		if call == 0 {
			time.AfterFunc(50*time.Millisecond, cancel)
		}
	}}
	ticks := make(chan time.Time)
	close(ticks)
	var errs errorList
	if err := Export(ctx, src, &fakeWriter{}, ticks, errs.add); err != nil {
		t.Fatalf("Export() = %v", err)
	}
	if len(src.calls) != 2 || len(errs.errs) != 0 {
		t.Errorf("the reads = %d and the errors = %q, want 2, the start and the end, and none", len(src.calls), errs.texts())
	}
}

// TestExportNilTicks checks that Export with no ticks writes at its start and at its end.
func TestExportNilTicks(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	src := &fakeSource{answers: []headsAnswer{{heads: []Head{headOf(LoginLog, 1)}}, {heads: []Head{headOf(LoginLog, 2)}}},
		cancel: cancel, call: 0}
	w := &fakeWriter{}
	if err := Export(ctx, src, w, nil, nil); err != nil {
		t.Fatalf("Export() = %v", err)
	}
	if want := []string{lineOf(headOf(LoginLog, 1)), lineOf(headOf(LoginLog, 2))}; !slices.Equal(w.lines, want) {
		t.Errorf("the lines = %q, want %q", w.lines, want)
	}
}
