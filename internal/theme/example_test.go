package theme

import (
	"bytes"
	"flag"
	"html/template"
	"image/png"
	"io/fs"
	"maps"
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
// with base.
//
// The data is provisional: the build, which is not written yet, will define the data of pages. Until then, the sample
// templates read maps of this shape, with "" or nil for what a page does not have:
//
//   - every page: Language; Site, with Name and Home (the URL of the front page); Menus, by location (header and
//     footer), each a list of items with Label, URL and Children; Title, except the page of 404.html, whose title the
//     theme writes with t
//   - entries: Body (template.HTML, the rich text that the core renders), Date and Author of posts, the fields of
//     custom types (Client and Year of works), and Sections of page-landing.html, each with the Class of its variant,
//     such as is-variant-box, and its Body
//   - lists: Entries, each with Title, URL, Excerpt and Author; Pagination of a list on several pages, with Page, Total
//     and the URLs Previous and Next; Description of terms; Bio of authors
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
	url := func(p string) string {
		if !ja {
			p = "/" + language + p
		}
		return base + p
	}
	item := func(label, url string, children ...map[string]any) map[string]any {
		return map[string]any{"Label": label, "URL": url, "Children": children}
	}
	entry := func(title, url, excerpt, author string) map[string]any {
		return map[string]any{"Title": title, "URL": url, "Excerpt": excerpt, "Author": author}
	}
	pagination := func(page, total int, previous, next string) map[string]any {
		return map[string]any{"Page": page, "Total": total, "Previous": previous, "Next": next}
	}

	var footer []map[string]any // none in en
	if ja {
		footer = []map[string]any{item("会社概要", url("/about/")), item("お問い合わせ", url("/contact/"))}
	}
	data := map[string]any{
		"Language": language,
		"Site":     map[string]any{"Name": text("株式会社〇〇", "Example Inc."), "Home": url("/")},
		"Menus": map[string]any{
			"header": []map[string]any{
				item(text("サービス", "Services"), url("/services/")),
				item(text("制作実績", "Works"), url("/works/")),
				item(text("お知らせ", "News"), url("/news/"), item(text("技術", "Technology"), url("/category/tech/"))),
			},
			"footer": footer,
		},
	}

	script := entry(text("<script>タグの使い方", "How to use the <script> tag"), url("/2026/10/31/script-tag/"),
		text("JavaScriptを書く方法です。", "How to write JavaScript in HTML."), "Hanako Yamada")
	meeting := entry(text("秋の勉強会のお知らせ", "Autumn study meeting"),
		url(text("/2026/10/15/秋の勉強会/", "/2026/10/15/autumn-study-meeting/")),
		text("11月に勉強会を開きます。", "We hold a study meeting in November."), "Ichiro Sato")
	works := entry(text("コーポレートサイトの制作", "Corporate website"), url("/works/corporate-site/"),
		text("株式会社サンプルのサイトです。", "The website of Sample Corp."), "")
	product := entry(text("見本の製品", "Sample product"), url("/products/sample/"),
		text("製品の説明です。", "A description of the product."), "")

	var page map[string]any
	switch name {
	case "entry.html":
		page = map[string]any{
			"Title":  script["Title"],
			"Date":   "2026-10-31",
			"Author": script["Author"],
			"Body": html(
				`<p class="is-variant-lead"><code>&lt;script&gt;</code>タグには、JavaScriptを書きます。</p>`+"\n"+
					`<p>ほかのファイルは、<mark class="is-variant-marker">src属性</mark>で読み込みます。</p>`,
				`<p class="is-variant-lead">The <code>&lt;script&gt;</code> tag holds JavaScript.</p>`+"\n"+
					`<p>It loads other files with <mark class="is-variant-marker">the src attribute</mark>.</p>`),
		}
	case "entry-works.html":
		page = map[string]any{
			"Title":  works["Title"],
			"Client": text("株式会社サンプル", "Sample Corp."),
			"Year":   2026,
			"Body":   html("<p>会社案内のサイトを、企画から制作しました。</p>", "<p>We planned and made the website of the company.</p>"),
		}
	case "page-landing.html":
		page = map[string]any{
			"Title": text("サービス", "Services"),
			"Sections": []map[string]any{
				{
					"Class": "is-variant-box",
					"Body": html("<h2>企画</h2>\n<p>サイトの目的から、一緒に考えます。</p>",
						"<h2>Planning</h2>\n<p>We start from the purpose of the site.</p>"),
				},
				{"Class": "is-variant-note", "Body": html("<p>お見積もりは無料です。</p>", "<p>Estimates are free.</p>")},
			},
		}
	case "posts.html":
		page = map[string]any{
			"Title":      text("お知らせ", "News"),
			"Entries":    []map[string]any{script, meeting},
			"Pagination": pagination(2, 3, url("/news/"), url("/news/page/3/")),
		}
	case "term-category.html":
		page = map[string]any{
			"Title":       text("技術", "Technology"),
			"Description": text("Webの技術についての記事です。", "Articles on web technology."),
			"Entries":     []map[string]any{script},
			"Pagination":  pagination(1, 2, "", url("/category/tech/page/2/")),
		}
	case "term.html":
		page = map[string]any{
			"Title":       text("イベント", "Events"),
			"Description": "",
			"Entries":     []map[string]any{meeting},
			"Pagination":  nil,
		}
	case "archive-works.html":
		page = map[string]any{
			"Title":      text("制作実績", "Works"),
			"Entries":    []map[string]any{works},
			"Pagination": nil,
		}
	case "list.html":
		page = map[string]any{
			"Title":      text("製品", "Products"),
			"Entries":    []map[string]any{product},
			"Pagination": nil,
		}
	case "author.html":
		page = map[string]any{
			"Title":      "Hanako Yamada",
			"Bio":        text("サイトの設計を担当しています。", "She designs websites."),
			"Entries":    []map[string]any{script},
			"Pagination": pagination(2, 2, url("/author/yamada/"), ""),
		}
	case "404.html":
	default:
		panic("no sample page for " + name)
	}
	maps.Copy(data, page)
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
