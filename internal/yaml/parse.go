package yaml

import (
	"encoding/json/jsontext"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxDepth  = 100
	maxKeyLen = 1024 // characters from the start of an implicit key to ":", as YAML limits them

	msgTab           = "tab characters are not allowed; use spaces"
	msgComment       = `a comment needs a space before "#"`
	msgMultiLine     = "multi-line values are not supported; write the value on one line"
	msgUnclosedQuote = "missing closing quote; write the quoted value on one line"
	msgIndent        = "unexpected indentation"

	removeKey  = "; remove the key"
	removeItem = "; remove the item"
	quoteHash  = `; a "#" after a space starts a comment, so quote a value that starts with "#", such as "#1e73be"`
)

type parser struct {
	lines []string
	ln    int // the current line, len(lines) at the end of the input
	off   int // the byte offset in the current line
	col   int // the column of off

	out []byte
	pos map[string]Position

	depth int

	// A line indented deeper than expected continues a plain scalar in YAML if the scalar ended its line and
	// only blank lines are between.
	plainEnd   int  // the line where the last plain scalar of a block value ended at the end of the line, or -1
	from       int  // the line where the last call of nextLine started
	sawComment bool // whether the last call of nextLine skipped a comment line
}

func (p *parser) eof() bool {
	return p.ln >= len(p.lines)
}

func (p *parser) line() string {
	if p.eof() {
		return ""
	}
	return p.lines[p.ln]
}

// at returns the byte k bytes after the cursor, or 0 after the end of the line.
func (p *parser) at(k int) byte {
	s := p.line()
	if p.off+k >= len(s) {
		return 0
	}
	return s[p.off+k]
}

func (p *parser) peek() byte {
	return p.at(0)
}

// blankAt reports whether the byte k bytes after the cursor is a space, a tab or the end of the line.
func (p *parser) blankAt(k int) bool {
	c := p.at(k)
	return c == 0 || c == ' ' || c == '\t'
}

// skip moves the cursor past n ASCII characters.
func (p *parser) skip(n int) {
	p.off += n
	p.col += n
}

// advance moves the cursor past one character.
func (p *parser) advance() {
	_, n := utf8.DecodeRuneInString(p.line()[p.off:])
	p.off += n
	p.col++
}

func (p *parser) here() Position {
	return Position{Line: p.ln + 1, Column: p.col}
}

func (p *parser) errorf(pos Position, format string, args ...any) error {
	return &Error{Position: pos, Msg: fmt.Sprintf(format, args...)}
}

func (p *parser) skipSpaces() error {
	for {
		switch p.peek() {
		case ' ':
			p.skip(1)
		case '\t':
			return p.errorf(p.here(), msgTab)
		default:
			return nil
		}
	}
}

// endLine checks that only spaces and a comment follow on the line, and returns an error with msg for other text.
// It reports whether there is a comment.
func (p *parser) endLine(msg string) (bool, error) {
	from := p.off
	if err := p.skipSpaces(); err != nil {
		return false, err
	}
	switch c := p.peek(); {
	case c == 0:
		return false, nil
	case c == '#' && p.off > from:
		return true, nil
	case c == '#':
		return false, p.errorf(p.here(), msgComment)
	}
	return false, p.errorf(p.here(), "%s", msg)
}

func indentOf(s string) int {
	n := 0
	for n < len(s) && s[n] == ' ' {
		n++
	}
	return n
}

// scanLine moves the cursor to the first character of the next line with content, or to the end of the input.
func (p *parser) scanLine() error {
	p.from, p.sawComment = p.ln, false
	for p.ln++; p.ln < len(p.lines); p.ln++ {
		s := p.lines[p.ln]
		ind := indentOf(s)
		switch {
		case ind == len(s):
			continue
		case s[ind] == '#':
			p.sawComment = true
			continue
		case s[ind] == '\t':
			return p.errorf(Position{Line: p.ln + 1, Column: ind + 1}, msgTab)
		}
		p.off, p.col = ind, ind+1
		return nil
	}
	p.off, p.col = 0, 1
	return nil
}

