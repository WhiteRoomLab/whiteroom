package wpimport

import (
	"fmt"
	"strings"
	"testing"
)

// wpButton writes a button block of WordPress with the attributes and the content of its div.
func wpButton(attrs, inner string) string {
	return wpBlock("button", attrs, `<div class="wp-block-button">`+inner+`</div>`)
}

// buttonLink writes the a element of a button.
func buttonLink(href, text string) string {
	return `<a class="wp-block-button__link wp-element-button" href="` + href + `">` + text + `</a>`
}

func wpButtons(attrs string, buttons ...string) string {
	return wpBlock("buttons", attrs, `<div class="wp-block-buttons">`+strings.Join(buttons, "\n")+`</div>`)
}

func wpColumn(attrs string, blocks ...string) string {
	return wpBlock("column", attrs, `<div class="wp-block-column">`+strings.Join(blocks, "\n")+`</div>`)
}

func wpColumns(attrs string, columns ...string) string {
	return wpBlock("columns", attrs, `<div class="wp-block-columns">`+strings.Join(columns, "\n")+`</div>`)
}

// wpMediaText writes a media and text block with the content of its figure and the blocks of its text.
func wpMediaText(attrs, figure string, blocks ...string) string {
	return wpBlock("media-text", attrs, `<div class="wp-block-media-text is-stacked-on-mobile"><figure class="wp-block-media-text__media">`+figure+
		`</figure><div class="wp-block-media-text__content">`+strings.Join(blocks, "\n")+`</div></div>`)
}

// wpCover writes a cover block with the elements of its background and the blocks in it.
func wpCover(attrs, background string, blocks ...string) string {
	return wpBlock("cover", attrs, `<div class="wp-block-cover">`+background+`<div class="wp-block-cover__inner-container">`+strings.Join(blocks, "\n")+`</div></div>`)
}

// jsonHolder is the JSON of a block that holds blocks, with the n-th ID: the keys before the attributes, the attributes
// (the inside of the object), and the blocks in it.
func jsonHolder(typ string, n int, keys, attrs string, blocks ...string) string {
	content := ""
	if len(blocks) > 0 {
		content = jsonList(blocks...)
	}
	return jsonBlock(typ, n, keys, attrs, content)
}

