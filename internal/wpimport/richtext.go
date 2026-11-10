package wpimport

import (
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/html"
)

// richText turns the content of the element n, such as the p of a paragraph, into text with marks. verse is set for
// text that keeps its line breaks and spaces, such as that of a verse; otherwise the white space is collapsed as a
// browser does. What whiteroom has no counterpart of is left out, and noted at at: of the elements that are not marks,
// only the text is kept. The error is a legacy error, for the text of elements that a text cannot have, such as an
// image.
func (c *bodyConv) richText(n *html.Node, verse bool, at noteAt) (content.RichText, error) {
	return c.readRich(n, verse, false, at)
}

// plainText is the content of n as one line of text without marks, such as the label of a button.
func (c *bodyConv) plainText(n *html.Node, at noteAt) (string, error) {
	rt, err := c.readRich(n, false, true, at)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, in := range rt {
		if in.Type == content.InlineText {
			b.WriteString(in.Text)
		} else if s := b.String(); s != "" && !strings.HasSuffix(s, " ") {
			b.WriteByte(' ')
		}
	}
	s, _ := oneLine(b.String())
	return s, nil
}

// codeText is the content of n, in a pre, as the text of code: the text as it is written with LF as the line break, br as a
// line break, and the tags of the other elements left out, which are noted at at. Comments are not text.
func (c *bodyConv) codeText(n *html.Node, at noteAt) string {
	var b strings.Builder
	var kept orderedSet
	for d := n.FirstChild; d != nil; d = after(n, d, true) {
		if pc := c.pieceOf(d); pc != nil {
			// A pre that holds a shortcode is a legacy block before this, so the piece is a URL.
			b.WriteString(pc.url)
			continue
		}
		switch {
		case d.Type == html.TextNode:
			b.WriteString(d.Data)
		case d.Type != html.ElementNode:
		case d.Namespace == html.HTML && d.Name == "br":
			b.WriteByte('\n')
		case d.Namespace == html.HTML && d.Name == "wbr":
		default:
			kept.add(tagText(d.Name))
		}
	}
	c.noteItems(at, Changed, "kept only the text of ", "", kept.items...)
	s := strings.ReplaceAll(strings.ReplaceAll(b.String(), "\r\n", "\n"), "\r", "\n")
	return strings.ToValidUTF8(s, string(rune(0xFFFD)))
}

// orderedSet is a list of texts without repeats, in the order in which they were added.
type orderedSet struct {
	items []string
	seen  map[string]bool
}

func (s *orderedSet) add(item string) {
	if s.seen == nil {
		s.seen = make(map[string]bool)
	}
	if !s.seen[item] {
		s.seen[item] = true
		s.items = append(s.items, item)
	}
}

// richPieceBytes is what a piece of text and a break count in the budget of a text, besides the bytes of the text.
const richPieceBytes = 20

type tokenKind uint8

const (
	tokText     tokenKind = iota // text with marks
	tokBreak                     // a br
	tokBoundary                  // the start or the end of an element that sets its content apart, such as a div
)

type richToken struct {
	kind  tokenKind
	text  string
	marks []content.Mark // shared by the tokens read under the same element: do not change
}

// richMark is a mark with the tag of the element that gave it, for the report.
type richMark struct {
	content.Mark
	by string
}

// richFrame is an element that the reader is in: the marks from before it, whether it sets its content apart, and whether it is
// code.
type richFrame struct {
	node     *html.Node
	marks    []richMark
	plain    []content.Mark
	boundary bool
	code     bool
}

// richReader reads the content of an element into tokens, which assemble then makes text of. plain is set to leave out
// the marks.
type richReader struct {
	c     *bodyConv
	at    noteAt
	verse bool
	plain bool

	frames []richFrame
	marks  []richMark     // of the element being read
	cur    []content.Mark // the same, as the marks of a token
	tokens []richToken
	size   int // the bytes of text read so far, with richPieceBytes more for each piece of text and each break
	// codeDepth is how many pre and code elements the reader is in. In them, only the shortcodes that WordPress registers are
	// shortcodes.
	codeDepth int
	// consumed are the properties of the style of the element being read that its marks took.
	consumed []string
	// styleLost are the properties of the styles of the elements of the HTML of the classic editor that no mark took, by element.
	styleLost  []tagItems
	styleIndex map[string]int

	kept, removed, nested, blocking, colorsLost, shortcodes orderedSet
}

