package wpformat

import (
	"strconv"
	"strings"
)

const (
	preTag = "<pre wp-pre-tag-"
	wpnl   = "<!-- wpnl -->"
)

// Autop returns what wpautop of WordPress 7.1.4 makes of s, with line breaks turned into <br />. WordPress runs it on the
// body of a post that has no blocks. WordPress takes time that is the square of the length of s for some texts; Autop takes
// time that is proportional to it. The two differ only where s has "wp-pre-tag-", the text that WordPress puts in place of
// a <pre> element, and where a regular expression of PHP gives up on a very long text and WordPress makes an empty text.
func Autop(s string) string {
	return autop(s, nil)
}

type autopper struct {
	s    string
	m    *meter
	pres []string
}

func autop(s string, m *meter) string {
	m.text(len(s))
	i := 0
	for i < len(s) && isTrim(s[i]) {
		i++
	}
	m.look(i + 1)
	if i == len(s) {
		return ""
	}
	a := &autopper{s: s + "\n", m: m}
	// The steps are the ones of wpautop, in its order. The one that makes three line breaks or more into two is not
	// here, because splitting into paragraphs reads them as one break.
	for _, step := range []func(){
		a.hidePres,
		a.joinBrs,
		a.breakBlocks,
		a.unifyNewlines,
		a.markNewlines,
		a.collapseOptions,
		a.collapseObjects,
		a.collapseMedia,
		a.collapseFigcaptions,
		a.paragraphs,
		a.dropEmptyParagraphs,
		a.closeParagraphs,
		a.unwrapBlockTags,
		a.unwrapListItems,
		a.moveBlockquotes,
		a.dropParagraphStarts,
		a.dropParagraphEnds,
		a.protectRawText,
		a.normalizeBrs,
		a.breakLines,
		a.restoreRawText,
		a.dropBrAfterBlocks,
		a.dropBrBeforeBlocks,
		a.trimEnd,
		a.restorePres,
		a.restoreNewlines,
	} {
		a.m.text(len(a.s))
		step()
	}
	return a.s
}

// hidePres puts a placeholder in place of each <pre> element up to its end tag. A stretch of text with an end tag and
// without a start tag loses the end tag.
func (a *autopper) hidePres() {
	s := a.s
	if !strings.Contains(s, "<pre") {
		return
	}
	var b strings.Builder
	off := 0
	for {
		k := strings.Index(s[off:], "</pre>")
		if k < 0 {
			a.m.look(len(s) - off)
			break
		}
		a.m.look(k + len("</pre>"))
		end := off + k + len("</pre>")
		part := s[off : off+k]
		if i := strings.Index(part, "<pre"); i < 0 {
			a.m.look(len(part))
			b.WriteString(part)
		} else {
			a.m.look(i + len("<pre"))
			b.WriteString(part[:i])
			b.WriteString(preTag + strconv.Itoa(len(a.pres)) + "></pre>")
			a.pres = append(a.pres, s[off+i:end])
		}
		off = end
	}
	b.WriteString(s[off:])
	a.s = b.String()
}

// brTag returns the end of <br, white space, an optional slash, and ">" at s[i], or -1.
func (a *autopper) brTag(s string, i int) int {
	j := skipSpace(a.m, s, i+len("<br"))
	if j < len(s) && s[j] == '/' {
		j++
	}
	if j < len(s) && s[j] == '>' {
		return j + 1
	}
	return -1
}

// joinBrs makes two <br> tags, and the white space between them, into two line breaks.
func (a *autopper) joinBrs() {
	s := a.s
	e := editor{s: s}
	for i := 0; ; {
		k := strings.Index(s[i:], "<br")
		if k < 0 {
			a.m.look(len(s) - i)
			break
		}
		a.m.look(k + len("<br"))
		start := i + k
		i = start + len("<br")
		end := a.brTag(s, start)
		if end < 0 {
			continue
		}
		next := skipSpace(a.m, s, end)
		if !strings.HasPrefix(s[next:], "<br") {
			continue
		}
		if end = a.brTag(s, next); end < 0 {
			continue
		}
		e.replace(start, end, "\n\n")
		i = end
	}
	a.s = e.result()
}

