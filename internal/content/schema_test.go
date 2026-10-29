package content

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/yaml"
)

// everySchemaYAML is a schema file with every item: the type with productFields, post with postFields, and page,
// category and tag.
const everySchemaYAML = `# Every type of field and every option.
schemaVersion: 1
authorUrl: /writers/{slug}/
types:
  product:
    label: 製品
    taxonomies: [series, tag]
    fields:
      name:
        type: text
        label: 製品名
        required: true
        minLength: 2
        maxLength: 40
      summary:
        type: textarea
        maxLength: 200
      description:
        type: richText
      details:
        type: blocks
      price:
        type: number
        min: 0
        max: 1000000
        translatable: false
      inStock:
        type: boolean
        translatable: false
      releaseDate:
        type: date
      saleEnds:
        type: datetime
      size:
        type: select
        options: [S, M, L]
      colors:
        type: select
        options: [赤, 青, 緑]
        multiple: true
        minItems: 1
        maxItems: 2
      manual:
        type: media
        accept: [pdf]
      shop:
        type: link
      maker:
        type: entry
        types: [page]
      related:
        type: entry
        types: [product, post]
        multiple: true
        maxItems: 3
      mainCategory:
        type: term
        taxonomy: category
      keywords:
        type: term
        taxonomy: tag
        multiple: true
        minItems: 2
      spec:
        type: group
        translatable: false
        fields:
          material:
            type: text
            required: true
          weight:
            type: number
            min: 0
      features:
        type: list
        of: { type: text, maxLength: 50 }
        minItems: 1
        maxItems: 5
      variants:
        type: list
        of:
          type: group
          label: バリエーション
          fields:
            code: { type: text, required: true }
            image: { type: media, accept: [image] }
            sizes: { type: select, options: [S, M, L], multiple: true }
      matrix:
        type: list
        of:
          type: list
          of: { type: number }
  post:
    label: お知らせ
    url: /news/{slug}/
    taxonomies: [series]
    fields:
      subtitle:
        type: text
      sidebar:
        type: blocks
  page:
    label: 固定ページ
    url: /pages/{path}/
    fields:
      lead:
        type: textarea
        label: リード

taxonomies:
  series:
    label: シリーズ
    hierarchical: true
  category:
    label: カテゴリー
    url: /topics/{path}/
  tag:
    label: タグ
    url: /tags/{slug}/
`

// everySchema returns the items of everySchemaYAML.
func everySchema() *Site {
	return &Site{
		AuthorURL: "/writers/{slug}/",
		Types: []TypeDef{
			{Name: "product", Label: "製品", Taxonomies: []string{"series", TaxonomyTag}, Fields: productFields},
			{Name: TypePost, Label: "お知らせ", URL: "/news/{slug}/", Taxonomies: []string{"series"}, Fields: postFields},
			{Name: TypePage, Label: "固定ページ", URL: "/pages/{path}/", Fields: []FieldDef{{Name: "lead", Type: FieldTextarea, Label: "リード"}}},
		},
		Taxonomies: []TaxonomyDef{
			{Name: "series", Label: "シリーズ", Hierarchical: true},
			{Name: TaxonomyCategory, Label: "カテゴリー", URL: "/topics/{path}/"},
			{Name: TaxonomyTag, Label: "タグ", URL: "/tags/{slug}/"},
		},
	}
}

type unmarshalSchemaTest struct {
	name  string
	in    string
	want  *Site // AuthorURL, Types and Taxonomies
	valid bool  // Validate accepts them with the settings of minimalSite
}

