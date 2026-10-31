package content

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/yaml"
)

// Names of the files in the tests of LoadSite.
const (
	settingsFile = "settings.json"
	schemaFile   = "site/schema.yaml"
)

func loadSite(settings, schema string, lookup func(kind RefKind, id string) (Ref, bool)) (*Site, error) {
	return LoadSite(SiteFile{Name: settingsFile, Data: []byte(settings)}, SiteFile{Name: schemaFile, Data: []byte(schema)}, lookup)
}

func TestLoadExampleSite(t *testing.T) {
	settings := readTestdata(t, "example-site-settings.json")
	schema := readTestdata(t, "example-site-schema.yaml")
	got, err := loadSite(settings, schema, testLookup)
	if err != nil {
		t.Fatalf("LoadSite() = %v", err)
	}
	if !reflect.DeepEqual(got, exampleSite()) {
		t.Errorf("LoadSite() = %#v, want %#v", got, exampleSite())
	}
	if s, err := loadSite(settings, schema, nil); err != nil || !reflect.DeepEqual(s, exampleSite()) {
		t.Errorf("LoadSite() without lookup = %#v, %v; want %#v", s, err, exampleSite())
	}

	out, err := got.MarshalSettings()
	if err != nil {
		t.Fatalf("MarshalSettings() = %v", err)
	}
	want := jsontext.Value(settings)
	if err := want.Compact(); err != nil {
		t.Fatalf("Compact(%s) = %v", settings, err)
	}
	if !bytes.Equal(out, want) {
		t.Errorf("MarshalSettings() = %s, want %s", out, want)
	}

	// The schema file of 6.11.9 of the concept (docs/06.11-examples.md) adds post, with the default URL rule, to that
	// of 6.4.4.
	var works Site
	if err := works.UnmarshalSchema([]byte(readTestdata(t, "works-schema.yaml"))); err != nil {
		t.Fatalf("UnmarshalSchema() = %v", err)
	}
	types := append([]TypeDef{{Name: TypePost, URL: DefaultPostURL}}, works.Types...)
	if !reflect.DeepEqual(got.Types, types) {
		t.Errorf("Types = %#v, want %#v", got.Types, types)
	}
	if !reflect.DeepEqual(got.Taxonomies, works.Taxonomies) {
		t.Errorf("Taxonomies = %#v, want %#v", got.Taxonomies, works.Taxonomies)
	}
}

// Files that LoadSite reads without errors, for the tests below.
const (
	validSettingsJSON = `{
  "schemaVersion": 1,
  "name": {"en": "Example Inc.", "ja": "株式会社〇〇"},
  "url": "https://example.com",
  "timeZone": "Asia/Tokyo",
  "languages": ["ja", "en"],
  "defaultLanguage": "ja"
}
`
	validSchemaYAML = `schemaVersion: 1
types:
  works:
    label: 制作実績
`
)

// Files with an item that belongs in the other file.
const (
	misplacedSettingsJSON = `{
  "schemaVersion": 1,
  "types": {"works": {"label": "制作実績"}}
}
`
	misplacedSchemaYAML = `schemaVersion: 1
name:
  ja: 株式会社〇〇
`
)

