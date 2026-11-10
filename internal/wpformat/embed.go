package wpformat

import "strings"

// A URLSpan is the range of a URL in a text.
type URLSpan struct {
	Start, End int
}

// AutoembedURLs returns the ranges of the URLs that the automatic embeds of WordPress 7.1.4 (autoembed of WP_Embed) try
// to embed, from the left: a URL that is alone on its line, and a URL that is alone in a paragraph element. s is HTML:
// a line break in an element does not end a line. Whether WordPress can embed a URL is not decided here.
func AutoembedURLs(s string) []URLSpan {
	return autoembedURLs(s, nil)
}

func autoembedURLs(s string, m *meter) []URLSpan {
	m.look(len(s))
	if !strings.Contains(s, "://") {
		return nil
	}
	return mergeURLSpans(lineURLs(m, s), paragraphURLs(m, s))
}

// urlScheme returns the length of "http://" or "https://", in any case, at the start of s, or 0.
func urlScheme(s string) int {
	switch {
	case hasPrefixFold(s, "https://"):
		return len("https://")
	case hasPrefixFold(s, "http://"):
		return len("http://")
	}
	return 0
}

func isURLByte(c byte) bool {
	return !isSpace(c) && c != '<' && c != '>' && c != '"'
}

// urlEnd returns the end of the URL that starts at s[i], where the scheme has n bytes, or -1 if nothing follows the scheme.
func urlEnd(m *meter, s string, i, n int) int {
	end := i + n
	for end < len(s) && isURLByte(s[end]) {
		end++
	}
	m.look(end - i + 1)
	if end == i+n {
		return -1
	}
	return end
}

func mergeURLSpans(a, b []URLSpan) []URLSpan {
	if len(b) == 0 {
		return a
	}
	if len(a) == 0 {
		return b
	}
	out := make([]URLSpan, 0, len(a)+len(b))
	for len(a) > 0 && len(b) > 0 {
		switch {
		case a[0].Start < b[0].Start:
			out, a = append(out, a[0]), a[1:]
		case a[0].Start > b[0].Start:
			out, b = append(out, b[0]), b[1:]
		default:
			out, a, b = append(out, a[0]), a[1:], b[1:]
		}
	}
	out = append(out, a...)
	return append(out, b...)
}

// lineURLs returns the URLs that are alone on a line: after white space that starts at the start of a line or of s, and
// before white space that has a line break in it or reaches the end of s. A line break in an element is not one.
func lineURLs(m *meter, s string) []URLSpan {
	var out []URLSpan
	for i := 0; i < len(s); {
		end := len(s)
		lt := strings.IndexByte(s[i:], '<')
		if lt >= 0 {
			end = i + lt
		}
		m.look(end - i + 1)
		out = textLineURLs(m, s, i, end, out)
		if lt < 0 {
			break
		}
		i = elementEnd(m, s, end)
	}
	return out
}

// textLineURLs appends the URLs of lineURLs that are in the text s[start:end], between elements.
func textLineURLs(m *meter, s string, start, end int, out []URLSpan) []URLSpan {
	atLine := start == 0
	for pos := start; pos < end; {
		w := pos
		for w < end && isSpace(s[w]) {
			if s[w] == '\n' {
				atLine = true
			}
			w++
		}
		m.look(w - pos + 1)
		if w == end {
			break
		}
		if n := urlScheme(s[w:end]); atLine && n > 0 {
			if v := urlEnd(m, s, w, n); v >= 0 {
				after, lineBreak := v, false
				for after < end && isSpace(s[after]) {
					if s[after] == '\n' {
						lineBreak = true
					}
					after++
				}
				m.look(after - v + 1)
				if lineBreak || after == len(s) {
					out = append(out, URLSpan{w, v})
					pos, atLine = v, false
					continue
				}
			}
		}
		lf := strings.IndexByte(s[w:end], '\n')
		if lf < 0 {
			m.look(end - w)
			break
		}
		m.look(lf + 1)
		pos, atLine = w+lf, false
	}
	return out
}

// paragraphURLs returns the URLs in "<p>", white space, a URL, white space, and "</p>". "<p" may be followed by ">" or by
// a space and attributes that end at the first ">". A line break in an element ends the attributes, as the text for it has
// a ">". The white space and the URL have no such line break, and the URL may be in an element, if the "<p" is.
func paragraphURLs(m *meter, s string) []URLSpan {
	var out []URLSpan
	gt := searcher{m: m, s: s, sep: ">"}
	lf := searcher{m: m, s: s, sep: "\n"}
	parts := partWalker{m: m, s: s}
	lastAfterTag := -1
	var last paragraph
	for i := 0; i < len(s); {
		k := strings.IndexByte(s[i:], '<')
		if k < 0 {
			m.look(len(s) - i)
			break
		}
		m.look(k + 1)
		i += k
		if i+2 < len(s) && s[i+1]|0x20 == 'p' {
			tagEnd := -1
			switch s[i+2] {
			case '>':
				tagEnd = i + 2
			case ' ':
				tagEnd = firstOf(gt.next(i+3), lf.next(i+3))
			}
			if tagEnd >= 0 {
				if tagEnd+1 != lastAfterTag {
					lastAfterTag = tagEnd + 1
					last = paragraphAt(m, s, lastAfterTag, parts.inside(lastAfterTag))
				}
				if last.ok {
					out = append(out, URLSpan{last.start, last.end})
					i = last.after
					continue
				}
			}
		}
		i++
	}
	return out
}

// firstOf returns the smaller of two positions, where -1 is no position.
func firstOf(a, b int) int {
	switch {
	case a < 0:
		return b
	case b < 0:
		return a
	}
	return min(a, b)
}

type paragraph struct {
	ok         bool
	start, end int
	// after is the end of "</p>".
	after int
}

// paragraphAt reads white space, a URL, white space, and "</p>" at s[i:]. In an element, a line break is not white space.
func paragraphAt(m *meter, s string, i int, inElement bool) paragraph {
	space := func(from int) int {
		end := from
		for end < len(s) && isSpace(s[end]) && (!inElement || s[end] != '\n') {
			end++
		}
		m.look(end - from + 1)
		return end
	}
	start := space(i)
	n := urlScheme(s[start:])
	if n == 0 {
		return paragraph{}
	}
	end := urlEnd(m, s, start, n)
	if end < 0 {
		return paragraph{}
	}
	tag := space(end)
	if !hasPrefixFold(s[tag:], "</p>") {
		return paragraph{}
	}
	return paragraph{ok: true, start: start, end: end, after: tag + len("</p>")}
}

// A partWalker tells where the elements of s are, from the left.
type partWalker struct {
	m          *meter
	s          string
	start, end int
	element    bool
}

// inside reports whether p is in an element. The positions that it is asked about do not decrease.
func (w *partWalker) inside(p int) bool {
	for w.end <= p {
		if w.end >= len(w.s) {
			return false
		}
		w.start = w.end
		if w.s[w.start] == '<' {
			w.element = true
			w.end = elementEnd(w.m, w.s, w.start)
			continue
		}
		w.element = false
		w.end = len(w.s)
		if lt := strings.IndexByte(w.s[w.start:], '<'); lt >= 0 {
			w.end = w.start + lt
		}
		w.m.look(w.end - w.start + 1)
	}
	return w.element
}
