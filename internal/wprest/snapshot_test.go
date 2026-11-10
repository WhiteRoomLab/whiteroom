package wprest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

// fullSnapshot has a value in every field, in the order of a snapshot.
func fullSnapshot() *Snapshot {
	return &Snapshot{
		API: "https://example.com/wp-json/",
		Index: Index{
			Name: "テストのサイト", Description: "どこにでもあるサイト", URL: "https://example.com", Home: "https://example.com/blog",
			GMTOffset: "5.5", TimezoneString: "Asia/Tokyo", ShowOnFront: "page", PageOnFront: 7, PageForPosts: 9, SiteLogo: 11, SiteIcon: 12,
		},
		Taxonomies: []Taxonomy{
			{Slug: "category", Name: "カテゴリー", Hierarchical: true, RestBase: "categories", RestNamespace: "wp/v2", Types: []string{"post"}},
			{Slug: "genre", Name: "ジャンル", RestBase: "genres", Types: []string{"post", "works"}},
			{Slug: "post_tag", Name: "タグ", RestBase: "tags", Types: []string{"post"}},
		},
		Types: []Type{
			{Slug: "books", Name: "本", HasArchive: Archive{Set: true, On: true, Slug: "library"}, RestBase: "books", Taxonomies: []string{"genre"}},
			{Slug: "page", Name: "固定ページ", Hierarchical: true, HasArchive: Archive{Set: true}, RestBase: "pages", RestNamespace: "wp/v2"},
			{Slug: "post", Name: "投稿", RestBase: "posts", Taxonomies: []string{"category", "post_tag"}},
			{Slug: "works", Name: "作品", HasArchive: Archive{Set: true, On: true}, RestBase: "works"},
		},
		Terms: []Term{
			{ID: 1, Taxonomy: "category", Slug: "uncategorized", Name: "未分類", Link: "https://example.com/blog/category/uncategorized/"},
			{ID: 5, Taxonomy: "category", Slug: "%e3%83%8b%e3%83%a5%e3%83%bc%e3%82%b9", Name: "ニュース &amp; お知らせ", Parent: 1},
			{ID: 3, Taxonomy: "genre", Slug: "novel", Name: "小説"},
			{ID: 4, Taxonomy: "post_tag", Slug: "tag-1", Name: "<タグ>"},
		},
		Users: []User{
			{ID: 1, Name: "管理人", Slug: "admin", Description: "<p>こんにちは</p>", URL: "https://example.net", Link: "https://example.com/blog/author/admin/"},
			{ID: 2, Name: "A &amp; B", Slug: "%e3%81%82"},
		},
		Unavailable: []Unavailable{
			{Taxonomy: "weird", Code: "invalid_route"},
			{Route: "/wp/v2/menus", Status: 404, Code: "rest_no_route"},
			{Route: "/wp/v2/users", Status: 200, Code: "not_json"},
		},
	}
}

const fullJSON = `{
  "api": "https://example.com/wp-json/",
  "index": {
    "name": "テストのサイト",
    "description": "どこにでもあるサイト",
    "url": "https://example.com",
    "home": "https://example.com/blog",
    "gmt_offset": "5.5",
    "timezone_string": "Asia/Tokyo",
    "show_on_front": "page",
    "page_on_front": 7,
    "page_for_posts": 9,
    "site_logo": 11,
    "site_icon": 12
  },
  "taxonomies": [
    {
      "slug": "category",
      "name": "カテゴリー",
      "hierarchical": true,
      "rest_base": "categories",
      "rest_namespace": "wp/v2",
      "types": [
        "post"
      ]
    },
    {
      "slug": "genre",
      "name": "ジャンル",
      "rest_base": "genres",
      "types": [
        "post",
        "works"
      ]
    },
    {
      "slug": "post_tag",
      "name": "タグ",
      "rest_base": "tags",
      "types": [
        "post"
      ]
    }
  ],
  "types": [
    {
      "slug": "books",
      "name": "本",
      "has_archive": "library",
      "rest_base": "books",
      "taxonomies": [
        "genre"
      ]
    },
    {
      "slug": "page",
      "name": "固定ページ",
      "hierarchical": true,
      "has_archive": false,
      "rest_base": "pages",
      "rest_namespace": "wp/v2"
    },
    {
      "slug": "post",
      "name": "投稿",
      "rest_base": "posts",
      "taxonomies": [
        "category",
        "post_tag"
      ]
    },
    {
      "slug": "works",
      "name": "作品",
      "has_archive": true,
      "rest_base": "works"
    }
  ],
  "terms": [
    {
      "id": 1,
      "taxonomy": "category",
      "slug": "uncategorized",
      "name": "未分類",
      "link": "https://example.com/blog/category/uncategorized/"
    },
    {
      "id": 5,
      "taxonomy": "category",
      "slug": "%e3%83%8b%e3%83%a5%e3%83%bc%e3%82%b9",
      "name": "ニュース &amp; お知らせ",
      "parent": 1
    },
    {
      "id": 3,
      "taxonomy": "genre",
      "slug": "novel",
      "name": "小説"
    },
    {
      "id": 4,
      "taxonomy": "post_tag",
      "slug": "tag-1",
      "name": "<タグ>"
    }
  ],
  "users": [
    {
      "id": 1,
      "name": "管理人",
      "slug": "admin",
      "description": "<p>こんにちは</p>",
      "url": "https://example.net",
      "link": "https://example.com/blog/author/admin/"
    },
    {
      "id": 2,
      "name": "A &amp; B",
      "slug": "%e3%81%82"
    }
  ],
  "unavailable": [
    {
      "taxonomy": "weird",
      "code": "invalid_route"
    },
    {
      "route": "/wp/v2/menus",
      "status": 404,
      "code": "rest_no_route"
    },
    {
      "route": "/wp/v2/users",
      "status": 200,
      "code": "not_json"
    }
  ]
}
`

