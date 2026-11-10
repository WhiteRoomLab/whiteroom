package wpimport

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wpblock"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

// wpForm writes a block of Contact Form 7 with the attributes.
func wpForm(attrs string) string {
	return wpBlock("contact-form-7/contact-form-selector", attrs, `<div class="wp-block-contact-form-7-contact-form-selector">[contact-form-7]</div>`)
}

// wpRef writes a block that uses the synced pattern with the post ID ref.
func wpRef(ref int) string { return fmt.Sprintf(`<!-- wp:block {"ref":%d} /-->`, ref) }

func TestRefForms(t *testing.T) {
	form := mediaIDs.contact
	jsonForm := func(n int, keys string) string { return jsonBlock("form", n, keys, fmt.Sprintf(`"form":%q`, form), "") }
	legacyForm := func(attrs, reason string) (string, string) {
		return jsonLegacy(0, wpForm(attrs), reason), legacyLine(reason, "/body/0")
	}
	var cases []textCase
	for _, tt := range []struct{ name, attrs string }{
		{"by its hash, if the ID is not one", `{"id":40,"hash":"1a2b3c4","title":"Other"}`},
		{"by its ID if the hash is in capitals, which is no hash", `{"hash":"1A2B3C4D","id":31}`},
		{"by its ID", `{"id":31}`},
		{"by its ID written as text", `{"id":"31"}`},
		{"by its title, the first of the forms that have it", `{"title":"Contact"}`},
		{"by the ID before the title", `{"id":31,"title":"Quote"}`},
		{"by the title if the ID is of a post that is not a form", `{"id":12,"title":"Contact"}`},
		{"by the ID if the hash is shorter than 7", `{"hash":"1a2b3c","id":31}`},
		{"by the ID if the hash is not hexadecimal", `{"hash":"1a2b3cz","id":31}`},
	} {
		cases = append(cases, textCase{name: "a form " + tt.name, text: wpForm(tt.attrs), want: jsonList(jsonForm(0, ""))})
	}
	for _, tt := range []struct{ name, attrs, reason string }{
		{"by the hash before the ID and the title", `{"id":31,"hash":"0123456","title":"Quote"}`, `the form "Quote" is not imported`},
		{"that is not imported, by its ID", `{"id":32}`, "the form 32 is not imported"},
		{"that is not imported, by its title", `{"title":"Quote"}`, `the form "Quote" is not imported`},
		{"that is not imported, by its hash", `{"hash":"9f8e7d6"}`, "the form is not imported"},
		{"that is not in the WXR", `{"id":999,"title":"Missing"}`, `the form "Missing" is not imported`},
		{"without anything to find it by", `{}`, "the form is not imported"},
		{"of a hash that is longer than the one of the form", `{"hash":"1a2b3c4d5e6f7a8b9"}`, "the form is not imported"},
		{"of a hash in capitals, which Contact Form 7 does not look for", `{"hash":"1A2B3C4D"}`, "the form is not imported"},
	} {
		want, line := legacyForm(tt.attrs, tt.reason)
		cases = append(cases, textCase{name: "a form " + tt.name, text: wpForm(tt.attrs), want: jsonList(want), lines: []string{line}})
	}
	cases = append(cases,
		textCase{name: "the class and the anchor of a form",
			text: wpBlock("contact-form-7/contact-form-selector", `{"id":31,"className":"wide"}`, `<div id="f" class="wp-block-contact-form-7-contact-form-selector wide">[contact-form-7]</div>`),
			want: jsonList(jsonForm(0, `"className":"wide","anchor":"f"`))},
		textCase{name: "a form whose HTML is not a div", text: wpBlock("contact-form-7/contact-form-selector", `{"id":31}`, `[contact-form-7 id="1a2b3c4"]`),
			want: jsonList(jsonForm(0, ""))},
		textCase{name: "a form that has attributes which are not JSON", text: wpForm(`{bad}`),
			want: jsonList(jsonLegacy(0, wpForm(`{bad}`), "the form is not imported")),
			lines: []string{
				legacyLine("the form is not imported", "/body/0"), `changed: the attributes of the block "contact-form-7/contact-form-selector" are not valid JSON; they are ignored -> /body/0`,
			}},
	)
	runMediaCases(t, cases)
}

