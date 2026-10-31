package html

// The insertion modes of 13.2.6.4 but "in body", the table modes and "in template". Each processes a token and reports
// whether it must be reprocessed; it may have changed the token, such as the characters left to reprocess.

// isASCIIWhitespace reports whether c is tab, LF, FF, CR or space, which the insertion modes take as whitespace.
func isASCIIWhitespace(c byte) bool {
	return c == '\t' || c == '\n' || c == '\f' || c == '\r' || c == ' '
}

// splitSpace splits s into its leading whitespace and the rest.
func splitSpace(s string) (space, rest string) {
	i := 0
	for i < len(s) && isASCIIWhitespace(s[i]) {
		i++
	}
	return s[:i], s[i:]
}

// onlySpace returns the whitespace characters of s.
func onlySpace(s string) string {
	if space, rest := splitSpace(s); rest == "" {
		return space
	}
	b := make([]byte, 0, len(s))
	for i := range len(s) {
		if isASCIIWhitespace(s[i]) {
			b = append(b, s[i])
		}
	}
	return string(b)
}

// 13.2.6.4.1
func (p *parser) initial(tok *token) bool {
	switch tok.typ {
	case characterToken:
		_, rest := splitSpace(tok.data)
		if rest == "" {
			return false
		}
		tok.data = rest
	case commentToken:
		p.insertComment(tok.data, p.doc, nil)
		return false
	case processingInstructionToken:
		p.insertProcessingInstruction(tok, p.doc, nil)
		return false
	case doctypeToken:
		if !hasDoctypeOrElementChild(p.doc) {
			p.doc.AppendChild(&Node{Type: DoctypeNode, Name: tok.name, PublicID: tok.publicID, SystemID: tok.systemID})
		}
		p.docMode = doctypeMode(tok)
		p.mode = beforeHTMLMode
		return false
	case startTagToken, endTagToken, eofToken:
	}
	p.docMode = quirks
	p.mode = beforeHTMLMode
	return true
}

func hasDoctypeOrElementChild(n *Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == DoctypeNode || c.Type == ElementNode {
			return true
		}
	}
	return false
}

// quirkyPublicIDs are the public identifiers that set a document to quirks mode, and quirkyPublicIDPrefixes the
// prefixes that do (13.2.6.4.1), but those that depend on the system identifier.
var (
	quirkyPublicIDs = [...]string{
		"-//W3O//DTD W3 HTML Strict 3.0//EN//",
		"-/W3C/DTD HTML 4.0 Transitional/EN",
		"HTML",
	}
	quirkyPublicIDPrefixes = [...]string{
		"+//Silmaril//dtd html Pro v0r11 19970101//",
		"-//AS//DTD HTML 3.0 asWedit + extensions//",
		"-//AdvaSoft Ltd//DTD HTML 3.0 asWedit + extensions//",
		"-//IETF//DTD HTML 2.0 Level 1//",
		"-//IETF//DTD HTML 2.0 Level 2//",
		"-//IETF//DTD HTML 2.0 Strict Level 1//",
		"-//IETF//DTD HTML 2.0 Strict Level 2//",
		"-//IETF//DTD HTML 2.0 Strict//",
		"-//IETF//DTD HTML 2.0//",
		"-//IETF//DTD HTML 2.1E//",
		"-//IETF//DTD HTML 3.0//",
		"-//IETF//DTD HTML 3.2 Final//",
		"-//IETF//DTD HTML 3.2//",
		"-//IETF//DTD HTML 3//",
		"-//IETF//DTD HTML Level 0//",
		"-//IETF//DTD HTML Level 1//",
		"-//IETF//DTD HTML Level 2//",
		"-//IETF//DTD HTML Level 3//",
		"-//IETF//DTD HTML Strict Level 0//",
		"-//IETF//DTD HTML Strict Level 1//",
		"-//IETF//DTD HTML Strict Level 2//",
		"-//IETF//DTD HTML Strict Level 3//",
		"-//IETF//DTD HTML Strict//",
		"-//IETF//DTD HTML//",
		"-//Metrius//DTD Metrius Presentational//",
		"-//Microsoft//DTD Internet Explorer 2.0 HTML Strict//",
		"-//Microsoft//DTD Internet Explorer 2.0 HTML//",
		"-//Microsoft//DTD Internet Explorer 2.0 Tables//",
		"-//Microsoft//DTD Internet Explorer 3.0 HTML Strict//",
		"-//Microsoft//DTD Internet Explorer 3.0 HTML//",
		"-//Microsoft//DTD Internet Explorer 3.0 Tables//",
		"-//Netscape Comm. Corp.//DTD HTML//",
		"-//Netscape Comm. Corp.//DTD Strict HTML//",
		"-//O'Reilly and Associates//DTD HTML 2.0//",
		"-//O'Reilly and Associates//DTD HTML Extended 1.0//",
		"-//O'Reilly and Associates//DTD HTML Extended Relaxed 1.0//",
		"-//SQ//DTD HTML 2.0 HoTMetaL + extensions//",
		"-//SoftQuad Software//DTD HoTMetaL PRO 6.0::19990601::extensions to HTML 4.0//",
		"-//SoftQuad//DTD HoTMetaL PRO 4.0::19971010::extensions to HTML 4.0//",
		"-//Spyglass//DTD HTML 2.0 Extended//",
		"-//Sun Microsystems Corp.//DTD HotJava HTML//",
		"-//Sun Microsystems Corp.//DTD HotJava Strict HTML//",
		"-//W3C//DTD HTML 3 1995-03-24//",
		"-//W3C//DTD HTML 3.2 Draft//",
		"-//W3C//DTD HTML 3.2 Final//",
		"-//W3C//DTD HTML 3.2//",
		"-//W3C//DTD HTML 3.2S Draft//",
		"-//W3C//DTD HTML 4.0 Frameset//",
		"-//W3C//DTD HTML 4.0 Transitional//",
		"-//W3C//DTD HTML Experimental 19960712//",
		"-//W3C//DTD HTML Experimental 970421//",
		"-//W3C//DTD W3 HTML//",
		"-//W3O//DTD W3 HTML 3.0//",
		"-//WebTechs//DTD Mozilla HTML 2.0//",
		"-//WebTechs//DTD Mozilla HTML//",
	}
)