func TestLayoutButtons(t *testing.T) {
	photo := mediaIDs.photo
	about := mediaIDs.about
	pointer := func(i, j int) string { return fmt.Sprintf("/body/%d/content/%d", i, j) }
	buttonOf := func(n int, keys, attrs string) string { return jsonBlock("button", n, keys, attrs, "") }
	// A buttons block with one button each, so that the settings of the buttons block can be told apart.
	one := func(attrs string) string {
		return wpButtons(attrs, wpButton(``, buttonLink("https://example.com/x", "Go")))
	}
	oneJSON := func(n int, attrs string) string {
		return jsonHolder("buttons", n, "", attrs, buttonOf(n+1, "", `"label":"Go","link":{"href":"/x"}`))
	}
	empty := wpButton(``, `<a class="wp-block-button__link"></a>`)
	runMediaCases(t, []textCase{
		{name: "buttons with links, a block style and the target of a link", settings: styleSettings(),
			text: wpButtons(`{"layout":{"type":"flex","justifyContent":"center"}}`,
				wpButton(`{"className":"is-style-fancy"}`, buttonLink("https://example.com/about/", "About")),
				wpButton(``, `<a class="wp-block-button__link wp-element-button" href="https://other.example/" target="_blank" rel="noreferrer noopener nofollow">Other</a>`)),
			want: jsonList(jsonHolder("buttons", 0, "", `"justify":"center"`,
				buttonOf(1, `"variant":"fancy-theme"`, `"label":"About","link":{"entry":"`+about+`"}`),
				buttonOf(2, "", `"label":"Other","link":{"href":"https://other.example/","newTab":true,"rel":["nofollow"]}`)))},
		{name: "where the buttons are put",
			text: one(`{"layout":{"type":"flex","justifyContent":"left"}}`) + one(`{"layout":{"justifyContent":"right"}}`) +
				one(`{"layout":{"justifyContent":"space-between"}}`) + one(`{"contentJustification":"center"}`) +
				one(`{"layout":{"justifyContent":"stretch"},"contentJustification":"right"}`) + one(`{"contentJustification":"around"}`) + one(``),
			want: jsonList(oneJSON(0, `"justify":"left"`), oneJSON(2, `"justify":"right"`), oneJSON(4, `"justify":"spaceBetween"`), oneJSON(6, `"justify":"center"`),
				oneJSON(8, ""), oneJSON(10, ""), oneJSON(12, "")),
			lines: []string{lostLine("layout.justifyContent", "core/buttons", "/body/4"), lostLine("contentJustification", "core/buttons", "/body/5")}},
		{name: "the settings of the layout that are lost",
			text: one(`{"layout":{"type":"flex","orientation":"vertical","flexWrap":"nowrap"}}`) + one(`{"layout":{"orientation":"horizontal","flexWrap":"wrap"}}`),
			want: jsonList(oneJSON(0, ""), oneJSON(2, "")),
			lines: []string{
				lostLine("layout.orientation, layout.flexWrap", "core/buttons", "/body/0"),
			}},
		{name: "the width of a button",
			text: wpButtons(``,
				wpButton(`{"width":25}`, buttonLink("/a", "A")), wpButton(`{"width":100}`, buttonLink("/b", "B")),
				wpButton(`{"style":{"dimensions":{"width":"75%"}}}`, buttonLink("/c", "C")), wpButton(`{"width":50,"style":{"dimensions":{"width":"25%"}}}`, buttonLink("/d", "D")),
				wpButton(`{"width":30}`, buttonLink("/e", "E")), wpButton(`{"style":{"dimensions":{"width":"40%","minHeight":"3rem"}}}`, buttonLink("/f", "F"))),
			want: jsonList(jsonHolder("buttons", 0, "", "",
				buttonOf(1, "", `"label":"A","link":{"href":"/a"},"width":"25%"`), buttonOf(2, "", `"label":"B","link":{"href":"/b"},"width":"100%"`),
				buttonOf(3, "", `"label":"C","link":{"href":"/c"},"width":"75%"`), buttonOf(4, "", `"label":"D","link":{"href":"/d"},"width":"50%"`),
				buttonOf(5, "", `"label":"E","link":{"href":"/e"}`), buttonOf(6, "", `"label":"F","link":{"href":"/f"}`))),
			lines: []string{
				lostLine("width", "core/button", pointer(0, 4)), lostLine("style.dimensions.width, style.dimensions.minHeight", "core/button", pointer(0, 5)),
			}},
		{name: "a button that is a button element, and has no link",
			text: wpButtons(``, wpButton(``, `<button class="wp-block-button__link wp-element-button" type="button" href="/x">Go</button>`), wpButton(``, `<a class="wp-block-button__link">Plain</a>`)),
			want: jsonList(jsonHolder("buttons", 0, "", "", buttonOf(1, "", `"label":"Go"`), buttonOf(2, "", `"label":"Plain"`)))},
		{name: "the links of a button: a file, and an address that cannot be a link",
			text:  wpButtons(``, wpButton(``, buttonLink(photoURL, "File")), wpButton(``, buttonLink("javascript:alert(1)", "Script"))),
			want:  jsonList(jsonHolder("buttons", 0, "", "", buttonOf(1, "", `"label":"File","link":{"media":"`+photo+`"}`), buttonOf(2, "", `"label":"Script"`))),
			lines: []string{`skipped: the link to "javascript:alert(1)" is removed: scheme "javascript" is not allowed -> ` + pointer(0, 1)}},
		{name: "a label with marks", text: wpButtons(``, wpButton(``, buttonLink("/a", "Go <strong>now</strong><br>please"))),
			want:  jsonList(jsonHolder("buttons", 0, "", "", buttonOf(1, "", `"label":"Go now please","link":{"href":"/a"}`))),
			lines: []string{"changed: kept only the text of <strong> -> " + pointer(0, 0)}},
		{name: "the text and the border of a button that are lost",
			text: wpButtons(``, wpButton(`{"textAlign":"center","borderRadius":4}`, buttonLink("/a", "A")), wpButton(`{"style":{"typography":{"textAlign":"right"}}}`, buttonLink("/b", "B"))),
			want: jsonList(jsonHolder("buttons", 0, "", "", buttonOf(1, "", `"label":"A","link":{"href":"/a"}`), buttonOf(2, "", `"label":"B","link":{"href":"/b"}`))),
			lines: []string{
				lostLine("textAlign, borderRadius", "core/button", pointer(0, 0)), lostLine("textAlign", "core/button", pointer(0, 1)),
			}},
		{name: "the style of a button", settings: styleSettings(),
			text: wpButtons(``, wpButton(`{"className":"is-style-fancy extra","backgroundColor":"vivid-red","textColor":"white","style":{"border":{"radius":"4px"},"spacing":{"padding":{"top":"1rem"}}}}`,
				buttonLink("/a", "A"))),
			want: jsonList(jsonHolder("buttons", 0, "", "", buttonOf(1,
				`"variant":"fancy-theme","className":"extra","style":{"textColor":{"preset":"white"},"backgroundColor":{"preset":"red"},"padding":{"top":{"value":"1rem"}},"border":{"radius":{"value":"4px"}}}`,
				`"label":"A","link":{"href":"/a"}`)))},
		{name: "the anchors of the buttons block and of a button",
			text: wpBlock("buttons", ``, `<div id="all" class="wp-block-buttons">`+wpButton(``, `<a class="wp-block-button__link" href="/a" id="x">A</a>`)+
				wpBlock("button", ``, `<div id="first" class="wp-block-button"><a class="wp-block-button__link" href="/b">B</a></div>`)+`</div>`),
			want: jsonList(jsonHolder("buttons", 0, `"anchor":"all"`, "", buttonOf(1, "", `"label":"A","link":{"href":"/a"}`),
				buttonOf(2, `"anchor":"first"`, `"label":"B","link":{"href":"/b"}`)))},
		{name: "a button that has attributes which are not JSON", text: wpButtons(``, wpButton(`{bad}`, buttonLink("/a", "A"))),
			want:  jsonList(jsonHolder("buttons", 0, "", "", buttonOf(1, "", `"label":"A","link":{"href":"/a"}`))),
			lines: []string{`changed: the attributes of the block "core/button" are not valid JSON; they are ignored -> ` + pointer(0, 0)}},
		{name: "text in the buttons block outside the buttons",
			text:  wpBlock("buttons", ``, `<div class="wp-block-buttons">text`+wpButton(``, buttonLink("/a", "A"))+`</div>`),
			want:  jsonList(jsonHolder("buttons", 0, "", "", buttonOf(1, "", `"label":"A","link":{"href":"/a"}`))),
			lines: []string{`skipped: the HTML of the block "core/buttons" outside its inner blocks is not imported -> /body/0`}},
		{name: "a label of nothing but a control character is none, and one with a control character keeps the rest",
			text: wpButtons(``, wpButton(``, buttonLink("/a", string(rune(0x11)))), wpButton(``, buttonLink("/b", "a"+string(rune(0x11))+"b "+string(rune(0x7f))))),
			// The first button took the ID 1.
			want:  jsonList(jsonHolder("buttons", 0, "", "", buttonOf(2, "", `"label":"ab","link":{"href":"/b"}`))),
			lines: []string{"skipped: the empty button is removed -> /body/0/content"}},
		{name: "a button without a text is left out", text: wpButtons(``, empty, wpButton(``, buttonLink("/a", "A")), wpButton(``, `<a class="wp-block-button__link" href="/b">  </a>`)),
			// The empty buttons took the IDs 1 and 3.
			want:  jsonList(jsonHolder("buttons", 0, "", "", buttonOf(2, "", `"label":"A","link":{"href":"/a"}`))),
			lines: []string{"skipped: the empty button is removed -> /body/0/content", "skipped: the empty button is removed -> /body/0/content"}},
		{name: "buttons without any button are left out", text: wpButtons(``, empty) + wpButtons(``) + wpPara("after"),
			// The first took the IDs 0 and 1, and the second none.
			want:  jsonList(jsonPara(2, "after")),
			lines: []string{"skipped: the empty buttons block is removed -> /body", "skipped: the empty buttons block is removed -> /body"}},
		{name: "buttons with a block that is not a button", text: wpButtons(``, wpButton(``, buttonLink("/a", "A")), wpPara("x")),
			want:  jsonList(jsonLegacy(0, wpButtons(``, wpButton(``, buttonLink("/a", "A")), wpPara("x")), "the buttons block has a block other than buttons")),
			lines: []string{legacyLine("the buttons block has a block other than buttons", "/body/0")}},
		{name: "a label that has an image makes the buttons block a legacy block",
			text:  wpButtons(``, wpButton(``, buttonLink("/a", `<img src="x.png">`+"A"))),
			want:  jsonList(jsonLegacy(2, wpButtons(``, wpButton(``, buttonLink("/a", `<img src="x.png">`+"A"))), "the text has <img>, which rich text cannot have")),
			lines: []string{legacyLine("the text has <img>, which rich text cannot have", "/body/0")}},
		{name: "a button that is not in a div", text: wpButtons(``, wpBlock("button", ``, `<a class="wp-block-button__link" href="/a">A</a>`)),
			want:  jsonList(jsonLegacy(0, wpButtons(``, wpBlock("button", ``, `<a class="wp-block-button__link" href="/a">A</a>`)), notAsWritten+"<div>")),
			lines: []string{legacyLine(notAsWritten+"<div>", "/body/0")}},
		{name: "a button outside the buttons block, as WordPress before 5.4 had it",
			text:  wpBlock("button", `{"align":"center"}`, `<div class="wp-block-button aligncenter"><a class="wp-block-button__link" href="/old">Old</a></div>`),
			want:  jsonList(jsonHolder("buttons", 0, "", "", buttonOf(1, "", `"label":"Old","link":{"href":"/old"}`))),
			lines: []string{lostLine("align", "core/button", "/body/0/content/0")}},
		{name: "a button outside the buttons block that has no text", text: wpButton(``, `<a class="wp-block-button__link"></a>`) + wpPara("after"),
			want:  jsonList(jsonPara(2, "after")),
			lines: []string{"skipped: the empty button is removed -> /body"}},
		{name: "buttons in a group", text: wpGroup("", "div", wpButtons(``, wpButton(``, buttonLink("/a", "A")))),
			want: jsonList(jsonHolder("group", 0, "", "", jsonHolder("buttons", 1, "", "", buttonOf(2, "", `"label":"A","link":{"href":"/a"}`))))},
	})
}

