package wpimport

import (
	"cmp"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/html"
	"github.com/WhiteRoomLab/whiteroom/internal/wpblock"
)

func init() {
	converters["core/buttons"] = convertButtons
	converters["core/button"] = convertButton
	converters["core/columns"] = convertColumns
	converters["core/column"] = outsideOf("core/columns")
	converters["core/media-text"] = convertMediaText
	converters["core/cover"] = convertCover
}

// admit counts a block of WordPress that a converter converts without flow, such as a column, and a part that it makes of
// an element of HTML, such as a list item, as flow counts the others. It returns false if the conversion has stopped, or
// stops with this one.
func (c *bodyConv) admit(at noteAt) bool {
	if c.stopped {
		return false
	}
	if c.blocks++; c.blocks > maxWPBlocks {
		c.stop(at)
		return false
	}
	return true
}

// oneOf reads v as one of the choices. lost is set if v is set to anything else.
func oneOf(v *wpblock.Value, choices ...string) (value string, lost bool) {
	if !present(v) {
		return "", false
	}
	if s := str(v); slices.Contains(choices, s) {
		return s, false
	}
	return "", true
}

// innerRoots reads the root element, a div, of each inner block of b, which must all be called name. It reads as many as
// the conversion admits. kind is what the blocks are called in the reason for a legacy block: buttons, columns.
func (c *bodyConv) innerRoots(b *wpblock.Block, name, kind string, at noteAt) ([]*html.Node, error) {
	for i := range b.Inner {
		if b.Inner[i].Name != name {
			return nil, legacyf("the %[1]s block has a block other than %[1]s", kind)
		}
	}
	var roots []*html.Node
	for i := range b.Inner {
		if !c.admit(at) {
			break
		}
		root, err := c.root(&b.Inner[i], "div")
		if err != nil {
			return nil, err
		}
		roots = append(roots, root)
	}
	return roots, nil
}

// buttonsJustify reads where the buttons are put: from the attribute layout, or else the older contentJustification.
// lost are the settings of the layout that whiteroom has not.
func buttonsJustify(b *wpblock.Block) (justify string, lost []string) {
	layout := b.Attrs.Get("layout")
	v, name := layout.Get("justifyContent"), "layout.justifyContent"
	if v == nil {
		v, name = b.Attrs.Get("contentJustification"), "contentJustification"
	}
	if present(v) {
		switch s := str(v); s {
		case "left", "center", "right":
			justify = s
		case "space-between":
			justify = "spaceBetween"
		default:
			lost = append(lost, name)
		}
	}
	if str(layout.Get("orientation")) == "vertical" {
		lost = append(lost, "layout.orientation")
	}
	if f := layout.Get("flexWrap"); present(f) && str(f) != "wrap" {
		lost = append(lost, "layout.flexWrap")
	}
	return justify, lost
}

func convertButtons(c *bodyConv, _ string, b *wpblock.Block, _ bodyPlace, at noteAt) ([]content.Block, error) {
	root, err := c.root(b, "div")
	if err != nil {
		return nil, err
	}
	roots, err := c.innerRoots(b, "core/button", "buttons", at)
	if err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		if !c.stopped {
			c.stray(root, nil, b.Name, at)
			c.note(at, Skipped, "the empty buttons block is removed")
		}
		return nil, nil
	}
	blk := c.newBlock(content.BlockButtons)
	own := noteAt{id: blk.ID}
	var lost []string
	blk.Attrs.Justify, lost = buttonsJustify(b)
	c.lost(own, b.Name, lost...)
	c.applyProps(&blk, b, root, own)
	list := noteAt{id: blk.ID, sub: "/content"}
	for i, r := range roots {
		btn, err := c.buttonFrom(&b.Inner[i], r, list)
		if err != nil {
			return nil, err
		}
		if btn != nil {
			c.noteDelimiter(&b.Inner[i], noteAt{id: btn.ID})
			blk.Children = append(blk.Children, *btn)
		}
	}
	if len(blk.Children) == 0 {
		c.giveBackAnchor(&blk)
		if !c.stopped {
			c.stray(root, nil, b.Name, at)
			c.note(at, Skipped, "the empty buttons block is removed")
		}
		return nil, nil
	}
	c.stray(root, nil, b.Name, own)
	return []content.Block{blk}, nil
}

