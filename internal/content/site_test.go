package content

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

// productFields use every type of field and every option.
var productFields = []FieldDef{
	{Name: "name", Type: FieldText, Label: "製品名", Required: true, MinLength: 2, MaxLength: 40},
	{Name: "summary", Type: FieldTextarea, MaxLength: 200},
	{Name: "description", Type: FieldRichText},
	{Name: "details", Type: FieldBlocks},
	{Name: "price", Type: FieldNumber, Min: new(0.0), Max: new(1000000.0), Synced: true},
	{Name: "inStock", Type: FieldBoolean, Synced: true},
	{Name: "releaseDate", Type: FieldDate},
	{Name: "saleEnds", Type: FieldDatetime},
	{Name: "size", Type: FieldSelect, Options: []string{"S", "M", "L"}},
	{Name: "colors", Type: FieldSelect, Options: []string{"赤", "青", "緑"}, Multiple: true, MinItems: 1, MaxItems: 2},
	{Name: "manual", Type: FieldMedia, Accept: []string{MediaPDF}},
	{Name: "shop", Type: FieldLink},
	{Name: "maker", Type: FieldEntry, Types: []string{TypePage}},
	{Name: "related", Type: FieldEntry, Types: []string{"product", TypePost}, Multiple: true, MaxItems: 3},
	{Name: "mainCategory", Type: FieldTerm, Taxonomy: TaxonomyCategory},
	{Name: "keywords", Type: FieldTerm, Taxonomy: TaxonomyTag, Multiple: true, MinItems: 2},
	{Name: "spec", Type: FieldGroup, Synced: true, Fields: []FieldDef{
		{Name: "material", Type: FieldText, Required: true},
		{Name: "weight", Type: FieldNumber, Min: new(0.0)},
	}},
	{Name: "features", Type: FieldList, Of: &FieldDef{Type: FieldText, MaxLength: 50}, MinItems: 1, MaxItems: 5},
	{Name: "variants", Type: FieldList, Of: &FieldDef{Type: FieldGroup, Label: "バリエーション", Fields: []FieldDef{
		{Name: "code", Type: FieldText, Required: true},
		{Name: "image", Type: FieldMedia, Accept: []string{MediaImage}},
		{Name: "sizes", Type: FieldSelect, Options: []string{"S", "M", "L"}, Multiple: true},
	}}},
	{Name: "matrix", Type: FieldList, Of: &FieldDef{Type: FieldList, Of: &FieldDef{Type: FieldNumber}}},
}

var postFields = []FieldDef{
	{Name: "subtitle", Type: FieldText},
	{Name: "sidebar", Type: FieldBlocks},
}

// testSite returns a site with the type and the taxonomy of the example in CONCEPT.md 6.4.4,
// a type with productFields, and post with postFields.
func testSite() *Site {
	return &Site{
		Languages: []string{"ja", "en"},
		Types: []TypeDef{
			{
				Name:       "works",
				Label:      "制作実績",
				Body:       true,
				Taxonomies: []string{"works_category"},
				Fields: []FieldDef{
					{Name: "client", Type: FieldText, Label: "クライアント名", Required: true, MaxLength: 100},
					{Name: "year", Type: FieldNumber, Label: "制作年", Min: new(1990.0), Max: new(2100.0), Synced: true},
					{Name: "images", Type: FieldList, Label: "画像", Of: &FieldDef{Type: FieldMedia, Accept: []string{MediaImage}}, MaxItems: 20},
				},
			},
			{Name: "product", Label: "製品", Taxonomies: []string{"series", TaxonomyTag}, Fields: productFields},
			{Name: TypePost, Label: "お知らせ", Taxonomies: []string{"series"}, Fields: postFields},
		},
		Taxonomies: []TaxonomyDef{
			{Name: "works_category", Label: "実績の種類"},
			{Name: "series", Label: "シリーズ", Hierarchical: true},
		},
	}
}

