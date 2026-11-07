package build

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io/fs"
	"maps"
	"path"
	"slices"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/siteplan"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// doc is a document of a site and its file.
type doc[T any] = siteplan.Doc[T]

// entryDoc is an entry and, if it is published, its published revision. Once the revision is valid, RevFile has its
// Name alone (revisionFile reads the file again) and Rev has no Body (revisionBody).
type entryDoc struct {
	siteplan.Entry
	revSum  [sha256.Size]byte // the SHA-256 of the file that was valid
	excerpt string            // the excerpt of Rev (excerpt)
}

// builder holds what the build has read of a site and found out about it. The documents are by ID.
type builder struct {
	src              Source
	settings, schema content.SiteFile
	site             *content.Site
	theme            *theme.Theme

	entries   map[string]*entryDoc
	revisions map[string]*entryDoc // the published entries with a valid revision, by the names of their revision files
	terms     map[string]*doc[content.Term]
	media     map[string]*doc[content.Media]
	authors   map[string]*doc[content.AuthorProfile]
	menus     map[string]*doc[content.Menu]
	forms     map[string]*doc[content.Form]
	redirects map[string]*doc[content.Redirect]
	// The documents as siteplan checks and plans them: the maps above, and the entries of entries (Entries).
	sp *siteplan.Site

	base  string         // the path of the site URL as written, such as "/blog"; "" at the root of its host
	loc   *time.Location // the time zone of the site
	pages []*page
	// The paths of the published entries, the front pages at their language paths and the posts pages at those of
	// their lists; of the lists of the terms; and of the author pages by language. All by ID.
	entryPaths  map[string]string
	termPaths   map[string]string
	authorPaths map[string]map[string]string
	outputs     []siteplan.Output
	// The IDs of the published entries and of the terms by their translation groups and languages (groupTranslations).
	entryGroups, termGroups map[groupKey]string
	// The redirects that the servers serve, sorted by from (resolveRedirects).
	serverRedirects []siteplan.Redirect
	// The resolver of the media and the other entities that content refers to, which records the media to publish.
	res *resolver
	// The HTML of the posts pages, which pageData makes once for all the pages of their lists.
	postsBodies onceMap[renderKey, renderedEntry]

	warnings problems
}

// kinds are the kinds of the documents of a Source, in the order load reads them.
var kinds = []content.RefKind{
	content.RefEntry, content.RefTerm, content.RefMedia, content.RefAuthor, content.RefMenu, content.RefForm,
	content.RefRedirect,
}

// load reads the site in src and checks what is published, in stages that each stop at their errors: decoding the
// documents of every kind; reading the site settings and the theme; validating what is published; checking across
// the documents; planning the pages and the other outputs. The error is content.Errors, or an error of src as it is.
func load(src Source) (*builder, error) {
	b := &builder{src: src}
	b.warnings.read = b.revisionData
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

	p := problems{read: b.revisionData}
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
	stageEnded("validated")
	b.sp.CheckAcross(p.report())
	if p.len() > 0 {
		return nil, p.sorted()
	}
	b.plan(&p)
	if p.len() > 0 {
		return nil, p.sorted()
	}
	stageEnded("planned")
	return b, nil
}

// decode decodes the documents in files, by kind and ID. The id in a document, if any, must be its ID.
func (b *builder) decode(files map[content.RefKind]map[string]content.SiteFile, p *problems) {
	entries := decodeDocs(p, files[content.RefEntry], func(e *content.Entry) string { return e.ID })
	b.entries = make(map[string]*entryDoc, len(entries))
	b.revisions = make(map[string]*entryDoc, len(entries))
	b.sp = &siteplan.Site{Settings: b.settings, Schema: b.schema, Entries: make(map[string]*siteplan.Entry, len(entries))}
	for id, d := range entries {
		e := &entryDoc{Entry: siteplan.Entry{File: d.File, E: d.V}}
		b.entries[id], b.sp.Entries[id] = e, &e.Entry
	}
	b.terms = decodeDocs(p, files[content.RefTerm], func(t *content.Term) string { return t.ID })
	b.media = decodeDocs(p, files[content.RefMedia], func(m *content.Media) string { return m.ID })
	b.authors = decodeDocs(p, files[content.RefAuthor], func(a *content.AuthorProfile) string { return a.ID })
	b.menus = decodeDocs(p, files[content.RefMenu], func(m *content.Menu) string { return m.ID })
	b.forms = decodeDocs(p, files[content.RefForm], func(f *content.Form) string { return f.ID })
	b.redirects = decodeDocs(p, files[content.RefRedirect], func(r *content.Redirect) string { return r.ID })
	b.sp.Terms, b.sp.Media, b.sp.Authors, b.sp.Menus = b.terms, b.media, b.authors, b.menus
	b.sp.Forms, b.sp.Redirects = b.forms, b.redirects
}

// decodeDocs decodes files, the documents of one kind by ID, in ID order; id returns the id in a document.
func decodeDocs[T any, P interface {
	*T
	UnmarshalJSON(data []byte) error
}](p *problems, files map[string]content.SiteFile, id func(*T) string) map[string]*doc[T] {
	docs := make(map[string]*doc[T], len(files))
	for _, key := range slices.Sorted(maps.Keys(files)) {
		d := &doc[T]{File: files[key]}
		// A Source may return an array larger than the file (sitedir reads with bytes.MinRead more); keep a copy.
		d.File.Data = bytes.Clone(d.File.Data)
		if !decodeFile(p, d.File, P(&d.V).UnmarshalJSON) {
			continue
		}
		if v := id(&d.V); v != "" && v != key {
			p.add(d.File, "/id", "id %q differs from the ID of the document, %q", v, key)
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
		b.sp.Site, b.sp.Theme = site, th
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