// convertButton converts a button that is not in a buttons block, as WordPress before 5.4 had it, into the only button of
// a buttons block.
func convertButton(c *bodyConv, _ string, b *wpblock.Block, _ bodyPlace, at noteAt) ([]content.Block, error) {
	root, err := c.root(b, "div")
	if err != nil {
		return nil, err
	}
	buttons := c.newBlock(content.BlockButtons)
	var extra []string
	if present(b.Attrs.Get("align")) {
		extra = append(extra, "align")
	}
	btn, err := c.buttonFrom(b, root, at, extra...)
	if err != nil || btn == nil {
		return nil, err
	}
	buttons.Children = content.Blocks{*btn}
	return []content.Block{buttons}, nil
}

// buttonFrom makes the button block of ib, whose root element is root. A button without text is not made: it is noted at
// at. extra are more settings that are lost. The error is a legacy error: the label has something that rich text cannot
// have, or the link goes over the limit of the HTML that is read (see link).
func (c *bodyConv) buttonFrom(ib *wpblock.Block, root *html.Node, at noteAt, extra ...string) (*content.Block, error) {
	blk := c.newBlock(content.BlockButton)
	own := noteAt{id: blk.ID}
	el := findElement(root, "a", "button")
	if el != nil {
		label, err := c.plainText(el, own)
		if err != nil {
			return nil, err
		}
		blk.Attrs.Label = cleanLine(label)
	}
	if blk.Attrs.Label == "" {
		c.note(at, Skipped, "the empty button is removed")
		return nil, nil
	}
	if el.Name == "a" {
		link, err := c.link(attr(el, "href"), attr(el, "target"), attr(el, "rel"), own)
		if err != nil {
			return nil, err
		}
		blk.Attrs.Link = link
	}
	var lost []string
	blk.Attrs.Width, lost = buttonWidth(ib)
	if present(ib.Attrs.Get("textAlign")) || present(ib.Attrs.Get("style").Get("typography").Get("textAlign")) {
		lost = append(lost, "textAlign")
	}
	if present(ib.Attrs.Get("borderRadius")) {
		lost = append(lost, "borderRadius")
	}
	c.lost(own, ib.Name, append(lost, extra...)...)
	c.applyProps(&blk, ib, root, own)
	return &blk, nil
}

// buttonWidth reads the width of a button: the attribute width (25, 50, 75 or 100), or else style.dimensions.width (25%
// and so on). lost is the setting that has a width that whiteroom cannot have, in a list of at most one.
func buttonWidth(ib *wpblock.Block) (width string, lost []string) {
	for _, s := range []struct {
		v    *wpblock.Value
		name string
	}{{ib.Attrs.Get("width"), "width"}, {ib.Attrs.Get("style").Get("dimensions").Get("width"), "style.dimensions.width"}} {
		if !present(s.v) {
			continue
		}
		text, _ := scalarText(s.v)
		if n, ok := s.v.Int(); ok {
			text = strconv.FormatInt(n, 10) + "%"
		}
		switch text {
		case "25%", "50%", "75%", "100%":
			return text, nil
		}
		return "", []string{s.name}
	}
	return "", nil
}

