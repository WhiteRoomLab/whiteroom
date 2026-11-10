package wpimport

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/html"
)

// classicCase is a body, the blocks that it becomes (as JSON, with <n> for the IDs), the lines of the report, and where the
// read more tag is, if the body has one (More is -1 if it is nil).
type classicCase struct {
	name  string
	text  string
	want  string
	lines []string
	more  *int
}

func moreAt(n int) *int { return &n }

// freeform is the HTML in the classic block, which WordPress shows as it is, with no paragraphs made.
func freeform(markup string) string { return "<!-- wp:freeform -->" + markup + "<!-- /wp:freeform -->" }

// runClassic converts each body of the cases in the import of mediaExport, and checks what holds for every result.
func runClassic(t *testing.T, cases []classicCase) {
	t.Helper()
	runClassicIn(t, mediaImport, cases)
}

// runClassicIn is runClassic in the import that newImport makes.
func runClassicIn(t *testing.T, newImport func(testing.TB) *Import, cases []classicCase) {
	t.Helper()
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			imp := newImport(t)
			res := convertMedia(t, imp, bodySite(false), nil, tt.text)
			got, err := res.Blocks.MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
			if want := withIDs(tt.want); string(got) != want {
				t.Errorf("the blocks are\n%s\nwant\n%s", got, want)
			}
			wantMore := -1
			if tt.more != nil {
				wantMore = *tt.more
			}
			if res.More != wantMore {
				t.Errorf("More = %d, want %d", res.More, wantMore)
			}
			wantLines(t, imp.Report, bodyLines(tt.lines...))
		})
	}
}

// jsonText is the JSON of a piece of text with the marks, which are JSON too.
func jsonText(text string, marks ...string) string {
	s := `{"type":"text","text":` + strconv.Quote(text)
	if len(marks) > 0 {
		s += `,"marks":[` + strings.Join(marks, ",") + `]`
	}
	return s + "}"
}

const jsonBreak = `{"type":"hardBreak"}`

// jsonRich is a paragraph with the n-th ID and the pieces of text; keys are the keys before the content.
func jsonRich(n int, keys string, pieces ...string) string {
	return jsonBlock("paragraph", n, keys, "", jsonList(pieces...))
}

func jsonHeading(n int, keys, attrs, text string) string {
	return jsonBlock("heading", n, keys, attrs, jsonList(jsonText(text)))
}

const jsonBold = `{"type":"bold"}`

// img is an image of the medium photo, which is imported, as the classic editor writes it.
func classicImg(class string) string {
	return `<img class="` + class + `" src="` + photoURL + `" alt="Photo" width="300" height="200" />`
}

func TestClassicText(t *testing.T) {
	removed := func(tags, pointer string) string {
		return "skipped: removed " + tags + " with their content -> " + pointer
	}
	runClassic(t, []classicCase{
		{name: "paragraphs", text: "First paragraph.\n\nSecond paragraph.",
			want: jsonList(jsonPara(0, "First paragraph."), jsonPara(1, "Second paragraph."))},
		{name: "a line break makes a hard break", text: "Line one\nLine two\n\nNext",
			want: jsonList(jsonRich(0, "", jsonText("Line one"), jsonBreak, jsonText("Line two")), jsonPara(1, "Next"))},
		{name: "a br is a hard break, and a run of empty lines is one break between paragraphs", text: "A<br>B\n\n\n\n\nC",
			want: jsonList(jsonRich(0, "", jsonText("A"), jsonBreak, jsonText("B")), jsonPara(1, "C"))},
		{name: "marks and a link", text: `Some <strong>bold</strong> and <a href="https://example.com/about/">about</a>.`,
			want: jsonList(jsonRich(0, "", jsonText("Some "), jsonText("bold", jsonBold), jsonText(" and "),
				jsonText("about", `{"type":"link","attrs":{"entry":"`+mediaIDs.about+`"}}`), jsonText(".")))},
		{name: "a heading, and h1 is h2", text: "<h1>One</h1>\n<h3>Three</h3>",
			want:  jsonList(jsonHeading(0, "", `"level":2`, "One"), jsonHeading(1, "", `"level":3`, "Three")),
			lines: []string{"changed: the heading level 1 is 2 here; h1 is for the title -> /body/0"}},
		{name: "an empty heading is removed", text: "<h2> </h2>\n\nx", want: jsonList(jsonPara(1, "x")),
			lines: []string{"skipped: the empty heading is removed -> /body"}},
		// wpautop leaves a line break before the end tag of a heading that is written over several lines; it shows nothing.
		{name: "a heading written over several lines has no line break at its end", text: "<h2>\nHead\n</h2>",
			want: jsonList(jsonHeading(0, "", `"level":2`, "Head"))},
		{name: "a line break in the middle of a heading stays, and the one at its end goes", text: "<h3>One<br>Two<br></h3>\n\n<h2>\nOne\nTwo\n</h2>",
			want: jsonList(jsonBlock("heading", 0, "", `"level":3`, jsonList(jsonText("One"), jsonBreak, jsonText("Two"))),
				jsonBlock("heading", 1, "", `"level":2`, jsonList(jsonText("One"), jsonBreak, jsonText("Two"))))},
		{name: "a list, with a list in it", text: "<ul>\n<li>a</li>\n<li>b\n<ol start=\"3\"><li>c</li></ol></li>\n</ul>",
			want: jsonList(jsonBlock("list", 0, "", "", jsonList(
				jsonItem(jsonPara(1, "a")),
				jsonItem(jsonPara(2, "b"), jsonBlock("list", 3, "", `"ordered":true,"start":3`, jsonList(jsonItem(jsonPara(4, "c")))))))),
		},
		{name: "an ordered list of paragraphs in the items", text: "<ol>\n<li>a\n\nb</li>\n</ol>",
			want:  jsonList(jsonBlock("list", 0, "", `"ordered":true`, jsonList(jsonItem(jsonRich(1, "", jsonText("a"), jsonBreak, jsonText("b")))))),
			lines: []string{"changed: kept only the text of <p> -> /body/0/content/0/content/0"}},
		{name: "reversed and type are lost", text: `<ol reversed type="a"><li>x</li></ol>`,
			want:  jsonList(jsonBlock("list", 0, "", `"ordered":true`, jsonList(jsonItem(jsonPara(1, "x"))))),
			lines: []string{lostLine("reversed, type", "core/list", "/body/0")}},
		{name: "an empty list is removed", text: "<ul>\n</ul>\n\nx", want: jsonList(jsonPara(1, "x")),
			lines: []string{"skipped: the empty list is removed -> /body"}},
		{name: "a list that has other than items is a legacy block", text: freeform("<ul><li>a</li>text</ul>"),
			want: jsonList(jsonLegacy(2, "<ul><li>a</li>text</ul>", "the HTML of the block is not as WordPress writes it: expected <li>")),
			lines: []string{
				legacyLine("the HTML of the block is not as WordPress writes it: expected <li>", "/body/0"),
			}},
		{name: "a list that fails is a legacy block of all of its HTML, with the lists in it",
			text: freeform(`<ul><li>a<ul><li>b</li></ul></li><li><img src="https://other.example.org/x.png"> c<ul><li>d</li></ul></li></ul>`),
			want: jsonList(jsonLegacy(5, `<ul><li>a<ul><li>b</li></ul></li><li><img src="https://other.example.org/x.png"> c<ul><li>d</li></ul></li></ul>`,
				"the text has <img>, which rich text cannot have")),
			lines: []string{legacyLine("the text has <img>, which rich text cannot have", "/body/0")}},
		{name: "a list with an item that has two lists is a legacy block of all of its HTML", text: freeform(`<ul><li>a<ul><li>b</li></ul><ol><li>c</li></ol></li></ul>`),
			want:  jsonList(jsonLegacy(1, `<ul><li>a<ul><li>b</li></ul><ol><li>c</li></ol></li></ul>`, "a list item has blocks other than one list")),
			lines: []string{legacyLine("a list item has blocks other than one list", "/body/0")}},
		{name: "a quote whose citation cannot be text is a legacy block", text: freeform(`<blockquote><p>Quote</p><cite><img src="x.png"></cite></blockquote>`),
			want: jsonList(jsonLegacy(1, `<blockquote><p>Quote</p><cite><img src="https://example.com/x.png"></cite></blockquote>`,
				"the text has <img>, which rich text cannot have")),
			lines: []string{legacyLine("the text has <img>, which rich text cannot have", "/body/0")}},
		{name: "details whose summary cannot be text is a legacy block", text: freeform(`<details><summary><img src="x.png"></summary>d</details>`),
			want:  jsonList(jsonLegacy(1, `<details><summary><img src="https://example.com/x.png"></summary>d</details>`, "the text has <img>, which rich text cannot have")),
			lines: []string{legacyLine("the text has <img>, which rich text cannot have", "/body/0")}},
		{name: "a quote with paragraphs and a list", text: "<blockquote>\n<p>Quote</p>\n<ul><li>x</li></ul>\n</blockquote>",
			want: jsonList(jsonHolder("quote", 0, "", "", jsonPara(1, "Quote"),
				jsonBlock("list", 2, "", "", jsonList(jsonItem(jsonPara(3, "x"))))))},
		{name: "a quote with a citation", text: "<!-- wp:freeform --><blockquote><p>Quote</p><cite>Someone <b>else</b></cite></blockquote><!-- /wp:freeform -->",
			want: jsonList(jsonBlock("quote", 0, "", `"citation":[`+jsonText("Someone ")+`,`+jsonText("else", jsonBold)+`]`, jsonList(jsonPara(1, "Quote"))))},
		// wpautop puts a paragraph around a cite that comes last, or any text that is a line of its own.
		{name: "a quote with a citation that wpautop put in a paragraph", text: "<blockquote><p>Quote</p><cite>Someone <b>else</b></cite></blockquote>",
			want: jsonList(jsonBlock("quote", 0, "", `"citation":[`+jsonText("Someone ")+`,`+jsonText("else", jsonBold)+`]`, jsonList(jsonPara(1, "Quote"))))},
		{name: "a citation in a paragraph with white space and a comment around it", text: freeform("<blockquote>\n<p>Quote</p>\n<p> <!-- c --><cite>Someone</cite>\n</p>\n</blockquote>"),
			want: jsonList(jsonBlock("quote", 0, "", `"citation":[`+jsonText("Someone")+`]`, jsonList(jsonPara(1, "Quote"))))},
		{name: "the citation of the quote wins over the one in a paragraph", text: freeform("<blockquote><cite>One</cite><p>Quote</p><p><cite>Two</cite></p></blockquote>"),
			want:  jsonList(jsonBlock("quote", 0, "", `"citation":[`+jsonText("One")+`]`, jsonList(jsonPara(1, "Quote"), jsonPara(2, "Two")))),
			lines: []string{"changed: kept only the text of <cite> -> /body/0/content/1"}},
		{name: "a cite with text in its paragraph is the text of a paragraph", text: freeform("<blockquote><p>Quote</p><p>By <cite>Someone</cite></p></blockquote>"),
			want:  jsonList(jsonHolder("quote", 0, "", "", jsonPara(1, "Quote"), jsonPara(2, "By Someone"))),
			lines: []string{"changed: kept only the text of <cite> -> /body/0/content/1"}},
		{name: "a cite in a paragraph that does not come last is the text of a paragraph", text: freeform("<blockquote><p><cite>Someone</cite></p><p>Quote</p></blockquote>"),
			want:  jsonList(jsonHolder("quote", 0, "", "", jsonPara(1, "Someone"), jsonPara(2, "Quote"))),
			lines: []string{"changed: kept only the text of <cite> -> /body/0/content/0"}},
		{name: "a cite in a paragraph that has text after it in the quote", text: freeform("<blockquote><p>Quote</p><p><cite>Someone</cite></p>after</blockquote>"),
			want:  jsonList(jsonHolder("quote", 0, "", "", jsonPara(1, "Quote"), jsonPara(2, "Someone"), jsonPara(3, "after"))),
			lines: []string{"changed: kept only the text of <cite> -> /body/0/content/1"}},
		{name: "a cite in a paragraph that is two cites", text: freeform("<blockquote><p>Quote</p><p><cite>A</cite><cite>B</cite></p></blockquote>"),
			want:  jsonList(jsonHolder("quote", 0, "", "", jsonPara(1, "Quote"), jsonPara(2, "AB"))),
			lines: []string{"changed: kept only the text of <cite> -> /body/0/content/1"}},
		{name: "a citation and nothing else in the quote", text: freeform("<blockquote><p><cite>Someone</cite></p></blockquote>"),
			want: jsonList(jsonBlock("quote", 0, "", `"citation":[`+jsonText("Someone")+`]`, jsonList(jsonBlock("paragraph", 1, "", "", ""))))},
		{name: "a citation that is an empty paragraph", text: freeform("<blockquote><p>Quote</p><p><cite></cite></p></blockquote>"),
			want: jsonList(jsonHolder("quote", 0, "", "", jsonPara(1, "Quote")))},
		{name: "a citation that cannot be text is a legacy block of the quote", text: freeform(`<blockquote><p>Quote</p><p><cite><img src="x.png"></cite></p></blockquote>`),
			want:  jsonList(jsonLegacy(1, `<blockquote><p>Quote</p><p><cite><img src="https://example.com/x.png"></cite></p></blockquote>`, "the text has <img>, which rich text cannot have")),
			lines: []string{legacyLine("the text has <img>, which rich text cannot have", "/body/0")}},
		{name: "a quote with text only", text: "<blockquote>Words</blockquote>",
			want: jsonList(jsonHolder("quote", 0, "", "", jsonPara(1, "Words")))},
		{name: "an empty quote has an empty paragraph", text: "<blockquote></blockquote>",
			want: jsonList(jsonHolder("quote", 0, "", "", jsonBlock("paragraph", 1, "", "", "")))},
		{name: "a table", text: `<table><thead><tr><th>Name</th><th style="text-align: right">Qty</th></tr></thead>` +
			`<tbody><tr><td>Apple</td><td align="center">3</td></tr><tr><td colspan="2">Total</td></tr></tbody></table>`,
			want: jsonList(jsonBlock("table", 0, "", "", jsonList(
				jsonRow("head", jsonCell(`"header":true`, "Name"), jsonCell(`"header":true,"textAlign":"right"`, "Qty")),
				jsonRow("", jsonCell("", "Apple"), jsonCell(`"textAlign":"center"`, "3")),
				jsonRow("", jsonCell(`"colspan":2`, "Total")))))},
		{name: "a table with settings that are lost", text: `<table border="1" cellpadding="3" style="width: 100%; border-collapse: collapse"><tbody>` +
			`<tr bgcolor="#eee"><td style="text-align: center; width: 50%" valign="top" align="left">a</td></tr></tbody></table>`,
			want: jsonList(jsonBlock("table", 0, "", "", jsonList(jsonRow("", jsonCell(`"textAlign":"center"`, "a"))))),
			lines: []string{"skipped: lost style.width, style.border-collapse, border, cellpadding, bgcolor, valign, align of <table>; " +
				"whiteroom has no such settings -> /body/0"}},
		{name: "a table with a caption", text: "<table><caption>Title</caption><tr><td>a</td></tr></table>",
			want: jsonList(jsonBlock("table", 0, "", `"caption":[`+jsonText("Title")+`]`, jsonList(jsonRow("", jsonCell("", "a")))))},
		{name: "a table with an image in a cell is a legacy block, with the address of the image made absolute", text: freeform(`<table><tr><td>x <img src="a.png"></td></tr></table>`),
			want: jsonList(jsonLegacy(1, `<table><tbody><tr><td>x <img src="https://example.com/a.png"></td></tr></tbody></table>`,
				"the text has <img>, which rich text cannot have")),
			lines: []string{legacyLine("the text has <img>, which rich text cannot have", "/body/0")}},
		{name: "pre is code, with the language", text: "<pre class=\"language-go\">func main() {\n\treturn\n}</pre>",
			want: jsonList(jsonBlock("code", 0, "", `"language":"go"`, `"func main() {\n\treturn\n}"`))},
		{name: "code in pre", text: "<pre><code class=\"language-php\">echo 1;</code></pre>",
			want: jsonList(jsonBlock("code", 0, "", `"language":"php"`, `"echo 1;"`))},
		{name: "a rule", text: "a\n\n<hr />\n\nb",
			want: jsonList(jsonPara(0, "a"), jsonBlock("separator", 1, "", "", ""), jsonPara(2, "b"))},
		{name: "a div is a group", text: "<div>\nLine one\n\nLine two\n</div>",
			// wpautop leaves a closing p in the div, and the parser makes an empty p of it, which is not a paragraph.
			want: jsonList(jsonHolder("group", 0, "", "", jsonPara(1, "Line one"), jsonPara(2, "Line two")))},
		{name: "a div with a class, and a div in it", text: `<div class="box"><div id="in"><p>x</p></div></div>`,
			want: jsonList(jsonHolder("group", 0, `"className":"box"`, "", jsonHolder("group", 1, `"anchor":"in"`, "", jsonPara(2, "x"))))},
		{name: "section, article and aside stay", text: "<section>a</section><article>b</article><aside>c</aside>",
			want: jsonList(jsonHolder("group", 0, "", `"tagName":"section"`, jsonPara(1, "a")), jsonHolder("group", 2, "", `"tagName":"article"`, jsonPara(3, "b")),
				jsonHolder("group", 4, "", `"tagName":"aside"`, jsonPara(5, "c")))},
		{name: "main, header, footer, nav and center are a div", text: "<main>a</main><header>b</header><footer>c</footer><nav>d</nav><center>e</center>",
			want: jsonList(jsonHolder("group", 0, "", "", jsonPara(1, "a")), jsonHolder("group", 2, "", "", jsonPara(3, "b")),
				jsonHolder("group", 4, "", "", jsonPara(5, "c")), jsonHolder("group", 6, "", "", jsonPara(7, "d")), jsonHolder("group", 8, "", "", jsonPara(9, "e"))),
			lines: []string{
				"changed: the group's <main> is a <div> here -> /body/0", "changed: the group's <header> is a <div> here -> /body/1",
				"changed: the group's <footer> is a <div> here -> /body/2", "changed: the group's <nav> is a <div> here -> /body/3",
				"changed: the group's <center> is a <div> here -> /body/4",
			}},
		{name: "an empty div is removed", text: "<div>\n</div>\n\nx", want: jsonList(jsonPara(1, "x")), lines: []string{"skipped: the empty group is removed -> /body"}},
		{name: "details", text: "<details><summary>More <b>text</b></summary>\n<p>Inside</p></details>",
			want: jsonList(jsonHolder("group", 0, "", `"label":"More text"`, jsonPara(1, "Inside"))),
			lines: []string{
				"changed: the details block is a group here, always open; whiteroom has no details blocks yet (stage 2) -> /body/0",
				"changed: kept only the text of <b> -> /body/0",
			}},
		{name: "details without a summary", text: "<details><p>Inside</p></details>",
			want:  jsonList(jsonHolder("group", 0, "", "", jsonPara(1, "Inside"))),
			lines: []string{"changed: the details block is a group here, always open; whiteroom has no details blocks yet (stage 2) -> /body/0"}},
		{name: "an empty details has an empty paragraph", text: "<details><summary>S</summary></details>",
			want:  jsonList(jsonHolder("group", 0, "", `"label":"S"`, jsonBlock("paragraph", 1, "", "", ""))),
			lines: []string{"changed: the details block is a group here, always open; whiteroom has no details blocks yet (stage 2) -> /body/0"}},
		{name: "a figure with an image and a caption", text: `<figure>` + classicImg("size-large wp-image-21") + `<figcaption>Cap <i>tion</i></figcaption></figure>`,
			want: jsonList(jsonImage(0, mediaIDs.photo, "", `"alt":"Photo","caption":[`+jsonText("Cap ")+`,`+jsonText("tion", `{"type":"italic"}`)+`],"size":"large"`))},
		{name: "a figure with a table", text: `<figure><table><tbody><tr><td>a</td></tr></tbody></table><figcaption>Cap</figcaption></figure>`,
			want: jsonList(jsonBlock("table", 0, "", `"caption":[`+jsonText("Cap")+`]`, jsonList(jsonRow("", jsonCell("", "a")))))},
		{name: "a figure that is neither is a legacy block", text: freeform("<figure><p>x</p></figure>"),
			want:  jsonList(jsonLegacy(0, "<figure><p>x</p></figure>", "the element <figure> is not converted")),
			lines: []string{legacyLine("the element <figure> is not converted", "/body/0")}},
		{name: "an iframe of YouTube is an embed", text: `<iframe width="560" src="https://www.youtube.com/embed/dQw4w9WgXcQ" allowfullscreen></iframe>`,
			want:  jsonList(jsonBlock("embed", 0, "", `"provider":"youtube","url":"https://www.youtube.com/embed/dQw4w9WgXcQ"`, "")),
			lines: []string{"changed: the iframe is an embed block here -> /body/0"}},
		{name: "an iframe in an element, without a scheme", text: `<div><iframe src="//player.vimeo.com/video/76979871"></iframe></div>`,
			want:  jsonList(jsonHolder("group", 0, "", "", jsonBlock("embed", 1, "", `"provider":"vimeo","url":"https://player.vimeo.com/video/76979871"`, ""))),
			lines: []string{"changed: the iframe is an embed block here -> /body/0/content/0"}},
		// WordPress embeds the http address of a provider too, so it is looked for as an https address, and is one.
		{name: "an iframe with an http address is an embed with an https address",
			text: `<iframe src="http://www.youtube.com/embed/dQw4w9WgXcQ"></iframe>` + "\n\n" + `<div><iframe src="HTTP://player.vimeo.com/video/76979871"></iframe></div>` +
				"\n\n" + `<p><iframe src="Http://www.youtube.com/embed/abcdefghijk"></iframe></p>`,
			want: jsonList(
				jsonBlock("embed", 0, "", `"provider":"youtube","url":"https://www.youtube.com/embed/dQw4w9WgXcQ"`, ""),
				jsonHolder("group", 1, "", "", jsonBlock("embed", 2, "", `"provider":"vimeo","url":"https://player.vimeo.com/video/76979871"`, "")),
				jsonBlock("embed", 3, "", `"provider":"youtube","url":"https://www.youtube.com/embed/abcdefghijk"`, "")),
			lines: []string{
				"changed: the iframe is an embed block here -> /body/0", "changed: the iframe is an embed block here -> /body/1/content/0",
				"changed: the iframe is an embed block here -> /body/2",
			}},
		{name: "an iframe with an http address that no provider embeds is a legacy block as it was written",
			text: `<iframe src="http://example.org/x"></iframe>` + "\n\n" + `<iframe src="http://www.youtube.com:8080/embed/dQw4w9WgXcQ"></iframe>`,
			want: jsonList(
				jsonLegacy(0, `<iframe src="http://example.org/x"></iframe>`, "the iframe cannot be embedded: not a URL that a provider can embed"),
				jsonLegacy(1, `<iframe src="http://www.youtube.com:8080/embed/dQw4w9WgXcQ"></iframe>`, "the iframe cannot be embedded: not a URL that a provider can embed")),
			lines: []string{
				legacyLine("the iframe cannot be embedded: not a URL that a provider can embed", "/body/0"),
				legacyLine("the iframe cannot be embedded: not a URL that a provider can embed", "/body/1"),
			}},
		{name: "an iframe that no provider embeds is a legacy block", text: `<iframe src="https://example.org/x"></iframe>` + "\n\n" + `<iframe src="javascript:x"></iframe>` + "\n\n<iframe></iframe>",
			want: jsonList(
				jsonLegacy(0, `<iframe src="https://example.org/x"></iframe>`, "the iframe cannot be embedded: not a URL that a provider can embed"),
				jsonLegacy(1, `<iframe src="javascript:x"></iframe>`, `the iframe cannot be embedded: scheme "javascript" is not allowed`),
				jsonLegacy(2, `<iframe></iframe>`, "the iframe cannot be embedded: empty URL")),
			lines: []string{
				legacyLine("the iframe cannot be embedded: not a URL that a provider can embed", "/body/0"),
				legacyLine(`the iframe cannot be embedded: scheme "javascript" is not allowed`, "/body/1"),
				legacyLine("the iframe cannot be embedded: empty URL", "/body/2"),
			}},
		{name: "scripts and styles are left out with what they hold", text: "<script>alert(1)</script>\n\nx\n\n<div><style>p{}</style><p>y</p><template><b>z</b></template></div>",
			want: jsonList(jsonPara(1, "x"), jsonHolder("group", 2, "", "", jsonPara(3, "y"))),
			lines: []string{
				removed("<script>", "/body"), removed("<style>, <template>", "/body/1/content"),
			}},
		{name: "elements that are not converted are legacy blocks", text: freeform("<dl><dt>a</dt><dd>b</dd></dl>\n<form action=\"/x\"><input></form>\n<video src=\"v.mp4\"></video>"),
			want: jsonList(
				jsonLegacy(0, "<dl><dt>a</dt><dd>b</dd></dl>", "the element <dl> is not converted"),
				jsonLegacy(1, `<form action="/x"><input></form>`, "the element <form> is not converted"),
				jsonLegacy(2, `<video src="v.mp4"></video>`, "the element <video> is not converted")),
			lines: []string{
				legacyLine("the element <dl> is not converted", "/body/0"), legacyLine("the element <form> is not converted", "/body/1"),
				legacyLine("the element <video> is not converted", "/body/2"),
			}},
		{name: "a svg is a legacy block", text: freeform(`<svg><path d="M0 0"/></svg>`),
			want:  jsonList(jsonLegacy(0, `<svg><path d="M0 0"></path></svg>`, "the element <svg> is not converted")),
			lines: []string{legacyLine("the element <svg> is not converted", "/body/0")}},
		{name: "nothing is made of white space and line breaks", text: "\n\n<br>\n<br />\n\n  \n", want: "[]"},
	})
}

