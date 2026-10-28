package content

import (
	"encoding/json/v2"
	"reflect"
	"testing"
)

// IDs and translation groups of the terms in testdata, apart from testNewsCategory.
const (
	testPressReleaseCategory = "trm_01JBA4C7F3H6K9N2Q5S8V1X4Z7"
	testEventTag             = "trm_01JBA4C7G4J7M1P4R7T1W4Y7B1"
	testEnglishEventTag      = "trm_01JBA4C7H5K8N3Q6S9V3X6Z9C2"
	testNewsGroup            = "tg_01JBA4C7J6M9Q2S5V8Y1B4D7F0"
	testPressReleaseGroup    = "tg_01JBA4C7K7N1R4T7W1Z4C7E1G4"
	testEventGroup           = "tg_01JBA4C7M8P2S5V9Y2B6D9G3J6"
)

// The terms in testdata: a category and its child, and a tag in Japanese and its translation in English.
var (
	newsCategory = Term{ // news-category.json
		ID:               testNewsCategory,
		Taxonomy:         TaxonomyCategory,
		Language:         "ja",
		TranslationGroup: testNewsGroup,
		Name:             "お知らせ",
		Slug:             "news",
		Description:      "会社からのお知らせです。\n新しいサービスやイベントの情報を掲載します。",
	}
	pressReleaseCategory = Term{ // press-release-category.json
		ID:               testPressReleaseCategory,
		Taxonomy:         TaxonomyCategory,
		Language:         "ja",
		TranslationGroup: testPressReleaseGroup,
		Name:             "プレスリリース",
		Slug:             "press-release",
		Parent:           testNewsCategory,
	}
	eventTag = Term{ // event-tag-ja.json
		ID:               testEventTag,
		Taxonomy:         TaxonomyTag,
		Language:         "ja",
		TranslationGroup: testEventGroup,
		Name:             "イベント",
		Slug:             "イベント",
	}
	englishEventTag = Term{ // event-tag-en.json
		ID:               testEnglishEventTag,
		Taxonomy:         TaxonomyTag,
		Language:         "en",
		TranslationGroup: testEventGroup,
		Name:             "Events",
		Slug:             "events",
	}
)

func TestTermUnmarshalJSON(t *testing.T) {
	tests := []struct {
		file string
		want Term
	}{
		{file: "news-category.json", want: newsCategory},
		{file: "press-release-category.json", want: pressReleaseCategory},
		{file: "event-tag-ja.json", want: eventTag},
		{file: "event-tag-en.json", want: englishEventTag},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			in := readTestdata(t, tt.file)
			var got Term
			if err := json.Unmarshal([]byte(in), &got); err != nil {
				t.Fatalf("Unmarshal(%s) = %v", in, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Unmarshal(%s) = %#v, want %#v", in, got, tt.want)
			}
		})
	}
}

// TestTermTestdata checks how the terms in testdata relate, and that testRefs agrees with testdata about the parent.
func TestTermTestdata(t *testing.T) {
	if pressReleaseCategory.Parent != newsCategory.ID {
		t.Errorf("parent of the child category = %q, want %q", pressReleaseCategory.Parent, newsCategory.ID)
	}
	if want := (Ref{Type: newsCategory.Taxonomy, Language: newsCategory.Language}); testRefs[newsCategory.ID] != want {
		t.Errorf("testRefs[%q] = %#v, want %#v", newsCategory.ID, testRefs[newsCategory.ID], want)
	}
	if eventTag.TranslationGroup != englishEventTag.TranslationGroup {
		t.Errorf("translation groups of the tags = %q and %q, want the same", eventTag.TranslationGroup, englishEventTag.TranslationGroup)
	}
	if eventTag.Language == englishEventTag.Language {
		t.Errorf("languages of the tags = %q and %q, want different ones", eventTag.Language, englishEventTag.Language)
	}
}

func TestTermRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // "" if in is canonical
	}{
		{name: "category", in: readTestdata(t, "news-category.json")},
		{name: "child category", in: readTestdata(t, "press-release-category.json")},
		{name: "Japanese tag", in: readTestdata(t, "event-tag-ja.json")},
		{name: "English tag", in: readTestdata(t, "event-tag-en.json")},
		{name: "schemaVersion only", in: `{"schemaVersion":1}`},
		{
			name: "reversed key order",
			in: `{"description":"説明","parent":"` + testNewsCategory + `","slug":"press-release","name":"プレスリリース",` +
				`"translationGroup":"` + testPressReleaseGroup + `","language":"ja","taxonomy":"category",` +
				`"id":"` + testPressReleaseCategory + `","schemaVersion":1}`,
			want: `{"schemaVersion":1,"id":"` + testPressReleaseCategory + `","taxonomy":"category","language":"ja",` +
				`"translationGroup":"` + testPressReleaseGroup + `","name":"プレスリリース","slug":"press-release",` +
				`"parent":"` + testNewsCategory + `","description":"説明"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.want
			if want == "" {
				want = tt.in
			}
			assertRoundTrip[Term](t, tt.in, want)
		})
	}
}

func TestTermMarshalJSON(t *testing.T) {
	got, err := json.Marshal(Term{})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"schemaVersion":1}`; string(got) != want {
		t.Errorf("Marshal(Term{}) = %s, want %s", got, want)
	}
}

func TestTermUnmarshalJSONError(t *testing.T) {
	tests := []struct {
		name string
		in   string
		path string
		msg  string
	}{
		{name: "empty", in: `{}`, path: "", msg: "empty object is not allowed"},
		{name: "missing schemaVersion", in: `{"name":"x"}`, path: "", msg: `missing key "schemaVersion"`},
		{name: "schemaVersion 2", in: `{"schemaVersion":2}`, path: "/schemaVersion", msg: "unsupported schemaVersion 2 (use 1)"},
		{name: "schemaVersion as a string", in: `{"schemaVersion":"1"}`, path: "/schemaVersion", msg: "expected number, got string"},
		{name: "unknown key", in: `{"schemaVersion":1,"count":3}`, path: "/count", msg: "unknown key"},
		{name: "item of an entry", in: `{"schemaVersion":1,"status":"draft"}`, path: "/status", msg: "unknown key"},
		{name: "key case", in: `{"schemaVersion":1,"Name":"x"}`, path: "/Name", msg: "unknown key"},
		{name: "empty name", in: `{"schemaVersion":1,"name":""}`, path: "/name", msg: "empty string is not allowed"},
		{name: "number for slug", in: `{"schemaVersion":1,"slug":1}`, path: "/slug", msg: "expected string, got number"},
		{name: "null parent", in: `{"schemaVersion":1,"parent":null}`, path: "/parent", msg: "expected string, got null"},
		{name: "duplicate key", in: `{"schemaVersion":1,"name":"a","name":"b"}`, path: "/name", msg: "duplicate key"},
		{name: "trailing data", in: `{"schemaVersion":1} {}`, path: "", msg: "unexpected data after value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Term{ID: "old"}
			assertError(t, got.UnmarshalJSON([]byte(tt.in)), tt.path, tt.msg)
			if got != (Term{ID: "old"}) {
				t.Errorf("UnmarshalJSON(%s) changed the term to %#v", tt.in, got)
			}
		})
	}
}

