package render

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// styleTheme is the settings file of a theme with presets of the kinds that styles name, which allows every style item
// and direct values, and whose highlight has the variant marker.
const styleTheme = `schemaVersion: 1
presets:
  color:
    primary: "#1e73be"
    accent: "#e85d04"
  fontSize:
    large: clamp(1.25rem, 1rem + 1vw, 1.5rem)
  spacing:
    "30": 1rem
    "40": 2rem
  fontFamily:
    body: '"Noto Sans JP", sans-serif'
  gradient:
    sunset: "linear-gradient(135deg, #f093fb 0%, #f5576c 100%)"
marks:
  highlight:
    variants:
      marker: マーカー
`

// flowCSS returns the rules of the margins between the children of a flow container whose children are those of sel.
func flowCSS(sel, gap string) string {
	return sel + " > * {\n  margin-top: 0 !important;\n  margin-bottom: 0 !important;\n}\n\n" +
		sel + " > * + * {\n  margin-top: " + gap + " !important;\n}\n"
}

// TestStyle checks the CSS of the styles of blocks and their classes b-<ID>.
func TestStyle(t *testing.T) {
	tests := []blockTest{
		{
			name: "every item, in the order of the items",
			json: `[{
				"type": "group", "id": "b_00group1",
				"style": {
					"border": {
						"radius": {"value": "50%"}, "color": {"preset": "accent"}, "style": {"value": "dashed"},
						"width": {"value": "1px"}
					},
					"blockGap": {"preset": "30"},
					"margin": {"left": {"value": "10%"}, "bottom": {"preset": "40"}, "right": {"value": "2vw"}, "top": {"value": "-1rem"}},
					"padding": {"top": {"preset": "30"}, "right": {"value": "1rem"}, "bottom": {"value": "0"}, "left": {"value": "5%"}},
					"lineHeight": {"value": 1.5},
					"fontWeight": {"value": 700},
					"fontFamily": {"preset": "body"},
					"letterSpacing": {"value": "-0.02em"},
					"fontSize": {"preset": "large"},
					"gradient": {"preset": "sunset"},
					"linkColor": {"value": "rgb(30 115 190 / 50%)"},
					"backgroundColor": {"value": "#fff4e5"},
					"textColor": {"preset": "primary"}
				},
				"content": [` + paragraphJSON("b_0grppar1", "a") + `]
			}]`,
			want: "<div class=\"b-00group1\">\n<p>a</p>\n</div>",
			css: `.b-00group1 {
  color: var(--color-primary) !important;
  background-color: #fff4e5 !important;
  background-image: var(--gradient-sunset) !important;
  font-size: var(--font-size-large) !important;
  letter-spacing: -0.02em !important;
  font-family: var(--font-family-body) !important;
  font-weight: 700 !important;
  line-height: 1.5 !important;
  padding-top: var(--spacing-30) !important;
  padding-right: 1rem !important;
  padding-bottom: 0 !important;
  padding-left: 5% !important;
  margin-top: -1rem !important;
  margin-right: 2vw !important;
  margin-bottom: var(--spacing-40) !important;
  margin-left: 10% !important;
  border-width: 1px !important;
  border-style: dashed !important;
  border-color: var(--color-accent) !important;
  border-radius: 50% !important;
}

.b-00group1 :where(a) {
  color: rgb(30 115 190 / 50%) !important;
}

` + flowCSS(".b-00group1", "var(--spacing-30)"),
		},
		{
			name: "the rules of a block, in their order",
			json: `[{
				"type": "group", "id": "b_00group2",
				"style": {"linkColor": {"preset": "accent"}, "blockGap": {"value": "1.5rem"}, "border": {"width": {"value": "2px"}}},
				"content": [` + paragraphJSON("b_0grppar2", "a") + `]
			}]`,
			want: "<div class=\"b-00group2\">\n<p>a</p>\n</div>",
			css: ".b-00group2 {\n  border-width: 2px !important;\n}\n\n" +
				":where(.b-00group2) {\n  border-style: solid;\n}\n\n" +
				".b-00group2 :where(a) {\n  color: var(--color-accent) !important;\n}\n\n" +
				flowCSS(".b-00group2", "1.5rem"),
		},
		{
			name: "only the color of links",
			json: `[{"type": "paragraph", "id": "b_0paragr1", "style": {"linkColor": {"value": "#c00"}}}]`,
			want: `<p class="b-0paragr1"></p>`,
			css:  ".b-0paragr1 :where(a) {\n  color: #c00 !important;\n}\n",
		},
		{
			name: "blockGap as the gap of flex and grid containers",
			json: `[
				{
					"type": "buttons", "id": "b_buttons1",
					"style": {"border": {"radius": {"value": "4px"}}, "blockGap": {"value": "1rem"}, "margin": {"top": {"value": "0"}}},
					"content": [{"type": "button", "id": "b_0button1", "attrs": {"label": "a"}}]
				},
				{
					"type": "columns", "id": "b_columns1", "style": {"blockGap": {"preset": "40"}},
					"content": [{"type": "column", "id": "b_0column1"}]
				},
				{"type": "group", "id": "b_00group1", "attrs": {"layout": {"type": "row"}}, "style": {"blockGap": {"value": "2%"}}},
				{"type": "group", "id": "b_00group2", "attrs": {"layout": {"type": "stack"}}, "style": {"blockGap": {"value": "0"}}},
				{"type": "group", "id": "b_00group3", "attrs": {"layout": {"type": "grid"}}, "style": {"blockGap": {"value": "1.5em"}}}
			]`,
			want: `<div class="b-buttons1 is-layout-row">
<a>a</a>
</div>
<div class="b-columns1 is-layout-columns">
<div></div>
</div>
<div class="b-00group1 is-layout-row"></div>
<div class="b-00group2 is-layout-stack"></div>
<div class="b-00group3 is-layout-grid"></div>`,
			css: ".b-buttons1 {\n  margin-top: 0 !important;\n  gap: 1rem !important;\n  border-radius: 4px !important;\n}\n\n" +
				".b-columns1 {\n  gap: var(--spacing-40) !important;\n}\n\n" +
				".b-00group1 {\n  gap: 2% !important;\n}\n\n" +
				".b-00group2 {\n  gap: 0 !important;\n}\n\n" +
				".b-00group3 {\n  gap: 1.5em !important;\n}\n",
		},
		{
			name: "blockGap as the margins between the children of flow containers",
			json: `[
				{
					"type": "list", "id": "b_00list01", "style": {"blockGap": {"value": "0.5rem"}},
					"content": [{"type": "listItem", "content": [` + paragraphJSON("b_0litem01", "a") + `]}]
				},
				{
					"type": "columns", "id": "b_columns1",
					"content": [{"type": "column", "id": "b_0column1", "style": {"blockGap": {"preset": "30"}}}]
				},
				{"type": "group", "id": "b_00group1", "style": {"blockGap": {"value": "1rem"}}},
				{"type": "group", "id": "b_00group2", "attrs": {"layout": {"type": "flow"}}, "style": {"blockGap": {"value": "2rem"}}},
				{"type": "group", "id": "b_00group3", "attrs": {"layout": {"type": "Row"}}, "style": {"blockGap": {"value": "3rem"}}},
				{"type": "group", "id": "b_00group4", "attrs": {"layout": {"columns": 2}}, "style": {"blockGap": {"value": "4rem"}}}
			]`,
			want: `<ul class="b-00list01">
<li><p>a</p></li>
</ul>
<div class="is-layout-columns">
<div class="b-0column1"></div>
</div>
<div class="b-00group1"></div>
<div class="b-00group2 is-layout-flow"></div>
<div class="b-00group3"></div>
<div class="b-00group4"></div>`,
			css: flowCSS(".b-00list01", "0.5rem") + "\n" + flowCSS(".b-0column1", "var(--spacing-30)") + "\n" +
				flowCSS(".b-00group1", "1rem") + "\n" + flowCSS(".b-00group2", "2rem") + "\n" + flowCSS(".b-00group3", "3rem") +
				"\n" + flowCSS(".b-00group4", "4rem"),
			warnings: []string{
				`/4/attrs/layout/type: unknown layout type "Row"; ignored`, "/5/attrs/layout/type: missing layout type; ignored",
			},
		},
		{
			name: "blockGap of quotes with and without a citation",
			json: `[
				{
					"type": "quote", "id": "b_000quote", "style": {"blockGap": {"value": "1rem"}},
					"attrs": {"citation": [{"type": "text", "text": "c"}]}, "content": [` + paragraphJSON("b_0quotep1", "a") + `]
				},
				{"type": "quote", "id": "b_000quot2", "style": {"blockGap": {"value": "2rem"}}, "content": [` +
				paragraphJSON("b_0quotep2", "b") + `]}
			]`,
			want: `<figure class="b-000quote"><blockquote>
<p>a</p>
</blockquote><figcaption>c</figcaption></figure>
<blockquote class="b-000quot2">
<p>b</p>
</blockquote>`,
			css: flowCSS(".b-000quote > blockquote", "1rem") + "\n" + flowCSS(".b-000quot2", "2rem"),
		},
		{
			name: "blockGap of a quote whose citation renders nothing",
			in: content.Blocks{{
				Type: content.BlockQuote, ID: "b_000quote",
				Style: content.Style{BlockGap: content.Length{Value: "1rem"}},
				Attrs: content.Attrs{Citation: content.RichText{textNode(""), {Type: "image"}}},
				Children: content.Blocks{
					{Type: content.BlockParagraph, ID: "b_0quotep1", Text: content.RichText{textNode("a")}},
				},
			}},
			want:     "<blockquote class=\"b-000quote\">\n<p>a</p>\n</blockquote>",
			css:      flowCSS(".b-000quote", "1rem"),
			warnings: []string{`/0/attrs/citation/1: unknown inline type "image"; left out`},
		},
		{
			name: "blockGap of blocks that cannot have it",
			json: `[
				{
					"type": "paragraph", "id": "b_0paragr1",
					"style": {"blockGap": {"value": "1rem"}, "textColor": {"value": "#123456"}}, "content": [{"type": "text", "text": "a"}]
				},
				{
					"type": "table", "id": "b_000table", "style": {"blockGap": {"value": "1rem"}},
					"content": [{"type": "tableRow", "content": [{"type": "tableCell"}]}]
				}
			]`,
			want: "<p class=\"b-0paragr1\">a</p>\n<figure><table><tbody>\n<tr>\n<td></td>\n</tr>\n</tbody></table></figure>",
			css:  ".b-0paragr1 {\n  color: #123456 !important;\n}\n",
			warnings: []string{
				"/0/style/blockGap: blockGap is not allowed for paragraph blocks; ignored",
				"/1/style/blockGap: blockGap is not allowed for table blocks; ignored",
			},
		},
		{
			name: "the default style of borders",
			json: `[
				{"type": "paragraph", "id": "b_0paragr1", "style": {"border": {"width": {"value": "1px"}}}},
				{"type": "paragraph", "id": "b_0paragr2", "style": {"border": {"color": {"preset": "primary"}}}},
				{
					"type": "paragraph", "id": "b_0paragr3",
					"style": {"border": {"width": {"value": "1px"}, "style": {"value": "dotted"}}}
				},
				{"type": "paragraph", "id": "b_0paragr4", "style": {"border": {"style": {"value": "solid"}}}},
				{"type": "paragraph", "id": "b_0paragr5", "style": {"border": {"radius": {"value": "4px"}}}},
				{
					"type": "paragraph", "id": "b_0paragr6",
					"style": {"border": {"width": {"value": "1px"}, "style": {"value": "double"}}}
				},
				{
					"type": "paragraph", "id": "b_0paragr7",
					"style": {"border": {"color": {"preset": "missing"}, "radius": {"value": "2px"}}}
				}
			]`,
			want: `<p class="b-0paragr1"></p>
<p class="b-0paragr2"></p>
<p class="b-0paragr3"></p>
<p class="b-0paragr4"></p>
<p class="b-0paragr5"></p>
<p class="b-0paragr6"></p>
<p class="b-0paragr7"></p>`,
			css: ".b-0paragr1 {\n  border-width: 1px !important;\n}\n\n:where(.b-0paragr1) {\n  border-style: solid;\n}\n\n" +
				".b-0paragr2 {\n  border-color: var(--color-primary) !important;\n}\n\n" +
				":where(.b-0paragr2) {\n  border-style: solid;\n}\n\n" +
				".b-0paragr3 {\n  border-width: 1px !important;\n  border-style: dotted !important;\n}\n\n" +
				".b-0paragr4 {\n  border-style: solid !important;\n}\n\n" +
				".b-0paragr5 {\n  border-radius: 4px !important;\n}\n\n" +
				".b-0paragr6 {\n  border-width: 1px !important;\n}\n\n:where(.b-0paragr6) {\n  border-style: solid;\n}\n\n" +
				".b-0paragr7 {\n  border-radius: 2px !important;\n}\n",
			warnings: []string{
				`/5/style/border/style/value: unknown border style "double"; ignored`,
				`/6/style/border/color/preset: the theme has no color preset "missing"; ignored`,
			},
		},
		{
			name: "presets that the theme does not have",
			json: `[{
				"type": "paragraph", "id": "b_0paragr1",
				"style": {
					"textColor": {"preset": "missing"}, "gradient": {"preset": "dawn"}, "fontSize": {"preset": "huge"},
					"fontFamily": {"preset": "mono"}, "padding": {"top": {"preset": "50"}}
				}
			}]`,
			want: "<p></p>",
			warnings: []string{
				`/0/style/textColor/preset: the theme has no color preset "missing"; ignored`,
				`/0/style/gradient/preset: the theme has no gradient preset "dawn"; ignored`,
				`/0/style/fontSize/preset: the theme has no fontSize preset "huge"; ignored`,
				`/0/style/fontFamily/preset: the theme has no fontFamily preset "mono"; ignored`,
				`/0/style/padding/top/preset: the theme has no spacing preset "50"; ignored`,
			},
		},
		{
			name: "invalid values",
			in: content.Blocks{{Type: content.BlockParagraph, ID: "b_0paragr1", Style: content.Style{
				TextColor:       content.Color{Value: "red"},
				BackgroundColor: content.Color{Preset: "primary", Value: "#fff"},
				LinkColor:       content.Color{Preset: "a b"},
				Gradient:        content.Gradient{Preset: "sunset;}"},
				FontSize:        content.Length{Value: "1rem;}body{color:red"},
				LetterSpacing:   content.Length{Preset: "wide"},
				FontWeight:      content.FontWeight{Value: 50},
				LineHeight:      content.LineHeight{Value: 11},
				Padding:         content.Sides{Top: content.Length{Value: "-1rem"}},
				Margin:          content.Sides{Left: content.Length{Value: "1"}},
				Border: content.Border{
					Width: content.Length{Value: "10%"}, Style: content.BorderStyle{Value: "double"},
					Color: content.Color{Value: "#ggg"}, Radius: content.Length{Value: "-2px"},
				},
			}}},
			want: "<p></p>",
			warnings: []string{
				`/0/style/textColor/value: invalid color "red"; ignored`,
				"/0/style/backgroundColor: set either preset or value, not both; ignored",
				`/0/style/linkColor/preset: invalid name "a b": use ASCII letters, digits, "-" and "_"; ignored`,
				`/0/style/gradient/preset: invalid name "sunset;}": use ASCII letters, digits, "-" and "_"; ignored`,
				`/0/style/fontSize/value: invalid length "1rem;}body{color:red"; ignored`,
				"/0/style/letterSpacing/preset: presets are not allowed here; ignored",
				"/0/style/fontWeight/value: font weight 50 is out of range (100-900); ignored",
				"/0/style/lineHeight/value: line height 11 is out of range (0 < x <= 10); ignored",
				`/0/style/padding/top/value: negative length "-1rem" is not allowed here; ignored`,
				`/0/style/margin/left/value: invalid length "1"; ignored`,
				`/0/style/border/width/value: percentage "10%" is not allowed here; ignored`,
				`/0/style/border/style/value: unknown border style "double"; ignored`,
				`/0/style/border/color/value: invalid color "#ggg"; ignored`,
				`/0/style/border/radius/value: negative length "-2px" is not allowed here; ignored`,
			},
		},
		// Without rules, an invalid ID does not matter, and blocks without rules do not take their IDs. The warning
		// about an ID comes first, where the ID is written.
		{
			name: "IDs",
			json: `[
				{"type": "paragraph", "id": "b_x", "style": {"textColor": {"value": "#111"}}},
				{"type": "paragraph", "style": {"textColor": {"value": "#111"}}},
				{"type": "paragraph", "id": "b_0PARAGR", "style": {"textColor": {"value": "#111"}}},
				{"type": "paragraph", "id": "b_y", "style": {"textColor": {"preset": "missing"}}},
				{"type": "paragraph", "id": "b_0paragr1", "style": {"textColor": {"value": "#111"}}},
				{"type": "paragraph", "id": "b_0paragr1", "style": {"textColor": {"value": "#222"}}},
				{"type": "paragraph", "id": "b_0paragr2"},
				{"type": "paragraph", "id": "b_0paragr2", "style": {"textColor": {"preset": "missing"}}},
				{"type": "paragraph", "id": "b_0paragr2", "style": {"textColor": {"value": "#333"}}},
				{
					"type": "group", "id": "b_00group1", "style": {"textColor": {"value": "#444"}},
					"content": [{"type": "paragraph", "id": "b_00group1", "style": {"textColor": {"value": "#555"}}}]
				},
				{
					"type": "paragraph", "id": "b_z", "variant": "zzz", "className": "x",
					"style": {"textColor": {"preset": "missing"}, "backgroundColor": {"value": "#666"}}
				}
			]`,
			want: `<p></p>
<p></p>
<p></p>
<p></p>
<p class="b-0paragr1"></p>
<p></p>
<p></p>
<p></p>
<p class="b-0paragr2"></p>
<div class="b-00group1">
<p></p>
</div>
<p class="x"></p>`,
			css: ".b-0paragr1 {\n  color: #111 !important;\n}\n\n.b-0paragr2 {\n  color: #333 !important;\n}\n\n" +
				".b-00group1 {\n  color: #444 !important;\n}\n",
			warnings: []string{
				`/0/id: invalid block ID "b_x"; rendered without its CSS`,
				`/1/id: invalid block ID ""; rendered without its CSS`,
				`/2/id: invalid block ID "b_0PARAGR"; rendered without its CSS`,
				`/3/style/textColor/preset: the theme has no color preset "missing"; ignored`,
				`/5/id: duplicate block ID "b_0paragr1"; rendered without its CSS`,
				`/7/style/textColor/preset: the theme has no color preset "missing"; ignored`,
				`/9/content/0/id: duplicate block ID "b_00group1"; rendered without its CSS`,
				`/10/id: invalid block ID "b_z"; rendered without its CSS`,
				`/10/variant: the theme has no paragraph variant "zzz"; rendered without its class`,
				`/10/style/textColor/preset: the theme has no color preset "missing"; ignored`,
			},
		},
		{
			name: "rules and warnings in document order",
			json: `[{
				"type": "quote", "id": "b_000quote", "variant": "pull",
				"style": {"textColor": {"value": "red"}, "blockGap": {"value": "1rem"}},
				"attrs": {"citation": [
					{"type": "text", "text": "c", "marks": [{"type": "highlight", "attrs": {"backgroundColor": {"value": "#fff4e5"}}}]},
					{"type": "text", "text": "d", "marks": [{"type": "link", "attrs": {"entry": "` + draftEntry + `"}}]}
				]},
				"content": [{
					"type": "paragraph", "id": "b_0quotep1", "style": {"fontSize": {"preset": "large"}},
					"content": [
						{"type": "text", "text": "q", "marks": [{"type": "highlight", "attrs": {"color": {"preset": "accent"}}}]},
						{"type": "text", "text": "r", "marks": [{"type": "highlight", "attrs": {"color": {"preset": "missing"}}}]}
					]
				}]
			}]`,
			want: `<figure class="b-000quote"><blockquote>
<p class="b-0quotep1"><mark class="hl-4e6255fc">q</mark><mark>r</mark></p>
</blockquote><figcaption><mark class="hl-a70e621a">c</mark>d</figcaption></figure>`,
			css: ".hl-a70e621a {\n  background-color: #fff4e5 !important;\n}\n\n" +
				flowCSS(".b-000quote > blockquote", "1rem") + "\n" +
				".b-0quotep1 {\n  font-size: var(--font-size-large) !important;\n}\n\n" +
				".hl-4e6255fc {\n  color: var(--color-accent) !important;\n}\n",
			warnings: []string{
				`/0/variant: the theme has no quote variant "pull"; rendered without its class`,
				`/0/style/textColor/value: invalid color "red"; ignored`,
				`/0/attrs/citation/1/marks/0: entry "` + draftEntry + `" is not published; rendered without the link`,
				`/0/content/0/content/1/marks/0/attrs/color/preset: the theme has no color preset "missing"; ignored`,
			},
		},
		{
			name: "rules of the rich text of attrs before those of the block, and of parts after them",
			json: `[
				{
					"type": "image", "id": "b_000image", "style": {"border": {"radius": {"value": "4px"}}},
					"attrs": {"media": "` + officeImage + `", "decorative": true, "caption": [
						{"type": "text", "text": "c", "marks": [{"type": "highlight", "attrs": {"color": {"preset": "primary"}}}]}
					]}
				},
				{
					"type": "table", "id": "b_000table", "style": {"margin": {"top": {"value": "1rem"}}},
					"attrs": {"caption": [
						{"type": "text", "text": "t", "marks": [{"type": "highlight", "attrs": {"backgroundColor": {"value": "#fff4e5"}}}]}
					]},
					"content": [{"type": "tableRow", "content": [{"type": "tableCell", "content": [
						{"type": "text", "text": "x", "marks": [{"type": "highlight", "attrs": {"color": {"preset": "accent"}}}]}
					]}]}]
				}
			]`,
			want: `<figure class="b-000image"><img src="` + officePath + `" alt="">` +
				`<figcaption><mark class="hl-065c532e">c</mark></figcaption></figure>
<figure class="b-000table"><table><tbody>
<tr>
<td><mark class="hl-4e6255fc">x</mark></td>
</tr>
</tbody></table><figcaption><mark class="hl-a70e621a">t</mark></figcaption></figure>`,
			css: ".hl-065c532e {\n  color: var(--color-primary) !important;\n}\n\n" +
				".b-000image {\n  border-radius: 4px !important;\n}\n\n" +
				".hl-a70e621a {\n  background-color: #fff4e5 !important;\n}\n\n" +
				".b-000table {\n  margin-top: 1rem !important;\n}\n\n" +
				".hl-4e6255fc {\n  color: var(--color-accent) !important;\n}\n",
		},
	}
	checkBlocks(t, tests, styleTheme, "ja", "", mediaResolver(t))
}

