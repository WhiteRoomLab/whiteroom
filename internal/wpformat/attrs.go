package wpformat

import (
	"strings"
	"unicode/utf8"
)

// An Attr is an attribute of a shortcode. A value that has no name has an empty Name.
type Attr struct {
	Name, Value string
}

// An AttrList is the attributes of a shortcode in the order of the PHP array that shortcode_parse_atts makes.
type AttrList []Attr

// Get returns the value of the attribute that has the name.
func (as AttrList) Get(name string) (string, bool) {
	if name != "" {
		for _, a := range as {
			if a.Name == name {
				return a.Value, true
			}
		}
	}
	return "", false
}

// First returns the first attribute, whether it has a name or not.
func (as AttrList) First() (Attr, bool) {
	if len(as) == 0 {
		return Attr{}, false
	}
	return as[0], true
}

// ParseAttrs reads the attributes of a shortcode in the way of shortcode_parse_atts of WordPress 7.1.4. Names are in lower
// case. The escapes of the values are resolved as stripcslashes of PHP does, so a value can have bytes that are not UTF-8.
// The value of an attribute that has a name that an earlier attribute has is put at the place of the earlier one. A value
// with a "<" that no ">" follows is empty. There are no attributes if s is not UTF-8. As in PHP, an attribute named "0" and
// a quoted value without a name that is empty are left out.
func ParseAttrs(s string) AttrList {
	return parseAttrs(s, nil)
}

func parseAttrs(s string, m *meter) AttrList {
	m.look(len(s))
	if !utf8.ValidString(s) {
		return nil
	}
	s = collapseSpaces(m, s)
	var list attrBuilder
	for i := 0; i < len(s); {
		if isSpace(s[i]) {
			i = skipSpace(m, s, i)
			continue
		}
		if name, value, end, ok := namedAttr(m, s, i); ok {
			list.put(m, name, value)
			i = end
			continue
		}
		if q := s[i]; q == '"' || q == '\'' {
			if value, end, ok := quotedAttr(m, s, i); ok {
				if value != "" {
					list.put(m, "", value)
				}
				i = end
				continue
			}
		}
		j := i
		for j < len(s) && !isSpace(s[j]) {
			j++
		}
		m.look(j - i + 1)
		list.put(m, "", s[i:j])
		i = delimiterEnd(s, j)
	}
	return list.attrs
}

// collapseSpaces makes each run of no-break spaces and zero-width spaces a space.
func collapseSpaces(m *meter, s string) string {
	if !strings.Contains(s, "\xc2\xa0") && !strings.Contains(s, "\xe2\x80\x8b") {
		m.look(2 * len(s))
		return s
	}
	m.look(len(s))
	var b strings.Builder
	b.Grow(len(s))
	copied := 0
	for i := 0; i < len(s); {
		n := invisibleSpaceLen(s[i:])
		if n == 0 {
			i++
			continue
		}
		j := i + n
		for k := invisibleSpaceLen(s[j:]); k > 0; k = invisibleSpaceLen(s[j:]) {
			j += k
		}
		b.WriteString(s[copied:i])
		b.WriteByte(' ')
		copied, i = j, j
	}
	b.WriteString(s[copied:])
	return b.String()
}

// invisibleSpaceLen returns the length of the no-break space or the zero-width space at the start of s, or 0.
func invisibleSpaceLen(s string) int {
	switch {
	case strings.HasPrefix(s, "\xc2\xa0"):
		return 2
	case strings.HasPrefix(s, "\xe2\x80\x8b"):
		return 3
	}
	return 0
}

func delimiterEnd(s string, end int) int {
	if end < len(s) {
		return end + 1
	}
	return end
}

// namedAttr reads a name, "=", and a value in double quotes, in single quotes, or with neither, at s[i:], with white space
// allowed around the "=". The value is followed by white space or the end of s. It returns the end of the attribute, which
// includes that white space.
func namedAttr(m *meter, s string, i int) (name, value string, end int, ok bool) {
	j := i
	for j < len(s) && isNameByte(s[j]) {
		j++
	}
	if j == i {
		return "", "", 0, false
	}
	m.look(j - i)
	eq := skipSpace(m, s, j)
	if eq >= len(s) || s[eq] != '=' {
		return "", "", 0, false
	}
	v := skipSpace(m, s, eq+1)
	if v >= len(s) {
		return "", "", 0, false
	}
	switch q := s[v]; q {
	case '"', '\'':
		value, end, ok = quotedAttr(m, s, v)
		if !ok {
			return "", "", 0, false
		}
		return s[i:j], value, end, true
	default:
		e := v
		for e < len(s) && !isSpace(s[e]) && s[e] != '"' && s[e] != '\'' {
			e++
		}
		m.look(e - v + 1)
		if e < len(s) && !isSpace(s[e]) {
			return "", "", 0, false
		}
		return s[i:j], s[v:e], delimiterEnd(s, e), true
	}
}