func readString(in string, opts Options) (*Snapshot, error) {
	return ReadSnapshot(strings.NewReader(in), opts)
}

func writeString(t *testing.T, s *Snapshot) string {
	t.Helper()
	var buf bytes.Buffer
	if err := s.WriteJSON(&buf); err != nil {
		t.Fatalf("WriteJSON() = %v", err)
	}
	return buf.String()
}

// mustRead reads in, and fails the test if that fails.
func mustRead(t *testing.T, in string) *Snapshot {
	t.Helper()
	s, err := readString(in, Options{})
	if err != nil {
		t.Fatalf("ReadSnapshot(%q) = %v", in, err)
	}
	return s
}

func TestSnapshotRoundTrip(t *testing.T) {
	if got := writeString(t, fullSnapshot()); got != fullJSON {
		t.Errorf("WriteJSON() =\n%s\nwant\n%s", got, fullJSON)
	}
	s := mustRead(t, fullJSON)
	if want := fullSnapshot(); !reflect.DeepEqual(s, want) {
		t.Errorf("ReadSnapshot() =\n%s\nwant\n%s", writeString(t, s), writeString(t, want))
	}
}

func TestSnapshotEmpty(t *testing.T) {
	const empty = "{\n  \"index\": {}\n}\n"
	if got := writeString(t, &Snapshot{}); got != empty {
		t.Errorf("WriteJSON() = %q, want %q", got, empty)
	}
	if s := mustRead(t, empty); !reflect.DeepEqual(s, &Snapshot{}) {
		t.Errorf("ReadSnapshot() = %+v, want nothing", s)
	}
	// Lists with nothing in them, and null, are the same as no lists.
	in := `{"index":{},"taxonomies":[],"types":null,"terms":[],"users":[],"unavailable":[]}`
	if s := mustRead(t, in); !reflect.DeepEqual(s, &Snapshot{}) {
		t.Errorf("ReadSnapshot(%s) = %+v, want nothing", in, s)
	}
}

func TestSnapshotOrder(t *testing.T) {
	in := `{
  "unavailable": [{"route": "/wp/v2/users", "status": 401}, {"route": "/a", "taxonomy": "z"}, {"route": "/a", "taxonomy": "b", "status": 300}, {"status": 500}, {"route": "/a", "taxonomy": "b"}],
  "users": [{"id": 20}, {"id": 3}, {"id": 100}],
  "terms": [
    {"id": 9, "taxonomy": "post_tag"}, {"id": 10, "taxonomy": "category"}, {"id": 2, "taxonomy": "post_tag"}, {"id": 2, "taxonomy": "category"}
  ],
  "types": [{"slug": "post"}, {"slug": "Page"}, {"slug": "attachment"}],
  "taxonomies": [{"slug": "post_tag"}, {"slug": "category"}],
  "index": {}
}`
	s := mustRead(t, in)
	var got []string
	for _, tx := range s.Taxonomies {
		got = append(got, "taxonomy "+tx.Slug)
	}
	for _, ty := range s.Types {
		got = append(got, "type "+ty.Slug)
	}
	for _, tm := range s.Terms {
		got = append(got, fmt.Sprintf("term %s %d", tm.Taxonomy, tm.ID))
	}
	for _, u := range s.Users {
		got = append(got, fmt.Sprintf("user %d", u.ID))
	}
	for _, u := range s.Unavailable {
		got = append(got, fmt.Sprintf("unavailable %q %q %d", u.Route, u.Taxonomy, u.Status))
	}
	// The slugs are in the order of the bytes, so Page is before attachment.
	want := []string{
		"taxonomy category", "taxonomy post_tag",
		"type Page", "type attachment", "type post",
		"term category 2", "term category 10", "term post_tag 2", "term post_tag 9",
		"user 3", "user 20", "user 100",
		`unavailable "" "" 500`, `unavailable "/a" "b" 0`, `unavailable "/a" "b" 300`, `unavailable "/a" "z" 0`,
		`unavailable "/wp/v2/users" "" 401`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadSnapshot() has\n%q\nwant\n%q", got, want)
	}
}

