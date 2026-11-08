package edit

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/siteplan"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// withStatus returns the state ent with the status and the published revision given, and no scheduled revision or time.
func withStatus(ent content.Entry, status content.Status, published string) content.Entry {
	ent.Status, ent.PublishedRevision = status, published
	ent.ScheduledRevision, ent.ScheduledAt = "", ""
	return ent
}

// currentEntry returns the current version of the entry id, which must be the version base (as current does), and its
// state decoded.
func (r *run) currentEntry(id string, base int64) (store.Document, content.Entry, error) {
	cur, err := r.current(content.RefEntry, id, base)
	if err != nil {
		return store.Document{}, content.Entry{}, err
	}
	ent, err := decodeState(id, cur)
	if err != nil {
		return store.Document{}, content.Entry{}, err
	}
	return cur, ent, nil
}

// decodeState decodes the state of the entry id from its document cur, which may lack the id.
func decodeState(id string, cur store.Document) (content.Entry, error) {
	var ent content.Entry
	if err := decodeFile(content.SiteFile{Name: docName(content.RefEntry, id), Data: cur.Data}, ent.UnmarshalJSON); err != nil {
		return content.Entry{}, err
	}
	ent.ID = id
	return ent, nil
}

// checkLatest fails unless revision is the latest revision of the entry: if the entry has none to verb, that is a problem
// of its document, and if it has another one, the error wraps ErrConflict.
func (r *run) checkLatest(entry, revision, verb string) error {
	name := docName(content.RefEntry, entry)
	sum, err := r.rd.Summary(content.RefEntry, entry)
	switch {
	case err != nil:
		return err
	case sum.Latest == "":
		return content.Errors{{File: name, Msg: "has no revision to " + verb}}
	case sum.Latest != revision:
		return fmt.Errorf("%s: %w: its latest revision is %s, not %s", name, ErrConflict, sum.Latest, revision)
	}
	return nil
}

// revisionFile is a revision that the store has, and its file, decoded.
type revisionFile struct {
	id   string
	file content.SiteFile
	rev  *content.Revision
}

// readRevision reads the revision id and decodes it: the problems of decoding, located in its bytes, are the error.
func (r *run) readRevision(id string) (revisionFile, error) {
	stored, err := r.tx.Revision(id)
	if err != nil {
		return revisionFile{}, err
	}
	f := content.SiteFile{Name: revisionName(id), Data: stored.Data}
	rev := new(content.Revision)
	if err := decodeFile(f, rev.UnmarshalJSON); err != nil {
		return revisionFile{}, err
	}
	return revisionFile{id, f, rev}, nil
}

// formatIn returns t in the time zone of the site, in RFC 3339, to the second.
func formatIn(site *content.Site, t time.Time) (string, error) {
	zone, err := time.LoadLocation(site.TimeZone)
	if err != nil {
		return "", err
	}
	return t.In(zone).Format(time.RFC3339), nil
}

// lacksDate reports whether rev, a revision of an entry of the type typ, is that of a post and has no date (6.4.3).
func lacksDate(typ string, rev *content.Revision) bool {
	return typ == content.TypePost && rev.Date == ""
}

// withDate returns rev, a revision of an entry of the type typ, as it is published: for a post without a date, a copy of
// it with the time t as its date, in the time zone of the site, to the second; else rev itself.
func withDate(site *content.Site, typ string, rev *content.Revision, t time.Time) (*content.Revision, error) {
	if !lacksDate(typ, rev) {
		return rev, nil
	}
	date, err := formatIn(site, t)
	if err != nil {
		return nil, err
	}
	dated := *rev
	dated.Date = date
	return &dated, nil
}

// dated returns the revision that an entry of the type typ is published with, which is rv: for a post whose revision has
// no date, a copy of it with the date that when gives (withDate) and a new ID (6.4.2); the revision itself never changes
// (6.4.1). The bytes of the copy are kept in r.given, so that the problems in them are located in them.
func (r *run) dated(site *content.Site, typ string, rv revisionFile, when func() time.Time) (revisionFile, error) {
	if !lacksDate(typ, rv.rev) {
		return rv, nil
	}
	rev, err := withDate(site, typ, rv.rev, when())
	if err != nil {
		return revisionFile{}, err
	}
	id, err := r.e.newID(content.RefRevision)
	if err != nil {
		return revisionFile{}, err
	}
	data, err := rev.MarshalJSON()
	if err != nil {
		return revisionFile{}, err
	}
	f := content.SiteFile{Name: revisionName(id), Data: data}
	r.given[f.Name] = data
	return revisionFile{id, f, rev}, nil
}

