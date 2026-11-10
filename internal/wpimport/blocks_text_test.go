package wpimport

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/html"
)

var idMark = regexp.MustCompile(`<(\d+)>`)

// withIDs puts the n-th ID of bodyKey where the expected JSON of a test has <n>.
func withIDs(s string) string {
	return idMark.ReplaceAllStringFunc(s, func(m string) string {
		n, _ := strconv.Atoi(m[1 : len(m)-1])
		return bodyIDs[n]
	})
}

func jsonList(items ...string) string { return "[" + strings.Join(items, ",") + "]" }

// jsonPara is the JSON of a paragraph with the n-th ID and the text, which has nothing in it to escape in JSON but quotes.
func jsonPara(n int, text string) string {
	if text == "" {
		return fmt.Sprintf(`{"type":"paragraph","id":"<%d>"}`, n)
	}
	return fmt.Sprintf(`{"type":"paragraph","id":"<%d>","content":[{"type":"text","text":%s}]}`, n, strconv.Quote(text))
}

// jsonLegacy is the JSON of the legacy block with the n-th ID, whose HTML and reason have no more to escape than quotes.
func jsonLegacy(n int, html, reason string) string {
	return fmt.Sprintf(`{"type":"legacy","id":"<%d>","attrs":{"html":%s,"reason":%s}}`, n, strconv.Quote(html), strconv.Quote(reason))
}

// jsonItem is the JSON of a list item with the blocks.
func jsonItem(blocks ...string) string {
	return `{"type":"listItem","content":` + jsonList(blocks...) + `}`
}

// jsonBlock is the JSON of a block of the type typ with the n-th ID, the keys before the attributes (such as
// `"variant":"x"`), the attributes (the inside of the object), and the content, each empty if there is none.
func jsonBlock(typ string, n int, keys, attrs, content string) string {
	s := fmt.Sprintf(`{"type":%q,"id":"<%d>"`, typ, n)
	if keys != "" {
		s += "," + keys
	}
	if attrs != "" {
		s += `,"attrs":{` + attrs + `}`
	}
	if content != "" {
		s += `,"content":` + content
	}
	return s + "}"
}

func jsonCell(attrs, text string) string {
	s := `{"type":"tableCell"`
	if attrs != "" {
		s += `,"attrs":{` + attrs + `}`
	}
	if text != "" {
		s += `,"content":[{"type":"text","text":` + strconv.Quote(text) + `}]`
	}
	return s + "}"
}

// jsonRow is a table row in the section (head, foot, or "" for the body) with the cells.
func jsonRow(section string, cells ...string) string {
	s := `{"type":"tableRow"`
	if section != "" {
		s += `,"attrs":{"section":"` + section + `"}`
	}
	return s + `,"content":` + jsonList(cells...) + `}`
}

// textCase is a body, the blocks that it must become (as JSON, with <n> for the IDs), and the lines of the report.
type textCase struct {
	name      string
	text      string
	settings  *Settings
	allowHTML bool
	want      string
	lines     []string
}

// noGuard checks that the conversion needed no repair: Body notes what its guard replaces or takes out.
func noGuard(t *testing.T, imp *Import) {
	t.Helper()
	for _, e := range imp.Report.Entries() {
		if strings.Contains(e.Message, "the conversion does not pass validation") ||
			strings.HasPrefix(e.Message, "the block is not imported:") || strings.HasPrefix(e.Message, "the content is not imported:") {
			t.Errorf("the guard repaired the blocks: %s", e.Message)
		}
	}
}

// textBody converts text as the content of an item, and checks what holds for every result: the blocks pass Validate as
// those of an import, and the guard of Body had nothing to do.
func textBody(t *testing.T, site *content.Site, settings *Settings, text string) (*BodyResult, *Import) {
	t.Helper()
	imp := bodyImport()
	res, err := imp.Body(BodyOptions{Site: site, Settings: settings, Item: bodyItem(text), Target: bodyTarget, Key: bodyKey})
	if err != nil {
		t.Fatalf("Body = %v", err)
	}
	checkBody(t, imp, site, res)
	checkNoPieces(t, text, imp, res)
	noGuard(t, imp)
	return res, imp
}

func runTextCases(t *testing.T, cases []textCase) {
	t.Helper()
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			res, imp := textBody(t, bodySite(tt.allowHTML), tt.settings, tt.text)
			got, err := res.Blocks.MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
			if want := withIDs(tt.want); string(got) != want {
				t.Errorf("the blocks are\n%s\nwant\n%s", got, want)
			}
			wantLines(t, imp.Report, bodyLines(tt.lines...))
		})
	}
}

// wpBlock writes the block name with the attributes attrs and the markup, on lines as the block editor writes it.
func wpBlock(name, attrs, markup string) string {
	if attrs != "" {
		attrs += " "
	}
	return fmt.Sprintf("<!-- wp:%s %s-->\n%s\n<!-- /wp:%s -->", name, attrs, markup, name)
}

// wpPara is a paragraph of WordPress with the text.
func wpPara(text string) string { return wpBlock("paragraph", "", "<p>"+text+"</p>") }

const lostSuffix = "; whiteroom has no such settings"

// lostLine is the line of the report for settings that are lost.
func lostLine(items, block, pointer string) string {
	return fmt.Sprintf("skipped: lost %s of the block %q%s -> %s", items, block, lostSuffix, pointer)
}

func TestTextParagraphs(t *testing.T) {
	// A control character is removed from the text when Body normalizes it, and may leave nothing.
	ctl := string(rune(0x11))
	notP := notAsWritten + "<p>"
	img := `<p>a <img src="x.png"></p>`
	notHeading := notAsWritten + "<h1>, <h2>, <h3>, <h4>, <h5> or <h6>"
	runTextCases(t, []textCase{
		{name: "a paragraph", text: wpPara(`Hello <strong>World</strong>`),
			want: `[{"type":"paragraph","id":"<0>","content":[{"type":"text","text":"Hello "},{"type":"text","text":"World","marks":[{"type":"bold"}]}]}]`},
		{name: "text in Japanese", text: wpPara(`日本語の段落です。 <strong>太字</strong>`),
			want: jsonList(`{"type":"paragraph","id":"<0>","content":[{"type":"text","text":"日本語の段落です。 "},{"type":"text","text":"太字","marks":[{"type":"bold"}]}]}`)},
		{name: "an empty paragraph", text: wpPara(``), want: jsonList(jsonPara(0, ""))},
		{name: "a paragraph of a control character", text: wpPara(ctl), want: jsonList(jsonPara(0, ""))},
		{name: "the alignment of the text, in the three places where it is",
			text: wpBlock("paragraph", `{"align":"center"}`, `<p class="has-text-align-center">a</p>`) +
				wpBlock("paragraph", `{"style":{"typography":{"textAlign":"right"}}}`, `<p class="has-text-align-right">b</p>`) +
				wpBlock("paragraph", `{"textAlign":"left"}`, `<p class="has-text-align-left">c</p>`),
			want: jsonList(
				jsonBlock("paragraph", 0, "", `"textAlign":"center"`, `[{"type":"text","text":"a"}]`),
				jsonBlock("paragraph", 1, "", `"textAlign":"right"`, `[{"type":"text","text":"b"}]`),
				jsonBlock("paragraph", 2, "", `"textAlign":"left"`, `[{"type":"text","text":"c"}]`))},
		{name: "what a paragraph has that whiteroom has not",
			text: wpBlock("paragraph", `{"align":"wide","dropCap":true,"direction":"rtl"}`, `<p class="has-drop-cap">x</p>`) +
				wpBlock("paragraph", `{"dropCap":false,"direction":""}`, `<p>y</p>`),
			want:  jsonList(jsonPara(0, "x"), jsonPara(1, "y")),
			lines: []string{lostLine("dropCap, direction, align", "core/paragraph", "/body/0")}},
		{name: "justified text",
			text: wpBlock("paragraph", `{"align":"justify"}`, `<p>a</p>`) + wpBlock("paragraph", `{"textAlign":"justify"}`, `<p>b</p>`),
			want: jsonList(jsonPara(0, "a"), jsonPara(1, "b")),
			lines: []string{
				lostLine("textAlign", "core/paragraph", "/body/0"), lostLine("textAlign", "core/paragraph", "/body/1"),
			}},
		{name: "the style, the anchor, the class and the block style", settings: styleSettings(),
			text: wpBlock("paragraph", `{"className":"is-style-fancy lead","textColor":"vivid-red","fontSize":"huge","style":{"spacing":{"padding":{"top":"1rem"}}}}`,
				`<p id="intro" class="lead has-vivid-red-color has-text-color has-huge-font-size" style="padding-top:1rem">Hi</p>`),
			want: jsonList(jsonBlock("paragraph", 0,
				`"variant":"fancy-theme","className":"lead","anchor":"intro",`+
					`"style":{"textColor":{"preset":"red"},"fontSize":{"preset":"x-large"},"padding":{"top":{"value":"1rem"}}}`,
				"", `[{"type":"text","text":"Hi"}]`))},
		{name: "colors written as values and as references to presets", settings: styleSettings(),
			text: wpBlock("paragraph", `{"style":{"color":{"text":"#cf2e2e","background":"var:preset|color|vivid-red"},"typography":{"fontSize":"1.25rem"}}}`,
				`<p class="has-text-color has-background" style="color:#cf2e2e">Hi</p>`),
			want: jsonList(jsonBlock("paragraph", 0, `"style":{"textColor":{"value":"#cf2e2e"},"backgroundColor":{"preset":"red"},"fontSize":{"value":"1.25rem"}}`,
				"", `[{"type":"text","text":"Hi"}]`))},
		{name: "a style that the settings do not import", settings: styleSettings(),
			text: wpBlock("paragraph", `{"className":"is-style-gone","textColor":"gone"}`, `<p>Hi</p>`),
			want: jsonList(jsonPara(0, "Hi")),
			lines: []string{
				`skipped: the block style "gone" is not imported: blocks.variants maps it to nothing -> /body/0`,
				`skipped: the color preset "gone" is not imported: blocks.presets.color maps it to nothing -> /body/0`,
			}},
		{name: "a break, and an element that is not kept", text: wpPara(`a<br>b <kbd>c</kbd>`),
			want:  `[{"type":"paragraph","id":"<0>","content":[{"type":"text","text":"a"},{"type":"hardBreak"},{"type":"text","text":"b c"}]}]`,
			lines: []string{`changed: kept only the text of <kbd> -> /body/0`}},
		{name: "a paragraph that is not as WordPress writes it", text: wpBlock("paragraph", "", `<div>x</div>`),
			want:  jsonList(jsonLegacy(0, wpBlock("paragraph", "", `<div>x</div>`), notP)),
			lines: []string{legacyLine(notP, "/body/0")}},
		{name: "a paragraph with an image is a legacy block", text: wpBlock("paragraph", "", img),
			want:  jsonList(jsonLegacy(1, wpBlock("paragraph", "", img), "the text has <img>, which rich text cannot have")),
			lines: []string{legacyLine("the text has <img>, which rich text cannot have", "/body/0")}},

		// Verses.
		{name: "a verse", text: wpBlock("verse", "", "<pre class=\"wp-block-verse\">Roses are red,\n  Violets are blue</pre>"),
			want: `[{"type":"paragraph","id":"<0>","content":[{"type":"text","text":"Roses are red,"},{"type":"hardBreak"},{"type":"text","text":"  Violets are blue"}]}]`},
		{name: "a verse with the alignment", text: wpBlock("verse", `{"textAlign":"center","align":"full"}`, `<pre class="wp-block-verse has-text-align-center">a<br>b</pre>`),
			want:  `[{"type":"paragraph","id":"<0>","attrs":{"textAlign":"center"},"content":[{"type":"text","text":"a"},{"type":"hardBreak"},{"type":"text","text":"b"}]}]`,
			lines: []string{lostLine("align", "core/verse", "/body/0")}},
		{name: "a verse that is not a pre", text: wpBlock("verse", "", `<p>x</p>`),
			want:  jsonList(jsonLegacy(0, wpBlock("verse", "", `<p>x</p>`), notAsWritten+"<pre>")),
			lines: []string{legacyLine(notAsWritten+"<pre>", "/body/0")}},

		// Headings.
		{name: "headings", text: wpBlock("heading", "", `<h2 class="wp-block-heading">Two</h2>`) +
			wpBlock("heading", `{"level":3,"textAlign":"right"}`, `<h3 class="wp-block-heading has-text-align-right" id="three">Three</h3>`),
			want: jsonList(
				jsonBlock("heading", 0, "", `"level":2`, `[{"type":"text","text":"Two"}]`),
				jsonBlock("heading", 1, `"anchor":"three"`, `"level":3,"textAlign":"right"`, `[{"type":"text","text":"Three"}]`))},
		{name: "the levels from 2 to 6", text: wpBlock("heading", `{"level":4}`, `<h4>Four</h4>`) + wpBlock("heading", `{"level":6}`, `<h6>Six</h6>`),
			want: jsonList(
				jsonBlock("heading", 0, "", `"level":4`, `[{"type":"text","text":"Four"}]`),
				jsonBlock("heading", 1, "", `"level":6`, `[{"type":"text","text":"Six"}]`))},
		{name: "a heading of level 1 is of level 2", text: wpBlock("heading", `{"level":1}`, `<h1 class="wp-block-heading">Title</h1>`),
			want:  jsonList(jsonBlock("heading", 0, "", `"level":2`, `[{"type":"text","text":"Title"}]`)),
			lines: []string{`changed: the heading level 1 is 2 here; h1 is for the title -> /body/0`}},
		{name: "an empty heading is left out", text: wpBlock("heading", "", `<h2 class="wp-block-heading"></h2>`) +
			wpBlock("heading", "", `<h2 id="gone"> <!-- x --> </h2>`) + wpBlock("heading", `{"level":3}`, `<h3 id="gone">x</h3>`),
			// The headings that are left out took the IDs 0 and 1, and not the anchor.
			want:  jsonList(jsonBlock("heading", 2, `"anchor":"gone"`, `"level":3`, `[{"type":"text","text":"x"}]`)),
			lines: []string{`skipped: the empty heading is removed -> /body`, `skipped: the empty heading is removed -> /body`}},
		{name: "a heading of nothing but a control character is left out", text: wpBlock("heading", "", "<h2>"+ctl+"</h2>") + wpBlock("heading", "", "<h3>x"+ctl+"y</h3>"),
			want:  jsonList(jsonBlock("heading", 1, "", `"level":3`, `[{"type":"text","text":"xy"}]`)),
			lines: []string{`skipped: the empty heading is removed -> /body`}},
		{name: "a heading with the alignment of the block", text: wpBlock("heading", `{"level":2,"align":"wide"}`, `<h2>x</h2>`),
			want:  jsonList(jsonBlock("heading", 0, "", `"level":2`, `[{"type":"text","text":"x"}]`)),
			lines: []string{lostLine("align", "core/heading", "/body/0")}},
		{name: "a heading that is not a heading", text: wpBlock("heading", "", `<p>x</p>`),
			want:  jsonList(jsonLegacy(0, wpBlock("heading", "", `<p>x</p>`), notHeading)),
			lines: []string{legacyLine(notHeading, "/body/0")}},
	})
}