func TestWriteJSONOrder(t *testing.T) {
	s := &Snapshot{
		Taxonomies: []Taxonomy{{Slug: "b"}, {Slug: "a"}},
		Users:      []User{{ID: 2}, {ID: 1}},
		Terms:      []Term{{ID: 2, Taxonomy: "a"}, {ID: 1, Taxonomy: "a"}},
	}
	want := writeString(t, &Snapshot{
		Taxonomies: []Taxonomy{{Slug: "a"}, {Slug: "b"}},
		Users:      []User{{ID: 1}, {ID: 2}},
		Terms:      []Term{{ID: 1, Taxonomy: "a"}, {ID: 2, Taxonomy: "a"}},
	})
	if got := writeString(t, s); got != want {
		t.Errorf("WriteJSON() =\n%s\nwant\n%s", got, want)
	}
	if s.Taxonomies[0].Slug != "b" || s.Users[0].ID != 2 || s.Terms[0].ID != 2 {
		t.Errorf("WriteJSON() changed the snapshot: %+v", s)
	}
}

func TestSnapshotIgnoresOtherKeys(t *testing.T) {
	in := `{
  "index": {"name": "n", "authentication": [], "routes": {"/": {"namespace": "", "methods": ["GET"]}}, "_links": {"self": [{"href": "x"}]}, "namespaces": ["wp/v2"]},
  "taxonomies": [{"slug": "category", "description": "", "visibility": {"show_ui": true}, "_links": {}, "capabilities": {}, "labels": null, "types": ["post"]}],
  "types": [{"slug": "post", "supports": {"title": true}, "viewable": true, "icon": null, "_links": {"collection": [{"href": "y"}]}}],
  "terms": [{"id": 1, "taxonomy": "category", "count": 7, "description": "d", "meta": [], "_links": {}}],
  "users": [{"id": 1, "name": "a", "avatar_urls": {"24": "https://secure.gravatar.com/avatar/x"}, "meta": [], "_links": {}}],
  "unavailable": [{"route": "/wp/v2/users", "message": "not allowed"}]
}`
	want := &Snapshot{
		Index:       Index{Name: "n"},
		Taxonomies:  []Taxonomy{{Slug: "category", Types: []string{"post"}}},
		Types:       []Type{{Slug: "post"}},
		Terms:       []Term{{ID: 1, Taxonomy: "category"}},
		Users:       []User{{ID: 1, Name: "a"}},
		Unavailable: []Unavailable{{Route: "/wp/v2/users"}},
	}
	if s := mustRead(t, in); !reflect.DeepEqual(s, want) {
		t.Errorf("ReadSnapshot() =\n%s\nwant\n%s", writeString(t, s), writeString(t, want))
	}

	// null is the same as a key that is not there, wherever it is.
	in = `{"api": null, "index": {"name": null, "gmt_offset": null, "page_on_front": null, "site_logo": null},
  "taxonomies": [{"slug": "a", "name": null, "hierarchical": null, "types": null}],
  "types": [{"slug": "a", "has_archive": null, "taxonomies": null}],
  "terms": [{"id": 1, "taxonomy": "a", "parent": null, "link": null}],
  "users": [{"id": 1, "url": null}], "unavailable": [{"status": null, "route": null}]}`
	want = &Snapshot{
		Taxonomies: []Taxonomy{{Slug: "a"}}, Types: []Type{{Slug: "a"}}, Terms: []Term{{ID: 1, Taxonomy: "a"}},
		Users: []User{{ID: 1}}, Unavailable: []Unavailable{{}},
	}
	if s := mustRead(t, in); !reflect.DeepEqual(s, want) {
		t.Errorf("ReadSnapshot() =\n%s\nwant\n%s", writeString(t, s), writeString(t, want))
	}

	_, err := readString(`{"index": {}, "extra": null}`, Options{})
	checkError(t, err, "/extra", "unknown key")
}

// checkError checks that err is an *Error with the pointer and the message.
func checkError(t *testing.T, err error, pointer, msg string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Errorf("error = %v (%T), want an *Error at %q: %s", err, err, pointer, msg)
		return
	}
	if e.Pointer != pointer || e.Msg != msg {
		t.Errorf("error = %q at %q, want %q at %q", e.Msg, e.Pointer, msg, pointer)
	}
}

