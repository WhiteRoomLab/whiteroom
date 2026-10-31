package html

import (
	"slices"
	"strings"
)

// 13.2.6.4.7
func (p *parser) inBody(tok *token) bool {
	switch tok.typ {
	case characterToken:
		p.bodyText(tok.data)
	case commentToken:
		p.insertComment(tok.data, nil, nil)
	case processingInstructionToken:
		p.insertProcessingInstruction(tok, nil, nil)
	case doctypeToken:
	case startTagToken:
		return p.inBodyStartTag(tok)
	case endTagToken:
		return p.inBodyEndTag(tok)
	case eofToken:
		if len(p.templateModes) > 0 {
			return p.inTemplate(tok)
		}
	}
	return false
}

// bodyText processes the characters s in the "in body" insertion mode: U+0000 is ignored, and characters other than
// whitespace set the frameset-ok flag to "not ok".
func (p *parser) bodyText(s string) {
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
		if s == "" {
			return
		}
	}
	p.reconstructActive()
	p.insertText(s)
	if _, rest := splitSpace(s); rest != "" {
		p.framesetOK = false
	}
}

func (p *parser) inBodyStartTag(tok *token) bool {
	switch tok.name {
	case "html":
		if !p.templateOnStack() {
			p.addMissingAttrs(p.stack[0], tok.attrs)
		}
	case "base", "basefont", "bgsound", "link", "meta", "noframes", "script", "style", "template", "title":
		return p.inHead(tok)
	case "body":
		if len(p.stack) > 1 && p.stack[1].isHTML("body") && !p.templateOnStack() {
			p.framesetOK = false
			p.addMissingAttrs(p.stack[1], tok.attrs)
		}
	case "frameset":
		if len(p.stack) == 1 || !p.stack[1].isHTML("body") || !p.framesetOK {
			return false
		}
		if body := p.stack[1]; body.Parent != nil {
			body.Parent.RemoveChild(body)
		}
		p.popTo(1)
		p.insertHTMLElement(tok)
		p.mode = inFramesetMode
	case "address", "article", "aside", "blockquote", "center", "details", "dialog", "dir", "div", "dl", "fieldset",
		"figcaption", "figure", "footer", "header", "hgroup", "main", "menu", "nav", "ol", "p", "search", "section",
		"summary", "ul":
		p.closePInButtonScope()
		p.insertHTMLElement(tok)
	case "h1", "h2", "h3", "h4", "h5", "h6":
		p.closePInButtonScope()
		if isHeading(p.currentNode()) {
			p.pop()
		}
		p.insertHTMLElement(tok)
	case "pre", "listing":
		p.closePInButtonScope()
		p.insertHTMLElement(tok)
		p.skipLF = true
		p.framesetOK = false
	case "form":
		contents := p.parsingTemplateContents()
		if p.form != nil && !contents {
			return false
		}
		p.closePInButtonScope()
		n := p.insertHTMLElement(tok)
		if !contents {
			p.form = n
		}
	case "li":
		p.framesetOK = false
		p.closeListItem(func(n *Node) bool { return n.isHTML("li") })
		p.closePInButtonScope()
		p.insertHTMLElement(tok)
	case "dd", "dt":
		p.framesetOK = false
		p.closeListItem(func(n *Node) bool { return n.isHTML("dd") || n.isHTML("dt") })
		p.closePInButtonScope()
		p.insertHTMLElement(tok)
	case "plaintext":
		p.closePInButtonScope()
		p.insertHTMLElement(tok)
		p.z.setState(plaintextState)
	case "button":
		if p.inScope("button", defaultScope) {
			p.generateImpliedEndTags("")
			p.popUntil("button")
		}
		p.reconstructActive()
		p.insertHTMLElement(tok)
		p.framesetOK = false
	case "a":
		for i := len(p.active) - 1; i >= 0 && p.active[i] != nil; i-- {
			p.step(1)
			if a := p.active[i]; a.Name == "a" {
				p.adoptionAgency(tok)
				if j := p.activeIndex(a); j >= 0 {
					p.removeActive(j)
				}
				if j := p.stackIndex(a); j >= 0 {
					p.removeFromStack(j)
				}
				break
			}
		}
		p.reconstructActive()
		p.pushActive(p.insertHTMLElement(tok))
	case "b", "big", "code", "em", "font", "i", "s", "small", "strike", "strong", "tt", "u":
		p.reconstructActive()
		p.pushActive(p.insertHTMLElement(tok))
	case "nobr":
		p.reconstructActive()
		if p.inScope("nobr", defaultScope) {
			p.adoptionAgency(tok)
			p.reconstructActive()
		}
		p.pushActive(p.insertHTMLElement(tok))
	case "applet", "marquee", "object":
		p.reconstructActive()
		p.insertHTMLElement(tok)
		p.insertMarker()
		p.framesetOK = false
	case "table":
		if p.docMode != quirks {
			p.closePInButtonScope()
		}
		p.insertHTMLElement(tok)
		p.framesetOK = false
		p.mode = inTableMode
	case "area", "br", "embed", "img", "keygen", "wbr":
		p.reconstructActive()
		p.insertHTMLElement(tok)
		p.pop()
		p.framesetOK = false
	case "input":
		if p.context.isHTML("select") {
			return false
		}
		if p.inScope("select", defaultScope) {
			p.popUntil("select")
		}
		p.reconstructActive()
		p.insertHTMLElement(tok)
		p.pop()
		if !hiddenInput(tok) {
			p.framesetOK = false
		}
	case "param", "source", "track":
		p.insertHTMLElement(tok)
		p.pop()
	case "hr":
		p.closePInButtonScope()
		if p.inScope("select", defaultScope) {
			p.generateImpliedEndTags("")
		}
		p.insertHTMLElement(tok)
		p.pop()
		p.framesetOK = false
	case "image":
		tok.name = "img"
		return true
	case "textarea":
		p.insertHTMLElement(tok)
		p.skipLF = true
		p.z.setState(rcdataState)
		p.originalMode = p.mode
		p.framesetOK = false
		p.mode = textMode
	case "xmp":
		p.closePInButtonScope()
		p.reconstructActive()
		p.framesetOK = false
		p.parseText(tok, rawtextState)
	case "iframe":
		p.framesetOK = false
		p.parseText(tok, rawtextState)
	case "noembed":
		p.parseText(tok, rawtextState)
	case "noscript":
		if p.scripting {
			p.parseText(tok, rawtextState)
		} else {
			p.reconstructActive()
			p.insertHTMLElement(tok)
		}
	case "select":
		switch {
		case p.context.isHTML("select"):
		case p.inScope("select", defaultScope):
			p.popUntil("select")
		default:
			p.reconstructActive()
			p.insertHTMLElement(tok)
			p.framesetOK = false
		}
	case "option":
		if p.inScope("select", defaultScope) {
			p.generateImpliedEndTags("optgroup")
		} else if p.currentNode().isHTML("option") {
			p.pop()
		}
		p.reconstructActive()
		p.insertHTMLElement(tok)
	case "optgroup":
		if p.inScope("select", defaultScope) {
			p.generateImpliedEndTags("")
		} else if p.currentNode().isHTML("option") {
			p.pop()
		}
		p.reconstructActive()
		p.insertHTMLElement(tok)
	case "rb", "rtc":
		if p.inScope("ruby", defaultScope) {
			p.generateImpliedEndTags("")
		}
		p.insertHTMLElement(tok)
	case "rp", "rt":
		if p.inScope("ruby", defaultScope) {
			p.generateImpliedEndTags("rtc")
		}
		p.insertHTMLElement(tok)
	case "math":
		p.reconstructActive()
		adjustMathMLAttrs(tok)
		adjustForeignAttrs(tok)
		p.insertForeignElement(tok, MathML)
		if tok.selfClosing {
			p.pop()
		}
	case "svg":
		p.reconstructActive()
		adjustSVGAttrs(tok)
		adjustForeignAttrs(tok)
		p.insertForeignElement(tok, SVG)
		if tok.selfClosing {
			p.pop()
		}
	case "caption", "col", "colgroup", "frame", "head", "tbody", "td", "tfoot", "th", "thead", "tr":
	default:
		p.reconstructActive()
		p.insertHTMLElement(tok)
	}
	return false
}