func TestTextLists(t *testing.T) {
	const (
		oneList  = "a list item has blocks other than one list"
		notItems = "the list has a block other than list items"
		notLi    = notAsWritten + "<li>"
	)
	item := func(text string) string { return wpBlock("list-item", "", "<li>"+text+"</li>") }
	// holding is a list item with the list in it, as WordPress 6.1 writes it.
	holding := func(text, list string) string {
		return "<!-- wp:list-item -->\n<li>" + text + list + "</li>\n<!-- /wp:list-item -->"
	}
	list := func(attrs, tag, items string) string {
		return wpBlock("list", attrs, "<"+tag+` class="wp-block-list">`+items+"</"+tag+">")
	}
	listOf := func(id int, attrs string, items ...string) string {
		return jsonBlock("list", id, "", attrs, jsonList(items...))
	}
	nested := jsonList(listOf(0, "",
		jsonItem(jsonPara(1, "One")),
		jsonItem(jsonPara(2, "Two"), listOf(3, "", jsonItem(jsonPara(4, "Nested"))))))
	itemWithTwo := holding("One", list("", "ul", item("a"))+list("", "ul", item("b")))
	itemWithBlock := "<!-- wp:list-item -->\n<li>One<!-- wp:paragraph --><p>x</p><!-- /wp:paragraph --></li>\n<!-- /wp:list-item -->"
	oldWithTwo := wpBlock("list", "", `<ul><li>One<ul><li>a</li></ul><ol><li>b</li></ol></li></ul>`)
	runTextCases(t, []textCase{
		{name: "a list", text: list("", "ul", item("One")+item("Two")),
			want: jsonList(listOf(0, "", jsonItem(jsonPara(1, "One")), jsonItem(jsonPara(2, "Two"))))},
		{name: "a list with a list in an item, as WordPress 6.1 writes it",
			text: list("", "ul", item("One")+holding("Two", list("", "ul", item("Nested")))), want: nested},
		{name: "an ordered list that starts at 3", text: list(`{"ordered":true,"start":3}`, "ol", item("One")),
			want: jsonList(listOf(0, `"ordered":true,"start":3`, jsonItem(jsonPara(1, "One"))))},
		{name: "the start of the element, if the block has none",
			text: wpBlock("list", `{"ordered":true}`, `<ol start="5" class="wp-block-list">`+item("One")+`</ol>`),
			want: jsonList(listOf(0, `"ordered":true,"start":5`, jsonItem(jsonPara(1, "One"))))},
		{name: "the start of the block, over that of the element",
			text: wpBlock("list", `{"ordered":true,"start":2}`, `<ol start="5" class="wp-block-list">`+item("One")+`</ol>`),
			want: jsonList(listOf(0, `"ordered":true,"start":2`, jsonItem(jsonPara(1, "One"))))},
		{name: "a list that is reversed, has a type, or starts below 1",
			text: wpBlock("list", `{"ordered":true,"reversed":true,"type":"a","start":0}`, `<ol reversed type="a" start="0" class="wp-block-list">`+item("One")+`</ol>`) +
				wpBlock("list", `{"ordered":true}`, `<ol reversed start="-3" class="wp-block-list">`+item("Two")+`</ol>`),
			want: jsonList(
				listOf(0, `"ordered":true`, jsonItem(jsonPara(1, "One"))),
				listOf(2, `"ordered":true`, jsonItem(jsonPara(3, "Two")))),
			lines: []string{
				lostLine("start, reversed, type", "core/list", "/body/0"), lostLine("start, reversed", "core/list", "/body/1"),
			}},
		{name: "a start of the element that is below 1",
			text: wpBlock("list", `{"ordered":true}`, `<ol start="0" class="wp-block-list">`+item("One")+`</ol>`) +
				wpBlock("list", `{"ordered":true}`, `<ol start="x" class="wp-block-list">`+item("Two")+`</ol>`),
			want: jsonList(
				listOf(0, `"ordered":true`, jsonItem(jsonPara(1, "One"))),
				listOf(2, `"ordered":true`, jsonItem(jsonPara(3, "Two")))),
			lines: []string{lostLine("start", "core/list", "/body/0"), lostLine("start", "core/list", "/body/1")}},
		{name: "a start on a list that is not ordered is nothing",
			text: wpBlock("list", `{"start":4}`, `<ul start="4" class="wp-block-list">`+item("One")+`</ul>`),
			want: jsonList(listOf(0, "", jsonItem(jsonPara(1, "One"))))},
		{name: "the settings of the list", settings: styleSettings(),
			text: wpBlock("list", `{"className":"is-style-fancy","textColor":"vivid-red"}`, `<ul id="l" class="wp-block-list">`+item("One")+`</ul>`),
			want: jsonList(jsonBlock("list", 0, `"variant":"fancy-theme","anchor":"l","style":{"textColor":{"preset":"red"}}`, "", jsonList(jsonItem(jsonPara(1, "One")))))},
		{name: "the settings of an item go to its paragraph", settings: styleSettings(),
			text: list("", "ul", wpBlock("list-item", `{"className":"is-style-fancy extra","textColor":"vivid-red"}`, `<li id="first">One</li>`)+
				wpBlock("list-item", `{bad}`, `<li></li>`)),
			want: jsonList(listOf(0, "",
				jsonItem(jsonBlock("paragraph", 1, `"variant":"fancy-theme","className":"extra","anchor":"first","style":{"textColor":{"preset":"red"}}`, "", `[{"type":"text","text":"One"}]`)),
				jsonItem(jsonPara(2, "")))),
			lines: []string{`changed: the attributes of the block "core/list-item" are not valid JSON; they are ignored -> /body/0/content/1/content/0`}},
		{name: "marks and breaks in an item", text: list("", "ul", item(`<strong>a</strong><br>b`)),
			want: jsonList(listOf(0, "", jsonItem(
				`{"type":"paragraph","id":"<1>","content":[{"type":"text","text":"a","marks":[{"type":"bold"}]},{"type":"hardBreak"},{"type":"text","text":"b"}]}`)))},
		{name: "a list in an item that has attributes which are not JSON",
			text: list("", "ul", holding("One", wpBlock("list", `{bad}`, `<ul class="wp-block-list">`+item("a")+`</ul>`))),
			want: jsonList(listOf(0, "", jsonItem(jsonPara(1, "One"), listOf(2, "", jsonItem(jsonPara(3, "a")))))),
			lines: []string{
				`changed: the attributes of the block "core/list" are not valid JSON; they are ignored -> /body/0/content/0/content/1`,
			}},
		{name: "a note about the text of an item is at its paragraph", text: list("", "ul", item("a")+holding("b", list("", "ul", item("c <kbd>d</kbd>")))),
			want: jsonList(listOf(0, "", jsonItem(jsonPara(1, "a")),
				jsonItem(jsonPara(2, "b"), listOf(3, "", jsonItem(jsonPara(4, "c d")))))),
			lines: []string{`changed: kept only the text of <kbd> -> /body/0/content/1/content/1/content/0/content/0`}},

		// Lists of WordPress before 6.1, which have no list item blocks.
		{name: "an old list", text: wpBlock("list", "", `<ul><li>One</li><li>Two<ul><li>Nested</li></ul></li></ul>`), want: nested},
		{name: "an old ordered list with an ordered list in an item",
			text: wpBlock("list", `{"ordered":true}`, `<ol start="2"><li>One<ol start="7"><li>a</li></ol></li></ol>`),
			want: jsonList(listOf(0, `"ordered":true,"start":2`, jsonItem(jsonPara(1, "One"), listOf(2, `"ordered":true,"start":7`, jsonItem(jsonPara(3, "a"))))))},
		{name: "an old list with comments and white space between the items", text: wpBlock("list", "", "<ul>\n  <li>a</li>\n<!-- x -->\n  <li>b</li>\n</ul>"),
			want: jsonList(listOf(0, "", jsonItem(jsonPara(1, "a")), jsonItem(jsonPara(2, "b"))))},
		{name: "an old list with an item that is empty", text: wpBlock("list", "", `<ul><li></li></ul>`),
			want: jsonList(listOf(0, "", jsonItem(jsonPara(1, ""))))},
		{name: "an old list with an item that has block elements", text: wpBlock("list", "", `<ul><li><p>a</p><p>b</p></li></ul>`),
			want:  jsonList(listOf(0, "", jsonItem(`{"type":"paragraph","id":"<1>","content":[{"type":"text","text":"a"},{"type":"hardBreak"},{"type":"text","text":"b"}]}`))),
			lines: []string{`changed: kept only the text of <p> -> /body/0/content/0/content/0`}},

		// Lists that are not as WordPress writes them.
		{name: "a list item with two lists", text: list("", "ul", itemWithTwo),
			want:  jsonList(jsonLegacy(1, list("", "ul", itemWithTwo), oneList)),
			lines: []string{legacyLine(oneList, "/body/0")}},
		{name: "an old list item with two lists", text: oldWithTwo,
			want:  jsonList(jsonLegacy(1, oldWithTwo, oneList)),
			lines: []string{legacyLine(oneList, "/body/0")}},
		{name: "a list item with a block that is not a list", text: list("", "ul", itemWithBlock),
			want:  jsonList(jsonLegacy(1, list("", "ul", itemWithBlock), oneList)),
			lines: []string{legacyLine(oneList, "/body/0")}},
		{name: "a list with a block that is not a list item", text: list("", "ul", wpPara("x")),
			want:  jsonList(jsonLegacy(1, list("", "ul", wpPara("x")), notItems)),
			lines: []string{legacyLine(notItems, "/body/0")}},
		{name: "an old list with an element that is not an item", text: wpBlock("list", "", `<ul><div>x</div></ul>`),
			want:  jsonList(jsonLegacy(1, wpBlock("list", "", `<ul><div>x</div></ul>`), notLi)),
			lines: []string{legacyLine(notLi, "/body/0")}},
		{name: "an old list with text that is not in an item", text: wpBlock("list", "", `<ul>x<li>a</li></ul>`),
			want:  jsonList(jsonLegacy(1, wpBlock("list", "", `<ul>x<li>a</li></ul>`), notLi)),
			lines: []string{legacyLine(notLi, "/body/0")}},
		{name: "an item that is not a li", text: list("", "ul", wpBlock("list-item", "", "<p>x</p>")),
			want:  jsonList(jsonLegacy(1, list("", "ul", wpBlock("list-item", "", "<p>x</p>")), notLi)),
			lines: []string{legacyLine(notLi, "/body/0")}},
		{name: "an item with an image", text: list("", "ul", item(`Item <img src="x.png">`)),
			want:  jsonList(jsonLegacy(2, list("", "ul", item(`Item <img src="x.png">`)), "the text has <img>, which rich text cannot have")),
			lines: []string{legacyLine("the text has <img>, which rich text cannot have", "/body/0")}},
		{name: "a legacy block gives back the anchor of its items", text: list("", "ul", wpBlock("list-item", "", `<li id="a">x <img src="a.png"></li>`)) +
			wpBlock("heading", "", `<h2 id="a">After</h2>`),
			want: jsonList(
				jsonLegacy(2, list("", "ul", wpBlock("list-item", "", `<li id="a">x <img src="a.png"></li>`)), "the text has <img>, which rich text cannot have"),
				jsonBlock("heading", 3, `"anchor":"a"`, `"level":2`, `[{"type":"text","text":"After"}]`)),
			lines: []string{legacyLine("the text has <img>, which rich text cannot have", "/body/0")}},

		// Lists without items.
		{name: "a list without items is left out", text: list("", "ul", "") + list("", "ol", "\n"),
			want:  `[]`,
			lines: []string{"skipped: the empty list is removed -> /body", "skipped: the empty list is removed -> /body"}},
		{name: "a list without items in an item is left out", text: list("", "ul", holding("One", list("", "ul", ""))),
			// The list that was left out took the ID 2.
			want:  jsonList(listOf(0, "", jsonItem(jsonPara(1, "One")))),
			lines: []string{"skipped: the empty list is removed -> /body/0/content/0/content/0"}},
		{name: "an old list without items in an item is left out", text: wpBlock("list", "", `<ul><li>One<ul></ul></li></ul>`),
			want:  jsonList(listOf(0, "", jsonItem(jsonPara(1, "One")))),
			lines: []string{"skipped: the empty list is removed -> /body/0/content/0/content/0"}},
		{name: "HTML in a list outside its items", text: wpBlock("list", "", `<ul class="wp-block-list">x<!-- wp:list-item --><li>One</li><!-- /wp:list-item --></ul>`),
			want:  jsonList(listOf(0, "", jsonItem(jsonPara(1, "One")))),
			lines: []string{`skipped: the HTML of the block "core/list" outside its inner blocks is not imported -> /body/0`}},
	})
}

