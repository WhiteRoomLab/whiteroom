package wpimport

import (
	"context"
	"fmt"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wpblock"
)

// blockParagraphs writes the paragraphs of the texts as the block editor does.
func blockParagraphs(texts ...string) string {
	var parts []string
	for _, s := range texts {
		parts = append(parts, "<!-- wp:paragraph -->\n<p>"+s+"</p>\n<!-- /wp:paragraph -->")
	}
	return strings.Join(parts, "\n\n")
}

const (
	moreTag    = "<!-- wp:more -->\n<!--more-->\n<!-- /wp:more -->"
	bodyBlockA = "<!-- wp:acme/a /-->"
	bodyBlockB = "<!-- wp:acme/b /-->"
)

// countBodies counts the conversions of each item that a block of the content, <!-- wp:acme/count /-->, tells.
func countBodies(t *testing.T) map[int64]int {
	t.Helper()
	counts := make(map[int64]int)
	withConverters(t, map[string]converter{
		"acme/count": func(c *bodyConv, _ string, _ *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
			counts[c.opts.Item.ID]++
			return nil, nil
		},
	})
	return counts
}

func blockTypes(bs content.Blocks) []string {
	var out []string
	for i := range bs {
		out = append(out, string(bs[i].Type))
	}
	return out
}

