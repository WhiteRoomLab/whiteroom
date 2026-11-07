package edit

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
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
	var ent content.Entry
	if err := decodeFile(content.SiteFile{Name: docName(content.RefEntry, id), Data: cur.Data}, ent.UnmarshalJSON); err != nil {
		return store.Document{}, content.Entry{}, err
	}
	ent.ID = id
	return cur, ent, nil
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
		cur, ent, err := r.currentEntry(entry, base)
		if err != nil {
			return Result{}, err
		}
		name := docName(content.RefEntry, entry)
		sum, err := r.rd.Summary(content.RefEntry, entry)
		switch {
		case err != nil:
			return Result{}, err
		case sum.Latest == "":
			return Result{}, content.Errors{{File: name, Msg: "has no revision to publish"}}
		case sum.Latest != revision:
			return Result{}, fmt.Errorf("%s: %w: its latest revision is %s, not %s", name, ErrConflict, sum.Latest, revision)
		}
		site, settings, err := r.site()
		if err != nil {
			return Result{}, err
		}
		stored, err := r.tx.Revision(revision)
		if err != nil {
			return Result{}, err
		}
		rf := content.SiteFile{Name: revisionName(revision), Data: stored.Data}
		var rev content.Revision
		if err := decodeFile(rf, rev.UnmarshalJSON); err != nil {
			return Result{}, err
		}

		// The date goes into a new revision, which the validation below sees with its date.
		id := revision
		if ent.Type == content.TypePost && rev.Date == "" {
			zone, err := time.LoadLocation(site.TimeZone)
			if err != nil {
				return Result{}, err
			}
			rev.Date = e.now().In(zone).Format(time.RFC3339)
			if id, err = e.newID(content.RefRevision); err != nil {
				return Result{}, err
			}
			data, err := rev.MarshalJSON()
			if err != nil {
				return Result{}, err
			}
			rf = content.SiteFile{Name: revisionName(id), Data: data}
			r.given[rf.Name] = data
		}

		state := withStatus(ent, content.StatusPublished, id)
		res := Result{Kind: content.RefEntry, ID: entry, Version: cur.Version, Revision: id, Status: content.StatusPublished}
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
		problems := locatedIn(validatePublished(site, &state, &rev, r.lookup), rf)
		switch {
		case r.err != nil:
			return Result{}, r.err
		case len(problems) > 0:
			return Result{}, problems
		}

		if id != revision {
			if _, err := r.tx.AddRevision(entry, id, rf.Data, r.by); err != nil {
				return Result{}, err
			}
		}
		v, err := r.tx.Put(content.RefEntry, entry, ef.Data, r.by)
		if err != nil {
			return Result{}, err
		}
		after, err := r.model(site, settings)
		if err != nil {
			return Result{}, err
		}
		res.Version, res.Publishes = v, true
		return res, r.finish(&res, after)
	})
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