// snapshotErrors are files that ReadSnapshot rejects, with the pointer and the message of the error.
var snapshotErrors = []struct{ name, in, pointer, msg string }{
	{"empty", ``, "", "unexpected end of JSON input"},
	{"white space", " \n", "", "unexpected end of JSON input"},
	{"array", `[]`, "", "expected object, got array"},
	{"null", `null`, "", "expected object, got null"},
	{"string", `"index"`, "", "expected object, got string"},
	{"no index", `{}`, "", `missing key "index"`},
	{"null index", `{"index": null}`, "", `missing key "index"`},
	{"api only", `{"api": "https://example.com/wp-json/"}`, "", `missing key "index"`},
	{"data after", `{"index": {}} x`, "", "unexpected data after value"},
	{"two objects", `{"index": {}}{"index": {}}`, "", "unexpected data after value"},
	{"truncated", `{"index": {"name": "a"`, "/index", "unexpected end of JSON input"},
	{"missing value", `{"index": }`, "/index", "missing value after object name"},
	{"unknown key", `{"index": {}, "foo": 1}`, "/foo", "unknown key"},
	{"unknown key in uppercase", `{"index": {}, "Terms": []}`, "/Terms", "unknown key"},
	{"duplicate top key", `{"index": {}, "index": {}}`, "/index", "duplicate key"},
	{"duplicate key in index", `{"index": {"name": "a", "name": "b"}}`, "/index/name", "duplicate key"},
	{"duplicate ID", `{"index": {}, "terms": [{"id": 1, "id": 2, "taxonomy": "a"}]}`, "/terms/0/id", "duplicate key"},
	{"duplicate key that is ignored", `{"index": {}, "users": [{"id": 1, "meta": {"a": 1, "a": 2}}]}`, "/users/0/meta/a", "duplicate key"},
	{"api is not a string", `{"api": 1, "index": {}}`, "/api", "expected string, got number"},
	{"index is an array", `{"index": []}`, "/index", "expected object, got array"},
	{"name is not a string", `{"index": {"name": 5}}`, "/index/name", "expected string, got number"},
	{"name is an object", `{"index": {"name": {}}}`, "/index/name", "expected string, got object"},
	{"gmt_offset is a boolean", `{"index": {"gmt_offset": true}}`, "/index/gmt_offset", "expected number or string, got boolean"},
	{"gmt_offset is an array", `{"index": {"gmt_offset": [9]}}`, "/index/gmt_offset", "expected number or string, got array"},
	{"page_on_front is negative", `{"index": {"page_on_front": -1}}`, "/index/page_on_front", "invalid page_on_front -1: use an integer from 0"},
	{"page_for_posts is negative", `{"index": {"page_for_posts": -3}}`, "/index/page_for_posts", "invalid page_for_posts -3: use an integer from 0"},
	{"site_logo is a fraction", `{"index": {"site_logo": 1.5}}`, "/index/site_logo", "must be an integer"},
	{"site_icon is an exponent", `{"index": {"site_icon": 1e2}}`, "/index/site_icon", "must be an integer"},
	{"site_icon is too large", `{"index": {"site_icon": 123456789012345678901234567890}}`, "/index/site_icon", "must be an integer"},
	{"site_icon is true", `{"index": {"site_icon": true}}`, "/index/site_icon", "expected number, got boolean"},
	{"site_icon is text", `{"index": {"site_icon": "abc"}}`, "/index/site_icon", `invalid site_icon "abc": use an integer from 0`},
	{"site_icon is a negative string", `{"index": {"site_icon": "-5"}}`, "/index/site_icon", `invalid site_icon "-5": use an integer from 0`},
	{"site_icon is a signed string", `{"index": {"site_icon": "+5"}}`, "/index/site_icon", `invalid site_icon "+5": use an integer from 0`},
	{"site_icon is a string with spaces", `{"index": {"site_icon": " 5"}}`, "/index/site_icon", `invalid site_icon " 5": use an integer from 0`},
	{"terms is an object", `{"index": {}, "terms": {}}`, "/terms", "expected array, got object"},
	{"term is a number", `{"index": {}, "terms": [1]}`, "/terms/0", "expected object, got number"},
	{"term is null", `{"index": {}, "terms": [null]}`, "/terms/0", "expected object, got null"},
	{"second term is an array", `{"index": {}, "terms": [{"id": 1, "taxonomy": "a"}, []]}`, "/terms/1", "expected object, got array"},
	{"term ID is a string", `{"index": {}, "terms": [{"id": "5", "taxonomy": "a"}]}`, "/terms/0/id", "expected number, got string"},
	{"term ID is 0", `{"index": {}, "terms": [{"id": 0, "taxonomy": "a"}]}`, "/terms/0/id", "invalid id 0: use an integer from 1"},
	{"term ID is negative", `{"index": {}, "terms": [{"id": -5, "taxonomy": "a"}]}`, "/terms/0/id", "invalid id -5: use an integer from 1"},
	{"term ID is a fraction", `{"index": {}, "terms": [{"id": 1.5, "taxonomy": "a"}]}`, "/terms/0/id", "must be an integer"},
	{"term without an ID", `{"index": {}, "terms": [{"taxonomy": "a"}]}`, "/terms/0", `missing key "id"`},
	{"term with a null ID", `{"index": {}, "terms": [{"id": null, "taxonomy": "a"}]}`, "/terms/0", `missing key "id"`},
	{"term without a taxonomy", `{"index": {}, "terms": [{"id": 1}]}`, "/terms/0", `missing key "taxonomy"`},
	{"term with an empty taxonomy", `{"index": {}, "terms": [{"id": 1, "taxonomy": ""}]}`, "/terms/0", `missing key "taxonomy"`},
	{"term parent is negative", `{"index": {}, "terms": [{"id": 1, "taxonomy": "a", "parent": -1}]}`, "/terms/0/parent", "invalid parent -1: use an integer from 0"},
	{"term name is a number", `{"index": {}, "terms": [{"id": 1, "taxonomy": "a", "name": 1}]}`, "/terms/0/name", "expected string, got number"},
	{"duplicate term", `{"index": {}, "terms": [{"id": 1, "taxonomy": "a"}, {"id": 2, "taxonomy": "a"}, {"id": 1, "taxonomy": "a", "slug": "x"}]}`, "/terms/2", "duplicate term 1 of a"},
	{"user without an ID", `{"index": {}, "users": [{"name": "x"}]}`, "/users/0", `missing key "id"`},
	{"user ID is 0", `{"index": {}, "users": [{"id": 0}]}`, "/users/0/id", "invalid id 0: use an integer from 1"},
	{"user name is a boolean", `{"index": {}, "users": [{"id": 1, "name": true}]}`, "/users/0/name", "expected string, got boolean"},
	{"duplicate user", `{"index": {}, "users": [{"id": 3}, {"id": 3, "name": "x"}]}`, "/users/1", "duplicate user 3"},
	{"taxonomy without a slug", `{"index": {}, "taxonomies": [{}]}`, "/taxonomies/0", `missing key "slug"`},
	{"taxonomy with an empty slug", `{"index": {}, "taxonomies": [{"slug": ""}]}`, "/taxonomies/0", `missing key "slug"`},
	{"taxonomy hierarchical is a string", `{"index": {}, "taxonomies": [{"slug": "a", "hierarchical": "yes"}]}`, "/taxonomies/0/hierarchical", "expected boolean, got string"},
	{"taxonomy types is a string", `{"index": {}, "taxonomies": [{"slug": "a", "types": "post"}]}`, "/taxonomies/0/types", "expected array, got string"},
	{"taxonomy type is a number", `{"index": {}, "taxonomies": [{"slug": "a", "types": ["post", 1]}]}`, "/taxonomies/0/types/1", "expected string, got number"},
	{"taxonomy type is null", `{"index": {}, "taxonomies": [{"slug": "a", "types": [null]}]}`, "/taxonomies/0/types/0", "expected string, got null"},
	{"duplicate taxonomy", `{"index": {}, "taxonomies": [{"slug": "a"}, {"slug": "b"}, {"slug": "a"}]}`, "/taxonomies/2", `duplicate taxonomy "a"`},
	{"type without a slug", `{"index": {}, "types": [{"name": "x"}]}`, "/types/0", `missing key "slug"`},
	{"has_archive is a number", `{"index": {}, "types": [{"slug": "a", "has_archive": 1}]}`, "/types/0/has_archive", "expected boolean or string, got number"},
	{"has_archive is an object", `{"index": {}, "types": [{"slug": "a", "has_archive": {}}]}`, "/types/0/has_archive", "expected boolean or string, got object"},
	{"duplicate type", `{"index": {}, "types": [{"slug": "a"}, {"slug": "a"}]}`, "/types/1", `duplicate type "a"`},
	{"route without a slash", `{"index": {}, "unavailable": [{"route": "wp/v2/users"}]}`, "/unavailable/0/route", `invalid route "wp/v2/users": use a path that starts with "/"`},
	{"status 99", `{"index": {}, "unavailable": [{"status": 99}]}`, "/unavailable/0/status", "invalid status 99: use 0 or an integer from 100 to 599"},
	{"status 600", `{"index": {}, "unavailable": [{"status": 600}]}`, "/unavailable/0/status", "invalid status 600: use 0 or an integer from 100 to 599"},
	{"status -1", `{"index": {}, "unavailable": [{"status": -1}]}`, "/unavailable/0/status", "invalid status -1: use 0 or an integer from 100 to 599"},
	{"status is a string", `{"index": {}, "unavailable": [{"status": "404"}]}`, "/unavailable/0/status", "expected number, got string"},
	{"code is a number", `{"index": {}, "unavailable": [{"code": 5}]}`, "/unavailable/0/code", "expected string, got number"},
	{"control character in a name", "{\"index\": {\"name\": \"a\x01\"}}", "/index/name", "invalid character '\\x01' in string (expecting non-control character)"},
}

