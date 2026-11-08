package edit

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/siteplan"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// ErrNotDue is the error of PublishScheduled for an entry that is not scheduled, or whose time has not come yet.
var ErrNotDue = errors.New("not due")

// notDue is an error with its own text that wraps ErrNotDue.
type notDue string

func (e notDue) Error() string { return string(e) }
func (e notDue) Unwrap() error { return ErrNotDue }

// Schedule schedules the latest revision of the entry entry to be published at at, to the second, which must be after the
// time of the operation (else the error says to publish it instead): its status becomes scheduled, its scheduled revision
// revision, and its scheduled time at in the time zone of the site. Only an entry that is not published is scheduled; to
// schedule a published entry, unpublish it first, which also cancels a schedule. base and revision are those of Publish,
// and the error wraps ErrConflict in the same cases. An entry that is scheduled is scheduled again.
//
// It validates the new state of the entry and the revision as Publish does, and runs the checks across the site on the
// site as it would be if the entry were published with the revision: the problems that this would have, such as a slug
// that another page has, are the error, located as the build locates them, and nothing is saved; the new warnings are in
// the result. Nothing is published, so the result does not have Publishes, and no build is due.
//
// No revision is added. A post whose revision has no date is validated and checked as it will be published, with a copy
// of the revision in memory that has the scheduled time as its date; its problems are located in the bytes that the store
// has, in which a pointer that they lack, such as /date, is located at its nearest ancestor. The date goes into a new
// revision when the entry is published (PublishScheduled), or when Publish publishes it instead. Result.Revision is the
// scheduled revision, and Result.Status is scheduled. Scheduling what is scheduled in the same way saves nothing.
func (e *Editor) Schedule(ctx context.Context, op Op, entry string, base int64, revision string, at time.Time) (Result, error) {
	if err := checkOp(op); err != nil {
		return Result{}, err
	}
	if err := content.CheckID(content.RefEntry, entry); err != nil {
		return Result{}, err
	}
	if err := content.CheckID(content.RefRevision, revision); err != nil {
		return Result{}, err
	}
	at = at.Truncate(time.Second)
	q := request{name: "schedule", params: []string{entry, strconv.FormatInt(base, 10), revision, at.UTC().Format(time.RFC3339)}}
	return e.run(ctx, op, q, func(r *run) (Result, error) {
		cur, ent, err := r.currentEntry(entry, base)
		if err != nil {
			return Result{}, err
		}
		if err := r.checkLatest(entry, revision, "schedule"); err != nil {
			return Result{}, err
		}
		if ent.Status == content.StatusPublished {
			f := content.SiteFile{Name: docName(content.RefEntry, entry), Data: cur.Data}
			return Result{}, problemAt(f, "/status", "a published entry cannot be scheduled; unpublish it first")
		}
		site, settings, err := r.site()
		if err != nil {
			return Result{}, err
		}
		rv, err := r.readRevision(revision)
		if err != nil {
			return Result{}, err
		}
		scheduledAt, err := formatIn(site, at)
		if err != nil {
			return Result{}, err
		}

		state := withStatus(ent, content.StatusScheduled, "")
		state.ScheduledRevision, state.ScheduledAt = revision, scheduledAt
		res := Result{Kind: content.RefEntry, ID: entry, Version: cur.Version, Revision: revision, Status: content.StatusScheduled}
		if state == ent {
			return res, nil
		}
		ef, err := r.stateFile(state)
		if err != nil {
			return Result{}, err
		}
		problems := locatedIn(state.Validate(site), ef)
		if now := r.e.now(); !at.After(now) {
			text, err := formatIn(site, now)
			if err != nil {
				return Result{}, err
			}
			msg := fmt.Sprintf("%s is not after the time of the operation, %s; publish it instead", scheduledAt, text)
			problems = sorted(slices.Concat(problems, problemAt(ef, "/scheduledAt", msg)))
		}
		if len(problems) > 0 {
			return Result{}, problems
		}
		// The revision as it will be published, in memory: its problems are located in the bytes that the store has (rv.file).
		if rv.rev, err = publishedAtSchedule(site, &state, rv.rev); err != nil {
			return Result{}, err
		}
		if err := r.checkPublishable(site, &state, rv); err != nil {
			return Result{}, err
		}

		// The site as it would be with the entry published, which nothing is saved for: the model is made before the writes.
		model := sync.OnceValues(func() (*siteplan.Site, error) { return r.model(site, settings) })
		bad, warnings, err := r.publishedCheck(model, &ent, rv.id, rv.rev)
		switch {
		case err != nil:
			return Result{}, err
		case len(bad) > 0:
			return Result{}, bad
		}
		v, err := r.tx.Put(content.RefEntry, entry, ef.Data, r.by)
		if err != nil {
			return Result{}, err
		}
		res.Version, res.Warnings = v, warnings
		return res, nil
	})
}