// IDs of the entities that testLookup finds, and of others.
const (
	testAboutPage   = "ent_01JB8W2N5KD3F6H9J1M4P7R2T5" // page in ja
	testPage        = "ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3" // page in ja
	testProduct     = "ent_01JB8Y6T2SA3F6H9J2K5M8N1P4" // product in ja
	testEnglishPage = "ent_01JB8Y6T2VS4W7Z2B5D8F1H6J9" // page in en
	testPost        = "ent_01JB8Y6T2WT6X9C3E6G9J2K5M8" // post in ja
	testImage       = "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5"
	testImage2      = "med_01JB8Z3K5RJ4W7Z1D5S8X2F6C9"
	testPDF         = "med_01JB8Z3K5TM8A1C5F9V3J6K2N7"
	testForm        = "frm_01JB8Z3K5VP1D4G7J2M5Q8T3W6"
	testWorksTerm   = "trm_01JB9A00000000000000000001" // works_category in ja
	testCategory    = "trm_01JB9A00000000000000000002" // category in ja
	testTag         = "trm_01JB9A00000000000000000003" // tag in ja
	testTag2        = "trm_01JB9A00000000000000000004" // tag in ja
	testEnglishTag  = "trm_01JB9A00000000000000000005" // tag in en
	testSeries      = "trm_01JB9A00000000000000000006" // series in ja
	testAuthor      = "aut_01JB9B00000000000000000001"
	testRevision    = "rev_01JB8X9Y3ZE5G8K1N4Q7S2V6W9"
	testGroup       = "tg_01JB8X4M7NB5D8F1H4J7K2M6P9"
	testUser        = "usr_01JB8T7H4MA2C5E8G1J4K7N3P6"
	testMissing     = "01JB9Z00000000000000000000" // after a prefix, the ID of an entity that testLookup does not find
)

var testRefs = map[string]Ref{
	testAboutPage:   {Type: TypePage, Language: "ja"},
	testPage:        {Type: TypePage, Language: "ja"},
	testProduct:     {Type: "product", Language: "ja"},
	testEnglishPage: {Type: TypePage, Language: "en"},
	testPost:        {Type: TypePost, Language: "ja"},
	testImage:       {Type: MediaImage},
	testImage2:      {Type: MediaImage},
	testPDF:         {Type: MediaPDF},
	testForm:        {},
	testWorksTerm:   {Type: "works_category", Language: "ja"},
	testCategory:    {Type: TaxonomyCategory, Language: "ja"},
	testTag:         {Type: TaxonomyTag, Language: "ja"},
	testTag2:        {Type: TaxonomyTag, Language: "ja"},
	testEnglishTag:  {Type: TaxonomyTag, Language: "en"},
	testSeries:      {Type: "series", Language: "ja"},
	testAuthor:      {},
}

func testLookup(kind RefKind, id string) (Ref, bool) {
	r, ok := testRefs[id]
	return r, ok && strings.HasPrefix(id, idPrefixes[kind])
}

func TestTestSite(t *testing.T) {
	if err := testSite().Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	for id := range testRefs {
		found := false
		for kind := range idPrefixes {
			if checkID(kind, id) == nil {
				found = true
			}
		}
		if !found {
			t.Errorf("invalid ID %q in testRefs", id)
		}
	}
}

// TestEveryFieldCoverage checks that testSite uses every type of field and every option.
func TestEveryFieldCoverage(t *testing.T) {
	types := make(map[FieldType]bool)
	options := make(map[string]bool)
	var walk func(fs []FieldDef)
	walk = func(fs []FieldDef) {
		for i := range fs {
			f := &fs[i]
			types[f.Type] = true
			for _, k := range f.options() {
				options[k] = true
			}
			if f.Label != "" {
				options["label"] = true
			}
			if f.Required {
				options["required"] = true
			}
			if f.Synced {
				options["translatable"] = true
			}
			walk(f.Fields)
			if f.Of != nil {
				walk([]FieldDef{*f.Of})
			}
		}
	}
	for _, typ := range testSite().Types {
		walk(typ.Fields)
	}
	for typ, opts := range fieldOptions {
		if !types[typ] {
			t.Errorf("no %s field", typ)
		}
		for _, k := range opts {
			if !options[k] {
				t.Errorf("no field with %s", k)
			}
		}
	}
	for _, k := range []string{"label", "required", "translatable"} {
		if !options[k] {
			t.Errorf("no field with %s", k)
		}
	}
}

