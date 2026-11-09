// Package wpblock reads the block delimiters of a WordPress post body, as the block editor writes them, into the tree
// of blocks that parse_blocks of WordPress 7.1 makes: names, attributes, inner blocks, innerHTML, and innerContent. It
// differs in three ways, in which it follows the later versions of WordPress (6.10.5 of the design document): blocks that
// the body leaves open are closed one inside another at its end; a piece of HTML is left out only if it is empty, not
// also if it is "0"; and an empty piece of HTML before the closing delimiter of an inner block is left out, where 7.1
// keeps it in innerContent.
package wpblock

import (
	"errors"
	"strings"
)

const DefaultMaxBlocks = 100000

var ErrLimit = errors.New("wpblock: too many blocks")

// Block is a block, or HTML between blocks (freeform HTML, whose Name is empty).
//
// Start and End are the byte range of the block in the document: from the "<" of its opening delimiter to the end of
// its closing delimiter, or to the end of the document if it is Unclosed. For freeform HTML they are the range of the
// HTML. OpenerEnd and CloserStart bound what is inside the delimiters: CloserStart is End where there is no closing
// delimiter, and OpenerEnd is Start for freeform HTML.
type Block struct {
	// Name has its namespace: core/paragraph.
	Name string
	// Attrs are the attributes in the opening delimiter: an object with no members where there are none, and nil where
	// the delimiter has them but they are not valid JSON (null in WordPress).
	Attrs *Value
	Inner []Block
	// Content is innerContent: the pieces of HTML of the block and the places of its inner blocks, in the order of the
	// document.
	Content []Piece

	Start, End, OpenerEnd, CloserStart int

	// Void is a block written as one delimiter. Unclosed is a block that the document ends before it closes, or one of
	// the blocks that are open inside it. Stray is the HTML that WordPress reads when a closing delimiter closes no
	// block: everything from the end of the last block to the end of the document, delimiters included.
	Void, Unclosed, Stray bool
}

// Piece is an element of Content: a piece of HTML, or the place of Inner[Inner].
type Piece struct {
	HTML  string
	Inner int // -1 for a piece of HTML, which is never empty
}

// HTML returns innerHTML: the HTML pieces of the block, without the HTML of its inner blocks.
func (b *Block) HTML() string {
	var html string
	n := 0
	for _, p := range b.Content {
		if p.Inner < 0 {
			html = p.HTML
			n++
		}
	}
	if n < 2 {
		return html
	}
	var sb strings.Builder
	for _, p := range b.Content {
		sb.WriteString(p.HTML)
	}
	return sb.String()
}

type Options struct {
	// MaxBlocks is the most blocks to read, inner blocks too, not counting freeform HTML. Zero or less is
	// DefaultMaxBlocks.
	MaxBlocks int
}

// Parse returns the blocks at the top level of doc. The error is ErrLimit if there are more blocks than the limit.
func Parse(doc string, opts Options) ([]Block, error) {
	return (&parser{doc: doc, s: scanner{doc: doc}}).run(opts)
}

type parser struct {
	doc   string
	s     scanner
	stack []frame
	out   []Block
}

// frame is a block whose closing delimiter has not been read.
type frame struct {
	block Block
	// leading is where the HTML between the delimiter before the block and the block starts, or -1 if there is none.
	leading int
	// prev is where the next piece of HTML of the block starts.
	prev int
}

func (f *frame) addHTML(doc string, end int) {
	if end > f.prev {
		f.block.Content = append(f.block.Content, Piece{HTML: doc[f.prev:end], Inner: -1})
	}
}

func (f *frame) addInner(doc string, inner Block) {
	f.addHTML(doc, inner.Start)
	f.block.Content = append(f.block.Content, Piece{Inner: len(f.block.Inner)})
	f.block.Inner = append(f.block.Inner, inner)
	f.prev = inner.End
}

func (p *parser) run(opts Options) ([]Block, error) {
	limit := opts.MaxBlocks
	if limit <= 0 {
		limit = DefaultMaxBlocks
	}
	offset, count := 0, 0
	for {
		tok, ok := p.s.next(offset)
		if !ok {
			p.finish(offset)
			return p.out, nil
		}
		leading := -1
		if tok.start > offset {
			leading = offset
		}
		switch tok.kind {
		case kindVoid:
			if count++; count > limit {
				return nil, ErrLimit
			}
			p.void(tok, leading)
		case kindOpener:
			if count++; count > limit {
				return nil, ErrLimit
			}
			p.stack = append(p.stack, frame{
				block:   Block{Name: tok.name, Attrs: tok.attrs, Start: tok.start, OpenerEnd: tok.end},
				leading: leading,
				prev:    tok.end,
			})
		case kindCloser:
			if !p.close(tok, offset) {
				return p.out, nil
			}
		}
		offset = tok.end
	}
}

func (p *parser) freeform(start, end int) Block {
	return Block{
		Attrs:     &Value{Kind: Object},
		Content:   []Piece{{HTML: p.doc[start:end], Inner: -1}},
		Start:     start,
		End:       end,
		OpenerEnd: start, CloserStart: end,
	}
}

func (p *parser) void(tok token, leading int) {
	b := Block{
		Name: tok.name, Attrs: tok.attrs, Start: tok.start, End: tok.end, OpenerEnd: tok.end, CloserStart: tok.end,
		Void: true,
	}
	if n := len(p.stack); n > 0 {
		p.stack[n-1].addInner(p.doc, b)
		return
	}
	if leading >= 0 {
		p.out = append(p.out, p.freeform(leading, tok.start))
	}
	p.out = append(p.out, b)
}

// close closes the block that is open. If none is, it reads the rest of the document from offset as HTML and reports
// false.
func (p *parser) close(tok token, offset int) bool {
	n := len(p.stack)
	if n == 0 {
		stray := p.freeform(offset, len(p.doc))
		stray.Stray = true
		p.out = append(p.out, stray)
		return false
	}
	f := p.stack[n-1]
	p.stack[n-1] = frame{}
	p.stack = p.stack[:n-1]
	f.addHTML(p.doc, tok.start)
	b := f.block
	b.End, b.CloserStart = tok.end, tok.start
	if n > 1 {
		p.stack[n-2].addInner(p.doc, b)
		return true
	}
	if f.leading >= 0 {
		p.out = append(p.out, p.freeform(f.leading, b.Start))
	}
	p.out = append(p.out, b)
	return true
}

// finish ends the read at the end of the document, from offset, closing the blocks that are open from the inside out.
func (p *parser) finish(offset int) {
	end := len(p.doc)
	if len(p.stack) == 0 {
		if offset < end {
			p.out = append(p.out, p.freeform(offset, end))
		}
		return
	}
	var inner Block
	leading := -1
	for i := len(p.stack) - 1; i >= 0; i-- {
		f := p.stack[i]
		p.stack[i] = frame{}
		if i == len(p.stack)-1 {
			f.addHTML(p.doc, end)
		} else {
			f.addInner(p.doc, inner)
		}
		inner = f.block
		inner.End, inner.CloserStart, inner.Unclosed = end, end, true
		leading = f.leading
	}
	p.stack = p.stack[:0]
	if leading >= 0 {
		p.out = append(p.out, p.freeform(leading, inner.Start))
	}
	p.out = append(p.out, inner)
}
