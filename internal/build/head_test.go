package build

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/imaging"
	"github.com/WhiteRoomLab/whiteroom/internal/mediafile"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// Percent-encoded paths of pages of the fixture.
const (
	postPath  = "/2026/10/26/%E6%96%B0%E3%82%B5%E3%83%BC%E3%83%93%E3%82%B9/" // 新サービス
	eventPath = "/tag/%E3%82%A4%E3%83%99%E3%83%B3%E3%83%88/"                 // イベント
)

// headSource returns the fixture with 11 more posts in each language, the originals of the logo (a PNG of 400x100
// pixels), the icon (a PNG of 512x512 pixels), the image for Open Graph in ja (a JPEG of 1200x630 pixels), the avatar
// (a PNG of 256x256 pixels) and the image of 6.11.5 (a JPEG of 1200x800 pixels with an alt in ja and en), and with:
//   - a seo.title, an excerpt on two lines, and the image of 6.11.5 as the featured image, of the post in ja;
//   - the date of the post in en in UTC, the same instant as that of the post in ja;
//   - a seo.title of the about page in ja, and of the front page in ja, which Open Graph does not use;
//   - a link of the author with a path that is not ASCII.
func headSource(t *testing.T) *memSource {
	t.Helper()
	src := testSource(t)
	src.addPosts(t, "ja", 11)
	src.addPosts(t, "en", 11)
	src.putOriginal(t, logoID, "logo.png", testPNG(t, 400, 100))
	src.putOriginal(t, iconID, "icon.png", testPNG(t, 512, 512))
	src.putOriginal(t, ogImageID, "ogp.jpg", testJPEG(t, 1200, 630))
	src.putOriginal(t, avatarID, "yamada.png", testPNG(t, 256, 256))
	src.putOriginal(t, imageID, "会社外観.jpg", testJPEG(t, 1200, 800))
	edit(t, src, content.RefMedia, imageID, func(m *content.Media) {
		m.Alt = content.Localized{"ja": "会社外観", "en": "Our office building"}
	})
	src.editRevision(t, postID, func(r *content.Revision) {
		r.SEO.Title = "新サービスのお知らせ"
		r.Excerpt = "新しいサービスの\nお知らせです。"
		r.FeaturedImage = imageID
	})
	src.editRevision(t, enPostID, func(r *content.Revision) { r.Date = "2026-10-25T15:30:00Z" })
	src.editRevision(t, aboutID, func(r *content.Revision) { r.SEO.Title = "株式会社〇〇について" })
	src.editRevision(t, homeID, func(r *content.Revision) { r.SEO.Title = "トップ" })
	edit(t, src, content.RefAuthor, authorID, func(a *content.AuthorProfile) {
		a.Links = append(a.Links, "https://example.com/著者/")
	})
	return src
}

// mediaURL returns the URL of the file name of the medium id of b, whose variant is v, with its version, on the site
// https://example.com.
func mediaURL(b *builder, id, name string, v imaging.Variant) string {
	return "https://example.com/media/" + id + "/" + name + "?v=" + imaging.Version(b.media[id].v.Hash, v)
}

func pngVariant(w, h int) imaging.Variant {
	return imaging.Variant{Width: w, Height: h, Format: mediafile.PNG}
}
func jpegVariant(w, h int) imaging.Variant {
	return imaging.Variant{Width: w, Height: h, Format: mediafile.JPEG}
}

// headLines returns the lines of the head of the page at path.
func headLines(t *testing.T, b *builder, path string) []string {
	t.Helper()
	p := pageAt(b, path)
	if p == nil {
		t.Fatalf("no page at %s", path)
	}
	return strings.Split(string(b.head(p)), "\n")
}

// checkHeads checks the lines of the heads of the pages of b by path.
func checkHeads(t *testing.T, b *builder, want map[string][]string) {
	t.Helper()
	for _, path := range slices.Sorted(maps.Keys(want)) {
		if got := headLines(t, b, path); !slices.Equal(got, want[path]) {
			t.Errorf("head of %s:\n%s\nwant:\n%s", path, strings.Join(got, "\n"), strings.Join(want[path], "\n"))
		}
	}
}

// Lines of heads, with their values as the heads write them (escaped).
func canonicalLine(url string) string { return `<link rel="canonical" href="` + url + `">` }
func descriptionLine(s string) string { return `<meta name="description" content="` + s + `">` }
func ogLine(property, s string) string {
	return `<meta property="` + property + `" content="` + s + `">`
}
func jsonLDLine(js string) string { return `<script type="application/ld+json">` + js + `</script>` }

func feedLine(siteName, url string) string {
	return `<link rel="alternate" type="application/rss+xml" title="` + siteName + `" href="` + url + `">`
}

