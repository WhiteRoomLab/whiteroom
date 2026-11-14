package wpimport

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// refsDefs is the schema file of the tests of the references: the type works, which uses the taxonomies genre and tag, the
// type events, which uses none, and post, which uses genre as well as category and tag. keyword is a taxonomy that no type
// uses.
func refsDefs() *content.Site {
	return &content.Site{
		Types: []content.TypeDef{
			{Name: "post", Taxonomies: []string{"genre"}},
			{Name: "works", Label: "Works", Body: true, Taxonomies: []string{"genre", "tag"}},
			{Name: "events", Label: "Events", Body: true},
		},
		Taxonomies: []content.TaxonomyDef{{Name: "genre", Hierarchical: true}, {Name: "keyword"}},
	}
}

// refsSettings is entrySettings with the URL rule of the posts.
func refsSettings(post string) *Settings {
	s := entrySettings()
	s.URLs.Post = post
	return s
}

// refsChannel writes the authors and the terms for the channel of the WXR.
func refsChannel(authors []wxrAuthor, terms []wxrTerm) string {
	var b strings.Builder
	for _, a := range authors {
		b.WriteString(a.xml())
	}
	for _, x := range terms {
		b.WriteString(x.xml())
	}
	return b.String()
}

// makeRefs makes the entries of the items of a WXR that has the channel after its link. defs is nil for refsDefs.
func makeRefs(t *testing.T, s *Settings, defs *content.Site, channel string, items ...wxrEntry) (*Import, *content.Site, []*EntryDoc) {
	t.Helper()
	if defs == nil {
		defs = refsDefs()
	}
	return makeEntries(t, s, defs, entriesWXR(channel, items...))
}

// termRefs writes the category elements of an item for the terms with the nicenames, which have the domain.
func termRefs(domain string, nicenames ...string) []wxrTermRef {
	var out []wxrTermRef
	for _, n := range nicenames {
		out = append(out, wxrTermRef{domain: domain, nicename: n, name: n})
	}
	return out
}

