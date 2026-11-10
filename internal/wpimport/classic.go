package wpimport

import (
	"errors"
	"net/url"
	"slices"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/html"
	"github.com/WhiteRoomLab/whiteroom/internal/wpblock"
	"github.com/WhiteRoomLab/whiteroom/internal/wpformat"
)

// wpAutop is a variable so that the tests can see what it is given.
var wpAutop = wpformat.Autop

const (
	classicHTML    = "the HTML of the classic editor"
	reasonComplex  = classicHTML + " is too complex to read"
	reasonEmbedURL = "not a URL that a provider can embed"
)

func deepError() error { return legacyf("the blocks are nested more than %d deep", content.MaxDepth) }

func reasonOf(err error) string {
	var le *legacyError
	if errors.As(err, &le) {
		return le.reason
	}
	return err.Error()
}

// bodyMark is where the notes, the anchors, the page breaks and the targets stood before an element, to forget what it did if
// it fails.
type bodyMark struct{ notes, anchors, pageBreaks, targets int }

func (c *bodyConv) snapshot() bodyMark {
	return bodyMark{len(c.notes), len(c.anchorLog), c.pageBreaks, len(c.targets)}
}

func (c *bodyConv) restore(m bodyMark) {
	c.forget(m.notes)
	c.pageBreaks = m.pageBreaks
	for _, id := range c.anchorLog[m.anchors:] {
		delete(c.anchors, id)
	}
	c.anchorLog = c.anchorLog[:m.anchors]
	c.targets = c.targets[:min(m.targets, len(c.targets))]
}

// takeTargets returns the targets that were read since len(c.targets) was mark, and forgets them.
func (c *bodyConv) takeTargets(mark int) []string {
	if mark >= len(c.targets) {
		return nil
	}
	taken := slices.Clone(c.targets[mark:])
	c.targets = c.targets[:mark]
	return taken
}

// claimAnchors makes the first of the targets that it can the anchor of blk, which is not a part, if blk has none. What it
// cannot take it notes at blk.
func (c *bodyConv) claimAnchors(blk *content.Block, names []string) {
	at := noteAt{id: blk.ID}
	for _, name := range names {
		switch {
		case name == blk.Anchor:
		case blk.Anchor != "":
			c.note(at, Skipped, "the anchor %q is not imported: the block has the anchor %q", name, blk.Anchor)
		default:
			if err := content.CheckName(name); err != nil {
				c.note(at, Skipped, "the anchor %q is not imported: %s", name, err)
			} else if c.anchors[name] {
				c.note(at, Skipped, "the anchor %q is not imported: another block has it", name)
			} else {
				c.anchors[name] = true
				c.anchorLog = append(c.anchorLog, name)
				blk.Anchor = name
			}
		}
	}
}

// classicSource is HTML to convert as the classic editor wrote it. what names it in the note of a part that is not read: "a part
// of" and what. WordPress makes the paragraphs of the text with wpautop if autop is set, and splits it into pages first if
// pages is set. top is set for the content of the item, as opposed to a copy of a synced pattern: only its page breaks and its
// read more tag count.
type classicSource struct {
	text              string
	what              string
	autop, pages, top bool
}

// classicState is what the conversion of one source keeps from one page and part to the next.
type classicState struct {
	classicSource
	sub int // the blocks made at the top of the source so far
	// name is the name of the elements that stand for the shortcodes and URLs of the source, chosen when the first is found:
	// named says that it was, and name is "" if there is none to choose.
	name  string
	named bool
}

func (c *bodyConv) classicSourceOf(doc string, b *wpblock.Block) classicSource {
	inItem := len(c.patterns) == 0
	switch {
	case b.Name != "":
		return classicSource{text: b.HTML(), what: classicHTML, top: inItem}
	case b.Start != 0 || b.End != len(doc):
		return classicSource{text: doc[b.Start:b.End], what: classicHTML, top: inItem}
	case !inItem:
		return classicSource{text: doc, what: classicHTML}
	case c.autop:
		return classicSource{text: doc, what: classicHTML, autop: true, pages: true, top: true}
	}
	return classicSource{text: doc, what: classicHTML, top: true}
}

// classic converts the HTML that the classic editor wrote: the HTML between blocks, the content without delimiters, and the
// block core/freeform. What it cannot convert becomes a legacy block.
func (c *bodyConv) classic(doc string, b *wpblock.Block, p bodyPlace, at noteAt) ([]content.Block, error) {
	if p.depth > content.MaxDepth {
		return nil, deepError()
	}
	return c.classicContent(c.classicSourceOf(doc, b), p, at), nil
}

// classicContent converts src page by page and part by part, so that the tree of only one part, which is at most
// maxBlockHTMLBytes long, is in memory at a time. wpautop can make the text it works on 16 times as long, so a page is cut
// before it is given to wpautop, and what wpautop makes is cut again before it is read. WordPress runs [embed] and tries to
// embed the URLs of a line of their own before it makes the paragraphs, and the other shortcodes after: elements stand for
// them while the HTML is read.
func (c *bodyConv) classicContent(src classicSource, p bodyPlace, at noteAt) []content.Block {
	st := &classicState{classicSource: src}
	c.classicDepth++
	saved, wasClassic := c.pieceName, c.inClassic
	c.pieceName, c.inClassic = "", true
	defer func() { c.classicDepth--; c.pieceName, c.inClassic = saved, wasClassic }()
	pages := []string{src.text}
	if src.pages {
		pages = wpformat.SplitPages(src.text)
		c.pageBreaks += len(pages) - 1
	}
	var out []content.Block
	for _, page := range pages {
		if c.stopped {
			break
		}
		page = c.embedPieces(st, page)
		for _, part := range wpformat.SplitBlocks(page, maxBlockHTMLBytes) {
			if c.stopped {
				break
			}
			if len(part) > maxBlockHTMLBytes {
				out = append(out, c.classicPart(st, part, p, at)...)
				continue
			}
			if src.autop {
				part = wpAutop(part)
			}
			for _, made := range wpformat.SplitBlocks(c.shortcodePieces(st, part), maxBlockHTMLBytes) {
				if c.stopped {
					break
				}
				out = append(out, c.classicPart(st, made, p, at)...)
			}
		}
	}
	if c.piecesCut && !c.cutNoted {
		c.cutNoted = true
		c.note(noteAt{}, Review, reasonCutPieces, maxWPBlocks)
	}
	if c.targetsCut && !c.targetsNoted {
		c.targetsNoted = true
		c.note(noteAt{}, Skipped, "the anchors after the first %d are not imported", maxWPBlocks)
	}
	return out
}

