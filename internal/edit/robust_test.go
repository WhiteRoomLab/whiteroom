package edit

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/siteplan"
)

// randomSite makes a model of a site whose documents hold what the store's own API lets in: values that do not
// validate, and IDs of documents that are missing, drawn from small pools so that they meet.
func randomSite(t *testing.T, r *rand.Rand, site *content.Site, ed *Editor) *siteplan.Site {
	t.Helper()
	pick := func(pool ...string) string { return pool[r.IntN(len(pool))] }
	id := func(kind content.RefKind, n int) string { return testID(kind, n) }
	// The documents numbered 1 to 4 may be in the model, and the one numbered 6 never is.
	entryID := func() string {
		return pick("", id(content.RefEntry, 1), id(content.RefEntry, 2), id(content.RefEntry, 3), id(content.RefEntry, 4), id(content.RefEntry, 6), "ent_x")
	}
	termID := func() string {
		return pick("", id(content.RefTerm, 1), id(content.RefTerm, 2), id(content.RefTerm, 3), id(content.RefTerm, 4), id(content.RefTerm, 6), "trm_x")
	}
	authorID := func() string {
		return pick("", id(content.RefAuthor, 1), id(content.RefAuthor, 2), id(content.RefAuthor, 6))
	}
	language := func() string { return pick("", "ja", "en", "zz", "en-US") }
	slug := func() string { return pick("", "a", "b", "news", "A B", "a/b", "..", "イベント") }
	path := func() string {
		return pick("", "/", "/a/", "/a", "/a/?x=1", "/a?x=1&y=2", "/?p=1", "/about/", "/feed/", "x", "a/b", "//", "/a/../b/", "/%zz/",
			"https://example.com/a/", "http://other.example/", "mailto:x@example.com", "#top", "/a/#top", "?x=1", "/ "+string(rune(0x3000))+"/")
	}

	s := *site
	s.FrontPage = content.Localized{"ja": entryID(), "en": entryID()}
	s.PostsPage = nil
	if !strings.HasSuffix(s.Type(content.TypePage).URLRule(), ".html") {
		s.PostsPage = content.Localized{"ja": entryID()}
	}
	m := &siteplan.Site{
		Settings: content.SiteFile{Name: settingsName}, Schema: content.SiteFile{Name: ed.schema.Name}, Site: &s, Theme: ed.theme,
		Entries:   make(map[string]*siteplan.Entry),
		Terms:     make(map[string]*siteplan.Doc[content.Term]),
		Media:     make(map[string]*siteplan.Doc[content.Media]),
		Authors:   make(map[string]*siteplan.Doc[content.AuthorProfile]),
		Menus:     make(map[string]*siteplan.Doc[content.Menu]),
		Forms:     make(map[string]*siteplan.Doc[content.Form]),
		Redirects: make(map[string]*siteplan.Doc[content.Redirect]),
	}
	for n := 1; n <= 4; n++ {
		if r.IntN(5) == 0 {
			continue
		}
		eid := id(content.RefEntry, n)
		e := &siteplan.Entry{File: content.SiteFile{Name: docName(content.RefEntry, eid)}, E: content.Entry{
			ID: eid, Number: r.IntN(4), Type: pick("", "post", "page", "works", "nope"), Language: language(),
			TranslationGroup: pick("", "tg_1", "tg_2", "tg_3"), CreatedAt: pick("", "x", "2026-10-25T10:00:00+09:00"),
			Status: content.Status(pick("published", "published", "draft")),
		}}
		if e.E.Status == content.StatusPublished && r.IntN(5) != 0 {
			e.E.PublishedRevision = id(content.RefRevision, n)
			e.RevFile = content.SiteFile{Name: revisionName(e.E.PublishedRevision)}
			e.Rev = &content.Revision{
				Slug: slug(), Parent: entryID(), Date: pick("", "x", "2026-10-25T10:00:00+09:00", "2026-10-25T10:00:00Z"),
				Author: authorID(), Sticky: r.IntN(2) == 0, Terms: content.Terms{},
			}
			for range r.IntN(3) {
				e.Rev.Terms[pick("category", "tag", "works_category", "nope", "")] = []string{termID(), termID()}
			}
		}
		m.Entries[eid] = e
	}
	for n := 1; n <= 4; n++ {
		if r.IntN(5) == 0 {
			continue
		}
		tid := id(content.RefTerm, n)
		m.Terms[tid] = &siteplan.Doc[content.Term]{File: content.SiteFile{Name: docName(content.RefTerm, tid)}, V: content.Term{
			ID: tid, Taxonomy: pick("", "category", "tag", "works_category", "nope"), Language: language(),
			TranslationGroup: pick("", "tg_1", "tg_2"), Slug: slug(), Parent: pick("", tid, termID(), termID()),
		}}
	}
	for n := 1; n <= 2; n++ {
		aid := id(content.RefAuthor, n)
		m.Authors[aid] = &siteplan.Doc[content.AuthorProfile]{File: content.SiteFile{Name: docName(content.RefAuthor, aid)},
			V: content.AuthorProfile{ID: aid, Slug: slug()}}
		mid := id(content.RefMenu, n)
		m.Menus[mid] = &siteplan.Doc[content.Menu]{File: content.SiteFile{Name: docName(content.RefMenu, mid)},
			V: content.Menu{ID: mid, Language: language(), Location: pick("", "header", "footer", "sidebar", "nope")}}
		fid := id(content.RefForm, n)
		m.Forms[fid] = &siteplan.Doc[content.Form]{File: content.SiteFile{Name: docName(content.RefForm, fid)}, V: content.Form{ID: fid}}
		did := id(content.RefMedia, n)
		m.Media[did] = &siteplan.Doc[content.Media]{File: content.SiteFile{Name: docName(content.RefMedia, did)},
			V: content.Media{ID: did, MIMEType: pick("", "image/jpeg", "application/pdf", "x")}}
	}
	for n := 1; n <= 5; n++ {
		rid := id(content.RefRedirect, n)
		to := pick(path(), entryID(), termID(), authorID(), id(content.RefMedia, 1), id(content.RefMedia, 6))
		m.Redirects[rid] = &siteplan.Doc[content.Redirect]{File: content.SiteFile{Name: docName(content.RefRedirect, rid)},
			V: content.Redirect{ID: rid, From: path(), To: to, Status: []int{301, 302, 0}[r.IntN(3)]}}
	}
	return m
}

