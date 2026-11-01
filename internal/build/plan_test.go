package build

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// pageAt returns the page of b at path, or nil.
func pageAt(b *builder, path string) *page {
	for _, pg := range b.pages {
		if pg.path == path {
			return pg
		}
	}
	return nil
}

// outputAt returns the output of b at path, or nil.
func outputAt(b *builder, path string) *output {
	for i := range b.outputs {
		if b.outputs[i].path == path {
			return &b.outputs[i]
		}
	}
	return nil
}

// outputFiles returns the file of every output of b by its path, reporting the paths that more than one output has.
func outputFiles(t *testing.T, b *builder) map[string]string {
	t.Helper()
	files := make(map[string]string, len(b.outputs))
	for _, o := range b.outputs {
		if _, ok := files[o.path]; ok {
			t.Errorf("more than one output at %q", o.path)
		}
		files[o.path] = o.file
	}
	return files
}

// entryIDs returns the IDs of es.
func entryIDs(es []*entryDoc) []string {
	ids := make([]string, len(es))
	for i, d := range es {
		ids[i] = d.e.ID
	}
	return ids
}

// newestFirst returns first and then ids, which addPosts has returned, in reverse order: newest first.
func newestFirst(first string, ids []string) []string {
	r := slices.Clone(ids)
	slices.Reverse(r)
	return append([]string{first}, r...)
}

