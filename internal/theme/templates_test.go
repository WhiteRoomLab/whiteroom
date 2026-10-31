package theme

import (
	"errors"
	"html/template"
	"maps"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// testDict is dict for executing namespaces in tests.
func testDict(args ...any) (map[string]any, error) {
	m := make(map[string]any)
	for i := 0; i+1 < len(args); i += 2 {
		m[args[i].(string)] = args[i+1]
	}
	return m, nil
}

func TestLoadNamespaces(t *testing.T) {
	th := mustLoad(t, themeFS(map[string]*fstest.MapFile{
		"components/layout.html":     file(`<main>{{block "main" .}}<p>default</p>{{end}}</main>{{block "aside" .}}{{end}}`),
		"components/entry/card.html": file(`<article>{{.title}}</article>`),
		"components/parts.html":      file(`{{define "parts/meta"}}<p>meta</p>{{end}}`),
		"templates/entry.html": file(`{{template "layout" .}}` +
			`{{define "main"}}{{template "entry/card" dict "title" "T"}}{{template "parts/meta"}}{{end}}`),
		"templates/list.html": file(`{{template "layout" .}}`),
		// An empty define replaces a block too.
		"templates/posts.html":  file(`{{template "layout" .}}{{define "aside"}}<aside></aside>{{end}}{{define "main"}}{{end}}`),
		"templates/author.html": file(`<h1>{{.Title}}</h1>`),
	}))
	layout := `<main>{{template "main" .}}</main>{{template "aside" .}}`
	components := map[string]string{
		"layout":     layout,
		"main":       "<p>default</p>",
		"aside":      "",
		"entry/card": "<article>{{.title}}</article>",
		"parts":      "",
		"parts/meta": "<p>meta</p>",
	}
	with := func(changes map[string]string) map[string]string {
		m := maps.Clone(components)
		maps.Copy(m, changes)
		return m
	}
	want := map[string]map[string]string{
		"entry.html": with(map[string]string{
			"entry.html": `{{template "layout" .}}`,
			"main":       `{{template "entry/card" dict "title" "T"}}{{template "parts/meta"}}`,
		}),
		"list.html":   with(map[string]string{"list.html": `{{template "layout" .}}`}),
		"posts.html":  with(map[string]string{"posts.html": `{{template "layout" .}}`, "main": "", "aside": "<aside></aside>"}),
		"author.html": with(map[string]string{"author.html": "<h1>{{.Title}}</h1>"}),
	}
	if got := namespaceTexts(th); !reflect.DeepEqual(got, want) {
		t.Errorf("namespaces =\n%q\nwant\n%q", got, want)
	}
	for name, ns := range th.namespaces {
		if ns.Name() != name || ns.Tree == nil {
			t.Errorf("namespaces[%s] = %q with tree %v, want the page template", name, ns.Name(), ns.Tree)
		}
	}

	// The namespaces execute once cloned with the functions bound, and stay unexecuted.
	outputs := map[string]string{
		"entry.html": "<main><article>T</article><p>meta</p></main>",
		"list.html":  "<main><p>default</p></main>",
		"posts.html": "<main></main><aside></aside>",
	}
	for range 2 {
		for name, want := range outputs {
			c, err := th.namespaces[name].Clone()
			if err != nil {
				t.Fatalf("Clone(%s) = %v", name, err)
			}
			var b strings.Builder
			if err := c.Funcs(template.FuncMap{"dict": testDict}).Execute(&b, nil); err != nil {
				t.Fatalf("Execute(%s) = %v", name, err)
			}
			if got := b.String(); got != want {
				t.Errorf("Execute(%s) = %q, want %q", name, got, want)
			}
		}
	}
	c, err := th.namespaces["author.html"].Clone()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Execute(&strings.Builder{}, map[string]any{}); err == nil || !strings.Contains(err.Error(), `map has no entry for key "Title"`) {
		t.Errorf("Execute(author.html) without Title = %v, want an error for the missing key", err)
	}
}

func TestLoadBlockNamespaces(t *testing.T) {
	th := mustLoad(t, themeFS(map[string]*fstest.MapFile{
		"components/figure.html":     file(`<figure>{{block "media" .}}{{end}}{{block "caption" .}}{{end}}</figure>`),
		"components/entry/card.html": file(`<article>{{.title}}</article>`),
		"blocks/image.html": file(`{{template "figure" .}}{{define "media"}}<img src="{{.src}}">{{end}}` +
			`{{define "caption"}}{{template "image/credit" dict "by" "A"}}{{end}}` +
			`{{define "image/credit"}}<figcaption>{{.by}}</figcaption>{{end}}`),
		"blocks/list.html":     file(`<ul>{{template "entry/card" dict "title" "T"}}</ul>`),
		"templates/entry.html": file(`{{template "figure" .}}{{define "media"}}<p>entry</p>{{end}}`),
	}))
	components := map[string]string{
		"figure":     `<figure>{{template "media" .}}{{template "caption" .}}</figure>`,
		"media":      "",
		"caption":    "",
		"entry/card": "<article>{{.title}}</article>",
	}
	with := func(changes map[string]string) map[string]string {
		m := maps.Clone(components)
		maps.Copy(m, changes)
		return m
	}
	// The defines of a block template are in its namespace only, and the page template list.html is not a block
	// template.
	want := map[string]map[string]string{
		"entry.html": with(map[string]string{"entry.html": `{{template "figure" .}}`, "media": "<p>entry</p>"}),
		"list.html":  with(map[string]string{"list.html": "<p>list</p>\n"}),
		"blocks/image.html": with(map[string]string{
			"image.html":   `{{template "figure" .}}`,
			"media":        `<img src="{{.src}}">`,
			"caption":      `{{template "image/credit" dict "by" "A"}}`,
			"image/credit": "<figcaption>{{.by}}</figcaption>",
		}),
		"blocks/list.html": with(map[string]string{"list.html": `<ul>{{template "entry/card" dict "title" "T"}}</ul>`}),
	}
	if got := namespaceTexts(th); !reflect.DeepEqual(got, want) {
		t.Errorf("namespaces =\n%q\nwant\n%q", got, want)
	}
	for bt, ns := range th.blockNamespaces {
		if ns.Name() != string(bt)+".html" || ns.Tree == nil {
			t.Errorf("blockNamespaces[%s] = %q with tree %v, want the block template", bt, ns.Name(), ns.Tree)
		}
	}

	// The namespaces execute once cloned with the functions bound, and stay unexecuted.
	outputs := map[content.BlockType]string{
		content.BlockImage: `<figure><img src="a.png"><figcaption>A</figcaption></figure>`,
		content.BlockList:  "<ul><article>T</article></ul>",
	}
	for range 2 {
		for bt, want := range outputs {
			c, err := th.blockNamespaces[bt].Clone()
			if err != nil {
				t.Fatalf("Clone(%s) = %v", bt, err)
			}
			var b strings.Builder
			if err := c.Funcs(template.FuncMap{"dict": testDict}).Execute(&b, map[string]any{"src": "a.png"}); err != nil {
				t.Fatalf("Execute(%s) = %v", bt, err)
			}
			if got := b.String(); got != want {
				t.Errorf("Execute(%s) = %q, want %q", bt, got, want)
			}
		}
	}
	c, err := th.blockNamespaces[content.BlockImage].Clone()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Execute(&strings.Builder{}, map[string]any{}); err == nil || !strings.Contains(err.Error(), `map has no entry for key "src"`) {
		t.Errorf("Execute(image) without src = %v, want an error for the missing key", err)
	}
}

func TestLoadTemplateSyntax(t *testing.T) {
	runLoadTests(t, []loadTest{
		{
			name:    "unclosed action",
			changes: map[string]*fstest.MapFile{"templates/entry.html": file("<p>\n{{.Title\n")},
			want:    []string{"templates/entry.html:3: unclosed action started at templates/entry.html:2"},
		},
		{
			name: "several files",
			changes: map[string]*fstest.MapFile{
				"templates/entry.html":    file("<p>{{nope .}}</p>"),
				"templates/list.html":     file("{{range .}}{{end}}{{break}}"),
				"components/x.html":       file("\n\n{{end}}"),
				"components/entry/y.html": file("{{define \"a\"}}1{{end}}\n{{define \"a\"}}2{{end}}\n"),
			},
			want: []string{
				`components/entry/y.html:2: duplicate define "a"`,
				"components/x.html:3: unexpected {{end}}",
				`templates/entry.html:1: function "nope" not defined`,
				"templates/list.html:1: {{break}} outside {{range}}",
			},
		},
		{
			name: "invalid UTF-8",
			changes: map[string]*fstest.MapFile{
				"templates/entry.html": file("<p>ok</p>\n<p>\xff</p>"),
				"components/x.html":    file("日本\xe6\x97"),
			},
			want: []string{"components/x.html:1:3: invalid UTF-8", "templates/entry.html:2:4: invalid UTF-8"},
		},
	})
}

func TestLoadDefines(t *testing.T) {
	const use = `: use ASCII letters, digits, "-" and "_", and "/" between parts`
	runLoadTests(t, []loadTest{
		{
			name: "duplicates in components",
			changes: map[string]*fstest.MapFile{
				"components/a.html":   file(`{{define "main"}}a{{end}}`),
				"components/b.html":   file("<p>b</p>\n{{block \"main\" .}}b{{end}}"),
				"components/c/d.html": file(`{{- define "main" -}} d {{end}}`),
			},
			want: []string{
				`components/a.html:1:10: define "main" is also in components/b.html, components/c/d.html`,
				`components/b.html:2:9: define "main" is also in components/a.html, components/c/d.html`,
				`components/c/d.html:1:12: define "main" is also in components/a.html, components/b.html`,
			},
		},
		{
			name: "names of components",
			changes: map[string]*fstest.MapFile{
				"components/header.html":     file("<header>{{call .F}}</header>"),
				"components/entry/card.html": file("card"),
				"components/layout.html":     file(`{{define "header"}}h{{end}}{{define "entry/card"}}c{{end}}{{define "entry"}}e{{end}}`),
				// The component is called, not the define.
				"templates/entry.html": file("{{template \"header\" .}}\n{{define \"header\"}}{{call .G}}{{end}}"),
			},
			want: []string{
				"components/header.html:1:11: call is not allowed; template data holds no functions",
				`components/layout.html:1:10: define "header" has the name of components/header.html`,
				`components/layout.html:1:37: define "entry/card" has the name of components/entry/card.html`,
				`templates/entry.html:2:10: define "header" has the name of components/header.html`,
			},
		},
		{
			name: "invalid names",
			changes: map[string]*fstest.MapFile{
				"components/a.html": file("{{define \"a b\"}}{{end}}\n{{define \"\"}}x{{end}}\n{{define \"a//b\"}}{{end}}\n" +
					"{{define \"/a\"}}{{end}}\n{{define \"a/\"}}{{end}}\n{{define \"a.html\"}}{{end}}\n" +
					"{{define \"ok/x-y_Z9\"}}{{end}}\n{{define `raw name`}}{{end}}\n"),
				"templates/entry.html": file("{{block \"main page\" .}}{{end}}"),
			},
			want: []string{
				`components/a.html:1:10: invalid define name "a b"` + use,
				`components/a.html:2:10: invalid define name ""` + use,
				`components/a.html:3:10: invalid define name "a//b"` + use,
				`components/a.html:4:10: invalid define name "/a"` + use,
				`components/a.html:5:10: invalid define name "a/"` + use,
				`components/a.html:6:10: invalid define name "a.html"` + use,
				`components/a.html:8:10: invalid define name "raw name"` + use,
				`templates/entry.html:1:9: invalid define name "main page"` + use,
			},
		},
		{
			name: "pages",
			changes: map[string]*fstest.MapFile{
				"components/layout.html": file(`{{block "main" .}}{{end}}`),
				"templates/entry.html":   file(`{{define "main"}}entry{{end}}{{define "entry/meta"}}{{end}}`),
				"templates/list.html":    file(`{{define "main"}}list{{end}}{{define "entry/meta"}}{{end}}`),
			},
		},
	})
}

func TestLoadTemplateCalls(t *testing.T) {
	runLoadTests(t, []loadTest{
		{
			name: "in a page",
			changes: map[string]*fstest.MapFile{
				"templates/entry.html": file("<p>\n  {{template \"heaer\" .}}\n</p>{{if .}}{{else}}{{template \"footr\"}}{{end}}"),
			},
			want: []string{
				`templates/entry.html:2:14: unknown template "heaer"`,
				`templates/entry.html:3:32: unknown template "footr"`,
			},
		},
		{
			name: "in a component reached from one page",
			changes: map[string]*fstest.MapFile{
				"components/header.html": file(`<header>{{template "logo" .}}</header>`),
				// Templates that no page template reaches are not checked.
				"components/unused.html": file(`{{template "nothing"}}{{call .F}}{{t .Key}}`),
				"templates/entry.html":   file(`{{template "header" .}}`),
			},
			want: []string{`components/header.html:1:20: unknown template "logo" when used by templates/entry.html`},
		},
		{
			name: "defined by some pages",
			changes: map[string]*fstest.MapFile{
				"components/layout.html": file("<body>\n{{template \"main\" .}}\n{{template \"aside\" .}}\n</body>"),
				"templates/entry.html":   file(`{{template "layout" .}}{{define "main"}}{{end}}{{define "aside"}}{{end}}`),
				"templates/list.html":    file(`{{template "layout" .}}{{define "main"}}{{end}}`),
				"templates/posts.html":   file(`{{template "layout" .}}`),
				"templates/404.html":     file(`<p>not found</p>`),
			},
			want: []string{
				`components/layout.html:2:12: unknown template "main" when used by templates/posts.html`,
				`components/layout.html:3:12: unknown template "aside" when used by templates/list.html, templates/posts.html`,
			},
		},
		{
			name: "defines of other pages",
			changes: map[string]*fstest.MapFile{
				"templates/entry.html": file(`{{define "x"}}x{{end}}`),
				"templates/list.html":  file(`{{template "x"}}{{template "entry.html"}}{{if .}}{{template "list.html"}}{{end}}`),
			},
			want: []string{
				`templates/list.html:1:12: unknown template "x"`,
				`templates/list.html:1:28: unknown template "entry.html"`,
			},
		},
		{
			name: "components that do not parse",
			changes: map[string]*fstest.MapFile{
				"components/layout.html": file("{{block \"main\" .}}{{end}}{{define `nav`}}{{end}}{{.X"),
				"components/broken.html": file("\xff"),
				"templates/entry.html": file(`{{template "layout" .}}{{template "main" .}}{{template "nav"}}` +
					`{{template "broken"}}{{template "other"}}`),
			},
			want: []string{
				"components/broken.html:1:1: invalid UTF-8",
				"components/layout.html:1: unclosed action",
				`templates/entry.html:1:95: unknown template "other"`,
			},
		},
		{
			name: "components that do not load",
			changes: map[string]*fstest.MapFile{
				"components/header.html": symlink("../shared/header.html"),
				"templates/entry.html":   file(`{{template "header" .}}{{template "other"}}`),
			},
			want: []string{"components/header.html: symbolic links are not allowed"},
		},
		{
			name: "components with an invalid name",
			changes: map[string]*fstest.MapFile{
				"components/my header.html": file("header"),
				"templates/entry.html":      file(`{{template "other"}}`),
			},
			want: []string{`components/my header.html: invalid name "my header": use ASCII letters, digits, "-" and "_"`},
		},
		{
			name: "components is a file",
			changes: map[string]*fstest.MapFile{
				"components":           file("header.html\n"),
				"templates/entry.html": file(`{{template "header" .}}`),
			},
			want: []string{"components: must be a directory"},
		},
	})
}

const (
	funcsSettings    = "schemaVersion: 1\ndefaultLanguage: ja\n"
	funcsTranslation = "readMore: 続きを読む\npageOf: \"{page} / {total} ページ\"\n"
)

// funcsTheme returns the changes to themeFS for a theme with translations, an asset and a component, and entry.html
// as given.
func funcsTheme(entry string) map[string]*fstest.MapFile {
	return map[string]*fstest.MapFile{
		"theme.yaml":           file(funcsSettings),
		"translations/ja.yaml": file(funcsTranslation),
		"assets/css/style.css": file("body {}"),
		"components/card.html": file("<article>{{.title}}</article>"),
		"templates/entry.html": file(entry),
	}
}

func TestLoadFuncs(t *testing.T) {
	mustLoad(t, themeFS(funcsTheme(`{{t "readMore"}}
{{t "pageOf" "page" 1 "total" .N}} {{t "pageOf" "total" 2 "page" 1}}
{{t "readMore" | printf "%s!"}} {{print (t "readMore")}} {{(t "readMore") | len}}
{{$x := t "readMore"}}{{$x}} {{with t "readMore"}}{{.}}{{end}} {{if t "readMore"}}x{{end}} {{t `+"`readMore`"+`}}
{{asset "css/style.css"}} <link rel="stylesheet" href="{{asset "css/style.css"}}">
{{template "card" dict "title" .Title "n" 1}} {{template "card" (dict "title" "x")}}
{{with dict}}{{end}} {{range $k, $v := dict "a" 1}}{{$k}}{{end}} {{len (dict "a" 1 "b" (dict "c" 2))}}
`)))

	const (
		key       = `the key of t must be a string constant, such as "readMore"`
		names     = `the names of placeholders must be string constants, such as "page"`
		path      = `the path of asset must be a string constant, such as "css/style.css"`
		dictKey   = `the keys of dict must be string constants, such as "title"`
		call      = "call is not allowed; template data holds no functions"
		unknownTo = `; the default translation of "pageOf" has {page}, {total}`
	)
	runLoadTests(t, []loadTest{
		{
			name: "t",
			changes: funcsTheme(`{{t}}
{{t .Key}}
{{"readMore" | t}}
{{print t "readMore"}}
{{t.X}}
{{t "readMoar"}}
{{t "pageOf" "page" 1}}
{{t "pageOf"}}
{{t "pageOf" "page" 1 "total" 2 "pages" 3}}
{{t "pageOf" "page" 1 "page" 2 "total" 3}}
{{t "pageOf" "page" 1 "total"}}
{{t "pageOf" .Name 1 "total" 2}}
{{t "readMore" "x" 1}}
<p title="{{t (print "read" "More")}}">x</p>
`),
			want: []string{
				`templates/entry.html:1:3: t needs a key, as in {{t "key"}}`,
				"templates/entry.html:2:5: " + key,
				`templates/entry.html:3:16: t cannot be piped into; write {{t "key"}}`,
				`templates/entry.html:4:9: t as an argument needs parentheses, as in (t "key")`,
				`templates/entry.html:5:3: t as an argument needs parentheses, as in (t "key")`,
				`templates/entry.html:6:5: unknown key "readMoar"; keys must be in the default translation, translations/ja.yaml`,
				"templates/entry.html:7:5: missing value for placeholder {total}",
				"templates/entry.html:8:5: missing values for placeholders {page}, {total}",
				"templates/entry.html:9:33: unknown placeholder {pages}" + unknownTo,
				"templates/entry.html:10:23: duplicate placeholder {page}",
				"templates/entry.html:11:23: missing value for placeholder {total}",
				"templates/entry.html:12:5: missing value for placeholder {page}",
				"templates/entry.html:12:14: " + names,
				`templates/entry.html:13:16: unknown placeholder {x}; the default translation of "readMore" has no placeholders`,
				"templates/entry.html:14:16: " + key,
			},
		},
		{
			name: "call",
			changes: funcsTheme(`{{call .F}}
{{.X | call}}
{{print (call .F 1)}}
{{with .X}}{{call .F}}{{else}}{{call .G}}{{end}}
{{if .X}}{{else if call .F}}{{end}}
{{range .}}{{call .F}}{{end}}
{{template "card" call .F}}
`),
			want: []string{
				"templates/entry.html:1:3: " + call,
				"templates/entry.html:2:8: " + call,
				"templates/entry.html:3:10: " + call,
				"templates/entry.html:4:14: " + call,
				"templates/entry.html:4:33: " + call,
				"templates/entry.html:5:20: " + call,
				"templates/entry.html:6:14: " + call,
				"templates/entry.html:7:19: " + call,
			},
		},
		{
			name: "asset",
			changes: funcsTheme(`{{asset}}
{{asset .Path}}
{{asset "css/style.css" "x"}}
{{asset "css/missing.css"}}
{{asset "/css/style.css"}}
{{"css/style.css" | asset}}
`),
			want: []string{
				`templates/entry.html:1:3: asset needs a path, as in {{asset "css/style.css"}}`,
				"templates/entry.html:2:9: " + path,
				"templates/entry.html:3:25: asset takes one path",
				`templates/entry.html:4:9: unknown asset "css/missing.css"; paths are relative to assets/`,
				`templates/entry.html:5:9: unknown asset "/css/style.css"; paths are relative to assets/`,
				`templates/entry.html:6:21: asset cannot be piped into; write {{asset "css/style.css"}}`,
			},
		},
		{
			name: "dict",
			changes: funcsTheme(`{{dict "a"}}
{{dict .K 1}}
{{dict "a" 1 "a" 2}}
{{template "card" print dict}}
{{template "card" (dict "a" 1 .K)}}
`),
			want: []string{
				`templates/entry.html:1:8: missing value for key "a"`,
				"templates/entry.html:2:8: " + dictKey,
				`templates/entry.html:3:14: duplicate key "a" in dict`,
				`templates/entry.html:4:25: dict as an argument needs parentheses, as in (dict "key" value)`,
				"templates/entry.html:5:31: " + dictKey,
			},
		},
		{
			name: "in components and in the defines of pages",
			changes: map[string]*fstest.MapFile{
				"components/layout.html": file("<main>{{block \"main\" .}}{{call .F}}{{end}}</main>"),
				"templates/entry.html":   file("{{template \"layout\" .}}\n{{define \"main\"}}\n  {{asset .P}}\n{{end}}"),
				"templates/list.html":    file(`{{template "layout" .}}`),
			},
			want: []string{
				"components/layout.html:1:27: call is not allowed; template data holds no functions",
				"templates/entry.html:3:11: " + path,
			},
		},
		{
			name:    "columns in characters",
			changes: map[string]*fstest.MapFile{"templates/entry.html": file("<p>日本語の{{t .Key}}</p>")},
			want:    []string{"templates/entry.html:1:12: " + key},
		},
	})
}

// TestLoadFuncsWithout checks the functions in themes without what they need, or where that has not loaded.
func TestLoadFuncsWithout(t *testing.T) {
	runLoadTests(t, []loadTest{
		{
			name:    "translations",
			changes: map[string]*fstest.MapFile{"templates/entry.html": file(`{{t "readMore"}}`)},
			want: []string{
				`templates/entry.html:1:5: unknown key "readMore"; the theme has no translation files, translations/<language>.yaml`,
			},
		},
		{
			name: "a default translation that does not load",
			changes: map[string]*fstest.MapFile{
				"theme.yaml":           file(funcsSettings),
				"translations/ja.yaml": file("readMore: [x]\n"),
				"templates/entry.html": file(`{{t "readMoar"}}{{t .Key}}`),
			},
			want: []string{
				"translations/ja.yaml:1:1: /readMore: expected string, got array",
				"templates/entry.html:1:21: the key of t must be a string constant, such as \"readMore\"",
			},
		},
		{
			name: "settings that do not load",
			changes: map[string]*fstest.MapFile{
				"theme.yaml":           file("schemaVersion: 2\n"),
				"templates/entry.html": file(`{{t "readMore"}}`),
			},
			want: []string{"theme.yaml:1:1: /schemaVersion: unsupported schemaVersion 2 (use 1)"},
		},
		{
			name: "translations that are not read",
			changes: map[string]*fstest.MapFile{
				"theme.yaml":           file(funcsSettings),
				"translations":         symlink("i18n"),
				"templates/entry.html": file(`{{t "readMore"}}`),
			},
			want: []string{"translations: symbolic links are not allowed"},
		},
		{
			name: "translations that are not read, without a default language",
			changes: map[string]*fstest.MapFile{
				"translations":         symlink("i18n"),
				"templates/entry.html": file(`{{t "readMore"}}`),
			},
			want: []string{"translations: symbolic links are not allowed"},
		},
		{
			name:    "assets",
			changes: map[string]*fstest.MapFile{"templates/entry.html": file(`{{asset "css/style.css"}}`)},
			want:    []string{`templates/entry.html:1:9: unknown asset "css/style.css"; paths are relative to assets/`},
		},
		{
			name: "assets that do not load",
			changes: map[string]*fstest.MapFile{
				"assets/css/a b.css":   file("x"),
				"templates/entry.html": file(`{{asset "css/a b.css"}}{{asset "other.css"}}`),
			},
			want: []string{`assets/css/a b.css: invalid name "a b.css": use ASCII letters, digits, "-", "_", ".", "~", "@" and "+"`},
		},
		{
			name: "assets is a file",
			changes: map[string]*fstest.MapFile{
				"assets":               file("css/style.css\n"),
				"templates/entry.html": file(`{{asset "css/style.css"}}`),
			},
			want: []string{"assets: must be a directory"},
		},
	})
}

func TestLoadEscaping(t *testing.T) {
	runLoadTests(t, []loadTest{
		{
			name: "missing data",
			changes: map[string]*fstest.MapFile{
				"templates/entry.html": file(`<h1>{{.Title}}</h1>{{index .Tags 0}}{{range .Items}}{{.Name}}{{end}}{{len .X}}`),
				"templates/list.html":  file(`{{range 3}}{{end}}{{with .X}}{{.Y.Z}}{{end}}<a href="{{.URL}}">{{.Text}}</a>`),
			},
		},
		{
			name:    "end in a non-text context",
			changes: map[string]*fstest.MapFile{"templates/entry.html": file("<p>\n<a href=\"{{.URL}}\n</p>\n")},
			want: []string{
				"templates/entry.html: ends in a non-text context: " +
					"{stateURL delimDoubleQuote urlPartPreQuery jsCtxRegexp [] attrURL elementNone <nil>}",
			},
		},
		{
			name: "a component of two pages",
			changes: map[string]*fstest.MapFile{
				"components/nav.html":  file("<nav>\n  <a href=/a=b>A</a>\n</nav>"),
				"templates/entry.html": file(`{{template "nav" .}}`),
				"templates/list.html":  file(`<div>{{template "nav" .}}</div>`),
			},
			want: []string{`components/nav.html:2:13: "=" in unquoted attr: "/a=b"`},
		},
		{
			name: "attribute name",
			changes: map[string]*fstest.MapFile{
				"templates/entry.html": file("{{if .A}}\n  <img src=\"a.png\" alt\"x\">\n{{else}}<br>{{end}}"),
			},
			want: []string{`templates/entry.html:2:23: "\"" in attribute name: " alt\"x\">\n"`},
		},
		{
			name: "else branch of a define",
			changes: map[string]*fstest.MapFile{
				"components/layout.html": file(`<body>{{block "main" .}}{{end}}</body>`),
				"templates/entry.html": file("{{template \"layout\" .}}\n{{define \"main\"}}\n" +
					"{{with .X}}<p>{{.}}</p>{{else}}\n  <p class=a\"b>\n{{end}}\n{{end}}"),
			},
			want: []string{`templates/entry.html:4:13: "\"" in unquoted attr: "a\"b"`},
		},
		{
			name: "body of a range",
			changes: map[string]*fstest.MapFile{
				"templates/entry.html": file("<ul>\n{{range .}}\n<li><a href=a\"b>x</a></li>\n{{end}}\n</ul>"),
			},
			want: []string{`templates/entry.html:3:14: "\"" in unquoted attr: "a\"b"`},
		},
		{
			name: "component in an attribute",
			changes: map[string]*fstest.MapFile{
				"components/v.html":    file("a=b"),
				"templates/entry.html": file("{{template \"v\" .}}\n<p class={{template \"v\" .}}>x</p>"),
			},
			want: []string{`components/v.html:1:2: "=" in unquoted attr: "a=b"`},
		},
		{
			name:    "second pass over the body of a range",
			changes: map[string]*fstest.MapFile{"templates/entry.html": file("<p>{{range .}}{{.}}<b c=d{{end}}</p>")},
			want:    []string{`templates/entry.html:1:12: on range loop re-entry: "<" in unquoted attr: "<b"`},
		},
		{
			// The error is at the call that closes the cycle.
			name: "components that call each other",
			changes: map[string]*fstest.MapFile{
				"components/a.html":    file(`<div>{{template "b" .}}<a b'c></div>`),
				"components/b.html":    file(`<p>{{if .}}{{template "a" .}}{{end}}</p>`),
				"templates/entry.html": file(`{{template "a" .}}`),
			},
			want: []string{`components/b.html:1:23: "'" in attribute name: " b'c></div>"`},
		},
		{
			name: "recursive component",
			changes: map[string]*fstest.MapFile{
				"components/tree.html": file(`<ul>{{range .}}<li>{{template "tree" .Children}}<a b'c></li>{{end}}</ul>`),
				"templates/entry.html": file(`{{template "tree" .}}`),
			},
			want: []string{`components/tree.html:1:53: "'" in attribute name: " b'c></li>"`},
		},
		{
			name: "scripts",
			changes: map[string]*fstest.MapFile{
				"templates/entry.html":  file("<script>\n{{if .C}}var x = 1{{end}}\n/-{{.N}}/i.test(x)\n</script>"),
				"templates/list.html":   file("<script>\nalert(\"\\{{.X}}\")\n</script>"),
				"templates/posts.html":  file("<script>\nvar p = /foo[{{.X}}]/\n</script>"),
				"templates/term.html":   file("<script>\nvar p = /[あ{{.X}}]/\n</script>"),
				"templates/author.html": file("<p>日本語</p><a href=日本=語>"),
			},
			want: []string{
				`templates/author.html:1:21: "=" in unquoted attr: "日本=語"`,
				`templates/entry.html:3:1: '/' could start a division or regexp: "/-"`,
				`templates/list.html:2:8: unfinished escape sequence in JS string: "\\"`,
				`templates/posts.html:2:13: unfinished JS regexp charset: "foo["`,
				`templates/term.html:2:11: unfinished JS regexp charset: "[あ"`,
			},
		},
		{
			name: "nodes",
			changes: map[string]*fstest.MapFile{
				"components/link.html": file(`<a href="{{if .Q}}/search?q={{else}}/page/{{end}}{{.X}}">x</a>`),
				"templates/entry.html": file(`{{template "link" .}}`),
				"templates/list.html":  file("<p>\n{{if .C}}<a href=\"{{end}}x\">\n</p>"),
				"templates/posts.html": file(`<div class={{.C | html}}>x</div>`),
			},
			want: []string{
				`components/link.html:1:52: {{.X}} appears in an ambiguous context within a URL`,
				`templates/list.html:2:6: {{if}} branches end in different contexts: ` +
					`{stateURL delimDoubleQuote urlPartNone jsCtxRegexp [] attrURL elementNone <nil>}, ` +
					`{stateText delimNone urlPartNone jsCtxRegexp [] attrNone elementNone <nil>}`,
				`templates/posts.html:1:14: predefined escaper "html" disallowed in template`,
			},
		},
		{
			name: "unknown template",
			changes: map[string]*fstest.MapFile{
				"templates/entry.html": file(`{{template "x"}}<a href=a=b>`),
			},
			want: []string{`templates/entry.html:1:12: unknown template "x"`},
		},
	})
}

// TestLoadBlockTemplates checks that block templates are checked as page templates are.
func TestLoadBlockTemplates(t *testing.T) {
	const use = `: use ASCII letters, digits, "-" and "_", and "/" between parts`
	runLoadTests(t, []loadTest{
		{
			name: "syntax",
			changes: map[string]*fstest.MapFile{
				"blocks/code.html":  file(`<pre>{{nope .}}</pre>`),
				"blocks/file.html":  file("<a>\xff</a>"),
				"blocks/image.html": file("<figure>\n{{.Src\n"),
				"blocks/quote.html": file("{{define \"a\"}}1{{end}}\n{{define \"a\"}}2{{end}}"),
			},
			want: []string{
				`blocks/code.html:1: function "nope" not defined`,
				"blocks/file.html:1:4: invalid UTF-8",
				"blocks/image.html:3: unclosed action started at blocks/image.html:2",
				`blocks/quote.html:2: duplicate define "a"`,
			},
		},
		{
			name:    "unknown template",
			changes: map[string]*fstest.MapFile{"blocks/image.html": file(`{{template "x"}}<figure></figure>`)},
			want:    []string{`blocks/image.html:1:12: unknown template "x"`},
		},
		{
			name: "in a component reached from a page and a block template",
			changes: map[string]*fstest.MapFile{
				"components/caption.html": file(`<figcaption>{{template "credit" .}}</figcaption>`),
				"templates/entry.html":    file(`{{template "caption" .}}`),
				"blocks/image.html":       file(`{{template "caption" .}}`),
			},
			want: []string{
				`components/caption.html:1:24: unknown template "credit" when used by templates/entry.html, blocks/image.html`,
			},
		},
		{
			name: "in a component reached from a block template",
			changes: map[string]*fstest.MapFile{
				"components/caption.html": file(`<figcaption>{{template "credit" .}}</figcaption>`),
				"blocks/image.html":       file(`{{template "caption" .}}`),
				"blocks/table.html":       file(`<table></table>`),
			},
			want: []string{`components/caption.html:1:24: unknown template "credit" when used by blocks/image.html`},
		},
		{
			name: "defined by some templates",
			changes: map[string]*fstest.MapFile{
				"components/figure.html": file("<figure>\n{{template \"media\" .}}\n{{template \"caption\" .}}\n</figure>"),
				"templates/entry.html":   file(`{{template "figure" .}}{{define "media"}}{{end}}{{define "caption"}}{{end}}`),
				"templates/list.html":    file(`{{template "figure" .}}{{define "media"}}{{end}}`),
				"blocks/embed.html":      file(`{{template "figure" .}}`),
				"blocks/image.html":      file(`{{template "figure" .}}{{define "media"}}<img>{{end}}`),
				"blocks/table.html":      file(`{{template "figure" .}}{{define "media"}}{{end}}{{define "caption"}}{{end}}`),
			},
			want: []string{
				`components/figure.html:2:12: unknown template "media" when used by blocks/embed.html`,
				`components/figure.html:3:12: unknown template "caption" when used by templates/list.html, ` +
					`blocks/embed.html, blocks/image.html`,
			},
		},
		{
			name: "page templates and block templates",
			changes: map[string]*fstest.MapFile{
				"templates/entry.html": file(`{{template "image.html"}}{{template "image/caption"}}`),
				"blocks/image.html":    file(`{{template "entry.html"}}{{template "list.html"}}{{define "image/caption"}}{{end}}`),
				"blocks/list.html":     file(`{{if .}}{{template "list.html" .Children}}{{end}}`),
			},
			want: []string{
				`templates/entry.html:1:12: unknown template "image.html"`,
				`templates/entry.html:1:37: unknown template "image/caption"`,
				`blocks/image.html:1:12: unknown template "entry.html"`,
				`blocks/image.html:1:37: unknown template "list.html"`,
			},
		},
		{
			name: "defines",
			changes: map[string]*fstest.MapFile{
				"components/layout.html": file(`<div>{{block "main" .}}{{end}}</div>`),
				"components/card.html":   file(`<article></article>`),
				"blocks/group.html":      file(`{{template "layout" .}}{{define "main"}}{{.Children}}{{end}}`),
				"blocks/quote.html":      file(`{{template "layout" .}}{{define "main"}}<blockquote></blockquote>{{end}}`),
				"blocks/image.html":      file("{{define \"card\"}}c{{end}}\n{{define \"a b\"}}{{end}}\n{{block \"x.y\" .}}{{end}}"),
			},
			want: []string{
				`blocks/image.html:1:10: define "card" has the name of components/card.html`,
				`blocks/image.html:2:10: invalid define name "a b"` + use,
				`blocks/image.html:3:9: invalid define name "x.y"` + use,
			},
		},
		{
			name: "functions",
			changes: map[string]*fstest.MapFile{
				"theme.yaml":           file(funcsSettings),
				"translations/ja.yaml": file(funcsTranslation),
				"assets/css/style.css": file("body {}"),
				"components/card.html": file("<article>{{.title}}</article>"),
				"components/icon.html": file(`<i>{{asset .Icon}}</i>`),
				"blocks/file.html": file(`{{call .F}}
{{t "readMoar"}}
{{asset "css/missing.css"}}
{{dict "a"}}
{{t "readMore"}} {{t "pageOf" "page" 1 "total" .N}} {{asset "css/style.css"}}
{{template "card" dict "title" .Label}}{{template "icon" .}}
`),
			},
			want: []string{
				`components/icon.html:1:12: the path of asset must be a string constant, such as "css/style.css"`,
				"blocks/file.html:1:3: call is not allowed; template data holds no functions",
				`blocks/file.html:2:5: unknown key "readMoar"; keys must be in the default translation, translations/ja.yaml`,
				`blocks/file.html:3:9: unknown asset "css/missing.css"; paths are relative to assets/`,
				`blocks/file.html:4:8: missing value for key "a"`,
			},
		},
		{
			name:    "t without translations",
			changes: map[string]*fstest.MapFile{"blocks/file.html": file(`<a>{{t "download"}}</a>`)},
			want: []string{
				`blocks/file.html:1:8: unknown key "download"; the theme has no translation files, translations/<language>.yaml`,
			},
		},
		{
			name: "escaping",
			changes: map[string]*fstest.MapFile{
				"components/caption.html": file("<figcaption class=a\"b>x</figcaption>"),
				"blocks/embed.html":       file("<figure>\n<iframe src=a=b></iframe>\n</figure>"),
				"blocks/image.html":       file("<figure>\n<a href=\"{{.URL}}\n</figure>\n"),
				"blocks/table.html":       file(`<figure><table></table>{{template "caption" .}}</figure>`),
			},
			want: []string{
				`components/caption.html:1:20: "\"" in unquoted attr: "a\"b"`,
				`blocks/embed.html:2:14: "=" in unquoted attr: "a=b"`,
				"blocks/image.html: ends in a non-text context: " +
					"{stateURL delimDoubleQuote urlPartPreQuery jsCtxRegexp [] attrURL elementNone <nil>}",
			},
		},
		{
			name: "escaping in a component of a page and a block template",
			changes: map[string]*fstest.MapFile{
				"components/caption.html": file("<figcaption>\n<b class=a=b>x</b>\n</figcaption>"),
				"templates/entry.html":    file(`<figure>{{template "caption" .}}</figure>`),
				"blocks/image.html":       file(`<figure>{{template "caption" .}}</figure>`),
			},
			want: []string{`components/caption.html:2:11: "=" in unquoted attr: "a=b"`},
		},
	})
}

func TestSourcePosition(t *testing.T) {
	s := &source{text: "ab\nあいう\n\nx"}
	tests := []struct{ pos, line, column int }{
		{0, 1, 1}, {1, 1, 2}, {2, 1, 3}, {3, 2, 1}, {6, 2, 2}, {12, 2, 4}, {13, 3, 1}, {14, 4, 1}, {15, 4, 2},
		{-1, 1, 1}, {100, 4, 2},
	}
	for _, tt := range tests {
		if line, column := s.position(tt.pos); line != tt.line || column != tt.column {
			t.Errorf("position(%d) = %d:%d, want %d:%d", tt.pos, line, column, tt.line, tt.column)
		}
	}
}

func TestDefinePos(t *testing.T) {
	tests := []struct {
		text string
		body string // the text from the body on
		want int
	}{
		{`{{define "x"}}body`, "body", 9},
		{`{{- define "x" -}}  body`, "body", 11},
		{"{{ define\n `x` }}\nbody", "\nbody", 11},
		{`{{block "x" .}}body`, "body", 8},
		{`a{{define "x"}}{{end}}`, "{{end}}", 10},
		// The action is not found.
		{`{{define "x"}}{{/* c */}}{{end}}`, "{{end}}", 25},
		{`{{define "x"}}a{{end}}`, "a{{end}}", 9},
		{`{{define "x" -}} a{{end}}`, "a{{end}}", 9},
		{`{{define "x"}}a b{{end}}`, "b{{end}}", 16},
		{`body`, "body", 0},
		{`}}body`, "body", 2},
		{`{{.X}}body`, "body", 6},
	}
	for _, tt := range tests {
		body := strings.LastIndex(tt.text, tt.body)
		if got := definePos(tt.text, body); got != tt.want {
			t.Errorf("definePos(%q, %d) = %d, want %d", tt.text, body, got, tt.want)
		}
	}
}

func TestSyntaxError(t *testing.T) {
	const p = "templates/entry.html"
	tests := []struct {
		err  error
		line int
		msg  string
	}{
		{errors.New("template: templates/entry.html:3: unexpected EOF"), 3, "unexpected EOF"},
		{errors.New("template: templates/entry.html:12: template: multiple definition of template \"a\""), 12, `duplicate define "a"`},
		{errors.New("template: templates/entry.html:x: unexpected EOF"), 0, "template: templates/entry.html:x: unexpected EOF"},
		{errors.New("template: templates/entry.html:0: unexpected EOF"), 0, "template: templates/entry.html:0: unexpected EOF"},
		{errors.New("template: templates/entry.html:3"), 0, "template: templates/entry.html:3"},
		{errors.New("template: other.html:3: unexpected EOF"), 0, "template: other.html:3: unexpected EOF"},
		{errors.New("other"), 0, "other"},
	}
	for _, tt := range tests {
		e := syntaxError(p, tt.err)
		if e.File != p || e.Line != tt.line || e.Column != 0 || e.Msg != tt.msg {
			t.Errorf("syntaxError(%q) = %+v, want line %d and %q", tt.err, e, tt.line, tt.msg)
		}
	}
}

// TestLoadEscapeCheckStops checks that the escape check of Load runs no node of the templates: the loops would not end.
func TestLoadEscapeCheckStops(t *testing.T) {
	const loop = "{{range 9223372036854775807}}{{end}}"
	fsys := themeFS(map[string]*fstest.MapFile{
		"components/loop.html": file(loop),
		"templates/entry.html": file(loop + `{{template "loop" .}}{{if true}}` + loop + `{{end}}{{template "inner"}}` +
			`{{define "inner"}}` + loop + `{{end}}`),
		"templates/list.html":   file(`{{if true}}` + loop + `{{end}}`),
		"templates/posts.html":  file(`{{template "inner"}}{{define "inner"}}` + loop + `{{end}}`),
		"templates/term.html":   file(`{{template "loop" .}}`),
		"templates/author.html": file(`<p>{{range 1000000000}}{{range 1000000000}}x{{end}}{{end}}</p>`),
	})
	done := make(chan error, 1)
	go func() {
		_, err := Load(fsys)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Load() error:\n%v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Load() runs the loops of the templates")
	}
}

// TestNamespacesParallel checks that the namespaces, which share the templates of the components, can be cloned and
// executed in parallel.
func TestNamespacesParallel(t *testing.T) {
	th := mustLoad(t, themeFS(map[string]*fstest.MapFile{
		"components/layout.html": file(`<main>{{block "main" .}}{{end}}</main><a href="/x?q={{.}}">{{template "card" .}}</a>`),
		"components/card.html":   file(`<p title="{{.}}">{{.}}</p>`),
		"templates/entry.html":   file(`{{template "layout" .}}{{define "main"}}<h1>{{.}}</h1>{{end}}`),
		"templates/list.html":    file(`{{template "layout" .}}`),
	}))
	want := map[string]string{
		"entry.html": `<main><h1>a&lt;b</h1></main><a href="/x?q=a%3cb"><p title="a&lt;b">a&lt;b</p></a>`,
		"list.html":  `<main></main><a href="/x?q=a%3cb"><p title="a&lt;b">a&lt;b</p></a>`,
	}
	var wg sync.WaitGroup
	for range 8 {
		for name, want := range want {
			wg.Go(func() {
				c, err := th.namespaces[name].Clone()
				if err != nil {
					t.Error(err)
					return
				}
				var b strings.Builder
				if err := c.Execute(&b, "a<b"); err != nil {
					t.Error(err)
				} else if b.String() != want {
					t.Errorf("Execute(%s) = %q, want %q", name, b.String(), want)
				}
			})
		}
	}
	wg.Wait()
}
