package wpimport

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/html"
	"github.com/WhiteRoomLab/whiteroom/internal/wpblock"
)

func init() {
	converters["core/paragraph"] = convertParagraph
	converters["core/verse"] = convertVerse
	converters["core/heading"] = convertHeading
	converters["core/list"] = convertList
	converters["core/quote"] = convertQuote
	converters["core/pullquote"] = convertPullquote
	converters["core/code"] = convertCode
	converters["core/preformatted"] = convertPreformatted
	converters["core/table"] = convertTable
	converters["core/separator"] = convertSeparator
	converters["core/spacer"] = convertSpacer
	converters["core/html"] = convertHTML
	converters["core/group"] = convertGroup
	converters["core/details"] = convertDetails
	converters["core/accordion"] = convertAccordion
	// The parts of a block are converted by the block that holds them; these are the ones that have no such block.
	converters["core/list-item"] = outsideOf("core/list")
	converters["core/accordion-item"] = outsideOf("core/accordion")
	converters["core/accordion-heading"] = outsideOf("core/accordion-item")
	converters["core/accordion-panel"] = outsideOf("core/accordion-item")
}

// outsideOf is the converter of a block that only makes sense inside the block parent, which converts it there.
func outsideOf(parent string) converter {
	return func(_ *bodyConv, _ string, b *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
		return nil, legacyf("the block %q is outside %q", b.Name, parent)
	}
}

const notAsWritten = "the HTML of the block is not as WordPress writes it: expected "

func isElement(n *html.Node, names ...string) bool {
	return n != nil && n.Type == html.ElementNode && n.Namespace == html.HTML && slices.Contains(names, n.Name)
}

func childNamed(n *html.Node, name string) *html.Node {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		if isElement(ch, name) {
			return ch
		}
	}
	return nil
}

// onlyChild returns the child of n if it is the only one that is not a comment or white space, and nil otherwise.
func onlyChild(n *html.Node) *html.Node {
	var only *html.Node
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		if ch.Type == html.CommentNode || ch.Type == html.TextNode && blank(ch.Data) {
			continue
		}
		if only != nil {
			return nil
		}
		only = ch
	}
	return only
}

func hasAttr(n *html.Node, name string) bool {
	return slices.ContainsFunc(n.Attrs, func(a html.Attr) bool { return a.Namespace == "" && a.Name == name })
}

func findClass(n *html.Node, class string) *html.Node {
	for d := n.FirstChild; d != nil; d = after(n, d, true) {
		if d.Type == html.ElementNode && d.Namespace == html.HTML && hasClass(d, class) {
			return d
		}
	}
	return nil
}

// present reports whether an attribute is set to something: it is not missing, null, or an empty string, array or object.
func present(v *wpblock.Value) bool {
	if v == nil {
		return false
	}
	switch v.Kind {
	case wpblock.Null:
		return false
	case wpblock.String:
		return v.String != ""
	case wpblock.Array:
		return len(v.Array) > 0
	case wpblock.Object:
		return len(v.Object) > 0
	case wpblock.Bool, wpblock.Number:
	}
	return true
}

// emptyText reports whether text is nothing once Body normalizes it, which removes the control characters that are left.
func emptyText(text content.RichText) bool {
	norm := slices.Clone(text)
	norm.Normalize()
	return len(norm) == 0
}

// applyProps puts into blk the anchor, the class name, the variant and the style that b has. root is the element of the
// HTML of b that has the id.
func (c *bodyConv) applyProps(blk *content.Block, b *wpblock.Block, root *html.Node, own noteAt) {
	c.setProps(blk, c.props(b, root, blk.Type, own))
}

// giveBackAnchor lets another block have the anchor of blk, which is left out after its settings were read.
func (c *bodyConv) giveBackAnchor(blk *content.Block) {
	delete(c.anchors, blk.Anchor)
}

// textAlignOf returns the alignment of the text of a paragraph, a verse or a heading. What whiteroom has not, such as
// justify and the alignments wide and full of the block, it notes as lost.
func (c *bodyConv) textAlignOf(b *wpblock.Block, own noteAt) string {
	aligned, other := textAlign(b.Attrs)
	var lost []string
	if other != "" {
		lost = append(lost, "textAlign")
	}
	if a, _ := b.Attrs.Get("align").Str(); a == "wide" || a == "full" {
		lost = append(lost, "align")
	}
	c.lost(own, b.Name, lost...)
	return aligned
}

// lostAlignment notes as lost the alignment of the text and of the block, which a quote has not.
func (c *bodyConv) lostAlignment(b *wpblock.Block, own noteAt) {
	var lost []string
	if aligned, other := textAlign(b.Attrs); aligned != "" || other != "" {
		lost = append(lost, "textAlign")
	}
	switch a, _ := b.Attrs.Get("align").Str(); a {
	case "wide", "full", "left", "right":
		lost = append(lost, "align")
	}
	c.lost(own, b.Name, lost...)
}

