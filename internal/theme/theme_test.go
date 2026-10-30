package theme

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

func file(s string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(s)}
}

func symlink(target string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(target), Mode: fs.ModeSymlink}
}

func special(mode fs.FileMode) *fstest.MapFile {
	return &fstest.MapFile{Mode: mode}
}

func dir() *fstest.MapFile {
	return &fstest.MapFile{Mode: fs.ModeDir | 0o755}
}

// themeFS returns the files of the smallest valid theme with changes: files to add or replace, and nil for those to
// remove.
func themeFS(changes map[string]*fstest.MapFile) fstest.MapFS {
	fsys := fstest.MapFS{
		"theme.yaml":           file("schemaVersion: 1\n"),
		"templates/entry.html": file("<p>entry</p>\n"),
		"templates/list.html":  file("<p>list</p>\n"),
	}
	for name, f := range changes {
		if f == nil {
			delete(fsys, name)
		} else {
			fsys[name] = f
		}
	}
	return fsys
}

// loadErrors returns the errors of loading fsys, which must be content.Errors without a theme.
func loadErrors(t *testing.T, fsys fs.FS) []string {
	t.Helper()
	th, err := Load(fsys)
	if err == nil {
		if th == nil {
			t.Fatal("Load() = nil, nil")
		}
		return nil
	}
	if th != nil {
		t.Errorf("Load() = %+v with an error, want nil", th)
	}
	var es content.Errors
	if !errors.As(err, &es) {
		t.Fatalf("error %q (%T) is not content.Errors", err, err)
	}
	msgs := make([]string, len(es))
	for i, e := range es {
		msgs[i] = e.Error()
	}
	return msgs
}

func checkErrors(t *testing.T, fsys fs.FS, want []string) {
	t.Helper()
	if got := loadErrors(t, fsys); !slices.Equal(got, want) {
		t.Errorf("errors:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

type loadTest struct {
	name    string
	changes map[string]*fstest.MapFile // to themeFS
	want    []string
}

func runLoadTests(t *testing.T, tests []loadTest) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkErrors(t, themeFS(tt.changes), tt.want)
		})
	}
}

func mustLoad(t *testing.T, fsys fs.FS) *Theme {
	t.Helper()
	th, err := Load(fsys)
	if err != nil {
		t.Fatalf("Load() error:\n%v", err)
	}
	return th
}

// namespaceTexts returns the templates of each namespace of th, as their text by name.
func namespaceTexts(th *Theme) map[string]map[string]string {
	texts := make(map[string]map[string]string)
	for page, ns := range th.namespaces {
		m := make(map[string]string)
		for _, x := range ns.Templates() {
			m[x.Name()] = x.Tree.Root.String()
		}
		texts[page] = m
	}
	return texts
}

// withoutNamespaces returns a copy of th without its namespaces, which reflect.DeepEqual cannot compare since they
// hold functions, and their texts.
func withoutNamespaces(th *Theme) (*Theme, map[string]map[string]string) {
	c := *th
	c.namespaces = nil
	return &c, namespaceTexts(th)
}

// fullThemeYAML is the settings file of fullTheme, which has a page template.
const fullThemeYAML = `# A theme with every kind of file
schemaVersion: 1
defaultLanguage: ja
pageTemplates:
  landing: ランディングページ
`

var logoPNG = []byte("\x89PNG\r\n\x1a\n")

// fullTheme returns a valid theme with files of every kind.
func fullTheme() fstest.MapFS {
	return themeFS(map[string]*fstest.MapFile{
		"theme.yaml":                  file(fullThemeYAML),
		"templates/entry-works.html":  file("works"),
		"templates/page-landing.html": file("landing"),
		"templates/404.html":          file("not found"),
		"components/header.html":      file("<header></header>"),
		"components/entry/card.html":  file("<article></article>"),
		"translations/ja.yaml":        file("readMore: 続きを読む\npageOf: \"{page} / {total} ページ\"\n"),
		"translations/en.yaml":        file("pageOf: \"Page {page} of {total}\"\n"),
		"assets/css/style.css":        file("body { color: var(--color-text); }\n"),
		"assets/images/logo.png":      {Data: logoPNG},
		"README.md":                   file("# A theme\n"),
	})
}