// doctypeMode returns the mode that the DOCTYPE token tok sets the document to (13.2.6.4.1). The identifiers are
// compared ASCII case-insensitively; a missing one, which is the empty string in tok, matches none of the lists.
func doctypeMode(tok *token) documentMode {
	if tok.forceQuirks || tok.name != "html" {
		return quirks
	}
	pub, sys := tok.publicID, tok.systemID
	for _, id := range quirkyPublicIDs {
		if foldEqual(pub, id) {
			return quirks
		}
	}
	if foldEqual(sys, "http://www.ibm.com/data/dtd/v11/ibmxhtml1-transitional.dtd") {
		return quirks
	}
	for _, prefix := range quirkyPublicIDPrefixes {
		if foldPrefix(pub, prefix) {
			return quirks
		}
	}
	html401 := foldPrefix(pub, "-//W3C//DTD HTML 4.01 Frameset//") ||
		foldPrefix(pub, "-//W3C//DTD HTML 4.01 Transitional//")
	if html401 && sys == "" {
		return quirks
	}
	if foldPrefix(pub, "-//W3C//DTD XHTML 1.0 Frameset//") || foldPrefix(pub, "-//W3C//DTD XHTML 1.0 Transitional//") ||
		html401 {
		return limitedQuirks
	}
	return noQuirks
}

func foldByte(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 0x20
	}
	return c
}

// foldEqual reports whether a and b are an ASCII case-insensitive match.
func foldEqual(a, b string) bool {
	return len(a) == len(b) && foldPrefix(a, b)
}

// foldPrefix reports whether s starts with prefix, ASCII case-insensitively.
func foldPrefix(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := range len(prefix) {
		if foldByte(s[i]) != foldByte(prefix[i]) {
			return false
		}
	}
	return true
}

// 13.2.6.4.2
func (p *parser) beforeHTML(tok *token) bool {
	switch tok.typ {
	case doctypeToken:
		return false
	case commentToken:
		p.insertComment(tok.data, p.doc, nil)
		return false
	case processingInstructionToken:
		p.insertProcessingInstruction(tok, p.doc, nil)
		return false
	case characterToken:
		_, rest := splitSpace(tok.data)
		if rest == "" {
			return false
		}
		tok.data = rest
	case startTagToken:
		if tok.name == "html" {
			n := p.createElement(tok, HTML)
			p.insertElement(n, p.doc, nil)
			p.push(n)
			p.mode = beforeHeadMode
			return false
		}
	case endTagToken:
		switch tok.name {
		case "head", "body", "html", "br":
		default:
			return false
		}
	case eofToken:
	}
	n := p.createElement(&token{typ: startTagToken, name: "html"}, HTML)
	p.insertElement(n, p.doc, nil)
	p.push(n)
	p.mode = beforeHeadMode
	return true
}

