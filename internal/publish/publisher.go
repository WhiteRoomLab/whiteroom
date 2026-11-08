package publish

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"sync"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/build"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// The reasons of a Request.
const (
	reasonStart     = "start"
	reasonOperation = "operation"
	reasonRebuild   = "rebuild"
	reasonResume    = "resume"
)

// Options are the options of New.
type Options struct {
	Store  *store.Store     // the store to publish, which the Publisher does not close
	Schema content.SiteFile // the schema file of the site, named as in a site directory (site/schema.yaml)
	Theme  fs.FS            // the files of the theme, which every build reads again
	// Out, CacheDir, Memory and Workers are those of build.Options.
	Out, CacheDir string
	Memory        int64
	Workers       int
	Now           func() time.Time // the clock of the Publisher and of its Editor, for several goroutines; nil is time.Now
}

// Request asks for a build.
type Request struct {
	At     time.Time   // when it was made
	Reason string      // "start", "operation", "rebuild" or "resume"
	Result edit.Result // of the operation, if the reason is "operation"
}

// Build is a build of the site of the store. While it runs, only Started and Requests are set.
type Build struct {
	Started, Ended time.Time
	Requests       []Request // those that the build covers
	// Result is what the build published, also if it failed after publishing; nil if it failed before.
	Result *build.Result
	// Err is why the build failed: content.Errors, as build.Build returns them, for the problems of the site.
	Err error
}

// Status is the state of the builds of a Publisher.
type Status struct {
	Running *Build // nil if no build runs
	Pending bool   // requests wait for a build to take them, also while the builds are paused
	// Paused is true while no build may start: after a Rollback until Resume, and while a Rollback runs.
	Paused bool
	Last   *Build // the build that ended last, nil if none has
}

// Publisher builds the site of a store into Options.Out whenever what is published may have changed, one build at a
// time (4.4).
//
// The builds are asked for by requests: the start of the Publisher, which New records; each operation of its Editor
// whose Result has Publishes; Rebuild; and Resume. Run takes all the requests that no build has taken into one build, as
// soon as none runs and the builds are not paused, and that build reads a Snapshot of the store and builds it. The
// requests are taken before the snapshot, so the build sees the change that each of them stands for. A request that comes
// while a build runs is not covered by it, even if its snapshot sees the change: one more build covers all that came
// meanwhile, when that one ends.
//
// A build that fails neither undoes the operations, which stay in the store, nor changes the site that is published,
// which stays as the last successful build left it (4.2). The failure is in Status. The running build and the last one are
// kept in memory only: Status is empty in every new Publisher, whose start builds the store again.
type Publisher struct {
	o   Options
	ed  *edit.Editor
	now func() time.Time

	// beforeBuild, if not nil, is called by Run with the options of a build after its snapshot is taken and before it is
	// built; for the tests.
	beforeBuild func(build.Options)

	// mu guards the state below. Each change of it closes changed and replaces it, which is how Run, Wait and Rollback
	// learn of the change.
	mu      sync.Mutex
	changed chan struct{}
	started bool      // Run has been called
	wanted  []Request // the requests that no build has taken
	running *Build    // the build that runs, if any
	last    *Build    // the build that ended last
	paused  bool      // a Rollback has paused the builds, until Resume
	rolling bool      // a Rollback runs, so that no build may start
}

// New returns a Publisher of o.Store that has not started: it wants a build, which Run makes. It loads the theme for the
// Editor, and fails if it does not load, or if the store, the output directory, the cache directory or the theme is
// missing, or Memory or Workers is negative.
func New(o Options) (*Publisher, error) {
	switch {
	case o.Store == nil:
		return nil, errors.New("no store")
	case o.Out == "":
		return nil, errors.New("no output directory")
	case o.CacheDir == "":
		return nil, errors.New("no cache directory")
	case o.Theme == nil:
		return nil, errors.New("no theme")
	case o.Memory < 0:
		return nil, fmt.Errorf("memory %d is out of range (0 or more)", o.Memory)
	case o.Workers < 0:
		return nil, fmt.Errorf("workers %d is out of range (0 or more)", o.Workers)
	}
	th, err := theme.Load(o.Theme)
	if err != nil {
		return nil, themeErrors(err)
	}
	p := &Publisher{o: o, now: o.Now, changed: make(chan struct{})}
	if p.now == nil {
		p.now = time.Now
	}
	p.ed = edit.New(o.Store, o.Schema, th)
	p.ed.SetClock(p.now)
	p.ed.Notify(p.notify)
	p.request(reasonStart, edit.Result{})
	return p, nil
}

// themeErrors puts the files of the problems of the theme in theme/, as the build does.
func themeErrors(err error) error {
	var es content.Errors
	if !errors.As(err, &es) {
		return err
	}
	out := make(content.Errors, len(es))
	for i, e := range es {
		c := *e
		c.File = path.Join("theme", c.File)
		out[i] = &c
	}
	return out
}

// Editor returns the Editor whose operations the Publisher follows. Its clock is Options.Now.
func (p *Publisher) Editor() *edit.Editor { return p.ed }

// notify is the Notify function of the Editor, which its operations call in their own goroutines.
func (p *Publisher) notify(res edit.Result) {
	if res.Publishes {
		res.Warnings = slices.Clone(res.Warnings) // the caller of the operation gets the same result
		p.request(reasonOperation, res)
	}
}

// request records a request for a build.
func (p *Publisher) request(reason string, res edit.Result) {
	r := Request{At: p.now(), Reason: reason, Result: res}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.wanted = append(p.wanted, r)
	p.changeLocked()
}

