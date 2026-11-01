package build

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

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

// source is where an output comes from, for the errors about it.
type source struct {
	file    content.SiteFile // the Name is "" for an output of the core
	pointer string           // a JSON Pointer in file
	what    string           // the output in messages, such as "revisions/<ID>.json" or "the feed in en"
}

// output is a path of the site or a file in the public directory, which no other output may have (4.1).
type output struct {
	path string // from the site URL, not encoded, such as "/feed/"
	file string // in the public directory, such as "feed/index.xml"; "" for a path without a file
	src  source
	rank int
}

// Ranks of outputs, by which the check of duplicates orders them before their paths: of two outputs with the same path,
// the later one gets the error, so that it goes to a document, such as a page whose slug takes the path of a feed,
// rather than to what the core or the developers make.
const (
	rankCore      = iota // the front pages, the 404 pages, the feeds, the sitemap, the site CSS, the .htaccess file
	rankDeveloper        // the assets of the theme and the lists of the types in the schema
	rankDocument         // the pages of the entries, the lists of the terms, the author pages, the actions of the forms
	rankRedirect         // the froms of the redirects
)

// plan finds the paths and the files of the pages of the site and of the other outputs, adding the errors to p: the
// pages of the published entries; the lists of the latest posts, of the terms, of the types and of the authors, each
// with its pages; the 404 pages if the theme has 404.html; the feeds; the sitemap unless the site has seo.noindex; the
// site CSS; the .htaccess file; the assets of the theme; and the actions of the forms, which are paths without files.
// Every page and output goes to b.outputs with where it comes from. It also finds the translations of the entries and
// the terms (groupTranslations).
func (b *builder) plan(p *problems) {
	b.base = basePath(b.site.URL)
	var err error
	if b.loc, err = time.LoadLocation(b.site.TimeZone); err != nil {
		p.add(b.settings, "/timeZone", "%v", err)
	}
	members := b.members()
	b.planEntries(p, members)
	b.planLists(p, members)
	b.planOthers(p)
	b.groupTranslations()
}

// planEntries adds the pages of the published entries: the front pages at their language paths, and the posts pages
// as the lists of the latest posts.
func (b *builder) planEntries(p *problems, members lists) {
	b.entryPaths = make(map[string]string)
	for _, id := range slices.Sorted(maps.Keys(b.entries)) {
		d := b.published(id)
		if d == nil {
			continue
		}
		lang := d.e.Language
		src := source{file: d.revFile, pointer: "/slug", what: d.revFile.Name}
		path, err := b.site.EntryPath(&d.e, d.rev, b.pathRef)
		if err != nil {
			p.add(src.file, src.pointer, "cannot make the path: %v", err)
			continue
		}
		pg := page{kind: theme.KindEntry, language: lang, path: path, entry: d}
		var ok bool
		switch id {
		case b.site.PostsPage[lang]:
			pg.kind = theme.KindPosts
			ok = b.addList(p, pg, members.sorted(theme.KindPosts, lang, ""), src, rankDocument)
		case b.site.FrontPage[lang]:
			pg.front = true
			front := source{file: b.settings, pointer: "/frontPage/" + lang, what: "the front page in " + lang}
			ok = b.addPage(p, &pg, front, rankCore)
		default:
			ok = b.addPage(p, &pg, src, rankDocument)
		}
		if ok {
			b.entryPaths[id] = path
		}
	}
}