func TestClassicImages(t *testing.T) {
	const missing = "review: the image has no alt; set alt, or decorative for a decorative image -> "
	runClassic(t, []classicCase{
		{name: "an image alone in a paragraph", text: classicImg("size-medium wp-image-21"),
			want: jsonList(jsonImage(0, mediaIDs.photo, "", `"alt":"Photo","size":"medium"`))},
		{name: "an image with a link", text: `<a href="https://example.com/about/">` + classicImg("alignnone size-full wp-image-21") + `</a>`,
			want: jsonList(jsonImage(0, mediaIDs.photo, "", `"alt":"Photo","link":{"entry":"`+mediaIDs.about+`"}`))},
		{name: "an image on the left and the text next to it", text: classicImg("alignleft size-thumbnail wp-image-21") + "Text next to it\nand more",
			want: jsonList(jsonImage(0, mediaIDs.photo, "", `"alt":"Photo","size":"thumbnail","align":"left"`),
				jsonRich(1, "", jsonText("Text next to it"), jsonBreak, jsonText("and more")))},
		{name: "the text and an image at the end", text: "Text before it" + classicImg("aligncenter wp-image-21"),
			want: jsonList(jsonRich(0, "", jsonText("Text before it")), jsonImage(1, mediaIDs.photo, "", `"alt":"Photo","align":"center"`))},
		{name: "an image, a break and text", text: classicImg("alignright wp-image-21") + "<br />\nText",
			want: jsonList(jsonImage(0, mediaIDs.photo, "", `"alt":"Photo","align":"right"`), jsonPara(1, "Text"))},
		{name: "images in a row", text: classicImg("wp-image-21") + " " + classicImg("wp-image-21") + "<br>" + classicImg("wp-image-21"),
			want: jsonList(jsonImage(0, mediaIDs.photo, "", `"alt":"Photo"`), jsonImage(1, mediaIDs.photo, "", `"alt":"Photo"`),
				jsonImage(2, mediaIDs.photo, "", `"alt":"Photo"`))},
		{name: "text, an image at each end, and the text between", text: classicImg("alignleft wp-image-21") + "Between" + classicImg("alignright wp-image-21"),
			want: jsonList(jsonImage(0, mediaIDs.photo, "", `"alt":"Photo","align":"left"`), jsonPara(1, "Between"),
				jsonImage(2, mediaIDs.photo, "", `"alt":"Photo","align":"right"`))},
		{name: "the alignment of the paragraph is the alignment of an image that has none", text: `<p style="text-align: center">` + classicImg("wp-image-21") + `</p>` +
			`<p style="text-align: right">` + classicImg("alignleft wp-image-21") + `</p>`,
			want: jsonList(jsonImage(0, mediaIDs.photo, "", `"alt":"Photo","align":"center"`), jsonImage(1, mediaIDs.photo, "", `"alt":"Photo","align":"left"`)),
			// The second image has its own, which is not the alignment of its paragraph, so that is lost.
			lines: []string{elementLost("style.text-align", "<p>", "/body/1")}},
		{name: "an image that is aligned as the paragraph is loses nothing", text: `<p style="text-align: center">` + classicImg("aligncenter wp-image-21") + `</p>`,
			want: jsonList(jsonImage(0, mediaIDs.photo, "", `"alt":"Photo","align":"center"`))},
		{name: "the alignment of the paragraph is that of the text and of the image", text: `<p style="text-align: center">` + classicImg("wp-image-21") + `<br>Text</p>`,
			want: jsonList(jsonImage(0, mediaIDs.photo, "", `"alt":"Photo","align":"center"`), jsonBlock("paragraph", 1, "", `"textAlign":"center"`, jsonList(jsonText("Text"))))},
		{name: "the alignment of the paragraph is the alignment of an image after the text", text: `<p align="right">Text<br>` + classicImg("wp-image-21") + `</p>`,
			want: jsonList(jsonBlock("paragraph", 0, "", `"textAlign":"right"`, jsonList(jsonText("Text"))), jsonImage(1, mediaIDs.photo, "", `"alt":"Photo","align":"right"`))},
		{name: "alignnone is no alignment, so the paragraph's is the image's", text: `<p style="text-align: center">` + classicImg("alignnone size-medium wp-image-21") + `</p>`,
			want: jsonList(jsonImage(0, mediaIDs.photo, "", `"alt":"Photo","size":"medium","align":"center"`))},
		{name: "alignnone and a link around the image", text: `<p style="text-align: center"><a href="https://example.com/about/">` + classicImg("alignnone wp-image-21") + `</a></p>`,
			want: jsonList(jsonImage(0, mediaIDs.photo, "", `"alt":"Photo","link":{"entry":"`+mediaIDs.about+`"},"align":"center"`))},
		{name: "an image that is not imported does not take the alignment, which is lost", text: `<p style="text-align: center"><img src="https://other.example.org/x.png" alt="x"></p>`,
			want: jsonList(jsonLegacy(0, `<img src="https://other.example.org/x.png" alt="x">`, "the image is not an imported attachment")),
			lines: []string{
				elementLost("style.text-align", "<p>", "/body/0"),
				legacyLine("the image is not an imported attachment", "/body/0"),
			}},
		{name: "the attribute align of the paragraph, which no block has, is lost", text: `<p align="center"><iframe src="https://www.youtube.com/embed/dQw4w9WgXcQ"></iframe></p>`,
			want: jsonList(jsonBlock("embed", 0, "", `"provider":"youtube","url":"https://www.youtube.com/embed/dQw4w9WgXcQ"`, "")),
			lines: []string{
				elementLost("align", "<p>", "/body/0"),
				"changed: the iframe is an embed block here -> /body/0",
			}},
		{name: "the style and the attribute of the paragraph, which no block has, are lost", text: `<p style="text-align: center; color: red" align="center"><iframe src="https://www.youtube.com/embed/dQw4w9WgXcQ"></iframe></p>`,
			want: jsonList(jsonBlock("embed", 0, "", `"provider":"youtube","url":"https://www.youtube.com/embed/dQw4w9WgXcQ"`, "")),
			lines: []string{
				elementLost("style.text-align, style.color, align", "<p>", "/body/0"),
				"changed: the iframe is an embed block here -> /body/0",
			}},
		{name: "a paragraph of a URL that is embedded loses its alignment", text: `<p style="text-align: center;">https://www.youtube.com/watch?v=dQw4w9WgXcQ</p>`,
			want:  jsonList(jsonBlock("embed", 0, "", `"provider":"youtube","url":"https://www.youtube.com/watch?v=dQw4w9WgXcQ"`, "")),
			lines: []string{elementLost("style.text-align", "<p>", "/body/0")}},
		{name: "a figure takes the alignment and the size of its classes when the image has none", text: `<figure class="aligncenter size-large foo">` + classicImg("wp-image-21") + `</figure>`,
			want: jsonList(jsonImage(0, mediaIDs.photo, `"className":"foo"`, `"alt":"Photo","size":"large","align":"center"`))},
		{name: "the classes of the image win over the classes of the figure", text: `<figure class="alignright size-large bar">` + classicImg("alignleft size-thumbnail baz wp-image-21") + `</figure>`,
			want:  jsonList(jsonImage(0, mediaIDs.photo, `"className":"bar"`, `"alt":"Photo","size":"thumbnail","align":"left"`)),
			lines: []string{`skipped: the class "baz" is not imported: whiteroom blocks have one class name -> /body/0`}},
		{name: "a figure of WordPress's own classes", text: `<figure class="wp-caption alignnone wp-block-image">` + classicImg("wp-image-21") + `</figure>`,
			want: jsonList(jsonImage(0, mediaIDs.photo, "", `"alt":"Photo"`))},
		{name: "a figure with a width in its style: the setting is lost", text: `<figure class="wp-caption alignright" style="width: 300px" id="fig">` + classicImg("wp-image-21") + `<figcaption>Cap</figcaption></figure>`,
			want:  jsonList(jsonImage(0, mediaIDs.photo, `"anchor":"fig"`, `"alt":"Photo","caption":[`+jsonText("Cap")+`],"align":"right"`)),
			lines: []string{elementLost("style.width", "<figure>", "/body/0")}},
		{name: "an image in the middle of a sentence makes the paragraph a legacy block",
			text:  `<p>Before ` + classicImg("wp-image-21") + ` after</p>`,
			want:  jsonList(jsonLegacy(1, `<p>Before `+strings.Replace(classicImg("wp-image-21"), " />", ">", 1)+` after</p>`, "the text has <img>, which rich text cannot have")),
			lines: []string{legacyLine("the text has <img>, which rich text cannot have", "/body/0")}},
		{name: "an image in the middle of the text after an image at the start: the legacy block has all of the paragraph",
			text:  classicImg("alignleft wp-image-21") + `Before <img src="x.png"> after`,
			want:  jsonList(jsonLegacy(2, strings.ReplaceAll(`<p>`+classicImg("alignleft wp-image-21")+`Before <img src="https://example.com/x.png"> after</p>`, " />", ">"), "the text has <img>, which rich text cannot have")),
			lines: []string{legacyLine("the text has <img>, which rich text cannot have", "/body/0")}},
		{name: "an image that is not imported is a legacy block of its own", text: `<img src="https://other.example.org/x.png" alt="x">` + "Text",
			want: jsonList(jsonLegacy(0, `<img src="https://other.example.org/x.png" alt="x">`, "the image is not an imported attachment"), jsonPara(1, "Text")),
			lines: []string{
				legacyLine("the image is not an imported attachment", "/body/0"),
			}},
		{name: "a link around an image that is not imported", text: `<a href="/about/"><img src="data:image/gif;base64,R0lGOD" alt=""></a>`,
			want:  jsonList(jsonLegacy(0, `<a href="/about/"><img src="data:image/gif;base64,R0lGOD" alt=""></a>`, "the image is not an imported attachment")),
			lines: []string{legacyLine("the image is not an imported attachment", "/body/0")}},
		{name: "an image that is a PDF attachment", text: `<img src="` + reportURL + `" alt="">`,
			want:  jsonList(jsonLegacy(0, `<img src="`+reportURL+`" alt="">`, "the image is a PDF attachment")),
			lines: []string{legacyLine("the image is a PDF attachment", "/body/0")}},
		{name: "the settings of an image that are lost", text: `<img class="wp-image-22 foo" src="` + bannerURL + `" alt="" title="T" style="float: left; margin: 0" border="0" hspace="5" width="9">`,
			want: jsonList(jsonImage(0, mediaIDs.banner, `"className":"foo"`, "")),
			lines: []string{
				"skipped: lost style.float, style.margin, border, hspace, title of <img>; whiteroom has no such settings -> /body/0",
				missing + "/body/0/attrs/alt",
			}},
		{name: "an image with no alt is noted", text: `<img class="wp-image-22" src="` + bannerURL + `">`,
			want:  jsonList(jsonImage(0, mediaIDs.banner, "", "")),
			lines: []string{missing + "/body/0/attrs/alt"}},
		{name: "an image of a size that the settings do not know", text: `<img class="size-huge wp-image-21" src="` + photoURL + `" alt="Photo">`,
			want: jsonList(jsonImage(0, mediaIDs.photo, "", `"alt":"Photo","size":"huge"`))},
		{name: "the id of the paragraph is the anchor of the first block", text: `<p id="intro" class="lead">First<br>` + classicImg("wp-image-21") + `</p>`,
			want: jsonList(jsonBlock("paragraph", 0, `"className":"lead","anchor":"intro"`, "", jsonList(jsonText("First"))), jsonImage(1, mediaIDs.photo, "", `"alt":"Photo"`))},
		{name: "the id of the paragraph is the anchor of an image that comes first", text: `<p id="intro">` + classicImg("wp-image-21") + `<br>Text</p>`,
			want: jsonList(jsonImage(0, mediaIDs.photo, `"anchor":"intro"`, `"alt":"Photo"`), jsonPara(1, "Text"))},
	})
}

