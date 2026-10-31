package html

import "slices"

// isHTML reports whether n is an HTML element named name. n can be nil.
func (n *Node) isHTML(name string) bool {
	return n != nil && n.Type == ElementNode && n.Namespace == HTML && n.Name == name
}

func (p *parser) currentNode() *Node {
	return p.stack[len(p.stack)-1]
}

// adjustedCurrentNode is the adjusted current node of 13.2.4.2.
func (p *parser) adjustedCurrentNode() *Node {
	if p.context != nil && len(p.stack) == 1 {
		return p.context
	}
	return p.currentNode()
}

func (p *parser) push(n *Node) {
	p.stack = append(p.stack, n)
}

func (p *parser) pop() {
	p.popTo(len(p.stack) - 1)
}

// popTo pops the elements from the current node up to the element at index i, including it.
func (p *parser) popTo(i int) {
	clear(p.stack[i:])
	p.stack = p.stack[:i]
}

// popUntil pops elements from the stack of open elements until an HTML element named name has been popped.
func (p *parser) popUntil(name string) {
	for i := len(p.stack) - 1; i >= 0; i-- {
		p.step(1)
		if p.stack[i].isHTML(name) {
			p.popTo(i)
			return
		}
	}
}

// stackIndex returns the index of n in the stack of open elements, or -1.
func (p *parser) stackIndex(n *Node) int {
	for i := len(p.stack) - 1; i >= 0; i-- {
		p.step(1)
		if p.stack[i] == n {
			return i
		}
	}
	return -1
}

func (p *parser) removeFromStack(i int) {
	p.step(len(p.stack) - i)
	p.stack = slices.Delete(p.stack, i, i+1)
}

// templateOnStack reports whether there is a template element on the stack of open elements.
func (p *parser) templateOnStack() bool {
	for _, n := range p.stack {
		p.step(1)
		if n.isHTML("template") {
			return true
		}
	}
	return false
}

// parsingTemplateContents reports whether the parser is parsing template contents (13.2.4.4).
func (p *parser) parsingTemplateContents() bool {
	return p.context.isHTML("template") || p.templateOnStack()
}

// scope is one of the specific scopes of 13.2.4.2.
type scope uint8

const (
	defaultScope scope = iota
	listItemScope
	buttonScope
	tableScope
)

// bounds reports whether n is one of the element types that the scope s consists of.
func (s scope) bounds(n *Node) bool {
	switch n.Namespace {
	case HTML:
		switch n.Name {
		case "html", "table", "template":
			return true
		case "applet", "caption", "td", "th", "marquee", "object", "select":
			return s != tableScope
		case "ol", "ul":
			return s == listItemScope
		case "button":
			return s == buttonScope
		}
	case MathML:
		switch n.Name {
		case "mi", "mo", "mn", "ms", "mtext", "annotation-xml":
			return s != tableScope
		}
	case SVG:
		switch n.Name {
		case "foreignObject", "desc", "title":
			return s != tableScope
		}
	}
	return false
}

// inScope reports whether the stack of open elements has an HTML element named name in the scope s.
func (p *parser) inScope(name string, s scope) bool {
	for i := len(p.stack) - 1; i >= 0; i-- {
		p.step(1)
		n := p.stack[i]
		if n.isHTML(name) {
			return true
		}
		if s.bounds(n) {
			return false
		}
	}
	return false
}

// nodeInScope reports whether the stack of open elements has the element target in the scope s.
func (p *parser) nodeInScope(target *Node, s scope) bool {
	for i := len(p.stack) - 1; i >= 0; i-- {
		p.step(1)
		n := p.stack[i]
		if n == target {
			return true
		}
		if s.bounds(n) {
			return false
		}
	}
	return false
}

func isHeading(n *Node) bool {
	if n.Namespace != HTML {
		return false
	}
	switch n.Name {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		return true
	}
	return false
}

// headingInScope reports whether the stack of open elements has an HTML h1-h6 element in scope.
func (p *parser) headingInScope() bool {
	for i := len(p.stack) - 1; i >= 0; i-- {
		p.step(1)
		n := p.stack[i]
		if isHeading(n) {
			return true
		}
		if defaultScope.bounds(n) {
			return false
		}
	}
	return false
}

// popUntilHeading pops elements from the stack of open elements until an HTML h1-h6 element has been popped.
func (p *parser) popUntilHeading() {
	for i := len(p.stack) - 1; i >= 0; i-- {
		p.step(1)
		if isHeading(p.stack[i]) {
			p.popTo(i)
			return
		}
	}
}

// isSpecial reports whether n is in the special category of 13.2.4.2.
func isSpecial(n *Node) bool {
	switch n.Namespace {
	case HTML:
		switch n.Name {
		case "address", "applet", "area", "article", "aside", "base", "basefont", "bgsound", "blockquote", "body",
			"br", "button", "caption", "center", "col", "colgroup", "dd", "details", "dir", "div", "dl", "dt",
			"embed", "fieldset", "figcaption", "figure", "footer", "form", "frame", "frameset", "h1", "h2", "h3", "h4",
			"h5", "h6", "head", "header", "hgroup", "hr", "html", "iframe", "img", "input", "keygen", "li", "link",
			"listing", "main", "marquee", "menu", "meta", "nav", "noembed", "noframes", "noscript", "object", "ol",
			"p", "param", "plaintext", "pre", "script", "search", "section", "select", "source", "style", "summary",
			"table", "tbody", "td", "template", "textarea", "tfoot", "th", "thead", "title", "tr", "track", "ul",
			"wbr", "xmp":
			return true
		}
	case MathML:
		switch n.Name {
		case "mi", "mo", "mn", "ms", "mtext", "annotation-xml":
			return true
		}
	case SVG:
		switch n.Name {
		case "foreignObject", "desc", "title":
			return true
		}
	}
	return false
}

