package edit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// normalizer is a document, decoded, that can be normalized and written.
type normalizer interface {
	normalize()
	marshal() ([]byte, error)
}

// document is a decoded document of a kind that Create, Save and Delete handle.
type document interface {
	normalizer
	unmarshal(data []byte) error
	id() *string
	value() any
	validate(site *content.Site, lookup func(kind content.RefKind, id string) (content.Ref, bool)) error
}

// docPtr is the type of a pointer to a document of type T that content defines.
type docPtr[T any] interface {
	*T
	UnmarshalJSON(data []byte) error
	Normalize()
	Validate(site *content.Site, lookup func(kind content.RefKind, id string) (content.Ref, bool)) error
	MarshalJSON() ([]byte, error)
}

type typed[T any, P docPtr[T]] struct {
	v    T
	idOf func(*T) *string
}

func (d *typed[T, P]) unmarshal(data []byte) error { return P(&d.v).UnmarshalJSON(data) }
func (d *typed[T, P]) id() *string                 { return d.idOf(&d.v) }
func (d *typed[T, P]) value() any                  { return &d.v }
func (d *typed[T, P]) normalize()                  { P(&d.v).Normalize() }
func (d *typed[T, P]) marshal() ([]byte, error)    { return P(&d.v).MarshalJSON() }

func (d *typed[T, P]) validate(site *content.Site, lookup func(kind content.RefKind, id string) (content.Ref, bool)) error {
	return P(&d.v).Validate(site, lookup)
}

// newDocument returns a function that makes an empty document of type T, whose ID idOf finds.
func newDocument[T any, P docPtr[T]](idOf func(*T) *string) func() document {
	return func() document { return &typed[T, P]{idOf: idOf} }
}

// docKinds make the documents of the kinds that Create, Save and Delete handle; Create does not make media, which AddMedia
// does with their files.
var docKinds = map[content.RefKind]func() document{
	content.RefTerm:     newDocument(func(t *content.Term) *string { return &t.ID }),
	content.RefMedia:    func() document { return new(mediaDoc) },
	content.RefAuthor:   newDocument(func(a *content.AuthorProfile) *string { return &a.ID }),
	content.RefMenu:     newDocument(func(m *content.Menu) *string { return &m.ID }),
	content.RefForm:     newDocument(func(f *content.Form) *string { return &f.ID }),
	content.RefRedirect: newDocument(func(r *content.Redirect) *string { return &r.ID }),
}

// kindActions are the actions of the activity log for the operations on the documents of a kind, "" for one that it has not.
type kindActions struct{ create, save, remove audit.Action }

var actionsOf = map[content.RefKind]kindActions{
	content.RefEntry:    {remove: audit.EntryDelete},
	content.RefTerm:     {audit.TermCreate, audit.TermSave, audit.TermDelete},
	content.RefMedia:    {save: audit.MediaSave, remove: audit.MediaDelete},
	content.RefAuthor:   {audit.AuthorCreate, audit.AuthorSave, audit.AuthorDelete},
	content.RefMenu:     {audit.MenuCreate, audit.MenuSave, audit.MenuDelete},
	content.RefForm:     {audit.FormCreate, audit.FormSave, audit.FormDelete},
	content.RefRedirect: {audit.RedirectCreate, audit.RedirectSave, audit.RedirectDelete},
}

// handledKinds are the kinds that the operations that documentKind serves handle, for its error.
var handledKinds = map[string]string{
	"Create": "terms, authors, menus, forms and redirects",
	"Save":   "terms, media, authors, menus, forms and redirects",
	"Delete": "entries, terms, media, authors, menus, forms and redirects",
}

// documentKind returns what makes the documents of kind for the operation op, or the error that says why op does not
// handle them.
func documentKind(kind content.RefKind, op string) (func() document, error) {
	if newDoc, ok := docKinds[kind]; ok {
		return newDoc, nil
	}
	if kind == store.SettingsKind {
		return nil, fmt.Errorf("%s: it is saved with SaveSettings", settingsName)
	}
	if kind == content.RefEntry {
		return nil, errors.New("entries change through CreateEntry, SaveRevision, CreateTranslation and Delete")
	}
	return nil, fmt.Errorf("unsupported kind %q: %s handles %s", kind, op, handledKinds[op])
}