// nestedLists writes n lists, each in an item of the one before, as WordPress 6.1 writes them, or as it wrote them before
// if old is set.
func nestedLists(n int, old bool) string {
	if old {
		return "<!-- wp:list -->" + strings.Repeat("<ul><li>x", n) + strings.Repeat("</li></ul>", n) + "<!-- /wp:list -->"
	}
	return strings.Repeat(`<!-- wp:list --><ul class="wp-block-list"><!-- wp:list-item --><li>x`, n) +
		strings.Repeat(`</li><!-- /wp:list-item --></ul><!-- /wp:list -->`, n)
}

func TestTextListsDepth(t *testing.T) {
	// A list at depth d has its items at d+1, and the paragraphs and lists in them at d+2: 15 lists fill the 32 levels, and
	// with the 16th the whole list is a legacy block.
	lists := func(bs content.Blocks) int {
		n := 0
		for len(bs) == 1 {
			n++
			item := bs[0].Children[len(bs[0].Children)-1]
			bs = nil
			if len(item.Children) == 2 {
				bs = item.Children[1:]
			}
		}
		return n
	}
	for _, old := range []bool{false, true} {
		t.Run(fmt.Sprintf("old=%v", old), func(t *testing.T) {
			for _, n := range []int{1, 2, 15} {
				res, imp := textBody(t, bodySite(false), nil, nestedLists(n, old))
				if len(res.Blocks) != 1 || lists(res.Blocks) != n {
					t.Errorf("%d nested lists became %d blocks with %d lists", n, len(res.Blocks), lists(res.Blocks))
				}
				wantLines(t, imp.Report, nil)
			}
			for _, n := range []int{16, 17, 500} {
				res, imp := textBody(t, bodySite(false), nil, nestedLists(n, old))
				if len(res.Blocks) != 1 || res.Blocks[0].Type != content.BlockLegacy || res.Blocks[0].Attrs.Reason != deepReason {
					t.Errorf("%d nested lists became %+v, want a legacy block", n, res.Blocks)
				}
				wantLines(t, imp.Report, bodyLines(legacyLine(deepReason, "/body/0")))
			}
		})
	}

	// A list is at depth 1 in a body, but deeper in groups: in 29 groups it is at depth 30, and its paragraph at 32; in 30
	// groups, its paragraph would be at 33.
	withConverters(t, map[string]converter{"test/group": testContainer(content.BlockGroup, 1)})
	inGroups := func(groups int) content.Block {
		text := strings.Repeat(groupOpen, groups) + wpBlock("list", "", `<ul><li>x</li></ul>`) + strings.Repeat(groupClose, groups)
		res, _ := textBody(t, bodySite(false), nil, text)
		g := res.Blocks[0]
		for range groups - 1 {
			g = g.Children[0]
		}
		return g.Children[0]
	}
	if got := inGroups(29); got.Type != content.BlockList {
		t.Errorf("the list in 29 groups is a %s block, want a list", got.Type)
	}
	if got := inGroups(30); got.Type != content.BlockLegacy || got.Attrs.Reason != deepReason {
		t.Errorf("the list in 30 groups is %+v, want a legacy block", got)
	}
}

func TestTextQuotes(t *testing.T) {
	quote := func(attrs, markup string) string { return wpBlock("quote", attrs, markup) }
	pull := func(attrs, markup string) string { return wpBlock("pullquote", attrs, markup) }
	notPC := notAsWritten + "<p> or <cite>"
	notBlockquote := notAsWritten + "<blockquote>"
	img := "the text has <img>, which rich text cannot have"
	cite := func(text string) string { return `"citation":[{"type":"text","text":` + strconv.Quote(text) + `}]` }
	quoteOf := func(id int, attrs string, children ...string) string {
		return jsonBlock("quote", id, "", attrs, jsonList(children...))
	}
	pullOf := func(id int, attrs string, children ...string) string {
		return jsonBlock("quote", id, `"variant":"pullquote"`, attrs, jsonList(children...))
	}
	mapped := styleSettings()
	mapped.Blocks.Variants["pullquote"] = "pull-theme"
	unmapped := styleSettings()
	unmapped.Blocks.Variants["pullquote"] = ""
	oldQuote := quote("", `<blockquote class="wp-block-quote"><p>x <img src="a.png"></p></blockquote>`)
	twoCites := quote("", `<blockquote class="wp-block-quote"><p>x</p><cite>a</cite><cite>b</cite></blockquote>`)
	withList := quote("", `<blockquote class="wp-block-quote"><ul><li>x</li></ul></blockquote>`)
	runTextCases(t, []textCase{
		{name: "a quote", text: quote("", `<blockquote class="wp-block-quote">`+wpPara("Quoted")+`<cite>Someone</cite></blockquote>`),
			want: jsonList(quoteOf(0, cite("Someone"), jsonPara(1, "Quoted")))},
		{name: "a quote without a citation", text: quote("", `<blockquote class="wp-block-quote">`+wpPara("Quoted")+wpPara("More")+`</blockquote>`),
			want: jsonList(quoteOf(0, "", jsonPara(1, "Quoted"), jsonPara(2, "More")))},
		{name: "a quote with other blocks and a citation with a link",
			text: quote("", `<blockquote class="wp-block-quote"><!-- wp:heading {"level":3} --><h3>Head</h3><!-- /wp:heading -->`+wpPara("Quoted")+
				`<cite>by <a href="/about/">me</a></cite></blockquote>`),
			want: jsonList(quoteOf(0, `"citation":[{"type":"text","text":"by "},{"type":"text","text":"me","marks":[{"type":"link","attrs":{"href":"/about/"}}]}]`,
				jsonBlock("heading", 1, "", `"level":3`, `[{"type":"text","text":"Head"}]`), jsonPara(2, "Quoted")))},
		{name: "a quote in a quote", text: quote("", `<blockquote class="wp-block-quote">`+quote("", `<blockquote class="wp-block-quote">`+wpPara("Inner")+`</blockquote>`)+`</blockquote>`),
			want: jsonList(quoteOf(0, "", quoteOf(1, "", jsonPara(2, "Inner"))))},
		{name: "the settings of a quote", settings: styleSettings(),
			text: quote(`{"className":"is-style-fancy","textColor":"vivid-red"}`, `<blockquote id="q" class="wp-block-quote">`+wpPara("Quoted")+`</blockquote>`),
			want: jsonList(jsonBlock("quote", 0, `"variant":"fancy-theme","anchor":"q","style":{"textColor":{"preset":"red"}}`, "", jsonList(jsonPara(1, "Quoted"))))},
		{name: "the alignment of a quote is lost", text: quote(`{"textAlign":"center","align":"wide"}`, `<blockquote class="wp-block-quote">`+wpPara("Quoted")+`</blockquote>`),
			want:  jsonList(quoteOf(0, "", jsonPara(1, "Quoted"))),
			lines: []string{lostLine("textAlign, align", "core/quote", "/body/0")}},
		{name: "the alignment of the block is lost", text: quote(`{"align":"right"}`, `<blockquote class="wp-block-quote">`+wpPara("Quoted")+`</blockquote>`) +
			quote(`{"align":"center"}`, `<blockquote class="wp-block-quote">`+wpPara("Again")+`</blockquote>`),
			want: jsonList(quoteOf(0, "", jsonPara(1, "Quoted")), quoteOf(2, "", jsonPara(3, "Again"))),
			lines: []string{
				lostLine("textAlign, align", "core/quote", "/body/0"), lostLine("textAlign", "core/quote", "/body/1"),
			}},
		{name: "a quote whose blocks are all left out has an empty paragraph",
			text: quote("", `<blockquote class="wp-block-quote"><!-- wp:heading --><h2></h2><!-- /wp:heading --><cite>Someone</cite></blockquote>`),
			// The empty heading took the ID 1.
			want:  jsonList(quoteOf(0, cite("Someone"), jsonPara(2, ""))),
			lines: []string{"skipped: the empty heading is removed -> /body/0/content"}},
		{name: "HTML in a quote outside its blocks", text: quote("", `<blockquote class="wp-block-quote">x`+wpPara("Quoted")+`</blockquote>`),
			want:  jsonList(quoteOf(0, "", jsonPara(1, "Quoted"))),
			lines: []string{`skipped: the HTML of the block "core/quote" outside its inner blocks is not imported -> /body/0`}},
		{name: "a block in a quote that becomes a legacy block", text: quote("", `<blockquote class="wp-block-quote"><!-- wp:acme/a /--></blockquote>`),
			want:  jsonList(quoteOf(0, "", jsonLegacy(1, blockA, notConverted("acme/a")))),
			lines: []string{legacyLine(notConverted("acme/a"), "/body/0/content/0")}},
		{name: "a citation with an image", text: quote("", `<blockquote class="wp-block-quote">`+wpPara("Quoted")+`<cite><img src="a.png"></cite></blockquote>`),
			want:  jsonList(jsonLegacy(1, quote("", `<blockquote class="wp-block-quote">`+wpPara("Quoted")+`<cite><img src="a.png"></cite></blockquote>`), img)),
			lines: []string{legacyLine(img, "/body/0")}},

		// Quotes of WordPress before 6.2.
		{name: "an old quote", text: quote("", `<blockquote class="wp-block-quote"><p>One</p><p>Two <em>2</em></p><cite>Old</cite></blockquote>`),
			want: jsonList(quoteOf(0, cite("Old"), jsonPara(1, "One"),
				`{"type":"paragraph","id":"<2>","content":[{"type":"text","text":"Two "},{"type":"text","text":"2","marks":[{"type":"italic"}]}]}`))},
		{name: "an old quote with text outside any p", text: quote("", `<blockquote class="wp-block-quote">Plain text<cite>Someone</cite></blockquote>`),
			want: jsonList(quoteOf(0, cite("Someone"), jsonPara(1, "Plain text")))},
		{name: "an old quote with text before, between and after its paragraphs", text: quote("", "<blockquote>\nA<p>One</p>B\n<p>Two</p> C</blockquote>"),
			want: jsonList(quoteOf(0, "", jsonPara(1, "A B C"), jsonPara(2, "One"), jsonPara(3, "Two")))},
		{name: "an old quote without a paragraph", text: quote("", `<blockquote class="wp-block-quote"></blockquote>`),
			want: jsonList(quoteOf(0, "", jsonPara(1, "")))},
		{name: "an old quote with only a citation", text: quote("", `<blockquote class="wp-block-quote"><cite>Someone</cite></blockquote>`),
			want: jsonList(quoteOf(0, cite("Someone"), jsonPara(1, "")))},
		{name: "an old quote with a list", text: withList,
			want:  jsonList(jsonLegacy(1, withList, notPC)),
			lines: []string{legacyLine(notPC, "/body/0")}},
		{name: "an old quote with two citations", text: twoCites,
			want:  jsonList(jsonLegacy(2, twoCites, notPC)),
			lines: []string{legacyLine(notPC, "/body/0")}},
		{name: "a paragraph of an old quote with an image", text: oldQuote,
			want:  jsonList(jsonLegacy(2, oldQuote, img)),
			lines: []string{legacyLine(img, "/body/0")}},
		{name: "a quote that is not a blockquote", text: quote("", `<div>x</div>`),
			want:  jsonList(jsonLegacy(0, quote("", `<div>x</div>`), notAsWritten+"<blockquote>")),
			lines: []string{legacyLine(notAsWritten+"<blockquote>", "/body/0")}},

		// Pullquotes.
		{name: "a pullquote", text: pull("", `<figure class="wp-block-pullquote"><blockquote><p>Big</p><cite>Author</cite></blockquote></figure>`),
			want: jsonList(pullOf(0, cite("Author"), jsonPara(1, "Big")))},
		{name: "a pullquote of the style that is the default or solid color",
			text: pull(`{"className":"is-style-default"}`, `<figure class="wp-block-pullquote is-style-default"><blockquote><p>a</p></blockquote></figure>`) +
				pull(`{"className":"is-style-solid-color"}`, `<figure class="wp-block-pullquote is-style-solid-color"><blockquote><p>b</p></blockquote></figure>`),
			want:  jsonList(pullOf(0, "", jsonPara(1, "a")), pullOf(2, "", jsonPara(3, "b"))),
			lines: []string{`skipped: the block style "solid-color" is not imported: a block has one style -> /body/1`}},
		{name: "a pullquote with more than one paragraph", text: pull("", `<figure class="wp-block-pullquote"><blockquote><p>a</p><p>b</p></blockquote></figure>`),
			want: jsonList(pullOf(0, "", jsonPara(1, "a"), jsonPara(2, "b")))},
		{name: "a pullquote with text and marks outside any p", text: pull("", `<figure class="wp-block-pullquote"><blockquote>Some <em>text</em><br>more<cite>C</cite></blockquote></figure>`),
			want: jsonList(pullOf(0, cite("C"),
				`{"type":"paragraph","id":"<1>","content":[{"type":"text","text":"Some "},{"type":"text","text":"text","marks":[{"type":"italic"}]},{"type":"hardBreak"},{"type":"text","text":"more"}]}`))},
		{name: "an old pullquote", text: pull("", `<blockquote class="wp-block-pullquote"><p>Old</p><cite>C</cite></blockquote>`),
			want: jsonList(pullOf(0, cite("C"), jsonPara(1, "Old")))},
		{name: "an empty pullquote", text: pull("", `<figure class="wp-block-pullquote"><blockquote></blockquote></figure>`),
			want: jsonList(pullOf(0, "", jsonPara(1, "")))},
		{name: "a pullquote with nothing but an element that holds nothing", text: pull("", `<figure class="wp-block-pullquote"><blockquote><span></span></blockquote></figure>`),
			// The paragraph for what is outside any p took the ID 1, and was left out.
			want: jsonList(pullOf(0, "", jsonPara(2, "")))},
		{name: "a pullquote with nothing but a control character", text: pull("", `<figure class="wp-block-pullquote"><blockquote>`+string(rune(0x11))+`</blockquote></figure>`),
			want: jsonList(pullOf(0, "", jsonPara(2, "")))},
		{name: "a pullquote with a paragraph and an element that holds nothing", text: pull("", `<figure class="wp-block-pullquote"><blockquote><p>a</p><b></b></blockquote></figure>`),
			want: jsonList(pullOf(0, "", jsonPara(1, "a")))},
		{name: "a pullquote with the variant of the settings", settings: mapped,
			text:  pull(`{"className":"is-style-fancy"}`, `<figure class="wp-block-pullquote"><blockquote><p>a</p></blockquote></figure>`),
			want:  jsonList(jsonBlock("quote", 0, `"variant":"pull-theme"`, "", jsonList(jsonPara(1, "a")))),
			lines: []string{`skipped: the block style "fancy" is not imported: a block has one style -> /body/0`}},
		{name: "a pullquote whose variant is not imported", settings: unmapped,
			text:  pull("", `<figure class="wp-block-pullquote"><blockquote><p>a</p></blockquote></figure>`),
			want:  jsonList(quoteOf(0, "", jsonPara(1, "a"))),
			lines: []string{`skipped: the block style "pullquote" is not imported: blocks.variants maps it to nothing -> /body/0`}},
		{name: "the alignment of a pullquote is lost", text: pull(`{"textAlign":"left","align":"wide"}`, `<figure class="wp-block-pullquote"><blockquote><p>a</p></blockquote></figure>`),
			want:  jsonList(pullOf(0, "", jsonPara(1, "a"))),
			lines: []string{lostLine("textAlign, align", "core/pullquote", "/body/0")}},
		{name: "a pullquote without a blockquote", text: pull("", `<figure class="wp-block-pullquote"><div>x</div></figure>`),
			want:  jsonList(jsonLegacy(0, pull("", `<figure class="wp-block-pullquote"><div>x</div></figure>`), notBlockquote)),
			lines: []string{legacyLine(notBlockquote, "/body/0")}},
		{name: "the settings of a pullquote", settings: styleSettings(),
			text: pull(`{"textColor":"vivid-red"}`, `<figure id="p" class="wp-block-pullquote has-vivid-red-color"><blockquote><p>a</p></blockquote></figure>`),
			want: jsonList(jsonBlock("quote", 0, `"variant":"pullquote","anchor":"p","style":{"textColor":{"preset":"red"}}`, "", jsonList(jsonPara(1, "a"))))},
	})
}

