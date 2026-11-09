package wpimport

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/html"
	"github.com/WhiteRoomLab/whiteroom/internal/wpblock"
)

// noBlockNames stands for the name tables of settings that there are not: every name stays as it is.
var noBlockNames BlockSettings

// blockNames returns the name tables of the settings of the import (BlockSettings).
func (c *bodyConv) blockNames() *BlockSettings {
	if c.opts.Settings == nil {
		return &noBlockNames
	}
	return &c.opts.Settings.Blocks
}

// nameResult tells what a table of names makes of a name.
type nameResult int

const (
	nameMapped      nameResult = iota // it is a name in the theme
	nameToNothing                     // the table says that it is not imported
	nameNotAccepted                   // it, or the name that the table gives, is not a name that whiteroom accepts
)

// lookupName finds the name in the theme of name, which the table m maps: a name that is not in m stays as it is.
func lookupName(m map[string]string, name string) (string, nameResult) {
	to, ok := m[name]
	if !ok {
		to = name
	} else if to == "" {
		return "", nameToNothing
	}
	if content.CheckName(to) != nil {
		return "", nameNotAccepted
	}
	return to, nameMapped
}

// mapName returns the name in the theme of name, a name of WordPress, by the table m of the settings, or false if it
// is not imported. A name that m does not have stays as it is if whiteroom accepts it as a name. The settings may be
// absent, and m is then nil. The caller notes why a name is not imported.
func (c *bodyConv) mapName(m map[string]string, name string) (string, bool) {
	to, r := lookupName(m, name)
	return to, r == nameMapped
}

// presetKind is a kind of preset: key is how the settings and the report call it, and wp how the references of
// WordPress do.
type presetKind struct{ key, wp string }

var (
	kindColor      = presetKind{"color", "color"}
	kindGradient   = presetKind{"gradient", "gradient"}
	kindFontSize   = presetKind{"fontSize", "font-size"}
	kindFontFamily = presetKind{"fontFamily", "font-family"}
	kindSpacing    = presetKind{"spacing", "spacing"}
)

var presetKinds = []presetKind{kindColor, kindGradient, kindFontSize, kindFontFamily, kindSpacing}

// presetKindOf returns the kind that WordPress calls wp in a reference, such as font-size.
func presetKindOf(wp string) (presetKind, bool) {
	for _, k := range presetKinds {
		if k.wp == wp {
			return k, true
		}
	}
	return presetKind{}, false
}

// of returns the table of the settings for the presets of the kind.
func (p *PresetSettings) of(k presetKind) map[string]string {
	switch k.key {
	case kindColor.key:
		return p.Color
	case kindGradient.key:
		return p.Gradient
	case kindFontSize.key:
		return p.FontSize
	case kindFontFamily.key:
		return p.FontFamily
	}
	return p.Spacing
}

// presetMap returns the table of the settings for the presets of the kind.
func (c *bodyConv) presetMap(k presetKind) map[string]string {
	return c.blockNames().Presets.of(k)
}

// presetName returns the name in the theme of the preset slug of the kind. If it is not imported, it notes at at why, and
// returns false.
func (c *bodyConv) presetName(k presetKind, slug string, at noteAt) (string, bool) {
	to, r := lookupName(c.presetMap(k), slug)
	switch r {
	case nameMapped:
		return to, true
	case nameToNothing:
		c.note(at, Skipped, "the %s preset %q is not imported: blocks.presets.%s maps it to nothing", k.key, slug, k.key)
	case nameNotAccepted:
		c.note(at, Skipped, "the %s preset %q is not imported: it is not a name that whiteroom accepts", k.key, slug)
	}
	return "", false
}

// variantName returns the name in the theme of the block style name. If it is not imported, it notes at at why, and
// returns false.
func (c *bodyConv) variantName(name string, at noteAt) (string, bool) {
	to, r := lookupName(c.blockNames().Variants, name)
	switch r {
	case nameMapped:
		return to, true
	case nameToNothing:
		c.note(at, Skipped, "the block style %q is not imported: blocks.variants maps it to nothing", name)
	case nameNotAccepted:
		c.note(at, Skipped, "the block style %q is not imported: it is not a name that whiteroom accepts", name)
	}
	return "", false
}

