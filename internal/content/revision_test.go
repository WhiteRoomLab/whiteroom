package content

import (
	"cmp"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"reflect"
	"strings"
	"testing"
)

// mustRevision reads in with Revision.UnmarshalJSON.
func mustRevision(t *testing.T, in string) Revision {
	t.Helper()
	var r Revision
	if err := r.UnmarshalJSON([]byte(in)); err != nil {
		t.Fatalf("UnmarshalJSON(%s) = %v", in, err)
	}
	return r
}

// testEntryID is the ID of the entries of the revisions in the tests, which testLookup does not find.
const testEntryID = "ent_01JB9E00000000000000000001"

// testEntry returns a draft entry of the type typ in ja.
func testEntry(typ string) *Entry {
	return &Entry{
		ID:               testEntryID,
		Number:           1,
		Type:             typ,
		Language:         "ja",
		TranslationGroup: testGroup,
		Status:           StatusDraft,
		CreatedAt:        "2026-10-25T10:00:00+09:00",
		CreatedBy:        testUser,
	}
}

// rev is the JSON of a revision with members, which start with a comma.
func rev(members string) string {
	return `{"schemaVersion":1` + members + `}`
}

// revFields is the JSON of a revision with the fields, the members of an object.
func revFields(fields string) string {
	return rev(`,"fields":{` + fields + `}`)
}

func TestRevisionUnmarshalJSON(t *testing.T) {
	want := Revision{
		Title: "〇〇株式会社のコーポレートサイト",
		Slug:  "example-corporate-site",
		Terms: Terms{"works_category": {testWorksTerm}},
		Fields: Fields{
			"client": jsontext.Value(`"〇〇株式会社"`),
			"images": jsontext.Value(`["` + testImage + `","` + testImage2 + `"]`),
			"year":   jsontext.Value(`2024`),
		},
		Body: Blocks{{
			Type: BlockParagraph,
			ID:   "b_7k2m9x4p",
			Text: RichText{{Type: InlineText, Text: "コーポレートサイトを全面的に作り直しました。"}},
		}},
	}
	bothOrders(t, readTestdata(t, "works-revision.json"), func(t *testing.T, in string) {
		var got Revision
		if err := json.Unmarshal([]byte(in), &got); err != nil {
			t.Fatalf("Unmarshal(%s) = %v", in, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Unmarshal(%s) = %#v, want %#v", in, got, want)
		}
	})
}

func TestRevisionRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  string // "" if in is canonical
		order bool   // also with the type members moved to the end, which changes the values of fields as written
	}{
		{name: "about page", in: readTestdata(t, "about-page-revision.json"), order: true},
		{name: "works", in: readTestdata(t, "works-revision.json"), order: true},
		{name: "every field", in: readTestdata(t, "every-field-revision.json")},
		{name: "schemaVersion only", in: rev(""), order: true},
		{
			name: "post",
			in: rev(`,"title":"お知らせ","slug":"news-1","date":"2026-10-25T10:00:00+09:00","author":"` + testAuthor + `","sticky":true,` +
				`"terms":{"category":["` + testCategory + `"],"tag":["` + testTag + `","` + testTag2 + `"]}`),
			order: true,
		},
		{
			name:  "page",
			in:    rev(`,"title":"採用情報","slug":"recruit","parent":"` + testPage + `","order":3,"template":"wide"`),
			order: true,
		},
		{name: "negative order", in: rev(`,"order":-1`), order: true},
		{
			name:  "default values",
			in:    rev(`,"sticky":false,"order":0,"seo":{"noindex":false}`),
			want:  rev(""),
			order: true,
		},
		{
			name:  "sorted taxonomies",
			in:    rev(`,"terms":{"tag":["` + testTag + `"],"category":["` + testCategory + `"]}`),
			want:  rev(`,"terms":{"category":["` + testCategory + `"],"tag":["` + testTag + `"]}`),
			order: true,
		},
		{
			name:  "sorted fields without spaces",
			in:    revFields(`"b": [ 1, 2 ], "a": { "x": "y z" }, "c": "\u0041\u00e9\u0001", "d": 1.50`),
			want:  revFields(`"a":{"x":"y z"},"b":[1,2],"c":"Aé\u0001","d":1.50`),
			order: true,
		},
		{
			name: "reversed key order",
			in: `{"body":[{"id":"b_00000001","type":"separator"}],"fields":{"x":1},"seo":{"canonical":"/a/","noindex":true,"title":"t"},` +
				`"terms":{"tag":["` + testTag + `"]},"template":"t","order":1,"parent":"` + testPage + `","sticky":true,` +
				`"author":"` + testAuthor + `","date":"2026-10-25T10:00:00Z","featuredImage":"` + testImage + `","excerpt":"e",` +
				`"slug":"s","title":"t","schemaVersion":1}`,
			want: rev(`,"title":"t","slug":"s","excerpt":"e","featuredImage":"` + testImage + `","date":"2026-10-25T10:00:00Z",` +
				`"author":"` + testAuthor + `","sticky":true,"parent":"` + testPage + `","order":1,"template":"t",` +
				`"terms":{"tag":["` + testTag + `"]},"seo":{"title":"t","noindex":true,"canonical":"/a/"},"fields":{"x":1},` +
				`"body":[{"type":"separator","id":"b_00000001"}]`),
			order: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.want
			if want == "" {
				want = tt.in
			}
			if tt.order {
				bothOrders(t, tt.in, func(t *testing.T, in string) { assertRoundTrip[Revision](t, in, want) })
			} else {
				assertRoundTrip[Revision](t, tt.in, want)
			}
		})
	}
}

