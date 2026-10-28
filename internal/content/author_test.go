package content

import (
	"encoding/json/v2"
	"maps"
	"reflect"
	"slices"
	"testing"
)

// testAuthorProfile is the ID of the author profile in testdata.
const testAuthorProfile = "aut_01JBA4C7N9Q3T6W9Z3C6F9H2K5"

// authorProfile is testdata/author-profile.json.
var authorProfile = AuthorProfile{
	ID:   testAuthorProfile,
	Name: "山田 太郎",
	Slug: "yamada",
	Bio: Localized{
		"ja": "編集長。Webデザインとアクセシビリティについて書いています。\n著書に『はじめてのWebアクセシビリティ』があります。",
		"en": "Editor in chief. Writes about web design and accessibility.",
	},
	Avatar: testImage2,
	Links:  []string{"https://x.com/example", "https://example.com/"},
}

func TestAuthorProfileUnmarshalJSON(t *testing.T) {
	in := readTestdata(t, "author-profile.json")
	var got AuthorProfile
	if err := json.Unmarshal([]byte(in), &got); err != nil {
		t.Fatalf("Unmarshal(%s) = %v", in, err)
	}
	if !reflect.DeepEqual(got, authorProfile) {
		t.Errorf("Unmarshal(%s) = %#v, want %#v", in, got, authorProfile)
	}
}

func TestAuthorProfileRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // "" if in is canonical
	}{
		{name: "author profile", in: readTestdata(t, "author-profile.json")},
		{name: "schemaVersion only", in: `{"schemaVersion":1}`},
		{
			name: "reversed key order",
			in: `{"links":["https://x.com/example","https://example.com/"],"avatar":"` + testImage + `",` +
				`"bio":{"ja":"編集長","en":"Editor in chief"},"slug":"yamada","name":"山田 太郎",` +
				`"id":"` + testAuthorProfile + `","schemaVersion":1}`,
			want: `{"schemaVersion":1,"id":"` + testAuthorProfile + `","name":"山田 太郎","slug":"yamada",` +
				`"bio":{"en":"Editor in chief","ja":"編集長"},"avatar":"` + testImage + `",` +
				`"links":["https://x.com/example","https://example.com/"]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.want
			if want == "" {
				want = tt.in
			}
			assertRoundTrip[AuthorProfile](t, tt.in, want)
		})
	}
}

func TestAuthorProfileMarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		in   AuthorProfile
	}{
		{name: "empty", in: AuthorProfile{}},
		{name: "empty bio and links", in: AuthorProfile{Bio: Localized{}, Links: []string{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if want := `{"schemaVersion":1}`; string(got) != want {
				t.Errorf("Marshal(%#v) = %s, want %s", tt.in, got, want)
			}
		})
	}
}

func TestAuthorProfileUnmarshalJSONError(t *testing.T) {
	tests := []struct {
		name string
		in   string
		path string
		msg  string
	}{
		{name: "empty", in: `{}`, path: "", msg: "empty object is not allowed"},
		{
			name: "missing schemaVersion",
			in:   `{"id":"x","name":"x","slug":"x","bio":{"ja":"x"},"avatar":"x","links":["x"]}`,
			path: "",
			msg:  `missing key "schemaVersion"`,
		},
		{name: "schemaVersion 2", in: `{"schemaVersion":2}`, path: "/schemaVersion", msg: "unsupported schemaVersion 2 (use 1)"},
		{name: "email address", in: `{"schemaVersion":1,"email":"yamada@example.com"}`, path: "/email", msg: "unknown key"},
		{name: "login account", in: `{"schemaVersion":1,"user":"` + testUser + `"}`, path: "/user", msg: "unknown key"},
		{name: "item of a term", in: `{"schemaVersion":1,"description":"x"}`, path: "/description", msg: "unknown key"},
		{name: "key case", in: `{"schemaVersion":1,"Bio":{"ja":"x"}}`, path: "/Bio", msg: "unknown key"},
		{name: "empty bio", in: `{"schemaVersion":1,"bio":{}}`, path: "/bio", msg: "empty object is not allowed"},
		{name: "string for the bio", in: `{"schemaVersion":1,"bio":"x"}`, path: "/bio", msg: "expected object, got string"},
		{name: "number in the bio", in: `{"schemaVersion":1,"bio":{"ja":1}}`, path: "/bio/ja", msg: "expected string, got number"},
		{name: "empty text in the bio", in: `{"schemaVersion":1,"bio":{"en":"x","ja":""}}`, path: "/bio/ja", msg: "empty string is not allowed"},
		{name: "null avatar", in: `{"schemaVersion":1,"avatar":null}`, path: "/avatar", msg: "expected string, got null"},
		{name: "empty links", in: `{"schemaVersion":1,"links":[]}`, path: "/links", msg: "empty array is not allowed"},
		{name: "string for the links", in: `{"schemaVersion":1,"links":"https://example.com/"}`, path: "/links", msg: "expected array, got string"},
		{name: "number in the links", in: `{"schemaVersion":1,"links":[1]}`, path: "/links/0", msg: "expected string, got number"},
		{name: "empty link", in: `{"schemaVersion":1,"links":["https://example.com/",""]}`, path: "/links/1", msg: "empty string is not allowed"},
		{name: "duplicate key", in: `{"schemaVersion":1,"name":"a","name":"b"}`, path: "/name", msg: "duplicate key"},
		{name: "trailing data", in: `{"schemaVersion":1} {}`, path: "", msg: "unexpected data after value"},
	}
	// old makes a new profile on each call, so that changes to its bio or links in place show.
	old := func() AuthorProfile {
		return AuthorProfile{ID: "old", Bio: Localized{"ja": "old"}, Links: []string{"old"}}
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := old()
			assertError(t, got.UnmarshalJSON([]byte(tt.in)), tt.path, tt.msg)
			if !reflect.DeepEqual(got, old()) {
				t.Errorf("UnmarshalJSON(%s) changed the profile to %#v", tt.in, got)
			}
		})
	}
}

func TestAuthorProfileNormalize(t *testing.T) {
	const ctrl = string(rune(0x0001))
	tests := []struct {
		name string
		in   AuthorProfile
		want AuthorProfile
	}{
		{
			name: "control characters",
			in:   AuthorProfile{Name: "a" + ctrl + "b", Slug: "c" + ctrl + "d", Bio: Localized{"ja": "e" + ctrl + "f"}},
			want: AuthorProfile{Name: "ab", Slug: "cd", Bio: Localized{"ja": "ef"}},
		},
		{
			name: "NFC",
			in:   AuthorProfile{Name: "cafe" + acute, Slug: "cafe" + acute, Bio: Localized{"en": "cafe" + acute}},
			want: AuthorProfile{Name: "caf" + eAcute, Slug: "caf" + eAcute, Bio: Localized{"en": "caf" + eAcute}},
		},
		{name: "slug in lower case", in: AuthorProfile{Name: "Yamada", Slug: "Yamada"}, want: AuthorProfile{Name: "Yamada", Slug: "yamada"}},
		{name: "line breaks and tabs left in the name", in: AuthorProfile{Name: "a\r\nb\tc"}, want: AuthorProfile{Name: "a\r\nb\tc"}},
		{
			name: "line breaks in the bio",
			in:   AuthorProfile{Bio: Localized{"ja": "a\r\nb\rc\td", "en": "e\r\n"}},
			want: AuthorProfile{Bio: Localized{"ja": "a\nb\nc\td", "en": "e\n"}},
		},
		{
			name: "language of which nothing is left in the bio",
			in:   AuthorProfile{Bio: Localized{"ja": "a", "en": ctrl}},
			want: AuthorProfile{Bio: Localized{"ja": "a"}},
		},
		{name: "nothing left in the bio", in: AuthorProfile{Bio: Localized{"ja": ctrl, "en": "\v"}}, want: AuthorProfile{}},
		{
			name: "spaces and control characters in the links",
			in:   AuthorProfile{Links: []string{" https://x.com/exa\tmple\n", "https://example.com/" + ctrl}},
			want: AuthorProfile{Links: []string{"https://x.com/example", "https://example.com/"}},
		},
		{
			name: "link not in NFC",
			in:   AuthorProfile{Links: []string{"https://example.com/cafe" + acute}},
			want: AuthorProfile{Links: []string{"https://example.com/cafe" + acute}},
		},
		{
			name: "links of which nothing is left",
			in:   AuthorProfile{Links: []string{" ", "https://x.com/example", ctrl, "https://example.com/"}},
			want: AuthorProfile{Links: []string{"https://x.com/example", "https://example.com/"}},
		},
		{name: "nothing left in the links", in: AuthorProfile{Links: []string{" ", "\t" + ctrl}}, want: AuthorProfile{}},
		{name: "empty bio and links", in: AuthorProfile{Bio: Localized{}, Links: []string{}}, want: AuthorProfile{}},
		{name: "author profile", in: authorProfile, want: authorProfile},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in
			// Normalize changes the bio and the links in place.
			got.Bio, got.Links = maps.Clone(tt.in.Bio), slices.Clone(tt.in.Links)
			got.Normalize()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Normalize() of %#v = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

func TestAuthorProfileValidate(t *testing.T) {
	const ctrl = string(rune(0x0001))
	// profile returns x changed by f, which gets copies of the bio and the links of x.
	profile := func(x AuthorProfile, f func(x *AuthorProfile)) AuthorProfile {
		x.Bio, x.Links = maps.Clone(x.Bio), slices.Clone(x.Links)
		f(&x)
		return x
	}
	tests := []struct {
		name     string
		in       AuthorProfile
		noLookup bool // validate without lookup
		paths    []string
		msg      string // part of the error text, a "path: message" line for each problem; not checked if ""
	}{
		{name: "author profile", in: authorProfile},
		{name: "author profile without lookup", in: authorProfile, noLookup: true},
		{name: "required items only", in: AuthorProfile{ID: testAuthorProfile, Name: "山田 太郎", Slug: "yamada"}},
		{
			name:  "missing items",
			in:    AuthorProfile{},
			paths: []string{"/id", "/name", "/slug"},
			msg:   "/id: missing id\n/name: missing name\n/slug: missing slug",
		},
		{
			// The errors are in document order.
			name: "invalid items",
			in: AuthorProfile{
				ID:     testUser,
				Name:   "a\tb",
				Slug:   "Yamada",
				Bio:    Localized{"de": "x", "en": "a" + ctrl},
				Avatar: testPDF,
				Links:  []string{"/about/", "/about/"},
			},
			paths: []string{"/id", "/name", "/slug", "/bio/en", "/bio/de", "/avatar", "/links/0", "/links/1"},
			msg: `/id: invalid author ID "` + testUser + `": use "aut_" and a ULID (26 characters in upper case)` + "\n" +
				"/name: contains a line break or tab\n/slug: contains upper case letters; normalize it first\n" +
				"/bio/en: contains control characters; normalize it first\n" +
				`/bio/de: language "de" is not in the site settings (use ja, en)` + "\n" +
				`/avatar: media "` + testPDF + `" (pdf) is not accepted here (accept: image)` + "\n" +
				"/links/0: must be an http or https URL\n" + `/links/1: duplicate link "/about/"`,
		},
		{
			name:  "ID of a login account",
			in:    profile(authorProfile, func(x *AuthorProfile) { x.ID = testUser }),
			paths: []string{"/id"},
			msg:   `/id: invalid author ID "` + testUser + `": use "aut_" and a ULID (26 characters in upper case)`,
		},
		{
			name:  "line break in the name",
			in:    profile(authorProfile, func(x *AuthorProfile) { x.Name = "山田\n太郎" }),
			paths: []string{"/name"},
			msg:   "/name: contains a line break or tab",
		},
		{name: "Japanese slug", in: profile(authorProfile, func(x *AuthorProfile) { x.Slug = "山田-太郎" })},
		{
			name:  "upper case slug",
			in:    profile(authorProfile, func(x *AuthorProfile) { x.Slug = "Yamada" }),
			paths: []string{"/slug"},
			msg:   "/slug: contains upper case letters; normalize it first",
		},

		{name: "bio in one language", in: profile(authorProfile, func(x *AuthorProfile) { delete(x.Bio, "en") })},
		{name: "bio with line breaks and tabs", in: profile(authorProfile, func(x *AuthorProfile) { x.Bio["en"] = "a\nb\tc\n" })},
		{
			name:  "bio in a language not in the site settings",
			in:    profile(authorProfile, func(x *AuthorProfile) { x.Bio["de"] = "Chefredakteur." }),
			paths: []string{"/bio/de"},
			msg:   `/bio/de: language "de" is not in the site settings (use ja, en)`,
		},
		{
			name:  "bio not in NFC",
			in:    profile(authorProfile, func(x *AuthorProfile) { x.Bio["en"] = "cafe" + acute }),
			paths: []string{"/bio/en"},
			msg:   "/bio/en: not in Unicode NFC; normalize it first",
		},
		{
			name:  "control character in the bio",
			in:    profile(authorProfile, func(x *AuthorProfile) { x.Bio["ja"] = "編集長" + ctrl }),
			paths: []string{"/bio/ja"},
			msg:   "/bio/ja: contains control characters; normalize it first",
		},
		{
			name:  "CR in the bio",
			in:    profile(authorProfile, func(x *AuthorProfile) { x.Bio["en"] = "a\r\nb" }),
			paths: []string{"/bio/en"},
			msg:   "/bio/en: contains control characters; normalize it first",
		},

		{
			name:  "PDF for the avatar",
			in:    profile(authorProfile, func(x *AuthorProfile) { x.Avatar = testPDF }),
			paths: []string{"/avatar"},
			msg:   `/avatar: media "` + testPDF + `" (pdf) is not accepted here (accept: image)`,
		},
		{name: "PDF for the avatar without lookup", in: profile(authorProfile, func(x *AuthorProfile) { x.Avatar = testPDF }), noLookup: true},
		{
			name:  "avatar that does not exist",
			in:    profile(authorProfile, func(x *AuthorProfile) { x.Avatar = "med_" + testMissing }),
			paths: []string{"/avatar"},
			msg:   `/avatar: media "med_` + testMissing + `" does not exist`,
		},
		{
			name:     "avatar that does not exist without lookup",
			in:       profile(authorProfile, func(x *AuthorProfile) { x.Avatar = "med_" + testMissing }),
			noLookup: true,
		},
		{
			name:  "avatar of another kind",
			in:    profile(authorProfile, func(x *AuthorProfile) { x.Avatar = testAboutPage }),
			paths: []string{"/avatar"},
			msg:   `/avatar: invalid media ID "` + testAboutPage + `": use "med_" and a ULID`,
		},
		{
			name:     "avatar of another kind without lookup",
			in:       profile(authorProfile, func(x *AuthorProfile) { x.Avatar = testAboutPage }),
			noLookup: true,
			paths:    []string{"/avatar"},
			msg:      `/avatar: invalid media ID "` + testAboutPage + `"`,
		},

		{name: "http link", in: profile(authorProfile, func(x *AuthorProfile) { x.Links = []string{"http://example.com/"} })},
		{name: "upper case scheme", in: profile(authorProfile, func(x *AuthorProfile) { x.Links = []string{"HTTPS://EXAMPLE.COM/"} })},
		{
			name:  "mailto link",
			in:    profile(authorProfile, func(x *AuthorProfile) { x.Links = []string{"mailto:yamada@example.com"} }),
			paths: []string{"/links/0"},
			msg:   "/links/0: must be an http or https URL",
		},
		{
			name:  "tel link",
			in:    profile(authorProfile, func(x *AuthorProfile) { x.Links = []string{"tel:+81-3-1234-5678"} }),
			paths: []string{"/links/0"},
			msg:   "/links/0: must be an http or https URL",
		},
		{
			name:  "path in the site",
			in:    profile(authorProfile, func(x *AuthorProfile) { x.Links = []string{"/about/"} }),
			paths: []string{"/links/0"},
			msg:   "/links/0: must be an http or https URL",
		},
		{
			name:  "javascript link",
			in:    profile(authorProfile, func(x *AuthorProfile) { x.Links = []string{"javascript:alert(1)"} }),
			paths: []string{"/links/0"},
			msg:   `/links/0: scheme "javascript" is not allowed`,
		},
		{
			name:  "link with credentials",
			in:    profile(authorProfile, func(x *AuthorProfile) { x.Links = []string{"https://user:pass@example.com/"} }),
			paths: []string{"/links/0"},
			msg:   "/links/0: credentials in URLs are not allowed",
		},
		{
			name:  "link with surrounding spaces",
			in:    profile(authorProfile, func(x *AuthorProfile) { x.Links = []string{" https://example.com/"} }),
			paths: []string{"/links/0"},
			msg:   "/links/0: contains control characters or surrounding spaces; normalize it first",
		},
		{
			name:  "protocol-relative link",
			in:    profile(authorProfile, func(x *AuthorProfile) { x.Links = []string{"//example.com/"} }),
			paths: []string{"/links/0"},
			msg:   "/links/0: protocol-relative URLs are not allowed",
		},
		{
			name:  "empty link",
			in:    profile(authorProfile, func(x *AuthorProfile) { x.Links = []string{"https://example.com/", ""} }),
			paths: []string{"/links/1"},
			msg:   "/links/1: empty URL",
		},
		{
			name: "duplicate link",
			in: profile(authorProfile, func(x *AuthorProfile) {
				x.Links = []string{"https://x.com/example", "https://example.com/", "https://x.com/example"}
			}),
			paths: []string{"/links/2"},
			msg:   `/links/2: duplicate link "https://x.com/example"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := testLookup
			if tt.noLookup {
				lookup = nil
			}
			err := tt.in.Validate(testSite(), lookup)
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