func TestLayoutButtonLinkBaseCountsAsHTML(t *testing.T) {
	// The link of a button is resolved against the link of the item, as a link in a text is (TestLinkBaseCountsAsHTML): what
	// resolving adds to the address counts among the bytes of HTML that the conversion reads.
	path := longBasePath()
	itemLink := "https://example.com" + path
	buttons := func(n int) string { return wpButtons(``, wpButton(``, buttonLink(fmt.Sprint(n), "A"))) }
	c, res := linkBytesBody(t, mediaImport(t), itemLink, len(itemLink), buttons)
	reason := fmt.Sprintf("the conversion has read more than %d bytes of HTML", maxHTMLBytes)
	button := func(id, n int) string {
		return jsonBlock("button", id, "", fmt.Sprintf(`"label":"A","link":{"href":"%s%d"}`, path, n), "")
	}
	// The third buttons block and its button took the IDs 4 and 5 before the link was over the limit.
	want := withIDs(jsonList(
		jsonHolder("buttons", 0, "", "", button(1, 1)), jsonHolder("buttons", 2, "", "", button(3, 2)), jsonLegacy(6, buttons(3), reason)))
	if got, _ := res.Blocks.MarshalJSON(); string(got) != want {
		t.Errorf("the blocks are\n%s\nwant\n%s", got, want)
	}
	wantLines(t, c.imp.Report, bodyLines(legacyLine(reason, "/body/2"), stopLine("/body")))
}

func TestLayoutCoverUrlCountsTheBase(t *testing.T) {
	// The address of the background of a cover is resolved against the link of the item to find the attachment, and counts the
	// length of the link as the address of a link does (TestLinkBaseCountsAsHTML): the cover whose address goes over the limit
	// is a legacy block, and the conversion stops.
	itemLink := "https://example.com" + longBasePath()
	cover := func(n int) string {
		return wpCover(fmt.Sprintf(`{"url":"../wp-content/uploads/2026/10/photo-scaled.jpg?n=%d"}`, n),
			`<img class="wp-block-cover__image-background" alt=""/>`, wpPara("in"))
	}
	c, res := linkBytesBody(t, mediaImport(t), itemLink, len(itemLink), cover)
	reason := fmt.Sprintf("the conversion has read more than %d bytes of HTML", maxHTMLBytes)
	group := func(n int) string {
		return jsonHolder("group", n, "", "", jsonImage(n+1, mediaIDs.photo, "", `"decorative":true`), jsonPara(n+2, "in"))
	}
	// The third cover took the ID 6 before its address was over the limit.
	want := withIDs(jsonList(group(0), group(3), jsonLegacy(7, cover(3), reason)))
	if got, _ := res.Blocks.MarshalJSON(); string(got) != want {
		t.Errorf("the blocks are\n%s\nwant\n%s", got, want)
	}
	const coverLine = "changed: the cover is a group here; whiteroom has no cover blocks yet (stage 2) -> "
	wantLines(t, c.imp.Report, bodyLines(coverLine+"/body/0", coverLine+"/body/1", legacyLine(reason, "/body/2"), stopLine("/body")))
}

func TestLayoutColumns(t *testing.T) {
	columnOf := func(n int, keys, attrs string, blocks ...string) string {
		return jsonHolder("column", n, keys, attrs, blocks...)
	}
	runMediaCases(t, []textCase{
		{name: "columns with widths and alignments",
			text: wpColumns(`{"verticalAlignment":"center","isStackedOnMobile":false,"align":"wide"}`,
				wpColumn(`{"width":"33.33%","verticalAlignment":"center"}`, wpPara("a")), wpColumn(`{"width":66.67}`, wpPara("b")), wpColumn(`{"width":"abc"}`, wpPara("c"))),
			want: jsonList(jsonHolder("columns", 0, "", `"verticalAlign":"center","stackOnMobile":false`,
				columnOf(1, "", `"width":"33.33%","verticalAlign":"center"`, jsonPara(2, "a")),
				columnOf(3, "", `"width":"66.67%"`, jsonPara(4, "b")),
				columnOf(5, "", "", jsonPara(6, "c")))),
			lines: []string{lostLine("align", "core/columns", "/body/0"), lostLine("width", "core/column", "/body/0/content/2")}},
		{name: "the widths of columns",
			text: wpColumns(``, wpColumn(`{"width":"300px"}`), wpColumn(`{"width":"10vw"}`), wpColumn(`{"width":"50"}`), wpColumn(`{"width":"-10%"}`),
				wpColumn(`{"width":true}`), wpColumn(`{"width":""}`), wpColumn(`{"width":12.5}`)),
			want: jsonList(jsonHolder("columns", 0, "", "",
				columnOf(1, "", `"width":"300px"`), columnOf(2, "", `"width":"10vw"`), columnOf(3, "", ""), columnOf(4, "", ""), columnOf(5, "", ""),
				columnOf(6, "", ""), columnOf(7, "", `"width":"12.5%"`))),
			lines: []string{
				lostLine("width", "core/column", "/body/0/content/2"), lostLine("width", "core/column", "/body/0/content/3"),
				lostLine("width", "core/column", "/body/0/content/4"),
			}},
		{name: "the vertical alignments",
			text: wpColumns(`{"verticalAlignment":"stretch"}`, wpColumn(`{"verticalAlignment":"stretch"}`), wpColumn(`{"verticalAlignment":"top"}`), wpColumn(`{"verticalAlignment":"middle"}`)),
			want: jsonList(jsonHolder("columns", 0, "", "", columnOf(1, "", `"verticalAlign":"stretch"`), columnOf(2, "", `"verticalAlign":"top"`), columnOf(3, "", ""))),
			lines: []string{
				lostLine("verticalAlignment", "core/columns", "/body/0"), lostLine("verticalAlignment", "core/column", "/body/0/content/2"),
			}},
		{name: "the style of columns and of a column", settings: styleSettings(),
			text: wpColumns(`{"className":"is-style-fancy extra","backgroundColor":"vivid-red","style":{"spacing":{"blockGap":{"left":"2rem","top":"1rem"}}}}`,
				wpBlock("column", `{"className":"col","style":{"spacing":{"blockGap":"1rem","padding":{"top":"1rem"}}}}`, `<div id="first" class="wp-block-column">`+wpPara("a")+`</div>`)),
			want: jsonList(jsonHolder("columns", 0, `"variant":"fancy-theme","className":"extra","style":{"backgroundColor":{"preset":"red"},"blockGap":{"value":"2rem"}}`, "",
				columnOf(1, `"className":"col","anchor":"first","style":{"padding":{"top":{"value":"1rem"}},"blockGap":{"value":"1rem"}}`, "", jsonPara(2, "a")))),
			lines: []string{`skipped: the blockGap.top "1rem" is not imported: whiteroom has one block gap, which is blockGap.left here -> /body/0`}},
		{name: "the anchor of the columns is taken before those of the blocks in them",
			text:  wpBlock("columns", ``, `<div id="x" class="wp-block-columns">`+wpColumn(``, wpBlock("paragraph", ``, `<p id="x">a</p>`))+`</div>`),
			want:  jsonList(jsonHolder("columns", 0, `"anchor":"x"`, "", columnOf(1, "", "", jsonPara(2, "a")))),
			lines: []string{`skipped: the anchor "x" is not imported: another block has it -> /body/0/content/0/content/0`}},
		{name: "text outside the columns and outside a column",
			text: wpBlock("columns", ``, `<div class="wp-block-columns">text`+wpBlock("column", ``, `<div class="wp-block-column">more`+wpPara("a")+`</div>`)+`</div>`),
			want: jsonList(jsonHolder("columns", 0, "", "", columnOf(1, "", "", jsonPara(2, "a")))),
			lines: []string{
				`skipped: the HTML of the block "core/columns" outside its inner blocks is not imported -> /body/0`,
				`skipped: the HTML of the block "core/column" outside its inner blocks is not imported -> /body/0/content/0`,
			}},
		{name: "a column that has attributes which are not JSON", text: wpColumns(``, wpColumn(`{bad}`, wpPara("a"))),
			want:  jsonList(jsonHolder("columns", 0, "", "", columnOf(1, "", "", jsonPara(2, "a")))),
			lines: []string{`changed: the attributes of the block "core/column" are not valid JSON; they are ignored -> /body/0/content/0`}},
		{name: "a column with a block that becomes a legacy block", text: wpColumns(``, wpColumn(``, wpPara("a"), blockA)),
			want:  jsonList(jsonHolder("columns", 0, "", "", columnOf(1, "", "", jsonPara(2, "a"), jsonLegacy(3, blockA, notConverted("acme/a"))))),
			lines: []string{legacyLine(notConverted("acme/a"), "/body/0/content/0/content/1")}},
		{name: "a column without blocks stays", text: wpColumns(``, wpColumn(``), wpColumn(``, wpPara("a"))),
			want: jsonList(jsonHolder("columns", 0, "", "", columnOf(1, "", ""), columnOf(2, "", "", jsonPara(3, "a"))))},
		{name: "columns without columns are left out", text: wpColumns(``) + wpColumns(``, "\n\n") + wpPara("after"),
			want:  jsonList(jsonPara(0, "after")),
			lines: []string{"skipped: the empty columns block is removed -> /body", "skipped: the empty columns block is removed -> /body"}},
		{name: "columns with a block that is not a column", text: wpColumns(``, wpColumn(``, wpPara("a")), wpPara("b")),
			want:  jsonList(jsonLegacy(0, wpColumns(``, wpColumn(``, wpPara("a")), wpPara("b")), "the columns block has a block other than columns")),
			lines: []string{legacyLine("the columns block has a block other than columns", "/body/0")}},
		{name: "a column outside columns", text: wpColumn(``, wpPara("a")),
			want:  jsonList(jsonLegacy(0, wpColumn(``, wpPara("a")), `the block "core/column" is outside "core/columns"`)),
			lines: []string{legacyLine(`the block "core/column" is outside "core/columns"`, "/body/0")}},
		{name: "columns and columns that are not divs", text: wpBlock("columns", ``, `<p>x</p>`) + wpBlock("columns", ``, `<div class="wp-block-columns">`+wpBlock("column", ``, `<p>x</p>`)+`</div>`),
			want: jsonList(
				jsonLegacy(0, wpBlock("columns", ``, `<p>x</p>`), notAsWritten+"<div>"),
				jsonLegacy(1, wpBlock("columns", ``, `<div class="wp-block-columns">`+wpBlock("column", ``, `<p>x</p>`)+`</div>`), notAsWritten+"<div>")),
			lines: []string{legacyLine(notAsWritten+"<div>", "/body/0"), legacyLine(notAsWritten+"<div>", "/body/1")}},
	})
}

