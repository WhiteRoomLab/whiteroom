package html

import (
	"strings"
	"unicode/utf8"
)

// state is a state of the tokenizer, numbered as the sections 13.2.5.1-84 that define them.
type state uint8

const (
	dataState state = iota + 1
	rcdataState
	rawtextState
	scriptDataState
	plaintextState
	tagOpenState
	endTagOpenState
	tagNameState
	rcdataLessThanSignState
	rcdataEndTagOpenState
	rcdataEndTagNameState
	rawtextLessThanSignState
	rawtextEndTagOpenState
	rawtextEndTagNameState
	scriptDataLessThanSignState
	scriptDataEndTagOpenState
	scriptDataEndTagNameState
	scriptDataEscapeStartState
	scriptDataEscapeStartDashState
	scriptDataEscapedState
	scriptDataEscapedDashState
	scriptDataEscapedDashDashState
	scriptDataEscapedLessThanSignState
	scriptDataEscapedEndTagOpenState
	scriptDataEscapedEndTagNameState
	scriptDataDoubleEscapeStartState
	scriptDataDoubleEscapedState
	scriptDataDoubleEscapedDashState
	scriptDataDoubleEscapedDashDashState
	scriptDataDoubleEscapedLessThanSignState
	scriptDataDoubleEscapeEndState
	beforeAttributeNameState
	attributeNameState
	afterAttributeNameState
	beforeAttributeValueState
	attributeValueDoubleQuotedState
	attributeValueSingleQuotedState
	attributeValueUnquotedState
	afterAttributeValueQuotedState
	selfClosingStartTagState
	bogusCommentState
	markupDeclarationOpenState
	commentStartState
	commentStartDashState
	commentState
	commentLessThanSignState
	commentLessThanSignBangState
	commentLessThanSignBangDashState
	commentLessThanSignBangDashDashState
	commentEndDashState
	commentEndState
	commentEndBangState
	doctypeState
	beforeDoctypeNameState
	doctypeNameState
	afterDoctypeNameState
	afterDoctypePublicKeywordState
	beforeDoctypePublicIdentifierState
	doctypePublicIdentifierDoubleQuotedState
	doctypePublicIdentifierSingleQuotedState
	afterDoctypePublicIdentifierState
	betweenDoctypePublicAndSystemIdentifiersState
	afterDoctypeSystemKeywordState
	beforeDoctypeSystemIdentifierState
	doctypeSystemIdentifierDoubleQuotedState
	doctypeSystemIdentifierSingleQuotedState
	afterDoctypeSystemIdentifierState
	bogusDoctypeState
	cdataSectionState
	cdataSectionBracketState
	cdataSectionEndState
	processingInstructionOpenState
	processingInstructionTargetState
	afterProcessingInstructionTargetState
	processingInstructionDataState
	processingInstructionQuestionableState
	characterReferenceState
	namedCharacterReferenceState
	ambiguousAmpersandState
	numericCharacterReferenceState
	hexadecimalCharacterReferenceStartState
	hexadecimalCharacterReferenceState
	decimalCharacterReferenceState
	numericCharacterReferenceEndState
)

// eof is what consume returns at the end of the input.
const eof = -1

const replacement = string(utf8.RuneError)

// manyAttrs is the number of attributes of a tag above which duplicate names are found with a map.
const manyAttrs = 16

// tokenizer is the tokenization stage of 13.2.5. It reports no parse errors.
//
// It consumes the input a byte at a time: every state treats the bytes of a non-ASCII character as "anything else",
// so they stay together and in order in whatever the character is appended to.
type tokenizer struct {
	s   string // the input, preprocessed
	pos int    // the offset of the next input character

	state       state
	returnState state

	// lastStartTag is the tag name of the last start tag emitted, which an appropriate end tag token matches; "" when
	// there is none.
	lastStartTag string
	// cdataAllowed reports whether there is an adjusted current node that is not an element in the HTML namespace,
	// which the markup declaration open state asks at "[CDATA["; nil reports false.
	cdataAllowed func() bool

	text []byte // the characters emitted after the last other token
	tmp  []byte // the temporary buffer

	// The current tag, comment, DOCTYPE or processing instruction token.
	tagType     tokenType
	name        []byte // tag name or DOCTYPE name
	attrs       []Attr
	attrNames   map[string]struct{} // the names of attrs, once there are more than manyAttrs
	attrOpen    bool                // the current attribute is not in attrs yet
	attrName    []byte
	attrKey     string // the name of the current attribute, once the attribute name state is left
	attrDropped bool   // the current attribute repeats a name and is not added
	attrValue   []byte
	selfClosing bool
	data        []byte // comment or processing instruction data
	target      string // processing instruction target
	publicID    []byte
	systemID    []byte
	hasPublicID bool
	hasSystemID bool
	forceQuirks bool
	code        int // the character reference code

	out   [2]token // emitted tokens not yet returned: characters, then another token
	nout  int
	ended bool // an end-of-file token was emitted
}

func newTokenizer(s string) *tokenizer {
	return &tokenizer{s: preprocess(s), state: dataState}
}