func convertColumns(c *bodyConv, doc string, b *wpblock.Block, p bodyPlace, at noteAt) ([]content.Block, error) {
	root, err := c.root(b, "div")
	if err != nil {
		return nil, err
	}
	roots, err := c.innerRoots(b, "core/column", "columns", at)
	if err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		if !c.stopped {
			c.stray(root, nil, b.Name, at)
			c.note(at, Skipped, "the empty columns block is removed")
		}
		return nil, nil
	}
	if p.inColumns {
		return c.columnsAsGroup(doc, b, root, roots, p), nil
	}
	blk := c.newBlock(content.BlockColumns)
	own := noteAt{id: blk.ID}
	var lost []string
	var alignLost bool
	if blk.Attrs.VerticalAlign, alignLost = oneOf(b.Attrs.Get("verticalAlignment"), "top", "center", "bottom"); alignLost {
		lost = append(lost, "verticalAlignment")
	}
	if stacked, ok := b.Attrs.Get("isStackedOnMobile").IsTrue(); ok && !stacked {
		blk.Attrs.StackOnMobile = &stacked
	}
	if present(b.Attrs.Get("align")) {
		lost = append(lost, "align")
	}
	c.lost(own, b.Name, lost...)
	c.applyProps(&blk, b, root, own)
	c.stray(root, nil, b.Name, own)
	for i, r := range roots {
		blk.Children = append(blk.Children, c.column(doc, &b.Inner[i], r, p))
	}
	return []content.Block{blk}, nil
}

// columnWidth reads the width of a column: a length, or a number of percent. lost is set if there is a width that
// whiteroom cannot have.
func columnWidth(cb *wpblock.Block) (width string, lost bool) {
	v := cb.Attrs.Get("width")
	if !present(v) {
		return "", false
	}
	text, ok := scalarText(v)
	if v.Kind == wpblock.Number {
		text += "%"
	}
	if !ok || content.CheckLength(text, content.LengthRule{Percent: true}) != nil {
		return "", true
	}
	return text, false
}

// column makes the column block of cb, an inner block of the columns block at p, whose root element is root.
func (c *bodyConv) column(doc string, cb *wpblock.Block, root *html.Node, p bodyPlace) content.Block {
	col := c.newBlock(content.BlockColumn)
	own := noteAt{id: col.ID}
	c.noteDelimiter(cb, own)
	var lost []string
	var widthLost, alignLost bool
	if col.Attrs.Width, widthLost = columnWidth(cb); widthLost {
		lost = append(lost, "width")
	}
	if col.Attrs.VerticalAlign, alignLost = oneOf(cb.Attrs.Get("verticalAlignment"), "top", "center", "bottom", "stretch"); alignLost {
		lost = append(lost, "verticalAlignment")
	}
	c.lost(own, cb.Name, lost...)
	c.applyProps(&col, cb, root, own)
	c.stray(root, nil, cb.Name, own)
	col.Children = c.flow(doc, cb.Inner, bodyPlace{depth: p.depth + 2, inColumns: true, top: p.top}, noteAt{id: col.ID, sub: "/content"})
	return col
}

