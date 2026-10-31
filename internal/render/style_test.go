package render

import (
	"cmp"
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
			want: `<div class="b-buttons1">
<a>a</a>
</div>
<div class="b-columns1">
<div></div>
</div>
<div class="b-00group1"></div>
<div class="b-00group2"></div>
<div class="b-00group3"></div>`,
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
				{"type": "group", "id": "b_00group3", "attrs": {"layout": {"type": "Row"}}, "style": {"blockGap": {"value": "3rem"}}}
			]`,
			want: `<ul class="b-00list01">
<li><p>a</p></li>
</ul>
<div>
<div class="b-0column1"></div>
</div>
<div class="b-00group1"></div>
<div class="b-00group2"></div>
<div class="b-00group3"></div>`,
			css: flowCSS(".b-00list01", "0.5rem") + "\n" + flowCSS(".b-0column1", "var(--spacing-30)") + "\n" +
				flowCSS(".b-00group1", "1rem") + "\n" + flowCSS(".b-00group2", "2rem") + "\n" + flowCSS(".b-00group3", "3rem"),
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