func TestClassicMore(t *testing.T) {
	skippedNotTop := "skipped: the more tag is removed: it is not at the top of the content -> "
	runClassic(t, []classicCase{
		{name: "a tag in a line of its own", text: "Before\n<!--more-->\nAfter",
			want: jsonList(jsonPara(0, "Before"), jsonPara(1, "After")), more: moreAt(1)},
		{name: "a tag between paragraphs", text: "One\n\nTwo\n\n<!--more-->\n\nThree",
			want: jsonList(jsonPara(0, "One"), jsonPara(1, "Two"), jsonPara(2, "Three")), more: moreAt(2)},
		{name: "a tag between elements", text: "<p>One</p>\n<!--more-->\n<p>Two</p>",
			want: jsonList(jsonPara(0, "One"), jsonPara(1, "Two")), more: moreAt(1)},
		{name: "a tag in the middle of a sentence counts the paragraph before it", text: "One <!--more--> two\n\nThree",
			want: jsonList(jsonPara(0, "One two"), jsonPara(1, "Three")), more: moreAt(1)},
		{name: "a tag at the start", text: "<!--more-->\nOne", want: jsonList(jsonPara(0, "One")), more: moreAt(0)},
		{name: "a tag at the end", text: "One\n<!--more-->", want: jsonList(jsonPara(0, "One")), more: moreAt(1)},
		{name: "a tag alone", text: "<!--more-->", want: "[]", more: moreAt(0)},
		{name: "the second tag is left out without a word", text: "One\n\n<!--more-->\n\nTwo\n\n<!--more-->\n\nThree",
			want: jsonList(jsonPara(0, "One"), jsonPara(1, "Two"), jsonPara(2, "Three")), more: moreAt(1)},
		{name: "a tag with a text", text: "One\n<!--more Read on-->\nTwo", want: jsonList(jsonPara(0, "One"), jsonPara(1, "Two")), more: moreAt(1),
			lines: []string{`skipped: the text "Read on" of the more link is not imported -> /body`}},
		{name: "the text of a tag, without the tags and the space around it", text: "One\n<!--more   <b>Go</b> on -->\nTwo", want: jsonList(jsonPara(0, "One"), jsonPara(1, "Two")), more: moreAt(1),
			lines: []string{`skipped: the text "Go on" of the more link is not imported -> /body`}},
		{name: "a comment that has a line feed is not a tag, and the line that has it is empty", text: "One\n<!--more\n-->\nTwo",
			want: jsonList(jsonRich(0, "", jsonText("One"), jsonBreak, jsonBreak, jsonText("Two")))},
		{name: "a tag that is more than more", text: "One\n<!--moreover-->\nTwo", want: jsonList(jsonPara(0, "One"), jsonPara(1, "Two")), more: moreAt(1),
			lines: []string{`skipped: the text "over" of the more link is not imported -> /body`}},
		{name: "a tag with noteaser", text: "One\n<!--more-->\n<!--noteaser-->\nTwo", want: jsonList(jsonPara(0, "One"), jsonPara(1, "Two")), more: moreAt(1),
			lines: []string{"changed: the more tag hid the text before it on the page (<!--noteaser-->); whiteroom shows it -> /body"}},
		{name: "a tag in an element is removed and noted", text: "<div>\n<p>One</p>\n<!--more-->\n<p>Two</p>\n</div>",
			want: jsonList(jsonHolder("group", 0, "", "", jsonPara(1, "One"), jsonPara(2, "Two"))), lines: []string{skippedNotTop + "/body/0/content"}},
		{name: "a tag in a paragraph in an element", text: "<div><p>One<br><!--more--><br>Two</p></div>",
			want: jsonList(jsonHolder("group", 0, "", "", jsonPara(1, "One"), jsonPara(2, "Two"))), lines: []string{skippedNotTop + "/body/0/content"}},
		{name: "a tag in an element does not make the tag of the content", text: "<div><!--more--></div>\n\nOne\n\n<!--more-->\n\nTwo",
			want:  jsonList(jsonPara(1, "One"), jsonPara(2, "Two")),
			more:  moreAt(1),
			lines: []string{"skipped: the empty group is removed -> /body"}},
		{name: "a tag in a quote", text: "<blockquote><p>One</p><!--more--></blockquote>",
			want: jsonList(jsonHolder("quote", 0, "", "", jsonPara(1, "One"))), lines: []string{skippedNotTop + "/body/0/content"}},
		{name: "the first tag of a paragraph counts, and the others are left out", text: "A<br><!--more--><br>B<br><!--more--><br>C",
			want: jsonList(jsonPara(0, "A"), jsonPara(1, "B"), jsonPara(2, "C")), more: moreAt(1)},
		{name: "a tag after an image in a line", text: classicImg("wp-image-21") + "<br><!--more--><br>Text",
			want: jsonList(jsonImage(0, mediaIDs.photo, "", `"alt":"Photo"`), jsonPara(1, "Text")), more: moreAt(1)},
		{name: "a tag in a text with an image at its end is before the image", text: "Text<!--more-->" + classicImg("wp-image-21"),
			want: jsonList(jsonPara(0, "Text"), jsonImage(1, mediaIDs.photo, "", `"alt":"Photo"`)), more: moreAt(1)},
		{name: "a tag in a paragraph that is a legacy block counts the legacy block before it", text: "<p>A<!--more--><img src=\"https://other.example.org/x.png\"> b</p>\n<p>C</p>",
			want: jsonList(jsonLegacy(1, "<p>A<!--more--><img src=\"https://other.example.org/x.png\"> b</p>", "the text has <img>, which rich text cannot have"), jsonPara(2, "C")),
			more: moreAt(1), lines: []string{legacyLine("the text has <img>, which rich text cannot have", "/body/0")}},
		{name: "a tag and the blocks before and after the HTML", text: "<!-- wp:acme/a /-->\n<p>One</p>\n<!--more-->\n<p>Two</p>\n<!-- wp:acme/b /-->",
			want: jsonList(jsonLegacy(0, blockA, notConverted("acme/a")), jsonPara(1, "One"), jsonPara(2, "Two"), jsonLegacy(3, blockB, notConverted("acme/b"))),
			more: moreAt(2),
			lines: []string{
				legacyLine(notConverted("acme/a"), "/body/0"), legacyLine(notConverted("acme/b"), "/body/3"),
			}},
		{name: "a tag in the HTML after a read more block is the second", text: "One\n" + moreBlock + "\n<p>Two</p>\n<!--more-->\n<p>Three</p>",
			want: jsonList(jsonPara(0, "One"), jsonPara(1, "Two"), jsonPara(2, "Three")), more: moreAt(1)},
		{name: "a read more block after a tag is the second", text: "<p>One</p>\n<!--more-->\n<p>Two</p>\n" + moreBlock,
			want: jsonList(jsonPara(0, "One"), jsonPara(1, "Two")), more: moreAt(1)},
		{name: "a tag in the classic block", text: "<!-- wp:acme/a /-->" + "<!-- wp:freeform --><p>One</p><!--more--><p>Two</p><!-- /wp:freeform -->",
			want: jsonList(jsonLegacy(0, blockA, notConverted("acme/a")), jsonPara(1, "One"), jsonPara(2, "Two")), more: moreAt(2),
			lines: []string{legacyLine(notConverted("acme/a"), "/body/0")}},
		{name: "a tag in the classic block in a group", text: wpGroup("", "div", freeform("<p>One</p><!--more--><p>Two</p>")),
			want:  jsonList(jsonHolder("group", 0, "", "", jsonPara(1, "One"), jsonPara(2, "Two"))),
			lines: []string{skippedNotTop + "/body/0/content"}},
	})
}

func TestClassicPageBreaks(t *testing.T) {
	pages := func(n int) string {
		verb := "are"
		if n == 1 {
			verb = "is"
		}
		return fmt.Sprintf("skipped: %s %s removed; whiteroom has no page breaks -> /body", count(n, "page break"), verb)
	}
	runClassic(t, []classicCase{
		{name: "pages of the content", text: "One\n\n<!--nextpage-->\n\nTwo\n\n<!--nextpage-->\n\nThree",
			want: jsonList(jsonPara(0, "One"), jsonPara(1, "Two"), jsonPara(2, "Three")), lines: []string{pages(2)}},
		{name: "one page break", text: "One<!--nextpage-->Two", want: jsonList(jsonPara(0, "One"), jsonPara(1, "Two")), lines: []string{pages(1)}},
		{name: "a page break at the start is not one", text: "<!--nextpage-->One", want: jsonList(jsonPara(0, "One"))},
		{name: "a page break splits the paragraphs of its page", text: "One\nTwo<!--nextpage-->Three\nFour",
			want:  jsonList(jsonRich(0, "", jsonText("One"), jsonBreak, jsonText("Two")), jsonRich(1, "", jsonText("Three"), jsonBreak, jsonText("Four"))),
			lines: []string{pages(1)}},
		{name: "page breaks in the HTML between blocks", text: "<!-- wp:acme/a /-->\n<p>One</p>\n<!--nextpage-->\n<p>Two</p>\n<!-- wp:acme/b /-->",
			want: jsonList(jsonLegacy(0, blockA, notConverted("acme/a")), jsonPara(1, "One"), jsonPara(2, "Two"), jsonLegacy(3, blockB, notConverted("acme/b"))),
			lines: []string{
				legacyLine(notConverted("acme/a"), "/body/0"), legacyLine(notConverted("acme/b"), "/body/3"), pages(1),
			}},
		{name: "a page break cuts the text that waits", text: "<!-- wp:acme/a /-->One<!--nextpage-->Two",
			want:  jsonList(jsonLegacy(0, blockA, notConverted("acme/a")), jsonPara(1, "One"), jsonPara(2, "Two")),
			lines: []string{legacyLine(notConverted("acme/a"), "/body/0"), pages(1)}},
		{name: "page breaks in the classic block, with the ones of blocks", text: "<!-- wp:freeform --><p>One</p><!--nextpage--><p>Two</p><!-- /wp:freeform -->" + pageBreak,
			want:  jsonList(jsonPara(0, "One"), jsonPara(1, "Two")),
			lines: []string{pages(2)}},
		{name: "page breaks in an element and in a paragraph", text: "<!-- wp:freeform --><div><p>One</p><!--nextpage--><p>Two<!--nextpage-->x</p></div><!-- /wp:freeform -->",
			want:  jsonList(jsonHolder("group", 0, "", "", jsonPara(1, "One"), jsonPara(2, "Twox"))),
			lines: []string{pages(2)}},
		{name: "page breaks in a content that is not blocks, with a delimiter that is not one", text: "<!-- wp:Foo -->One<!--nextpage-->Two",
			want: jsonList(jsonPara(0, "One"), jsonPara(1, "Two")), lines: []string{pages(1)}},
	})
}

func TestClassicSources(t *testing.T) {
	runClassic(t, []classicCase{
		{name: "the HTML between blocks has no paragraphs made by wpautop", text: wpPara("a") + "\n\nLine one\n\nLine two <b>x</b>\n" + wpPara("b"),
			want: jsonList(jsonPara(0, "a"), jsonRich(1, "", jsonText("Line one Line two "), jsonText("x", jsonBold)), jsonPara(2, "b"))},
		{name: "the elements and the text between blocks are paragraphs, and the elements are what they are",
			text: wpPara("a") + "Text<h2>Head</h2><p>P</p>more" + wpPara("b"),
			want: jsonList(jsonPara(0, "a"), jsonPara(1, "Text"), jsonHeading(2, "", `"level":2`, "Head"), jsonPara(3, "P"), jsonPara(4, "more"), jsonPara(5, "b"))},
		{name: "the classic block has none either", text: wpPara("a") + freeform("Line one\n\nLine two"),
			want: jsonList(jsonPara(0, "a"), jsonPara(1, "Line one Line two"))},
		{name: "the classic block in a group", text: wpGroup("", "div", freeform("<p>x</p>\n\ny"), wpPara("z")),
			want: jsonList(jsonHolder("group", 0, "", "", jsonPara(1, "x"), jsonPara(2, "y"), jsonPara(3, "z")))},
		{name: "the classic block in a quote", text: wpBlock("quote", "", `<blockquote class="wp-block-quote">`+freeform("<p>x</p>")+`</blockquote>`),
			want: jsonList(jsonHolder("quote", 0, "", "", jsonPara(1, "x")))},
		{name: "a classic block of nothing", text: wpPara("a") + freeform(" \n ") + freeform("") + wpPara("b"),
			want: jsonList(jsonPara(0, "a"), jsonPara(1, "b"))},
		{name: "wpautop makes the paragraphs of a content that has no delimiter", text: "Line one\n\nLine two",
			want: jsonList(jsonPara(0, "Line one"), jsonPara(1, "Line two"))},
		{name: "a delimiter that is not one keeps wpautop away", text: "<!-- wp:Foo -->Line one\n\nLine two",
			want: jsonList(jsonPara(0, "Line one Line two"))},
		{name: "a closer without an opener does not", text: "One\n\nTwo<!-- /wp:acme/a -->",
			want: jsonList(jsonPara(0, "One"), jsonPara(1, "Two"))},
		{name: "a line break in a paragraph is a hard break, and an empty line is a paragraph", text: "One\n\nTwo\nThree",
			want: jsonList(jsonPara(0, "One"), jsonRich(1, "", jsonText("Two"), jsonBreak, jsonText("Three")))},
	})
}

