package render

import (
	"html/template"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// headingJSON returns the JSON of a heading of level 2 with the ID id and the text s.
func headingJSON(id, s string) string {
	return `{"type": "heading", "id": "` + id + `", "attrs": {"level": 2}, "content": [{"type": "text", "text": "` + s + `"}]}`
}

// checkDocument checks the result of Document for lists against want, the HTML of each list, css and the warnings.
func checkDocument(t *testing.T, r *Renderer, lists []content.Blocks, want []string, css string, warnings []Warning) {
	t.Helper()
	html, gotCSS, got, err := r.Document(lists...)
	if err != nil {
		t.Fatalf("Document() error: %v", err)
	}
	if len(html) != len(want) {
		t.Fatalf("Document() = %q, want %d lists", html, len(want))
	}
	for i := range want {
		if string(html[i]) != want[i] {
			t.Errorf("list %d:\n%s\nwant:\n%s", i, html[i], want[i])
		}
	}
	if gotCSS.String() != css {
		t.Errorf("CSS:\n%s\nwant:\n%s", gotCSS, css)
	}
	if !slices.Equal(got, warnings) {
		t.Errorf("warnings:\n%q\nwant:\n%q", got, warnings)
	}
}

// TestDocumentAnchors checks the automatic anchors of headings in a document of two lists. They avoid the anchors of
// every block, at any depth, also of blocks that are not rendered; b_head06 is not a valid block ID.
func TestDocumentAnchors(t *testing.T) {
	body := decodeBlocks(t, `[
		`+headingJSON("b_00head01", "一")+`,
		`+headingJSON("b_00head02", "二")+`,
		`+headingJSON("b_00head03", "三")+`,
		{"type": "paragraph", "id": "b_0paragr1", "anchor": "b_00head03-2"},
		{"type": "heading", "id": "b_00head04", "attrs": {"level": 3}, "content": [{"type": "text", "text": "四"}]},
		{"type": "heading", "id": "b_00head04", "attrs": {"level": 3}, "content": [{"type": "text", "text": "四"}]},
		{"type": "heading", "id": "b_00head05", "anchor": "kept", "attrs": {"level": 2}, "content": [{"type": "text", "text": "五"}]},
		`+headingJSON("b_head06", "六")+`,
		{"type": "heading", "attrs": {"level": 2}, "content": [{"type": "text", "text": "七"}]},
		{"type": "heading", "id": "b_00head08", "variant": "x", "attrs": {"level": 2}, "content": [{"type": "text", "text": "八"}]}
	]`)
	field := decodeBlocks(t, `[
		{"type": "paragraph", "id": "b_0paragr2", "anchor": "b_00head02"},
		{"type": "group", "id": "b_000group", "content": [{"type": "paragraph", "id": "b_0paragr3", "anchor": "b_00head03"}]},
		`+headingJSON("b_00head01", "一")+`,
		{"type": "html", "id": "b_0000html", "anchor": "b_00head09", "attrs": {"html": "<p>x</p>"}},
		{"type": "group", "id": "b_00group2", "content": [`+headingJSON("b_00head09", "九")+`]}
	]`)
	r := newRenderer(t, testTheme(t, nil), "ja", "", nil)
	want := []string{
		`<h2 id="b_00head01">一</h2>
<h2 id="b_00head02-2">二</h2>
<h2 id="b_00head03-3">三</h2>
<p id="b_00head03-2"></p>
<h3 id="b_00head04">四</h3>
<h3 id="b_00head04-2">四</h3>
<h2 id="kept">五</h2>
<h2>六</h2>
<h2>七</h2>
<h2 id="b_00head08">八</h2>`,
		`<p id="b_00head02"></p>
<div>
<p id="b_00head03"></p>
</div>
<h2 id="b_00head01-2">一</h2>
<div>
<h2 id="b_00head09-2">九</h2>
</div>`,
	}
	warnings := []Warning{
		{List: 0, Path: "/9/variant", Msg: `the theme has no heading variant "x"; rendered without its class`},
		{List: 1, Path: "/3", Msg: "html blocks are not rendered yet"},
	}
	checkDocument(t, r, []content.Blocks{body, field}, want, "", warnings)
	// Rendered alone, the field has the anchors that the body took.
	checkDocument(t, r, []content.Blocks{field}, []string{strings.ReplaceAll(want[1], "b_00head01-2", "b_00head01")},
		"", []Warning{{List: 0, Path: "/3", Msg: "html blocks are not rendered yet"}})
}

// themeBlocks are block templates of a theme that write the values of their data.
var themeBlocks = map[string]string{
	"theme.yaml": "schemaVersion: 1\npresets:\n  color:\n    primary: \"#1e73be\"\n" +
		"blocks:\n  paragraph:\n    variants:\n      lead: リード\n",
	"blocks/paragraph.html": "{{.ID}}|{{.Anchor}}|{{.Class}}|{{.TextAlign}}|{{.Content}}\n\n",
	"blocks/heading.html":   `<h{{.Level}} id="{{.Anchor}}" data-align="{{.TextAlign}}">{{.Content}}</h{{.Level}}>` + "\n",
	"blocks/list.html":      `<ol data-ordered="{{.Ordered}}" data-start="{{.Start}}">{{.Children}}</ol>` + "\n",
	"blocks/listItem.html":  `<li class="item">{{.Children}}</li>` + "\r\n",
	"blocks/table.html":     "<table>{{.Head}}|{{.Body}}|{{.Foot}}|{{.Caption}}</table>\n",
	"blocks/tableRow.html":  `<tr data-section="{{.Section}}">{{.Children}}</tr>`,
	"blocks/tableCell.html": "<td>{{.Header}} {{.Colspan}} {{.Rowspan}} {{.TextAlign}} {{.Content}}</td>\r\n\r\n",
	"blocks/buttons.html":   `<div data-justify="{{.Justify}}">{{.Children}}</div>` + "\n",
	"blocks/button.html":    `<a data-width="{{.Width}}"{{with .Link}} href="{{.Href}}"{{end}}>{{.Label}}</a>` + "\n",
	"blocks/columns.html":   `<div data-stack="{{.StackOnMobile}}" data-align="{{.VerticalAlign}}">{{.Children}}</div>` + "\n",
	"blocks/column.html":    `<div data-width="{{.Width}}" data-align="{{.VerticalAlign}}">{{.Children}}</div>` + "\n",
	"blocks/group.html":     "{{.TagName}}|{{.Label}}|{{.Layout.Type}}|{{.Layout.Columns}}|{{.Align}}|{{.Children}}\n",
	"blocks/spacer.html":    `<div data-height="{{.Height.Preset}}{{.Height.Value}}"></div>` + "\t\n",
}

// TestThemeBlocks checks the block templates of a theme, which replace the default templates of their types, the data
// that they get, and that only the line breaks at the end of their output are left out.
func TestThemeBlocks(t *testing.T) {
	bs := decodeBlocks(t, `[
		{
			"type": "paragraph", "id": "b_0paragr1", "variant": "lead", "className": "intro", "anchor": "top",
			"style": {"textColor": {"preset": "primary"}}, "attrs": {"textAlign": "center"},
			"content": [{"type": "text", "text": "a", "marks": [{"type": "bold"}]}]
		},
		{"type": "paragraph", "id": "b_0paragr2"},
		{"type": "heading", "id": "b_0heading", "attrs": {"level": 3, "textAlign": "right"}, "content": [{"type": "text", "text": "見出し"}]},
		{
			"type": "list", "id": "b_00list01", "attrs": {"ordered": true, "start": 3},
			"content": [
				{"type": "listItem", "content": [`+paragraphJSON("b_0litem01", "x")+`]},
				{"type": "listItem", "content": [`+paragraphJSON("b_0litem02", "y")+`]}
			]
		},
		{
			"type": "list", "id": "b_00list02", "attrs": {"start": 5},
			"content": [{"type": "listItem", "content": [`+paragraphJSON("b_0litem03", "z")+`]}]
		},
		{
			"type": "table", "id": "b_000table", "attrs": {"caption": [{"type": "text", "text": "c"}]},
			"content": [
				{
					"type": "tableRow", "attrs": {"section": "foot"},
					"content": [{"type": "tableCell", "content": [{"type": "text", "text": "f"}]}]
				},
				{
					"type": "tableRow", "attrs": {"section": "head"},
					"content": [{
						"type": "tableCell", "attrs": {"header": true, "colspan": 2, "rowspan": 3, "textAlign": "left"},
						"content": [{"type": "text", "text": "h"}]
					}]
				},
				{
					"type": "tableRow",
					"content": [
						{"type": "tableCell", "content": [{"type": "text", "text": "b1"}]},
						{"type": "tableCell", "content": [{"type": "text", "text": "b2"}]}
					]
				},
				{
					"type": "tableRow", "attrs": {"section": "body"},
					"content": [{"type": "tableCell", "content": [{"type": "text", "text": "b3"}]}]
				},
				{
					"type": "tableRow", "attrs": {"section": "side"},
					"content": [{"type": "tableCell", "content": [{"type": "text", "text": "b4"}]}]
				}
			]
		},
		{
			"type": "buttons", "id": "b_0buttons", "attrs": {"justify": "spaceBetween"},
			"content": [
				{"type": "button", "id": "b_0button1", "attrs": {"label": "x", "width": "50%", "link": {"anchor": "top"}}},
				{"type": "button", "id": "b_0button2", "attrs": {"label": "y"}}
			]
		},
		{
			"type": "columns", "id": "b_0columns", "attrs": {"verticalAlign": "center"},
			"content": [{
				"type": "column", "id": "b_0column1", "attrs": {"width": "25%", "verticalAlign": "stretch"},
				"content": [`+paragraphJSON("b_0colpar1", "p")+`]
			}]
		},
		{"type": "columns", "id": "b_columns2", "attrs": {"stackOnMobile": false}, "content": [{"type": "column", "id": "b_0column2"}]},
		{"type": "columns", "id": "b_columns3", "attrs": {"stackOnMobile": true}, "content": [{"type": "column", "id": "b_0column3"}]},
		{
			"type": "group", "id": "b_000group",
			"attrs": {"tagName": "aside", "label": "注", "layout": {"type": "grid", "columns": 3}, "align": "wide"},
			"content": [`+paragraphJSON("b_0grppar1", "g")+`]
		},
		{"type": "group", "id": "b_00group2", "attrs": {"tagName": "main"}},
		{"type": "spacer", "id": "b_00spacer", "attrs": {"height": {"preset": "large"}}},
		{"type": "spacer", "id": "b_0spacer2", "attrs": {"height": {"value": "40px"}}},
		{"type": "separator", "id": "b_separat1", "className": "thin"},
		{"type": "quote", "id": "b_000quote", "content": [`+paragraphJSON("b_0quotep1", "q")+`]},
		{"type": "code", "id": "b_0000code", "content": "x\n"}
	]`)
	r := newRenderer(t, testTheme(t, themeBlocks), "ja", "", nil)
	want := `b_0paragr1|top|b-0paragr1 is-variant-lead intro|center|<strong>a</strong>
b_0paragr2||||
<h3 id="b_0heading" data-align="right">見出し</h3>
<ol data-ordered="true" data-start="3"><li class="item">b_0litem01||||x</li>
<li class="item">b_0litem02||||y</li></ol>
<ol data-ordered="false" data-start="0"><li class="item">b_0litem03||||z</li></ol>
<table><tr data-section="head"><td>true 2 3 left h</td></tr>|<tr data-section="body"><td>false 0 0  b1</td>
<td>false 0 0  b2</td></tr>
<tr data-section="body"><td>false 0 0  b3</td></tr>
<tr data-section="body"><td>false 0 0  b4</td></tr>|<tr data-section="foot"><td>false 0 0  f</td></tr>|c</table>
<div data-justify="spaceBetween"><a data-width="50%" href="#top">x</a>
<a data-width="">y</a></div>
<div data-stack="true" data-align="center"><div data-width="25%" data-align="stretch">b_0colpar1||||p</div></div>
<div data-stack="false" data-align=""><div data-width="" data-align=""></div></div>
<div data-stack="true" data-align=""><div data-width="" data-align=""></div></div>
aside|注|grid|3|wide|b_0grppar1||||g
div|||0||
<div data-height="large"></div>` + "\t\n" + `<div data-height="40px"></div>` + "\t\n" + `<hr class="thin">
<blockquote>
b_0quotep1||||q
</blockquote>
<pre><code>x
</code></pre>`
	const css = ".b-0paragr1 {\n  color: var(--color-primary) !important;\n}\n"
	checkDocument(t, r, []content.Blocks{bs}, []string{want}, css, nil)
}

// TestThemeBlocksError checks that the error of a block template ends the rendering of the document, without the CSS
// and the warnings of what was rendered before it.
func TestThemeBlocksError(t *testing.T) {
	variant := decodeBlocks(t, `[{
		"type": "paragraph", "id": "b_0paragr1", "variant": "x", "style": {"textColor": {"value": "#111"}}
	}]`)
	tests := []struct {
		name, file, text string
		lists            []content.Blocks
		msg              string
	}{
		{
			name: "separator", file: "blocks/separator.html", text: `<hr class="{{.Missing}}">` + "\n",
			lists: []content.Blocks{variant, decodeBlocks(t, `[{"type": "separator", "id": "b_separat1"}]`)},
			msg: `template: blocks/separator.html:1:13: executing "separator.html" at <.Missing>: ` +
				"can't evaluate field Missing in type render.Separator",
		},
		{
			name: "cell in a table in a group", file: "blocks/tableCell.html", text: "<td>{{.Content}}{{.Class}}</td>",
			lists: []content.Blocks{variant, decodeBlocks(t, `[{
				"type": "group", "id": "b_000group",
				"content": [{"type": "table", "id": "b_000table", "content": [{"type": "tableRow", "content": [{"type": "tableCell"}]}]}]
			}]`)},
			msg: `template: blocks/tableCell.html:1:18: executing "tableCell.html" at <.Class>: ` +
				"can't evaluate field Class in type render.TableCell",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRenderer(t, testTheme(t, map[string]string{tt.file: tt.text}), "ja", "", nil)
			html, css, warnings, err := r.Document(tt.lists...)
			if err == nil || err.Error() != tt.msg || html != nil || css.rules != nil || warnings != nil {
				t.Errorf("Document() = %q, %q, %q, %v, want the error %s", html, css, warnings, err, tt.msg)
			}
		})
	}
}

