package wpimport

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wprest"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

// reportCase is an entry, and the line that WriteText writes for it.
type reportCase struct {
	entry Entry
	line  string
}

// reportCases are in the order that Entries returns them.
func reportCases() []reportCase {
	item := Source{File: "wp.xml", Line: 20, Column: 2, Record: "item", ID: 45, Type: "post", Name: "Hello"}
	return []reportCase{
		{Entry{Kind: Changed, Message: "no source at all"}, `changed: no source at all`},
		{Entry{Kind: Review, Source: Source{Record: "channel"}, Message: "no file"}, `review: channel: no file`},
		{
			Entry{Kind: Changed, Source: Source{File: "wp-rest.json", Record: "rest"}, Message: "a record of the REST API with nothing to tell it by"},
			`wp-rest.json: changed: rest: a record of the REST API with nothing to tell it by`,
		},
		{
			Entry{Kind: Review, Source: Source{File: "wp-rest.json", Record: "rest", Type: "post_tag", Name: "no ID"}, Message: "a record with no ID, as the route of a taxonomy"},
			`wp-rest.json: review: rest (post_tag "no ID"): a record with no ID, as the route of a taxonomy`,
		},
		{
			Entry{Kind: Review, Source: Source{File: "wp-rest.json", Record: "rest", Name: "/wp/v2/users"}, Message: "a route"},
			`wp-rest.json: review: rest "/wp/v2/users": a route`,
		},
		{
			Entry{Kind: Skipped, Source: Source{File: "wp-rest.json", Record: "rest", ID: 3, Type: "category", Name: "News"}, Message: "a term of the REST API"},
			`wp-rest.json: skipped: rest 3 (category "News"): a term of the REST API`,
		},
		{
			Entry{Kind: Skipped, Source: Source{File: "wp-rest.json", Record: "rest", ID: 5, Type: "user", Name: "admin"}, Message: "a user"},
			`wp-rest.json: skipped: rest 5 (user "admin"): a user`,
		},
		{
			Entry{
				Kind: Review, Source: Source{File: "wp-rest.json", Record: "rest", ID: 5, Type: "user", Name: "admin"}, Message: "a user",
				Target: Target{File: "authors/aut_X.json", Pointer: "/slug"},
			},
			`wp-rest.json: review: rest 5 (user "admin"): a user -> authors/aut_X.json#/slug`,
		},
		{
			Entry{Kind: Review, Source: Source{File: "wp-rest.json", Record: "rest", ID: 6, Type: "user"}, Message: "a user with no name"},
			`wp-rest.json: review: rest 6 (user): a user with no name`,
		},
		{
			Entry{Kind: Review, Source: Source{File: "wp-rest.json", Record: "rest", ID: 7, Name: "no type"}, Message: "a record with no type"},
			`wp-rest.json: review: rest 7 ("no type"): a record with no type`,
		},
		{
			Entry{Kind: Skipped, Source: Source{File: "wp.xml", Record: "item", ID: 9, Type: "page", Name: "No place"}, Message: "no line"},
			`wp.xml: skipped: item 9 (page "No place"): no line`,
		},
		{
			Entry{Kind: Changed, Source: Source{File: "wp.xml", Line: 3, Column: 7, Record: "channel"}, Message: "the channel"},
			`wp.xml:3:7: changed: channel: the channel`,
		},
		{
			Entry{Kind: Skipped, Source: Source{File: "wp.xml", Line: 9, Record: "author", Name: "admin"}, Message: "no column"},
			`wp.xml:9: skipped: author "admin": no column`,
		},
		{
			Entry{Kind: Skipped, Source: Source{File: "wp.xml", Line: 10, Column: 3, Record: "author", ID: 4, Name: `ed"itor`}, Message: "the author"},
			`wp.xml:10:3: skipped: author "ed\"itor": the author`,
		},
		{
			Entry{Kind: Review, Source: Source{File: "wp.xml", Line: 11, Column: 1, Record: "author", ID: 3}, Message: "an author with the ID only"},
			`wp.xml:11:1: review: author 3: an author with the ID only`,
		},
		{
			Entry{Kind: Review, Source: Source{File: "wp.xml", Line: 11, Column: 2, Record: "author", Name: "maryexample-org"}, Message: "an author with a name only"},
			`wp.xml:11:2: review: author "maryexample-org": an author with a name only`,
		},
		{
			Entry{Kind: Review, Source: Source{File: "wp.xml", Line: 11, Column: 3, Record: "author"}, Message: "an author with neither"},
			`wp.xml:11:3: review: author: an author with neither`,
		},
		{
			Entry{Kind: Changed, Source: Source{File: "wp.xml", Line: 12, Column: 2, Record: "author", Name: "x"}, Message: "the place of a term"},
			`wp.xml:12:2: changed: author "x": the place of a term`,
		},
		{
			Entry{Kind: Changed, Source: Source{File: "wp.xml", Line: 12, Column: 2, Record: "channel"}, Message: "the place of a term"},
			`wp.xml:12:2: changed: channel: the place of a term`,
		},
		{
			Entry{Kind: Changed, Source: Source{File: "wp.xml", Line: 12, Column: 2, Record: "term", Type: "category", Name: "News"}, Message: "no ID"},
			`wp.xml:12:2: changed: term (category "News"): no ID`,
		},
		{
			Entry{Kind: Changed, Source: Source{File: "wp.xml", Line: 12, Column: 2, Record: "term", ID: 12, Type: "category", Name: "News"}, Message: "an ID"},
			`wp.xml:12:2: changed: term 12 (category "News"): an ID`,
		},
		{
			Entry{Kind: Changed, Source: Source{File: "wp.xml", Line: 19, Column: 3, Record: "item", ID: 44}, Message: "an earlier column"},
			`wp.xml:19:3: changed: item 44: an earlier column`,
		},
		{
			Entry{Kind: Changed, Source: Source{File: "wp.xml", Line: 19, Column: 9, Record: "item", ID: 43}, Message: "a later column"},
			`wp.xml:19:9: changed: item 43: a later column`,
		},
		{Entry{Kind: Skipped, Source: item, Message: "m"}, `wp.xml:20:2: skipped: item 45 (post "Hello"): m`},
		{Entry{Kind: Changed, Source: item, Message: "m"}, `wp.xml:20:2: changed: item 45 (post "Hello"): m`},
		{
			Entry{Kind: Review, Source: item, Message: "m", Target: Target{File: "entries/ent_X.json", Pointer: "/blocks/0"}},
			`wp.xml:20:2: review: item 45 (post "Hello"): m -> entries/ent_X.json#/blocks/0`,
		},
		{Entry{Kind: Review, Source: item, Message: "n"}, `wp.xml:20:2: review: item 45 (post "Hello"): n`},
		{
			Entry{Kind: Review, Source: item, Message: "n", Target: Target{File: "site/settings.json"}},
			`wp.xml:20:2: review: item 45 (post "Hello"): n -> site/settings.json`,
		},
		{
			Entry{Kind: Review, Source: item, Message: "n", Target: Target{File: "site/settings.json", Pointer: "/name"}},
			`wp.xml:20:2: review: item 45 (post "Hello"): n -> site/settings.json#/name`,
		},
		{
			Entry{Kind: Changed, Source: Source{File: "wp.xml", Line: 25, Column: 3, Record: "item", ID: 17, Type: "wpcf7_contact_form", Name: "Contact", Meta: "_mail"}, Message: "read it"},
			`wp.xml:25:3: changed: item 17 (wpcf7_contact_form "Contact") postmeta "_mail": read it`,
		},
		{
			Entry{Kind: Skipped, Source: Source{File: "wp.xml", Line: 30, Column: 3, Record: "term", ID: 5, Type: "post_tag", Name: "tag", Meta: "accent_color"}, Message: "not imported"},
			`wp.xml:30:3: skipped: term 5 (post_tag "tag") termmeta "accent_color": not imported`,
		},
		{
			Entry{Kind: Changed, Source: Source{File: "wp.xml", Line: 40, Column: 1, Record: "item", ID: 50, Type: "post", Name: "He said \"hi\"\nこんにちは"}, Message: "quoted"},
			`wp.xml:40:1: changed: item 50 (post "He said \"hi\"\nこんにちは"): quoted`,
		},
		{Entry{Kind: Skipped, Source: Source{File: "wp.xml", Line: 41, Column: 1, Record: "item", ID: 51}, Message: "only an ID"}, `wp.xml:41:1: skipped: item 51: only an ID`},
		{
			Entry{Kind: Skipped, Source: Source{File: "wp.xml", Line: 42, Column: 1, Record: "item", ID: 52, Type: "post"}, Message: "a type"},
			`wp.xml:42:1: skipped: item 52 (post): a type`,
		},
		{
			Entry{Kind: Skipped, Source: Source{File: "wp.xml", Line: 43, Column: 1, Record: "item", ID: 53, Name: "T"}, Message: "a name"},
			`wp.xml:43:1: skipped: item 53 ("T"): a name`,
		},
		{
			Entry{Kind: Skipped, Source: Source{File: "wp.xml", Line: 44, Column: 1, Record: "item", ID: 54, Type: "\n  post\n", Name: "A"}, Message: "a type with line breaks"},
			`wp.xml:44:1: skipped: item 54 ("\n  post\n" "A"): a type with line breaks`,
		},
		{
			Entry{Kind: Skipped, Source: Source{File: "wp.xml", Line: 45, Column: 1, Record: "item", ID: 55, Type: "my type"}, Message: "a type with a space"},
			`wp.xml:45:1: skipped: item 55 ("my type"): a type with a space`,
		},
		{
			Entry{Kind: Skipped, Source: Source{File: "wp.xml", Line: 46, Column: 1, Record: "item", ID: 56, Type: "Post"}, Message: "a type in upper case"},
			`wp.xml:46:1: skipped: item 56 ("Post"): a type in upper case`,
		},
		{
			Entry{Kind: Skipped, Source: Source{File: "wp.xml", Line: 47, Column: 1, Record: "term", ID: 6, Type: "my\ntaxonomy", Name: "T"}, Message: "a taxonomy with a line break"},
			`wp.xml:47:1: skipped: term 6 ("my\ntaxonomy" "T"): a taxonomy with a line break`,
		},
		{
			Entry{Kind: Skipped, Source: Source{File: "wp.xml", Line: 48, Column: 1, Record: "item", ID: 57, Type: "nav_menu_item-2", Name: "N"}, Message: "a type with all the characters that are allowed"},
			`wp.xml:48:1: skipped: item 57 (nav_menu_item-2 "N"): a type with all the characters that are allowed`,
		},
		{Entry{Kind: Review, Source: Source{File: "wp2.xml", Line: 1, Column: 1, Record: "channel"}, Message: "another file"}, `wp2.xml:1:1: review: channel: another file`},
	}
}

