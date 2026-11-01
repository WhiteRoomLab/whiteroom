package theme

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"html/template"
	"image/png"
	"io"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/example-out")

const (
	exampleDir = "testdata/example"     // the sample theme
	exampleOut = "testdata/example-out" // the pages that its templates render, in a directory for each language
)

// exampleSettings returns the settings in the theme.yaml of the sample theme.
func exampleSettings() *content.ThemeSettings {
	presets := func(pairs ...string) []content.Preset {
		var p []content.Preset
		for i := 0; i < len(pairs); i += 2 {
			p = append(p, content.Preset{Name: pairs[i], Value: pairs[i+1]})
		}
		return p
	}
	names := func(pairs ...string) []content.ThemeName {
		var n []content.ThemeName
		for i := 0; i < len(pairs); i += 2 {
			n = append(n, content.ThemeName{Name: pairs[i], Label: pairs[i+1]})
		}
		return n
	}
	return &content.ThemeSettings{
		DefaultLanguage: "ja",
		Presets: content.Presets{
			Color:      presets("primary", "#1e73be", "accent", "#e85d04", "muted", "#f5f5f5", "text", "oklch(25% 0 0)"),
			FontSize:   presets("small", "0.875rem", "medium", "1rem", "large", "clamp(1.25rem, 1rem + 1vw, 1.5rem)"),
			Spacing:    presets("10", "0.25rem", "20", "0.5rem", "30", "1rem", "40", "2rem"),
			FontFamily: presets("body", `"Noto Sans JP", sans-serif`, "mono", "ui-monospace, monospace"),
			Gradient:   presets("sunset", "linear-gradient(135deg, #f093fb 0%, #f5576c 100%)"),
			Shadow:     presets("soft", "0 2px 4px rgba(0, 0, 0, 0.1)"),
		},
		Styles: []string{
			"textColor", "backgroundColor", "linkColor", "fontSize", "fontWeight", "lineHeight", "padding", "margin",
			"blockGap", "border",
		},
		PresetsOnly: []string{"color"},
		Blocks: []content.ThemeBlock{
			{Type: content.BlockParagraph, Variants: names("lead", "リード文")},
			{
				Type:   content.BlockHeading,
				Styles: []string{"textColor", "gradient", "fontSize", "fontFamily", "letterSpacing", "margin"},
			},
			{Type: content.BlockCode, Styles: []string{"fontSize", "backgroundColor"}},
			{Type: content.BlockGroup, Variants: names("box", "ボックス", "note", "注意書き")},
		},
		HighlightVariants: names("marker", "マーカー"),
		Locations:         names("header", "ヘッダー", "footer", "フッター"),
		PageTemplates:     names("landing", "ランディングページ"),
		ImageSizes: []content.ImageSize{
			{Name: "thumbnail", Label: "サムネイル", Width: 150, Height: 150, Crop: true},
			{Name: "medium", Label: "中", Width: 300},
			{Name: "large", Label: "大", Width: 1024},
		},
		MobileWidth: "600px",
	}
}

// examplePages are the sample pages, one for each page template of the sample theme.
var examplePages = []struct {
	page     Page
	template string // that Template chooses for the page
}{
	{Page{Kind: KindEntry, Type: "post"}, "entry.html"},
	{Page{Kind: KindEntry, Type: "works"}, "entry-works.html"},
	{Page{Kind: KindEntry, Type: "page", Template: "landing"}, "page-landing.html"},
	{Page{Kind: KindPosts}, "posts.html"},
	{Page{Kind: KindTerm, Taxonomy: "category"}, "term-category.html"},
	{Page{Kind: KindTerm, Taxonomy: "tag"}, "term.html"},
	{Page{Kind: KindArchive, Type: "works"}, "archive-works.html"},
	{Page{Kind: KindArchive, Type: "products"}, "list.html"},
	{Page{Kind: KindAuthor}, "author.html"},
	{Page{Kind: KindNotFound}, "404.html"},
}

// exampleSites are the languages in which the tests render the sample pages, with the bases of the URLs of their sites.
var exampleSites = []struct{ language, base string }{{"ja", ""}, {"en", "/blog"}}

