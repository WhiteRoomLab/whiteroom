package admin

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// entryFact is what the store has of an entry, read without the API, which the lists are checked against.
type entryFact struct {
	id, typ, language, status, createdBy, group string
	number                                      int
	createdAt                                   time.Time
	scheduledAt                                 time.Time
	hasScheduled                                bool
	last                                        *store.Revision   // the last revision, without its JSON; nil if none
	lastDoc                                     *content.Revision // its JSON, decoded
	published                                   *content.Revision // the published revision, decoded; nil if none
}

// entryFacts returns the facts of every entry in the store, in the order of the IDs.
func (a *apiServer) entryFacts(t *testing.T) []entryFact {
	t.Helper()
	var facts []entryFact
	err := a.db.View(t.Context(), func(tx *store.Tx) error {
		docs, err := tx.Documents(content.RefEntry)
		if err != nil {
			return err
		}
		for _, d := range docs {
			var e content.Entry
			if err := e.UnmarshalJSON(d.Data); err != nil {
				return err
			}
			f := entryFact{
				id: e.ID, typ: e.Type, language: e.Language, status: string(e.Status), createdBy: e.CreatedBy, group: e.TranslationGroup,
				number: e.Number,
			}
			if f.createdAt, err = time.Parse(time.RFC3339, e.CreatedAt); err != nil {
				return err
			}
			if e.ScheduledAt != "" {
				if f.scheduledAt, err = time.Parse(time.RFC3339, e.ScheduledAt); err != nil {
					return err
				}
				f.hasScheduled = true
			}
			revs, err := tx.Revisions(e.ID)
			if err != nil {
				return err
			}
			read := func(id string) (store.Revision, *content.Revision, error) {
				r, err := tx.Revision(id)
				if err != nil {
					return r, nil, err
				}
				rev := new(content.Revision)
				return r, rev, rev.UnmarshalJSON(r.Data)
			}
			if len(revs) > 0 {
				r, rev, err := read(revs[len(revs)-1].ID)
				if err != nil {
					return err
				}
				r.Data = nil
				f.last, f.lastDoc = &r, rev
			}
			if e.Status == content.StatusPublished {
				if _, f.published, err = read(e.PublishedRevision); err != nil {
					return err
				}
			}
			facts = append(facts, f)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return facts
}

// ids returns the IDs of the facts for which keep is true, in order.
func ids(facts []entryFact, keep func(f entryFact) bool) []string {
	out := []string{}
	for _, f := range facts {
		if keep(f) {
			out = append(out, f.id)
		}
	}
	return out
}

// hasTermIn reports whether the published revision or the last one of the entry has the term in its terms.
func (f entryFact) hasTermIn(term string) bool {
	for _, rev := range []*content.Revision{f.published, f.lastDoc} {
		if rev == nil {
			continue
		}
		for _, in := range rev.Terms {
			if slices.Contains(in, term) {
				return true
			}
		}
	}
	return false
}

// entryFixture is the sample site with ten entries more, made through the API and the Editor, for the lists.
type entryFixture struct {
	feature string // a term in the category taxonomy, in ja
	// The entries, each a post in ja and a draft unless it says other, made by the editor and a minute after the one before it:
	promoted   string // published with the term; its last revision, a draft, has not; the same time as later
	later      string // published without the term; its last revision has it
	linked     string // its last revision has the ID of the term in a text, and not in its terms
	drafted    string // a draft that has the term
	soon       string // scheduled in two days
	afterwards string // scheduled in three days
	retired    string // archived
	untitled   string // its revision has no title
	english    string // in en
	fixed      string // a page
	authored   string // made by the author
}

// newEntryFixture returns a server whose store has the sample site and the entries of the fixture.
func newEntryFixture(t *testing.T) (*apiServer, entryFixture) {
	t.Helper()
	a := newAPIServer(t)
	tick := func() { a.clock.advance(time.Minute) }
	date := ` "date": "2026-11-01T10:00:00+09:00",`
	var f entryFixture
	f.feature = result(t, "POST /api/terms", a.as("editor").post("/api/terms", createBody(termDoc("category", "ja", "特集", "feature"))), http.StatusCreated).ID

	f.promoted = a.newEntry(t, "editor", "post", "ja").ID
	a.addRevision(t, "editor", f.promoted, postRevision("公開した特集", "promoted", date+termsOf(f.feature)))
	a.publishEntry(t, "editor", f.promoted)
	a.addRevision(t, "editor", f.promoted, postRevision("特集をはずした下書き", "promoted", date))
	f.later = a.newEntry(t, "editor", "post", "ja").ID // in the same minute
	a.addRevision(t, "editor", f.later, postRevision("公開したもの", "later", date))
	a.publishEntry(t, "editor", f.later)
	a.addRevision(t, "editor", f.later, postRevision("特集に入れた下書き", "later", date+termsOf(f.feature)))
	tick()

	f.linked = a.newEntry(t, "editor", "post", "ja").ID
	a.addRevision(t, "editor", f.linked, strings.Replace(postRevision("リンクだけ", "linked", ""), "下書きです。", f.feature, 1))
	tick()

	f.drafted = a.newEntry(t, "editor", "post", "ja").ID
	a.addRevision(t, "editor", f.drafted, postRevision("特集の下書き", "drafted", termsOf(f.feature)))
	tick()

	ed := a.pub.Editor()
	op := edit.Op{By: a.people["editor"].account.ID}
	for _, s := range []struct {
		id    *string
		slug  string
		after time.Duration
	}{{&f.soon, "soon", 48 * time.Hour}, {&f.afterwards, "afterwards", 72 * time.Hour}} {
		*s.id = a.newEntry(t, "editor", "post", "ja").ID
		a.addRevision(t, "editor", *s.id, postRevision("予約 "+s.slug, s.slug, date))
		doc := a.stored(t, content.RefEntry, *s.id)
		if _, err := ed.Schedule(t.Context(), op, *s.id, doc.Seq, a.lastRevision(t, *s.id), a.clock.now().Add(s.after)); err != nil {
			t.Fatalf("Schedule(%s) error = %v", s.slug, err)
		}
		tick()
	}

	f.retired = a.newEntry(t, "editor", "post", "ja").ID
	a.addRevision(t, "editor", f.retired, postRevision("しまったもの", "retired", date))
	if _, err := ed.Archive(t.Context(), op, f.retired, a.publishEntry(t, "editor", f.retired).Version.Seq); err != nil {
		t.Fatalf("Archive() error = %v", err)
	}
	tick()

	f.untitled = a.newEntry(t, "editor", "post", "ja").ID
	result(t, "a revision without a title", a.as("editor").post("/api/entries/"+f.untitled+"/revisions", revisionBody("", `{"schemaVersion":1}`)), http.StatusCreated)
	tick()

	f.english = a.newEntry(t, "editor", "post", "en").ID
	a.addRevision(t, "editor", f.english, postRevision("English draft", "english", ""))
	tick()

	f.fixed = a.newEntry(t, "editor", "page", "ja").ID
	a.addRevision(t, "editor", f.fixed, `{"schemaVersion":1,"title":"固定ページ","slug":"fixed","body":[`+paragraph+`]}`)
	tick()

	f.authored = a.newEntry(t, "author", "post", "ja").ID
	a.addRevision(t, "author", f.authored, postRevision("著者の下書き", "authored", ""))
	return a, f
}

// entryRowBody is a row of the list of entries, in which what may be left out is a pointer.
type entryRowBody struct {
	Version        versionAnswer  `json:"version"`
	Document       jsontext.Value `json:"document"`
	LatestRevision *struct {
		ID      string    `json:"id"`
		Title   *string   `json:"title"`
		SavedAt time.Time `json:"savedAt"`
		SavedBy string    `json:"savedBy"`
	} `json:"latestRevision"`
	Published *struct {
		Slug string  `json:"slug"`
		Date *string `json:"date"`
	} `json:"published"`
}

type entryListBody struct {
	Items  []entryRowBody `json:"items"`
	Total  int            `json:"total"`
	Offset int            `json:"offset"`
	Limit  int            `json:"limit"`
}

// entryList reads the list of entries at the path as the account login, which must answer 200, and returns the IDs of its
// rows and the answer.
func (a *apiServer) entryList(t *testing.T, login, path string) ([]string, entryListBody) {
	t.Helper()
	var l entryListBody
	b := body(t, "GET "+path, a.as(login).get(path), http.StatusOK)
	if err := json.Unmarshal([]byte(b), &l); err != nil {
		t.Fatalf("GET %s: the answer %.300s is not a list: %v", path, b, err)
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

// byNumber returns the facts as their IDs in the order of the numbers, the largest first.
func byNumber(facts []entryFact, keep func(f entryFact) bool) []string {
	var kept []entryFact
	for _, f := range facts {
		if keep(f) {
			kept = append(kept, f)
		}
	}
	slices.SortFunc(kept, func(x, y entryFact) int { return y.number - x.number })
	out := make([]string, len(kept))
	for i, f := range kept {
		out[i] = f.id
	}
	return out
}

// TestRevisionTitle shows which title a row has: the member title of the top level, and none where the revision has none that is
// a string or cannot be read.
func TestRevisionTitle(t *testing.T) {
	for _, tt := range []struct {
		name, data string
		want       string
		ok         bool
	}{
		{"a title", `{"schemaVersion":1,"title":"題","slug":"s"}`, "題", true},
		{"a title first", `{"title":"a"}`, "a", true},
		{"white space", "{\n\t\"title\" :\n  \"a b\"\n}\n", "a b", true},
		{"escapes", `{"title":"a \"b\" あ\n"}`, "a \"b\" あ\n", true},
		{"an empty title", `{"title":""}`, "", true},
		{"after members of other kinds", `{"fields":{"title":"inner"},"body":[{"title":"block"},[1,2,{"title":"x"}],null,1.5,true],"title":"outer"}`, "outer", true},
		{"the first of two", `{"title":"one","title":"two"}`, "one", true},
		{"nothing after the title is read", `{"title":"a",`, "a", true},
		{"no title", `{"schemaVersion":1,"slug":"s"}`, "", false},
		{"a title only inside", `{"seo":{"title":"seo"},"body":[{"title":"b"}]}`, "", false},
		{"a title that is a number", `{"title":5}`, "", false},
		{"a title that is null", `{"title":null}`, "", false},
		{"a title that is an object", `{"title":{"a":"b"}}`, "", false},
		{"a title that is an array", `{"title":["a"]}`, "", false},
		{"a name like title", `{"Title":"a","titles":"b"," title":"c"}`, "", false},
		{"an empty object", `{}`, "", false},
		{"an array", `["title"]`, "", false},
		{"a string", `"title"`, "", false},
		{"null", `null`, "", false},
		{"nothing", ``, "", false},
		{"JSON that stops before the title", `{"body":[{"a":`, "", false},
		{"JSON that stops in the title", `{"title":"ab`, "", false},
		{"a value that is not JSON before the title", `{"a":oops,"title":"x"}`, "", false},
		{"a title with invalid UTF-8", "{\"title\":\"a\xffb\"}", "", false},
	} {
		got, ok := revisionTitle([]byte(tt.data))
		if got != tt.want || ok != tt.ok {
			t.Errorf("%s: revisionTitle(%q) = %q, %v, want %q, %v", tt.name, tt.data, got, ok, tt.want, tt.ok)
		}
	}
}

// TestEntryListFilters shows what each name of the query keeps, alone and together, and the total after the filters.
func TestEntryListFilters(t *testing.T) {
	a, _ := newEntryFixture(t)
	facts := a.entryFacts(t)
	editor, author := a.people["editor"].account.ID, a.people["author"].account.ID
	var aboutGroup string // the group of the two pages "about", in ja and in en
	for _, x := range facts {
		if x.id == "ent_01JB8W2N5KD3F6H9J1M4P7R2T5" {
			aboutGroup = x.group
		}
	}
	if len(facts) != 34+11 || len(ids(facts, func(x entryFact) bool { return x.status == "scheduled" })) != 2 {
		t.Fatalf("the store has %d entries, want the 34 of the sample and the 11 of the fixture, 2 of them scheduled", len(facts))
	}
	is := func(field func(x entryFact) string, value string) func(x entryFact) bool {
		return func(x entryFact) bool { return field(x) == value }
	}
	typ := func(x entryFact) string { return x.typ }
	language := func(x entryFact) string { return x.language }
	status := func(x entryFact) string { return x.status }
	createdBy := func(x entryFact) string { return x.createdBy }
	group := func(x entryFact) string { return x.group }

	for _, tt := range []struct {
		query string
		keep  func(x entryFact) bool
		count int // how many entries it keeps, if it is not 0
	}{
		{"", func(entryFact) bool { return true }, 45},
		{"?type=post", is(typ, "post"), 0},
		{"?type=page", is(typ, "page"), 0},
		{"?type=works", is(typ, "works"), 1},
		{"?type=nothing", is(typ, "nothing"), 0},
		{"?language=ja", is(language, "ja"), 0},
		{"?language=en", is(language, "en"), 0},
		{"?language=fr", is(language, "fr"), 0},
		{"?status=draft", is(status, "draft"), 0},
		{"?status=published", is(status, "published"), 0},
		{"?status=scheduled", is(status, "scheduled"), 2},
		{"?status=archived", is(status, "archived"), 1},
		{"?status=draft,published", func(x entryFact) bool { return x.status == "draft" || x.status == "published" }, 0},
		{"?status=archived,scheduled", func(x entryFact) bool { return x.status == "archived" || x.status == "scheduled" }, 3},
		{"?status=draft,draft", is(status, "draft"), 0},
		{"?status=archived,scheduled,draft,published", func(entryFact) bool { return true }, 45},
		{"?createdBy=" + importer, is(createdBy, importer), 34},
		{"?createdBy=" + editor, is(createdBy, editor), 10},
		{"?createdBy=" + author, is(createdBy, author), 1},
		{"?createdBy=usr_01JD0000000000000000000099", is(createdBy, "usr_01JD0000000000000000000099"), 0},
		{"?translationGroup=" + aboutGroup, is(group, aboutGroup), 2},
		{"?translationGroup=tg_01JD0000000000000000000099", is(group, "tg_01JD0000000000000000000099"), 0},
		{"?type=post&language=en", func(x entryFact) bool { return x.typ == "post" && x.language == "en" }, 0},
		{"?type=post&language=ja&status=draft&createdBy=" + editor, func(x entryFact) bool {
			return x.typ == "post" && x.language == "ja" && x.status == "draft" && x.createdBy == editor
		}, 0},
		{"?type=page&status=published,draft", func(x entryFact) bool { return x.typ == "page" && (x.status == "published" || x.status == "draft") }, 0},
		{"?status=scheduled&createdBy=" + author, func(entryFact) bool { return false }, 0},
	} {
		want := byNumber(facts, tt.keep)
		if tt.count != 0 && len(want) != tt.count {
			t.Errorf("the facts say that %s keeps %d entries, want %d", tt.query, len(want), tt.count)
		}
		// Every role reads them, and each reads the same.
		for _, l := range apiLogins {
			got, list := a.entryList(t, l.login, "/api/entries"+tt.query)
			if !slices.Equal(got, want) || list.Total != len(want) || list.Offset != 0 || list.Limit != defaultPage {
				t.Errorf("GET /api/entries%s as %s: %d rows of %d, want %d: %v", tt.query, l.login, len(got), list.Total, len(want), want)
			}
		}
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}
