package wpformat

import (
	"slices"
	"strings"
)

// A Shortcode is a piece of text that do_shortcode of WordPress runs. Start and End bound it: from the first "[" (the extra
// one, if Open) to the last "]" (the extra one, if Close).
type Shortcode struct {
	Start, End int
	Name       string
	// Attrs is what lies between the name and the "]" (or the "/]" of SelfClosing), as written.
	Attrs string
	// Content is what lies between the "]" and the "[/name]" of a Closed shortcode, as written.
	Content     string
	Closed      bool
	SelfClosing bool
	// Open and Close are an extra "[" before the shortcode and an extra "]" after it. WordPress does not run a shortcode that
	// has both: it shows the text without the outer brackets.
	Open, Close bool
}

// FindShortcodes returns the shortcodes of s, from the left and without overlaps, in the way that do_shortcode of WordPress
// 7.1.4 finds the names that isName accepts. s is HTML: a "[" or "]" in an element is text, and a shortcode may run over
// elements. isName gets the longest run of letters, digits, "_", and "-" after the "[", and the byte after the run (0 at
// the end of s). Names with other characters, which WordPress can register, are not found. WordPress runs a name only if
// some "[" has exactly that name after it and then a byte that no name has (white space, "]", "/", "=", "<", ">", "&", "[",
// or a control byte); isName can use the byte to leave out the names that WordPress would not run.
func FindShortcodes(s string, isName func(name string, next byte) bool) []Shortcode {
	return findShortcodes(s, isName, nil)
}

// FindShortcodesAt is FindShortcodes, and isName also gets the position in s of the "[" that the name follows (of the first
// of two, if there are two). The positions that it is asked about grow.
func FindShortcodesAt(s string, isName func(at int, name string, next byte) bool) []Shortcode {
	return findShortcodesAt(s, isName, nil)
}

func isNameByte(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '-'
}

func findShortcodes(s string, isName func(string, byte) bool, m *meter) []Shortcode {
	return findShortcodesAt(s, func(_ int, name string, next byte) bool { return isName(name, next) }, m)
}

func findShortcodesAt(s string, isName func(int, string, byte) bool, m *meter) []Shortcode {
	first := strings.IndexByte(s, '[')
	if first < 0 {
		m.look(len(s))
		return nil
	}
	m.look(first + 1)
	t := textBrackets(m, s)
	closes := searcher{m: m, s: t, sep: "]"}
	closers := closerIndex{m: m, s: s, t: t}
	var out []Shortcode
	for p := first; p < len(t); {
		k := strings.IndexByte(t[p:], '[')
		if k < 0 {
			m.look(len(t) - p)
			break
		}
		m.look(k + 1)
		p += k
		open := p+1 < len(t) && t[p+1] == '['
		ns := p + 1
		if open {
			ns++
		}
		ne := ns
		for ne < len(t) && isNameByte(t[ne]) {
			ne++
		}
		m.look(ne - ns + 1)
		var next byte
		if ne < len(s) {
			next = s[ne]
		}
		if ne == ns || !isName(p, s[ns:ne], next) {
			// The "[" after this one has the same name, unless another "[" follows it.
			p = ns
			if open && ns < len(t) && t[ns] == '[' {
				p--
			}
			continue
		}
		bracket := closes.next(ne)
		if bracket < 0 {
			// No "]" is left, so no shortcode is.
			break
		}
		sc := Shortcode{Start: p, Name: s[ns:ne], Open: open}
		sc.SelfClosing = bracket > ne && t[bracket-1] == '/'
		sc.Attrs = s[ne:bracket]
		if sc.SelfClosing {
			sc.Attrs = s[ne : bracket-1]
		}
		end := bracket + 1
		if !sc.SelfClosing {
			if at := closers.next(sc.Name, bracket+1); at >= 0 {
				sc.Closed = true
				sc.Content = s[bracket+1 : at]
				end = at + len("[/]") + len(sc.Name)
			}
		}
		if end < len(t) && t[end] == ']' {
			sc.Close = true
			end++
		}
		sc.End = end
		out = append(out, sc)
		p = end
	}
	return out
}

// textBrackets returns s with each "[" and "]" in an element replaced by a byte that is none of them, so that the brackets
// left are the ones in text. It returns s itself if no element has a bracket.
func textBrackets(m *meter, s string) string {
	var b strings.Builder
	copied := 0
	for i := 0; i < len(s); {
		k := strings.IndexByte(s[i:], '<')
		if k < 0 {
			m.look(len(s) - i)
			break
		}
		m.look(k + 1)
		start := i + k
		i = elementEnd(m, s, start)
		m.look(i - start)
		for j := start; j < i; j++ {
			if c := s[j]; c == '[' || c == ']' {
				if b.Cap() == 0 {
					b.Grow(len(s))
				}
				b.WriteString(s[copied:j])
				b.WriteByte(0)
				copied = j + 1
			}
		}
	}
	if copied == 0 {
		return s
	}
	b.WriteString(s[copied:])
	return b.String()
}

// A closerIndex finds the "[/name]" that ends a shortcode. It indexes the closing tags it passes by name, except the one it
// returns, so that a search for a name that has no closing tag, which reads to the end of the text, is not repeated for the
// next name. The positions that it is asked for do not decrease. t is s with the brackets in elements hidden.
type closerIndex struct {
	m    *meter
	s, t string
	// at holds, for each name, the positions of the closing tags that were passed, in order.
	at     map[string][]int
	cursor int
}

// next returns the start of the first "[/name]" at or after from, or -1.
func (c *closerIndex) next(name string, from int) int {
	if list := c.at[name]; len(list) > 0 {
		if i, _ := slices.BinarySearch(list, from); i < len(list) {
			return list[i]
		}
	}
	for p := max(c.cursor, from); ; {
		k := strings.IndexByte(c.t[p:], '[')
		if k < 0 {
			c.m.look(len(c.t) - p)
			c.cursor = len(c.t)
			return -1
		}
		c.m.look(k + 1)
		p += k
		if p+1 >= len(c.t) || c.t[p+1] != '/' {
			p++
			continue
		}
		e := p + 2
		for e < len(c.t) && isNameByte(c.t[e]) {
			e++
		}
		c.m.look(e - p)
		if e == p+2 || e >= len(c.t) || c.t[e] != ']' {
			p++
			continue
		}
		found := c.s[p+2 : e]
		c.cursor = e + 1
		if found == name {
			return p
		}
		if c.at == nil {
			c.at = map[string][]int{}
		}
		c.at[found] = append(c.at[found], p)
		p = e + 1
	}
}
