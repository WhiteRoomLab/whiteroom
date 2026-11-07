package content

import (
	"encoding/json/v2"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
)

// translateFields hold links and terms in every place that a revision can, and entries and media, which a translation
// does not move.
var translateFields = []FieldDef{
	{Name: "intro", Type: FieldRichText},
	{Name: "extra", Type: FieldBlocks},
	{Name: "more", Type: FieldLink},
	{Name: "main", Type: FieldTerm, Taxonomy: TaxonomyCategory},
	{Name: "keywords", Type: FieldTerm, Taxonomy: TaxonomyTag, Multiple: true},
	{Name: "tags", Type: FieldList, Of: &FieldDef{Type: FieldTerm, Taxonomy: TaxonomyTag}},
	{Name: "box", Type: FieldGroup, Fields: []FieldDef{
		{Name: "link", Type: FieldLink},
		{Name: "tag", Type: FieldTerm, Taxonomy: TaxonomyTag},
		{Name: "note", Type: FieldRichText},
		{Name: "title", Type: FieldText},
	}},
	{Name: "rows", Type: FieldList, Of: &FieldDef{Type: FieldGroup, Fields: []FieldDef{
		{Name: "link", Type: FieldLink},
		{Name: "title", Type: FieldText},
	}}},
	{Name: "links", Type: FieldList, Of: &FieldDef{Type: FieldLink}},
	{Name: "related", Type: FieldEntry, Multiple: true},
	{Name: "cover", Type: FieldMedia},
}

func translateSite() *Site {
	s := testSite()
	s.Types = []TypeDef{{Name: TypePost, Fields: translateFields}}
	return s
}

// entryLink is a text of a rich text with a link to the entry id.
func entryLink(id string) string {
	return `{"type":"text","text":"t","marks":[{"type":"link","attrs":{"entry":"` + id + `"}}]}`
}

// The translations into en of the entries and the terms that testLookup knows.
var (
	entryTranslations = map[string]string{
		testAboutPage:   testEnglishPage,
		testPrivacyPage: testEnglishPrivacyPage,
	}
	termTranslations = map[string]string{
		testCategory: testEnglishCategory,
		testTag:      testEnglishTag,
	}
)

func findIn(m map[string]string) func(string) (string, bool) {
	return func(id string) (string, bool) {
		to, ok := m[id]
		return to, ok
	}
}

// translateJSON returns a revision of the type of translateSite that holds links to the pages about, contact and privacy
// in every place that a link can be, and in the fields of the type, and terms: the lists of terms have a category or a
// tag, and then another. The entry and the media fields, which a translation leaves as they are, hold about.
func translateJSON(about, contact, privacy, category, news, tag, tag2 string) string {
	return rev(`,"title":"t","slug":"s",` +
		`"terms":{"category":["` + category + `","` + news + `"],"tag":["` + tag + `","` + tag2 + `"]},` +
		`"fields":{` +
		`"box":{"link":{"entry":"` + about + `"},"note":[` + entryLink(contact) + `],"tag":"` + tag + `","title":"t"},` +
		`"cover":"` + testImage + `",` +
		`"extra":[{"type":"paragraph","id":"b_000000f1","content":[` + entryLink(about) + `,` + entryLink(contact) + `]}],` +
		`"intro":[` + entryLink(about) + `,` + entryLink(contact) + `],` +
		`"keywords":["` + tag + `","` + tag2 + `"],` +
		`"links":[{"entry":"` + about + `"},{"href":"/x/"},{"entry":"` + contact + `"}],` +
		`"main":"` + category + `",` +
		`"more":{"entry":"` + about + `"},` +
		`"related":["` + testAboutPage + `","` + testContactPage + `"],` +
		`"rows":[{"link":{"entry":"` + privacy + `"},"title":"a"},{"link":{"entry":"` + contact + `"},"title":"b"}],` +
		`"tags":["` + tag + `","` + tag2 + `"]},` +
		`"body":[` +
		`{"type":"paragraph","id":"b_00000001","content":[` + entryLink(about) + `,` + entryLink(contact) + `]},` +
		`{"type":"quote","id":"b_00000002","attrs":{"citation":[` + entryLink(about) + `,` + entryLink(contact) + `]},"content":[` +
		`{"type":"paragraph","id":"b_00000003","content":[` + entryLink(contact) + `]}]},` +
		`{"type":"image","id":"b_00000004","attrs":{"media":"` + testImage + `","alt":"a","caption":[` + entryLink(about) + `],` +
		`"link":{"entry":"` + contact + `"}}},` +
		`{"type":"buttons","id":"b_00000005","content":[{"type":"button","attrs":{"label":"b","link":{"entry":"` + privacy + `"}}}]},` +
		`{"type":"group","id":"b_00000006","content":[{"type":"list","id":"b_00000007","content":[{"type":"listItem","content":[` +
		`{"type":"paragraph","id":"b_00000008","content":[` + entryLink(contact) + `]}]}]}]},` +
		`{"type":"table","id":"b_00000009","attrs":{"caption":[` + entryLink(contact) + `]},"content":[{"type":"tableRow","content":[` +
		`{"type":"tableCell","content":[` + entryLink(about) + `]}]}]}` +
		`]`)
}

