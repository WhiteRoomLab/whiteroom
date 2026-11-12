package admin

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// TestDocumentRoutes shows which routes there are, and that each of them is a route of heavy work: it has its long deadline.
func TestDocumentRoutes(t *testing.T) {
	a := newAPIServer(t)
	patterns := map[string]bool{}
	for _, d := range docKinds {
		item := "/api/" + d.path + "/" + newsTerm
		for _, r := range []struct{ method, path string }{
			{http.MethodGet, "/api/" + d.path}, {http.MethodGet, item}, {http.MethodPut, item}, {http.MethodDelete, item},
		} {
			patterns[r.method+" "+r.path] = true
		}
		if d.create.action != "" {
			patterns[http.MethodPost+" /api/"+d.path] = true
		}
	}
	patterns["GET /api/settings"], patterns["PUT /api/settings"] = true, true
	if want := 6*4 + 5 + 2; len(patterns) != want {
		t.Fatalf("%d routes are expected, want %d: 4 for each of the 6 kinds, a POST for 5 of them, and 2 for the settings", len(patterns), want)
	}
	for route := range patterns {
		method, path, _ := strings.Cut(route, " ")
		_, pattern := a.mux.Handler(httptest.NewRequest(method, path, nil))
		if pattern == "" {
			t.Errorf("%s: no route", route)
			continue
		}
		if got := a.deadlineOf(pattern); got != heavyDeadline {
			t.Errorf("%s (%s) has the deadline %v, want that of heavy work, %v", route, pattern, got, heavyDeadline)
		}
	}
}

// TestDocumentsHeavyPlace shows that the routes of the documents work in the place of heavy work one at a time: reading, listing
// and writing wait for the one that has it.
func TestDocumentsHeavyPlace(t *testing.T) {
	a := newAPIServer(t)
	g := newGate(t, a.testServer)
	loc := "/api/terms/" + newsTerm
	news := a.stored(t, content.RefTerm, newsTerm)
	changed := strings.Replace(string(news.Data), "お知らせ", "お知らせ6", 1)

	first := async(func() *http.Response { return a.as("admin").get(loc) })
	g.waitHeld(t)
	var waiting []<-chan *http.Response
	for _, do := range []func() *http.Response{
		func() *http.Response { return a.as("editor").get("/api/terms") },
		func() *http.Response { return a.as("viewer").get("/api/settings") },
		func() *http.Response { return a.as("editor").put(loc, saveBody(news.Seq, changed)) },
		func() *http.Response { return a.as("admin").get("/api/media?uploadedBy=" + importer) },
		func() *http.Response { return a.as("author").get("/api/authors/" + yamada) },
	} {
		ch := async(do)
		waiting = append(waiting, ch)
		quiet(t, ch, "a request was answered while another had the place")
	}
	if n, held := g.calls.Load(), len(a.heavy); n != 1 || held != 1 {
		t.Errorf("%d requests reached the place and it holds %d, want 1 and 1", n, held)
	}
	if got := a.stored(t, content.RefTerm, newsTerm); got.Seq != news.Seq {
		t.Error("the save has worked while another request had the place")
	}

	g.open()
	if got := body(t, "the first request", receive(t, first, "the first request"), http.StatusOK); got != wantFrame(t, news) {
		t.Errorf("the first request answers\n%s\nwant\n%s", got, wantFrame(t, news))
	}
	for i, ch := range waiting {
		if res := receive(t, ch, "a request that waited"); res.StatusCode != http.StatusOK {
			t.Errorf("the request %d that waited has the status %d: %s", i, res.StatusCode, bodyOf(t, res))
		}
	}
	if got := a.stored(t, content.RefTerm, newsTerm); string(got.Data) != strings.TrimSpace(changed) {
		t.Error("the save that waited has not been done")
	}
	if n := len(a.heavy); n != 0 {
		t.Errorf("the place holds %d requests at the end, want none", n)
	}
}

