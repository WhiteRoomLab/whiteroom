package publish

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/build"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// activity returns the records of the activity log of s, from the oldest.
func activity(t *testing.T, s *store.Store) []audit.Record {
	t.Helper()
	var out []audit.Record
	err := s.ReadAudit(t.Context(), audit.Filter{Log: audit.ActivityLog}, func(r audit.Record) bool {
		out = append(out, r)
		return true
	})
	if err != nil {
		t.Fatalf("ReadAudit() = %v", err)
	}
	return out
}

// since returns the records of the activity log of s after the first n.
func since(t *testing.T, s *store.Store, n int) []audit.Record {
	t.Helper()
	all := activity(t, s)
	if len(all) < n {
		t.Fatalf("the activity log has %d records, want at least %d", len(all), n)
	}
	return all[n:]
}

// verifyChains fails the test unless the chains of the logs of s are as the store wrote them.
func verifyChains(t *testing.T, s *store.Store) {
	t.Helper()
	if err := s.VerifyAudit(t.Context(), nil); err != nil {
		t.Errorf("VerifyAudit() = %v", err)
	}
}

func releaseTarget(name string) audit.Target { return audit.Target{Kind: audit.KindRelease, ID: name} }

// checkRecord checks that got is the record want of the activity log but for the number, the hash of the record before
// it and the time, which the store sets, and that the record could be written.
func checkRecord(t *testing.T, got, want audit.Record) {
	t.Helper()
	if got.N == 0 || got.At.IsZero() {
		t.Errorf("the record %+v has no number or no time", got)
	}
	if err := got.Check(); err != nil {
		t.Errorf("the record %+v is not one that can be written: %v", got, err)
	}
	got.N, got.Prev, got.At = 0, "", time.Time{}
	want.Log = audit.ActivityLog
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the record is\n%+v\nwant\n%+v", got, want)
	}
}

// wantRecords checks that got are the records want, in that order.
func wantRecords(t *testing.T, got []audit.Record, want ...audit.Record) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d records were added, want %d:\n%+v", len(got), len(want), got)
	}
	for i := range want {
		checkRecord(t, got[i], want[i])
	}
}

// builtRecord returns the record of a build that took the requests counted by counts and published res.
func builtRecord(res *build.Result, counts map[string]string) audit.Record {
	detail := maps.Clone(counts)
	detail["pages"] = strconv.Itoa(res.Pages)
	detail["files"] = strconv.Itoa(res.Files)
	if n := len(res.Warnings); n > 0 {
		detail["warnings"] = strconv.Itoa(n)
	}
	return audit.Record{
		Actor: audit.System, Action: audit.Build, Target: releaseTarget(res.Release), Changed: res.Changed, Detail: detail,
	}
}

// TestBuildRecords checks the record that each build leaves in the activity log: the system's, with the requests it
// took, the release it published and whether that is another than before, or why it failed.
func TestBuildRecords(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	n := len(activity(t, r.s)) // store.create and site.import
	r.start()
	r.idle()

	b := wantBuild(t, r.Status(), "start")
	if len(b.Result.Warnings) == 0 {
		t.Fatal("the sample site has no warnings, which the record counts")
	}
	first := r.current()
	wantRecords(t, since(t, r.s, n), audit.Record{
		Actor: audit.System, Action: audit.Build, Target: releaseTarget(first), Changed: true,
		Detail: map[string]string{
			"requestsStart": "1", "pages": strconv.Itoa(b.Result.Pages), "files": strconv.Itoa(b.Result.Files),
			"warnings": strconv.Itoa(len(b.Result.Warnings)),
		},
	})
	n++

	// An operation asks for a build, which publishes another release.
	r.renameNews("News 1")
	r.idle()
	second := r.current()
	b = wantBuild(t, r.Status(), "operation")
	if second == first || !b.Result.Changed {
		t.Fatalf("the build of the operation published %s after %s, %+v; want another release", second, first, b.Result)
	}
	records := since(t, r.s, n)
	if len(records) != 2 || records[0].Action != audit.TermSave {
		t.Fatalf("the operation added the records %+v, want term.save and the build", records)
	}
	checkRecord(t, records[1], builtRecord(b.Result, map[string]string{"requestsOperation": "1"}))
	n += 2

	// A build of the same site publishes the same release, which is not a change.
	r.rebuild()
	r.idle()
	b = wantBuild(t, r.Status(), "rebuild")
	if b.Result.Release != second || b.Result.Changed {
		t.Fatalf("the build of the rebuild = %+v, want %s unchanged", b.Result, second)
	}
	records = since(t, r.s, n)
	if len(records) != 2 {
		t.Fatalf("the rebuild added the records %+v, want its request and the build", records)
	}
	checkRecord(t, records[0], audit.Record{Actor: userID, Action: audit.BuildRebuild})
	checkRecord(t, records[1], builtRecord(b.Result, map[string]string{"requestsRebuild": "1"}))
	if records[1].Changed {
		t.Errorf("the build that published the release again is recorded as a change: %+v", records[1])
	}
	n += 2

	// A build that fails publishes nothing: the record has the problems of the site, as Status has them.
	data, _ := r.badRedirect()
	created := must(t)(r.Editor().Create(t.Context(), r.op(), content.RefRedirect, data))
	r.idle()
	st := r.Status()
	if st.Last == nil || st.Last.Err == nil || st.Last.Result != nil {
		t.Fatalf("Status() = %+v, want a build that failed before publishing", st)
	}
	problem := st.Last.Err.Error()
	if !strings.Contains(problem, "redirects/"+created.ID+".json:") {
		t.Fatalf("the error of the build is %q, want the problems of the redirect", problem)
	}
	records = since(t, r.s, n)
	if len(records) != 2 || records[0].Action != audit.RedirectCreate {
		t.Fatalf("the operation added the records %+v, want redirect.create and the build", records)
	}
	checkRecord(t, records[1], audit.Record{
		Actor: audit.System, Action: audit.Build, Result: "failed",
		Detail: map[string]string{"requestsOperation": "1", "error": problem},
	})
	n += 2

	// The next build publishes again.
	must(t)(r.Editor().Delete(t.Context(), r.op(), content.RefRedirect, created.ID, created.Version.Seq))
	r.idle()
	b = wantBuild(t, r.Status(), "operation")
	records = since(t, r.s, n)
	if len(records) != 2 {
		t.Fatalf("Delete added the records %+v, want redirect.delete and the build", records)
	}
	checkRecord(t, records[1], builtRecord(b.Result, map[string]string{"requestsOperation": "1"}))
	verifyChains(t, r.s)
}