func TestClassicInSyncedPatterns(t *testing.T) {
	// WordPress does not make paragraphs of the content of a synced pattern, and splits the teaser and the pages on the content
	// of the post only: the tag and the page break of a copy are comments.
	e := patternsExport(1, func(int) string { return "Line one\n\nLine two\n<!--more-->\n<p>Three</p>\n<!--nextpage-->\nFour" })
	imp := New("site.xml", e, NewReport(Info{File: "site.xml"}))
	_, res := limitedBody(t, imp, wpPara("a")+wpRef(1)+"<p>b</p>\n<!--more-->\n<p>c</p>\n<!--nextpage-->\n"+wpPara("d"))
	got, err := res.Blocks.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	want := withIDs(jsonList(jsonPara(0, "a"), jsonPara(1, "Line one Line two"), jsonPara(2, "Three"), jsonPara(3, "Four"),
		jsonPara(4, "b"), jsonPara(5, "c"), jsonPara(6, "d")))
	if string(got) != want {
		t.Errorf("the blocks are\n%s\nwant\n%s", got, want)
	}
	// The tag after the copy is the tag of the content: the blocks of the copy come before it.
	if res.More != 5 {
		t.Errorf("More = %d, want 5", res.More)
	}
	wantLines(t, imp.Report, bodyLines(
		`changed: the synced pattern 1 ("Pattern 1") is copied here; whiteroom has no shared blocks yet (stage 2) -> /body/1`,
		"skipped: 1 page break is removed; whiteroom has no page breaks -> /body"))
}

// elementLost is the line of the report for settings of the element tag that are lost.
func elementLost(items, tag, pointer string) string {
	return fmt.Sprintf("skipped: lost %s of %s%s -> %s", items, tag, lostSuffix, pointer)
}

func TestClassicSettings(t *testing.T) {
	runClassic(t, []classicCase{
		{name: "the classes of WordPress are left out, the first of the others is the class name, and the others are noted",
			text:  `<p class="alignleft size-thumbnail wp-block-foo attachment-x foo bar baz">x</p>`,
			want:  jsonList(jsonBlock("paragraph", 0, `"className":"foo"`, "", jsonList(jsonText("x")))),
			lines: []string{`skipped: the classes "bar", "baz" are not imported: whiteroom blocks have one class name -> /body/0`}},
		{name: "a class that is only one of WordPress", text: `<h2 class="wp-heading aligncenter">x</h2>`,
			want: jsonList(jsonHeading(0, "", `"level":2`, "x"))},
		{name: "a class that whiteroom does not accept", text: `<p class="` + "日本語" + ` ok">x</p>`,
			want:  jsonList(jsonBlock("paragraph", 0, `"className":"ok"`, "", jsonList(jsonText("x")))),
			lines: []string{"skipped: the class \"日本語\" is not imported: invalid name \"日本語\": use ASCII letters, digits, \"-\" and \"_\" -> /body/0"}},
		{name: "the alignment of a paragraph, the settings that are lost, and the attribute that the style wins over",
			text:  `<p style="text-align: right; padding-left: 30px; font-size: 20px" align="center">x</p>`,
			want:  jsonList(jsonBlock("paragraph", 0, "", `"textAlign":"right"`, jsonList(jsonText("x")))),
			lines: []string{elementLost("style.padding-left, style.font-size, align", "<p>", "/body/0")}},
		{name: "the attribute align of a paragraph", text: `<p align="Center">x</p>`,
			want: jsonList(jsonBlock("paragraph", 0, "", `"textAlign":"center"`, jsonList(jsonText("x"))))},
		{name: "an alignment that whiteroom does not have", text: `<p style="text-align: justify">x</p><p align="justify">y</p><h3 style="text-align:start;color:red">z</h3>`,
			want: jsonList(jsonPara(0, "x"), jsonPara(1, "y"), jsonHeading(2, "", `"level":3`, "z")),
			lines: []string{
				elementLost("style.text-align", "<p>", "/body/0"), elementLost("align", "<p>", "/body/1"),
				elementLost("style.text-align, style.color", "<h3>", "/body/2"),
			}},
		{name: "a heading", text: `<h3 id="x" style="text-align: center !important" align="right">Head</h3>`,
			want:  jsonList(jsonHeading(0, `"anchor":"x"`, `"level":3,"textAlign":"center"`, "Head")),
			lines: []string{elementLost("align", "<h3>", "/body/0")}},
		{name: "the alignment is not that of an element that has no text", text: `<div align="center" style="text-align: center">x</div>`,
			want:  jsonList(jsonHolder("group", 0, "", "", jsonPara(1, "x"))),
			lines: []string{elementLost("style.text-align, align", "<div>", "/body/0")}},
		{name: "a list", text: `<ul id="l" class="a b" style="margin-left: 20px" type="square"><li>x</li></ul>`,
			want: jsonList(jsonBlock("list", 0, `"className":"a","anchor":"l"`, "", jsonList(jsonItem(jsonPara(1, "x"))))),
			lines: []string{
				elementLost("style.margin-left", "<ul>", "/body/0"), lostLine("type", "core/list", "/body/0"),
				`skipped: the class "b" is not imported: whiteroom blocks have one class name -> /body/0`,
			}},
		{name: "pre", text: `<pre id="c" style="width: 100px" width="80">x</pre>`,
			want:  jsonList(jsonBlock("code", 0, `"anchor":"c"`, "", `"x"`)),
			lines: []string{elementLost("style.width, width", "<pre>", "/body/0")}},
		{name: "a rule", text: `<hr width="50%" size="2" color="red" style="color: red" noshade>`,
			want:  jsonList(jsonBlock("separator", 0, "", "", "")),
			lines: []string{elementLost("style.color, width, size, color", "<hr>", "/body/0")}},
		{name: "a quote and a details block", text: `<blockquote id="q" class="x" style="border: 0">q</blockquote><details class="d" open><summary>S</summary>d</details>`,
			want: jsonList(jsonHolder("quote", 0, `"className":"x","anchor":"q"`, "", jsonPara(1, "q")), jsonHolder("group", 2, `"className":"d"`, `"label":"S"`, jsonPara(3, "d"))),
			lines: []string{
				elementLost("style.border", "<blockquote>", "/body/0"),
				"changed: the details block is a group here, always open; whiteroom has no details blocks yet (stage 2) -> /body/1",
			}},
		{name: "the cells of a table", text: `<table><tbody><tr><td align="right">a</td><th style="text-align: left" align="right" width="10">b</th><td style="text-align: justify">c</td></tr></tbody></table>`,
			want: jsonList(jsonBlock("table", 0, "", "", jsonList(jsonRow("", jsonCell(`"textAlign":"right"`, "a"), jsonCell(`"header":true,"textAlign":"left"`, "b"), jsonCell("", "c"))))),
			lines: []string{
				elementLost("align, width, style.text-align", "<table>", "/body/0"), lostLine("textAlign", "core/table", "/body/0"),
			}},
		{name: "an anchor that another block has", text: `<h2 id="a">1</h2><h2 id="a">2</h2><h2 id="b c">3</h2>`,
			want: jsonList(jsonHeading(0, `"anchor":"a"`, `"level":2`, "1"), jsonHeading(1, "", `"level":2`, "2"), jsonHeading(2, "", `"level":2`, "3")),
			lines: []string{
				`skipped: the anchor "a" is not imported: another block has it -> /body/1`,
				`skipped: the anchor "b c" is not imported: invalid name "b c": use ASCII letters, digits, "-" and "_" -> /body/2`,
			}},
		{name: "what is said of a paragraph that is a legacy block is forgotten, and its anchor is free",
			text: `<p id="z" class="a b" style="padding: 1px">x <img src="` + photoURL + `"> y</p><p id="z">ok</p>`,
			want: jsonList(jsonLegacy(1, `<p id="z" class="a b" style="padding: 1px">x <img src="`+photoURL+`"> y</p>`, "the text has <img>, which rich text cannot have"),
				jsonBlock("paragraph", 2, `"anchor":"z"`, "", jsonList(jsonText("ok")))),
			lines: []string{legacyLine("the text has <img>, which rich text cannot have", "/body/0")}},
		{name: "an iframe with a class", text: `<iframe id="v" class="yt" width="9" src="https://www.youtube.com/embed/dQw4w9WgXcQ"></iframe>`,
			want:  jsonList(jsonBlock("embed", 0, `"className":"yt","anchor":"v"`, `"provider":"youtube","url":"https://www.youtube.com/embed/dQw4w9WgXcQ"`, "")),
			lines: []string{"changed: the iframe is an embed block here -> /body/0"}},
	})
}

func TestClassicLegacyAddresses(t *testing.T) {
	const (
		link   = "https://example.com/2026/10/hello/"
		markup = `<dl><dt><a href="about/">a</a> <a href="../x.pdf">b</a> <a href="/abs/">c</a> <a href="#frag">d</a> <a href="mailto:a@b.c">e</a> ` +
			`<a href="//cdn.example/x.png">f</a> <a href="?s=1">g</a> <a href="">h</a> <a href=" https://other.example.org/ ">i</a></dt>` +
			`<dd><img src="../a.jpg" srcset="b.jpg"><q cite="x/">q</q><blockquote cite="y/">b</blockquote><del cite="../z">d</del>` +
			`<ins cite="//ins.example/">i</ins><iframe src="rel/"></iframe><video src="v.mp4"></video></dd></dl>`
		rewritten = `<dl><dt><a href="https://example.com/2026/10/hello/about/">a</a> <a href="https://example.com/2026/10/x.pdf">b</a> ` +
			`<a href="/abs/">c</a> <a href="#frag">d</a> <a href="mailto:a@b.c">e</a> ` +
			`<a href="https://cdn.example/x.png">f</a> <a href="https://example.com/2026/10/hello/?s=1">g</a> <a href="">h</a> ` +
			`<a href=" https://other.example.org/ ">i</a></dt>` +
			`<dd><img src="https://example.com/2026/10/a.jpg" srcset="b.jpg"><q cite="https://example.com/2026/10/hello/x/">q</q>` +
			`<blockquote cite="https://example.com/2026/10/hello/y/">b</blockquote><del cite="https://example.com/2026/10/z">d</del>` +
			`<ins cite="https://ins.example/">i</ins><iframe src="rel/"></iframe><video src="v.mp4"></video></dd></dl>`
		relative = 9 // the addresses that have no scheme, which count the base
	)
	item := bodyItem(freeform(markup))
	item.Link = link

	imp := mediaImport(t)
	c, res := limitedItem(t, imp, item)
	sameBlocks(t, res.Blocks, []content.Block{legacyBlock(bodyIDs[0], rewritten, "the element <dl> is not converted")})
	wantLines(t, imp.Report, bodyLines(legacyLine("the element <dl> is not converted", "/body/0")))
	if want := len(markup) + relative*len(link); c.htmlBytes != want {
		t.Errorf("the conversion read %d bytes of HTML, want %d: the HTML, and the base for each address without a scheme", c.htmlBytes, want)
	}

	// Once the conversion has read as much as it may, the addresses that are left stay as they are.
	t.Run("the limit of the HTML that is read", func(t *testing.T) {
		lowerLimit(t, &maxHTMLBytes, len(markup)+2*len(link))
		imp := mediaImport(t)
		c, res := limitedItem(t, imp, item)
		if !c.stopped || len(res.Blocks) != 1 {
			t.Fatalf("the conversion stopped = %v and made %d blocks", c.stopped, len(res.Blocks))
		}
		got := res.Blocks[0].Attrs.HTML
		if !strings.Contains(got, `<a href="https://example.com/2026/10/hello/about/">a</a>`) ||
			!strings.Contains(got, `<a href="https://example.com/2026/10/x.pdf">b</a>`) || !strings.Contains(got, `<a href="//cdn.example/x.png">f</a>`) ||
			!strings.Contains(got, `<img src="../a.jpg"`) {
			t.Errorf("the HTML is %s, want the first two addresses absolute and the others as they were", got)
		}
	})
}

func TestClassicWhiteSpace(t *testing.T) {
	nbsp := string(rune(0xA0))
	runClassic(t, []classicCase{
		{name: "nothing but line breaks", text: "<br>\n<br />\n\n<br><br>", want: "[]"},
		{name: "nothing but comments and white space", text: "<!-- x -->\n\n <!-- y --> \t", want: "[]"},
		{name: "empty paragraphs", text: "<p></p><p> </p><p>\n</p><p><br></p>", want: "[]"},
		{name: "control characters only", text: "<p>" + string(rune(0x11)) + "</p>\n\n" + string(rune(0x0B)), want: "[]"},
		{name: "empty elements in a group", text: "<div><p></p><br> <!-- x --></div>x", want: jsonList(jsonPara(1, "x")),
			lines: []string{"skipped: the empty group is removed -> /body"}},
	})

	// A paragraph of a no-break space is text, which is how the classic editor keeps an empty line.
	for _, in := range []string{"<p>&nbsp;</p>", "&nbsp;", "<p>" + nbsp + "</p>", "<br>&nbsp;<br>"} {
		imp := mediaImport(t)
		res := convertMedia(t, imp, bodySite(false), nil, in)
		if len(res.Blocks) != 1 || res.Blocks[0].Type != content.BlockParagraph || len(res.Blocks[0].Text) != 1 || res.Blocks[0].Text[0].Text != nbsp {
			t.Errorf("%q became %+v, want a paragraph of a no-break space", in, res.Blocks)
		}
	}
}

func TestClassicDepth(t *testing.T) {
	nest := func(n int) string { return freeform(strings.Repeat("<div>", n) + "x" + strings.Repeat("</div>", n)) }
	// leaf returns the block at the end of the groups, and how deep it is.
	leaf := func(bs content.Blocks) (content.Block, int) {
		n := 1
		for len(bs[0].Children) > 0 && bs[0].Type == content.BlockGroup {
			bs = bs[0].Children
			n++
		}
		return bs[0], n
	}
	deepAt := func(n int) string { return "/body/0" + strings.Repeat("/content/0", n-1) }

	// 31 groups hold a paragraph at the 32nd level, the deepest that a document can have.
	res, imp := textBody(t, bodySite(false), nil, nest(31))
	if got, depth := leaf(res.Blocks); got.Type != content.BlockParagraph || depth != 32 {
		t.Errorf("31 groups became a %s block at depth %d, want a paragraph at 32", got.Type, depth)
	}
	wantLines(t, imp.Report, nil)

	// With a group more, the group at the 32nd level would have a block at the 33rd: it is a legacy block of its HTML.
	for _, n := range []int{32, 33, 200} {
		res, imp := textBody(t, bodySite(false), nil, nest(n))
		got, depth := leaf(res.Blocks)
		inner := strings.Repeat("<div>", n-31) + "x" + strings.Repeat("</div>", n-31)
		if got.Type != content.BlockLegacy || depth != 32 || got.Attrs.HTML != inner || got.Attrs.Reason != deepReason {
			t.Errorf("%d groups became a %s block at depth %d with the HTML %.60s, want a legacy block at 32", n, got.Type, depth, got.Attrs.HTML)
		}
		wantLines(t, imp.Report, bodyLines(legacyLine(deepReason, deepAt(32))))
	}

	// Elements that nest so deep that the parser gives up on them are a legacy block of the HTML that is read.
	res, imp = textBody(t, bodySite(false), nil, nest(3000))
	if len(res.Blocks) != 1 || res.Blocks[0].Type != content.BlockLegacy || res.Blocks[0].Attrs.Reason != reasonComplex {
		t.Errorf("3000 groups became %.100v, want a legacy block that the HTML is too complex to read", res.Blocks)
	}
	wantLines(t, imp.Report, bodyLines(legacyLine(reasonComplex, "/body/0")))

	// The same for quotes and details, and for a list in groups.
	nestTag := func(tag string, n int) string {
		return freeform(strings.Repeat("<"+tag+">", n) + "x" + strings.Repeat("</"+tag+">", n))
	}
	for _, tag := range []string{"blockquote", "section", "details"} {
		res, imp := textBody(t, bodySite(false), nil, nestTag(tag, 40))
		bs, depth := res.Blocks, 0
		for len(bs) == 1 && len(bs[0].Children) > 0 && bs[0].Type != content.BlockLegacy && bs[0].Type != content.BlockParagraph {
			bs, depth = bs[0].Children, depth+1
		}
		if depth != 31 || len(bs) != 1 || bs[0].Type != content.BlockLegacy || bs[0].Attrs.Reason != deepReason {
			t.Errorf("40 %s elements became %d levels and then %v, want 31 and a legacy block", tag, depth, bs)
		}
		if lines := reportText(t, imp.Report); !strings.Contains(lines, bodyLine(legacyLine(deepReason, deepAt(32)))+"\n") {
			t.Errorf("40 %s elements: the report has no legacy block at depth 32:\n%.1000s", tag, lines)
		}
	}
	inGroups := func(groups int) content.Block {
		res, _ := textBody(t, bodySite(false), nil, freeform(strings.Repeat("<div>", groups)+"<ul><li>x</li></ul>"+strings.Repeat("</div>", groups)))
		g := res.Blocks[0]
		for range groups - 1 {
			g = g.Children[0]
		}
		return g.Children[0]
	}
	// A list at depth d has its items at d+1 and their paragraphs at d+2.
	if got := inGroups(29); got.Type != content.BlockList {
		t.Errorf("the list in 29 groups is a %s block, want a list", got.Type)
	}
	if got := inGroups(30); got.Type != content.BlockLegacy || got.Attrs.Reason != deepReason {
		t.Errorf("the list in 30 groups is %+v, want a legacy block", got)
	}

	// Lists in lists: 15 fill the levels, and the whole list is a legacy block with the 16th.
	lists := func(n int) string { return freeform(strings.Repeat("<ul><li>x", n) + strings.Repeat("</li></ul>", n)) }
	for _, n := range []int{1, 15} {
		res, imp := textBody(t, bodySite(false), nil, lists(n))
		if len(res.Blocks) != 1 || res.Blocks[0].Type != content.BlockList {
			t.Errorf("%d nested lists became %+v, want a list", n, res.Blocks)
		}
		wantLines(t, imp.Report, nil)
	}
	for _, n := range []int{16, 500} {
		res, imp := textBody(t, bodySite(false), nil, lists(n))
		if len(res.Blocks) != 1 || res.Blocks[0].Type != content.BlockLegacy || res.Blocks[0].Attrs.Reason != deepReason ||
			res.Blocks[0].Attrs.HTML != strings.Repeat("<ul><li>x", n)+strings.Repeat("</li></ul>", n) {
			t.Errorf("%d nested lists became %.100v, want a legacy block of all of them", n, res.Blocks)
		}
		wantLines(t, imp.Report, bodyLines(legacyLine(deepReason, "/body/0")))
	}

	// A table in 30 groups has its cells at the 33rd level, and a table in 29 has them at the 32nd.
	inGroupsTable := func(groups int) content.Block {
		res, _ := textBody(t, bodySite(false), nil, freeform(strings.Repeat("<div>", groups)+"<table><tr><td>x</td></tr></table>"+strings.Repeat("</div>", groups)))
		g := res.Blocks[0]
		for range groups - 1 {
			g = g.Children[0]
		}
		return g.Children[0]
	}
	if got := inGroupsTable(29); got.Type != content.BlockTable {
		t.Errorf("the table in 29 groups is a %s block, want a table", got.Type)
	}
	if got := inGroupsTable(30); got.Type != content.BlockLegacy || got.Attrs.Reason != deepReason {
		t.Errorf("the table in 30 groups is %+v, want a legacy block", got)
	}
}