// planLists adds the lists that are not pages of entries: the latest posts at the front of each language without
// frontPage, the lists of the terms, the lists of the types in every language, and the author pages in every language.
func (b *builder) planLists(p *problems, members lists) {
	languages := b.site.Languages
	for i, lang := range languages {
		if b.site.FrontPage[lang] == "" {
			src := source{file: b.settings, pointer: "/languages/" + strconv.Itoa(i), what: "the front page in " + lang}
			pg := page{kind: theme.KindPosts, language: lang, path: b.site.LanguagePath(lang), front: true}
			b.addList(p, pg, members.sorted(theme.KindPosts, lang, ""), src, rankCore)
		}
	}

	b.termPaths = make(map[string]string, len(b.terms))
	for _, id := range slices.Sorted(maps.Keys(b.terms)) {
		d := b.terms[id]
		src := source{file: d.file, pointer: "/slug", what: d.file.Name}
		path, err := b.site.TermPath(&d.v, b.pathRef)
		if err != nil {
			p.add(src.file, src.pointer, "cannot make the path: %v", err)
			continue
		}
		pg := page{kind: theme.KindTerm, language: d.v.Language, path: path, term: d}
		if b.addList(p, pg, members.sorted(theme.KindTerm, d.v.Language, id), src, rankDocument) {
			b.termPaths[id] = path
		}
	}

	for _, t := range b.site.Types {
		if t.Archive == "" {
			continue
		}
		for _, lang := range languages {
			src := source{
				file:    b.schema,
				pointer: "/types/" + t.Name + "/archive",
				what:    fmt.Sprintf("the list of the type %q in %s", t.Name, lang),
			}
			path, err := b.site.ArchivePath(t.Name, lang)
			if err != nil {
				p.add(src.file, src.pointer, "cannot make the path: %v", err)
				continue
			}
			pg := page{kind: theme.KindArchive, language: lang, path: path, typeName: t.Name}
			b.addList(p, pg, members.sorted(theme.KindArchive, lang, t.Name), src, rankDeveloper)
		}
	}

	b.authorPaths = make(map[string]map[string]string, len(b.authors))
	for _, id := range slices.Sorted(maps.Keys(b.authors)) {
		d := b.authors[id]
		src := source{file: d.file, pointer: "/slug", what: d.file.Name}
		paths := make(map[string]string, len(languages))
		for _, lang := range languages {
			path, err := b.site.AuthorPath(&d.v, lang)
			if err != nil {
				p.add(src.file, src.pointer, "cannot make the path: %v", err)
				continue
			}
			pg := page{kind: theme.KindAuthor, language: lang, path: path, author: d}
			if b.addList(p, pg, members.sorted(theme.KindAuthor, lang, id), src, rankDocument) {
				paths[lang] = path
			}
		}
		b.authorPaths[id] = paths
	}
}

// planOthers adds the 404 pages, if the theme has 404.html, and the outputs that are not pages.
func (b *builder) planOthers(p *problems) {
	languages := b.site.Languages
	if b.theme.Template(theme.Page{Kind: theme.KindNotFound}) != "" {
		for _, lang := range languages {
			pg := page{kind: theme.KindNotFound, language: lang, path: b.site.LanguagePath(lang) + "404.html"}
			b.addPage(p, &pg, source{what: "the 404 page in " + lang}, rankCore)
		}
	}
	for _, lang := range languages {
		path := b.feedPath(lang)
		src := source{what: "the feed in " + lang}
		if file, ok := outputFile(p, path, src); ok {
			b.addOutput(path, strings.TrimSuffix(file, "index.html")+"index.xml", src, rankCore)
		}
	}
	if !b.site.SEO.NoIndex {
		b.addOutput("/sitemap.xml", "sitemap.xml", source{what: "the sitemap"}, rankCore)
	}
	b.addOutput("/css/site.css", "css/site.css", source{what: "the site CSS"}, rankCore)
	b.addOutput("/.htaccess", ".htaccess", source{what: "the .htaccess file for Apache"}, rankCore)
	for _, a := range b.theme.Assets() {
		name := "theme/assets/" + a
		b.addOutput("/assets/"+a, "assets/"+a, source{file: content.SiteFile{Name: name}, what: name}, rankDeveloper)
	}
	for _, id := range slices.Sorted(maps.Keys(b.forms)) {
		d := b.forms[id]
		b.addOutput("/_forms/"+id, "", source{file: d.file, pointer: "/id", what: d.file.Name}, rankDocument)
	}
}

// addPage adds pg, with the file of its path, to the pages and to the outputs, from src with rank, or reports at src
// why it has no file. It reports whether it has added pg.
func (b *builder) addPage(p *problems, pg *page, src source, rank int) bool {
	file, ok := outputFile(p, pg.path, src)
	if !ok {
		return false
	}
	pg.file = file
	if pg.total == 0 {
		pg.number, pg.total, pg.first = 1, 1, pg.path
	}
	b.pages = append(b.pages, pg)
	b.addOutput(pg.path, file, src, rank)
	return true
}

func (b *builder) addOutput(path, file string, src source, rank int) {
	b.outputs = append(b.outputs, output{path: path, file: file, src: src, rank: rank})
}

// outputFile returns the file of the page at path, as content.OutputFile does, or reports at src why it has none.
func outputFile(p *problems, path string, src source) (string, bool) {
	file, err := content.OutputFile(path)
	if err != nil {
		p.add(src.file, src.pointer, "cannot make the path: %v", err)
		return "", false
	}
	return file, true
}

// basePath returns the path of siteURL, a valid URL of a site, as it is written: "/blog" for
// "https://example.com/blog", or "" for a site at the root of its host.
func basePath(siteURL string) string {
	_, rest, _ := strings.Cut(siteURL, "://")
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[i:]
	}
	return ""
}
