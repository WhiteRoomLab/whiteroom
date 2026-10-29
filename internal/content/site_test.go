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
		Name:            Localized{"ja": "株式会社〇〇", "en": "Example Inc."},
		URL:             "https://example.com",
		TimeZone:        "Asia/Tokyo",
		Languages:       []string{"ja", "en"},
		DefaultLanguage: "ja",
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

// minimalSite returns a site with only the required items, in one language.
func minimalSite() *Site {
	return &Site{
		Name:            Localized{"ja": "株式会社〇〇"},
		URL:             "https://example.com",
		TimeZone:        "UTC",
		Languages:       []string{"ja"},
		DefaultLanguage: "ja",
	}
}

// exampleSite returns the site settings of the example in CONCEPT.md 6.11.9: a site in Japanese and English whose posts
// have the URL rule of "Day and name" in the permalink settings of WordPress.
func exampleSite() *Site {
	s := testSite()
	works := *s.Type("works")
	works.URL, works.Archive = "/works/{slug}/", "/works/"
	return &Site{
		Name:            Localized{"ja": "株式会社〇〇", "en": "Example Inc."},
		Tagline:         Localized{"ja": "Webサイトの企画と制作"},
		URL:             "https://example.com",
		TimeZone:        "Asia/Tokyo",
		Logo:            testLogo,
		Icon:            testIcon,
		Languages:       []string{"ja", "en"},
		DefaultLanguage: "ja",
		FrontPage:       Localized{"ja": testHomePage},
		PostsPage:       Localized{"ja": testNewsPage},
		SEO: SiteSEO{
			TitleFormat: Localized{"ja": "{title}｜{siteName}", "en": "{title} | {siteName}"},
			OGImage:     Localized{"ja": testOGImage},
		},
		Types:      []TypeDef{{Name: TypePost, URL: "/{yyyy}/{mm}/{dd}/{slug}/"}, works},
		Taxonomies: []TaxonomyDef{*s.Taxonomy("works_category")},
	}
}

// IDs of the entities that testLookup finds, and of others.
const (
	testAboutPage          = "ent_01JB8W2N5KD3F6H9J1M4P7R2T5" // page in ja
	testPage               = "ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3" // page in ja
	testProduct            = "ent_01JB8Y6T2SA3F6H9J2K5M8N1P4" // product in ja
	testEnglishPage        = "ent_01JB8Y6T2VS4W7Z2B5D8F1H6J9" // page in en
	testPost               = "ent_01JB8Y6T2WT6X9C3E6G9J2K5M8" // post in ja
	testPrivacyPage        = "ent_01JBD1A2B3C4D5E6F7G8H9J0K1" // page in ja
	testEnglishPrivacyPage = "ent_01JBD1M2N3P4Q5R6S7T8V9W0X1" // page in en
	testContactPage        = "ent_01JBD1Y2Z3A4B5C6D7E8F9G0H1" // page in ja
	testHomePage           = "ent_01JBG3N6S1V4X7Z0C3E6G9J2K5" // page in ja
	testNewsPage           = "ent_01JBG3N6T2W5Y8B1D4F7H0K3M6" // page in ja
	testImage              = "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5"
	testImage2             = "med_01JB8Z3K5RJ4W7Z1D5S8X2F6C9"
	testLogo               = "med_01JBG3N6V3X6Z9C2E5G8J1K4M7"
	testIcon               = "med_01JBG3N6W4Y7B0D3F6H9K2M5P8"
	testOGImage            = "med_01JBG3N6X5Z8C1E4G7J0K3N6Q9"
	testPDF                = "med_01JB8Z3K5TM8A1C5F9V3J6K2N7"
	testForm               = "frm_01JB8Z3K5VP1D4G7J2M5Q8T3W6"
	testWorksTerm          = "trm_01JB9A00000000000000000001" // works_category in ja
	testCategory           = "trm_01JB9A00000000000000000002" // category in ja
	testTag                = "trm_01JB9A00000000000000000003" // tag in ja
	testTag2               = "trm_01JB9A00000000000000000004" // tag in ja
	testEnglishTag         = "trm_01JB9A00000000000000000005" // tag in en
	testSeries             = "trm_01JB9A00000000000000000006" // series in ja
	testNewsCategory       = "trm_01JBA4C7E2G5J8K1M4P7R2T5W8" // category in ja
	testEnglishCategory    = "trm_01JB9A00000000000000000008" // category in en
	testAuthor             = "aut_01JB9B00000000000000000001"
	testRevision           = "rev_01JB8X9Y3ZE5G8K1N4Q7S2V6W9"
	testGroup              = "tg_01JB8X4M7NB5D8F1H4J7K2M6P9"
	testUser               = "usr_01JB8T7H4MA2C5E8G1J4K7N3P6"
	testMissing            = "01JB9Z00000000000000000000" // after a prefix, the ID of an entity that testLookup does not find
)

var testRefs = map[string]Ref{
	testAboutPage:            {Type: TypePage, Language: "ja"},
	testPage:                 {Type: TypePage, Language: "ja"},
	testProduct:              {Type: "product", Language: "ja"},
	testEnglishPage:          {Type: TypePage, Language: "en"},
	testPost:                 {Type: TypePost, Language: "ja"},
	testPrivacyPage:          {Type: TypePage, Language: "ja"},
	testEnglishPrivacyPage:   {Type: TypePage, Language: "en"},
	testContactPage:          {Type: TypePage, Language: "ja"},
	testHomePage:             {Type: TypePage, Language: "ja"},
	testNewsPage:             {Type: TypePage, Language: "ja"},
	testImage:                {Type: MediaImage},
	testImage2:               {Type: MediaImage},
	testLogo:                 {Type: MediaImage},
	testIcon:                 {Type: MediaImage},
	testOGImage:              {Type: MediaImage},
	testPDF:                  {Type: MediaPDF},
	testForm:                 {},
	testWorksTerm:            {Type: "works_category", Language: "ja"},
	testCategory:             {Type: TaxonomyCategory, Language: "ja"},
	testTag:                  {Type: TaxonomyTag, Language: "ja"},
	testTag2:                 {Type: TaxonomyTag, Language: "ja"},
	testEnglishTag:           {Type: TaxonomyTag, Language: "en"},
	testSeries:               {Type: "series", Language: "ja"},
	testNewsCategory:         {Type: TaxonomyCategory, Language: "ja"},
	testPressReleaseCategory: {Type: TaxonomyCategory, Language: "ja"},
	testEventTag:             {Type: TaxonomyTag, Language: "ja"},
	testEnglishEventTag:      {Type: TaxonomyTag, Language: "en"},
	testEnglishCategory:      {Type: TaxonomyCategory, Language: "en"},
	testAuthor:               {},
}