// classicPart reads and converts part. A part that cannot be read, such as one that could not be cut to the limit, is a legacy
// block of its HTML.
func (c *bodyConv) classicPart(st *classicState, part string, p bodyPlace, at noteAt) []content.Block {
	var root *html.Node
	var err error
	if len(part) > maxBlockHTMLBytes {
		// It does not count among the bytes that the conversion reads, as a block with too much HTML does not.
		err = legacyf("the HTML of the classic editor has more than %d bytes", maxBlockHTMLBytes)
	} else if err = c.readHTML(len(part)); err == nil {
		if root, err = parseFragment(part); err != nil {
			err = legacyf(reasonComplex)
		} else if c.strayPiece(part, root) {
			// The parser took an element that stands for a piece for text, such as that of a textarea, or for part of a tag: the
			// shortcodes and the URLs of the part are text, as they were written.
			c.note(noteAt{}, Review, "the shortcodes and URLs in a part of %s are left as text: the HTML around them is not made of elements", st.what)
			part = c.restorePieces(part)
			if len(part) > maxBlockHTMLBytes {
				err = legacyf("the HTML of the classic editor has more than %d bytes", maxBlockHTMLBytes)
			} else if err = c.readHTML(len(part)); err == nil {
				if root, err = parseFragment(part); err != nil {
					err = legacyf(reasonComplex)
				}
			}
		}
	}
	if err != nil {
		w := &classicWalker{c: c, st: st, p: p, at: at}
		w.add(c.legacyText(part, "a part of "+st.what, reasonOf(err), at), nil, w.origin("", "", true), 0)
		return w.out
	}
	if !st.pages && st.top {
		c.pageBreaks += countComments(root, "nextpage")
	}
	return c.classicFlow(root, p, at, st)
}

func parseFragment(s string) (*html.Node, error) {
	nodes, err := parseIn("div", s)
	if err != nil {
		return nil, err
	}
	root := &html.Node{Type: html.DocumentFragmentNode}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	return root, nil
}

func countComments(root *html.Node, data string) int {
	n := 0
	for d := root.FirstChild; d != nil; d = after(root, d, true) {
		if d.Type == html.CommentNode && d.Data == data {
			n++
		}
	}
	return n
}

// classicKind says what the conversion makes of an element that is a child of a flow.
type classicKind uint8

const (
	kindInline    classicKind = iota // text in a paragraph
	kindParagraph                    // p
	kindHeading                      // h1 to h6
	kindList                         // ul, ol
	kindQuote                        // blockquote
	kindTable                        // table
	kindPre                          // pre
	kindRule                         // hr
	kindGroup                        // div and the like
	kindDetails                      // details
	kindFigure                       // figure
	kindFrame                        // iframe
	kindDrop                         // script and the like, which are left out with what they hold
	kindLegacy                       // elements that have no counterpart
)

var classicKinds = func() map[string]classicKind {
	m := make(map[string]classicKind)
	for kind, names := range map[classicKind][]string{
		kindParagraph: {"p"},
		kindHeading:   {"h1", "h2", "h3", "h4", "h5", "h6"},
		kindList:      {"ul", "ol"},
		kindQuote:     {"blockquote"},
		kindTable:     {"table"},
		kindPre:       {"pre"},
		kindRule:      {"hr"},
		kindGroup:     {"div", "section", "article", "aside", "header", "footer", "main", "nav", "center"},
		kindDetails:   {"details"},
		kindFigure:    {"figure"},
		kindFrame:     {"iframe"},
		kindDrop:      {"script", "style", "template", "noscript", "title", "noembed", "noframes"},
		kindLegacy: {
			"address", "audio", "canvas", "dd", "dialog", "dir", "dl", "dt", "embed", "fieldset", "figcaption", "form", "frameset",
			"hgroup", "legend", "li", "listing", "menu", "object", "plaintext", "search", "summary", "video", "xmp",
		},
	} {
		for _, name := range names {
			m[name] = kind
		}
	}
	return m
}()

func classicKindOf(n *html.Node) classicKind {
	if n.Namespace != html.HTML {
		return kindLegacy
	}
	return classicKinds[n.Name]
}

// classicMark is a read more tag before the block with the index at among the blocks that one element made.
type classicMark struct {
	at   int
	data string
}

// classicWalker converts the children of a node, a flow of blocks. Text and the elements that are part of text wait in run
// until the next element that makes blocks, and become a paragraph.
type classicWalker struct {
	c   *bodyConv
	st  *classicState
	p   bodyPlace
	at  noteAt
	out []content.Block
	run []*html.Node
	// carry are the targets (see bodyConv.targets) of elements that made no block, which the next block of the flow takes.
	carry []string
}

func (c *bodyConv) classicFlow(parent *html.Node, p bodyPlace, at noteAt, st *classicState) []content.Block {
	blocks, left := c.classicFlowOf(parent, p, at, st)
	c.noteLeft(at, left)
	return blocks
}

// classicFlowOf is classicFlow, but it returns the targets that no block of the flow came after (see bodyConv.targets), instead
// of noting that they are not imported.
func (c *bodyConv) classicFlowOf(parent *html.Node, p bodyPlace, at noteAt, st *classicState) (blocks []content.Block, left []string) {
	w := &classicWalker{c: c, st: st, p: p, at: at}
	for n := parent.FirstChild; n != nil && !c.stopped; {
		next := n.NextSibling
		w.node(n)
		n = next
	}
	if c.stopped {
		return w.out, nil
	}
	w.flushRun()
	return w.out, w.carry
}

func (c *bodyConv) noteLeft(at noteAt, targets []string) {
	for _, name := range targets {
		c.note(at, Skipped, "the anchor %q is not imported: no block comes after it", name)
	}
}

// takeCarry gives the block blk, which comes next in the flow, the targets that wait.
func (w *classicWalker) takeCarry(blk *content.Block) {
	if len(w.carry) > 0 {
		w.c.claimAnchors(blk, w.carry)
		w.carry = nil
	}
}

func (w *classicWalker) node(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		w.run = append(w.run, n)
	case html.CommentNode:
		if n.Data == "nextpage" {
			// What comes before a page break is not a paragraph with what comes after it.
			w.flushRun()
			return
		}
		w.run = append(w.run, n)
	case html.ElementNode:
		kind := classicKindOf(n)
		if kind == kindInline {
			w.run = append(w.run, n)
			return
		}
		w.flushRun()
		w.element(n, kind)
	case html.DocumentNode, html.DocumentFragmentNode, html.DoctypeNode, html.ProcessingInstructionNode:
	}
}

