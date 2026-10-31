package render

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

var (
	boldMark        = content.Mark{Type: content.MarkBold}
	italicMark      = content.Mark{Type: content.MarkItalic}
	underlineMark   = content.Mark{Type: content.MarkUnderline}
	strikeMark      = content.Mark{Type: content.MarkStrike}
	codeMark        = content.Mark{Type: content.MarkCode}
	subscriptMark   = content.Mark{Type: content.MarkSubscript}
	superscriptMark = content.Mark{Type: content.MarkSuperscript}
	hardBreakNode   = content.Inline{Type: content.InlineHardBreak}
)

func textNode(s string, marks ...content.Mark) content.Inline {
	return content.Inline{Type: content.InlineText, Text: s, Marks: marks}
}

func linkMark(l content.Link) content.Mark {
	return content.Mark{Type: content.MarkLink, Link: &l}
}

func highlightMark(h content.Highlight) content.Mark {
	return content.Mark{Type: content.MarkHighlight, Highlight: &h}
}

// markerTheme is the settings file of a theme with the color accent, whose highlight has the variant marker.
const markerTheme = "schemaVersion: 1\npresets:\n  color:\n    accent: \"#e85d04\"\n" +
	"marks:\n  highlight:\n    variants:\n      marker: マーカー\n"

