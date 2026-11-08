package wpimport

import (
	"bytes"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

// termSite is the site of the tests of Terms: Japanese, with the custom taxonomies genre, which has a hierarchy, and
// keyword, which has none.
func termSite() *content.Site {
	return &content.Site{
		Languages:       []string{"ja"},
		DefaultLanguage: "ja",
		Taxonomies:      []content.TaxonomyDef{{Name: "genre", Hierarchical: true}, {Name: "keyword"}},
	}
}

// wxrTerm is a term to write into a WXR.
type wxrTerm struct {
	id                              int // 0 for a term with no wp:term_id, as in WXR 1.0
	taxonomy                        string
	slug, parent, name, description string
	meta                            []string // the keys of the termmeta
}

func (x wxrTerm) xml() string {
	var b strings.Builder
	cdata := func(tag, v string) { fmt.Fprintf(&b, "<wp:%s><![CDATA[%s]]></wp:%s>", tag, v, tag) }
	elem, prefix := "term", "term_"
	switch {
	case x.taxonomy == "category":
		elem, prefix = "category", "category_"
	case x.taxonomy == "post_tag" && x.parent == "":
		elem, prefix = "tag", "tag_"
	}
	fmt.Fprintf(&b, "<wp:%s>", elem)
	if x.id > 0 {
		fmt.Fprintf(&b, "<wp:term_id>%d</wp:term_id>", x.id)
	}
	switch elem {
	case "category":
		cdata("category_nicename", x.slug)
		cdata("category_parent", x.parent)
		cdata("cat_name", x.name)
	case "tag":
		cdata("tag_slug", x.slug)
		cdata("tag_name", x.name)
	default:
		cdata("term_taxonomy", x.taxonomy)
		cdata("term_slug", x.slug)
		cdata("term_parent", x.parent)
		cdata("term_name", x.name)
	}
	if x.description != "" {
		cdata(prefix+"description", x.description)
	}
	for _, key := range x.meta {
		fmt.Fprintf(&b, "<wp:termmeta><wp:meta_key><![CDATA[%s]]></wp:meta_key><wp:meta_value><![CDATA[v]]></wp:meta_value></wp:termmeta>", key)
	}
	fmt.Fprintf(&b, "</wp:%s>\n", elem)
	return b.String()
}

// termsWXR writes a WXR of the site https://example.com with the terms, a term to a line.
func termsWXR(terms ...wxrTerm) string {
	var b strings.Builder
	b.WriteString(head + "<link>https://example.com</link>\n")
	for _, x := range terms {
		b.WriteString(x.xml())
	}
	b.WriteString(tail)
	return b.String()
}

// makeTerms reads the WXR in, and makes the terms of site and checks them.
func makeTerms(t *testing.T, site *content.Site, in string) (*Import, []content.Term) {
	t.Helper()
	e, rep := readString(t, "site.xml", in)
	imp := New("site.xml", e, rep)
	terms, err := imp.Terms(site)
	if err != nil {
		t.Fatalf("Terms = %v", err)
	}
	checkTerms(t, imp, site, terms)
	return imp, terms
}

// checkTerms checks what holds for the terms of any WXR: each passes Validate with the records of the import, which
// finds it; they are in the order of their IDs, with no slug that another has in its taxonomy, and a translation group
// of its own each; and their parents make no cycle.
func checkTerms(t *testing.T, imp *Import, site *content.Site, terms []content.Term) {
	t.Helper()
	slugs := make(map[[3]string]string)
	groups := make(map[string]string)
	byID := make(map[string]content.Term, len(terms))
	for i, term := range terms {
		byID[term.ID] = term
		if err := term.Validate(site, imp.Ref); err != nil {
			t.Errorf("term %s (%.40q): %v", term.ID, term.Name, err)
		}
		if r, ok := imp.Ref(content.RefTerm, term.ID); !ok || r != (content.Ref{Type: term.Taxonomy, Language: site.DefaultLanguage}) {
			t.Errorf("Ref of term %s = %+v, %v", term.ID, r, ok)
		}
		if i > 0 && terms[i-1].ID >= term.ID {
			t.Errorf("terms %s and %s are not in the order of their IDs", terms[i-1].ID, term.ID)
		}
		key := [3]string{term.Taxonomy, term.Language, term.Slug}
		if other, ok := slugs[key]; ok {
			t.Errorf("terms %s and %s have the slug %q in %s", other, term.ID, term.Slug, term.Taxonomy)
		}
		slugs[key] = term.ID
		if other, ok := groups[term.TranslationGroup]; ok {
			t.Errorf("terms %s and %s have the translation group %s", other, term.ID, term.TranslationGroup)
		}
		groups[term.TranslationGroup] = term.ID
	}
	acyclic := make(map[string]bool)
	for _, term := range terms {
		var path []string
		onPath := make(map[string]bool)
		for id := term.ID; id != "" && !acyclic[id]; id = byID[id].Parent {
			if _, ok := byID[id]; !ok {
				t.Errorf("the parent %s of a term is not among the terms", id)
				break
			}
			if onPath[id] {
				t.Errorf("the parents of term %s make a cycle", term.ID)
				break
			}
			onPath[id] = true
			path = append(path, id)
		}
		for _, id := range path {
			acyclic[id] = true
		}
	}
}

// reportLines returns the lines of the text of the report.
func reportLines(t *testing.T, rep *Report) []string {
	t.Helper()
	var b bytes.Buffer
	if err := rep.WriteText(&b); err != nil {
		t.Fatal(err)
	}
	if b.Len() == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
}

// wantLines compares the lines of the report, in any order, with want. A line of more than 300 bytes is shown cut.
func wantLines(t *testing.T, rep *Report, want []string) {
	t.Helper()
	got := reportLines(t, rep)
	slices.Sort(got)
	want = slices.Clone(want)
	slices.Sort(want)
	if slices.Equal(got, want) {
		return
	}
	cut := func(lines []string) string {
		var out []string
		for _, l := range lines {
			if len(l) > 300 {
				l = l[:150] + " ... " + l[len(l)-150:]
			}
			out = append(out, l)
		}
		return strings.Join(out, "\n")
	}
	t.Errorf("the report is\n%s\nwant\n%s", cut(got), cut(want))
}

// termAt returns the term of the WXR that is in the taxonomy tax and has the ID id.
func termAt(t *testing.T, e *wxr.Export, tax string, id int64) *wxr.Term {
	t.Helper()
	term := e.Term(tax, id)
	if term == nil {
		t.Fatalf("no term %d of %s", id, tax)
	}
	return term
}

// idOf returns the ID of the term of WordPress.
func idOf(t *testing.T, imp *Import, term *wxr.Term) string {
	t.Helper()
	_, id, ok := imp.IDs.Lookup(TermObject(term))
	if !ok {
		t.Fatalf("term %d (%s %s) has no ID", term.ID, term.Taxonomy, term.Slug)
	}
	return id
}

// who is the start of a line of the report, up to the colon after the kind, for the term.
func who(file, kind string, term *wxr.Term) string {
	what := "term"
	if term.ID != 0 {
		what += fmt.Sprintf(" %d", term.ID)
	}
	if term.Name == "" {
		what += fmt.Sprintf(" (%s)", typeText(term.Taxonomy))
	} else {
		what += fmt.Sprintf(" (%s %q)", typeText(term.Taxonomy), term.Name)
	}
	return fmt.Sprintf("%s:%d:%d: %s: %s: ", file, term.Line, term.Column, kind, what)
}

// target is the end of a line of the report for the term with the ID id.
func target(id, pointer string) string {
	s := " -> terms/" + id + ".json"
	if pointer != "" {
		s += "#" + pointer
	}
	return s
}

const commentMsg = "wordpress-6.xml:12:1: skipped: channel: 2 comments on 1 item are not imported; whiteroom has no comments"

func TestTermsSample(t *testing.T) {
	e, rep := readSample(t, "wordpress-6.xml")
	imp := New("wordpress-6.xml", e, rep)
	site := termSite()
	terms, err := imp.Terms(site)
	if err != nil {
		t.Fatal(err)
	}
	checkTerms(t, imp, site, terms)

	for _, w := range []struct {
		wpTax            string
		wpID             int64
		taxonomy         string
		slug, name, desc string
		parent           int64 // the WordPress ID of the parent
	}{
		{"category", 1, "category", "uncategorized", "未分類", "", 0},
		{"category", 3, "category", "お知らせ", "お知らせ", "サイトのお知らせ", 0},
		{"category", 4, "category", "技術", "技術", "", 3},
		{"post_tag", 5, "tag", "wordpress", "WordPress", "", 0},
		{"post_tag", 6, "tag", "日記", "日記", "日々の記録", 0},
		{"genre", 7, "genre", "fiction", "フィクション", "", 0},
		{"genre", 8, "genre", "sf", "SF", "", 7},
	} {
		term := termAt(t, e, w.wpTax, w.wpID)
		want := content.Term{
			ID:               idOf(t, imp, term),
			Taxonomy:         w.taxonomy,
			Language:         "ja",
			TranslationGroup: imp.IDs.NewID(content.RefTranslationGroup, TermObject(term).key()),
			Name:             w.name,
			Slug:             w.slug,
			Description:      w.desc,
		}
		if w.parent != 0 {
			want.Parent = idOf(t, imp, termAt(t, e, w.wpTax, w.parent))
		}
		i := slices.IndexFunc(terms, func(x content.Term) bool { return x.ID == want.ID })
		if i < 0 {
			t.Errorf("no term for term %d of %s", w.wpID, w.wpTax)
		} else if terms[i] != want {
			t.Errorf("term %d of %s = %+v, want %+v", w.wpID, w.wpTax, terms[i], want)
		}
	}
	if len(terms) != 7 {
		t.Errorf("%d terms, want 7", len(terms))
	}
	// The menu is for the conversion of menus.
	if _, _, ok := imp.IDs.Lookup(TermObject(termAt(t, e, "nav_menu", 9))); ok {
		t.Error("the menu nav_menu 9 has an ID")
	}

	file := "wordpress-6.xml"
	t3, t5, t8 := termAt(t, e, "category", 3), termAt(t, e, "post_tag", 5), termAt(t, e, "genre", 8)
	wantLines(t, rep, []string{
		commentMsg,
		who(file, "changed", t3) + `kept only the text of the HTML; lost a link to "https://example.com/"` + target(idOf(t, imp, t3), "/description"),
		who(file, "skipped", t5) + `termmeta "accent_color" is not imported; whiteroom terms have no custom fields` + target(idOf(t, imp, t5), ""),
		who(file, "skipped", t8) + `termmeta "sort_key" is not imported; whiteroom terms have no custom fields` + target(idOf(t, imp, t8), ""),
	})
	// The places are those of the sample.
	if line := who(file, "changed", t3); line != `wordpress-6.xml:31:2: changed: term 3 (category "お知らせ"): ` {
		t.Errorf("the start of the line for the term 3 is %q", line)
	}
}

func TestTermsSampleWithoutTheTaxonomy(t *testing.T) {
	e, rep := readSample(t, "wordpress-6.xml")
	imp := New("wordpress-6.xml", e, rep)
	site := &content.Site{Languages: []string{"ja"}, DefaultLanguage: "ja"}
	terms, err := imp.Terms(site)
	if err != nil {
		t.Fatal(err)
	}
	checkTerms(t, imp, site, terms)
	if len(terms) != 5 {
		t.Errorf("%d terms, want the categories and the tags: 5", len(terms))
	}
	if _, _, ok := imp.IDs.Lookup(TermObject(termAt(t, e, "genre", 7))); ok {
		t.Error("the term 7 of genre has an ID")
	}
	file := "wordpress-6.xml"
	t3, t5, g7 := termAt(t, e, "category", 3), termAt(t, e, "post_tag", 5), termAt(t, e, "genre", 7)
	wantLines(t, rep, []string{
		commentMsg,
		who(file, "changed", t3) + `kept only the text of the HTML; lost a link to "https://example.com/"` + target(idOf(t, imp, t3), "/description"),
		who(file, "skipped", t5) + `termmeta "accent_color" is not imported; whiteroom terms have no custom fields` + target(idOf(t, imp, t5), ""),
		fmt.Sprintf("%s:%d:%d: skipped: term (genre): 2 terms of the taxonomy genre are not imported; define genre in the schema file to import them", file, g7.Line, g7.Column),
	})
}

func TestTermsOldWXR(t *testing.T) {
	t.Run("WXR 1.0, which has no IDs", func(t *testing.T) {
		e, rep := readSample(t, "wordpress-3.0.xml")
		imp := New("wordpress-3.0.xml", e, rep)
		site := termSite()
		terms, err := imp.Terms(site)
		if err != nil {
			t.Fatal(err)
		}
		checkTerms(t, imp, site, terms)
		if len(terms) != 4 {
			t.Fatalf("%d terms, want 4", len(terms))
		}
		by := func(tax, slug string) content.Term {
			t.Helper()
			id := idOf(t, imp, e.TermBySlug(tax, slug))
			for _, term := range terms {
				if term.ID == id {
					return term
				}
			}
			t.Fatalf("no term for %s %s", tax, slug)
			return content.Term{}
		}
		uncategorized, travel := by("category", "uncategorized"), by("category", "travel")
		if uncategorized.Name != "Uncategorized" || uncategorized.Parent != "" || uncategorized.Taxonomy != "category" {
			t.Errorf("uncategorized = %+v", uncategorized)
		}
		if travel.Parent != uncategorized.ID || travel.Name != "Travel" || travel.Slug != "travel" {
			t.Errorf("travel = %+v, want Uncategorized %s as the parent", travel, uncategorized.ID)
		}
		if rss := by("post_tag", "rss"); rss.Taxonomy != "tag" || rss.Name != "rss" {
			t.Errorf("rss = %+v", rss)
		}
		wantLines(t, rep, []string{"wordpress-3.0.xml:12:1: skipped: channel: 1 comment on 1 item is not imported; whiteroom has no comments"})
	})
	t.Run("WXR 1.1", func(t *testing.T) {
		e, rep := readSample(t, "wordpress-3.3.xml")
		imp := New("wordpress-3.3.xml", e, rep)
		site := termSite()
		terms, err := imp.Terms(site)
		if err != nil {
			t.Fatal(err)
		}
		checkTerms(t, imp, site, terms)
		if len(terms) != 3 {
			t.Fatalf("%d terms, want 3", len(terms))
		}
		id := idOf(t, imp, termAt(t, e, "category", 2))
		for _, term := range terms {
			if term.ID == id && (term.Slug != "ニュース" || term.Name != "ニュース" || term.Description != "ニュースの一覧") {
				t.Errorf("the category 2 = %+v", term)
			}
		}
		wantLines(t, rep, nil)
	})
}

func TestTermsOtherTaxonomies(t *testing.T) {
	in := termsWXR(
		wxrTerm{id: 1, taxonomy: "category", slug: "news", name: "News"},
		wxrTerm{id: 2, taxonomy: "post_format", slug: "post-format-aside", name: "Aside"},
		wxrTerm{id: 3, taxonomy: "series", slug: "a", name: "A"},
		wxrTerm{id: 4, taxonomy: "series", slug: "b", name: "B"},
		wxrTerm{id: 5, taxonomy: "gallery", slug: "g", name: "G"},
		wxrTerm{id: 6, taxonomy: "My Tax", slug: "m", name: "M"},
		wxrTerm{id: 7, taxonomy: "nav_menu", slug: "main", name: "Main"},
		wxrTerm{id: 8, taxonomy: "language", slug: "ja", name: "日本語"},
		wxrTerm{id: 9, taxonomy: "term_language", slug: "pll_ja", name: "pll_ja"},
		wxrTerm{id: 10, taxonomy: "post_translations", slug: "pll_1", name: "pll_1"},
		wxrTerm{id: 11, taxonomy: "term_translations", slug: "pll_2", name: "pll_2"},
		wxrTerm{id: 12, taxonomy: "keyword", slug: "k", name: "K"},
		wxrTerm{id: 13, taxonomy: "post_tag", slug: "t", name: "T"},
		wxrTerm{id: 14, taxonomy: "series", slug: "c", name: "C"},
		wxrTerm{id: 15, taxonomy: "link_category", slug: "blogroll", name: "Blogroll"},
		wxrTerm{id: 16, taxonomy: "tag", slug: "plain", name: "Plain"},
	)
	imp, terms := makeTerms(t, termSite(), in)
	var got []string
	for _, term := range terms {
		got = append(got, term.Taxonomy+" "+term.Slug)
	}
	slices.Sort(got)
	if want := []string{"category news", "keyword k", "tag t"}; !slices.Equal(got, want) {
		t.Errorf("the terms are %q, want %q", got, want)
	}
	e := imp.WXR
	at := func(id int64) string {
		for i := range e.Terms {
			if e.Terms[i].ID == id {
				return fmt.Sprintf("site.xml:%d:%d: skipped: term (", e.Terms[i].Line, e.Terms[i].Column)
			}
		}
		t.Fatalf("no term %d", id)
		return ""
	}
	wantLines(t, imp.Report, []string{
		at(2) + "post_format): 1 term of the taxonomy post_format is not imported; whiteroom does not use it",
		at(3) + "series): 3 terms of the taxonomy series are not imported; define series in the schema file to import them",
		at(5) + "gallery): 1 term of the taxonomy gallery is not imported; define gallery in the schema file to import it",
		at(6) + `"My Tax"): 1 term of the taxonomy "My Tax" is not imported; define "My Tax" in the schema file to import it`,
		at(15) + "link_category): 1 term of the taxonomy link_category is not imported; whiteroom does not use it",
		at(16) + "tag): 1 term of the taxonomy tag is not imported; whiteroom uses the name tag for post_tag",
	})
	for i := range e.Terms {
		if _, _, ok := imp.IDs.Lookup(TermObject(&e.Terms[i])); ok != slices.Contains([]int64{1, 12, 13}, e.Terms[i].ID) {
			t.Errorf("the term %d of %s has an ID: %v", e.Terms[i].ID, e.Terms[i].Taxonomy, ok)
		}
	}

	// With no custom taxonomy in the site, keyword is not imported either.
	site := &content.Site{Languages: []string{"ja"}, DefaultLanguage: "ja"}
	imp, terms = makeTerms(t, site, in)
	if len(terms) != 2 {
		t.Errorf("%d terms with no custom taxonomies, want category news and tag t", len(terms))
	}
	if n := len(reportLines(t, imp.Report)); n != 7 {
		t.Errorf("%d lines in the report, want 7:\n%s", n, strings.Join(reportLines(t, imp.Report), "\n"))
	}
}

func TestTermsNames(t *testing.T) {
	long := strings.Repeat("x", content.MaxTextBytes+1)
	in := termsWXR(
		wxrTerm{id: 1, taxonomy: "category", slug: "a", name: "Tom &amp; Jerry"},
		wxrTerm{id: 2, taxonomy: "category", slug: "b", name: "A &lt;b&gt; &quot;q&quot; &nbsp;&#12354;"},
		wxrTerm{id: 3, taxonomy: "category", slug: "c", name: "Line1\nLine2\tTab"},
		wxrTerm{id: 4, taxonomy: "category", slug: "d", name: "  padded  "},
		wxrTerm{id: 5, taxonomy: "category", slug: "e", name: ""},
		wxrTerm{id: 6, taxonomy: "category", slug: "f", name: "\n\t"},
		wxrTerm{id: 7, taxonomy: "category", slug: "g", name: "<b>not markup</b>"},
		wxrTerm{id: 8, taxonomy: "category", slug: "h", name: long},
		wxrTerm{id: 9, taxonomy: "category", slug: "i", name: "Line1\nLine2 &amp; Line3"},
	)
	imp, terms := makeTerms(t, termSite(), in)
	e := imp.WXR
	names := make(map[string]string)
	for _, term := range terms {
		names[term.Slug] = term.Name
	}
	for slug, want := range map[string]string{
		"a": "Tom & Jerry",
		"b": `A <b> "q" ` + string(rune(0xA0)) + string(rune(12354)),
		"c": "Line1 Line2 Tab",
		"d": "padded",
		"e": "e",
		"f": "f",
		"g": "<b>not markup</b>",
		"i": "Line1 Line2 & Line3",
	} {
		if got, ok := names[slug]; !ok || got != want {
			t.Errorf("the name of the term with the slug %s = %q, %v, want %q", slug, got, ok, want)
		}
	}
	if _, ok := names["h"]; ok || len(terms) != 8 {
		t.Errorf("the term with the long name is among the %d terms", len(terms))
	}
	if _, _, ok := imp.IDs.Lookup(TermObject(termAt(t, e, "category", 8))); ok {
		t.Error("the term with the long name has an ID")
	}
	if _, ok := imp.Ref(content.RefTerm, "trm_00000000000000000000000000"); ok {
		t.Error("Ref knows an ID that no term has")
	}
	droppedID := idOf(t, imp, termAt(t, e, "category", 1))
	if _, ok := imp.Ref(content.RefTerm, droppedID); !ok {
		t.Errorf("Ref does not know the ID %s of a term", droppedID)
	}
	file := "site.xml"
	id := func(wp int64) string { return idOf(t, imp, termAt(t, e, "category", wp)) }
	long8 := termAt(t, e, "category", 8)
	wantLines(t, imp.Report, []string{
		who(file, "changed", termAt(t, e, "category", 3)) + "line breaks and tabs in the name are spaces here" + target(id(3), "/name"),
		who(file, "changed", termAt(t, e, "category", 5)) + `the name is empty; it is the slug "e" here` + target(id(5), "/name"),
		who(file, "changed", termAt(t, e, "category", 6)) + `the name is empty; it is the slug "f" here` + target(id(6), "/name"),
		who(file, "changed", termAt(t, e, "category", 9)) + "line breaks and tabs in the name are spaces here" + target(id(9), "/name"),
		fmt.Sprintf("%s:%d:%d: skipped: term 8 (category %q): the term is not imported: its name is too long: %d bytes (max %d)",
			file, long8.Line, long8.Column, long, len(long), content.MaxTextBytes),
	})
}

func TestTermsSlugs(t *testing.T) {
	pa := string(rune(0x30D1)) // which NFC makes of HA and the semi-voiced sound mark, in the slug of the term 10
	in := termsWXR(
		wxrTerm{id: 1, taxonomy: "category", slug: "News", name: "N1"},
		wxrTerm{id: 2, taxonomy: "category", slug: "%zz", name: "N2"},
		wxrTerm{id: 3, taxonomy: "category", slug: "!!!", name: "N3"},
		wxrTerm{id: 4, taxonomy: "category", slug: "%e3%81%8a%e7%9f%a5%e3%82%89%e3%81%9b", name: "N4"},
		wxrTerm{id: 5, taxonomy: "category", slug: "a.b", name: "N5"},
		wxrTerm{id: 6, taxonomy: "category", slug: "dup", name: "N6"},
		wxrTerm{id: 7, taxonomy: "category", slug: "DUP", name: "N7"},
		wxrTerm{id: 8, taxonomy: "post_tag", slug: "dup", name: "N8"},
		wxrTerm{id: 9, taxonomy: "category", slug: "%e3%83%91", name: "N9"},
		wxrTerm{id: 10, taxonomy: "category", slug: "%e3%83%8f%e3%82%9a", name: "N10"},
		wxrTerm{id: 11, taxonomy: "category", slug: "?", name: "N11"},
		wxrTerm{id: 12, taxonomy: "category", slug: "dup-2", name: "N12"},
		wxrTerm{id: 13, taxonomy: "category", slug: "Dup", name: "N13"},
	)
	imp, terms := makeTerms(t, termSite(), in)
	e := imp.WXR
	slugOf := func(tax string, wp int64) string {
		id := idOf(t, imp, termAt(t, e, tax, wp))
		for _, term := range terms {
			if term.ID == id {
				return term.Slug
			}
		}
		t.Fatalf("no term for the %s %d", tax, wp)
		return ""
	}
	// The slugs that WordPress had come first: the term 12 has dup-2, which the term 7 would be given after dup.
	for wp, want := range map[int64]string{
		1: "news", 2: "zz", 3: "term-3", 4: "お知らせ", 5: "a-b", 6: "dup", 7: "dup-3", 9: pa, 10: pa + "-2", 11: "term-11",
		12: "dup-2", 13: "dup-4",
	} {
		if got := slugOf("category", wp); got != want {
			t.Errorf("the slug of the category %d = %q, want %q", wp, got, want)
		}
	}
	// The tag with the slug of a category is in another taxonomy.
	if got := slugOf("post_tag", 8); got != "dup" {
		t.Errorf("the slug of the tag = %q, want dup", got)
	}

	file := "site.xml"
	id := func(wp int64) string { return idOf(t, imp, termAt(t, e, "category", wp)) }
	line := func(wp int64, msg string) string {
		return who(file, "changed", termAt(t, e, "category", wp)) + msg + target(id(wp), "/slug")
	}
	wantLines(t, imp.Report, []string{
		line(1, `the slug "News" is "news" here: normalized to lower case and NFC`),
		line(2, `the slug "%zz" is "zz" here: it cannot be decoded: invalid percent-encoding at byte 0; characters that a slug cannot have are "-"`),
		line(3, `nothing is left of the slug "!!!"; it is "term-3" here`),
		line(5, `the slug "a.b" is "a-b" here: characters that a slug cannot have are "-"`),
		line(7, `the slug "DUP" is "dup" here: normalized to lower case and NFC`),
		line(7, `the slug "dup" is also that of another term of category; it is "dup-3" here`),
		line(10, `the slug "%e3%83%8f%e3%82%9a" is "`+pa+`" here: normalized to lower case and NFC`),
		line(10, `the slug "`+pa+`" is also that of another term of category; it is "`+pa+`-2" here`),
		line(11, `nothing is left of the slug "?"; it is "term-11" here`),
		line(13, `the slug "Dup" is "dup" here: normalized to lower case and NFC`),
		line(13, `the slug "dup" is also that of another term of category; it is "dup-4" here`),
	})
}

// TestTermsKeptSlugFirst checks that a term whose slug is the one that WordPress had is not given a number because the
// slug of another term, which the import changed to it, comes first in the WXR.
func TestTermsKeptSlugFirst(t *testing.T) {
	pan := string(rune(0x30D1)) + string(rune(0x30F3)) // PA and N, in NFC
	if got, err := wxr.DecodeSlug("%e3%83%91%e3%83%b3"); err != nil || got != pan {
		t.Fatalf("DecodeSlug = %q, %v, want %q", got, err, pan)
	}
	t.Run("a slug in NFD", func(t *testing.T) {
		in := termsWXR(
			wxrTerm{id: 5, taxonomy: "post_tag", slug: "%e3%83%8f%e3%82%9a%e3%83%b3", name: "N5"},
			wxrTerm{id: 6, taxonomy: "post_tag", slug: "%e3%83%91%e3%83%b3", name: "N6"},
		)
		imp, terms := makeTerms(t, termSite(), in)
		e := imp.WXR
		slugOf := func(wp int64) string {
			id := idOf(t, imp, termAt(t, e, "post_tag", wp))
			for _, term := range terms {
				if term.ID == id {
					return term.Slug
				}
			}
			t.Fatalf("no term for the tag %d", wp)
			return ""
		}
		if a, b := slugOf(5), slugOf(6); a != pan+"-2" || b != pan {
			t.Errorf("the slugs of the tags 5 and 6 are %q and %q, want %q and %q", a, b, pan+"-2", pan)
		}
		first := termAt(t, e, "post_tag", 5)
		line := func(msg string) string {
			return who("site.xml", "changed", first) + msg + target(idOf(t, imp, first), "/slug")
		}
		wantLines(t, imp.Report, []string{
			line(`the slug "%e3%83%8f%e3%82%9a%e3%83%b3" is "` + pan + `" here: normalized to lower case and NFC`),
			line(`the slug "` + pan + `" is also that of another term of post_tag; it is "` + pan + `-2" here`),
		})
	})

	t.Run("a slug that could not be taken is given a number before the slugs that the import changed", func(t *testing.T) {
		// The terms 1 and 3 have the slug pa, one written with percent-encoding, which WordPress does not do; the term 2
		// has the slug PA-2, which becomes pa-2.
		in := termsWXR(
			wxrTerm{id: 1, taxonomy: "category", slug: "pa", name: "N1"},
			wxrTerm{id: 2, taxonomy: "category", slug: "PA-2", name: "N2"},
			wxrTerm{id: 3, taxonomy: "category", slug: "%70%61", name: "N3"},
		)
		imp, terms := makeTerms(t, termSite(), in)
		e := imp.WXR
		got := make(map[int64]string)
		for _, term := range terms {
			for wp := int64(1); wp <= 3; wp++ {
				if term.ID == idOf(t, imp, termAt(t, e, "category", wp)) {
					got[wp] = term.Slug
				}
			}
		}
		if want := map[int64]string{1: "pa", 2: "pa-2-2", 3: "pa-2"}; !reflect.DeepEqual(got, want) {
			t.Errorf("the slugs are %v, want %v", got, want)
		}
		line := func(wp int64, msg string) string {
			term := termAt(t, e, "category", wp)
			return who("site.xml", "changed", term) + msg + target(idOf(t, imp, term), "/slug")
		}
		wantLines(t, imp.Report, []string{
			line(2, `the slug "PA-2" is "pa-2" here: normalized to lower case and NFC`),
			line(2, `the slug "pa-2" is also that of another term of category; it is "pa-2-2" here`),
			line(3, `the slug "pa" is also that of another term of category; it is "pa-2" here`),
		})
	})

	t.Run("a slug with hyphens at the ends, which WordPress does not write, comes after", func(t *testing.T) {
		in := termsWXR(
			wxrTerm{id: 1, taxonomy: "category", slug: "-dup-", name: "N1"},
			wxrTerm{id: 2, taxonomy: "category", slug: "dup", name: "N2"},
		)
		imp, terms := makeTerms(t, termSite(), in)
		e := imp.WXR
		got := make(map[int64]string)
		for _, term := range terms {
			for wp := int64(1); wp <= 2; wp++ {
				if term.ID == idOf(t, imp, termAt(t, e, "category", wp)) {
					got[wp] = term.Slug
				}
			}
		}
		if want := map[int64]string{1: "dup-2", 2: "dup"}; !reflect.DeepEqual(got, want) {
			t.Errorf("the slugs are %v, want %v", got, want)
		}
		first := termAt(t, e, "category", 1)
		line := func(msg string) string {
			return who("site.xml", "changed", first) + msg + target(idOf(t, imp, first), "/slug")
		}
		wantLines(t, imp.Report, []string{
			line(`the slug "-dup-" is "dup" here: "-" at the ends is removed`),
			line(`the slug "dup" is also that of another term of category; it is "dup-2" here`),
		})
	})
}

func TestTermsSlugsOfAWXRWithoutIDs(t *testing.T) {
	in := termsWXR(
		wxrTerm{taxonomy: "category", slug: "!!!", name: "N1"},
		wxrTerm{taxonomy: "category", slug: "###", name: "N2"},
		wxrTerm{taxonomy: "category", slug: "ok", name: "N3"},
	)
	imp, terms := makeTerms(t, termSite(), in)
	var slugs []string
	for _, term := range terms {
		slugs = append(slugs, term.Slug)
	}
	slices.Sort(slugs)
	if want := []string{"ok", "term", "term-2"}; !slices.Equal(slugs, want) {
		t.Errorf("the slugs are %q, want %q", slugs, want)
	}
	e := imp.WXR
	file := "site.xml"
	line := func(i int, msg string) string {
		return who(file, "changed", &e.Terms[i]) + msg + target(idOf(t, imp, &e.Terms[i]), "/slug")
	}
	wantLines(t, imp.Report, []string{
		line(0, `nothing is left of the slug "!!!"; it is "term" here`),
		line(1, `nothing is left of the slug "###"; it is "term" here`),
		line(1, `the slug "term" is also that of another term of category; it is "term-2" here`),
	})
}

func TestTermsParents(t *testing.T) {
	in := termsWXR(
		wxrTerm{id: 1, taxonomy: "category", slug: "top", name: "Top"},
		wxrTerm{id: 2, taxonomy: "category", slug: "mid", parent: "top", name: "Mid"},
		wxrTerm{id: 3, taxonomy: "category", slug: "low", parent: "mid", name: "Low"},
		wxrTerm{id: 4, taxonomy: "category", slug: "orphan", parent: "nowhere", name: "Orphan"},
		wxrTerm{id: 5, taxonomy: "category", slug: "self", parent: "self", name: "Self"},
		wxrTerm{id: 6, taxonomy: "category", slug: "ca", parent: "cb", name: "Ca"},
		wxrTerm{id: 7, taxonomy: "category", slug: "cb", parent: "ca", name: "Cb"},
		wxrTerm{id: 8, taxonomy: "category", slug: "cc", parent: "cd", name: "Cc"},
		wxrTerm{id: 9, taxonomy: "category", slug: "cd", parent: "ce", name: "Cd"},
		wxrTerm{id: 10, taxonomy: "category", slug: "ce", parent: "cc", name: "Ce"},
		wxrTerm{id: 11, taxonomy: "category", slug: "tail", parent: "cd", name: "Tail"},
		wxrTerm{id: 12, taxonomy: "post_tag", slug: "t1", name: "T1"},
		wxrTerm{id: 13, taxonomy: "post_tag", slug: "t2", parent: "t1", name: "T2"},
		wxrTerm{id: 14, taxonomy: "keyword", slug: "k1", name: "K1"},
		wxrTerm{id: 15, taxonomy: "keyword", slug: "k2", parent: "k1", name: "K2"},
		wxrTerm{id: 16, taxonomy: "genre", slug: "g1", name: "G1"},
		wxrTerm{id: 17, taxonomy: "genre", slug: "g2", parent: "g1", name: "G2"},
		wxrTerm{id: 18, taxonomy: "category", slug: "other", parent: "t1", name: "Other"},
		wxrTerm{id: 19, taxonomy: "genre", slug: "t1", name: "Genre T1"},
		wxrTerm{id: 20, taxonomy: "genre", slug: "g3", parent: "t1", name: "G3"},
	)
	imp, terms := makeTerms(t, termSite(), in)
	e := imp.WXR
	byWP := func(tax string, wp int64) content.Term {
		id := idOf(t, imp, termAt(t, e, tax, wp))
		for _, term := range terms {
			if term.ID == id {
				return term
			}
		}
		t.Fatalf("no term for %s %d", tax, wp)
		return content.Term{}
	}
	idOfWP := func(tax string, wp int64) string { return idOf(t, imp, termAt(t, e, tax, wp)) }
	for _, tt := range []struct {
		tax    string
		wp     int64
		parent int64 // 0 for none
	}{
		{"category", 1, 0}, {"category", 2, 1}, {"category", 3, 2}, {"category", 4, 0}, {"category", 5, 0}, {"category", 11, 9},
		{"genre", 17, 16}, {"genre", 20, 19},
		{"post_tag", 13, 0}, {"keyword", 15, 0}, {"category", 18, 0},
	} {
		want := ""
		if tt.parent != 0 {
			want = idOfWP(tt.tax, tt.parent)
		}
		if got := byWP(tt.tax, tt.wp).Parent; got != want {
			t.Errorf("the parent of the %s %d = %q, want %q (the term %d)", tt.tax, tt.wp, got, want, tt.parent)
		}
	}

	// Of each cycle, the term with the lowest ID is at the top, and the others keep their parents.
	lowest := func(wps ...int64) int64 {
		best := wps[0]
		for _, wp := range wps[1:] {
			if idOfWP("category", wp) < idOfWP("category", best) {
				best = wp
			}
		}
		return best
	}
	cut2, cut3 := lowest(6, 7), lowest(8, 9, 10)
	for _, wp := range []int64{6, 7, 8, 9, 10} {
		got := byWP("category", wp).Parent
		if cut := wp == cut2 || wp == cut3; cut && got != "" {
			t.Errorf("the category %d, which has the lowest ID of a cycle, has the parent %s", wp, got)
		} else if !cut && got == "" {
			t.Errorf("the category %d, which is not the lowest of a cycle, has no parent", wp)
		}
	}

	file := "site.xml"
	changed := func(tax string, wp int64, msg string) string {
		return who(file, "changed", termAt(t, e, tax, wp)) + msg + target(idOfWP(tax, wp), "/parent")
	}
	wantLines(t, imp.Report, []string{
		changed("category", 4, `the parent "nowhere" is not in the WXR; the term is at the top`),
		changed("category", 5, "the term is its own parent; it is at the top"),
		changed("category", cut2, "the parents make a cycle; the term is at the top"),
		changed("category", cut3, "the parents make a cycle; the term is at the top"),
		changed("post_tag", 13, `the parent "t1" is left out, as post_tag has no hierarchy`),
		changed("keyword", 15, `the parent "k1" is left out, as keyword has no hierarchy`),
		changed("category", 18, `the parent "t1" is not in the WXR; the term is at the top`),
	})
}

func TestTermsMeta(t *testing.T) {
	keys := func(n int) []string {
		var out []string
		for i := 1; i <= n; i++ {
			out = append(out, fmt.Sprintf("k%d", i))
		}
		return out
	}
	in := termsWXR(
		wxrTerm{id: 1, taxonomy: "category", slug: "one", name: "One", meta: []string{"a", "a", "a"}},
		wxrTerm{id: 2, taxonomy: "category", slug: "two", name: "Two", meta: []string{"a", "b", "a", "b"}},
		wxrTerm{id: 3, taxonomy: "category", slug: "ten", name: "Ten", meta: append(keys(10), "k1", "k5")},
		wxrTerm{id: 4, taxonomy: "category", slug: "eleven", name: "Eleven", meta: append(keys(11), "k1")},
		wxrTerm{id: 5, taxonomy: "category", slug: "many", name: "Many", meta: keys(40)},
		wxrTerm{id: 6, taxonomy: "category", slug: "odd", name: "Odd", meta: []string{"a\nb", `a"b`, "お知らせ"}},
		wxrTerm{id: 7, taxonomy: "category", slug: "none", name: "None"},
	)
	imp, terms := makeTerms(t, termSite(), in)
	if len(terms) != 7 {
		t.Errorf("%d terms, want 7", len(terms))
	}
	e := imp.WXR
	const suffix = " not imported; whiteroom terms have no custom fields"
	meta := func(wp int64, msg string) string {
		term := termAt(t, e, "category", wp)
		return who("site.xml", "skipped", term) + msg + suffix + target(idOf(t, imp, term), "")
	}
	ten := `"k1", "k2", "k3", "k4", "k5", "k6", "k7", "k8", "k9", "k10"`
	wantLines(t, imp.Report, []string{
		meta(1, `termmeta "a" is`),
		meta(2, `termmeta "a", "b" are`),
		meta(3, "termmeta "+ten+" are"),
		meta(4, "termmeta "+ten+", and 1 more are"),
		meta(5, "termmeta "+ten+", and 30 more are"),
		meta(6, `termmeta "a\nb", "a\"b", "お知らせ" are`),
	})
}

func TestTermsDescriptions(t *testing.T) {
	complex := strings.Repeat("<div>", 10000) + strings.Repeat("</p>", 10000)
	long := strings.Repeat("a", content.MaxTextBytes+1)
	in := termsWXR(
		wxrTerm{id: 1, taxonomy: "category", slug: "a", name: "A", description: `<p>One</p><p>Two <a href="https://example.com/a">link</a></p>`},
		wxrTerm{id: 2, taxonomy: "category", slug: "b", name: "B", description: "line 1\nline 2"},
		wxrTerm{id: 3, taxonomy: "category", slug: "c", name: "C", description: `<img src="/a.png">`},
		wxrTerm{id: 4, taxonomy: "category", slug: "d", name: "D", description: "  \n "},
		wxrTerm{id: 5, taxonomy: "category", slug: "e", name: "E", description: complex},
		wxrTerm{id: 6, taxonomy: "category", slug: "f", name: "F", description: long},
		wxrTerm{id: 7, taxonomy: "category", slug: "g", name: "G", description: "<b>1</b><i>2</i><u>3</u><s>4</s><em>5</em><strong>6</strong><code>7</code><sub>8</sub><sup>9</sup><small>10</small><big>11</big>"},
		wxrTerm{id: 8, taxonomy: "category", slug: "h", name: "H", description: "&lt;b&gt; &amp; <br>next"},
		wxrTerm{id: 9, taxonomy: "category", slug: "i", name: "I", description: "<p>x</p>\n\n\n\n<p>y</p>"},
	)
	imp, terms := makeTerms(t, termSite(), in)
	if len(terms) != 9 {
		t.Fatalf("%d terms, want 9", len(terms))
	}
	e := imp.WXR
	desc := make(map[string]string)
	for _, term := range terms {
		desc[term.Slug] = term.Description
	}
	for slug, want := range map[string]string{
		"a": "One\n\nTwo link", "b": "line 1\nline 2", "c": "", "d": "", "e": "", "f": "", "g": "1234567891011",
		"h": "<b> &\nnext", "i": "x\n\ny",
	} {
		if got := desc[slug]; got != want {
			t.Errorf("the description of %s = %.60q, want %q", slug, got, want)
		}
	}
	line := func(wp int64, kind Kind, msg string) string {
		term := termAt(t, e, "category", wp)
		return who("site.xml", string(kind), term) + msg + target(idOf(t, imp, term), "/description")
	}
	wantLines(t, imp.Report, []string{
		line(1, Changed, `kept only the text of the HTML; lost a link to "https://example.com/a"`),
		line(3, Changed, `kept only the text of the HTML; lost an image "/a.png"`),
		line(5, Skipped, "the description is not imported: it is too complex to read as HTML"),
		line(6, Skipped, fmt.Sprintf("the description is not imported: it is too long: %d bytes (max %d)", len(long), content.MaxTextBytes)),
		line(7, Changed, "kept only the text of the HTML; lost <b>, <i>, <u>, <s>, <em>, <strong>, <code>, <sub>, <sup>, <small>, and 1 more"),
	})
}

// TestTermsValidationGuard makes a name that is within the limit as it is, and over it once NFC has composed it, so
// that the check of the document leaves the term out, and its child loses the parent.
func TestTermsValidationGuard(t *testing.T) {
	// U+0344 is two bytes, and NFC writes it as U+0308 U+0301, which are four.
	name := strings.Repeat(string(rune(0x344)), content.MaxTextBytes/2)
	in := termsWXR(
		wxrTerm{id: 1, taxonomy: "category", slug: "parent", name: name},
		wxrTerm{id: 2, taxonomy: "category", slug: "child", parent: "parent", name: "Child"},
		wxrTerm{id: 3, taxonomy: "category", slug: "grandchild", parent: "child", name: "Grandchild"},
		wxrTerm{id: 4, taxonomy: "category", slug: "other", name: "Other"},
	)
	imp, terms := makeTerms(t, termSite(), in)
	e := imp.WXR
	var slugs []string
	for _, term := range terms {
		slugs = append(slugs, term.Slug)
	}
	slices.Sort(slugs)
	if want := []string{"child", "grandchild", "other"}; !slices.Equal(slugs, want) {
		t.Fatalf("the terms are %q, want %q", slugs, want)
	}
	child := idOf(t, imp, termAt(t, e, "category", 2))
	for _, term := range terms {
		switch term.Slug {
		case "child":
			if term.Parent != "" {
				t.Errorf("the child has the parent %s", term.Parent)
			}
		case "grandchild":
			if term.Parent != child {
				t.Errorf("the grandchild has the parent %q, want the child %s", term.Parent, child)
			}
		}
	}
	if _, _, ok := imp.IDs.Lookup(TermObject(termAt(t, e, "category", 1))); ok {
		t.Error("the term that failed the check has an ID")
	}

	parent := termAt(t, e, "category", 1)
	wantLines(t, imp.Report, []string{
		who("site.xml", "changed", termAt(t, e, "category", 2)) + `the parent "parent" is not imported; the term is at the top` + target(child, "/parent"),
		who("site.xml", "skipped", parent) + fmt.Sprintf("the term is not imported: /name: too long: %d bytes (max %d)", 2*len(name), content.MaxTextBytes),
	})
}

func TestTermsErrors(t *testing.T) {
	e, rep := readString(t, "site.xml", termsWXR(wxrTerm{id: 1, taxonomy: "category", slug: "a", name: "A"}))
	imp := New("site.xml", e, rep)
	if terms, err := imp.Terms(nil); err == nil || terms != nil {
		t.Errorf("Terms(nil) = %v, %v, want an error", terms, err)
	}
	for _, language := range []string{"", "japanese", "JA", "ja_JP"} {
		site := termSite()
		site.DefaultLanguage = language
		if terms, err := imp.Terms(site); err == nil || terms != nil || !strings.Contains(err.Error(), "default language") {
			t.Errorf("Terms with the default language %q = %v, %v, want an error", language, terms, err)
		}
	}
	wantLines(t, rep, nil)

	// A term that is already imported as another kind is the error of the table.
	if _, err := imp.IDs.Assign(TermObject(&e.Terms[0]), content.RefMenu); err != nil {
		t.Fatal(err)
	}
	if terms, err := imp.Terms(termSite()); err == nil || terms != nil || !strings.Contains(err.Error(), "already imported as menu") {
		t.Errorf("Terms of a term that is a menu = %v, %v, want the error of the table", terms, err)
	}
}

func TestTermsNone(t *testing.T) {
	for _, in := range []string{
		termsWXR(),
		termsWXR(wxrTerm{id: 1, taxonomy: "nav_menu", slug: "main", name: "Main"}, wxrTerm{id: 2, taxonomy: "language", slug: "ja", name: "ja"}),
	} {
		imp, terms := makeTerms(t, termSite(), in)
		if terms != nil || len(reportLines(t, imp.Report)) != 0 {
			t.Errorf("Terms = %v, report %q, want nothing", terms, reportLines(t, imp.Report))
		}
	}
}

// TestTermsValidationOfTheSite checks a site that cannot hold the terms: the terms are left out, with the reason.
func TestTermsValidationOfTheSite(t *testing.T) {
	site := termSite()
	site.Languages = []string{"en"} // the default language is not among them
	imp, terms := makeTerms(t, site, termsWXR(wxrTerm{id: 1, taxonomy: "category", slug: "a", name: "A"}))
	if len(terms) != 0 {
		t.Errorf("%d terms, want none", len(terms))
	}
	wantLines(t, imp.Report, []string{
		who("site.xml", "skipped", &imp.WXR.Terms[0]) + `the term is not imported: /language: language "ja" is not in the site settings (use en)`,
	})
}

// TestTermsAgain checks that the same WXR and site give the same terms and report.
func TestTermsAgain(t *testing.T) {
	var all []wxrTerm
	for i := 1; i <= 40; i++ {
		x := wxrTerm{id: i, taxonomy: "category", slug: fmt.Sprintf("s%d", i), name: fmt.Sprintf("N&amp;%d", i), description: "<b>d</b>"}
		if i%7 == 0 {
			x.slug = fmt.Sprintf("S%d", i-1) // the slug of the term before, in upper case
		}
		if i%3 == 0 {
			x.parent = fmt.Sprintf("s%d", i-1)
		}
		if i%5 == 0 {
			x.meta = []string{"m"}
		}
		all = append(all, x)
	}
	all[37].parent, all[38].parent = all[38].slug, all[37].slug // a cycle
	in := termsWXR(all...)
	imp1, terms1 := makeTerms(t, termSite(), in)
	imp2, terms2 := makeTerms(t, termSite(), in)
	if !slices.Equal(terms1, terms2) {
		t.Errorf("two runs made different terms:\n%+v\n%+v", terms1, terms2)
	}
	if !slices.Equal(imp1.Report.Entries(), imp2.Report.Entries()) {
		t.Error("two runs made different reports")
	}
	if len(terms1) != 40 {
		t.Errorf("%d terms, want 40", len(terms1))
	}
	if n := len(reportLines(t, imp1.Report)); n < 20 {
		t.Errorf("%d lines in the report, want at least 20", n)
	}
}

// TestTermsMany makes the terms of a long chain of parents and of a long cycle of them within the time of a list.
func TestTermsMany(t *testing.T) {
	const n = 5000
	var all []wxrTerm
	for i := 1; i <= n; i++ {
		x := wxrTerm{id: i, taxonomy: "category", slug: fmt.Sprintf("chain-%d", i), name: fmt.Sprintf("Chain %d", i)}
		if i > 1 {
			x.parent = fmt.Sprintf("chain-%d", i-1)
		}
		all = append(all, x)
	}
	for i := 1; i <= n; i++ {
		all = append(all, wxrTerm{id: n + i, taxonomy: "category", slug: fmt.Sprintf("ring-%d", i), parent: fmt.Sprintf("ring-%d", i%n+1), name: fmt.Sprintf("Ring %d", i)})
	}
	imp, terms := makeTerms(t, termSite(), termsWXR(all...))
	if len(terms) != 2*n {
		t.Fatalf("%d terms, want %d", len(terms), 2*n)
	}
	withParent := 0
	for _, term := range terms {
		if term.Parent != "" {
			withParent++
		}
	}
	// All but the first of the chain, and all but the one that the cycle was cut at.
	if withParent != 2*(n-1) {
		t.Errorf("%d terms have a parent, want %d", withParent, 2*(n-1))
	}
	if lines := reportLines(t, imp.Report); len(lines) != 1 || !strings.Contains(lines[0], "the parents make a cycle; the term is at the top") {
		t.Errorf("the report is %q, want one line for the cycle", lines)
	}
}
