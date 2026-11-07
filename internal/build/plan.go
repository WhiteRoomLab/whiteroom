package build

import (
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// page is a page of the site: that of an entry, a page of a list, or a 404 page.
type page struct {
	kind     theme.Kind
	language string
	path     string // from the site URL, not encoded, such as "/news/page/2/"
	file     string // in the public directory, such as "news/page/2/index.html"
	number   int    // of the page in its list, from 1; 1 for a page that is not in a list
	total    int    // the pages of its list; 1 for a page that is not in a list
	first    string // the path of page 1 of its list; path for a page that is not in a list
	front    bool   // the front page of the language: the page of its frontPage, or the latest posts

	entry    *entryDoc                   // KindEntry: the entry; KindPosts: the posts page, or nil at the front
	term     *doc[content.Term]          // KindTerm
	author   *doc[content.AuthorProfile] // KindAuthor
	typeName string                      // KindArchive
	entries  []*entryDoc                 // of a list, those on this page in order
}

// plan finds the paths and the files of the pages of the site and of the other outputs (siteplan.Site.Plan), adding the
// errors to p. It keeps the pages as the build's own, with the entries as entryDoc, and drops those of siteplan; and it
// finds the translations of the entries and the terms (groupTranslations).
func (b *builder) plan(p *problems) {
	pl := b.sp.Plan(p.report())
	b.base, b.loc = pl.Base, pl.Location
	b.entryPaths, b.termPaths, b.authorPaths = pl.EntryPaths, pl.TermPaths, pl.AuthorPaths
	b.outputs = pl.Outputs
	b.pages = make([]*page, len(pl.Pages))
	for i, pg := range pl.Pages {
		b.pages[i] = &page{
			kind: pg.Kind, language: pg.Language, path: pg.Path, file: pg.File, number: pg.Number, total: pg.Total,
			first: pg.First, front: pg.Front,
			entry: b.docOf(pg.Entry), term: pg.Term, author: pg.Author, typeName: pg.Type, entries: b.docsOf(pg.Entries),
		}
	}
	b.groupTranslations()
}