// tagItems are the things that are noted of the elements with the name tag, such as <span>.
type tagItems struct {
	tag   string
	items orderedSet
}

// Elements that are dropped with what they hold.
var droppedRich = map[string]bool{
	"script": true, "style": true, "template": true, "title": true, "noscript": true, "noembed": true, "noframes": true,
}

// Elements that hold something that is not text: text with one of them is a legacy block. (Those of other namespaces,
// svg and math, are too.)
var notRich = map[string]bool{
	"img": true, "picture": true, "svg": true, "math": true, "iframe": true, "video": true, "audio": true, "object": true,
	"embed": true, "canvas": true, "ruby": true, "input": true, "select": true, "textarea": true, "button": true,
	"meter": true, "progress": true, "map": true, "ul": true, "ol": true, "dl": true, "table": true, "hr": true,
}

// Elements that set their content apart from the text around them: only the text of them is kept, with a line break
// between.
var blockRich = map[string]bool{
	"p": true, "div": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "blockquote": true,
	"pre": true, "address": true, "figure": true, "figcaption": true, "section": true, "article": true, "aside": true,
	"header": true, "footer": true, "nav": true, "main": true, "details": true, "summary": true, "li": true, "dt": true,
	"dd": true, "center": true,
}

func (c *bodyConv) readRich(n *html.Node, verse, plain bool, at noteAt) (content.RichText, error) {
	r := &richReader{c: c, at: at, verse: verse, plain: plain}
	// Not recursive: HTML can nest as deep as it is long.
	for d := n.FirstChild; d != nil; {
		descend, err := r.enter(d)
		if err != nil {
			return nil, err
		}
		if r.size > maxRichBytes {
			return nil, legacyf("the text has more than %d bytes, counting %d for each piece of text and each break", maxRichBytes, richPieceBytes)
		}
		if descend && d.FirstChild != nil {
			d = d.FirstChild
			continue
		}
		for d != nil {
			r.leave(d)
			if d.NextSibling != nil {
				d = d.NextSibling
				break
			}
			if d = d.Parent; d == n {
				d = nil
			}
		}
	}
	if len(r.blocking.items) > 0 {
		return nil, legacyf("the text has %s, which rich text cannot have", strings.Join(r.blocking.items, ", "))
	}
	if len(r.shortcodes.items) > 0 {
		return nil, legacyf("%s", reasonShortcodes(r.shortcodes.items))
	}
	c.noteItems(at, Changed, "kept only the text of ", "", r.kept.items...)
	c.noteItems(at, Skipped, "removed ", " with their content", r.removed.items...)
	c.noteItems(at, Changed, "removed ", "", r.nested.items...)
	c.noteItems(at, Skipped, "the colors of ", " are not imported: a highlight with a variant has no colors", r.colorsLost.items...)
	for _, l := range r.styleLost {
		keys := make([]string, len(l.items.items))
		for i, item := range l.items.items {
			keys[i] = keyText(item)
		}
		c.noteItems(at, Skipped, "lost ", " of "+l.tag+lostTail, keys...)
	}
	return r.assemble(), nil
}