// decodeFile decodes f, a document, with decode, as the store and the build do: the problems, located in f and sorted,
// are the error. A file over content.MaxDocumentBytes is not read.
func decodeFile(f content.SiteFile, decode func([]byte) error) error {
	if len(f.Data) > content.MaxDocumentBytes {
		return content.Errors{{
			File: f.Name,
			Msg:  fmt.Sprintf("too large: %d bytes (max %d)", len(f.Data), content.MaxDocumentBytes),
		}}
	}
	return sortedError(content.DecodeFile(f, decode))
}

// sortedError returns err, sorted if it is content.Errors.
func sortedError(err error) error {
	var es content.Errors
	if errors.As(err, &es) {
		return sorted(es)
	}
	return err
}

// problemAt returns the problem at the JSON Pointer path in f, located in it.
func problemAt(f content.SiteFile, path, msg string) content.Errors {
	return content.LocateErrors(&content.Error{Path: path, Msg: msg}, f)
}

// normalized normalizes d, which was decoded from given, and returns what to store: given, if normalizing has
// not changed what d writes, else what it writes now.
func normalized(d normalizer, given []byte) ([]byte, error) {
	was, err := d.marshal()
	if err != nil {
		return nil, err
	}
	d.normalize()
	now, err := d.marshal()
	if err != nil {
		return nil, err
	}
	if bytes.Equal(was, now) {
		return given, nil
	}
	return now, nil
}

// validate validates d, a normalized document, in the site with the lookup of the index; the problems are the error,
// located in f. An error of the store in the lookup is the error instead of them.
func (r *run) validate(d document, site *content.Site, f content.SiteFile) error {
	err := d.validate(site, r.lookup)
	switch {
	case r.err != nil:
		return r.err
	case err != nil:
		return sorted(content.LocateErrors(err, f))
	}
	return nil
}

// current returns the current version of the document of kind with id, which must be the version base: else the
// error wraps ErrConflict, and if there is none, fs.ErrNotExist.
func (r *run) current(kind content.RefKind, id string, base int64) (store.Document, error) {
	d, err := r.tx.Document(kind, id)
	if err != nil {
		return store.Document{}, err
	}
	if d.Seq != base {
		return store.Document{}, conflict(docName(kind, id), d.Seq, base)
	}
	return d, nil
}

func conflict(name string, current, base int64) error {
	return fmt.Errorf("%s: %w: it is at version %d, not %d", name, ErrConflict, current, base)
}

// Create makes a document of kind (a term, an author, a menu, a form or a redirect) from data, its JSON without an id,
// which the store assigns. A term without a translationGroup gets a group of its own. A medium is added with AddMedia, with
// its file.
func (e *Editor) Create(ctx context.Context, op Op, kind content.RefKind, data []byte) (Result, error) {
	if err := checkOp(op); err != nil {
		return Result{}, err
	}
	if kind == content.RefMedia {
		return Result{}, errors.New("media are added with AddMedia")
	}
	newDoc, err := documentKind(kind, "Create")
	if err != nil {
		return Result{}, err
	}
	q := request{name: "create", params: []string{string(kind)}, data: data, action: actionsOf[kind].create}
	return e.run(ctx, op, q, func(r *run) (Result, error) {
		id, err := e.newID(kind)
		if err != nil {
			return Result{}, err
		}
		given := content.SiteFile{Name: docName(kind, id), Data: data}
		r.given[given.Name] = data
		d := newDoc()
		if err := decodeFile(given, d.unmarshal); err != nil {
			return Result{}, err
		}
		if *d.id() != "" {
			return Result{}, problemAt(given, "/id", "not allowed when creating; the store assigns the ID")
		}
		*d.id() = id
		if t, ok := d.value().(*content.Term); ok && t.TranslationGroup == "" {
			if t.TranslationGroup, err = e.newID(content.RefTranslationGroup); err != nil {
				return Result{}, err
			}
		}
		d.normalize()
		site, settings, err := r.site()
		if err != nil {
			return Result{}, err
		}
		if err := r.validate(d, site, given); err != nil {
			return Result{}, err
		}
		stored, err := d.marshal()
		if err != nil {
			return Result{}, err
		}
		v, err := r.tx.Put(kind, id, stored, r.by)
		if err != nil {
			return Result{}, err
		}
		after, err := r.model(site, settings)
		if err != nil {
			return Result{}, err
		}
		res := Result{Kind: kind, ID: id, Version: v, Publishes: true}
		return res, r.finish(&res, after)
	})
}