// presetRefPattern matches a reference to a preset of WordPress: var:preset|color|vivid-red, which the attributes of
// the blocks have, or var(--wp--preset--color--vivid-red), which the themes and the plugins write.
var presetRefPattern = regexp.MustCompile(`var:preset\|([a-z]+(?:-[a-z]+)*)\|([A-Za-z0-9_-]+)|var\(--wp--preset--([a-z]+(?:-[a-z]+)*)--([A-Za-z0-9_-]+)\)`)

// presetRef reads s, all of it, as a reference to a preset, and returns the kind as WordPress calls it, and the slug.
func presetRef(s string) (kind, slug string, ok bool) {
	s = strings.TrimSpace(s)
	m := presetRefPattern.FindStringSubmatch(s)
	switch {
	case m == nil || m[0] != s:
		return "", "", false
	case m[1] != "":
		return m[1], m[2], true
	}
	return m[3], m[4], true
}

// keyText writes the name of a setting for the report: as it is, if it is made of the characters that the names of
// settings have, and quoted otherwise, so that the report stays on one line.
func keyText(s string) string {
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-', c == ':':
		default:
			return strconv.Quote(s)
		}
	}
	if s == "" {
		return strconv.Quote(s)
	}
	return s
}

// lost notes the settings of the block called name in WordPress that whiteroom has no counterpart of. The calls for the
// same place and block make one note, which names each once.
func (c *bodyConv) lost(at noteAt, name string, items ...string) {
	keys := make([]string, len(items))
	for i, item := range items {
		keys[i] = keyText(item)
	}
	c.noteItems(at, Skipped, "lost ", fmt.Sprintf(" of the block %q; whiteroom has no such settings", name), keys...)
}

// textAlign returns the alignment of the text that the attributes a tell, from style.typography.textAlign, textAlign,
// and align, the first that is there; the align of a paragraph is that of its text only if it is left, center, right, or
// justify (wide and full are the alignment of the block). aligned is left, center or right; other is any other, such as
// justify, which whiteroom has not, for the caller to note.
func textAlign(a *wpblock.Value) (aligned, other string) {
	for i, v := range []*wpblock.Value{a.Get("style").Get("typography").Get("textAlign"), a.Get("textAlign"), a.Get("align")} {
		s, ok := v.Str()
		switch {
		case !ok || s == "":
		case s == "left" || s == "center" || s == "right":
			return s, ""
		case i < 2 || s == "justify":
			return "", s
		}
	}
	return "", ""
}

// props are what every block has beside its type and its content.
type props struct {
	anchor, className, variant string
	style                      content.Style
}

// props reads the settings that the block b of WordPress has in common with the blocks of whiteroom of the type t: the
// anchor, which root, the root element of its HTML, has as its id; the block style and the additional class, which its
// attribute className has; and the style, which its attributes, such as textColor and style, have. What whiteroom has no
// counterpart of, and what it does not accept, it notes at at, and leaves out. The parts of a list and a table have
// none of these, so the caller reads those of a list item and a table from the type that carries them.
func (c *bodyConv) props(b *wpblock.Block, root *html.Node, t content.BlockType, at noteAt) props {
	if t == content.BlockListItem || t == content.BlockTableRow || t == content.BlockTableCell {
		return props{}
	}
	p := props{anchor: c.anchorOf(root, at)}
	p.variant, p.className = c.classesOf(b, t, at)
	r := &styleReader{c: c, b: b, t: t, at: at}
	r.read()
	c.lost(at, b.Name, r.lost...)
	p.style = r.st
	return p
}

