package render

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"maps"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/sanitize"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// conceptRichTextJSON is the example in 6.5.2 of the concept (docs/06.05-blocks.md).
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
var urlAttrs = []string{"action", "cite", "href", "poster", "src"}

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
// after them. The warnings are those of the texts alone, and so are the rules of the CSS, each once, which checkCSS
// and checkClasses accept.
func checkRichText(r *Renderer, rt content.RichText) error {
	out, css, warnings := r.RichText(rt)
	pieces, err := checkRichHTML(string(out), r.highlights)
	if err != nil {
		return fmt.Errorf("%q: %w", out, err)
	}
	var wantWarnings []Warning
	var wantCSS []string   // the rules of the texts alone, each once
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
			alone, aloneCSS, ws := r.RichText(rt[i : i+1])
			for _, x := range aloneCSS.rules {
				if !slices.Contains(wantCSS, x) {
					wantCSS = append(wantCSS, x)
				}
			}
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
	if !slices.Equal(css.rules, wantCSS) {
		return fmt.Errorf("%q: CSS %q, want the rules of the texts alone %q", out, css.rules, wantCSS)
	}
	rules, err := checkCSS(css.String(), r.settings)
	if err == nil {
		err = checkClasses([]string{string(out)}, rules, r.settings, nil)
	}
	if err != nil {
		return fmt.Errorf("%q: CSS:\n%s\n%w", out, css, err)
	}
	return nil
}

// cssRule is a rule of the CSS of a page: its selector, the class in it, its form, which is the selector with b-x for
// the class of a block and hl-x for that of a highlight, such as ".b-x img", its block from "{" on, and its
// declarations.
type cssRule struct {
	selector, class, form, body string
	decls                       []cssDecl
}

// cssDecl is a declaration of a rule, whose value is without " !important".
type cssDecl struct {
	property, value string
	important       bool
}

// valueRule is the rule of the values of a property: the kind of the presets that it can name, as var() of their custom
// properties, if any, and the check of the values written directly, if it can have them.
type valueRule struct {
	kind  string
	check func(string) error
}

// cssProperty is a property of the rules of a form, with the style item that sets it; "" for those of attributes and
// highlights.
type cssProperty struct {
	name, item string
	value      valueRule
}

// cssForm is a form of selectors, with the properties of its rules in their order.
type cssForm struct {
	form       string
	properties []cssProperty
}

// lengthValue is the rule of lengths by r, which can also be presets of kind unless it is "".
func lengthValue(kind string, r content.LengthRule) valueRule {
	return valueRule{kind, func(s string) error { return content.CheckLength(s, r) }}
}

// fixedValue is the rule of a property whose value is always want.
func fixedValue(want string) valueRule {
	return valueRule{check: func(s string) error {
		if s != want {
			return fmt.Errorf("%q is not %s", s, want)
		}
		return nil
	}}
}

var (
	colorValue   = valueRule{"color", content.CheckColor}
	paddingValue = lengthValue("spacing", content.LengthRule{Percent: true}) // also of blockGap
	marginValue  = lengthValue("spacing", content.LengthRule{Negative: true, Percent: true})
	// The margins between the children of a flow container.
	childMargins = []cssProperty{
		{"margin-top", "blockGap", fixedValue("0")}, {"margin-bottom", "blockGap", fixedValue("0")},
	}
	childGap = []cssProperty{{"margin-top", "blockGap", paddingValue}}
)

// cssForms are the forms of the rules of the CSS of pages: those of a block, in their order, and that of a highlight.
var cssForms = []cssForm{
	{".b-x", []cssProperty{
		{"color", "textColor", colorValue},
		{"background-color", "backgroundColor", colorValue},
		{"background-image", "gradient", valueRule{kind: "gradient"}},
		{"font-size", "fontSize", lengthValue("fontSize", content.LengthRule{Percent: true})},
		{"letter-spacing", "letterSpacing", lengthValue("", content.LengthRule{Negative: true})},
		{"font-family", "fontFamily", valueRule{kind: "fontFamily"}},
		{"font-weight", "fontWeight", valueRule{check: checkWeight}},
		{"line-height", "lineHeight", valueRule{check: checkLineHeight}},
		{"padding-top", "padding", paddingValue},
		{"padding-right", "padding", paddingValue},
		{"padding-bottom", "padding", paddingValue},
		{"padding-left", "padding", paddingValue},
		{"margin-top", "margin", marginValue},
		{"margin-right", "margin", marginValue},
		{"margin-bottom", "margin", marginValue},
		{"margin-left", "margin", marginValue},
		{"gap", "blockGap", paddingValue},
		{"border-width", "border", lengthValue("", content.LengthRule{})},
		{"border-style", "border", valueRule{check: checkBorderStyle}},
		{"border-color", "border", colorValue},
		{"border-radius", "border", lengthValue("", content.LengthRule{Percent: true})},
		{"height", "", lengthValue("spacing", content.LengthRule{})},
		{"flex-basis", "", lengthValue("", content.LengthRule{Percent: true})},
		{"flex-grow", "", fixedValue("0")},
		{"grid-template-columns", "", valueRule{}}, // checkGrid
	}},
	{":where(.b-x)", []cssProperty{{"border-style", "border", fixedValue("solid")}}},
	{".b-x :where(a)", []cssProperty{{"color", "linkColor", colorValue}}},
	{".b-x img", []cssProperty{
		{"width", "", lengthValue("", content.LengthRule{Percent: true})},
		{"aspect-ratio", "", valueRule{check: content.CheckAspectRatio}},
		{"object-fit", "", fixedValue("cover")},
		{"height", "", fixedValue("auto")},
	}},
	{".b-x > *", childMargins},
	{".b-x > * + *", childGap},
	{".b-x > blockquote > *", childMargins},
	{".b-x > blockquote > * + *", childGap},
	{".hl-x", []cssProperty{{"color", "", colorValue}, {"background-color", "", colorValue}}},
}

// formIndex returns the index of form in cssForms, or -1.
func formIndex(form string) int {
	return slices.IndexFunc(cssForms, func(f cssForm) bool { return f.form == form })
}

// presetPrefixes are the prefixes of the custom properties of the presets of each kind.
var presetPrefixes = map[string]string{
	"color": "--color-", "fontSize": "--font-size-", "spacing": "--spacing-", "fontFamily": "--font-family-",
	"gradient": "--gradient-", "shadow": "--shadow-",
}

// kindPresets returns the presets of p of kind, a key of presetPrefixes.
func kindPresets(p *content.Presets, kind string) *[]content.Preset {
	return map[string]*[]content.Preset{
		"color": &p.Color, "fontSize": &p.FontSize, "spacing": &p.Spacing, "fontFamily": &p.FontFamily,
		"gradient": &p.Gradient, "shadow": &p.Shadow,
	}[kind]
}

// checkValue returns an error unless v is var() of the custom property of a preset of the kind of x that the theme s
// has, or a value written directly that x accepts and the theme allows.
func (x valueRule) checkValue(v string, s *content.ThemeSettings) error {
	if x.kind != "" && strings.HasPrefix(v, "var(") {
		if s != nil && slices.ContainsFunc(*kindPresets(&s.Presets, x.kind), func(p content.Preset) bool {
			return v == "var("+presetPrefixes[x.kind]+p.Name+")"
		}) {
			return nil
		}
		return fmt.Errorf("%s is not a %s preset of the theme", v, x.kind)
	}
	if x.check == nil {
		return fmt.Errorf("%s is not a preset", v)
	}
	if err := x.check(v); err != nil {
		return err
	}
	if s != nil && slices.Contains(s.PresetsOnly, x.kind) {
		return fmt.Errorf("%s is not a preset, but the theme allows only presets for %s", v, x.kind)
	}
	return nil
}

func checkWeight(s string) error {
	if n, err := strconv.Atoi(s); err != nil || strconv.Itoa(n) != s || n < 100 || n > 900 {
		return fmt.Errorf("invalid font weight %q", s)
	}
	return nil
}

// cssNumber matches the numbers of CSS without a sign or an exponent.
var cssNumber = regexp.MustCompile(`^(?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+)$`)

func checkLineHeight(s string) error {
	if v, err := strconv.ParseFloat(s, 64); !cssNumber.MatchString(s) || err != nil || v <= 0 || v > 10 {
		return fmt.Errorf("invalid line height %q", s)
	}
	return nil
}

