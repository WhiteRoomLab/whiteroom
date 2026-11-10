// Package wpformat redoes, for the importer, what WordPress 7.1.4 does with the body of a post when it shows the post, so
// that the result is the same string: the paragraphs of wpautop and the pages of <!--nextpage-->. It also splits large
// HTML outside of block elements. It uses only the standard library.
package wpformat

import "strings"

// isSpace is what \s is in the regular expressions of PHP: a space, a tab, an LF, a VT, an FF, or a CR. Bytes such as
// 0x85 and 0xA0 are not white space.
func isSpace(c byte) bool {
	return c == ' ' || '\t' <= c && c <= '\r'
}

// isTrim is what trim of PHP removes: not an FF.
func isTrim(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == 0 || c == '\v'
}

// A meter counts the bytes that the scanners look at, so that a test can tell how the time grows without a clock. Only the
// tests read it, and a nil meter counts nothing.
type meter struct {
	scanned int
	// longest is the length of the longest text that the steps of Autop worked on.
	longest int
	// budget, if it is not 0, stops a run that looks at more bytes, so that a test of a slow input fails soon.
	budget int
}

type overBudget struct{}

func (m *meter) look(n int) {
	if m != nil {
		m.scanned += n
		if m.budget > 0 && m.scanned > m.budget {
			panic(overBudget{})
		}
	}
}

func (m *meter) text(n int) {
	if m != nil {
		m.longest = max(m.longest, n)
	}
}

// skipSpace returns the end of the white space that starts at s[i].
func skipSpace(m *meter, s string, i int) int {
	j := i
	for j < len(s) && isSpace(s[j]) {
		j++
	}
	m.look(j - i + 1)
	return j
}

// A searcher finds the first sep at or after a position. It remembers the last answer, so that searches from positions that
// do not go back look at each stretch of s once.
type searcher struct {
	m        *meter
	s, sep   string
	from, at int
	known    bool
}

func (f *searcher) next(x int) int {
	if x > len(f.s) {
		return -1
	}
	if f.known && x >= f.from && (f.at < 0 || x <= f.at) {
		return f.at
	}
	i := strings.Index(f.s[x:], f.sep)
	if i < 0 {
		f.m.look(len(f.s) - x)
		f.from, f.at, f.known = x, -1, true
		return -1
	}
	f.m.look(i + len(f.sep))
	f.from, f.at, f.known = x, x+i, true
	return x + i
}

// An editor makes a new text from s by replacing stretches of it, in order from the left.
type editor struct {
	s       string
	b       strings.Builder
	pos     int
	changed bool
}

func (e *editor) replace(start, end int, with string) {
	e.b.WriteString(e.s[e.pos:start])
	e.b.WriteString(with)
	e.pos = end
	e.changed = true
}

func (e *editor) result() string {
	if !e.changed {
		return e.s
	}
	e.b.WriteString(e.s[e.pos:])
	return e.b.String()
}