// anchorOf returns the id of root as an anchor, if whiteroom accepts it and no other block has it.
func (c *bodyConv) anchorOf(root *html.Node, at noteAt) string {
	if root == nil {
		return ""
	}
	id := attr(root, "id")
	if id == "" {
		return ""
	}
	if err := content.CheckName(id); err != nil {
		c.note(at, Skipped, "the anchor %q is not imported: %s", id, err)
		return ""
	}
	if c.anchors[id] {
		c.note(at, Skipped, "the anchor %q is not imported: another block has it", id)
		return ""
	}
	c.anchors[id] = true
	c.anchorLog = append(c.anchorLog, id)
	return id
}

// classesOf reads the attribute className: the first block style (is-style-) is the variant, and the first of the other
// classes, the class name. A block has one of each.
func (c *bodyConv) classesOf(b *wpblock.Block, t content.BlockType, at noteAt) (variant, className string) {
	name, _ := b.Attrs.Get("className").Str()
	seen := make(map[string]bool)
	styles := 0
	var others, rest []string
	for _, class := range strings.FieldsFunc(name, isHTMLSpace) {
		if seen[class] {
			continue
		}
		seen[class] = true
		style, isStyle := strings.CutPrefix(class, "is-style-")
		switch {
		case isStyle && style == "default":
		case isStyle && style != "":
			if styles++; styles == 1 {
				variant, _ = c.variantName(style, at)
			} else {
				c.note(at, Skipped, "the block style %q is not imported: a block has one style", style)
			}
		case t == content.BlockEmbed && (strings.HasPrefix(class, "wp-embed-aspect-") || class == "wp-has-aspect-ratio"):
		default:
			others = append(others, class)
		}
	}
	for _, class := range others {
		if err := content.CheckName(class); err != nil {
			c.note(at, Skipped, "the class %q is not imported: %s", class, err)
		} else if content.ReservedClass(class) {
			c.note(at, Skipped, "the class %q is not imported: the core gives classes of the forms b-xxxxxxxx and hl-xxxxxxxx", class)
		} else if className == "" {
			className = class
		} else {
			rest = append(rest, class)
		}
	}
	switch len(rest) {
	case 0:
	case 1:
		c.note(at, Skipped, "the class %q is not imported: whiteroom blocks have one class name", rest[0])
	default:
		c.note(at, Skipped, "the classes %s are not imported: whiteroom blocks have one class name", quotedList(rest))
	}
	return variant, className
}

// quotedList writes the first maxLostShown of items, quoted and separated by ", ", and the number of the others.
func quotedList(items []string) string {
	shown := items[:min(len(items), maxLostShown)]
	quoted := make([]string, len(shown))
	for i, s := range shown {
		quoted[i] = strconv.Quote(s)
	}
	s := strings.Join(quoted, ", ")
	if more := len(items) - len(shown); more > 0 {
		s += fmt.Sprintf(", and %d more", more)
	}
	return s
}

// styleReader reads the style of one block.
type styleReader struct {
	c    *bodyConv
	b    *wpblock.Block
	t    content.BlockType
	at   noteAt
	st   content.Style
	lost []string // the settings that whiteroom has no counterpart of
}

// styleKeys are the keys of the attribute style that are read, by the path of the object that has them. The others are
// lost.
var styleKeys = map[string][]string{
	"":                    {"color", "typography", "spacing", "border", "elements"},
	"color":               {"text", "background", "gradient"},
	"typography":          {"fontSize", "fontFamily", "fontWeight", "lineHeight", "letterSpacing", "textAlign"},
	"spacing":             {"padding", "margin", "blockGap"},
	"spacing.padding":     {"top", "right", "bottom", "left"},
	"spacing.margin":      {"top", "right", "bottom", "left"},
	"spacing.blockGap":    {"top", "left"},
	"border":              {"width", "style", "color", "radius", "top", "right", "bottom", "left"},
	"border.top":          {"width", "style", "color"},
	"border.right":        {"width", "style", "color"},
	"border.bottom":       {"width", "style", "color"},
	"border.left":         {"width", "style", "color"},
	"border.radius":       {"topLeft", "topRight", "bottomLeft", "bottomRight"},
	"elements":            {"link"},
	"elements.link":       {"color"},
	"elements.link.color": {"text"},
}

