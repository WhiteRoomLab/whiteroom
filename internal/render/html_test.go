package render

import (
	"fmt"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/sanitize"
)

// htmlTheme is the settings file of a theme with the color primary, whose html blocks have the variant box and whose
// legacy blocks have old.
const htmlTheme = "schemaVersion: 1\npresets:\n  color:\n    primary: \"#1e73be\"\n" +
	"blocks:\n  html:\n    variants:\n      box: ボックス\n  legacy:\n    variants:\n      old: 旧\n"

// htmlBlock returns a block of type t, html or legacy, with the ID id and the HTML s.
func htmlBlock(t content.BlockType, id, s string) content.Block {
	return content.Block{Type: t, ID: id, Attrs: content.Attrs{HTML: s}}
}

// TestHTMLBlocks checks html and legacy blocks: their HTML sanitized by the policy of their type, alone or in a div
// element with the anchor and the class of the block, and the changes of sanitizing as warnings.
func TestHTMLBlocks(t *testing.T) {
	const media = `<iframe src="https://www.youtube.com/embed/x" title="v" allowfullscreen></iframe>` +
		`<video src="/v.mp4" controls><track src="/v.vtt" kind="captions"></video><p>a</p>`
	anchored := htmlBlock(content.BlockHTML, "b_0000html", "<p>a</p>\n")
	anchored.Anchor = "custom"
	styled := htmlBlock(content.BlockHTML, "b_000html2", "<p>b</p>")
	styled.Style.TextColor.Preset = "primary"
	classed := htmlBlock(content.BlockLegacy, "b_00legacy", `[gallery ids="1,2"]`)
	classed.Variant, classed.ClassName = "old", "imported"
	every := htmlBlock(content.BlockHTML, "b_000html3", "<p>c</p>")
	every.Anchor, every.Variant, every.ClassName = "every", "box", "note"
	every.Style.TextColor.Value = "#111"
	inner := htmlBlock(content.BlockHTML, "b_000html4", "<p>d</p>")
	inner.Anchor = "inner"
	emptyAnchored := htmlBlock(content.BlockHTML, "b_000html6", "<!-- c --><style>p { color: red }</style>")
	emptyAnchored.Anchor = "empty"
	emptyClassed := htmlBlock(content.BlockLegacy, "b_00legacy", "<!-- only a comment -->\n")
	emptyClassed.ClassName = "x"
	escaping := htmlBlock(content.BlockHTML, "b_0000html", "<p>a</p>")
	escaping.Anchor, escaping.ClassName = `a"b<c`, "x'y&z"
	ordered := htmlBlock(content.BlockHTML, "b_x", `<p onclick="a()" style="color: red">x</p><p onclick="b()">y</p>`+
		`<font color="red">z</font><script>s()</script><p onclick="c()">w</p>`)
	ordered.Variant = "nope"
	ordered.Style.TextColor.Preset = "missing"
	ordered.Style.BackgroundColor.Value = "#111"
	limited := htmlBlock(content.BlockHTML, "b_0000html", strings.Repeat("<div>", 10000)+strings.Repeat("</p>", 10000))
	limited.Anchor, limited.Variant, limited.ClassName = "big", "nope", "x"
	limited.Style.TextColor.Value = "#111"
	paragraph := func(id, s string) content.Block {
		return content.Block{Type: content.BlockParagraph, ID: id, Variant: "nope", Text: content.RichText{textNode(s)}}
	}
	const noVariant = `variant: the theme has no paragraph variant "nope"; rendered without its class`
	tests := []blockTest{
		{
			name: "html keeps iframes and media, legacy does not",
			in: content.Blocks{
				htmlBlock(content.BlockHTML, "b_0000html", media), htmlBlock(content.BlockLegacy, "b_00legacy", media),
			},
			want: `<iframe src="https://www.youtube.com/embed/x" title="v" allowfullscreen=""></iframe>` +
				`<video src="/v.mp4" controls=""><track src="/v.vtt" kind="captions"></video><p>a</p>` + "\n<p>a</p>",
			warnings: []string{
				"/1/attrs/html: removed <iframe> element and its content",
				"/1/attrs/html: removed <video> element and its content",
			},
		},
		{
			name: "shortcodes are text",
			in: content.Blocks{htmlBlock(content.BlockLegacy, "b_00legacy",
				`[gallery ids="1,2"]`+"\n"+`<p>[caption id="c"]<img src="/a.jpg" alt="a"> A &amp; B[/caption]</p>`)},
			want: `[gallery ids="1,2"]` + "\n" + `<p>[caption id="c"]<img src="/a.jpg" alt="a"> A &amp; B[/caption]</p>`,
		},
		{
			name: "wrappers",
			in: content.Blocks{
				anchored, styled, classed, every,
				{Type: content.BlockGroup, ID: "b_000group", Children: content.Blocks{inner}},
				htmlBlock(content.BlockHTML, "b_000html5", "\n\t<p>e</p>\n<p>f</p>\r\n "),
			},
			want: `<div id="custom">
<p>a</p>
</div>
<div class="b-000html2">
<p>b</p>
</div>
<div class="is-variant-old imported">
[gallery ids="1,2"]
</div>
<div id="every" class="b-000html3 is-variant-box note">
<p>c</p>
</div>
<div>
<div id="inner">
<p>d</p>
</div>
</div>
<p>e</p>
<p>f</p>`,
			css: ".b-000html2 {\n  color: var(--color-primary) !important;\n}\n\n" +
				".b-000html3 {\n  color: #111 !important;\n}\n",
		},
		{
			name: "empty",
			in: content.Blocks{
				htmlBlock(content.BlockHTML, "b_0000html", "<script>alert(1)</script>"),
				emptyAnchored,
				emptyClassed,
				htmlBlock(content.BlockHTML, "b_000html7", ""),
				htmlBlock(content.BlockLegacy, "b_0legacy2", "\n\n<!-- more -->\n"),
			},
			want: "<div id=\"empty\"></div>\n<div class=\"x\"></div>",
			warnings: []string{
				"/0/attrs/html: removed <script> element and its content",
				"/1/attrs/html: removed <style> element and its content",
			},
		},
		{
			name: "escaping",
			in:   content.Blocks{escaping},
			want: "<div id=\"a&#34;b&lt;c\" class=\"x&#39;y&amp;z\">\n<p>a</p>\n</div>",
		},
		{
			name: "warnings in document order",
			in:   content.Blocks{paragraph("b_0paragr1", "a"), ordered, paragraph("b_0paragr2", "b")},
			want: "<p>a</p>\n<p>x</p><p>y</p>z<p>w</p>\n<p>b</p>",
			warnings: []string{
				"/0/" + noVariant,
				`/1/id: invalid block ID "b_x"; rendered without its CSS`,
				`/1/variant: the theme has no html variant "nope"; rendered without its class`,
				`/1/style/textColor/preset: the theme has no color preset "missing"; ignored`,
				"/1/attrs/html: removed onclick attribute of <p> (3 times)",
				"/1/attrs/html: removed style attribute of <p>",
				"/1/attrs/html: removed <font> element, keeping its content",
				"/1/attrs/html: removed <script> element and its content",
				"/2/" + noVariant,
			},
		},
		{
			name:     "too complex",
			in:       content.Blocks{limited, paragraph("b_0paragr1", "a")},
			want:     "<p>a</p>",
			warnings: []string{"/0/attrs/html: the HTML is too complex to sanitize; not rendered", "/1/" + noVariant},
		},
	}
	checkBlocks(t, tests, htmlTheme, "ja", "", nil)
}

