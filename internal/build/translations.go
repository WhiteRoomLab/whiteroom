package build

import (
	"github.com/WhiteRoomLab/whiteroom/internal/render"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// groupKey is a translation group and a language, which a site has at most one entry and one term of.
type groupKey struct{ group, language string }

// counterpart is the page in a language that has what a page has, such as the page of a translation of its entry.
type counterpart struct {
	language string
	path     string // from the site URL, not encoded; that of the front page of the language if missing
	missing  bool   // the page has no counterpart in the language
}

// groupTranslations sets b.entryGroups and b.termGroups to the IDs of the published entries and of the terms by their
// translation groups and languages.
func (b *builder) groupTranslations() {
	b.entryGroups = make(map[groupKey]string)
	for id, d := range b.entries {
		if d.rev != nil {
			b.entryGroups[groupKey{d.e.TranslationGroup, d.e.Language}] = id
		}
	}
	b.termGroups = make(map[groupKey]string, len(b.terms))
	for id, d := range b.terms {
		b.termGroups[groupKey{d.v.TranslationGroup, d.v.Language}] = id
	}
}

// counterparts returns the counterparts of p in the languages of the site, in the order of the settings: in its own
// language, p itself; in the others, as counterpartPath finds them, or else the front page of the language, as
// missing. A 404 page has the front pages, also in its own language.
func (b *builder) counterparts(p *page) []counterpart {
	cs := make([]counterpart, len(b.site.Languages))
	for i, lang := range b.site.Languages {
		path, ok := p.path, true
		if lang != p.language || p.kind == theme.KindNotFound {
			path, ok = b.counterpartPath(p, lang)
		}
		if !ok {
			path = b.site.LanguagePath(lang)
		}
		cs[i] = counterpart{language: lang, path: path, missing: !ok}
	}
	return cs
}

// counterpartPath returns the path of the counterpart of p in language, or false if it has none: for a front page and
// a 404 page, the front page of the language; for the page of an entry and the posts page, the page of the published
// entry of the language in the translation group of its entry; for a term list, the list of the term of the language
// in the translation group of its term; for the list of a type and an author page, the same list in the language. Every
// page of a list has the counterparts of its first page.
func (b *builder) counterpartPath(p *page, language string) (string, bool) {
	if p.front {
		return b.site.LanguagePath(language), true
	}
	switch p.kind {
	case theme.KindEntry, theme.KindPosts:
		id, ok := b.entryGroups[groupKey{p.entry.e.TranslationGroup, language}]
		return b.entryPaths[id], ok
	case theme.KindTerm:
		id, ok := b.termGroups[groupKey{p.term.v.TranslationGroup, language}]
		return b.termPaths[id], ok
	case theme.KindArchive:
		path, err := b.site.ArchivePath(p.typeName, language)
		return path, err == nil
	case theme.KindAuthor:
		path, ok := b.authorPaths[p.author.v.ID][language]
		return path, ok
	case theme.KindNotFound:
		return b.site.LanguagePath(language), true
	}
	return "", false
}

// translations returns the counterparts of p as the data of its page, which r renders.
func (b *builder) translations(p *page, r *render.Renderer) []Translation {
	cs := b.counterparts(p)
	ts := make([]Translation, len(cs))
	for i, c := range cs {
		ts[i] = Translation{
			Language: c.language,
			URL:      r.URL(c.path),
			Current:  c.language == p.language,
			Missing:  c.missing,
		}
	}
	return ts
}