// autopLengths wraps the function that makes the paragraphs until the end of the test, and returns the lengths of the texts it
// is given.
func autopLengths(t *testing.T) *[]int {
	t.Helper()
	var lengths []int
	old := wpAutop
	wpAutop = func(s string) string {
		lengths = append(lengths, len(s))
		return old(s)
	}
	t.Cleanup(func() { wpAutop = old })
	return &lengths
}

func TestClassicLimits(t *testing.T) {
	t.Run("the blocks that are made count among those that are converted", func(t *testing.T) {
		lowerLimit(t, &maxWPBlocks, 3)
		imp := bodyImport()
		c, res := limitedBody(t, imp, "a\n\nb\n\nc\n\nd\n\ne")
		sameBlocks(t, res.Blocks, []content.Block{paragraphBlock(bodyIDs[0], "a"), paragraphBlock(bodyIDs[1], "b"), paragraphBlock(bodyIDs[2], "c")})
		wantLines(t, imp.Report, bodyLines(stopLine("/body")))
		if !c.stopped || c.blocks != 4 {
			t.Errorf("the conversion stopped = %v with %d blocks counted, want true and 4", c.stopped, c.blocks)
		}
	})

	t.Run("the HTML between blocks counts too, and the blocks of WordPress that the parser counts do not change", func(t *testing.T) {
		lowerLimit(t, &maxWPBlocks, 3)
		imp := bodyImport()
		_, res := limitedBody(t, imp, blockA+"<p>x</p><p>y</p>"+blockB)
		sameBlocks(t, res.Blocks, []content.Block{
			legacyBlock(bodyIDs[0], blockA, notConverted("acme/a")), paragraphBlock(bodyIDs[1], "x"), paragraphBlock(bodyIDs[2], "y"),
		})
		wantLines(t, imp.Report, bodyLines(legacyLine(notConverted("acme/a"), "/body/0"), stopLine("/body")))
	})

	t.Run("a group counts before what is in it, and keeps what was made", func(t *testing.T) {
		lowerLimit(t, &maxWPBlocks, 2)
		imp := bodyImport()
		c, res := limitedBody(t, imp, "<div><p>a</p><p>b</p><p>c</p></div>")
		sameBlocks(t, res.Blocks, []content.Block{{Type: content.BlockGroup, ID: bodyIDs[0], Children: content.Blocks{paragraphBlock(bodyIDs[1], "a")}}})
		wantLines(t, imp.Report, bodyLines(stopLine("/body/0/content")))
		if !c.stopped {
			t.Error("the conversion did not stop")
		}

		// A group that the conversion stops before its first block is not made, and says nothing of being empty.
		lowerLimit(t, &maxWPBlocks, 1)
		imp = bodyImport()
		_, res = limitedBody(t, imp, "<p>x</p><div><p>a</p></div>")
		sameBlocks(t, res.Blocks, []content.Block{paragraphBlock(bodyIDs[0], "x")})
		wantLines(t, imp.Report, bodyLines(stopLine("/body")))
	})

	t.Run("the items of a list count, and a list that the conversion stops in stays", func(t *testing.T) {
		lowerLimit(t, &maxWPBlocks, 10)
		imp := bodyImport()
		c, res := limitedBody(t, imp, "<ul>"+strings.Repeat("<li>x</li>", 100)+"</ul>")
		if !c.stopped || len(res.Blocks) != 1 || countOfType(res.Blocks, content.BlockListItem) != 9 {
			t.Errorf("the conversion stopped = %v and made %d blocks with %d items, want true, 1 and 9", c.stopped, len(res.Blocks),
				countOfType(res.Blocks, content.BlockListItem))
		}
		wantLines(t, imp.Report, bodyLines(stopLine("/body")))
	})

	t.Run("the cells of a table count", func(t *testing.T) {
		lowerLimit(t, &maxWPBlocks, 10)
		imp := bodyImport()
		c, res := limitedBody(t, imp, "<table><tr>"+strings.Repeat("<td>x</td>", 100)+"</tr></table>")
		if !c.stopped || len(res.Blocks) != 1 || countOfType(res.Blocks, content.BlockTableRow, content.BlockTableCell) != 9 {
			t.Errorf("the conversion stopped = %v and made %d blocks with %d parts, want true, 1 and 9", c.stopped, len(res.Blocks),
				countOfType(res.Blocks, content.BlockTableRow, content.BlockTableCell))
		}
	})

	t.Run("more paragraphs than a document has blocks", func(t *testing.T) {
		imp := bodyImport()
		c, res := limitedBody(t, imp, strings.Repeat("x\n\n", content.MaxBlocks+1))
		if len(res.Blocks) != content.MaxBlocks || c.blocks != content.MaxBlocks+1 {
			t.Errorf("%d paragraphs gave %d blocks with %d counted, want %d and all counted", content.MaxBlocks+1, len(res.Blocks), c.blocks, content.MaxBlocks)
		}
		wantLines(t, imp.Report, bodyLines(fmt.Sprintf("skipped: the blocks from /body/%d on are not imported: a document has at most %d blocks -> /body",
			content.MaxBlocks, content.MaxBlocks)))
	})

	t.Run("the HTML that is read is the HTML of the parts", func(t *testing.T) {
		// 10 lines of 13 bytes: the parts are 3 lines, and what wpautop makes of them is cut again, and read in parts.
		lowerLimit(t, &maxBlockHTMLBytes, 40)
		lengths := autopLengths(t)
		imp := bodyImport()
		text := strings.Repeat("line of text\n", 10)
		c, res := limitedBody(t, imp, text)
		if len(*lengths) != 4 || slicesMax(*lengths) > 39 {
			t.Errorf("wpautop was given the lengths %v, want 4 texts of at most 39 bytes", *lengths)
		}
		if c.htmlBytes < len(text) || countOfType(res.Blocks, content.BlockParagraph) != len(res.Blocks) {
			t.Errorf("the conversion made %d blocks and read %d bytes, want paragraphs and at least %d bytes", len(res.Blocks), c.htmlBytes, len(text))
		}
		lowerLimit(t, &maxHTMLBytes, 100)
		imp = bodyImport()
		c, res = limitedBody(t, imp, text)
		if !c.stopped {
			t.Fatalf("the conversion did not stop, having read %d bytes", c.htmlBytes)
		}
		last := res.Blocks[len(res.Blocks)-1]
		if reason := fmt.Sprintf("the conversion has read more than %d bytes of HTML", maxHTMLBytes); last.Type != content.BlockLegacy || last.Attrs.Reason != reason {
			t.Errorf("the last block is %+v, want a legacy block with the reason %q", last, reason)
		}
		if lines := reportText(t, imp.Report); !strings.Contains(lines, bodyLine(stopLine("/body"))+"\n") {
			t.Errorf("the report has no line that says the conversion stopped:\n%.2000s", lines)
		}
	})

	t.Run("a part that cannot be read", func(t *testing.T) {
		markup := "<iframe></iframe>" + strings.Repeat("<div>", 10000) + strings.Repeat("</p>", 10000)
		imp := bodyImport()
		_, res := limitedBody(t, imp, freeform(markup)+wpPara("x"))
		sameBlocks(t, res.Blocks, []content.Block{legacyBlock(bodyIDs[0], markup, reasonComplex), paragraphBlock(bodyIDs[1], "x")})
		wantLines(t, imp.Report, bodyLines(legacyLine(reasonComplex, "/body/0")))
	})
}

func slicesMax(s []int) int {
	m := 0
	for _, n := range s {
		m = max(m, n)
	}
	return m
}

func TestClassicBigContent(t *testing.T) {
	// WordPress makes the paragraphs of all of the page at once. A page that is longer than a block may be read is cut, outside of
	// elements and after a line break, and wpautop is given the parts, which are at most maxBlockHTMLBytes long. The line break
	// at a cut ends a paragraph, where it is a br in a paragraph in WordPress.
	lowerLimit(t, &maxBlockHTMLBytes, 100)
	lengths := autopLengths(t)
	line := "a line of text\n"
	text := strings.Repeat(line, 50)

	imp := bodyImport()
	c, res := limitedBody(t, imp, text)
	if len(*lengths) == 0 || slicesMax(*lengths) > maxBlockHTMLBytes {
		t.Errorf("wpautop was given the lengths %v, want texts of at most %d bytes", *lengths, maxBlockHTMLBytes)
	}
	lines := 0
	for _, b := range res.Blocks {
		if b.Type != content.BlockParagraph {
			t.Fatalf("a %s block among the paragraphs", b.Type)
		}
		for _, in := range b.Text {
			if in.Type == content.InlineText {
				lines++
			}
		}
	}
	if lines != 50 || len(res.Blocks) < len(*lengths) {
		t.Errorf("the %d paragraphs have %d lines of text, want 50 in at least %d paragraphs", len(res.Blocks), lines, len(*lengths))
	}
	wantLines(t, imp.Report, nil)
	if c.stopped {
		t.Error("the conversion stopped")
	}

	// The whole page is one paragraph, as WordPress makes it, where it is not cut.
	*lengths = nil
	lowerLimit(t, &maxBlockHTMLBytes, 8<<20)
	imp = bodyImport()
	_, res = limitedBody(t, imp, text)
	if len(res.Blocks) != 1 || len(res.Blocks[0].Text) != 99 || len(*lengths) != 1 || (*lengths)[0] != len(text) {
		t.Errorf("the page became %d paragraphs with %d pieces, with wpautop given %v", len(res.Blocks), len(res.Blocks[0].Text), *lengths)
	}

	// The pages are cut one by one, and a page break is a page break wherever the cuts are.
	lowerLimit(t, &maxBlockHTMLBytes, 100)
	*lengths = nil
	imp = bodyImport()
	_, res = limitedBody(t, imp, text+"<!--nextpage-->\n"+text)
	if slicesMax(*lengths) > maxBlockHTMLBytes || countOfType(res.Blocks, content.BlockParagraph) != len(res.Blocks) {
		t.Errorf("wpautop was given %v for %d blocks", *lengths, len(res.Blocks))
	}
	wantLines(t, imp.Report, bodyLines("skipped: 1 page break is removed; whiteroom has no page breaks -> /body"))

	// Elements are not cut: a part is longer than the limit where an element is, and it is not read.
	*lengths = nil
	table := "<table>" + strings.Repeat("<tr><td>cell</td></tr>\n", 20) + "</table>\n"
	imp = bodyImport()
	_, res = limitedBody(t, imp, "before\n"+table+"after\n")
	reason := "the HTML of the classic editor has more than 100 bytes"
	sameBlocks(t, res.Blocks, []content.Block{paragraphBlock(bodyIDs[0], "before"), legacyBlock(bodyIDs[1], table, reason), paragraphBlock(bodyIDs[2], "after")})
	wantLines(t, imp.Report, bodyLines(legacyLine(reason, "/body/1")))
	if slicesMax(*lengths) > maxBlockHTMLBytes {
		t.Errorf("wpautop was given %v", *lengths)
	}
}

func TestClassicPartThatCannotBeCut(t *testing.T) {
	lowerLimit(t, &maxBlockHTMLBytes, 1000)
	lengths := autopLengths(t)

	// A text of one line is one part, and wpautop is not given it. It is a legacy block of its HTML, unless it is too long to be one.
	short := strings.Repeat("y", 2000)
	imp := bodyImport()
	_, res := limitedBody(t, imp, "before\n"+short)
	reason := "the HTML of the classic editor has more than 1000 bytes"
	sameBlocks(t, res.Blocks, []content.Block{paragraphBlock(bodyIDs[0], "before"), legacyBlock(bodyIDs[1], short, reason)})
	wantLines(t, imp.Report, bodyLines(legacyLine(reason, "/body/1")))
	if slicesMax(*lengths) > 1000 {
		t.Errorf("wpautop was given the lengths %v", *lengths)
	}

	long := strings.Repeat("y", content.MaxTextBytes+1)
	*lengths = nil
	imp = bodyImport()
	_, res = limitedBody(t, imp, "before\n"+long+"\nafter")
	sameBlocks(t, res.Blocks, []content.Block{paragraphBlock(bodyIDs[0], "before"), paragraphBlock(bodyIDs[1], "after")})
	wantLines(t, imp.Report, bodyLines(
		fmt.Sprintf("skipped: a part of the HTML of the classic editor is not imported: its HTML is too long: %d bytes (max %d) -> /body",
			len(long)+1, content.MaxTextBytes)))
	if slicesMax(*lengths) > 1000 {
		t.Errorf("wpautop was given the lengths %v", *lengths)
	}

	// The part that cannot be read does not count among the HTML that the conversion reads.
	imp = bodyImport()
	c, _ := limitedBody(t, imp, long)
	if c.htmlBytes != 0 {
		t.Errorf("the conversion read %d bytes of a part that it did not read", c.htmlBytes)
	}
}

func TestClassicContentOfASource(t *testing.T) {
	// A source other than the content of the item, such as the content of a block, names itself in what it does not read.
	lowerLimit(t, &maxBlockHTMLBytes, 100)
	imp := bodyImport()
	c := newBodyConv(imp, BodyOptions{Site: bodySite(false), Item: bodyItem("x"), Target: bodyTarget, Key: bodyKey})
	long := strings.Repeat("y", content.MaxTextBytes+1)
	src := classicSource{text: "One\n\nTwo\n" + long + "\nThree", what: `the block "core/shortcode"`, autop: true, top: true}
	blocks := c.classicContent(src, bodyPlace{depth: 1, top: 0}, noteAt{})
	c.flush(blocks)
	sameBlocks(t, blocks, []content.Block{paragraphBlock(bodyIDs[0], "One"), paragraphBlock(bodyIDs[1], "Two"), paragraphBlock(bodyIDs[2], "Three")})
	wantLines(t, imp.Report, bodyLines(
		fmt.Sprintf(`skipped: a part of the block "core/shortcode" is not imported: its HTML is too long: %d bytes (max %d) -> /body`, len(long)+1, content.MaxTextBytes)))

	// The source of the content of the item is the HTML of the classic editor.
	imp = bodyImport()
	c = newBodyConv(imp, BodyOptions{Site: bodySite(false), Item: bodyItem("x"), Target: bodyTarget, Key: bodyKey})
	blocks = c.classicContent(classicSource{text: long + "\nThree", what: classicHTML}, bodyPlace{depth: 1, top: 0}, noteAt{})
	c.flush(blocks)
	wantLines(t, imp.Report, bodyLines(
		fmt.Sprintf("skipped: a part of the HTML of the classic editor is not imported: its HTML is too long: %d bytes (max %d) -> /body", len(long)+1, content.MaxTextBytes)))
}