var unmarshalSchemaTests = []unmarshalSchemaTest{
	{name: "every item", in: everySchemaYAML, want: everySchema(), valid: true},
	{name: "schemaVersion only", in: "schemaVersion: 1\n", want: &Site{}, valid: true},
	{
		name:  "JSON",
		in:    `{"schemaVersion": 1, "types": {"works": {"label": "制作実績", "body": true}}}`,
		want:  &Site{Types: []TypeDef{{Name: "works", Label: "制作実績", Body: true}}},
		valid: true,
	},
	{
		name: "written order",
		in: `taxonomies:
  zeta: { label: Z }
  alpha: { label: A }
types:
  zeta:
    fields:
      b: { type: text }
      a: { type: text }
  alpha: { label: A }
authorUrl: /writers/{slug}/
schemaVersion: 1
`,
		want: &Site{
			AuthorURL: "/writers/{slug}/",
			Types: []TypeDef{
				{Name: "zeta", Fields: []FieldDef{{Name: "b", Type: FieldText}, {Name: "a", Type: FieldText}}},
				{Name: "alpha", Label: "A"},
			},
			Taxonomies: []TaxonomyDef{{Name: "zeta", Label: "Z"}, {Name: "alpha", Label: "A"}},
		},
		valid: true,
	},
	{
		name: "numbers",
		in:   worksHead + "    fields:\n      ratio:\n        type: number\n        min: -0.1\n        max: 1e300\n",
		want: &Site{Types: []TypeDef{{
			Name:   "works",
			Fields: []FieldDef{{Name: "ratio", Type: FieldNumber, Min: new(-0.1), Max: new(1e300)}},
		}}},
		valid: true,
	},
	{
		// Validate reports these.
		name: "values as written",
		in: `schemaVersion: 1
authorUrl: " /Writers/{slug}"
types:
  Works!:
    label: " 制作実績"
    url: works
    archive: /
    taxonomies: [unknown, unknown]
    body: false
    fields:
      year:
        type: color
        label: Cafe` + acute + `
        required: false
        translatable: true
        minLength: -1
        maxLength: -2
        min: 2100
        max: -0.5
        options: [a, a]
        multiple: false
        minItems: 0
        maxItems: -3
        accept: [video]
        types: [nothing]
        taxonomy: nothing
        fields:
          x y: { type: text }
        of: { type: text, required: true, translatable: false }
taxonomies:
  Works Category:
    url: works
    hierarchical: false
`,
		want: &Site{
			AuthorURL: " /Writers/{slug}",
			Types: []TypeDef{{
				Name: "Works!", Label: " 制作実績", URL: "works", Archive: "/", Taxonomies: []string{"unknown", "unknown"},
				Fields: []FieldDef{{
					Name: "year", Type: "color", Label: "Cafe" + acute, MinLength: -1, MaxLength: -2,
					Min: new(2100.0), Max: new(-0.5), Options: []string{"a", "a"}, MaxItems: -3,
					Accept: []string{"video"}, Types: []string{"nothing"}, Taxonomy: "nothing",
					Fields: []FieldDef{{Name: "x y", Type: FieldText}},
					Of:     &FieldDef{Type: FieldText, Required: true, Synced: true},
				}},
			}},
			Taxonomies: []TaxonomyDef{{Name: "Works Category", URL: "works"}},
		},
	},
}

func TestUnmarshalSchema(t *testing.T) {
	example := exampleSite()
	tests := append([]unmarshalSchemaTest{{
		name:  "example",
		in:    readTestdata(t, "works-schema.yaml"),
		want:  &Site{Types: []TypeDef{*example.Type("works")}, Taxonomies: example.Taxonomies},
		valid: true,
	}}, unmarshalSchemaTests...)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The items of the schema file are replaced, even if in omits them, and the others stay.
			got := fullSettingsSite()
			got.AuthorURL = "/authors/{slug}/"
			if err := got.UnmarshalSchema([]byte(tt.in)); err != nil {
				t.Fatalf("UnmarshalSchema(%s) = %v", tt.in, err)
			}
			want := fullSettingsSite()
			want.AuthorURL, want.Types, want.Taxonomies = tt.want.AuthorURL, tt.want.Types, tt.want.Taxonomies
			if !reflect.DeepEqual(got, want) {
				t.Errorf("UnmarshalSchema(%s) = %#v, want %#v", tt.in, got, want)
			}
			s := minimalSite()
			if err := s.UnmarshalSchema([]byte(tt.in)); err != nil {
				t.Fatalf("UnmarshalSchema(%s) = %v", tt.in, err)
			}
			if err := s.Validate(testLookup); (err == nil) != tt.valid {
				t.Errorf("Validate() = %v, want valid %t", err, tt.valid)
			}
		})
	}
}

