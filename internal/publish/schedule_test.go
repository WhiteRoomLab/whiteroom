package publish

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// otherUser schedules in the tests, as another user than the one that does the other operations.
const otherUser = "usr_01JB8T7H4MA2C5E8G1J4K7N3P7"

// jst is the offset of the time zone of the sample site.
var jst = time.FixedZone("", 9*60*60)

// clock is the clock of a Publisher and its Editor, which a test sets.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// add moves the clock forward by d, and returns the new time.
func (c *clock) add(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
	return c.t
}

// timers makes the timers of a Publisher in place of time.AfterFunc. None fires by itself: the test fires them.
type timers struct {
	mu   sync.Mutex
	made []*timer
}

type timer struct {
	ts      *timers
	d       time.Duration // that it was armed for
	f       func()
	stopped bool
	fired   bool
}

func (ts *timers) afterFunc(d time.Duration, f func()) stopper {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	tm := &timer{ts: ts, d: d, f: f}
	ts.made = append(ts.made, tm)
	return tm
}

// Stop reports whether the timer was armed, as time.Timer.Stop does.
func (tm *timer) Stop() bool {
	tm.ts.mu.Lock()
	defer tm.ts.mu.Unlock()
	armed := !tm.stopped && !tm.fired
	tm.stopped = true
	return armed
}

// fire runs the function of the timer in the goroutine of the caller, which has asked for a step when it returns, as it
// has when a time.Timer runs it in a goroutine of its own. It can be called for a timer that has been stopped, as a timer
// can fire as it is stopped.
func (tm *timer) fire() {
	tm.ts.mu.Lock()
	tm.fired = true
	tm.ts.mu.Unlock()
	tm.f()
}

// armed returns the timers that are armed: those that are neither stopped nor fired.
func (ts *timers) armed() []*timer {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	var out []*timer
	for _, tm := range ts.made {
		if !tm.stopped && !tm.fired {
			out = append(out, tm)
		}
	}
	return out
}

// want fails the test unless the timers that are armed are exactly those for the durations, in the order they were made.
func (ts *timers) want(t *testing.T, durations ...time.Duration) {
	t.Helper()
	var got []time.Duration
	for _, tm := range ts.armed() {
		got = append(got, tm.d)
	}
	if !slices.Equal(got, durations) {
		t.Errorf("the timers that are armed are for %v, want %v", got, durations)
	}
}

// fire fires the one timer that is armed.
func (ts *timers) fire(t *testing.T) {
	t.Helper()
	armed := ts.armed()
	if len(armed) != 1 {
		t.Fatalf("%d timers are armed, want one to fire", len(armed))
	}
	armed[0].fire()
}

// count returns the number of the timers that have been made.
func (ts *timers) count() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return len(ts.made)
}

// clockRig is a rig whose clock and timers the test drives.
type clockRig struct {
	*rig
	clock  *clock
	timers *timers
}

// newClockRig returns a clockRig of the imported sample site at fixedNow, whose Publisher has not started to run; mod, if
// it is not nil, changes the options.
func newClockRig(t *testing.T, mod func(*Options)) *clockRig {
	t.Helper()
	return newClockRigOn(t, importSample(t), &clock{t: fixedNow}, mod)
}