func testLookup(kind RefKind, id string) (Ref, bool) {
	r, ok := testRefs[id]
	return r, ok && strings.HasPrefix(id, idPrefixes[kind])
}

func TestTestSite(t *testing.T) {
	if err := testSite().Validate(testLookup); err != nil {
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

func TestExampleSite(t *testing.T) {
	if err := exampleSite().Validate(testLookup); err != nil {
		t.Fatalf("Validate() = %v", err)
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
	// post and page have the URL rules of their definitions, but not the archives, which they cannot have.
	rules := &Site{Types: []TypeDef{
		{Name: TypePost, URL: "/archives/{number}/", Archive: "/news/"},
		{Name: TypePage, URL: "/{path}.html", Archive: "/pages/"},
	}}
	wantPost := &TypeDef{Name: TypePost, URL: "/archives/{number}/", Body: true, Taxonomies: []string{TaxonomyCategory, TaxonomyTag}}
	if got := rules.Type(TypePost); !reflect.DeepEqual(got, wantPost) {
		t.Errorf("Type(post) of a site with its URL rule = %#v, want %#v", got, wantPost)
	}
	wantPage := &TypeDef{Name: TypePage, URL: "/{path}.html", Body: true}
	if got := rules.Type(TypePage); !reflect.DeepEqual(got, wantPage) {
		t.Errorf("Type(page) of a site with its URL rule = %#v, want %#v", got, wantPage)
	}
	// The taxonomies of post are not shared between calls.
	s.Type(TypePost).Taxonomies[0] = "x"
	if got := s.Type(TypePost).Taxonomies[0]; got != TaxonomyCategory {
		t.Errorf("Type(post).Taxonomies[0] = %q after changing another result", got)
	}
}

func TestSiteTaxonomy(t *testing.T) {
	s := testSite()
	tests := []struct {
		name string
		want *TaxonomyDef
	}{
		{name: TaxonomyCategory, want: &TaxonomyDef{Name: TaxonomyCategory, Hierarchical: true}},
		{name: TaxonomyTag, want: &TaxonomyDef{Name: TaxonomyTag}},
		{name: "series", want: &s.Taxonomies[1]},
		{name: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.Taxonomy(tt.name); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Taxonomy(%q) = %#v, want %#v", tt.name, got, tt.want)
			}
		})
	}
	// The definition of tag gives the label and the URL rule, but not Hierarchical, which Validate reports.
	custom := &Site{Taxonomies: []TaxonomyDef{{Name: TaxonomyTag, Label: "タグ", URL: "/tags/{slug}/", Hierarchical: true}}}
	want := &TaxonomyDef{Name: TaxonomyTag, Label: "タグ", URL: "/tags/{slug}/"}
	if got := custom.Taxonomy(TaxonomyTag); !reflect.DeepEqual(got, want) {
		t.Errorf("Taxonomy(tag) of a site with a definition of tag = %#v, want %#v", got, want)
	}
	// The built-in definitions are not shared between calls.
	s.Taxonomy(TaxonomyCategory).Hierarchical = false
	if !s.Taxonomy(TaxonomyCategory).Hierarchical {
		t.Error("Taxonomy(category) is not hierarchical after changing another result")
	}
}

func TestURLRule(t *testing.T) {
	s := testSite()
	tests := []struct {
		name string
		rule string
		want string
	}{
		{name: "post", rule: s.Type(TypePost).URLRule(), want: DefaultPostURL},
		{name: "page", rule: s.Type(TypePage).URLRule(), want: DefaultPageURL},
		{name: "custom type", rule: s.Type("works").URLRule(), want: "/works/{slug}/"},
		{name: "post with a rule", rule: (&TypeDef{Name: TypePost, URL: "/archives/{number}/"}).URLRule(), want: "/archives/{number}/"},
		{name: "custom type with a rule", rule: (&TypeDef{Name: "works", URL: "/実績/{slug}/"}).URLRule(), want: "/実績/{slug}/"},
		{name: "category", rule: s.Taxonomy(TaxonomyCategory).URLRule(), want: DefaultCategoryURL},
		{name: "tag", rule: s.Taxonomy(TaxonomyTag).URLRule(), want: DefaultTagURL},
		// As in WordPress, the default of a custom taxonomy has no parents.
		{name: "hierarchical custom taxonomy", rule: s.Taxonomy("series").URLRule(), want: "/series/{slug}/"},
		{name: "category with a rule", rule: (&TaxonomyDef{Name: TaxonomyCategory, URL: "/topics/{path}/"}).URLRule(), want: "/topics/{path}/"},
		{name: "author pages", rule: s.AuthorURLRule(), want: DefaultAuthorURL},
		{name: "author pages with a rule", rule: (&Site{AuthorURL: "/writers/{slug}/"}).AuthorURLRule(), want: "/writers/{slug}/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.rule != tt.want {
				t.Errorf("URL rule = %q, want %q", tt.rule, tt.want)
			}
		})
	}
}

func TestLanguagePath(t *testing.T) {
	s := &Site{Languages: []string{"ja", "en-US", "zh-Hant"}, DefaultLanguage: "ja"}
	prefixed := &Site{Languages: s.Languages, DefaultLanguage: "ja", PrefixDefaultLanguage: true}
	tests := []struct {
		name     string
		site     *Site
		language string
		want     string
	}{
		{name: "default language", site: s, language: "ja", want: "/"},
		{name: "region", site: s, language: "en-US", want: "/en-us/"},
		{name: "script", site: s, language: "zh-Hant", want: "/zh-hant/"},
		{name: "default language with a prefix", site: prefixed, language: "ja", want: "/ja/"},
		{name: "another language with a prefix for the default", site: prefixed, language: "en-US", want: "/en-us/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.site.LanguagePath(tt.language); got != tt.want {
				t.Errorf("LanguagePath(%q) = %q, want %q", tt.language, got, tt.want)
			}
		})
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

