package admin

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
)

// entryRequest is a request to one of the routes of the entries.
type entryRequest struct {
	name, method, path, body string
}

// entryRequests returns a request of each route of the entries for the entry, which must be a draft post with a revision.
func (a *apiServer) entryRequests(t *testing.T, entry string) []entryRequest {
	t.Helper()
	item := "/api/entries/" + entry
	doc := a.stored(t, content.RefEntry, entry)
	return []entryRequest{
		{"a list", http.MethodGet, "/api/entries?limit=2", ""},
		{"a creation", http.MethodPost, "/api/entries", `{"type":"post","language":"ja"}`},
		{"a reading", http.MethodGet, item, ""},
		{"a deletion", http.MethodDelete, item + "?base=" + strconv.FormatInt(doc.Seq, 10), ""},
		{"a list of revisions", http.MethodGet, item + "/revisions", ""},
		{"a save", http.MethodPost, item + "/revisions", revisionBody(a.lastRevision(t, entry), postRevision("待った", "waited", ""))},
		{"a translation", http.MethodPost, item + "/translations", `{"language":"en"}`},
		{"the reading of a revision", http.MethodGet, "/api/revisions/" + a.lastRevision(t, entry), ""},
	}
}

// TestEntryRoutes shows which routes there are, and that each of them is a route of heavy work: it has its long deadline.
func TestEntryRoutes(t *testing.T) {
	a := newAPIServer(t)
	e := a.newEntry(t, "editor", "post", "ja")
	a.addRevision(t, "editor", e.ID, postRevision("題", "routes", ""))
	requests := a.entryRequests(t, e.ID)
	if len(requests) != 8 {
		t.Fatalf("%d routes are listed, want the 8 of the entries and their revisions", len(requests))
	}
	for _, r := range requests {
		_, pattern := a.mux.Handler(httptest.NewRequest(r.method, r.path, nil))
		if pattern == "" {
			t.Errorf("%s %s: no route", r.method, r.path)
			continue
		}
		if got := a.deadlineOf(pattern); got != heavyDeadline {
			t.Errorf("%s %s (%s) has the deadline %v, want that of heavy work, %v", r.method, r.path, pattern, got, heavyDeadline)
		}
	}
}

// TestEntriesHeavyPlace shows that the routes of the entries work in the place of heavy work one at a time: reading, listing and
// writing wait for the one that has it.
func TestEntriesHeavyPlace(t *testing.T) {
	a := newAPIServer(t)
	e := a.newEntry(t, "editor", "post", "ja")
	a.addRevision(t, "editor", e.ID, postRevision("題", "place", ""))
	requests := a.entryRequests(t, e.ID)
	before := a.versions(t)
	revs := a.revisionIDs(t, e.ID)
	g := newGate(t, a.testServer)

	first := async(func() *http.Response { return a.as("admin").get("/api/settings") })
	g.waitHeld(t)
	var waiting []<-chan *http.Response
	for _, r := range requests {
		if r.method == http.MethodDelete {
			continue // the others read the entry
		}
		login := "editor"
		if r.method == http.MethodGet {
			login = "viewer"
		}
		ch := async(func() *http.Response { return a.as(login).send(r.method, r.path, r.body) })
		waiting = append(waiting, ch)
		quiet(t, ch, "a request was answered while another had the place: "+r.name)
	}
	if n, held := g.calls.Load(), len(a.heavy); n != 1 || held != 1 {
		t.Errorf("%d requests reached the place and it holds %d, want 1 and 1", n, held)
	}
	if got := a.versions(t); got != before {
		t.Errorf("a request has worked while another had the place:\n%s\nwant\n%s", got, before)
	}
	if got := a.revisionIDs(t, e.ID); len(got) != len(revs) {
		t.Errorf("the revisions of the entry are %v, want %v: a save has worked while another request had the place", got, revs)
	}

	g.open()
	receive(t, first, "the first request")
	for i, ch := range waiting {
		res := receive(t, ch, "a request that waited")
		if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
			t.Errorf("the request %d that waited has the status %d: %s", i, res.StatusCode, bodyOf(t, res))
		}
	}
	if got := a.revisionIDs(t, e.ID); len(got) != len(revs)+1 {
		t.Errorf("the revisions of the entry are %v, want one more than %v: the save that waited has not been done", got, revs)
	}
	if n := len(a.heavy); n != 0 {
		t.Errorf("the place holds %d requests at the end, want none", n)
	}
}

