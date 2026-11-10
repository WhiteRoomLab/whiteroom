package wpformat

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// The functions here do again what FindShortcodes, ParseAttrs, and AutoembedURLs do, in the plainest way, with a search from
// each place and every length of every repeat. They are slow, so the tests use them for short texts only.

func countedBytes(s string) []bool {
	counted := make([]bool, len(s))
	for i := 0; i < len(s); {
		end := len(s)
		if lt := strings.IndexByte(s[i:], '<'); lt >= 0 {
			end = i + lt
		}
		for j := i; j < end; j++ {
			counted[j] = true
		}
		if end == len(s) {
			break
		}
		i = elementEnd(nil, s, end)
	}
	return counted
}

func referenceShortcodes(s string, isName func(string, byte) bool) []Shortcode {
	counted := countedBytes(s)
	var out []Shortcode
	for p := 0; p < len(s); {
		if c, ok := referenceShortcodeAt(s, counted, p, isName); ok {
			out = append(out, c)
			p = c.End
		} else {
			p++
		}
	}
	return out
}

func referenceShortcodeAt(s string, counted []bool, p int, isName func(string, byte) bool) (Shortcode, bool) {
	if s[p] != '[' || !counted[p] {
		return Shortcode{}, false
	}
	for _, open := range []bool{true, false} {
		i := p + 1
		if open {
			if i >= len(s) || s[i] != '[' {
				continue
			}
			i++
		}
		j := i
		for j < len(s) && isNameByte(s[j]) {
			j++
		}
		if j == i {
			continue
		}
		name := s[i:j]
		var next byte
		if j < len(s) {
			next = s[j]
		}
		if !isName(name, next) {
			continue
		}
		k := j
		for k < len(s) && (s[k] != ']' || !counted[k]) {
			k++
		}
		if k >= len(s) {
			continue
		}
		c := Shortcode{Start: p, Name: name, Open: open, Attrs: s[j:k]}
		if k > j && s[k-1] == '/' {
			c.SelfClosing = true
			c.Attrs = s[j : k-1]
		}
		end := k + 1
		if !c.SelfClosing {
			closing := "[/" + name + "]"
			for q := k + 1; q+len(closing) <= len(s); q++ {
				if counted[q] && s[q:q+len(closing)] == closing {
					c.Closed, c.Content, end = true, s[k+1:q], q+len(closing)
					break
				}
			}
		}
		if end < len(s) && s[end] == ']' && counted[end] {
			c.Close = true
			end++
		}
		c.End = end
		return c, true
	}
	return Shortcode{}, false
}

func runOf(s string, i int, in func(byte) bool) int {
	for i < len(s) && in(s[i]) {
		i++
	}
	return i
}

type attrMatch struct {
	name, value string
	named       bool
	end         int
}

// referenceAttrAt tries the six forms of the pattern of shortcode_parse_atts at s[x:], in order, and for each repeat the
// longest length first and then the shorter ones.
func referenceAttrAt(s string, x int) (attrMatch, bool) {
	delimiter := func(p int) (int, bool) {
		switch {
		case p == len(s):
			return p, true
		case isSpace(s[p]):
			return p + 1, true
		}
		return 0, false
	}
	for form := 1; form <= 3; form++ {
		for ne := runOf(s, x, isNameByte); ne > x; ne-- {
			for w1 := runOf(s, ne, isSpace); w1 >= ne; w1-- {
				if w1 >= len(s) || s[w1] != '=' {
					continue
				}
				for w2 := runOf(s, w1+1, isSpace); w2 >= w1+1; w2-- {
					switch form {
					case 1, 2:
						quote := byte('"')
						if form == 2 {
							quote = '\''
						}
						if w2 >= len(s) || s[w2] != quote {
							continue
						}
						for ve := runOf(s, w2+1, func(c byte) bool { return c != quote }); ve > w2; ve-- {
							if ve >= len(s) || s[ve] != quote {
								continue
							}
							if end, ok := delimiter(ve + 1); ok {
								return attrMatch{s[x:ne], s[w2+1 : ve], true, end}, true
							}
						}
					default:
						for ve := runOf(s, w2, func(c byte) bool { return !isSpace(c) && c != '\'' && c != '"' }); ve > w2; ve-- {
							if end, ok := delimiter(ve); ok {
								return attrMatch{s[x:ne], s[w2:ve], true, end}, true
							}
						}
					}
				}
			}
		}
	}
	for _, quote := range []byte{'"', '\''} {
		if x >= len(s) || s[x] != quote {
			continue
		}
		for ve := runOf(s, x+1, func(c byte) bool { return c != quote }); ve > x; ve-- {
			if ve >= len(s) || s[ve] != quote {
				continue
			}
			if end, ok := delimiter(ve + 1); ok {
				return attrMatch{"", s[x+1 : ve], false, end}, true
			}
		}
	}
	for ve := runOf(s, x, func(c byte) bool { return !isSpace(c) }); ve > x; ve-- {
		if end, ok := delimiter(ve); ok {
			return attrMatch{"", s[x:ve], false, end}, true
		}
	}
	return attrMatch{}, false
}

