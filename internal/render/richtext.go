package render

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// markOrder is the order in which the marks of a text nest, outermost first.
var markOrder = [...]content.MarkType{
	content.MarkLink, content.MarkHighlight, content.MarkBold, content.MarkItalic, content.MarkUnderline,
	content.MarkStrike, content.MarkSubscript, content.MarkSuperscript, content.MarkCode,
}

// tag is a mark of a text as HTML: its index in markOrder, and its start and end tags.
type tag struct {
	order      int
	start, end string
}

// richText returns the HTML of rt, rich text at path. From one text to the next, the marks that both have from the
// outermost on, compared by their start tags, stay open, and the others are closed and opened; the hardBreaks between
// the texts sit inside only the marks that stay open.
func (w *walker) richText(rt content.RichText, path string) string {
	var b strings.Builder
	var open []tag
	breaks := 0 // hardBreaks that wait for the marks of the next text
	for i, n := range rt {
		p := path + "/" + strconv.Itoa(i)
		switch n.Type {
		case content.InlineText:
			if n.Text == "" {
				continue
			}
			tags := w.marks(n.Marks, p)
			kept := 0
			for kept < len(open) && kept < len(tags) && open[kept].start == tags[kept].start {
				kept++
			}
			closeTags(&b, open[kept:])
			b.WriteString(strings.Repeat("<br>", breaks))
			breaks = 0
			for _, t := range tags[kept:] {
				b.WriteString(t.start)
			}
			open = tags
			b.WriteString(escapeText(n.Text))
		case content.InlineHardBreak:
			breaks++
		default:
			w.warnf(p, "unknown inline type %q; left out", n.Type)
		}
	}
	closeTags(&b, open)
	b.WriteString(strings.Repeat("<br>", breaks))
	return b.String()
}

// closeTags writes the end tags of tags, innermost first.
func closeTags(b *strings.Builder, tags []tag) {
	for i := len(tags) - 1; i >= 0; i-- {
		b.WriteString(tags[i].end)
	}
}

// marks returns the tags of ms, the marks of a text at path, in markOrder. It leaves out the marks that render nothing
// and, of the marks of one type, all but the first.
func (w *walker) marks(ms []content.Mark, path string) []tag {
	var tags []tag
	var seen [len(markOrder)]bool
	for j, m := range ms {
		p := path + "/marks/" + strconv.Itoa(j)
		order := slices.Index(markOrder[:], m.Type)
		if order < 0 {
			w.warnf(p, "unknown mark type %q; left out", m.Type)
			continue
		}
		if seen[order] {
			continue
		}
		seen[order] = true
		if start, end, ok := w.markTags(m, p); ok {
			tags = append(tags, tag{order, start, end})
		}
	}
	slices.SortStableFunc(tags, func(a, b tag) int { return cmp.Compare(a.order, b.order) })
	return tags
}

// markTags returns the start and end tags of m, a mark at path; ok is false if it renders nothing.
func (w *walker) markTags(m content.Mark, path string) (start, end string, ok bool) {
	switch m.Type {
	case content.MarkLink:
		if m.Link == nil {
			w.warnf(path, "link without a target; rendered without the link")
			return "", "", false
		}
		l := w.link(m.Link, path)
		if l == nil {
			return "", "", false
		}
		start = `<a href="` + escapeAttr(string(l.Href)) + `"`
		if l.Target != "" {
			start += ` target="` + escapeAttr(l.Target) + `"`
		}
		if l.Rel != "" {
			start += ` rel="` + escapeAttr(l.Rel) + `"`
		}
		return start + ">", "</a>", true
	case content.MarkHighlight:
		if h := m.Highlight; h != nil && h.Variant != "" {
			if w.r.highlights[h.Variant] {
				return `<mark class="is-variant-` + escapeAttr(h.Variant) + `">`, "</mark>", true
			}
			w.warnf(path, "the theme has no highlight variant %q; rendered without its class", h.Variant)
		}
		return "<mark>", "</mark>", true
	case content.MarkBold:
		return "<strong>", "</strong>", true
	case content.MarkItalic:
		return "<em>", "</em>", true
	case content.MarkUnderline:
		return "<u>", "</u>", true
	case content.MarkStrike:
		return "<s>", "</s>", true
	case content.MarkSubscript:
		return "<sub>", "</sub>", true
	case content.MarkSuperscript:
		return "<sup>", "</sup>", true
	case content.MarkCode:
		return "<code>", "</code>", true
	}
	return "", "", false
}

var (
	// Not template.HTMLEscapeString, which also escapes quotes in text.
	textEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\x00", string(utf8.RuneError))
	// Unlike html/template, it keeps "+", which reads the same as its "&#43;".
	attrEscaper = strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;", "\x00", string(utf8.RuneError),
	)
)

func escapeText(s string) string {
	return textEscaper.Replace(s)
}

// escapeAttr escapes s for the value of an attribute in double or single quotes.
func escapeAttr(s string) string {
	return attrEscaper.Replace(s)
}

// plainText returns the texts of rt without their marks, with a space for each hardBreak.
func plainText(rt content.RichText) string {
	var b strings.Builder
	for _, n := range rt {
		switch n.Type {
		case content.InlineText:
			b.WriteString(n.Text)
		case content.InlineHardBreak:
			b.WriteByte(' ')
		}
	}
	return b.String()
}