func TestTextCode(t *testing.T) {
	code := func(attrs, markup string) string { return wpBlock("code", attrs, markup) }
	runTextCases(t, []textCase{
		{name: "code with a language, line breaks, tabs and a break",
			text: code("", "<pre class=\"wp-block-code\"><code class=\"language-go\">func main() {\n\tfmt.Println(&quot;&lt;hi&gt;&quot;)<br>}</code></pre>"),
			want: jsonList(jsonBlock("code", 0, "", `"language":"go"`, strconv.Quote("func main() {\n\tfmt.Println(\"<hi>\")\n}")))},
		{name: "code without a language", text: code("", "<pre class=\"wp-block-code\"><code>a\r\nb</code></pre>"),
			want: jsonList(jsonBlock("code", 0, "", "", `"a\nb"`))},
		{name: "code without a code element has the language of the pre", text: code("", `<pre class="wp-block-code language-php">plain</pre>`),
			want: jsonList(jsonBlock("code", 0, "", `"language":"php"`, `"plain"`))},
		{name: "the language of the pre, if there is a code element, is not read", text: code("", `<pre class="wp-block-code language-php"><code>plain</code></pre>`),
			want: jsonList(jsonBlock("code", 0, "", "", `"plain"`))},
		{name: "code with other elements", text: code("", `<pre class="wp-block-code"><code class="x language-js y">a<span class="k">b</span>c</code></pre>`),
			want:  jsonList(jsonBlock("code", 0, "", `"language":"js"`, `"abc"`)),
			lines: []string{`changed: kept only the text of <span> -> /body/0`}},
		{name: "code with more than the code element", text: code("", `<pre class="wp-block-code"><code>a</code>b</pre>`),
			want:  jsonList(jsonBlock("code", 0, "", "", `"ab"`)),
			lines: []string{`changed: kept only the text of <code> -> /body/0`}},
		{name: "a language that is not a name", text: code("", `<pre class="wp-block-code"><code class="language-c++">x</code></pre>`),
			want:  jsonList(jsonBlock("code", 0, "", "", `"x"`)),
			lines: []string{`skipped: the language "c++" is not imported: invalid name "c++": use ASCII letters, digits, "-" and "_" -> /body/0`}},
		{name: "empty code", text: code("", `<pre class="wp-block-code"><code></code></pre>`), want: jsonList(jsonBlock("code", 0, "", "", ""))},
		{name: "the settings of code", settings: styleSettings(),
			text: code(`{"className":"is-style-fancy"}`, `<pre id="c" class="wp-block-code"><code>x</code></pre>`),
			want: jsonList(jsonBlock("code", 0, `"variant":"fancy-theme","anchor":"c"`, "", `"x"`))},
		{name: "code that is not a pre", text: code("", `<div>x</div>`),
			want:  jsonList(jsonLegacy(0, code("", `<div>x</div>`), notAsWritten+"<pre>")),
			lines: []string{legacyLine(notAsWritten+"<pre>", "/body/0")}},

		{name: "preformatted text", text: wpBlock("preformatted", "", "<pre class=\"wp-block-preformatted\">Line <strong>one</strong><br>  Line two\n\tthree</pre>"),
			want:  jsonList(jsonBlock("code", 0, "", "", strconv.Quote("Line one\n  Line two\n\tthree"))),
			lines: []string{`changed: kept only the text of <strong> -> /body/0`}},
		{name: "preformatted text with a style", settings: styleSettings(),
			text: wpBlock("preformatted", `{"textColor":"vivid-red"}`, `<pre class="wp-block-preformatted has-vivid-red-color has-text-color">x</pre>`),
			want: jsonList(jsonBlock("code", 0, `"style":{"textColor":{"preset":"red"}}`, "", `"x"`))},
	})
}

func TestTextTables(t *testing.T) {
	table := func(attrs, markup string) string { return wpBlock("table", attrs, markup) }
	figure := func(inner string) string { return `<figure class="wp-block-table">` + inner + `</figure>` }
	tableOf := func(id int, keys, attrs string, rows ...string) string {
		return jsonBlock("table", id, keys, attrs, jsonList(rows...))
	}
	img := "the text has <img>, which rich text cannot have"
	withImage := table("", figure(`<table><tbody><tr><td>x <img src="a.png"></td></tr></tbody></table>`))
	noTable := table("", figure(`<div></div>`))
	runTextCases(t, []textCase{
		{name: "a table",
			text: table(`{"hasFixedLayout":true,"className":"is-style-stripes"}`,
				`<figure class="wp-block-table is-style-stripes"><table class="has-fixed-layout"><thead><tr><th>Name</th><th class="has-text-align-right" data-align="right">Qty</th></tr></thead>`+
					`<tbody><tr><td>Apple</td><td class="has-text-align-right" data-align="right">3</td></tr><tr><td colspan="2">Total</td></tr></tbody>`+
					`<tfoot><tr><td>Sum</td><td>4</td></tr></tfoot></table><figcaption class="wp-element-caption">Fruit</figcaption></figure>`),
			want: jsonList(tableOf(0, `"variant":"stripes"`, `"caption":[{"type":"text","text":"Fruit"}]`,
				jsonRow("head", jsonCell(`"header":true`, "Name"), jsonCell(`"header":true,"textAlign":"right"`, "Qty")),
				jsonRow("", jsonCell("", "Apple"), jsonCell(`"textAlign":"right"`, "3")),
				jsonRow("", jsonCell(`"colspan":2`, "Total")),
				jsonRow("foot", jsonCell("", "Sum"), jsonCell("", "4")))),
			lines: []string{lostLine("hasFixedLayout", "core/table", "/body/0")}},
		{name: "a table that is only a table", text: table("", `<table><tbody><tr><td>x</td></tr></tbody></table>`),
			want: jsonList(tableOf(0, "", "", jsonRow("", jsonCell("", "x"))))},
		{name: "a table with a fixed layout, which WordPress does not write in the attributes, as the class of the table tells",
			text:  table("", figure(`<table class="has-fixed-layout"><tbody><tr><td>x</td></tr></tbody></table>`)),
			want:  jsonList(tableOf(0, "", "", jsonRow("", jsonCell("", "x")))),
			lines: []string{lostLine("hasFixedLayout", "core/table", "/body/0")}},
		{name: "a table without the class has no fixed layout, whatever the attributes say",
			text: table(`{"hasFixedLayout":true}`, figure(`<table><tbody><tr><td>x</td></tr></tbody></table>`)) +
				table(`{"hasFixedLayout":false}`, figure(`<table class="other"><tbody><tr><td>y</td></tr></tbody></table>`)),
			want: jsonList(tableOf(0, "", "", jsonRow("", jsonCell("", "x"))), tableOf(1, "", "", jsonRow("", jsonCell("", "y"))))},
		{name: "the class of a table that is only a table, and of a figure", text: table("", `<table class="x has-fixed-layout y"><tbody><tr><td>a</td></tr></tbody></table>`) +
			table("", `<figure class="wp-block-table has-fixed-layout"><table><tbody><tr><td>b</td></tr></tbody></table></figure>`),
			want:  jsonList(tableOf(0, "", "", jsonRow("", jsonCell("", "a"))), tableOf(1, "", "", jsonRow("", jsonCell("", "b")))),
			lines: []string{lostLine("hasFixedLayout", "core/table", "/body/0")}},
		{name: "the settings of a table", settings: styleSettings(),
			text: table(`{"className":"is-style-fancy","backgroundColor":"vivid-red"}`, `<figure id="t" class="wp-block-table"><table><tbody><tr><td>x</td></tr></tbody></table></figure>`),
			want: jsonList(tableOf(0, `"variant":"fancy-theme","anchor":"t","style":{"backgroundColor":{"preset":"red"}}`, "", jsonRow("", jsonCell("", "x"))))},
		{name: "the head, the body and the foot in the order of a document",
			text: table("", figure(`<table><tfoot><tr><td>f</td></tr></tfoot><tbody><tr><td>b1</td></tr></tbody><thead><tr><th>h</th></tr></thead><tbody><tr><td>b2</td></tr></tbody></table>`)),
			want: jsonList(tableOf(0, "", "",
				jsonRow("head", jsonCell(`"header":true`, "h")), jsonRow("", jsonCell("", "b1")), jsonRow("", jsonCell("", "b2")), jsonRow("foot", jsonCell("", "f"))))},
		{name: "rows that are only in the head", text: table("", figure(`<table><thead><tr><th>a</th><td>b</td></tr></thead></table>`)),
			want: jsonList(tableOf(0, "", "", jsonRow("head", jsonCell(`"header":true`, "a"), jsonCell("", "b"))))},
		{name: "cells that whiteroom cannot have as they are",
			text: table("", figure(`<table><tbody><tr><th scope="row">H</th><td rowspan="0" colspan="1001" data-align="justify">x</td><td rowspan="2" class="has-text-align-center">y</td></tr></tbody></table>`)),
			want: jsonList(tableOf(0, "", "", jsonRow("", jsonCell(`"header":true`, "H"), jsonCell("", "x"), jsonCell(`"rowspan":2,"textAlign":"center"`, "y")))),
			lines: []string{
				lostLine("colspan, rowspan, textAlign", "core/table", "/body/0"),
			}},
		{name: "the alignment of a cell from the attribute, over the class",
			text: table("", figure(`<table><tbody><tr><td data-align="Left" class="has-text-align-right">a</td><td class="x has-text-align-center">b</td><td data-align="">c</td></tr></tbody></table>`)),
			want: jsonList(tableOf(0, "", "", jsonRow("", jsonCell(`"textAlign":"left"`, "a"), jsonCell(`"textAlign":"center"`, "b"), jsonCell("", "c"))))},
		{name: "the spans of a cell", text: table("", figure(`<table><tbody><tr><td colspan=" 3 " rowspan="1000">a</td><td colspan="1" rowspan="">b</td></tr></tbody></table>`)),
			want: jsonList(tableOf(0, "", "", jsonRow("", jsonCell(`"colspan":3,"rowspan":1000`, "a"), jsonCell(`"colspan":1`, "b"))))},
		{name: "cells with marks and breaks", text: table("", figure(`<table><tbody><tr><td><strong>a</strong><br>b</td></tr></tbody></table>`)),
			want: jsonList(tableOf(0, "", "", jsonRow("",
				`{"type":"tableCell","content":[{"type":"text","text":"a","marks":[{"type":"bold"}]},{"type":"hardBreak"},{"type":"text","text":"b"}]}`)))},
		{name: "a note about the text of a cell is at the cell",
			text: table("", figure(`<table><tbody><tr></tr><tr><td>a</td><td>b <kbd>c</kbd></td></tr></tbody></table>`)),
			want: jsonList(tableOf(0, "", "", jsonRow("", jsonCell("", "a"), jsonCell("", "b c")))),
			lines: []string{
				"skipped: the empty row of the table is removed -> /body/0",
				"changed: kept only the text of <kbd> -> /body/0/content/0/content/1/content",
			}},
		{name: "rows without cells are left out", text: table("", figure(`<table><tbody><tr></tr><tr><td>a</td></tr><tr></tr><tr></tr></tbody></table>`)),
			want:  jsonList(tableOf(0, "", "", jsonRow("", jsonCell("", "a")))),
			lines: []string{"skipped: 3 empty rows of the table are removed -> /body/0"}},
		{name: "a table without rows is left out", text: table("", figure(`<table><tbody><tr></tr></tbody></table>`)) + table("", figure(`<table></table>`)),
			want:  `[]`,
			lines: []string{"skipped: the empty table is removed -> /body", "skipped: the empty table is removed -> /body"}},
		{name: "a table with an image in a cell", text: withImage,
			want:  jsonList(jsonLegacy(1, withImage, img)),
			lines: []string{legacyLine(img, "/body/0")}},
		{name: "a figure without a table", text: noTable,
			want:  jsonList(jsonLegacy(0, noTable, notAsWritten+"<table>")),
			lines: []string{legacyLine(notAsWritten+"<table>", "/body/0")}},
		{name: "a table that is not a table", text: table("", `<div>x</div>`),
			want:  jsonList(jsonLegacy(0, table("", `<div>x</div>`), notAsWritten+"<figure> or <table>")),
			lines: []string{legacyLine(notAsWritten+"<figure> or <table>", "/body/0")}},
		{name: "a caption with an image", text: table("", `<figure class="wp-block-table"><table><tbody><tr><td>a</td></tr></tbody></table><figcaption><img src="a.png"></figcaption></figure>`),
			want:  jsonList(jsonLegacy(1, table("", `<figure class="wp-block-table"><table><tbody><tr><td>a</td></tr></tbody></table><figcaption><img src="a.png"></figcaption></figure>`), img)),
			lines: []string{legacyLine(img, "/body/0")}},
	})
}

