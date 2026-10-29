package yaml

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

const byteOrderMark = 0xFEFF

// printable reports whether r is in c-printable of YAML.
func printable(r rune) bool {
	switch {
	case r == '\t', r == '\n', r == '\r', r == 0x85:
		return true
	case r < 0x20:
		return false
	case r <= 0x7E:
		return true
	}
	return 0xA0 <= r && r <= 0xD7FF || 0xE000 <= r && r <= 0xFFFD || 0x10000 <= r && r <= utf8.MaxRune
}

func isFlowIndicator(c byte) bool {
	return c == ',' || c == '[' || c == ']' || c == '{' || c == '}'
}

// token is a plain or quoted scalar as read, and the place of its first character.
type token struct {
	text   string
	quoted bool
	pos    Position
}

// scalar reads the scalar at the cursor, in a flow collection if flow is true.
func (p *parser) scalar(flow bool) (token, error) {
	tok := token{pos: p.here()}
	var err error
	switch p.peek() {
	case '"':
		tok.quoted = true
		tok.text, err = p.doubleQuoted()
	case '\'':
		tok.quoted = true
		tok.text, err = p.singleQuoted()
	default:
		if err := p.plainStart(flow); err != nil {
			return token{}, err
		}
		tok.text, err = p.plain(flow)
	}
	if err != nil {
		return token{}, err
	}
	return tok, nil
}

// plainSafe reports whether the byte k bytes after the cursor starts a character that may follow ":", "?" or "-"
// at the start of a plain scalar.
func (p *parser) plainSafe(k int, flow bool) bool {
	c := p.at(k)
	return c != 0 && c != ' ' && c != '\t' && (!flow || !isFlowIndicator(c))
}

// plainStart checks that the character at the cursor may start a plain scalar.
func (p *parser) plainStart(flow bool) error {
	c := p.peek()
	switch c {
	case '-', '?', ':':
		if p.plainSafe(1, flow) {
			return nil
		}
		switch c {
		case '?':
			return p.errorf(p.here(), `explicit keys ("? ") are not supported; write "key: value"`)
		case ':':
			return p.errorf(p.here(), `missing key before ":"`)
		}
	case '&', '*':
		return p.errorf(p.here(), "anchors (&) and aliases (*) are not supported; write each value in full")
	case '!':
		return p.errorf(p.here(), "tags (!) are not supported; remove the tag")
	case '|', '>':
		return p.errorf(p.here(), "block scalars (| and >) are not supported; write the value on one line")
	case '#':
		return p.errorf(p.here(), msgComment)
	case ',', '[', ']', '{', '}', '%', '@', '`':
	default:
		return nil
	}
	return p.errorf(p.here(), "unquoted text cannot start with %q; quote it", string(c))
}

// plain reads the plain scalar at the cursor, whose first character plainStart accepts. It stops at the end of the
// line, before spaces and a comment, before ":" without a safe character after it, and in flow collections before
// ",[]{}". The cursor is left after its last character, as trailing spaces are not part of it.
func (p *parser) plain(flow bool) (string, error) {
	s := p.line()
	start := p.off
	end, endCol := p.off, p.col
loop:
	for p.off < len(s) {
		c := s[p.off]
		switch {
		case c == ' ':
			p.skip(1)
			continue
		case c == '\t':
			return "", p.errorf(p.here(), msgTab)
		case c == '#' && s[p.off-1] == ' ', c == ':' && !p.plainSafe(1, flow), flow && isFlowIndicator(c):
			break loop
		}
		p.advance()
		end, endCol = p.off, p.col
	}
	p.off, p.col = end, endCol
	return s[start:end], nil
}

func (p *parser) singleQuoted() (string, error) {
	open := p.here()
	s := p.line()
	p.skip(1)
	var b strings.Builder
	for {
		switch {
		case p.off == len(s):
			return "", p.errorf(open, msgUnclosedQuote)
		case s[p.off] != '\'':
			start := p.off
			p.advance()
			b.WriteString(s[start:p.off])
		case p.at(1) == '\'':
			b.WriteByte('\'')
			p.skip(2)
		default:
			p.skip(1)
			return b.String(), nil
		}
	}
}

func (p *parser) doubleQuoted() (string, error) {
	open := p.here()
	s := p.line()
	p.skip(1)
	var b strings.Builder
	for {
		switch {
		case p.off == len(s):
			return "", p.errorf(open, msgUnclosedQuote)
		case s[p.off] == '"':
			p.skip(1)
			return b.String(), nil
		case s[p.off] == '\\':
			r, n, err := p.escape()
			if err != nil {
				return "", err
			}
			b.WriteRune(r)
			p.skip(n)
		default:
			start := p.off
			p.advance()
			b.WriteString(s[start:p.off])
		}
	}
}

var escapes = map[byte]rune{
	'0': 0, 'a': 0x07, 'b': 0x08, 't': 0x09, '\t': 0x09, 'n': 0x0A, 'v': 0x0B, 'f': 0x0C, 'r': 0x0D, 'e': 0x1B,
	' ': ' ', '"': '"', '/': '/', '\\': '\\', 'N': 0x85, '_': 0xA0, 'L': 0x2028, 'P': 0x2029,
}