// TestStyleTheme checks that styles keep to what the theme allows: the style items of its blocks, and its presets
// only, for the kinds whose custom is false.
func TestStyleTheme(t *testing.T) {
	const settings = `schemaVersion: 1
presets:
  color:
    primary: "#1e73be"
  spacing:
    "30": 1rem
styles: [textColor, backgroundColor, padding, border]
custom:
  color: false
  spacing: false
blocks:
  paragraph:
    variants:
      lead: リード
  code:
    styles: [backgroundColor]
  group:
    styles: [textColor, blockGap]
`
	tests := []blockTest{
		{
			name: "the items of the theme",
			json: `[{
				"type": "paragraph", "id": "b_0paragr1",
				"style": {
					"textColor": {"preset": "primary"}, "backgroundColor": {"value": "#fff"}, "fontSize": {"value": "1rem"},
					"padding": {"top": {"preset": "30"}, "bottom": {"value": "1rem"}}, "margin": {"top": {"value": "1rem"}},
					"border": {"width": {"value": "1px"}, "color": {"value": "#000"}}
				}
			}]`,
			want: `<p class="b-0paragr1"></p>`,
			css: ".b-0paragr1 {\n  color: var(--color-primary) !important;\n  padding-top: var(--spacing-30) !important;\n" +
				"  border-width: 1px !important;\n}\n\n:where(.b-0paragr1) {\n  border-style: solid;\n}\n",
			warnings: []string{
				"/0/style/backgroundColor/value: the theme allows only presets for color; ignored",
				"/0/style/fontSize: the theme does not allow fontSize for paragraph blocks; ignored",
				"/0/style/padding/bottom/value: the theme allows only presets for spacing; ignored",
				"/0/style/margin: the theme does not allow margin for paragraph blocks; ignored",
				"/0/style/border/color/value: the theme allows only presets for color; ignored",
			},
		},
		{
			name: "the items of the blocks of a type",
			json: `[
				{
					"type": "code", "id": "b_0000code",
					"style": {"textColor": {"preset": "primary"}, "backgroundColor": {"preset": "primary"}}
				},
				{
					"type": "group", "id": "b_00group1",
					"style": {"textColor": {"preset": "primary"}, "blockGap": {"value": "1rem"}, "border": {"width": {"value": "1px"}}}
				},
				{"type": "quote", "id": "b_000quote", "style": {"blockGap": {"preset": "30"}}, "content": [` +
				paragraphJSON("b_0quotep1", "q") + `]},
				{"type": "paragraph", "id": "b_0paragr1", "style": {"blockGap": {"preset": "30"}}}
			]`,
			want: "<pre class=\"b-0000code\"><code></code></pre>\n<div class=\"b-00group1\"></div>\n" +
				"<blockquote>\n<p>q</p>\n</blockquote>\n<p></p>",
			css: ".b-0000code {\n  background-color: var(--color-primary) !important;\n}\n\n" +
				".b-00group1 {\n  color: var(--color-primary) !important;\n}\n",
			warnings: []string{
				"/0/style/textColor: the theme does not allow textColor for code blocks; ignored",
				"/1/style/blockGap/value: the theme allows only presets for spacing; ignored",
				"/1/style/border: the theme does not allow border for group blocks; ignored",
				"/2/style/blockGap: the theme does not allow blockGap for quote blocks; ignored",
				"/3/style/blockGap: blockGap is not allowed for paragraph blocks; ignored",
			},
		},
	}
	checkBlocks(t, tests, settings, "ja", "", nil)
}

