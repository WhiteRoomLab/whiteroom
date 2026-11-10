package wpformat

import (
	"strconv"
	"strings"
)

const keepNewline = "<WPPreserveNewline />"

// A tagFinder finds the first tag at or after from; ok is unset if no tag is left, or if no later tag can match either.
type tagFinder func(s string, from int) (start, end int, ok bool)

func (a *autopper) literal(lit string) tagFinder {
	return func(s string, from int) (int, int, bool) {
		i := strings.Index(s[from:], lit)
		if i < 0 {
			a.m.look(len(s) - from)
			return 0, 0, false
		}
		a.m.look(i + len(lit))
		return from + i, from + i + len(lit), true
	}
}

// openTag finds prefix and the rest of the tag, up to the first ">".
func (a *autopper) openTag(prefix string) tagFinder {
	return func(s string, from int) (int, int, bool) {
		i := strings.Index(s[from:], prefix)
		if i < 0 {
			a.m.look(len(s) - from)
			return 0, 0, false
		}
		start := from + i
		g := strings.IndexByte(s[start+len(prefix):], '>')
		if g < 0 {
			a.m.look(len(s) - from)
			return 0, 0, false
		}
		end := start + len(prefix) + g + 1
		a.m.look(end - from)
		return start, end, true
	}
}

// nameAt returns the length of the first of names that s starts with, or 0.
func nameAt(s string, names ...string) int {
	for _, n := range names {
		if strings.HasPrefix(s, n) {
			return len(n)
		}
	}
	return 0
}

// paramEmbed finds a start tag or an end tag of param or embed.
func (a *autopper) paramEmbed(s string, from int) (int, int, bool) {
	for i := from; ; {
		k := strings.IndexByte(s[i:], '<')
		if k < 0 {
			a.m.look(len(s) - i)
			return 0, 0, false
		}
		a.m.look(k + 1)
		start := i + k
		i = start + 1
		n := i
		if n < len(s) && s[n] == '/' {
			n++
		}
		l := nameAt(s[n:], "embed", "param")
		if l == 0 {
			continue
		}
		g := strings.IndexByte(s[n+l:], '>')
		if g < 0 {
			a.m.look(len(s) - n)
			return 0, 0, false
		}
		a.m.look(g + 1)
		return start, n + l + g + 1, true
	}
}

// audioVideoOpen finds <audio or [audio, and <video or [video, up to the first ">" or "]".
func (a *autopper) audioVideoOpen(s string, from int) (int, int, bool) {
	for i := from; ; {
		k := strings.IndexAny(s[i:], "<[")
		if k < 0 {
			a.m.look(len(s) - i)
			return 0, 0, false
		}
		a.m.look(k + 1)
		start := i + k
		i = start + 1
		l := nameAt(s[i:], "audio", "video")
		if l == 0 {
			continue
		}
		g := strings.IndexAny(s[i+l:], ">]")
		if g < 0 {
			a.m.look(len(s) - i)
			return 0, 0, false
		}
		a.m.look(g + 1)
		return start, i + l + g + 1, true
	}
}

// audioVideoClose finds </audio>, </video>, [/audio], and [/video], in which the brackets may be mixed.
func (a *autopper) audioVideoClose(s string, from int) (int, int, bool) {
	for i := from; ; {
		k := strings.IndexAny(s[i:], "<[")
		if k < 0 {
			a.m.look(len(s) - i)
			return 0, 0, false
		}
		a.m.look(k + 1)
		start := i + k
		i = start + 1
		if i >= len(s) || s[i] != '/' {
			continue
		}
		l := nameAt(s[i+1:], "audio", "video")
		end := i + 1 + l
		if l > 0 && end < len(s) && (s[end] == '>' || s[end] == ']') {
			return start, end + 1, true
		}
	}
}

// sourceTrack finds a start tag of source or track.
func (a *autopper) sourceTrack(s string, from int) (int, int, bool) {
	for i := from; ; {
		k := strings.IndexByte(s[i:], '<')
		if k < 0 {
			a.m.look(len(s) - i)
			return 0, 0, false
		}
		a.m.look(k + 1)
		start := i + k
		i = start + 1
		l := nameAt(s[i:], "source", "track")
		if l == 0 {
			continue
		}
		g := strings.IndexByte(s[i+l:], '>')
		if g < 0 {
			a.m.look(len(s) - i)
			return 0, 0, false
		}
		a.m.look(g + 1)
		return start, i + l + g + 1, true
	}
}