// preprocess normalizes newlines as 13.2.3.5 says: CR LF and CR become LF. Each byte that is not part of valid UTF-8
// becomes U+FFFD.
func preprocess(s string) string {
	if strings.IndexByte(s, '\r') < 0 && utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == '\r':
			b.WriteByte('\n')
			i++
			if i < len(s) && s[i] == '\n' {
				i++
			}
		case c < utf8.RuneSelf:
			b.WriteByte(c)
			i++
		default:
			r, n := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && n == 1 {
				b.WriteString(replacement)
			} else {
				b.WriteString(s[i : i+n])
			}
			i += n
		}
	}
	return b.String()
}

// setState switches the tokenizer to s: the tree construction stage switches to the data, RCDATA, RAWTEXT, script data
// and PLAINTEXT states.
func (z *tokenizer) setState(s state) {
	z.state = s
}

// next returns the next token. The characters up to the next other token are one character token, but those before a
// "<![CDATA[" are returned before cdataAllowed is called. At the end of the input it returns an end-of-file token, and
// so on every later call.
func (z *tokenizer) next() token {
	for z.nout == 0 && !z.ended {
		z.step()
	}
	if z.nout == 0 {
		z.flushText()
		if z.nout == 0 {
			return token{typ: eofToken}
		}
	}
	t := z.out[0]
	z.out[0], z.out[1] = z.out[1], token{}
	z.nout--
	return t
}

func (z *tokenizer) consume() int {
	if z.pos >= len(z.s) {
		return eof
	}
	c := z.s[z.pos]
	z.pos++
	return int(c)
}

// reconsume switches to s, which consumes c again.
func (z *tokenizer) reconsume(c int, s state) {
	if c != eof {
		z.pos--
	}
	z.state = s
}

// appendRun appends the current input character and the input up to the next a, b or c to buf, and consumes it.
func (z *tokenizer) appendRun(buf []byte, a, b, c byte) []byte {
	i := z.pos
	for i < len(z.s) && z.s[i] != a && z.s[i] != b && z.s[i] != c {
		i++
	}
	buf = append(buf, z.s[z.pos-1:i]...)
	z.pos = i
	return buf
}

func (z *tokenizer) emit(t token) {
	z.flushText()
	z.out[z.nout] = t
	z.nout++
}

func (z *tokenizer) flushText() {
	if len(z.text) > 0 {
		z.out[z.nout] = token{typ: characterToken, data: string(z.text)}
		z.nout++
		z.text = z.text[:0]
	}
}

func (z *tokenizer) newTag(typ tokenType) {
	z.tagType = typ
	z.name = z.name[:0]
	z.attrs, z.attrNames = nil, nil
	z.attrOpen = false
	z.selfClosing = false
}

// startAttr starts a new attribute in the current tag token.
func (z *tokenizer) startAttr() {
	z.addAttr()
	z.attrOpen = true
	z.attrName = z.attrName[:0]
	z.attrKey = ""
	z.attrDropped = false
	z.attrValue = z.attrValue[:0]
}

// leaveAttrName runs when the tokenizer leaves the attribute name state: if the tag already has an attribute of the
// same name, the current one is removed (13.2.5.33), though its value is still consumed.
func (z *tokenizer) leaveAttrName() {
	z.attrKey = string(z.attrName)
	z.attrDropped = z.hasAttr(z.attrKey)
}

func (z *tokenizer) hasAttr(name string) bool {
	if z.attrNames != nil {
		_, ok := z.attrNames[name]
		return ok
	}
	for _, a := range z.attrs {
		if a.Name == name {
			return true
		}
	}
	return false
}

// addAttr adds the current attribute, if any, to the current tag token.
func (z *tokenizer) addAttr() {
	if !z.attrOpen {
		return
	}
	z.attrOpen = false
	if z.attrDropped {
		return
	}
	z.attrs = append(z.attrs, Attr{Name: z.attrKey, Value: string(z.attrValue)})
	switch {
	case z.attrNames != nil:
		z.attrNames[z.attrKey] = struct{}{}
	case len(z.attrs) > manyAttrs:
		z.attrNames = make(map[string]struct{}, 2*len(z.attrs))
		for _, a := range z.attrs {
			z.attrNames[a.Name] = struct{}{}
		}
	}
}

func (z *tokenizer) emitTag() {
	z.addAttr()
	t := token{typ: z.tagType, name: string(z.name), attrs: z.attrs, selfClosing: z.selfClosing}
	z.attrs, z.attrNames = nil, nil
	if t.typ == startTagToken {
		z.lastStartTag = t.name
	}
	z.emit(t)
}

// appropriate reports whether the current end tag token is an appropriate end tag token.
func (z *tokenizer) appropriate() bool {
	return z.lastStartTag != "" && string(z.name) == z.lastStartTag
}

func (z *tokenizer) emitComment() {
	z.emit(token{typ: commentToken, data: string(z.data)})
}

// tmpToComment converts the temporary buffer to a comment.
func (z *tokenizer) tmpToComment() {
	z.data = append(append(z.data[:0], '?'), z.tmp...)
}

func (z *tokenizer) emitProcessingInstruction() {
	z.emit(token{typ: processingInstructionToken, name: z.target, data: string(z.data)})
}