func checkBorderStyle(s string) error {
	if s != "solid" && s != "dashed" && s != "dotted" {
		return fmt.Errorf("invalid border style %q", s)
	}
	return nil
}

var (
	gridRepeat = regexp.MustCompile(`^repeat\(([0-9]+), minmax\(0, 1fr\)\)$`)
	gridFill   = regexp.MustCompile(`^repeat\(auto-fill, minmax\(min\(([^()]+), 100%\), 1fr\)\)$`)
	gridBoth   = regexp.MustCompile(
		`^repeat\(auto-fill, minmax\(max\(min\(([^()]+), 100%\), \(100% - ([0-9]+) \* (.+)\) / ([0-9]+)\), 1fr\)\)$`)
)

// checkGrid checks v, the grid-template-columns of a grid whose gap is gap: n columns of one width; columns at least w
// wide; or both, as many columns at least w wide as fit, but no more than n, with the n-1 gaps between them.
func checkGrid(v, gap string) error {
	columns := func(s string) bool {
		n, err := strconv.Atoi(s)
		return err == nil && strconv.Itoa(n) == s && n >= 1 && n <= 16
	}
	width := func(s string) bool { return content.CheckLength(s, content.LengthRule{}) == nil }
	if m := gridRepeat.FindStringSubmatch(v); m != nil && columns(m[1]) {
		return nil
	}
	if m := gridFill.FindStringSubmatch(v); m != nil && width(m[1]) {
		return nil
	}
	if m := gridBoth.FindStringSubmatch(v); m != nil && width(m[1]) && columns(m[4]) && m[3] == gap {
		if n, _ := strconv.Atoi(m[4]); m[2] == strconv.Itoa(n-1) {
			return nil
		}
	}
	return fmt.Errorf("%q is not the columns of a grid with the gap %s", v, gap)
}

var (
	// cssClass matches the class of a block or of a highlight in a selector.
	cssClass = regexp.MustCompile(`\.(b-[0-9a-z]{8}|hl-[0-9a-f]{8})`)
	// cssText matches the values that cannot end a declaration, a rule or a style element, apart from comments.
	cssText = regexp.MustCompile(`^[0-9A-Za-z_#%.,/()*-]+(?: +[0-9A-Za-z_#%.,/()*-]+)*$`)
)

// parseCSS splits css into rules: each a line of its selector and " {", a line of each declaration, "  " and
// "<property>: <value>;", and a line of "}"; with a blank line between each two and a line break at the end. Each
// selector is of one of cssForms.
func parseCSS(css string) ([]cssRule, error) {
	if css == "" {
		return nil, nil
	}
	text, ok := strings.CutSuffix(css, "\n")
	if !ok {
		return nil, errors.New("no line break at the end")
	}
	var rules []cssRule
	for _, t := range strings.Split(text, "\n\n") {
		lines := strings.Split(t, "\n")
		selector, ok := strings.CutSuffix(lines[0], " {")
		if !ok || len(lines) < 3 || lines[len(lines)-1] != "}" {
			return nil, fmt.Errorf("%q is not a rule with declarations", t)
		}
		r := cssRule{selector: selector, body: strings.TrimPrefix(t, selector+" ") + "\n"}
		if m := cssClass.FindStringSubmatchIndex(selector); m != nil {
			r.class = selector[m[2]:m[3]]
			prefix, _, _ := strings.Cut(r.class, "-")
			r.form = selector[:m[0]] + "." + prefix + "-x" + selector[m[1]:]
		}
		if formIndex(r.form) < 0 {
			return nil, fmt.Errorf("unknown selector %q", selector)
		}
		for _, l := range lines[1 : len(lines)-1] {
			d, ok1 := strings.CutPrefix(l, "  ")
			d, ok2 := strings.CutSuffix(d, ";")
			property, value, ok3 := strings.Cut(d, ": ")
			if !ok1 || !ok2 || !ok3 {
				return nil, fmt.Errorf("%s: %q is not a declaration", selector, l)
			}
			value, important := strings.CutSuffix(value, " !important")
			r.decls = append(r.decls, cssDecl{property, value, important})
		}
		rules = append(rules, r)
	}
	return rules, nil
}

// checkCSS returns the rules of css, the CSS of a page in the theme s, as parseCSS reads them, or an error if a rule is
// not one that checkRule accepts, or if the rules of a class are not together and in the order of cssForms, at most one
// of each form, as checkBlockRules expects of those of a block.
func checkCSS(css string, s *content.ThemeSettings) ([]cssRule, error) {
	rules, err := parseCSS(css)
	if err != nil {
		return nil, err
	}
	var classes []string
	forms := make(map[string]map[string]*cssRule) // the rules of each class by form
	for i := range rules {
		r := &rules[i]
		if err := checkRule(r, s); err != nil {
			return nil, fmt.Errorf("%s: %w", r.selector, err)
		}
		switch {
		case forms[r.class] == nil:
			classes = append(classes, r.class)
			forms[r.class] = make(map[string]*cssRule)
		case rules[i-1].class != r.class:
			return nil, fmt.Errorf("%s: apart from the rules of %s before", r.selector, r.class)
		case formIndex(rules[i-1].form) >= formIndex(r.form):
			return nil, fmt.Errorf("%s: after %s", r.selector, rules[i-1].selector)
		}
		forms[r.class][r.form] = r
	}
	for _, c := range classes {
		if err := checkBlockRules(forms[c]); err != nil {
			return nil, fmt.Errorf("%s: %w", c, err)
		}
	}
	return rules, nil
}

// checkRule returns an error if r, a rule of the CSS of a page in the theme s, has properties that its form does not
// have, or out of their order; a value that its property does not accept or the theme does not allow; a declaration
// without !important, or that of the default style of borders with it, which themes can override; a property of an
// image without those that go with it, or flex-basis without flex-grow; or, for a highlight, a class that is not that
// of its block.
func checkRule(r *cssRule, s *content.ThemeSettings) error {
	properties := cssForms[formIndex(r.form)].properties
	values := make(map[string]string)
	last := -1
	for _, d := range r.decls {
		i := slices.IndexFunc(properties, func(p cssProperty) bool { return p.name == d.property })
		if i <= last {
			return fmt.Errorf("%s is not a property of the rule, or out of order", d.property)
		}
		last = i
		values[d.property] = d.value
		var err error
		switch {
		case d.important == (r.form == ":where(.b-x)"):
			err = fmt.Errorf("!important: %v", d.important)
		case !cssText.MatchString(d.value) || strings.Contains(d.value, "/*") || strings.Contains(d.value, "*/"):
			err = fmt.Errorf("%q is not a value", d.value)
		case d.property == "grid-template-columns":
			// gap comes before it.
			err = checkGrid(d.value, cmp.Or(values["gap"], "0.5em"))
		default:
			err = properties[i].value.checkValue(d.value, s)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", d.property, err)
		}
	}
	has := func(p string) bool {
		_, ok := values[p]
		return ok
	}
	switch r.form {
	case ".b-x img":
		if !has("height") || has("aspect-ratio") != has("object-fit") || !has("width") && !has("aspect-ratio") {
			return errors.New("an image without its height, the fit of its aspect ratio, or a width or a ratio")
		}
	case ".b-x":
		if has("flex-basis") != has("flex-grow") {
			return errors.New("one of flex-basis and flex-grow")
		}
	case ".hl-x":
		sum := sha256.Sum256([]byte(r.body))
		if want := "hl-" + hex.EncodeToString(sum[:4]); r.class != want {
			return fmt.Errorf("the class of the colors is %s", want)
		}
	}
	return nil
}

// checkBlockRules returns an error if forms, the rules of a class by form, do not go together as those of a block do:
// the default style of borders exactly when the main rule has a width or a color of borders but no style; and the
// margins between children in pairs of rules of one form, without a gap.
func checkBlockRules(forms map[string]*cssRule) error {
	main := make(map[string]bool)
	if r := forms[".b-x"]; r != nil {
		for _, d := range r.decls {
			main[d.property] = true
		}
	}
	solid := (main["border-width"] || main["border-color"]) && !main["border-style"]
	if (forms[":where(.b-x)"] != nil) != solid {
		return fmt.Errorf("a rule of the default style of borders: %v, want %v", !solid, solid)
	}
	n := 0
	for _, pair := range [][2]string{
		{".b-x > *", ".b-x > * + *"}, {".b-x > blockquote > *", ".b-x > blockquote > * + *"},
	} {
		if (forms[pair[0]] == nil) != (forms[pair[1]] == nil) {
			return fmt.Errorf("one of the rules %s and %s", pair[0], pair[1])
		}
		if forms[pair[0]] != nil {
			n++
		}
	}
	if n > 1 || n > 0 && main["gap"] {
		return errors.New("margins between children of two forms, or with a gap")
	}
	return nil
}