// nextLine is scanLine for lines in the document, where "---" and "..." are not allowed.
func (p *parser) nextLine() error {
	if err := p.scanLine(); err != nil {
		return err
	}
	return p.checkMarker()
}

func isMarker(s, marker string) bool {
	return strings.HasPrefix(s, marker) && (len(s) == 3 || s[3] == ' ' || s[3] == '\t')
}

// checkMarker returns an error if the cursor is at a "---" or "..." line.
func (p *parser) checkMarker() error {
	if p.eof() || p.off != 0 {
		return nil
	}
	switch s := p.line(); {
	case isMarker(s, "---"):
		return p.errorf(p.here(), `multiple documents are not supported; remove this "---"`)
	case isMarker(s, "..."):
		return p.errorf(p.here(), `document end markers ("...") are not supported; remove the line`)
	}
	return nil
}

func (p *parser) document() error {
	p.ln = -1
	if err := p.scanLine(); err != nil {
		return err
	}
	if !p.eof() && p.off == 0 {
		switch s := p.line(); {
		case s[0] == '%':
			return p.errorf(p.here(), "directives such as %%YAML are not supported; remove the line")
		case isMarker(s, "---"):
			p.skip(3)
			if _, err := p.endLine(`a value on the "---" line is not supported; start it on the next line`); err != nil {
				return err
			}
			if err := p.nextLine(); err != nil {
				return err
			}
		}
	}
	if err := p.checkMarker(); err != nil {
		return err
	}
	if p.eof() {
		return p.errorf(Position{Line: 1, Column: 1}, "empty document")
	}
	p.pos[""] = p.here()
	block, err := p.node("", -1, "")
	switch {
	case err != nil:
		return err
	case p.eof():
		return nil
	case block:
		return p.errorf(p.here(), msgIndent)
	case p.continues():
		return p.errorf(p.here(), msgMultiLine)
	}
	return p.errorf(p.here(), "unexpected text after the value")
}

// continues reports whether the line at the cursor would continue a plain scalar in YAML.
func (p *parser) continues() bool {
	if p.plainEnd < 0 || p.plainEnd != p.from || p.sawComment {
		return false
	}
	s, _, _ := strings.Cut(p.line()[p.off:], " #")
	looksLikeEntry := s == "-" || strings.HasPrefix(s, "- ") || strings.Contains(s, ": ") || strings.HasSuffix(s, ":")
	return !looksLikeEntry
}

// deeper returns the error for the line at the cursor, which is indented deeper than the items before it.
func (p *parser) deeper() error {
	if p.continues() {
		return p.errorf(p.here(), msgMultiLine)
	}
	return p.errorf(p.here(), msgIndent)
}

func (p *parser) enter(pos Position) error {
	if p.depth++; p.depth > maxDepth {
		return p.errorf(pos, "nested too deeply (max %d)", maxDepth)
	}
	return nil
}

func (p *parser) appendString(s string) {
	// s is valid UTF-8, so AppendQuote does not fail.
	p.out, _ = jsontext.AppendQuote(p.out, s)
}

var pointerEscaper = strings.NewReplacer("~", "~0", "/", "~1")

func member(ptr, key string) string {
	return ptr + "/" + pointerEscaper.Replace(key)
}

func item(ptr string, i int) string {
	return ptr + "/" + strconv.Itoa(i)
}

// node reads the block node at the cursor, which is at the start of a line or after "- ". Lines indented deeper
// than parent belong to the node. It reports whether the node is a block mapping or sequence.
func (p *parser) node(ptr string, parent int, remove string) (bool, error) {
	ind := p.col - 1
	switch c := p.peek(); {
	case c == '-' && p.blankAt(1):
		return true, p.sequence(ptr, ind, false)
	case c == '[' || c == '{':
		return false, p.flowValue(ptr, parent, true)
	}
	tok, err := p.scalar(false)
	if err != nil {
		return false, err
	}
	if p.keyColon() >= 0 {
		return true, p.mapping(ptr, ind, tok)
	}
	if n := p.colonAfterSpaces(); tok.quoted && n >= 0 {
		return false, p.errorf(Position{Line: p.ln + 1, Column: p.col + n}, `missing space after ":"`)
	}
	return false, p.scalarValue(tok, remove)
}