// buttonKeys are the keys that the conversion of a button reads besides: its width.
var buttonKeys = map[string][]string{"": {"dimensions"}, "dimensions": {"width"}}

// keysAt returns the keys that are read in the object at path of the attribute style, and whether any are.
func (r *styleReader) keysAt(path string) ([]string, bool) {
	keys, ok := styleKeys[path]
	if extra, has := buttonKeys[path]; has && r.t == content.BlockButton {
		return append(slices.Clone(keys), extra...), true
	}
	return keys, ok
}

// lostKeys collects the keys of the object v, which is at path in the style, that are not read.
func (r *styleReader) lostKeys(v *wpblock.Value, path string) {
	if v == nil || v.Kind != wpblock.Object {
		return
	}
	keys, _ := r.keysAt(path)
	for i := range v.Object {
		m := &v.Object[i]
		name, sub := "style."+m.Name, m.Name
		if path != "" {
			name, sub = "style."+path+"."+m.Name, path+"."+m.Name
		}
		if !slices.Contains(keys, m.Name) {
			r.lost = append(r.lost, name)
		} else if _, deeper := r.keysAt(sub); deeper {
			r.lostKeys(&m.Value, sub)
		}
	}
}

// scalarText returns the text of v, if it is a string or a number as it is written, without the space around it.
func scalarText(v *wpblock.Value) (string, bool) {
	if s, ok := v.Str(); ok {
		return strings.TrimSpace(s), true
	}
	if v != nil && v.Kind == wpblock.Number {
		return v.Number, true
	}
	return "", false
}

// lengthText is scalarText, except that a number is a length in pixels.
func lengthText(v *wpblock.Value) (string, bool) {
	text, ok := scalarText(v)
	if ok && v.Kind == wpblock.Number {
		text += "px"
	}
	return text, ok
}

// first returns the first of the values that is there.
func first(vs ...*wpblock.Value) *wpblock.Value {
	for _, v := range vs {
		if v != nil {
			return v
		}
	}
	return nil
}

func (r *styleReader) drop(item, written, why string) {
	r.c.notImported(r.at, item, written, why)
}

// styleProblem returns why st is not a valid style, or "" if it is.
func styleProblem(st content.Style) string {
	err := st.Validate()
	if err == nil {
		return ""
	}
	var errs content.Errors
	if errors.As(err, &errs) && len(errs) > 0 {
		return errs[0].Msg
	}
	return err.Error()
}

// try puts what set puts in a style into the style of the block, unless the rules of the style do not accept it. If they
// do not, it notes that the item, which was written as written, is not imported.
func (r *styleReader) try(item, written string, set func(*content.Style)) bool {
	candidate := r.st
	set(&candidate)
	if why := styleProblem(candidate); why != "" {
		r.drop(item, written, why)
		return false
	}
	r.st = candidate
	return true
}

func (r *styleReader) read() {
	attrs := r.b.Attrs
	style := attrs.Get("style")
	if style != nil && style.Kind != wpblock.Object {
		if style.Kind != wpblock.Null {
			r.lost = append(r.lost, "style")
		}
		style = nil
	}
	r.lostKeys(style, "")
	colors, typography, spacing, border := style.Get("color"), style.Get("typography"), style.Get("spacing"), style.Get("border")

	r.color("textColor", attrs.Get("textColor"), first(colors.Get("text"), attrs.Get("customTextColor")),
		func(st *content.Style, c content.Color) { st.TextColor = c })
	r.color("backgroundColor", attrs.Get("backgroundColor"), first(colors.Get("background"), attrs.Get("customBackgroundColor")),
		func(st *content.Style, c content.Color) { st.BackgroundColor = c })
	r.color("linkColor", nil, style.Get("elements").Get("link").Get("color").Get("text"),
		func(st *content.Style, c content.Color) { st.LinkColor = c })
	r.gradient(attrs.Get("gradient"), first(colors.Get("gradient"), attrs.Get("customGradient")))
	r.fontSize(attrs.Get("fontSize"), first(typography.Get("fontSize"), attrs.Get("customFontSize")))
	r.fontFamily(attrs.Get("fontFamily"), typography.Get("fontFamily"))
	r.letterSpacing(typography.Get("letterSpacing"))
	r.fontWeight(typography.Get("fontWeight"))
	r.lineHeight(typography.Get("lineHeight"))
	r.sides("padding", spacing.Get("padding"), func(st *content.Style, side string) *content.Length { return paddingSide(st, side) })
	r.sides("margin", spacing.Get("margin"), func(st *content.Style, side string) *content.Length { return marginSide(st, side) })
	r.blockGap(spacing.Get("blockGap"))
	r.border(attrs.Get("borderColor"), border)
}