// columnsAsGroup makes a group of the columns block b, which is in a columns block, where Validate has no columns block:
// a grid of groups if the columns have no widths and no alignment, and a row of groups otherwise. roots are the root
// elements of the columns.
func (c *bodyConv) columnsAsGroup(doc string, b *wpblock.Block, root *html.Node, roots []*html.Node, p bodyPlace) []content.Block {
	g := c.newBlock(content.BlockGroup)
	own := noteAt{id: g.ID}
	var lost []string
	align, alignLost := oneOf(b.Attrs.Get("verticalAlignment"), "top", "center", "bottom")
	if alignLost {
		lost = append(lost, "verticalAlignment")
	}
	widths, aligned := false, present(b.Attrs.Get("verticalAlignment"))
	for i := range roots {
		widths = widths || present(b.Inner[i].Attrs.Get("width"))
		aligned = aligned || present(b.Inner[i].Attrs.Get("verticalAlignment"))
	}
	stacked := true
	if v, ok := b.Attrs.Get("isStackedOnMobile").IsTrue(); ok {
		stacked = v
	}
	if n := len(roots); n <= 16 && !widths && !aligned {
		g.Attrs.Layout = content.Layout{Type: "grid", Columns: n}
		if !stacked {
			lost = append(lost, "isStackedOnMobile")
		}
	} else {
		g.Attrs.Layout = content.Layout{Type: "row", VerticalAlign: align, NoWrap: !stacked}
	}
	if present(b.Attrs.Get("align")) {
		lost = append(lost, "align")
	}
	c.lost(own, b.Name, lost...)
	c.setProps(&g, c.props(b, root, content.BlockColumns, own))
	msg := "the columns inside columns are a group here"
	if widths {
		msg += "; the widths of the columns are lost"
	}
	c.note(own, Changed, "%s", msg)
	c.stray(root, nil, b.Name, own)
	inner := bodyPlace{depth: p.depth + 2, inColumns: true, top: p.top}
	for i, r := range roots {
		cb := &b.Inner[i]
		item := c.newBlock(content.BlockGroup)
		itemAt := noteAt{id: item.ID}
		c.noteDelimiter(cb, itemAt)
		if v := cb.Attrs.Get("verticalAlignment"); present(v) && str(v) != align {
			c.lost(itemAt, cb.Name, "verticalAlignment")
		}
		c.applyProps(&item, cb, r, itemAt)
		c.stray(r, nil, cb.Name, itemAt)
		item.Children = c.flow(doc, cb.Inner, inner, noteAt{id: item.ID, sub: "/content"})
		c.origins[item.ID] = c.newOrigin(cb, doc, p.top)
		g.Children = append(g.Children, item)
	}
	return []content.Block{g}
}

// setProps puts the settings that props read into blk.
func (c *bodyConv) setProps(blk *content.Block, pr props) {
	blk.Anchor, blk.ClassName, blk.Variant, blk.Style = pr.anchor, pr.className, pr.variant, pr.style
}

// percent writes w, a number of percent, with at most two decimals.
func percent(w float64) string {
	return strconv.FormatFloat(math.Round(w*100)/100, 'f', -1, 64) + "%"
}

// mediaTextWidths returns the widths of the media and of the text of a media and text block, which attribute mediaWidth
// says, or halves. valid is false if the attribute is set to anything but a number between 0 and 100.
func mediaTextWidths(v *wpblock.Value) (media, text string, valid bool) {
	if !present(v) {
		return "50%", "50%", true
	}
	if w, ok := v.Float(); ok {
		if w = math.Round(w*100) / 100; w > 0 && w < 100 {
			return percent(w), percent(100 - w), true
		}
	}
	return "50%", "50%", false
}