var loadSiteErrorTests = []struct {
	name             string
	settings, schema string
	want             string // Error() of the error
	lookup           bool   // the errors come from lookup: without it, the files load
}{
	// Errors of reading the files.
	{
		name: "syntax error in the settings document",
		settings: `{
  "schemaVersion": 1,
  "name": {"ja": "株式会社〇〇" "en": "Example Inc."}
}
`,
		schema: validSchemaYAML,
		want:   `settings.json:3:27: invalid character '"' after object value (expecting ',' or '}')`,
	},
	{
		name:     "syntax error in the schema file",
		settings: validSettingsJSON,
		schema: `schemaVersion: 1
types:
  works:
    label: 制作実績
  works:
    label: 実績
`,
		want: `site/schema.yaml:5:3: duplicate key "works"`,
	},
	{
		name:     "key of the schema file in the settings document",
		settings: misplacedSettingsJSON,
		schema:   validSchemaYAML,
		want:     "settings.json:3:3: /types: unknown key; it belongs in the schema file",
	},
	{
		name:     "key of the settings document in the schema file",
		settings: validSettingsJSON,
		schema:   misplacedSchemaYAML,
		want:     "site/schema.yaml:2:1: /name: unknown key; it belongs in the settings document",
	},
	{
		name:     "errors of reading both files",
		settings: misplacedSettingsJSON,
		schema:   misplacedSchemaYAML,
		want: "settings.json:3:3: /types: unknown key; it belongs in the schema file\n" +
			"site/schema.yaml:2:1: /name: unknown key; it belongs in the settings document",
	},
	{
		name: "empty files",
		want: "settings.json:1:1: unexpected end of JSON input\nsite/schema.yaml:1:1: empty document",
	},
	{
		name: "missing schemaVersion in the settings document",
		settings: `{
  "name": {"ja": "株式会社〇〇"}
}
`,
		schema: validSchemaYAML,
		want:   `settings.json:1:1: missing key "schemaVersion"`,
	},
	{
		name:     "hierarchical of category",
		settings: validSettingsJSON,
		schema: `schemaVersion: 1
taxonomies:
  category:
    hierarchical: false
`,
		want: "site/schema.yaml:4:5: /taxonomies/category/hierarchical: not allowed for built-in taxonomies",
	},
	{
		// The URL rule of works is not validated.
		name: "error of reading the settings document only",
		settings: `{
  "schemaVersion": 2
}
`,
		schema: `schemaVersion: 1
types:
  works:
    url: /works/{id}/
`,
		want: "settings.json:2:3: /schemaVersion: unsupported schemaVersion 2 (use 1)",
	},
	{
		// The missing url is not validated.
		name:     "error of reading the schema file only",
		settings: `{"schemaVersion": 1}`,
		schema: `schemaVersion: 1
types:
  works:
`,
		want: "site/schema.yaml:3:3: missing value",
	},

	// Errors of Validate in the schema file.
	{
		name:     "URL rule of a type",
		settings: validSettingsJSON,
		schema: `schemaVersion: 1
types:
  works:
    label: 制作実績
    url: /works/{id}/
`,
		want: `site/schema.yaml:5:5: /types/works/url: invalid URL rule "/works/{id}/": unknown placeholder "{id}" (use {slug}, {number})`,
	},
	{
		// The error is about the URL rule, though the languages in the settings document cause it.
		name:     "URL rule at the path of a language",
		settings: validSettingsJSON,
		schema: `schemaVersion: 1
types:
  works:
    label: 制作実績
    url: /en/{slug}/
`,
		want: `site/schema.yaml:5:5: /types/works/url: invalid URL rule "/en/{slug}/": cannot start with "/en/", the path of the language en`,
	},
	{
		name:     "URL rule of a taxonomy",
		settings: validSettingsJSON,
		schema: `schemaVersion: 1
taxonomies:
  works_category:
    label: 実績の種類
    url: /kinds/{path}/
`,
		want: `site/schema.yaml:5:5: /taxonomies/works_category/url: invalid URL rule "/kinds/{path}/": ` +
			`placeholder "{path}" is not allowed for works_category (use {slug})`,
	},
	{
		name:     "URL rule of the author pages",
		settings: validSettingsJSON,
		schema: `schemaVersion: 1
authorUrl: /writers/
`,
		want: `site/schema.yaml:2:1: /authorUrl: invalid URL rule "/writers/": missing {slug}`,
	},
	{
		name:     "escaped name",
		settings: validSettingsJSON,
		schema: `schemaVersion: 1
types:
  "a/b":
    label: x
`,
		want: `site/schema.yaml:3:3: /types/a~1b: invalid name "a/b": use ASCII letters, digits, "-" and "_"`,
	},
	{
		// A missing item is at its parent.
		name:     "missing type of a field",
		settings: validSettingsJSON,
		schema: `schemaVersion: 1
types:
  works:
    fields:
      client:
        label: クライアント名
`,
		want: "site/schema.yaml:5:7: /types/works/fields/client/type: missing type",
	},

	// Errors of Validate in the settings document.
	{
		name: "default language",
		settings: `{
  "schemaVersion": 1,
  "name": {"en": "Example Inc.", "ja": "株式会社〇〇"},
  "url": "https://example.com",
  "timeZone": "Asia/Tokyo",
  "languages": ["ja", "en"],
  "defaultLanguage": "fr"
}
`,
		schema: validSchemaYAML,
		want:   `settings.json:7:3: /defaultLanguage: language "fr" is not in the site settings (use ja, en)`,
	},
	{
		name: "title format",
		settings: `{
  "schemaVersion": 1,
  "name": {"en": "Example Inc.", "ja": "株式会社〇〇"},
  "url": "https://example.com",
  "timeZone": "Asia/Tokyo",
  "languages": ["ja", "en"],
  "defaultLanguage": "ja",
  "seo": {"titleFormat": {"ja": "{siteName}"}}
}
`,
		schema: validSchemaYAML,
		want:   "settings.json:8:27: /seo/titleFormat/ja: missing {title}",
	},
	{
		// The error is about the posts page, though the URL rule of page in the schema file causes it.
		name: "posts page with the URL rule of page",
		settings: `{
  "schemaVersion": 1,
  "name": {"en": "Example Inc.", "ja": "株式会社〇〇"},
  "url": "https://example.com",
  "timeZone": "Asia/Tokyo",
  "languages": ["ja", "en"],
  "defaultLanguage": "ja",
  "frontPage": {"ja": "` + testHomePage + `"},
  "postsPage": {"ja": "` + testNewsPage + `"}
}
`,
		schema: `schemaVersion: 1
types:
  page:
    url: /{path}.html
`,
		want: `settings.json:9:17: /postsPage/ja: the URL rule of page "/{path}.html" must end with "/" for the posts page`,
	},
	{
		// A missing item is at its parent.
		name: "missing url",
		settings: `{
  "schemaVersion": 1,
  "name": {"en": "Example Inc.", "ja": "株式会社〇〇"},
  "timeZone": "Asia/Tokyo",
  "languages": ["ja", "en"],
  "defaultLanguage": "ja"
}
`,
		schema: validSchemaYAML,
		want:   "settings.json:1:1: /url: missing url",
	},
	{
		name: "missing name in a language",
		settings: `{
  "schemaVersion": 1,
  "name": {"ja": "株式会社〇〇"},
  "url": "https://example.com",
  "timeZone": "Asia/Tokyo",
  "languages": ["ja", "en"],
  "defaultLanguage": "ja"
}
`,
		schema: validSchemaYAML,
		want:   "settings.json:3:3: /name/en: missing value; every language of the site needs one",
	},
	{
		name: "logo that does not exist",
		settings: `{
  "schemaVersion": 1,
  "name": {"en": "Example Inc.", "ja": "株式会社〇〇"},
  "url": "https://example.com",
  "timeZone": "Asia/Tokyo",
  "logo": "med_` + testMissing + `",
  "languages": ["ja", "en"],
  "defaultLanguage": "ja"
}
`,
		schema: validSchemaYAML,
		want:   `settings.json:6:3: /logo: media "med_` + testMissing + `" does not exist`,
		lookup: true,
	},

	{
		// Not grouped by file.
		name: "errors of both files in the order of Validate",
		settings: `{
  "schemaVersion": 1,
  "name": {"en": "Example Inc.", "ja": "株式会社〇〇"},
  "url": "https://example.com",
  "timeZone": "Asia/Tokyo",
  "languages": ["ja", "en"],
  "defaultLanguage": "fr",
  "seo": {"titleFormat": {"ja": "{siteName}"}}
}
`,
		schema: `schemaVersion: 1
authorUrl: /writers/
types:
  works:
    label: 制作実績
    url: /works/{id}/
`,
		want: `settings.json:7:3: /defaultLanguage: language "fr" is not in the site settings (use ja, en)` + "\n" +
			`site/schema.yaml:2:1: /authorUrl: invalid URL rule "/writers/": missing {slug}` + "\n" +
			"settings.json:8:27: /seo/titleFormat/ja: missing {title}\n" +
			`site/schema.yaml:6:5: /types/works/url: invalid URL rule "/works/{id}/": unknown placeholder "{id}" (use {slug}, {number})`,
	},
}

