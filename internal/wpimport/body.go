package wpimport

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wpblock"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

// The limits of the conversion of a body. They are variables so that the tests can lower them.
var (
	maxBodyBytes = content.MaxDocumentBytes // the size of the content of an item
	maxWPBlocks  = 4 * content.MaxBlocks    // the blocks of WordPress in the content, and the blocks and parts converted, patterns included
	maxHTMLBytes = content.MaxDocumentBytes // the bytes of HTML that the conversion reads
	// The HTML of one block, without that of its inner blocks, that the conversion reads: the tree of the HTML takes some 50
	// to 70 times as many bytes.
	maxBlockHTMLBytes = 8 << 20
	maxRichBytes      = content.MaxTextBytes // the text of a rich text, counting richPieceBytes more for each piece of it
)

const (
	reasonStray     = "a block closer without an opener ends the blocks; WordPress shows the rest as it is"
	reasonElementor = "the page was made with Elementor, which showed its own data in place of the content"
)

// BodyOptions are what Body needs to convert the content of an item.
type BodyOptions struct {
	// Site is what Site returned: its AllowHTML decides whether custom HTML can stay an html block.
	Site *content.Site
	// Settings are the corrected settings, whose Blocks rename the presets and the like. With nil, names stay as they are.
	Settings *Settings
	// Item is the item whose Content is converted, and the source of the entries of the report.
	Item *wxr.Item
	// Target is the document that the blocks will be written in, and the pointer in it of the body, such as /body.
	Target Target
	// Key makes the block IDs: the same key and the same content give the same IDs, so use the ID of the revision.
	Key string
}

type BodyResult struct {
	Blocks content.Blocks
	// More is the number of blocks at the top before the first read more block, or tag in the HTML of the classic editor,
	// at the top, which is not imported, or -1 if there is none.
	More int
}

// Body converts the content of opts.Item, which the block editor or the classic editor wrote, into blocks (6.5, 6.10.2). The
// conversion of an entry calls it once for each item, after Site and the conversion of the media. It adds to the report what
// it did not import, changed, or made a legacy block of, each with the place in the document at opts.Target. The blocks pass
// Validate as those of an import (6.7), also where the conversion made something that does not: that becomes a legacy block
// of the original text. The error is for a nil Site or Item.
func (imp *Import) Body(opts BodyOptions) (*BodyResult, error) {
	if opts.Site == nil {
		return nil, errors.New("the site is needed to convert the content")
	}
	if opts.Item == nil {
		return nil, errors.New("the item is needed to convert the content")
	}
	c := newBodyConv(imp, opts)
	blocks, more := c.run()
	c.flush(blocks)
	return &BodyResult{Blocks: blocks, More: more}, nil
}

func newBodyConv(imp *Import, opts BodyOptions) *bodyConv {
	return &bodyConv{
		imp:        imp,
		opts:       opts,
		ids:        newBlockIDs(opts.Key),
		anchors:    make(map[string]bool),
		origins:    make(map[string]origin),
		patterns:   make(map[int64]bool),
		moreTop:    -1,
		pieceLeft:  maxWPBlocks,
		targetLeft: maxWPBlocks,
	}
}