func TestRefFindForm(t *testing.T) {
	form := func(id int64, title, hash string) wxr.Item {
		return wxr.Item{ID: id, Type: formType, Title: title, Meta: wxr.MetaList{{Key: "_hash", Value: hash}}}
	}
	imp := New("site.xml", &wxr.Export{Items: []wxr.Item{
		form(1, "A", "aaaaaaa1"),
		{ID: 2, Type: "post", Title: "A", Meta: wxr.MetaList{{Key: "_hash", Value: "bbbbbbb2"}}},
		form(3, "A", "bbbbbbb3"),
		form(4, "B", "bbbbbbb4"),
		{ID: 5, Type: formType, Title: "No hash"},
		form(6, "D", "DDDDDDD6"),
	}}, NewReport(Info{File: "site.xml"}))
	for _, tt := range []struct {
		name        string
		id          int64
		hash, title string
		want        int64
	}{
		{"the first form with the hash, not a post that has it", 0, "bbbbbbb", "", 3},
		{"the hash before the ID and the title", 1, "bbbbbbb4", "A", 4},
		{"a hash in capitals is not one", 0, "BBBBBBB4", "", 0},
		{"a hash in capitals is not one: the ID wins", 4, "BBBBBBB3", "", 4},
		{"the hash is found in a form whose hash is in capitals, as the database matches without regard to case", 0, "ddddddd", "", 6},
		{"a hash of 7 digits", 0, "aaaaaaa", "", 1},
		{"a hash of fewer than 7 digits is not one", 4, "bbbbbb", "", 4},
		{"a hash that is not hexadecimal is not one", 4, "bbbbbbbz", "", 4},
		{"a hash that is longer than the one of the form", 0, "aaaaaaa1x", "", 0},
		{"the ID", 4, "", "", 4},
		{"the ID before the title", 4, "", "A", 4},
		{"the ID of a post that is not a form", 2, "", "", 0},
		{"the title if the post of the ID is not a form", 2, "", "B", 4},
		{"the first form with the title", 0, "", "A", 1},
		{"a form without a hash", 5, "", "", 5},
		{"nothing", 0, "", "", 0},
		{"nothing that is there", 9, "ccccccc", "C", 0},
	} {
		got := int64(0)
		if it := imp.findForm(tt.id, tt.hash, tt.title); it != nil {
			got = it.ID
		}
		if got != tt.want {
			t.Errorf("%s: findForm(%d, %q, %q) is the form %d, want %d", tt.name, tt.id, tt.hash, tt.title, got, tt.want)
		}
	}
}

func TestRefFindFormCollectsTheFormsOnce(t *testing.T) {
	// The forms are collected on the first call, so that a body with many form blocks does not look at every item of the
	// WXR, and at the metadata of each, for each of them.
	imp := New("site.xml", &wxr.Export{Items: []wxr.Item{
		{ID: 1, Type: "post", Title: "A", Meta: wxr.MetaList{{Key: "_hash", Value: "bbbbbbb1"}}},
		{ID: 2, Type: formType, Title: "A", Meta: wxr.MetaList{{Key: "_hash", Value: "bbbbbbb2"}}},
		{ID: 3, Type: formType, Title: "B", Meta: wxr.MetaList{{Key: "_hash", Value: "ccccccc3"}}},
	}}, NewReport(Info{File: "site.xml"}))
	if imp.forms != nil || imp.formsRead {
		t.Fatal("the forms are collected before they are needed")
	}
	form := imp.findForm(0, "bbbbbbb", "")
	if form == nil || form.ID != 2 {
		t.Fatalf("findForm = %+v, want the form 2", form)
	}
	if len(imp.forms) != 2 || imp.forms[0].ID != 2 || imp.forms[1].ID != 3 {
		t.Errorf("the forms are %+v, want the items 2 and 3", imp.forms)
	}
	imp.WXR.Items = nil
	if again := imp.findForm(0, "ccccccc", ""); again == nil || again.ID != 3 {
		t.Errorf("findForm after the items were taken away = %+v, want the form 3, from the forms that were collected", again)
	}
	if again := imp.findForm(0, "", "A"); again != form {
		t.Errorf("findForm by the title = %+v, want the form 2", again)
	}
}

