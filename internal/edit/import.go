package edit

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strconv"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/mediafile"
	"github.com/WhiteRoomLab/whiteroom/internal/siteplan"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// Source is what Import reads a site from: the methods of build.Source, so that a site directory (internal/sitedir) is
// one, and the build and the import read a site in the same way.
type Source interface {
	Settings() (settings, schema content.SiteFile, err error)
	Documents(kind content.RefKind) (map[string]content.SiteFile, error)
	Revision(id string) (content.SiteFile, error)
	OpenMedia(hash string) (fs.File, error)
	Theme() (fs.FS, error)
}

// importKinds are the kinds of the documents of a Source, in the order that Import stores them.
var importKinds = []content.RefKind{
	content.RefEntry, content.RefTerm, content.RefMedia, content.RefAuthor, content.RefMenu, content.RefForm,
	content.RefRedirect,
}

var errNotEmpty = errors.New("the store is not empty: Import needs a store without documents")

// Import imports the site that src reads into s, which must have no documents, as the user by: the settings, every
// document, the revisions that its entries publish or schedule and the original of every medium, with the IDs and the
// numbers that its files have. The revisions that no entry publishes or schedules are not read, and the theme and
// the schema file are read only to validate the site.
//
// It runs in the stages of the build (4.3), each of which stops at its problems: reading the site; decoding its
// documents; loading the settings and the theme; validating the documents and the revisions that the published entries
// publish and the scheduled entries schedule, as imports without the theme; and checking the originals of the media,
// which it saves in the store, against their documents. Then one Update stores everything and checks the site across its
// documents with the code of the build (internal/siteplan), on the model that the index of the store gives. It validates
// also the entries that are not published, and normalizes the documents as a save does before it validates them; it
// stores them as their files have them unless that changes them.
//
// The problems are the error, content.Errors with the files, lines and columns of the files that the source gave. Where
// the build finds the same problem in a stage that Import has too, they are the build's lines; the exceptions are the
// texts about the hash and the format of the original of an image, and the duplicates of output paths that need the files of
// the release, which only the build finds. A failed Import stores no document, but it may leave originals, which nothing
// uses. One Update stores everything, so the engine's limit of a transaction bounds the size of the site (item 136 of
// 6.12). On success, the warnings are those of the redirects.
func Import(ctx context.Context, s *store.Store, src Source, by string) (content.Errors, error) {
	if err := checkOp(Op{By: by}); err != nil {
		return nil, err
	}
	// Before anything is read or saved, so that the originals of a site do not go into a store that has documents.
	if err := s.View(ctx, checkEmpty); err != nil {
		return nil, err
	}
	im := &importer{ctx: ctx, store: s, src: src, by: by}
	if err := im.read(); err != nil {
		return nil, err
	}
	for _, stage := range []func() (content.Errors, error){im.decode, im.loadSite, im.validate, im.originals} {
		problems, err := stage()
		if err != nil {
			return nil, err
		}
		if len(problems) > 0 {
			return nil, problems
		}
	}
	return im.write()
}

// checkEmpty fails if the store has a current document of any kind, the settings document included.
func checkEmpty(tx *store.Tx) error {
	for _, kind := range slices.Concat([]content.RefKind{store.SettingsKind}, importKinds) {
		current, err := tx.Documents(kind)
		if err != nil {
			return err
		}
		if len(current) > 0 {
			return errNotEmpty
		}
	}
	return nil
}

// imported is a document or a revision of the source that Import stores.
type imported struct {
	id    string
	entry string           // of a revision: the entry that publishes or schedules it
	file  content.SiteFile // as the source gave it, but named as in a site directory
	v     any              // the decoded document: *content.Entry, or the pointer that docKinds makes; nil for a revision
	data  []byte           // what to store: the bytes of file, unless normalizing has changed what the document writes
}

type importer struct {
	ctx   context.Context
	store *store.Store
	src   Source
	by    string

	settings, schema content.SiteFile
	files            map[content.RefKind]map[string]content.SiteFile
	themeFS          fs.FS

	docs      map[content.RefKind][]*imported // in the order of their IDs
	revisions []*imported                     // in the order of the entries that publish them
	kept      map[string]bool                 // the IDs of the revisions in revisions
	sp        *siteplan.Site                  // the documents as decoded, which validation looks up
	site      *content.Site
	ed        *Editor // of the store, with the schema file and the theme of the source
}