// TestBuildRecordsCoalesced checks that a build that took several requests counts them by their reasons.
func TestBuildRecordsCoalesced(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	h := holdBuild(t, r.Publisher, 1)
	n := len(activity(t, r.s))
	r.start()
	h.await(t)
	for _, name := range []string{"News 1", "News 2", "News 3"} {
		r.renameNews(name)
	}
	r.rebuild()
	h.release()
	r.idle()

	records := since(t, r.s, n)
	if len(records) != 6 {
		t.Fatalf("%d records were added, want 3 saves, the request, and 2 builds: %+v", len(records), records)
	}
	builds := records[4:]
	last := wantBuild(t, r.Status(), "operation", "operation", "operation", "rebuild")
	if got := builds[0].Detail; got["requestsStart"] != "1" || len(got) != 4 {
		t.Errorf("the record of the start build has the detail %v", got)
	}
	checkRecord(t, builds[1], builtRecord(last.Result, map[string]string{"requestsOperation": "3", "requestsRebuild": "1"}))
	verifyChains(t, r.s)
}

// TestBuildRecordOf checks the record of the builds that are hard to make: each combination of a result and an error, the
// requests of all reasons, and an error that the record cannot hold as it is.
func TestBuildRecordOf(t *testing.T) {
	t.Parallel()
	const release = "0123456789abcdef"
	requests := func(reasons ...string) []Request {
		var out []Request
		for _, r := range reasons {
			out = append(out, Request{Reason: r})
		}
		return out
	}
	published := &build.Result{
		Release: release, Changed: true, Pages: 12, Files: 40, Warnings: []*content.Error{{Msg: "one"}, {Msg: "two"}},
	}
	unchanged := &build.Result{Release: release, Pages: 3, Files: 9}
	replacement := string(rune(0xFFFD))
	for _, tt := range []struct {
		name     string
		requests []Request
		res      *build.Result
		err      error
		want     audit.Record
	}{
		{
			"published", requests("start", "operation", "operation"), published, nil,
			audit.Record{Target: releaseTarget(release), Changed: true, Detail: map[string]string{
				"requestsStart": "1", "requestsOperation": "2", "pages": "12", "files": "40", "warnings": "2",
			}},
		},
		{
			"published without a change or a warning", requests("rebuild"), unchanged, nil,
			audit.Record{Target: releaseTarget(release), Detail: map[string]string{
				"requestsRebuild": "1", "pages": "3", "files": "9",
			}},
		},
		{
			"failed before it published", requests("start"), nil, errors.New("reading the store: the store is closed"),
			audit.Record{Result: "failed", Detail: map[string]string{
				"requestsStart": "1", "error": "reading the store: the store is closed",
			}},
		},
		{
			"failed after it published", requests("operation"), published, errors.New("removing the old releases: busy"),
			audit.Record{Result: "failed", Target: releaseTarget(release), Changed: true, Detail: map[string]string{
				"requestsOperation": "1", "pages": "12", "files": "40", "warnings": "2", "error": "removing the old releases: busy",
			}},
		},
		{
			"canceled", requests("operation"), nil, context.Canceled,
			audit.Record{Result: "failed", Detail: map[string]string{"requestsOperation": "1", "error": "context canceled"}},
		},
		{
			"every reason", requests("resume", "rebuild", "start", "rebuild", "operation", "rebuild", "resume", "other"), unchanged, nil,
			audit.Record{Target: releaseTarget(release), Detail: map[string]string{
				"requestsStart": "1", "requestsOperation": "1", "requestsRebuild": "3", "requestsResume": "2", "pages": "3",
				"files": "9",
			}},
		},
		{
			"an error longer than the limit", requests("start"), nil, errors.New(strings.Repeat("あ", 1000)),
			audit.Record{Result: "failed", Detail: map[string]string{
				"requestsStart": "1", "error": strings.Repeat("あ", 682), // 2046 bytes, the most whole characters
			}},
		},
		{
			"an error with control characters and invalid bytes", requests("start"), nil,
			errors.New("one\ntwo\tthree\x00four\xff"),
			audit.Record{Result: "failed", Detail: map[string]string{
				"requestsStart": "1", "error": "one\ntwo" + replacement + "three" + replacement + "four" + replacement,
			}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := buildRecord(tt.requests, tt.res, tt.err)
			tt.want.Log, tt.want.Actor, tt.want.Action = audit.ActivityLog, audit.System, audit.Build
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildRecord() =\n%+v\nwant\n%+v", got, tt.want)
			}
			if err := got.Check(); err != nil {
				t.Errorf("the record cannot be written: %v", err)
			}
			if _, err := audit.Encode(got); err != nil {
				t.Errorf("Encode() = %v", err)
			}
		})
	}
}