// publishedAtSchedule returns rev, the revision of the scheduled entry ent, as its scheduled publish publishes it (withDate
// with the scheduled time): the revision to validate wherever a scheduled revision is validated as a published one. The
// state ent must be valid, so that its scheduledAt reads.
func publishedAtSchedule(site *content.Site, ent *content.Entry, rev *content.Revision) (*content.Revision, error) {
	at, _ := time.Parse(time.RFC3339, ent.ScheduledAt)
	return withDate(site, ent.Type, rev, at)
}

// checkPublishable validates the revision rv as the one that the entry state is published or scheduled with, as the build
// validates what it publishes; the problems, located in its bytes, are the error.
func (r *run) checkPublishable(site *content.Site, state *content.Entry, rv revisionFile) error {
	problems := locatedIn(validatePublished(site, state, rv.rev, r.lookup), rv.file)
	switch {
	case r.err != nil:
		return r.err
	case len(problems) > 0:
		return problems
	}
	return nil
}

// stateFile writes the state ent of an entry as MarshalJSON writes it (Q14-B), and keeps the bytes in r.given so that
// the problems in them are located in them.
func (r *run) stateFile(ent content.Entry) (content.SiteFile, error) {
	data, err := ent.MarshalJSON()
	if err != nil {
		return content.SiteFile{}, err
	}
	f := content.SiteFile{Name: docName(content.RefEntry, ent.ID), Data: data}
	r.given[f.Name] = data
	return f, nil
}

// Publish publishes the latest revision of the entry entry, and no other: its status becomes published, its published
// revision revision, and a scheduled revision and time, if it has them, are taken away. base is the Seq of the entry
// document that the caller has read, which the entry must still be at, and revision is the latest revision that it has
// read, which the entry must still have: else the error wraps ErrConflict, and nothing is saved. An entry without a
// revision is a problem of its document.
//
// It validates the new state of the entry and the revision as the build validates what it publishes, with the items that
// only a revision to be published needs (6.4.2); their problems are located in their bytes. It then runs the checks
// across the site (6.7) on the site before and the site after: the problems that are new, such as a slug that another
// page has, a parent that is not published or an output path that another output has, are the error, located as the
// build locates them, and nothing is saved; the new warnings are in the result.
//
// A post whose revision has no date gets a new revision: a copy of it with the date of the operation in the time
// zone of the site, to the second, which is then the latest revision and the one that is published. The revision is
// never changed (6.4.1). Result.Revision is the revision that is published, and Result.Status is published.
//
// If the entry was published and its path changes (with its slug, its date, its parent or its first category), the same
// Update registers a 301 redirect from the old path to the entry, and Result.Redirect is its ID. If a redirect has that
// from already, none is registered, and a warning says so unless that one goes to the entry. A redirect to the entry from
// its new path would be the same as the output path of its page: it is deleted, with a warning, also when the entry was
// not published before. The URLs that other documents change are not redirected.
//
// Publishing what is published, with its published revision, saves nothing.
func (e *Editor) Publish(ctx context.Context, op Op, entry string, base int64, revision string) (Result, error) {
	if err := checkOp(op); err != nil {
		return Result{}, err
	}
	if err := content.CheckID(content.RefEntry, entry); err != nil {
		return Result{}, err
	}
	if err := content.CheckID(content.RefRevision, revision); err != nil {
		return Result{}, err
	}
	q := request{name: "publish", params: []string{entry, strconv.FormatInt(base, 10), revision}}
	return e.run(ctx, op, q, func(r *run) (Result, error) {
		before := sync.OnceValues(r.before)
		cur, ent, err := r.currentEntry(entry, base)
		if err != nil {
			return Result{}, err
		}
		if err := r.checkLatest(entry, revision, "publish"); err != nil {
			return Result{}, err
		}
		site, settings, err := r.site()
		if err != nil {
			return Result{}, err
		}
		rv, err := r.readRevision(revision)
		if err != nil {
			return Result{}, err
		}
		return r.publish(cur, ent, rv, site, settings, e.now, before)
	})
}