func TestLoad(t *testing.T) {
	th := mustLoad(t, fullTheme())
	settings := &content.ThemeSettings{
		DefaultLanguage: "ja",
		PageTemplates:   []content.ThemeName{{Name: "landing", Label: "ランディングページ"}},
	}
	if !reflect.DeepEqual(th.Settings, settings) {
		t.Errorf("Settings = %#v, want %#v", th.Settings, settings)
	}
	templates := map[string]string{
		"entry.html":        "<p>entry</p>\n",
		"list.html":         "<p>list</p>\n",
		"entry-works.html":  "works",
		"page-landing.html": "landing",
		"404.html":          "not found",
	}
	if !reflect.DeepEqual(th.templates, templates) {
		t.Errorf("templates = %q, want %q", th.templates, templates)
	}
	components := map[string]string{"header": "<header></header>", "entry/card": "<article></article>"}
	if !reflect.DeepEqual(th.components, components) {
		t.Errorf("components = %q, want %q", th.components, components)
	}
	namespaces := make(map[string]map[string]string)
	for name, text := range templates {
		namespaces[name] = map[string]string{name: text, "header": "<header></header>", "entry/card": "<article></article>"}
	}
	if got := namespaceTexts(th); !reflect.DeepEqual(got, namespaces) {
		t.Errorf("namespaces = %q, want %q", got, namespaces)
	}
	translations := map[string]map[string]string{
		"ja": {"readMore": "続きを読む", "pageOf": "{page} / {total} ページ"},
		"en": {"pageOf": "Page {page} of {total}"},
	}
	if !reflect.DeepEqual(th.translations, translations) {
		t.Errorf("translations = %q, want %q", th.translations, translations)
	}
	if want := []string{"readMore", "pageOf"}; !slices.Equal(th.keys, want) {
		t.Errorf("keys = %q, want %q", th.keys, want)
	}
	style := []byte("body { color: var(--color-text); }\n")
	assets := map[string]asset{
		"css/style.css":   {data: style, sum: sha256.Sum256(style)},
		"images/logo.png": {data: logoPNG, sum: sha256.Sum256(logoPNG)},
	}
	if !reflect.DeepEqual(th.assets, assets) {
		t.Errorf("assets = %v, want %v", th.assets, assets)
	}
}

func TestLoadMinimal(t *testing.T) {
	th := mustLoad(t, themeFS(nil))
	if !reflect.DeepEqual(th.Settings, &content.ThemeSettings{}) {
		t.Errorf("Settings = %#v, want zero", th.Settings)
	}
	if want := map[string]string{"entry.html": "<p>entry</p>\n", "list.html": "<p>list</p>\n"}; !reflect.DeepEqual(th.templates, want) {
		t.Errorf("templates = %q, want %q", th.templates, want)
	}
	if len(th.components) != 0 || len(th.translations) != 0 || th.keys != nil || len(th.assets) != 0 {
		t.Errorf("Load() = %+v, want no components, translations or assets", th)
	}
}