func TestLoadSiteErrors(t *testing.T) {
	if _, err := loadSite(validSettingsJSON, validSchemaYAML, testLookup); err != nil {
		t.Fatalf("LoadSite() of the valid files = %v", err)
	}
	for _, tt := range loadSiteErrorTests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := loadSite(tt.settings, tt.schema, testLookup)
			var es Errors
			if !errors.As(err, &es) || err.Error() != tt.want {
				t.Errorf("LoadSite() = %q (%T), want %q", err, err, tt.want)
			}
			if got != nil {
				t.Errorf("LoadSite() = %#v with an error", got)
			}
			if !tt.lookup {
				return
			}
			if got, err := loadSite(tt.settings, tt.schema, nil); err != nil || got == nil {
				t.Errorf("LoadSite() without lookup = %#v, %v", got, err)
			}
		})
	}
}

// TestInSchemaFile checks that the errors of Validate are in the schema file if their first key is one of those of
// everySchemaYAML apart from schemaVersion, and in the settings document otherwise.
func TestInSchemaFile(t *testing.T) {
	out, _, err := yaml.ToJSON([]byte(everySchemaYAML))
	if err != nil {
		t.Fatal(err)
	}
	var in []string
	for _, key := range topKeys(t, out)[1:] {
		in = append(in, "/"+key, "/"+key+"/x")
	}
	in = append(in, "/types/works/fields/client/type", "/taxonomies/works_category/url")
	for _, p := range in {
		if !inSchemaFile(p) {
			t.Errorf("inSchemaFile(%q) = false", p)
		}
	}
	notIn := []string{"", "/", "/schemaVersion", "/typesX", "/types~1works", "/xtypes", "/seo/types", "/name/authorUrl"}
	for _, key := range settingsKeys {
		notIn = append(notIn, "/"+key, "/"+key+"/x")
	}
	for _, p := range notIn {
		if inSchemaFile(p) {
			t.Errorf("inSchemaFile(%q) = true", p)
		}
	}
}