func (w *classicWalker) element(n *html.Node, kind classicKind) {
	c := w.c
	switch kind {
	case kindInline:
		w.run = append(w.run, n)
	case kindParagraph:
		w.paragraph(n)
	case kindHeading:
		s := classicProps("core/heading", n, true, nil)
		w.leaf(n, s.lost, false, func() ([]content.Block, error) {
			blocks, err := c.headingFrom(s.block, n, w.at)
			if err == nil && len(blocks) == 1 {
				// wpautop leaves a line break before the end tag of a heading written over several lines; it shows nothing.
				blocks[0].Text = withoutTrailingBreaks(blocks[0].Text)
			}
			return blocks, err
		})
	case kindList:
		s := classicProps("core/list", n, false, nil)
		w.leaf(n, slices.Concat(s.lost, listLost(n)), true, func() ([]content.Block, error) {
			list, err := c.listOf("", s.block, n, w.p.depth, w.at)
			if err != nil || list == nil {
				return nil, err
			}
			return []content.Block{*list}, nil
		})
	case kindQuote:
		w.quote(n)
	case kindTable:
		s := classicProps("core/table", n, false, nil)
		w.leaf(n, append(s.lost, tableLost(n)...), true, func() ([]content.Block, error) { return c.tableWithCaption(s.block, n, w.at) })
	case kindPre:
		s := classicProps("core/code", n, false, func(class string) bool { return strings.HasPrefix(class, "language-") })
		w.leaf(n, s.lost, false, func() ([]content.Block, error) {
			if names := c.shortcodeNames(n); len(names) > 0 {
				return nil, legacyf("%s", reasonShortcodes(names))
			}
			blocks := c.codeFrom(s.block, n)
			if blk := &blocks[0]; blk.Attrs.Language == "" && isElement(onlyChild(n), "code") {
				// The code element has no language; the pre may have it.
				blk.Attrs.Language = c.codeLanguage(n, noteAt{id: blk.ID})
			}
			return blocks, nil
		})
	case kindRule:
		s := classicProps("core/separator", n, false, nil)
		w.leaf(n, s.lost, false, func() ([]content.Block, error) {
			blk := c.newBlock(content.BlockSeparator)
			c.applyProps(&blk, s.block, n, noteAt{id: blk.ID})
			return []content.Block{blk}, nil
		})
	case kindGroup:
		w.group(n)
	case kindDetails:
		w.details(n)
	case kindFigure:
		w.figure(n)
	case kindFrame:
		w.leaf(n, nil, false, func() ([]content.Block, error) { return c.frameBlocks(n, n, w.at), nil })
	case kindDrop:
		c.noteItems(w.at, Skipped, "removed ", " with their content", tagText(n.Name))
	case kindLegacy:
		w.add(c.classicLegacy(n, "the element "+tagText(n.Name)+" is not converted", w.at), nil, w.origin("", "", true), 0)
	}
}

// withoutTrailingBreaks returns text without the line breaks at its end, unless nothing else is left.
func withoutTrailingBreaks(text content.RichText) content.RichText {
	end := len(text)
	for end > 0 && text[end-1].Type == content.InlineHardBreak {
		end--
	}
	if end == 0 {
		return text
	}
	return text[:end]
}

// origin returns the origin of the blocks of one conversion. markup and what are for the legacy block that the guard makes of
// them; guard is set for blocks that it takes out instead.
func (w *classicWalker) origin(markup, what string, guard bool) origin {
	o := w.c.newOrigin(nil, "", w.p.top)
	o.html, o.what, o.guard = markup, what, guard
	return o
}

// add puts the blocks of one conversion, whose origin is o, in the flow, and the read more tags of marks among them. Each
// block counts among those converted, except the first admitted, which are counted. Once the conversion has stopped, the
// blocks being made are kept: the legacy block of an element that went over the limit of the HTML is one.
func (w *classicWalker) add(blocks []content.Block, marks []classicMark, o origin, admitted int) {
	c := w.c
	for i := 0; ; i++ {
		for len(marks) > 0 && marks[0].at <= i {
			c.classicMore(w.st, w.p, w.at, marks[0].data)
			marks = marks[1:]
		}
		if i == len(blocks) || i >= admitted && !c.stopped && !c.admit(w.at) {
			return
		}
		bo := o
		if w.p.depth == 1 {
			w.st.sub++
			bo.sub = w.st.sub
		}
		c.origins[blocks[i].ID] = bo
		w.takeCarry(&blocks[i])
		w.out = append(w.out, blocks[i])
	}
}

// classicMore handles a read more tag, whose comment has the data, before the next block of the flow.
func (c *bodyConv) classicMore(st *classicState, p bodyPlace, at noteAt, data string) {
	switch {
	case !st.top:
		// WordPress splits the teaser on the content of the post only: in a synced pattern the tag is a comment.
	case p.depth != 1:
		c.note(at, Skipped, "the more tag is removed: it is not at the top of the content")
	case c.moreTop < 0:
		c.moreTop, c.moreSub = p.top, st.sub+1
		if text := moreText(data); text != "" {
			c.note(noteAt{}, Skipped, "the text %q of the more link is not imported", text)
		}
		if strings.Contains(c.opts.Item.Content, "<!--noteaser-->") {
			c.note(noteAt{}, Changed, "the more tag hid the text before it on the page (<!--noteaser-->); whiteroom shows it")
		}
	}
}

// moreText returns what follows more in the data of the comment of a read more tag, without tags and the white space around.
func moreText(data string) string {
	rest := strings.TrimPrefix(data, "more")
	var b strings.Builder
	for {
		open := strings.IndexByte(rest, '<')
		if open < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:open])
		end := strings.IndexByte(rest[open:], '>')
		if end < 0 {
			break
		}
		rest = rest[open+end+1:]
	}
	return strings.TrimSpace(b.String())
}

// isMoreComment reports whether n is the comment of a read more tag: its data starts with more and has no line feed.
func isMoreComment(n *html.Node) bool {
	return n.Type == html.CommentNode && strings.HasPrefix(n.Data, "more") && !strings.Contains(n.Data, "\n")
}

// isBlankNode reports whether n is white space or a comment that is not a read more tag.
func isBlankNode(n *html.Node) bool {
	return n.Type == html.TextNode && blank(n.Data) || n.Type == html.CommentNode && !isMoreComment(n)
}

func trimBlank(nodes []*html.Node) []*html.Node {
	for len(nodes) > 0 && isBlankNode(nodes[0]) {
		nodes = nodes[1:]
	}
	for len(nodes) > 0 && isBlankNode(nodes[len(nodes)-1]) {
		nodes = nodes[:len(nodes)-1]
	}
	return nodes
}

// isSpacingNode is isBlankNode, and a text of no-break spaces too: the classic editor puts one next to an image.
func isSpacingNode(n *html.Node) bool {
	return isBlankNode(n) || n.Type == html.TextNode && trimSpaces(n.Data) == ""
}

// afterUnit returns the nodes that follow a unit, an image, without the spacing next to it, and without the blank nodes at their
// other end.
func afterUnit(nodes []*html.Node) []*html.Node {
	for len(nodes) > 0 && isSpacingNode(nodes[0]) {
		nodes = nodes[1:]
	}
	return trimBlank(nodes)
}

// beforeUnit returns the nodes that come before a unit, an image, without the spacing next to it, and without the blank nodes
// at their other end.
func beforeUnit(nodes []*html.Node) []*html.Node {
	for len(nodes) > 0 && isSpacingNode(nodes[len(nodes)-1]) {
		nodes = nodes[:len(nodes)-1]
	}
	return trimBlank(nodes)
}

// flushRun makes a paragraph of the text that waits, if there is more than white space.
func (w *classicWalker) flushRun() {
	run := w.run
	w.run = nil
	if !slices.ContainsFunc(run, func(n *html.Node) bool { return !isBlankNode(n) }) {
		return
	}
	p := &html.Node{Type: html.ElementNode, Namespace: html.HTML, Name: "p"}
	for _, n := range run {
		n.Parent.RemoveChild(n)
		p.AppendChild(n)
	}
	w.paragraph(p)
}