// Save saves data, the JSON of the document of kind with id, as its next version. base is the Seq of the version that
// the caller has read, which the store must still have as the current one: else the error wraps ErrConflict, and
// nothing is saved. Saving what the store has saves nothing. The save of a medium is that of its metadata, its alt, caption
// and fileName: a change of what comes from its file and its upload (hash, mimeType, size, width, height, uploadedAt and
// uploadedBy) is a problem, as ReplaceMedia replaces the file.
//
// If the save changes what the validation of other documents reads of this one (a term's taxonomy or language), what
// refers to it is validated again as the build validates it (6.7). A problem that this brings to a document, or to a
// published entry or its published revision, which the build validates, or to a scheduled entry or its scheduled revision,
// which its scheduled publish validates, fails the save, located in the bytes that the store has; one that it brings only
// to an entry that is neither published nor scheduled, or to the latest revision of one that does not publish or schedule
// it, is a warning. The problems that they had before are neither. A current version that this program cannot decode has
// no summary, so lookup did not find the document before: its save is not an error, and what refers to it is validated
// again as for a change of the Ref.
func (e *Editor) Save(ctx context.Context, op Op, kind content.RefKind, id string, base int64, data []byte) (Result, error) {
	if err := checkOp(op); err != nil {
		return Result{}, err
	}
	newDoc, err := documentKind(kind, "Save")
	if err != nil {
		return Result{}, err
	}
	if err := content.CheckID(kind, id); err != nil {
		return Result{}, err
	}
	q := request{
		name: "save", params: []string{string(kind), id, strconv.FormatInt(base, 10)}, data: data,
		action: actionsOf[kind].save, base: base,
	}
	return e.run(ctx, op, q, func(r *run) (Result, error) {
		cur, err := r.current(kind, id, base)
		if err != nil {
			return Result{}, err
		}
		given := content.SiteFile{Name: docName(kind, id), Data: data}
		r.given[given.Name] = data
		d := newDoc()
		if c, ok := d.(replacer); ok {
			c.replaces(cur.Data)
		}
		if err := decodeFile(given, d.unmarshal); err != nil {
			return Result{}, err
		}
		if got := *d.id(); got != "" && got != id {
			return Result{}, problemAt(given, "/id", fmt.Sprintf("id %q differs from the ID of the document, %q", got, id))
		}
		stored, err := normalized(d, data)
		if err != nil {
			return Result{}, err
		}
		site, settings, err := r.site()
		if err != nil {
			return Result{}, err
		}
		if err := r.validate(d, site, given); err != nil {
			return Result{}, err
		}
		// What lookup tells of the document now, to tell after the save whether it has changed. A current version that this
		// program cannot decode has no summary: lookup finds none, and the save, which makes one, is not an error.
		var old *store.Summary
		tracked := refHasFields(kind) && !bytes.Equal(stored, cur.Data)
		if tracked {
			if old, err = r.oldSummary(kind, id); err != nil {
				return Result{}, err
			}
		}
		v, err := r.tx.Put(kind, id, stored, r.by)
		if err != nil {
			return Result{}, err
		}
		res := Result{Kind: kind, ID: id, Version: v, Publishes: v.Seq != cur.Seq}
		if v.Seq == cur.Seq {
			return res, nil
		}
		if tracked {
			if res.Warnings, err = r.checkRefChange(site, kind, id, old); err != nil {
				return Result{}, err
			}
		}
		after, err := r.model(site, settings)
		if err != nil {
			return Result{}, err
		}
		return res, r.finish(&res, after)
	})
}