func TestLayoutColumnsInColumns(t *testing.T) {
	columnOf := func(n int, blocks ...string) string { return jsonHolder("column", n, "", "", blocks...) }
	groupOf := func(n int, keys, attrs string, blocks ...string) string {
		return jsonHolder("group", n, keys, attrs, blocks...)
	}
	// grid is the layout of a group made of columns that have no widths and no alignment.
	grid := func(n int) string { return fmt.Sprintf(`"layout":{"type":"grid","columns":%d}`, n) }
	const (
		groupLine = "changed: the columns inside columns are a group here -> "
		widthLine = "changed: the columns inside columns are a group here; the widths of the columns are lost -> "
		nestedAt  = "/body/0/content/0/content/0"
	)
	// inColumn is a columns block with a column that has the blocks: the IDs 0 and 1 are theirs.
	inColumn := func(blocks ...string) string { return wpColumns(``, wpColumn(``, blocks...)) }
	inColumnJSON := func(blocks ...string) string {
		return jsonList(jsonHolder("columns", 0, "", "", columnOf(1, blocks...)))
	}
	// pair is columns with two columns that have the attributes, and a paragraph each.
	pair := func(attrs, first, second string) string {
		return wpColumns(attrs, wpColumn(first, wpPara("a")), wpColumn(second, wpPara("b")))
	}
	rowOf := func(n int, layout string) string {
		return groupOf(n, "", layout, groupOf(n+1, "", "", jsonPara(n+2, "a")), groupOf(n+3, "", "", jsonPara(n+4, "b")))
	}
	body := func(n string) string { return "/body/0/content/0/content/" + n }
	runMediaCases(t, []textCase{
		{name: "columns inside columns are a grid of groups", text: inColumn(pair(``, ``, ``)),
			want: inColumnJSON(rowOf(2, grid(2))), lines: []string{groupLine + nestedAt}},
		{name: "columns inside columns that have widths are a row", text: inColumn(pair(``, `{"width":"30%"}`, `{"width":"70%"}`)),
			want: inColumnJSON(rowOf(2, `"layout":{"type":"row"}`)), lines: []string{widthLine + nestedAt}},
		{name: "columns inside columns that have an alignment are a row",
			text:  inColumn(pair(`{"verticalAlignment":"bottom"}`, `{"verticalAlignment":"bottom"}`, `{"verticalAlignment":"top"}`)),
			want:  inColumnJSON(rowOf(2, `"layout":{"type":"row","verticalAlign":"bottom"}`)),
			lines: []string{groupLine + nestedAt, lostLine("verticalAlignment", "core/column", nestedAt+"/content/1")}},
		{name: "columns inside columns that are not stacked on mobile",
			text: inColumn(pair(`{"isStackedOnMobile":false}`, ``, ``), pair(`{"isStackedOnMobile":false}`, `{"width":"10%"}`, ``)),
			want: inColumnJSON(rowOf(2, grid(2)), rowOf(7, `"layout":{"type":"row","nowrap":true}`)),
			lines: []string{
				groupLine + nestedAt, widthLine + body("1"), lostLine("isStackedOnMobile", "core/columns", nestedAt),
			}},
		{name: "columns inside columns: what a group has not is lost", text: inColumn(pair(`{"align":"wide","verticalAlignment":"stretch"}`, ``, ``)),
			want:  inColumnJSON(rowOf(2, `"layout":{"type":"row"}`)),
			lines: []string{groupLine + nestedAt, lostLine("verticalAlignment, align", "core/columns", nestedAt)}},
		{name: "columns inside columns that have attributes which are not JSON", text: inColumn(wpColumns(`{bad}`, wpColumn(`{bad}`, wpPara("a")))),
			want: inColumnJSON(groupOf(2, "", grid(1), groupOf(3, "", "", jsonPara(4, "a")))),
			lines: []string{
				groupLine + nestedAt, `changed: the attributes of the block "core/columns" are not valid JSON; they are ignored -> ` + nestedAt,
				`changed: the attributes of the block "core/column" are not valid JSON; they are ignored -> ` + nestedAt + "/content/0",
			}},
		{name: "columns inside columns that have a style", settings: styleSettings(),
			text: inColumn(wpColumns(`{"className":"is-style-fancy","backgroundColor":"vivid-red","style":{"spacing":{"blockGap":"2rem"}}}`,
				wpBlock("column", `{"className":"col","style":{"spacing":{"padding":{"top":"1rem"}}}}`, `<div id="c" class="wp-block-column">`+wpPara("a")+`</div>`))),
			want: inColumnJSON(groupOf(2, `"variant":"fancy-theme","style":{"backgroundColor":{"preset":"red"},"blockGap":{"value":"2rem"}}`, grid(1),
				groupOf(3, `"className":"col","anchor":"c","style":{"padding":{"top":{"value":"1rem"}}}`, "", jsonPara(4, "a")))),
			lines: []string{groupLine + nestedAt}},
		{name: "columns inside columns inside columns",
			text:  inColumn(wpColumns(``, wpColumn(``, wpColumns(``, wpColumn(``, wpPara("deep"))), wpPara("next")))),
			want:  inColumnJSON(groupOf(2, "", grid(1), groupOf(3, "", "", groupOf(4, "", grid(1), groupOf(5, "", "", jsonPara(6, "deep"))), jsonPara(7, "next")))),
			lines: []string{groupLine + nestedAt, groupLine + nestedAt + "/content/0/content/0"}},
		{name: "columns in a group in a column", text: inColumn(wpGroup("", "div", wpColumns(``, wpColumn(``, wpPara("a"))))),
			want:  inColumnJSON(groupOf(2, "", "", groupOf(3, "", grid(1), groupOf(4, "", "", jsonPara(5, "a"))))),
			lines: []string{groupLine + nestedAt + "/content/0"}},
		{name: "columns in a quote in a column",
			text:  inColumn(wpBlock("quote", ``, `<blockquote class="wp-block-quote">`+wpColumns(``, wpColumn(``, wpPara("a")))+`</blockquote>`)),
			want:  inColumnJSON(jsonHolder("quote", 2, "", "", groupOf(3, "", grid(1), groupOf(4, "", "", jsonPara(5, "a"))))),
			lines: []string{groupLine + nestedAt + "/content/0"}},
		{name: "columns in details in a column",
			text: inColumn(wpBlock("details", ``, `<details class="wp-block-details"><summary>Q</summary>`+wpColumns(``, wpColumn(``, wpPara("a")))+`</details>`)),
			want: inColumnJSON(groupOf(2, "", `"label":"Q"`, groupOf(3, "", grid(1), groupOf(4, "", "", jsonPara(5, "a"))))),
			lines: []string{
				"changed: the details block is a group here, always open; whiteroom has no details blocks yet (stage 2) -> " + nestedAt,
				groupLine + nestedAt + "/content/0",
			}},
		{name: "columns inside columns that are in a block that is not in columns", text: wpGroup("", "div", wpColumns(``, wpColumn(``, wpPara("a")))),
			want: jsonList(groupOf(0, "", "", jsonHolder("columns", 1, "", "", columnOf(2, jsonPara(3, "a")))))},
	})
}