func TestDocumentEmpty(t *testing.T) {
	r := newRenderer(t, testTheme(t, nil), "ja", "", nil)
	for _, lists := range [][]content.Blocks{nil, {nil}, {{}, nil, {}}} {
		html, css, warnings, err := r.Document(lists...)
		empty := !slices.ContainsFunc(html, func(h template.HTML) bool { return h != "" })
		if err != nil || css.rules != nil || warnings != nil || len(html) != len(lists) || !empty {
			t.Errorf("Document() of %d empty lists = %q, %q, %q, %v, want as many empty HTML",
				len(lists), html, css, warnings, err)
		}
	}
	// A zero Theme has no block templates, no variants and no presets, and allows every style item and value.
	r = newRenderer(t, &theme.Theme{}, "ja", "", nil)
	checkDocument(t, r, []content.Blocks{decodeBlocks(t, `[{
			"type": "paragraph", "id": "b_0paragr1", "variant": "lead",
			"style": {"textColor": {"preset": "primary"}, "letterSpacing": {"value": "1px"}}
		}]`)},
		[]string{`<p class="b-0paragr1"></p>`}, ".b-0paragr1 {\n  letter-spacing: 1px !important;\n}\n", []Warning{
			{Path: "/0/variant", Msg: `the theme has no paragraph variant "lead"; rendered without its class`},
			{Path: "/0/style/textColor/preset", Msg: `the theme has no color preset "primary"; ignored`},
		})
}

