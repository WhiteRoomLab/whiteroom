package admin

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// TestPublishProblems shows the refusals of the operations: a version or a revision that is not the current one is a conflict,
// what the checks of the Editor find is 422 and located, and an entry that is not there is 404. Nothing is changed.
func TestPublishProblems(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("editor")
	e, first := a.draftPage(t, "editor", "en", "Problems", "problems")
	item := "/api/entries/" + e
	last := a.addRevision(t, "editor", e, pageRevision("Problems 2", "problems")).Revision
	seq := a.stored(t, content.RefEntry, e).Seq
	publishAt := func(base int64, revision string) string {
		return fmt.Sprintf(`{"base":%d,"revision":%q}`, base, revision)
	}
	empty := a.newEntry(t, "editor", "page", "en")
	bare := a.newEntry(t, "editor", "page", "en")
	bareRev := a.addRevision(t, "editor", bare.ID, `{"schemaVersion":1}`).Revision
	a.waitBuilds(t)
	before, records := a.versions(t), len(a.records(t, ""))

	// 409: a revision that is not the last one, one that the entry does not have, and a version that is not the current one.
	checkProblem(t, "publish of a revision that is not the last", as.post(item+"/publish", publishAt(seq, first)), "conflict")
	checkProblem(t, "publish of a revision that the entry lacks", as.post(item+"/publish", publishAt(seq, missingRevision)), "conflict")
	checkProblem(t, "publish at a later version", as.post(item+"/publish", publishAt(seq+1, last)), "conflict")
	checkProblem(t, "publish at the version 0", as.post(item+"/publish", publishAt(0, last)), "conflict")
	checkProblem(t, "schedule of a revision that is not the last", as.post(item+"/schedule",
		fmt.Sprintf(`{"base":%d,"revision":%q,"at":%q}`, seq, first, a.clock.now().Add(time.Hour).Format(time.RFC3339))), "conflict")
	checkProblem(t, "unpublish at a later version", as.post(item+"/unpublish", fmt.Sprintf(`{"base":%d}`, seq+1)), "conflict")
	checkProblem(t, "archive at the version 0", as.post(item+"/archive", `{"base":0}`), "conflict")

	// 422: an entry without a revision.
	emptySeq := a.stored(t, content.RefEntry, empty.ID).Seq
	p := firstProblem(t, "publish of an entry without a revision", as.post("/api/entries/"+empty.ID+"/publish", publishAt(emptySeq, last)))
	if !entryFile.MatchString(p.File) || p.Pointer != "" || !strings.Contains(p.Detail, "has no revision to publish") {
		t.Errorf("the error of an entry without a revision is %+v, want its file and that it has no revision to publish", p)
	}

	// 422: a revision that cannot be published, with its problems in the file of the revision.
	res := as.post("/api/entries/"+bare.ID+"/publish", publishAt(a.stored(t, content.RefEntry, bare.ID).Seq, bareRev))
	var pointers []string
	for _, pe := range readInvalid(t, "publish of a revision without a title", res) {
		if pe.File != "revisions/"+bareRev+".json" {
			t.Errorf("the error %+v is in the file %s, want that of the revision", pe, pe.File)
		}
		pointers = append(pointers, pe.Pointer)
	}
	if !slices.Contains(pointers, "/title") || !slices.Contains(pointers, "/slug") {
		t.Errorf("the errors of a revision without a title and a slug are at %v, want /title and /slug", pointers)
	}

	// 422: scheduling in the past, now, or a published entry.
	for _, at := range []time.Time{a.clock.now().Add(-time.Hour), a.clock.now(), a.clock.now().Add(-1000 * 24 * time.Hour)} {
		p = firstProblem(t, "schedule at "+at.Format(time.RFC3339), as.post(item+"/schedule",
			fmt.Sprintf(`{"base":%d,"revision":%q,"at":%q}`, seq, last, at.Format(time.RFC3339))))
		if p.File != "entries/"+e+".json" || p.Pointer != "/scheduledAt" || !strings.Contains(p.Detail, "publish it instead") {
			t.Errorf("the error of a schedule at %s is %+v, want the file of the entry, /scheduledAt and that it is to be published instead", at, p)
		}
	}
	if got := a.versions(t); got != before {
		t.Errorf("the requests that were refused changed the store:\n%s\nwant\n%s", got, before)
	}
	if got := len(a.records(t, "")); got != records {
		t.Errorf("the requests that were refused made %d records, want none", got-records)
	}

	result(t, "publish", as.post(item+"/publish", publishAt(seq, last)), http.StatusOK)
	published := a.stored(t, content.RefEntry, e).Seq
	p = firstProblem(t, "schedule of a published entry", as.post(item+"/schedule",
		fmt.Sprintf(`{"base":%d,"revision":%q,"at":%q}`, published, last, a.clock.now().Add(time.Hour).Format(time.RFC3339))))
	if p.File != "entries/"+e+".json" || p.Pointer != "/status" || !strings.Contains(p.Detail, "unpublish it first") {
		t.Errorf("the error of the schedule of a published entry is %+v, want the file of the entry, /status and that it is to be unpublished first", p)
	}
	if got := a.stored(t, content.RefEntry, e).Seq; got != published {
		t.Errorf("the entry is at the version %d, want %d", got, published)
	}
}