// trimAround takes away the white space before the tags that find returns, and the white space after them.
func (a *autopper) trimAround(before, after bool, find tagFinder) {
	s := a.s
	e := editor{s: s}
	for from := 0; ; {
		start, end, ok := find(s, from)
		if !ok {
			break
		}
		cut, next := start, end
		if before {
			for cut > e.pos && isSpace(s[cut-1]) {
				cut--
			}
			a.m.look(start - cut + 1)
		}
		if after {
			next = skipSpace(a.m, s, end)
		}
		if cut != start || next != end {
			e.replace(cut, next, s[start:end])
		}
		from = next
	}
	a.s = e.result()
}

// collapseOptions takes away the white space before <option and after </option>.
func (a *autopper) collapseOptions() {
	if !strings.Contains(a.s, "<option") {
		return
	}
	a.trimAround(true, false, a.literal("<option"))
	a.trimAround(false, true, a.literal("</option>"))
}

// collapseObjects takes away the white space after <object>, before </object>, and around param and embed.
func (a *autopper) collapseObjects() {
	if !strings.Contains(a.s, "</object>") {
		return
	}
	a.trimAround(false, true, a.openTag("<object"))
	a.trimAround(true, false, a.literal("</object>"))
	a.trimAround(true, true, a.paramEmbed)
}

// collapseMedia takes away the white space after the start of audio and video, before their end, and around source and
// track.
func (a *autopper) collapseMedia() {
	if !strings.Contains(a.s, "<source") && !strings.Contains(a.s, "<track") {
		return
	}
	a.trimAround(false, true, a.audioVideoOpen)
	a.trimAround(true, false, a.audioVideoClose)
	a.trimAround(true, true, a.sourceTrack)
}

// collapseFigcaptions takes away the white space before <figcaption> and after </figcaption>.
func (a *autopper) collapseFigcaptions() {
	if !strings.Contains(a.s, "<figcaption") {
		return
	}
	a.trimAround(true, false, a.openTag("<figcaption"))
	a.trimAround(false, true, a.literal("</figcaption>"))
}

// blockTagName returns the position after "<" or "</" at s[i] if the name of a block starts there.
func blockTagName(s string, i int) (int, bool) {
	j := i + 1
	if j < len(s) && s[j] == '/' {
		j++
	}
	return j, hasBlockPrefix(s[j:])
}

// unwrapBlockTags takes <p> and </p> away around a tag of a block, if white space is all that comes between them.
func (a *autopper) unwrapBlockTags() {
	a.unwrapP(true)
}

// dropParagraphStarts takes <p> away before a tag of a block, with the white space after it.
func (a *autopper) dropParagraphStarts() {
	a.unwrapP(false)
}

func (a *autopper) unwrapP(closing bool) {
	s := a.s
	e := editor{s: s}
	gt := searcher{m: a.m, s: s, sep: ">"}
	for i := 0; ; {
		k := strings.Index(s[i:], "<p>")
		if k < 0 {
			a.m.look(len(s) - i)
			break
		}
		a.m.look(k + len("<p>"))
		start := i + k
		i = start + len("<p>")
		tag := skipSpace(a.m, s, i)
		if tag >= len(s) || s[tag] != '<' {
			continue
		}
		name, ok := blockTagName(s, tag)
		if !ok {
			continue
		}
		g := gt.next(name)
		if g < 0 {
			break
		}
		end := g + 1
		if closing {
			next := skipSpace(a.m, s, end)
			if !strings.HasPrefix(s[next:], "</p>") {
				continue
			}
			end = next + len("</p>")
		}
		e.replace(start, end, s[tag:g+1])
		i = end
	}
	a.s = e.result()
}

// dropParagraphEnds takes </p> away after a tag of a block.
func (a *autopper) dropParagraphEnds() {
	a.dropAfterTag("</p>")
}

// dropBrAfterBlocks takes <br /> away after a tag of a block.
func (a *autopper) dropBrAfterBlocks() {
	a.dropAfterTag("<br />")
}

// dropAfterTag takes the white space and tail away after a tag of a block.
func (a *autopper) dropAfterTag(tail string) {
	s := a.s
	e := editor{s: s}
	gt := searcher{m: a.m, s: s, sep: ">"}
	// Tags that start at different places can end at the same ">", and look at the same white space.
	lastEnd, lastTail := -1, -1
	for i := 0; ; {
		k := strings.IndexByte(s[i:], '<')
		if k < 0 {
			a.m.look(len(s) - i)
			break
		}
		a.m.look(k + 1)
		start := i + k
		i = start + 1
		name, ok := blockTagName(s, start)
		if !ok {
			continue
		}
		g := gt.next(name)
		if g < 0 {
			break
		}
		if g != lastEnd {
			lastEnd, lastTail = g, -1
			if next := skipSpace(a.m, s, g+1); strings.HasPrefix(s[next:], tail) {
				lastTail = next + len(tail)
			}
		}
		if lastTail < 0 {
			continue
		}
		e.replace(start, lastTail, s[start:g+1])
		i = lastTail
	}
	a.s = e.result()
}