// TestEntriesReadInPlace shows that what a route reads of the store it reads after it has the place, not before it waits for
// it: an entry or a revision that is made while the request waits is read as it is after the wait.
func TestEntriesReadInPlace(t *testing.T) {
	a := newAPIServer(t)
	editor := a.people["editor"].account.ID
	e := a.newEntry(t, "editor", "post", "ja")
	a.addRevision(t, "editor", e.ID, postRevision("待つ前", "before", ""))
	g := newGate(t, a.testServer)

	first := async(func() *http.Response { return a.as("admin").get("/api/settings") })
	g.waitHeld(t)
	list := async(func() *http.Response { return a.as("editor").get("/api/entries?limit=1") })
	waitFor(t, "the list to be counted", func() bool { return a.inflight.count(editor) == 1 })
	show := async(func() *http.Response { return a.as("editor").get("/api/entries/" + e.ID) })
	waitFor(t, "the reading to be counted", func() bool { return a.inflight.count(editor) == 2 })
	revisions := async(func() *http.Response { return a.as("editor").get("/api/entries/" + e.ID + "/revisions") })
	waitFor(t, "the list of revisions to be counted", func() bool { return a.inflight.count(editor) == 3 })

	// While they wait, a revision is saved and an entry is made, by the Editor and not by a route.
	ed := a.pub.Editor()
	op := edit.Op{By: a.people["admin"].account.ID}
	saved, err := ed.SaveRevision(t.Context(), op, e.ID, a.lastRevision(t, e.ID), []byte(postRevision("待つ間", "meanwhile", "")))
	if err != nil {
		t.Fatal(err)
	}
	made, err := ed.CreateEntry(t.Context(), op, "post", "ja")
	if err != nil {
		t.Fatal(err)
	}
	g.open()
	receive(t, first, "the first request")

	if got, l := entryListOf(t, receive(t, list, "the list")); len(got) != 1 || got[0] != made.ID || l.Total != 36 {
		t.Errorf("the list that waited has %v of %d, want the entry that was made while it waited, first, of the 36 that the store has then", got, l.Total)
	}
	var shown entryAnswer
	if err := json.Unmarshal([]byte(body(t, "the reading", receive(t, show, "the reading"), http.StatusOK)), &shown); err != nil ||
		shown.LatestRevision == nil || shown.LatestRevision.ID != saved.Revision {
		t.Errorf("the reading that waited has the last revision %+v (%v), want %s, which was saved while it waited", shown.LatestRevision, err, saved.Revision)
	}
	var list2 revisionListAnswer
	if err := json.Unmarshal([]byte(body(t, "the list of revisions", receive(t, revisions, "the list of revisions"), http.StatusOK)), &list2); err != nil ||
		list2.Total != 2 || len(list2.Items) != 2 || list2.Items[0].ID != saved.Revision {
		t.Errorf("the list of revisions that waited is %+v (%v), want the two, the one that was saved while it waited first", list2, err)
	}
}

// entryListOf reads the list of entries of res, which must answer 200, and returns the IDs of its rows and the answer.
func entryListOf(t *testing.T, res *http.Response) ([]string, entryListBody) {
	t.Helper()
	var l entryListBody
	b := body(t, "the list of entries", res, http.StatusOK)
	if err := json.Unmarshal([]byte(b), &l); err != nil {
		t.Fatalf("the answer %.300s is not a list: %v", b, err)
	}
	got := make([]string, 0, len(l.Items))
	for _, item := range l.Items {
		var d struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(item.Document, &d); err != nil {
			t.Fatal(err)
		}
		got = append(got, d.ID)
	}
	return got, l
}