func paddingSide(st *content.Style, side string) *content.Length {
	switch side {
	case "top":
		return &st.Padding.Top
	case "right":
		return &st.Padding.Right
	case "bottom":
		return &st.Padding.Bottom
	}
	return &st.Padding.Left
}

func marginSide(st *content.Style, side string) *content.Length {
	switch side {
	case "top":
		return &st.Margin.Top
	case "right":
		return &st.Margin.Right
	case "bottom":
		return &st.Margin.Bottom
	}
	return &st.Margin.Left
}

// preset reads text, a reference to a preset of the kind, as the name of a preset in the theme. It returns false, and
// notes why, if it is not imported; ref is false if text is not a reference to a preset of the kind.
func (r *styleReader) preset(k presetKind, text string) (name string, ref, ok bool) {
	kind, slug, isRef := presetRef(text)
	if !isRef || kind != k.wp {
		return "", false, false
	}
	name, ok = r.c.presetName(k, slug, r.at)
	return name, true, ok
}

// color reads a color: slug is the attribute that names a preset of the theme of WordPress, and direct the value in
// the style, a reference to a preset or a color written directly.
func (r *styleReader) color(item string, slug, direct *wpblock.Value, set func(*content.Style, content.Color)) {
	if s, ok := slug.Str(); ok && s != "" {
		if name, ok := r.c.presetName(kindColor, s, r.at); ok {
			r.try(item, s, func(st *content.Style) { set(st, content.Color{Preset: name}) })
		}
		return
	}
	text, ok := scalarText(direct)
	if !ok || text == "" {
		return
	}
	if name, ref, ok := r.preset(kindColor, text); ref {
		if ok {
			r.try(item, text, func(st *content.Style) { set(st, content.Color{Preset: name}) })
		}
		return
	}
	r.try(item, text, func(st *content.Style) { set(st, content.Color{Value: text}) })
}

func (r *styleReader) gradient(slug, direct *wpblock.Value) {
	if s, ok := slug.Str(); ok && s != "" {
		if name, ok := r.c.presetName(kindGradient, s, r.at); ok {
			r.try("gradient", s, func(st *content.Style) { st.Gradient = content.Gradient{Preset: name} })
		}
		return
	}
	text, ok := scalarText(direct)
	if !ok || text == "" {
		return
	}
	name, ref, ok := r.preset(kindGradient, text)
	switch {
	case !ref:
		r.drop("gradient", text, "whiteroom has gradient presets only")
	case ok:
		r.try("gradient", text, func(st *content.Style) { st.Gradient = content.Gradient{Preset: name} })
	}
}

func (r *styleReader) fontSize(slug, direct *wpblock.Value) {
	if s, ok := slug.Str(); ok && s != "" {
		if name, ok := r.c.presetName(kindFontSize, s, r.at); ok {
			r.try("fontSize", s, func(st *content.Style) { st.FontSize = content.Length{Preset: name} })
		}
		return
	}
	text, ok := lengthText(direct)
	if !ok || text == "" {
		return
	}
	if name, ref, ok := r.preset(kindFontSize, text); ref {
		if ok {
			r.try("fontSize", text, func(st *content.Style) { st.FontSize = content.Length{Preset: name} })
		}
		return
	}
	r.try("fontSize", text, func(st *content.Style) { st.FontSize = content.Length{Value: text} })
}