func TestLayoutColumnsInColumnsCount(t *testing.T) {
	many := func(n int) string {
		columns := make([]string, n)
		for i := range columns {
			columns[i] = wpColumn(``, wpPara("x"))
		}
		return wpColumns(``, columns...)
	}
	for _, tt := range []struct {
		columns int
		layout  string
		count   int
	}{{1, "grid", 1}, {16, "grid", 16}, {17, "row", 0}} {
		imp := mediaImport(t)
		res := convertMedia(t, imp, bodySite(false), nil, wpColumns(``, wpColumn(``, many(tt.columns))))
		g := res.Blocks[0].Children[0].Children[0]
		if g.Attrs.Layout.Type != tt.layout || g.Attrs.Layout.Columns != tt.count || len(g.Children) != tt.columns {
			t.Errorf("%d columns inside columns: the layout is %+v with %d blocks, want %s", tt.columns, g.Attrs.Layout, len(g.Children), tt.layout)
		}
	}
}

func TestLayoutMediaText(t *testing.T) {
	photo, banner, about := mediaIDs.photo, mediaIDs.banner, mediaIDs.about
	img := imgOf(photoURL, "A", 21)
	columnOf := func(n int, keys, attrs string, blocks ...string) string {
		return jsonHolder("column", n, keys, attrs, blocks...)
	}
	groupOf := func(n int, keys, attrs string, blocks ...string) string {
		return jsonHolder("group", n, keys, attrs, blocks...)
	}
	imageA := func(n int, extra string) string { return jsonImage(n, photo, "", `"alt":"A"`+extra) }
	const (
		columnsLine = "changed: the media and text block is columns here -> /body/0"
		groupLine   = "changed: the media and text block inside columns is a group here -> "
	)
	columnsAt := func(i int) string {
		return fmt.Sprintf("changed: the media and text block is columns here -> /body/%d", i)
	}
	// halves is the JSON of the media and text block with the image A and a paragraph, each in a column of 50%.
	halves := func(n int, attrs string) string {
		return jsonHolder("columns", n, "", attrs, columnOf(n+1, "", `"width":"50%"`, imageA(n+2, "")), columnOf(n+3, "", `"width":"50%"`, jsonPara(n+4, "Text")))
	}
	figure := func(inner string) string { return `<figure class="wp-block-media-text__media">` + inner + `</figure>` }
	runMediaCases(t, []textCase{
		{name: "an image and a text", text: wpMediaText(`{"mediaId":21,"mediaType":"image"}`, img, wpPara("Text")),
			want: jsonList(halves(0, "")), lines: []string{columnsLine}},
		{name: "the media on the right, the widths, the alignment and what is lost",
			text: wpMediaText(`{"mediaId":21,"mediaWidth":33.33,"mediaPosition":"right","verticalAlignment":"center","isStackedOnMobile":false,"imageFill":true,`+
				`"focalPoint":{"x":0.5,"y":0.5},"useFeaturedImage":true,"align":"wide","mediaSizeSlug":"full"}`, img, wpPara("Text")),
			want: jsonList(jsonHolder("columns", 0, "", `"verticalAlign":"center","stackOnMobile":false`,
				columnOf(1, "", `"width":"66.67%"`, jsonPara(2, "Text")), columnOf(3, "", `"width":"33.33%"`, imageA(4, "")))),
			lines: []string{lostLine("imageFill, focalPoint, useFeaturedImage, align", "core/media-text", "/body/0"), columnsLine}},
		{name: "the widths that are rounded, and the widths that are not between 0 and 100",
			text: wpMediaText(`{"mediaId":21,"mediaWidth":66.666}`, img, wpPara("Text")) + wpMediaText(`{"mediaId":21,"mediaWidth":99.999}`, img, wpPara("Text")) +
				wpMediaText(`{"mediaId":21,"mediaWidth":0}`, img, wpPara("Text")) + wpMediaText(`{"mediaId":21,"mediaWidth":"50"}`, img, wpPara("Text")) +
				wpMediaText(`{"mediaId":21,"mediaWidth":15}`, img, wpPara("Text")),
			want: jsonList(
				jsonHolder("columns", 0, "", "", columnOf(1, "", `"width":"66.67%"`, imageA(2, "")), columnOf(3, "", `"width":"33.33%"`, jsonPara(4, "Text"))),
				halves(5, ""), halves(10, ""), halves(15, ""),
				jsonHolder("columns", 20, "", "", columnOf(21, "", `"width":"15%"`, imageA(22, "")), columnOf(23, "", `"width":"85%"`, jsonPara(24, "Text")))),
			lines: []string{
				columnsAt(0), columnsAt(1), columnsAt(2), columnsAt(3), columnsAt(4),
				lostLine("mediaWidth", "core/media-text", "/body/1"), lostLine("mediaWidth", "core/media-text", "/body/2"), lostLine("mediaWidth", "core/media-text", "/body/3"),
			}},
		{name: "a link on the image", text: wpMediaText(`{"mediaId":22}`, `<a href="https://example.com/about/" target="_blank" rel="noopener">`+imgOf(bannerURL, "", 22)+`</a>`, wpPara("Text")),
			want: jsonList(jsonHolder("columns", 0, "", "",
				columnOf(1, "", `"width":"50%"`, jsonImage(2, banner, "", `"link":{"entry":"`+about+`","newTab":true}`)), columnOf(3, "", `"width":"50%"`, jsonPara(4, "Text")))),
			lines: []string{columnsLine, noAltLine + "/body/0/content/0/content/0/attrs/alt"}},
		{name: "the alt and the size of the image", settings: &Settings{Blocks: BlockSettings{ImageSizes: map[string]string{"large": "wide"}}},
			text: wpMediaText(`{"mediaId":22,"mediaAlt":"From the attribute","mediaSizeSlug":"large"}`, imgOf(bannerURL, "", 22), wpPara("Text")),
			want: jsonList(jsonHolder("columns", 0, "", "", columnOf(1, "", `"width":"50%"`, jsonImage(2, banner, "", `"alt":"From the attribute","size":"wide"`)),
				columnOf(3, "", `"width":"50%"`, jsonPara(4, "Text")))),
			lines: []string{columnsLine}},
		{name: "the image by its class, and the blocks of the text",
			text: wpMediaText(``, imgOf(photoURL, "A", 21), wpPara("a"), wpBlock("heading", ``, `<h3>b</h3>`)),
			want: jsonList(jsonHolder("columns", 0, "", "", columnOf(1, "", `"width":"50%"`, imageA(2, "")),
				columnOf(3, "", `"width":"50%"`, jsonPara(4, "a"), jsonBlock("heading", 5, "", `"level":3`, `[{"type":"text","text":"b"}]`)))),
			lines: []string{columnsLine}},
		{name: "a video", text: wpMediaText(`{"mediaId":5,"mediaType":"video"}`, `<video controls src="x.mp4"></video>`, wpPara("Text")),
			want: jsonList(jsonHolder("columns", 0, "", "",
				columnOf(1, "", `"width":"50%"`, jsonLegacy(2, figure(`<video controls="" src="x.mp4"></video>`), "whiteroom has no video blocks yet (stage 2)")),
				columnOf(3, "", `"width":"50%"`, jsonPara(4, "Text")))),
			lines: []string{columnsLine, legacyLine("whiteroom has no video blocks yet (stage 2)", "/body/0/content/0/content/0")}},
		{name: "an image that is not imported", text: wpMediaText(`{"mediaId":99}`, `<img src="https://other.example/x.jpg" alt="x"/>`, wpPara("Text")),
			want: jsonList(jsonHolder("columns", 0, "", "",
				columnOf(1, "", `"width":"50%"`, jsonLegacy(2, figure(`<img src="https://other.example/x.jpg" alt="x">`), notImage)),
				columnOf(3, "", `"width":"50%"`, jsonPara(4, "Text")))),
			lines: []string{columnsLine, legacyLine(notImage, "/body/0/content/0/content/0")}},
		{name: "no image", text: wpMediaText(``, ``, wpPara("Text")),
			want:  jsonList(jsonHolder("columns", 0, "", "", columnOf(1, "", `"width":"50%"`), columnOf(2, "", `"width":"50%"`, jsonPara(3, "Text")))),
			lines: []string{columnsLine}},
		{name: "no text", text: wpMediaText(`{"mediaId":21}`, img),
			want:  jsonList(jsonHolder("columns", 0, "", "", columnOf(1, "", `"width":"50%"`, imageA(2, "")), columnOf(3, "", `"width":"50%"`))),
			lines: []string{columnsLine}},
		{name: "a media and text block without a figure", text: wpBlock("media-text", ``, `<div class="wp-block-media-text"><div class="wp-block-media-text__content"></div></div>`),
			want:  jsonList(jsonLegacy(0, wpBlock("media-text", ``, `<div class="wp-block-media-text"><div class="wp-block-media-text__content"></div></div>`), notAsWritten+"<figure>")),
			lines: []string{legacyLine(notAsWritten+"<figure>", "/body/0")}},
		{name: "the style, and the columns in the text", settings: styleSettings(),
			text: wpMediaText(`{"mediaId":21,"className":"is-style-fancy extra","backgroundColor":"vivid-red"}`, img, wpColumns(``, wpColumn(``, wpPara("x")))),
			want: jsonList(jsonHolder("columns", 0, `"variant":"fancy-theme","className":"extra","style":{"backgroundColor":{"preset":"red"}}`, "",
				columnOf(1, "", `"width":"50%"`, imageA(2, "")),
				columnOf(3, "", `"width":"50%"`, groupOf(4, "", `"layout":{"type":"grid","columns":1}`, groupOf(5, "", "", jsonPara(6, "x")))))),
			lines: []string{columnsLine, "changed: the columns inside columns are a group here -> /body/0/content/1/content/0"}},
		{name: "text next to the figure and the text", text: wpBlock("media-text", `{"mediaId":21}`,
			`<div class="wp-block-media-text">stray`+figure(img)+`<div class="wp-block-media-text__content">`+wpPara("Text")+`</div></div>`),
			want: jsonList(halves(0, "")),
			lines: []string{
				`skipped: the HTML of the block "core/media-text" outside its inner blocks is not imported -> /body/0`, columnsLine,
			}},
		{name: "a media and text block that has attributes which are not JSON", text: wpMediaText(`{bad}`, img, wpPara("Text")),
			want:  jsonList(halves(0, "")),
			lines: []string{`changed: the attributes of the block "core/media-text" are not valid JSON; they are ignored -> /body/0`, columnsLine}},

		// Inside columns, a media and text block is a group of two groups.
		{name: "a media and text block inside columns",
			text: wpColumns(``, wpColumn(``, wpMediaText(`{"mediaId":21,"mediaWidth":30}`, img, wpPara("Text")))),
			want: jsonList(jsonHolder("columns", 0, "", "", columnOf(1, "", "", groupOf(2, "", `"layout":{"type":"row"}`,
				groupOf(3, "", "", imageA(4, "")), groupOf(5, "", "", jsonPara(6, "Text")))))),
			lines: []string{lostLine("mediaWidth", "core/media-text", "/body/0/content/0/content/0"), groupLine + "/body/0/content/0/content/0"}},
		{name: "a media and text block inside columns, with the media on the right",
			text: wpColumns(``, wpColumn(``, wpMediaText(`{"mediaId":21,"mediaPosition":"right","verticalAlignment":"bottom","isStackedOnMobile":false}`, img, wpPara("Text")))),
			want: jsonList(jsonHolder("columns", 0, "", "", columnOf(1, "", "", groupOf(2, "", `"layout":{"type":"row","verticalAlign":"bottom","nowrap":true}`,
				groupOf(3, "", "", jsonPara(4, "Text")), groupOf(5, "", "", imageA(6, "")))))),
			lines: []string{groupLine + "/body/0/content/0/content/0"}},
		{name: "a media and text block inside columns with a video",
			text: wpColumns(``, wpColumn(``, wpMediaText(`{"mediaType":"video"}`, `<video controls src="x.mp4"></video>`, wpPara("Text")))),
			want: jsonList(jsonHolder("columns", 0, "", "", columnOf(1, "", "", groupOf(2, "", `"layout":{"type":"row"}`,
				groupOf(3, "", "", jsonLegacy(4, figure(`<video controls="" src="x.mp4"></video>`), "whiteroom has no video blocks yet (stage 2)")),
				groupOf(5, "", "", jsonPara(6, "Text")))))),
			lines: []string{groupLine + "/body/0/content/0/content/0", legacyLine("whiteroom has no video blocks yet (stage 2)", "/body/0/content/0/content/0/content/0/content/0")}},
	})
}