// TestDocumentsReadInPlace shows that what a route reads of the store it reads after it has the place, not before it waits for
// it: a document that changes while the request waits is read as it is after the wait.
func TestDocumentsReadInPlace(t *testing.T) {
	a := newAPIServer(t)
	g := newGate(t, a.testServer)
	editor := a.people["editor"].account.ID

	first := async(func() *http.Response { return a.as("admin").get("/api/settings") })
	g.waitHeld(t)
	list := async(func() *http.Response { return a.as("editor").get("/api/terms?language=ja") })
	waitFor(t, "the list to be counted", func() bool { return a.inflight.count(editor) == 1 })
	show := async(func() *http.Response { return a.as("editor").get("/api/terms/" + newsTerm) })
	waitFor(t, "the reading to be counted", func() bool { return a.inflight.count(editor) == 2 })

	// While they wait, the term changes, and a term is made, by the Editor and not by a route.
	news := a.stored(t, content.RefTerm, newsTerm)
	changed := strings.Replace(string(news.Data), "お知らせ", "お知らせ7", 1)
	op := edit.Op{By: a.people["admin"].account.ID}
	saved, err := a.pub.Editor().Save(t.Context(), op, content.RefTerm, newsTerm, news.Seq, []byte(changed))
	if err != nil {
		t.Fatal(err)
	}
	made, err := a.pub.Editor().Create(t.Context(), op, content.RefTerm, []byte(termDoc("category", "ja", "後から", "later")))
	if err != nil {
		t.Fatal(err)
	}
	g.open()
	receive(t, first, "the first request")

	var l listAnswer
	if got := body(t, "the list", receive(t, list, "the list"), http.StatusOK); !strings.Contains(got, made.ID) {
		t.Errorf("the list that waited does not have the term that was made while it waited: %.200s", got)
	} else if err := json.Unmarshal([]byte(got), &l); err != nil || l.Total != 5 {
		t.Errorf("the list that waited has the total %d (%v), want 5: the 4 of the language and the new one", l.Total, err)
	}
	if got := body(t, "the reading", receive(t, show, "the reading"), http.StatusOK); got != wantFrame(t, a.stored(t, content.RefTerm, newsTerm)) ||
		!strings.Contains(got, `"seq":`+strconv.FormatInt(saved.Version.Seq, 10)+`,`) {
		t.Errorf("the reading that waited answers %.300s, want the version %d that was saved while it waited", got, saved.Version.Seq)
	}
}

// TestDocumentsPerAccount shows that an account has four requests of heavy work at once, whichever route they are, and the
// fifth is refused at once, with the others of other accounts only waiting.
func TestDocumentsPerAccount(t *testing.T) {
	a := newAPIServer(t)
	adminID := a.people["admin"].account.ID
	g := newGate(t, a.testServer)
	loc := "/api/terms/" + newsTerm
	news := a.stored(t, content.RefTerm, newsTerm)
	doc := termDoc("category", "ja", "ニュースレター", "newsletter")

	var four [heavyPerAccount]<-chan *http.Response
	four[0] = async(func() *http.Response { return a.as("admin").get(loc) })
	g.waitHeld(t)
	for i := 1; i < heavyPerAccount; i++ {
		four[i] = async(func() *http.Response { return a.as("admin").get("/api/terms") })
	}
	waitFor(t, "four requests of the admin to be counted", func() bool { return a.inflight.count(adminID) == heavyPerAccount })

	before := a.versions(t)
	as := a.as("admin")
	for name, res := range map[string]*http.Response{
		"a list":                 as.get("/api/media"),
		"a reading":              as.get(loc),
		"the settings":           as.get("/api/settings"),
		"a creation":             as.post("/api/terms", createBody(doc)),
		"a save":                 as.put(loc, saveBody(news.Seq, string(news.Data))),
		"a deletion":             as.del(loc + "?base=" + strconv.FormatInt(news.Seq, 10)),
		"a save of the settings": as.put("/api/settings", saveBody(1, "{}")),
	} {
		checkProblem(t, "the fifth request, "+name, res, "too-many-requests")
	}
	if got := a.versions(t); got != before {
		t.Errorf("the requests that were refused have changed the store:\n%s\nwant\n%s", got, before)
	}
	if n := a.inflight.count(adminID); n != heavyPerAccount {
		t.Errorf("the admin has %d requests counted after the fifth ones were refused, want %d", n, heavyPerAccount)
	}

	// The others are not refused: they wait for the place.
	editor := async(func() *http.Response { return a.as("editor").get("/api/terms") })
	quiet(t, editor, "a request of another account was answered while another had the place")
	g.open()
	for i, ch := range four {
		if res := receive(t, ch, "a request of the admin"); res.StatusCode != http.StatusOK {
			t.Errorf("request %d of the admin has the status %d", i+1, res.StatusCode)
		}
	}
	if res := receive(t, editor, "the request of the editor"); res.StatusCode != http.StatusOK {
		t.Errorf("the request of the editor has the status %d", res.StatusCode)
	}
	if n := a.inflight.count(adminID); n != 0 {
		t.Errorf("the admin has %d requests counted at the end, want none", n)
	}
	if res := as.get("/api/terms"); res.StatusCode != http.StatusOK {
		t.Errorf("a request of the admin after them has the status %d, want 200", res.StatusCode)
	}
}