// TestPublishNotFound shows that a path that names no entry is 404 for every route: an ID that no entry has, one that is not
// the form of an ID of an entry, and the ID of something else.
func TestPublishNotFound(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("editor")
	a.waitBuilds(t)
	before, records := a.versions(t), len(a.records(t, ""))
	bodies := map[string]string{
		"publish":   `{"base":1,"revision":"` + missingRevision + `"}`,
		"unpublish": `{"base":1}`,
		"archive":   `{"base":1}`,
		"schedule":  fmt.Sprintf(`{"base":1,"revision":%q,"at":%q}`, missingRevision, a.clock.now().Add(time.Hour).Format(time.RFC3339)),
	}
	for route, body := range bodies {
		for _, id := range []string{missingEntry, "ent_x", newsTerm, "x"} {
			checkProblem(t, "POST /api/entries/"+id+"/"+route, as.post("/api/entries/"+id+"/"+route, body), "not-found")
		}
	}
	if got := a.versions(t); got != before {
		t.Errorf("the requests changed the store:\n%s\nwant\n%s", got, before)
	}
	// The entry that is not there is found in the Update, so that a refusal of the permission, and not this, is what a viewer sees.
	if got := len(a.records(t, "")); got != records {
		t.Errorf("the requests made %d records, want none", got-records)
	}
	checkProblem(t, "a viewer's publishing of an entry that is not there", a.as("viewer").post("/api/entries/"+missingEntry+"/publish", bodies["publish"]), "forbidden")
}