// bodyConv is the state of the conversion of one body.
type bodyConv struct {
	imp  *Import
	opts BodyOptions
	ids  *blockIDs

	anchors map[string]bool // the anchors of the blocks made so far
	origins map[string]origin
	calls   int // the conversions so far, which number the origins
	notes   []bodyNote
	// anchorLog is the anchors in the order they were added: a block that is made a legacy block gives its anchors back.
	anchorLog []string

	blocks     int // the blocks of WordPress converted so far, and the parts that converters made of elements, such as list items
	htmlBytes  int // the bytes of HTML read so far
	pageBreaks int
	stopped    bool
	// moreTop is the index of the first read more block or tag at the top among the blocks of WordPress, or -1. moreSub says
	// where it is in the HTML of the classic editor that block holds: the blocks that origin.sub numbers below it come before.
	moreTop, moreSub int
	autop            bool // the content has no block delimiter, so WordPress makes its paragraphs
	// patterns are the synced patterns that are being copied, by the IDs of their posts.
	patterns map[int64]bool
	base     *url.URL // the address that the relative addresses are from, made when the first of them is read
	baseLen  int      // the length of base as text, found with it

	// classicDepth is more than 0 while the HTML of the classic editor is converted. In it, the shortcodes and the URLs of a line of
	// their own are elements that stand for pieces, named pieceName.
	classicDepth int
	pieceName    string
	pieces       []*classicPiece
	// pieceLeft is how many more shortcodes and URLs the conversion may make elements of; piecesCut says that some were left as
	// text because of it, and cutNoted that the report says so.
	pieceLeft           int
	piecesCut, cutNoted bool
	// inClassic is set while the conversion reads the HTML of the classic editor, as opposed to a shortcode of the block editor
	// that it reads the same way (classicDepth).
	inClassic bool
	// targets are the ids and names of the a elements with no address that the rich text read since a block began, in order: the
	// block takes them as its anchor. targetLeft is how many more the conversion may take; targetsCut says that it left some
	// because of it, and targetsNoted that the report says so.
	targets      []string
	targetLeft   int
	targetsCut   bool
	targetsNoted bool
}

// origin is the block of WordPress that a block was made from, in the document doc, and the index of the block at the top
// of the content that holds it. call numbers the conversion of b that made the block: the blocks of one conversion go
// together, and two copies of a synced pattern are two conversions. guard is set for a legacy block that the validation
// guard made. The blocks that the conversion of HTML of the classic editor makes have no b and doc. Those at the top have
// sub, their number in it from 1, and those it makes of an element that holds no blocks have html, the element as HTML, and
// what to call it: the guard makes a legacy block of that.
type origin struct {
	b     *wpblock.Block
	doc   string
	top   int
	sub   int
	call  int
	guard bool
	html  string
	what  string
}

// newOrigin returns the origin of the blocks of a new conversion of b.
func (c *bodyConv) newOrigin(b *wpblock.Block, doc string, top int) origin {
	c.calls++
	return origin{b: b, doc: doc, top: top, call: c.calls}
}

// noteAt is where a note goes: the block with the ID, or the body if id is "", and the pointer sub below it. The
// notes of blocks that are not in the end result are not reported.
type noteAt struct{ id, sub string }

type bodyNote struct {
	at   noteAt
	kind Kind
	msg  string
	// stop is set for the note that the conversion has stopped. It is kept when the block that was being converted is made
	// a legacy block, and put at the body if the block that it is at is not in the result.
	stop bool
	// list is set for a note that names things, instead of msg (see noteItems).
	list *noteList
}

// noteList is the message of a note that names things: head, the things, and tail.
type noteList struct {
	head, tail string
	items      []string
}

func (c *bodyConv) note(at noteAt, kind Kind, format string, args ...any) {
	c.notes = append(c.notes, bodyNote{at: at, kind: kind, msg: fmt.Sprintf(format, args...)})
}

// noteItems notes things, such as the elements of which only the text is kept. The notes with the same place, kind, head
// and tail are one note when they are written: its message is head, the things in the order of their first appearance,
// each once, and tail. Only the first maxLostShown are named, and the number of the others follows.
func (c *bodyConv) noteItems(at noteAt, kind Kind, head, tail string, items ...string) {
	if len(items) > 0 {
		c.notes = append(c.notes, bodyNote{at: at, kind: kind, list: &noteList{head: head, tail: tail, items: items}})
	}
}

func (l *noteList) text() string {
	shown := l.items[:min(len(l.items), maxLostShown)]
	s := l.head + strings.Join(shown, ", ")
	if more := len(l.items) - len(shown); more > 0 {
		s += fmt.Sprintf(", and %d more", more)
	}
	return s + l.tail
}