// TestStyleIDsInDocument checks that the blocks of the lists of a document with the same ID as a block with rules in
// an earlier list are rendered without their rules.
func TestStyleIDsInDocument(t *testing.T) {
	r := newRenderer(t, testTheme(t, map[string]string{"theme.yaml": styleTheme}), "ja", "", nil)
	body := decodeBlocks(t, `[
		{"type": "paragraph", "id": "b_0paragr1", "style": {"textColor": {"preset": "primary"}}},
		{"type": "paragraph", "id": "b_0paragr2"}
	]`)
	field := decodeBlocks(t, `[
		{"type": "paragraph", "id": "b_0paragr2", "style": {"textColor": {"preset": "accent"}}},
		{"type": "group", "id": "b_000group", "content": [
			{"type": "paragraph", "id": "b_0paragr1", "style": {"textColor": {"preset": "accent"}}}
		]}
	]`)
	checkDocument(t, r, []content.Blocks{body, field},
		[]string{"<p class=\"b-0paragr1\"></p>\n<p></p>", "<p class=\"b-0paragr2\"></p>\n<div>\n<p></p>\n</div>"},
		".b-0paragr1 {\n  color: var(--color-primary) !important;\n}\n\n"+
			".b-0paragr2 {\n  color: var(--color-accent) !important;\n}\n",
		[]Warning{{List: 1, Path: "/1/content/0/id", Msg: `duplicate block ID "b_0paragr1"; rendered without its CSS`}})
}

// attrsTheme is the settings file of a theme with the spacing presets 30 and 40, whose paragraphs have the variant
// lead and whose groups have box.
const attrsTheme = "schemaVersion: 1\npresets:\n  spacing:\n    \"30\": 1rem\n    \"40\": 2rem\n" +
	"blocks:\n  paragraph:\n    variants:\n      lead: リード\n  group:\n    variants:\n      box: ボックス\n"

// decorativeImage returns the JSON of a decorative image of officeImage with the ID id, and attrs, more members of its
// attrs, if not "".
func decorativeImage(id, attrs string) string {
	if attrs != "" {
		attrs = ", " + attrs
	}
	return `{"type": "image", "id": "` + id + `", "attrs": {"media": "` + officeImage + `", "decorative": true` + attrs + "}}"
}

// decorativeFigure returns the HTML of a decorative image of officeImage whose figure has the classes class, if not "".
func decorativeFigure(class string) string {
	if class != "" {
		class = ` class="` + class + `"`
	}
	return "<figure" + class + `><img src="` + officePath + `" alt=""></figure>`
}

