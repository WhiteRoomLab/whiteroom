package siteplan

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

// Page is a page of the site: that of an entry, a page of a list, or a 404 page.
type Page struct {
	Kind     theme.Kind
	Language string
	Path     string // from the site URL, not encoded, such as "/news/page/2/"
	File     string // in the public directory, such as "news/page/2/index.html"
	Number   int    // of the page in its list, from 1; 1 for a page that is not in a list
	Total    int    // the pages of its list; 1 for a page that is not in a list
	First    string // the path of page 1 of its list; Path for a page that is not in a list
	Front    bool   // the front page of the language: the page of its frontPage, or the latest posts

	Entry   *Entry                      // KindEntry: the entry; KindPosts: the posts page, or nil at the front
	Term    *Doc[content.Term]          // KindTerm
	Author  *Doc[content.AuthorProfile] // KindAuthor
	Type    string                      // KindArchive
	Entries []*Entry                    // of a list, those on this page in order
}

// Source is where an output comes from, for the errors about it.
type Source struct {
	File    content.SiteFile // the Name is "" for an output of the core
	Pointer string           // a JSON Pointer in File
	What    string           // the output in messages, such as "revisions/<ID>.json" or "the feed in en"
}

// Output is a path of the site or a file in the public directory, which no other output may have (4.1).
type Output struct {
	Path string // from the site URL, not encoded, such as "/feed/"
	File string // in the public directory, such as "feed/index.xml"; "" for a path without a file
	Src  Source
	Rank int
}

// Ranks of outputs, by which the check of duplicates orders them before their paths: of two outputs with the same path,
// the later one gets the error, so that it goes to a document, such as a page whose slug takes the path of a feed,
// rather than to what the core or the developers make.
const (
	RankCore      = iota // the front pages, the 404 pages, the feeds, the sitemap, the site CSS, the .htaccess file
	RankDeveloper        // the assets of the theme and the lists of the types in the schema
	RankDocument         // the pages of the entries, the lists of the terms, the author pages, the actions of the forms
	RankRedirect         // the froms of the redirects
)

// Plan is the pages and the other outputs of a site, and the paths of what has a page (Site.Plan).
type Plan struct {
	Base     string         // the path of the site URL as written, such as "/blog"; "" at the root of its host
	Location *time.Location // the time zone of the site; nil if it cannot be loaded, which Plan reports
	Pages    []*Page
	Outputs  []Output // every page, and the other outputs, in the order that Plan found them
	// The paths of the published entries, the front pages at their language paths and the posts pages at those of
	// their lists; of the lists of the terms; and of the author pages by language. All by ID.
	EntryPaths  map[string]string
	TermPaths   map[string]string
	AuthorPaths map[string]map[string]string
}

// Plan finds the paths and the files of the pages of the site and of the other outputs, reporting the errors: the
// pages of the published entries; the lists of the latest posts, of the terms, of the types and of the authors, each
// with its pages; the 404 pages if the theme has 404.html; the feeds; the sitemap unless the site has seo.noindex; the
// site CSS; the .htaccess file; the assets of the theme; and the actions of the forms, which are paths without files.
// Every page and output goes to the Outputs of the Plan with where it comes from. It needs s.Site and s.Theme.
func (s *Site) Plan(report Report) *Plan {
	pl := &planner{s: s, report: report, plan: &Plan{Base: BasePath(s.Site.URL)}}
	var err error
	if pl.plan.Location, err = time.LoadLocation(s.Site.TimeZone); err != nil {
		report.addf(s.Settings, "/timeZone", "%v", err)
	}
	members := s.Members()
	pl.planEntries(members)
	pl.planLists(members)
	pl.planOthers()
	return pl.plan
}

// planner makes a Plan of a site, reporting the errors.
type planner struct {
	s      *Site
	report Report
	plan   *Plan
}