// mergedNotes returns the notes with those that name things made one for each place, kind, head and tail, at the place
// of the first.
func (c *bodyConv) mergedNotes() []bodyNote {
	type key struct {
		at         noteAt
		kind       Kind
		head, tail string
	}
	type group struct {
		index int
		seen  map[string]bool
	}
	var out []bodyNote
	groups := make(map[key]*group)
	for _, n := range c.notes {
		if n.list == nil {
			out = append(out, n)
			continue
		}
		k := key{n.at, n.kind, n.list.head, n.list.tail}
		g := groups[k]
		if g == nil {
			g = &group{index: len(out), seen: make(map[string]bool)}
			groups[k] = g
			out = append(out, bodyNote{at: n.at, kind: n.kind, list: &noteList{head: n.list.head, tail: n.list.tail}})
		}
		merged := out[g.index].list
		for _, item := range n.list.items {
			if !g.seen[item] {
				g.seen[item] = true
				merged.items = append(merged.items, item)
			}
		}
	}
	for i := range out {
		if out[i].list != nil {
			out[i].msg = out[i].list.text()
		}
	}
	return out
}

// bodyPlace is where the blocks that a flow converts will be: the depth at which they are made (the top is 1), whether a
// columns block holds them, and the index among the blocks of WordPress at the top of the content of the one that
// holds them, which is -1 for the call for the top itself.
type bodyPlace struct {
	depth     int
	inColumns bool
	top       int
}

// converter makes the blocks of the block b of WordPress, which is in doc, to be put at p. at is where to note what it
// does not make a block of. A converter that cannot convert b returns legacyf, and b becomes a legacy block.
type converter func(c *bodyConv, doc string, b *wpblock.Block, p bodyPlace, at noteAt) ([]content.Block, error)

// converters are the converters by the names of the blocks, which init functions fill: a converter that converts the
// inner blocks calls flow, which reads this, so a map literal here would be a cycle in the initialization.
var converters = map[string]converter{}

// converterPrefixes are the converters of the blocks that have no converter by their names, by the beginning of the name
// up to its slash, such as "core-embed/": there is a block for each provider.
var converterPrefixes = map[string]converter{}

func init() {
	converters["core/nextpage"] = convertNextPage
	converters["core/more"] = convertMore
	converters["core/freeform"] = convertFreeform
	converters["core/shortcode"] = convertShortcode
}

// legacyError says why a block is made a legacy block.
type legacyError struct{ reason string }

func (e *legacyError) Error() string { return e.reason }

func legacyf(format string, args ...any) error {
	return &legacyError{reason: fmt.Sprintf(format, args...)}
}

// newBlock returns a block of type t with an ID, unless it is a part.
func (c *bodyConv) newBlock(t content.BlockType) content.Block {
	b := content.Block{Type: t}
	if t != content.BlockListItem && t != content.BlockTableRow && t != content.BlockTableCell {
		b.ID = c.ids.next()
	}
	return b
}

func (c *bodyConv) run() (content.Blocks, int) {
	text := c.opts.Item.Content
	body := noteAt{}
	switch {
	case len(text) > maxBodyBytes:
		c.note(body, Skipped, "the content is not imported: it is too large: %d bytes (max %d)", len(text), maxBodyBytes)
		return nil, -1
	case blank(text):
		return nil, -1
	case builtWithElementor(c.opts.Item):
		// Elementor shows its own data in place of the content, which is the HTML that it made of them.
		blocks := content.Blocks(c.legacyText(text, "the content of the page made with Elementor", reasonElementor, body))
		blocks.Normalize()
		c.guard(&blocks)
		return blocks, -1
	}
	// As has_blocks of WordPress: a delimiter that is not one of a block (<!-- wp:Foo -->) is enough to keep wpautop away.
	c.autop = !strings.Contains(text, "<!-- wp:")
	parsed, err := wpblock.Parse(text, wpblock.Options{MaxBlocks: maxWPBlocks})
	if err != nil {
		c.note(body, Skipped, "the content is not imported: it has more than %d blocks", maxWPBlocks)
		return nil, -1
	}
	blocks := c.flow(text, withClassic(parsed, text), bodyPlace{depth: 1, top: -1}, body)
	if n := c.pageBreaks; n > 0 {
		verb := "are"
		if n == 1 {
			verb = "is"
		}
		c.note(body, Skipped, "%s %s removed; whiteroom has no page breaks", count(n, "page break"), verb)
	}
	blocks = c.limit(blocks)
	blocks.Normalize()
	c.guard(&blocks)

	more := -1
	if c.moreTop >= 0 {
		more = 0
		for i := range blocks {
			if o, ok := c.origins[blocks[i].ID]; ok && (o.top < c.moreTop || o.top == c.moreTop && o.sub < c.moreSub) {
				more++
			}
		}
	}
	return blocks, more
}