func (z *tokenizer) newDoctype() {
	z.name = z.name[:0]
	z.publicID, z.systemID = z.publicID[:0], z.systemID[:0]
	z.hasPublicID, z.hasSystemID, z.forceQuirks = false, false, false
}

// emitDoctype emits the current DOCTYPE token, after setting its force-quirks flag to on if quirks is true.
func (z *tokenizer) emitDoctype(quirks bool) {
	if quirks {
		z.forceQuirks = true
	}
	z.emit(token{
		typ:         doctypeToken,
		name:        string(z.name),
		publicID:    string(z.publicID),
		systemID:    string(z.systemID),
		hasPublicID: z.hasPublicID,
		hasSystemID: z.hasSystemID,
		forceQuirks: z.forceQuirks,
	})
}

// step runs the current state once.
func (z *tokenizer) step() {
	switch z.state {
	case dataState:
		switch c := z.consume(); c {
		case '&':
			z.returnState = dataState
			z.state = characterReferenceState
		case '<':
			z.state = tagOpenState
		case eof:
			z.ended = true
		default:
			z.text = z.appendRun(z.text, '&', '<', '<')
		}
	case rcdataState:
		switch c := z.consume(); c {
		case '&':
			z.returnState = rcdataState
			z.state = characterReferenceState
		case '<':
			z.state = rcdataLessThanSignState
		case 0:
			z.text = append(z.text, replacement...)
		case eof:
			z.ended = true
		default:
			z.text = z.appendRun(z.text, '&', '<', 0)
		}
	case rawtextState:
		switch c := z.consume(); c {
		case '<':
			z.state = rawtextLessThanSignState
		case 0:
			z.text = append(z.text, replacement...)
		case eof:
			z.ended = true
		default:
			z.text = z.appendRun(z.text, '<', 0, 0)
		}
	case scriptDataState:
		switch c := z.consume(); c {
		case '<':
			z.state = scriptDataLessThanSignState
		case 0:
			z.text = append(z.text, replacement...)
		case eof:
			z.ended = true
		default:
			z.text = z.appendRun(z.text, '<', 0, 0)
		}
	case plaintextState:
		switch c := z.consume(); c {
		case 0:
			z.text = append(z.text, replacement...)
		case eof:
			z.ended = true
		default:
			z.text = z.appendRun(z.text, 0, 0, 0)
		}
	case tagOpenState:
		switch c := z.consume(); {
		case c == '!':
			z.state = markupDeclarationOpenState
		case c == '/':
			z.state = endTagOpenState
		case isAlpha(c):
			z.newTag(startTagToken)
			z.reconsume(c, tagNameState)
		case c == '?':
			z.tmp = z.tmp[:0]
			z.state = processingInstructionOpenState
		case c == eof:
			z.text = append(z.text, '<')
			z.ended = true
		default:
			z.text = append(z.text, '<')
			z.reconsume(c, dataState)
		}
	case endTagOpenState:
		switch c := z.consume(); {
		case isAlpha(c):
			z.newTag(endTagToken)
			z.reconsume(c, tagNameState)
		case c == '>':
			z.state = dataState
		case c == eof:
			z.text = append(z.text, "</"...)
			z.ended = true
		default:
			z.data = z.data[:0]
			z.reconsume(c, bogusCommentState)
		}
	case tagNameState:
		switch c := z.consume(); {
		case isSpace(c):
			z.state = beforeAttributeNameState
		case c == '/':
			z.state = selfClosingStartTagState
		case c == '>':
			z.state = dataState
			z.emitTag()
		case isUpper(c):
			z.name = append(z.name, byte(c+0x20))
		case c == 0:
			z.name = append(z.name, replacement...)
		case c == eof:
			z.ended = true
		default:
			z.name = append(z.name, byte(c))
		}
	case rcdataLessThanSignState:
		z.lessThanSign(rcdataEndTagOpenState, rcdataState)
	case rcdataEndTagOpenState:
		z.endTagOpen(rcdataEndTagNameState, rcdataState)
	case rcdataEndTagNameState:
		z.endTagName(rcdataState)
	case rawtextLessThanSignState:
		z.lessThanSign(rawtextEndTagOpenState, rawtextState)
	case rawtextEndTagOpenState:
		z.endTagOpen(rawtextEndTagNameState, rawtextState)
	case rawtextEndTagNameState:
		z.endTagName(rawtextState)
	case scriptDataLessThanSignState:
		switch c := z.consume(); c {
		case '/':
			z.tmp = z.tmp[:0]
			z.state = scriptDataEndTagOpenState
		case '!':
			z.state = scriptDataEscapeStartState
			z.text = append(z.text, "<!"...)
		default:
			z.text = append(z.text, '<')
			z.reconsume(c, scriptDataState)
		}
	case scriptDataEndTagOpenState:
		z.endTagOpen(scriptDataEndTagNameState, scriptDataState)
	case scriptDataEndTagNameState:
		z.endTagName(scriptDataState)
	case scriptDataEscapeStartState:
		if c := z.consume(); c == '-' {
			z.state = scriptDataEscapeStartDashState
			z.text = append(z.text, '-')
		} else {
			z.reconsume(c, scriptDataState)
		}
	case scriptDataEscapeStartDashState:
		if c := z.consume(); c == '-' {
			z.state = scriptDataEscapedDashDashState
			z.text = append(z.text, '-')
		} else {
			z.reconsume(c, scriptDataState)
		}
	case scriptDataEscapedState:
		switch c := z.consume(); c {
		case '-':
			z.state = scriptDataEscapedDashState
			z.text = append(z.text, '-')
		case '<':
			z.state = scriptDataEscapedLessThanSignState
		case 0:
			z.text = append(z.text, replacement...)
		case eof:
			z.ended = true
		default:
			z.text = z.appendRun(z.text, '-', '<', 0)
		}
	case scriptDataEscapedDashState:
		switch c := z.consume(); c {
		case '-':
			z.state = scriptDataEscapedDashDashState
			z.text = append(z.text, '-')
		case '<':
			z.state = scriptDataEscapedLessThanSignState
		case 0:
			z.state = scriptDataEscapedState
			z.text = append(z.text, replacement...)
		case eof:
			z.ended = true
		default:
			z.state = scriptDataEscapedState
			z.text = append(z.text, byte(c))
		}
	case scriptDataEscapedDashDashState:
		switch c := z.consume(); c {
		case '-':
			z.text = append(z.text, '-')
		case '<':
			z.state = scriptDataEscapedLessThanSignState
		case '>':
			z.state = scriptDataState
			z.text = append(z.text, '>')
		case 0:
			z.state = scriptDataEscapedState
			z.text = append(z.text, replacement...)
		case eof:
			z.ended = true
		default:
			z.state = scriptDataEscapedState
			z.text = append(z.text, byte(c))
		}
	case scriptDataEscapedLessThanSignState:
		switch c := z.consume(); {
		case c == '/':
			z.tmp = z.tmp[:0]
			z.state = scriptDataEscapedEndTagOpenState
		case isAlpha(c):
			z.tmp = z.tmp[:0]
			z.text = append(z.text, '<')
			z.reconsume(c, scriptDataDoubleEscapeStartState)
		default:
			z.text = append(z.text, '<')
			z.reconsume(c, scriptDataEscapedState)
		}
	case scriptDataEscapedEndTagOpenState:
		z.endTagOpen(scriptDataEscapedEndTagNameState, scriptDataEscapedState)
	case scriptDataEscapedEndTagNameState:
		z.endTagName(scriptDataEscapedState)
	case scriptDataDoubleEscapeStartState:
		z.doubleEscape(scriptDataDoubleEscapedState, scriptDataEscapedState)
	case scriptDataDoubleEscapedState:
		switch c := z.consume(); c {
		case '-':
			z.state = scriptDataDoubleEscapedDashState
			z.text = append(z.text, '-')
		case '<':
			z.state = scriptDataDoubleEscapedLessThanSignState
			z.text = append(z.text, '<')
		case 0:
			z.text = append(z.text, replacement...)
		case eof:
			z.ended = true
		default:
			z.text = z.appendRun(z.text, '-', '<', 0)
		}
	case scriptDataDoubleEscapedDashState:
		switch c := z.consume(); c {
		case '-':
			z.state = scriptDataDoubleEscapedDashDashState
			z.text = append(z.text, '-')
		case '<':
			z.state = scriptDataDoubleEscapedLessThanSignState
			z.text = append(z.text, '<')
		case 0:
			z.state = scriptDataDoubleEscapedState
			z.text = append(z.text, replacement...)
		case eof:
			z.ended = true
		default:
			z.state = scriptDataDoubleEscapedState
			z.text = append(z.text, byte(c))
		}
	case scriptDataDoubleEscapedDashDashState:
		switch c := z.consume(); c {
		case '-':
			z.text = append(z.text, '-')
		case '<':
			z.state = scriptDataDoubleEscapedLessThanSignState
			z.text = append(z.text, '<')
		case '>':
			z.state = scriptDataState
			z.text = append(z.text, '>')
		case 0:
			z.state = scriptDataDoubleEscapedState
			z.text = append(z.text, replacement...)
		case eof:
			z.ended = true
		default:
			z.state = scriptDataDoubleEscapedState
			z.text = append(z.text, byte(c))
		}
	case scriptDataDoubleEscapedLessThanSignState:
		if c := z.consume(); c == '/' {
			z.tmp = z.tmp[:0]
			z.state = scriptDataDoubleEscapeEndState
			z.text = append(z.text, '/')
		} else {
			z.reconsume(c, scriptDataDoubleEscapedState)
		}
	case scriptDataDoubleEscapeEndState:
		z.doubleEscape(scriptDataEscapedState, scriptDataDoubleEscapedState)
	case beforeAttributeNameState:
		switch c := z.consume(); {
		case isSpace(c):
		case c == '/' || c == '>' || c == eof:
			z.reconsume(c, afterAttributeNameState)
		case c == '=':
			z.startAttr()
			z.attrName = append(z.attrName, '=')
			z.state = attributeNameState
		default:
			z.startAttr()
			z.reconsume(c, attributeNameState)
		}
	case attributeNameState:
		switch c := z.consume(); {
		case isSpace(c) || c == '/' || c == '>' || c == eof:
			z.leaveAttrName()
			z.reconsume(c, afterAttributeNameState)
		case c == '=':
			z.leaveAttrName()
			z.state = beforeAttributeValueState
		case isUpper(c):
			z.attrName = append(z.attrName, byte(c+0x20))
		case c == 0:
			z.attrName = append(z.attrName, replacement...)
		default:
			z.attrName = append(z.attrName, byte(c))
		}
	case afterAttributeNameState:
		switch c := z.consume(); {
		case isSpace(c):
		case c == '/':
			z.state = selfClosingStartTagState
		case c == '=':
			z.state = beforeAttributeValueState
		case c == '>':
			z.state = dataState
			z.emitTag()
		case c == eof:
			z.ended = true
		default:
			z.startAttr()
			z.reconsume(c, attributeNameState)
		}
	case beforeAttributeValueState:
		switch c := z.consume(); {
		case isSpace(c):
		case c == '"':
			z.state = attributeValueDoubleQuotedState
		case c == '\'':
			z.state = attributeValueSingleQuotedState
		case c == '>':
			z.state = dataState
			z.emitTag()
		default:
			z.reconsume(c, attributeValueUnquotedState)
		}
	case attributeValueDoubleQuotedState:
		z.quotedValue('"', attributeValueDoubleQuotedState)
	case attributeValueSingleQuotedState:
		z.quotedValue('\'', attributeValueSingleQuotedState)
	case attributeValueUnquotedState:
		switch c := z.consume(); {
		case isSpace(c):
			z.state = beforeAttributeNameState
		case c == '&':
			z.returnState = attributeValueUnquotedState
			z.state = characterReferenceState
		case c == '>':
			z.state = dataState
			z.emitTag()
		case c == 0:
			z.attrValue = append(z.attrValue, replacement...)
		case c == eof:
			z.ended = true
		default:
			z.attrValue = append(z.attrValue, byte(c))
		}
	case afterAttributeValueQuotedState:
		switch c := z.consume(); {
		case isSpace(c):
			z.state = beforeAttributeNameState
		case c == '/':
			z.state = selfClosingStartTagState
		case c == '>':
			z.state = dataState
			z.emitTag()
		case c == eof:
			z.ended = true
		default:
			z.reconsume(c, beforeAttributeNameState)
		}
	case selfClosingStartTagState:
		switch c := z.consume(); c {
		case '>':
			z.selfClosing = true
			z.state = dataState
			z.emitTag()
		case eof:
			z.ended = true
		default:
			z.reconsume(c, beforeAttributeNameState)
		}
	case bogusCommentState:
		switch c := z.consume(); c {
		case '>':
			z.state = dataState
			z.emitComment()
		case eof:
			z.emitComment()
			z.ended = true
		case 0:
			z.data = append(z.data, replacement...)
		default:
			z.data = z.appendRun(z.data, '>', 0, 0)
		}
	case markupDeclarationOpenState:
		z.markupDeclarationOpen()
	case commentStartState:
		switch c := z.consume(); c {
		case '-':
			z.state = commentStartDashState
		case '>':
			z.state = dataState
			z.emitComment()
		default:
			z.reconsume(c, commentState)
		}
	case commentStartDashState:
		switch c := z.consume(); c {
		case '-':
			z.state = commentEndState
		case '>':
			z.state = dataState
			z.emitComment()
		case eof:
			z.emitComment()
			z.ended = true
		default:
			z.data = append(z.data, '-')
			z.reconsume(c, commentState)
		}
	case commentState:
		switch c := z.consume(); c {
		case '<':
			z.data = append(z.data, '<')
			z.state = commentLessThanSignState
		case '-':
			z.state = commentEndDashState
		case 0:
			z.data = append(z.data, replacement...)
		case eof:
			z.emitComment()
			z.ended = true
		default:
			z.data = z.appendRun(z.data, '<', '-', 0)
		}
	case commentLessThanSignState:
		switch c := z.consume(); c {
		case '!':
			z.data = append(z.data, '!')
			z.state = commentLessThanSignBangState
		case '<':
			z.data = append(z.data, '<')
		default:
			z.reconsume(c, commentState)
		}
	case commentLessThanSignBangState:
		if c := z.consume(); c == '-' {
			z.state = commentLessThanSignBangDashState
		} else {
			z.reconsume(c, commentState)
		}
	case commentLessThanSignBangDashState:
		if c := z.consume(); c == '-' {
			z.state = commentLessThanSignBangDashDashState
		} else {
			z.reconsume(c, commentEndDashState)
		}
	case commentLessThanSignBangDashDashState:
		// Every character, EOF too, is reconsumed in the comment end state.
		z.state = commentEndState
	case commentEndDashState:
		switch c := z.consume(); c {
		case '-':
			z.state = commentEndState
		case eof:
			z.emitComment()
			z.ended = true
		default:
			z.data = append(z.data, '-')
			z.reconsume(c, commentState)
		}
	case commentEndState:
		switch c := z.consume(); c {
		case '>':
			z.state = dataState
			z.emitComment()
		case '!':
			z.state = commentEndBangState
		case '-':
			z.data = append(z.data, '-')
		case eof:
			z.emitComment()
			z.ended = true
		default:
			z.data = append(z.data, "--"...)
			z.reconsume(c, commentState)
		}
	case commentEndBangState:
		switch c := z.consume(); c {
		case '-':
			z.data = append(z.data, "--!"...)
			z.state = commentEndDashState
		case '>':
			z.state = dataState
			z.emitComment()
		case eof:
			z.emitComment()
			z.ended = true
		default:
			z.data = append(z.data, "--!"...)
			z.reconsume(c, commentState)
		}
	case doctypeState:
		switch c := z.consume(); {
		case isSpace(c):
			z.state = beforeDoctypeNameState
		case c == eof:
			z.newDoctype()
			z.emitDoctype(true)
			z.ended = true
		default:
			// '>' too
			z.reconsume(c, beforeDoctypeNameState)
		}
	case beforeDoctypeNameState:
		switch c := z.consume(); {
		case isSpace(c):
		case isUpper(c):
			z.newDoctype()
			z.name = append(z.name, byte(c+0x20))
			z.state = doctypeNameState
		case c == 0:
			z.newDoctype()
			z.name = append(z.name, replacement...)
			z.state = doctypeNameState
		case c == '>':
			z.newDoctype()
			z.state = dataState
			z.emitDoctype(true)
		case c == eof:
			z.newDoctype()
			z.emitDoctype(true)
			z.ended = true
		default:
			z.newDoctype()
			z.name = append(z.name, byte(c))
			z.state = doctypeNameState
		}
	case doctypeNameState:
		switch c := z.consume(); {
		case isSpace(c):
			z.state = afterDoctypeNameState
		case c == '>':
			z.state = dataState
			z.emitDoctype(false)
		case isUpper(c):
			z.name = append(z.name, byte(c+0x20))
		case c == 0:
			z.name = append(z.name, replacement...)
		case c == eof:
			z.emitDoctype(true)
			z.ended = true
		default:
			z.name = append(z.name, byte(c))
		}
	case afterDoctypeNameState:
		switch c := z.consume(); {
		case isSpace(c):
		case c == '>':
			z.state = dataState
			z.emitDoctype(false)
		case c == eof:
			z.emitDoctype(true)
			z.ended = true
		case hasPrefixFold(z.s[z.pos-1:], "public"):
			z.pos += len("public") - 1
			z.state = afterDoctypePublicKeywordState
		case hasPrefixFold(z.s[z.pos-1:], "system"):
			z.pos += len("system") - 1
			z.state = afterDoctypeSystemKeywordState
		default:
			z.forceQuirks = true
			z.reconsume(c, bogusDoctypeState)
		}
	case afterDoctypePublicKeywordState:
		z.afterDoctypeKeyword(beforeDoctypePublicIdentifierState, false)
	case beforeDoctypePublicIdentifierState:
		z.beforeDoctypeIdentifier(false)
	case doctypePublicIdentifierDoubleQuotedState:
		z.doctypeIdentifier(&z.publicID, '"', afterDoctypePublicIdentifierState)
	case doctypePublicIdentifierSingleQuotedState:
		z.doctypeIdentifier(&z.publicID, '\'', afterDoctypePublicIdentifierState)
	case afterDoctypePublicIdentifierState:
		switch c := z.consume(); {
		case isSpace(c):
			z.state = betweenDoctypePublicAndSystemIdentifiersState
		case c == '>':
			z.state = dataState
			z.emitDoctype(false)
		case c == '"' || c == '\'':
			z.openDoctypeIdentifier(c, true)
		case c == eof:
			z.emitDoctype(true)
			z.ended = true
		default:
			z.forceQuirks = true
			z.reconsume(c, bogusDoctypeState)
		}
	case betweenDoctypePublicAndSystemIdentifiersState:
		switch c := z.consume(); {
		case isSpace(c):
		case c == '>':
			z.state = dataState
			z.emitDoctype(false)
		case c == '"' || c == '\'':
			z.openDoctypeIdentifier(c, true)
		case c == eof:
			z.emitDoctype(true)
			z.ended = true
		default:
			z.forceQuirks = true
			z.reconsume(c, bogusDoctypeState)
		}
	case afterDoctypeSystemKeywordState:
		z.afterDoctypeKeyword(beforeDoctypeSystemIdentifierState, true)
	case beforeDoctypeSystemIdentifierState:
		z.beforeDoctypeIdentifier(true)
	case doctypeSystemIdentifierDoubleQuotedState:
		z.doctypeIdentifier(&z.systemID, '"', afterDoctypeSystemIdentifierState)
	case doctypeSystemIdentifierSingleQuotedState:
		z.doctypeIdentifier(&z.systemID, '\'', afterDoctypeSystemIdentifierState)
	case afterDoctypeSystemIdentifierState:
		switch c := z.consume(); {
		case isSpace(c):
		case c == '>':
			z.state = dataState
			z.emitDoctype(false)
		case c == eof:
			z.emitDoctype(true)
			z.ended = true
		default:
			// This does not set the force-quirks flag.
			z.reconsume(c, bogusDoctypeState)
		}
	case bogusDoctypeState:
		switch z.consume() {
		case '>':
			z.state = dataState
			z.emitDoctype(false)
		case eof:
			z.emitDoctype(false)
			z.ended = true
		}
	case cdataSectionState:
		switch c := z.consume(); c {
		case ']':
			z.state = cdataSectionBracketState
		case eof:
			z.ended = true
		default:
			z.text = z.appendRun(z.text, ']', ']', ']')
		}
	case cdataSectionBracketState:
		if c := z.consume(); c == ']' {
			z.state = cdataSectionEndState
		} else {
			z.text = append(z.text, ']')
			z.reconsume(c, cdataSectionState)
		}
	case cdataSectionEndState:
		switch c := z.consume(); c {
		case ']':
			z.text = append(z.text, ']')
		case '>':
			z.state = dataState
		default:
			z.text = append(z.text, "]]"...)
			z.reconsume(c, cdataSectionState)
		}
	case processingInstructionOpenState:
		switch c := z.consume(); {
		case isAlpha(c) || c == '_':
			z.reconsume(c, processingInstructionTargetState)
		case c == eof:
			z.ended = true
		default:
			z.tmpToComment()
			z.reconsume(c, bogusCommentState)
		}
	case processingInstructionTargetState:
		switch c := z.consume(); {
		case isSpace(c) || c == '?' || c == '>':
			if target := string(z.tmp); equalFold(target, "xml") || equalFold(target, "xml-stylesheet") {
				z.tmpToComment()
				z.reconsume(c, bogusCommentState)
			} else {
				z.target = target
				z.data = z.data[:0]
				z.reconsume(c, afterProcessingInstructionTargetState)
			}
		case isAlnum(c) || c == '-' || c == '_':
			z.tmp = append(z.tmp, byte(c))
		case c == eof:
			z.ended = true
		default:
			z.tmpToComment()
			z.reconsume(c, bogusCommentState)
		}
	case afterProcessingInstructionTargetState:
		if c := z.consume(); !isSpace(c) {
			z.reconsume(c, processingInstructionDataState)
		}
	case processingInstructionDataState:
		switch c := z.consume(); c {
		case '?':
			z.state = processingInstructionQuestionableState
		case '>':
			z.state = dataState
			z.emitProcessingInstruction()
		case eof:
			z.ended = true
		default:
			z.data = z.appendRun(z.data, '?', '>', '>')
		}
	case processingInstructionQuestionableState:
		switch c := z.consume(); c {
		case '>':
			z.state = dataState
			z.emitProcessingInstruction()
		case eof:
			z.ended = true
		default:
			z.data = append(z.data, '?')
			z.reconsume(c, processingInstructionDataState)
		}
	case characterReferenceState:
		z.characterReference()
	case namedCharacterReferenceState:
		z.namedCharacterReference()
	case ambiguousAmpersandState:
		z.ambiguousAmpersand()
	case numericCharacterReferenceState:
		z.numericCharacterReference()
	case hexadecimalCharacterReferenceStartState:
		z.hexadecimalCharacterReferenceStart()
	case hexadecimalCharacterReferenceState:
		z.hexadecimalCharacterReference()
	case decimalCharacterReferenceState:
		z.decimalCharacterReference()
	case numericCharacterReferenceEndState:
		z.numericCharacterReferenceEnd()
	default:
		panic("html: tokenizer in no state")
	}
}