func joinRefs(lists ...[]wxrTermRef) []wxrTermRef {
	var out []wxrTermRef
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// termIDOf returns the ID of the term of the taxonomy of WordPress with the slug, as written.
func termIDOf(t *testing.T, imp *Import, taxonomy, slug string) string {
	t.Helper()
	term := imp.WXR.TermBySlug(taxonomy, slug)
	if term == nil {
		t.Fatalf("the WXR has no term %q of %s", slug, taxonomy)
	}
	_, id, ok := imp.IDs.Lookup(TermObject(term))
	if !ok {
		t.Fatalf("the term %q of %s has no ID", slug, taxonomy)
	}
	return id
}

// termIDs returns the IDs of the terms of the taxonomy of WordPress with the slugs, which are nil if there are none.
func termIDs(t *testing.T, imp *Import, taxonomy string, slugs ...string) []string {
	t.Helper()
	var out []string
	for _, s := range slugs {
		out = append(out, termIDOf(t, imp, taxonomy, s))
	}
	return out
}

func postIDOf(t *testing.T, imp *Import, post int64) string {
	t.Helper()
	_, id, ok := imp.IDs.Lookup(Object{Kind: ObjectPost, ID: post})
	if !ok {
		t.Fatalf("the post %d has no ID", post)
	}
	return id
}

var refsGroupLine = regexp.MustCompile(`: skipped: item \([a-z_]+\): the (terms of the taxonomy|menu order|templates) `)

// refsGroupLines lists the lines of the report that are about the groups of items.
func refsGroupLines(t *testing.T, imp *Import) []string {
	t.Helper()
	var out []string
	for _, l := range reportLines(t, imp.Report) {
		if refsGroupLine.MatchString(l) {
			out = append(out, l)
		}
	}
	return out
}

// groupLine is the line of the report about a group of items of the type typ whose first is the post.
func groupLine(imp *Import, post int64, typ, message string) string {
	it := imp.WXR.Post(post)
	return fmt.Sprintf("site.xml:%d:%d: skipped: item (%s): %s", it.Line, it.Column, typ, message)
}

func unusedTaxonomyMessage(taxonomy, items, typ string) string {
	return fmt.Sprintf("the terms of the taxonomy %[1]s of %[2]s of the post type %[3]s are not imported; add %[1]s to the taxonomies of %[3]s in the schema file to import them",
		taxonomy, items, typ)
}

func TestEntriesTerms(t *testing.T) {
	terms := []wxrTerm{
		{id: 2, taxonomy: "category", slug: "news", name: "News"},
		{id: 3, taxonomy: "category", slug: "japan", parent: "news", name: "Japan"},
		{id: 4, taxonomy: "post_tag", slug: "go", name: "Go"},
		{id: 5, taxonomy: "post_tag", slug: "rust", name: "Rust"},
		{id: 6, taxonomy: "genre", slug: "drama", name: "Drama"},
		{id: 7, taxonomy: "keyword", slug: "kw", name: "Keyword"},
		{id: 8, taxonomy: "post_format", slug: "post-format-video", name: "Video"},
		{id: 9, taxonomy: "nav_menu", slug: "main", name: "Main"},
		{id: 10, taxonomy: "category", slug: "long", name: strings.Repeat("a", content.MaxTextBytes+1)},
		{id: 11, taxonomy: "product_cat", slug: "mugs", name: "Mugs"},
	}
	items := []wxrEntry{
		{id: 1, title: "One", name: "one", terms: joinRefs(
			termRefs("category", "news", "japan"), termRefs("post_tag", "go", "go"), termRefs("genre", "drama"),
			termRefs("post_format", "post-format-video"), termRefs("nav_menu", "main"), termRefs("product_cat", "mugs"), termRefs("nosuch", "x"),
		)},
		{id: 2, title: "Two", name: "two", terms: joinRefs(termRefs("category", "ghost", "long"), termRefs("post_tag", "rust", "nosuch"))},
		{id: 3, title: "Three", name: "three", terms: joinRefs(termRefs("keyword", "kw", "kw"), termRefs("genre", "drama"))},
		{id: 4, title: "Four", name: "four", terms: termRefs("keyword", "kw")},
		{id: 5, typ: "page", title: "Page", name: "page", terms: joinRefs(termRefs("category", "news"), termRefs("post_tag", "go"))},
		{id: 6, typ: "page", title: "Page two", name: "page-two", terms: termRefs("category", "news")},
		{id: 7, typ: "works", title: "Work", name: "work", terms: joinRefs(termRefs("genre", "drama"), termRefs("post_tag", "go"), termRefs("category", "news"))},
		{id: 8, typ: "events", title: "Event", name: "event", terms: termRefs("genre", "drama")},
		{id: 9, title: "Nine", name: "nine", terms: termRefs("category", "japan", "news")},
		{id: 10, title: "Ten", name: "ten", status: "draft", terms: joinRefs(termRefs("post_tag", "rust", "go"), termRefs("category", "news"))},
		{id: 11, title: "Eleven", name: "eleven"},
	}
	imp, _, docs := makeRefs(t, nil, nil, refsChannel(nil, terms), items...)
	if len(docs) != len(items) {
		t.Fatalf("%d entries, want %d", len(docs), len(items))
	}
	for post, want := range map[int64]content.Terms{
		1: {
			"category": termIDs(t, imp, "category", "news", "japan"), "tag": termIDs(t, imp, "post_tag", "go"),
			"genre": termIDs(t, imp, "genre", "drama"),
		},
		2:  {"tag": termIDs(t, imp, "post_tag", "rust")},
		3:  {"genre": termIDs(t, imp, "genre", "drama")},
		4:  nil,
		5:  nil,
		6:  nil,
		7:  {"genre": termIDs(t, imp, "genre", "drama"), "tag": termIDs(t, imp, "post_tag", "go")},
		8:  nil,
		9:  {"category": termIDs(t, imp, "category", "japan", "news")},
		10: {"tag": termIDs(t, imp, "post_tag", "rust", "go"), "category": termIDs(t, imp, "category", "news")},
		11: nil,
	} {
		if got := docOf(docs, post).Revision.Terms; !reflect.DeepEqual(got, want) {
			t.Errorf("the terms of item %d are %#v, want %#v", post, got, want)
		}
	}
	for _, d := range docs {
		if d.Revision.Terms != nil && len(d.Revision.Terms) == 0 {
			t.Errorf("the terms of item %d are an empty map", d.Post)
		}
		if d.Entry.Status != content.StatusPublished && d.Post != 10 {
			t.Errorf("the status of item %d is %q", d.Post, d.Entry.Status)
		}
	}

	wantItem(t, imp, docs, 1)
	wantItem(t, imp, docs, 2,
		`skipped: the term "ghost" of category is not in the WXR; it is left out -> {R}#/terms/category`,
		`skipped: the term "long" of category is not imported; it is left out -> {R}#/terms/category`,
		`skipped: the term "nosuch" of post_tag is not in the WXR; it is left out -> {R}#/terms/tag`,
	)
	for _, post := range []int64{3, 4, 5, 6, 7, 8, 9, 10, 11} {
		wantItem(t, imp, docs, post)
	}
	sameSet(t, "the groups", refsGroupLines(t, imp), []string{
		groupLine(imp, 3, "post", unusedTaxonomyMessage("keyword", "2 items", "post")),
		groupLine(imp, 5, "page", unusedTaxonomyMessage("category", "2 items", "page")),
		groupLine(imp, 5, "page", unusedTaxonomyMessage("tag", "1 item", "page")),
		groupLine(imp, 7, "works", unusedTaxonomyMessage("category", "1 item", "works")),
		groupLine(imp, 8, "events", unusedTaxonomyMessage("genre", "1 item", "events")),
	})
}

func TestEntriesTermsWithoutIDs(t *testing.T) {
	// The terms of WXR 1.0 have no IDs, so they are found by their slugs.
	terms := []wxrTerm{
		{taxonomy: "category", slug: "b", name: "B"},
		{taxonomy: "category", slug: "a", name: "A"},
		{taxonomy: "post_tag", slug: "t", name: "T"},
	}
	imp, _, docs := makeRefs(t, nil, nil, refsChannel(nil, terms),
		wxrEntry{id: 1, title: "One", name: "one", terms: joinRefs(termRefs("category", "b", "a"), termRefs("post_tag", "t"))},
		wxrEntry{id: 2, title: "Two", name: "two", terms: termRefs("category", "c")},
	)
	want := content.Terms{"category": termIDs(t, imp, "category", "b", "a"), "tag": termIDs(t, imp, "post_tag", "t")}
	if got := docOf(docs, 1).Revision.Terms; !reflect.DeepEqual(got, want) {
		t.Errorf("the terms of item 1 are %#v, want %#v", got, want)
	}
	wantItem(t, imp, docs, 1)
	wantItem(t, imp, docs, 2, `skipped: the term "c" of category is not in the WXR; it is left out -> {R}#/terms/category`)
}

func TestEntriesTermsOfTheOldSlugs(t *testing.T) {
	// The slugs are as WordPress wrote them in the items and in the terms, and the terms have the slugs of the import.
	terms := []wxrTerm{
		{id: 2, taxonomy: "category", slug: "%e3%83%8b%e3%83%a5%e3%83%bc%e3%82%b9", name: "ニュース"},
		{id: 3, taxonomy: "category", slug: "Upper Case", name: "Upper"},
	}
	imp, _, docs := makeRefs(t, nil, nil, refsChannel(nil, terms),
		wxrEntry{id: 1, title: "One", name: "one", terms: termRefs("category", "%e3%83%8b%e3%83%a5%e3%83%bc%e3%82%b9", "Upper Case")},
		wxrEntry{id: 2, title: "Two", name: "two", terms: termRefs("category", "ニュース", "%E3%83%8B%E3%83%A5%E3%83%BC%E3%82%B9")},
	)
	want := content.Terms{"category": termIDs(t, imp, "category", "%e3%83%8b%e3%83%a5%e3%83%bc%e3%82%b9", "Upper Case")}
	if got := docOf(docs, 1).Revision.Terms; !reflect.DeepEqual(got, want) {
		t.Errorf("the terms of item 1 are %#v, want %#v", got, want)
	}
	if got := docOf(docs, 2).Revision.Terms; got != nil {
		t.Errorf("the terms of item 2 are %#v, want none", got)
	}
	wantItem(t, imp, docs, 1)
	wantItem(t, imp, docs, 2,
		`skipped: the term "ニュース" of category is not in the WXR; it is left out -> {R}#/terms/category`,
		`skipped: the term "%E3%83%8B%E3%83%A5%E3%83%BC%E3%82%B9" of category is not in the WXR; it is left out -> {R}#/terms/category`,
	)
}

func TestEntriesCategoryOrder(t *testing.T) {
	const rule = "/{category}/{slug}/"
	terms := []wxrTerm{
		{id: 1, taxonomy: "category", slug: "uncategorized", name: "Uncategorized"},
		{id: 2, taxonomy: "category", slug: "news", name: "News"},
		{id: 3, taxonomy: "category", slug: "japan", parent: "news", name: "Japan"},
		{id: 4, taxonomy: "category", slug: "tech", name: "Tech"},
		{id: 9, taxonomy: "category", slug: "%e3%83%8b%e3%83%a5%e3%83%bc%e3%82%b9", name: "ニュース"},
		// A slug with the ideographic space, which a path writes as it is not and the link writes encoded.
		{id: 11, taxonomy: "category", slug: "a%e3%80%80b", name: "A B"},
		{id: 5, taxonomy: "post_tag", slug: "go", name: "Go"},
	}
	cats := func(slugs ...string) []wxrTermRef { return termRefs("category", slugs...) }
	item := func(id int, status, link string, refs ...wxrTermRef) wxrEntry {
		return wxrEntry{id: id, status: status, title: fmt.Sprintf("Post %d", id), name: "hello", link: link, terms: refs}
	}
	const home = "https://example.com"
	s := refsSettings(rule)
	s.DefaultCategory = "uncategorized"
	items := []wxrEntry{
		item(1, "", home+"/news/hello/", cats("tech", "news")...),
		item(2, "", home+"/tech/hello/", cats("news", "tech")...),
		item(3, "", home+"/news/japan/hello/", cats("news", "japan")...),
		item(4, "", home+"/other/hello/", cats("tech", "news")...),
		item(5, "draft", home+"/?p=5", cats("tech", "news")...),
		item(6, "", home+"/uncategorized/hello/"),
		item(7, "", home+"/%E3%83%8B%E3%83%A5%E3%83%BC%E3%82%B9/hello/", cats("tech", "%e3%83%8b%e3%83%a5%e3%83%bc%e3%82%b9")...),
		item(8, "", "https://other.example.org/news/hello/", cats("tech", "news")...),
		item(9, "", home+"/tech/hello/", cats("tech")...),
		item(10, "", home+"/tech/hello/", joinRefs(cats("japan", "tech", "news"), termRefs("post_tag", "go"))...),
		item(11, "", "", cats("tech", "japan", "news")...),
		item(12, "", home+"/news/hello/?replytocom=1", cats("tech", "news")...),
		item(13, "", home+"/tech/hello", cats("news", "tech")...),
		item(14, "", home+"/uncategorized/hello/"),
		item(15, "", home+"/a%E3%80%80b/hello/", cats("tech", "a%e3%80%80b")...),
	}
	imp, site, docs := makeRefs(t, s, nil, refsChannel(nil, terms), items...)
	for post, want := range map[int64][]string{
		1:  {"news", "tech"},
		2:  {"tech", "news"},
		3:  {"japan", "news"},
		4:  {"news", "tech"},
		5:  {"news", "tech"},
		6:  {"uncategorized"},
		7:  {"%e3%83%8b%e3%83%a5%e3%83%bc%e3%82%b9", "tech"},
		8:  {"news", "tech"},
		9:  {"tech"},
		10: {"tech", "japan", "news"},
		11: {"news", "tech", "japan"},
		12: {"news", "tech"},
		13: {"news", "tech"},
		14: {"uncategorized"},
		15: {"a%e3%80%80b", "tech"},
	} {
		d := docOf(docs, post)
		got := d.Revision.Terms["category"]
		if w := termIDs(t, imp, "category", want...); !slices.Equal(got, w) {
			t.Errorf("the categories of item %d are %v, want %v (%v)", post, got, w, want)
		}
		if len(d.Revision.Terms) > 1 && post != 10 {
			t.Errorf("the terms of item %d are %#v", post, d.Revision.Terms)
		}
		if d.Entry.Status != content.StatusPublished && post != 5 {
			t.Errorf("the status of item %d is %q", post, d.Entry.Status)
		}
	}
	for post, want := range map[int64]string{
		1: "/news/hello/", 2: "/tech/hello/", 3: "/news/japan/hello/", 4: "/news/hello/", 6: "/uncategorized/hello/",
		7: "/ニュース/hello/", 9: "/tech/hello/", 10: "/tech/hello/",
	} {
		d := docOf(docs, post)
		if got, err := site.EntryPath(&d.Entry, &d.Revision, imp.PathRef); err != nil || got != want {
			t.Errorf("the path of item %d is %q, %v, want %q", post, got, err, want)
		}
	}
	// What WordPress used is the path of the link, for the items that its link tells.
	for _, post := range []int64{1, 2, 3, 7, 15} {
		d := docOf(docs, post)
		got, err := site.EntryRulePath(&d.Entry, &d.Revision, imp.PathRef)
		link, ok := linkPath(parseHome(home), imp.WXR.Post(post).Link)
		if err != nil || !ok || rawPath(got) != link {
			t.Errorf("the path of item %d is %q, %v, want that of its link %q, %v", post, got, err, link, ok)
		}
	}
	for post := int64(1); post <= 15; post++ {
		if post == 6 || post == 14 {
			continue
		}
		wantItem(t, imp, docs, post)
	}
	for _, post := range []int64{6, 14} {
		wantItem(t, imp, docs, post,
			`changed: the entry has no category; it has the default category "uncategorized" here, which {category} needs -> {R}#/terms/category`)
	}
}

func TestEntriesCategoryOrderNeedsAPath(t *testing.T) {
	// A path that cannot be made leaves the order of the ID, and the order of the items is that of the others.
	terms := []wxrTerm{
		{id: 4, taxonomy: "category", slug: "tech", name: "Tech"},
		{id: 2, taxonomy: "category", slug: "news", name: "News"},
		{id: 7, taxonomy: "category", slug: "z", name: "Z"},
	}
	cats := termRefs("category", "tech", "z", "news")
	imp, _, docs := makeRefs(t, refsSettings("/{yyyy}/{category}/{slug}/"), nil, refsChannel(nil, terms),
		wxrEntry{id: 1, title: "Dated", name: "hello", link: "https://example.com/2026/tech/hello/", terms: cats},
		// A draft whose date was not decided has no date, which the path needs.
		wxrEntry{id: 2, status: "draft", dateGMT: zeroDate, title: "Undated", name: "hello", link: "https://example.com/2026/tech/hello/", terms: cats},
		// A post whose slug the path cannot have.
		wxrEntry{id: 3, status: "draft", title: "No slug", name: "", link: "https://example.com/2026/tech/hello/", terms: cats},
	)
	for post, want := range map[int64][]string{1: {"tech", "z", "news"}, 2: {"news", "tech", "z"}, 3: {"news", "tech", "z"}} {
		if got, w := docOf(docs, post).Revision.Terms["category"], termIDs(t, imp, "category", want...); !slices.Equal(got, w) {
			t.Errorf("the categories of item %d are %v, want %v (%v)", post, got, w, want)
		}
	}
}

func TestEntriesCategoryOrderSkipsAPathThatFails(t *testing.T) {
	// The path of a category whose parent is gone cannot be made, so it is not the one that the link tells.
	terms := []wxrTerm{
		{id: 2, taxonomy: "category", slug: "child", parent: "parent", name: "Child"},
		{id: 4, taxonomy: "category", slug: "other", name: "Other"},
		{id: 5, taxonomy: "category", slug: "parent", name: "Parent"},
	}
	in := entriesWXR(refsChannel(nil, terms),
		wxrEntry{id: 1, title: "One", name: "hello", link: "https://example.com/other/hello/", terms: termRefs("category", "child", "other")},
	)
	imp, site := planEntries(t, refsSettings("/{category}/{slug}/"), refsDefs(), in)
	imp.DropRef(termIDOf(t, imp, "category", "parent"))
	docs, err := collectEntries(context.Background(), imp, nil, site)
	if err != nil {
		t.Fatal(err)
	}
	checkEntries(t, imp, site, docs)
	if got, want := docs[0].Revision.Terms["category"], termIDs(t, imp, "category", "other", "child"); !slices.Equal(got, want) {
		t.Errorf("the categories are %v, want %v", got, want)
	}
}

func TestEntriesCategoryOrderWithoutIDs(t *testing.T) {
	// The terms of WXR 1.0 have no ID: they come after those that have one, in the order of the item.
	terms := []wxrTerm{
		{taxonomy: "category", slug: "b", name: "B"},
		{taxonomy: "category", slug: "a", name: "A"},
		{id: 5, taxonomy: "category", slug: "c", name: "C"},
	}
	imp, _, docs := makeRefs(t, refsSettings("/{category}/{slug}/"), nil, refsChannel(nil, terms),
		wxrEntry{id: 1, title: "One", name: "x", link: "https://example.com/other/x/", terms: termRefs("category", "b", "a", "c")},
		wxrEntry{id: 2, title: "Two", name: "x", link: "https://example.com/other/x/", terms: termRefs("category", "b", "a")},
		wxrEntry{id: 3, title: "Three", name: "x", link: "https://example.com/a/x/", terms: termRefs("category", "b", "c", "a")},
		wxrEntry{id: 4, title: "Four", name: "x", link: "https://example.com/other/x/", terms: termRefs("category", "c", "b")},
		wxrEntry{id: 5, title: "Five", name: "x", link: "https://example.com/b/x/", terms: termRefs("category", "a", "b", "c")},
	)
	for post, want := range map[int64][]string{1: {"c", "b", "a"}, 2: {"b", "a"}, 3: {"a", "b", "c"}, 4: {"c", "b"}, 5: {"b", "a", "c"}} {
		if got, w := docOf(docs, post).Revision.Terms["category"], termIDs(t, imp, "category", want...); !slices.Equal(got, w) {
			t.Errorf("the categories of item %d are %v, want %v (%v)", post, got, w, want)
		}
	}
}

func TestEntriesDefaultCategory(t *testing.T) {
	const rule = "/{category}/{slug}/"
	missing := "review: the entry cannot be published: /terms/category: missing category; the URL rule \"/{category}/{slug}/\" of post needs one; it is a draft here -> {E}#/status"
	terms := func(extra ...wxrTerm) []wxrTerm {
		return append([]wxrTerm{
			{id: 1, taxonomy: "category", slug: "uncategorized", name: "Uncategorized"},
			{id: 2, taxonomy: "category", slug: "news", name: "News"},
			{id: 3, taxonomy: "category", slug: "long", name: strings.Repeat("a", content.MaxTextBytes+1)},
		}, extra...)
	}
	post := wxrEntry{id: 1, title: "One", name: "one"}
	for _, tt := range []struct {
		name, def string
		terms     []wxrTerm
		item      wxrEntry
		want      []string // the slugs of the categories
		draft     bool
		lines     []string
	}{
		{
			name: "a default category", def: "uncategorized", terms: terms(), item: post, want: []string{"uncategorized"},
			lines: []string{`changed: the entry has no category; it has the default category "uncategorized" here, which {category} needs -> {R}#/terms/category`},
		},
		{
			name: "a draft too", def: "uncategorized", terms: terms(), item: wxrEntry{id: 1, status: "draft", name: "one"}, want: []string{"uncategorized"}, draft: true,
			lines: []string{`changed: the entry has no category; it has the default category "uncategorized" here, which {category} needs -> {R}#/terms/category`},
		},
		{name: "an item that has a category", def: "uncategorized", terms: terms(), item: wxrEntry{id: 1, title: "One", name: "one", link: "https://example.com/news/one/", terms: termRefs("category", "news")}, want: []string{"news"}},
		{
			name: "a japanese slug", def: "お知らせ", item: post, want: []string{"%e3%81%8a%e7%9f%a5%e3%82%89%e3%81%9b"},
			terms: terms(wxrTerm{id: 5, taxonomy: "category", slug: "%e3%81%8a%e7%9f%a5%e3%82%89%e3%81%9b", name: "お知らせ"}),
			lines: []string{`changed: the entry has no category; it has the default category "お知らせ" here, which {category} needs -> {R}#/terms/category`},
		},
		{
			name: "a slug that cannot be decoded is as written", def: "a%ffb", item: post, want: []string{"a%ffb"},
			terms: terms(wxrTerm{id: 5, taxonomy: "category", slug: "a%ffb", name: "Bad"}),
			lines: []string{`changed: the entry has no category; it has the default category "a%ffb" here, which {category} needs -> {R}#/terms/category`},
		},
		{name: "the slug as it is written is not the default", def: "%e3%81%8a", item: post, draft: true, lines: []string{missing},
			terms: terms(wxrTerm{id: 5, taxonomy: "category", slug: "%e3%81%8a", name: "お"})},
		{name: "no default category", def: "", terms: terms(), item: post, draft: true, lines: []string{missing}},
		{name: "a default category that the WXR does not have", def: "nosuch", terms: terms(), item: post, draft: true, lines: []string{missing}},
		{name: "a default category that is not imported", def: "long", terms: terms(), item: post, draft: true, lines: []string{missing}},
		{
			name: "the first category of the slug", def: "dup", item: post, want: []string{"dup"}, terms: terms(
				wxrTerm{id: 6, taxonomy: "category", slug: "dup", name: "First"}, wxrTerm{id: 7, taxonomy: "category", slug: "%64up", name: "Second"}),
			lines: []string{`changed: the entry has no category; it has the default category "dup" here, which {category} needs -> {R}#/terms/category`},
		},
		{name: "a tag is not a category", def: "go", item: post, draft: true, lines: []string{missing},
			terms: terms(wxrTerm{id: 6, taxonomy: "post_tag", slug: "go", name: "Go"})},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := refsSettings(rule)
			s.DefaultCategory = tt.def
			imp, _, docs := makeRefs(t, s, nil, refsChannel(nil, tt.terms), tt.item)
			d := docs[0]
			var want []string
			if tt.want != nil {
				// The first term of the slug is the one that the plan took, whatever its taxonomy.
				for _, slug := range tt.want {
					want = append(want, termIDOf(t, imp, "category", slug))
				}
			}
			if got := d.Revision.Terms["category"]; !slices.Equal(got, want) {
				t.Errorf("the categories are %v, want %v", got, want)
			}
			if got := d.Entry.Status == content.StatusDraft; got != (tt.draft || tt.item.status == "draft") {
				t.Errorf("the status is %q", d.Entry.Status)
			}
			wantItem(t, imp, docs, 1, tt.lines...)
		})
	}

	t.Run("no settings", func(t *testing.T) {
		in := entriesWXR(refsChannel(nil, terms()), post)
		s := refsSettings(rule)
		s.DefaultCategory = "uncategorized"
		imp, site := planEntries(t, s, refsDefs(), in)
		docs, err := collectEntries(context.Background(), imp, nil, site)
		if err != nil {
			t.Fatal(err)
		}
		checkEntries(t, imp, site, docs)
		wantItem(t, imp, docs, 1, missing)
		if docs[0].Entry.Status != content.StatusDraft || docs[0].Revision.Terms != nil {
			t.Errorf("the entry is %+v with the terms %#v, want a draft without terms", docs[0].Entry, docs[0].Revision.Terms)
		}
	})

	t.Run("a rule without a category", func(t *testing.T) {
		s := entrySettings()
		s.DefaultCategory = "uncategorized"
		items := []wxrEntry{
			{id: 1, title: "One", name: "one"},
			{id: 2, title: "Two", name: "two", link: "https://example.com/2026/10/05/two/", terms: termRefs("category", "news", "uncategorized")},
			{id: 3, typ: "works", title: "Work", name: "work"},
		}
		imp, _, docs := makeRefs(t, s, nil, refsChannel(nil, terms()), items...)
		if got := docOf(docs, 1).Revision.Terms; got != nil {
			t.Errorf("the terms of an item without a category are %#v, want none", got)
		}
		if got, want := docOf(docs, 2).Revision.Terms["category"], termIDs(t, imp, "category", "news", "uncategorized"); !slices.Equal(got, want) {
			t.Errorf("the categories are %v, want those of the item, %v", got, want)
		}
		for post := int64(1); post <= 3; post++ {
			wantItem(t, imp, docs, post)
		}
	})
}

