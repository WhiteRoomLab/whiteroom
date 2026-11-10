package wpformat

import (
	"slices"
	"strings"
)

// countedNames are the elements that splitPoints counts: those that hold content and need their end tags, and those that
// hold text.
var countedNames = []string{
	"address", "article", "aside", "blockquote", "center", "details", "div", "dl", "fieldset", "figure", "footer", "form",
	"header", "hgroup", "iframe", "main", "menu", "nav", "noembed", "noframes", "noscript", "ol", "pre", "script", "section",
	"style", "table", "template", "textarea", "title", "ul", "xmp",
}

// rawNames are the counted elements whose content is text, that no tag ends but the end tag of the element.
var rawNames = []string{
	"iframe", "noembed", "noframes", "noscript", "script", "style", "template", "textarea", "title", "xmp",
}

// maxCountedName is the length of the longest of countedNames.
const maxCountedName = len("blockquote")

// countedTag returns the name in lower case of the counted element that the tag (an element that starts with "<") is a
// start tag or an end tag of.
func countedTag(tag string) (name string, closing, ok bool) {
	i := 1
	if i < len(tag) && tag[i] == '/' {
		closing = true
		i++
	}
	j := i
	for j < len(tag) && j-i <= maxCountedName && ('a' <= tag[j]|0x20 && tag[j]|0x20 <= 'z' || '0' <= tag[j] && tag[j] <= '9') {
		j++
	}
	if j >= len(tag) || !isSpace(tag[j]) && tag[j] != '/' && tag[j] != '>' {
		return "", false, false
	}
	name = strings.ToLower(tag[i:j])
	_, ok = slices.BinarySearch(countedNames, name)
	return name, closing, ok
}

// rawEnd returns the start of the first end tag of name at or after from, or the end of s.
func rawEnd(m *meter, s string, from int, name string) int {
	for i := from; ; {
		k := strings.IndexByte(s[i:], '<')
		if k < 0 {
			m.look(len(s) - i)
			return len(s)
		}
		m.look(k + 1)
		start := i + k
		i = start + 1
		if i < len(s) && s[i] == '/' && hasPrefixFold(s[i+1:], name) {
			if end := i + 1 + len(name); end < len(s) && (isSpace(s[end]) || s[end] == '/' || s[end] == '>') {
				return start
			}
		}
	}
}

// splitPoints returns the places at which SplitBlocks may split s: the places after a line break in text, with no counted
// element open. A counted element is open from its start tag, unless that ends with "/>", to an end tag of a counted
// element, and nothing is read in an element of raw text, up to its end tag.
func splitPoints(m *meter, s string) []int {
	var points []int
	depth := 0
	for i := 0; i < len(s); {
		lt := strings.IndexByte(s[i:], '<')
		textEnd := len(s)
		if lt >= 0 {
			textEnd = i + lt
		}
		m.look(textEnd - i + 1)
		if depth == 0 {
			for j := i; ; {
				k := strings.IndexByte(s[j:textEnd], '\n')
				if k < 0 {
					break
				}
				j += k + 1
				if j < len(s) {
					points = append(points, j)
				}
			}
		}
		if lt < 0 {
			break
		}
		i = elementEnd(m, s, textEnd)
		tag := s[textEnd:i]
		name, closing, ok := countedTag(tag)
		switch {
		case !ok:
		case closing:
			depth = max(depth-1, 0)
		case strings.HasSuffix(tag, "/>"):
		default:
			depth++
			if _, raw := slices.BinarySearch(rawNames, name); raw {
				i = rawEnd(m, s, i, name)
			}
		}
	}
	return points
}

// SplitBlocks splits s into parts that are at most max bytes long, outside of block elements, so that the parts make s
// again. It is not in WordPress: the importer uses it to keep each tree of elements small. A part is longer than max
// where there is no place to split it before max; the part ends at the next place, or at the end of s.
func SplitBlocks(s string, max int) []string {
	return splitBlocks(s, max, nil)
}

func splitBlocks(s string, max int, m *meter) []string {
	if len(s) <= max || s == "" {
		return []string{s}
	}
	var parts []string
	start, best := 0, -1
	cut := func(end int) {
		parts = append(parts, s[start:end])
		start, best = end, -1
	}
	for _, p := range splitPoints(m, s) {
		for p-start > max {
			if best <= start {
				cut(p)
				break
			}
			cut(best)
		}
		if p > start && p-start <= max {
			best = p
		}
	}
	if len(s)-start > max && best > start {
		cut(best)
	}
	if start < len(s) {
		parts = append(parts, s[start:])
	}
	return parts
}