// publish publishes the revision rv with the entry ent, whose current version is cur, in the Update of r: the state and the
// revision are validated, a post without a date gets a dated copy of the revision (when gives the date), the redirects
// change and the checks across the site run on the site before, which before makes, and the site after.
func (r *run) publish(cur store.Document, ent content.Entry, rv revisionFile, site *content.Site, settings content.SiteFile,
	when func() time.Time, before func() (*siteplan.Site, error)) (Result, error) {
	given := rv.id
	// The date goes into a new revision, which the validation below sees with its date.
	rv, err := r.dated(site, ent.Type, rv, when)
	if err != nil {
		return Result{}, err
	}
	state := withStatus(ent, content.StatusPublished, rv.id)
	res := Result{Kind: content.RefEntry, ID: ent.ID, Version: cur.Version, Revision: rv.id, Status: content.StatusPublished}
	if state == ent {
		return res, nil
	}
	ef, err := r.stateFile(state)
	if err != nil {
		return Result{}, err
	}
	if problems := locatedIn(state.Validate(site), ef); len(problems) > 0 {
		return Result{}, problems
	}
	if err := r.checkPublishable(site, &state, rv); err != nil {
		return Result{}, err
	}

	if rv.id != given {
		if _, err := r.tx.AddRevision(ent.ID, rv.id, rv.file.Data, r.by); err != nil {
			return Result{}, err
		}
	}
	v, err := r.tx.Put(content.RefEntry, ent.ID, ef.Data, r.by)
	if err != nil {
		return Result{}, err
	}
	after, err := r.model(site, settings)
	if err != nil {
		return Result{}, err
	}
	res.Version, res.Publishes = v, true
	if err := r.updateRedirects(&res, ent, rv.file, site, after, before); err != nil {
		return Result{}, err
	}
	return res, r.finishWith(&res, after, before)
}

// updateRedirects changes the redirects for the entry ent, whose new revision rf the Update has published, in the store
// and in after, the model of the site with the change, so that the checks across the site see them. If ent was
// published and its path has changed, it registers a redirect from the old path, unless a redirect has that from. It
// deletes the redirects to ent from its new path, which would have the same path as the page.
func (r *run) updateRedirects(res *Result, ent content.Entry, rf content.SiteFile, site *content.Site, after *siteplan.Site,
	before func() (*siteplan.Site, error)) error {
	var old string
	if ent.Status == content.StatusPublished {
		m, err := before()
		if err != nil {
			return err
		}
		if m != nil { // nil if the site did not load before: then the entry had no path
			old = plannedPath(m, ent.ID)
		}
	}
	var toEntry []string
	for id, d := range after.Redirects {
		if d.V.To == ent.ID {
			toEntry = append(toEntry, id)
		}
	}
	if old == "" && len(toEntry) == 0 {
		return nil
	}
	now := plannedPath(after, ent.ID)
	if now == "" {
		return nil
	}
	from := content.CanonicalFrom(now)
	key := siteplan.FromKey(from)
	slices.Sort(toEntry)
	for _, id := range toEntry {
		if siteplan.FromKey(after.Redirects[id].V.From) != key {
			continue
		}
		if _, err := r.tx.Delete(content.RefRedirect, id, r.by); err != nil {
			return err
		}
		delete(after.Redirects, id)
		res.Warnings = append(res.Warnings, &content.Error{
			File: docName(content.RefRedirect, id),
			Msg:  fmt.Sprintf("deleted: its from is the new path %q of %s", from, docName(content.RefEntry, ent.ID)),
		})
	}
	if old == "" || siteplan.FromKey(old) == key {
		return nil
	}
	return r.registerRedirect(res, ent.ID, old, rf, site, after)
}