func TestEntriesBody(t *testing.T) {
	const (
		blocks  = blockBodyContent
		classic = "First line.\n\nSecond <em>paragraph</em>.\n\n<h3>Heading</h3>"
	)
	in := entriesWXR("",
		wxrEntry{id: 1, title: "Blocks", name: "blocks", content: blocks},
		wxrEntry{id: 2, typ: "page", title: "Page", name: "page", content: blocks},
		wxrEntry{id: 3, title: "Classic", name: "classic", content: classic},
		wxrEntry{id: 4, typ: "page", title: "Classic page", name: "classic-page", content: classic},
		wxrEntry{id: 5, typ: "works", title: "Work", name: "work", content: classic},
		wxrEntry{id: 6, title: "Draft", name: "draft", status: "draft", content: blocks},
		wxrEntry{id: 7, title: "Empty", name: "empty"},
		wxrEntry{id: 8, title: "Blank", name: "blank", content: " \n\t\r\n"},
		wxrEntry{id: 9, title: "Private", name: "private", status: "private", content: blocks},
		wxrEntry{id: 10, title: "Styled", name: "styled", content: "<!-- wp:paragraph {\"className\":\"is-style-lead-text\"} -->\n<p class=\"is-style-lead-text\">Lead</p>\n<!-- /wp:paragraph -->"},
	)
	s := entrySettings()
	s.Blocks = emptyBlockSettings()
	s.Blocks.Variants["lead-text"] = "lead"
	imp, _, docs := makeEntries(t, s, nil, in)
	if len(docs) != 10 {
		t.Fatalf("%d entries, want 10", len(docs))
	}

	// What Body gives for the same content, the same key and the same place, on an import that has made nothing else.
	other, site := planEntries(t, s, nil, in)
	for _, d := range docs {
		it := other.WXR.Post(d.Post)
		target := Target{File: "revisions/" + d.RevisionID + ".json", Pointer: "/body"}
		res, err := other.Body(BodyOptions{Site: site, Settings: s, Item: it, Target: target, Key: d.RevisionID})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Blocks) != len(d.Revision.Body) || len(res.Blocks) > 0 && !reflect.DeepEqual(res.Blocks, d.Revision.Body) {
			got, _ := d.Revision.Body.MarshalJSON()
			want, _ := res.Blocks.MarshalJSON()
			t.Errorf("the body of item %d is %s, want %s: what Body gives for the revision ID as the key", d.Post, got, want)
		}
		var got []string
		for _, l := range itemLines(imp.Report, d.Post) {
			if strings.Contains(l, ".json#/body") {
				got = append(got, l)
			}
		}
		if want := itemLines(other.Report, d.Post); !slices.Equal(got, want) {
			t.Errorf("the report of item %d about the body is\n  %s\nwant what Body adds to it\n  %s", d.Post, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
	}

	for _, tt := range []struct {
		post  int64
		types []string
	}{
		{1, []string{"paragraph", "legacy", "heading"}}, {2, []string{"paragraph", "legacy", "heading"}}, {3, []string{"paragraph", "paragraph", "heading"}},
		{4, []string{"paragraph", "paragraph", "heading"}}, {5, []string{"paragraph", "paragraph", "heading"}},
		{6, []string{"paragraph", "legacy", "heading"}}, {7, nil}, {8, nil}, {9, []string{"paragraph", "legacy", "heading"}}, {10, []string{"paragraph"}},
	} {
		d := docOf(docs, tt.post)
		if got := blockTypes(d.Revision.Body); !slices.Equal(got, tt.types) {
			t.Errorf("the body of item %d has the blocks %v, want %v", tt.post, got, tt.types)
		}
		if tt.types == nil && d.Revision.Body != nil {
			t.Errorf("the body of item %d is %#v, want none", tt.post, d.Revision.Body)
		}
		if len(tt.types) > 0 {
			// The key of the IDs of the blocks is the ID of the revision.
			if want := nextIDs(newBlockIDs(d.RevisionID), 1)[0]; d.Revision.Body[0].ID != want {
				t.Errorf("the first block of item %d has the ID %s, want %s, the first of the key %s", tt.post, d.Revision.Body[0].ID, want, d.RevisionID)
			}
		}
	}
	// The place of the records of the blocks is the revision that has them.
	for _, post := range []int64{1, 2, 6} {
		wantItem(t, imp, docs, post, `review: the block "acme/a" is not converted; it is a legacy block -> {R}#/body/1`)
	}
	wantItem(t, imp, docs, 9, `review: the block "acme/a" is not converted; it is a legacy block -> {R}#/body/1`,
		"review: the entry was private; it is a draft here -> {E}#/status")
	for _, post := range []int64{3, 4, 5, 7, 8, 10} {
		wantItem(t, imp, docs, post)
	}
	if got := docOf(docs, 10).Revision.Body[0].Variant; got != "lead" {
		t.Errorf("the variant of the block of item 10 is %q, want lead: the settings are given to Body", got)
	}
	// A draft and a private item have a body as well.
	if d := docOf(docs, 6); d.Entry.Status != content.StatusDraft || len(d.Revision.Body) != 3 {
		t.Errorf("the draft has the status %s and %d blocks", d.Entry.Status, len(d.Revision.Body))
	}
	if d := docOf(docs, 9); d.Entry.Status != content.StatusDraft || len(d.Revision.Body) != 3 {
		t.Errorf("the private item has the status %s and %d blocks", d.Entry.Status, len(d.Revision.Body))
	}
}

const blockBodyContent = "<!-- wp:paragraph -->\n<p>Hello <strong>world</strong></p>\n<!-- /wp:paragraph -->\n\n" + bodyBlockA +
	"\n\n<!-- wp:heading -->\n<h2 class=\"wp-block-heading\">Heading</h2>\n<!-- /wp:heading -->"

func TestEntriesBodyOfTypes(t *testing.T) {
	counts := countBodies(t)
	const text = "<!-- wp:acme/count /-->"
	defs := &content.Site{Types: []content.TypeDef{{Name: "works", Label: "Works", Body: true}, {Name: "events", Label: "Events"}}}
	s := entrySettings()
	s.URLs.Types["events"] = TypeURLs{URL: "/events/{slug}/", Archive: "/events/"}
	in := entriesWXR("",
		wxrEntry{id: 1, typ: "events", title: "With content", name: "with", content: text},
		wxrEntry{id: 2, typ: "events", title: "Without", name: "without"},
		wxrEntry{id: 3, typ: "events", title: "Blank", name: "blank", content: " \n\t"},
		wxrEntry{id: 4, typ: "events", title: "Draft", name: "draft", status: "draft", content: blockParagraphs("Text")},
		wxrEntry{id: 5, typ: "works", title: "Work", name: "work", content: text},
		wxrEntry{id: 6, title: "Post", name: "post", content: text},
	)
	imp, _, docs := makeEntries(t, s, defs, in)
	wantItem(t, imp, docs, 1, "skipped: the content is not imported; the type events has no body -> {R}")
	wantItem(t, imp, docs, 2)
	wantItem(t, imp, docs, 3)
	wantItem(t, imp, docs, 4, "skipped: the content is not imported; the type events has no body -> {R}")
	for _, d := range docs {
		if d.Entry.Type == "events" && d.Revision.Body != nil {
			t.Errorf("the item %d of the type events has the body %v", d.Post, d.Revision.Body)
		}
	}
	// Body is not called for them: it would add to the report.
	if want := map[int64]int{5: 1, 6: 1}; !reflect.DeepEqual(counts, want) {
		t.Errorf("the conversions of the bodies are %v, want %v", counts, want)
	}
}

func TestEntriesBodyIsConvertedOnce(t *testing.T) {
	counts := countBodies(t)
	const text = "<!-- wp:acme/count /-->\n\n<!-- wp:nextpage -->\n<!--nextpage-->\n<!-- /wp:nextpage -->"
	in := entriesWXR("",
		wxrEntry{id: 1, title: "Published", name: "published", content: text},
		wxrEntry{id: 2, title: "Draft", name: "draft", status: "draft", content: text},
		wxrEntry{id: 3, typ: "page", title: "Private", name: "private", status: "private", content: text},
		wxrEntry{id: 4, typ: "works", title: "Future", name: "future", status: "future", date: "2036-10-05 09:45:00", dateGMT: "2036-10-05 00:45:00", content: text},
		wxrEntry{id: 5, title: "Gone", name: "gone", status: "trash", content: text},
		wxrEntry{id: 6, typ: "page", title: "Auto", name: "auto", status: "auto-draft", content: text},
		wxrEntry{id: 7, typ: "product", title: "Mug", name: "mug", content: text},
		wxrEntry{id: 8, title: "No date", name: "no-date", content: text, date: zeroDate, dateGMT: zeroDate, modified: zeroDate, modifiedGMT: zeroDate},
	)
	imp, site := planEntries(t, nil, nil, in)
	var calls []int
	var order []int64
	err := imp.Entries(context.Background(), EntryOptions{Site: site, Settings: entrySettings(), By: mediaTestBy}, func(d *EntryDoc) error {
		total := 0
		for _, n := range counts {
			total += n
		}
		calls = append(calls, total)
		order = append(order, d.Post)
		return nil
	})
	if err != nil {
		t.Fatalf("Entries = %v", err)
	}
	if want := map[int64]int{1: 1, 2: 1, 3: 1, 4: 1}; !reflect.DeepEqual(counts, want) {
		t.Errorf("the conversions of the bodies are %v, want %v: one for each entry, none for the others", counts, want)
	}
	// The body of an entry is made just before the entry is given, not before.
	if want := []int{1, 2, 3, 4}; !slices.Equal(calls, want) || !slices.Equal(order, []int64{1, 2, 3, 4}) {
		t.Errorf("the conversions made when the entries %v were given: %v, want %v", order, calls, want)
	}
	for _, post := range []int64{5, 6, 7, 8} {
		for _, l := range itemLines(imp.Report, post) {
			if strings.Contains(l, "page break") {
				t.Errorf("the report has what the conversion of the body of item %d adds: %s", post, l)
			}
		}
	}
	for _, post := range []int64{1, 2, 3, 4} {
		found := false
		for _, l := range itemLines(imp.Report, post) {
			found = found || strings.Contains(l, "1 page break is removed; whiteroom has no page breaks")
		}
		if !found {
			t.Errorf("the report of item %d has no record of the page break that the conversion of its body found", post)
		}
	}
}

func TestEntriesBodyLinks(t *testing.T) {
	const text = `<!-- wp:paragraph --><p><a href="https://example.com/published/">a</a> <a href="https://example.com/?p=3">b</a> ` +
		`<a href="https://example.com/gone/">c</a> <a href="https://example.com/?p=5">d</a></p><!-- /wp:paragraph -->`
	in := entriesWXR("",
		wxrEntry{id: 1, title: "Links", name: "links", content: text},
		wxrEntry{id: 2, title: "Published", name: "published", link: "https://example.com/published/"},
		wxrEntry{id: 3, title: "Draft", name: "draft", status: "draft", link: "https://example.com/?p=3"},
		wxrEntry{id: 4, title: "Gone", name: "gone", status: "trash", link: "https://example.com/gone/"},
		wxrEntry{id: 5, typ: "page", title: "Private", name: "private", status: "private", link: "https://example.com/?page_id=5"},
	)
	imp, _, docs := makeEntries(t, nil, nil, in)
	var got []content.Link
	for _, x := range docOf(docs, 1).Revision.Body[0].Text {
		for _, m := range x.Marks {
			if m.Link != nil {
				got = append(got, *m.Link)
			}
		}
	}
	id := func(post int64) string { return docOf(docs, post).Entry.ID }
	want := []content.Link{{Entry: id(2)}, {Entry: id(3)}, {Href: "/gone/"}, {Entry: id(5)}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the links are %+v, want a link to the published entry, the draft, a path for the item in the trash, and the private one: %+v", got, want)
	}
	wantItem(t, imp, docs, 1)
}

func TestEntriesBodyExcerpt(t *testing.T) {
	const (
		listAndQuote = "<ul><li>one</li><li>two</li></ul><blockquote><p>Quoted</p><cite>Source</cite></blockquote>" +
			"<table><tr><td>a</td><td>b</td></tr><tr><td>c</td><td>d</td></tr></table>"
		blockOthers = "<!-- wp:paragraph --><p>One</p><!-- /wp:paragraph -->\n" +
			"<!-- wp:code --><pre class=\"wp-block-code\"><code>code text</code></pre><!-- /wp:code -->\n" +
			"<!-- wp:buttons --><div class=\"wp-block-buttons\"><!-- wp:button --><div class=\"wp-block-button\"><a class=\"wp-block-button__link wp-element-button\" href=\"https://example.com/x\">Click</a></div><!-- /wp:button --></div><!-- /wp:buttons -->\n" +
			"<!-- wp:group --><div class=\"wp-block-group\"><!-- wp:paragraph --><p>In group</p><!-- /wp:paragraph --></div><!-- /wp:group -->\n" +
			"<!-- wp:columns --><div class=\"wp-block-columns\"><!-- wp:column --><div class=\"wp-block-column\"><!-- wp:heading --><h2 class=\"wp-block-heading\">In column</h2><!-- /wp:heading --></div><!-- /wp:column --></div><!-- /wp:columns -->\n"
		blockList = "<!-- wp:list --><ul><!-- wp:list-item --><li>one</li><!-- /wp:list-item --><!-- wp:list-item --><li>two</li><!-- /wp:list-item --></ul><!-- /wp:list -->\n" +
			"<!-- wp:quote --><blockquote class=\"wp-block-quote\"><!-- wp:paragraph --><p>Quoted</p><!-- /wp:paragraph --><cite>Source</cite></blockquote><!-- /wp:quote -->\n" +
			"<!-- wp:table --><figure class=\"wp-block-table\"><table><tbody><tr><td>a</td><td>b</td></tr><tr><td>c</td><td>d</td></tr></tbody></table></figure><!-- /wp:table -->\n"
	)
	for _, tt := range []struct {
		name, content, excerpt string
		want                   string
		blocks                 int // the blocks of the body
		lines                  []string
	}{
		{name: "the classic editor", content: "First paragraph.\n\nSecond one.\n<!--more-->\nAfter", want: "First paragraph.\nSecond one.", blocks: 3},
		{name: "the classic editor, text of the link", content: "<p>One</p><!--more Read on-->\n<p>Two</p>", want: "One", blocks: 2,
			lines: []string{`skipped: the text "Read on" of the more link is not imported -> {R}#/body`}},
		{name: "the block editor", content: blockParagraphs("Lead") + "\n\n" + moreTag + "\n\n" + blockParagraphs("Rest"), want: "Lead", blocks: 2},
		{name: "the first blocks only", content: blockParagraphs("One", "Two", "Three") + "\n\n" + moreTag + "\n\n" + blockParagraphs("Four"), want: "One\nTwo\nThree", blocks: 4},
		{name: "no read more", content: blockParagraphs("One", "Two"), want: "", blocks: 2},
		{name: "read more at the start", content: moreTag + "\n\n" + blockParagraphs("One"), want: "", blocks: 1},
		{name: "classic read more at the start", content: "<!--more-->\n<p>Two</p>", want: "", blocks: 1},
		{
			name: "only a block without text before it", blocks: 2,
			content: "<!-- wp:separator -->\n<hr class=\"wp-block-separator\"/>\n<!-- /wp:separator -->\n" + moreTag + "\n" + blockParagraphs("After"),
		},
		{
			name: "only an image before it", blocks: 2,
			lines:   []string{"review: the image is not an imported attachment; it is a legacy block -> {R}#/body/0"},
			content: "<!-- wp:image {\"id\":99} -->\n<figure class=\"wp-block-image\"><img src=\"https://example.com/a.png\" alt=\"a\"/></figure>\n<!-- /wp:image -->\n" + moreTag + "\n" + blockParagraphs("After"),
		},
		{name: "a hand-written excerpt", content: blockParagraphs("One") + "\n" + moreTag + "\n" + blockParagraphs("Two"), excerpt: "By hand", want: "By hand", blocks: 2},
		{name: "a hand-written excerpt that is only space", content: blockParagraphs("One") + "\n" + moreTag + "\n" + blockParagraphs("Two"), excerpt: " \n", want: "One", blocks: 2},
		{
			name: "a hand-written excerpt that is not imported", blocks: 2, want: "One",
			content: blockParagraphs("One") + "\n" + moreTag + "\n" + blockParagraphs("Two"), excerpt: strings.Repeat("a", content.MaxTextBytes+1),
			lines: []string{"skipped: the excerpt is not imported: it is too long: 1048577 bytes (max 1048576) -> {R}#/excerpt"},
		},
		{name: "lists, quotes and tables, classic", content: listAndQuote + "<!--more-->\n<p>x</p>", want: "one\ntwo\nQuoted\nSource\na\nb\nc\nd", blocks: 4},
		{name: "lists, quotes and tables, blocks", content: blockList + moreTag + "\n" + blockParagraphs("x"), want: "one\ntwo\nQuoted\nSource\na\nb\nc\nd", blocks: 4},
		{name: "line breaks", content: "<p>Line one<br>Line two</p><!--more--><p>x</p>", want: "Line one\nLine two", blocks: 2},
		{
			name: "only the characters", blocks: 2, want: "A bold link and code",
			content: `<p>A <strong>bold</strong> <a href="https://example.com/x">link</a> and <code>code</code></p><!--more--><p>x</p>`,
		},
		{name: "blocks that are not text", content: blockOthers + moreTag + "\n" + blockParagraphs("After"), want: "One\nIn group\nIn column", blocks: 6},
		{name: "a paragraph of space", content: "<p>One</p><p>&nbsp;</p><p>Two</p><!--more--><p>x</p>", want: "One\nTwo", blocks: 4},
		{name: "normalized", content: blockParagraphs("e"+string(rune(0x301))) + "\n" + moreTag + "\n" + blockParagraphs("x"), want: string(rune(0xE9)), blocks: 2},
		{name: "a second read more", content: blockParagraphs("One") + moreTag + blockParagraphs("Two") + moreTag + blockParagraphs("Three"), want: "One", blocks: 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			item := wxrEntry{id: 5, title: "Title", name: "slug", content: tt.content, excerpt: tt.excerpt}
			imp, _, docs := makeEntries(t, nil, nil, entriesWXR("", item))
			if len(docs) != 1 {
				t.Fatalf("%d entries, want 1", len(docs))
			}
			rev := docs[0].Revision
			if rev.Excerpt != tt.want {
				t.Errorf("excerpt = %q, want %q", rev.Excerpt, tt.want)
			}
			if len(rev.Body) != tt.blocks {
				t.Errorf("the body has %d blocks (%v), want %d", len(rev.Body), blockTypes(rev.Body), tt.blocks)
			}
			wantItem(t, imp, docs, 5, tt.lines...)
		})
	}
}

