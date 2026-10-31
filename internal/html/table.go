package html

import (
	"slices"
	"strings"
)

// The table insertion modes (13.2.6.4.9-15).

// 13.2.6.4.9
func (p *parser) inTable(tok *token) bool {
	switch tok.typ {
	case characterToken:
		switch n := p.currentNode(); {
		case n.isHTML("table"), n.isHTML("tbody"), n.isHTML("template"), n.isHTML("tfoot"), n.isHTML("thead"),
			n.isHTML("tr"):
			p.pendingTable = p.pendingTable[:0]
			p.originalMode = p.mode
			p.mode = inTableTextMode
			return true
		}
	case commentToken:
		p.insertComment(tok.data, nil, nil)
		return false
	case processingInstructionToken:
		p.insertProcessingInstruction(tok, nil, nil)
		return false
	case doctypeToken:
		return false
	case startTagToken:
		switch tok.name {
		case "caption":
			p.clearStackBack("table")
			p.insertMarker()
			p.insertHTMLElement(tok)
			p.mode = inCaptionMode
			return false
		case "colgroup":
			p.clearStackBack("table")
			p.insertHTMLElement(tok)
			p.mode = inColumnGroupMode
			return false
		case "col":
			p.clearStackBack("table")
			p.insertHTMLElement(&token{typ: startTagToken, name: "colgroup"})
			p.mode = inColumnGroupMode
			return true
		case "tbody", "tfoot", "thead":
			p.clearStackBack("table")
			p.insertHTMLElement(tok)
			p.mode = inTableBodyMode
			return false
		case "td", "th", "tr":
			p.clearStackBack("table")
			p.insertHTMLElement(&token{typ: startTagToken, name: "tbody"})
			p.mode = inTableBodyMode
			return true
		case "table":
			if !p.inScope("table", tableScope) {
				return false
			}
			p.popUntil("table")
			p.resetInsertionMode()
			return true
		case "style", "script", "template":
			return p.inHead(tok)
		case "input":
			if hiddenInput(tok) {
				p.insertHTMLElement(tok)
				p.pop()
				return false
			}
		case "form":
			contents := p.parsingTemplateContents()
			if p.form != nil && !contents {
				return false
			}
			n := p.insertHTMLElement(tok)
			if !contents {
				p.form = n
			}
			p.pop()
			return false
		}
	case endTagToken:
		switch tok.name {
		case "table":
			if p.inScope("table", tableScope) {
				p.popUntil("table")
				p.resetInsertionMode()
			}
			return false
		case "body", "caption", "col", "colgroup", "html", "tbody", "td", "tfoot", "th", "thead", "tr":
			return false
		case "template":
			return p.inHead(tok)
		}
	case eofToken:
		return p.inBody(tok)
	}
	return p.inBodyFostered(tok)
}

// inBodyFostered processes tok by the rules of "in body" with foster parenting enabled ("anything else" of "in table").
func (p *parser) inBodyFostered(tok *token) bool {
	p.fosterParenting = true
	again := p.inBody(tok)
	p.fosterParenting = false
	return again
}

// hiddenInput reports whether tok has a type attribute whose value is an ASCII case-insensitive match for "hidden".
func hiddenInput(tok *token) bool {
	i := slices.IndexFunc(tok.attrs, func(a Attr) bool { return a.Name == "type" })
	return i >= 0 && equalFold(tok.attrs[i].Value, "hidden")
}

// clearStackBack pops elements from the stack of open elements while the current node is not an HTML element named
// html, template or one of names: it clears the stack back to a table context ("table"), a table body context ("tbody",
// "tfoot", "thead") or a table row context ("tr").
func (p *parser) clearStackBack(names ...string) {
	for {
		p.step(1)
		n := p.currentNode()
		if n.Namespace == HTML && (n.Name == "html" || n.Name == "template" || slices.Contains(names, n.Name)) {
			return
		}
		p.pop()
	}
}