// builtWithElementor reports whether the item was made with Elementor, as Elementor tells it: its postmeta has an edit mode
// that PHP counts as true.
func builtWithElementor(it *wxr.Item) bool {
	mode, ok := it.Meta.Get("_elementor_edit_mode")
	return ok && mode != "" && mode != "0"
}

// withClassic returns parsed, the blocks of the content text, or, if none of them has a name, one block of all of the text,
// which is the HTML of the classic editor. It is made as the parser makes HTML between blocks, with the HTML in its pieces.
func withClassic(parsed []wpblock.Block, text string) []wpblock.Block {
	if slices.ContainsFunc(parsed, func(b wpblock.Block) bool { return b.Name != "" }) {
		return parsed
	}
	return []wpblock.Block{{
		Attrs:   &wpblock.Value{Kind: wpblock.Object},
		Content: []wpblock.Piece{{HTML: text, Inner: -1}},
		End:     len(text), CloserStart: len(text),
	}}
}

// blank reports whether s is empty or made of the white space of HTML (ASCII only).
func blank(s string) bool {
	return strings.Trim(s, " \t\n\f\r") == ""
}

// flow converts the blocks bs of the content doc, which are at p, into blocks. at is where to note what is left out.
func (c *bodyConv) flow(doc string, bs []wpblock.Block, p bodyPlace, at noteAt) content.Blocks {
	var out content.Blocks
	for i := range bs {
		if c.stopped {
			break
		}
		b := &bs[i]
		if b.Name != "" {
			if c.blocks++; c.blocks > maxWPBlocks {
				c.stop(at)
				break
			}
		}
		top := p.top
		if top < 0 {
			top = i
		}
		blocks := c.convert(doc, b, bodyPlace{depth: p.depth, inColumns: p.inColumns, top: top}, at)
		o := c.newOrigin(b, doc, top)
		for j := range blocks {
			if id := blocks[j].ID; id != "" {
				if _, ok := c.origins[id]; !ok {
					c.origins[id] = o
				}
			}
		}
		out = append(out, blocks...)
	}
	return out
}

// stop ends the conversion: the blocks that come after are not converted.
func (c *bodyConv) stop(at noteAt) {
	c.stopped = true
	c.notes = append(c.notes, bodyNote{
		at: at, kind: Skipped, stop: true,
		msg: fmt.Sprintf("the rest of the content is not imported: the conversion reads at most %d blocks and %d bytes of HTML",
			maxWPBlocks, maxHTMLBytes),
	})
}

// forget drops the notes made since mark, except the one that says the conversion has stopped.
func (c *bodyConv) forget(mark int) {
	kept := c.notes[:mark]
	for _, n := range c.notes[mark:] {
		if n.stop {
			kept = append(kept, n)
		}
	}
	c.notes = kept
}

// readHTML counts n bytes of HTML that the conversion is going to read. Once it has read more than the limit, it stops
// the conversion and returns the error that makes the block a legacy block.
func (c *bodyConv) readHTML(n int) error {
	c.htmlBytes += n
	if c.htmlBytes <= maxHTMLBytes {
		return nil
	}
	if !c.stopped {
		c.stop(noteAt{})
	}
	return legacyf("the conversion has read more than %d bytes of HTML", maxHTMLBytes)
}

