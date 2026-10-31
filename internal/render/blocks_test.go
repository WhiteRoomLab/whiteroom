package render

import (
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// blocksTheme is the settings file of a theme whose paragraphs have the variant lead and whose groups have box.
const blocksTheme = "schemaVersion: 1\nblocks:\n  paragraph:\n    variants:\n      lead: リード\n" +
	"  group:\n    variants:\n      box: ボックス\n"

// decodeBlocks returns the blocks of s, a JSON array.
func decodeBlocks(t *testing.T, s string) content.Blocks {
	t.Helper()
	var bs content.Blocks
	if err := bs.UnmarshalJSON([]byte(s)); err != nil {
		t.Fatalf("UnmarshalJSON() error: %v\n%s", err, s)
	}
	return bs
}

// paragraphJSON returns the JSON of a paragraph with the ID id and the text s.
func paragraphJSON(id, s string) string {
	return `{"type": "paragraph", "id": "` + id + `", "content": [{"type": "text", "text": "` + s + `"}]}`
}

// TestBlocks checks the HTML and the warnings of lists of blocks with the default templates, both from the core and
// copied to the blocks/ of a theme.
func TestBlocks(t *testing.T) {
	const (
		unpublished = `entry "` + draftEntry + `" is not published; rendered without the link`
		draftLink   = `{"type": "link", "attrs": {"entry": "` + draftEntry + `"}}`
		missingLink = `{"type": "link", "attrs": {"entry": "` + missingEntry + `"}}`
		mediaLink   = `{"type": "link", "attrs": {"media": "` + missingMedia + `"}}`
	)
	tests := []struct {
		name     string
		json     string         // the blocks, unless in has them
		in       content.Blocks // blocks that JSON cannot hold
		want     string
		warnings []string
	}{
		{
			name: "paragraph",
			json: `[{
				"type": "paragraph", "id": "b_0paragr1", "variant": "lead", "className": "intro", "anchor": "intro",
				"style": {"textColor": {"preset": "primary"}}, "attrs": {"textAlign": "center"},
				"content": [
					{"type": "text", "text": "当社の"}, {"type": "text", "text": "事業", "marks": [{"type": "bold"}]},
					{"type": "text", "text": "をご紹介します。"}
				]
			}]`,
			want: `<p id="intro" class="b-0paragr1 is-variant-lead intro">当社の<strong>事業</strong>をご紹介します。</p>`,
		},
		{name: "empty paragraph", json: `[{"type": "paragraph", "id": "b_0paragr2"}]`, want: "<p></p>"},
		{
			name: "classes without a style or without a valid ID",
			json: `[
				{"type": "paragraph", "id": "b_0paragr3", "variant": "lead", "content": [{"type": "text", "text": "a"}]},
				{
					"type": "paragraph", "id": "b_x", "className": "note", "style": {"textColor": {"preset": "primary"}},
					"content": [{"type": "text", "text": "b"}]
				}
			]`,
			want: "<p class=\"is-variant-lead\">a</p>\n<p class=\"note\">b</p>",
		},
		{
			name: "escaping",
			json: `[
				{"type": "separator", "id": "b_separat3", "anchor": "a\"b<c", "className": "x'y&z"},
				{"type": "group", "id": "b_00group1", "attrs": {"label": "<b>注</b>"}}
			]`,
			want: "<hr id=\"a&#34;b&lt;c\" class=\"x&#39;y&amp;z\">\n<div><header>&lt;b&gt;注&lt;/b&gt;</header></div>",
		},
		{
			name: "headings",
			json: `[
				{
					"type": "heading", "id": "b_0heading", "anchor": "services", "attrs": {"level": 3, "textAlign": "right"},
					"content": [{"type": "text", "text": "Services"}]
				},
				{
					"type": "heading", "id": "b_heading2", "className": "title", "attrs": {"level": 2},
					"content": [{"type": "text", "text": "会社"}, {"type": "text", "text": "概要", "marks": [{"type": "italic"}]}]
				},
				{"type": "heading", "id": "b_heading6", "attrs": {"level": 6}, "content": [{"type": "text", "text": "注記"}]}
			]`,
			want: `<h3 id="services">Services</h3>
<h2 id="b_heading2" class="title">会社<em>概要</em></h2>
<h6 id="b_heading6">注記</h6>`,
		},
		{
			name: "heading levels out of range",
			in: content.Blocks{
				{Type: content.BlockHeading, ID: "b_heading1", Attrs: content.Attrs{Level: 1}, Text: content.RichText{textNode("一")}},
				{Type: content.BlockHeading, ID: "b_heading9", Attrs: content.Attrs{Level: 9}, Text: content.RichText{textNode("九")}},
				{Type: content.BlockHeading, ID: "b_heading0", Text: content.RichText{textNode("零")}},
			},
			want: "<h2 id=\"b_heading1\">一</h2>\n<h6 id=\"b_heading9\">九</h6>\n<h2 id=\"b_heading0\">零</h2>",
		},
		{
			name: "ordered list with start and a nested list",
			json: `[{
				"type": "list", "id": "b_00list01", "anchor": "steps", "className": "steps", "attrs": {"ordered": true, "start": 3},
				"content": [
					{
						"type": "listItem",
						"content": [
							` + paragraphJSON("b_0litem01", "First") + `,
							{
								"type": "list", "id": "b_00list02",
								"content": [{"type": "listItem", "content": [` + paragraphJSON("b_0litem02", "Nested") + `]}]
							}
						]
					},
					{"type": "listItem", "content": [` + paragraphJSON("b_0litem03", "Second") + `]}
				]
			}]`,
			want: `<ol id="steps" class="steps" start="3">
<li><p>First</p>
<ul>
<li><p>Nested</p></li>
</ul></li>
<li><p>Second</p></li>
</ol>`,
		},
		{
			name: "ordered list without start",
			json: `[{"type": "list", "id": "b_00list03", "attrs": {"ordered": true}, "content": [{"type": "listItem", "content": [` +
				paragraphJSON("b_0litem04", "x") + `]}]}]`,
			want: "<ol>\n<li><p>x</p></li>\n</ol>",
		},
		{
			name: "start without ordered",
			json: `[{"type": "list", "id": "b_00list04", "attrs": {"start": 5}, "content": [{"type": "listItem", "content": [` +
				paragraphJSON("b_0litem05", "x") + `]}]}]`,
			want: "<ul>\n<li><p>x</p></li>\n</ul>",
		},
		{
			name: "parts without anchor, class or variant",
			in: content.Blocks{{Type: content.BlockList, ID: "b_00list06", Children: content.Blocks{{
				Type: content.BlockListItem, ID: "b_0litem08", Variant: "lead", ClassName: "item", Anchor: "item",
				Style:    content.Style{TextColor: content.Color{Preset: "primary"}},
				Children: content.Blocks{{Type: content.BlockParagraph, ID: "b_0litem09", Text: content.RichText{textNode("x")}}},
			}}}},
			want: "<ul>\n<li><p>x</p></li>\n</ul>",
		},
		{
			name: "quote with citation",
			json: `[{
				"type": "quote", "id": "b_000quote", "anchor": "voice",
				"attrs": {"citation": [{"type": "text", "text": "A customer", "marks": [{"type": "italic"}]}]},
				"content": [` + paragraphJSON("b_0quotep1", "Great service.") + `, ` + paragraphJSON("b_0quotep2", "Again.") + `]
			}]`,
			want: `<figure id="voice"><blockquote>
<p>Great service.</p>
<p>Again.</p>
</blockquote><figcaption><em>A customer</em></figcaption></figure>`,
		},
		{
			name: "quote without citation",
			json: `[{"type": "quote", "id": "b_000quot2", "className": "pull", "content": [` +
				paragraphJSON("b_0quotep3", "Short.") + `]}]`,
			want: "<blockquote class=\"pull\">\n<p>Short.</p>\n</blockquote>",
		},
		{
			name: "table",
			json: `[{
				"type": "table", "id": "b_000table", "anchor": "profile",
				"attrs": {"caption": [{"type": "text", "text": "Company profile"}]},
				"content": [
					{
						"type": "tableRow", "attrs": {"section": "head"},
						"content": [
							{"type": "tableCell", "attrs": {"header": true}, "content": [{"type": "text", "text": "Item"}]},
							{"type": "tableCell", "attrs": {"header": true}, "content": [{"type": "text", "text": "Value"}]}
						]
					},
					{
						"type": "tableRow",
						"content": [
							{
								"type": "tableCell", "attrs": {"header": true, "rowspan": 2},
								"content": [{"type": "text", "text": "Address"}]
							},
							{"type": "tableCell", "attrs": {"textAlign": "left"}, "content": [{"type": "text", "text": "Tokyo"}]}
						]
					},
					{"type": "tableRow", "attrs": {"section": "body"}, "content": [{"type": "tableCell"}]},
					{
						"type": "tableRow", "attrs": {"section": "foot"},
						"content": [{
							"type": "tableCell", "attrs": {"colspan": 2, "rowspan": 1},
							"content": [{"type": "text", "text": "As of 2026"}]
						}]
					}
				]
			}]`,
			want: `<figure id="profile"><table><thead>
<tr>
<th>Item</th>
<th>Value</th>
</tr>
</thead><tbody>
<tr>
<th rowspan="2">Address</th>
<td>Tokyo</td>
</tr>
<tr>
<td></td>
</tr>
</tbody><tfoot>
<tr>
<td colspan="2" rowspan="1">As of 2026</td>
</tr>
</tfoot></table><figcaption>Company profile</figcaption></figure>`,
		},
		{
			name: "table without head",
			json: `[{
				"type": "table", "id": "b_00table2", "className": "plain",
				"content": [{
					"type": "tableRow",
					"content": [
						{"type": "tableCell", "content": [{"type": "text", "text": "a"}]},
						{"type": "tableCell", "content": [{"type": "text", "text": "b", "marks": [{"type": "bold"}]}]}
					]
				}]
			}]`,
			want: "<figure class=\"plain\"><table><tbody>\n<tr>\n<td>a</td>\n<td><strong>b</strong></td>\n</tr>\n</tbody></table></figure>",
		},
		{
			name: "rows out of order and other blocks in a table",
			json: `[{
				"type": "table", "id": "b_00table3",
				"content": [
					{
						"type": "tableRow", "attrs": {"section": "foot"},
						"content": [{"type": "tableCell", "content": [{"type": "text", "text": "f1"}]}]
					},
					{"type": "tableRow", "content": [{"type": "tableCell", "content": [{"type": "text", "text": "b1"}]}]},
					{
						"type": "tableRow", "attrs": {"section": "head"},
						"content": [{"type": "tableCell", "attrs": {"header": true}, "content": [{"type": "text", "text": "h1"}]}]
					},
					` + paragraphJSON("b_0tablep1", "p") + `,
					{
						"type": "tableRow", "attrs": {"section": "foot"},
						"content": [{"type": "tableCell", "content": [{"type": "text", "text": "f2"}]}]
					},
					{
						"type": "tableRow", "attrs": {"section": "side"},
						"content": [{"type": "tableCell", "content": [{"type": "text", "text": "b2"}]}]
					},
					{"type": "html", "id": "b_0tableh1", "attrs": {"html": "<tr></tr>"}}
				]
			}]`,
			want: `<figure><table><thead>
<tr>
<th>h1</th>
</tr>
</thead><tbody>
<tr>
<td>b1</td>
</tr>
<p>p</p>
<tr>
<td>b2</td>
</tr>
</tbody><tfoot>
<tr>
<td>f1</td>
</tr>
<tr>
<td>f2</td>
</tr>
</tfoot></table></figure>`,
			warnings: []string{"/0/content/6: html blocks are not rendered yet"},
		},
		{
			name: "a block in a table that is not a row, with a section",
			in: content.Blocks{{Type: content.BlockTable, ID: "b_00table5", Children: content.Blocks{
				{
					Type: content.BlockParagraph, ID: "b_0tablep2", Attrs: content.Attrs{Section: "head"},
					Text: content.RichText{textNode("p")},
				},
				{Type: content.BlockTableRow, Attrs: content.Attrs{Section: "head"}, Children: content.Blocks{
					{Type: content.BlockTableCell, Text: content.RichText{textNode("h")}},
				}},
			}}},
			want: "<figure><table><thead>\n<tr>\n<td>h</td>\n</tr>\n</thead><tbody>\n<p>p</p>\n</tbody></table></figure>",
		},
		{
			name: "buttons",
			json: `[{
				"type": "buttons", "id": "b_0buttons", "attrs": {"justify": "center"},
				"content": [
					{
						"type": "button", "id": "b_0button1", "anchor": "about", "className": "primary",
						"attrs": {"label": "会社概要", "link": {"entry": "` + aboutEntry + `", "anchor": "access"}, "width": "50%"}
					},
					{
						"type": "button", "id": "b_0button2", "style": {"backgroundColor": {"value": "#1e73be"}},
						"attrs": {
							"label": "Example & Co",
							"link": {"href": "https://example.com/?a=1&b=2", "newTab": true, "rel": ["nofollow"]}
						}
					},
					{"type": "button", "id": "b_0button3", "attrs": {"label": "リンクなし"}},
					{"type": "button", "id": "b_0button4", "attrs": {"label": "下書き", "link": {"entry": "` + draftEntry + `"}}}
				]
			}]`,
			want: `<div>
<a id="about" class="primary" href="/about/#access">会社概要</a>
<a class="b-0button2" href="https://example.com/?a=1&amp;b=2" target="_blank" rel="noopener nofollow">Example &amp; Co</a>
<a>リンクなし</a>
<a>下書き</a>
</div>`,
			warnings: []string{"/0/content/3/attrs/link: " + unpublished},
		},
		{
			name: "separators",
			json: `[
				{"type": "separator", "id": "b_separat1", "anchor": "line", "className": "thin"},
				{"type": "separator", "id": "b_separat2"}
			]`,
			want: "<hr id=\"line\" class=\"thin\">\n<hr>",
		},
		{
			name: "spacers",
			json: `[
				{"type": "spacer", "id": "b_00spacer", "attrs": {"height": {"preset": "large"}}},
				{
					"type": "spacer", "id": "b_0spacer2", "style": {"margin": {"top": {"value": "1rem"}}},
					"attrs": {"height": {"value": "40px"}}
				}
			]`,
			want: "<div aria-hidden=\"true\"></div>\n<div class=\"b-0spacer2\" aria-hidden=\"true\"></div>",
		},
		{
			name: "code",
			json: `[{
				"type": "code", "id": "b_0000code", "anchor": "sample", "className": "wide", "attrs": {"language": "go"},
				"content": "if a < b && c > d {\n\tfmt.Println(\"<p>\", 'x')\n}\n\n"
			}]`,
			want: "<pre id=\"sample\" class=\"wide\"><code class=\"language-go\">if a &lt; b &amp;&amp; c &gt; d {\n" +
				"\tfmt.Println(&#34;&lt;p&gt;&#34;, &#39;x&#39;)\n}\n\n</code></pre>",
		},
		{name: "empty code", json: `[{"type": "code", "id": "b_000code2"}]`, want: "<pre><code></code></pre>"},
		{
			name: "columns with an empty column",
			json: `[{
				"type": "columns", "id": "b_0columns", "anchor": "cols", "attrs": {"verticalAlign": "top", "stackOnMobile": false},
				"content": [
					{
						"type": "column", "id": "b_0column1", "style": {"backgroundColor": {"preset": "muted"}},
						"attrs": {"width": "33.33%", "verticalAlign": "stretch"},
						"content": [` + paragraphJSON("b_0colpar1", "Left") + `]
					},
					{"type": "column", "id": "b_0column2"}
				]
			}]`,
			want: `<div id="cols">
<div class="b-0column1">
<p>Left</p>
</div>
<div></div>
</div>`,
		},
		{
			name: "group section with label",
			json: `[{
				"type": "group", "id": "b_000group", "variant": "box", "anchor": "notice",
				"attrs": {"layout": {"type": "row", "justify": "spaceBetween"}, "tagName": "section", "label": "お知らせ", "align": "full"},
				"content": [` + paragraphJSON("b_0grppar1", "日曜は休業です。") + `]
			}]`,
			want: "<section id=\"notice\" class=\"is-variant-box\"><header>お知らせ</header>\n<p>日曜は休業です。</p>\n</section>",
		},
		{
			name: "groups",
			json: `[
				{"type": "group", "id": "b_00group2", "attrs": {"tagName": "div"}, "content": [` + paragraphJSON("b_0grppar2", "a") + `]},
				{
					"type": "group", "id": "b_00group3", "attrs": {"tagName": "article"},
					"content": [` + paragraphJSON("b_0grppar3", "b") + `]
				},
				{
					"type": "group", "id": "b_00group4", "attrs": {"tagName": "aside", "label": "注"},
					"content": [` + paragraphJSON("b_0grppar4", "c") + `, ` + paragraphJSON("b_0grppar5", "d") + `]
				},
				{"type": "group", "id": "b_00group5", "content": [` + paragraphJSON("b_0grppar6", "e") + `]}
			]`,
			want: `<div>
<p>a</p>
</div>
<article>
<p>b</p>
</article>
<aside><header>注</header>
<p>c</p>
<p>d</p>
</aside>
<div>
<p>e</p>
</div>`,
		},
		{
			name: "unknown tagName",
			json: `[
				{"type": "group", "id": "b_00group6", "attrs": {"tagName": "main"}, "content": [` + paragraphJSON("b_0grppar7", "a") + `]},
				{"type": "group", "id": "b_00group7", "attrs": {"tagName": "Section"}},
				{"type": "group", "id": "b_00group8", "attrs": {"tagName": "<script>"}}
			]`,
			want: "<div>\n<p>a</p>\n</div>\n<div></div>\n<div></div>",
		},
		{
			name: "empty groups",
			json: `[{"type": "group", "id": "b_00group9"}, {"type": "group", "id": "b_00groupa", "attrs": {"label": "空"}}]`,
			want: "<div></div>\n<div><header>空</header></div>",
		},
		{
			name: "unknown variants",
			json: `[
				{"type": "paragraph", "id": "b_0paragr4", "variant": "box", "className": "x", "content": [{"type": "text", "text": "a"}]},
				{"type": "group", "id": "b_00groupb", "variant": "lead"},
				{"type": "separator", "id": "b_separat4", "variant": "wide"}
			]`,
			want: "<p class=\"x\">a</p>\n<div></div>\n<hr>",
			warnings: []string{
				`/0/variant: the theme has no paragraph variant "box"; rendered without its class`,
				`/1/variant: the theme has no group variant "lead"; rendered without its class`,
				`/2/variant: the theme has no separator variant "wide"; rendered without its class`,
			},
		},
		{
			name: "html and legacy between paragraphs",
			json: `[
				` + paragraphJSON("b_0paragr5", "a") + `,
				{"type": "html", "id": "b_0000html", "attrs": {"html": "<div class=\"note\">\n  <p>Custom</p>\n</div>"}},
				{"type": "legacy", "id": "b_00legacy", "variant": "old", "attrs": {"html": "[gallery ids=\"1,2\"]", "reason": "shortcode"}},
				` + paragraphJSON("b_0paragr6", "b") + `
			]`,
			want:     "<p>a</p>\n<p>b</p>",
			warnings: []string{"/1: html blocks are not rendered yet", "/2: legacy blocks are not rendered yet"},
		},
		{
			name: "blocks not rendered yet",
			json: `[
				{"type": "image", "id": "b_000image", "variant": "x", "attrs": {"media": "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5", "alt": "a"}},
				{"type": "file", "id": "b_0000file", "attrs": {"media": "` + guideMedia + `"}},
				{"type": "embed", "id": "b_000embed", "attrs": {"provider": "youtube", "url": "https://youtu.be/dQw4w9WgXcQ"}},
				{"type": "form", "id": "b_0000form", "attrs": {"form": "frm_01JB8Z3K5VP1D4G7J2M5Q8T3W6"}}
			]`,
			warnings: []string{
				"/0: image blocks are not rendered yet", "/1: file blocks are not rendered yet",
				"/2: embed blocks are not rendered yet", "/3: form blocks are not rendered yet",
			},
		},
		{
			name: "unknown types",
			in: content.Blocks{
				{
					Type: "gallery", ID: "b_gallery1", Variant: "x",
					Children: content.Blocks{{Type: content.BlockParagraph, ID: "b_0paragr7"}},
				},
				{Type: content.BlockGroup, ID: "b_00groupc", Children: content.Blocks{{Type: "Paragraph", ID: "b_0paragr8"}}},
			},
			want: "<div></div>",
			warnings: []string{
				`/0: unknown block type "gallery"; not rendered`, `/1/content/0: unknown block type "Paragraph"; not rendered`,
			},
		},
		{
			name: "warnings in document order",
			json: `[
				{
					"type": "group", "id": "b_00groupd", "variant": "card",
					"content": [
						{
							"type": "paragraph", "id": "b_0paragr9", "variant": "note",
							"content": [{"type": "text", "text": "a", "marks": [` + draftLink + `]}]
						},
						{
							"type": "quote", "id": "b_000quot3", "variant": "pull",
							"attrs": {"citation": [{"type": "text", "text": "c", "marks": [` + missingLink + `]}]},
							"content": [{
								"type": "paragraph", "id": "b_0quotep4",
								"content": [{"type": "text", "text": "q", "marks": [` + mediaLink + `]}]
							}]
						}
					]
				},
				{
					"type": "table", "id": "b_00table4", "variant": "striped",
					"attrs": {
						"caption": [{"type": "text", "text": "t", "marks": [{"type": "link", "attrs": {"href": "javascript:alert(1)"}}]}]
					},
					"content": [{
						"type": "tableRow",
						"content": [{"type": "tableCell", "content": [{"type": "text", "text": "x", "marks": [` + draftLink + `]}]}]
					}]
				},
				{"type": "html", "id": "b_000html2", "attrs": {"html": "<p>x</p>"}},
				{
					"type": "buttons", "id": "b_buttons2", "variant": "row",
					"content": [{
						"type": "button", "id": "b_0button5", "variant": "big",
						"attrs": {"label": "x", "link": {"entry": "` + draftEntry + `"}}
					}]
				},
				{
					"type": "list", "id": "b_00list05", "variant": "check",
					"content": [{
						"type": "listItem",
						"content": [{
							"type": "paragraph", "id": "b_0litem07", "variant": "small",
							"content": [{"type": "text", "text": "y", "marks": [{"type": "link", "attrs": {"newTab": true}}]}]
						}]
					}]
				},
				{
					"type": "heading", "id": "b_heading3", "variant": "big", "anchor": "h", "attrs": {"level": 2},
					"content": [{"type": "text", "text": "z", "marks": [` + draftLink + `]}]
				}
			]`,
			want: `<div>
<p>a</p>
<figure><blockquote>
<p>q</p>
</blockquote><figcaption>c</figcaption></figure>
</div>
<figure><table><tbody>
<tr>
<td>x</td>
</tr>
</tbody></table><figcaption>t</figcaption></figure>
<div>
<a>x</a>
</div>
<ul>
<li><p>y</p></li>
</ul>
<h2 id="h">z</h2>`,
			warnings: []string{
				`/0/variant: the theme has no group variant "card"; rendered without its class`,
				`/0/content/0/variant: the theme has no paragraph variant "note"; rendered without its class`,
				"/0/content/0/content/0/marks/0: " + unpublished,
				`/0/content/1/variant: the theme has no quote variant "pull"; rendered without its class`,
				`/0/content/1/attrs/citation/0/marks/0: entry "` + missingEntry + `" does not exist; rendered without the link`,
				`/0/content/1/content/0/content/0/marks/0: media "` + missingMedia + `" does not exist; rendered without the link`,
				`/1/variant: the theme has no table variant "striped"; rendered without its class`,
				`/1/attrs/caption/0/marks/0: invalid href "javascript:alert(1)": scheme "javascript" is not allowed; ` +
					"rendered without the link",
				"/1/content/0/content/0/content/0/marks/0: " + unpublished,
				"/2: html blocks are not rendered yet",
				`/3/variant: the theme has no buttons variant "row"; rendered without its class`,
				`/3/content/0/variant: the theme has no button variant "big"; rendered without its class`,
				"/3/content/0/attrs/link: " + unpublished,
				`/4/variant: the theme has no list variant "check"; rendered without its class`,
				`/4/content/0/content/0/variant: the theme has no paragraph variant "small"; rendered without its class`,
				"/4/content/0/content/0/content/0/marks/0: link without a target; rendered without the link",
				`/5/variant: the theme has no heading variant "big"; rendered without its class`,
				"/5/content/0/marks/0: " + unpublished,
			},
		},
	}
	files := defaultBlockFiles(t)
	files["theme.yaml"] = blocksTheme
	renderers := []struct {
		name string
		r    *Renderer
	}{
		{"default templates", newRenderer(t, testTheme(t, map[string]string{"theme.yaml": blocksTheme}), "ja", "", linkResolver)},
		{"default templates as the theme's", newRenderer(t, testTheme(t, files), "ja", "", linkResolver)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bs := tt.in
			if tt.json != "" {
				bs = decodeBlocks(t, tt.json)
			}
			for _, x := range renderers {
				html, warnings, err := x.r.Document(bs)
				if err != nil || len(html) != 1 {
					t.Fatalf("%s: Document() = %q, %v, want one list", x.name, html, err)
				}
				if string(html[0]) != tt.want {
					t.Errorf("%s: Document() =\n%s\nwant\n%s", x.name, html[0], tt.want)
				}
				if got := warningStrings(warnings); !slices.Equal(got, tt.warnings) {
					t.Errorf("%s: warnings:\n%s\nwant:\n%s", x.name, strings.Join(got, "\n"), strings.Join(tt.warnings, "\n"))
				}
				for _, w := range warnings {
					if w.List != 0 {
						t.Errorf("%s: warning %q has List %d, want 0", x.name, w, w.List)
					}
				}
			}
		})
	}
}