func TestJSONMarshalRevisionTypes(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{name: "empty revision", in: Revision{}, want: `{"schemaVersion":1}`},
		{name: "empty fields of a revision", in: Revision{Fields: Fields{}, Terms: Terms{}}, want: `{"schemaVersion":1}`},
		{name: "terms", in: Terms{"b": {"x"}, "a": {"y"}}, want: `{"a":["y"],"b":["x"]}`},
		{name: "fields", in: Fields{"b": jsontext.Value(`1`), "a": jsontext.Value(`2`)}, want: `{"a":2,"b":1}`},
		{name: "SEO", in: SEO{NoIndex: true}, want: `{"noindex":true}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.in)
			if err != nil {
				t.Fatalf("Marshal(%#v) = %v", tt.in, err)
			}
			if string(got) != tt.want {
				t.Errorf("Marshal(%#v) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

func TestJSONUnmarshalRevisionTypes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		v    any
		path string
	}{
		{name: "revision", in: rev(`,"x":1`), v: new(Revision), path: "/x"},
		{name: "SEO", in: `{"x":1}`, v: new(SEO), path: "/x"},
		{name: "terms", in: `{"tag":[1]}`, v: new(Terms), path: "/tag/0"},
		{name: "fields", in: `{"x":null}`, v: new(Fields), path: "/x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertError(t, json.Unmarshal([]byte(tt.in), tt.v), tt.path, "")
		})
	}
}

func TestRevisionUnmarshalJSONError(t *testing.T) {
	tests := []struct {
		name string
		in   string
		path string
		msg  string
	}{
		{name: "empty", in: `{}`, path: "", msg: "empty object is not allowed"},
		{name: "missing schemaVersion", in: `{"title":"a"}`, path: "", msg: `missing key "schemaVersion"`},
		{name: "schemaVersion 2", in: `{"schemaVersion":2}`, path: "/schemaVersion", msg: "unsupported schemaVersion 2 (use 1)"},
		{name: "item of the state", in: rev(`,"type":"page"`), path: "/type", msg: "unknown key"},
		{name: "categories", in: rev(`,"categories":["` + testCategory + `"]`), path: "/categories", msg: "unknown key"},
		{name: "empty title", in: rev(`,"title":""`), path: "/title", msg: "empty string is not allowed"},
		{name: "number for slug", in: rev(`,"slug":1`), path: "/slug", msg: "expected string, got number"},
		{name: "string for sticky", in: rev(`,"sticky":"true"`), path: "/sticky", msg: "expected boolean, got string"},
		{name: "order with a fraction", in: rev(`,"order":1.5`), path: "/order", msg: "must be an integer"},
		{name: "string for order", in: rev(`,"order":"1"`), path: "/order", msg: "expected number, got string"},
		{name: "empty terms", in: rev(`,"terms":{}`), path: "/terms", msg: "empty object is not allowed"},
		{name: "empty list of terms", in: rev(`,"terms":{"tag":[]}`), path: "/terms/tag", msg: "empty array is not allowed"},
		{name: "string for terms", in: rev(`,"terms":{"tag":"x"}`), path: "/terms/tag", msg: "expected array, got string"},
		{name: "number for a term", in: rev(`,"terms":{"tag":["` + testTag + `",1]}`), path: "/terms/tag/1", msg: "expected string, got number"},
		{name: "empty term", in: rev(`,"terms":{"tag":[""]}`), path: "/terms/tag/0", msg: "empty string is not allowed"},
		{name: "empty seo", in: rev(`,"seo":{}`), path: "/seo", msg: "empty object is not allowed"},
		{name: "unknown key of seo", in: rev(`,"seo":{"keywords":"a"}`), path: "/seo/keywords", msg: "unknown key"},
		{name: "string for noindex", in: rev(`,"seo":{"noindex":"true"}`), path: "/seo/noindex", msg: "expected boolean, got string"},
		{name: "empty fields", in: rev(`,"fields":{}`), path: "/fields", msg: "empty object is not allowed"},
		{name: "null field", in: revFields(`"a":1,"b":null`), path: "/fields/b", msg: "null is not allowed"},
		{name: "empty string field", in: revFields(`"a":""`), path: "/fields/a", msg: "empty string is not allowed"},
		{name: "empty object field", in: revFields(`"a":{ }`), path: "/fields/a", msg: "empty object is not allowed"},
		{name: "empty array field", in: revFields(`"a":[ ]`), path: "/fields/a", msg: "empty array is not allowed"},
		{name: "duplicate key in a field", in: revFields(`"a":{"b":1,"b":2}`), path: "/fields/a/b", msg: "duplicate key"},
		{name: "duplicate field", in: revFields(`"a":1,"a":2`), path: "/fields/a", msg: "duplicate key"},
		{name: "empty body", in: rev(`,"body":[]`), path: "/body", msg: "empty array is not allowed"},
		{name: "unknown block", in: rev(`,"body":[{"type":"bogus"}]`), path: "/body/0/type", msg: `unknown block type "bogus"`},
		{
			name: "deep in the body",
			in:   rev(`,"body":[{"type":"group","content":[{"type":"group","content":[{"type":"x"}]}]}]`),
			path: "/body/0/content/0/content/0/type",
			msg:  `unknown block type "x"`,
		},
		{
			name: "body too deep",
			in:   rev(`,"body":[` + nestedGroups(MaxDepth) + `]`),
			path: "/body/0" + strings.Repeat("/content/0", MaxDepth-1) + "/content",
			msg:  "blocks are nested too deeply (max 32)",
		},
		{name: "trailing data", in: rev("") + `{}`, path: "", msg: "unexpected data after value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Revision{Title: "old"}
			assertError(t, got.UnmarshalJSON([]byte(tt.in)), tt.path, tt.msg)
			if !reflect.DeepEqual(got, Revision{Title: "old"}) {
				t.Errorf("UnmarshalJSON(%s) changed the revision to %#v", tt.in, got)
			}
		})
	}
}

// assertRevisionValidate checks the paths of the problems that Validate(opts) finds in r and, if msg is not "", their messages.
func assertRevisionValidate(t *testing.T, r Revision, opts RevisionOptions, paths []string, msg string) {
	t.Helper()
	err := r.Validate(opts)
	if paths == nil && err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	if got := errorPaths(t, err); !reflect.DeepEqual(got, paths) {
		t.Errorf("Validate() paths = %q, want %q", got, paths)
	}
	if msg != "" {
		assertErrorMsg(t, err, msg)
	}
}

// TestAboutPage checks the example in 6.11.1 of the concept (docs/06.11-examples.md), which is a published entry and
// its revision.
func TestAboutPage(t *testing.T) {
	site := testSite()
	var e Entry
	if err := e.UnmarshalJSON([]byte(readTestdata(t, "about-page-entry.json"))); err != nil {
		t.Fatalf("UnmarshalJSON(about-page-entry.json) = %v", err)
	}
	if err := e.Validate(site); err != nil {
		t.Errorf("Entry.Validate() = %v", err)
	}
	r := mustRevision(t, readTestdata(t, "about-page-revision.json"))
	normalized := mustRevision(t, readTestdata(t, "about-page-revision.json"))
	normalized.Normalize(site.Type(e.Type))
	if !reflect.DeepEqual(normalized, r) {
		t.Errorf("Normalize() changed the revision to %#v", normalized)
	}
	opts := RevisionOptions{Site: site, Entry: &e, Publish: true, Lookup: testLookup}
	assertRevisionValidate(t, r, opts, nil, "")
}

func TestValidateRevisionTestdata(t *testing.T) {
	tests := []struct {
		file string
		typ  string
	}{
		{file: "about-page-revision.json", typ: TypePage},
		{file: "works-revision.json", typ: "works"},
		{file: "every-field-revision.json", typ: "product"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			r := mustRevision(t, readTestdata(t, tt.file))
			for _, publish := range []bool{false, true} {
				opts := RevisionOptions{Site: testSite(), Entry: testEntry(tt.typ), Publish: publish, Lookup: testLookup}
				assertRevisionValidate(t, r, opts, nil, "")
			}
			opts := RevisionOptions{Site: testSite(), Entry: testEntry(tt.typ), Publish: true}
			assertRevisionValidate(t, r, opts, nil, "")
		})
	}
}

func TestRevisionValidate(t *testing.T) {
	const missing = "med_" + testMissing
	tests := []struct {
		name    string
		typ     string // type of the entry
		in      string
		publish bool
		opts    func(o *RevisionOptions) // changes the options; nil for none
		paths   []string
		msg     string // part of the error text, a "path: message" line for each problem; not checked if ""
	}{
		{name: "draft without items", typ: "product", in: rev("")},
		{
			name:    "published without items",
			typ:     "product",
			in:      rev(""),
			publish: true,
			paths:   []string{"/title", "/slug", "/fields/name"},
			msg:     "/title: missing title\n/slug: missing slug\n/fields/name: missing name",
		},
		{name: "draft post without date", typ: TypePost, in: rev(`,"title":"t","slug":"s"`)},
		{
			name:    "published post without date",
			typ:     TypePost,
			in:      rev(`,"title":"t","slug":"s"`),
			publish: true,
			paths:   []string{"/date"},
			msg:     "/date: missing date",
		},
		{name: "published page", typ: TypePage, in: rev(`,"title":"t","slug":"s"`), publish: true},
		{
			name:  "title",
			typ:   TypePage,
			in:    rev(`,"title":"a\nb"`),
			paths: []string{"/title"},
			msg:   "/title: contains a line break or tab",
		},
		{
			name:  "title not in NFC",
			typ:   TypePage,
			in:    rev(`,"title":"cafe\u0301"`),
			paths: []string{"/title"},
			msg:   "/title: not in Unicode NFC; normalize it first",
		},
		{name: "Japanese slug", typ: TypePage, in: rev(`,"slug":"会社概要（2024年）"`)},
		{
			name:  "slug",
			typ:   TypePage,
			in:    rev(`,"slug":"About"`),
			paths: []string{"/slug"},
			msg:   "/slug: contains upper case letters; normalize it first",
		},
		{name: "excerpt with line breaks and tabs", typ: TypePage, in: rev(`,"excerpt":"a\nb\tc"`)},
		{
			name:  "excerpt",
			typ:   TypePage,
			in:    rev(`,"excerpt":"a\u0001b"`),
			paths: []string{"/excerpt"},
			msg:   "/excerpt: contains control characters; normalize it first",
		},
		{
			name:  "featured PDF",
			typ:   TypePage,
			in:    rev(`,"featuredImage":"` + testPDF + `"`),
			paths: []string{"/featuredImage"},
			msg:   `/featuredImage: media "` + testPDF + `" (pdf) is not accepted here (accept: image)`,
		},
		{
			name:  "featured image that does not exist",
			typ:   TypePage,
			in:    rev(`,"featuredImage":"` + missing + `"`),
			paths: []string{"/featuredImage"},
			msg:   `/featuredImage: media "` + missing + `" does not exist`,
		},
		{name: "featured image without Lookup", typ: TypePage, in: rev(`,"featuredImage":"` + missing + `"`), opts: func(o *RevisionOptions) { o.Lookup = nil }},
		{
			name:  "seo",
			typ:   TypePage,
			in:    rev(`,"seo":{"title":"a\tb","description":"a\nb","ogImage":"` + testPDF + `","canonical":"mailto:a@example.com"}`),
			paths: []string{"/seo/title", "/seo/description", "/seo/ogImage", "/seo/canonical"},
			msg: "/seo/title: contains a line break or tab\n/seo/description: contains a line break or tab\n" +
				`/seo/ogImage: media "` + testPDF + `" (pdf) is not accepted here (accept: image)` + "\n" +
				`/seo/canonical: scheme "mailto" is not allowed for canonical`,
		},
		{name: "canonical path", typ: TypePage, in: rev(`,"seo":{"canonical":"/about/"}`)},
		{
			name:  "canonical script",
			typ:   TypePage,
			in:    rev(`,"seo":{"canonical":"javascript:alert(1)"}`),
			paths: []string{"/seo/canonical"},
			msg:   `/seo/canonical: scheme "javascript" is not allowed`,
		},

		{
			name: "post",
			typ:  TypePost,
			in: rev(`,"title":"t","slug":"s","date":"2026-10-25T10:00:00+09:00","author":"` + testAuthor + `","sticky":true,` +
				`"terms":{"category":["` + testCategory + `"],"tag":["` + testTag + `","` + testTag2 + `"],"series":["` + testSeries + `"]},` +
				`"fields":{"subtitle":"s"}`),
			publish: true,
		},
		{name: "page", typ: TypePage, in: rev(`,"parent":"` + testPage + `","order":-3,"template":"wide"`)},
		{
			name:  "items of other types",
			typ:   "product",
			in:    rev(`,"date":"x","author":"x","sticky":true,"parent":"x","order":1,"template":"x"`),
			paths: []string{"/date", "/author", "/sticky", "/parent", "/order", "/template"},
			msg: "/date: not allowed for product\n/author: not allowed for product\n/sticky: not allowed for product\n" +
				"/parent: not allowed for product\n/order: not allowed for product\n/template: not allowed for product",
		},
		{
			name:  "items of page in post",
			typ:   TypePost,
			in:    rev(`,"parent":"` + testPage + `","order":1,"template":"wide"`),
			paths: []string{"/parent", "/order", "/template"},
			msg:   "/parent: not allowed for post\n/order: not allowed for post\n/template: not allowed for post",
		},
		{
			name:  "items of post in page",
			typ:   TypePage,
			in:    rev(`,"date":"2026-10-25T10:00:00Z","author":"` + testAuthor + `","sticky":true`),
			paths: []string{"/date", "/author", "/sticky"},
			msg:   "/date: not allowed for page\n/author: not allowed for page\n/sticky: not allowed for page",
		},
		{
			name:  "date",
			typ:   TypePost,
			in:    rev(`,"date":"2026-10-25"`),
			paths: []string{"/date"},
			msg:   `/date: invalid date and time "2026-10-25"`,
		},
		{
			name:  "author",
			typ:   TypePost,
			in:    rev(`,"author":"aut_` + testMissing + `"`),
			paths: []string{"/author"},
			msg:   `/author: author "aut_` + testMissing + `" does not exist`,
		},
		{
			name:  "author of another kind",
			typ:   TypePost,
			in:    rev(`,"author":"` + testUser + `"`),
			paths: []string{"/author"},
			msg:   `/author: invalid author ID "` + testUser + `"`,
		},
		{
			name:  "own parent",
			typ:   TypePage,
			in:    rev(`,"parent":"` + testEntryID + `"`),
			paths: []string{"/parent"},
			msg:   "/parent: an entry cannot be its own parent",
		},
		{
			name:  "parent of another type",
			typ:   TypePage,
			in:    rev(`,"parent":"` + testPost + `"`),
			paths: []string{"/parent"},
			msg:   `/parent: entry "` + testPost + `" is of the type "post" (use page)`,
		},
		{
			name:  "parent in another language",
			typ:   TypePage,
			in:    rev(`,"parent":"` + testEnglishPage + `"`),
			paths: []string{"/parent"},
			msg:   `/parent: entry "` + testEnglishPage + `" is in the language "en", not "ja"`,
		},
		{
			name:  "parent that does not exist",
			typ:   TypePage,
			in:    rev(`,"parent":"ent_` + testMissing + `"`),
			paths: []string{"/parent"},
			msg:   `/parent: entry "ent_` + testMissing + `" does not exist`,
		},
		{
			name:  "template",
			typ:   TypePage,
			in:    rev(`,"template":"a b"`),
			paths: []string{"/template"},
			msg:   `/template: invalid name "a b"`,
		},

		{
			name:  "taxonomies the type does not use",
			typ:   "works",
			in:    rev(`,"terms":{"tag":["` + testTag + `"],"category":["` + testCategory + `"],"works_category":["` + testWorksTerm + `"]}`),
			paths: []string{"/terms/category", "/terms/tag"},
			msg:   `/terms/category: taxonomy "category" is not used by works` + "\n" + `/terms/tag: taxonomy "tag" is not used by works`,
		},
		{
			name:  "terms in the order of the taxonomies of the type",
			typ:   TypePost,
			in:    rev(`,"terms":{"tag":["` + testEnglishTag + `"],"category":["` + testTag + `"]}`),
			paths: []string{"/terms/category/0", "/terms/tag/0"},
			msg: `/terms/category/0: term "` + testTag + `" is in the taxonomy "tag", not "category"` + "\n" +
				`/terms/tag/0: term "` + testEnglishTag + `" is in the language "en", not "ja"`,
		},
		{
			name:  "duplicate term",
			typ:   TypePost,
			in:    rev(`,"terms":{"tag":["` + testTag + `","` + testTag2 + `","` + testTag + `"]}`),
			paths: []string{"/terms/tag/2"},
			msg:   `/terms/tag/2: duplicate term "` + testTag + `"`,
		},
		{
			name:  "term that does not exist",
			typ:   TypePost,
			in:    rev(`,"terms":{"tag":["trm_` + testMissing + `","` + testCategory + `x"]}`),
			paths: []string{"/terms/tag/0", "/terms/tag/1"},
			msg:   `/terms/tag/0: term "trm_` + testMissing + `" does not exist` + "\n" + `/terms/tag/1: invalid term ID`,
		},

		{
			name:  "body of a type without body",
			typ:   "product",
			in:    rev(`,"body":[{"type":"separator","id":"b_00000001"}]`),
			paths: []string{"/body"},
			msg:   "/body: not allowed for product, which has no body",
		},
		{
			name:  "problems in the body",
			typ:   TypePage,
			in:    rev(`,"body":[{"type":"heading","id":"b_00000001"},{"type":"listItem","content":[{"type":"paragraph","id":"b_00000002"}]}]`),
			paths: []string{"/body/0/attrs/level", "/body/0/content", "/body/1"},
			msg:   "/body/0/attrs/level: missing level\n/body/0/content: empty heading\n/body/1: listItem must be inside list",
		},
		{
			name:  "html without the site setting",
			typ:   TypePage,
			in:    rev(`,"body":[{"type":"html","id":"b_00000001","attrs":{"html":"<p>a</p>"}}]`),
			paths: []string{"/body/0"},
			msg:   "/body/0: html blocks are not enabled",
		},
		{
			name: "html with the site setting",
			typ:  TypePage,
			in:   rev(`,"body":[{"type":"html","id":"b_00000001","attrs":{"html":"<p>a</p>"}}]`),
			opts: func(o *RevisionOptions) { o.Site.AllowHTML = true },
		},
		{
			name:  "legacy without import",
			typ:   TypePage,
			in:    rev(`,"body":[{"type":"legacy","id":"b_00000001","attrs":{"html":"<p>a</p>"}}]`),
			paths: []string{"/body/0"},
			msg:   "/body/0: legacy blocks can only be created by import",
		},
		{
			name: "legacy from import",
			typ:  TypePage,
			in:   rev(`,"body":[{"type":"legacy","id":"b_00000001","attrs":{"html":"<p>a</p>"}}]`),
			opts: func(o *RevisionOptions) { o.Import = true },
		},
		{
			name: "link to an entry in the body",
			typ:  TypePage,
			in: rev(`,"body":[{"type":"paragraph","id":"b_00000001","content":[` +
				`{"type":"text","text":"a","marks":[{"type":"link","attrs":{"entry":"ent_` + testMissing + `"}}]}]}]`),
			paths: []string{"/body/0/content/0/marks/0/attrs/entry"},
			msg:   `/body/0/content/0/marks/0/attrs/entry: entry "ent_` + testMissing + `" does not exist`,
		},
		{
			name: "block IDs and anchors of the body and the fields",
			typ:  TypePost,
			in: rev(`,"fields":{"sidebar":[{"type":"separator","id":"b_00000001","anchor":"a"}]},` +
				`"body":[{"type":"separator","id":"b_00000001","anchor":"a"}]`),
			paths: []string{"/body/0/id", "/body/0/anchor"},
			msg: `/body/0/id: duplicate block ID "b_00000001" (first used at /fields/sidebar/0/id)` + "\n" +
				`/body/0/anchor: duplicate anchor "a" (first used at /fields/sidebar/0/anchor)`,
		},
		{
			name:  "unknown type",
			typ:   "news",
			in:    rev(`,"title":"a\nb","date":"x","terms":{"x":["y"]},"fields":{"x":1},"body":[{"type":"separator"}]`),
			paths: []string{"", "/title", "/body/0/id"},
			msg:   `unknown type "news"` + "\n/title: contains a line break or tab\n/body/0/id: missing block ID",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := RevisionOptions{Site: testSite(), Entry: testEntry(tt.typ), Publish: tt.publish, Lookup: testLookup}
			if tt.opts != nil {
				tt.opts(&opts)
			}
			assertRevisionValidate(t, mustRevision(t, tt.in), opts, tt.paths, tt.msg)
		})
	}
}

// TestRevisionValidateCategory checks that publishing an entry needs a category if the URL rule of its type has {category}.
func TestRevisionValidateCategory(t *testing.T) {
	// site returns testSite with postRule and worksRule as the URL rules of post and works ("" for the default),
	// and works using category if useCategory is true.
	site := func(postRule, worksRule string, useCategory bool) *Site {
		s := testSite()
		for i := range s.Types {
			switch d := &s.Types[i]; d.Name {
			case TypePost:
				d.URL = postRule
			case "works":
				d.URL = worksRule
				if useCategory {
					d.Taxonomies = append(d.Taxonomies, TaxonomyCategory)
				}
			}
		}
		return s
	}
	byCategory := site("/{category}/{slug}/", "/works/{category}/{slug}/", true)
	defaults := site("", "", true)
	assertValidSite(t, byCategory)
	assertValidSite(t, defaults)
	const (
		missingPost  = `/terms/category: missing category; the URL rule "/{category}/{slug}/" of post needs one`
		missingWorks = `/terms/category: missing category; the URL rule "/works/{category}/{slug}/" of works needs one`
	)
	post := Revision{Title: "t", Slug: "s", Date: "2026-10-25T10:00:00+09:00"}
	works := Revision{Title: "t", Slug: "s", Fields: Fields{"client": jsontext.Value(`"c"`)}}
	// withTerms returns r with terms.
	withTerms := func(r Revision, terms Terms) Revision {
		r.Terms = terms
		return r
	}
	tests := []struct {
		name    string
		site    *Site // byCategory if nil
		typ     string
		in      Revision
		publish bool
		paths   []string
		msg     string // part of the error text, a "path: message" line for each problem; not checked if ""
	}{
		{name: "post without a category", typ: TypePost, in: post, publish: true, paths: []string{"/terms/category"}, msg: missingPost},
		{name: "post with a category", typ: TypePost, in: withTerms(post, Terms{TaxonomyCategory: {testCategory}}), publish: true},
		{name: "draft post without a category", typ: TypePost, in: post},
		{
			name:    "post with empty categories",
			typ:     TypePost,
			in:      withTerms(post, Terms{TaxonomyCategory: {}}),
			publish: true,
			paths:   []string{"/terms/category"},
			msg:     "/terms/category: empty value; omit it",
		},
		{
			// In the order of the taxonomies of post, which starts with category.
			name:    "post with only a tag",
			typ:     TypePost,
			in:      withTerms(post, Terms{TaxonomyTag: {testEnglishTag}}),
			publish: true,
			paths:   []string{"/terms/category", "/terms/tag/0"},
			msg:     missingPost + "\n" + `/terms/tag/0: term "` + testEnglishTag + `" is in the language "en", not "ja"`,
		},
		{name: "post without a category in the default rule", site: defaults, typ: TypePost, in: post, publish: true},
		{name: "page", typ: TypePage, in: Revision{Title: "t", Slug: "s"}, publish: true},

		{name: "custom type without a category", typ: "works", in: works, publish: true, paths: []string{"/terms/category"}, msg: missingWorks},
		{name: "custom type with a category", typ: "works", in: withTerms(works, Terms{TaxonomyCategory: {testCategory}}), publish: true},
		{name: "draft of a custom type without a category", typ: "works", in: works},
		{
			name:    "custom type with empty categories",
			typ:     "works",
			in:      withTerms(works, Terms{TaxonomyCategory: nil}),
			publish: true,
			paths:   []string{"/terms/category"},
			msg:     "/terms/category: empty value; omit it",
		},
		{
			// In the order of the taxonomies of works, which ends with category.
			name:    "custom type with only its own taxonomy",
			typ:     "works",
			in:      withTerms(works, Terms{"works_category": {testTag}}),
			publish: true,
			paths:   []string{"/terms/works_category/0", "/terms/category"},
			msg:     `/terms/works_category/0: term "` + testTag + `" is in the taxonomy "tag", not "works_category"` + "\n" + missingWorks,
		},
		{name: "custom type without a category in the default rule", site: defaults, typ: "works", in: works, publish: true},
		{
			// The site is invalid: the rule cannot have {category} without category, which the entry cannot have either.
			name:    "custom type without category",
			site:    site("", "/works/{category}/{slug}/", false),
			typ:     "works",
			in:      works,
			publish: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := RevisionOptions{Site: cmp.Or(tt.site, byCategory), Entry: testEntry(tt.typ), Publish: tt.publish, Lookup: testLookup}
			assertRevisionValidate(t, tt.in, opts, tt.paths, tt.msg)
		})
	}
}

func TestRevisionValidateFields(t *testing.T) {
	tests := []struct {
		name    string
		in      string // members of the fields of a product
		publish bool
		paths   []string // under /fields
		msg     string   // part of the error text with the paths under /fields; not checked if ""
	}{
		{name: "text", in: `"name":"Bottle","summary":"a\nb"`},
		{name: "number for text", in: `"name":1`, paths: []string{"/name"}, msg: "/name: expected string, got number"},
		{name: "line break in text", in: `"name":"a\nb"`, paths: []string{"/name"}, msg: "/name: contains a line break or tab"},
		{name: "text not in NFC", in: `"name":"cafe\u0301"`, paths: []string{"/name"}, msg: "/name: not in Unicode NFC; normalize it first"},
		{
			name:  "longest text",
			in:    `"name":"` + strings.Repeat("あ", 40) + `"`,
			paths: nil,
		},
		{
			name:  "text too long",
			in:    `"name":"` + strings.Repeat("あ", 41) + `"`,
			paths: []string{"/name"},
			msg:   "/name: too long: 41 characters (max 40)",
		},
		{name: "short text in a draft", in: `"name":"a"`},
		{
			name:    "short text to publish",
			in:      `"name":"a"`,
			publish: true,
			paths:   []string{"/name"},
			msg:     "/name: too short: 1 characters (min 2)",
		},
		{
			name:  "control character in textarea",
			in:    `"summary":"a\u0001b"`,
			paths: []string{"/summary"},
			msg:   "/summary: contains control characters; normalize it first",
		},
		{
			name:  "rich text",
			in:    `"description":[{"type":"text","text":"a","marks":[{"type":"bold"},{"type":"bold"}]}]`,
			paths: []string{"/description/0/marks/1"},
			msg:   "/description/0/marks/1: duplicate bold mark",
		},
		{name: "string for rich text", in: `"description":"a"`, paths: []string{"/description"}, msg: "/description: expected array, got string"},
		{
			name:  "unknown inline type",
			in:    `"description":[{"type":"ruby"}]`,
			paths: []string{"/description/0/type"},
			msg:   `/description/0/type: unknown inline type "ruby"`,
		},
		{
			name:  "blocks",
			in:    `"details":[{"type":"paragraph"},{"type":"listItem","content":[{"type":"paragraph","id":"b_00000001"}]}]`,
			paths: []string{"/details/0/id", "/details/1"},
			msg:   "/details/0/id: missing block ID\n/details/1: listItem must be inside list",
		},
		{name: "object for blocks", in: `"details":{"type":"paragraph"}`, paths: []string{"/details"}, msg: "/details: expected array, got object"},
		{name: "number", in: `"price":0,"spec":{"material":"a","weight":0.5}`},
		{name: "string for number", in: `"price":"1"`, paths: []string{"/price"}, msg: "/price: expected number, got string"},
		{name: "number below the minimum", in: `"price":-0.5`, paths: []string{"/price"}, msg: "/price: -0.5 is less than the minimum 0"},
		{name: "number above the maximum", in: `"price":1e7`, paths: []string{"/price"}, msg: "/price: 1e7 is greater than the maximum 1000000"},
		{name: "number out of range", in: `"price":1e400`, paths: []string{"/price"}, msg: "/price: number 1e400 is out of range"},
		{name: "boolean", in: `"inStock":false`},
		{name: "string for boolean", in: `"inStock":"true"`, paths: []string{"/inStock"}, msg: "/inStock: expected boolean, got string"},
		{name: "date", in: `"releaseDate":"2026-02-29"`, paths: []string{"/releaseDate"}, msg: `/releaseDate: invalid date "2026-02-29"`},
		{name: "datetime", in: `"saleEnds":"2026-12-31T23:59:59"`, paths: []string{"/saleEnds"}, msg: `/saleEnds: invalid date and time "2026-12-31T23:59:59"`},
		{name: "select", in: `"size":"XL"`, paths: []string{"/size"}, msg: `/size: "XL" is not an option`},
		{name: "array for select", in: `"size":["S"]`, paths: []string{"/size"}, msg: "/size: expected string, got array"},
		{name: "string for multiple select", in: `"colors":"赤"`, paths: []string{"/colors"}, msg: "/colors: expected array, got string"},
		{
			name:  "multiple select",
			in:    `"colors":["赤","黒","赤"]`,
			paths: []string{"/colors", "/colors/1", "/colors/2"},
			msg:   "/colors: too many items: 3 (max 2)\n" + `/colors/1: "黒" is not an option` + "\n" + `/colors/2: duplicate item "赤"`,
		},
		{
			name:  "media of another kind",
			in:    `"manual":"` + testImage + `"`,
			paths: []string{"/manual"},
			msg:   `/manual: media "` + testImage + `" (image) is not accepted here (accept: pdf)`,
		},
		{name: "invalid media", in: `"manual":"med_1"`, paths: []string{"/manual"}, msg: `/manual: invalid media ID "med_1"`},
		{
			name:  "link",
			in:    `"shop":{"href":"javascript:alert(1)","newTab":true}`,
			paths: []string{"/shop/href"},
			msg:   `/shop/href: scheme "javascript" is not allowed`,
		},
		{name: "link without a destination", in: `"shop":{"newTab":true}`, paths: []string{"/shop"}, msg: "/shop: link needs href, entry, media or anchor"},
		{name: "unknown key of a link", in: `"shop":{"url":"https://example.com"}`, paths: []string{"/shop/url"}, msg: "/shop/url: unknown key"},
		{
			name:  "entry of another type",
			in:    `"maker":"` + testProduct + `"`,
			paths: []string{"/maker"},
			msg:   `/maker: entry "` + testProduct + `" is of the type "product" (use page)`,
		},
		{name: "entry that does not exist", in: `"maker":"ent_` + testMissing + `"`, paths: []string{"/maker"}, msg: `/maker: entry "ent_` + testMissing + `" does not exist`},
		{
			name:  "multiple entries",
			in:    `"related":["` + testPost + `","` + testPage + `","` + testPost + `","` + testProduct + `"]`,
			paths: []string{"/related", "/related/1", "/related/2"},
			msg: "/related: too many items: 4 (max 3)\n" +
				`/related/1: entry "` + testPage + `" is of the type "page" (use product, post)` + "\n" +
				`/related/2: duplicate item "` + testPost + `"`,
		},
		{
			name:  "term of another taxonomy",
			in:    `"mainCategory":"` + testTag + `"`,
			paths: []string{"/mainCategory"},
			msg:   `/mainCategory: term "` + testTag + `" is in the taxonomy "tag", not "category"`,
		},
		{
			name:  "term in another language",
			in:    `"keywords":["` + testTag + `","` + testEnglishTag + `"]`,
			paths: []string{"/keywords/1"},
			msg:   `/keywords/1: term "` + testEnglishTag + `" is in the language "en", not "ja"`,
		},
		{
			name:  "group",
			in:    `"spec":{"color":"red","weight":-1}`,
			paths: []string{"/spec/weight", "/spec/color"},
			msg:   "/spec/weight: -1 is less than the minimum 0\n" + `/spec/color: unknown field "color"`,
		},
		{name: "group without a required field in a draft", in: `"spec":{"weight":1}`},
		{
			name:    "group without a required field to publish",
			in:      `"name":"Bottle","spec":{"weight":1}`,
			publish: true,
			paths:   []string{"/spec/material"},
			msg:     "/spec/material: missing material",
		},
		{name: "string for group", in: `"spec":"x"`, paths: []string{"/spec"}, msg: "/spec: expected object, got string"},
		{name: "null in a group", in: `"spec":{"material":null}`, paths: []string{"/spec/material"}, msg: "/spec/material: expected string, got null"},
		{
			name:  "list",
			in:    `"features":["a",1,"b\tc","d","e","f"]`,
			paths: []string{"/features", "/features/1", "/features/2"},
			msg:   "/features: too many items: 6 (max 5)\n/features/1: expected string, got number\n/features/2: contains a line break or tab",
		},
		{name: "same items in a list", in: `"features":["a","a"]`},
		{name: "empty item", in: `"features":[""]`, paths: []string{"/features/0"}, msg: "/features/0: empty value; omit it"},
		{
			name:    "fewest items to publish",
			in:      `"name":"Bottle","colors":["赤"],"keywords":["` + testTag + `","` + testTag2 + `"],"features":["a"],"variants":[{"code":"x"}]`,
			publish: true,
		},
		{name: "too few items in a draft", in: `"keywords":["` + testTag + `"]`},
		{
			name:    "too few items to publish",
			in:      `"name":"Bottle","keywords":["` + testTag + `"]`,
			publish: true,
			paths:   []string{"/keywords"},
			msg:     "/keywords: too few items: 1 (min 2)",
		},
		{
			name:    "list of groups to publish",
			in:      `"name":"Bottle","variants":[{"image":"` + testPDF + `","sizes":["S","XL"]},{"code":"x","code2":"y"}]`,
			publish: true,
			paths:   []string{"/variants/0/code", "/variants/0/image", "/variants/0/sizes/1", "/variants/1/code2"},
			msg: "/variants/0/code: missing code\n" +
				`/variants/0/image: media "` + testPDF + `" (pdf) is not accepted here (accept: image)` + "\n" +
				`/variants/0/sizes/1: "XL" is not an option` + "\n" +
				`/variants/1/code2: unknown field "code2"`,
		},
		{
			name:  "list of lists",
			in:    `"matrix":[[1,"a"],2]`,
			paths: []string{"/matrix/0/1", "/matrix/1"},
			msg:   "/matrix/0/1: expected number, got string\n/matrix/1: expected array, got number",
		},
		{
			name:  "unknown fields in sorted order",
			in:    `"zeta":1,"name":1,"alpha":1`,
			paths: []string{"/name", "/alpha", "/zeta"},
			msg:   "/name: expected string, got number\n" + `/alpha: unknown field "alpha"` + "\n" + `/zeta: unknown field "zeta"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var paths []string
			for _, p := range tt.paths {
				paths = append(paths, "/fields"+p)
			}
			msg := ""
			if tt.msg != "" {
				msg = "/fields" + strings.ReplaceAll(tt.msg, "\n/", "\n/fields/")
			}
			opts := RevisionOptions{Site: testSite(), Entry: testEntry("product"), Publish: tt.publish, Lookup: testLookup}
			in := revFields(tt.in)
			if tt.publish {
				in = rev(`,"title":"t","slug":"s","fields":{` + tt.in + `}`)
			}
			assertRevisionValidate(t, mustRevision(t, in), opts, paths, msg)
		})
	}
}

