package build

import (
	"errors"
	"io/fs"
	"maps"
	"path"
	"slices"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// doc is a document of a site and its file.
type doc[T any] struct {
	file content.SiteFile
	v    T
}

// entryDoc is an entry and, if it is published, its published revision.
type entryDoc struct {
	file    content.SiteFile
	e       content.Entry
	revFile content.SiteFile
	rev     *content.Revision // nil unless e is published and the revision is valid
}

// builder holds what the build has read of a site and found out about it. The documents are by ID.
type builder struct {
	src              Source
	settings, schema content.SiteFile
	site             *content.Site
	theme            *theme.Theme

	entries   map[string]*entryDoc
	terms     map[string]*doc[content.Term]
	media     map[string]*doc[content.Media]
	authors   map[string]*doc[content.AuthorProfile]
	menus     map[string]*doc[content.Menu]
	forms     map[string]*doc[content.Form]
	redirects map[string]*doc[content.Redirect]

	warnings problems
}

// kinds are the kinds of the documents of a Source, in the order load reads them.
var kinds = []content.RefKind{
	content.RefEntry, content.RefTerm, content.RefMedia, content.RefAuthor, content.RefMenu, content.RefForm,
	content.RefRedirect,
}

// load reads the site in src and checks what is published, in stages that each stop at their errors: decoding the
// documents of every kind; reading the site settings and the theme; validating what is published; checking across
// the documents. The error is content.Errors, or an error of src as it is.
func load(src Source) (*builder, error) {
	b := &builder{src: src}
	var err error
	if b.settings, b.schema, err = src.Settings(); err != nil {
		return nil, err
	}
	files := make(map[content.RefKind]map[string]content.SiteFile, len(kinds))
	for _, kind := range kinds {
		if files[kind], err = src.Documents(kind); err != nil {
			return nil, err
		}
	}
	themeFS, err := src.Theme()
	if err != nil {
		return nil, err
	}

	var p problems
	b.decode(files, &p)
	if p.len() > 0 {
		return nil, p.sorted()
	}
	if err := b.loadSite(themeFS); err != nil {
		return nil, err
	}
	if err := b.validate(&p); err != nil {
		return nil, err
	}
	if p.len() > 0 {
		return nil, p.sorted()
	}
	b.checkAcross(&p)
	if p.len() > 0 {
		return nil, p.sorted()
	}
	return b, nil
}

// decode decodes the documents in files, by kind and ID. The id in a document, if any, must be its ID.
func (b *builder) decode(files map[content.RefKind]map[string]content.SiteFile, p *problems) {
	entries := decodeDocs(p, files[content.RefEntry], func(e *content.Entry) string { return e.ID })
	b.entries = make(map[string]*entryDoc, len(entries))
	for id, d := range entries {
		b.entries[id] = &entryDoc{file: d.file, e: d.v}
	}
	b.terms = decodeDocs(p, files[content.RefTerm], func(t *content.Term) string { return t.ID })
	b.media = decodeDocs(p, files[content.RefMedia], func(m *content.Media) string { return m.ID })
	b.authors = decodeDocs(p, files[content.RefAuthor], func(a *content.AuthorProfile) string { return a.ID })
	b.menus = decodeDocs(p, files[content.RefMenu], func(m *content.Menu) string { return m.ID })
	b.forms = decodeDocs(p, files[content.RefForm], func(f *content.Form) string { return f.ID })
	b.redirects = decodeDocs(p, files[content.RefRedirect], func(r *content.Redirect) string { return r.ID })
}

// decodeDocs decodes files, the documents of one kind by ID, in ID order; id returns the id in a document.
func decodeDocs[T any, P interface {
	*T
	UnmarshalJSON(data []byte) error
}](p *problems, files map[string]content.SiteFile, id func(*T) string) map[string]*doc[T] {
	docs := make(map[string]*doc[T], len(files))
	for _, key := range slices.Sorted(maps.Keys(files)) {
		d := &doc[T]{file: files[key]}
		if !decodeFile(p, d.file, P(&d.v).UnmarshalJSON) {
			continue
		}
		if v := id(&d.v); v != "" && v != key {
			p.add(d.file, "/id", "id %q differs from the ID of the document, %q", v, key)
		}
		docs[key] = d
	}
	return docs
}

// decodeFile decodes f with decode as content.DecodeFile does, adding the errors to p, and reports whether it has
// decoded f.
func decodeFile(p *problems, f content.SiteFile, decode func([]byte) error) bool {
	err := content.DecodeFile(f, decode)
	var es content.Errors
	if errors.As(err, &es) {
		p.addLocated(es)
	} else {
		p.addErr(f, err)
	}
	return err == nil
}

// loadSite reads the site settings with b.ref, which finds drafts too, and the theme in fsys. The errors are those of
// content.LoadSite and then those of theme.Load, in their orders, with the files of the theme in theme/.
func (b *builder) loadSite(fsys fs.FS) error {
	site, siteErr := content.LoadSite(b.settings, b.schema, b.ref)
	th, themeErr := theme.Load(fsys)
	if siteErr == nil && themeErr == nil {
		b.site, b.theme = site, th
		return nil
	}
	var es content.Errors
	es = append(es, errorsOf(siteErr)...)
	for _, e := range errorsOf(themeErr) {
		c := *e
		c.File = path.Join("theme", c.File)
		es = append(es, &c)
	}
	return es
}