// 13.2.6.4.10
func (p *parser) inTableText(tok *token) bool {
	if tok.typ == characterToken {
		s := tok.data
		if strings.IndexByte(s, 0) >= 0 {
			s = strings.ReplaceAll(s, "\x00", "")
		}
		p.pendingTable = append(p.pendingTable, s...)
		return false
	}
	if len(p.pendingTable) > 0 {
		s := string(p.pendingTable)
		p.pendingTable = p.pendingTable[:0]
		if _, rest := splitSpace(s); rest != "" {
			p.inBodyFostered(&token{typ: characterToken, data: s})
		} else {
			p.insertText(s)
		}
	}
	p.mode = p.originalMode
	return true
}

// 13.2.6.4.11
func (p *parser) inCaption(tok *token) bool {
	switch tok.typ {
	case startTagToken:
		switch tok.name {
		case "caption", "col", "colgroup", "tbody", "td", "tfoot", "th", "thead", "tr":
			return p.closeCaption()
		}
	case endTagToken:
		switch tok.name {
		case "caption":
			p.closeCaption()
			return false
		case "table":
			return p.closeCaption()
		case "body", "col", "colgroup", "html", "tbody", "td", "tfoot", "th", "thead", "tr":
			return false
		}
	case characterToken, commentToken, processingInstructionToken, doctypeToken, eofToken:
	}
	return p.inBody(tok)
}

// closeCaption ends the caption as "in caption" does, if a caption element is in table scope, and reports whether it
// did.
func (p *parser) closeCaption() bool {
	if !p.inScope("caption", tableScope) {
		return false
	}
	p.generateImpliedEndTags("")
	p.popUntil("caption")
	p.clearActiveToMarker()
	p.mode = inTableMode
	return true
}

// 13.2.6.4.12
func (p *parser) inColumnGroup(tok *token) bool {
	switch tok.typ {
	case characterToken:
		if !p.currentNode().isHTML("colgroup") {
			// Whitespace is inserted; every other character is ignored by "anything else".
			if s := onlySpace(tok.data); s != "" {
				p.insertText(s)
			}
			return false
		}
		space, rest := splitSpace(tok.data)
		if space != "" {
			p.insertText(space)
		}
		if rest == "" {
			return false
		}
		tok.data = rest
	case commentToken:
		p.insertComment(tok.data, nil, nil)
		return false
	case processingInstructionToken:
		p.insertProcessingInstruction(tok, nil, nil)
		return false
	case doctypeToken:
		return false
	case startTagToken:
		switch tok.name {
		case "html":
			return p.inBody(tok)
		case "col":
			p.insertHTMLElement(tok)
			p.pop()
			return false
		case "template":
			return p.inHead(tok)
		}
	case endTagToken:
		switch tok.name {
		case "colgroup":
			if p.currentNode().isHTML("colgroup") {
				p.pop()
				p.mode = inTableMode
			}
			return false
		case "col":
			return false
		case "template":
			return p.inHead(tok)
		}
	case eofToken:
		return p.inBody(tok)
	}
	if !p.currentNode().isHTML("colgroup") {
		return false
	}
	p.pop()
	p.mode = inTableMode
	return true
}

// 13.2.6.4.13
func (p *parser) inTableBody(tok *token) bool {
	switch tok.typ {
	case startTagToken:
		switch tok.name {
		case "tr":
			p.clearStackBack("tbody", "tfoot", "thead")
			p.insertHTMLElement(tok)
			p.mode = inRowMode
			return false
		case "th", "td":
			p.clearStackBack("tbody", "tfoot", "thead")
			p.insertHTMLElement(&token{typ: startTagToken, name: "tr"})
			p.mode = inRowMode
			return true
		case "caption", "col", "colgroup", "tbody", "tfoot", "thead":
			return p.closeTableSection()
		}
	case endTagToken:
		switch tok.name {
		case "tbody", "tfoot", "thead":
			if p.inScope(tok.name, tableScope) {
				p.clearStackBack("tbody", "tfoot", "thead")
				p.pop()
				p.mode = inTableMode
			}
			return false
		case "table":
			return p.closeTableSection()
		case "body", "caption", "col", "colgroup", "html", "td", "th", "tr":
			return false
		}
	case characterToken, commentToken, processingInstructionToken, doctypeToken, eofToken:
	}
	return p.inTable(tok)
}

