package wpimport

import (
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/html"
	"github.com/WhiteRoomLab/whiteroom/internal/wpblock"
)

// styleSettings are the settings that the tests of the style use: the color vivid-red is red in the theme, gone is not
// imported; the block style fancy is a variant of the theme, and gone is not imported; the font size huge is x-large.
func styleSettings() *Settings {
	s := &Settings{Blocks: emptyBlockSettings()}
	s.Blocks.Presets.Color["vivid-red"] = "red"
	s.Blocks.Presets.Color["gone"] = ""
	s.Blocks.Presets.FontSize["huge"] = "x-large"
	s.Blocks.Presets.Spacing["40"] = "large"
	s.Blocks.Variants["fancy"] = "fancy-theme"
	s.Blocks.Variants["gone"] = ""
	return s
}

// blockOf reads doc, one block, and returns it and the root element of its HTML.
func blockOf(t *testing.T, c *bodyConv, doc string) wpblock.Block {
	t.Helper()
	blocks, err := wpblock.Parse(doc, wpblock.Options{})
	if err != nil || len(blocks) != 1 {
		t.Fatalf("Parse(%q) = %v, %v", doc, blocks, err)
	}
	if blocks[0].Attrs == nil {
		blocks[0].Attrs = &wpblock.Value{Kind: wpblock.Object}
	}
	return blocks[0]
}

