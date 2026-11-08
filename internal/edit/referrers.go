package edit

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// A save can make other documents invalid without changing them: when it changes what their validation reads of the
// saved document (its Ref, which a lookup tells), or the site settings. They are validated again as the build validates
// them (Q16-A), each as a unit, under the state after the change and, if that has problems, under the state before it, to
// tell the problems that are new. New problems in what the build validates are errors, and so are those in a scheduled
// entry and its scheduled revision, which its scheduled publish validates in the same way; those only in drafts, which are
// not validated, are warnings.

type lookupFunc = func(kind content.RefKind, id string) (content.Ref, bool)

// validateDocument validates v, the document of kind that decoding has made (a pointer, such as *content.Term), as the
// build validates the documents of the kinds that have no drafts.
func validateDocument(kind content.RefKind, v any, site *content.Site, lookup lookupFunc) error {
	switch x := v.(type) {
	case *content.Term:
		return x.Validate(site, lookup)
	case *content.Media:
		return x.Validate(site)
	case *content.AuthorProfile:
		return x.Validate(site, lookup)
	case *content.Menu:
		return x.Validate(site, lookup)
	case *content.Form:
		return x.Validate(site, lookup)
	case *content.Redirect:
		return x.Validate(site, lookup)
	}
	return fmt.Errorf("unsupported kind %q", kind)
}

// validatePublished validates rev, the published revision of the entry ent, as the build does: for publishing, as an
// import, which accepts what saving accepted, and without the theme, whose mismatches only warn when rendering.
func validatePublished(site *content.Site, ent *content.Entry, rev *content.Revision, lookup lookupFunc) error {
	return rev.Validate(content.RevisionOptions{Site: site, Entry: ent, Publish: true, Import: true, Lookup: lookup})
}

type decodable interface{ UnmarshalJSON(data []byte) error }

// emptyDocuments make the documents of the kinds that the build validates without drafts, to decode into.
var emptyDocuments = map[content.RefKind]func() decodable{
	content.RefTerm:     func() decodable { return new(content.Term) },
	content.RefMedia:    func() decodable { return new(content.Media) },
	content.RefAuthor:   func() decodable { return new(content.AuthorProfile) },
	content.RefMenu:     func() decodable { return new(content.Menu) },
	content.RefForm:     func() decodable { return new(content.Form) },
	content.RefRedirect: func() decodable { return new(content.Redirect) },
}

// refHasFields reports whether a lookup tells more of an entity of kind than that it exists: the type and the language of
// an entry, the taxonomy and the language of a term, the kind of a medium.
func refHasFields(kind content.RefKind) bool {
	return kind == content.RefEntry || kind == content.RefTerm || kind == content.RefMedia
}

// checkEnv is the state that a validation sees: the site, and the lookup of the entities. A nil site means that the
// state is not known.
type checkEnv struct {
	site   *content.Site
	lookup lookupFunc
}

// unit is the validation of one document, or of one revision and the state of its entry, as the store has it: check decodes
// the bytes, finds no problem in what does not decode, and returns the problems located in them. draft says that they are
// warnings, as the build does not validate what the unit validates.
type unit struct {
	draft bool
	check func(env checkEnv) (content.Errors, error)
}

// locatedIn returns the problems in err, located in f and sorted.
func locatedIn(err error, f content.SiteFile) content.Errors {
	return sorted(content.LocateErrors(err, f))
}

// documentUnit validates the document of kind with id, whose bytes are data, as the build does: a term, a medium, an
// author, a menu, a form or a redirect.
func documentUnit(kind content.RefKind, id string, data []byte) unit {
	f := content.SiteFile{Name: docName(kind, id), Data: data}
	return unit{check: func(env checkEnv) (content.Errors, error) {
		newDoc, ok := emptyDocuments[kind]
		if !ok {
			return nil, fmt.Errorf("unsupported kind %q", kind)
		}
		v := newDoc()
		if content.DecodeFile(f, v.UnmarshalJSON) != nil {
			return nil, nil
		}
		return locatedIn(validateDocument(kind, v, env.site, env.lookup), f), nil
	}}
}

// settingsUnit validates the settings document, whose bytes are data, as a document that refers to others: it loads
// again with the schema file, which validates it with the lookup of the state.
func (r *run) settingsUnit(data []byte) unit {
	f := content.SiteFile{Name: settingsName, Data: data}
	return unit{check: func(env checkEnv) (content.Errors, error) {
		_, err := content.LoadSite(f, r.e.schema, env.lookup)
		var problems content.Errors
		if err != nil && !errors.As(err, &problems) {
			return nil, err
		}
		return sorted(problems), nil
	}}
}