func TestCheckSiteURL(t *testing.T) {
	const (
		normalize     = "contains control characters or surrounding spaces; normalize it first"
		web           = "must be an http or https URL"
		nonASCII      = "the host must be ASCII; write a domain name in the xn-- form"
		queryFragment = "queries and fragments are not allowed"
		trailingSlash = `must not end with "/"`
		segments      = `the path cannot have empty, "." or ".." segments`
	)
	tests := []struct {
		name string
		in   string
		msg  string
	}{
		{name: "https", in: "https://example.com"},
		{name: "http and port", in: "http://localhost:8080"},
		{name: "path", in: "https://example.com/blog"},
		{name: "path of two segments", in: "https://example.com/a/b"},
		{name: "Japanese path", in: "https://example.com/ブログ"},
		{name: "IDN in the xn-- form", in: "https://xn--eckwd4c7c.xn--zckzah"}, // ドメイン.テスト
		{name: "IPv6 and port", in: "https://[::1]:8443"},
		{name: "lowest port", in: "https://example.com:1"},
		{name: "highest port", in: "https://example.com:65535"},
		{name: "default port of the other scheme", in: "http://example.com:443"},

		{name: "empty", in: "", msg: "empty URL"},
		{name: "host without scheme", in: "example.com", msg: `must be an absolute URL or a path starting with "/"`},
		{name: "path only", in: "/blog", msg: web},
		{name: "ftp", in: "ftp://example.com", msg: `scheme "ftp" is not allowed`},
		{name: "mailto", in: "mailto:a@example.com", msg: web},
		{name: "without host", in: "https://", msg: "missing host"},
		{name: "port without host", in: "https://:8080", msg: "missing host"},
		{name: "colon without host", in: "https://:", msg: "missing host"},
		{name: "credentials", in: "https://user:pass@example.com", msg: "credentials in URLs are not allowed"},
		{name: "protocol-relative", in: "//example.com", msg: "protocol-relative URLs are not allowed"},
		{name: "leading space", in: " https://example.com", msg: normalize},
		{name: "trailing space", in: "https://example.com ", msg: normalize},
		{name: "upper case scheme", in: "HTTPS://example.com", msg: "the scheme must be in lower case"},
		{name: "upper case host", in: "https://Example.com", msg: "the host must be in lower case"},
		{name: "Japanese host", in: "https://例え.jp", msg: nonASCII},
		{name: "percent-encoded host", in: "https://%E4%BE%8B.jp", msg: nonASCII},
		{name: "empty port", in: "https://example.com:", msg: `empty port; remove the ":"`},
		{name: "port 0", in: "https://example.com:0", msg: "port 0 is out of range (1 to 65535)"},
		{name: "port 65536", in: "https://example.com:65536", msg: "port 65536 is out of range (1 to 65535)"},
		{
			name: "port that overflows int",
			in:   "https://example.com:99999999999999999999",
			msg:  "port 99999999999999999999 is out of range (1 to 65535)",
		},
		{name: "default port of https", in: "https://example.com:443", msg: "port 443 is the default for https; omit it"},
		{name: "default port of http", in: "http://localhost:80", msg: "port 80 is the default for http; omit it"},
		{name: "default port with a leading zero", in: "https://example.com:0443", msg: "port 0443 is the default for https; omit it"},
		{name: "query", in: "https://example.com?x=1", msg: queryFragment},
		{name: "empty query", in: "https://example.com/?", msg: queryFragment},
		{name: "query ending with a slash", in: "https://example.com/blog?next=/", msg: queryFragment},
		{name: "fragment", in: "https://example.com#top", msg: queryFragment},
		{name: "empty fragment", in: "https://example.com#", msg: queryFragment},
		{name: "trailing slash", in: "https://example.com/", msg: trailingSlash},
		{name: "path with a trailing slash", in: "https://example.com/blog/", msg: trailingSlash},
		{name: "empty segment", in: "https://example.com//blog", msg: segments},
		{name: "dot segment", in: "https://example.com/./blog", msg: segments},
		{name: "dot-dot segment", in: "https://example.com/a/../blog", msg: segments},
		{name: "percent-encoded dot-dot segment", in: "https://example.com/%2E%2E/blog", msg: segments},
		{name: "dots in a segment", in: "https://example.com/a..b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertErrorMsg(t, checkSiteURL(tt.in), tt.msg)
		})
	}
}

// TestCheckTimeZone does not try names in another case, which the time zone data of macOS may have.
func TestCheckTimeZone(t *testing.T) {
	tests := []struct {
		in string
		ok bool
	}{
		{in: "Asia/Tokyo", ok: true},
		{in: "UTC", ok: true},
		{in: "America/New_York", ok: true},
		{in: "Etc/GMT-9", ok: true},
		{in: "Local"},
		{in: ""},
		{in: "Tokyo"},
		{in: "Asia/Tokio"},
		{in: "../etc/passwd"},
		{in: "/etc/localtime"},
		{in: "+09:00"},
		// The files of the system may have these, but the time zone database does not.
		{in: "Asia/./Tokyo"},
		{in: "Asia//Tokyo"},
		{in: "Asia/Tokyo/"},
		{in: " Asia/Tokyo"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			msg := ""
			if !tt.ok {
				msg = `unknown time zone "` + tt.in + `": use an IANA name such as "Asia/Tokyo" or "UTC"`
			}
			assertErrorMsg(t, checkTimeZone(tt.in), msg)
		})
	}
}