// TestHTMLURLs checks that the URLs of HTML are written as those of links: the paths of the site with the base, and
// encoded.
func TestHTMLURLs(t *testing.T) {
	in := content.Blocks{htmlBlock(content.BlockHTML, "b_0000html", `<p><a href="/about/">a</a> `+
		`<a href=" https://例え.jp/パス?q=a b&amp;r=1#c ">b</a> <a href="#top">c</a> <a href="mailto:info@example.com">d</a> `+
		`<a href="tel:+81-3-1234-5678">e</a> <a href="//evil.example/">f</a></p>`+
		`<img src="/media/製品.jpg" alt="製品"><blockquote cite="/quotes/1"><p>q</p></blockquote>`+
		`<video src="https://example.com/v.mp4" poster="/poster.jpg"><source src="/v.webm" type="video/webm"></video>`)}
	want := `<p><a href="/blog/about/">a</a> ` +
		`<a href="https://%E4%BE%8B%E3%81%88.jp/%E3%83%91%E3%82%B9?q=a%20b&amp;r=1#c">b</a> <a href="#top">c</a> ` +
		`<a href="mailto:info@example.com">d</a> <a href="tel:+81-3-1234-5678">e</a> <a>f</a></p>` +
		`<img src="/blog/media/%E8%A3%BD%E5%93%81.jpg" alt="製品"><blockquote cite="/blog/quotes/1"><p>q</p></blockquote>` +
		`<video src="https://example.com/v.mp4" poster="/blog/poster.jpg"><source src="/blog/v.webm" type="video/webm"></video>`
	warnings := []string{
		`/0/attrs/html: removed href attribute of <a>: invalid URL "//evil.example/": protocol-relative URLs are not allowed`,
	}
	tests := []blockTest{{name: "with a base", in: in, want: want, warnings: warnings}}
	checkBlocks(t, tests, htmlTheme, "en", "/blog", nil)
	tests = []blockTest{{name: "without a base", in: in, want: strings.ReplaceAll(want, `="/blog/`, `="/`), warnings: warnings}}
	checkBlocks(t, tests, htmlTheme, "ja", "", nil)
}

