package content

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// IDs of the pages and the terms in testPathRefs, apart from testAboutPage, testNewsCategory, testPressReleaseCategory
// and testSeries. testLoopPage and testLoopPage2 are the parents of each other.
const (
	testAccessPage  = "ent_01JB9F00000000000000000001" // child of testAboutPage
	testLoopPage    = "ent_01JB9F00000000000000000002"
	testLoopPage2   = "ent_01JB9F00000000000000000003"
	testNoSlugPage  = "ent_01JB9F00000000000000000004"
	testBadSlugPage = "ent_01JB9F00000000000000000005"
	testSeriesChild = "trm_01JB9F00000000000000000006" // child of testSeries
)

// testPathRefs are the pages and the terms on the public site that testPathLookup finds.
var testPathRefs = map[string]PathRef{
	testAboutPage:            {Slug: "about"},
	testAccessPage:           {Slug: "access", Parent: testAboutPage},
	testLoopPage:             {Slug: "loop", Parent: testLoopPage2},
	testLoopPage2:            {Slug: "loop2", Parent: testLoopPage},
	testNoSlugPage:           {},
	testBadSlugPage:          {Slug: "About"},
	testNewsCategory:         {Slug: "news"},
	testPressReleaseCategory: {Slug: "press-release", Parent: testNewsCategory},
	testSeries:               {Slug: "classic"},
	testSeriesChild:          {Slug: "mini", Parent: testSeries},
}

func testPathLookup(kind RefKind, id string) (PathRef, bool) {
	r, ok := testPathRefs[id]
	return r, ok && strings.HasPrefix(id, idPrefixes[kind])
}

// assertValidSite checks that s is valid, as the functions that return paths expect.
func assertValidSite(t *testing.T, s *Site) {
	t.Helper()
	if err := s.Validate(testLookup); err != nil {
		t.Fatalf("Validate() of the site = %v", err)
	}
}

// assertPath checks that got and err are the path want, or the error msg if it is not "",
// and that OutputFile accepts the path.
func assertPath(t *testing.T, got string, err error, want, msg string) {
	t.Helper()
	switch {
	case msg == "" && err != nil:
		t.Errorf("error = %q, want nil", err)
	case msg != "" && (err == nil || err.Error() != msg):
		t.Errorf("error = %v, want %q", err, msg)
	}
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if got != "" {
		if _, err := OutputFile(got); err != nil {
			t.Errorf("OutputFile(%q) = %v", got, err)
		}
	}
}