func (r *styleReader) fontFamily(slug, direct *wpblock.Value) {
	if s, ok := slug.Str(); ok && s != "" {
		if name, ok := r.c.presetName(kindFontFamily, s, r.at); ok {
			r.try("fontFamily", s, func(st *content.Style) { st.FontFamily = content.FontFamily{Preset: name} })
		}
		return
	}
	text, ok := scalarText(direct)
	if !ok || text == "" {
		return
	}
	name, ref, ok := r.preset(kindFontFamily, text)
	switch {
	case !ref:
		r.drop("fontFamily", text, "whiteroom has font family presets only")
	case ok:
		r.try("fontFamily", text, func(st *content.Style) { st.FontFamily = content.FontFamily{Preset: name} })
	}
}

func (r *styleReader) letterSpacing(v *wpblock.Value) {
	if text, ok := scalarText(v); ok && text != "" {
		r.try("letterSpacing", text, func(st *content.Style) { st.LetterSpacing = content.Length{Value: text} })
	}
}

func (r *styleReader) fontWeight(v *wpblock.Value) {
	text, ok := scalarText(v)
	if !ok || text == "" {
		return
	}
	var n int
	var err error
	switch strings.ToLower(text) {
	case "normal":
		n = 400
	case "bold":
		n = 700
	default:
		n, err = strconv.Atoi(text)
	}
	switch {
	case err != nil:
		r.drop("fontWeight", text, "must be an integer")
	case n == 0:
		// A weight of 0 would be taken for none.
		r.drop("fontWeight", text, "font weight 0 is out of range (100-900)")
	default:
		r.try("fontWeight", text, func(st *content.Style) { st.FontWeight = content.FontWeight{Value: n} })
	}
}

var numberText = regexp.MustCompile(`^(?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+)$`)

func (r *styleReader) lineHeight(v *wpblock.Value) {
	text, ok := scalarText(v)
	if !ok || text == "" {
		return
	}
	f, err := strconv.ParseFloat(text, 64)
	switch {
	case err != nil || v.Kind == wpblock.String && !numberText.MatchString(text):
		r.drop("lineHeight", text, "invalid number")
	case f == 0:
		// A height of 0 would be taken for none.
		r.drop("lineHeight", text, "line height 0 is out of range (0 < x <= 10)")
	default:
		r.try("lineHeight", text, func(st *content.Style) { st.LineHeight = content.LineHeight{Value: f} })
	}
}

// length reads text as a length: a reference to a preset of the kind, if presets are accepted, or else a value written
// directly, which the rules of the style check. It returns false if the preset is not imported, and notes why.
func (r *styleReader) length(text string, k presetKind, presets bool) (content.Length, bool) {
	if presets {
		if name, ref, ok := r.preset(k, text); ref {
			return content.Length{Preset: name}, ok
		}
	}
	return content.Length{Value: text}, true
}

// sides reads padding or margin: a text is the same for the four sides, and an object has the sides, such as top.
func (r *styleReader) sides(item string, v *wpblock.Value, field func(*content.Style, string) *content.Length) {
	if v == nil {
		return
	}
	names := []string{"top", "right", "bottom", "left"}
	if text, ok := scalarText(v); ok {
		if text == "" {
			return
		}
		if l, ok := r.length(text, kindSpacing, true); ok {
			r.try(item, text, func(st *content.Style) {
				for _, side := range names {
					*field(st, side) = l
				}
			})
		}
		return
	}
	for _, side := range names {
		text, ok := scalarText(v.Get(side))
		if !ok || text == "" {
			continue
		}
		if l, ok := r.length(text, kindSpacing, true); ok {
			r.try(item+"."+side, text, func(st *content.Style) { *field(st, side) = l })
		}
	}
}

// horizontal reports whether the block puts its children in a row, whose gap is blockGap.left.
func (r *styleReader) horizontal() bool {
	if r.t == content.BlockColumns || r.t == content.BlockButtons {
		return true
	}
	if r.t != content.BlockGroup {
		return false
	}
	layout := r.b.Attrs.Get("layout")
	typ, _ := layout.Get("type").Str()
	orientation, _ := layout.Get("orientation").Str()
	return typ == "flex" && orientation != "vertical"
}