// TestPublishShapes shows that a body that is not the one a route takes is 400 before anything is done, and that the media type,
// the size, the query and the header are checked as for the other routes.
func TestPublishShapes(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("editor")
	e, rev := a.draftPage(t, "editor", "en", "Shapes", "shapes")
	item := "/api/entries/" + e
	seq := a.stored(t, content.RefEntry, e).Seq
	at := a.clock.now().Add(time.Hour).Format(time.RFC3339)
	r := fmt.Sprintf("%q", rev)
	a.waitBuilds(t)
	before, records := a.versions(t), len(a.records(t, ""))

	bad := func(route string, bodies ...string) {
		t.Helper()
		for _, body := range bodies {
			checkProblem(t, fmt.Sprintf("POST %s%s with the body %q", item, route, body), as.post(item+route, body, withBody(body)...), "bad-request")
		}
	}
	bad("/publish",
		``, `null`, `[]`, `7`, `{}`, `{"base":`+fmt.Sprint(seq)+`}`, `{"revision":`+r+`}`,
		`{"base":-1,"revision":`+r+`}`, `{"base":"1","revision":`+r+`}`, `{"base":1.5,"revision":`+r+`}`, `{"base":null,"revision":`+r+`}`,
		`{"base":1,"revision":null}`, `{"base":1,"revision":""}`, `{"base":1,"revision":"x"}`, `{"base":1,"revision":7}`,
		`{"base":1,"revision":"`+missingEntry+`"}`, `{"base":1,"revision":`+r+`,"at":"`+at+`"}`, `{"base":1,"base":2,"revision":`+r+`}`,
		`{"Base":1,"revision":`+r+`}`)
	bad("/unpublish", ``, `{}`, `{"base":-1}`, `{"base":"1"}`, `{"base":1,"revision":`+r+`}`, `{"base":null}`)
	bad("/archive", ``, `{}`, `{"base":-1}`, `{"base":true}`, `{"base":1,"at":"`+at+`"}`)
	bad("/schedule",
		``, `{}`, `{"base":1,"revision":`+r+`}`, `{"base":1,"at":"`+at+`"}`, `{"revision":`+r+`,"at":"`+at+`"}`,
		`{"base":-1,"revision":`+r+`,"at":"`+at+`"}`, `{"base":1,"revision":"x","at":"`+at+`"}`, `{"base":1,"revision":`+r+`,"at":null}`,
		`{"base":1,"revision":`+r+`,"at":5}`, `{"base":1,"revision":`+r+`,"at":""}`, `{"base":1,"revision":`+r+`,"at":"tomorrow"}`,
		`{"base":1,"revision":`+r+`,"at":"2026-11-09"}`, `{"base":1,"revision":`+r+`,"at":"2026-11-09 12:00:00"}`,
		`{"base":1,"revision":`+r+`,"at":"2026-11-09T12:00:00"}`, `{"base":1,"revision":`+r+`,"at":"2026-11-09T12:00:00+25:00"}`,
		`{"base":1,"revision":`+r+`,"at":"2026-13-09T12:00:00Z"}`, `{"base":1,"revision":`+r+`,"at":"`+at+`","extra":1}`)

	// The media type is JSON, whatever the body, and the size is at most 64 KiB.
	checkProblem(t, "a publishing as text", as.post(item+"/publish", a.publishBody(t, e), "Content-Type", "text/plain"), "unsupported-media-type")
	checkProblem(t, "a publishing without a media type", as.send(http.MethodPost, item+"/archive", ``), "unsupported-media-type")
	big := `{"base":1,"revision":"` + strings.Repeat("a", smallBodyLimit) + `"}`
	checkProblem(t, "a publishing of more than 64 KiB", as.post(item+"/publish", big), "too-large")

	// The routes take no query and no other header than the key of the operation, which has the form of an operation ID.
	checkProblem(t, "a publishing with a query", as.post(item+"/publish?base=1", a.publishBody(t, e)), "bad-query")
	checkProblem(t, "a publishing with a query that does not parse", as.post(item+"/archive?%", a.baseBody(t, e)), "bad-query")
	checkProblem(t, "a publishing with a key that is no operation ID", as.post(item+"/publish", a.publishBody(t, e), "Idempotency-Key", `"x"`), "bad-header")
	checkProblem(t, "a publishing with two keys", as.post(item+"/publish", a.publishBody(t, e), append(key(1), key(2)...)...), "bad-header")

	if got := a.versions(t); got != before {
		t.Errorf("the requests that were refused changed the store:\n%s\nwant\n%s", got, before)
	}
	if got := len(a.records(t, "")); got != records {
		t.Errorf("the requests that were refused made %d records, want none", got-records)
	}
	a.waitBuilds(t)
	if got := a.entryDoc(t, e); got.Status != content.StatusDraft {
		t.Errorf("the entry is %+v, want a draft", got)
	}
}

// readInvalid reads the errors of res, which must be the problem 422 invalid.
func readInvalid(t *testing.T, what string, res *http.Response) []problemError {
	t.Helper()
	p := readProblem(t, res)
	if p.Status != http.StatusUnprocessableEntity || p.Code != "invalid" || p.Detail != detailInvalid || len(p.Errors) == 0 {
		t.Fatalf("%s: the problem is %+v, want the problem invalid with errors", what, p)
	}
	return p.Errors
}