func TestRevisionValidateGoValues(t *testing.T) {
	site := testSite()
	tests := []struct {
		name   string
		fields Fields
		paths  []string
		msg    string
	}{
		{name: "empty string", fields: Fields{"name": jsontext.Value(`""`)}, paths: []string{"/fields/name"}, msg: "/fields/name: empty value; omit it"},
		{name: "empty list", fields: Fields{"features": jsontext.Value(`[]`)}, paths: []string{"/fields/features"}, msg: "/fields/features: empty value; omit it"},
		{name: "null", fields: Fields{"spec": jsontext.Value(`null`)}, paths: []string{"/fields/spec"}, msg: "/fields/spec: expected object, got null"},
		{name: "spaces", fields: Fields{"features": jsontext.Value(` [ "a" ] `), "price": jsontext.Value("\t1\n")}},
		{name: "invalid JSON", fields: Fields{"name": jsontext.Value(`"a`)}, paths: []string{"/fields/name"}, msg: "/fields/name: unexpected end of JSON input"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := RevisionOptions{Site: site, Entry: testEntry("product"), Lookup: testLookup}
			assertRevisionValidate(t, Revision{Fields: tt.fields}, opts, tt.paths, tt.msg)
		})
	}
	opts := RevisionOptions{Site: site, Entry: testEntry(TypePost), Lookup: testLookup}
	t.Run("empty terms", func(t *testing.T) {
		assertRevisionValidate(t, Revision{Terms: Terms{TaxonomyTag: nil}}, opts, []string{"/terms/tag"}, "/terms/tag: empty value; omit it")
	})
	t.Run("too many blocks in the body and the fields", func(t *testing.T) {
		r := Revision{
			Fields: Fields{"sidebar": marshalValue(separatorBlocks(0, 4001))},
			Body:   separatorBlocks(4001, 6000),
		}
		assertRevisionValidate(t, r, opts, []string{"/body/5999"}, "/body/5999: too many blocks (max 10000)")
	})
}

