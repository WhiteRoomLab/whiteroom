package admin

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// The documents that the tests make, in the layout of the files of the sample site, without an id.
func termDoc(taxonomy, language, name, slug string) string {
	return fmt.Sprintf("{\n  \"schemaVersion\": 1,\n  \"taxonomy\": %q,\n  \"language\": %q,\n  \"name\": %q,\n  \"slug\": %q\n}",
		taxonomy, language, name, slug)
}

func authorDoc(name, slug string) string {
	return fmt.Sprintf("{\n  \"schemaVersion\": 1,\n  \"name\": %q,\n  \"slug\": %q,\n  \"links\": [\"https://example.com/%s\"]\n}",
		name, slug, slug)
}

func menuDoc(title string) string {
	return fmt.Sprintf("{\n  \"schemaVersion\": 1,\n  \"title\": %q,\n  \"language\": \"ja\",\n  \"items\": [{ \"label\": \"ホーム\", \"href\": \"/\" }]\n}", title)
}

func redirectDoc(from, to string) string {
	return fmt.Sprintf("{\n  \"schemaVersion\": 1,\n  \"from\": %q,\n  \"to\": %q,\n  \"status\": 301\n}", from, to)
}

const formDoc = `{
  "schemaVersion": 1,
  "title": "資料請求",
  "fields": [
    { "name": "your-email", "type": "email", "label": { "en": "Email", "ja": "メール" }, "required": true }
  ],
  "submitLabel": { "en": "Send", "ja": "送信" },
  "message": { "en": "Thank you.", "ja": "ありがとうございます。" },
  "notify": ["info@example.com"]
}`

// Documents of the sample site, and the file of its PDF.
const (
	newsTerm   = "trm_01JBA4C7E2G5J8K1M4P7R2T5W8" // category, ja; menus use it
	yamada     = "aut_01JBA4C7N9Q3T6W9Z3C6F9H2K5" // the only author profile
	headerMenu = "mnu_01JBD2A3B4C5D6E7F8G9H0J1K2"
	sampleForm = "frm_01JB8Z3K5VP1D4G7J2M5Q8T3W6"
	sampleRule = "rdr_01JBD3A4B5C6D7E8F9G0H1J2K3"
	samplePDF  = "med_01JB8Z3K5TM8A1C5F9V3J6K2N7"
	sampleLogo = "med_01JBG3N6V3X6Z9C2E5G8J1K4M7"
	missingID  = "trm_01JBA4C7E2G5J8K1M4P7R2T5ZZ" // the form of the ID of a term, and none that the store has
	missingMed = "med_01JB8Z3K5TM8A1C5F9V3J6K2N8"
	pdfOfSite  = "../build/testdata/site/files/3702c7f0b8c94dc18a33e687db167c2f210c2816727b19da83ea6019d953ee1e"
)

func (p asPerson) put(path, body string, headers ...string) *http.Response {
	return p.send(http.MethodPut, path, body, headers...)
}

func (p asPerson) del(path string, headers ...string) *http.Response {
	return p.send(http.MethodDelete, path, "", headers...)
}

// withBody returns the headers of a request whose body is body: JSON, also when the body is empty.
func withBody(body string, headers ...string) []string {
	if body == "" {
		return append([]string{"Content-Type", "application/json"}, headers...)
	}
	return headers
}

// key returns the lines of the header Idempotency-Key for the n-th operation of a test.
func key(n int) []string {
	return []string{"Idempotency-Key", fmt.Sprintf(`"op_01JD0000000000000000%06d"`, n)}
}

func createBody(document string) string { return `{"document":` + document + `}` }

func saveBody(base int64, document string) string {
	return fmt.Sprintf(`{"base":%d,"document":%s}`, base, document)
}

// docAnswer is the answer to the reading of a document, whose Document is the JSON that the store keeps.
type docAnswer struct {
	Version  versionAnswer  `json:"version"`
	Document jsontext.Value `json:"document"`
}