func TestLayoutCover(t *testing.T) {
	photo := mediaIDs.photo
	groupOf := func(n int, keys, attrs string, blocks ...string) string {
		return jsonHolder("group", n, keys, attrs, blocks...)
	}
	span := `<span aria-hidden="true" class="wp-block-cover__background has-background-dim"></span>`
	img := `<img class="wp-block-cover__image-background wp-image-21" alt="" src="` + photoURL + `" data-object-fit="cover"/>`
	backImage := func(n int, attrs string) string { return jsonImage(n, photo, "", attrs) }
	const (
		externalImg  = `<img class="wp-block-cover__image-background" alt="" src="https://other.example/x.jpg" data-object-fit="cover"/>`
		externalHTML = `<img class="wp-block-cover__image-background" alt="" src="https://other.example/x.jpg" data-object-fit="cover">`
	)
	const coverLine = "changed: the cover is a group here; whiteroom has no cover blocks yet (stage 2) -> "
	settings := &Settings{Blocks: emptyBlockSettings()}
	settings.Blocks.Presets.Color["black"] = "dark"
	settings.Blocks.Presets.Gradient["vivid"] = "vivid-gradient"
	settings.Blocks.Presets.Color["gone"] = ""
	settings.Blocks.Presets.Gradient["gone"] = ""
	runMediaCases(t, []textCase{
		{name: "a background image, a color over it and the text", settings: settings,
			text:  wpCover(`{"url":"`+photoURL+`","id":21,"alt":"Back","dimRatio":100,"overlayColor":"black"}`, span+img, wpPara("Over")),
			want:  jsonList(groupOf(0, `"style":{"backgroundColor":{"preset":"dark"}}`, "", backImage(1, `"alt":"Back"`), jsonPara(2, "Over"))),
			lines: []string{coverLine + "/body/0"}},
		{name: "the settings that are lost", settings: settings,
			text: wpCover(`{"url":"`+photoURL+`","id":21,"dimRatio":50,"focalPoint":{"x":0.5,"y":0.5},"minHeight":300,"minHeightUnit":"px","hasParallax":true,"isRepeated":true,`+
				`"contentPosition":"center left","useFeaturedImage":true,"isDark":false,"isUserOverlayColor":true,"templateLock":false}`, span+img, wpPara("Over")),
			want: jsonList(groupOf(0, "", "", backImage(1, `"decorative":true`), jsonPara(2, "Over"))),
			lines: []string{
				lostLine("dimRatio, hasParallax, isRepeated, focalPoint, minHeight, contentPosition, useFeaturedImage", "core/cover", "/body/0"), coverLine + "/body/0",
			}},
		{name: "settings that are not lost", settings: settings,
			text: wpCover(`{"url":"`+photoURL+`","id":21,"dimRatio":100,"hasParallax":false,"isRepeated":false,"useFeaturedImage":false}`, span+img, wpPara("Over")) +
				wpCover(`{"url":"`+photoURL+`","id":21,"dimRatio":0,"overlayColor":"black"}`, span+img, wpPara("Over")),
			want:  jsonList(groupOf(0, "", "", backImage(1, `"decorative":true`), jsonPara(2, "Over")), groupOf(3, "", "", backImage(4, `"decorative":true`), jsonPara(5, "Over"))),
			lines: []string{coverLine + "/body/0", coverLine + "/body/1"}},
		{name: "the amount of the color", settings: settings,
			text:  wpCover(`{"overlayColor":"black","dimRatio":"half"}`, span, wpPara("a")) + wpCover(`{"overlayColor":"black"}`, span, wpPara("b")),
			want:  jsonList(groupOf(0, `"style":{"backgroundColor":{"preset":"dark"}}`, "", jsonPara(1, "a")), groupOf(2, `"style":{"backgroundColor":{"preset":"dark"}}`, "", jsonPara(3, "b"))),
			lines: []string{lostLine("dimRatio", "core/cover", "/body/0"), coverLine + "/body/0", coverLine + "/body/1"}},
		{name: "colors written as values, and gradients", settings: settings,
			text: wpCover(`{"customOverlayColor":"#ff0000","gradient":"vivid"}`, span, wpPara("a")) + wpCover(`{"customGradient":"linear-gradient(red, blue)"}`, span, wpPara("b")) +
				wpCover(`{"overlayColor":"gone","customOverlayColor":"red","gradient":"gone"}`, span, wpPara("c")),
			want: jsonList(groupOf(0, `"style":{"backgroundColor":{"value":"#ff0000"},"gradient":{"preset":"vivid-gradient"}}`, "", jsonPara(1, "a")),
				groupOf(2, "", "", jsonPara(3, "b")), groupOf(4, "", "", jsonPara(5, "c"))),
			lines: []string{
				coverLine + "/body/0", coverLine + "/body/1", coverLine + "/body/2",
				`skipped: the gradient "linear-gradient(red, blue)" is not imported: whiteroom has gradient presets only -> /body/1`,
				`skipped: the color preset "gone" is not imported: blocks.presets.color maps it to nothing -> /body/2`,
				`skipped: the gradient preset "gone" is not imported: blocks.presets.gradient maps it to nothing -> /body/2`,
			}},
		{name: "a background image by its address, and a background that is not imported",
			text: wpCover(`{"url":"`+photoURL+`"}`, span+img, wpPara("a")) + wpCover(`{"url":"https://other.example/x.jpg","id":99}`, span, wpPara("b")) +
				wpCover(`{"id":99}`, span, wpPara("c")) + wpCover(`{"url":"https://example.com/wp-content/uploads/2026/10/report.pdf"}`, span, wpPara("d")) +
				wpCover(``, span, wpPara("e")),
			want: jsonList(groupOf(0, "", "", backImage(1, `"decorative":true`), jsonPara(2, "a")), groupOf(3, "", "", jsonPara(4, "b")), groupOf(5, "", "", jsonPara(6, "c")),
				groupOf(7, "", "", jsonPara(8, "d")), groupOf(9, "", "", jsonPara(10, "e"))),
			lines: []string{
				coverLine + "/body/0", coverLine + "/body/1", coverLine + "/body/2", coverLine + "/body/3", coverLine + "/body/4",
				"skipped: the background image of the cover is not an imported attachment -> /body/1",
				"skipped: the background image of the cover is not an imported attachment -> /body/2",
				"skipped: the background image of the cover is not an imported attachment -> /body/3",
			}},
		{name: "a background image that is not an imported attachment is a legacy block of its HTML",
			text: wpCover(`{"url":"https://other.example/x.jpg","id":99}`, span+externalImg, wpPara("a")) +
				wpCover(`{"id":23}`, span+`<img class="wp-block-cover__image-background wp-image-23" alt="" src="`+reportURL+`" data-object-fit="cover"/>`, wpPara("b")) +
				wpCover(`{"url":"https://other.example/x.jpg"}`, span+externalImg, wpPara("c")),
			want: jsonList(
				groupOf(0, "", "", jsonLegacy(1, externalHTML, notImage), jsonPara(2, "a")),
				groupOf(3, "", "", jsonLegacy(4, `<img class="wp-block-cover__image-background wp-image-23" alt="" src="`+reportURL+`" data-object-fit="cover">`, pdfImage), jsonPara(5, "b")),
				groupOf(6, "", "", jsonLegacy(7, externalHTML, notImage), jsonPara(8, "c"))),
			lines: []string{
				coverLine + "/body/0", coverLine + "/body/1", coverLine + "/body/2",
				legacyLine(notImage, "/body/0/content/0"), legacyLine(pdfImage, "/body/1/content/0"), legacyLine(notImage, "/body/2/content/0"),
			}},
		{name: "a video in the background", text: wpCover(`{"url":"x.mp4","backgroundType":"video"}`, span+`<video class="wp-block-cover__video-background" src="x.mp4"></video>`, wpPara("a")),
			want:  jsonList(groupOf(0, "", "", jsonPara(1, "a"))),
			lines: []string{lostLine("backgroundType", "core/cover", "/body/0"), coverLine + "/body/0"}},
		{name: "the alt of the background image",
			text:  wpCover(`{"id":21,"alt":"First  line\nSecond"}`, img, wpPara("a")) + wpCover(`{"id":21,"alt":""}`, img, wpPara("b")),
			want:  jsonList(groupOf(0, "", "", backImage(1, `"alt":"First  line Second"`), jsonPara(2, "a")), groupOf(3, "", "", backImage(4, `"decorative":true`), jsonPara(5, "b"))),
			lines: []string{coverLine + "/body/0", coverLine + "/body/1"}},
		{name: "the alt of the background image that is nothing but a control character",
			text:  wpCover(`{"id":21,"alt":"\u0011"}`, img, wpPara("a")),
			want:  jsonList(groupOf(0, "", "", backImage(1, `"decorative":true`), jsonPara(2, "a"))),
			lines: []string{coverLine + "/body/0"}},
		{name: "the element, the alignment and the layout",
			text: wpCover(`{"tagName":"section","align":"full","layout":{"type":"flex","justifyContent":"center"}}`, span, wpPara("a")) +
				wpCover(`{"tagName":"main","align":"left"}`, span, wpPara("b")) + wpCover(`{"align":"wide"}`, span, wpPara("c")),
			want: jsonList(
				groupOf(0, "", `"layout":{"type":"row","justify":"center"},"tagName":"section","align":"full"`, jsonPara(1, "a")),
				groupOf(2, "", "", jsonPara(3, "b")), groupOf(4, "", `"align":"wide"`, jsonPara(5, "c"))),
			lines: []string{
				coverLine + "/body/0", coverLine + "/body/1", coverLine + "/body/2", "changed: the group's <main> is a <div> here -> /body/1",
				lostLine("align", "core/cover", "/body/1"),
			}},
		{name: "the style of the cover", settings: styleSettings(),
			text:  wpCover(`{"className":"is-style-fancy extra","textColor":"vivid-red","style":{"spacing":{"padding":{"top":"2rem"}},"dimensions":{"minHeight":"50vh"}}}`, span, wpPara("a")),
			want:  jsonList(groupOf(0, `"variant":"fancy-theme","className":"extra","style":{"textColor":{"preset":"red"},"padding":{"top":{"value":"2rem"}}}`, "", jsonPara(1, "a"))),
			lines: []string{coverLine + "/body/0", lostLine("style.dimensions", "core/cover", "/body/0")}},
		{name: "a cover without blocks",
			text: wpCover(`{"overlayColor":"black"}`, span) + wpCover(`{"id":21,"url":"`+photoURL+`"}`, img) + wpPara("after"),
			// The first took the ID 0. The second took the IDs 1 and 2, and is only its background.
			want:  jsonList(groupOf(1, "", "", backImage(2, `"decorative":true`)), jsonPara(3, "after")),
			lines: []string{"skipped: the empty cover is removed -> /body", coverLine + "/body/0"}},
		{name: "text in the cover outside its blocks", text: wpBlock("cover", ``, `<div class="wp-block-cover">stray`+span+`<div class="wp-block-cover__inner-container">`+wpPara("a")+`</div></div>`),
			want:  jsonList(groupOf(0, "", "", jsonPara(1, "a"))),
			lines: []string{`skipped: the HTML of the block "core/cover" outside its inner blocks is not imported -> /body/0`, coverLine + "/body/0"}},
		{name: "a cover of WordPress before 5.8: the image is the background of the element",
			text: wpBlock("cover", `{"url":"`+photoURL+`","id":21,"hasParallax":false,"dimRatio":40}`,
				`<div class="wp-block-cover has-background-dim-40 has-background-dim" style="background-image:url(`+photoURL+`)"><div class="wp-block-cover__inner-container">`+wpPara("a")+`</div></div>`),
			want:  jsonList(groupOf(0, "", "", backImage(1, `"decorative":true`), jsonPara(2, "a"))),
			lines: []string{lostLine("dimRatio", "core/cover", "/body/0"), coverLine + "/body/0"}},
		{name: "a cover with attributes which are not JSON", text: wpCover(`{bad}`, span, wpPara("a")),
			want:  jsonList(groupOf(0, "", "", jsonPara(1, "a"))),
			lines: []string{`changed: the attributes of the block "core/cover" are not valid JSON; they are ignored -> /body/0`, coverLine + "/body/0"}},
		{name: "columns in a cover in a column are a group",
			text: wpColumns(``, wpColumn(``, wpCover(`{"id":21}`, img, wpColumns(``, wpColumn(``, wpPara("a"))), wpPara("b")))),
			want: jsonList(jsonHolder("columns", 0, "", "", jsonHolder("column", 1, "", "",
				groupOf(2, "", "", backImage(3, `"decorative":true`), groupOf(4, "", `"layout":{"type":"grid","columns":1}`, groupOf(5, "", "", jsonPara(6, "a"))), jsonPara(7, "b"))))),
			lines: []string{coverLine + "/body/0/content/0/content/0", "changed: the columns inside columns are a group here -> /body/0/content/0/content/0/content/1"}},
		{name: "columns in a cover that is not in columns", text: wpCover(``, span, wpColumns(``, wpColumn(``, wpPara("a")))),
			want:  jsonList(groupOf(0, "", "", jsonHolder("columns", 1, "", "", jsonHolder("column", 2, "", "", jsonPara(3, "a"))))),
			lines: []string{coverLine + "/body/0"}},
		{name: "a cover whose block becomes a legacy block", text: wpCover(`{"id":21}`, img, blockA),
			want:  jsonList(groupOf(0, "", "", backImage(1, `"decorative":true`), jsonLegacy(2, blockA, notConverted("acme/a")))),
			lines: []string{coverLine + "/body/0", legacyLine(notConverted("acme/a"), "/body/0/content/1")}},
	})
}