// TestAttrClasses checks the class of each choice of the attributes with fixed choices, and the order of the classes:
// b-<ID>, those of the attributes in their order, the class of the variant and className.
func TestAttrClasses(t *testing.T) {
	tests := []blockTest{
		{
			name: "textAlign",
			json: `[
				{"type": "paragraph", "id": "b_0paragr1", "attrs": {"textAlign": "left"}},
				{"type": "paragraph", "id": "b_0paragr2", "attrs": {"textAlign": "center"}},
				{"type": "paragraph", "id": "b_0paragr3", "attrs": {"textAlign": "right"}},
				{"type": "heading", "id": "b_heading1", "attrs": {"level": 2, "textAlign": "left"}},
				{"type": "heading", "id": "b_heading2", "anchor": "h", "attrs": {"level": 3, "textAlign": "center"}},
				{"type": "heading", "id": "b_heading3", "attrs": {"level": 4, "textAlign": "right"}},
				{"type": "table", "id": "b_000table", "content": [{"type": "tableRow", "content": [
					{"type": "tableCell", "attrs": {"header": true, "colspan": 2, "textAlign": "left"}},
					{"type": "tableCell", "attrs": {"rowspan": 3, "textAlign": "center"}, "content": [{"type": "text", "text": "x"}]},
					{"type": "tableCell", "attrs": {"header": true, "textAlign": "right"}},
					{"type": "tableCell"}
				]}]}
			]`,
			want: `<p class="has-text-align-left"></p>
<p class="has-text-align-center"></p>
<p class="has-text-align-right"></p>
<h2 id="b_heading1" class="has-text-align-left"></h2>
<h3 id="h" class="has-text-align-center"></h3>
<h4 id="b_heading3" class="has-text-align-right"></h4>
<figure><table><tbody>
<tr>
<th class="has-text-align-left" colspan="2"></th>
<td class="has-text-align-center" rowspan="3">x</td>
<th class="has-text-align-right"></th>
<td></td>
</tr>
</tbody></table></figure>`,
		},
		{
			name: "align",
			json: `[
				` + decorativeImage("b_00image1", `"align": "left"`) + `,
				` + decorativeImage("b_00image2", `"align": "center"`) + `,
				` + decorativeImage("b_00image3", `"align": "right"`) + `,
				` + decorativeImage("b_00image4", `"align": "wide"`) + `,
				` + decorativeImage("b_00image5", `"align": "full"`) + `,
				{"type": "group", "id": "b_00group1", "attrs": {"align": "wide"}},
				{"type": "group", "id": "b_00group2", "attrs": {"tagName": "section", "align": "full"}}
			]`,
			want: decorativeFigure("alignleft") + "\n" + decorativeFigure("aligncenter") + "\n" + decorativeFigure("alignright") +
				"\n" + decorativeFigure("alignwide") + "\n" + decorativeFigure("alignfull") + "\n" +
				"<div class=\"alignwide\"></div>\n<section class=\"alignfull\"></section>",
		},
		{
			name: "buttons",
			json: `[
				{"type": "buttons", "id": "b_buttons1", "content": [
					{"type": "button", "id": "b_0button1", "attrs": {"label": "a", "width": "25%"}},
					{"type": "button", "id": "b_0button2", "attrs": {"label": "b", "width": "50%"}},
					{"type": "button", "id": "b_0button3", "attrs": {"label": "c", "width": "75%"}},
					{"type": "button", "id": "b_0button4", "attrs": {"label": "d", "width": "100%"}},
					{"type": "button", "id": "b_0button5", "attrs": {"label": "e"}}
				]},
				{"type": "buttons", "id": "b_buttons2", "attrs": {"justify": "left"}},
				{"type": "buttons", "id": "b_buttons3", "attrs": {"justify": "center"}},
				{"type": "buttons", "id": "b_buttons4", "attrs": {"justify": "right"}},
				{"type": "buttons", "id": "b_buttons5", "attrs": {"justify": "spaceBetween"}}
			]`,
			want: `<div class="is-layout-row">
<a class="has-width-25">a</a>
<a class="has-width-50">b</a>
<a class="has-width-75">c</a>
<a class="has-width-100">d</a>
<a>e</a>
</div>
<div class="is-layout-row is-content-justification-left"></div>
<div class="is-layout-row is-content-justification-center"></div>
<div class="is-layout-row is-content-justification-right"></div>
<div class="is-layout-row is-content-justification-space-between"></div>`,
		},
		{
			name: "columns",
			json: `[
				{"type": "columns", "id": "b_columns1", "content": [
					{"type": "column", "id": "b_0column1", "attrs": {"verticalAlign": "top"}},
					{"type": "column", "id": "b_0column2", "attrs": {"verticalAlign": "center"}},
					{"type": "column", "id": "b_0column3", "attrs": {"verticalAlign": "bottom"}},
					{"type": "column", "id": "b_0column4", "attrs": {"verticalAlign": "stretch"}},
					{"type": "column", "id": "b_0column5"}
				]},
				{"type": "columns", "id": "b_columns2", "attrs": {"stackOnMobile": true}},
				{"type": "columns", "id": "b_columns3", "attrs": {"stackOnMobile": false}},
				{"type": "columns", "id": "b_columns4", "attrs": {"verticalAlign": "top"}},
				{"type": "columns", "id": "b_columns5", "attrs": {"verticalAlign": "center", "stackOnMobile": true}},
				{"type": "columns", "id": "b_columns6", "attrs": {"verticalAlign": "bottom", "stackOnMobile": false}}
			]`,
			want: `<div class="is-layout-columns">
<div class="is-vertically-aligned-top"></div>
<div class="is-vertically-aligned-center"></div>
<div class="is-vertically-aligned-bottom"></div>
<div class="is-vertically-aligned-stretch"></div>
<div></div>
</div>
<div class="is-layout-columns"></div>
<div class="is-layout-columns is-not-stacked-on-mobile"></div>
<div class="is-layout-columns are-vertically-aligned-top"></div>
<div class="is-layout-columns are-vertically-aligned-center"></div>
<div class="is-layout-columns are-vertically-aligned-bottom is-not-stacked-on-mobile"></div>`,
		},
		{
			name: "layouts",
			json: `[
				{"type": "group", "id": "b_00group1", "attrs": {"layout": {"type": "flow"}}},
				{"type": "group", "id": "b_00group2", "attrs": {"layout": {"type": "row"}}},
				{
					"type": "group", "id": "b_00group3",
					"attrs": {"layout": {"type": "row", "justify": "left", "verticalAlign": "top"}}
				},
				{
					"type": "group", "id": "b_00group4",
					"attrs": {"layout": {"type": "row", "justify": "center", "verticalAlign": "center"}}
				},
				{
					"type": "group", "id": "b_00group5",
					"attrs": {"layout": {"type": "row", "justify": "right", "verticalAlign": "bottom"}}
				},
				{
					"type": "group", "id": "b_00group6",
					"attrs": {"layout": {"type": "row", "justify": "spaceBetween", "verticalAlign": "stretch", "nowrap": true}}
				},
				{"type": "group", "id": "b_00group7", "attrs": {"layout": {"type": "row", "nowrap": true}}},
				{"type": "group", "id": "b_00group8", "attrs": {"layout": {"type": "stack"}}},
				{
					"type": "group", "id": "b_00group9",
					"attrs": {"layout": {"type": "stack", "justify": "left", "verticalAlign": "top"}}
				},
				{
					"type": "group", "id": "b_00groupa",
					"attrs": {"layout": {"type": "stack", "justify": "center", "verticalAlign": "center"}}
				},
				{
					"type": "group", "id": "b_00groupb",
					"attrs": {"layout": {"type": "stack", "justify": "right", "verticalAlign": "bottom"}}
				},
				{
					"type": "group", "id": "b_00groupc",
					"attrs": {"layout": {"type": "stack", "justify": "stretch", "verticalAlign": "spaceBetween"}}
				},
				{"type": "group", "id": "b_00groupd", "attrs": {"layout": {"type": "grid"}}}
			]`,
			want: `<div class="is-layout-flow"></div>
<div class="is-layout-row"></div>
<div class="is-layout-row is-content-justification-left are-vertically-aligned-top"></div>
<div class="is-layout-row is-content-justification-center are-vertically-aligned-center"></div>
<div class="is-layout-row is-content-justification-right are-vertically-aligned-bottom"></div>
<div class="is-layout-row is-content-justification-space-between are-vertically-aligned-stretch is-nowrap"></div>
<div class="is-layout-row is-nowrap"></div>
<div class="is-layout-stack"></div>
<div class="is-layout-stack is-content-justification-left are-vertically-aligned-top"></div>
<div class="is-layout-stack is-content-justification-center are-vertically-aligned-center"></div>
<div class="is-layout-stack is-content-justification-right are-vertically-aligned-bottom"></div>
<div class="is-layout-stack is-content-justification-stretch are-vertically-aligned-space-between"></div>
<div class="is-layout-grid"></div>`,
		},
		{
			name: "the order of the classes, also without rules or a valid ID",
			json: `[
				{
					"type": "paragraph", "id": "b_0paragr1", "variant": "lead", "className": "intro",
					"style": {"padding": {"top": {"preset": "30"}}}, "attrs": {"textAlign": "center"}
				},
				{
					"type": "group", "id": "b_00group1", "variant": "box", "className": "note x",
					"style": {"blockGap": {"preset": "40"}},
					"attrs": {
						"layout": {"type": "row", "justify": "spaceBetween", "verticalAlign": "stretch", "nowrap": true},
						"tagName": "aside", "label": "l", "align": "full"
					}
				},
				{
					"type": "columns", "id": "b_columns1", "className": "cols",
					"attrs": {"verticalAlign": "center", "stackOnMobile": false}
				},
				{"type": "paragraph", "variant": "lead", "attrs": {"textAlign": "right"}},
				` + decorativeImage("b_x", `"width": "50%", "align": "right"`) + `
			]`,
			want: `<p class="b-0paragr1 has-text-align-center is-variant-lead intro"></p>
<aside class="b-00group1 is-layout-row is-content-justification-space-between are-vertically-aligned-stretch is-nowrap ` +
				`alignfull is-variant-box note x"><header>l</header></aside>
<div class="is-layout-columns are-vertically-aligned-center is-not-stacked-on-mobile cols"></div>
<p class="has-text-align-right is-variant-lead"></p>
` + decorativeFigure("alignright"),
			css: ".b-0paragr1 {\n  padding-top: var(--spacing-30) !important;\n}\n\n" +
				".b-00group1 {\n  gap: var(--spacing-40) !important;\n}\n",
			warnings: []string{`/4/id: invalid block ID "b_x"; rendered without its CSS`},
		},
	}
	checkBlocks(t, tests, attrsTheme, "ja", "", mediaResolver(t))
}