func TestRefSyncedPatterns(t *testing.T) {
	const copiedMsg = "whiteroom has no shared blocks yet (stage 2)"
	copied := func(ref, title, pointer string) string {
		if title != "" {
			ref += " (" + title + ")"
		}
		return fmt.Sprintf("changed: the synced pattern %s is copied here; %s -> %s", ref, copiedMsg, pointer)
	}
	notIn := func(ref int) string { return fmt.Sprintf("the synced pattern %d is not in the WXR", ref) }
	hidden := func(ref int, why string) string {
		return fmt.Sprintf("the synced pattern %d %s; WordPress shows nothing for it", ref, why)
	}
	join := jsonBlock("heading", 0, "", `"level":2`, `[{"type":"text","text":"Join"}]`)
	buy := func(n int) string {
		return fmt.Sprintf(`{"type":"paragraph","id":"<%d>","content":[{"type":"text","text":"Buy "},{"type":"text","text":"now","marks":[{"type":"bold"}]}]}`, n)
	}
	headingOf := func(n int) string { return strings.Replace(join, `"<0>"`, fmt.Sprintf(`"<%d>"`, n), 1) }
	override := `{"ref":60,"content":{"title":{"values":{"content":"Own"}}}}`
	runMediaCases(t, []textCase{
		{name: "a synced pattern is copied", text: wpRef(60),
			want: jsonList(join, buy(1)), lines: []string{copied("60", `"Call to action"`, "/body/0")}},
		{name: "blocks before and after it", text: wpPara("before") + wpRef(60) + wpPara("after"),
			want:  jsonList(jsonPara(0, "before"), headingOf(1), buy(2), jsonPara(3, "after")),
			lines: []string{copied("60", `"Call to action"`, "/body/1")}},
		{name: "a pattern that uses a pattern", text: wpRef(61),
			want: jsonList(jsonPara(0, "Before"), headingOf(1), buy(2), jsonPara(3, "After")),
			lines: []string{
				copied("61", `"Wrapper"`, "/body/0"), copied("60", `"Call to action"`, "/body/1"),
			}},
		{name: "a pattern used twice", text: wpRef(60) + wpRef(60),
			want:  jsonList(join, buy(1), headingOf(2), buy(3)),
			lines: []string{copied("60", `"Call to action"`, "/body/0"), copied("60", `"Call to action"`, "/body/2")}},
		{name: "a pattern without a title, and a title that needs quotes", text: wpRef(71) + wpRef(72),
			want: jsonList(jsonPara(0, "Untitled"), jsonPara(1, "Quoted")),
			lines: []string{
				copied("71", "", "/body/0"), copied("72", "\"Say \\\"hi\\\" 日本語\"", "/body/1"),
			}},
		{name: "a pattern that includes itself: the block that does is a legacy block of the text of the pattern",
			// The pattern has the block without className.
			text: `<!-- wp:block {"ref":64,"className":"x"} /-->`,
			want: jsonList(jsonLegacy(0, wpRef(64), "the synced pattern 64 includes itself")),
			lines: []string{
				copied("64", `"Self"`, "/body/0"), legacyLine("the synced pattern 64 includes itself", "/body/0"),
			}},
		{name: "patterns that include each other", text: wpRef(62),
			want: jsonList(jsonPara(0, "Back"), jsonLegacy(1, wpRef(62), "the synced pattern 62 includes itself")),
			lines: []string{
				copied("62", `"Loop"`, "/body/0"), copied("63", `"Loop back"`, "/body/0"), legacyLine("the synced pattern 62 includes itself", "/body/1"),
			}},
		{name: "a block that is not a synced pattern of the WXR", text: wpRef(68) + wpRef(999) + `<!-- wp:block /-->` + `<!-- wp:block {"ref":"x"} /-->`,
			want: jsonList(jsonLegacy(0, wpRef(68), notIn(68)), jsonLegacy(1, wpRef(999), notIn(999)),
				jsonLegacy(2, `<!-- wp:block /-->`, notIn(0)), jsonLegacy(3, `<!-- wp:block {"ref":"x"} /-->`, notIn(0))),
			lines: []string{
				legacyLine(notIn(68), "/body/0"), legacyLine(notIn(999), "/body/1"), legacyLine(notIn(0), "/body/2"), legacyLine(notIn(0), "/body/3"),
			}},
		{name: "patterns that WordPress does not show: not published, or with a password", text: wpRef(77) + wpRef(78),
			want: jsonList(jsonLegacy(0, wpRef(77), hidden(77, "is not published")), jsonLegacy(1, wpRef(78), hidden(78, "has a password"))),
			lines: []string{
				legacyLine(hidden(77, "is not published"), "/body/0"), legacyLine(hidden(78, "has a password"), "/body/1"),
			}},
		// The paragraphs of the HTML are not made by wpautop in a copy of a pattern, as WordPress does not make them there.
		{name: "a pattern without content, and a pattern of the classic editor", text: wpRef(65) + wpRef(66),
			want:  jsonList(jsonPara(0, "Hello")),
			lines: []string{copied("65", `"Empty"`, "/body"), copied("66", `"Classic"`, "/body/0")}},
		{name: "a pattern that is HTML with a closer and no block is the HTML of the classic editor", text: wpRef(76),
			want:  jsonList(jsonPara(0, "Hello"), jsonPara(1, "World")),
			lines: []string{copied("76", `"Stray"`, "/body/0")}},
		{name: "the overrides of a pattern are not applied", text: `<!-- wp:block ` + override + ` /-->`,
			want:  jsonList(join, buy(1)),
			lines: []string{copied("60", `"Call to action"`, "/body/0"), "skipped: the overrides of the synced pattern 60 are not applied -> /body/0"}},
		{name: "attributes content that are not overrides",
			text: `<!-- wp:block {"ref":71,"content":{}} /-->` + `<!-- wp:block {"ref":71,"content":null} /-->` + `<!-- wp:block {"ref":71,"content":"x"} /-->`,
			want: jsonList(jsonPara(0, "Untitled"), jsonPara(1, "Untitled"), jsonPara(2, "Untitled")),
			lines: []string{
				copied("71", "", "/body/0"), copied("71", "", "/body/1"), copied("71", "", "/body/2"),
			}},
		{name: "a pattern in a group", text: wpGroup("", "div", wpRef(60), wpPara("after")),
			want:  jsonList(jsonHolder("group", 0, "", "", headingOf(1), buy(2), jsonPara(3, "after"))),
			lines: []string{copied("60", `"Call to action"`, "/body/0/content/0")}},
		{name: "columns in a pattern in a column are a group", text: wpColumns(``, wpColumn(``, wpRef(73))),
			want: jsonList(jsonHolder("columns", 0, "", "", jsonHolder("column", 1, "", "",
				jsonHolder("group", 2, "", `"layout":{"type":"grid","columns":1}`, jsonHolder("group", 3, "", "", jsonPara(4, "n")))))),
			lines: []string{
				copied("73", `"Nested columns"`, "/body/0/content/0/content/0"),
				"changed: the columns inside columns are a group here -> /body/0/content/0/content/0",
			}},
		{name: "columns in a pattern at the top are columns", text: wpRef(73),
			want:  jsonList(jsonHolder("columns", 0, "", "", jsonHolder("column", 1, "", "", jsonPara(2, "n")))),
			lines: []string{copied("73", `"Nested columns"`, "/body/0")}},
		{name: "a pattern that has attributes which are not JSON", text: `<!-- wp:block {bad} /-->`,
			want: jsonList(jsonLegacy(0, `<!-- wp:block {bad} /-->`, notIn(0))),
			lines: []string{
				legacyLine(notIn(0), "/body/0"), `changed: the attributes of the block "core/block" are not valid JSON; they are ignored -> /body/0`,
			}},
	})
}