// TestRebuildRecord checks that Rebuild records the request of the user, and then asks for the build.
func TestRebuildRecord(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	n := len(activity(t, r.s))
	if got := requested(r.Publisher); !slices.Equal(got, []string{"start"}) {
		t.Fatalf("the requests are %v, want only the start", got)
	}
	r.reread = false
	if err := r.Rebuild(t.Context(), otherUser); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	wantRecords(t, since(t, r.s, n), audit.Record{Actor: otherUser, Action: audit.BuildRebuild})
	if got := requested(r.Publisher); !slices.Equal(got, []string{"start", "rebuild"}) || !r.reread {
		t.Errorf("the requests are %v and a step is due = %v, want the rebuild asked for, and a step", got, r.reread)
	}
	verifyChains(t, r.s)
}

// TestRequestsRefuseTheUser checks that Rebuild, Rollback and Resume do nothing, and record nothing, for a user that is not
// the ID of one.
func TestRequestsRefuseTheUser(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	n := len(activity(t, r.s))
	r.reread, r.paused = false, true
	wanted := slices.Clone(r.wanted)
	for _, by := range []string{
		"", "usr_", userID[:len(userID)-1], userID + "0", strings.ToLower(userID), audit.System, "ent_01JB8T7H4MA2C5E8G1J4K7N3P6",
	} {
		errs := map[string]error{
			"Rebuild":  r.Rebuild(t.Context(), by),
			"Rollback": r.Rollback(t.Context(), by, "0123456789abcdef"),
			"Resume":   r.Resume(t.Context(), by),
		}
		for name, err := range errs {
			if err == nil || !strings.Contains(err.Error(), "invalid user ID") {
				t.Errorf("%s(%q) error = %v, want the user ID refused", name, by, err)
			}
		}
	}
	if got := activity(t, r.s); len(got) != n {
		t.Errorf("the requests added the records %+v, want none", got[n:])
	}
	if !reflect.DeepEqual(r.wanted, wanted) || r.reread || !r.paused || r.rolling {
		t.Errorf("the state is wanted %+v, reread %v, paused %v, rolling %v; want it as it was", r.wanted, r.reread, r.paused, r.rolling)
	}
}

// TestRequestsWithEndedContext checks that Rebuild, Rollback and Resume return the error of a context that has ended, and
// do nothing and record nothing.
func TestRequestsWithEndedContext(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	n := len(activity(t, r.s))
	r.reread, r.paused = false, true
	wanted := slices.Clone(r.wanted)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for what, err := range map[string]error{
		"Rebuild":  r.Rebuild(canceled, userID),
		"Rollback": r.Rollback(canceled, userID, "0123456789abcdef"),
		"Resume":   r.Resume(canceled, userID),
	} {
		if !errors.Is(err, context.Canceled) {
			t.Errorf("%s() with a context that has ended: error = %v, want %v", what, err, context.Canceled)
		}
	}
	if got := activity(t, r.s); len(got) != n {
		t.Errorf("the requests added the records %+v, want none", got[n:])
	}
	if !reflect.DeepEqual(r.wanted, wanted) || r.reread || !r.paused || r.rolling {
		t.Errorf("the state is wanted %+v, reread %v, paused %v, rolling %v; want it as it was", r.wanted, r.reread, r.paused, r.rolling)
	}
}