// unwrapListItems takes <p> and </p> away around <li, if the </p> is in the same line.
func (a *autopper) unwrapListItems() {
	s := a.s
	e := editor{s: s}
	ends := searcher{m: a.m, s: s, sep: "</p>"}
	lines := searcher{m: a.m, s: s, sep: "\n"}
	for i := 0; ; {
		k := strings.Index(s[i:], "<p><li")
		if k < 0 {
			a.m.look(len(s) - i)
			break
		}
		a.m.look(k + len("<p><li"))
		start := i + k
		i = start + len("<p>")
		end := ends.next(start + len("<p><li") + 1)
		if end < 0 {
			break
		}
		if lf := lines.next(start + len("<p><li")); lf >= 0 && lf < end {
			continue
		}
		e.replace(start, end+len("</p>"), s[start+len("<p>"):end])
		i = end + len("</p>")
	}
	a.s = e.result()
}

// hasPrefixFold reports whether s starts with lower, which has only lower case letters, in any case.
func hasPrefixFold(s, lower string) bool {
	if len(s) < len(lower) {
		return false
	}
	for i := range len(lower) {
		c := s[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != lower[i] {
			return false
		}
	}
	return true
}

// moveBlockquotes moves <p> from before <blockquote> to after it, and </p> from after </blockquote> to before it.
func (a *autopper) moveBlockquotes() {
	s := a.s
	e := editor{s: s}
	quotes := [2]searcher{{m: a.m, s: s, sep: `"`}, {m: a.m, s: s, sep: "'"}}
	for i := 0; ; {
		k := strings.IndexByte(s[i:], '<')
		if k < 0 {
			a.m.look(len(s) - i)
			break
		}
		a.m.look(k + 1)
		start := i + k
		i = start + 1
		if !hasPrefixFold(s[start:], "<p><blockquote") {
			continue
		}
		from := start + len("<p><blockquote")
		end := a.attributesEnd(&quotes, from)
		if end < 0 {
			continue
		}
		e.replace(start, end+1, "<blockquote"+s[from:end]+"><p>")
		i = end + 1
	}
	a.s = strings.ReplaceAll(e.result(), "</blockquote></p>", "</p></blockquote>")
}

// attributesEnd returns the ">" that ends the attributes that start at s[i], or -1. A ">" in quotes is not the end, and a
// quote with no quote to end it ends the search.
func (a *autopper) attributesEnd(quotes *[2]searcher, i int) int {
	s := quotes[0].s
	for {
		k := strings.IndexAny(s[i:], `>"'`)
		if k < 0 {
			a.m.look(len(s) - i)
			return -1
		}
		a.m.look(k + 1)
		i += k
		if s[i] == '>' {
			return i
		}
		q := &quotes[0]
		if s[i] == '\'' {
			q = &quotes[1]
		}
		j := q.next(i + 1)
		if j < 0 {
			return -1
		}
		i = j + 1
	}
}

// protectRawText puts a placeholder in place of each line break in script, style, svg, and math elements.
func (a *autopper) protectRawText() {
	s := a.s
	e := editor{s: s}
	names := [...]string{"math", "script", "style", "svg"}
	var ends [len(names)]searcher
	var dead [len(names)]bool
	for i, n := range names {
		ends[i] = searcher{m: a.m, s: s, sep: "</" + n + ">"}
	}
	for i := 0; ; {
		k := strings.IndexByte(s[i:], '<')
		if k < 0 {
			a.m.look(len(s) - i)
			break
		}
		a.m.look(k + 1)
		start := i + k
		i = start + 1
		for j, n := range names {
			if dead[j] || !strings.HasPrefix(s[start+1:], n) {
				continue
			}
			at := ends[j].next(start + 1 + len(n))
			if at < 0 {
				dead[j] = true
				break
			}
			end := at + len(ends[j].sep)
			if region := s[start:end]; strings.Contains(region, "\n") {
				a.m.look(2 * len(region))
				e.replace(start, end, strings.ReplaceAll(region, "\n", keepNewline))
			}
			i = end
			break
		}
	}
	a.s = e.result()
}

// normalizeBrs writes <br> and <br/> as <br />.
func (a *autopper) normalizeBrs() {
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
		switch {
		case strings.HasPrefix(s[i:], ">"):
			e.replace(start, i+1, "<br />")
		case strings.HasPrefix(s[i:], "/>"):
			e.replace(start, i+2, "<br />")
		}
	}
	a.s = e.result()
}