const settingsDocumentKey = "unknown key; it belongs in the settings document"

// Inputs before the lines 4 and 6 of the tests below.
const (
	worksHead  = "schemaVersion: 1\ntypes:\n  works:\n"
	clientHead = worksHead + "    fields:\n      client:\n"
)

var unmarshalSchemaErrorTests = []struct {
	name         string
	in           string
	path, msg    string
	line, column int
}{
	// Errors in the YAML have no Path.
	{name: "tab", in: "schemaVersion: 1\ntypes:\n\tworks:\n", msg: "tab characters are not allowed; use spaces", line: 3, column: 1},
	{
		name: "duplicate key",
		in:   worksHead + "    label: 制作実績\n  works:\n    label: 実績\n",
		msg:  `duplicate key "works"`, line: 5, column: 3,
	},
	{name: "null", in: "schemaVersion: 1\nauthorUrl: null\n", msg: "null is not allowed; remove the key", line: 2, column: 12},
	{name: "without a value", in: worksHead, msg: "missing value", line: 3, column: 3},
	{
		name: "unclosed flow",
		in:   worksHead + "    taxonomies: [works_category\n",
		msg:  `unclosed "["; to write a text that starts with "[", quote the whole value`, line: 4, column: 17,
	},
	{name: "empty input", in: "", msg: "empty document", line: 1, column: 1},

	// A missing key is at its parent, the root.
	{name: "missing schemaVersion", in: "types:\n  works:\n    label: 制作実績\n", msg: `missing key "schemaVersion"`, line: 1, column: 1},
	{
		name: "missing schemaVersion after a comment",
		in:   "# site/schema.yaml\n\ntypes:\n  works:\n    label: 制作実績\n",
		msg:  `missing key "schemaVersion"`, line: 3, column: 1,
	},
	{
		name: "missing schemaVersion of an indented mapping",
		in:   "  types:\n    works:\n      label: 制作実績\n",
		msg:  `missing key "schemaVersion"`, line: 1, column: 3,
	},
	{name: "schemaVersion 2", in: "schemaVersion: 2\n", path: "/schemaVersion", msg: "unsupported schemaVersion 2 (use 1)", line: 1, column: 1},
	{name: "schemaVersion as a string", in: "schemaVersion: \"1\"\n", path: "/schemaVersion", msg: "expected number, got string", line: 1, column: 1},
	{name: "schemaVersion 1.0", in: "schemaVersion: 1.0\n", path: "/schemaVersion", msg: "must be an integer", line: 1, column: 1},

	{name: "unknown key", in: "schemaVersion: 1\ntitle: 制作実績\n", path: "/title", msg: "unknown key", line: 2, column: 1},
	{name: "key case", in: "schemaVersion: 1\nTypes:\n  works:\n    label: 制作実績\n", path: "/Types", msg: "unknown key", line: 2, column: 1},
	{name: "name", in: "schemaVersion: 1\nname:\n  ja: 株式会社〇〇\n", path: "/name", msg: settingsDocumentKey, line: 2, column: 1},
	{
		name: "allowHtml after the types",
		in:   worksHead + "    label: 制作実績\nallowHtml: true\n",
		path: "/allowHtml", msg: settingsDocumentKey, line: 5, column: 1,
	},
	{name: "seo before schemaVersion", in: "seo:\n  noindex: true\nschemaVersion: 1\n", path: "/seo", msg: settingsDocumentKey, line: 1, column: 1},
	{
		name: "key of the settings document in a type",
		in:   worksHead + "    name: 制作実績\n",
		path: "/types/works/name", msg: "unknown key", line: 4, column: 5,
	},
	{
		name: "key of the settings document in a field",
		in:   clientHead + "        languages: [ja]\n",
		path: "/types/works/fields/client/languages", msg: "unknown key", line: 6, column: 9,
	},
	{name: "unknown key in a type", in: worksHead + "    title: 制作実績\n", path: "/types/works/title", msg: "unknown key", line: 4, column: 5},
	{
		name: "unknown key in a taxonomy",
		in:   "schemaVersion: 1\ntaxonomies:\n  works_category:\n    hierarchy: true\n",
		path: "/taxonomies/works_category/hierarchy", msg: "unknown key", line: 4, column: 5,
	},
	{
		name: "unknown key in a field",
		in:   clientHead + "        type: text\n        maxlength: 100\n",
		path: "/types/works/fields/client/maxlength", msg: "unknown key", line: 7, column: 9,
	},
	{
		name: "name of a field",
		in:   clientHead + "        name: client\n",
		path: "/types/works/fields/client/name", msg: "unknown key", line: 6, column: 9,
	},
	{
		name: "name of the items of a list",
		in:   worksHead + "    fields:\n      images:\n        type: list\n        of: { type: media, name: image }\n",
		path: "/types/works/fields/images/of/name", msg: "unknown key", line: 7, column: 28,
	},
	{
		name: "unknown key in a group",
		in:   clientHead + "        type: group\n        fields:\n          weight:\n            unit: kg\n",
		path: "/types/works/fields/client/fields/weight/unit", msg: "unknown key", line: 9, column: 13,
	},
	{
		name: "unknown key in the items of a list of groups",
		in: worksHead + "    fields:\n      variants:\n        type: list\n        of:\n          type: group\n" +
			"          fields:\n            code: {type: text, unique: true}\n",
		path: "/types/works/fields/variants/of/fields/code/unique", msg: "unknown key", line: 10, column: 32,
	},

	{name: "label as a number", in: worksHead + "    label: 1\n", path: "/types/works/label", msg: "expected string, got number", line: 4, column: 5},
	{name: "body as yes", in: worksHead + "    body: yes\n", path: "/types/works/body", msg: "expected boolean, got string", line: 4, column: 5},
	{
		name: "taxonomies of a type as a string",
		in:   worksHead + "    taxonomies: works_category\n",
		path: "/types/works/taxonomies", msg: "expected array, got string", line: 4, column: 5,
	},
	{
		name: "taxonomy of a type as a number",
		in:   worksHead + "    taxonomies: [works_category, 1]\n",
		path: "/types/works/taxonomies/1", msg: "expected string, got number", line: 4, column: 34,
	},
	{
		name: "fields as a sequence",
		in:   worksHead + "    fields:\n      - client\n",
		path: "/types/works/fields", msg: "expected object, got array", line: 4, column: 5,
	},
	{
		name: "maxLength 2.0",
		in:   clientHead + "        type: text\n        maxLength: 2.0\n",
		path: "/types/works/fields/client/maxLength", msg: "must be an integer", line: 7, column: 9,
	},
	{
		name: "min as a string",
		in:   clientHead + "        type: number\n        min: \"10\"\n",
		path: "/types/works/fields/client/min", msg: "expected number, got string", line: 7, column: 9,
	},
	{
		name: "min beyond float64",
		in:   clientHead + "        type: number\n        min: 1e400\n",
		path: "/types/works/fields/client/min", msg: "number 1e400 is out of range", line: 7, column: 9,
	},
	{
		name: "max beyond float64",
		in:   clientHead + "        type: number\n        max: -1e999\n",
		path: "/types/works/fields/client/max", msg: "number -1e999 is out of range", line: 7, column: 9,
	},
	{
		name: "translatable as no",
		in:   clientHead + "        translatable: no\n",
		path: "/types/works/fields/client/translatable", msg: "expected boolean, got string", line: 6, column: 9,
	},
	{
		name: "type as a number",
		in:   clientHead + "        type: 1\n",
		path: "/types/works/fields/client/type", msg: "expected string, got number", line: 6, column: 9,
	},
	{
		name: "options as a string",
		in:   clientHead + "        type: select\n        options: S\n",
		path: "/types/works/fields/client/options", msg: "expected array, got string", line: 7, column: 9,
	},
	{
		name: "option as a number",
		in:   clientHead + "        type: select\n        options: [S, 1]\n",
		path: "/types/works/fields/client/options/1", msg: "expected string, got number", line: 7, column: 22,
	},
	{
		name: "taxonomy of a field as a sequence",
		in:   clientHead + "        type: term\n        taxonomy: [category]\n",
		path: "/types/works/fields/client/taxonomy", msg: "expected string, got array", line: 7, column: 9,
	},
	{
		name: "of as a string",
		in:   clientHead + "        type: list\n        of: media\n",
		path: "/types/works/fields/client/of", msg: "expected object, got string", line: 7, column: 9,
	},
	{
		name: "hierarchical as a string",
		in:   "schemaVersion: 1\ntaxonomies:\n  works_category:\n    hierarchical: \"false\"\n",
		path: "/taxonomies/works_category/hierarchical", msg: "expected boolean, got string", line: 4, column: 5,
	},
	// Validate cannot tell false from omitted, which these would silently ignore.
	{
		name: "hierarchical of category",
		in:   "schemaVersion: 1\ntaxonomies:\n  category:\n    label: 分類\n    hierarchical: false\n",
		path: "/taxonomies/category/hierarchical", msg: "not allowed for built-in taxonomies", line: 5, column: 5,
	},
	{
		name: "hierarchical of tag",
		in:   "schemaVersion: 1\ntaxonomies:\n  tag:\n    hierarchical: true\n",
		path: "/taxonomies/tag/hierarchical", msg: "not allowed for built-in taxonomies", line: 4, column: 5,
	},
	{
		name: "body of post",
		in:   "schemaVersion: 1\ntypes:\n  post:\n    body: false\n",
		path: "/types/post/body", msg: "not allowed for post", line: 4, column: 5,
	},
	{
		name: "body of page",
		in:   "schemaVersion: 1\ntypes:\n  page:\n    label: 固定ページ\n    body: true\n",
		path: "/types/page/body", msg: "not allowed for page", line: 5, column: 5,
	},
	{
		name: "authorUrl as a mapping",
		in:   "schemaVersion: 1\nauthorUrl: { slug: x }\n",
		path: "/authorUrl", msg: "expected string, got object", line: 2, column: 1,
	},
	{name: "types as a sequence", in: "schemaVersion: 1\ntypes:\n  - works\n", path: "/types", msg: "expected object, got array", line: 2, column: 1},
	{
		name: "type as a string",
		in:   "schemaVersion: 1\ntypes:\n  works: 制作実績\n",
		path: "/types/works", msg: "expected object, got string", line: 3, column: 3,
	},
	{name: "sequence", in: "- schemaVersion: 1\n", msg: "expected object, got array", line: 1, column: 1},
	{name: "text", in: "schemaVersion\n", msg: "expected object, got string", line: 1, column: 1},
	{name: "indented flow sequence", in: "  [schemaVersion]\n", msg: "expected object, got array", line: 1, column: 3},

	{
		name: "empty taxonomies of a type",
		in:   worksHead + "    taxonomies: []\n",
		path: "/types/works/taxonomies", msg: "empty array is not allowed", line: 4, column: 5,
	},
	{name: "empty fields", in: worksHead + "    fields: {}\n", path: "/types/works/fields", msg: "empty object is not allowed", line: 4, column: 5},
	{name: "empty types", in: "schemaVersion: 1\ntypes: {}\n", path: "/types", msg: "empty object is not allowed", line: 2, column: 1},
	{name: "empty type", in: "schemaVersion: 1\ntypes:\n  works: {}\n", path: "/types/works", msg: "empty object is not allowed", line: 3, column: 3},
	{
		name: "empty taxonomy",
		in:   "schemaVersion: 1\ntaxonomies:\n  works_category: {}\n",
		path: "/taxonomies/works_category", msg: "empty object is not allowed", line: 3, column: 3,
	},
	{name: "empty label", in: worksHead + "    label: \"\"\n", path: "/types/works/label", msg: "empty string is not allowed", line: 4, column: 5},
	{
		name: "empty options",
		in:   clientHead + "        type: select\n        options: []\n",
		path: "/types/works/fields/client/options", msg: "empty array is not allowed", line: 7, column: 9,
	},
	{
		name: "empty kind of media",
		in:   clientHead + "        type: media\n        accept: ['']\n",
		path: "/types/works/fields/client/accept/0", msg: "empty string is not allowed", line: 7, column: 18,
	},
	{
		name: "empty of",
		in:   clientHead + "        type: list\n        of: {}\n",
		path: "/types/works/fields/client/of", msg: "empty object is not allowed", line: 7, column: 9,
	},
	{name: "empty mapping", in: "{}\n", msg: "empty object is not allowed", line: 1, column: 1},

	// Pointers escape "~" and "/", and columns count characters.
	{
		name: "escaped name",
		in:   "schemaVersion: 1\ntypes:\n  \"a/b~c\":\n    title: x\n",
		path: "/types/a~1b~0c/title", msg: "unknown key", line: 4, column: 5,
	},
	{
		name: "Japanese names",
		in:   "schemaVersion: 1\ntypes:\n  実績:\n    fields:\n      顧客: {type: text, 名前: x}\n",
		path: "/types/実績/fields/顧客/名前", msg: "unknown key", line: 5, column: 24,
	},
	{
		name: "name outside the BMP",
		in:   "schemaVersion: 1\ntypes:\n  " + grinningFace + ": {label: 1}\n",
		path: "/types/" + grinningFace + "/label", msg: "expected string, got number", line: 3, column: 7,
	},
	{
		name: "error after the other items",
		in: "schemaVersion: 1\nauthorUrl: /writers/{slug}/\ntypes:\n  works:\n    label: 制作実績\n" +
			"taxonomies:\n  works_category:\n    label: 1\n",
		path: "/taxonomies/works_category/label", msg: "expected string, got number", line: 8, column: 5,
	},
}