// keyColon returns the number of spaces between the cursor and a ":" that ends a key, or -1 if there is none.
func (p *parser) keyColon() int {
	s := p.line()
	j := p.off + indentOf(s[p.off:])
	if j < len(s) && s[j] == ':' && (j+1 == len(s) || s[j+1] == ' ' || s[j+1] == '\t') {
		return j - p.off
	}
	return -1
}

// colonAfterSpaces returns the number of spaces between the cursor and a following ":", or -1 if there is none.
func (p *parser) colonAfterSpaces() int {
	s := p.line()
	n := indentOf(s[p.off:])
	if p.off+n < len(s) && s[p.off+n] == ':' {
		return n
	}
	return -1
}

// scalarValue writes tok, a block value, and moves to the next line.
func (p *parser) scalarValue(tok token, remove string) error {
	if tok.quoted {
		p.appendString(tok.text)
		if _, err := p.endLine("unexpected text after the closing quote"); err != nil {
			return err
		}
		return p.nextLine()
	}
	if err := p.appendPlain(tok, remove); err != nil {
		return err
	}
	comment, err := p.endLine("unexpected text after the value")
	if err != nil {
		return err
	}
	if !comment {
		p.plainEnd = p.ln
	}
	return p.nextLine()
}

func (p *parser) appendPlain(tok token, remove string) error {
	switch s := tok.text; {
	case isNull(s):
		return p.errorf(tok.pos, "null is not allowed%s", remove)
	case isBool(s):
		p.out = strconv.AppendBool(p.out, s[0] == 't' || s[0] == 'T')
	case isNumber(s):
		if !isJSONNumber(s) {
			return p.errorf(tok.pos, "number %q must be written as in JSON, such as 10, -1.5 or 1e3; quote it for a text", s)
		}
		p.out = append(p.out, s...)
	default:
		p.appendString(s)
	}
	return nil
}

// key returns the text of tok as a key: plain keys must not be null, booleans or numbers.
func (p *parser) key(tok token) (string, error) {
	if !tok.quoted && (isNull(tok.text) || isBool(tok.text) || isNumber(tok.text)) {
		return "", p.errorf(tok.pos, "key %q must be a string; quote it", tok.text)
	}
	return tok.text, nil
}

// mapping reads a block mapping whose keys have the indentation ind. The cursor is after its first key, tok.
func (p *parser) mapping(ptr string, ind int, tok token) error {
	if err := p.enter(tok.pos); err != nil {
		return err
	}
	p.out = append(p.out, '{')
	seen := make(map[string]bool)
	for {
		n := p.keyColon()
		if p.col+n-tok.pos.Column > maxKeyLen {
			return p.errorf(tok.pos, "key is longer than %d characters", maxKeyLen)
		}
		key, err := p.key(tok)
		if err != nil {
			return err
		}
		if seen[key] {
			return p.errorf(tok.pos, "duplicate key %q", key)
		}
		if len(seen) > 0 {
			p.out = append(p.out, ',')
		}
		seen[key] = true
		p.appendString(key)
		p.out = append(p.out, ':')
		m := member(ptr, key)
		p.pos[m] = tok.pos
		p.skip(n + 1)
		if err := p.mapValue(m, ind, tok.pos); err != nil {
			return err
		}
		if p.eof() || p.col-1 < ind {
			break
		}
		if p.col-1 > ind {
			return p.deeper()
		}
		if tok, err = p.nextKey(); err != nil {
			return err
		}
	}
	p.out = append(p.out, '}')
	p.depth--
	return nil
}