func TestProps(t *testing.T) {
	notes := func(lines ...string) []string {
		var out []string
		for _, l := range lines {
			out = append(out, "skipped: "+l+" -> /body")
		}
		return out
	}
	for _, tt := range []struct {
		name     string
		typ      content.BlockType // a paragraph if empty
		settings *Settings
		doc      string
		want     props
		notes    []string
	}{
		{name: "nothing", doc: paragraphDoc("", "<p>Hi</p>")},
		{name: "the slugs of the presets", doc: paragraphDoc(`{"textColor":"vivid-red","backgroundColor":"luminous-amber","gradient":"sunset","fontSize":"large","fontFamily":"system","borderColor":"cyan"}`, "<p></p>"),
			want: props{style: content.Style{
				TextColor: content.Color{Preset: "vivid-red"}, BackgroundColor: content.Color{Preset: "luminous-amber"},
				Gradient: content.Gradient{Preset: "sunset"}, FontSize: content.Length{Preset: "large"},
				FontFamily: content.FontFamily{Preset: "system"}, Border: content.Border{Color: content.Color{Preset: "cyan"}},
			}}},
		{name: "the slugs mapped by the settings", settings: styleSettings(), doc: paragraphDoc(`{"textColor":"vivid-red","backgroundColor":"gone","fontSize":"huge","borderColor":"vivid-red"}`, "<p></p>"),
			want: props{style: content.Style{
				TextColor: content.Color{Preset: "red"}, FontSize: content.Length{Preset: "x-large"}, Border: content.Border{Color: content.Color{Preset: "red"}},
			}},
			notes: notes(`the color preset "gone" is not imported: blocks.presets.color maps it to nothing`)},
		{name: "a slug that is not a name", doc: paragraphDoc(`{"textColor":"赤","gradient":"a.b"}`, "<p></p>"),
			notes: notes(`the color preset "赤" is not imported: it is not a name that whiteroom accepts`,
				`the gradient preset "a.b" is not imported: it is not a name that whiteroom accepts`)},
		{name: "the references to presets", settings: styleSettings(),
			doc: paragraphDoc(`{"style":{"color":{"text":"var:preset|color|vivid-red","background":"var(--wp--preset--color--gone)","gradient":"var:preset|gradient|sunset"},`+
				`"typography":{"fontSize":"var:preset|font-size|huge","fontFamily":"var(--wp--preset--font-family--body)"},`+
				`"spacing":{"padding":{"top":"var:preset|spacing|40","left":"var(--wp--preset--spacing--30)"},"margin":"var:preset|spacing|40","blockGap":"var:preset|spacing|40"}}}`, "<p></p>"),
			typ: content.BlockGroup,
			want: props{style: content.Style{
				TextColor: content.Color{Preset: "red"}, Gradient: content.Gradient{Preset: "sunset"}, FontSize: content.Length{Preset: "x-large"},
				FontFamily: content.FontFamily{Preset: "body"},
				Padding:    content.Sides{Top: content.Length{Preset: "large"}, Left: content.Length{Preset: "30"}},
				Margin: content.Sides{
					Top: content.Length{Preset: "large"}, Right: content.Length{Preset: "large"}, Bottom: content.Length{Preset: "large"},
					Left: content.Length{Preset: "large"},
				},
				BlockGap: content.Length{Preset: "large"},
			}},
			notes: notes(`the color preset "gone" is not imported: blocks.presets.color maps it to nothing`)},
		{name: "the values written directly",
			doc: paragraphDoc(`{"style":{"color":{"text":" #cf2e2e ","background":"rgb(1, 2, 3)"},"typography":{"fontSize":"1.5rem","letterSpacing":"-0.05em","fontWeight":"700","lineHeight":"1.6"},`+
				`"spacing":{"padding":{"top":"1rem","right":"5%","bottom":"0","left":"1.5em"},"margin":{"top":"-1rem","bottom":"2vh"}},`+
				`"border":{"width":"2px","style":"dashed","color":"hsl(10 20% 30%)","radius":"50%"}}}`, "<p></p>"),
			want: props{style: content.Style{
				TextColor: content.Color{Value: "#cf2e2e"}, BackgroundColor: content.Color{Value: "rgb(1, 2, 3)"},
				FontSize: content.Length{Value: "1.5rem"}, LetterSpacing: content.Length{Value: "-0.05em"},
				FontWeight: content.FontWeight{Value: 700}, LineHeight: content.LineHeight{Value: 1.6},
				Padding: content.Sides{
					Top: content.Length{Value: "1rem"}, Right: content.Length{Value: "5%"}, Bottom: content.Length{Value: "0"},
					Left: content.Length{Value: "1.5em"},
				},
				Margin: content.Sides{Top: content.Length{Value: "-1rem"}, Bottom: content.Length{Value: "2vh"}},
				Border: content.Border{
					Width: content.Length{Value: "2px"}, Style: content.BorderStyle{Value: "dashed"}, Color: content.Color{Value: "hsl(10 20% 30%)"},
					Radius: content.Length{Value: "50%"},
				},
			}}},
		{name: "values that are not accepted",
			doc: paragraphDoc(`{"style":{"color":{"text":"red","background":"var:preset|spacing|40"},"typography":{"fontSize":"clamp(1rem, 2vw, 3rem)","letterSpacing":"5%","fontFamily":"Arial, sans-serif"},`+
				`"spacing":{"padding":{"top":"-1rem","left":"1vmin"},"margin":"1"},"border":{"width":"50%","style":"double","radius":"-1px"}}}`, "<p></p>"),
			notes: notes(
				`the textColor "red" is not imported: invalid color "red"`,
				`the backgroundColor "var:preset|spacing|40" is not imported: invalid color "var:preset|spacing|40"`,
				`the fontSize "clamp(1rem, 2vw, 3rem)" is not imported: invalid length "clamp(1rem, 2vw, 3rem)"`,
				`the letterSpacing "5%" is not imported: percentage "5%" is not allowed here`,
				`the fontFamily "Arial, sans-serif" is not imported: whiteroom has font family presets only`,
				`the padding.top "-1rem" is not imported: negative length "-1rem" is not allowed here`,
				`the padding.left "1vmin" is not imported: invalid length "1vmin"`,
				`the margin "1" is not imported: invalid length "1"`,
				`the border.width "50%" is not imported: percentage "50%" is not allowed here`,
				`the border.style "double" is not imported: unknown border style "double"`,
				`the border.radius "-1px" is not imported: negative length "-1px" is not allowed here`,
			)},
		{name: "numbers", doc: paragraphDoc(`{"fontSize":null,"style":{"typography":{"fontSize":18,"fontWeight":600,"lineHeight":1.5},"border":{"width":2,"radius":8}}}`, "<p></p>"),
			want: props{style: content.Style{
				FontSize: content.Length{Value: "18px"}, FontWeight: content.FontWeight{Value: 600}, LineHeight: content.LineHeight{Value: 1.5},
				Border: content.Border{Width: content.Length{Value: "2px"}, Radius: content.Length{Value: "8px"}},
			}}},
		{name: "the old attributes", doc: paragraphDoc(`{"customTextColor":"#112233","customBackgroundColor":"rgb(1,2,3)","customFontSize":20,"customGradient":"linear-gradient(red, blue)"}`, "<p></p>"),
			want: props{style: content.Style{
				TextColor: content.Color{Value: "#112233"}, BackgroundColor: content.Color{Value: "rgb(1,2,3)"}, FontSize: content.Length{Value: "20px"},
			}},
			notes: notes(`the gradient "linear-gradient(red, blue)" is not imported: whiteroom has gradient presets only`)},
		{name: "the slug wins over the style", doc: paragraphDoc(`{"textColor":"vivid-red","style":{"color":{"text":"#112233"}}}`, "<p></p>"),
			want: props{style: content.Style{TextColor: content.Color{Preset: "vivid-red"}}}},
		{name: "the color of links", doc: paragraphDoc(`{"style":{"elements":{"link":{"color":{"text":"var:preset|color|vivid-red"}}}}}`, "<p></p>"),
			want: props{style: content.Style{LinkColor: content.Color{Preset: "vivid-red"}}}},
		{name: "font weights", doc: paragraphDoc(`{"style":{"typography":{"fontWeight":"normal"}}}`, "<p></p>"),
			want: props{style: content.Style{FontWeight: content.FontWeight{Value: 400}}}},
		{name: "a bold font weight", doc: paragraphDoc(`{"style":{"typography":{"fontWeight":"BOLD"}}}`, "<p></p>"),
			want: props{style: content.Style{FontWeight: content.FontWeight{Value: 700}}}},
		{name: "font weights that are not accepted", doc: paragraphDoc(`{"style":{"typography":{"fontWeight":"bolder"}}}`, "<p></p>"),
			notes: notes(`the fontWeight "bolder" is not imported: must be an integer`)},
		{name: "a font weight out of range", doc: paragraphDoc(`{"style":{"typography":{"fontWeight":"50"}}}`, "<p></p>"),
			notes: notes(`the fontWeight "50" is not imported: font weight 50 is out of range (100-900)`)},
		{name: "a font weight of 0", doc: paragraphDoc(`{"style":{"typography":{"fontWeight":0}}}`, "<p></p>"),
			notes: notes(`the fontWeight "0" is not imported: font weight 0 is out of range (100-900)`)},
		{name: "line heights that are not accepted", doc: paragraphDoc(`{"style":{"typography":{"lineHeight":"24px"}}}`, "<p></p>"),
			notes: notes(`the lineHeight "24px" is not imported: invalid number`)},
		{name: "a line height out of range", doc: paragraphDoc(`{"style":{"typography":{"lineHeight":11}}}`, "<p></p>"),
			notes: notes(`the lineHeight "11" is not imported: line height 11 is out of range (0 < x <= 10)`)},
		{name: "a line height of 0", doc: paragraphDoc(`{"style":{"typography":{"lineHeight":"0"}}}`, "<p></p>"),
			notes: notes(`the lineHeight "0" is not imported: line height 0 is out of range (0 < x <= 10)`)},
		{name: "a padding that is a text", doc: paragraphDoc(`{"style":{"spacing":{"padding":"2rem"}}}`, "<p></p>"),
			want: props{style: content.Style{Padding: content.Sides{
				Top: content.Length{Value: "2rem"}, Right: content.Length{Value: "2rem"}, Bottom: content.Length{Value: "2rem"}, Left: content.Length{Value: "2rem"},
			}}}},
		{name: "a padding that is a text that is not accepted", doc: paragraphDoc(`{"style":{"spacing":{"padding":"2"}}}`, "<p></p>"),
			notes: notes(`the padding "2" is not imported: invalid length "2"`)},

		// The gap between the children.
		{name: "a gap of a block that cannot have one", doc: paragraphDoc(`{"style":{"spacing":{"blockGap":"1rem"}}}`, "<p></p>"),
			notes: notes(`the blockGap "1rem" is not imported: blockGap is not allowed for paragraph blocks`)},
		{name: "a gap of a group", typ: content.BlockGroup, doc: paragraphDoc(`{"style":{"spacing":{"blockGap":"1rem"}}}`, "<p></p>"),
			want: props{style: content.Style{BlockGap: content.Length{Value: "1rem"}}}},
		{name: "a gap of a group that is a pair", typ: content.BlockGroup, doc: paragraphDoc(`{"style":{"spacing":{"blockGap":{"top":"1rem","left":"2rem"}}}}`, "<p></p>"),
			want:  props{style: content.Style{BlockGap: content.Length{Value: "1rem"}}},
			notes: notes(`the blockGap.left "2rem" is not imported: whiteroom has one block gap, which is blockGap.top here`)},
		{name: "a gap of columns", typ: content.BlockColumns, doc: paragraphDoc(`{"style":{"spacing":{"blockGap":{"top":"1rem","left":"2rem"}}}}`, "<p></p>"),
			want:  props{style: content.Style{BlockGap: content.Length{Value: "2rem"}}},
			notes: notes(`the blockGap.top "1rem" is not imported: whiteroom has one block gap, which is blockGap.left here`)},
		{name: "a gap of a row of a group", typ: content.BlockGroup, doc: paragraphDoc(`{"layout":{"type":"flex","flexWrap":"nowrap"},"style":{"spacing":{"blockGap":{"top":"1rem","left":"2rem"}}}}`, "<p></p>"),
			want:  props{style: content.Style{BlockGap: content.Length{Value: "2rem"}}},
			notes: notes(`the blockGap.top "1rem" is not imported: whiteroom has one block gap, which is blockGap.left here`)},
		{name: "a gap of a stack of a group", typ: content.BlockGroup, doc: paragraphDoc(`{"layout":{"type":"flex","orientation":"vertical"},"style":{"spacing":{"blockGap":{"top":"1rem","left":"2rem"}}}}`, "<p></p>"),
			want:  props{style: content.Style{BlockGap: content.Length{Value: "1rem"}}},
			notes: notes(`the blockGap.left "2rem" is not imported: whiteroom has one block gap, which is blockGap.top here`)},
		{name: "a gap of the same value", typ: content.BlockGroup, doc: paragraphDoc(`{"style":{"spacing":{"blockGap":{"top":"1rem","left":"1rem"}}}}`, "<p></p>"),
			want: props{style: content.Style{BlockGap: content.Length{Value: "1rem"}}}},
		{name: "a gap of one side that is not the one", typ: content.BlockButtons, doc: paragraphDoc(`{"style":{"spacing":{"blockGap":{"top":"1rem"}}}}`, "<p></p>"),
			want: props{style: content.Style{BlockGap: content.Length{Value: "1rem"}}}},
		{name: "a gap that is not accepted", typ: content.BlockQuote, doc: paragraphDoc(`{"style":{"spacing":{"blockGap":"clamp(1px, 2px, 3px)"}}}`, "<p></p>"),
			notes: notes(`the blockGap "clamp(1px, 2px, 3px)" is not imported: invalid length "clamp(1px, 2px, 3px)"`)},

		// The border.
		{name: "borders of the four sides that are the same",
			doc: paragraphDoc(`{"style":{"border":{"top":{"width":"1px","style":"solid","color":"#111111"},"right":{"width":"1px","style":"solid","color":"#111111"},`+
				`"bottom":{"width":"1px","style":"solid","color":"#111111"},"left":{"width":"1px","style":"solid","color":"#111111"},"radius":"4px"}}}`, "<p></p>"),
			want: props{style: content.Style{Border: content.Border{
				Width: content.Length{Value: "1px"}, Style: content.BorderStyle{Value: "solid"}, Color: content.Color{Value: "#111111"}, Radius: content.Length{Value: "4px"},
			}}}},
		{name: "borders of the sides that differ",
			doc:   paragraphDoc(`{"style":{"border":{"top":{"width":"1px"},"right":{"width":"2px"},"bottom":{"width":"1px"},"left":{"width":"1px"}}}}`, "<p></p>"),
			notes: notes(`the border is not imported: its sides differ, and whiteroom has one border for all four sides`)},
		{name: "a border on one side", doc: paragraphDoc(`{"style":{"border":{"bottom":{"width":"1px","color":"#111111"}}}}`, "<p></p>"),
			notes: notes(`the border is not imported: its sides differ, and whiteroom has one border for all four sides`)},
		{name: "a radius of the four corners that is the same",
			doc:  paragraphDoc(`{"style":{"border":{"radius":{"topLeft":"4px","topRight":"4px","bottomLeft":"4px","bottomRight":"4px"}}}}`, "<p></p>"),
			want: props{style: content.Style{Border: content.Border{Radius: content.Length{Value: "4px"}}}}},
		{name: "a radius of the corners that differ",
			doc:   paragraphDoc(`{"style":{"border":{"radius":{"topLeft":"4px","topRight":"8px","bottomLeft":"4px","bottomRight":"4px"}}}}`, "<p></p>"),
			notes: notes(`the border radius is not imported: its corners differ, and whiteroom has one radius for all four corners`)},
		{name: "a radius of one corner", doc: paragraphDoc(`{"style":{"border":{"radius":{"topLeft":"4px"}}}}`, "<p></p>"),
			notes: notes(`the border radius is not imported: its corners differ, and whiteroom has one radius for all four corners`)},
		{name: "a border color by a slug and a style", doc: paragraphDoc(`{"borderColor":"vivid-red","style":{"border":{"color":"#111111","width":"1px"}}}`, "<p></p>"),
			want: props{style: content.Style{Border: content.Border{Width: content.Length{Value: "1px"}, Color: content.Color{Preset: "vivid-red"}}}}},

		// What whiteroom has not.
		{name: "settings that whiteroom has not",
			doc: paragraphDoc(`{"style":{"typography":{"fontStyle":"italic","textTransform":"uppercase","fontSize":"2rem","textDecoration":"underline"},"shadow":"var:preset|shadow|natural",`+
				`"color":{"duotone":"x","text":"#111111"},"dimensions":{"minHeight":"10px"},"elements":{"heading":{"color":{"text":"#111111"}},"link":{":hover":{"color":{"text":"#222222"}},"color":{"background":"#333333"}}},`+
				`"css":"a{}","position":{"type":"sticky"},"spacing":{"padding":{"top":"1rem","horizontal":"2rem"}},"background":{"backgroundImage":{}}}}`, "<p></p>"),
			want: props{style: content.Style{TextColor: content.Color{Value: "#111111"}, FontSize: content.Length{Value: "2rem"}, Padding: content.Sides{Top: content.Length{Value: "1rem"}}}},
			notes: []string{`skipped: lost style.typography.fontStyle, style.typography.textTransform, style.typography.textDecoration, style.shadow, style.color.duotone, ` +
				`style.dimensions, style.elements.heading, style.elements.link.:hover, style.elements.link.color.background, style.css, ` +
				`and 3 more of the block "core/paragraph"; whiteroom has no such settings -> /body`}},
		{name: "the width of a button is not lost", typ: content.BlockButton,
			doc:   `<!-- wp:button {"style":{"dimensions":{"width":"50%","minHeight":"3rem"}}} --><div class="wp-block-button"><a class="wp-block-button__link">Go</a></div><!-- /wp:button -->`,
			notes: []string{`skipped: lost style.dimensions.minHeight of the block "core/button"; whiteroom has no such settings -> /body`}},
		{name: "a style that is not an object", doc: paragraphDoc(`{"style":"color:red"}`, "<p></p>"),
			notes: []string{`skipped: lost style of the block "core/paragraph"; whiteroom has no such settings -> /body`}},
		{name: "a style that is null", doc: paragraphDoc(`{"style":null}`, "<p></p>")},
		{name: "many settings that whiteroom has not", doc: paragraphDoc(`{"style":{"a":1,"b":1,"c":1,"d":1,"e":1,"f":1,"g":1,"h":1,"i":1,"j":1,"k":1,"l":1}}`, "<p></p>"),
			notes: []string{`skipped: lost style.a, style.b, style.c, style.d, style.e, style.f, style.g, style.h, style.i, style.j, and 2 more of the block "core/paragraph"; whiteroom has no such settings -> /body`}},
		{name: "a setting with a name to be quoted", doc: paragraphDoc(`{"style":{"a b":1}}`, "<p></p>"),
			notes: []string{`skipped: lost "style.a b" of the block "core/paragraph"; whiteroom has no such settings -> /body`}},

		// Block styles, classes and anchors.
		{name: "an anchor", doc: paragraphDoc("", `<p id="intro">Hi</p>`), want: props{anchor: "intro"}},
		{name: "an anchor that is not a name", doc: paragraphDoc("", `<p id="はじめに">Hi</p>`),
			notes: notes(`the anchor "はじめに" is not imported: invalid name "はじめに": use ASCII letters, digits, "-" and "_"`)},
		{name: "an anchor with a dot", doc: paragraphDoc("", `<p id="a.b">Hi</p>`),
			notes: notes(`the anchor "a.b" is not imported: invalid name "a.b": use ASCII letters, digits, "-" and "_"`)},
		{name: "a block style", settings: styleSettings(), doc: paragraphDoc(`{"className":"is-style-fancy"}`, "<p></p>"), want: props{variant: "fancy-theme"}},
		{name: "a block style that stays", doc: paragraphDoc(`{"className":"is-style-rounded"}`, "<p></p>"), want: props{variant: "rounded"}},
		{name: "the default block style", doc: paragraphDoc(`{"className":"is-style-default extra"}`, "<p></p>"), want: props{className: "extra"}},
		{name: "a block style that is not imported", settings: styleSettings(), doc: paragraphDoc(`{"className":"is-style-gone"}`, "<p></p>"),
			notes: notes(`the block style "gone" is not imported: blocks.variants maps it to nothing`)},
		{name: "a block style that is not a name", doc: paragraphDoc(`{"className":"is-style-赤"}`, "<p></p>"),
			notes: notes(`the block style "赤" is not imported: it is not a name that whiteroom accepts`)},
		{name: "two block styles", doc: paragraphDoc(`{"className":"is-style-a is-style-b is-style-a"}`, "<p></p>"), want: props{variant: "a"},
			notes: notes(`the block style "b" is not imported: a block has one style`)},
		{name: "a class", doc: paragraphDoc(`{"className":"  my-class  "}`, "<p></p>"), want: props{className: "my-class"}},
		{name: "classes", doc: paragraphDoc(`{"className":"a is-style-s b c a"}`, "<p></p>"), want: props{variant: "s", className: "a"},
			notes: notes(`the classes "b", "c" are not imported: whiteroom blocks have one class name`)},
		{name: "two classes", doc: paragraphDoc(`{"className":"a b"}`, "<p></p>"), want: props{className: "a"},
			notes: notes(`the class "b" is not imported: whiteroom blocks have one class name`)},
		{name: "classes that are not names", doc: paragraphDoc(`{"className":"a.b ok 日本 b-0123abcd hl-0123abcd"}`, "<p></p>"), want: props{className: "ok"},
			notes: notes(
				`the class "a.b" is not imported: invalid name "a.b": use ASCII letters, digits, "-" and "_"`,
				`the class "日本" is not imported: invalid name "日本": use ASCII letters, digits, "-" and "_"`,
				`the class "b-0123abcd" is not imported: the core gives classes of the forms b-xxxxxxxx and hl-xxxxxxxx`,
				`the class "hl-0123abcd" is not imported: the core gives classes of the forms b-xxxxxxxx and hl-xxxxxxxx`)},
		{name: "many classes", doc: paragraphDoc(`{"className":"c0 c1 c2 c3 c4 c5 c6 c7 c8 c9 c10 c11 c12"}`, "<p></p>"), want: props{className: "c0"},
			notes: notes(`the classes "c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "c9", "c10", and 2 more are not imported: whiteroom blocks have one class name`)},
		{name: "the classes of an embed", typ: content.BlockEmbed, doc: paragraphDoc(`{"className":"wp-embed-aspect-16-9 wp-has-aspect-ratio wide"}`, "<p></p>"), want: props{className: "wide"}},
		{name: "the classes of an image", typ: content.BlockImage, doc: paragraphDoc(`{"className":"wp-embed-aspect-16-9"}`, "<p></p>"), want: props{className: "wp-embed-aspect-16-9"}},
		{name: "the parts of a list have none", typ: content.BlockListItem, doc: paragraphDoc(`{"className":"a","textColor":"red"}`, `<p id="x">Hi</p>`)},
		{name: "the parts of a table have none", typ: content.BlockTableCell, doc: paragraphDoc(`{"className":"a","textColor":"red"}`, `<p id="x">Hi</p>`)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			imp := bodyImport()
			c := richConv(imp, "", tt.settings)
			b := blockOf(t, c, tt.doc)
			root, err := c.root(&b)
			if err != nil {
				t.Fatalf("root = %v", err)
			}
			typ := tt.typ
			if typ == "" {
				typ = content.BlockParagraph
			}
			got := c.props(&b, root, typ, noteAt{})
			if got != tt.want {
				t.Errorf("props = %+v, want %+v", got, tt.want)
			}
			if problem := styleProblem(got.style); problem != "" {
				t.Errorf("the style is not valid: %s", problem)
			}
			c.flush(nil)
			var lines []string
			for _, n := range tt.notes {
				lines = append(lines, bodyLine(n))
			}
			wantLines(t, imp.Report, lines)
		})
	}
}

