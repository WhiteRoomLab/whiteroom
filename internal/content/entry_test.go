package content

import (
	"encoding/json/v2"
	"reflect"
	"testing"
)

// aboutPageEntry is testdata/about-page-entry.json.
var aboutPageEntry = Entry{
	ID:                testAboutPage,
	Number:            2,
	Type:              TypePage,
	Language:          "ja",
	TranslationGroup:  testGroup,
	Status:            StatusPublished,
	PublishedRevision: testRevision,
	CreatedAt:         "2026-10-25T10:00:00+09:00",
	CreatedBy:         testUser,
}

func TestEntryUnmarshalJSON(t *testing.T) {
	in := readTestdata(t, "about-page-entry.json")
	bothOrders(t, in, func(t *testing.T, in string) {
		var got Entry
		if err := json.Unmarshal([]byte(in), &got); err != nil {
			t.Fatalf("Unmarshal(%s) = %v", in, err)
		}
		if !reflect.DeepEqual(got, aboutPageEntry) {
			t.Errorf("Unmarshal(%s) = %#v, want %#v", in, got, aboutPageEntry)
		}
	})
}

func TestEntryRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // "" if in is canonical
	}{
		{name: "about page", in: readTestdata(t, "about-page-entry.json")},
		{name: "schemaVersion only", in: `{"schemaVersion":1}`},
		{
			name: "any key order",
			in:   `{"status":"draft","type":"post","schemaVersion":1,"number":10}`,
			want: `{"schemaVersion":1,"number":10,"type":"post","status":"draft"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.want
			if want == "" {
				want = tt.in
			}
			assertRoundTrip[Entry](t, tt.in, want)
		})
	}
}

func TestEntryMarshalJSON(t *testing.T) {
	got, err := json.Marshal(Entry{})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"schemaVersion":1}`; string(got) != want {
		t.Errorf("Marshal(Entry{}) = %s, want %s", got, want)
	}
}

func TestEntryUnmarshalJSONError(t *testing.T) {
	tests := []struct {
		name string
		in   string
		path string
		msg  string
	}{
		{name: "empty", in: `{}`, path: "", msg: "empty object is not allowed"},
		{name: "missing schemaVersion", in: `{"id":"x"}`, path: "", msg: `missing key "schemaVersion"`},
		{name: "schemaVersion 2", in: `{"schemaVersion":2}`, path: "/schemaVersion", msg: "unsupported schemaVersion 2 (use 1)"},
		{name: "schemaVersion 0", in: `{"schemaVersion":0}`, path: "/schemaVersion", msg: "unsupported schemaVersion 0 (use 1)"},
		{name: "schemaVersion as a string", in: `{"schemaVersion":"1"}`, path: "/schemaVersion", msg: "expected number, got string"},
		{name: "schemaVersion with a fraction", in: `{"schemaVersion":1.0}`, path: "/schemaVersion", msg: "must be an integer"},
		{name: "number 0", in: `{"schemaVersion":1,"number":0}`, path: "/number", msg: "number 0 is out of range (1 or more)"},
		{name: "negative number", in: `{"schemaVersion":1,"number":-1}`, path: "/number", msg: "number -1 is out of range (1 or more)"},
		{name: "number with a fraction", in: `{"schemaVersion":1,"number":1.5}`, path: "/number", msg: "must be an integer"},
		{name: "empty id", in: `{"schemaVersion":1,"id":""}`, path: "/id", msg: "empty string is not allowed"},
		{name: "null status", in: `{"schemaVersion":1,"status":null}`, path: "/status", msg: "expected string, got null"},
		{name: "item of a revision", in: `{"schemaVersion":1,"slug":"about"}`, path: "/slug", msg: "unknown key"},
		{name: "key case", in: `{"schemaVersion":1,"ID":"x"}`, path: "/ID", msg: "unknown key"},
		{name: "duplicate key", in: `{"schemaVersion":1,"type":"post","type":"page"}`, path: "/type", msg: "duplicate key"},
		{name: "trailing data", in: `{"schemaVersion":1} {}`, path: "", msg: "unexpected data after value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Entry{ID: "old"}
			assertError(t, got.UnmarshalJSON([]byte(tt.in)), tt.path, tt.msg)
			if got != (Entry{ID: "old"}) {
				t.Errorf("UnmarshalJSON(%s) changed the entry to %#v", tt.in, got)
			}
		})
	}
}