func TestEntriesBodyExcerptOfAnyType(t *testing.T) {
	text := blockParagraphs("One") + "\n" + moreTag + "\n" + blockParagraphs("Two")
	in := entriesWXR("",
		wxrEntry{id: 1, typ: "page", title: "Page", name: "page", content: text},
		wxrEntry{id: 2, typ: "works", title: "Work", name: "work", content: text},
		wxrEntry{id: 3, title: "Draft", name: "draft", status: "draft", content: text},
	)
	_, _, docs := makeEntries(t, nil, nil, in)
	for _, d := range docs {
		if d.Revision.Excerpt != "One" {
			t.Errorf("the excerpt of item %d is %q, want One", d.Post, d.Revision.Excerpt)
		}
	}
}

func TestEntriesBodyExcerptLength(t *testing.T) {
	paragraph := func(n int) content.Block {
		return content.Block{Type: content.BlockParagraph, Text: content.RichText{rtText(strings.Repeat("a", n))}}
	}
	for _, tt := range []struct {
		name     string
		a, b     int
		wantSize int // 0 if the excerpt is too long
	}{
		{"the longest", content.MaxTextBytes/2 - 1, content.MaxTextBytes / 2, content.MaxTextBytes},
		{"one byte too long", content.MaxTextBytes / 2, content.MaxTextBytes / 2, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			imp, c, ei := newTidy(t, entriesWXR("", wxrEntry{id: 5, title: "T", name: "t"}))
			made := c.setMoreExcerpt(ei, content.Blocks{paragraph(tt.a), paragraph(tt.b)})
			if made != (tt.wantSize > 0) || len(ei.rev.Excerpt) != tt.wantSize {
				t.Errorf("setMoreExcerpt = %v, with an excerpt of %d bytes, want %d", made, len(ei.rev.Excerpt), tt.wantSize)
			}
			var want []string
			if tt.wantSize == 0 {
				want = []string{"skipped: the excerpt before the read more tag is not imported: it is too long: 1048577 bytes (max 1048576) -> revisions/" + ei.revID + ".json#/excerpt"}
			}
			if got := itemLines(imp.Report, 5); !slices.Equal(got, want) {
				t.Errorf("the report is %q, want %q", got, want)
			}
		})
	}
	t.Run("too long in a whole entry", func(t *testing.T) {
		long := strings.Repeat("a", 525000)
		in := entriesWXR("", wxrEntry{id: 5, title: "T", name: "t", content: blockParagraphs(long, long) + "\n" + moreTag + "\n" + blockParagraphs("After")})
		imp, _, docs := makeEntries(t, nil, nil, in)
		if docs[0].Revision.Excerpt != "" || len(docs[0].Revision.Body) != 3 {
			t.Errorf("the entry has an excerpt of %d bytes and %d blocks, want none and 3", len(docs[0].Revision.Excerpt), len(docs[0].Revision.Body))
		}
		wantItem(t, imp, docs, 5, "skipped: the excerpt before the read more tag is not imported: it is too long: 1050001 bytes (max 1048576) -> {R}#/excerpt")
	})
}

