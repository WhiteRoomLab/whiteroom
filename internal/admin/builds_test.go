package admin

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// readBuilds reads GET /api/builds as the sender.
func readBuilds(t *testing.T, as asPerson) buildsAnswer {
	t.Helper()
	return decodeBuilds(t, "GET /api/builds", as.get("/api/builds"), http.StatusOK)
}

// decodeBuilds reads the state of the builds that res answers with the status.
func decodeBuilds(t *testing.T, what string, res *http.Response, status int) buildsAnswer {
	t.Helper()
	var v buildsAnswer
	if err := json.Unmarshal([]byte(body(t, what, res, status)), &v); err != nil {
		t.Fatalf("%s: the answer is not the state of the builds: %v", what, err)
	}
	return v
}

// readReleases reads GET /api/releases as the sender.
func readReleases(t *testing.T, as asPerson) releasesAnswer {
	t.Helper()
	var v releasesAnswer
	if err := json.Unmarshal([]byte(body(t, "GET /api/releases", as.get("/api/releases"), http.StatusOK)), &v); err != nil {
		t.Fatalf("GET /api/releases: the answer is not the releases: %v", err)
	}
	return v
}

// hasRequest reports whether the build took the request of an operation that set the status of the entry.
func hasRequest(b *buildAnswer, entry string, status content.Status) bool {
	return slices.ContainsFunc(b.Requests, func(q buildRequestAnswer) bool {
		return q.Reason == reasonOperation && q.Kind == content.RefEntry && q.ID == entry && q.Status == status
	})
}

// idle reports whether the state says that no build runs and none is wanted.
func (b buildsAnswer) idle() bool { return b.Running == nil && !b.Pending }

// settled reads the state until it is idle.
func settled(t *testing.T, as asPerson) buildsAnswer {
	t.Helper()
	var st buildsAnswer
	waitFor(t, "the builds to be idle", func() bool {
		st = readBuilds(t, as)
		return st.idle()
	})
	return st
}

// TestBuildRoutes shows the routes of the builds and the releases, and that they take no place of heavy work and count for no
// account: they answer while another request has the place and the account has the most that it may.
func TestBuildRoutes(t *testing.T) {
	a := newAPIServer(t)
	a.waitBuilds(t)
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/api/builds"}, {http.MethodPost, "/api/builds"}, {http.MethodPost, "/api/builds/resume"},
		{http.MethodGet, "/api/releases"}, {http.MethodPost, "/api/releases/0123456789abcdef/rollback"},
	} {
		_, pattern := a.mux.Handler(httptest.NewRequest(r.method, r.path, nil))
		if pattern == "" {
			t.Errorf("%s %s: no route", r.method, r.path)
			continue
		}
		if got := a.deadlineOf(pattern); got != defaultDeadline {
			t.Errorf("%s %s (%s) has the deadline %v, want that of the server, %v", r.method, r.path, pattern, got, defaultDeadline)
		}
	}

	// The admin has four requests of heavy work, one in the place: its routes of the builds are not among them.
	adminID := a.people["admin"].account.ID
	g := newGate(t, a.testServer)
	var four [heavyPerAccount]<-chan *http.Response
	four[0] = async(func() *http.Response { return a.as("admin").get("/api/settings") })
	g.waitHeld(t)
	for i := 1; i < heavyPerAccount; i++ {
		four[i] = async(func() *http.Response { return a.as("admin").get("/api/entries?limit=1") })
	}
	waitFor(t, "four requests of the admin to be counted", func() bool { return a.inflight.count(adminID) == heavyPerAccount })
	checkProblem(t, "a fifth request of heavy work", a.as("admin").get("/api/entries?limit=1"), "too-many-requests")

	answers := async(func() []int {
		return []int{
			a.as("admin").get("/api/builds").StatusCode, a.as("viewer").get("/api/releases").StatusCode,
			a.as("admin").post("/api/builds", "").StatusCode,
		}
	})
	got := receive(t, answers, "the routes of the builds, while the place is taken")
	if !slices.Equal(got, []int{http.StatusOK, http.StatusOK, http.StatusAccepted}) {
		t.Errorf("the routes of the builds answer %v while the place and the account's requests are taken, want 200, 200 and 202", got)
	}
	g.open()
	for i, ch := range four {
		if res := receive(t, ch, "a request of the admin"); res.StatusCode != http.StatusOK {
			t.Errorf("request %d of the admin has the status %d", i+1, res.StatusCode)
		}
	}
	waitFor(t, "the requests of the admin to end", func() bool { return a.inflight.count(adminID) == 0 })
}

