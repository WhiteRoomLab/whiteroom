package theme

import (
	"reflect"
	"slices"
	"testing"
	"testing/fstest"
)

const (
	jaSettings = "schemaVersion: 1\ndefaultLanguage: ja\n"

	jaTranslation = `readMore: 続きを読む
pageOf: "{page} / {total} ページ"
postedBy: "{name} が投稿"
range: "{from}〜{to}"
`
)

func TestLoadTranslations(t *testing.T) {
	th := mustLoad(t, themeFS(map[string]*fstest.MapFile{
		"theme.yaml":                   file(jaSettings),
		"translations/ja.yaml":         file(jaTranslation),
		"translations/en.yaml":         file("pageOf: \"Page {page} of {total}\"\nreadMore: Read more\n"),
		"translations/en-US.yaml":      file("readMore: Read more\n"),
		"translations/zh-Hant.yaml":    file("pageOf: \"{total} 頁中第 {page} 頁\"\n"),
		"translations/es-419.yaml":     file("range: \"{from} – {to} – {from}\"\n"),
		"translations/zh-Hant-TW.yaml": file("readMore: 繼續閱讀\n"),
	}))
	want := map[string]map[string]string{
		"ja":         {"readMore": "続きを読む", "pageOf": "{page} / {total} ページ", "postedBy": "{name} が投稿", "range": "{from}〜{to}"},
		"en":         {"pageOf": "Page {page} of {total}", "readMore": "Read more"},
		"en-US":      {"readMore": "Read more"},
		"zh-Hant":    {"pageOf": "{total} 頁中第 {page} 頁"},
		"es-419":     {"range": "{from} – {to} – {from}"},
		"zh-Hant-TW": {"readMore": "繼續閱讀"},
	}
	if !reflect.DeepEqual(th.translations, want) {
		t.Errorf("translations = %q, want %q", th.translations, want)
	}
	if want := []string{"readMore", "pageOf", "postedBy", "range"}; !slices.Equal(th.keys, want) {
		t.Errorf("keys = %q, want %q", th.keys, want)
	}
}