// closeTableSection ends the tbody, thead or tfoot element as "in table body" does, if one is in table scope, and
// reports whether it did.
func (p *parser) closeTableSection() bool {
	if !p.tableSectionInScope() {
		return false
	}
	p.clearStackBack("tbody", "tfoot", "thead")
	p.pop()
	p.mode = inTableMode
	return true
}

// tableSectionInScope reports whether the stack of open elements has a tbody, thead or tfoot element in table scope.
func (p *parser) tableSectionInScope() bool {
	for i := len(p.stack) - 1; i >= 0; i-- {
		p.step(1)
		n := p.stack[i]
		if n.isHTML("tbody") || n.isHTML("thead") || n.isHTML("tfoot") {
			return true
		}
		if tableScope.bounds(n) {
			return false
		}
	}
	return false
}

// 13.2.6.4.14
func (p *parser) inRow(tok *token) bool {
	switch tok.typ {
	case startTagToken:
		switch tok.name {
		case "th", "td":
			p.clearStackBack("tr")
			p.insertHTMLElement(tok)
			p.mode = inCellMode
			p.insertMarker()
			return false
		case "caption", "col", "colgroup", "tbody", "tfoot", "thead", "tr":
			return p.closeRow()
		}
	case endTagToken:
		switch tok.name {
		case "tr":
			p.closeRow()
			return false
		case "table":
			return p.closeRow()
		case "tbody", "tfoot", "thead":
			return p.inScope(tok.name, tableScope) && p.closeRow()
		case "body", "caption", "col", "colgroup", "html", "td", "th":
			return false
		}
	case characterToken, commentToken, processingInstructionToken, doctypeToken, eofToken:
	}
	return p.inTable(tok)
}

// closeRow ends the tr element as "in row" does, if a tr element is in table scope, and reports whether it did.
func (p *parser) closeRow() bool {
	if !p.inScope("tr", tableScope) {
		return false
	}
	p.clearStackBack("tr")
	p.pop()
	p.mode = inTableBodyMode
	return true
}

// 13.2.6.4.15
func (p *parser) inCell(tok *token) bool {
	switch tok.typ {
	case startTagToken:
		switch tok.name {
		case "caption", "col", "colgroup", "tbody", "td", "tfoot", "th", "thead", "tr":
			// The spec asserts a td or th element in table scope here, so there is no check.
			p.closeCell()
			return true
		}
	case endTagToken:
		switch tok.name {
		case "td", "th":
			if p.inScope(tok.name, tableScope) {
				p.generateImpliedEndTags("")
				p.popUntil(tok.name)
				p.clearActiveToMarker()
				p.mode = inRowMode
			}
			return false
		case "body", "caption", "col", "colgroup", "html":
			return false
		case "table", "tbody", "tfoot", "thead", "tr":
			if !p.inScope(tok.name, tableScope) {
				return false
			}
			p.closeCell()
			return true
		}
	case characterToken, commentToken, processingInstructionToken, doctypeToken, eofToken:
	}
	return p.inBody(tok)
}

// closeCell closes the cell (13.2.6.4.15).
func (p *parser) closeCell() {
	p.generateImpliedEndTags("")
	for i := len(p.stack) - 1; i >= 0; i-- {
		p.step(1)
		if n := p.stack[i]; n.isHTML("td") || n.isHTML("th") {
			p.popTo(i)
			break
		}
	}
	p.clearActiveToMarker()
	p.mode = inRowMode
}