// enter reads the start of n, and reports whether to go on to its children. The error is a legacy error, from a link (see
// link).
func (r *richReader) enter(n *html.Node) (bool, error) {
	if n.Type == html.TextNode {
		if data := r.textOf(n.Data); data != "" {
			r.size += len(data) + richPieceBytes
			if r.verse {
				// Each line of a verse is a piece and a break; a CR and an LF together are one line break.
				r.size += richPieceBytes * (strings.Count(data, "\n") + strings.Count(data, "\r") - strings.Count(data, "\r\n"))
			}
			r.tokens = append(r.tokens, richToken{kind: tokText, text: strings.ToValidUTF8(data, string(rune(0xFFFD))), marks: r.cur})
		}
		return false, nil
	}
	if n.Type != html.ElementNode {
		return false, nil
	}
	if pc := r.c.pieceOf(n); pc != nil {
		r.piece(pc)
		return false, nil
	}
	tag := tagText(n.Name)
	switch {
	case n.Namespace != html.HTML:
		r.blocking.add(tag)
		return false, nil
	case n.Name == "br":
		r.size += richPieceBytes
		r.tokens = append(r.tokens, richToken{kind: tokBreak})
		return false, nil
	case n.Name == "wbr":
		return false, nil
	case droppedRich[n.Name]:
		r.removed.add(tag)
		return false, nil
	case notRich[n.Name]:
		r.blocking.add(tag)
		return false, nil
	}
	frame := richFrame{node: n, marks: r.marks, plain: r.cur, boundary: blockRich[n.Name], code: n.Name == "pre" || n.Name == "code"}
	r.consumed = r.consumed[:0]
	if frame.boundary {
		r.kept.add(tag)
		r.tokens = append(r.tokens, richToken{kind: tokBoundary})
	} else {
		add, handled, err := r.marksOf(n, tag)
		if err != nil {
			return false, err
		}
		if len(add) > 0 {
			r.addMarks(add)
		} else if !handled && (n.Name != "span" || len(n.Attrs) > 0) {
			// A span with no attributes holds nothing that is lost.
			r.kept.add(tag)
		}
	}
	if r.c.inClassic {
		r.loseStyle(n, tag)
	}
	if frame.code {
		r.codeDepth++
	}
	r.frames = append(r.frames, frame)
	return true, nil
}

// maxStyleNotes is how many elements and how many properties of each a text keeps of what it notes of the styles that no mark
// took: a text can have any number, and the note names only the first few.
const maxStyleNotes = 100

// loseStyle notes the properties of the style of the element n, called tag, that no mark took.
func (r *richReader) loseStyle(n *html.Node, tag string) {
	style := attr(n, "style")
	if style == "" {
		return
	}
	for _, name := range styleNames(style) {
		if slices.Contains(r.consumed, name) {
			continue
		}
		i, ok := r.styleIndex[tag]
		if !ok {
			if len(r.styleLost) >= maxStyleNotes {
				return
			}
			if r.styleIndex == nil {
				r.styleIndex = make(map[string]int)
			}
			i = len(r.styleLost)
			r.styleIndex[tag] = i
			r.styleLost = append(r.styleLost, tagItems{tag: tag})
		}
		if len(r.styleLost[i].items.items) < maxStyleNotes {
			r.styleLost[i].items.add("style." + name)
		}
	}
}

// textOf returns the text of a text node. The shortcodes in the text of the block editor are noted, as WordPress runs them,
// and the escaped ones ([[foo]]) are the text of the shortcode. The text of the HTML of the classic editor has none: the
// shortcodes were found before, and what is left is text.
func (r *richReader) textOf(s string) string {
	if r.c.classicDepth > 0 || !strings.Contains(s, "[") {
		return s
	}
	isName := textShortcodeFilter()
	if r.codeDepth > 0 {
		isName = registeredOnly(isName)
	}
	found := findTextShortcodes(s, isName)
	if len(found) == 0 {
		return s
	}
	var b strings.Builder
	pos := 0
	for _, sc := range found {
		if sc.Open && sc.Close {
			b.WriteString(s[pos:sc.Start])
			b.WriteString(s[sc.Start+1 : sc.End-1])
			pos = sc.End
			continue
		}
		r.shortcodes.add("[" + sc.Name + "]")
	}
	b.WriteString(s[pos:])
	return b.String()
}

// piece reads an element that stands for a shortcode or a URL in the HTML of the classic editor: a shortcode that is in the
// text cannot be text; a URL is text, and WordPress may have embedded it.
func (r *richReader) piece(pc *classicPiece) {
	if pc.kind == pieceShortcode {
		r.shortcodes.add("[" + pc.sc.Name + "]")
		return
	}
	r.size += len(pc.url) + richPieceBytes
	r.tokens = append(r.tokens, richToken{kind: tokText, text: strings.ToValidUTF8(pc.url, string(rune(0xFFFD))), marks: r.cur})
	r.c.note(r.at, Review, urlRecord, pc.url)
}