func TestUnmarshalSchemaErrors(t *testing.T) {
	for _, tt := range unmarshalSchemaErrorTests {
		t.Run(tt.name, func(t *testing.T) {
			got := fullSettingsSite()
			err := got.UnmarshalSchema([]byte(tt.in))
			want := &Error{Path: tt.path, Msg: tt.msg, Line: tt.line, Column: tt.column}
			var e *Error
			if !errors.As(err, &e) || *e != *want {
				t.Errorf("UnmarshalSchema(%q) = %#v, want %#v", tt.in, err, want)
			}
			if !reflect.DeepEqual(got, fullSettingsSite()) {
				t.Errorf("UnmarshalSchema(%q) changed the site to %#v", tt.in, got)
			}
		})
	}
}

// TestUnmarshalSchemaKeys checks that every key of the definitions reads its value, and reports a value of another
// type at the key. The definitions of fields have every option that fieldOptions lists.
func TestUnmarshalSchemaKeys(t *testing.T) {
	fieldKeys := []string{"type", "label", "required", "translatable"}
	var options []string
	for _, opts := range fieldOptions {
		for _, k := range opts {
			if !slices.Contains(options, k) {
				options = append(options, k)
			}
		}
	}
	slices.Sort(options)
	fieldKeys = append(fieldKeys, options...)
	tests := []struct {
		head         string
		path         string
		keys         []string
		mappings     []string // the keys whose values are mappings
		line, column int
	}{
		{
			head: "schemaVersion: 1\n",
			keys: []string{"authorUrl", "taxonomies", "types"}, mappings: []string{"taxonomies", "types"}, line: 2, column: 1,
		},
		{
			head: "schemaVersion: 1\ntaxonomies:\n  works_category:\n", path: "/taxonomies/works_category",
			keys: []string{"label", "url", "hierarchical"}, line: 4, column: 5,
		},
		{
			head: worksHead, path: "/types/works",
			keys: []string{"label", "url", "archive", "taxonomies", "body", "fields"}, mappings: []string{"fields"}, line: 4, column: 5,
		},
		{head: clientHead, path: "/types/works/fields/client", keys: fieldKeys, mappings: []string{"fields", "of"}, line: 6, column: 9},
	}
	for _, tt := range tests {
		for _, key := range tt.keys {
			value, kind := "{ x: 1 }", "object"
			if slices.Contains(tt.mappings, key) {
				value, kind = "[x]", "array"
			}
			in := tt.head + strings.Repeat(" ", tt.column-1) + key + ": " + value + "\n"
			err := new(Site).UnmarshalSchema([]byte(in))
			var e *Error
			if !errors.As(err, &e) || e.Path != tt.path+"/"+key || e.Line != tt.line || e.Column != tt.column ||
				!strings.HasPrefix(e.Msg, "expected ") || !strings.HasSuffix(e.Msg, ", got "+kind) {
				t.Errorf("UnmarshalSchema(%q) = %#v, want an error about the %s at %d:%d", in, err, kind, tt.line, tt.column)
			}
		}
	}
}

