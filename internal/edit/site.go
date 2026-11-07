package edit

import (
	"context"
	"errors"
	"io/fs"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/siteplan"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

const settingsName = "site/settings.json"

// docName returns the name of the document of kind with id in problems, that of its file in a site directory (4.3).
func docName(kind content.RefKind, id string) string {
	if kind == store.SettingsKind {
		return settingsName
	}
	return content.DocumentDir(kind) + "/" + id + ".json"
}

func revisionName(id string) string { return "revisions/" + id + ".json" }

// reader is what an operation reads of the index of the store: the methods of *store.Tx, which a test can wrap to make
// them fail.
type reader interface {
	Summary(kind content.RefKind, id string) (store.Summary, error)
	Summaries(kind content.RefKind) ([]store.Summary, error)
	Referrers(kind content.RefKind, id string) ([]store.Referrer, error)
}

func (e *Editor) reader(tx *store.Tx) reader {
	if e.wrap != nil {
		return e.wrap(tx)
	}
	return tx
}

// run is an operation in the transaction of its Update, or in the View that makes the model of the site before it.
type run struct {
	e     *Editor
	ctx   context.Context
	tx    *store.Tx
	rd    reader
	by    string            // the user, who saves
	given map[string][]byte // the files that the caller gave, by name, in which problems in them are located
	err   error             // the first error of lookup, which fails the operation instead of the problems of validation
}

func newRun(e *Editor, ctx context.Context, tx *store.Tx, by string) *run {
	return &run{e: e, ctx: ctx, tx: tx, rd: e.reader(tx), by: by, given: make(map[string][]byte)}
}

// refKinds are the kinds of the entities that validation looks up.
var refKinds = map[content.RefKind]bool{
	content.RefEntry: true, content.RefTerm: true, content.RefMedia: true, content.RefForm: true, content.RefAuthor: true,
}

// lookup tells about the entity of kind with the ID id as content.ValidateOptions.Lookup does, from the index. An error
// of the store other than a missing entity is kept in r.err, and then lookup finds nothing: the operation fails with
// it, not with the problems that validation has found by it.
func (r *run) lookup(kind content.RefKind, id string) (content.Ref, bool) {
	if r.err != nil || !refKinds[kind] || content.CheckID(kind, id) != nil {
		return content.Ref{}, false
	}
	s, err := r.rd.Summary(kind, id)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return content.Ref{}, false
	case err != nil:
		r.err = err
		return content.Ref{}, false
	}
	return refOf(s), true
}

// refOf returns what lookup tells about the entity whose summary is s: the type and the language of an entry, the
// taxonomy and the language of a term, the kind of a medium.
func refOf(s store.Summary) content.Ref {
	switch {
	case s.Entry != nil:
		return content.Ref{Type: s.Entry.Type, Language: s.Entry.Language}
	case s.Term != nil:
		return content.Ref{Type: s.Term.Taxonomy, Language: s.Term.Language}
	case s.Media != nil:
		return content.Ref{Type: content.MediaKind(s.Media.MIMEType)}
	}
	return content.Ref{}
}

// noSettingsError is the error of an operation on a store that has no settings document.
type noSettingsError struct{ err error }

func (e noSettingsError) Error() string { return e.err.Error() + "; save the settings first" }
func (e noSettingsError) Unwrap() error { return e.err }

// settings returns the settings document that the store has.
func (r *run) settings() (content.SiteFile, error) {
	d, err := r.tx.Document(store.SettingsKind, "")
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return content.SiteFile{}, noSettingsError{err}
	case err != nil:
		return content.SiteFile{}, err
	}
	return content.SiteFile{Name: settingsName, Data: d.Data}, nil
}

// site loads the settings that the store has with the schema file, as the build does, and returns the site and the
// settings document. Their problems, located in them, are the error.
func (r *run) site() (*content.Site, content.SiteFile, error) {
	settings, err := r.settings()
	if err != nil {
		return nil, content.SiteFile{}, err
	}
	site, err := content.LoadSite(settings, r.e.schema, r.lookup)
	switch {
	case r.err != nil:
		return nil, content.SiteFile{}, r.err
	case err != nil:
		return nil, content.SiteFile{}, err
	}
	return site, settings, nil
}