// TestAttrChoices checks that the attributes with fixed choices have classes for the values that validation accepts,
// and for no others.
func TestAttrChoices(t *testing.T) {
	group := func(a content.Attrs) content.Blocks {
		return content.Blocks{{Type: content.BlockGroup, ID: "b_000group", Attrs: a}}
	}
	buttons := func(a, button content.Attrs) content.Blocks {
		button.Label = "a"
		return content.Blocks{{Type: content.BlockButtons, ID: "b_0buttons", Attrs: a, Children: content.Blocks{
			{Type: content.BlockButton, ID: "b_0button1", Attrs: button},
		}}}
	}
	columns := func(a, column content.Attrs) content.Blocks {
		return content.Blocks{{Type: content.BlockColumns, ID: "b_0columns", Attrs: a, Children: content.Blocks{
			{Type: content.BlockColumn, ID: "b_0column1", Attrs: column},
		}}}
	}
	type attrTest struct {
		name    string
		classes map[string]string
		blocks  func(v string) content.Blocks // valid unless v, the value of the attribute, is not
	}
	tests := []attrTest{
		{"textAlign of paragraphs", textAlignClasses, func(v string) content.Blocks {
			return content.Blocks{{Type: content.BlockParagraph, ID: "b_0paragr1", Attrs: content.Attrs{TextAlign: v}}}
		}},
		{"textAlign of headings", textAlignClasses, func(v string) content.Blocks {
			return content.Blocks{{
				Type: content.BlockHeading, ID: "b_0heading", Attrs: content.Attrs{Level: 2, TextAlign: v},
				Text: content.RichText{textNode("h")},
			}}
		}},
		{"textAlign of table cells", textAlignClasses, func(v string) content.Blocks {
			return content.Blocks{{Type: content.BlockTable, ID: "b_000table", Children: content.Blocks{{
				Type:     content.BlockTableRow,
				Children: content.Blocks{{Type: content.BlockTableCell, Attrs: content.Attrs{TextAlign: v}}},
			}}}}
		}},
		{"align of images", imageAlignClasses, func(v string) content.Blocks {
			return content.Blocks{{
				Type: content.BlockImage, ID: "b_000image", Attrs: content.Attrs{Media: officeImage, Decorative: true, Align: v},
			}}
		}},
		{"align of groups", groupAlignClasses, func(v string) content.Blocks { return group(content.Attrs{Align: v}) }},
		{"justify of buttons", justifyClasses["row"], func(v string) content.Blocks {
			return buttons(content.Attrs{Justify: v}, content.Attrs{})
		}},
		{"width of buttons", buttonWidthClasses, func(v string) content.Blocks {
			return buttons(content.Attrs{}, content.Attrs{Width: v})
		}},
		{"verticalAlign of columns", columnsAlignClasses, func(v string) content.Blocks {
			return columns(content.Attrs{VerticalAlign: v}, content.Attrs{})
		}},
		{"verticalAlign of a column", columnAlignClasses, func(v string) content.Blocks {
			return columns(content.Attrs{}, content.Attrs{VerticalAlign: v})
		}},
		{"types of layouts", layoutClasses, func(v string) content.Blocks {
			return group(content.Attrs{Layout: content.Layout{Type: v}})
		}},
	}
	for _, typ := range []string{"row", "stack"} {
		tests = append(tests,
			attrTest{"justify of " + typ, justifyClasses[typ], func(v string) content.Blocks {
				return group(content.Attrs{Layout: content.Layout{Type: typ, Justify: v}})
			}},
			attrTest{"verticalAlign of " + typ, verticalAlignClasses[typ], func(v string) content.Blocks {
				return group(content.Attrs{Layout: content.Layout{Type: typ, VerticalAlign: v}})
			}},
		)
	}
	// The choices of every attribute, and values close to them.
	values := map[string]bool{"Left": true, "middle": true, "space-between": true, "none": true, "25": true, "30%": true}
	for _, tt := range tests {
		for v := range tt.classes {
			values[v] = true
		}
	}
	for _, tt := range tests {
		for _, v := range slices.Sorted(maps.Keys(values)) {
			err := tt.blocks(v).Validate(content.ValidateOptions{})
			if _, ok := tt.classes[v]; ok != (err == nil) {
				t.Errorf("%s: %q has a class: %v, but Validate() error: %v", tt.name, v, ok, err)
			}
		}
	}
}

// TestAttrCSS checks the rules of the lengths and the numbers of attributes: the size of the image of an image block,
// the width of a column, the height of a spacer and the columns of a grid. In the main rule, they follow the style.
func TestAttrCSS(t *testing.T) {
	tests := []blockTest{
		{
			name: "images",
			json: `[
				` + decorativeImage("b_00image1", `"width": "300px"`) + `,
				` + decorativeImage("b_00image2", `"aspectRatio": "16/9"`) + `,
				` + decorativeImage("b_00image3", `"width": "50%", "aspectRatio": "1.5", "align": "center"`) + `,
				` + decorativeImage("b_00image4", `"size": "large", "width": "0"`) + `,
				` + decorativeImage("b_00image5", `"size": "large"`) + `
			]`,
			want: decorativeFigure("b-00image1") + "\n" + decorativeFigure("b-00image2") + "\n" +
				decorativeFigure("b-00image3 aligncenter") + "\n" + decorativeFigure("b-00image4") + "\n" + decorativeFigure(""),
			css: ".b-00image1 img {\n  width: 300px !important;\n  height: auto !important;\n}\n\n" +
				".b-00image2 img {\n  aspect-ratio: 16/9 !important;\n  object-fit: cover !important;\n  height: auto !important;\n}\n\n" +
				".b-00image3 img {\n  width: 50% !important;\n  aspect-ratio: 1.5 !important;\n  object-fit: cover !important;\n" +
				"  height: auto !important;\n}\n\n" +
				".b-00image4 img {\n  width: 0 !important;\n  height: auto !important;\n}\n",
		},
		{
			name: "the rules of an image, in their order",
			json: `[{
				"type": "image", "id": "b_000image",
				"style": {"linkColor": {"value": "#c00"}, "border": {"width": {"value": "2px"}}},
				"attrs": {
					"media": "` + officeImage + `", "decorative": true, "link": {"href": "https://example.com/"},
					"aspectRatio": "4/3"
				}
			}]`,
			want: `<figure class="b-000image"><a href="https://example.com/"><img src="` + officePath + `" alt=""></a></figure>`,
			css: ".b-000image {\n  border-width: 2px !important;\n}\n\n:where(.b-000image) {\n  border-style: solid;\n}\n\n" +
				".b-000image :where(a) {\n  color: #c00 !important;\n}\n\n" +
				".b-000image img {\n  aspect-ratio: 4/3 !important;\n  object-fit: cover !important;\n  height: auto !important;\n}\n",
		},
		{
			name: "columns",
			json: `[{"type": "columns", "id": "b_columns1", "content": [
				{"type": "column", "id": "b_0column1", "attrs": {"width": "33.33%"}},
				{
					"type": "column", "id": "b_0column2",
					"style": {"padding": {"top": {"value": "1rem"}}, "blockGap": {"preset": "30"}},
					"attrs": {"width": "20rem", "verticalAlign": "center"},
					"content": [{"type": "paragraph", "id": "b_0colpar1", "style": {"margin": {"top": {"value": "0"}}}}]
				},
				{"type": "column", "id": "b_0column3", "attrs": {"width": "0"}}
			]}]`,
			want: `<div class="is-layout-columns">
<div class="b-0column1"></div>
<div class="b-0column2 is-vertically-aligned-center">
<p class="b-0colpar1"></p>
</div>
<div class="b-0column3"></div>
</div>`,
			css: ".b-0column1 {\n  flex-basis: 33.33% !important;\n  flex-grow: 0 !important;\n}\n\n" +
				".b-0column2 {\n  padding-top: 1rem !important;\n  flex-basis: 20rem !important;\n  flex-grow: 0 !important;\n}\n\n" +
				flowCSS(".b-0column2", "var(--spacing-30)") + "\n" +
				".b-0colpar1 {\n  margin-top: 0 !important;\n}\n\n" +
				".b-0column3 {\n  flex-basis: 0 !important;\n  flex-grow: 0 !important;\n}\n",
		},
		{
			name: "spacers",
			json: `[
				{"type": "spacer", "id": "b_0spacer1", "attrs": {"height": {"preset": "30"}}},
				{"type": "spacer", "id": "b_0spacer2", "attrs": {"height": {"value": "40px"}}},
				{"type": "spacer", "id": "b_0spacer3", "attrs": {"height": {"value": "0"}}},
				{
					"type": "spacer", "id": "b_0spacer4",
					"style": {"margin": {"top": {"preset": "40"}, "bottom": {"value": "-1rem"}}}, "attrs": {"height": {"value": "5vh"}}
				},
				{"type": "spacer", "id": "b_0spacer5"}
			]`,
			want: `<div class="b-0spacer1" aria-hidden="true"></div>
<div class="b-0spacer2" aria-hidden="true"></div>
<div class="b-0spacer3" aria-hidden="true"></div>
<div class="b-0spacer4" aria-hidden="true"></div>
<div aria-hidden="true"></div>`,
			css: ".b-0spacer1 {\n  height: var(--spacing-30) !important;\n}\n\n.b-0spacer2 {\n  height: 40px !important;\n}\n\n" +
				".b-0spacer3 {\n  height: 0 !important;\n}\n\n" +
				".b-0spacer4 {\n  margin-top: var(--spacing-40) !important;\n  margin-bottom: -1rem !important;\n" +
				"  height: 5vh !important;\n}\n",
		},
		// The gap in the columns of a grid with both is that of its blockGap, or the default 0.5em.
		{
			name: "grids",
			json: `[
				{"type": "group", "id": "b_00grid01", "attrs": {"layout": {"type": "grid", "columns": 3}}},
				{"type": "group", "id": "b_00grid02", "attrs": {"layout": {"type": "grid", "minColumnWidth": "12rem"}}},
				{"type": "group", "id": "b_00grid03", "attrs": {"layout": {"type": "grid", "columns": 4, "minColumnWidth": "10em"}}},
				{
					"type": "group", "id": "b_00grid04", "style": {"blockGap": {"preset": "30"}},
					"attrs": {"layout": {"type": "grid", "columns": 2, "minColumnWidth": "200px"}}
				},
				{
					"type": "group", "id": "b_00grid05", "style": {"padding": {"top": {"value": "1rem"}}, "blockGap": {"value": "2%"}},
					"attrs": {"layout": {"type": "grid", "columns": 16, "minColumnWidth": "0"}}
				},
				{"type": "group", "id": "b_00grid06", "attrs": {"layout": {"type": "grid", "columns": 1, "minColumnWidth": "5vw"}}},
				{
					"type": "group", "id": "b_00grid07", "style": {"blockGap": {"preset": "50"}},
					"attrs": {"layout": {"type": "grid", "columns": 3, "minColumnWidth": "8rem"}}
				},
				{"type": "group", "id": "b_00grid08", "style": {"blockGap": {"value": "1rem"}}, "attrs": {"layout": {"type": "grid"}}},
				{"type": "group", "id": "b_00grid09", "attrs": {"layout": {"type": "grid"}}}
			]`,
			want: `<div class="b-00grid01 is-layout-grid"></div>
<div class="b-00grid02 is-layout-grid"></div>
<div class="b-00grid03 is-layout-grid"></div>
<div class="b-00grid04 is-layout-grid"></div>
<div class="b-00grid05 is-layout-grid"></div>
<div class="b-00grid06 is-layout-grid"></div>
<div class="b-00grid07 is-layout-grid"></div>
<div class="b-00grid08 is-layout-grid"></div>
<div class="is-layout-grid"></div>`,
			css: ".b-00grid01 {\n  grid-template-columns: repeat(3, minmax(0, 1fr)) !important;\n}\n\n" +
				".b-00grid02 {\n  grid-template-columns: repeat(auto-fill, minmax(min(12rem, 100%), 1fr)) !important;\n}\n\n" +
				".b-00grid03 {\n  grid-template-columns: " +
				"repeat(auto-fill, minmax(max(min(10em, 100%), (100% - 3 * 0.5em) / 4), 1fr)) !important;\n}\n\n" +
				".b-00grid04 {\n  gap: var(--spacing-30) !important;\n  grid-template-columns: " +
				"repeat(auto-fill, minmax(max(min(200px, 100%), (100% - 1 * var(--spacing-30)) / 2), 1fr)) !important;\n}\n\n" +
				".b-00grid05 {\n  padding-top: 1rem !important;\n  gap: 2% !important;\n  grid-template-columns: " +
				"repeat(auto-fill, minmax(max(min(0, 100%), (100% - 15 * 2%) / 16), 1fr)) !important;\n}\n\n" +
				".b-00grid06 {\n  grid-template-columns: " +
				"repeat(auto-fill, minmax(max(min(5vw, 100%), (100% - 0 * 0.5em) / 1), 1fr)) !important;\n}\n\n" +
				".b-00grid07 {\n  grid-template-columns: " +
				"repeat(auto-fill, minmax(max(min(8rem, 100%), (100% - 2 * 0.5em) / 3), 1fr)) !important;\n}\n\n" +
				".b-00grid08 {\n  gap: 1rem !important;\n}\n",
			warnings: []string{`/6/style/blockGap/preset: the theme has no spacing preset "50"; ignored`},
		},
	}
	checkBlocks(t, tests, attrsTheme, "ja", "", mediaResolver(t))
}