// TestDocumentsSlowSender shows that a document that is sent slowly is received before the place is asked for: the others
// read and write while it comes, and it is saved when it is whole.
func TestDocumentsSlowSender(t *testing.T) {
	a, ls := newLiveAPIServer(t)
	editorID := a.people["editor"].account.ID
	news := a.stored(t, content.RefTerm, newsTerm)
	changed := strings.Replace(string(news.Data), "お知らせ", "お知らせ8", 1)

	for _, tt := range []struct {
		name, method, path, body string
		status                   int
	}{
		{"a save", http.MethodPut, "/api/terms/" + newsTerm, saveBody(news.Seq, changed), http.StatusOK},
		{"a creation", http.MethodPost, "/api/terms", createBody(termDoc("category", "ja", "ゆっくり", "slowly")), http.StatusCreated},
	} {
		t.Run(tt.name, func(t *testing.T) {
			half := len(tt.body) / 2
			slow := ls.slowSend(t, tt.method, tt.path, a.people["editor"].token, tt.body[:half])
			waitFor(t, "the slow request of the editor to be counted", func() bool { return a.inflight.count(editorID) == 1 })

			// Nobody has the place, so the others are answered at once, also if they write.
			for _, login := range []string{"admin", "viewer"} {
				res := receive(t, async(func() *http.Response { return a.as(login).get("/api/terms") }), "a list while a body is sent slowly")
				if res.StatusCode != http.StatusOK {
					t.Errorf("a list of %s while a body is sent slowly has the status %d, want 200", login, res.StatusCode)
				}
			}
			settings := a.stored(t, store.SettingsKind, "")
			other := receive(t, async(func() *http.Response {
				return a.as("admin").put("/api/settings", saveBody(settings.Seq, string(settings.Data)))
			}), "a save of the settings while a body is sent slowly")
			if other.StatusCode != http.StatusOK {
				t.Errorf("a save of the settings while a body is sent slowly has the status %d, want 200", other.StatusCode)
			}
			if n := len(a.heavy); n != 0 {
				t.Errorf("the place holds %d requests while a body is received, want none", n)
			}
			quiet(t, slow.result, "the slow request was answered before its body was whole")

			slow.finish(tt.body[half:])
			r := receive(t, slow.result, "the slow request")
			if r.err != nil || r.status != tt.status {
				t.Fatalf("the slow request: %d %.200s %v, want %d", r.status, r.body, r.err, tt.status)
			}
			waitFor(t, "the requests of the editor to end", func() bool { return a.inflight.count(editorID) == 0 })
		})
	}
	if got := a.stored(t, content.RefTerm, newsTerm); string(got.Data) != strings.TrimSpace(changed) {
		t.Errorf("the term is\n%s\nwant what the slow save sent", got.Data)
	}
	if files := a.tempFiles(t); len(files) != 0 {
		t.Errorf("%d temporary files stay: %v", len(files), files)
	}
}

// TestDocumentsBig shows documents of more than the memory of a spool (256 KiB), which go through a temporary file in the
// body and in the answer, and leave none.
func TestDocumentsBig(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("editor")
	big := func(n int) string {
		return `{"schemaVersion":1,"taxonomy":"category","language":"ja","name":"大きい","slug":"big","description":"` + strings.Repeat("a", n) + `"}`
	}

	res := as.post("/api/terms", createBody(big(300_000)))
	created := result(t, "POST a big term", res, http.StatusCreated)
	loc := "/api/terms/" + created.ID
	doc := a.stored(t, content.RefTerm, created.ID)
	if len(doc.Data) < 300_000 {
		t.Fatalf("the stored term has %d bytes", len(doc.Data))
	}
	res = as.get(loc)
	if got := body(t, "GET a big term", res, http.StatusOK); got != wantFrame(t, doc) || len(got) <= spoolMemory {
		t.Errorf("GET a big term answers %d bytes, want the frame of the stored term, more than %d bytes", len(got), spoolMemory)
	}
	if got, want := res.Header.Get("Content-Length"), strconv.Itoa(len(wantFrame(t, doc))); got != want {
		t.Errorf("GET a big term has the Content-Length %q, want %q", got, want)
	}

	bigger := strings.Replace(string(doc.Data), strings.Repeat("a", 300_000), strings.Repeat("a", 400_000), 1)
	saved := result(t, "PUT a bigger term", as.put(loc, saveBody(doc.Seq, bigger)), http.StatusOK)
	doc = a.stored(t, content.RefTerm, created.ID)
	if doc.Seq != saved.Version.Seq || string(doc.Data) != bigger {
		t.Errorf("the stored term is %d bytes of version %d, want the %d bytes that were sent", len(doc.Data), doc.Seq, len(bigger))
	}
	list := body(t, "GET the terms", as.get("/api/terms?taxonomy=category"), http.StatusOK)
	if !strings.Contains(list, string(doc.Data)) || len(list) <= spoolMemory {
		t.Errorf("the list of terms has %d bytes, want the big term in it as it is", len(list))
	}
	result(t, "DELETE a big term", as.del(loc+"?base="+strconv.FormatInt(doc.Seq, 10)), http.StatusOK)

	// More than the most that a body may have is refused before it is read in.
	tooBig := make([]byte, documentBodyLimit+1)
	for i := range tooBig {
		tooBig[i] = ' '
	}
	checkProblem(t, "a body of more than a document and its wrap", as.post("/api/terms", string(tooBig)), "too-large")
	// And a document of more than the most that the store has is a problem of the document.
	e := firstProblem(t, "a document of more than 64 MiB", as.post("/api/terms", createBody(big(content.MaxDocumentBytes))))
	if !strings.Contains(e.Detail, "too large") {
		t.Errorf("the error of a document of more than 64 MiB is %+v", e)
	}

	if files := a.tempFiles(t); len(files) != 0 {
		t.Errorf("%d temporary files stay: %v", len(files), files)
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}
