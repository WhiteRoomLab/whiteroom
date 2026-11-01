package build

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// rssFeed is an RSS feed as encoding/xml reads it, with the elements of the modules by the URLs of their namespaces.
type rssFeed struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	Channel struct {
		Title       string    `xml:"title"`
		Links       []rssLink `xml:"link"` // link and atom:link
		Description string    `xml:"description"`
		Language    string    `xml:"language"`
		Items       []rssItem `xml:"item"`
	} `xml:"channel"`
}

type rssLink struct {
	XMLName xml.Name
	Href    string `xml:"href,attr"`
	Rel     string `xml:"rel,attr"`
	Type    string `xml:"type,attr"`
	URL     string `xml:",chardata"`
}

type rssItem struct {
	Title string `xml:"title"`
	Link  string `xml:"link"`
	GUID  struct {
		IsPermaLink string `xml:"isPermaLink,attr"`
		ID          string `xml:",chardata"`
	} `xml:"guid"`
	PubDate     string   `xml:"pubDate"`
	Creator     string   `xml:"http://purl.org/dc/elements/1.1/ creator"`
	Categories  []string `xml:"category"`
	Description string   `xml:"description"`
	Content     string   `xml:"http://purl.org/rss/1.0/modules/content/ encoded"`
}

// readFeed returns the feed in the file name of dir, after checking that it is XML that encoding/xml reads.
func readFeed(t *testing.T, dir, name string) rssFeed {
	t.Helper()
	data := []byte(readFile(t, dir, name))
	checkXML(t, name, data)
	var f rssFeed
	if err := xml.Unmarshal(data, &f); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return f
}

// mustWriteFeeds writes the feeds of b into a new directory, which it returns, setting b.res to a new resolver if it
// has none.
func mustWriteFeeds(t *testing.T, b *builder) string {
	t.Helper()
	if b.res == nil {
		newTestResolver(t, b)
	}
	dir, pub := newPublic(t)
	if err := b.writeFeeds(t.Context(), pub); err != nil {
		t.Fatalf("writeFeeds() = %v", err)
	}
	return dir
}

// linked returns text with a link to the entry id, as rich text.
func linked(text, id string) content.Inline {
	return content.Inline{
		Type:  content.InlineText,
		Text:  text,
		Marks: []content.Mark{{Type: content.MarkLink, Link: &content.Link{Entry: id}}},
	}
}

// urlAttributes matches the URLs in the href, src and srcset attributes of HTML.
var urlAttributes = regexp.MustCompile(`(?:href|src|srcset)="([^"]*)"`)

// atomLink returns the atom:link of a channel as encoding/xml reads it, by the URL of the feed.
func atomLink(url string) rssLink {
	return rssLink{
		XMLName: xml.Name{Space: "http://www.w3.org/2005/Atom", Local: "link"},
		Href:    url,
		Rel:     "self",
		Type:    "application/rss+xml",
	}
}

