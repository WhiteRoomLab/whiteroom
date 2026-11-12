package admin

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// TestPublishThroughAPI is the end of the issue's conditions: through HTTP and nothing else, an editor makes an entry, saves a
// draft of it and publishes it, and the page is in the output directory when the build has ended; a viewer cannot publish, and
// nothing is published. The requests are those of the browser: the cookie of the login and the origin. The output directory is
// the temporary one of the test.
func TestPublishThroughAPI(t *testing.T) {
	a := newAPIServer(t)
	editor, viewer := a.people["editor"].account.ID, a.people["viewer"].account.ID
	as, asViewer := a.as("editor"), a.as("viewer")
	const (
		title = "Published through the API"
		slug  = "through-the-api"
	)

	// Make a page in English, and save a draft of it.
	res := as.post("/api/entries", `{"type":"page","language":"en"}`)
	location := res.Header.Get("Location")
	created := result(t, "POST /api/entries", res, http.StatusCreated)
	if want := "/api/entries/" + created.ID; location != want || created.Kind != content.RefEntry || created.Publishes {
		t.Fatalf("the creation answers %+v with the Location %q, want a new entry at %s that publishes nothing", created, location, want)
	}
	saved := result(t, "POST "+location+"/revisions", as.post(location+"/revisions", revisionBody("", pageRevision(title, slug))), http.StatusCreated)

	// Read the entry for the version to publish and its last revision.
	shown := readEntry(t, as, location)
	if shown.LatestRevision == nil || shown.LatestRevision.ID != saved.Revision || statusOf(t, shown.Document) != content.StatusDraft {
		t.Fatalf("the entry is %+v, want a draft whose last revision is the one that was saved", shown)
	}
	publish := fmt.Sprintf(`{"base":%d,"revision":%q}`, shown.Version.Seq, shown.LatestRevision.ID)

	// A viewer cannot publish it: 403, recorded, and the entry is a draft and no page is made.
	checkProblem(t, "the publishing by a viewer", asViewer.post(location+"/publish", publish), "forbidden")
	if st := settled(t, asViewer); st.Last == nil || hasRequest(st.Last, created.ID, content.StatusPublished) {
		t.Errorf("the state after the refused publishing is %+v, want no build that took a publishing of the entry", st)
	}
	if _, ok := a.page("en/" + slug + "/"); ok {
		t.Error("the page is in the output directory after a publishing that was refused")
	}
	if got := statusOf(t, readEntry(t, as, location).Document); got != content.StatusDraft {
		t.Errorf("the entry after the refused publishing is %s, want a draft", got)
	}
	denied := a.records(t, audit.AccessDenied)
	if len(denied) != 1 || denied[0].Actor != viewer || denied[0].Target != (audit.Target{Kind: "entry", ID: created.ID}) ||
		denied[0].Detail["permission"] != "entry.publish" {
		t.Errorf("the records of access.denied are %+v, want one for the viewer's publishing of the entry", denied)
	}

	// The editor publishes it: 200 and a build is asked for.
	pub := result(t, "POST "+location+"/publish", as.post(location+"/publish", publish), http.StatusOK)
	if pub.Kind != content.RefEntry || pub.ID != created.ID || pub.Status != content.StatusPublished || pub.Revision != saved.Revision || !pub.Publishes {
		t.Errorf("the publishing answers %+v, want the entry published with the revision that was saved, and a publish", pub)
	}

	// Read the state of the builds until the build that took the publishing has ended and none runs or is wanted.
	var st buildsAnswer
	waitFor(t, "the build of the publishing to end", func() bool {
		st = readBuilds(t, asViewer)
		return st.idle() && st.Last != nil && hasRequest(st.Last, created.ID, content.StatusPublished)
	})
	last := st.Last
	if last.Failed == nil || *last.Failed || last.Release == "" || last.Changed == nil || !*last.Changed || len(last.Errors) != 0 {
		t.Fatalf("the last build is %+v, want one that published a release", last)
	}
	if got := readReleases(t, asViewer); got.Current != last.Release {
		t.Errorf("the published release is %s, want that of the last build, %s", got.Current, last.Release)
	}

	// The page is in the release that the output directory publishes, and has the title of the revision.
	html, ok := a.page("en/" + slug + "/")
	if !ok {
		t.Fatalf("the page en/%s/ is not in the output directory", slug)
	}
	for _, want := range []string{"<title>" + title + " | Example Inc.</title>", "<h1>" + title + "</h1>", `<html lang="en">`} {
		if !strings.Contains(html, want) {
			t.Errorf("the page does not have %s:\n%.600s", want, html)
		}
	}

	// The list of the entries says it is published, at the URL of the page.
	ids, list := a.entryList(t, "viewer", "/api/entries?type=page&language=en&status=published&limit=1")
	if len(ids) != 1 || ids[0] != created.ID || list.Items[0].Published == nil || list.Items[0].Published.Slug != slug {
		t.Errorf("the first published page in English is %v (%+v), want the new page, which is the last made, with its slug", ids, list.Items)
	}

	// The activity log says who did what: the editor made, saved and published it, and the system built.
	for _, action := range []audit.Action{audit.EntryCreate, audit.EntrySaveRevision, audit.EntryPublish} {
		if got := a.mine(t, action, created.ID); len(got) != 1 || got[0].Actor != editor || got[0].Log != audit.ActivityLog {
			t.Errorf("the records of %s for the entry are %+v, want one by the editor", action, got)
		}
	}
	waitFor(t, "the record of the build", func() bool {
		for _, r := range a.records(t, audit.Build) {
			if r.Target.ID == last.Release {
				return r.Actor == audit.System && r.Target.Kind == audit.KindRelease && r.Changed && r.Result == "" && r.Detail["requestsOperation"] == "1"
			}
		}
		return false
	})
	if got := len(a.records(t, audit.AccessDenied)); got != 1 {
		t.Errorf("%d records of access.denied, want the one refusal", got)
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// readEntry reads the entry at the path as the sender.
func readEntry(t *testing.T, as asPerson, path string) entryAnswer {
	t.Helper()
	var shown entryAnswer
	if err := json.Unmarshal([]byte(body(t, "GET "+path, as.get(path), http.StatusOK)), &shown); err != nil {
		t.Fatal(err)
	}
	return shown
}

// statusOf returns the status in the state document of an entry.
func statusOf(t *testing.T, document jsontext.Value) content.Status {
	t.Helper()
	var e content.Entry
	if err := e.UnmarshalJSON(document); err != nil {
		t.Fatal(err)
	}
	return e.Status
}