func TestEntriesCategoryOfACustomType(t *testing.T) {
	defs := refsDefs()
	defs.Types[1].Taxonomies = []string{"category", "genre"}
	s := refsSettings("")
	s.URLs.Types["works"] = TypeURLs{URL: "/works/{category}/{slug}/", Archive: "/works/"}
	s.DefaultCategory = "uncategorized"
	terms := []wxrTerm{
		{id: 1, taxonomy: "category", slug: "uncategorized", name: "Uncategorized"},
		{id: 2, taxonomy: "category", slug: "news", name: "News"},
		{id: 4, taxonomy: "category", slug: "tech", name: "Tech"},
	}
	imp, site, docs := makeRefs(t, s, defs, refsChannel(nil, terms),
		wxrEntry{id: 1, typ: "works", title: "One", name: "one", link: "https://example.com/works/tech/one/", terms: termRefs("category", "news", "tech")},
		wxrEntry{id: 2, typ: "works", title: "Two", name: "two"},
		// A post does not have the rule of the works.
		wxrEntry{id: 3, title: "Three", name: "three", link: "https://example.com/2026/10/05/three/", terms: termRefs("category", "tech", "news")},
	)
	for post, want := range map[int64][]string{1: {"tech", "news"}, 2: {"uncategorized"}, 3: {"tech", "news"}} {
		if got, w := docOf(docs, post).Revision.Terms["category"], termIDs(t, imp, "category", want...); !slices.Equal(got, w) {
			t.Errorf("the categories of item %d are %v, want %v (%v)", post, got, w, want)
		}
	}
	d := docOf(docs, 1)
	if got, err := site.EntryRulePath(&d.Entry, &d.Revision, imp.PathRef); err != nil || got != "/works/tech/one/" {
		t.Errorf("the path of item 1 is %q, %v", got, err)
	}
	wantItem(t, imp, docs, 1)
	wantItem(t, imp, docs, 2, `changed: the entry has no category; it has the default category "uncategorized" here, which {category} needs -> {R}#/terms/category`)
	wantItem(t, imp, docs, 3)
}