func TestReadSnapshotErrors(t *testing.T) {
	for _, tt := range snapshotErrors {
		t.Run(tt.name, func(t *testing.T) {
			s, err := readString(tt.in, Options{})
			if s != nil {
				t.Errorf("ReadSnapshot() returned a Snapshot with the error %v", err)
			}
			if err == nil {
				t.Fatalf("ReadSnapshot(%s) = nil, want the error %q at %q", tt.in, tt.msg, tt.pointer)
			}
			checkError(t, err, tt.pointer, tt.msg)
		})
	}
}

func TestReadSnapshotInvalidText(t *testing.T) {
	for _, tt := range []struct{ name, in, pointer, msg string }{
		{"invalid UTF-8", "{\"index\": {\"name\": \"a\xffb\"}}", "/index/name", "invalid UTF-8"},
		{"invalid UTF-8 in a key", "{\"index\": {\"na\xffme\": 1}}", "/index", "invalid UTF-8"},
		{"invalid UTF-8 in an ignored value", "{\"index\": {\"routes\": \"\xc0\"}}", "/index/routes", "invalid UTF-8"},
		{"lone surrogate", `{"index": {"name": "` + `\` + `ud800"}}`, "/index/name", "invalid surrogate pair"},
		{"byte order mark", "\xef\xbb\xbf{\"index\": {}}", "", "invalid character"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := readString(tt.in, Options{})
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("ReadSnapshot() = %v (%T), want an *Error", err, err)
			}
			if e.Pointer != tt.pointer || !strings.Contains(e.Msg, tt.msg) {
				t.Errorf("ReadSnapshot() = %q at %q, want %q at %q", e.Msg, e.Pointer, tt.msg, tt.pointer)
			}
		})
	}
}