// convert makes the blocks of b, or a legacy block if it cannot.
func (c *bodyConv) convert(doc string, b *wpblock.Block, p bodyPlace, at noteAt) []content.Block {
	// Normalize removes the control characters, and a legacy block of nothing does not pass.
	if b.Name == "" && blank(strings.Map(dropControl, doc[b.Start:b.End])) {
		return nil
	}
	mark, pageBreaks, anchors := len(c.notes), c.pageBreaks, len(c.anchorLog)
	conv := b
	if b.Attrs == nil {
		// The converters read the attributes as an object with no members.
		cp := *b
		cp.Attrs = &wpblock.Value{Kind: wpblock.Object}
		conv = &cp
	}
	blocks, err := c.dispatch(doc, conv, p, at)
	if err == nil && deeper(blocks, content.MaxDepth-p.depth+1) {
		err = legacyf("the blocks are nested more than %d deep", content.MaxDepth)
	}
	if err != nil {
		reason := err.Error()
		var le *legacyError
		if errors.As(err, &le) {
			reason = le.reason
		}
		// Nothing else is said of the block that is made a legacy block.
		c.forget(mark)
		c.pageBreaks = pageBreaks
		for _, id := range c.anchorLog[anchors:] {
			delete(c.anchors, id)
		}
		c.anchorLog = c.anchorLog[:anchors]
		blocks = c.legacy(doc, b, reason, at)
	}
	if b.Name != "" {
		target := at
		if len(blocks) > 0 && blocks[0].ID != "" {
			target = noteAt{id: blocks[0].ID}
		}
		c.noteDelimiter(b, target)
	}
	return blocks
}

// noteDelimiter notes what is wrong with the delimiters of the block b: attributes that are not JSON, and a block that
// the content leaves open. A converter that converts an inner block by itself, not with flow, calls it for the block.
func (c *bodyConv) noteDelimiter(b *wpblock.Block, at noteAt) {
	if b.Attrs == nil {
		c.note(at, Changed, "the attributes of the block %q are not valid JSON; they are ignored", b.Name)
	}
	if b.Unclosed {
		c.note(at, Review, "the block %q is not closed; it ends at the end of the content", b.Name)
	}
}

func (c *bodyConv) dispatch(doc string, b *wpblock.Block, p bodyPlace, at noteAt) ([]content.Block, error) {
	conv := converters[b.Name]
	if conv == nil {
		if prefix, _, ok := strings.Cut(b.Name, "/"); ok {
			conv = converterPrefixes[prefix+"/"]
		}
	}
	switch {
	case b.Name == "" && b.Stray:
		return nil, legacyf(reasonStray)
	case b.Name == "":
		return c.classic(doc, b, p, at)
	case p.depth > content.MaxDepth:
		return nil, legacyf("the blocks are nested more than %d deep", content.MaxDepth)
	case conv != nil:
		return conv(c, doc, b, p, at)
	case dynamicBlock(b.Name):
		return nil, legacyf("the block %q is made by WordPress when the page is shown", b.Name)
	}
	return nil, legacyf("the block %q is not converted", b.Name)
}

// legacy returns a legacy block of the original text of b, with the reason, and notes it for review. If the text is too
// long, it returns none, and notes at at that the block is not imported.
func (c *bodyConv) legacy(doc string, b *wpblock.Block, reason string, at noteAt) []content.Block {
	what := fmt.Sprintf("the block %q", b.Name)
	switch {
	case b.Name != "":
	case b.Stray:
		what = "the rest of the content after a block closer without an opener"
	case b.Start == 0 && b.End == len(doc) && len(c.patterns) > 0:
		what = "the content of the synced pattern"
	case b.Start == 0 && b.End == len(doc):
		what = "the content of the classic editor"
	default:
		what = "the HTML between blocks"
	}
	return c.legacyText(doc[b.Start:b.End], what, reason, at)
}

