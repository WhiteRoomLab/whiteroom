package html

// templateStart handles a template start tag in the "in head" insertion mode. Declarative shadow roots are not
// allowed, and content patching (the for attribute) is not supported, so the template element is always inserted.
func (p *parser) templateStart(tok *token) {
	p.insertMarker()
	p.framesetOK = false
	p.mode = inTemplateMode
	p.templateModes = append(p.templateModes, inTemplateMode)
	t := p.createElement(tok, HTML)
	parent, ref := p.appropriatePlace(nil)
	p.push(t)
	p.insertElement(t, parent, ref)
}

// templateEnd handles a template end tag in the "in head" insertion mode.
func (p *parser) templateEnd() {
	if !p.templateOnStack() {
		return
	}
	p.generateAllImpliedEndTagsThoroughly()
	p.popUntil("template")
	p.clearActiveToMarker()
	p.templateModes = p.templateModes[:len(p.templateModes)-1]
	p.resetInsertionMode()
}

// 13.2.6.4.16
func (p *parser) inTemplate(tok *token) bool {
	switch tok.typ {
	case characterToken, commentToken, processingInstructionToken, doctypeToken:
		return p.inBody(tok)
	case startTagToken:
		m := inBodyMode
		switch tok.name {
		case "base", "basefont", "bgsound", "link", "meta", "noframes", "script", "style", "template", "title":
			return p.inHead(tok)
		case "caption", "colgroup", "tbody", "tfoot", "thead":
			m = inTableMode
		case "col":
			m = inColumnGroupMode
		case "tr":
			m = inTableBodyMode
		case "td", "th":
			m = inRowMode
		}
		p.templateModes[len(p.templateModes)-1] = m
		p.mode = m
		return true
	case endTagToken:
		if tok.name == "template" {
			return p.inHead(tok)
		}
	case eofToken:
		if !p.templateOnStack() {
			return false
		}
		p.popUntil("template")
		p.clearActiveToMarker()
		p.templateModes = p.templateModes[:len(p.templateModes)-1]
		p.resetInsertionMode()
		return true
	}
	return false
}