// leaf makes the blocks of the element n, which holds no blocks of its own, with conv. If conv fails, or makes blocks that go
// too deep, the element is a legacy block and what conv noted is forgotten. lost are the settings of n that whiteroom has no
// counterpart of. parts is set for an element whose blocks have parts that count: it counts first.
func (w *classicWalker) leaf(n *html.Node, lost []string, parts bool, conv func() ([]content.Block, error)) {
	c := w.c
	admitted := 0
	if parts {
		if !c.admit(w.at) {
			return
		}
		admitted = 1
	}
	text := html.Render(n)
	m := c.snapshot()
	blocks, err := conv()
	if err == nil && deeper(blocks, content.MaxDepth-w.p.depth+1) {
		err = deepError()
	}
	if err != nil {
		c.restore(m)
		w.add(c.classicLegacy(n, reasonOf(err), w.at), nil, w.origin("", "", true), admitted)
		return
	}
	// The targets in the element are the anchor of its block, or wait for the next block of the flow if it made none.
	targets, at := c.takeTargets(m.targets), w.at
	if len(blocks) == 0 {
		w.carry = append(w.carry, targets...)
		// An element that makes no block counts too, as a body can have millions of them, each with its note.
		if !parts && !c.admit(at) {
			return
		}
	} else {
		at = noteAt{id: blocks[0].ID}
		c.claimAnchors(&blocks[0], targets)
		c.lostOf(at, n, lost)
	}
	w.noteMoreIn(n, true, at)
	w.add(blocks, nil, w.origin(text, "the element "+tagText(n.Name), false), admitted)
}

// hasMoreTag reports whether a read more tag is below n; the tags that are children of n count if direct is set.
func hasMoreTag(n *html.Node, direct bool) bool {
	for d := n.FirstChild; d != nil; d = after(n, d, true) {
		if isMoreComment(d) && (direct || d.Parent != n) {
			return true
		}
	}
	return false
}

// noteMoreIn notes at at that the read more tag in the element n, other than a child of n if direct is not set, is removed:
// only in the content of the item is a tag one.
func (w *classicWalker) noteMoreIn(n *html.Node, direct bool, at noteAt) {
	if w.st.top && hasMoreTag(n, direct) {
		w.c.note(at, Skipped, "the more tag is removed: it is not at the top of the content")
	}
}

// addContainer puts in the flow the block of an element that holds blocks, which was counted when it was begun. The guard
// takes it out with what it holds if it does not pass validation.
func (w *classicWalker) addContainer(blk content.Block) {
	o := w.origin("", "", true)
	if w.p.depth == 1 {
		w.st.sub++
		o.sub = w.st.sub
	}
	w.c.origins[blk.ID] = o
	w.takeCarry(&blk)
	w.out = append(w.out, blk)
}

// enter counts the element n, which holds blocks, and reports whether they can be one level deeper. If not, n is a legacy block.
func (w *classicWalker) enter(n *html.Node) bool {
	if w.p.depth >= content.MaxDepth {
		w.add(w.c.classicLegacy(n, reasonOf(deepError()), w.at), nil, w.origin("", "", true), 0)
		return false
	}
	return w.c.admit(w.at)
}

// inner is the place of the blocks of the block g, which holds blocks, and where to note what is left out of them.
func (w *classicWalker) inner(g content.Block) (bodyPlace, noteAt) {
	return bodyPlace{depth: w.p.depth + 1, inColumns: w.p.inColumns, top: w.p.top}, noteAt{id: g.ID, sub: "/content"}
}

// withName returns a copy of b whose attributes have the member name with the string value.
func withName(b *wpblock.Block, name, value string) *wpblock.Block {
	cp := *b
	cp.Attrs = &wpblock.Value{Kind: wpblock.Object, Object: slices.Clone(b.Attrs.Object)}
	cp.Attrs.Object = append(cp.Attrs.Object, wpblock.Member{Name: name, Value: wpblock.Value{Kind: wpblock.String, String: value}})
	return &cp
}

func (w *classicWalker) group(n *html.Node) {
	c := w.c
	if !w.enter(n) {
		return
	}
	s := classicProps("core/group", n, false, nil)
	g := c.newBlock(content.BlockGroup)
	own := noteAt{id: g.ID}
	g.Attrs.TagName = c.groupTag(withName(s.block, "tagName", n.Name), own)
	c.applyProps(&g, s.block, n, own)
	place, at := w.inner(g)
	var left []string
	g.Children, left = c.classicFlowOf(n, place, at, w.st)
	if len(g.Children) == 0 {
		// The places that waited in a group that is gone wait for the next block of the flow that held it.
		c.giveBackAnchor(&g)
		w.carry = append(w.carry, left...)
		if !c.stopped {
			c.note(w.at, Skipped, "the empty group is removed")
		}
		return
	}
	c.noteLeft(at, left)
	c.lostOf(own, n, s.lost)
	w.addContainer(g)
}

func (w *classicWalker) quote(n *html.Node) {
	c := w.c
	if !w.enter(n) {
		return
	}
	m := c.snapshot()
	s := classicProps("core/quote", n, false, nil)
	blk := c.newBlock(content.BlockQuote)
	own := noteAt{id: blk.ID}
	c.applyProps(&blk, s.block, n, own)
	cite := childNamed(n, "cite")
	// wpautop puts a paragraph around a cite that comes last.
	holder := (*html.Node)(nil)
	if cite == nil {
		if holder = citeParagraph(n); holder != nil {
			cite = onlyChild(holder)
		}
	}
	if cite != nil {
		text, err := c.richText(cite, false, own)
		if err != nil {
			c.restore(m)
			w.add(c.classicLegacy(n, reasonOf(err), w.at), nil, w.origin("", "", true), 1)
			return
		}
		blk.Attrs.Citation = text
		if holder != nil {
			n.RemoveChild(holder)
		} else {
			n.RemoveChild(cite)
		}
		c.claimAnchors(&blk, c.takeTargets(m.targets))
	}
	place, at := w.inner(blk)
	blk.Children = c.classicFlow(n, place, at, w.st)
	if len(blk.Children) == 0 {
		if c.stopped {
			c.giveBackAnchor(&blk)
			return
		}
		blk.Children = content.Blocks{c.newBlock(content.BlockParagraph)}
	}
	c.lostOf(own, n, s.lost)
	w.addContainer(blk)
}

// citeParagraph returns the last child of the blockquote n that is not white space or a comment, if it is a p element that
// holds a cite element and nothing else but white space.
func citeParagraph(n *html.Node) *html.Node {
	for ch := n.LastChild; ch != nil; ch = ch.PrevSibling {
		if isBlankNode(ch) {
			continue
		}
		if isElement(ch, "p") && isElement(onlyChild(ch), "cite") {
			return ch
		}
		return nil
	}
	return nil
}

func (w *classicWalker) details(n *html.Node) {
	c := w.c
	if !w.enter(n) {
		return
	}
	m := c.snapshot()
	s := classicProps("core/details", n, false, nil)
	g := c.newBlock(content.BlockGroup)
	own := noteAt{id: g.ID}
	c.applyProps(&g, s.block, n, own)
	if summary := childNamed(n, "summary"); summary != nil {
		label, err := c.plainText(summary, own)
		if err != nil {
			c.restore(m)
			w.add(c.classicLegacy(n, reasonOf(err), w.at), nil, w.origin("", "", true), 1)
			return
		}
		g.Attrs.Label = label
		n.RemoveChild(summary)
	}
	place, at := w.inner(g)
	g.Children = c.classicFlow(n, place, at, w.st)
	if len(g.Children) == 0 {
		if c.stopped {
			c.giveBackAnchor(&g)
			return
		}
		g.Children = content.Blocks{c.newBlock(content.BlockParagraph)}
	}
	c.note(own, Changed, "the details block is a group here, always open; whiteroom has no details blocks yet (stage 2)")
	c.lostOf(own, n, s.lost)
	w.addContainer(g)
}