// maxListed is the most files that the error of a deletion names.
const maxListed = 10

// listed returns names for an error or a record: the first maxListed of them, and how many more there are.
func listed(names []string) string {
	list := strings.Join(names[:min(len(names), maxListed)], ", ")
	if len(names) > maxListed {
		list += fmt.Sprintf(" and %d more", len(names)-maxListed)
	}
	return list
}

// uses finds what refers to the document of kind with id. If a document, the published revision of an entry or the
// scheduled revision of a scheduled entry does, the error is that it cannot be deleted, which names their files. Otherwise
// the warnings are those for the entries whose last revision, which is neither published nor scheduled, refers to it.
func (r *run) uses(kind content.RefKind, id string) (content.Errors, error) {
	sources, err := r.referrers(kind, id)
	if err != nil {
		return nil, err
	}
	name := docName(kind, id)
	var files []string
	var warnings content.Errors
	for _, s := range sources {
		var entry store.Summary
		if s.kind == content.RefEntry {
			if entry, err = r.rd.Summary(s.kind, s.id); err != nil {
				return nil, err
			}
		}
		used := false
		if slices.Contains(s.roles, store.RoleDocument) {
			files, used = append(files, docName(s.kind, s.id)), true
		}
		if slices.Contains(s.roles, store.RolePublished) && entry.Entry != nil {
			files, used = append(files, revisionName(entry.Entry.PublishedRevision)), true
		}
		if slices.Contains(s.roles, store.RoleScheduled) && entry.Entry != nil && entry.Entry.Status == content.StatusScheduled {
			files, used = append(files, revisionName(entry.Entry.ScheduledRevision)), true
		}
		if !used && entry.Entry != nil && slices.Contains(s.roles, store.RoleLatest) {
			warnings = append(warnings, &content.Error{File: name, Msg: fmt.Sprintf(
				"deleted although used by %s, the latest revision of %s, which is not published",
				revisionName(entry.Latest), docName(s.kind, s.id))})
		}
	}
	if len(files) == 0 {
		return warnings, nil
	}
	slices.Sort(files)
	files = slices.Compact(files)
	return nil, content.Errors{{File: name, Msg: "cannot delete: used by " + listed(files)}}
}

// Delete deletes the document of kind with id, which must be at the version base (as in Save). It refuses a document
// that published content uses: another document, the published revision of an entry, or the scheduled revision of a
// scheduled entry, which is published later; the error names the first maxListed files that use it. One that only drafts
// use (the last revision of an entry that is neither published nor scheduled) it deletes, with a warning for each. The
// settings document cannot be deleted. Nor can an entry that is published or scheduled; the revisions of an entry that is
// deleted stay, and so does the original of a medium that is deleted, which the store has no way to remove.
func (e *Editor) Delete(ctx context.Context, op Op, kind content.RefKind, id string, base int64) (Result, error) {
	if err := checkOp(op); err != nil {
		return Result{}, err
	}
	if kind == store.SettingsKind {
		return Result{}, fmt.Errorf("%s: cannot be deleted", settingsName)
	}
	if kind != content.RefEntry {
		if _, err := documentKind(kind, "Delete"); err != nil {
			return Result{}, err
		}
	}
	if err := content.CheckID(kind, id); err != nil {
		return Result{}, err
	}
	q := request{
		name: "delete", params: []string{string(kind), id, strconv.FormatInt(base, 10)}, action: actionsOf[kind].remove, base: base,
	}
	return e.run(ctx, op, q, func(r *run) (Result, error) {
		cur, err := r.current(kind, id, base)
		if err != nil {
			return Result{}, err
		}
		if kind == content.RefEntry {
			if err := checkDeletable(id, cur.Data); err != nil {
				return Result{}, err
			}
		}
		warnings, err := r.uses(kind, id)
		if err != nil {
			return Result{}, err
		}
		site, settings, err := r.site()
		if err != nil {
			return Result{}, err
		}
		v, err := r.tx.Delete(kind, id, r.by)
		if err != nil {
			return Result{}, err
		}
		after, err := r.model(site, settings)
		if err != nil {
			return Result{}, err
		}
		res := Result{Kind: kind, ID: id, Version: v, Warnings: warnings, Publishes: kind != content.RefEntry}
		return res, r.finish(&res, after)
	})
}