// TestPlan checks the pages and the other outputs of the fixture with 12 posts in each language, among them the paths
// of the table in 6.11.9 of the concept (docs/06.11-examples.md).
func TestPlan(t *testing.T) {
	src := testSource(t)
	jaPosts := src.addPosts(t, "ja", 11)
	enPosts := src.addPosts(t, "en", 11)
	b := mustLoad(t, src)

	files := map[string]string{
		"/":                               "index.html",
		"/about/":                         "about/index.html",
		"/services/":                      "services/index.html",
		"/contact/":                       "contact/index.html",
		"/privacy/":                       "privacy/index.html",
		"/news/":                          "news/index.html",
		"/news/page/2/":                   "news/page/2/index.html",
		"/2026/10/26/新サービス/":              "2026/10/26/新サービス/index.html",
		"/works/example-corporate-site/":  "works/example-corporate-site/index.html",
		"/en/":                            "en/index.html",
		"/en/page/2/":                     "en/page/2/index.html",
		"/en/about/":                      "en/about/index.html",
		"/en/privacy/":                    "en/privacy/index.html",
		"/en/2026/10/26/new-service/":     "en/2026/10/26/new-service/index.html",
		"/category/news/":                 "category/news/index.html",
		"/category/news/page/2/":          "category/news/page/2/index.html",
		"/category/news/press-release/":   "category/news/press-release/index.html",
		"/tag/イベント/":                      "tag/イベント/index.html",
		"/en/tag/events/":                 "en/tag/events/index.html",
		"/en/tag/events/page/2/":          "en/tag/events/page/2/index.html",
		"/works_category/corporate-site/": "works_category/corporate-site/index.html",
		"/works/":                         "works/index.html",
		"/en/works/":                      "en/works/index.html",
		"/author/yamada/":                 "author/yamada/index.html",
		"/author/yamada/page/2/":          "author/yamada/page/2/index.html",
		"/en/author/yamada/":              "en/author/yamada/index.html",
		"/en/author/yamada/page/2/":       "en/author/yamada/page/2/index.html",
		"/404.html":                       "404.html",
		"/en/404.html":                    "en/404.html",
		"/feed/":                          "feed/index.xml",
		"/en/feed/":                       "en/feed/index.xml",
		"/sitemap.xml":                    "sitemap.xml",
		"/css/site.css":                   "css/site.css",
		"/.htaccess":                      ".htaccess",
		"/_forms/" + formID:               "",
	}
	for i := range 11 {
		path := fmt.Sprintf("/2026/09/%02d/post-%d/", i+1, 1001+i)
		files[path] = path[1:] + "index.html"
		path = fmt.Sprintf("/en/2026/09/%02d/post-%d/", i+1, 1012+i)
		files[path] = path[1:] + "index.html"
	}
	if got := outputFiles(t, b); !maps.Equal(got, files) {
		t.Errorf("outputs:\n%v\nwant:\n%v", got, files)
	}
	// Apart from the feeds, the sitemap, the site CSS, .htaccess and the form.
	if n := len(b.pages); n != len(files)-6 {
		t.Errorf("plan() made %d pages, want %d", n, len(files)-6)
	}
	if pg := pageAt(b, "/home/"); pg != nil || outputAt(b, "/home/") != nil {
		t.Errorf("the front page is also at /home/")
	}

	ja, en := newestFirst(postID, jaPosts), newestFirst(enPostID, enPosts)
	for _, tt := range []struct {
		path          string
		kind          theme.Kind
		language      string
		number, total int
		front         bool
		entry         string   // the ID of the entry of the page; "" for none
		entries       []string // on the page
	}{
		{path: "/2026/10/26/新サービス/", kind: theme.KindEntry, language: "ja", number: 1, total: 1, entry: postID},
		{path: "/about/", kind: theme.KindEntry, language: "ja", number: 1, total: 1, entry: aboutID},
		{path: "/", kind: theme.KindEntry, language: "ja", number: 1, total: 1, front: true, entry: homeID},
		{path: "/news/", kind: theme.KindPosts, language: "ja", number: 1, total: 2, entry: newsID, entries: ja[:10]},
		{path: "/news/page/2/", kind: theme.KindPosts, language: "ja", number: 2, total: 2, entry: newsID, entries: ja[10:]},
		{path: "/en/", kind: theme.KindPosts, language: "en", number: 1, total: 2, front: true, entries: en[:10]},
		{path: "/en/page/2/", kind: theme.KindPosts, language: "en", number: 2, total: 2, front: true, entries: en[10:]},
		{path: "/en/about/", kind: theme.KindEntry, language: "en", number: 1, total: 1, entry: enAboutID},
		{path: "/category/news/", kind: theme.KindTerm, language: "ja", number: 1, total: 2, entries: ja[:10]},
		{path: "/category/news/page/2/", kind: theme.KindTerm, language: "ja", number: 2, total: 2, entries: ja[10:]},
		{path: "/category/news/press-release/", kind: theme.KindTerm, language: "ja", number: 1, total: 1, entries: []string{postID}},
		{path: "/tag/イベント/", kind: theme.KindTerm, language: "ja", number: 1, total: 1, entries: []string{postID}},
		{path: "/en/tag/events/", kind: theme.KindTerm, language: "en", number: 1, total: 2, entries: en[:10]},
		{path: "/author/yamada/", kind: theme.KindAuthor, language: "ja", number: 1, total: 2, entries: ja[:10]},
		{path: "/en/author/yamada/", kind: theme.KindAuthor, language: "en", number: 1, total: 2, entries: en[:10]},
		{path: "/en/author/yamada/page/2/", kind: theme.KindAuthor, language: "en", number: 2, total: 2, entries: en[10:]},
		{path: "/works/", kind: theme.KindArchive, language: "ja", number: 1, total: 1, entries: []string{worksID}},
		{path: "/en/works/", kind: theme.KindArchive, language: "en", number: 1, total: 1, entries: []string{}},
		{path: "/works/example-corporate-site/", kind: theme.KindEntry, language: "ja", number: 1, total: 1, entry: worksID},
		{path: "/404.html", kind: theme.KindNotFound, language: "ja", number: 1, total: 1},
		{path: "/en/404.html", kind: theme.KindNotFound, language: "en", number: 1, total: 1},
	} {
		pg := pageAt(b, tt.path)
		if pg == nil {
			t.Errorf("no page at %q", tt.path)
			continue
		}
		var entry string
		if pg.entry != nil {
			entry = pg.entry.e.ID
		}
		got := fmt.Sprintf("%d %s %d/%d %t %q %q", pg.kind, pg.language, pg.number, pg.total, pg.front, entry, entryIDs(pg.entries))
		want := fmt.Sprintf("%d %s %d/%d %t %q %q", tt.kind, tt.language, tt.number, tt.total, tt.front, tt.entry, tt.entries)
		if got != want {
			t.Errorf("page at %q = %s, want %s", tt.path, got, want)
		}
		first := tt.path
		if tt.number > 1 {
			first = strings.TrimSuffix(tt.path, fmt.Sprintf("page/%d/", tt.number))
		}
		if pg.first != first || pg.file != files[tt.path] {
			t.Errorf("page at %q has page 1 at %q and the file %q", tt.path, pg.first, pg.file)
		}
	}
	for _, x := range []struct {
		page *page
		want string
	}{
		{pageAt(b, "/category/news/"), newsCategoryID},
		{pageAt(b, "/en/tag/events/page/2/"), enEventTagID},
		{pageAt(b, "/author/yamada/page/2/"), authorID},
		{pageAt(b, "/works/"), "works"},
	} {
		var got string
		switch {
		case x.page.term != nil:
			got = x.page.term.v.ID
		case x.page.author != nil:
			got = x.page.author.v.ID
		default:
			got = x.page.typeName
		}
		if got != x.want {
			t.Errorf("the list at %q is of %q, want %q", x.page.path, got, x.want)
		}
	}

	if b.base != "" {
		t.Errorf("base = %q, want \"\"", b.base)
	}
	if n := len(b.entryPaths); n != 11+22 {
		t.Errorf("entryPaths has %d entries, want %d", n, 11+22)
	}
	for id, want := range map[string]string{
		homeID:     "/",
		newsID:     "/news/",
		postID:     "/2026/10/26/新サービス/",
		enAboutID:  "/en/about/",
		jaPosts[0]: "/2026/09/01/post-1001/",
		draftID:    "",
	} {
		if got := b.entryPaths[id]; got != want {
			t.Errorf("entryPaths[%s] = %q, want %q", id, got, want)
		}
	}
	wantTerms := map[string]string{
		newsCategoryID:  "/category/news/",
		pressReleaseID:  "/category/news/press-release/",
		eventTagID:      "/tag/イベント/",
		enEventTagID:    "/en/tag/events/",
		worksCategoryID: "/works_category/corporate-site/",
	}
	if !maps.Equal(b.termPaths, wantTerms) {
		t.Errorf("termPaths = %v, want %v", b.termPaths, wantTerms)
	}
	wantAuthors := map[string]map[string]string{authorID: {"ja": "/author/yamada/", "en": "/en/author/yamada/"}}
	if !reflect.DeepEqual(b.authorPaths, wantAuthors) {
		t.Errorf("authorPaths = %v, want %v", b.authorPaths, wantAuthors)
	}

	schema := `the list of the type "works" in `
	for _, tt := range []struct {
		path          string
		file, pointer string
		what          string
		rank          int
	}{
		{"/", "site/settings.json", "/frontPage/ja", "the front page in ja", rankCore},
		{"/en/", "site/settings.json", "/languages/1", "the front page in en", rankCore},
		{"/en/page/2/", "site/settings.json", "/languages/1", "the front page in en", rankCore},
		{"/about/", revisionFile(aboutID), "/slug", revisionFile(aboutID), rankDocument},
		{"/news/page/2/", revisionFile(newsID), "/slug", revisionFile(newsID), rankDocument},
		{"/category/news/", fileOf(content.RefTerm, newsCategoryID), "/slug", fileOf(content.RefTerm, newsCategoryID), rankDocument},
		{"/en/author/yamada/", fileOf(content.RefAuthor, authorID), "/slug", fileOf(content.RefAuthor, authorID), rankDocument},
		{"/works/", "site/schema.yaml", "/types/works/archive", schema + "ja", rankDeveloper},
		{"/en/works/", "site/schema.yaml", "/types/works/archive", schema + "en", rankDeveloper},
		{"/_forms/" + formID, fileOf(content.RefForm, formID), "/id", fileOf(content.RefForm, formID), rankDocument},
		{"/404.html", "", "", "the 404 page in ja", rankCore},
		{"/en/feed/", "", "", "the feed in en", rankCore},
		{"/sitemap.xml", "", "", "the sitemap", rankCore},
		{"/css/site.css", "", "", "the site CSS", rankCore},
		{"/.htaccess", "", "", "the .htaccess file for Apache", rankCore},
	} {
		o := outputAt(b, tt.path)
		if o == nil {
			t.Errorf("no output at %q", tt.path)
			continue
		}
		got := fmt.Sprintf("%s %s %q %d", o.src.file.Name, o.src.pointer, o.src.what, o.rank)
		if want := fmt.Sprintf("%s %s %q %d", tt.file, tt.pointer, tt.what, tt.rank); got != want {
			t.Errorf("source of %q = %s, want %s", tt.path, got, want)
		}
	}
}