func convertMediaText(c *bodyConv, doc string, b *wpblock.Block, p bodyPlace, _ noteAt) ([]content.Block, error) {
	root, err := c.root(b, "div")
	if err != nil {
		return nil, err
	}
	figure := childNamed(root, "figure")
	if figure == nil {
		return nil, legacyf("%s%s", notAsWritten, tagText("figure"))
	}
	a := b.Attrs
	// Inside columns, Validate has no columns block: the media and the text are two groups in a row.
	asGroup := p.inColumns
	typ, itemType := content.BlockColumns, content.BlockColumn
	if asGroup {
		typ, itemType = content.BlockGroup, content.BlockGroup
	}
	blk := c.newBlock(typ)
	own := noteAt{id: blk.ID}
	mediaWidth, textWidth, valid := mediaTextWidths(a.Get("mediaWidth"))
	var lost []string
	if asGroup && present(a.Get("mediaWidth")) || !asGroup && !valid {
		lost = append(lost, "mediaWidth")
	}
	align, alignLost := oneOf(a.Get("verticalAlignment"), "top", "center", "bottom")
	if alignLost {
		lost = append(lost, "verticalAlignment")
	}
	stacked := true
	if v, ok := a.Get("isStackedOnMobile").IsTrue(); ok {
		stacked = v
	}
	if asGroup {
		blk.Attrs.Layout = content.Layout{Type: "row", VerticalAlign: align, NoWrap: !stacked}
	} else {
		blk.Attrs.VerticalAlign = align
		if !stacked {
			blk.Attrs.StackOnMobile = &stacked
		}
	}
	if on, _ := a.Get("imageFill").IsTrue(); on {
		lost = append(lost, "imageFill")
	}
	if present(a.Get("focalPoint")) {
		lost = append(lost, "focalPoint")
	}
	if on, _ := a.Get("useFeaturedImage").IsTrue(); on {
		lost = append(lost, "useFeaturedImage")
	}
	if present(a.Get("align")) {
		lost = append(lost, "align")
	}
	c.lost(own, b.Name, lost...)
	c.setProps(&blk, c.props(b, root, content.BlockColumns, own))
	if asGroup {
		c.note(own, Changed, "the media and text block inside columns is a group here")
	} else {
		c.note(own, Changed, "the media and text block is columns here")
	}
	c.stray(root, func(n *html.Node) bool {
		return n == figure || isElement(n, "div") && hasClass(n, "wp-block-media-text__content")
	}, b.Name, own)

	inner := bodyPlace{depth: p.depth + 2, inColumns: true, top: p.top}
	pair := c.newOrigin(b, doc, p.top) // of both groups, which the guard takes out together
	item := func(width string, children func(noteAt) []content.Block) content.Block {
		it := c.newBlock(itemType)
		if asGroup {
			c.origins[it.ID] = pair
		} else {
			it.Attrs.Width = width
		}
		it.Children = children(noteAt{id: it.ID, sub: "/content"})
		return it
	}
	media := func() content.Block {
		return item(mediaWidth, func(at noteAt) []content.Block { return c.mediaTextMedia(figure, a, at) })
	}
	text := func() content.Block {
		return item(textWidth, func(at noteAt) []content.Block { return c.flow(doc, b.Inner, inner, at) })
	}
	if str(a.Get("mediaPosition")) == "right" {
		blk.Children = append(blk.Children, text())
		blk.Children = append(blk.Children, media())
	} else {
		blk.Children = append(blk.Children, media())
		blk.Children = append(blk.Children, text())
	}
	return []content.Block{blk}, nil
}

// mediaTextMedia makes the blocks of the media of a media and text block: the image, or a legacy block of the HTML of the
// figure if the media is not an image that was imported.
func (c *bodyConv) mediaTextMedia(figure *html.Node, a *wpblock.Value, at noteAt) []content.Block {
	if str(a.Get("mediaType")) == "video" {
		return c.legacyHTML(html.Render(figure), "whiteroom has no video blocks yet (stage 2)", at)
	}
	img := findElement(figure, "img")
	if img == nil {
		return nil
	}
	blk, err := c.imageBlock(imageInput{
		img: img, link: linkAround(img, figure), id: postID(a.Get("mediaId")), size: str(a.Get("mediaSizeSlug")),
		alt: str(a.Get("mediaAlt")),
	})
	if err != nil {
		return c.legacyHTML(html.Render(figure), err.Error(), at)
	}
	return []content.Block{blk}
}

// isCoverPart reports whether n is one of the elements that WordPress puts in a cover besides its blocks: the layers of
// the background, and the container of the blocks.
func isCoverPart(n *html.Node) bool {
	return isElement(n, "span", "img", "video", "div") &&
		slices.ContainsFunc(classList(n), func(class string) bool { return strings.HasPrefix(class, "wp-block-cover__") })
}