func TestCheckTitleFormat(t *testing.T) {
	const (
		missing   = "missing {title}"
		use       = " (use {title} and {siteName})"
		lineBreak = "contains a line break or tab"
	)
	tests := []struct {
		name string
		in   string
		msg  string
	}{
		{name: "default", in: DefaultTitleFormat},
		{name: "title only", in: "{title}"},
		{name: "site name first", in: "{siteName} - {title}"},
		{name: "brackets", in: "【{siteName}】{title}"},
		{name: "full-width bar", in: "{title}｜{siteName}"},
		{name: "surrounding spaces", in: " {title} | {siteName} "},

		{name: "empty", in: "", msg: missing},
		{name: "site name only", in: "{siteName}", msg: missing},
		{name: "title twice", in: "{title}{title}", msg: "{title} can be used only once"},
		{name: "site name twice", in: "{title} {siteName} {siteName}", msg: "{siteName} can be used only once"},
		{name: "unknown placeholder", in: "{title} {name}", msg: `unknown placeholder "{name}"` + use},
		{name: "placeholder in another case", in: "{Title}", msg: `unknown placeholder "{Title}"` + use},
		{name: "spaces in a placeholder", in: "{ title }", msg: `unknown placeholder "{ title }"` + use},
		{name: "empty placeholder", in: "{title}{}", msg: `unknown placeholder "{}"` + use},
		{name: "brace in a placeholder", in: "{{title}}", msg: `unknown placeholder "{{title}"` + use},
		{name: "unclosed brace", in: "{title} {", msg: `unclosed "{"`},
		{name: "unclosed placeholder", in: "{title", msg: `unclosed "{"`},
		{name: "closing brace", in: "{title} }", msg: `unexpected "}"`},
		{name: "closing brace first", in: "}{title}", msg: `unexpected "}"`},
		// The braces are checked before the counts.
		{name: "title twice and an unclosed brace", in: "{title}{title}{", msg: `unclosed "{"`},
		{name: "line break", in: "{title}\n", msg: lineBreak},
		{name: "tab", in: "{title}\t{siteName}", msg: lineBreak},
		{name: "control character", in: "{title}\x00", msg: "contains control characters; normalize it first"},
		{name: "not NFC", in: "{title} | Cafe" + acute, msg: "not in Unicode NFC; normalize it first"},
		{name: "invalid UTF-8", in: "{title}\xff", msg: "invalid UTF-8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertErrorMsg(t, checkTitleFormat(tt.in), tt.msg)
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
	// edit returns s changed by f.
	edit := func(s *Site, f func(s *Site)) *Site {
		f(s)
		return s
	}
	// pages returns testSite with front for the front pages and posts for the posts pages.
	pages := func(front, posts Localized) *Site {
		return edit(testSite(), func(s *Site) { s.FrontPage, s.PostsPage = front, posts })
	}
	tests := []struct {
		name     string
		in       *Site
		noLookup bool // validate without lookup
		paths    []string
		msg      string // part of the error text, a "path: message" line for each problem; not checked if ""
	}{
		{name: "test site", in: testSite()},
		{name: "test site without lookup", in: testSite(), noLookup: true},
		{name: "minimum", in: minimalSite()},
		{
			name: "general settings",
			in: edit(testSite(), func(s *Site) {
				s.Tagline = Localized{"ja": "暮らしに寄り添う道具をつくる", "en": "Tools for everyday life"}
				s.Logo, s.Icon = testImage, testImage2
			}),
		},
		{
			name:  "empty",
			in:    &Site{},
			paths: []string{"/name", "/url", "/timeZone", "/languages", "/defaultLanguage"},
			msg: "/name: missing name\n/url: missing url\n/timeZone: missing timeZone\n/languages: missing languages\n" +
				"/defaultLanguage: missing defaultLanguage",
		},
		{
			name:  "missing items",
			in:    &Site{Languages: []string{"ja"}},
			paths: []string{"/name", "/url", "/timeZone", "/defaultLanguage"},
			msg:   "/name: missing name\n/url: missing url\n/timeZone: missing timeZone\n/defaultLanguage: missing defaultLanguage",
		},
		{
			// The errors are in document order.
			name: "invalid items",
			in: edit(testSite(), func(s *Site) {
				s.Name = Localized{"ja": "株式会社\t〇〇", "de": "Beispiel GmbH"}
				s.Tagline = Localized{"en": "a\nb"}
				s.URL, s.TimeZone, s.Logo, s.Icon, s.DefaultLanguage = "https://example.com/", "Local", testPDF, testAboutPage, "fr"
				s.FrontPage, s.PostsPage, s.PostsPerPage = Localized{"ja": testPost}, Localized{"en": testEnglishPage}, -1
				s.AuthorURL = "/writers/"
				s.SEO = SiteSEO{TitleFormat: Localized{"ja": "{siteName}"}, OGImage: Localized{"en": testPDF}}
				s.Taxonomies[0].Label, s.Taxonomies[0].URL = "a\tb", "/kinds/{path}/"
				s.Types[0].URL, s.Types[0].Archive = "/works/{slug}", "/"
			}),
			paths: []string{
				"/name/ja", "/name/en", "/name/de", "/tagline/en", "/url", "/timeZone", "/logo", "/icon", "/defaultLanguage",
				"/frontPage/ja", "/postsPage/en", "/postsPerPage", "/authorUrl", "/seo/titleFormat/ja", "/seo/ogImage/en",
				"/taxonomies/works_category/label", "/taxonomies/works_category/url", "/types/works/url", "/types/works/archive",
			},
			msg: "/name/ja: contains a line break or tab\n/name/en: missing value; every language of the site needs one\n" +
				`/name/de: language "de" is not in the site settings (use ja, en)` + "\n" +
				"/tagline/en: contains a line break or tab\n" + `/url: must not end with "/"` + "\n" +
				`/timeZone: unknown time zone "Local": use an IANA name such as "Asia/Tokyo" or "UTC"` + "\n" +
				`/logo: media "` + testPDF + `" (pdf) is not accepted here (accept: image)` + "\n" +
				`/icon: invalid media ID "` + testAboutPage + `": use "med_" and a ULID (26 characters in upper case)` + "\n" +
				`/defaultLanguage: language "fr" is not in the site settings (use ja, en)` + "\n" +
				`/frontPage/ja: entry "` + testPost + `" is of the type "post" (use page)` + "\n" +
				"/postsPage/en: not allowed while the front page of en shows the latest posts; set frontPage\n" +
				"/postsPerPage: postsPerPage -1 is out of range (1 to 100)\n" +
				`/authorUrl: invalid URL rule "/writers/": missing {slug}` + "\n" +
				"/seo/titleFormat/ja: missing {title}\n" +
				`/seo/ogImage/en: media "` + testPDF + `" (pdf) is not accepted here (accept: image)` + "\n" +
				"/taxonomies/works_category/label: contains a line break or tab\n" +
				`/taxonomies/works_category/url: invalid URL rule "/kinds/{path}/": placeholder "{path}" is not allowed for works_category (use {slug})` + "\n" +
				`/types/works/url: invalid URL rule "/works/{slug}": must end with "/" or ".html"` + "\n" +
				`/types/works/archive: invalid archive path "/": cannot be "/", the path of the front page`,
		},

		{
			name:  "name in one language",
			in:    edit(testSite(), func(s *Site) { delete(s.Name, "en") }),
			paths: []string{"/name/en"},
			msg:   "/name/en: missing value; every language of the site needs one",
		},
		{
			name:  "line break in the name",
			in:    edit(testSite(), func(s *Site) { s.Name["ja"] = "株式会社\n〇〇" }),
			paths: []string{"/name/ja"},
			msg:   "/name/ja: contains a line break or tab",
		},
		{
			name: "name and tagline in languages not in the site settings",
			in: edit(testSite(), func(s *Site) {
				s.Name["de"] = "Beispiel GmbH"
				s.Tagline = Localized{"ja": "暮らしに寄り添う道具をつくる", "fr": "Des outils du quotidien"}
			}),
			paths: []string{"/name/de", "/tagline/fr"},
			msg: `/name/de: language "de" is not in the site settings (use ja, en)` + "\n" +
				`/tagline/fr: language "fr" is not in the site settings (use ja, en)`,
		},
		{name: "tagline in one language", in: edit(testSite(), func(s *Site) { s.Tagline = Localized{"en": "Tools for everyday life"} })},
		{
			name:  "line break in the tagline",
			in:    edit(testSite(), func(s *Site) { s.Tagline = Localized{"ja": "暮らしに寄り添う\n道具をつくる"} }),
			paths: []string{"/tagline/ja"},
			msg:   "/tagline/ja: contains a line break or tab",
		},
		{
			name:  "empty tagline",
			in:    edit(testSite(), func(s *Site) { s.Tagline = Localized{"ja": "暮らしに寄り添う道具をつくる", "en": ""} }),
			paths: []string{"/tagline/en"},
			msg:   "/tagline/en: empty value; omit it",
		},
		{
			name:  "URL with a trailing slash",
			in:    edit(testSite(), func(s *Site) { s.URL = "https://example.com/" }),
			paths: []string{"/url"},
			msg:   `/url: must not end with "/"`,
		},
		{
			name:  "unknown time zone",
			in:    edit(testSite(), func(s *Site) { s.TimeZone = "Tokyo" }),
			paths: []string{"/timeZone"},
			msg:   `/timeZone: unknown time zone "Tokyo": use an IANA name such as "Asia/Tokyo" or "UTC"`,
		},

		{
			name:  "PDF for the logo",
			in:    edit(testSite(), func(s *Site) { s.Logo = testPDF }),
			paths: []string{"/logo"},
			msg:   `/logo: media "` + testPDF + `" (pdf) is not accepted here (accept: image)`,
		},
		{name: "PDF for the logo without lookup", in: edit(testSite(), func(s *Site) { s.Logo = testPDF }), noLookup: true},
		{
			name:  "icon that does not exist",
			in:    edit(testSite(), func(s *Site) { s.Icon = "med_" + testMissing }),
			paths: []string{"/icon"},
			msg:   `/icon: media "med_` + testMissing + `" does not exist`,
		},
		{
			name:     "icon that does not exist without lookup",
			in:       edit(testSite(), func(s *Site) { s.Icon = "med_" + testMissing }),
			noLookup: true,
		},
		{
			name:  "icon of another kind",
			in:    edit(testSite(), func(s *Site) { s.Icon = testAboutPage }),
			paths: []string{"/icon"},
			msg:   `/icon: invalid media ID "` + testAboutPage + `": use "med_" and a ULID (26 characters in upper case)`,
		},
		{
			name:     "icon of another kind without lookup",
			in:       edit(testSite(), func(s *Site) { s.Icon = testAboutPage }),
			noLookup: true,
			paths:    []string{"/icon"},
			msg:      `/icon: invalid media ID "` + testAboutPage + `"`,
		},

		{
			// The name in ja and the default language ja are not in the site settings either.
			name:  "missing languages",
			in:    edit(minimalSite(), func(s *Site) { s.Languages = nil }),
			paths: []string{"/name/ja", "/languages", "/defaultLanguage"},
			msg: `/name/ja: language "ja" is not in the site settings` + "\n/languages: missing languages\n" +
				`/defaultLanguage: language "ja" is not in the site settings`,
		},
		{
			name:  "invalid language",
			in:    edit(minimalSite(), func(s *Site) { s.Languages, s.Name["EN"] = []string{"ja", "EN"}, "Example Inc." }),
			paths: []string{"/languages/1"},
			msg:   `/languages/1: invalid language code "EN"`,
		},
		{
			name:  "duplicate language",
			in:    edit(minimalSite(), func(s *Site) { s.Languages, s.Name["en"] = []string{"ja", "en", "ja"}, "Example Inc." }),
			paths: []string{"/languages/2"},
			msg:   `/languages/2: duplicate language "ja"`,
		},
		{name: "default language other than the first", in: edit(testSite(), func(s *Site) { s.DefaultLanguage = "en" })},
		{
			name:  "default language not in the site settings",
			in:    edit(testSite(), func(s *Site) { s.DefaultLanguage = "fr" }),
			paths: []string{"/defaultLanguage"},
			msg:   `/defaultLanguage: language "fr" is not in the site settings (use ja, en)`,
		},
		{
			name:  "default language in another case",
			in:    edit(testSite(), func(s *Site) { s.DefaultLanguage = "JA" }),
			paths: []string{"/defaultLanguage"},
			msg:   `/defaultLanguage: language "JA" is not in the site settings (use ja, en)`,
		},
		{name: "prefix for the default language", in: edit(testSite(), func(s *Site) { s.PrefixDefaultLanguage = true })},

		{name: "front pages", in: pages(Localized{"ja": testPage, "en": testEnglishPage}, nil)},
		{name: "front page in one language", in: pages(Localized{"en": testEnglishPage}, nil)},
		{
			name:  "post for the front page",
			in:    pages(Localized{"ja": testPost}, nil),
			paths: []string{"/frontPage/ja"},
			msg:   `/frontPage/ja: entry "` + testPost + `" is of the type "post" (use page)`,
		},
		{
			name:  "front page in another language",
			in:    pages(Localized{"ja": testEnglishPage}, nil),
			paths: []string{"/frontPage/ja"},
			msg:   `/frontPage/ja: entry "` + testEnglishPage + `" is in the language "en", not "ja"`,
		},
		{
			name:  "post in another language for the front page",
			in:    pages(Localized{"en": testPost}, nil),
			paths: []string{"/frontPage/en", "/frontPage/en"},
			msg: `/frontPage/en: entry "` + testPost + `" is of the type "post" (use page)` + "\n" +
				`/frontPage/en: entry "` + testPost + `" is in the language "ja", not "en"`,
		},
		{
			name:     "front pages of another type and language without lookup",
			in:       pages(Localized{"ja": testEnglishPage, "en": testPost}, nil),
			noLookup: true,
		},
		{
			name:  "front page that does not exist",
			in:    pages(Localized{"ja": "ent_" + testMissing}, nil),
			paths: []string{"/frontPage/ja"},
			msg:   `/frontPage/ja: entry "ent_` + testMissing + `" does not exist`,
		},
		{name: "front page that does not exist without lookup", in: pages(Localized{"ja": "ent_" + testMissing}, nil), noLookup: true},
		{
			name:  "front page of another kind",
			in:    pages(Localized{"ja": testImage}, nil),
			paths: []string{"/frontPage/ja"},
			msg:   `/frontPage/ja: invalid entry ID "` + testImage + `": use "ent_" and a ULID (26 characters in upper case)`,
		},
		{
			name:  "front page in a language not in the site settings",
			in:    pages(Localized{"ja": testPage, "fr": testPage}, nil),
			paths: []string{"/frontPage/fr"},
			msg:   `/frontPage/fr: language "fr" is not in the site settings (use ja, en)`,
		},
		{
			name:  "empty front page",
			in:    pages(Localized{"ja": testPage, "en": ""}, nil),
			paths: []string{"/frontPage/en"},
			msg:   "/frontPage/en: empty value; omit it",
		},

		{
			name: "posts pages",
			in:   pages(Localized{"ja": testPage, "en": testEnglishPage}, Localized{"ja": testAboutPage, "en": testEnglishPrivacyPage}),
		},
		{name: "posts page in one language", in: pages(Localized{"ja": testPage, "en": testEnglishPage}, Localized{"ja": testAboutPage})},
		{
			name:  "posts page without a front page",
			in:    pages(Localized{"ja": testPage}, Localized{"ja": testAboutPage, "en": testEnglishPage}),
			paths: []string{"/postsPage/en"},
			msg:   "/postsPage/en: not allowed while the front page of en shows the latest posts; set frontPage",
		},
		{
			name:     "posts page without a front page without lookup",
			in:       pages(Localized{"ja": testPage}, Localized{"ja": testAboutPage, "en": testEnglishPage}),
			noLookup: true,
			paths:    []string{"/postsPage/en"},
			msg:      "/postsPage/en: not allowed while the front page of en shows the latest posts; set frontPage",
		},
		{
			name:  "front page for the posts page",
			in:    pages(Localized{"ja": testPage}, Localized{"ja": testPage}),
			paths: []string{"/postsPage/ja"},
			msg:   "/postsPage/ja: cannot be the front page of ja",
		},
		{
			name:  "post for the posts page",
			in:    pages(Localized{"ja": testPage}, Localized{"ja": testPost}),
			paths: []string{"/postsPage/ja"},
			msg:   `/postsPage/ja: entry "` + testPost + `" is of the type "post" (use page)`,
		},
		{
			name:  "posts page in another language",
			in:    pages(Localized{"ja": testPage}, Localized{"ja": testEnglishPage}),
			paths: []string{"/postsPage/ja"},
			msg:   `/postsPage/ja: entry "` + testEnglishPage + `" is in the language "en", not "ja"`,
		},
		{
			name:     "posts page in another language without lookup",
			in:       pages(Localized{"ja": testPage}, Localized{"ja": testEnglishPage}),
			noLookup: true,
		},
		{
			name:  "posts page that does not exist",
			in:    pages(Localized{"ja": testPage}, Localized{"ja": "ent_" + testMissing}),
			paths: []string{"/postsPage/ja"},
			msg:   `/postsPage/ja: entry "ent_` + testMissing + `" does not exist`,
		},
		{
			name:  "posts page of another kind",
			in:    pages(Localized{"ja": testPage}, Localized{"ja": testImage}),
			paths: []string{"/postsPage/ja"},
			msg:   `/postsPage/ja: invalid entry ID "` + testImage + `": use "ent_" and a ULID (26 characters in upper case)`,
		},
		{
			// Only the language is reported, though fr has no front page either.
			name:  "posts page in a language not in the site settings",
			in:    pages(Localized{"ja": testPage}, Localized{"fr": testAboutPage}),
			paths: []string{"/postsPage/fr"},
			msg:   `/postsPage/fr: language "fr" is not in the site settings (use ja, en)`,
		},
		{
			// An empty value counts as no posts page.
			name:  "empty posts page",
			in:    pages(Localized{"ja": testPage}, Localized{"en": ""}),
			paths: []string{"/postsPage/en"},
			msg:   "/postsPage/en: empty value; omit it",
		},
		{
			// An empty value counts as no front page.
			name:  "posts page with an empty front page",
			in:    pages(Localized{"ja": ""}, Localized{"ja": testAboutPage}),
			paths: []string{"/frontPage/ja", "/postsPage/ja"},
			msg:   "/frontPage/ja: empty value; omit it\n/postsPage/ja: not allowed while the front page of ja shows the latest posts; set frontPage",
		},
		{
			name: "posts pages with pages ending with .html",
			in: edit(pages(Localized{"ja": testPage, "en": testEnglishPage}, Localized{"ja": testAboutPage, "en": testEnglishPrivacyPage}), func(s *Site) {
				s.Types = append(s.Types, TypeDef{Name: TypePage, URL: "/{path}.html"})
			}),
			paths: []string{"/postsPage/ja", "/postsPage/en"},
			msg: `/postsPage/ja: the URL rule of page "/{path}.html" must end with "/" for the posts page` + "\n" +
				`/postsPage/en: the URL rule of page "/{path}.html" must end with "/" for the posts page`,
		},
		{
			name: "pages ending with .html without a posts page",
			in: edit(pages(Localized{"ja": testPage}, nil), func(s *Site) {
				s.Types = append(s.Types, TypeDef{Name: TypePage, URL: "/{path}.html"})
			}),
		},

		{name: "posts per page omitted", in: edit(testSite(), func(s *Site) { s.PostsPerPage = 0 })},
		{name: "one post per page", in: edit(testSite(), func(s *Site) { s.PostsPerPage = 1 })},
		{name: "most posts per page", in: edit(testSite(), func(s *Site) { s.PostsPerPage = MaxPostsPerPage })},
		{
			name:  "negative posts per page",
			in:    edit(testSite(), func(s *Site) { s.PostsPerPage = -1 }),
			paths: []string{"/postsPerPage"},
			msg:   "/postsPerPage: postsPerPage -1 is out of range (1 to 100)",
		},
		{
			name:  "too many posts per page",
			in:    edit(testSite(), func(s *Site) { s.PostsPerPage = MaxPostsPerPage + 1 }),
			paths: []string{"/postsPerPage"},
			msg:   "/postsPerPage: postsPerPage 101 is out of range (1 to 100)",
		},

		{name: "URL rule of the author pages", in: edit(testSite(), func(s *Site) { s.AuthorURL = "/writers/{slug}/" })},
		{
			name:  "invalid URL rule of the author pages",
			in:    edit(testSite(), func(s *Site) { s.AuthorURL = "/writers/{number}/" }),
			paths: []string{"/authorUrl"},
			msg:   `/authorUrl: invalid URL rule "/writers/{number}/": placeholder "{number}" is not allowed for author pages (use {slug})`,
		},
		{
			name:  "author pages at the path of a language",
			in:    edit(testSite(), func(s *Site) { s.AuthorURL = "/en/{slug}/" }),
			paths: []string{"/authorUrl"},
			msg:   `/authorUrl: invalid URL rule "/en/{slug}/": cannot start with "/en/", the path of the language en`,
		},

		{
			name: "SEO",
			in: edit(testSite(), func(s *Site) {
				s.SEO = SiteSEO{
					TitleFormat: Localized{"ja": "{title}｜{siteName}", "en": "{title} | {siteName}"},
					OGImage:     Localized{"ja": testImage, "en": testImage2},
					NoIndex:     true,
				}
			}),
		},
		{name: "title format in one language", in: edit(testSite(), func(s *Site) { s.SEO.TitleFormat = Localized{"en": "{siteName} - {title}"} })},
		{
			name:  "title format without the title",
			in:    edit(testSite(), func(s *Site) { s.SEO.TitleFormat = Localized{"ja": "{title}", "en": "{siteName}"} }),
			paths: []string{"/seo/titleFormat/en"},
			msg:   "/seo/titleFormat/en: missing {title}",
		},
		{
			name:  "title format in a language not in the site settings",
			in:    edit(testSite(), func(s *Site) { s.SEO.TitleFormat = Localized{"fr": "{title} | {siteName}"} }),
			paths: []string{"/seo/titleFormat/fr"},
			msg:   `/seo/titleFormat/fr: language "fr" is not in the site settings (use ja, en)`,
		},
		{
			name:  "empty title format",
			in:    edit(testSite(), func(s *Site) { s.SEO.TitleFormat = Localized{"ja": ""} }),
			paths: []string{"/seo/titleFormat/ja"},
			msg:   "/seo/titleFormat/ja: empty value; omit it",
		},
		{name: "Open Graph image in one language", in: edit(testSite(), func(s *Site) { s.SEO.OGImage = Localized{"ja": testImage} })},
		{
			name:  "PDF for the Open Graph image",
			in:    edit(testSite(), func(s *Site) { s.SEO.OGImage = Localized{"ja": testImage, "en": testPDF} }),
			paths: []string{"/seo/ogImage/en"},
			msg:   `/seo/ogImage/en: media "` + testPDF + `" (pdf) is not accepted here (accept: image)`,
		},
		{
			name:     "PDF for the Open Graph image without lookup",
			in:       edit(testSite(), func(s *Site) { s.SEO.OGImage = Localized{"ja": testImage, "en": testPDF} }),
			noLookup: true,
		},
		{
			name:  "Open Graph image that does not exist",
			in:    edit(testSite(), func(s *Site) { s.SEO.OGImage = Localized{"ja": "med_" + testMissing} }),
			paths: []string{"/seo/ogImage/ja"},
			msg:   `/seo/ogImage/ja: media "med_` + testMissing + `" does not exist`,
		},
		{
			name:  "Open Graph image of another kind",
			in:    edit(testSite(), func(s *Site) { s.SEO.OGImage = Localized{"ja": testPage} }),
			paths: []string{"/seo/ogImage/ja"},
			msg:   `/seo/ogImage/ja: invalid media ID "` + testPage + `": use "med_" and a ULID (26 characters in upper case)`,
		},
		{
			name:  "Open Graph image in a language not in the site settings",
			in:    edit(testSite(), func(s *Site) { s.SEO.OGImage = Localized{"fr": testImage} }),
			paths: []string{"/seo/ogImage/fr"},
			msg:   `/seo/ogImage/fr: language "fr" is not in the site settings (use ja, en)`,
		},
		{
			name:  "empty Open Graph image",
			in:    edit(testSite(), func(s *Site) { s.SEO.OGImage = Localized{"en": ""} }),
			paths: []string{"/seo/ogImage/en"},
			msg:   "/seo/ogImage/en: empty value; omit it",
		},
		{name: "noindex", in: edit(testSite(), func(s *Site) { s.SEO.NoIndex = true })},

		{
			name: "taxonomies",
			in: edit(minimalSite(), func(s *Site) {
				s.Taxonomies = []TaxonomyDef{
					{Name: "a b"},
					{Name: TaxonomyTag},
					{Name: "x", Label: "a\nb"},
					{Name: "x"},
					{Name: ""},
				}
			}),
			paths: []string{"/taxonomies/a b", "/taxonomies/x/label", "/taxonomies/x", "/taxonomies/"},
			msg: `/taxonomies/a b: invalid name "a b": use ASCII letters, digits, "-" and "_"` + "\n" +
				"/taxonomies/x/label: contains a line break or tab\n" +
				`/taxonomies/x: duplicate taxonomy "x"` + "\n" +
				"/taxonomies/: empty name",
		},
		{
			// The URL rule of category can have {path} without hierarchical.
			name: "built-in taxonomies",
			in: edit(testSite(), func(s *Site) {
				s.Taxonomies = append(s.Taxonomies, TaxonomyDef{Name: TaxonomyCategory, Label: "分類", URL: "/topics/{path}/"}, TaxonomyDef{Name: TaxonomyTag})
			}),
		},
		{
			// The URL rule is checked with the hierarchy of tag.
			name: "hierarchical tag",
			in: edit(testSite(), func(s *Site) {
				s.Taxonomies = append(s.Taxonomies, TaxonomyDef{Name: TaxonomyTag, URL: "/tag/{path}/", Hierarchical: true})
			}),
			paths: []string{"/taxonomies/tag/url", "/taxonomies/tag/hierarchical"},
			msg: `/taxonomies/tag/url: invalid URL rule "/tag/{path}/": placeholder "{path}" is not allowed for tag (use {slug})` + "\n" +
				"/taxonomies/tag/hierarchical: not allowed for built-in taxonomies",
		},
		{
			name: "hierarchical category",
			in: edit(testSite(), func(s *Site) {
				s.Taxonomies = append(s.Taxonomies, TaxonomyDef{Name: TaxonomyCategory, Hierarchical: true})
			}),
			paths: []string{"/taxonomies/category/hierarchical"},
			msg:   "/taxonomies/category/hierarchical: not allowed for built-in taxonomies",
		},
		{
			// Each definition is checked with its own URL rule.
			name: "duplicate tag",
			in: edit(testSite(), func(s *Site) {
				s.Taxonomies = append(s.Taxonomies, TaxonomyDef{Name: TaxonomyTag, URL: "/tags/{slug}/"}, TaxonomyDef{Name: TaxonomyTag, URL: "/tags/"})
			}),
			paths: []string{"/taxonomies/tag", "/taxonomies/tag/url"},
			msg:   `/taxonomies/tag: duplicate taxonomy "tag"` + "\n" + `/taxonomies/tag/url: invalid URL rule "/tags/": missing {slug}`,
		},
		{name: "URL rule of a taxonomy", in: edit(testSite(), func(s *Site) { s.Taxonomies[1].URL = "/series/{path}/" })},
		{
			name:  "invalid URL rule of a taxonomy",
			in:    edit(testSite(), func(s *Site) { s.Taxonomies[1].URL = "/series/{path}" }),
			paths: []string{"/taxonomies/series/url"},
			msg:   `/taxonomies/series/url: invalid URL rule "/series/{path}": must end with "/"`,
		},
		{
			// The default URL rule is checked too.
			name:  "taxonomy with the name of a language",
			in:    edit(testSite(), func(s *Site) { s.Taxonomies = append(s.Taxonomies, TaxonomyDef{Name: "en"}) }),
			paths: []string{"/taxonomies/en/url"},
			msg:   `/taxonomies/en/url: invalid URL rule "/en/{slug}/": cannot start with "/en/", the path of the language en`,
		},
		{
			// The URL rule of a taxonomy with an invalid name is checked only if it is not the default.
			name:  "URL rules of taxonomies with invalid names",
			in:    edit(minimalSite(), func(s *Site) { s.Taxonomies = []TaxonomyDef{{Name: "a b", URL: "/a b/{slug}/"}, {Name: "c~d"}} }),
			paths: []string{"/taxonomies/a b", "/taxonomies/a b/url", "/taxonomies/c~0d"},
			msg: `/taxonomies/a b: invalid name "a b": use ASCII letters, digits, "-" and "_"` + "\n" +
				`/taxonomies/a b/url: invalid URL rule "/a b/{slug}/": character ' ' (U+0020) is not allowed` + "\n" +
				`/taxonomies/c~0d: invalid name "c~d"`,
		},
		{
			name:  "escaped name",
			in:    edit(minimalSite(), func(s *Site) { s.Types = []TypeDef{{Name: "a/b~c"}} }),
			paths: []string{"/types/a~1b~0c"},
			msg:   `/types/a~1b~0c: invalid name "a/b~c"`,
		},
		{
			name: "types",
			in: edit(minimalSite(), func(s *Site) {
				s.Types = []TypeDef{
					{Name: "a", Label: "a\tb"},
					{Name: "a"},
					{Name: TypePost, Taxonomies: []string{TaxonomyTag, "unknown"}},
					{Name: TypePage, Taxonomies: []string{TaxonomyCategory, TaxonomyTag, TaxonomyCategory}},
				}
			}),
			paths: []string{"/types/a/label", "/types/a", "/types/post/taxonomies/0", "/types/post/taxonomies/1", "/types/page/taxonomies/2"},
			msg: "/types/a/label: contains a line break or tab\n" +
				`/types/a: duplicate type "a"` + "\n" +
				"/types/post/taxonomies/0: post always uses tag\n" +
				`/types/post/taxonomies/1: unknown taxonomy "unknown"` + "\n" +
				`/types/page/taxonomies/2: duplicate taxonomy "category"`,
		},
		{
			name: "URL rules and archives of types",
			in: edit(testSite(), func(s *Site) {
				s.Types[0].URL, s.Types[0].Archive = "/works/{slug}.html", "/works/"
				s.Types[2].URL = "/archives/{number}/"
				s.Types = append(s.Types,
					TypeDef{Name: TypePage, URL: "/{path}.html"},
					TypeDef{Name: "news", URL: "/news/{category}/{slug}/", Archive: "/news/", Taxonomies: []string{TaxonomyCategory}},
				)
			}),
		},
		{
			name:  "invalid URL rule of a type",
			in:    edit(testSite(), func(s *Site) { s.Types[0].URL = "/works/{path}/" }),
			paths: []string{"/types/works/url"},
			msg:   `/types/works/url: invalid URL rule "/works/{path}/": placeholder "{path}" is not allowed for works (use {slug}, {number})`,
		},
		{
			name:  "invalid URL rule of post",
			in:    edit(testSite(), func(s *Site) { s.Types[2].URL = "/{yyyy}/{mm}/{dd}/" }),
			paths: []string{"/types/post/url"},
			msg:   `/types/post/url: invalid URL rule "/{yyyy}/{mm}/{dd}/": missing {slug} or {number}`,
		},
		{
			name: "archives of post and page",
			in: edit(testSite(), func(s *Site) {
				s.Types[2].Archive = "/news/"
				s.Types = append(s.Types, TypeDef{Name: TypePage, Archive: "/pages/"})
			}),
			paths: []string{"/types/post/archive", "/types/page/archive"},
			msg:   "/types/post/archive: not allowed for post\n/types/page/archive: not allowed for page",
		},
		{
			name:  "archive with a placeholder",
			in:    edit(testSite(), func(s *Site) { s.Types[0].Archive = "/works/{slug}/" }),
			paths: []string{"/types/works/archive"},
			msg:   `/types/works/archive: invalid archive path "/works/{slug}/": placeholders are not allowed`,
		},
		{
			name:  "archive at the front page",
			in:    edit(testSite(), func(s *Site) { s.Types[0].Archive = "/" }),
			paths: []string{"/types/works/archive"},
			msg:   `/types/works/archive: invalid archive path "/": cannot be "/", the path of the front page`,
		},
		{
			// The default URL rule is checked too.
			name:  "type with the name of a language",
			in:    edit(testSite(), func(s *Site) { s.Types = append(s.Types, TypeDef{Name: "en"}) }),
			paths: []string{"/types/en/url"},
			msg:   `/types/en/url: invalid URL rule "/en/{slug}/": cannot start with "/en/", the path of the language en`,
		},
		{name: "type with the name of the default language", in: edit(testSite(), func(s *Site) { s.Types = append(s.Types, TypeDef{Name: "ja"}) })},
		{
			name: "type with the name of the default language with a prefix",
			in: edit(testSite(), func(s *Site) {
				s.PrefixDefaultLanguage = true
				s.Types = append(s.Types, TypeDef{Name: "ja"})
			}),
			paths: []string{"/types/ja/url"},
			msg:   `/types/ja/url: invalid URL rule "/ja/{slug}/": cannot start with "/ja/", the path of the language ja`,
		},
		{
			// Each definition is checked with its own URL rule.
			name:  "duplicate type",
			in:    edit(testSite(), func(s *Site) { s.Types = append(s.Types, TypeDef{Name: "works", URL: "/works/"}) }),
			paths: []string{"/types/works", "/types/works/url"},
			msg:   `/types/works: duplicate type "works"` + "\n" + `/types/works/url: invalid URL rule "/works/": missing {slug} or {number}`,
		},
		{
			// The URL rule of a type with an invalid name is checked only if it is not the default.
			name:  "URL rules of types with invalid names",
			in:    edit(minimalSite(), func(s *Site) { s.Types = []TypeDef{{Name: "a b", URL: "/a b/{slug}/"}, {Name: "c~d"}} }),
			paths: []string{"/types/a b", "/types/a b/url", "/types/c~0d"},
			msg: `/types/a b: invalid name "a b": use ASCII letters, digits, "-" and "_"` + "\n" +
				`/types/a b/url: invalid URL rule "/a b/{slug}/": character ' ' (U+0020) is not allowed` + "\n" +
				`/types/c~0d: invalid name "c~d"`,
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
			lookup := testLookup
			if tt.noLookup {
				lookup = nil
			}
			err := tt.in.Validate(lookup)
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