// entryState decodes f, an entry document, and validates its state in site: ok is false if f does not decode. The
// problems of the state are located in f.
func entryState(f content.SiteFile, site *content.Site) (ent content.Entry, problems content.Errors, ok bool) {
	if content.DecodeFile(f, ent.UnmarshalJSON) != nil {
		return content.Entry{}, nil, false
	}
	return ent, locatedIn(ent.Validate(site), f), true
}

// decodedRevision returns the revision id, decoded, and its file: the revision is nil if it does not decode. A revision
// that the store does not have is an error that wraps fs.ErrNotExist.
func (r *run) decodedRevision(id string) (content.SiteFile, *content.Revision, error) {
	stored, err := r.tx.Revision(id)
	if err != nil {
		return content.SiteFile{}, nil, err
	}
	f := content.SiteFile{Name: revisionName(id), Data: stored.Data}
	rev := new(content.Revision)
	if content.DecodeFile(f, rev.UnmarshalJSON) != nil {
		return f, nil, nil
	}
	return f, rev, nil
}

// previousRevision returns the revision that comes before latest among the revisions of the entry in the order of saving,
// decoded: nil if there is none, or it does not decode.
func (r *run) previousRevision(entry, latest string) (*content.Revision, error) {
	revs, err := r.tx.Revisions(entry)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(revs, func(v store.Revision) bool { return v.ID == latest })
	if i <= 0 {
		return nil, nil
	}
	return r.lastRevision(revs[i-1].ID)
}

// publishedUnit validates the published entry id, whose document is data, as the build does: its state, and if that is
// valid, its published revision, which it reads. A published revision that the store does not have is the problem that
// the build reports.
func (r *run) publishedUnit(id string, data []byte) unit {
	return r.publicationUnit(id, data, "/publishedRevision", func(ent *content.Entry) string { return ent.PublishedRevision })
}

// scheduledUnit validates the scheduled entry id, whose document is data, as publishedUnit validates a published entry: its
// state, and if that is valid, its scheduled revision, as the build would validate it if it were published. Its problems
// are not those of a draft, as the scheduled publish needs them to be none.
func (r *run) scheduledUnit(id string, data []byte) unit {
	return r.publicationUnit(id, data, "/scheduledRevision", func(ent *content.Entry) string { return ent.ScheduledRevision })
}

// publicationUnit validates the entry id, whose document is data, and the revision that revision finds in its state, which
// the entry publishes now or later: a revision that the store does not have is the problem at pointer.
func (r *run) publicationUnit(id string, data []byte, pointer string, revision func(*content.Entry) string) unit {
	f := content.SiteFile{Name: docName(content.RefEntry, id), Data: data}
	return unit{check: func(env checkEnv) (content.Errors, error) {
		ent, problems, ok := entryState(f, env.site)
		if !ok || len(problems) > 0 {
			return problems, nil
		}
		revID := revision(&ent)
		rf, rev, err := r.decodedRevision(revID)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return problemAt(f, pointer, fmt.Sprintf("revision %q does not exist", revID)), nil
		case err != nil || rev == nil:
			return nil, err
		}
		return locatedIn(validatePublished(env.site, &ent, rev, env.lookup), rf), nil
	}}
}

// stateUnit validates the state of the entry id, which is not published, whose document is data: a draft, as the build
// does not validate it.
func stateUnit(id string, data []byte) unit {
	f := content.SiteFile{Name: docName(content.RefEntry, id), Data: data}
	return unit{draft: true, check: func(env checkEnv) (content.Errors, error) {
		_, problems, _ := entryState(f, env.site)
		return problems, nil
	}}
}

// latestUnit validates latest, the latest revision of the entry id, which the entry does not publish and whose document
// is data, as SaveRevision validated it when it saved it, which the build does not: it needs the state of the entry to be
// valid, which another unit validates. The revision that it follows only makes validation accept more (the blocks and
// the styles that it kept), so it is read only if the revision has problems without it.
func (r *run) latestUnit(id string, data []byte, latest string) unit {
	f := content.SiteFile{Name: docName(content.RefEntry, id), Data: data}
	return unit{draft: true, check: func(env checkEnv) (content.Errors, error) {
		ent, problems, ok := entryState(f, env.site)
		if !ok || len(problems) > 0 {
			return nil, nil
		}
		rf, rev, err := r.decodedRevision(latest)
		if err != nil || rev == nil {
			return nil, err
		}
		opts := content.RevisionOptions{Site: env.site, Entry: &ent, Theme: r.e.theme.Settings, Lookup: env.lookup}
		invalid := rev.Validate(opts)
		if invalid != nil {
			prev, err := r.previousRevision(id, latest)
			if err != nil {
				return nil, err
			}
			if prev != nil {
				opts.Previous = prev
				invalid = rev.Validate(opts)
			}
		}
		return locatedIn(invalid, rf), nil
	}}
}