// 13.2.6.4.3
func (p *parser) beforeHead(tok *token) bool {
	switch tok.typ {
	case characterToken:
		_, rest := splitSpace(tok.data)
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
		case "head":
			p.head = p.insertHTMLElement(tok)
			p.mode = inHeadMode
			return false
		}
	case endTagToken:
		switch tok.name {
		case "head", "body", "html", "br":
		default:
			return false
		}
	case eofToken:
	}
	p.head = p.insertHTMLElement(&token{typ: startTagToken, name: "head"})
	p.mode = inHeadMode
	return true
}

// 13.2.6.4.4
func (p *parser) inHead(tok *token) bool {
	switch tok.typ {
	case characterToken:
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
		case "base", "basefont", "bgsound", "link", "meta":
			// Character encoding declarations of meta do not apply: the input is a Go string.
			p.insertHTMLElement(tok)
			p.pop()
			return false
		case "title":
			p.parseText(tok, rcdataState)
			return false
		case "noscript":
			if p.scripting {
				p.parseText(tok, rawtextState)
			} else {
				p.insertHTMLElement(tok)
				p.mode = inHeadNoscriptMode
			}
			return false
		case "noframes", "style":
			p.parseText(tok, rawtextState)
			return false
		case "script":
			p.insertHTMLElement(tok)
			p.z.setState(scriptDataState)
			p.originalMode = p.mode
			p.mode = textMode
			return false
		case "template":
			p.templateStart(tok)
			return false
		case "head":
			return false
		}
	case endTagToken:
		switch tok.name {
		case "head":
			p.pop()
			p.mode = afterHeadMode
			return false
		case "body", "html", "br":
		case "template":
			p.templateEnd()
			return false
		default:
			return false
		}
	case eofToken:
	}
	p.pop()
	p.mode = afterHeadMode
	return true
}

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

// 13.2.6.4.5
func (p *parser) inHeadNoscript(tok *token) bool {
	switch tok.typ {
	case doctypeToken:
		return false
	case startTagToken:
		switch tok.name {
		case "html":
			return p.inBody(tok)
		case "basefont", "bgsound", "link", "meta", "noframes", "style":
			return p.inHead(tok)
		case "head", "noscript":
			return false
		}
	case endTagToken:
		switch tok.name {
		case "noscript":
			p.pop()
			p.mode = inHeadMode
			return false
		case "br":
		default:
			return false
		}
	case characterToken:
		space, rest := splitSpace(tok.data)
		if space != "" {
			p.inHead(&token{typ: characterToken, data: space})
		}
		if rest == "" {
			return false
		}
		tok.data = rest
	case commentToken, processingInstructionToken:
		return p.inHead(tok)
	case eofToken:
	}
	p.pop()
	p.mode = inHeadMode
	return true
}

// 13.2.6.4.6
func (p *parser) afterHead(tok *token) bool {
	switch tok.typ {
	case characterToken:
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
		case "body":
			p.insertHTMLElement(tok)
			p.framesetOK = false
			p.mode = inBodyMode
			return false
		case "frameset":
			p.insertHTMLElement(tok)
			p.mode = inFramesetMode
			return false
		case "base", "basefont", "bgsound", "link", "meta", "noframes", "script", "style", "template", "title":
			p.push(p.head)
			again := p.inHead(tok)
			p.removeFromStack(p.stackIndex(p.head))
			return again
		case "head":
			return false
		}
	case endTagToken:
		switch tok.name {
		case "template":
			return p.inHead(tok)
		case "body", "html", "br":
		default:
			return false
		}
	case eofToken:
	}
	p.insertHTMLElement(&token{typ: startTagToken, name: "body"})
	p.framesetOK = true
	p.mode = inBodyMode
	return true
}