// model makes the model of the site that siteplan checks, as the build makes it from the files of a site directory,
// from the summaries of the index, which it reads once for each kind: site and settings are the site and its settings
// document. An entry has its published revision only if it is published and the store has the revision.
func (r *run) model(site *content.Site, settings content.SiteFile) (*siteplan.Site, error) {
	m := &siteplan.Site{Settings: settings, Schema: r.e.schema, Site: site, Theme: r.e.theme}
	entries, err := r.rd.Summaries(content.RefEntry)
	if err != nil {
		return nil, err
	}
	m.Entries = make(map[string]*siteplan.Entry, len(entries))
	for _, s := range entries {
		if s.Entry == nil {
			continue
		}
		e := &siteplan.Entry{File: content.SiteFile{Name: docName(content.RefEntry, s.ID)}, E: *s.Entry}
		e.E.ID = s.ID
		if e.E.Status == content.StatusPublished && s.Published != nil {
			e.Rev, e.RevFile = s.Published, content.SiteFile{Name: revisionName(e.E.PublishedRevision)}
		}
		m.Entries[s.ID] = e
	}
	m.Terms, err = docs(r, content.RefTerm, func(s store.Summary) *content.Term { return s.Term },
		func(t *content.Term, id string) { t.ID = id })
	if err == nil {
		m.Media, err = docs(r, content.RefMedia, func(s store.Summary) *content.Media { return s.Media },
			func(m *content.Media, id string) { m.ID = id })
	}
	if err == nil {
		m.Authors, err = docs(r, content.RefAuthor, func(s store.Summary) *content.AuthorProfile { return s.Author },
			func(a *content.AuthorProfile, id string) { a.ID = id })
	}
	if err == nil {
		m.Menus, err = docs(r, content.RefMenu, func(s store.Summary) *content.Menu { return s.Menu },
			func(m *content.Menu, id string) { m.ID = id })
	}
	if err == nil {
		m.Forms, err = docs(r, content.RefForm, func(s store.Summary) *content.Form { return s.Form },
			func(f *content.Form, id string) { f.ID = id })
	}
	if err == nil {
		m.Redirects, err = docs(r, content.RefRedirect, func(s store.Summary) *content.Redirect { return s.Redirect },
			func(d *content.Redirect, id string) { d.ID = id })
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}

// docs returns the documents of kind of the summaries of the index by ID: the value that value takes from a summary,
// with the ID of the summary, as the id in the JSON of a document may be missing.
func docs[T any](r *run, kind content.RefKind, value func(store.Summary) *T, setID func(*T, string)) (map[string]*siteplan.Doc[T], error) {
	sums, err := r.rd.Summaries(kind)
	if err != nil {
		return nil, err
	}
	m := make(map[string]*siteplan.Doc[T], len(sums))
	for _, s := range sums {
		v := value(s)
		if v == nil {
			continue
		}
		d := &siteplan.Doc[T]{File: content.SiteFile{Name: docName(kind, s.ID)}, V: *v}
		setID(&d.V, s.ID)
		m[s.ID] = d
	}
	return m, nil
}

// before returns the model of the site as the store has committed it, which inside an Update is the site as it was
// before the Update: nil if its settings are missing or do not load, so that every problem after the change counts as
// new. The error is one of the store.
func (r *run) before() (*siteplan.Site, error) {
	var m *siteplan.Site
	err := r.e.store.View(r.ctx, func(tx *store.Tx) error {
		vr := newRun(r.e, r.ctx, tx, "")
		site, settings, err := vr.site()
		var none noSettingsError
		var problems content.Errors
		if errors.As(err, &none) || errors.As(err, &problems) {
			return nil
		} else if err != nil {
			return err
		}
		m, err = vr.model(site, settings)
		return err
	})
	return m, err
}
