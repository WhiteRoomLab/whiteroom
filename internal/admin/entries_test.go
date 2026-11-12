package admin

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// IDs of the right form that the sample site and the tests do not use.
const (
	missingEntry    = "ent_01JD0000000000000000000099"
	missingRevision = "rev_01JD0000000000000000000099"
	missingTerm     = "trm_01JD0000000000000000000099"
)

const paragraph = `{"type":"paragraph","id":"b_draft001","content":[{"type":"text","text":"下書きです。"}]}`

// postRevision is the JSON of a revision of a post, with the white space of a person's. extra is members to add before the
// body, each with its comma after it.
func postRevision(title, slug, extra string) string {
	return postRevisionWith(title, slug, extra, paragraph)
}

// postRevisionWith is postRevision with the blocks as its body.
func postRevisionWith(title, slug, extra string, blocks ...string) string {
	return fmt.Sprintf("{\n\t\"schemaVersion\": 1,\n  \"title\": %q,\n \"slug\": %q,\n \"author\": %q,%s\n \"body\": [%s]\n}",
		title, slug, yamada, extra, strings.Join(blocks, ","))
}

// htmlBlock is an html block with the ID, whose html is the paragraph with the text.
func htmlBlock(id, text string) string {
	return `{"type":"html","id":"` + id + `","attrs":{"html":"<p>` + text + `</p>"}}`
}

// textBlock is a paragraph with the ID and the text.
func textBlock(id, text string) string {
	return `{"type":"paragraph","id":"` + id + `","content":[{"type":"text","text":"` + text + `"}]}`
}

// termsOf is the member terms of a revision of a post, with the terms in the category taxonomy.
func termsOf(ids ...string) string {
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = strconv.Quote(id)
	}
	return ` "terms": {"category": [` + strings.Join(quoted, ", ") + `]},`
}

func revisionBody(base, revision string) string {
	return `{"base":` + strconv.Quote(base) + `,"revision":` + revision + `}`
}

// revisionLines wraps the revision of a body in lines of its own, which the places of the problems must not count.
func revisionLines(base, revision string) string {
	return "{\n\n  \"base\": " + strconv.Quote(base) + ",\n  \"revision\":\n" + revision + "\n}\n"
}

// revisionAnswerBody is the JSON of a revision as GET /api/revisions/{id} and the last revision of an entry answer it.
type revisionAnswerBody struct {
	ID       string         `json:"id"`
	Entry    string         `json:"entry"`
	Seq      int64          `json:"seq"`
	Hash     string         `json:"hash"`
	SavedAt  time.Time      `json:"savedAt"`
	SavedBy  string         `json:"savedBy"`
	Document jsontext.Value `json:"document"`
}

// entryAnswer is the answer to the reading of an entry.
type entryAnswer struct {
	Version        versionAnswer       `json:"version"`
	Document       jsontext.Value      `json:"document"`
	LatestRevision *revisionAnswerBody `json:"latestRevision"`
}

// revisionListAnswer is the answer to the list of the revisions of an entry.
type revisionListAnswer struct {
	Items  []revisionAnswer `json:"items"`
	Total  int              `json:"total"`
	Offset int              `json:"offset"`
	Limit  int              `json:"limit"`
}