// tableOfCells writes a table of rows rows with cellsPerRow cells in each, and then a row of the cells in the last.
func tableOfCells(rows, cellsPerRow, last int) string {
	row := "<tr>" + strings.Repeat("<td>x</td>", cellsPerRow) + "</tr>"
	end := ""
	if last > 0 {
		end = "<tr>" + strings.Repeat("<td>y</td>", last) + "</tr>"
	}
	return wpBlock("table", "", `<figure class="wp-block-table"><table><tbody>`+strings.Repeat(row, rows)+end+`</tbody></table></figure>`)
}

func TestTextCellAlign(t *testing.T) {
	cell := func(attrs ...string) *html.Node {
		n := &html.Node{Type: html.ElementNode, Namespace: html.HTML, Name: "td"}
		for i := 0; i < len(attrs); i += 2 {
			n.Attrs = append(n.Attrs, html.Attr{Name: attrs[i], Value: attrs[i+1]})
		}
		return n
	}
	for _, tt := range []struct {
		name           string
		cell           *html.Node
		aligned, other string
	}{
		{"nothing", cell(), "", ""},
		{"data-align", cell("data-align", " Right "), "right", ""},
		{"the class", cell("class", "x has-text-align-Center"), "center", ""},
		{"data-align wins over the class", cell("data-align", "left", "class", "has-text-align-right"), "left", ""},
		{"data-align that is not one", cell("data-align", "justify", "class", "has-text-align-right"), "", "justify"},
		{"the style when there is neither", cell("style", "width: 1px; Text-Align: CENTER"), "center", ""},
		{"the style with !important", cell("style", "text-align: right !important"), "right", ""},
		{"the style that is not an alignment", cell("style", "text-align: justify"), "", "justify"},
		{"the style has other properties", cell("style", "color: red"), "", ""},
		{"data-align and the class win over the style", cell("style", "text-align: right", "data-align", "left"), "left", ""},
		{"the class wins over the style", cell("style", "text-align: right", "class", "has-text-align-center"), "center", ""},
		{"the attribute align when there is none of the others", cell("align", "Right"), "right", ""},
		{"the style wins over the attribute align", cell("style", "text-align: left", "align", "right"), "left", ""},
		{"the attribute align that is not one", cell("align", "char"), "", "char"},
	} {
		if aligned, other := cellAlign(tt.cell); aligned != tt.aligned || other != tt.other {
			t.Errorf("%s: cellAlign = %q, %q, want %q, %q", tt.name, aligned, other, tt.aligned, tt.other)
		}
	}
}

func TestTextTableCells(t *testing.T) {
	// A table has at most content.MaxTableCells cells; the cells of the other tables of the body are not counted.
	res, imp := textBody(t, bodySite(false), nil, tableOfCells(1000, 5, 0))
	if len(res.Blocks) != 1 || res.Blocks[0].Type != content.BlockTable || len(res.Blocks[0].Children) != 1000 {
		t.Fatalf("5000 cells became %d blocks", len(res.Blocks))
	}
	wantLines(t, imp.Report, nil)
	res, _ = textBody(t, bodySite(false), nil, tableOfCells(1000, 5, 0)+tableOfCells(1, 1, 0))
	if len(res.Blocks) != 2 || res.Blocks[0].Type != content.BlockTable || res.Blocks[1].Type != content.BlockTable {
		t.Errorf("two tables, of 5000 cells and of 1, became %d blocks", len(res.Blocks))
	}

	const reason = "the table has more than 5000 cells"
	for name, text := range map[string]string{
		"in many rows": tableOfCells(1000, 5, 1),
		"in one row":   tableOfCells(1, 5001, 0),
	} {
		t.Run(name, func(t *testing.T) {
			res, imp := textBody(t, bodySite(false), nil, text)
			if len(res.Blocks) != 1 || res.Blocks[0].Type != content.BlockLegacy || res.Blocks[0].Attrs.Reason != reason {
				t.Fatalf("5001 cells became %d blocks, want a legacy block", len(res.Blocks))
			}
			wantLines(t, imp.Report, bodyLines(legacyLine(reason, "/body/0")))
		})
	}
}

func TestTextSeparatorsAndSpacers(t *testing.T) {
	settings := styleSettings()
	settings.Blocks.Presets.Spacing["50"] = ""
	settings.Blocks.Presets.Spacing["60"] = "a b"
	separator := func(attrs, markup string) string { return wpBlock("separator", attrs, markup) }
	spacer := func(attrs string) string {
		return wpBlock("spacer", attrs, `<div style="height:1px" aria-hidden="true" class="wp-block-spacer"></div>`)
	}
	spacerOf := func(id int, value string) string {
		return jsonBlock("spacer", id, "", `"height":`+value, "")
	}
	removed := func(height, why string) string {
		return fmt.Sprintf("skipped: the spacer is removed: its height %q is not one that whiteroom accepts: %s -> /body", height, why)
	}
	notHR := notAsWritten + "<hr> or <div>"
	runTextCases(t, []textCase{
		{name: "a separator", text: separator("", `<hr class="wp-block-separator has-alpha-channel-opacity"/>`),
			want: jsonList(jsonBlock("separator", 0, "", "", ""))},
		{name: "separators of the wide and the dots styles, and the default",
			text: separator(`{"className":"is-style-wide"}`, `<hr class="wp-block-separator has-alpha-channel-opacity is-style-wide"/>`) +
				separator(`{"className":"is-style-dots"}`, `<hr class="wp-block-separator has-alpha-channel-opacity is-style-dots"/>`) +
				separator(`{"className":"is-style-default"}`, `<hr class="wp-block-separator has-alpha-channel-opacity is-style-default"/>`),
			want: jsonList(jsonBlock("separator", 0, `"variant":"wide"`, "", ""), jsonBlock("separator", 1, `"variant":"dots"`, "", ""), jsonBlock("separator", 2, "", "", ""))},
		{name: "a separator that is a div, with an opacity", text: separator(`{"tagName":"div","opacity":"css"}`, `<div class="wp-block-separator has-alpha-channel-opacity"></div>`),
			want: jsonList(jsonBlock("separator", 0, "", "", ""))},
		{name: "a separator with a color", settings: styleSettings(),
			text: separator(`{"backgroundColor":"vivid-red"}`, `<hr id="s" class="wp-block-separator has-text-color has-alpha-channel-opacity has-background has-vivid-red-background-color"/>`),
			want: jsonList(jsonBlock("separator", 0, `"anchor":"s","style":{"backgroundColor":{"preset":"red"}}`, "", ""))},
		{name: "a separator that is not an hr", text: separator("", `<p>x</p>`),
			want:  jsonList(jsonLegacy(0, separator("", `<p>x</p>`), notHR)),
			lines: []string{legacyLine(notHR, "/body/0")}},

		{name: "a spacer", text: spacer(`{"height":"50px"}`), want: jsonList(spacerOf(0, `{"value":"50px"}`))},
		{name: "a spacer of the height by default", text: spacer(""), want: jsonList(spacerOf(0, `{"value":"100px"}`))},
		{name: "a spacer of the height by default, when the height is empty", text: spacer(`{"height":""}`), want: jsonList(spacerOf(0, `{"value":"100px"}`))},
		{name: "a spacer of the height that is a number", text: spacer(`{"height":30}`), want: jsonList(spacerOf(0, `{"value":"30px"}`))},
		{name: "spacers of the heights in the other units", text: spacer(`{"height":" 2rem "}`) + spacer(`{"height":"1.5em"}`) + spacer(`{"height":"10vh"}`) + spacer(`{"height":"0"}`),
			want: jsonList(spacerOf(0, `{"value":"2rem"}`), spacerOf(1, `{"value":"1.5em"}`), spacerOf(2, `{"value":"10vh"}`), spacerOf(3, `{"value":"0"}`))},
		{name: "a spacer of a preset", settings: settings, text: spacer(`{"height":"var:preset|spacing|40"}`) + spacer(`{"height":"var:preset|spacing|70"}`),
			want: jsonList(spacerOf(0, `{"preset":"large"}`), spacerOf(1, `{"preset":"70"}`))},
		{name: "a spacer of a preset that is not imported", settings: settings,
			text: spacer(`{"height":"var:preset|spacing|50"}`) + spacer(`{"height":"var:preset|spacing|60"}`) + spacer(`{"height":"50px"}`),
			// Spacers that are left out take no ID.
			want: jsonList(spacerOf(0, `{"value":"50px"}`)),
			lines: []string{
				removed("var:preset|spacing|50", "blocks.presets.spacing maps it to nothing"),
				removed("var:preset|spacing|60", "it is not a name that whiteroom accepts"),
			}},
		{name: "spacers of heights that whiteroom does not accept",
			text: spacer(`{"height":"10%"}`) + spacer(`{"height":"-5px"}`) + spacer(`{"height":"30"}`) + spacer(`{"height":"calc(1rem + 2px)"}`) +
				spacer(`{"height":"var:preset|font-size|large"}`) + spacer(`{"height":-4}`),
			want: `[]`,
			lines: []string{
				removed("10%", `percentage "10%" is not allowed here`),
				removed("-5px", `negative length "-5px" is not allowed here`),
				removed("30", `invalid length "30"`),
				removed("calc(1rem + 2px)", `invalid length "calc(1rem + 2px)"`),
				removed("var:preset|font-size|large", `invalid length "var:preset|font-size|large"`),
				removed("-4px", `negative length "-4px" is not allowed here`),
			}},
		{name: "a spacer with a width", text: spacer(`{"height":"20px","width":"10px"}`) + spacer(`{"width":""}`),
			want:  jsonList(spacerOf(0, `{"value":"20px"}`), spacerOf(1, `{"value":"100px"}`)),
			lines: []string{lostLine("width", "core/spacer", "/body/0")}},
		{name: "the settings of a spacer", settings: styleSettings(),
			text: wpBlock("spacer", `{"height":"20px","className":"is-style-fancy"}`, `<div style="height:20px" id="gap" aria-hidden="true" class="wp-block-spacer"></div>`),
			want: jsonList(jsonBlock("spacer", 0, `"variant":"fancy-theme","anchor":"gap"`, `"height":{"value":"20px"}`, ""))},
		{name: "a spacer that is not a div", text: wpBlock("spacer", "", `<p>x</p>`),
			want:  jsonList(jsonLegacy(0, wpBlock("spacer", "", `<p>x</p>`), notAsWritten+"<div>")),
			lines: []string{legacyLine(notAsWritten+"<div>", "/body/0")}},
	})
}