// newClockRigOn returns a clockRig of the store s with the clock clk, which another Publisher can share.
func newClockRigOn(t *testing.T, s *store.Store, clk *clock, mod func(*Options)) *clockRig {
	t.Helper()
	opts := Options{
		Store: s, Schema: schemaFile(t), Theme: exampleTheme(t), Out: t.TempDir(), CacheDir: t.TempDir(), Workers: 2, Now: clk.Now,
	}
	if mod != nil {
		mod(&opts)
	}
	p, err := New(opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ts := &timers{}
	p.afterFunc = ts.afterFunc
	return &clockRig{rig: &rig{Publisher: p, t: t, s: s, opts: opts}, clock: clk, timers: ts}
}

// schedule schedules the revision, the latest of the entry, at at as the user by, through the Editor.
func (r *clockRig) schedule(entry, revision string, at time.Time, by string) edit.Result {
	r.t.Helper()
	seq := r.doc(content.RefEntry, entry).Seq
	return must(r.t)(r.Editor().Schedule(r.t.Context(), edit.Op{By: by}, entry, seq, revision, at))
}

// newPage makes a draft page in en with a revision of the slug, and returns them.
func (r *clockRig) newPage(slug string) (entry, revision string) {
	r.t.Helper()
	entry = must(r.t)(r.Editor().CreateEntry(r.t.Context(), r.op(), content.TypePage, "en")).ID
	data := fmt.Sprintf(`{"schemaVersion":1,"title":%q,"slug":%q}`, strings.ToUpper(slug[:1])+slug[1:], slug)
	return entry, r.saveRevision(entry, data).Revision
}

func (r *clockRig) wantStatus(entry string, want content.Status) content.Entry {
	r.t.Helper()
	state, _ := r.entryState(entry)
	if state.Status != want {
		r.t.Errorf("the entry %s is %s, want %s", entry, state.Status, want)
	}
	return state
}

func (r *clockRig) wantPublished(entry, revision, by string) {
	r.t.Helper()
	state := r.wantStatus(entry, content.StatusPublished)
	if state.PublishedRevision != revision || state.ScheduledRevision != "" || state.ScheduledAt != "" {
		r.t.Errorf("the entry %s is %+v, want it published with %s and not scheduled", entry, state, revision)
	}
	if got := r.doc(content.RefEntry, entry).SavedBy; got != by {
		r.t.Errorf("the entry %s was saved by %s, want %s", entry, got, by)
	}
}

// runStep runs the step of the scheduler that is due, as Run does, in the goroutine of the test.
func (r *clockRig) runStep() {
	r.t.Helper()
	r.stepWith(r.t.Context())
}

// stepWith lets next decide, which must be a step of the scheduler, and makes it with ctx.
func (r *clockRig) stepWith(ctx context.Context) {
	r.t.Helper()
	if step, b, _ := r.next(); !step || b != nil {
		r.t.Fatalf("next() = %v, %+v, want a step of the scheduler", step, b)
	}
	r.step(ctx)
}

func (r *clockRig) noStep() {
	r.t.Helper()
	if r.reread || r.stepping {
		r.t.Errorf("a step is due (%v) or runs (%v), want none", r.reread, r.stepping)
	}
}

// drain runs the steps that are due until none is, and returns their number.
func (r *clockRig) drain() int {
	r.t.Helper()
	n := 0
	for r.reread {
		n++
		r.runStep()
	}
	return n
}

// requested describes the requests that no build has taken: the reason, and the entry of an operation. It is for the tests
// that run no goroutine.
func requested(p *Publisher) []string {
	var out []string
	for _, q := range p.wanted {
		if q.Reason == reasonOperation {
			out = append(out, q.Reason+":"+q.Result.ID)
		} else {
			out = append(out, q.Reason)
		}
	}
	return out
}

// nextNumber returns a number that no entry of s has.
func nextNumber(t *testing.T, s *store.Store) int {
	t.Helper()
	n := 0
	err := s.View(t.Context(), func(tx *store.Tx) error {
		sums, err := tx.Summaries(content.RefEntry)
		for _, sum := range sums {
			n = max(n, sum.Entry.Number)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return n + 1
}

// entryJSON returns the JSON of a page in the language that has the status given, as the store's own API takes it.
func entryJSON(t *testing.T, id, language string, number int, status content.Status, rev, at string) string {
	t.Helper()
	e := content.Entry{
		ID: id, Number: number, Type: content.TypePage, Language: language, TranslationGroup: testID(content.RefTranslationGroup, number),
		Status: status, CreatedAt: "2026-10-25T10:00:00+09:00", CreatedBy: userID,
	}
	switch status {
	case content.StatusScheduled:
		e.ScheduledRevision, e.ScheduledAt = rev, at
	case content.StatusPublished:
		e.PublishedRevision = rev
	case content.StatusDraft, content.StatusArchived:
	}
	data, err := e.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// stateJSON returns the JSON of the entry whose document is data, with the status given, the revision as the published or
// the scheduled one, and at as the scheduled time.
func stateJSON(t *testing.T, data []byte, status content.Status, rev, at string) string {
	t.Helper()
	var e content.Entry
	if err := e.UnmarshalJSON(data); err != nil {
		t.Fatal(err)
	}
	e.Status, e.PublishedRevision, e.ScheduledRevision, e.ScheduledAt = status, "", "", ""
	if status == content.StatusPublished {
		e.PublishedRevision = rev
	}
	if status == content.StatusScheduled {
		e.ScheduledRevision, e.ScheduledAt = rev, at
	}
	out, err := e.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// addStoredRevision adds the revision id to the entry with the store's own API, which validates nothing against the site.
func addStoredRevision(t *testing.T, s *store.Store, entry, id, data string) {
	t.Helper()
	err := s.Update(t.Context(), func(tx *store.Tx) error {
		_, err := tx.AddRevision(entry, id, []byte(data), userID)
		return err
	})
	if err != nil {
		t.Fatalf("AddRevision(%q, %q) = %v", entry, id, err)
	}
}

// putPublishedPage puts a page in en that is published with a revision of the slug careers, with the store's own API.
func putPublishedPage(t *testing.T, s *store.Store, id, rev string) {
	t.Helper()
	number := nextNumber(t, s)
	put(t, s, content.RefEntry, id, entryJSON(t, id, "en", number, content.StatusDraft, "", ""))
	addStoredRevision(t, s, id, rev, careersRevision)
	put(t, s, content.RefEntry, id, entryJSON(t, id, "en", number, content.StatusPublished, rev, ""))
}

// TestScheduledPublish checks that Run publishes a scheduled entry when its time comes, as the user who scheduled it, and
// that the build for it has the page.
func TestScheduledPublish(t *testing.T) {
	t.Parallel()
	r := newClockRig(t, nil)
	h := holdBuild(t, r.Publisher, 0)
	r.start()
	r.idle()
	start := r.Status().Last
	startRelease := r.current()
	r.timers.want(t)

	rev := r.saveRevision(draftEN, careersRevision).Revision
	at := r.clock.Now().Add(time.Hour)
	sched := r.schedule(draftEN, rev, at, otherUser)
	r.idle()
	if sched.Publishes || sched.Status != content.StatusScheduled {
		t.Errorf("Schedule() = %+v, want a result that asks for no build", sched)
	}
	if st := r.Status(); st.Pending || !reflect.DeepEqual(st.Last, start) || h.builds() != 1 || len(st.Schedules) != 0 {
		t.Errorf("Status() after Schedule = %+v after %d builds, want only the start build, and nothing wanted", st, h.builds())
	}
	r.timers.want(t, time.Hour)
	r.wantStatus(draftEN, content.StatusScheduled)

	r.clock.set(at)
	r.timers.fire(t)
	r.idle()
	st := r.Status()
	b := wantBuild(t, st, "operation")
	q := b.Requests[0]
	if q.Result.Kind != content.RefEntry || q.Result.ID != draftEN || q.Result.Status != content.StatusPublished ||
		!q.Result.Publishes || q.Result.Revision != rev || !q.At.Equal(at) {
		t.Errorf("the request of the scheduled publish is %+v, want the entry %s published with %s at %v", q, draftEN, rev, at)
	}
	r.wantPublished(draftEN, rev, otherUser)
	if len(r.file(careersPage)) == 0 {
		t.Errorf("%s is not published", careersPage)
	}
	if b.Result.Release == startRelease || b.Result.Release != r.current() || !b.Result.Changed || h.builds() != 2 {
		t.Errorf("the build of the scheduled publish made %+v after %d builds, want a release other than %s, published", b.Result, h.builds(), startRelease)
	}
	if st.Pending || st.Running != nil || len(st.Schedules) != 0 {
		t.Errorf("Status() = %+v, want nothing running, wanted or failed", st)
	}
	r.timers.want(t)
	r.wantReleases(b.Result.Release, startRelease, b.Result.Release)
	r.wantStoreBuilt()

	// The publish that the scheduler makes is recorded by the Editor as the system's, with the user who scheduled the entry.
	var published []audit.Record
	for _, rec := range activity(t, r.s) {
		if rec.Action == audit.EntryPublishScheduled {
			published = append(published, rec)
		}
	}
	want := audit.Record{
		Log: audit.ActivityLog, Actor: audit.System, Action: audit.EntryPublishScheduled, Changed: true,
		Target: audit.Target{Kind: string(content.RefEntry), ID: draftEN}, Revision: rev, Status: string(content.StatusPublished),
	}
	if len(published) != 1 {
		t.Fatalf("the activity log has the records %+v of the scheduled publish, want one", published)
	}
	got := published[0]
	if got.Actor != want.Actor || got.Target != want.Target || got.Revision != want.Revision || got.Status != want.Status ||
		got.Changed != want.Changed || got.Detail["scheduledBy"] != otherUser {
		t.Errorf("the record of the scheduled publish is %+v, want %+v by the system, scheduled by %s", got, want, otherUser)
	}
}

// TestScheduledAtStart checks that a Publisher that starts after the time of a schedule has come, as after a stop of the
// server, publishes the entry before its first build, which covers it, and waits for the schedules that are to come.
func TestScheduledAtStart(t *testing.T) {
	t.Parallel()
	before := newClockRig(t, nil) // the server that stops: it never runs
	t0 := before.clock.Now()
	rev := before.saveRevision(draftEN, careersRevision).Revision
	before.schedule(draftEN, rev, t0.Add(time.Hour), otherUser)
	soon, soonRev := before.newPage("press-soon")
	before.schedule(soon, soonRev, t0.Add(2*time.Hour), userID)
	next, nextRev := before.newPage("press-next")
	before.schedule(next, nextRev, t0.Add(5*time.Hour), userID)

	// The server is down at the time of the first two, and starts when 30 minutes have passed since the second.
	before.clock.set(t0.Add(150 * time.Minute))
	r := newClockRigOn(t, before.s, before.clock, nil)
	h := holdBuild(t, r.Publisher, 0)
	if st := r.Status(); !st.Pending || st.Last != nil {
		t.Fatalf("Status() before Run = %+v, want only the start build wanted", st)
	}
	r.start()
	r.idle()

	st := r.Status()
	b := wantBuild(t, st, "start", "operation", "operation")
	if got := []string{b.Requests[1].Result.ID, b.Requests[2].Result.ID}; !slices.Equal(got, []string{draftEN, soon}) {
		t.Errorf("the scheduled publishes were of %v, want %v, in the order of their times", got, []string{draftEN, soon})
	}
	if h.builds() != 1 {
		t.Errorf("%d builds have run, want the first build to cover the schedules that were overdue", h.builds())
	}
	r.wantPublished(draftEN, rev, otherUser)
	r.wantPublished(soon, soonRev, userID)
	r.wantStatus(next, content.StatusScheduled)
	if len(r.file(careersPage)) == 0 || len(r.file("public/en/press-soon/index.html")) == 0 || r.file("public/en/press-next/index.html") != nil {
		t.Errorf("the first release lacks a page that was due or has one that is not")
	}
	r.timers.want(t, 150*time.Minute)
	r.wantStoreBuilt()
}

// TestScheduledPostDate checks that a post without a date, scheduled and published when its time comes, is built with the
// scheduled time as its date: its page is at the dated path, not at one of the time when it was scheduled or when the
// scheduler called, and a revision that the editor saved since stays the latest one.
func TestScheduledPostDate(t *testing.T) {
	t.Parallel()
	r := newClockRig(t, nil)
	h := holdBuild(t, r.Publisher, 0)
	r.start()
	r.idle()
	startRelease := r.current()

	entry := must(t)(r.Editor().CreateEntry(t.Context(), r.op(), content.TypePost, "ja")).ID
	rev := r.saveRevision(entry, `{"schemaVersion":1,"title":"Coming","slug":"coming"}`).Revision
	// 18:00 UTC is 03:00 of the next day in Tokyo, where the date of the page is counted.
	at := time.Date(2026, 11, 11, 18, 0, 0, 0, time.UTC)
	r.schedule(entry, rev, at, otherUser)
	later := r.saveRevision(entry, `{"schemaVersion":1,"title":"Coming soon","slug":"coming-soon"}`).Revision
	r.idle()
	r.wantStatus(entry, content.StatusScheduled)
	if h.builds() != 1 || r.current() != startRelease {
		t.Errorf("%d builds have run and %s is published, want only the start build: a schedule asks for none", h.builds(), r.current())
	}
	r.timers.want(t, at.Sub(fixedNow))

	// The scheduler publishes it some time after the time of the schedule.
	r.clock.set(at.Add(5 * time.Minute))
	r.timers.fire(t)
	r.idle()
	state, latest := r.entryState(entry)
	if state.Status != content.StatusPublished || state.PublishedRevision == rev || state.PublishedRevision == later {
		t.Fatalf("the entry is %+v, want it published with a new revision, the copy of %s with the date", state, rev)
	}
	if latest == later || latest == state.PublishedRevision {
		t.Errorf("the latest revision is %s, want a copy of %s, the draft that was saved after the schedule", latest, later)
	}
	titleAndDate := func(id string) (title, date string) {
		t.Helper()
		var parsed content.Revision
		err := r.s.View(t.Context(), func(tx *store.Tx) error {
			stored, err := tx.Revision(id)
			if err != nil {
				return err
			}
			return parsed.UnmarshalJSON(stored.Data)
		})
		if err != nil {
			t.Fatalf("Revision(%q) error = %v", id, err)
		}
		return parsed.Title, parsed.Date
	}
	const date = "2026-11-12T03:00:00+09:00"
	if title, got := titleAndDate(state.PublishedRevision); title != "Coming" || got != date {
		t.Errorf("the published revision is %q dated %q, want Coming dated %s, the scheduled time", title, got, date)
	}
	if title, got := titleAndDate(latest); title != "Coming soon" || got != date {
		t.Errorf("the latest revision is %q dated %q, want Coming soon dated %s", title, got, date)
	}
	const page = "public/2026/11/12/coming/index.html"
	if len(r.file(page)) == 0 {
		t.Errorf("%s is not published", page)
	}
	if r.file("public/2026/11/08/coming/index.html") != nil || r.file("public/2026/11/11/coming/index.html") != nil {
		t.Errorf("the post has a page at a date that is not the scheduled one")
	}
	b := wantBuild(t, r.Status(), "operation")
	if q := b.Requests[0]; q.Result.ID != entry || q.Result.Revision != state.PublishedRevision || !q.Result.Publishes {
		t.Errorf("the request of the scheduled publish is %+v, want the entry %s published with %s", q, entry, state.PublishedRevision)
	}
	r.wantStoreBuilt()
}

// TestScheduledFailure checks that a scheduled publish that does not pass the checks of the build leaves the entry
// scheduled, shows the problems of the build in Status, is tried again after the retry and after the state of an entry
// changes, and publishes once the problem is gone.
func TestScheduledFailure(t *testing.T) {
	t.Parallel()
	r := newClockRig(t, nil)
	h := holdBuild(t, r.Publisher, 0)
	r.start()
	r.idle()
	start := r.Status().Last
	startRelease := r.current()

	rev := r.saveRevision(draftEN, careersRevision).Revision
	at := r.clock.Now().Add(time.Hour)
	r.schedule(draftEN, rev, at, otherUser)
	r.idle()
	r.timers.want(t, time.Hour)

	// Another page in en takes the slug careers: put with the store's own API, which the Publisher does not see.
	rival, rivalRev := testID(content.RefEntry, 2), testID(content.RefRevision, 2)
	putPublishedPage(t, r.s, rival, rivalRev)
	want := r.siteWithPublished(draftEN, rev, rival, rivalRev)
	if !strings.Contains(want, `/slug: slug "careers" is also used by `) {
		t.Fatalf("the problems of the build are:\n%s\nwant the one for the slug", want)
	}

	r.clock.set(at)
	r.timers.fire(t)
	r.idle()
	st := r.Status()
	if len(st.Schedules) != 1 {
		t.Fatalf("Status().Schedules = %+v, want the failure of the entry", st.Schedules)
	}
	f := st.Schedules[0]
	var problems content.Errors
	if f.Entry != draftEN || !f.At.Equal(at) || !f.Tried.Equal(at) || !errors.As(f.Err, &problems) || f.Err.Error() != want {
		t.Errorf("the failure is %+v with the error:\n%v\nwant the entry %s scheduled at %v, tried at %v, with the problems of the build:\n%s",
			f, f.Err, draftEN, at, at, want)
	}
	r.wantStatus(draftEN, content.StatusScheduled)
	if st.Pending || !reflect.DeepEqual(st.Last, start) || h.builds() != 1 || r.current() != startRelease {
		t.Errorf("Status() = %+v after %d builds, want only the start build: a publish that failed asks for none", st, h.builds())
	}
	r.timers.want(t, time.Minute)

	// The retry: the same problem, tried again at the new time.
	tried := r.clock.add(time.Minute)
	r.timers.fire(t)
	r.idle()
	if st := r.Status(); len(st.Schedules) != 1 || st.Schedules[0].Entry != draftEN || !st.Schedules[0].Tried.Equal(tried) ||
		!st.Schedules[0].At.Equal(at) || st.Schedules[0].Err.Error() != want {
		t.Errorf("Status().Schedules = %+v, want the same failure, tried at %v", st.Schedules, tried)
	}
	r.timers.want(t, time.Minute)
	r.wantStatus(draftEN, content.StatusScheduled)

	// The other page is unpublished: a change of the state of an entry, which makes the scheduler try again at once.
	un := r.unpublish(rival)
	r.idle()
	st = r.Status()
	b := wantBuild(t, st, "operation", "operation")
	if !reflect.DeepEqual(b.Requests[0].Result, un) || b.Requests[1].Result.ID != draftEN || b.Requests[1].Result.Status != content.StatusPublished {
		t.Errorf("the requests of the build are %+v, want the Unpublish and the scheduled publish", b.Requests)
	}
	if len(st.Schedules) != 0 || st.Pending || h.builds() != 2 {
		t.Errorf("Status() = %+v after %d builds, want no failure, and one build for both", st, h.builds())
	}
	r.wantPublished(draftEN, rev, otherUser)
	if len(r.file(careersPage)) == 0 {
		t.Errorf("%s is not published", careersPage)
	}
	r.timers.want(t)
	r.wantStoreBuilt()
}

// siteWithPublished returns the error of a build of the store with the entry published with the revision, and the
// rival, another page, published with the revision rivalRev: what a scheduled publish of the entry must report. The sites
// are those of a copy of the sample.
func (r *clockRig) siteWithPublished(entry, rev, rival, rivalRev string) string {
	r.t.Helper()
	s := importSample(r.t)
	putPublishedPage(r.t, s, rival, rivalRev)
	addStoredRevision(r.t, s, entry, rev, careersRevision)
	put(r.t, s, content.RefEntry, entry, stateJSON(r.t, r.doc(content.RefEntry, entry).Data, content.StatusPublished, rev, ""))
	return buildError(r.t, snapshotOf(r.t, s))
}

// TestScheduleCancelSteps checks without a goroutine that a timer which fires after the schedule was cancelled, as one can
// as it is stopped, finds nothing to publish at the time: no request, no failure, no timer.
func TestScheduleCancelSteps(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		cancel func(r *clockRig)
		want   content.Status
	}{
		{"Unpublish", func(r *clockRig) { r.unpublish(draftEN) }, content.StatusDraft},
		{"Archive", func(r *clockRig) {
			must(r.t)(r.Editor().Archive(r.t.Context(), r.op(), draftEN, r.doc(content.RefEntry, draftEN).Seq))
		}, content.StatusArchived},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := newClockRig(t, nil)
			r.runStep()
			rev := r.saveRevision(draftEN, careersRevision).Revision
			at := r.clock.Now().Add(time.Hour)
			r.schedule(draftEN, rev, at, otherUser)
			r.runStep()
			r.timers.want(t, time.Hour)
			stale := r.timers.armed()[0]

			tt.cancel(r)
			r.drain()
			r.timers.want(t)
			if !stale.stopped {
				t.Errorf("the timer of the schedule has not been stopped")
			}

			r.clock.set(at)
			stale.fire()
			r.runStep()
			r.noStep()
			r.timers.want(t)
			r.wantStatus(draftEN, tt.want)
			if got := requested(r.Publisher); !slices.Equal(got, []string{"start"}) {
				t.Errorf("the requests are %v, want only the start: nothing is published at the time", got)
			}
			if got := r.Status().Schedules; len(got) != 0 {
				t.Errorf("Status().Schedules = %+v, want none", got)
			}
		})
	}
}

// TestScheduledPaused checks that a scheduled publish while the builds are paused is saved in the store, as every operation
// is, and that its build waits for Resume.
func TestScheduledPaused(t *testing.T) {
	t.Parallel()
	r := newClockRig(t, nil)
	h := holdBuild(t, r.Publisher, 0)
	r.start()
	r.idle()
	first := r.current()
	if err := r.Rollback(t.Context(), userID, first); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	rev := r.saveRevision(draftEN, careersRevision).Revision
	at := r.clock.Now().Add(time.Hour)
	r.schedule(draftEN, rev, at, otherUser)
	r.idle()
	r.timers.want(t, time.Hour)

	r.clock.set(at)
	r.timers.fire(t)
	r.idle() // returns although a build is due
	st := r.Status()
	if !st.Paused || !st.Pending || st.Running != nil || h.builds() != 1 {
		t.Errorf("Status() = %+v after %d builds, want the builds paused with the build of the scheduled publish due", st, h.builds())
	}
	r.wantPublished(draftEN, rev, otherUser)
	if r.current() != first || r.file(careersPage) != nil {
		t.Errorf("the site changed during the pause: %s is published", r.current())
	}
	r.timers.want(t)

	r.resume()
	r.idle()
	st = r.Status()
	b := wantBuild(t, st, "operation", "resume")
	if b.Requests[0].Result.ID != draftEN || st.Paused || st.Pending || h.builds() != 2 {
		t.Errorf("Status() after Resume = %+v after %d builds, want one build for the scheduled publish and the resume", st, h.builds())
	}
	if len(r.file(careersPage)) == 0 {
		t.Errorf("%s is not published after Resume", careersPage)
	}
	r.wantStoreBuilt()
	r.wantReleases(b.Result.Release, first, b.Result.Release)
}

// TestScheduledDuringBuild checks that a step of the scheduler waits for the build that runs: a timer that fires during
// it makes a step after it, and the publish gets a build of its own.
func TestScheduledDuringBuild(t *testing.T) {
	t.Parallel()
	r := newClockRig(t, nil)
	rev := r.saveRevision(draftEN, careersRevision).Revision
	at := r.clock.Now().Add(time.Hour)
	r.schedule(draftEN, rev, at, otherUser)
	h := holdBuild(t, r.Publisher, 1)
	r.start()
	h.await(t) // the step at the start has armed the timer, and the start build has read the store and holds
	r.timers.want(t, time.Hour)

	r.clock.set(at)
	r.timers.fire(t)
	if st := r.Status(); st.Running == nil || st.Pending || len(st.Schedules) != 0 {
		t.Errorf("Status() = %+v, want the start build running and nothing wanted: the step waits for it", st)
	}
	r.wantStatus(draftEN, content.StatusScheduled)
	h.release()
	r.idle()

	st := r.Status()
	b := wantBuild(t, st, "operation")
	if first := h.lastBefore(2); first == nil || !slices.Equal(reasons(first.Requests), []string{"start"}) || first.Result.Release == b.Result.Release {
		t.Errorf("the build before the scheduled publish is %+v, want the start build, with another release than %s", first, b.Result.Release)
	}
	r.wantPublished(draftEN, rev, otherUser)
	if len(r.file(careersPage)) == 0 || h.builds() != 2 {
		t.Errorf("%s is published: %v after %d builds, want the page after two", careersPage, len(r.file(careersPage)) != 0, h.builds())
	}
	r.wantStoreBuilt()
}

// TestScheduleStep checks the decisions of a step of the scheduler without a goroutine, so that what must not happen cannot
// be missed because it is slow: which entries it publishes and in which order, what a publish asks for, and the timer.
func TestScheduleStep(t *testing.T) {
	t.Parallel()
	r := newClockRig(t, nil)
	t0 := r.clock.Now()
	a, aRev := r.newPage("press-a")
	b, bRev := r.newPage("press-b")
	c, cRev := r.newPage("press-c")
	if a >= b || b >= c {
		t.Fatalf("the IDs %s, %s and %s do not grow", a, b, c)
	}

	// The start asks for a step, which finds nothing to publish or to wait for.
	r.runStep()
	r.noStep()
	r.timers.want(t)
	if got := requested(r.Publisher); !slices.Equal(got, []string{"start"}) {
		t.Fatalf("the requests are %v, want only the start", got)
	}

	// Scheduling asks for a step and for no build. The entries are scheduled out of the order of their times and IDs.
	r.schedule(c, cRev, t0.Add(time.Hour), userID)
	r.schedule(b, bRev, t0.Add(2*time.Hour), userID)
	r.schedule(a, aRev, t0.Add(2*time.Hour), userID)
	if got := requested(r.Publisher); !slices.Equal(got, []string{"start"}) {
		t.Fatalf("the requests are %v after the schedules, want only the start", got)
	}
	r.runStep()
	r.noStep()
	r.timers.want(t, time.Hour) // for the earliest time

	// A timer that fires a second early: the step publishes nothing, and arms a timer for that second.
	r.clock.set(t0.Add(time.Hour - time.Second))
	r.timers.fire(t)
	r.runStep()
	r.timers.want(t, time.Second)
	if got := requested(r.Publisher); !slices.Equal(got, []string{"start"}) {
		t.Fatalf("the requests are %v a second before the time, want only the start", got)
	}

	// At the time itself an entry is due. Its publish asks for a build, and, being a change of the state of an entry, for
	// another step.
	r.clock.set(t0.Add(time.Hour))
	r.timers.fire(t)
	r.runStep()
	if got := requested(r.Publisher); !slices.Equal(got, []string{"start", "operation:" + c}) {
		t.Fatalf("the requests are %v at the time of %s, want its publish", got, c)
	}
	r.timers.want(t, time.Hour) // for the time of a and b
	r.runStep()
	r.noStep()
	r.timers.want(t, time.Hour)
	if got := requested(r.Publisher); len(got) != 2 {
		t.Errorf("the requests are %v, want the step that follows to publish nothing more", got)
	}

	// Both entries of one time are published, in the order of their IDs and not of the order they were scheduled in, late.
	r.clock.set(t0.Add(3 * time.Hour))
	r.timers.fire(t)
	r.runStep()
	if got := requested(r.Publisher); !slices.Equal(got, []string{"start", "operation:" + c, "operation:" + a, "operation:" + b}) {
		t.Fatalf("the requests are %v, want the publishes of %s and %s after that of %s, by their IDs", got, a, b, c)
	}
	r.timers.want(t)
	if n := r.drain(); n != 1 {
		t.Errorf("%d steps followed, want one for the changes of the state of the entries", n)
	}
	r.timers.want(t)
	if st := r.Status(); len(st.Schedules) != 0 {
		t.Errorf("Status().Schedules = %+v, want none", st.Schedules)
	}
	for _, e := range [][2]string{{a, aRev}, {b, bRev}, {c, cRev}} {
		r.wantPublished(e[0], e[1], userID)
	}
}

// TestScheduleStepClock checks that the timer is for the time to come counted from when the step ends, as the publishes
// of the step take time.
func TestScheduleStepClock(t *testing.T) {
	t.Parallel()
	r := newClockRig(t, nil)
	t0 := r.clock.Now()
	a, aRev := r.newPage("press-a")
	b, bRev := r.newPage("press-b")
	r.schedule(a, aRev, t0.Add(time.Hour), userID)
	r.schedule(b, bRev, t0.Add(3*time.Hour), userID)
	r.clock.set(t0.Add(time.Hour))
	r.beforePublish = func(string) { r.clock.add(30 * time.Second) }
	r.runStep()
	if got := requested(r.Publisher); !slices.Equal(got, []string{"start", "operation:" + a}) {
		t.Fatalf("the requests are %v, want the publish of %s", got, a)
	}
	r.timers.want(t, 2*time.Hour-30*time.Second)
}

// TestScheduleFailures checks the steps in which a publish fails: the others are published, the entry stays scheduled and
// is in Status until it is not scheduled, the wait before the next step, and that Status shares nothing with the Publisher.
func TestScheduleFailures(t *testing.T) {
	t.Parallel()
	r := newClockRig(t, func(o *Options) { o.Retry = 5 * time.Minute })
	t0 := r.clock.Now()

	// An entry whose time does not parse is first in the order of the schedules, and due at once.
	bad := testID(content.RefEntry, 3)
	number := nextNumber(t, r.s)
	put(t, r.s, content.RefEntry, bad, entryJSON(t, bad, "ja", number, content.StatusScheduled, testID(content.RefRevision, 3), "tomorrow"))
	good, goodRev := r.newPage("press-good")
	r.schedule(good, goodRev, t0.Add(time.Hour), userID)
	later, laterRev := r.newPage("press-later")
	r.schedule(later, laterRev, t0.Add(3*time.Hour), userID)

	r.runStep()
	r.noStep()
	st := r.Status()
	if len(st.Schedules) != 1 {
		t.Fatalf("Status().Schedules = %+v, want the failure of %s", st.Schedules, bad)
	}
	f := st.Schedules[0]
	var problems content.Errors
	if f.Entry != bad || !f.At.IsZero() || !f.Tried.Equal(t0) || !errors.As(f.Err, &problems) ||
		!strings.Contains(f.Err.Error(), `/scheduledAt: invalid date and time "tomorrow"`) {
		t.Errorf("the failure is %+v with the error:\n%v\nwant %s, tried at %v, with a time of no schedule", f, f.Err, bad, t0)
	}
	r.timers.want(t, 5*time.Minute) // the retry comes before the time of good

	// Status shares nothing with the Publisher.
	text := st.text()
	st.Schedules[0].Entry = "changed"
	st.Schedules = append(st.Schedules, ScheduleFailure{Entry: "added"})
	if got := r.Status().text(); got != text {
		t.Errorf("Status() after the copy was changed is\n%swant\n%s", got, text)
	}

	// The retry fires, and the failure is tried again at the new time.
	tried := r.clock.add(5 * time.Minute)
	r.timers.fire(t)
	r.runStep()
	if got := r.Status().Schedules; len(got) != 1 || got[0].Entry != bad || !got[0].Tried.Equal(tried) {
		t.Errorf("Status().Schedules = %+v, want the same failure, tried at %v", got, tried)
	}
	r.timers.want(t, 5*time.Minute)

	// The time of good is closer than the retry: the timer is for it.
	r.clock.set(t0.Add(58 * time.Minute))
	r.timers.fire(t)
	r.runStep()
	r.timers.want(t, 2*time.Minute)

	// good is published although bad fails before it. The publish is a change of the state of an entry: bad is tried again.
	r.clock.set(t0.Add(time.Hour))
	r.timers.fire(t)
	r.runStep()
	if got := requested(r.Publisher); !slices.Equal(got, []string{"start", "operation:" + good}) {
		t.Fatalf("the requests are %v, want the publish of %s", got, good)
	}
	if !r.reread {
		t.Errorf("the publish of %s has asked for no step", good)
	}
	r.runStep()
	r.noStep()
	if got := r.Status().Schedules; len(got) != 1 || got[0].Entry != bad || !got[0].Tried.Equal(t0.Add(time.Hour)) {
		t.Errorf("Status().Schedules = %+v, want the failure of %s, tried at the time of the publish of %s", got, bad, good)
	}
	r.timers.want(t, 5*time.Minute)

	// bad is no longer scheduled: its failure is gone, and the timer is for the time of the entry that is left.
	put(t, r.s, content.RefEntry, bad, entryJSON(t, bad, "ja", number, content.StatusDraft, "", ""))
	r.rebuild()
	r.runStep()
	if got := r.Status().Schedules; len(got) != 0 {
		t.Errorf("Status().Schedules = %+v, want none: %s is not scheduled", got, bad)
	}
	r.timers.want(t, 2*time.Hour)
	r.wantPublished(good, goodRev, userID)
	r.wantStatus(later, content.StatusScheduled)
}

// TestScheduleNotDue checks that an entry which has changed since the schedules were read, and so is not due when the
// scheduler publishes it, is neither published nor a failure; its change asks for the step that finds the schedules as they
// are.
func TestScheduleNotDue(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		change func(r *clockRig, entry, rev string, at time.Time) // what another goroutine does between the read and the publish
		timer  []time.Duration                                    // the timers that are armed when the steps are done
	}{
		{"unpublished", func(r *clockRig, entry, _ string, _ time.Time) { r.unpublish(entry) }, nil},
		{"scheduled again for later", func(r *clockRig, entry, rev string, at time.Time) {
			r.schedule(entry, rev, at.Add(time.Hour), otherUser)
		}, []time.Duration{time.Hour}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := newClockRig(t, nil)
			r.runStep()
			rev := r.saveRevision(draftEN, careersRevision).Revision
			at := r.clock.Now().Add(time.Hour)
			r.schedule(draftEN, rev, at, otherUser)
			r.runStep()
			r.timers.want(t, time.Hour)

			r.clock.set(at)
			r.timers.fire(t)
			calls := 0
			r.beforePublish = func(entry string) {
				calls++
				tt.change(r, entry, rev, at)
			}
			r.runStep()
			r.beforePublish = nil
			if calls != 1 {
				t.Fatalf("the scheduler was about to publish %d entries, want one", calls)
			}
			if got := requested(r.Publisher); !slices.Equal(got, []string{"start"}) {
				t.Errorf("the requests are %v, want only the start: the entry was not published", got)
			}
			if got := r.Status().Schedules; len(got) != 0 {
				t.Errorf("Status().Schedules = %+v, want none: an entry that is not due is not a failure", got)
			}
			if !r.reread {
				t.Errorf("the change of the entry has asked for no step")
			}
			r.drain()
			r.timers.want(t, tt.timer...)
			if state, _ := r.entryState(draftEN); state.Status == content.StatusPublished {
				t.Errorf("the entry is published, want it %s or scheduled", content.StatusDraft)
			}
		})
	}
}

// TestScheduleGone checks that an entry which is deleted between the read of the schedules and its publish, after it was
// unpublished, is a failure of that step, which the step that the unpublishing asked for clears.
func TestScheduleGone(t *testing.T) {
	t.Parallel()
	r := newClockRig(t, nil)
	r.runStep()
	gone, rev := r.newPage("press-gone")
	at := r.clock.Now().Add(time.Hour)
	r.schedule(gone, rev, at, otherUser)
	r.runStep()
	r.clock.set(at)
	r.timers.fire(t)
	r.beforePublish = func(entry string) {
		r.unpublish(entry)
		must(t)(r.Editor().Delete(t.Context(), r.op(), content.RefEntry, entry, r.doc(content.RefEntry, entry).Seq))
	}
	r.runStep()
	r.beforePublish = nil
	got := r.Status().Schedules
	if len(got) != 1 || got[0].Entry != gone || !errors.Is(got[0].Err, fs.ErrNotExist) {
		t.Fatalf("Status().Schedules = %+v, want the failure of %s, which does not exist", got, gone)
	}
	r.timers.want(t, time.Minute)
	if n := r.drain(); n != 1 {
		t.Errorf("%d steps followed, want the one that the unpublishing asked for", n)
	}
	if got := r.Status().Schedules; len(got) != 0 {
		t.Errorf("Status().Schedules = %+v, want none: the entry is not scheduled", got)
	}
	r.timers.want(t)
	if got := requested(r.Publisher); !slices.Equal(got, []string{"start"}) {
		t.Errorf("the requests are %v, want only the start", got)
	}
}

// TestScheduleCanceled checks that a step which the end of its context stops, in the read of the schedules or in a publish,
// records nothing, neither a failure nor a timer, and that Run returns nil then.
func TestScheduleCanceled(t *testing.T) {
	t.Parallel()
	t.Run("read", func(t *testing.T) {
		t.Parallel()
		r := newClockRig(t, nil)
		rev := r.saveRevision(draftEN, careersRevision).Revision
		at := r.clock.Now().Add(time.Hour)
		r.schedule(draftEN, rev, at, otherUser)
		r.clock.set(at)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		r.beforePublish = func(entry string) {
			t.Errorf("the scheduler was about to publish %s, want it to have read nothing", entry)
		}
		r.stepWith(ctx)
		r.noStep()
		if got := r.Status().Schedules; len(got) != 0 || r.timers.count() != 0 {
			t.Errorf("Status().Schedules = %+v and %d timers were made, want neither", got, r.timers.count())
		}
		r.wantStatus(draftEN, content.StatusScheduled)
	})

	t.Run("publish", func(t *testing.T) {
		t.Parallel()
		r := newClockRig(t, nil)
		rev := r.saveRevision(draftEN, careersRevision).Revision
		at := r.clock.Now().Add(time.Hour)
		r.schedule(draftEN, rev, at, otherUser)
		r.clock.set(at)
		ctx, cancel := context.WithCancel(t.Context())
		r.beforePublish = func(string) { cancel() }
		r.stepWith(ctx)
		r.noStep()
		if got := r.Status().Schedules; len(got) != 0 || r.timers.count() != 0 {
			t.Errorf("Status().Schedules = %+v and %d timers were made, want neither", got, r.timers.count())
		}
		r.wantStatus(draftEN, content.StatusScheduled)
		if got := requested(r.Publisher); !slices.Equal(got, []string{"start"}) {
			t.Errorf("the requests are %v, want only the start", got)
		}
	})

	t.Run("after a failure", func(t *testing.T) {
		t.Parallel()
		r := newClockRig(t, nil)
		bad := testID(content.RefEntry, 3) // its time does not parse, so that it is first, and fails
		put(t, r.s, content.RefEntry, bad, entryJSON(t, bad, "ja", nextNumber(t, r.s), content.StatusScheduled, testID(content.RefRevision, 3), "tomorrow"))
		rev := r.saveRevision(draftEN, careersRevision).Revision
		at := r.clock.Now().Add(time.Hour)
		r.schedule(draftEN, rev, at, otherUser)
		r.clock.set(at)
		ctx, cancel := context.WithCancel(t.Context())
		var about []string
		r.beforePublish = func(entry string) {
			about = append(about, entry)
			if entry == draftEN {
				cancel()
			}
		}
		r.stepWith(ctx)
		r.noStep()
		if !slices.Equal(about, []string{bad, draftEN}) {
			t.Fatalf("the scheduler was about to publish %v, want %v in this order", about, []string{bad, draftEN})
		}
		if got := r.Status().Schedules; len(got) != 0 || r.timers.count() != 0 {
			t.Errorf("Status().Schedules = %+v and %d timers were made, want neither: the step was stopped", got, r.timers.count())
		}
		r.wantStatus(draftEN, content.StatusScheduled)
	})

	t.Run("Run", func(t *testing.T) {
		t.Parallel()
		r := newClockRig(t, nil)
		rev := r.saveRevision(draftEN, careersRevision).Revision
		at := r.clock.Now().Add(time.Hour)
		r.schedule(draftEN, rev, at, otherUser)
		r.clock.set(at)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		r.beforePublish = func(string) { cancel() }
		if err := r.Run(ctx); err != nil {
			t.Errorf("Run() error = %v, want nil", err)
		}
		st := r.Status()
		if len(st.Schedules) != 0 || st.Last != nil || st.Running != nil || !st.Pending {
			t.Errorf("Status() = %+v, want no failure and no build: only the start is wanted", st)
		}
		r.wantStatus(draftEN, content.StatusScheduled)
	})
}

// TestScheduleReadFailure checks that a store that cannot be read is a failure without an entry, which replaces those of
// the entries, is tried again at every step, and does not stop Run.
func TestScheduleReadFailure(t *testing.T) {
	t.Parallel()
	const retry = 2 * time.Minute
	r := newClockRig(t, func(o *Options) { o.Retry = retry })
	rn := r.start()
	r.idle()

	bad := testID(content.RefEntry, 3)
	put(t, r.s, content.RefEntry, bad, entryJSON(t, bad, "ja", nextNumber(t, r.s), content.StatusScheduled, testID(content.RefRevision, 3), "tomorrow"))
	r.rebuild()
	r.idle()
	if got := r.Status().Schedules; len(got) != 1 || got[0].Entry != bad {
		t.Fatalf("Status().Schedules = %+v, want the failure of %s", got, bad)
	}
	r.timers.want(t, retry)

	if err := r.s.Close(); err != nil {
		t.Fatal(err)
	}
	tried := r.clock.add(retry)
	r.timers.fire(t)
	r.idle()
	got := r.Status().Schedules
	if len(got) != 1 || got[0].Entry != "" || !got[0].At.IsZero() || !got[0].Tried.Equal(tried) || got[0].Err == nil ||
		!strings.Contains(got[0].Err.Error(), "closed") {
		t.Errorf("Status().Schedules = %+v, want only the failure to read the schedules, tried at %v", got, tried)
	}
	r.timers.want(t, retry)

	// It is tried again at the next step, here the one that Rebuild asks for, and Run goes on. Rebuild would not ask: it cannot
	// record the request in a store that is closed.
	tried = r.clock.add(time.Second)
	r.request(reasonRebuild, edit.Result{}, true)
	r.idle()
	if got := r.Status().Schedules; len(got) != 1 || got[0].Entry != "" || !got[0].Tried.Equal(tried) {
		t.Errorf("Status().Schedules = %+v, want the failure to read, tried at %v", got, tried)
	}
	select {
	case <-rn.done:
		t.Error("Run has returned")
	default:
	}
	if err := rn.stop(); err != nil {
		t.Errorf("Run() error = %v, want nil", err)
	}
}

func TestRunStopsTimer(t *testing.T) {
	t.Parallel()
	r := newClockRig(t, nil)
	rev := r.saveRevision(draftEN, careersRevision).Revision
	r.schedule(draftEN, rev, r.clock.Now().Add(time.Hour), otherUser)
	rn := r.start()
	r.idle()
	r.timers.want(t, time.Hour)
	if err := rn.stop(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	r.timers.want(t)
	r.wantStatus(draftEN, content.StatusScheduled)
}

// TestRebuildReadsSchedules checks that Rebuild makes the scheduler read the schedules, which an import or a save with the
// store's own API can have changed without the Editor telling.
func TestRebuildReadsSchedules(t *testing.T) {
	t.Parallel()
	r := newClockRig(t, nil)
	h := holdBuild(t, r.Publisher, 0)
	r.start()
	r.idle()
	startRelease := r.current()

	rev := testID(content.RefRevision, 1)
	addStoredRevision(t, r.s, draftEN, rev, careersRevision)
	at := r.clock.Now().Add(time.Hour)
	put(t, r.s, content.RefEntry, draftEN, stateJSON(t, r.doc(content.RefEntry, draftEN).Data, content.StatusScheduled, rev, at.In(jst).Format(time.RFC3339)))
	r.idle()
	r.timers.want(t) // the Publisher knows nothing of it

	r.rebuild()
	r.idle()
	r.timers.want(t, time.Hour)
	b := wantBuild(t, r.Status(), "rebuild")
	if b.Result.Release != startRelease || b.Result.Changed {
		t.Errorf("the build after Rebuild made %+v, want the start release %s: a scheduled entry is not built", b.Result, startRelease)
	}

	r.clock.set(at)
	r.timers.fire(t)
	r.idle()
	wantBuild(t, r.Status(), "operation")
	r.wantPublished(draftEN, rev, userID)
	if len(r.file(careersPage)) == 0 || h.builds() != 3 {
		t.Errorf("%s is published: %v after %d builds, want the page after three builds", careersPage, len(r.file(careersPage)) != 0, h.builds())
	}
	r.timers.want(t)
}

// TestWaitScheduler checks that Wait waits for a step of the scheduler that is due or runs, also while the builds are
// paused, which the steps do not wait for.
func TestWaitScheduler(t *testing.T) {
	t.Parallel()
	p := quietPublisher(t)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, paused := range []bool{false, true} {
		for _, wanted := range []bool{false, true} {
			for _, reread := range []bool{false, true} {
				for _, stepping := range []bool{false, true} {
					p.wanted = nil
					if wanted {
						p.wanted = []Request{{Reason: "operation"}}
					}
					p.paused, p.reread, p.stepping = paused, reread, stepping
					waits := reread || stepping || (wanted && !paused)
					if err := p.Wait(canceled); (err != nil) != waits {
						t.Errorf("Wait() error = %v with paused %v, wanted %v, reread %v and stepping %v, want it to wait: %v",
							err, paused, wanted, reread, stepping, waits)
					}
				}
			}
		}
	}
}

// TestNext checks what next decides in each state of the scheduler and the builds, without a goroutine: a step that is
// due first, nothing while a build runs, and a build only if one is due; what it starts is the state that it leaves; and
// the channel that it returns when it decides nothing is closed by the next request, step or end of a step.
func TestNext(t *testing.T) {
	t.Parallel()
	p := quietPublisher(t)
	for _, reread := range []bool{false, true} {
		for _, running := range []bool{false, true} {
			for _, wanted := range []bool{false, true} {
				for _, blocked := range []bool{false, true} {
					name := fmt.Sprintf("reread=%v running=%v wanted=%v blocked=%v", reread, running, wanted, blocked)
					p.reread, p.stepping, p.paused, p.rolling, p.running, p.wanted = reread, false, blocked, false, nil, nil
					if running {
						p.running = &Build{Started: fixedNow}
					}
					if wanted {
						p.wanted = []Request{{Reason: "operation"}}
					}
					wantStep := reread && !running
					wantBuild := !wantStep && !running && wanted && !blocked

					step, b, changed := p.next()
					if step != wantStep || (b != nil) != wantBuild {
						t.Errorf("%s: next() = %v, %+v; want a step: %v, a build: %v", name, step, b, wantStep, wantBuild)
					}
					switch {
					case step:
						if p.reread || !p.stepping || p.running != nil || len(p.wanted) != btoi(wanted) {
							t.Errorf("%s: next() left reread %v, stepping %v and %d requests wanted, want the step to run and nothing else changed",
								name, p.reread, p.stepping, len(p.wanted))
						}
					case b != nil:
						if p.running != b || len(p.wanted) != 0 || p.stepping || len(b.Requests) != 1 {
							t.Errorf("%s: next() left running %v, %d requests wanted and stepping %v, want the build to run with the request", name, p.running, len(p.wanted), p.stepping)
						}
					default:
						if changed == nil || p.stepping || p.reread != reread || len(p.wanted) != btoi(wanted) {
							t.Errorf("%s: next() changed the state or returned no channel", name)
							continue
						}
						select {
						case <-changed:
							t.Errorf("%s: the channel is closed before anything has changed", name)
						default:
						}
						p.wake()
						select {
						case <-changed:
						default:
							t.Errorf("%s: the channel is not closed by a step that is asked for", name)
						}
					}
				}
			}
		}
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// TestStepState checks the changes of the state that asking for a step and ending one make, and tells the change.
func TestStepState(t *testing.T) {
	t.Parallel()
	p := quietPublisher(t)
	closed := func(ch <-chan struct{}) bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}

	ch := p.changed
	p.wake()
	if !p.reread || p.stepping || !closed(ch) {
		t.Errorf("wake() left reread %v and stepping %v, closed %v; want a step due, and the change told", p.reread, p.stepping, closed(ch))
	}
	if step, _, _ := p.next(); !step || p.reread || !p.stepping {
		t.Errorf("next() left reread %v and stepping %v, want the step that was due to run", p.reread, p.stepping)
	}
	// What asks for a step while one runs asks for another, after it.
	p.wake()
	if !p.reread || !p.stepping {
		t.Errorf("wake() during a step left reread %v and stepping %v, want both", p.reread, p.stepping)
	}
	ch = p.changed
	p.endStep()
	if !p.reread || p.stepping || !closed(ch) {
		t.Errorf("endStep() left reread %v and stepping %v, closed %v; want the step that was asked for due, and the change told", p.reread, p.stepping, closed(ch))
	}
}

// TestNotifySteps checks which results of the Editor ask for a step: those of an entry that have a Status, and only those.
func TestNotifySteps(t *testing.T) {
	t.Parallel()
	p := quietPublisher(t)
	for _, tt := range []struct {
		name      string
		res       edit.Result
		step      bool
		publishes bool
	}{
		{"an entry published", edit.Result{Kind: content.RefEntry, ID: draftEN, Status: content.StatusPublished, Publishes: true}, true, true},
		{"an entry unpublished", edit.Result{Kind: content.RefEntry, ID: draftEN, Status: content.StatusDraft, Publishes: true}, true, true},
		{"an entry unpublished that was not published", edit.Result{Kind: content.RefEntry, ID: draftEN, Status: content.StatusDraft}, true, false},
		{"an entry archived", edit.Result{Kind: content.RefEntry, ID: draftEN, Status: content.StatusArchived}, true, false},
		{"an entry scheduled", edit.Result{Kind: content.RefEntry, ID: draftEN, Status: content.StatusScheduled}, true, false},
		{"an entry without a status", edit.Result{Kind: content.RefEntry, ID: draftEN}, false, false},
		{"a term", edit.Result{Kind: content.RefTerm, ID: newsTerm, Publishes: true}, false, true},
		{"a term with a status", edit.Result{Kind: content.RefTerm, ID: newsTerm, Status: content.StatusPublished}, false, false},
		{"the settings", edit.Result{Kind: store.SettingsKind, Publishes: true}, false, true},
	} {
		p.wanted, p.reread = nil, false
		p.notify(tt.res)
		if p.reread != tt.step || (len(p.wanted) > 0) != tt.publishes {
			t.Errorf("%s: notify() left reread %v and %d requests, want a step: %v and a build: %v", tt.name, p.reread, len(p.wanted), tt.step, tt.publishes)
		}
	}
}

// TestWakeIn checks how long the scheduler waits for its next step.
func TestWakeIn(t *testing.T) {
	t.Parallel()
	now := fixedNow
	at := func(d time.Duration) time.Time { return now.Add(d) }
	const retry = time.Minute
	for _, tt := range []struct {
		name   string
		later  time.Time
		failed bool
		want   time.Duration
		ok     bool
	}{
		{"nothing", time.Time{}, false, 0, false},
		{"a time to come", at(time.Hour), false, time.Hour, true},
		{"a time that has come since", at(-time.Second), false, 0, true},
		{"a failure", time.Time{}, true, retry, true},
		{"a failure and a later time", at(time.Hour), true, retry, true},
		{"a failure and an earlier time", at(10 * time.Second), true, 10 * time.Second, true},
		{"a failure and a time as far as the retry", at(retry), true, retry, true},
		{"a failure and a time that has come since", at(-time.Hour), true, 0, true},
	} {
		d, ok := wakeIn(now, tt.later, tt.failed, retry)
		if d != tt.want || ok != tt.ok {
			t.Errorf("%s: wakeIn() = %v, %v; want %v, %v", tt.name, d, ok, tt.want, tt.ok)
		}
	}
}

// TestRetryOption checks the option Retry: the default is a minute, and a negative one is refused.
func TestRetryOption(t *testing.T) {
	t.Parallel()
	good := Options{Store: newStore(t), Schema: schemaFile(t), Theme: exampleTheme(t), Out: t.TempDir(), CacheDir: t.TempDir()}
	for _, tt := range []struct {
		retry time.Duration
		want  time.Duration
	}{{0, time.Minute}, {time.Second, time.Second}, {time.Hour, time.Hour}} {
		o := good
		o.Retry = tt.retry
		p, err := New(o)
		if err != nil || p.retry != tt.want {
			t.Errorf("New() with the retry %v = %+v, %v; want the retry %v", tt.retry, p, err, tt.want)
		}
	}
	o := good
	o.Retry = -time.Second
	if p, err := New(o); err == nil || err.Error() != "retry -1s is out of range (0 or more)" || p != nil {
		t.Errorf("New() with a negative retry = %v, %v; want the error", p, err)
	}
}

// TestWakeStress asks for steps while Run decides what to do and waits, from several goroutines at once and one after the
// other, as soon as Wait returns: none of the requests is lost, so that Wait returns once Run has made the last step.
func TestWakeStress(t *testing.T) {
	t.Parallel()
	r := newClockRig(t, nil)
	r.start()
	r.idle()
	wait := func(what string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		if err := r.Wait(ctx); err != nil {
			r.mu.Lock()
			defer r.mu.Unlock()
			t.Fatalf("%s: Wait() error = %v with reread %v and stepping %v, want a step that is asked for to be made", what, err, r.reread, r.stepping)
		}
	}

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 200 {
				r.wake()
				runtime.Gosched()
			}
		})
	}
	wg.Wait()
	wait("the requests of several goroutines")

	for round := range 200 {
		r.wake()
		wait(fmt.Sprintf("round %d", round))
	}
}