// lessThanSign is the RCDATA (13.2.5.9) and RAWTEXT (13.2.5.12) less-than sign state.
func (z *tokenizer) lessThanSign(endTagOpen, back state) {
	if c := z.consume(); c == '/' {
		z.tmp = z.tmp[:0]
		z.state = endTagOpen
	} else {
		z.text = append(z.text, '<')
		z.reconsume(c, back)
	}
}

// endTagOpen is the RCDATA (13.2.5.10), RAWTEXT (13.2.5.13), script data (13.2.5.16) and script data escaped
// (13.2.5.24) end tag open state.
func (z *tokenizer) endTagOpen(endTagName, back state) {
	if c := z.consume(); isAlpha(c) {
		z.newTag(endTagToken)
		z.reconsume(c, endTagName)
	} else {
		z.text = append(z.text, "</"...)
		z.reconsume(c, back)
	}
}

// endTagName is the RCDATA (13.2.5.11), RAWTEXT (13.2.5.14), script data (13.2.5.17) and script data escaped
// (13.2.5.25) end tag name state.
func (z *tokenizer) endTagName(back state) {
	switch c := z.consume(); {
	case isSpace(c) && z.appropriate():
		z.state = beforeAttributeNameState
	case c == '/' && z.appropriate():
		z.state = selfClosingStartTagState
	case c == '>' && z.appropriate():
		z.state = dataState
		z.emitTag()
	case isUpper(c):
		z.name = append(z.name, byte(c+0x20))
		z.tmp = append(z.tmp, byte(c))
	case isLower(c):
		z.name = append(z.name, byte(c))
		z.tmp = append(z.tmp, byte(c))
	default:
		z.text = append(z.text, "</"...)
		z.text = append(z.text, z.tmp...)
		z.reconsume(c, back)
	}
}