func TestTextHTML(t *testing.T) {
	const (
		youtube  = "https://www.youtube.com/embed/dQw4w9WgXcQ"
		notAllow = "custom HTML, which the site does not allow (site.allowHtml is false)"
		embedMsg = "changed: the iframe of the custom HTML is an embed block here -> /body/0"
		htmlMsg  = "review: the custom HTML is an html block; check what it shows -> /body/0"
	)
	html := func(markup string) string { return wpBlock("html", "", markup) }
	embedOf := func(id int, provider, url string) string {
		return jsonBlock("embed", id, "", fmt.Sprintf(`"provider":%q,"url":%q`, provider, url), "")
	}
	htmlOf := func(id int, markup string) string {
		return jsonBlock("html", id, "", `"html":`+strconv.Quote(markup), "")
	}
	frame := func(src string) string {
		return `<iframe width="560" height="315" src="` + src + `" title="Video" frameborder="0" allowfullscreen></iframe>`
	}
	maps := "https://www.google.com/maps/embed?pb=!1m18!1m12!1m3"
	other := frame("https://example.com/video")
	div := `<div class="x">Custom</div>`
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprintf("an iframe that a provider embeds, allowHtml=%v", allow), func(t *testing.T) {
			runTextCases(t, []textCase{
				{name: "YouTube", allowHTML: allow, text: html(frame(youtube)), want: jsonList(embedOf(0, "youtube", youtube)), lines: []string{embedMsg}},
				{name: "YouTube without the privacy mode", allowHTML: allow, text: html(frame("https://www.youtube.com/embed/dQw4w9WgXcQ?start=3")),
					want: jsonList(embedOf(0, "youtube", "https://www.youtube.com/embed/dQw4w9WgXcQ?start=3")), lines: []string{embedMsg}},
				{name: "Vimeo, in a div", allowHTML: allow, text: html(`<div class="video">` + frame("https://player.vimeo.com/video/76979871") + `</div>`),
					want: jsonList(embedOf(0, "vimeo", "https://player.vimeo.com/video/76979871")), lines: []string{embedMsg}},
				{name: "Google Maps, in a p, with comments and white space", allowHTML: allow, text: html("<!-- a -->\n<p>\n" + frame(maps) + "\n</p>\n<!-- b -->"),
					want: jsonList(embedOf(0, "googleMaps", maps)), lines: []string{embedMsg}},
				{name: "the address is read with its references", allowHTML: allow, text: html(frame("https://www.youtube.com/embed/dQw4w9WgXcQ?a=1&amp;start=3")),
					want: jsonList(embedOf(0, "youtube", "https://www.youtube.com/embed/dQw4w9WgXcQ?a=1&start=3")), lines: []string{embedMsg}},
				{name: "an address without the scheme is https", allowHTML: allow, text: html(frame("//www.youtube.com/embed/dQw4w9WgXcQ")),
					want: jsonList(embedOf(0, "youtube", youtube)), lines: []string{embedMsg}},
			})
		})
	}
	runTextCases(t, []textCase{
		{name: "an iframe of another site is custom HTML that the site does not allow", text: html(other),
			want:  jsonList(jsonLegacy(0, html(other), notAllow)),
			lines: []string{legacyLine(notAllow, "/body/0")}},
		{name: "an iframe of another site is custom HTML that the site allows", allowHTML: true, text: html(other),
			want: jsonList(htmlOf(0, other)), lines: []string{htmlMsg}},
		{name: "custom HTML that the site does not allow", text: html(div),
			want:  jsonList(jsonLegacy(0, html(div), notAllow)),
			lines: []string{legacyLine(notAllow, "/body/0")}},
		{name: "custom HTML that the site allows", allowHTML: true, text: html(div),
			want: jsonList(htmlOf(0, div)), lines: []string{htmlMsg}},
		{name: "custom HTML with the settings of the block", allowHTML: true, settings: styleSettings(),
			text: wpBlock("html", `{"className":"is-style-fancy"}`, div),
			want: jsonList(jsonBlock("html", 0, `"variant":"fancy-theme"`, `"html":`+strconv.Quote(div), "")), lines: []string{htmlMsg}},
		{name: "custom HTML of white space only is nothing", text: html("") + html(" \t\r\n\f"), allowHTML: true, want: `[]`},
		{name: "custom HTML of white space only is nothing, when it is not allowed", text: html("\n\n"), want: `[]`},
		{name: "custom HTML of white space and control characters only is nothing", allowHTML: true, text: html("\n" + string(rune(1)) + " \t" + string(rune(0x85))), want: `[]`},
		{name: "two iframes", allowHTML: true, text: html(frame(youtube) + frame(youtube)),
			want: jsonList(htmlOf(0, frame(youtube)+frame(youtube))), lines: []string{htmlMsg}},
		{name: "an iframe and a text", allowHTML: true, text: html(frame(youtube) + "<p>after</p>"),
			want: jsonList(htmlOf(0, frame(youtube)+"<p>after</p>")), lines: []string{htmlMsg}},
		{name: "an iframe in a div in a div", allowHTML: true, text: html(`<div><div>` + frame(youtube) + `</div></div>`),
			want: jsonList(htmlOf(0, `<div><div>`+frame(youtube)+`</div></div>`)), lines: []string{htmlMsg}},
		{name: "an iframe in a div with a text", allowHTML: true, text: html(`<div>look: ` + frame(youtube) + `</div>`),
			want: jsonList(htmlOf(0, `<div>look: `+frame(youtube)+`</div>`)), lines: []string{htmlMsg}},
		{name: "an iframe of a video with credentials", allowHTML: true, text: html(frame("https://user:pw@www.youtube.com/embed/dQw4w9WgXcQ")),
			want: jsonList(htmlOf(0, frame("https://user:pw@www.youtube.com/embed/dQw4w9WgXcQ"))), lines: []string{htmlMsg}},
		{name: "an iframe of a video that is not https", allowHTML: true, text: html(frame("http://www.youtube.com/embed/dQw4w9WgXcQ")),
			want: jsonList(htmlOf(0, frame("http://www.youtube.com/embed/dQw4w9WgXcQ"))), lines: []string{htmlMsg}},
		{name: "an iframe of a playlist", allowHTML: true, text: html(frame("https://www.youtube.com/embed/videoseries?list=PLx")),
			want: jsonList(htmlOf(0, frame("https://www.youtube.com/embed/videoseries?list=PLx"))), lines: []string{htmlMsg}},
		{name: "an iframe without a src", allowHTML: true, text: html(`<iframe></iframe>`),
			want: jsonList(htmlOf(0, `<iframe></iframe>`)), lines: []string{htmlMsg}},
		{name: "text that only tells of an iframe", allowHTML: true, text: html(`<p>&lt;iframe src="` + youtube + `"&gt;</p>`),
			want: jsonList(htmlOf(0, `<p>&lt;iframe src="`+youtube+`"&gt;</p>`)), lines: []string{htmlMsg}},
		{name: "the IDs follow the blocks that are made", allowHTML: true, text: html(div) + html("") + html(frame(youtube)) + wpPara("x"),
			want: jsonList(htmlOf(0, div), embedOf(1, "youtube", youtube), jsonPara(2, "x")),
			lines: []string{
				"review: the custom HTML is an html block; check what it shows -> /body/0",
				"changed: the iframe of the custom HTML is an embed block here -> /body/1",
			}},
	})
}

// wpGroup is a group of WordPress with the attributes attrs, whose element is tag, with the blocks in it.
func wpGroup(attrs, tag string, blocks ...string) string {
	return wpBlock("group", attrs, "<"+tag+` class="wp-block-group">`+strings.Join(blocks, "\n")+"</"+tag+">")
}

func TestTextGroups(t *testing.T) {
	groupOf := func(id int, keys, attrs string, children ...string) string {
		return jsonBlock("group", id, keys, attrs, jsonList(children...))
	}
	layout := func(attrs string) string { return wpGroup(`{"layout":`+attrs+`}`, "div", wpPara("x")) }
	inLayout := func(l string) string { return groupOf(0, "", `"layout":`+l, jsonPara(1, "x")) }
	notOne := notAsWritten + "one element"
	runTextCases(t, []textCase{
		{name: "a group in the flow", text: layout(`{"type":"constrained"}`), want: jsonList(groupOf(0, "", "", jsonPara(1, "x")))},
		{name: "a group with no layout", text: wpGroup("", "div", wpPara("x")), want: jsonList(groupOf(0, "", "", jsonPara(1, "x")))},
		{name: "groups with a layout by default or inherited", text: layout(`{"type":"default"}`) + layout(`{"type":"inherit"}`) + layout(`{}`) + layout(`{"inherit":true}`),
			want: jsonList(groupOf(0, "", "", jsonPara(1, "x")), groupOf(2, "", "", jsonPara(3, "x")), groupOf(4, "", "", jsonPara(5, "x")), groupOf(6, "", "", jsonPara(7, "x")))},
		{name: "a group of WordPress 5.9 and 6.0, whose layout has the width of the theme", text: layout(`{"inherit":true}`) + layout(`{"inherit":false}`) +
			layout(`{"inherit":true,"contentSize":"800px"}`) + layout(`{"inherit":"yes"}`) + layout(`{"type":"flex","inherit":true}`),
			want: jsonList(groupOf(0, "", "", jsonPara(1, "x")), groupOf(2, "", "", jsonPara(3, "x")), groupOf(4, "", "", jsonPara(5, "x")),
				groupOf(6, "", "", jsonPara(7, "x")), groupOf(8, "", `"layout":{"type":"row"}`, jsonPara(9, "x"))),
			lines: []string{
				lostLine("layout.contentSize", "core/group", "/body/2"), lostLine("layout.inherit", "core/group", "/body/3"),
				lostLine("layout.inherit", "core/group", "/body/4"),
			}},
		{name: "the settings of a constrained layout are lost",
			text:  layout(`{"type":"constrained","contentSize":"800px","wideSize":"1200px","justifyContent":"center"}`),
			want:  jsonList(groupOf(0, "", "", jsonPara(1, "x"))),
			lines: []string{lostLine("layout.contentSize, layout.wideSize, layout.justifyContent", "core/group", "/body/0")}},
		{name: "a row", text: wpGroup(`{"tagName":"div","align":"wide","layout":{"type":"flex","flexWrap":"nowrap","justifyContent":"space-between","verticalAlignment":"center"}}`, "div", wpPara("x")),
			want: jsonList(groupOf(0, "", `"layout":{"type":"row","justify":"spaceBetween","verticalAlign":"center","nowrap":true},"align":"wide"`, jsonPara(1, "x")))},
		{name: "a row, by default and with the other alignments", text: layout(`{"type":"flex"}`) + layout(`{"type":"flex","justifyContent":"right","verticalAlignment":"stretch","flexWrap":"wrap"}`) +
			layout(`{"type":"flex","orientation":"horizontal","justifyContent":"left","verticalAlignment":"bottom"}`),
			want: jsonList(inLayout(`{"type":"row"}`),
				groupOf(2, "", `"layout":{"type":"row","justify":"right","verticalAlign":"stretch"}`, jsonPara(3, "x")),
				groupOf(4, "", `"layout":{"type":"row","justify":"left","verticalAlign":"bottom"}`, jsonPara(5, "x")))},
		{name: "a stack", text: layout(`{"type":"flex","orientation":"vertical","justifyContent":"stretch","verticalAlignment":"space-between"}`) +
			layout(`{"type":"flex","orientation":"vertical","justifyContent":"center","verticalAlignment":"top"}`),
			want: jsonList(groupOf(0, "", `"layout":{"type":"stack","justify":"stretch","verticalAlign":"spaceBetween"}`, jsonPara(1, "x")),
				groupOf(2, "", `"layout":{"type":"stack","justify":"center","verticalAlign":"top"}`, jsonPara(3, "x")))},
		{name: "a grid", text: layout(`{"type":"grid","columnCount":3,"minimumColumnWidth":"10rem"}`) + layout(`{"type":"grid","minimumColumnWidth":"12rem"}`) +
			layout(`{"type":"grid","columnCount":16}`) + layout(`{"type":"grid"}`),
			want: jsonList(groupOf(0, "", `"layout":{"type":"grid","columns":3,"minColumnWidth":"10rem"}`, jsonPara(1, "x")),
				groupOf(2, "", `"layout":{"type":"grid","minColumnWidth":"12rem"}`, jsonPara(3, "x")),
				groupOf(4, "", `"layout":{"type":"grid","columns":16}`, jsonPara(5, "x")),
				groupOf(6, "", `"layout":{"type":"grid"}`, jsonPara(7, "x")))},
		{name: "layouts that whiteroom has not as they are",
			text: layout(`{"type":"flex","orientation":"vertical","justifyContent":"space-between","flexWrap":"nowrap"}`) +
				layout(`{"type":"flex","justifyContent":"stretch","verticalAlignment":"space-between","flexWrap":"wrap-reverse"}`) +
				layout(`{"type":"flex","justifyContent":"spaceBetween","contentSize":"1px"}`) +
				layout(`{"type":"grid","columnCount":17,"minimumColumnWidth":"50%","rowCount":2}`) +
				layout(`{"type":"grid","columnCount":0,"minimumColumnWidth":"1"}`),
			want: jsonList(inLayout(`{"type":"stack"}`),
				groupOf(2, "", `"layout":{"type":"row"}`, jsonPara(3, "x")),
				groupOf(4, "", `"layout":{"type":"row"}`, jsonPara(5, "x")),
				groupOf(6, "", `"layout":{"type":"grid"}`, jsonPara(7, "x")),
				groupOf(8, "", `"layout":{"type":"grid"}`, jsonPara(9, "x"))),
			lines: []string{
				lostLine("layout.justifyContent, layout.flexWrap", "core/group", "/body/0"),
				lostLine("layout.justifyContent, layout.verticalAlignment, layout.flexWrap", "core/group", "/body/1"),
				lostLine("layout.justifyContent, layout.contentSize", "core/group", "/body/2"),
				lostLine("layout.columnCount, layout.minimumColumnWidth, layout.rowCount", "core/group", "/body/3"),
				lostLine("layout.columnCount, layout.minimumColumnWidth", "core/group", "/body/4"),
			}},
		{name: "a layout of a type that whiteroom has not", text: layout(`{"type":"weird","x":1}`) + layout(`"flex"`) + layout(`[]`) + layout(`null`),
			want: jsonList(groupOf(0, "", "", jsonPara(1, "x")), groupOf(2, "", "", jsonPara(3, "x")), groupOf(4, "", "", jsonPara(5, "x")), groupOf(6, "", "", jsonPara(7, "x"))),
			lines: []string{
				lostLine("layout.type", "core/group", "/body/0"), lostLine("layout", "core/group", "/body/1"),
			}},
		{name: "the elements of a group", text: wpGroup(`{"tagName":"section"}`, "section", wpPara("a")) + wpGroup(`{"tagName":"article"}`, "article", wpPara("b")) +
			wpGroup(`{"tagName":"aside"}`, "aside", wpPara("c")) + wpGroup(`{"tagName":"div"}`, "div", wpPara("d")),
			want: jsonList(groupOf(0, "", `"tagName":"section"`, jsonPara(1, "a")), groupOf(2, "", `"tagName":"article"`, jsonPara(3, "b")),
				groupOf(4, "", `"tagName":"aside"`, jsonPara(5, "c")), groupOf(6, "", "", jsonPara(7, "d")))},
		{name: "the elements of a group that are a div here", text: wpGroup(`{"tagName":"main"}`, "main", wpPara("a")) + wpGroup(`{"tagName":"header"}`, "header", wpPara("b")) +
			wpGroup(`{"tagName":"footer"}`, "footer", wpPara("c")),
			want: jsonList(groupOf(0, "", "", jsonPara(1, "a")), groupOf(2, "", "", jsonPara(3, "b")), groupOf(4, "", "", jsonPara(5, "c"))),
			lines: []string{
				"changed: the group's <main> is a <div> here -> /body/0", "changed: the group's <header> is a <div> here -> /body/1",
				"changed: the group's <footer> is a <div> here -> /body/2",
			}},
		{name: "the alignment of a group", text: wpGroup(`{"align":"wide"}`, "div", wpPara("a")) + wpGroup(`{"align":"full"}`, "div", wpPara("b")) +
			wpGroup(`{"align":"left"}`, "div", wpPara("c")),
			want: jsonList(groupOf(0, "", `"align":"wide"`, jsonPara(1, "a")), groupOf(2, "", `"align":"full"`, jsonPara(3, "b")), groupOf(4, "", "", jsonPara(5, "c"))),
			lines: []string{
				lostLine("align", "core/group", "/body/2"),
			}},
		{name: "a group in a group", text: wpGroup("", "div", wpPara("a"), wpGroup(`{"layout":{"type":"flex"}}`, "div", wpPara("b")), wpPara("c")),
			want: jsonList(groupOf(0, "", "", jsonPara(1, "a"), groupOf(2, "", `"layout":{"type":"row"}`, jsonPara(3, "b")), jsonPara(4, "c")))},
		{name: "a group of WordPress before 5.9", text: wpBlock("group", `{"tagName":"section"}`,
			`<section class="wp-block-group"><div class="wp-block-group__inner-container">`+wpPara("old")+`</div></section>`),
			want: jsonList(groupOf(0, "", `"tagName":"section"`, jsonPara(1, "old")))},
		{name: "a group with HTML outside its blocks", text: wpBlock("group", "", `<div class="wp-block-group">text`+wpPara("a")+`</div>`),
			want:  jsonList(groupOf(0, "", "", jsonPara(1, "a"))),
			lines: []string{`skipped: the HTML of the block "core/group" outside its inner blocks is not imported -> /body/0`}},
		{name: "the settings of a group", settings: styleSettings(),
			text: wpGroup(`{"className":"is-style-fancy extra","backgroundColor":"vivid-red","style":{"spacing":{"blockGap":"1rem","padding":{"top":"1rem"}}},"layout":{"type":"flex"}}`, "div", wpPara("x")),
			want: jsonList(groupOf(0, `"variant":"fancy-theme","className":"extra","style":{"backgroundColor":{"preset":"red"},"padding":{"top":{"value":"1rem"}},"blockGap":{"value":"1rem"}}`,
				`"layout":{"type":"row"}`, jsonPara(1, "x")))},
		{name: "a group with a block that becomes a legacy block", text: wpGroup("", "div", wpPara("a"), blockA, wpPara("b")),
			want:  jsonList(groupOf(0, "", "", jsonPara(1, "a"), jsonLegacy(2, blockA, notConverted("acme/a")), jsonPara(3, "b"))),
			lines: []string{legacyLine(notConverted("acme/a"), "/body/0/content/1")}},
		{name: "a group without blocks is left out", text: wpGroup("", "div") + wpGroup("", "div", "\n\n"),
			want:  `[]`,
			lines: []string{"skipped: the empty group is removed -> /body", "skipped: the empty group is removed -> /body"}},
		{name: "a group whose blocks are left out is left out", text: wpGroup("", "div", wpBlock("heading", "", `<h2></h2>`)) + wpPara("after"),
			// The empty group took the ID 0, and the heading in it the ID 1.
			want:  jsonList(jsonPara(2, "after")),
			lines: []string{"skipped: the empty group is removed -> /body"}},
		{name: "a group with HTML outside its blocks and no blocks", text: wpBlock("group", "", `<div class="wp-block-group">text</div>`),
			want: `[]`,
			lines: []string{
				`skipped: the HTML of the block "core/group" outside its inner blocks is not imported -> /body`,
				"skipped: the empty group is removed -> /body",
			}},
		{name: "a group that is not an element", text: wpBlock("group", "", `text`),
			want:  jsonList(jsonLegacy(0, wpBlock("group", "", `text`), notOne)),
			lines: []string{legacyLine(notOne, "/body/0")}},
	})
}

