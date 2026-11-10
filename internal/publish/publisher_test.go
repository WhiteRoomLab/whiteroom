package publish

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/build"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

const (
	draftEN   = "ent_01JC0000000000000000000003" // a draft page in en without revisions, which a menu and a redirect use
	postJA    = "ent_01JB8Y6T2WT6X9C3E6G9J2K5M8" // a published post in ja, which a redirect goes to
	newsTerm  = "trm_01JBA4C7E2G5J8K1M4P7R2T5W8" // the category news in ja, whose page is /category/news/
	pdfMedium = "med_01JB8Z3K5TM8A1C5F9V3J6K2N7" // a PDF, whose file has a path that no redirect may take

	newsPage    = "public/category/news/index.html"
	careersPage = "public/en/careers/index.html" // that of draftEN, with careersRevision

	// careersRevision gives draftEN a slug that no page has, and feedRevision the one of the feed in en.
	careersRevision = `{
  "schemaVersion": 1,
  "title": "Careers",
  "slug": "careers"
}`
	feedRevision = `{
  "schemaVersion": 1,
  "title": "Feed",
  "slug": "feed"
}`
)

// fixedNow is the clock of the Publishers of the tests, and so the time of every request and build.
var fixedNow = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

// rig is a Publisher of the imported sample site in new directories, which a test drives.
type rig struct {
	*Publisher
	t    *testing.T
	s    *store.Store
	opts Options
}

// newRig returns a rig whose Publisher has not started to run.
func newRig(t *testing.T) *rig {
	t.Helper()
	s := importSample(t)
	opts := Options{
		Store: s, Schema: schemaFile(t), Theme: exampleTheme(t), Out: t.TempDir(), CacheDir: t.TempDir(), Workers: 2,
		Now: func() time.Time { return fixedNow },
	}
	p, err := New(opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return &rig{Publisher: p, t: t, s: s, opts: opts}
}

// runner is a Publisher that runs.
type runner struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error // of Run, set before done is closed
}

// runPublisher runs p until the end of the test, or until the runner is stopped.
func runPublisher(t *testing.T, p *Publisher) *runner {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	rn := &runner{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(rn.done)
		rn.err = p.Run(ctx)
	}()
	t.Cleanup(func() {
		if err := rn.stop(); err != nil {
			t.Errorf("Run() error = %v", err)
		}
	})
	return rn
}

// stop ends the context of Run, waits for it to return, and returns its error.
func (rn *runner) stop() error {
	rn.cancel()
	<-rn.done
	return rn.err
}

func (r *rig) start() *runner { return runPublisher(r.t, r.Publisher) }

// hold stops the builds of a Publisher at the point where beforeBuild is called, after their snapshots.
type hold struct {
	n       int32 // the build to hold, counting from 1; none if 0
	count   atomic.Int32
	reached chan struct{} // closed when the build n holds
	gate    chan struct{} // closed to let it go
	once    sync.Once

	mu    sync.Mutex
	lasts []*Build // the last build when each build took its snapshot
}

// holdBuild makes the n-th build of p, counting from 1, wait after its snapshot until release; the others do not wait.
// With n = 0 it only counts the builds. It must be called before p runs.
func holdBuild(t *testing.T, p *Publisher, n int) *hold {
	t.Helper()
	h := &hold{n: int32(n), reached: make(chan struct{}), gate: make(chan struct{})}
	ended := t.Context()
	p.beforeBuild = func(build.Options) {
		last := p.Status().Last
		h.mu.Lock()
		h.lasts = append(h.lasts, last)
		h.mu.Unlock()
		if h.count.Add(1) != h.n {
			return
		}
		close(h.reached)
		select {
		case <-h.gate:
		case <-ended.Done(): // so that a failing test does not hang
		}
	}
	return h
}

// release lets the held build go.
func (h *hold) release() { h.once.Do(func() { close(h.gate) }) }

// builds returns the number of builds that have taken their snapshots.
func (h *hold) builds() int { return int(h.count.Load()) }

// lastBefore returns the build that had ended last when the n-th build, counting from 1, took its snapshot.
func (h *hold) lastBefore(n int) *Build {
	h.mu.Lock()
	defer h.mu.Unlock()
	if n < 1 || n > len(h.lasts) {
		return nil
	}
	return h.lasts[n-1]
}

// await waits for the held build.
func (h *hold) await(t *testing.T) {
	t.Helper()
	select {
	case <-h.reached:
	case <-time.After(time.Minute):
		t.Fatal("no build holds")
	}
}

// idle waits until Wait returns.
func (r *rig) idle() {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(r.t.Context(), time.Minute)
	defer cancel()
	if err := r.Wait(ctx); err != nil {
		r.t.Fatalf("Wait() error = %v; Status() = %+v", err, r.Status())
	}
}

// until waits until cond, which is called with the lock of the state held, is true.
func (r *rig) until(what string, cond func() bool) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(r.t.Context(), time.Minute)
	defer cancel()
	if err := r.wait(ctx, cond); err != nil {
		r.t.Fatalf("waiting until %s: %v", what, err)
	}
}

// must returns res, or fails the test if err is not nil.
func must(t *testing.T) func(res edit.Result, err error) edit.Result {
	return func(res edit.Result, err error) edit.Result {
		t.Helper()
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		return res
	}
}

func (r *rig) op() edit.Op { return edit.Op{By: userID} }

// doc returns the current version of a document.
func (r *rig) doc(kind content.RefKind, id string) store.Document {
	r.t.Helper()
	var d store.Document
	err := r.s.View(r.t.Context(), func(tx *store.Tx) (err error) {
		d, err = tx.Document(kind, id)
		return err
	})
	if err != nil {
		r.t.Fatalf("Document(%q, %q) error = %v", kind, id, err)
	}
	return d
}

// entryState returns the state of the entry, and the latest of its revisions, "" if it has none.
func (r *rig) entryState(id string) (content.Entry, string) {
	r.t.Helper()
	var sum store.Summary
	err := r.s.View(r.t.Context(), func(tx *store.Tx) (err error) {
		sum, err = tx.Summary(content.RefEntry, id)
		return err
	})
	if err != nil {
		r.t.Fatalf("Summary(%q) error = %v", id, err)
	}
	return *sum.Entry, sum.Latest
}

func (r *rig) saveRevision(entry, data string) edit.Result {
	r.t.Helper()
	_, latest := r.entryState(entry)
	return must(r.t)(r.Editor().SaveRevision(r.t.Context(), r.op(), entry, latest, []byte(data)))
}

func (r *rig) publish(entry, revision string) edit.Result {
	r.t.Helper()
	seq := r.doc(content.RefEntry, entry).Seq
	return must(r.t)(r.Editor().Publish(r.t.Context(), r.op(), entry, seq, revision))
}

func (r *rig) unpublish(entry string) edit.Result {
	r.t.Helper()
	seq := r.doc(content.RefEntry, entry).Seq
	return must(r.t)(r.Editor().Unpublish(r.t.Context(), r.op(), entry, seq))
}