// breakLines puts <br /> before a line break that has none before it. The white space that ends in the line break goes with
// it, but not the first byte of that white space if a <br /> comes right before it.
func (a *autopper) breakLines() {
	s := a.s
	e := editor{s: s}
	for i := 0; i < len(s); {
		if !isSpace(s[i]) {
			i++
			a.m.look(1)
			continue
		}
		run := i
		last := -1
		for i < len(s) && isSpace(s[i]) {
			if s[i] == '\n' {
				last = i
			}
			i++
		}
		a.m.look(i - run + 1)
		if last < 0 {
			continue
		}
		from := run
		if run >= len("<br />") && s[run-len("<br />"):run] == "<br />" {
			from++
		}
		if from <= last {
			e.replace(from, last+1, "<br />\n")
		}
	}
	a.s = e.result()
}

func (a *autopper) restoreRawText() {
	if strings.Contains(a.s, keepNewline) {
		a.m.look(2 * len(a.s))
		a.s = strings.ReplaceAll(a.s, keepNewline, "\n")
	}
}

// dropBrBeforeBlocks takes <br /> away before a tag of a list or a paragraph, if white space is all that comes between.
func (a *autopper) dropBrBeforeBlocks() {
	s := a.s
	e := editor{s: s}
	gt := searcher{m: a.m, s: s, sep: ">"}
	for i := 0; ; {
		k := strings.Index(s[i:], "<br />")
		if k < 0 {
			a.m.look(len(s) - i)
			break
		}
		a.m.look(k + len("<br />"))
		start := i + k
		i = start + len("<br />")
		tag := skipSpace(a.m, s, i)
		if tag >= len(s) || s[tag] != '<' {
			continue
		}
		name := tag + 1
		if name < len(s) && s[name] == '/' {
			name++
		}
		if !hasListPrefix(s[name:]) {
			continue
		}
		g := gt.next(name)
		if g < 0 {
			break
		}
		e.replace(start, i, "")
		i = g + 1
	}
	a.s = e.result()
}

// trimEnd takes a line break away before the </p> that ends the text, which may be followed by a line break.
func (a *autopper) trimEnd() {
	s := a.s
	switch {
	case strings.HasSuffix(s, "\n</p>"):
		a.s = s[:len(s)-len("\n</p>")] + "</p>"
	case strings.HasSuffix(s, "\n</p>\n"):
		a.s = s[:len(s)-len("\n</p>\n")] + "</p>\n"
	}
}

// restorePres puts the <pre> elements back in place of their placeholders.
func (a *autopper) restorePres() {
	if len(a.pres) == 0 {
		return
	}
	s := a.s
	e := editor{s: s}
	const digits = 9
	for i := 0; ; {
		k := strings.Index(s[i:], preTag)
		if k < 0 {
			a.m.look(len(s) - i)
			break
		}
		a.m.look(k + len(preTag))
		start := i + k
		i = start + len(preTag)
		j := i
		for j < len(s) && '0' <= s[j] && s[j] <= '9' && j-i <= digits {
			j++
		}
		if j == i || j-i > digits || s[i] == '0' && j-i > 1 || !strings.HasPrefix(s[j:], "></pre>") {
			continue
		}
		n, err := strconv.Atoi(s[i:j])
		if err != nil || n >= len(a.pres) {
			continue
		}
		i = j + len("></pre>")
		e.replace(start, i, a.pres[n])
	}
	a.s = e.result()
}

// restoreNewlines turns the placeholders for line breaks in elements back into line breaks, with the spaces around them.
func (a *autopper) restoreNewlines() {
	s := a.s
	if !strings.Contains(s, wpnl) {
		return
	}
	e := editor{s: s}
	// A placeholder with a space on each side is the first to go; the spaces of one of them are not used for another.
	used := 0
	for i := 0; ; {
		k := strings.Index(s[i:], wpnl)
		if k < 0 {
			a.m.look(len(s) - i)
			break
		}
		a.m.look(k + len(wpnl))
		at := i + k
		end := at + len(wpnl)
		i = end
		if at > used && s[at-1] == ' ' && end < len(s) && s[end] == ' ' {
			used = end + 1
			i = used
			e.replace(at-1, used, "\n")
		} else {
			e.replace(at, end, "\n")
		}
	}
	a.s = e.result()
}