func TestTextDetails(t *testing.T) {
	const detailsMsg = "the details block is a group here, always open; whiteroom has no details blocks yet (stage 2)"
	details := func(attrs, markup string) string { return wpBlock("details", attrs, markup) }
	groupOf := func(id int, keys, attrs string, children ...string) string {
		return jsonBlock("group", id, keys, attrs, jsonList(children...))
	}
	changed := func(pointer string) string { return "changed: " + detailsMsg + " -> " + pointer }
	notDetails := notAsWritten + "<details>"
	runTextCases(t, []textCase{
		{name: "details", text: details("", `<details class="wp-block-details"><summary>Question?</summary>`+wpPara("Answer")+wpPara("More")+`</details>`),
			want: jsonList(groupOf(0, "", `"label":"Question?"`, jsonPara(1, "Answer"), jsonPara(2, "More"))), lines: []string{changed("/body/0")}},
		{name: "details with blocks of other kinds in them",
			text: details("", `<details class="wp-block-details"><summary>Q</summary>`+wpBlock("heading", "", `<h3>Head</h3>`)+wpBlock("list", "", `<ul><li>a</li></ul>`)+`</details>`),
			want: jsonList(groupOf(0, "", `"label":"Q"`, jsonBlock("heading", 1, "", `"level":3`, `[{"type":"text","text":"Head"}]`),
				jsonBlock("list", 2, "", "", jsonList(jsonItem(jsonPara(3, "a")))))),
			lines: []string{changed("/body/0")}},
		{name: "a summary with marks and a break", text: details("", `<details class="wp-block-details"><summary><strong>Bold</strong> and<br>more</summary>`+wpPara("A")+`</details>`),
			want: jsonList(groupOf(0, "", `"label":"Bold and more"`, jsonPara(1, "A"))),
			lines: []string{
				changed("/body/0"), "changed: kept only the text of <strong> -> /body/0",
			}},
		{name: "details open by default, with a name", text: details(`{"showContent":true,"name":"faq"}`, `<details class="wp-block-details" open name="faq"><summary>Q</summary>`+wpPara("A")+`</details>`),
			want: jsonList(groupOf(0, "", `"label":"Q"`, jsonPara(1, "A"))), lines: []string{changed("/body/0")}},
		{name: "details without a summary or blocks have an empty paragraph", text: details("", `<details class="wp-block-details"><summary></summary></details>`) +
			details("", `<details class="wp-block-details"></details>`),
			want:  jsonList(groupOf(0, "", "", jsonPara(1, "")), groupOf(2, "", "", jsonPara(3, ""))),
			lines: []string{changed("/body/0"), changed("/body/1")}},
		{name: "the settings of details", settings: styleSettings(),
			text:  details(`{"className":"is-style-fancy","backgroundColor":"vivid-red"}`, `<details id="d" class="wp-block-details"><summary>Q</summary>`+wpPara("A")+`</details>`),
			want:  jsonList(groupOf(0, `"variant":"fancy-theme","anchor":"d","style":{"backgroundColor":{"preset":"red"}}`, `"label":"Q"`, jsonPara(1, "A"))),
			lines: []string{changed("/body/0")}},
		{name: "details with HTML outside the summary and the blocks", text: details("", `<details class="wp-block-details">text<summary>Q</summary>`+wpPara("A")+`</details>`),
			want: jsonList(groupOf(0, "", `"label":"Q"`, jsonPara(1, "A"))),
			lines: []string{
				changed("/body/0"), `skipped: the HTML of the block "core/details" outside its inner blocks is not imported -> /body/0`,
			}},
		{name: "details in a group", text: wpGroup("", "div", details("", `<details class="wp-block-details"><summary>Q</summary>`+wpPara("A")+`</details>`)),
			want: jsonList(groupOf(0, "", "", groupOf(1, "", `"label":"Q"`, jsonPara(2, "A")))), lines: []string{changed("/body/0/content/0")}},
		{name: "details with an image in the summary", text: details("", `<details class="wp-block-details"><summary><img src="a.png"></summary></details>`),
			want: jsonList(jsonLegacy(1, details("", `<details class="wp-block-details"><summary><img src="a.png"></summary></details>`), "the text has <img>, which rich text cannot have")),
			lines: []string{
				legacyLine("the text has <img>, which rich text cannot have", "/body/0"),
			}},
		{name: "details that are not details", text: details("", `<div>x</div>`),
			want: jsonList(jsonLegacy(0, details("", `<div>x</div>`), notDetails)), lines: []string{legacyLine(notDetails, "/body/0")}},
	})
}

// wpAccordionItem is an item of an accordion as WordPress writes it, with the heading and the blocks of the panel.
func wpAccordionItem(heading string, blocks ...string) string {
	return "<!-- wp:accordion-item -->\n<div class=\"wp-block-accordion-item\">" + wpAccordionHeading(heading) + "\n\n" +
		wpBlock("accordion-panel", "", `<div role="region" class="wp-block-accordion-panel">`+strings.Join(blocks, "")+`</div>`) + "</div>\n<!-- /wp:accordion-item -->"
}

func wpAccordionHeading(title string) string {
	return wpBlock("accordion-heading", `{"level":3}`, `<h3 class="wp-block-accordion-heading"><button type="button" class="wp-block-accordion-heading__toggle">`+
		`<span class="wp-block-accordion-heading__toggle-title">`+title+`</span><span class="wp-block-accordion-heading__toggle-icon" aria-hidden="true">+</span></button></h3>`)
}

func wpAccordion(attrs string, items ...string) string {
	return wpBlock("accordion", attrs, `<div role="group" class="wp-block-accordion">`+strings.Join(items, "")+`</div>`)
}