// doubleEscape is the script data double escape start (13.2.5.26) and end (13.2.5.31) state, which switch to script
// after the name "script" and to other after other names.
func (z *tokenizer) doubleEscape(script, other state) {
	switch c := z.consume(); {
	case isSpace(c) || c == '/' || c == '>':
		if string(z.tmp) == "script" {
			z.state = script
		} else {
			z.state = other
		}
		z.text = append(z.text, byte(c))
	case isUpper(c):
		z.tmp = append(z.tmp, byte(c+0x20))
		z.text = append(z.text, byte(c))
	case isLower(c):
		z.tmp = append(z.tmp, byte(c))
		z.text = append(z.text, byte(c))
	default:
		z.reconsume(c, other)
	}
}

// quotedValue is the attribute value (double-quoted) (13.2.5.36) and (single-quoted) (13.2.5.37) state.
func (z *tokenizer) quotedValue(quote byte, self state) {
	switch c := z.consume(); c {
	case int(quote):
		z.state = afterAttributeValueQuotedState
	case '&':
		z.returnState = self
		z.state = characterReferenceState
	case 0:
		z.attrValue = append(z.attrValue, replacement...)
	case eof:
		z.ended = true
	default:
		z.attrValue = z.appendRun(z.attrValue, quote, '&', 0)
	}
}