// topKeys returns the keys of data, a JSON object, in the written order.
func topKeys(t *testing.T, data []byte) []string {
	t.Helper()
	ms, err := members(data, "")
	if err != nil {
		t.Fatalf("members(%s) = %v", data, err)
	}
	keys := make([]string, len(ms))
	for i, m := range ms {
		keys[i] = m.name
	}
	return keys
}

// TestMisplacedKeys checks that each of the settings document and the schema file has the keys of the other reported as
// belonging there: settingsKeys are the keys of fullSettingsJSON apart from schemaVersion, and the keys of the schema
// file are those of everySchemaYAML.
func TestMisplacedKeys(t *testing.T) {
	// fullSettingsJSON has every key, as fullSettingsSite sets every item of the settings document.
	v := reflect.ValueOf(*fullSettingsSite())
	for i := range v.NumField() {
		switch name := v.Type().Field(i).Name; name {
		case "AuthorURL", "Types", "Taxonomies":
		default:
			if v.Field(i).IsZero() {
				t.Errorf("fullSettingsSite() has no %s", name)
			}
		}
	}
	got := topKeys(t, []byte(fullSettingsJSON))
	if want := append([]string{"schemaVersion"}, settingsKeys...); !slices.Equal(got, want) {
		t.Errorf("keys of fullSettingsJSON = %q, want schemaVersion and settingsKeys, %q", got, want)
	}
	for _, key := range settingsKeys {
		in := "schemaVersion: 1\n" + key + ": x\n"
		err := new(Site).UnmarshalSchema([]byte(in))
		want := &Error{Path: "/" + key, Msg: settingsDocumentKey, Line: 2, Column: 1}
		var e *Error
		if !errors.As(err, &e) || *e != *want {
			t.Errorf("UnmarshalSchema(%q) = %#v, want %#v", in, err, want)
		}
	}

	out, _, err := yaml.ToJSON([]byte(everySchemaYAML))
	if err != nil {
		t.Fatal(err)
	}
	keys := topKeys(t, out)
	if want := []string{"schemaVersion", "authorUrl", "types", "taxonomies"}; !slices.Equal(keys, want) {
		t.Errorf("keys of everySchemaYAML = %q, want %q", keys, want)
	}
	for _, key := range keys[1:] {
		in := `{"schemaVersion":1,"` + key + `":1}`
		err := new(Site).UnmarshalSettings([]byte(in))
		want := &Error{Path: "/" + key, Msg: schemaFileKey}
		var e *Error
		if !errors.As(err, &e) || *e != *want {
			t.Errorf("UnmarshalSettings(%s) = %#v, want %#v", in, err, want)
		}
	}
}