func convertParagraph(c *bodyConv, _ string, b *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
	root, err := c.root(b, "p")
	if err != nil {
		return nil, err
	}
	blk := c.newBlock(content.BlockParagraph)
	own := noteAt{id: blk.ID}
	if blk.Text, err = c.richText(root, false, own); err != nil {
		return nil, err
	}
	var lost []string
	if on, _ := b.Attrs.Get("dropCap").IsTrue(); on {
		lost = append(lost, "dropCap")
	}
	if dir, _ := b.Attrs.Get("direction").Str(); dir != "" {
		lost = append(lost, "direction")
	}
	c.lost(own, b.Name, lost...)
	blk.Attrs.TextAlign = c.textAlignOf(b, own)
	c.applyProps(&blk, b, root, own)
	return []content.Block{blk}, nil
}

func convertVerse(c *bodyConv, _ string, b *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
	root, err := c.root(b, "pre")
	if err != nil {
		return nil, err
	}
	blk := c.newBlock(content.BlockParagraph)
	own := noteAt{id: blk.ID}
	if blk.Text, err = c.richText(root, true, own); err != nil {
		return nil, err
	}
	blk.Attrs.TextAlign = c.textAlignOf(b, own)
	c.applyProps(&blk, b, root, own)
	return []content.Block{blk}, nil
}

func convertHeading(c *bodyConv, _ string, b *wpblock.Block, _ bodyPlace, at noteAt) ([]content.Block, error) {
	root, err := c.root(b, "h1", "h2", "h3", "h4", "h5", "h6")
	if err != nil {
		return nil, err
	}
	blk := c.newBlock(content.BlockHeading)
	own := noteAt{id: blk.ID}
	if blk.Text, err = c.richText(root, false, own); err != nil {
		return nil, err
	}
	if emptyText(blk.Text) {
		c.note(at, Skipped, "the empty heading is removed")
		return nil, nil
	}
	blk.Attrs.Level = int(root.Name[1] - '0')
	if blk.Attrs.Level == 1 {
		blk.Attrs.Level = 2
		c.note(own, Changed, "the heading level 1 is 2 here; h1 is for the title")
	}
	blk.Attrs.TextAlign = c.textAlignOf(b, own)
	c.applyProps(&blk, b, root, own)
	return []content.Block{blk}, nil
}

func convertList(c *bodyConv, doc string, b *wpblock.Block, p bodyPlace, at noteAt) ([]content.Block, error) {
	list, err := c.list(doc, b, p.depth, at)
	if err != nil || list == nil {
		return nil, err
	}
	return []content.Block{*list}, nil
}

// list converts the list block b, which is at depth, and returns nil if it has no items.
func (c *bodyConv) list(doc string, b *wpblock.Block, depth int, at noteAt) (*content.Block, error) {
	root, err := c.root(b, "ul", "ol")
	if err != nil {
		return nil, err
	}
	return c.listOf(doc, b, root, depth, at)
}

// listOf converts the list whose element is root. Its items are the inner blocks of b, or, for a list of WordPress before
// 6.1, which has none, the li elements of root.
func (c *bodyConv) listOf(doc string, b *wpblock.Block, root *html.Node, depth int, at noteAt) (*content.Block, error) {
	// The list is at depth, its items at depth+1, and their paragraphs and lists at depth+2.
	if depth+2 > content.MaxDepth {
		return nil, legacyf("the blocks are nested more than %d deep", content.MaxDepth)
	}
	blk := c.newBlock(content.BlockList)
	own := noteAt{id: blk.ID}
	c.listSettings(&blk, b, root, own)
	c.applyProps(&blk, b, root, own)
	if len(b.Inner) > 0 {
		c.stray(root, nil, b.Name, own)
		for i := range b.Inner {
			if b.Inner[i].Name != "core/list-item" {
				return nil, legacyf("the list has a block other than list items")
			}
			if !c.admit(at) {
				break
			}
			item, err := c.listItem(doc, &b.Inner[i], depth+1)
			if err != nil {
				return nil, err
			}
			blk.Children = append(blk.Children, item)
		}
	} else if err := c.oldItems(&blk, root, depth+1, at); err != nil {
		return nil, err
	}
	if len(blk.Children) == 0 {
		c.giveBackAnchor(&blk)
		if !c.stopped {
			c.note(at, Skipped, "the empty list is removed")
		}
		return nil, nil
	}
	return &blk, nil
}

func (c *bodyConv) listSettings(blk *content.Block, b *wpblock.Block, root *html.Node, own noteAt) {
	var lost []string
	blk.Attrs.Ordered = root.Name == "ol"
	if blk.Attrs.Ordered {
		var bad bool
		if blk.Attrs.Start, bad = listStart(b, root); bad {
			lost = append(lost, "start")
		}
	}
	if rev, _ := b.Attrs.Get("reversed").IsTrue(); rev || hasAttr(root, "reversed") {
		lost = append(lost, "reversed")
	}
	if typ, _ := b.Attrs.Get("type").Str(); typ != "" || attr(root, "type") != "" {
		lost = append(lost, "type")
	}
	c.lost(own, b.Name, lost...)
}