// generateImpliedEndTags generates implied end tags (13.2.6.3), except for HTML elements named except.
func (p *parser) generateImpliedEndTags(except string) {
	for {
		p.step(1)
		n := p.currentNode()
		if n.Namespace != HTML || n.Name == except {
			return
		}
		switch n.Name {
		case "dd", "dt", "li", "optgroup", "option", "p", "rb", "rp", "rt", "rtc":
			p.pop()
		default:
			return
		}
	}
}

// generateAllImpliedEndTagsThoroughly generates all implied end tags thoroughly (13.2.6.3).
func (p *parser) generateAllImpliedEndTagsThoroughly() {
	for {
		p.step(1)
		n := p.currentNode()
		if n.Namespace != HTML {
			return
		}
		switch n.Name {
		case "caption", "colgroup", "dd", "dt", "li", "optgroup", "option", "p", "rb", "rp", "rt", "rtc", "tbody",
			"td", "tfoot", "th", "thead", "tr":
			p.pop()
		default:
			return
		}
	}
}

// closeP closes a p element (13.2.6.4.7).
func (p *parser) closeP() {
	p.generateImpliedEndTags("p")
	p.popUntil("p")
}

// closePInButtonScope closes a p element if the stack of open elements has one in button scope.
func (p *parser) closePInButtonScope() {
	if p.inScope("p", buttonScope) {
		p.closeP()
	}
}

// resetInsertionMode resets the insertion mode appropriately (13.2.4.1).
func (p *parser) resetInsertionMode() {
	for i := len(p.stack) - 1; i >= 0; i-- {
		p.step(1)
		node := p.stack[i]
		last := i == 0
		if last && p.context != nil {
			node = p.context
		}
		if node.Namespace == HTML {
			switch node.Name {
			case "td", "th":
				if !last {
					p.mode = inCellMode
					return
				}
			case "tr":
				p.mode = inRowMode
				return
			case "tbody", "thead", "tfoot":
				p.mode = inTableBodyMode
				return
			case "caption":
				p.mode = inCaptionMode
				return
			case "colgroup":
				p.mode = inColumnGroupMode
				return
			case "table":
				p.mode = inTableMode
				return
			case "template":
				p.mode = p.templateModes[len(p.templateModes)-1]
				return
			case "head":
				if !last {
					p.mode = inHeadMode
					return
				}
			case "body":
				p.mode = inBodyMode
				return
			case "frameset":
				p.mode = inFramesetMode
				return
			case "html":
				if p.head == nil {
					p.mode = beforeHeadMode
				} else {
					p.mode = afterHeadMode
				}
				return
			}
		}
		if last {
			p.mode = inBodyMode
			return
		}
	}
}

// activeIndex returns the index of n in the list of active formatting elements, or -1.
func (p *parser) activeIndex(n *Node) int {
	for i := len(p.active) - 1; i >= 0; i-- {
		p.step(1)
		if p.active[i] == n {
			return i
		}
	}
	return -1
}

func (p *parser) removeActive(i int) {
	p.step(len(p.active) - i)
	p.active = slices.Delete(p.active, i, i+1)
}

func (p *parser) insertMarker() {
	p.active = append(p.active, nil)
}

// pushActive pushes n onto the list of active formatting elements (13.2.4.3), after removing the earliest of three
// elements like n since the last marker (the Noah's Ark clause).
func (p *parser) pushActive(n *Node) {
	count, earliest := 0, -1
	for i := len(p.active) - 1; i >= 0 && p.active[i] != nil; i-- {
		p.step(1)
		e := p.active[i]
		if e.Name == n.Name && e.Namespace == n.Namespace && p.sameAttrs(e.Attrs, n.Attrs) {
			count++
			earliest = i
		}
	}
	if count >= 3 {
		p.removeActive(earliest)
	}
	p.active = append(p.active, n)
}

// sameAttrs reports whether a and b, each without repeated names, have the same attributes in any order.
func (p *parser) sameAttrs(a, b []Attr) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) <= manyAttrs {
	next:
		for _, x := range a {
			for _, y := range b {
				p.step(1)
				if x == y {
					continue next
				}
			}
			return false
		}
		return true
	}
	p.step(len(a) + len(b))
	m := make(map[Attr]struct{}, len(a))
	for _, x := range a {
		m[x] = struct{}{}
	}
	for _, y := range b {
		if _, ok := m[y]; !ok {
			return false
		}
	}
	return true
}

// reconstructActive reconstructs the active formatting elements (13.2.4.3).
func (p *parser) reconstructActive() {
	i := len(p.active) - 1
	if i < 0 {
		return
	}
	if e := p.active[i]; e == nil || p.stackIndex(e) >= 0 {
		return
	}
	for i > 0 {
		if e := p.active[i-1]; e == nil || p.stackIndex(e) >= 0 {
			break
		}
		i--
	}
	for ; i < len(p.active); i++ {
		p.active[i] = p.insertHTMLElement(p.tokenFor(p.active[i]))
	}
}

// clearActiveToMarker clears the list of active formatting elements up to the last marker.
func (p *parser) clearActiveToMarker() {
	for len(p.active) > 0 {
		p.step(1)
		i := len(p.active) - 1
		e := p.active[i]
		p.active[i] = nil
		p.active = p.active[:i]
		if e == nil {
			return
		}
	}
}
