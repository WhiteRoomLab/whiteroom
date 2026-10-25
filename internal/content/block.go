package content

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"strconv"
)

type BlockType string

const (
	BlockParagraph BlockType = "paragraph"
	BlockHeading   BlockType = "heading"
	BlockList      BlockType = "list"
	BlockListItem  BlockType = "listItem"
	BlockQuote     BlockType = "quote"
	BlockImage     BlockType = "image"
	BlockTable     BlockType = "table"
	BlockTableRow  BlockType = "tableRow"
	BlockTableCell BlockType = "tableCell"
	BlockButtons   BlockType = "buttons"
	BlockButton    BlockType = "button"
	BlockSeparator BlockType = "separator"
	BlockSpacer    BlockType = "spacer"
	BlockCode      BlockType = "code"
	BlockEmbed     BlockType = "embed"
	BlockColumns   BlockType = "columns"
	BlockColumn    BlockType = "column"
	BlockGroup     BlockType = "group"
	BlockFile      BlockType = "file"
	BlockForm      BlockType = "form"
	BlockHTML      BlockType = "html"
	BlockLegacy    BlockType = "legacy"
)

type contentKind int

const (
	contentNone   contentKind = iota
	contentInline             // RichText in Text
	contentBlocks             // child blocks in Children
	contentText               // a JSON string in Code
)

type blockSpec struct {
	attrs   []string // attribute keys, in the order Attrs writes them; nil if none
	content contentKind
	part    bool      // no id, style, variant, className or anchor
	parent  BlockType // the only type it can be placed in; "" for flow blocks
	child   BlockType // the only type of its children; "" for flow blocks
	min     int       // minimum number of children
}

var blockSpecs = map[BlockType]blockSpec{
	BlockParagraph: {attrs: []string{"textAlign"}, content: contentInline},
	BlockHeading:   {attrs: []string{"level", "textAlign"}, content: contentInline},
	BlockList:      {attrs: []string{"ordered", "start"}, content: contentBlocks, child: BlockListItem, min: 1},
	BlockListItem:  {content: contentBlocks, part: true, parent: BlockList, min: 1},
	BlockQuote:     {attrs: []string{"citation"}, content: contentBlocks, min: 1},
	BlockImage:     {attrs: []string{"media", "alt", "decorative", "caption", "link", "size", "width", "aspectRatio", "align"}},
	BlockTable:     {attrs: []string{"caption"}, content: contentBlocks, child: BlockTableRow, min: 1},
	BlockTableRow:  {attrs: []string{"section"}, content: contentBlocks, part: true, parent: BlockTable, child: BlockTableCell, min: 1},
	BlockTableCell: {attrs: []string{"header", "colspan", "rowspan", "textAlign"}, content: contentInline, part: true, parent: BlockTableRow},
	BlockButtons:   {attrs: []string{"justify"}, content: contentBlocks, child: BlockButton, min: 1},
	BlockButton:    {attrs: []string{"label", "link", "width"}, parent: BlockButtons},
	BlockSeparator: {},
	BlockSpacer:    {attrs: []string{"height"}},
	BlockCode:      {attrs: []string{"language"}, content: contentText},
	BlockEmbed:     {attrs: []string{"provider", "url", "caption"}},
	BlockColumns:   {attrs: []string{"verticalAlign", "stackOnMobile"}, content: contentBlocks, child: BlockColumn, min: 1},
	BlockColumn:    {attrs: []string{"width", "verticalAlign"}, content: contentBlocks, parent: BlockColumns},
	BlockGroup:     {attrs: []string{"layout", "tagName", "label", "align"}, content: contentBlocks},
	BlockFile:      {attrs: []string{"media", "label"}},
	BlockForm:      {attrs: []string{"form"}},
	BlockHTML:      {attrs: []string{"html"}},
	BlockLegacy:    {attrs: []string{"html", "reason"}},
}

type Block struct {
	Type      BlockType
	ID        string
	Variant   string
	ClassName string
	Anchor    string
	Style     Style
	Attrs     Attrs
	Text      RichText // content of paragraph, heading and tableCell
	Children  Blocks   // content of the blocks that hold blocks
	Code      string   // content of code
}

type Blocks []Block

func (x *Block) UnmarshalJSON(data []byte) error  { return decodeJSON(x, data) }
func (x *Blocks) UnmarshalJSON(data []byte) error { return decodeJSON(x, data) }

func (x *Block) decode(d *decoder) error {
	r := blockReader{n: 1}
	v, err := r.block(d, 1)
	if err != nil {
		return err
	}
	*x = v
	return nil
}

func (x *Blocks) decode(d *decoder) error {
	v, err := (&blockReader{}).list(d, 1, false)
	if err != nil {
		return err
	}
	*x = v
	return nil
}

// blockReader stops at MaxDepth and MaxBlocks; n counts the blocks read so far, including those in content read later.
type blockReader struct{ n int }

