package wpblock

import "strings"

type tokenKind uint8

const (
	kindOpener tokenKind = iota
	kindCloser
	kindVoid
)

type token struct {
	kind  tokenKind
	name  string
	attrs *Value // nil for a closing delimiter, and for attributes that are not valid JSON
	// start is the "<" of the delimiter, and end is the end of its "-->".
	start, end int
}

// A scanner finds the delimiters in doc. A delimiter is a comment: "<!--", white space, an optional "/" (a closing
// delimiter), "wp:", a name with an optional namespace and "/" before it (lower case letters, digits, "_" and "-", the
// first a letter), white space, optional attributes, an optional "/" (a void block), and "-->". White space is a space,
// a tab, an LF, a CR, or an FF, and some must follow the name even where there are no attributes. The attributes run
// from a "{" to the first "}" that is followed by white space and then "-->" or "/-->", whatever strings come between,
// and JSON is read from there to the end of that white space. Where no such "}" follows, there is no delimiter.
type scanner struct {
	doc string

	// closeAt is what the last search for a closing brace found: the first one at or after closeFrom, or -1 if there is
	// none.
	closeFrom, closeAt int
	closeKnown         bool

	// scanned counts the bytes looked at, so that a test can tell how the time grows without a clock.
	scanned int
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

func isNameChar(c byte) bool {
	return 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '_' || c == '-'
}

// next returns the first delimiter that starts at or after offset.
func (s *scanner) next(offset int) (token, bool) {
	pos := offset
	for {
		i := strings.Index(s.doc[pos:], "<!--")
		if i < 0 {
			s.scanned += len(s.doc) - pos
			return token{}, false
		}
		start := pos + i
		s.scanned += i + len("<!--")
		if tok, ok := s.match(start); ok {
			return tok, true
		}
		// The next one cannot start inside "<!--".
		pos = start + len("<!--")
	}
}

func (s *scanner) skipSpace(i int) int {
	j := i
	for j < len(s.doc) && isSpace(s.doc[j]) {
		j++
	}
	s.scanned += j - i + 1
	return j
}

// skipWord skips a lower case letter and then the characters of a name, if the letter is there.
func (s *scanner) skipWord(i int) int {
	if i >= len(s.doc) || s.doc[i] < 'a' || 'z' < s.doc[i] {
		s.scanned++
		return i
	}
	j := i + 1
	for j < len(s.doc) && isNameChar(s.doc[j]) {
		j++
	}
	s.scanned += j - i + 1
	return j
}

// match reads the delimiter that "<!--" at start begins, if it begins one.
func (s *scanner) match(start int) (token, bool) {
	doc := s.doc
	i := start + len("<!--")
	j := s.skipSpace(i)
	if j == i {
		return token{}, false
	}
	i = j
	closer := i < len(doc) && doc[i] == '/'
	if closer {
		i++
	}
	if !strings.HasPrefix(doc[i:], "wp:") {
		return token{}, false
	}
	i += len("wp:")
	nameFrom := i
	if j = s.skipWord(i); j == i {
		return token{}, false
	}
	namespaced := j < len(doc) && doc[j] == '/'
	if namespaced {
		i = j + 1
		if j = s.skipWord(i); j == i {
			return token{}, false
		}
	}
	name := doc[nameFrom:j]
	if !namespaced {
		name = "core/" + name
	}
	i = s.skipSpace(j)
	if i == j {
		return token{}, false
	}

	var attrs string
	hasAttrs, void := false, false
	var end int
	if i < len(doc) && doc[i] == '{' {
		brace := s.closingBrace(i + 1)
		if brace < 0 {
			return token{}, false
		}
		j = s.skipSpace(brace + 1)
		attrs, hasAttrs = doc[i:j], true
		if j < len(doc) && doc[j] == '/' {
			void = true
			j++
		}
		end = j + len("-->")
	} else {
		if i < len(doc) && doc[i] == '/' {
			void = true
			i++
		}
		if !strings.HasPrefix(doc[i:], "-->") {
			return token{}, false
		}
		end = i + len("-->")
	}

	tok := token{name: name, start: start, end: end}
	switch {
	case void:
		tok.kind = kindVoid
	case closer:
		tok.kind = kindCloser
		return tok, true
	default:
		tok.kind = kindOpener
	}
	if hasAttrs {
		tok.attrs, _ = DecodeAttrs(attrs)
	} else {
		tok.attrs = &Value{Kind: Object}
	}
	return tok, true
}

// closingBrace returns the position of the first "}" at or after from that is followed by white space, an optional "/",
// and "-->", or -1 if there is none. The search is not done twice for the same stretch: a document that repeats
// "<!-- wp:a {" is looked at once, not once for each repeat, as a regular expression would.
func (s *scanner) closingBrace(from int) int {
	if s.closeKnown && from >= s.closeFrom && (s.closeAt < 0 || from <= s.closeAt) {
		return s.closeAt
	}
	doc := s.doc
	i := from
	for {
		k := strings.IndexByte(doc[i:], '}')
		if k < 0 {
			s.scanned += len(doc) - i
			s.closeFrom, s.closeAt, s.closeKnown = from, -1, true
			return -1
		}
		i += k
		s.scanned += k + 1
		if j := s.skipSpace(i + 1); j > i+1 {
			if j < len(doc) && doc[j] == '/' {
				j++
			}
			if strings.HasPrefix(doc[j:], "-->") {
				s.closeFrom, s.closeAt, s.closeKnown = from, i, true
				return i
			}
		}
		i++
	}
}
