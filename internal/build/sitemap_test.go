package build

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// urlSet is a sitemap that is not an index, as encoding/xml reads it.
type urlSet struct {
	XMLName xml.Name `xml:"http://www.sitemaps.org/schemas/sitemap/0.9 urlset"`
	URLs    []struct {
		Loc        string `xml:"http://www.sitemaps.org/schemas/sitemap/0.9 loc"`
		Alternates []struct {
			Rel      string `xml:"rel,attr"`
			Hreflang string `xml:"hreflang,attr"`
			Href     string `xml:"href,attr"`
		} `xml:"http://www.w3.org/1999/xhtml link"`
	} `xml:"http://www.sitemaps.org/schemas/sitemap/0.9 url"`
}

// sitemapIndex is a sitemap index as encoding/xml reads it.
type sitemapIndex struct {
	XMLName  xml.Name `xml:"http://www.sitemaps.org/schemas/sitemap/0.9 sitemapindex"`
	Sitemaps []struct {
		Loc string `xml:"http://www.sitemaps.org/schemas/sitemap/0.9 loc"`
	} `xml:"http://www.sitemaps.org/schemas/sitemap/0.9 sitemap"`
}

// readSitemap returns the URLs of the sitemap in the file name of dir, after checking that it is XML that encoding/xml
// reads, as texts such as "https://example.com/about/ ja=https://example.com/about/ en=https://example.com/en/about/".
func readSitemap(t *testing.T, dir, name string) []string {
	t.Helper()
	data := []byte(readFile(t, dir, name))
	checkXML(t, name, data)
	var s urlSet
	if err := xml.Unmarshal(data, &s); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var urls []string
	for _, u := range s.URLs {
		text := u.Loc
		for _, a := range u.Alternates {
			if a.Rel != "alternate" {
				t.Errorf("%s: rel %q of an alternate of %s", name, a.Rel, u.Loc)
			}
			text += " " + a.Hreflang + "=" + a.Href
		}
		urls = append(urls, text)
	}
	return urls
}

// mustWriteSitemap writes the sitemap of b into a new directory, which it returns.
func mustWriteSitemap(t *testing.T, b *builder) string {
	t.Helper()
	dir, pub := newPublic(t)
	if err := b.writeSitemap(pub); err != nil {
		t.Fatalf("writeSitemap() = %v", err)
	}
	return dir
}

// withAlternates returns url with the alternates in ja and en and x-default to ja, by their URLs, as readSitemap writes
// them.
func withAlternates(url, ja, en string) string {
	return url + " ja=" + ja + " en=" + en + " x-default=" + ja
}

