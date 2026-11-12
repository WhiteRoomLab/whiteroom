package admin

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// pageRevision is the JSON of a revision of a page, with the white space of a person's.
func pageRevision(title, slug string) string {
	return fmt.Sprintf("{\n  \"schemaVersion\": 1,\n  \"title\": %q,\n  \"slug\": %q,\n  \"body\": [%s]\n}", title, slug, paragraph)
}

// draftPage makes a page in the language through the API, as the account login, saves a revision of it, and returns the entry
// and the revision.
func (a *apiServer) draftPage(t *testing.T, login, language, title, slug string) (entry, revision string) {
	t.Helper()
	e := a.newEntry(t, login, "page", language)
	saved := a.addRevision(t, login, e.ID, pageRevision(title, slug))
	return e.ID, saved.Revision
}

// publishBody is the body that publishes the last revision of the entry, which is at the version it has now.
func (a *apiServer) publishBody(t *testing.T, entry string) string {
	t.Helper()
	return fmt.Sprintf(`{"base":%d,"revision":%q}`, a.stored(t, content.RefEntry, entry).Seq, a.lastRevision(t, entry))
}

// baseBody is the body of the operations that take an entry back from the public.
func (a *apiServer) baseBody(t *testing.T, entry string) string {
	t.Helper()
	return fmt.Sprintf(`{"base":%d}`, a.stored(t, content.RefEntry, entry).Seq)
}

// scheduleBody is the body that schedules the last revision of the entry for the time that is after from now.
func (a *apiServer) scheduleBody(t *testing.T, entry string, after time.Duration) string {
	t.Helper()
	at := a.clock.now().Add(after).UTC().Format(time.RFC3339)
	return fmt.Sprintf(`{"base":%d,"revision":%q,"at":%q}`, a.stored(t, content.RefEntry, entry).Seq, a.lastRevision(t, entry), at)
}

// page returns the HTML of the page at path, such as "en/hello/", in the release that the output directory publishes now.
func (a *apiServer) page(path string) (html string, ok bool) {
	b, err := os.ReadFile(filepath.Join(a.out, "current", "public", filepath.FromSlash(path), "index.html"))
	return string(b), err == nil
}

// buildCount returns how many builds have ended: the records of the activity log by the system.
func (a *apiServer) buildCount(t *testing.T) int {
	t.Helper()
	return len(a.records(t, audit.Build))
}

// TestPublishRoutes shows that the four routes of the publishing exist and are routes of heavy work.
func TestPublishRoutes(t *testing.T) {
	a := newAPIServer(t)
	for _, name := range []string{"publish", "unpublish", "archive", "schedule"} {
		path := "/api/entries/" + sampleEntry + "/" + name
		_, pattern := a.mux.Handler(httptest.NewRequest(http.MethodPost, path, nil))
		if pattern == "" {
			t.Errorf("POST %s: no route", path)
			continue
		}
		if got := a.deadlineOf(pattern); got != heavyDeadline {
			t.Errorf("POST %s (%s) has the deadline %v, want that of heavy work, %v", path, pattern, got, heavyDeadline)
		}
		checkProblem(t, "GET "+path, a.as("editor").get(path), "method-not-allowed")
	}
}