func TestRevisionValidatePrevious(t *testing.T) {
	const (
		legacy  = `{"type":"legacy","id":"b_0legacy1","attrs":{"html":"<p>a</p>","reason":"r"}}`
		changed = `{"type":"legacy","id":"b_0legacy1","attrs":{"html":"<p>b</p>","reason":"r"}}`
		image   = `{"type":"image","id":"b_000image","attrs":{"media":"` + testImage + `"}}`
	)
	tests := []struct {
		name     string
		previous string
		in       string
		paths    []string
	}{
		{name: "legacy kept in the body", previous: rev(`,"body":[` + legacy + `]`), in: rev(`,"body":[` + legacy + `]`)},
		{name: "legacy moved from the body to a field", previous: rev(`,"body":[` + legacy + `]`), in: revFields(`"sidebar":[` + legacy + `]`)},
		{name: "legacy moved from a field to the body", previous: revFields(`"sidebar":[` + legacy + `]`), in: rev(`,"body":[` + legacy + `]`)},
		{
			name:     "legacy in a group of the previous revision",
			previous: rev(`,"body":[{"type":"group","id":"b_000group","content":[` + legacy + `]}]`),
			in:       revFields(`"sidebar":[` + legacy + `]`),
		},
		{name: "changed legacy", previous: rev(`,"body":[` + legacy + `]`), in: revFields(`"sidebar":[` + changed + `]`), paths: []string{"/fields/sidebar/0"}},
		{name: "legacy in an unknown field", previous: revFields(`"other":[` + legacy + `]`), in: rev(`,"body":[` + legacy + `]`), paths: []string{"/body/0"}},
		{name: "new legacy", previous: rev(""), in: rev(`,"body":[` + legacy + `]`), paths: []string{"/body/0"}},
		{name: "image without alt kept", previous: revFields(`"sidebar":[` + image + `]`), in: rev(`,"body":[` + image + `]`)},
		{name: "new image without alt", previous: rev(""), in: rev(`,"body":[` + image + `]`), paths: []string{"/body/0/attrs/alt"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prev := mustRevision(t, tt.previous)
			opts := RevisionOptions{Site: testSite(), Entry: testEntry(TypePost), Previous: &prev, Lookup: testLookup}
			assertRevisionValidate(t, mustRevision(t, tt.in), opts, tt.paths, "")
		})
	}
}