func TestLayoutOrigins(t *testing.T) {
	// The groups that are made of the columns inside columns, and of a media and text block inside columns, are blocks of
	// a flow: the guard can make a legacy block of the text that they come from, if it knows which it is.
	img := imgOf(photoURL, "A", 21)
	for _, tt := range []struct {
		name, text string
		group      string   // the block of WordPress that the group comes from
		items      []string // and those that the groups in it come from
	}{
		{"columns inside columns", wpColumns(``, wpColumn(``, wpColumns(``, wpColumn(``, wpPara("a")), wpColumn(``, wpPara("b"))))),
			"core/columns", []string{"core/column", "core/column"}},
		{"a media and text block inside columns", wpColumns(``, wpColumn(``, wpMediaText(`{"mediaId":21}`, img, wpPara("Text")))),
			"core/media-text", []string{"core/media-text", "core/media-text"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := newBodyConv(mediaImport(t), BodyOptions{Site: bodySite(false), Item: bodyItem(tt.text), Target: bodyTarget, Key: bodyKey})
			blocks, _ := c.run()
			group := blocks[0].Children[0].Children[0]
			if o, ok := c.origins[group.ID]; !ok || o.guard || o.b.Name != tt.group {
				t.Errorf("the origin of the group is %+v, %v, want %s", o, ok, tt.group)
			}
			if len(group.Children) != len(tt.items) {
				t.Fatalf("the group has %d blocks, want %d", len(group.Children), len(tt.items))
			}
			for i, item := range group.Children {
				if o, ok := c.origins[item.ID]; !ok || o.guard || o.b.Name != tt.items[i] {
					t.Errorf("the origin of the block %d is %+v, %v, want %s", i, o, ok, tt.items[i])
				}
			}
		})
	}
}

func TestLayoutConvertersAreRegistered(t *testing.T) {
	for _, name := range []string{"core/buttons", "core/button", "core/columns", "core/column", "core/media-text", "core/cover"} {
		if converters[name] == nil {
			t.Errorf("%s has no converter", name)
		}
		if dynamicBlock(name) {
			t.Errorf("%s is a dynamic block", name)
		}
	}
}