func TestLoadErrorOrder(t *testing.T) {
	fsys := themeFS(map[string]*fstest.MapFile{
		// Validate reports defaultLanguage before imageSizes.
		"theme.yaml":             file("schemaVersion: 1\nimageSizes:\n  thumb: { label: サムネイル }\ndefaultLanguage: JA\n"),
		"assets/a b.png":         file("png"),
		"assets/logo.png":        file("png"),
		"templates/single.html":  file("single"),
		"components/b c.html":    file("b"),
		"components/a b.html":    file("a"),
		"translations/en.yaml":   file("next: Next\nread more: Read more\n"),
		"translations/README.md": file("# Translations\n"),
		"translations/ja.yaml":   file("next: 次へ\nprevious: \"{\"\n"),
	})
	checkErrors(t, fsys, []string{
		"theme.yaml:3:3: /imageSizes/thumb: missing width and height; set one or both",
		`theme.yaml:4:1: /defaultLanguage: invalid language code "JA": use a language tag such as "ja", "en-US" or "zh-Hant"`,
		`translations/README.md: unknown file "README.md"; translation files are <language>.yaml files, such as ja.yaml`,
		`translations/en.yaml:2:1: /read more: invalid name "read more": use ASCII letters, digits, "-" and "_"`,
		`translations/ja.yaml:2:1: /previous: unclosed "{"`,
		`components/a b.html: invalid name "a b": use ASCII letters, digits, "-" and "_"`,
		`components/b c.html: invalid name "b c": use ASCII letters, digits, "-" and "_"`,
		`templates/single.html: unknown template name "single.html" (use entry.html, entry-{type}.html, ` +
			`page-{template}.html, list.html, posts.html, term.html, term-{taxonomy}.html, archive-{type}.html, ` +
			`author.html, 404.html)`,
		`assets/a b.png: invalid name "a b.png": use ASCII letters, digits, "-", "_", ".", "~", "@" and "+"`,
	})
}

// TestLoadErrorOrderStable checks that errors at the same place keep their order among many others.
func TestLoadErrorOrderStable(t *testing.T) {
	changes := map[string]*fstest.MapFile{"theme.yaml": file("schemaVersion: 1\ndefaultLanguage: ja\n")}
	var ja, en strings.Builder
	var want []string
	for i := range 20 {
		key := string(rune('a' + i))
		ja.WriteString(key + ": \"{" + key + "1}\"\n")
		en.WriteString(key + ": \"{" + key + "2}\"\n")
		want = append(want,
			fmt.Sprintf("translations/en.yaml:%d:1: /%s: unknown placeholder {%s2}; the default translation has {%s1}", i+1, key, key, key),
			fmt.Sprintf("translations/en.yaml:%d:1: /%s: missing placeholder {%s1} of the default translation", i+1, key, key))
	}
	changes["translations/ja.yaml"] = file(ja.String())
	changes["translations/en.yaml"] = file(en.String())
	// Errors that Load finds before those of the translations.
	for i := range 20 {
		name := fmt.Sprintf("a b%02d.png", i)
		changes["assets/"+name] = file("png")
		want = append(want, fmt.Sprintf(`assets/%s: invalid name %q: use ASCII letters, digits, "-", "_", ".", "~", "@" and "+"`, name, name))
	}
	checkErrors(t, themeFS(changes), want)
}

func TestCompareErrors(t *testing.T) {
	want := content.Errors{
		{File: "theme.yaml", Line: 1, Column: 3, Msg: "a"},
		{File: "theme.yaml", Line: 2, Column: 1, Msg: "b"},
		{File: "theme.yaml", Line: 2, Column: 1, Msg: "c"},
		{File: "theme.yaml", Line: 2, Column: 5, Msg: "d"},
		{File: "theme.yaml", Line: 10, Column: 1, Msg: "e"},
		{File: "translations/en.yaml", Line: 9, Column: 1, Msg: "f"},
		{File: "translations/ja.yaml", Line: 1, Column: 1, Msg: "g"},
		{File: "components/a.html", Msg: "h"},
		{File: "components/a/b.html", Msg: "i"},
		{File: "templates", Msg: "j"},
		{File: "templates/entry.html", Msg: "k"},
		{File: "templates/entry.html", Line: 3, Msg: "l"},
		{File: "assets/z.css", Msg: "m"},
		{Msg: "n"},
		{File: "README.md", Msg: "o"},
	}
	// b stays before c, which is at the same place.
	errs := content.Errors{
		want[14], want[12], want[13], want[11], want[10], want[9], want[8], want[7], want[6], want[5], want[4], want[3],
		want[1], want[2], want[0],
	}
	slices.SortStableFunc(errs, compareErrors)
	if !slices.Equal(errs, want) {
		t.Errorf("sorted:\n%v\nwant:\n%v", errs, want)
	}
}