// TestRequestsNeedTheirRecord checks that Rebuild, Rollback and Resume do nothing if their record cannot be written, and
// say why: the store is closed.
func TestRequestsNeedTheirRecord(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rn := r.start()
	r.idle()
	first := r.current()
	r.renameNews("News 1")
	r.idle()
	second := r.current()
	if err := rn.stop(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if err := r.s.Close(); err != nil {
		t.Fatal(err)
	}
	refused := func(what string, err error, action audit.Action) {
		t.Helper()
		want := "cannot record " + string(action) + ": "
		if err == nil || !strings.HasPrefix(err.Error(), want) || !strings.Contains(err.Error(), "closed") {
			t.Errorf("%s error = %v, want %q and the closed store", what, err, want)
		}
	}

	st := r.Status()
	refused("Rebuild()", r.Rebuild(t.Context(), userID), audit.BuildRebuild)
	refused("Rollback()", r.Rollback(t.Context(), userID, first), audit.ReleaseRollback)
	r.wantReleases(second, first, second)
	if got := r.Status(); !reflect.DeepEqual(got, st) {
		t.Errorf("Status() = %+v, want %+v: nothing was asked for", got, st)
	}

	r.mu.Lock()
	r.paused = true
	r.mu.Unlock()
	refused("Resume()", r.Resume(t.Context(), userID), audit.BuildResume)
	if got := r.Status(); !got.Paused || got.Pending {
		t.Errorf("Status() = %+v, want the builds still paused and none wanted", got)
	}
}

// readOnlyRig returns a rig of the sample site whose store was opened by OpenReader, in which no record can be written
// although the site can be read and built.
func readOnlyRig(t *testing.T, mod func(*Options)) *rig {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	w, err := store.Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := edit.Import(t.Context(), w, openSite(t, sampleDir), userID); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := store.OpenReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	opts := Options{
		Store: s, Schema: schemaFile(t), Theme: exampleTheme(t), Out: t.TempDir(), CacheDir: t.TempDir(), Workers: 2,
		Now: func() time.Time { return fixedNow },
	}
	if mod != nil {
		mod(&opts)
	}
	p, err := New(opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return &rig{Publisher: p, t: t, s: s, opts: opts}
}

// recordFailure is a call of Options.OnRecordError.
type recordFailure struct {
	action audit.Action
	err    error
}

// failures collects the calls of Options.OnRecordError, from any goroutine.
type failures struct {
	mu   sync.Mutex
	list []recordFailure
}

func (f *failures) add(action audit.Action, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.list = append(f.list, recordFailure{action, err})
}

func (f *failures) get() []recordFailure {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.list)
}

// TestBuildRecordFailure checks that a build whose record cannot be written is as it would be otherwise: it publishes, Status
// has it, and the function of Options.OnRecordError is told once.
func TestBuildRecordFailure(t *testing.T) {
	t.Parallel()
	var got failures
	r := readOnlyRig(t, func(o *Options) { o.OnRecordError = got.add })
	r.start()
	r.idle()

	b := wantBuild(t, r.Status(), "start")
	if !b.Result.Changed || r.current() != b.Result.Release {
		t.Errorf("the build published %+v and %q is published, want its release", b.Result, r.current())
	}
	r.wantStoreBuilt()
	list := got.get()
	if len(list) != 1 || list[0].action != audit.Build || list[0].err == nil || !strings.Contains(list[0].err.Error(), "read-only") {
		t.Fatalf("OnRecordError was called with %+v, want the build and the error of the read-only store", list)
	}

	// What the requests record is returned to the caller, not told to the function, and they do nothing.
	st := r.Status()
	for what, err := range map[string]error{
		"Rebuild":  r.Rebuild(t.Context(), userID),
		"Rollback": r.Rollback(t.Context(), userID, b.Result.Release),
	} {
		if err == nil || !strings.HasPrefix(err.Error(), "cannot record ") || !strings.Contains(err.Error(), "read-only") {
			t.Errorf("%s error = %v, want the record refused", what, err)
		}
	}
	r.idle()
	if now := r.Status(); !reflect.DeepEqual(now, st) {
		t.Errorf("Status() = %+v, want %+v: the requests did nothing", now, st)
	}
	if again := got.get(); len(again) != 1 {
		t.Errorf("OnRecordError was called %d times, want once: for the build", len(again))
	}
}

// TestRollbackRecordFailure checks that the record of a Rollback that has failed, if it cannot be written, is told to the
// function of Options.OnRecordError, and that the Rollback returns its own error.
func TestRollbackRecordFailure(t *testing.T) {
	t.Parallel()
	var got failures
	r := newRig(t)
	r.o.OnRecordError = got.add
	h := holdBuild(t, r.Publisher, 2)
	r.start()
	r.idle()
	first := r.current()
	r.renameNews("News 1")
	h.await(t)

	ctx, giveUp := context.WithCancel(t.Context())
	errc := make(chan error, 1)
	go func() { errc <- r.Rollback(ctx, userID, first) }()
	r.until("the rollback runs", func() bool { return r.rolling })
	// The request has been recorded; the failure cannot be.
	if err := r.s.Close(); err != nil {
		t.Fatal(err)
	}
	giveUp()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("Rollback() error = %v, want %v, its own", err, context.Canceled)
	}
	list := got.get()
	if len(list) != 1 || list[0].action != audit.ReleaseRollback || list[0].err == nil || !strings.Contains(list[0].err.Error(), "closed") {
		t.Errorf("OnRecordError was called with %+v, want the rollback and the error of the closed store", list)
	}
}