type listAnswer struct {
	Items  []docAnswer `json:"items"`
	Total  int         `json:"total"`
	Offset int         `json:"offset"`
	Limit  int         `json:"limit"`
}

// spaced returns doc, a JSON object, with the white space of a document that a person has written by hand.
func spaced(t *testing.T, doc string) string {
	t.Helper()
	v := jsontext.Value(doc)
	if err := v.Indent(jsontext.WithIndentPrefix("  "), jsontext.WithIndent("\t")); err != nil {
		t.Fatal(err)
	}
	return string(v)
}

// locate returns the line and the column, both from 1 and the column in characters, of the first needle in text.
func locate(t *testing.T, text, needle string) (line, col int) {
	t.Helper()
	i := strings.Index(text, needle)
	if i < 0 {
		t.Fatalf("%q does not occur in %q", needle, text)
	}
	start := strings.LastIndex(text[:i], "\n") + 1
	return 1 + strings.Count(text[:i], "\n"), 1 + utf8.RuneCountInString(text[start:i])
}

// stored returns the document of kind with id as the store has it.
func (a *apiServer) stored(t *testing.T, kind content.RefKind, id string) store.Document {
	t.Helper()
	var d store.Document
	err := a.db.View(t.Context(), func(tx *store.Tx) (err error) {
		d, err = tx.Document(kind, id)
		return err
	})
	if err != nil {
		t.Fatalf("Document(%s, %q) error = %v", kind, id, err)
	}
	return d
}

// missing reports whether the store has no document of kind with id.
func (a *apiServer) missing(t *testing.T, kind content.RefKind, id string) bool {
	t.Helper()
	err := a.db.View(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Document(kind, id)
		return err
	})
	return errors.Is(err, fs.ErrNotExist)
}