// listStart reads the number that an ordered list starts with: the attribute start of the block, else that of the
// element. bad is set for one that whiteroom cannot have, which is less than 1.
func listStart(b *wpblock.Block, root *html.Node) (start int, bad bool) {
	if v := b.Attrs.Get("start"); present(v) {
		n, ok := v.Int()
		if !ok || n < 1 || n != int64(int(n)) {
			return 0, true
		}
		return int(n), false
	}
	if s := strings.TrimSpace(attr(root, "start")); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return 0, true
		}
		return n, false
	}
	return 0, false
}

// listItem converts the block ib, a list item at depth: its paragraph, and the list in it. The settings of the item go to
// the paragraph.
func (c *bodyConv) listItem(doc string, ib *wpblock.Block, depth int) (content.Block, error) {
	root, err := c.root(ib, "li")
	if err != nil {
		return content.Block{}, err
	}
	if len(ib.Inner) > 1 || len(ib.Inner) == 1 && ib.Inner[0].Name != "core/list" {
		return content.Block{}, legacyf("a list item has blocks other than one list")
	}
	item := c.newBlock(content.BlockListItem)
	para := c.newBlock(content.BlockParagraph)
	own := noteAt{id: para.ID}
	c.noteDelimiter(ib, own)
	if para.Text, err = c.richText(root, false, own); err != nil {
		return content.Block{}, err
	}
	c.applyProps(&para, ib, root, own)
	item.Children = content.Blocks{para}
	if len(ib.Inner) == 1 {
		nested, err := c.list(doc, &ib.Inner[0], depth+1, own)
		if err != nil {
			return content.Block{}, err
		}
		if nested != nil {
			c.noteDelimiter(&ib.Inner[0], noteAt{id: nested.ID})
			item.Children = append(item.Children, *nested)
		}
	}
	return item, nil
}

// oldItems converts the li elements of root, the list of an old list, into the items of blk, which are at depth, as many
// as the conversion admits.
func (c *bodyConv) oldItems(blk *content.Block, root *html.Node, depth int, at noteAt) error {
	for n := root.FirstChild; n != nil; n = n.NextSibling {
		switch {
		case n.Type == html.CommentNode, n.Type == html.TextNode && blank(n.Data):
		case isElement(n, "li"):
			if !c.admit(at) {
				return nil
			}
			item, err := c.oldItem(n, depth)
			if err != nil {
				return err
			}
			blk.Children = append(blk.Children, item)
		default:
			return legacyf("%s%s", notAsWritten, tagText("li"))
		}
	}
	return nil
}

// oldItem converts the element li, an item at depth: the text before the list in it, and the list.
func (c *bodyConv) oldItem(li *html.Node, depth int) (content.Block, error) {
	var lists []*html.Node
	for ch := li.FirstChild; ch != nil; {
		next := ch.NextSibling
		if isElement(ch, "ul", "ol") {
			li.RemoveChild(ch)
			lists = append(lists, ch)
		}
		ch = next
	}
	if len(lists) > 1 {
		return content.Block{}, legacyf("a list item has blocks other than one list")
	}
	item := c.newBlock(content.BlockListItem)
	para := c.newBlock(content.BlockParagraph)
	own := noteAt{id: para.ID}
	var err error
	if para.Text, err = c.richText(li, false, own); err != nil {
		return content.Block{}, err
	}
	item.Children = content.Blocks{para}
	if len(lists) == 1 {
		// The list has no block of WordPress to take attributes from.
		plain := &wpblock.Block{Name: "core/list", Attrs: &wpblock.Value{Kind: wpblock.Object}}
		nested, err := c.listOf("", plain, lists[0], depth+1, own)
		if err != nil {
			return content.Block{}, err
		}
		if nested != nil {
			item.Children = append(item.Children, *nested)
		}
	}
	return item, nil
}

func convertQuote(c *bodyConv, doc string, b *wpblock.Block, p bodyPlace, at noteAt) ([]content.Block, error) {
	root, err := c.root(b, "blockquote")
	if err != nil {
		return nil, err
	}
	blk := c.newBlock(content.BlockQuote)
	own := noteAt{id: blk.ID}
	c.lostAlignment(b, own)
	c.applyProps(&blk, b, root, own)
	cite := childNamed(root, "cite")
	if cite != nil {
		if blk.Attrs.Citation, err = c.richText(cite, false, own); err != nil {
			return nil, err
		}
	}
	if len(b.Inner) > 0 {
		c.stray(root, func(n *html.Node) bool { return n == cite }, b.Name, own)
		inner := bodyPlace{depth: p.depth + 1, inColumns: p.inColumns, top: p.top}
		blk.Children = c.flow(doc, b.Inner, inner, noteAt{id: blk.ID, sub: "/content"})
	} else {
		if cite != nil {
			root.RemoveChild(cite)
		}
		if blk.Children, err = c.quoteParagraphs(root, false, at); err != nil {
			return nil, err
		}
	}
	if len(blk.Children) == 0 {
		if c.stopped {
			c.giveBackAnchor(&blk)
			return nil, nil
		}
		blk.Children = content.Blocks{c.newBlock(content.BlockParagraph)}
	}
	return []content.Block{blk}, nil
}