// leave reads the end of n.
func (r *richReader) leave(n *html.Node) {
	last := len(r.frames) - 1
	if last < 0 || r.frames[last].node != n {
		return
	}
	f := r.frames[last]
	r.frames = r.frames[:last]
	r.marks, r.cur = f.marks, f.plain
	if f.code {
		r.codeDepth--
	}
	if f.boundary {
		r.tokens = append(r.tokens, richToken{kind: tokBoundary})
	}
}

// marksOf returns the marks that the element n gives its content. handled is set if it gives none and nothing is lost
// with it, or if the loss is already noted. The error is a legacy error, from a link (see link).
func (r *richReader) marksOf(n *html.Node, tag string) (add []richMark, handled bool, err error) {
	mark := func(t content.MarkType) []richMark {
		return []richMark{{Mark: content.Mark{Type: t}, by: tag}}
	}
	if r.plain {
		// The text has no marks.
		return nil, false, nil
	}
	switch n.Name {
	case "b", "strong":
		add = mark(content.MarkBold)
	case "i", "em":
		add = mark(content.MarkItalic)
	case "u":
		add = mark(content.MarkUnderline)
	case "s", "strike", "del":
		add = mark(content.MarkStrike)
	case "code":
		add = mark(content.MarkCode)
	case "sub":
		add = mark(content.MarkSubscript)
	case "sup":
		add = mark(content.MarkSuperscript)
	case "a":
		href := attr(n, "href")
		if r.c.inClassic && strings.Contains(href, "[") && len(findTextShortcodes(href, textShortcodeFilter())) > 0 {
			r.c.note(r.at, Review, "the address %q has a shortcode, which whiteroom does not run", href)
		}
		l, err := r.c.link(href, attr(n, "target"), attr(n, "rel"), r.at)
		if err != nil {
			return nil, false, err
		}
		if l == nil {
			// c.link notes the address that it leaves out; an anchor with no address has none.
			noAddress := strings.TrimSpace(href) == ""
			return nil, !noAddress || r.target(n), nil
		}
		add = []richMark{{Mark: content.Mark{Type: content.MarkLink, Link: l}, by: tag}}
	case "mark":
		if h := r.highlight(n); h != nil {
			add = []richMark{{Mark: content.Mark{Type: content.MarkHighlight, Highlight: h}, by: tag}}
			r.useColors(h)
		}
	case "span":
		decls := styleDecls(attr(n, "style"))
		decoration := strings.Fields(declValue(decls, "text-decoration"))
		if slices.Contains(decoration, "underline") {
			add = append(add, mark(content.MarkUnderline)...)
			r.consumed = append(r.consumed, "text-decoration")
		}
		if slices.Contains(decoration, "line-through") {
			add = append(add, mark(content.MarkStrike)...)
			r.consumed = append(r.consumed, "text-decoration")
		}
		if heavy(declValue(decls, "font-weight")) {
			add = append(add, mark(content.MarkBold)...)
			r.consumed = append(r.consumed, "font-weight")
		}
		if style, _, _ := strings.Cut(declValue(decls, "font-style"), " "); style == "italic" || style == "oblique" {
			add = append(add, mark(content.MarkItalic)...)
			r.consumed = append(r.consumed, "font-style")
		}
		var h *content.Highlight
		if hasClass(n, "has-inline-color") || r.mappedClass(n) {
			h = r.highlight(n)
		} else {
			h = writtenHighlight(decls)
		}
		if h != nil {
			add = append(add, richMark{Mark: content.Mark{Type: content.MarkHighlight, Highlight: h}, by: tag})
			r.useColors(h)
		}
	}
	return add, false, nil
}

// useColors notes that the highlight h took the colors that the style of the element being read wrote: a variant has no colors,
// which is noted already.
func (r *richReader) useColors(h *content.Highlight) {
	if h.Variant != "" || h.Color.Value != "" {
		r.consumed = append(r.consumed, "color")
	}
	if h.Variant != "" || h.BackgroundColor.Value != "" {
		r.consumed = append(r.consumed, "background-color")
	}
}