// breakBlocks puts two line breaks before the start of a block element, after its end, and after <hr>.
func (a *autopper) breakBlocks() {
	s := a.s
	e := editor{s: s}
	for i := 0; ; {
		k := strings.IndexByte(s[i:], '<')
		if k < 0 {
			a.m.look(len(s) - i)
			break
		}
		a.m.look(k + 1)
		start := i + k
		i = start + 1
		if blockOpen(s, start) {
			e.replace(start, start, "\n\n")
		}
		if end := blockClose(s, start); end >= 0 {
			e.replace(end, end, "\n\n")
		} else if end := hrTag(a.m, s, start); end >= 0 {
			e.replace(end, end, "\n\n")
		}
	}
	a.s = e.result()
}

func (a *autopper) unifyNewlines() {
	if strings.Contains(a.s, "\r") {
		a.m.look(2 * len(a.s))
		a.s = strings.ReplaceAll(strings.ReplaceAll(a.s, "\r\n", "\n"), "\r", "\n")
	}
}

// markNewlines puts a placeholder in place of each line break inside an element or a comment.
func (a *autopper) markNewlines() {
	s := a.s
	e := editor{s: s}
	for i := 0; ; {
		k := strings.IndexByte(s[i:], '<')
		if k < 0 {
			a.m.look(len(s) - i)
			break
		}
		a.m.look(k + 1)
		start := i + k
		end := elementEnd(a.m, s, start)
		i = end
		if !strings.Contains(s[start:end], "\n") {
			a.m.look(end - start)
			continue
		}
		a.m.look(2 * (end - start))
		e.replace(start, end, strings.ReplaceAll(s[start:end], "\n", " "+wpnl+" "))
	}
	a.s = e.result()
}

// paragraphs makes a paragraph of each stretch of text between breaks, and a break is a line break and white space up to
// a last line break.
func (a *autopper) paragraphs() {
	s := a.s
	var b strings.Builder
	b.Grow(len(s) + len(s)/8)
	piece := func(p string) {
		if p != "" {
			b.WriteString("<p>")
			b.WriteString(strings.Trim(p, "\n"))
			b.WriteString("</p>\n")
		}
	}
	pos := 0
	for i := 0; i < len(s); {
		k := strings.IndexByte(s[i:], '\n')
		if k < 0 {
			a.m.look(len(s) - i)
			break
		}
		a.m.look(k + 1)
		lf := i + k
		last := -1
		j := lf + 1
		for j < len(s) && isSpace(s[j]) {
			if s[j] == '\n' {
				last = j
			}
			j++
		}
		a.m.look(j - lf)
		i = j
		if last >= 0 {
			piece(s[pos:lf])
			pos = last + 1
		}
	}
	piece(s[pos:])
	a.s = b.String()
}

// dropEmptyParagraphs takes away <p>, white space, and </p>.
func (a *autopper) dropEmptyParagraphs() {
	s := a.s
	e := editor{s: s}
	for i := 0; ; {
		k := strings.Index(s[i:], "<p>")
		if k < 0 {
			a.m.look(len(s) - i)
			break
		}
		a.m.look(k + len("<p>"))
		start := i + k
		i = start + len("<p>")
		j := skipSpace(a.m, s, i)
		if strings.HasPrefix(s[j:], "</p>") {
			i = j + len("</p>")
			e.replace(start, i, "")
		}
	}
	a.s = e.result()
}

// closeParagraphs closes a paragraph that is cut off by the end tag of a div, an address, or a form, if there is only text
// in it.
func (a *autopper) closeParagraphs() {
	s := a.s
	e := editor{s: s}
	for i := 0; ; {
		k := strings.Index(s[i:], "<p>")
		if k < 0 {
			a.m.look(len(s) - i)
			break
		}
		a.m.look(k + len("<p>"))
		start := i + k
		i = start + len("<p>")
		lt := strings.IndexByte(s[i:], '<')
		if lt < 0 {
			a.m.look(len(s) - i)
			break
		}
		a.m.look(lt + 1)
		if lt == 0 {
			continue
		}
		at := i + lt
		for _, name := range [...]string{"address", "div", "form"} {
			if strings.HasPrefix(s[at:], "</"+name+">") {
				e.replace(at, at, "</p>")
				i = at + len("</"+name+">")
				break
			}
		}
	}
	a.s = e.result()
}