func convertPullquote(c *bodyConv, _ string, b *wpblock.Block, _ bodyPlace, at noteAt) ([]content.Block, error) {
	root, err := c.root(b, "figure", "blockquote")
	if err != nil {
		return nil, err
	}
	q := root
	if root.Name == "figure" {
		if q = childNamed(root, "blockquote"); q == nil {
			return nil, legacyf("%s%s", notAsWritten, tagText("blockquote"))
		}
	}
	blk := c.newBlock(content.BlockQuote)
	own := noteAt{id: blk.ID}
	if cite := childNamed(q, "cite"); cite != nil {
		if blk.Attrs.Citation, err = c.richText(cite, false, own); err != nil {
			return nil, err
		}
		q.RemoveChild(cite)
	}
	if blk.Children, err = c.quoteParagraphs(q, true, at); err != nil {
		return nil, err
	}
	if len(blk.Children) == 0 {
		if c.stopped {
			return nil, nil
		}
		blk.Children = content.Blocks{c.newBlock(content.BlockParagraph)}
	}
	c.lostAlignment(b, own)
	c.applyProps(&blk, withBlockStyle(b, "pullquote"), root, own)
	return []content.Block{blk}, nil
}

// withBlockStyle returns a copy of b whose attribute className has the block style first. A pullquote is a quote with
// the variant pullquote, so props takes that as the one block style, and notes any other as a second.
func withBlockStyle(b *wpblock.Block, style string) *wpblock.Block {
	cp := *b
	attrs := &wpblock.Value{Kind: wpblock.Object}
	if b.Attrs != nil && b.Attrs.Kind == wpblock.Object {
		attrs.Object = slices.Clone(b.Attrs.Object)
	}
	class := "is-style-" + style
	if i := slices.IndexFunc(attrs.Object, func(m wpblock.Member) bool { return m.Name == "className" }); i >= 0 {
		old, _ := attrs.Object[i].Value.Str()
		attrs.Object[i].Value = wpblock.Value{Kind: wpblock.String, String: class + " " + old}
	} else {
		attrs.Object = append(attrs.Object, wpblock.Member{Name: "className", Value: wpblock.Value{Kind: wpblock.String, String: class}})
	}
	cp.Attrs = attrs
	return &cp
}

// quoteParagraphs makes the paragraphs of the blockquote q of an old quote or a pullquote, from which the citation is
// taken: one for each p, as many as the conversion admits, and one for what is not in a p, at the place of the first of
// it. If mixed is not set, an element that is not a p makes the quote a legacy block; if it is, it is part of that
// paragraph.
func (c *bodyConv) quoteParagraphs(q *html.Node, mixed bool, at noteAt) ([]content.Block, error) {
	var out []content.Block
	rest := &html.Node{Type: html.ElementNode, Namespace: html.HTML, Name: "div"}
	restAt := -1
	apart := false // a p has come since the last of the rest
paragraphs:
	for n := q.FirstChild; n != nil; {
		next := n.NextSibling
		switch {
		case n.Type == html.CommentNode:
		case isElement(n, "p"):
			if !c.admit(at) {
				break paragraphs
			}
			para := c.newBlock(content.BlockParagraph)
			text, err := c.richText(n, false, noteAt{id: para.ID})
			if err != nil {
				return nil, err
			}
			para.Text = text
			out = append(out, para)
			apart = rest.FirstChild != nil
		case n.Type == html.TextNode && blank(n.Data) && rest.FirstChild == nil:
		default:
			if n.Type == html.ElementNode && !mixed {
				return nil, legacyf("%s%s", notAsWritten, expectedText([]string{"p", "cite"}))
			}
			if rest.FirstChild == nil {
				restAt = len(out)
				out = append(out, c.newBlock(content.BlockParagraph))
			} else if apart {
				rest.AppendChild(&html.Node{Type: html.TextNode, Data: " "})
			}
			apart = false
			q.RemoveChild(n)
			rest.AppendChild(n)
		}
		n = next
	}
	if restAt >= 0 {
		text, err := c.richText(rest, false, noteAt{id: out[restAt].ID})
		if err != nil {
			return nil, err
		}
		if emptyText(text) {
			out = slices.Delete(out, restAt, restAt+1)
		} else {
			out[restAt].Text = text
		}
	}
	return out, nil
}

func convertCode(c *bodyConv, _ string, b *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
	root, err := c.root(b, "pre")
	if err != nil {
		return nil, err
	}
	blk := c.newBlock(content.BlockCode)
	own := noteAt{id: blk.ID}
	text := root
	if code := onlyChild(root); isElement(code, "code") {
		text = code
	}
	blk.Code = c.codeText(text, own)
	blk.Attrs.Language = c.codeLanguage(text, own)
	c.applyProps(&blk, b, root, own)
	return []content.Block{blk}, nil
}

// codeLanguage reads the language from the class language-<name> of the element that holds the code.
func (c *bodyConv) codeLanguage(el *html.Node, own noteAt) string {
	for _, class := range classList(el) {
		name, ok := strings.CutPrefix(class, "language-")
		if !ok || name == "" {
			continue
		}
		if err := content.CheckName(name); err != nil {
			c.note(own, Skipped, "the language %q is not imported: %s", name, err)
			return ""
		}
		return name
	}
	return ""
}