// TestEntriesPerAccount shows that an account has four requests of heavy work at once, whichever route they are, and the fifth
// of any route of the entries is refused at once.
func TestEntriesPerAccount(t *testing.T) {
	a := newAPIServer(t)
	adminID := a.people["admin"].account.ID
	e := a.newEntry(t, "editor", "post", "ja")
	a.addRevision(t, "editor", e.ID, postRevision("題", "count", ""))
	requests := a.entryRequests(t, e.ID)
	before := a.versions(t)
	revs := a.revisionIDs(t, e.ID)
	g := newGate(t, a.testServer)

	var four [heavyPerAccount]<-chan *http.Response
	four[0] = async(func() *http.Response { return a.as("admin").get("/api/settings") })
	g.waitHeld(t)
	for i := 1; i < heavyPerAccount; i++ {
		four[i] = async(func() *http.Response { return a.as("admin").get("/api/entries?limit=1") })
	}
	waitFor(t, "four requests of the admin to be counted", func() bool { return a.inflight.count(adminID) == heavyPerAccount })
	for _, r := range requests {
		checkProblem(t, "the fifth request, "+r.name, a.as("admin").send(r.method, r.path, r.body), "too-many-requests")
	}
	if got := a.versions(t); got != before {
		t.Errorf("the requests that were refused have changed the store:\n%s\nwant\n%s", got, before)
	}
	if got := a.revisionIDs(t, e.ID); len(got) != len(revs) {
		t.Errorf("the revisions of the entry are %v, want %v", got, revs)
	}
	g.open()
	for i, ch := range four {
		if res := receive(t, ch, "a request of the admin"); res.StatusCode != http.StatusOK {
			t.Errorf("request %d of the admin has the status %d", i+1, res.StatusCode)
		}
	}
	waitFor(t, "the requests of the admin to end", func() bool { return a.inflight.count(adminID) == 0 })
}

// TestEntriesSlowSender shows that a revision that is sent slowly is received before the place is asked for, and counts for its
// account: the others read and write while it comes, and it is saved when it is whole.
func TestEntriesSlowSender(t *testing.T) {
	a, ls := newLiveAPIServer(t)
	editorID := a.people["editor"].account.ID
	e := a.newEntry(t, "editor", "post", "ja")
	revision := postRevision("ゆっくり", "slowly", "")
	request := revisionBody("", revision)
	half := len(request) / 2
	slow := ls.slowSend(t, http.MethodPost, "/api/entries/"+e.ID+"/revisions", a.people["editor"].token, request[:half])
	waitFor(t, "the slow request of the editor to be counted", func() bool { return a.inflight.count(editorID) == 1 })

	// Nobody has the place, so the others are answered at once, also if they write.
	for _, login := range []string{"admin", "viewer"} {
		res := receive(t, async(func() *http.Response { return a.as(login).get("/api/entries?limit=1") }), "a list while a body is sent slowly")
		if res.StatusCode != http.StatusOK {
			t.Errorf("a list of %s while a body is sent slowly has the status %d, want 200", login, res.StatusCode)
		}
	}
	made := receive(t, async(func() *http.Response {
		return a.as("admin").post("/api/entries", `{"type":"page","language":"ja"}`)
	}), "a creation while a body is sent slowly")
	if made.StatusCode != http.StatusCreated {
		t.Errorf("a creation while a body is sent slowly has the status %d, want 201", made.StatusCode)
	}
	if n := len(a.heavy); n != 0 {
		t.Errorf("the place holds %d requests while a body is received, want none", n)
	}
	quiet(t, slow.result, "the slow request was answered before its body was whole")
	if got := a.revisionIDs(t, e.ID); len(got) != 0 {
		t.Errorf("the revisions of the entry are %v while the body is not whole, want none", got)
	}

	slow.finish(request[half:])
	r := receive(t, slow.result, "the slow request")
	if r.err != nil || r.status != http.StatusCreated {
		t.Fatalf("the slow request: %d %.200s %v, want 201", r.status, r.body, r.err)
	}
	waitFor(t, "the requests of the editor to end", func() bool { return a.inflight.count(editorID) == 0 })
	if got := a.storedRevision(t, a.lastRevision(t, e.ID)); string(got.Data) != revision {
		t.Errorf("the revision is\n%s\nwant what the slow save sent", got.Data)
	}
	if files := a.tempFiles(t); len(files) != 0 {
		t.Errorf("%d temporary files stay: %v", len(files), files)
	}
}