func TestLoadTranslationErrors(t *testing.T) {
	const placeholderKeys = "x: \"{a} {b}\"\ny: 文\nz: \"{n} × {n}\"\n"
	runLoadTests(t, []loadTest{
		{
			name: "names",
			changes: map[string]*fstest.MapFile{
				"theme.yaml":                 file(jaSettings),
				"translations/ja.yaml":       file(jaTranslation),
				"translations/ja.yml":        file("next: 次へ\n"),
				"translations/japanese.yaml": file("next: 次へ\n"),
				"translations/en_US.yaml":    file("next: Next\n"),
				"translations/EN.yaml":       file("next: Next\n"),
				"translations/en-us.yaml":    file("next: Next\n"),
				"translations/README.md":     file("# Translations\n"),
				"translations/en.yaml.bak":   file("next: Next\n"),
			},
			want: []string{
				`translations/EN.yaml: invalid language code "EN": use a language tag such as "ja", "en-US" or "zh-Hant"`,
				`translations/README.md: unknown file "README.md"; translation files are <language>.yaml files, such as ja.yaml`,
				`translations/en-us.yaml: invalid language code "en-us": use a language tag such as "ja", "en-US" or "zh-Hant"`,
				`translations/en.yaml.bak: unknown file "en.yaml.bak"; translation files are <language>.yaml files, such as ja.yaml`,
				`translations/en_US.yaml: invalid language code "en_US": use a language tag such as "ja", "en-US" or "zh-Hant"`,
				`translations/ja.yml: unknown file "ja.yml"; translation files are <language>.yaml files, such as ja.yaml`,
				`translations/japanese.yaml: invalid language code "japanese": use a language tag such as "ja", "en-US" or "zh-Hant"`,
			},
		},
		{
			name:    "default language without translations",
			changes: map[string]*fstest.MapFile{"theme.yaml": file(jaSettings)},
			want:    []string{"theme.yaml:2:1: /defaultLanguage: missing translations/ja.yaml for the default language"},
		},
		{
			name: "default language without its file",
			changes: map[string]*fstest.MapFile{
				"theme.yaml":           file("# 設定\nschemaVersion: 1\nlocations:\n  header: ヘッダー\ndefaultLanguage: en\n"),
				"translations/ja.yaml": file(jaTranslation),
			},
			want: []string{"theme.yaml:5:1: /defaultLanguage: missing translations/en.yaml for the default language"},
		},
		{
			name:    "translations without a default language",
			changes: map[string]*fstest.MapFile{"translations/ja.yaml": file(jaTranslation), "translations/en.yaml": file("x: X\n")},
			want:    []string{`theme.yaml:1:1: missing key "defaultLanguage"; a theme with translations needs it`},
		},
		{
			name: "translations without a default language after a comment",
			changes: map[string]*fstest.MapFile{
				"theme.yaml":           file("# 設定\n\nschemaVersion: 1\n"),
				"translations/ja.yaml": file(jaTranslation),
			},
			want: []string{`theme.yaml:3:1: missing key "defaultLanguage"; a theme with translations needs it`},
		},
		{
			name: "invalid file without a default language",
			changes: map[string]*fstest.MapFile{
				"translations/ja.yaml": file("next: [次へ]\n"),
			},
			want: []string{
				`theme.yaml:1:1: missing key "defaultLanguage"; a theme with translations needs it`,
				"translations/ja.yaml:1:1: /next: expected string, got array",
			},
		},
		{
			name: "keys",
			changes: map[string]*fstest.MapFile{
				"theme.yaml":           file(jaSettings),
				"translations/ja.yaml": file(jaTranslation),
				"translations/en.yaml": file("readMore: Read more\nreadMoar: Read moar\nnext: Next\n"),
				"translations/fr.yaml": file("suivant: Suivant\n"),
			},
			want: []string{
				"translations/en.yaml:2:1: /readMoar: unknown key; keys must be in the default translation, translations/ja.yaml",
				"translations/en.yaml:3:1: /next: unknown key; keys must be in the default translation, translations/ja.yaml",
				"translations/fr.yaml:1:1: /suivant: unknown key; keys must be in the default translation, translations/ja.yaml",
			},
		},
		{
			name: "placeholders",
			changes: map[string]*fstest.MapFile{
				"theme.yaml":           file(jaSettings),
				"translations/ja.yaml": file(jaTranslation),
				"translations/en.yaml": file(`# English
pageOf: "Page {page}"
postedBy: "Posted by {author}"
readMore: "Read {more}"
range: "{to} {total} {from} {all}"
`),
			},
			want: []string{
				"translations/en.yaml:2:1: /pageOf: missing placeholder {total} of the default translation",
				"translations/en.yaml:3:1: /postedBy: unknown placeholder {author}; the default translation has {name}",
				"translations/en.yaml:3:1: /postedBy: missing placeholder {name} of the default translation",
				"translations/en.yaml:4:1: /readMore: unknown placeholder {more}; the default translation has no placeholders",
				"translations/en.yaml:5:1: /range: unknown placeholders {total}, {all}; the default translation has {from}, {to}",
			},
		},
		{
			name: "several placeholders",
			changes: map[string]*fstest.MapFile{
				"theme.yaml":           file("schemaVersion: 1\ndefaultLanguage: en\n"),
				"translations/en.yaml": file(placeholderKeys),
				"translations/ja.yaml": file("x: \"{d} {c}\"\ny: \"{c} {d}\"\nz: \"{n}\"\n"),
			},
			want: []string{
				"translations/ja.yaml:1:1: /x: unknown placeholders {d}, {c}; the default translation has {a}, {b}",
				"translations/ja.yaml:1:1: /x: missing placeholders {a}, {b} of the default translation",
				"translations/ja.yaml:2:1: /y: unknown placeholders {c}, {d}; the default translation has no placeholders",
			},
		},
		{
			name: "default file that does not load",
			changes: map[string]*fstest.MapFile{
				"theme.yaml":           file(jaSettings),
				"translations/ja.yaml": file("readMore: 続きを読む\nreadMore: 続き\n"),
				"translations/en.yaml": file("next: Next\n"),
			},
			want: []string{`translations/ja.yaml:2:1: duplicate key "readMore"`},
		},
		{
			name: "default file that is too large",
			changes: map[string]*fstest.MapFile{
				"theme.yaml":           file(jaSettings),
				"translations/ja.yaml": {Data: make([]byte, 1<<20+1)},
				"translations/en.yaml": file("next: Next\n"),
			},
			want: []string{"translations/ja.yaml: too large: 1048577 bytes (max 1048576)"},
		},
		{
			name: "other file that does not load",
			changes: map[string]*fstest.MapFile{
				"theme.yaml":           file(jaSettings),
				"translations/ja.yaml": file(jaTranslation),
				"translations/en.yaml": file("readMore: \"{more\"\nnext: Next\n"),
			},
			want: []string{`translations/en.yaml:1:1: /readMore: unclosed "{"`},
		},
		{
			name: "settings that do not load",
			changes: map[string]*fstest.MapFile{
				"theme.yaml":           file("schemaVersion: 1\ndefaultLanguage: ja\nstyles: [color]\n"),
				"translations/en.yaml": file("next: Next\n"),
			},
			want: []string{
				"theme.yaml:3:10: /styles/0: unknown style item \"color\" (use textColor, backgroundColor, linkColor, gradient, " +
					"fontSize, letterSpacing, fontFamily, fontWeight, lineHeight, padding, margin, blockGap, border)",
			},
		},
		{
			name: "translations that are not a directory",
			changes: map[string]*fstest.MapFile{
				"theme.yaml":   file(jaSettings),
				"translations": file("ja.yaml\n"),
			},
			want: []string{"translations: must be a directory"},
		},
		{
			name: "translations that is a symbolic link",
			changes: map[string]*fstest.MapFile{
				"theme.yaml":   file(jaSettings),
				"translations": symlink("i18n"),
				"i18n/ja.yaml": file(jaTranslation),
			},
			want: []string{"translations: symbolic links are not allowed"},
		},
	})
}