func TestClassicGuard(t *testing.T) {
	// refuse makes the validation of the guard find a problem in each block of the type typ, at the top and in the groups, until
	// there are none left.
	refuse := func(t *testing.T, typ content.BlockType, attr string) {
		t.Helper()
		old := validateBlocks
		validateBlocks = func(blocks content.Blocks, opts content.ValidateOptions) error {
			if err := old(blocks, opts); err != nil {
				return err
			}
			var errs content.Errors
			var walk func(bs content.Blocks, path string)
			walk = func(bs content.Blocks, path string) {
				for i := range bs {
					at := fmt.Sprintf("%s/%d", path, i)
					if bs[i].Type == typ {
						errs = append(errs, &content.Error{Path: at + "/attrs/" + attr, Msg: "refused"})
					}
					walk(bs[i].Children, at+"/content")
				}
			}
			walk(blocks, "")
			if len(errs) > 0 {
				return errs
			}
			return nil
		}
		t.Cleanup(func() { validateBlocks = old })
	}
	reason := "the conversion does not pass validation: /attrs/level: refused"

	t.Run("an element that does not pass is a legacy block of its HTML, and not all of the content", func(t *testing.T) {
		refuse(t, content.BlockHeading, "level")
		imp := bodyImport()
		res, err := imp.Body(BodyOptions{Site: bodySite(false), Item: bodyItem(`a<h2 class="x">Head</h2><div>b<h3>Deep</h3></div>c`), Target: bodyTarget, Key: bodyKey})
		if err != nil {
			t.Fatal(err)
		}
		got, _ := res.Blocks.MarshalJSON()
		// The paragraph a is 0, the heading 1, the group 2, b 3, the heading in it 4, c 5; the legacy blocks are 6 and 7.
		want := withIDs(jsonList(jsonPara(0, "a"), jsonLegacy(6, `<h2 class="x">Head</h2>`, reason),
			jsonHolder("group", 2, "", "", jsonPara(3, "b"), jsonLegacy(7, "<h3>Deep</h3>", reason)), jsonPara(5, "c")))
		if string(got) != want {
			t.Errorf("the blocks are\n%s\nwant\n%s", got, want)
		}
		wantLines(t, imp.Report, bodyLines(legacyLine(reason, "/body/1"), legacyLine(reason, "/body/2/content/1")))
	})

	t.Run("the blocks that one element made go together", func(t *testing.T) {
		refuse(t, content.BlockImage, "media")
		imp := mediaImport(t)
		text := `<p id="p">Before<br>` + classicImg("wp-image-21") + `<br>After<br>` + classicImg("wp-image-21") + `</p>` + "\n\nx"
		res := convertMediaWithGuard(t, imp, text)
		markup := `<p id="p">Before<br>` + strings.Replace(classicImg("wp-image-21"), " />", ">", 1) + `<br>After<br>` + strings.Replace(classicImg("wp-image-21"), " />", ">", 1) + `</p>`
		reason := "the conversion does not pass validation: /attrs/media: refused"
		got, _ := res.Blocks.MarshalJSON()
		want := withIDs(jsonList(jsonLegacy(5, markup, reason), jsonPara(4, "x")))
		if string(got) != want {
			t.Errorf("the blocks are\n%s\nwant\n%s", got, want)
		}
		wantLines(t, imp.Report, bodyLines(legacyLine(reason, "/body/0")))
	})

	t.Run("the legacy block stands where the first block was, so the read more tag stays after it", func(t *testing.T) {
		refuse(t, content.BlockImage, "media")
		imp := mediaImport(t)
		text := "x\n\n" + classicImg("wp-image-21") + "<br><!--more--><br>After\n\ny"
		res := convertMediaWithGuard(t, imp, text)
		// The image and the paragraph after the tag come from one element, which is one legacy block; the tag is after it.
		if len(res.Blocks) != 3 || res.Blocks[1].Type != content.BlockLegacy || res.More != 2 {
			t.Errorf("there are %d blocks with More %d, want 3 and 2: %s", len(res.Blocks), res.More, bodyDocument(t, res))
		}
	})

	t.Run("a group that does not pass is not imported with what it holds", func(t *testing.T) {
		refuse(t, content.BlockGroup, "tagName")
		imp := bodyImport()
		res, err := imp.Body(BodyOptions{Site: bodySite(false), Item: bodyItem(`a<div><p>b</p></div>c`), Target: bodyTarget, Key: bodyKey})
		if err != nil {
			t.Fatal(err)
		}
		got, _ := res.Blocks.MarshalJSON()
		if want := withIDs(jsonList(jsonPara(0, "a"), jsonPara(3, "c"))); string(got) != want {
			t.Errorf("the blocks are\n%s\nwant\n%s", got, want)
		}
		wantLines(t, imp.Report, bodyLines("skipped: the block is not imported: /1/attrs/tagName: refused -> /body"))
	})
}

// convertMediaWithGuard converts text as convertMedia does, but does not check that the guard had nothing to do.
func convertMediaWithGuard(t *testing.T, imp *Import, text string) *BodyResult {
	t.Helper()
	res, err := imp.Body(BodyOptions{Site: bodySite(false), Item: bodyItem(text), Target: bodyTarget, Key: bodyKey})
	if err != nil {
		t.Fatalf("Body = %v", err)
	}
	return res
}

