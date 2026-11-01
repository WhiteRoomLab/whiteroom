package build

import (
	"cmp"
	"strings"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// excerptLength is the number of code points that an automatic excerpt keeps of a longer text.
const excerptLength = 120

// pageTitle returns the title of p for <title>: the site name in the language of p alone on the front pages and the
// 404 pages, else the title format of the language, or content.DefaultTitleFormat, with titleValue(p).
func (b *builder) pageTitle(p *page) string {
	name := b.site.Name[p.language]
	if p.front || p.kind == theme.KindNotFound {
		return name
	}
	format := cmp.Or(b.site.SEO.TitleFormat[p.language], content.DefaultTitleFormat)
	return formatTitle(format, b.titleValue(p), name)
}

// formatTitle returns format, a title format such as "{title} | {siteName}", with title and siteName in place of
// {title} and {siteName}.
func formatTitle(format, title, siteName string) string {
	return strings.NewReplacer("{title}", title, "{siteName}", siteName).Replace(format)
}

// titleValue returns the title of p for {title} in the title format: the seo.title of its entry, else its title;
// otherwise heading(p).
func (b *builder) titleValue(p *page) string {
	if p.entry != nil {
		return cmp.Or(p.entry.rev.SEO.Title, p.entry.rev.Title)
	}
	return b.heading(p)
}

// heading returns the title of p itself: the title of its entry, also on the front page and the posts page; the name
// of its term or its author; the label of its type, else its name; "" for the latest posts at the front and the 404
// pages.
func (b *builder) heading(p *page) string {
	switch p.kind {
	case theme.KindEntry, theme.KindPosts:
		if p.entry != nil {
			return p.entry.rev.Title
		}
	case theme.KindTerm:
		return p.term.v.Name
	case theme.KindArchive:
		return b.typeLabel(p.typeName)
	case theme.KindAuthor:
		return p.author.v.Name
	case theme.KindNotFound:
	}
	return ""
}

// typeLabel returns the label of the type name, else its name.
func (b *builder) typeLabel(name string) string {
	return cmp.Or(b.site.Type(name).Label, name)
}

// description returns the description of p on one line (oneLine), or "" if it has none: for the page of an entry, also
// the front page and the posts page, the seo.description of the entry, else its excerpt (excerpt); for a term, its
// description; for an author, the bio in the language of p; for the latest posts at the front, the tagline of the
// language. Every page of a list has that of its first page.
func (b *builder) description(p *page) string {
	switch p.kind {
	case theme.KindEntry, theme.KindPosts:
		if p.entry == nil {
			return oneLine(b.site.Tagline[p.language])
		}
		if s := p.entry.rev.SEO.Description; s != "" {
			return oneLine(s)
		}
		return oneLine(excerpt(p.entry.rev))
	case theme.KindTerm:
		return oneLine(p.term.v.Description)
	case theme.KindAuthor:
		return oneLine(p.author.v.Bio[p.language])
	case theme.KindArchive, theme.KindNotFound:
	}
	return ""
}

// oneLine returns s with each run of white space, such as line breaks and U+3000, as a space, and none at the ends.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// excerpt returns the excerpt of rev, the published revision of an entry: its excerpt as written, else its automatic
// excerpt (autoExcerpt).
func excerpt(rev *content.Revision) string {
	if rev.Excerpt != "" {
		return rev.Excerpt
	}
	return autoExcerpt(rev.Body)
}

// autoExcerpt returns the automatic excerpt of bs, the body of an entry: the texts of its paragraphs, its headings and
// its table cells, and the citations of its quotes after their contents, in the order of the body, also in lists,
// quotes, tables, columns and groups, on one line (oneLine), with a space for each hardBreak and between two blocks.
// Of a text longer than excerptLength code points, it is the first ones, without a space at the end, and "…". The
// other blocks, such as images and their captions, buttons, code, embeds and HTML, are left out.
func autoExcerpt(bs content.Blocks) string {
	var w excerptWriter
	w.blocks(bs)
	s := w.b.String()
	if w.n <= excerptLength {
		return s
	}
	end := 0
	for range excerptLength {
		_, size := utf8.DecodeRuneInString(s[end:])
		end += size
	}
	return strings.TrimRight(s[:end], " ") + "…"
}

// excerptWriter writes the words of texts on one line, with a space between each two, until it has more code points
// than excerptLength.
type excerptWriter struct {
	b strings.Builder
	n int // the code points in b
}

func (w *excerptWriter) full() bool {
	return w.n > excerptLength
}

// blocks writes the texts of bs that autoExcerpt takes.
func (w *excerptWriter) blocks(bs content.Blocks) {
	for i := range bs {
		if w.full() {
			return
		}
		b := &bs[i]
		switch b.Type {
		case content.BlockParagraph, content.BlockHeading, content.BlockTableCell:
			w.text(b.Text)
		case content.BlockList, content.BlockListItem, content.BlockTable, content.BlockTableRow, content.BlockColumns,
			content.BlockColumn, content.BlockGroup:
			w.blocks(b.Children)
		case content.BlockQuote:
			w.blocks(b.Children)
			w.text(b.Attrs.Citation)
		case content.BlockImage, content.BlockButtons, content.BlockButton, content.BlockSeparator, content.BlockSpacer,
			content.BlockCode, content.BlockEmbed, content.BlockFile, content.BlockForm, content.BlockHTML,
			content.BlockLegacy:
		}
	}
}

// text writes the words of rt: of its texts, with a space for each hardBreak.
func (w *excerptWriter) text(rt content.RichText) {
	var s strings.Builder
	for _, x := range rt {
		switch x.Type {
		case content.InlineText:
			s.WriteString(x.Text)
		case content.InlineHardBreak:
			s.WriteByte(' ')
		}
	}
	for word := range strings.FieldsSeq(s.String()) {
		if w.full() {
			return
		}
		if w.n > 0 {
			w.b.WriteByte(' ')
			w.n++
		}
		w.b.WriteString(word)
		w.n += utf8.RuneCountInString(word)
	}
}