// figureParts are what a figure holds that a block is made of.
type figureParts struct {
	table, img, link, caption *html.Node
}

// figureOf reads the children of the figure n. A figure that is made into a block holds one table, or one image, which may be
// the content of the a element that links it, and which wpautop may have put in a p element; it may hold a figcaption, and
// white space, line breaks (wpautop puts one at the end of a line), empty paragraphs and comments. ok is unset if it holds
// anything else, which the block would lose, or if it has two captions, a figcaption and one of the table.
func figureOf(n *html.Node) (f figureParts, ok bool) {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		switch {
		case ch.Type == html.CommentNode, ch.Type == html.TextNode && blank(ch.Data), isElement(ch, "br"), isElement(ch, "p") && blankChildren(ch):
		case isElement(ch, "figcaption") && f.caption == nil:
			f.caption = ch
		case isElement(ch, "table") && f.table == nil && f.img == nil:
			f.table = ch
		case f.table == nil && f.img == nil:
			unit := ch
			if isElement(ch, "p") {
				unit = nil
				for g := ch.FirstChild; g != nil; g = g.NextSibling {
					switch {
					case g.Type == html.CommentNode, g.Type == html.TextNode && blank(g.Data), isElement(g, "br"):
					case unit == nil:
						unit = g
					default:
						return f, false
					}
				}
			}
			if f.img, f.link = imageUnit(unit); f.img == nil {
				return f, false
			}
		default:
			return f, false
		}
	}
	if f.table != nil && f.caption != nil && childNamed(f.table, "caption") != nil {
		return f, false
	}
	return f, f.table != nil || f.img != nil
}

// figure converts a figure that holds one table, or one image, with the figcaption as its caption. Any other is a legacy block.
func (w *classicWalker) figure(n *html.Node) {
	c := w.c
	f, ok := figureOf(n)
	switch {
	case ok && f.table != nil:
		s := classicProps("core/table", n, false, nil)
		w.leaf(n, slices.Concat(s.lost, tableLost(f.table)), true, func() ([]content.Block, error) { return c.tableWithCaption(s.block, n, w.at) })
	case ok:
		_, lost := lostSettings(n, false)
		w.leaf(n, lost, false, func() ([]content.Block, error) {
			return c.classicImage(classicImageInput{
				img: f.img, link: f.link, caption: f.caption, legacyOf: n, anchor: n, outer: n,
			}, w.at), nil
		})
	default:
		w.add(c.classicLegacy(n, "the element "+tagText(n.Name)+" is not converted", w.at), nil, w.origin("", "", true), 0)
	}
}

// tableWithCaption is tableFrom, and a table with a caption element has it as its caption, if the figure that holds it has no
// figcaption.
func (c *bodyConv) tableWithCaption(b *wpblock.Block, root *html.Node, at noteAt) ([]content.Block, error) {
	blocks, err := c.tableFrom(b, root, at)
	if err != nil || len(blocks) != 1 || blocks[0].Type != content.BlockTable || len(blocks[0].Attrs.Caption) > 0 {
		return blocks, err
	}
	table := root
	if root.Name == "figure" {
		table = childNamed(root, "table")
	}
	if caption := childNamed(table, "caption"); caption != nil {
		text, err := c.richText(caption, false, noteAt{id: blocks[0].ID})
		if err != nil {
			return nil, err
		}
		if !emptyText(text) {
			blocks[0].Attrs.Caption = text
		}
	}
	return blocks, nil
}

// frameBlocks converts the iframe element frame: an embed block, or a legacy block if no provider can embed its address.
// anchor is the element whose id is the anchor of the block, or nil.
func (c *bodyConv) frameBlocks(frame, anchor *html.Node, at noteAt) []content.Block {
	provider, src, why := frameEmbed(frame, true)
	if provider == "" {
		return c.classicLegacy(frame, "the iframe cannot be embedded: "+why, at)
	}
	s := classicProps("core/embed", frame, false, nil)
	blk := c.newBlock(content.BlockEmbed)
	own := noteAt{id: blk.ID}
	blk.Attrs.Provider, blk.Attrs.URL = provider, src
	c.note(own, Changed, "the iframe is an embed block here")
	c.applyProps(&blk, s.block, anchor, own)
	return []content.Block{blk}
}

// frameEmbed returns the provider that embeds the iframe element and the address; if there is none, why says so. WordPress
// embeds the http address of a provider too: with upgrade, it is looked for, and returned, as an https address.
func frameEmbed(frame *html.Node, upgrade bool) (provider, src, why string) {
	src = content.NormalizeHref(attr(frame, "src"))
	if strings.HasPrefix(src, "//") {
		// An address without a scheme takes that of the page, which is https on the site.
		src = "https:" + src
	}
	if err := content.CheckHref(src); err != nil {
		return "", "", err.Error()
	}
	if upgrade {
		src = httpsOf(src)
	}
	for _, name := range embedProviders {
		if _, err := content.ParseEmbedURL(name, src); err == nil {
			return name, src, ""
		}
	}
	return "", "", reasonEmbedURL
}

// httpsOf returns href with the scheme http, in any case, made https.
func httpsOf(href string) string {
	if hasPrefixFold(href, "http://") {
		return "https://" + href[len("http://"):]
	}
	return href
}

func (c *bodyConv) classicLegacy(n *html.Node, reason string, at noteAt) []content.Block {
	return c.legacyText(c.renderClassic(n), "the element "+tagText(n.Name), reason, at)
}

// urlAttributes are the attributes that renderClassic makes absolute, by element.
var urlAttributes = map[string]string{"a": "href", "img": "src", "blockquote": "cite", "q": "cite", "del": "cite", "ins": "cite"}

// renderClassic returns the HTML of n for a legacy block, which is not shown at the address of the item: the addresses that
// are relative to it, or take its scheme, become absolute. The base counts among the HTML read (see resolve); past the limit,
// the addresses that are left stay as they are.
func (c *bodyConv) renderClassic(n *html.Node) string {
	for d := n; d != nil; d = after(n, d, true) {
		name := urlAttributes[d.Name]
		if d.Type != html.ElementNode || d.Namespace != html.HTML || name == "" {
			continue
		}
		for i := range d.Attrs {
			a := &d.Attrs[i]
			if a.Namespace != "" || a.Name != name {
				continue
			}
			v := content.NormalizeHref(a.Value)
			if v == "" || strings.HasPrefix(v, "#") || strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "//") {
				continue
			}
			ref, err := url.Parse(v)
			if err != nil || ref.Scheme != "" {
				continue
			}
			abs, err := c.resolve(ref)
			if err != nil {
				return html.Render(n)
			}
			a.Value = abs.String()
		}
	}
	return html.Render(n)
}

const lostTail = "; whiteroom has no such settings"

