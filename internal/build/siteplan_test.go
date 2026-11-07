package build

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/siteplan"
)

// decodeSite reads and decodes the documents of src, loads the settings and the theme, and validates what is published,
// as load does, and returns the builder there: whatever the checks across and the planning find, the caller runs them.
func decodeSite(t *testing.T, src Source) *builder {
	t.Helper()
	b := &builder{src: src}
	b.warnings.read = b.revisionData
	var err error
	if b.settings, b.schema, err = src.Settings(); err != nil {
		t.Fatal(err)
	}
	files := make(map[content.RefKind]map[string]content.SiteFile, len(kinds))
	for _, kind := range kinds {
		if files[kind], err = src.Documents(kind); err != nil {
			t.Fatal(err)
		}
	}
	themeFS, err := src.Theme()
	if err != nil {
		t.Fatal(err)
	}
	p := problems{read: b.revisionData}
	b.decode(files, &p)
	if p.len() > 0 {
		t.Fatalf("decode() problems:\n%v", p.sorted())
	}
	if err := b.loadSite(themeFS); err != nil {
		t.Fatalf("loadSite() error:\n%v", err)
	}
	if err := b.validate(&p); err != nil {
		t.Fatal(err)
	}
	if p.len() > 0 {
		t.Fatalf("validate() problems:\n%v", p.sorted())
	}
	return b
}

// summaries returns a copy of s that has only the fields of its documents that the doc comment of siteplan.Site lists,
// and only the names of the files, as the summaries that the store keeps of the documents would.
func summaries(s *siteplan.Site) *siteplan.Site {
	named := func(f content.SiteFile) content.SiteFile { return content.SiteFile{Name: f.Name} }
	r := &siteplan.Site{
		Settings:  named(s.Settings),
		Schema:    named(s.Schema),
		Site:      s.Site,
		Theme:     s.Theme,
		Entries:   make(map[string]*siteplan.Entry, len(s.Entries)),
		Terms:     make(map[string]*siteplan.Doc[content.Term], len(s.Terms)),
		Media:     make(map[string]*siteplan.Doc[content.Media], len(s.Media)),
		Authors:   make(map[string]*siteplan.Doc[content.AuthorProfile], len(s.Authors)),
		Menus:     make(map[string]*siteplan.Doc[content.Menu], len(s.Menus)),
		Forms:     make(map[string]*siteplan.Doc[content.Form], len(s.Forms)),
		Redirects: make(map[string]*siteplan.Doc[content.Redirect], len(s.Redirects)),
	}
	for id, e := range s.Entries {
		x := &siteplan.Entry{
			File: named(e.File),
			E: content.Entry{
				ID:               e.E.ID,
				Number:           e.E.Number,
				Type:             e.E.Type,
				Language:         e.E.Language,
				TranslationGroup: e.E.TranslationGroup,
				CreatedAt:        e.E.CreatedAt,
			},
			RevFile: named(e.RevFile),
		}
		if e.Rev != nil {
			x.Rev = &content.Revision{
				Slug:   e.Rev.Slug,
				Parent: e.Rev.Parent,
				Date:   e.Rev.Date,
				Author: e.Rev.Author,
				Sticky: e.Rev.Sticky,
				Terms:  e.Rev.Terms,
			}
		}
		r.Entries[id] = x
	}
	for id, d := range s.Terms {
		r.Terms[id] = &siteplan.Doc[content.Term]{File: named(d.File), V: content.Term{
			ID:               d.V.ID,
			Taxonomy:         d.V.Taxonomy,
			Language:         d.V.Language,
			TranslationGroup: d.V.TranslationGroup,
			Slug:             d.V.Slug,
			Parent:           d.V.Parent,
		}}
	}
	for id, d := range s.Media {
		r.Media[id] = &siteplan.Doc[content.Media]{File: named(d.File), V: content.Media{ID: d.V.ID, MIMEType: d.V.MIMEType}}
	}
	for id, d := range s.Authors {
		r.Authors[id] = &siteplan.Doc[content.AuthorProfile]{
			File: named(d.File), V: content.AuthorProfile{ID: d.V.ID, Slug: d.V.Slug},
		}
	}
	for id, d := range s.Menus {
		r.Menus[id] = &siteplan.Doc[content.Menu]{
			File: named(d.File), V: content.Menu{ID: d.V.ID, Language: d.V.Language, Location: d.V.Location},
		}
	}
	for id, d := range s.Forms {
		r.Forms[id] = &siteplan.Doc[content.Form]{File: named(d.File), V: content.Form{ID: d.V.ID}}
	}
	for id, d := range s.Redirects {
		r.Redirects[id] = &siteplan.Doc[content.Redirect]{
			File: named(d.File), V: content.Redirect{ID: d.V.ID, From: d.V.From, To: d.V.To, Status: d.V.Status},
		}
	}
	return r
}

