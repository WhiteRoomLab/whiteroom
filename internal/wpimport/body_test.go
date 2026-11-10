package wpimport

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wpblock"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

const bodyKey = "rev_0000000000ABCDEFGHJKMNPQRS"

var (
	bodyTarget = Target{File: "revisions/rev_0000000000ABCDEFGHJKMNPQRS.json", Pointer: "/body"}
	// bodyIDs are the first block IDs of bodyKey: the IDs of the blocks in the order that the conversion makes them.
	bodyIDs = nextIDs(newBlockIDs(bodyKey), 64)
)

const (
	moreBlock  = "<!-- wp:more -->\n<!--more-->\n<!-- /wp:more -->"
	pageBreak  = "<!-- wp:nextpage -->\n<!--nextpage-->\n<!-- /wp:nextpage -->"
	blockA     = "<!-- wp:acme/a /-->"
	blockB     = "<!-- wp:acme/b /-->"
	blockC     = "<!-- wp:acme/c /-->"
	groupOpen  = "<!-- wp:test/group -->"
	groupClose = "<!-- /wp:test/group -->"
	imageBlock = `<!-- wp:test/image {"id":5} /-->`

	deepReason = "the blocks are nested more than 32 deep"
)

func bodyItem(text string) *wxr.Item {
	return &wxr.Item{Pos: wxr.Pos{Line: 12, Column: 3}, ID: 101, Type: "post", Title: "Hello", Content: text}
}

func bodySite(allowHTML bool) *content.Site {
	return &content.Site{Languages: []string{"ja"}, DefaultLanguage: "ja", AllowHTML: allowHTML}
}

func bodyImport() *Import {
	return New("site.xml", &wxr.Export{}, NewReport(Info{File: "site.xml"}))
}

// bodyLine writes the line of the report for short, "kind: message -> pointer", of the item that the tests convert.
func bodyLine(short string) string {
	kind, rest, _ := strings.Cut(short, ": ")
	i := strings.LastIndex(rest, " -> ")
	msg, pointer := rest[:i], rest[i+len(" -> "):]
	return fmt.Sprintf("site.xml:12:3: %s: item 101 (post %q): %s -> %s#%s", kind, "Hello", msg, bodyTarget.File, pointer)
}

func bodyLines(shorts ...string) []string {
	var lines []string
	for _, s := range shorts {
		lines = append(lines, bodyLine(s))
	}
	return lines
}

func legacyBlock(id, html, reason string) content.Block {
	return content.Block{Type: content.BlockLegacy, ID: id, Attrs: content.Attrs{HTML: html, Reason: reason}}
}

// paragraphBlock is a paragraph of the text, which has no marks.
func paragraphBlock(id, text string) content.Block {
	return content.Block{Type: content.BlockParagraph, ID: id, Text: content.RichText{rtText(text)}}
}

// legacyLine is the short line for the review of a legacy block with the reason, at the pointer.
func legacyLine(reason, pointer string) string {
	return "review: " + reason + "; it is a legacy block -> " + pointer
}

func notConverted(name string) string { return fmt.Sprintf("the block %q is not converted", name) }

// convertBody converts text as the content of the item that bodyItem makes, and checks what holds for every result.
func convertBody(t *testing.T, site *content.Site, text string) (*BodyResult, *Import) {
	t.Helper()
	imp := bodyImport()
	res, err := imp.Body(BodyOptions{Site: site, Item: bodyItem(text), Target: bodyTarget, Key: bodyKey})
	if err != nil {
		t.Fatalf("Body = %v", err)
	}
	checkBody(t, imp, site, res)
	checkNoPieces(t, text, imp, res)
	return res, imp
}

// checkBody checks what holds for the result of any content: the blocks pass Validate as those of an import, they write
// and read back as the same JSON, their IDs are all different, and More is among the blocks.
func checkBody(t *testing.T, imp *Import, site *content.Site, res *BodyResult) {
	t.Helper()
	if err := res.Blocks.Validate(content.ValidateOptions{AllowHTML: site.AllowHTML, Import: true, Lookup: imp.Ref}); err != nil {
		t.Errorf("the blocks do not pass Validate: %v", err)
	}
	written, err := res.Blocks.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON = %v", err)
	}
	var back content.Blocks
	if err := back.UnmarshalJSON(written); err != nil {
		t.Errorf("the JSON %.200s does not read back: %v", written, err)
	} else if again, err := back.MarshalJSON(); err != nil || string(again) != string(written) {
		t.Errorf("the blocks read back as other JSON: %.200s, want %.200s (%v)", again, written, err)
	}
	seen := make(map[string]bool)
	stack := []content.Blocks{res.Blocks}
	for len(stack) > 0 {
		list := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for i := range list {
			if id := list[i].ID; id != "" {
				if seen[id] {
					t.Errorf("the block ID %s is used twice", id)
				}
				seen[id] = true
			}
			stack = append(stack, list[i].Children)
		}
	}
	if res.More < -1 || res.More > len(res.Blocks) {
		t.Errorf("More = %d among %d blocks", res.More, len(res.Blocks))
	}
}

func sameBlocks(t *testing.T, got content.Blocks, want []content.Block) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual([]content.Block(got), want) {
		g, _ := got.MarshalJSON()
		w, _ := content.Blocks(want).MarshalJSON()
		t.Errorf("the blocks are\n%s\nwant\n%s", g, w)
	}
}

// withConverters adds converters, for the blocks that the tests make, until the end of the test.
func withConverters(t *testing.T, add map[string]converter) {
	t.Helper()
	saved := make(map[string]converter)
	for name, conv := range add {
		if old, ok := converters[name]; ok {
			saved[name] = old
		}
		converters[name] = conv
	}
	t.Cleanup(func() {
		for name := range add {
			if old, ok := saved[name]; ok {
				converters[name] = old
			} else {
				delete(converters, name)
			}
		}
	})
}

// lowerLimit sets a limit of the conversion until the end of the test.
func lowerLimit(t *testing.T, limit *int, v int) {
	t.Helper()
	old := *limit
	*limit = v
	t.Cleanup(func() { *limit = old })
}

// testContainer is the converter of a block of type typ that converts its inner blocks, as many times as times says.
func testContainer(typ content.BlockType, times int) converter {
	return func(c *bodyConv, doc string, b *wpblock.Block, p bodyPlace, _ noteAt) ([]content.Block, error) {
		g := c.newBlock(typ)
		inner := bodyPlace{depth: p.depth + 1, inColumns: p.inColumns, top: p.top}
		for range times {
			g.Children = append(g.Children, c.flow(doc, b.Inner, inner, noteAt{id: g.ID, sub: "/content"})...)
		}
		return []content.Block{g}, nil
	}
}

// testBlocks is the converter of a block that makes n blocks of type typ, with no content.
func testBlocks(typ content.BlockType, n int) converter {
	return func(c *bodyConv, _ string, _ *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
		out := make([]content.Block, n)
		for i := range out {
			out[i] = c.newBlock(typ)
		}
		return out, nil
	}
}