func TestClassicMoreText(t *testing.T) {
	for in, want := range map[string]string{
		"more":                     "",
		"more ":                    "",
		"more Read on":             "Read on",
		"more   Read <b>on</b> \t": "Read on",
		"moreover":                 "over",
		"more <br/>":               "",
		"more a<b":                 "a",
		"more <a>x</a><i>y</i>":    "xy",
		"more >":                   ">",
		"more 続きを読む":               "続きを読む",
	} {
		if got := moreText(in); got != want {
			t.Errorf("moreText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClassicSettingsOfAnElement(t *testing.T) {
	element := func(tag string, attrs ...string) *html.Node {
		n := &html.Node{Type: html.ElementNode, Namespace: html.HTML, Name: tag}
		for i := 0; i < len(attrs); i += 2 {
			n.Attrs = append(n.Attrs, html.Attr{Name: attrs[i], Value: attrs[i+1]})
		}
		return n
	}
	for _, tt := range []struct {
		name  string
		n     *html.Node
		text  bool
		quiet []string
		align string
		lost  []string
	}{
		{name: "nothing", n: element("p"), text: true},
		{name: "the alignment of the style", n: element("p", "style", "text-align: Center"), text: true, align: "center"},
		{name: "the alignment of the style with !important", n: element("p", "style", "color: red; TEXT-ALIGN:right !important"), text: true, align: "right", lost: []string{"style.color"}},
		{name: "the alignment of the attribute", n: element("td", "align", " LEFT "), text: true, align: "left"},
		{name: "the style wins over the attribute", n: element("p", "style", "text-align: right", "align", "left"), text: true, align: "right", lost: []string{"align"}},
		{name: "a style that is not an alignment wins over the attribute", n: element("p", "style", "text-align: justify", "align", "left"), text: true,
			lost: []string{"style.text-align", "align"}},
		{name: "an attribute that is not an alignment", n: element("p", "align", "justify"), text: true, lost: []string{"align"}},
		{name: "no alignment of an element without text", n: element("div", "style", "text-align: center", "align", "center"), lost: []string{"style.text-align", "align"}},
		{name: "the properties in the order of the first", n: element("table", "style", "width: 1px; Border: 0; width: 2px; margin:0 ;;; : 1; color"),
			lost: []string{"style.width", "style.border", "style.margin"}},
		{name: "the visual attributes in the order of the element", n: element("table", "width", "9", "class", "x", "border", "1", "id", "i", "cellpadding", "2", "summary", "s", "bgcolor", "red"),
			lost: []string{"width", "border", "cellpadding", "bgcolor"}},
		{name: "all the visual attributes", n: element("td", "align", "x", "background", "b", "bgcolor", "c", "border", "1", "cellpadding", "1", "cellspacing", "1", "color", "c",
			"face", "f", "height", "1", "hspace", "1", "size", "1", "valign", "top", "vspace", "1", "width", "1", "nowrap", "", "scope", "row"),
			lost: []string{"align", "background", "bgcolor", "border", "cellpadding", "cellspacing", "color", "face", "height", "hspace", "size", "valign", "vspace", "width"}},
		{name: "attributes that are dropped without a word", n: element("img", "width", "1", "height", "1", "border", "0"), quiet: []string{"width", "height"}, lost: []string{"border"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := classicProps("core/x", tt.n, tt.text, nil, tt.quiet...)
			if s.align != tt.align || !slices.Equal(s.lost, tt.lost) {
				t.Errorf("the alignment is %q and the lost %q, want %q and %q", s.align, s.lost, tt.align, tt.lost)
			}
			if got := s.block.Attrs.Get("textAlign"); tt.align == "" && got != nil || tt.align != "" && str(got) != tt.align {
				t.Errorf("the attribute textAlign is %v, want %q", got, tt.align)
			}
		})
	}

	t.Run("the class names", func(t *testing.T) {
		for _, tt := range []struct {
			class string
			skip  func(string) bool
			want  string
		}{
			{"", nil, ""},
			{"alignleft aligncenter alignright alignnone", nil, ""},
			{"size-medium wp-image-5 attachment-full wp-caption", nil, ""},
			{"foo  bar\tsize-large baz", nil, "foo bar baz"},
			{"language-go foo", func(c string) bool { return strings.HasPrefix(c, "language-") }, "foo"},
			{"wp", nil, "wp"},
		} {
			s := classicProps("core/x", element("p", "class", tt.class), false, tt.skip)
			if got := str(s.block.Attrs.Get("className")); got != tt.want {
				t.Errorf("the classes %q gave the class name %q, want %q", tt.class, got, tt.want)
			}
		}
	})
}

func TestClassicImageAlign(t *testing.T) {
	for _, tt := range []struct {
		class string
		want  string
		found bool
	}{
		{"", "", false},
		{"size-large wp-image-5", "", false},
		{"alignleft", "left", true},
		{"x aligncenter y", "center", true},
		{"alignright", "right", true},
		{"alignnone size-large", "", false},
		{"alignnone", "", false},
		{"alignright alignleft", "right", true},
		{"alignnone alignleft", "left", true},
		{"ALIGNLEFT", "", false},
	} {
		img := &html.Node{Type: html.ElementNode, Namespace: html.HTML, Name: "img", Attrs: []html.Attr{{Name: "class", Value: tt.class}}}
		if got, found := imageAlign(img); got != tt.want || found != tt.found {
			t.Errorf("imageAlign(%q) = %q, %v, want %q, %v", tt.class, got, found, tt.want, tt.found)
		}
	}
}

func TestClassicParagraphItems(t *testing.T) {
	img := `<img src="a.png">`
	for _, tt := range []struct {
		name string
		in   string
		want string // the kinds of the items, with the text of a text item
	}{
		{"text", "a<b>b</b>", "text(a<b>b</b>)"},
		{"lines of text are one item", "a<br>b<br>c", "text(a<br>b<br>c)"},
		{"a blank line inside", "a<br> <br>b", "text(a<br> <br>b)"},
		{"blank lines at the ends", "<br>\n<br>a<br> <br>", "text(a)"},
		{"an image", img, "image"},
		{"a link around an image", `<a href="x">` + img + `</a>`, "image"},
		{"a link around an image and a space", `<a href="x"> ` + img + ` </a>`, "image"},
		{"a link around an image and text is text", `<a href="x">` + img + `t</a>`, `text(<a href="x">` + img + `t</a>)`},
		{"an image and text", img + "t", "image text(t)"},
		{"text and an image", "t" + img, "text(t) image"},
		{"an image, text and an image", img + " t " + img, "image text( t ) image"},
		{"images in a row", img + " " + img + "<!-- c -->" + img, "image image image"},
		{"an image in the middle", "a" + img + "b", "text(a" + img + "b)"},
		{"an image on a line of its own", "a<br>" + img + "<br>b", "text(a) image text(b)"},
		{"an iframe on a line of its own", `a<br><iframe src="x"></iframe><br>b`, "text(a) frame text(b)"},
		{"an iframe with text", `<iframe src="x"></iframe>t`, `text(<iframe src="x"></iframe>t)`},
		{"an iframe with an image", `<iframe src="x"></iframe>` + img, "text(<iframe src=\"x\"></iframe>) image"},
		{"a tag", "<!--more-->", "more"},
		{"a tag in a line of its own", "a<br> <!--more--> <br>b", "text(a) more text(b)"},
		{"a tag in a line of text", "a<!--more-->b<br>c", "text(a<!--more-->b<br>c) more"},
		{"a tag after text", "a<br>b<!--more-->", "text(a<br>b<!--more-->) more"},
		{"two tags", "<!--more--><!-- x --><!--more Go-->", "more more"},
		{"a tag and an image", "<!--more-->" + img, "more image"},
		{"an image and a tag", img + "<!--more-->", "image more"},
		{"other comments are nothing", "<!-- a --><br><!-- b -->", ""},
		{"a tag that is not one", "<!--more\n-->", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pn := richRoot(t, tt.in)
			pn.Name = "p"
			var got []string
			for _, it := range new(bodyConv).paragraphItems(pn) {
				switch it.kind {
				case itemText:
					var b strings.Builder
					for n := it.first; ; n = n.NextSibling {
						b.WriteString(html.Render(n))
						if n == it.last {
							break
						}
					}
					got = append(got, "text("+b.String()+")")
				case itemImage:
					got = append(got, "image")
				case itemFrame:
					got = append(got, "frame")
				case itemPiece:
					got = append(got, "piece")
				case itemMore:
					got = append(got, "more")
				}
			}
			if s := strings.Join(got, " "); s != tt.want {
				t.Errorf("the items of %q are %q, want %q", tt.in, s, tt.want)
			}
		})
	}
}

func TestClassicFigure(t *testing.T) {
	const notConverted = "the element <figure> is not converted"
	photo := mediaIDs.photo
	img := classicImg("wp-image-21")
	// A figure that holds anything but an image or a table, and its caption, would lose it.
	legacies := []struct{ name, markup string }{
		{"a second image", `<figure>` + img + img + `<figcaption>c</figcaption></figure>`},
		{"text beside the image", `<figure>` + img + `text<figcaption>c</figcaption></figure>`},
		{"a paragraph beside the image", `<figure>` + img + `<p>text</p></figure>`},
		{"an image in an element", `<figure><div>` + img + `<figcaption>c</figcaption></div></figure>`},
		{"two captions", `<figure>` + img + `<figcaption>a</figcaption><figcaption>b</figcaption></figure>`},
		{"a link beside the image", `<figure><a href="https://example.com/x/">` + img + `</a><a href="https://example.com/y/">y</a></figure>`},
		{"a link around the image and text", `<figure><a href="https://example.com/x/">` + img + `text</a></figure>`},
		{"an image of another site and text", `<figure><img src="https://other.example.org/a.jpg" alt="x">text</figure>`},
		{"a paragraph with two images", `<figure><p>` + img + img + `</p></figure>`},
		{"a paragraph with an image and text", `<figure><p>` + img + `text</p></figure>`},
		{"a gallery of the block editor", `<figure class="wp-block-gallery"><ul><li><figure>` + img + `<figcaption>a</figcaption></figure></li>` +
			`<li><figure>` + img + `</figure></li></ul></figure>`},
		{"a second table", `<figure><table><tr><td>a</td></tr></table><table><tr><td>b</td></tr></table></figure>`},
		{"a paragraph beside the table", `<figure><table><tr><td>a</td></tr></table><p>note</p></figure>`},
		{"an image and a table", `<figure>` + img + `<table><tr><td>a</td></tr></table></figure>`},
		{"a table and an image", `<figure><table><tr><td>a</td></tr></table>` + img + `</figure>`},
		{"a caption of the table and a figcaption", `<figure><table><caption>a</caption><tr><td>b</td></tr></table><figcaption>c</figcaption></figure>`},
		{"nothing", `<figure></figure>`},
		{"a paragraph", `<figure><p>x</p></figure>`},
	}
	for _, tt := range legacies {
		t.Run(tt.name, func(t *testing.T) {
			// The HTML is that of the parser, which makes the table and its rows the way it does.
			imp := mediaImport(t)
			res := convertMedia(t, imp, bodySite(false), nil, freeform(tt.markup))
			if len(res.Blocks) != 1 || res.Blocks[0].Type != content.BlockLegacy || res.Blocks[0].Attrs.Reason != notConverted {
				t.Fatalf("the blocks are %s, want a legacy block with the reason %q", bodyDocument(t, res), notConverted)
			}
			// All that the figure holds is in the HTML of the legacy block.
			got := res.Blocks[0].Attrs.HTML
			for _, tag := range []string{"<img", "<table", "<figcaption", "<a ", "<p>"} {
				if strings.Count(got, tag) != strings.Count(tt.markup, tag) {
					t.Errorf("the HTML of the legacy block, %s, has %d of %s, want %d", got, strings.Count(got, tag), tag, strings.Count(tt.markup, tag))
				}
			}
			wantLines(t, imp.Report, bodyLines(legacyLine(notConverted, "/body/0")))
		})
	}
	runClassic(t, []classicCase{
		{name: "an image alone", text: `<figure>` + img + `</figure>`, want: jsonList(jsonImage(0, photo, "", `"alt":"Photo"`))},
		{name: "an image and its caption", text: `<figure>` + img + `<figcaption>c</figcaption></figure>`,
			want: jsonList(jsonImage(0, photo, "", `"alt":"Photo","caption":[`+jsonText("c")+`]`))},
		{name: "an image with a link", text: `<figure><a href="https://example.com/about/">` + img + `</a><figcaption>c</figcaption></figure>`,
			want: jsonList(jsonImage(0, photo, "", `"alt":"Photo","caption":[`+jsonText("c")+`],"link":{"entry":"`+mediaIDs.about+`"}`))},
		{name: "the paragraph that wpautop made of the image", text: "<figure>\n\n" + img + "\n\n<figcaption>c</figcaption>\n\n</figure>",
			want: jsonList(jsonImage(0, photo, "", `"alt":"Photo","caption":[`+jsonText("c")+`]`))},
		{name: "white space, comments and line breaks around them", text: freeform("<figure> <!-- a -->" + img + "<br>\n<!-- b --><figcaption>c</figcaption> <br></figure>"),
			want: jsonList(jsonImage(0, photo, "", `"alt":"Photo","caption":[`+jsonText("c")+`]`))},
		{name: "a figure of lines, which wpautop puts a line break in", text: "<figure class=\"aligncenter\">\n" + img + "\n<figcaption>c</figcaption>\n</figure>\n\n<figure>\n" + img + "\n</figure>",
			want: jsonList(jsonImage(0, photo, "", `"alt":"Photo","caption":[`+jsonText("c")+`],"align":"center"`), jsonImage(1, photo, "", `"alt":"Photo"`))},
		{name: "an image in a paragraph that has white space and a line break", text: freeform("<figure><p> " + img + "<br> </p></figure>"),
			want: jsonList(jsonImage(0, photo, "", `"alt":"Photo"`))},
		{name: "a table", text: `<figure><table><tr><td>a</td></tr></table></figure>`,
			want: jsonList(jsonBlock("table", 0, "", "", jsonList(jsonRow("", jsonCell("", "a")))))},
		{name: "a table and its figcaption", text: `<figure><table><tr><td>a</td></tr></table><figcaption>c</figcaption></figure>`,
			want: jsonList(jsonBlock("table", 0, "", `"caption":[`+jsonText("c")+`]`, jsonList(jsonRow("", jsonCell("", "a")))))},
		{name: "a table with a caption of its own", text: `<figure><table><caption>c</caption><tr><td>a</td></tr></table></figure>`,
			want: jsonList(jsonBlock("table", 0, "", `"caption":[`+jsonText("c")+`]`, jsonList(jsonRow("", jsonCell("", "a")))))},
		{name: "a table and white space and comments", text: freeform("<figure>\n<!-- a --><table><tr><td>a</td></tr></table>\n</figure>"),
			want: jsonList(jsonBlock("table", 0, "", "", jsonList(jsonRow("", jsonCell("", "a")))))},
	})
}

func TestClassicAnchors(t *testing.T) {
	photo := mediaIDs.photo
	notImported := func(name, why, pointer string) string {
		return fmt.Sprintf("skipped: the anchor %q is not imported: %s -> %s", name, why, pointer)
	}
	runClassic(t, []classicCase{
		// The place that a link goes to is an a element with a name or an id and no address.
		{name: "a link and the place it goes to, in a line of its own", text: "<a href=\"#sec1\">目次へ</a>\n\n<a name=\"sec1\"></a>\n\n<h2>見出し</h2>",
			want: jsonList(jsonBlock("paragraph", 0, "", "", jsonList(jsonText("目次へ", `{"type":"link","attrs":{"anchor":"sec1"}}`))),
				jsonHeading(2, `"anchor":"sec1"`, `"level":2`, "見出し"))},
		{name: "a name in a heading", text: `<h2><a name="sec1"></a>見出し</h2>`, want: jsonList(jsonHeading(0, `"anchor":"sec1"`, `"level":2`, "見出し"))},
		{name: "an id in a heading", text: `<h2><a id="sec2"></a>見出し</h2>`, want: jsonList(jsonHeading(0, `"anchor":"sec2"`, `"level":2`, "見出し"))},
		{name: "the id wins over the name, and the name is noted", text: `<h2><a id="i" name="n"></a>H</h2>`,
			want:  jsonList(jsonHeading(0, `"anchor":"i"`, `"level":2`, "H")),
			lines: []string{notImported("n", `the element has the id "i"`, "/body/0")}},
		{name: "the same id and name", text: `<h2><a id="i" name="i"></a>H</h2>`, want: jsonList(jsonHeading(0, `"anchor":"i"`, `"level":2`, "H"))},
		{name: "an id that is empty", text: `<h2><a id="" name="n"></a>H</h2>`, want: jsonList(jsonHeading(0, `"anchor":"n"`, `"level":2`, "H"))},
		{name: "the text of the element stays", text: `<p>text <a name="x">the place</a> more</p>`,
			want: jsonList(jsonBlock("paragraph", 0, `"anchor":"x"`, "", jsonList(jsonText("text the place more"))))},
		{name: "the id of the block is its anchor, so the place is not", text: `<h2 id="own"><a name="sec"></a>H</h2>`,
			want:  jsonList(jsonHeading(0, `"anchor":"own"`, `"level":2`, "H")),
			lines: []string{notImported("sec", `the block has the anchor "own"`, "/body/0")}},
		{name: "two places in a block: the first", text: `<p><a name="a"></a>x<a name="b"></a></p>`,
			want:  jsonList(jsonBlock("paragraph", 0, `"anchor":"a"`, "", jsonList(jsonText("x")))),
			lines: []string{notImported("b", `the block has the anchor "a"`, "/body/0")}},
		{name: "the same place twice", text: `<p><a name="a"></a>x<a id="a"></a></p>`, want: jsonList(jsonBlock("paragraph", 0, `"anchor":"a"`, "", jsonList(jsonText("x"))))},
		{name: "places in lines of their own wait for the next block, which has the first", text: "<a name=\"a\"></a>\n\n<a name=\"b\"></a>\n\n<h2>H</h2>",
			want:  jsonList(jsonHeading(2, `"anchor":"a"`, `"level":2`, "H")),
			lines: []string{notImported("b", `the block has the anchor "a"`, "/body/0")}},
		{name: "a place with no block after it", text: "<h2>H</h2>\n\n<a name=\"z\"></a>", want: jsonList(jsonHeading(0, "", `"level":2`, "H")),
			lines: []string{notImported("z", "no block comes after it", "/body")}},
		{name: "a place that another block has", text: "<h2 id=\"s\">A</h2>\n\n<a name=\"s\"></a>\n\n<h2>B</h2>",
			want:  jsonList(jsonHeading(0, `"anchor":"s"`, `"level":2`, "A"), jsonHeading(2, "", `"level":2`, "B")),
			lines: []string{notImported("s", "another block has it", "/body/1")}},
		{name: "a place with a name that is not accepted", text: "<a name=\"a b\"></a>\n\n<h2>H</h2>", want: jsonList(jsonHeading(1, "", `"level":2`, "H")),
			lines: []string{notImported("a b", `invalid name "a b": use ASCII letters, digits, "-" and "_"`, "/body/0")}},
		{name: "an empty heading passes its place on", text: "<h2><a name=\"x\"></a></h2>\n\n<h3>Next</h3>", want: jsonList(jsonHeading(1, `"anchor":"x"`, `"level":3`, "Next")),
			lines: []string{"skipped: the empty heading is removed -> /body"}},
		{name: "a place before an element that is not converted", text: freeform("<a name=\"x\"></a>\n\n<dl><dt>a</dt></dl>"),
			want:  jsonList(jsonBlock("legacy", 1, `"anchor":"x"`, `"html":"<dl><dt>a</dt></dl>","reason":"the element <dl> is not converted"`, "")),
			lines: []string{legacyLine("the element <dl> is not converted", "/body/0")}},
		{name: "a place before an element that has no block", text: "<a name=\"a\"></a>\n\n<script>x</script>\n\nText", want: jsonList(jsonBlock("paragraph", 2, `"anchor":"a"`, "", jsonList(jsonText("Text")))),
			lines: []string{"skipped: removed <script> with their content -> /body"}},
		{name: "a place and an image in the same paragraph", text: `<a name="x"></a>` + classicImg("wp-image-21"),
			want: jsonList(jsonImage(1, photo, `"anchor":"x"`, `"alt":"Photo"`))},
		{name: "a place before a group is the anchor of the group", text: `<a name="g"></a><div><p>x</p></div>`,
			want: jsonList(jsonHolder("group", 1, `"anchor":"g"`, "", jsonPara(2, "x")))},
		{name: "a place in a group is the anchor of what comes after it in the group", text: `<div><a name="g"></a><p>x</p></div>`,
			want: jsonList(jsonHolder("group", 0, "", "", jsonBlock("paragraph", 2, `"anchor":"g"`, "", jsonList(jsonText("x")))))},
		{name: "a place in a group that is removed waits for the next block of the flow", text: "<div><a name=\"g\"></a></div>\n\n<h2>H</h2>",
			want:  jsonList(jsonHeading(2, `"anchor":"g"`, `"level":2`, "H")),
			lines: []string{"skipped: the empty group is removed -> /body"}},
		{name: "a place at the end of a group has no block after it", text: `<div><p>x</p><a name="z"></a></div>`,
			want:  jsonList(jsonHolder("group", 0, "", "", jsonPara(1, "x"))),
			lines: []string{notImported("z", "no block comes after it", "/body/0/content")}},
		{name: "a place in an item of a list is the anchor of the list", text: `<ul><li><a name="x"></a>one</li></ul>`,
			want: jsonList(jsonBlock("list", 0, `"anchor":"x"`, "", jsonList(jsonItem(jsonPara(1, "one")))))},
		{name: "a place in a cell of a table is the anchor of the table", text: `<table><tr><td><a name="x"></a>a</td></tr></table>`,
			want: jsonList(jsonBlock("table", 0, `"anchor":"x"`, "", jsonList(jsonRow("", jsonCell("", "a")))))},
		{name: "a place in the caption of an image is the anchor of the image", text: `<figure>` + classicImg("wp-image-21") + `<figcaption><a name="x"></a>c</figcaption></figure>`,
			want: jsonList(jsonImage(0, photo, `"anchor":"x"`, `"alt":"Photo","caption":[`+jsonText("c")+`]`))},
		{name: "a place in the citation of a quote is the anchor of the quote", text: freeform(`<blockquote><p>q</p><cite><a name="x"></a>c</cite></blockquote>`),
			want: jsonList(jsonBlock("quote", 0, `"anchor":"x"`, `"citation":[`+jsonText("c")+`]`, jsonList(jsonPara(1, "q"))))},
		{name: "a place in a block of text that is a legacy block is gone with it", text: freeform(`<p id="a"><a name="x"></a><img src="https://other.example.org/x.png"> y</p><p><a name="x"></a>z</p>`),
			want: jsonList(jsonLegacy(1, `<p id="a"><a name="x"></a><img src="https://other.example.org/x.png"> y</p>`, "the text has <img>, which rich text cannot have"),
				jsonBlock("paragraph", 2, `"anchor":"x"`, "", jsonList(jsonText("z")))),
			lines: []string{legacyLine("the text has <img>, which rich text cannot have", "/body/0")}},
		{name: "a place in a text of the block editor stays as it was", text: wpPara(`<a name="x"></a>text`), want: jsonList(jsonPara(0, "text")),
			lines: []string{"changed: kept only the text of <a> -> /body/0"}},
		{name: "a link that has an address is not a place", text: `<a id="x" href="#y">text</a>`,
			want: jsonList(jsonBlock("paragraph", 0, "", "", jsonList(jsonText("text", `{"type":"link","attrs":{"anchor":"y"}}`))))},
		{name: "a place in a text with no name or id", text: `<a>text</a>`, want: jsonList(jsonPara(0, "text")), lines: []string{"changed: kept only the text of <a> -> /body/0"}},
	})

	t.Run("the places that the conversion takes are limited", func(t *testing.T) {
		lowerLimit(t, &maxWPBlocks, 3)
		var b strings.Builder
		b.WriteString("<p>")
		for i := range 5 {
			fmt.Fprintf(&b, `<a name="n%d"></a>`, i)
		}
		b.WriteString("x</p>")
		runClassic(t, []classicCase{{name: "five places", text: b.String(),
			want: jsonList(jsonBlock("paragraph", 0, `"anchor":"n0"`, "", jsonList(jsonText("x")))),
			lines: []string{
				notImported("n1", `the block has the anchor "n0"`, "/body/0"), notImported("n2", `the block has the anchor "n0"`, "/body/0"),
				"skipped: the anchors after the first 3 are not imported -> /body",
				"changed: kept only the text of <a> -> /body/0",
			}}})
	})
}

func TestClassicMoreInside(t *testing.T) {
	const removed = "skipped: the more tag is removed: it is not at the top of the content -> "
	runClassic(t, []classicCase{
		{name: "in the item of a list", text: "One\n\n<ul>\n<li>x<!--more--></li>\n</ul>\n\nTwo",
			want:  jsonList(jsonPara(0, "One"), jsonBlock("list", 1, "", "", jsonList(jsonItem(jsonPara(2, "x")))), jsonPara(3, "Two")),
			lines: []string{removed + "/body/1"}},
		{name: "in a heading, as its last child", text: "<h2>Head<!--more--></h2>", want: jsonList(jsonHeading(0, "", `"level":2`, "Head")), lines: []string{removed + "/body/0"}},
		{name: "in a cell of a table", text: "<table><tr><td>a<!--more--></td></tr></table>",
			want: jsonList(jsonBlock("table", 0, "", "", jsonList(jsonRow("", jsonCell("", "a"))))), lines: []string{removed + "/body/0"}},
		{name: "in pre", text: "<pre>a<!--more-->b</pre>", want: jsonList(jsonBlock("code", 0, "", "", `"ab"`)), lines: []string{removed + "/body/0"}},
		{name: "in the figure of an image", text: `<figure>` + classicImg("wp-image-21") + `<!--more--></figure>`,
			want: jsonList(jsonImage(0, mediaIDs.photo, "", `"alt":"Photo"`)), lines: []string{removed + "/body/0"}},
		{name: "in an element of a paragraph", text: "One <strong>b<!--more-->c</strong>", want: jsonList(jsonRich(0, "", jsonText("One "), jsonText("bc", jsonBold))),
			lines: []string{removed + "/body/0"}},
		{name: "in an element of a paragraph, next to an image", text: classicImg("wp-image-21") + "<br><em>a<!--more-->b</em>",
			want: jsonList(jsonImage(0, mediaIDs.photo, "", `"alt":"Photo"`), jsonRich(1, "", jsonText("ab", `{"type":"italic"}`))), lines: []string{removed + "/body/0"}},
		{name: "one note for the tags of a block", text: "<ul><li>a<!--more--></li><li>b<!--more--></li></ul>",
			want:  jsonList(jsonBlock("list", 0, "", "", jsonList(jsonItem(jsonPara(1, "a")), jsonItem(jsonPara(2, "b"))))),
			lines: []string{removed + "/body/0"}},
		{name: "the tag of the content is not that of the element", text: "<h2>H<!--more--></h2>\n\nA\n\n<!--more-->\n\nB",
			want:  jsonList(jsonHeading(0, "", `"level":2`, "H"), jsonPara(1, "A"), jsonPara(2, "B")),
			more:  moreAt(2),
			lines: []string{removed + "/body/0"}},
		{name: "a tag that is a child of a paragraph is its own, not a note", text: "A<!--more-->B", want: jsonList(jsonPara(0, "AB")), more: moreAt(1)},
		{name: "a comment that is not a tag", text: "<h2>H<!-- x --></h2>", want: jsonList(jsonHeading(0, "", `"level":2`, "H"))},
		{name: "in a legacy block, nothing is said", text: freeform("<dl><dt>a<!--more--></dt></dl>"),
			want:  jsonList(jsonLegacy(0, "<dl><dt>a<!--more--></dt></dl>", "the element <dl> is not converted")),
			lines: []string{legacyLine("the element <dl> is not converted", "/body/0")}},
	})

	t.Run("in a synced pattern, the tag is a comment", func(t *testing.T) {
		imp := New("site.xml", patternsExport(1, func(int) string { return "<h2>Head<!--more--></h2>\n<p>a<b>b<!--more-->c</b></p>" }), NewReport(Info{File: "site.xml"}))
		_, res := limitedBody(t, imp, wpRef(1))
		got, err := res.Blocks.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if want := withIDs(jsonList(jsonHeading(0, "", `"level":2`, "Head"), jsonRich(1, "", jsonText("a"), jsonText("bc", jsonBold)))); string(got) != want {
			t.Errorf("the blocks are\n%s\nwant\n%s", got, want)
		}
		wantLines(t, imp.Report, bodyLines(`changed: the synced pattern 1 ("Pattern 1") is copied here; whiteroom has no shared blocks yet (stage 2) -> /body/0`))
	})
}

func TestClassicStyleOfElements(t *testing.T) {
	lost := func(items, tag, pointer string) string { return elementLost(items, tag, pointer) }
	red := `{"type":"highlight","attrs":{"color":{"value":"#f00"}}}`
	runClassic(t, []classicCase{
		{name: "a span that is a color and a size", text: `<span style="color: #ff0000; font-size: 12pt;">赤</span>`,
			want:  jsonList(jsonRich(0, "", jsonText("赤", `{"type":"highlight","attrs":{"color":{"value":"#ff0000"}}}`))),
			lines: []string{lost("style.font-size", "<span>", "/body/0")}},
		{name: "a span of a color only", text: `<span style="color: #f00">x</span>`, want: jsonList(jsonRich(0, "", jsonText("x", red)))},
		{name: "a color that whiteroom does not accept is lost", text: `<span style="color: red">x</span>`, want: jsonList(jsonPara(0, "x")),
			lines: []string{lost("style.color", "<span>", "/body/0"), "changed: kept only the text of <span> -> /body/0"}},
		{name: "a span of the marks that it makes", text: `<span style="font-weight: bold; font-style: italic; text-decoration: underline line-through; background-color: #ff0">x</span>`,
			want: jsonList(jsonRich(0, "", jsonText("x", `{"type":"underline"}`, `{"type":"strike"}`, jsonBold, `{"type":"italic"}`,
				`{"type":"highlight","attrs":{"backgroundColor":{"value":"#ff0"}}}`)))},
		{name: "what a span does not take", text: `<span style="font-weight: normal; font-style: normal; text-decoration: overline; color: var(--x); background-color: transparent">x</span>`,
			want:  jsonList(jsonPara(0, "x")),
			lines: []string{lost("style.font-weight, style.font-style, style.text-decoration, style.color, style.background-color", "<span>", "/body/0"), "changed: kept only the text of <span> -> /body/0"}},
		{name: "a span of a size only", text: `<span style="font-size: 12pt">x</span>`, want: jsonList(jsonPara(0, "x")),
			lines: []string{lost("style.font-size", "<span>", "/body/0"), "changed: kept only the text of <span> -> /body/0"}},
		{name: "strong, em and a: no property is taken", text: `<strong style="color: red; font-size: 20px;">太字</strong> と <a style="color: red;" href="https://example.com/about/">リンク</a> と <em class="foo" style="font-size: 20px">斜体</em>`,
			want: jsonList(jsonRich(0, "", jsonText("太字", jsonBold), jsonText(" と "), jsonText("リンク", `{"type":"link","attrs":{"entry":"`+mediaIDs.about+`"}}`), jsonText(" と "), jsonText("斜体", `{"type":"italic"}`))),
			lines: []string{
				lost("style.color, style.font-size", "<strong>", "/body/0"), lost("style.color", "<a>", "/body/0"), lost("style.font-size", "<em>", "/body/0"),
			}},
		{name: "the elements of one name, in one note", text: `<span style="font-size: 1px">a</span> <span style="font-size: 2px; margin: 0">b</span> <span style="font-size: 3px">c</span>`,
			want:  jsonList(jsonPara(0, "a b c")),
			lines: []string{lost("style.font-size, style.margin", "<span>", "/body/0"), "changed: kept only the text of <span> -> /body/0"}},
		{name: "a note for each block", text: `<span style="margin: 0">a</span>` + "\n\n" + `<span style="margin: 0">b</span>`,
			want: jsonList(jsonPara(0, "a"), jsonPara(1, "b")),
			lines: []string{
				lost("style.margin", "<span>", "/body/0"), lost("style.margin", "<span>", "/body/1"),
				"changed: kept only the text of <span> -> /body/0", "changed: kept only the text of <span> -> /body/1",
			}},
		{name: "a mark with a style", text: `<mark style="background-color: #ff0; padding: 0">x</mark>`,
			want:  jsonList(jsonRich(0, "", jsonText("x", `{"type":"highlight","attrs":{"backgroundColor":{"value":"#ff0"}}}`))),
			lines: []string{lost("style.padding", "<mark>", "/body/0")}},
		{name: "an element in a heading, a list and a table", text: `<h2><span style="font-size: 9px">h</span></h2><ul><li><b style="margin: 0">l</b></li></ul><table><tr><td><i style="margin: 0">t</i></td></tr></table>`,
			want: jsonList(jsonHeading(0, "", `"level":2`, "h"),
				jsonBlock("list", 1, "", "", jsonList(jsonItem(jsonBlock("paragraph", 2, "", "", jsonList(jsonText("l", jsonBold)))))),
				jsonBlock("table", 3, "", "", jsonList(jsonRow("", `{"type":"tableCell","content":[{"type":"text","text":"t","marks":[{"type":"italic"}]}]}`)))),
			lines: []string{
				lost("style.font-size", "<span>", "/body/0"), "changed: kept only the text of <span> -> /body/0",
				lost("style.margin", "<b>", "/body/1/content/0/content/0"), lost("style.margin", "<i>", "/body/2/content/0/content/0/content"),
			}},
		{name: "an element with a style that has nothing in it", text: `<span style="">a</span><b style=" ; ">b</b>`, want: jsonList(jsonRich(0, "", jsonText("a"), jsonText("b", jsonBold))),
			lines: []string{"changed: kept only the text of <span> -> /body/0"}},
		{name: "the block editor says what it said", text: wpPara(`<span style="font-size: 9px">a</span>`), want: jsonList(jsonPara(0, "a")),
			lines: []string{"changed: kept only the text of <span> -> /body/0"}},
	})
}

func TestClassicPartsOfListsAndTables(t *testing.T) {
	runClassic(t, []classicCase{
		{name: "the style, the class and the id of the items of a list are lost in one note", text: `<ul><li style="color: red" class="c d" id="i">a</li><li class="wp-x" id="j" style="color: blue; margin: 0">b</li></ul>`,
			want:  jsonList(jsonBlock("list", 0, "", "", jsonList(jsonItem(jsonPara(1, "a")), jsonItem(jsonPara(2, "b"))))),
			lines: []string{elementLost("style.color, class, id, style.margin", "<ul>", "/body/0")}},
		{name: "the items of a list in a list", text: `<ol><li>a<ul><li class="n">b</li></ul></li></ol>`,
			want:  jsonList(jsonBlock("list", 0, "", `"ordered":true`, jsonList(jsonItem(jsonPara(1, "a"), jsonBlock("list", 2, "", "", jsonList(jsonItem(jsonPara(3, "b")))))))),
			lines: []string{elementLost("class", "<ol>", "/body/0")}},
		{name: "the classes of WordPress are not lost", text: `<ul><li class="alignleft wp-x size-y attachment-z">a</li></ul>`,
			want: jsonList(jsonBlock("list", 0, "", "", jsonList(jsonItem(jsonPara(1, "a")))))},
		{name: "the class, the id and the style of the rows and the cells of a table", text: `<table style="width: 1px"><tr class="r" id="s"><td class="c" id="d" style="color: red">a</td><th class="wp-x">b</th></tr></table>`,
			want:  jsonList(jsonBlock("table", 0, "", "", jsonList(jsonRow("", jsonCell("", "a"), jsonCell(`"header":true`, "b"))))),
			lines: []string{elementLost("style.width, class, id, style.color", "<table>", "/body/0")}},
		{name: "the class of a cell for its alignment is read, not lost", text: `<table><tr><td class="has-text-align-center">a</td></tr></table>`,
			want: jsonList(jsonBlock("table", 0, "", "", jsonList(jsonRow("", jsonCell(`"textAlign":"center"`, "a")))))},
		{name: "the id of a cell of a table of a figure", text: `<figure><table><tr><td id="d">a</td></tr></table></figure>`,
			want:  jsonList(jsonBlock("table", 0, "", "", jsonList(jsonRow("", jsonCell("", "a"))))),
			lines: []string{elementLost("id", "<figure>", "/body/0")}},
	})
}

func TestClassicNestedListText(t *testing.T) {
	const reason = "a list item has content after its list"
	nested := func(item string) string { return freeform("<ul><li>" + item + "</li></ul>") }
	runClassic(t, []classicCase{
		{name: "text after the list in the item", text: nested("a<ul><li>b</li></ul>c"),
			want:  jsonList(jsonLegacy(1, "<ul><li>a<ul><li>b</li></ul>c</li></ul>", reason)),
			lines: []string{legacyLine(reason, "/body/0")}},
		{name: "an element after the list in the item", text: nested("a<ul><li>b</li></ul><b>c</b>"),
			want:  jsonList(jsonLegacy(1, "<ul><li>a<ul><li>b</li></ul><b>c</b></li></ul>", reason)),
			lines: []string{legacyLine(reason, "/body/0")}},
		{name: "text after the list, which wpautop put in a paragraph", text: "<ul>\n<li>a<ul><li>b</li></ul>c</li>\n</ul>",
			want:  jsonList(jsonLegacy(1, "<ul>\n<li>a\n<ul>\n<li>b</li>\n</ul>\n<p>c</p></li>\n</ul>", reason)),
			lines: []string{legacyLine(reason, "/body/0")}},
		{name: "text after a list in a list in the item", text: freeform("<ul><li>a<ul><li>b<ol><li>c</li></ol>d</li></ul></li></ul>"),
			want:  jsonList(jsonLegacy(3, "<ul><li>a<ul><li>b<ol><li>c</li></ol>d</li></ul></li></ul>", reason)),
			lines: []string{legacyLine(reason, "/body/0")}},
		{name: "white space and comments after the list", text: nested("a<ul><li>b</li></ul> \n<!-- c -->"),
			want: jsonList(jsonBlock("list", 0, "", "", jsonList(jsonItem(jsonPara(1, "a"), jsonBlock("list", 2, "", "", jsonList(jsonItem(jsonPara(3, "b"))))))))},
		{name: "text before the list", text: nested("a <b>b</b><ul><li>c</li></ul>"),
			want: jsonList(jsonBlock("list", 0, "", "", jsonList(jsonItem(jsonRich(1, "", jsonText("a "), jsonText("b", jsonBold)), jsonBlock("list", 2, "", "", jsonList(jsonItem(jsonPara(3, "c"))))))))},
		{name: "a list of the block editor, which is not the HTML of the classic editor", text: wpBlock("list", "", "<ul><li>a<ul><li>b</li></ul>c</li></ul>"),
			want: jsonList(jsonBlock("list", 0, "", "", jsonList(jsonItem(jsonRich(1, "", jsonText("ac")), jsonBlock("list", 2, "", "", jsonList(jsonItem(jsonPara(3, "b"))))))))},
	})
}

func TestClassicImagesAndNoBreakSpaces(t *testing.T) {
	photo, banner := mediaIDs.photo, mediaIDs.banner
	const ref = "&nbsp;"
	nbsp := string(rune(0xA0))
	// The JSON of a paragraph of text, which has a no-break space in it as it is.
	para := func(n int, text string) string {
		return jsonBlock("paragraph", n, "", "", jsonList(`{"type":"text","text":"`+text+`"}`))
	}
	runClassic(t, []classicCase{
		{name: "after an image", text: `<p>` + classicImg("wp-image-21") + ref + `</p>`, want: jsonList(jsonImage(0, photo, "", `"alt":"Photo"`))},
		{name: "before an image", text: `<p>` + ref + classicImg("wp-image-21") + `</p>`, want: jsonList(jsonImage(0, photo, "", `"alt":"Photo"`))},
		{name: "between images", text: `<p>` + classicImg("wp-image-21") + ref + `<img class="wp-image-22" src="` + bannerURL + `" alt="B" /></p>`,
			want: jsonList(jsonImage(0, photo, "", `"alt":"Photo"`), jsonImage(1, banner, "", `"alt":"B"`))},
		{name: "spaces and no-break spaces after an image", text: `<p>` + classicImg("wp-image-21") + ` ` + ref + ref + ` </p>`, want: jsonList(jsonImage(0, photo, "", `"alt":"Photo"`))},
		{name: "a paragraph of its own is text, as the classic editor makes an empty line", text: "<p>" + ref + "</p>\n<p>" + classicImg("wp-image-21") + "</p>\n<p>" + ref + "</p>",
			want: jsonList(para(0, nbsp), jsonImage(1, photo, "", `"alt":"Photo"`), para(2, nbsp))},
		{name: "no-break spaces in the text next to an image are text", text: `<p>` + classicImg("wp-image-21") + `a` + ref + `</p>`,
			want: jsonList(jsonImage(0, photo, "", `"alt":"Photo"`), para(1, "a"+nbsp))},
		{name: "no-break spaces at the far end of the text are text", text: `<p>` + ref + `<b>b</b>` + classicImg("wp-image-21") + `</p>`,
			want: jsonList(jsonBlock("paragraph", 0, "", "", jsonList(`{"type":"text","text":"`+nbsp+`"}`, jsonText("b", jsonBold))), jsonImage(1, photo, "", `"alt":"Photo"`))},
	})
}

func TestClassicLanguageOfPre(t *testing.T) {
	runClassic(t, []classicCase{
		{name: "the language of the code", text: `<pre><code class="language-php">echo 1;</code></pre>`, want: jsonList(jsonBlock("code", 0, "", `"language":"php"`, `"echo 1;"`))},
		{name: "the language of the pre when the code has none", text: `<pre class="language-go"><code>x := 1</code></pre>`, want: jsonList(jsonBlock("code", 0, "", `"language":"go"`, `"x := 1"`))},
		{name: "the language of the code wins", text: `<pre class="language-go"><code class="language-php">x</code></pre>`, want: jsonList(jsonBlock("code", 0, "", `"language":"php"`, `"x"`))},
		{name: "a class of the code that is not a language", text: `<pre class="language-go"><code class="hljs">x</code></pre>`, want: jsonList(jsonBlock("code", 0, "", `"language":"go"`, `"x"`))},
		{name: "the language of the pre when it holds only text", text: `<pre class="foo language-go">x</pre>`, want: jsonList(jsonBlock("code", 0, `"className":"foo"`, `"language":"go"`, `"x"`))},
		{name: "the language of the pre when the code is not all it holds", text: `<pre class="language-go"><code>a</code>b</pre>`,
			want:  jsonList(jsonBlock("code", 0, "", `"language":"go"`, `"ab"`)),
			lines: []string{"changed: kept only the text of <code> -> /body/0"}},
		{name: "a language of the pre that is not accepted", text: `<pre class="language-a.b"><code>x</code></pre>`, want: jsonList(jsonBlock("code", 0, "", "", `"x"`)),
			lines: []string{`skipped: the language "a.b" is not imported: invalid name "a.b": use ASCII letters, digits, "-" and "_" -> /body/0`}},
		{name: "no language", text: `<pre><code>x</code></pre>`, want: jsonList(jsonBlock("code", 0, "", "", `"x"`))},
	})
}

func TestClassicAddressWithShortcode(t *testing.T) {
	const record = `review: the address %q has a shortcode, which whiteroom does not run -> /body/0`
	runClassic(t, []classicCase{
		{name: "a shortcode in the address of a link", text: `<a href="[home_url]/about/">リンク</a>`,
			want:  jsonList(jsonBlock("paragraph", 0, "", "", jsonList(jsonText("リンク", `{"type":"link","attrs":{"href":"/%5Bhome_url%5D/about/"}}`)))),
			lines: []string{fmt.Sprintf(record, "[home_url]/about/")}},
		{name: "a registered name in the address", text: `<a href="https://example.org/?x=[gallery ids=1]">a</a>`,
			want:  jsonList(jsonBlock("paragraph", 0, "", "", jsonList(jsonText("a", `{"type":"link","attrs":{"href":"https://example.org/?x=[gallery ids=1]"}}`)))),
			lines: []string{fmt.Sprintf(record, "https://example.org/?x=[gallery ids=1]")}},
		{name: "brackets that are no shortcode", text: `<a href="/x/?a[]=1&b[1]=2&c=[PR]">a</a>`,
			want: jsonList(jsonBlock("paragraph", 0, "", "", jsonList(jsonText("a", `{"type":"link","attrs":{"href":"/x/?a[]=1&b[1]=2&c=[PR]"}}`))))},
		{name: "the block editor says what it said", text: wpPara(`<a href="[home_url]/about/">a</a>`),
			want: jsonList(jsonBlock("paragraph", 0, "", "", jsonList(jsonText("a", `{"type":"link","attrs":{"href":"/%5Bhome_url%5D/about/"}}`))))},
	})
}

func TestClassicCenterInText(t *testing.T) {
	runClassic(t, []classicCase{
		{name: "a center element in a cell sets its text apart", text: `<table><tr><td><center>x</center>y</td></tr></table>`,
			want:  jsonList(jsonBlock("table", 0, "", "", jsonList(jsonRow("", `{"type":"tableCell","content":[{"type":"text","text":"x"},{"type":"hardBreak"},{"type":"text","text":"y"}]}`)))),
			lines: []string{"changed: kept only the text of <center> -> /body/0/content/0/content/0/content"}},
	})
}