// legacyHTML is legacy for a part of the HTML of a block that a converter makes a legacy block of, such as an item of an
// old gallery: text is the HTML of the part.
func (c *bodyConv) legacyHTML(text, reason string, at noteAt) []content.Block {
	return c.legacyText(text, "a part of the block", reason, at)
}

func (c *bodyConv) legacyText(text, what, reason string, at noteAt) []content.Block {
	text = c.restorePieces(text)
	if len(text) > content.MaxTextBytes {
		c.note(at, Skipped, "%s is not imported: its HTML is too long: %d bytes (max %d)", what, len(text), content.MaxTextBytes)
		return nil
	}
	l := c.newBlock(content.BlockLegacy)
	l.Attrs = content.Attrs{HTML: text, Reason: reason}
	c.note(noteAt{id: l.ID}, Review, "%s; it is a legacy block", reason)
	return []content.Block{l}
}

func convertNextPage(c *bodyConv, _ string, _ *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
	// WordPress splits a post into pages on its own content: in a synced pattern the block is a comment.
	if len(c.patterns) == 0 {
		c.pageBreaks++
	}
	return nil, nil
}

func convertMore(c *bodyConv, _ string, b *wpblock.Block, p bodyPlace, at noteAt) ([]content.Block, error) {
	switch {
	case len(c.patterns) > 0:
		// WordPress splits the teaser on the content of the post only: in a synced pattern the block is a comment.
	case p.depth != 1:
		c.note(at, Skipped, "the more block is removed: it is not at the top of the content")
	case c.moreTop < 0:
		c.moreTop, c.moreSub = p.top, 0
		if hid, _ := b.Attrs.Get("noTeaser").IsTrue(); hid {
			c.note(noteAt{}, Changed, "the more block hid the text before it on the page (noTeaser); whiteroom shows it")
		}
		if text, _ := b.Attrs.Get("customText").Str(); text != "" {
			c.note(noteAt{}, Skipped, "the text %q of the more link is not imported", text)
		}
	}
	return nil, nil
}

func convertFreeform(c *bodyConv, doc string, b *wpblock.Block, p bodyPlace, at noteAt) ([]content.Block, error) {
	return c.classic(doc, b, p, at)
}

// convertShortcode converts the content of the shortcode block as WordPress shows it: the HTML of the classic editor, with its
// paragraphs made.
func convertShortcode(c *bodyConv, _ string, b *wpblock.Block, p bodyPlace, at noteAt) ([]content.Block, error) {
	src := classicSource{text: b.HTML(), what: `the block "core/shortcode"`, autop: true, top: len(c.patterns) == 0}
	return c.classicContent(src, p, at), nil
}

// limit leaves out the blocks from the one at the top that makes the document have more than content.MaxBlocks blocks.
func (c *bodyConv) limit(blocks content.Blocks) content.Blocks {
	total := 0
	for i := range blocks {
		if total += countBlocks(blocks[i : i+1]); total > content.MaxBlocks {
			c.note(noteAt{}, Skipped, "the blocks from %s/%d on are not imported: a document has at most %d blocks",
				c.opts.Target.Pointer, i, content.MaxBlocks)
			return blocks[:i]
		}
	}
	return blocks
}

// countBlocks returns the number of blocks in bs, parts and blocks at any depth included.
func countBlocks(bs content.Blocks) int {
	n := 0
	stack := []content.Blocks{bs}
	for len(stack) > 0 {
		list := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n += len(list)
		for i := range list {
			if len(list[i].Children) > 0 {
				stack = append(stack, list[i].Children)
			}
		}
	}
	return n
}