func TestReadSnapshotLimits(t *testing.T) {
	terms := func(n int) string {
		var items []string
		for i := range n {
			items = append(items, fmt.Sprintf(`{"id": %d, "taxonomy": "a"}`, i+1))
		}
		return `{"index": {}, "terms": [` + strings.Join(items, ",") + `]}`
	}
	if _, err := readString(terms(3), Options{MaxTerms: 3}); err != nil {
		t.Errorf("ReadSnapshot() with 3 terms and the limit 3 = %v", err)
	}
	_, err := readString(terms(4), Options{MaxTerms: 3})
	checkError(t, err, "/terms/3", "too many terms: at least 4 (max 3)")

	_, err = readString(`{"index": {}, "users": [{"id": 1}, {"id": 2}]}`, Options{MaxUsers: 1})
	checkError(t, err, "/users/1", "too many users: at least 2 (max 1)")
	if _, err := readString(`{"index": {}, "users": [{"id": 1}, {"id": 2}]}`, Options{MaxUsers: 2}); err != nil {
		t.Errorf("ReadSnapshot() with 2 users and the limit 2 = %v", err)
	}
	_, err = readString(`{"index": {}, "taxonomies": [{"slug": "a"}, {"slug": "b"}, {"slug": "c"}]}`, Options{MaxTaxonomies: 2})
	checkError(t, err, "/taxonomies/2", "too many taxonomies: at least 3 (max 2)")
	_, err = readString(`{"index": {}, "types": [{"slug": "a"}, {"slug": "b"}]}`, Options{MaxTaxonomies: 1})
	checkError(t, err, "/types/1", "too many types: at least 2 (max 1)")
	// A list of strings has no limit of its own.
	if _, err := readString(`{"index": {}, "taxonomies": [{"slug": "a", "types": ["a", "b", "c", "d"]}]}`, Options{MaxTaxonomies: 1}); err != nil {
		t.Errorf("ReadSnapshot() = %v, want the types of a taxonomy not to count", err)
	}

	in := fullJSON
	n := int64(len(in))
	if _, err := readString(in, Options{MaxSnapshotBytes: n}); err != nil {
		t.Errorf("ReadSnapshot() with the size of the file as the limit = %v", err)
	}
	_, err = readString(in, Options{MaxSnapshotBytes: n - 1})
	checkError(t, err, "", fmt.Sprintf("too large: at least %d bytes (max %d)", n, n-1))
}

