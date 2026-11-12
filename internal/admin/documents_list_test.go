package admin

import (
	"encoding/json/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// listOf reads the list at path as the account login, which must answer 200.
func (a *apiServer) listOf(t *testing.T, login, path string) listAnswer {
	t.Helper()
	var l listAnswer
	b := body(t, "GET "+path, a.as(login).get(path), http.StatusOK)
	if err := json.Unmarshal([]byte(b), &l); err != nil {
		t.Fatalf("GET %s: the answer %.300s is not a list: %v", path, b, err)
	}
	return l
}

// everyDocument keeps the JSON of every document.
func everyDocument([]byte) bool { return true }

// TestDocumentLists shows the lists of the documents: their pages, the total after the filters, and the filters of each kind.
func TestDocumentLists(t *testing.T) {
	a := newAPIServer(t)
	terms := func(keep func(x content.Term) bool) []string {
		return a.idsWhere(t, content.RefTerm, func(data []byte) bool {
			var x content.Term
			return x.UnmarshalJSON(data) == nil && keep(x)
		})
	}
	menus := func(keep func(x content.Menu) bool) []string {
		return a.idsWhere(t, content.RefMenu, func(data []byte) bool {
			var x content.Menu
			return x.UnmarshalJSON(data) == nil && keep(x)
		})
	}
	allTerms := terms(func(content.Term) bool { return true })
	allMenus := menus(func(content.Menu) bool { return true })
	redirects := a.idsWhere(t, content.RefRedirect, everyDocument)
	if len(allTerms) != 5 || len(allMenus) != 4 || len(redirects) < 3 {
		t.Fatalf("the sample site has %d terms, %d menus and %d redirects, want 5, 4 and at least 3", len(allTerms), len(allMenus), len(redirects))
	}

	for _, tt := range []struct {
		path                 string
		want                 []string
		total, offset, limit int
	}{
		{"/api/terms", allTerms, 5, 0, defaultPage},
		{"/api/terms?limit=2", allTerms[:2], 5, 0, 2},
		{"/api/terms?limit=2&offset=1", allTerms[1:3], 5, 1, 2},
		{"/api/terms?offset=4&limit=2", allTerms[4:], 5, 4, 2},
		{"/api/terms?offset=5", nil, 5, 5, defaultPage},
		{"/api/terms?offset=2147483647", nil, 5, 2147483647, defaultPage},
		{"/api/terms?limit=100", allTerms, 5, 0, 100},
		{"/api/terms?limit=1", allTerms[:1], 5, 0, 1},
		{"/api/terms?taxonomy=category", terms(func(x content.Term) bool { return x.Taxonomy == "category" }), 2, 0, defaultPage},
		{"/api/terms?taxonomy=tag", terms(func(x content.Term) bool { return x.Taxonomy == "tag" }), 2, 0, defaultPage},
		{"/api/terms?taxonomy=works_category", terms(func(x content.Term) bool { return x.Taxonomy == "works_category" }), 1, 0, defaultPage},
		{"/api/terms?taxonomy=nothing", nil, 0, 0, defaultPage},
		{"/api/terms?language=ja", terms(func(x content.Term) bool { return x.Language == "ja" }), 4, 0, defaultPage},
		{"/api/terms?language=en", terms(func(x content.Term) bool { return x.Language == "en" }), 1, 0, defaultPage},
		{"/api/terms?language=fr", nil, 0, 0, defaultPage},
		{"/api/terms?taxonomy=tag&language=en", terms(func(x content.Term) bool { return x.Taxonomy == "tag" && x.Language == "en" }), 1, 0, defaultPage},
		{"/api/terms?taxonomy=category&language=en", nil, 0, 0, defaultPage},
		// The total is that after the filters, whatever the page.
		{"/api/terms?taxonomy=category&limit=1", terms(func(x content.Term) bool { return x.Taxonomy == "category" })[:1], 2, 0, 1},
		{"/api/terms?language=ja&limit=2&offset=3", terms(func(x content.Term) bool { return x.Language == "ja" })[3:], 4, 3, 2},
		{"/api/menus", allMenus, 4, 0, defaultPage},
		{"/api/menus?language=ja", menus(func(x content.Menu) bool { return x.Language == "ja" }), 2, 0, defaultPage},
		{"/api/menus?language=en", menus(func(x content.Menu) bool { return x.Language == "en" }), 2, 0, defaultPage},
		{"/api/menus?location=header", menus(func(x content.Menu) bool { return x.Location == "header" }), 2, 0, defaultPage},
		{"/api/menus?location=sidebar", menus(func(x content.Menu) bool { return x.Location == "sidebar" }), 1, 0, defaultPage},
		{"/api/menus?location=header&language=ja", menus(func(x content.Menu) bool { return x.Location == "header" && x.Language == "ja" }), 1, 0, defaultPage},
		{"/api/menus?location=footer&language=en", nil, 0, 0, defaultPage},
		{"/api/authors", []string{yamada}, 1, 0, defaultPage},
		{"/api/authors?offset=1", nil, 1, 1, defaultPage},
		{"/api/forms", a.idsWhere(t, content.RefForm, everyDocument), 1, 0, defaultPage},
		{"/api/redirects", redirects, len(redirects), 0, defaultPage},
		{"/api/redirects?limit=2", redirects[:2], len(redirects), 0, 2},
		{"/api/media", a.idsWhere(t, content.RefMedia, everyDocument), 6, 0, defaultPage},
	} {
		// Every role reads them, and each reads the same.
		for _, l := range apiLogins {
			list := a.listOf(t, l.login, tt.path)
			got := listIDs(t, list)
			if !slices.Equal(got, slices.Concat(tt.want)) || list.Total != tt.total || list.Offset != tt.offset || list.Limit != tt.limit {
				t.Errorf("GET %s as %s: %v of %d at %d, limit %d, want %v of %d at %d, limit %d",
					tt.path, l.login, got, list.Total, list.Offset, list.Limit, tt.want, tt.total, tt.offset, tt.limit)
			}
		}
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestDocumentListBytes shows that a list holds the documents as the store keeps them, with their white space, and in the
// order of their IDs.
func TestDocumentListBytes(t *testing.T) {
	a := newAPIServer(t)
	got := body(t, "GET /api/authors", a.as("viewer").get("/api/authors"), http.StatusOK)
	doc := a.stored(t, content.RefAuthor, yamada)
	want := `{"items":[` + wantFrame(t, doc) + `],"total":1,"offset":0,"limit":50}`
	if got != want {
		t.Errorf("GET /api/authors answers\n%s\nwant\n%s", got, want)
	}
	if !strings.Contains(got, "\n  \"name\": \"山田 太郎\",\n") {
		t.Error("the list has lost the line breaks of the document")
	}

	empty := newEmptySiteServer(t)
	for _, path := range []string{"/api/terms", "/api/authors", "/api/menus", "/api/forms", "/api/redirects", "/api/media"} {
		if got := bodyOf(t, empty.as("admin").get(path)); got != `{"items":[],"total":0,"offset":0,"limit":50}` {
			t.Errorf("GET %s of a site without documents answers %s, want an empty list", path, got)
		}
	}
}

// TestDocumentListQueries shows the queries that a list refuses with 400: unknown names, a name twice, values of another
// form, and numbers out of range.
func TestDocumentListQueries(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("viewer")
	page := []string{
		"?limit=0", "?limit=101", "?limit=abc", "?limit=-1", "?limit=%2B1", "?limit=", "?limit=1&limit=2", "?limit=1.5", "?limit=0x10",
		"?limit=99999999999999999999", "?offset=-1", "?offset=x", "?offset=", "?offset=1&offset=2", "?offset=2147483648", "?offset=%2B1",
		"?foo=1", "?foo", "?%zz", "?offset=1;limit=2", "?Limit=1", "?=1",
	}
	for path, extra := range map[string][]string{
		"/api/terms":     {"?taxonomy=", "?taxonomy=a%20b", "?taxonomy=a/b", "?taxonomy=a&taxonomy=b", "?language=JA", "?language=japanese", "?language=", "?location=header", "?kind=image"},
		"/api/menus":     {"?location=", "?location=a%20b", "?language=x", "?language=ja&language=en", "?taxonomy=category"},
		"/api/authors":   {"?language=ja", "?taxonomy=category"},
		"/api/forms":     {"?language=ja"},
		"/api/redirects": {"?language=ja"},
		"/api/media": {"?kind=gif", "?kind=", "?kind=IMAGE", "?kind=image&kind=pdf", "?uploadedBy=x", "?uploadedBy=", "?uploadedBy=" + newsTerm,
			"?uploadedBy=" + strings.ToLower(importer), "?language=ja"},
	} {
		for _, q := range slices.Concat(page, extra) {
			checkProblem(t, "GET "+path+q, as.get(path+q), "bad-query")
		}
		// HEAD is GET without the body.
		for _, q := range []string{"?limit=0", "?foo=1"} {
			res := as.send(http.MethodHead, path+q, "")
			if res.StatusCode != http.StatusBadRequest {
				t.Errorf("HEAD %s%s has the status %d, want 400", path, q, res.StatusCode)
			}
		}
	}

	// The limits themselves are accepted.
	for _, q := range []string{"?limit=1", "?limit=100", "?offset=0", "?offset=2147483647", "?limit=007&offset=000"} {
		if res := as.get("/api/terms" + q); res.StatusCode != http.StatusOK {
			t.Errorf("GET /api/terms%s has the status %d, want 200", q, res.StatusCode)
		}
	}
	res := as.send(http.MethodHead, "/api/terms", "")
	if got := bodyOf(t, res); res.StatusCode != http.StatusOK || got != "" || res.Header.Get("Content-Length") == "" || res.Header.Get("Content-Length") == "0" {
		t.Errorf("HEAD /api/terms: %d, %d bytes, Content-Length %q, want 200, no body and the length of the list",
			res.StatusCode, len(got), res.Header.Get("Content-Length"))
	}
}

// TestMediaDocuments shows the media: their lists by kind and by who uploaded them, the save of their metadata and the deletion.
func TestMediaDocuments(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("editor")
	author, editor := a.people["author"].account.ID, a.people["editor"].account.ID
	mine := a.addMedium(t, "author", "mine.pdf")
	theirs := a.addMedium(t, "editor", "theirs.pdf")

	media := func(keep func(x content.Media) bool) []string {
		return a.idsWhere(t, content.RefMedia, func(data []byte) bool {
			var x content.Media
			return x.UnmarshalJSON(data) == nil && keep(x)
		})
	}
	if got := len(media(func(content.Media) bool { return true })); got != 8 {
		t.Fatalf("the store has %d media, want the 6 of the sample and 2", got)
	}
	for _, tt := range []struct {
		query string
		want  []string
	}{
		{"", media(func(content.Media) bool { return true })},
		{"?kind=image", media(func(x content.Media) bool { return x.Kind() == content.MediaImage })},
		{"?kind=pdf", media(func(x content.Media) bool { return x.Kind() == content.MediaPDF })},
		{"?uploadedBy=" + importer, media(func(x content.Media) bool { return x.UploadedBy == importer })},
		{"?uploadedBy=" + author, []string{mine.ID}},
		{"?uploadedBy=" + editor, []string{theirs.ID}},
		{"?uploadedBy=" + author + "&kind=pdf", []string{mine.ID}},
		{"?uploadedBy=" + author + "&kind=image", nil},
		{"?uploadedBy=" + importer + "&kind=pdf", []string{samplePDF}},
		{"?uploadedBy=usr_01JD0000000000000000000000", nil},
	} {
		list := a.listOf(t, "viewer", "/api/media"+tt.query)
		if got := listIDs(t, list); !slices.Equal(got, slices.Concat(tt.want)) || list.Total != len(tt.want) {
			t.Errorf("GET /api/media%s: %v of %d, want %v", tt.query, got, list.Total, tt.want)
		}
	}
	if n := len(a.listOf(t, "viewer", "/api/media?kind=image").Items); n != 5 {
		t.Errorf("the sample site has %d images, want 5", n)
	}
	// Paging goes by the total after the filters, and reads only the page.
	list := a.listOf(t, "viewer", "/api/media?kind=pdf&limit=1&offset=1")
	if got := listIDs(t, list); list.Total != 3 || len(got) != 1 || got[0] != mine.ID {
		t.Errorf("the second PDF of three is %v of %d, want %s of 3", got, list.Total, mine.ID)
	}

	// Its metadata is saved as a document is, and what the file and the upload give is not.
	loc := "/api/media/" + theirs.ID
	doc := a.stored(t, content.RefMedia, theirs.ID)
	if got := body(t, "GET "+loc, as.get(loc), http.StatusOK); got != wantFrame(t, doc) {
		t.Errorf("GET %s answers\n%s\nwant\n%s", loc, got, wantFrame(t, doc))
	}
	changed := strings.Replace(string(doc.Data), "theirs.pdf", "ours.pdf", 1)
	saved := result(t, "PUT "+loc, as.put(loc, saveBody(doc.Seq, changed)), http.StatusOK)
	if saved.Kind != content.RefMedia || saved.ID != theirs.ID || saved.Version.Seq <= doc.Seq || !saved.Publishes {
		t.Errorf("the answer of PUT %s is %+v, want a later version that publishes", loc, saved)
	}
	if got := a.stored(t, content.RefMedia, theirs.ID); string(got.Data) != changed {
		t.Errorf("the medium is\n%s\nwant\n%s", got.Data, changed)
	}
	before := a.versions(t)
	now := a.stored(t, content.RefMedia, theirs.ID)
	var m content.Media
	if err := m.UnmarshalJSON(now.Data); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ name, from, to string }{
		{"hash", m.Hash, "sha256:" + strings.Repeat("0", 64)},
		{"uploadedBy", m.UploadedBy, author},
	} {
		if !strings.Contains(string(now.Data), tt.from) {
			t.Fatalf("the medium is %s, which lacks %q", now.Data, tt.from)
		}
		e := firstProblem(t, "a change of "+tt.name, as.put(loc, saveBody(now.Seq, strings.Replace(string(now.Data), tt.from, tt.to, 1))))
		if e.File != "media/"+theirs.ID+".json" || e.Pointer != "/"+tt.name || e.Detail != "set from the file; replace the file to change it" {
			t.Errorf("the error of a change of %s is %+v, want that it is set from the file", tt.name, e)
		}
	}
	if got := a.versions(t); got != before {
		t.Errorf("the changes that were refused have changed the store:\n%s\nwant\n%s", got, before)
	}

	// A medium that is used cannot be deleted; one that is not can.
	used := a.stored(t, content.RefMedia, samplePDF)
	e := firstProblem(t, "a medium that is used", as.del("/api/media/"+samplePDF+"?base="+strconv.FormatInt(used.Seq, 10)))
	if e.File != "media/"+samplePDF+".json" || !strings.Contains(e.Detail, "cannot delete: used by") {
		t.Errorf("the error of the deletion of a medium that is used is %+v", e)
	}
	now = a.stored(t, content.RefMedia, theirs.ID)
	deleted := result(t, "DELETE "+loc, as.del(loc+"?base="+strconv.FormatInt(now.Seq, 10)), http.StatusOK)
	if deleted.Kind != content.RefMedia || deleted.ID != theirs.ID || !a.missing(t, content.RefMedia, theirs.ID) {
		t.Errorf("the answer of DELETE %s is %+v, and the medium is still in the store: %v", loc, deleted, !a.missing(t, content.RefMedia, theirs.ID))
	}
	checkProblem(t, "GET "+loc+" after the deletion", as.get(loc), "not-found")
}

// newEmptySiteServer returns a server whose store has no documents, and whose Publisher does not run. The only account is an
// admin, who is logged in.
func newEmptySiteServer(t *testing.T) *apiServer {
	t.Helper()
	ts := newTestBase()
	db, dir := testStore(t)
	m := testManager(t, db)
	p, out := newTestPublisher(t, ts, db)
	ts.serve(t, Config{Origin: httpsOrigin, Accounts: m, Publisher: p}, db, dir)
	a := &apiServer{testServer: ts, pub: p, out: out, people: make(map[string]*person)}
	acct := ts.addAccount(t, "admin", account.RoleAdmin)
	a.people["admin"] = &person{account: acct, token: ts.logIn(t, "admin")}
	return a
}

// minimalSettings is the settings of a site with one language, which has nothing else to refer to.
const minimalSettings = `{
  "schemaVersion": 1,
  "name": { "ja": "サイト" },
  "url": "https://example.com",
  "timeZone": "Asia/Tokyo",
  "languages": ["ja"],
  "defaultLanguage": "ja"
}`

// TestSettings shows the reading and the saving of the settings document, also when the site has none yet.
func TestSettings(t *testing.T) {
	a := newAPIServer(t)
	admin := a.as("admin")
	doc := a.stored(t, store.SettingsKind, "")

	for _, l := range apiLogins {
		res := a.as(l.login).get("/api/settings")
		if got := body(t, "GET /api/settings as "+l.login, res, http.StatusOK); got != wantFrame(t, doc) {
			t.Errorf("GET /api/settings as %s answers\n%s\nwant\n%s", l.login, got, wantFrame(t, doc))
		}
	}

	changed := spaced(t, strings.Replace(string(doc.Data), "Webサイトの企画と制作", "Webサイトの企画と制作です", 1))
	saved := result(t, "PUT /api/settings", admin.put("/api/settings", saveBody(doc.Seq, changed)), http.StatusOK)
	if saved.Kind != store.SettingsKind || saved.ID != "" || saved.Version.Seq <= doc.Seq || !saved.Publishes {
		t.Errorf("the answer of PUT /api/settings is %+v, want a later version of the settings that publishes", saved)
	}
	now := a.stored(t, store.SettingsKind, "")
	if now.Seq != saved.Version.Seq || string(now.Data) != strings.TrimSpace(changed) {
		t.Errorf("the settings are\n%s\nwant\n%s", now.Data, changed)
	}
	if got := body(t, "GET /api/settings", a.as("viewer").get("/api/settings"), http.StatusOK); got != wantFrame(t, now) {
		t.Errorf("GET /api/settings answers\n%s\nwant\n%s", got, wantFrame(t, now))
	}
	again := result(t, "PUT /api/settings again", admin.put("/api/settings", saveBody(now.Seq, changed)), http.StatusOK)
	if again.Version != saved.Version || again.Publishes {
		t.Errorf("a save of the same bytes answers %+v, want the version %+v and no publish", again, saved.Version)
	}

	// A problem is located in the document that was sent.
	bad := strings.Replace(changed, "Asia/Tokyo", "Nowhere/Land", 1)
	e := firstProblem(t, "a time zone that does not exist", admin.put("/api/settings", linesSave(now.Seq, bad)))
	line, col := locate(t, bad, `"timeZone"`)
	if e.File != "site/settings.json" || e.Pointer != "/timeZone" || e.Line != line || e.Column != col {
		t.Errorf("the error of a time zone is %+v, want site/settings.json, /timeZone and the place %d:%d", e, line, col)
	}
	if got := a.stored(t, store.SettingsKind, ""); got.Seq != now.Seq {
		t.Errorf("the settings that did not validate made the version %d, want %d", got.Seq, now.Seq)
	}

	// A site that has none: reading says so, and the first save is at the version 0.
	empty := newEmptySiteServer(t)
	checkProblem(t, "GET /api/settings of a site without settings", empty.as("admin").get("/api/settings"), "not-found")
	checkProblem(t, "a save of the first settings at a version", empty.as("admin").put("/api/settings", saveBody(1, minimalSettings)), "conflict")
	first := result(t, "the first settings", empty.as("admin").put("/api/settings", saveBody(0, minimalSettings)), http.StatusOK)
	if first.Kind != store.SettingsKind || first.Version.Seq == 0 || !first.Publishes {
		t.Errorf("the answer of the first settings is %+v", first)
	}
	got := empty.stored(t, store.SettingsKind, "")
	if body(t, "GET /api/settings", empty.as("admin").get("/api/settings"), http.StatusOK) != wantFrame(t, got) || string(got.Data) != minimalSettings {
		t.Errorf("the first settings are\n%s\nwant\n%s", got.Data, minimalSettings)
	}
	checkProblem(t, "a second first save", empty.as("admin").put("/api/settings", saveBody(0, minimalSettings)), "conflict")
}