// read reads the settings, the documents of every kind and the theme, as the build does.
func (im *importer) read() error {
	var err error
	if im.settings, im.schema, err = im.src.Settings(); err != nil {
		return err
	}
	im.files = make(map[content.RefKind]map[string]content.SiteFile, len(importKinds))
	for _, kind := range importKinds {
		if im.files[kind], err = im.src.Documents(kind); err != nil {
			return err
		}
	}
	im.themeFS, err = im.src.Theme()
	return err
}

// decode decodes the documents, in the order of the kinds and of the IDs, and normalizes all but the entries, which have
// nothing to normalize. The id in a document, if any, must be its ID. The documents get the names that the model of the
// index gives them, which are those of a site directory.
func (im *importer) decode() (content.Errors, error) {
	im.sp = &siteplan.Site{
		Settings: im.settings, Schema: im.schema,
		Entries: make(map[string]*siteplan.Entry), Terms: make(map[string]*siteplan.Doc[content.Term]),
		Media: make(map[string]*siteplan.Doc[content.Media]), Authors: make(map[string]*siteplan.Doc[content.AuthorProfile]),
		Menus: make(map[string]*siteplan.Doc[content.Menu]), Forms: make(map[string]*siteplan.Doc[content.Form]),
		Redirects: make(map[string]*siteplan.Doc[content.Redirect]),
	}
	im.docs = make(map[content.RefKind][]*imported, len(importKinds))
	im.kept = make(map[string]bool)
	var errs content.Errors
	for _, kind := range importKinds {
		for _, id := range slices.Sorted(maps.Keys(im.files[kind])) {
			f := im.files[kind][id]
			f.Name = docName(kind, id)
			d, problems, err := decodeDoc(kind, id, f)
			if err != nil {
				return nil, err
			}
			errs = append(errs, problems...)
			if d != nil {
				im.docs[kind] = append(im.docs[kind], d)
				addToModel(im.sp, d)
			}
		}
	}
	return sorted(errs), nil
}

// decodeDoc decodes f, the document of kind with id, and normalizes it. The problems are those of decoding; the error is
// one of writing the normalized document.
func decodeDoc(kind content.RefKind, id string, f content.SiteFile) (*imported, content.Errors, error) {
	d := &imported{id: id, file: f, data: f.Data}
	var got string
	var doc document
	if kind == content.RefEntry {
		ent := new(content.Entry)
		if err := decodeFile(f, ent.UnmarshalJSON); err != nil {
			return nil, errorsOf(err), nil
		}
		d.v, got = ent, ent.ID
	} else {
		doc = docKinds[kind]()
		if err := decodeFile(f, doc.unmarshal); err != nil {
			return nil, errorsOf(err), nil
		}
		d.v, got = doc.value(), *doc.id()
	}
	if got != "" && got != id {
		return nil, problemAt(f, "/id", fmt.Sprintf("id %q differs from the ID of the document, %q", got, id)), nil
	}
	if doc != nil {
		var err error
		if d.data, err = normalized(doc, f.Data); err != nil {
			return nil, nil, err
		}
	}
	return d, nil, nil
}

func addToModel(sp *siteplan.Site, d *imported) {
	switch v := d.v.(type) {
	case *content.Entry:
		sp.Entries[d.id] = &siteplan.Entry{File: d.file, E: *v}
	case *content.Term:
		sp.Terms[d.id] = &siteplan.Doc[content.Term]{File: d.file, V: *v}
	case *content.Media:
		sp.Media[d.id] = &siteplan.Doc[content.Media]{File: d.file, V: *v}
	case *content.AuthorProfile:
		sp.Authors[d.id] = &siteplan.Doc[content.AuthorProfile]{File: d.file, V: *v}
	case *content.Menu:
		sp.Menus[d.id] = &siteplan.Doc[content.Menu]{File: d.file, V: *v}
	case *content.Form:
		sp.Forms[d.id] = &siteplan.Doc[content.Form]{File: d.file, V: *v}
	case *content.Redirect:
		sp.Redirects[d.id] = &siteplan.Doc[content.Redirect]{File: d.file, V: *v}
	}
}