func convertCover(c *bodyConv, doc string, b *wpblock.Block, p bodyPlace, at noteAt) ([]content.Block, error) {
	root, err := c.root(b)
	if err != nil {
		return nil, err
	}
	g := c.newBlock(content.BlockGroup)
	own := noteAt{id: g.ID}
	a := b.Attrs
	g.Attrs.TagName = c.groupTag(b, own)
	var lost []string
	g.Attrs.Layout, lost = groupLayout(b)
	switch align := str(a.Get("align")); align {
	case "", "none":
	case "wide", "full":
		g.Attrs.Align = align
	default:
		lost = append(lost, "align")
	}
	video := str(a.Get("backgroundType")) == "video"
	if video {
		lost = append(lost, "backgroundType")
	}
	// The color over the background shows only as much as dimRatio says; whiteroom has the color or nothing.
	dimRatio := a.Get("dimRatio")
	dim, dimNumber := dimRatio.Float()
	if present(dimRatio) && (!dimNumber || dim != 0 && dim != 100) {
		lost = append(lost, "dimRatio")
	}
	for _, key := range []string{"hasParallax", "isRepeated"} {
		if on, _ := a.Get(key).IsTrue(); on {
			lost = append(lost, key)
		}
	}
	for _, key := range []string{"focalPoint", "minHeight", "contentPosition"} {
		if present(a.Get(key)) {
			lost = append(lost, key)
		}
	}
	if on, _ := a.Get("useFeaturedImage").IsTrue(); on {
		lost = append(lost, "useFeaturedImage")
	}
	c.lost(own, b.Name, lost...)
	c.applyProps(&g, b, root, own)
	if !dimNumber || dim != 0 {
		r := &styleReader{c: c, b: b, t: content.BlockGroup, at: own, st: g.Style}
		r.color("overlayColor", a.Get("overlayColor"), a.Get("customOverlayColor"), func(st *content.Style, color content.Color) {
			st.BackgroundColor = color
		})
		g.Style = r.st
	}
	if !video {
		background, err := c.coverBackground(root, a, own)
		if err != nil {
			return nil, err
		}
		g.Children = background
	}
	g.Children = append(g.Children, c.flow(doc, b.Inner, bodyPlace{depth: p.depth + 1, inColumns: p.inColumns, top: p.top}, noteAt{id: g.ID, sub: "/content"})...)
	if len(g.Children) == 0 {
		c.giveBackAnchor(&g)
		if !c.stopped {
			c.stray(root, isCoverPart, b.Name, at)
			c.note(at, Skipped, "the empty cover is removed")
		}
		return nil, nil
	}
	c.stray(root, isCoverPart, b.Name, own)
	c.note(own, Changed, "the cover is a group here; whiteroom has no cover blocks yet (stage 2)")
	return []content.Block{g}, nil
}

// coverBackground makes the image block for the background image of a cover, which is the first block of the group: none
// if the cover has none. An image that is not an imported attachment is a legacy block of the HTML of the img element, as
// any image is; if the cover has no such element, it is noted at own that there is none. The block has no origin: the
// guard leaves it out if it does not pass, and does not make a legacy block of the text of the cover, which holds its other
// blocks too. The error is a legacy error (see attachmentByURL).
func (c *bodyConv) coverBackground(root *html.Node, a *wpblock.Value, own noteAt) ([]content.Block, error) {
	post := postID(a.Get("id"))
	src := str(a.Get("url"))
	if post < 1 && src != "" {
		var err error
		if post, _, err = c.attachmentByURL(src); err != nil {
			return nil, err
		}
	}
	if post < 1 && !present(a.Get("id")) && src == "" {
		return nil, nil
	}
	mediaID, ok, why := c.attachment(post, content.MediaImage)
	if !ok {
		if img := findElement(root, "img"); img != nil {
			return c.legacyHTML(html.Render(img), cmp.Or(why, reasonNotImage), own), nil
		}
		c.note(own, Skipped, "the background image of the cover is not an imported attachment")
		return nil, nil
	}
	img := c.newBlock(content.BlockImage)
	img.Attrs.Media = mediaID
	if alt := cleanLine(str(a.Get("alt"))); alt != "" {
		img.Attrs.Alt = alt
	} else {
		img.Attrs.Decorative = true
	}
	return []content.Block{img}, nil
}