// rename saves the term id with another name. It may be called from any goroutine.
func (r *rig) rename(id, name string) (edit.Result, error) {
	var d store.Document
	err := r.s.View(r.t.Context(), func(tx *store.Tx) (err error) {
		d, err = tx.Document(content.RefTerm, id)
		return err
	})
	if err != nil {
		return edit.Result{}, err
	}
	var term content.Term
	if err := term.UnmarshalJSON(d.Data); err != nil {
		return edit.Result{}, err
	}
	term.Name = name
	data, err := term.MarshalJSON()
	if err != nil {
		return edit.Result{}, err
	}
	return r.Editor().Save(r.t.Context(), r.op(), content.RefTerm, id, d.Seq, data)
}

// renameNews gives the term news another name, which its page shows.
func (r *rig) renameNews(name string) edit.Result {
	r.t.Helper()
	return must(r.t)(r.rename(newsTerm, name))
}

// current returns the name of the release that the output directory publishes, "" if there is none.
func (r *rig) current() string {
	r.t.Helper()
	link, err := os.Readlink(filepath.Join(r.opts.Out, "current"))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		r.t.Fatal(err)
	}
	return filepath.Base(link)
}

// file returns a file of the release that is published, or nil if there is none.
func (r *rig) file(name string) []byte {
	r.t.Helper()
	data, err := os.ReadFile(filepath.Join(r.opts.Out, "current", filepath.FromSlash(name)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		r.t.Fatal(err)
	}
	return data
}

func (r *rig) releases() build.Releases {
	r.t.Helper()
	rel, err := r.Releases()
	if err != nil {
		r.t.Fatalf("Releases() error = %v", err)
	}
	return rel
}

// wantReleases checks that the output directory publishes current and keeps exactly the releases kept.
func (r *rig) wantReleases(current string, kept ...string) {
	r.t.Helper()
	slices.Sort(kept)
	if rel := r.releases(); rel.Current != current || !slices.Equal(rel.Names, kept) {
		r.t.Errorf("Releases() = %+v, want %s published among %v", rel, current, kept)
	}
	if got := r.current(); got != current {
		r.t.Errorf("current is %q, want %q", got, current)
	}
}

// wantStoreBuilt checks that the release that is published is that of a build of the store as it is.
func (r *rig) wantStoreBuilt() {
	r.t.Helper()
	fresh, _ := buildSite(r.t, snapshotOf(r.t, r.s))
	if got := r.current(); got != fresh.Release {
		r.t.Errorf("the release %s is published, want %s, that of a build of the store as it is", got, fresh.Release)
	}
}

// wantBuild checks the last build of st: it has ended without an error, and covers exactly the requests with the reasons.
func wantBuild(t *testing.T, st Status, want ...string) *Build {
	t.Helper()
	b := st.Last
	if b == nil {
		t.Fatalf("Status() = %+v, want a last build", st)
	}
	if b.Err != nil || b.Result == nil {
		t.Fatalf("the last build = %+v, want a result and no error", b)
	}
	if got := reasons(b.Requests); !slices.Equal(got, want) {
		t.Fatalf("the last build covers the requests %v, want %v", got, want)
	}
	return b
}

// sameDocuments fails the test if the settings or the documents of any kind differ between the snapshots before and after;
// what says what must not have changed them.
func sameDocuments(t *testing.T, what string, before, after *Source) {
	t.Helper()
	bs, _, err := before.Settings()
	if err != nil {
		t.Fatal(err)
	}
	as, _, err := after.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if !sameFile(as, bs) {
		t.Errorf("%s has changed the settings: %s, want %s", what, describe(as), describe(bs))
	}
	for _, kind := range siteKinds {
		b, err := before.Documents(kind)
		if err != nil {
			t.Fatal(err)
		}
		a, err := after.Documents(kind)
		if err != nil {
			t.Fatal(err)
		}
		if d := diffFiles(a, b); d != "" {
			t.Errorf("%s has changed the %s: %s", what, kind, d)
		}
	}
}

// quietPublisher returns a Publisher of an empty store, which nothing runs, for the tests of its state: they set and read
// the state without a lock, as no goroutine uses it. The step of the scheduler that the start asks for is not due, so that
// they can set the state of the builds by hand.
func quietPublisher(t *testing.T) *Publisher {
	t.Helper()
	p, err := New(Options{
		Store: newStore(t), Schema: schemaFile(t), Theme: exampleTheme(t), Out: t.TempDir(), CacheDir: t.TempDir(),
		Now: func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p.reread = false
	return p
}

// reasons returns the reasons of the requests.
func reasons(requests []Request) []string {
	var got []string
	for _, q := range requests {
		got = append(got, q.Reason)
	}
	return got
}

// TestStateMachine checks, for each combination of what the state can hold, whether a build starts, what Status says, and
// whether Wait returns. No goroutine runs, so that what must not happen cannot be missed because it is slow.
func TestStateMachine(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, wanted := range []bool{false, true} {
		for _, running := range []bool{false, true} {
			for _, paused := range []bool{false, true} {
				for _, rolling := range []bool{false, true} {
					name := fmt.Sprintf("wanted=%v running=%v paused=%v rolling=%v", wanted, running, paused, rolling)
					t.Run(name, func(t *testing.T) {
						p := quietPublisher(t)
						requests := []Request{{At: fixedNow, Reason: "operation"}}
						p.wanted = nil
						if wanted {
							p.wanted = requests
						}
						if running {
							p.running = &Build{Started: fixedNow, Requests: []Request{{Reason: "start"}}}
						}
						p.paused, p.rolling = paused, rolling

						blocked := paused || rolling
						st := p.Status()
						if st.Pending != wanted || st.Paused != blocked || (st.Running != nil) != running || st.Last != nil {
							t.Errorf("Status() = %+v, want Pending %v, Paused %v and Running %v", st, wanted, blocked, running)
						}
						due := wanted && !blocked
						if err := p.Wait(canceled); (err == nil) != (!running && !due) {
							t.Errorf("Wait() error = %v with a build running = %v and one due = %v, want it to return when neither", err, running, due)
						}

						before, nWanted := p.running, len(p.wanted)
						step, b, changed := p.next()
						if step {
							t.Fatal("next() = a step of the scheduler, want none: no step is due")
						}
						if starts := due && !running; (b != nil) != starts {
							t.Fatalf("next() = %+v, want a build: %v", b, starts)
						}
						if b == nil {
							if p.running != before || len(p.wanted) != nWanted {
								t.Errorf("next() changed the state without starting a build")
							}
							p.request("rebuild", edit.Result{}, false)
							select {
							case <-changed:
							default:
								t.Errorf("the channel that next() returned is not closed by the next change")
							}
							return
						}
						if !reflect.DeepEqual(b.Requests, requests) || !b.Started.Equal(fixedNow) || b.Ended != (time.Time{}) ||
							p.running != b || len(p.wanted) != 0 {
							t.Errorf("next() = %+v with %d requests wanted, want a build of %+v that runs", b, len(p.wanted), requests)
						}
					})
				}
			}
		}
	}
}

// TestTransitions checks the coalescing of the requests, and what a build leaves in Status, step by step in one goroutine.
func TestTransitions(t *testing.T) {
	t.Parallel()
	p := quietPublisher(t)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	_, b1, _ := p.next()
	if b1 == nil || !slices.Equal(reasons(b1.Requests), []string{"start"}) {
		t.Fatalf("next() = %+v, want the build of the start", b1)
	}
	// What comes while it runs is for the next build, and only what asks for one.
	p.Rebuild()
	p.notify(edit.Result{Kind: content.RefTerm, ID: newsTerm, Publishes: true})
	p.notify(edit.Result{Kind: content.RefEntry, ID: draftEN})
	if step, b, _ := p.next(); step || b != nil {
		t.Fatalf("next() = %v, %+v while a build runs", step, b)
	}
	st := p.Status()
	if st.Running == nil || !st.Pending || st.Last != nil || !slices.Equal(reasons(st.Running.Requests), []string{"start"}) {
		t.Errorf("Status() = %+v, want the start build running and a build wanted", st)
	}
	if err := p.Wait(canceled); !errors.Is(err, context.Canceled) {
		t.Errorf("Wait() error = %v while a build runs, want %v", err, context.Canceled)
	}

	failure := errors.New("failed")
	fixedLater := fixedNow.Add(time.Hour)
	p.now = func() time.Time { return fixedLater }
	p.end(b1, nil, failure)
	st = p.Status()
	if st.Running != nil || !st.Pending || st.Last == nil || !errors.Is(st.Last.Err, failure) || st.Last.Result != nil ||
		!st.Last.Started.Equal(fixedNow) || !st.Last.Ended.Equal(fixedLater) || !slices.Equal(reasons(st.Last.Requests), []string{"start"}) {
		t.Errorf("Status() = %+v, want the start build ended with its error, and a build wanted", st)
	}
	if err := p.Wait(canceled); !errors.Is(err, context.Canceled) {
		t.Errorf("Wait() error = %v with a build wanted, want %v", err, context.Canceled)
	}

	// Rebuild asked for a step of the scheduler too, which comes before the build and which Wait waits for: the entries it
	// publishes are covered by that build. No other request asked for a step.
	if step, b, _ := p.next(); !step || b != nil {
		t.Fatalf("next() = %v, %+v, want the step that Rebuild asked for", step, b)
	}
	if st := p.Status(); !st.Pending || st.Running != nil {
		t.Errorf("Status() = %+v during the step, want the build still wanted", st)
	}
	p.step(t.Context())

	_, b2, _ := p.next()
	if b2 == nil || !slices.Equal(reasons(b2.Requests), []string{"rebuild", "operation"}) || !b2.Started.Equal(fixedLater) {
		t.Fatalf("next() = %+v, want the build of the rebuild and the operation, which the first build did not cover", b2)
	}
	res := &build.Result{Release: "0123456789abcdef"}
	p.end(b2, res, nil)
	st = p.Status()
	if st.Running != nil || st.Pending || st.Last == nil || st.Last.Err != nil || st.Last.Result == res || st.Last.Result.Release != res.Release {
		t.Errorf("Status() = %+v, want the second build ended with a copy of its result, and nothing wanted", st)
	}
	if err := p.Wait(canceled); err != nil {
		t.Errorf("Wait() error = %v with nothing running, wanted or due, want nil", err)
	}
	if step, b, _ := p.next(); step || b != nil {
		t.Errorf("next() = %v, %+v with nothing wanted or due", step, b)
	}
}

// TestResume checks Resume with the state set by hand: it ends a pause and asks for a build, and does nothing without one.
func TestResume(t *testing.T) {
	t.Parallel()
	p := quietPublisher(t)
	p.wanted = nil
	p.Resume()
	if st := p.Status(); st.Pending || st.Paused {
		t.Errorf("Status() after a Resume without a pause = %+v, want nothing wanted", st)
	}

	p.paused = true
	p.Resume()
	st := p.Status()
	if st.Pending != true || st.Paused || !slices.Equal(reasons(p.wanted), []string{"resume"}) || !p.wanted[0].At.Equal(fixedNow) {
		t.Errorf("Status() after Resume = %+v with the requests %+v, want the pause ended and a build wanted", st, p.wanted)
	}
	p.Resume()
	if len(p.wanted) != 1 {
		t.Errorf("a second Resume has asked for %d builds, want one", len(p.wanted))
	}
}

// TestPublisherStart checks that the Publisher builds the store at its start, to the release of the site directory.
func TestPublisherStart(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	if st := r.Status(); st.Running != nil || st.Last != nil || !st.Pending || st.Paused {
		t.Errorf("Status() before Run = %+v, want a build that is due, and nothing else", st)
	}
	before := snapshotOf(t, r.s)
	r.start()
	r.idle()
	sameDocuments(t, "the build", before, snapshotOf(t, r.s))

	fromDir, _ := buildSite(t, openSite(t, sampleDir))
	st := r.Status()
	if st.Running != nil || st.Pending || st.Paused {
		t.Errorf("Status() = %+v, want no build running, due or paused", st)
	}
	b := wantBuild(t, st, "start")
	if b.Result.Release != fromDir.Release || b.Result.Pages != fromDir.Pages || b.Result.Files != fromDir.Files || !b.Result.Changed {
		t.Errorf("the start build published %+v, want the release, pages and files of %+v, changed", b.Result, fromDir)
	}
	if want := []Request{{At: fixedNow, Reason: "start"}}; !reflect.DeepEqual(b.Requests, want) {
		t.Errorf("the requests of the start build are %+v, want %+v", b.Requests, want)
	}
	if !b.Started.Equal(fixedNow) || !b.Ended.Equal(fixedNow) {
		t.Errorf("the start build ran from %v to %v, want the time of the clock, %v", b.Started, b.Ended, fixedNow)
	}

	release := filepath.Join(r.opts.Out, "releases", b.Result.Release)
	wantManifest := string(readFile(t, filepath.Join(goldenDir, "manifest.txt")))
	if got := manifest(t, release); got != wantManifest {
		t.Errorf("the manifest of the release differs from the golden file: %s", firstDiff(got, wantManifest))
	}
	wantWarnings := string(readFile(t, filepath.Join(goldenDir, "warnings.txt")))
	if got := warnings(b.Result); got == "" || got != wantWarnings {
		t.Errorf("the warnings of the start build differ from the golden file: %s", firstDiff(got, wantWarnings))
	}
	r.wantReleases(b.Result.Release, b.Result.Release)
}

// TestPublishDraft checks which operations ask for a build: saving a revision does not, publishing and unpublishing do,
// and so does only what changes the store.
func TestPublishDraft(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	h := holdBuild(t, r.Publisher, 0)
	r.start()
	r.idle()
	start := r.Status().Last
	startRelease := r.current()

	rev := r.saveRevision(draftEN, careersRevision)
	other := must(t)(r.Editor().CreateEntry(t.Context(), r.op(), content.TypePage, "en"))
	r.idle()
	if st := r.Status(); st.Pending || !reflect.DeepEqual(st.Last, start) || h.builds() != 1 {
		t.Errorf("after the revision and a new entry: Status() = %+v after %d builds, want only the start build", st, h.builds())
	}
	if r.file(careersPage) != nil || other.Publishes {
		t.Errorf("a draft is published before Publish")
	}

	pub := r.publish(draftEN, rev.Revision)
	r.idle()
	st := r.Status()
	b := wantBuild(t, st, "operation")
	if got := b.Requests[0]; !got.At.Equal(fixedNow) || !reflect.DeepEqual(got.Result, pub) {
		t.Errorf("the request of Publish is %+v, want its result %+v at %v", got, pub, fixedNow)
	}
	if st.Pending || st.Running != nil || h.builds() != 2 {
		t.Errorf("after Publish: Status() = %+v after %d builds, want one build for it", st, h.builds())
	}
	if len(r.file(careersPage)) == 0 {
		t.Errorf("%s is not published", careersPage)
	}
	publishedRelease := r.current()
	if publishedRelease == startRelease || b.Result.Release != publishedRelease || !b.Result.Changed {
		t.Errorf("the build of Publish made %+v, want another release than %s, published", b.Result, startRelease)
	}
	r.wantReleases(publishedRelease, startRelease, publishedRelease)

	// Publishing what is published saves nothing, and asks for no build.
	again := r.publish(draftEN, rev.Revision)
	r.idle()
	if again.Publishes || !reflect.DeepEqual(r.Status(), st) || h.builds() != 2 {
		t.Errorf("Publish again: Publishes = %v, Status() = %+v after %d builds, want no build", again.Publishes, r.Status(), h.builds())
	}

	un := r.unpublish(draftEN)
	r.idle()
	st = r.Status()
	b = wantBuild(t, st, "operation")
	if !reflect.DeepEqual(b.Requests[0].Result, un) || h.builds() != 3 {
		t.Errorf("the request of Unpublish is %+v after %d builds, want its result %+v", b.Requests[0].Result, h.builds(), un)
	}
	if r.file(careersPage) != nil {
		t.Errorf("%s is still published", careersPage)
	}
	if b.Result.Release == publishedRelease || b.Result.Release != startRelease {
		t.Errorf("the release after Unpublish is %s, want the start release %s, which is not %s", b.Result.Release, startRelease, publishedRelease)
	}
	r.wantReleases(startRelease, startRelease, publishedRelease)
}

// TestPublishRefused checks that a publish that the checks refuse asks for no build, and changes neither the store nor
// the published site.
func TestPublishRefused(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	h := holdBuild(t, r.Publisher, 0)
	r.start()
	r.idle()
	start := r.Status()
	release := r.current()

	rev := r.saveRevision(draftEN, feedRevision)
	before := snapshotOf(t, r.s)
	seq := r.doc(content.RefEntry, draftEN).Seq

	_, err := r.Editor().Publish(t.Context(), r.op(), draftEN, seq, rev.Revision)
	const problem = `/slug: output path "/en/feed/" is also that of the feed in en`
	if err == nil || !strings.Contains(err.Error(), problem) || !strings.HasPrefix(err.Error(), "revisions/"+rev.Revision+".json:") {
		t.Fatalf("Publish() error = %v, want %s in the revision", err, problem)
	}
	r.idle()
	if got := r.Status(); !reflect.DeepEqual(got, start) || h.builds() != 1 {
		t.Errorf("Status() = %+v after %d builds, want %+v: the publish asked for no build", got, h.builds(), start)
	}
	r.wantReleases(release, release)
	sameDocuments(t, "the failed Publish", before, snapshotOf(t, r.s))
	if state, latest := r.entryState(draftEN); state.Status != content.StatusDraft || latest != rev.Revision {
		t.Errorf("the entry is %+v with the latest revision %s, want a draft with %s", state, latest, rev.Revision)
	}
}

// TestCoalesce checks that the requests that come while a build runs make one more build, which covers all of them, and
// that the operations do not wait for the build.
func TestCoalesce(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	h := holdBuild(t, r.Publisher, 1)
	r.start()
	h.await(t) // the start build has read the store and holds

	startSite, _ := buildSite(t, openSite(t, sampleDir))
	var saved []edit.Result
	for _, name := range []string{"News 1", "News 2", "News 3"} {
		saved = append(saved, r.renameNews(name))
	}
	st := r.Status()
	if st.Running == nil || !st.Pending || !reflect.DeepEqual(st.Running.Requests, []Request{{At: fixedNow, Reason: "start"}}) {
		t.Errorf("Status() during the start build = %+v, want it running with its request and a build due", st)
	}
	h.release()
	r.idle()

	st = r.Status()
	b := wantBuild(t, st, "operation", "operation", "operation")
	for i, q := range b.Requests {
		if !reflect.DeepEqual(q.Result, saved[i]) || q.Result.Kind != content.RefTerm || !q.At.Equal(fixedNow) {
			t.Errorf("request %d of the last build is %+v, want the result of save %d, %+v", i, q, i, saved[i])
		}
	}
	if st.Pending || st.Running != nil || h.builds() != 2 {
		t.Errorf("Status() = %+v after %d builds, want the start build and one more, and none due", st, h.builds())
	}
	if b.Result.Release == startSite.Release || !b.Result.Changed {
		t.Errorf("the last build published %+v, want another release than the start build's %s", b.Result, startSite.Release)
	}
	r.wantStoreBuilt()
	r.wantReleases(b.Result.Release, startSite.Release, b.Result.Release)
	if page := string(r.file(newsPage)); !strings.Contains(page, `content="News 3"`) || strings.Contains(page, `content="News 2"`) {
		t.Errorf("the page of the term has no longer the name of the last save, News 3")
	}
}

// TestBuildFailure checks that a build that fails leaves the operation in the store, and the site as it was, and shows
// the problems; and that the next build, when the cause is gone, publishes.
func TestBuildFailure(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	h := holdBuild(t, r.Publisher, 0)
	r.start()
	r.idle()
	release := r.current()

	// The save accepts a redirect from the path of the file of a medium, which only the build can see (item 151 of 6.12).
	data, from := r.badRedirect()
	created := must(t)(r.Editor().Create(t.Context(), r.op(), content.RefRedirect, data))
	r.idle()

	st := r.Status()
	if st.Running != nil || st.Pending || st.Last == nil || st.Last.Result != nil || h.builds() != 2 {
		t.Fatalf("Status() = %+v after %d builds, want a build that failed before publishing, and none due", st, h.builds())
	}
	var problems content.Errors
	if !errors.As(st.Last.Err, &problems) {
		t.Fatalf("the error of the build is %v, want content.Errors", st.Last.Err)
	}
	line := regexp.MustCompile(`^redirects/` + created.ID + `\.json:\d+:\d+: /from: from "` + regexp.QuoteMeta(from) +
		`" is the output path "` + regexp.QuoteMeta(from) + `" of media/` + pdfMedium + `\.json$`)
	if len(problems) != 1 || !line.MatchString(problems[0].Error()) {
		t.Errorf("the problems of the build are:\n%v\nwant the one line that matches %s", problems, line)
	}
	if got := st.Last.Requests; len(got) != 1 || got[0].Reason != "operation" || !reflect.DeepEqual(got[0].Result, created) {
		t.Errorf("the requests of the failed build are %+v, want the creation of the redirect", got)
	}
	r.wantReleases(release, release)
	var stored content.Redirect
	d := r.doc(content.RefRedirect, created.ID)
	if err := stored.UnmarshalJSON(d.Data); err != nil || stored.From != from || d.Version != created.Version {
		t.Errorf("the redirect in the store is %+v, %v at %+v; want the one that was created at %+v: the operation stays", stored, err, d.Version, created.Version)
	}

	deleted := must(t)(r.Editor().Delete(t.Context(), r.op(), content.RefRedirect, created.ID, created.Version.Seq))
	r.idle()
	b := wantBuild(t, r.Status(), "operation")
	if !reflect.DeepEqual(b.Requests[0].Result, deleted) || b.Result.Release != release || b.Result.Changed {
		t.Errorf("the build after Delete = %+v, want the release %s again, unchanged", b, release)
	}
	r.wantReleases(release, release)
}

// badRedirect returns a redirect from the path of the file of the medium pdfMedium, which only the build refuses, and the
// path.
func (r *rig) badRedirect() (data []byte, from string) {
	r.t.Helper()
	var medium content.Media
	if err := medium.UnmarshalJSON(r.doc(content.RefMedia, pdfMedium).Data); err != nil {
		r.t.Fatal(err)
	}
	from = "/media/" + pdfMedium + "/" + medium.FileName
	data, err := content.Redirect{From: from, To: "/services/", Status: 301}.MarshalJSON()
	if err != nil {
		r.t.Fatal(err)
	}
	return data, from
}

// TestRequestDuringFailure checks that a request that comes while a build runs gets its build also when that build
// fails, and that the build which fails does not see the change that came after its snapshot.
func TestRequestDuringFailure(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	h := holdBuild(t, r.Publisher, 2)
	r.start()
	r.idle()
	release := r.current()

	data, from := r.badRedirect()
	created := must(t)(r.Editor().Create(t.Context(), r.op(), content.RefRedirect, data))
	h.await(t) // the build that is to fail has read the redirect
	deleted := must(t)(r.Editor().Delete(t.Context(), r.op(), content.RefRedirect, created.ID, created.Version.Seq))
	h.release()
	r.idle()

	b := wantBuild(t, r.Status(), "operation")
	if !reflect.DeepEqual(b.Requests[0].Result, deleted) || b.Result.Release != release || b.Result.Changed || h.builds() != 3 {
		t.Errorf("the last build = %+v after %d builds, want the one for Delete, which publishes the release %s unchanged",
			b, h.builds(), release)
	}
	// The build before it failed for the redirect that it had read, and did not publish.
	failed := h.lastBefore(3)
	if failed == nil || failed.Result != nil || failed.Err == nil || !strings.Contains(failed.Err.Error(), `/from: from "`+from+`" is the output path`) ||
		!slices.Equal(reasons(failed.Requests), []string{"operation"}) || !reflect.DeepEqual(failed.Requests[0].Result, created) {
		t.Errorf("the build that read the redirect = %+v, want it to have failed for the redirect that Create made", failed)
	}
	r.wantReleases(release, release)
}

// TestRebuild checks that Rebuild builds the store as it is: the same release.
func TestRebuild(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	h := holdBuild(t, r.Publisher, 0)
	r.start()
	r.idle()
	first := wantBuild(t, r.Status(), "start")

	r.Rebuild()
	r.idle()
	b := wantBuild(t, r.Status(), "rebuild")
	if want := []Request{{At: fixedNow, Reason: "rebuild"}}; !reflect.DeepEqual(b.Requests, want) {
		t.Errorf("the requests of the build are %+v, want %+v", b.Requests, want)
	}
	if b.Result.Release != first.Result.Release || b.Result.Changed || h.builds() != 2 {
		t.Errorf("the build made %+v after %d builds, want the release %s, unchanged", b.Result, h.builds(), first.Result.Release)
	}
}

// TestRollback checks the rollback to the first release: the pause, the operations that are saved during it, and Resume.
func TestRollback(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	h := holdBuild(t, r.Publisher, 0)
	r.start()
	r.idle()
	first := r.current()
	r.renameNews("News 1")
	r.idle()
	second := r.current()
	if second == first || !strings.Contains(string(r.file(newsPage)), `content="News 1"`) {
		t.Fatalf("the release after the first change is %s, want another than %s, with the new name", second, first)
	}
	r.wantReleases(second, first, second)

	if err := r.Rollback(t.Context(), first); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	if st := r.Status(); !st.Paused || st.Running != nil || st.Pending {
		t.Errorf("Status() after Rollback = %+v, want the builds paused, and none running or due", st)
	}
	r.wantReleases(first, first, second)
	if strings.Contains(string(r.file(newsPage)), `content="News 1"`) {
		t.Errorf("the page of the term has the new name after the rollback")
	}

	// During the pause an operation is saved, its build is due, and the site stays.
	rev := r.saveRevision(draftEN, careersRevision)
	pub := r.publish(draftEN, rev.Revision)
	r.idle() // returns although a build is due
	st := r.Status()
	if !st.Paused || !st.Pending || st.Running != nil || h.builds() != 2 {
		t.Errorf("Status() during the pause = %+v after %d builds, want it paused with a build due", st, h.builds())
	}
	if state, _ := r.entryState(draftEN); state.Status != content.StatusPublished || state.PublishedRevision != rev.Revision {
		t.Errorf("the entry is %+v during the pause, want it published: the operation is saved", state)
	}
	if r.current() != first || r.file(careersPage) != nil {
		t.Errorf("the site changed during the pause: %s is published", r.current())
	}

	r.Resume()
	r.idle()
	st = r.Status()
	b := wantBuild(t, st, "operation", "resume")
	if !reflect.DeepEqual(b.Requests[0].Result, pub) || st.Paused || st.Pending || h.builds() != 3 {
		t.Errorf("Status() after Resume = %+v after %d builds, want one build for the publish and the resume, not paused", st, h.builds())
	}
	if len(r.file(careersPage)) == 0 || !strings.Contains(string(r.file(newsPage)), `content="News 1"`) {
		t.Errorf("the build after Resume lacks the careers page or the new name of the term: it should publish the store as it is")
	}
	third := r.current()
	if third == first || third == second || b.Result.Release != third {
		t.Errorf("the release after Resume is %s, want one that is not %s or %s", third, first, second)
	}
	r.wantStoreBuilt()
	r.wantReleases(third, first, third)
}

// TestRollbackWaits checks that Rollback waits for the build that runs, which publishes first, and that no build starts
// meanwhile, although an operation asks for one.
func TestRollbackWaits(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	h := holdBuild(t, r.Publisher, 2)
	r.start()
	r.idle()
	first := r.current()
	r.renameNews("News 1")
	h.await(t) // the second build holds

	errc := make(chan error, 1)
	go func() { errc <- r.Rollback(t.Context(), first) }()
	r.until("the rollback runs", func() bool { return r.rolling })
	r.renameNews("News 2") // takes some time, in which a rollback that did not wait would be done
	select {
	case err := <-errc:
		t.Fatalf("Rollback() returned %v while a build runs", err)
	default:
	}
	if st := r.Status(); !st.Paused || st.Running == nil || !st.Pending || r.current() != first {
		t.Errorf("Status() = %+v and %s published, want a build running, a build due and the builds paused, with the first release", st, r.current())
	}

	h.release()
	if err := <-errc; err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	st := r.Status()
	b := wantBuild(t, st, "operation")
	if !st.Paused || !st.Pending || st.Running != nil || h.builds() != 2 {
		t.Errorf("Status() after Rollback = %+v after %d builds, want it paused, with the build of News 2 due and not started", st, h.builds())
	}
	if b.Result.Release == first || !b.Result.Changed {
		t.Errorf("the build that ran published %+v, want a release that is not the first, %s", b.Result, first)
	}
	r.wantReleases(first, first, b.Result.Release)
}

// TestRollbackErrors checks that a Rollback that fails leaves the builds and the site as they were.
func TestRollbackErrors(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	h := holdBuild(t, r.Publisher, 2)
	r.start()
	r.idle()
	first := r.current()

	const invalid = `invalid release name "latest"`
	if err := r.Rollback(t.Context(), "latest"); err == nil || err.Error() != invalid {
		t.Errorf(`Rollback("latest") error = %v, want %s`, err, invalid)
	}
	unknown := "0123456789abcdef"
	if err := r.Rollback(t.Context(), unknown); err == nil || !strings.HasSuffix(err.Error(), filepath.Join("releases", unknown)+": no such release") {
		t.Errorf("Rollback() of an unknown release: error = %v, want no such release", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.Rollback(canceled, first); !errors.Is(err, context.Canceled) {
		t.Errorf("Rollback() with a canceled context: error = %v, want %v", err, context.Canceled)
	}
	if st := r.Status(); st.Paused {
		t.Errorf("Status() = %+v after Rollbacks that failed, want the builds not paused", st)
	}

	// The builds go on, and a Rollback that waits for one can be given up, or refused while another one waits.
	r.renameNews("News 1")
	h.await(t)
	ctx, giveUp := context.WithCancel(t.Context())
	errc := make(chan error, 1)
	go func() { errc <- r.Rollback(ctx, first) }()
	r.until("the rollback runs", func() bool { return r.rolling })
	// A Rollback that is not refused waits for the held build, so that this one gives up after a minute.
	second, cancelSecond := context.WithTimeout(t.Context(), time.Minute)
	defer cancelSecond()
	if err := r.Rollback(second, first); err == nil || err.Error() != "another rollback is running" {
		t.Errorf("Rollback() while another one runs: error = %v, want it refused", err)
	}
	giveUp()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Errorf("Rollback() given up: error = %v, want %v", err, context.Canceled)
	}
	if st := r.Status(); st.Paused || st.Running == nil {
		t.Errorf("Status() = %+v after the rollback was given up, want the build running, and not paused", st)
	}
	h.release()
	r.idle()
	b := wantBuild(t, r.Status(), "operation")
	r.wantReleases(b.Result.Release, first, b.Result.Release)
}

// TestRestart checks that a new Publisher over the same directories is not paused, and builds the store at its start.
func TestRestart(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rn := r.start()
	r.idle()
	first := r.current()
	r.renameNews("News 1")
	r.idle()
	second := r.current()
	if err := r.Rollback(t.Context(), first); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	r.renameNews("News 2")
	if st := r.Status(); !st.Paused || !st.Pending || r.current() != first {
		t.Fatalf("Status() = %+v with %s published, want it paused with a build due, and the first release", st, r.current())
	}
	if err := rn.stop(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	p, err := New(r.opts)
	if err != nil {
		t.Fatal(err)
	}
	again := &rig{Publisher: p, t: t, s: r.s, opts: r.opts}
	if st := again.Status(); st.Paused || !st.Pending || st.Last != nil || st.Running != nil {
		t.Errorf("Status() of the new Publisher = %+v, want it not paused, with only its start build due", st)
	}
	again.start()
	again.idle()
	b := wantBuild(t, again.Status(), "start")
	if b.Result.Release == first || b.Result.Release == second {
		t.Errorf("the start build published %s, want a release with both new names, not %s or %s", b.Result.Release, first, second)
	}
	again.wantStoreBuilt()
	again.wantReleases(b.Result.Release, first, b.Result.Release)
}

// TestRunCancel checks that Run waits for the build that runs, whose cancellation leaves the site as it was, and that
// it returns nil.
func TestRunCancel(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	h := holdBuild(t, r.Publisher, 2)
	rn := r.start()
	r.idle()
	first := r.current()
	change := r.renameNews("News 1")
	h.await(t)

	rn.cancel()
	select {
	case <-rn.done:
		t.Fatal("Run returned while a build runs")
	default:
	}
	if st := r.Status(); st.Running == nil {
		t.Errorf("Status() = %+v after the context ended, want the build to run until it stops", st)
	}
	h.release()
	<-rn.done
	if rn.err != nil {
		t.Fatalf("Run() error = %v, want nil", rn.err)
	}
	st := r.Status()
	if st.Running != nil || st.Last == nil || !errors.Is(st.Last.Err, context.Canceled) || st.Last.Result != nil {
		t.Errorf("Status() = %+v, want the build to have stopped with %v and no result", st, context.Canceled)
	}
	if got := st.Last.Requests; len(got) != 1 || !reflect.DeepEqual(got[0].Result, change) {
		t.Errorf("the requests of the canceled build are %+v, want the rename", got)
	}
	r.wantReleases(first, first)
	if entries, err := os.ReadDir(filepath.Join(r.opts.Out, "releases")); err != nil || len(entries) != 1 {
		t.Errorf("releases/ has %v, %v; want only the first release", entries, err)
	}

	// Run has returned: what is asked for stays due.
	r.renameNews("News 2")
	if st := r.Status(); !st.Pending || st.Running != nil || h.builds() != 2 {
		t.Errorf("Status() = %+v after %d builds, want a build due and none started", st, h.builds())
	}
}

// TestRunTwice checks that Run can be called only once.
func TestRunTwice(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rn := r.start()
	r.idle()
	// With a context that has ended, a Run that is not refused returns nil at once.
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.Run(canceled); err == nil || err.Error() != "the publisher has run already" {
		t.Errorf("Run() while it runs: error = %v, want it refused", err)
	}
	if err := rn.stop(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if err := r.Run(canceled); err == nil || err.Error() != "the publisher has run already" {
		t.Errorf("Run() after it has returned: error = %v, want it refused", err)
	}
}

// TestWait checks that Wait returns when no build runs or is due, and with the error of its context otherwise.
func TestWait(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	h := holdBuild(t, r.Publisher, 1)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	if err := r.Wait(canceled); !errors.Is(err, context.Canceled) {
		t.Errorf("Wait() before Run: error = %v, want %v: the start build is due", err, context.Canceled)
	}
	r.start()
	h.await(t)
	if err := r.Wait(canceled); !errors.Is(err, context.Canceled) {
		t.Errorf("Wait() during a build: error = %v, want %v", err, context.Canceled)
	}
	done := make(chan error, 1)
	go func() { done <- r.Wait(t.Context()) }()
	h.release()
	if err := <-done; err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if st := r.Status(); st.Running != nil || st.Pending || st.Last == nil {
		t.Errorf("Status() after Wait = %+v, want the build to have ended", st)
	}
	if err := r.Wait(canceled); err != nil {
		t.Errorf("Wait() with nothing to wait for, and a canceled context: error = %v, want nil", err)
	}
}

// text returns st as text, which shares nothing with it, so that a later change of the Publisher or of st does not change
// it.
func (st Status) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "pending=%v paused=%v\n", st.Pending, st.Paused)
	for _, x := range []struct {
		name  string
		build *Build
	}{{"running", st.Running}, {"last", st.Last}} {
		if x.build == nil {
			fmt.Fprintf(&b, "%s: none\n", x.name)
			continue
		}
		fmt.Fprintf(&b, "%s: started=%v ended=%v err=%v\n", x.name, x.build.Started, x.build.Ended, x.build.Err)
		for _, q := range x.build.Requests {
			res := q.Result
			res.Warnings = nil
			fmt.Fprintf(&b, "  request %v %s %+v\n", q.At, q.Reason, res)
			for _, w := range q.Result.Warnings {
				fmt.Fprintf(&b, "    warning %v\n", w)
			}
		}
		if r := x.build.Result; r != nil {
			fmt.Fprintf(&b, "  result %s changed=%v pages=%d files=%d\n", r.Release, r.Changed, r.Pages, r.Files)
			for _, w := range r.Warnings {
				fmt.Fprintf(&b, "    warning %v\n", w)
			}
		}
	}
	for _, f := range st.Schedules {
		fmt.Fprintf(&b, "schedule %q at=%v tried=%v err=%v\n", f.Entry, f.At, f.Tried, f.Err)
	}
	return b.String()
}

// TestStatusCopies checks that Status returns copies: changing one changes neither the Publisher nor the next one.
func TestStatusCopies(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	h := holdBuild(t, r.Publisher, 2)
	r.start()
	r.idle()
	un := r.unpublish(postJA) // a redirect to the post is left out: a warning
	h.await(t)

	st := r.Status()
	if st.Running == nil || st.Last == nil || len(st.Last.Result.Warnings) == 0 || len(un.Warnings) == 0 ||
		len(st.Running.Requests) != 1 || !reflect.DeepEqual(st.Running.Requests[0].Result, un) {
		t.Fatalf("Status() = %+v, want a running build for Unpublish with a warning, and the last one with warnings", st)
	}
	want := st.text()
	// The caller of the operation has the same result.
	warning := un.Warnings[0]
	un.Warnings[0] = nil
	if got := r.Status().text(); got != want {
		t.Fatalf("Status() after the caller changed the result of its operation is\n%swant\n%s", got, want)
	}
	un.Warnings[0] = warning
	for range 2 {
		got := r.Status()
		if text := got.text(); text != want {
			t.Fatalf("Status() is\n%swant\n%s", text, want)
		}
		got.Running.Requests[0].Reason = "changed"
		got.Running.Requests[0].Result.Warnings[0] = nil
		got.Running.Requests = append(got.Running.Requests, Request{})
		got.Running.Started = time.Time{}
		got.Last.Requests[0].Reason = "changed"
		got.Last.Result.Release = "changed"
		got.Last.Result.Warnings[0] = nil
		got.Last.Err = errors.New("changed")
		got.Last.Ended = time.Time{}
		got.Pending, got.Paused = !got.Pending, true
	}
	h.release()
	r.idle()
	if end := r.Status(); end.Last == nil || !reflect.DeepEqual(end.Last.Requests, st.Running.Requests) || !end.Last.Started.Equal(st.Running.Started) {
		t.Errorf("Status() after the build = %+v, want it to have ended with the requests and the start it had running", end)
	}
}

// TestRunCanceledAtStart checks that Run does not build when its context has ended, and returns nil.
func TestRunCanceledAtStart(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	h := holdBuild(t, r.Publisher, 0)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.Run(canceled); err != nil {
		t.Errorf("Run() error = %v, want nil", err)
	}
	if st := r.Status(); st.Last != nil || st.Running != nil || !st.Pending || h.builds() != 0 {
		t.Errorf("Status() = %+v after %d builds, want only the start build wanted", st, h.builds())
	}
}

// TestRollbackStress rolls back and resumes while operations, builds and readers go on: no build fails because another
// one, or a rollback, runs, and when it is quiet the site that is published is that of the store.
func TestRollbackStress(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.start()
	r.idle()

	stop := make(chan struct{})
	var readers, writers sync.WaitGroup
	readers.Go(func() {
		for {
			if st := r.Status(); st.Last != nil && (st.Last.Err != nil || st.Last.Result == nil) {
				t.Errorf("the last build = %+v, want it to have published", st.Last)
			}
			select {
			case <-stop:
				return
			default:
				runtime.Gosched()
			}
		}
	})
	writers.Go(func() {
		for n := range 8 {
			if _, err := r.rename(newsTerm, fmt.Sprintf("News %d", n)); err != nil {
				t.Errorf("rename() error = %v", err)
				return
			}
		}
	})
	writers.Go(func() {
		for range 4 {
			rel, err := r.Releases()
			if err != nil || len(rel.Names) == 0 {
				t.Errorf("Releases() = %+v, %v; want the releases", rel, err)
				return
			}
			// A release that a build has removed since it was listed is no longer there.
			err = r.Rollback(t.Context(), rel.Names[0])
			if err != nil && !strings.HasSuffix(err.Error(), ": no such release") {
				t.Errorf("Rollback(%s) error = %v", rel.Names[0], err)
			}
			r.Resume()
		}
	})
	writers.Wait()
	r.Resume()
	r.idle()
	close(stop)
	readers.Wait()

	if st := r.Status(); st.Pending || st.Running != nil || st.Paused || st.Last == nil || st.Last.Err != nil {
		t.Errorf("Status() = %+v, want it quiet after a build that published", st)
	}
	r.wantStoreBuilt()
}

// TestSnapshotFailure checks that a build that cannot read the store fails with that, and that the Publisher goes on.
func TestSnapshotFailure(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	h := holdBuild(t, r.Publisher, 0)
	if err := r.s.Close(); err != nil {
		t.Fatal(err)
	}
	failed := func(reason string) {
		t.Helper()
		st := r.Status()
		b := st.Last
		if b == nil || b.Result != nil || b.Err == nil || !strings.HasPrefix(b.Err.Error(), "reading the store: ") ||
			!strings.Contains(b.Err.Error(), "closed") || !slices.Equal(reasons(b.Requests), []string{reason}) {
			t.Fatalf("Status() = %+v, want a build for the %s that failed to read the store", st, reason)
		}
		if st.Running != nil || st.Pending {
			t.Errorf("Status() = %+v, want no build running or due", st)
		}
	}
	r.start()
	r.idle()
	failed("start")
	r.Rebuild()
	r.idle()
	failed("rebuild")
	if h.builds() != 0 {
		t.Errorf("%d builds read the store, want none", h.builds())
	}
	if rel, err := r.Releases(); err != nil || rel.Current != "" || len(rel.Names) != 0 {
		t.Errorf("Releases() = %+v, %v; want none: nothing was built", rel, err)
	}
}

// TestNew checks the errors of New.
func TestNew(t *testing.T) {
	t.Parallel()
	good := Options{Store: newStore(t), Schema: schemaFile(t), Theme: exampleTheme(t), Out: filepath.Join(t.TempDir(), "out"), CacheDir: t.TempDir()}
	for _, tt := range []struct {
		name   string
		change func(*Options)
		want   string
	}{
		{"no store", func(o *Options) { o.Store = nil }, "no store"},
		{"no output directory", func(o *Options) { o.Out = "" }, "no output directory"},
		{"no cache directory", func(o *Options) { o.CacheDir = "" }, "no cache directory"},
		{"no theme", func(o *Options) { o.Theme = nil }, "no theme"},
		{"negative memory", func(o *Options) { o.Memory = -1 }, "memory -1 is out of range (0 or more)"},
		{"negative workers", func(o *Options) { o.Workers = -1 }, "workers -1 is out of range (0 or more)"},
		{"a theme that does not load", func(o *Options) { o.Theme = fstest.MapFS{} }, "theme/theme.yaml: missing file\ntheme/templates: missing directory"},
	} {
		o := good
		tt.change(&o)
		if p, err := New(o); err == nil || err.Error() != tt.want || p != nil {
			t.Errorf("%s: New() = %v, %v; want the error %q", tt.name, p, err, tt.want)
		}
	}

	p, err := New(good)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if p.Editor() == nil {
		t.Errorf("Editor() = nil")
	}
	if st := p.Status(); st.Running != nil || st.Last != nil || !st.Pending || st.Paused {
		t.Errorf("Status() = %+v, want only the start build due", st)
	}
	if _, err := os.Stat(good.Out); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("New() made the output directory: Stat() error = %v", err)
	}

	// Without a clock the Publisher has the time.
	before := time.Now()
	q, err := New(good)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	after := time.Now()
	if len(q.wanted) != 1 {
		t.Fatalf("the new Publisher wants the requests %+v, want only the start", q.wanted)
	}
	if at := q.wanted[0].At; at.Before(before) || at.After(after) {
		t.Errorf("the start was requested at %v, want a time from %v to %v", at, before, after)
	}
	if _, err := p.Releases(); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Releases() before the first build: error = %v, want one that wraps fs.ErrNotExist", err)
	}
}

// TestBuildOptions checks that the options of the Publisher reach the build, with a snapshot of the store as its source.
func TestBuildOptions(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.opts.Memory, r.opts.Workers = 1<<28, 3
	p, err := New(r.opts)
	if err != nil {
		t.Fatal(err)
	}
	r.Publisher = p
	var got build.Options
	p.beforeBuild = func(o build.Options) { got = o }
	r.start()
	r.idle()

	src, ok := got.Source.(*Source)
	if !ok {
		t.Fatalf("the source of the build is %T, want a *Source", got.Source)
	}
	settings, schema, err := src.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if want := r.doc(store.SettingsKind, ""); string(settings.Data) != string(want.Data) || !sameFile(schema, r.opts.Schema) {
		t.Errorf("the source of the build has the settings %s and the schema %s, want those of the store and the option", describe(settings), describe(schema))
	}
	got.Source = nil
	if want := (build.Options{Out: r.opts.Out, CacheDir: r.opts.CacheDir, Memory: 1 << 28, Workers: 3}); got != want {
		t.Errorf("the options of the build are %+v, want %+v", got, want)
	}
}

// TestClock checks that the Editor of the Publisher has its clock: the time of the entries it creates.
func TestClock(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	res := must(t)(r.Editor().CreateEntry(t.Context(), r.op(), content.TypePage, "en"))
	state, _ := r.entryState(res.ID)
	if state.CreatedAt != "2026-11-08T21:00:00+09:00" {
		t.Errorf("createdAt = %q, want the time of the clock of the Publisher in the time zone of the site", state.CreatedAt)
	}
}

// TestManyOperations makes operations from several goroutines while builds run, Rebuild, Status and Releases are called
// all the time: no build fails, and when it is quiet the site that is published is that of the store.
func TestManyOperations(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.start()
	r.idle()
	var terms []string
	err := r.s.View(t.Context(), func(tx *store.Tx) error {
		docs, err := tx.Documents(content.RefTerm)
		for _, d := range docs {
			terms = append(terms, d.ID)
		}
		return err
	})
	if err != nil || len(terms) < 3 {
		t.Fatalf("the sample site has the terms %v, %v; want several", terms, err)
	}

	const saves = 6
	stop := make(chan struct{})
	var readers, writers sync.WaitGroup
	readers.Go(func() {
		for {
			st := r.Status()
			if st.Running != nil && len(st.Running.Requests) == 0 {
				t.Errorf("Status() = %+v, want a running build to cover a request", st)
			}
			if st.Last != nil && (st.Last.Err != nil || st.Last.Result == nil) {
				t.Errorf("the last build = %+v, want it to have published", st.Last)
			}
			if _, err := r.Releases(); err != nil {
				t.Errorf("Releases() error = %v", err)
			}
			select {
			case <-stop:
				return
			default:
				runtime.Gosched()
			}
		}
	})
	writers.Go(func() {
		for range saves {
			r.Rebuild()
		}
	})
	for i, id := range terms {
		writers.Go(func() {
			for n := range saves {
				if _, err := r.rename(id, "Term "+string(rune('A'+i))+string(rune('0'+n))); err != nil {
					t.Errorf("rename(%s) error = %v", id, err)
					return
				}
			}
		})
	}
	writers.Wait()
	r.idle()
	close(stop)
	readers.Wait()

	st := r.Status()
	if st.Pending || st.Running != nil || st.Paused || st.Last == nil || st.Last.Err != nil || st.Last.Result == nil {
		t.Errorf("Status() = %+v, want it quiet after a build that published", st)
	}
	for _, q := range st.Last.Requests {
		if q.Reason != "operation" && q.Reason != "rebuild" {
			t.Errorf("a request of the last build has the reason %q", q.Reason)
		}
	}
	r.wantStoreBuilt()
}