// loadSite loads the settings, with the lookup of the documents, and the theme. The problems are those of the settings and
// then those of the theme, which are in theme/, each in its own order.
func (im *importer) loadSite() (content.Errors, error) {
	site, siteErr := content.LoadSite(im.settings, im.schema, im.sp.Ref)
	th, themeErr := theme.Load(im.themeFS)
	if siteErr != nil || themeErr != nil {
		errs := errorsOf(siteErr)
		for _, e := range errorsOf(themeErr) {
			c := *e
			c.File = path.Join("theme", c.File)
			errs = append(errs, &c)
		}
		return errs, nil
	}
	im.site = site
	im.sp.Site, im.sp.Theme = site, th
	im.ed = New(im.store, im.schema, th)
	return nil, nil
}

// validate validates every entry and, for those that are published or scheduled, the revision that they publish or
// schedule, which it reads; and every other document. The revisions are validated as the build validates them: as imports
// that need what publishing needs, without the theme.
func (im *importer) validate() (content.Errors, error) {
	var errs content.Errors
	for _, d := range im.docs[content.RefEntry] {
		if err := im.ctx.Err(); err != nil {
			return nil, err
		}
		ent := d.v.(*content.Entry)
		if err := ent.Validate(im.site); err != nil {
			errs = append(errs, content.LocateErrors(err, d.file)...)
			continue
		}
		var problems content.Errors
		var err error
		switch ent.Status {
		case content.StatusPublished:
			problems, err = im.validateRevision(d, ent, ent.PublishedRevision, "/publishedRevision")
		case content.StatusScheduled:
			problems, err = im.validateRevision(d, ent, ent.ScheduledRevision, "/scheduledRevision")
		case content.StatusDraft, content.StatusArchived:
			continue
		}
		if err != nil {
			return nil, err
		}
		errs = append(errs, problems...)
	}
	for _, kind := range importKinds {
		if kind == content.RefEntry {
			continue
		}
		for _, d := range im.docs[kind] {
			if err := validateDocument(kind, d.v, im.site, im.sp.Ref); err != nil {
				errs = append(errs, content.LocateErrors(err, d.file)...)
			}
		}
	}
	return sorted(errs), nil
}

// validateRevision reads the revision id, which ent, the entry in d, publishes or schedules, as pointer says, normalizes
// and validates it, and keeps it to store. A revision that two entries name is kept once.
func (im *importer) validateRevision(d *imported, ent *content.Entry, id, pointer string) (content.Errors, error) {
	f, err := im.src.Revision(id)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return problemAt(d.file, pointer, fmt.Sprintf("revision %q does not exist", id)), nil
	case err != nil:
		return nil, err
	}
	f.Name = revisionName(id)
	var rev content.Revision
	if err := decodeFile(f, rev.UnmarshalJSON); err != nil {
		return errorsOf(err), nil
	}
	data, err := normalized(revisionDoc{&rev, im.site.Type(ent.Type)}, f.Data)
	if err != nil {
		return nil, err
	}
	// A post without a date is validated with the scheduled time as its date, as its scheduled publish publishes it; the
	// bytes that are stored stay as they are.
	checked := &rev
	if ent.Status == content.StatusScheduled {
		if checked, err = publishedAtSchedule(im.site, ent, &rev); err != nil {
			return nil, err
		}
	}
	if err := validatePublished(im.site, ent, checked, im.sp.Ref); err != nil {
		return content.LocateErrors(err, f), nil
	}
	if !im.kept[id] {
		im.kept[id] = true
		im.revisions = append(im.revisions, &imported{id: id, entry: d.id, file: f, data: data})
	}
	return nil, nil
}

// originals saves the original of every medium in the store, and not only those that published content uses, which the
// build checks; and checks it against the document of its medium: that it exists, its size and its hash, and the format and
// the size in pixels that internal/mediafile reads from it. What the build finds only after it has read more of an image,
// such as a side of more than 65535 pixels, is not refused.
func (im *importer) originals() (content.Errors, error) {
	var errs content.Errors
	for _, d := range im.docs[content.RefMedia] {
		problems, err := im.checkOriginal(d, d.v.(*content.Media))
		if err != nil {
			return nil, err
		}
		errs = append(errs, problems...)
	}
	return sorted(errs), nil
}