// PublishScheduled publishes the scheduled revision of the entry entry if its status is scheduled and its scheduled time is
// not after now: it does what Publish does with that revision, with the same checks, and the entry is saved as the user who
// saved its current version, who scheduled it (Q9-A). It has no user and no operation ID: the scheduler that calls it
// retries by itself, and the state of the entry tells whether it is done. An entry that is not scheduled, or not due, is the
// error that wraps ErrNotDue, and nothing is saved. The state of the entry is validated first, with its schedule, so that a
// scheduledAt that does not parse, or a scheduled revision that the store does not have, is a problem of the document. The
// problems of the checks leave the entry scheduled.
//
// No redirect is registered, as the entry was not published; the redirects to the entry from its new path are deleted, as
// Publish does. Result.Status is published, and the result has Publishes.
//
// A post whose scheduled revision has no date gets a new revision: a copy of it with the scheduled time as its date, in
// the time zone of the site, which is then the latest revision and the one that is published. If the scheduled revision
// was not the latest revision, the editor has saved revisions since: the same Update then adds a copy of the latest one
// after it, so that the draft that the editor works on stays the latest revision. That copy has the same date if it is
// a post without a date, and is otherwise the same bytes; it is a draft, which nothing validates. Both revisions are saved
// as the user who scheduled the entry, and Result.Revision is the one that is published.
func (e *Editor) PublishScheduled(ctx context.Context, entry string, now time.Time) (Result, error) {
	if err := content.CheckID(content.RefEntry, entry); err != nil {
		return Result{}, err
	}
	q := request{name: "publishScheduled", params: []string{entry}}
	return e.run(ctx, Op{}, q, func(r *run) (Result, error) {
		before := sync.OnceValues(r.before)
		cur, err := r.tx.Document(content.RefEntry, entry)
		if err != nil {
			return Result{}, err
		}
		ent, err := decodeState(entry, cur)
		if err != nil {
			return Result{}, err
		}
		r.by = cur.SavedBy
		name := docName(content.RefEntry, entry)
		if ent.Status != content.StatusScheduled {
			return Result{}, notDue(name + ": not scheduled")
		}
		at, err := time.Parse(time.RFC3339, ent.ScheduledAt)
		if err == nil && at.After(now) {
			return Result{}, notDue(fmt.Sprintf("%s: scheduled at %s, after %s", name, ent.ScheduledAt, now.In(at.Location()).Format(time.RFC3339)))
		}
		site, settings, err := r.site()
		if err != nil {
			return Result{}, err
		}
		ef := content.SiteFile{Name: name, Data: cur.Data}
		if problems := locatedIn(ent.Validate(site), ef); len(problems) > 0 {
			return Result{}, problems
		}
		rv, err := r.readRevision(ent.ScheduledRevision)
		if errors.Is(err, fs.ErrNotExist) {
			return Result{}, problemAt(ef, "/scheduledRevision", fmt.Sprintf("revision %q does not exist", ent.ScheduledRevision))
		} else if err != nil {
			return Result{}, err
		}
		sum, err := r.rd.Summary(content.RefEntry, entry)
		if err != nil {
			return Result{}, err
		}
		res, err := r.publish(cur, ent, rv, site, settings, func() time.Time { return at }, before)
		if err != nil {
			return Result{}, err
		}
		// A dated copy is the latest revision now, which an editor's later revision must stay: it is copied after it.
		if res.Revision != rv.id && sum.Latest != "" && sum.Latest != rv.id {
			if err := r.copyLatest(site, ent, sum.Latest, at); err != nil {
				return Result{}, err
			}
		}
		return res, nil
	})
}

// copyLatest adds a copy of latest, the latest revision of the entry ent, as the latest revision again, after the scheduled
// publish has added the dated copy of an earlier one: a post without a date gets at as its date, in the time zone of the
// site, as that copy has it; any other revision is copied as it is.
func (r *run) copyLatest(site *content.Site, ent content.Entry, latest string, at time.Time) error {
	rv, err := r.readRevision(latest)
	if err != nil {
		return err
	}
	data := rv.file.Data
	if lacksDate(ent.Type, rv.rev) {
		dated, err := withDate(site, ent.Type, rv.rev, at)
		if err != nil {
			return err
		}
		if data, err = dated.MarshalJSON(); err != nil {
			return err
		}
	}
	id, err := r.e.newID(content.RefRevision)
	if err != nil {
		return err
	}
	_, err = r.tx.AddRevision(ent.ID, id, data, r.by)
	return err
}

// Scheduled is an entry that is scheduled, and its scheduled time.
type Scheduled struct {
	Entry string
	At    time.Time // zero if the scheduledAt of the entry does not parse
}

// Schedules returns the scheduled entries of the index, by their times and then their IDs. An entry whose scheduledAt does
// not parse has the zero time, so that it comes first and its scheduled publish reports its problem.
func (e *Editor) Schedules(ctx context.Context) ([]Scheduled, error) {
	var out []Scheduled
	err := e.store.View(ctx, func(tx *store.Tx) error {
		out = nil
		sums, err := e.reader(tx).Summaries(content.RefEntry)
		if err != nil {
			return err
		}
		for _, s := range sums {
			if s.Entry == nil || s.Entry.Status != content.StatusScheduled {
				continue
			}
			at, _ := time.Parse(time.RFC3339, s.Entry.ScheduledAt)
			out = append(out, Scheduled{Entry: s.ID, At: at})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b Scheduled) int {
		return cmp.Or(a.At.Compare(b.At), strings.Compare(a.Entry, b.Entry))
	})
	return out, nil
}
