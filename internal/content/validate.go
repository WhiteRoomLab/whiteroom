package content

import (
	"cmp"
	"slices"
	"strconv"
)

type RefKind string

const (
	RefMedia RefKind = "media"
	RefEntry RefKind = "entry"
	RefForm  RefKind = "form"
)

type ValidateOptions struct {
	AllowHTML bool                               // html blocks are enabled in the site settings
	Import    bool                               // the blocks come from an import, which may create legacy blocks and images without alt
	Previous  Blocks                             // blocks of the previous revision, matched by ID
	RefExists func(kind RefKind, id string) bool // nil skips checking that referenced entities exist
}

// docState is shared by the checks of every list of blocks in a document, such as its body and fields.
type docState struct {
	opts    ValidateOptions
	blocks  int               // blocks checked so far
	ids     map[string]string // block ID to the path of its "id" where first used
	anchors map[string]string // anchor to the path of its "anchor" where first used
	prev    map[string]*Block // blocks of opts.Previous by ID; built on first use
}

func (x Blocks) Validate(opts ValidateOptions) error {
	c := checker{doc: &docState{opts: opts}}
	x.check(&c, "")
	return c.result()
}

// check checks x as a top-level list of blocks in the document of c.doc, which it creates if nil.
func (x Blocks) check(c *checker, path string) {
	if c.doc == nil {
		c.doc = &docState{}
	}
	x.checkIn(c, path, "", 1, false)
}

// rowSections orders the sections of table rows; "" is body.
var rowSections = map[string]int{"head": 0, "": 1, "body": 1, "foot": 2}

// checkIn checks x, the content of a block of type parent ("" for the top level), whose blocks are at depth.
// inColumns reports whether a columns block holds x.
func (x Blocks) checkIn(c *checker, path string, parent BlockType, depth int, inColumns bool) {
	if len(x) > 0 && depth > MaxDepth {
		c.addf(path, "blocks are nested too deeply (max %d)", MaxDepth)
		return
	}
	ps := blockSpecs[parent]
	section := 0 // the furthest section of the rows so far
	for i := range x {
		b := &x[i]
		p := path + "/" + strconv.Itoa(i)
		if parent == BlockTable && b.Type == BlockTableRow {
			if s, ok := rowSections[b.Attrs.Section]; ok {
				if s < section {
					c.addf(p, "table rows must be in the order head, body, foot")
				}
				section = max(section, s)
			}
		}
		c.doc.blocks++
		if c.doc.blocks == MaxBlocks+1 {
			c.addf(p, "too many blocks (max %d)", MaxBlocks)
		}
		spec, ok := blockSpecs[b.Type]
		if !ok {
			c.addf(p+"/type", "unknown block type %q", b.Type)
			continue
		}
		// listItem first: the rules of flow containers would report its children again.
		switch {
		case parent == BlockListItem:
			switch {
			case i == 0 && b.Type != BlockParagraph:
				c.addf(p, "listItem must start with a paragraph")
			case i == 1 && b.Type != BlockList, i > 1:
				c.addf(p, "listItem can only contain a paragraph and a list")
			}
		case ps.child != "":
			if b.Type != ps.child {
				c.addf(p, "%s can only contain %s", parent, ps.child)
			}
		case spec.parent != "":
			c.addf(p, "%s must be inside %s", b.Type, spec.parent)
		}
		if b.Type == BlockColumns && inColumns {
			c.addf(p, "columns cannot be nested inside columns")
		}
		b.check(c, p, spec, depth, inColumns)
	}
}

// check checks x, a block of a known type at depth, apart from its place in the document.
// inColumns reports whether a columns block holds x.
func (x *Block) check(c *checker, path string, spec blockSpec, depth int, inColumns bool) {
	if x.Type == BlockHTML && !c.doc.opts.AllowHTML {
		c.addf(path, "html blocks are not enabled")
	}
	if x.Type == BlockLegacy && !c.doc.opts.Import && !c.unchangedLegacy(x) {
		c.addf(path, "legacy blocks can only be created by import")
	}
	if x.Type == BlockTable {
		x.checkCells(c, path)
	}
	if spec.part {
		notAllowed := func(set bool, key string) {
			if set {
				c.addf(path+"/"+key, "not allowed for %s", x.Type)
			}
		}
		notAllowed(x.ID != "", "id")
		notAllowed(x.Variant != "", "variant")
		notAllowed(x.ClassName != "", "className")
		notAllowed(x.Anchor != "", "anchor")
		notAllowed(x.Style != (Style{}), "style")
	} else {
		c.blockID(path+"/id", x.ID)
		if x.Variant != "" {
			c.add(path+"/variant", checkName(x.Variant))
		}
		if x.ClassName != "" {
			c.add(path+"/className", checkName(x.ClassName))
		}
		if x.Anchor != "" {
			c.anchor(path+"/anchor", x.Anchor)
		}
		x.Style.check(c, path+"/style")
	}
	for _, k := range x.Attrs.keys() {
		if !slices.Contains(spec.attrs, k) {
			c.addf(path+"/attrs/"+k, "not allowed for %s", x.Type)
		}
	}
	x.checkAttrs(c, path+"/attrs")
	x.checkContent(c, path+"/content", spec, depth, inColumns)
}