// exampleData returns the data of the sample page for the page template name, in language, on a site whose URLs start
// with base. The data has the shape of the page data of the build (internal/build/data.go), which this package cannot
// import, in maps: every page has every key, with nil, "", 0 or false for what it does not have, and the URLs are
// template.URL, percent-encoded, with base before them.
//
// The sample site has ja at the root and en under /en/. Its works entry has no translation, and ja has a posts page,
// /news/, while the front page of en lists the latest posts. The site of en has a logo.
func exampleData(name, language, base string) map[string]any {
	ja := language == "ja"
	text := func(jaText, enText string) string {
		if ja {
			return jaText
		}
		return enText
	}
	html := func(jaHTML, enHTML string) template.HTML {
		return template.HTML(text(jaHTML, enHTML))
	}
	// link returns the URL of the page at p, a path in lang.
	link := func(lang, p string) template.URL {
		if lang != "ja" {
			p = "/" + lang + p
		}
		return template.URL(base + (&url.URL{Path: p}).EscapedPath())
	}
	here := func(p string) template.URL { return link(language, p) }
	// file returns the URL of a published file of the medium id, with a made-up version.
	file := func(id, fileName string) template.URL {
		sum := sha256.Sum256([]byte(fileName))
		return template.URL(base + "/media/" + id + "/" + fileName + "?v=" + hex.EncodeToString(sum[:4]))
	}
	image := func(src template.URL, width, height int, alt string) map[string]any {
		return map[string]any{
			"Src": src, "Srcset": template.Srcset(""), "Sizes": "", "Width": width, "Height": height, "Alt": alt,
			"HasAlt": alt != "",
		}
	}
	date := func(month, day, hour, minute int) map[string]any {
		datetime := fmt.Sprintf("2026-%02d-%02dT%02d:%02d:00+09:00", month, day, hour, minute)
		return map[string]any{"Datetime": datetime, "Year": 2026, "Month": month, "Day": day, "Hour": hour, "Minute": minute}
	}
	termLink := func(label, p string) map[string]any {
		return map[string]any{"Name": label, "URL": here(p)}
	}
	pagination := func(number, total int, previous, next string) map[string]any {
		p := map[string]any{"Page": number, "Total": total, "Previous": template.URL(""), "Next": template.URL("")}
		if previous != "" {
			p["Previous"] = here(previous)
		}
		if next != "" {
			p["Next"] = here(next)
		}
		return p
	}

	// item is an item of a menu: a page at path, a URL that opens in a new tab, or a heading of its children.
	type item struct {
		label, path, newTab string
		children            []item
	}
	var menu func(items []item, current template.URL) []map[string]any
	menu = func(items []item, current template.URL) []map[string]any {
		var m []map[string]any
		for _, it := range items {
			u, target, rel := template.URL(""), "", ""
			if it.newTab != "" {
				u, target, rel = template.URL(it.newTab), "_blank", "noopener"
			} else if it.path != "" {
				u = here(it.path)
			}
			m = append(m, map[string]any{
				"Label": it.label, "URL": u, "Target": target, "Rel": rel, "Current": u == current,
				"Children": menu(it.children, current),
			})
		}
		return m
	}
	header := []item{
		{label: text("サービス", "Services"), path: "/services/"},
		{label: text("制作実績", "Works"), path: "/works/"},
		{
			label: text("お知らせ", "News"), path: text("/news/", "/"),
			children: []item{{label: text("技術", "Technology"), path: "/category/blog/tech/"}},
		},
		{label: text("会社情報", "Company"), children: []item{
			{label: text("会社概要", "About us"), path: "/about/"},
			{label: text("採用情報", "Careers"), newTab: "https://recruit.example.com/"},
		}},
	}
	var footer []item // none in en
	if ja {
		footer = []item{{label: "会社概要", path: "/about/"}, {label: "お問い合わせ", path: "/contact/"}}
	}
	// translations returns the counterparts of the page at p, with other, the path of that in the other language, or ""
	// if it has none, for which they have the front page of that language.
	translations := func(p, other string) []map[string]any {
		var ts []map[string]any
		for _, lang := range []string{"ja", "en"} {
			tr := map[string]any{"Language": lang, "URL": link(lang, other), "Current": lang == language, "Missing": false}
			switch {
			case lang == language:
				tr["URL"] = link(lang, p)
			case other == "":
				tr["URL"], tr["Missing"] = link(lang, "/"), true
			}
			ts = append(ts, tr)
		}
		return ts
	}

	siteName := text("株式会社〇〇", "Example Inc.")
	site := map[string]any{
		"Name": siteName, "Tagline": text("Webサイトの企画と制作", "Planning and making websites"), "Home": here("/"),
		"Logo": nil,
	}
	if !ja {
		site["Logo"] = image(file("med_01JBG3N6V3X6Z9C2E5G8J1K4M7", "logo.png"), 200, 50, "Example Inc.")
	}
	// page returns the data of the page at p whose title is title, with other as for translations.
	page := func(kind, title, p, other string) map[string]any {
		u := here(p)
		return map[string]any{
			"Kind":         kind,
			"Language":     language,
			"Front":        false,
			"PageTitle":    title + " | " + siteName,
			"Title":        title,
			"URL":          u,
			"Head":         template.HTML(`<link rel="canonical" href="https://example.com` + string(u) + `">`),
			"SiteCSS":      template.URL(base + "/css/site.css?v=5f3c9e21"),
			"PageCSS":      template.URL(""),
			"Site":         site,
			"Menus":        map[string]any{"header": menu(header, u), "footer": menu(footer, u)},
			"Translations": translations(p, other),
			"Entry":        nil,
			"Entries":      nil,
			"Pagination":   nil,
			"Term":         nil,
			"Author":       nil,
			"Type":         nil,
		}
	}

	yamada := map[string]any{
		"Name": "山田 太郎",
		"URL":  here("/author/yamada/"),
		"Bio": text("編集長。Webデザインとアクセシビリティについて書いています。\n著書に『はじめてのWebアクセシビリティ』があります。",
			"Editor in chief. Writes about web design and accessibility."),
		"Avatar": image(file("med_01JB8Z3K5RJ4W7Z1D5S8X2F6C9", "yamada.png"), 256, 256, text("山田 太郎の写真", "")),
		"Links":  []template.URL{"https://x.com/example", "https://example.com/"},
	}
	sato := map[string]any{
		"Name": "佐藤 一郎", "URL": here("/author/sato/"), "Bio": "", "Avatar": nil, "Links": []template.URL(nil),
	}
	// entry returns an entry at p, as lists have it, without its body.
	entry := func(id, typ, title, p, excerpt string) map[string]any {
		return map[string]any{
			"ID":            id,
			"Type":          typ,
			"Title":         title,
			"URL":           here(p),
			"Excerpt":       excerpt,
			"Date":          nil,
			"Author":        nil,
			"Terms":         map[string]any(nil),
			"FeaturedImage": nil,
			"Sticky":        false,
			"Body":          template.HTML(""),
			"Fields":        map[string]any{},
		}
	}
	withBody := func(e map[string]any, body template.HTML) map[string]any {
		e = maps.Clone(e)
		e["Body"] = body
		return e
	}
	const scriptImageID, topImageID, newsImageID = "med_01JBQ3V6Y9B2E5H8K1N4R7T0X3", "med_01JBQ3V6Y9B2E5H8K1N4R7T0X5",
		"med_01JBQ3V6Y9B2E5H8K1N4R7T0X6"
	scriptImage := image(file(scriptImageID, "script-tag.png"), 1200, 630,
		text("エディターに書いたscript要素", "A script element in an editor"))
	scriptImage["Srcset"] = template.Srcset(file(scriptImageID, "script-tag-768x403.png") + " 768w, " +
		file(scriptImageID, "script-tag.png") + " 1200w")
	scriptImage["Sizes"] = "(max-width: 1200px) 100vw, 1200px"
	topImage := image(file(topImageID, "top.png"), 1600, 1000, text("トップページ", "The front page"))
	topImage["Srcset"] = template.Srcset(file(topImageID, "top-1024x640.png") + " 1024w, " +
		file(topImageID, "top.png") + " 1600w")
	topImage["Sizes"] = "(max-width: 1600px) 100vw, 1600px"

	tech := text("技術", "Technology")
	script := entry("ent_01JBQ3V6Y9B2E5H8K1N4R7T0W3", "post", text("<script>タグの使い方", "How to use the <script> tag"),
		"/2026/10/31/script-tag/", text("JavaScriptを書く方法です。", "How to write JavaScript in HTML."))
	maps.Copy(script, map[string]any{
		"Date":   date(10, 31, 9, 0),
		"Author": yamada,
		"Terms": map[string]any{
			"category": []map[string]any{termLink(tech, "/category/blog/tech/")},
			"tag":      []map[string]any{termLink("HTML", "/tag/html/")},
		},
		"FeaturedImage": scriptImage,
	})
	meeting := entry("ent_01JBQ3V6Y9B2E5H8K1N4R7T0W4", "post", text("秋の勉強会のお知らせ", "Autumn study meeting"),
		text("/2026/10/15/秋の勉強会/", "/2026/10/15/autumn-study-meeting/"),
		text("11月に勉強会を開きます。", "We hold a study meeting in November."))
	maps.Copy(meeting, map[string]any{
		"Date":   date(10, 15, 18, 30),
		"Author": sato,
		"Terms": map[string]any{
			"category": []map[string]any{termLink(text("お知らせ", "News"), "/category/news/")},
			"tag":      []map[string]any{termLink(text("イベント", "Events"), text("/tag/イベント/", "/tag/events/"))},
		},
		"Sticky": true,
	})
	works := entry("ent_01JBQ3V6Y9B2E5H8K1N4R7T0W5", "works", text("コーポレートサイトの制作", "Corporate website"),
		"/works/corporate-site/", text("株式会社サンプルのサイトです。", "The website of Sample Corp."))
	maps.Copy(works, map[string]any{
		"Terms": map[string]any{
			"works_category": []map[string]any{
				termLink(text("コーポレートサイト", "Corporate sites"), "/works_category/corporate/"),
			},
		},
		"FeaturedImage": topImage,
		"Fields": map[string]any{
			"client": text("株式会社サンプル", "Sample Corp."),
			"year":   2026.0,
			"images": []any{
				map[string]any{
					"URL": file(topImageID, "top.png"), "Kind": "image", "FileName": "top.png", "MIMEType": "image/png",
					"Size": 482133, "Image": topImage,
				},
				map[string]any{
					"URL": file(newsImageID, "news.png"), "Kind": "image", "FileName": "news.png", "MIMEType": "image/png",
					"Size": 125407, "Image": image(file(newsImageID, "news.png"), 800, 500, ""),
				},
			},
		},
	})
	product := entry("ent_01JBQ3V6Y9B2E5H8K1N4R7T0W6", "products", text("見本の製品", "Sample product"),
		"/products/sample/", text("製品の説明です。", "A description of the product."))

	var data map[string]any
	switch name {
	case "entry.html":
		data = page("entry", script["Title"].(string), "/2026/10/31/script-tag/", "/2026/10/31/script-tag/")
		data["Entry"] = withBody(script, html(
			`<p class="is-variant-lead"><code>&lt;script&gt;</code>タグには、JavaScriptを書きます。</p>`+"\n"+
				`<p>ほかのファイルは、<mark class="is-variant-marker">src属性</mark>で読み込みます。</p>`,
			`<p class="is-variant-lead">The <code>&lt;script&gt;</code> tag holds JavaScript.</p>`+"\n"+
				`<p>It loads other files with <mark class="is-variant-marker">the src attribute</mark>.</p>`))
	case "entry-works.html":
		data = page("entry", works["Title"].(string), "/works/corporate-site/", "")
		data["Entry"] = withBody(works, html("<p>会社案内のサイトを、企画から制作しました。</p>",
			"<p>We planned and made the website of the company.</p>"))
	case "page-landing.html":
		title := text("サービス", "Services")
		data = page("entry", title, "/services/", "/services/")
		data["PageCSS"] = template.URL(base + "/css/8d1f4b7a2c6e9035.css")
		data["Entry"] = withBody(
			entry("ent_01JBQ3V6Y9B2E5H8K1N4R7T0W7", "page", title, "/services/",
				text("Webサイトの企画から制作まで承ります。", "We plan and make websites.")),
			template.HTML(fmt.Sprintf("<section class=\"b-7k2m9p4q is-variant-box\">\n<h2 id=\"b_3x8n1v6c\">%s</h2>\n"+
				"<p>%s</p>\n</section>\n<div class=\"is-variant-note\">\n<p>%s</p>\n</div>",
				text("企画", "Planning"), text("サイトの目的から、一緒に考えます。", "We start from the purpose of the site."),
				text("お見積もりは無料です。", "Estimates are free."))))
	case "posts.html":
		if ja {
			data = page("posts", "お知らせ", "/news/page/2/", "")
			data["Entry"] = withBody(
				entry("ent_01JBQ3V6Y9B2E5H8K1N4R7T0W8", "page", "お知らせ", "/news/", "新しいサービスやイベントのお知らせです。"),
				"<p>新しいサービスやイベントのお知らせです。</p>")
			data["Pagination"] = pagination(2, 3, "/news/", "/news/page/3/")
		} else {
			data = page("posts", "", "/page/2/", "/")
			data["Front"], data["PageTitle"] = true, siteName
			data["Pagination"] = pagination(2, 3, "/", "/page/3/")
		}
		data["Entries"] = []map[string]any{script, meeting}
	case "term-category.html":
		data = page("term", tech, "/category/blog/tech/", "/category/blog/tech/")
		data["Term"] = map[string]any{
			"Name": tech, "Description": text("Webの技術についての記事です。", "Articles on web technology."),
			"URL": here("/category/blog/tech/"), "Taxonomy": "category",
			"Parent": termLink(text("ブログ", "Blog"), "/category/blog/"),
		}
		data["Entries"] = []map[string]any{script}
		data["Pagination"] = pagination(1, 2, "", "/category/blog/tech/page/2/")
	case "term.html":
		events := text("イベント", "Events")
		data = page("term", events, text("/tag/イベント/", "/tag/events/"), text("/tag/events/", "/tag/イベント/"))
		data["Term"] = map[string]any{
			"Name": events, "Description": "", "URL": data["URL"], "Taxonomy": "tag", "Parent": nil,
		}
		data["Entries"] = []map[string]any{meeting}
	case "archive-works.html":
		// The labels of types are those of the schema, in every language.
		data = page("archive", "制作実績", "/works/", "/works/")
		data["Type"] = map[string]any{"Name": "works", "Label": "制作実績"}
		data["Entries"] = []map[string]any{works}
	case "list.html":
		data = page("archive", "製品", "/products/", "/products/")
		data["Type"] = map[string]any{"Name": "products", "Label": "製品"}
		data["Entries"] = []map[string]any{product}
	case "author.html":
		data = page("author", "山田 太郎", "/author/yamada/page/2/", "/author/yamada/")
		data["Author"] = yamada
		data["Entries"] = []map[string]any{script}
		data["Pagination"] = pagination(2, 2, "/author/yamada/", "")
	case "404.html":
		data = page("notFound", "", "/404.html", "/")
		data["PageTitle"] = siteName
		data["Head"] = template.HTML(`<meta name="robots" content="noindex">`)
		data["Translations"] = translations("/", "/")
	default:
		panic("no sample page for " + name)
	}
	return data
}