func convertPreformatted(c *bodyConv, _ string, b *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
	root, err := c.root(b, "pre")
	if err != nil {
		return nil, err
	}
	blk := c.newBlock(content.BlockCode)
	own := noteAt{id: blk.ID}
	blk.Code = c.codeText(root, own)
	c.applyProps(&blk, b, root, own)
	return []content.Block{blk}, nil
}

func convertSeparator(c *bodyConv, _ string, b *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
	root, err := c.root(b, "hr", "div")
	if err != nil {
		return nil, err
	}
	blk := c.newBlock(content.BlockSeparator)
	c.applyProps(&blk, b, root, noteAt{id: blk.ID})
	return []content.Block{blk}, nil
}

// defaultSpacerHeight is the height of a spacer that has none in its attributes, as WordPress has it.
const defaultSpacerHeight = "100px"

func convertSpacer(c *bodyConv, _ string, b *wpblock.Block, _ bodyPlace, at noteAt) ([]content.Block, error) {
	root, err := c.root(b, "div")
	if err != nil {
		return nil, err
	}
	text, _ := lengthText(b.Attrs.Get("height"))
	if text == "" {
		text = defaultSpacerHeight
	}
	height, why := c.spacerHeight(text)
	if why != "" {
		c.note(at, Skipped, "the spacer is removed: its height %q is not one that whiteroom accepts: %s", text, why)
		return nil, nil
	}
	blk := c.newBlock(content.BlockSpacer)
	own := noteAt{id: blk.ID}
	blk.Attrs.Height = height
	if present(b.Attrs.Get("width")) {
		c.lost(own, b.Name, "width")
	}
	c.applyProps(&blk, b, root, own)
	return []content.Block{blk}, nil
}

// spacerHeight reads the height of a spacer as a preset of the spacing or as a length that is written directly. why is
// the reason if whiteroom cannot have it.
func (c *bodyConv) spacerHeight(text string) (height content.Length, why string) {
	if kind, slug, ok := presetRef(text); ok && kind == kindSpacing.wp {
		name, r := lookupName(c.presetMap(kindSpacing), slug)
		switch r {
		case nameMapped:
			return content.Length{Preset: name}, ""
		case nameToNothing:
			return content.Length{}, "blocks.presets.spacing maps it to nothing"
		case nameNotAccepted:
		}
		return content.Length{}, "it is not a name that whiteroom accepts"
	}
	if err := content.CheckLength(text, content.LengthRule{}); err != nil {
		return content.Length{}, err.Error()
	}
	return content.Length{Value: text}, ""
}

func convertTable(c *bodyConv, _ string, b *wpblock.Block, _ bodyPlace, at noteAt) ([]content.Block, error) {
	root, err := c.root(b, "figure", "table")
	if err != nil {
		return nil, err
	}
	table, figcaption := root, (*html.Node)(nil)
	if root.Name == "figure" {
		if table = childNamed(root, "table"); table == nil {
			return nil, legacyf("%s%s", notAsWritten, tagText("table"))
		}
		figcaption = childNamed(root, "figcaption")
	}
	blk := c.newBlock(content.BlockTable)
	own := noteAt{id: blk.ID}
	if figcaption != nil {
		if blk.Attrs.Caption, err = c.richText(figcaption, false, own); err != nil {
			return nil, err
		}
	}
	var lost struct{ layout, colspan, rowspan, align bool }
	// WordPress writes the layout in the class of the table, not in the attributes, where it is the default.
	lost.layout = hasClass(table, "has-fixed-layout")
	emptyRows, cells := 0, 0
	for _, src := range tableRows(table) {
		var nodes []*html.Node
		for _, ch := range childElements(src.tr) {
			if isElement(ch, "th", "td") {
				nodes = append(nodes, ch)
			}
		}
		if len(nodes) == 0 {
			emptyRows++
			continue
		}
		if !c.admit(at) {
			break
		}
		row := c.newBlock(content.BlockTableRow)
		switch src.section {
		case "thead":
			row.Attrs.Section = "head"
		case "tfoot":
			row.Attrs.Section = "foot"
		}
		stopped := false
		for _, cell := range nodes {
			if cells++; cells > content.MaxTableCells {
				return nil, legacyf("the table has more than %d cells", content.MaxTableCells)
			}
			if !c.admit(at) {
				stopped = true
				break
			}
			tc := c.newBlock(content.BlockTableCell)
			cellAt := noteAt{id: blk.ID, sub: fmt.Sprintf("/content/%d/content/%d/content", len(blk.Children), len(row.Children))}
			if tc.Text, err = c.richText(cell, false, cellAt); err != nil {
				return nil, err
			}
			var badColspan, badRowspan bool
			tc.Attrs.Header = cell.Name == "th"
			tc.Attrs.Colspan, badColspan = tableSpan(cell, "colspan")
			tc.Attrs.Rowspan, badRowspan = tableSpan(cell, "rowspan")
			aligned, other := cellAlign(cell)
			tc.Attrs.TextAlign = aligned
			lost.colspan, lost.rowspan = lost.colspan || badColspan, lost.rowspan || badRowspan
			lost.align = lost.align || other != ""
			row.Children = append(row.Children, tc)
		}
		if len(row.Children) > 0 {
			blk.Children = append(blk.Children, row)
		}
		if stopped {
			break
		}
	}
	switch emptyRows {
	case 0:
	case 1:
		c.note(own, Skipped, "the empty row of the table is removed")
	default:
		c.note(own, Skipped, "%d empty rows of the table are removed", emptyRows)
	}
	if len(blk.Children) == 0 {
		if !c.stopped {
			c.note(at, Skipped, "the empty table is removed")
		}
		return nil, nil
	}
	var items []string
	for _, l := range []struct {
		set  bool
		name string
	}{{lost.layout, "hasFixedLayout"}, {lost.colspan, "colspan"}, {lost.rowspan, "rowspan"}, {lost.align, "textAlign"}} {
		if l.set {
			items = append(items, l.name)
		}
	}
	c.lost(own, b.Name, items...)
	c.applyProps(&blk, b, root, own)
	return []content.Block{blk}, nil
}

