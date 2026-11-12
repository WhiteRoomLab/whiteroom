package admin

import (
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// TestReleases shows the list of the releases: those that the output directory holds, in the order of their names, and the one
// that is published; and that there are none before a build has made the directory.
func TestReleases(t *testing.T) {
	a := newAPIServer(t)
	a.waitBuilds(t)
	start := readReleases(t, a.as("viewer"))
	if len(start.Names) != 1 || start.Current != start.Names[0] || !isReleaseName(start.Current) {
		t.Fatalf("the releases after the start are %+v, want the one that the build made, published", start)
	}
	for _, l := range apiLogins {
		if got := readReleases(t, a.as(l.login)); got.Current != start.Current || !slices.Equal(got.Names, start.Names) {
			t.Errorf("the releases for %s are %+v, want %+v", l.login, got, start)
		}
	}

	e, _ := a.draftPage(t, "editor", "en", "Release", "release")
	result(t, "publish", a.as("editor").post("/api/entries/"+e+"/publish", a.publishBody(t, e)), http.StatusOK)
	a.waitBuilds(t)
	two := readReleases(t, a.as("viewer"))
	if len(two.Names) != 2 {
		t.Fatalf("the releases after the publishing are %+v, want two", two)
	}
	if !sort.StringsAreSorted(two.Names) || two.Current == start.Current || !slices.Contains(two.Names, start.Current) || !slices.Contains(two.Names, two.Current) {
		t.Errorf("the releases after the publishing are %+v, want the new one published, and the one before it, in the order of their names", two)
	}
	want := `{"current":"` + two.Current + `","names":["` + two.Names[0] + `","` + two.Names[1] + `"]}`
	if got := body(t, "GET /api/releases", a.as("viewer").get("/api/releases"), http.StatusOK); got != want {
		t.Errorf("GET /api/releases answers %s, want %s", got, want)
	}

	// No key, no query.
	checkProblem(t, "GET /api/releases with a key", a.as("viewer").send(http.MethodGet, "/api/releases", "", key(1)...), "bad-header")
	checkProblem(t, "GET /api/releases with a query", a.as("viewer").get("/api/releases?x=1"), "bad-query")

	// An output directory that holds no release, or is not there yet.
	empty := newEmptySiteServer(t)
	const none = `{"current":"","names":[]}`
	if got := body(t, "GET /api/releases of an empty directory", empty.as("admin").get("/api/releases"), http.StatusOK); got != none {
		t.Errorf("GET /api/releases of an empty output directory answers %s, want %s", got, none)
	}
	if err := os.Remove(empty.out); err != nil {
		t.Fatal(err)
	}
	if got := body(t, "GET /api/releases of a directory that is not there", empty.as("admin").get("/api/releases"), http.StatusOK); got != none {
		t.Errorf("GET /api/releases of a missing output directory answers %s, want %s", got, none)
	}
}

// TestRollbackAndResume shows the rollback to a release and the resuming: the published release is the one asked for, the builds
// are paused, the operations that come meanwhile wait, and the resuming makes one build of the store as it is.
func TestRollbackAndResume(t *testing.T) {
	a := newAPIServer(t)
	admin := a.people["admin"].account.ID
	a.waitBuilds(t)
	before := readReleases(t, a.as("viewer"))
	e, _ := a.draftPage(t, "editor", "en", "Back", "back")
	result(t, "publish", a.as("editor").post("/api/entries/"+e+"/publish", a.publishBody(t, e)), http.StatusOK)
	a.waitBuilds(t)
	now := readReleases(t, a.as("viewer"))
	old := before.Current
	if now.Current == old || len(now.Names) != 2 {
		t.Fatalf("the releases are %+v, want two", now)
	}

	// Back to the release before: 200 with the state, in which the builds are paused.
	res := a.as("admin").post("/api/releases/"+old+"/rollback", "")
	st := decodeBuilds(t, "the rollback", res, http.StatusOK)
	if !st.Paused || st.Pending || st.Running != nil {
		t.Errorf("the state after the rollback is %+v, want the builds paused and nothing else wanted", st)
	}
	if got := readReleases(t, a.as("viewer")); got.Current != old || !slices.Equal(got.Names, now.Names) {
		t.Errorf("the releases after the rollback are %+v, want %s published and both kept", got, old)
	}
	if _, ok := a.page("en/back/"); ok {
		t.Error("the page that the release before did not have is published after the rollback")
	}
	rolled := a.records(t, audit.ReleaseRollback)
	if len(rolled) != 1 || rolled[0].Actor != admin || rolled[0].Target != (audit.Target{Kind: audit.KindRelease, ID: old}) ||
		rolled[0].Detail["to"] != old || rolled[0].Detail["from"] != now.Current || rolled[0].Result != "" {
		t.Errorf("the records of release.rollback are %+v, want one by the administrator to %s from %s", rolled, old, now.Current)
	}
	// Rolling back to the release that is published changes nothing, and the builds stay paused.
	st = decodeBuilds(t, "the rollback to the release that is published", a.as("admin").post("/api/releases/"+old+"/rollback", ""), http.StatusOK)
	if !st.Paused {
		t.Errorf("the state after the second rollback is %+v, want the builds paused", st)
	}

	// An operation is saved and asks for a build, which waits: no build is made while the builds are paused.
	n := a.buildCount(t)
	other, _ := a.draftPage(t, "editor", "en", "While paused", "while-paused")
	result(t, "publish while paused", a.as("editor").post("/api/entries/"+other+"/publish", a.publishBody(t, other)), http.StatusOK)
	a.waitBuilds(t)
	st = readBuilds(t, a.as("viewer"))
	if !st.Paused || !st.Pending || st.Running != nil {
		t.Errorf("the state while paused is %+v, want the builds paused and one wanted", st)
	}
	if got := a.buildCount(t); got != n {
		t.Errorf("%d builds ended while the builds were paused, want none", got-n)
	}
	if got := readReleases(t, a.as("viewer")).Current; got != old {
		t.Errorf("the published release is %s while paused, want %s", got, old)
	}

	// Resuming: the builds go on, with one build of the store as it is, which covers the operation and the resuming.
	res = a.as("admin").post("/api/builds/resume", "")
	if st = decodeBuilds(t, "the resuming", res, http.StatusOK); st.Paused {
		t.Errorf("the state after the resuming is %+v, want the builds not paused", st)
	}
	a.waitBuilds(t)
	if got := a.buildCount(t); got != n+1 {
		t.Errorf("%d builds ended after the resuming, want 1", got-n)
	}
	st = readBuilds(t, a.as("viewer"))
	if !st.idle() || st.Paused || st.Last == nil || len(st.Last.Requests) != 2 || !hasRequest(st.Last, other, content.StatusPublished) ||
		st.Last.Requests[1].Reason != "resume" || st.Last.Changed == nil || !*st.Last.Changed || st.Last.Failed == nil || *st.Last.Failed {
		t.Errorf("the state after the build is %+v, want a build that took the operation and the resuming and changed the release", st)
	}
	final := readReleases(t, a.as("viewer"))
	if final.Current != st.Last.Release || final.Current == old || final.Current == now.Current {
		t.Errorf("the releases are %+v, want the release of the build, which is neither of the two before", final)
	}
	for _, path := range []string{"en/back/", "en/while-paused/"} {
		if _, ok := a.page(path); !ok {
			t.Errorf("the page %s is not published after the resuming", path)
		}
	}
	resumed := a.records(t, audit.BuildResume)
	if len(resumed) != 1 || resumed[0].Actor != admin || !resumed[0].Changed {
		t.Errorf("the records of build.resume are %+v, want one by the administrator that ended a pause", resumed)
	}

	// Resuming when the builds are not paused is 200 and does nothing.
	if st = decodeBuilds(t, "a second resuming", a.as("admin").post("/api/builds/resume", ""), http.StatusOK); st.Paused || st.Pending {
		t.Errorf("the state after the second resuming is %+v, want nothing wanted and no pause", st)
	}
	a.waitBuilds(t)
	if got := a.buildCount(t); got != n+1 {
		t.Errorf("%d builds ended after a resuming that ended no pause, want none", got-n-1)
	}
	if resumed = a.records(t, audit.BuildResume); len(resumed) != 2 || resumed[1].Changed {
		t.Errorf("the records of build.resume are %+v, want a second that changed nothing", resumed)
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestRollbackNames shows that a name that is not the form of the name of a release is 404 before anything is recorded or done,
// and a name that has the form and no release is 404 after the request is recorded; the releases and the pause stay as they were.
func TestRollbackNames(t *testing.T) {
	a := newAPIServer(t)
	a.waitBuilds(t)
	before := readReleases(t, a.as("viewer"))
	records := len(a.records(t, ""))
	for _, name := range []string{
		"xyz", "release", "0123456789ABCDEF", "0123456789abcde", "0123456789abcdef0", "0123456789abcdeg", "0123456789abcde-", "%20123456789abcdef",
		strings.Repeat("a", 300), "0x23456789abcdef",
	} {
		checkProblem(t, "a rollback to "+name, a.as("admin").post("/api/releases/"+name+"/rollback", ""), "not-found")
	}
	if got := len(a.records(t, "")); got != records {
		t.Errorf("the rollbacks to names that are not releases made %d records, want none", got-records)
	}

	const missing = "0123456789abcdef"
	checkProblem(t, "a rollback to a release that does not exist", a.as("admin").post("/api/releases/"+missing+"/rollback", ""), "not-found")
	rolled := a.records(t, audit.ReleaseRollback)
	if len(rolled) != 2 || rolled[0].Detail["to"] != missing || rolled[0].Result != "" || rolled[1].Result != "failed" || rolled[1].Actor != a.people["admin"].account.ID {
		t.Errorf("the records of release.rollback are %+v, want the request, and one that says it failed", rolled)
	}
	if st := readBuilds(t, a.as("admin")); st.Paused {
		t.Errorf("the state is %+v after a rollback that failed, want the builds not paused", st)
	}
	if got := readReleases(t, a.as("viewer")); got.Current != before.Current || !slices.Equal(got.Names, before.Names) {
		t.Errorf("the releases are %+v after rollbacks that failed, want %+v", got, before)
	}

	// No key, no query, no GET.
	checkProblem(t, "a rollback with a key", a.as("admin").post("/api/releases/"+before.Current+"/rollback", "", key(1)...), "bad-header")
	checkProblem(t, "a rollback with a query", a.as("admin").post("/api/releases/"+before.Current+"/rollback?x=1", ""), "bad-query")
	checkProblem(t, "GET of a rollback", a.as("admin").get("/api/releases/"+before.Current+"/rollback"), "method-not-allowed")

	// An output directory that is not there has no release either.
	empty := newEmptySiteServer(t)
	checkProblem(t, "a rollback in an empty output directory", empty.as("admin").post("/api/releases/"+missing+"/rollback", ""), "not-found")
	if err := os.Remove(empty.out); err != nil {
		t.Fatal(err)
	}
	checkProblem(t, "a rollback in an output directory that is not there", empty.as("admin").post("/api/releases/"+missing+"/rollback", ""), "not-found")
}

// TestRollbackRunning shows that a rollback waits for the build that runs, without taking the place of heavy work, that another
// rollback meanwhile is refused, and that the pause begins with the wait.
func TestRollbackRunning(t *testing.T) {
	a := newAPIServer(t)
	a.waitBuilds(t)
	old := readReleases(t, a.as("viewer")).Current
	e, _ := a.draftPage(t, "editor", "en", "Waiting", "waiting-for-the-build")
	result(t, "publish", a.as("editor").post("/api/entries/"+e+"/publish", a.publishBody(t, e)), http.StatusOK)
	a.waitBuilds(t)
	current := readReleases(t, a.as("viewer")).Current

	a.builds.holdBuilds()
	if res := a.as("admin").post("/api/builds", ""); res.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /api/builds has the status %d", res.StatusCode)
	}
	a.builds.waitHeld(t)
	first := async(func() *http.Response { return a.as("admin").post("/api/releases/"+old+"/rollback", "") })
	waitFor(t, "the rollback to pause the builds", func() bool { return readBuilds(t, a.as("viewer")).Paused })
	quiet(t, first, "the rollback was answered while a build was running")

	// The place of heavy work is free, and another rollback is refused.
	read := async(func() int { return a.as("admin").get("/api/settings").StatusCode })
	if got := receive(t, read, "a read while a rollback waits"); got != http.StatusOK {
		t.Errorf("a read while a rollback waits has the status %d, want 200", got)
	}
	checkProblem(t, "a second rollback", a.as("admin").post("/api/releases/"+current+"/rollback", ""), "rollback-running")
	if got := readReleases(t, a.as("viewer")).Current; got != current {
		t.Errorf("the published release is %s while the rollback waits, want %s", got, current)
	}

	a.builds.letGo()
	st := decodeBuilds(t, "the rollback that waited", receive(t, first, "the rollback"), http.StatusOK)
	if !st.Paused || st.Running != nil {
		t.Errorf("the state after the rollback is %+v, want the builds paused", st)
	}
	if got := readReleases(t, a.as("viewer")).Current; got != old {
		t.Errorf("the published release is %s after the rollback, want %s", got, old)
	}
	// The build that was running published its release first, which is the one that was published already.
	if last := readBuilds(t, a.as("viewer")).Last; last == nil || last.Release != current || last.Changed == nil || *last.Changed {
		t.Errorf("the last build is %+v, want one that made %s again", last, current)
	}
	rolled := a.records(t, audit.ReleaseRollback)
	if len(rolled) != 3 {
		t.Fatalf("%d records of release.rollback, want the request of each rollback and one that failed", len(rolled))
	}
	var failed int
	for _, r := range rolled {
		if r.Result == "failed" {
			failed++
		}
	}
	if failed != 1 {
		t.Errorf("%d of the records of release.rollback say that it failed, want 1: %+v", failed, rolled)
	}
}