// 13.2.6.4.8
func (p *parser) text(tok *token) bool {
	switch tok.typ {
	case characterToken:
		p.insertText(tok.data)
	case eofToken:
		p.pop()
		p.mode = p.originalMode
		return true
	case endTagToken:
		p.pop()
		p.mode = p.originalMode
	case doctypeToken, startTagToken, commentToken, processingInstructionToken:
		// The tokenizer emits none of these in the states that go with this mode.
	}
	return false
}

// 13.2.6.4.17
func (p *parser) afterBody(tok *token) bool {
	switch tok.typ {
	case characterToken:
		space, rest := splitSpace(tok.data)
		if space != "" {
			p.inBody(&token{typ: characterToken, data: space})
		}
		if rest == "" {
			return false
		}
		tok.data = rest
	case commentToken:
		p.insertComment(tok.data, p.stack[0], nil)
		return false
	case processingInstructionToken:
		p.insertProcessingInstruction(tok, p.stack[0], nil)
		return false
	case doctypeToken:
		return false
	case startTagToken:
		if tok.name == "html" {
			return p.inBody(tok)
		}
	case endTagToken:
		if tok.name == "html" {
			if p.context == nil {
				p.mode = afterAfterBodyMode
			}
			return false
		}
	case eofToken:
		return false
	}
	p.mode = inBodyMode
	return true
}

// 13.2.6.4.18
func (p *parser) inFrameset(tok *token) bool {
	switch tok.typ {
	case characterToken:
		if s := onlySpace(tok.data); s != "" {
			p.insertText(s)
		}
	case commentToken:
		p.insertComment(tok.data, nil, nil)
	case processingInstructionToken:
		p.insertProcessingInstruction(tok, nil, nil)
	case doctypeToken:
	case startTagToken:
		switch tok.name {
		case "html":
			return p.inBody(tok)
		case "frameset":
			p.insertHTMLElement(tok)
		case "frame":
			p.insertHTMLElement(tok)
			p.pop()
		case "noframes":
			return p.inHead(tok)
		}
	case endTagToken:
		if tok.name == "frameset" && len(p.stack) > 1 {
			p.pop()
			if p.context == nil && !p.currentNode().isHTML("frameset") {
				p.mode = afterFramesetMode
			}
		}
	case eofToken:
	}
	return false
}

// 13.2.6.4.19
func (p *parser) afterFrameset(tok *token) bool {
	switch tok.typ {
	case characterToken:
		if s := onlySpace(tok.data); s != "" {
			p.insertText(s)
		}
	case commentToken:
		p.insertComment(tok.data, nil, nil)
	case processingInstructionToken:
		p.insertProcessingInstruction(tok, nil, nil)
	case doctypeToken:
	case startTagToken:
		switch tok.name {
		case "html":
			return p.inBody(tok)
		case "noframes":
			return p.inHead(tok)
		}
	case endTagToken:
		if tok.name == "html" {
			p.mode = afterAfterFramesetMode
		}
	case eofToken:
	}
	return false
}

// 13.2.6.4.20
func (p *parser) afterAfterBody(tok *token) bool {
	switch tok.typ {
	case commentToken:
		p.insertComment(tok.data, p.doc, nil)
		return false
	case processingInstructionToken:
		p.insertProcessingInstruction(tok, p.doc, nil)
		return false
	case doctypeToken:
		return p.inBody(tok)
	case characterToken:
		space, rest := splitSpace(tok.data)
		if space != "" {
			p.inBody(&token{typ: characterToken, data: space})
		}
		if rest == "" {
			return false
		}
		tok.data = rest
	case startTagToken:
		if tok.name == "html" {
			return p.inBody(tok)
		}
	case endTagToken:
	case eofToken:
		return false
	}
	p.mode = inBodyMode
	return true
}

// 13.2.6.4.21
func (p *parser) afterAfterFrameset(tok *token) bool {
	switch tok.typ {
	case commentToken:
		p.insertComment(tok.data, p.doc, nil)
	case processingInstructionToken:
		p.insertProcessingInstruction(tok, p.doc, nil)
	case doctypeToken:
		return p.inBody(tok)
	case characterToken:
		if s := onlySpace(tok.data); s != "" {
			return p.inBody(&token{typ: characterToken, data: s})
		}
	case startTagToken:
		switch tok.name {
		case "html":
			return p.inBody(tok)
		case "noframes":
			return p.inHead(tok)
		}
	case endTagToken, eofToken:
	}
	return false
}