// target notes the a element n, which has no address, as the place that a link goes to, if it has an id or a name and the HTML
// is that of the classic editor: its id, or else its name, is to be the anchor of the block that holds it (see bodyConv.targets).
// It reports whether it did.
func (r *richReader) target(n *html.Node) bool {
	c := r.c
	id, name := attr(n, "id"), attr(n, "name")
	if !c.inClassic || id == "" && name == "" {
		return false
	}
	if c.targetLeft <= 0 {
		c.targetsCut = true
		return false
	}
	c.targetLeft--
	r.size += richPieceBytes
	if id == "" {
		id, name = name, ""
	}
	c.targets = append(c.targets, id)
	if name != "" && name != id {
		c.note(r.at, Skipped, "the anchor %q is not imported: the element has the id %q", name, id)
	}
	return true
}

// heavy reports whether the value of font-weight is bold, bolder, or a weight of 600 to 900.
func heavy(v string) bool {
	if v == "bold" || v == "bolder" {
		return true
	}
	n, err := strconv.Atoi(v)
	return err == nil && n >= 600 && n <= 900
}

// declValue returns the value of the property of the declarations of a style, in lower case and without !important.
func declValue(decls map[string]string, property string) string {
	v, _, _ := strings.Cut(strings.ToLower(decls[property]), "!")
	return strings.TrimSpace(v)
}

// writtenColor returns the color that the declarations write for the property, if it is not clear and the rules of the colors
// (6.5.3) accept it.
func writtenColor(decls map[string]string, property string) string {
	if v := decls[property]; v != "" && !transparent(v) && content.CheckColor(v) == nil {
		return v
	}
	return ""
}

// writtenHighlight returns the highlight that the colors in the declarations of a style make, or nil if there are none.
func writtenHighlight(decls map[string]string) *content.Highlight {
	h := content.Highlight{
		Color:           content.Color{Value: writtenColor(decls, "color")},
		BackgroundColor: content.Color{Value: writtenColor(decls, "background-color")},
	}
	if h == (content.Highlight{}) {
		return nil
	}
	return &h
}

// mappedClass reports whether the settings map a class of n to a variant: a span with such a class is a highlight too.
func (r *richReader) mappedClass(n *html.Node) bool {
	marks := r.c.blockNames().Marks
	return slices.ContainsFunc(classList(n), func(class string) bool { return marks[class] != "" })
}

// variantOf returns the variant of the highlight that the classes of n tell: the first class that the settings map to a
// name. Failing that, a mark and a span of the class has-inline-color take the first class that the settings do not have
// and that is not one of WordPress's own (has-, wp-), as the classes of a theme or a plugin are.
func (r *richReader) variantOf(n *html.Node, classes []string) string {
	marks := r.c.blockNames().Marks
	for _, class := range classes {
		if marks[class] != "" {
			if name, result := lookupName(marks, class); result == nameMapped {
				return name
			}
		}
	}
	if n.Name != "mark" && !slices.Contains(classes, "has-inline-color") {
		return ""
	}
	for _, class := range classes {
		if _, mapped := marks[class]; mapped || strings.HasPrefix(class, "has-") || strings.HasPrefix(class, "wp-") {
			continue
		}
		if name, result := lookupName(marks, class); result == nameMapped {
			return name
		}
	}
	return ""
}

// addMarks puts the marks of an element in those of its content. One that the content has already is not added again,
// and a highlight in a highlight and a subscript in a superscript, or the other way, are left out and noted.
func (r *richReader) addMarks(add []richMark) {
	marks := slices.Clone(r.marks)
	for _, m := range add {
		var outer *richMark
		for i := range marks {
			if marks[i].Type == m.Type || m.Type == content.MarkSubscript && marks[i].Type == content.MarkSuperscript ||
				m.Type == content.MarkSuperscript && marks[i].Type == content.MarkSubscript {
				outer = &marks[i]
				break
			}
		}
		switch {
		case outer == nil:
			marks = append(marks, m)
		case m.Type == content.MarkHighlight || outer.Type != m.Type:
			r.nested.add(m.by + " inside " + outer.by)
		}
	}
	r.marks = marks
	r.cur = make([]content.Mark, len(marks))
	for i, m := range marks {
		r.cur[i] = m.Mark
	}
}