// lostOf notes the settings items of the element n that whiteroom has no counterpart of, at the block made of it.
func (c *bodyConv) lostOf(at noteAt, n *html.Node, items []string) {
	if len(items) == 0 {
		return
	}
	keys := make([]string, len(items))
	for i, item := range items {
		keys[i] = keyText(item)
	}
	c.noteItems(at, Skipped, "lost ", " of "+tagText(n.Name)+lostTail, keys...)
}

// visualAttrs are the attributes that set how an element looks, which HTML before 5 has.
var visualAttrs = map[string]bool{
	"align": true, "background": true, "bgcolor": true, "border": true, "cellpadding": true, "cellspacing": true, "color": true,
	"face": true, "height": true, "hspace": true, "size": true, "valign": true, "vspace": true, "width": true,
}

// classicSettings are the settings that an element of the classic editor gives its block.
type classicSettings struct {
	// block is a block of WordPress that has them as attributes: className has the classes that WordPress does not give, and
	// textAlign the alignment of the text.
	block *wpblock.Block
	align string   // the alignment of the text, if the element sets one that whiteroom has
	lost  []string // the settings that whiteroom has no counterpart of
}

func isAlignment(v string) bool { return v == "left" || v == "center" || v == "right" }

// wordPressClass reports whether class is one that WordPress gives an element of the classic editor.
func wordPressClass(class string) bool {
	switch class {
	case "alignleft", "aligncenter", "alignright", "alignnone":
		return true
	}
	return strings.HasPrefix(class, "size-") || strings.HasPrefix(class, "wp-") || strings.HasPrefix(class, "attachment-")
}