// registerRedirect saves a 301 redirect from old, the old path of the entry, to the entry, whose revision rf is
// published, and sets res.Redirect. If a redirect of after has that from (the first by ID counts, as in the build), it
// saves nothing, and the warning says so unless that redirect goes to the entry.
func (r *run) registerRedirect(res *Result, entry, old string, rf content.SiteFile, site *content.Site, after *siteplan.Site) error {
	from := content.CanonicalFrom(old)
	key := siteplan.FromKey(from)
	same := ""
	for id, d := range after.Redirects {
		if siteplan.FromKey(d.V.From) == key && (same == "" || id < same) {
			same = id
		}
	}
	if same != "" {
		if after.Redirects[same].V.To != entry {
			msg := fmt.Sprintf("the redirect from %q is not registered: %s has the same from", from, docName(content.RefRedirect, same))
			res.Warnings = append(res.Warnings, locatedIn(&content.Error{Path: "/slug", Msg: msg}, rf)...)
		}
		return nil
	}
	id, err := r.e.newID(content.RefRedirect)
	if err != nil {
		return err
	}
	rd := content.Redirect{ID: id, From: from, To: entry, Status: 301}
	rd.Normalize()
	data, err := rd.MarshalJSON()
	if err != nil {
		return err
	}
	f := content.SiteFile{Name: docName(content.RefRedirect, id), Data: data}
	r.given[f.Name] = data
	problems := locatedIn(rd.Validate(site, r.lookup), f)
	switch {
	case r.err != nil:
		return r.err
	case len(problems) > 0:
		return problems
	}
	if _, err := r.tx.Put(content.RefRedirect, id, data, r.by); err != nil {
		return err
	}
	after.Redirects[id] = &siteplan.Doc[content.Redirect]{File: content.SiteFile{Name: f.Name}, V: rd}
	res.Redirect = id
	return nil
}

// Unpublish makes the entry entry, which is published, scheduled or archived, a draft: it takes away its published
// revision and its scheduled revision and time. base is the Seq of the entry document that the caller has read (as in
// Publish). The revisions stay. Unpublishing a draft saves nothing.
//
// The checks across the site run on the site before and the site after, so that what other published content needs fails
// with the problem that the build has: the front page and the posts page of the settings (`/frontPage/ja: entry "..." is
// not published`), and the parent of a published page (`/parent: entry "..." is not published`). A redirect to the entry
// is left out of the build, which the warning in the result says. Result.Status is draft.
func (e *Editor) Unpublish(ctx context.Context, op Op, entry string, base int64) (Result, error) {
	return e.retire(ctx, op, "unpublish", content.StatusDraft, entry, base)
}

// Archive makes the entry entry, which is a draft, published or scheduled, archived: it takes away its published
// revision and its scheduled revision and time. base is the Seq of the entry document that the caller has read (as in
// Publish). The revisions stay. Archiving what is archived saves nothing.
//
// The checks across the site are those of Unpublish, which an archived entry needs as much as a draft. Result.Status is
// archived.
func (e *Editor) Archive(ctx context.Context, op Op, entry string, base int64) (Result, error) {
	return e.retire(ctx, op, "archive", content.StatusArchived, entry, base)
}

// retire does Unpublish and Archive: it makes the entry a draft or archived, as to says. The build validates no entry
// that is not published, so the new state is not validated, and neither is a revision.
func (e *Editor) retire(ctx context.Context, op Op, name string, to content.Status, entry string, base int64) (Result, error) {
	if err := checkOp(op); err != nil {
		return Result{}, err
	}
	if err := content.CheckID(content.RefEntry, entry); err != nil {
		return Result{}, err
	}
	q := request{name: name, params: []string{entry, strconv.FormatInt(base, 10)}}
	return e.run(ctx, op, q, func(r *run) (Result, error) {
		cur, ent, err := r.currentEntry(entry, base)
		if err != nil {
			return Result{}, err
		}
		state := withStatus(ent, to, "")
		res := Result{Kind: content.RefEntry, ID: entry, Version: cur.Version, Status: to}
		if state == ent {
			return res, nil
		}
		site, settings, err := r.site()
		if err != nil {
			return Result{}, err
		}
		ef, err := r.stateFile(state)
		if err != nil {
			return Result{}, err
		}
		v, err := r.tx.Put(content.RefEntry, entry, ef.Data, r.by)
		if err != nil {
			return Result{}, err
		}
		after, err := r.model(site, settings)
		if err != nil {
			return Result{}, err
		}
		res.Version, res.Publishes = v, ent.Status == content.StatusPublished
		return res, r.finish(&res, after)
	})
}