// deeper reports whether bs, as blocks at depth 1, have blocks at a depth of more than limit.
func deeper(bs []content.Block, limit int) bool {
	type level struct {
		blocks []content.Block
		depth  int
	}
	stack := []level{{bs, 1}}
	for len(stack) > 0 {
		l := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if len(l.blocks) > 0 && l.depth > limit {
			return true
		}
		for i := range l.blocks {
			if len(l.blocks[i].Children) > 0 {
				stack = append(stack, level{l.blocks[i].Children, l.depth + 1})
			}
		}
	}
	return false
}

// validateBlocks is Blocks.Validate; the tests count the calls of the guard.
var validateBlocks = content.Blocks.Validate

// guard makes blocks pass Validate: where it finds a problem, the block of a flow that holds it is made a legacy block of
// the original text, with the blocks that came from the same conversion of a block of WordPress, or is left out if it has
// no origin or is a legacy block that the guard made. A round mends all the problems that Validate finds, so that the
// rounds are few however many the problems are.
func (c *bodyConv) guard(blocks *content.Blocks) {
	opts := content.ValidateOptions{AllowHTML: c.opts.Site.AllowHTML, Import: true, Lookup: c.imp.Ref}
	for range 2*countBlocks(*blocks) + 2 {
		err := validateBlocks(*blocks, opts)
		if err == nil {
			return
		}
		c.repair(blocks, problems(err))
	}
	if err := validateBlocks(*blocks, opts); err != nil {
		*blocks = nil
		c.note(noteAt{}, Skipped, "the content is not imported: it does not pass validation: %s", problems(err)[0])
	}
}

// problems returns the problems of err, which Validate returned.
func problems(err error) content.Errors {
	var errs content.Errors
	if errors.As(err, &errs) && len(errs) > 0 {
		return errs
	}
	return content.Errors{{Msg: err.Error()}}
}

// unit is the block that repair takes out for a problem at a place.
type unit struct {
	list  *content.Blocks // the blocks of the flow that holds it
	index int
	owner noteAt // of the flow
	rel   string // the pointer of the problem below the block
	at    []int  // the indexes of the block and of the blocks above it, from the top
}

// findUnit finds, for the problem at path in blocks, the deepest block on the path that a flow holds: the top, and the
// children of a quote, a group and a column.
func findUnit(blocks *content.Blocks, path string) (u unit, ok bool) {
	segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	list, inFlow, owner := blocks, true, noteAt{}
	var at []int
	for pos := 0; pos < len(segs); {
		i, err := strconv.Atoi(segs[pos])
		if err != nil || i < 0 || i >= len(*list) {
			break
		}
		blk := &(*list)[i]
		at = append(at, i)
		pos++
		if inFlow {
			u, ok = unit{list: list, index: i, owner: owner, at: slices.Clone(at)}, true
			if pos < len(segs) {
				u.rel = "/" + strings.Join(segs[pos:], "/")
			}
		}
		if pos+1 >= len(segs) || segs[pos] != "content" || len(blk.Children) == 0 {
			break
		}
		holds := blk.Type == content.BlockQuote || blk.Type == content.BlockGroup || blk.Type == content.BlockColumn
		list, inFlow, owner = &blk.Children, holds, noteAt{id: blk.ID, sub: "/content"}
		pos++
	}
	return u, ok
}

// mend is what repair does for a problem: leave out the block of u, or put repl in place of the blocks of its origin o.
type mend struct {
	u       unit
	msg     string
	o       origin
	replace bool
	repl    []content.Block
}