// TestSnapshotCheck tests the limits that check has for a snapshot that is not read, such as the one that Fetch makes.
func TestSnapshotCheck(t *testing.T) {
	s := &Snapshot{Terms: []Term{{ID: 1, Taxonomy: "a"}, {ID: 2, Taxonomy: "a"}, {ID: 3, Taxonomy: "a"}}}
	checkError(t, s.check(Options{MaxTerms: 2}.withDefaults()), "/terms/2", "too many terms: at least 3 (max 2)")
	if err := s.check(Options{MaxTerms: 3}.withDefaults()); err != nil {
		t.Errorf("check() = %v", err)
	}
	s = &Snapshot{Users: []User{{ID: 1}, {ID: 2}}, Taxonomies: []Taxonomy{{Slug: "a"}, {Slug: "b"}}, Types: []Type{{Slug: "a"}, {Slug: "b"}}}
	checkError(t, s.check(Options{MaxUsers: 1}.withDefaults()), "/users/1", "too many users: at least 2 (max 1)")
	checkError(t, s.check(Options{MaxTaxonomies: 1}.withDefaults()), "/taxonomies/1", "too many taxonomies: at least 2 (max 1)")
	s.Taxonomies = nil
	checkError(t, s.check(Options{MaxTaxonomies: 1}.withDefaults()), "/types/1", "too many types: at least 2 (max 1)")
	s = &Snapshot{Terms: []Term{{ID: 1, Taxonomy: "a"}, {ID: 1, Taxonomy: "b"}, {ID: 1, Taxonomy: "a"}}}
	checkError(t, s.check(Options{}.withDefaults()), "/terms/2", "duplicate term 1 of a")
}

func TestSnapshotIndexValues(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want Index
	}{
		{`{}`, Index{}},
		{`{"gmt_offset": 9}`, Index{GMTOffset: "9"}},
		{`{"gmt_offset": "9"}`, Index{GMTOffset: "9"}},
		{`{"gmt_offset": 5.5}`, Index{GMTOffset: "5.5"}},
		{`{"gmt_offset": -3.5}`, Index{GMTOffset: "-3.5"}},
		{`{"gmt_offset": 5.50}`, Index{GMTOffset: "5.50"}},
		{`{"gmt_offset": ""}`, Index{}},
		{`{"gmt_offset": 0}`, Index{GMTOffset: "0"}},
		{`{"site_logo": 12, "site_icon": "13"}`, Index{SiteLogo: 12, SiteIcon: 13}},
		{`{"site_logo": false, "site_icon": ""}`, Index{}},
		{`{"site_logo": 0, "site_icon": null, "page_on_front": "0", "page_for_posts": "00012"}`, Index{PageForPosts: 12}},
		{`{"show_on_front": "page", "page_on_front": 5, "page_for_posts": 6}`, Index{ShowOnFront: "page", PageOnFront: 5, PageForPosts: 6}},
		{`{"timezone_string": "Asia/Tokyo", "url": "https://a.example", "home": "https://b.example", "description": "d"}`,
			Index{TimezoneString: "Asia/Tokyo", URL: "https://a.example", Home: "https://b.example", Description: "d"}},
	} {
		s := mustRead(t, `{"index": `+tt.in+`}`)
		if s.Index != tt.want {
			t.Errorf("ReadSnapshot(%s).Index = %+v, want %+v", tt.in, s.Index, tt.want)
		}
		// What is written is read as the same.
		if again := mustRead(t, writeString(t, s)); again.Index != tt.want {
			t.Errorf("the index of %s after writing and reading is %+v, want %+v", tt.in, again.Index, tt.want)
		}
	}
}

func TestSnapshotArchive(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want Archive
		out  string // what is written for it, or "" if nothing is
	}{
		{`false`, Archive{Set: true}, `false`},
		{`true`, Archive{Set: true, On: true}, `true`},
		{`"library"`, Archive{Set: true, On: true, Slug: "library"}, `"library"`},
		{`""`, Archive{Set: true}, `false`},
		{`null`, Archive{}, ""},
		{``, Archive{}, ""},
	} {
		in := `{"slug": "a"}`
		if tt.in != "" {
			in = `{"slug": "a", "has_archive": ` + tt.in + `}`
		}
		s := mustRead(t, `{"index": {}, "types": [`+in+`]}`)
		if got := s.Types[0].HasArchive; got != tt.want {
			t.Errorf("has_archive %s is %+v, want %+v", tt.in, got, tt.want)
		}
		out := writeString(t, s)
		if tt.out == "" {
			if strings.Contains(out, "has_archive") {
				t.Errorf("WriteJSON() for has_archive %s writes it: %s", tt.in, out)
			}
		} else if want := `"has_archive": ` + tt.out; !strings.Contains(out, want) {
			t.Errorf("WriteJSON() for has_archive %s = %s, want %s", tt.in, out, want)
		}
		if again := mustRead(t, out); again.Types[0].HasArchive != s.Types[0].HasArchive {
			t.Errorf("has_archive %s after writing and reading is %+v, want %+v", tt.in, again.Types[0].HasArchive, s.Types[0].HasArchive)
		}
	}
	// A value that is not from a response: without Set, there is nothing to write; with a slug that is off, it is false.
	out := writeString(t, &Snapshot{Types: []Type{{Slug: "a", HasArchive: Archive{On: true, Slug: "x"}}, {Slug: "b", HasArchive: Archive{Set: true, Slug: "x"}}}})
	if strings.Count(out, "has_archive") != 1 || !strings.Contains(out, `"has_archive": false`) {
		t.Errorf("WriteJSON() = %s, want only has_archive false", out)
	}
}