// planEntries adds the pages of the published entries: the front pages at their language paths, and the posts pages
// as the lists of the latest posts.
func (pl *planner) planEntries(members Lists) {
	s := pl.s
	pl.plan.EntryPaths = make(map[string]string)
	for _, id := range slices.Sorted(maps.Keys(s.Entries)) {
		d := s.Published(id)
		if d == nil {
			continue
		}
		lang := d.E.Language
		src := Source{File: d.RevFile, Pointer: "/slug", What: d.RevFile.Name}
		path, err := s.Site.EntryPath(&d.E, d.Rev, s.PathRef)
		if err != nil {
			pl.report.addf(src.File, src.Pointer, "cannot make the path: %v", err)
			continue
		}
		pg := Page{Kind: theme.KindEntry, Language: lang, Path: path, Entry: d}
		var ok bool
		switch id {
		case s.Site.PostsPage[lang]:
			pg.Kind = theme.KindPosts
			ok = pl.addList(pg, members.Sorted(theme.KindPosts, lang, ""), src, RankDocument)
		case s.Site.FrontPage[lang]:
			pg.Front = true
			front := Source{File: s.Settings, Pointer: "/frontPage/" + lang, What: "the front page in " + lang}
			ok = pl.addPage(&pg, front, RankCore)
		default:
			ok = pl.addPage(&pg, src, RankDocument)
		}
		if ok {
			pl.plan.EntryPaths[id] = path
		}
	}
}

// planLists adds the lists that are not pages of entries: the latest posts at the front of each language without
// frontPage, the lists of the terms, the lists of the types in every language, and the author pages in every language.
func (pl *planner) planLists(members Lists) {
	s := pl.s
	languages := s.Site.Languages
	for i, lang := range languages {
		if s.Site.FrontPage[lang] == "" {
			src := Source{File: s.Settings, Pointer: "/languages/" + strconv.Itoa(i), What: "the front page in " + lang}
			pg := Page{Kind: theme.KindPosts, Language: lang, Path: s.Site.LanguagePath(lang), Front: true}
			pl.addList(pg, members.Sorted(theme.KindPosts, lang, ""), src, RankCore)
		}
	}

	pl.plan.TermPaths = make(map[string]string, len(s.Terms))
	for _, id := range slices.Sorted(maps.Keys(s.Terms)) {
		d := s.Terms[id]
		src := Source{File: d.File, Pointer: "/slug", What: d.File.Name}
		path, err := s.Site.TermPath(&d.V, s.PathRef)
		if err != nil {
			pl.report.addf(src.File, src.Pointer, "cannot make the path: %v", err)
			continue
		}
		pg := Page{Kind: theme.KindTerm, Language: d.V.Language, Path: path, Term: d}
		if pl.addList(pg, members.Sorted(theme.KindTerm, d.V.Language, id), src, RankDocument) {
			pl.plan.TermPaths[id] = path
		}
	}

	for _, t := range s.Site.Types {
		if t.Archive == "" {
			continue
		}
		for _, lang := range languages {
			src := Source{
				File:    s.Schema,
				Pointer: "/types/" + t.Name + "/archive",
				What:    fmt.Sprintf("the list of the type %q in %s", t.Name, lang),
			}
			path, err := s.Site.ArchivePath(t.Name, lang)
			if err != nil {
				pl.report.addf(src.File, src.Pointer, "cannot make the path: %v", err)
				continue
			}
			pg := Page{Kind: theme.KindArchive, Language: lang, Path: path, Type: t.Name}
			pl.addList(pg, members.Sorted(theme.KindArchive, lang, t.Name), src, RankDeveloper)
		}
	}

	pl.plan.AuthorPaths = make(map[string]map[string]string, len(s.Authors))
	for _, id := range slices.Sorted(maps.Keys(s.Authors)) {
		d := s.Authors[id]
		src := Source{File: d.File, Pointer: "/slug", What: d.File.Name}
		paths := make(map[string]string, len(languages))
		for _, lang := range languages {
			path, err := s.Site.AuthorPath(&d.V, lang)
			if err != nil {
				pl.report.addf(src.File, src.Pointer, "cannot make the path: %v", err)
				continue
			}
			pg := Page{Kind: theme.KindAuthor, Language: lang, Path: path, Author: d}
			if pl.addList(pg, members.Sorted(theme.KindAuthor, lang, id), src, RankDocument) {
				paths[lang] = path
			}
		}
		pl.plan.AuthorPaths[id] = paths
	}
}