func (r *styleReader) blockGap(v *wpblock.Value) {
	if v == nil {
		return
	}
	if v.Kind != wpblock.Object {
		r.blockGapValue(v)
		return
	}
	// The gap of a row is the one between its columns, the left one; the others have the top one.
	used, unused := "top", "left"
	if r.horizontal() {
		used, unused = unused, used
	}
	value, other := v.Get(used), v.Get(unused)
	if text, _ := scalarText(value); text == "" {
		value, other = other, nil
	}
	if otherText, _ := scalarText(other); otherText != "" {
		if text, _ := scalarText(value); text != otherText {
			r.drop("blockGap."+unused, otherText, "whiteroom has one block gap, which is blockGap."+used+" here")
		}
	}
	r.blockGapValue(value)
}

func (r *styleReader) blockGapValue(v *wpblock.Value) {
	text, ok := scalarText(v)
	if !ok || text == "" {
		return
	}
	if !r.t.AllowsBlockGap() {
		r.drop("blockGap", text, fmt.Sprintf("blockGap is not allowed for %s blocks", r.t))
		return
	}
	if l, ok := r.length(text, kindSpacing, true); ok {
		r.try("blockGap", text, func(st *content.Style) { st.BlockGap = l })
	}
}

// border reads the border: colorSlug is the attribute borderColor, and border the object style.border.
func (r *styleReader) border(colorSlug, border *wpblock.Value) {
	width, style, color := border.Get("width"), border.Get("style"), border.Get("color")
	sides := []*wpblock.Value{border.Get("top"), border.Get("right"), border.Get("bottom"), border.Get("left")}
	if first(sides...) != nil {
		if same := sameSides(sides); same != nil {
			width, style, color = first(width, same.Get("width")), first(style, same.Get("style")), first(color, same.Get("color"))
		} else {
			r.c.note(r.at, Skipped, "the border is not imported: its sides differ, and whiteroom has one border for all four sides")
		}
	}
	if text, ok := lengthText(width); ok && text != "" {
		r.try("border.width", text, func(st *content.Style) { st.Border.Width = content.Length{Value: text} })
	}
	if text, ok := scalarText(style); ok && text != "" {
		r.try("border.style", text, func(st *content.Style) { st.Border.Style = content.BorderStyle{Value: text} })
	}
	r.color("border.color", colorSlug, color, func(st *content.Style, c content.Color) { st.Border.Color = c })
	r.radius(border.Get("radius"))
}

// sameSides returns the first of the four sides of a border if all four are there, and have the same width, style and
// color, or nil.
func sameSides(sides []*wpblock.Value) *wpblock.Value {
	var want [3]string
	for i, side := range sides {
		if side == nil || side.Kind != wpblock.Object {
			return nil
		}
		var got [3]string
		for j, key := range []string{"width", "style", "color"} {
			got[j], _ = scalarText(side.Get(key))
		}
		if i == 0 {
			want = got
		} else if got != want {
			return nil
		}
	}
	return sides[0]
}

func (r *styleReader) radius(v *wpblock.Value) {
	if v == nil {
		return
	}
	if v.Kind == wpblock.Object {
		var want string
		for i, key := range []string{"topLeft", "topRight", "bottomLeft", "bottomRight"} {
			text, ok := lengthText(v.Get(key))
			if i == 0 {
				want = text
			}
			if !ok || text != want {
				r.c.note(r.at, Skipped, "the border radius is not imported: its corners differ, and whiteroom has one radius for all four corners")
				return
			}
		}
		if want == "" {
			return
		}
		r.try("border.radius", want, func(st *content.Style) { st.Border.Radius = content.Length{Value: want} })
		return
	}
	if text, ok := lengthText(v); ok && text != "" {
		r.try("border.radius", text, func(st *content.Style) { st.Border.Radius = content.Length{Value: text} })
	}
}