// siteDump returns what siteplan makes of s as text, and the problems that it reports with the stages that find them:
// the lookups; the pages, the outputs and the paths of the plan; the redirects; and the problems of the checks across
// the documents, of the plan, of the redirects (the errors, and the warnings) and of the duplicates of the outputs.
// The stub of mediaPath makes "/media/<id>/", and fails for pdfID.
func siteDump(s *siteplan.Site) (dump string, found []string) {
	reportTo := func(stage string) siteplan.Report {
		return func(f content.SiteFile, pointer, msg string) {
			e := content.Error{File: f.Name, Path: pointer, Msg: msg}
			found = append(found, stage+" "+e.Error())
		}
	}
	mediaPath := func(id string) (string, error) {
		if id == pdfID {
			return "", errors.New("no original")
		}
		return "/media/" + id + "/", nil
	}
	var w strings.Builder
	ids := func(es []*siteplan.Entry) []string {
		out := make([]string, len(es))
		for i, e := range es {
			out[i] = e.E.ID
		}
		return out
	}

	var published []string
	for _, id := range slices.Sorted(maps.Keys(s.Entries)) {
		if e := s.Published(id); e != nil {
			published = append(published, e.E.ID)
		}
	}
	fmt.Fprintf(&w, "published %q\n", published)
	missing := map[content.RefKind]string{content.RefEntry: missingEntryID, content.RefTerm: missingTermID}
	docIDs := map[content.RefKind][]string{
		content.RefEntry:    slices.Sorted(maps.Keys(s.Entries)),
		content.RefTerm:     slices.Sorted(maps.Keys(s.Terms)),
		content.RefMedia:    slices.Sorted(maps.Keys(s.Media)),
		content.RefAuthor:   slices.Sorted(maps.Keys(s.Authors)),
		content.RefMenu:     slices.Sorted(maps.Keys(s.Menus)),
		content.RefForm:     slices.Sorted(maps.Keys(s.Forms)),
		content.RefRedirect: slices.Sorted(maps.Keys(s.Redirects)),
	}
	for _, kind := range kinds {
		for _, id := range append(docIDs[kind], missing[kind]) {
			ref, ok := s.Ref(kind, id)
			path, pathOK := s.PathRef(kind, id)
			fmt.Fprintf(&w, "ref %s %s %+v %t %+v %t\n", kind, id, ref, ok, path, pathOK)
		}
	}
	fmt.Fprintf(&w, "per page %d\n", s.PostsPerPage())
	members := s.Members()
	for _, k := range slices.SortedFunc(maps.Keys(members), func(a, b siteplan.ListKey) int {
		return strings.Compare(fmt.Sprintf("%d %s %s", a.Kind, a.Language, a.Of), fmt.Sprintf("%d %s %s", b.Kind, b.Language, b.Of))
	}) {
		fmt.Fprintf(&w, "list %d %s %s %q\n", k.Kind, k.Language, k.Of, ids(members.Sorted(k.Kind, k.Language, k.Of)))
	}

	s.CheckAcross(reportTo("across"))
	plan := s.Plan(reportTo("plan"))
	fmt.Fprintf(&w, "base %q %v\n", plan.Base, plan.Location)
	for _, pg := range plan.Pages {
		var entry, term, author string
		if pg.Entry != nil {
			entry = pg.Entry.E.ID
		}
		if pg.Term != nil {
			term = pg.Term.V.ID
		}
		if pg.Author != nil {
			author = pg.Author.V.ID
		}
		fmt.Fprintf(&w, "page %d %s %s %s %d/%d %s %t %s %s %s %s %q\n", pg.Kind, pg.Language, pg.Path, pg.File, pg.Number,
			pg.Total, pg.First, pg.Front, entry, term, author, pg.Type, ids(pg.Entries))
	}
	for _, o := range plan.Outputs {
		fmt.Fprintf(&w, "output %s %s %s %s %q %d\n", o.Path, o.File, o.Src.File.Name, o.Src.Pointer, o.Src.What, o.Rank)
	}
	fmt.Fprintln(&w, "paths", plan.EntryPaths, plan.TermPaths, plan.AuthorPaths)
	rs := s.ResolveRedirects(plan, mediaPath, reportTo("warning"), reportTo("redirects"))
	for _, text := range redirectTexts(rs) {
		fmt.Fprintf(&w, "redirect %s\n", text)
	}
	siteplan.CheckOutputs(plan.Outputs, rs, reportTo("outputs"))

	slices.Sort(found)
	return w.String(), found
}