// referrer is a document that refers to another, and how it does.
type referrer struct {
	kind  content.RefKind
	id    string
	roles []store.Role
}

// referrers returns the documents that refer to the document of kind with id, but not that document itself, in the order
// of their kinds and IDs.
func (r *run) referrers(kind content.RefKind, id string) ([]referrer, error) {
	refs, err := r.rd.Referrers(kind, id)
	if err != nil {
		return nil, err
	}
	var out []referrer
	for _, ref := range refs {
		switch last := len(out) - 1; {
		case ref.Kind == kind && ref.ID == id:
		case last >= 0 && out[last].kind == ref.Kind && out[last].id == ref.ID:
			out[last].roles = append(out[last].roles, ref.Role)
		default:
			out = append(out, referrer{ref.Kind, ref.ID, []store.Role{ref.Role}})
		}
	}
	return out, nil
}

// referrerUnits returns the validations of what refers to the document of kind with id: the documents that name it, the
// published entries whose published revision names it, the scheduled entries whose scheduled revision names it, and the
// latest revisions that name it of the entries that do not publish or schedule them. An entry document names nothing.
func (r *run) referrerUnits(kind content.RefKind, id string) ([]unit, error) {
	refs, err := r.referrers(kind, id)
	if err != nil {
		return nil, err
	}
	var units []unit
	for _, ref := range refs {
		switch {
		case ref.kind == store.SettingsKind:
			d, err := r.tx.Document(store.SettingsKind, "")
			if err != nil {
				return nil, err
			}
			units = append(units, r.settingsUnit(d.Data))
		case ref.kind != content.RefEntry:
			d, err := r.tx.Document(ref.kind, ref.id)
			if err != nil {
				return nil, err
			}
			units = append(units, documentUnit(ref.kind, ref.id, d.Data))
		default:
			sum, err := r.rd.Summary(content.RefEntry, ref.id)
			if err != nil {
				return nil, err
			}
			published := sum.Entry != nil && sum.Entry.Status == content.StatusPublished && slices.Contains(ref.roles, store.RolePublished)
			scheduled := sum.Entry != nil && sum.Entry.Status == content.StatusScheduled && slices.Contains(ref.roles, store.RoleScheduled)
			latest := sum.Latest != "" && slices.Contains(ref.roles, store.RoleLatest) && !publishesLatest(sum)
			if !published && !scheduled && !latest {
				continue
			}
			d, err := r.tx.Document(content.RefEntry, ref.id)
			if err != nil {
				return nil, err
			}
			if published {
				units = append(units, r.publishedUnit(ref.id, d.Data))
			}
			if scheduled {
				units = append(units, r.scheduledUnit(ref.id, d.Data))
			}
			if latest {
				units = append(units, r.latestUnit(ref.id, d.Data, sum.Latest))
			}
		}
	}
	return units, nil
}

// publishesLatest reports whether the entry of sum is published or scheduled with its latest revision, which then the unit
// of its published or scheduled revision validates.
func publishesLatest(sum store.Summary) bool {
	e := sum.Entry
	if e == nil {
		return false
	}
	return e.Status == content.StatusPublished && sum.Latest == e.PublishedRevision ||
		e.Status == content.StatusScheduled && sum.Latest == e.ScheduledRevision
}

// entryUnit returns the validation of the entry id with the status status, whose document is data: as published or
// scheduled, with its revision, else as a draft, with its state alone.
func (r *run) entryUnit(status content.Status, id string, data []byte) unit {
	switch status {
	case content.StatusPublished:
		return r.publishedUnit(id, data)
	case content.StatusScheduled:
		return r.scheduledUnit(id, data)
	case content.StatusDraft, content.StatusArchived:
	}
	return stateUnit(id, data)
}

// allUnits returns the validations of everything that the build validates, of the scheduled entries that its scheduled
// publish validates, and of the latest revisions that neither does: every term, medium, author, menu, form and redirect;
// every entry, as published (its state and its published revision), as scheduled (its state and its scheduled revision) or
// as a draft (its state); and the latest revision of every entry that does not publish or schedule it.
func (r *run) allUnits() ([]unit, error) {
	var units []unit
	for _, kind := range slices.Sorted(maps.Keys(emptyDocuments)) {
		docs, err := r.tx.Documents(kind)
		if err != nil {
			return nil, err
		}
		for _, d := range docs {
			units = append(units, documentUnit(kind, d.ID, d.Data))
		}
	}
	sums, err := r.rd.Summaries(content.RefEntry)
	if err != nil {
		return nil, err
	}
	docs, err := r.tx.Documents(content.RefEntry)
	if err != nil {
		return nil, err
	}
	data := make(map[string][]byte, len(docs))
	for _, d := range docs {
		data[d.ID] = d.Data
	}
	for _, sum := range sums {
		if sum.Entry == nil || data[sum.ID] == nil {
			continue
		}
		units = append(units, r.entryUnit(sum.Entry.Status, sum.ID, data[sum.ID]))
		if sum.Latest != "" && !publishesLatest(sum) {
			units = append(units, r.latestUnit(sum.ID, data[sum.ID], sum.Latest))
		}
	}
	return units, nil
}