func TestEntryPath(t *testing.T) {
	const upperCase = "contains upper case letters; normalize it first"
	// typeRule returns testSite with rule as the URL rule of the type name.
	typeRule := func(name, rule string) *Site {
		s := testSite()
		i := slices.IndexFunc(s.Types, func(d TypeDef) bool { return d.Name == name })
		if i < 0 {
			s.Types = append(s.Types, TypeDef{Name: name})
			i = len(s.Types) - 1
		}
		s.Types[i].URL = rule
		return s
	}
	// edit returns testSite changed by f.
	edit := func(f func(s *Site)) *Site {
		s := testSite()
		f(s)
		return s
	}
	var (
		numeric       = typeRule(TypePost, "/archives/{number}/")
		byCategory    = typeRule(TypePost, "/{category}/{slug}/")
		front         = edit(func(s *Site) { s.FrontPage = Localized{"ja": testAboutPage, "en": testEnglishPage} })
		prefixed      = edit(func(s *Site) { s.PrefixDefaultLanguage = true })
		prefixedFront = edit(func(s *Site) { s.PrefixDefaultLanguage, s.FrontPage = true, Localized{"ja": testAboutPage} })
	)
	// news is a post at 0:30 in Japan, which is on the day before in UTC.
	news := Entry{ID: testPost, Number: 123, Type: TypePost, Language: "ja"}
	newsRev := Revision{Slug: "新サービス", Date: "2026-10-26T00:30:00+09:00", Terms: Terms{TaxonomyCategory: {testPressReleaseCategory}}}
	about := Entry{ID: testAboutPage, Number: 2, Type: TypePage, Language: "ja"}
	aboutRev := Revision{Slug: "about"}
	access := Entry{ID: testAccessPage, Number: 3, Type: TypePage, Language: "ja"}
	accessRev := Revision{Slug: "access", Parent: testAboutPage}
	english := Entry{ID: testEnglishPage, Number: 4, Type: TypePage, Language: "en"}
	works := Entry{ID: testEntryID, Number: 5, Type: "works", Language: "ja"}
	worksRev := Revision{Slug: "example-corporate-site"}
	// entry returns e changed by f, and revision returns r changed by f.
	entry := func(e Entry, f func(e *Entry)) Entry {
		f(&e)
		return e
	}
	revision := func(r Revision, f func(r *Revision)) Revision {
		f(&r)
		return r
	}
	tests := []struct {
		name     string
		site     *Site // testSite() if nil
		entry    Entry
		rev      Revision
		noLookup bool
		want     string
		msg      string // the whole error
	}{
		{name: "default rule of post", entry: news, rev: newsRev, want: "/2026/10/26/新サービス/"},
		{
			name:  "date in UTC",
			entry: news,
			rev:   revision(newsRev, func(r *Revision) { r.Date = "2026-10-25T15:30:00Z" }),
			want:  "/2026/10/25/新サービス/",
		},
		{
			name:  "date with a negative offset",
			entry: news,
			rev:   revision(newsRev, func(r *Revision) { r.Date = "2026-10-25T22:30:00-05:00" }),
			want:  "/2026/10/25/新サービス/",
		},
		{name: "numeric", site: numeric, entry: news, rev: newsRev, want: "/archives/123/"},
		{name: "category and name", site: byCategory, entry: news, rev: newsRev, want: "/news/press-release/新サービス/"},
		{
			name:  "first category",
			site:  byCategory,
			entry: news,
			rev:   revision(newsRev, func(r *Revision) { r.Terms = Terms{TaxonomyCategory: {testNewsCategory, testPressReleaseCategory}} }),
			want:  "/news/新サービス/",
		},
		{
			name:  "date in a segment and .html",
			site:  typeRule(TypePost, "/{yyyy}{mm}{dd}/{number}.html"),
			entry: news,
			rev:   newsRev,
			want:  "/20261026/123.html",
		},
		{name: "name and .html", site: typeRule(TypePost, "/{yyyy}/{mm}/{slug}.html"), entry: news, rev: newsRev, want: "/2026/10/新サービス.html"},
		{
			name:  "post in en",
			entry: entry(news, func(e *Entry) { e.Language = "en" }),
			rev:   revision(newsRev, func(r *Revision) { r.Slug, r.Terms = "new-service", nil }),
			want:  "/en/2026/10/26/new-service/",
		},
		{name: "page", entry: about, rev: aboutRev, want: "/about/"},
		{name: "child page", entry: access, rev: accessRev, want: "/about/access/"},
		{
			name:  "grandchild page",
			entry: entry(access, func(e *Entry) { e.ID = testEntryID }),
			rev:   Revision{Slug: "map", Parent: testAccessPage},
			want:  "/about/access/map/",
		},
		{name: "child page and .html", site: typeRule(TypePage, "/{path}.html"), entry: access, rev: accessRev, want: "/about/access.html"},
		{name: "page in en", entry: english, rev: aboutRev, want: "/en/about/"},
		{name: "front page", site: front, entry: about, rev: aboutRev, want: "/"},
		{name: "front page without a slug", site: front, entry: about, want: "/"},
		{name: "front page in en", site: front, entry: english, rev: aboutRev, want: "/en/"},
		{name: "child of the front page", site: front, entry: access, rev: accessRev, want: "/about/access/"},
		{name: "default language with a prefix", site: prefixed, entry: about, rev: aboutRev, want: "/ja/about/"},
		{name: "en with a prefix for the default language", site: prefixed, entry: english, rev: aboutRev, want: "/en/about/"},
		{name: "front page with a prefix", site: prefixedFront, entry: about, rev: aboutRev, want: "/ja/"},
		{name: "custom type", entry: works, rev: worksRev, want: "/works/example-corporate-site/"},
		{name: "custom type with a rule", site: typeRule("works", "/実績/{number}/"), entry: works, rev: worksRev, want: "/実績/5/"},
		{name: "post without lookup", entry: news, rev: newsRev, noLookup: true, want: "/2026/10/26/新サービス/"},
		{name: "page without a parent or lookup", entry: about, rev: aboutRev, noLookup: true, want: "/about/"},

		{
			name:  "language not in the site settings",
			entry: entry(news, func(e *Entry) { e.Language = "fr" }),
			rev:   newsRev,
			msg:   `language "fr" is not in the site settings (use ja, en)`,
		},
		{name: "unknown type", entry: entry(news, func(e *Entry) { e.Type = "news" }), rev: newsRev, msg: `unknown type "news"`},
		{name: "missing slug", entry: news, rev: revision(newsRev, func(r *Revision) { r.Slug = "" }), msg: "missing slug"},
		{name: "invalid slug", entry: news, rev: revision(newsRev, func(r *Revision) { r.Slug = "News" }), msg: "invalid slug: " + upperCase},
		{
			name:  "slash in the slug",
			entry: news,
			rev:   revision(newsRev, func(r *Revision) { r.Slug = "a/b" }),
			msg:   `invalid slug: character '/' is not allowed in slugs: use lower case letters, digits, "-" and "_" in ASCII`,
		},
		{name: "missing slug of a child page", entry: access, rev: revision(accessRev, func(r *Revision) { r.Slug = "" }), msg: "missing slug"},
		{name: "missing date", entry: news, rev: revision(newsRev, func(r *Revision) { r.Date = "" }), msg: "missing date"},
		{
			name:  "date without a time",
			entry: news,
			rev:   revision(newsRev, func(r *Revision) { r.Date = "2026-10-26" }),
			msg:   `invalid date and time "2026-10-26": use RFC 3339 with seconds and a time zone offset, such as "2026-10-25T10:00:00+09:00"`,
		},
		{name: "missing number", site: numeric, entry: entry(news, func(e *Entry) { e.Number = 0 }), rev: newsRev, msg: "missing number"},
		{
			name:  "negative number",
			site:  numeric,
			entry: entry(news, func(e *Entry) { e.Number = -1 }),
			rev:   newsRev,
			msg:   "number -1 is out of range (1 or more)",
		},
		{name: "missing category", site: byCategory, entry: news, rev: revision(newsRev, func(r *Revision) { r.Terms = nil }), msg: "missing category"},
		{
			name:  "empty categories",
			site:  byCategory,
			entry: news,
			rev:   revision(newsRev, func(r *Revision) { r.Terms = Terms{TaxonomyCategory: {}} }),
			msg:   "missing category",
		},
		{
			name:  "category not found",
			site:  byCategory,
			entry: news,
			rev:   revision(newsRev, func(r *Revision) { r.Terms = Terms{TaxonomyCategory: {"trm_" + testMissing}} }),
			msg:   `term "trm_` + testMissing + `" is not found`,
		},
		{
			name:     "category without lookup",
			site:     byCategory,
			entry:    news,
			rev:      newsRev,
			noLookup: true,
			msg:      `term "` + testPressReleaseCategory + `" is not found`,
		},
		{
			name:  "parent not found",
			entry: access,
			rev:   revision(accessRev, func(r *Revision) { r.Parent = "ent_" + testMissing }),
			msg:   `page "ent_` + testMissing + `" is not found`,
		},
		{name: "parent without lookup", entry: access, rev: accessRev, noLookup: true, msg: `page "` + testAboutPage + `" is not found`},
		{
			name:  "parent without a slug",
			entry: access,
			rev:   revision(accessRev, func(r *Revision) { r.Parent = testNoSlugPage }),
			msg:   `page "` + testNoSlugPage + `" has no slug`,
		},
		{
			name:  "parent with an invalid slug",
			entry: access,
			rev:   revision(accessRev, func(r *Revision) { r.Parent = testBadSlugPage }),
			msg:   `page "` + testBadSlugPage + `" has an invalid slug: ` + upperCase,
		},
		{
			name:  "own parent",
			entry: access,
			rev:   revision(accessRev, func(r *Revision) { r.Parent = testAccessPage }),
			msg:   `the parents of "` + testAccessPage + `" make a cycle`,
		},
		{
			name:  "cycle of the parents",
			entry: access,
			rev:   revision(accessRev, func(r *Revision) { r.Parent = testLoopPage }),
			msg:   `the parents of "` + testLoopPage + `" make a cycle`,
		},
		{
			name:  "cycle back to the page",
			entry: entry(access, func(e *Entry) { e.ID = testLoopPage }),
			rev:   revision(accessRev, func(r *Revision) { r.Parent = testLoopPage2 }),
			msg:   `the parents of "` + testLoopPage + `" make a cycle`,
		},
		{
			name:  "URL rule that cannot be read",
			site:  typeRule(TypePost, "/{slug/"),
			entry: news,
			rev:   newsRev,
			msg:   `invalid URL rule "/{slug/": unclosed "{"`,
		},
		{
			name:  "unknown placeholder",
			site:  typeRule(TypePost, "/{name}/"),
			entry: news,
			rev:   newsRev,
			msg:   `invalid URL rule "/{name}/": placeholder "{name}" is not allowed for post`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := cmp.Or(tt.site, testSite())
			if tt.msg == "" {
				assertValidSite(t, s)
			}
			lookup := testPathLookup
			if tt.noLookup {
				lookup = nil
			}
			got, err := s.EntryPath(&tt.entry, &tt.rev, lookup)
			assertPath(t, got, err, tt.want, tt.msg)
		})
	}
}