func TestRefSyncedPatternsAreRead(t *testing.T) {
	imp := mediaImport(t)
	convertMedia(t, imp, bodySite(false), nil, wpRef(60)+wpRef(60)+wpRef(61))
	body := imp.patternBodies[60]
	if body == nil || len(imp.patternBodies) != 2 || imp.patternBodies[61] == nil {
		t.Fatalf("the patterns that are read are %v", imp.patternBodies)
	}
	if again := imp.readPattern(imp.WXR.Post(60)); again != body {
		t.Error("the pattern is read again")
	}
	// The blocks of the pattern are not changed by the conversion of the second body.
	res := convertMedia(t, imp, bodySite(false), nil, wpRef(60))
	if got, _ := res.Blocks.MarshalJSON(); !strings.Contains(string(got), `"text":"Join"`) {
		t.Errorf("the blocks are %s", got)
	}
}

func TestRefClassicPatternHasItsHTML(t *testing.T) {
	// A pattern of the classic editor is read as one block of the HTML, as the parser reads HTML between blocks.
	imp := mediaImport(t)
	for _, id := range []int64{66, 76} {
		it := imp.WXR.Post(id)
		body := imp.readPattern(it)
		if len(body.blocks) != 1 || body.blocks[0].HTML() != it.Content {
			t.Errorf("the pattern %d is read as %+v, want one block with the HTML %q", id, body.blocks, it.Content)
		}
	}
}