func TestSiteType(t *testing.T) {
	s := testSite()
	tests := []struct {
		name string
		want *TypeDef
	}{
		{
			name: TypePost,
			want: &TypeDef{
				Name:       TypePost,
				Label:      "お知らせ",
				Body:       true,
				Taxonomies: []string{TaxonomyCategory, TaxonomyTag, "series"},
				Fields:     postFields,
			},
		},
		{name: TypePage, want: &TypeDef{Name: TypePage, Body: true}},
		{name: "product", want: &s.Types[1]},
		{name: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.Type(tt.name); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Type(%q) = %#v, want %#v", tt.name, got, tt.want)
			}
		})
	}
	if got := (&Site{}).Type(TypePost); !reflect.DeepEqual(got, &TypeDef{Name: TypePost, Body: true, Taxonomies: []string{TaxonomyCategory, TaxonomyTag}}) {
		t.Errorf("Type(post) of an empty site = %#v", got)
	}
	// The taxonomies of post are not shared between calls.
	s.Type(TypePost).Taxonomies[0] = "x"
	if got := s.Type(TypePost).Taxonomies[0]; got != TaxonomyCategory {
		t.Errorf("Type(post).Taxonomies[0] = %q after changing another result", got)
	}
}

func TestCheckLanguage(t *testing.T) {
	tests := []struct {
		in string
		ok bool
	}{
		{in: "ja", ok: true},
		{in: "en", ok: true},
		{in: "fil", ok: true},
		{in: "en-US", ok: true},
		{in: "es-419", ok: true},
		{in: "zh-Hant", ok: true},
		{in: "zh-Hant-TW", ok: true},
		{in: ""},
		{in: "j"},
		{in: "jpan"},
		{in: "japanese"},
		{in: "JA"},
		{in: "en-us"},
		{in: "en_US"},
		{in: "zh-hant"},
		{in: "zh-HANT"},
		{in: "zh-TW-Hant"},
		{in: "en-US-x-test"},
		{in: "de-CH-1996"},
		{in: "en-"},
		{in: " ja"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			msg := ""
			if !tt.ok {
				msg = `invalid language code "` + tt.in + `": use a language tag such as "ja", "en-US" or "zh-Hant"`
			}
			assertErrorMsg(t, checkLanguage(tt.in), msg)
		})
	}
}