func TestTermPath(t *testing.T) {
	const upperCase = "contains upper case letters; normalize it first"
	// taxonomyRule returns testSite with rule as the URL rule of the taxonomy name.
	taxonomyRule := func(name, rule string) *Site {
		s := testSite()
		i := slices.IndexFunc(s.Taxonomies, func(d TaxonomyDef) bool { return d.Name == name })
		if i < 0 {
			s.Taxonomies = append(s.Taxonomies, TaxonomyDef{Name: name})
			i = len(s.Taxonomies) - 1
		}
		s.Taxonomies[i].URL = rule
		return s
	}
	prefixed := testSite()
	prefixed.PrefixDefaultLanguage = true
	// mini is a term of series, which is hierarchical, and web one of works_category, which is not.
	mini := Term{ID: testSeriesChild, Taxonomy: "series", Language: "ja", Slug: "mini", Parent: testSeries}
	web := Term{ID: testWorksTerm, Taxonomy: "works_category", Language: "ja", Slug: "web"}
	// term returns x changed by f.
	term := func(x Term, f func(x *Term)) Term {
		f(&x)
		return x
	}
	tests := []struct {
		name     string
		site     *Site // testSite() if nil
		in       Term
		noLookup bool
		want     string
		msg      string // the whole error
	}{
		{name: "category", in: newsCategory, want: "/category/news/"},
		{name: "child category", in: pressReleaseCategory, want: "/category/news/press-release/"},
		{
			name: "grandchild category",
			in:   term(pressReleaseCategory, func(x *Term) { x.ID, x.Slug, x.Parent = testCategory, "domestic", testPressReleaseCategory }),
			want: "/category/news/press-release/domestic/",
		},
		{
			name: "child category without its parents",
			site: taxonomyRule(TaxonomyCategory, "/category/{slug}/"),
			in:   pressReleaseCategory,
			want: "/category/press-release/",
		},
		{name: "category with a rule", site: taxonomyRule(TaxonomyCategory, "/topics/{path}/"), in: pressReleaseCategory, want: "/topics/news/press-release/"},
		{name: "tag", in: eventTag, want: "/tag/イベント/"},
		{name: "tag in en", in: englishEventTag, want: "/en/tag/events/"},
		{name: "custom taxonomy", in: web, want: "/works_category/web/"},
		// As in WordPress, the default rule of a custom taxonomy has no parents.
		{name: "hierarchical custom taxonomy", in: mini, want: "/series/mini/"},
		{name: "hierarchical custom taxonomy with the parents", site: taxonomyRule("series", "/series/{path}/"), in: mini, want: "/series/classic/mini/"},
		{name: "default language with a prefix", site: prefixed, in: pressReleaseCategory, want: "/ja/category/news/press-release/"},
		{name: "category without a parent or lookup", in: newsCategory, noLookup: true, want: "/category/news/"},

		{
			name: "language not in the site settings",
			in:   term(newsCategory, func(x *Term) { x.Language = "fr" }),
			msg:  `language "fr" is not in the site settings (use ja, en)`,
		},
		{name: "unknown taxonomy", in: term(newsCategory, func(x *Term) { x.Taxonomy = "genre" }), msg: `unknown taxonomy "genre"`},
		{name: "missing slug", in: term(englishEventTag, func(x *Term) { x.Slug = "" }), msg: "missing slug"},
		{name: "invalid slug", in: term(englishEventTag, func(x *Term) { x.Slug = "Events" }), msg: "invalid slug: " + upperCase},
		{name: "missing slug of a child", in: term(pressReleaseCategory, func(x *Term) { x.Slug = "" }), msg: "missing slug"},
		{
			name: "parent not found",
			in:   term(pressReleaseCategory, func(x *Term) { x.Parent = "trm_" + testMissing }),
			msg:  `term "trm_` + testMissing + `" is not found`,
		},
		{name: "parent without lookup", in: pressReleaseCategory, noLookup: true, msg: `term "` + testNewsCategory + `" is not found`},
		{
			name: "own parent",
			in:   term(pressReleaseCategory, func(x *Term) { x.Parent = x.ID }),
			msg:  `the parents of "` + testPressReleaseCategory + `" make a cycle`,
		},
		{
			name: "cycle back to the term",
			in:   term(newsCategory, func(x *Term) { x.Parent = testPressReleaseCategory }),
			msg:  `the parents of "` + testNewsCategory + `" make a cycle`,
		},
		{
			name: "URL rule that cannot be read",
			site: taxonomyRule(TaxonomyTag, "/tag/{slug"),
			in:   eventTag,
			msg:  `invalid URL rule "/tag/{slug": unclosed "{"`,
		},
		{
			name: "placeholder not allowed",
			site: taxonomyRule(TaxonomyTag, "/tag/{number}/"),
			in:   eventTag,
			msg:  `invalid URL rule "/tag/{number}/": placeholder "{number}" is not allowed for tag`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := cmp.Or(tt.site, testSite())
			if tt.msg == "" {
				assertValidSite(t, s)
			}
			lookup := testPathLookup
			if tt.noLookup {
				lookup = nil
			}
			got, err := s.TermPath(&tt.in, lookup)
			assertPath(t, got, err, tt.want, tt.msg)
		})
	}
}

