package html

import (
	"errors"
	"math"
)

// Options are the options of Parse and ParseFragment.
type Options struct {
	// Scripting is the scripting flag. Browsers parse with it set, so that the content of a noscript element is
	// text; without it, noscript elements hold markup.
	Scripting bool

	// MaxNodes and MaxSteps, if positive, limit a parse: past either, Parse and ParseFragment stop and return ErrLimit.
	// MaxNodes limits the size of the tree. Each node counts one, and each attribute of an element one more; an element
	// that the parser copies from a formatting element (to reconstruct it, or in the adoption agency algorithm) also
	// counts one for each 16 bytes of the names and values of its attributes, which it shares with the original but
	// which Render writes again. MaxSteps limits the work: a step is a token, a node or attribute counted against
	// MaxNodes, an element of the stack of open elements or an entry of the list of active formatting elements that a
	// scan looks at, and the like. A parse then takes time linear in len(s) and MaxSteps, and Render writes its tree in
	// time linear in len(s) and MaxNodes.
	MaxNodes, MaxSteps int
}

// ErrLimit is the error of Parse and ParseFragment when a parse passes Options.MaxNodes or Options.MaxSteps.
var ErrLimit = errors.New("html: the input passes the limits of the parser")

// Parse parses s as an HTML document, as the HTML Standard does for a document whose input is s (13.2), and returns
// its Document node. Scripts are not run, and parse errors are not reported. The parsed contents of a template element
// always go to its Content: declarative shadow roots and content patching (the for attribute of template) are not
// supported. The only error is ErrLimit.
func Parse(s string, opts Options) (*Node, error) {
	p, err := parse(s, nil, opts)
	if err != nil {
		return nil, err
	}
	return p.doc, nil
}

// parse parses s as a document or, if context is not nil, as a fragment for context. The only error is ErrLimit.
func parse(s string, context *Node, opts Options) (p *parser, err error) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(limitPassed); !ok {
				panic(r)
			}
			err = ErrLimit
		}
	}()
	p = newParser(s, opts)
	if context != nil {
		p.initFragment(context)
	}
	p.run()
	return p, nil
}

func newParser(s string, opts Options) *parser {
	p := &parser{
		z:               newTokenizer(s),
		mode:            initialMode,
		scripting:       opts.Scripting,
		framesetOK:      true,
		texts:           make(map[*Node][]byte),
		htmlAnnotations: make(map[*Node]bool),
		maxNodes:        limit(opts.MaxNodes),
		maxSteps:        limit(opts.MaxSteps),
	}
	p.grow(1)
	p.doc = &Node{Type: DocumentNode}
	p.z.cdataAllowed = func() bool { return len(p.stack) > 0 && p.adjustedCurrentNode().Namespace != HTML }
	return p
}

// limit returns the limit n of Options, or the largest int for no limit.
func limit(n int) int {
	if n <= 0 {
		return math.MaxInt
	}
	return n
}

// limitPassed is the panic of step and grow when a limit is passed, which parse recovers.
type limitPassed struct{}

// step counts n steps against MaxSteps.
func (p *parser) step(n int) {
	if p.steps += n; p.steps > p.maxSteps {
		panic(limitPassed{})
	}
}

// grow counts n nodes against MaxNodes, and as many steps for making them.
func (p *parser) grow(n int) {
	p.step(n)
	if p.nodes += n; p.nodes > p.maxNodes {
		panic(limitPassed{})
	}
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

	// The MathML annotation-xml elements that are HTML integration points, the context element among them: found once,
	// since their attributes can be many.
	htmlAnnotations map[*Node]bool

	nodes, steps       int // counted against the limits
	maxNodes, maxSteps int
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
	p.step(1)
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
		return p.isHTMLIntegrationPoint(n)
	case characterToken:
		return isMathMLTextIntegrationPoint(n) || p.isHTMLIntegrationPoint(n)
	case eofToken:
		return true
	case doctypeToken, endTagToken, commentToken, processingInstructionToken:
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