var (
	blockClass = regexp.MustCompile(`^b-[0-9a-z]{8}$`)
	markClass  = regexp.MustCompile(`^hl-[0-9a-f]{8}$`)
	// The classes of the attributes with fixed choices.
	attrClass = regexp.MustCompile(`^(?:has-text-align-(?:left|center|right)|align(?:left|center|right|wide|full)|` +
		`is-layout-(?:flow|row|stack|grid|columns)|` +
		`is-content-justification-(?:left|center|right|space-between|stretch)|` +
		`are-vertically-aligned-(?:top|center|bottom|stretch|space-between)|` +
		`is-vertically-aligned-(?:top|center|bottom|stretch)|is-nowrap|is-not-stacked-on-mobile|` +
		`has-width-(?:25|50|75|100))$`)
)

// templateClasses are the classes that the block templates of the sample theme write.
var templateClasses = []string{"table-scroll", "file", "button"}

// htmlSpace reports whether r separates the classes of a class attribute.
func htmlSpace(r rune) bool {
	return strings.ContainsRune(" \t\n\f\r", r)
}

func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}

// htmlClasses returns the classes of the class attributes of the start tags of s, HTML that checkHTML accepts.
func htmlClasses(s string) []string {
	tokens, _ := htmlTokens(s)
	var classes []string
	for _, tok := range tokens {
		m := htmlStartTag.FindStringSubmatch(tok)
		if m == nil {
			continue
		}
		for _, a := range htmlAttr.FindAllStringSubmatch(m[2], -1) {
			if a[1] == "class" {
				classes = append(classes, strings.FieldsFunc(html.UnescapeString(a[2]), htmlSpace)...)
			}
		}
	}
	return classes
}

// writtenClasses adds to classes those that the blocks of bs and their children write as they are, which the renderer
// does not check: those of their classNames; for code blocks, of "language-" and the language; and for html and legacy
// blocks, those that sanitizing keeps in their HTML, but none of the forms of the classes that the core gives, also in
// upper case, which it must remove.
func writtenClasses(classes map[string]bool, bs content.Blocks) {
	for _, b := range bs {
		s := b.ClassName
		if b.Type == content.BlockCode {
			s += " language-" + b.Attrs.Language
		}
		if b.Type == content.BlockHTML || b.Type == content.BlockLegacy {
			out, _, _ := sanitize.Sanitize(b.Attrs.HTML, sanitize.Options{Kind: sanitizeKind(b.Type)})
			for _, c := range htmlClasses(out) {
				if !content.ReservedClass(asciiLower(c)) {
					classes[c] = true
				}
			}
		}
		// html/template writes NUL as U+FFFD.
		for _, c := range strings.FieldsFunc(strings.ReplaceAll(s, "\x00", string(utf8.RuneError)), htmlSpace) {
			classes[c] = true
		}
		writtenClasses(classes, b.Children)
	}
}

// checkClasses returns an error unless each class of lists, the HTML of the lists of blocks of a document or of rich
// text in the theme s, is in written, of an attribute, of a variant that the theme has or of the templates, or is that
// of a block or a highlight with rules in rules. Each class with rules that is not in written is on elements: that of
// a block on one, in the first list, since the blocks of the others with rules have the IDs of those of the first.
func checkClasses(lists []string, rules []cssRule, s *content.ThemeSettings, written map[string]bool) error {
	variants := make(map[string]bool)
	if s != nil {
		for _, b := range s.Blocks {
			for _, v := range b.Variants {
				variants["is-variant-"+v.Name] = true
			}
		}
		for _, v := range s.HighlightVariants {
			variants["is-variant-"+v.Name] = true
		}
	}
	elements := make(map[string]int) // the elements of each class with rules
	for _, r := range rules {
		elements[r.class] = 0
	}
	for i, h := range lists {
		for _, c := range htmlClasses(h) {
			n, ruled := elements[c]
			switch {
			case written[c] || attrClass.MatchString(c) || variants[c] || slices.Contains(templateClasses, c):
			case !blockClass.MatchString(c) && !markClass.MatchString(c):
				return fmt.Errorf("list %d has the class %q", i, c)
			case !ruled:
				return fmt.Errorf("list %d has the class %s, which has no rules", i, c)
			case i > 0 && blockClass.MatchString(c):
				return fmt.Errorf("list %d has the class %s of a block", i, c)
			default:
				elements[c] = n + 1
			}
		}
	}
	for _, r := range rules {
		if n := elements[r.class]; !written[r.class] && (n == 0 || n > 1 && blockClass.MatchString(r.class)) {
			return fmt.Errorf("the class %s is on %d elements", r.class, n)
		}
	}
	return nil
}

// checkIDs returns an error if an id of out, the HTML of lists, the lists of blocks of a document, is written more
// times than the blocks of lists have it as their anchor, or more than once if none has it: the automatic anchors of
// headings and the ids of HTML avoid the anchors of the blocks, each other and themselves.
func checkIDs(out []string, lists []content.Blocks) error {
	anchors := make(map[string]int) // the blocks with each anchor
	var add func(bs content.Blocks)
	add = func(bs content.Blocks) {
		for _, b := range bs {
			if b.Anchor != "" {
				// html/template writes NUL as U+FFFD.
				anchors[strings.ReplaceAll(b.Anchor, "\x00", string(utf8.RuneError))]++
			}
			add(b.Children)
		}
	}
	for _, bs := range lists {
		add(bs)
	}
	ids := make(map[string]int)
	for _, s := range out {
		tokens, _ := htmlTokens(s)
		for _, tok := range tokens {
			if m := htmlStartTag.FindStringSubmatch(tok); m != nil {
				for _, a := range htmlAttr.FindAllStringSubmatch(m[2], -1) {
					if a[1] == "id" {
						ids[html.UnescapeString(a[2])]++
					}
				}
			}
		}
	}
	for id, n := range ids {
		if n > max(anchors[id], 1) {
			return fmt.Errorf("the id %q is written %d times, but is the anchor of %d blocks", id, n, anchors[id])
		}
	}
	return nil
}

var (
	// usedAnchor matches the warnings about ids of HTML that are anchors already, and moreChanges the last warning of
	// HTML with more changes than sanitizing describes.
	usedAnchor = regexp.MustCompile(
		`^removed id attribute of <[a-z0-9]+>: anchor "[0-9A-Za-z_-]+(?:\.\.\.)?" is already used(?: \([0-9]+ times\))?$`)
	moreChanges = regexp.MustCompile(`^and [0-9]+ more changes?$`)
)

// fuzzRenderer is a Renderer of the golden theme with the name.
type fuzzRenderer struct {
	theme string
	r     *Renderer
}