// checkOriginal saves the original of m, the medium in d, in the store, and checks it against m.
func (im *importer) checkOriginal(d *imported, m *content.Media) (content.Errors, error) {
	f, err := im.src.OpenMedia(m.Hash)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return problemAt(d.file, "/hash", "no original with this hash"), nil
	case err != nil:
		return nil, err
	}
	defer func() { _ = f.Close() }()
	stat, err := f.Stat()
	switch {
	case err != nil:
		return nil, err
	case stat.Size() != int64(m.Size):
		return problemAt(d.file, "/size", fmt.Sprintf("the original has %d bytes, not %d", stat.Size(), m.Size)), nil
	}
	o, err := im.store.PutOriginal(im.ctx, f)
	if err != nil {
		return nil, err
	}
	if o.Hash != m.Hash {
		return problemAt(d.file, "/hash", "the original has the hash "+o.Hash), nil
	}
	info, bad, err := im.ed.probe(o.Hash)
	if err != nil {
		return nil, err
	}
	return originalProblems(d.file, m, info, bad), nil
}

// originalProblems returns the problems of the original of m, the medium in f, whose format and size in pixels
// internal/mediafile has read as info, or bad if it cannot read them, as the build words them.
func originalProblems(f content.SiteFile, m *content.Media, info mediafile.Info, bad error) content.Errors {
	if m.Kind() == content.MediaPDF {
		switch {
		case bad != nil:
			return problemAt(f, "/mimeType", fmt.Sprintf("the original is not %s: %v", m.MIMEType, bad))
		case info.MIMEType != m.MIMEType:
			return problemAt(f, "/mimeType", fmt.Sprintf("the original is %s, not %s", info.MIMEType, m.MIMEType))
		}
		return nil
	}
	if bad != nil {
		return problemAt(f, "", "cannot read the original: "+bad.Error())
	}
	var errs content.Errors
	if info.MIMEType != m.MIMEType {
		errs = append(errs, problemAt(f, "/mimeType", fmt.Sprintf("the original is %s, not %s", info.MIMEType, m.MIMEType))...)
	}
	if info.Width != m.Width || info.Height != m.Height {
		pointer := "/width"
		if info.Width == m.Width {
			pointer = "/height"
		}
		errs = append(errs, problemAt(f, pointer, fmt.Sprintf("the original is %dx%d pixels as displayed, not %dx%d",
			info.Width, info.Height, m.Width, m.Height))...)
	}
	return errs
}

// write stores everything in one Update: the settings, the documents and the revisions, and then checks the site across
// its documents on the model of the index, every problem being new. The problems are located in the files that the source
// gave. If there are problems, nothing is stored.
func (im *importer) write() (content.Errors, error) {
	var warnings content.Errors
	_, err := im.ed.run(im.ctx, Op{By: im.by}, request{name: "import", action: audit.SiteImport}, func(r *run) (Result, error) {
		if err := checkEmpty(r.tx); err != nil {
			return Result{}, err
		}
		im.noteCounts(r)
		r.given[im.settings.Name] = im.settings.Data
		if _, err := r.tx.Put(store.SettingsKind, "", im.settings.Data, im.by); err != nil {
			return Result{}, err
		}
		for _, kind := range importKinds {
			for _, d := range im.docs[kind] {
				r.given[d.file.Name] = d.file.Data
				if _, err := r.tx.Put(kind, d.id, d.data, im.by); err != nil {
					return Result{}, err
				}
			}
		}
		for _, d := range im.revisions {
			r.given[d.file.Name] = d.file.Data
			if _, err := r.tx.AddRevision(d.entry, d.id, d.data, im.by); err != nil {
				return Result{}, err
			}
		}
		after, err := r.model(im.site, im.settings)
		if err != nil {
			return Result{}, err
		}
		problems, redirects, err := r.checkSite(after, nil)
		switch {
		case err != nil:
			return Result{}, err
		case len(problems) > 0:
			return Result{}, problems
		}
		warnings = redirects
		return Result{}, nil
	})
	if err != nil {
		return nil, err
	}
	return warnings, nil
}

// noteCounts adds to the detail of the record how many documents of each kind, revisions and originals the import stores.
func (im *importer) noteCounts(r *run) {
	for _, kind := range importKinds {
		r.note(string(kind), strconv.Itoa(len(im.docs[kind])))
	}
	r.note("revisions", strconv.Itoa(len(im.revisions)))
	originals := make(map[string]bool)
	for _, d := range im.docs[content.RefMedia] {
		originals[d.v.(*content.Media).Hash] = true
	}
	r.note("originals", strconv.Itoa(len(originals)))
}

// errorsOf returns the problems in err: those of content.Errors, a *content.Error, or else err as one at the root.
func errorsOf(err error) content.Errors {
	var es content.Errors
	var e *content.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &es):
		return es
	case errors.As(err, &e):
		return content.Errors{e}
	}
	return content.Errors{{Msg: err.Error()}}
}
