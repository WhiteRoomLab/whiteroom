package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"errors"
	"html/template"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/render"
)

// sidebarID is a menu in ja at a location that the example theme does not have.
var sidebarID = newID("mnu", 1)

// renderSource returns the fixture of productSource with the example theme (internal/theme/testdata/example), 11 more
// posts in each language, the original of the logo of the site, and:
//   - the template landing of the theme for the page services;
//   - a paragraph with a style in the body of the posts page, whose CSS every page of its list needs;
//   - an item of the header menu to the draft, with a child, and a menu in ja at a location that the theme lacks;
//   - values of the fields of productID that need CSS or warn: a highlight with a color in intro, a paragraph with a
//     variant that the theme does not have in details, and a highlight with such a variant in the description of
//     variants/1.
func renderSource(t *testing.T) *memSource {
	t.Helper()
	src := productSource(t)
	src.theme = os.DirFS("../theme/testdata/example")
	src.addPosts(t, "ja", 11)
	src.addPosts(t, "en", 11)
	src.putOriginal(t, logoID, "logo.png", testPNG(t, 400, 100))
	src.editRevision(t, servicesID, func(r *content.Revision) { r.Template = "landing" })
	src.editRevision(t, newsID, func(r *content.Revision) {
		r.Body = append(r.Body, content.Block{
			Type:  content.BlockParagraph,
			ID:    "b_news0002",
			Style: content.Style{TextColor: content.Color{Preset: "primary"}},
			Text:  richText("重要なお知らせ"),
		})
	})
	edit(t, src, content.RefMenu, headerID, func(m *content.Menu) {
		m.Items = append(m.Items, content.MenuItem{
			Label: "準備中", Entry: draftID, Children: []content.MenuItem{{Label: "子", Href: "/child/"}},
		})
	})
	src.put(t, content.RefMenu, sidebarID, content.Menu{
		ID: sidebarID, Title: "サイドバー", Location: "sidebar", Language: "ja", Items: []content.MenuItem{{Entry: aboutID}},
	})
	text := func(s, marks string) string {
		return `[{"type":"text","text":"` + s + `"` + marks + `}]`
	}
	src.editRevision(t, productID, func(r *content.Revision) {
		r.Fields["intro"] = jsontext.Value(text("はじめに", `,"marks":[{"type":"highlight","attrs":{"color":{"preset":"accent"}}}]`))
		r.Fields["details"] = jsontext.Value(`[{"type":"paragraph","id":"b_prod0002","content":` + text("詳細", "") + `},` +
			`{"type":"paragraph","id":"b_prod0003","variant":"wide","content":` + text("補足", "") + `}]`)
		r.Fields["variants"] = jsontext.Value(`[{"name":"小","link":{"entry":"` + draftID + `"}},` +
			`{"name":"大","description":` + text("大きいサイズ", `,"marks":[{"type":"highlight","attrs":{"variant":"neon"}}]`) +
			`}]`)
	})
	return src
}

// mustRender sets b.res to a new resolver and renders the pages of b into a new directory with workers, which it
// returns.
func mustRender(t *testing.T, b *builder, workers int) string {
	t.Helper()
	newTestResolver(t, b)
	dir, pub := newPublic(t)
	if err := b.renderPages(t.Context(), pub, workers); err != nil {
		t.Fatalf("renderPages() = %v", err)
	}
	return dir
}

// readFile returns the content of the file name, a path with "/", in dir.
func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// pageCSSOutputs returns the outputs of b that are page CSS files.
func pageCSSOutputs(b *builder) []output {
	var outs []output
	for _, o := range b.outputs {
		if strings.HasPrefix(o.path, "/css/") && o.path != "/css/site.css" {
			outs = append(outs, o)
		}
	}
	return outs
}