func TestEntriesBodyExcerptBlocks(t *testing.T) {
	text := func(s string) content.RichText { return content.RichText{rtText(s)} }
	para := func(s string) content.Block { return content.Block{Type: content.BlockParagraph, Text: text(s)} }
	blocks := content.Blocks{
		{Type: content.BlockHeading, Text: text("Heading"), Attrs: content.Attrs{Level: 2}},
		{Type: content.BlockImage, Attrs: content.Attrs{Media: "med_x", Caption: text("Caption")}},
		{Type: content.BlockCode, Code: "code"},
		{Type: content.BlockHTML, Attrs: content.Attrs{HTML: "<b>html</b>"}},
		{Type: content.BlockLegacy, Attrs: content.Attrs{HTML: "<b>legacy</b>"}},
		{Type: content.BlockEmbed, Attrs: content.Attrs{URL: "https://example.com/", Caption: text("Embed caption")}},
		{Type: content.BlockButtons, Children: content.Blocks{{Type: content.BlockButton, Attrs: content.Attrs{Label: "Button"}}}},
		{Type: content.BlockFile, Attrs: content.Attrs{Label: "File"}},
		{Type: content.BlockForm, Attrs: content.Attrs{Form: "frm_x"}},
		{Type: content.BlockSeparator},
		{Type: content.BlockSpacer},
		{Type: content.BlockTable, Attrs: content.Attrs{Caption: text("Table caption")}, Children: content.Blocks{
			{Type: content.BlockTableRow, Children: content.Blocks{{Type: content.BlockTableCell, Text: text("Cell")}}},
		}},
		{Type: content.BlockQuote, Children: content.Blocks{para("In quote"), {Type: content.BlockQuote, Children: content.Blocks{para("Nested")}, Attrs: content.Attrs{Citation: text("Inner")}}}, Attrs: content.Attrs{Citation: text("Outer")}},
		{Type: content.BlockGroup, Children: content.Blocks{{Type: content.BlockList, Children: content.Blocks{{Type: content.BlockListItem, Children: content.Blocks{para("Item")}}}}}},
		{Type: content.BlockColumns, Children: content.Blocks{{Type: content.BlockColumn, Children: content.Blocks{para("Column")}}}},
		para(" "),
		{Type: content.BlockParagraph, Text: content.RichText{rtBreak(), rtText("Break "), rtBreak(), rtBreak(), rtText("after")}},
		para("After"),
	}
	imp, c, ei := newTidy(t, entriesWXR("", wxrEntry{id: 5, title: "T", name: "t"}))
	if !c.setMoreExcerpt(ei, blocks) {
		t.Fatal("setMoreExcerpt made no excerpt")
	}
	// The break at the start of a text makes an empty line, and the space at the end of a line goes.
	want := "Heading\nCell\nIn quote\nNested\nInner\nOuter\nItem\nColumn\n\nBreak\n\nafter\nAfter"
	if ei.rev.Excerpt != want {
		t.Errorf("the excerpt is %q, want %q", ei.rev.Excerpt, want)
	}
	if got := itemLines(imp.Report, 5); len(got) != 0 {
		t.Errorf("the report is %q, want it empty", got)
	}
	// Nothing, if there is no text in the blocks.
	ei.rev.Excerpt = ""
	if c.setMoreExcerpt(ei, content.Blocks{{Type: content.BlockSeparator}, para(" ")}) || ei.rev.Excerpt != "" {
		t.Errorf("setMoreExcerpt made %q of blocks without text", ei.rev.Excerpt)
	}
	if c.setMoreExcerpt(ei, nil) {
		t.Error("setMoreExcerpt made an excerpt of no blocks")
	}
	// The characters that normalization takes out leave nothing: there is no excerpt.
	if c.setMoreExcerpt(ei, content.Blocks{para(string(rune(7)))}) || ei.rev.Excerpt != "" {
		t.Errorf("setMoreExcerpt made %q of a text that is a control character", ei.rev.Excerpt)
	}
}