func (z *tokenizer) markupDeclarationOpen() {
	rest := z.s[z.pos:]
	switch {
	case strings.HasPrefix(rest, "--"):
		z.pos += len("--")
		z.data = z.data[:0]
		z.state = commentStartState
	case hasPrefixFold(rest, "doctype"):
		z.pos += len("doctype")
		z.state = doctypeState
	case strings.HasPrefix(rest, "[CDATA["):
		if len(z.text) > 0 {
			// The tree construction stage handles the characters before, which can change the adjusted current node.
			z.flushText()
			return
		}
		z.pos += len("[CDATA[")
		if z.cdataAllowed != nil && z.cdataAllowed() {
			z.state = cdataSectionState
		} else {
			z.data = append(z.data[:0], "[CDATA["...)
			z.state = bogusCommentState
		}
	default:
		z.data = z.data[:0]
		z.state = bogusCommentState
	}
}

// openDoctypeIdentifier sets the public or system identifier of the current DOCTYPE token to the empty string and
// switches to its state for the quote c.
func (z *tokenizer) openDoctypeIdentifier(c int, system bool) {
	if system {
		z.hasSystemID, z.systemID = true, z.systemID[:0]
		z.state = doctypeSystemIdentifierDoubleQuotedState
		if c == '\'' {
			z.state = doctypeSystemIdentifierSingleQuotedState
		}
	} else {
		z.hasPublicID, z.publicID = true, z.publicID[:0]
		z.state = doctypePublicIdentifierDoubleQuotedState
		if c == '\'' {
			z.state = doctypePublicIdentifierSingleQuotedState
		}
	}
}