// TestFeeds checks the feeds of the fixture with 12 posts in each language: the channels; the latest 10 posts, newest
// first, also when one of them is sticky; and their items: the guid, the date in the time zone of the site, the author,
// the terms by taxonomy and in order, the excerpt, and the body with absolute URLs, whose warnings are those of the
// page.
func TestFeeds(t *testing.T) {
	src := testSource(t)
	jaPosts := src.addPosts(t, "ja", 11)
	enPosts := src.addPosts(t, "en", 11)
	src.putOriginal(t, imageID, "会社外観.jpg", testJPEG(t, 1200, 800))
	src.editRevision(t, jaPosts[4], func(r *content.Revision) { r.Sticky = true })
	src.editRevision(t, enPostID, func(r *content.Revision) { r.Date = "2026-10-25T15:30:00Z" })
	src.editRevision(t, postID, func(r *content.Revision) {
		r.Excerpt = "新しいサービスの\nお知らせです。"
		r.Terms[content.TaxonomyCategory] = []string{pressReleaseID, newsCategoryID}
		r.Body = append(r.Body,
			content.Block{Type: content.BlockParagraph, ID: "b_feed0001", Text: content.RichText{
				linked("会社概要", aboutID), {Type: content.InlineText, Text: "と"}, linked("準備中のページ", draftID),
			}},
			content.Block{
				Type: content.BlockImage, ID: "b_feed0002", Attrs: content.Attrs{Media: imageID, Alt: "会社外観"},
			},
		)
	})
	b := mustLoad(t, src)
	dir := mustWriteFeeds(t, b)
	if got, want := listFiles(t, dir), []string{"en/feed/index.xml", "feed/index.xml"}; !slices.Equal(got, want) {
		t.Errorf("files %q, want %q", got, want)
	}

	links := map[string]string{
		postID:   "https://example.com" + postPath,
		enPostID: "https://example.com/en/2026/10/25/new-service/",
	}
	for i := range 11 {
		links[jaPosts[i]] = fmt.Sprintf("https://example.com/2026/09/%02d/post-%d/", i+1, 1001+i)
		links[enPosts[i]] = fmt.Sprintf("https://example.com/en/2026/09/%02d/post-%d/", i+1, 1012+i)
	}
	ja, en := readFeed(t, dir, "feed/index.xml"), readFeed(t, dir, "en/feed/index.xml")
	for _, tt := range []struct {
		feed                                 rssFeed
		title, link, description, lang, self string
		items                                []string
	}{
		{
			feed:        ja,
			title:       "株式会社〇〇",
			link:        "https://example.com/",
			description: "Webサイトの企画と制作",
			lang:        "ja",
			self:        "https://example.com/feed/",
			items:       newestFirst(postID, jaPosts)[:10], // the sticky post at its date
		},
		{
			feed:        en,
			title:       "Example Inc.",
			link:        "https://example.com/en/",
			description: "Example Inc.", // without a tagline
			lang:        "en",
			self:        "https://example.com/en/feed/",
			items:       newestFirst(enPostID, enPosts)[:10],
		},
	} {
		c := tt.feed.Channel
		wantLinks := []rssLink{{XMLName: xml.Name{Local: "link"}, URL: tt.link}, atomLink(tt.self)}
		if tt.feed.Version != "2.0" || c.Title != tt.title || !slices.Equal(c.Links, wantLinks) ||
			c.Description != tt.description || c.Language != tt.lang {
			t.Errorf("feed in %s: version %q, channel %q, %+v, %q, %q", tt.lang, tt.feed.Version, c.Title, c.Links,
				c.Description, c.Language)
		}
		var ids []string
		for _, item := range c.Items {
			ids = append(ids, item.GUID.ID)
			if item.GUID.IsPermaLink != "false" || item.Link != links[item.GUID.ID] || item.Creator != "山田 太郎" {
				t.Errorf("item %s: isPermaLink %q, link %q, creator %q", item.GUID.ID, item.GUID.IsPermaLink, item.Link,
					item.Creator)
			}
			for _, m := range urlAttributes.FindAllStringSubmatch(item.Content, -1) {
				for u := range strings.SplitSeq(m[1], ", ") {
					if !strings.HasPrefix(u, "https://example.com/") {
						t.Errorf("item %s: URL %q is not absolute in\n%s", item.GUID.ID, u, item.Content)
					}
				}
			}
		}
		if !slices.Equal(ids, tt.items) {
			t.Errorf("items in %s:\n%q\nwant:\n%q", tt.lang, ids, tt.items)
		}
	}

	// The first item of each, then the second in ja: the date of a post of addPosts.
	office := mediaURL(b, imageID, officeName+".jpg", jpegVariant(1200, 800))
	for _, tt := range []struct {
		item                     rssItem
		title, date, description string
		categories               []string
		content                  []string // in the body
	}{
		{
			item:        ja.Channel.Items[0],
			title:       "新サービスを始めました",
			date:        "Mon, 26 Oct 2026 00:30:00 +0900",
			description: "新しいサービスの お知らせです。",
			categories:  []string{"プレスリリース", "お知らせ", "イベント"},
			content: []string{
				"<p>Webサイトの更新と運用をお手伝いするサービスです。</p>\n",
				`<p><a href="https://example.com/about/">会社概要</a>と準備中のページ</p>` + "\n",
				`<img src="` + office + `"`,
			},
		},
		{
			// The date in UTC, in the time zone of the site.
			item:        en.Channel.Items[0],
			title:       "We started a new service",
			date:        "Mon, 26 Oct 2026 00:30:00 +0900",
			description: "A service to update and run your website.",
			categories:  []string{"Events"},
			content:     []string{"<p>A service to update and run your website.</p>"},
		},
		{
			item:        ja.Channel.Items[1],
			title:       "Post 1011",
			date:        "Fri, 11 Sep 2026 09:00:00 +0900",
			description: "Post 1011.",
			categories:  []string{"お知らせ"},
			content:     []string{"<p>Post 1011.</p>"},
		},
	} {
		x := tt.item
		if x.Title != tt.title || x.PubDate != tt.date || x.Description != tt.description ||
			!slices.Equal(x.Categories, tt.categories) {
			t.Errorf("item %s: title %q, pubDate %q, description %q, categories %q", x.GUID.ID, x.Title, x.PubDate,
				x.Description, x.Categories)
		}
		for _, s := range tt.content {
			if !strings.Contains(x.Content, s) {
				t.Errorf("item %s: no %q in its body:\n%s", x.GUID.ID, s, x.Content)
			}
		}
	}

	// The link to the draft warns, and only once after the pages, whose bodies have the same warnings.
	warning := fmt.Sprintf("%s:52:13: /body/1/content/2/marks/0: entry %q is not published; "+
		"rendered without the link", revisionFile(postID), draftID)
	if got := warningTexts(b); !slices.Equal(got, []string{warning}) {
		t.Errorf("warnings:\n%s\nwant:\n%s", strings.Join(got, "\n"), warning)
	}
	mustRender(t, b, 2)
	want := []string{
		revisionFile(aboutID) + `:5:3: /template: the theme has no page template "standard"; rendered with entry.html`,
		warning,
	}
	if got := warningTexts(b); !slices.Equal(got, want) {
		t.Errorf("warnings after the pages:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// As many items as a page of a list has.
	settings := testSite()
	settings.PostsPerPage = 3
	src.putSettings(t, settings)
	b = mustLoad(t, src)
	dir = mustWriteFeeds(t, b)
	for name, want := range map[string][]string{
		"feed/index.xml":    newestFirst(postID, jaPosts)[:3],
		"en/feed/index.xml": newestFirst(enPostID, enPosts)[:3],
	} {
		var ids []string
		for _, item := range readFeed(t, dir, name).Channel.Items {
			ids = append(ids, item.GUID.ID)
		}
		if !slices.Equal(ids, want) {
			t.Errorf("items of %s with postsPerPage 3: %q, want %q", name, ids, want)
		}
	}
}

// TestFeedText checks the text of the feeds: one element on each line, the body on as many lines as its HTML has; the
// title and the excerpt of a post, not those of its SEO; an item without an author, terms, an excerpt and a body; and a
// channel without items in a language without posts.
func TestFeedText(t *testing.T) {
	src := testSource(t)
	edit(t, src, content.RefEntry, enPostID, func(e *content.Entry) {
		e.Status, e.PublishedRevision = content.StatusDraft, ""
	})
	src.editRevision(t, postID, func(r *content.Revision) {
		r.SEO = content.SEO{Title: "新サービスのお知らせ", Description: "検索結果に表示する説明です。"}
		r.Body = append(r.Body, paragraph("b_feed0001", "詳しくはお問い合わせください。"))
	})
	empty := newID("ent", 1)
	src.putEntry(t, newEntry(empty, 200, content.TypePost, "ja", newID("tg", 1)), content.Revision{
		Title: "空の投稿",
		Slug:  "empty",
		Date:  "2026-10-27T09:00:00+09:00",
	})
	b := mustLoad(t, src)
	dir := mustWriteFeeds(t, b)

	const header = `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<rss version="2.0" xmlns:content="http://purl.org/rss/1.0/modules/content/"` +
		` xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:atom="http://www.w3.org/2005/Atom">` + "\n" +
		"<channel>\n"
	for name, want := range map[string]string{
		"feed/index.xml": header + `<title>株式会社〇〇</title>
<link>https://example.com/</link>
<description>Webサイトの企画と制作</description>
<language>ja</language>
<atom:link href="https://example.com/feed/" rel="self" type="application/rss+xml"/>
<item>
<title>空の投稿</title>
<link>https://example.com/2026/10/27/empty/</link>
<guid isPermaLink="false">` + empty + `</guid>
<pubDate>Tue, 27 Oct 2026 09:00:00 +0900</pubDate>
</item>
<item>
<title>新サービスを始めました</title>
<link>https://example.com/2026/10/26/%E6%96%B0%E3%82%B5%E3%83%BC%E3%83%93%E3%82%B9/</link>
<guid isPermaLink="false">` + postID + `</guid>
<pubDate>Mon, 26 Oct 2026 00:30:00 +0900</pubDate>
<dc:creator>山田 太郎</dc:creator>
<category>プレスリリース</category>
<category>イベント</category>
<description>Webサイトの更新と運用をお手伝いするサービスです。 詳しくはお問い合わせください。</description>
<content:encoded>&lt;p&gt;Webサイトの更新と運用をお手伝いするサービスです。&lt;/p&gt;
&lt;p&gt;詳しくはお問い合わせください。&lt;/p&gt;</content:encoded>
</item>
</channel>
</rss>
`,
		"en/feed/index.xml": header + `<title>Example Inc.</title>
<link>https://example.com/en/</link>
<description>Example Inc.</description>
<language>en</language>
<atom:link href="https://example.com/en/feed/" rel="self" type="application/rss+xml"/>
</channel>
</rss>
`,
	} {
		if got := readFile(t, dir, name); got != want {
			t.Errorf("%s:\n%s\nwant:\n%s", name, got, want)
		}
		checkXML(t, name, []byte(want))
	}
}

// TestFeedEscaping checks that the plain texts of a feed are escaped with character references, as the RSS Best
// Practices Profile advises, and its HTML with entities, the excerpt as HTML, and that encoding/xml reads them back.
func TestFeedEscaping(t *testing.T) {
	src := testSource(t)
	settings := testSite()
	settings.Name["ja"] = `A&B "<社>"`
	settings.Tagline["ja"] = "企画 & 制作"
	src.putSettings(t, settings)
	edit(t, src, content.RefAuthor, authorID, func(a *content.AuthorProfile) { a.Name = "山田 & 佐藤" })
	edit(t, src, content.RefTerm, pressReleaseID, func(x *content.Term) { x.Name = "<PR>" })
	src.editRevision(t, postID, func(r *content.Revision) {
		r.Title = "Q&A: <b>'x'</b>"
		r.Excerpt = "1 < 2 & 3 > 0"
		r.Body = content.Blocks{paragraph("b_feed0001", `a < b & "c"`)}
	})
	b := mustLoad(t, src)
	dir := mustWriteFeeds(t, b)
	data := readFile(t, dir, "feed/index.xml")
	for _, line := range []string{
		`<title>A&#x26;B "&#x3C;社&#x3E;"</title>`,
		`<description>企画 &#x26; 制作</description>`,
		`<title>Q&#x26;A: &#x3C;b&#x3E;'x'&#x3C;/b&#x3E;</title>`,
		`<dc:creator>山田 &#x26; 佐藤</dc:creator>`,
		`<category>&#x3C;PR&#x3E;</category>`,
		`<description>1 &amp;lt; 2 &amp;amp; 3 &amp;gt; 0</description>`,
		`<content:encoded>&lt;p&gt;a &amp;lt; b &amp;amp; "c"&lt;/p&gt;</content:encoded>`,
	} {
		if !slices.Contains(strings.Split(data, "\n"), line) {
			t.Errorf("no line %s in\n%s", line, data)
		}
	}
	f := readFeed(t, dir, "feed/index.xml")
	item := f.Channel.Items[0]
	if f.Channel.Title != settings.Name["ja"] || f.Channel.Description != settings.Tagline["ja"] ||
		item.Title != "Q&A: <b>'x'</b>" || item.Creator != "山田 & 佐藤" ||
		!slices.Equal(item.Categories, []string{"<PR>", "イベント"}) ||
		item.Description != "1 &lt; 2 &amp; 3 &gt; 0" || item.Content != `<p>a &lt; b &amp; "c"</p>` {
		t.Errorf("read channel %q, %q; item %q, %q, %q, %q, %q", f.Channel.Title, f.Channel.Description, item.Title,
			item.Creator, item.Categories, item.Description, item.Content)
	}
}

// TestFeedsUnderPath checks the URLs of the feeds of a site under a path with the default language in its paths: of the
// channels, the items and in the bodies, also those of the assets of the theme.
func TestFeedsUnderPath(t *testing.T) {
	src := testSource(t)
	settings := testSite()
	settings.URL += "/blog"
	settings.PrefixDefaultLanguage = true
	src.putSettings(t, settings)
	th := testTheme()
	th["blocks/separator.html"] = &fstest.MapFile{Data: []byte(`<img src="{{asset "images/line.png"}}" alt="">`)}
	th["assets/images/line.png"] = &fstest.MapFile{Data: []byte("line")}
	src.theme = th
	src.editRevision(t, postID, func(r *content.Revision) {
		r.Body = content.Blocks{
			{Type: content.BlockParagraph, ID: "b_feed0001", Text: content.RichText{linked("会社概要", aboutID)}},
			{Type: content.BlockSeparator, ID: "b_feed0002"},
		}
	})
	b := mustLoad(t, src)
	dir := mustWriteFeeds(t, b)
	if got, want := listFiles(t, dir), []string{"en/feed/index.xml", "ja/feed/index.xml"}; !slices.Equal(got, want) {
		t.Errorf("files %q, want %q", got, want)
	}
	const site = "https://example.com/blog"
	f := readFeed(t, dir, "ja/feed/index.xml")
	wantLinks := []rssLink{{XMLName: xml.Name{Local: "link"}, URL: site + "/ja/"}, atomLink(site + "/ja/feed/")}
	if !slices.Equal(f.Channel.Links, wantLinks) {
		t.Errorf("links of the channel %+v, want %+v", f.Channel.Links, wantLinks)
	}
	item := f.Channel.Items[0]
	asset := regexp.MustCompile(`^<img src="https://example\.com/blog/assets/images/line\.png\?v=[0-9a-f]{8}" alt="">$`)
	lines := strings.Split(item.Content, "\n")
	if item.Link != site+"/ja"+postPath || len(lines) != 2 ||
		lines[0] != `<p><a href="`+site+`/ja/about/">会社概要</a></p>` || !asset.MatchString(lines[1]) {
		t.Errorf("item: link %q, body:\n%s", item.Link, item.Content)
	}
	if got := readFeed(t, dir, "en/feed/index.xml").Channel.Items[0].Link; got != site+"/en/2026/10/26/new-service/" {
		t.Errorf("link of the item in en: %q", got)
	}
}

// TestFeedDates checks the dates of the items in other time zones of the site.
func TestFeedDates(t *testing.T) {
	for zone, want := range map[string]string{
		"UTC":              "Sun, 25 Oct 2026 15:30:00 +0000",
		"America/New_York": "Sun, 25 Oct 2026 11:30:00 -0400",
		"Asia/Kolkata":     "Sun, 25 Oct 2026 21:00:00 +0530",
	} {
		src := testSource(t)
		settings := testSite()
		settings.TimeZone = zone
		src.putSettings(t, settings)
		dir := mustWriteFeeds(t, mustLoad(t, src))
		if got := readFeed(t, dir, "feed/index.xml").Channel.Items[0].PubDate; got != want {
			t.Errorf("pubDate in %s: %q, want %q", zone, got, want)
		}
	}
}

// TestFeedsDeterministic checks that the feeds are the same when they are written again and from another load.
func TestFeedsDeterministic(t *testing.T) {
	src := testSource(t)
	src.addPosts(t, "ja", 11)
	src.addPosts(t, "en", 11)
	b := mustLoad(t, src)
	dirs := []string{mustWriteFeeds(t, b), mustWriteFeeds(t, b), mustWriteFeeds(t, mustLoad(t, src))}
	files := make(map[string]string)
	for _, name := range listFiles(t, dirs[0]) {
		files[name] = readFile(t, dirs[0], name)
	}
	for _, dir := range dirs[1:] {
		got := make(map[string]string)
		for _, name := range listFiles(t, dir) {
			got[name] = readFile(t, dir, name)
		}
		if !maps.Equal(got, files) {
			t.Errorf("the feeds differ")
		}
	}
}

// TestFeedsCanceled checks that writing the feeds stops when the context is done: before a feed without items, and
// before an item, here after the original of an image in the item before it was opened.
func TestFeedsCanceled(t *testing.T) {
	src := testSource(t)
	edit(t, src, content.RefEntry, postID, func(e *content.Entry) {
		e.Status, e.PublishedRevision = content.StatusDraft, ""
	})
	b := mustLoad(t, src)
	newTestResolver(t, b)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	dir, pub := newPublic(t)
	if err := b.writeFeeds(ctx, pub); !errors.Is(err, context.Canceled) {
		t.Errorf("writeFeeds() with a canceled context = %v", err)
	}
	if files := listFiles(t, dir); len(files) > 0 {
		t.Errorf("files after canceling: %q", files)
	}

	src = testSource(t)
	src.putOriginal(t, imageID, "会社外観.jpg", testJPEG(t, 120, 80))
	for _, id := range src.addPosts(t, "ja", 2) {
		src.editRevision(t, id, func(r *content.Revision) {
			r.Body = append(r.Body, content.Block{Type: content.BlockImage, ID: "b_feed0001", Attrs: content.Attrs{
				Media: imageID, Alt: "会社外観",
			}})
		})
	}
	b = mustLoad(t, src)
	newTestResolver(t, b)
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	b.src = hookedSource{Source: b.src, hook: func(string) { cancel() }}
	dir, pub = newPublic(t)
	if err := b.writeFeeds(ctx, pub); !errors.Is(err, context.Canceled) {
		t.Errorf("writeFeeds() canceled while rendering = %v", err)
	}
	if files := listFiles(t, dir); len(files) > 0 {
		t.Errorf("files after canceling: %q", files)
	}
}

// TestFeedsErrors checks the errors of writing a file and of rendering a body.
func TestFeedsErrors(t *testing.T) {
	src := testSource(t)
	b := mustLoad(t, src)
	newTestResolver(t, b)
	_, pub := newPublic(t)
	if err := writeFile(pub, "en/feed/index.xml", nil); err != nil {
		t.Fatal(err)
	}
	if err := b.writeFeeds(t.Context(), pub); !errors.Is(err, fs.ErrExist) {
		t.Errorf("writeFeeds() over a file = %v", err)
	}

	th := testTheme()
	th["blocks/paragraph.html"] = &fstest.MapFile{Data: []byte("{{.Nothing}}")}
	src.theme = th
	b = mustLoad(t, src)
	newTestResolver(t, b)
	_, pub = newPublic(t)
	err := b.writeFeeds(t.Context(), pub)
	if err == nil || !strings.HasPrefix(err.Error(), revisionFile(postID)+": template: ") {
		t.Errorf("writeFeeds() with a failing block template = %v", err)
	}
}