func TestRichText(t *testing.T) {
	var (
		about   = linkMark(content.Link{Entry: aboutEntry})
		example = linkMark(content.Link{Href: "https://example.com/"})
		draft   = linkMark(content.Link{Entry: draftEntry})
		marker  = highlightMark(content.Highlight{Variant: "marker"})
		wave    = highlightMark(content.Highlight{Variant: "wave"})
		accent  = highlightMark(content.Highlight{Color: content.Color{Preset: "accent"}})
		cream   = highlightMark(content.Highlight{BackgroundColor: content.Color{Value: "#fff4e5"}})
		blink   = content.Mark{Type: "blink"}
		nul     = string(rune(0))
		fffd    = string(utf8.RuneError)
	)
	const (
		unpublished = `entry "` + draftEntry + `" is not published; rendered without the link`
		noWave      = `the theme has no highlight variant "wave"; rendered without its class`
		accentCSS   = ".hl-4e6255fc {\n  color: var(--color-accent) !important;\n}\n"
		creamCSS    = ".hl-a70e621a {\n  background-color: #fff4e5 !important;\n}\n"
	)
	tests := []struct {
		name     string
		in       content.RichText
		want     string
		css      string
		warnings []string
	}{
		{name: "empty"},
		{name: "plain", in: content.RichText{textNode("当社の事業内容をご紹介します。")}, want: "当社の事業内容をご紹介します。"},
		{
			name: "escaping",
			in:   content.RichText{textNode(`a & b < c > d " e ' f ` + nul + " &amp;")},
			want: `a &amp; b &lt; c &gt; d " e ' f ` + fffd + " &amp;amp;",
		},
		{
			name: "each mark",
			in: content.RichText{
				textNode("太字", boldMark), textNode("斜体", italicMark), textNode("下線", underlineMark),
				textNode("取り消し線", strikeMark), textNode("コード", codeMark), textNode("H"), textNode("2", subscriptMark),
				textNode("O、x"), textNode("2", superscriptMark), textNode("強調", marker), textNode("リンク", example),
			},
			want: "<strong>太字</strong><em>斜体</em><u>下線</u><s>取り消し線</s><code>コード</code>H<sub>2</sub>O、x<sup>2</sup>" +
				`<mark class="is-variant-marker">強調</mark><a href="https://example.com/">リンク</a>`,
		},
		{
			name: "every mark on one text",
			in: content.RichText{textNode("x",
				codeMark, superscriptMark, subscriptMark, strikeMark, underlineMark, italicMark, boldMark, marker, example,
			)},
			want: `<a href="https://example.com/"><mark class="is-variant-marker"><strong><em><u><s><sub><sup><code>x` +
				"</code></sup></sub></s></u></em></strong></mark></a>",
		},
		{
			name: "italic and bold in either order",
			in:   content.RichText{textNode("a", italicMark, boldMark), textNode("、"), textNode("b", boldMark, italicMark)},
			want: "<strong><em>a</em></strong>、<strong><em>b</em></strong>",
		},
		{
			name: "marks that stay open",
			in: content.RichText{
				textNode("当社の", boldMark), textNode("事業", italicMark, boldMark), textNode("内容", boldMark),
				textNode("をご紹介します。"),
			},
			want: "<strong>当社の<em>事業</em>内容</strong>をご紹介します。",
		},
		{
			name: "one link over two texts",
			in:   content.RichText{textNode("サービス", about), textNode("一覧", linkMark(content.Link{Entry: aboutEntry}), boldMark)},
			want: `<a href="/about/">サービス<strong>一覧</strong></a>`,
		},
		{
			name: "a link around bold after bold",
			in:   content.RichText{textNode("詳しくは", boldMark), textNode("こちら", example, boldMark)},
			want: `<strong>詳しくは</strong><a href="https://example.com/"><strong>こちら</strong></a>`,
		},
		{
			name: "the same href with and without newTab",
			in: content.RichText{
				textNode("a", example), textNode("b", linkMark(content.Link{Href: "https://example.com/", NewTab: true})),
			},
			want: `<a href="https://example.com/">a</a><a href="https://example.com/" target="_blank" rel="noopener">b</a>`,
		},
		{
			name: "two links",
			in:   content.RichText{textNode("a", example), textNode("b", about)},
			want: `<a href="https://example.com/">a</a><a href="/about/">b</a>`,
		},
		{
			name: "attributes",
			in: content.RichText{textNode("a", linkMark(content.Link{
				Href: "https://example.com/?q=1&r='2'+3", NewTab: true, Rel: []content.Rel{content.Rel(`x"y'z<&>` + nul)},
			}))},
			want: `<a href="https://example.com/?q=1&amp;r=%272%27+3" target="_blank" rel="noopener x&#34;y&#39;z&lt;&amp;&gt;` +
				fffd + `">a</a>`,
		},
		{
			name: "hardBreaks",
			in: content.RichText{
				hardBreakNode, textNode("一行目", boldMark), hardBreakNode, textNode("二行目", boldMark), hardBreakNode,
				textNode("三行目"), hardBreakNode,
			},
			want: "<br><strong>一行目<br>二行目</strong><br>三行目<br>",
		},
		{name: "hardBreak first", in: content.RichText{hardBreakNode, textNode("a", boldMark)}, want: "<br><strong>a</strong>"},
		{name: "hardBreak at the end", in: content.RichText{textNode("a", boldMark), hardBreakNode}, want: "<strong>a</strong><br>"},
		{
			name: "hardBreak after a link",
			in:   content.RichText{textNode("About", about), hardBreakNode, textNode("draft")},
			want: `<a href="/about/">About</a><br>draft`,
		},
		{
			name: "hardBreak inside a link",
			in:   content.RichText{textNode("About", about), hardBreakNode, textNode("Us", about, boldMark)},
			want: `<a href="/about/">About<br><strong>Us</strong></a>`,
		},
		{
			name: "consecutive hardBreaks",
			in: content.RichText{
				textNode("a", boldMark), hardBreakNode, hardBreakNode, textNode("b", italicMark, boldMark), hardBreakNode,
				hardBreakNode,
			},
			want: "<strong>a<br><br><em>b</em></strong><br><br>",
		},
		{
			name: "hardBreak across nodes left out",
			in: content.RichText{
				textNode("a", boldMark), hardBreakNode, {Type: "image"}, textNode("", italicMark), textNode("b", draft, boldMark),
			},
			want:     "<strong>a<br>b</strong>",
			warnings: []string{`/2: unknown inline type "image"; left out`, "/4/marks/0: " + unpublished},
		},
		{
			name: "hardBreak with text and marks",
			in:   content.RichText{{Type: content.InlineHardBreak, Text: "x", Marks: []content.Mark{boldMark}}},
			want: "<br>",
		},
		{name: "highlight variant", in: content.RichText{textNode("a", marker)}, want: `<mark class="is-variant-marker">a</mark>`},
		{
			name:     "unknown highlight variant",
			in:       content.RichText{textNode("a", wave)},
			want:     "<mark>a</mark>",
			warnings: []string{"/0/marks/0: " + noWave},
		},
		{
			name: "colors",
			in:   content.RichText{textNode("a", accent)},
			want: `<mark class="hl-4e6255fc">a</mark>`,
			css:  accentCSS,
		},
		{
			name: "variant next to colors",
			in:   content.RichText{textNode("a", marker), textNode("b", cream)},
			want: `<mark class="is-variant-marker">a</mark><mark class="hl-a70e621a">b</mark>`,
			css:  creamCSS,
		},
		{
			name: "colors next to colors",
			in:   content.RichText{textNode("a", accent), textNode("b", cream), textNode("c", accent)},
			want: `<mark class="hl-4e6255fc">a</mark><mark class="hl-a70e621a">b</mark>` +
				`<mark class="hl-4e6255fc">c</mark>`,
			css: accentCSS + "\n" + creamCSS,
		},
		{
			name: "highlight without attributes",
			in:   content.RichText{textNode("a", content.Mark{Type: content.MarkHighlight})},
			want: "<mark>a</mark>",
		},
		{
			name:     "unpublished entry",
			in:       content.RichText{textNode("下書き", draft)},
			want:     "下書き",
			warnings: []string{"/0/marks/0: " + unpublished},
		},
		{
			name: "javascript",
			in:   content.RichText{textNode("x", linkMark(content.Link{Href: "javascript:alert(1)"}), boldMark)},
			want: "<strong>x</strong>",
			warnings: []string{
				`/0/marks/0: invalid href "javascript:alert(1)": scheme "javascript" is not allowed; rendered without the link`,
			},
		},
		{
			name:     "link without attributes",
			in:       content.RichText{textNode("x", content.Mark{Type: content.MarkLink})},
			want:     "x",
			warnings: []string{"/0/marks/0: link without a target; rendered without the link"},
		},
		{
			name:     "link left out between links",
			in:       content.RichText{textNode("a", example, boldMark), textNode("b", draft, boldMark), textNode("c", example)},
			want:     `<a href="https://example.com/"><strong>a</strong></a><strong>b</strong><a href="https://example.com/">c</a>`,
			warnings: []string{"/1/marks/0: " + unpublished},
		},
		{
			name: "unknown marks",
			in:   content.RichText{textNode("x", blink, boldMark, content.Mark{Type: "Bold"}, blink)},
			want: "<strong>x</strong>",
			warnings: []string{
				`/0/marks/0: unknown mark type "blink"; left out`,
				`/0/marks/2: unknown mark type "Bold"; left out`,
				`/0/marks/3: unknown mark type "blink"; left out`,
			},
		},
		{name: "duplicate bold", in: content.RichText{textNode("x", boldMark, italicMark, boldMark)}, want: "<strong><em>x</em></strong>"},
		{
			name:     "duplicate links",
			in:       content.RichText{textNode("x", example, about), textNode("y", draft, example)},
			want:     `<a href="https://example.com/">x</a>y`,
			warnings: []string{"/1/marks/0: " + unpublished},
		},
		{
			name:     "duplicate highlights",
			in:       content.RichText{textNode("x", marker, wave), textNode("y", wave, marker)},
			want:     `<mark class="is-variant-marker">x</mark><mark>y</mark>`,
			warnings: []string{"/1/marks/0: " + noWave},
		},
		{
			name:     "unknown inline type",
			in:       content.RichText{textNode("a", boldMark), {Type: "image", Text: "x"}, textNode("b", boldMark)},
			want:     "<strong>ab</strong>",
			warnings: []string{`/1: unknown inline type "image"; left out`},
		},
		{
			name: "empty texts",
			in: content.RichText{
				textNode("a", boldMark), textNode("", italicMark, blink, draft), textNode("b", boldMark), textNode(""),
			},
			want: "<strong>ab</strong>",
		},
		{
			name: "warnings in document order",
			in: content.RichText{
				textNode("a"),
				textNode("b", boldMark, wave, linkMark(content.Link{Entry: missingEntry}), blink),
				{Type: "image"},
				textNode("c", linkMark(content.Link{})),
			},
			want: "a<mark><strong>b</strong></mark>c",
			warnings: []string{
				"/1/marks/1: " + noWave,
				`/1/marks/2: entry "` + missingEntry + `" does not exist; rendered without the link`,
				`/1/marks/3: unknown mark type "blink"; left out`,
				`/2: unknown inline type "image"; left out`,
				"/3/marks/0: link without a target; rendered without the link",
			},
		},
	}
	r := newRenderer(t, testTheme(t, map[string]string{"theme.yaml": markerTheme}), "ja", "", linkResolver)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
			for _, w := range warnings {
				if w.List != 0 {
					t.Errorf("warning %q has List %d, want 0", w, w.List)
				}
			}
		})
	}
}