func TestAuthorPath(t *testing.T) {
	// authorRule returns testSite with rule as the URL rule of the author pages.
	authorRule := func(rule string) *Site {
		s := testSite()
		s.AuthorURL = rule
		return s
	}
	prefixed := testSite()
	prefixed.PrefixDefaultLanguage = true
	// author returns authorProfile with slug.
	author := func(slug string) AuthorProfile {
		a := authorProfile
		a.Slug = slug
		return a
	}
	tests := []struct {
		name     string
		site     *Site // testSite() if nil
		in       AuthorProfile
		language string
		want     string
		msg      string // the whole error
	}{
		{name: "ja", in: authorProfile, language: "ja", want: "/author/yamada/"},
		{name: "en", in: authorProfile, language: "en", want: "/en/author/yamada/"},
		{name: "rule", site: authorRule("/writers/{slug}/"), in: authorProfile, language: "ja", want: "/writers/yamada/"},
		{name: "rule in en", site: authorRule("/writers/{slug}/"), in: authorProfile, language: "en", want: "/en/writers/yamada/"},
		{name: "default language with a prefix", site: prefixed, in: authorProfile, language: "ja", want: "/ja/author/yamada/"},

		{name: "language not in the site settings", in: authorProfile, language: "fr", msg: `language "fr" is not in the site settings (use ja, en)`},
		{name: "missing language", in: authorProfile, msg: `language "" is not in the site settings (use ja, en)`},
		{name: "missing slug", in: author(""), language: "ja", msg: "missing slug"},
		{name: "invalid slug", in: author("Yamada"), language: "ja", msg: "invalid slug: contains upper case letters; normalize it first"},
		{
			name:     "URL rule that cannot be read",
			site:     authorRule("/author/}{slug}/"),
			in:       authorProfile,
			language: "ja",
			msg:      `invalid URL rule "/author/}{slug}/": unexpected "}"`,
		},
		{
			name:     "placeholder not allowed",
			site:     authorRule("/author/{number}/"),
			in:       authorProfile,
			language: "ja",
			msg:      `invalid URL rule "/author/{number}/": placeholder "{number}" is not allowed for author pages`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := cmp.Or(tt.site, testSite())
			if tt.msg == "" {
				assertValidSite(t, s)
			}
			got, err := s.AuthorPath(&tt.in, tt.language)
			assertPath(t, got, err, tt.want, tt.msg)
		})
	}
}