// quotedAttr reads a value in the quotes that s[i] is, followed by white space or the end of s.
func quotedAttr(m *meter, s string, i int) (value string, end int, ok bool) {
	k := strings.IndexByte(s[i+1:], s[i])
	if k < 0 {
		m.look(len(s) - i)
		return "", 0, false
	}
	m.look(k + 2)
	e := i + 1 + k + 1
	if e < len(s) && !isSpace(s[e]) {
		return "", 0, false
	}
	return s[i+1 : i+1+k], delimiterEnd(s, e), true
}

// An attrBuilder makes the list. It looks for a name in the list as it is until the list is long, and then in a map.
type attrBuilder struct {
	attrs AttrList
	index map[string]int
}

const attrScanLimit = 16

func (b *attrBuilder) put(m *meter, name, value string) {
	value = attrValue(m, value)
	if name == "" {
		b.attrs = append(b.attrs, Attr{Value: value})
		return
	}
	name = strings.ToLower(name)
	if name == "0" {
		return
	}
	if b.index == nil && len(b.attrs) >= attrScanLimit {
		b.index = make(map[string]int, 2*attrScanLimit)
		for i, a := range b.attrs {
			if a.Name != "" {
				b.index[a.Name] = i
			}
		}
	}
	if b.index != nil {
		if i, ok := b.index[name]; ok {
			b.attrs[i].Value = value
			return
		}
		b.index[name] = len(b.attrs)
	} else {
		for i := range b.attrs {
			if b.attrs[i].Name == name {
				b.attrs[i].Value = value
				return
			}
		}
	}
	b.attrs = append(b.attrs, Attr{Name: name, Value: value})
}

// attrValue resolves the escapes of a value, and empties a value that has a "<" with no ">" after it.
func attrValue(m *meter, v string) string {
	m.look(3 * len(v))
	if strings.IndexByte(v, '\\') >= 0 {
		v = stripCSlashes(v)
	}
	if lt := strings.LastIndexByte(v, '<'); lt >= 0 && lt > strings.LastIndexByte(v, '>') {
		return ""
	}
	return v
}

func hexValue(c byte) (int, bool) {
	switch {
	case '0' <= c && c <= '9':
		return int(c - '0'), true
	case 'a' <= c && c <= 'f':
		return int(c-'a') + 10, true
	case 'A' <= c && c <= 'F':
		return int(c-'A') + 10, true
	}
	return 0, false
}

// stripCSlashes does what stripcslashes of PHP does.
func stripCSlashes(v string) string {
	var b strings.Builder
	b.Grow(len(v))
	for i := 0; i < len(v); {
		c := v[i]
		if c != '\\' || i+1 >= len(v) {
			b.WriteByte(c)
			i++
			continue
		}
		i++
		switch e := v[i]; e {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 'a':
			b.WriteByte('\a')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte('\v')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case '\\':
			b.WriteByte('\\')
		case 'x':
			if h, ok := hexValue(peek(v, i+1)); ok {
				i++
				if l, ok := hexValue(peek(v, i+1)); ok {
					i++
					h = h*16 + l
				}
				b.WriteByte(byte(h))
			} else {
				b.WriteByte(e)
			}
		default:
			if '0' <= e && e <= '7' {
				n := 0
				j := i
				for j < len(v) && j-i < 3 && '0' <= v[j] && v[j] <= '7' {
					n = n*8 + int(v[j]-'0')
					j++
				}
				b.WriteByte(byte(n))
				i = j - 1
			} else {
				b.WriteByte(e)
			}
		}
		i++
	}
	return b.String()
}

// peek returns v[i], or 0 if there is no such byte.
func peek(v string, i int) byte {
	if i < len(v) {
		return v[i]
	}
	return 0
}
