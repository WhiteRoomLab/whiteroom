package build

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/sitedir"
)

var _ Source = (*sitedir.Dir)(nil)

// IDs of entities that the fixture does not have.
const (
	missingEntryID    = "ent_01JCZZZZZZZZZZZZZZZZZZZZZZ"
	missingRevisionID = "rev_01JCZZZZZZZZZZZZZZZZZZZZZZ"
	missingTermID     = "trm_01JCZZZZZZZZZZZZZZZZZZZZZZ"
)

func mustLoad(t *testing.T, src Source) *builder {
	t.Helper()
	b, err := load(src)
	if err != nil {
		t.Fatalf("load() error:\n%v", err)
	}
	return b
}

// errorTexts returns the Error() of each problem in err, which must be content.Errors.
func errorTexts(t *testing.T, err error) []string {
	t.Helper()
	var es content.Errors
	if !errors.As(err, &es) {
		t.Fatalf("error %q (%T) is not content.Errors", err, err)
	}
	texts := make([]string, len(es))
	for i, e := range es {
		texts[i] = e.Error()
	}
	return texts
}

func TestLoad(t *testing.T) {
	src := testSource(t)
	jaPosts := src.addPosts(t, "ja", 11)
	enPosts := src.addPosts(t, "en", 11)
	b := mustLoad(t, src)
	if n := b.warnings.len(); n != 0 {
		t.Errorf("load() made %d warnings:\n%v", n, b.warnings.sorted())
	}
	if b.site.Type("works") == nil || len(b.site.Languages) != 2 || len(b.theme.Settings.Locations) != 2 {
		t.Errorf("load() has the site %+v and the theme settings %+v", b.site, b.theme.Settings)
	}

	for _, tt := range []struct {
		kind content.RefKind
		id   string
		want content.Ref
		ok   bool
	}{
		{content.RefEntry, aboutID, content.Ref{Type: content.TypePage, Language: "ja"}, true},
		{content.RefEntry, enPostID, content.Ref{Type: content.TypePost, Language: "en"}, true},
		{content.RefEntry, worksID, content.Ref{Type: "works", Language: "ja"}, true},
		{content.RefEntry, draftID, content.Ref{Type: content.TypePage, Language: "ja"}, true},
		{content.RefEntry, enPosts[10], content.Ref{Type: content.TypePost, Language: "en"}, true},
		{content.RefEntry, missingEntryID, content.Ref{}, false},
		{content.RefEntry, eventTagID, content.Ref{}, false},
		{content.RefTerm, pressReleaseID, content.Ref{Type: content.TaxonomyCategory, Language: "ja"}, true},
		{content.RefTerm, enEventTagID, content.Ref{Type: content.TaxonomyTag, Language: "en"}, true},
		{content.RefTerm, worksCategoryID, content.Ref{Type: "works_category", Language: "ja"}, true},
		{content.RefMedia, imageID, content.Ref{Type: content.MediaImage}, true},
		{content.RefMedia, pdfID, content.Ref{Type: content.MediaPDF}, true},
		{content.RefForm, formID, content.Ref{}, true},
		{content.RefAuthor, authorID, content.Ref{}, true},
		{content.RefMenu, headerID, content.Ref{}, false},
		{content.RefRedirect, redirectID, content.Ref{}, false},
		{content.RefRevision, revisionID(aboutID), content.Ref{}, false},
	} {
		if got, ok := b.ref(tt.kind, tt.id); got != tt.want || ok != tt.ok {
			t.Errorf("ref(%s, %s) = %+v, %t; want %+v, %t", tt.kind, tt.id, got, ok, tt.want, tt.ok)
		}
	}

	for _, tt := range []struct {
		kind content.RefKind
		id   string
		want content.PathRef
		ok   bool
	}{
		{content.RefEntry, homeID, content.PathRef{Slug: "home"}, true},
		{content.RefEntry, aboutID, content.PathRef{Slug: "about"}, true},
		{content.RefEntry, enPrivacyID, content.PathRef{Slug: "privacy"}, true},
		{content.RefEntry, postID, content.PathRef{}, false},
		{content.RefEntry, worksID, content.PathRef{}, false},
		{content.RefEntry, draftID, content.PathRef{}, false},
		{content.RefEntry, missingEntryID, content.PathRef{}, false},
		{content.RefTerm, pressReleaseID, content.PathRef{Slug: "press-release", Parent: newsCategoryID}, true},
		{content.RefTerm, eventTagID, content.PathRef{Slug: "イベント"}, true},
		{content.RefTerm, missingTermID, content.PathRef{}, false},
		{content.RefMedia, imageID, content.PathRef{}, false},
	} {
		if got, ok := b.pathRef(tt.kind, tt.id); got != tt.want || ok != tt.ok {
			t.Errorf("pathRef(%s, %s) = %+v, %t; want %+v, %t", tt.kind, tt.id, got, ok, tt.want, tt.ok)
		}
	}

	if n := len(b.entries); n != 12+22 {
		t.Errorf("load() has %d entries, want %d", n, 12+22)
	}
	var revisions []string
	for id, d := range b.entries {
		if id == draftID {
			if d.rev != nil || b.published(id) != nil {
				t.Errorf("the draft %s has the revision %+v", id, d.rev)
			}
			continue
		}
		revisions = append(revisions, revisionID(id))
		if b.published(id) != d || d.rev == nil || d.revFile.Name != "revisions/"+revisionID(id)+".json" {
			t.Errorf("the published entry %s has the revision %+v in %q", id, d.rev, d.revFile.Name)
		}
	}
	if got, want := slices.Sorted(slices.Values(src.read)), slices.Sorted(slices.Values(revisions)); !slices.Equal(got, want) {
		t.Errorf("load() read the revisions %q, want %q", got, want)
	}
	if d := b.published(jaPosts[0]); d == nil || d.rev.Slug != "post-1001" || d.rev.Date != "2026-09-01T09:00:00+09:00" {
		t.Errorf("the first post added has %+v", d)
	}
	if d := b.published(postID); d == nil || d.rev.Slug != "新サービス" || d.e.Number != 123 {
		t.Errorf("the post of 6.11.9 has %+v", d)
	}
	media := []string{imageID, pdfID, avatarID, logoID, iconID, ogImageID}
	if got, want := slices.Sorted(maps.Keys(b.media)), slices.Sorted(slices.Values(media)); !slices.Equal(got, want) {
		t.Errorf("load() has the media %q, want %q", got, want)
	}
	got := [5]int{len(b.terms), len(b.authors), len(b.menus), len(b.forms), len(b.redirects)}
	if want := [5]int{5, 1, 2, 1, 1}; got != want {
		t.Errorf("load() has %v terms, authors, menus, forms and redirects, want %v", got, want)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name    string
		change  func(t *testing.T, s *memSource)
		want    []string // Error() of each error
		notRead string   // a revision that must not be read
	}{
		{
			name: "decoding stops before the settings",
			change: func(t *testing.T, s *memSource) {
				s.putRaw(content.RefTerm, eventTagID, "{\n  \"schemaVersion\": 1,\n  \"id\": \""+eventTagID+"\",\n  \"label\": \"イベント\"\n}\n")
				s.putRaw(content.RefEntry, homeID, "{\n  \"schemaVersion\": 1,\n  \"id\": \""+homeID+"\"\n  \"number\": 1\n}\n")
				site := testSite()
				site.FrontPage["ja"] = missingEntryID
				s.putSettings(t, site)
			},
			want: []string{
				"entries/" + homeID + `.json:4:3: invalid character '"' after object value (expecting ',' or '}')`,
				"terms/" + eventTagID + ".json:4:3: /label: unknown key",
			},
		},
		{
			name: "id differs",
			change: func(t *testing.T, s *memSource) {
				edit(t, s, content.RefEntry, aboutID, func(e *content.Entry) { e.ID = enAboutID })
				edit(t, s, content.RefRedirect, redirectID, func(r *content.Redirect) { r.ID = "rdr_01JC0000000000000000000001" })
			},
			want: []string{
				"entries/" + aboutID + `.json:3:3: /id: id "` + enAboutID + `" differs from the ID of the document, "` + aboutID + `"`,
				"redirects/" + redirectID + `.json:3:3: /id: id "rdr_01JC0000000000000000000001" differs from the ID of the document, "` +
					redirectID + `"`,
			},
		},
		{
			name: "settings",
			change: func(t *testing.T, s *memSource) {
				site := testSite()
				site.FrontPage["ja"] = missingEntryID
				s.putSettings(t, site)
			},
			want: []string{`site/settings.json:20:5: /frontPage/ja: entry "` + missingEntryID + `" does not exist`},
		},
		{
			name: "theme after the settings",
			change: func(t *testing.T, s *memSource) {
				site := testSite()
				site.PostsPerPage = 1000
				s.putSettings(t, site)
				fsys := testTheme()
				fsys["theme.yaml"].Data = []byte("schemaVersion: 1\nlocation:\n  header: ヘッダー\n")
				delete(fsys, "templates/list.html")
				s.theme = fsys
			},
			want: []string{
				"site/settings.json:25:3: /postsPerPage: postsPerPage 1000 is out of range (1 to 100)",
				"theme/theme.yaml:2:1: /location: unknown key",
				"theme/templates/list.html: missing file; every theme needs entry.html and list.html",
			},
		},
		{
			name: "missing revision",
			change: func(t *testing.T, s *memSource) {
				edit(t, s, content.RefEntry, servicesID, func(e *content.Entry) { e.PublishedRevision = missingRevisionID })
			},
			want: []string{"entries/" + servicesID + `.json:9:3: /publishedRevision: revision "` + missingRevisionID + `" does not exist`},
		},
		{
			name: "invalid entry",
			change: func(t *testing.T, s *memSource) {
				edit(t, s, content.RefEntry, contactID, func(e *content.Entry) { e.Type = "product" })
			},
			want:    []string{"entries/" + contactID + `.json:5:3: /type: unknown type "product"`},
			notRead: revisionID(contactID),
		},
		{
			name: "revisions",
			change: func(t *testing.T, s *memSource) {
				s.putRevisionRaw(revisionID(newsID), "{\n  \"schemaVersion\": 1,\n  \"title\": 1\n}\n")
				s.editRevision(t, servicesID, func(r *content.Revision) { r.Title = "" })
				s.editRevision(t, postID, func(r *content.Revision) { r.Terms[content.TaxonomyTag] = []string{enEventTagID} })
			},
			want: []string{
				"revisions/" + revisionID(servicesID) + ".json:1:1: /title: missing title",
				"revisions/" + revisionID(postID) + `.json:12:7: /terms/tag/0: term "` + enEventTagID + `" is in the language "en", not "ja"`,
				"revisions/" + revisionID(newsID) + ".json:3:3: /title: expected string, got number",
			},
		},
		{
			name: "documents of the other kinds",
			change: func(t *testing.T, s *memSource) {
				edit(t, s, content.RefTerm, pressReleaseID, func(x *content.Term) { x.Parent = missingTermID })
				edit(t, s, content.RefMedia, logoID, func(m *content.Media) { m.Width = 0 })
				edit(t, s, content.RefAuthor, authorID, func(a *content.AuthorProfile) { a.Slug = "Yamada" })
				edit(t, s, content.RefMenu, headerID, func(m *content.Menu) { m.Location = "header menu" })
				edit(t, s, content.RefForm, formID, func(f *content.Form) { f.Title = "" })
				edit(t, s, content.RefRedirect, redirectID, func(r *content.Redirect) { r.To = missingEntryID })
			},
			want: []string{
				"authors/" + authorID + ".json:5:3: /slug: contains upper case letters; normalize it first",
				"forms/" + formID + ".json:1:1: /title: missing title",
				"media/" + logoID + ".json:1:1: /width: missing width",
				"menus/" + headerID + `.json:5:3: /location: invalid name "header menu": use ASCII letters, digits, "-" and "_"`,
				"redirects/" + redirectID + `.json:5:3: /to: entry "` + missingEntryID + `" does not exist`,
				"terms/" + pressReleaseID + `.json:9:3: /parent: term "` + missingTermID + `" does not exist`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := testSource(t)
			tt.change(t, src)
			b, err := load(src)
			if b != nil {
				t.Errorf("load() = %+v with an error", b)
			}
			if got := errorTexts(t, err); !slices.Equal(got, tt.want) {
				t.Errorf("errors:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
			if tt.notRead != "" && slices.Contains(src.read, tt.notRead) {
				t.Errorf("load() read the revision %s", tt.notRead)
			}
		})
	}
}

// TestLoadImport checks that revisions are validated as imports without the theme: legacy blocks, images without alt,
// and styles and templates that the theme lacks pass.
func TestLoadImport(t *testing.T) {
	src := testSource(t)
	src.editRevision(t, aboutID, func(r *content.Revision) {
		r.Body = append(r.Body,
			content.Block{Type: content.BlockLegacy, ID: "b_legacy01", Attrs: content.Attrs{HTML: `[gallery ids="1,2"]`, Reason: "shortcode gallery"}},
			content.Block{Type: content.BlockImage, ID: "b_noalt001", Attrs: content.Attrs{Media: imageID}},
			content.Block{
				Type:  content.BlockParagraph,
				ID:    "b_styled01",
				Style: content.Style{TextColor: content.Color{Preset: "primary"}},
				Text:  richText("赤い文字"),
			},
		)
	})
	b := mustLoad(t, src)
	if r := b.published(aboutID).rev; len(r.Body) != 7 || r.Template != "standard" {
		t.Errorf("the about page has the revision %+v", r)
	}
}

// TestLoadUnpublished checks that the entries that are not published are neither validated nor have their revisions
// read.
func TestLoadUnpublished(t *testing.T) {
	src := testSource(t)
	edit(t, src, content.RefEntry, draftID, func(e *content.Entry) {
		e.Type, e.PublishedRevision = "product", missingRevisionID
	})
	for i, status := range []content.Status{content.StatusDraft, content.StatusScheduled, content.StatusArchived} {
		id := fmt.Sprintf("ent_01JE%022d", i+1)
		e := newEntry(id, 20+i, content.TypePage, "ja", groupOf(id))
		e.Status = status
		src.putEntry(t, e, content.Revision{})
		src.putRevisionRaw(e.PublishedRevision, "{")
	}
	b := mustLoad(t, src)
	for id, d := range b.entries {
		if d.e.Status != content.StatusPublished && d.rev != nil {
			t.Errorf("the entry %s (%s) has a revision", id, d.e.Status)
		}
	}
	for _, id := range src.read {
		if d := b.entries["ent_"+strings.TrimPrefix(id, "rev_")]; d == nil || d.e.Status != content.StatusPublished {
			t.Errorf("load() read the revision %s", id)
		}
	}
}

func TestLoadSourceErrors(t *testing.T) {
	errBoom := errors.New("boom")
	for _, method := range []string{"Settings", "Documents", "Revision", "Theme"} {
		t.Run(method, func(t *testing.T) {
			src := testSource(t)
			src.fail = map[string]error{method: errBoom}
			b, err := load(src)
			if b != nil || !errors.Is(err, errBoom) || err.Error() != errBoom.Error() {
				t.Errorf("load() = %+v, %v; want nil, %v", b, err, errBoom)
			}
		})
	}
}

// TestLoadSiteDir checks that the fixture written in a site directory loads through internal/sitedir as it does from
// memory.
func TestLoadSiteDir(t *testing.T) {
	src := testSource(t)
	src.addPosts(t, "ja", 2)
	want := mustLoad(t, src)
	d, err := sitedir.Open(writeSiteDir(t, src))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	got := mustLoad(t, d)
	for _, x := range []struct {
		name      string
		got, want any
	}{
		{"settings", got.settings, want.settings},
		{"schema", got.schema, want.schema},
		{"site", got.site, want.site},
		{"theme settings", got.theme.Settings, want.theme.Settings},
		{"entries", got.entries, want.entries},
		{"terms", got.terms, want.terms},
		{"media", got.media, want.media},
		{"authors", got.authors, want.authors},
		{"menus", got.menus, want.menus},
		{"forms", got.forms, want.forms},
		{"redirects", got.redirects, want.redirects},
	} {
		if !reflect.DeepEqual(x.got, x.want) {
			t.Errorf("%s through sitedir = %+v, want %+v", x.name, x.got, x.want)
		}
	}
}