// revalidate validates the units under the state after a change and returns the new problems as errors, those in the
// units of what the build validates, and as warnings, those in the drafts, sorted. The problems of a unit that the state
// before the change has too, by file, JSON Pointer and message, are not new; if that state is not known, every problem is.
// A store error of a read or of a lookup is the error.
func (r *run) revalidate(units []unit, after, before checkEnv) (errs, warnings content.Errors, err error) {
	for _, u := range units {
		now, err := u.check(after)
		switch {
		case r.err != nil:
			return nil, nil, r.err
		case err != nil:
			return nil, nil, err
		case len(now) == 0:
			continue
		}
		if before.site != nil {
			was, err := u.check(before)
			switch {
			case r.err != nil:
				return nil, nil, r.err
			case err != nil:
				return nil, nil, err
			}
			old := make(map[problemKey]bool, len(was))
			for _, e := range was {
				old[keyOf(e)] = true
			}
			now = slices.DeleteFunc(slices.Clone(now), func(e *content.Error) bool { return old[keyOf(e)] })
		}
		if u.draft {
			warnings = append(warnings, now...)
		} else {
			errs = append(errs, now...)
		}
	}
	return sorted(errs), sorted(warnings), nil
}

// oldSummary returns the summary of the document of kind with id that lookup finds, which a save compares with the one that
// it makes: nil if lookup finds none, as for a current version that this program cannot decode, which a save may replace.
func (r *run) oldSummary(kind content.RefKind, id string) (*store.Summary, error) {
	sum, err := r.rd.Summary(kind, id)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, err
	}
	return &sum, nil
}

// checkRefChange validates again what refers to the document of kind (an entry, a term or a medium) with id, which the
// Update has saved, if what a lookup tells about it has changed from old, its summary before (oldSummary): the warnings
// are the new problems in drafts, and the error is the new problems in what the build validates (content.Errors), or an
// error of the store. The state before it is that with the Ref of old, in which the problems that it had are not new; if
// old is nil, lookup found no such document before, and the Ref has changed.
func (r *run) checkRefChange(site *content.Site, kind content.RefKind, id string, old *store.Summary) (content.Errors, error) {
	now, err := r.rd.Summary(kind, id)
	if err != nil {
		return nil, err
	}
	if old != nil && refOf(now) == refOf(*old) {
		return nil, nil
	}
	units, err := r.referrerUnits(kind, id)
	if err != nil {
		return nil, err
	}
	before := func(k content.RefKind, i string) (content.Ref, bool) {
		if k == kind && i == id {
			if old == nil {
				return content.Ref{}, false
			}
			return refOf(*old), true
		}
		return r.lookup(k, i)
	}
	errs, warnings, err := r.revalidate(units, checkEnv{site: site, lookup: r.lookup}, checkEnv{site: site, lookup: before})
	switch {
	case err != nil:
		return nil, err
	case len(errs) > 0:
		return nil, errs
	}
	return warnings, nil
}

// checkSettingsChange validates again every document, and every revision that the build validates and the latest ones
// that it does not, in site, the settings that the Update has saved, and returns the new problems as checkRefChange does.
// old is the site of the settings that they replace, nil if it is not known.
func (r *run) checkSettingsChange(site, old *content.Site) (content.Errors, error) {
	units, err := r.allUnits()
	if err != nil {
		return nil, err
	}
	errs, warnings, err := r.revalidate(units, checkEnv{site: site, lookup: r.lookup}, checkEnv{site: old, lookup: r.lookup})
	switch {
	case err != nil:
		return nil, err
	case len(errs) > 0:
		return nil, errs
	}
	return warnings, nil
}

// savedSite loads data, the settings document that the store had, with the schema file as r.site does: nil if it does not
// load.
func (r *run) savedSite(data []byte) (*content.Site, error) {
	site, err := content.LoadSite(content.SiteFile{Name: settingsName, Data: data}, r.e.schema, r.lookup)
	switch {
	case r.err != nil:
		return nil, r.err
	case err != nil:
		return nil, nil
	}
	return site, nil
}
