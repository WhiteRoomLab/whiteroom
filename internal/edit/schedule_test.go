package edit

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// The time that the tests schedule the draft page for: an hour after testNow, and the same in the time zone of the site.
var careersAt = testNow.Add(time.Hour)

const (
	careersJST = "2026-11-07T20:00:00+09:00"
	otherUser  = "usr_01JB8T7H4MA2C5E8G1J4K7N3P7"
)

// jst is the offset of the time zone of the sample site.
var jst = time.FixedZone("", 9*60*60)

// usedByRoles returns the roles in which entries refer to the medium id, in the order of the index.
func usedByRoles(t *testing.T, s *store.Store, id string) []store.Role {
	t.Helper()
	var roles []store.Role
	err := s.View(t.Context(), func(tx *store.Tx) error {
		refs, err := tx.Referrers(content.RefMedia, id)
		for _, r := range refs {
			if r.Kind == content.RefEntry {
				roles = append(roles, r.Role)
			}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return roles
}

// scheduleCareers saves the revision careersRevision for the draft page draftEN, schedules it at careersAt as the user
// userID, and returns the revision.
func scheduleCareers(t *testing.T, ed *Editor, s *store.Store) string {
	t.Helper()
	rev := saveRevision(t, ed, s, draftEN, careersRevision).Revision
	must(t)(ed.Schedule(t.Context(), Op{By: userID}, draftEN, entrySeq(t, s, draftEN), rev, careersAt))
	return rev
}

// scheduledJSON returns the JSON of the entry whose document is data, scheduled with the revision rev at at.
func scheduledJSON(t *testing.T, data []byte, rev, at string) string {
	t.Helper()
	e := decodeEntry(t, data)
	e.Status, e.PublishedRevision, e.ScheduledRevision, e.ScheduledAt = content.StatusScheduled, "", rev, at
	out, err := e.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// publishedFrom returns the JSON of the entry whose document is data, as Publish writes it when it publishes the revision
// rev: published, and without a scheduled revision and time.
func publishedFrom(t *testing.T, data []byte, rev string) string {
	t.Helper()
	out, err := withStatus(decodeEntry(t, data), content.StatusPublished, rev).MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// storedScheduledAt is storedScheduled with the scheduled time at, which the store's own API takes as it is.
func storedScheduledAt(t *testing.T, id string, number int, rev, at string) string {
	t.Helper()
	return scheduledJSON(t, []byte(storedScheduled(t, id, number, testID(content.RefTranslationGroup, number), rev)), rev, at)
}

// sameSchedules fails the test if the scheduled entries got are not those want, with the same times.
func sameSchedules(t *testing.T, got, want []Scheduled) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("Schedules() = %+v, want %+v", got, want)
		return
	}
	for i := range got {
		if got[i].Entry != want[i].Entry || !got[i].At.Equal(want[i].At) {
			t.Errorf("Schedules()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// schedules returns what Schedules returns for ed, which must not fail.
func schedules(t *testing.T, ed *Editor) []Scheduled {
	t.Helper()
	got, err := ed.Schedules(t.Context())
	if err != nil {
		t.Fatalf("Schedules() = %v", err)
	}
	return got
}

// TestSchedule schedules the latest revision of a draft: the state, the summary, the result and the list of the schedules,
// and scheduling again.
func TestSchedule(t *testing.T) {
	ed, s := newSample(t)
	op := Op{By: userID}
	rev := saveRevision(t, ed, s, draftEN, careersRevision).Revision
	before := read(t, s, content.RefEntry, draftEN)
	if got := schedules(t, ed); len(got) != 0 {
		t.Fatalf("the sample site has the schedules %+v", got)
	}

	res, err := ed.Schedule(t.Context(), op, draftEN, before.Seq, rev, careersAt)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != content.RefEntry || res.ID != draftEN || res.Revision != rev || res.Status != content.StatusScheduled ||
		res.Publishes || res.Redirect != "" || res.Number != 0 || len(res.Warnings) != 0 {
		t.Errorf("Schedule() = %+v, want the entry %s scheduled with %s, which changes nothing that a build publishes, without warnings", res, draftEN, rev)
	}
	doc := read(t, s, content.RefEntry, draftEN)
	if doc.Version != res.Version || doc.Seq <= before.Seq || doc.SavedBy != userID || doc.Kind != content.RefEntry || doc.ID != draftEN {
		t.Errorf("Document() = %+v, Schedule() returned the version %+v after %d", doc.Version, res.Version, before.Seq)
	}
	want := decodeEntry(t, before.Data)
	want.Status, want.ScheduledRevision, want.ScheduledAt = content.StatusScheduled, rev, careersJST
	if got := decodeEntry(t, doc.Data); got != want {
		t.Errorf("the entry is %+v, want %+v", got, want)
	}
	if again, err := want.MarshalJSON(); err != nil || !bytes.Equal(again, doc.Data) {
		t.Errorf("the stored bytes are %s, MarshalJSON writes %s, %v", doc.Data, again, err)
	}
	sum := summaryOf(t, s, draftEN)
	if *sum.Entry != want || sum.Latest != rev || sum.Published != nil {
		t.Errorf("Summary() = %+v %+v, want the entry %+v scheduled with %s, which is not published", sum, sum.Entry, want, rev)
	}
	if got := entryPath(t, ed, s, draftEN); got != "" {
		t.Errorf("the page of the entry is %q, want none before it is published", got)
	}
	if got := revisionData(t, s, rev); string(got) != careersRevision {
		t.Errorf("the revision is %s, want it as it was", got)
	}
	sameSchedules(t, schedules(t, ed), []Scheduled{{draftEN, careersAt}})

	// Scheduling what is scheduled saves nothing: also with a time that has a fraction of a second, which is dropped.
	after, index := snapshot(t, s), indexOf(t, s)
	for _, at := range []time.Time{careersAt, careersAt.Add(999 * time.Millisecond), careersAt.In(jst)} {
		again, err := ed.Schedule(t.Context(), op, draftEN, doc.Seq, rev, at)
		if err != nil {
			t.Fatal(err)
		}
		if again.Version != res.Version || again.Publishes || again.Status != content.StatusScheduled || again.Revision != rev ||
			again.Kind != content.RefEntry || again.ID != draftEN || len(again.Warnings) != 0 {
			t.Errorf("Schedule() again at %s = %+v, want the same version, and that nothing is changed", at, again)
		}
		sameStore(t, s, after, index, "Schedule() again")
	}

	// Another time replaces the schedule.
	later := careersAt.Add(24 * time.Hour)
	res2 := must(t)(ed.Schedule(t.Context(), op, draftEN, doc.Seq, rev, later))
	if res2.Version.Seq <= doc.Seq || res2.Status != content.StatusScheduled || res2.Publishes {
		t.Errorf("Schedule() at another time = %+v, want a new version of the entry that is scheduled", res2)
	}
	if got := decodeEntry(t, read(t, s, content.RefEntry, draftEN).Data); got.ScheduledAt != "2026-11-08T20:00:00+09:00" || got.ScheduledRevision != rev {
		t.Errorf("the entry is %+v, want it scheduled at 2026-11-08T20:00:00+09:00", got)
	}
	sameSchedules(t, schedules(t, ed), []Scheduled{{draftEN, later}})
}

// TestScheduleAgain checks that scheduling what is scheduled saves nothing also when its time has come and the entry is
// still scheduled, and that any other time that has come is refused.
func TestScheduleAgain(t *testing.T) {
	ed, s := newSample(t)
	op := Op{By: userID}
	rev := scheduleCareers(t, ed, s)
	seq := entrySeq(t, s, draftEN)
	ed.SetClock(func() time.Time { return careersAt.Add(time.Hour) })
	before, index := snapshot(t, s), indexOf(t, s)

	res, err := ed.Schedule(t.Context(), op, draftEN, seq, rev, careersAt)
	if err != nil || res.Version.Seq != seq || res.Status != content.StatusScheduled || res.Publishes {
		t.Errorf("Schedule() = %+v, %v; want the same version of the entry that is scheduled", res, err)
	}
	sameStore(t, s, before, index, "Schedule()")

	_, err = ed.Schedule(t.Context(), op, draftEN, seq, rev, careersAt.Add(time.Minute))
	if err == nil || !strings.Contains(err.Error(), "/scheduledAt: 2026-11-07T20:01:00+09:00 is not after the time of the operation, 2026-11-07T21:00:00+09:00; publish it instead") {
		t.Errorf("Schedule() at another time that has come = %v, want the error for the time", err)
	}
	sameStore(t, s, before, index, "Schedule() at another time that has come")
}

// TestScheduleStatuses schedules entries that are not published, whatever the other status they have.
func TestScheduleStatuses(t *testing.T) {
	op := Op{By: userID}
	for _, tt := range []struct {
		name string
		to   func(t *testing.T, ed *Editor, s *store.Store)
	}{
		{"an archived entry", func(t *testing.T, ed *Editor, s *store.Store) {
			must(t)(ed.Archive(t.Context(), op, draftEN, entrySeq(t, s, draftEN)))
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			rev := saveRevision(t, ed, s, draftEN, careersRevision).Revision
			tt.to(t, ed, s)
			res, err := ed.Schedule(t.Context(), op, draftEN, entrySeq(t, s, draftEN), rev, careersAt)
			if err != nil || res.Status != content.StatusScheduled {
				t.Fatalf("Schedule() = %+v, %v; want the entry scheduled", res, err)
			}
			if got := summaryOf(t, s, draftEN).Entry; got.Status != content.StatusScheduled || got.ScheduledRevision != rev || got.ScheduledAt != careersJST || got.PublishedRevision != "" {
				t.Errorf("the entry is %+v, want it scheduled", got)
			}
		})
	}
}

// TestPublishScheduled publishes a scheduled entry when its time has come: not before, as the user who scheduled it, with
// the scheduled revision whatever the latest one is.
func TestPublishScheduled(t *testing.T) {
	ed, s := newSample(t)
	rev := scheduleCareers(t, ed, s)
	// Another user adds a newer revision, which the entry does not publish.
	newer := must(t)(ed.SaveRevision(t.Context(), Op{By: otherUser}, draftEN, rev,
		[]byte(`{"schemaVersion":1,"title":"Careers soon","slug":"careers-soon"}`))).Revision
	state := read(t, s, content.RefEntry, draftEN)
	if state.SavedBy != userID {
		t.Fatalf("the entry was saved by %s, want %s", state.SavedBy, userID)
	}
	before, index := snapshot(t, s), indexOf(t, s)

	name := docName(content.RefEntry, draftEN)
	for _, now := range []time.Time{testNow, careersAt.Add(-time.Second), careersAt.Add(-time.Nanosecond)} {
		res, err := ed.PublishScheduled(t.Context(), draftEN, now)
		want := fmt.Sprintf("%s: scheduled at %s, after %s", name, careersJST, now.In(jst).Format(time.RFC3339))
		if !errors.Is(err, ErrNotDue) || err == nil || err.Error() != want || res.Version != (store.Version{}) || res.Publishes {
			t.Errorf("PublishScheduled() at %s = %+v, %v; want the error %q wrapping ErrNotDue", now, res, err, want)
		}
		sameStore(t, s, before, index, "PublishScheduled() before the time")
	}

	res, err := ed.PublishScheduled(t.Context(), draftEN, careersAt)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != content.RefEntry || res.ID != draftEN || res.Revision != rev || res.Status != content.StatusPublished ||
		!res.Publishes || res.Redirect != "" || res.Number != 0 || len(res.Warnings) != 0 {
		t.Errorf("PublishScheduled() = %+v, want the entry %s published with the scheduled revision %s, which changes what is published", res, draftEN, rev)
	}
	doc := read(t, s, content.RefEntry, draftEN)
	if doc.Version != res.Version || doc.Seq <= state.Seq || doc.SavedBy != userID {
		t.Errorf("Document() = %+v, PublishScheduled() returned the version %+v after %d; want it saved by %s, who scheduled it, not by %s",
			doc.Version, res.Version, state.Seq, userID, otherUser)
	}
	want := decodeEntry(t, state.Data)
	want.Status, want.PublishedRevision, want.ScheduledRevision, want.ScheduledAt = content.StatusPublished, rev, "", ""
	if got := decodeEntry(t, doc.Data); got != want {
		t.Errorf("the entry is %+v, want %+v", got, want)
	}
	if again, err := want.MarshalJSON(); err != nil || !bytes.Equal(again, doc.Data) {
		t.Errorf("the stored bytes are %s, MarshalJSON writes %s, %v", doc.Data, again, err)
	}
	sum := summaryOf(t, s, draftEN)
	if *sum.Entry != want || sum.Latest != newer || sum.Published == nil || sum.Published.Slug != "careers" {
		t.Errorf("Summary() = %+v %+v, want the entry published with %s and the latest revision %s", sum, sum.Entry, rev, newer)
	}
	if got := entryPath(t, ed, s, draftEN); got != "/en/careers/" {
		t.Errorf("the page of the entry is %q, want /en/careers/", got)
	}
	if got := revisionIDs(t, s, draftEN); !slices.Equal(got, []string{rev, newer}) {
		t.Errorf("the revisions are %v, want %v: a page is published as it is, and the newer revision stays the latest", got, []string{rev, newer})
	}
	if got := schedules(t, ed); len(got) != 0 {
		t.Errorf("Schedules() = %+v after the publish, want none", got)
	}

	// The entry is published now: it is not scheduled, whatever the time.
	after, index := snapshot(t, s), indexOf(t, s)
	_, err = ed.PublishScheduled(t.Context(), draftEN, careersAt.Add(time.Hour))
	if want := name + ": not scheduled"; !errors.Is(err, ErrNotDue) || err == nil || err.Error() != want {
		t.Errorf("PublishScheduled() of a published entry = %v, want %q wrapping ErrNotDue", err, want)
	}
	sameStore(t, s, after, index, "PublishScheduled() of a published entry")
}

// TestPublishScheduledDue checks that an entry is due at its time in another time zone and after it. TestPublishScheduled
// checks the time itself and the times before it.
func TestPublishScheduledDue(t *testing.T) {
	for _, tt := range []struct {
		name string
		now  time.Time
	}{
		{"the time in another time zone", careersAt.In(time.FixedZone("west", -5*60*60))},
		{"after the time", careersAt.Add(time.Nanosecond)},
		{"long after the time", careersAt.Add(1000 * time.Hour)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			scheduleCareers(t, ed, s)
			res, err := ed.PublishScheduled(t.Context(), draftEN, tt.now)
			if err != nil || !res.Publishes {
				t.Errorf("PublishScheduled() = %+v, %v; want the entry published", res, err)
			}
		})
	}
}

// The revision of the posts that the tests of the dates schedule: it has no date.
const comingRevision = `{"schemaVersion":1,"title":"Coming","slug":"coming"}`

const day = 24 * time.Hour

// revisionUsers returns the user who saved each revision of the entry id, by the ID of the revision.
func revisionUsers(t *testing.T, s *store.Store, id string) map[string]string {
	t.Helper()
	users := make(map[string]string)
	err := s.View(t.Context(), func(tx *store.Tx) error {
		revs, err := tx.Revisions(id)
		for _, r := range revs {
			users[r.ID] = r.SavedBy
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return users
}

// wantRevision fails the test unless the revision id is the JSON that MarshalJSON writes for want.
func wantRevision(t *testing.T, s *store.Store, id string, want content.Revision) {
	t.Helper()
	data, err := want.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if got := revisionData(t, s, id); !bytes.Equal(got, data) {
		t.Errorf("the revision %s is %s, want %s", id, got, data)
	}
}

// wantRebuiltIndex fails the test unless the index of s is the one that RebuildIndex makes of its documents and revisions.
func wantRebuiltIndex(t *testing.T, s *store.Store) {
	t.Helper()
	got := indexOf(t, s)
	errRollBack := errors.New("roll back")
	err := s.Update(t.Context(), func(tx *store.Tx) error {
		if err := tx.RebuildIndex(); err != nil {
			return err
		}
		sameIndex(t, "after RebuildIndex", readIndex(t, tx), got)
		return errRollBack
	})
	if !errors.Is(err, errRollBack) {
		t.Fatalf("Update() = %v, want the error that rolls it back", err)
	}
}

// TestScheduleDate schedules posts: Schedule adds no revision, whatever the revision lacks, and the date of a post that has
// none goes into a new revision when the entry is published, with the scheduled time.
func TestScheduleDate(t *testing.T) {
	op := Op{By: userID}
	t.Run("a post without a date", func(t *testing.T) {
		ed, s := newSample(t)
		ids := predictIDs(t, content.RefEntry, content.RefTranslationGroup, content.RefRevision, content.RefRevision)
		entry := newDraft(t, ed, content.TypePost, "ja")
		const data = `{"schemaVersion":1,"title":"予告","slug":"coming"}`
		saved := must(t)(ed.SaveRevision(t.Context(), op, entry, "", []byte(data)))
		if saved.Revision != ids[2] {
			t.Fatalf("SaveRevision() made %s, want %s", saved.Revision, ids[2])
		}

		// 2026-12-01T00:30:00Z is 09:30 in Tokyo; the fraction of a second is dropped.
		at := time.Date(2026, 12, 1, 0, 30, 0, 700_000_000, time.UTC)
		const date = "2026-12-01T09:30:00+09:00"
		res, err := ed.Schedule(t.Context(), op, entry, entrySeq(t, s, entry), saved.Revision, at)
		if err != nil {
			t.Fatal(err)
		}
		if res.Revision != saved.Revision || res.Status != content.StatusScheduled || res.Publishes {
			t.Fatalf("Schedule() = %+v, want the revision %s scheduled as it was given", res, saved.Revision)
		}
		if got := revisionIDs(t, s, entry); !slices.Equal(got, []string{saved.Revision}) {
			t.Errorf("the revisions are %v, want only the one that was saved: Schedule adds none", got)
		}
		if got := revisionData(t, s, saved.Revision); string(got) != data {
			t.Errorf("the revision is %s, want it as it was", got)
		}
		sum := summaryOf(t, s, entry)
		if sum.Latest != saved.Revision || sum.Entry.ScheduledRevision != saved.Revision || sum.Entry.ScheduledAt != date || sum.Entry.Status != content.StatusScheduled {
			t.Errorf("Summary() = %+v %+v, want the saved revision as the latest and the scheduled one, at %s", sum, sum.Entry, date)
		}
		if got := entryPath(t, ed, s, entry); got != "" {
			t.Errorf("the page of the entry is %q, want none before it is published", got)
		}

		// Scheduling it again saves nothing, and adds no revision either.
		after, index := snapshot(t, s), indexOf(t, s)
		rescheduled := must(t)(ed.Schedule(t.Context(), op, entry, entrySeq(t, s, entry), saved.Revision, at))
		if rescheduled.Version != res.Version || rescheduled.Revision != saved.Revision || rescheduled.Publishes {
			t.Errorf("Schedule() again = %+v, want the result %+v, and that nothing is changed", rescheduled, res)
		}
		sameStore(t, s, after, index, "Schedule() again")

		// The time comes: the date goes into a new revision, which is published and is the latest revision now.
		pub := must(t)(ed.PublishScheduled(t.Context(), entry, at.Add(time.Hour)))
		if pub.Revision != ids[3] || pub.Status != content.StatusPublished || !pub.Publishes {
			t.Fatalf("PublishScheduled() = %+v, want the new revision %s published", pub, ids[3])
		}
		want := decodeRevision(t, []byte(data))
		want.Date = date
		if got := decodeRevision(t, revisionData(t, s, pub.Revision)); !reflect.DeepEqual(got, want) {
			t.Errorf("the new revision is %+v, want %+v", got, want)
		}
		wantRevision(t, s, pub.Revision, want)
		if got := revisionData(t, s, saved.Revision); string(got) != data {
			t.Errorf("the revision that was scheduled is %s, want it as it was", got)
		}
		if got := revisionIDs(t, s, entry); !slices.Equal(got, []string{saved.Revision, pub.Revision}) {
			t.Errorf("the revisions are %v, want the saved one and the new one", got)
		}
		sum = summaryOf(t, s, entry)
		if sum.Latest != pub.Revision || sum.Entry.PublishedRevision != pub.Revision || sum.Entry.Status != content.StatusPublished {
			t.Errorf("Summary() = %+v %+v, want the new revision as the latest and the published one", sum, sum.Entry)
		}
		if got := entryPath(t, ed, s, entry); got != "/2026/12/01/coming/" {
			t.Errorf("the page of the entry is %q, want /2026/12/01/coming/", got)
		}
		wantRebuiltIndex(t, s)
		// The caller's base is no longer the latest revision.
		_, err = ed.SaveRevision(t.Context(), op, entry, saved.Revision, []byte(data))
		if wantErr := docName(content.RefEntry, entry) + ": changed since it was read: its latest revision is " + pub.Revision +
			", not " + saved.Revision; !errors.Is(err, ErrConflict) || err.Error() != wantErr {
			t.Errorf("SaveRevision() with the old base = %v, want %q wrapping ErrConflict", err, wantErr)
		}
	})

	t.Run("a post with a date", func(t *testing.T) {
		ed, s := newSample(t)
		entry := newDraft(t, ed, content.TypePost, "ja")
		const data = `{"schemaVersion":1,"title":"先日の投稿","slug":"earlier","date":"2026-10-30T09:00:00+09:00"}`
		saved := must(t)(ed.SaveRevision(t.Context(), op, entry, "", []byte(data)))
		res, err := ed.Schedule(t.Context(), op, entry, entrySeq(t, s, entry), saved.Revision, careersAt)
		if err != nil || res.Revision != saved.Revision {
			t.Fatalf("Schedule() = %+v, %v; want the revision that was given", res, err)
		}
		if got := revisionIDs(t, s, entry); !slices.Equal(got, []string{saved.Revision}) {
			t.Errorf("the revisions are %v, want the one that was saved", got)
		}
		pub := must(t)(ed.PublishScheduled(t.Context(), entry, careersAt))
		if pub.Revision != saved.Revision {
			t.Errorf("PublishScheduled() = %+v, want the revision %s, which has a date", pub, saved.Revision)
		}
		if got := revisionIDs(t, s, entry); !slices.Equal(got, []string{saved.Revision}) {
			t.Errorf("the revisions are %v after the publish, want the one that was saved", got)
		}
		if got := entryPath(t, ed, s, entry); got != "/2026/10/30/earlier/" {
			t.Errorf("the page of the entry is %q, want the date of the revision, /2026/10/30/earlier/", got)
		}
	})

	t.Run("a page", func(t *testing.T) {
		ed, s := newSample(t)
		rev := scheduleCareers(t, ed, s)
		if got := revisionIDs(t, s, draftEN); !slices.Equal(got, []string{rev}) {
			t.Errorf("the revisions are %v, want no new revision for a page", got)
		}
		must(t)(ed.PublishScheduled(t.Context(), draftEN, careersAt))
		if got := revisionIDs(t, s, draftEN); !slices.Equal(got, []string{rev}) {
			t.Errorf("the revisions are %v after the publish, want no new revision for a page", got)
		}
	})

	// The date is in the time zone of the site, to the second: a site in UTC has the time as it is.
	t.Run("a site in UTC", func(t *testing.T) {
		s := newStore(t)
		ed := testEditor(t, s)
		settings := `{
  "schemaVersion": 1,
  "name": { "ja": "サイト" },
  "url": "https://example.com",
  "timeZone": "UTC",
  "languages": ["ja"],
  "defaultLanguage": "ja"
}`
		must(t)(ed.SaveSettings(t.Context(), op, 0, []byte(settings)))
		entry := newDraft(t, ed, content.TypePost, "ja")
		saved := must(t)(ed.SaveRevision(t.Context(), op, entry, "", []byte(`{"schemaVersion":1,"title":"a","slug":"a"}`)))
		res := must(t)(ed.Schedule(t.Context(), op, entry, entrySeq(t, s, entry), saved.Revision, careersAt.In(jst)))
		if res.Revision != saved.Revision {
			t.Errorf("Schedule() = %+v, want the revision that was given", res)
		}
		if got := summaryOf(t, s, entry).Entry.ScheduledAt; got != "2026-11-07T11:00:00Z" {
			t.Errorf("scheduledAt is %q, want 2026-11-07T11:00:00Z", got)
		}
		pub := must(t)(ed.PublishScheduled(t.Context(), entry, careersAt.Add(time.Hour)))
		if got := decodeRevision(t, revisionData(t, s, pub.Revision)).Date; got != "2026-11-07T11:00:00Z" {
			t.Errorf("the date is %q, want 2026-11-07T11:00:00Z", got)
		}
	})

	// A schedule that the store's own API took in another time zone than the site's: the date is in the site's.
	t.Run("a scheduled time in another time zone", func(t *testing.T) {
		ed, s := newSample(t)
		entry, rev := testID(content.RefEntry, 1), testID(content.RefRevision, 1)
		put(t, s, content.RefEntry, entry, scheduledJSON(t, []byte(storedEntry(entry, maxNumber(t, s)+1, content.TypePost, "ja",
			testID(content.RefTranslationGroup, 1), content.StatusDraft, "")), rev, "2026-11-10T00:00:00Z"))
		addStoredRevision(t, s, entry, rev, `{"schemaVersion":1,"title":"A","slug":"a"}`)
		due := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
		res, err := ed.PublishScheduled(t.Context(), entry, due.Add(time.Hour))
		if err != nil || res.Revision == rev || !res.Publishes {
			t.Fatalf("PublishScheduled() = %+v, %v; want a new revision published", res, err)
		}
		if got := decodeRevision(t, revisionData(t, s, res.Revision)).Date; got != "2026-11-10T09:00:00+09:00" {
			t.Errorf("the date is %q, want the scheduled time in the time zone of the site, 2026-11-10T09:00:00+09:00", got)
		}
		if got := entryPath(t, ed, s, entry); got != "/2026/11/10/a/" {
			t.Errorf("the page of the entry is %q, want /2026/11/10/a/", got)
		}
	})
}

// TestScheduledPostPublishedAtOnce checks that a post without a date that is scheduled and then published at once is dated
// with the time of the publish, not with the time that it was scheduled for.
func TestScheduledPostPublishedAtOnce(t *testing.T) {
	ed, s := newSample(t)
	op := Op{By: userID}
	entry := newDraft(t, ed, content.TypePost, "ja")
	saved := must(t)(ed.SaveRevision(t.Context(), op, entry, "", []byte(comingRevision)))
	sch := must(t)(ed.Schedule(t.Context(), op, entry, entrySeq(t, s, entry), saved.Revision, testNow.Add(7*day)))
	pub := must(t)(ed.Publish(t.Context(), op, entry, entrySeq(t, s, entry), sch.Revision))
	if pub.Revision == saved.Revision || pub.Status != content.StatusPublished || !pub.Publishes {
		t.Fatalf("Publish() = %+v, want a new revision with the date published", pub)
	}
	if got := decodeRevision(t, revisionData(t, s, pub.Revision)).Date; got != testNowJST {
		t.Errorf("the date is %q, want the time of the publish, %s", got, testNowJST)
	}
	if got := entryPath(t, ed, s, entry); got != "/2026/11/07/coming/" {
		t.Errorf("the page of the entry is %q, want the date of the publish, /2026/11/07/coming/", got)
	}
	if got := revisionIDs(t, s, entry); !slices.Equal(got, []string{saved.Revision, pub.Revision}) {
		t.Errorf("the revisions are %v, want the saved one and the new one", got)
	}
	if got := schedules(t, ed); len(got) != 0 {
		t.Errorf("Schedules() = %+v, want none", got)
	}
	if _, err := ed.PublishScheduled(t.Context(), entry, testNow.Add(8*day)); !errors.Is(err, ErrNotDue) {
		t.Errorf("PublishScheduled() of the entry = %v, want ErrNotDue", err)
	}
}

// TestScheduledPostScheduledAgain checks that a post without a date that is scheduled for another time is dated with the
// second time, not the first.
func TestScheduledPostScheduledAgain(t *testing.T) {
	ed, s := newSample(t)
	op := Op{By: userID}
	entry := newDraft(t, ed, content.TypePost, "ja")
	saved := must(t)(ed.SaveRevision(t.Context(), op, entry, "", []byte(comingRevision)))
	first, later := testNow.Add(day), testNow.Add(30*day)
	one := must(t)(ed.Schedule(t.Context(), op, entry, entrySeq(t, s, entry), saved.Revision, first))
	two := must(t)(ed.Schedule(t.Context(), op, entry, entrySeq(t, s, entry), one.Revision, later))
	if one.Revision != saved.Revision || two.Revision != saved.Revision {
		t.Errorf("Schedule() scheduled %s and %s, want the revision %s both times: it adds none", one.Revision, two.Revision, saved.Revision)
	}
	if _, err := ed.PublishScheduled(t.Context(), entry, first.Add(time.Hour)); !errors.Is(err, ErrNotDue) {
		t.Errorf("PublishScheduled() at the first time = %v, want ErrNotDue: it is scheduled for the second", err)
	}
	pub := must(t)(ed.PublishScheduled(t.Context(), entry, later.Add(time.Hour)))
	if got := decodeRevision(t, revisionData(t, s, pub.Revision)).Date; got != "2026-12-07T19:00:00+09:00" {
		t.Errorf("the date is %q, want the second time, 2026-12-07T19:00:00+09:00", got)
	}
	if got := entryPath(t, ed, s, entry); got != "/2026/12/07/coming/" {
		t.Errorf("the page of the entry is %q, want /2026/12/07/coming/", got)
	}
	if got := revisionIDs(t, s, entry); !slices.Equal(got, []string{saved.Revision, pub.Revision}) {
		t.Errorf("the revisions are %v, want the saved one and the new one", got)
	}
}

// TestScheduledPostCancelledThenPublished checks that a post without a date whose schedule is cancelled and which is
// published a month later is dated with the time of that publish.
func TestScheduledPostCancelledThenPublished(t *testing.T) {
	ed, s := newSample(t)
	op := Op{By: userID}
	entry := newDraft(t, ed, content.TypePost, "ja")
	saved := must(t)(ed.SaveRevision(t.Context(), op, entry, "", []byte(comingRevision)))
	sch := must(t)(ed.Schedule(t.Context(), op, entry, entrySeq(t, s, entry), saved.Revision, testNow.Add(day)))
	must(t)(ed.Unpublish(t.Context(), op, entry, entrySeq(t, s, entry)))
	now := testNow.Add(40 * day)
	ed.SetClock(func() time.Time { return now })
	pub := must(t)(ed.Publish(t.Context(), op, entry, entrySeq(t, s, entry), sch.Revision))
	if got := decodeRevision(t, revisionData(t, s, pub.Revision)).Date; got != "2026-12-17T19:00:00+09:00" {
		t.Errorf("the date is %q, want the time of the publish, 2026-12-17T19:00:00+09:00", got)
	}
	if got := entryPath(t, ed, s, entry); got != "/2026/12/17/coming/" {
		t.Errorf("the page of the entry is %q, want the date of the publish, /2026/12/17/coming/", got)
	}
	if got := revisionIDs(t, s, entry); !slices.Equal(got, []string{saved.Revision, pub.Revision}) {
		t.Errorf("the revisions are %v, want the saved one and the new one", got)
	}
}

// TestScheduledPublishCopiesLatest checks the revisions that a scheduled publish adds when the editor has saved revisions
// since the schedule: after the dated copy of the scheduled revision, a copy of the latest one, so that the draft stays the
// latest revision.
func TestScheduledPublishCopiesLatest(t *testing.T) {
	const (
		scheduler = "usr_01JB8T7H4MA2C5E8G1J4K7N3P9" // who schedules, as the users who save the revisions are others
		first     = comingRevision
		second    = `{"schemaVersion":1,"title":"Coming soon","slug":"coming-soon"}`
		withDate  = `{
  "schemaVersion": 1,
  "title": "Coming later",
  "slug": "coming-later",
  "date": "2026-10-30T09:00:00+09:00"
}`
		date = "2026-11-08T19:00:00+09:00"
	)
	// The scheduler calls PublishScheduled some time after the time of the schedule.
	at := testNow.Add(day)
	now := at.Add(90 * time.Minute)

	// setup saves the revision initial for a new post as userID, schedules it at at as scheduler, and saves the revisions
	// later as otherUser, one after the other. It returns the entry and the IDs of the revisions that it saved.
	setup := func(t *testing.T, initial string, later ...string) (*Editor, *store.Store, string, []string) {
		t.Helper()
		ed, s := newSample(t)
		entry := newDraft(t, ed, content.TypePost, "ja")
		revs := []string{must(t)(ed.SaveRevision(t.Context(), Op{By: userID}, entry, "", []byte(initial))).Revision}
		must(t)(ed.Schedule(t.Context(), Op{By: scheduler}, entry, entrySeq(t, s, entry), revs[0], at))
		for _, data := range later {
			revs = append(revs, must(t)(ed.SaveRevision(t.Context(), Op{By: otherUser}, entry, revs[len(revs)-1], []byte(data))).Revision)
		}
		return ed, s, entry, revs
	}

	t.Run("a later revision without a date", func(t *testing.T) {
		ed, s, entry, revs := setup(t, first, second)
		pub := must(t)(ed.PublishScheduled(t.Context(), entry, now))
		ids := revisionIDs(t, s, entry)
		if len(ids) != 4 || !slices.Equal(ids[:2], revs) || ids[2] != pub.Revision {
			t.Fatalf("the revisions are %v, want %v, the published revision %s, and a copy of the latest", ids, revs, pub.Revision)
		}
		copied := ids[3]
		if pub.Status != content.StatusPublished || !pub.Publishes {
			t.Errorf("PublishScheduled() = %+v, want the dated revision published", pub)
		}

		// The published revision is the scheduled one with the date, and the copy of the latest has the same date.
		one := decodeRevision(t, []byte(first))
		one.Date = date
		wantRevision(t, s, pub.Revision, one)
		two := decodeRevision(t, []byte(second))
		two.Date = date
		wantRevision(t, s, copied, two)
		if string(revisionData(t, s, revs[0])) != first || string(revisionData(t, s, revs[1])) != second {
			t.Errorf("the revisions that were saved are %s and %s, want them as they were", revisionData(t, s, revs[0]), revisionData(t, s, revs[1]))
		}

		sum := summaryOf(t, s, entry)
		if sum.Latest != copied || sum.Entry.PublishedRevision != pub.Revision || sum.Entry.Status != content.StatusPublished {
			t.Errorf("Summary() = %+v %+v, want the copy as the latest revision and the dated one as the published one", sum, sum.Entry)
		}
		if got := entryPath(t, ed, s, entry); got != "/2026/11/08/coming/" {
			t.Errorf("the page of the entry is %q, want /2026/11/08/coming/", got)
		}
		wantRebuiltIndex(t, s)

		// Both revisions that it adds are saved as the user who scheduled the entry.
		want := map[string]string{revs[0]: userID, revs[1]: otherUser, pub.Revision: scheduler, copied: scheduler}
		if users := revisionUsers(t, s, entry); !reflect.DeepEqual(users, want) {
			t.Errorf("the revisions were saved by %v, want %v", users, want)
		}

		// The editor goes on from the copy: the revision that it had read is not the latest any more.
		_, err := ed.SaveRevision(t.Context(), Op{By: otherUser}, entry, revs[1], []byte(second))
		if wantErr := docName(content.RefEntry, entry) + ": changed since it was read: its latest revision is " + copied +
			", not " + revs[1]; !errors.Is(err, ErrConflict) || err.Error() != wantErr {
			t.Errorf("SaveRevision() with the base %s = %v, want %q wrapping ErrConflict", revs[1], err, wantErr)
		}

		// Publishing the copy keeps the date that it has: the post is not dated again.
		again := must(t)(ed.Publish(t.Context(), Op{By: otherUser}, entry, entrySeq(t, s, entry), copied))
		if again.Revision != copied || len(revisionIDs(t, s, entry)) != 4 {
			t.Errorf("Publish() = %+v with the revisions %v, want the copy published as it is", again, revisionIDs(t, s, entry))
		}
		if got := entryPath(t, ed, s, entry); got != "/2026/11/08/coming-soon/" {
			t.Errorf("the page of the entry is %q, want the date of the schedule, /2026/11/08/coming-soon/", got)
		}
	})

	t.Run("a later revision with a date", func(t *testing.T) {
		ed, s, entry, revs := setup(t, first, withDate)
		pub := must(t)(ed.PublishScheduled(t.Context(), entry, now))
		ids := revisionIDs(t, s, entry)
		if len(ids) != 4 || !slices.Equal(ids[:2], revs) || ids[2] != pub.Revision {
			t.Fatalf("the revisions are %v, want %v, the published revision %s, and a copy of the latest", ids, revs, pub.Revision)
		}
		// The copy is the bytes of the latest revision, as the editor wrote them.
		if got := string(revisionData(t, s, ids[3])); got != withDate {
			t.Errorf("the copy is %s, want the bytes of the latest revision, %s", got, withDate)
		}
		one := decodeRevision(t, []byte(first))
		one.Date = date
		wantRevision(t, s, pub.Revision, one)
		if sum := summaryOf(t, s, entry); sum.Latest != ids[3] {
			t.Errorf("the latest revision is %s, want the copy %s", sum.Latest, ids[3])
		}
		wantRebuiltIndex(t, s)
	})

	t.Run("several later revisions", func(t *testing.T) {
		ed, s, entry, revs := setup(t, first, second, withDate)
		pub := must(t)(ed.PublishScheduled(t.Context(), entry, now))
		ids := revisionIDs(t, s, entry)
		if len(ids) != 5 || !slices.Equal(ids[:3], revs) || ids[3] != pub.Revision {
			t.Fatalf("the revisions are %v, want %v, the published revision %s, and a copy of the latest", ids, revs, pub.Revision)
		}
		if got := string(revisionData(t, s, ids[4])); got != withDate {
			t.Errorf("the copy is %s, want the bytes of the latest revision, %s", got, withDate)
		}
	})

	t.Run("a scheduled revision with a date", func(t *testing.T) {
		ed, s, entry, revs := setup(t, withDate, second)
		pub := must(t)(ed.PublishScheduled(t.Context(), entry, now))
		if got := revisionIDs(t, s, entry); pub.Revision != revs[0] || !slices.Equal(got, revs) {
			t.Errorf("PublishScheduled() = %+v with the revisions %v, want %v and the first published as it is", pub, got, revs)
		}
		if sum := summaryOf(t, s, entry); sum.Latest != revs[1] {
			t.Errorf("the latest revision is %s, want %s", sum.Latest, revs[1])
		}
	})

	// The revision that a scheduled entry names may be one of another entry, which leaves it without a revision of its own.
	t.Run("an entry that has no revision of its own", func(t *testing.T) {
		ed, s := newSample(t)
		owner := newDraft(t, ed, content.TypePost, "ja")
		rev := must(t)(ed.SaveRevision(t.Context(), Op{By: userID}, owner, "", []byte(first))).Revision
		entry := testID(content.RefEntry, 1)
		put(t, s, content.RefEntry, entry, scheduledJSON(t, []byte(storedEntry(entry, maxNumber(t, s)+1, content.TypePost, "ja",
			testID(content.RefTranslationGroup, 1), content.StatusDraft, "")), rev, date))
		pub := must(t)(ed.PublishScheduled(t.Context(), entry, now))
		if got := revisionIDs(t, s, entry); !slices.Equal(got, []string{pub.Revision}) {
			t.Errorf("the revisions are %v, want only the dated copy %s", got, pub.Revision)
		}
		if got := revisionIDs(t, s, owner); !slices.Equal(got, []string{rev}) {
			t.Errorf("the revisions of the entry that has the revision are %v, want %s", got, rev)
		}
	})

	// The revisions and the state are saved together, or not at all: a copy that cannot be saved fails the publish.
	t.Run("a copy that cannot be saved", func(t *testing.T) {
		ed, s, entry, _ := setup(t, first, second)
		// The ID of the copy is the sixth that the Editor makes: the entry, its group, the two revisions that were saved, the
		// dated revision, and then the copy. A revision of another entry has it.
		ids := predictIDs(t, content.RefEntry, content.RefTranslationGroup, content.RefRevision, content.RefRevision, content.RefRevision, content.RefRevision)
		addStoredRevision(t, s, draftEN, ids[5], careersRevision)
		before, index := snapshot(t, s), indexOf(t, s)
		res, err := ed.PublishScheduled(t.Context(), entry, now)
		if !errors.Is(err, fs.ErrExist) || err == nil || err.Error() != revisionName(ids[5])+": already exists" {
			t.Errorf("PublishScheduled() = %+v, %v; want the error that %s exists", res, err, revisionName(ids[5]))
		}
		sameStore(t, s, before, index, "PublishScheduled()")
	})

	// A publish that fails adds no revision, neither the dated one nor the copy.
	t.Run("a publish that fails", func(t *testing.T) {
		ed, s, entry, _ := setup(t, first, second)
		// Another post takes the slug of the scheduled revision before its time comes.
		other := newDraft(t, ed, content.TypePost, "ja")
		rev := must(t)(ed.SaveRevision(t.Context(), Op{By: userID}, other, "",
			[]byte(`{"schemaVersion":1,"title":"Other","slug":"coming","date":"2026-10-30T09:00:00+09:00"}`))).Revision
		must(t)(ed.Publish(t.Context(), Op{By: userID}, other, entrySeq(t, s, other), rev))
		before, index := snapshot(t, s), indexOf(t, s)
		_, err := ed.PublishScheduled(t.Context(), entry, now)
		if got := problemLines(t, err); len(got) != 1 || !strings.Contains(got[0], `/slug: slug "coming" is also used by `) {
			t.Errorf("PublishScheduled() = %v, want the problem of the slug", err)
		}
		sameStore(t, s, before, index, "PublishScheduled()")
	})
}

// TestScheduleErrors checks what Schedule refuses: nothing is saved, and the problems are located in the bytes of the
// entry and of the revision.
func TestScheduleErrors(t *testing.T) {
	const missing = "ent_01JBZ0000000000000000000E9"
	op := Op{By: userID}
	entryName := docName(content.RefEntry, draftEN)
	first := predictIDs(t, content.RefRevision)[0]
	revName := revisionName(first)
	feed := `{"schemaVersion":1,"title":"Feed","slug":"feed"}`
	about := `{"schemaVersion":1,"title":"About","slug":"about"}`
	noTitle := `{"schemaVersion":1,"slug":"x"}`
	notAfter := func(at string) func(t *testing.T, s *store.Store) string {
		return func(t *testing.T, s *store.Store) string {
			state := scheduledJSON(t, read(t, s, content.RefEntry, draftEN).Data, first, at)
			return where(t, entryName, state, `"scheduledAt"`) + "/scheduledAt: " + at +
				" is not after the time of the operation, " + testNowJST + "; publish it instead"
		}
	}
	text := func(msg string) func(*testing.T, *store.Store) string {
		return func(*testing.T, *store.Store) string { return msg }
	}
	schedule := func(rev string, at time.Time) func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
		return func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Schedule(t.Context(), op, draftEN, entrySeq(t, s, draftEN), rev, at)
		}
	}
	saveFirst := func(data string) func(t *testing.T, ed *Editor, s *store.Store) {
		return func(t *testing.T, ed *Editor, s *store.Store) { saveRevision(t, ed, s, draftEN, data) }
	}

	for _, tt := range []struct {
		name  string
		setup func(t *testing.T, ed *Editor, s *store.Store) // before the store is looked at
		call  func(t *testing.T, ed *Editor, s *store.Store) (Result, error)
		want  func(t *testing.T, s *store.Store) string
		is    error
	}{
		{"a published entry", nil, func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Schedule(t.Context(), op, aboutEN, entrySeq(t, s, aboutEN), aboutENRev, careersAt)
		}, func(t *testing.T, s *store.Store) string {
			return where(t, docName(content.RefEntry, aboutEN), string(read(t, s, content.RefEntry, aboutEN).Data), `"status"`) +
				"/status: a published entry cannot be scheduled; unpublish it first"
		}, nil},

		{"a time that is the time of the operation", saveFirst(careersRevision), schedule(first, testNow),
			notAfter("2026-11-07T19:00:00+09:00"), nil},
		{"a time before the time of the operation", saveFirst(careersRevision), schedule(first, testNow.Add(-time.Hour)),
			notAfter("2026-11-07T18:00:00+09:00"), nil},
		{"a time less than a second after it", saveFirst(careersRevision), schedule(first, testNow.Add(999*time.Millisecond)),
			notAfter("2026-11-07T19:00:00+09:00"), nil},
		{"a time that is not set", saveFirst(careersRevision), schedule(first, time.Time{}),
			func(t *testing.T, s *store.Store) string {
				tokyo, err := time.LoadLocation("Asia/Tokyo")
				if err != nil {
					t.Fatal(err)
				}
				return notAfter(time.Time{}.In(tokyo).Format(time.RFC3339))(t, s)
			}, nil},

		{"an entry without revisions", nil, schedule(testID(content.RefRevision, 9), careersAt),
			text(entryName + ": has no revision to schedule"), nil},

		{"an entry that does not exist", nil, func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Schedule(t.Context(), op, missing, 1, testID(content.RefRevision, 9), careersAt)
		}, text("entries/" + missing + ".json: not found"), fs.ErrNotExist},

		{"a stale base", saveFirst(careersRevision), func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Schedule(t.Context(), op, draftEN, entrySeq(t, s, draftEN)+1, first, careersAt)
		}, func(t *testing.T, s *store.Store) string {
			seq := entrySeq(t, s, draftEN)
			return fmt.Sprintf("%s: changed since it was read: it is at version %d, not %d", entryName, seq, seq+1)
		}, ErrConflict},

		{"a revision that is not the latest", func(t *testing.T, ed *Editor, s *store.Store) {
			saveRevision(t, ed, s, draftEN, careersRevision)
			saveRevision(t, ed, s, draftEN, careersRevision)
		}, schedule(first, careersAt), func(t *testing.T, s *store.Store) string {
			return entryName + ": changed since it was read: its latest revision is " + summaryOf(t, s, draftEN).Latest + ", not " + first
		}, ErrConflict},

		{"a missing title", saveFirst(noTitle), schedule(first, careersAt), text(revName + ":1:1: /title: missing title"), nil},

		{"a slug that is the path of a feed", saveFirst(feed), schedule(first, careersAt), func(t *testing.T, s *store.Store) string {
			return where(t, revName, feed, `"slug"`) + `/slug: output path "/en/feed/" is also that of the feed in en`
		}, nil},

		{"a slug that another page has", saveFirst(about), schedule(first, careersAt), func(t *testing.T, s *store.Store) string {
			return where(t, revName, about, `"slug"`) + `/slug: slug "about" is also used by ` + revisionName(aboutENRev)
		}, nil},

		{"a state that is not valid", func(t *testing.T, ed *Editor, s *store.Store) {
			entry := testID(content.RefEntry, 1)
			put(t, s, content.RefEntry, entry, storedEntry(entry, maxNumber(t, s)+1, content.TypePage, "xx", testID(content.RefTranslationGroup, 1),
				content.StatusDraft, ""))
			addStoredRevision(t, s, entry, first, careersRevision)
		}, func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			entry := testID(content.RefEntry, 1)
			return ed.Schedule(t.Context(), op, entry, entrySeq(t, s, entry), first, careersAt)
		}, func(t *testing.T, s *store.Store) string {
			entry := testID(content.RefEntry, 1)
			state := scheduledJSON(t, read(t, s, content.RefEntry, entry).Data, first, careersJST)
			return where(t, docName(content.RefEntry, entry), state, `"language"`) +
				`/language: language "xx" is not in the site settings (use ja, en)`
		}, nil},

		{"a malformed entry ID", nil, func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Schedule(t.Context(), op, "ent_x", 1, first, careersAt)
		}, text(`invalid entry ID "ent_x": use "ent_" and a ULID (26 characters in upper case)`), nil},

		{"a malformed revision ID", nil, func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Schedule(t.Context(), op, draftEN, 1, "rev_x", careersAt)
		}, text(`invalid revision ID "rev_x": use "rev_" and a ULID (26 characters in upper case)`), nil},

		{"a malformed user ID", nil, func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Schedule(t.Context(), Op{By: "x"}, draftEN, 1, first, careersAt)
		}, text(`invalid user ID "x": use "usr_" and a ULID (26 characters in upper case)`), nil},

		{"a malformed operation ID", nil, func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Schedule(t.Context(), Op{By: userID, ID: "op_x"}, draftEN, 1, first, careersAt)
		}, text(`invalid operation ID "op_x": use "op_" and a ULID (26 characters in upper case)`), nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			if tt.setup != nil {
				tt.setup(t, ed, s)
			}
			before, index := snapshot(t, s), indexOf(t, s)
			res, err := tt.call(t, ed, s)
			want := tt.want(t, s)
			if err == nil || tt.is != nil && !errors.Is(err, tt.is) || err.Error() != want {
				t.Errorf("Schedule() = %+v, %v\nwant the error %q wrapping %v", res, err, want, tt.is)
			}
			if res.Version != (store.Version{}) || res.Publishes || res.Status != "" {
				t.Errorf("Schedule() returned the result %+v with its error", res)
			}
			t.Logf("the error: %v", err)
			sameStore(t, s, before, index, "Schedule()")
		})
	}
}