// TestIsReleaseName checks the names that the records take for those of releases.
func TestIsReleaseName(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]bool{
		"0123456789abcdef":  true,
		"ffffffffffffffff":  true,
		"0000000000000000":  true,
		"":                  false,
		"0123456789abcde":   false,
		"0123456789abcdef0": false,
		"0123456789ABCDEF":  false,
		"0123456789abcdeg":  false,
		"g123456789abcdef":  false,
		"0123456789:abcde":  false, // after the digits
		"0123456789`abcde":  false, // before the letters
		"/123456789abcdef":  false, // before the digits
		"0123456789abcdé":   false,
	} {
		if got := isReleaseName(name); got != want {
			t.Errorf("isReleaseName(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestBuildRecordCanceled checks that the build that the end of Run stops is recorded although its context has ended.
func TestBuildRecordCanceled(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	h := holdBuild(t, r.Publisher, 2)
	rn := r.start()
	r.idle()
	r.renameNews("News 1")
	h.await(t)
	n := len(activity(t, r.s))

	rn.cancel()
	h.release()
	<-rn.done
	if rn.err != nil {
		t.Fatalf("Run() error = %v", rn.err)
	}
	st := r.Status()
	if st.Last == nil || !errors.Is(st.Last.Err, context.Canceled) || st.Last.Result != nil {
		t.Fatalf("Status() = %+v, want a build that the end of Run stopped", st)
	}
	wantRecords(t, since(t, r.s, n), audit.Record{
		Actor: audit.System, Action: audit.Build, Result: "failed",
		Detail: map[string]string{"requestsOperation": "1", "error": "context canceled"},
	})
	verifyChains(t, r.s)
}

// TestWaitForRecord checks, with the store held so that the record of a build cannot be written, that the build is ended and
// in Status before its record is written, that Status does not wait for the store, and that Wait returns only when the record
// is written.
func TestWaitForRecord(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.start()
	r.idle()
	n := len(activity(t, r.s))

	release := holdStore(t, r.s)
	// The request asks for no Update, so that the build runs while the store is held.
	r.request(reasonRebuild, edit.Result{}, true)
	r.until("the build has ended and its record waits for the store", func() bool {
		return r.recording && r.running == nil && r.last != nil && len(r.last.Requests) == 1 && r.last.Requests[0].Reason == reasonRebuild
	})

	st := r.Status()
	if st.Running != nil || st.Last == nil || st.Last.Err != nil || st.Last.Result == nil {
		t.Errorf("Status() = %+v, want the build ended with its result while its record waits", st)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.Wait(canceled); !errors.Is(err, context.Canceled) {
		t.Errorf("Wait() error = %v while the record is not written, want %v", err, context.Canceled)
	}
	if got := activity(t, r.s); len(got) != n {
		t.Errorf("the record is written although the store is held: %+v", got[n:])
	}

	release()
	r.idle()
	b := wantBuild(t, r.Status(), "rebuild")
	wantRecords(t, since(t, r.s, n), builtRecord(b.Result, map[string]string{"requestsRebuild": "1"}))
}

// holdStore holds the writer of s, so that no Update of it begins, until the function that it returns is called, which
// waits for the Update that holds it to end; the end of the test calls it too. The store is held when holdStore returns.
func holdStore(t *testing.T, s *store.Store) (release func()) {
	t.Helper()
	held, gate, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- s.Update(t.Context(), func(*store.Tx) error {
			close(held)
			select {
			case <-gate:
			case <-t.Context().Done(): // so that a failing test does not hang
			}
			return nil
		})
	}()
	select {
	case <-held:
	case <-time.After(time.Minute):
		t.Fatal("the store is not held")
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			close(gate)
			if err := <-done; err != nil && t.Context().Err() == nil {
				t.Errorf("the Update that held the store: error = %v", err)
			}
		})
	}
	t.Cleanup(release)
	return release
}

// TestRollbackRecords checks the records of Rollback: one for a rollback that is done, and the request and then the failure
// for each that fails after the request is recorded, whatever the name of the release is.
func TestRollbackRecords(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.start()
	r.idle()
	first := r.current()
	r.renameNews("News 1")
	r.idle()
	second := r.current()
	n := len(activity(t, r.s))

	asked := func(by, to, from string) audit.Record {
		rec := audit.Record{Actor: by, Action: audit.ReleaseRollback, Detail: map[string]string{"to": to}}
		if len(to) == 16 && strings.Trim(to, "0123456789abcdef") == "" {
			rec.Target = releaseTarget(to)
		}
		if from != "" {
			rec.Detail["from"] = from
		}
		return rec
	}
	failed := func(by, to, reason string) audit.Record {
		rec := asked(by, to, "")
		rec.Result = "failed"
		rec.Detail["error"] = reason
		return rec
	}
	rollback := func(by, release string) error {
		t.Helper()
		return r.Rollback(t.Context(), by, release)
	}

	// A rollback that is done is one record, with the release that was published, and so is one to the release that is.
	if err := rollback(userID, first); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	wantRecords(t, since(t, r.s, n), asked(userID, first, second))
	n++
	if err := rollback(otherUser, first); err != nil {
		t.Fatalf("Rollback() to the release that is published: error = %v", err)
	}
	wantRecords(t, since(t, r.s, n), asked(otherUser, first, first))
	n++
	r.wantReleases(first, first, second)

	// A release that does not exist: the request, and then why it failed. The pause and the release are as they were.
	unknown := "0123456789abcdef"
	err := rollback(userID, unknown)
	if err == nil || !strings.HasSuffix(err.Error(), filepath.Join("releases", unknown)+": no such release") {
		t.Fatalf("Rollback() to an unknown release: error = %v", err)
	}
	wantRecords(t, since(t, r.s, n), asked(userID, unknown, first), failed(userID, unknown, err.Error()))
	n += 2

	// A name that is not one of a release is in the detail, cut, and is not the target.
	err = rollback(userID, "latest")
	if err == nil || err.Error() != `invalid release name "latest"` {
		t.Fatalf("Rollback() to latest: error = %v", err)
	}
	wantRecords(t, since(t, r.s, n), asked(userID, "latest", first), failed(userID, "latest", err.Error()))
	n += 2

	long := strings.Repeat("x", 100)
	err = rollback(userID, long)
	if err == nil {
		t.Fatal("Rollback() to a name of 100 bytes succeeded")
	}
	wantRecords(t, since(t, r.s, n), asked(userID, long[:64], first), failed(userID, long[:64], err.Error()))
	n += 2

	err = rollback(userID, "a\x00b\xff")
	if err == nil {
		t.Fatal("Rollback() to a name with a control character succeeded")
	}
	wanted := "a" + string(rune(0xFFFD)) + "b" + string(rune(0xFFFD))
	wantRecords(t, since(t, r.s, n), asked(userID, wanted, first), failed(userID, wanted, err.Error()))
	n += 2

	// The error of a name that is longer than the record can hold is cut.
	huge := strings.Repeat("y", 3000)
	err = rollback(userID, huge)
	if err == nil || len(err.Error()) <= audit.MaxDetailValue {
		t.Fatalf("Rollback() to a name of 3000 bytes: error = %v, want one longer than %d bytes", err, audit.MaxDetailValue)
	}
	wantRecords(t, since(t, r.s, n), asked(userID, huge[:64], first), failed(userID, huge[:64], err.Error()[:audit.MaxDetailValue]))

	if st := r.Status(); !st.Paused {
		t.Errorf("Status() = %+v, want the builds still paused by the rollback that was done", st)
	}
	r.wantReleases(first, first, second)
	verifyChains(t, r.s)
}

// TestRollbackRecordsWaiting checks that the request of a Rollback is recorded before it waits for the build that runs,
// that a Rollback which another refuses is recorded as a request and a failure, and that a Rollback that ctx stops is
// recorded as failed although ctx has ended.
func TestRollbackRecordsWaiting(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	h := holdBuild(t, r.Publisher, 3)
	r.start()
	r.idle()
	first := r.current()
	r.renameNews("News 1")
	r.idle()
	second := r.current()
	r.renameNews("News 2")
	h.await(t) // the third build has read the store and holds
	n := len(activity(t, r.s))

	ctx, giveUp := context.WithCancel(t.Context())
	errc := make(chan error, 1)
	go func() { errc <- r.Rollback(ctx, userID, first) }()
	r.until("the rollback runs", func() bool { return r.rolling })
	asked := audit.Record{
		Actor: userID, Action: audit.ReleaseRollback, Target: releaseTarget(first),
		Detail: map[string]string{"to": first, "from": second},
	}
	wantRecords(t, since(t, r.s, n), asked) // although the build that it waits for has not ended

	// A second Rollback by another user is refused.
	err := r.Rollback(t.Context(), otherUser, first)
	if err == nil || err.Error() != "another rollback is running" {
		t.Fatalf("Rollback() while another one runs: error = %v", err)
	}
	askedAgain, refused := asked, asked
	askedAgain.Actor, refused.Actor = otherUser, otherUser
	askedAgain.Detail = map[string]string{"to": first, "from": second}
	refused.Result = "failed"
	refused.Detail = map[string]string{"to": first, "error": "another rollback is running"}
	wantRecords(t, since(t, r.s, n), asked, askedAgain, refused)

	// The first gives up: ctx has ended, and its failure is recorded.
	giveUp()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("Rollback() given up: error = %v, want %v", err, context.Canceled)
	}
	stopped := asked
	stopped.Result = "failed"
	stopped.Detail = map[string]string{"to": first, "error": "context canceled"}
	wantRecords(t, since(t, r.s, n), asked, askedAgain, refused, stopped)
	if st := r.Status(); st.Paused || st.Running == nil {
		t.Errorf("Status() = %+v, want the build still running and not paused", st)
	}
	h.release()
	r.idle()
	verifyChains(t, r.s)
}