// FuzzRichText checks that RichText writes the HTML and the CSS that checkRichText expects for any rich text that
// decodes, in the default theme in en with the base /blog and in the example theme in ja.
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
		colors = highlightMark(content.Highlight{
			Color: content.Color{Preset: "primary"}, BackgroundColor: content.Color{Value: "#fff4e5"},
		})
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
		// Colors of highlights, the same twice, ignored with a variant, and values that would end their rules.
		{
			textNode("a", colors), textNode("b", boldMark, colors),
			textNode("c", highlightMark(content.Highlight{Variant: "marker", Color: content.Color{Value: "#000"}})),
			textNode("d", highlightMark(content.Highlight{Color: content.Color{Value: "#fff;}body{color:red"}})),
			textNode("e", highlightMark(content.Highlight{
				Color: content.Color{Preset: "x</style>"}, BackgroundColor: content.Color{Value: "url(x) !important"},
			})),
		},
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
// golden theme and site: without an error, as HTML that checkHTML and checkIDs accept, and with the warnings of the
// first list again for the second, apart from those about duplicate block IDs: the blocks of the second list have the
// IDs of those of the first, so that each of its blocks with CSS has such a warning instead of its class. The warnings
// about ids of HTML that are anchors already are left out too, with the last warning of HTML with more changes than
// sanitizing describes, which counts them: the ids that the HTML of the first list keeps are anchors when the second
// list repeats it. The CSS is the same in each site, and checkCSS and checkClasses accept it.
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
		// HTML with ids that are anchors already or not, links and media with paths of the site and non-ASCII
		// characters, and what sanitizing removes or changes.
		`[
			` + headingJSON("b_00head01", "a") + `,
			{
				"type": "html", "id": "b_0000html", "anchor": "top", "variant": "box", "className": "raw",
				"style": {"textColor": {"value": "#111"}},
				"attrs": {"html": "<h1 id=\"top\">t</h1><p id=\"b_00head01\" class=\"note b-0000html B-0PARAGR1\">p</p><p id=\"b_00head02\"><a href=\"/about/\" target=\"_BLANK\" rel=\"external nofollow\">a</a> <a href=\" https://例え.jp/パス?q=a b&amp;r=1#c\" onclick=\"x()\">b</a> <a href=\"#top\">c</a></p><script>alert(1)</script><svg><a href=\"javascript:x\"><text>s</text></a></svg><iframe src=\"https://www.youtube.com/embed/x\" sandbox=\"\" srcdoc=\"<script>y()</script>\">c</iframe><img src=\"/media/日本.png\" alt=\"a\" onerror=\"x()\"><video src=\"/v.mp4\" poster=\"/p.jpg\" controls><source src=\"/v.webm\"><track src=\"/v.vtt\"></video><blockquote cite=\"/q\">q</blockquote>"}
			},
			` + headingJSON("b_00head02", "b") + `,
			{
				"type": "legacy", "id": "b_00legacy",
				"attrs": {"html": "[gallery ids=\"1,2\"]<p id=\"b_00head02\">x</p><p id=\"own\" style=\"color: red\">y</p><iframe src=\"https://www.youtube.com/embed/x\"></iframe><a href=\"java&#115;cript:x\">z</a><img src=\"data:image/png;base64,AAAA\">"}
			},
			{"type": "group", "id": "b_000group", "content": [
				{"type": "html", "id": "b_000html2", "attrs": {"html": "<table><tr><td id=\"cell\">c</td></tr></table><p><b id=\"own\">a</p>b<noscript><p>n</p></noscript><pre>\n\nx</pre>"}}
			]}
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
		// Values that would end the rules of the CSS, classes of the forms that the core gives, and a duplicate ID.
		`[
			{
				"type": "paragraph", "id": "b_0paragr1", "className": "b-0paragr1 hl-065c532e",
				"style": {
					"textColor": {"value": "#fff;}body{color:red"}, "fontSize": {"value": "1rem</style>"},
					"padding": {"top": {"preset": "30;}"}}, "border": {"width": {"value": "1px"}}
				},
				"content": [{"type": "text", "text": "a", "marks": [
					{"type": "highlight", "attrs": {"color": {"preset": "primary"}, "backgroundColor": {"value": "red;}"}}}
				]}]
			},
			{"type": "paragraph", "id": "b_0paragr1", "style": {"textColor": {"value": "#111"}}},
			{
				"type": "group", "id": "b_000group", "style": {"blockGap": {"value": "1rem}"}},
				"attrs": {"layout": {"type": "grid", "columns": 3, "minColumnWidth": "10rem;}"}}
			},
			{"type": "code", "id": "b_0000code", "className": "x\"y", "attrs": {"language": "c b-0000code"}}
		]`,
		// Choices that are not those of their attributes, and IDs that are not valid with a style.
		`[
			{"type": "paragraph", "id": "b_x", "style": {"textColor": {"value": "#111"}}, "attrs": {"textAlign": "Left"}},
			{"type": "heading", "id": "B_0HEADING", "style": {"margin": {"top": {"value": "0"}}}, "attrs": {"level": 2, "textAlign": "x y"}},
			{"type": "buttons", "id": "b_buttons1", "attrs": {"justify": "stretch"}, "content": [
				{"type": "button", "id": "b_0button1", "attrs": {"label": "a", "width": "30%"}}
			]},
			{"type": "columns", "id": "b_columns1", "attrs": {"verticalAlign": "stretch"}, "content": [
				{"type": "column", "id": "b_0column1", "attrs": {"verticalAlign": "space-between", "width": "-1%"}}
			]},
			{"type": "group", "id": "b_00group1", "attrs": {"align": "left", "layout": {"type": "row", "justify": "stretch", "verticalAlign": "\""}}},
			{"type": "group", "id": "b_00group2", "attrs": {"layout": {"type": "stack", "justify": "spaceBetween", "verticalAlign": "stretch"}}},
			{"type": "group", "id": "b_00group3", "attrs": {"layout": {"type": "Grid", "columns": 2}}},
			{"type": "table", "id": "b_000table", "content": [{"type": "tableRow", "content": [
				{"type": "tableCell", "attrs": {"textAlign": "has-text-align-left"}}
			]}]}
		]`,
		imageJSON(`{"media": "` + officeImage + `", "decorative": true, "width": "1rem;}", "aspectRatio": "16/9 !important"}`),
	} {
		f.Add([]byte(s))
	}
	// HTML with more changes than sanitizing describes, and an id.
	var many strings.Builder
	many.WriteString(`<p id="many"`)
	for i := range 120 {
		fmt.Fprintf(&many, ` on%d="x()"`, i)
	}
	many.WriteString(">x</p>")
	manyJSON, err := content.Blocks{
		{Type: content.BlockHTML, ID: "b_0000html", Attrs: content.Attrs{HTML: many.String()}},
	}.MarshalJSON()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(manyJSON)
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
		written := make(map[string]bool)
		writtenClasses(written, bs)
		themeCSS := make(map[string]string) // that of the first site of each theme
		for _, x := range renderers {
			lists, css, warnings, err := x.r.Document(bs, bs)
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
			if err := checkIDs([]string{string(lists[0]), string(lists[1])}, []content.Blocks{bs, bs}); err != nil {
				t.Errorf("Document() of %s twice with the %s theme in %s:\n%s\n%s\n%v",
					data, x.theme, x.r.language, lists[0], lists[1], err)
			}
			var byList [2][]Warning // without List and the warnings that are left out
			var duplicates [2]int   // the warnings about duplicate block IDs
			ok := slices.IsSortedFunc(warnings, func(a, b Warning) int { return cmp.Compare(a.List, b.List) })
			for _, w := range warnings {
				switch {
				case w.List < 0 || w.List > 1 || !strings.HasPrefix(w.Path, "/") || w.Msg == "":
					ok = false
				case strings.HasPrefix(w.Msg, "duplicate block ID "):
					duplicates[w.List]++
				case strings.HasSuffix(w.Path, "/attrs/html") &&
					(usedAnchor.MatchString(w.Msg) || moreChanges.MatchString(w.Msg)):
				default:
					byList[w.List] = append(byList[w.List], Warning{Path: w.Path, Msg: w.Msg})
				}
			}
			if !ok || !slices.Equal(byList[0], byList[1]) {
				t.Errorf("Document() of %s twice with the %s theme in %s: warnings %q, want those of the first list twice",
					data, x.theme, x.r.language, warnings)
			}
			if first, seen := themeCSS[x.theme]; !seen {
				themeCSS[x.theme] = css.String()
			} else if css.String() != first {
				t.Errorf("Document() of %s twice with the %s theme: the CSS in %s differs from that in %s:\n%s",
					data, x.theme, x.r.language, goldenSites[0].language, css)
			}
			rules, err := checkCSS(css.String(), x.r.settings)
			if err == nil {
				err = checkClasses([]string{string(lists[0]), string(lists[1])}, rules, x.r.settings, written)
			}
			if err != nil {
				t.Errorf("Document() of %s twice with the %s theme in %s:\n%s\n%s\nCSS:\n%s\n%v",
					data, x.theme, x.r.language, lists[0], lists[1], css, err)
				continue
			}
			blocks := make(map[string]bool) // the classes of the blocks with rules
			for _, r := range rules {
				if blockClass.MatchString(r.class) {
					blocks[r.class] = true
				}
			}
			if want := duplicates[0] + len(blocks); duplicates[1] != want {
				t.Errorf("Document() of %s twice with the %s theme in %s: %d warnings about duplicate block IDs "+
					"in the second list, want %d, those of the first and one for each class of a block in the CSS",
					data, x.theme, x.r.language, duplicates[1], want)
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

// styleFields are the fields of the input of FuzzStyle, separated by "\x1f": the presets and the values of a style; the
// variant and the colors of a highlight; the height of a spacer; and the lengths and the ratio of attributes.
var styleFields = []string{
	"textColor/preset", "textColor/value", "backgroundColor/preset", "backgroundColor/value",
	"linkColor/preset", "linkColor/value", "gradient/preset", "fontSize/preset", "fontSize/value",
	"letterSpacing/preset", "letterSpacing/value", "fontFamily/preset",
	"padding/top/preset", "padding/top/value", "padding/right/preset", "padding/right/value",
	"padding/bottom/preset", "padding/bottom/value", "padding/left/preset", "padding/left/value",
	"margin/top/preset", "margin/top/value", "margin/right/preset", "margin/right/value",
	"margin/bottom/preset", "margin/bottom/value", "margin/left/preset", "margin/left/value",
	"blockGap/preset", "blockGap/value", "border/width/preset", "border/width/value", "border/style/value",
	"border/color/preset", "border/color/value", "border/radius/preset", "border/radius/value",
	"highlight/variant", "highlight/color/preset", "highlight/color/value",
	"highlight/backgroundColor/preset", "highlight/backgroundColor/value",
	"height/preset", "height/value", "image/width", "image/aspectRatio", "column/width", "grid/minColumnWidth",
}

// styleBlocks returns blocks of the types whose CSS differs, each with its own ID and the style of v, the fields of
// the input of FuzzStyle by name, whose fontWeight and lineHeight are weight and lineHeight; with the attributes of v
// and grids of columns columns; and with texts in the highlight of v.
func styleBlocks(v map[string]string, weight int, lineHeight float64, columns int) content.Blocks {
	color := func(name string) content.Color {
		return content.Color{Preset: v[name+"/preset"], Value: v[name+"/value"]}
	}
	length := func(name string) content.Length {
		return content.Length{Preset: v[name+"/preset"], Value: v[name+"/value"]}
	}
	sides := func(item string) content.Sides {
		return content.Sides{
			Top: length(item + "/top"), Right: length(item + "/right"), Bottom: length(item + "/bottom"),
			Left: length(item + "/left"),
		}
	}
	style := content.Style{
		TextColor: color("textColor"), BackgroundColor: color("backgroundColor"), LinkColor: color("linkColor"),
		Gradient: content.Gradient{Preset: v["gradient/preset"]}, FontSize: length("fontSize"),
		LetterSpacing: length("letterSpacing"), FontFamily: content.FontFamily{Preset: v["fontFamily/preset"]},
		FontWeight: content.FontWeight{Value: weight}, LineHeight: content.LineHeight{Value: lineHeight},
		Padding: sides("padding"), Margin: sides("margin"), BlockGap: length("blockGap"),
		Border: content.Border{
			Width: length("border/width"), Style: content.BorderStyle{Value: v["border/style/value"]},
			Color: color("border/color"), Radius: length("border/radius"),
		},
	}
	text := content.RichText{textNode("a", highlightMark(content.Highlight{
		Variant: v["highlight/variant"], Color: color("highlight/color"),
		BackgroundColor: color("highlight/backgroundColor"),
	}))}
	child := content.Block{Type: content.BlockParagraph, Text: text}
	block := func(t content.BlockType, id string, a content.Attrs, children ...content.Block) content.Block {
		return content.Block{Type: t, ID: id, Style: style, Attrs: a, Children: children}
	}
	group := func(id string, l content.Layout) content.Block {
		return block(content.BlockGroup, id, content.Attrs{Layout: l}, child, child)
	}
	width := v["grid/minColumnWidth"]
	return content.Blocks{
		{Type: content.BlockParagraph, ID: "b_0paragr1", Style: style, Text: text},
		{Type: content.BlockHeading, ID: "b_0heading", Style: style, Attrs: content.Attrs{Level: 2}, Text: text},
		group("b_00group1", content.Layout{}),
		group("b_00group2", content.Layout{Type: "flow"}),
		group("b_00group3", content.Layout{Type: "row"}),
		group("b_00group4", content.Layout{Type: "stack"}),
		group("b_00grid01", content.Layout{Type: "grid", Columns: columns}),
		group("b_00grid02", content.Layout{Type: "grid", MinColumnWidth: width}),
		group("b_00grid03", content.Layout{Type: "grid", Columns: columns, MinColumnWidth: width}),
		block(content.BlockColumns, "b_columns1", content.Attrs{},
			block(content.BlockColumn, "b_0column1", content.Attrs{Width: v["column/width"]}, child, child)),
		block(content.BlockButtons, "b_buttons1", content.Attrs{},
			block(content.BlockButton, "b_0button1", content.Attrs{Label: "a"})),
		block(content.BlockList, "b_00list01", content.Attrs{},
			content.Block{Type: content.BlockListItem, Children: content.Blocks{child}},
			content.Block{Type: content.BlockListItem, Children: content.Blocks{child}}),
		block(content.BlockQuote, "b_000quote", content.Attrs{Citation: text}, child, child),
		block(content.BlockQuote, "b_000quot2", content.Attrs{}, child, child),
		block(content.BlockSpacer, "b_0spacer1", content.Attrs{Height: length("height")}),
		block(content.BlockImage, "b_000image", content.Attrs{
			Media: officeImage, Decorative: true, Width: v["image/width"], AspectRatio: v["image/aspectRatio"],
		}),
		block(content.BlockTable, "b_000table", content.Attrs{Caption: text}, content.Block{
			Type: content.BlockTableRow, Children: content.Blocks{{Type: content.BlockTableCell, Text: text}},
		}),
		block(content.BlockFile, "b_0000file", content.Attrs{Media: guideMedia}),
		block(content.BlockSeparator, "b_separat1", content.Attrs{}),
		block(content.BlockCode, "b_0000code", content.Attrs{}),
	}
}

// checkStyleTypes returns an error unless each rule of a block in rules, the CSS of bs in the theme s, is one that the
// type of the block can have: properties of the style items that the theme allows for the type; blockGap only for the
// types that can have it, as the gap of flex and grid containers and as the margins between the children of the
// others, inside the blockquote of a quote with a citation; and the CSS of the attributes of the type only.
func checkStyleTypes(rules []cssRule, bs content.Blocks, s *content.ThemeSettings) error {
	blocks := make(map[string]*content.Block)
	var add func(bs content.Blocks)
	add = func(bs content.Blocks) {
		for i := range bs {
			blocks["b-"+strings.TrimPrefix(bs[i].ID, "b_")] = &bs[i]
			add(bs[i].Children)
		}
	}
	add(bs)
	for _, r := range rules {
		if r.form == ".hl-x" {
			continue
		}
		b := blocks[r.class]
		if b == nil {
			return fmt.Errorf("%s: no block has the class", r.selector)
		}
		t, layout := b.Type, b.Attrs.Layout.Type
		gap := t == content.BlockButtons || t == content.BlockColumns ||
			t == content.BlockGroup && (layout == "row" || layout == "stack" || layout == "grid")
		cited := t == content.BlockQuote && len(b.Attrs.Citation) > 0
		// Of the forms and the properties that only some types have, those that t has.
		has := map[string]bool{
			".b-x img": t == content.BlockImage, ".b-x > *": !gap && !cited, ".b-x > * + *": !gap && !cited,
			".b-x > blockquote > *": cited, ".b-x > blockquote > * + *": cited, "gap": gap,
			"height": t == content.BlockSpacer, "flex-basis": t == content.BlockColumn,
			"flex-grow": t == content.BlockColumn, "grid-template-columns": t == content.BlockGroup && layout == "grid",
		}
		if ok, only := has[r.form]; only && !ok {
			return fmt.Errorf("%s: a rule of a %s block", r.selector, t)
		}
		properties := cssForms[formIndex(r.form)].properties
		for _, d := range r.decls {
			if ok, only := has[d.property]; only && !ok && r.form == ".b-x" {
				return fmt.Errorf("%s: %s of a %s block", r.selector, d.property, t)
			}
			i := slices.IndexFunc(properties, func(p cssProperty) bool { return p.name == d.property })
			item := properties[i].item
			if item == "blockGap" && !t.AllowsBlockGap() {
				return fmt.Errorf("%s: blockGap of a %s block", r.selector, t)
			}
			if item != "" {
				if err := s.CheckStyleItem(t, item); err != nil {
					return fmt.Errorf("%s: %s: %w", r.selector, d.property, err)
				}
			}
		}
	}
	return nil
}

// FuzzStyle checks the CSS of any style, highlight and attributes of lengths, valid or not and made without JSON, in
// the block of styleBlocks and the theme that which chooses: one with presets, one that allows only presets for the
// kinds of custom, one without presets, the sample theme, which allows only some style items, or a zero Theme.
// Document renders it without an error, as HTML that checkHTML accepts, with CSS that checkCSS, checkClasses and
// checkStyleTypes accept, and with warnings only about what it ignores. The seeds are added with every choice, but
// those of values that would end the CSS with every block in one theme each. One block in one theme keeps each call
// fast, which minimizing the long inputs needs.
func FuzzStyle(f *testing.F) {
	res := goldenResolver(f)
	golden := goldenThemes(f)
	only := styleTheme + "custom:\n  color: false\n  fontSize: false\n  spacing: false\n"
	var renderers []fuzzRenderer
	for _, gt := range []goldenTheme{
		{"presets", testTheme(f, map[string]string{"theme.yaml": styleTheme})},
		{"presets only", testTheme(f, map[string]string{"theme.yaml": only})},
		golden[0],
		golden[1],
		{"zero", &theme.Theme{}},
	} {
		renderers = append(renderers, fuzzRenderer{gt.name, newRenderer(f, gt.th, "ja", "", res)})
	}
	themes, blocks := len(renderers), len(styleBlocks(nil, 0, 0, 0))
	// input returns the input of value, the fields by name.
	input := func(value func(field string) string) string {
		var fields []string
		for _, name := range styleFields {
			fields = append(fields, value(name))
		}
		return strings.Join(fields, "\x1f")
	}
	// add adds the fields of value and the numbers with each choice.
	add := func(value func(field string) string, weight int, lineHeight float64, columns int) {
		for which := range uint(themes * blocks) {
			f.Add(which, input(value), weight, lineHeight, columns)
		}
	}
	// Presets of styleTheme and of the sample theme, and values written directly.
	presets := map[string]string{
		"textColor/preset": "primary", "backgroundColor/value": "#fff4e5", "linkColor/value": "rgb(30 115 190 / 50%)",
		"gradient/preset": "sunset", "fontSize/preset": "large", "letterSpacing/value": "-0.02em",
		"fontFamily/preset": "body", "padding/top/preset": "30", "padding/right/value": "1rem",
		"padding/bottom/value": "0", "padding/left/value": "5%", "margin/top/value": "-1rem",
		"margin/right/value": "2vw", "margin/bottom/preset": "40", "margin/left/value": "10%", "blockGap/preset": "30",
		"border/width/value": "1px", "border/color/preset": "accent", "border/radius/value": "50%",
		"highlight/color/preset": "accent", "highlight/backgroundColor/value": "#fff4e5", "height/preset": "40",
		"image/width": "50%", "image/aspectRatio": "16/9", "column/width": "33.33%", "grid/minColumnWidth": "12rem",
	}
	// Values written directly.
	direct := map[string]string{
		"textColor/value": "#1E73BE", "backgroundColor/value": "hsl(210deg 50% 40% / 0.5)",
		"linkColor/value": "rgba(0, 0, 0, 0.5)", "fontSize/value": "125%", "letterSpacing/value": "0.1em",
		"padding/top/value": "1rem", "padding/right/value": "2em", "padding/bottom/value": "3px",
		"padding/left/value": "4vw", "margin/top/value": "0", "margin/right/value": "-2px",
		"margin/bottom/value": "1.5rem", "margin/left/value": "-5%", "blockGap/value": "2%",
		"border/width/value": "0", "border/style/value": "dotted", "border/color/value": "#000",
		"border/radius/value": "4px", "highlight/color/value": "#7c2d12", "highlight/backgroundColor/value": "#ffedd5",
		"height/value": "3rem", "image/width": "240px", "image/aspectRatio": "1.5", "column/width": "20rem",
		"grid/minColumnWidth": "0",
	}
	add(func(n string) string { return presets[n] }, 700, 1.5, 3)
	add(func(n string) string { return direct[n] }, 100, 10, 16)
	// Both a preset and a value, and colors with a variant.
	add(func(n string) string {
		if n == "highlight/variant" {
			return "marker"
		}
		return cmp.Or(presets[n], direct[n])
	}, 900, 0.5, 1)
	for _, x := range []struct {
		weight     int
		lineHeight float64
		columns    int
	}{
		{50, 11, 17}, {1000, -1, -1}, {-700, math.NaN(), math.MaxInt}, {901, math.Inf(1), math.MinInt},
		{99, math.SmallestNonzeroFloat64, 0}, {0, 1e-7, 2},
	} {
		add(func(n string) string { return presets[n] }, x.weight, x.lineHeight, x.columns)
	}
	// Values that would end a declaration, a rule or a style element, and values near valid ones: in each field of a
	// preset or a variant, and in each other field; with each block, in one of the themes in turn.
	for i, a := range []string{
		";", "{", "}", "red;}p{", "#fff;}", "1rem}", "1rem{", "</style>", `\`, `\31 rem`, `"`, "'", `"x"`, "url(x)",
		"#fff !important", "1rem!important", "1rem\n", "\n}", "\x00", "#fff\x00", "\xff", "\xed\xa0\x80", "/**/",
		"var(--color-primary)", "expression(x)", "calc(1rem)", "1rem 2rem", " 1rem", "1rem ", "16/9;}", "a b", "--x",
		"-1rem", "1", "0.0", "1e3px", "+1px", "\xef\xbc\x91rem",
	} {
		for _, named := range []bool{true, false} {
			fields := input(func(n string) string {
				if (strings.HasSuffix(n, "/preset") || strings.HasSuffix(n, "/variant")) == named {
					return a
				}
				return ""
			})
			for b := range blocks {
				f.Add(uint(b*themes+i%themes), fields, 0, 0.0, 0)
			}
		}
	}
	f.Fuzz(func(t *testing.T, which uint, fields string, weight int, lineHeight float64, columns int) {
		v := make(map[string]string)
		for i, s := range strings.Split(fields, "\x1f") {
			if i < len(styleFields) {
				v[styleFields[i]] = s
			}
		}
		x := renderers[which%uint(len(renderers))]
		bs := styleBlocks(v, weight, lineHeight, columns)
		bs = content.Blocks{bs[which/uint(len(renderers))%uint(len(bs))]}
		in := fmt.Sprintf("Document() of the %s block of %q, %d, %v, %d with the %s theme",
			bs[0].Type, fields, weight, lineHeight, columns, x.theme)
		lists, css, warnings, err := x.r.Document(bs)
		if err != nil || len(lists) != 1 {
			t.Fatalf("%s = %d lists, %v", in, len(lists), err)
		}
		if err := checkHTML(string(lists[0])); err != nil {
			t.Errorf("%s:\n%s\n%v", in, lists[0], err)
		}
		rules, err := checkCSS(css.String(), x.r.settings)
		if err == nil {
			err = checkClasses([]string{string(lists[0])}, rules, x.r.settings, nil)
		}
		if err == nil {
			err = checkStyleTypes(rules, bs, x.r.settings)
		}
		if err != nil {
			t.Errorf("%s:\n%s\nCSS:\n%s\n%v", in, lists[0], css, err)
		}
		for _, w := range warnings {
			if w.List != 0 || !strings.HasPrefix(w.Path, "/") ||
				!strings.HasSuffix(w.Msg, "; ignored") && !strings.HasSuffix(w.Msg, "; rendered without its class") {
				t.Errorf("%s: warning %q", in, w)
			}
		}
	})
}

