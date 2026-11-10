package edit

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/siteplan"
)

// revisionDoc is a decoded revision of an entry of the type typ, as a normalizer.
type revisionDoc struct {
	rev *content.Revision
	typ *content.TypeDef
}

func (d revisionDoc) normalize()               { d.rev.Normalize(d.typ) }
func (d revisionDoc) marshal() ([]byte, error) { return d.rev.MarshalJSON() }

// newEntry makes the state of a draft entry of the type typ in language, and its document, whose bytes it puts into r.given
// so that the problems of the entry are located in them. The entry is in the translation group group, which is new if
// group is "", and has the number after the largest that the store has had.
func (r *run) newEntry(site *content.Site, typ, language, group string) (content.Entry, content.SiteFile, error) {
	id, err := r.e.newID(content.RefEntry)
	if err != nil {
		return content.Entry{}, content.SiteFile{}, err
	}
	if group == "" {
		if group, err = r.e.newID(content.RefTranslationGroup); err != nil {
			return content.Entry{}, content.SiteFile{}, err
		}
	}
	last, err := r.tx.LastNumber()
	if err != nil {
		return content.Entry{}, content.SiteFile{}, err
	}
	zone, err := time.LoadLocation(site.TimeZone)
	if err != nil {
		return content.Entry{}, content.SiteFile{}, err
	}
	entry := content.Entry{
		ID:               id,
		Number:           last + 1,
		Type:             typ,
		Language:         language,
		TranslationGroup: group,
		Status:           content.StatusDraft,
		CreatedAt:        r.e.now().In(zone).Format(time.RFC3339),
		CreatedBy:        r.by,
	}
	data, err := entry.MarshalJSON()
	if err != nil {
		return content.Entry{}, content.SiteFile{}, err
	}
	f := content.SiteFile{Name: docName(content.RefEntry, id), Data: data}
	r.given[f.Name] = data
	return entry, f, nil
}

// CreateEntry makes a draft entry of the type typ in language, with a new ID, the next number of the site and a
// translation group of its own, and no revision. Its CreatedAt is the time of the operation in the time zone of the site,
// and its CreatedBy is op.By. Its problems, such as an unknown type, are located in the document that it makes.
func (e *Editor) CreateEntry(ctx context.Context, op Op, typ, language string) (Result, error) {
	if err := checkOp(op); err != nil {
		return Result{}, err
	}
	q := request{name: "createEntry", params: []string{typ, language}}
	return e.run(ctx, op, q, func(r *run) (Result, error) {
		site, settings, err := r.site()
		if err != nil {
			return Result{}, err
		}
		entry, f, err := r.newEntry(site, typ, language, "")
		if err != nil {
			return Result{}, err
		}
		if err := entry.Validate(site); err != nil {
			return Result{}, sorted(content.LocateErrors(err, f))
		}
		v, err := r.tx.Put(content.RefEntry, entry.ID, f.Data, r.by)
		if err != nil {
			return Result{}, err
		}
		after, err := r.model(site, settings)
		if err != nil {
			return Result{}, err
		}
		res := Result{Kind: content.RefEntry, ID: entry.ID, Version: v, Number: entry.Number}
		return res, r.finish(&res, after)
	})
}

// validateRevision validates rev, a normalized revision of the entry ent that follows prev (nil if it follows none), as one
// that is to be published if publish is true. The problems, located in f, are returned; an error of the store in the
// lookup is the error instead of them.
func (r *run) validateRevision(site *content.Site, ent *content.Entry, rev, prev *content.Revision, publish bool, f content.SiteFile) (content.Errors, error) {
	err := rev.Validate(content.RevisionOptions{
		Site: site, Entry: ent, Publish: publish, Previous: prev, Theme: r.e.theme.Settings, Lookup: r.lookup,
	})
	if r.err != nil {
		return nil, r.err
	}
	return sorted(content.LocateErrors(err, f)), nil
}