// gateSource holds the build that reads its settings, which holds the lock of its output directory by then, until gate is
// closed.
type gateSource struct {
	*Source
	reached chan struct{}
	gate    chan struct{}
	once    sync.Once
}

func (g *gateSource) Settings() (settings, schema content.SiteFile, err error) {
	g.once.Do(func() { close(g.reached) })
	<-g.gate
	return g.Source.Settings()
}

// TestRollbackRecordsLocked checks the failure of a Rollback while a build of another process holds the lock of the output
// directory.
func TestRollbackRecordsLocked(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.start()
	r.idle()
	first := r.current()
	n := len(activity(t, r.s))

	src := &gateSource{Source: snapshotOf(t, r.s), reached: make(chan struct{}), gate: make(chan struct{})}
	opts := build.Options{Source: src, Out: r.opts.Out, CacheDir: t.TempDir(), Workers: 1}
	done := make(chan error, 1)
	go func() {
		_, err := build.Build(t.Context(), opts)
		done <- err
	}()
	defer func() {
		close(src.gate)
		if err := <-done; err != nil {
			t.Errorf("the build that held the lock: error = %v", err)
		}
	}()
	select {
	case <-src.reached:
	case <-time.After(time.Minute):
		t.Fatal("the build does not hold the lock")
	}

	err := r.Rollback(t.Context(), userID, first)
	want := "another build is running in " + r.opts.Out
	if err == nil || err.Error() != want {
		t.Fatalf("Rollback() while a build holds the lock: error = %v, want %q", err, want)
	}
	asked := audit.Record{
		Actor: userID, Action: audit.ReleaseRollback, Target: releaseTarget(first),
		Detail: map[string]string{"to": first, "from": first},
	}
	failed := asked
	failed.Result = "failed"
	failed.Detail = map[string]string{"to": first, "error": want}
	wantRecords(t, since(t, r.s, n), asked, failed)
	if st := r.Status(); st.Paused {
		t.Errorf("Status() = %+v, want the builds not paused", st)
	}
}