// sizeEntry is an item with a body of four blocks: two paragraphs of text, which make most of the JSON, and two legacy
// blocks that have a record each, and a page break, which has one for the body as a whole.
func sizeEntry() wxrEntry {
	big := strings.Repeat("a", 10000)
	text := blockParagraphs(big) + "\n\n" + bodyBlockA + "\n\n" + blockParagraphs(big) + "\n\n" + bodyBlockB +
		"\n\n<!-- wp:nextpage -->\n<!--nextpage-->\n<!-- /wp:nextpage -->"
	return wxrEntry{id: 5, title: "Large", name: "large", content: text}
}

// TestEntriesBodyOfNothing checks a body that Body leaves empty, as a slice that is not nil: the revision has none.
func TestEntriesBodyOfNothing(t *testing.T) {
	withConverters(t, map[string]converter{
		"acme/huge": func(c *bodyConv, _ string, _ *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
			g := c.newBlock(content.BlockGroup)
			for range content.MaxBlocks + 1 {
				g.Children = append(g.Children, c.newBlock(content.BlockSeparator))
			}
			return []content.Block{g}, nil
		},
	})
	imp, _, docs := makeEntries(t, nil, nil, entriesWXR("", wxrEntry{id: 5, title: "T", name: "t", content: "<!-- wp:acme/huge /-->"}))
	if docs[0].Revision.Body != nil {
		t.Errorf("the body is %d blocks that are not nil, want none", len(docs[0].Revision.Body))
	}
	wantItem(t, imp, docs, 5, "skipped: the blocks from /body/0 on are not imported: a document has at most 10000 blocks -> {R}#/body")
}