func (x *Block) checkContent(c *checker, path string, spec blockSpec, depth int, inColumns bool) {
	if len(x.Text) > 0 && spec.content != contentInline {
		c.addf(path, "rich text content is not allowed for %s", x.Type)
	}
	if len(x.Children) > 0 && spec.content != contentBlocks {
		c.addf(path, "child blocks are not allowed for %s", x.Type)
	}
	if x.Code != "" && spec.content != contentText {
		c.addf(path, "text content is not allowed for %s", x.Type)
	}
	switch spec.content {
	case contentNone:
	case contentInline:
		if x.Type == BlockHeading && len(x.Text) == 0 {
			c.addf(path, "empty heading")
		}
		x.Text.check(c, path)
	case contentBlocks:
		if len(x.Children) < spec.min {
			what := cmp.Or(string(spec.child), "block")
			if x.Type == BlockListItem {
				what = string(BlockParagraph)
			}
			c.addf(path, "%s needs at least one %s", x.Type, what)
		}
		x.Children.checkIn(c, path, x.Type, depth+1, inColumns || x.Type == BlockColumns)
	case contentText:
		if x.Code != "" {
			c.add(path, checkText(x.Code, true))
		}
	}
}

// checkCells checks the number of cells in the rows of x, a table.
func (x *Block) checkCells(c *checker, path string) {
	n := 0
	for i := range x.Children {
		row := &x.Children[i]
		if row.Type != BlockTableRow {
			continue
		}
		for j := range row.Children {
			if row.Children[j].Type == BlockTableCell {
				n++
			}
		}
	}
	if n > MaxTableCells {
		c.addf(path, "too many cells in the table (%d; max %d)", n, MaxTableCells)
	}
}

func (c *checker) blockID(path, id string) {
	if id == "" {
		c.addf(path, "missing block ID")
		return
	}
	if err := checkBlockID(id); err != nil {
		c.add(path, err)
		return
	}
	if first, ok := c.doc.ids[id]; ok {
		c.addf(path, "duplicate block ID %q (first used at %s)", id, first)
		return
	}
	if c.doc.ids == nil {
		c.doc.ids = make(map[string]string)
	}
	c.doc.ids[id] = path
}

func (c *checker) anchor(path, anchor string) {
	if err := checkName(anchor); err != nil {
		c.add(path, err)
		return
	}
	if first, ok := c.doc.anchors[anchor]; ok {
		c.addf(path, "duplicate anchor %q (first used at %s)", anchor, first)
		return
	}
	if c.doc.anchors == nil {
		c.doc.anchors = make(map[string]string)
	}
	c.doc.anchors[anchor] = path
}

// ref checks id, which refers to an entity of kind, and asks opts.RefExists whether the entity exists.
func (c *checker) ref(path string, kind RefKind, id string) {
	if err := checkName(id); err != nil {
		c.add(path, err)
		return
	}
	if c.doc != nil && c.doc.opts.RefExists != nil && !c.doc.opts.RefExists(kind, id) {
		c.addf(path, "%s %q does not exist", kind, id)
	}
}

// previous returns the block of opts.Previous, at any depth, whose ID is id; the first one if there are more.
func (c *checker) previous(id string) *Block {
	d := c.doc
	if d.prev == nil {
		d.prev = make(map[string]*Block)
		var walk func(bs Blocks)
		walk = func(bs Blocks) {
			for i := range bs {
				b := &bs[i]
				if _, ok := d.prev[b.ID]; !ok && b.ID != "" {
					d.prev[b.ID] = b
				}
				walk(b.Children)
			}
		}
		walk(d.opts.Previous)
	}
	return d.prev[id]
}

// unchangedLegacy reports whether opts.Previous has x, a legacy block, with the same html and reason.
func (c *checker) unchangedLegacy(x *Block) bool {
	p := c.previous(x.ID)
	return p != nil && p.Type == BlockLegacy && p.Attrs.HTML == x.Attrs.HTML && p.Attrs.Reason == x.Attrs.Reason
}

// keptWithoutAlt reports whether opts.Previous has x, an image, with the same media and neither alt nor decorative.
func (c *checker) keptWithoutAlt(x *Block) bool {
	p := c.previous(x.ID)
	return p != nil && p.Type == BlockImage && p.Attrs.Media == x.Attrs.Media && p.Attrs.Alt == "" && !p.Attrs.Decorative
}