func TestTextAccordions(t *testing.T) {
	const accordionMsg = "the accordion is groups here, always open; whiteroom has no accordion blocks"
	groupOf := func(id int, keys, attrs string, children ...string) string {
		return jsonBlock("group", id, keys, attrs, jsonList(children...))
	}
	changed := "changed: " + accordionMsg + " -> /body/0"
	oneItem := "the accordion item has a block other than a heading and a panel"
	button := "the text has <button>, which rich text cannot have"
	plainHeading := wpBlock("accordion-heading", "", `<h3 class="wp-block-accordion-heading">Plain</h3>`)
	buttonHeading := wpBlock("accordion-heading", "", `<h3 class="wp-block-accordion-heading"><button>Button</button></h3>`)
	itemOf := func(parts ...string) string {
		return "<!-- wp:accordion-item -->\n<div class=\"wp-block-accordion-item\">" + strings.Join(parts, "") + "</div>\n<!-- /wp:accordion-item -->"
	}
	panel := func(blocks ...string) string {
		return wpBlock("accordion-panel", "", `<div role="region" class="wp-block-accordion-panel">`+strings.Join(blocks, "")+`</div>`)
	}
	withButton := wpAccordion("", itemOf(buttonHeading, panel(wpPara("A"))))
	withTwo := wpAccordion("", itemOf(wpAccordionHeading("A"), wpAccordionHeading("B")))
	withTwoPanels := wpAccordion("", itemOf(wpAccordionHeading("A"), panel(wpPara("1")), panel(wpPara("2"))))
	withBlock := wpAccordion("", itemOf(wpAccordionHeading("A"), wpPara("x")))
	runTextCases(t, []textCase{
		{name: "an accordion", text: wpAccordion(`{"autoclose":false,"iconPosition":"right","showIcon":true,"headingLevel":3}`,
			wpAccordionItem("Question A", wpPara("Answer A"))+"\n\n"+wpAccordionItem("Question B", wpPara("Answer B1"), wpPara("Answer B2"))),
			want: jsonList(groupOf(0, "", "",
				groupOf(1, "", `"label":"Question A"`, jsonPara(2, "Answer A")),
				groupOf(3, "", `"label":"Question B"`, jsonPara(4, "Answer B1"), jsonPara(5, "Answer B2")))),
			lines: []string{changed}},
		{name: "an item that is open by default", text: wpAccordion("", "<!-- wp:accordion-item {\"openByDefault\":true} -->\n<div class=\"wp-block-accordion-item is-open\">"+
			wpAccordionHeading("Q")+panel(wpPara("A"))+"</div>\n<!-- /wp:accordion-item -->"),
			want: jsonList(groupOf(0, "", "", groupOf(1, "", `"label":"Q"`, jsonPara(2, "A")))), lines: []string{changed}},
		{name: "a heading that is only text", text: wpAccordion("", itemOf(plainHeading, panel(wpPara("A")))),
			want: jsonList(groupOf(0, "", "", groupOf(1, "", `"label":"Plain"`, jsonPara(2, "A")))), lines: []string{changed}},
		{name: "a heading with marks in the title", text: wpAccordion("", itemOf(wpAccordionHeading("<em>Q</em> one"), panel(wpPara("A")))),
			want: jsonList(groupOf(0, "", "", groupOf(1, "", `"label":"Q one"`, jsonPara(2, "A")))),
			lines: []string{
				changed, "changed: kept only the text of <em> -> /body/0/content/0",
			}},
		{name: "an item with a panel of many kinds of blocks and an accordion in it",
			text: wpAccordion("", itemOf(wpAccordionHeading("Outer"), panel(wpBlock("heading", "", `<h3>H</h3>`), wpAccordion("", wpAccordionItem("Inner", wpPara("Deep")))))),
			want: jsonList(groupOf(0, "", "", groupOf(1, "", `"label":"Outer"`,
				jsonBlock("heading", 2, "", `"level":3`, `[{"type":"text","text":"H"}]`),
				groupOf(3, "", "", groupOf(4, "", `"label":"Inner"`, jsonPara(5, "Deep")))))),
			lines: []string{changed, "changed: " + accordionMsg + " -> /body/0/content/0/content/1"}},
		{name: "an item without a panel or a heading", text: wpAccordion("", itemOf(wpAccordionHeading("Only a heading")), itemOf(panel(wpPara("Only a panel")))),
			want: jsonList(groupOf(0, "", "", groupOf(1, "", `"label":"Only a heading"`, jsonPara(2, "")),
				groupOf(3, "", "", jsonPara(4, "Only a panel")))),
			lines: []string{changed}},
		{name: "a block in an accordion that is not an item", text: wpAccordion("", itemOf(wpAccordionHeading("Q"), panel(wpPara("A"))), wpPara("after")),
			want: jsonList(groupOf(0, "", "", groupOf(1, "", `"label":"Q"`, jsonPara(2, "A")), jsonPara(3, "after"))), lines: []string{changed}},
		{name: "the settings of an accordion and of an item", settings: styleSettings(),
			text: wpBlock("accordion", `{"className":"is-style-fancy"}`, `<div id="acc" role="group" class="wp-block-accordion">`+
				"<!-- wp:accordion-item {\"backgroundColor\":\"vivid-red\"} -->\n<div class=\"wp-block-accordion-item\">"+wpAccordionHeading("Q")+panel(wpPara("A"))+"</div>\n<!-- /wp:accordion-item --></div>"),
			want: jsonList(groupOf(0, `"variant":"fancy-theme","anchor":"acc"`, "",
				groupOf(1, `"style":{"backgroundColor":{"preset":"red"}}`, `"label":"Q"`, jsonPara(2, "A")))),
			lines: []string{changed}},
		{name: "an accordion without items is left out", text: wpAccordion("") + wpAccordion("", "\n"),
			want:  `[]`,
			lines: []string{"skipped: the empty accordion is removed -> /body", "skipped: the empty accordion is removed -> /body"}},
		{name: "an accordion with HTML outside its items", text: wpBlock("accordion", "", `<div role="group" class="wp-block-accordion">text`+wpAccordionItem("Q", wpPara("A"))+`</div>`),
			want: jsonList(groupOf(0, "", "", groupOf(1, "", `"label":"Q"`, jsonPara(2, "A")))),
			lines: []string{
				changed, `skipped: the HTML of the block "core/accordion" outside its inner blocks is not imported -> /body/0`,
			}},
		{name: "an accordion with an item with a block that is not a heading or a panel", text: withBlock,
			want:  jsonList(jsonLegacy(1, withBlock, oneItem)),
			lines: []string{legacyLine(oneItem, "/body/0")}},
		{name: "an accordion with an item with two headings", text: withTwo,
			want:  jsonList(jsonLegacy(1, withTwo, oneItem)),
			lines: []string{legacyLine(oneItem, "/body/0")}},
		{name: "an accordion with an item with two panels", text: withTwoPanels,
			want:  jsonList(jsonLegacy(1, withTwoPanels, oneItem)),
			lines: []string{legacyLine(oneItem, "/body/0")}},
		{name: "an accordion with a heading that has a button and no title", text: withButton,
			want:  jsonList(jsonLegacy(2, withButton, button)),
			lines: []string{legacyLine(button, "/body/0")}},
		{name: "an accordion that is not an element", text: wpBlock("accordion", "", "text"),
			want:  jsonList(jsonLegacy(0, wpBlock("accordion", "", "text"), notAsWritten+"one element")),
			lines: []string{legacyLine(notAsWritten+"one element", "/body/0")}},
	})
}

func TestTextPartsOutside(t *testing.T) {
	outside := func(name, parent string) string {
		return fmt.Sprintf("the block %q is outside %q", name, parent)
	}
	item := wpBlock("list-item", "", "<li>x</li>")
	accItem := wpBlock("accordion-item", "", "<div></div>")
	heading := wpBlock("accordion-heading", "", "<h3>x</h3>")
	panel := wpBlock("accordion-panel", "", "<div></div>")
	runTextCases(t, []textCase{
		{name: "the parts of lists and accordions", text: item + accItem + heading + panel,
			want: jsonList(
				jsonLegacy(0, item, outside("core/list-item", "core/list")),
				jsonLegacy(1, accItem, outside("core/accordion-item", "core/accordion")),
				jsonLegacy(2, heading, outside("core/accordion-heading", "core/accordion-item")),
				jsonLegacy(3, panel, outside("core/accordion-panel", "core/accordion-item"))),
			lines: []string{
				legacyLine(outside("core/list-item", "core/list"), "/body/0"),
				legacyLine(outside("core/accordion-item", "core/accordion"), "/body/1"),
				legacyLine(outside("core/accordion-heading", "core/accordion-item"), "/body/2"),
				legacyLine(outside("core/accordion-panel", "core/accordion-item"), "/body/3"),
			}},
		{name: "a part in a group", text: wpGroup("", "div", item),
			want:  jsonList(jsonBlock("group", 0, "", "", jsonList(jsonLegacy(1, item, outside("core/list-item", "core/list"))))),
			lines: []string{legacyLine(outside("core/list-item", "core/list"), "/body/0/content/0")}},
	})
}

func TestTextMixed(t *testing.T) {
	// The places of the notes in blocks that hold blocks that hold parts.
	text := wpGroup("", "div",
		wpBlock("heading", "", `<h2>Head</h2>`),
		wpBlock("list", "", `<ul class="wp-block-list">`+wpBlock("list-item", "", "<li>a <kbd>k</kbd></li>")+`</ul>`),
		wpBlock("quote", "", `<blockquote class="wp-block-quote">`+wpPara("q <kbd>q</kbd>")+`<cite>c <kbd>c</kbd></cite></blockquote>`),
		wpBlock("table", "", `<figure class="wp-block-table"><table><tbody><tr><td>t <kbd>t</kbd></td></tr></tbody></table><figcaption>f <kbd>f</kbd></figcaption></figure>`))
	res, imp := textBody(t, bodySite(false), nil, text)
	if len(res.Blocks) != 1 || len(res.Blocks[0].Children) != 4 {
		t.Fatalf("the blocks are %+v", res.Blocks)
	}
	kept := func(pointer string) string { return "changed: kept only the text of <kbd> -> " + pointer }
	wantLines(t, imp.Report, bodyLines(
		kept("/body/0/content/1/content/0/content/0"),
		kept("/body/0/content/2/content/0"),
		kept("/body/0/content/2"),
		kept("/body/0/content/3/content/0/content/0/content"),
		kept("/body/0/content/3"),
	))
	// The IDs are in the order of the blocks: the group, the heading, the list and its paragraph, the quote and its
	// paragraph, the table.
	ids := []string{
		res.Blocks[0].ID, res.Blocks[0].Children[0].ID, res.Blocks[0].Children[1].ID, res.Blocks[0].Children[1].Children[0].Children[0].ID,
		res.Blocks[0].Children[2].ID, res.Blocks[0].Children[2].Children[0].ID, res.Blocks[0].Children[3].ID,
	}
	for i, id := range ids {
		if id != bodyIDs[i] {
			t.Errorf("the ID %d is %s, want %s", i, id, bodyIDs[i])
		}
	}
}

func TestTextConvertersAreRegistered(t *testing.T) {
	for _, name := range []string{
		"core/paragraph", "core/verse", "core/heading", "core/list", "core/list-item", "core/quote", "core/pullquote", "core/code",
		"core/preformatted", "core/table", "core/separator", "core/spacer", "core/html", "core/group", "core/details", "core/accordion",
		"core/accordion-item", "core/accordion-heading", "core/accordion-panel",
	} {
		if converters[name] == nil {
			t.Errorf("%s has no converter", name)
		}
		if dynamicBlock(name) {
			t.Errorf("%s is a dynamic block", name)
		}
	}
}

func TestTextAnchorsOfBlocksThatHoldBlocks(t *testing.T) {
	// The anchor of a block is taken before those of the blocks in it, as the document has them in that order.
	in := wpBlock("paragraph", "", `<p id="x">in</p>`)
	taken := `skipped: the anchor "x" is not imported: another block has it -> `
	one := func(typ string, keys string, children ...string) string {
		return jsonList(jsonBlock(typ, 0, `"anchor":"x"`+keys, "", jsonList(children...)))
	}
	runTextCases(t, []textCase{
		{name: "a group", text: wpBlock("group", "", `<div id="x" class="wp-block-group">`+in+`</div>`),
			want: one("group", "", jsonPara(1, "in")), lines: []string{taken + "/body/0/content/0"}},
		{name: "a quote", text: wpBlock("quote", "", `<blockquote id="x" class="wp-block-quote">`+in+`</blockquote>`),
			want: one("quote", "", jsonPara(1, "in")), lines: []string{taken + "/body/0/content/0"}},
		{name: "details", text: wpBlock("details", "", `<details id="x" class="wp-block-details"><summary>Q</summary>`+in+`</details>`),
			want: jsonList(jsonBlock("group", 0, `"anchor":"x"`, `"label":"Q"`, jsonList(jsonPara(1, "in")))),
			lines: []string{
				"changed: the details block is a group here, always open; whiteroom has no details blocks yet (stage 2) -> /body/0",
				taken + "/body/0/content/0",
			}},
		{name: "a list", text: wpBlock("list", "", `<ul id="x" class="wp-block-list">`+wpBlock("list-item", "", `<li id="x">in</li>`)+`</ul>`),
			want:  jsonList(jsonBlock("list", 0, `"anchor":"x"`, "", jsonList(jsonItem(jsonPara(1, "in"))))),
			lines: []string{taken + "/body/0/content/0/content/0"}},
		{name: "an accordion", text: wpBlock("accordion", "", `<div id="x" role="group" class="wp-block-accordion">`+wpAccordionItem("Q", in)+`</div>`),
			want: jsonList(jsonBlock("group", 0, `"anchor":"x"`, "", jsonList(jsonBlock("group", 1, "", `"label":"Q"`, jsonList(jsonPara(2, "in")))))),
			lines: []string{
				"changed: the accordion is groups here, always open; whiteroom has no accordion blocks -> /body/0",
				taken + "/body/0/content/0/content/0",
			}},
		{name: "an item of an accordion", text: wpAccordion("", `<!-- wp:accordion-item -->`+"\n"+`<div id="x" class="wp-block-accordion-item">`+
			wpAccordionHeading("Q")+wpBlock("accordion-panel", "", `<div role="region" class="wp-block-accordion-panel">`+in+`</div>`)+"</div>\n<!-- /wp:accordion-item -->"),
			want: jsonList(jsonBlock("group", 0, "", "", jsonList(jsonBlock("group", 1, `"anchor":"x"`, `"label":"Q"`, jsonList(jsonPara(2, "in")))))),
			lines: []string{
				"changed: the accordion is groups here, always open; whiteroom has no accordion blocks -> /body/0",
				taken + "/body/0/content/0/content/0",
			}},
	})
}

func TestTextAccordionItemsHaveOrigins(t *testing.T) {
	// The guard of Body can make an item a legacy block of its own text if it knows where the item comes from.
	text := wpAccordion("", wpAccordionItem("A", wpPara("a")), wpAccordionItem("B", wpPara("b")))
	imp := bodyImport()
	c := newBodyConv(imp, BodyOptions{Site: bodySite(false), Item: bodyItem(text), Target: bodyTarget, Key: bodyKey})
	blocks, _ := c.run()
	if len(blocks) != 1 || len(blocks[0].Children) != 2 {
		t.Fatalf("the blocks are %+v", blocks)
	}
	for _, item := range blocks[0].Children {
		o, ok := c.origins[item.ID]
		if !ok || o.b.Name != "core/accordion-item" || o.guard {
			t.Errorf("the origin of the item %s is %+v, %v", item.ID, o, ok)
		}
		if got := c.origins[blocks[0].ID].b.Name; got != "core/accordion" {
			t.Errorf("the origin of the accordion is %s", got)
		}
	}
}

func TestTextAnchorsOfBlocksLeftOut(t *testing.T) {
	// A block that is left out lets another have its anchor.
	heading := wpBlock("heading", "", `<h2 id="x">h</h2>`)
	headingOf := func(id int) string {
		return jsonBlock("heading", id, `"anchor":"x"`, `"level":2`, `[{"type":"text","text":"h"}]`)
	}
	runTextCases(t, []textCase{
		{name: "a list", text: wpBlock("list", "", `<ul id="x" class="wp-block-list"></ul>`) + heading,
			want: jsonList(headingOf(1)), lines: []string{"skipped: the empty list is removed -> /body"}},
		{name: "a list in an item", text: wpBlock("list", "", `<ul class="wp-block-list">`+
			"<!-- wp:list-item -->\n<li>One<!-- wp:list --><ul id=\"x\" class=\"wp-block-list\"></ul><!-- /wp:list --></li>\n<!-- /wp:list-item --></ul>") + heading,
			want:  jsonList(jsonBlock("list", 0, "", "", jsonList(jsonItem(jsonPara(1, "One")))), headingOf(3)),
			lines: []string{"skipped: the empty list is removed -> /body/0/content/0/content/0"}},
		{name: "a group", text: wpBlock("group", "", `<div id="x" class="wp-block-group"></div>`) + heading,
			want: jsonList(headingOf(1)), lines: []string{"skipped: the empty group is removed -> /body"}},
		{name: "an accordion", text: wpBlock("accordion", "", `<div id="x" role="group" class="wp-block-accordion"></div>`) + heading,
			want: jsonList(headingOf(1)), lines: []string{"skipped: the empty accordion is removed -> /body"}},
	})
}