// TestRichTextPath checks the paths of the warnings of rich text in a block, and links with the base.
func TestRichTextPath(t *testing.T) {
	r := newRenderer(t, testTheme(t, nil), "en", "/blog", linkResolver)
	w := &walker{r: r, list: 1}
	in := content.RichText{
		textNode("About", linkMark(content.Link{Entry: aboutEntry, Anchor: "access"})),
		hardBreakNode,
		textNode("draft", linkMark(content.Link{Entry: draftEntry})),
		{Type: "image"},
	}
	const want = `<a href="/blog/about/#access">About</a><br>draft`
	if got := w.richText(in, "/4/attrs/caption"); got != want {
		t.Errorf("richText() =\n%s\nwant\n%s", got, want)
	}
	wantWarnings := []Warning{
		{List: 1, Path: "/4/attrs/caption/2/marks/0", Msg: `entry "` + draftEntry + `" is not published; rendered without the link`},
		{List: 1, Path: "/4/attrs/caption/3", Msg: `unknown inline type "image"; left out`},
	}
	if !slices.Equal(w.warnings, wantWarnings) {
		t.Errorf("warnings = %q, want %q", w.warnings, wantWarnings)
	}
}

func TestPlainText(t *testing.T) {
	tests := []struct {
		in   content.RichText
		want string
	}{
		{nil, ""},
		{
			content.RichText{
				textNode("当社の"), textNode("事業", boldMark), hardBreakNode, textNode("内容", linkMark(content.Link{Entry: draftEntry})),
			},
			"当社の事業 内容",
		},
		{content.RichText{hardBreakNode, textNode("a & <b>"), hardBreakNode, hardBreakNode}, " a & <b>  "},
		{content.RichText{textNode("a"), {Type: "image", Text: "x"}, textNode("b")}, "ab"},
	}
	for _, tt := range tests {
		if got := plainText(tt.in); got != tt.want {
			t.Errorf("plainText(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
