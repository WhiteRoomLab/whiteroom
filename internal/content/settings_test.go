package content

import (
	"bytes"
	"cmp"
	"encoding/json/jsontext"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fullSettingsJSON is the settings document of fullSettingsSite, in canonical form.
const fullSettingsJSON = `{
  "schemaVersion": 1,
  "name": {"en": "Example Inc.", "ja": "株式会社〇〇"},
  "tagline": {"en": "Planning and making websites", "ja": "Webサイトの企画と制作"},
  "url": "https://example.com/blog",
  "timeZone": "Asia/Tokyo",
  "logo": "` + testLogo + `",
  "icon": "` + testIcon + `",
  "languages": ["ja", "en"],
  "defaultLanguage": "ja",
  "prefixDefaultLanguage": true,
  "frontPage": {"en": "` + testEnglishPage + `", "ja": "` + testHomePage + `"},
  "postsPage": {"ja": "` + testNewsPage + `"},
  "postsPerPage": 20,
  "seo": {
    "titleFormat": {"en": "{title} | {siteName}", "ja": "{title}｜{siteName}"},
    "ogImage": {"en": "` + testImage + `", "ja": "` + testOGImage + `"},
    "noindex": true
  },
  "allowHtml": true
}`

// reversedSettingsJSON is fullSettingsJSON with the keys and the languages of the values in reverse order.
const reversedSettingsJSON = `{
  "allowHtml": true,
  "seo": {
    "noindex": true,
    "ogImage": {"ja": "` + testOGImage + `", "en": "` + testImage + `"},
    "titleFormat": {"ja": "{title}｜{siteName}", "en": "{title} | {siteName}"}
  },
  "postsPerPage": 20,
  "postsPage": {"ja": "` + testNewsPage + `"},
  "frontPage": {"ja": "` + testHomePage + `", "en": "` + testEnglishPage + `"},
  "prefixDefaultLanguage": true,
  "defaultLanguage": "ja",
  "languages": ["ja", "en"],
  "icon": "` + testIcon + `",
  "logo": "` + testLogo + `",
  "timeZone": "Asia/Tokyo",
  "url": "https://example.com/blog",
  "tagline": {"ja": "Webサイトの企画と制作", "en": "Planning and making websites"},
  "name": {"ja": "株式会社〇〇", "en": "Example Inc."},
  "schemaVersion": 1
}`

// fullSettingsSite returns exampleSite with every item of the settings document set.
func fullSettingsSite() *Site {
	s := exampleSite()
	s.Tagline["en"] = "Planning and making websites"
	s.URL = "https://example.com/blog"
	s.PrefixDefaultLanguage = true
	s.FrontPage["en"] = testEnglishPage
	s.PostsPerPage = 20
	s.SEO.OGImage["en"] = testImage
	s.SEO.NoIndex = true
	s.AllowHTML = true
	return s
}

func TestFullSettingsSite(t *testing.T) {
	if err := fullSettingsSite().Validate(testLookup); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

// settingsRoundTrip checks that the settings document of s reads back as s and writes the same, and returns it.
func settingsRoundTrip(t *testing.T, s *Site) []byte {
	t.Helper()
	out, err := s.MarshalSettings()
	if err != nil {
		t.Fatalf("MarshalSettings() of %#v = %v", s, err)
	}
	again := &Site{AuthorURL: s.AuthorURL, Types: s.Types, Taxonomies: s.Taxonomies}
	if err := again.UnmarshalSettings(out); err != nil {
		t.Fatalf("UnmarshalSettings(%s) = %v", out, err)
	}
	if !reflect.DeepEqual(again, s) {
		t.Fatalf("UnmarshalSettings(%s) = %#v, want %#v", out, again, s)
	}
	if b, err := again.MarshalSettings(); err != nil || !bytes.Equal(b, out) {
		t.Fatalf("MarshalSettings() = %s, %v; want %s", b, err, out)
	}
	return out
}

func TestUnmarshalSettings(t *testing.T) {
	schema := exampleSite()
	schema.AuthorURL = "/writers/{slug}/"
	tests := []struct {
		name string
		in   string
		want *Site  // with the items of schema in place of its AuthorURL, Types and Taxonomies
		out  string // what MarshalSettings writes, ignoring spaces; "" if it is in
	}{
		{name: "example", in: readTestdata(t, "example-site-settings.json"), want: exampleSite()},
		{name: "every item", in: fullSettingsJSON, want: fullSettingsSite()},
		{name: "reversed key order", in: reversedSettingsJSON, want: fullSettingsSite(), out: fullSettingsJSON},
		{name: "schemaVersion only", in: `{"schemaVersion":1}`, want: &Site{}},
		{
			name: "false",
			in:   `{"schemaVersion":1,"prefixDefaultLanguage":false,"seo":{"noindex":false},"allowHtml":false}`,
			want: &Site{},
			out:  `{"schemaVersion":1}`,
		},
		{name: "prefixDefaultLanguage only", in: `{"schemaVersion":1,"prefixDefaultLanguage":true}`, want: &Site{PrefixDefaultLanguage: true}},
		{name: "noindex only", in: `{"schemaVersion":1,"seo":{"noindex":true}}`, want: &Site{SEO: SiteSEO{NoIndex: true}}},
		{name: "allowHtml only", in: `{"schemaVersion":1,"allowHtml":true}`, want: &Site{AllowHTML: true}},
		{name: "fewest posts per page", in: `{"schemaVersion":1,"postsPerPage":1}`, want: &Site{PostsPerPage: 1}},
		{name: "most posts per page", in: `{"schemaVersion":1,"postsPerPage":100}`, want: &Site{PostsPerPage: MaxPostsPerPage}},
		{name: "default posts per page", in: `{"schemaVersion":1,"postsPerPage":10}`, want: &Site{PostsPerPage: DefaultPostsPerPage}},
		{
			// Validate reports these.
			name: "values as written",
			in:   `{"schemaVersion":1,"name":{"fr":" a\tb "},"url":"HTTPS://Example.COM/","languages":["ja","ja","JA"],"timeZone":"Local"}`,
			want: &Site{Name: Localized{"fr": " a\tb "}, URL: "HTTPS://Example.COM/", TimeZone: "Local", Languages: []string{"ja", "ja", "JA"}},
			out:  `{"schemaVersion":1,"name":{"fr":" a\tb "},"url":"HTTPS://Example.COM/","timeZone":"Local","languages":["ja","ja","JA"]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := *tt.want
			want.AuthorURL, want.Types, want.Taxonomies = schema.AuthorURL, schema.Types, schema.Taxonomies
			// Every item of the settings document is replaced, even if in omits it.
			got := fullSettingsSite()
			got.AuthorURL = schema.AuthorURL
			if err := got.UnmarshalSettings([]byte(tt.in)); err != nil {
				t.Fatalf("UnmarshalSettings(%s) = %v", tt.in, err)
			}
			if !reflect.DeepEqual(got, &want) {
				t.Errorf("UnmarshalSettings(%s) = %#v, want %#v", tt.in, got, &want)
			}
			out := settingsRoundTrip(t, got)
			canonical := jsontext.Value(cmp.Or(tt.out, tt.in))
			if err := canonical.Compact(); err != nil {
				t.Fatalf("Compact(%s) = %v", canonical, err)
			}
			if !bytes.Equal(out, canonical) {
				t.Errorf("MarshalSettings() = %s, want %s", out, canonical)
			}
		})
	}
}

func TestMarshalSettings(t *testing.T) {
	tests := []struct {
		name string
		in   *Site
		want string
	}{
		{name: "empty", in: &Site{}, want: `{"schemaVersion":1}`},
		{
			name: "items of the schema file",
			in:   &Site{AuthorURL: "/writers/{slug}/", Types: testSite().Types, Taxonomies: testSite().Taxonomies},
			want: `{"schemaVersion":1}`,
		},
		{
			name: "empty values",
			in: &Site{
				Name: Localized{}, Tagline: Localized{}, Languages: []string{}, FrontPage: Localized{}, PostsPage: Localized{},
				SEO: SiteSEO{TitleFormat: Localized{}, OGImage: Localized{}},
			},
			want: `{"schemaVersion":1}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.in.MarshalSettings()
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("MarshalSettings() of %#v = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

const schemaFileKey = "unknown key; it belongs in the schema file"

var unmarshalSettingsErrorTests = []struct {
	name string
	in   string
	path string
	msg  string
}{
	{name: "missing schemaVersion", in: `{"name":{"ja":"株式会社〇〇"}}`, path: "", msg: `missing key "schemaVersion"`},
	{
		name: "missing schemaVersion after the other items",
		in:   strings.Replace(fullSettingsJSON, `"schemaVersion": 1,`, "", 1),
		path: "", msg: `missing key "schemaVersion"`,
	},
	{name: "schemaVersion 2", in: `{"schemaVersion":2}`, path: "/schemaVersion", msg: "unsupported schemaVersion 2 (use 1)"},
	{name: "schemaVersion as a string", in: `{"schemaVersion":"1"}`, path: "/schemaVersion", msg: "expected number, got string"},
	{name: "unknown key", in: `{"schemaVersion":1,"title":"株式会社〇〇"}`, path: "/title", msg: "unknown key"},
	{name: "key case", in: `{"schemaVersion":1,"allowHTML":true}`, path: "/allowHTML", msg: "unknown key"},
	{name: "types", in: `{"schemaVersion":1,"types":{"works":{"label":"制作実績"}}}`, path: "/types", msg: schemaFileKey},
	{name: "taxonomies", in: `{"schemaVersion":1,"taxonomies":{"works_category":{"label":"実績の種類"}}}`, path: "/taxonomies", msg: schemaFileKey},
	{name: "authorUrl", in: `{"schemaVersion":1,"authorUrl":"/writers/{slug}/"}`, path: "/authorUrl", msg: schemaFileKey},
	{
		name: "types after the other items",
		in:   strings.Replace(fullSettingsJSON, `"allowHtml": true`, `"allowHtml": true, "types": {"post": {"url": "/{slug}/"}}`, 1),
		path: "/types", msg: schemaFileKey,
	},
	{name: "unknown key in seo", in: `{"schemaVersion":1,"seo":{"title":"{title}"}}`, path: "/seo/title", msg: "unknown key"},
	{name: "key of the schema file in seo", in: `{"schemaVersion":1,"seo":{"types":{}}}`, path: "/seo/types", msg: "unknown key"},
	{name: "duplicate key", in: `{"schemaVersion":1,"url":"https://example.com","url":"https://example.org"}`, path: "/url", msg: "duplicate key"},
	{name: "duplicate schemaVersion", in: `{"schemaVersion":1,"schemaVersion":1}`, path: "/schemaVersion", msg: "duplicate key"},
	{name: "duplicate language", in: `{"schemaVersion":1,"name":{"ja":"株式会社〇〇","ja":"白い部屋"}}`, path: "/name/ja", msg: "duplicate key"},
	{name: "duplicate key in seo", in: `{"schemaVersion":1,"seo":{"noindex":true,"noindex":false}}`, path: "/seo/noindex", msg: "duplicate key"},
	{name: "null", in: `null`, path: "", msg: "expected object, got null"},
	{name: "array", in: `[{"schemaVersion":1}]`, path: "", msg: "expected object, got array"},
	{name: "null logo", in: `{"schemaVersion":1,"logo":null}`, path: "/logo", msg: "expected string, got null"},
	{name: "null seo", in: `{"schemaVersion":1,"seo":null}`, path: "/seo", msg: "expected object, got null"},
	{name: "null language", in: `{"schemaVersion":1,"languages":[null]}`, path: "/languages/0", msg: "expected string, got null"},
	{name: "empty document", in: `{}`, path: "", msg: "empty object is not allowed"},
	{name: "empty url", in: `{"schemaVersion":1,"url":""}`, path: "/url", msg: "empty string is not allowed"},
	{name: "empty name", in: `{"schemaVersion":1,"name":{}}`, path: "/name", msg: "empty object is not allowed"},
	{name: "empty value of a language", in: `{"schemaVersion":1,"name":{"ja":""}}`, path: "/name/ja", msg: "empty string is not allowed"},
	{name: "empty seo", in: `{"schemaVersion":1,"seo":{}}`, path: "/seo", msg: "empty object is not allowed"},
	{name: "empty titleFormat", in: `{"schemaVersion":1,"seo":{"titleFormat":{}}}`, path: "/seo/titleFormat", msg: "empty object is not allowed"},
	{name: "empty ogImage", in: `{"schemaVersion":1,"seo":{"ogImage":{"ja":""}}}`, path: "/seo/ogImage/ja", msg: "empty string is not allowed"},
	{name: "empty languages", in: `{"schemaVersion":1,"languages":[]}`, path: "/languages", msg: "empty array is not allowed"},
	{name: "empty language", in: `{"schemaVersion":1,"languages":["ja",""]}`, path: "/languages/1", msg: "empty string is not allowed"},
	{name: "string for name", in: `{"schemaVersion":1,"name":"株式会社〇〇"}`, path: "/name", msg: "expected object, got string"},
	{name: "string for languages", in: `{"schemaVersion":1,"languages":"ja"}`, path: "/languages", msg: "expected array, got string"},
	{name: "number for a language", in: `{"schemaVersion":1,"languages":["ja",1]}`, path: "/languages/1", msg: "expected string, got number"},
	{name: "number for a page", in: `{"schemaVersion":1,"frontPage":{"ja":1}}`, path: "/frontPage/ja", msg: "expected string, got number"},
	{
		name: "string for titleFormat",
		in:   `{"schemaVersion":1,"seo":{"titleFormat":"{title}"}}`,
		path: "/seo/titleFormat", msg: "expected object, got string",
	},
	{name: "string for postsPerPage", in: `{"schemaVersion":1,"postsPerPage":"10"}`, path: "/postsPerPage", msg: "expected number, got string"},
	{name: "string for allowHtml", in: `{"schemaVersion":1,"allowHtml":"true"}`, path: "/allowHtml", msg: "expected boolean, got string"},
	{
		name: "number for prefixDefaultLanguage",
		in:   `{"schemaVersion":1,"prefixDefaultLanguage":1}`,
		path: "/prefixDefaultLanguage", msg: "expected boolean, got number",
	},
	{name: "string for noindex", in: `{"schemaVersion":1,"seo":{"noindex":"true"}}`, path: "/seo/noindex", msg: "expected boolean, got string"},
	{
		name: "postsPerPage 0",
		in:   `{"schemaVersion":1,"postsPerPage":0}`,
		path: "/postsPerPage", msg: "postsPerPage 0 is out of range (1 to 100)",
	},
	{
		name: "postsPerPage 101",
		in:   `{"schemaVersion":1,"postsPerPage":101}`,
		path: "/postsPerPage", msg: "postsPerPage 101 is out of range (1 to 100)",
	},
	{
		name: "postsPerPage -1",
		in:   `{"schemaVersion":1,"postsPerPage":-1}`,
		path: "/postsPerPage", msg: "postsPerPage -1 is out of range (1 to 100)",
	},
	{name: "postsPerPage 2.0", in: `{"schemaVersion":1,"postsPerPage":2.0}`, path: "/postsPerPage", msg: "must be an integer"},
	{name: "postsPerPage 1e1", in: `{"schemaVersion":1,"postsPerPage":1e1}`, path: "/postsPerPage", msg: "must be an integer"},
	{
		name: "postsPerPage that overflows int",
		in:   `{"schemaVersion":1,"postsPerPage":99999999999999999999}`,
		path: "/postsPerPage", msg: "must be an integer",
	},
	{name: "trailing data", in: `{"schemaVersion":1} {}`, path: "", msg: "unexpected data after value"},
	{name: "trailing data after the other items", in: fullSettingsJSON + "\n}", path: "", msg: "unexpected data after value"},
	{name: "truncated", in: `{"schemaVersion":1,"name":{"ja":"株式会社〇〇"`, path: "/name", msg: "unexpected end of JSON input"},
	{name: "truncated after the other items", in: strings.TrimSuffix(fullSettingsJSON, "}"), path: "", msg: "unexpected end of JSON input"},
	{name: "empty input", in: "", path: "", msg: "unexpected end of JSON input"},
	{
		name: "syntax error",
		in:   `{"schemaVersion":1,"url":https://example.com}`,
		path: "/url", msg: "invalid character 'h' at start of value",
	},
	{name: "invalid UTF-8", in: "{\"schemaVersion\":1,\"name\":{\"ja\":\"\xff\"}}", path: "/name/ja", msg: "invalid UTF-8"},
}

func TestUnmarshalSettingsErrors(t *testing.T) {
	for _, tt := range unmarshalSettingsErrorTests {
		t.Run(tt.name, func(t *testing.T) {
			got := fullSettingsSite()
			err := got.UnmarshalSettings([]byte(tt.in))
			want := &Error{Path: tt.path, Msg: tt.msg}
			var e *Error
			if !errors.As(err, &e) || *e != *want {
				t.Errorf("UnmarshalSettings(%s) = %#v, want %#v", tt.in, err, want)
			}
			if !reflect.DeepEqual(got, fullSettingsSite()) {
				t.Errorf("UnmarshalSettings(%s) changed the site to %#v", tt.in, got)
			}
		})
	}
}

func TestNormalizeSettings(t *testing.T) {
	const (
		ctrl       = string(rune(0x0001))
		c1         = string(rune(0x009F)) // a C1 control character, which normalizeText removes, unlike NEL
		nel        = string(rune(0x0085))
		voicedMark = string(rune(0x3099)) // "か" and voicedMark is the NFD form of "が"
	)
	// edit returns s changed by f.
	edit := func(s *Site, f func(s *Site)) *Site {
		f(s)
		return s
	}
	// lineBreaks are left for Validate to report; normalizing the texts as those with line breaks would change them.
	lineBreaks := func(s *Site) {
		s.Name["ja"] = "株式会社\r\n〇〇"
		s.Tagline["ja"] = "\tWebサイトの" + nel + "企画と制作"
		s.SEO.TitleFormat["ja"] = "{title}\v｜{siteName}\r"
	}
	// others change the items that NormalizeSettings leaves as they are.
	others := func(s *Site) {
		s.TimeZone = " ASIA/tokyo" + ctrl
		s.Logo, s.Icon = " "+testLogo+ctrl, "med_"+strings.ToLower(testIcon[4:])+"\t"
		s.Languages, s.DefaultLanguage = []string{"JA", " en" + ctrl}, "JA"
		s.FrontPage["ja"] = testHomePage + ctrl
		s.PostsPage["ja"] = " " + testNewsPage
		s.SEO.OGImage["ja"] = testOGImage + c1
		s.PostsPerPage, s.PrefixDefaultLanguage, s.SEO.NoIndex, s.AllowHTML = -1, true, true, true
		s.AuthorURL = " /Writers/{slug}/" + ctrl
		s.Types[0].Label = "投稿" + ctrl
		s.Types[1].URL = "/Works/{slug}/\t"
		s.Taxonomies[0].Label = "実績の" + c1 + "種類 cafe" + acute
	}
	tests := []struct {
		name string
		in   *Site
		want *Site
	}{
		{name: "example", in: exampleSite(), want: exampleSite()},
		{name: "every item", in: fullSettingsSite(), want: fullSettingsSite()},
		{name: "empty", in: &Site{}, want: &Site{}},
		{
			name: "control characters",
			in: edit(exampleSite(), func(s *Site) {
				s.Name["ja"] = "株式会社" + ctrl + "〇〇"
				s.Name["en"] = "Example" + c1 + " Inc.\x7f"
				s.Tagline["ja"] = "\x00Webサイトの企画と制作"
				s.SEO.TitleFormat["ja"] = "{title}" + ctrl + "｜{siteName}"
			}),
			want: exampleSite(),
		},
		{
			name: "NFC",
			in: edit(exampleSite(), func(s *Site) {
				s.Name["en"] = "Cafe" + acute
				s.Tagline["ja"] = "か" + voicedMark
				s.SEO.TitleFormat["en"] = "{title} | Cafe" + acute
			}),
			want: edit(exampleSite(), func(s *Site) {
				s.Name["en"] = "Caf" + eAcute
				s.Tagline["ja"] = "が"
				s.SEO.TitleFormat["en"] = "{title} | Caf" + eAcute
			}),
		},
		{
			name: "languages of which nothing is left",
			in: edit(exampleSite(), func(s *Site) {
				s.Name["en"] = ctrl
				s.Tagline["ja"] = ctrl + c1
				s.SEO.TitleFormat["en"] = "\x7f"
			}),
			want: edit(exampleSite(), func(s *Site) {
				delete(s.Name, "en")
				s.Tagline = nil
				delete(s.SEO.TitleFormat, "en")
			}),
		},
		{name: "line breaks and tabs", in: edit(exampleSite(), lineBreaks), want: edit(exampleSite(), lineBreaks)},
		{
			name: "URL",
			in:   edit(exampleSite(), func(s *Site) { s.URL = " HTTPS://Example.COM/Blog\n" }),
			want: edit(exampleSite(), func(s *Site) { s.URL = "https://example.com/Blog" }),
		},
		{name: "other items", in: edit(exampleSite(), others), want: edit(exampleSite(), others)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in
			got.NormalizeSettings()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NormalizeSettings() = %#v, want %#v", got, tt.want)
			}
			got.NormalizeSettings()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NormalizeSettings() again = %#v, want it unchanged", got)
			}
		})
	}
}

func TestNormalizeSettingsURL(t *testing.T) {
	const (
		ctrl             = string(rune(0x0001))
		ideographicSpace = string(rune(0x3000))
		kelvin           = string(rune(0x212A)) // KELVIN SIGN, which strings.ToLower maps to "k"
		queryFragment    = "queries and fragments are not allowed"
	)
	tests := []struct {
		name string
		in   string
		want string
		msg  string // what checkSiteURL reports about want; "" if it is valid
	}{
		{name: "normalized", in: "https://example.com", want: "https://example.com"},
		{name: "surrounding spaces", in: " https://example.com/blog\n", want: "https://example.com/blog"},
		{name: "surrounding ideographic spaces", in: ideographicSpace + "https://example.com" + ideographicSpace, want: "https://example.com"},
		{name: "control characters", in: "ht\ttps://exa\x00mple.com/b" + ctrl + "log", want: "https://example.com/blog"},
		{name: "scheme and host in lower case", in: "HTTPS://Example.COM/Blog", want: "https://example.com/Blog"},
		{name: "A and Z", in: "HTTPS://AZ.AZ", want: "https://az.az"},
		{name: "Japanese path", in: "Https://EXAMPLE.com/ブログ", want: "https://example.com/ブログ"},
		{name: "port", in: "HTTP://LOCALHOST:8080/A", want: "http://localhost:8080/A"},
		{name: "IPv6", in: "https://[::ABCD]:8443/A", want: "https://[::abcd]:8443/A"},
		{name: "query", in: "https://Example.COM?Q=A", want: "https://example.com?Q=A", msg: queryFragment},
		{name: "fragment", in: "https://Example.COM#Top", want: "https://example.com#Top", msg: queryFragment},
		{name: "credentials", in: "https://User:Pass@Example.COM", want: "https://user:pass@example.com", msg: "credentials in URLs are not allowed"},
		{name: "default port", in: "HTTPS://Example.COM:443", want: "https://example.com:443", msg: "port 443 is the default for https; omit it"},
		{name: "trailing slash", in: "https://Example.COM/", want: "https://example.com/", msg: `must not end with "/"`},
		{
			name: "kelvin sign",
			in:   "https://" + kelvin + "XAMPLE.COM",
			want: "https://" + kelvin + "xample.com",
			msg:  "the host must be ASCII; write a domain name in the xn-- form",
		},
		{name: "empty host", in: "HTTPS:///Example.COM", want: "https:///Example.COM", msg: "missing host"},
		{name: "without //", in: "HTTPS:Example.COM", want: "HTTPS:Example.COM", msg: "missing host"},
		{name: "one slash", in: "HTTPS:/Example.COM", want: "HTTPS:/Example.COM", msg: "missing host"},
		{name: "without scheme", in: "Example.COM/Blog", want: "Example.COM/Blog", msg: `must be an absolute URL or a path starting with "/"`},
		{name: "// without :", in: "Example.COM//Blog", want: "Example.COM//Blog", msg: `must be an absolute URL or a path starting with "/"`},
		{name: "protocol-relative", in: "//Example.COM", want: "//Example.COM", msg: "protocol-relative URLs are not allowed"},
		{
			name: "scheme after a path",
			in:   "/Blog?next=HTTPS://Example.COM",
			want: "/Blog?next=HTTPS://Example.COM",
			msg:  "must be an http or https URL",
		},
		{
			name: "scheme in a query",
			in:   "Blog?next=HTTPS://Example.COM",
			want: "Blog?next=HTTPS://Example.COM",
			msg:  `must be an absolute URL or a path starting with "/"`,
		},
		{
			name: "scheme in a fragment",
			in:   "Blog#HTTPS://Example.COM",
			want: "Blog#HTTPS://Example.COM",
			msg:  `must be an absolute URL or a path starting with "/"`,
		},
		{name: "mailto", in: "MAILTO:Info@Example.COM", want: "MAILTO:Info@Example.COM", msg: "must be an http or https URL"},
		{name: "empty", in: "", want: "", msg: "empty URL"},
		{name: "nothing left", in: " " + ctrl + "\n", want: "", msg: "empty URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Site{URL: tt.in}
			s.NormalizeSettings()
			if s.URL != tt.want {
				t.Errorf("NormalizeSettings() changed URL %q to %q, want %q", tt.in, s.URL, tt.want)
			}
			assertErrorMsg(t, checkSiteURL(s.URL), tt.msg)
			s.NormalizeSettings()
			if s.URL != tt.want {
				t.Errorf("NormalizeSettings() again changed URL %q to %q", tt.want, s.URL)
			}
		})
	}
}

// TestNormalizeSettingsValidate checks that Validate accepts what NormalizeSettings has cleaned up.
func TestNormalizeSettingsValidate(t *testing.T) {
	const (
		ctrl = string(rune(0x0001))
		c1   = string(rune(0x009F))
	)
	want := fullSettingsSite()
	want.Name["en"] = "Caf" + eAcute + " Inc."
	if err := want.Validate(testLookup); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	got := fullSettingsSite()
	got.Name["ja"] = "株式会社" + ctrl + "〇〇"
	got.Name["en"] = "Cafe" + acute + " Inc."
	got.Tagline["ja"] = c1 + "Webサイトの企画と制作"
	got.URL = " HTTPS://Example.COM/blog\t"
	got.SEO.TitleFormat["ja"] = "{title}｜{siteName}\x7f"
	paths := []string{"/name/ja", "/name/en", "/tagline/ja", "/url", "/seo/titleFormat/ja"}
	if p := errorPaths(t, got.Validate(testLookup)); !reflect.DeepEqual(p, paths) {
		t.Errorf("Validate() paths = %q, want %q", p, paths)
	}
	got.NormalizeSettings()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NormalizeSettings() = %#v, want %#v", got, want)
	}
	if err := got.Validate(testLookup); err != nil {
		t.Errorf("Validate() = %v after NormalizeSettings", err)
	}
}

// FuzzUnmarshalSettings checks that a settings document that reads writes as one that reads back the same and writes
// the same, before and after NormalizeSettings, which leaves nothing for Validate to ask to normalize and changes
// nothing when called again.
func FuzzUnmarshalSettings(f *testing.F) {
	example, err := os.ReadFile(filepath.Join("testdata", "example-site-settings.json"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(example)
	f.Add([]byte(fullSettingsJSON))
	f.Add([]byte(reversedSettingsJSON))
	for _, tt := range unmarshalSettingsErrorTests {
		f.Add([]byte(tt.in))
	}
	const authorURL = "/writers/{slug}/"
	f.Fuzz(func(t *testing.T, data []byte) {
		s := &Site{AuthorURL: authorURL}
		if err := s.UnmarshalSettings(data); err != nil {
			var e *Error
			if !errors.As(err, &e) || e.File != "" || e.Line != 0 {
				t.Fatalf("UnmarshalSettings(%q) = %#v, want an *Error without a position", data, err)
			}
			if !reflect.DeepEqual(s, &Site{AuthorURL: authorURL}) {
				t.Fatalf("UnmarshalSettings(%q) = %v and changed the site to %#v", data, err, s)
			}
			return
		}
		if s.AuthorURL != authorURL {
			t.Fatalf("UnmarshalSettings(%q) changed AuthorURL to %q", data, s.AuthorURL)
		}
		settingsRoundTrip(t, s)
		s.NormalizeSettings()
		normalized := settingsRoundTrip(t, s)
		if err := s.Validate(nil); err != nil && strings.Contains(err.Error(), "normalize it first") {
			t.Fatalf("Validate() of %s = %v", normalized, err)
		}
		s.NormalizeSettings()
		if again, err := s.MarshalSettings(); err != nil || !bytes.Equal(again, normalized) {
			t.Fatalf("NormalizeSettings() again changed %s to %s, %v", normalized, again, err)
		}
	})
}