// planOthers adds the 404 pages, if the theme has 404.html, and the outputs that are not pages.
func (pl *planner) planOthers() {
	s := pl.s
	languages := s.Site.Languages
	if s.Theme.Template(theme.Page{Kind: theme.KindNotFound}) != "" {
		for _, lang := range languages {
			pg := Page{Kind: theme.KindNotFound, Language: lang, Path: s.Site.LanguagePath(lang) + "404.html"}
			pl.addPage(&pg, Source{What: "the 404 page in " + lang}, RankCore)
		}
	}
	for _, lang := range languages {
		path := FeedPath(s.Site, lang)
		src := Source{What: "the feed in " + lang}
		if file, ok := pl.outputFile(path, src); ok {
			pl.addOutput(path, strings.TrimSuffix(file, "index.html")+"index.xml", src, RankCore)
		}
	}
	if !s.Site.SEO.NoIndex {
		pl.addOutput("/sitemap.xml", "sitemap.xml", Source{What: "the sitemap"}, RankCore)
	}
	pl.addOutput("/css/site.css", "css/site.css", Source{What: "the site CSS"}, RankCore)
	pl.addOutput("/.htaccess", ".htaccess", Source{What: "the .htaccess file for Apache"}, RankCore)
	for _, a := range s.Theme.Assets() {
		name := "theme/assets/" + a
		pl.addOutput("/assets/"+a, "assets/"+a, Source{File: content.SiteFile{Name: name}, What: name}, RankDeveloper)
	}
	for _, id := range slices.Sorted(maps.Keys(s.Forms)) {
		d := s.Forms[id]
		pl.addOutput("/_forms/"+id, "", Source{File: d.File, Pointer: "/id", What: d.File.Name}, RankDocument)
	}
}

// addPage adds pg, with the file of its path, to the pages and to the outputs, from src with rank, or reports at src
// why it has no file. It reports whether it has added pg.
func (pl *planner) addPage(pg *Page, src Source, rank int) bool {
	file, ok := pl.outputFile(pg.Path, src)
	if !ok {
		return false
	}
	pg.File = file
	if pg.Total == 0 {
		pg.Number, pg.Total, pg.First = 1, 1, pg.Path
	}
	pl.plan.Pages = append(pl.plan.Pages, pg)
	pl.addOutput(pg.Path, file, src, rank)
	return true
}

func (pl *planner) addOutput(path, file string, src Source, rank int) {
	pl.plan.Outputs = append(pl.plan.Outputs, Output{Path: path, File: file, Src: src, Rank: rank})
}

// outputFile returns the file of the page at path, as content.OutputFile does, or reports at src why it has none.
func (pl *planner) outputFile(path string, src Source) (string, bool) {
	file, err := content.OutputFile(path)
	if err != nil {
		pl.report.addf(src.File, src.Pointer, "cannot make the path: %v", err)
		return "", false
	}
	return file, true
}

// addList adds the pages of a list with the entries es, in order: first, page 1 with the kind, the language, the path
// and what the list is of, and from page 2 on, a page at content.PagedPath for each postsPerPage entries. A list
// without entries has page 1 alone. It reports at src why a page has no file, and whether every page has one.
func (pl *planner) addList(first Page, es []*Entry, src Source, rank int) bool {
	per := pl.s.PostsPerPage()
	total := max(1, (len(es)+per-1)/per)
	for n := 1; n <= total; n++ {
		pg := first
		pg.Path, pg.Number, pg.Total, pg.First = content.PagedPath(first.Path, n), n, total, first.Path
		lo, hi := min((n-1)*per, len(es)), min(n*per, len(es))
		pg.Entries = es[lo:hi:hi]
		if !pl.addPage(&pg, src, rank) {
			return false
		}
	}
	return true
}

// BasePath returns the path of siteURL, a valid URL of a site, as it is written: "/blog" for
// "https://example.com/blog", or "" for a site at the root of its host.
func BasePath(siteURL string) string {
	_, rest, _ := strings.Cut(siteURL, "://")
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[i:]
	}
	return ""
}

// FeedPath returns the path of the RSS feed in language: feed/ after the language path, such as "/en/feed/".
func FeedPath(site *content.Site, language string) string {
	return site.LanguagePath(language) + "feed/"
}
