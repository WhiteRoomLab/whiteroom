package render

import (
	"cmp"
	"html/template"
	"slices"
	"strconv"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// blocks returns the HTML of bs, blocks at path, joined with "\n"; the blocks that render nothing are left out.
func (w *walker) blocks(bs content.Blocks, path string) (string, error) {
	var parts []string
	for i := range bs {
		s, err := w.block(&bs[i], path+"/"+strconv.Itoa(i))
		if err != nil {
			return "", err
		}
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n"), nil
}

// block returns the HTML of b, a block at path, or "" for a block that is not rendered, which it warns about. It reads
// the ID, the variant and the style, then the attrs, then the content of b, the order in which blocks are written, so
// that the warnings are in document order; the rules of b go to the CSS before those of its content.
func (w *walker) block(b *content.Block, path string) (string, error) {
	a := &b.Attrs
	var data any
	var err error
	ok := true
	switch b.Type {
	case content.BlockParagraph:
		s := w.begin(b, path)
		s.addClass(w.choice(path+"/attrs", "textAlign", a.TextAlign, textAlignClasses))
		d := Paragraph{Common: w.finish(s), TextAlign: a.TextAlign}
		d.Content = w.text(b, path)
		data = d
	case content.BlockHeading:
		s := w.begin(b, path)
		s.addClass(w.choice(path+"/attrs", "textAlign", a.TextAlign, textAlignClasses))
		d := Heading{Common: w.finish(s), Level: min(max(a.Level, 2), 6), TextAlign: a.TextAlign}
		d.Content = w.text(b, path)
		data = d
	case content.BlockList:
		d := List{Common: w.common(b, path), Ordered: a.Ordered}
		if a.Ordered {
			d.Start = a.Start
		}
		d.Children, err = w.children(b, path)
		data = d
	case content.BlockListItem:
		var d ListItem
		d.Children, err = w.children(b, path)
		data = d
	case content.BlockQuote:
		s := w.begin(b, path)
		var d Quote
		d.Citation = template.HTML(w.richText(a.Citation, path+"/attrs/citation"))
		if d.Citation != "" {
			// The default template puts the class on a figure around the blockquote.
			s.inside = "blockquote"
		}
		d.Common = w.finish(s)
		d.Children, err = w.children(b, path)
		data = d
	case content.BlockImage:
		data, ok = w.image(b, path)
	case content.BlockTable:
		data, err = w.table(b, path)
	case content.BlockTableRow:
		d := TableRow{Section: rowSection(b)}
		d.Children, err = w.children(b, path)
		data = d
	case content.BlockTableCell:
		d := TableCell{Header: a.Header, Colspan: a.Colspan, Rowspan: a.Rowspan, TextAlign: a.TextAlign}
		d.Class = w.choice(path+"/attrs", "textAlign", a.TextAlign, textAlignClasses)
		d.Content = w.text(b, path)
		data = d
	case content.BlockButtons:
		s := w.begin(b, path)
		s.addClass("is-layout-row")
		s.addClass(w.choice(path+"/attrs", "justify", a.Justify, justifyClasses["row"]))
		d := Buttons{Common: w.finish(s), Justify: a.Justify}
		d.Children, err = w.children(b, path)
		data = d
	case content.BlockButton:
		s := w.begin(b, path)
		d := Button{Label: a.Label, Width: a.Width}
		d.Link = w.link(a.Link, path+"/attrs/link")
		s.addClass(w.choice(path+"/attrs", "width", a.Width, buttonWidthClasses))
		d.Common = w.finish(s)
		data = d
	case content.BlockSeparator:
		data = Separator{Common: w.common(b, path)}
	case content.BlockSpacer:
		s := w.begin(b, path)
		if v := a.HeightValue(); v != (content.StyleValue{}) {
			if height, kept := w.cssValue(v, path+"/attrs"); kept {
				s.main = append(s.main, important("height", height))
			}
		}
		data = Spacer{Common: w.finish(s), Height: a.Height}
	case content.BlockCode:
		data = Code{Common: w.common(b, path), Language: a.Language, Code: b.Code}
	case content.BlockEmbed:
		data, ok = w.embed(b, path)
	case content.BlockColumns:
		s := w.begin(b, path)
		s.addClass("is-layout-columns")
		s.addClass(w.choice(path+"/attrs", "verticalAlign", a.VerticalAlign, columnsAlignClasses))
		d := Columns{VerticalAlign: a.VerticalAlign, StackOnMobile: true}
		if a.StackOnMobile != nil {
			d.StackOnMobile = *a.StackOnMobile
		}
		if !d.StackOnMobile {
			s.addClass("is-not-stacked-on-mobile")
		}
		d.Common = w.finish(s)
		d.Children, err = w.children(b, path)
		data = d
	case content.BlockColumn:
		s := w.begin(b, path)
		if width := w.length(path+"/attrs/width", a.Width, content.LengthRule{Percent: true}); width != "" {
			s.main = append(s.main, important("flex-basis", width), important("flex-grow", "0"))
		}
		s.addClass(w.choice(path+"/attrs", "verticalAlign", a.VerticalAlign, columnAlignClasses))
		d := Column{Common: w.finish(s), Width: a.Width, VerticalAlign: a.VerticalAlign}
		d.Children, err = w.children(b, path)
		data = d
	case content.BlockGroup:
		s := w.begin(b, path)
		w.layout(&s, path+"/attrs/layout")
		s.addClass(w.choice(path+"/attrs", "align", a.Align, groupAlignClasses))
		d := Group{
			Common: w.finish(s), TagName: groupTag(a.TagName), Label: a.Label, Layout: a.Layout, Align: a.Align,
		}
		d.Children, err = w.children(b, path)
		data = d
	case content.BlockFile:
		data, ok = w.file(b, path)
	case content.BlockForm:
		data, ok = w.form(b, path)
	case content.BlockHTML, content.BlockLegacy:
		return w.html(b, path), nil
	default:
		w.warnf(path, "unknown block type %q; not rendered", b.Type)
		return "", nil
	}
	if err != nil || !ok {
		return "", err
	}
	return w.execute(b.Type, data)
}

// text returns the HTML of the rich text of b, a block at path.
func (w *walker) text(b *content.Block, path string) template.HTML {
	return template.HTML(w.richText(b.Text, path+"/content"))
}

// children returns the HTML of the child blocks of b, a block at path.
func (w *walker) children(b *content.Block, path string) (template.HTML, error) {
	s, err := w.blocks(b.Children, path+"/content")
	return template.HTML(s), err
}

// table returns the data of b, a table at path. Its children go to the sections that they name, in their order; those
// that are not rows of the head or the foot go to the body.
func (w *walker) table(b *content.Block, path string) (Table, error) {
	s := w.begin(b, path)
	var d Table
	d.Caption = template.HTML(w.richText(b.Attrs.Caption, path+"/attrs/caption"))
	d.Common = w.finish(s)
	var head, body, foot []string
	for i := range b.Children {
		row := &b.Children[i]
		html, err := w.block(row, path+"/content/"+strconv.Itoa(i))
		if err != nil {
			return Table{}, err
		}
		if html == "" {
			continue
		}
		switch rowSection(row) {
		case "head":
			head = append(head, html)
		case "foot":
			foot = append(foot, html)
		default:
			body = append(body, html)
		}
	}
	d.Head = template.HTML(strings.Join(head, "\n"))
	d.Body = template.HTML(strings.Join(body, "\n"))
	d.Foot = template.HTML(strings.Join(foot, "\n"))
	return d, nil
}

// rowSection returns the section of b, a child of a table: head or foot for a row that names it, else body.
func rowSection(b *content.Block) string {
	if s := b.Attrs.Section; b.Type == content.BlockTableRow && (s == "head" || s == "foot") {
		return s
	}
	return "body"
}

// groupTag returns the element of a group whose tagName is s.
func groupTag(s string) string {
	switch s {
	case "section", "article", "aside":
		return s
	}
	return "div"
}

// styling holds what begin reads of a block that is not a part, and then what its attrs add, for finish, which turns it
// into the Common and the rules of the block.
type styling struct {
	block   *content.Block
	path    string
	common  Common   // without Class
	classes []string // the classes of its attrs, such as "has-text-align-center"
	variant string   // the class of its variant, if the theme has it
	at      int      // the number of warnings of the walker before those of the block
	main    []string // the declarations of its rule .b-<ID>: those of its style, then those of its attrs
	solid   bool     // the border has a width or a color but no style, which the rule :where(.b-<ID>) gives it
	link    string   // the color of its links, in the rule .b-<ID> :where(a); "" if none
	img     []string // the declarations of the size of the image of an image block, in the rule .b-<ID> img
	gap     string   // the CSS of its blockGap; "" if none
	inside  string   // the element of the default template between that of the block and its children, if any
}

// begin starts b, a block at path that is not a part: it takes its ID and its anchor, or the automatic anchor of a
// heading, and reads its variant and its style, warning about what it ignores of them.
func (w *walker) begin(b *content.Block, path string) styling {
	s := styling{block: b, path: path, common: Common{ID: b.ID, Anchor: b.Anchor}, at: len(w.warnings)}
	if s.common.Anchor == "" && b.Type == content.BlockHeading {
		s.common.Anchor = w.autoAnchor(b.ID)
	}
	if v := b.Variant; v != "" {
		if w.r.variants[b.Type][v] {
			s.variant = "is-variant-" + v
		} else {
			w.warnf(path+"/variant", "the theme has no %s variant %q; rendered without its class", b.Type, v)
		}
	}
	w.style(&s)
	return s
}

// finish ends the block of s, after its attrs: it adds its rules to the CSS and returns its Common. A block whose ID is
// invalid, or is that of another block whose rules are in the CSS, is rendered without its rules, which it warns about
// before the other warnings of the block.
func (w *walker) finish(s styling) Common {
	b := s.block
	c := s.common
	var class []string
	if len(s.main) > 0 || s.link != "" || len(s.img) > 0 || s.gap != "" {
		name := "b-" + strings.TrimPrefix(b.ID, "b_")
		switch {
		case content.CheckBlockID(b.ID) != nil:
			w.warnAt(s.at, s.path+"/id", "invalid block ID %q; rendered without its CSS", b.ID)
		case !w.addRules(name, s.rules("."+name)...):
			w.warnAt(s.at, s.path+"/id", "duplicate block ID %q; rendered without its CSS", b.ID)
		default:
			class = append(class, name)
		}
	}
	class = append(class, s.classes...)
	if s.variant != "" {
		class = append(class, s.variant)
	}
	if b.ClassName != "" {
		class = append(class, b.ClassName)
	}
	c.Class = strings.Join(class, " ")
	return c
}

// common returns the Common of b, a block at path that is not a part whose attrs make no classes, rules or warnings,
// and adds its rules to the CSS.
func (w *walker) common(b *content.Block, path string) Common {
	return w.finish(w.begin(b, path))
}

// rules returns the rules of the block of s, whose selector is sel, such as ".b-0paragr1": the main rule, the default
// border style, the color of links, the size of an image, and the margins between the children of a flow container, in
// this order.
func (s *styling) rules(sel string) []string {
	var rules []string
	add := func(selector string, decls ...string) {
		if len(decls) > 0 {
			rules = append(rules, rule(selector, decls))
		}
	}
	add(sel, s.main...)
	if s.solid {
		add(":where("+sel+")", "border-style: solid")
	}
	if s.link != "" {
		add(sel+" :where(a)", important("color", s.link))
	}
	add(sel+" img", s.img...)
	if s.gap != "" && !gapContainer(s.block) {
		children := sel + " > *"
		if s.inside != "" {
			children = sel + " > " + s.inside + " > *"
		}
		add(children, important("margin-top", "0"), important("margin-bottom", "0"))
		add(children+" + *", important("margin-top", s.gap))
	}
	return rules
}

// style reads the style of the block of s: it keeps the values that the block can have and the theme allows, as
// declarations in the order of the items, and warns about the others.
func (w *walker) style(s *styling) {
	b := s.block
	path := s.path + "/style"
	vs := b.Style.Values()
	keep := false
	lined, styled := false, false // the border has a width or a color, and a style
	for i, v := range vs {
		if i == 0 || v.Item != vs[i-1].Item {
			keep = w.styleItem(b, path, v.Item)
		}
		if !keep {
			continue
		}
		value, ok := w.cssValue(v, path)
		if !ok {
			continue
		}
		switch v.Item {
		case "linkColor":
			s.link = value
		case "blockGap":
			s.gap = value
			if gapContainer(b) {
				s.main = append(s.main, important("gap", value))
			}
		default:
			s.main = append(s.main, important(styleProperty(v), value))
		}
		if v.Item == "border" {
			lined = lined || v.Key == "width" || v.Key == "color"
			styled = styled || v.Key == "style"
		}
	}
	s.solid = lined && !styled
}

// styleItem reports whether to keep the values of item, a style item of b at path, after warning about it if b cannot
// have it or the theme does not allow it.
func (w *walker) styleItem(b *content.Block, path, item string) bool {
	if item == "blockGap" && !b.Type.AllowsBlockGap() {
		w.warnf(path+"/blockGap", "blockGap is not allowed for %s blocks; ignored", b.Type)
		return false
	}
	if err := w.r.settings.CheckStyleItem(b.Type, item); err != nil {
		w.warnf(path+"/"+item, "%v; ignored", err)
		return false
	}
	return true
}

// styleProperty returns the property that v, a value of a style that is neither linkColor nor blockGap, sets in the
// rule of its block. padding, margin and border set one for each of their keys, such as padding-top.
func styleProperty(v content.StyleValue) string {
	switch v.Item {
	case "textColor":
		return "color"
	case "backgroundColor":
		return "background-color"
	case "gradient":
		return "background-image"
	case "fontSize":
		return "font-size"
	case "letterSpacing":
		return "letter-spacing"
	case "fontFamily":
		return "font-family"
	case "fontWeight":
		return "font-weight"
	case "lineHeight":
		return "line-height"
	}
	return v.Item + "-" + v.Key
}

// gapContainer reports whether b places its children as a flex or grid container, whose blockGap is its gap; the others
// place them in flow, whose blockGap is the margin between them.
func gapContainer(b *content.Block) bool {
	if b.Type == content.BlockGroup {
		t := b.Attrs.Layout.Type
		return t == "row" || t == "stack" || t == "grid"
	}
	return b.Type == content.BlockButtons || b.Type == content.BlockColumns
}

// The classes of the attributes with fixed choices, by value; the values are those that validation accepts.
var (
	textAlignClasses    = choiceClasses("has-text-align-", "left", "center", "right")
	imageAlignClasses   = choiceClasses("align", "left", "center", "right", "wide", "full")
	groupAlignClasses   = choiceClasses("align", "wide", "full")
	buttonWidthClasses  = choiceClasses("has-width-", "25%", "50%", "75%", "100%")
	columnsAlignClasses = choiceClasses("are-vertically-aligned-", "top", "center", "bottom")
	columnAlignClasses  = choiceClasses("is-vertically-aligned-", "top", "center", "bottom", "stretch")
	layoutClasses       = choiceClasses("is-layout-", "flow", "row", "stack", "grid")
	// Those of the justify and the verticalAlign of a layout, by its type; the justify of buttons is that of a row.
	justifyClasses = map[string]map[string]string{
		"row":   choiceClasses("is-content-justification-", "left", "center", "right", "spaceBetween"),
		"stack": choiceClasses("is-content-justification-", "left", "center", "right", "stretch"),
	}
	verticalAlignClasses = map[string]map[string]string{
		"row":   choiceClasses("are-vertically-aligned-", "top", "center", "bottom", "stretch"),
		"stack": choiceClasses("are-vertically-aligned-", "top", "center", "bottom", "spaceBetween"),
	}
)

// choiceClasses returns the classes of values, the choices of an attribute, by value: prefix and the value in kebab
// case without "%", such as "is-content-justification-space-between" for spaceBetween and "has-width-25" for 25%.
func choiceClasses(prefix string, values ...string) map[string]string {
	classes := make(map[string]string, len(values))
	for _, v := range values {
		classes[v] = prefix + kebab(strings.TrimSuffix(v, "%"))
	}
	return classes
}

// addClass adds class, a class of the attrs of the block of s, unless it is "".
func (s *styling) addClass(class string) {
	if class != "" {
		s.classes = append(s.classes, class)
	}
}

// choice returns the class of v, the value of the attribute key at path+"/"+key, from classes, those of its choices;
// "" if v is "" or is not one of them, which it warns about.
func (w *walker) choice(path, key, v string, classes map[string]string) string {
	if v == "" {
		return ""
	}
	class, ok := classes[v]
	if !ok {
		w.warnf(path+"/"+key, "unknown %s %q; ignored", key, v)
	}
	return class
}

// length returns v, the value of the attribute at path, if it is a length by r; "" if v is "" or is not one, which it
// warns about.
func (w *walker) length(path, v string, r content.LengthRule) string {
	if v == "" {
		return ""
	}
	if err := content.CheckLength(v, r); err != nil {
		w.warnf(path, "%v; ignored", err)
		return ""
	}
	return v
}

// layout reads the layout of the group of s, at path, into s: the classes of its type and its keys, and the columns of
// a grid. It ignores what validation does not accept, which it warns about: a layout without a valid type, which leaves
// the children of the group in flow as no layout does, and the keys and the values that its type cannot have.
func (w *walker) layout(s *styling, path string) {
	l := s.block.Attrs.Layout
	if l == (content.Layout{}) {
		return
	}
	class, ok := layoutClasses[l.Type]
	switch {
	case l.Type == "":
		w.warnf(path+"/type", "missing layout type; ignored")
		return
	case !ok:
		w.warnf(path+"/type", "unknown layout type %q; ignored", l.Type)
		return
	}
	s.addClass(class)
	// allowed reports whether the type can have key, and warns if not.
	allowed := func(key string, types ...string) bool {
		if slices.Contains(types, l.Type) {
			return true
		}
		w.warnf(path+"/"+key, "not allowed for %s layout; ignored", l.Type)
		return false
	}
	if l.Justify != "" && allowed("justify", "row", "stack") {
		s.addClass(w.choice(path, "justify", l.Justify, justifyClasses[l.Type]))
	}
	if l.VerticalAlign != "" && allowed("verticalAlign", "row", "stack") {
		s.addClass(w.choice(path, "verticalAlign", l.VerticalAlign, verticalAlignClasses[l.Type]))
	}
	if l.NoWrap && allowed("nowrap", "row") {
		s.addClass("is-nowrap")
	}
	columns, width := 0, ""
	if l.Columns != 0 && allowed("columns", "grid") {
		if l.Columns < 1 || l.Columns > 16 {
			w.warnf(path+"/columns", "columns %d is out of range (1-16); ignored", l.Columns)
		} else {
			columns = l.Columns
		}
	}
	if l.MinColumnWidth != "" && allowed("minColumnWidth", "grid") {
		width = w.length(path+"/minColumnWidth", l.MinColumnWidth, content.LengthRule{})
	}
	if t := gridColumns(columns, width, s.gap); t != "" {
		s.main = append(s.main, important("grid-template-columns", t))
	}
}

// gridColumns returns the grid-template-columns of a grid of n columns, or of columns at least width wide, or both,
// with gap between them ("" for the default gap of grids, 0.5em); "" if n is 0 and width is "". With both, the columns
// are at least width wide, but no more than n fit in a row.
func gridColumns(n int, width, gap string) string {
	switch {
	case width == "" && n == 0:
		return ""
	case width == "":
		return "repeat(" + strconv.Itoa(n) + ", minmax(0, 1fr))"
	case n == 0:
		return "repeat(auto-fill, minmax(min(" + width + ", 100%), 1fr))"
	}
	least := "(100% - " + strconv.Itoa(n-1) + " * " + cmp.Or(gap, "0.5em") + ") / " + strconv.Itoa(n)
	return "repeat(auto-fill, minmax(max(min(" + width + ", 100%), " + least + "), 1fr))"
}

// autoAnchor returns the automatic anchor of a heading whose ID is id, and takes it: id if it is free, else the first
// free one of id-2, id-3 and so on. A heading without a valid ID gets none.
func (w *walker) autoAnchor(id string) string {
	if content.CheckBlockID(id) != nil {
		return ""
	}
	a := id
	for n := 2; w.anchors[a]; n++ {
		a = id + "-" + strconv.Itoa(n)
	}
	w.anchors[a] = true
	return a
}

// addAnchors adds the anchors of bs and of their children, at any depth, to anchors.
func addAnchors(anchors map[string]bool, bs content.Blocks) {
	for i := range bs {
		if a := bs[i].Anchor; a != "" {
			anchors[a] = true
		}
		addAnchors(anchors, bs[i].Children)
	}
}