// colorClass reads class, such as has-vivid-red-color or has-vivid-red-background-color, as the slug of a color of the
// theme of WordPress, and whether it is for the background. The classes that WordPress gives to everything that has a
// color of the text, a link, or an inline element (has-text-color, has-link-color, has-inline-color) are not colors.
func colorClass(class string) (slug string, background, ok bool) {
	rest, ok := strings.CutPrefix(class, "has-")
	if !ok || class == "has-inline-color" || class == "has-text-color" || class == "has-link-color" {
		return "", false, false
	}
	if s, ok := strings.CutSuffix(rest, "-background-color"); ok && s != "" {
		return s, true, true
	}
	if s, ok := strings.CutSuffix(rest, "-color"); ok && s != "" {
		return s, false, true
	}
	return "", false, false
}

// highlight reads the colors of a mark, or of a span of the class has-inline-color: a class that the settings map to a
// variant is the variant; else the colors are the presets that the classes tell, or the colors written in the style. It
// returns nil if nothing is left, and the element then keeps only its text.
func (r *richReader) highlight(n *html.Node) *content.Highlight {
	classes := classList(n)
	var text, background string
	for _, class := range classes {
		if slug, isBackground, ok := colorClass(class); ok && isBackground {
			background = slug
		} else if ok {
			text = slug
		}
	}
	decls := styleDecls(attr(n, "style"))
	written := func(property string) string { return writtenColor(decls, property) }
	if variant := r.variantOf(n, classes); variant != "" {
		if text != "" || background != "" || written("color") != "" || written("background-color") != "" {
			r.colorsLost.add(tagText(n.Name))
		}
		return &content.Highlight{Variant: variant}
	}
	pick := func(slug, property string) content.Color {
		if slug != "" {
			if name, ok := r.c.presetName(kindColor, slug, r.at); ok {
				return content.Color{Preset: name}
			}
			return content.Color{}
		}
		return content.Color{Value: written(property)}
	}
	h := content.Highlight{Color: pick(text, "color"), BackgroundColor: pick(background, "background-color")}
	if h == (content.Highlight{}) {
		return nil
	}
	return &h
}

// styleDecls reads the declarations of the attribute style: the lower case names of the properties and their values,
// where the last of a property counts.
func styleDecls(s string) map[string]string {
	var out map[string]string
	for decl := range strings.SplitSeq(s, ";") {
		name, value, ok := strings.Cut(decl, ":")
		if name = strings.ToLower(strings.TrimSpace(name)); !ok || name == "" {
			continue
		}
		if out == nil {
			out = make(map[string]string)
		}
		out[name] = strings.TrimSpace(value)
	}
	return out
}

// transparent reports whether the color v, written in CSS, is clear: transparent, or with an alpha of 0. WordPress writes
// rgba(0, 0, 0, 0) as the background of the text that it colors, so that the browser's yellow for mark does not show.
func transparent(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "transparent" {
		return true
	}
	if hex, ok := strings.CutPrefix(v, "#"); ok {
		return len(hex) == 4 && hex[3] == '0' || len(hex) == 8 && hex[6:] == "00"
	}
	open := strings.IndexByte(v, '(')
	inner, ok := strings.CutSuffix(v, ")")
	if open < 0 || !ok {
		return false
	}
	args := strings.FieldsFunc(inner[open+1:], func(r rune) bool { return r == ',' || r == '/' || r == ' ' })
	if len(args) != 4 {
		return false
	}
	alpha, err := strconv.ParseFloat(strings.TrimSuffix(args[3], "%"), 64)
	return err == nil && alpha == 0
}

// run is the text with marks that is being made, or a hard break.
type run struct {
	hard  bool
	text  []byte
	marks []content.Mark
}

