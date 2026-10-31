package html

import "slices"

// appropriatePlace returns the appropriate place for inserting a node (13.2.6.1) with the override target override,
// or none if it is nil: a node goes into parent before ref, or at the end of parent if ref is nil.
func (p *parser) appropriatePlace(override *Node) (parent, ref *Node) {
	parent = override
	if parent == nil {
		parent = p.currentNode()
	}
	if p.fosterParenting && parent.Type == ElementNode && parent.Namespace == HTML {
		switch parent.Name {
		case "table", "tbody", "tfoot", "thead", "tr":
			parent, ref = p.fosterPlace()
		}
	}
	if !parent.isHTML("template") {
		return parent, ref
	}
	// Content patching (the for attribute of template) is not supported: the insertion target is always null.
	return parent.Content, nil
}

// fosterPlace is the place that foster parenting finds from the last template or table element in the stack of open
// elements (13.2.6.1).
func (p *parser) fosterPlace() (parent, ref *Node) {
	for i := len(p.stack) - 1; i >= 0; i-- {
		p.step(1)
		switch n := p.stack[i]; {
		case n.isHTML("template"):
			return n, nil
		case n.isHTML("table"):
			if n.Parent != nil {
				return n.Parent, n
			}
			return p.stack[i-1], nil
		}
	}
	return p.stack[0], nil
}

// adjusted returns the adjusted insertion location given the insertion location (parent, ref).
func (p *parser) adjusted(parent, ref *Node) (*Node, *Node) {
	if p.redirectFrom != nil && parent == p.redirectFrom {
		return p.redirectTo, nil
	}
	return parent, ref
}

// createElement creates an element for the token tok in the namespace ns (13.2.6.1). There are no custom elements and
// no sanitizer configuration. An HTML template element gets empty template contents.
func (p *parser) createElement(tok *token, ns Namespace) *Node {
	p.grow(1 + len(tok.attrs))
	n := &Node{Type: ElementNode, Namespace: ns, Name: tok.name, Attrs: tok.attrs}
	if ns == HTML && tok.name == "template" {
		p.grow(1)
		n.Content = &Node{Type: DocumentFragmentNode}
		if p.hosts == nil {
			p.hosts = make(map[*Node]*Node)
		}
		p.hosts[n.Content] = n
	}
	return n
}

// tokenFor returns a start tag token like the one that e, an element of the list of active formatting elements, was
// created for: the parser never changes the attributes of those elements. The element made from it shares the names
// and values of the attributes, but Render writes them again for each copy: they count a node for each copyBytes bytes.
func (p *parser) tokenFor(e *Node) *token {
	p.step(len(e.Attrs))
	size := 0
	for _, a := range e.Attrs {
		size += len(a.Namespace) + len(a.Name) + len(a.Value)
	}
	p.grow(size / copyBytes)
	return &token{typ: startTagToken, name: e.Name, attrs: slices.Clone(e.Attrs)}
}

// copyBytes is the bytes of the attributes of a copied element that count as a node.
const copyBytes = 16

// insertElement inserts n, a new element, at the adjusted insertion location given (parent, ref). Since n has no
// parent and no children, it cannot be a host-including inclusive ancestor of the parent.
func (p *parser) insertElement(n, parent, ref *Node) {
	parent, ref = p.adjusted(parent, ref)
	if parent.Type == DocumentNode && hasElementChild(parent) {
		return
	}
	parent.InsertBefore(n, ref)
}

func hasElementChild(n *Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == ElementNode {
			return true
		}
	}
	return false
}

// insertForeignElement inserts a foreign element for tok in the namespace ns and pushes it onto the stack of open
// elements.
func (p *parser) insertForeignElement(tok *token, ns Namespace) *Node {
	n := p.createElement(tok, ns)
	parent, ref := p.appropriatePlace(nil)
	p.insertElement(n, parent, ref)
	p.push(n)
	return n
}

func (p *parser) insertHTMLElement(tok *token) *Node {
	return p.insertForeignElement(tok, HTML)
}

// insertText inserts the characters s (13.2.6.1). Characters appended to an existing Text node go to its buffer in
// texts, so that appending is linear; finish sets the data.
func (p *parser) insertText(s string) {
	parent, ref := p.appropriatePlace(nil)
	parent, ref = p.adjusted(parent, ref)
	if parent.Type == DocumentNode {
		return
	}
	prev := parent.LastChild
	if ref != nil {
		prev = ref.PrevSibling
	}
	if prev != nil && prev.Type == TextNode {
		b, ok := p.texts[prev]
		if !ok {
			b = []byte(prev.Data)
		}
		p.texts[prev] = append(b, s...)
		return
	}
	p.grow(1)
	parent.InsertBefore(&Node{Type: TextNode, Data: s}, ref)
}

// insertComment inserts a comment with data at the adjusted insertion location given (parent, ref), or given the
// appropriate place for inserting a node if parent is nil.
func (p *parser) insertComment(data string, parent, ref *Node) {
	if parent == nil {
		parent, ref = p.appropriatePlace(nil)
	}
	parent, ref = p.adjusted(parent, ref)
	p.grow(1)
	parent.InsertBefore(&Node{Type: CommentNode, Data: data}, ref)
}

// insertProcessingInstruction inserts a processing instruction for tok at the adjusted insertion location given
// (parent, ref), or given the appropriate place for inserting a node if parent is nil.
func (p *parser) insertProcessingInstruction(tok *token, parent, ref *Node) {
	if parent == nil {
		parent, ref = p.appropriatePlace(nil)
	}
	parent, ref = p.adjusted(parent, ref)
	p.grow(1)
	parent.InsertBefore(&Node{Type: ProcessingInstructionNode, Name: tok.name, Data: tok.data}, ref)
}

// parseText follows the generic raw text element parsing algorithm if st is rawtextState, or the generic RCDATA element
// parsing algorithm if st is rcdataState (13.2.6.2).
func (p *parser) parseText(tok *token, st state) {
	p.insertHTMLElement(tok)
	p.z.setState(st)
	p.originalMode = p.mode
	p.mode = textMode
}

// hostIncludingInclusiveAncestor reports whether a is a host-including inclusive ancestor of b: an inclusive ancestor
// of b or, if the root of b is the template contents of a template element, of that element.
func (p *parser) hostIncludingInclusiveAncestor(a, b *Node) bool {
	for n := b; n != nil; {
		p.step(1)
		if n == a {
			return true
		}
		if n.Parent != nil {
			n = n.Parent
		} else {
			n = p.hosts[n]
		}
	}
	return false
}