// TestPublish shows the publishing of a page: the answer, the state in the store, the build that makes the page, and the
// operations that change nothing.
func TestPublish(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("editor")
	editor := a.people["editor"].account.ID
	e, rev := a.draftPage(t, "editor", "en", "Hello", "hello")
	item := "/api/entries/" + e
	a.waitBuilds(t)
	if _, ok := a.page("en/hello/"); ok {
		t.Error("the page of a draft is in the output directory")
	}
	before := a.stored(t, content.RefEntry, e)

	res := as.post(item+"/publish", a.publishBody(t, e))
	if got := res.Header.Get("Location"); got != "" {
		t.Errorf("the publishing has the Location %q, want none", got)
	}
	pub := result(t, "publish", res, http.StatusOK)
	if pub.Kind != content.RefEntry || pub.ID != e || pub.Status != content.StatusPublished || pub.Revision != rev || pub.Redirect != "" ||
		!pub.Publishes || pub.Number != 0 || len(pub.Warnings) != 0 || pub.Version.Seq <= before.Seq || pub.Version.SavedBy != editor {
		t.Errorf("the answer of the publishing is %+v, want the published revision %s at a later version saved by the editor, and a publish", pub, rev)
	}
	if entry := a.entryDoc(t, e); entry.Status != content.StatusPublished || entry.PublishedRevision != rev {
		t.Errorf("the entry in the store is %+v, want it published with the revision", entry)
	}
	a.waitBuilds(t)
	if html, ok := a.page("en/hello/"); !ok || !strings.Contains(html, "<h1>Hello</h1>") {
		t.Errorf("the page after the build is %q (%v), want the page of the revision", html, ok)
	}

	// What is published again saves nothing and asks for no build; the old version is a conflict.
	n := a.buildCount(t)
	again := result(t, "publish again", as.post(item+"/publish", a.publishBody(t, e)), http.StatusOK)
	if again.Publishes || again.Version != pub.Version || again.Status != content.StatusPublished || again.Revision != rev {
		t.Errorf("the second publishing answers %+v, want the same version as the first, and no publish", again)
	}
	checkProblem(t, "the publishing at the version before", as.post(item+"/publish",
		fmt.Sprintf(`{"base":%d,"revision":%q}`, before.Seq, rev)), "conflict")
	a.waitBuilds(t)
	if got := a.buildCount(t); got != n {
		t.Errorf("%d builds ended after the publishing that changed nothing, want none", got-n)
	}
	if got := len(a.mine(t, audit.EntryPublish, e)); got != 2 {
		t.Errorf("%d records of entry.publish for the entry, want 2", got)
	}
	if r := a.mine(t, audit.EntryPublish, e)[0]; r.Actor != editor || r.Status != string(content.StatusPublished) || r.Revision != rev {
		t.Errorf("the record is %+v, want the editor, the status and the revision", r)
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestUnpublishAndArchive shows the operations that take an entry back from the public: they ask for a build only when the
// entry was published.
func TestUnpublishAndArchive(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("editor")
	editor := a.people["editor"].account.ID
	e, rev := a.draftPage(t, "editor", "en", "Gone", "gone")
	item := "/api/entries/" + e
	a.waitBuilds(t)
	step := func(what, route, body string, status content.Status, publishes bool) {
		t.Helper()
		before := a.stored(t, content.RefEntry, e)
		res := result(t, what, as.post(item+route, body), http.StatusOK)
		wantRevision := ""
		if route == "/publish" {
			wantRevision = rev
		}
		if res.Kind != content.RefEntry || res.ID != e || res.Status != status || res.Publishes != publishes || res.Revision != wantRevision {
			t.Errorf("%s answers %+v, want the status %s, publishes %v and the revision %q", what, res, status, publishes, wantRevision)
		}
		if got := a.entryDoc(t, e).Status; got != status {
			t.Errorf("after %s the entry is %s, want %s", what, got, status)
		}
		if after := a.stored(t, content.RefEntry, e); publishes && (after.Seq <= before.Seq || res.Version.SavedBy != editor) ||
			!publishes && res.Version.Seq != after.Seq {
			t.Errorf("after %s the entry is at the version %d from %d, and the answer has %+v", what, after.Seq, before.Seq, res.Version)
		}
		a.waitBuilds(t)
	}

	// A draft is a draft already, and is archived without a build.
	n := a.buildCount(t)
	step("unpublish of a draft", "/unpublish", a.baseBody(t, e), content.StatusDraft, false)
	step("archive of a draft", "/archive", a.baseBody(t, e), content.StatusArchived, false)
	if got := a.buildCount(t); got != n {
		t.Errorf("%d builds ended for entries that were not published, want none", got-n)
	}

	// An archived entry is published again, and then taken back from the public: each asks for a build.
	step("publish of an archived entry", "/publish", a.publishBody(t, e), content.StatusPublished, true)
	if html, ok := a.page("en/gone/"); !ok || !strings.Contains(html, "<h1>Gone</h1>") {
		t.Errorf("the page of the published entry is %q (%v)", html, ok)
	}
	step("unpublish of a published entry", "/unpublish", a.baseBody(t, e), content.StatusDraft, true)
	if _, ok := a.page("en/gone/"); ok {
		t.Error("the page of the entry that was taken back is still in the output directory")
	}
	step("publish of a draft", "/publish", a.publishBody(t, e), content.StatusPublished, true)
	step("archive of a published entry", "/archive", a.baseBody(t, e), content.StatusArchived, true)
	if _, ok := a.page("en/gone/"); ok {
		t.Error("the page of the archived entry is still in the output directory")
	}

	// The old version is a conflict, and the entry stays as it was.
	stale := fmt.Sprintf(`{"base":%d}`, a.stored(t, content.RefEntry, e).Seq-1)
	checkProblem(t, "unpublish at the version before", as.post(item+"/unpublish", stale), "conflict")
	checkProblem(t, "archive at the version before", as.post(item+"/archive", stale), "conflict")
	for action, want := range map[audit.Action]int{audit.EntryUnpublish: 2, audit.EntryArchive: 2, audit.EntryPublish: 2} {
		if got := len(a.mine(t, action, e)); got != want {
			t.Errorf("%d records of %s for the entry, want %d", got, action, want)
		}
	}
}

// TestPublishRevisionAndRedirect shows the members of the answer that a publishing can fill: the revision that a post
// without a date is published with, and the redirect from the old URL of an entry whose URL changes.
func TestPublishRevisionAndRedirect(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("editor")

	// A post without a date is published with a copy that has one, which becomes its last revision.
	post := a.newEntry(t, "editor", "post", "ja")
	saved := a.addRevision(t, "editor", post.ID, postRevision("日付なし", "undated", ""))
	pub := result(t, "publish of a post without a date", as.post("/api/entries/"+post.ID+"/publish", a.publishBody(t, post.ID)), http.StatusOK)
	if pub.Revision == "" || pub.Revision == saved.Revision || pub.Revision != a.lastRevision(t, post.ID) {
		t.Errorf("the publishing of a post without a date has the revision %q, want a new one, the last of the entry, not %s", pub.Revision, saved.Revision)
	}
	if got := a.storedRevision(t, pub.Revision); !strings.Contains(string(got.Data), `"date"`) {
		t.Errorf("the revision that was published is %s, want one with a date", got.Data)
	}

	// A page whose slug changes is published at the new URL, and a redirect is made from the old one.
	e, _ := a.draftPage(t, "editor", "en", "Old name", "old-name")
	item := "/api/entries/" + e
	result(t, "the first publishing", as.post(item+"/publish", a.publishBody(t, e)), http.StatusOK)
	moved := a.addRevision(t, "editor", e, pageRevision("New name", "new-name"))
	res := result(t, "the publishing at the new slug", as.post(item+"/publish", a.publishBody(t, e)), http.StatusOK)
	if err := content.CheckID(content.RefRedirect, res.Redirect); err != nil || res.Revision != moved.Revision {
		t.Fatalf("the publishing at the new slug answers %+v, want the redirect and the new revision", res)
	}
	var rd content.Redirect
	if err := rd.UnmarshalJSON(a.stored(t, content.RefRedirect, res.Redirect).Data); err != nil || rd.To != e || rd.From != "/en/old-name/" || rd.Status != 301 {
		t.Errorf("the redirect is %+v (%v), want a 301 from /en/old-name/ to the entry", rd, err)
	}
	a.waitBuilds(t)
	if _, ok := a.page("en/new-name/"); !ok {
		t.Error("the page at the new slug is not in the output directory")
	}
}

// TestSchedule shows the scheduling of an entry: it is scheduled and not published, nothing is built, and it can be scheduled
// again for another time and taken back.
func TestSchedule(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("editor")
	e, rev := a.draftPage(t, "editor", "en", "Later", "later")
	item := "/api/entries/" + e
	a.waitBuilds(t)
	n := a.buildCount(t)

	res := result(t, "schedule", as.post(item+"/schedule", a.scheduleBody(t, e, 30*time.Minute)), http.StatusOK)
	if res.Kind != content.RefEntry || res.ID != e || res.Status != content.StatusScheduled || res.Revision != rev || res.Publishes {
		t.Errorf("the answer of the scheduling is %+v, want the status scheduled and the revision, and no publish", res)
	}
	entry := a.entryDoc(t, e)
	wantAt := a.clock.now().Add(30 * time.Minute).In(time.FixedZone("JST", 9*3600)).Format(time.RFC3339)
	if entry.Status != content.StatusScheduled || entry.ScheduledRevision != rev || entry.ScheduledAt != wantAt || entry.PublishedRevision != "" {
		t.Errorf("the entry is %+v, want it scheduled with the revision for %s", entry, wantAt)
	}

	// A time with an offset and with fractions of a second is read, and scheduling what is scheduled sets the new time.
	at := a.clock.now().Add(45 * time.Minute).In(time.FixedZone("", -5*3600)).Format("2006-01-02T15:04:05.999-07:00")
	body := fmt.Sprintf(`{"base":%d,"revision":%q,"at":%q}`, res.Version.Seq, rev, at)
	again := result(t, "schedule again", as.post(item+"/schedule", body), http.StatusOK)
	wantAt = a.clock.now().Add(45 * time.Minute).In(time.FixedZone("JST", 9*3600)).Format(time.RFC3339)
	if got := a.entryDoc(t, e); again.Status != content.StatusScheduled || got.ScheduledAt != wantAt {
		t.Errorf("the second scheduling answers %+v and the entry is scheduled for %s, want %s", again, got.ScheduledAt, wantAt)
	}

	// Taking it back cancels the schedule: a scheduled entry that is not published asks for no build.
	back := result(t, "unpublish of a scheduled entry", as.post(item+"/unpublish", a.baseBody(t, e)), http.StatusOK)
	if got := a.entryDoc(t, e); back.Status != content.StatusDraft || back.Publishes || got.ScheduledAt != "" || got.ScheduledRevision != "" {
		t.Errorf("after the unpublishing the answer is %+v and the entry %+v, want a draft that is not scheduled, and no publish", back, got)
	}
	a.waitBuilds(t)
	if got := a.buildCount(t); got != n {
		t.Errorf("%d builds ended for entries that were scheduled, want none", got-n)
	}
	if _, ok := a.page("en/later/"); ok {
		t.Error("the page of a scheduled entry is in the output directory")
	}
	if got := len(a.mine(t, audit.EntrySchedule, e)); got != 2 {
		t.Errorf("%d records of entry.schedule for the entry, want 2", got)
	}
}

// TestScheduledPublish shows that an entry that is scheduled by the API is published by the Publisher when its time has come:
// after the clock has moved past it, the next request for a build (the scheduler looks at the schedules then) publishes it.
func TestScheduledPublish(t *testing.T) {
	a := newAPIServer(t)
	editor := a.people["editor"].account.ID
	e, rev := a.draftPage(t, "editor", "en", "On time", "on-time")
	result(t, "schedule", a.as("editor").post("/api/entries/"+e+"/schedule", a.scheduleBody(t, e, 30*time.Minute)), http.StatusOK)
	a.waitBuilds(t)
	if got := a.entryDoc(t, e).Status; got != content.StatusScheduled {
		t.Fatalf("the entry is %s, want scheduled", got)
	}

	// The time has not come: a build is asked for, and the entry stays scheduled.
	a.clock.advance(10 * time.Minute)
	if res := a.as("admin").post("/api/builds", ""); res.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /api/builds has the status %d", res.StatusCode)
	}
	a.waitBuilds(t)
	if got := a.entryDoc(t, e).Status; got != content.StatusScheduled {
		t.Errorf("the entry is %s before its time, want scheduled", got)
	}

	a.clock.advance(21 * time.Minute)
	if res := a.as("admin").post("/api/builds", ""); res.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /api/builds has the status %d", res.StatusCode)
	}
	a.waitBuilds(t)
	entry := a.entryDoc(t, e)
	if entry.Status != content.StatusPublished || entry.PublishedRevision != rev {
		t.Errorf("the entry after its time is %+v, want it published with the revision", entry)
	}
	if html, ok := a.page("en/on-time/"); !ok || !strings.Contains(html, "<h1>On time</h1>") {
		t.Errorf("the page after its time is %q (%v), want the page of the revision", html, ok)
	}
	published := a.mine(t, audit.EntryPublishScheduled, e)
	if len(published) != 1 || published[0].Actor != audit.System || published[0].Detail["scheduledBy"] != editor {
		t.Errorf("the records of entry.publishScheduled are %+v, want one by the system for what the editor scheduled", published)
	}
	last := readBuilds(t, a.as("viewer")).Last
	if last == nil || !hasRequest(last, e, content.StatusPublished) {
		t.Errorf("the last build is %+v, want one that took the publishing of the entry", last)
	}
}