func TestBody(t *testing.T) {
	const box = `<!-- wp:my-plugin/box {"size":3} --><div class="box">Hello</div><!-- /wp:my-plugin/box -->`
	const dynamic = `<!-- wp:latest-posts {"postsToShow":4} /-->`
	const (
		stray     = "a block closer without an opener ends the blocks; WordPress shows the rest as it is"
		notClosed = `review: the block "acme/a" is not closed; it ends at the end of the content -> /body/0`
	)
	moreWith := func(attrs string) string {
		return "<!-- wp:more " + attrs + " -->\n<!--more-->\n<!-- /wp:more -->"
	}
	a := legacyBlock(bodyIDs[0], blockA, notConverted("acme/a"))
	for _, tt := range []struct {
		name  string
		text  string
		want  []content.Block
		more  int
		lines []string
	}{
		{"an unknown block", box, []content.Block{legacyBlock(bodyIDs[0], box, notConverted("my-plugin/box"))}, -1,
			[]string{legacyLine(notConverted("my-plugin/box"), "/body/0")}},
		{"a block without a namespace is of the core", `<!-- wp:acme /-->`,
			[]content.Block{legacyBlock(bodyIDs[0], `<!-- wp:acme /-->`, notConverted("core/acme"))}, -1,
			[]string{legacyLine(notConverted("core/acme"), "/body/0")}},
		{"a dynamic block", dynamic,
			[]content.Block{legacyBlock(bodyIDs[0], dynamic, `the block "core/latest-posts" is made by WordPress when the page is shown`)}, -1,
			[]string{legacyLine(`the block "core/latest-posts" is made by WordPress when the page is shown`, "/body/0")}},
		{"white space between blocks is left out", "\n" + blockA + " \n\t\f\r " + blockB + "\n\n",
			[]content.Block{a, legacyBlock(bodyIDs[1], blockB, notConverted("acme/b"))}, -1,
			[]string{legacyLine(notConverted("acme/a"), "/body/0"), legacyLine(notConverted("acme/b"), "/body/1")}},
		{"other HTML between blocks", "<p>Top</p>" + blockA + "\n<p>Hello</p>\n" + blockB,
			[]content.Block{
				paragraphBlock(bodyIDs[0], "Top"), legacyBlock(bodyIDs[1], blockA, notConverted("acme/a")),
				paragraphBlock(bodyIDs[2], "Hello"), legacyBlock(bodyIDs[3], blockB, notConverted("acme/b")),
			}, -1,
			[]string{legacyLine(notConverted("acme/a"), "/body/1"), legacyLine(notConverted("acme/b"), "/body/3")}},
		{"content without delimiters", "<p>Hello</p>\r\n<p>World</p>\r\n",
			[]content.Block{paragraphBlock(bodyIDs[0], "Hello"), paragraphBlock(bodyIDs[1], "World")}, -1, nil},
		{"content with a closer and no block", "<p>Hello</p><!-- /wp:acme/a --><p>World</p>",
			[]content.Block{paragraphBlock(bodyIDs[0], "Hello"), paragraphBlock(bodyIDs[1], "World")}, -1, nil},
		{"a closer without an opener", blockA + "<!-- /wp:acme/b -->tail",
			[]content.Block{a, legacyBlock(bodyIDs[1], "<!-- /wp:acme/b -->tail", stray)}, -1,
			[]string{legacyLine(notConverted("acme/a"), "/body/0"), legacyLine(stray, "/body/1")}},
		{"a block that is not closed", "<!-- wp:acme/a --><p>x</p>",
			[]content.Block{legacyBlock(bodyIDs[0], "<!-- wp:acme/a --><p>x</p>", notConverted("acme/a"))}, -1,
			[]string{legacyLine(notConverted("acme/a"), "/body/0"), notClosed}},
		{"blocks that are not closed", "<!-- wp:acme/a --><!-- wp:acme/b --><p>x</p>",
			[]content.Block{legacyBlock(bodyIDs[0], "<!-- wp:acme/a --><!-- wp:acme/b --><p>x</p>", notConverted("acme/a"))}, -1,
			[]string{legacyLine(notConverted("acme/a"), "/body/0"), notClosed}},
		{"attributes that are not JSON", `<!-- wp:acme/a {bad} /-->`,
			[]content.Block{legacyBlock(bodyIDs[0], `<!-- wp:acme/a {bad} /-->`, notConverted("acme/a"))}, -1,
			[]string{
				legacyLine(notConverted("acme/a"), "/body/0"),
				`changed: the attributes of the block "acme/a" are not valid JSON; they are ignored -> /body/0`,
			}},
		{"attributes that are not JSON on a block that is left out", "<!-- wp:nextpage {bad} -->\n<!--nextpage-->\n<!-- /wp:nextpage -->", nil, -1,
			[]string{
				`changed: the attributes of the block "core/nextpage" are not valid JSON; they are ignored -> /body`,
				"skipped: 1 page break is removed; whiteroom has no page breaks -> /body",
			}},
		// The content of the shortcode block is converted as WordPress shows it: no attachment is imported here, so no image is.
		{"the classic block and the shortcode block",
			"<!-- wp:freeform --><p>Hi</p><!-- /wp:freeform -->" + `<!-- wp:shortcode -->[gallery ids="1,2"]<!-- /wp:shortcode -->`,
			[]content.Block{paragraphBlock(bodyIDs[0], "Hi")}, -1,
			[]string{
				"skipped: the empty gallery is removed -> /body",
				"skipped: the image 1 of the gallery is not imported: it is not an imported image -> /body",
				"skipped: the image 2 of the gallery is not imported: it is not an imported image -> /body",
			}},
		{"one page break", blockA + "\n" + pageBreak + "\n" + blockB,
			[]content.Block{a, legacyBlock(bodyIDs[1], blockB, notConverted("acme/b"))}, -1,
			[]string{
				legacyLine(notConverted("acme/a"), "/body/0"), legacyLine(notConverted("acme/b"), "/body/1"),
				"skipped: 1 page break is removed; whiteroom has no page breaks -> /body",
			}},
		{"two page breaks", blockA + "\n" + pageBreak + "\n" + blockB + "\n" + pageBreak,
			[]content.Block{a, legacyBlock(bodyIDs[1], blockB, notConverted("acme/b"))}, -1,
			[]string{
				legacyLine(notConverted("acme/a"), "/body/0"), legacyLine(notConverted("acme/b"), "/body/1"),
				"skipped: 2 page breaks are removed; whiteroom has no page breaks -> /body",
			}},
		{"a read more block", blockA + "\n" + blockB + "\n" + moreBlock + "\n" + blockC,
			[]content.Block{
				a, legacyBlock(bodyIDs[1], blockB, notConverted("acme/b")), legacyBlock(bodyIDs[2], blockC, notConverted("acme/c")),
			}, 2,
			[]string{
				legacyLine(notConverted("acme/a"), "/body/0"), legacyLine(notConverted("acme/b"), "/body/1"),
				legacyLine(notConverted("acme/c"), "/body/2"),
			}},
		{"a second read more block is left out", blockA + moreBlock + blockB + moreBlock,
			[]content.Block{a, legacyBlock(bodyIDs[1], blockB, notConverted("acme/b"))}, 1,
			[]string{legacyLine(notConverted("acme/a"), "/body/0"), legacyLine(notConverted("acme/b"), "/body/1")}},
		{"a read more block first", moreBlock + blockA, []content.Block{a}, 0,
			[]string{legacyLine(notConverted("acme/a"), "/body/0")}},
		{"a read more block alone", moreBlock, nil, 0, nil},
		{"a read more block with a text and noTeaser", blockA + moreWith(`{"customText":"Go on","noTeaser":true}`), []content.Block{a}, 1,
			[]string{
				legacyLine(notConverted("acme/a"), "/body/0"),
				"changed: the more block hid the text before it on the page (noTeaser); whiteroom shows it -> /body",
				`skipped: the text "Go on" of the more link is not imported -> /body`,
			}},
		{"a second read more block is not noted", blockA + moreBlock + moreWith(`{"customText":"Go on","noTeaser":true}`), []content.Block{a}, 1,
			[]string{legacyLine(notConverted("acme/a"), "/body/0")}},
		{"a read more block with noTeaser that is false", blockA + moreWith(`{"noTeaser":false,"customText":""}`), []content.Block{a}, 1,
			[]string{legacyLine(notConverted("acme/a"), "/body/0")}},
		{"empty", "", nil, -1, nil},
		{"white space", " \n\t\f\r", nil, -1, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res, imp := convertBody(t, bodySite(false), tt.text)
			sameBlocks(t, res.Blocks, tt.want)
			if res.More != tt.more {
				t.Errorf("More = %d, want %d", res.More, tt.more)
			}
			wantLines(t, imp.Report, bodyLines(tt.lines...))
		})
	}
}