// TestHTMLAnchors checks that the ids of HTML that are anchors in the document already, of blocks anywhere in it, or
// of headings and HTML before them, are removed, and that the automatic anchors of headings after them avoid the ids
// that are kept.
func TestHTMLAnchors(t *testing.T) {
	body := decodeBlocks(t, `[
		`+headingJSON("b_00head01", "一")+`,
		{"type": "paragraph", "id": "b_0paragr1", "anchor": "intro"},
		{
			"type": "html", "id": "b_0000html", "anchor": "own",
			"attrs": {"html": "<h2 id=\"own\">a</h2><p id=\"intro\">b</p><p id=\"b_00head01\">c</p><p id=\"b_00head02\">d</p><p id=\"kept\">e</p><p id=\"kept\">f</p>"}
		},
		`+headingJSON("b_00head02", "二")+`
	]`)
	field := decodeBlocks(t, `[
		{"type": "legacy", "id": "b_00legacy", "attrs": {"html": "<p id=\"kept\">x</p><p id=\"b_00head02-2\">y</p><p id=\"later\">z</p><p id=\"new\">w</p>"}},
		{"type": "paragraph", "id": "b_0paragr2", "anchor": "later"}
	]`)
	r := newRenderer(t, testTheme(t, nil), "ja", "", nil)
	want := []string{
		`<h2 id="b_00head01">一</h2>
<p id="intro"></p>
<div id="own">
<h2>a</h2><p>b</p><p>c</p><p id="b_00head02">d</p><p id="kept">e</p><p>f</p>
</div>
<h2 id="b_00head02-2">二</h2>`,
		`<p>x</p><p>y</p><p>z</p><p id="new">w</p>
<p id="later"></p>`,
	}
	used := func(list int, path, el, id string) Warning {
		return Warning{List: list, Path: path, Msg: fmt.Sprintf("removed id attribute of <%s>: anchor %q is already used", el, id)}
	}
	checkDocument(t, r, []content.Blocks{body, field}, want, "", []Warning{
		{List: 0, Path: "/2/attrs/html", Msg: `removed id attribute of <p>: duplicate id "kept"`},
		used(0, "/2/attrs/html", "h2", "own"),
		used(0, "/2/attrs/html", "p", "intro"),
		used(0, "/2/attrs/html", "p", "b_00head01"),
		used(1, "/0/attrs/html", "p", "kept"),
		used(1, "/0/attrs/html", "p", "b_00head02-2"),
		used(1, "/0/attrs/html", "p", "later"),
	})
	// Rendered alone, the field keeps the ids that the body took.
	checkDocument(t, r, []content.Blocks{field}, []string{
		`<p id="kept">x</p><p id="b_00head02-2">y</p><p>z</p><p id="new">w</p>
<p id="later"></p>`,
	}, "", []Warning{used(0, "/0/attrs/html", "p", "later")})
}

func TestSanitizeError(t *testing.T) {
	const limit = "the HTML is too complex to sanitize; not rendered"
	for _, tt := range []struct {
		err  error
		want string
	}{
		{sanitize.ErrLimit, limit},
		{fmt.Errorf("parsing: %w", sanitize.ErrLimit), limit},
		{sanitize.ErrUnstable, "the HTML cannot be sanitized into a stable form; not rendered"},
	} {
		if got := sanitizeError(tt.err); got != tt.want {
			t.Errorf("sanitizeError(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}

// TestRichTextHTML checks that HTML in rich text is text, which RichText escapes: only that of html and legacy blocks
// is sanitized.
func TestRichTextHTML(t *testing.T) {
	r := newRenderer(t, testTheme(t, nil), "ja", "", nil)
	html, css, warnings := r.RichText(content.RichText{textNode(`<p id="x" onclick="f()">a</p><script>g()</script>`)})
	const want = `&lt;p id="x" onclick="f()"&gt;a&lt;/p&gt;&lt;script&gt;g()&lt;/script&gt;`
	if string(html) != want || css.rules != nil || warnings != nil {
		t.Errorf("RichText() = %q, %q, %q, want %q", html, css, warnings, want)
	}
}