// nextKey reads the key at the cursor, at the indentation of a mapping after its first entry.
func (p *parser) nextKey() (token, error) {
	pos := p.here()
	if c := p.peek(); c == '-' && p.blankAt(1) || c == '[' || c == '{' {
		return token{}, p.errorf(pos, `expected "key: value"`)
	}
	tok, err := p.scalar(false)
	if err != nil {
		return token{}, err
	}
	if p.keyColon() < 0 {
		if !tok.quoted && strings.Contains(tok.text, ":") || tok.quoted && p.colonAfterSpaces() >= 0 {
			return token{}, p.errorf(pos, `expected "key: value"; put a space after ":"`)
		}
		return token{}, p.errorf(pos, `expected "key: value"`)
	}
	return tok, nil
}

// mapValue reads the value of a member of a block mapping whose keys have the indentation ind.
// The cursor is after ":".
func (p *parser) mapValue(ptr string, ind int, keyPos Position) error {
	if err := p.skipSpaces(); err != nil {
		return err
	}
	c := p.peek()
	if c != 0 && c != '#' {
		return p.inlineValue(ptr, ind)
	}
	if err := p.nextLine(); err != nil {
		return err
	}
	switch {
	case p.eof():
	case p.col-1 > ind:
		_, err := p.node(ptr, ind, removeKey)
		return err
	case p.col-1 == ind && p.peek() == '-' && p.blankAt(1):
		return p.sequence(ptr, ind, true)
	}
	return p.missingValue(keyPos, c == '#')
}

// missingValue returns the error for the key or item at pos without a value. A comment right after its ":" or "-"
// on the line (comment) may have been meant as the value.
func (p *parser) missingValue(pos Position, comment bool) error {
	if comment {
		return p.errorf(pos, "missing value%s", quoteHash)
	}
	return p.errorf(pos, "missing value")
}

// inlineValue reads the value after "key: " on the same line, in a mapping whose keys have the indentation ind.
func (p *parser) inlineValue(ptr string, ind int) error {
	switch c := p.peek(); {
	case c == '-' && p.blankAt(1):
		return p.errorf(p.here(), "a sequence cannot start on the same line as its key; start it on the next line")
	case c == '[' || c == '{':
		return p.flowValue(ptr, ind, false)
	}
	tok, err := p.scalar(false)
	if err != nil {
		return err
	}
	if n := p.keyColon(); n >= 0 && !tok.quoted {
		return p.errorf(Position{Line: p.ln + 1, Column: p.col + n},
			`unexpected ":"; quote the value if it is a text, or start the nested mapping on the next line`)
	}
	return p.scalarValue(tok, removeKey)
}

// sequence reads a block sequence whose "-" have the indentation ind. A sequence that is the value of a key with
// the same indentation (same) ends at the next key.
func (p *parser) sequence(ptr string, ind int, same bool) error {
	if err := p.enter(p.here()); err != nil {
		return err
	}
	p.out = append(p.out, '[')
	for i := 0; ; i++ {
		dash := p.here()
		p.skip(1)
		if i > 0 {
			p.out = append(p.out, ',')
		}
		if err := p.skipSpaces(); err != nil {
			return err
		}
		if c := p.peek(); c == 0 || c == '#' {
			if err := p.nextLine(); err != nil {
				return err
			}
			if p.eof() || p.col-1 <= ind {
				return p.missingValue(dash, c == '#')
			}
		}
		it := item(ptr, i)
		p.pos[it] = p.here()
		if _, err := p.node(it, ind, removeItem); err != nil {
			return err
		}
		if p.eof() || p.col-1 < ind {
			break
		}
		if p.col-1 > ind {
			return p.deeper()
		}
		if p.peek() != '-' || !p.blankAt(1) {
			if same {
				break
			}
			return p.errorf(p.here(), `expected "- " for a sequence item`)
		}
	}
	p.out = append(p.out, ']')
	p.depth--
	return nil
}