// canonical returns the JSON that MarshalJSON writes for the revision in.
func canonical(t *testing.T, in string) string {
	t.Helper()
	r := mustRevision(t, in)
	data, err := r.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func warningLines(es Errors) []string {
	var lines []string
	for _, e := range es {
		lines = append(lines, e.Error())
	}
	return lines
}

func TestRevisionTranslate(t *testing.T) {
	typ := translateSite().Type(TypePost)
	r := mustRevision(t, translateJSON(testAboutPage, testContactPage, testPrivacyPage, testCategory, testNewsCategory, testTag, testTag2))
	got := r.Translate(typ, "en", findIn(entryTranslations), findIn(termTranslations))

	// The entries and the terms that have translations are replaced; the terms that have none are left out; the links to
	// entries that have none, and the fields of type entry and media, stay.
	want := translateJSON(testEnglishPage, testContactPage, testEnglishPrivacyPage, testEnglishCategory, testNewsCategory, testEnglishTag, testTag2)
	want = strings.NewReplacer(`,"`+testNewsCategory+`"`, "", `,"`+testTag2+`"`, "").Replace(want)
	want = canonical(t, want)
	if data, err := r.MarshalJSON(); err != nil || string(data) != want {
		t.Errorf("Translate() wrote\n%s\nwant\n%s\n%v", data, want, err)
	}

	link := func(path, id string) string {
		return path + `: entry "` + id + `" has no translation in en; the link still goes to it`
	}
	term := func(path, id string) string {
		return path + `: term "` + id + `" has no translation in en; it is left out`
	}
	wantWarnings := []string{
		link("/body/0/content/1/marks/0/attrs/entry", testContactPage),
		link("/body/1/attrs/citation/1/marks/0/attrs/entry", testContactPage),
		link("/body/1/content/0/content/0/marks/0/attrs/entry", testContactPage),
		link("/body/2/attrs/link/entry", testContactPage),
		link("/body/4/content/0/content/0/content/0/content/0/marks/0/attrs/entry", testContactPage),
		link("/body/5/attrs/caption/0/marks/0/attrs/entry", testContactPage),
		link("/fields/box/note/0/marks/0/attrs/entry", testContactPage),
		link("/fields/extra/0/content/1/marks/0/attrs/entry", testContactPage),
		link("/fields/intro/1/marks/0/attrs/entry", testContactPage),
		term("/fields/keywords/1", testTag2),
		link("/fields/links/2/entry", testContactPage),
		link("/fields/rows/1/link/entry", testContactPage),
		term("/fields/tags/1", testTag2),
		term("/terms/category/1", testNewsCategory),
		term("/terms/tag/1", testTag2),
	}
	if lines := warningLines(got); !slices.Equal(lines, wantWarnings) {
		t.Errorf("Translate() warnings:\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(wantWarnings, "\n"))
	}
}

// TestRevisionTranslatePointers checks that the warnings are at the pointers that validation uses for the same values:
// the problems of a revision whose entries and terms do not exist, apart from those of the entry fields, which a
// translation leaves as they are.
func TestRevisionTranslatePointers(t *testing.T) {
	typ := translateSite().Type(TypePost)
	missing := func(kind RefKind, id string) (Ref, bool) { return Ref{}, false }
	r := mustRevision(t, translateJSON(testAboutPage, testContactPage, testPrivacyPage, testCategory, testNewsCategory, testTag, testTag2))
	var problems Errors
	err := r.Validate(RevisionOptions{Site: translateSite(), Entry: testEntry(TypePost), Lookup: missing})
	if !errors.As(err, &problems) {
		t.Fatalf("Validate() = %v, want problems", err)
	}
	var want []string
	for _, e := range problems {
		kind, _, _ := strings.Cut(e.Msg, " ")
		if (kind == "entry" || kind == "term") && strings.HasSuffix(e.Msg, "does not exist") && !strings.HasPrefix(e.Path, "/fields/related/") {
			want = append(want, e.Path)
		}
	}
	slices.Sort(want)
	// 11 links in the body, 17 in the fields, and 4 terms.
	if len(want) != 32 {
		t.Fatalf("validation found %d entries and terms that do not exist, want the 32 of the revision: %v", len(want), want)
	}

	// Nothing has a translation, so every link and every term is reported.
	var paths []string
	for _, e := range r.Translate(typ, "en", nil, nil) {
		paths = append(paths, e.Path)
	}
	if !slices.Equal(paths, want) {
		t.Errorf("Translate() reported the pointers\n%s\nvalidation has\n%s", strings.Join(paths, "\n"), strings.Join(want, "\n"))
	}
}

func TestRevisionTranslateParent(t *testing.T) {
	r := mustRevision(t, rev(`,"title":"t","slug":"s","parent":"`+testAboutPage+`"`))
	if got := r.Translate(nil, "en", findIn(entryTranslations), nil); len(got) != 0 || r.Parent != testEnglishPage {
		t.Errorf("Translate() = %v, parent %q; want no warnings and the parent %q", got, r.Parent, testEnglishPage)
	}

	r = mustRevision(t, rev(`,"title":"t","slug":"s","parent":"`+testContactPage+`"`))
	got := r.Translate(nil, "en", findIn(entryTranslations), nil)
	want := []string{`/parent: entry "` + testContactPage + `" has no translation in en; the parent is left out`}
	if lines := warningLines(got); !slices.Equal(lines, want) || r.Parent != "" {
		t.Errorf("Translate() = %q, parent %q; want %q and no parent", lines, r.Parent, want)
	}
	if data, _ := r.MarshalJSON(); string(data) != rev(`,"title":"t","slug":"s"`) {
		t.Errorf("the revision without its parent is %s", data)
	}
}

func TestRevisionTranslateTerms(t *testing.T) {
	r := mustRevision(t, rev(`,"terms":{"category":["`+testNewsCategory+`"],"tag":["`+testTag2+`","`+testTag+`"],"series":["`+testSeries+`"]}`))
	got := r.Translate(nil, "en", nil, findIn(termTranslations))
	want := []string{
		`/terms/category/0: term "` + testNewsCategory + `" has no translation in en; it is left out`,
		`/terms/series/0: term "` + testSeries + `" has no translation in en; it is left out`,
		`/terms/tag/0: term "` + testTag2 + `" has no translation in en; it is left out`,
	}
	if lines := warningLines(got); !slices.Equal(lines, want) {
		t.Errorf("Translate() = %q, want %q", lines, want)
	}
	// A taxonomy with no term left is removed, and one with a term keeps it.
	if data, _ := json.Marshal(r.Terms); string(data) != `{"tag":["`+testEnglishTag+`"]}` {
		t.Errorf("the terms are %s, want only the tag in en", data)
	}

	// Without any term left, the revision has none.
	r = mustRevision(t, rev(`,"terms":{"category":["`+testNewsCategory+`"]}`))
	_ = r.Translate(nil, "en", nil, nil)
	if r.Terms != nil {
		t.Errorf("Terms = %v, want nil", r.Terms)
	}

	// A taxonomy name is escaped in a JSON Pointer.
	r = Revision{Terms: Terms{"a/b~c": {testTag2}}}
	got = r.Translate(nil, "en", nil, nil)
	if len(got) != 1 || got[0].Path != "/terms/a~1b~0c/0" {
		t.Errorf("Translate() = %v, want one warning at /terms/a~1b~0c/0", got)
	}
}

func TestRevisionTranslateFields(t *testing.T) {
	typ := translateSite().Type(TypePost)
	tests := []struct {
		name   string
		fields string
		want   string // "" if no field is left
		warn   []string
	}{
		{
			name:   "a term field without a translation is removed",
			fields: `"main":"` + testNewsCategory + `"`,
			warn:   []string{`/fields/main: term "` + testNewsCategory + `" has no translation in en; it is left out`},
		},
		{
			name:   "a list of terms without any translation is removed",
			fields: `"keywords":["` + testTag2 + `"],"tags":["` + testTag2 + `","` + testTag2 + `"]`,
			warn: []string{
				`/fields/keywords/0: term "` + testTag2 + `" has no translation in en; it is left out`,
				`/fields/tags/0: term "` + testTag2 + `" has no translation in en; it is left out`,
				`/fields/tags/1: term "` + testTag2 + `" has no translation in en; it is left out`,
			},
		},
		{
			name:   "a group that has nothing left is removed",
			fields: `"box":{"tag":"` + testTag2 + `"}`,
			warn:   []string{`/fields/box/tag: term "` + testTag2 + `" has no translation in en; it is left out`},
		},
		{
			name:   "a group keeps the values that are left",
			fields: `"box":{"tag":"` + testTag2 + `","title":"t"}`,
			want:   `"box":{"title":"t"}`,
			warn:   []string{`/fields/box/tag: term "` + testTag2 + `" has no translation in en; it is left out`},
		},
		{
			name:   "entries and media stay",
			fields: `"cover":"` + testImage + `","related":["` + testAboutPage + `"]`,
			want:   `"cover":"` + testImage + `","related":["` + testAboutPage + `"]`,
		},
		{
			name:   "a link field to a translation",
			fields: `"more":{"entry":"` + testAboutPage + `","newTab":true,"rel":["nofollow"]}`,
			want:   `"more":{"entry":"` + testEnglishPage + `","newTab":true,"rel":["nofollow"]}`,
		},
		{
			name:   "links that are not to entries stay",
			fields: `"more":{"href":"/x/"},"links":[{"href":"/y/"},{"anchor":"top"}]`,
			want:   `"links":[{"href":"/y/"},{"anchor":"top"}],"more":{"href":"/x/"}`,
		},
		{
			name:   "an unknown field stays",
			fields: `"nope":{"a":[1,2]}`,
			want:   `"nope":{"a":[1,2]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := mustRevision(t, revFields(tt.fields))
			got := r.Translate(typ, "en", findIn(entryTranslations), findIn(termTranslations))
			want := rev("")
			if tt.want != "" {
				want = revFields(tt.want)
			}
			if data, _ := r.MarshalJSON(); string(data) != want {
				t.Errorf("Translate() wrote\n%s\nwant\n%s", data, want)
			}
			if tt.want == "" && r.Fields != nil {
				t.Errorf("Fields = %v, want nil", r.Fields)
			}
			if lines := warningLines(got); !slices.Equal(lines, tt.warn) {
				t.Errorf("Translate() warnings:\n%q\nwant\n%q", lines, tt.warn)
			}
		})
	}
}

func TestRevisionTranslateWithoutType(t *testing.T) {
	// The fields are left as they are, which a translation cannot read without the type; the rest is moved.
	r := mustRevision(t, translateJSON(testAboutPage, testContactPage, testPrivacyPage, testCategory, testNewsCategory, testTag, testTag2))
	fields := maps.Clone(r.Fields)
	got := r.Translate(nil, "en", findIn(entryTranslations), findIn(termTranslations))
	if len(r.Fields) != len(fields) {
		t.Fatalf("Fields = %v, want %v", r.Fields, fields)
	}
	for name, v := range fields {
		if string(r.Fields[name]) != string(v) {
			t.Errorf("field %s = %s, want %s", name, r.Fields[name], v)
		}
	}
	for _, e := range got {
		if strings.HasPrefix(e.Path, "/fields/") {
			t.Errorf("warning %v about a field", e)
		}
	}
	if len(got) == 0 || r.Body[0].Text[0].Marks[0].Link.Entry != testEnglishPage || r.Terms["category"][0] != testEnglishCategory {
		t.Errorf("the body and the terms are not moved: %v, %v", r.Body[0].Text[0].Marks[0].Link, r.Terms)
	}
}

func TestRevisionTranslateNothingToMove(t *testing.T) {
	// A revision with no entry to link, no term and no parent is written as it was, whatever the functions say.
	in := rev(`,"title":"t","slug":"s","fields":{"client":"c","images":["` + testImage + `"],"year":2024},` +
		`"body":[{"type":"paragraph","id":"b_00000001","content":[{"type":"text","text":"x","marks":[{"type":"link","attrs":{"href":"/a/"}}]}]}]`)
	r := mustRevision(t, in)
	want := canonical(t, in)
	if got := r.Translate(testSite().Type("works"), "en", findIn(entryTranslations), findIn(termTranslations)); len(got) != 0 {
		t.Errorf("Translate() = %v, want no warnings", got)
	}
	if data, _ := r.MarshalJSON(); string(data) != want {
		t.Errorf("Translate() changed the revision:\n%s\nwant\n%s", data, want)
	}

	// The functions may be nil, and an answer without an ID is no translation.
	empty := func(string) (string, bool) { return "", true }
	in = rev(`,"parent":"` + testAboutPage + `","terms":{"tag":["` + testTag + `"]},` +
		`"body":[{"type":"paragraph","id":"b_00000001","content":[` + entryLink(testAboutPage) + `]}]`)
	for name, find := range map[string]func(string) (string, bool){"nil": nil, "empty": empty} {
		r = mustRevision(t, in)
		if got := r.Translate(nil, "en", find, find); len(got) != 3 || r.Parent != "" || r.Terms != nil {
			t.Errorf("%s: Translate() = %v, parent %q, terms %v; want three warnings", name, got, r.Parent, r.Terms)
		}
		if r.Body[0].Text[0].Marks[0].Link.Entry != testAboutPage {
			t.Errorf("%s: a link that has no translation changed", name)
		}
	}
}
