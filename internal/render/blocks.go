package render

import (
	"html/template"
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
// the variant, then the attrs, then the content of b, the order in which blocks are written, so that the warnings are
// in document order.
func (w *walker) block(b *content.Block, path string) (string, error) {
	a := &b.Attrs
	var data any
	var err error
	switch b.Type {
	case content.BlockParagraph:
		d := Paragraph{Common: w.common(b, path), TextAlign: a.TextAlign}
		d.Content = w.text(b, path)
		data = d
	case content.BlockHeading:
		d := Heading{Common: w.common(b, path), Level: min(max(a.Level, 2), 6), TextAlign: a.TextAlign}
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
		d := Quote{Common: w.common(b, path)}
		d.Citation = template.HTML(w.richText(a.Citation, path+"/attrs/citation"))
		d.Children, err = w.children(b, path)
		data = d
	case content.BlockTable:
		data, err = w.table(b, path)
	case content.BlockTableRow:
		d := TableRow{Section: rowSection(b)}
		d.Children, err = w.children(b, path)
		data = d
	case content.BlockTableCell:
		d := TableCell{Header: a.Header, Colspan: a.Colspan, Rowspan: a.Rowspan, TextAlign: a.TextAlign}
		d.Content = w.text(b, path)
		data = d
	case content.BlockButtons:
		d := Buttons{Common: w.common(b, path), Justify: a.Justify}
		d.Children, err = w.children(b, path)
		data = d
	case content.BlockButton:
		d := Button{Common: w.common(b, path), Label: a.Label, Width: a.Width}
		d.Link = w.link(a.Link, path+"/attrs/link")
		data = d
	case content.BlockSeparator:
		data = Separator{Common: w.common(b, path)}
	case content.BlockSpacer:
		data = Spacer{Common: w.common(b, path), Height: a.Height}
	case content.BlockCode:
		data = Code{Common: w.common(b, path), Language: a.Language, Code: b.Code}
	case content.BlockColumns:
		d := Columns{Common: w.common(b, path), VerticalAlign: a.VerticalAlign, StackOnMobile: true}
		if a.StackOnMobile != nil {
			d.StackOnMobile = *a.StackOnMobile
		}
		d.Children, err = w.children(b, path)
		data = d
	case content.BlockColumn:
		d := Column{Common: w.common(b, path), Width: a.Width, VerticalAlign: a.VerticalAlign}
		d.Children, err = w.children(b, path)
		data = d
	case content.BlockGroup:
		d := Group{
			Common: w.common(b, path), TagName: groupTag(a.TagName), Label: a.Label, Layout: a.Layout, Align: a.Align,
		}
		d.Children, err = w.children(b, path)
		data = d
	case content.BlockImage, content.BlockFile, content.BlockEmbed, content.BlockForm, content.BlockHTML,
		content.BlockLegacy:
		w.warnf(path, "%s blocks are not rendered yet", b.Type)
		return "", nil
	default:
		w.warnf(path, "unknown block type %q; not rendered", b.Type)
		return "", nil
	}
	if err != nil {
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
	d := Table{Common: w.common(b, path)}
	d.Caption = template.HTML(w.richText(b.Attrs.Caption, path+"/attrs/caption"))
	var head, body, foot []string
	for i := range b.Children {
		row := &b.Children[i]
		s, err := w.block(row, path+"/content/"+strconv.Itoa(i))
		if err != nil {
			return Table{}, err
		}
		if s == "" {
			continue
		}
		switch rowSection(row) {
		case "head":
			head = append(head, s)
		case "foot":
			foot = append(foot, s)
		default:
			body = append(body, s)
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

// common returns the Common of b, a block at path that is not a part, and warns about a variant that the theme does not
// have. A heading without an anchor gets its automatic anchor.
func (w *walker) common(b *content.Block, path string) Common {
	c := Common{ID: b.ID, Anchor: b.Anchor}
	if c.Anchor == "" && b.Type == content.BlockHeading {
		c.Anchor = w.autoAnchor(b.ID)
	}
	var class []string
	if b.Style != (content.Style{}) && content.CheckBlockID(b.ID) == nil {
		class = append(class, "b-"+strings.TrimPrefix(b.ID, "b_"))
	}
	if v := b.Variant; v != "" {
		if w.r.variants[b.Type][v] {
			class = append(class, "is-variant-"+v)
		} else {
			w.warnf(path+"/variant", "the theme has no %s variant %q; rendered without its class", b.Type, v)
		}
	}
	if b.ClassName != "" {
		class = append(class, b.ClassName)
	}
	c.Class = strings.Join(class, " ")
	return c
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
