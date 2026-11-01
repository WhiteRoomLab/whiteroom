package build

import (
	"html/template"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/render"
)

// entryData returns the data of d, a published entry, on a page that r renders. html has the HTML of its body and of
// its richText and blocks fields by their JSON Pointers in its revision, such as "/body" and "/fields/intro"; without
// it, as in lists, they are "". The warnings about its fields go to b.warnings.
func (b *builder) entryData(d *entryDoc, r *render.Renderer, html map[string]template.HTML) *Entry {
	rev := d.rev
	t := b.site.Type(d.e.Type)
	e := &Entry{
		ID:      d.e.ID,
		Type:    d.e.Type,
		Title:   rev.Title,
		URL:     r.URL(b.entryPaths[d.e.ID]),
		Excerpt: excerpt(rev),
		Terms:   make(map[string][]TermLink, len(t.Taxonomies)),
		Sticky:  rev.Sticky,
		Body:    html["/body"],
		Fields:  fieldData{b: b, d: d, r: r, html: html}.fields(t.Fields, rev.Fields, "/fields"),
	}
	if d.e.Type == content.TypePost {
		e.Date = newDate(rev.Date, b.loc)
		if rev.Author != "" {
			e.Author = b.authorData(rev.Author, d.e.Language, r)
		}
	}
	for _, taxonomy := range t.Taxonomies {
		var terms []TermLink
		for _, id := range rev.Terms[taxonomy] {
			terms = append(terms, b.termLink(id, r))
		}
		e.Terms[taxonomy] = terms
	}
	if rev.FeaturedImage != "" {
		e.FeaturedImage = r.PageImage(rev.FeaturedImage)
	}
	return e
}

// authorData returns the data of the author with the ID on a page in language, which r renders.
func (b *builder) authorData(id, language string, r *render.Renderer) *Author {
	a := &b.authors[id].v
	x := &Author{Name: a.Name, URL: r.URL(b.authorPaths[id][language]), Bio: a.Bio[language]}
	if a.Avatar != "" {
		x.Avatar = r.PageImage(a.Avatar)
	}
	for _, l := range a.Links {
		x.Links = append(x.Links, template.URL(render.EncodeURL(l)))
	}
	return x
}

// termData returns the data of the term with the ID on its list, which r renders.
func (b *builder) termData(id string, r *render.Renderer) *Term {
	t := &b.terms[id].v
	x := &Term{Name: t.Name, Description: t.Description, URL: r.URL(b.termPaths[id]), Taxonomy: t.Taxonomy}
	if t.Parent != "" {
		parent := b.termLink(t.Parent, r)
		x.Parent = &parent
	}
	return x
}

// termLink returns the link of the term with the ID on a page that r renders.
func (b *builder) termLink(id string, r *render.Renderer) TermLink {
	return TermLink{Name: b.terms[id].v.Name, URL: r.URL(b.termPaths[id])}
}

// entryLink returns the link of the entry with the ID on a page that r renders, without a title and a URL if it is not
// published.
func (b *builder) entryLink(id string, r *render.Renderer) EntryLink {
	d := b.published(id)
	if d == nil {
		return EntryLink{}
	}
	return EntryLink{Title: d.rev.Title, URL: r.URL(b.entryPaths[id])}
}