func TestBodyErrors(t *testing.T) {
	imp := bodyImport()
	if res, err := imp.Body(BodyOptions{Item: bodyItem(blockA), Key: bodyKey}); err == nil || res != nil {
		t.Errorf("Body without a site = %v, %v, want an error", res, err)
	}
	if res, err := imp.Body(BodyOptions{Site: bodySite(false), Key: bodyKey}); err == nil || res != nil {
		t.Errorf("Body without an item = %v, %v, want an error", res, err)
	}
	if n := len(imp.Report.Entries()); n != 0 {
		t.Errorf("the report has %d entries after the errors, want none", n)
	}
}

func TestBodyTarget(t *testing.T) {
	for _, target := range []Target{
		{File: "revisions/rev_X.json", Pointer: "/body"}, {File: "entries/ent_X.json", Pointer: "/fields/summary"},
		{File: "doc.json"}, {},
	} {
		imp := bodyImport()
		_, err := imp.Body(BodyOptions{Site: bodySite(false), Item: bodyItem(blockA + moreBlock + pageBreak), Target: target, Key: bodyKey})
		if err != nil {
			t.Fatal(err)
		}
		var got []Target
		for _, e := range imp.Report.Entries() {
			if e.Source != ItemSource("site.xml", bodyItem("")) {
				t.Errorf("the source of %+v is %+v, want the item", e, e.Source)
			}
			got = append(got, e.Target)
		}
		// The page break is noted at the body, and the legacy block at the first block.
		want := []Target{
			{File: target.File, Pointer: target.Pointer},
			{File: target.File, Pointer: target.Pointer + "/0"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("target %+v: the places are %+v, want %+v", target, got, want)
		}
	}
}

func TestBodyIsTheSameAgain(t *testing.T) {
	withConverters(t, map[string]converter{"test/group": testContainer(content.BlockGroup, 1)})
	text := groupOpen + blockA + groupClose + moreBlock + pageBreak + "\n<p>tail</p>\n<!-- wp:acme/c {bad} --><p>x</p>"
	first, one := convertBody(t, bodySite(false), text)
	second, other := convertBody(t, bodySite(false), text)
	if !reflect.DeepEqual(first, second) {
		t.Errorf("the same content gave %+v and %+v", first, second)
	}
	if a, b := reportText(t, one.Report), reportText(t, other.Report); a != b {
		t.Errorf("the same content gave the reports\n%s\nand\n%s", a, b)
	}
	if first.More != 1 || len(first.Blocks) != 3 {
		t.Errorf("More = %d with %d blocks, want 1 with 3", first.More, len(first.Blocks))
	}

	// Another key makes other IDs, and nothing else changes.
	imp := bodyImport()
	res, err := imp.Body(BodyOptions{Site: bodySite(false), Item: bodyItem(text), Target: bodyTarget, Key: "rev_other"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Blocks) != len(first.Blocks) || res.More != first.More {
		t.Fatalf("another key gave %d blocks and More %d, want %d and %d", len(res.Blocks), res.More, len(first.Blocks), first.More)
	}
	if a, b := reportText(t, imp.Report), reportText(t, one.Report); a != b {
		t.Errorf("another key gave the report\n%s\nwant\n%s", a, b)
	}
	for i := range res.Blocks {
		if res.Blocks[i].ID == first.Blocks[i].ID {
			t.Errorf("block %d has the ID %s with both keys", i, res.Blocks[i].ID)
		}
	}
}

func TestBodyInGroup(t *testing.T) {
	withConverters(t, map[string]converter{"test/group": testContainer(content.BlockGroup, 1)})
	text := groupOpen + blockA + moreBlock + pageBreak + groupOpen + blockB + groupClose + groupClose + moreBlock + blockC
	res, imp := convertBody(t, bodySite(false), text)
	// The groups are made first, so their IDs come first.
	want := []content.Block{
		{Type: content.BlockGroup, ID: bodyIDs[0], Children: content.Blocks{
			legacyBlock(bodyIDs[1], blockA, notConverted("acme/a")),
			{Type: content.BlockGroup, ID: bodyIDs[2], Children: content.Blocks{legacyBlock(bodyIDs[3], blockB, notConverted("acme/b"))}},
		}},
		legacyBlock(bodyIDs[4], blockC, notConverted("acme/c")),
	}
	sameBlocks(t, res.Blocks, want)
	// A read more block in a group is removed, and the one after the group is the read more block of the content.
	if res.More != 1 {
		t.Errorf("More = %d, want 1", res.More)
	}
	wantLines(t, imp.Report, bodyLines(
		legacyLine(notConverted("acme/a"), "/body/0/content/0"),
		legacyLine(notConverted("acme/b"), "/body/0/content/1/content/0"),
		legacyLine(notConverted("acme/c"), "/body/1"),
		"skipped: the more block is removed: it is not at the top of the content -> /body/0/content",
		"skipped: 1 page break is removed; whiteroom has no page breaks -> /body",
	))
}

func TestBodyAttrsOfAConverter(t *testing.T) {
	// A converter gets the attributes as an object, also where they were not valid JSON, and the block is the same.
	var got []string
	withConverters(t, map[string]converter{
		"test/attrs": func(_ *bodyConv, _ string, b *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
			got = append(got, fmt.Sprintf("%s %v %d", b.Name, b.Attrs != nil && b.Attrs.Kind == wpblock.Object, len(b.Attrs.Object)))
			return nil, nil
		},
	})
	_, imp := convertBody(t, bodySite(false), `<!-- wp:test/attrs /--><!-- wp:test/attrs {bad} /--><!-- wp:test/attrs {"a":1,"b":[]} /-->`)
	sameStrings(t, "the attributes", got, []string{"test/attrs true 0", "test/attrs true 0", "test/attrs true 2"})
	wantLines(t, imp.Report, bodyLines(
		`changed: the attributes of the block "test/attrs" are not valid JSON; they are ignored -> /body`))
}

func TestBodyMoreInACopy(t *testing.T) {
	// WordPress splits the teaser of a post on the content of the post only: the read more block and the page breaks of a
	// synced pattern are comments there, which WordPress shows later. A copy of a pattern drops them without a word, and
	// the ones of the post are what they are without a copy.
	const noTeaser = "<!-- wp:more {\"noTeaser\":true,\"customText\":\"Go\"} -->\n<!--more Go-->\n<!--noteaser-->\n<!-- /wp:more -->"
	pattern := func(id int64, text string) wxr.Item {
		return wxr.Item{ID: id, Type: "wp_block", Status: "publish", Title: "P", Content: text}
	}
	export := &wxr.Export{Items: []wxr.Item{
		pattern(41, blockB+moreBlock+blockC), pattern(42, blockB+noTeaser), pattern(43, blockB+pageBreak+blockC),
		pattern(44, groupOpen+blockB+moreBlock+groupClose),
	}}
	withConverters(t, map[string]converter{"test/group": testContainer(content.BlockGroup, 1)})
	convert := func(text string) (*BodyResult, *Import) {
		t.Helper()
		imp := New("site.xml", export, NewReport(Info{File: "site.xml"}))
		res, err := imp.Body(BodyOptions{Site: bodySite(false), Item: bodyItem(text), Target: bodyTarget, Key: bodyKey})
		if err != nil {
			t.Fatalf("Body = %v", err)
		}
		checkBody(t, imp, bodySite(false), res)
		return res, imp
	}
	copied := func(ref int, where string) string {
		return fmt.Sprintf(`changed: the synced pattern %d ("P") is copied here; whiteroom has no shared blocks yet (stage 2) -> %s`, ref, where)
	}

	t.Run("a read more block in a copy is not the one of the content", func(t *testing.T) {
		res, imp := convert(blockA + wpRef(41) + blockA)
		sameBlocks(t, res.Blocks, []content.Block{
			legacyBlock(bodyIDs[0], blockA, notConverted("acme/a")), legacyBlock(bodyIDs[1], blockB, notConverted("acme/b")),
			legacyBlock(bodyIDs[2], blockC, notConverted("acme/c")), legacyBlock(bodyIDs[3], blockA, notConverted("acme/a")),
		})
		if res.More != -1 {
			t.Errorf("More = %d, want -1", res.More)
		}
		wantLines(t, imp.Report, bodyLines(
			legacyLine(notConverted("acme/a"), "/body/0"), legacyLine(notConverted("acme/b"), "/body/1"),
			legacyLine(notConverted("acme/c"), "/body/2"), copied(41, "/body/1"), legacyLine(notConverted("acme/a"), "/body/3")))
	})

	t.Run("the read more block of the content, after a copy that has one", func(t *testing.T) {
		res, _ := convert(wpRef(41) + blockA + moreBlock + blockC)
		// The blocks of the copy and the one after it come before the read more block of the content.
		if len(res.Blocks) != 4 || res.More != 3 {
			t.Errorf("there are %d blocks and More is %d, want 4 and 3", len(res.Blocks), res.More)
		}
	})

	t.Run("a read more block in a group in a copy", func(t *testing.T) {
		res, imp := convert(groupOpen + wpRef(44) + groupClose)
		if res.More != -1 {
			t.Errorf("More = %d, want -1", res.More)
		}
		wantLines(t, imp.Report, bodyLines(
			legacyLine(notConverted("acme/b"), "/body/0/content/0/content/0"), copied(44, "/body/0/content/0")))
	})

	t.Run("the text and noTeaser of a read more block in a copy are not noted", func(t *testing.T) {
		res, imp := convert(blockA + wpRef(42))
		if res.More != -1 {
			t.Errorf("More = %d, want -1", res.More)
		}
		wantLines(t, imp.Report, bodyLines(
			legacyLine(notConverted("acme/a"), "/body/0"), legacyLine(notConverted("acme/b"), "/body/1"), copied(42, "/body/1")))
	})

	t.Run("a page break in a copy is not counted", func(t *testing.T) {
		_, imp := convert(wpRef(43) + pageBreak)
		wantLines(t, imp.Report, bodyLines(
			legacyLine(notConverted("acme/b"), "/body/0"), legacyLine(notConverted("acme/c"), "/body/1"), copied(43, "/body/0"),
			"skipped: 1 page break is removed; whiteroom has no page breaks -> /body"))
	})

	t.Run("the read more block of the content is the first one of its own", func(t *testing.T) {
		res, _ := convert(wpRef(41) + moreBlock + wpRef(41) + moreBlock)
		if res.More != 2 {
			t.Errorf("More = %d, want 2", res.More)
		}
	})
}

func TestBodyDepth(t *testing.T) {
	withConverters(t, map[string]converter{
		"test/group": testContainer(content.BlockGroup, 1),
		// A block that nests deeper by itself than the flow can tell.
		"test/chain": func(c *bodyConv, _ string, _ *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
			var chain []content.Block
			for range 40 {
				g := c.newBlock(content.BlockGroup)
				g.Children = chain
				chain = []content.Block{g}
			}
			return chain, nil
		},
	})
	nest := func(n int) string { return strings.Repeat(groupOpen, n) + strings.Repeat(groupClose, n) }
	depth := func(bs content.Blocks) int {
		n := 0
		for ; len(bs) > 0; bs = bs[0].Children {
			n++
		}
		return n
	}

	// 32 levels are the most that a document can have.
	res, imp := convertBody(t, bodySite(false), nest(32))
	if got := depth(res.Blocks); got != 32 || res.Blocks[0].Type != content.BlockGroup {
		t.Errorf("32 groups became %d levels", got)
	}
	wantLines(t, imp.Report, nil)

	// A group that would have a block at the 33rd level is a legacy block with what is in it.
	res, imp = convertBody(t, bodySite(false), nest(33))
	if got := depth(res.Blocks); got != 32 {
		t.Fatalf("33 groups became %d levels, want 32", got)
	}
	last := res.Blocks
	for i := 1; i < 31; i++ {
		last = last[0].Children
	}
	if len(last) != 1 || last[0].Type != content.BlockGroup || len(last[0].Children) != 1 {
		t.Fatalf("level 31 is %+v, want a group with a block", last)
	}
	if got := last[0].Children[0]; got.Type != content.BlockLegacy || got.Attrs.HTML != nest(2) || got.Attrs.Reason != deepReason {
		t.Errorf("level 32 is %+v, want a legacy block of the two groups", got)
	}
	pointer := "/body/0" + strings.Repeat("/content/0", 31)
	wantLines(t, imp.Report, bodyLines(legacyLine(deepReason, pointer)))

	// The recursion stops at the 32nd level, however deep the content nests.
	calls := 0
	group := converters["test/group"]
	converters["test/group"] = func(c *bodyConv, doc string, b *wpblock.Block, p bodyPlace, at noteAt) ([]content.Block, error) {
		calls++
		return group(c, doc, b, p, at)
	}
	res, _ = convertBody(t, bodySite(false), nest(500))
	if depth(res.Blocks) != 32 || calls != 32 {
		t.Errorf("500 groups gave %d levels with %d calls of the converter, want 32 and 32", depth(res.Blocks), calls)
	}
	converters["test/group"] = group

	// The same with the blocks that a converter makes by itself.
	res, imp = convertBody(t, bodySite(false), `<!-- wp:test/chain /-->`)
	sameBlocks(t, res.Blocks, []content.Block{legacyBlock(bodyIDs[40], `<!-- wp:test/chain /-->`, deepReason)})
	wantLines(t, imp.Report, bodyLines(legacyLine(deepReason, "/body/0")))
}

func TestBodyFailingConverter(t *testing.T) {
	withConverters(t, map[string]converter{
		"test/fail": func(c *bodyConv, _ string, _ *wpblock.Block, _ bodyPlace, at noteAt) ([]content.Block, error) {
			c.pageBreaks++
			c.note(at, Skipped, "something is left out")
			c.note(noteAt{}, Changed, "something is changed")
			return nil, legacyf("it fails at %q", "x")
		},
	})
	// Only what is said of the legacy block is reported.
	text := `<!-- wp:test/fail {"a":1} -->` + `<!-- /wp:test/fail -->`
	res, imp := convertBody(t, bodySite(false), text)
	sameBlocks(t, res.Blocks, []content.Block{legacyBlock(bodyIDs[0], text, `it fails at "x"`)})
	wantLines(t, imp.Report, bodyLines(legacyLine(`it fails at "x"`, "/body/0")))
}

func TestBodyGuard(t *testing.T) {
	const missingMedia = "the conversion does not pass validation: /attrs/media: missing media"
	withConverters(t, map[string]converter{
		"test/group": testContainer(content.BlockGroup, 1),
		"test/quote": testContainer(content.BlockQuote, 1),
		"test/image": func(c *bodyConv, _ string, _ *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
			// An image needs a medium, which this one does not have.
			img := c.newBlock(content.BlockImage)
			c.note(noteAt{id: img.ID}, Changed, "the size is lost")
			return []content.Block{img}, nil
		},
		"test/images": testBlocks(content.BlockImage, 2),
		"test/direct": func(c *bodyConv, _ string, _ *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
			q := c.newBlock(content.BlockQuote)
			img, p := c.newBlock(content.BlockImage), c.newBlock(content.BlockParagraph)
			q.Children = content.Blocks{img, p}
			return []content.Block{q}, nil
		},
		"test/hollow": func(c *bodyConv, _ string, _ *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
			q := c.newBlock(content.BlockQuote)
			q.Children = content.Blocks{c.newBlock(content.BlockImage)}
			return []content.Block{q}, nil
		},
		"test/html": func(c *bodyConv, _ string, _ *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
			h := c.newBlock(content.BlockHTML)
			h.Attrs.HTML = "<b>x</b>"
			return []content.Block{h}, nil
		},
		"test/anchored": func(c *bodyConv, doc string, b *wpblock.Block, p bodyPlace, _ noteAt) ([]content.Block, error) {
			// Two of these have the same anchor.
			q := c.newBlock(content.BlockQuote)
			q.Anchor = "x"
			q.Children = c.flow(doc, b.Inner, bodyPlace{depth: p.depth + 1, top: p.top}, noteAt{id: q.ID, sub: "/content"})
			return []content.Block{q}, nil
		},
	})

	t.Run("a block that does not pass becomes a legacy block of the original text", func(t *testing.T) {
		res, imp := convertBody(t, bodySite(false), blockA+imageBlock+blockB)
		// The image has the ID 1, and the legacy block that replaces it the ID 3.
		sameBlocks(t, res.Blocks, []content.Block{
			legacyBlock(bodyIDs[0], blockA, notConverted("acme/a")),
			legacyBlock(bodyIDs[3], imageBlock, missingMedia),
			legacyBlock(bodyIDs[2], blockB, notConverted("acme/b")),
		})
		// What was said of the image is gone.
		wantLines(t, imp.Report, bodyLines(
			legacyLine(notConverted("acme/a"), "/body/0"), legacyLine(missingMedia, "/body/1"), legacyLine(notConverted("acme/b"), "/body/2")))
	})

	t.Run("the blocks from the same block go together", func(t *testing.T) {
		const images = `<!-- wp:test/images /-->`
		res, imp := convertBody(t, bodySite(false), blockA+images+blockB)
		sameBlocks(t, res.Blocks, []content.Block{
			legacyBlock(bodyIDs[0], blockA, notConverted("acme/a")),
			legacyBlock(bodyIDs[4], images, missingMedia),
			legacyBlock(bodyIDs[3], blockB, notConverted("acme/b")),
		})
		wantLines(t, imp.Report, bodyLines(
			legacyLine(notConverted("acme/a"), "/body/0"), legacyLine(missingMedia, "/body/1"), legacyLine(notConverted("acme/b"), "/body/2")))
	})

	t.Run("a block in a quote leaves the quote", func(t *testing.T) {
		text := "<!-- wp:test/quote -->" + imageBlock + blockA + "<!-- /wp:test/quote -->"
		res, imp := convertBody(t, bodySite(false), text)
		sameBlocks(t, res.Blocks, []content.Block{{Type: content.BlockQuote, ID: bodyIDs[0], Children: content.Blocks{
			legacyBlock(bodyIDs[3], imageBlock, missingMedia),
			legacyBlock(bodyIDs[2], blockA, notConverted("acme/a")),
		}}})
		wantLines(t, imp.Report, bodyLines(
			legacyLine(missingMedia, "/body/0/content/0"), legacyLine(notConverted("acme/a"), "/body/0/content/1")))
	})

	t.Run("a block with no origin is left out", func(t *testing.T) {
		const direct = `<!-- wp:test/direct /-->`
		res, imp := convertBody(t, bodySite(false), direct)
		sameBlocks(t, res.Blocks, []content.Block{{Type: content.BlockQuote, ID: bodyIDs[0], Children: content.Blocks{
			{Type: content.BlockParagraph, ID: bodyIDs[2]},
		}}})
		wantLines(t, imp.Report, bodyLines("skipped: the block is not imported: /0/attrs/media: missing media -> /body/0/content"))
	})

	t.Run("a block that cannot stay without what was left out is a legacy block", func(t *testing.T) {
		const hollow = `<!-- wp:test/hollow /-->`
		reason := "the conversion does not pass validation: /content: quote needs at least one block"
		res, imp := convertBody(t, bodySite(false), hollow)
		sameBlocks(t, res.Blocks, []content.Block{legacyBlock(bodyIDs[2], hollow, reason)})
		// The record of the image that was left out is gone with the quote.
		wantLines(t, imp.Report, bodyLines(legacyLine(reason, "/body/0")))
	})

	t.Run("the html blocks follow the site settings", func(t *testing.T) {
		const html = `<!-- wp:test/html /-->`
		res, imp := convertBody(t, bodySite(true), html)
		sameBlocks(t, res.Blocks, []content.Block{{Type: content.BlockHTML, ID: bodyIDs[0], Attrs: content.Attrs{HTML: "<b>x</b>"}}})
		wantLines(t, imp.Report, nil)

		reason := "the conversion does not pass validation: html blocks are not enabled"
		res, imp = convertBody(t, bodySite(false), html)
		sameBlocks(t, res.Blocks, []content.Block{legacyBlock(bodyIDs[1], html, reason)})
		wantLines(t, imp.Report, bodyLines(legacyLine(reason, "/body/0")))
	})

	t.Run("HTML between blocks of control characters only is nothing", func(t *testing.T) {
		// U+0085 is a control character, which Normalize removes, so no legacy block could hold this.
		const nel = "\xc2\x85"
		res, imp := convertBody(t, bodySite(false), blockA+nel+blockB)
		sameBlocks(t, res.Blocks, []content.Block{
			legacyBlock(bodyIDs[0], blockA, notConverted("acme/a")),
			legacyBlock(bodyIDs[1], blockB, notConverted("acme/b")),
		})
		wantLines(t, imp.Report, bodyLines(legacyLine(notConverted("acme/a"), "/body/0"), legacyLine(notConverted("acme/b"), "/body/1")))

		res, imp = convertBody(t, bodySite(false), "\v")
		sameBlocks(t, res.Blocks, nil)
		wantLines(t, imp.Report, nil)
	})

	t.Run("a legacy block that the guard made is left out if it does not pass", func(t *testing.T) {
		imp := bodyImport()
		c := newBodyConv(imp, BodyOptions{Site: bodySite(false), Item: bodyItem(blockA), Target: bodyTarget, Key: bodyKey})
		blk := c.newBlock(content.BlockLegacy)
		blk.Attrs.Reason = "x"
		c.origins[blk.ID] = origin{guard: true}
		blocks := content.Blocks{blk}
		c.guard(&blocks)
		c.flush(blocks)
		sameBlocks(t, blocks, nil)
		wantLines(t, imp.Report, bodyLines("skipped: the block is not imported: /0/attrs/html: missing html -> /body"))
	})

	t.Run("one round mends all the problems that Validate finds", func(t *testing.T) {
		calls := countValidations(t)
		const n = 300
		ids := nextIDs(newBlockIDs(bodyKey), 3*n)
		res, imp := convertBody(t, bodySite(false), strings.Repeat(imageBlock+blockA, n))
		var want []content.Block
		var lines []string
		for i := range n {
			want = append(want, legacyBlock(ids[2*n+i], imageBlock, missingMedia), legacyBlock(ids[2*i+1], blockA, notConverted("acme/a")))
			lines = append(lines, legacyLine(missingMedia, fmt.Sprintf("/body/%d", 2*i)),
				legacyLine(notConverted("acme/a"), fmt.Sprintf("/body/%d", 2*i+1)))
		}
		sameBlocks(t, res.Blocks, want)
		wantLines(t, imp.Report, bodyLines(lines...))
		if *calls != 2 {
			t.Errorf("the guard validated %d times, want 2", *calls)
		}
	})

	t.Run("problems at several levels in one round", func(t *testing.T) {
		calls := countValidations(t)
		const (
			images = `<!-- wp:test/images /-->`
			direct = `<!-- wp:test/direct /-->`
			quote  = "<!-- wp:test/quote -->" + imageBlock + blockA + "<!-- /wp:test/quote -->"
		)
		res, imp := convertBody(t, bodySite(false), images+quote+direct)
		// IDs: the images 0 and 1, the quote 2 with the image 3 and the legacy block 4, the quote 5 with the image 6 and
		// the paragraph 7; then the legacy blocks of the guard, 8 and 9, in the order of the document.
		sameBlocks(t, res.Blocks, []content.Block{
			legacyBlock(bodyIDs[8], images, missingMedia),
			{Type: content.BlockQuote, ID: bodyIDs[2], Children: content.Blocks{
				legacyBlock(bodyIDs[9], imageBlock, missingMedia),
				legacyBlock(bodyIDs[4], blockA, notConverted("acme/a")),
			}},
			{Type: content.BlockQuote, ID: bodyIDs[5], Children: content.Blocks{{Type: content.BlockParagraph, ID: bodyIDs[7]}}},
		})
		wantLines(t, imp.Report, bodyLines(
			legacyLine(missingMedia, "/body/0"), legacyLine(missingMedia, "/body/1/content/0"),
			legacyLine(notConverted("acme/a"), "/body/1/content/1"),
			"skipped: the block is not imported: /0/attrs/media: missing media -> /body/2/content"))
		if *calls != 2 {
			t.Errorf("the guard validated %d times, want 2", *calls)
		}
	})

	t.Run("a problem in a block that is taken out goes with the block", func(t *testing.T) {
		calls := countValidations(t)
		const anchored = "<!-- wp:test/anchored -->" + imageBlock + "<!-- /wp:test/anchored -->"
		duplicate := `the conversion does not pass validation: /anchor: duplicate anchor "x" (first used at /0/anchor)`
		res, imp := convertBody(t, bodySite(false), anchored+anchored)
		// The quotes are 0 and 2, their images 1 and 3. The image of the second quote makes no legacy block of its own.
		q := content.Block{Type: content.BlockQuote, ID: bodyIDs[0], Anchor: "x", Children: content.Blocks{legacyBlock(bodyIDs[4], imageBlock, missingMedia)}}
		sameBlocks(t, res.Blocks, []content.Block{q, legacyBlock(bodyIDs[5], anchored, duplicate)})
		wantLines(t, imp.Report, bodyLines(legacyLine(missingMedia, "/body/0/content/0"), legacyLine(duplicate, "/body/1")))
		if *calls != 2 {
			t.Errorf("the guard validated %d times, want 2", *calls)
		}
	})

	t.Run("blocks that cannot stay without what was left out take a round more", func(t *testing.T) {
		calls := countValidations(t)
		const hollow = `<!-- wp:test/hollow /-->`
		reason := "the conversion does not pass validation: /content: quote needs at least one block"
		res, imp := convertBody(t, bodySite(false), hollow+hollow)
		sameBlocks(t, res.Blocks, []content.Block{legacyBlock(bodyIDs[4], hollow, reason), legacyBlock(bodyIDs[5], hollow, reason)})
		// The records of the images that were left out are gone with the quotes.
		wantLines(t, imp.Report, bodyLines(legacyLine(reason, "/body/0"), legacyLine(reason, "/body/1")))
		if *calls != 3 {
			t.Errorf("the guard validated %d times, want 3", *calls)
		}
	})
}

// countValidations counts the validations of the guard until the end of the test.
func countValidations(t *testing.T) *int {
	t.Helper()
	calls, old := new(int), validateBlocks
	validateBlocks = func(blocks content.Blocks, opts content.ValidateOptions) error {
		*calls++
		return old(blocks, opts)
	}
	t.Cleanup(func() { validateBlocks = old })
	return calls
}

func TestBodyLimits(t *testing.T) {
	t.Run("the size of the content", func(t *testing.T) {
		lowerLimit(t, &maxBodyBytes, 20)
		res, imp := convertBody(t, bodySite(false), blockA+blockB)
		sameBlocks(t, res.Blocks, nil)
		wantLines(t, imp.Report, bodyLines("skipped: the content is not imported: it is too large: 38 bytes (max 20) -> /body"))
		if res.More != -1 {
			t.Errorf("More = %d, want -1", res.More)
		}
		// The most is allowed.
		res, _ = convertBody(t, bodySite(false), blockA)
		if len(res.Blocks) != 1 {
			t.Errorf("a content of %d bytes gave %d blocks, want 1", len(blockA), len(res.Blocks))
		}
	})

	t.Run("the blocks of the content", func(t *testing.T) {
		lowerLimit(t, &maxWPBlocks, 2)
		res, imp := convertBody(t, bodySite(false), blockA+blockB+blockC)
		sameBlocks(t, res.Blocks, nil)
		wantLines(t, imp.Report, bodyLines("skipped: the content is not imported: it has more than 2 blocks -> /body"))
		// HTML between blocks is not a block of WordPress that the parser counts; a line break in it makes no block.
		res, _ = convertBody(t, bodySite(false), blockA+"<br>"+blockB)
		if len(res.Blocks) != 2 {
			t.Errorf("two blocks and HTML gave %d blocks, want 2", len(res.Blocks))
		}
	})

	t.Run("the blocks that are converted", func(t *testing.T) {
		// The group converts its inner blocks twice, as the copy of a pattern converts the blocks of the pattern.
		withConverters(t, map[string]converter{"test/twice": testContainer(content.BlockGroup, 2)})
		lowerLimit(t, &maxWPBlocks, 3)
		text := "<!-- wp:test/twice -->" + blockA + blockB + "<!-- /wp:test/twice -->"
		res, imp := convertBody(t, bodySite(false), text)
		sameBlocks(t, res.Blocks, []content.Block{{Type: content.BlockGroup, ID: bodyIDs[0], Children: content.Blocks{
			legacyBlock(bodyIDs[1], blockA, notConverted("acme/a")), legacyBlock(bodyIDs[2], blockB, notConverted("acme/b")),
		}}})
		wantLines(t, imp.Report, bodyLines(
			legacyLine(notConverted("acme/a"), "/body/0/content/0"), legacyLine(notConverted("acme/b"), "/body/0/content/1"),
			fmt.Sprintf("skipped: the rest of the content is not imported: the conversion reads at most 3 blocks and %d bytes of HTML -> /body/0/content", maxHTMLBytes)))
	})

	t.Run("a stop that the block it is in does not survive", func(t *testing.T) {
		// The block converts its inner blocks twice and then fails, so it is a legacy block, and what is said of its inner
		// blocks is gone; but not that the conversion stopped, which is said at the body then.
		withConverters(t, map[string]converter{
			"test/stopfail": func(c *bodyConv, doc string, b *wpblock.Block, p bodyPlace, _ noteAt) ([]content.Block, error) {
				g := c.newBlock(content.BlockGroup)
				inner := bodyPlace{depth: p.depth + 1, top: p.top}
				for range 2 {
					g.Children = append(g.Children, c.flow(doc, b.Inner, inner, noteAt{id: g.ID, sub: "/content"})...)
				}
				return nil, legacyf("it fails")
			},
		})
		lowerLimit(t, &maxWPBlocks, 3)
		text := "<!-- wp:test/stopfail -->" + blockA + blockB + "<!-- /wp:test/stopfail -->"
		res, imp := convertBody(t, bodySite(false), text)
		sameBlocks(t, res.Blocks, []content.Block{legacyBlock(bodyIDs[3], text, "it fails")})
		wantLines(t, imp.Report, bodyLines(
			legacyLine("it fails", "/body/0"),
			fmt.Sprintf("skipped: the rest of the content is not imported: the conversion reads at most 3 blocks and %d bytes of HTML -> /body", maxHTMLBytes)))
	})

	t.Run("the HTML that is read", func(t *testing.T) {
		withConverters(t, map[string]converter{
			"test/read": func(c *bodyConv, _ string, b *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
				if err := c.readHTML(len(b.HTML())); err != nil {
					return nil, err
				}
				return []content.Block{c.newBlock(content.BlockParagraph)}, nil
			},
		})
		lowerLimit(t, &maxHTMLBytes, 10)
		read := "<!-- wp:test/read -->123456<!-- /wp:test/read -->"
		res, imp := convertBody(t, bodySite(false), read+read+blockA)
		reason := "the conversion has read more than 10 bytes of HTML"
		sameBlocks(t, res.Blocks, []content.Block{
			{Type: content.BlockParagraph, ID: bodyIDs[0]},
			legacyBlock(bodyIDs[1], read, reason),
		})
		wantLines(t, imp.Report, bodyLines(
			legacyLine(reason, "/body/1"),
			fmt.Sprintf("skipped: the rest of the content is not imported: the conversion reads at most %d blocks and 10 bytes of HTML -> /body", maxWPBlocks)))
	})

	t.Run("the blocks of a document", func(t *testing.T) {
		withConverters(t, map[string]converter{
			"test/many":  testBlocks(content.BlockParagraph, 6000),
			"test/ten":   testBlocks(content.BlockParagraph, content.MaxBlocks),
			"test/group": testContainer(content.BlockGroup, 1),
			"test/bulk": func(c *bodyConv, _ string, _ *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
				g := c.newBlock(content.BlockGroup)
				for range 6000 {
					g.Children = append(g.Children, c.newBlock(content.BlockParagraph))
				}
				return []content.Block{g}, nil
			},
		})
		const cut = "skipped: the blocks from /body/%d on are not imported: a document has at most 10000 blocks -> /body"

		// The blocks at the top are counted one by one.
		res, imp := convertBody(t, bodySite(false), `<!-- wp:test/many /--><!-- wp:test/many /-->`)
		if len(res.Blocks) != content.MaxBlocks {
			t.Errorf("12000 blocks became %d, want %d", len(res.Blocks), content.MaxBlocks)
		}
		wantLines(t, imp.Report, bodyLines(fmt.Sprintf(cut, content.MaxBlocks)))

		// A block with the blocks in it goes with them or not at all.
		res, imp = convertBody(t, bodySite(false), `<!-- wp:test/bulk /--><!-- wp:test/bulk /-->`+blockA)
		if len(res.Blocks) != 1 || len(res.Blocks[0].Children) != 6000 {
			t.Errorf("two groups of 6000 blocks became %d blocks at the top, want one group", len(res.Blocks))
		}
		wantLines(t, imp.Report, bodyLines(fmt.Sprintf(cut, 1)))

		// The most is allowed, and the first block that is over it is left out with what comes after.
		res, imp = convertBody(t, bodySite(false), `<!-- wp:test/ten /-->`)
		if len(res.Blocks) != content.MaxBlocks {
			t.Errorf("%d blocks became %d", content.MaxBlocks, len(res.Blocks))
		}
		wantLines(t, imp.Report, nil)
		res, imp = convertBody(t, bodySite(false), `<!-- wp:test/ten /-->`+blockA)
		if len(res.Blocks) != content.MaxBlocks {
			t.Errorf("one block more became %d blocks, want %d", len(res.Blocks), content.MaxBlocks)
		}
		wantLines(t, imp.Report, bodyLines(fmt.Sprintf(cut, content.MaxBlocks)))

		// The notes of the blocks that are left out are not reported, and More is counted among those that are not.
		res, imp = convertBody(t, bodySite(false), `<!-- wp:test/ten /-->`+moreBlock+blockA)
		if res.More != content.MaxBlocks {
			t.Errorf("More = %d, want %d", res.More, content.MaxBlocks)
		}
		wantLines(t, imp.Report, bodyLines(fmt.Sprintf(cut, content.MaxBlocks)))
	})
}

func TestBodyLegacyTooLong(t *testing.T) {
	// A legacy block holds at most content.MaxTextBytes bytes of HTML.
	const open, closer = "<!-- wp:acme/a -->", "<!-- /wp:acme/a -->"
	filler := strings.Repeat("x", content.MaxTextBytes-len(open)-len(closer))
	fits := open + filler + closer
	res, imp := convertBody(t, bodySite(false), fits)
	sameBlocks(t, res.Blocks, []content.Block{legacyBlock(bodyIDs[0], fits, notConverted("acme/a"))})
	wantLines(t, imp.Report, bodyLines(legacyLine(notConverted("acme/a"), "/body/0")))

	// The HTML between blocks is a paragraph, which is a legacy block if its text is too long, and its HTML then is.
	long := strings.Repeat("y", content.MaxTextBytes+1)
	res, imp = convertBody(t, bodySite(false), blockB+long+blockC)
	sameBlocks(t, res.Blocks, []content.Block{
		legacyBlock(bodyIDs[0], blockB, notConverted("acme/b")), legacyBlock(bodyIDs[2], blockC, notConverted("acme/c")),
	})
	wantLines(t, imp.Report, bodyLines(
		legacyLine(notConverted("acme/b"), "/body/0"), legacyLine(notConverted("acme/c"), "/body/1"),
		fmt.Sprintf("skipped: the element <p> is not imported: its HTML is too long: %d bytes (max %d) -> /body", len(long)+len("<p></p>"), content.MaxTextBytes)))

	// A block.
	block := open + filler + "yyyyyyy" + closer
	res, imp = convertBody(t, bodySite(false), blockB+block)
	sameBlocks(t, res.Blocks, []content.Block{legacyBlock(bodyIDs[0], blockB, notConverted("acme/b"))})
	wantLines(t, imp.Report, bodyLines(
		legacyLine(notConverted("acme/b"), "/body/0"),
		fmt.Sprintf(`skipped: the block "acme/a" is not imported: its HTML is too long: %d bytes (max %d) -> /body`, len(block), content.MaxTextBytes)))

	// The content of the classic editor, which has no block, is paragraphs, and an element that is too long is not imported.
	classic := "<p>" + strings.Repeat("x", content.MaxTextBytes) + "</p>"
	res, imp = convertBody(t, bodySite(false), classic)
	sameBlocks(t, res.Blocks, nil)
	wantLines(t, imp.Report, bodyLines(
		fmt.Sprintf("skipped: the element <p> is not imported: its HTML is too long: %d bytes (max %d) -> /body", len(classic), content.MaxTextBytes)))

	// What comes after a closer without an opener is the rest of the content, which WordPress shows as it is.
	rest := "<!-- /wp:acme/c -->" + strings.Repeat("y", content.MaxTextBytes)
	res, imp = convertBody(t, bodySite(false), blockB+rest)
	sameBlocks(t, res.Blocks, []content.Block{legacyBlock(bodyIDs[0], blockB, notConverted("acme/b"))})
	wantLines(t, imp.Report, bodyLines(
		legacyLine(notConverted("acme/b"), "/body/0"),
		fmt.Sprintf("skipped: the rest of the content after a block closer without an opener is not imported: its HTML is too long: %d bytes (max %d) -> /body",
			len(rest), content.MaxTextBytes)))

	// The same for the content of a synced pattern that has no block, and the post is not lost with it.
	e := patternsExport(1, func(int) string { return classic })
	imp = New("site.xml", e, NewReport(Info{File: "site.xml"}))
	_, patterned := limitedBody(t, imp, wpPara("a")+wpRef(1)+wpPara("b"))
	sameBlocks(t, patterned.Blocks, []content.Block{
		{Type: content.BlockParagraph, ID: bodyIDs[0], Text: content.RichText{rtText("a")}},
		{Type: content.BlockParagraph, ID: bodyIDs[2], Text: content.RichText{rtText("b")}},
	})
	wantLines(t, imp.Report, bodyLines(
		fmt.Sprintf("skipped: the element <p> is not imported: its HTML is too long: %d bytes (max %d) -> /body", len(classic), content.MaxTextBytes),
		`changed: the synced pattern 1 ("Pattern 1") is copied here; whiteroom has no shared blocks yet (stage 2) -> /body`))
}

func TestWithClassic(t *testing.T) {
	// The block that stands for the whole content of the classic editor is the block that the parser makes of HTML between
	// blocks: the HTML is in its pieces.
	for _, text := range []string{"<p>Hello</p>", "plain", "<!-- a comment -->\n<p>x</p>", " "} {
		parsed, err := wpblock.Parse(text, wpblock.Options{})
		if err != nil || len(parsed) != 1 {
			t.Fatalf("Parse(%q) = %v, %v", text, parsed, err)
		}
		got := withClassic(nil, text)
		if len(got) != 1 || !reflect.DeepEqual(got[0], parsed[0]) {
			t.Errorf("withClassic(%q) = %+v, want %+v", text, got, parsed[0])
		}
		if got[0].HTML() != text {
			t.Errorf("the HTML of the block for %q is %q", text, got[0].HTML())
		}
	}

	// The blocks of the content stay as they are if one of them has a name; HTML only is the classic content, also if it has
	// a closer without an opener.
	named, err := wpblock.Parse(blockA+"<p>x</p>", wpblock.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := withClassic(named, blockA+"<p>x</p>"); !reflect.DeepEqual(got, named) {
		t.Errorf("withClassic changed the blocks %+v into %+v", named, got)
	}
	const stray = "<p>Hello</p><!-- /wp:acme/a --><p>World</p>"
	parsed, err := wpblock.Parse(stray, wpblock.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := withClassic(parsed, stray); len(got) != 1 || got[0].HTML() != stray || got[0].Start != 0 || got[0].End != len(stray) || got[0].CloserStart != len(stray) {
		t.Errorf("withClassic(%q) = %+v", stray, got)
	}
}

func TestDynamicBlock(t *testing.T) {
	for _, name := range []string{
		"core/archives", "core/avatar", "core/breadcrumbs", "core/calendar", "core/categories", "core/footnotes", "core/home-link",
		"core/latest-comments", "core/latest-posts", "core/legacy-widget", "core/loginout", "core/read-more", "core/rss",
		"core/search", "core/social-link", "core/social-links", "core/table-of-contents", "core/tag-cloud", "core/template-part",
		"core/terms-query", "core/widget-group",
		"core/comment-author-name", "core/comment-template", "core/comments", "core/comments-title", "core/navigation",
		"core/navigation-link", "core/navigation-submenu", "core/page-list", "core/page-list-item", "core/post-content",
		"core/post-title", "core/post-featured-image", "core/query", "core/query-pagination", "core/query-title",
		"core/site-logo", "core/site-title", "core/term-description",
	} {
		if !dynamicBlock(name) {
			t.Errorf("dynamicBlock(%q) is false", name)
		}
	}
	for _, name := range []string{
		"", "core/paragraph", "core/image", "core/group", "core/column", "core/columns", "core/block", "core/pattern", "core/more",
		"core/nextpage", "core/freeform", "core/shortcode", "core/html", "core/post", "core/site", "core/term", "core/terms",
		"core/comm", "core/latest-post", "acme/latest-posts", "acme/core/query", "latest-posts", "Core/latest-posts",
	} {
		if dynamicBlock(name) {
			t.Errorf("dynamicBlock(%q) is true", name)
		}
	}
}