// assemble makes text of the tokens: spaces are collapsed (unless the text is a verse), a line break is put between the
// text and an element that sets its content apart, and text with the same marks is joined.
func (r *richReader) assemble() content.RichText {
	var runs []run
	space := !r.verse // nothing has been written, or the last is a space or a break: spaces are dropped
	pending := false  // an element that sets its content apart has started or ended since the last text
	trim := func() {
		if n := len(runs); !r.verse && n > 0 && !runs[n-1].hard && runs[n-1].text[len(runs[n-1].text)-1] == ' ' {
			if runs[n-1].text = runs[n-1].text[:len(runs[n-1].text)-1]; len(runs[n-1].text) == 0 {
				runs = runs[:n-1]
			}
		}
	}
	hardBreak := func() {
		trim()
		runs = append(runs, run{hard: true})
	}
	write := func(text string, marks []content.Mark) {
		if n := len(runs); n > 0 && !runs[n-1].hard && sameMarks(runs[n-1].marks, marks) {
			runs[n-1].text = append(runs[n-1].text, text...)
		} else {
			runs = append(runs, run{text: []byte(text), marks: marks})
		}
	}
	startContent := func() {
		if pending && len(runs) > 0 && !runs[len(runs)-1].hard {
			hardBreak()
		}
		pending = false
	}
	for _, tk := range r.tokens {
		switch tk.kind {
		case tokBoundary:
			pending = true
		case tokBreak:
			hardBreak()
			space, pending = true, false
		case tokText:
			if r.verse {
				for i, line := range strings.Split(verseText(tk.text), "\n") {
					if i > 0 {
						hardBreak()
						pending = false
					}
					if line != "" {
						startContent()
						write(line, tk.marks)
					}
				}
				continue
			}
			var b strings.Builder
			for _, ch := range tk.text {
				if isHTMLSpace(ch) || isOddSpace(ch) {
					if !space && !pending {
						b.WriteByte(' ')
						space = true
					}
					continue
				}
				if pending {
					startContent()
				}
				b.WriteRune(ch)
				space = false
			}
			if b.Len() > 0 {
				write(b.String(), tk.marks)
			}
		}
	}
	if r.verse {
		for len(runs) > 0 && runs[len(runs)-1].hard {
			runs = runs[:len(runs)-1]
		}
	} else {
		trim()
	}
	var out content.RichText
	for _, ru := range runs {
		in := content.Inline{Type: content.InlineHardBreak}
		if !ru.hard {
			in = content.Inline{Type: content.InlineText, Text: string(ru.text)}
			if len(ru.marks) > 0 {
				in.Marks = slices.Clone(ru.marks)
			}
		}
		out = append(out, in)
	}
	return out
}

// isOddSpace reports whether r is a character that HTML does not count as white space, but that a line of text cannot
// have: a vertical tab, a next line, or a line or paragraph separator.
func isOddSpace(r rune) bool {
	return r == '\v' || r == 0x85 || r == 0x2028 || r == 0x2029
}

// verseText makes the line breaks of a verse LF and the tabs, the form feeds and the other characters that a line cannot
// have spaces.
func verseText(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\f' || isOddSpace(r) {
			return ' '
		}
		return r
	}, s)
}

func sameMarks(a, b []content.Mark) bool {
	return slices.EqualFunc(a, b, func(x, y content.Mark) bool {
		if x.Type != y.Type || (x.Highlight == nil) != (y.Highlight == nil) || (x.Link == nil) != (y.Link == nil) {
			return false
		}
		if x.Highlight != nil && *x.Highlight != *y.Highlight {
			return false
		}
		return x.Link == nil || x.Link.Href == y.Link.Href && x.Link.Entry == y.Link.Entry && x.Link.Media == y.Link.Media &&
			x.Link.Anchor == y.Link.Anchor && x.Link.NewTab == y.Link.NewTab && slices.Equal(x.Link.Rel, y.Link.Rel)
	})
}

// link makes the link of an element a, which has the address href, and the attributes target and rel, in the body being
// converted. A link to a page or a file of the old site that is imported is a link to the entry or the medium; one to the
// site is a path in it; another is the address as it is, if whiteroom accepts it. If it cannot be a link, it notes why at
// at and returns nil, and the text stays without it. The error is a legacy error: the conversion has read more bytes of HTML
// than it may with the base that resolving the address counts (see resolve).
func (c *bodyConv) link(href, target, rel string, at noteAt) (*content.Link, error) {
	href = content.NormalizeHref(href)
	if href == "" {
		return nil, nil
	}
	l, err := c.linkTo(href, at)
	if err != nil || l == nil {
		return nil, err
	}
	l.NewTab = strings.EqualFold(strings.TrimSpace(target), "_blank")
	for _, word := range strings.FieldsFunc(rel, isHTMLSpace) {
		r := content.Rel(strings.ToLower(word))
		if (r == content.RelNofollow || r == content.RelSponsored || r == content.RelUGC) && !slices.Contains(l.Rel, r) {
			l.Rel = append(l.Rel, r)
		}
	}
	return l, nil
}