// repair takes out the blocks that hold the problems errs. It finds them all before it changes a list.
func (c *bodyConv) repair(blocks *content.Blocks, errs content.Errors) {
	type group struct {
		list *content.Blocks
		call int
	}
	var mends []*mend
	dropped, replaced := make(map[*content.Block]bool), make(map[group]bool)
	for _, e := range errs {
		u, ok := findUnit(blocks, e.Path)
		if !ok {
			// A problem that is in no block: take out the first one, so that the guard ends.
			u = unit{list: blocks, rel: e.Path, at: []int{0}}
		}
		list := *u.list
		if len(list) == 0 {
			continue
		}
		blk := &list[u.index]
		o, known := c.origins[blk.ID]
		m := &mend{u: u, msg: e.Msg, o: o, replace: known && !o.guard}
		if m.replace {
			if g := (group{u.list, o.call}); !replaced[g] {
				replaced[g] = true
				mends = append(mends, m)
			}
		} else if !dropped[blk] {
			dropped[blk] = true
			mends = append(mends, m)
		}
	}

	changes := make(map[*content.Blocks][]*mend)
	var lists []*content.Blocks
	for _, m := range mends {
		if !m.replace {
			c.note(m.u.owner, Skipped, "the block is not imported: %s",
				(&content.Error{Path: "/" + strconv.Itoa(m.u.index) + m.u.rel, Msg: m.msg}).Error())
		} else {
			reason := "the conversion does not pass validation: " + (&content.Error{Path: m.u.rel, Msg: m.msg}).Error()
			if m.o.html != "" {
				m.repl = c.legacyText(m.o.html, m.o.what, reason, m.u.owner)
			} else {
				m.repl = c.legacy(m.o.doc, m.o.b, reason, m.u.owner)
			}
			if len(m.repl) > 0 {
				g := m.o
				g.guard = true
				c.origins[m.repl[0].ID] = g
				m.repl[0].Normalize()
			}
		}
		if changes[m.u.list] == nil {
			lists = append(lists, m.u.list)
		}
		changes[m.u.list] = append(changes[m.u.list], m)
	}
	// The lists in the blocks of a list change before that list moves its blocks: the last in the document first. A
	// change in a block that its list takes out is lost with the block.
	slices.SortStableFunc(lists, func(a, b *content.Blocks) int {
		pa, pb := changes[a][0].u.at, changes[b][0].u.at
		return slices.Compare(pb[:len(pb)-1], pa[:len(pa)-1])
	})
	for _, list := range lists {
		drop := make(map[*content.Block]bool)
		repl := make(map[int]*mend)
		for _, m := range changes[list] {
			if m.replace {
				repl[m.o.call] = m
			} else {
				drop[&(*list)[m.u.index]] = true
			}
		}
		// In place: a group of blocks gives way to at most one block, so no block is written over before it is read.
		out := (*list)[:0]
		for i := range *list {
			if drop[&(*list)[i]] {
				continue
			}
			blk := (*list)[i]
			if x, ok := c.origins[blk.ID]; ok && !x.guard {
				if m := repl[x.call]; m != nil {
					if len(m.repl) > 0 {
						// The legacy block stands where the first block of the conversion was.
						g := c.origins[m.repl[0].ID]
						g.sub = x.sub
						c.origins[m.repl[0].ID] = g
					}
					out = append(out, m.repl...)
					m.repl = nil
					continue
				}
			}
			out = append(out, blk)
		}
		*list = out
	}
}

// flush adds the notes of the blocks that are in the result to the report, at the places of the blocks.
func (c *bodyConv) flush(blocks content.Blocks) {
	pointers := map[string]string{"": ""}
	type item struct {
		block   *content.Block
		pointer string
	}
	var stack []item
	for i := range blocks {
		stack = append(stack, item{&blocks[i], "/" + strconv.Itoa(i)})
	}
	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if it.block.ID != "" {
			pointers[it.block.ID] = it.pointer
		}
		for j := range it.block.Children {
			stack = append(stack, item{&it.block.Children[j], it.pointer + "/content/" + strconv.Itoa(j)})
		}
	}
	src := ItemSource(c.imp.File, c.opts.Item)
	for _, n := range c.mergedNotes() {
		p, ok := pointers[n.at.id]
		sub := n.at.sub
		if !ok {
			if !n.stop {
				continue
			}
			p, sub = "", ""
		}
		c.imp.Report.Add(Entry{
			Kind: n.kind, Source: src, Message: n.msg,
			Target: Target{File: c.opts.Target.File, Pointer: c.opts.Target.Pointer + p + sub},
		})
	}
}