// TestSummaryFields checks that siteplan gives the same problems, pages, outputs, paths and redirects for a site made of
// the documents as the build reads them and for the same site with only the fields that the doc comment of siteplan.Site
// lists, which is what the store's summaries have, on the fixture and on variants of it that break each rule.
func TestSummaryFields(t *testing.T) {
	const ideographicSpace = string(rune(0x3000))
	rdr := func(n int) string { return fileOf(content.RefRedirect, newID("rdr", n)) }
	cycle := func(ids ...string) string { return strings.Join(append(ids, ids[0]), " -> ") }
	site := func(t *testing.T, s *memSource, change func(*content.Site)) {
		t.Helper()
		x := testSite()
		change(x)
		s.putSettings(t, x)
	}
	tests := []struct {
		name   string
		change func(t *testing.T, s *memSource)
		want   []string // problems, with the stages, that the site must have; if none, it must have no problem
	}{
		{
			name: "fixture with 12 posts in each language",
			change: func(t *testing.T, s *memSource) {
				s.addPosts(t, "ja", 11)
				s.addPosts(t, "en", 11)
			},
		},
		{
			name: "pages under parents, and in the lists of the terms of a hierarchy",
			change: func(t *testing.T, s *memSource) {
				s.addPage(t, 1, "team", aboutID)
				s.addPage(t, 2, "team", servicesID)
				id := newID("ent", 3)
				s.putEntry(t, newEntry(id, 103, content.TypePost, "ja", groupOf(id)), content.Revision{
					Title:  "Award",
					Slug:   "award",
					Date:   "2026-10-27T09:00:00+09:00",
					Author: authorID,
					Terms:  content.Terms{content.TaxonomyCategory: {pressReleaseID, newsCategoryID}},
				})
			},
		},
		{
			name: "order of the lists: dates as instants, numbers and sticky posts",
			change: func(t *testing.T, s *memSource) {
				s.schema.Data = []byte(strings.Replace(testSchema, "    url: /{yyyy}/{mm}/{dd}/{slug}/\n",
					"    url: /{yyyy}/{mm}/{dd}/{slug}/\n    taxonomies: [works_category]\n", 1))
				for i, x := range []struct {
					language, date string
					number         int
					sticky         bool
				}{
					{"ja", "2026-10-25T16:00:00Z", 2, false},
					{"ja", "2026-10-25T15:30:00Z", 200, false},
					{"ja", "2026-10-25T06:30:00-09:00", 200, false},
					{"ja", "2026-10-26T00:30:00+09:00", 1, true},
					{"ja", "2026-01-01T00:00:00Z", 3, true},
					{"en", "2026-10-26T00:30:00+09:00", 1, false},
					{"en", "2026-01-01T00:00:00Z", 3, true},
				} {
					id := newID("ent", i+1)
					s.putEntry(t, newEntry(id, x.number, content.TypePost, x.language, groupOf(id)), content.Revision{
						Title:  fmt.Sprint("Post ", i+1),
						Slug:   fmt.Sprint("post-", i+1),
						Date:   x.date,
						Author: authorID,
						Sticky: x.sticky,
					})
				}
				for i, createdAt := range []string{"2026-10-20T01:30:00Z", "2026-10-19T00:00:00Z"} {
					id := newID("ent", 10+i)
					e := newEntry(id, 10+i, "works", "ja", groupOf(id))
					e.CreatedAt = createdAt
					s.putEntry(t, e, content.Revision{
						Title:  fmt.Sprint("Works ", i+1),
						Slug:   fmt.Sprint("works-", i+1),
						Terms:  content.Terms{"works_category": {worksCategoryID}},
						Fields: content.Fields{"client": []byte(`"〇〇株式会社"`)},
					})
				}
			},
		},
		{
			name: "prefixDefaultLanguage, 3 posts on a page and a redirect to an author",
			change: func(t *testing.T, s *memSource) {
				site(t, s, func(x *content.Site) { x.PrefixDefaultLanguage, x.PostsPerPage = true, 3 })
				s.addPosts(t, "ja", 5)
				s.addRedirect(t, 1, "/yamada/", authorID)
			},
		},
		{
			name: "front pages in ja and en",
			change: func(t *testing.T, s *memSource) {
				site(t, s, func(x *content.Site) { x.FrontPage["en"] = enAboutID })
			},
		},
		{
			name: "latest posts at the front, under a path, with noindex",
			change: func(t *testing.T, s *memSource) {
				site(t, s, func(x *content.Site) {
					x.FrontPage, x.PostsPage, x.URL, x.SEO.NoIndex = nil, nil, "https://example.com/blog", true
				})
				s.addPosts(t, "ja", 11)
			},
		},
		{
			name: "theme with assets and without 404.html",
			change: func(t *testing.T, s *memSource) {
				fsys := testTheme()
				delete(fsys, "templates/404.html")
				fsys["assets/css/style.css"] = &fstest.MapFile{Data: []byte("body { margin: 0; }\n")}
				s.theme = fsys
			},
		},
		{
			name: "redirects to entries, terms, authors, media, paths and URLs",
			change: func(t *testing.T, s *memSource) {
				for i, x := range []struct{ from, to string }{
					{"/blog/", newsID},
					{"/draft/", draftID},
					{"/old-news/", newsCategoryID},
					{"/top/", homeID},
					{"/yamada/", authorID},
					{"/wp-content/uploads/2024/05/office.jpg", imageID},
					{"/wp-content/uploads/2024/05/guide.pdf", pdfID},
					{"/inquiry/", "/contact/"},
					{"/partner/", "https://example.org/"},
				} {
					s.addRedirect(t, i+1, x.from, x.to)
				}
			},
			want: []string{
				"warning " + rdr(2) + `: /to: entry "` + draftID + `" is not published; the redirect is left out`,
				"redirects " + rdr(7) + ": /to: cannot make the path: no original",
			},
		},
		{
			name: "slugs of pages",
			change: func(t *testing.T, s *memSource) {
				s.editRevision(t, servicesID, func(r *content.Revision) { r.Slug = "about" })
			},
			want: []string{
				"across " + revisionFile(servicesID) + `: /slug: slug "about" is also used by ` + revisionFile(aboutID),
				"outputs " + revisionFile(servicesID) + `: /slug: output path "/about/" is also that of ` + revisionFile(aboutID),
			},
		},
		{
			name: "slugs of terms and authors",
			change: func(t *testing.T, s *memSource) {
				x := newTerm(1, content.TaxonomyTag, "ja", "イベント")
				s.put(t, content.RefTerm, x.ID, x)
				id := newID("aut", 1)
				s.put(t, content.RefAuthor, id, content.AuthorProfile{ID: id, Name: "山田", Slug: "yamada"})
			},
			want: []string{
				"across " + fileOf(content.RefTerm, newID("trm", 1)) + `: /slug: slug "イベント" is also used by ` +
					fileOf(content.RefTerm, eventTagID),
				"across " + fileOf(content.RefAuthor, newID("aut", 1)) + `: /slug: slug "yamada" is also used by ` +
					fileOf(content.RefAuthor, authorID),
			},
		},
		{
			name: "translation groups of entries and terms",
			change: func(t *testing.T, s *memSource) {
				edit(t, s, content.RefEntry, draftID, func(e *content.Entry) { e.TranslationGroup = aboutGroup })
				x := newTerm(1, content.TaxonomyTag, "ja", "催し")
				x.TranslationGroup = eventGroup
				s.put(t, content.RefTerm, x.ID, x)
			},
			want: []string{
				"across " + fileOf(content.RefEntry, draftID) + `: /translationGroup: translation group "` + aboutGroup +
					`" already has an entry in the language "ja": ` + fileOf(content.RefEntry, aboutID),
				"across " + fileOf(content.RefTerm, newID("trm", 1)) + `: /translationGroup: translation group "` + eventGroup +
					`" already has a term in the language "ja": ` + fileOf(content.RefTerm, eventTagID),
			},
		},
		{
			name: "menus at the same location",
			change: func(t *testing.T, s *memSource) {
				edit(t, s, content.RefMenu, footerID, func(m *content.Menu) { m.Location = "header" })
			},
			want: []string{
				"across " + fileOf(content.RefMenu, footerID) + `: /location: location "header" already has a menu in the language "ja": ` +
					fileOf(content.RefMenu, headerID),
			},
		},
		{
			name: "a parent that is not published",
			change: func(t *testing.T, s *memSource) {
				s.editRevision(t, servicesID, func(r *content.Revision) { r.Parent = draftID })
			},
			want: []string{"across " + revisionFile(servicesID) + `: /parent: entry "` + draftID + `" is not published`},
		},
		{
			name: "cycles of pages and of terms",
			change: func(t *testing.T, s *memSource) {
				s.editRevision(t, aboutID, func(r *content.Revision) { r.Parent = servicesID })
				s.editRevision(t, servicesID, func(r *content.Revision) { r.Parent = aboutID })
				edit(t, s, content.RefTerm, newsCategoryID, func(x *content.Term) { x.Parent = pressReleaseID })
			},
			want: []string{
				"across " + revisionFile(aboutID) + ": /parent: the parents make a cycle: " + cycle(aboutID, servicesID),
				"across " + revisionFile(servicesID) + ": /parent: the parents make a cycle: " + cycle(aboutID, servicesID),
				"across " + fileOf(content.RefTerm, newsCategoryID) + ": /parent: the parents make a cycle: " +
					cycle(newsCategoryID, pressReleaseID),
			},
		},
		{
			name: "froms that match the same requests",
			change: func(t *testing.T, s *memSource) {
				s.addRedirect(t, 1, "/old/", "/new/")
				s.addRedirect(t, 2, "/old", "/new/")
				s.addRedirect(t, 3, "/?p=123", "/new/")
			},
			want: []string{
				"across " + rdr(2) + `: /from: from "/old" matches the same requests as that of ` + rdr(1),
				"across " + rdr(3) + `: /from: from "/?p=123" matches the same requests as that of ` +
					fileOf(content.RefRedirect, redirectID),
			},
		},
		{
			name: "loop of redirects in other forms",
			change: func(t *testing.T, s *memSource) {
				s.addRedirect(t, 1, "/会社/", "/%E6%A6%82%E8%A6%81/")
				s.addRedirect(t, 2, "/概要", "/a"+ideographicSpace+"b/")
				s.addRedirect(t, 3, "/a%E3%80%80b/", "/%e4%bc%9a%e7%a4%be")
				s.addRedirect(t, 4, "/a", "/b?x=1")
				s.addRedirect(t, 5, "/b?x=1", "/a")
			},
			want: []string{
				"across " + rdr(1) + ": /to: redirect loop: " + cycle(newID("rdr", 1), newID("rdr", 2), newID("rdr", 3)),
				"across " + rdr(4) + ": /to: redirect loop: " + cycle(newID("rdr", 4), newID("rdr", 5)),
			},
		},
		{
			name: "front page and posts page not published",
			change: func(t *testing.T, s *memSource) {
				site(t, s, func(x *content.Site) { x.FrontPage["ja"] = draftID })
				edit(t, s, content.RefEntry, newsID, func(e *content.Entry) { e.Status, e.PublishedRevision = content.StatusArchived, "" })
			},
			want: []string{
				"across site/settings.json: /frontPage/ja: entry \"" + draftID + `" is not published`,
				"across site/settings.json: /postsPage/ja: entry \"" + newsID + `" is not published`,
			},
		},
		{
			name: "pages at the paths of a feed and of the list of a type",
			change: func(t *testing.T, s *memSource) {
				s.addPage(t, 1, "feed", "")
				s.addPage(t, 2, "works", "")
			},
			want: []string{
				"outputs " + revisionFile(newID("ent", 1)) + `: /slug: output path "/feed/" is also that of the feed in ja`,
				"outputs " + revisionFile(newID("ent", 2)) + `: /slug: output path "/works/" is also that of the list of the type "works" in ja`,
			},
		},
		{
			name: "redirects from the paths of outputs",
			change: func(t *testing.T, s *memSource) {
				s.addRedirect(t, 1, "/about", "/contact/")
				s.addRedirect(t, 2, "/en/feed", "/en/")
				s.addRedirect(t, 3, "/_forms/"+formID+"/", "/contact/")
				s.addRedirect(t, 4, "/about/?ref=top", "/contact/")
			},
			want: []string{
				"outputs " + rdr(1) + `: /from: from "/about" is the output path "/about/" of ` + revisionFile(aboutID),
				"outputs " + rdr(2) + `: /from: from "/en/feed" is the output path "/en/feed/" of the feed in en`,
				"outputs " + rdr(3) + `: /from: from "/_forms/` + formID + `/" is the output path "/_forms/` + formID + `" of ` +
					fileOf(content.RefForm, formID),
			},
		},
		{
			name: "paths that cannot be made",
			change: func(t *testing.T, s *memSource) {
				literal, archive := strings.Repeat("x", 60), strings.Repeat("w", 256)
				s.schema.Data = []byte(strings.NewReplacer(
					"url: /{yyyy}/{mm}/{dd}/{slug}/", "url: /{yyyy}/{mm}/{dd}/{slug}-"+literal+"/",
					"archive: /works/", "archive: /"+archive+"/",
				).Replace(testSchema) + "  tag:\n    url: /tag/{slug}-" + literal + "/\nauthorUrl: /author/{slug}-" + literal + "/\n")
				s.editRevision(t, postID, func(r *content.Revision) { r.Slug = strings.Repeat("あ", 66) })
				edit(t, s, content.RefTerm, enEventTagID, func(x *content.Term) { x.Slug = strings.Repeat("e", 200) })
				edit(t, s, content.RefAuthor, authorID, func(a *content.AuthorProfile) { a.Slug = strings.Repeat("y", 200) })
			},
			want: []string{
				"plan " + revisionFile(postID) + ": /slug: cannot make the path: invalid path " +
					fmt.Sprintf("%q", "/2026/10/26/"+strings.Repeat("あ", 66)+"-"+strings.Repeat("x", 60)+"/") +
					": segment too long: 259 bytes (max 255)",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := testSource(t)
			tt.change(t, src)
			b := decodeSite(t, src)
			dump, found := siteDump(b.sp)
			gotDump, gotFound := siteDump(summaries(b.sp))
			if gotDump != dump {
				t.Errorf("the summaries give:\n%s\nthe documents give:\n%s", gotDump, dump)
			}
			if !slices.Equal(gotFound, found) {
				t.Errorf("problems of the summaries:\n%s\nof the documents:\n%s",
					strings.Join(gotFound, "\n"), strings.Join(found, "\n"))
			}
			if tt.want == nil && len(found) > 0 {
				t.Errorf("problems:\n%s\nwant none", strings.Join(found, "\n"))
			}
			for _, want := range tt.want {
				if !slices.Contains(found, want) {
					t.Errorf("problems:\n%s\nwant the problem:\n%s", strings.Join(found, "\n"), want)
				}
			}
		})
	}
}