func TestEntriesBodyLimit(t *testing.T) {
	// The bounds of the whole body, found from the revision that fits.
	_, _, docs := makeEntries(t, nil, nil, entriesWXR("", sizeEntry()))
	rev := docs[0].Revision
	if got := blockTypes(rev.Body); !slices.Equal(got, []string{"paragraph", "legacy", "paragraph", "legacy"}) {
		t.Fatalf("the body has the blocks %v", got)
	}
	rest, bounds := revisionBound(&rev)
	sum := func(n int) int {
		total := rest
		for _, b := range bounds[:n] {
			total += b
		}
		return total
	}
	const (
		legacyA = `review: the block "acme/a" is not converted; it is a legacy block -> {R}#/body/1`
		legacyB = `review: the block "acme/b" is not converted; it is a legacy block -> {R}#/body/3`
		pages   = "skipped: 1 page break is removed; whiteroom has no page breaks -> {R}#/body"
	)
	for _, tt := range []struct {
		name  string
		limit int
		keep  int
		lines []string
	}{
		{"it all fits", sum(4), 4, []string{legacyA, legacyB, pages}},
		{"the last block does not", sum(4) - 1, 3, []string{legacyA, pages}},
		{"the last block and the one before", sum(3) - 1, 2, []string{legacyA, pages}},
		{"the second block does not fit", sum(2) - 1, 1, []string{pages}},
		{"the first block does not fit", sum(1) - 1, 0, []string{pages}},
		{"nothing fits", 0, 0, []string{pages}},
		{"the first fits and the second does not", sum(1), 1, []string{pages}},
		{"two fit", sum(2), 2, []string{legacyA, pages}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			lowerLimit(t, &maxRevisionBytes, tt.limit)
			imp, _, docs := makeEntries(t, nil, nil, entriesWXR("", sizeEntry()))
			got := docs[0].Revision
			if len(got.Body) != tt.keep {
				t.Errorf("the body has %d blocks (%v), want %d", len(got.Body), blockTypes(got.Body), tt.keep)
			}
			want := tt.lines
			if tt.keep < 4 {
				want = append(slices.Clone(want), fmt.Sprintf("skipped: the blocks from /body/%d on are not imported: the revision would be larger than %d bytes as JSON -> {R}#/body", tt.keep, tt.limit))
			}
			wantItem(t, imp, docs, 5, want...)
			if tt.keep == 0 && got.Body != nil {
				t.Errorf("the body is %#v, want none", got.Body)
			}
			if tt.keep > 0 {
				data, err := got.MarshalJSON()
				if err != nil {
					t.Fatal(err)
				}
				if len(data) > tt.limit {
					t.Errorf("the JSON of the revision has %d bytes, the limit is %d", len(data), tt.limit)
				}
			}
			if status := docs[0].Entry.Status; status != content.StatusPublished {
				t.Errorf("the status is %s: the size of the body does not change it", status)
			}
		})
	}
}

func TestEntriesBodyLimitOtherItems(t *testing.T) {
	_, _, docs := makeEntries(t, nil, nil, entriesWXR("", sizeEntry()))
	rest, bounds := revisionBound(&docs[0].Revision)
	lowerLimit(t, &maxRevisionBytes, rest+bounds[0]+bounds[1]-1)
	small := func(id int) wxrEntry {
		return wxrEntry{id: id, title: "Small", name: fmt.Sprintf("small-%d", id), content: blockParagraphs("Small") + "\n" + bodyBlockA}
	}
	large := sizeEntry()
	imp, _, docs := makeEntries(t, nil, nil, entriesWXR("", small(4), large, small(6)))
	const legacyA = `review: the block "acme/a" is not converted; it is a legacy block -> {R}#/body/1`
	for _, post := range []int64{4, 6} {
		wantItem(t, imp, docs, post, legacyA)
		if got := docOf(docs, post).Revision.Body; len(got) != 2 {
			t.Errorf("the body of item %d has %d blocks, want 2", post, len(got))
		}
	}
	wantItem(t, imp, docs, 5, "skipped: 1 page break is removed; whiteroom has no page breaks -> {R}#/body",
		fmt.Sprintf("skipped: the blocks from /body/1 on are not imported: the revision would be larger than %d bytes as JSON -> {R}#/body", maxRevisionBytes))
	if got := docOf(docs, 5).Revision.Body; len(got) != 1 {
		t.Errorf("the body of the large item has %d blocks, want 1", len(got))
	}
}

func TestEntriesBodyLimitKeepsTheExcerpt(t *testing.T) {
	in := entriesWXR("", wxrEntry{id: 5, title: "T", name: "t", content: blockParagraphs("Lead") + "\n" + moreTag + "\n" + blockParagraphs(strings.Repeat("a", 10000))})
	_, _, docs := makeEntries(t, nil, nil, in)
	rest, bounds := revisionBound(&docs[0].Revision)
	lowerLimit(t, &maxRevisionBytes, rest+bounds[0])
	_, _, docs = makeEntries(t, nil, nil, in)
	if got := docs[0].Revision; len(got.Body) != 1 || got.Excerpt != "Lead" {
		t.Errorf("the revision has %d blocks and the excerpt %q, want 1 and Lead", len(got.Body), got.Excerpt)
	}
}