// TestScheduleStress schedules and cancels entries from several goroutines while the clock moves, the timers fire and
// the builds run: every error is one that an operation can have when the entry changes under it, and when it is quiet no
// entry is left scheduled and the site is that of the store.
func TestScheduleStress(t *testing.T) {
	t.Parallel()
	r := newClockRig(t, nil)
	var publishes atomic.Int32
	r.beforePublish = func(string) { publishes.Add(1) }
	r.start()
	r.idle()

	const pages, writers, rounds = 6, 3, 5
	ids := make([]string, pages)
	revs := make([]string, pages)
	for i := range pages {
		ids[i], revs[i] = r.newPage(fmt.Sprintf("stress-%d", i))
	}
	tolerated := func(err error) bool {
		var problems content.Errors
		return err == nil || errors.Is(err, edit.ErrConflict) || errors.As(err, &problems)
	}
	// fireTimer fires the timer that is armed, if any, whatever its time: a step must find that nothing is due.
	fireTimer := func() {
		armed := r.timers.armed()
		if len(armed) > 1 {
			t.Errorf("%d timers are armed, want at most one", len(armed))
		}
		if len(armed) > 0 {
			armed[0].fire()
		}
	}

	stop := make(chan struct{})
	var readers, workers sync.WaitGroup
	readers.Go(func() {
		for {
			st := r.Status()
			if st.Last != nil && (st.Last.Err != nil || st.Last.Result == nil) {
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
	for w := range writers {
		workers.Go(func() {
			for round := range rounds {
				for i := w; i < pages; i += writers {
					at := r.clock.Now().Add(time.Duration(10+10*((round+i)%4)) * time.Minute)
					seq := r.doc(content.RefEntry, ids[i]).Seq
					if _, err := r.Editor().Schedule(t.Context(), edit.Op{By: otherUser}, ids[i], seq, revs[i], at); !tolerated(err) {
						t.Errorf("Schedule(%s) error = %v", ids[i], err)
					}
					r.clock.add(3 * time.Minute)
					fireTimer()
					if round%2 == 1 {
						seq = r.doc(content.RefEntry, ids[i]).Seq
						if _, err := r.Editor().Unpublish(t.Context(), r.op(), ids[i], seq); !tolerated(err) {
							t.Errorf("Unpublish(%s) error = %v", ids[i], err)
						}
						r.clock.add(3 * time.Minute)
						fireTimer()
					}
				}
			}
		})
	}
	workers.Wait()
	close(stop)
	readers.Wait()

	// Everything that is still scheduled is due, and the steps read the schedules once more.
	r.clock.add(24 * time.Hour)
	r.rebuild()
	r.idle()
	scheduled, err := r.Editor().Schedules(t.Context())
	if err != nil || len(scheduled) != 0 {
		t.Errorf("Schedules() = %+v, %v; want none left", scheduled, err)
	}
	st := r.Status()
	if st.Pending || st.Running != nil || len(st.Schedules) != 0 || st.Last == nil || st.Last.Err != nil {
		t.Errorf("Status() = %+v, want it quiet after a build that published", st)
	}
	if publishes.Load() == 0 {
		t.Errorf("the scheduler published nothing")
	}
	r.timers.want(t)
	r.wantStoreBuilt()
}