// TestSitemap checks the sitemap of the fixture: the first pages of the entries and of the lists with entries, sorted
// by their URLs, which are percent-encoded, with the alternates of their heads; without the pages that are noindex or
// whose canonical URLs are others, the pages from 2, the lists without entries and the 404 pages.
func TestSitemap(t *testing.T) {
	src := testSource(t)
	src.addPosts(t, "ja", 2)
	settings := testSite()
	settings.PostsPerPage = 2
	src.putSettings(t, settings)
	src.editRevision(t, privacyID, func(r *content.Revision) { r.SEO.NoIndex = true })
	src.editRevision(t, aboutID, func(r *content.Revision) { r.SEO.Canonical = "https://other.example/about/" })
	// Canonical URLs that are those of the pages.
	src.editRevision(t, servicesID, func(r *content.Revision) { r.SEO.Canonical = "/services/" })
	src.editRevision(t, enAboutID, func(r *content.Revision) { r.SEO.Canonical = "https://example.com/en/about/" })
	empty := newTerm(1, content.TaxonomyTag, "ja", "empty")
	src.put(t, content.RefTerm, empty.ID, empty)
	b := mustLoad(t, src)
	for _, path := range []string{"/news/page/2/", "/category/news/page/2/", "/author/yamada/page/2/", "/tag/empty/",
		"/en/works/", "/privacy/", "/about/", "/404.html"} {
		if pageAt(b, path) == nil {
			t.Fatalf("no page at %s", path)
		}
	}
	dir := mustWriteSitemap(t, b)
	if got := listFiles(t, dir); !slices.Equal(got, []string{"sitemap.xml"}) {
		t.Errorf("files %q, want sitemap.xml", got)
	}

	const site = "https://example.com"
	want := []string{
		withAlternates(site+"/", site+"/", site+"/en/"),
		site + "/2026/09/01/post-1001/",
		site + "/2026/09/02/post-1002/",
		withAlternates(site+postPath, site+postPath, site+"/en/2026/10/26/new-service/"),
		withAlternates(site+"/author/yamada/", site+"/author/yamada/", site+"/en/author/yamada/"),
		site + "/category/news/",
		site + "/category/news/press-release/",
		site + "/contact/",
		withAlternates(site+"/en/", site+"/", site+"/en/"),
		withAlternates(site+"/en/2026/10/26/new-service/", site+postPath, site+"/en/2026/10/26/new-service/"),
		withAlternates(site+"/en/about/", site+"/about/", site+"/en/about/"),
		withAlternates(site+"/en/author/yamada/", site+"/author/yamada/", site+"/en/author/yamada/"),
		withAlternates(site+"/en/privacy/", site+"/privacy/", site+"/en/privacy/"),
		withAlternates(site+"/en/tag/events/", site+eventPath, site+"/en/tag/events/"),
		site + "/news/",
		site + "/services/",
		withAlternates(site+eventPath, site+eventPath, site+"/en/tag/events/"),
		withAlternates(site+"/works/", site+"/works/", site+"/en/works/"),
		site + "/works/example-corporate-site/",
		site + "/works_category/corporate-site/",
	}
	if got := readSitemap(t, dir, "sitemap.xml"); !slices.Equal(got, want) {
		t.Errorf("sitemap:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// The text: one element on each line.
	data := readFile(t, dir, "sitemap.xml")
	const start = `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xhtml="http://www.w3.org/1999/xhtml">
<url>
<loc>https://example.com/</loc>
<xhtml:link rel="alternate" hreflang="ja" href="https://example.com/"/>
<xhtml:link rel="alternate" hreflang="en" href="https://example.com/en/"/>
<xhtml:link rel="alternate" hreflang="x-default" href="https://example.com/"/>
</url>
<url>
<loc>https://example.com/2026/09/01/post-1001/</loc>
</url>
`
	if !strings.HasPrefix(data, start) || !strings.HasSuffix(data, "</url>\n</urlset>\n") {
		t.Errorf("sitemap.xml:\n%s", data)
	}

	// The alternates are those of the heads.
	newTestResolver(t, b)
	alternate := regexp.MustCompile(`^<link rel="alternate" hreflang="([^"]+)" href="([^"]+)">$`)
	var heads []string
	for _, x := range b.sitemapPages() {
		text := x.loc
		for line := range strings.SplitSeq(string(b.head(x.p)), "\n") {
			if m := alternate.FindStringSubmatch(line); m != nil {
				text += " " + m[1] + "=" + m[2]
			}
		}
		heads = append(heads, text)
	}
	if !slices.Equal(heads, want) {
		t.Errorf("the pages and the alternates of the heads:\n%s\nwant:\n%s", strings.Join(heads, "\n"),
			strings.Join(want, "\n"))
	}

	// The same again.
	if again := readFile(t, mustWriteSitemap(t, b), "sitemap.xml"); again != data {
		t.Errorf("another sitemap:\n%s", again)
	}
	if len(b.warnings.sorted()) > 0 {
		t.Errorf("warnings: %v", b.warnings.sorted())
	}
}

// TestSitemapNoIndex checks that a site with seo.noindex has no sitemap.
func TestSitemapNoIndex(t *testing.T) {
	src := testSource(t)
	settings := testSite()
	settings.SEO.NoIndex = true
	src.putSettings(t, settings)
	b := mustLoad(t, src)
	outputs := len(b.outputs)
	dir := mustWriteSitemap(t, b)
	if files := listFiles(t, dir); len(files) > 0 {
		t.Errorf("files %q, want none", files)
	}
	if outputAt(b, "/sitemap.xml") != nil || len(b.outputs) != outputs {
		t.Errorf("outputs of the sitemap: %v", b.outputs[outputs:])
	}
}

// TestSitemapEmpty checks the sitemap of a site without pages to list, whose only page is the latest posts at the front
// without posts: no URLs.
func TestSitemapEmpty(t *testing.T) {
	src := &memSource{
		docs:      make(map[content.RefKind]map[string]content.SiteFile),
		revisions: make(map[string]content.SiteFile),
		files:     fstest.MapFS{},
		theme:     testTheme(),
		opened:    make(map[string]int),
	}
	src.putSettings(t, &content.Site{
		Name:            content.Localized{"ja": "空のサイト"},
		URL:             "https://example.com",
		TimeZone:        "Asia/Tokyo",
		Languages:       []string{"ja"},
		DefaultLanguage: "ja",
	})
	src.schema = content.SiteFile{Name: "site/schema.yaml", Data: []byte("schemaVersion: 1\n")}
	b := mustLoad(t, src)
	want := `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xhtml="http://www.w3.org/1999/xhtml">
</urlset>
`
	if got := readFile(t, mustWriteSitemap(t, b), "sitemap.xml"); got != want {
		t.Errorf("sitemap.xml:\n%s\nwant:\n%s", got, want)
	}
}

// TestSitemapSplit checks a sitemap split by its number of URLs or of bytes into files, which a sitemap index lists,
// and which the check of the output paths checks too.
func TestSitemapSplit(t *testing.T) {
	src := testSource(t)
	settings := testSite()
	settings.URL += "/blog"
	src.putSettings(t, settings)
	src.addRedirect(t, 1, "/sitemap-2.xml", aboutID)
	full := readSitemap(t, mustWriteSitemap(t, mustLoad(t, src)), "sitemap.xml")
	if len(full) != 20 {
		t.Fatalf("%d URLs in the sitemap, want 20", len(full))
	}
	defer func(urls, bytes int) { maxSitemapURLs, maxSitemapBytes = urls, bytes }(maxSitemapURLs, maxSitemapBytes)
	for _, tt := range []struct {
		name  string
		urls  int
		bytes int
		parts []int // the URLs in each file
	}{
		{name: "20 URLs at most", urls: 20, bytes: 50 << 20, parts: []int{20}},
		{name: "4 URLs at most", urls: 4, bytes: 50 << 20, parts: []int{4, 4, 4, 4, 4}},
		{name: "3 URLs at most", urls: 3, bytes: 50 << 20, parts: []int{3, 3, 3, 3, 3, 3, 2}},
		// The URLs with alternates have about 300 bytes, the others 60, and a file 153 more.
		{name: "1000 bytes at most", urls: 50000, bytes: 1000, parts: []int{2, 4, 3, 2, 3, 3, 3}},
		// The URL of /en/tag/events/ fits after that of /en/privacy/ (867 bytes), but not with the end of the file.
		{name: "875 bytes at most", urls: 50000, bytes: 875, parts: []int{1, 1, 2, 4, 1, 2, 1, 2, 2, 1, 3}},
		// The third file, with /about/ and /author/yamada/, has 815 bytes.
		{name: "815 bytes at most", urls: 50000, bytes: 815, parts: []int{1, 1, 2, 4, 1, 1, 1, 1, 2, 2, 1, 3}},
		// Each URL is larger: a file has at least one.
		{name: "1 byte at most", urls: 50000, bytes: 1, parts: slices.Repeat([]int{1}, 20)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			maxSitemapURLs, maxSitemapBytes = tt.urls, tt.bytes
			b := mustLoad(t, src)
			mustResolve(t, b)
			var p problems
			b.checkPaths(&p)
			if p.len() > 0 {
				t.Fatalf("checkPaths() before the sitemap: %v", p.sorted())
			}
			outputs := len(b.outputs)
			dir := mustWriteSitemap(t, b)

			files := []string{"sitemap.xml"} // with the URLs
			if len(tt.parts) > 1 {
				data := []byte(readFile(t, dir, "sitemap.xml"))
				checkXML(t, "sitemap.xml", data)
				var index sitemapIndex
				if err := xml.Unmarshal(data, &index); err != nil {
					t.Fatalf("sitemap.xml: %v", err)
				}
				files = nil
				for i, s := range index.Sitemaps {
					name := fmt.Sprintf("sitemap-%d.xml", i+1)
					if s.Loc != "https://example.com/blog/"+name {
						t.Errorf("part %d at %s", i+1, s.Loc)
					}
					files = append(files, name)
				}
			}
			var urls []string
			var parts []int
			for i, name := range files {
				part := readSitemap(t, dir, name)
				if size := len(readFile(t, dir, name)); size > tt.bytes && len(part) > 1 {
					t.Errorf("%s has %d bytes and %d URLs", name, size, len(part))
				}
				urls = append(urls, part...)
				parts = append(parts, len(part))
				if len(tt.parts) == 1 {
					continue
				}
				o := b.outputs[outputs+i]
				if o.path != "/"+name || o.file != name || o.src.file.Name != "" || o.src.pointer != "" ||
					o.src.what != "the sitemap" || o.rank != rankCore {
					t.Errorf("output %d: %+v", i, o)
				}
			}
			if !slices.Equal(urls, full) || !slices.Equal(parts, tt.parts) {
				t.Errorf("URLs in the files %v, want %v:\n%s", parts, tt.parts, strings.Join(urls, "\n"))
			}
			wantFiles := slices.Clone(files)
			if len(tt.parts) > 1 {
				wantFiles = append(wantFiles, "sitemap.xml")
			}
			slices.Sort(wantFiles)
			if got := listFiles(t, dir); !slices.Equal(got, wantFiles) {
				t.Errorf("files %q, want %q", got, wantFiles)
			}
			if n := len(b.outputs) - outputs; len(tt.parts) == 1 && n != 0 || len(tt.parts) > 1 && n != len(tt.parts) {
				t.Errorf("%d outputs added", n)
			}

			b.checkPaths(&p)
			var want []string
			if len(tt.parts) > 1 {
				want = []string{fileOf(content.RefRedirect, newID("rdr", 1)) +
					`:4:3: /from: from "/sitemap-2.xml" is the output path "/sitemap-2.xml" of the sitemap`}
			}
			var got []string
			for _, e := range p.sorted() {
				got = append(got, e.Error())
			}
			if !slices.Equal(got, want) {
				t.Errorf("checkPaths() after the sitemap:\n%s\nwant:\n%s", strings.Join(got, "\n"),
					strings.Join(want, "\n"))
			}
		})
	}
}

// TestSitemapLongURL checks that the pages whose URLs have 2048 characters or more are left out of the sitemap, with
// warnings where the pages come from.
func TestSitemapLongURL(t *testing.T) {
	src := testSource(t)
	settings := testSite()
	settings.URL = "https://example.com/" + strings.Repeat("a", 2021) // 2041 characters
	src.putSettings(t, settings)
	b := mustLoad(t, src)
	site := settings.URL
	want := []string{
		withAlternates(site+"/", site+"/", site+"/en/"),    // 2042 characters
		withAlternates(site+"/en/", site+"/", site+"/en/"), // 2045
		site + "/news/", // 2047
	}
	if got := readSitemap(t, mustWriteSitemap(t, b), "sitemap.xml"); !slices.Equal(got, want) {
		t.Errorf("sitemap:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	warnings := warningTexts(b)
	for _, w := range []string{
		revisionFile(aboutID) + ":4:3: /slug: the URL of the page /about/ has 2048 characters, " +
			"more than a sitemap takes (2047); the page is left out of it",
		"site/schema.yaml:8:5: /types/works/archive: the URL of the page /works/ has 2048 characters, " +
			"more than a sitemap takes (2047); the page is left out of it",
		fileOf(content.RefAuthor, authorID) + ":5:3: /slug: the URL of the page /en/author/yamada/ " +
			"has 2059 characters, more than a sitemap takes (2047); the page is left out of it",
	} {
		if !slices.Contains(warnings, w) {
			t.Errorf("no warning %s in\n%s", w, strings.Join(warnings, "\n"))
		}
	}
	// Each page left out once: the pages that the sitemap would list but the three in it.
	n := 0
	for _, p := range b.pages {
		if b.inSitemap(p) {
			n++
		}
	}
	if len(warnings) != n-len(want) {
		t.Errorf("%d warnings for %d pages left out:\n%s", len(warnings), n-len(want), strings.Join(warnings, "\n"))
	}
}

// TestSitemapWriteErrors checks that writing the sitemap fails on a file that exists: sitemap.xml, a part of a split
// sitemap, its last part, or sitemap.xml after the parts.
func TestSitemapWriteErrors(t *testing.T) {
	src := testSource(t)
	defer func(urls int) { maxSitemapURLs = urls }(maxSitemapURLs)
	for _, tt := range []struct {
		urls int
		file string
	}{
		{urls: 50000, file: "sitemap.xml"},
		{urls: 3, file: "sitemap-2.xml"},
		{urls: 3, file: "sitemap-7.xml"}, // of 20 URLs
		{urls: 3, file: "sitemap.xml"},
	} {
		maxSitemapURLs = tt.urls
		b := mustLoad(t, src)
		_, pub := newPublic(t)
		if err := writeFile(pub, tt.file, nil); err != nil {
			t.Fatal(err)
		}
		if err := b.writeSitemap(pub); !errors.Is(err, fs.ErrExist) {
			t.Errorf("writeSitemap() with %d URLs at most over %s = %v", tt.urls, tt.file, err)
		}
	}
}