// bigRevision is a revision of a post of about n bytes: paragraphs of 100 characters.
func bigRevision(title string, n int) string {
	var blocks []string
	for i := 0; len(blocks)*140 < n; i++ {
		blocks = append(blocks, textBlock(fmt.Sprintf("b_%08x", i), strings.Repeat("あ", 33)))
	}
	return postRevisionWith(title, "big", "", blocks...)
}

// TestEntriesBig shows revisions of more than the memory of a spool (256 KiB), which go through a temporary file in the body
// and in the answer, and leave none.
func TestEntriesBig(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("editor")
	e := a.newEntry(t, "editor", "post", "ja")
	loc := "/api/entries/" + e.ID
	big := bigRevision("大きい", 600_000)
	if len(big) <= spoolMemory*2 {
		t.Fatalf("the revision has %d bytes", len(big))
	}

	saved := result(t, "POST a big revision", as.post(loc+"/revisions", revisionBody("", big)), http.StatusCreated)
	stored := a.storedRevision(t, saved.Revision)
	if string(stored.Data) != big {
		t.Errorf("the stored revision has %d bytes, want the %d that were sent", len(stored.Data), len(big))
	}
	res := as.get(loc)
	got := body(t, "GET a big entry", res, http.StatusOK)
	var shown entryAnswer
	if err := json.Unmarshal([]byte(got), &shown); err != nil || shown.LatestRevision == nil || string(shown.LatestRevision.Document) != big || len(got) <= spoolMemory {
		t.Errorf("GET an entry with a big revision answers %d bytes, want the revision as it is, in more than %d bytes: %v", len(got), spoolMemory, err)
	}
	if want := strconv.Itoa(len(got)); res.Header.Get("Content-Length") != want {
		t.Errorf("GET a big entry has the Content-Length %q, want %q", res.Header.Get("Content-Length"), want)
	}
	var one revisionAnswerBody
	if err := json.Unmarshal([]byte(body(t, "GET a big revision", as.get("/api/revisions/"+saved.Revision), http.StatusOK)), &one); err != nil || string(one.Document) != big {
		t.Errorf("GET a big revision answers %d bytes of document (%v), want the %d that were sent", len(one.Document), err, len(big))
	}
	// The list reads its title, without the rest.
	_, list := a.entryList(t, "viewer", "/api/entries?limit=1")
	if row := list.Items[0]; row.LatestRevision == nil || row.LatestRevision.Title == nil || *row.LatestRevision.Title != "大きい" || row.LatestRevision.ID != saved.Revision {
		t.Errorf("the first row of the list is %+v, want the entry with the big revision and its title", row)
	}

	// More than the most that a body may have is refused before it is read in.
	tooBig := strings.Repeat(" ", documentBodyLimit+1)
	checkProblem(t, "a body of more than a document and its wrap", as.post(loc+"/revisions", tooBig), "too-large")
	// And a revision of more than the most that the store has is a problem of the revision.
	huge := `{"schemaVersion":1,"title":"` + strings.Repeat("a", content.MaxDocumentBytes) + `"}`
	p := firstProblem(t, "a revision of more than 64 MiB", as.post(loc+"/revisions", revisionBody(saved.Revision, huge)))
	if !strings.Contains(p.Detail, "too large") || !revisionFile.MatchString(p.File) {
		t.Errorf("the error of a revision of more than 64 MiB is %+v", p)
	}

	if files := a.tempFiles(t); len(files) != 0 {
		t.Errorf("%d temporary files stay: %v", len(files), files)
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}