func TestExampleLoad(t *testing.T) {
	th := mustLoad(t, os.DirFS(exampleDir))
	if want := exampleSettings(); !reflect.DeepEqual(th.Settings, want) {
		t.Errorf("Settings =\n%#v\nwant\n%#v", th.Settings, want)
	}
	if got, want := th.Assets(), []string{"css/style.css", "images/logo.png"}; !slices.Equal(got, want) {
		t.Errorf("Assets() = %q, want %q", got, want)
	}
	if got, want := th.MissingTranslations("en"), []string{"postedBy"}; !slices.Equal(got, want) {
		t.Errorf("MissingTranslations(en) = %q, want %q", got, want)
	}
	if got := th.MissingTranslations("ja"); got != nil {
		t.Errorf("MissingTranslations(ja) = %q, want nil", got)
	}
	blocks := []content.BlockType{content.BlockFile, content.BlockTable}
	if got := slices.Sorted(maps.Keys(th.blocks)); !slices.Equal(got, blocks) {
		t.Errorf("block templates = %q, want %q", got, blocks)
	}
	r := mustRenderer(t, th, "ja", "")
	for bt, want := range map[content.BlockType]bool{
		content.BlockTable: true, content.BlockFile: true, content.BlockParagraph: false,
	} {
		if got := r.HasBlock(bt); got != want {
			t.Errorf("HasBlock(%q) = %v, want %v", bt, got, want)
		}
	}
	if logo, err := png.Decode(bytes.NewReader(th.assets["images/logo.png"].data)); err != nil {
		t.Errorf("images/logo.png: %v", err)
	} else if size := logo.Bounds().Size(); size.X != 32 || size.Y != 32 {
		t.Errorf("images/logo.png has %v pixels, want 32x32 as in header.html", size)
	}

	want, wantTexts := withoutNamespaces(th)
	got, gotTexts := withoutNamespaces(mustLoad(t, openRoot(t, exampleDir).FS()))
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotTexts, wantTexts) {
		t.Errorf("Load() through os.Root = %+v, want %+v", got, want)
	}
}

