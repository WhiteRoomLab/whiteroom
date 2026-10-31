package theme

import (
	"errors"
	"fmt"
	"html/template"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"unsafe"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

const (
	styleCSS     = "body { color: red; }\n"
	styleVersion = "9767e91e" // the first 8 hex digits of the SHA-256 of styleCSS
	logoVersion  = "4c4b6a3b" // and of logoPNG
)

// renderFiles returns the changes to themeFS for the tests of Renderer: a layout, a card, and page templates and a
// block template that use the functions, and translations in which ja is the default language, en lacks postedBy, fr
// has every key, and zh, zh-Hant and zh-Hant-TW have fewer and fewer keys.
func renderFiles() map[string]*fstest.MapFile {
	return map[string]*fstest.MapFile{
		"theme.yaml":                   file("schemaVersion: 1\ndefaultLanguage: ja\n"),
		"translations/ja.yaml":         file("readMore: 続きを読む\npageOf: \"{page} / {total} ページ\"\npostedBy: \"{name} が投稿\"\nnext: 次へ\n"),
		"translations/en.yaml":         file("readMore: Read more\npageOf: \"Page {page} of {total}\"\nnext: Next\n"),
		"translations/fr.yaml":         file("readMore: Lire la suite\npageOf: \"Page {page} sur {total}\"\npostedBy: \"Publié par {name}\"\nnext: Suivant\n"),
		"translations/zh.yaml":         file("readMore: zh\npageOf: \"{page}/{total} zh\"\npostedBy: \"{name} zh\"\n"),
		"translations/zh-Hant.yaml":    file("readMore: zh-Hant\npageOf: \"{page}/{total} zh-Hant\"\n"),
		"translations/zh-Hant-TW.yaml": file("readMore: zh-Hant-TW\n"),
		"assets/css/style.css":         file(styleCSS),
		"components/layout.html": file(`<html lang="{{.Lang}}"><link rel="stylesheet" href="{{asset "css/style.css"}}">` +
			`<main>{{block "main" .}}{{end}}</main></html>`),
		"components/entry/card.html": file(`<article><a href="{{.url}}">{{.title}}</a> {{t "postedBy" "name" .author}}</article>`),
		"templates/entry.html": file(`{{template "layout" .}}{{define "main"}}<h1>{{.Title}}</h1>{{.Body}}` +
			`<p>{{t "pageOf" "page" .Page "total" .Total}}</p>` +
			`{{template "entry/card" dict "title" .Title "url" .URL "author" .Author}}<a href="{{.Next}}">{{t "next"}}</a>{{end}}`),
		"templates/list.html": file(`{{t "readMore"}}|{{t "pageOf" "page" 1 "total" 2}}|{{t "postedBy" "name" "A"}}|{{t "next"}}`),
		// Clones share the nodes in parentheses before a field.
		"templates/author.html": file(`{{with (dict "name" .Author).name}}<p>{{t "postedBy" "name" .}}</p>{{end}}`),
		"blocks/file.html": file(`<p><a href="{{.Href}}">{{.Label}}</a> ({{t "pageOf" "page" 1 "total" .Pages}})` +
			`<link rel="stylesheet" href="{{asset "css/style.css"}}"></p>` +
			`{{template "entry/card" dict "title" .Label "url" .Href "author" .Author}}` + "\n"),
	}
}

// entryData is the data of templates/entry.html of renderFiles.
func entryData(language string) map[string]any {
	return map[string]any{
		"Lang":   language,
		"Title":  `<script>alert("x")</script>`,
		"Body":   template.HTML("<p>本文</p>"),
		"Page":   2,
		"Total":  5,
		"URL":    "/2026/10/31/日記/",
		"Author": "A&B",
		"Next":   "/page/3/?q=a b",
	}
}

// fileData is the data of blocks/file.html of renderFiles, for a site whose URLs start with base.
func fileData(language, base string) map[string]any {
	label := "Company guide"
	if language == "ja" {
		label = "会社案内"
	}
	return map[string]any{"Href": base + "/files/guide.pdf", "Label": label, "Pages": 3, "Author": "A&B"}
}

func mustRenderer(t *testing.T, th *Theme, language, base string) *Renderer {
	t.Helper()
	r, err := th.Renderer(language, base)
	if err != nil {
		t.Fatalf("Renderer(%q, %q) error: %v", language, base, err)
	}
	return r
}

func execute(t *testing.T, r *Renderer, name string, data any) string {
	t.Helper()
	var b strings.Builder
	if err := r.Execute(&b, name, data); err != nil {
		t.Fatalf("Execute(%s) error: %v", name, err)
	}
	return b.String()
}

func executeBlock(t *testing.T, r *Renderer, bt content.BlockType, data any) string {
	t.Helper()
	var b strings.Builder
	if err := r.ExecuteBlock(&b, bt, data); err != nil {
		t.Fatalf("ExecuteBlock(%s) error: %v", bt, err)
	}
	return b.String()
}

func TestTemplate(t *testing.T) {
	const settings = "schemaVersion: 1\npageTemplates:\n  landing: ランディングページ\n"
	// Each template writes its name.
	full := themeFS(map[string]*fstest.MapFile{"theme.yaml": file(settings)})
	for _, name := range []string{
		"entry.html", "entry-works.html", "entry-page.html", "page-landing.html", "list.html", "posts.html", "term.html",
		"term-category.html", "archive-works.html", "author.html", "404.html",
	} {
		full["templates/"+name] = file(strings.TrimSuffix(name, ".html"))
	}
	partial := themeFS(map[string]*fstest.MapFile{
		"theme.yaml":                  file(settings),
		"templates/entry.html":        file("entry"),
		"templates/list.html":         file("list"),
		"templates/page-landing.html": file("page-landing"),
		"templates/term.html":         file("term"),
	})
	minimal := themeFS(map[string]*fstest.MapFile{"templates/entry.html": file("entry"), "templates/list.html": file("list")})
	themes := []*Theme{mustLoad(t, full), mustLoad(t, partial), mustLoad(t, minimal)}

	tests := []struct {
		page Page
		want [3]string // with full, partial and minimal
	}{
		{Page{Kind: KindEntry, Type: "post"}, [3]string{"entry.html", "entry.html", "entry.html"}},
		{Page{Kind: KindEntry, Type: "works"}, [3]string{"entry-works.html", "entry.html", "entry.html"}},
		{Page{Kind: KindEntry, Type: "page"}, [3]string{"entry-page.html", "entry.html", "entry.html"}},
		{Page{Kind: KindEntry, Type: "page", Template: "landing"}, [3]string{"page-landing.html", "page-landing.html", "entry.html"}},
		// A page template that the theme does not have, as after a change of themes.
		{Page{Kind: KindEntry, Type: "page", Template: "wide"}, [3]string{"entry-page.html", "entry.html", "entry.html"}},
		// Only pages have page templates.
		{Page{Kind: KindEntry, Type: "works", Template: "landing"}, [3]string{"entry-works.html", "entry.html", "entry.html"}},
		{Page{Kind: KindEntry, Type: "post", Template: "landing"}, [3]string{"entry.html", "entry.html", "entry.html"}},
		{Page{Kind: KindEntry}, [3]string{"entry.html", "entry.html", "entry.html"}},
		{Page{Kind: KindPosts}, [3]string{"posts.html", "list.html", "list.html"}},
		{Page{Kind: KindTerm, Taxonomy: "category"}, [3]string{"term-category.html", "term.html", "list.html"}},
		{Page{Kind: KindTerm, Taxonomy: "tag"}, [3]string{"term.html", "term.html", "list.html"}},
		{Page{Kind: KindArchive, Type: "works"}, [3]string{"archive-works.html", "list.html", "list.html"}},
		{Page{Kind: KindArchive, Type: "news"}, [3]string{"list.html", "list.html", "list.html"}},
		{Page{Kind: KindAuthor}, [3]string{"author.html", "list.html", "list.html"}},
		{Page{Kind: KindNotFound}, [3]string{"404.html", "", ""}},
		// The fields that a kind does not use do not count.
		{Page{Kind: KindPosts, Type: "works", Taxonomy: "category", Template: "landing"}, [3]string{"posts.html", "list.html", "list.html"}},
		{Page{Kind: KindTerm, Type: "works", Template: "landing"}, [3]string{"term.html", "term.html", "list.html"}},
		{Page{Kind: KindArchive, Taxonomy: "category", Template: "landing"}, [3]string{"list.html", "list.html", "list.html"}},
		{Page{Kind: KindAuthor, Type: "works", Taxonomy: "category"}, [3]string{"author.html", "list.html", "list.html"}},
		{Page{Kind: KindNotFound, Type: "page", Template: "landing"}, [3]string{"404.html", "", ""}},
		// Names that no file can have.
		{Page{Kind: KindEntry, Type: "page", Template: "landing.html"}, [3]string{"entry-page.html", "entry.html", "entry.html"}},
		{Page{Kind: KindTerm, Taxonomy: "../category"}, [3]string{"term.html", "term.html", "list.html"}},
	}
	for i, th := range themes {
		r := mustRenderer(t, th, "ja", "")
		for _, tt := range tests {
			got := th.Template(tt.page)
			if got != tt.want[i] {
				t.Errorf("theme %d: Template(%+v) = %q, want %q", i, tt.page, got, tt.want[i])
				continue
			}
			if got == "" {
				continue
			}
			if out := execute(t, r, got, nil); out != strings.TrimSuffix(got, ".html") {
				t.Errorf("theme %d: Execute(%s) = %q, want its name", i, got, out)
			}
		}
	}
}

func TestTemplateUnknownKind(t *testing.T) {
	th := mustLoad(t, themeFS(nil))
	defer func() {
		if r := recover(); r != "theme: unknown kind 6" {
			t.Errorf("recover() = %v, want the panic for kind 6", r)
		}
	}()
	th.Template(Page{Kind: KindNotFound + 1})
	t.Error("Template() did not panic")
}

func TestRendererArguments(t *testing.T) {
	th := mustLoad(t, themeFS(nil))
	for _, language := range []string{"ja", "en-US", "zh-Hant", "zh-Hant-TW", "es-419"} {
		for _, base := range []string{"", "/blog", "https://example.com", "https://example.com/blog"} {
			if r, err := th.Renderer(language, base); r == nil || err != nil {
				t.Errorf("Renderer(%q, %q) = %v, %v", language, base, r, err)
			}
		}
	}
	const (
		language = `: use a language tag such as "ja", "en-US" or "zh-Hant"`
		base     = `: must not end with "/"; use "" for a site at the root`
	)
	tests := []struct{ language, base, want string }{
		{"", "", `invalid language code ""` + language},
		{"EN", "", `invalid language code "EN"` + language},
		{"en-us", "", `invalid language code "en-us"` + language},
		{"ja_JP", "", `invalid language code "ja_JP"` + language},
		{"ja", "/", `invalid base "/"` + base},
		{"ja", "/blog/", `invalid base "/blog/"` + base},
		{"ja", "https://example.com/", `invalid base "https://example.com/"` + base},
		{"EN", "/", `invalid language code "EN"` + language},
	}
	for _, tt := range tests {
		r, err := th.Renderer(tt.language, tt.base)
		if r != nil || err == nil || err.Error() != tt.want {
			t.Errorf("Renderer(%q, %q) = %v, %v, want error %q", tt.language, tt.base, r, err, tt.want)
		}
	}
}

func TestRendererExecute(t *testing.T) {
	th := mustLoad(t, themeFS(renderFiles()))
	const (
		head  = `<link rel="stylesheet" href="/assets/css/style.css?v=` + styleVersion + `"><main>`
		title = "&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;"
		card  = `<article><a href="/2026/10/31/%e6%97%a5%e8%a8%98/">` + title + "</a> A&amp;B が投稿</article>"
	)
	tests := []struct{ language, want string }{
		{"ja", `<html lang="ja">` + head + "<h1>" + title + "</h1><p>本文</p><p>2 / 5 ページ</p>" + card +
			`<a href="/page/3/?q=a%20b">次へ</a></main></html>`},
		// postedBy of the card falls back to ja.
		{"en", `<html lang="en">` + head + "<h1>" + title + "</h1><p>本文</p><p>Page 2 of 5</p>" + card +
			`<a href="/page/3/?q=a%20b">Next</a></main></html>`},
	}
	for _, tt := range tests {
		r := mustRenderer(t, th, tt.language, "")
		if got := execute(t, r, "entry.html", entryData(tt.language)); got != tt.want {
			t.Errorf("Execute(entry.html) in %s =\n%s\nwant\n%s", tt.language, got, tt.want)
		}
	}
}

func TestRendererLanguages(t *testing.T) {
	th := mustLoad(t, themeFS(renderFiles()))
	const (
		ja = "続きを読む|1 / 2 ページ|A が投稿|次へ"
		en = "Read more|Page 1 of 2|A が投稿|Next"
		zh = "zh|1/2 zh|A zh|次へ"
	)
	tests := []struct{ language, want string }{
		{"ja", ja},
		{"ja-JP", ja},
		{"en", en},
		{"en-US", en},
		{"fr", "Lire la suite|Page 1 sur 2|Publié par A|Suivant"},
		{"zh-Hant-TW", "zh-Hant-TW|1/2 zh-Hant|A zh|次へ"},
		{"zh-Hant", "zh-Hant|1/2 zh-Hant|A zh|次へ"},
		{"zh", zh},
		{"zh-TW", zh},
		{"zh-Hans-CN", zh},
		{"de", ja},
	}
	for _, tt := range tests {
		if got := execute(t, mustRenderer(t, th, tt.language, ""), "list.html", nil); got != tt.want {
			t.Errorf("Execute(list.html) in %s = %q, want %q", tt.language, got, tt.want)
		}
	}
}

// TestRendererDefaultLanguage checks a default language with a subtag, which the other languages fall back to.
func TestRendererDefaultLanguage(t *testing.T) {
	th := mustLoad(t, themeFS(map[string]*fstest.MapFile{
		"theme.yaml":                file("schemaVersion: 1\ndefaultLanguage: zh-Hant\n"),
		"translations/zh-Hant.yaml": file("a: 甲\nb: 乙\n"),
		"translations/zh.yaml":      file("a: A\n"),
		"templates/list.html":       file(`{{t "a"}}{{t "b"}}`),
	}))
	tests := []struct {
		language string
		want     string
		missing  []string
	}{
		{"zh-Hant", "甲乙", nil},
		{"zh-Hant-TW", "甲乙", nil},
		{"zh", "A乙", []string{"b"}},
		{"zh-CN", "A乙", []string{"b"}},
		{"en", "甲乙", []string{"a", "b"}},
	}
	for _, tt := range tests {
		if got := execute(t, mustRenderer(t, th, tt.language, ""), "list.html", nil); got != tt.want {
			t.Errorf("Execute(list.html) in %s = %q, want %q", tt.language, got, tt.want)
		}
		if got := th.MissingTranslations(tt.language); !slices.Equal(got, tt.missing) {
			t.Errorf("MissingTranslations(%q) = %q, want %q", tt.language, got, tt.missing)
		}
	}
}

func TestRendererAsset(t *testing.T) {
	th := mustLoad(t, themeFS(map[string]*fstest.MapFile{
		"assets/css/style.css":      file(styleCSS),
		"assets/images/logo@2x.png": {Data: logoPNG},
		"templates/entry.html": file(`<link rel="stylesheet" href="{{asset "css/style.css"}}">` +
			`<img src="{{asset "images/logo@2x.png"}}"> {{asset "css/style.css"}}`),
	}))
	page := func(base, encoded string) string {
		return `<link rel="stylesheet" href="` + encoded + `/assets/css/style.css?v=` + styleVersion + `">` +
			`<img src="` + encoded + `/assets/images/logo@2x.png?v=` + logoVersion + `"> ` +
			base + "/assets/css/style.css?v=" + styleVersion
	}
	tests := []struct{ base, want string }{
		{"", page("", "")},
		{"/blog", page("/blog", "/blog")},
		{"https://example.com", page("https://example.com", "https://example.com")},
		{"https://example.com/blog", page("https://example.com/blog", "https://example.com/blog")},
		// html/template escapes the URLs in their contexts.
		{"/ブログ", page("/ブログ", "/%e3%83%96%e3%83%ad%e3%82%b0")},
	}
	for _, tt := range tests {
		if got := execute(t, mustRenderer(t, th, "ja", tt.base), "entry.html", nil); got != tt.want {
			t.Errorf("Execute(entry.html) with base %q =\n%s\nwant\n%s", tt.base, got, tt.want)
		}
	}
}

func TestRendererEscaping(t *testing.T) {
	th := mustLoad(t, themeFS(map[string]*fstest.MapFile{
		"theme.yaml":           file("schemaVersion: 1\ndefaultLanguage: ja\n"),
		"translations/ja.yaml": file("postedBy: \"{name} が投稿\"\n"),
		"components/card.html": file(`<p class="{{.class}}">{{.text}}</p>`),
		"templates/entry.html": file(`<p title="{{.S}}">{{.S}}</p>{{.H}}<p title="{{.H}}">` +
			`{{template "card" dict "class" .S "text" .H}}` +
			`<p title="{{t "postedBy" "name" .H}}">{{t "postedBy" "name" .S}}</p>`),
	}))
	data := map[string]any{"S": `"<&>' x`, "H": template.HTML(`<b>&amp;</b>`)}
	// Data strings are escaped; template.HTML is not escaped in text and loses its tags in attributes; the functions
	// return strings, which are escaped.
	want := `<p title="&#34;&lt;&amp;&gt;&#39; x">&#34;&lt;&amp;&gt;&#39; x</p><b>&amp;</b><p title="&amp;">` +
		`<p class="&#34;&lt;&amp;&gt;&#39; x"><b>&amp;</b></p>` +
		`<p title="&lt;b&gt;&amp;amp;&lt;/b&gt; が投稿">&#34;&lt;&amp;&gt;&#39; x が投稿</p>`
	if got := execute(t, mustRenderer(t, th, "ja", ""), "entry.html", data); got != want {
		t.Errorf("Execute(entry.html) =\n%s\nwant\n%s", got, want)
	}
}

func TestRendererErrors(t *testing.T) {
	th := mustLoad(t, themeFS(renderFiles()))
	r := mustRenderer(t, th, "en", "")
	for _, name := range []string{
		"posts.html", "nope.html", "templates/entry.html", "entry", "layout", "entry/card", "main", "file.html",
		"blocks/file.html", "",
	} {
		err := r.Execute(&strings.Builder{}, name, entryData("en"))
		if want := fmt.Sprintf("unknown page template %q", name); err == nil || err.Error() != want {
			t.Errorf("Execute(%q) = %v, want %q", name, err, want)
		}
	}

	noTitle := entryData("en")
	delete(noTitle, "Title")
	nilAuthor := entryData("en")
	nilAuthor["Author"] = nil
	nilPage := entryData("en")
	nilPage["Page"] = (*int)(nil)
	tests := []struct {
		name string
		data any
		want string
	}{
		{
			name: "missing key",
			data: noTitle,
			want: `template: templates/entry.html:1:46: executing "main" at <.Title>: map has no entry for key "Title"`,
		},
		{
			name: "nil value in a component",
			data: nilAuthor,
			want: `template: components/entry/card.html:1:45: executing "entry/card" at <t "postedBy" "name" .author>: ` +
				"error calling t: nil value for placeholder {name}",
		},
		{
			name: "nil pointer",
			data: nilPage,
			want: `template: templates/entry.html:1:73: executing "main" at <t "pageOf" "page" .Page "total" .Total>: ` +
				"error calling t: nil value for placeholder {page}",
		},
	}
	for _, tt := range tests {
		var b strings.Builder
		if err := r.Execute(&b, "entry.html", tt.data); err == nil || err.Error() != tt.want {
			t.Errorf("%s: Execute() = %v, want %q", tt.name, err, tt.want)
		}
	}

	// The error of the writer comes as it is.
	if err := r.Execute(stopWriter{}, "list.html", nil); !errors.Is(err, errStopped) || err.Error() != errStopped.Error() {
		t.Errorf("Execute(stopWriter) = %v, want %v", err, errStopped)
	}
}

func TestRendererBlocks(t *testing.T) {
	th := mustLoad(t, themeFS(renderFiles()))
	const (
		ja = `<p><a href="/files/guide.pdf">会社案内</a> (1 / 3 ページ)` +
			`<link rel="stylesheet" href="/assets/css/style.css?v=` + styleVersion + `"></p>` +
			`<article><a href="/files/guide.pdf">会社案内</a> A&amp;B が投稿</article>` + "\n"
		// postedBy of the card falls back to ja.
		en = `<p><a href="/blog/files/guide.pdf">Company guide</a> (Page 1 of 3)` +
			`<link rel="stylesheet" href="/blog/assets/css/style.css?v=` + styleVersion + `"></p>` +
			`<article><a href="/blog/files/guide.pdf">Company guide</a> A&amp;B が投稿</article>` + "\n"
	)
	tests := []struct{ language, base, want string }{{"ja", "", ja}, {"en", "/blog", en}}
	for _, tt := range tests {
		r := mustRenderer(t, th, tt.language, tt.base)
		if got := executeBlock(t, r, content.BlockFile, fileData(tt.language, tt.base)); got != tt.want {
			t.Errorf("ExecuteBlock(file) in %s =\n%s\nwant\n%s", tt.language, got, tt.want)
		}
		if got := r.Language(); got != tt.language {
			t.Errorf("Language() = %q, want %q", got, tt.language)
		}
		if got := r.Base(); got != tt.base {
			t.Errorf("Base() = %q, want %q", got, tt.base)
		}
		if got := r.Settings(); got != th.Settings || got == nil {
			t.Errorf("Settings() = %p, want those of the theme, %p", got, th.Settings)
		}
	}

	r := mustRenderer(t, th, "en", "")
	for bt, want := range map[content.BlockType]bool{
		content.BlockFile: true, content.BlockImage: false, content.BlockHTML: false, content.BlockLegacy: false,
		"entry": false, "file.html": false, "": false,
	} {
		if got := r.HasBlock(bt); got != want {
			t.Errorf("HasBlock(%q) = %v, want %v", bt, got, want)
		}
		if want {
			continue
		}
		var b strings.Builder
		err := r.ExecuteBlock(&b, bt, fileData("en", ""))
		if msg := fmt.Sprintf("no block template for %s blocks", bt); err == nil || err.Error() != msg || b.Len() != 0 {
			t.Errorf("ExecuteBlock(%q) = %v, wrote %q, want error %q", bt, err, b.String(), msg)
		}
	}

	noLabel := fileData("en", "")
	delete(noLabel, "Label")
	const missing = `template: blocks/file.html:1:25: executing "file.html" at <.Label>: map has no entry for key "Label"`
	if err := r.ExecuteBlock(&strings.Builder{}, content.BlockFile, noLabel); err == nil || err.Error() != missing {
		t.Errorf("ExecuteBlock(file) without Label = %v, want %q", err, missing)
	}
	// The error of the writer comes as it is.
	err := r.ExecuteBlock(stopWriter{}, content.BlockFile, fileData("en", ""))
	if !errors.Is(err, errStopped) || err.Error() != errStopped.Error() {
		t.Errorf("ExecuteBlock(stopWriter) = %v, want %v", err, errStopped)
	}
}

func TestRendererFuncs(t *testing.T) {
	th := mustLoad(t, themeFS(renderFiles()))
	r := mustRenderer(t, th, "en", "/blog")
	texts := []struct {
		key  string
		args []any
		want string
	}{
		{"readMore", nil, "Read more"},
		{"postedBy", []any{"name", "A"}, "A が投稿"},
		{"pageOf", []any{"total", 5, "page", 2}, "Page 2 of 5"},
		{"pageOf", []any{"page", 0, "total", ""}, "Page 0 of "},
		{"pageOf", []any{"page", 1.5, "total", []int{}}, "Page 1.5 of []"},
		{"pageOf", []any{"page", "total", "total", 5}, "Page total of 5"},
		// The text is not scanned again: the values are as they are.
		{"postedBy", []any{"name", "{name}"}, "{name} が投稿"},
		{"postedBy", []any{"name", template.HTML("<b>B</b>")}, "<b>B</b> が投稿"},
	}
	for _, tt := range texts {
		if got, err := r.t(tt.key, tt.args...); got != tt.want || err != nil {
			t.Errorf("t(%q, %v) = %q, %v, want %q", tt.key, tt.args, got, err, tt.want)
		}
	}
	errs := []struct {
		key  string
		args []any
		want string
	}{
		{"readMoar", nil, `unknown key "readMoar"`},
		{"pageOf", []any{"page", nil, "total", 1}, "nil value for placeholder {page}"},
		{"pageOf", []any{"total", nil, "page", nil}, "nil value for placeholder {total}"},
		{"readMore", []any{"x", nil}, "nil value for placeholder {x}"},
		{"pageOf", []any{"page", 1}, "missing value for placeholder {total}"},
		{"pageOf", []any{"page", 1, "total"}, "missing value for placeholder {total}"},
		{"pageOf", []any{"page", 1, 2, 3}, "missing value for placeholder {total}"},
		{"pageOf", []any{"total", 2}, "missing value for placeholder {page}"},
	}
	for _, tt := range errs {
		if got, err := r.t(tt.key, tt.args...); got != "" || err == nil || err.Error() != tt.want {
			t.Errorf("t(%q, %v) = %q, %v, want error %q", tt.key, tt.args, got, err, tt.want)
		}
	}
	if got, err := mustRenderer(t, mustLoad(t, themeFS(nil)), "ja", "").t("readMore"); err == nil || got != "" {
		t.Errorf("t(readMore) without translations = %q, %v, want an error", got, err)
	}

	if got, err := r.asset("css/style.css"); got != "/blog/assets/css/style.css?v="+styleVersion || err != nil {
		t.Errorf("asset(css/style.css) = %q, %v", got, err)
	}
	if got, err := r.asset("css/other.css"); got != "" || err == nil || err.Error() != `unknown asset "css/other.css"` {
		t.Errorf("asset(css/other.css) = %q, %v, want an error", got, err)
	}
}

// TestPlaceholders checks how t replaces placeholders in texts.
func TestPlaceholders(t *testing.T) {
	tests := []struct {
		text string
		args []any
		want string
	}{
		{"", nil, ""},
		{"text", []any{"a", 1}, "text"},
		{"{a}", []any{"a", 1}, "1"},
		{"{a}{b}", []any{"b", 2, "a", 1}, "12"},
		{"{n} × {n} = {m}", []any{"n", 3, "m", 9}, "3 × 3 = 9"},
		{"全{total}件中{page}件目", []any{"page", 2, "total", 10}, "全10件中2件目"},
		{"{a}{a}", []any{"a", "{a}"}, "{a}{a}"},
	}
	for _, tt := range tests {
		r := &Renderer{texts: []map[string]string{{"key": tt.text}}}
		if got, err := r.t("key", tt.args...); got != tt.want || err != nil {
			t.Errorf("t(%q, %v) = %q, %v, want %q", tt.text, tt.args, got, err, tt.want)
		}
	}
}

func TestDict(t *testing.T) {
	tests := []struct {
		args []any
		want map[string]any
	}{
		{nil, map[string]any{}},
		{[]any{"a", 1, "b", nil, "c", "x"}, map[string]any{"a": 1, "b": nil, "c": "x"}},
	}
	for _, tt := range tests {
		if got, err := dict(tt.args...); !reflect.DeepEqual(got, tt.want) || err != nil {
			t.Errorf("dict(%v) = %v, %v, want %v", tt.args, got, err, tt.want)
		}
	}
	errs := []struct {
		args []any
		want string
	}{
		{[]any{"a"}, `missing value for key "a"`},
		{[]any{"a", 1, "b"}, `missing value for key "b"`},
		{[]any{1, "a"}, "key 1 is not a string"},
		{[]any{"a", 1, nil, 2}, "key <nil> is not a string"},
	}
	for _, tt := range errs {
		if got, err := dict(tt.args...); got != nil || err == nil || err.Error() != tt.want {
			t.Errorf("dict(%v) = %v, %v, want error %q", tt.args, got, err, tt.want)
		}
	}
}

func TestIsNil(t *testing.T) {
	var err error
	nils := []any{
		nil, err, (*int)(nil), []int(nil), map[string]int(nil), (func())(nil), (chan int)(nil), unsafe.Pointer(nil),
		(*Theme)(nil),
	}
	for _, v := range nils {
		if !isNil(v) {
			t.Errorf("isNil(%#v) = false", v)
		}
	}
	values := []any{
		0, "", false, 0.0, []int{}, map[string]int{}, new(int), struct{}{}, template.HTML(""), errors.New("x"),
		func() {}, make(chan int), unsafe.Pointer(new(int)), [0]int{},
	}
	for _, v := range values {
		if isNil(v) {
			t.Errorf("isNil(%#v) = true", v)
		}
	}
}

func TestLanguageChain(t *testing.T) {
	tests := []struct {
		language string
		want     []string
	}{
		{"ja", []string{"ja"}},
		{"en-US", []string{"en-US", "en"}},
		{"es-419", []string{"es-419", "es"}},
		{"zh-Hant-TW", []string{"zh-Hant-TW", "zh-Hant", "zh"}},
	}
	for _, tt := range tests {
		if got := languageChain(tt.language); !slices.Equal(got, tt.want) {
			t.Errorf("languageChain(%q) = %q, want %q", tt.language, got, tt.want)
		}
	}
}

func TestMissingTranslations(t *testing.T) {
	th := mustLoad(t, themeFS(renderFiles()))
	tests := []struct {
		language string
		want     []string
	}{
		{"ja", nil},
		{"ja-JP", nil},
		{"fr", nil},
		{"en", []string{"postedBy"}},
		{"en-US", []string{"postedBy"}},
		{"zh-Hant-TW", []string{"next"}},
		{"zh-Hant", []string{"next"}},
		{"zh", []string{"next"}},
		{"de", []string{"readMore", "pageOf", "postedBy", "next"}},
	}
	for _, tt := range tests {
		if got := th.MissingTranslations(tt.language); !slices.Equal(got, tt.want) || (got == nil) != (tt.want == nil) {
			t.Errorf("MissingTranslations(%q) = %#v, want %#v", tt.language, got, tt.want)
		}
	}
	if got := mustLoad(t, themeFS(nil)).MissingTranslations("en"); got != nil {
		t.Errorf("MissingTranslations(en) without translations = %q, want nil", got)
	}
}

// TestZeroTheme checks that a Theme that Load has not made has neither templates nor translations nor settings.
func TestZeroTheme(t *testing.T) {
	var th Theme
	if got := th.Template(Page{Kind: KindEntry}); got != "" {
		t.Errorf("Template() = %q, want none", got)
	}
	r := mustRenderer(t, &th, "ja", "")
	if err := r.Execute(&strings.Builder{}, "entry.html", nil); err == nil {
		t.Error("Execute(entry.html) = nil, want an error")
	}
	if got, err := r.t("readMore"); err == nil {
		t.Errorf("t(readMore) = %q, nil, want an error", got)
	}
	if got := th.MissingTranslations("en"); got != nil {
		t.Errorf("MissingTranslations(en) = %q, want nil", got)
	}
	if r.HasBlock(content.BlockParagraph) {
		t.Error("HasBlock(paragraph) = true, want false")
	}
	const msg = "no block template for paragraph blocks"
	if err := r.ExecuteBlock(&strings.Builder{}, content.BlockParagraph, nil); err == nil || err.Error() != msg {
		t.Errorf("ExecuteBlock(paragraph) = %v, want %q", err, msg)
	}
	if got := r.Settings(); got != nil {
		t.Errorf("Settings() = %+v, want nil", got)
	}
	if r.Language() != "ja" || r.Base() != "" {
		t.Errorf("Language(), Base() = %q, %q, want ja and none", r.Language(), r.Base())
	}
}

// TestRendererParallel checks that Renderers can be made and executed in parallel, and that they leave the namespaces
// of the theme unexecuted.
func TestRendererParallel(t *testing.T) {
	th := mustLoad(t, themeFS(renderFiles()))
	languages := []string{"ja", "en", "zh-Hant-TW"}
	names := []string{"entry.html", "list.html", "author.html"}
	want := make(map[string]string)
	for _, lang := range languages {
		r := mustRenderer(t, th, lang, "/blog")
		for _, name := range names {
			want[lang+" "+name] = execute(t, r, name, entryData(lang))
		}
		want[lang+" file"] = executeBlock(t, r, content.BlockFile, fileData(lang, "/blog"))
	}
	if got := want["zh-Hant-TW author.html"]; got != "<p>A&amp;B zh</p>" {
		t.Errorf("Execute(author.html) in zh-Hant-TW = %q", got)
	}
	if got := want["zh-Hant-TW file"]; !strings.Contains(got, "(1/3 zh-Hant)") {
		t.Errorf("ExecuteBlock(file) in zh-Hant-TW = %q", got)
	}
	var wg sync.WaitGroup
	for _, lang := range languages {
		wg.Go(func() {
			r, err := th.Renderer(lang, "/blog")
			if err != nil {
				t.Error(err)
				return
			}
			var pages sync.WaitGroup
			for range 4 {
				for _, name := range names {
					pages.Go(func() {
						var b strings.Builder
						if err := r.Execute(&b, name, entryData(lang)); err != nil {
							t.Error(err)
						} else if got := b.String(); got != want[lang+" "+name] {
							t.Errorf("Execute(%s) in %s = %q, want %q", name, lang, got, want[lang+" "+name])
						}
					})
				}
				pages.Go(func() {
					var b strings.Builder
					if err := r.ExecuteBlock(&b, content.BlockFile, fileData(lang, "/blog")); err != nil {
						t.Error(err)
					} else if got := b.String(); got != want[lang+" file"] {
						t.Errorf("ExecuteBlock(file) in %s = %q, want %q", lang, got, want[lang+" file"])
					}
				})
			}
			pages.Wait()
		})
	}
	wg.Wait()
	for name, ns := range th.namespaces {
		if _, err := ns.Clone(); err != nil {
			t.Errorf("namespaces[%s].Clone() = %v", name, err)
		}
	}
	for bt, ns := range th.blockNamespaces {
		if _, err := ns.Clone(); err != nil {
			t.Errorf("blockNamespaces[%s].Clone() = %v", bt, err)
		}
	}
}