func TestSnapshotText(t *testing.T) {
	// Whatever is in a string comes back, including what JSON has to escape, what HTML does, and the line separators.
	odd := []string{
		`quote " and backslash \ and slash /`, "line\nbreak\r\n\ttab", string(rune(0)) + string(rune(1)) + string(rune(0x1f)) + string(rune(0x7f)),
		string(rune(0x2028)) + string(rune(0x2029)) + string(rune(0x85)), "<script>alert(1)</script> & &amp; &#38;", "😀 and 日本語",
		string(rune(0xfeff)) + "bom", string(rune(0xfffd)), " leading and trailing ", "%e3%81%82%zz", "é" + string(rune(0x301)),
	}
	for _, s := range odd {
		snap := &Snapshot{
			API: s, Index: Index{Name: s, Description: s, GMTOffset: s, TimezoneString: s},
			Taxonomies:  []Taxonomy{{Slug: s, Name: s, Types: []string{s}}},
			Types:       []Type{{Slug: s, Name: s, HasArchive: Archive{Set: true, On: true, Slug: s}, Taxonomies: []string{s}}},
			Terms:       []Term{{ID: 1, Taxonomy: s, Slug: s, Name: s, Link: s}},
			Users:       []User{{ID: 1, Name: s, Slug: s, Description: s, URL: s, Link: s}},
			Unavailable: []Unavailable{{Taxonomy: s, Code: s}},
		}
		out := writeString(t, snap)
		got := mustRead(t, out)
		if !reflect.DeepEqual(got, snap) {
			t.Errorf("%q: ReadSnapshot(WriteJSON()) =\n%+v\nwant\n%+v", s, got, snap)
		}
		if again := writeString(t, got); again != out {
			t.Errorf("%q: WriteJSON() after reading differs:\n%s\nwant\n%s", s, again, out)
		}
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestSnapshotIOErrors(t *testing.T) {
	boom := errors.New("boom")
	if _, err := ReadSnapshot(failingReader{boom}, Options{}); !errors.Is(err, boom) {
		t.Errorf("ReadSnapshot() = %v, want %v", err, boom)
	}
	if err := fullSnapshot().WriteJSON(failingWriter{boom}); !errors.Is(err, boom) {
		t.Errorf("WriteJSON() = %v, want %v", err, boom)
	}
	if err := fullSnapshot().WriteJSON(io.Discard); err != nil {
		t.Errorf("WriteJSON() = %v", err)
	}
	// A string that is not UTF-8 cannot be written, and it is not changed into another.
	if err := (&Snapshot{Index: Index{Name: "a\xffb"}}).WriteJSON(io.Discard); err == nil {
		t.Error("WriteJSON() of a string that is not UTF-8 = nil, want an error")
	}
}

func TestOptionsWithDefaults(t *testing.T) {
	want := Options{
		Timeout: Timeout, MaxResponseBytes: MaxResponseBytes, MaxSnapshotBytes: MaxSnapshotBytes,
		MaxTerms: MaxTerms, MaxUsers: MaxUsers, MaxTaxonomies: MaxTaxonomies,
	}
	if got := (Options{}).withDefaults(); got != want {
		t.Errorf("Options{}.withDefaults() = %+v, want %+v", got, want)
	}
	if got := (Options{MaxTerms: -1, MaxUsers: -5, Timeout: -1, MaxResponseBytes: -1}).withDefaults(); got != want {
		t.Errorf("Options with negative limits .withDefaults() = %+v, want %+v", got, want)
	}
	set := Options{API: "a", Timeout: 1, MaxResponseBytes: 2, MaxSnapshotBytes: 3, MaxTerms: 4, MaxUsers: 5, MaxTaxonomies: 6}
	if got := set.withDefaults(); got != set {
		t.Errorf("%+v.withDefaults() = %+v, want it unchanged", set, got)
	}
}