func TestExampleTemplate(t *testing.T) {
	th := mustLoad(t, os.DirFS(exampleDir))
	var names []string
	for _, p := range examplePages {
		names = append(names, p.template)
		if got := th.Template(p.page); got != p.template {
			t.Errorf("Template(%+v) = %q, want %q", p.page, got, p.template)
		}
	}
	slices.Sort(names)
	if got := slices.Sorted(maps.Keys(th.namespaces)); !slices.Equal(got, names) {
		t.Errorf("page templates = %q, want those of the sample pages, %q", got, names)
	}
	// Pages without page templates of their own.
	for _, p := range []Page{
		{Kind: KindEntry, Type: "page"},
		{Kind: KindEntry, Type: "page", Template: "wide"},
		{Kind: KindEntry, Type: "products"},
	} {
		if got := th.Template(p); got != "entry.html" {
			t.Errorf("Template(%+v) = %q, want entry.html", p, got)
		}
	}
}

// TestExamplePages renders the sample pages and compares them with the files in testdata/example-out, which
// go test -update rewrites.
func TestExamplePages(t *testing.T) {
	th := mustLoad(t, os.DirFS(exampleDir))
	for _, site := range exampleSites {
		r := mustRenderer(t, th, site.language, site.base)
		dir := filepath.Join(exampleOut, site.language)
		if *update {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		var names []string
		for _, p := range examplePages {
			names = append(names, p.template)
			got := execute(t, r, p.template, exampleData(p.template, site.language, site.base))
			file := filepath.Join(dir, p.template)
			if *update {
				if err := os.WriteFile(file, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				continue
			}
			want, err := os.ReadFile(file)
			if err != nil {
				t.Errorf("%v; go test -update writes it", err)
			} else if got != string(want) {
				t.Errorf("Execute(%s) in %s differs from %s; go test -update rewrites it:\n%s",
					p.template, site.language, file, got)
			}
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var files []string
		for _, d := range entries {
			files = append(files, d.Name())
		}
		slices.Sort(names)
		if !slices.Equal(files, names) {
			t.Errorf("%s has %q, want %q", dir, files, names)
		}
	}
}

// TestExampleFallbacks executes entry.html and list.html, which the pages without page templates of their own get, with
// the data of the other sample pages of their kinds, such as a page and the latest posts at the front of en.
func TestExampleFallbacks(t *testing.T) {
	th := mustLoad(t, os.DirFS(exampleDir))
	fallbacks := map[string][]string{
		"entry.html": {"entry-works.html", "page-landing.html"},
		"list.html":  {"posts.html", "term-category.html", "term.html", "archive-works.html", "author.html"},
	}
	for _, site := range exampleSites {
		r := mustRenderer(t, th, site.language, site.base)
		for name, pages := range fallbacks {
			for _, p := range pages {
				if err := r.Execute(io.Discard, name, exampleData(p, site.language, site.base)); err != nil {
					t.Errorf("Execute(%s) with the data of %s in %s: %v", name, p, site.language, err)
				}
			}
		}
	}
}

func TestExampleWriteAssets(t *testing.T) {
	th := mustLoad(t, os.DirFS(exampleDir))
	out := t.TempDir()
	if err := th.WriteAssets(openRoot(t, out)); err != nil {
		t.Fatalf("WriteAssets() = %v", err)
	}
	var written []string
	err := filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(out, p)
		if err != nil {
			return err
		}
		written = append(written, filepath.ToSlash(rel))
		got, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		want, err := os.ReadFile(filepath.Join(exampleDir, rel))
		if err != nil {
			return err
		}
		if !bytes.Equal(got, want) {
			t.Errorf("WriteAssets() wrote %s with other bytes than the theme has", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"assets/css/style.css", "assets/images/logo.png"}; !slices.Equal(written, want) {
		t.Errorf("WriteAssets() wrote %q, want %q", written, want)
	}
}