// addMissingAttrs adds to n each attribute of attrs that n does not have, as the html and body start tags in "in body"
// do.
func (p *parser) addMissingAttrs(n *Node, attrs []Attr) {
	for _, a := range attrs {
		p.step(1 + len(n.Attrs))
		if !slices.ContainsFunc(n.Attrs, func(b Attr) bool { return b.Name == a.Name }) {
			p.grow(1)
			n.Attrs = append(n.Attrs, a)
		}
	}
}

// closeListItem runs the loop of the li, dd and dt start tags in "in body": from the current node up, it closes the
// first element that item reports true for, unless a special element other than address, div and p comes first.
func (p *parser) closeListItem(item func(*Node) bool) {
	for i := len(p.stack) - 1; i >= 0; i-- {
		p.step(1)
		n := p.stack[i]
		if item(n) {
			p.generateImpliedEndTags(n.Name)
			p.popUntil(n.Name)
			return
		}
		if isSpecial(n) && !n.isHTML("address") && !n.isHTML("div") && !n.isHTML("p") {
			return
		}
	}
}

func (p *parser) inBodyEndTag(tok *token) bool {
	switch tok.name {
	case "template":
		return p.inHead(tok)
	case "body":
		if p.inScope("body", defaultScope) {
			p.mode = afterBodyMode
		}
	case "html":
		if p.inScope("body", defaultScope) {
			p.mode = afterBodyMode
			return true
		}
	case "address", "article", "aside", "blockquote", "button", "center", "details", "dialog", "dir", "div", "dl",
		"fieldset", "figcaption", "figure", "footer", "header", "hgroup", "listing", "main", "menu", "nav", "ol", "pre",
		"search", "section", "select", "summary", "ul",
		"applet", "marquee", "object":
		if !p.inScope(tok.name, defaultScope) {
			return false
		}
		p.generateImpliedEndTags("")
		p.popUntil(tok.name)
		switch tok.name {
		case "applet", "marquee", "object":
			p.clearActiveToMarker()
		}
	case "form":
		if !p.parsingTemplateContents() {
			n := p.form
			p.form = nil
			if n == nil || !p.nodeInScope(n, defaultScope) {
				return false
			}
			p.generateImpliedEndTags("")
			p.removeFromStack(p.stackIndex(n))
		} else if p.inScope("form", defaultScope) {
			p.generateImpliedEndTags("")
			p.popUntil("form")
		}
	case "p":
		if !p.inScope("p", buttonScope) {
			p.insertHTMLElement(&token{typ: startTagToken, name: "p"})
		}
		p.closeP()
	case "li":
		if p.inScope("li", listItemScope) {
			p.generateImpliedEndTags("li")
			p.popUntil("li")
		}
	case "dd", "dt":
		if p.inScope(tok.name, defaultScope) {
			p.generateImpliedEndTags(tok.name)
			p.popUntil(tok.name)
		}
	case "h1", "h2", "h3", "h4", "h5", "h6":
		if p.headingInScope() {
			p.generateImpliedEndTags("")
			p.popUntilHeading()
		}
	case "a", "b", "big", "code", "em", "font", "i", "nobr", "s", "small", "strike", "strong", "tt", "u":
		p.adoptionAgency(tok)
	case "br":
		p.reconstructActive()
		p.insertHTMLElement(&token{typ: startTagToken, name: "br"})
		p.pop()
		p.framesetOK = false
	default:
		p.anyOtherEndTag(tok.name)
	}
	return false
}