// styleNames returns the properties that the attribute style s declares, in lower case, each once, in order of appearance.
func styleNames(s string) []string {
	var names []string
	seen := make(map[string]bool)
	for decl := range strings.SplitSeq(s, ";") {
		name, _, ok := strings.Cut(decl, ":")
		if name = strings.ToLower(strings.TrimSpace(name)); ok && name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

// classNames returns the classes of n that are not those of WordPress, and that skip, if it is not nil, does not leave out.
func classNames(n *html.Node, skip func(string) bool) []string {
	var classes []string
	for _, class := range classList(n) {
		if !wordPressClass(class) && (skip == nil || !skip(class)) {
			classes = append(classes, class)
		}
	}
	return classes
}

// classicProps reads the classes, the style and the visual attributes of the element n, as the block of WordPress called name
// has them. text is set for an element with text: its text-align or align is the alignment of the text. skip leaves out
// classes, and quiet attributes that are dropped without a word.
func classicProps(name string, n *html.Node, text bool, skip func(string) bool, quiet ...string) classicSettings {
	attrs := &wpblock.Value{Kind: wpblock.Object}
	if classes := classNames(n, skip); len(classes) > 0 {
		attrs.Object = append(attrs.Object, wpblock.Member{
			Name: "className", Value: wpblock.Value{Kind: wpblock.String, String: strings.Join(classes, " ")},
		})
	}
	s := classicSettings{block: &wpblock.Block{Name: name, Attrs: attrs}}
	s.align, s.lost = lostSettings(n, text, quiet...)
	if s.align != "" {
		attrs.Object = append(attrs.Object, wpblock.Member{Name: "textAlign", Value: wpblock.Value{Kind: wpblock.String, String: s.align}})
	}
	return s
}

// lostSettings returns the alignment of the text of n that whiteroom has, if text is set, and the properties of its style and
// its visual attributes that it has not.
func lostSettings(n *html.Node, text bool, quiet ...string) (align string, lost []string) {
	style := attr(n, "style")
	var decls map[string]string
	if style != "" {
		decls = styleDecls(style)
	}
	alignAttr := strings.ToLower(strings.TrimSpace(attr(n, "align")))
	usedStyle, usedAttr := false, false
	if text {
		switch styleAlign := declValue(decls, "text-align"); {
		case isAlignment(styleAlign):
			align, usedStyle = styleAlign, true
		case styleAlign == "" && isAlignment(alignAttr):
			align, usedAttr = alignAttr, true
		}
	}
	for _, name := range styleNames(style) {
		if name != "text-align" || !usedStyle {
			lost = append(lost, "style."+name)
		}
	}
	for _, a := range n.Attrs {
		if a.Namespace == "" && visualAttrs[a.Name] && !slices.Contains(quiet, a.Name) && (a.Name != "align" || !usedAttr) {
			lost = append(lost, a.Name)
		}
	}
	return align, lost
}

// partLost returns the settings of the part n of a list or a table, an li, tr, td or th element, that whiteroom has no
// counterpart of: the parts have no class and no anchor either. text is set if the part has the alignment of the text.
func partLost(n *html.Node, text bool) []string {
	_, lost := lostSettings(n, text)
	// The class that a cell has for its alignment is read (see cellAlign).
	if slices.ContainsFunc(classList(n), func(class string) bool {
		return !wordPressClass(class) && (!text || !strings.HasPrefix(class, "has-text-align-"))
	}) {
		lost = append(lost, "class")
	}
	if attr(n, "id") != "" {
		lost = append(lost, "id")
	}
	return lost
}

// listLost returns the settings of the items of list, at any depth, that whiteroom has no counterpart of.
func listLost(list *html.Node) []string {
	var set orderedSet
	for d := list.FirstChild; d != nil; d = after(list, d, true) {
		if isElement(d, "li") {
			for _, item := range partLost(d, false) {
				set.add(item)
			}
		}
	}
	return set.items
}

// tableLost returns the settings of the sections, rows and cells of table that whiteroom has no counterpart of.
func tableLost(table *html.Node) []string {
	var set orderedSet
	scan := func(n *html.Node, part, text bool) {
		var lost []string
		if part {
			lost = partLost(n, text)
		} else {
			_, lost = lostSettings(n, text)
		}
		for _, item := range lost {
			set.add(item)
		}
	}
	for _, section := range childElements(table) {
		scan(section, false, false)
		for _, row := range childElements(section) {
			if !isElement(row, "tr") {
				continue
			}
			scan(row, true, false)
			for _, cell := range childElements(row) {
				if isElement(cell, "td", "th") {
					scan(cell, true, true)
				}
			}
		}
	}
	return set.items
}

// richRange reads the children of pn from first to last as rich text. They are out of pn while they are read, and put back, so
// that a legacy block can be made of pn if the reading fails.
func (c *bodyConv) richRange(pn, first, last *html.Node, at noteAt) (content.RichText, error) {
	if first == pn.FirstChild && last == pn.LastChild {
		return c.richText(pn, false, at)
	}
	holder := &html.Node{Type: html.ElementNode, Namespace: html.HTML, Name: "p"}
	rest := last.NextSibling
	for n := first; n != nil; {
		next := n.NextSibling
		pn.RemoveChild(n)
		holder.AppendChild(n)
		if n == last {
			break
		}
		n = next
	}
	text, err := c.richText(holder, false, at)
	for n := holder.FirstChild; n != nil; {
		next := n.NextSibling
		holder.RemoveChild(n)
		pn.InsertBefore(n, rest)
		n = next
	}
	return text, err
}

type itemKind uint8

const (
	itemText  itemKind = iota // lines of text
	itemImage                 // an image
	itemFrame                 // an iframe
	itemPiece                 // a shortcode or a URL that stands alone
	itemMore                  // a read more tag
)

// paraItem is a part of a paragraph.
type paraItem struct {
	kind itemKind
	// first and last are the children of the p that make the lines of text, with the br elements between them.
	first, last *html.Node
	// unit is the img element or the a element around it, the iframe element, or the element of a piece; img and link are the
	// elements of an image.
	unit, img, link *html.Node
	data            string // the data of the comment of a read more tag
}

// imageUnit returns the img element that n is, or that the a element n holds alone, and the a element.
func imageUnit(n *html.Node) (img, link *html.Node) {
	switch {
	case isElement(n, "img"):
		return n, nil
	case isElement(n, "a") && isElement(onlyChild(n), "img"):
		return onlyChild(n), n
	}
	return nil, nil
}

// paragraphItems splits the p element pn into parts. Its lines are the children between br elements. A line that is an image,
// or starts or ends with images, is those and the rest of the line; a line that is an iframe, a shortcode, a URL that a provider
// embeds or a read more tag is that. Lines of text next to each other are one part. A read more tag in a line of text comes
// after the part: the text counts before it.
func (c *bodyConv) paragraphItems(pn *html.Node) []paraItem {
	var items []paraItem
	var first, last *html.Node
	flush := func() {
		if first == nil {
			return
		}
		items = append(items, paraItem{kind: itemText, first: first, last: last})
		for n := first; ; n = n.NextSibling {
			if isMoreComment(n) {
				items = append(items, paraItem{kind: itemMore, data: n.Data})
			}
			if n == last {
				break
			}
		}
		first, last = nil, nil
	}
	image := func(n *html.Node) {
		img, link := imageUnit(n)
		items = append(items, paraItem{kind: itemImage, unit: n, img: img, link: link})
	}
	isUnit := func(n *html.Node) bool { img, _ := imageUnit(n); return img != nil }
	// standalone reports whether n is the element of a piece that is converted by itself: a URL that no provider embeds is text.
	standalone := func(n *html.Node) bool {
		pc := c.pieceOf(n)
		return pc != nil && pc.kind != pieceTextURL
	}
	var line []*html.Node
	for ch := pn.FirstChild; ; ch = ch.NextSibling {
		if ch != nil && !isElement(ch, "br") {
			line = append(line, ch)
			continue
		}
		core := trimBlank(line)
		line = line[:0]
		var lead, trail []*html.Node
		for len(core) > 0 && isUnit(core[0]) {
			lead = append(lead, core[0])
			core = afterUnit(core[1:])
		}
		for len(core) > 0 && isUnit(core[len(core)-1]) {
			trail = append(trail, core[len(core)-1])
			core = beforeUnit(core[:len(core)-1])
		}
		if len(lead) > 0 {
			flush()
			for _, n := range lead {
				image(n)
			}
		}
		switch {
		case len(lead) == 0 && len(trail) == 0 && len(core) == 1 && isElement(core[0], "iframe"):
			flush()
			items = append(items, paraItem{kind: itemFrame, unit: core[0]})
		case len(lead) == 0 && len(trail) == 0 && len(core) == 1 && standalone(core[0]):
			flush()
			items = append(items, paraItem{kind: itemPiece, unit: core[0]})
		case slices.ContainsFunc(core, func(n *html.Node) bool { return !isBlankNode(n) && !isMoreComment(n) }):
			if first == nil {
				first = core[0]
			}
			last = core[len(core)-1]
		case len(core) > 0:
			// Read more tags and white space only.
			flush()
			for _, n := range core {
				if isMoreComment(n) {
					items = append(items, paraItem{kind: itemMore, data: n.Data})
				}
			}
		}
		if len(trail) > 0 {
			flush()
			for _, n := range slices.Backward(trail) {
				image(n)
			}
		}
		if ch == nil {
			break
		}
	}
	flush()
	return items
}

// paragraph converts the p element pn, or the text that waited in a flow and was made one.
func (w *classicWalker) paragraph(pn *html.Node) {
	c := w.c
	if pn.FirstChild == nil {
		// wpautop leaves an end tag behind where it makes paragraphs in an element, and the parser makes an empty p of it.
		return
	}
	items := c.paragraphItems(pn)
	if len(items) == 0 {
		return
	}
	// The text of the blocks that are not those of a shortcode or a URL: those have the text of their own.
	var pieces []*html.Node
	for _, it := range items {
		if it.kind == itemPiece {
			pieces = append(pieces, it.unit)
		}
	}
	text := renderWithout(pn, pieces)
	m := c.snapshot()
	units, err := c.paragraphBlocks(pn, items, w.at)
	if err != nil {
		c.restore(m)
		legacy := c.classicLegacy(pn, reasonOf(err), w.at)
		var marks []classicMark
		for _, it := range items {
			if it.kind == itemMore {
				marks = append(marks, classicMark{at: len(legacy), data: it.data})
			}
		}
		w.add(legacy, marks, w.origin("", "", true), 0)
		return
	}
	o := w.origin(text, "the element "+tagText(pn.Name), false)
	stopped := c.stopped
	var first []content.Block
	for _, u := range units {
		uo := o
		if u.piece != nil {
			// The blocks of a shortcode or a URL go together, apart from the rest of the paragraph.
			uo = w.origin(u.piece.text, u.piece.what(), false)
		}
		w.carry = append(w.carry, u.carry...)
		w.add(u.blocks, u.marks, uo, u.admitted)
		if len(first) == 0 {
			first = u.blocks
		}
		if !stopped && c.stopped {
			return
		}
	}
	// The read more tags that are children of the paragraph were handled as parts of it; the ones in its elements are not.
	at := w.at
	if len(first) > 0 {
		at = noteAt{id: first[0].ID}
	}
	w.noteMoreIn(pn, false, at)
}

// renderWithout returns the HTML of n without its children skip, which are put back.
func renderWithout(n *html.Node, skip []*html.Node) string {
	nexts := make([]*html.Node, len(skip))
	for i, ch := range skip {
		nexts[i] = ch.NextSibling
		n.RemoveChild(ch)
	}
	s := html.Render(n)
	for i := len(skip) - 1; i >= 0; i-- {
		n.InsertBefore(skip[i], nexts[i])
	}
	return s
}

// paraUnit is the blocks that one part of a paragraph made: admitted of them are counted already, and piece is the shortcode or
// URL that they come from, if they do. carry are the targets (see bodyConv.targets) of a part that made no block.
type paraUnit struct {
	blocks   []content.Block
	marks    []classicMark
	admitted int
	piece    *classicPiece
	carry    []string
}

// paragraphBlocks makes the blocks of the parts of the paragraph pn: a paragraph for each text that is not empty, the images,
// the embeds, and what the shortcodes and the URLs that stand alone make. The error is a legacy error, for a text that cannot be
// rich text, such as one with an image in the middle.
func (c *bodyConv) paragraphBlocks(pn *html.Node, items []paraItem, at noteAt) ([]paraUnit, error) {
	s := classicProps("core/paragraph", pn, true, nil)
	anchor := pn // until a block has its id
	var units []paraUnit
	firstID := ""
	alignUsed := false
	for _, it := range items {
		var made []content.Block
		var unit paraUnit
		mark := len(c.targets)
		switch it.kind {
		case itemMore:
			units = append(units, paraUnit{marks: []classicMark{{at: 0, data: it.data}}})
			continue
		case itemText:
			blk := c.newBlock(content.BlockParagraph)
			own := noteAt{id: blk.ID}
			noted := len(c.notes)
			text, err := c.richRange(pn, it.first, it.last, own)
			if err != nil {
				return nil, err
			}
			if emptyText(text) {
				// What was left out of a paragraph that is not made, such as a script, is said at the flow. The other notes of it
				// are never reported.
				kept := c.notes[:noted]
				for _, n := range c.notes[noted:] {
					switch {
					case n.at != own:
						kept = append(kept, n)
					case n.kind == Skipped:
						n.at = at
						kept = append(kept, n)
					}
				}
				c.notes = kept
				// A text that makes no block counts too, as a body can have millions of them.
				if !c.admit(at) {
					return units, nil
				}
				if carry := c.takeTargets(mark); len(carry) > 0 {
					units = append(units, paraUnit{carry: carry})
				}
				continue
			}
			blk.Text = text
			blk.Attrs.TextAlign = s.align
			alignUsed = alignUsed || s.align != ""
			c.applyProps(&blk, s.block, anchor, own)
			anchor = nil
			made = []content.Block{blk}
		case itemImage:
			made = c.classicImage(classicImageInput{img: it.img, link: it.link, legacyOf: it.unit, anchor: idOwner(anchor, it.img), align: s.align}, at)
			if len(made) > 0 && made[0].Type == content.BlockImage {
				anchor = nil
				// An image that is aligned as the paragraph is loses nothing of it.
				if s.align != "" && made[0].Attrs.Align == s.align {
					alignUsed = true
				}
			}
		case itemFrame:
			made = c.frameBlocks(it.unit, idOwner(anchor, it.unit), at)
			if len(made) > 0 && made[0].Type == content.BlockEmbed {
				anchor = nil
			}
		case itemPiece:
			pc := c.pieceOf(it.unit)
			var err error
			if made, unit.admitted, err = c.convertPiece(pc, at); err != nil {
				made, unit.admitted = c.legacyText(pc.text, pc.what(), reasonOf(err), at), 0
				c.targets = c.targets[:min(mark, len(c.targets))]
			}
			unit.piece = pc
		}
		if len(made) > 0 {
			c.claimAnchors(&made[0], c.takeTargets(mark))
		} else {
			unit.carry = c.takeTargets(mark)
		}
		unit.blocks = made
		units = append(units, unit)
		if firstID == "" && len(made) > 0 {
			firstID = made[0].ID
		}
	}
	if firstID != "" {
		lost := s.lost
		if s.align != "" && !alignUsed {
			// No block has the alignment of the paragraph, so the settings are lost with it.
			_, lost = lostSettings(pn, false)
		}
		c.lostOf(noteAt{id: firstID}, pn, lost)
	}
	return units, nil
}

// idOwner returns the element whose id is the anchor of the block of unit, an image or an iframe in a paragraph: pn, the
// paragraph, if it has an id that no block has taken, else unit, which may have one. It is nil once a block has taken the id.
func idOwner(pn, unit *html.Node) *html.Node {
	if pn != nil && attr(pn, "id") == "" {
		return unit
	}
	return pn
}

// classicImageInput is what classicImage makes an image block of.
type classicImageInput struct {
	img, link, caption *html.Node // the img element, the a element around it, and the figcaption element
	legacyOf           *html.Node // the element that is a legacy block if the image cannot be one
	anchor             *html.Node // the element whose id is the anchor of the block, or nil
	outer              *html.Node // the figure element, whose classes the img does not have, or nil
	align              string     // the alignment, if the classes of the img and of the figure tell none
	id                 int64      // the ID of the attachment, if something other than the img tells it
}

// imageAlign reads the alignment from the classes that WordPress gives an image. The class alignnone tells none: the image
// stays where the text puts it.
func imageAlign(img *html.Node) (align string, found bool) {
	for _, class := range classList(img) {
		switch class {
		case "alignleft":
			return "left", true
		case "aligncenter":
			return "center", true
		case "alignright":
			return "right", true
		}
	}
	return "", false
}

// imageSizeClass returns the name of the size in the first class size-<name> of n, or "".
func imageSizeClass(n *html.Node) string {
	for _, class := range classList(n) {
		if rest, ok := strings.CutPrefix(class, "size-"); ok && rest != "" {
			return rest
		}
	}
	return ""
}

// withClassesFirst returns a copy of b, whose attribute className has the classes in front of those it has.
func withClassesFirst(b *wpblock.Block, classes []string) *wpblock.Block {
	cp := *b
	cp.Attrs = &wpblock.Value{Kind: wpblock.Object, Object: slices.Clone(b.Attrs.Object)}
	joined := strings.Join(classes, " ")
	if i := slices.IndexFunc(cp.Attrs.Object, func(m wpblock.Member) bool { return m.Name == "className" }); i >= 0 {
		cp.Attrs.Object[i].Value = wpblock.Value{Kind: wpblock.String, String: joined + " " + str(&cp.Attrs.Object[i].Value)}
	} else {
		cp.Attrs.Object = append(cp.Attrs.Object, wpblock.Member{Name: "className", Value: wpblock.Value{Kind: wpblock.String, String: joined}})
	}
	return &cp
}

// classicImage makes the image block of in. If the image is not an imported one, or its caption or its link cannot be
// converted, the blocks are a legacy block of the element in.legacyOf.
func (c *bodyConv) classicImage(in classicImageInput, at noteAt) []content.Block {
	blk, err := c.classicImageBlock(in)
	if err != nil {
		return c.classicLegacy(in.legacyOf, reasonOf(err), at)
	}
	return []content.Block{blk}
}

// classicImageBlock is classicImage, but the error says why the image cannot be an image block, with nothing of it left in the
// notes. The error is a legacy error.
func (c *bodyConv) classicImageBlock(in classicImageInput) (content.Block, error) {
	size := imageSizeClass(in.img)
	if size == "" && in.outer != nil {
		size = imageSizeClass(in.outer)
	}
	m := c.snapshot()
	blk, err := c.imageBlock(imageInput{img: in.img, link: in.link, caption: in.caption, id: in.id, size: size})
	if err != nil {
		c.restore(m)
		return content.Block{}, err
	}
	own := noteAt{id: blk.ID}
	// The file has the size, so that the attributes width and height say nothing more.
	s := classicProps("core/image", in.img, false, nil, "width", "height")
	if attr(in.img, "title") != "" {
		s.lost = append(s.lost, "title")
	}
	align, found := imageAlign(in.img)
	if in.outer != nil {
		if classes := classNames(in.outer, nil); len(classes) > 0 {
			s.block = withClassesFirst(s.block, classes)
		}
		if !found {
			align, found = imageAlign(in.outer)
		}
	}
	if !found {
		align = in.align
	}
	blk.Attrs.Align = align
	c.applyProps(&blk, s.block, in.anchor, own)
	c.lostOf(own, in.img, s.lost)
	return blk, nil
}