// linkTo finds where the link href, which is not empty, goes.
func (c *bodyConv) linkTo(href string, at noteAt) (*content.Link, error) {
	if rest, ok := strings.CutPrefix(href, "#"); ok {
		name := rest
		if s, err := url.PathUnescape(rest); err == nil {
			name = s
		}
		if content.CheckName(name) != nil {
			c.note(at, Skipped, "the link to %q is removed: the anchor is not a name that whiteroom accepts", href)
			return nil, nil
		}
		return &content.Link{Anchor: name}, nil
	}
	ref, err := url.Parse(href)
	if err != nil {
		return c.externalLink(href, href, at), nil
	}
	abs, err := c.resolve(ref)
	if err != nil {
		return nil, err
	}
	id, sitePath, same := c.imp.urlIndex().lookup(abs)
	if !same {
		made := href
		if ref.Scheme == "" {
			made = abs.String()
		}
		return c.externalLink(made, href, at), nil
	}
	if id >= 1 {
		if kind, to, ok := c.imp.IDs.Lookup(Object{Kind: ObjectPost, ID: id}); ok {
			if _, ok := c.imp.Ref(kind, to); ok {
				switch kind {
				case content.RefEntry:
					l := &content.Link{Entry: to}
					if abs.Fragment != "" {
						if err := content.CheckName(abs.Fragment); err != nil {
							c.note(at, Skipped, "the anchor %q of the link is removed: %s", abs.Fragment, err)
						} else {
							l.Anchor = abs.Fragment
						}
					}
					return l, nil
				case content.RefMedia:
					return &content.Link{Media: to}, nil
				case content.RefForm, content.RefTerm, content.RefAuthor, content.RefMenu, content.RefRedirect, content.RefRevision,
					content.RefTranslationGroup, content.RefUser:
				}
			}
		}
	}
	// A page of the old site that is not imported, or one that is not a post, such as the list of a term.
	h := sitePath
	if abs.RawQuery != "" {
		h += "?" + abs.RawQuery
	}
	if f := abs.EscapedFragment(); f != "" {
		h += "#" + f
	}
	return c.externalLink(h, href, at), nil
}

// baseURL returns the address that the relative addresses in the body are from: the link of the item, or else the home of
// the site. A link of the item that is longer than a path may be is not used, as it would be copied into every address.
func (c *bodyConv) baseURL() *url.URL {
	if c.base == nil {
		c.base = c.itemBase()
		c.baseLen = len(c.base.String())
	}
	return c.base
}

// resolve resolves ref, an address in the HTML, against the base. Resolving works on a copy of the base for each address,
// whatever the address becomes and however short the result is, so the length of the base counts among the bytes of HTML that
// the conversion reads each time that an address without a scheme is resolved; an address with a scheme adds nothing. The
// error is a legacy error, from readHTML.
func (c *bodyConv) resolve(ref *url.URL) (*url.URL, error) {
	base := c.baseURL()
	if ref.Scheme == "" {
		if err := c.readHTML(c.baseLen); err != nil {
			return nil, err
		}
	}
	return base.ResolveReference(ref), nil
}

func (c *bodyConv) itemBase() *url.URL {
	if link := strings.TrimSpace(c.opts.Item.Link); len(link) <= permPathBytes {
		if it, err := url.Parse(link); err == nil && it.Host != "" {
			return it
		}
	}
	if home := c.imp.urlIndex().home; home != nil {
		return home
	}
	return &url.URL{}
}

// externalLink makes the link to the address href, if whiteroom accepts it. written is the address in the HTML, which the note
// quotes, as href is longer than it where it is made from the link of the item.
func (c *bodyConv) externalLink(href, written string, at noteAt) *content.Link {
	if err := content.CheckHref(href); err != nil {
		c.note(at, Skipped, "the link to %q is removed: %s", written, err)
		return nil
	}
	return &content.Link{Href: href}
}