// TestBuildsShape shows the state of the builds of a Publisher that has made no build, which is wanted by its start, and
// then that of one that has made the build of its start.
func TestBuildsShape(t *testing.T) {
	empty := newEmptySiteServer(t)
	if got := body(t, "GET /api/builds", empty.as("admin").get("/api/builds"), http.StatusOK); got != `{"pending":true,"paused":false,"schedules":[]}` {
		t.Errorf("the state of a Publisher that has not run is %s, want a pending build, and nothing else", got)
	}

	a := newAPIServer(t)
	a.waitBuilds(t)
	releases := readReleases(t, a.as("viewer"))
	for _, l := range apiLogins {
		raw := body(t, "GET /api/builds as "+l.login, a.as(l.login).get("/api/builds"), http.StatusOK)
		for _, absent := range []string{`"running"`, `"error"`, `"errors"`, `"omitted`} {
			if strings.Contains(raw, absent) {
				t.Errorf("the state for %s has %s: %.300s", l.login, absent, raw)
			}
		}
		if !strings.Contains(raw, `"schedules":[]`) {
			t.Errorf("the state for %s has no empty list of schedules: %.300s", l.login, raw)
		}
	}
	res := a.as("viewer").get("/api/builds")
	if got := res.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("GET /api/builds has the Content-Type %q, want application/json", got)
	}
	_ = bodyOf(t, res)
	st := readBuilds(t, a.as("viewer"))
	if st.Running != nil || st.Pending || st.Paused || len(st.Schedules) != 0 || st.Last == nil {
		t.Fatalf("the state after the start is %+v, want only the last build", st)
	}
	last := st.Last
	if len(last.Requests) != 1 || last.Requests[0].Reason != "start" || last.Requests[0].Kind != "" || last.Requests[0].ID != "" ||
		!last.Requests[0].At.Equal(a.clock.now()) {
		t.Errorf("the requests of the last build are %+v, want the start", last.Requests)
	}
	if last.Release != releases.Current || last.Changed == nil || !*last.Changed || last.Pages == nil || *last.Pages < 40 ||
		last.Files == nil || *last.Files < *last.Pages || last.Failed == nil || *last.Failed || last.Errors != nil || last.Error != "" ||
		last.EndedAt == nil || last.EndedAt.Before(last.StartedAt) || last.OmittedRequests != 0 {
		t.Errorf("the last build is %+v, want the release that is published, changed, with its pages and files, and no failure", last)
	}
	// The sample site has warnings, located as the build locates them.
	if len(last.Warnings) == 0 {
		t.Fatal("the last build has no warnings, but the sample site has some")
	}
	for _, w := range last.Warnings {
		if w.File == "" || w.Detail == "" {
			t.Errorf("the warning %+v lacks its file or its text", w)
		}
	}
}