func TestEntriesAuthor(t *testing.T) {
	authors := []wxrAuthor{{id: 2, login: "alice", display: "Alice", email: "alice@example.org"}}
	items := []wxrEntry{
		{id: 1, title: "One", name: "one", creator: "alice"},
		{id: 2, title: "Two", name: "two", creator: "bob"},
		{id: 3, title: "Three", name: "three"},
		{id: 4, typ: "page", title: "Page", name: "page", creator: "alice"},
		{id: 5, typ: "works", title: "Work", name: "work", creator: "alice"},
		{id: 6, title: "Six", name: "six", creator: "carol"},
		{id: 7, title: "Seven", name: "seven", creator: "dave"},
		{id: 8, title: "Eight", name: "eight", status: "draft", creator: "alice"},
		{id: 9, title: "Nine", name: "nine", status: "private", creator: "bob"},
	}
	imp, site := planEntries(t, nil, nil, entriesWXR(refsChannel(authors, nil), items...))
	forget := func(login string, table bool) {
		kind, id, ok := imp.IDs.Lookup(UserObject(login))
		if !ok || kind != content.RefAuthor {
			t.Fatalf("the author %q has no ID", login)
		}
		if table {
			imp.IDs.Drop(UserObject(login))
		}
		imp.DropRef(id)
	}
	forget("carol", true)
	forget("dave", false)
	docs, err := collectEntries(context.Background(), imp, nil, site)
	if err != nil {
		t.Fatal(err)
	}
	checkEntries(t, imp, site, docs)

	author := func(login string) string {
		_, id, ok := imp.IDs.Lookup(UserObject(login))
		if !ok {
			t.Fatalf("no ID for the author %q", login)
		}
		return id
	}
	for post, want := range map[int64]string{1: author("alice"), 2: author("bob"), 3: "", 4: "", 5: "", 6: "", 7: "", 8: author("alice"), 9: author("bob")} {
		if got := docOf(docs, post).Revision.Author; got != want {
			t.Errorf("the author of item %d is %q, want %q", post, got, want)
		}
	}
	const gone = "changed: the author of the post is not imported; the post has no author -> {R}#/author"
	for post, lines := range map[int64][]string{1: nil, 2: nil, 3: nil, 4: nil, 5: nil, 6: {gone}, 7: {gone}, 8: nil, 9: {"review: the entry was private; it is a draft here -> {E}#/status"}} {
		wantItem(t, imp, docs, post, lines...)
	}
	for _, l := range itemLines(imp.Report, 6) {
		if strings.Contains(l, "carol") {
			t.Errorf("the login is in the line %q", l)
		}
	}
}

