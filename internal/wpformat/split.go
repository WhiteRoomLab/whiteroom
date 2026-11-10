package wpformat

import (
	"slices"
	"strings"
)

// blockNames are the elements that wpautop sets apart as blocks.
var blockNames = []string{
	"address", "area", "article", "aside", "blockquote", "caption", "col", "colgroup", "dd", "details", "div", "dl", "dt",
	"fieldset", "figcaption", "figure", "footer", "form", "h1", "h2", "h3", "h4", "h5", "h6", "header", "hgroup", "hr",
	"legend", "li", "map", "menu", "nav", "ol", "p", "pre", "section", "style", "summary", "table", "tbody", "td", "tfoot",
	"th", "thead", "tr", "ul",
}

// maxBlockName is the length of the longest of blockNames.
const maxBlockName = len("blockquote")

// listNames are the blocks that wpautop takes a line break away before.
var listNames = []string{"dd", "div", "dl", "dt", "li", "ol", "p", "pre", "td", "th", "ul"}

var blocksByFirst = func() (t [256][]string) {
	for _, n := range blockNames {
		t[n[0]] = append(t[n[0]], n)
	}
	return t
}()

func isBlockName(name string) bool {
	_, ok := slices.BinarySearch(blockNames, name)
	return ok
}

// hasBlockPrefix reports whether s starts with the name of a block, whatever follows it: "p" is the start of "param" too.
func hasBlockPrefix(s string) bool {
	if s == "" {
		return false
	}
	for _, n := range blocksByFirst[s[0]] {
		if strings.HasPrefix(s, n) {
			return true
		}
	}
	return false
}

func hasListPrefix(s string) bool {
	for _, n := range listNames {
		if strings.HasPrefix(s, n) {
			return true
		}
	}
	return false
}

// nameRun returns the end of the lower case letters and digits at s[i:], or a little after the end of the longest name.
func nameRun(s string, i int) int {
	j := i
	for j < len(s) && j-i <= maxBlockName && ('a' <= s[j] && s[j] <= 'z' || '0' <= s[j] && s[j] <= '9') {
		j++
	}
	return j
}

// blockOpen reports whether the "<" at s[i] starts a block element: its name is one of blockNames, and white space, a
// slash, or ">" follows it.
func blockOpen(s string, i int) bool {
	j := nameRun(s, i+1)
	if j >= len(s) {
		return false
	}
	if c := s[j]; !isSpace(c) && c != '/' && c != '>' {
		return false
	}
	return isBlockName(s[i+1 : j])
}

// blockClose returns the end of the end tag of a block element that starts at the "<" at s[i], or -1.
func blockClose(s string, i int) int {
	if i+1 >= len(s) || s[i+1] != '/' {
		return -1
	}
	j := nameRun(s, i+2)
	if j < len(s) && s[j] == '>' && isBlockName(s[i+2:j]) {
		return j + 1
	}
	return -1
}

// hrTag returns the end of the tag <hr>, <hr/> and the like (white space, and an optional slash) at s[i], or -1.
func hrTag(m *meter, s string, i int) int {
	if !strings.HasPrefix(s[i:], "<hr") {
		return -1
	}
	j := skipSpace(m, s, i+3)
	if j < len(s) && s[j] == '/' {
		j++
	}
	if j < len(s) && s[j] == '>' {
		return j + 1
	}
	return -1
}

// ElementEnd returns the end of the element that starts at the "<" at s[i], where the text is cut into elements: a comment
// runs to the first "-->" (that may share dashes with its start), a CDATA section to the first "]]>", and any other element to
// the first ">", where it is included; if there is none, the element runs to the end of s. Whatever follows the "<" makes an
// element.
func ElementEnd(s string, i int) int { return elementEnd(nil, s, i) }

func elementEnd(m *meter, s string, i int) int {
	switch rest := s[i+1:]; {
	case strings.HasPrefix(rest, "!--"):
		if k := strings.Index(s[i+2:], "-->"); k >= 0 {
			m.look(k + 3)
			return i + 2 + k + 3
		}
	case strings.HasPrefix(rest, "![CDATA["):
		from := i + len("<![CDATA[")
		if k := strings.Index(s[from:], "]]>"); k >= 0 {
			m.look(k + 3)
			return from + k + 3
		}
	default:
		if k := strings.IndexByte(rest, '>'); k >= 0 {
			m.look(k + 1)
			return i + 1 + k + 1
		}
	}
	m.look(len(s) - i)
	return len(s)
}