func TestArchivePath(t *testing.T) {
	// archive returns testSite with path as the archive of works.
	archive := func(path string) *Site {
		s := testSite()
		s.Types[0].Archive = path
		return s
	}
	works := archive("/works/")
	prefixed := archive("/works/")
	prefixed.PrefixDefaultLanguage = true
	tests := []struct {
		name     string
		site     *Site // works if nil
		typ      string
		language string
		want     string
		msg      string // the whole error
	}{
		{name: "ja", typ: "works", language: "ja", want: "/works/"},
		{name: "en", typ: "works", language: "en", want: "/en/works/"},
		{name: "Japanese", site: archive("/実績/"), typ: "works", language: "en", want: "/en/実績/"},
		{name: "two segments", site: archive("/works/all/"), typ: "works", language: "ja", want: "/works/all/"},
		{name: "default language with a prefix", site: prefixed, typ: "works", language: "ja", want: "/ja/works/"},
		{name: "type without an archive", typ: "product", language: "ja"},
		{name: "post", typ: TypePost, language: "ja"},
		{name: "page", typ: TypePage, language: "en"},

		{name: "language not in the site settings", typ: "works", language: "fr", msg: `language "fr" is not in the site settings (use ja, en)`},
		{name: "unknown type", typ: "news", language: "ja", msg: `unknown type "news"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := cmp.Or(tt.site, works)
			if tt.msg == "" {
				assertValidSite(t, s)
			}
			got, err := s.ArchivePath(tt.typ, tt.language)
			assertPath(t, got, err, tt.want, tt.msg)
		})
	}
}

func TestPagedPath(t *testing.T) {
	tests := []struct {
		path string
		n    int
		want string
	}{
		{path: "/", n: 1, want: "/"},
		{path: "/", n: 0, want: "/"},
		{path: "/", n: -1, want: "/"},
		{path: "/", n: 2, want: "/page/2/"},
		{path: "/", n: 3, want: "/page/3/"},
		{path: "/en/", n: 1, want: "/en/"},
		{path: "/en/", n: 2, want: "/en/page/2/"},
		{path: "/category/news/", n: 1, want: "/category/news/"},
		{path: "/category/news/", n: 2, want: "/category/news/page/2/"},
		{path: "/category/news/", n: 10, want: "/category/news/page/10/"},
	}
	for _, tt := range tests {
		if got := PagedPath(tt.path, tt.n); got != tt.want {
			t.Errorf("PagedPath(%q, %d) = %q, want %q", tt.path, tt.n, got, tt.want)
		}
	}
}

func TestOutputFile(t *testing.T) {
	const (
		start  = `must start with "/"`
		ending = `must end with "/" or ".html"`
		dots   = `dot segments ("." and "..") are not allowed`
		hidden = `segments cannot start with "."`
	)
	longest := strings.Repeat("a", MaxFileNameBytes)
	tests := []struct {
		name   string
		in     string
		want   string
		reason string // the error is `invalid path "<in>": <reason>`
	}{
		{name: "front page", in: "/", want: "index.html"},
		{name: "directory", in: "/about/", want: "about/index.html"},
		{name: "Japanese", in: "/2026/10/26/新サービス/", want: "2026/10/26/新サービス/index.html"},
		{name: "html", in: "/archives/123.html", want: "archives/123.html"},
		{name: "html at the root", in: "/about.html", want: "about.html"},
		{name: "directory ending with .html", in: "/about.html/", want: "about.html/index.html"},
		{name: "page of a list", in: "/en/page/2/", want: "en/page/2/index.html"},
		{name: "dots in segments", in: "/a..b/c.d.html", want: "a..b/c.d.html"},
		{name: "longest segment", in: "/" + longest + "/", want: longest + "/index.html"},
		{name: "longest file name", in: "/" + longest[5:] + ".html", want: longest[5:] + ".html"},
		{name: "longest segment in Japanese", in: "/" + strings.Repeat("あ", 85) + "/", want: strings.Repeat("あ", 85) + "/index.html"},

		{name: "empty", in: "", reason: start},
		{name: "relative", in: "about/", reason: start},
		{name: "URL", in: "https://example.com/about/", reason: start},
		{name: "without an ending", in: "/about", reason: ending},
		{name: ".htm", in: "/about.htm", reason: ending},
		{name: ".HTML", in: "/about.HTML", reason: ending},
		{name: "query", in: "/?p=123", reason: ending},
		{name: "empty segment", in: "//", reason: "empty segment"},
		{name: "empty segment in the middle", in: "/a//b.html", reason: "empty segment"},
		{name: "dot segment", in: "/./", reason: dots},
		{name: "dot-dot segment", in: "/../", reason: dots},
		{name: "dot-dot segment in the middle", in: "/a/../b.html", reason: dots},
		{name: "hidden directory", in: "/.git/", reason: hidden},
		{name: "hidden file", in: "/a/.b.html", reason: hidden},
		{name: ".html only", in: "/.html", reason: hidden},
		{name: ".html after a slash", in: "/a/.html", reason: hidden},
		{name: "segment too long", in: "/" + longest + "a/", reason: "segment too long: 256 bytes (max 255)"},
		{name: "file name too long", in: "/" + longest[4:] + ".html", reason: "segment too long: 256 bytes (max 255)"},
		{name: "segment too long in Japanese", in: "/" + strings.Repeat("あ", 86) + "/", reason: "segment too long: 258 bytes (max 255)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := OutputFile(tt.in)
			msg := ""
			if tt.reason != "" {
				msg = "invalid path " + strconv.Quote(tt.in) + ": " + tt.reason
			}
			switch {
			case msg == "" && err != nil:
				t.Errorf("error = %q, want nil", err)
			case msg != "" && (err == nil || err.Error() != msg):
				t.Errorf("error = %v, want %q", err, msg)
			}
			if got != tt.want {
				t.Errorf("OutputFile(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// FuzzOutputFile checks that the name of an output file is the file of the path, and that its segments are not empty,
// hidden, "." or "..", which could leave the root, or too long.
func FuzzOutputFile(f *testing.F) {
	for _, s := range []string{"/", "/about/", "/2026/10/26/新サービス/", "/archives/123.html", "//", "/a/../b.html", "/.git/", "/a/.html", "a/b.html"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, path string) {
		name, err := OutputFile(path)
		if err != nil {
			return
		}
		if !strings.HasSuffix(name, ".html") || "/"+name != path && "/"+name != path+"index.html" {
			t.Fatalf("OutputFile(%q) = %q, which is not the file of the path", path, name)
		}
		for _, seg := range strings.Split(name, "/") {
			if seg == "" || strings.HasPrefix(seg, ".") || len(seg) > MaxFileNameBytes {
				t.Fatalf("OutputFile(%q) = %q, with the segment %q", path, name, seg)
			}
		}
	})
}