func TestPropsAnchorsAreUnique(t *testing.T) {
	imp := bodyImport()
	c := richConv(imp, "", nil)
	var got []string
	for range 3 {
		b := blockOf(t, c, `<!-- wp:paragraph --><p id="intro">Hi</p><!-- /wp:paragraph -->`)
		root, err := c.root(&b)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, c.props(&b, root, content.BlockParagraph, noteAt{}).anchor)
	}
	if got[0] != "intro" || got[1] != "" || got[2] != "" {
		t.Errorf("the anchors are %q, want intro and then none", got)
	}
	c.flush(nil)
	want := bodyLines(`skipped: the anchor "intro" is not imported: another block has it -> /body`)
	wantLines(t, imp.Report, append(want, want[0]))
}

func TestAnchorsOfAFailedBlockAreGivenBack(t *testing.T) {
	withConverters(t, map[string]converter{
		"test/anchor": func(c *bodyConv, _ string, b *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
			root, err := c.root(b, "p")
			if err != nil {
				return nil, err
			}
			blk := c.newBlock(content.BlockParagraph)
			pr := c.props(b, root, content.BlockParagraph, noteAt{id: blk.ID})
			if fail, _ := b.Attrs.Get("fail").IsTrue(); fail {
				return nil, legacyf("it fails")
			}
			blk.Anchor = pr.anchor
			return []content.Block{blk}, nil
		},
	})
	const failing = `<!-- wp:test/anchor {"fail":true} --><p id="x">a</p><!-- /wp:test/anchor -->`
	const working = `<!-- wp:test/anchor --><p id="x">b</p><!-- /wp:test/anchor -->`
	res, imp := convertBody(t, bodySite(false), failing+working+working)
	// The first block is a legacy block and does not keep the anchor from the second.
	want := []content.Block{
		legacyBlock(bodyIDs[1], failing, "it fails"),
		{Type: content.BlockParagraph, ID: bodyIDs[2], Anchor: "x"},
		{Type: content.BlockParagraph, ID: bodyIDs[3]},
	}
	sameBlocks(t, res.Blocks, want)
	wantLines(t, imp.Report, bodyLines(
		legacyLine("it fails", "/body/0"),
		`skipped: the anchor "x" is not imported: another block has it -> /body/2`,
	))
}