func TestTermNormalize(t *testing.T) {
	const ctrl = string(rune(0x0001))
	tests := []struct {
		name string
		in   Term
		want Term
	}{
		{
			name: "control characters",
			in:   Term{Name: "a" + ctrl + "b", Slug: "c" + ctrl + "d", Description: "e" + ctrl + "f"},
			want: Term{Name: "ab", Slug: "cd", Description: "ef"},
		},
		{
			name: "NFC",
			in:   Term{Name: "cafe" + acute, Slug: "cafe" + acute, Description: "cafe" + acute},
			want: Term{Name: "caf" + eAcute, Slug: "caf" + eAcute, Description: "caf" + eAcute},
		},
		{name: "slug in lower case", in: Term{Name: "News", Slug: "News"}, want: Term{Name: "News", Slug: "news"}},
		{name: "line breaks and tabs left in the name", in: Term{Name: "a\r\nb\tc"}, want: Term{Name: "a\r\nb\tc"}},
		{name: "line breaks in the description", in: Term{Description: "a\r\nb\rc\td"}, want: Term{Description: "a\nb\nc\td"}},
		{name: "category", in: newsCategory, want: newsCategory},
		{name: "child category", in: pressReleaseCategory, want: pressReleaseCategory},
		{name: "Japanese tag", in: eventTag, want: eventTag},
		{name: "English tag", in: englishEventTag, want: englishEventTag},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in
			got.Normalize()
			if got != tt.want {
				t.Errorf("Normalize() of %#v = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

func TestTermValidate(t *testing.T) {
	const ctrl = string(rune(0x0001))
	// term returns x changed by f.
	term := func(x Term, f func(x *Term)) Term {
		f(&x)
		return x
	}
	tests := []struct {
		name     string
		in       Term
		noLookup bool // validate without lookup
		paths    []string
		msg      string // part of the error text, a "path: message" line for each problem; not checked if ""
	}{
		{name: "category", in: newsCategory},
		{name: "child category", in: pressReleaseCategory},
		{name: "Japanese tag", in: eventTag},
		{name: "English tag", in: englishEventTag},
		{name: "category without lookup", in: newsCategory, noLookup: true},
		{name: "child category without lookup", in: pressReleaseCategory, noLookup: true},
		{name: "Japanese tag without lookup", in: eventTag, noLookup: true},
		{name: "English tag without lookup", in: englishEventTag, noLookup: true},
		{
			name:  "missing items",
			in:    Term{},
			paths: []string{"/id", "/taxonomy", "/language", "/translationGroup", "/name", "/slug"},
			msg: "/id: missing id\n/taxonomy: missing taxonomy\n/language: missing language\n" +
				"/translationGroup: missing translationGroup\n/name: missing name\n/slug: missing slug",
		},
		{
			name: "invalid items",
			in: Term{
				ID:               testAboutPage,
				Taxonomy:         "genre",
				Language:         "fr",
				TranslationGroup: "tg_1",
				Name:             "a\nb",
				Slug:             "News",
				Parent:           testPage,
				Description:      "a" + ctrl,
			},
			paths: []string{"/id", "/taxonomy", "/language", "/translationGroup", "/name", "/slug", "/parent", "/description"},
			msg:   `/id: invalid term ID "` + testAboutPage + `"`,
		},
		{
			name:  "term ID of another kind",
			in:    term(newsCategory, func(x *Term) { x.ID = testAboutPage }),
			paths: []string{"/id"},
			msg:   `/id: invalid term ID "` + testAboutPage + `": use "trm_" and a ULID (26 characters in upper case)`,
		},
		{
			name:  "unknown taxonomy",
			in:    term(newsCategory, func(x *Term) { x.Taxonomy = "genre" }),
			paths: []string{"/taxonomy"},
			msg:   `/taxonomy: unknown taxonomy "genre"`,
		},
		{
			name:  "language not in the site settings",
			in:    term(newsCategory, func(x *Term) { x.Language = "fr" }),
			paths: []string{"/language"},
			msg:   `/language: language "fr" is not in the site settings (use ja, en)`,
		},
		{
			name:  "invalid translation group",
			in:    term(newsCategory, func(x *Term) { x.TranslationGroup = "tg_1" }),
			paths: []string{"/translationGroup"},
			msg:   `/translationGroup: invalid translationGroup ID "tg_1": use "tg_" and a ULID`,
		},
		{
			name:  "line break in the name",
			in:    term(newsCategory, func(x *Term) { x.Name = "お知らせ\n一覧" }),
			paths: []string{"/name"},
			msg:   "/name: contains a line break or tab",
		},
		{name: "Japanese slug", in: term(newsCategory, func(x *Term) { x.Slug = "お知らせ（2024年）" })},
		{
			name:  "upper case slug",
			in:    term(newsCategory, func(x *Term) { x.Slug = "News" }),
			paths: []string{"/slug"},
			msg:   "/slug: contains upper case letters; normalize it first",
		},
		{name: "description with line breaks and tabs", in: term(newsCategory, func(x *Term) { x.Description = "a\nb\tc" })},
		{
			name:  "control character in the description",
			in:    term(newsCategory, func(x *Term) { x.Description = "a" + ctrl }),
			paths: []string{"/description"},
			msg:   "/description: contains control characters; normalize it first",
		},
		{
			name:  "description not in NFC",
			in:    term(newsCategory, func(x *Term) { x.Description = "cafe" + acute }),
			paths: []string{"/description"},
			msg:   "/description: not in Unicode NFC; normalize it first",
		},

		{name: "custom taxonomy", in: term(eventTag, func(x *Term) { x.Taxonomy = "works_category" })},
		{
			name: "hierarchical custom taxonomy",
			in:   term(pressReleaseCategory, func(x *Term) { x.Taxonomy, x.Parent = "series", testSeries }),
		},
		{
			name:  "parent of a tag",
			in:    term(eventTag, func(x *Term) { x.Parent = testTag }),
			paths: []string{"/parent"},
			msg:   "/parent: not allowed for tag, which is not hierarchical",
		},
		{
			name:  "parent in a custom taxonomy that is not hierarchical",
			in:    term(eventTag, func(x *Term) { x.Taxonomy, x.Parent = "works_category", testWorksTerm }),
			paths: []string{"/parent"},
			msg:   "/parent: not allowed for works_category, which is not hierarchical",
		},
		{
			name:  "own parent",
			in:    term(pressReleaseCategory, func(x *Term) { x.Parent = x.ID }),
			paths: []string{"/parent"},
			msg:   "/parent: a term cannot be its own parent",
		},
		{
			name:  "parent in another taxonomy",
			in:    term(pressReleaseCategory, func(x *Term) { x.Parent = testSeries }),
			paths: []string{"/parent"},
			msg:   `/parent: term "` + testSeries + `" is in the taxonomy "series", not "category"`,
		},
		{
			name:  "parent in another language",
			in:    term(pressReleaseCategory, func(x *Term) { x.Parent = testEnglishCategory }),
			paths: []string{"/parent"},
			msg:   `/parent: term "` + testEnglishCategory + `" is in the language "en", not "ja"`,
		},
		{
			name:  "parent that does not exist",
			in:    term(pressReleaseCategory, func(x *Term) { x.Parent = "trm_" + testMissing }),
			paths: []string{"/parent"},
			msg:   `/parent: term "trm_` + testMissing + `" does not exist`,
		},
		{
			name:     "parent that does not exist without lookup",
			in:       term(pressReleaseCategory, func(x *Term) { x.Parent = "trm_" + testMissing }),
			noLookup: true,
		},
		{
			name:  "parent of another kind",
			in:    term(pressReleaseCategory, func(x *Term) { x.Parent = testPage }),
			paths: []string{"/parent"},
			msg:   `/parent: invalid term ID "` + testPage + `"`,
		},
		{
			name:     "parent of another kind without lookup",
			in:       term(pressReleaseCategory, func(x *Term) { x.Parent = testPage }),
			noLookup: true,
			paths:    []string{"/parent"},
			msg:      `/parent: invalid term ID "` + testPage + `"`,
		},
		{
			// Without a taxonomy to compare with, only the ID of the parent and that it exists are checked.
			name:  "parent in an unknown taxonomy",
			in:    term(pressReleaseCategory, func(x *Term) { x.Taxonomy, x.Parent = "genre", testSeries }),
			paths: []string{"/taxonomy"},
			msg:   `/taxonomy: unknown taxonomy "genre"`,
		},
		{
			name:  "parent that does not exist in an unknown taxonomy",
			in:    term(pressReleaseCategory, func(x *Term) { x.Taxonomy, x.Parent = "genre", "trm_"+testMissing }),
			paths: []string{"/taxonomy", "/parent"},
			msg:   `/taxonomy: unknown taxonomy "genre"` + "\n" + `/parent: term "trm_` + testMissing + `" does not exist`,
		},
		{
			name:  "parent without a taxonomy",
			in:    term(pressReleaseCategory, func(x *Term) { x.Taxonomy = "" }),
			paths: []string{"/taxonomy"},
			msg:   "/taxonomy: missing taxonomy",
		},
		{
			// Likewise without a language to compare with.
			name:  "parent in a language not in the site settings",
			in:    term(pressReleaseCategory, func(x *Term) { x.Language = "fr" }),
			paths: []string{"/language"},
			msg:   `/language: language "fr" is not in the site settings (use ja, en)`,
		},
		{
			name:  "parent of another kind in a language not in the site settings",
			in:    term(pressReleaseCategory, func(x *Term) { x.Language, x.Parent = "fr", testPage }),
			paths: []string{"/language", "/parent"},
			msg:   `/language: language "fr" is not in the site settings (use ja, en)` + "\n" + `/parent: invalid term ID "` + testPage + `"`,
		},
		{
			name:  "parent without a language",
			in:    term(pressReleaseCategory, func(x *Term) { x.Language = "" }),
			paths: []string{"/language"},
			msg:   "/language: missing language",
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