func referenceAttrs(s string) AttrList {
	if !utf8.ValidString(s) {
		return nil
	}
	var collapsed strings.Builder
	inRun := false
	for _, r := range s {
		if r == 0xa0 || r == 0x200b {
			if !inRun {
				collapsed.WriteByte(' ')
			}
			inRun = true
			continue
		}
		inRun = false
		collapsed.WriteRune(r)
	}
	s = collapsed.String()
	var list AttrList
	for x := 0; x < len(s); {
		m, ok := referenceAttrAt(s, x)
		if !ok {
			x++
			continue
		}
		x = m.end
		value := m.value
		switch {
		case m.named && m.name == "0":
			continue
		case !m.named && value == "":
			continue
		}
		value = attrValue(nil, value)
		name := ""
		if m.named {
			name = strings.ToLower(m.name)
		}
		if i := slices.IndexFunc(list, func(a Attr) bool { return name != "" && a.Name == name }); i >= 0 {
			list[i].Value = value
		} else {
			list = append(list, Attr{name, value})
		}
	}
	return list
}

func referenceURLScheme(s string) int {
	for _, scheme := range []string{"https://", "http://"} {
		if len(s) >= len(scheme) && strings.EqualFold(s[:len(scheme)], scheme) {
			return len(scheme)
		}
	}
	return 0
}

// referenceAutoembed makes the text that WordPress searches, with the text it puts for a line break in an element, and
// searches it for a URL on a line and then for a URL in a paragraph, with the first kind of URL replaced.
func referenceAutoembed(s string) []URLSpan {
	const lineBreak = "<!-- wp-line-break -->"
	var tb strings.Builder
	var origin []int
	put := func(text string, at int) {
		tb.WriteString(text)
		for range len(text) {
			origin = append(origin, at)
		}
	}
	for i := 0; i < len(s); {
		end := len(s)
		if lt := strings.IndexByte(s[i:], '<'); lt >= 0 {
			end = i + lt
		}
		for j := i; j < end; j++ {
			put(s[j:j+1], j)
		}
		if end == len(s) {
			break
		}
		next := elementEnd(nil, s, end)
		for j := end; j < next; j++ {
			if s[j] == '\n' {
				put(lineBreak, j)
			} else {
				put(s[j:j+1], j)
			}
		}
		i = next
	}
	text := tb.String()
	spans := map[int]URLSpan{}
	record := func(from, to int) {
		spans[origin[from]] = URLSpan{origin[from], origin[to-1] + 1}
	}

	masked := []byte(text)
	for x := 0; x < len(text); {
		start, end, after, ok := referenceLine(text, x)
		if !ok {
			x++
			continue
		}
		record(start, end)
		for k := start; k < end; k++ {
			masked[k] = '{'
		}
		x = after
	}
	text = string(masked)
	for x := 0; x < len(text); {
		start, end, after, ok := referenceParagraph(text, x)
		if !ok {
			x++
			continue
		}
		record(start, end)
		x = after
	}
	var out []URLSpan
	for _, span := range spans {
		out = append(out, span)
	}
	slices.SortFunc(out, func(a, b URLSpan) int { return a.Start - b.Start })
	return out
}

// referenceLine matches, at text[x:], a line that has only white space, a URL, and white space: from the start of a line, to
// the end of a line, with the schemes in any case. The white space may have line breaks.
func referenceLine(text string, x int) (start, end, after int, ok bool) {
	if x != 0 && text[x-1] != '\n' {
		return 0, 0, 0, false
	}
	for w := runOf(text, x, isSpace); w >= x; w-- {
		n := referenceURLScheme(text[w:])
		if n == 0 {
			continue
		}
		for ue := runOf(text, w+n, isURLByte); ue > w+n; ue-- {
			for w3 := runOf(text, ue, isSpace); w3 >= ue; w3-- {
				if w3 == len(text) || text[w3] == '\n' {
					return w, ue, w3, true
				}
			}
		}
	}
	return 0, 0, 0, false
}

// referenceParagraph matches, at text[x:], the start tag of a p element (a space and attributes may follow the name, up to the
// first ">"), white space, a URL, white space, and the end tag, in any case.
func referenceParagraph(text string, x int) (start, end, after int, ok bool) {
	if text[x] != '<' || x+1 >= len(text) || text[x+1]|0x20 != 'p' {
		return 0, 0, 0, false
	}
	var tagEnds []int
	if p := x + 2; p < len(text) {
		if text[p] == ' ' {
			if q := runOf(text, p+1, func(c byte) bool { return c != '>' }); q < len(text) {
				tagEnds = append(tagEnds, q+1)
			}
		}
		if text[p] == '>' {
			tagEnds = append(tagEnds, p+1)
		}
	}
	for _, a := range tagEnds {
		for w := runOf(text, a, isSpace); w >= a; w-- {
			n := referenceURLScheme(text[w:])
			if n == 0 {
				continue
			}
			for ue := runOf(text, w+n, isURLByte); ue > w+n; ue-- {
				for w3 := runOf(text, ue, isSpace); w3 >= ue; w3-- {
					if len(text)-w3 >= 4 && strings.EqualFold(text[w3:w3+4], "</p>") {
						return w, ue, w3 + 4, true
					}
				}
			}
		}
	}
	return 0, 0, 0, false
}
