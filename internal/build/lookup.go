package build

import "github.com/WhiteRoomLab/whiteroom/internal/content"

// ref tells about the entity of kind with the ID id as content.ValidateOptions.Lookup does: entries of every status,
// terms, media, forms and authors.
func (b *builder) ref(kind content.RefKind, id string) (content.Ref, bool) {
	switch kind {
	case content.RefEntry:
		if d := b.entries[id]; d != nil {
			return content.Ref{Type: d.e.Type, Language: d.e.Language}, true
		}
	case content.RefTerm:
		if d := b.terms[id]; d != nil {
			return content.Ref{Type: d.v.Taxonomy, Language: d.v.Language}, true
		}
	case content.RefMedia:
		if d := b.media[id]; d != nil {
			return content.Ref{Type: d.v.Kind()}, true
		}
	case content.RefForm:
		return content.Ref{}, b.forms[id] != nil
	case content.RefAuthor:
		return content.Ref{}, b.authors[id] != nil
	case content.RefMenu, content.RefRedirect, content.RefRevision, content.RefTranslationGroup, content.RefUser:
	}
	return content.Ref{}, false
}

// pathRef tells about the page or the term with the ID id as the lookup of content.Site.EntryPath does: the published
// pages, by their published revisions, and every term.
func (b *builder) pathRef(kind content.RefKind, id string) (content.PathRef, bool) {
	switch kind {
	case content.RefEntry:
		if d := b.published(id); d != nil && d.e.Type == content.TypePage {
			return content.PathRef{Slug: d.rev.Slug, Parent: d.rev.Parent}, true
		}
	case content.RefTerm:
		if d := b.terms[id]; d != nil {
			return content.PathRef{Slug: d.v.Slug, Parent: d.v.Parent}, true
		}
	case content.RefMedia, content.RefForm, content.RefAuthor, content.RefMenu, content.RefRedirect, content.RefRevision,
		content.RefTranslationGroup, content.RefUser:
	}
	return content.PathRef{}, false
}

// published returns the entry id if it is published with a valid revision, or nil.
func (b *builder) published(id string) *entryDoc {
	if d := b.entries[id]; d != nil && d.rev != nil {
		return d
	}
	return nil
}