// TestBuildsOperations shows the requests that a build covers: the operations that were asked for while a build ran are taken
// together by the next build, in the order they came, with the kind and the ID that each changed.
func TestBuildsOperations(t *testing.T) {
	a := newAPIServer(t)
	a.waitBuilds(t)
	as := a.as("editor")
	a.builds.holdBuilds()
	first, _ := a.draftPage(t, "editor", "en", "First", "first")
	result(t, "publish", as.post("/api/entries/"+first+"/publish", a.publishBody(t, first)), http.StatusOK)
	a.builds.waitHeld(t)

	// Asked for while the build runs: they wait for the next build.
	term := result(t, "POST /api/terms", as.post("/api/terms", createBody(termDoc("category", "ja", "待つ", "wait"))), http.StatusCreated)
	settings := a.stored(t, store.SettingsKind, "")
	result(t, "PUT /api/settings", a.as("admin").put("/api/settings", saveBody(settings.Seq,
		strings.Replace(string(settings.Data), "Webサイトの企画と制作", "Webサイトの企画", 1))), http.StatusOK)
	result(t, "unpublish", as.post("/api/entries/"+first+"/unpublish", a.baseBody(t, first)), http.StatusOK)
	if res := a.as("admin").post("/api/builds", ""); res.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /api/builds has the status %d", res.StatusCode)
	}

	st := readBuilds(t, a.as("viewer"))
	if st.Running == nil || !st.Pending || len(st.Running.Requests) != 1 || !hasRequest(st.Running, first, content.StatusPublished) ||
		st.Running.EndedAt != nil || st.Running.Failed != nil || st.Running.Release != "" || st.Running.Pages != nil {
		t.Errorf("the state while the build is held is %+v, want it running with the publishing of the entry, and a build wanted", st)
	}
	a.builds.letGo()
	a.waitBuilds(t)
	st = readBuilds(t, a.as("viewer"))
	if !st.idle() || st.Last == nil || len(st.Last.Requests) != 4 {
		t.Fatalf("the state after the builds is %+v, want idle, and a last build with the four requests", st)
	}
	got := make([][4]string, 0, 4)
	for _, q := range st.Last.Requests {
		got = append(got, [4]string{q.Reason, string(q.Kind), q.ID, string(q.Status)})
	}
	want := [][4]string{
		{"operation", "term", term.ID, ""}, {"operation", "settings", "", ""},
		{"operation", "entry", first, "draft"}, {"rebuild", "", "", ""},
	}
	if !slices.Equal(got, want) {
		t.Errorf("the requests of the last build are %v, want %v", got, want)
	}
	// The page that was published and taken back is not in the site that the build made.
	if _, ok := a.page("en/first/"); ok {
		t.Error("the page of the entry that was taken back is in the output directory")
	}
}

