// Package siteplan finds the paths of the pages and the other outputs of a site (6.6.8) and checks what the documents
// of a site must agree on: the checks across entities of 6.7 and the duplicates of output paths (4.1). The build and
// the save operations run it on a Site that each fills.
package siteplan

import (
	"fmt"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// Doc is a document of a site and its file. Problems name File.Name; File.Data may be nil.
type Doc[T any] struct {
	File content.SiteFile
	V    T
}

// Entry is an entry and, if it is published with a valid revision, that revision.
type Entry struct {
	File    content.SiteFile
	E       content.Entry
	RevFile content.SiteFile  // names the file of Rev
	Rev     *content.Revision // nil unless E is published and its revision is valid
}

// Site is a model of a site, with its documents by ID, which the build fills from the files of a site and the save
// operations from the summaries that the store keeps of its documents. The site settings, the theme and the documents
// must be valid, and what they refer to must be in the maps: siteplan does not check them.
//
// A Site needs only the following fields of its documents, and the others may be zero. Of every file, it needs only
// the Name, which the problems name; the Data may be nil.
//   - of Entry.E, ID, Number, Type, Language, TranslationGroup and CreatedAt (not Status and PublishedRevision, which
//     tell whether an entry has an Entry.Rev);
//   - of Entry.Rev, which an entry has only if it is published with a valid revision, Slug, Parent, Date, Author,
//     Sticky and Terms;
//   - of a term, ID, Taxonomy, Language, TranslationGroup, Slug and Parent;
//   - of a medium, ID and MIMEType;
//   - of an author, ID and Slug;
//   - of a menu, ID, Language and Location;
//   - of a form, ID;
//   - of a redirect, ID, From, To and Status;
//   - of Theme, the 404 template and the assets (Theme.Template and Theme.Assets).
//
// The ID of a document is also its key in its map. The pages that Site.Plan makes have the terms and the authors that
// they are of, with their IDs.
type Site struct {
	Settings, Schema content.SiteFile // of the settings document and of the schema file, which problems about them name
	Site             *content.Site
	Theme            *theme.Theme

	Entries   map[string]*Entry
	Terms     map[string]*Doc[content.Term]
	Media     map[string]*Doc[content.Media]
	Authors   map[string]*Doc[content.AuthorProfile]
	Menus     map[string]*Doc[content.Menu]
	Forms     map[string]*Doc[content.Form]
	Redirects map[string]*Doc[content.Redirect]
}

// Report adds a problem with the text msg at pointer, a JSON Pointer in f.
type Report func(f content.SiteFile, pointer, msg string)

func (r Report) addf(f content.SiteFile, pointer, format string, args ...any) {
	r(f, pointer, fmt.Sprintf(format, args...))
}

// Published returns the entry id if it is published with a valid revision, or nil.
func (s *Site) Published(id string) *Entry {
	if e := s.Entries[id]; e != nil && e.Rev != nil {
		return e
	}
	return nil
}

// Ref tells about the entity of kind with the ID id as content.ValidateOptions.Lookup does: entries of every status,
// terms, media, forms and authors. It reads no field of s other than the maps of these documents.
func (s *Site) Ref(kind content.RefKind, id string) (content.Ref, bool) {
	switch kind {
	case content.RefEntry:
		if e := s.Entries[id]; e != nil {
			return content.Ref{Type: e.E.Type, Language: e.E.Language}, true
		}
	case content.RefTerm:
		if d := s.Terms[id]; d != nil {
			return content.Ref{Type: d.V.Taxonomy, Language: d.V.Language}, true
		}
	case content.RefMedia:
		if d := s.Media[id]; d != nil {
			return content.Ref{Type: d.V.Kind()}, true
		}
	case content.RefForm:
		return content.Ref{}, s.Forms[id] != nil
	case content.RefAuthor:
		return content.Ref{}, s.Authors[id] != nil
	case content.RefMenu, content.RefRedirect, content.RefRevision, content.RefTranslationGroup, content.RefUser:
	}
	return content.Ref{}, false
}

// PathRef tells about the page or the term with the ID id as the lookup of content.Site.EntryPath does: the published
// pages, by their published revisions, and every term.
func (s *Site) PathRef(kind content.RefKind, id string) (content.PathRef, bool) {
	switch kind {
	case content.RefEntry:
		if e := s.Published(id); e != nil && e.E.Type == content.TypePage {
			return content.PathRef{Slug: e.Rev.Slug, Parent: e.Rev.Parent}, true
		}
	case content.RefTerm:
		if d := s.Terms[id]; d != nil {
			return content.PathRef{Slug: d.V.Slug, Parent: d.V.Parent}, true
		}
	case content.RefMedia, content.RefForm, content.RefAuthor, content.RefMenu, content.RefRedirect, content.RefRevision,
		content.RefTranslationGroup, content.RefUser:
	}
	return content.PathRef{}, false
}