func TestPropsWithoutARoot(t *testing.T) {
	c := richConv(bodyImport(), "", nil)
	b := blockOf(t, c, `<!-- wp:separator {"className":"x","textColor":"red"} /-->`)
	got := c.props(&b, nil, content.BlockSeparator, noteAt{})
	want := props{className: "x", style: content.Style{TextColor: content.Color{Preset: "red"}}}
	if got != want {
		t.Errorf("props = %+v, want %+v", got, want)
	}
}

func TestMapName(t *testing.T) {
	c := richConv(bodyImport(), "", nil)
	m := map[string]string{"a": "b", "gone": "", "bad": "a.b", "japanese": "日本"}
	for _, tt := range []struct {
		m    map[string]string
		name string
		want string
		ok   bool
	}{
		{m, "a", "b", true},
		{m, "other", "other", true},
		{m, "gone", "", false},
		{m, "bad", "", false},
		{m, "japanese", "", false},
		{m, "日本", "", false},
		{m, "a.b", "", false},
		{m, "", "", false},
		{nil, "x-1_y", "x-1_y", true},
		{nil, "x y", "", false},
	} {
		if got, ok := c.mapName(tt.m, tt.name); got != tt.want || ok != tt.ok {
			t.Errorf("mapName(%q) = %q, %t, want %q, %t", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}

func TestPresetRef(t *testing.T) {
	for _, tt := range []struct {
		in         string
		kind, slug string
		ok         bool
	}{
		{"var:preset|color|vivid-red", "color", "vivid-red", true},
		{" var:preset|font-size|x-large ", "font-size", "x-large", true},
		{"var(--wp--preset--color--vivid-red)", "color", "vivid-red", true},
		{"var(--wp--preset--font-family--system-font)", "font-family", "system-font", true},
		{"var(--wp--preset--spacing--40)", "spacing", "40", true},
		{"var:preset|spacing|50", "spacing", "50", true},
		{"var:preset|color|", "", "", false},
		{"var:preset|color", "", "", false},
		{"var:preset||x", "", "", false},
		{"var(--wp--preset--color--x", "", "", false},
		{"var(--wp--preset--color--x, red)", "", "", false},
		{"var(--wp--preset--color)", "", "", false},
		{"calc(var(--wp--preset--spacing--40) * 2)", "", "", false},
		{"var:preset|color|a b", "", "", false},
		{"#fff", "", "", false},
		{"", "", "", false},
	} {
		kind, slug, ok := presetRef(tt.in)
		if kind != tt.kind || slug != tt.slug || ok != tt.ok {
			t.Errorf("presetRef(%q) = %q, %q, %t, want %q, %q, %t", tt.in, kind, slug, ok, tt.kind, tt.slug, tt.ok)
		}
	}
}

func TestTextAlign(t *testing.T) {
	for _, tt := range []struct {
		attrs          string
		aligned, other string
	}{
		{`{}`, "", ""},
		{`{"textAlign":"center"}`, "center", ""},
		{`{"align":"right"}`, "right", ""},
		{`{"style":{"typography":{"textAlign":"left"}}}`, "left", ""},
		{`{"style":{"typography":{"textAlign":"center"}},"textAlign":"right","align":"left"}`, "center", ""},
		{`{"textAlign":"right","align":"left"}`, "right", ""},
		{`{"textAlign":"justify"}`, "", "justify"},
		{`{"align":"justify"}`, "", "justify"},
		{`{"textAlign":"start"}`, "", "start"},
		{`{"align":"wide"}`, "", ""},
		{`{"align":"full"}`, "", ""},
		{`{"textAlign":"","align":"center"}`, "center", ""},
		{`{"textAlign":5,"align":"center"}`, "center", ""},
		{`{"align":"wide","textAlign":"right"}`, "right", ""},
		{`{"style":{"typography":{"textAlign":"justify"}},"textAlign":"center"}`, "", "justify"},
	} {
		a, ok := wpblock.DecodeAttrs(tt.attrs)
		if !ok {
			t.Fatalf("DecodeAttrs(%q) failed", tt.attrs)
		}
		if aligned, other := textAlign(a); aligned != tt.aligned || other != tt.other {
			t.Errorf("textAlign(%s) = %q, %q, want %q, %q", tt.attrs, aligned, other, tt.aligned, tt.other)
		}
	}
}

func FuzzProps(f *testing.F) {
	for _, seed := range []string{
		`{}`, `{"textColor":"vivid-red","className":"is-style-fancy a b"}`, `{"style":{"color":{"text":"var:preset|color|gone","background":"#fff"},"spacing":{"padding":"1rem","blockGap":{"top":"1px"}}}}`,
		`{"style":{"border":{"top":{"width":"1px"},"radius":{"topLeft":"1px"}},"typography":{"fontWeight":"bold","lineHeight":"1.5","fontSize":12}}}`,
		`{"layout":{"type":"flex"},"style":{"spacing":{"blockGap":{"left":"2px"}},"elements":{"link":{"color":{"text":"red"}}}}}`,
		`{"style":"x"}`, `{"style":[]}`, `{"className":5,"textColor":7,"fontSize":null}`, `{"style":{"spacing":{"padding":{"top":{"a":1}}}}}`,
		`{"style":{"typography":{"fontWeight":1e999,"lineHeight":-0}}}`, `{"className":"b-12345678 hl-0123abcd 日本 a.b"}`,
	} {
		for which := range uint8(6) {
			f.Add(seed, which)
		}
	}
	types := []content.BlockType{content.BlockParagraph, content.BlockGroup, content.BlockColumns, content.BlockButton, content.BlockEmbed, content.BlockListItem}
	f.Fuzz(func(t *testing.T, attrs string, which uint8) {
		a, ok := wpblock.DecodeAttrs(attrs)
		if !ok || a.Kind != wpblock.Object {
			t.Skip()
		}
		c := richConv(bodyImport(), "", styleSettings())
		root := &html.Node{Type: html.ElementNode, Namespace: html.HTML, Name: "p", Attrs: []html.Attr{{Name: "id", Value: "x"}}}
		b := wpblock.Block{Name: "core/paragraph", Attrs: a}
		typ := types[int(which)%len(types)]
		got := c.props(&b, root, typ, noteAt{})
		if problem := styleProblem(got.style); problem != "" {
			t.Errorf("props(%s) has a style that is not valid: %s", attrs, problem)
		}
		for _, name := range []string{got.variant, got.className, got.anchor} {
			if name != "" && content.CheckName(name) != nil {
				t.Errorf("props(%s) has the name %q", attrs, name)
			}
		}
		if content.ReservedClass(got.className) {
			t.Errorf("props(%s) has the reserved class %q", attrs, got.className)
		}
		if typ == content.BlockListItem && got != (props{}) {
			t.Errorf("props(%s) of a part = %+v", attrs, got)
		}
		if !typ.AllowsBlockGap() && got.style.BlockGap != (content.Length{}) {
			t.Errorf("props(%s) has a gap for %s", attrs, typ)
		}
	})
}