// FuzzSiteCSS checks that SiteCSS writes the presets whose names and values are valid, and no others, as custom
// properties of :root, in the order of the kinds of content.Presets and then in their own, and then classes.css with
// the mobileWidth if it is valid, else the default, for any presets and mobileWidth. The fields of presets, separated
// by "\x1f", are pairs of a name and a value, of each kind in turn.
func FuzzSiteCSS(f *testing.F) {
	kinds := []string{"color", "fontSize", "spacing", "fontFamily", "gradient", "shadow"}
	for _, s := range []struct {
		presets []string
		width   string
	}{
		{[]string{
			"primary", "#1e73be", "large", "clamp(1.25rem, 1rem + 1vw, 1.5rem)", "30", "1rem",
			"body", `"Noto Sans JP", sans-serif`, "sunset", "linear-gradient(135deg, #f093fb 0%, #f5576c 100%)",
			"soft", "0 2px 4px rgba(0, 0, 0, 0.1)", "accent", "#e85d04", "small", "0.875rem",
		}, "600px"},
		// The same name twice, and empty names and values.
		{[]string{"a", "red", "", "", "", "1rem", "x", "", "", "", "", "", "a", "blue"}, "48.75em"},
		{[]string{
			"a b", "red", "a;b", "red", "a:b", "red", "a}", "red", "a\n", "red", "a\x00", "red", "\xff", "red",
			"x", "red; color: blue", "x", "red}", "x", "{", "x", " red", "x", "#000 !important", "x", "a\nb",
			"x", "/* c */ red", "x", "url(x.png)", "x", "</style>", "x", `"a`, "x", "(red", "x", `\31 23`,
			"x", "\x00", "x", "\xff", "x", `"a;}b"`, "x", `image-set("x.png" 1x)`,
		}, ".5rem"},
	} {
		f.Add(strings.Join(s.presets, "\x1f"), s.width)
	}
	for _, w := range []string{
		"", "781px", "0px", "600", "-600px", "+600px", "1e3px", "600PX", " 600px", "600px;", "600px}",
		"600px) and (min-width: 1px", "calc(600px)", "{{.MobileWidth}}", "\x00", "\xff",
	} {
		f.Add("primary\x1f#1e73be", w)
	}
	f.Fuzz(func(t *testing.T, presets, width string) {
		s := &content.ThemeSettings{MobileWidth: width}
		fields := strings.Split(presets, "\x1f")
		for i := 0; i+1 < len(fields); i += 2 {
			p := kindPresets(&s.Presets, kinds[i/2%len(kinds)])
			*p = append(*p, content.Preset{Name: fields[i], Value: fields[i+1]})
		}
		var root strings.Builder
		for _, k := range kinds {
			for _, p := range *kindPresets(&s.Presets, k) {
				if content.CheckName(p.Name) == nil && content.CheckPresetValue(p.Value) == nil {
					root.WriteString("  " + presetPrefixes[k] + p.Name + ": " + p.Value + ";\n")
				}
			}
		}
		want := ""
		if root.Len() > 0 {
			want = ":root {\n" + root.String() + "}\n\n"
		}
		if content.CheckMobileWidth(width) != nil {
			width = content.DefaultMobileWidth
		}
		want += strings.Replace(classesCSS, "{{.MobileWidth}}", width, 1)
		if got := SiteCSS(s); got != want {
			t.Errorf("SiteCSS() of the presets %q and the mobileWidth %q =\n%s\nwant\n%s",
				presets, s.MobileWidth, got, want)
		}
	})
}