// alternateLines returns the alternates for hreflang of a page in ja and en, with x-default to ja, by their URLs.
func alternateLines(ja, en string) []string {
	return []string{
		`<link rel="alternate" hreflang="ja" href="` + ja + `">`,
		`<link rel="alternate" hreflang="en" href="` + en + `">`,
		`<link rel="alternate" hreflang="x-default" href="` + ja + `">`,
	}
}

const (
	robotsLine  = `<meta name="robots" content="noindex">`
	summaryLine = `<meta name="twitter:card" content="summary">`
	largeLine   = `<meta name="twitter:card" content="summary_large_image">`
)

// The links to the feeds of the fixture.
var (
	jaFeedLine = feedLine("株式会社〇〇", "https://example.com/feed/")
	enFeedLine = feedLine("Example Inc.", "https://example.com/en/feed/")
)

// TestHead checks the heads of pages of every kind, and the media that they publish.
func TestHead(t *testing.T) {
	b := mustLoad(t, headSource(t))
	newTestResolver(t, b)
	const site = "https://example.com"
	ogp := mediaURL(b, ogImageID, "ogp.jpg", jpegVariant(1200, 630))
	office := mediaURL(b, imageID, officeName+".jpg", jpegVariant(1200, 800))
	logo := mediaURL(b, logoID, "logo.png", pngVariant(400, 100))
	avatar := mediaURL(b, avatarID, "yamada.png", pngVariant(256, 256))
	icons := []string{
		`<link rel="icon" href="` + mediaURL(b, iconID, "icon-32x32.png", pngVariant(32, 32)) +
			`" sizes="32x32" type="image/png">`,
		`<link rel="icon" href="` + mediaURL(b, iconID, "icon-192x192.png", pngVariant(192, 192)) +
			`" sizes="192x192" type="image/png">`,
		`<link rel="icon" href="` + mediaURL(b, iconID, "icon.png", pngVariant(512, 512)) +
			`" sizes="512x512" type="image/png">`,
		`<link rel="apple-touch-icon" href="` + mediaURL(b, iconID, "icon-180x180.png", pngVariant(180, 180)) + `">`,
	}
	// The image for Open Graph of the site in ja, and the Twitter card with it.
	siteImage := []string{
		ogLine("og:image", ogp), ogLine("og:image:width", "1200"), ogLine("og:image:height", "630"), largeLine,
	}
	// lines returns the lines of groups, one after another.
	lines := func(groups ...[]string) []string { return slices.Concat(groups...) }
	const enPost = site + "/en/2026/10/25/new-service/"
	sameAs := `"sameAs":["https://x.com/example","https://example.com/","https://example.com/%E8%91%97%E8%80%85/"]`
	jaAuthor := `{"@type":"Person","name":"山田 太郎","url":"https://example.com/author/yamada/",` + sameAs
	enAuthor := `{"@type":"Person","name":"山田 太郎","url":"https://example.com/en/author/yamada/",` + sameAs
	newsDescription := "会社からのお知らせです。"
	bio := "編集長。Webデザインとアクセシビリティについて書いています。 著書に『はじめてのWebアクセシビリティ』があります。"

	checkHeads(t, b, map[string][]string{
		// The description from the excerpt, the seo.title for Open Graph, the featured image with its alt in ja,
		// BlogPosting with the title.
		"/2026/10/26/新サービス/": lines(
			[]string{descriptionLine("新しいサービスの お知らせです。"), canonicalLine(site + postPath)},
			alternateLines(site+postPath, enPost),
			[]string{jaFeedLine}, icons,
			[]string{
				ogLine("og:type", "article"),
				ogLine("og:title", "新サービスのお知らせ"),
				ogLine("og:description", "新しいサービスの お知らせです。"),
				ogLine("og:url", site+postPath),
				ogLine("og:site_name", "株式会社〇〇"),
				ogLine("og:image", office),
				ogLine("og:image:width", "1200"),
				ogLine("og:image:height", "800"),
				ogLine("og:image:alt", "会社外観"),
				largeLine,
				jsonLDLine(`{"@context":"https://schema.org","@type":"BlogPosting","headline":"新サービスを始めました",` +
					`"url":"` + site + postPath + `","datePublished":"2026-10-26T00:30:00+09:00","author":` + jaAuthor +
					`},"image":"` + office + `","description":"新しいサービスの お知らせです。","inLanguage":"ja",` +
					`"publisher":{"@type":"Organization","name":"株式会社〇〇","logo":"` + logo + `"}}`),
			},
		),
		// The date in the time zone of the site; no image for Open Graph in en.
		"/en/2026/10/25/new-service/": lines(
			[]string{descriptionLine("A service to update and run your website."), canonicalLine(enPost)},
			alternateLines(site+postPath, enPost),
			[]string{enFeedLine}, icons,
			[]string{
				ogLine("og:type", "article"),
				ogLine("og:title", "We started a new service"),
				ogLine("og:description", "A service to update and run your website."),
				ogLine("og:url", enPost),
				ogLine("og:site_name", "Example Inc."),
				summaryLine,
				jsonLDLine(`{"@context":"https://schema.org","@type":"BlogPosting",` +
					`"headline":"We started a new service",` +
					`"url":"` + enPost + `","datePublished":"2026-10-26T00:30:00+09:00","author":` + enAuthor +
					`},"description":"A service to update and run your website.","inLanguage":"en",` +
					`"publisher":{"@type":"Organization","name":"Example Inc.","logo":"` + logo + `"}}`),
			},
		),
		// The static front page: the site name for Open Graph, WebSite.
		"/": lines(
			[]string{descriptionLine("Webサイトの企画と制作の会社です。"), canonicalLine(site + "/")},
			alternateLines(site+"/", site+"/en/"),
			[]string{jaFeedLine}, icons,
			[]string{
				ogLine("og:type", "website"),
				ogLine("og:title", "株式会社〇〇"),
				ogLine("og:description", "Webサイトの企画と制作の会社です。"),
				ogLine("og:url", site+"/"),
				ogLine("og:site_name", "株式会社〇〇"),
			},
			siteImage,
			[]string{jsonLDLine(`{"@context":"https://schema.org","@type":"WebSite","name":"株式会社〇〇",` +
				`"url":"https://example.com/","inLanguage":"ja"}`)},
		),
		// The latest posts at the front in en, without a description.
		"/en/": lines(
			[]string{canonicalLine(site + "/en/")},
			alternateLines(site+"/", site+"/en/"),
			[]string{enFeedLine}, icons,
			[]string{
				ogLine("og:type", "website"),
				ogLine("og:title", "Example Inc."),
				ogLine("og:url", site+"/en/"),
				ogLine("og:site_name", "Example Inc."),
				summaryLine,
				jsonLDLine(`{"@context":"https://schema.org","@type":"WebSite","name":"Example Inc.",` +
					`"url":"https://example.com/en/","inLanguage":"en"}`),
			},
		),
		// Its second page: canonical to itself, without alternates; WebSite of the front page.
		"/en/page/2/": lines(
			[]string{canonicalLine(site + "/en/page/2/"), enFeedLine}, icons,
			[]string{
				ogLine("og:type", "website"),
				ogLine("og:title", "Example Inc."),
				ogLine("og:url", site+"/en/page/2/"),
				ogLine("og:site_name", "Example Inc."),
				summaryLine,
				jsonLDLine(`{"@context":"https://schema.org","@type":"WebSite","name":"Example Inc.",` +
					`"url":"https://example.com/en/","inLanguage":"en"}`),
			},
		),
		// The seo.title for Open Graph, the image of the site; WebPage with the title.
		"/about/": lines(
			[]string{descriptionLine("株式会社〇〇の会社概要です。"), canonicalLine(site + "/about/")},
			alternateLines(site+"/about/", site+"/en/about/"),
			[]string{jaFeedLine}, icons,
			[]string{
				ogLine("og:type", "website"),
				ogLine("og:title", "株式会社〇〇について"),
				ogLine("og:description", "株式会社〇〇の会社概要です。"),
				ogLine("og:url", site+"/about/"),
				ogLine("og:site_name", "株式会社〇〇"),
			},
			siteImage,
			[]string{jsonLDLine(`{"@context":"https://schema.org","@type":"WebPage","name":"会社概要",` +
				`"url":"https://example.com/about/","description":"株式会社〇〇の会社概要です。","inLanguage":"ja"}`)},
		),
		// The second page of the posts page, which has no translation: CollectionPage.
		"/news/page/2/": lines(
			[]string{descriptionLine(newsDescription), canonicalLine(site + "/news/page/2/"), jaFeedLine}, icons,
			[]string{
				ogLine("og:type", "website"),
				ogLine("og:title", "お知らせ"),
				ogLine("og:description", newsDescription),
				ogLine("og:url", site+"/news/page/2/"),
				ogLine("og:site_name", "株式会社〇〇"),
			},
			siteImage,
			[]string{jsonLDLine(`{"@context":"https://schema.org","@type":"CollectionPage","name":"お知らせ",` +
				`"url":"https://example.com/news/page/2/","description":"` + newsDescription + `","inLanguage":"ja"}`)},
		),
		// A term list and its second page, without alternates.
		"/en/tag/events/": lines(
			[]string{canonicalLine(site + "/en/tag/events/")},
			alternateLines(site+eventPath, site+"/en/tag/events/"),
			[]string{enFeedLine}, icons,
			[]string{
				ogLine("og:type", "website"),
				ogLine("og:title", "Events"),
				ogLine("og:url", site+"/en/tag/events/"),
				ogLine("og:site_name", "Example Inc."),
				summaryLine,
				jsonLDLine(`{"@context":"https://schema.org","@type":"CollectionPage","name":"Events",` +
					`"url":"https://example.com/en/tag/events/","inLanguage":"en"}`),
			},
		),
		"/en/tag/events/page/2/": lines(
			[]string{canonicalLine(site + "/en/tag/events/page/2/"), enFeedLine}, icons,
			[]string{
				ogLine("og:type", "website"),
				ogLine("og:title", "Events"),
				ogLine("og:url", site+"/en/tag/events/page/2/"),
				ogLine("og:site_name", "Example Inc."),
				summaryLine,
				jsonLDLine(`{"@context":"https://schema.org","@type":"CollectionPage","name":"Events",` +
					`"url":"https://example.com/en/tag/events/page/2/","inLanguage":"en"}`),
			},
		),
		// The list of a type.
		"/works/": lines(
			[]string{canonicalLine(site + "/works/")},
			alternateLines(site+"/works/", site+"/en/works/"),
			[]string{jaFeedLine}, icons,
			[]string{
				ogLine("og:type", "website"),
				ogLine("og:title", "制作実績"),
				ogLine("og:url", site+"/works/"),
				ogLine("og:site_name", "株式会社〇〇"),
			},
			siteImage,
			[]string{jsonLDLine(`{"@context":"https://schema.org","@type":"CollectionPage","name":"制作実績",` +
				`"url":"https://example.com/works/","inLanguage":"ja"}`)},
		),
		// ProfilePage, with the avatar.
		"/author/yamada/": lines(
			[]string{descriptionLine(bio), canonicalLine(site + "/author/yamada/")},
			alternateLines(site+"/author/yamada/", site+"/en/author/yamada/"),
			[]string{jaFeedLine}, icons,
			[]string{
				ogLine("og:type", "website"),
				ogLine("og:title", "山田 太郎"),
				ogLine("og:description", bio),
				ogLine("og:url", site+"/author/yamada/"),
				ogLine("og:site_name", "株式会社〇〇"),
			},
			siteImage,
			[]string{jsonLDLine(`{"@context":"https://schema.org","@type":"ProfilePage","mainEntity":` + jaAuthor +
				`,"image":"` + avatar + `"}}`)},
		),
		// Robots noindex, the feed and the icons alone.
		"/404.html":    lines([]string{robotsLine, jaFeedLine}, icons),
		"/en/404.html": lines([]string{robotsLine, enFeedLine}, icons),
	})

	// The heads ask for the published files of their images and for the icons, and the media publish them.
	for _, p := range b.pages {
		b.head(p)
	}
	want := []string{
		"media/" + imageID + "/会社外観.jpg",
		"media/" + avatarID + "/yamada.png",
		"media/" + logoID + "/logo.png",
		"media/" + iconID + "/icon-32x32.png",
		"media/" + iconID + "/icon-180x180.png",
		"media/" + iconID + "/icon-192x192.png",
		"media/" + iconID + "/icon.png",
		"media/" + ogImageID + "/ogp.jpg",
	}
	slices.Sort(want)
	if got := listFiles(t, mustPublish(t, b)); !slices.Equal(got, want) {
		t.Errorf("published media:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestHeadNoIndex checks robots noindex on every page of a site with seo.noindex, after the canonical URL.
func TestHeadNoIndex(t *testing.T) {
	src := testSource(t)
	site := testSite()
	site.SEO.NoIndex = true
	src.putSettings(t, site)
	b := mustLoad(t, src)
	newTestResolver(t, b)
	for _, p := range b.pages {
		lines := strings.Split(string(b.head(p)), "\n")
		i := slices.Index(lines, robotsLine)
		previous := ""
		if i > 0 {
			previous = lines[i-1]
		}
		switch {
		case i < 0 || slices.Contains(lines[i+1:], robotsLine):
			t.Errorf("%s: robots noindex not once in\n%s", p.path, strings.Join(lines, "\n"))
		case p.kind == theme.KindNotFound && i != 0,
			p.kind != theme.KindNotFound && !strings.HasPrefix(previous, `<link rel="canonical" `):
			t.Errorf("%s: robots noindex at line %d of\n%s", p.path, i, strings.Join(lines, "\n"))
		}
	}
}

// TestHeadCanonical checks the canonical URLs of a site under a path, which og:url and the structured data have too:
// seo.canonical as a path of the site, encoded or not, and as a URL; that of a posts page on its first page only; and
// seo.noindex of a posts page on all its pages.
func TestHeadCanonical(t *testing.T) {
	src := testSource(t)
	src.addPosts(t, "ja", 11)
	settings := testSite()
	settings.URL += "/blog"
	src.putSettings(t, settings)
	src.editRevision(t, aboutID, func(r *content.Revision) { r.SEO.Canonical = "/会社/" })
	src.editRevision(t, privacyID, func(r *content.Revision) { r.SEO.Canonical = "/%E4%BC%9A%E7%A4%BE/" })
	src.editRevision(t, enAboutID, func(r *content.Revision) { r.SEO.Canonical = "https://other.example/会社/?q=a&r=b" })
	src.editRevision(t, newsID, func(r *content.Revision) {
		r.SEO.Canonical = "http://other.example/news/"
		r.SEO.NoIndex = true
	})
	src.editRevision(t, postID, func(r *content.Revision) { r.SEO.Canonical = "/2026/10/26/new-service/" })
	b := mustLoad(t, src)
	newTestResolver(t, b)
	const (
		site    = "https://example.com/blog"
		company = "/%E4%BC%9A%E7%A4%BE/" // 会社
		other   = "https://other.example" + company + "?q=a&amp;r=b"
		post    = site + "/2026/10/26/new-service/"
	)
	amp := "\\" + "u0026"
	newsDescription := "会社からのお知らせです。"
	postDescription := "Webサイトの更新と運用をお手伝いするサービスです。"
	// The originals of the icon, the logo and the image for Open Graph are missing: the heads have none of them.
	checkHeads(t, b, map[string][]string{
		"/2026/10/26/新サービス/": slices.Concat(
			[]string{descriptionLine(postDescription), canonicalLine(post)},
			alternateLines(site+postPath, site+"/en/2026/10/26/new-service/"),
			[]string{
				feedLine("株式会社〇〇", site+"/feed/"),
				ogLine("og:type", "article"),
				ogLine("og:title", "新サービスを始めました"),
				ogLine("og:description", postDescription),
				ogLine("og:url", post),
				ogLine("og:site_name", "株式会社〇〇"),
				summaryLine,
				jsonLDLine(`{"@context":"https://schema.org","@type":"BlogPosting","headline":"新サービスを始めました",` +
					`"url":"` + post + `","datePublished":"2026-10-26T00:30:00+09:00","author":{"@type":"Person",` +
					`"name":"山田 太郎","url":"` + site + `/author/yamada/","sameAs":["https://x.com/example",` +
					`"https://example.com/"]},"description":"` + postDescription + `","inLanguage":"ja",` +
					`"publisher":{"@type":"Organization","name":"株式会社〇〇"}}`),
			},
		),
		"/about/": slices.Concat(
			[]string{descriptionLine("株式会社〇〇の会社概要です。"), canonicalLine(site + company)},
			alternateLines(site+"/about/", site+"/en/about/"),
			[]string{
				feedLine("株式会社〇〇", site+"/feed/"),
				ogLine("og:type", "website"),
				ogLine("og:title", "会社概要"),
				ogLine("og:description", "株式会社〇〇の会社概要です。"),
				ogLine("og:url", site+company),
				ogLine("og:site_name", "株式会社〇〇"),
				summaryLine,
				jsonLDLine(`{"@context":"https://schema.org","@type":"WebPage","name":"会社概要","url":"` + site + company +
					`","description":"株式会社〇〇の会社概要です。","inLanguage":"ja"}`),
			},
		),
		"/en/about/": slices.Concat(
			[]string{descriptionLine("We plan and make websites."), canonicalLine(other)},
			alternateLines(site+"/about/", site+"/en/about/"),
			[]string{
				feedLine("Example Inc.", site+"/en/feed/"),
				ogLine("og:type", "website"),
				ogLine("og:title", "About us"),
				ogLine("og:description", "We plan and make websites."),
				ogLine("og:url", other),
				ogLine("og:site_name", "Example Inc."),
				summaryLine,
				jsonLDLine(`{"@context":"https://schema.org","@type":"WebPage","name":"About us",` +
					`"url":"https://other.example` + company + `?q=a` + amp + `r=b",` +
					`"description":"We plan and make websites.","inLanguage":"en"}`),
			},
		),
		"/news/": {
			descriptionLine(newsDescription),
			canonicalLine("http://other.example/news/"),
			robotsLine,
			feedLine("株式会社〇〇", site+"/feed/"),
			ogLine("og:type", "website"),
			ogLine("og:title", "お知らせ"),
			ogLine("og:description", newsDescription),
			ogLine("og:url", "http://other.example/news/"),
			ogLine("og:site_name", "株式会社〇〇"),
			summaryLine,
			jsonLDLine(`{"@context":"https://schema.org","@type":"CollectionPage","name":"お知らせ",` +
				`"url":"http://other.example/news/","description":"` + newsDescription + `","inLanguage":"ja"}`),
		},
		"/news/page/2/": {
			descriptionLine(newsDescription),
			canonicalLine(site + "/news/page/2/"),
			robotsLine,
			feedLine("株式会社〇〇", site+"/feed/"),
			ogLine("og:type", "website"),
			ogLine("og:title", "お知らせ"),
			ogLine("og:description", newsDescription),
			ogLine("og:url", site+"/news/page/2/"),
			ogLine("og:site_name", "株式会社〇〇"),
			summaryLine,
			jsonLDLine(`{"@context":"https://schema.org","@type":"CollectionPage","name":"お知らせ",` +
				`"url":"` + site + `/news/page/2/","description":"` + newsDescription + `","inLanguage":"ja"}`),
		},
	})
	// Encoded once.
	if got := headLines(t, b, "/privacy/"); !slices.Contains(got, canonicalLine(site+company)) {
		t.Errorf("head of /privacy/ without the canonical URL %s:\n%s", site+company, strings.Join(got, "\n"))
	}
	if b.canonical(pageAt(b, "/404.html")) != "" {
		t.Error("the 404 page has a canonical URL")
	}
}

// TestIconLinks checks the links to the icons of the site: their sizes are those of their files, and each file is
// linked once as an icon.
func TestIconLinks(t *testing.T) {
	tests := []struct {
		name     string
		fileName string
		original func(t testing.TB) []byte // nil for none
		noIcon   bool
		want     func(b *builder) []string
	}{
		{
			name:     "smaller than 180 pixels",
			fileName: "icon.png",
			original: func(t testing.TB) []byte { return testPNG(t, 64, 64) },
			want: func(b *builder) []string {
				published := mediaURL(b, iconID, "icon.png", pngVariant(64, 64))
				return []string{
					`<link rel="icon" href="` + mediaURL(b, iconID, "icon-32x32.png", pngVariant(32, 32)) +
						`" sizes="32x32" type="image/png">`,
					`<link rel="icon" href="` + published + `" sizes="64x64" type="image/png">`,
					`<link rel="apple-touch-icon" href="` + published + `">`,
				}
			},
		},
		{
			name:     "not square, a JPEG",
			fileName: "icon.jpg",
			original: func(t testing.TB) []byte { return testJPEG(t, 100, 50) },
			want: func(b *builder) []string {
				small := mediaURL(b, iconID, "icon-50x50.png", pngVariant(50, 50))
				return []string{
					`<link rel="icon" href="` + mediaURL(b, iconID, "icon-32x32.png", pngVariant(32, 32)) +
						`" sizes="32x32" type="image/png">`,
					`<link rel="icon" href="` + small + `" sizes="50x50" type="image/png">`,
					`<link rel="apple-touch-icon" href="` + small + `">`,
				}
			},
		},
		{name: "without its original", want: func(*builder) []string { return nil }},
		{name: "no icon", noIcon: true, want: func(*builder) []string { return nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := testSource(t)
			if tt.original != nil {
				src.putOriginal(t, iconID, tt.fileName, tt.original(t))
			}
			if tt.noIcon {
				site := testSite()
				site.Icon = ""
				src.putSettings(t, site)
			}
			b := mustLoad(t, src)
			newTestResolver(t, b)
			if got, want := b.iconLinks(), tt.want(b); !slices.Equal(got, want) {
				t.Errorf("iconLinks() =\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}

// TestOGImage checks the image for Open Graph of pages: the seo.ogImage of their entries, else their featured images,
// else the image of the site in their languages, with the alt in their languages.
func TestOGImage(t *testing.T) {
	tests := []struct {
		name   string
		change func(t *testing.T, s *memSource)
		path   string
		want   string // the ID of the image, or "" for none
		alt    string
	}{
		{
			name: "seo.ogImage",
			change: func(t *testing.T, s *memSource) {
				s.editRevision(t, postID, func(r *content.Revision) {
					r.SEO.OGImage, r.FeaturedImage = avatarID, imageID
				})
			},
			path: "/2026/10/26/新サービス/",
			want: avatarID,
		},
		{
			name: "the featured image",
			change: func(t *testing.T, s *memSource) {
				s.editRevision(t, postID, func(r *content.Revision) { r.FeaturedImage = imageID })
			},
			path: "/2026/10/26/新サービス/",
			want: imageID,
			alt:  "会社外観",
		},
		{
			name: "the featured image in en",
			change: func(t *testing.T, s *memSource) {
				s.editRevision(t, enPostID, func(r *content.Revision) { r.FeaturedImage = imageID })
			},
			path: "/en/2026/10/26/new-service/",
			want: imageID,
			alt:  "Our office building",
		},
		{name: "the image of the site", path: "/2026/10/26/新サービス/", want: ogImageID},
		{name: "no image of the site in en", path: "/en/2026/10/26/new-service/"},
		{
			name: "an image of the site in en",
			change: func(t *testing.T, s *memSource) {
				site := testSite()
				site.SEO.OGImage["en"] = avatarID
				s.putSettings(t, site)
			},
			path: "/en/2026/10/26/new-service/",
			want: avatarID,
		},
		{name: "a term list", path: "/category/news/", want: ogImageID},
		{
			name: "the featured image of a posts page",
			change: func(t *testing.T, s *memSource) {
				s.editRevision(t, newsID, func(r *content.Revision) { r.FeaturedImage = imageID })
			},
			path: "/news/",
			want: imageID,
			alt:  "会社外観",
		},
		{
			name: "an original that cannot be inspected",
			change: func(t *testing.T, s *memSource) {
				s.editRevision(t, postID, func(r *content.Revision) { r.SEO.OGImage = logoID })
			},
			path: "/2026/10/26/新サービス/",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := testSource(t)
			src.putOriginal(t, imageID, "会社外観.jpg", testJPEG(t, 1200, 800))
			edit(t, src, content.RefMedia, imageID, func(m *content.Media) {
				m.Alt = content.Localized{"ja": "会社外観", "en": "Our office building"}
			})
			src.putOriginal(t, avatarID, "yamada.png", testPNG(t, 256, 256))
			src.putOriginal(t, ogImageID, "ogp.jpg", testJPEG(t, 1200, 630))
			if tt.change != nil {
				tt.change(t, src)
			}
			b := mustLoad(t, src)
			newTestResolver(t, b)
			office := mediaURL(b, imageID, officeName+".jpg", jpegVariant(1200, 800))
			files := map[string]*headImage{
				imageID:   {url: office, width: 1200, height: 800},
				avatarID:  {url: mediaURL(b, avatarID, "yamada.png", pngVariant(256, 256)), width: 256, height: 256},
				ogImageID: {url: mediaURL(b, ogImageID, "ogp.jpg", jpegVariant(1200, 630)), width: 1200, height: 630},
			}
			want := files[tt.want]
			if want != nil {
				want.alt = tt.alt
			}
			p := pageAt(b, tt.path)
			if p == nil {
				t.Fatalf("no page at %s", tt.path)
			}
			got := b.ogImage(p)
			if (got == nil) != (want == nil) || got != nil && *got != *want {
				t.Errorf("ogImage() = %+v, want %+v", got, want)
			}
		})
	}
}

// TestAlternates checks the alternates for hreflang in a site with three languages: x-default only when the default
// language has the counterpart, and none for a page whose counterparts are all missing.
func TestAlternates(t *testing.T) {
	src := testSource(t)
	site := testSite()
	site.Languages = append(site.Languages, "fr")
	site.Name["fr"] = "Entreprise"
	src.putSettings(t, site)
	edit(t, src, content.RefForm, formID, func(f *content.Form) {
		for i := range f.Fields {
			x := &f.Fields[i]
			x.Label["fr"] = x.Label["en"]
			for j := range x.Options {
				x.Options[j].Label["fr"] = x.Options[j].Label["en"]
			}
		}
		f.SubmitLabel["fr"], f.Message["fr"] = f.SubmitLabel["en"], f.Message["en"]
	})
	group := newID("tg", 1)
	src.putEntry(t, newEntry(newID("ent", 1), 101, content.TypePage, "en", group), content.Revision{
		Title: "Careers", Slug: "careers",
	})
	src.putEntry(t, newEntry(newID("ent", 2), 102, content.TypePage, "fr", group), content.Revision{
		Title: "Carrières", Slug: "carrieres",
	})
	src.putEntry(t, newEntry(newID("ent", 3), 103, content.TypePage, "fr", aboutGroup), content.Revision{
		Title: "À propos", Slug: "a-propos",
	})
	b := mustLoad(t, src)
	about := []string{
		"ja https://example.com/about/", "en https://example.com/en/about/", "fr https://example.com/fr/a-propos/",
		"x-default https://example.com/about/",
	}
	front := []string{
		"ja https://example.com/", "en https://example.com/en/", "fr https://example.com/fr/",
		"x-default https://example.com/",
	}
	tests := map[string][]string{
		"/about/":        about,
		"/fr/a-propos/":  about,
		"/en/careers/":   {"en https://example.com/en/careers/", "fr https://example.com/fr/carrieres/"},
		"/fr/carrieres/": {"en https://example.com/en/careers/", "fr https://example.com/fr/carrieres/"},
		"/":              front,
		"/fr/":           front,
		"/services/":     nil,
		"/404.html":      nil,
	}
	for _, path := range slices.Sorted(maps.Keys(tests)) {
		var got []string
		for _, a := range b.alternates(pageAt(b, path)) {
			got = append(got, a.hreflang+" "+a.url)
		}
		if !slices.Equal(got, tests[path]) {
			t.Errorf("alternates of %s = %q, want %q", path, got, tests[path])
		}
	}
}

// TestHeadEscaping checks the texts in the head: escaped in the attributes, and in the structured data, with <, > and
// & escaped as in JSON, so that the script ends only where it ends.
func TestHeadEscaping(t *testing.T) {
	src := testSource(t)
	settings := testSite()
	settings.Name["ja"] = `A&B "株式会社"`
	src.putSettings(t, settings)
	src.editRevision(t, postID, func(r *content.Revision) {
		r.Title = `Q&A: "<b>" & </script>`
		r.SEO.Description = `説明 <"'&>`
	})
	b := mustLoad(t, src)
	newTestResolver(t, b)
	lt, gt, amp := "\\"+"u003c", "\\"+"u003e", "\\"+"u0026"
	const (
		site        = "https://example.com"
		description = `説明 &lt;&#34;&#39;&amp;&gt;`
		name        = `A&amp;B &#34;株式会社&#34;`
	)
	author := `{"@type":"Person","name":"山田 太郎","url":"https://example.com/author/yamada/",` +
		`"sameAs":["https://x.com/example","https://example.com/"]}`
	want := slices.Concat(
		[]string{descriptionLine(description), canonicalLine(site + postPath)},
		alternateLines(site+postPath, site+"/en/2026/10/26/new-service/"),
		[]string{
			feedLine(name, "https://example.com/feed/"),
			ogLine("og:type", "article"),
			ogLine("og:title", `Q&amp;A: &#34;&lt;b&gt;&#34; &amp; &lt;/script&gt;`),
			ogLine("og:description", description),
			ogLine("og:url", site+postPath),
			ogLine("og:site_name", name),
			summaryLine,
			jsonLDLine(`{"@context":"https://schema.org","@type":"BlogPosting",` +
				`"headline":"Q` + amp + `A: \"` + lt + `b` + gt + `\" ` + amp + ` ` + lt + `/script` + gt + `",` +
				`"url":"` + site + postPath + `","datePublished":"2026-10-26T00:30:00+09:00","author":` + author + `,` +
				`"description":"説明 ` + lt + `\"'` + amp + gt + `","inLanguage":"ja",` +
				`"publisher":{"@type":"Organization","name":"A` + amp + `B \"株式会社\""}}`),
		},
	)
	checkHeads(t, b, map[string][]string{"/2026/10/26/新サービス/": want})
	head := string(b.head(pageAt(b, "/2026/10/26/新サービス/")))
	if n := strings.Count(head, "</script"); n != 1 {
		t.Errorf("</script appears %d times in\n%s", n, head)
	}
}

// TestJSONLD checks the script of structured data: on one line, with <, > and & escaped, and with invalid UTF-8
// replaced rather than an error.
func TestJSONLD(t *testing.T) {
	lt, gt, amp := "\\"+"u003c", "\\"+"u003e", "\\"+"u0026"
	got := jsonLD(&webPage{
		Context: schemaOrg, Type: "WebPage", Name: `</script><!-- "a" & 'b'`, URL: "https://example.com/?a=1&b=2",
		InLanguage: "ja",
	})
	want := `<script type="application/ld+json">{"@context":"https://schema.org","@type":"WebPage",` +
		`"name":"` + lt + `/script` + gt + lt + `!-- \"a\" ` + amp + ` 'b'","url":"https://example.com/?a=1` + amp +
		`b=2","inLanguage":"ja"}</script>`
	if got != want {
		t.Errorf("jsonLD() =\n%s\nwant\n%s", got, want)
	}
	replacement := string(rune(0xfffd))
	got = jsonLD(&organization{Type: "Organization", Name: "a\xffb"})
	want = `<script type="application/ld+json">{"@type":"Organization","name":"a` + replacement + `b"}</script>`
	if got != want {
		t.Errorf("jsonLD() of invalid UTF-8 =\n%s\nwant\n%s", got, want)
	}
}