// TestPlanSettings checks how the site settings and the theme change the outputs.
func TestPlanSettings(t *testing.T) {
	tests := []struct {
		name   string
		change func(t *testing.T, s *memSource)
		want   map[string]string // the file of each path; "-" for no output at the path
		check  func(t *testing.T, b *builder)
	}{
		{
			name: "prefixDefaultLanguage",
			change: func(t *testing.T, s *memSource) {
				site := testSite()
				site.PrefixDefaultLanguage = true
				s.putSettings(t, site)
			},
			want: map[string]string{
				"/":                                 "-",
				"/home/":                            "-",
				"/ja/":                              "ja/index.html",
				"/ja/about/":                        "ja/about/index.html",
				"/ja/news/":                         "ja/news/index.html",
				"/ja/2026/10/26/新サービス/":             "ja/2026/10/26/新サービス/index.html",
				"/ja/category/news/press-release/":  "ja/category/news/press-release/index.html",
				"/ja/tag/イベント/":                     "ja/tag/イベント/index.html",
				"/ja/author/yamada/":                "ja/author/yamada/index.html",
				"/ja/works/":                        "ja/works/index.html",
				"/ja/works/example-corporate-site/": "ja/works/example-corporate-site/index.html",
				"/ja/404.html":                      "ja/404.html",
				"/ja/feed/":                         "ja/feed/index.xml",
				"/en/":                              "en/index.html",
				"/en/about/":                        "en/about/index.html",
				"/en/feed/":                         "en/feed/index.xml",
				"/sitemap.xml":                      "sitemap.xml",
			},
			check: func(t *testing.T, b *builder) {
				for _, pg := range b.pages {
					if !strings.HasPrefix(pg.path, "/ja/") && !strings.HasPrefix(pg.path, "/en/") {
						t.Errorf("page at %q", pg.path)
					}
				}
				if got := b.entryPaths[homeID]; got != "/ja/" {
					t.Errorf("entryPaths[homeID] = %q, want \"/ja/\"", got)
				}
			},
		},
		{
			name: "latest posts at the front in ja",
			change: func(t *testing.T, s *memSource) {
				site := testSite()
				site.FrontPage, site.PostsPage = nil, nil
				s.putSettings(t, site)
				s.addPosts(t, "ja", 11)
			},
			want: map[string]string{
				"/":        "index.html",
				"/page/2/": "page/2/index.html",
				"/home/":   "home/index.html",
				"/news/":   "news/index.html",
			},
			check: func(t *testing.T, b *builder) {
				if pg := pageAt(b, "/page/2/"); pg.kind != theme.KindPosts || !pg.front || pg.entry != nil || pg.language != "ja" ||
					pg.number != 2 || pg.first != "/" || len(pg.entries) != 2 {
					t.Errorf("page at /page/2/ = %+v", pg)
				}
				for _, path := range []string{"/home/", "/news/"} {
					if pg := pageAt(b, path); pg.kind != theme.KindEntry || pg.front || pg.total != 1 {
						t.Errorf("page at %q = %+v", path, pg)
					}
				}
				if got := b.entryPaths[homeID]; got != "/home/" {
					t.Errorf("entryPaths[homeID] = %q, want \"/home/\"", got)
				}
			},
		},
		{
			name: "no 404.html",
			change: func(t *testing.T, s *memSource) {
				fsys := testTheme()
				delete(fsys, "templates/404.html")
				s.theme = fsys
			},
			want: map[string]string{"/404.html": "-", "/en/404.html": "-", "/feed/": "feed/index.xml"},
			check: func(t *testing.T, b *builder) {
				for _, pg := range b.pages {
					if pg.kind == theme.KindNotFound {
						t.Errorf("404 page at %q", pg.path)
					}
				}
			},
		},
		{
			name: "noindex",
			change: func(t *testing.T, s *memSource) {
				site := testSite()
				site.SEO.NoIndex = true
				s.putSettings(t, site)
			},
			want: map[string]string{"/sitemap.xml": "-", "/css/site.css": "css/site.css"},
		},
		{
			name: "site under a path",
			change: func(t *testing.T, s *memSource) {
				site := testSite()
				site.URL = "https://example.com/blog"
				s.putSettings(t, site)
			},
			want: map[string]string{"/": "index.html", "/about/": "about/index.html", "/blog/about/": "-"},
			check: func(t *testing.T, b *builder) {
				if b.base != "/blog" {
					t.Errorf("base = %q, want \"/blog\"", b.base)
				}
			},
		},
		{
			name: "assets",
			change: func(t *testing.T, s *memSource) {
				fsys := testTheme()
				fsys["assets/css/style.css"] = &fstest.MapFile{Data: []byte("body { margin: 0; }\n")}
				fsys["assets/logo.svg"] = &fstest.MapFile{Data: []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"/>\n")}
				s.theme = fsys
			},
			want: map[string]string{"/assets/css/style.css": "assets/css/style.css", "/assets/logo.svg": "assets/logo.svg"},
			check: func(t *testing.T, b *builder) {
				o := outputAt(b, "/assets/css/style.css")
				if got := fmt.Sprintf("%s %q %q %d", o.src.file.Name, o.src.pointer, o.src.what, o.rank); got !=
					fmt.Sprintf("theme/assets/css/style.css \"\" \"theme/assets/css/style.css\" %d", rankDeveloper) {
					t.Errorf("source of the asset = %s", got)
				}
			},
		},
		{
			name: "front page in en",
			change: func(t *testing.T, s *memSource) {
				site := testSite()
				site.FrontPage["en"] = enAboutID
				s.putSettings(t, site)
			},
			want: map[string]string{"/en/": "en/index.html", "/en/about/": "-", "/en/page/2/": "-"},
			check: func(t *testing.T, b *builder) {
				if pg := pageAt(b, "/en/"); pg.kind != theme.KindEntry || !pg.front || pg.entry.e.ID != enAboutID {
					t.Errorf("page at /en/ = %+v", pg)
				}
				for _, pg := range b.pages {
					if pg.kind == theme.KindPosts && pg.language == "en" {
						t.Errorf("latest posts at %q", pg.path)
					}
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := testSource(t)
			tt.change(t, src)
			b := mustLoad(t, src)
			files := outputFiles(t, b)
			for path, want := range tt.want {
				got, ok := files[path]
				if !ok {
					got = "-"
				}
				if got != want {
					t.Errorf("output at %q = %q, want %q", path, got, want)
				}
			}
			if tt.check != nil {
				tt.check(t, b)
			}
		})
	}
}