func TestRevisionValidateTheme(t *testing.T) {
	const (
		black       = `{"type":"paragraph","id":"b_0paragr1","style":{"textColor":{"value":"#000"}}}`
		noAccent    = `/marks/0/attrs/color/preset: the theme has no color preset "accent"`
		onlyPresets = "/style/textColor/value: the theme allows only presets for color"
	)
	accent := highlightJSON(`{"color":{"preset":"accent"}}`)
	marked := `{"type":"paragraph","id":"b_0paragr2","content":` + accent + `}`
	// faq has a body, and richText fields at the top and in a list of groups, which also have blocks.
	site := testSite()
	site.Types = append(site.Types, TypeDef{Name: "faq", Label: "FAQ", Body: true, Fields: []FieldDef{
		{Name: "notes", Type: FieldRichText},
		{Name: "items", Type: FieldList, Of: &FieldDef{Type: FieldGroup, Fields: []FieldDef{
			{Name: "answer", Type: FieldRichText},
			{Name: "more", Type: FieldBlocks},
		}}},
	}})
	tests := []struct {
		name     string
		previous string                   // the previous revision; "" for none
		in       string                   // a revision of a faq
		opts     func(o *RevisionOptions) // changes the options; nil for none
		want     []string                 // the problems as "path: message", in order
	}{
		{name: "style in the body", in: rev(`,"body":[` + black + `]`), want: []string{"/body/0" + onlyPresets}},
		{
			name: "style in a blocks field in a list",
			in:   revFields(`"items":[{"more":[` + black + `]}]`),
			want: []string{"/fields/items/0/more/0" + onlyPresets},
		},
		{name: "highlight in the body", in: rev(`,"body":[` + marked + `]`), want: []string{"/body/0/content/0" + noAccent}},
		{name: "highlight in a richText field", in: revFields(`"notes":` + accent), want: []string{"/fields/notes/0" + noAccent}},
		{
			name: "highlight in a richText field in a list",
			in:   revFields(`"items":[{"answer":` + accent + `}]`),
			want: []string{"/fields/items/0/answer/0" + noAccent},
		},

		{
			name:     "style kept from the body in a field",
			previous: rev(`,"body":[` + black + `]`),
			in:       revFields(`"items":[{"more":[` + black + `]}]`),
		},
		{
			name:     "style kept from a field in the body",
			previous: revFields(`"items":[{"more":[` + black + `]}]`),
			in:       rev(`,"body":[` + black + `]`),
		},
		{name: "highlight kept in a richText field", previous: revFields(`"notes":` + accent), in: revFields(`"notes":` + accent)},
		{name: "highlight kept from a richText field in the body", previous: revFields(`"notes":` + accent), in: rev(`,"body":[` + marked + `]`)},
		{name: "highlight kept from the body in a richText field", previous: rev(`,"body":[` + marked + `]`), in: revFields(`"notes":` + accent)},
		{
			name:     "highlight kept from a richText field in a list",
			previous: revFields(`"items":[{"answer":` + accent + `}]`),
			in:       revFields(`"notes":` + accent),
		},
		{
			name:     "highlight kept from a blocks field in a list",
			previous: revFields(`"items":[{"more":[` + marked + `]}]`),
			in:       revFields(`"notes":` + accent),
		},
		{
			name:     "highlight in a field that the type does not have",
			previous: revFields(`"other":` + accent),
			in:       revFields(`"notes":` + accent),
			want:     []string{"/fields/notes/0" + noAccent},
		},
		{
			name:     "highlight with another background color",
			previous: revFields(`"notes":` + highlightJSON(`{"color":{"preset":"accent"},"backgroundColor":{"preset":"primary"}}`)),
			in:       revFields(`"notes":` + accent),
			want:     []string{"/fields/notes/0" + noAccent},
		},

		{
			name: "import",
			in:   rev(`,"body":[` + black + `,` + marked + `],"fields":{"notes":` + accent + `}`),
			opts: func(o *RevisionOptions) { o.Import = true },
		},
		{
			name: "no theme",
			in:   rev(`,"body":[` + black + `,` + marked + `],"fields":{"notes":` + accent + `}`),
			opts: func(o *RevisionOptions) { o.Theme = nil },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := RevisionOptions{Site: site, Entry: testEntry("faq"), Theme: testTheme(), Lookup: testLookup}
			if tt.previous != "" {
				prev := mustRevision(t, tt.previous)
				opts.Previous = &prev
			}
			if tt.opts != nil {
				tt.opts(&opts)
			}
			var paths []string
			for _, w := range tt.want {
				p, _, _ := strings.Cut(w, ": ")
				paths = append(paths, p)
			}
			assertRevisionValidate(t, mustRevision(t, tt.in), opts, paths, strings.Join(tt.want, "\n"))
		})
	}
}