type tableRow struct {
	tr      *html.Node
	section string // thead, tbody or tfoot
}

// tableRows returns the rows of table: the head first, then the body, and the foot last, wherever they are written.
func tableRows(table *html.Node) []tableRow {
	var rows []tableRow
	for _, section := range []string{"thead", "tbody", "tfoot"} {
		for _, group := range childElements(table) {
			if !isElement(group, section) {
				continue
			}
			for _, tr := range childElements(group) {
				if isElement(tr, "tr") {
					rows = append(rows, tableRow{tr, section})
				}
			}
		}
	}
	return rows
}

// tableSpan reads the attribute colspan or rowspan of a cell. lost is set for one that whiteroom cannot have.
func tableSpan(cell *html.Node, name string) (n int, lost bool) {
	s := strings.TrimSpace(attr(cell, name))
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 1000 {
		return 0, true
	}
	return n, false
}

// cellAlign reads the alignment of the text of a cell, from the attribute data-align or else the class
// has-text-align-<value>. aligned is left, center or right; other is any other value.
func cellAlign(cell *html.Node) (aligned, other string) {
	v := strings.ToLower(strings.TrimSpace(attr(cell, "data-align")))
	if v == "" {
		for _, class := range classList(cell) {
			if rest, ok := strings.CutPrefix(class, "has-text-align-"); ok {
				v = strings.ToLower(rest)
				break
			}
		}
	}
	if v == "left" || v == "center" || v == "right" {
		return v, ""
	}
	return "", v
}

// dropControl removes the control characters, which Body removes from the HTML of a block.
func dropControl(r rune) rune {
	if unicode.IsControl(r) {
		return -1
	}
	return r
}

// embedProviders are the providers whose iframe the custom HTML can be: x is a link, not an iframe.
var embedProviders = []string{"youtube", "vimeo", "googleMaps"}

func convertHTML(c *bodyConv, _ string, b *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
	text := b.HTML()
	if blank(strings.Map(dropControl, text)) {
		return nil, nil
	}
	provider, src, err := c.iframeEmbed(text)
	if err != nil {
		return nil, err
	}
	if provider != "" {
		blk := c.newBlock(content.BlockEmbed)
		own := noteAt{id: blk.ID}
		blk.Attrs.Provider, blk.Attrs.URL = provider, src
		c.note(own, Changed, "the iframe of the custom HTML is an embed block here")
		c.applyProps(&blk, b, nil, own)
		return []content.Block{blk}, nil
	}
	if !c.opts.Site.AllowHTML {
		return nil, legacyf("custom HTML, which the site does not allow (site.allowHtml is false)")
	}
	blk := c.newBlock(content.BlockHTML)
	own := noteAt{id: blk.ID}
	// The line breaks at the ends are those that the editor writes after the opener and before the closer.
	blk.Attrs.HTML = strings.Trim(text, " \t\n\f\r")
	c.note(own, Review, "the custom HTML is an html block; check what it shows")
	c.applyProps(&blk, b, nil, own)
	return []content.Block{blk}, nil
}

// iframeEmbed reads text, the HTML of a custom HTML block. If it is one iframe, alone or in one div or p, that a provider
// of embeds accepts, it returns the provider and the address.
func (c *bodyConv) iframeEmbed(text string) (provider, src string, err error) {
	if !hasTag(text, "iframe") {
		return "", "", nil
	}
	nodes, err := c.parseHTML(text)
	if err != nil {
		return "", "", err
	}
	provider, src = embedOf(nodes)
	return provider, src, nil
}

// embedOf reads nodes, the HTML of a custom HTML block. If it is one iframe, alone or in one div or p, that a provider of
// embeds accepts, it returns the provider and the address.
func embedOf(nodes []*html.Node) (provider, src string) {
	holder := &html.Node{Type: html.DocumentFragmentNode}
	for _, n := range nodes {
		holder.AppendChild(n)
	}
	frame := onlyChild(holder)
	if isElement(frame, "div", "p") {
		frame = onlyChild(frame)
	}
	if !isElement(frame, "iframe") {
		return "", ""
	}
	src = content.NormalizeHref(attr(frame, "src"))
	if strings.HasPrefix(src, "//") {
		// An address without a scheme takes that of the page, which is https on the site.
		src = "https:" + src
	}
	if content.CheckHref(src) != nil {
		return "", ""
	}
	for _, name := range embedProviders {
		if _, err := content.ParseEmbedURL(name, src); err == nil {
			return name, src
		}
	}
	return "", ""
}