// versions returns the version of every document of every kind, one per line, to show that a request has changed none.
func (a *apiServer) versions(t *testing.T) string {
	t.Helper()
	var lines []string
	err := a.db.View(t.Context(), func(tx *store.Tx) error {
		for _, kind := range []content.RefKind{
			content.RefEntry, content.RefTerm, content.RefMedia, content.RefAuthor, content.RefMenu, content.RefForm,
			content.RefRedirect, store.SettingsKind,
		} {
			docs, err := tx.Documents(kind)
			if err != nil {
				return err
			}
			for _, d := range docs {
				lines = append(lines, fmt.Sprintf("%s/%s@%d", kind, d.ID, d.Seq))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}

// wantFrame is the answer to the reading of d: its version, and its JSON as the store has it, with its white space.
func wantFrame(t *testing.T, d store.Document) string {
	t.Helper()
	v, err := json.Marshal(versionOf(d.Version))
	if err != nil {
		t.Fatal(err)
	}
	return `{"version":` + string(v) + `,"document":` + string(d.Data) + `}`
}

// body returns the body of res, which must have the status.
func body(t *testing.T, what string, res *http.Response, status int) string {
	t.Helper()
	b := bodyOf(t, res)
	if res.StatusCode != status {
		t.Fatalf("%s has the status %d, want %d: %.400s", what, res.StatusCode, status, b)
	}
	return b
}

// result reads the answer of an operation, which must have the status.
func result(t *testing.T, what string, res *http.Response, status int) resultAnswer {
	t.Helper()
	var r resultAnswer
	b := body(t, what, res, status)
	if err := json.Unmarshal([]byte(b), &r); err != nil {
		t.Fatalf("%s: the answer %s is not the answer of an operation: %v", what, b, err)
	}
	return r
}

// listIDs returns the IDs of the documents of a list answer.
func listIDs(t *testing.T, l listAnswer) []string {
	t.Helper()
	ids := make([]string, 0, len(l.Items))
	for _, item := range l.Items {
		var d struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(item.Document, &d); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, d.ID)
	}
	return ids
}

// idsWhere returns the IDs of the documents of kind in the store for which keep, given their JSON, is true, in order.
func (a *apiServer) idsWhere(t *testing.T, kind content.RefKind, keep func(data []byte) bool) []string {
	t.Helper()
	ids := []string{}
	err := a.db.View(t.Context(), func(tx *store.Tx) error {
		docs, err := tx.Documents(kind)
		if err != nil {
			return err
		}
		for _, d := range docs {
			if keep(d.Data) {
				ids = append(ids, d.ID)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// addMedium adds a medium of the PDF of the sample site, as the account login uploads it, and returns the result.
func (a *apiServer) addMedium(t *testing.T, login, fileName string) edit.Result {
	t.Helper()
	data, err := os.ReadFile(filepath.FromSlash(pdfOfSite))
	if err != nil {
		t.Fatal(err)
	}
	op := edit.Op{By: a.people[login].account.ID}
	res, err := a.pub.Editor().AddMedia(t.Context(), op, bytes.NewReader(data), fileName, "application/pdf")
	if err != nil {
		t.Fatalf("AddMedia(%q) error = %v", fileName, err)
	}
	return res
}

// lifecycles are the kinds that POST makes, with a document of each, and the change that the tests make to the one that
// the store has: from is replaced by to.
var lifecycles = []struct {
	path     string
	kind     content.RefKind
	create   string
	from, to string
	actions  [3]audit.Action // of the creation, the save and the deletion
}{
	{"terms", content.RefTerm, termDoc("category", "ja", "ニュースレター", "newsletter"), "ニュースレター", "ニュースレター2",
		[3]audit.Action{audit.TermCreate, audit.TermSave, audit.TermDelete}},
	{"authors", content.RefAuthor, authorDoc("佐藤 花子", "sato"), "佐藤 花子", "佐藤 次郎",
		[3]audit.Action{audit.AuthorCreate, audit.AuthorSave, audit.AuthorDelete}},
	{"menus", content.RefMenu, menuDoc("サイドバー"), "サイドバー", "サイドバー2",
		[3]audit.Action{audit.MenuCreate, audit.MenuSave, audit.MenuDelete}},
	{"forms", content.RefForm, formDoc, "資料請求", "資料請求フォーム",
		[3]audit.Action{audit.FormCreate, audit.FormSave, audit.FormDelete}},
	{"redirects", content.RefRedirect, redirectDoc("/old-contact/", "/contact/"), "/old-contact/", "/old-contact-2/",
		[3]audit.Action{audit.RedirectCreate, audit.RedirectSave, audit.RedirectDelete}},
}

// TestDocumentLifecycle makes, reads, saves and deletes a document of each kind, with the answers of the operations, the
// bytes that the store keeps, and the records of the activity log.
func TestDocumentLifecycle(t *testing.T) {
	a := newAPIServer(t)
	editor := a.people["editor"].account.ID
	for _, tt := range lifecycles {
		t.Run(tt.path, func(t *testing.T) {
			as := a.as("editor")
			collection := "/api/" + tt.path

			// Make it: 201, where it is, and what the operation did.
			res := as.post(collection, createBody(tt.create))
			location := res.Header.Get("Location")
			created := result(t, "POST "+collection, res, http.StatusCreated)
			if err := content.CheckID(tt.kind, created.ID); err != nil || created.Kind != tt.kind || !created.Publishes ||
				created.Version.Seq == 0 || created.Version.SavedBy != editor || created.Version.Hash == "" {
				t.Errorf("the answer of POST %s is %+v, want the new document of the kind %s, saved by the editor", collection, created, tt.kind)
			}
			if want := collection + "/" + created.ID; location != want {
				t.Errorf("POST %s has the Location %q, want %q", collection, location, want)
			}
			doc := a.stored(t, tt.kind, created.ID)
			if doc.Seq != created.Version.Seq || doc.Hash != created.Version.Hash || doc.SavedBy != editor {
				t.Errorf("the store has the version %+v, the answer says %+v", doc.Version, created.Version)
			}
			if !strings.Contains(string(doc.Data), tt.from) {
				t.Fatalf("the stored document is %s, which lacks %q", doc.Data, tt.from)
			}

			// Read it: the answer holds the bytes of the store as they are.
			res = as.get(location)
			if got, want := body(t, "GET "+location, res, http.StatusOK), wantFrame(t, doc); got != want {
				t.Errorf("GET %s answers\n%s\nwant\n%s", location, got, want)
			}
			if got := res.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("GET %s has the Content-Type %q", location, got)
			}

			// Save it with a document that has the white space of a person's: it stays as it was written.
			changed := spaced(t, strings.Replace(string(doc.Data), tt.from, tt.to, 1))
			res = as.put(location, saveBody(doc.Seq, changed))
			saved := result(t, "PUT "+location, res, http.StatusOK)
			if saved.Kind != tt.kind || saved.ID != created.ID || saved.Version.Seq <= doc.Seq || !saved.Publishes {
				t.Errorf("the answer of PUT %s is %+v, want a later version of the document that publishes", location, saved)
			}
			doc = a.stored(t, tt.kind, created.ID)
			if doc.Seq != saved.Version.Seq || string(doc.Data) != changed {
				t.Errorf("the store has the version %d and\n%s\nwant %d and\n%s", doc.Seq, doc.Data, saved.Version.Seq, changed)
			}
			if got, want := body(t, "GET "+location, as.get(location), http.StatusOK), wantFrame(t, doc); got != want || !strings.Contains(got, changed) {
				t.Errorf("GET %s answers\n%s\nwant\n%s", location, got, want)
			}

			// The same bytes again is a save of nothing: the version stays and nothing is to be built.
			res = as.put(location, saveBody(doc.Seq, changed))
			again := result(t, "PUT "+location+" again", res, http.StatusOK)
			if again.Version != saved.Version || again.Publishes {
				t.Errorf("a save of the same bytes answers %+v, want the version %+v and no publish", again, saved.Version)
			}
			if got := a.stored(t, tt.kind, created.ID); got.Seq != doc.Seq {
				t.Errorf("a save of the same bytes made the version %d, want %d", got.Seq, doc.Seq)
			}

			// Delete it at its version.
			res = as.del(location + "?base=" + strconv.FormatInt(doc.Seq, 10))
			deleted := result(t, "DELETE "+location, res, http.StatusOK)
			if deleted.Kind != tt.kind || deleted.ID != created.ID || deleted.Version.Seq <= doc.Seq || deleted.Version.Hash != "" || !deleted.Publishes {
				t.Errorf("the answer of DELETE %s is %+v, want a later version without a hash that publishes", location, deleted)
			}
			if !a.missing(t, tt.kind, created.ID) {
				t.Error("the store still has the document that was deleted")
			}
			checkProblem(t, "GET "+location+" after the deletion", as.get(location), "not-found")

			// The records say who did what, once for each operation that succeeded.
			for i, want := range []int{1, 2, 1} {
				var mine []audit.Record
				for _, rec := range a.records(t, tt.actions[i]) {
					if rec.Target.ID == created.ID {
						mine = append(mine, rec)
					}
				}
				if len(mine) != want || mine[0].Actor != editor || mine[0].Target.Kind != string(tt.kind) {
					t.Errorf("%d records of %s for the document, the first %+v, want %d by the editor", len(mine), tt.actions[i], mine, want)
				}
			}
		})
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// firstProblem returns the one error of the problem invalid that res is.
func firstProblem(t *testing.T, what string, res *http.Response) problemError {
	t.Helper()
	p := readProblem(t, res)
	if p.Status != http.StatusUnprocessableEntity || p.Code != "invalid" || p.Detail != detailInvalid || len(p.Errors) != 1 {
		t.Fatalf("%s: the problem is %+v, want one error of the problem invalid", what, p)
	}
	return p.Errors[0]
}

var termFile = regexp.MustCompile(`^terms/trm_[0-9A-Z]{26}\.json$`)

// lines wraps the document of a body in lines of its own, which the places of the problems must not count.
func lines(document string) string { return "{\n\n  \"document\":\n" + document + "\n}\n" }

func linesSave(base int64, document string) string {
	return fmt.Sprintf("{\n\n  \"base\": %d,\n  \"document\":\n%s\n}\n", base, document)
}

// TestDocumentProblems shows that the problems of a document are answered with 422 and their places in the bytes of the
// document that was sent: its lines and its columns, and not those of the body that carried it.
func TestDocumentProblems(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("editor")
	before := a.versions(t)

	withID := strings.Replace(termDoc("category", "ja", "ニュースレター", "newsletter"), `  "taxonomy"`, `  "id": "`+newsTerm+`",`+"\n"+`  "taxonomy"`, 1)
	e := firstProblem(t, "an id in a document to create", as.post("/api/terms", lines(withID)))
	line, col := locate(t, withID, `"id"`)
	if !termFile.MatchString(e.File) || e.Pointer != "/id" || e.Detail != "not allowed when creating; the store assigns the ID" || e.Line != line || e.Column != col {
		t.Errorf("the error of an id is %+v, want the file of the new term, /id and the place %d:%d of the id in the document", e, line, col)
	}

	wrongType := strings.Replace(termDoc("category", "ja", "x", "newsletter"), `"name": "x"`, `"name": 5`, 1)
	e = firstProblem(t, "a name that is a number", as.post("/api/terms", lines(wrongType)))
	line, col = locate(t, wrongType, `"name"`)
	if !termFile.MatchString(e.File) || e.Pointer != "/name" || e.Line != line || e.Column != col || e.Detail != "expected string, got number" {
		t.Errorf("the error of a name that is a number is %+v, want the file of the new term, /name and the place %d:%d of the name", e, line, col)
	}

	badSlug := termDoc("category", "ja", "ニュースレター", "a b")
	e = firstProblem(t, "a slug with a space", as.post("/api/terms", lines(badSlug)))
	line, col = locate(t, badSlug, `"slug"`)
	if !termFile.MatchString(e.File) || e.Pointer != "/slug" || e.Line != line || e.Column != col || !strings.Contains(e.Detail, "not allowed in slugs") {
		t.Errorf("the error of a slug is %+v, want the file of the new term, /slug and the place %d:%d of the slug", e, line, col)
	}

	// A body that is not JSON is not a document with problems: it is refused as a body.
	checkProblem(t, "a document that is not JSON", as.post("/api/terms", "{\"document\": {\n  \"schemaVersion\": 1,\n  oops\n}}"), "bad-request")

	// The same for a save; the file is that of the document.
	news := a.stored(t, content.RefTerm, newsTerm)
	text := strings.Replace(string(news.Data), `"slug": "news"`, `"slug": "n w"`, 1)
	e = firstProblem(t, "a slug with a space in a save", as.put("/api/terms/"+newsTerm, linesSave(news.Seq, text)))
	line, col = locate(t, text, `"slug"`)
	if e.File != "terms/"+newsTerm+".json" || e.Pointer != "/slug" || e.Line != line || e.Column != col {
		t.Errorf("the error of a slug in a save is %+v, want terms/%s.json, /slug and the place %d:%d", e, newsTerm, line, col)
	}

	other := strings.Replace(string(news.Data), `"id": "`+newsTerm+`"`, `"id": "trm_01JBA4C7E2G5J8K1M4P7R2T5W9"`, 1)
	e = firstProblem(t, "the id of another document in a save", as.put("/api/terms/"+newsTerm, linesSave(news.Seq, other)))
	line, col = locate(t, other, `"id"`)
	if e.Pointer != "/id" || e.Line != line || e.Column != col || !strings.Contains(e.Detail, "differs from the ID of the document") {
		t.Errorf("the error of another id in a save is %+v, want /id at %d:%d", e, line, col)
	}

	// A document that others use cannot be deleted, which is a problem of the request too.
	e = firstProblem(t, "a term that is used", as.del("/api/terms/"+newsTerm+"?base="+strconv.FormatInt(news.Seq, 10)))
	if e.File != "terms/"+newsTerm+".json" || !strings.Contains(e.Detail, "cannot delete: used by") {
		t.Errorf("the error of the deletion of a term that is used is %+v, want that it is used by documents", e)
	}

	if got := a.versions(t); got != before {
		t.Errorf("the requests that failed have changed the store:\n%s\nwant\n%s", got, before)
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestDocumentConflicts shows that a save or a deletion at a version that is not the current one is refused with 409, and
// changes nothing.
func TestDocumentConflicts(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("editor")
	loc := "/api/terms/" + newsTerm
	first := a.stored(t, content.RefTerm, newsTerm)
	changed := strings.Replace(string(first.Data), "お知らせ", "お知らせ2", 1)
	second := result(t, "the first save", as.put(loc, saveBody(first.Seq, changed)), http.StatusOK)
	if second.Version.Seq <= first.Seq {
		t.Fatalf("the save made the version %d after %d", second.Version.Seq, first.Seq)
	}
	before := a.versions(t)
	records := len(a.records(t, ""))

	other := strings.Replace(string(first.Data), "お知らせ", "お知らせ3", 1)
	checkProblem(t, "a save at an old version", as.put(loc, saveBody(first.Seq, other)), "conflict")
	checkProblem(t, "a save at a version that does not exist yet", as.put(loc, saveBody(second.Version.Seq+100, other)), "conflict")
	checkProblem(t, "a save at the version 0", as.put(loc, saveBody(0, other)), "conflict")
	checkProblem(t, "a deletion at an old version", as.del(loc+"?base="+strconv.FormatInt(first.Seq, 10)), "conflict")
	checkProblem(t, "a deletion at the version 0", as.del(loc+"?base=0"), "conflict")
	settings := a.stored(t, store.SettingsKind, "")
	checkProblem(t, "a save of the settings as if there were none", a.as("admin").put("/api/settings", saveBody(0, string(settings.Data))), "conflict")
	checkProblem(t, "a save of the settings at an old version", a.as("admin").put("/api/settings", saveBody(settings.Seq-1, string(settings.Data))), "conflict")

	if got := a.versions(t); got != before {
		t.Errorf("the requests that conflicted have changed the store:\n%s\nwant\n%s", got, before)
	}
	if got := len(a.records(t, "")); got != records {
		t.Errorf("the requests that conflicted made %d records of the activity log", got-records)
	}
	// The document of the save is the value that was sent, without the line break after it in the file it came from.
	if got := a.stored(t, content.RefTerm, newsTerm); string(got.Data) != strings.TrimSpace(changed) {
		t.Errorf("the term is\n%s\nwant what the one save made", got.Data)
	}

	// At the current version a deletion is not a conflict: the menus that use the term are the problem.
	e := firstProblem(t, "a deletion at the current version of a term that is used", as.del(loc+"?base="+strconv.FormatInt(second.Version.Seq, 10)))
	if !strings.Contains(e.Detail, "cannot delete: used by") {
		t.Errorf("the error is %+v, want that the term is used", e)
	}
}

// TestDocumentShapes shows what a request must look like: the body, the query, the header and the path.
func TestDocumentShapes(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("editor")
	before := a.versions(t)
	loc := "/api/terms/" + newsTerm
	news := a.stored(t, content.RefTerm, newsTerm)
	doc := termDoc("category", "ja", "ニュースレター", "newsletter")
	valid := saveBody(news.Seq, strings.Replace(string(news.Data), "お知らせ", "お知らせ4", 1))

	// What is wrong in the JSON of the body is 400 whatever the route, and nothing is saved.
	for _, tt := range []struct {
		name, body string
	}{
		{"no base", `{"document":` + doc + `}`},
		{"a base that is a string", `{"base":"1","document":` + doc + `}`},
		{"a base that is a fraction", `{"base":1.5,"document":` + doc + `}`},
		{"a base that is negative", `{"base":-1,"document":` + doc + `}`},
		{"a base that is null", `{"base":null,"document":` + doc + `}`},
		{"no document", `{"base":1}`},
		{"a document that is null", `{"base":1,"document":null}`},
		{"a document that is an array", `{"base":1,"document":[]}`},
		{"a document that is a string", `{"base":1,"document":"x"}`},
		{"a document that is a number", `{"base":1,"document":5}`},
		{"a member that the body does not have", `{"base":1,"document":` + doc + `,"extra":1}`},
		{"a member twice", `{"base":1,"base":2,"document":` + doc + `}`},
		{"an array", `[]`},
		{"an object without members", `{}`},
		{"JSON that stops", `{"base":1,"document":{`},
		{"text after the JSON", valid + ` {}`},
		{"no body", ``},
	} {
		checkProblem(t, "PUT with "+tt.name, as.put(loc, tt.body, withBody(tt.body)...), "bad-request")
	}
	for _, tt := range []struct {
		name, body string
	}{
		{"a base", `{"base":1,"document":` + doc + `}`},
		{"no document", `{}`},
		{"a document that is null", `{"document":null}`},
		{"a document that is an array", `{"document":[]}`},
		{"a member that the body does not have", `{"document":` + doc + `,"extra":1}`},
		{"no body", ``},
	} {
		checkProblem(t, "POST with "+tt.name, as.post("/api/terms", tt.body, withBody(tt.body)...), "bad-request")
	}
	checkProblem(t, "PUT settings with no base", a.as("admin").put("/api/settings", `{"document":{}}`), "bad-request")
	checkProblem(t, "PUT settings with a document that is null", a.as("admin").put("/api/settings", `{"base":1,"document":null}`), "bad-request")

	// The media type of the body.
	checkProblem(t, "a body that is text", as.put(loc, valid, "Content-Type", "text/plain"), "unsupported-media-type")
	checkProblem(t, "a body with no media type", as.put(loc, valid, "Content-Type", ""), "unsupported-media-type")

	// The query: a deletion needs base once, as the Seq of a version; the others take none.
	for _, q := range []string{"", "?base=", "?base=abc", "?base=-1", "?base=%2B1", "?base=1.5", "?base=1&base=2", "?base=1&x=2", "?x=2", "?base=99999999999999999999"} {
		checkProblem(t, "DELETE "+q, as.del(loc+q), "bad-query")
	}
	for _, q := range []string{"?x=1", "?base=1", "?offset=0", "?%zz"} {
		checkProblem(t, "GET "+q, as.get(loc+q), "bad-query")
		checkProblem(t, "PUT "+q, as.put(loc+q, valid), "bad-query")
		checkProblem(t, "POST "+q, as.post("/api/terms"+q, createBody(doc)), "bad-query")
		checkProblem(t, "GET settings "+q, as.get("/api/settings"+q), "bad-query")
		checkProblem(t, "PUT settings "+q, a.as("admin").put("/api/settings"+q, valid), "bad-query")
	}

	// The header Idempotency-Key is an operation ID, once, on the routes that take one, and on none that only read.
	for _, h := range [][]string{
		{"Idempotency-Key", "op_01JD0000000000000000000001"},
		{"Idempotency-Key", `"op_x"`},
		{"Idempotency-Key", `""`},
		{"Idempotency-Key", `"` + someOp + `"`, "Idempotency-Key", `"` + someOp + `"`},
	} {
		what := strings.Join(h, " ")
		checkProblem(t, "PUT with the header "+what, as.put(loc, valid, h...), "bad-header")
		checkProblem(t, "POST with the header "+what, as.post("/api/terms", createBody(doc), h...), "bad-header")
		checkProblem(t, "DELETE with the header "+what, as.del(loc+"?base=1", h...), "bad-header")
		checkProblem(t, "PUT settings with the header "+what, a.as("admin").put("/api/settings", valid, h...), "bad-header")
	}
	for _, path := range []string{"/api/terms", loc, "/api/settings", "/api/media"} {
		checkProblem(t, "GET "+path+" with an operation ID", as.send(http.MethodGet, path, "", key(1)...), "bad-header")
	}

	// The path: an ID that is not the ID of a document of the kind names nothing.
	for _, id := range []string{missingID, "x", "trm_x", "ent_01JB8Y6T2WT6X9C3E6G9J2K5M8", strings.ToLower(newsTerm), "trm_01JBA4C7E2G5J8K1M4P7R2T5W", "%20", "..%2f"} {
		path := "/api/terms/" + id
		checkProblem(t, "GET "+path, as.get(path), "not-found")
		checkProblem(t, "PUT "+path, as.put(path, valid), "not-found")
		checkProblem(t, "DELETE "+path, as.del(path+"?base=1"), "not-found")
	}
	checkProblem(t, "GET a medium that does not exist", as.get("/api/media/"+missingMed), "not-found")
	checkProblem(t, "PUT a medium that does not exist", as.put("/api/media/"+missingMed, valid), "not-found")
	checkProblem(t, "DELETE a medium that does not exist", as.del("/api/media/"+missingMed+"?base=1"), "not-found")
	checkProblem(t, "GET an author that does not exist", as.get("/api/authors/aut_01JBA4C7N9Q3T6W9Z3C6F9H2ZZ"), "not-found")
	checkProblem(t, "GET a kind that does not exist", as.get("/api/things"), "not-found")
	checkProblem(t, "GET a term as a menu", as.get("/api/menus/"+newsTerm), "not-found")

	// A method that the resource does not take.
	for _, tt := range []struct {
		method, path, allow string
	}{
		{http.MethodPatch, loc, "DELETE, GET, HEAD, PUT"},
		{http.MethodPost, loc, "DELETE, GET, HEAD, PUT"},
		{http.MethodPut, "/api/terms", "GET, HEAD, POST"},
		{http.MethodDelete, "/api/terms", "GET, HEAD, POST"},
		{http.MethodPost, "/api/settings", "GET, HEAD, PUT"},
		{http.MethodDelete, "/api/settings", "GET, HEAD, PUT"},
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
	if got := a.records(t, audit.AccessDenied); len(got) != 0 {
		t.Errorf("the requests that were refused as requests made %d records of access.denied, want none", len(got))
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestDocumentDeleteWarnings shows that the answer of a deletion has the warnings of the operation: a term that only a draft
// uses is deleted, and the draft is told of.
func TestDocumentDeleteWarnings(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("editor")
	term := result(t, "POST a term", as.post("/api/terms", createBody(termDoc("category", "ja", "下書きだけ", "draft-only"))), http.StatusCreated)

	ed := a.pub.Editor()
	op := edit.Op{By: a.people["editor"].account.ID}
	entry, err := ed.CreateEntry(t.Context(), op, content.TypePost, "ja")
	if err != nil {
		t.Fatal(err)
	}
	draft := fmt.Sprintf(`{"schemaVersion":1,"title":"下書き","slug":"draft","author":%q,"terms":{"category":[%q]},`+
		`"body":[{"type":"paragraph","id":"b_draft001","content":[{"type":"text","text":"下書きです。"}]}]}`, yamada, term.ID)
	revision, err := ed.SaveRevision(t.Context(), op, entry.ID, "", []byte(draft))
	if err != nil {
		t.Fatal(err)
	}

	loc := "/api/terms/" + term.ID
	doc := a.stored(t, content.RefTerm, term.ID)
	deleted := result(t, "DELETE "+loc, as.del(loc+"?base="+strconv.FormatInt(doc.Seq, 10)), http.StatusOK)
	if len(deleted.Warnings) != 1 || deleted.OmittedWarnings != 0 {
		t.Fatalf("the deletion has the warnings %+v, want one", deleted.Warnings)
	}
	w := deleted.Warnings[0]
	if want := "deleted although used by revisions/" + revision.Revision + ".json, the latest revision of entries/" + entry.ID + ".json, which is not published"; w.File != "terms/"+term.ID+".json" || w.Detail != want {
		t.Errorf("the warning is %+v, want that %s", w, want)
	}
	if !a.missing(t, content.RefTerm, term.ID) {
		t.Error("the term is still in the store")
	}
}
