package html

// Options are the options of Parse.
type Options struct {
	// Scripting is the scripting flag. Browsers parse with it set, so that the content of a noscript element is
	// text; without it, noscript elements hold markup.
	Scripting bool
}

// Parse parses s as an HTML document, as the HTML Standard does for a document whose input is s (13.2), and returns
// its Document node. Scripts are not run, and parse errors are not reported. The parsed contents of a template element
// always go to its Content: declarative shadow roots and content patching (the for attribute of template) are not
// supported.
func Parse(s string, opts Options) (*Node, error) {
	p := &parser{
		z:          newTokenizer(s),
		doc:        &Node{Type: DocumentNode},
		mode:       initialMode,
		scripting:  opts.Scripting,
		framesetOK: true,
		texts:      make(map[*Node][]byte),
	}
	p.run()
	return p.doc, nil
}

// insertionMode is an insertion mode of the tree construction stage (13.2.4.1).
type insertionMode uint8

const (
	initialMode insertionMode = iota + 1
	beforeHTMLMode
	beforeHeadMode
	inHeadMode
	inHeadNoscriptMode
	afterHeadMode
	inBodyMode
	textMode
	inTableMode
	inTableTextMode
	inCaptionMode
	inColumnGroupMode
	inTableBodyMode
	inRowMode
	inCellMode
	inTemplateMode
	afterBodyMode
	inFramesetMode
	afterFramesetMode
	afterAfterBodyMode
	afterAfterFramesetMode
)

// documentMode is the mode of the document: no-quirks, limited-quirks or quirks.
type documentMode uint8

const (
	noQuirks documentMode = iota
	limitedQuirks
	quirks
)

// parser is the tree construction stage (13.2.6) with the parse state of 13.2.4.
type parser struct {
	z   *tokenizer
	doc *Node

	mode, originalMode insertionMode
	templateModes      []insertionMode // the stack of template insertion modes
	stack              []*Node         // the stack of open elements
	active             []*Node         // the list of active formatting elements; nil is a marker
	head, form         *Node           // the element pointers
	context            *Node           // the fragment context element
	scripting          bool
	framesetOK         bool
	fosterParenting    bool
	docMode            documentMode

	// The insertion target redirection map. With no sanitizer configuration, it only redirects insertions into the
	// root html element to the fragment in the fragment case.
	redirectFrom, redirectTo *Node

	skipLF bool // the next token is ignored if it is a line feed (after pre, listing and textarea start tags)

	pendingTable []byte // the pending table character tokens of "in table text"

	texts map[*Node][]byte // the data of the Text nodes that characters were appended to
	hosts map[*Node]*Node  // the template element of each template contents
}

// run runs the tree construction stage over the tokens up to the end of the input, then the steps of 13.2.7 that
// apply.
func (p *parser) run() {
	for {
		tok := p.z.next()
		if p.skipLF {
			p.skipLF = false
			if tok.typ == characterToken && tok.data[0] == '\n' {
				if tok.data = tok.data[1:]; tok.data == "" {
					continue
				}
			}
		}
		for p.dispatch(&tok) {
		}
		if tok.typ == eofToken {
			break
		}
	}
	p.popTo(0)
	for n, b := range p.texts {
		n.Data = string(b)
	}
}

// dispatch processes tok as the tree construction dispatcher of 13.2.6 says, and reports whether tok must be
// reprocessed.
func (p *parser) dispatch(tok *token) bool {
	if p.htmlContent(tok) {
		return p.process(p.mode, tok)
	}
	return p.inForeignContent(tok)
}

// htmlContent reports whether the tree construction dispatcher processes tok with the rules of the current insertion
// mode, rather than with those for foreign content.
func (p *parser) htmlContent(tok *token) bool {
	if len(p.stack) == 0 {
		return true
	}
	n := p.adjustedCurrentNode()
	if n.Namespace == HTML {
		return true
	}
	switch tok.typ {
	case startTagToken:
		if isMathMLTextIntegrationPoint(n) && tok.name != "mglyph" && tok.name != "malignmark" {
			return true
		}
		if n.Namespace == MathML && n.Name == "annotation-xml" && tok.name == "svg" {
			return true
		}
		return isHTMLIntegrationPoint(n)
	case characterToken:
		return isMathMLTextIntegrationPoint(n) || isHTMLIntegrationPoint(n)
	case eofToken:
		return true
	case doctypeToken, endTagToken, commentToken, processingInstructionToken:
	}
	return false
}

func isMathMLTextIntegrationPoint(n *Node) bool {
	if n.Namespace != MathML {
		return false
	}
	switch n.Name {
	case "mi", "mo", "mn", "ms", "mtext":
		return true
	}
	return false
}

// isHTMLIntegrationPoint reports whether n is an HTML integration point. The attributes of an annotation-xml element
// are those of its start tag token, since adjusting the attributes of a token does not rename encoding.
func isHTMLIntegrationPoint(n *Node) bool {
	switch n.Namespace {
	case MathML:
		if n.Name != "annotation-xml" {
			return false
		}
		for _, a := range n.Attrs {
			if a.Namespace == "" && a.Name == "encoding" {
				return equalFold(a.Value, "text/html") || equalFold(a.Value, "application/xhtml+xml")
			}
		}
	case SVG:
		switch n.Name {
		case "foreignObject", "desc", "title":
			return true
		}
	case HTML:
	}
	return false
}

// process processes tok with the rules of the insertion mode m and reports whether tok must be reprocessed.
func (p *parser) process(m insertionMode, tok *token) bool {
	switch m {
	case initialMode:
		return p.initial(tok)
	case beforeHTMLMode:
		return p.beforeHTML(tok)
	case beforeHeadMode:
		return p.beforeHead(tok)
	case inHeadMode:
		return p.inHead(tok)
	case inHeadNoscriptMode:
		return p.inHeadNoscript(tok)
	case afterHeadMode:
		return p.afterHead(tok)
	case inBodyMode:
		return p.inBody(tok)
	case textMode:
		return p.text(tok)
	case inTableMode:
		return p.inTable(tok)
	case inTableTextMode:
		return p.inTableText(tok)
	case inCaptionMode:
		return p.inCaption(tok)
	case inColumnGroupMode:
		return p.inColumnGroup(tok)
	case inTableBodyMode:
		return p.inTableBody(tok)
	case inRowMode:
		return p.inRow(tok)
	case inCellMode:
		return p.inCell(tok)
	case inTemplateMode:
		return p.inTemplate(tok)
	case afterBodyMode:
		return p.afterBody(tok)
	case inFramesetMode:
		return p.inFrameset(tok)
	case afterFramesetMode:
		return p.afterFrameset(tok)
	case afterAfterBodyMode:
		return p.afterAfterBody(tok)
	case afterAfterFramesetMode:
		return p.afterAfterFrameset(tok)
	}
	panic("html: no insertion mode")
}

// inForeignContent applies the rules for parsing tokens in foreign content (13.2.6.5). Until they exist, the current
// insertion mode applies.
func (p *parser) inForeignContent(tok *token) bool {
	return p.process(p.mode, tok)
}