func TestFieldDefBlocks(t *testing.T) {
	f := FieldDef{Type: FieldList, Of: &FieldDef{Type: FieldGroup, Fields: []FieldDef{
		{Name: "text", Type: FieldText},
		{Name: "a", Type: FieldBlocks},
		{Name: "b", Type: FieldBlocks},
	}}}
	in := jsontext.Value(`[{"b":[{"type":"separator","id":"b_00000002"}],"a":[{"type":"separator","id":"b_00000001"}],"text":"x"},` +
		`{"c":[{"type":"separator","id":"b_00000003"}]},{"a":[{"type":"bogus"}]},{"a":[{"type":"separator","id":"b_00000004"}]}]`)
	got := f.blocks(in)
	want := []Blocks{
		{{Type: BlockSeparator, ID: "b_00000002"}},
		{{Type: BlockSeparator, ID: "b_00000001"}},
		{{Type: BlockSeparator, ID: "b_00000004"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("blocks() = %#v, want %#v", got, want)
	}
	for _, v := range []string{`1`, `{"a":1}`, `[1]`} {
		if got := f.blocks(jsontext.Value(v)); got != nil {
			t.Errorf("blocks(%s) = %#v, want nil", v, got)
		}
	}
}

func TestFieldDefRichTexts(t *testing.T) {
	f := FieldDef{Type: FieldList, Of: &FieldDef{Type: FieldGroup, Fields: []FieldDef{
		{Name: "text", Type: FieldText},
		{Name: "a", Type: FieldRichText},
		{Name: "b", Type: FieldRichText},
		{Name: "c", Type: FieldList, Of: &FieldDef{Type: FieldRichText}},
		{Name: "d", Type: FieldBlocks},
	}}}
	text := func(s string) string { return `[{"type":"text","text":"` + s + `"}]` }
	in := jsontext.Value(`[{"b":` + text("2") + `,"a":` + text("1") + `,"text":"x"},{"e":` + text("3") + `},` +
		`{"a":[{"type":"ruby"}]},{"c":[` + text("4") + `,` + text("5") + `]},` +
		`{"d":[{"type":"paragraph","id":"b_00000001","content":` + text("6") + `}]}]`)
	got := f.richTexts(in)
	want := []RichText{
		{{Type: InlineText, Text: "2"}},
		{{Type: InlineText, Text: "1"}},
		{{Type: InlineText, Text: "4"}},
		{{Type: InlineText, Text: "5"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("richTexts() = %#v, want %#v", got, want)
	}
	for _, v := range []string{`1`, `{"a":1}`, `[1]`} {
		if got := f.richTexts(jsontext.Value(v)); got != nil {
			t.Errorf("richTexts(%s) = %#v, want nil", v, got)
		}
	}
}

func TestRevisionNormalize(t *testing.T) {
	in := rev(`,"title":"cafe\u0301\u0007","slug":"Caf\u00c9","excerpt":"a\r\nb","featuredImage":"` + testImage + `",` +
		`"seo":{"title":"x\u0000","description":"d","canonical":" https://example.com/a "},` +
		`"fields":{` +
		`"name":"Bottle\u0001",` +
		`"summary":"a\r\nb\rc",` +
		`"description":[{"type":"text","text":"\u0001"},{"type":"text","text":"x"}],` +
		`"details":[{"id":"b_00000001","content":[{"type":"text","text":"e\u0301"}],"type":"paragraph"}],` +
		`"inStock":false,` +
		`"colors":["赤","\u0001"],` +
		`"spec":{"weight":1,"material":"\u0001"},` +
		`"features":["\u0001"],` +
		`"variants":[{"sizes":["S"],"code":"A\u0001"},{"code":"\u0001"}],` +
		`"shop":{"newTab":true,"href":" https://example.com/ "},` +
		`"size":"M",` +
		`"matrix":[[1, 2]],` +
		`"price":"cheap",` +
		`"unknown":"\u0001"},` +
		`"body":[{"type":"paragraph","id":"b_00000002","content":[{"type":"text","text":"a\u0007"}]}]`)
	want := rev(`,"title":"caf\u00e9","slug":"caf\u00e9","excerpt":"a\nb","featuredImage":"` + testImage + `",` +
		`"seo":{"title":"x","description":"d","canonical":"https://example.com/a"},` +
		`"fields":{` +
		`"colors":["赤"],` +
		`"description":[{"type":"text","text":"x"}],` +
		`"details":[{"type":"paragraph","id":"b_00000001","content":[{"type":"text","text":"\u00e9"}]}],` +
		`"matrix":[[1,2]],` +
		`"name":"Bottle",` +
		`"price":"cheap",` +
		`"shop":{"href":"https://example.com/","newTab":true},` +
		`"size":"M",` +
		`"spec":{"weight":1},` +
		`"summary":"a\nb\nc",` +
		`"unknown":"\u0001",` +
		`"variants":[{"code":"A","sizes":["S"]}]},` +
		`"body":[{"type":"paragraph","id":"b_00000002","content":[{"type":"text","text":"a"}]}]`)
	r := mustRevision(t, in)
	r.Normalize(testSite().Type("product"))
	assertJSON(t, r, want)

	t.Run("without type", func(t *testing.T) {
		r := mustRevision(t, revFields(`"name":"Bottle\u0001","inStock":false`))
		r.Normalize(nil)
		assertJSON(t, r, revFields(`"inStock":false,"name":"Bottle\u0001"`))
	})
	t.Run("nothing left in fields", func(t *testing.T) {
		r := mustRevision(t, revFields(`"name":"\u0001","inStock":false`))
		r.Normalize(testSite().Type("product"))
		if r.Fields != nil {
			t.Errorf("Fields = %#v, want nil", r.Fields)
		}
	})
	t.Run("values of other types", func(t *testing.T) {
		in := revFields(`"colors":"赤","description":"a","details":"b","matrix":{"a":1},"name":["c"],"shop":"d","spec":["e"],"summary":1,"variants":[1]`)
		r := mustRevision(t, in)
		r.Normalize(testSite().Type("product"))
		assertJSON(t, r, in)
	})
}

// assertJSON checks that v marshals as want, ignoring the spaces of want.
func assertJSON(t *testing.T, v any, want string) {
	t.Helper()
	got, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal(%#v) = %v", v, err)
	}
	w := jsontext.Value(want)
	if err := w.Format(); err != nil {
		t.Fatalf("Format(%s) = %v", want, err)
	}
	if string(got) != string(w) {
		t.Errorf("Marshal() = %s\nwant %s", got, w)
	}
}