func TestSiteValidate(t *testing.T) {
	// site returns testSite with its first type replaced by typ, if typ has a name.
	site := func(typ TypeDef) *Site {
		s := testSite()
		if typ.Name != "" {
			s.Types[0] = typ
		}
		return s
	}
	// fields returns a site with a type "t" of fs.
	fields := func(fs ...FieldDef) *Site { return site(TypeDef{Name: "t", Fields: fs}) }
	tests := []struct {
		name  string
		in    *Site
		paths []string
		msg   string // part of the error text, a "path: message" line for each problem; not checked if ""
	}{
		{name: "test site", in: testSite()},
		{name: "minimum", in: &Site{Languages: []string{"ja"}}},
		{name: "missing languages", in: &Site{}, paths: []string{"/languages"}, msg: "/languages: missing languages"},
		{
			name:  "invalid language",
			in:    &Site{Languages: []string{"ja", "EN"}},
			paths: []string{"/languages/1"},
			msg:   `/languages/1: invalid language code "EN"`,
		},
		{
			name:  "duplicate language",
			in:    &Site{Languages: []string{"ja", "en", "ja"}},
			paths: []string{"/languages/2"},
			msg:   `/languages/2: duplicate language "ja"`,
		},
		{
			name: "taxonomies",
			in: &Site{Languages: []string{"ja"}, Taxonomies: []TaxonomyDef{
				{Name: "a b"},
				{Name: TaxonomyTag},
				{Name: "x", Label: "a\nb"},
				{Name: "x"},
				{Name: ""},
			}},
			paths: []string{"/taxonomies/a b", "/taxonomies/tag", "/taxonomies/x/label", "/taxonomies/x", "/taxonomies/"},
			msg: `/taxonomies/a b: invalid name "a b": use ASCII letters, digits, "-" and "_"` + "\n" +
				"/taxonomies/tag: tag is a built-in taxonomy\n" +
				"/taxonomies/x/label: contains a line break or tab\n" +
				`/taxonomies/x: duplicate taxonomy "x"` + "\n" +
				"/taxonomies/: empty name",
		},
		{
			name:  "escaped name",
			in:    &Site{Languages: []string{"ja"}, Types: []TypeDef{{Name: "a/b~c"}}},
			paths: []string{"/types/a~1b~0c"},
			msg:   `/types/a~1b~0c: invalid name "a/b~c"`,
		},
		{
			name: "types",
			in: &Site{Languages: []string{"ja"}, Types: []TypeDef{
				{Name: "a", Label: "a\tb"},
				{Name: "a"},
				{Name: TypePost, Taxonomies: []string{TaxonomyTag, "unknown"}},
				{Name: TypePage, Taxonomies: []string{TaxonomyCategory, TaxonomyTag, TaxonomyCategory}},
			}},
			paths: []string{"/types/a/label", "/types/a", "/types/post/taxonomies/0", "/types/post/taxonomies/1", "/types/page/taxonomies/2"},
			msg: "/types/a/label: contains a line break or tab\n" +
				`/types/a: duplicate type "a"` + "\n" +
				"/types/post/taxonomies/0: post always uses tag\n" +
				`/types/post/taxonomies/1: unknown taxonomy "unknown"` + "\n" +
				`/types/page/taxonomies/2: duplicate taxonomy "category"`,
		},
		{
			name:  "field names",
			in:    fields(FieldDef{Name: "a b", Type: FieldText}, FieldDef{Name: "x", Type: FieldText}, FieldDef{Name: "x", Type: FieldText}),
			paths: []string{"/types/t/fields/a b", "/types/t/fields/x"},
			msg:   `/types/t/fields/a b: invalid name "a b": use ASCII letters, digits, "-" and "_"` + "\n" + `/types/t/fields/x: duplicate field "x"`,
		},
		{
			name:  "missing type",
			in:    fields(FieldDef{Name: "x", MaxLength: 1}),
			paths: []string{"/types/t/fields/x/type"},
			msg:   "/types/t/fields/x/type: missing type",
		},
		{
			name:  "unknown type",
			in:    fields(FieldDef{Name: "x", Type: "color", Required: true, Synced: true, MaxLength: -1}),
			paths: []string{"/types/t/fields/x/type"},
			msg:   `/types/t/fields/x/type: unknown field type "color"`,
		},
		{
			name:  "label",
			in:    fields(FieldDef{Name: "x", Type: FieldText, Label: "a\nb"}),
			paths: []string{"/types/t/fields/x/label"},
			msg:   "/types/t/fields/x/label: contains a line break or tab",
		},
		{
			name:  "required boolean",
			in:    fields(FieldDef{Name: "x", Type: FieldBoolean, Required: true}),
			paths: []string{"/types/t/fields/x/required"},
			msg:   "/types/t/fields/x/required: not allowed for boolean fields",
		},
		{
			name: "options of other types",
			in: fields(FieldDef{
				Name: "x", Type: FieldBoolean, MinLength: 1, MaxLength: 2, Min: new(1.0), Max: new(2.0), Options: []string{"a"}, Multiple: true,
				MinItems: 1, MaxItems: 2, Accept: []string{MediaImage}, Types: []string{TypePage}, Taxonomy: TaxonomyTag,
				Fields: []FieldDef{{Name: "y", Type: FieldText}}, Of: &FieldDef{Type: FieldText},
			}),
			paths: []string{
				"/types/t/fields/x/minLength", "/types/t/fields/x/maxLength", "/types/t/fields/x/min", "/types/t/fields/x/max",
				"/types/t/fields/x/options", "/types/t/fields/x/multiple", "/types/t/fields/x/minItems", "/types/t/fields/x/maxItems",
				"/types/t/fields/x/accept", "/types/t/fields/x/types", "/types/t/fields/x/taxonomy", "/types/t/fields/x/fields",
				"/types/t/fields/x/of",
			},
			msg: "/types/t/fields/x/minLength: not allowed for boolean fields",
		},
		{
			name: "lengths",
			in: fields(
				FieldDef{Name: "a", Type: FieldText, MinLength: 3, MaxLength: 2},
				FieldDef{Name: "b", Type: FieldTextarea, MinLength: -1, MaxLength: -2},
				FieldDef{Name: "c", Type: FieldText, MinLength: 3},
				FieldDef{Name: "d", Type: FieldTextarea, MinLength: 2, MaxLength: 2},
			),
			paths: []string{"/types/t/fields/a/minLength", "/types/t/fields/b/minLength", "/types/t/fields/b/maxLength"},
			msg: "/types/t/fields/a/minLength: minLength 3 is greater than maxLength 2\n" +
				"/types/t/fields/b/minLength: minLength -1 is negative\n" +
				"/types/t/fields/b/maxLength: maxLength -2 is negative",
		},
		{
			name: "number range",
			in: fields(
				FieldDef{Name: "a", Type: FieldNumber, Min: new(2.0), Max: new(1.0)},
				FieldDef{Name: "b", Type: FieldNumber, Min: new(-1.5), Max: new(-1.5)},
				FieldDef{Name: "c", Type: FieldNumber, Min: new(nan()), Max: new(inf())},
			),
			paths: []string{"/types/t/fields/a/min", "/types/t/fields/c/min", "/types/t/fields/c/max"},
			msg: "/types/t/fields/a/min: min 2 is greater than max 1\n" +
				"/types/t/fields/c/min: min must be a finite number\n" +
				"/types/t/fields/c/max: max must be a finite number",
		},
		{
			name: "select",
			in: fields(
				FieldDef{Name: "a", Type: FieldSelect},
				FieldDef{Name: "b", Type: FieldSelect, Options: []string{"x", "", "x", "y\tz", "e" + acute}},
			),
			paths: []string{"/types/t/fields/a/options", "/types/t/fields/b/options/1", "/types/t/fields/b/options/2", "/types/t/fields/b/options/3", "/types/t/fields/b/options/4"},
			msg: "/types/t/fields/a/options: missing options\n" +
				"/types/t/fields/b/options/1: empty option\n" +
				`/types/t/fields/b/options/2: duplicate option "x"` + "\n" +
				"/types/t/fields/b/options/3: contains a line break or tab\n" +
				"/types/t/fields/b/options/4: not in Unicode NFC; normalize it first",
		},
		{
			name: "items",
			in: fields(
				FieldDef{Name: "a", Type: FieldSelect, Options: []string{"x"}, MinItems: 1, MaxItems: 2},
				FieldDef{Name: "b", Type: FieldEntry, Multiple: true, MinItems: 3, MaxItems: 2},
				FieldDef{Name: "c", Type: FieldList, Of: &FieldDef{Type: FieldText}, MinItems: -1},
				FieldDef{Name: "d", Type: FieldTerm, Taxonomy: TaxonomyTag, Multiple: true, MaxItems: 1},
			),
			paths: []string{"/types/t/fields/a/minItems", "/types/t/fields/a/maxItems", "/types/t/fields/b/minItems", "/types/t/fields/c/minItems"},
			msg: "/types/t/fields/a/minItems: minItems needs multiple\n" +
				"/types/t/fields/a/maxItems: maxItems needs multiple\n" +
				"/types/t/fields/b/minItems: minItems 3 is greater than maxItems 2\n" +
				"/types/t/fields/c/minItems: minItems -1 is negative",
		},
		{
			name:  "accept",
			in:    fields(FieldDef{Name: "a", Type: FieldMedia, Accept: []string{MediaImage, "video", MediaImage}}),
			paths: []string{"/types/t/fields/a/accept/1", "/types/t/fields/a/accept/2"},
			msg: `/types/t/fields/a/accept/1: unknown media kind "video" (use image, pdf)` + "\n" +
				`/types/t/fields/a/accept/2: duplicate media kind "image"`,
		},
		{
			name:  "entry types",
			in:    fields(FieldDef{Name: "a", Type: FieldEntry, Types: []string{TypePost, "product", "news", "product"}}),
			paths: []string{"/types/t/fields/a/types/2", "/types/t/fields/a/types/3"},
			msg:   `/types/t/fields/a/types/2: unknown type "news"` + "\n" + `/types/t/fields/a/types/3: duplicate type "product"`,
		},
		{
			name:  "taxonomy",
			in:    fields(FieldDef{Name: "a", Type: FieldTerm}, FieldDef{Name: "b", Type: FieldTerm, Taxonomy: "unknown"}),
			paths: []string{"/types/t/fields/a/taxonomy", "/types/t/fields/b/taxonomy"},
			msg:   "/types/t/fields/a/taxonomy: missing taxonomy\n" + `/types/t/fields/b/taxonomy: unknown taxonomy "unknown"`,
		},
		{
			name: "group",
			in: fields(
				FieldDef{Name: "a", Type: FieldGroup},
				FieldDef{Name: "b", Type: FieldGroup, Fields: []FieldDef{{Name: "c", Type: FieldText, MaxLength: -1}, {Name: "c", Type: FieldText}}},
			),
			paths: []string{"/types/t/fields/a/fields", "/types/t/fields/b/fields/c/maxLength", "/types/t/fields/b/fields/c"},
			msg: "/types/t/fields/a/fields: missing fields\n" +
				"/types/t/fields/b/fields/c/maxLength: maxLength -1 is negative\n" +
				`/types/t/fields/b/fields/c: duplicate field "c"`,
		},
		{
			name: "list",
			in: fields(
				FieldDef{Name: "a", Type: FieldList},
				FieldDef{Name: "b", Type: FieldList, Of: &FieldDef{Name: "x", Type: FieldText, Required: true}},
				FieldDef{Name: "c", Type: FieldList, Of: &FieldDef{Type: FieldList, Of: &FieldDef{Type: "x"}}},
			),
			paths: []string{"/types/t/fields/a/of", "/types/t/fields/b/of/name", "/types/t/fields/b/of/required", "/types/t/fields/c/of/of/type"},
			msg: "/types/t/fields/a/of: missing of\n" +
				"/types/t/fields/b/of/name: not allowed for the items of a list\n" +
				"/types/t/fields/b/of/required: not allowed for the items of a list\n" +
				`/types/t/fields/c/of/of/type: unknown field type "x"`,
		},
		{
			name: "translatable",
			in: fields(
				FieldDef{Name: "a", Type: FieldText, Synced: true},
				FieldDef{Name: "b", Type: FieldGroup, Synced: true, Fields: []FieldDef{{Name: "c", Type: FieldMedia}, {Name: "d", Type: FieldList, Of: &FieldDef{Type: FieldDate}}}},
				FieldDef{Name: "e", Type: FieldEntry, Synced: true},
				FieldDef{Name: "f", Type: FieldGroup, Synced: true, Fields: []FieldDef{{Name: "g", Type: FieldLink}}},
				FieldDef{Name: "h", Type: FieldList, Synced: true, Of: &FieldDef{Type: FieldTerm, Taxonomy: TaxonomyTag}},
				FieldDef{Name: "i", Type: FieldGroup, Fields: []FieldDef{{Name: "j", Type: FieldText, Synced: true}}},
				FieldDef{Name: "k", Type: FieldList, Of: &FieldDef{Type: FieldText, Synced: true}},
				FieldDef{Name: "l", Type: FieldRichText, Synced: true},
				FieldDef{Name: "m", Type: FieldBlocks, Synced: true},
				FieldDef{Name: "n", Type: FieldList, Synced: true},
			),
			paths: []string{
				"/types/t/fields/e/translatable", "/types/t/fields/f/translatable", "/types/t/fields/h/translatable",
				"/types/t/fields/i/fields/j/translatable", "/types/t/fields/k/of/translatable", "/types/t/fields/l/translatable",
				"/types/t/fields/m/translatable", "/types/t/fields/n/translatable", "/types/t/fields/n/of",
			},
			msg: "/types/t/fields/e/translatable: entry fields cannot be untranslatable\n" +
				"/types/t/fields/f/translatable: group fields cannot be untranslatable\n" +
				"/types/t/fields/h/translatable: list fields cannot be untranslatable\n" +
				"/types/t/fields/i/fields/j/translatable: only the fields of a type can be untranslatable\n" +
				"/types/t/fields/k/of/translatable: only the fields of a type can be untranslatable\n" +
				"/types/t/fields/l/translatable: richText fields cannot be untranslatable\n" +
				"/types/t/fields/m/translatable: blocks fields cannot be untranslatable\n" +
				"/types/t/fields/n/translatable: list fields cannot be untranslatable\n" +
				"/types/t/fields/n/of: missing of",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.in.Validate()
			if tt.paths == nil && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if got := errorPaths(t, err); !reflect.DeepEqual(got, tt.paths) {
				t.Errorf("Validate() paths = %q, want %q", got, tt.paths)
			}
			if tt.msg != "" {
				assertErrorMsg(t, err, tt.msg)
			}
		})
	}
}

func nan() float64 { return math.NaN() }
func inf() float64 { return math.Inf(1) }