// changeLocked wakes those who wait for a change of the state, with mu held.
func (p *Publisher) changeLocked() {
	close(p.changed)
	p.changed = make(chan struct{})
}

// Run makes the builds until ctx ends, and returns nil then. It cancels the build that runs as build.Build is canceled,
// and returns when that has stopped: a build that has switched the site by then ends its publish. The builds that are
// wanted then are not made. Run can be called only once.
func (p *Publisher) Run(ctx context.Context) error {
	p.mu.Lock()
	started := p.started
	p.started = true
	p.mu.Unlock()
	if started {
		return errors.New("the publisher has run already")
	}
	for ctx.Err() == nil {
		b, changed := p.next()
		if b == nil {
			select {
			case <-changed:
			case <-ctx.Done():
			}
			continue
		}
		res, err := p.build(ctx)
		p.end(b, res, err)
	}
	return nil
}

// blocked reports whether no build may start, with mu held: a Rollback has paused the builds or runs.
func (p *Publisher) blocked() bool { return p.paused || p.rolling }

// due reports whether a build will start as soon as none runs, with mu held.
func (p *Publisher) due() bool { return len(p.wanted) > 0 && !p.blocked() }

// next takes the requests into a new build, which it sets as the one that runs, if none runs and a build is due; else
// it returns the channel that is closed at the next change of the state.
func (p *Publisher) next() (*Build, <-chan struct{}) {
	at := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running != nil || !p.due() {
		return nil, p.changed
	}
	b := &Build{Started: at, Requests: p.wanted}
	p.wanted, p.running = nil, b
	p.changeLocked()
	return b, nil
}

// build reads a snapshot of the store and builds it. Its result is what build.Build returns.
func (p *Publisher) build(ctx context.Context) (*build.Result, error) {
	src, err := Snapshot(ctx, p.o.Store, p.o.Schema, p.o.Theme)
	if err != nil {
		return nil, fmt.Errorf("reading the store: %w", err)
	}
	opts := build.Options{Source: src, Out: p.o.Out, CacheDir: p.o.CacheDir, Memory: p.o.Memory, Workers: p.o.Workers}
	if p.beforeBuild != nil {
		p.beforeBuild(opts)
	}
	return build.Build(ctx, opts)
}

// end records that b has ended with res and err.
func (p *Publisher) end(b *Build, res *build.Result, err error) {
	ended := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.last = &Build{Started: b.Started, Ended: ended, Requests: b.Requests, Result: res, Err: err}
	p.running = nil
	p.changeLocked()
}

// Rebuild asks for a build of the store as it is, also if no operation of the Editor has asked for one: after a change
// that it does not see, such as an import or a save with the store's own API.
func (p *Publisher) Rebuild() { p.request(reasonRebuild, edit.Result{}) }

// Status returns the state of the builds. It is a copy.
func (p *Publisher) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Status{
		Running: p.running.clone(),
		Pending: len(p.wanted) > 0,
		Paused:  p.blocked(),
		Last:    p.last.clone(),
	}
}

// clone returns a copy of b, which is nil if b is.
func (b *Build) clone() *Build {
	if b == nil {
		return nil
	}
	c := *b
	c.Requests = slices.Clone(b.Requests)
	for i := range c.Requests {
		c.Requests[i].Result.Warnings = slices.Clone(c.Requests[i].Result.Warnings)
	}
	if b.Result != nil {
		r := *b.Result
		r.Warnings = slices.Clone(r.Warnings)
		c.Result = &r
	}
	return &c
}

// Wait returns when no build runs and none is due: none is wanted, or the builds are paused. It returns the error of ctx
// if that ends first. Before Run starts and after it has returned, Wait waits for a build that is wanted until ctx ends.
func (p *Publisher) Wait(ctx context.Context) error {
	return p.wait(ctx, func() bool { return p.running == nil && !p.due() })
}

// wait returns when cond, which is called with mu held, is true, or with the error of ctx if that ends first.
func (p *Publisher) wait(ctx context.Context, cond func() bool) error {
	for {
		p.mu.Lock()
		ok, changed := cond(), p.changed
		p.mu.Unlock()
		if ok {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Releases returns the releases in the output directory and the one that is published (build.ListReleases), which is an
// error that wraps fs.ErrNotExist before the first build has made the directory.
func (p *Publisher) Releases() (build.Releases, error) { return build.ListReleases(p.o.Out) }

// Rollback publishes the release, one of those that Releases lists, instead of the one that is published, at once
// (build.Rollback), and pauses the builds until Resume, so that the next build does not undo it (Q14-A). While they are
// paused the operations are saved as before, and the builds they ask for wait. No build starts from the call on; Rollback
// waits for the one that runs to end, which publishes its release before the rollback does.
//
// If Rollback fails, as build.Rollback does for a release that does not exist, or ctx ends while it waits, the published
// release and the pause are as they were. Only one Rollback can run at a time.
func (p *Publisher) Rollback(ctx context.Context, release string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	if p.rolling {
		p.mu.Unlock()
		return errors.New("another rollback is running")
	}
	p.rolling = true
	p.changeLocked()
	p.mu.Unlock()

	err := p.wait(ctx, func() bool { return p.running == nil })
	if err == nil {
		_, err = build.Rollback(p.o.Out, release)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.rolling = false
	if err == nil {
		p.paused = true
	}
	p.changeLocked()
	return err
}

// Resume ends the pause of a Rollback and asks for a build of the store as it is. It does nothing if the builds are not
// paused.
func (p *Publisher) Resume() {
	r := Request{At: p.now(), Reason: reasonResume}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.paused {
		return
	}
	p.paused = false
	p.wanted = append(p.wanted, r)
	p.changeLocked()
}