// definitionPointers returns the JSON Pointers of the items that s has from a schema file, as Validate writes them.
func definitionPointers(s *Site) []string {
	var ps []string
	add := func(set bool, p string) {
		if set {
			ps = append(ps, p)
		}
	}
	items := func(p string, vs []string) {
		for i := range vs {
			ps = append(ps, p+"/"+strconv.Itoa(i))
		}
	}
	add(s.AuthorURL != "", "/authorUrl")
	for _, t := range s.Taxonomies {
		p := "/taxonomies/" + escapePointer(t.Name)
		ps = append(ps, p)
		add(t.Label != "", p+"/label")
		add(t.URL != "", p+"/url")
		add(t.Hierarchical, p+"/hierarchical")
	}
	var field func(p string, f *FieldDef)
	field = func(p string, f *FieldDef) {
		ps = append(ps, p)
		add(f.Type != "", p+"/type")
		add(f.Label != "", p+"/label")
		add(f.Required, p+"/required")
		add(f.Synced, p+"/translatable")
		for _, k := range f.options() {
			ps = append(ps, p+"/"+k)
		}
		items(p+"/options", f.Options)
		items(p+"/accept", f.Accept)
		items(p+"/types", f.Types)
		for i := range f.Fields {
			field(p+"/fields/"+escapePointer(f.Fields[i].Name), &f.Fields[i])
		}
		if f.Of != nil {
			field(p+"/of", f.Of)
		}
	}
	for _, t := range s.Types {
		p := "/types/" + escapePointer(t.Name)
		ps = append(ps, p)
		add(t.Label != "", p+"/label")
		add(t.URL != "", p+"/url")
		add(t.Archive != "", p+"/archive")
		add(t.Body, p+"/body")
		items(p+"/taxonomies", t.Taxonomies)
		add(t.Fields != nil, p+"/fields")
		for i := range t.Fields {
			field(p+"/fields/"+escapePointer(t.Fields[i].Name), &t.Fields[i])
		}
	}
	return ps
}