func TestRefSyncedPatternsAndTheMoreBlock(t *testing.T) {
	// WordPress splits the teaser on the content of the post only, so the read more block of a pattern is not the one of the
	// content: the blocks of the pattern are all there, and the content has none.
	imp := mediaImport(t)
	res := convertMedia(t, imp, bodySite(false), nil, wpPara("x")+wpRef(74)+wpPara("y"))
	if len(res.Blocks) != 4 || res.More != -1 {
		t.Errorf("there are %d blocks and More is %d, want 4 and -1", len(res.Blocks), res.More)
	}
	// The one of the content is after the blocks of the pattern, and what comes before it in the content.
	res = convertMedia(t, mediaImport(t), bodySite(false), nil, wpRef(74)+wpPara("y")+moreBlock+wpPara("z"))
	if len(res.Blocks) != 4 || res.More != 3 {
		t.Errorf("there are %d blocks and More is %d, want 4 and 3", len(res.Blocks), res.More)
	}
}

func TestRefSyncedPatternsAndTheGuard(t *testing.T) {
	const missingMedia = "the conversion does not pass validation: /attrs/media: missing media"
	withConverters(t, map[string]converter{
		"test/image": func(c *bodyConv, _ string, _ *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
			// An image needs a medium, which this one does not have.
			return []content.Block{c.newBlock(content.BlockImage)}, nil
		},
	})
	imp := mediaImport(t)
	res, err := imp.Body(BodyOptions{Site: bodySite(false), Item: bodyItem(wpPara("x") + wpRef(75)), Target: bodyTarget, Key: bodyKey})
	if err != nil {
		t.Fatal(err)
	}
	checkBody(t, imp, bodySite(false), res)
	// The legacy block holds the text of the pattern, and not the block of the body that uses it.
	if len(res.Blocks) != 2 || res.Blocks[1].Type != content.BlockLegacy || res.Blocks[1].Attrs.HTML != imageBlock || res.Blocks[1].Attrs.Reason != missingMedia {
		t.Errorf("the blocks are %+v", res.Blocks)
	}

	// Two copies of the pattern are two conversions: each keeps its own legacy block.
	imp = mediaImport(t)
	res, err = imp.Body(BodyOptions{Site: bodySite(false), Item: bodyItem(wpRef(75) + wpRef(75)), Target: bodyTarget, Key: bodyKey})
	if err != nil {
		t.Fatal(err)
	}
	checkBody(t, imp, bodySite(false), res)
	if len(res.Blocks) != 2 || slices.ContainsFunc(res.Blocks, func(b content.Block) bool {
		return b.Type != content.BlockLegacy || b.Attrs.HTML != imageBlock || b.Attrs.Reason != missingMedia
	}) {
		t.Errorf("the blocks are %+v", res.Blocks)
	}
}