// TestRebuild shows that an administrator asks for a build of the store as it is: 202, a build that nothing has changed makes
// the same release, and the request is in the activity log with the account that asked.
func TestRebuild(t *testing.T) {
	a := newAPIServer(t)
	a.waitBuilds(t)
	admin := a.people["admin"].account.ID
	before := readReleases(t, a.as("viewer"))
	n := a.buildCount(t)

	res := a.as("admin").post("/api/builds", "")
	if got := res.Header.Get("Location"); got != "" {
		t.Errorf("POST /api/builds has the Location %q, want none", got)
	}
	st := decodeBuilds(t, "POST /api/builds", res, http.StatusAccepted)
	if !st.Pending && st.Running == nil {
		t.Errorf("the answer to POST /api/builds is %+v, want a build that is wanted or runs", st)
	}
	a.waitBuilds(t)
	if got := a.buildCount(t); got != n+1 {
		t.Errorf("%d builds ended after the request, want 1", got-n)
	}
	st = readBuilds(t, a.as("admin"))
	if !st.idle() || st.Last == nil || len(st.Last.Requests) != 1 || st.Last.Requests[0].Reason != "rebuild" || st.Last.Requests[0].Kind != "" ||
		st.Last.Changed == nil || *st.Last.Changed || st.Last.Release != before.Current || st.Last.Failed == nil || *st.Last.Failed {
		t.Errorf("the state after the build is %+v, want it to have taken the request, and made the release that was published", st)
	}
	if got := readReleases(t, a.as("admin")); got.Current != before.Current || !slices.Equal(got.Names, before.Names) {
		t.Errorf("the releases are %+v, want %+v: nothing was changed", got, before)
	}
	records := a.records(t, audit.BuildRebuild)
	if len(records) != 1 || records[0].Actor != admin || records[0].Log != audit.ActivityLog {
		t.Errorf("the records of build.rebuild are %+v, want one by the administrator", records)
	}

	// No operation ID, no query.
	checkProblem(t, "a rebuild with a key", a.as("admin").post("/api/builds", "", key(1)...), "bad-header")
	checkProblem(t, "a rebuild with a query", a.as("admin").post("/api/builds?now=1", ""), "bad-query")
	checkProblem(t, "GET /api/builds with a key", a.as("viewer").send(http.MethodGet, "/api/builds", "", key(1)...), "bad-header")
	checkProblem(t, "GET /api/builds with a query", a.as("viewer").get("/api/builds?x=1"), "bad-query")
	a.waitBuilds(t)
	if got := a.buildCount(t); got != n+1 {
		t.Errorf("%d builds ended, want 1", got-n)
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestBuildFailures shows what the state says of a build that fails: the problems of the site in the form of the errors of an
// operation, and, for any other failure, a sentence that names no path of the server. The site that is published stays.
func TestBuildFailures(t *testing.T) {
	a := newAPIServer(t)
	a.waitBuilds(t)
	admin := a.people["admin"].account.ID
	published := readReleases(t, a.as("viewer"))

	// A document that the store keeps and the build refuses: it is not validated against the settings when it is saved.
	const broken = "trm_01JD0000000000000000000077"
	document := `{"schemaVersion":1,"id":"` + broken + `","taxonomy":"nosuch","language":"ja","translationGroup":"tg_01JD0000000000000000000077","name":"x","slug":"x"}`
	if err := a.db.Update(t.Context(), storeWrite(func(tx *store.Tx) error {
		_, err := tx.Put(content.RefTerm, broken, []byte(document), admin)
		return err
	})); err != nil {
		t.Fatal(err)
	}
	a.as("admin").post("/api/builds", "")
	a.waitBuilds(t)
	st := readBuilds(t, a.as("viewer"))
	last := st.Last
	if last == nil || last.Failed == nil || !*last.Failed || last.Error != "" || len(last.Errors) != 1 || last.OmittedErrors != 0 {
		t.Fatalf("the last build is %+v, want a failure with the one problem of the site", last)
	}
	line, col := locate(t, document, `"taxonomy"`)
	if e := last.Errors[0]; e.File != "terms/"+broken+".json" || e.Pointer != "/taxonomy" || e.Detail != `unknown taxonomy "nosuch"` || e.Line != line || e.Column != col {
		t.Errorf("the error is %+v, want the file of the term, /taxonomy, and the place in the document", e)
	}
	if last.Release != "" || last.Changed != nil || last.Pages != nil || last.Files != nil || len(last.Warnings) != 0 || last.EndedAt == nil {
		t.Errorf("the failed build is %+v, want no release and no numbers", last)
	}
	if got := readReleases(t, a.as("viewer")); got.Current != published.Current {
		t.Errorf("the published release is %s after a failed build, want %s", got.Current, published.Current)
	}
	if st.Pending || st.Running != nil {
		t.Errorf("the state is %+v: a failed build is not tried again", st)
	}
	// The activity log has the text of the failure in full, and says that it failed.
	builds := a.records(t, audit.Build)
	if r := builds[len(builds)-1]; r.Actor != audit.System || r.Result != "failed" || !strings.Contains(r.Detail["error"], `unknown taxonomy "nosuch"`) {
		t.Errorf("the last record of a build is %+v, want a failure of the system with the text of the problem", r)
	}

	// Without the document the build succeeds again.
	if err := a.db.Update(t.Context(), storeWrite(func(tx *store.Tx) error {
		_, err := tx.Delete(content.RefTerm, broken, admin)
		return err
	})); err != nil {
		t.Fatal(err)
	}
	a.as("admin").post("/api/builds", "")
	a.waitBuilds(t)
	if last := readBuilds(t, a.as("viewer")).Last; last == nil || last.Failed == nil || *last.Failed || len(last.Errors) != 0 || last.Release != published.Current {
		t.Errorf("the last build after the document was deleted is %+v, want one that did not fail", last)
	}

	// A failure that is none of the problems of the site: the output directory holds what the build has not made.
	stray := filepath.Join(a.out, "stray.txt")
	if err := os.WriteFile(stray, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.as("admin").post("/api/builds", "")
	a.waitBuilds(t)
	raw := body(t, "GET /api/builds", a.as("viewer").get("/api/builds"), http.StatusOK)
	var other buildsAnswer
	if err := json.Unmarshal([]byte(raw), &other); err != nil {
		t.Fatal(err)
	}
	if l := other.Last; l == nil || l.Failed == nil || !*l.Failed || l.Error != "the build failed for a reason other than the content of the site" ||
		len(l.Errors) != 0 || l.OmittedErrors != 0 {
		t.Errorf("the last build is %+v, want a failure with the fixed sentence", l)
	}
	if strings.Contains(raw, a.out) || strings.Contains(raw, "stray") || strings.Contains(raw, "not made by the build") {
		t.Errorf("the state names the output directory or the file in it: %s", raw)
	}
	builds = a.records(t, audit.Build)
	if r := builds[len(builds)-1]; r.Result != "failed" || !strings.Contains(r.Detail["error"], "stray.txt") {
		t.Errorf("the last record of a build is %+v, want the text of the failure, which names the file", r)
	}
	// Reading the releases fails in the same way; the answer says no more than that the server failed, and the log has the cause.
	checkProblem(t, "GET /api/releases of an output directory that has a stray file", a.as("viewer").get("/api/releases"), "internal")
	if logged := a.logs.String(); !strings.Contains(logged, "stray.txt") {
		t.Errorf("the log is %q, want the error that names the file", logged)
	}
	a.logs.Reset()

	if err := os.Remove(stray); err != nil {
		t.Fatal(err)
	}
	a.as("admin").post("/api/builds", "")
	a.waitBuilds(t)
	if last := readBuilds(t, a.as("viewer")).Last; last == nil || last.Failed == nil || *last.Failed || last.Error != "" {
		t.Errorf("the last build after the file was removed is %+v, want one that did not fail", last)
	}
}

// TestBuildsRunning shows the state while a build runs, and that the requests that come meanwhile wait for the next build.
func TestBuildsRunning(t *testing.T) {
	a := newAPIServer(t)
	a.waitBuilds(t)
	as := a.as("editor")
	releases := readReleases(t, a.as("viewer"))
	a.builds.holdBuilds()
	first, _ := a.draftPage(t, "editor", "en", "Running", "running")
	result(t, "publish", as.post("/api/entries/"+first+"/publish", a.publishBody(t, first)), http.StatusOK)
	a.builds.waitHeld(t)

	st := readBuilds(t, a.as("viewer"))
	if st.Running == nil || st.Pending || st.Paused || st.Last == nil || st.Last.Requests[0].Reason != "start" {
		t.Fatalf("the state while a build is held is %+v, want it running, nothing wanted, and the build of the start as the last", st)
	}
	if r := st.Running; len(r.Requests) != 1 || !hasRequest(r, first, content.StatusPublished) || !r.StartedAt.Equal(a.clock.now()) {
		t.Errorf("the running build is %+v, want it to have taken the publishing", r)
	}
	// The releases are read while a build holds the output directory.
	if got := readReleases(t, a.as("viewer")); got.Current != releases.Current {
		t.Errorf("the releases while a build runs are %+v, want %+v", got, releases)
	}

	second, _ := a.draftPage(t, "editor", "en", "Waiting", "waiting")
	result(t, "publish", as.post("/api/entries/"+second+"/publish", a.publishBody(t, second)), http.StatusOK)
	st = readBuilds(t, a.as("viewer"))
	if st.Running == nil || !st.Pending || len(st.Running.Requests) != 1 {
		t.Errorf("the state with a second publishing is %+v, want the same build running, and a build wanted", st)
	}
	a.builds.letGo()
	settled(t, a.as("viewer"))
	a.waitBuilds(t)
	st = readBuilds(t, a.as("viewer"))
	if st.Last == nil || len(st.Last.Requests) != 1 || !hasRequest(st.Last, second, content.StatusPublished) || st.Last.Failed == nil || *st.Last.Failed {
		t.Errorf("the last build is %+v, want the one that took the second publishing", st.Last)
	}
	for _, path := range []string{"en/running/", "en/waiting/"} {
		if _, ok := a.page(path); !ok {
			t.Errorf("the page %s is not in the output directory", path)
		}
	}
}

// TestScheduleFailures shows the scheduled publishes that fail: they are in the state with the problems that stopped them, and
// the entries stay scheduled; taking an entry back from its schedule takes its failure away.
func TestScheduleFailures(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("editor")
	later, laterRev := a.draftPage(t, "editor", "en", "Same name", "same-name")
	result(t, "schedule", as.post("/api/entries/"+later+"/schedule", a.scheduleBody(t, later, 30*time.Minute)), http.StatusOK)
	scheduled := a.entryDoc(t, later).ScheduledAt

	// Another page takes the slug in the meantime: the publish of the scheduled one is then refused by the checks across the site.
	other, _ := a.draftPage(t, "editor", "en", "Same name", "same-name")
	result(t, "publish of the other", as.post("/api/entries/"+other+"/publish", a.publishBody(t, other)), http.StatusOK)
	a.waitBuilds(t)
	if got := readBuilds(t, a.as("viewer")).Schedules; len(got) != 0 {
		t.Fatalf("the failures of the schedules are %+v before the time has come, want none", got)
	}

	a.clock.advance(31 * time.Minute)
	tried := a.clock.now()
	a.as("admin").post("/api/builds", "")
	a.waitBuilds(t)
	st := readBuilds(t, a.as("viewer"))
	if len(st.Schedules) != 1 {
		t.Fatalf("the failures of the schedules are %+v, want one", st.Schedules)
	}
	f := st.Schedules[0]
	wantAt, err := time.Parse(time.RFC3339, scheduled)
	if err != nil {
		t.Fatal(err)
	}
	if f.Entry != later || !f.At.Equal(wantAt) || !f.Tried.Equal(tried) || f.Error != "" || len(f.Errors) == 0 || f.OmittedErrors != 0 {
		t.Errorf("the failure of the schedule is %+v, want the entry, its time, the time of the step, and the problems", f)
	}
	for _, e := range f.Errors {
		if e.Detail == "" || e.Pointer == "" && e.File == "" {
			t.Errorf("the error %+v of the schedule lacks its place or its text", e)
		}
	}
	if got := a.entryDoc(t, later); got.Status != content.StatusScheduled || got.ScheduledRevision != laterRev {
		t.Errorf("the entry is %+v, want it to stay scheduled", got)
	}
	if _, ok := a.page("en/same-name/"); !ok {
		t.Error("the page of the entry that was published is not in the output directory")
	}

	// Taking the entry back from the schedule takes the failure away at the next step.
	result(t, "unpublish", as.post("/api/entries/"+later+"/unpublish", a.baseBody(t, later)), http.StatusOK)
	a.waitBuilds(t)
	if got := readBuilds(t, a.as("viewer")).Schedules; len(got) != 0 {
		t.Errorf("the failures of the schedules are %+v after the entry was taken back, want none", got)
	}
}