func convertGroup(c *bodyConv, doc string, b *wpblock.Block, p bodyPlace, at noteAt) ([]content.Block, error) {
	root, err := c.root(b)
	if err != nil {
		return nil, err
	}
	g := c.newBlock(content.BlockGroup)
	own := noteAt{id: g.ID}
	g.Attrs.TagName = c.groupTag(b, own)
	var lost []string
	g.Attrs.Layout, lost = groupLayout(b)
	switch a, _ := b.Attrs.Get("align").Str(); a {
	case "":
	case "wide", "full":
		g.Attrs.Align = a
	default:
		lost = append(lost, "align")
	}
	c.lost(own, b.Name, lost...)
	c.applyProps(&g, b, root, own)
	g.Children = c.flow(doc, b.Inner, bodyPlace{depth: p.depth + 1, inColumns: p.inColumns, top: p.top}, noteAt{id: g.ID, sub: "/content"})
	if len(g.Children) == 0 {
		// What the HTML holds outside the blocks is lost with it.
		c.giveBackAnchor(&g)
		if !c.stopped {
			c.stray(root, isInnerContainer, b.Name, at)
			c.note(at, Skipped, "the empty group is removed")
		}
		return nil, nil
	}
	c.stray(root, isInnerContainer, b.Name, own)
	return []content.Block{g}, nil
}

// isInnerContainer reports whether n is the div that WordPress before 5.9 put in a group around its blocks.
func isInnerContainer(n *html.Node) bool {
	return isElement(n, "div") && hasClass(n, "wp-block-group__inner-container")
}

// groupTag returns the tagName of a group. The elements that a group has in whiteroom are div, section, article and
// aside; main, header, footer and the others become a div, which is the empty tagName.
func (c *bodyConv) groupTag(b *wpblock.Block, own noteAt) string {
	tag, _ := b.Attrs.Get("tagName").Str()
	switch tag {
	case "", "div":
	case "section", "article", "aside":
		return tag
	default:
		c.note(own, Changed, "the group's %s is a %s here", tagText(tag), tagText("div"))
	}
	return ""
}

// groupLayout reads the attribute layout of a group. lost are its keys that whiteroom has no counterpart of.
func groupLayout(b *wpblock.Block) (l content.Layout, lost []string) {
	v := b.Attrs.Get("layout")
	if !present(v) {
		return content.Layout{}, nil
	}
	if v.Kind != wpblock.Object {
		return content.Layout{}, []string{"layout"}
	}
	read := map[string]bool{"type": true}
	// choice reads the key as one of the choices: WordPress writes space-between where whiteroom writes spaceBetween.
	choice := func(key string, choices ...string) string {
		read[key] = true
		f := v.Get(key)
		if !present(f) {
			return ""
		}
		s, _ := f.Str()
		switch s {
		case "space-between":
			s = "spaceBetween"
		case "spaceBetween":
			s = ""
		}
		if s != "" && slices.Contains(choices, s) {
			return s
		}
		lost = append(lost, "layout."+key)
		return ""
	}
	typ, _ := v.Get("type").Str()
	switch typ {
	case "", "constrained", "default", "inherit":
		// The blocks are in the flow, which whiteroom does not write. WordPress 5.9 and 6.0 write inherit for the width of
		// the theme, which WordPress 6.1 writes as the type constrained.
		if _, ok := v.Get("inherit").IsTrue(); ok {
			read["inherit"] = true
		}
	case "flex":
		read["orientation"] = true
		stack := false
		if o, _ := v.Get("orientation").Str(); o == "vertical" {
			stack = true
		}
		if stack {
			l.Type = "stack"
			l.Justify = choice("justifyContent", "left", "center", "right", "stretch")
			l.VerticalAlign = choice("verticalAlignment", "top", "center", "bottom", "spaceBetween")
		} else {
			l.Type = "row"
			l.Justify = choice("justifyContent", "left", "center", "right", "spaceBetween")
			l.VerticalAlign = choice("verticalAlignment", "top", "center", "bottom", "stretch")
		}
		read["flexWrap"] = true
		if f := v.Get("flexWrap"); present(f) {
			w, _ := f.Str()
			switch {
			case w == "nowrap" && !stack:
				l.NoWrap = true
			case w != "wrap":
				lost = append(lost, "layout.flexWrap")
			}
		}
	case "grid":
		l.Type = "grid"
		read["columnCount"], read["minimumColumnWidth"] = true, true
		if f := v.Get("columnCount"); present(f) {
			if n, ok := f.Int(); ok && n >= 1 && n <= 16 {
				l.Columns = int(n)
			} else {
				lost = append(lost, "layout.columnCount")
			}
		}
		if f := v.Get("minimumColumnWidth"); present(f) {
			if s, ok := scalarText(f); ok && content.CheckLength(s, content.LengthRule{}) == nil {
				l.MinColumnWidth = s
			} else {
				lost = append(lost, "layout.minimumColumnWidth")
			}
		}
	default:
		return content.Layout{}, []string{"layout.type"}
	}
	for i := range v.Object {
		if m := &v.Object[i]; !read[m.Name] && present(&m.Value) {
			lost = append(lost, "layout."+m.Name)
		}
	}
	return l, lost
}