// TestSpacerTheme checks that the height of a spacer keeps to the spacing presets of the theme and to its custom, as
// the values of styles do, but not to its style items, which are for styles.
func TestSpacerTheme(t *testing.T) {
	const (
		spacers = `[
			{"type": "spacer", "id": "b_0spacer1", "attrs": {"height": {"preset": "30"}}},
			{"type": "spacer", "id": "b_0spacer2", "attrs": {"height": {"value": "2rem"}}}
		]`
		presetCSS = ".b-0spacer1 {\n  height: var(--spacing-30) !important;\n}\n"
		valueCSS  = ".b-0spacer2 {\n  height: 2rem !important;\n}\n"
	)
	checkBlocks(t, []blockTest{{
		name: "a theme with the preset",
		json: spacers,
		want: "<div class=\"b-0spacer1\" aria-hidden=\"true\"></div>\n<div class=\"b-0spacer2\" aria-hidden=\"true\"></div>",
		css:  presetCSS + "\n" + valueCSS,
	}}, attrsTheme+"styles: [textColor]\n", "ja", "", nil)
	checkBlocks(t, []blockTest{{
		name:     "a theme without presets",
		json:     spacers,
		want:     "<div aria-hidden=\"true\"></div>\n<div class=\"b-0spacer2\" aria-hidden=\"true\"></div>",
		css:      valueCSS,
		warnings: []string{`/0/attrs/height/preset: the theme has no spacing preset "30"; ignored`},
	}}, "schemaVersion: 1\n", "ja", "", nil)
	checkBlocks(t, []blockTest{{
		name:     "a theme that allows only presets for spacing",
		json:     spacers,
		want:     "<div class=\"b-0spacer1\" aria-hidden=\"true\"></div>\n<div aria-hidden=\"true\"></div>",
		css:      presetCSS,
		warnings: []string{"/1/attrs/height/value: the theme allows only presets for spacing; ignored"},
	}}, attrsTheme+"custom:\n  spacing: false\n", "ja", "", nil)
}