// siteVariants returns sites that load, which differ in what the checks read of the site: the sample site; one with
// other URL rules, a path and a language prefix, and a page of a list that has one entry; and one whose pages are files.
func siteVariants(t *testing.T, ed *Editor) []*content.Site {
	t.Helper()
	settings, schema := string(sampleFile(t, settingsName)), string(ed.schema.Data)
	other := replaceOnce(t, settings, `"url": "https://example.com",`,
		`"url": "https://example.com/blog", "prefixDefaultLanguage": true, "postsPerPage": 1,`)
	files := replaceOnce(t, schema, "types:\n", "types:\n  page:\n    url: /{path}.html\n")
	noPostsPage := replaceOnce(t, settings, `  "postsPage": { "ja": "ent_01JBG3N6T2W5Y8B1D4F7H0K3M6" },`+"\n", "")
	var sites []*content.Site
	for _, v := range []struct{ settings, schema string }{
		{settings, schema},
		{other, replaceOnce(t, replaceOnce(t, schema, "url: /{yyyy}/{mm}/{dd}/{slug}/", "url: /{category}/{slug}/"),
			"url: /works/{slug}/", "url: /works/{number}/")},
		{noPostsPage, files},
	} {
		site, err := content.LoadSite(content.SiteFile{Name: settingsName, Data: []byte(v.settings)},
			content.SiteFile{Name: ed.schema.Name, Data: []byte(v.schema)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		sites = append(sites, site)
	}
	return sites
}

// TestStagesRobust runs the checks of the build on models with the values that the store's own API lets in, which a
// site that the build has validated does not have: none panics or loops.
func TestStagesRobust(t *testing.T) {
	ed := testEditor(t, nil)
	sites := siteVariants(t, ed)
	const models = 3000
	var withProblems [3]int // the models with problems in each stage
	var withWarnings, plans int
	for seed := range uint64(models) {
		r := rand.New(rand.NewPCG(seed, 7))
		m := randomSite(t, r, sites[seed%uint64(len(sites))], ed)
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("model %d: %v\n%s", seed, p, describe(m))
				}
			}()
			stage := 0
			stages(m, func(p, w content.Errors) bool {
				if len(p) > 0 {
					withProblems[stage]++
				}
				if len(w) > 0 {
					withWarnings++
				}
				stage++
				return true
			})
			if stage == 3 {
				plans++
			}
			_ = problemKeys(m)
		}()
	}
	t.Logf("%d models: with problems in the stages %v, %d with warnings, %d planned", models, withProblems, withWarnings, plans)
	// The models are not trivial: they have problems in every stage, and warnings.
	if withProblems[0] == 0 || withProblems[1] == 0 || withProblems[2] == 0 || withWarnings == 0 {
		t.Errorf("the models have problems in the stages %v and %d warnings, want some of each", withProblems, withWarnings)
	}
}

// describe returns the values of the documents of m, which make a model that a test can be asked to reproduce.
func describe(m *siteplan.Site) string {
	var b strings.Builder
	fmt.Fprintf(&b, "frontPage %v postsPage %v\n", m.Site.FrontPage, m.Site.PostsPage)
	for _, e := range m.Entries {
		fmt.Fprintf(&b, "%+v %+v\n", e.E, e.Rev)
	}
	for _, d := range m.Terms {
		fmt.Fprintf(&b, "%+v\n", d.V)
	}
	for _, d := range m.Redirects {
		fmt.Fprintf(&b, "%+v\n", d.V)
	}
	return b.String()
}