// TestPlanErrors checks that the paths that cannot be made, such as with a segment longer than a file name, are errors
// at the items that make them.
func TestPlanErrors(t *testing.T) {
	src := testSource(t)
	literal := strings.Repeat("x", 60)
	archive := strings.Repeat("w", 256)
	src.schema.Data = []byte(strings.NewReplacer(
		"url: /{yyyy}/{mm}/{dd}/{slug}/", "url: /{yyyy}/{mm}/{dd}/{slug}-"+literal+"/",
		"archive: /works/", "archive: /"+archive+"/",
	).Replace(testSchema) + "  tag:\n    url: /tag/{slug}-" + literal + "/\nauthorUrl: /author/{slug}-" + literal + "/\n")
	slug, termSlug, authorSlug := strings.Repeat("あ", 66), strings.Repeat("e", 200), strings.Repeat("y", 200)
	src.editRevision(t, postID, func(r *content.Revision) { r.Slug = slug })
	edit(t, src, content.RefTerm, enEventTagID, func(x *content.Term) { x.Slug = termSlug })
	edit(t, src, content.RefAuthor, authorID, func(a *content.AuthorProfile) { a.Slug = authorSlug })
	b, err := load(src)
	if b != nil {
		t.Errorf("load() = %+v with an error", b)
	}
	tooLong := func(path string, n int) string {
		return fmt.Sprintf("cannot make the path: invalid path %q: segment too long: %d bytes (max 255)", path, n)
	}
	want := []string{
		fileOf(content.RefAuthor, authorID) + ":5:3: /slug: " + tooLong("/author/"+authorSlug+"-"+literal+"/", 261),
		fileOf(content.RefAuthor, authorID) + ":5:3: /slug: " + tooLong("/en/author/"+authorSlug+"-"+literal+"/", 261),
		revisionFile(postID) + ":4:3: /slug: " + tooLong("/2026/10/26/"+slug+"-"+literal+"/", 259),
		"site/schema.yaml:8:5: /types/works/archive: " + tooLong("/en/"+archive+"/", 256),
		"site/schema.yaml:8:5: /types/works/archive: " + tooLong("/"+archive+"/", 256),
		fileOf(content.RefTerm, enEventTagID) + ":8:3: /slug: " + tooLong("/en/tag/"+termSlug+"-"+literal+"/", 261),
	}
	if got := errorTexts(t, err); !slices.Equal(got, want) {
		t.Errorf("errors:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestBasePath(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"https://example.com", ""},
		{"https://example.com/blog", "/blog"},
		{"http://localhost:8080/a/b", "/a/b"},
		{"https://example.com/ブログ", "/ブログ"},
		{"https://example.com/%E3%83%96", "/%E3%83%96"},
	} {
		if got := basePath(tt.in); got != tt.want {
			t.Errorf("basePath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