// escape reads the escape sequence at the cursor in a double-quoted scalar and returns its character and length.
func (p *parser) escape() (rune, int, error) {
	s := p.line()[p.off:]
	if len(s) == 1 {
		return 0, 0, p.errorf(p.here(), `escaped line breaks (\ at the end of a line) are not supported; write the value on one line`)
	}
	if r, ok := escapes[s[1]]; ok {
		return r, 2, nil
	}
	var width int
	switch s[1] {
	case 'x':
		width = 2
	case 'u':
		width = 4
	case 'U':
		width = 8
	default:
		r, _ := utf8.DecodeRuneInString(s[1:])
		return 0, 0, p.errorf(p.here(), `unknown escape \%c; write \\ for a backslash`, r)
	}
	n := 2 + width
	if len(s) < n || !isHex(s[2:n]) {
		return 0, 0, p.errorf(p.here(), `escape \%c needs %d hex digits`, s[1], width)
	}
	v, _ := strconv.ParseUint(s[2:n], 16, 32)
	switch {
	case 0xD800 <= v && v <= 0xDFFF:
		if lo, ok := lowSurrogate(s[n:]); ok && v <= 0xDBFF && s[1] == 'u' {
			r := 0x10000 + (rune(v)-0xD800)<<10 + (lo - 0xDC00)
			return 0, 0, p.errorf(p.here(), `surrogate escapes are not supported; write \U%08X or the character itself`, r)
		}
		return 0, 0, p.errorf(p.here(), `surrogate escapes are not supported; write \U and 8 hex digits, or the character itself`)
	case v > utf8.MaxRune:
		return 0, 0, p.errorf(p.here(), `escape %s is beyond U+10FFFF`, s[:n])
	}
	// Characters that the input may not contain are also not allowed escaped, except C0 controls, which JSON escapes.
	if r := rune(v); r < 0x20 || printable(r) && r != byteOrderMark {
		return r, n, nil
	}
	return 0, 0, p.errorf(p.here(), "character U+%04X is not allowed, even escaped", v)
}

// lowSurrogate returns the value of s if it starts with a \u escape of a low surrogate.
func lowSurrogate(s string) (rune, bool) {
	if len(s) < 6 || s[0] != '\\' || s[1] != 'u' || !isHex(s[2:6]) {
		return 0, false
	}
	v, _ := strconv.ParseUint(s[2:6], 16, 32)
	return rune(v), 0xDC00 <= v && v <= 0xDFFF
}

func isHex(s string) bool {
	for i := range len(s) {
		if !isHexDigit(s[i]) {
			return false
		}
	}
	return true
}

func isHexDigit(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func isNull(s string) bool {
	return s == "null" || s == "Null" || s == "NULL" || s == "~"
}

func isBool(s string) bool {
	switch s {
	case "true", "True", "TRUE", "false", "False", "FALSE":
		return true
	}
	return false
}

// isNumber reports whether the core schema of YAML 1.2 reads s as an integer or a floating-point number.
func isNumber(s string) bool {
	if len(s) > 2 && s[0] == '0' && (s[1] == 'o' || s[1] == 'x') {
		rest := s[2:]
		if s[1] == 'o' {
			return strings.Trim(rest, "01234567") == ""
		}
		return isHex(rest)
	}
	t := s
	if t != "" && (t[0] == '+' || t[0] == '-') {
		t = t[1:]
	}
	switch {
	case t == ".inf" || t == ".Inf" || t == ".INF":
		return true
	case s == ".nan" || s == ".NaN" || s == ".NAN":
		return true
	}
	i := digits(t, 0)
	if i < len(t) && t[i] == '.' {
		j := digits(t, i+1)
		if i == 0 && j == 1 {
			return false
		}
		i = j
	} else if i == 0 {
		return false
	}
	return exponent(t, i) == len(t)
}

// isJSONNumber reports whether s is a number as JSON writes it.
func isJSONNumber(s string) bool {
	i := 0
	if s != "" && s[0] == '-' {
		i++
	}
	switch {
	case i < len(s) && s[i] == '0':
		i++
	case i < len(s) && '1' <= s[i] && s[i] <= '9':
		i = digits(s, i)
	default:
		return false
	}
	if i < len(s) && s[i] == '.' {
		j := digits(s, i+1)
		if j == i+1 {
			return false
		}
		i = j
	}
	return exponent(s, i) == len(s)
}

// digits returns the index of the first byte at or after i in s that is not a decimal digit.
func digits(s string, i int) int {
	for i < len(s) && '0' <= s[i] && s[i] <= '9' {
		i++
	}
	return i
}

// exponent returns the index after an exponent such as "e+10" at i in s, or i if there is none or it lacks digits.
func exponent(s string, i int) int {
	if i == len(s) || s[i] != 'e' && s[i] != 'E' {
		return i
	}
	j := i + 1
	if j < len(s) && (s[j] == '+' || s[j] == '-') {
		j++
	}
	if k := digits(s, j); k > j {
		return k
	}
	return i
}