func convertDetails(c *bodyConv, doc string, b *wpblock.Block, p bodyPlace, _ noteAt) ([]content.Block, error) {
	root, err := c.root(b, "details")
	if err != nil {
		return nil, err
	}
	g := c.newBlock(content.BlockGroup)
	own := noteAt{id: g.ID}
	c.applyProps(&g, b, root, own)
	summary := childNamed(root, "summary")
	if summary != nil {
		if g.Attrs.Label, err = c.plainText(summary, own); err != nil {
			return nil, err
		}
	}
	c.stray(root, func(n *html.Node) bool { return n == summary }, b.Name, own)
	g.Children = c.flow(doc, b.Inner, bodyPlace{depth: p.depth + 1, inColumns: p.inColumns, top: p.top}, noteAt{id: g.ID, sub: "/content"})
	if len(g.Children) == 0 {
		if c.stopped {
			c.giveBackAnchor(&g)
			return nil, nil
		}
		g.Children = content.Blocks{c.newBlock(content.BlockParagraph)}
	}
	c.note(own, Changed, "the details block is a group here, always open; whiteroom has no details blocks yet (stage 2)")
	return []content.Block{g}, nil
}

func convertAccordion(c *bodyConv, doc string, b *wpblock.Block, p bodyPlace, at noteAt) ([]content.Block, error) {
	root, err := c.root(b)
	if err != nil {
		return nil, err
	}
	g := c.newBlock(content.BlockGroup)
	own := noteAt{id: g.ID}
	c.applyProps(&g, b, root, own)
	inner := bodyPlace{depth: p.depth + 1, inColumns: p.inColumns, top: p.top}
	contentAt := noteAt{id: g.ID, sub: "/content"}
	for i := range b.Inner {
		ib := &b.Inner[i]
		if ib.Name != "core/accordion-item" {
			g.Children = append(g.Children, c.flow(doc, b.Inner[i:i+1], inner, contentAt)...)
			continue
		}
		if !c.admit(at) {
			break
		}
		item, err := c.accordionItem(doc, ib, inner)
		if err != nil {
			return nil, err
		}
		if item == nil {
			break
		}
		c.origins[item.ID] = c.newOrigin(ib, doc, p.top)
		g.Children = append(g.Children, *item)
	}
	if len(g.Children) == 0 {
		c.giveBackAnchor(&g)
		if !c.stopped {
			c.stray(root, nil, b.Name, at)
			c.note(at, Skipped, "the empty accordion is removed")
		}
		return nil, nil
	}
	c.stray(root, nil, b.Name, own)
	c.note(own, Changed, "the accordion is groups here, always open; whiteroom has no accordion blocks")
	return []content.Block{g}, nil
}

// accordionItem converts the item ib of an accordion, which is at p: a group whose label is the text of the heading of the
// item, and whose blocks are those in its panel. It returns none if the conversion stopped before the first block of the panel.
func (c *bodyConv) accordionItem(doc string, ib *wpblock.Block, p bodyPlace) (*content.Block, error) {
	root, err := c.root(ib)
	if err != nil {
		return nil, err
	}
	var heading, panel *wpblock.Block
	for i := range ib.Inner {
		part := &ib.Inner[i]
		switch {
		case part.Name == "core/accordion-heading" && heading == nil:
			heading = part
		case part.Name == "core/accordion-panel" && panel == nil:
			panel = part
		default:
			return nil, legacyf("the accordion item has a block other than a heading and a panel")
		}
	}
	g := c.newBlock(content.BlockGroup)
	own := noteAt{id: g.ID}
	c.noteDelimiter(ib, own)
	c.applyProps(&g, ib, root, own)
	c.stray(root, nil, ib.Name, own)
	if heading != nil {
		c.noteDelimiter(heading, own)
		hroot, err := c.root(heading)
		if err != nil {
			return nil, err
		}
		title := findClass(hroot, "wp-block-accordion-heading__toggle-title")
		if title == nil {
			title = hroot
		}
		if g.Attrs.Label, err = c.plainText(title, own); err != nil {
			return nil, err
		}
	}
	if panel != nil {
		c.noteDelimiter(panel, own)
		proot, err := c.root(panel)
		if err != nil {
			return nil, err
		}
		c.stray(proot, nil, panel.Name, own)
		inner := bodyPlace{depth: p.depth + 1, inColumns: p.inColumns, top: p.top}
		g.Children = c.flow(doc, panel.Inner, inner, noteAt{id: g.ID, sub: "/content"})
	}
	if len(g.Children) == 0 {
		if c.stopped {
			c.giveBackAnchor(&g)
			return nil, nil
		}
		g.Children = content.Blocks{c.newBlock(content.BlockParagraph)}
	}
	return &g, nil
}