// afterDoctypeKeyword is the after DOCTYPE public keyword (13.2.5.57) and the after DOCTYPE system keyword (13.2.5.63)
// state.
func (z *tokenizer) afterDoctypeKeyword(before state, system bool) {
	switch c := z.consume(); {
	case isSpace(c):
		z.state = before
	case c == '"' || c == '\'':
		z.openDoctypeIdentifier(c, system)
	case c == '>':
		z.state = dataState
		z.emitDoctype(true)
	case c == eof:
		z.emitDoctype(true)
		z.ended = true
	default:
		z.forceQuirks = true
		z.reconsume(c, bogusDoctypeState)
	}
}

// beforeDoctypeIdentifier is the before DOCTYPE public identifier (13.2.5.58) and the before DOCTYPE system
// identifier (13.2.5.64) state.
func (z *tokenizer) beforeDoctypeIdentifier(system bool) {
	switch c := z.consume(); {
	case isSpace(c):
	case c == '"' || c == '\'':
		z.openDoctypeIdentifier(c, system)
	case c == '>':
		z.state = dataState
		z.emitDoctype(true)
	case c == eof:
		z.emitDoctype(true)
		z.ended = true
	default:
		z.forceQuirks = true
		z.reconsume(c, bogusDoctypeState)
	}
}