// TestEntriesBodyLimitWithoutWriting runs the limit of the revision as it is, on a paragraph whose JSON is about 9 GiB,
// which it must not write.
func TestEntriesBodyLimitWithoutWriting(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("the JSON of the paragraph is more than an int holds")
	}
	imp, c, ei := newTidy(t, entriesWXR("", wxrEntry{id: 5, title: "T", name: "t"}))
	small := content.Block{Type: content.BlockParagraph, ID: "b_a", Text: content.RichText{rtText("small")}}
	ei.rev = content.Revision{Title: "T", Slug: "t", Body: content.Blocks{small, bigParagraph(24000, linkMark(strings.Repeat("a", 200000))), small}}
	reports := imp.Report.count()

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	c.fitBody(ei, reports)
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)

	if len(ei.rev.Body) != 1 || ei.rev.Body[0].ID != "b_a" {
		t.Errorf("the body has %d blocks, want the first one", len(ei.rev.Body))
	}
	want := fmt.Sprintf("skipped: the blocks from /body/1 on are not imported: the revision would be larger than %d bytes as JSON -> revisions/%s.json#/body", content.MaxDocumentBytes, ei.revID)
	if got := itemLines(imp.Report, 5); !slices.Equal(got, []string{want}) {
		t.Errorf("the report is %q, want %q", got, want)
	}
	if elapsed > time.Second {
		t.Errorf("the limit took %v, want much less than a second", elapsed)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 16<<20 {
		t.Errorf("the limit allocated %d bytes, want at most 16 MiB: it must not write the JSON", alloc)
	}
}

// withValidateRevision replaces the validation of a revision with its body until the end of the test.
func withValidateRevision(t *testing.T, f func(content.Revision, content.RevisionOptions) error) {
	t.Helper()
	old := validateRevision
	validateRevision = f
	t.Cleanup(func() { validateRevision = old })
}

func TestEntriesBodyGuard(t *testing.T) {
	const bad = "/body/0: bad block"
	// failsWithABody fails the check of a revision that has a body, if its title is the title given or any title is.
	failsWithABody := func(calls *int, title string) func(content.Revision, content.RevisionOptions) error {
		return func(r content.Revision, opts content.RevisionOptions) error {
			*calls++
			if len(r.Body) > 0 && (title == "" || r.Title == title) {
				return content.Errors{{Path: "/body/0", Msg: "bad block"}, {Path: "/body/1", Msg: "worse"}}
			}
			return r.Validate(opts)
		}
	}
	withMore := blockParagraphs("One") + "\n" + bodyBlockA + "\n" + moreTag + "\n" + blockParagraphs("Two")
	const legacyA = `review: the block "acme/a" is not converted; it is a legacy block -> {R}#/body/1`

	t.Run("the body and the excerpt made of it are not imported", func(t *testing.T) {
		calls := 0
		withValidateRevision(t, failsWithABody(&calls, "Bad"))
		imp, _, docs := makeEntries(t, nil, nil, entriesWXR("",
			wxrEntry{id: 4, title: "Good", name: "good", content: withMore},
			wxrEntry{id: 5, title: "Bad", name: "bad", content: withMore},
			wxrEntry{id: 6, title: "No body", name: "no-body"},
		))
		if got := docOf(docs, 4).Revision; len(got.Body) != 3 || got.Excerpt != "One" {
			t.Errorf("the revision of the item that passes has %d blocks and the excerpt %q, want 3 and One", len(got.Body), got.Excerpt)
		}
		got := docOf(docs, 5)
		if got.Revision.Body != nil || got.Revision.Excerpt != "" {
			t.Errorf("the revision has the body %v and the excerpt %q, want neither", got.Revision.Body, got.Revision.Excerpt)
		}
		if got.Entry.Status != content.StatusPublished || got.Revision.Title != "Bad" {
			t.Errorf("the entry is %s with the title %q: the rest is as it was", got.Entry.Status, got.Revision.Title)
		}
		// What the conversion noted of the blocks of the entry goes with them, and what it noted of the others stays.
		wantItem(t, imp, docs, 4, legacyA)
		wantItem(t, imp, docs, 5, "skipped: the content is not imported: it does not pass validation: "+bad+" -> {R}#/body")
		wantItem(t, imp, docs, 6)
		// The revision that fails is checked again without the body; each of the others once.
		if calls != 4 {
			t.Errorf("the revisions were checked %d times, want 4", calls)
		}
	})

	t.Run("an excerpt that was written is kept", func(t *testing.T) {
		calls := 0
		withValidateRevision(t, failsWithABody(&calls, ""))
		imp, _, docs := makeEntries(t, nil, nil, entriesWXR("", wxrEntry{id: 5, title: "T", name: "t", content: withMore, excerpt: "By hand"}))
		if got := docs[0].Revision; got.Body != nil || got.Excerpt != "By hand" {
			t.Errorf("the revision has the body %v and the excerpt %q, want the excerpt only", got.Body, got.Excerpt)
		}
		wantItem(t, imp, docs, 5, "skipped: the content is not imported: it does not pass validation: "+bad+" -> {R}#/body")
	})

	t.Run("an excerpt of a body that was cut", func(t *testing.T) {
		calls := 0
		withValidateRevision(t, failsWithABody(&calls, ""))
		lowerLimit(t, &maxRevisionBytes, 0)
		_, _, docs := makeEntries(t, nil, nil, entriesWXR("", wxrEntry{id: 5, title: "T", name: "t", content: withMore}))
		// Nothing was left of the body, but the excerpt that was made of it: the check passes, with it.
		if got := docs[0].Revision; got.Body != nil || got.Excerpt != "One" {
			t.Errorf("the revision has the body %v and the excerpt %q", got.Body, got.Excerpt)
		}
		if calls != 1 {
			t.Errorf("the revision was checked %d times, want 1", calls)
		}
	})

	t.Run("an excerpt that does not pass", func(t *testing.T) {
		calls := 0
		withValidateRevision(t, func(r content.Revision, opts content.RevisionOptions) error {
			calls++
			if r.Excerpt != "" {
				return content.Errors{{Path: "/excerpt", Msg: "bad excerpt"}}
			}
			return r.Validate(opts)
		})
		lowerLimit(t, &maxRevisionBytes, 0)
		imp, _, docs := makeEntries(t, nil, nil, entriesWXR("", wxrEntry{id: 5, title: "T", name: "t", content: withMore}))
		// The body was cut, and the excerpt of the text before the read more tag is not imported with it.
		if got := docs[0].Revision; got.Body != nil || got.Excerpt != "" {
			t.Errorf("the revision has the body %v and the excerpt %q, want neither", got.Body, got.Excerpt)
		}
		if calls != 2 {
			t.Errorf("the revision was checked %d times, want 2", calls)
		}
		wantItem(t, imp, docs, 5,
			"skipped: the content is not imported: it does not pass validation: /excerpt: bad excerpt -> {R}#/body",
			"skipped: the blocks from /body/0 on are not imported: the revision would be larger than 0 bytes as JSON -> {R}#/body")
	})

	t.Run("a revision that cannot pass", func(t *testing.T) {
		for _, tt := range []struct {
			name, content string
			calls         int
		}{
			{"with a body", withMore, 2},
			{"without a body", "", 1},
		} {
			t.Run(tt.name, func(t *testing.T) {
				calls := 0
				withValidateRevision(t, func(content.Revision, content.RevisionOptions) error {
					calls++
					return content.Errors{{Path: "/title", Msg: "broken"}}
				})
				imp, site := planEntries(t, nil, nil, entriesWXR("", wxrEntry{id: 5, title: "T", name: "t", content: tt.content}))
				docs, err := collectEntries(context.Background(), imp, nil, site)
				if err == nil || err.Error() != "the entry of item 5 does not pass validation: /title: broken" {
					t.Errorf("Entries = %v", err)
				}
				if len(docs) != 0 {
					t.Errorf("%d entries were given", len(docs))
				}
				if calls != tt.calls {
					t.Errorf("the revision was checked %d times, want %d", calls, tt.calls)
				}
			})
		}
	})

	t.Run("an entry that does not pass is not given a body", func(t *testing.T) {
		counts := countBodies(t)
		imp, c, ei := newTidy(t, entriesWXR("", wxrEntry{id: 5, title: "T", name: "t", content: "<!-- wp:acme/count /-->"}))
		c.convert(ei)
		c.tidy(ei)
		ei.entry.Type = "ghost"
		err := c.emitAll(context.Background(), []*entryItem{ei}, func(*EntryDoc) error { t.Error("emit was called"); return nil })
		if err == nil || !strings.HasPrefix(err.Error(), "the entry of item 5 does not pass validation: ") {
			t.Errorf("emitAll = %v", err)
		}
		if len(counts) != 0 || len(itemLines(imp.Report, 5)) != 0 {
			t.Errorf("the body was converted %v times, the report has %q", counts, itemLines(imp.Report, 5))
		}
	})
}

