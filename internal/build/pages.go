package build

import (
	"html/template"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/render"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// renderKey is an entry and a renderer, by which pageData keeps what it makes once.
type renderKey struct {
	id string
	r  *render.Renderer
}

// renderedEntry is what renderEntry returns.
type renderedEntry struct {
	html map[string]template.HTML
	css  render.CSS
	err  error
}

// pageData returns the data of the page template of p, which r renders in the language of p, with menus, the menus of
// the language (menusOf), and the CSS that the page needs: that of the body and the fields of its entry. It leaves
// SiteCSS and PageCSS, which depend on the files of the CSS, to the caller, and Head empty. The error is that of
// rendering the entry (renderEntry).
func (b *builder) pageData(p *page, r *render.Renderer, menus map[string][]MenuItem) (*Page, render.CSS, error) {
	url := r.URL(p.path)
	data := &Page{
		Kind:         kindName(p.kind),
		Language:     p.language,
		Front:        p.front,
		PageTitle:    b.pageTitle(p),
		Title:        b.heading(p),
		URL:          url,
		Site:         b.siteData(p.language, r),
		Menus:        currentMenus(menus, url),
		Translations: b.translations(p, r),
	}
	var css render.CSS
	if p.entry != nil {
		html, entryCSS, err := b.entryHTML(p, r)
		if err != nil {
			return nil, render.CSS{}, err
		}
		css.Add(entryCSS)
		data.Entry = b.entryData(p.entry, r, html)
	}
	for _, d := range p.entries {
		data.Entries = append(data.Entries, b.summary(d, r))
	}
	if p.total > 1 {
		data.Pagination = &Pagination{Page: p.number, Total: p.total}
		if p.number > 1 {
			data.Pagination.Previous = r.URL(content.PagedPath(p.first, p.number-1))
		}
		if p.number < p.total {
			data.Pagination.Next = r.URL(content.PagedPath(p.first, p.number+1))
		}
	}
	switch p.kind {
	case theme.KindTerm:
		data.Term = b.termData(p.term.v.ID, r)
	case theme.KindArchive:
		data.Type = &Type{Name: p.typeName, Label: b.typeLabel(p.typeName)}
	case theme.KindAuthor:
		data.Author = b.authorData(p.author.v.ID, p.language, r)
	case theme.KindEntry, theme.KindPosts, theme.KindNotFound:
	}
	return data, css, nil
}

// siteData returns the site in language, on a page that r renders.
func (b *builder) siteData(language string, r *render.Renderer) *Site {
	s := &Site{
		Name:    b.site.Name[language],
		Tagline: b.site.Tagline[language],
		Home:    r.URL(b.site.LanguagePath(language)),
	}
	if b.site.Logo != "" {
		s.Logo = r.PageImage(b.site.Logo)
	}
	return s
}

// entryHTML returns what renderEntry returns for the entry of p, an entry page or a page of the list of a posts page,
// with r, rendering the posts page once for all the pages of its list.
func (b *builder) entryHTML(p *page, r *render.Renderer) (map[string]template.HTML, render.CSS, error) {
	if p.kind != theme.KindPosts {
		return b.renderEntry(p.entry, r)
	}
	x := b.postsBodies.get(renderKey{p.entry.e.ID, r}, func() renderedEntry {
		html, css, err := b.renderEntry(p.entry, r)
		return renderedEntry{html, css, err}
	})
	return x.html, x.css, x.err
}

// summary returns the data of d, a published entry, in the lists of the pages that r renders (entryData without its
// HTML), made once for d and r.
func (b *builder) summary(d *entryDoc, r *render.Renderer) *Entry {
	return b.summaries.get(renderKey{d.e.ID, r}, func() *Entry { return b.entryData(d, r, nil) })
}
