package publish

import (
	"context"
	"errors"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/edit"
)

// defaultRetry is the wait before a failed scheduled publish is tried again, if Options.Retry is 0.
const defaultRetry = time.Minute

// ScheduleFailure is a scheduled publish that has failed, or the failure to read the schedules.
type ScheduleFailure struct {
	Entry string    // the entry, which stays scheduled; "" if the schedules could not be read
	At    time.Time // when the entry is scheduled to be published; zero if its scheduledAt does not parse, or if Entry is ""
	Tried time.Time // the time, by the clock of the Publisher, of the step that failed
	// Err is why it failed: content.Errors for the problems of the entry, its revision or the site, as
	// Editor.PublishScheduled returns them; else the error of the store.
	Err error
}

// stopper is the timer of the scheduler: a *time.Timer.
type stopper interface{ Stop() bool }

func startTimer(d time.Duration, f func()) stopper { return time.AfterFunc(d, f) }

// wake asks for a step of the scheduler, which Run makes before it starts the next build.
func (p *Publisher) wake() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reread = true
	p.changeLocked()
}

// endStep ends the step that runs, after it has recorded all it has to.
func (p *Publisher) endStep() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stepping = false
	p.changeLocked()
}

// step publishes the entries that are scheduled at a time that is not after the clock, in the order of Editor.Schedules,
// and arms the timer for the next step. Each publish asks for a build through the Notify of the Editor. An entry that
// changed since the schedules were read (Editor.PublishScheduled says it is not due) is left to the step that its change
// asks for, and a publish that fails is a ScheduleFailure of Status. A step that ctx stops records nothing.
func (p *Publisher) step(ctx context.Context) {
	defer p.endStep()
	now := p.now()
	list, err := p.ed.Schedules(ctx)
	if err != nil {
		if !canceled(ctx, err) {
			p.setFailures([]ScheduleFailure{{Tried: now, Err: err}})
			p.arm(p.retry, true)
		}
		return
	}
	var failures []ScheduleFailure
	var later time.Time // the earliest time that has not come
	for _, s := range list {
		if s.At.After(now) {
			later = s.At
			break
		}
		if p.beforePublish != nil {
			p.beforePublish(s.Entry)
		}
		_, err := p.ed.PublishScheduled(ctx, s.Entry, now)
		switch {
		case err == nil, errors.Is(err, edit.ErrNotDue):
		case canceled(ctx, err):
			return
		default:
			failures = append(failures, ScheduleFailure{Entry: s.Entry, At: s.At, Tried: now, Err: err})
		}
	}
	p.setFailures(failures)
	p.arm(wakeIn(p.now(), later, len(failures) > 0, p.retry))
}

// canceled reports whether err comes from the end of ctx.
func canceled(ctx context.Context, err error) bool {
	return ctx.Err() != nil && errors.Is(err, ctx.Err())
}

func (p *Publisher) setFailures(failures []ScheduleFailure) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failures = failures
}

// wakeIn returns how long to wait for the next step: until later, the earliest time that has not come, if it is not zero,
// or retry if a publish has failed, whichever is shorter. ok is false if there is nothing to wait for.
func wakeIn(now, later time.Time, failed bool, retry time.Duration) (d time.Duration, ok bool) {
	if !later.IsZero() {
		d, ok = max(later.Sub(now), 0), true
	}
	if failed && (!ok || retry < d) {
		d, ok = retry, true
	}
	return d, ok
}

// arm replaces the timer, which wakes the loop of Run for a step after d if ok, and stops it otherwise.
func (p *Publisher) arm(d time.Duration, ok bool) {
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	if ok {
		p.timer = p.afterFunc(d, p.wake)
	}
}