func TestEntryValidate(t *testing.T) {
	// entry returns aboutPageEntry changed by f.
	entry := func(f func(e *Entry)) Entry {
		e := aboutPageEntry
		f(&e)
		return e
	}
	tests := []struct {
		name  string
		in    Entry
		paths []string
		msg   string // part of the error text, a "path: message" line for each problem; not checked if ""
	}{
		{name: "about page", in: aboutPageEntry},
		{name: "draft", in: entry(func(e *Entry) { e.Status, e.PublishedRevision = StatusDraft, "" })},
		{name: "scheduled", in: entry(func(e *Entry) { e.Status, e.PublishedRevision = StatusScheduled, "" })},
		{name: "archived", in: entry(func(e *Entry) { e.Status, e.PublishedRevision = StatusArchived, "" })},
		{name: "post", in: entry(func(e *Entry) { e.Type = TypePost })},
		{name: "custom type", in: entry(func(e *Entry) { e.Type, e.Language = "works", "en" })},
		{
			name:  "missing items",
			in:    Entry{},
			paths: []string{"/id", "/number", "/type", "/language", "/translationGroup", "/status", "/createdAt", "/createdBy"},
			msg: "/id: missing id\n/number: missing number\n/type: missing type\n/language: missing language\n" +
				"/translationGroup: missing translationGroup\n/status: missing status\n/createdAt: missing createdAt\n/createdBy: missing createdBy",
		},
		{
			name: "invalid items",
			in: Entry{
				ID:                testImage,
				Number:            -1,
				Type:              "news",
				Language:          "fr",
				TranslationGroup:  "tg_1",
				Status:            "private",
				PublishedRevision: testUser,
				CreatedAt:         "2026-10-25 10:00",
				CreatedBy:         testAuthor,
			},
			paths: []string{
				"/id", "/number", "/type", "/language", "/translationGroup", "/status",
				"/publishedRevision", "/publishedRevision", "/createdAt", "/createdBy",
			},
			msg: `/id: invalid entry ID "` + testImage + `"`,
		},
		{
			name:  "unknown type",
			in:    entry(func(e *Entry) { e.Type = "news" }),
			paths: []string{"/type"},
			msg:   `/type: unknown type "news"`,
		},
		{
			name:  "language not in the site settings",
			in:    entry(func(e *Entry) { e.Language = "en-US" }),
			paths: []string{"/language"},
			msg:   `/language: language "en-US" is not in the site settings (use ja, en)`,
		},
		{
			name:  "unknown status",
			in:    entry(func(e *Entry) { e.Status = "private" }),
			paths: []string{"/status", "/publishedRevision"},
			msg:   `/status: unknown status "private" (use draft, published, scheduled, archived)` + "\n/publishedRevision: not allowed unless the status is published",
		},
		{
			name:  "published without publishedRevision",
			in:    entry(func(e *Entry) { e.PublishedRevision = "" }),
			paths: []string{"/publishedRevision"},
			msg:   "/publishedRevision: missing publishedRevision of a published entry",
		},
		{
			name:  "draft with publishedRevision",
			in:    entry(func(e *Entry) { e.Status = StatusDraft }),
			paths: []string{"/publishedRevision"},
			msg:   "/publishedRevision: not allowed unless the status is published",
		},
		{
			name:  "invalid publishedRevision",
			in:    entry(func(e *Entry) { e.PublishedRevision = testUser }),
			paths: []string{"/publishedRevision"},
			msg:   `/publishedRevision: invalid revision ID "` + testUser + `"`,
		},
		{
			name:  "createdAt without an offset",
			in:    entry(func(e *Entry) { e.CreatedAt = "2026-10-25T10:00:00" }),
			paths: []string{"/createdAt"},
			msg:   `/createdAt: invalid date and time "2026-10-25T10:00:00"`,
		},
		{
			name:  "translation group of another kind",
			in:    entry(func(e *Entry) { e.TranslationGroup = testRevision }),
			paths: []string{"/translationGroup"},
			msg:   `/translationGroup: invalid translationGroup ID "` + testRevision + `": use "tg_" and a ULID`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.in.Validate(testSite())
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