func TestEntriesFeaturedImage(t *testing.T) {
	// 20 in Arabic-Indic digits, which are not the digits of an ID.
	arabicTwenty := string([]rune{0x662, 0x660})
	attachment := func(id int, status, file string) wxrEntry {
		return wxrEntry{id: id, typ: "attachment", status: status, title: file, name: fmt.Sprintf("att-%d", id), meta: [][2]string{{"_wp_attached_file", file}}}
	}
	thumb := func(id int, typ string, values ...string) wxrEntry {
		x := wxrEntry{id: id, typ: typ, title: fmt.Sprintf("Item %d", id), name: fmt.Sprintf("item-%d", id)}
		for _, v := range values {
			x.meta = append(x.meta, [2]string{"_thumbnail_id", v})
		}
		return x
	}
	items := []wxrEntry{
		attachment(20, "inherit", "2026/10/a.png"),
		attachment(21, "inherit", "2026/10/b.pdf"),
		attachment(22, "trash", "2026/10/c.png"),
		attachment(23, "inherit", "2026/10/d.png"),
		thumb(101, "", "20"),
		thumb(102, "", " 20 \n"),
		thumb(103, "", "21"),
		thumb(104, "", "22"),
		thumb(105, "", "99"),
		thumb(106, "", "23"),
		thumb(107, "", "0"),
		thumb(108, "", "-1"),
		thumb(109, "", "abc"),
		thumb(110, "", "20abc"),
		thumb(111, "", "+20"),
		thumb(112, "", "2.5"),
		thumb(113, "", "99999999999999999999"),
		thumb(114, "", ""),
		thumb(115, "", "  \n"),
		thumb(116, "", "101"),
		thumb(117, "page", "20"),
		thumb(118, "works", "20"),
		thumb(119, "", "20", "21"),
		thumb(120, "", "", "20"),
		thumb(121, "", "021"),
		thumb(122, ""),
		thumb(123, "", arabicTwenty),
	}
	imp, site := planEntries(t, nil, nil, entriesWXR("", items...))
	_, id23, _ := imp.IDs.Lookup(Object{Kind: ObjectPost, ID: 23})
	imp.DropRef(id23)
	docs, err := collectEntries(context.Background(), imp, nil, site)
	if err != nil {
		t.Fatal(err)
	}
	checkEntries(t, imp, site, docs)

	image := postIDOf(t, imp, 20)
	notID := func(v string) string {
		return fmt.Sprintf(`skipped: the featured image %q is not the ID of an attachment -> {R}#/featuredImage`, v)
	}
	notImported := func(n int) string {
		return fmt.Sprintf("skipped: the featured image, attachment %d, is not imported -> {R}#/featuredImage", n)
	}
	for _, tt := range []struct {
		post  int64
		want  string
		lines []string
	}{
		{101, image, nil},
		{102, image, nil},
		{103, "", []string{"skipped: the featured image, attachment 21, is not an image -> {R}#/featuredImage"}},
		{104, "", []string{notImported(22)}},
		{105, "", []string{notImported(99)}},
		{106, "", []string{notImported(23)}},
		{107, "", []string{notID("0")}},
		{108, "", []string{notID("-1")}},
		{109, "", []string{notID("abc")}},
		{110, "", []string{notID("20abc")}},
		{111, "", []string{notID("+20")}},
		{112, "", []string{notID("2.5")}},
		{113, "", []string{notID("99999999999999999999")}},
		{114, "", nil},
		{115, "", nil},
		{116, "", []string{notImported(101)}},
		{117, image, nil},
		{118, image, nil},
		{119, image, nil},
		{120, "", nil},
		{121, "", []string{"skipped: the featured image, attachment 21, is not an image -> {R}#/featuredImage"}},
		{122, "", nil},
		{123, "", []string{notID(arabicTwenty)}},
	} {
		d := docOf(docs, tt.post)
		got := d.Revision.FeaturedImage
		if got != tt.want {
			t.Errorf("the featured image of item %d is %q, want %q", tt.post, got, tt.want)
		}
		wantItem(t, imp, docs, tt.post, tt.lines...)
	}

	// The record is about the postmeta.
	wantMetaSource(t, imp, 107, "_thumbnail_id", "the featured image")
}

// wantMetaSource checks that the entry of the report about the item post whose message starts with prefix is about the
// first postmeta with the key, at its place.
func wantMetaSource(t *testing.T, imp *Import, post int64, key, prefix string) {
	t.Helper()
	meta, ok := firstMeta(imp.WXR.Post(post).Meta, key)
	if !ok || meta.Line == 0 || meta.Column == 0 {
		t.Fatalf("the item %d has no postmeta %q with a place: %+v", post, key, meta)
	}
	found := false
	for _, e := range imp.Report.Entries() {
		if e.Source.ID != post || !strings.HasPrefix(e.Message, prefix) {
			continue
		}
		found = true
		if e.Source.Meta != key || e.Source.Line != meta.Line || e.Source.Column != meta.Column || e.Source.Record != recordItem {
			t.Errorf("the source of the entry %q is %+v, want the postmeta %q of the item at %d:%d", e.Message, e.Source, key, meta.Line, meta.Column)
		}
	}
	if !found {
		t.Errorf("no entry about the item %d starts with %q", post, prefix)
	}
}