// anyOtherEndTag handles an end tag named name as "any other end tag" in "in body".
func (p *parser) anyOtherEndTag(name string) {
	for i := len(p.stack) - 1; i >= 0; i-- {
		p.step(1)
		n := p.stack[i]
		if n.isHTML(name) {
			p.generateImpliedEndTags(name)
			p.popTo(i)
			return
		}
		if isSpecial(n) {
			return
		}
	}
}

// adoptionAgency runs the adoption agency algorithm for tok (13.2.6.4.7). With no sanitizer configuration, the steps
// for elements that the sanitizer removes or replaces with their children do nothing, and the insertion target
// redirection map has no entry for the elements that the algorithm moves.
func (p *parser) adoptionAgency(tok *token) {
	subject := tok.name
	if n := p.currentNode(); n.isHTML(subject) && p.activeIndex(n) < 0 {
		p.pop()
		return
	}
	for range 8 {
		p.step(1)
		fi := -1
		for i := len(p.active) - 1; i >= 0 && p.active[i] != nil; i-- {
			p.step(1)
			if p.active[i].Name == subject {
				fi = i
				break
			}
		}
		if fi < 0 {
			p.anyOtherEndTag(subject)
			return
		}
		formatting := p.active[fi]
		si := p.stackIndex(formatting)
		if si < 0 {
			p.removeActive(fi)
			return
		}
		if !p.nodeInScope(formatting, defaultScope) {
			return
		}
		fbi := -1
		for i := si + 1; i < len(p.stack); i++ {
			p.step(1)
			if isSpecial(p.stack[i]) {
				fbi = i
				break
			}
		}
		if fbi < 0 {
			p.popTo(si)
			p.removeActive(fi)
			return
		}
		furthest := p.stack[fbi]
		common := p.stack[si-1]
		bookmark := fi // the index in active that the new element goes to
		bookmarkMoved := false
		last := furthest
		for inner, ni := 1, fbi; ; inner++ {
			p.step(1)
			ni--
			node := p.stack[ni]
			if node == formatting {
				break
			}
			ai := p.activeIndex(node)
			if inner > 3 && ai >= 0 {
				p.removeActive(ai)
				if ai < bookmark {
					bookmark--
				}
				ai = -1
			}
			if ai < 0 {
				p.removeFromStack(ni)
				fbi--
				continue
			}
			node = p.createElement(p.tokenFor(node), HTML)
			p.active[ai] = node
			p.stack[ni] = node
			if !bookmarkMoved {
				bookmark = ai + 1
				bookmarkMoved = true
			}
			if last.Parent != nil {
				last.Parent.RemoveChild(last)
			}
			node.AppendChild(last)
			last = node
		}
		parent, ref := p.appropriatePlace(common)
		parent, ref = p.adjusted(parent, ref)
		if last.Parent != nil {
			last.Parent.RemoveChild(last)
		}
		if !p.hostIncludingInclusiveAncestor(last, parent) && (parent.Type != DocumentNode || !hasElementChild(parent)) &&
			(ref == nil || ref.Parent == parent) {
			parent.InsertBefore(last, ref)
		}
		n := p.createElement(p.tokenFor(formatting), HTML)
		fi = p.activeIndex(formatting)
		p.removeActive(fi)
		if fi < bookmark {
			bookmark--
		}
		p.step(len(p.active) - bookmark)
		p.active = slices.Insert(p.active, bookmark, n)
		p.removeFromStack(si)
		fbi--
		p.step(len(p.stack) - fbi)
		p.stack = slices.Insert(p.stack, fbi+1, n)
		for c := furthest.FirstChild; c != nil; c = furthest.FirstChild {
			p.step(1)
			furthest.RemoveChild(c)
			n.AppendChild(c)
		}
		parent, ref = p.adjusted(furthest, nil)
		parent.InsertBefore(n, ref)
	}
}
