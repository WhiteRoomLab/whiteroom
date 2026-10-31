package render

import (
	"cmp"
	"fmt"
	"html"
	"maps"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// conceptRichTextJSON is the example in CONCEPT.md 6.5.2.
const conceptRichTextJSON = `[
  { "type": "text", "text": "当社の" },
  { "type": "text", "text": "事業内容", "marks": [{ "type": "bold" }] },
  { "type": "text", "text": "をご紹介します。" },
  { "type": "hardBreak" },
  { "type": "text", "text": "サービス一覧", "marks": [{ "type": "link", "attrs": { "entry": "ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3" } }] }
]`

// richElements are the elements of the marks, in the order in which they nest, outermost first.
var richElements = []string{"a", "mark", "strong", "em", "u", "s", "sub", "sup", "code"}

// markElements are the elements of the types of marks.
var markElements = map[content.MarkType]string{
	content.MarkLink: "a", content.MarkHighlight: "mark", content.MarkBold: "strong", content.MarkItalic: "em",
	content.MarkUnderline: "u", content.MarkStrike: "s", content.MarkSubscript: "sub", content.MarkSuperscript: "sup",
	content.MarkCode: "code",
}

var (
	// The start tags of links and highlights in rich text, whose attribute values are checked further.
	richLinkTag = regexp.MustCompile(`^<a href="([^"'<>]*)"(?: target="_blank")?(?: rel="([^"'<>]+)")?>$`)
	richMarkTag = regexp.MustCompile(`^<mark(?: class="(?:is-variant-([^"'<>]+)|hl-[0-9a-f]{8})")?>$`)
	// A start tag with its attributes, whose values are in double quotes, and one of the attributes.
	htmlStartTag = regexp.MustCompile(`^<([a-z][a-z0-9]*)((?: [a-z][a-z-]*(?:="[^"'<>]*")?)*)>$`)
	htmlAttr     = regexp.MustCompile(` ([a-z][a-z-]*)(?:="([^"'<>]*)")?`)
)

// The escapes that the renderer writes in text and in attribute values, and those that html/template writes in both.
var (
	textEscapes     = []string{"&amp;", "&lt;", "&gt;"}
	attrEscapes     = []string{"&amp;", "&lt;", "&gt;", "&#34;", "&#39;"}
	templateEscapes = []string{"&amp;", "&lt;", "&gt;", "&#34;", "&#39;", "&#43;"}
)

// urlAttrs are the attributes whose values are URLs.
var urlAttrs = []string{"action", "href", "src"}

// voidElements are the elements of HTML that have no end tag.
var voidElements = []string{
	"area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "source", "track", "wbr",
}

// escaped reports whether every "&" of s starts one of escapes.
func escaped(s string, escapes []string) bool {
	n := 0
	for _, e := range escapes {
		n += strings.Count(s, e)
	}
	return n == strings.Count(s, "&")
}

// escapeAt reports whether s has a "%" and two hex digits at i.
func escapeAt(s string, i int) bool {
	return s[i] == '%' && i+2 < len(s) && strings.IndexByte(hexDigits, s[i+1]) >= 0 &&
		strings.IndexByte(hexDigits, s[i+2]) >= 0
}

// encoded reports whether s has only urlBytes and "%" followed by two hex digits, as encodeURL writes.
func encoded(s string) bool {
	for i := 0; i < len(s); i++ {
		if escapeAt(s, i) {
			i += 2
		} else if strings.IndexByte(urlBytes, s[i]) < 0 {
			return false
		}
	}
	return true
}

// unescapeURL returns s with each "%" followed by two hex digits replaced by the byte that they write.
func unescapeURL(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if escapeAt(s, i) {
			n, _ := strconv.ParseUint(s[i+1:i+3], 16, 8)
			b.WriteByte(byte(n))
			i += 2
		} else {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// htmlTokens splits s, HTML, into its tags, each from a "<" to the next ">", and the texts between them.
func htmlTokens(s string) ([]string, error) {
	parts := strings.Split(s, "<")
	tokens := []string{parts[0]}
	for _, p := range parts[1:] {
		tag, text, ok := strings.Cut(p, ">")
		if !ok {
			return nil, fmt.Errorf("%q is not a tag", "<"+p)
		}
		tokens = append(tokens, "<"+tag+">", text)
	}
	return slices.DeleteFunc(tokens, func(s string) bool { return s == "" }), nil
}

// checkHTML returns an error if s, the HTML of blocks, is not well formed: every element that is not void is closed,
// in order; the text and the attribute values are escaped as the renderer and html/template escape them; and URLs are
// encoded as encodeURL encodes them.
func checkHTML(s string) error {
	tokens, err := htmlTokens(s)
	if err != nil {
		return err
	}
	var open []string
	for _, tok := range tokens {
		switch {
		case tok[0] != '<':
			if strings.Contains(tok, ">") || !escaped(tok, templateEscapes) {
				return fmt.Errorf("the text %q is not escaped", tok)
			}
		case strings.HasPrefix(tok, "</"):
			if len(open) == 0 || tok != "</"+open[len(open)-1]+">" {
				return fmt.Errorf("%s closes no open element", tok)
			}
			open = open[:len(open)-1]
		default:
			m := htmlStartTag.FindStringSubmatch(tok)
			if m == nil {
				return fmt.Errorf("%s is not a start tag", tok)
			}
			for _, a := range htmlAttr.FindAllStringSubmatch(m[2], -1) {
				if !escaped(a[2], templateEscapes) {
					return fmt.Errorf("%s has the attribute %s, which is not escaped", tok, a[1])
				}
				if slices.Contains(urlAttrs, a[1]) && !encoded(html.UnescapeString(a[2])) {
					return fmt.Errorf("%s has the attribute %s, which is not encoded", tok, a[1])
				}
			}
			if !slices.Contains(voidElements, m[1]) {
				open = append(open, m[1])
			}
		}
	}
	if len(open) > 0 {
		return fmt.Errorf("<%s> is not closed", open[len(open)-1])
	}
	return nil
}

// richTag is an element in the HTML of rich text: its name and its start tag.
type richTag struct{ name, start string }

// richPiece is a text or a <br> in the HTML of rich text, with the elements that are open around it, outermost first,
// and the fewest elements that were open since the piece before it.
type richPiece struct {
	text   string // unescaped; "" for a <br>
	open   []richTag
	fewest int
}

// checkRichHTML returns the texts and the <br> of s, the HTML of rich text in a theme whose highlight has the variants
// highlights. It returns an error if s has other tags than <br> and those of marks, if the elements do not nest in the
// order of richElements or are not closed, or if the text, the attribute values or the hrefs are not escaped and
// encoded as the renderer writes them.
func checkRichHTML(s string, highlights map[string]bool) ([]richPiece, error) {
	tokens, err := htmlTokens(s)
	if err != nil {
		return nil, err
	}
	var pieces []richPiece
	var open []richTag
	fewest := 0
	for _, tok := range tokens {
		switch {
		case tok[0] != '<':
			if strings.Contains(tok, ">") || !escaped(tok, textEscapes) {
				return nil, fmt.Errorf("the text %q is not escaped", tok)
			}
			pieces = append(pieces, richPiece{html.UnescapeString(tok), slices.Clone(open), fewest})
			fewest = len(open)
		case tok == "<br>":
			pieces = append(pieces, richPiece{"", slices.Clone(open), fewest})
			fewest = len(open)
		case strings.HasPrefix(tok, "</"):
			if len(open) == 0 || tok != "</"+open[len(open)-1].name+">" {
				return nil, fmt.Errorf("%s closes no open element", tok)
			}
			open = open[:len(open)-1]
			fewest = min(fewest, len(open))
		default:
			name, err := richStart(tok, highlights)
			if err != nil {
				return nil, err
			}
			if n := len(open); n > 0 && slices.Index(richElements, name) <= slices.Index(richElements, open[n-1].name) {
				return nil, fmt.Errorf("%s is inside %s", tok, open[n-1].start)
			}
			open = append(open, richTag{name, tok})
		}
	}
	if len(open) > 0 {
		return nil, fmt.Errorf("%s is not closed", open[len(open)-1].start)
	}
	return pieces, nil
}

// richStart returns the element of tag, the start tag of a mark in a theme whose highlight has the variants highlights,
// or an error if it is not one.
func richStart(tag string, highlights map[string]bool) (string, error) {
	if m := richLinkTag.FindStringSubmatch(tag); m != nil {
		if !escaped(m[1], attrEscapes) || !escaped(m[2], attrEscapes) || !encoded(html.UnescapeString(m[1])) {
			return "", fmt.Errorf("%s is not escaped, or its href is not encoded", tag)
		}
		return "a", nil
	}
	if m := richMarkTag.FindStringSubmatch(tag); m != nil {
		if m[1] != "" && !highlights[m[1]] {
			return "", fmt.Errorf("%s has a variant that the theme does not have", tag)
		}
		return "mark", nil
	}
	switch tag {
	case "<strong>", "<em>", "<u>", "<s>", "<sub>", "<sup>", "<code>":
		return tag[1 : len(tag)-1], nil
	}
	return "", fmt.Errorf("%s is not the start tag of a mark", tag)
}

// markNames returns the elements of ms, the marks of a text that has the warnings ws when it is rendered alone, in the
// order of richElements: of the marks of each type the first, and a link only if it has no warning.
func markNames(ms []content.Mark, ws []Warning) []string {
	var names []string
	for j, m := range ms {
		if slices.ContainsFunc(ms[:j], func(x content.Mark) bool { return x.Type == m.Type }) {
			continue
		}
		path := "/0/marks/" + strconv.Itoa(j)
		if m.Type != content.MarkLink || !slices.ContainsFunc(ws, func(w Warning) bool { return w.Path == path }) {
			names = append(names, markElements[m.Type])
		}
	}
	slices.SortFunc(names, func(a, b string) int {
		return cmp.Compare(slices.Index(richElements, a), slices.Index(richElements, b))
	})
	return names
}

// checkRichText checks RichText(rt) against each text of rt rendered alone, whose elements are those of its marks
// (markNames). Each text is in the elements that it has alone. From one text to the next, the elements that both have
// from the outermost on stay open, and the others close before the <br> of the hardBreaks between the texts and open
// after them. The warnings are those of the texts alone.
func checkRichText(r *Renderer, rt content.RichText) error {
	out, _, warnings := r.RichText(rt)
	pieces, err := checkRichHTML(string(out), r.highlights)
	if err != nil {
		return fmt.Errorf("%q: %w", out, err)
	}
	var wantWarnings []Warning
	var before []richTag   // the elements of the text before
	var breaks []richPiece // the <br> since the text before
	k, off := 0, 0         // the next text is in pieces[k] from off
	// between checks what is between the text before and the next one, whose elements are after: the <br> of breaks,
	// and fewest, the fewest elements that were open before the next text.
	between := func(after []richTag, fewest int) error {
		shared := 0
		for shared < len(before) && shared < len(after) && before[shared] == after[shared] {
			shared++
		}
		for _, b := range breaks {
			if b.fewest != shared || len(b.open) != shared {
				return fmt.Errorf("%q: a <br> is in %v between %v and %v", out, b.open, before, after)
			}
		}
		if fewest != shared {
			return fmt.Errorf("%q: %d elements stay open between %v and %v, want %d", out, fewest, before, after, shared)
		}
		return nil
	}
	for i, n := range rt {
		switch n.Type {
		case content.InlineHardBreak:
			if off > 0 || k == len(pieces) || pieces[k].text != "" {
				return fmt.Errorf("%q: no <br> for /%d", out, i)
			}
			breaks = append(breaks, pieces[k])
			k++
		case content.InlineText:
			text := strings.ReplaceAll(n.Text, "\x00", string(utf8.RuneError))
			alone, _, ws := r.RichText(rt[i : i+1])
			ps, err := checkRichHTML(string(alone), r.highlights)
			if err != nil {
				return fmt.Errorf("/%d alone: %q: %w", i, alone, err)
			}
			if len(ps) != 1 || ps[0].text != text {
				return fmt.Errorf("/%d alone: %q is not one text", i, alone)
			}
			elements := ps[0].open
			var names []string
			for _, e := range elements {
				names = append(names, e.name)
			}
			if want := markNames(n.Marks, ws); !slices.Equal(names, want) {
				return fmt.Errorf("/%d alone: %q has %q, want %q", i, alone, names, want)
			}
			for _, w := range ws {
				w.Path = "/" + strconv.Itoa(i) + strings.TrimPrefix(w.Path, "/0")
				wantWarnings = append(wantWarnings, w)
			}
			if k == len(pieces) || !strings.HasPrefix(pieces[k].text[off:], text) || !slices.Equal(pieces[k].open, elements) {
				return fmt.Errorf("%q: the text of /%d is not next, in %v", out, i, elements)
			}
			if off == 0 {
				if err := between(elements, pieces[k].fewest); err != nil {
					return err
				}
			}
			before, breaks = elements, nil
			if off += len(text); off == len(pieces[k].text) {
				k, off = k+1, 0
			}
		}
	}
	if k < len(pieces) {
		return fmt.Errorf("%q: more than the texts and hardBreaks", out)
	}
	if err := between(nil, 0); err != nil {
		return err
	}
	if !slices.Equal(warnings, wantWarnings) {
		return fmt.Errorf("warnings %q, want those of the texts alone %q", warnings, wantWarnings)
	}
	for _, w := range warnings {
		if w.List != 0 || !strings.HasPrefix(w.Path, "/") || w.Msg == "" {
			return fmt.Errorf("warning %+v", w)
		}
	}
	return nil
}

// fuzzRenderer is a Renderer of the golden theme with the name.
type fuzzRenderer struct {
	theme string
	r     *Renderer
}

// FuzzRichText checks that RichText writes the HTML that checkRichText expects for any rich text that decodes, in the
// default theme in en with the base /blog and in the example theme in ja.
func FuzzRichText(f *testing.F) {
	add := func(rt content.RichText) {
		data, err := rt.MarshalJSON()
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	data, err := os.ReadFile("testdata/every-mark.json")
	if err != nil {
		f.Fatal(err)
	}
	var bs content.Blocks
	if err := bs.UnmarshalJSON(data); err != nil {
		f.Fatal(err)
	}
	for _, b := range bs {
		add(b.Text)
	}
	f.Add([]byte(conceptRichTextJSON))
	var (
		about  = linkMark(content.Link{Entry: aboutEntry, Anchor: "a b"})
		draft  = linkMark(content.Link{Entry: draftEntry})
		guide  = linkMark(content.Link{Media: guideMedia, NewTab: true, Rel: []content.Rel{content.RelNofollow, `"x'`}})
		search = linkMark(content.Link{Href: `https://example.com/?q="a b"&r=<'1'>+%41%zz`})
		marker = highlightMark(content.Highlight{Variant: "marker"})
		wave   = highlightMark(content.Highlight{Variant: "wave"})
	)
	for _, rt := range []content.RichText{
		nil,
		// Of the marks of each type, the first counts.
		{textNode("a", boldMark, draft, italicMark, boldMark, search, wave, marker), textNode("b", search, draft)},
		// Every mark on one text.
		{textNode("x", codeMark, superscriptMark, subscriptMark, strikeMark, underlineMark, italicMark, boldMark, marker, about)},
		// hardBreaks between texts that share some of their marks.
		{
			hardBreakNode, textNode("a", boldMark, italicMark), hardBreakNode, textNode("b", boldMark, underlineMark),
			hardBreakNode, hardBreakNode, textNode("c", underlineMark), textNode("d", guide, underlineMark), hardBreakNode,
			textNode("e", guide), textNode("f", about), hardBreakNode,
		},
		{textNode(`<b>&amp;"'` + string(rune(0)) + "</b>")},
	} {
		add(rt)
	}
	res := goldenResolver(f)
	themes := goldenThemes(f)
	renderers := []fuzzRenderer{
		{themes[0].name, newRenderer(f, themes[0].th, "en", "/blog", res)},
		{themes[1].name, newRenderer(f, themes[1].th, "ja", "", res)},
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var rt content.RichText
		if rt.UnmarshalJSON(data) != nil {
			return
		}
		for _, x := range renderers {
			if err := checkRichText(x.r, rt); err != nil {
				t.Errorf("RichText(%s) with the %s theme in %s: %v", data, x.theme, x.r.language, err)
			}
		}
	})
}

// FuzzDocument checks that Document renders any list of blocks that decodes, as both lists of a document, in each
// golden theme and site: without an error, as HTML that checkHTML accepts, and with the warnings of the first list
// again for the second, apart from those about duplicate block IDs: the blocks of the second list have the IDs of
// those of the first, so that its blocks with CSS have them too.
func FuzzDocument(f *testing.F) {
	for _, p := range goldenDocuments {
		data, err := os.ReadFile(p)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	for _, s := range []string{
		// Rows out of the order of the sections, and children of a table that are not rows.
		`[{"type": "table", "id": "b_000table", "attrs": {"caption": [{"type": "text", "text": "c"}]}, "content": [
			{"type": "tableRow", "attrs": {"section": "foot"}, "content": [{"type": "tableCell", "content": [{"type": "text", "text": "f"}]}]},
			{"type": "tableRow", "content": [{"type": "tableCell", "attrs": {"header": true, "colspan": 2}}]},
			{"type": "tableRow", "attrs": {"section": "head"}, "content": [{"type": "tableCell", "content": [{"type": "text", "text": "h"}]}]},
			{"type": "tableCell", "content": [{"type": "text", "text": "c"}]},
			` + paragraphJSON("b_0paragr1", "p") + `
		]}]`,
		// A list that holds a paragraph, and an item outside a list.
		`[
			{"type": "list", "id": "b_00list01", "attrs": {"ordered": true}, "content": [` + paragraphJSON("b_0paragr1", "x") + `]},
			{"type": "listItem", "content": [` + paragraphJSON("b_0paragr2", "y") + `]}
		]`,
		`[
			{"type": "html", "id": "b_0000html", "attrs": {"html": "<p>x</p>"}},
			{"type": "legacy", "id": "b_00legacy", "attrs": {"html": "[gallery]", "reason": "shortcode gallery"}}
		]`,
		// Columns in columns, and headings with one ID.
		`[{"type": "columns", "id": "b_columns1", "content": [{"type": "column", "id": "b_0column1", "content": [
			{"type": "columns", "id": "b_columns2", "content": [{"type": "column", "id": "b_0column2", "content": [` +
			headingJSON("b_00head01", "a") + `]}]},
			` + headingJSON("b_00head01", "b") + `,
			{"type": "group", "id": "b_000group", "anchor": "b_00head01-2", "attrs": {"tagName": "aside", "label": "<l>"}}
		]}]}]`,
		// Numeric attributes with several values each, which byte mutations change into others.
		`[
			{"type": "heading", "id": "b_0heading", "attrs": {"level": 2}, "content": [{"type": "text", "text": "2"}]},
			{"type": "heading", "id": "b_0heading", "attrs": {"level": 3}, "content": [{"type": "text", "text": "3"}]},
			{"type": "heading", "id": "b_0heading", "attrs": {"level": 4}, "content": [{"type": "text", "text": "4"}]},
			{"type": "heading", "id": "b_0heading", "attrs": {"level": 5}, "content": [{"type": "text", "text": "5"}]},
			{"type": "heading", "id": "b_0heading", "attrs": {"level": 6}, "content": [{"type": "text", "text": "6"}]}
		]`,
		`[
			{"type": "list", "attrs": {"ordered": true, "start": 1}, "content": [{"type": "listItem"}]},
			{"type": "list", "attrs": {"ordered": true, "start": 9}, "content": [{"type": "listItem"}]},
			{"type": "list", "attrs": {"ordered": true, "start": 10}, "content": [{"type": "listItem"}]},
			{"type": "list", "attrs": {"ordered": true, "start": 100}, "content": [{"type": "listItem"}]}
		]`,
		`[{"type": "table", "content": [{"type": "tableRow", "content": [
			{"type": "tableCell", "attrs": {"colspan": 1, "rowspan": 1}},
			{"type": "tableCell", "attrs": {"colspan": 2, "rowspan": 2}},
			{"type": "tableCell", "attrs": {"header": true, "colspan": 1000, "rowspan": 1000}}
		]}]}]`,
		`[
			{"type": "group", "attrs": {"layout": {"type": "grid", "columns": 1}}, "content": [{"type": "paragraph"}]},
			{"type": "group", "attrs": {"layout": {"type": "grid", "columns": 16}}, "content": [{"type": "paragraph"}]}
		]`,
		embedJSON("vimeo", "https://vimeo.com/76979871/8272103f6e"),
		embedJSON("x", "https://x.com/jack/status/20?s=20&t=a'b+c"),
		embedJSON("googleMaps", mapsURL),
		embedJSON("tiktok", "https://www.tiktok.com/"),
		imageJSON(`{"media": "` + guideMedia + `", "link": {"href": "javascript:alert(1)"}}`),
		imageJSON(`{"media": "` + nilMedia + `"}`),
		fileJSON(`{"media": "` + missingMedia + `"}`),
		formJSON(escapingForm),
		formJSON(typesForm),
		formJSON(nilForm),
	} {
		f.Add([]byte(s))
	}
	// Every type of block, without anything else.
	var empty []string
	for _, bt := range append(slices.Sorted(maps.Keys(defaultTemplates)), content.BlockHTML, content.BlockLegacy) {
		empty = append(empty, `{"type": "`+string(bt)+`"}`)
	}
	f.Add([]byte("[" + strings.Join(empty, ", ") + "]"))
	res := goldenResolver(f)
	// Also the forms of TestForm, and a medium that the resolver finds as nil.
	maps.Copy(res.forms, testForms)
	res.media[nilMedia] = testMedium{nil, "/media/nil.png"}
	var renderers []fuzzRenderer
	for _, gt := range goldenThemes(f) {
		for _, site := range goldenSites {
			renderers = append(renderers, fuzzRenderer{gt.name, newRenderer(f, gt.th, site.language, site.base, res)})
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var bs content.Blocks
		if bs.UnmarshalJSON(data) != nil {
			return
		}
		for _, x := range renderers {
			lists, _, warnings, err := x.r.Document(bs, bs)
			if err != nil || len(lists) != 2 {
				t.Fatalf("Document() of %s twice with the %s theme in %s = %d lists, %v",
					data, x.theme, x.r.language, len(lists), err)
			}
			for i, s := range lists {
				if err := checkHTML(string(s)); err != nil {
					t.Errorf("Document() of %s twice with the %s theme in %s, list %d:\n%s\n%v",
						data, x.theme, x.r.language, i, s, err)
				}
			}
			var byList [2][]Warning // without List and the warnings about duplicate block IDs
			ok := slices.IsSortedFunc(warnings, func(a, b Warning) int { return cmp.Compare(a.List, b.List) })
			for _, w := range warnings {
				if w.List < 0 || w.List > 1 || !strings.HasPrefix(w.Path, "/") || w.Msg == "" {
					ok = false
				} else if !strings.HasPrefix(w.Msg, "duplicate block ID ") {
					byList[w.List] = append(byList[w.List], Warning{Path: w.Path, Msg: w.Msg})
				}
			}
			if !ok || !slices.Equal(byList[0], byList[1]) {
				t.Errorf("Document() of %s twice with the %s theme in %s: warnings %q, want those of the first list twice",
					data, x.theme, x.r.language, warnings)
			}
		}
	})
}

// FuzzEncodeURL checks that encodeURL returns what checkEncoded expects, that it keeps an encoded URL as it is, and that
// url.PathUnescape gives s back with its escapes decoded.
func FuzzEncodeURL(f *testing.F) {
	for _, s := range []string{
		"", "/about/", "/日記/", "/a b/", `"`, "'", `"'"`, "%", "%4", "%zz", "%41", "%%41", "%e6%97%a5", "100%", "%4%41",
		"!#$&*+,/:;=?@[]", "-._~", "()<>\\^`{|}", "\x00\x01\x1f\x7f", "\xff", "\xe6\x97", "\xed\xa0\x80",
		"https://例え.jp/パス?q=a b#c", `javascript:alert("x")`, "mailto:info@example.com?subject=お問い合わせ",
		"tel:+81-3-1234-5678",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := encodeURL(s)
		checkEncoded(t, s, out)
		if encoded(s) && out != s {
			t.Errorf("encodeURL(%q) = %q, want it as it is", s, out)
		}
		if got, err := url.PathUnescape(out); err != nil || got != unescapeURL(s) {
			t.Errorf("PathUnescape(%q) = %q, %v, want %q", out, got, err, unescapeURL(s))
		}
	})
}