func TestEntriesSticky(t *testing.T) {
	items := []wxrEntry{
		{id: 1, title: "One", name: "one", sticky: true},
		{id: 2, title: "Two", name: "two"},
		{id: 3, typ: "page", title: "Page", name: "page", sticky: true},
		{id: 4, typ: "page", title: "Page two", name: "page-two"},
		{id: 5, typ: "works", title: "Work", name: "work", sticky: true},
		{id: 6, title: "Six", name: "six", sticky: true, status: "draft"},
		{id: 7, typ: "events", title: "Event", name: "event", sticky: true},
	}
	imp, _, docs := makeRefs(t, nil, nil, "", items...)
	for post, want := range map[int64]bool{1: true, 2: false, 3: false, 4: false, 5: false, 6: true, 7: false} {
		if got := docOf(docs, post).Revision.Sticky; got != want {
			t.Errorf("the sticky of item %d is %v, want %v", post, got, want)
		}
	}
	const only = "skipped: the entry was sticky; only posts can be sticky -> {R}#/sticky"
	for post, lines := range map[int64][]string{1: nil, 2: nil, 3: {only}, 4: nil, 5: {only}, 6: nil, 7: {only}} {
		wantItem(t, imp, docs, post, lines...)
	}
}

func TestEntriesMenuOrder(t *testing.T) {
	items := []wxrEntry{
		{id: 1, typ: "page", title: "One", name: "one", menuOrder: 5},
		{id: 2, typ: "page", title: "Two", name: "two", menuOrder: -3},
		{id: 3, typ: "page", title: "Three", name: "three"},
		{id: 4, title: "Post", name: "post", menuOrder: 3},
		{id: 5, title: "Post 2", name: "post-2", menuOrder: -1},
		{id: 6, title: "Post 3", name: "post-3"},
		{id: 7, typ: "works", title: "Work", name: "work", menuOrder: 2},
		{id: 8, typ: "works", title: "Work 2", name: "work-2"},
		{id: 9, typ: "page", title: "Draft", name: "draft", menuOrder: 9, status: "draft"},
		{id: 10, typ: "events", title: "Event", name: "event", menuOrder: 1},
		{id: 11, title: "Post 4", name: "post-4", menuOrder: 7},
	}
	imp, _, docs := makeRefs(t, nil, nil, "", items...)
	for post, want := range map[int64]int{1: 5, 2: -3, 3: 0, 4: 0, 5: 0, 6: 0, 7: 0, 8: 0, 9: 9, 10: 0, 11: 0} {
		if got := docOf(docs, post).Revision.Order; got != want {
			t.Errorf("the order of item %d is %d, want %d", post, got, want)
		}
	}
	for post := int64(1); post <= 11; post++ {
		wantItem(t, imp, docs, post)
	}
	const only = "; only pages have an order"
	sameSet(t, "the groups", refsGroupLines(t, imp), []string{
		groupLine(imp, 4, "post", "the menu order of 3 items of the post type post is not imported"+only),
		groupLine(imp, 7, "works", "the menu order of 1 item of the post type works is not imported"+only),
		groupLine(imp, 10, "events", "the menu order of 1 item of the post type events is not imported"+only),
	})
}

func TestEntriesTemplate(t *testing.T) {
	const unmapped = "skipped: the page template %q is not imported; pageTemplates of the settings maps it to a page template of the theme of the site -> {R}#/template"
	const only = "; only pages have templates"
	meta := func(v string) [][2]string { return [][2]string{{"_wp_page_template", v}} }
	page := func(id int, template string) wxrEntry {
		return wxrEntry{id: id, typ: "page", title: fmt.Sprintf("Page %d", id), name: fmt.Sprintf("page-%d", id), meta: meta(template)}
	}
	items := []wxrEntry{
		page(1, "templates/full-width.php"),
		page(2, "templates/landing.php"),
		page(3, "templates/none.php"),
		page(4, "Landing Page.php"),
		page(5, "default"),
		page(6, ""),
		page(7, " templates/landing.php \n"),
		page(8, "page-lp.php"),
		page(9, "templates/bad.php"),
		{id: 10, typ: "page", title: "Page 10", name: "page-10", meta: [][2]string{{"_wp_page_template", "templates/landing.php"}, {"_wp_page_template", "x.php"}}},
		{id: 11, typ: "page", title: "Page 11", name: "page-11"},
		{id: 12, typ: "page", title: "Page 12", name: "page-12", status: "draft", meta: meta("templates/full-width.php")},
		{id: 13, title: "Post", name: "post", meta: meta("templates/full-width.php")},
		{id: 14, title: "Post 2", name: "post-2", meta: meta("page-lp.php")},
		{id: 15, title: "Post 3", name: "post-3", meta: meta("default")},
		{id: 16, typ: "works", title: "Work", name: "work", meta: meta("templates/work.php")},
		{id: 17, typ: "events", title: "Event", name: "event", meta: meta("templates/x.php")},
	}
	// The settings that the entries get are not those of Site, which does not accept a name that is not one.
	given := entrySettings()
	given.PageTemplates = map[string]string{"templates/landing.php": "lp", "templates/none.php": "", "templates/bad.php": "a b", "templates/unused.php": "u"}

	run := func(t *testing.T, s *Settings) (*Import, []*EntryDoc) {
		t.Helper()
		imp, site := planEntries(t, entrySettings(), refsDefs(), entriesWXR("", items...))
		docs, err := collectEntries(context.Background(), imp, s, site)
		if err != nil {
			t.Fatal(err)
		}
		checkEntries(t, imp, site, docs)
		return imp, docs
	}
	check := func(t *testing.T, imp *Import, docs []*EntryDoc, want map[int64]string, lines map[int64][]string) {
		t.Helper()
		for post := int64(1); post <= 17; post++ {
			if got := docOf(docs, post).Revision.Template; got != want[post] {
				t.Errorf("the template of item %d is %q, want %q", post, got, want[post])
			}
			wantItem(t, imp, docs, post, lines[post]...)
		}
		sameSet(t, "the groups", refsGroupLines(t, imp), []string{
			groupLine(imp, 13, "post", "the templates of 2 items of the post type post are not imported"+only),
			groupLine(imp, 16, "works", "the templates of 1 item of the post type works are not imported"+only),
			groupLine(imp, 17, "events", "the templates of 1 item of the post type events are not imported"+only),
		})
	}
	byName := map[int64]string{1: "full-width", 2: "landing", 3: "none", 7: "landing", 8: "page-lp", 9: "bad", 10: "landing", 12: "full-width"}
	t.Run("the settings", func(t *testing.T) {
		imp, docs := run(t, given)
		check(t, imp, docs,
			map[int64]string{1: "full-width", 2: "lp", 7: "lp", 8: "page-lp", 10: "lp", 12: "full-width"},
			map[int64][]string{
				3: {fmt.Sprintf(unmapped, "templates/none.php")},
				4: {fmt.Sprintf(unmapped, "Landing Page.php")},
				9: {`skipped: the template is not imported: invalid name "a b": use ASCII letters, digits, "-" and "_" -> {R}#/template`},
			})
		wantMetaSource(t, imp, 3, "_wp_page_template", "the page template")
	})
	t.Run("no settings", func(t *testing.T) {
		imp, docs := run(t, nil)
		check(t, imp, docs, byName, map[int64][]string{4: {fmt.Sprintf(unmapped, "Landing Page.php")}})
	})
	t.Run("settings without the table", func(t *testing.T) {
		imp, docs := run(t, entrySettings())
		check(t, imp, docs, byName, map[int64][]string{4: {fmt.Sprintf(unmapped, "Landing Page.php")}})
	})
	t.Run("a table with an empty map", func(t *testing.T) {
		s := entrySettings()
		s.PageTemplates = map[string]string{}
		imp, docs := run(t, s)
		check(t, imp, docs, byName, map[int64][]string{4: {fmt.Sprintf(unmapped, "Landing Page.php")}})
	})
	t.Run("a table that has the template but with no name", func(t *testing.T) {
		s := entrySettings()
		s.PageTemplates = map[string]string{"templates/full-width.php": ""}
		imp, _, docs := makeRefs(t, s, nil, "", page(1, "templates/full-width.php"))
		if got := docs[0].Revision.Template; got != "" {
			t.Errorf("the template is %q, want none", got)
		}
		wantItem(t, imp, docs, 1, fmt.Sprintf(unmapped, "templates/full-width.php"))
	})
}