// list reads an array of blocks at depth; nonEmpty rejects [].
func (r *blockReader) list(d *decoder, depth int, nonEmpty bool) (Blocks, error) {
	// Element paths are built from base: before an element is read, d.pointer() is the previous one.
	base := d.pointer()
	read := d.items
	if nonEmpty {
		read = d.array
	}
	var v Blocks
	err := read(func(i int) error {
		if depth > MaxDepth {
			return &Error{Path: base, Msg: fmt.Sprintf("blocks are nested too deeply (max %d)", MaxDepth)}
		}
		r.n++
		if r.n > MaxBlocks {
			return &Error{Path: base + "/" + strconv.Itoa(i), Msg: fmt.Sprintf("too many blocks (max %d)", MaxBlocks)}
		}
		b, err := r.block(d, depth)
		if err != nil {
			return err
		}
		v = append(v, b)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return v, nil
}

func (r *blockReader) block(d *decoder, depth int) (Block, error) {
	var v Block
	var attrs, content jsontext.Value
	var attrsPath, contentPath string
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "type":
			var s string
			if s, err = d.string(); err != nil {
				return err
			}
			if _, ok := blockSpecs[BlockType(s)]; !ok {
				return d.errorf("unknown block type %q", s)
			}
			v.Type = BlockType(s)
		case "id":
			v.ID, err = d.string()
		case "variant":
			v.Variant, err = d.string()
		case "className":
			v.ClassName, err = d.string()
		case "anchor":
			v.Anchor, err = d.string()
		case "style":
			err = v.Style.decode(d)
		case "attrs":
			// attrs and content may come before type; then they are read once type is known.
			if v.Type == "" {
				attrs, attrsPath, err = d.raw()
			} else {
				err = v.Attrs.decodeFor(d, v.Type)
			}
		case "content":
			if v.Type == "" {
				content, contentPath, err = d.raw()
			} else {
				err = r.content(d, &v, depth)
			}
		default:
			return d.errorf("unknown key")
		}
		return err
	})
	if err != nil {
		return Block{}, err
	}
	if v.Type == "" {
		return Block{}, d.errorf(`missing key "type"`)
	}
	if blockSpecs[v.Type].part {
		key := ""
		switch {
		case v.ID != "":
			key = "id"
		case v.Variant != "":
			key = "variant"
		case v.ClassName != "":
			key = "className"
		case v.Anchor != "":
			key = "anchor"
		case v.Style != (Style{}):
			key = "style"
		}
		if key != "" {
			return Block{}, &Error{Path: d.pointer() + "/" + key, Msg: fmt.Sprintf("not allowed for %s", v.Type)}
		}
	}
	if attrs != nil {
		if err := unmarshal(attrs, attrsPath, func(d *decoder) error { return v.Attrs.decodeFor(d, v.Type) }); err != nil {
			return Block{}, err
		}
	}
	if content != nil {
		if err := unmarshal(content, contentPath, func(d *decoder) error { return r.content(d, &v, depth) }); err != nil {
			return Block{}, err
		}
	}
	return v, nil
}

func (r *blockReader) content(d *decoder, v *Block, depth int) error {
	var err error
	switch blockSpecs[v.Type].content {
	case contentNone:
		return d.errorf("not allowed for %s", v.Type)
	case contentInline:
		err = v.Text.decode(d)
	case contentBlocks:
		v.Children, err = r.list(d, depth+1, true)
	case contentText:
		v.Code, err = d.string()
	}
	return err
}

func (x Block) MarshalJSON() ([]byte, error) {
	var content any
	switch blockSpecs[x.Type].content {
	case contentNone:
	case contentInline:
		if len(x.Text) > 0 {
			content = x.Text
		}
	case contentBlocks:
		if len(x.Children) > 0 {
			content = x.Children
		}
	case contentText:
		if x.Code != "" {
			content = x.Code
		}
	}
	return json.Marshal(struct {
		Type      BlockType `json:"type"`
		ID        string    `json:"id,omitempty"`
		Variant   string    `json:"variant,omitempty"`
		ClassName string    `json:"className,omitempty"`
		Anchor    string    `json:"anchor,omitempty"`
		Style     Style     `json:"style,omitzero"`
		// omitempty, not omitzero: attrs with only an empty caption would be written as {}.
		Attrs   Attrs `json:"attrs,omitempty"`
		Content any   `json:"content,omitempty"`
	}{
		Type:      x.Type,
		ID:        x.ID,
		Variant:   x.Variant,
		ClassName: x.ClassName,
		Anchor:    x.Anchor,
		Style:     x.Style,
		Attrs:     x.Attrs,
		Content:   content,
	})
}

func (x Blocks) MarshalJSON() ([]byte, error) {
	// Never null, which UnmarshalJSON rejects.
	if len(x) == 0 {
		return []byte("[]"), nil
	}
	// As Blocks, x would call this method again.
	return json.Marshal([]Block(x))
}

// Normalize cleans up texts, HTML and URLs in place, also in the children and through the Link pointers, and removes empty texts.
func (x *Block) Normalize() {
	x.Text.Normalize()
	x.Children.Normalize()
	x.Code = normalizeText(x.Code, true)
	x.Attrs.normalize()
}

func (x *Blocks) Normalize() {
	for i := range *x {
		(*x)[i].Normalize()
	}
}