// TestRollbackRecordNoRelease checks the record of a Rollback before the first build: there is no release that is
// published, and the request says so by not naming one.
func TestRollbackRecordNoRelease(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	n := len(activity(t, r.s))
	err := r.Rollback(t.Context(), userID, "0123456789abcdef")
	if err == nil {
		t.Fatal("Rollback() before the first build succeeded")
	}
	asked := audit.Record{
		Actor: userID, Action: audit.ReleaseRollback, Target: releaseTarget("0123456789abcdef"),
		Detail: map[string]string{"to": "0123456789abcdef"},
	}
	failed := asked
	failed.Result = "failed"
	failed.Detail = map[string]string{"to": "0123456789abcdef", "error": err.Error()}
	wantRecords(t, since(t, r.s, n), asked, failed)
}

// TestResumeRecords checks the records of Resume: changed if the builds were paused, which it ends, and not if they were
// not, which it leaves as they are.
func TestResumeRecords(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	h := holdBuild(t, r.Publisher, 0)
	r.start()
	r.idle()
	first := r.current()
	r.renameNews("News 1")
	r.idle()
	if err := r.Rollback(t.Context(), userID, first); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	n := len(activity(t, r.s))

	if err := r.Resume(t.Context(), otherUser); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	r.idle()
	b := wantBuild(t, r.Status(), "resume")
	records := since(t, r.s, n)
	if len(records) != 2 {
		t.Fatalf("Resume added the records %+v, want its request and the build it asks for", records)
	}
	checkRecord(t, records[0], audit.Record{Actor: otherUser, Action: audit.BuildResume, Changed: true})
	checkRecord(t, records[1], builtRecord(b.Result, map[string]string{"requestsResume": "1"}))
	n += 2
	builds := h.builds()

	// The builds are not paused now: the request is recorded as one that changed nothing, and asks for nothing.
	if err := r.Resume(t.Context(), userID); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	r.idle()
	wantRecords(t, since(t, r.s, n), audit.Record{Actor: userID, Action: audit.BuildResume})
	if st := r.Status(); st.Pending || st.Paused || h.builds() != builds {
		t.Errorf("Status() = %+v after %d builds, want none asked for after the first %d", st, h.builds(), builds)
	}
	verifyChains(t, r.s)
}

// TestResumeTogether checks that of Resumes that come together only one finds the builds paused: its record is the only one
// that has changed, and it asks for one build.
func TestResumeTogether(t *testing.T) {
	t.Parallel()
	p := quietPublisher(t)
	p.wanted, p.paused = nil, true
	n := len(activity(t, p.o.Store))
	const callers = 8
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			<-start
			errs <- p.Resume(t.Context(), userID)
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Resume() error = %v", err)
		}
	}

	var changed, all int
	for _, rec := range since(t, p.o.Store, n) {
		if rec.Action == audit.BuildResume {
			all++
			if rec.Changed {
				changed++
			}
		}
	}
	if all != callers || changed != 1 {
		t.Errorf("%d records were added, of which %d have changed; want %d and 1", all, changed, callers)
	}
	if got := reasons(p.wanted); !slices.Equal(got, []string{"resume"}) || p.paused {
		t.Errorf("the requests are %v and paused is %v, want one resume and the pause ended", got, p.paused)
	}
}