// settingsDoc is the settings document, decoded.
type settingsDoc struct{ site content.Site }

func (d *settingsDoc) normalize()               { d.site.NormalizeSettings() }
func (d *settingsDoc) marshal() ([]byte, error) { return d.site.MarshalSettings() }

// SaveSettings saves data, the JSON of the settings document, as its next version, or as its first if the store has
// none, which base 0 says; else base is the Seq of the version that the caller has read (as in Save). The settings are
// validated with the schema file and with what the store has.
//
// What the build validates of every other document reads the settings, so a save that changes them validates again every
// document of every kind, every published and scheduled revision and the latest revision of every entry, which
// takes a time that grows with the site. The problems that this brings fail the save and the warnings are in the result,
// as for Save. If the settings that it replaces do not load, every problem is new.
func (e *Editor) SaveSettings(ctx context.Context, op Op, base int64, data []byte) (Result, error) {
	if err := checkOp(op); err != nil {
		return Result{}, err
	}
	q := request{
		name: "saveSettings", params: []string{strconv.FormatInt(base, 10)}, data: data, action: audit.SettingsSave, base: base,
	}
	return e.run(ctx, op, q, func(r *run) (Result, error) {
		var cur int64
		var curData []byte
		switch d, err := r.tx.Document(store.SettingsKind, ""); {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return Result{}, err
		default:
			cur, curData = d.Seq, d.Data
		}
		if cur != base {
			return Result{}, conflict(settingsName, cur, base)
		}
		given := content.SiteFile{Name: settingsName, Data: data}
		r.given[given.Name] = data
		d := new(settingsDoc)
		if err := decodeFile(given, d.site.UnmarshalSettings); err != nil {
			return Result{}, err
		}
		stored, err := normalized(d, data)
		if err != nil {
			return Result{}, err
		}
		file := content.SiteFile{Name: settingsName, Data: stored}
		site, err := content.LoadSite(file, e.schema, r.lookup)
		switch {
		case r.err != nil:
			return Result{}, r.err
		case err != nil:
			return Result{}, settingsProblems(err, given, file)
		}
		v, err := r.tx.Put(store.SettingsKind, "", stored, r.by)
		if err != nil {
			return Result{}, err
		}
		res := Result{Kind: store.SettingsKind, Version: v, Publishes: v.Seq != cur}
		if v.Seq == cur {
			return res, nil
		}
		// The settings that it replaces make the site before the change, which the Put did not change what lookup tells of.
		var old *content.Site
		if cur != 0 {
			if old, err = r.savedSite(curData); err != nil {
				return Result{}, err
			}
		}
		if res.Warnings, err = r.checkSettingsChange(site, old); err != nil {
			return Result{}, err
		}
		after, err := r.model(site, file)
		if err != nil {
			return Result{}, err
		}
		return res, r.finish(&res, after)
	})
}

// settingsProblems returns the problems of content.LoadSite of the settings file stored, which are located in the
// bytes of it, in the file that the caller gave when it differs: the problems in the schema file keep theirs.
func settingsProblems(err error, given, stored content.SiteFile) error {
	var es content.Errors
	if !errors.As(err, &es) || bytes.Equal(given.Data, stored.Data) {
		return sortedError(err)
	}
	var out content.Errors
	for _, e := range es {
		if e.File != stored.Name {
			out = append(out, e)
			continue
		}
		c := *e
		c.Line, c.Column = 0, 0
		out = append(out, content.LocateErrors(&c, given)...)
	}
	return sorted(out)
}