// FuzzUnmarshalSchema checks that an error has the position of its value, or of the root for a missing key, and that
// the positions of a schema file that reads have every item of the definitions, for the errors of Validate.
func FuzzUnmarshalSchema(f *testing.F) {
	example, err := os.ReadFile(filepath.Join("testdata", "works-schema.yaml"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(example)
	for _, tt := range unmarshalSchemaTests {
		f.Add([]byte(tt.in))
	}
	for _, tt := range unmarshalSchemaErrorTests {
		f.Add([]byte(tt.in))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		s := fullSettingsSite()
		positions, err := s.unmarshalSchema(data)
		if err != nil {
			var e *Error
			if !errors.As(err, &e) || e.File != "" || e.Line < 1 || e.Column < 1 || positions != nil {
				t.Fatalf("unmarshalSchema(%q) = %v, %#v; want an *Error with a position", data, positions, err)
			}
			if !reflect.DeepEqual(s, fullSettingsSite()) {
				t.Fatalf("unmarshalSchema(%q) = %v and changed the site to %#v", data, err, s)
			}
			_, ypos, yerr := yaml.ToJSON(data)
			var ye *yaml.Error
			if errors.As(yerr, &ye) {
				if *e != (Error{Msg: ye.Msg, Line: ye.Line, Column: ye.Column}) {
					t.Errorf("unmarshalSchema(%q) = %#v for %v", data, e, yerr)
				}
			} else if pos, ok := ypos[e.Path]; !ok || pos != (yaml.Position{Line: e.Line, Column: e.Column}) {
				t.Errorf("unmarshalSchema(%q) = %#v, but the value is at %v", data, e, pos)
			}
			return
		}
		want := fullSettingsSite()
		want.AuthorURL, want.Types, want.Taxonomies = s.AuthorURL, s.Types, s.Taxonomies
		if !reflect.DeepEqual(s, want) {
			t.Fatalf("unmarshalSchema(%q) changed the settings to %#v", data, s)
		}
		for _, p := range definitionPointers(s) {
			if _, ok := positions[p]; !ok {
				t.Errorf("unmarshalSchema(%q) has no position of %q", data, p)
			}
		}
		// Validate takes any definitions that read, without panicking.
		_ = s.Validate(nil)
	})
}