// TestResumeOneAtATime checks, with the store held so that the first Resume cannot end, that a second one does not go on
// before it: it waits for its turn, as its record must tell whether the first has ended the pause.
func TestResumeOneAtATime(t *testing.T) {
	t.Parallel()
	p := quietPublisher(t)
	if cap(p.resumeGate) != 1 {
		t.Fatalf("the Resumes have %d turns at a time, want 1", cap(p.resumeGate))
	}
	p.wanted, p.paused = nil, true
	n := len(activity(t, p.o.Store))
	release := holdStore(t, p.o.Store)

	first := make(chan error, 1)
	go func() { first <- p.Resume(t.Context(), userID) }()
	deadline := time.After(time.Minute)
	for len(p.resumeGate) == 0 { // the first Resume has its turn, and waits for the store to record its request
		select {
		case <-deadline:
			t.Fatal("the first Resume does not take its turn")
		default:
			runtime.Gosched()
		}
	}
	// A context that has ended is seen only by a Resume that waits for its turn: one that goes on fails to record.
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := p.Resume(canceled, otherUser); !errors.Is(err, context.Canceled) || errors.Unwrap(err) != nil {
		t.Errorf("Resume() while another one records: error = %v, want the error of its context, from the wait", err)
	}

	release()
	if err := <-first; err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	wantRecords(t, since(t, p.o.Store, n), audit.Record{Actor: userID, Action: audit.BuildResume, Changed: true})
	if got := reasons(p.wanted); !slices.Equal(got, []string{"resume"}) || p.paused {
		t.Errorf("the requests are %v and paused is %v, want one resume and the pause ended", got, p.paused)
	}
}

// TestWaitRecording checks that Wait does not return while the record of a build is written.
func TestWaitRecording(t *testing.T) {
	t.Parallel()
	p := quietPublisher(t)
	p.wanted = nil
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := p.Wait(canceled); err != nil {
		t.Errorf("Wait() error = %v with nothing to wait for, want nil", err)
	}
	p.recording = true
	if err := p.Wait(canceled); !errors.Is(err, context.Canceled) {
		t.Errorf("Wait() error = %v while a record is written, want %v", err, context.Canceled)
	}
}

// TestRecordsHoldNoSecrets checks that none of what the Publisher records has anything of the accounts and the sessions
// of the store, also when a build fails: it records the problems of the site, and the errors of the files and the store.
func TestRecordsHoldNoSecrets(t *testing.T) {
	t.Parallel()
	const (
		hash    = "$argon2id$v=19$m=65536,t=1,p=4$c2VjcmV0c2FsdA$c2VjcmV0aGFzaA"
		token   = "secret-token-0123456789abcdef"
		session = "ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12"
	)
	r := newRig(t)
	err := r.s.Update(t.Context(), func(tx *store.Tx) error {
		if err := tx.PutAccount(userID, []byte(`{"login":"admin","password":"`+hash+`","token":"`+token+`"}`)); err != nil {
			return err
		}
		if err := tx.PutSession(session, []byte(`{"account":"`+userID+`","token":"`+token+`"}`)); err != nil {
			return err
		}
		return tx.Record(audit.Record{Log: audit.ActivityLog, Actor: audit.System, Action: audit.StoreWrite})
	})
	if err != nil {
		t.Fatal(err)
	}
	n := len(activity(t, r.s))

	r.start()
	r.idle()
	first := r.current()
	data, _ := r.badRedirect()
	created := must(t)(r.Editor().Create(t.Context(), r.op(), content.RefRedirect, data))
	r.idle()
	if st := r.Status(); st.Last == nil || st.Last.Err == nil {
		t.Fatalf("Status() = %+v, want a build that failed", st)
	}
	must(t)(r.Editor().Delete(t.Context(), r.op(), content.RefRedirect, created.ID, created.Version.Seq))
	r.idle()
	r.rebuild()
	if err := r.Rollback(t.Context(), userID, first); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	if err := r.Rollback(t.Context(), userID, "0123456789abcdef"); err == nil {
		t.Fatal("Rollback() to an unknown release succeeded")
	}
	r.resume()
	r.idle()

	seen := map[audit.Action]bool{}
	for _, rec := range since(t, r.s, n) {
		data, err := audit.Encode(rec)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{hash, token, session, "argon2", "admin"} {
			if strings.Contains(string(data), secret) {
				t.Errorf("the record %s has %q", data, secret)
			}
		}
		seen[rec.Action] = true
	}
	for _, action := range []audit.Action{audit.Build, audit.BuildRebuild, audit.BuildResume, audit.ReleaseRollback} {
		if !seen[action] {
			t.Errorf("no record of %s was checked", action)
		}
	}
}