// TestAttrWarnings checks the warnings about the values of attributes that validation does not accept, which give no
// classes or rules: in the order of the attributes, after those of the variant and the style, and before those of the
// content.
func TestAttrWarnings(t *testing.T) {
	const unpublished = `entry "` + draftEntry + `" is not published; rendered without the link`
	tests := []blockTest{
		{
			name: "values that are not choices",
			json: `[
				{"type": "paragraph", "id": "b_0paragr1", "attrs": {"textAlign": "left\" onclick=\"alert(1)"}},
				{"type": "heading", "id": "b_0heading", "attrs": {"level": 2, "textAlign": "Left"}},
				{"type": "table", "id": "b_000table", "content": [{"type": "tableRow", "content": [{
					"type": "tableCell", "attrs": {"textAlign": "has-text-align-left"},
					"content": [{"type": "text", "text": "x", "marks": [{"type": "link", "attrs": {"entry": "` + draftEntry + `"}}]}]
				}]}]},
				` + decorativeImage("b_000image", `"align": "none"`) + `,
				{"type": "buttons", "id": "b_buttons1", "attrs": {"justify": "stretch"}, "content": [
					{"type": "button", "id": "b_0button1", "attrs": {"label": "a", "width": "30%"}},
					{"type": "button", "id": "b_0button2", "attrs": {"label": "b", "width": "50"}}
				]},
				{"type": "columns", "id": "b_columns1", "attrs": {"verticalAlign": "stretch"}, "content": [
					{"type": "column", "id": "b_0column1", "attrs": {"verticalAlign": "spaceBetween"}}
				]},
				{"type": "group", "id": "b_00group1", "attrs": {"align": "left"}},
				{"type": "group", "id": "b_00group2", "attrs": {"align": "wide "}}
			]`,
			want: `<p></p>
<h2 id="b_0heading"></h2>
<figure><table><tbody>
<tr>
<td>x</td>
</tr>
</tbody></table></figure>
` + decorativeFigure("") + `
<div class="is-layout-row">
<a>a</a>
<a>b</a>
</div>
<div class="is-layout-columns">
<div></div>
</div>
<div></div>
<div></div>`,
			warnings: []string{
				`/0/attrs/textAlign: unknown textAlign "left\" onclick=\"alert(1)"; ignored`,
				`/1/attrs/textAlign: unknown textAlign "Left"; ignored`,
				`/2/content/0/content/0/attrs/textAlign: unknown textAlign "has-text-align-left"; ignored`,
				"/2/content/0/content/0/content/0/marks/0: " + unpublished,
				`/3/attrs/align: unknown align "none"; ignored`,
				`/4/attrs/justify: unknown justify "stretch"; ignored`,
				`/4/content/0/attrs/width: unknown width "30%"; ignored`,
				`/4/content/1/attrs/width: unknown width "50"; ignored`,
				`/5/attrs/verticalAlign: unknown verticalAlign "stretch"; ignored`,
				`/5/content/0/attrs/verticalAlign: unknown verticalAlign "spaceBetween"; ignored`,
				`/6/attrs/align: unknown align "left"; ignored`,
				`/7/attrs/align: unknown align "wide "; ignored`,
			},
		},
		{
			name: "lengths and ratios",
			json: `[
				` + decorativeImage("b_00image1", `"width": "300", "aspectRatio": "16:9", "align": "center"`) + `,
				` + decorativeImage("b_00image2", `"width": "-1px", "aspectRatio": "0/1"`) + `,
				` + decorativeImage("b_00image3", `"width": "10vmin", "aspectRatio": "1.5"`) + `,
				` + decorativeImage("b_00image4", `"width": "1rem;}img{color:red", "aspectRatio": "16/9;}"`) + `,
				{"type": "columns", "id": "b_columns1", "content": [
					{"type": "column", "id": "b_0column1", "attrs": {"width": "calc(50% - 1rem)", "verticalAlign": "top"}},
					{"type": "column", "id": "b_0column2", "attrs": {"width": "-10%"}}
				]}
			]`,
			want: decorativeFigure("aligncenter") + "\n" + decorativeFigure("") + "\n" + decorativeFigure("b-00image3") + "\n" +
				decorativeFigure("") + "\n" + `<div class="is-layout-columns">
<div class="is-vertically-aligned-top"></div>
<div></div>
</div>`,
			css: ".b-00image3 img {\n  aspect-ratio: 1.5 !important;\n  object-fit: cover !important;\n  height: auto !important;\n}\n",
			warnings: []string{
				`/0/attrs/width: invalid length "300"; ignored`,
				`/0/attrs/aspectRatio: invalid aspect ratio "16:9"; ignored`,
				`/1/attrs/width: negative length "-1px" is not allowed here; ignored`,
				`/1/attrs/aspectRatio: invalid aspect ratio "0/1"; ignored`,
				`/2/attrs/width: invalid length "10vmin"; ignored`,
				`/3/attrs/width: invalid length "1rem;}img{color:red"; ignored`,
				`/3/attrs/aspectRatio: invalid aspect ratio "16/9;}"; ignored`,
				`/4/content/0/attrs/width: invalid length "calc(50% - 1rem)"; ignored`,
				`/4/content/1/attrs/width: negative length "-10%" is not allowed here; ignored`,
			},
		},
		{
			name: "heights of spacers",
			json: `[
				{"type": "spacer", "id": "b_0spacer1", "attrs": {"height": {"value": "50%"}}},
				{"type": "spacer", "id": "b_0spacer2", "attrs": {"height": {"value": "-1px"}}},
				{"type": "spacer", "id": "b_0spacer3", "attrs": {"height": {"preset": "a b"}}},
				{"type": "spacer", "id": "b_0spacer4", "attrs": {"height": {"preset": "30", "value": "1rem"}}},
				{"type": "spacer", "id": "b_0spacer5", "attrs": {"height": {"value": "1rem;}"}}},
				{"type": "spacer", "id": "b_0spacer6", "attrs": {"height": {"preset": "50"}}}
			]`,
			want: strings.Repeat("<div aria-hidden=\"true\"></div>\n", 5) + "<div aria-hidden=\"true\"></div>",
			warnings: []string{
				`/0/attrs/height/value: percentage "50%" is not allowed here; ignored`,
				`/1/attrs/height/value: negative length "-1px" is not allowed here; ignored`,
				`/2/attrs/height/preset: invalid name "a b": use ASCII letters, digits, "-" and "_"; ignored`,
				"/3/attrs/height: set either preset or value, not both; ignored",
				`/4/attrs/height/value: invalid length "1rem;}"; ignored`,
				`/5/attrs/height/preset: the theme has no spacing preset "50"; ignored`,
			},
		},
		{
			name: "layouts",
			json: `[
				{"type": "group", "id": "b_00group1", "attrs": {"layout": {"justify": "left", "nowrap": true}}},
				{"type": "group", "id": "b_00group2", "attrs": {"layout": {"type": "column", "columns": 2}}},
				{
					"type": "group", "id": "b_00group3",
					"attrs": {"layout": {
						"type": "flow", "justify": "left", "verticalAlign": "top", "nowrap": true, "columns": 2, "minColumnWidth": "10rem"
					}}
				},
				{
					"type": "group", "id": "b_00group4",
					"attrs": {"layout": {
						"type": "row", "justify": "stretch", "verticalAlign": "spaceBetween", "columns": 3, "minColumnWidth": "10rem"
					}}
				},
				{
					"type": "group", "id": "b_00group5",
					"attrs": {"layout": {"type": "stack", "justify": "spaceBetween", "verticalAlign": "stretch", "nowrap": true}}
				},
				{
					"type": "group", "id": "b_00group6",
					"attrs": {"layout": {
						"type": "grid", "justify": "center", "verticalAlign": "center", "nowrap": true, "minColumnWidth": "50%"
					}}
				},
				{"type": "group", "id": "b_00group7", "attrs": {"layout": {"type": "grid", "columns": 2, "minColumnWidth": "-1rem"}}}
			]`,
			want: `<div></div>
<div></div>
<div class="is-layout-flow"></div>
<div class="is-layout-row"></div>
<div class="is-layout-stack"></div>
<div class="is-layout-grid"></div>
<div class="b-00group7 is-layout-grid"></div>`,
			css: ".b-00group7 {\n  grid-template-columns: repeat(2, minmax(0, 1fr)) !important;\n}\n",
			warnings: []string{
				"/0/attrs/layout/type: missing layout type; ignored",
				`/1/attrs/layout/type: unknown layout type "column"; ignored`,
				"/2/attrs/layout/justify: not allowed for flow layout; ignored",
				"/2/attrs/layout/verticalAlign: not allowed for flow layout; ignored",
				"/2/attrs/layout/nowrap: not allowed for flow layout; ignored",
				"/2/attrs/layout/columns: not allowed for flow layout; ignored",
				"/2/attrs/layout/minColumnWidth: not allowed for flow layout; ignored",
				`/3/attrs/layout/justify: unknown justify "stretch"; ignored`,
				`/3/attrs/layout/verticalAlign: unknown verticalAlign "spaceBetween"; ignored`,
				"/3/attrs/layout/columns: not allowed for row layout; ignored",
				"/3/attrs/layout/minColumnWidth: not allowed for row layout; ignored",
				`/4/attrs/layout/justify: unknown justify "spaceBetween"; ignored`,
				`/4/attrs/layout/verticalAlign: unknown verticalAlign "stretch"; ignored`,
				"/4/attrs/layout/nowrap: not allowed for stack layout; ignored",
				"/5/attrs/layout/justify: not allowed for grid layout; ignored",
				"/5/attrs/layout/verticalAlign: not allowed for grid layout; ignored",
				"/5/attrs/layout/nowrap: not allowed for grid layout; ignored",
				`/5/attrs/layout/minColumnWidth: percentage "50%" is not allowed here; ignored`,
				`/6/attrs/layout/minColumnWidth: negative length "-1rem" is not allowed here; ignored`,
			},
		},
		{
			name: "columns of grids out of range",
			in: content.Blocks{
				{Type: content.BlockGroup, ID: "b_00group1", Attrs: content.Attrs{Layout: content.Layout{
					Type: "grid", Columns: 17, MinColumnWidth: "10rem",
				}}},
				{Type: content.BlockGroup, ID: "b_00group2", Attrs: content.Attrs{Layout: content.Layout{Type: "grid", Columns: -1}}},
				{Type: content.BlockGroup, ID: "b_00group3", Attrs: content.Attrs{Layout: content.Layout{Type: "row", Columns: 99}}},
			},
			want: "<div class=\"b-00group1 is-layout-grid\"></div>\n<div class=\"is-layout-grid\"></div>\n" +
				"<div class=\"is-layout-row\"></div>",
			css: ".b-00group1 {\n  grid-template-columns: repeat(auto-fill, minmax(min(10rem, 100%), 1fr)) !important;\n}\n",
			warnings: []string{
				"/0/attrs/layout/columns: columns 17 is out of range (1-16); ignored",
				"/1/attrs/layout/columns: columns -1 is out of range (1-16); ignored",
				"/2/attrs/layout/columns: not allowed for row layout; ignored",
			},
		},
		{
			name: "warnings in document order",
			json: `[
				{
					"type": "image", "id": "b_000image", "variant": "x", "style": {"textColor": {"value": "red"}},
					"attrs": {
						"media": "` + noAltImage + `",
						"caption": [{"type": "text", "text": "c", "marks": [{"type": "link", "attrs": {"entry": "` + draftEntry + `"}}]}],
						"link": {"entry": "` + missingEntry + `"}, "size": "large", "width": "1", "aspectRatio": "a", "align": "middle"
					}
				},
				{
					"type": "buttons", "id": "b_buttons1", "attrs": {"justify": "x"},
					"content": [{
						"type": "button", "id": "b_0button1",
						"attrs": {"label": "a", "link": {"entry": "` + draftEntry + `"}, "width": "x"}
					}]
				},
				{
					"type": "columns", "id": "b_columns1", "variant": "y", "attrs": {"verticalAlign": "x", "stackOnMobile": false},
					"content": [{
						"type": "column", "id": "b_0column1", "style": {"blockGap": {"preset": "50"}},
						"attrs": {"width": "x", "verticalAlign": "x"},
						"content": [{"type": "paragraph", "id": "b_0paragr1", "attrs": {"textAlign": "x"}, "content": [
							{"type": "text", "text": "p", "marks": [{"type": "link", "attrs": {"entry": "` + draftEntry + `"}}]}
						]}]
					}]
				},
				{
					"type": "group", "id": "b_000group", "variant": "z", "style": {"margin": {"top": {"value": "1"}}},
					"attrs": {"layout": {"type": "row", "justify": "x", "nowrap": true}, "align": "x"},
					"content": [{"type": "spacer", "id": "b_00spacer", "attrs": {"height": {"value": "x"}}}]
				}
			]`,
			want: `<figure><img src="/media/plain.webp"><figcaption>c</figcaption></figure>
<div class="is-layout-row">
<a>a</a>
</div>
<div class="is-layout-columns is-not-stacked-on-mobile">
<div>
<p>p</p>
</div>
</div>
<div class="is-layout-row is-nowrap">
<div aria-hidden="true"></div>
</div>`,
			warnings: []string{
				`/0/variant: the theme has no image variant "x"; rendered without its class`,
				`/0/style/textColor/value: invalid color "red"; ignored`,
				"/0/attrs/alt: no alt in ja: neither the block nor its medium has one; rendered without the alt attribute",
				"/0/attrs/caption/0/marks/0: " + unpublished,
				`/0/attrs/link: entry "` + missingEntry + `" does not exist; rendered without the link`,
				`/0/attrs/width: invalid length "1"; ignored`,
				`/0/attrs/aspectRatio: invalid aspect ratio "a"; ignored`,
				`/0/attrs/align: unknown align "middle"; ignored`,
				`/1/attrs/justify: unknown justify "x"; ignored`,
				"/1/content/0/attrs/link: " + unpublished,
				`/1/content/0/attrs/width: unknown width "x"; ignored`,
				`/2/variant: the theme has no columns variant "y"; rendered without its class`,
				`/2/attrs/verticalAlign: unknown verticalAlign "x"; ignored`,
				`/2/content/0/style/blockGap/preset: the theme has no spacing preset "50"; ignored`,
				`/2/content/0/attrs/width: invalid length "x"; ignored`,
				`/2/content/0/attrs/verticalAlign: unknown verticalAlign "x"; ignored`,
				`/2/content/0/content/0/attrs/textAlign: unknown textAlign "x"; ignored`,
				"/2/content/0/content/0/content/0/marks/0: " + unpublished,
				`/3/variant: the theme has no group variant "z"; rendered without its class`,
				`/3/style/margin/top/value: invalid length "1"; ignored`,
				`/3/attrs/layout/justify: unknown justify "x"; ignored`,
				`/3/attrs/align: unknown align "x"; ignored`,
				`/3/content/0/attrs/height/value: invalid length "x"; ignored`,
			},
		},
	}
	checkBlocks(t, tests, attrsTheme, "ja", "", mediaResolver(t))
}