// waitReleased waits for the cleanup that closes done, which the garbage collector runs some time after what it is for
// cannot be reached.
func waitReleased(t *testing.T, what string, done <-chan struct{}) {
	t.Helper()
	for range 100 {
		runtime.GC()
		select {
		case <-done:
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Errorf("%s is still held", what)
}

// TestEntriesBodyIsNotKept checks that Entries does not hold on to what it gave: the body of an entry cannot be reached
// after emit returns for it, and is not made until its turn.
func TestEntriesBodyIsNotKept(t *testing.T) {
	counts := countBodies(t)
	const n = 6
	var items []wxrEntry
	for i := 1; i <= n; i++ {
		items = append(items, wxrEntry{
			id: i, title: fmt.Sprintf("Post %d", i), name: fmt.Sprintf("post-%d", i),
			content: "<!-- wp:acme/count /-->\n\n" + blockParagraphs(fmt.Sprintf("Text %d", i), "More text") + "\n\n" + bodyBlockA,
		})
	}
	imp, site := planEntries(t, nil, nil, entriesWXR("", items...))
	released := make([]chan struct{}, n)
	for i := range released {
		released[i] = make(chan struct{})
	}
	given := 0
	err := imp.Entries(context.Background(), EntryOptions{Site: site, Settings: entrySettings(), By: mediaTestBy}, func(d *EntryDoc) error {
		k := given
		given++
		if total := len(counts); total != k+1 {
			t.Errorf("the bodies of %d items were converted when the entry %d was given, want %d", total, k, k+1)
		}
		if len(d.Revision.Body) < 3 {
			t.Errorf("the entry %d has %d blocks", k, len(d.Revision.Body))
			return nil
		}
		if k > 0 {
			waitReleased(t, fmt.Sprintf("the body of the entry %d", k-1), released[k-1])
		}
		runtime.AddCleanup(&d.Revision.Body[0], func(done chan struct{}) { close(done) }, released[k])
		return nil
	})
	if err != nil {
		t.Fatalf("Entries = %v", err)
	}
	if given != n {
		t.Fatalf("%d entries were given, want %d", given, n)
	}
	waitReleased(t, "the body of the last entry", released[n-1])
	runtime.KeepAlive(imp)
}

func TestEntriesBodySameOutput(t *testing.T) {
	in := entriesWXR("",
		wxrEntry{id: 1, title: "One", name: "one", content: blockBodyContent},
		wxrEntry{id: 2, typ: "page", title: "Two", name: "two", content: "Classic <b>text</b>.\n\nMore.\n<!--more-->\nEnd"},
		wxrEntry{id: 3, typ: "events", title: "Three", name: "three", content: "Not imported"},
	)
	defs := &content.Site{Types: []content.TypeDef{{Name: "works", Label: "Works", Body: true}, {Name: "events", Label: "Events"}}}
	s := entrySettings()
	s.URLs.Types["events"] = TypeURLs{URL: "/events/{slug}/", Archive: "/events/"}
	var texts []string
	for range 2 {
		imp, _, docs := makeEntries(t, s, defs, in)
		texts = append(texts, entriesText(t, imp, docs))
	}
	if texts[0] != texts[1] {
		t.Errorf("two conversions of the same WXR differ:\n%s\n%s", texts[0], texts[1])
	}
	if !strings.Contains(texts[0], "Classic") || !strings.Contains(texts[0], "the type events has no body") {
		t.Errorf("the output has no body or no record of the type without one: %s", texts[0])
	}
}