func TestRefPatternLimits(t *testing.T) {
	copied := func(ref, title, where string) string {
		return fmt.Sprintf("changed: the synced pattern %s (%q) is copied here; whiteroom has no shared blocks yet (stage 2) -> %s", ref, title, where)
	}
	stop := func(blocks int) string {
		return fmt.Sprintf("skipped: the rest of the content is not imported: the conversion reads at most %d blocks and %d bytes of HTML -> /body", blocks, maxHTMLBytes)
	}
	count := func(res *BodyResult, typ content.BlockType) int {
		n := 0
		for _, b := range res.Blocks {
			if b.Type == typ {
				n++
			}
		}
		return n
	}

	t.Run("the blocks that are copied count as the blocks of the content", func(t *testing.T) {
		lowerLimit(t, &maxWPBlocks, 6)
		imp := mediaImport(t)
		res := convertMedia(t, imp, bodySite(false), nil, wpRef(67)+wpRef(67)+wpRef(67))
		// The first copy has 3 paragraphs; the second has the first of its 3 when the limit is reached at the second.
		if n := count(res, content.BlockParagraph); n != 4 || len(res.Blocks) != 4 {
			t.Errorf("there are %d paragraphs among %d blocks, want 4", n, len(res.Blocks))
		}
		wantLines(t, imp.Report, bodyLines(copied("67", "Three", "/body/0"), copied("67", "Three", "/body/3"), stop(6)))
	})

	t.Run("the HTML that is copied counts as the HTML that the conversion reads", func(t *testing.T) {
		pattern := mediaExport().Post(60).Content
		lowerLimit(t, &maxHTMLBytes, 2*len(pattern)+len(pattern)/2)
		imp := mediaImport(t)
		res := convertMedia(t, imp, bodySite(false), nil, strings.Repeat(wpRef(60), 10))
		if n := count(res, content.BlockHeading); n < 1 || n > 2 {
			t.Errorf("there are %d headings, want 1 or 2", n)
		}
		legacy := 0
		for _, b := range res.Blocks {
			if b.Type == content.BlockLegacy {
				legacy++
				if want := fmt.Sprintf("the conversion has read more than %d bytes of HTML", maxHTMLBytes); b.Attrs.Reason != want {
					t.Errorf("the reason is %q", b.Attrs.Reason)
				}
			}
		}
		stops := 0
		for _, e := range imp.Report.Entries() {
			if strings.HasPrefix(e.Message, "the rest of the content is not imported") {
				stops++
			}
		}
		if legacy != 1 || stops != 1 {
			t.Errorf("there are %d legacy blocks and %d notes that the conversion stopped, want 1 each", legacy, stops)
		}
	})

	t.Run("the columns of a copy count too", func(t *testing.T) {
		lowerLimit(t, &maxWPBlocks, 10)
		imp := mediaImport(t)
		res := convertMedia(t, imp, bodySite(false), nil, wpRef(69)+wpRef(69))
		// The first copy takes 8 blocks (the columns, 3 columns, 3 paragraphs and the block that uses the pattern), the
		// second takes 2, and its first column is the 11th.
		if len(res.Blocks) != 1 || len(res.Blocks[0].Children) != 3 {
			t.Errorf("the blocks are %+v", res.Blocks)
		}
		wantLines(t, imp.Report, bodyLines(copied("69", "Columns", "/body/0"), copied("69", "Columns", "/body"), stop(10)))
	})

	t.Run("the buttons of a copy count too", func(t *testing.T) {
		lowerLimit(t, &maxWPBlocks, 8)
		imp := mediaImport(t)
		res := convertMedia(t, imp, bodySite(false), nil, wpRef(70)+wpRef(70)+wpRef(70))
		if len(res.Blocks) != 2 || len(res.Blocks[0].Children) != 3 || len(res.Blocks[1].Children) != 1 {
			t.Errorf("the blocks are %+v", res.Blocks)
		}
		wantLines(t, imp.Report, bodyLines(copied("70", "Buttons", "/body/0"), copied("70", "Buttons", "/body/1"), stop(8)))
	})

	t.Run("a pattern that has more blocks than the limit is a legacy block", func(t *testing.T) {
		lowerLimit(t, &maxWPBlocks, 2)
		imp := mediaImport(t)
		res := convertMedia(t, imp, bodySite(false), nil, wpRef(67))
		reason := "the synced pattern 67 has more than 2 blocks"
		if len(res.Blocks) != 1 || res.Blocks[0].Type != content.BlockLegacy || res.Blocks[0].Attrs.Reason != reason {
			t.Errorf("the blocks are %+v", res.Blocks)
		}
		wantLines(t, imp.Report, bodyLines(legacyLine(reason, "/body/0")))
	})
}

func TestRefThemePatterns(t *testing.T) {
	text := wpBlock("pattern", `{"slug":"theme/hero"}`, ``) + `<!-- wp:pattern /-->`
	runMediaCases(t, []textCase{
		{name: "a pattern of the theme", text: text,
			want: jsonList(
				jsonLegacy(0, wpBlock("pattern", `{"slug":"theme/hero"}`, ``), `the pattern "theme/hero" of the theme is not in the WXR`),
				jsonLegacy(1, `<!-- wp:pattern /-->`, `the pattern "" of the theme is not in the WXR`)),
			lines: []string{
				legacyLine(`the pattern "theme/hero" of the theme is not in the WXR`, "/body/0"),
				legacyLine(`the pattern "" of the theme is not in the WXR`, "/body/1"),
			}},
	})
}

func TestRefConvertersAreRegistered(t *testing.T) {
	for _, name := range []string{"contact-form-7/contact-form-selector", "core/block", "core/pattern"} {
		if converters[name] == nil {
			t.Errorf("%s has no converter", name)
		}
		if dynamicBlock(name) {
			t.Errorf("%s is a dynamic block", name)
		}
	}
}