// checkRevision validates rev as validateRevision does: its problems are the error, except for the missing items that
// only a revision to be published needs (6.4.2), which are the warnings that it returns.
func (r *run) checkRevision(site *content.Site, ent *content.Entry, rev, prev *content.Revision, f content.SiteFile) (content.Errors, error) {
	errs, err := r.validateRevision(site, ent, rev, prev, false, f)
	if err != nil {
		return nil, err
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return r.validateRevision(site, ent, rev, prev, true, f)
}

// lastRevision returns the revision id of an entry, decoded: nil if id is "", or if it does not decode.
func (r *run) lastRevision(id string) (*content.Revision, error) {
	if id == "" {
		return nil, nil
	}
	stored, err := r.tx.Revision(id)
	if err != nil {
		return nil, err
	}
	var rev content.Revision
	if rev.UnmarshalJSON(stored.Data) != nil {
		return nil, nil
	}
	return &rev, nil
}

// publishedCheck runs the checks of the build on the site as it would be if the entry ent, with the revision rev whose ID
// is id, were published, which model is the model of the site without it: the problems and the warnings that are new.
func (r *run) publishedCheck(model func() (*siteplan.Site, error), ent *content.Entry, id string, rev *content.Revision) (problems, warnings content.Errors, err error) {
	m, err := model()
	if err != nil {
		return nil, nil, err
	}
	after := *m
	after.Entries = maps.Clone(m.Entries)
	published := &siteplan.Entry{
		File:    content.SiteFile{Name: docName(content.RefEntry, ent.ID)},
		E:       *ent,
		RevFile: content.SiteFile{Name: revisionName(id)},
		Rev:     rev,
	}
	published.E.Status, published.E.PublishedRevision = content.StatusPublished, id
	after.Entries[ent.ID] = published
	return r.checkSite(&after, model)
}

// publishedWarnings returns the problems that the site would have if the entry ent, with the revision rev whose ID is id,
// were published, which model is the model of the site without it, as warnings: those that the checks of the
// build find in a site in which it is.
func (r *run) publishedWarnings(model func() (*siteplan.Site, error), ent *content.Entry, id string, rev *content.Revision) (content.Errors, error) {
	problems, warnings, err := r.publishedCheck(model, ent, id, rev)
	if err != nil {
		return nil, err
	}
	return slices.Concat(problems, warnings), nil
}

// sameValue reports whether a and b are the same JSON value, which differ in the order of the keys of objects and in the
// way that numbers are written.
func sameValue(a, b jsontext.Value) bool {
	ca, cb := a.Clone(), b.Clone()
	if ca.Canonicalize() != nil || cb.Canonicalize() != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(ca, cb)
}

// syncedWarnings returns the warnings for the fields of typ that keep one value in every translation (translatable: false,
// 6.4.5) whose values in rev, the revision that the entry ent is to get, differ from those in the latest revision of
// another entry of its translation group, of the same type. They are located in f.
func (r *run) syncedWarnings(model func() (*siteplan.Site, error), typ *content.TypeDef, ent *content.Entry, rev *content.Revision, f content.SiteFile) (content.Errors, error) {
	var synced []string
	if typ != nil {
		for _, fd := range typ.Fields {
			if fd.Synced {
				synced = append(synced, fd.Name)
			}
		}
	}
	if len(synced) == 0 || ent.TranslationGroup == "" {
		return nil, nil
	}
	m, err := model()
	if err != nil {
		return nil, err
	}
	var warnings content.Errors
	for id, d := range m.Entries {
		other := d.E
		if id == ent.ID || other.TranslationGroup != ent.TranslationGroup || other.Type != ent.Type {
			continue
		}
		sum, err := r.rd.Summary(content.RefEntry, id)
		if err != nil {
			return nil, err
		}
		last, err := r.lastRevision(sum.Latest)
		if err != nil {
			return nil, err
		}
		if last == nil {
			continue
		}
		for _, name := range synced {
			if !sameValue(rev.Fields[name], last.Fields[name]) {
				warnings = append(warnings, problemAt(f, "/fields/"+name, fmt.Sprintf(
					"differs from the value in %s, the latest revision of the translation in %s; "+
						"a field with translatable: false has one value in every translation",
					revisionName(sum.Latest), other.Language))...)
			}
		}
	}
	return warnings, nil
}

func orNone(id string) string {
	if id == "" {
		return "none"
	}
	return id
}

// SaveRevision adds data, the JSON of a revision, to the entry entry as its latest one, and does not publish it. base is
// the ID of the latest revision that the caller has read, "" if there is none, which the entry must still have: else the
// error wraps ErrConflict, and nothing is added. The revision is normalized and validated as a draft: its problems are the
// error. The warnings are the items that only a revision to be published needs, the problems that the site would
// have if the entry were published with it, and the fields that keep one value in every translation (translatable:
// false) with a value that differs from that of another translation (6.4.5), which the operation leaves as they are. The
// result has the ID of the revision and the version of the entry document, which the operation does not change.
func (e *Editor) SaveRevision(ctx context.Context, op Op, entry, base string, data []byte) (Result, error) {
	if err := checkOp(op); err != nil {
		return Result{}, err
	}
	if err := content.CheckID(content.RefEntry, entry); err != nil {
		return Result{}, err
	}
	if base != "" {
		if err := content.CheckID(content.RefRevision, base); err != nil {
			return Result{}, err
		}
	}
	q := request{name: "saveRevision", params: []string{entry, base}, data: data}
	return e.run(ctx, op, q, func(r *run) (Result, error) {
		sum, err := r.rd.Summary(content.RefEntry, entry)
		if err != nil {
			return Result{}, err
		}
		if sum.Latest != base {
			return Result{}, fmt.Errorf("%s: %w: its latest revision is %s, not %s",
				docName(content.RefEntry, entry), ErrConflict, orNone(sum.Latest), orNone(base))
		}
		site, settings, err := r.site()
		if err != nil {
			return Result{}, err
		}
		ent := *sum.Entry
		ent.ID = entry
		id, err := e.newID(content.RefRevision)
		if err != nil {
			return Result{}, err
		}
		f := content.SiteFile{Name: revisionName(id), Data: data}
		r.given[f.Name] = data
		var rev content.Revision
		if err := decodeFile(f, rev.UnmarshalJSON); err != nil {
			return Result{}, err
		}
		typ := site.Type(ent.Type)
		stored, err := normalized(revisionDoc{&rev, typ}, data)
		if err != nil {
			return Result{}, err
		}
		prev, err := r.lastRevision(sum.Latest)
		if err != nil {
			return Result{}, err
		}
		warnings, err := r.checkRevision(site, &ent, &rev, prev, f)
		if err != nil {
			return Result{}, err
		}
		model := sync.OnceValues(func() (*siteplan.Site, error) { return r.model(site, settings) })
		if len(warnings) == 0 {
			published, err := r.publishedWarnings(model, &ent, id, &rev)
			if err != nil {
				return Result{}, err
			}
			warnings = published
		}
		synced, err := r.syncedWarnings(model, typ, &ent, &rev, f)
		if err != nil {
			return Result{}, err
		}
		if _, err := r.tx.AddRevision(entry, id, stored, r.by); err != nil {
			return Result{}, err
		}
		doc, err := r.tx.Document(content.RefEntry, entry)
		if err != nil {
			return Result{}, err
		}
		return Result{
			Kind: content.RefEntry, ID: entry, Version: doc.Version, Revision: id,
			Warnings: sorted(slices.Concat(warnings, synced)),
		}, nil
	})
}

// translators returns what Revision.Translate takes to find the translations into language of the entries and the terms of
// m, by their translation groups. Of two with a group and language, which the checks of the site report, the first in the
// order of IDs is the translation. The entry self, which is new, is the translation of the entries of the group group.
func translators(m *siteplan.Site, language, group, self string) (entry, term func(id string) (string, bool)) {
	entries, terms := make(map[string]string), make(map[string]string)
	for _, id := range slices.Sorted(maps.Keys(m.Entries)) {
		if e := &m.Entries[id].E; e.Language == language && e.TranslationGroup != "" && entries[e.TranslationGroup] == "" {
			entries[e.TranslationGroup] = id
		}
	}
	for _, id := range slices.Sorted(maps.Keys(m.Terms)) {
		if t := &m.Terms[id].V; t.Language == language && t.TranslationGroup != "" && terms[t.TranslationGroup] == "" {
			terms[t.TranslationGroup] = id
		}
	}
	if entries[group] == "" {
		entries[group] = self
	}
	entry = func(id string) (string, bool) {
		if d := m.Entries[id]; d != nil {
			to, ok := entries[d.E.TranslationGroup]
			return to, ok
		}
		return "", false
	}
	term = func(id string) (string, bool) {
		if d := m.Terms[id]; d != nil {
			to, ok := terms[d.V.TranslationGroup]
			return to, ok
		}
		return "", false
	}
	return entry, term
}

// CreateTranslation makes a draft entry in language that translates the entry source: it has the type and the
// translation group of source, and a first revision that copies the latest revision of source with the entries that its
// links go to, its terms and its parent moved to their translations in language (6.4.5). Links and terms that have none
// are kept or left out with a warning. A group that has an entry in language already, as source has in its own, is an
// error, as are the problems of the revision as SaveRevision finds them. The result has the new entry's ID, version and
// number, and the ID of its revision.
func (e *Editor) CreateTranslation(ctx context.Context, op Op, source, language string) (Result, error) {
	if err := checkOp(op); err != nil {
		return Result{}, err
	}
	if err := content.CheckID(content.RefEntry, source); err != nil {
		return Result{}, err
	}
	q := request{name: "createTranslation", params: []string{source, language}}
	return e.run(ctx, op, q, func(r *run) (Result, error) {
		sum, err := r.rd.Summary(content.RefEntry, source)
		if err != nil {
			return Result{}, err
		}
		site, settings, err := r.site()
		if err != nil {
			return Result{}, err
		}
		src := *sum.Entry
		src.ID = source
		if src.TranslationGroup == "" {
			return Result{}, content.Errors{{File: docName(content.RefEntry, source), Msg: "has no translation group"}}
		}
		entry, ef, err := r.newEntry(site, src.Type, language, src.TranslationGroup)
		if err != nil {
			return Result{}, err
		}
		var problems content.Errors
		if err := entry.Validate(site); err != nil {
			problems = content.LocateErrors(err, ef)
		}
		if language == src.Language {
			problems = append(problems, problemAt(ef, "/language", fmt.Sprintf("language %q is that of the entry it translates", language))...)
		}
		if len(problems) > 0 {
			return Result{}, sorted(problems)
		}
		if sum.Latest == "" {
			return Result{}, content.Errors{{File: docName(content.RefEntry, source), Msg: "has no revision to translate"}}
		}
		id, err := e.newID(content.RefRevision)
		if err != nil {
			return Result{}, err
		}
		rev, prev, err := r.copyRevision(sum.Latest)
		if err != nil {
			return Result{}, err
		}

		model := sync.OnceValues(func() (*siteplan.Site, error) { return r.model(site, settings) })
		m, err := model()
		if err != nil {
			return Result{}, err
		}
		typ := site.Type(src.Type)
		entryIn, termIn := translators(m, language, src.TranslationGroup, entry.ID)
		moved := rev.Translate(typ, language, entryIn, termIn)
		data, err := rev.MarshalJSON()
		if err != nil {
			return Result{}, err
		}
		f := content.SiteFile{Name: revisionName(id), Data: data}
		r.given[f.Name] = data
		var warnings content.Errors
		if len(moved) > 0 {
			warnings = content.LocateErrors(moved, f)
		}
		// The entry is put first, so that the revision finds it if it links to it, as it can; if the operation fails, nothing stays.
		v, err := r.tx.Put(content.RefEntry, entry.ID, ef.Data, r.by)
		if err != nil {
			return Result{}, err
		}
		publishOnly, err := r.checkRevision(site, &entry, rev, prev, f)
		if err != nil {
			return Result{}, err
		}
		warnings = append(warnings, publishOnly...)

		// The new entry is a draft that joins the translation group, which the group must allow.
		draft := *m
		draft.Entries = maps.Clone(m.Entries)
		draft.Entries[entry.ID] = &siteplan.Entry{File: content.SiteFile{Name: ef.Name}, E: entry}
		bad, redirects, err := r.checkSite(&draft, model)
		if err != nil {
			return Result{}, err
		}
		if len(bad) > 0 {
			return Result{}, bad
		}
		warnings = append(warnings, redirects...)
		if len(publishOnly) == 0 {
			published, err := r.publishedWarnings(model, &entry, id, rev)
			if err != nil {
				return Result{}, err
			}
			warnings = append(warnings, published...)
		}
		synced, err := r.syncedWarnings(model, typ, &entry, rev, f)
		if err != nil {
			return Result{}, err
		}

		if _, err := r.tx.AddRevision(entry.ID, id, data, r.by); err != nil {
			return Result{}, err
		}
		return Result{
			Kind: content.RefEntry, ID: entry.ID, Version: v, Revision: id, Number: entry.Number,
			Warnings: sorted(slices.Concat(warnings, synced)),
		}, nil
	})
}

// copyRevision returns two decodings of the revision id, the latest of an entry: the copy that a translation changes, and
// the revision that its checks compare it to.
func (r *run) copyRevision(id string) (rev, prev *content.Revision, err error) {
	stored, err := r.tx.Revision(id)
	if err != nil {
		return nil, nil, err
	}
	f := content.SiteFile{Name: revisionName(id), Data: stored.Data}
	rev, prev = new(content.Revision), new(content.Revision)
	if err := decodeFile(f, rev.UnmarshalJSON); err != nil {
		return nil, nil, err
	}
	if err := decodeFile(f, prev.UnmarshalJSON); err != nil {
		return nil, nil, err
	}
	return rev, prev, nil
}

// checkDeletable returns the error that an entry whose document is data cannot be deleted, if its status is published or
// scheduled: it must be unpublished first.
func checkDeletable(id string, data []byte) error {
	var entry content.Entry
	if err := entry.UnmarshalJSON(data); err != nil {
		return err
	}
	if entry.Status == content.StatusPublished || entry.Status == content.StatusScheduled {
		return content.Errors{{
			File: docName(content.RefEntry, id), Path: "/status",
			Msg: "cannot delete a published or scheduled entry; unpublish it first",
		}}
	}
	return nil
}