// FuzzLoadSite checks that LoadSite returns a site that passes Validate, or nil and Errors, each in one of the files at
// a position in it.
func FuzzLoadSite(f *testing.F) {
	settings, err := os.ReadFile(filepath.Join("testdata", "example-site-settings.json"))
	if err != nil {
		f.Fatal(err)
	}
	schema, err := os.ReadFile(filepath.Join("testdata", "example-site-schema.yaml"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(settings, schema)
	for _, tt := range loadSiteErrorTests {
		f.Add([]byte(tt.settings), []byte(tt.schema))
	}
	f.Fuzz(func(t *testing.T, settings, schema []byte) {
		s, err := LoadSite(SiteFile{Name: settingsFile, Data: settings}, SiteFile{Name: schemaFile, Data: schema}, testLookup)
		if err == nil {
			if s == nil {
				t.Fatal("LoadSite() = nil, nil")
			}
			if err := s.Validate(testLookup); err != nil {
				t.Fatalf("Validate() of the site of LoadSite() = %v", err)
			}
			return
		}
		var es Errors
		if s != nil || !errors.As(err, &es) || len(es) == 0 {
			t.Fatalf("LoadSite() = %#v, %v (%T); want nil and Errors", s, err, err)
		}
		for _, e := range es {
			var data []byte
			switch e.File {
			case settingsFile:
				data = settings
			case schemaFile:
				data = schema
			default:
				t.Fatalf("LoadSite() = %v, in an unknown file", e)
			}
			lines := strings.Split(string(data), "\n")
			if e.Line < 1 || e.Line > len(lines) || e.Column < 1 || e.Column > utf8.RuneCountInString(lines[e.Line-1])+1 {
				t.Errorf("LoadSite() = %v, outside the file", e)
			}
		}
	})
}
