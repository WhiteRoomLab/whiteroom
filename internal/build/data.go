package build

import (
	"html/template"

	"github.com/WhiteRoomLab/whiteroom/internal/render"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// The data of the page templates (5.4.4). Every page has every field of Page, with nil, "", 0 or false for what it does
// not have, so that a template shared by pages of several kinds can read any of them. URLs are percent-encoded (6.5.2),
// with the path of the site before the paths of the site, as template.URL, which html/template writes as it is.

// Page is the data of a page template.
type Page struct {
	Kind     string // entry, posts, term, archive, author or notFound (kindName)
	Language string // of the page, such as "ja"
	// The front page of the language: the page of its frontPage, or a page of the latest posts at the front.
	Front bool
	// For <title>: the title of the page in the title format of the language, or the site name alone on the front pages
	// and the 404 pages.
	PageTitle string
	Title     string // the title of the page itself; "" for the latest posts at the front and the 404 pages
	URL       template.URL
	Head      template.HTML // what the core writes in <head>, such as the description and the canonical URL
	SiteCSS   template.URL  // of the site CSS, with its version
	PageCSS   template.URL  // of the CSS of the page; "" if it needs none
	Site      *Site
	// The menus of the language by location: every location of the theme, with nil for one without a menu.
	Menus        map[string][]MenuItem
	Translations []Translation // one for each language of the site, in the order of the settings
	// The entry of an entry page, and the posts page on the pages of its list, with its body; nil on other pages.
	Entry      *Entry
	Entries    []*Entry    // on the pages of lists, the entries on the page, in order, without their bodies
	Pagination *Pagination // of a list of several pages; nil on other pages
	Term       *Term       // of a term list; nil on other pages
	Author     *Author     // of an author page; nil on other pages
	Type       *Type       // of a list of a custom type; nil on other pages
}

// kindName returns the Kind of the pages of k.
func kindName(k theme.Kind) string {
	switch k {
	case theme.KindEntry:
		return "entry"
	case theme.KindPosts:
		return "posts"
	case theme.KindTerm:
		return "term"
	case theme.KindArchive:
		return "archive"
	case theme.KindAuthor:
		return "author"
	case theme.KindNotFound:
		return "notFound"
	}
	return ""
}

// Site is the site in the language of the page.
type Site struct {
	Name    string
	Tagline string            // "" if it has none in the language
	Home    template.URL      // the front page of the language
	Logo    *render.PageImage // nil if the site has none
}

// Translation is the counterpart of the page in a language of the site, such as a translation of its entry.
type Translation struct {
	Language string
	URL      template.URL // of the counterpart; of the front page of the language if Missing
	Current  bool         // the page itself
	Missing  bool         // the page has no counterpart in the language
}

// Pagination is the place of a page in its list.
type Pagination struct {
	Page, Total    int          // the number of the page, from 1, and the number of the pages of the list
	Previous, Next template.URL // of the previous and the next pages; "" on the first and the last
}

// Term is the term of a term list.
type Term struct {
	Name        string
	Description string       // as written, with its line breaks; "" if none
	URL         template.URL // of its list
	Taxonomy    string
	Parent      *TermLink // nil if it has none
}

// Type is the custom type of a list of a type.
type Type struct {
	Name  string
	Label string // its label, else its name
}

// Entry is a published entry: on its own page, with its body and the HTML of its fields, or in a list, without them.
type Entry struct {
	ID      string
	Type    string // the name of its type, such as "post"
	Title   string
	URL     template.URL // of its page
	Excerpt string       // its excerpt as written, else its automatic excerpt
	Date    *Date        // of a post: its date in the time zone of the site; nil for other types
	Author  *Author      // of a post: its author; nil if it has none and for other types
	// Its terms by taxonomy, in their order: every taxonomy of its type, with nil for one without terms.
	Terms         map[string][]TermLink
	FeaturedImage *render.PageImage // nil if it has none
	Sticky        bool
	Body          template.HTML // on its page; "" in lists
	// The values of its custom fields by name: every field of its type (fieldData.value).
	Fields map[string]any
}

// Author is the author of a post, or that of an author page.
type Author struct {
	Name   string
	URL    template.URL      // of the author page in the language of the page
	Bio    string            // in the language of the page, as written, with its line breaks; "" if none
	Avatar *render.PageImage // nil if none
	Links  []template.URL
}

// TermLink is a term that an entry has: its name and the URL of its list.
type TermLink struct {
	Name string
	URL  template.URL
}

// EntryLink is an entry that a field refers to.
type EntryLink struct {
	Title string       // of its published revision; "" if it is not published
	URL   template.URL // of its page; "" if it is not published
}

// Date is a date and a time in the time zone of the site, or a date alone.
type Date struct {
	// For <time datetime>: RFC 3339, such as "2026-10-26T00:30:00+09:00", or the date alone, such as "2026-10-26".
	Datetime         string
	Year, Month, Day int
	Hour, Minute     int // 0 for a date alone
}

// MenuItem is an item of a menu.
type MenuItem struct {
	Label    string
	URL      template.URL // "" for a heading of its children
	Target   string       // "_blank" to open in a new tab, or ""
	Rel      string       // "noopener" for a new tab, or ""
	Current  bool         // its URL is that of the page
	Children []MenuItem
}

// Media is a medium that a media field refers to.
type Media struct {
	URL      template.URL // of its published file, with its version
	Kind     string       // image or pdf
	FileName string
	MIMEType string
	Size     int               // of the original, in bytes
	Image    *render.PageImage // of an image; nil for a PDF
}