// datedText returns data, the JSON of a revision in the form that the tests write, an object that ends in a line with "}",
// with date as its last item, so that every other item keeps its place.
func datedText(t *testing.T, data, date string) string {
	t.Helper()
	i := strings.LastIndex(data, "\n}")
	if i < 0 {
		t.Fatalf("%q does not end in a line with }", data)
	}
	return data[:i] + ",\n  \"date\": \"" + date + "\"" + data[i:]
}

// TestScheduleParity compares what Schedule refuses with what the build refuses in a copy of the sample site in which the
// entry is published with the revision: the same lines, with the files, the pointers, the messages and the positions. A post
// without a date is published with the date of the schedule, which Schedule only has in memory: the build reads the bytes
// that the store has with the date added at their end.
func TestScheduleParity(t *testing.T) {
	op := Op{By: userID}
	for _, tt := range []struct {
		name  string
		typ   string // of a new draft entry in en that gets the revision; "" for the draft entry draftEN of the sample site
		data  string
		msg   string
		setup func(t *testing.T, p *parity) // after the revision is saved, for the store and the copy
	}{
		{"a slug that is the path of a feed", "", `{
  "schemaVersion": 1,
  "title": "Feed",
  "slug": "feed"
}`, `/slug: output path "/en/feed/" is also that of the feed in en`, nil},
		{"a slug that another page has", "", `{
  "schemaVersion": 1,
  "title": "About",
  "slug": "about"
}`, `/slug: slug "about" is also used by ` + revisionName(aboutENRev), nil},
		{"a missing title", "", `{
  "schemaVersion": 1,
  "slug": "untitled"
}`, `/title: missing title`, nil},
		{"a parent that is not published", content.TypePage, `{
  "schemaVersion": 1,
  "title": "Team",
  "slug": "team",
  "parent": "` + draftEN + `"
}`, `/parent: entry "` + draftEN + `" is not published`, nil},
		{"a post without a date", content.TypePost, `{
  "schemaVersion": 1,
  "title": "New service again",
  "slug": "new-service"
}`, `/slug: slug "new-service" is also used by ` + revisionName("rev_01JC0000000000000000000001"), nil},
		// The path of the post has the date of the schedule: a redirect from it is the problem.
		{"a redirect from the dated path of a post without a date", content.TypePost, `{
  "schemaVersion": 1,
  "title": "Dated",
  "slug": "dated"
}`, `/from: from "/en/2026/11/07/dated/" is the output path "/en/2026/11/07/dated/" of revisions/`, func(t *testing.T, p *parity) {
			made := must(t)(p.ed.Create(t.Context(), op, content.RefRedirect, []byte(redirectJSON("/en/2026/11/07/dated/", "/"))))
			p.copyStored(content.RefRedirect, made.ID)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := newParity(t)
			entry := draftEN
			if tt.typ != "" {
				entry = newDraft(t, p.ed, tt.typ, "en")
			}
			saved := saveRevision(t, p.ed, p.s, entry, tt.data)
			data := tt.data
			if tt.typ == content.TypePost {
				data = datedText(t, tt.data, careersJST)
			}
			if tt.setup != nil {
				tt.setup(t, p)
			}
			p.write(revisionName(saved.Revision), data)
			p.write(docName(content.RefEntry, entry), published(t, read(t, p.s, content.RefEntry, entry).Data, saved.Revision))
			p.base = snapshot(t, p.s)
			want := buildErrors(t, p.dir)
			res, err := p.ed.Schedule(t.Context(), op, entry, p.seq(content.RefEntry, entry), saved.Revision, careersAt)
			got := problemLines(t, err)
			if len(want) == 0 || !slices.Equal(got, want) || !strings.Contains(strings.Join(got, "\n"), tt.msg) {
				t.Errorf("the problems of Schedule() (%+v):\n%s\nthe problems of the build:\n%s", res, strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			if snap := snapshot(t, p.s); snap != p.base {
				t.Errorf("Schedule() has changed the store:\n%s\nwant\n%s", snap, p.base)
			}
			t.Logf("the problems:\n%s", strings.Join(got, "\n"))
		})
	}

	// What Schedule accepts and PublishScheduled publishes, the build accepts.
	t.Run("what the build accepts", func(t *testing.T) {
		p := newParity(t)
		rev := scheduleCareers(t, p.ed, p.s)
		res := must(t)(p.ed.PublishScheduled(t.Context(), draftEN, careersAt))
		p.copyStored(content.RefEntry, draftEN)
		p.write(revisionName(rev), careersRevision)
		if got := buildErrors(t, p.dir); len(got) != 0 {
			t.Errorf("the build refuses what PublishScheduled() has published (%+v):\n%s", res, strings.Join(got, "\n"))
		}
	})

	t.Run("what the build accepts of a post without a date", func(t *testing.T) {
		p := newParity(t)
		entry := newDraft(t, p.ed, content.TypePost, "en")
		saved := must(t)(p.ed.SaveRevision(t.Context(), Op{By: userID}, entry, "", []byte(`{"schemaVersion":1,"title":"Fresh","slug":"fresh"}`)))
		must(t)(p.ed.Schedule(t.Context(), Op{By: userID}, entry, p.seq(content.RefEntry, entry), saved.Revision, careersAt))
		res := must(t)(p.ed.PublishScheduled(t.Context(), entry, careersAt))
		p.copyStored(content.RefEntry, entry)
		p.write(revisionName(res.Revision), string(revisionData(t, p.s, res.Revision)))
		if got := buildErrors(t, p.dir); len(got) != 0 {
			t.Errorf("the build refuses what PublishScheduled() has published (%+v):\n%s", res, strings.Join(got, "\n"))
		}
	})
}

// TestScheduledProtected checks that a scheduled revision is protected as a published one is: what it uses cannot be
// deleted, and a change of what it refers to that makes it invalid is refused, where a draft would only have a warning.
func TestScheduledProtected(t *testing.T) {
	op := Op{By: userID}

	t.Run("a medium that the scheduled revision uses", func(t *testing.T) {
		image := func(medium string) string {
			return `{"schemaVersion":1,"title":"Careers","slug":"careers","body":[{"type":"image","id":"b_00000001","attrs":{"media":"` +
				medium + `","alt":"Soon"}}]}`
		}

		// A draft only has a warning.
		ed, s := newSample(t)
		added := must(t)(ed.AddMedia(t.Context(), op, bytes.NewReader(testPNG(t, 6, 5, 31)), "soon.png", ""))
		rev := saveRevision(t, ed, s, draftEN, image(added.ID)).Revision
		res, err := ed.Delete(t.Context(), op, content.RefMedia, added.ID, added.Version.Seq)
		if err != nil {
			t.Fatalf("Delete() of a medium that a draft uses = %v", err)
		}
		if want := docName(content.RefMedia, added.ID) + ": deleted although used by " + revisionName(rev) + ", the latest revision of " +
			docName(content.RefEntry, draftEN) + ", which is not published"; !slices.Equal(lines(res.Warnings), []string{want}) {
			t.Errorf("Delete() warnings:\n%s\nwant\n%s", strings.Join(lines(res.Warnings), "\n"), want)
		}

		// A scheduled revision is refused, until the schedule is cancelled.
		ed, s = newSample(t)
		added = must(t)(ed.AddMedia(t.Context(), op, bytes.NewReader(testPNG(t, 6, 5, 31)), "soon.png", ""))
		rev = saveRevision(t, ed, s, draftEN, image(added.ID)).Revision
		scheduled := must(t)(ed.Schedule(t.Context(), op, draftEN, entrySeq(t, s, draftEN), rev, careersAt))
		if len(scheduled.Warnings) != 0 {
			t.Fatalf("Schedule() warnings: %v", scheduled.Warnings)
		}
		before, index := snapshot(t, s), indexOf(t, s)
		_, err = ed.Delete(t.Context(), op, content.RefMedia, added.ID, added.Version.Seq)
		if want := docName(content.RefMedia, added.ID) + ": cannot delete: used by " + revisionName(rev); err == nil || err.Error() != want {
			t.Errorf("Delete() of a medium that the scheduled revision uses = %v, want %q", err, want)
		}
		sameStore(t, s, before, index, "Delete()")

		must(t)(ed.Unpublish(t.Context(), op, draftEN, entrySeq(t, s, draftEN)))
		res, err = ed.Delete(t.Context(), op, content.RefMedia, added.ID, added.Version.Seq)
		if err != nil || len(res.Warnings) != 1 || !strings.HasSuffix(res.Warnings[0].Msg, ", which is not published") {
			t.Errorf("Delete() after the schedule is cancelled = %+v, %v; want the medium deleted with a warning for the draft", res, err)
		}
	})

	// A revision that is newer than the scheduled one is a draft.
	t.Run("a medium that a newer revision uses", func(t *testing.T) {
		ed, s := newSample(t)
		rev := scheduleCareers(t, ed, s)
		added := must(t)(ed.AddMedia(t.Context(), op, bytes.NewReader(testPNG(t, 6, 5, 32)), "newer.png", ""))
		newer := must(t)(ed.SaveRevision(t.Context(), op, draftEN, rev, []byte(`{"schemaVersion":1,"title":"Careers","slug":"careers","body":[`+
			`{"type":"image","id":"b_00000001","attrs":{"media":"`+added.ID+`","alt":"Soon"}}]}`))).Revision
		res, err := ed.Delete(t.Context(), op, content.RefMedia, added.ID, added.Version.Seq)
		if err != nil {
			t.Fatalf("Delete() = %v, want the medium deleted: only a revision newer than the scheduled one uses it", err)
		}
		if want := docName(content.RefMedia, added.ID) + ": deleted although used by " + revisionName(newer) + ", the latest revision of " +
			docName(content.RefEntry, draftEN) + ", which is not published"; !slices.Equal(lines(res.Warnings), []string{want}) {
			t.Errorf("Delete() warnings:\n%s\nwant\n%s", strings.Join(lines(res.Warnings), "\n"), want)
		}
	})

	// The index has the role of a scheduled revision for any entry that names one, but only an entry that is scheduled
	// publishes it: the store's own API takes a draft that names one.
	namedByDraft := func(t *testing.T, ed *Editor, s *store.Store, medium string) (entry, named string) {
		t.Helper()
		entry, named = testID(content.RefEntry, 1), testID(content.RefRevision, 1)
		doc := decodeEntry(t, []byte(storedScheduled(t, entry, maxNumber(t, s)+1, testID(content.RefTranslationGroup, 1), named)))
		doc.Status = content.StatusDraft
		data, err := doc.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		put(t, s, content.RefEntry, entry, string(data))
		addStoredRevision(t, s, entry, named, `{"schemaVersion":1,"title":"A","slug":"a","body":[`+
			`{"type":"image","id":"b_00000001","attrs":{"media":"`+medium+`","alt":"x"}}]}`)
		return entry, named
	}

	t.Run("a medium that a draft names as its scheduled revision", func(t *testing.T) {
		ed, s := newSample(t)
		added := must(t)(ed.AddMedia(t.Context(), op, bytes.NewReader(testPNG(t, 6, 5, 33)), "named.png", ""))
		entry, named := namedByDraft(t, ed, s, added.ID)

		// The revision that it names is also its latest revision, which is a draft: a warning.
		if got, want := usedByRoles(t, s, added.ID), []store.Role{store.RoleLatest, store.RoleScheduled}; !slices.Equal(got, want) {
			t.Fatalf("the roles of the entry are %v, want %v", got, want)
		}
		res, err := ed.Delete(t.Context(), op, content.RefMedia, added.ID, added.Version.Seq)
		if want := docName(content.RefMedia, added.ID) + ": deleted although used by " + revisionName(named) + ", the latest revision of " +
			docName(content.RefEntry, entry) + ", which is not published"; err != nil || !slices.Equal(lines(res.Warnings), []string{want}) {
			t.Errorf("Delete() = %+v, %v; want the medium deleted with the warning %q", res, err, want)
		}
	})

	t.Run("a medium that only a draft names as its scheduled revision", func(t *testing.T) {
		ed, s := newSample(t)
		added := must(t)(ed.AddMedia(t.Context(), op, bytes.NewReader(testPNG(t, 6, 5, 34)), "named.png", ""))
		entry, _ := namedByDraft(t, ed, s, added.ID)
		addStoredRevision(t, s, entry, testID(content.RefRevision, 2), `{"schemaVersion":1,"title":"B","slug":"a"}`)

		// A newer revision is the latest, and it does not use the medium: nothing that is published or a draft does.
		if got, want := usedByRoles(t, s, added.ID), []store.Role{store.RoleScheduled}; !slices.Equal(got, want) {
			t.Fatalf("the roles of the entry are %v, want %v", got, want)
		}
		res, err := ed.Delete(t.Context(), op, content.RefMedia, added.ID, added.Version.Seq)
		if err != nil || len(res.Warnings) != 0 {
			t.Errorf("Delete() = %+v, %v; want the medium deleted without a warning", res, err)
		}
	})

	t.Run("a term that the scheduled revision uses", func(t *testing.T) {
		toJapanese := func(x *content.Term) { x.Language, x.TranslationGroup = "ja", testID(content.RefTranslationGroup, 1) }
		newPost := func(t *testing.T, ed *Editor, s *store.Store) (tag, entry, rev string) {
			t.Helper()
			tag = must(t)(ed.Create(t.Context(), op, content.RefTerm, []byte(termJSON("tag", "en", "", "Festival", "festival")))).ID
			entry = newDraft(t, ed, content.TypePost, "en")
			rev = must(t)(ed.SaveRevision(t.Context(), op, entry, "", []byte(`{"schemaVersion":1,"title":"Festival","slug":"festival-news",`+
				`"date":"2026-10-30T09:00:00+09:00","terms":{"tag":["`+tag+`"]}}`))).Revision
			return tag, entry, rev
		}
		wantLine := func(t *testing.T, s *store.Store, tag, rev string) string {
			t.Helper()
			return where(t, revisionName(rev), string(revisionData(t, s, rev)), `"`+tag+`"`) +
				`/terms/tag/0: term "` + tag + `" is in the language "ja", not "en"`
		}

		// A draft only has a warning.
		ed, s := newSample(t)
		tag, _, rev := newPost(t, ed, s)
		doc := read(t, s, content.RefTerm, tag)
		res, err := ed.Save(t.Context(), op, content.RefTerm, tag, doc.Seq, []byte(changedTerm(t, doc.Data, toJapanese)))
		if err != nil || !slices.Equal(lines(res.Warnings), []string{wantLine(t, s, tag, rev)}) {
			t.Errorf("Save() of a term that a draft uses = %+v, %v; want it saved with the warning %q", res, err, wantLine(t, s, tag, rev))
		}

		// A scheduled revision is an error.
		ed, s = newSample(t)
		tag, entry, rev := newPost(t, ed, s)
		scheduled := must(t)(ed.Schedule(t.Context(), op, entry, entrySeq(t, s, entry), rev, careersAt))
		if scheduled.Revision != rev {
			t.Fatalf("Schedule() = %+v, want the revision %s scheduled", scheduled, rev)
		}
		doc = read(t, s, content.RefTerm, tag)
		before, index := snapshot(t, s), indexOf(t, s)
		_, err = ed.Save(t.Context(), op, content.RefTerm, tag, doc.Seq, []byte(changedTerm(t, doc.Data, toJapanese)))
		if got := problemLines(t, err); !slices.Equal(got, []string{wantLine(t, s, tag, rev)}) {
			t.Errorf("Save() of a term that the scheduled revision uses:\n%s\nwant the error %q", strings.Join(got, "\n"), wantLine(t, s, tag, rev))
		}
		sameStore(t, s, before, index, "Save()")

		// The same problem is not an error once the schedule is cancelled.
		must(t)(ed.Unpublish(t.Context(), op, entry, entrySeq(t, s, entry)))
		res, err = ed.Save(t.Context(), op, content.RefTerm, tag, doc.Seq, []byte(changedTerm(t, doc.Data, toJapanese)))
		if err != nil || !slices.Equal(lines(res.Warnings), []string{wantLine(t, s, tag, rev)}) {
			t.Errorf("Save() after the schedule is cancelled = %+v, %v; want it saved with the warning", res, err)
		}
	})

	// A revision that is newer than the scheduled one, and uses the term, is a draft.
	t.Run("a term that a newer revision uses", func(t *testing.T) {
		ed, s := newSample(t)
		tag := must(t)(ed.Create(t.Context(), op, content.RefTerm, []byte(termJSON("tag", "en", "", "Festival", "festival")))).ID
		entry := newDraft(t, ed, content.TypePost, "en")
		rev := must(t)(ed.SaveRevision(t.Context(), op, entry, "", []byte(`{"schemaVersion":1,"title":"Festival","slug":"festival-news",`+
			`"date":"2026-10-30T09:00:00+09:00"}`))).Revision
		must(t)(ed.Schedule(t.Context(), op, entry, entrySeq(t, s, entry), rev, careersAt))
		newer := must(t)(ed.SaveRevision(t.Context(), op, entry, rev, []byte(`{"schemaVersion":1,"title":"Festival","slug":"festival-news",`+
			`"date":"2026-10-30T09:00:00+09:00","terms":{"tag":["`+tag+`"]}}`))).Revision
		doc := read(t, s, content.RefTerm, tag)
		res, err := ed.Save(t.Context(), op, content.RefTerm, tag, doc.Seq, []byte(changedTerm(t, doc.Data, func(x *content.Term) {
			x.Language, x.TranslationGroup = "ja", testID(content.RefTranslationGroup, 1)
		})))
		want := where(t, revisionName(newer), string(revisionData(t, s, newer)), `"`+tag+`"`) +
			`/terms/tag/0: term "` + tag + `" is in the language "ja", not "en"`
		if err != nil || !slices.Equal(lines(res.Warnings), []string{want}) {
			t.Errorf("Save() = %+v, %v; want it saved with the warning %q for the newer revision", res, err, want)
		}
	})

	settings := func(extra string) string { return strings.Replace(smallSettings, "%s", extra, 1) }

	t.Run("settings that the scheduled entry no longer passes", func(t *testing.T) {
		x := newSmallSite(t, settings(""), nil)
		must(t)(x.ed.Schedule(t.Context(), op, x.draft, entrySeq(t, x.s, x.draft), x.draftRev, careersAt))
		before, index := snapshot(t, x.s), indexOf(t, x.s)
		_, err := x.ed.SaveSettings(t.Context(), op, read(t, x.s, store.SettingsKind, "").Seq, []byte(smallJA))
		doc := string(read(t, x.s, content.RefEntry, x.draft).Data)
		want := where(t, docName(content.RefEntry, x.draft), doc, `"language"`) + `/language: language "en" is not in the site settings (use ja)`
		if got := problemLines(t, err); !slices.Equal(got, []string{want}) {
			t.Errorf("SaveSettings() problems:\n%s\nwant the error %q, which a draft would only have as a warning", strings.Join(got, "\n"), want)
		}
		sameStore(t, x.s, before, index, "SaveSettings()")

		// Once the schedule is cancelled, it is that warning.
		must(t)(x.ed.Unpublish(t.Context(), op, x.draft, entrySeq(t, x.s, x.draft)))
		doc = string(read(t, x.s, content.RefEntry, x.draft).Data)
		want = where(t, docName(content.RefEntry, x.draft), doc, `"language"`) + `/language: language "en" is not in the site settings (use ja)`
		res, err := x.ed.SaveSettings(t.Context(), op, read(t, x.s, store.SettingsKind, "").Seq, []byte(smallJA))
		if err != nil || !slices.Equal(lines(res.Warnings), []string{want}) {
			t.Errorf("SaveSettings() after the schedule is cancelled = %+v, %v; want it saved with the warning %q", res, err, want)
		}
	})

	t.Run("a scheduled revision with a block that the settings stop allowing", func(t *testing.T) {
		const html = `{"type":"html","id":"b_0000html","attrs":{"html":"<p>x</p>"}}`
		draft := `{"schemaVersion":1,"title":"Draft","slug":"draft","body":[` + html + `]}`
		x := newSmallSite(t, settings(",\n  \"allowHtml\": true"), map[string]string{revisionName(testID(content.RefRevision, 2)): draft})
		must(t)(x.ed.Schedule(t.Context(), op, x.draft, entrySeq(t, x.s, x.draft), x.draftRev, careersAt))
		before, index := snapshot(t, x.s), indexOf(t, x.s)
		_, err := x.ed.SaveSettings(t.Context(), op, read(t, x.s, store.SettingsKind, "").Seq, []byte(settings("")))
		want := where(t, revisionName(x.draftRev), draft, `{"type":"html"`) + "/body/0: html blocks are not enabled"
		if got := problemLines(t, err); !slices.Equal(got, []string{want}) {
			t.Errorf("SaveSettings() problems:\n%s\nwant the error %q", strings.Join(got, "\n"), want)
		}
		sameStore(t, x.s, before, index, "SaveSettings()")
	})

	// A scheduled post without a date is valid with the scheduled time as its date, so a change finds no problem in it that
	// it does not have, and finds those that it has, in the bytes that the store has.
	t.Run("a post without a date that the settings change finds a problem in", func(t *testing.T) {
		const html = `{"type":"html","id":"b_0000html","attrs":{"html":"<p>x</p>"}}`
		post := `{
  "schemaVersion": 1,
  "title": "Post",
  "slug": "post",
  "body": [` + html + `]
}`
		x := newSmallSite(t, settings(",\n  \"allowHtml\": true"), nil)
		entry := newDraft(t, x.ed, content.TypePost, "ja")
		rev := must(t)(x.ed.SaveRevision(t.Context(), op, entry, "", []byte(post))).Revision
		must(t)(x.ed.Schedule(t.Context(), op, entry, entrySeq(t, x.s, entry), rev, careersAt))
		before, index := snapshot(t, x.s), indexOf(t, x.s)
		_, err := x.ed.SaveSettings(t.Context(), op, read(t, x.s, store.SettingsKind, "").Seq, []byte(settings("")))
		want := where(t, revisionName(rev), post, `{"type":"html"`) + "/body/0: html blocks are not enabled"
		if got := problemLines(t, err); !slices.Equal(got, []string{want}) {
			t.Errorf("SaveSettings() problems:\n%s\nwant the error %q alone: the revision is not refused for its missing date", strings.Join(got, "\n"), want)
		}
		sameStore(t, x.s, before, index, "SaveSettings()")
	})

	t.Run("a term that the scheduled revision of a post without a date uses", func(t *testing.T) {
		ed, s := newSample(t)
		tag := must(t)(ed.Create(t.Context(), op, content.RefTerm, []byte(termJSON("tag", "en", "", "Festival", "festival")))).ID
		entry := newDraft(t, ed, content.TypePost, "en")
		post := `{
  "schemaVersion": 1,
  "title": "Festival",
  "slug": "festival-news",
  "terms": { "tag": ["` + tag + `"] }
}`
		rev := must(t)(ed.SaveRevision(t.Context(), op, entry, "", []byte(post))).Revision
		must(t)(ed.Schedule(t.Context(), op, entry, entrySeq(t, s, entry), rev, careersAt))
		doc := read(t, s, content.RefTerm, tag)
		before, index := snapshot(t, s), indexOf(t, s)
		_, err := ed.Save(t.Context(), op, content.RefTerm, tag, doc.Seq, []byte(changedTerm(t, doc.Data, func(x *content.Term) {
			x.Language, x.TranslationGroup = "ja", testID(content.RefTranslationGroup, 1)
		})))
		want := where(t, revisionName(rev), post, `"`+tag+`"`) + `/terms/tag/0: term "` + tag + `" is in the language "ja", not "en"`
		if got := problemLines(t, err); !slices.Equal(got, []string{want}) {
			t.Errorf("Save() of a term that the scheduled revision uses:\n%s\nwant the error %q alone", strings.Join(got, "\n"), want)
		}
		sameStore(t, s, before, index, "Save()")
	})

	// With settings that do not load there is no state before the change, and every problem of the new state counts as new:
	// a post without a date is not one of them.
	t.Run("a post without a date, for settings over some that do not load", func(t *testing.T) {
		ed, s := newSample(t)
		entry := newDraft(t, ed, content.TypePost, "ja")
		rev := must(t)(ed.SaveRevision(t.Context(), op, entry, "", []byte(comingRevision))).Revision
		must(t)(ed.Schedule(t.Context(), op, entry, entrySeq(t, s, entry), rev, careersAt))
		sample := string(sampleFile(t, settingsName))
		put(t, s, store.SettingsKind, "", replaceOnce(t, sample, "Asia/Tokyo", "Mars/Base"))
		res, err := ed.SaveSettings(t.Context(), op, read(t, s, store.SettingsKind, "").Seq, []byte(sample))
		if err != nil {
			t.Fatalf("SaveSettings() = %+v, %v; want the settings saved: the scheduled post has a date when it is published", res, err)
		}
		if got := string(read(t, s, store.SettingsKind, "").Data); got != sample {
			t.Errorf("the settings are %s, want the ones that were saved", got)
		}
	})
}

// TestScheduledPublishFails checks that a scheduled publish that does not pass the checks fails with the problems of the
// build and leaves the entry scheduled, and succeeds once the problem is gone.
func TestScheduledPublishFails(t *testing.T) {
	p := newParity(t)
	rev := scheduleCareers(t, p.ed, p.s)

	// Another page in en takes the slug careers: put with the store's own API, which checks nothing against the site. Its
	// ID is the later one, so that the build gives it the problem.
	other, otherRev := testID(content.RefEntry, 2), testID(content.RefRevision, 2)
	number, group := maxNumber(t, p.s)+1, testID(content.RefTranslationGroup, 2)
	put(t, p.s, content.RefEntry, other, storedEntry(other, number, content.TypePage, "en", group, content.StatusDraft, ""))
	addStoredRevision(t, p.s, other, otherRev, careersRevision)
	put(t, p.s, content.RefEntry, other, storedEntry(other, number, content.TypePage, "en", group, content.StatusPublished, otherRev))

	p.write(docName(content.RefEntry, other), storedEntry(other, number, content.TypePage, "en", group, content.StatusPublished, otherRev))
	p.write(revisionName(otherRev), careersRevision)
	p.write(docName(content.RefEntry, draftEN), publishedFrom(t, read(t, p.s, content.RefEntry, draftEN).Data, rev))
	p.write(revisionName(rev), careersRevision)
	want := buildErrors(t, p.dir)
	if len(want) != 1 || !strings.Contains(want[0], `/slug: slug "careers" is also used by `+revisionName(rev)) {
		t.Fatalf("the problems of the build:\n%s\nwant the one for the slug", strings.Join(want, "\n"))
	}

	before, index := snapshot(t, p.s), indexOf(t, p.s)
	res, err := p.ed.PublishScheduled(t.Context(), draftEN, careersAt)
	if got := problemLines(t, err); !slices.Equal(got, want) || errors.Is(err, ErrNotDue) {
		t.Errorf("PublishScheduled() = %+v with the problems:\n%s\nwant the build's problems:\n%s", res, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	sameStore(t, p.s, before, index, "PublishScheduled()")
	sameSchedules(t, schedules(t, p.ed), []Scheduled{{draftEN, careersAt}})

	// The other page is unpublished: the next try publishes.
	must(t)(p.ed.Unpublish(t.Context(), Op{By: userID}, other, entrySeq(t, p.s, other)))
	pub, err := p.ed.PublishScheduled(t.Context(), draftEN, careersAt)
	if err != nil || pub.Status != content.StatusPublished || !pub.Publishes {
		t.Fatalf("PublishScheduled() after the problem is gone = %+v, %v; want the entry published", pub, err)
	}
	if got := schedules(t, p.ed); len(got) != 0 {
		t.Errorf("Schedules() = %+v, want none", got)
	}
}

// TestUnschedule checks that the operations that make an entry a draft or archived cancel its schedule.
func TestUnschedule(t *testing.T) {
	op := Op{By: userID}
	for _, tt := range []struct {
		name string
		do   func(t *testing.T, ed *Editor, id string, seq int64) (Result, error)
		want content.Status
	}{
		{"Unpublish", func(t *testing.T, ed *Editor, id string, seq int64) (Result, error) {
			return ed.Unpublish(t.Context(), op, id, seq)
		}, content.StatusDraft},
		{"Archive", func(t *testing.T, ed *Editor, id string, seq int64) (Result, error) {
			return ed.Archive(t.Context(), op, id, seq)
		}, content.StatusArchived},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			rev := scheduleCareers(t, ed, s)
			if got := schedules(t, ed); len(got) != 1 {
				t.Fatalf("Schedules() = %+v, want the entry", got)
			}
			res, err := tt.do(t, ed, draftEN, entrySeq(t, s, draftEN))
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != tt.want || res.Publishes {
				t.Errorf("%s() = %+v, want the entry %s, which changes nothing that a build publishes", tt.name, res, tt.want)
			}
			want := decodeEntry(t, read(t, s, content.RefEntry, draftEN).Data)
			if want.Status != tt.want || want.PublishedRevision != "" || want.ScheduledRevision != "" || want.ScheduledAt != "" {
				t.Errorf("the entry is %+v, want it %s without a published or scheduled revision or time", want, tt.want)
			}
			for _, key := range []string{"scheduledRevision", "scheduledAt"} {
				if bytes.Contains(read(t, s, content.RefEntry, draftEN).Data, []byte(key)) {
					t.Errorf("the document of the entry has %s: %s", key, read(t, s, content.RefEntry, draftEN).Data)
				}
			}
			if got := summaryOf(t, s, draftEN); *got.Entry != want || got.Latest != rev {
				t.Errorf("Summary() = %+v %+v, want the state %+v and the latest revision %s", got, got.Entry, want, rev)
			}
			if got := schedules(t, ed); len(got) != 0 {
				t.Errorf("Schedules() = %+v, want none", got)
			}
			if got := revisionIDs(t, s, draftEN); !slices.Equal(got, []string{rev}) {
				t.Errorf("the revisions are %v, want %s to stay", got, rev)
			}
			if _, err := ed.PublishScheduled(t.Context(), draftEN, careersAt); !errors.Is(err, ErrNotDue) {
				t.Errorf("PublishScheduled() of the entry = %v, want ErrNotDue", err)
			}

			// It is scheduled again with the same revision.
			again, err := ed.Schedule(t.Context(), op, draftEN, entrySeq(t, s, draftEN), rev, careersAt)
			if err != nil || again.Status != content.StatusScheduled {
				t.Errorf("Schedule() after it = %+v, %v; want the entry scheduled", again, err)
			}
		})
	}
}

// TestPublishInstead checks that Publish publishes a scheduled entry at once, with its latest revision, and takes the
// schedule away, so that the time does not publish it again.
func TestPublishInstead(t *testing.T) {
	ed, s := newSample(t)
	rev := scheduleCareers(t, ed, s)
	newer := must(t)(ed.SaveRevision(t.Context(), Op{By: userID}, draftEN, rev, []byte(`{"schemaVersion":1,"title":"Careers now","slug":"careers-now"}`))).Revision
	res, err := ed.Publish(t.Context(), Op{By: userID}, draftEN, entrySeq(t, s, draftEN), newer)
	if err != nil || res.Status != content.StatusPublished || !res.Publishes || res.Revision != newer {
		t.Fatalf("Publish() = %+v, %v; want the latest revision %s published", res, err, newer)
	}
	if got := summaryOf(t, s, draftEN).Entry; got.Status != content.StatusPublished || got.PublishedRevision != newer || got.ScheduledRevision != "" || got.ScheduledAt != "" {
		t.Errorf("the entry is %+v, want it published with %s and not scheduled", got, newer)
	}
	if got := schedules(t, ed); len(got) != 0 {
		t.Errorf("Schedules() = %+v, want none", got)
	}
	if _, err := ed.PublishScheduled(t.Context(), draftEN, careersAt); !errors.Is(err, ErrNotDue) {
		t.Errorf("PublishScheduled() of the entry = %v, want ErrNotDue", err)
	}
}

// TestSchedules checks the list of the schedules: the entries that are scheduled, by their times and then their IDs, and
// the entries whose time does not parse first with the zero time.
func TestSchedules(t *testing.T) {
	ed, s := newSample(t)
	if got := schedules(t, ed); len(got) != 0 {
		t.Fatalf("Schedules() = %+v for the sample site, want none", got)
	}
	id := func(n int) string { return testID(content.RefEntry, n) }
	rev := testID(content.RefRevision, 1)
	number := maxNumber(t, s)
	for _, e := range []struct {
		n  int
		at string
	}{
		{4, "2026-11-10T09:00:00+09:00"},
		{2, "2026-11-10T00:00:00Z"}, // the same time as the one before, in another zone: by ID
		{3, "2026-11-09T09:00:00+09:00"},
		{5, "tomorrow"},
		{1, "2026-12-01T00:00:00Z"},
	} {
		put(t, s, content.RefEntry, id(e.n), storedScheduledAt(t, id(e.n), number+e.n, rev, e.at))
	}
	// Entries that are not scheduled are not listed.
	put(t, s, content.RefEntry, id(6), storedEntry(id(6), number+6, content.TypePage, "ja", testID(content.RefTranslationGroup, 6), content.StatusDraft, ""))

	at := func(layout, value string) time.Time {
		tm, err := time.Parse(layout, value)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	sameSchedules(t, schedules(t, ed), []Scheduled{
		{id(5), time.Time{}},
		{id(3), at(time.RFC3339, "2026-11-09T09:00:00+09:00")},
		{id(2), at(time.RFC3339, "2026-11-10T00:00:00Z")},
		{id(4), at(time.RFC3339, "2026-11-10T09:00:00+09:00")},
		{id(1), at(time.RFC3339, "2026-12-01T00:00:00Z")},
	})
	if got := schedules(t, ed)[0].At; !got.IsZero() {
		t.Errorf("the time of the entry whose time does not parse is %v, want the zero time", got)
	}
}

// TestSchedulesTies checks that entries with one time are listed by their IDs, whatever the number of the entries, as a sort
// that does not keep the order of equal elements would not.
func TestSchedulesTies(t *testing.T) {
	ed, s := newSample(t)
	rev := testID(content.RefRevision, 1)
	number := maxNumber(t, s)
	early, late := "2026-11-10T09:00:00+09:00", "2026-11-11T09:00:00+09:00"
	var want []Scheduled
	err := s.Update(t.Context(), storeWrite(func(tx *store.Tx) error {
		for n := 10; n < 90; n++ {
			at := early
			if n%2 == 1 {
				at = late
			}
			id := testID(content.RefEntry, n)
			if _, err := tx.Put(content.RefEntry, id, []byte(storedScheduledAt(t, id, number+n, rev, at)), userID); err != nil {
				return err
			}
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, parity := range []int{0, 1} {
		for n := 10; n < 90; n++ {
			if n%2 == parity {
				at := early
				if parity == 1 {
					at = late
				}
				tm, err := time.Parse(time.RFC3339, at)
				if err != nil {
					t.Fatal(err)
				}
				want = append(want, Scheduled{testID(content.RefEntry, n), tm})
			}
		}
	}
	sameSchedules(t, schedules(t, ed), want)
}

// TestPublishScheduledErrors checks what PublishScheduled refuses: an entry that is not scheduled is the error that wraps
// ErrNotDue, and the problems of a scheduled entry that does not pass its checks are not; nothing is saved.
func TestPublishScheduledErrors(t *testing.T) {
	const missing = "ent_01JBZ0000000000000000000E9"
	entry, rev := testID(content.RefEntry, 1), testID(content.RefRevision, 1)
	name := docName(content.RefEntry, entry)
	group := testID(content.RefTranslationGroup, 1)
	scheduledWith := func(t *testing.T, s *store.Store, language, rev, at string) {
		t.Helper()
		put(t, s, content.RefEntry, entry, scheduledJSON(t, []byte(storedEntry(entry, maxNumber(t, s)+1, content.TypePage, language, group,
			content.StatusDraft, "")), rev, at))
	}
	due := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) // after the time of storedScheduled, 2026-11-10T09:00:00+09:00

	for _, tt := range []struct {
		name  string
		setup func(t *testing.T, ed *Editor, s *store.Store) string // returns the ID of the entry
		want  func(t *testing.T, s *store.Store, id string) string
		is    error
		not   error // that the error must not be
	}{
		{"a draft", func(*testing.T, *Editor, *store.Store) string { return draftEN }, func(t *testing.T, s *store.Store, id string) string {
			return docName(content.RefEntry, id) + ": not scheduled"
		}, ErrNotDue, nil},

		{"a published entry", func(*testing.T, *Editor, *store.Store) string { return aboutEN }, func(t *testing.T, s *store.Store, id string) string {
			return docName(content.RefEntry, id) + ": not scheduled"
		}, ErrNotDue, nil},

		{"an archived entry", func(t *testing.T, ed *Editor, s *store.Store) string {
			must(t)(ed.Archive(t.Context(), Op{By: userID}, draftEN, entrySeq(t, s, draftEN)))
			return draftEN
		}, func(t *testing.T, s *store.Store, id string) string {
			return docName(content.RefEntry, id) + ": not scheduled"
		}, ErrNotDue, nil},

		{"an entry that does not exist", func(*testing.T, *Editor, *store.Store) string { return missing }, func(t *testing.T, s *store.Store, id string) string {
			return "entries/" + missing + ".json: not found"
		}, fs.ErrNotExist, ErrNotDue},

		{"a time that does not parse", func(t *testing.T, ed *Editor, s *store.Store) string {
			scheduledWith(t, s, "ja", rev, "tomorrow")
			addStoredRevision(t, s, entry, rev, careersRevision)
			return entry
		}, func(t *testing.T, s *store.Store, id string) string {
			return where(t, name, string(read(t, s, content.RefEntry, entry).Data), `"scheduledAt"`) +
				`/scheduledAt: invalid date and time "tomorrow": use RFC 3339 with seconds and a time zone offset, such as "2026-10-25T10:00:00+09:00"`
		}, nil, ErrNotDue},

		{"a scheduled entry without its items", func(t *testing.T, ed *Editor, s *store.Store) string {
			put(t, s, content.RefEntry, entry, storedEntry(entry, maxNumber(t, s)+1, content.TypePage, "ja", group, content.StatusScheduled, ""))
			return entry
		}, func(t *testing.T, s *store.Store, id string) string {
			return name + ":1:1: /scheduledAt: missing scheduledAt of a scheduled entry\n" +
				name + ":1:1: /scheduledRevision: missing scheduledRevision of a scheduled entry"
		}, nil, ErrNotDue},

		{"a state that is not valid", func(t *testing.T, ed *Editor, s *store.Store) string {
			scheduledWith(t, s, "xx", rev, "2026-11-10T09:00:00+09:00")
			addStoredRevision(t, s, entry, rev, careersRevision)
			return entry
		}, func(t *testing.T, s *store.Store, id string) string {
			return where(t, name, string(read(t, s, content.RefEntry, entry).Data), `"language"`) +
				`/language: language "xx" is not in the site settings (use ja, en)`
		}, nil, ErrNotDue},

		{"a scheduled revision that the store does not have", func(t *testing.T, ed *Editor, s *store.Store) string {
			scheduledWith(t, s, "ja", rev, "2026-11-10T09:00:00+09:00")
			return entry
		}, func(t *testing.T, s *store.Store, id string) string {
			return where(t, name, string(read(t, s, content.RefEntry, entry).Data), `"scheduledRevision"`) +
				`/scheduledRevision: revision "` + rev + `" does not exist`
		}, nil, ErrNotDue},

		{"a scheduled revision that does not validate", func(t *testing.T, ed *Editor, s *store.Store) string {
			scheduledWith(t, s, "ja", rev, "2026-11-10T09:00:00+09:00")
			addStoredRevision(t, s, entry, rev, `{"schemaVersion":1,"slug":"x"}`)
			return entry
		}, func(t *testing.T, s *store.Store, id string) string {
			return revisionName(rev) + ":1:1: /title: missing title"
		}, nil, ErrNotDue},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			id := tt.setup(t, ed, s)
			before, index := snapshot(t, s), indexOf(t, s)
			res, err := ed.PublishScheduled(t.Context(), id, due)
			want := tt.want(t, s, id)
			if err == nil || err.Error() != want || tt.is != nil && !errors.Is(err, tt.is) || tt.not != nil && errors.Is(err, tt.not) {
				t.Errorf("PublishScheduled() = %+v, %v\nwant the error %q wrapping %v, and not %v", res, err, want, tt.is, tt.not)
			}
			if res.Version != (store.Version{}) || res.Publishes || res.Status != "" {
				t.Errorf("PublishScheduled() returned the result %+v with its error", res)
			}
			sameStore(t, s, before, index, "PublishScheduled()")
		})
	}

	t.Run("a malformed entry ID", func(t *testing.T) {
		ed, _ := newSample(t)
		_, err := ed.PublishScheduled(t.Context(), "ent_x", due)
		if want := `invalid entry ID "ent_x": use "ent_" and a ULID (26 characters in upper case)`; err == nil || err.Error() != want {
			t.Errorf("PublishScheduled() = %v, want %q", err, want)
		}
	})
}

// TestPublishScheduledRedirects checks the redirects of a scheduled publish: none is registered, as the entry was not
// published, and a redirect to the entry from its new path is deleted, as Publish deletes it.
func TestPublishScheduledRedirects(t *testing.T) {
	op := Op{By: userID}

	t.Run("a redirect from the new path", func(t *testing.T) {
		ed, s := newSample(t)
		rev := scheduleCareers(t, ed, s)
		// The redirect is made after the schedule, as the page does not have the path yet.
		made := must(t)(ed.Create(t.Context(), op, content.RefRedirect, []byte(redirectJSON("/en/careers/", draftEN))))
		before := storedRedirects(t, s)
		res, err := ed.PublishScheduled(t.Context(), draftEN, careersAt)
		if err != nil {
			t.Fatal(err)
		}
		want := docName(content.RefRedirect, made.ID) + `: deleted: its from is the new path "/en/careers/" of ` + docName(content.RefEntry, draftEN)
		if got := lines(res.Warnings); !slices.Equal(got, []string{want}) || res.Redirect != "" || res.Revision != rev {
			t.Errorf("PublishScheduled() = %+v with the warnings\n%s\nwant the warning\n%s", res, strings.Join(got, "\n"), want)
		}
		wantRedirects(t, s, before, nil, made.ID)
	})

	t.Run("a redirect from the new path to another entry", func(t *testing.T) {
		ed, s := newSample(t)
		scheduleCareers(t, ed, s)
		made := must(t)(ed.Create(t.Context(), op, content.RefRedirect, []byte(redirectJSON("/en/careers/", "/contact/"))))
		before, index := snapshot(t, s), indexOf(t, s)
		_, err := ed.PublishScheduled(t.Context(), draftEN, careersAt)
		got := problemLines(t, err)
		if len(got) != 1 || !strings.HasPrefix(got[0], docName(content.RefRedirect, made.ID)+":") ||
			!strings.Contains(got[0], `/from: from "/en/careers/" is the output path "/en/careers/" of `) {
			t.Errorf("PublishScheduled() problems:\n%s\nwant the redirect %s refused as a duplicate of the output path", strings.Join(got, "\n"), made.ID)
		}
		sameStore(t, s, before, index, "PublishScheduled()")
	})

	// Schedule checks the site as it would be with the page published, so it refuses what the publish would refuse; a
	// redirect to the page from its path, which Publish deletes, is one of them.
	t.Run("a redirect from the new path before the schedule", func(t *testing.T) {
		ed, s := newSample(t)
		rev := saveRevision(t, ed, s, draftEN, careersRevision).Revision
		made := must(t)(ed.Create(t.Context(), op, content.RefRedirect, []byte(redirectJSON("/en/careers/", draftEN))))
		before, index := snapshot(t, s), indexOf(t, s)
		_, err := ed.Schedule(t.Context(), op, draftEN, entrySeq(t, s, draftEN), rev, careersAt)
		got := problemLines(t, err)
		if len(got) != 1 || !strings.HasPrefix(got[0], docName(content.RefRedirect, made.ID)+":") ||
			!strings.Contains(got[0], `/from: from "/en/careers/" is the output path "/en/careers/" of `+revisionName(rev)) {
			t.Errorf("Schedule() problems:\n%s\nwant the redirect %s refused as a duplicate of the output path", strings.Join(got, "\n"), made.ID)
		}
		sameStore(t, s, before, index, "Schedule()")
	})

	// An entry that was published, then unpublished and scheduled again, registers nothing at its scheduled publish: the
	// redirect from its old path is the work of Publish.
	t.Run("no redirect is registered", func(t *testing.T) {
		ed, s := newSample(t)
		before := storedRedirects(t, s)
		old := read(t, s, content.RefEntry, contactJA)
		must(t)(ed.Unpublish(t.Context(), op, contactJA, old.Seq))
		moved := string(revisionData(t, s, contactRev))
		moved = replaceOnce(t, moved, `"slug": "contact"`, `"slug": "contact-us"`)
		rev := saveRevision(t, ed, s, contactJA, moved).Revision
		must(t)(ed.Schedule(t.Context(), op, contactJA, entrySeq(t, s, contactJA), rev, careersAt))
		res, err := ed.PublishScheduled(t.Context(), contactJA, careersAt)
		if err != nil || res.Redirect != "" {
			t.Fatalf("PublishScheduled() = %+v, %v; want the entry published without a redirect", res, err)
		}
		if got := entryPath(t, ed, s, contactJA); got != "/contact-us/" {
			t.Errorf("the page of the entry is %q, want /contact-us/", got)
		}
		wantRedirects(t, s, before, nil)
	})
}

// TestScheduleOperationID checks that a retry of Schedule returns the recorded result, also when its base is no longer the
// version, and that another request with the same operation ID fails.
func TestScheduleOperationID(t *testing.T) {
	ed, s := newSample(t)
	rev := saveRevision(t, ed, s, draftEN, careersRevision).Revision
	op := Op{By: userID, ID: testOpID(11)}
	seq := entrySeq(t, s, draftEN)
	first, err := ed.Schedule(t.Context(), op, draftEN, seq, rev, careersAt)
	if err != nil || first.Status != content.StatusScheduled || first.Publishes {
		t.Fatalf("Schedule() = %+v, %v; want a result that schedules, without Publishes", first, err)
	}
	after, index := snapshot(t, s), indexOf(t, s)

	// The base is stale now; the retry gets the recorded result and nothing is done.
	again, err := ed.Schedule(t.Context(), op, draftEN, seq, rev, careersAt)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Errorf("a retry of Schedule() = %+v, %v; want the first result %+v", again, err, first)
	}
	sameStore(t, s, after, index, "a retry of Schedule()")

	err = s.View(t.Context(), func(tx *store.Tx) error {
		data, ok, err := tx.Operation(op.ID)
		if err != nil || !ok || !strings.Contains(string(data), `"status":"scheduled"`) || strings.Contains(string(data), "publishes") {
			t.Errorf("Operation() = %s, %t, %v; want the record with the status scheduled and without publishes", data, ok, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Another request with the same ID fails, whatever differs: the entry, the base, the revision, the time, the user or the
	// operation.
	wantErr := fmt.Sprintf("operation ID %q was used for another request", op.ID)
	for name, call := range map[string]func() (Result, error){
		"another entry": func() (Result, error) {
			return ed.Schedule(t.Context(), op, testID(content.RefEntry, 9), seq, rev, careersAt)
		},
		"another base": func() (Result, error) { return ed.Schedule(t.Context(), op, draftEN, seq+1, rev, careersAt) },
		"another revision": func() (Result, error) {
			return ed.Schedule(t.Context(), op, draftEN, seq, testID(content.RefRevision, 9), careersAt)
		},
		"another time": func() (Result, error) {
			return ed.Schedule(t.Context(), op, draftEN, seq, rev, careersAt.Add(time.Second))
		},
		"another user": func() (Result, error) {
			return ed.Schedule(t.Context(), Op{By: otherUser, ID: op.ID}, draftEN, seq, rev, careersAt)
		},
		"another operation": func() (Result, error) { return ed.Archive(t.Context(), op, draftEN, first.Version.Seq) },
	} {
		if res, err := call(); err == nil || err.Error() != wantErr {
			t.Errorf("%s with the same operation ID = %+v, %v; want the error %q", name, res, err, wantErr)
		}
	}
	sameStore(t, s, after, index, "the requests with the same operation ID")

	// The same second with a fraction, and the same time in another time zone, are the same request.
	for _, at := range []time.Time{careersAt.Add(250 * time.Millisecond), careersAt.In(jst)} {
		again, err := ed.Schedule(t.Context(), op, draftEN, seq, rev, at)
		if err != nil || !reflect.DeepEqual(first, again) {
			t.Errorf("Schedule() at %s = %+v, %v; want the recorded result", at, again, err)
		}
	}
}

// TestScheduleNotify checks that Notify gets the results of Schedule and PublishScheduled: the first does not publish and
// the second does.
func TestScheduleNotify(t *testing.T) {
	ed, s := newSample(t)
	var mu sync.Mutex
	var got []Result
	ed.Notify(func(r Result) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, r)
	})
	take := func() []Result {
		mu.Lock()
		defer mu.Unlock()
		r := got
		got = nil
		return r
	}
	rev := saveRevision(t, ed, s, draftEN, careersRevision).Revision
	take()
	scheduled := must(t)(ed.Schedule(t.Context(), Op{By: userID}, draftEN, entrySeq(t, s, draftEN), rev, careersAt))
	if r := take(); len(r) != 1 || !reflect.DeepEqual(r[0], scheduled) || r[0].Publishes || r[0].Status != content.StatusScheduled {
		t.Errorf("after Schedule() Notify got %+v, want %+v", r, scheduled)
	}
	if _, err := ed.PublishScheduled(t.Context(), draftEN, testNow); !errors.Is(err, ErrNotDue) {
		t.Fatalf("PublishScheduled() = %v, want ErrNotDue", err)
	}
	if r := take(); len(r) != 0 {
		t.Errorf("after a PublishScheduled() that was not due Notify got %+v, want none", r)
	}
	published := must(t)(ed.PublishScheduled(t.Context(), draftEN, careersAt))
	if r := take(); len(r) != 1 || !reflect.DeepEqual(r[0], published) || !r[0].Publishes || r[0].Status != content.StatusPublished {
		t.Errorf("after PublishScheduled() Notify got %+v, want %+v", r, published)
	}
}

// TestPublishScheduledConcurrent checks that of several scheduled publishes of one entry, exactly one succeeds: the others
// find it published.
func TestPublishScheduledConcurrent(t *testing.T) {
	ed, s := newSample(t)
	scheduleCareers(t, ed, s)
	const goroutines = 8
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[g] = ed.PublishScheduled(t.Context(), draftEN, careersAt)
		}()
	}
	wg.Wait()
	var done, notDue int
	for _, err := range errs {
		switch {
		case err == nil:
			done++
		case errors.Is(err, ErrNotDue):
			notDue++
		default:
			t.Errorf("PublishScheduled() = %v, want success or ErrNotDue", err)
		}
	}
	if done != 1 || notDue != goroutines-1 {
		t.Errorf("%d scheduled publishes succeeded and %d were not due, want one and %d", done, notDue, goroutines-1)
	}
	if got := summaryOf(t, s, draftEN).Entry.Status; got != content.StatusPublished {
		t.Errorf("the status is %q, want published", got)
	}
}

func TestScheduleNoSettings(t *testing.T) {
	s := newStore(t)
	ed := testEditor(t, s)
	id, rev := testID(content.RefEntry, 1), testID(content.RefRevision, 1)
	put(t, s, content.RefEntry, id, storedEntry(id, 1, content.TypePage, "ja", testID(content.RefTranslationGroup, 1), content.StatusDraft, ""))
	addStoredRevision(t, s, id, rev, careersRevision)
	want := "site/settings.json: not found; save the settings first"
	if _, err := ed.Schedule(t.Context(), Op{By: userID}, id, entrySeq(t, s, id), rev, careersAt); !errors.Is(err, fs.ErrNotExist) || err == nil || err.Error() != want {
		t.Errorf("Schedule() = %v, want %q", err, want)
	}

	scheduled := testID(content.RefEntry, 2)
	put(t, s, content.RefEntry, scheduled, storedScheduledAt(t, scheduled, 2, rev, "2026-11-10T09:00:00+09:00"))
	if _, err := ed.PublishScheduled(t.Context(), scheduled, careersAt.Add(240*time.Hour)); !errors.Is(err, fs.ErrNotExist) || err == nil || err.Error() != want {
		t.Errorf("PublishScheduled() = %v, want %q", err, want)
	}
	// The list of the schedules reads the index alone.
	sameSchedules(t, schedules(t, ed), []Scheduled{{scheduled, time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)}})
}

// TestScheduleLookupFailure checks that a failure to read the index fails the operations that schedule with that error, not
// with problems, and saves nothing.
func TestScheduleLookupFailure(t *testing.T) {
	errRead := errors.New("the index cannot be read")
	op := Op{By: userID}
	for _, tt := range []struct {
		name  string
		call  func(t *testing.T, ed *Editor, s *store.Store, rev string) error
		fails [3]bool // the summary of a document, the summaries of the documents, the summaries for the site before the change
	}{
		{"Schedule", func(t *testing.T, ed *Editor, s *store.Store, rev string) error {
			_, err := ed.Schedule(t.Context(), op, draftEN, entrySeq(t, s, draftEN), rev, careersAt)
			return err
		}, [3]bool{true, true, false}},
		{"PublishScheduled", func(t *testing.T, ed *Editor, s *store.Store, rev string) error {
			_, err := ed.PublishScheduled(t.Context(), draftEN, careersAt)
			return err
		}, [3]bool{true, true, false}},
		{"Schedules", func(t *testing.T, ed *Editor, s *store.Store, rev string) error {
			_, err := ed.Schedules(t.Context())
			return err
		}, [3]bool{false, true, false}},
	} {
		for i, wrap := range []func() func(reader) reader{
			func() func(reader) reader { return failReads(0, failing{summary: errRead}) },
			func() func(reader) reader { return failReads(0, failing{summaries: errRead}) },
			func() func(reader) reader { return failReads(2, failing{summaries: errRead}) },
		} {
			t.Run(fmt.Sprintf("%s/%d", tt.name, i), func(t *testing.T) {
				ed, s := newSample(t)
				var rev string
				if tt.name == "PublishScheduled" {
					rev = scheduleCareers(t, ed, s)
				} else {
					rev = saveRevision(t, ed, s, draftEN, careersRevision).Revision
				}
				ed.wrap = wrap()
				before := snapshot(t, s)
				err := tt.call(t, ed, s, rev)
				var problems content.Errors
				switch {
				case tt.fails[i]:
					if !errors.Is(err, errRead) || errors.As(err, &problems) || snapshot(t, s) != before {
						t.Errorf("%s() = %v; want the error of the read, not problems, and no change", tt.name, err)
					}
				case err != nil:
					t.Errorf("%s() error = %v, want none", tt.name, err)
				}
			})
		}
	}
}

// TestPublishScheduledLatestFailure checks that a failure to read the summary of the entry, which tells its latest revision,
// fails a scheduled publish with that error, not with problems, and saves nothing.
func TestPublishScheduledLatestFailure(t *testing.T) {
	errRead := errors.New("the index cannot be read")
	ed, s := newSample(t)
	scheduleCareers(t, ed, s)
	ed.wrap = func(r reader) reader { return failSummaryOf{r, draftEN, errRead} }
	before, index := snapshot(t, s), indexOf(t, s)
	res, err := ed.PublishScheduled(t.Context(), draftEN, careersAt)
	var problems content.Errors
	if !errors.Is(err, errRead) || errors.As(err, &problems) {
		t.Errorf("PublishScheduled() = %+v, %v; want the error of the read, not problems", res, err)
	}
	sameStore(t, s, before, index, "PublishScheduled()")
}

// TestScheduledUnits checks the validation of a scheduled entry that a change runs again, and which entries have the
// validation of their latest revision besides.
func TestScheduledUnits(t *testing.T) {
	t.Run("bytes that do not decode", func(t *testing.T) {
		ed, s := newSample(t)
		if got := unitProblems(t, ed, s, func(r *run) unit { return r.scheduledUnit(draftEN, []byte("{")) }); len(got) != 0 {
			t.Errorf("the unit of a scheduled entry has the problems %v, want none", got)
		}
	})

	// The build reports a published revision that the store does not have at the entry, and the unit a scheduled one.
	t.Run("a scheduled revision that the store does not have", func(t *testing.T) {
		x := newSmallSite(t, strings.Replace(smallSettings, "%s", "", 1), nil)
		entry, rev := testID(content.RefEntry, 4), testID(content.RefRevision, 4)
		doc := storedScheduledAt(t, entry, 704, rev, careersJST)
		put(t, x.s, content.RefEntry, entry, doc)
		got := lines(unitProblems(t, x.ed, x.s, func(r *run) unit { return r.scheduledUnit(entry, []byte(doc)) }))
		want := where(t, docName(content.RefEntry, entry), doc, `"scheduledRevision"`) + `/scheduledRevision: revision "` + rev + `" does not exist`
		if !slices.Equal(got, []string{want}) {
			t.Errorf("the problems of the unit:\n%s\nwant\n%s", strings.Join(got, "\n"), want)
		}
	})

	// A scheduled revision is validated as the build validates a published one: as an import, without the theme.
	t.Run("a scheduled revision that saving accepted", func(t *testing.T) {
		revision := `{"schemaVersion":1,"title":"ホーム","slug":"soon","body":[` +
			`{"type":"legacy","id":"b_00000001","attrs":{"html":"<p>old</p>","reason":"imported"}},` +
			`{"type":"paragraph","id":"b_00000003","style":{"textColor":{"value":"#ff0000"}},"content":[{"type":"text","text":"x"}]}]}`
		x := newSmallSite(t, strings.Replace(smallSettings, "%s", "", 1), nil)
		entry, rev := testID(content.RefEntry, 4), testID(content.RefRevision, 4)
		doc := storedScheduledAt(t, entry, 704, rev, careersJST)
		put(t, x.s, content.RefEntry, entry, doc)
		addStoredRevision(t, x.s, entry, rev, revision)
		if got := unitProblems(t, x.ed, x.s, func(r *run) unit { return r.scheduledUnit(entry, []byte(doc)) }); len(got) != 0 {
			t.Errorf("the unit of the scheduled revision has the problems %v, want none", got)
		}
	})

	// A post without a date is valid with the scheduled time as its date, as it is published then, where a published post
	// without one is not; the problems of the revision stay in the bytes that the store has.
	t.Run("a post without a date", func(t *testing.T) {
		ed, s := newSample(t)
		entry, rev := testID(content.RefEntry, 1), testID(content.RefRevision, 1)
		group, number := testID(content.RefTranslationGroup, 1), maxNumber(t, s)+1
		doc := scheduledJSON(t, []byte(storedEntry(entry, number, content.TypePost, "ja", group, content.StatusDraft, "")), rev, careersJST)
		put(t, s, content.RefEntry, entry, doc)
		addStoredRevision(t, s, entry, rev, `{"schemaVersion":1,"title":"A","slug":"a"}`)
		if got := unitProblems(t, ed, s, func(r *run) unit { return r.scheduledUnit(entry, []byte(doc)) }); len(got) != 0 {
			t.Errorf("the unit of a scheduled post without a date has the problems %v, want none", got)
		}
		published := storedEntry(entry, number, content.TypePost, "ja", group, content.StatusPublished, rev)
		got := lines(unitProblems(t, ed, s, func(r *run) unit { return r.publishedUnit(entry, []byte(published)) }))
		if want := revisionName(rev) + ":1:1: /date: missing date"; !slices.Equal(got, []string{want}) {
			t.Errorf("the problems of the unit of a published post without a date:\n%s\nwant\n%s", strings.Join(got, "\n"), want)
		}
	})

	t.Run("a post without a date that has a problem", func(t *testing.T) {
		ed, s := newSample(t)
		entry, rev := testID(content.RefEntry, 1), testID(content.RefRevision, 1)
		doc := scheduledJSON(t, []byte(storedEntry(entry, maxNumber(t, s)+1, content.TypePost, "ja",
			testID(content.RefTranslationGroup, 1), content.StatusDraft, "")), rev, careersJST)
		put(t, s, content.RefEntry, entry, doc)
		const revision = `{
  "schemaVersion": 1,
  "title": "A",
  "slug": "-a"
}`
		addStoredRevision(t, s, entry, rev, revision)
		got := lines(unitProblems(t, ed, s, func(r *run) unit { return r.scheduledUnit(entry, []byte(doc)) }))
		if want := where(t, revisionName(rev), revision, `"slug"`) + `/slug: slugs cannot start or end with "-"`; !slices.Equal(got, []string{want}) {
			t.Errorf("the problems of the unit:\n%s\nwant\n%s", strings.Join(got, "\n"), want)
		}
	})

	t.Run("which units a scheduled entry has", func(t *testing.T) {
		ed, s := newSample(t)
		op := Op{By: userID}
		tag := must(t)(ed.Create(t.Context(), op, content.RefTerm, []byte(termJSON("tag", "en", "", "Festival", "festival")))).ID
		entry := newDraft(t, ed, content.TypePost, "en")
		withTag := `{"schemaVersion":1,"title":"Festival","slug":"festival-news","date":"2026-10-30T09:00:00+09:00","terms":{"tag":["` + tag + `"]}}`
		without := `{"schemaVersion":1,"title":"Festival","slug":"festival-news","date":"2026-10-30T09:00:00+09:00"}`
		units := func() []unit {
			var us []unit
			err := s.View(t.Context(), func(tx *store.Tx) error {
				var err error
				us, err = newRun(ed, t.Context(), tx, "").referrerUnits(content.RefTerm, tag)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			return us
		}
		drafts := func(us []unit) []bool {
			var out []bool
			for _, u := range us {
				out = append(out, u.draft)
			}
			return out
		}

		// A draft: the unit of its latest revision, which is a draft.
		rev := must(t)(ed.SaveRevision(t.Context(), op, entry, "", []byte(withTag))).Revision
		if got := drafts(units()); !slices.Equal(got, []bool{true}) {
			t.Fatalf("the units of a draft that uses the tag have draft = %v, want [true]", got)
		}

		// Scheduled with its latest revision: the unit of the scheduled revision alone, which is not a draft.
		must(t)(ed.Schedule(t.Context(), op, entry, entrySeq(t, s, entry), rev, careersAt))
		if got := drafts(units()); !slices.Equal(got, []bool{false}) {
			t.Errorf("the units of a scheduled entry that uses the tag have draft = %v, want [false]", got)
		}

		// A newer revision that uses it as well: its unit is a draft, and comes after.
		must(t)(ed.SaveRevision(t.Context(), op, entry, rev, []byte(withTag)))
		if got := drafts(units()); !slices.Equal(got, []bool{false, true}) {
			t.Errorf("the units of a scheduled entry and a newer revision that use the tag have draft = %v, want [false true]", got)
		}

		// A newer revision that uses it, and a scheduled revision that does not: the draft alone.
		must(t)(ed.Unpublish(t.Context(), op, entry, entrySeq(t, s, entry)))
		second := must(t)(ed.SaveRevision(t.Context(), op, entry, summaryOf(t, s, entry).Latest, []byte(without))).Revision
		must(t)(ed.Schedule(t.Context(), op, entry, entrySeq(t, s, entry), second, careersAt))
		must(t)(ed.SaveRevision(t.Context(), op, entry, second, []byte(withTag)))
		if got := drafts(units()); !slices.Equal(got, []bool{true}) {
			t.Errorf("the units of a scheduled entry whose newer revision alone uses the tag have draft = %v, want [true]", got)
		}

		// An entry whose schedule is cancelled is a draft.
		must(t)(ed.Unpublish(t.Context(), op, entry, entrySeq(t, s, entry)))
		if got := drafts(units()); !slices.Equal(got, []bool{true}) {
			t.Errorf("the units of an entry whose schedule is cancelled have draft = %v, want [true]", got)
		}

		// An entry that is not scheduled does not have the unit of a scheduled revision even if the index has the role: the
		// store's own API takes a draft that names one.
		other := testID(content.RefEntry, 1)
		draft := decodeEntry(t, []byte(storedScheduled(t, other, maxNumber(t, s)+1, testID(content.RefTranslationGroup, 1), testID(content.RefRevision, 5))))
		draft.Status, draft.Type, draft.Language = content.StatusDraft, content.TypePost, "en"
		data, err := draft.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		put(t, s, content.RefEntry, other, string(data))
		addStoredRevision(t, s, other, testID(content.RefRevision, 5), withTag)
		if got := drafts(units()); !slices.Equal(got, []bool{true, true}) {
			t.Errorf("the units of a draft that names a scheduled revision have draft = %v, want [true true]", got)
		}
	})

	t.Run("publishesLatest", func(t *testing.T) {
		sum := func(status content.Status, published, scheduled, latest string) store.Summary {
			return store.Summary{Entry: &content.Entry{Status: status, PublishedRevision: published, ScheduledRevision: scheduled}, Latest: latest}
		}
		for _, tt := range []struct {
			name string
			sum  store.Summary
			want bool
		}{
			{"published with its latest revision", sum(content.StatusPublished, "r1", "", "r1"), true},
			{"published with another revision", sum(content.StatusPublished, "r1", "", "r2"), false},
			{"scheduled with its latest revision", sum(content.StatusScheduled, "", "r1", "r1"), true},
			{"scheduled with another revision", sum(content.StatusScheduled, "", "r1", "r2"), false},
			{"a draft", sum(content.StatusDraft, "", "", "r1"), false},
			{"an archived entry", sum(content.StatusArchived, "", "", ""), false},
			{"published, with the scheduled revision as the latest", sum(content.StatusPublished, "r1", "r2", "r2"), false},
			{"scheduled, with the published revision as the latest", sum(content.StatusScheduled, "r2", "r1", "r2"), false},
			{"no entry", store.Summary{Latest: "r1"}, false},
		} {
			if got := publishesLatest(tt.sum); got != tt.want {
				t.Errorf("publishesLatest(%s) = %t, want %t", tt.name, got, tt.want)
			}
		}
	})
}

// TestImportScheduled checks that the Import takes a scheduled entry with the revision that it schedules, as it takes the
// published entries with theirs, and refuses it without the revision.
func TestImportScheduled(t *testing.T) {
	t.Parallel()
	entry, rev := testID(content.RefEntry, 1), testID(content.RefRevision, 1)
	const page = `{"schemaVersion":1,"title":"Soon","slug":"soon"}`
	scheduledEntry := func(t *testing.T, rev string) string {
		return storedScheduled(t, entry, 301, testID(content.RefTranslationGroup, 1), rev)
	}
	withEntry := func(t *testing.T, rev string) string {
		dir := copySite(t)
		writeSite(t, dir, docName(content.RefEntry, entry), scheduledEntry(t, rev))
		return dir
	}

	t.Run("with its revision", func(t *testing.T) {
		t.Parallel()
		dir := withEntry(t, rev)
		writeSite(t, dir, revisionName(rev), page)
		if got := buildErrors(t, dir); len(got) != 0 {
			t.Fatalf("the problems of the build:\n%s", strings.Join(got, "\n"))
		}
		s := newStore(t)
		if _, err := importSite(t, s, dir); err != nil {
			t.Fatalf("Import() error = %v", err)
		}
		if got := revisionData(t, s, rev); string(got) != page {
			t.Errorf("the revision is %s, want the bytes of its file", got)
		}
		if got := revisionIDs(t, s, entry); !slices.Equal(got, []string{rev}) {
			t.Errorf("the revisions of the entry are %v, want %s", got, rev)
		}
		sum := summaryOf(t, s, entry)
		if sum.Entry.Status != content.StatusScheduled || sum.Entry.ScheduledRevision != rev || sum.Entry.ScheduledAt != "2026-11-10T09:00:00+09:00" || sum.Latest != rev {
			t.Errorf("Summary() = %+v %+v, want the entry scheduled with %s", sum, sum.Entry, rev)
		}
		sameFiles(t, storedFiles(t, s), siteFiles(t, dir))
		sameSchedules(t, schedules(t, testEditor(t, s)), []Scheduled{{entry, time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)}})
	})

	t.Run("without its revision", func(t *testing.T) {
		t.Parallel()
		dir := withEntry(t, rev)
		importRefuses(t, dir, true, docName(content.RefEntry, entry)+`: /scheduledRevision: revision "`+rev+`" does not exist`)
		s := newStore(t)
		_, err := importSite(t, s, dir)
		doc := scheduledEntry(t, rev)
		want := where(t, docName(content.RefEntry, entry), doc, `"scheduledRevision"`) + `/scheduledRevision: revision "` + rev + `" does not exist`
		if got := problemLines(t, err); !slices.Equal(got, []string{want}) {
			t.Errorf("the problems of Import():\n%s\nwant\n%s", strings.Join(got, "\n"), want)
		}
	})

	// A revision that is scheduled is validated as one that is published, which the build never reads.
	t.Run("a revision that does not validate", func(t *testing.T) {
		t.Parallel()
		dir := withEntry(t, rev)
		writeSite(t, dir, revisionName(rev), `{"schemaVersion":1,"slug":"soon"}`)
		importRefuses(t, dir, true, revisionName(rev)+`: /title: missing title`)
	})

	t.Run("a revision that does not decode", func(t *testing.T) {
		t.Parallel()
		dir := withEntry(t, rev)
		writeSite(t, dir, revisionName(rev), `{`)
		importRefuses(t, dir, true, revisionName(rev)+`: unexpected end of JSON input`)
	})

	// The slug of a scheduled revision is not checked against the published pages, as the build checks only published
	// entries: its publish would find the problem.
	t.Run("a slug that a page has", func(t *testing.T) {
		t.Parallel()
		dir := withEntry(t, rev)
		writeSite(t, dir, revisionName(rev), `{"schemaVersion":1,"title":"About","slug":"about"}`)
		s := newStore(t)
		if _, err := importSite(t, s, dir); err != nil {
			t.Errorf("Import() error = %v, want the site imported: a scheduled entry is not checked across the site", err)
		}
	})

	// A post without a date is validated with the scheduled time as its date, as its scheduled publish publishes it, and
	// stored as it is. The build ignores it.
	scheduledPost := func(t *testing.T) string {
		t.Helper()
		e := decodeEntry(t, []byte(scheduledEntry(t, rev)))
		e.Type = content.TypePost
		data, err := e.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	t.Run("a post without a date", func(t *testing.T) {
		t.Parallel()
		dir := copySite(t)
		writeSite(t, dir, docName(content.RefEntry, entry), scheduledPost(t))
		writeSite(t, dir, revisionName(rev), page)
		if got := buildErrors(t, dir); len(got) != 0 {
			t.Fatalf("the problems of the build:\n%s", strings.Join(got, "\n"))
		}
		s := newStore(t)
		if _, err := importSite(t, s, dir); err != nil {
			t.Fatalf("Import() error = %v", err)
		}
		if got := revisionData(t, s, rev); string(got) != page {
			t.Errorf("the revision is %s, want the bytes of its file, which lack the date", got)
		}
		sameFiles(t, storedFiles(t, s), siteFiles(t, dir))

		// Its scheduled publish gives it the date of the schedule, with the same checks.
		ed := testEditor(t, s)
		pub := must(t)(ed.PublishScheduled(t.Context(), entry, time.Date(2026, 11, 10, 1, 0, 0, 0, time.UTC)))
		if pub.Revision == rev || !pub.Publishes {
			t.Fatalf("PublishScheduled() = %+v, want a new revision published", pub)
		}
		if got := decodeRevision(t, revisionData(t, s, pub.Revision)).Date; got != "2026-11-10T09:00:00+09:00" {
			t.Errorf("the date is %q, want the scheduled time, 2026-11-10T09:00:00+09:00", got)
		}
		if got := entryPath(t, ed, s, entry); got != "/2026/11/10/soon/" {
			t.Errorf("the page of the entry is %q, want /2026/11/10/soon/", got)
		}
	})

	t.Run("a post without a date that has a problem", func(t *testing.T) {
		t.Parallel()
		dir := copySite(t)
		writeSite(t, dir, docName(content.RefEntry, entry), scheduledPost(t))
		writeSite(t, dir, revisionName(rev), `{"schemaVersion":1,"slug":"soon"}`)
		importRefuses(t, dir, true, revisionName(rev)+`: /title: missing title`)
	})

	t.Run("an entry without its items", func(t *testing.T) {
		t.Parallel()
		dir := copySite(t)
		writeSite(t, dir, docName(content.RefEntry, entry),
			storedEntry(entry, 301, content.TypePage, "ja", testID(content.RefTranslationGroup, 1), content.StatusScheduled, ""))
		importRefuses(t, dir, true,
			docName(content.RefEntry, entry)+`: /scheduledAt: missing scheduledAt of a scheduled entry`,
			docName(content.RefEntry, entry)+`: /scheduledRevision: missing scheduledRevision of a scheduled entry`)
	})

	// The revision that a scheduled entry and a published entry both name is stored once, with the first of them.
	t.Run("a revision that a published and a scheduled entry name", func(t *testing.T) {
		t.Parallel()
		dir := withEntry(t, aboutENRev)
		s := newStore(t)
		if _, err := importSite(t, s, dir); err != nil {
			t.Fatalf("Import() error = %v", err)
		}
		// aboutEN sorts before the new entry, so it owns the revision.
		if got := revisionIDs(t, s, aboutEN); !slices.Equal(got, []string{aboutENRev}) {
			t.Errorf("the revisions of %s are %v, want %s", aboutEN, got, aboutENRev)
		}
		if got := revisionIDs(t, s, entry); len(got) != 0 {
			t.Errorf("the revisions of %s are %v, want none", entry, got)
		}
		sameFiles(t, storedFiles(t, s), siteFiles(t, dir))
		sum := summaryOf(t, s, entry)
		if sum.Entry.ScheduledRevision != aboutENRev || sum.Latest != "" {
			t.Errorf("Summary() = %+v %+v, want the entry scheduled with %s, which another entry owns", sum, sum.Entry, aboutENRev)
		}
	})

	// The same revision, with a scheduled entry that sorts first, which then owns it.
	t.Run("a revision that a scheduled entry that sorts first and a published entry name", func(t *testing.T) {
		t.Parallel()
		dir := copySite(t)
		first := "ent_00000000000000000000000001"
		writeSite(t, dir, docName(content.RefEntry, first), storedScheduled(t, first, 302, testID(content.RefTranslationGroup, 2), aboutENRev))
		s := newStore(t)
		if _, err := importSite(t, s, dir); err != nil {
			t.Fatalf("Import() error = %v", err)
		}
		if got := revisionIDs(t, s, first); !slices.Equal(got, []string{aboutENRev}) {
			t.Errorf("the revisions of %s are %v, want %s", first, got, aboutENRev)
		}
		if got := revisionIDs(t, s, aboutEN); len(got) != 0 {
			t.Errorf("the revisions of %s are %v, want none", aboutEN, got)
		}
		sameFiles(t, storedFiles(t, s), siteFiles(t, dir))
		// The published entry still has its revision for the checks: the summary has the page.
		if sum := summaryOf(t, s, aboutEN); sum.Published == nil || sum.Published.Slug != "about" {
			t.Errorf("the summary of %s is %+v, want the published revision %s", aboutEN, sum, aboutENRev)
		}
	})
}