// newRefsTidy is newTidy for a WXR of one page, or of one post if typ is "".
func newRefsTidy(t *testing.T, typ string) (*Import, *entryConv, *entryItem) {
	t.Helper()
	return newTidy(t, entriesWXR("", wxrEntry{id: 5, typ: typ, title: "T", name: "t"}))
}

func TestEntriesTidyReferences(t *testing.T) {
	bad := func(kind content.RefKind) string {
		t.Helper()
		err := content.CheckID(kind, "bad")
		if err == nil {
			t.Fatalf("%q is an ID of %s", "bad", kind)
		}
		return err.Error()
	}
	const notAllowed = "skipped: the %[1]s is not imported: not allowed for %[2]s -> {R}#/%[1]s"
	post := content.Revision{Title: "T", Slug: "t", Date: entryTime}
	page := content.Revision{Title: "T", Slug: "t"}
	with := func(r content.Revision, edit func(*content.Revision)) content.Revision {
		edit(&r)
		return r
	}
	for _, tt := range []struct {
		name, typ string
		rev, want content.Revision
		lines     []string
	}{
		{
			name: "terms that are wrong", rev: with(post, func(r *content.Revision) { r.Terms = content.Terms{"category": {"bad"}, "tag": {"bad"}} }),
			want: post, lines: []string{
				"skipped: the terms/category is not imported: " + bad(content.RefTerm) + " -> {R}#/terms/category",
				"skipped: the terms/tag is not imported: " + bad(content.RefTerm) + " -> {R}#/terms/tag",
			},
		},
		{
			name: "a taxonomy with no terms", rev: with(post, func(r *content.Revision) { r.Terms = content.Terms{"category": {"bad"}, "tag": {}} }),
			want: post, lines: []string{
				"skipped: the terms/category is not imported: " + bad(content.RefTerm) + " -> {R}#/terms/category",
				"skipped: the terms/tag is not imported: empty value; omit it -> {R}#/terms/tag",
			},
		},
		{
			name: "an author that is wrong", rev: with(post, func(r *content.Revision) { r.Author = "bad" }), want: post,
			lines: []string{"skipped: the author is not imported: " + bad(content.RefAuthor) + " -> {R}#/author"},
		},
		{
			name: "a featured image that is wrong", rev: with(post, func(r *content.Revision) { r.FeaturedImage = "bad" }), want: post,
			lines: []string{"skipped: the featuredImage is not imported: " + bad(content.RefMedia) + " -> {R}#/featuredImage"},
		},
		{
			name: "a sticky page", typ: "page", rev: with(page, func(r *content.Revision) { r.Sticky = true }), want: page,
			lines: []string{fmt.Sprintf(notAllowed, "sticky", "page")},
		},
		{
			name: "an order of a post", rev: with(post, func(r *content.Revision) { r.Order = 4 }), want: post,
			lines: []string{fmt.Sprintf(notAllowed, "order", "post")},
		},
		{
			name: "a template of a post", rev: with(post, func(r *content.Revision) { r.Template = "x" }), want: post,
			lines: []string{fmt.Sprintf(notAllowed, "template", "post")},
		},
		{
			name: "a template that is not a name", typ: "page", rev: with(page, func(r *content.Revision) { r.Template = "a b" }), want: page,
			lines: []string{`skipped: the template is not imported: invalid name "a b": use ASCII letters, digits, "-" and "_" -> {R}#/template`},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			imp, c, ei := newRefsTidy(t, tt.typ)
			ei.status = content.StatusDraft
			ei.rev = tt.rev
			c.tidy(ei)
			var want []string
			for _, l := range tt.lines {
				want = append(want, strings.ReplaceAll(l, "{R}", "revisions/"+ei.revID+".json"))
			}
			sameSet(t, "the report", itemLines(imp.Report, 5), want)
			if !reflect.DeepEqual(ei.rev, tt.want) {
				t.Errorf("the revision is %+v, want %+v", ei.rev, tt.want)
			}
		})
	}

	t.Run("only the taxonomy that is wrong is taken out", func(t *testing.T) {
		imp, c, ei := newRefsTidy(t, "")
		tag := content.FormatID(content.RefTerm, [16]byte{15: 3})
		imp.AddRef(content.RefTerm, tag, content.Ref{Type: content.TaxonomyTag, Language: "ja"})
		ei.status = content.StatusDraft
		ei.rev = with(post, func(r *content.Revision) { r.Terms = content.Terms{"category": {"bad"}, "tag": {tag}} })
		c.tidy(ei)
		if want := with(post, func(r *content.Revision) { r.Terms = content.Terms{"tag": {tag}} }); !reflect.DeepEqual(ei.rev, want) {
			t.Errorf("the revision is %+v, want %+v", ei.rev, want)
		}
		want := "skipped: the terms/category is not imported: " + bad(content.RefTerm) + " -> revisions/" + ei.revID + ".json#/terms/category"
		sameSet(t, "the report", itemLines(imp.Report, 5), []string{want})
	})
}