// revisionIDs returns the IDs of the revisions of the entry in the store, the oldest first.
func (a *apiServer) revisionIDs(t *testing.T, entry string) []string {
	t.Helper()
	ids := []string{}
	err := a.db.View(t.Context(), func(tx *store.Tx) error {
		revs, err := tx.Revisions(entry)
		for _, r := range revs {
			ids = append(ids, r.ID)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// storedRevision returns the revision id as the store has it.
func (a *apiServer) storedRevision(t *testing.T, id string) store.Revision {
	t.Helper()
	var r store.Revision
	err := a.db.View(t.Context(), func(tx *store.Tx) (err error) {
		r, err = tx.Revision(id)
		return err
	})
	if err != nil {
		t.Fatalf("Revision(%q) error = %v", id, err)
	}
	return r
}

// lastRevision returns the ID of the last revision of the entry in the store, "" if it has none.
func (a *apiServer) lastRevision(t *testing.T, entry string) string {
	t.Helper()
	ids := a.revisionIDs(t, entry)
	if len(ids) == 0 {
		return ""
	}
	return ids[len(ids)-1]
}

// newEntry makes an entry of the type and the language through the API, as the account login.
func (a *apiServer) newEntry(t *testing.T, login, typ, language string) resultAnswer {
	t.Helper()
	res := a.as(login).post("/api/entries", fmt.Sprintf(`{"type":%q,"language":%q}`, typ, language))
	return result(t, "POST /api/entries", res, http.StatusCreated)
}

// addRevision saves the revision as the next one of the entry through the API, as the account login.
func (a *apiServer) addRevision(t *testing.T, login, entry, revision string) resultAnswer {
	t.Helper()
	res := a.as(login).post("/api/entries/"+entry+"/revisions", revisionBody(a.lastRevision(t, entry), revision))
	return result(t, "POST /api/entries/"+entry+"/revisions", res, http.StatusCreated)
}

// publishEntry publishes the last revision of the entry, as the account login does through the Editor: the routes of the
// publishing are not these.
func (a *apiServer) publishEntry(t *testing.T, login, entry string) edit.Result {
	t.Helper()
	doc := a.stored(t, content.RefEntry, entry)
	res, err := a.pub.Editor().Publish(t.Context(), edit.Op{By: a.people[login].account.ID}, entry, doc.Seq, a.lastRevision(t, entry))
	if err != nil {
		t.Fatalf("Publish(%s) error = %v", entry, err)
	}
	return res
}

// entryDoc returns the state document of the entry in the store, decoded.
func (a *apiServer) entryDoc(t *testing.T, id string) content.Entry {
	t.Helper()
	var e content.Entry
	if err := e.UnmarshalJSON(a.stored(t, content.RefEntry, id).Data); err != nil {
		t.Fatal(err)
	}
	return e
}

// lastNumber returns the largest number that an entry has had.
func (a *apiServer) lastNumber(t *testing.T) int {
	t.Helper()
	var n int
	err := a.db.View(t.Context(), func(tx *store.Tx) (err error) {
		n, err = tx.LastNumber()
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

var revisionFile = regexp.MustCompile(`^revisions/rev_[0-9A-Z]{26}\.json$`)
var entryFile = regexp.MustCompile(`^entries/ent_[0-9A-Z]{26}\.json$`)

// mine returns the records of the activity log with the action whose target is the entry.
func (a *apiServer) mine(t *testing.T, action audit.Action, entry string) []audit.Record {
	t.Helper()
	var out []audit.Record
	for _, rec := range a.records(t, action) {
		if rec.Target.ID == entry {
			out = append(out, rec)
		}
	}
	return out
}

// TestEntryLifecycle makes an entry, saves revisions of it, reads them, and deletes the entry, with the answers of the
// operations, the bytes that the store keeps, and the records of the activity log.
func TestEntryLifecycle(t *testing.T) {
	a := newAPIServer(t)
	editor := a.people["editor"].account.ID
	as := a.as("editor")
	next := a.lastNumber(t) + 1

	// Make it: 201, where it is, the next number, and nothing to build.
	res := as.post("/api/entries", `{"type":"post","language":"ja"}`)
	location := res.Header.Get("Location")
	created := result(t, "POST /api/entries", res, http.StatusCreated)
	if err := content.CheckID(content.RefEntry, created.ID); err != nil || created.Kind != content.RefEntry || created.Number != next ||
		created.Publishes || created.Version.Seq == 0 || created.Version.SavedBy != editor || created.Revision != "" {
		t.Errorf("the answer of POST /api/entries is %+v, want the new entry with the number %d, saved by the editor, and no publish", created, next)
	}
	if want := "/api/entries/" + created.ID; location != want {
		t.Errorf("POST /api/entries has the Location %q, want %q", location, want)
	}
	doc := a.stored(t, content.RefEntry, created.ID)
	entry := a.entryDoc(t, created.ID)
	if doc.Seq != created.Version.Seq || doc.SavedBy != editor || entry.Number != next || entry.Type != "post" || entry.Language != "ja" ||
		entry.Status != content.StatusDraft || entry.CreatedBy != editor || entry.TranslationGroup == "" {
		t.Errorf("the store has the version %+v of %+v, want a draft post in ja that the editor made", doc.Version, entry)
	}

	// Read it: no revision yet, and the document as the store has it.
	res = as.get(location)
	if got, want := body(t, "GET "+location, res, http.StatusOK), wantFrame(t, doc); got != want {
		t.Errorf("GET %s answers\n%s\nwant\n%s", location, got, want)
	}
	if got := res.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("GET %s has the Content-Type %q", location, got)
	}
	revisions := location + "/revisions"
	if got := body(t, "GET "+revisions, as.get(revisions), http.StatusOK); got != `{"items":[],"total":0,"offset":0,"limit":50}` {
		t.Errorf("GET %s of an entry without revisions answers %s, want an empty list", revisions, got)
	}

	// Save a revision, with the white space of a person's: it stays as it was written.
	first := postRevision("最初", "first", "")
	res = as.post(revisions, revisionLines("", first))
	savedLocation := res.Header.Get("Location")
	saved := result(t, "POST "+revisions, res, http.StatusCreated)
	if err := content.CheckID(content.RefRevision, saved.Revision); err != nil || saved.Kind != content.RefEntry || saved.ID != created.ID ||
		saved.Version != created.Version || saved.Publishes || saved.Number != 0 {
		t.Errorf("the answer of POST %s is %+v, want a new revision of the entry at its version, and no publish", revisions, saved)
	}
	if want := "/api/revisions/" + saved.Revision; savedLocation != want {
		t.Errorf("POST %s has the Location %q, want %q", revisions, savedLocation, want)
	}
	// The post has no date: a warning, located in the revision that was sent.
	if len(saved.Warnings) != 1 || saved.Warnings[0].Pointer != "/date" || saved.Warnings[0].File != "revisions/"+saved.Revision+".json" {
		t.Errorf("the warnings of the save are %+v, want the one about the date", saved.Warnings)
	}
	firstStored := a.storedRevision(t, saved.Revision)
	if string(firstStored.Data) != first || firstStored.Entry != created.ID || firstStored.SavedBy != editor {
		t.Errorf("the store has the revision of the entry %s saved by %s:\n%s\nwant what was sent:\n%s", firstStored.Entry, firstStored.SavedBy, firstStored.Data, first)
	}
	if got := a.stored(t, content.RefEntry, created.ID); got.Seq != doc.Seq {
		t.Errorf("the save of a revision made the version %d of the entry, want %d", got.Seq, doc.Seq)
	}

	// Read it again: the last revision comes with the entry, as the store has it.
	var shown entryAnswer
	if err := json.Unmarshal([]byte(body(t, "GET "+location, as.get(location), http.StatusOK)), &shown); err != nil {
		t.Fatal(err)
	}
	if r := shown.LatestRevision; r == nil || r.ID != saved.Revision || r.Seq != firstStored.Seq || r.Hash != firstStored.Hash ||
		!r.SavedAt.Equal(firstStored.SavedAt) || r.SavedBy != editor || string(r.Document) != first || r.Entry != "" {
		t.Errorf("the last revision in the reading of the entry is %+v, want the one that was saved, without the entry", r)
	}
	if string(shown.Document) != string(doc.Data) || shown.Version != versionOf(doc.Version) {
		t.Errorf("the reading of the entry answers %+v, want its document and version", shown)
	}

	// A second revision is saved on the first, and one on a revision that is not the last is a conflict.
	second := postRevision("二番目", "second", "")
	secondRes := result(t, "the second revision", as.post(revisions, revisionBody(saved.Revision, second)), http.StatusCreated)
	before := a.revisionIDs(t, created.ID)
	checkProblem(t, "a revision on the first", as.post(revisions, revisionBody(saved.Revision, postRevision("三", "third", ""))), "conflict")
	checkProblem(t, "a revision on none", as.post(revisions, revisionBody("", postRevision("三", "third", ""))), "conflict")
	checkProblem(t, "a revision on one that the entry does not have", as.post(revisions, revisionBody(missingRevision, postRevision("三", "third", ""))), "conflict")
	if got := a.revisionIDs(t, created.ID); !slices.Equal(got, before) || len(got) != 2 {
		t.Errorf("the revisions of the entry are %v, want the two that were saved", got)
	}
	if err := json.Unmarshal([]byte(body(t, "GET "+location, as.get(location), http.StatusOK)), &shown); err != nil || shown.LatestRevision == nil || shown.LatestRevision.ID != secondRes.Revision {
		t.Errorf("the last revision of the entry is %+v, want the second", shown.LatestRevision)
	}

	// The list of the revisions: the newest first, without their JSON, and paged.
	var list revisionListAnswer
	listBody := body(t, "GET "+revisions, as.get(revisions), http.StatusOK)
	if err := json.Unmarshal([]byte(listBody), &list); err != nil {
		t.Fatal(err)
	}
	secondStored := a.storedRevision(t, secondRes.Revision)
	want := []revisionAnswer{
		{ID: secondRes.Revision, Seq: secondStored.Seq, Hash: secondStored.Hash, SavedAt: secondStored.SavedAt, SavedBy: editor},
		{ID: saved.Revision, Seq: firstStored.Seq, Hash: firstStored.Hash, SavedAt: firstStored.SavedAt, SavedBy: editor},
	}
	if list.Total != 2 || list.Offset != 0 || list.Limit != defaultPage || len(list.Items) != 2 {
		t.Fatalf("the list of the revisions is %+v, want the two revisions", list)
	}
	for i, item := range list.Items {
		if item.ID != want[i].ID || item.Seq != want[i].Seq || item.Hash != want[i].Hash || !item.SavedAt.Equal(want[i].SavedAt) || item.SavedBy != want[i].SavedBy {
			t.Errorf("item %d of the list of the revisions is %+v, want %+v", i, item, want[i])
		}
	}
	if strings.Contains(listBody, `"document"`) || strings.Contains(listBody, second) {
		t.Errorf("the list of the revisions has the JSON of a revision: %.300s", listBody)
	}
	for _, tt := range []struct {
		query     string
		ids       []string
		offset    int
		limit     int
		wantTotal int
	}{
		{"?limit=1", []string{secondRes.Revision}, 0, 1, 2},
		{"?limit=1&offset=1", []string{saved.Revision}, 1, 1, 2},
		{"?offset=2", nil, 2, defaultPage, 2},
		{"?offset=1&limit=100", []string{saved.Revision}, 1, 100, 2},
	} {
		var page revisionListAnswer
		if err := json.Unmarshal([]byte(body(t, "GET "+revisions+tt.query, as.get(revisions+tt.query), http.StatusOK)), &page); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, item := range page.Items {
			got = append(got, item.ID)
		}
		if !slices.Equal(got, tt.ids) || page.Total != tt.wantTotal || page.Offset != tt.offset || page.Limit != tt.limit {
			t.Errorf("GET %s%s: %v of %d at %d, limit %d, want %v of %d at %d, limit %d",
				revisions, tt.query, got, page.Total, page.Offset, page.Limit, tt.ids, tt.wantTotal, tt.offset, tt.limit)
		}
	}

	// One revision, with the entry it belongs to.
	var one revisionAnswerBody
	if err := json.Unmarshal([]byte(body(t, "GET "+savedLocation, as.get(savedLocation), http.StatusOK)), &one); err != nil {
		t.Fatal(err)
	}
	if one.ID != saved.Revision || one.Entry != created.ID || one.Seq != firstStored.Seq || one.Hash != firstStored.Hash ||
		!one.SavedAt.Equal(firstStored.SavedAt) || one.SavedBy != editor || string(one.Document) != first {
		t.Errorf("GET %s answers %+v, want the first revision as the store has it, with the entry", savedLocation, one)
	}

	// Delete the draft at its version: a conflict at any other.
	deletion := location + "?base="
	checkProblem(t, "a deletion at an old version", as.del(deletion+strconv.FormatInt(doc.Seq-1, 10)), "conflict")
	deleted := result(t, "DELETE "+location, as.del(deletion+strconv.FormatInt(doc.Seq, 10)), http.StatusOK)
	if deleted.Kind != content.RefEntry || deleted.ID != created.ID || deleted.Version.Seq <= doc.Seq || deleted.Version.Hash != "" || deleted.Publishes {
		t.Errorf("the answer of DELETE %s is %+v, want a later version without a hash, and no publish", location, deleted)
	}
	if !a.missing(t, content.RefEntry, created.ID) {
		t.Error("the store still has the entry that was deleted")
	}
	checkProblem(t, "GET "+location+" after the deletion", as.get(location), "not-found")

	// Its revisions stay, and can be read: by the list, and one by one.
	if err := json.Unmarshal([]byte(body(t, "GET "+revisions+" after the deletion", as.get(revisions), http.StatusOK)), &list); err != nil || list.Total != 2 {
		t.Errorf("the list of the revisions of the entry that was deleted is %+v (%v), want its two revisions", list, err)
	}
	body(t, "GET "+savedLocation+" after the deletion", as.get(savedLocation), http.StatusOK)

	// The records say who did what, once for each operation that succeeded.
	for action, want := range map[audit.Action]int{audit.EntryCreate: 1, audit.EntrySaveRevision: 2, audit.EntryDelete: 1} {
		mine := a.mine(t, action, created.ID)
		if len(mine) != want || mine[0].Actor != editor || mine[0].Target.Kind != "entry" {
			t.Errorf("%d records of %s for the entry, the first %+v, want %d by the editor", len(mine), action, mine, want)
		}
	}
	if r := a.mine(t, audit.EntrySaveRevision, created.ID)[0]; r.Revision != saved.Revision {
		t.Errorf("the record of the first revision is %+v, want that it names the revision %s", r, saved.Revision)
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestEntryProblems shows that the problems of the documents that an entry and its revisions are made of are answered with
// 422, located in the bytes that were sent, and that they change nothing.
func TestEntryProblems(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("editor")
	entry := a.newEntry(t, "editor", "post", "ja")
	revisions := "/api/entries/" + entry.ID + "/revisions"
	before := a.versions(t)
	revs := a.revisionIDs(t, entry.ID)

	// A member of a revision that its type does not take: the place is in the revision, and not in the body that carried it.
	wrongType := postRevision("題", "slug", ` "template": "wide",`)
	e := firstProblem(t, "a template of a post", as.post(revisions, revisionLines("", wrongType)))
	line, col := locate(t, wrongType, `"template"`)
	if !revisionFile.MatchString(e.File) || e.Pointer != "/template" || e.Detail != "not allowed for post" || e.Line != line || e.Column != col {
		t.Errorf("the error of a template of a post is %+v, want a new revision's file, /template and the place %d:%d of the template in the revision", e, line, col)
	}
	wrongValue := postRevision("題", "slug", ` "sticky": "yes",`)
	e = firstProblem(t, "a sticky that is a string", as.post(revisions, revisionLines("", wrongValue)))
	line, col = locate(t, wrongValue, `"sticky"`)
	if !revisionFile.MatchString(e.File) || e.Pointer != "/sticky" || e.Line != line || e.Column != col || e.Detail != "expected boolean, got string" {
		t.Errorf("the error of a sticky that is a string is %+v, want /sticky at %d:%d", e, line, col)
	}
	// A revision that is not JSON, or that names a member twice, is not a revision with problems: it is refused as a body.
	checkProblem(t, "a revision that is not JSON", as.post(revisions, "{\"base\":\"\",\"revision\":{\n  oops\n}}"), "bad-request")
	checkProblem(t, "a revision with a member twice", as.post(revisions, revisionBody("", strings.Replace(postRevision("題", "slug", ""), `"slug": "slug",`, `"slug": "a", "slug": "b",`, 1))), "bad-request")

	// An entry of a type or a language that the site does not have: the problem is in the document that would have been made.
	for _, tt := range []struct {
		name, body, pointer, detail string
	}{
		{"an unknown type", `{"type":"nothing","language":"ja"}`, "/type", `unknown type "nothing"`},
		{"an empty type", `{"type":"","language":"ja"}`, "/type", "missing type"},
		{"a language that the site lacks", `{"type":"post","language":"fr"}`, "/language", `language "fr" is not in the site settings (use ja, en)`},
	} {
		e := firstProblem(t, tt.name, as.post("/api/entries", tt.body))
		if !entryFile.MatchString(e.File) || e.Pointer != tt.pointer || e.Detail != tt.detail {
			t.Errorf("the error of %s is %+v, want an entry's file, %s and %q", tt.name, e, tt.pointer, tt.detail)
		}
	}

	// A published or scheduled entry is not deleted, and says why: its status.
	published := a.newEntry(t, "editor", "post", "ja")
	a.addRevision(t, "editor", published.ID, postRevision("公開", "published-entry", ""))
	pub := a.publishEntry(t, "editor", published.ID)
	e = firstProblem(t, "a published entry", as.del("/api/entries/"+published.ID+"?base="+strconv.FormatInt(pub.Version.Seq, 10)))
	if e.File != "entries/"+published.ID+".json" || e.Pointer != "/status" || e.Detail != "cannot delete a published or scheduled entry; unpublish it first" {
		t.Errorf("the error of the deletion of a published entry is %+v, want the problem of its status", e)
	}
	scheduled := a.newEntry(t, "editor", "post", "ja")
	a.addRevision(t, "editor", scheduled.ID, postRevision("予約", "scheduled-entry", ""))
	doc := a.stored(t, content.RefEntry, scheduled.ID)
	plan, err := a.pub.Editor().Schedule(t.Context(), edit.Op{By: a.people["editor"].account.ID}, scheduled.ID, doc.Seq,
		a.lastRevision(t, scheduled.ID), a.clock.now().Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	e = firstProblem(t, "a scheduled entry", as.del("/api/entries/"+scheduled.ID+"?base="+strconv.FormatInt(plan.Version.Seq, 10)))
	if e.File != "entries/"+scheduled.ID+".json" || e.Pointer != "/status" || !strings.Contains(e.Detail, "unpublish it first") {
		t.Errorf("the error of the deletion of a scheduled entry is %+v, want the problem of its status", e)
	}
	if _, err := a.pub.Editor().Unpublish(t.Context(), edit.Op{By: a.people["editor"].account.ID}, published.ID, pub.Version.Seq); err != nil {
		t.Fatal(err)
	}
	now := a.stored(t, content.RefEntry, published.ID)
	result(t, "DELETE an entry that is unpublished", as.del("/api/entries/"+published.ID+"?base="+strconv.FormatInt(now.Seq, 10)), http.StatusOK)

	if got := a.revisionIDs(t, entry.ID); !slices.Equal(got, revs) {
		t.Errorf("the revisions of the entry are %v, want those it had: the revisions with problems were not saved", got)
	}
	// The entries that were refused were not made: only what these tests made, in all.
	if got := len(a.idsWhere(t, content.RefEntry, everyDocument)); got != 34+3-1 {
		t.Errorf("the store has %d entries, want the 34 of the sample and the 3 that were made, less the 1 deleted", got)
	}
	if a.versions(t) == before {
		t.Error("the entries that the test made are not in the store")
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestEntryTranslations makes the translation of an entry, and shows the problems that it has.
func TestEntryTranslations(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("editor")
	editor := a.people["editor"].account.ID
	source := a.newEntry(t, "editor", "post", "ja")
	empty := a.newEntry(t, "editor", "post", "ja")
	a.addRevision(t, "editor", source.ID, postRevision("原文", "original", termsOf(newsTerm)))
	translations := "/api/entries/" + source.ID + "/translations"
	origin := a.entryDoc(t, source.ID)
	next := a.lastNumber(t) + 1

	res := as.post(translations, `{"language":"en"}`)
	location := res.Header.Get("Location")
	made := result(t, "POST "+translations, res, http.StatusCreated)
	if err := content.CheckID(content.RefEntry, made.ID); err != nil || made.ID == source.ID || made.Kind != content.RefEntry || made.Number != next ||
		content.CheckID(content.RefRevision, made.Revision) != nil || made.Version.SavedBy != editor || made.Publishes {
		t.Errorf("the answer of POST %s is %+v, want a new entry with the number %d and a revision", translations, made, next)
	}
	if want := "/api/entries/" + made.ID; location != want {
		t.Errorf("POST %s has the Location %q, want %q", translations, location, want)
	}
	translated := a.entryDoc(t, made.ID)
	if translated.Language != "en" || translated.TranslationGroup != origin.TranslationGroup || translated.Type != "post" ||
		translated.Status != content.StatusDraft || translated.CreatedBy != editor {
		t.Errorf("the translation is %+v, want a draft post in en that is in the translation group of %+v", translated, origin)
	}
	var shown entryAnswer
	if err := json.Unmarshal([]byte(body(t, "GET "+location, as.get(location), http.StatusOK)), &shown); err != nil || shown.LatestRevision == nil ||
		shown.LatestRevision.ID != made.Revision || !strings.Contains(string(shown.LatestRevision.Document), "原文") {
		t.Errorf("the translation has the last revision %+v (%v), want the copy of the original that the answer names", shown.LatestRevision, err)
	}
	// The original's term is in ja, and the copy has none in en, which is a warning, in the file of the copy.
	if len(made.Warnings) == 0 {
		t.Error("the translation has no warning, want that a term has no translation")
	}
	for _, w := range made.Warnings {
		if w.File != "revisions/"+made.Revision+".json" {
			t.Errorf("the warning %+v is not in the file of the revision that was made", w)
		}
	}
	if got := a.mine(t, audit.EntryCreateTranslation, made.ID); len(got) != 1 || got[0].Actor != editor || got[0].Revision != made.Revision {
		t.Errorf("the records of entry.createTranslation are %+v, want one by the editor with the revision", got)
	}

	// The problems are those of the document that would have been made.
	for _, tt := range []struct {
		name, path, body, pointer, detail string
	}{
		{"the language of the original", translations, `{"language":"ja"}`, "/language", `language "ja" is that of the entry it translates`},
		{"a language that the site lacks", translations, `{"language":"fr"}`, "/language", `language "fr" is not in the site settings (use ja, en)`},
		{"a language that has a translation", translations, `{"language":"en"}`, "/translationGroup", "already has an entry in the language"},
		{"an entry without a revision", "/api/entries/" + empty.ID + "/translations", `{"language":"en"}`, "", "has no revision to translate"},
	} {
		e := firstProblem(t, tt.name, as.post(tt.path, tt.body))
		if !entryFile.MatchString(e.File) && e.File != "entries/"+empty.ID+".json" || e.Pointer != tt.pointer || !strings.Contains(e.Detail, tt.detail) {
			t.Errorf("the error of %s is %+v, want an entry's file, %q and %q", tt.name, e, tt.pointer, tt.detail)
		}
	}
	checkProblem(t, "the translation of an entry that does not exist", as.post("/api/entries/"+missingEntry+"/translations", `{"language":"en"}`), "not-found")
	if got := a.lastNumber(t); got != next {
		t.Errorf("the last number is %d, want %d: the translations that failed made no entry", got, next)
	}
}

// TestEntryShapes shows what a request must look like: the body, the query, the header, the path and the method.
func TestEntryShapes(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("editor")
	entry := a.newEntry(t, "editor", "post", "ja")
	loc := "/api/entries/" + entry.ID
	a.addRevision(t, "editor", entry.ID, postRevision("題", "slug", ""))
	last := a.lastRevision(t, entry.ID)
	revision := postRevision("次", "next", "")
	before := a.versions(t)
	revs := a.revisionIDs(t, entry.ID)

	// What is wrong in the JSON of the body is 400, and nothing is made.
	for _, tt := range []struct {
		name, body string
	}{
		{"no body", ``},
		{"an array", `[]`},
		{"no members", `{}`},
		{"no language", `{"type":"post"}`},
		{"no type", `{"language":"ja"}`},
		{"a type that is null", `{"type":null,"language":"ja"}`},
		{"a language that is a number", `{"type":"post","language":5}`},
		{"a member that the body does not have", `{"type":"post","language":"ja","extra":1}`},
		{"a member twice", `{"type":"post","type":"page","language":"ja"}`},
		{"JSON that stops", `{"type":"post",`},
		{"text after the JSON", `{"type":"post","language":"ja"} {}`},
	} {
		checkProblem(t, "POST /api/entries with "+tt.name, as.post("/api/entries", tt.body, withBody(tt.body)...), "bad-request")
	}
	for _, tt := range []struct {
		name, body string
	}{
		{"no body", ``},
		{"no members", `{}`},
		{"a language that is null", `{"language":null}`},
		{"a type", `{"language":"en","type":"post"}`},
		{"a member twice", `{"language":"en","language":"en"}`},
	} {
		checkProblem(t, "a translation with "+tt.name, as.post(loc+"/translations", tt.body, withBody(tt.body)...), "bad-request")
	}
	for _, tt := range []struct {
		name, body string
	}{
		{"no body", ``},
		{"no base", `{"revision":` + revision + `}`},
		{"a base that is null", `{"base":null,"revision":` + revision + `}`},
		{"a base that is a number", `{"base":1,"revision":` + revision + `}`},
		{"a base that is not the ID of a revision", `{"base":"x","revision":` + revision + `}`},
		{"a base that is the ID of an entry", `{"base":"` + entry.ID + `","revision":` + revision + `}`},
		{"no revision", `{"base":"` + last + `"}`},
		{"a revision that is null", `{"base":"` + last + `","revision":null}`},
		{"a revision that is an array", `{"base":"` + last + `","revision":[]}`},
		{"a revision that is a string", `{"base":"` + last + `","revision":"x"}`},
		{"a document in place of the revision", `{"base":"` + last + `","document":` + revision + `}`},
		{"a member that the body does not have", `{"base":"` + last + `","revision":` + revision + `,"extra":1}`},
		{"a member twice", `{"base":"` + last + `","base":"` + last + `","revision":` + revision + `}`},
		{"JSON that stops", `{"base":"` + last + `","revision":{`},
		{"text after the JSON", revisionBody(last, revision) + ` {}`},
	} {
		checkProblem(t, "a revision with "+tt.name, as.post(loc+"/revisions", tt.body, withBody(tt.body)...), "bad-request")
	}

	// The media type and the size of the body.
	checkProblem(t, "a body that is text", as.post("/api/entries", `{"type":"post","language":"ja"}`, "Content-Type", "text/plain"), "unsupported-media-type")
	checkProblem(t, "a translation that is text", as.post(loc+"/translations", `{"language":"en"}`, "Content-Type", "text/plain"), "unsupported-media-type")
	checkProblem(t, "a revision that is text", as.post(loc+"/revisions", revisionBody(last, revision), "Content-Type", "text/plain"), "unsupported-media-type")
	checkProblem(t, "a body of more than 64 KiB", as.post("/api/entries", `{"type":"post","language":"`+strings.Repeat("a", smallBodyLimit)+`"}`), "too-large")
	checkProblem(t, "a translation of more than 64 KiB", as.post(loc+"/translations", `{"language":"`+strings.Repeat("a", smallBodyLimit)+`"}`), "too-large")

	// The query: none for a creation, a save or a translation; the deletion needs base once, as the Seq of a version.
	for _, q := range []string{"?x=1", "?base=1", "?offset=0", "?%zz"} {
		checkProblem(t, "POST /api/entries"+q, as.post("/api/entries"+q, `{"type":"post","language":"ja"}`), "bad-query")
		checkProblem(t, "a save"+q, as.post(loc+"/revisions"+q, revisionBody(last, revision)), "bad-query")
		checkProblem(t, "a translation"+q, as.post(loc+"/translations"+q, `{"language":"en"}`), "bad-query")
		checkProblem(t, "GET"+q, as.get(loc+q), "bad-query")
		checkProblem(t, "GET a revision"+q, as.get("/api/revisions/"+last+q), "bad-query")
	}
	for _, q := range []string{"", "?base=", "?base=abc", "?base=-1", "?base=%2B1", "?base=1.5", "?base=1&base=2", "?base=1&x=2", "?x=2", "?base=99999999999999999999"} {
		checkProblem(t, "DELETE "+q, as.del(loc+q), "bad-query")
	}
	for _, q := range []string{"?x=1", "?base=1", "?limit=0", "?limit=101", "?offset=-1", "?limit=1&limit=2", "?%zz"} {
		checkProblem(t, "GET the revisions"+q, as.get(loc+"/revisions"+q), "bad-query")
	}

	// The header Idempotency-Key is an operation ID, once, on the routes that take one, and on none that only read.
	for _, h := range [][]string{
		{"Idempotency-Key", "op_01JD0000000000000000000001"},
		{"Idempotency-Key", `"op_x"`},
		{"Idempotency-Key", `""`},
		{"Idempotency-Key", `"` + someOp + `"`, "Idempotency-Key", `"` + someOp + `"`},
	} {
		what := strings.Join(h, " ")
		checkProblem(t, "POST /api/entries with the header "+what, as.post("/api/entries", `{"type":"post","language":"ja"}`, h...), "bad-header")
		checkProblem(t, "a save with the header "+what, as.post(loc+"/revisions", revisionBody(last, revision), h...), "bad-header")
		checkProblem(t, "a translation with the header "+what, as.post(loc+"/translations", `{"language":"en"}`, h...), "bad-header")
		checkProblem(t, "DELETE with the header "+what, as.del(loc+"?base=1", h...), "bad-header")
	}
	for _, path := range []string{"/api/entries", loc, loc + "/revisions", "/api/revisions/" + last} {
		checkProblem(t, "GET "+path+" with an operation ID", as.send(http.MethodGet, path, "", key(1)...), "bad-header")
	}

	// The path: an ID that is not the ID of an entry, or of a revision, names nothing.
	for _, id := range []string{"x", "ent_x", last, newsTerm, strings.ToLower(entry.ID), entry.ID + "0", "%20", "..%2f"} {
		path := "/api/entries/" + id
		checkProblem(t, "GET "+path, as.get(path), "not-found")
		checkProblem(t, "GET "+path+"/revisions", as.get(path+"/revisions"), "not-found")
		checkProblem(t, "a save in "+path, as.post(path+"/revisions", revisionBody("", revision)), "not-found")
		checkProblem(t, "a translation of "+path, as.post(path+"/translations", `{"language":"en"}`), "not-found")
		checkProblem(t, "DELETE "+path, as.del(path+"?base=1"), "not-found")
	}
	for _, id := range []string{"x", "rev_x", entry.ID, strings.ToLower(last), last + "0"} {
		checkProblem(t, "GET /api/revisions/"+id, as.get("/api/revisions/"+id), "not-found")
	}
	// An ID of the right form that nothing has: a deletion or a reading of it finds nothing.
	checkProblem(t, "GET an entry that does not exist", as.get("/api/entries/"+missingEntry), "not-found")
	checkProblem(t, "GET the revisions of an entry that does not exist", as.get("/api/entries/"+missingEntry+"/revisions"), "not-found")
	checkProblem(t, "a save in an entry that does not exist", as.post("/api/entries/"+missingEntry+"/revisions", revisionBody("", revision)), "not-found")
	checkProblem(t, "DELETE an entry that does not exist", as.del("/api/entries/"+missingEntry+"?base=1"), "not-found")
	checkProblem(t, "GET a revision that does not exist", as.get("/api/revisions/"+missingRevision), "not-found")
	checkProblem(t, "GET a kind that does not exist", as.get("/api/entry"), "not-found")

	// A method that the resource does not take.
	for _, tt := range []struct {
		method, path, allow string
	}{
		{http.MethodPut, "/api/entries", "GET, HEAD, POST"},
		{http.MethodDelete, "/api/entries", "GET, HEAD, POST"},
		{http.MethodPut, loc, "DELETE, GET, HEAD"},
		{http.MethodPost, loc, "DELETE, GET, HEAD"},
		{http.MethodPut, loc + "/revisions", "GET, HEAD, POST"},
		{http.MethodDelete, loc + "/revisions", "GET, HEAD, POST"},
		{http.MethodGet, loc + "/translations", "POST"},
		{http.MethodPost, "/api/revisions/" + last, "GET, HEAD"},
		{http.MethodDelete, "/api/revisions/" + last, "GET, HEAD"},
		{http.MethodPut, "/api/revisions/" + last, "GET, HEAD"},
	} {
		res := as.send(tt.method, tt.path, "")
		if got := res.Header.Get("Allow"); got != tt.allow {
			t.Errorf("%s %s has the Allow %q, want %q", tt.method, tt.path, got, tt.allow)
		}
		checkProblem(t, tt.method+" "+tt.path, res, "method-not-allowed")
	}

	if got := a.versions(t); got != before {
		t.Errorf("the requests that were refused have changed the store:\n%s\nwant\n%s", got, before)
	}
	if got := a.revisionIDs(t, entry.ID); !slices.Equal(got, revs) {
		t.Errorf("the revisions of the entry are %v, want those it had", got)
	}
	if got := a.records(t, audit.AccessDenied); len(got) != 0 {
		t.Errorf("the requests that were refused as requests made %d records of access.denied, want none", len(got))
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}