// TestDocumentParallel checks that a Renderer renders documents in parallel, each with its own anchors, CSS, block IDs
// with CSS and warnings.
func TestDocumentParallel(t *testing.T) {
	th := testTheme(t, map[string]string{
		"theme.yaml":            blocksTheme,
		"blocks/paragraph.html": `<p class="theme{{with .Class}} {{.}}{{end}}">{{.Content}}</p>` + "\n",
	})
	r := newRenderer(t, th, "ja", "/blog", linkResolver)
	lists := []content.Blocks{
		decodeBlocks(t, `[
			`+headingJSON("b_00head01", "一")+`,
			{
				"type": "paragraph", "id": "b_0paragr1", "variant": "lead",
				"style": {"textColor": {"preset": "primary"}},
				"content": [{"type": "text", "text": "会社概要", "marks": [{"type": "link", "attrs": {"entry": "`+aboutEntry+`"}}]}]
			},
			{"type": "group", "id": "b_000group", "variant": "box", "content": [`+headingJSON("b_00head01", "二")+`]}
		]`),
		decodeBlocks(t, `[
			`+headingJSON("b_00head01", "三")+`,
			{
				"type": "paragraph", "id": "b_0paragr2",
				"content": [{"type": "text", "text": "下書き", "marks": [
					{"type": "link", "attrs": {"entry": "`+draftEntry+`"}},
					{"type": "highlight", "attrs": {"backgroundColor": {"value": "#fff4e5"}}}
				]}]
			},
			{"type": "separator", "id": "b_0paragr1", "style": {"margin": {"top": {"value": "1rem"}}}},
			{"type": "legacy", "id": "b_00legacy", "attrs": {"html": "<p>x</p>"}}
		]`),
	}
	want := []string{
		`<h2 id="b_00head01">一</h2>
<p class="theme b-0paragr1 is-variant-lead"><a href="/blog/about/">会社概要</a></p>
<div class="is-variant-box">
<h2 id="b_00head01-2">二</h2>
</div>`,
		"<h2 id=\"b_00head01-3\">三</h2>\n<p class=\"theme\"><mark class=\"hl-a70e621a\">下書き</mark></p>\n<hr>",
	}
	const css = ".b-0paragr1 {\n  color: var(--color-primary) !important;\n}\n\n" +
		".hl-a70e621a {\n  background-color: #fff4e5 !important;\n}\n"
	warnings := []Warning{
		{List: 1, Path: "/1/content/0/marks/0", Msg: `entry "` + draftEntry + `" is not published; rendered without the link`},
		{List: 1, Path: "/2/id", Msg: `duplicate block ID "b_0paragr1"; rendered without its CSS`},
		{List: 1, Path: "/3", Msg: "legacy blocks are not rendered yet"},
	}
	checkDocument(t, r, lists, want, css, warnings)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 4 {
				html, gotCSS, got, err := r.Document(lists...)
				if err != nil || len(html) != 2 || string(html[0]) != want[0] || string(html[1]) != want[1] ||
					gotCSS.String() != css || !slices.Equal(got, warnings) {
					t.Errorf("Document() in parallel = %q, %q, %q, %v", html, gotCSS, got, err)
				}
			}
		})
	}
	wg.Wait()
}