// shuffled returns the cases in an order that is not the sorted one, and not the same for each seed.
func shuffled(cases []reportCase, seed int) []reportCase {
	out := slices.Clone(cases)
	for i := range out {
		j := (i*seed + 7) % len(out)
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func TestReportText(t *testing.T) {
	cases := reportCases()
	var want strings.Builder
	for _, c := range cases {
		want.WriteString(c.line + "\n")
	}
	for _, seed := range []int{1, 3, 5, 11} {
		rep := NewReport(Info{File: "wp.xml"})
		order := shuffled(cases, seed)
		if slices.Equal(order, cases) {
			t.Fatalf("the order %d is the order of the report", seed)
		}
		for _, c := range order {
			rep.Add(c.entry)
		}
		var got bytes.Buffer
		if err := rep.WriteText(&got); err != nil {
			t.Fatal(err)
		}
		if got.String() != want.String() {
			t.Errorf("WriteText (order %d) =\n%s\nwant\n%s", seed, got.String(), want.String())
		}
		entries := rep.Entries()
		if len(entries) != len(cases) {
			t.Fatalf("%d entries, want %d", len(entries), len(cases))
		}
		for i, c := range cases {
			if entries[i] != c.entry {
				t.Errorf("Entries()[%d] = %+v, want %+v", i, entries[i], c.entry)
			}
		}
	}
}

func TestTypeText(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"post", "post"},
		{"post_tag", "post_tag"},
		{"nav_menu", "nav_menu"},
		{"wp-block2", "wp-block2"},
		{"Post", `"Post"`},
		{"my type", `"my type"`},
		{"a.b", `"a.b"`},
		{"a\nb", `"a\nb"`},
		{`a"b`, `"a\"b"`},
		{"投稿", `"投稿"`},
	} {
		if got := typeText(tt.in); got != tt.want {
			t.Errorf("typeText(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

// TestReportTextOneLine checks that whatever the post type, the name and the key of the meta are, an entry is one line.
func TestReportTextOneLine(t *testing.T) {
	for _, odd := range []string{"\n", "a\nb", "\r\n", "x\ry", "\n  post\n", string(rune(0x2028)), string(rune(0x85)), " post", "a-b_c9"} {
		rep := NewReport(Info{})
		rep.Add(Entry{Kind: Changed, Source: Source{File: "wp.xml", Line: 1, Column: 1, Record: "item", ID: 1, Type: odd, Name: odd, Meta: odd}, Message: "m"})
		rep.Add(Entry{Kind: Changed, Source: Source{File: "wp.xml", Line: 2, Column: 1, Record: "term", Type: odd}, Message: "m"})
		var got bytes.Buffer
		if err := rep.WriteText(&got); err != nil {
			t.Fatal(err)
		}
		if lines := strings.Split(strings.TrimSuffix(got.String(), "\n"), "\n"); len(lines) != 2 || strings.ContainsAny(got.String(), "\r") {
			t.Errorf("%q: WriteText = %q, want two lines", odd, got.String())
		}
	}
}

// TestReportTextOneLineREST checks the same for the records of the REST API.
func TestReportTextOneLineREST(t *testing.T) {
	for _, odd := range []string{"\n", "a\nb", "\r\n", "x\ry", "\n  post\n", string(rune(0x2028)), string(rune(0x85)), " post", "a-b_c9"} {
		rep := NewReport(Info{})
		rep.Add(Entry{Kind: Changed, Source: Source{File: "wp-rest.json", Record: "rest", ID: 1, Type: odd, Name: odd}, Message: "m"})
		rep.Add(Entry{Kind: Changed, Source: Source{File: "wp-rest.json", Record: "rest", Name: odd}, Message: "m"})
		var got bytes.Buffer
		if err := rep.WriteText(&got); err != nil {
			t.Fatal(err)
		}
		if lines := strings.Split(strings.TrimSuffix(got.String(), "\n"), "\n"); len(lines) != 2 || strings.ContainsAny(got.String(), "\r") {
			t.Errorf("%q: WriteText = %q, want two lines", odd, got.String())
		}
	}
}

func TestReportEmpty(t *testing.T) {
	rep := NewReport(Info{})
	var text, jsonOut bytes.Buffer
	if err := rep.WriteText(&text); err != nil || text.Len() != 0 {
		t.Errorf("WriteText = %q, %v, want nothing", text.String(), err)
	}
	if err := rep.WriteJSON(&jsonOut); err != nil {
		t.Fatal(err)
	}
	if want := "{\n  \"wxr\": {},\n  \"entries\": []\n}\n"; jsonOut.String() != want {
		t.Errorf("WriteJSON = %q, want %q", jsonOut.String(), want)
	}
	if entries := rep.Entries(); len(entries) != 0 {
		t.Errorf("Entries = %v, want none", entries)
	}
}

func TestReportJSON(t *testing.T) {
	rep := NewReport(Info{File: "wordpress-6.xml", Version: "1.2", WordPress: "6.8.3", Site: "https://example.com"})
	// Some of the cases, which are in the order of the report, and so are these.
	cases := reportCases()
	for _, pick := range []struct {
		message string
		kind    Kind
	}{{"quoted", Changed}, {"m", Review}, {"no source at all", Changed}, {"the author", Skipped}, {"read it", Changed}, {"the channel", Changed}} {
		i := slices.IndexFunc(cases, func(c reportCase) bool { return c.entry.Message == pick.message && c.entry.Kind == pick.kind })
		if i < 0 {
			t.Fatalf("no case with the message %q", pick.message)
		}
		rep.Add(cases[i].entry)
	}
	var got bytes.Buffer
	if err := rep.WriteJSON(&got); err != nil {
		t.Fatal(err)
	}
	want := `{
  "wxr": {
    "file": "wordpress-6.xml",
    "version": "1.2",
    "wordpress": "6.8.3",
    "site": "https://example.com"
  },
  "entries": [
    {
      "kind": "changed",
      "message": "no source at all"
    },
    {
      "kind": "changed",
      "source": {
        "file": "wp.xml",
        "line": 3,
        "column": 7,
        "record": "channel"
      },
      "message": "the channel"
    },
    {
      "kind": "skipped",
      "source": {
        "file": "wp.xml",
        "line": 10,
        "column": 3,
        "record": "author",
        "id": 4,
        "name": "ed\"itor"
      },
      "message": "the author"
    },
    {
      "kind": "review",
      "source": {
        "file": "wp.xml",
        "line": 20,
        "column": 2,
        "record": "item",
        "id": 45,
        "type": "post",
        "name": "Hello"
      },
      "message": "m",
      "target": {
        "file": "entries/ent_X.json",
        "pointer": "/blocks/0"
      }
    },
    {
      "kind": "changed",
      "source": {
        "file": "wp.xml",
        "line": 25,
        "column": 3,
        "record": "item",
        "id": 17,
        "type": "wpcf7_contact_form",
        "name": "Contact",
        "meta": "_mail"
      },
      "message": "read it"
    },
    {
      "kind": "changed",
      "source": {
        "file": "wp.xml",
        "line": 40,
        "column": 1,
        "record": "item",
        "id": 50,
        "type": "post",
        "name": "He said \"hi\"\nこんにちは"
      },
      "message": "quoted"
    }
  ]
}
`
	if got.String() != want {
		t.Errorf("WriteJSON =\n%s\nwant\n%s", got.String(), want)
	}
}

func TestReportJSONInvalidUTF8(t *testing.T) {
	rep := NewReport(Info{})
	rep.Add(Entry{Kind: Changed, Source: Source{Record: "item", Name: "a\xffb"}, Message: "x"})
	var got bytes.Buffer
	if err := rep.WriteJSON(&got); err != nil {
		t.Fatalf("WriteJSON = %v, want the replacement character for the invalid UTF-8", err)
	}
	if want := `"name": "a` + string(rune(0xfffd)) + `b"`; !strings.Contains(got.String(), want) {
		t.Errorf("WriteJSON = %s, want %s", got.String(), want)
	}
	var text bytes.Buffer
	if err := rep.WriteText(&text); err != nil || !strings.Contains(text.String(), `item ("a\xffb")`) {
		t.Errorf("WriteText = %q, %v, want the byte as \\xff", text.String(), err)
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestReportWriteError(t *testing.T) {
	boom := errors.New("boom")
	rep := NewReport(Info{})
	rep.Add(Entry{Kind: Review, Message: "x"})
	if err := rep.WriteText(failingWriter{boom}); !errors.Is(err, boom) {
		t.Errorf("WriteText = %v, want %v", err, boom)
	}
	if err := rep.WriteJSON(failingWriter{boom}); !errors.Is(err, boom) {
		t.Errorf("WriteJSON = %v, want %v", err, boom)
	}
	if err := rep.WriteText(io.Discard); err != nil {
		t.Error(err)
	}
}

func TestReportEntriesAreACopy(t *testing.T) {
	rep := NewReport(Info{})
	rep.Add(Entry{Kind: Review, Message: "b"})
	rep.Add(Entry{Kind: Review, Message: "a"})
	rep.Entries()[0].Message = "changed"
	if got := rep.Entries(); got[0].Message != "a" || got[1].Message != "b" {
		t.Errorf("Entries() = %+v, want a and b after changing a copy", got)
	}
}

func TestReportConcurrent(t *testing.T) {
	const workers, each = 8, 200
	rep := NewReport(Info{})
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for i := range each {
				rep.Add(Entry{Kind: Changed, Source: Source{File: "wp.xml", Line: i + 1, Column: w + 1, Record: "item"}, Message: fmt.Sprintf("%d/%d", w, i)})
				if i%50 == 0 {
					_ = rep.Entries()
				}
			}
		})
	}
	wg.Wait()
	entries := rep.Entries()
	if len(entries) != workers*each {
		t.Fatalf("%d entries, want %d", len(entries), workers*each)
	}
	for i, e := range entries {
		line, column := i/workers+1, i%workers+1
		if e.Source.Line != line || e.Source.Column != column || e.Message != fmt.Sprintf("%d/%d", column-1, line-1) {
			t.Fatalf("Entries()[%d] = %+v, want the line %d and the column %d", i, e, line, column)
		}
	}
}

func TestKindRank(t *testing.T) {
	kinds := []Kind{Skipped, Changed, Review}
	for i, k := range kinds {
		if k.rank() != i {
			t.Errorf("%s.rank() = %d, want %d", k, k.rank(), i)
		}
	}
	if Kind("other").rank() <= Review.rank() {
		t.Error("an unknown kind does not come after the known ones")
	}
}

func TestTargets(t *testing.T) {
	const id = "trm_0000000000AAFHG8EJHY38VMP6"
	for _, tt := range []struct {
		name string
		got  Target
		want Target
	}{
		{"a term", documentTarget(content.RefTerm, id, ""), Target{File: "terms/" + id + ".json"}},
		{"in a term", documentTarget(content.RefTerm, id, "/name"), Target{File: "terms/" + id + ".json", Pointer: "/name"}},
		{"in an author", documentTarget(content.RefAuthor, "aut_X", "/bio/ja"), Target{File: "authors/aut_X.json", Pointer: "/bio/ja"}},
		{"in an entry", documentTarget(content.RefEntry, "ent_X", "/blocks/0"), Target{File: "entries/ent_X.json", Pointer: "/blocks/0"}},
		{"in a medium", documentTarget(content.RefMedia, "med_X", "/alt"), Target{File: "media/med_X.json", Pointer: "/alt"}},
		{"in a menu", documentTarget(content.RefMenu, "mnu_X", "/items/0"), Target{File: "menus/mnu_X.json", Pointer: "/items/0"}},
		{"in a form", documentTarget(content.RefForm, "frm_X", "/fields/0"), Target{File: "forms/frm_X.json", Pointer: "/fields/0"}},
		{"in a redirect", documentTarget(content.RefRedirect, "rdr_X", "/to"), Target{File: "redirects/rdr_X.json", Pointer: "/to"}},
		{"the settings", settingsTarget(""), Target{File: "site/settings.json"}},
		{"in the settings", settingsTarget("/url"), Target{File: "site/settings.json", Pointer: "/url"}},
		{"the schema file", schemaTarget(""), Target{File: "site/schema.yaml"}},
		{"in the schema file", schemaTarget("/types/works/url"), Target{File: "site/schema.yaml", Pointer: "/types/works/url"}},
	} {
		if tt.got != tt.want {
			t.Errorf("%s: Target = %+v, want %+v", tt.name, tt.got, tt.want)
		}
	}
	// A target is the end of a line of the text, after " -> ".
	e := Entry{Kind: Changed, Source: Source{Record: "term", ID: 5, Type: "category", Name: "News"}, Message: "m", Target: documentTarget(content.RefTerm, id, "/slug")}
	if got, want := e.line(), `changed: term 5 (category "News"): m -> terms/`+id+".json#/slug"; got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
}

// TestAuthorSources checks how the report names an author: by the login, and, if the login is an email address or the
// email address of the author, by the ID, or by the slug that the login makes if there is no ID.
func TestAuthorSources(t *testing.T) {
	pos := wxr.Pos{Line: 5, Column: 2}
	author := func(id int64, login, email string) *wxr.Author {
		return &wxr.Author{Pos: pos, ID: id, Login: login, Email: email}
	}
	at := func(id int64, name string) Source {
		return Source{File: "a.xml", Line: 5, Column: 2, Record: "author", ID: id, Name: name}
	}
	for _, tt := range []struct {
		name string
		got  Source
		want Source
		text string // what the report says of it
	}{
		{"a login", AuthorSource("a.xml", author(3, "mary", "mary@example.org")), at(3, "mary"), `author "mary"`},
		{"a login with no ID", AuthorSource("a.xml", author(0, "mary", "mary@example.org")), at(0, "mary"), `author "mary"`},
		{"a login that is an email address", AuthorSource("a.xml", author(3, "mary@example.org", "")), at(3, ""), "author 3"},
		{"an email address with no ID", AuthorSource("a.xml", author(0, "mary@example.org", "")), at(0, "maryexample-org"), `author "maryexample-org"`},
		{"the email address of the author", AuthorSource("a.xml", author(8, "erin@localhost", "erin@localhost")), at(8, ""), "author 8"},
		{"the email address in other letters", AuthorSource("a.xml", author(8, "ERIN@localhost", " erin@Localhost\n")), at(8, ""), "author 8"},
		{"a login that is not the email address", AuthorSource("a.xml", author(8, "erin", "erin@localhost")), at(8, "erin"), `author "erin"`},
		{"a user that dc:creator names", authorSource("a.xml", pos, 0, "dave", ""), at(0, "dave"), `author "dave"`},
		{"a user that dc:creator names, with an email address", authorSource("a.xml", pos, 0, "dave@example.com", ""), at(0, "daveexample-com"), `author "daveexample-com"`},
		{"an email address that makes no slug", authorSource("a.xml", pos, 0, "-@-.-", ""), at(0, ""), "author"},
		{"a user of the REST API", RESTUserSource("rest.json", &wprest.User{ID: 5, Name: "Ghost"}), Source{File: "rest.json", Record: "rest", ID: 5, Type: "user", Name: "Ghost"}, `rest 5 (user "Ghost")`},
		{"a user of the REST API with an escaped name", RESTUserSource("rest.json", &wprest.User{ID: 5, Name: "Tom &amp; Jerry"}), Source{File: "rest.json", Record: "rest", ID: 5, Type: "user", Name: "Tom &amp; Jerry"}, `rest 5 (user "Tom &amp; Jerry")`},
		{"a user of the REST API whose name is an email address", RESTUserSource("rest.json", &wprest.User{ID: 5, Name: "a@b.c"}), Source{File: "rest.json", Record: "rest", ID: 5, Type: "user"}, "rest 5 (user)"},
		{"a user of the REST API whose name is an escaped email address", RESTUserSource("rest.json", &wprest.User{ID: 5, Name: "a&#64;b.c"}), Source{File: "rest.json", Record: "rest", ID: 5, Type: "user"}, "rest 5 (user)"},
	} {
		if tt.got != tt.want {
			t.Errorf("%s: Source = %+v, want %+v", tt.name, tt.got, tt.want)
		}
		if got := tt.got.what(); got != tt.text {
			t.Errorf("%s: what() = %q, want %q", tt.name, got, tt.text)
		}
	}

	// The JSON of an author with the ID only has no name.
	rep := NewReport(Info{})
	rep.Add(Entry{Kind: Review, Source: at(3, ""), Message: "m"})
	var got bytes.Buffer
	if err := rep.WriteJSON(&got); err != nil {
		t.Fatal(err)
	}
	if out := got.String(); !strings.Contains(out, `"id": 3`) || strings.Contains(out, `"name"`) {
		t.Errorf("WriteJSON = %s, want the ID and no name", out)
	}
}