// doctypeIdentifier is a DOCTYPE public or system identifier state (13.2.5.59, 60, 65, 66), which appends to id.
func (z *tokenizer) doctypeIdentifier(id *[]byte, quote byte, after state) {
	switch c := z.consume(); c {
	case int(quote):
		z.state = after
	case 0:
		*id = append(*id, replacement...)
	case '>':
		z.state = dataState
		z.emitDoctype(true)
	case eof:
		z.emitDoctype(true)
		z.ended = true
	default:
		*id = append(*id, byte(c))
	}
}

func isSpace(c int) bool { return c == '\t' || c == '\n' || c == '\f' || c == ' ' }
func isUpper(c int) bool { return 'A' <= c && c <= 'Z' }
func isLower(c int) bool { return 'a' <= c && c <= 'z' }
func isAlpha(c int) bool { return isUpper(c) || isLower(c) }
func isDigit(c int) bool { return '0' <= c && c <= '9' }
func isAlnum(c int) bool { return isAlpha(c) || isDigit(c) }

// equalFold reports whether s is an ASCII case-insensitive match for lower, which is in lowercase.
func equalFold(s, lower string) bool {
	if len(s) != len(lower) {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if isUpper(int(c)) {
			c += 0x20
		}
		if c != lower[i] {
			return false
		}
	}
	return true
}

func hasPrefixFold(s, lower string) bool {
	return len(s) >= len(lower) && equalFold(s[:len(lower)], lower)
}