// TestSiteplanModel checks that the site that the build gives siteplan is made of its own documents: the maps are the
// same, the entries are those of the builder, and the pages have them.
func TestSiteplanModel(t *testing.T) {
	src := testSource(t)
	src.addPosts(t, "ja", 11)
	b := mustLoad(t, src)
	if b.sp.Site != b.site || b.sp.Theme != b.theme || b.sp.Settings.Name != b.settings.Name || b.sp.Schema.Name != b.schema.Name {
		t.Errorf("the site of siteplan has the settings %+v and the theme %+v", b.sp.Site, b.sp.Theme)
	}
	if len(b.sp.Entries) != len(b.entries) {
		t.Errorf("siteplan has %d entries, the builder %d", len(b.sp.Entries), len(b.entries))
	}
	for id, d := range b.entries {
		if b.sp.Entries[id] != &d.Entry {
			t.Errorf("the entry %s of siteplan is not that of the builder", id)
		}
		if got, want := b.sp.Published(id) != nil, b.published(id) != nil; got != want || want != (d.Rev != nil) {
			t.Errorf("Published(%s) = %t, published() = %t, the revision %v", id, got, want, d.Rev)
		}
	}
	for _, x := range []struct {
		name      string
		got, want any
	}{
		{"terms", b.sp.Terms, b.terms},
		{"media", b.sp.Media, b.media},
		{"authors", b.sp.Authors, b.authors},
		{"menus", b.sp.Menus, b.menus},
		{"forms", b.sp.Forms, b.forms},
		{"redirects", b.sp.Redirects, b.redirects},
	} {
		if reflect.ValueOf(x.got).UnsafePointer() != reflect.ValueOf(x.want).UnsafePointer() {
			t.Errorf("the %s of siteplan are not those of the builder", x.name)
		}
	}
	for _, pg := range b.pages {
		if pg.entry != nil && pg.entry != b.entries[pg.entry.E.ID] {
			t.Errorf("the page at %q has an entry that is not that of the builder", pg.path)
		}
		for _, d := range pg.entries {
			if d != b.entries[d.E.ID] {
				t.Errorf("the page at %q lists an entry that is not that of the builder", pg.path)
			}
		}
	}
}