// TestCheckCSS checks that checkCSS accepts the CSS of the golden files and CSS of every form, and rejects CSS that
// differs from that by a replacement.
func TestCheckCSS(t *testing.T) {
	for _, gt := range goldenThemes(t) {
		for _, p := range goldenDocuments {
			doc := strings.TrimSuffix(filepath.Base(p), ".json")
			name := filepath.Join(goldenOut, gt.name, goldenSites[0].language, doc+".css")
			b, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := checkCSS(string(b), gt.th.Settings); err != nil {
				t.Errorf("checkCSS() of %s error: %v", name, err)
			}
		}
	}
	s := &content.ThemeSettings{
		Presets: content.Presets{
			Color: testPresets("primary", "#1e73be"), FontSize: testPresets("large", "2rem"),
			Spacing: testPresets("30", "1rem"), FontFamily: testPresets("body", "serif"),
			Gradient: testPresets("sunset", "linear-gradient(#fff, #000)"),
		},
		PresetsOnly: []string{"fontSize"},
	}
	const valid = `.b-0paragr1 {
  color: var(--color-primary) !important;
  background-color: #fff4e5 !important;
  background-image: var(--gradient-sunset) !important;
  font-size: var(--font-size-large) !important;
  letter-spacing: -0.02em !important;
  font-family: var(--font-family-body) !important;
  font-weight: 700 !important;
  line-height: 1.5 !important;
  padding-top: var(--spacing-30) !important;
  padding-right: 5% !important;
  margin-top: -1rem !important;
  margin-left: 2vw !important;
  border-width: 1px !important;
  border-color: rgb(0 0 0 / 50%) !important;
  border-radius: 50% !important;
}

:where(.b-0paragr1) {
  border-style: solid;
}

.b-0paragr1 :where(a) {
  color: #c00 !important;
}

.hl-065c532e {
  color: var(--color-primary) !important;
}

.b-00grid01 {
  gap: var(--spacing-30) !important;
  grid-template-columns: repeat(auto-fill, minmax(max(min(10rem, 100%), (100% - 2 * var(--spacing-30)) / 3), 1fr)) !important;
}

.b-00grid02 {
  grid-template-columns: repeat(auto-fill, minmax(max(min(10rem, 100%), (100% - 1 * 0.5em) / 2), 1fr)) !important;
}

.b-00grid03 {
  grid-template-columns: repeat(16, minmax(0, 1fr)) !important;
}

.b-00grid04 {
  grid-template-columns: repeat(auto-fill, minmax(min(0, 100%), 1fr)) !important;
}

.b-0column1 {
  border-style: dashed !important;
  flex-basis: 33.33% !important;
  flex-grow: 0 !important;
}

.b-0column1 > * {
  margin-top: 0 !important;
  margin-bottom: 0 !important;
}

.b-0column1 > * + * {
  margin-top: 1rem !important;
}

.b-000quote > blockquote > * {
  margin-top: 0 !important;
  margin-bottom: 0 !important;
}

.b-000quote > blockquote > * + * {
  margin-top: var(--spacing-30) !important;
}

.b-000image {
  border-width: 2px !important;
  border-style: dotted !important;
}

.b-000image img {
  width: 50% !important;
  aspect-ratio: 16/9 !important;
  object-fit: cover !important;
  height: auto !important;
}

.b-0spacer1 {
  height: var(--spacing-30) !important;
}
`
	const (
		hl         = ".hl-065c532e {\n  color: var(--color-primary) !important;\n}\n"
		links      = ".b-0paragr1 :where(a) {\n  color: #c00 !important;\n}\n"
		borders    = ":where(.b-0paragr1) {\n  border-style: solid;\n}\n"
		spacer     = ".b-0spacer1 {\n  height: var(--spacing-30) !important;\n}\n"
		children   = ".b-0column1 > * {\n  margin-top: 0 !important;\n  margin-bottom: 0 !important;\n}\n"
		next       = ".b-0column1 > * + * {\n  margin-top: 1rem !important;\n}\n"
		quoted     = ".b-0column1 > blockquote > * {\n  margin-top: 0 !important;\n  margin-bottom: 0 !important;\n}\n"
		quotedNext = ".b-0column1 > blockquote > * + * {\n  margin-top: 1rem !important;\n}\n"
	)
	for _, css := range []string{valid, "", hl} {
		if _, err := checkCSS(css, s); err != nil {
			t.Errorf("checkCSS() of\n%s\nerror: %v", css, err)
		}
	}
	direct := ".b-0paragr1 {\n  color: #fff !important;\n  font-size: 1rem !important;\n}\n"
	if _, err := checkCSS(direct, nil); err != nil {
		t.Errorf("checkCSS() of\n%s\nwithout a theme error: %v", direct, err)
	}
	if _, err := checkCSS(hl, nil); err == nil {
		t.Errorf("checkCSS() accepts a preset without a theme:\n%s", hl)
	}
	for _, css := range []string{valid + "\n", strings.TrimSuffix(valid, "\n"), "\n" + valid, "\n", " "} {
		if _, err := checkCSS(css, s); err == nil {
			t.Errorf("checkCSS() accepts %q", css)
		}
	}
	tests := []struct{ name, old, new string }{
		{"a declaration without a semicolon", "  font-weight: 700 !important;", "  font-weight: 700 !important"},
		{"a declaration on two lines", "  font-weight: 700 !important;", "  font-weight:\n700 !important;"},
		{"a blank line in a rule", "  font-weight: 700 !important;", "\n  font-weight: 700 !important;"},
		{"an indentation of 4 spaces", "  font-weight: 700 !important;", "    font-weight: 700 !important;"},
		{"a tab", "  font-weight: 700 !important;", "\tfont-weight: 700 !important;"},
		{"a space before the colon", "font-weight: 700", "font-weight : 700"},
		{"two spaces after the colon", "font-weight: 700", "font-weight:  700"},
		{"a declaration on the line of the selector", ".b-0spacer1 {\n", ".b-0spacer1 { height: 0 !important;\n"},
		{"no space before {", ".b-0spacer1 {", ".b-0spacer1{"},
		{"no blank line between rules", "}\n\n.b-0spacer1 {", "}\n.b-0spacer1 {"},
		{"two blank lines between rules", "}\n\n.b-0spacer1 {", "}\n\n\n.b-0spacer1 {"},
		{"a rule without declarations", "  height: var(--spacing-30) !important;\n", ""},
		{"a rule in a rule", "  height: var(--spacing-30) !important;\n",
			"  .b-0spacer2 {\n  height: 0 !important;\n  }\n"},
		{"an unknown selector", ".b-000image img {", ".b-000image p {"},
		{"a selector of two classes", ".b-0spacer1 {", ".b-0spacer1, .b-0spacer2 {"},
		{"a selector of an element", ".b-0spacer1 {", "body {"},
		{"a class in upper case", ".b-0spacer1 {", ".b-0SPACER1 {"},
		{"a class of 9 characters", ".b-0spacer1 {", ".b-0spacer12 {"},
		{"a class of a highlight not in hex", ".hl-065c532e", ".hl-065c532g"},
		{"a class of a highlight not of its colors", ".hl-065c532e", ".hl-065c532f"},
		{"a highlight with a property of blocks", hl, ".hl-a3a0d7b7 {\n  font-weight: 700 !important;\n}\n"},
		{"no !important", "  height: var(--spacing-30) !important;", "  height: var(--spacing-30);"},
		{"!important without a space", "letter-spacing: -0.02em !important", "letter-spacing: -0.02em!important"},
		{"!important twice", "margin-left: 2vw !important", "margin-left: 2vw !important !important"},
		{"!important in the default style of borders", "  border-style: solid;", "  border-style: solid !important;"},
		{"a default style of borders other than solid", "  border-style: solid;", "  border-style: dashed;"},
		{"a preset that the theme does not have", "var(--gradient-sunset)", "var(--gradient-dawn)"},
		{"a preset of another kind", "padding-top: var(--spacing-30)", "padding-top: var(--color-primary)"},
		{"a kind of presets not in kebab case", "var(--font-size-large)", "var(--fontSize-large)"},
		{"a preset without var()", "padding-top: var(--spacing-30)", "padding-top: --spacing-30"},
		{"a preset where there are none", "letter-spacing: -0.02em", "letter-spacing: var(--spacing-30)"},
		{"a value where there are only presets", "font-family: var(--font-family-body)", "font-family: serif"},
		{"a value of a kind of only presets", "font-size: var(--font-size-large)", "font-size: 2rem"},
		{"an invalid color", "background-color: #fff4e5", "background-color: red"},
		{"a color that ends its rule", "background-color: #fff4e5", "background-color: #fff4e5;}body{color:red"},
		{"url()", "background-color: #fff4e5", "background-color: url(x)"},
		{"a comment", "margin-top: -1rem", "margin-top: -1rem /* x */"},
		{"an escape", "margin-top: -1rem", `margin-top: -1\72 em`},
		{"NUL", "margin-left: 2vw", "margin-left: 2vw\x00"},
		{"invalid UTF-8", "margin-left: 2vw", "margin-left: 2v\xffw"},
		{"a negative padding", "padding-right: 5%", "padding-right: -5%"},
		{"a percentage of border-width", "border-width: 1px", "border-width: 1%"},
		{"an unknown border style", "border-style: dotted", "border-style: double"},
		{"a font weight out of range", "font-weight: 700", "font-weight: 1000"},
		{"a font weight with a zero first", "font-weight: 700", "font-weight: 0700"},
		{"a line height out of range", "line-height: 1.5", "line-height: 11"},
		{"a line height with an exponent", "line-height: 1.5", "line-height: 15e-1"},
		{"an invalid aspect ratio", "aspect-ratio: 16/9", "aspect-ratio: 16/0"},
		{"an image without object-fit", "  object-fit: cover !important;\n", ""},
		{"an image without its height", "  height: auto !important;\n", ""},
		{"an image of another height", "height: auto", "height: 100%"},
		{"an image of another fit", "object-fit: cover", "object-fit: contain"},
		{"an image of only its height",
			"  width: 50% !important;\n  aspect-ratio: 16/9 !important;\n  object-fit: cover !important;\n", ""},
		{"a column without flex-grow", "  flex-grow: 0 !important;\n", ""},
		{"a column that grows", "flex-grow: 0", "flex-grow: 1"},
		{"a property of another form", "  height: auto !important;\n",
			"  height: auto !important;\n  color: #000 !important;\n"},
		{"an unknown property", "font-weight: 700", "font-style: 700"},
		{
			"properties out of order",
			"  color: var(--color-primary) !important;\n  background-color: #fff4e5 !important;\n",
			"  background-color: #fff4e5 !important;\n  color: var(--color-primary) !important;\n",
		},
		{"a property twice", "  margin-top: -1rem !important;\n",
			"  margin-top: -1rem !important;\n  margin-top: 0 !important;\n"},
		{"two rules of one selector", spacer, spacer + "\n" + spacer},
		{"rules of a class apart", links + "\n" + hl, hl + "\n" + links},
		{"rules of a block out of order", borders + "\n" + links, links + "\n" + borders},
		{"the margins between children without those of the next ones", "\n" + next, ""},
		{"the margins of the next children alone", "\n" + children, ""},
		{"the margins between children of two forms", next, next + "\n" + quoted + "\n" + quotedNext},
		{"the margins between children with a gap", "  border-style: dashed !important;\n",
			"  gap: 1rem !important;\n  border-style: dashed !important;\n"},
		{"margins of children that are not 0", children,
			strings.Replace(children, "margin-top: 0", "margin-top: 1px", 1)},
		{"a width of borders without the default style", borders + "\n", ""},
		{"the default style without a width or a color of borders",
			"  border-width: 1px !important;\n  border-color: rgb(0 0 0 / 50%) !important;\n", ""},
		{"the default style with a style of borders", "  border-width: 1px !important;\n",
			"  border-width: 1px !important;\n  border-style: solid !important;\n"},
		{"a grid with the default gap that has a gap", "(100% - 2 * var(--spacing-30)) / 3", "(100% - 2 * 0.5em) / 3"},
		{"a grid with a gap that has none", "(100% - 1 * 0.5em) / 2", "(100% - 1 * 1em) / 2"},
		{"a grid with n gaps", "(100% - 1 * 0.5em) / 2", "(100% - 2 * 0.5em) / 2"},
		{"a grid of 17 columns", "repeat(16, minmax(0, 1fr))", "repeat(17, minmax(0, 1fr))"},
		{"a grid of 0 columns", "repeat(16, minmax(0, 1fr))", "repeat(0, minmax(0, 1fr))"},
		{"a grid of columns with a zero first", "repeat(16, minmax(0, 1fr))", "repeat(016, minmax(0, 1fr))"},
		{"a grid of another form", "repeat(16, minmax(0, 1fr))", "repeat(16, 1fr)"},
		{"a grid of columns in percent", "minmax(min(0, 100%), 1fr)", "minmax(min(10%, 100%), 1fr)"},
		{"a grid of columns that end its rule", "minmax(min(0, 100%), 1fr)", "minmax(min(1rem;}, 100%), 1fr)"},
	}
	for _, tt := range tests {
		if n := strings.Count(valid, tt.old); n != 1 {
			t.Errorf("%s: %q is in the CSS %d times, want once", tt.name, tt.old, n)
			continue
		}
		css := strings.Replace(valid, tt.old, tt.new, 1)
		if _, err := checkCSS(css, s); err == nil {
			t.Errorf("%s: checkCSS() accepts\n%s", tt.name, css)
		}
	}
}