func TestPathRef(t *testing.T) {
	terms := []wxrTerm{
		{id: 2, taxonomy: "category", slug: "news", name: "News"},
		{id: 3, taxonomy: "category", slug: "japan", parent: "news", name: "Japan"},
		{id: 4, taxonomy: "category", slug: "%e3%83%8b%e3%83%a5%e3%83%bc%e3%82%b9", parent: "japan", name: "ニュース"},
		{id: 5, taxonomy: "category", slug: "UPPER", name: "Upper"},
		{id: 6, taxonomy: "category", slug: "long", name: strings.Repeat("a", content.MaxTextBytes+1)},
		{id: 7, taxonomy: "category", slug: "child-of-long", parent: "long", name: "Child"},
		{id: 8, taxonomy: "category", slug: "loop-a", parent: "loop-b", name: "A"},
		{id: 9, taxonomy: "category", slug: "loop-b", parent: "loop-a", name: "B"},
		{id: 10, taxonomy: "post_tag", slug: "go", name: "Go"},
		{id: 11, taxonomy: "genre", slug: "drama", name: "Drama"},
		{id: 12, taxonomy: "post_format", slug: "post-format-video", name: "Video"},
	}
	in := entriesWXR(refsChannel(nil, terms), wxrEntry{id: 1, title: "One", name: "one"}, wxrEntry{id: 2, typ: "page", title: "Two", name: "two"})
	e, rep := readString(t, "site.xml", in)
	imp := New("site.xml", e, rep)
	defs, s := refsDefs(), entrySettings()
	if err := imp.PlanEntries(s, defs); err != nil {
		t.Fatal(err)
	}
	site, err := imp.Site(s, defs)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := imp.PathRef(content.RefTerm, "x"); ok {
		t.Error("PathRef before Terms found a term")
	}
	made, err := imp.Terms(site)
	if err != nil {
		t.Fatal(err)
	}

	id := func(taxonomy, slug string) string { return termIDOf(t, imp, taxonomy, slug) }
	news, japan, katakana := id("category", "news"), id("category", "japan"), id("category", "%e3%83%8b%e3%83%a5%e3%83%bc%e3%82%b9")
	for _, tt := range []struct {
		name string
		kind content.RefKind
		id   string
		want content.PathRef
		ok   bool
	}{
		{"a term at the top", content.RefTerm, news, content.PathRef{Slug: "news"}, true},
		{"a term with a parent", content.RefTerm, japan, content.PathRef{Slug: "japan", Parent: news}, true},
		{"the slug of the import", content.RefTerm, katakana, content.PathRef{Slug: "ニュース", Parent: japan}, true},
		{"a slug in lower case", content.RefTerm, id("category", "UPPER"), content.PathRef{Slug: "upper"}, true},
		{"a tag", content.RefTerm, id("post_tag", "go"), content.PathRef{Slug: "go"}, true},
		{"a custom taxonomy", content.RefTerm, id("genre", "drama"), content.PathRef{Slug: "drama"}, true},
		{"a term that has no ID", content.RefTerm, "", content.PathRef{}, false},
		{"an ID that is not known", content.RefTerm, "trm_000000000000000000000ZZZZZ", content.PathRef{}, false},
		{"a term as a page", content.RefEntry, news, content.PathRef{}, false},
		{"a term as a medium", content.RefMedia, news, content.PathRef{}, false},
		{"a term as an author", content.RefAuthor, news, content.PathRef{}, false},
		{"a page that is not recorded", content.RefEntry, postIDOf(t, imp, 2), content.PathRef{}, false},
	} {
		got, ok := imp.PathRef(tt.kind, tt.id)
		if got != tt.want || ok != tt.ok {
			t.Errorf("%s: PathRef(%q, %q) = %+v, %v, want %+v, %v", tt.name, tt.kind, tt.id, got, ok, tt.want, tt.ok)
		}
	}
	if term := imp.WXR.TermBySlug("category", "long"); term == nil {
		t.Fatal("the WXR has no term long")
	} else if _, _, ok := imp.IDs.Lookup(TermObject(term)); ok {
		t.Error("the term that is not imported has an ID")
	}
	if p, ok := imp.PathRef(content.RefTerm, id("category", "child-of-long")); !ok || p != (content.PathRef{Slug: "child-of-long"}) {
		t.Errorf("PathRef of the child of a term that is not imported = %+v, %v, want a term at the top", p, ok)
	}
	// One of the two terms of a cycle loses its parent, and the other has it.
	a1, ok1 := imp.PathRef(content.RefTerm, id("category", "loop-a"))
	b1, ok2 := imp.PathRef(content.RefTerm, id("category", "loop-b"))
	broken := a1.Parent == "" && b1.Parent == id("category", "loop-a") || b1.Parent == "" && a1.Parent == id("category", "loop-b")
	if !ok1 || !ok2 || a1.Slug != "loop-a" || b1.Slug != "loop-b" || !broken {
		t.Errorf("PathRef of the terms of a cycle = %+v, %v and %+v, %v", a1, ok1, b1, ok2)
	}
	if len(made) != 9 {
		t.Errorf("%d terms, want 9", len(made))
	}

	// The lookup is what the paths of the site take.
	for _, term := range made {
		if term.ID != japan && term.ID != katakana {
			continue
		}
		got, err := site.TermPath(&term, imp.PathRef)
		want := map[string]string{japan: "/category/news/japan/", katakana: "/category/news/japan/ニュース/"}[term.ID]
		if err != nil || got != want {
			t.Errorf("TermPath of %q = %q, %v, want %q", term.Slug, got, err, want)
		}
	}

	t.Run("a term that is forgotten", func(t *testing.T) {
		imp.DropRef(japan)
		if p, ok := imp.PathRef(content.RefTerm, japan); ok {
			t.Errorf("PathRef of a term that Ref does not know = %+v, true", p)
		}
		if _, ok := imp.PathRef(content.RefTerm, news); !ok {
			t.Error("PathRef of another term fails")
		}
	})
	t.Run("an Import that was not made by New", func(t *testing.T) {
		var bare Import
		if _, ok := bare.PathRef(content.RefTerm, news); ok {
			t.Error("PathRef of an empty import found a term")
		}
		bare.setPath("x", content.PathRef{Slug: "s"})
		if _, ok := bare.PathRef(content.RefTerm, "x"); ok {
			t.Error("PathRef found a path that Ref does not know")
		}
		bare.AddRef(content.RefEntry, "x", content.Ref{Type: content.TypePage, Language: "ja"})
		if p, ok := bare.PathRef(content.RefEntry, "x"); !ok || p != (content.PathRef{Slug: "s"}) {
			t.Errorf("PathRef of a page = %+v, %v", p, ok)
		}
		if _, ok := bare.PathRef(content.RefTerm, "x"); ok {
			t.Error("PathRef took a page for a term")
		}
	})
}

func TestEntriesReferencesSameOutput(t *testing.T) {
	terms := []wxrTerm{
		{id: 1, taxonomy: "category", slug: "uncategorized", name: "Uncategorized"},
		{id: 2, taxonomy: "category", slug: "news", name: "News"},
		{id: 4, taxonomy: "category", slug: "tech", name: "Tech"},
		{id: 5, taxonomy: "post_tag", slug: "go", name: "Go"},
		{id: 6, taxonomy: "genre", slug: "drama", name: "Drama"},
		{id: 7, taxonomy: "keyword", slug: "kw", name: "Keyword"},
	}
	authors := []wxrAuthor{{id: 2, login: "alice", display: "Alice"}}
	items := []wxrEntry{
		{id: 1, title: "One", name: "one", creator: "alice", sticky: true, link: "https://example.com/tech/one/",
			terms: joinRefs(termRefs("category", "news", "tech"), termRefs("post_tag", "go"), termRefs("genre", "drama"), termRefs("keyword", "kw")),
			meta:  [][2]string{{"_thumbnail_id", "20"}}},
		{id: 2, typ: "page", title: "Page", name: "page", menuOrder: 3, meta: [][2]string{{"_wp_page_template", "templates/a.php"}}, sticky: true, terms: termRefs("category", "news")},
		{id: 3, title: "Three", name: "three", menuOrder: 1, meta: [][2]string{{"_wp_page_template", "x.php"}}, creator: "bob"},
		{id: 4, typ: "works", title: "Work", name: "work", terms: termRefs("category", "tech", "ghost")},
		{id: 5, title: "Five", name: "five", status: "draft", creator: "alice", terms: termRefs("category", "tech", "news")},
		{id: 20, typ: "attachment", status: "inherit", title: "a.png", name: "a", meta: [][2]string{{"_wp_attached_file", "2026/10/a.png"}}},
	}
	s := refsSettings("/{category}/{slug}/")
	s.DefaultCategory = "uncategorized"
	var texts []string
	for range 3 {
		imp, _, docs := makeRefs(t, s, nil, refsChannel(authors, terms), items...)
		texts = append(texts, entriesText(t, imp, docs))
	}
	if texts[0] != texts[1] || texts[1] != texts[2] {
		t.Errorf("conversions of the same WXR differ:\n%s\n%s", texts[0], texts[1])
	}
	for _, want := range []string{`"terms"`, `"author"`, `"featuredImage"`, `"sticky":true`, `"order":3`, `"template":"a"`} {
		if !strings.Contains(texts[0], want) {
			t.Errorf("the output has no %s: %s", want, texts[0])
		}
	}
}