// TestHighlightCSS checks the classes and the CSS of the colors of highlights.
func TestHighlightCSS(t *testing.T) {
	var (
		primary = highlightMark(content.Highlight{Color: content.Color{Preset: "primary"}})
		cream   = highlightMark(content.Highlight{BackgroundColor: content.Color{Value: "#fff4e5"}})
		both    = highlightMark(content.Highlight{
			Color: content.Color{Preset: "accent"}, BackgroundColor: content.Color{Value: "#fff4e5"},
		})
	)
	const (
		primaryCSS = ".hl-065c532e {\n  color: var(--color-primary) !important;\n}\n"
		creamCSS   = ".hl-a70e621a {\n  background-color: #fff4e5 !important;\n}\n"
	)
	tests := []struct {
		name     string
		settings string
		in       content.RichText
		want     string
		css      string
		warnings []string
	}{
		{
			name: "preset",
			in:   content.RichText{textNode("a", primary)},
			want: `<mark class="hl-065c532e">a</mark>`,
			css:  primaryCSS,
		},
		{
			name: "value",
			in:   content.RichText{textNode("a", cream)},
			want: `<mark class="hl-a70e621a">a</mark>`,
			css:  creamCSS,
		},
		{
			name: "both colors",
			in:   content.RichText{textNode("a", both)},
			want: `<mark class="hl-0b423a93">a</mark>`,
			css:  ".hl-0b423a93 {\n  color: var(--color-accent) !important;\n  background-color: #fff4e5 !important;\n}\n",
		},
		{
			name: "the same colors twice",
			in:   content.RichText{textNode("a", primary), textNode("b"), textNode("c", cream), textNode("d", primary)},
			want: `<mark class="hl-065c532e">a</mark>b<mark class="hl-a70e621a">c</mark><mark class="hl-065c532e">d</mark>`,
			css:  primaryCSS + "\n" + creamCSS,
		},
		{
			name: "the same colors next to each other",
			in: content.RichText{
				textNode("a", primary),
				textNode("b", boldMark, highlightMark(content.Highlight{Color: content.Color{Preset: "primary"}})),
			},
			want: `<mark class="hl-065c532e">a<strong>b</strong></mark>`,
			css:  primaryCSS,
		},
		{
			name: "colors that are ignored",
			in: content.RichText{
				textNode("a", highlightMark(content.Highlight{Color: content.Color{Preset: "missing"}})),
				textNode("b", highlightMark(content.Highlight{
					Color: content.Color{Value: "red"}, BackgroundColor: content.Color{Value: "#fff4e5"},
				})),
				textNode("c", highlightMark(content.Highlight{Color: content.Color{Preset: "primary", Value: "#fff"}})),
				textNode("d", highlightMark(content.Highlight{BackgroundColor: content.Color{Value: "#fff;}"}})),
			},
			want: `<mark>a</mark><mark class="hl-a70e621a">b</mark><mark>cd</mark>`,
			css:  creamCSS,
			warnings: []string{
				`/0/marks/0/attrs/color/preset: the theme has no color preset "missing"; ignored`,
				`/1/marks/0/attrs/color/value: invalid color "red"; ignored`,
				"/2/marks/0/attrs/color: set either preset or value, not both; ignored",
				`/3/marks/0/attrs/backgroundColor/value: invalid color "#fff;}"; ignored`,
			},
		},
		{
			name: "colors with a variant",
			in: content.RichText{
				textNode("a", highlightMark(content.Highlight{
					Variant: "marker", Color: content.Color{Preset: "primary"},
				})),
				textNode("b", highlightMark(content.Highlight{
					Variant: "wave", BackgroundColor: content.Color{Value: "red"},
				})),
			},
			want:     `<mark class="is-variant-marker">a</mark><mark>b</mark>`,
			warnings: []string{`/1/marks/0: the theme has no highlight variant "wave"; rendered without its class`},
		},
		{
			name: "no colors",
			in:   content.RichText{textNode("a", highlightMark(content.Highlight{}))},
			want: "<mark>a</mark>",
		},
		{
			name:     "a theme that allows only presets for colors",
			settings: "schemaVersion: 1\npresets:\n  color:\n    primary: \"#1e73be\"\ncustom:\n  color: false\n",
			in:       content.RichText{textNode("a", both), textNode("b", primary)},
			want:     `<mark>a</mark><mark class="hl-065c532e">b</mark>`,
			css:      primaryCSS,
			warnings: []string{
				`/0/marks/0/attrs/color/preset: the theme has no color preset "accent"; ignored`,
				"/0/marks/0/attrs/backgroundColor/value: the theme allows only presets for color; ignored",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			th := testTheme(t, map[string]string{"theme.yaml": cmp.Or(tt.settings, styleTheme)})
			r := newRenderer(t, th, "ja", "", nil)
			got, css, warnings := r.RichText(tt.in)
			if string(got) != tt.want {
				t.Errorf("RichText() =\n%s\nwant\n%s", got, tt.want)
			}
			if css.String() != tt.css {
				t.Errorf("CSS:\n%s\nwant:\n%s", css, tt.css)
			}
			if got := warningStrings(warnings); !slices.Equal(got, tt.warnings) {
				t.Errorf("warnings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.warnings, "\n"))
			}
		})
	}
}

func TestCSS(t *testing.T) {
	const (
		a = ".b-0paragr1 {\n  color: #111 !important;\n}\n"
		b = ".hl-a70e621a {\n  background-color: #fff4e5 !important;\n}\n"
		c = ":where(.b-0paragr1) {\n  border-style: solid;\n}\n"
	)
	if got := (CSS{}).String(); got != "" {
		t.Errorf("String() of no rules = %q, want empty", got)
	}
	if got := (CSS{rules: []string{a}}).String(); got != a {
		t.Errorf("String() of one rule = %q, want %q", got, a)
	}
	if got, want := (CSS{rules: []string{a, b, c}}).String(), a+"\n"+b+"\n"+c; got != want {
		t.Errorf("String() of three rules = %q, want %q", got, want)
	}
	tests := []struct {
		c, o, want []string
	}{
		{nil, nil, nil},
		{nil, []string{a, b}, []string{a, b}},
		{[]string{a, b}, nil, []string{a, b}},
		{[]string{b}, []string{a, b, c}, []string{b, a, c}},
		{[]string{a, b}, []string{b, a}, []string{a, b}},
		{[]string{a}, []string{c, c}, []string{a, c}},
	}
	for _, tt := range tests {
		x := CSS{rules: slices.Clone(tt.c)}
		x.Add(CSS{rules: tt.o})
		if !slices.Equal(x.rules, tt.want) {
			t.Errorf("Add() of %q to %q = %q, want %q", tt.o, tt.c, x.rules, tt.want)
		}
	}
	// The CSS of a page: its document and a rich text with the same highlight.
	r := newRenderer(t, testTheme(t, map[string]string{"theme.yaml": styleTheme}), "ja", "", nil)
	cream := highlightMark(content.Highlight{BackgroundColor: content.Color{Value: "#fff4e5"}})
	_, page, _, err := r.Document(content.Blocks{{
		Type: content.BlockParagraph, ID: "b_0paragr1", Style: content.Style{TextColor: content.Color{Value: "#111"}},
		Text: content.RichText{textNode("x", cream)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, field, _ := r.RichText(content.RichText{textNode("y", cream)})
	page.Add(field)
	if got, want := page.String(), a+"\n"+b; got != want {
		t.Errorf("CSS of the page =\n%s\nwant\n%s", got, want)
	}
}

func TestPresetVar(t *testing.T) {
	tests := []struct{ kind, name, want string }{
		{"color", "primary", "--color-primary"},
		{"fontSize", "large", "--font-size-large"},
		{"spacing", "30", "--spacing-30"},
		{"fontFamily", "body", "--font-family-body"},
		{"gradient", "sunset", "--gradient-sunset"},
		{"shadow", "Soft_1", "--shadow-Soft_1"},
	}
	for _, tt := range tests {
		if got := presetVar(tt.kind, tt.name); got != tt.want {
			t.Errorf("presetVar(%q, %q) = %q, want %q", tt.kind, tt.name, got, tt.want)
		}
	}
}