// TestRenderPages checks the pages of the fixture rendered with the example theme: the files, parts of the pages, the
// page CSS, and the warnings.
func TestRenderPages(t *testing.T) {
	b := mustLoad(t, renderSource(t))
	dir := mustRender(t, b, 4)

	sheets := pageCSSOutputs(b)
	want := []string{"css/site.css"}
	for _, p := range b.pages {
		want = append(want, p.file)
	}
	for _, a := range b.theme.Assets() {
		want = append(want, "assets/"+a)
	}
	for _, o := range sheets {
		want = append(want, o.file)
	}
	slices.Sort(want)
	if got := listFiles(t, dir); !slices.Equal(got, want) {
		t.Errorf("files:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	siteSum := sha256.Sum256([]byte(readFile(t, dir, "css/site.css")))
	links := `<link rel="stylesheet" href="/css/site.css?v=` + hex.EncodeToString(siteSum[:4]) + `">` + "\n" +
		`<link rel="stylesheet" href="/assets/css/style.css?v=b791d850">` + "\n"
	// headOf returns the head of the page at path, which the layout writes after the links to the CSS.
	headOf := func(path string) string { return string(b.head(pageAt(b, path))) }

	// The page CSS of the posts page, which the pages of its list share, and that of productID.
	if len(sheets) != 2 {
		t.Fatalf("page CSS outputs: %+v, want 2", sheets)
	}
	news, product := sheets[0], sheets[1]
	newsCSS := ".b-news0002 {\n  color: var(--color-primary) !important;\n}\n"
	if got := readFile(t, dir, news.file); got != newsCSS {
		t.Errorf("%s:\n%s\nwant:\n%s", news.file, got, newsCSS)
	}
	sum := sha256.Sum256([]byte(newsCSS))
	if name := "css/" + hex.EncodeToString(sum[:8]) + ".css"; news.file != name || news.path != "/"+name ||
		news.rank != rankCore || !reflect.DeepEqual(news.src, source{what: "the page CSS of /news/"}) {
		t.Errorf("output of the page CSS of /news/: %+v, want the file %s", news, name)
	}
	productCSS := readFile(t, dir, product.file)
	if !regexp.MustCompile(`^\.hl-[0-9a-f]{8} \{\n  color: var\(--color-accent\) !important;\n\}\n$`).MatchString(productCSS) ||
		product.src.what != "the page CSS of /products/product-a/" {
		t.Errorf("page CSS of /products/product-a/: %+v:\n%s", product, productCSS)
	}

	ja := testRenderer(t, b, "ja")
	logo := ja.PageImage(logoID)
	office := ja.PageImage(imageID)
	yamada := ja.PageImage(avatarID)
	// jaHeader returns the header of the pages in ja, without the item to the draft, in which the item n is the current
	// page: 会社概要, サービス一覧, 制作実績 and お知らせ from 1, or none for 0.
	jaHeader := func(n int) string {
		current := func(i int) string {
			if i == n {
				return ` aria-current="page"`
			}
			return ""
		}
		return `<header class="site-header">
<a href="/"><img src="` + string(logo.Src) + `" width="400" height="100" alt=""> 株式会社〇〇</a>
<nav><ul>
<li><a href="/about/"` + current(1) + `>会社概要</a></li>
<li><span>サービス</span><ul>
<li><a href="/services/"` + current(2) + `>サービス一覧</a></li>
<li><a href="/works/"` + current(3) + `>制作実績</a></li>
</ul>
</li>
<li><a href="/category/news/"` + current(4) + `>お知らせ</a><ul>
<li><a href="/category/news/press-release/">プレスリリース</a></li>
</ul>
</li>
<li><a href="https://recruit.example.com/" target="_blank" rel="noopener">採用情報</a></li>
</ul>
</nav>
</header>`
	}
	footer := `<footer class="site-footer">
<nav><ul>
<li><a href="/contact/">お問い合わせ</a></li>
<li><a href="/privacy/">プライバシーポリシー</a></li>
<li><a href="/en/about/">English</a></li>
</ul>
</nav>
<nav><ul class="languages">
`
	// languages returns the language switcher with the URLs in ja and en; the current one is that of language, and a
	// URL that starts with "!" is missing.
	languages := func(language, jaURL, enURL string) string {
		var s strings.Builder
		s.WriteString("<nav><ul class=\"languages\">\n")
		for _, l := range []struct{ code, url string }{{"ja", jaURL}, {"en", enURL}} {
			url, missing := strings.CutPrefix(l.url, "!")
			s.WriteString(`<li><a href="` + url + `" hreflang="` + l.code + `" lang="` + l.code + `"`)
			if l.code == language {
				s.WriteString(` aria-current="page"`)
			}
			if missing {
				s.WriteString(` class="missing"`)
			}
			s.WriteString(">" + l.code + "</a></li>\n")
		}
		s.WriteString("</ul>\n</nav>\n")
		return s.String()
	}
	const post = "/2026/10/26/%E6%96%B0%E3%82%B5%E3%83%BC%E3%83%93%E3%82%B9/"
	pages := []struct {
		path      string
		fragments []string
	}{
		{"/", []string{
			"<!DOCTYPE html>\n<html lang=\"ja\">\n",
			"<title>株式会社〇〇</title>\n" + links + headOf("/") + "\n</head>\n",
			jaHeader(0),
			footer,
			languages("ja", "/", "/en/"),
			"<h1>ホーム</h1>\n<p>Webサイトの企画と制作の会社です。</p>\n</article>",
		}},
		{"/about/", []string{
			"<title>会社概要｜株式会社〇〇</title>\n" + links + headOf("/about/") + "\n</head>\n",
			jaHeader(1),
			languages("ja", "/about/", "/en/about/"),
			`<img src="` + string(office.Src) + `" srcset="` + string(office.Srcset) + `" sizes="` + office.Sizes +
				`" width="1200" height="800" alt="会社外観">`,
		}},
		{"/services/", []string{
			jaHeader(2),
			"<main>\n<article class=\"landing\">\n<h1>サービス</h1>\n<p>Webサイトの企画、制作と運用をお手伝いします。</p>\n</article>\n</main>",
		}},
		{"/works/", []string{
			jaHeader(3),
			"<section class=\"list archive-works\">\n<h1>制作実績</h1>\n<article class=\"card\">",
			languages("ja", "/works/", "/en/works/"),
		}},
		{"/category/news/", []string{jaHeader(4)}},
		// Not current on page 2.
		{"/category/news/page/2/", []string{
			"<title>お知らせ｜株式会社〇〇</title>",
			jaHeader(0),
			"<section class=\"list term-category\">\n<h1>お知らせ</h1>\n" +
				"<p>会社からのお知らせです。\n新しいサービスやイベントの情報を掲載します。</p>\n<article class=\"card\">",
			"<nav class=\"pagination\">\n<a href=\"/category/news/\" rel=\"prev\">前へ</a>\n<span>2 / 2 ページ</span>\n</nav>",
			languages("ja", "/category/news/page/2/", "!/en/"),
		}},
		{"/2026/10/26/新サービス/", []string{
			"<title>新サービスを始めました｜株式会社〇〇</title>\n" + links + headOf("/2026/10/26/新サービス/") + "\n</head>\n",
			`<p><time datetime="2026-10-26T00:30:00&#43;09:00">2026-10-26</time> <a href="/author/yamada/">山田 太郎 が投稿</a></p>`,
			"<li><a href=\"/category/news/press-release/\">プレスリリース</a></li>\n" +
				"<li><a href=\"/tag/%E3%82%A4%E3%83%99%E3%83%B3%E3%83%88/\">#イベント</a></li>",
			languages("ja", post, "/en/2026/10/26/new-service/"),
		}},
		{"/works/example-corporate-site/", []string{
			"<h1>〇〇株式会社のコーポレートサイト</h1>\n<p>〇〇株式会社 (2024)</p>\n" +
				`<img src="` + string(office.Src) + `" srcset="` + string(office.Srcset) + `" sizes="` + office.Sizes +
				`" width="1200" height="800" alt="会社外観">` + "\n" +
				`<img src="` + string(yamada.Src) + `" width="256" height="256">` + "\n" +
				"<p>コーポレートサイトを全面的に作り直しました。</p>",
			languages("ja", "/works/example-corporate-site/", "!/en/"),
		}},
		// The page CSS of the posts page on both pages of its list.
		{"/news/", []string{
			"<title>お知らせ｜株式会社〇〇</title>\n" + links + `<link rel="stylesheet" href="` + news.path + "\">\n" +
				headOf("/news/") + "\n</head>\n",
			"<h1>お知らせ</h1>\n<p>会社からのお知らせです。</p>\n<p class=\"b-news0002\">重要なお知らせ</p>\n<article class=\"card\">",
			languages("ja", "/news/", "!/en/"),
		}},
		{"/news/page/2/", []string{
			"<title>お知らせ｜株式会社〇〇</title>\n" + links + `<link rel="stylesheet" href="` + news.path + "\">\n" +
				headOf("/news/page/2/") + "\n</head>\n",
			"<h1>お知らせ</h1>\n<p>会社からのお知らせです。</p>\n<p class=\"b-news0002\">重要なお知らせ</p>\n<article class=\"card\">",
			languages("ja", "/news/page/2/", "!/en/"),
		}},
		{"/products/product-a/", []string{
			links + `<link rel="stylesheet" href="` + product.path + "\">\n" + headOf("/products/product-a/") +
				"\n</head>\n",
		}},
		{"/en/", []string{
			"<!DOCTYPE html>\n<html lang=\"en\">\n",
			"<title>Example Inc.</title>\n" + links + headOf("/en/") + "\n</head>\n",
			// No menus in en.
			"<header class=\"site-header\">\n<a href=\"/en/\"><img src=\"" + string(logo.Src) +
				"\" width=\"400\" height=\"100\" alt=\"\"> Example Inc.</a>\n</header>",
			"<section class=\"list posts\">\n<article class=\"card\">\n" +
				"<h2><a href=\"/en/2026/10/26/new-service/\">We started a new service</a></h2>\n" +
				"<p><time datetime=\"2026-10-26T00:30:00&#43;09:00\">2026-10-26</time> 山田 太郎 が投稿</p>\n" +
				"<p>A service to update and run your website.</p>\n" +
				"<a href=\"/en/2026/10/26/new-service/\">Read more</a>\n</article>",
			"<nav class=\"pagination\">\n<span>Page 1 of 2</span>\n<a href=\"/en/page/2/\" rel=\"next\">Next</a>\n</nav>",
			"<footer class=\"site-footer\">\n" + languages("en", "/", "/en/"),
		}},
		{"/en/page/2/", []string{languages("en", "/", "/en/page/2/")}},
		{"/en/tag/events/page/2/", []string{
			"<title>Events | Example Inc.</title>",
			"<section class=\"list term\">\n<h1>Events</h1>\n<article class=\"card\">",
			languages("en", "/tag/%E3%82%A4%E3%83%99%E3%83%B3%E3%83%88/", "/en/tag/events/page/2/"),
		}},
		{"/author/yamada/page/2/", []string{
			"<title>山田 太郎｜株式会社〇〇</title>",
			"<section class=\"list author\">\n<img src=\"" + string(yamada.Src) + "\" width=\"256\" height=\"256\">\n" +
				"<h1>山田 太郎</h1>",
			languages("ja", "/author/yamada/page/2/", "/en/author/yamada/"),
		}},
		// The icon of the site has no original: no links to its icons.
		{"/404.html", []string{
			"<title>ページが見つかりません | 株式会社〇〇</title>\n" + links + robotsLine + "\n" + jaFeedLine + "\n</head>\n",
			"<h1>ページが見つかりません</h1>\n<p><a href=\"/\">株式会社〇〇</a></p>",
			languages("ja", "/", "/en/"),
		}},
		{"/en/404.html", []string{
			"<!DOCTYPE html>\n<html lang=\"en\">\n",
			"<title>Page not found | Example Inc.</title>",
			"<h1>Page not found</h1>\n<p><a href=\"/en/\">Example Inc.</a></p>",
			languages("en", "/", "/en/"),
		}},
	}
	for _, tt := range pages {
		p := pageAt(b, tt.path)
		if p == nil {
			t.Errorf("no page at %s", tt.path)
			continue
		}
		html := readFile(t, dir, p.file)
		for _, f := range tt.fragments {
			if !strings.Contains(html, f) {
				t.Errorf("%s has no\n%s\nin\n%s", tt.path, f, html)
			}
		}
	}

	rev := revisionFile(productID)
	wantWarnings := []string{
		fileOf(content.RefMenu, headerID) + ":38:5: /items/4: entry \"" + draftID +
			"\" is not published; the item and its children are left out",
		fileOf(content.RefMenu, sidebarID) + ":5:3: /location: the theme has no location \"sidebar\"; the menu is left out",
		revisionFile(aboutID) + ":5:3: /template: the theme has no page template \"standard\"; rendered with entry.html",
		rev + ":28:9: /fields/details/1/variant: the theme has no paragraph variant \"wide\"; rendered without its class",
		rev + ":53:5: /fields/link: entry \"" + draftID + "\" is not published; rendered without the link",
		rev + ":92:9: /fields/variants/0/link: entry \"" + draftID + "\" is not published; rendered without the link",
		rev + ":103:15: /fields/variants/1/description/0/marks/0: the theme has no highlight variant \"neon\"; " +
			"rendered without its class",
		"theme: no translation in en for the key \"postedBy\"; pages in en use its text in ja, the default language of " +
			"the theme",
	}
	if got := warningTexts(b); !slices.Equal(got, wantWarnings) {
		t.Errorf("warnings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantWarnings, "\n"))
	}
}

// TestRenderPagesWorkers checks that renderPages writes the same files and outputs, with the same warnings, with one
// worker and with several.
func TestRenderPagesWorkers(t *testing.T) {
	type result struct {
		files    map[string]string
		outputs  []output
		warnings []string
	}
	var results []result
	for _, workers := range []int{1, 8} {
		b := mustLoad(t, renderSource(t))
		dir := mustRender(t, b, workers)
		r := result{files: make(map[string]string), outputs: b.outputs, warnings: warningTexts(b)}
		for _, name := range listFiles(t, dir) {
			r.files[name] = readFile(t, dir, name)
		}
		results = append(results, r)
	}
	one, eight := results[0], results[1]
	if len(one.files) != len(eight.files) {
		t.Errorf("%d files with one worker, %d with 8", len(one.files), len(eight.files))
	}
	for name, data := range one.files {
		if eight.files[name] != data {
			t.Errorf("%s differs with 8 workers", name)
		}
	}
	if !reflect.DeepEqual(one.outputs, eight.outputs) {
		t.Errorf("outputs with one worker:\n%+v\nwith 8:\n%+v", one.outputs, eight.outputs)
	}
	if !slices.Equal(one.warnings, eight.warnings) {
		t.Errorf("warnings with one worker:\n%s\nwith 8:\n%s",
			strings.Join(one.warnings, "\n"), strings.Join(eight.warnings, "\n"))
	}
}

// TestSiteCSS checks the site CSS, with @charset before CSS that is not ASCII, and its URL with its version.
func TestSiteCSS(t *testing.T) {
	tests := []struct {
		name    string
		presets string // of theme.yaml
		base    string // the path of the site URL
		charset bool
	}{
		{name: "ASCII"},
		{
			name:    "not ASCII, under a path",
			presets: "presets:\n  fontFamily:\n    jp: '\"游ゴシック\", sans-serif'\n",
			base:    "/blog",
			charset: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := testSource(t)
			th := testTheme()
			th["theme.yaml"] = &fstest.MapFile{
				Data: []byte("schemaVersion: 1\nlocations:\n  header: ヘッダー\n  footer: フッター\n" + tt.presets),
			}
			th["templates/entry.html"] = &fstest.MapFile{Data: []byte("{{.SiteCSS}}\n")}
			src.theme = th
			site := testSite()
			site.URL += tt.base
			src.putSettings(t, site)
			b := mustLoad(t, src)
			dir := mustRender(t, b, 2)

			css := readFile(t, dir, "css/site.css")
			want := render.SiteCSS(b.theme.Settings)
			if tt.charset {
				want = "@charset \"UTF-8\";\n" + want
				if !strings.HasPrefix(want, "@charset \"UTF-8\";\n:root {\n  --font-family-jp: \"游ゴシック\", sans-serif;\n}\n") {
					t.Errorf("SiteCSS() =\n%s\nwithout the preset", want)
				}
			}
			if css != want {
				t.Errorf("css/site.css:\n%s\nwant:\n%s", css, want)
			}
			sum := sha256.Sum256([]byte(want))
			url := tt.base + "/css/site.css?v=" + hex.EncodeToString(sum[:4]) + "\n"
			if got := readFile(t, dir, "about/index.html"); got != url {
				t.Errorf("SiteCSS of /about/ = %q, want %q", got, url)
			}
		})
	}
}

// counterpartTexts returns the counterparts of the page at path as texts such as "en /en/about/", with " (missing)".
func counterpartTexts(t *testing.T, b *builder, path string) []string {
	t.Helper()
	p := pageAt(b, path)
	if p == nil {
		t.Fatalf("no page at %s", path)
	}
	var texts []string
	for _, c := range b.counterparts(p) {
		s := c.language + " " + c.path
		if c.missing {
			s += " (missing)"
		}
		texts = append(texts, s)
	}
	return texts
}

// TestCounterparts checks the counterparts of the pages in the languages of the site.
func TestCounterparts(t *testing.T) {
	enNewsID := newID("ent", 3)
	tests := []struct {
		name   string
		change func(t *testing.T, s *memSource)
		want   map[string][]string // by path
	}{
		{
			name: "the fixture, with a draft in en in the translation group of services",
			change: func(t *testing.T, s *memSource) {
				draft := newEntry(newID("ent", 5), 105, content.TypePage, "en", groupOf(servicesID))
				draft.Status, draft.PublishedRevision = content.StatusDraft, ""
				s.putEntry(t, draft, content.Revision{})
			},
			want: map[string][]string{
				"/":                              {"ja /", "en /en/"},
				"/about/":                        {"ja /about/", "en /en/about/"},
				"/en/about/":                     {"ja /about/", "en /en/about/"},
				"/privacy/":                      {"ja /privacy/", "en /en/privacy/"},
				"/services/":                     {"ja /services/", "en /en/ (missing)"},
				"/2026/10/26/新サービス/":             {"ja /2026/10/26/新サービス/", "en /en/2026/10/26/new-service/"},
				"/en/2026/10/26/new-service/":    {"ja /2026/10/26/新サービス/", "en /en/2026/10/26/new-service/"},
				"/2026/09/01/post-1001/":         {"ja /2026/09/01/post-1001/", "en /en/ (missing)"},
				"/works/example-corporate-site/": {"ja /works/example-corporate-site/", "en /en/ (missing)"},
				"/news/":                         {"ja /news/", "en /en/ (missing)"},
				"/news/page/2/":                  {"ja /news/page/2/", "en /en/ (missing)"},
				"/en/":                           {"ja /", "en /en/"},
				"/en/page/2/":                    {"ja /", "en /en/page/2/"},
				"/category/news/":                {"ja /category/news/", "en /en/ (missing)"},
				"/category/news/page/2/":         {"ja /category/news/page/2/", "en /en/ (missing)"},
				"/tag/イベント/":                     {"ja /tag/イベント/", "en /en/tag/events/"},
				"/en/tag/events/":                {"ja /tag/イベント/", "en /en/tag/events/"},
				"/en/tag/events/page/2/":         {"ja /tag/イベント/", "en /en/tag/events/page/2/"},
				"/works/":                        {"ja /works/", "en /en/works/"},
				"/en/works/":                     {"ja /works/", "en /en/works/"},
				"/author/yamada/":                {"ja /author/yamada/", "en /en/author/yamada/"},
				"/author/yamada/page/2/":         {"ja /author/yamada/page/2/", "en /en/author/yamada/"},
				"/en/author/yamada/page/2/":      {"ja /author/yamada/", "en /en/author/yamada/page/2/"},
				"/404.html":                      {"ja /", "en /en/"},
				"/en/404.html":                   {"ja /", "en /en/"},
			},
		},
		{
			name: "translations that are the front page and the posts page in en",
			change: func(t *testing.T, s *memSource) {
				s.putEntry(t, newEntry(enNewsID, 103, content.TypePage, "en", groupOf(newsID)), content.Revision{
					Title: "News", Slug: "news",
				})
				site := testSite()
				site.FrontPage["en"], site.PostsPage["en"] = enAboutID, enNewsID
				s.putSettings(t, site)
			},
			want: map[string][]string{
				"/":                  {"ja /", "en /en/"},
				"/about/":            {"ja /about/", "en /en/"},
				"/en/":               {"ja /", "en /en/"},
				"/news/":             {"ja /news/", "en /en/news/"},
				"/news/page/2/":      {"ja /news/page/2/", "en /en/news/"},
				"/en/news/":          {"ja /news/", "en /en/news/"},
				"/en/news/page/2/":   {"ja /news/", "en /en/news/page/2/"},
				"/privacy/":          {"ja /privacy/", "en /en/privacy/"},
				"/en/404.html":       {"ja /", "en /en/"},
				"/en/author/yamada/": {"ja /author/yamada/", "en /en/author/yamada/"},
			},
		},
		{
			name: "three languages with prefixDefaultLanguage",
			change: func(t *testing.T, s *memSource) {
				site := testSite()
				site.Languages = append(site.Languages, "fr")
				site.Name["fr"] = "Entreprise"
				site.PrefixDefaultLanguage = true
				s.putSettings(t, site)
				edit(t, s, content.RefForm, formID, func(f *content.Form) {
					for i := range f.Fields {
						x := &f.Fields[i]
						x.Label["fr"] = x.Label["en"]
						for j := range x.Options {
							x.Options[j].Label["fr"] = x.Options[j].Label["en"]
						}
					}
					f.SubmitLabel["fr"], f.Message["fr"] = f.SubmitLabel["en"], f.Message["en"]
				})
			},
			want: map[string][]string{
				"/ja/":                      {"ja /ja/", "en /en/", "fr /fr/"},
				"/fr/":                      {"ja /ja/", "en /en/", "fr /fr/"},
				"/ja/about/":                {"ja /ja/about/", "en /en/about/", "fr /fr/ (missing)"},
				"/en/tag/events/":           {"ja /ja/tag/イベント/", "en /en/tag/events/", "fr /fr/ (missing)"},
				"/fr/works/":                {"ja /ja/works/", "en /en/works/", "fr /fr/works/"},
				"/fr/author/yamada/":        {"ja /ja/author/yamada/", "en /en/author/yamada/", "fr /fr/author/yamada/"},
				"/ja/news/page/2/":          {"ja /ja/news/page/2/", "en /en/ (missing)", "fr /fr/ (missing)"},
				"/fr/404.html":              {"ja /ja/", "en /en/", "fr /fr/"},
				"/ja/category/news/":        {"ja /ja/category/news/", "en /en/ (missing)", "fr /fr/ (missing)"},
				"/en/2026/09/01/post-1012/": {"ja /ja/ (missing)", "en /en/2026/09/01/post-1012/", "fr /fr/ (missing)"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := testSource(t)
			src.addPosts(t, "ja", 11)
			src.addPosts(t, "en", 11)
			tt.change(t, src)
			b := mustLoad(t, src)
			for _, path := range slices.Sorted(maps.Keys(tt.want)) {
				if got := counterpartTexts(t, b, path); !slices.Equal(got, tt.want[path]) {
					t.Errorf("counterparts of %s = %q, want %q", path, got, tt.want[path])
				}
			}
		})
	}
}

// TestMenus checks the menus of the languages of a site under a path, and their copies for pages.
func TestMenus(t *testing.T) {
	src := testSource(t)
	site := testSite()
	site.URL = "https://example.com/blog"
	src.putSettings(t, site)
	edit(t, src, content.RefMenu, headerID, func(m *content.Menu) {
		m.Items[1].Children = append(m.Items[1].Children, content.MenuItem{Entry: draftID})
		m.Items = append(m.Items,
			content.MenuItem{Entry: postID, NewTab: true},
			content.MenuItem{Label: "準備中", Entry: draftID, Children: []content.MenuItem{{Label: "子", Href: "/child/"}}},
		)
	})
	enHeaderID, hiddenID := newID("mnu", 2), newID("mnu", 3)
	src.put(t, content.RefMenu, enHeaderID, content.Menu{
		ID: enHeaderID, Title: "Header", Location: "header", Language: "en",
		Items: []content.MenuItem{
			{Term: enEventTagID, NewTab: true},
			{Label: "日本語", Entry: aboutID},
			{Label: "Careers", Href: "/採用/"},
			{Label: "Mail", Href: "mailto:info@example.com"},
		},
	})
	src.put(t, content.RefMenu, sidebarID, content.Menu{
		ID: sidebarID, Title: "サイドバー", Location: "sidebar", Language: "ja", Items: []content.MenuItem{{Entry: aboutID}},
	})
	src.put(t, content.RefMenu, hiddenID, content.Menu{
		ID: hiddenID, Title: "表示しない", Language: "ja", Items: []content.MenuItem{{Entry: draftID}},
	})
	b := mustLoad(t, src)
	newTestResolver(t, b)

	ja := b.menusOf("ja", testRenderer(t, b, "ja"))
	wantJA := map[string][]MenuItem{
		"header": {
			{Label: "会社概要", URL: "/blog/about/"},
			{Label: "サービス", Children: []MenuItem{
				{Label: "サービス一覧", URL: "/blog/services/"},
				{Label: "制作実績", URL: "/blog/works/"},
			}},
			{Label: "お知らせ", URL: "/blog/category/news/", Children: []MenuItem{
				{Label: "プレスリリース", URL: "/blog/category/news/press-release/"},
			}},
			{Label: "採用情報", URL: "https://recruit.example.com/", Target: "_blank", Rel: "noopener"},
			{
				Label: "新サービスを始めました", URL: "/blog/2026/10/26/%E6%96%B0%E3%82%B5%E3%83%BC%E3%83%93%E3%82%B9/",
				Target: "_blank", Rel: "noopener",
			},
		},
		"footer": {
			{Label: "お問い合わせ", URL: "/blog/contact/"},
			{Label: "プライバシーポリシー", URL: "/blog/privacy/"},
			{Label: "English", URL: "/blog/en/about/"},
		},
	}
	if !reflect.DeepEqual(ja, wantJA) {
		t.Errorf("menusOf(ja) =\n%+v\nwant\n%+v", ja, wantJA)
	}
	en := b.menusOf("en", testRenderer(t, b, "en"))
	wantEN := map[string][]MenuItem{
		"header": {
			{Label: "Events", URL: "/blog/en/tag/events/", Target: "_blank", Rel: "noopener"},
			{Label: "日本語", URL: "/blog/about/"},
			{Label: "Careers", URL: "/blog/%E6%8E%A1%E7%94%A8/"},
			{Label: "Mail", URL: "mailto:info@example.com"},
		},
		"footer": nil,
	}
	if !reflect.DeepEqual(en, wantEN) {
		t.Errorf("menusOf(en) =\n%+v\nwant\n%+v", en, wantEN)
	}
	if _, ok := en["footer"]; !ok {
		t.Error("menusOf(en) has no footer")
	}
	wantWarnings := []string{
		fileOf(content.RefMenu, headerID) + ":23:9: /items/1/children/2: entry \"" + draftID +
			"\" is not published; the item and its children are left out",
		fileOf(content.RefMenu, headerID) + ":45:5: /items/5: entry \"" + draftID +
			"\" is not published; the item and its children are left out",
		fileOf(content.RefMenu, sidebarID) + ":5:3: /location: the theme has no location \"sidebar\"; the menu is left out",
	}
	if got := warningTexts(b); !slices.Equal(got, wantWarnings) {
		t.Errorf("warnings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantWarnings, "\n"))
	}

	// The items to the page are current in a copy; the menus stay as they are.
	current := currentMenus(ja, "/blog/services/")
	wantJA["header"][1].Children[0].Current = true
	if !reflect.DeepEqual(current, wantJA) {
		t.Errorf("currentMenus(/blog/services/) =\n%+v\nwant\n%+v", current, wantJA)
	}
	wantJA["header"][1].Children[0].Current = false
	if !reflect.DeepEqual(ja, wantJA) {
		t.Errorf("menusOf(ja) after currentMenus() =\n%+v", ja)
	}
	if got := currentMenus(en, "/blog/en/tag/events/"); !got["header"][0].Current || got["header"][1].Current ||
		got["footer"] != nil {
		t.Errorf("currentMenus(/blog/en/tag/events/) = %+v", got)
	}
}

// docType is a type whose blocks and richText fields are also in a group and in a list, for the schema file.
const docType = `  doc:
    url: /docs/{slug}/
    body: true
    fields:
      a: {type: blocks}
      g:
        type: group
        fields:
          t: {type: richText}
          b: {type: blocks}
      l:
        type: list
        of:
          type: group
          fields:
            c: {type: blocks}
            u: {type: richText}
      z: {type: blocks}
`

// TestRenderEntry checks the HTML, the CSS and the warnings of the body and the fields of an entry: the body and the
// blocks fields are one document, in the order of the definitions, depth first; then the richText fields.
func TestRenderEntry(t *testing.T) {
	docID := newID("ent", 4)
	src := testSource(t)
	src.theme = os.DirFS("../theme/testdata/example")
	src.schema.Data = []byte(strings.Replace(testSchema, "\ntaxonomies:\n", "\n"+docType+"taxonomies:\n", 1))
	styled := func(id string) string {
		return `{"type":"paragraph","id":"` + id + `","style":{"textColor":{"preset":"primary"}},` +
			`"content":[{"type":"text","text":"` + id + `"}]}`
	}
	wide := func(id string) string {
		return `{"type":"paragraph","id":"` + id + `","variant":"wide","content":[{"type":"text","text":"` + id + `"}]}`
	}
	highlight := func(s, attrs string) string {
		return `{"type":"text","text":"` + s + `","marks":[{"type":"highlight","attrs":` + attrs + `}]}`
	}
	var body content.Blocks
	if err := body.UnmarshalJSON([]byte(`[` + styled("b_body0001") + `,` + wide("b_body0002") + `]`)); err != nil {
		t.Fatal(err)
	}
	// Written in another order than that of the definitions.
	src.putEntry(t, newEntry(docID, 104, "doc", "ja", groupOf(docID)), content.Revision{
		Title: "文書",
		Slug:  "doc",
		Body:  body,
		Fields: content.Fields{
			"z": jsontext.Value(`[` + styled("b_z0000001") + `]`),
			"l": jsontext.Value(`[{"u":[` + highlight("一", `{"color":{"preset":"muted"}}`) + `],"c":[` +
				styled("b_lc000001") + `]},{"c":[` + styled("b_lc000002") + `,` + wide("b_lc000003") + `],"u":[` +
				highlight("二", `{"color":{"preset":"text"}}`) + `,` + highlight("三", `{"variant":"neon"}`) + `]}]`),
			"g": jsontext.Value(`{"b":[` + styled("b_gb000001") + `],"t":[` + highlight("四", `{"color":{"preset":"accent"}}`) + `]}`),
			"a": jsontext.Value(`[` + styled("b_a0000001") + `]`),
		},
	})
	b := mustLoad(t, src)
	newTestResolver(t, b)
	html, css, err := b.renderEntry(b.published(docID), testRenderer(t, b, "ja"))
	if err != nil {
		t.Fatalf("renderEntry() = %v", err)
	}

	hl := regexp.MustCompile(`<mark class="(hl-[0-9a-f]{8})">`)
	class := func(pointer string) string {
		m := hl.FindStringSubmatch(string(html[pointer]))
		if m == nil {
			t.Fatalf("no highlight class in %s: %s", pointer, html[pointer])
		}
		return m[1]
	}
	p := func(id string) template.HTML { return template.HTML(`<p class="b-` + id[2:] + `">` + id + `</p>`) }
	wantHTML := map[string]template.HTML{
		"/body":         p("b_body0001") + "\n<p>b_body0002</p>",
		"/fields/a":     p("b_a0000001"),
		"/fields/g/t":   template.HTML(`<mark class="` + class("/fields/g/t") + `">四</mark>`),
		"/fields/g/b":   p("b_gb000001"),
		"/fields/l/0/c": p("b_lc000001"),
		"/fields/l/0/u": template.HTML(`<mark class="` + class("/fields/l/0/u") + `">一</mark>`),
		"/fields/l/1/c": p("b_lc000002") + "\n<p>b_lc000003</p>",
		"/fields/l/1/u": template.HTML(`<mark class="` + class("/fields/l/1/u") + `">二</mark><mark>三</mark>`),
		"/fields/z":     p("b_z0000001"),
	}
	if !reflect.DeepEqual(html, wantHTML) {
		t.Errorf("renderEntry() HTML:\n%q\nwant:\n%q", html, wantHTML)
	}

	rule := func(selector, preset string) string {
		return selector + " {\n  color: var(--color-" + preset + ") !important;\n}\n"
	}
	wantCSS := strings.Join([]string{
		rule(".b-body0001", "primary"),
		rule(".b-a0000001", "primary"),
		rule(".b-gb000001", "primary"),
		rule(".b-lc000001", "primary"),
		rule(".b-lc000002", "primary"),
		rule(".b-z0000001", "primary"),
		rule("."+class("/fields/g/t"), "accent"),
		rule("."+class("/fields/l/0/u"), "muted"),
		rule("."+class("/fields/l/1/u"), "text"),
	}, "\n")
	if got := css.String(); got != wantCSS {
		t.Errorf("renderEntry() CSS:\n%s\nwant:\n%s", got, wantCSS)
	}

	// In the order of the revision, whose body comes after its fields.
	wantWarnings := []string{
		"/fields/l/1/c/1/variant: the theme has no paragraph variant \"wide\"; rendered without its class",
		"/fields/l/1/u/1/marks/0: the theme has no highlight variant \"neon\"; rendered without its class",
		"/body/1/variant: the theme has no paragraph variant \"wide\"; rendered without its class",
	}
	var got []string
	for _, e := range b.warnings.sorted() {
		if e.File != revisionFile(docID) || e.Line == 0 {
			t.Errorf("warning %v is not located in %s", e, revisionFile(docID))
		}
		got = append(got, e.Path+": "+e.Msg)
	}
	if !slices.Equal(got, wantWarnings) {
		t.Errorf("warnings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantWarnings, "\n"))
	}
}

// TestPageData checks the data of pages of every kind.
func TestPageData(t *testing.T) {
	src := testSource(t)
	src.addPosts(t, "ja", 11)
	src.addPosts(t, "en", 11)
	src.putOriginal(t, logoID, "logo.png", testPNG(t, 400, 100))
	b := mustLoad(t, src)
	newTestResolver(t, b)
	renderers := map[string]*render.Renderer{"ja": testRenderer(t, b, "ja"), "en": testRenderer(t, b, "en")}
	menus := map[string]map[string][]MenuItem{
		"ja": b.menusOf("ja", renderers["ja"]),
		"en": b.menusOf("en", renderers["en"]),
	}
	data := func(path string) *Page {
		t.Helper()
		p := pageAt(b, path)
		if p == nil {
			t.Fatalf("no page at %s", path)
		}
		d, css, err := b.pageData(p, renderers[p.language], menus[p.language])
		if err != nil || css.String() != "" {
			t.Fatalf("pageData(%s) = %v, CSS %q", path, err, css)
		}
		return d
	}
	ja, en := renderers["ja"], renderers["en"]
	jaSite := &Site{Name: "株式会社〇〇", Tagline: "Webサイトの企画と制作", Home: "/", Logo: ja.PageImage(logoID)}
	enSite := &Site{Name: "Example Inc.", Home: "/en/", Logo: en.PageImage(logoID)}
	translations := func(language, jaURL, enURL string, missing bool) []Translation {
		return []Translation{
			{Language: "ja", URL: template.URL(jaURL), Current: language == "ja", Missing: missing && language == "en"},
			{Language: "en", URL: template.URL(enURL), Current: language == "en", Missing: missing && language == "ja"},
		}
	}
	// summaries returns the entries of lists, without their HTML.
	summaries := func(language string, ids ...string) []*Entry {
		var es []*Entry
		for _, id := range ids {
			es = append(es, b.entryData(b.published(id), renderers[language], nil))
		}
		return es
	}
	homeBody := map[string]template.HTML{"/body": "<p>Webサイトの企画と制作の会社です。</p>"}
	newsBody := map[string]template.HTML{"/body": "<p>会社からのお知らせです。</p>"}
	tests := []struct {
		path string
		want *Page
	}{
		{"/", &Page{
			Kind: "entry", Language: "ja", Front: true, PageTitle: "株式会社〇〇", Title: "ホーム", URL: "/", Site: jaSite,
			Menus:        currentMenus(menus["ja"], "/"),
			Translations: translations("ja", "/", "/en/", false),
			Entry:        b.entryData(b.published(homeID), ja, homeBody),
		}},
		{"/news/page/2/", &Page{
			Kind: "posts", Language: "ja", PageTitle: "お知らせ｜株式会社〇〇", Title: "お知らせ", URL: "/news/page/2/",
			Site:         jaSite,
			Menus:        currentMenus(menus["ja"], "/news/page/2/"),
			Translations: translations("ja", "/news/page/2/", "/en/", true),
			Entry:        b.entryData(b.published(newsID), ja, newsBody),
			Entries:      summaries("ja", "ent_01JD0000000000000000000002", "ent_01JD0000000000000000000001"),
			Pagination:   &Pagination{Page: 2, Total: 2, Previous: "/news/"},
		}},
		{"/en/page/2/", &Page{
			Kind: "posts", Language: "en", Front: true, PageTitle: "Example Inc.", URL: "/en/page/2/", Site: enSite,
			Menus:        map[string][]MenuItem{"header": nil, "footer": nil},
			Translations: translations("en", "/", "/en/page/2/", false),
			Entries:      summaries("en", "ent_01JD0000000000000000000013", "ent_01JD0000000000000000000012"),
			Pagination:   &Pagination{Page: 2, Total: 2, Previous: "/en/"},
		}},
		{"/category/news/", &Page{
			Kind: "term", Language: "ja", PageTitle: "お知らせ｜株式会社〇〇", Title: "お知らせ", URL: "/category/news/",
			Site:         jaSite,
			Menus:        currentMenus(menus["ja"], "/category/news/"),
			Translations: translations("ja", "/category/news/", "/en/", true),
			Entries: summaries("ja", postID, "ent_01JD0000000000000000000011", "ent_01JD0000000000000000000010",
				"ent_01JD0000000000000000000009", "ent_01JD0000000000000000000008", "ent_01JD0000000000000000000007",
				"ent_01JD0000000000000000000006", "ent_01JD0000000000000000000005", "ent_01JD0000000000000000000004",
				"ent_01JD0000000000000000000003"),
			Pagination: &Pagination{Page: 1, Total: 2, Next: "/category/news/page/2/"},
			Term:       b.termData(newsCategoryID, ja),
		}},
		{"/en/works/", &Page{
			Kind: "archive", Language: "en", PageTitle: "制作実績 | Example Inc.", Title: "制作実績", URL: "/en/works/",
			Site:         enSite,
			Menus:        map[string][]MenuItem{"header": nil, "footer": nil},
			Translations: translations("en", "/works/", "/en/works/", false),
			Type:         &Type{Name: "works", Label: "制作実績"},
		}},
		{"/works/", &Page{
			Kind: "archive", Language: "ja", PageTitle: "制作実績｜株式会社〇〇", Title: "制作実績", URL: "/works/",
			Site:         jaSite,
			Menus:        currentMenus(menus["ja"], "/works/"),
			Translations: translations("ja", "/works/", "/en/works/", false),
			Entries:      summaries("ja", worksID),
			Type:         &Type{Name: "works", Label: "制作実績"},
		}},
		{"/en/author/yamada/", &Page{
			Kind: "author", Language: "en", PageTitle: "山田 太郎 | Example Inc.", Title: "山田 太郎",
			URL: "/en/author/yamada/", Site: enSite,
			Menus:        map[string][]MenuItem{"header": nil, "footer": nil},
			Translations: translations("en", "/author/yamada/", "/en/author/yamada/", false),
			Entries: summaries("en", enPostID, "ent_01JD0000000000000000000022", "ent_01JD0000000000000000000021",
				"ent_01JD0000000000000000000020", "ent_01JD0000000000000000000019", "ent_01JD0000000000000000000018",
				"ent_01JD0000000000000000000017", "ent_01JD0000000000000000000016", "ent_01JD0000000000000000000015",
				"ent_01JD0000000000000000000014"),
			Pagination: &Pagination{Page: 1, Total: 2, Next: "/en/author/yamada/page/2/"},
			Author:     b.authorData(authorID, "en", en),
		}},
		{"/en/404.html", &Page{
			Kind: "notFound", Language: "en", PageTitle: "Example Inc.", URL: "/en/404.html", Site: enSite,
			Menus:        map[string][]MenuItem{"header": nil, "footer": nil},
			Translations: translations("en", "/", "/en/", false),
		}},
	}
	for _, tt := range tests {
		tt.want.Head = b.head(pageAt(b, tt.path))
		if got := data(tt.path); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("pageData(%s) =\n%+v\nwant\n%+v", tt.path, got, tt.want)
		}
	}

	// The entries of lists are made once for every page, and the posts page is rendered once for its list.
	if data("/news/").Entries[1] != data("/category/news/").Entries[1] {
		t.Error("two lists have their own data of the same entry")
	}
	data("/about/")
	if keys := slices.Collect(maps.Keys(b.postsBodies.m)); len(keys) != 1 || keys[0] != (renderKey{newsID, ja}) {
		t.Errorf("rendered once: %v, want the posts page in ja", keys)
	}
}

// TestPagination checks the places of the pages of lists of three pages, in a site under a path.
func TestPagination(t *testing.T) {
	src := testSource(t)
	src.addPosts(t, "ja", 11)
	site := testSite()
	site.URL += "/blog"
	site.PostsPerPage = 5
	src.putSettings(t, site)
	b := mustLoad(t, src)
	newTestResolver(t, b)
	r := testRenderer(t, b, "ja")
	menus := b.menusOf("ja", r)
	tests := map[string]*Pagination{
		"/news/":        {Page: 1, Total: 3, Next: "/blog/news/page/2/"},
		"/news/page/2/": {Page: 2, Total: 3, Previous: "/blog/news/", Next: "/blog/news/page/3/"},
		"/news/page/3/": {Page: 3, Total: 3, Previous: "/blog/news/page/2/"},
		"/category/news/page/2/": {
			Page: 2, Total: 3, Previous: "/blog/category/news/", Next: "/blog/category/news/page/3/",
		},
		"/about/": nil,
	}
	for _, path := range slices.Sorted(maps.Keys(tests)) {
		data, _, err := b.pageData(pageAt(b, path), r, menus)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(data.Pagination, tests[path]) {
			t.Errorf("Pagination of %s = %+v, want %+v", path, data.Pagination, tests[path])
		}
	}
}

// TestRenderPagesConcurrent checks that renderPages renders as many pages at once as it has workers: the about page
// and the post wait for their images while the other is rendered.
func TestRenderPagesConcurrent(t *testing.T) {
	src := testSource(t)
	site := testSite()
	// No logo, icon or image for Open Graph: only the images of the two pages are inspected.
	site.Logo, site.Icon, site.SEO.OGImage = "", "", nil
	src.putSettings(t, site)
	src.putOriginal(t, imageID, "会社外観.jpg", testJPEG(t, 16, 12))
	src.putOriginal(t, avatarID, "yamada.png", testPNG(t, 16, 16))
	b := mustLoad(t, src)
	newTestResolver(t, b)
	arrived, release := make(chan string, 10), make(chan struct{})
	b.src = hookedSource{Source: src, hook: gate(arrived, release)}
	_, pub := newPublic(t)
	done := make(chan error)
	go func() { done <- b.renderPages(t.Context(), pub, 2) }()
	select {
	case <-arrived:
	case err := <-done:
		t.Fatalf("renderPages() = %v without reading an original", err)
	}
	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Error("renderPages with 2 workers rendered one page at a time")
	}
	close(release)
	if err := <-done; err != nil {
		t.Errorf("renderPages() = %v", err)
	}
}

func TestUntranslated(t *testing.T) {
	for _, tt := range []struct {
		keys []string
		want string
	}{
		{
			[]string{"postedBy"},
			`no translation in en for the key "postedBy"; pages in en use its text in ja, the default language of the theme`,
		},
		{
			[]string{"postedBy", "readMore"},
			`no translation in en for the keys "postedBy", "readMore"; pages in en use their texts in ja, the default ` +
				`language of the theme`,
		},
	} {
		if got := untranslated("en", tt.keys, "ja"); got != tt.want {
			t.Errorf("untranslated(en, %q, ja) = %q, want %q", tt.keys, got, tt.want)
		}
	}
}

// TestRenderPagesErrors checks the errors of renderPages: the first in the order of the pages, which the page or the
// revision tells.
func TestRenderPagesErrors(t *testing.T) {
	tests := []struct {
		name   string
		theme  map[string]string // files to add to testTheme or to replace
		exists string            // a file that the public directory has already
		cancel bool
		want   *regexp.Regexp
		is     error
	}{
		{
			name:  "a page template that fails",
			theme: map[string]string{"templates/entry.html": "{{.Titel}}", "templates/list.html": "{{.Titel}}"},
			// The about page is the first page.
			want: regexp.MustCompile(`^page /about/: template: templates/entry\.html:1:2: executing "entry\.html" at ` +
				`<\.Titel>: can't evaluate field Titel in type \*build\.Page$`),
		},
		{
			name:  "a block template that fails",
			theme: map[string]string{"blocks/paragraph.html": "<p>{{.Text}}</p>"},
			want: regexp.MustCompile(`^` + regexp.QuoteMeta(revisionFile(aboutID)) + `: template: blocks/paragraph\.html:1:5: ` +
				`executing "paragraph\.html" at <\.Text>: can't evaluate field Text in type render\.Paragraph$`),
		},
		{name: "a page that exists", exists: "privacy/index.html", is: fs.ErrExist},
		{name: "the site CSS that exists", exists: "css/site.css", is: fs.ErrExist},
		{
			name:   "an asset that exists",
			theme:  map[string]string{"assets/css/style.css": "p { color: red; }\n"},
			exists: "assets/css/style.css",
			is:     fs.ErrExist,
		},
		{name: "canceled", cancel: true, is: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := testSource(t)
			th := testTheme()
			for name, data := range tt.theme {
				th[name] = &fstest.MapFile{Data: []byte(data)}
			}
			src.theme = th
			b := mustLoad(t, src)
			newTestResolver(t, b)
			dir, pub := newPublic(t)
			if tt.exists != "" {
				if err := writeFile(pub, tt.exists, nil); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tt.cancel {
				cancel()
			}
			err := b.renderPages(ctx, pub, 8)
			switch {
			case err == nil:
				t.Fatal("renderPages() = nil")
			case tt.want != nil && !tt.want.MatchString(err.Error()):
				t.Errorf("renderPages() = %v, want %v", err, tt.want)
			case tt.is != nil && !errors.Is(err, tt.is):
				t.Errorf("renderPages() = %v, want %v", err, tt.is)
			}
			if tt.cancel {
				if files := listFiles(t, dir); !slices.Equal(files, []string{"css/site.css"}) {
					t.Errorf("files after canceling: %q", files)
				}
			}
		})
	}
}
