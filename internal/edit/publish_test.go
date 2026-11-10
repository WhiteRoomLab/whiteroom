package edit

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/build"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/sitedir"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// careersRevision gives the draft page draftEN of the sample site, in en, a slug that no page has.
const careersRevision = `{
  "schemaVersion": 1,
  "title": "Careers",
  "slug": "careers"
}`

// saveRevision saves data as the next revision of the entry, for a caller that has read the latest one.
func saveRevision(t *testing.T, ed *Editor, s *store.Store, entry, data string) Result {
	t.Helper()
	return must(t)(ed.SaveRevision(t.Context(), Op{By: userID}, entry, summaryOf(t, s, entry).Latest, []byte(data)))
}

// entrySeq returns the version number of the document of the entry id.
func entrySeq(t *testing.T, s *store.Store, id string) int64 {
	t.Helper()
	return read(t, s, content.RefEntry, id).Seq
}

// entryPath returns the path of the page of the entry id in the plan of the site that s has: "" if the entry has none,
// as an entry that is not published.
func entryPath(t *testing.T, ed *Editor, s *store.Store, id string) string {
	t.Helper()
	var path string
	err := s.View(t.Context(), func(tx *store.Tx) error {
		r := newRun(ed, t.Context(), tx, "")
		site, settings, err := r.site()
		if err != nil {
			return err
		}
		m, err := r.model(site, settings)
		if err != nil {
			return err
		}
		path = m.Plan(func(content.SiteFile, string, string) {}).EntryPaths[id]
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// storedScheduled returns the JSON of a page in ja that is scheduled with the revision rev, as the store's own API takes
// it.
func storedScheduled(t *testing.T, id string, number int, group, rev string) string {
	t.Helper()
	e := content.Entry{
		ID: id, Number: number, Type: content.TypePage, Language: "ja", TranslationGroup: group,
		Status: content.StatusScheduled, ScheduledRevision: rev, ScheduledAt: "2026-11-10T09:00:00+09:00",
		CreatedAt: "2026-10-25T10:00:00+09:00", CreatedBy: userID,
	}
	data, err := e.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// retired returns the JSON of the entry whose document is data, with the status to and no published revision.
func retired(t *testing.T, data []byte, to content.Status) string {
	t.Helper()
	e := decodeEntry(t, data)
	e.Status, e.PublishedRevision = to, ""
	out, err := e.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// sameStore fails the test if the store has changed from before, the snapshot that it had, or from index, what its index
// answered.
func sameStore(t *testing.T, s *store.Store, before string, index indexState, what string) {
	t.Helper()
	if got := snapshot(t, s); got != before {
		t.Errorf("%s has changed the store:\n%s\nwant\n%s", what, got, before)
	}
	sameIndex(t, what, indexOf(t, s), index)
}

// TestPublish publishes the latest revision of a draft: the state, the summary and the page, and publishing again.
func TestPublish(t *testing.T) {
	ed, s := newSample(t)
	op := Op{By: userID}
	rev := saveRevision(t, ed, s, draftEN, careersRevision).Revision
	before := read(t, s, content.RefEntry, draftEN)
	if got := entryPath(t, ed, s, draftEN); got != "" {
		t.Fatalf("the draft has the page %q", got)
	}

	res, err := ed.Publish(t.Context(), op, draftEN, before.Seq, rev)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != content.RefEntry || res.ID != draftEN || res.Revision != rev || res.Status != content.StatusPublished ||
		!res.Publishes || res.Number != 0 || len(res.Warnings) != 0 {
		t.Errorf("Publish() = %+v, want the entry %s published with %s, which changes what is published, without warnings", res, draftEN, rev)
	}
	doc := read(t, s, content.RefEntry, draftEN)
	if doc.Version != res.Version || doc.Seq <= before.Seq || doc.SavedBy != userID || doc.Kind != content.RefEntry || doc.ID != draftEN {
		t.Errorf("Document() = %+v, Publish() returned the version %+v after %d", doc.Version, res.Version, before.Seq)
	}
	want := decodeEntry(t, before.Data)
	want.Status, want.PublishedRevision = content.StatusPublished, rev
	if got := decodeEntry(t, doc.Data); got != want {
		t.Errorf("the entry is %+v, want %+v", got, want)
	}
	if again, err := want.MarshalJSON(); err != nil || !bytes.Equal(again, doc.Data) {
		t.Errorf("the stored bytes are %s, MarshalJSON writes %s, %v", doc.Data, again, err)
	}
	sum := summaryOf(t, s, draftEN)
	if *sum.Entry != want || sum.Latest != rev || sum.Published == nil || sum.Published.Slug != "careers" {
		t.Errorf("Summary() = %+v %+v, want the entry %+v published with %s", sum, sum.Entry, want, rev)
	}
	if got := entryPath(t, ed, s, draftEN); got != "/en/careers/" {
		t.Errorf("the page of the entry is %q, want /en/careers/", got)
	}
	if got := revisionData(t, s, rev); string(got) != careersRevision {
		t.Errorf("the revision is %s, want it as it was", got)
	}

	// Publishing what is published saves nothing and keeps the version.
	after, index := snapshot(t, s), indexOf(t, s)
	again, err := ed.Publish(t.Context(), op, draftEN, doc.Seq, rev)
	if err != nil {
		t.Fatal(err)
	}
	if again.Version != res.Version || again.Publishes || again.Status != content.StatusPublished || again.Revision != rev ||
		again.Kind != content.RefEntry || again.ID != draftEN || len(again.Warnings) != 0 {
		t.Errorf("the second Publish() = %+v, want the same version, and that nothing is changed", again)
	}
	sameStore(t, s, after, index, "the second Publish()")
}

// TestPublishNewRevision publishes a new revision of a page that is published, whose path stays.
func TestPublishNewRevision(t *testing.T) {
	ed, s := newSample(t)
	old := string(revisionData(t, s, aboutENRev))
	rev := saveRevision(t, ed, s, aboutEN, replaceOnce(t, old, `"title": "About us"`, `"title": "About the company"`)).Revision
	if got := entryPath(t, ed, s, aboutEN); got != "/en/about/" {
		t.Fatalf("the page of the entry is %q, want /en/about/", got)
	}
	was := read(t, s, content.RefEntry, aboutEN)

	res, err := ed.Publish(t.Context(), Op{By: userID}, aboutEN, was.Seq, rev)
	if err != nil {
		t.Fatal(err)
	}
	if res.Revision != rev || res.Status != content.StatusPublished || !res.Publishes || len(res.Warnings) != 0 || res.Version.Seq <= was.Seq {
		t.Errorf("Publish() = %+v, want the new revision %s published", res, rev)
	}
	sum := summaryOf(t, s, aboutEN)
	if sum.Entry.PublishedRevision != rev || sum.Entry.Status != content.StatusPublished || sum.Latest != rev {
		t.Errorf("Summary() = %+v %+v, want %s published", sum, sum.Entry, rev)
	}
	if got := entryPath(t, ed, s, aboutEN); got != "/en/about/" {
		t.Errorf("the page of the entry is %q, want the same path", got)
	}
	if got := revisionData(t, s, aboutENRev); string(got) != old {
		t.Errorf("the revision that was published is %s, want it as it was", got)
	}
}

// TestPublishDate publishes a post whose revision has no date: the date goes into a new revision, which is the latest and
// the one that is published.
func TestPublishDate(t *testing.T) {
	op := Op{By: userID}
	t.Run("a post without a date", func(t *testing.T) {
		ed, s := newSample(t)
		ids := predictIDs(t, content.RefEntry, content.RefTranslationGroup, content.RefRevision, content.RefRevision)
		entry := newDraft(t, ed, content.TypePost, "ja")
		const data = `{"schemaVersion":1,"title":"今日の投稿","slug":"today","terms":{"tag":["` + tagJA + `"]},` +
			`"body":[{"type":"paragraph","id":"b_00000001","content":[{"type":"text","text":"本文"}]}]}`
		saved := must(t)(ed.SaveRevision(t.Context(), op, entry, "", []byte(data)))
		if saved.Revision != ids[2] || len(saved.Warnings) != 1 || !strings.HasSuffix(saved.Warnings[0].Error(), "/date: missing date") {
			t.Fatalf("SaveRevision() = %+v, want the revision %s with the warning for the date", saved, ids[2])
		}

		res, err := ed.Publish(t.Context(), op, entry, entrySeq(t, s, entry), saved.Revision)
		if err != nil {
			t.Fatal(err)
		}
		if res.Revision != ids[3] || res.Status != content.StatusPublished || !res.Publishes || len(res.Warnings) != 0 {
			t.Fatalf("Publish() = %+v, want the new revision %s published", res, ids[3])
		}
		// The copy has the date of the operation, in the time zone of the site, and nothing else changed.
		want := decodeRevision(t, []byte(data))
		want.Date = testNowJST
		if got := decodeRevision(t, revisionData(t, s, res.Revision)); !reflect.DeepEqual(got, want) {
			t.Errorf("the new revision is %+v, want %+v", got, want)
		}
		if again, err := want.MarshalJSON(); err != nil || !bytes.Equal(again, revisionData(t, s, res.Revision)) {
			t.Errorf("the stored bytes are %s, MarshalJSON writes %s, %v", revisionData(t, s, res.Revision), again, err)
		}
		if got := revisionData(t, s, saved.Revision); string(got) != data {
			t.Errorf("the revision that was given is %s, want it as it was", got)
		}
		if got := revisionIDs(t, s, entry); !slices.Equal(got, []string{saved.Revision, res.Revision}) {
			t.Errorf("the revisions are %v, want the saved one and the new one", got)
		}
		sum := summaryOf(t, s, entry)
		if sum.Latest != res.Revision || sum.Entry.PublishedRevision != res.Revision || sum.Published == nil || sum.Published.Date != testNowJST {
			t.Errorf("Summary() = %+v %+v, want the new revision as the latest and the published one", sum, sum.Entry)
		}
		if got := entryPath(t, ed, s, entry); got != "/2026/11/07/today/" {
			t.Errorf("the page of the entry is %q, want /2026/11/07/today/", got)
		}
		// The caller's base is no longer the latest revision.
		_, err = ed.SaveRevision(t.Context(), op, entry, saved.Revision, []byte(data))
		if want := docName(content.RefEntry, entry) + ": changed since it was read: its latest revision is " + res.Revision +
			", not " + saved.Revision; !errors.Is(err, ErrConflict) || err.Error() != want {
			t.Errorf("SaveRevision() with the old base = %v, want %q wrapping ErrConflict", err, want)
		}

		// A second Publish of the new revision saves nothing.
		before, index := snapshot(t, s), indexOf(t, s)
		again := must(t)(ed.Publish(t.Context(), op, entry, res.Version.Seq, res.Revision))
		if again.Publishes || again.Revision != res.Revision || again.Version != res.Version {
			t.Errorf("the second Publish() = %+v, want that nothing is changed", again)
		}
		sameStore(t, s, before, index, "the second Publish()")
	})

	t.Run("a post with a date", func(t *testing.T) {
		ed, s := newSample(t)
		entry := newDraft(t, ed, content.TypePost, "ja")
		const data = `{"schemaVersion":1,"title":"先日の投稿","slug":"earlier","date":"2026-10-30T09:00:00+09:00"}`
		saved := must(t)(ed.SaveRevision(t.Context(), op, entry, "", []byte(data)))
		res, err := ed.Publish(t.Context(), op, entry, entrySeq(t, s, entry), saved.Revision)
		if err != nil || res.Revision != saved.Revision {
			t.Fatalf("Publish() = %+v, %v; want the revision that was given", res, err)
		}
		if got := revisionIDs(t, s, entry); !slices.Equal(got, []string{saved.Revision}) {
			t.Errorf("the revisions are %v, want the one that was saved", got)
		}
		if got := entryPath(t, ed, s, entry); got != "/2026/10/30/earlier/" {
			t.Errorf("the page of the entry is %q, want /2026/10/30/earlier/", got)
		}
	})

	// The date is in the time zone of the site, to the second.
	t.Run("the time zone of the site", func(t *testing.T) {
		ed, s := newSample(t)
		entry := newDraft(t, ed, content.TypePost, "ja")
		saved := must(t)(ed.SaveRevision(t.Context(), op, entry, "", []byte(`{"schemaVersion":1,"title":"a","slug":"a"}`)))
		// 23:30 at -05:00 is 13:30 the next day in Asia/Tokyo.
		ed.SetClock(func() time.Time {
			return time.Date(2026, 11, 7, 23, 30, 45, 999_000_000, time.FixedZone("west", -5*60*60))
		})
		res := must(t)(ed.Publish(t.Context(), op, entry, entrySeq(t, s, entry), saved.Revision))
		if got := decodeRevision(t, revisionData(t, s, res.Revision)).Date; got != "2026-11-08T13:30:45+09:00" {
			t.Errorf("the date is %q, want 2026-11-08T13:30:45+09:00", got)
		}
	})

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
		ed.SetClock(func() time.Time { return testNow.Add(999 * time.Millisecond) })
		res := must(t)(ed.Publish(t.Context(), op, entry, entrySeq(t, s, entry), saved.Revision))
		if got := decodeRevision(t, revisionData(t, s, res.Revision)).Date; got != "2026-11-07T10:00:00Z" {
			t.Errorf("the date is %q, want 2026-11-07T10:00:00Z", got)
		}
	})
}

// TestPublishEntryWithoutID checks the operations on an entry whose document has no id, which the store's own API accepts: they
// write the state with the id, which the build requires of a published entry.
func TestPublishEntryWithoutID(t *testing.T) {
	op := Op{By: userID}
	entry, rev := testID(content.RefEntry, 1), testID(content.RefRevision, 1)
	setup := func(t *testing.T, status content.Status, published string) (*Editor, *store.Store) {
		ed, s := newSample(t)
		put(t, s, content.RefEntry, entry,
			storedEntry("", maxNumber(t, s)+1, content.TypePage, "ja", testID(content.RefTranslationGroup, 1), status, published))
		addStoredRevision(t, s, entry, rev, careersRevision)
		if strings.Contains(string(read(t, s, content.RefEntry, entry).Data), `"id"`) {
			t.Fatal("the document has an id")
		}
		return ed, s
	}
	t.Run("Publish", func(t *testing.T) {
		ed, s := setup(t, content.StatusDraft, "")
		must(t)(ed.Publish(t.Context(), op, entry, entrySeq(t, s, entry), rev))
		if got := decodeEntry(t, read(t, s, content.RefEntry, entry).Data); got.ID != entry || got.Status != content.StatusPublished {
			t.Errorf("the entry is %+v, want it published with its id", got)
		}
	})
	t.Run("Unpublish", func(t *testing.T) {
		ed, s := setup(t, content.StatusPublished, rev)
		must(t)(ed.Unpublish(t.Context(), op, entry, entrySeq(t, s, entry)))
		if got := decodeEntry(t, read(t, s, content.RefEntry, entry).Data); got.ID != entry || got.Status != content.StatusDraft {
			t.Errorf("the entry is %+v, want a draft with its id", got)
		}
	})
	// A state that is the same but for the id is the same: nothing is saved.
	t.Run("Archive of what is archived", func(t *testing.T) {
		ed, s := setup(t, content.StatusArchived, "")
		before, index := snapshot(t, s), indexOf(t, s)
		res := must(t)(ed.Archive(t.Context(), op, entry, entrySeq(t, s, entry)))
		if res.Publishes || res.Status != content.StatusArchived {
			t.Errorf("Archive() = %+v, want that nothing is changed", res)
		}
		sameStore(t, s, before, index, "Archive()")
	})
}

// TestPublishKeepsLegacy checks that a revision is validated as an import is: the legacy blocks and the images without alt
// that it holds are accepted, as the build accepts them in a revision that it publishes.
func TestPublishKeepsLegacy(t *testing.T) {
	ed, s := newSample(t)
	rev := testID(content.RefRevision, 1)
	addStoredRevision(t, s, draftEN, rev, `{"schemaVersion":1,"title":"Old","slug":"old","body":[`+
		`{"type":"legacy","id":"b_00000001","attrs":{"html":"<p>old</p>","reason":"imported"}},`+
		`{"type":"image","id":"b_00000002","attrs":{"media":"`+jpegMedium+`"}}]}`)
	res, err := ed.Publish(t.Context(), Op{By: userID}, draftEN, entrySeq(t, s, draftEN), rev)
	if err != nil || res.Revision != rev {
		t.Errorf("Publish() = %+v, %v; want the revision published", res, err)
	}
}

// TestPublishErrors checks what Publish refuses: nothing is saved, and the problems are located in the bytes of the
// entry and of the revision.
func TestPublishErrors(t *testing.T) {
	const missing = "ent_01JBZ0000000000000000000E9"
	op := Op{By: userID}
	entryName := docName(content.RefEntry, draftEN)
	first := predictIDs(t, content.RefRevision)[0]
	revName := revisionName(first)
	feed := `{"schemaVersion":1,"title":"Feed","slug":"feed"}`
	about := `{"schemaVersion":1,"title":"About","slug":"about"}`
	noTitle := `{"schemaVersion":1,"slug":"x"}`
	noMedium := `{"schemaVersion":1,"title":"a","slug":"a","featuredImage":"` + testID(content.RefMedia, 3) + `"}`

	for _, tt := range []struct {
		name  string
		setup func(t *testing.T, ed *Editor, s *store.Store) // before the store is looked at
		call  func(t *testing.T, ed *Editor, s *store.Store) (Result, error)
		want  func(t *testing.T) string
		is    error
	}{
		{"an entry without revisions", nil, func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Publish(t.Context(), op, draftEN, entrySeq(t, s, draftEN), testID(content.RefRevision, 9))
		}, func(*testing.T) string { return entryName + ": has no revision to publish" }, nil},

		{"an entry that does not exist", nil, func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Publish(t.Context(), op, missing, 1, testID(content.RefRevision, 9))
		}, func(*testing.T) string { return "entries/" + missing + ".json: not found" }, fs.ErrNotExist},

		{"a missing title", func(t *testing.T, ed *Editor, s *store.Store) {
			saveRevision(t, ed, s, draftEN, noTitle)
		}, func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Publish(t.Context(), op, draftEN, entrySeq(t, s, draftEN), first)
		}, func(t *testing.T) string { return revName + ":1:1: /title: missing title" }, nil},

		{"a slug that is the path of a feed", func(t *testing.T, ed *Editor, s *store.Store) {
			saveRevision(t, ed, s, draftEN, feed)
		}, func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Publish(t.Context(), op, draftEN, entrySeq(t, s, draftEN), first)
		}, func(t *testing.T) string {
			return where(t, revName, feed, `"slug"`) + `/slug: output path "/en/feed/" is also that of the feed in en`
		}, nil},

		{"a slug that another page has", func(t *testing.T, ed *Editor, s *store.Store) {
			saveRevision(t, ed, s, draftEN, about)
		}, func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Publish(t.Context(), op, draftEN, entrySeq(t, s, draftEN), first)
		}, func(t *testing.T) string {
			return where(t, revName, about, `"slug"`) + `/slug: slug "about" is also used by ` + revisionName(aboutENRev)
		}, nil},

		{"a revision that the build refuses, which the store has", func(t *testing.T, ed *Editor, s *store.Store) {
			addStoredRevision(t, s, draftEN, first, noMedium)
		}, func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Publish(t.Context(), op, draftEN, entrySeq(t, s, draftEN), first)
		}, func(t *testing.T) string {
			return where(t, revName, noMedium, `"featuredImage"`) + `/featuredImage: media "` + testID(content.RefMedia, 3) + `" does not exist`
		}, nil},

		{"a malformed entry ID", nil, func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Publish(t.Context(), op, "ent_x", 1, first)
		}, func(*testing.T) string {
			return `invalid entry ID "ent_x": use "ent_" and a ULID (26 characters in upper case)`
		}, nil},

		{"a malformed revision ID", nil, func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Publish(t.Context(), op, draftEN, 1, "rev_x")
		}, func(*testing.T) string {
			return `invalid revision ID "rev_x": use "rev_" and a ULID (26 characters in upper case)`
		}, nil},

		{"a malformed user ID", nil, func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Publish(t.Context(), Op{By: "x"}, draftEN, 1, first)
		}, func(*testing.T) string {
			return `invalid user ID "x": use "usr_" and a ULID (26 characters in upper case)`
		}, nil},

		{"a malformed operation ID", nil, func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Publish(t.Context(), Op{By: userID, ID: "op_x"}, draftEN, 1, first)
		}, func(*testing.T) string {
			return `invalid operation ID "op_x": use "op_" and a ULID (26 characters in upper case)`
		}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			if tt.setup != nil {
				tt.setup(t, ed, s)
			}
			before, index := snapshot(t, s), indexOf(t, s)
			res, err := tt.call(t, ed, s)
			want := tt.want(t)
			if err == nil || tt.is != nil && !errors.Is(err, tt.is) || want != "" && err.Error() != want {
				t.Errorf("Publish() = %+v, %v\nwant the error %q wrapping %v", res, err, want, tt.is)
			}
			if res.Version != (store.Version{}) || res.Publishes {
				t.Errorf("Publish() returned the result %+v with its error", res)
			}
			t.Logf("the error: %v", err)
			sameStore(t, s, before, index, "Publish()")
		})
	}
}

// TestPublishErrorTexts checks the texts of the errors of the base, whose numbers a test table cannot know before it runs.
func TestPublishErrorTexts(t *testing.T) {
	ed, s := newSample(t)
	op := Op{By: userID}
	name := docName(content.RefEntry, draftEN)
	a := saveRevision(t, ed, s, draftEN, careersRevision).Revision
	b := saveRevision(t, ed, s, draftEN, careersRevision).Revision

	// A revision that is not the latest.
	before, index := snapshot(t, s), indexOf(t, s)
	seq := entrySeq(t, s, draftEN)
	_, err := ed.Publish(t.Context(), op, draftEN, seq, a)
	if want := name + ": changed since it was read: its latest revision is " + b + ", not " + a; !errors.Is(err, ErrConflict) || err == nil || err.Error() != want {
		t.Errorf("Publish() of the revision before the latest = %v, want %q wrapping ErrConflict", err, want)
	}
	sameStore(t, s, before, index, "Publish() of the revision before the latest")

	// A stale base.
	must(t)(ed.Archive(t.Context(), op, draftEN, seq))
	now := entrySeq(t, s, draftEN)
	before, index = snapshot(t, s), indexOf(t, s)
	_, err = ed.Publish(t.Context(), op, draftEN, seq, b)
	if want := fmt.Sprintf("%s: changed since it was read: it is at version %d, not %d", name, now, seq); !errors.Is(err, ErrConflict) || err == nil || err.Error() != want {
		t.Errorf("Publish() with a stale base = %v, want %q wrapping ErrConflict", err, want)
	}
	sameStore(t, s, before, index, "Publish() with a stale base")

	// A state that is not valid: the problem is in the bytes that the operation would write.
	entry := testID(content.RefEntry, 1)
	put(t, s, content.RefEntry, entry, storedEntry(entry, 200, content.TypePage, "xx", testID(content.RefTranslationGroup, 1), content.StatusDraft, ""))
	rev := testID(content.RefRevision, 1)
	addStoredRevision(t, s, entry, rev, careersRevision)
	before, index = snapshot(t, s), indexOf(t, s)
	_, err = ed.Publish(t.Context(), op, entry, entrySeq(t, s, entry), rev)
	written := published(t, read(t, s, content.RefEntry, entry).Data, rev)
	if want := where(t, docName(content.RefEntry, entry), written, `"language"`) +
		`/language: language "xx" is not in the site settings (use ja, en)`; err == nil || err.Error() != want {
		t.Errorf("Publish() of an entry with an unknown language = %v, want %q", err, want)
	}
	sameStore(t, s, before, index, "Publish() of an entry with an unknown language")
}

// TestPublishParity compares what Publish refuses with what the build refuses in a copy of the sample site in which the
// entry is published with the revision: the same lines, with the files, the pointers, the messages and the positions.
func TestPublishParity(t *testing.T) {
	op := Op{By: userID}
	for _, tt := range []struct {
		name string
		typ  string // of a new draft entry in en that gets the revision; "" for the draft entry draftEN of the sample site
		data string
		msg  string
	}{
		{"a slug that is the path of a feed", "", `{
  "schemaVersion": 1,
  "title": "Feed",
  "slug": "feed"
}`, `/slug: output path "/en/feed/" is also that of the feed in en`},
		{"a slug that another page has", "", `{
  "schemaVersion": 1,
  "title": "About",
  "slug": "about"
}`, `/slug: slug "about" is also used by ` + revisionName(aboutENRev)},
		{"a missing title", "", `{
  "schemaVersion": 1,
  "slug": "untitled"
}`, `/title: missing title`},
		{"a parent that is not published", content.TypePage, `{
  "schemaVersion": 1,
  "title": "Team",
  "slug": "team",
  "parent": "` + draftEN + `"
}`, `/parent: entry "` + draftEN + `" is not published`},
		// The revision that the build reads is the copy with the date, in the bytes that Publish would have written.
		{"a post without a date", content.TypePost, `{
  "schemaVersion": 1,
  "title": "New service again",
  "slug": "new-service"
}`, `/slug: slug "new-service" is also used by ` + revisionName("rev_01JC0000000000000000000001")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := newParity(t)
			entry := draftEN
			if tt.typ != "" {
				entry = newDraft(t, p.ed, tt.typ, "en")
			}
			ids := predictIDs(t, content.RefEntry, content.RefTranslationGroup, content.RefRevision, content.RefRevision)
			saved := saveRevision(t, p.ed, p.s, entry, tt.data)
			rev, data := saved.Revision, tt.data
			if tt.typ == content.TypePost {
				copied := decodeRevision(t, []byte(tt.data))
				copied.Date = testNowJST
				copiedData, err := copied.MarshalJSON()
				if err != nil {
					t.Fatal(err)
				}
				rev, data = ids[3], string(copiedData)
			}
			p.write(revisionName(rev), data)
			p.write(docName(content.RefEntry, entry), published(t, read(t, p.s, content.RefEntry, entry).Data, rev))
			p.base = snapshot(t, p.s)
			want := buildErrors(t, p.dir)
			res, err := p.ed.Publish(t.Context(), op, entry, p.seq(content.RefEntry, entry), saved.Revision)
			got := problemLines(t, err)
			if len(want) == 0 || !slices.Equal(got, want) || !strings.Contains(strings.Join(got, "\n"), tt.msg) {
				t.Errorf("the problems of Publish() (%+v):\n%s\nthe problems of the build:\n%s", res, strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			if snap := snapshot(t, p.s); snap != p.base {
				t.Errorf("Publish() has changed the store:\n%s\nwant\n%s", snap, p.base)
			}
			t.Logf("the problems:\n%s", strings.Join(got, "\n"))
		})
	}

	// What Publish accepts, the build accepts.
	t.Run("what the build accepts", func(t *testing.T) {
		p := newParity(t)
		saved := saveRevision(t, p.ed, p.s, draftEN, careersRevision)
		res := must(t)(p.ed.Publish(t.Context(), op, draftEN, p.seq(content.RefEntry, draftEN), saved.Revision))
		p.copyStored(content.RefEntry, draftEN)
		p.write(revisionName(saved.Revision), careersRevision)
		if got := buildErrors(t, p.dir); len(got) != 0 {
			t.Errorf("the build refuses what Publish() has published (%+v):\n%s", res, strings.Join(got, "\n"))
		}
	})

	// The same for a post that gets a date: the revision that the build reads is the one that Publish has added.
	t.Run("what the build accepts of a post without a date", func(t *testing.T) {
		p := newParity(t)
		entry := newDraft(t, p.ed, content.TypePost, "en")
		saved := must(t)(p.ed.SaveRevision(t.Context(), op, entry, "", []byte(`{"schemaVersion":1,"title":"Fresh","slug":"fresh"}`)))
		res := must(t)(p.ed.Publish(t.Context(), op, entry, p.seq(content.RefEntry, entry), saved.Revision))
		p.copyStored(content.RefEntry, entry)
		p.write(revisionName(res.Revision), string(revisionData(t, p.s, res.Revision)))
		if got := buildErrors(t, p.dir); len(got) != 0 {
			t.Errorf("the build refuses what Publish() has published (%+v):\n%s", res, strings.Join(got, "\n"))
		}
	})
}

// TestUnpublish makes published, scheduled and archived entries drafts.
func TestUnpublish(t *testing.T) {
	op := Op{By: userID}
	const redirect = "redirects/rdr_01JBD3A4B5C6D7E8F9G0H1J2K3.json"

	t.Run("a post", func(t *testing.T) {
		ed, s := newSample(t)
		before := read(t, s, content.RefEntry, postJA)
		if got := entryPath(t, ed, s, postJA); got != "/2026/10/26/新サービス/" {
			t.Fatalf("the page of the post is %q", got)
		}
		res, err := ed.Unpublish(t.Context(), op, postJA, before.Seq)
		if err != nil {
			t.Fatal(err)
		}
		// The redirect from /?p=123 goes to the post, and is left out of the build now.
		wantWarning := redirect + ":5:3: /to: entry " + `"` + postJA + `"` + " is not published; the redirect is left out"
		if res.Kind != content.RefEntry || res.ID != postJA || res.Status != content.StatusDraft || !res.Publishes || res.Revision != "" ||
			!slices.Equal(lines(res.Warnings), []string{wantWarning}) {
			t.Errorf("Unpublish() = %+v, want the entry a draft, which changes what is published, with the warning %q", res, wantWarning)
		}
		doc := read(t, s, content.RefEntry, postJA)
		if doc.Version != res.Version || doc.Seq <= before.Seq || doc.SavedBy != userID {
			t.Errorf("Document() = %+v, Unpublish() returned the version %+v after %d", doc.Version, res.Version, before.Seq)
		}
		if want := retired(t, before.Data, content.StatusDraft); string(doc.Data) != want {
			t.Errorf("the document is %s, want %s", doc.Data, want)
		}
		got := decodeEntry(t, doc.Data)
		if got.Status != content.StatusDraft || got.PublishedRevision != "" || got.ScheduledRevision != "" || got.ScheduledAt != "" {
			t.Errorf("the entry is %+v, want a draft without a published revision", got)
		}
		sum := summaryOf(t, s, postJA)
		if sum.Published != nil || sum.Entry.Status != content.StatusDraft || sum.Latest != "rev_01JBG3N6R0T3W6Y9C2E5G8J1K4" {
			t.Errorf("Summary() = %+v %+v, want a draft that keeps its revision as the latest", sum, sum.Entry)
		}
		if got := entryPath(t, ed, s, postJA); got != "" {
			t.Errorf("the draft has the page %q", got)
		}
		if got := revisionIDs(t, s, postJA); !slices.Equal(got, []string{"rev_01JBG3N6R0T3W6Y9C2E5G8J1K4"}) {
			t.Errorf("the revisions are %v, want the one that it had", got)
		}

		// A draft stays one, and saves nothing.
		after, index := snapshot(t, s), indexOf(t, s)
		again, err := ed.Unpublish(t.Context(), op, postJA, doc.Seq)
		if err != nil || again.Version != res.Version || again.Publishes || again.Status != content.StatusDraft || len(again.Warnings) != 0 {
			t.Errorf("the second Unpublish() = %+v, %v; want that nothing is changed", again, err)
		}
		sameStore(t, s, after, index, "the second Unpublish()")
	})

	t.Run("the front page", func(t *testing.T) {
		ed, s := newSample(t)
		before, index := snapshot(t, s), indexOf(t, s)
		_, err := ed.Unpublish(t.Context(), op, homeJA, entrySeq(t, s, homeJA))
		got := problemLines(t, err)
		want := where(t, settingsName, string(sampleFile(t, settingsName)), `"ja": "`+homeJA+`"`) +
			`/frontPage/ja: entry "` + homeJA + `" is not published`
		if len(got) != 1 || got[0] != want {
			t.Errorf("Unpublish() of the front page:\n%s\nwant\n%s", strings.Join(got, "\n"), want)
		}
		sameStore(t, s, before, index, "Unpublish() of the front page")
	})

	t.Run("the posts page", func(t *testing.T) {
		ed, s := newSample(t)
		const postsPage = "ent_01JBG3N6T2W5Y8B1D4F7H0K3M6"
		before, index := snapshot(t, s), indexOf(t, s)
		_, err := ed.Unpublish(t.Context(), op, postsPage, entrySeq(t, s, postsPage))
		got := problemLines(t, err)
		if len(got) != 1 || !strings.HasSuffix(got[0], `/postsPage/ja: entry "`+postsPage+`" is not published`) {
			t.Errorf("Unpublish() of the posts page = %q", got)
		}
		sameStore(t, s, before, index, "Unpublish() of the posts page")
	})

	t.Run("the parent of a published page", func(t *testing.T) {
		ed, s := newSample(t)
		child, rev := testID(content.RefEntry, 1), testID(content.RefRevision, 1)
		data := `{"schemaVersion":1,"title":"Child","slug":"child","parent":"` + servicesJA + `"}`
		put(t, s, content.RefEntry, child,
			storedEntry(child, maxNumber(t, s)+1, content.TypePage, "ja", testID(content.RefTranslationGroup, 1), content.StatusPublished, rev))
		addStoredRevision(t, s, child, rev, data)
		if got := entryPath(t, ed, s, child); got != "/services/child/" {
			t.Fatalf("the page of the child is %q", got)
		}
		before, index := snapshot(t, s), indexOf(t, s)
		_, err := ed.Unpublish(t.Context(), op, servicesJA, entrySeq(t, s, servicesJA))
		got := problemLines(t, err)
		want := where(t, revisionName(rev), data, `"parent"`) + `/parent: entry "` + servicesJA + `" is not published`
		if len(got) != 1 || got[0] != want {
			t.Errorf("Unpublish() of the parent:\n%s\nwant\n%s", strings.Join(got, "\n"), want)
		}
		sameStore(t, s, before, index, "Unpublish() of the parent")

		// Archiving it is refused in the same way.
		_, err = ed.Archive(t.Context(), op, servicesJA, entrySeq(t, s, servicesJA))
		if got := problemLines(t, err); len(got) != 1 || got[0] != want {
			t.Errorf("Archive() of the parent:\n%s\nwant\n%s", strings.Join(got, "\n"), want)
		}
		sameStore(t, s, before, index, "Archive() of the parent")

		// The child can go first.
		must(t)(ed.Unpublish(t.Context(), op, child, entrySeq(t, s, child)))
		must(t)(ed.Unpublish(t.Context(), op, servicesJA, entrySeq(t, s, servicesJA)))
	})

	t.Run("an archived entry", func(t *testing.T) {
		ed, s := newSample(t)
		archived := must(t)(ed.Archive(t.Context(), op, draftEN, entrySeq(t, s, draftEN)))
		res, err := ed.Unpublish(t.Context(), op, draftEN, archived.Version.Seq)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != content.StatusDraft || res.Publishes || res.Version.Seq <= archived.Version.Seq || len(res.Warnings) != 0 {
			t.Errorf("Unpublish() = %+v, want a draft that did not change what is published", res)
		}
		if got := summaryOf(t, s, draftEN).Entry.Status; got != content.StatusDraft {
			t.Errorf("the status is %q, want draft", got)
		}
	})

	t.Run("a draft", func(t *testing.T) {
		ed, s := newSample(t)
		before, index := snapshot(t, s), indexOf(t, s)
		cur := read(t, s, content.RefEntry, draftEN)
		res, err := ed.Unpublish(t.Context(), op, draftEN, cur.Seq)
		if err != nil || res.Version != cur.Version || res.Publishes || res.Status != content.StatusDraft || len(res.Warnings) != 0 {
			t.Errorf("Unpublish() of a draft = %+v, %v; want that nothing is changed", res, err)
		}
		sameStore(t, s, before, index, "Unpublish() of a draft")
	})
}

// TestArchive makes entries archived.
func TestArchive(t *testing.T) {
	op := Op{By: userID}

	t.Run("a published page", func(t *testing.T) {
		ed, s := newSample(t)
		before := read(t, s, content.RefEntry, contactJA)
		if got := entryPath(t, ed, s, contactJA); got != "/contact/" {
			t.Fatalf("the page is %q", got)
		}
		res, err := ed.Archive(t.Context(), op, contactJA, before.Seq)
		if err != nil {
			t.Fatal(err)
		}
		if res.Kind != content.RefEntry || res.ID != contactJA || res.Status != content.StatusArchived || !res.Publishes ||
			res.Revision != "" || len(res.Warnings) != 0 {
			t.Errorf("Archive() = %+v, want the entry archived, which changes what is published", res)
		}
		doc := read(t, s, content.RefEntry, contactJA)
		if doc.Version != res.Version || doc.Seq <= before.Seq {
			t.Errorf("Document() = %+v, Archive() returned the version %+v after %d", doc.Version, res.Version, before.Seq)
		}
		if want := retired(t, before.Data, content.StatusArchived); string(doc.Data) != want {
			t.Errorf("the document is %s, want %s", doc.Data, want)
		}
		sum := summaryOf(t, s, contactJA)
		if sum.Published != nil || sum.Entry.Status != content.StatusArchived || sum.Entry.PublishedRevision != "" || sum.Latest != contactRev {
			t.Errorf("Summary() = %+v %+v, want an archived entry that keeps its revision", sum, sum.Entry)
		}
		if got := entryPath(t, ed, s, contactJA); got != "" {
			t.Errorf("the archived page has the path %q", got)
		}

		// Archived again, it saves nothing.
		after, index := snapshot(t, s), indexOf(t, s)
		again, err := ed.Archive(t.Context(), op, contactJA, doc.Seq)
		if err != nil || again.Version != res.Version || again.Publishes || again.Status != content.StatusArchived {
			t.Errorf("the second Archive() = %+v, %v; want that nothing is changed", again, err)
		}
		sameStore(t, s, after, index, "the second Archive()")
	})

	t.Run("a draft", func(t *testing.T) {
		ed, s := newSample(t)
		before := read(t, s, content.RefEntry, draftEN)
		res, err := ed.Archive(t.Context(), op, draftEN, before.Seq)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != content.StatusArchived || res.Publishes || res.Version.Seq <= before.Seq {
			t.Errorf("Archive() = %+v, want an archived entry that did not change what is published", res)
		}
		if got := summaryOf(t, s, draftEN).Entry.Status; got != content.StatusArchived {
			t.Errorf("the status is %q, want archived", got)
		}
	})

	// An archived entry can be published again with its latest revision.
	t.Run("published again", func(t *testing.T) {
		ed, s := newSample(t)
		archived := must(t)(ed.Archive(t.Context(), op, contactJA, entrySeq(t, s, contactJA)))
		res, err := ed.Publish(t.Context(), op, contactJA, archived.Version.Seq, contactRev)
		if err != nil || res.Status != content.StatusPublished || !res.Publishes || len(res.Warnings) != 0 {
			t.Fatalf("Publish() = %+v, %v; want the entry published again", res, err)
		}
		if got := entryPath(t, ed, s, contactJA); got != "/contact/" {
			t.Errorf("the page is %q, want /contact/", got)
		}
	})

	t.Run("a redirect to the entry", func(t *testing.T) {
		ed, s := newSample(t)
		res, err := ed.Archive(t.Context(), op, aboutJA, entrySeq(t, s, aboutJA))
		if err != nil {
			t.Fatal(err)
		}
		// The redirect from /company/ goes to the page, and is left out of the build now.
		want := "redirects/rdr_01JC0000000000000000000001.json:5:3: /to: entry " + `"` + aboutJA + `"` + " is not published; the redirect is left out"
		if !slices.Equal(lines(res.Warnings), []string{want}) {
			t.Errorf("Archive() warnings:\n%s\nwant\n%s", strings.Join(lines(res.Warnings), "\n"), want)
		}
	})
}

// TestUnpublishParity compares what Unpublish and Archive refuse with what the build refuses in a copy of the sample site
// in which the entry is a draft.
func TestUnpublishParity(t *testing.T) {
	op := Op{By: userID}
	for _, to := range []content.Status{content.StatusDraft, content.StatusArchived} {
		for _, tt := range []struct {
			name  string
			entry string
			setup func(t *testing.T, p *parity)
		}{
			{"the front page", homeJA, nil},
			{"the parent of a published page", servicesJA, func(t *testing.T, p *parity) {
				child, rev := testID(content.RefEntry, 1), testID(content.RefRevision, 1)
				data := `{"schemaVersion":1,"title":"Child","slug":"child","parent":"` + servicesJA + `"}`
				entry := storedEntry(child, maxNumber(t, p.s)+1, content.TypePage, "ja", testID(content.RefTranslationGroup, 1), content.StatusPublished, rev)
				put(t, p.s, content.RefEntry, child, entry)
				addStoredRevision(t, p.s, child, rev, data)
				p.write(docName(content.RefEntry, child), entry)
				p.write(revisionName(rev), data)
				p.base = snapshot(t, p.s)
			}},
		} {
			t.Run(string(to)+"/"+tt.name, func(t *testing.T) {
				p := newParity(t)
				if tt.setup != nil {
					tt.setup(t, p)
				}
				p.write(docName(content.RefEntry, tt.entry), retired(t, read(t, p.s, content.RefEntry, tt.entry).Data, to))
				want := buildErrors(t, p.dir)
				var err error
				if to == content.StatusDraft {
					_, err = p.ed.Unpublish(t.Context(), op, tt.entry, p.seq(content.RefEntry, tt.entry))
				} else {
					_, err = p.ed.Archive(t.Context(), op, tt.entry, p.seq(content.RefEntry, tt.entry))
				}
				got := problemLines(t, err)
				if len(want) == 0 || !slices.Equal(got, want) {
					t.Errorf("the problems of the operation:\n%s\nthe problems of the build:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
				}
				if snap := snapshot(t, p.s); snap != p.base {
					t.Errorf("the operation has changed the store:\n%s\nwant\n%s", snap, p.base)
				}
			})
		}
	}
}

// TestPublishesFlag checks the Publishes and the Status of the results of the operations: Publishes is true for the
// operations that change what a build of the site publishes.
func TestPublishesFlag(t *testing.T) {
	op := Op{By: userID}
	redirect := redirectJSON("/old-contact/", "/contact/")
	settings := replaceOnce(t, string(sampleFile(t, settingsName)), "Webサイトの企画と制作", "Webサイトの企画・制作")
	news := replaceOnce(t, string(sampleFile(t, newsName)), "会社からのお知らせです。", "お知らせです。")
	png := testPNG(t, 64, 48, 21)
	const rdr = "rdr_01JBD3A4B5C6D7E8F9G0H1J2K3"

	for _, tt := range []struct {
		name      string
		call      func(t *testing.T, ed *Editor, s *store.Store) (Result, error)
		publishes bool
		status    content.Status
	}{
		{"Create", func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Create(t.Context(), op, content.RefRedirect, []byte(redirect))
		}, true, ""},
		{"Save", func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Save(t.Context(), op, content.RefTerm, newsID, read(t, s, content.RefTerm, newsID).Seq, []byte(news))
		}, true, ""},
		{"Save of the same bytes", func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			d := read(t, s, content.RefTerm, newsID)
			return ed.Save(t.Context(), op, content.RefTerm, newsID, d.Seq, d.Data)
		}, false, ""},
		{"SaveSettings", func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.SaveSettings(t.Context(), op, read(t, s, store.SettingsKind, "").Seq, []byte(settings))
		}, true, ""},
		{"SaveSettings of the same bytes", func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			d := read(t, s, store.SettingsKind, "")
			return ed.SaveSettings(t.Context(), op, d.Seq, d.Data)
		}, false, ""},
		{"Delete of a redirect", func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Delete(t.Context(), op, content.RefRedirect, rdr, read(t, s, content.RefRedirect, rdr).Seq)
		}, true, ""},
		{"Delete of an entry", func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			entry := newDraft(t, ed, content.TypePage, "ja")
			return ed.Delete(t.Context(), op, content.RefEntry, entry, entrySeq(t, s, entry))
		}, false, ""},
		{"CreateEntry", func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.CreateEntry(t.Context(), op, content.TypePage, "ja")
		}, false, ""},
		{"SaveRevision", func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.SaveRevision(t.Context(), op, draftEN, "", []byte(careersRevision))
		}, false, ""},
		{"SaveRevision of a published entry", func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.SaveRevision(t.Context(), op, aboutEN, aboutENRev, revisionData(t, s, aboutENRev))
		}, false, ""},
		{"CreateTranslation", func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.CreateTranslation(t.Context(), op, servicesJA, "en")
		}, false, ""},
		{"AddMedia", func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.AddMedia(t.Context(), op, bytes.NewReader(png), "new.png", "image/png")
		}, false, ""},
		{"ReplaceMedia", func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.ReplaceMedia(t.Context(), op, jpegMedium, read(t, s, content.RefMedia, jpegMedium).Seq, bytes.NewReader(png), "image/png")
		}, true, ""},
		{"Publish", func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			rev := saveRevision(t, ed, s, draftEN, careersRevision).Revision
			return ed.Publish(t.Context(), op, draftEN, entrySeq(t, s, draftEN), rev)
		}, true, content.StatusPublished},
		{"Publish of what is published", func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Publish(t.Context(), op, contactJA, entrySeq(t, s, contactJA), contactRev)
		}, false, content.StatusPublished},
		{"Unpublish of a published entry", func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Unpublish(t.Context(), op, contactJA, entrySeq(t, s, contactJA))
		}, true, content.StatusDraft},
		{"Unpublish of a draft", func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Unpublish(t.Context(), op, draftEN, entrySeq(t, s, draftEN))
		}, false, content.StatusDraft},
		{"Archive of a published entry", func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Archive(t.Context(), op, contactJA, entrySeq(t, s, contactJA))
		}, true, content.StatusArchived},
		{"Archive of a draft", func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			return ed.Archive(t.Context(), op, draftEN, entrySeq(t, s, draftEN))
		}, false, content.StatusArchived},
		{"Schedule", func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			rev := saveRevision(t, ed, s, draftEN, careersRevision).Revision
			return ed.Schedule(t.Context(), op, draftEN, entrySeq(t, s, draftEN), rev, careersAt)
		}, false, content.StatusScheduled},
		{"Schedule of what is scheduled", func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			rev := scheduleCareers(t, ed, s)
			return ed.Schedule(t.Context(), op, draftEN, entrySeq(t, s, draftEN), rev, careersAt)
		}, false, content.StatusScheduled},
		{"PublishScheduled", func(t *testing.T, ed *Editor, s *store.Store) (Result, error) {
			scheduleCareers(t, ed, s)
			return ed.PublishScheduled(t.Context(), draftEN, careersAt)
		}, true, content.StatusPublished},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			res, err := tt.call(t, ed, s)
			if err != nil {
				t.Fatal(err)
			}
			if res.Publishes != tt.publishes || res.Status != tt.status {
				t.Errorf("the result is %+v, want Publishes %t and the status %q", res, tt.publishes, tt.status)
			}
		})
	}
}

// TestPublishOperationID checks that a retry of an operation that sets a status returns the recorded result, with
// Publishes and Status, also when its base is no longer the version, and that another request with the same operation ID
// fails.
func TestPublishOperationID(t *testing.T) {
	type call func(t *testing.T, ed *Editor, s *store.Store, op Op, seq int64) (Result, error)
	archive := func(t *testing.T, ed *Editor, s *store.Store, op Op, seq int64) (Result, error) {
		return ed.Archive(t.Context(), op, contactJA, seq)
	}
	unpublish := func(t *testing.T, ed *Editor, s *store.Store, op Op, seq int64) (Result, error) {
		return ed.Unpublish(t.Context(), op, contactJA, seq)
	}
	for _, tt := range []struct {
		name   string
		entry  string
		do     call
		other  call // another request, which the same operation ID refuses
		record string
	}{
		{"Publish", draftEN, func(t *testing.T, ed *Editor, s *store.Store, op Op, seq int64) (Result, error) {
			return ed.Publish(t.Context(), op, draftEN, seq, summaryOf(t, s, draftEN).Latest)
		}, func(t *testing.T, ed *Editor, s *store.Store, op Op, seq int64) (Result, error) {
			return ed.Archive(t.Context(), op, draftEN, seq)
		}, `"publishes":true,"status":"published"`},
		{"Unpublish", contactJA, unpublish, archive, `"publishes":true,"status":"draft"`},
		{"Archive", contactJA, archive, unpublish, `"publishes":true,"status":"archived"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			if tt.entry == draftEN {
				saveRevision(t, ed, s, draftEN, careersRevision)
			}
			op := Op{By: userID, ID: testOpID(11)}
			seq := entrySeq(t, s, tt.entry)
			first, err := tt.do(t, ed, s, op, seq)
			if err != nil || !first.Publishes || first.Status == "" {
				t.Fatalf("%s() = %+v, %v; want a result that publishes, with a status", tt.name, first, err)
			}
			after, index := snapshot(t, s), indexOf(t, s)

			// The base is stale now; the retry gets the recorded result and nothing is done.
			again, err := tt.do(t, ed, s, op, seq)
			if err != nil || !reflect.DeepEqual(first, again) {
				t.Errorf("a retry of %s() = %+v, %v; want the first result %+v", tt.name, again, err, first)
			}
			sameStore(t, s, after, index, "a retry of "+tt.name+"()")

			// The record keeps Publishes and Status.
			err = s.View(t.Context(), func(tx *store.Tx) error {
				data, ok, err := tx.Operation(op.ID)
				if err != nil || !ok || !strings.Contains(string(data), tt.record) {
					t.Errorf("Operation() = %s, %t, %v; want the record with %s", data, ok, err, tt.record)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}

			// Another request with the same ID fails, whatever its base.
			_, err = tt.other(t, ed, s, op, first.Version.Seq)
			if want := fmt.Sprintf("operation ID %q was used for another request", op.ID); err == nil || err.Error() != want {
				t.Errorf("another request with the same operation ID = %v, want %q", err, want)
			}
			sameStore(t, s, after, index, "another request with the same operation ID")
		})
	}
}

// TestPublishLookupFailure checks that a failure to read the index fails the operations that set a status with that error,
// not with problems, and saves nothing.
func TestPublishLookupFailure(t *testing.T) {
	errRead := errors.New("the index cannot be read")
	op := Op{By: userID}
	// about is a new revision of a published page.
	about := replaceOnce(t, string(sampleFile(t, revisionName(aboutENRev))), `"title": "About us"`, `"title": "About the company"`)
	type revisions struct{ careers, about string }

	// fails says which of the readers fail the call; the others it works with. The sample site has the warning of a redirect to
	// a draft, which the site after a change has too unless the change publishes the draft, so the operations that leave it
	// make the site before the change to tell that it is old.
	for _, tt := range []struct {
		name  string
		call  func(t *testing.T, ed *Editor, s *store.Store, revs revisions) (Result, error)
		fails [3]bool // the summary of a document, the summaries of the documents, the summaries for the site before the change
	}{
		{"Publish", func(t *testing.T, ed *Editor, s *store.Store, revs revisions) (Result, error) {
			return ed.Publish(t.Context(), op, draftEN, entrySeq(t, s, draftEN), revs.careers)
		}, [3]bool{true, true, false}},
		{"Publish of a new revision of a page", func(t *testing.T, ed *Editor, s *store.Store, revs revisions) (Result, error) {
			return ed.Publish(t.Context(), op, aboutEN, entrySeq(t, s, aboutEN), revs.about)
		}, [3]bool{true, true, true}},
		{"Unpublish", func(t *testing.T, ed *Editor, s *store.Store, revs revisions) (Result, error) {
			return ed.Unpublish(t.Context(), op, contactJA, entrySeq(t, s, contactJA))
		}, [3]bool{true, true, true}},
		{"Archive", func(t *testing.T, ed *Editor, s *store.Store, revs revisions) (Result, error) {
			return ed.Archive(t.Context(), op, contactJA, entrySeq(t, s, contactJA))
		}, [3]bool{true, true, true}},
	} {
		for i, wrap := range []func() func(reader) reader{
			func() func(reader) reader { return failReads(0, failing{summary: errRead}) },
			func() func(reader) reader { return failReads(0, failing{summaries: errRead}) },
			func() func(reader) reader { return failReads(2, failing{summaries: errRead}) },
		} {
			t.Run(fmt.Sprintf("%s/%d", tt.name, i), func(t *testing.T) {
				ed, s := newSample(t)
				revs := revisions{
					careers: saveRevision(t, ed, s, draftEN, careersRevision).Revision,
					about:   saveRevision(t, ed, s, aboutEN, about).Revision,
				}
				ed.wrap = wrap()
				before := snapshot(t, s)
				res, err := tt.call(t, ed, s, revs)
				var problems content.Errors
				switch {
				case tt.fails[i]:
					if !errors.Is(err, errRead) || errors.As(err, &problems) || snapshot(t, s) != before {
						t.Errorf("%s() = %+v, %v; want the error of the read, not problems, and no change", tt.name, res, err)
					}
				case err != nil:
					t.Errorf("%s() error = %v, want none", tt.name, err)
				}
			})
		}
	}
}

// TestRecordResult checks the record of a result: it keeps Publishes, Status and Redirect, and the record of an operation
// that has none of them is what it was before they existed, so that the records that a store has still read.
func TestRecordResult(t *testing.T) {
	res := Result{
		Kind: content.RefEntry, ID: draftEN, Revision: testID(content.RefRevision, 1), Redirect: testID(content.RefRedirect, 1),
		Publishes: true, Status: content.StatusArchived,
		Version: store.Version{Seq: 7, Kind: content.RefEntry, ID: draftEN, Hash: "sha256:ab", SavedAt: testNow, SavedBy: userID},
	}
	if got := newRecordResult(res).result(); !reflect.DeepEqual(got, res) {
		t.Errorf("the result from its record is %+v, want %+v", got, res)
	}

	plain := res
	plain.Publishes, plain.Status, plain.Redirect = false, "", ""
	data, err := json.Marshal(record{Request: "sha256:00", Result: newRecordResult(plain)})
	if err != nil {
		t.Fatal(err)
	}
	if text := string(data); strings.Contains(text, "publishes") || strings.Contains(text, "status") || strings.Contains(text, "redirect") {
		t.Errorf("the record of a result without them is %s, want none", text)
	}
	rec, err := decodeRecord("op_x", data)
	if err != nil || !reflect.DeepEqual(rec.Result.result(), plain) {
		t.Errorf("decodeRecord() = %+v, %v; want the result %+v", rec.Result.result(), err, plain)
	}
}

// TestNotify checks Notify: it gets the result of each operation that has committed, and of no operation that failed or
// that a retry returned from its record, and it is called outside the Update.
func TestNotify(t *testing.T) {
	ed, s := newSample(t)
	op := Op{By: userID}
	var mu sync.Mutex
	var got []Result
	ed.Notify(func(r Result) {
		// A write from here would wait for itself if the Update were still running.
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		err := s.Update(ctx, func(tx *store.Tx) error {
			_, err := tx.Put(content.RefRedirect, testID(content.RefRedirect, 1), []byte(redirectJSON("/notified/", "/")), userID)
			return err
		})
		if err != nil {
			t.Errorf("the Update in the function of Notify = %v", err)
		}
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

	term := must(t)(ed.Create(t.Context(), op, content.RefTerm, []byte(termJSON("tag", "ja", "", "お祭り", "festival"))))
	if r := take(); len(r) != 1 || !reflect.DeepEqual(r[0], term) || !r[0].Publishes {
		t.Errorf("after Create() Notify got %+v, want %+v", r, term)
	}
	saved := saveRevision(t, ed, s, draftEN, careersRevision)
	if r := take(); len(r) != 1 || !reflect.DeepEqual(r[0], saved) || r[0].Publishes {
		t.Errorf("after SaveRevision() Notify got %+v, want %+v", r, saved)
	}
	published := must(t)(ed.Publish(t.Context(), op, draftEN, entrySeq(t, s, draftEN), saved.Revision))
	if r := take(); len(r) != 1 || !reflect.DeepEqual(r[0], published) || !r[0].Publishes || r[0].Status != content.StatusPublished {
		t.Errorf("after Publish() Notify got %+v, want %+v", r, published)
	}

	// An operation that saves nothing is called, with Publishes false.
	same := must(t)(ed.Publish(t.Context(), op, draftEN, published.Version.Seq, saved.Revision))
	if r := take(); len(r) != 1 || !reflect.DeepEqual(r[0], same) || r[0].Publishes {
		t.Errorf("after a Publish() that saved nothing Notify got %+v, want %+v", r, same)
	}

	// An operation that fails is not.
	if _, err := ed.Publish(t.Context(), op, draftEN, 1, saved.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("Publish() with a stale base = %v, want a conflict", err)
	}
	if _, err := ed.Create(t.Context(), op, content.RefRedirect, []byte(redirectJSON("old-contact", "/contact/"))); err == nil {
		t.Fatal("Create() of a redirect that does not validate has no error")
	}
	if r := take(); len(r) != 0 {
		t.Errorf("after operations that failed Notify got %+v, want none", r)
	}

	// A retry that returns the record is not, and the first call is.
	withID, seq := Op{By: userID, ID: testOpID(12)}, entrySeq(t, s, draftEN)
	first := must(t)(ed.Unpublish(t.Context(), withID, draftEN, seq))
	if r := take(); len(r) != 1 || !reflect.DeepEqual(r[0], first) || r[0].Status != content.StatusDraft {
		t.Errorf("after Unpublish() with an operation ID Notify got %+v, want %+v", r, first)
	}
	again := must(t)(ed.Unpublish(t.Context(), withID, draftEN, seq))
	if r := take(); len(r) != 0 || !reflect.DeepEqual(first, again) {
		t.Errorf("after the retry Notify got %+v and Unpublish() returned %+v, want none and %+v", r, again, first)
	}
}

// TestNotifyConcurrent checks that operations from several goroutines each call the function of Notify once.
func TestNotifyConcurrent(t *testing.T) {
	ed, _ := newSample(t)
	var mu sync.Mutex
	var ids []string
	ed.Notify(func(r Result) {
		mu.Lock()
		defer mu.Unlock()
		ids = append(ids, r.ID)
	})
	const goroutines = 8
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			from := fmt.Sprintf("/n-%d/", g)
			if _, err := ed.Create(t.Context(), Op{By: userID}, content.RefRedirect, []byte(redirectJSON(from, "/contact/"))); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	slices.Sort(ids)
	if len(ids) != goroutines || len(slices.Compact(ids)) != goroutines {
		t.Errorf("Notify got the IDs %v, want %d different ones", ids, goroutines)
	}
}

// TestSetClock checks that SetClock gives the times of the operations and of the IDs that they make.
func TestSetClock(t *testing.T) {
	ed, s := newSample(t)
	at := time.Date(2026, 12, 31, 16, 0, 30, 0, time.UTC) // 2027-01-01T01:00:30+09:00
	ed.SetClock(func() time.Time { return at })
	ids := predictIDsAt(t, at, content.RefEntry, content.RefTranslationGroup)
	res := must(t)(ed.CreateEntry(t.Context(), Op{By: userID}, content.TypePage, "ja"))
	if res.ID != ids[0] {
		t.Errorf("CreateEntry() made the ID %s, want %s, that of the clock", res.ID, ids[0])
	}
	if got := decodeEntry(t, read(t, s, content.RefEntry, res.ID).Data).CreatedAt; got != "2027-01-01T01:00:30+09:00" {
		t.Errorf("CreatedAt = %q, want 2027-01-01T01:00:30+09:00", got)
	}

	// The IDs keep growing when the clock goes back.
	ed.SetClock(func() time.Time { return at.Add(-time.Hour) })
	next := must(t)(ed.CreateEntry(t.Context(), Op{By: userID}, content.TypePage, "ja"))
	if next.ID <= res.ID {
		t.Errorf("the ID %s after the clock went back is not after %s", next.ID, res.ID)
	}
}

// TestPublishConcurrent checks that of several publishes with the same base, exactly one succeeds: the others find the
// entry changed.
func TestPublishConcurrent(t *testing.T) {
	ed, s := newSample(t)
	rev := saveRevision(t, ed, s, draftEN, careersRevision).Revision
	seq := entrySeq(t, s, draftEN)
	const goroutines = 8
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[g] = ed.Publish(t.Context(), Op{By: userID}, draftEN, seq, rev)
		}()
	}
	wg.Wait()
	var done, conflicts int
	for _, err := range errs {
		switch {
		case err == nil:
			done++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Errorf("Publish() = %v, want success or a conflict", err)
		}
	}
	if done != 1 || conflicts != goroutines-1 {
		t.Errorf("%d publishes succeeded and %d conflicted, want one and %d", done, conflicts, goroutines-1)
	}
	if got := summaryOf(t, s, draftEN).Entry.Status; got != content.StatusPublished {
		t.Errorf("the status is %q, want published", got)
	}
}

// TestPublishNoSettings checks the operations on a store that has no settings.
func TestPublishNoSettings(t *testing.T) {
	s := newStore(t)
	ed := testEditor(t, s)
	id, rev := testID(content.RefEntry, 1), testID(content.RefRevision, 1)
	put(t, s, content.RefEntry, id, storedEntry(id, 1, content.TypePage, "ja", testID(content.RefTranslationGroup, 1), content.StatusDraft, ""))
	addStoredRevision(t, s, id, rev, careersRevision)
	want := "site/settings.json: not found; save the settings first"
	seq := entrySeq(t, s, id)
	if _, err := ed.Publish(t.Context(), Op{By: userID}, id, seq, rev); !errors.Is(err, fs.ErrNotExist) || err == nil || err.Error() != want {
		t.Errorf("Publish() = %v, want %q", err, want)
	}
	if _, err := ed.Archive(t.Context(), Op{By: userID}, id, seq); !errors.Is(err, fs.ErrNotExist) || err == nil || err.Error() != want {
		t.Errorf("Archive() = %v, want %q", err, want)
	}
}

// Documents of the sample site that the tests of the redirects use.
const (
	postJARev   = "rev_01JBG3N6R0T3W6Y9C2E5G8J1K4" // the published revision of postJA, in the slug 新サービス
	postJAPath  = "/2026/10/26/新サービス/"             // the path of postJA
	postSlug    = `"slug": "新サービス"`
	encodedSlug = "%E6%96%B0%E3%82%B5%E3%83%BC%E3%83%93%E3%82%B9" // 新サービス as a server sends it
	postJAURL   = "/2026/10/26/" + encodedSlug + "/"              // postJAPath as a server sends it
	newsPageID  = "ent_01JBG3N6T2W5Y8B1D4F7H0K3M6"                // the posts page of ja, in the slug news
	newsPageRev = "rev_01JBG3N6T2W5Y8B1D4F7H0K3M6"
	launchPath  = "/2026/10/26/launch/"
)

// ideographicSpace is U+3000, which a slug can have and the canonical form of a from encodes.
var ideographicSpace = string(rune(0x3000))

// postRevision returns the revision of postJA that the sample site has, with old replaced by new.
func postRevision(t *testing.T, old, new string) string {
	t.Helper()
	return replaceOnce(t, string(sampleFile(t, revisionName(postJARev))), old, new)
}

// publishNext saves data as the next revision of the entry and publishes it.
func publishNext(t *testing.T, ed *Editor, s *store.Store, op Op, entry, data string) (Result, error) {
	t.Helper()
	saved := saveRevision(t, ed, s, entry, data)
	return ed.Publish(t.Context(), op, entry, entrySeq(t, s, entry), saved.Revision)
}

// redirectDoc returns the JSON of the redirect rd as MarshalJSON writes it.
func redirectDoc(t *testing.T, rd content.Redirect) string {
	t.Helper()
	data, err := rd.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// storedRedirects returns the redirects that s has, by ID.
func storedRedirects(t *testing.T, s *store.Store) map[string]content.Redirect {
	t.Helper()
	out := make(map[string]content.Redirect)
	err := s.View(t.Context(), func(tx *store.Tx) error {
		docs, err := tx.Documents(content.RefRedirect)
		if err != nil {
			return err
		}
		for _, d := range docs {
			var rd content.Redirect
			if err := rd.UnmarshalJSON(d.Data); err != nil {
				return fmt.Errorf("%s: %w", d.ID, err)
			}
			rd.ID = d.ID
			out[d.ID] = rd
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// lastSeq returns the version number of the last version of the document of kind with id, which is its deletion if it
// has been deleted.
func lastSeq(t *testing.T, s *store.Store, kind content.RefKind, id string) int64 {
	t.Helper()
	var seq int64
	err := s.View(t.Context(), func(tx *store.Tx) error {
		versions, err := tx.History(kind, id)
		if err == nil && len(versions) > 0 {
			seq = versions[len(versions)-1].Seq
		}
		return err
	})
	if err != nil || seq == 0 {
		t.Fatalf("History(%q, %q) = %d, %v; want versions", kind, id, seq, err)
	}
	return seq
}

// wantRedirects fails the test unless the redirects of s are those of before, without the IDs in removed and with those
// in added, which are in the store as MarshalJSON writes them and in its index.
func wantRedirects(t *testing.T, s *store.Store, before map[string]content.Redirect, added []content.Redirect, removed ...string) {
	t.Helper()
	want := make(map[string]content.Redirect)
	maps.Copy(want, before)
	for _, id := range removed {
		delete(want, id)
	}
	for _, rd := range added {
		want[rd.ID] = rd
		if got := read(t, s, content.RefRedirect, rd.ID); string(got.Data) != redirectDoc(t, rd) || got.SavedBy != userID {
			t.Errorf("the redirect %s is %s by %s, want %s by %s", rd.ID, got.Data, got.SavedBy, redirectDoc(t, rd), userID)
		}
	}
	if got := storedRedirects(t, s); !reflect.DeepEqual(got, want) {
		t.Errorf("the redirects are\n%+v\nwant\n%+v", got, want)
	}
	err := s.View(t.Context(), func(tx *store.Tx) error {
		sums, err := tx.Summaries(content.RefRedirect)
		if err != nil {
			return err
		}
		got := make(map[string]content.Redirect, len(sums))
		for _, sum := range sums {
			rd := *sum.Redirect
			rd.ID = sum.ID
			got[sum.ID] = rd
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("the index has the redirects\n%+v\nwant\n%+v", got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// exportSite returns a copy of the sample site directory that has the entries, the redirects and the published revisions
// of s, so that a build reads what s has.
func exportSite(t *testing.T, s *store.Store) string {
	t.Helper()
	dir := copySite(t)
	for _, sub := range []string{"entries", "redirects", "revisions"} {
		if err := os.RemoveAll(filepath.Join(dir, sub)); err != nil {
			t.Fatal(err)
		}
	}
	err := s.View(t.Context(), func(tx *store.Tx) error {
		for _, kind := range []content.RefKind{content.RefEntry, content.RefRedirect} {
			docs, err := tx.Documents(kind)
			if err != nil {
				return err
			}
			for _, d := range docs {
				writeSite(t, dir, docName(kind, d.ID), string(d.Data))
			}
		}
		sums, err := tx.Summaries(content.RefEntry)
		if err != nil {
			return err
		}
		for _, sum := range sums {
			if sum.Entry == nil || sum.Entry.PublishedRevision == "" {
				continue
			}
			rev, err := tx.Revision(sum.Entry.PublishedRevision)
			if err != nil {
				return err
			}
			writeSite(t, dir, revisionName(rev.ID), string(rev.Data))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// builtRedirect is a redirect in the server.json of a release.
type builtRedirect struct {
	From   string `json:"from"`
	To     string `json:"to"` // as a server sends it
	Status int    `json:"status"`
}

// builtRedirects builds the site directory dir, which the build must accept, and returns the redirects of the server.json
// of its release by their froms.
func builtRedirects(t *testing.T, dir string) map[string]builtRedirect {
	t.Helper()
	d, err := sitedir.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	out := t.TempDir()
	_, err = build.Build(t.Context(), build.Options{
		Source: source{Dir: d, theme: os.DirFS(themeDir)}, Out: out, CacheDir: t.TempDir(), Workers: 2,
	})
	if err != nil {
		t.Fatalf("the build refuses the site: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(out, "current", "server.json"))
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Redirects []builtRedirect `json:"redirects"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatal(err)
	}
	m := make(map[string]builtRedirect, len(list.Redirects))
	for _, r := range list.Redirects {
		m[r.From] = r
	}
	return m
}

// wantBuilt fails the test unless the build has the redirect from from to to with the status 301.
func wantBuilt(t *testing.T, built map[string]builtRedirect, from, to string) {
	t.Helper()
	if got, want := built[from], (builtRedirect{From: from, To: to, Status: 301}); got != want {
		t.Errorf("the build has the redirect %+v from %q, want %+v", got, from, want)
	}
}

// TestPublishRedirect checks the redirects that Publish registers from the old URL of a published entry whose URL it
// changes, and those from the new URL that it deletes: what the store and its index have, the warnings, and what the
// build does with the documents that the store has.
func TestPublishRedirect(t *testing.T) {
	op := Op{By: userID}

	// A new revision that changes the path of a published entry gets the old path a redirect to the entry.
	for _, tt := range []struct {
		name  string
		entry string
		data  string
		dated bool   // Publish adds a revision with the date, which makes an ID before the redirect's
		from  string // of the redirect
		path  string // of the page after
		to    string // as a server sends it, if a build of the documents is to have the redirect
	}{
		{"a changed slug", postJA, postRevision(t, postSlug, `"slug": "launch"`), false, postJAPath, launchPath, launchPath},
		{"a changed date", postJA, postRevision(t, `"date": "2026-10-26T00:30:00+09:00"`, `"date": "2026-10-27T00:30:00+09:00"`), false,
			postJAPath, "/2026/10/27/新サービス/", ""},
		{"a post that gets the date of the operation", postJA, postRevision(t, "  \"date\": \"2026-10-26T00:30:00+09:00\",\n", ""), true,
			postJAPath, "/2026/11/07/新サービス/", ""},
		{"a changed parent", servicesJA, replaceOnce(t, string(sampleFile(t, revisionName(servicesRev))),
			`"slug": "services",`, `"slug": "services",`+"\n  "+`"parent": "`+aboutJA+`",`), false,
			"/services/", "/about/services/", "/about/services/"},
		{"a changed slug of the posts page", newsPageID, replaceOnce(t, string(sampleFile(t, revisionName(newsPageRev))),
			`"slug": "news",`, `"slug": "blog",`), false, "/news/", "/blog/", "/blog/"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			kinds := []content.RefKind{content.RefRevision, content.RefRedirect}
			if tt.dated {
				kinds = []content.RefKind{content.RefRevision, content.RefRevision, content.RefRedirect}
			}
			ids := predictIDs(t, kinds...)
			before := storedRedirects(t, s)
			res, err := publishNext(t, ed, s, op, tt.entry, tt.data)
			if err != nil {
				t.Fatal(err)
			}
			want := content.Redirect{ID: ids[len(ids)-1], From: tt.from, To: tt.entry, Status: 301}
			if res.Revision != ids[len(ids)-2] || res.Redirect != want.ID || res.Status != content.StatusPublished || !res.Publishes ||
				len(res.Warnings) != 0 {
				t.Errorf("Publish() = %+v, want the revision %s published and the redirect %s registered, without warnings",
					res, ids[len(ids)-2], want.ID)
			}
			wantRedirects(t, s, before, []content.Redirect{want})
			if got := entryPath(t, ed, s, tt.entry); got != tt.path {
				t.Errorf("the page of the entry is %q, want %q", got, tt.path)
			}
			if tt.to == "" {
				return
			}
			built := builtRedirects(t, exportSite(t, s))
			wantBuilt(t, built, tt.from, tt.to)
			if tt.entry == postJA {
				// The redirect of the sample site to the post follows it too.
				wantBuilt(t, built, "/?p=123", tt.to)
			}
		})
	}

	t.Run("a path that stays", func(t *testing.T) {
		ed, s := newSample(t)
		before := storedRedirects(t, s)
		res, err := publishNext(t, ed, s, op, postJA, postRevision(t, `"title": "新サービスを始めました"`, `"title": "新サービスを開始しました"`))
		if err != nil || res.Redirect != "" || !res.Publishes || len(res.Warnings) != 0 {
			t.Errorf("Publish() = %+v, %v; want a new revision published without a redirect", res, err)
		}
		wantRedirects(t, s, before, nil)
	})

	// The path of the front page is that of its language, whatever its slug is.
	t.Run("the front page", func(t *testing.T) {
		ed, s := newSample(t)
		before := storedRedirects(t, s)
		res, err := publishNext(t, ed, s, op, homeJA, replaceOnce(t, string(sampleFile(t, revisionName(homeRev))), `"slug": "home",`, `"slug": "start",`))
		if err != nil || res.Redirect != "" || !res.Publishes || len(res.Warnings) != 0 {
			t.Errorf("Publish() = %+v, %v; want the revision published without a redirect", res, err)
		}
		wantRedirects(t, s, before, nil)
		built := builtRedirects(t, exportSite(t, s))
		wantBuilt(t, built, "/start/", "/")
		if got, ok := built["/home/"]; ok {
			t.Errorf("the build has the redirect %+v from /home/, which the front page has left", got)
		}
	})

	// Going back to the old path takes the redirect from it away, and the path that it leaves gets one.
	t.Run("back to the old slug", func(t *testing.T) {
		ed, s := newSample(t)
		ids := predictIDs(t, content.RefRevision, content.RefRedirect, content.RefRevision, content.RefRedirect)
		before := storedRedirects(t, s)
		first := must(t)(publishNext(t, ed, s, op, postJA, postRevision(t, postSlug, `"slug": "launch"`)))
		firstRedirect := content.Redirect{ID: ids[1], From: postJAPath, To: postJA, Status: 301}
		if first.Redirect != firstRedirect.ID {
			t.Fatalf("Publish() = %+v, want the redirect %s", first, firstRedirect.ID)
		}

		second, err := publishNext(t, ed, s, op, postJA, postRevision(t, postSlug, postSlug))
		if err != nil {
			t.Fatal(err)
		}
		secondRedirect := content.Redirect{ID: ids[3], From: launchPath, To: postJA, Status: 301}
		wantWarning := docName(content.RefRedirect, firstRedirect.ID) + `: deleted: its from is the new path "` + postJAPath + `" of ` + docName(content.RefEntry, postJA)
		if second.Redirect != secondRedirect.ID || !second.Publishes || !slices.Equal(lines(second.Warnings), []string{wantWarning}) {
			t.Errorf("Publish() = %+v, want the redirect %s and the warning %q", second, secondRedirect.ID, wantWarning)
		}
		wantRedirects(t, s, before, []content.Redirect{secondRedirect})
		if got := entryPath(t, ed, s, postJA); got != postJAPath {
			t.Errorf("the page of the entry is %q, want %q", got, postJAPath)
		}
		built := builtRedirects(t, exportSite(t, s))
		wantBuilt(t, built, launchPath, postJAURL)
		if got, ok := built[postJAPath]; ok {
			t.Errorf("the build has the redirect %+v from the path of the page", got)
		}
	})

	// The from is the canonical form of the path, which encodes the ideographic space of a slug, and the redirect that a slug
	// that goes back makes useless is found by it.
	t.Run("a slug with an ideographic space", func(t *testing.T) {
		ed, s := newSample(t)
		ids := predictIDs(t, content.RefRevision, content.RefRedirect, content.RefRevision, content.RefRedirect, content.RefRevision, content.RefRedirect)
		before := storedRedirects(t, s)
		slug := "新サービス" + ideographicSpace + "旧"
		spaced := postRevision(t, postSlug, `"slug": "`+slug+`"`)
		must(t)(publishNext(t, ed, s, op, postJA, spaced))
		second := must(t)(publishNext(t, ed, s, op, postJA, postRevision(t, postSlug, `"slug": "launch"`)))

		canonical := "/2026/10/26/新サービス%E3%80%80旧/"
		spacedRedirect := content.Redirect{ID: ids[3], From: canonical, To: postJA, Status: 301}
		if second.Redirect != spacedRedirect.ID || len(second.Warnings) != 0 {
			t.Fatalf("Publish() = %+v, want the redirect %s from %q", second, spacedRedirect.ID, canonical)
		}

		third, err := publishNext(t, ed, s, op, postJA, spaced)
		if err != nil {
			t.Fatal(err)
		}
		launchRedirect := content.Redirect{ID: ids[5], From: launchPath, To: postJA, Status: 301}
		wantWarning := docName(content.RefRedirect, spacedRedirect.ID) + `: deleted: its from is the new path "` + canonical + `" of ` + docName(content.RefEntry, postJA)
		if third.Redirect != launchRedirect.ID || !slices.Equal(lines(third.Warnings), []string{wantWarning}) {
			t.Errorf("Publish() = %+v, want the redirect %s and the warning %q", third, launchRedirect.ID, wantWarning)
		}
		first := content.Redirect{ID: ids[1], From: postJAPath, To: postJA, Status: 301}
		wantRedirects(t, s, before, []content.Redirect{first, launchRedirect})
		built := builtRedirects(t, exportSite(t, s))
		spacedURL := "/2026/10/26/" + encodedSlug + "%E3%80%80%E6%97%A7/"
		wantBuilt(t, built, postJAPath, spacedURL)
		wantBuilt(t, built, launchPath, spacedURL)
	})

	// Only the new revision of the entry registers a redirect: a term whose slug changes moves its list page and gives the
	// pages that use it other paths, and registers none.
	t.Run("other changes of URLs", func(t *testing.T) {
		ed, s := newSample(t)
		before := storedRedirects(t, s)
		news := read(t, s, content.RefTerm, newsID)
		data := replaceOnce(t, string(news.Data), `"slug": "news"`, `"slug": "headlines"`)
		res, err := ed.Save(t.Context(), op, content.RefTerm, newsID, news.Seq, []byte(data))
		if err != nil || res.Redirect != "" || !res.Publishes {
			t.Errorf("Save() = %+v, %v; want the term saved without a redirect", res, err)
		}
		wantRedirects(t, s, before, nil)
	})

	// Only the entry that was published gets one: the old path of an entry that is not published is not its path.
	t.Run("an entry that was not published", func(t *testing.T) {
		for _, tt := range []struct {
			name  string
			setup func(t *testing.T, ed *Editor, s *store.Store) string // returns the entry
		}{
			{"a draft", func(t *testing.T, ed *Editor, s *store.Store) string {
				must(t)(ed.Unpublish(t.Context(), op, contactJA, entrySeq(t, s, contactJA)))
				return contactJA
			}},
			{"an archived entry", func(t *testing.T, ed *Editor, s *store.Store) string {
				must(t)(ed.Archive(t.Context(), op, contactJA, entrySeq(t, s, contactJA)))
				return contactJA
			}},
			{"a scheduled entry", func(t *testing.T, ed *Editor, s *store.Store) string {
				id, rev := testID(content.RefEntry, 1), testID(content.RefRevision, 1)
				put(t, s, content.RefEntry, id, storedScheduled(t, id, maxNumber(t, s)+1, testID(content.RefTranslationGroup, 1), rev))
				addStoredRevision(t, s, id, rev, `{"schemaVersion":1,"title":"Soon","slug":"soon"}`)
				return id
			}},
		} {
			t.Run(tt.name, func(t *testing.T) {
				ed, s := newSample(t)
				entry := tt.setup(t, ed, s)
				before := storedRedirects(t, s)
				res, err := publishNext(t, ed, s, op, entry, `{"schemaVersion":1,"title":"Reach us","slug":"reach-us"}`)
				if err != nil || res.Redirect != "" || !res.Publishes || res.Status != content.StatusPublished || len(res.Warnings) != 0 {
					t.Errorf("Publish() = %+v, %v; want the entry published without a redirect", res, err)
				}
				wantRedirects(t, s, before, nil)
				if got := entryPath(t, ed, s, entry); got != "/reach-us/" {
					t.Errorf("the page of the entry is %q, want /reach-us/", got)
				}
			})
		}
	})
}

// jobsRevision gives the draft page draftEN of the sample site, in en, the slug jobs, which is the path /en/jobs/ that a
// redirect of the sample site is from.
const jobsRevision = `{"schemaVersion":1,"title":"Jobs","slug":"jobs"}`

// jobsSite returns an Editor and a store of the sample site that has, besides its redirect from /en/jobs/ to draftEN, five
// more to it from that path or without the slash at the end, and one from it with a query. removed are the redirects that
// the publish of jobsRevision deletes, in the order of their IDs.
func jobsSite(t *testing.T) (ed *Editor, s *store.Store, removed []string) {
	t.Helper()
	ed, s = newSample(t)
	removed = []string{"rdr_01JC0000000000000000000006"}
	for n := 1; n <= 5; n++ {
		id, from := testID(content.RefRedirect, n), "/en/jobs/"
		if n%2 == 1 {
			from = "/en/jobs"
		}
		put(t, s, content.RefRedirect, id, redirectDoc(t, content.Redirect{ID: id, From: from, To: draftEN, Status: 302}))
		removed = append(removed, id)
	}
	withQuery := testID(content.RefRedirect, 6)
	put(t, s, content.RefRedirect, withQuery, redirectDoc(t, content.Redirect{ID: withQuery, From: "/en/jobs/?ref=a", To: draftEN, Status: 301}))
	if _, ok := storedRedirects(t, s)[removed[0]]; !ok {
		t.Fatalf("the sample site has no redirect %s", removed[0])
	}
	return ed, s, removed
}

// TestPublishRedirectExisting checks Publish when redirects already have the froms that it would register or delete.
func TestPublishRedirectExisting(t *testing.T) {
	op := Op{By: userID}
	const elsewhere = "https://recruit.example.com/"
	other := testID(content.RefRedirect, 1)

	// A redirect from the path of a page duplicates its output path, which a save refuses, so it can only be an old problem
	// of the site. Moving the page takes the problem away; the redirect then stays, and the old path is not registered.
	t.Run("a redirect from the old path to elsewhere", func(t *testing.T) {
		ed, s := newSample(t)
		put(t, s, content.RefRedirect, other, redirectDoc(t, content.Redirect{ID: other, From: postJAPath, To: elsewhere, Status: 301}))
		old := buildErrors(t, exportSite(t, s))
		if len(old) != 1 || !strings.Contains(old[0], `/from: from "`+postJAPath+`" is the output path "`+postJAPath+`"`) {
			t.Fatalf("the build of the site as it is gives\n%s\nwant the duplicate of the output path", strings.Join(old, "\n"))
		}
		before := storedRedirects(t, s)
		saved := saveRevision(t, ed, s, postJA, postRevision(t, postSlug, `"slug": "launch"`))
		res, err := ed.Publish(t.Context(), op, postJA, entrySeq(t, s, postJA), saved.Revision)
		if err != nil {
			t.Fatal(err)
		}
		want := where(t, revisionName(saved.Revision), string(revisionData(t, s, saved.Revision)), `"slug"`) +
			`/slug: the redirect from "` + postJAPath + `" is not registered: ` + docName(content.RefRedirect, other) + ` has the same from`
		if res.Redirect != "" || !res.Publishes || !slices.Equal(lines(res.Warnings), []string{want}) {
			t.Errorf("Publish() = %+v, want no redirect and the warning %q", res, want)
		}
		wantRedirects(t, s, before, nil)
		wantBuilt(t, builtRedirects(t, exportSite(t, s)), postJAPath, elsewhere)
	})

	// Of the redirects that have the from, which differ in the slash at the end, the warning names the first by ID.
	t.Run("two redirects from the old path", func(t *testing.T) {
		ed, s := newSample(t)
		second := testID(content.RefRedirect, 2)
		put(t, s, content.RefRedirect, second, redirectDoc(t, content.Redirect{ID: second, From: strings.TrimSuffix(postJAPath, "/"), To: elsewhere, Status: 301}))
		put(t, s, content.RefRedirect, other, redirectDoc(t, content.Redirect{ID: other, From: postJAPath, To: elsewhere, Status: 301}))
		before := storedRedirects(t, s)
		saved := saveRevision(t, ed, s, postJA, postRevision(t, postSlug, `"slug": "launch"`))
		res, err := ed.Publish(t.Context(), op, postJA, entrySeq(t, s, postJA), saved.Revision)
		if err != nil {
			t.Fatal(err)
		}
		want := where(t, revisionName(saved.Revision), string(revisionData(t, s, saved.Revision)), `"slug"`) +
			`/slug: the redirect from "` + postJAPath + `" is not registered: ` + docName(content.RefRedirect, other) + ` has the same from`
		if res.Redirect != "" || !slices.Equal(lines(res.Warnings), []string{want}) {
			t.Errorf("Publish() = %+v, want no redirect and the warning %q", res, want)
		}
		wantRedirects(t, s, before, nil)
	})

	// The warning names the from as the redirect has it, in the canonical form, which encodes the ideographic space of a slug.
	t.Run("a redirect from the old path with an ideographic space", func(t *testing.T) {
		ed, s := newSample(t)
		spaced := postRevision(t, postSlug, `"slug": "新サービス`+ideographicSpace+`旧"`)
		must(t)(publishNext(t, ed, s, op, postJA, spaced))
		canonical := "/2026/10/26/新サービス%E3%80%80旧/"
		put(t, s, content.RefRedirect, other, redirectDoc(t, content.Redirect{ID: other, From: canonical, To: elsewhere, Status: 301}))
		before := storedRedirects(t, s)
		saved := saveRevision(t, ed, s, postJA, postRevision(t, postSlug, `"slug": "launch"`))
		res, err := ed.Publish(t.Context(), op, postJA, entrySeq(t, s, postJA), saved.Revision)
		if err != nil {
			t.Fatal(err)
		}
		want := where(t, revisionName(saved.Revision), string(revisionData(t, s, saved.Revision)), `"slug"`) +
			`/slug: the redirect from "` + canonical + `" is not registered: ` + docName(content.RefRedirect, other) + ` has the same from`
		if res.Redirect != "" || !slices.Equal(lines(res.Warnings), []string{want}) {
			t.Errorf("Publish() = %+v, want no redirect and the warning %q", res, want)
		}
		wantRedirects(t, s, before, nil)
	})

	// The revision that is published is the copy with the date that Publish makes, whose bytes are those that the warning is
	// located in.
	t.Run("a redirect from the old path of a post that gets the date", func(t *testing.T) {
		ed, s := newSample(t)
		put(t, s, content.RefRedirect, other, redirectDoc(t, content.Redirect{ID: other, From: postJAPath, To: elsewhere, Status: 301}))
		before := storedRedirects(t, s)
		res, err := publishNext(t, ed, s, op, postJA, postRevision(t, "  \"date\": \"2026-10-26T00:30:00+09:00\",\n", ""))
		if err != nil {
			t.Fatal(err)
		}
		copied := string(revisionData(t, s, res.Revision))
		want := where(t, revisionName(res.Revision), copied, `"slug"`) +
			`/slug: the redirect from "` + postJAPath + `" is not registered: ` + docName(content.RefRedirect, other) + ` has the same from`
		if line, _ := at(t, copied, `"slug"`); line != 1 || res.Redirect != "" || !slices.Equal(lines(res.Warnings), []string{want}) {
			t.Errorf("Publish() = %+v, want no redirect and the warning %q, in the first line of the revision it made", res, want)
		}
		wantRedirects(t, s, before, nil)
	})

	t.Run("a redirect from the old path to the entry", func(t *testing.T) {
		ed, s := newSample(t)
		put(t, s, content.RefRedirect, other, redirectDoc(t, content.Redirect{ID: other, From: postJAPath, To: postJA, Status: 301}))
		before := storedRedirects(t, s)
		res, err := publishNext(t, ed, s, op, postJA, postRevision(t, postSlug, `"slug": "launch"`))
		if err != nil || res.Redirect != "" || !res.Publishes || len(res.Warnings) != 0 {
			t.Errorf("Publish() = %+v, %v; want the revision published without a redirect or a warning", res, err)
		}
		wantRedirects(t, s, before, nil)
	})

	// A from with a query matches other requests than the old path does, so it neither stops the redirect nor goes with it.
	t.Run("a redirect from the old path with a query", func(t *testing.T) {
		ed, s := newSample(t)
		ids := predictIDs(t, content.RefRevision, content.RefRedirect, content.RefRevision, content.RefRedirect)
		withQuery := content.Redirect{ID: other, From: postJAPath + "?x=1", To: elsewhere, Status: 301}
		put(t, s, content.RefRedirect, other, redirectDoc(t, withQuery))
		before := storedRedirects(t, s)

		first := must(t)(publishNext(t, ed, s, op, postJA, postRevision(t, postSlug, `"slug": "launch"`)))
		oldPath := content.Redirect{ID: ids[1], From: postJAPath, To: postJA, Status: 301}
		if first.Redirect != oldPath.ID || len(first.Warnings) != 0 {
			t.Fatalf("Publish() = %+v, want the redirect %s without warnings", first, oldPath.ID)
		}
		wantRedirects(t, s, before, []content.Redirect{oldPath})
		built := builtRedirects(t, exportSite(t, s))
		wantBuilt(t, built, postJAPath, launchPath)
		wantBuilt(t, built, postJAPath+"?x=1", elsewhere)

		second := must(t)(publishNext(t, ed, s, op, postJA, postRevision(t, postSlug, postSlug)))
		launch := content.Redirect{ID: ids[3], From: launchPath, To: postJA, Status: 301}
		wantWarning := docName(content.RefRedirect, oldPath.ID) + `: deleted: its from is the new path "` + postJAPath + `" of ` + docName(content.RefEntry, postJA)
		if second.Redirect != launch.ID || !slices.Equal(lines(second.Warnings), []string{wantWarning}) {
			t.Errorf("Publish() = %+v, want the redirect %s and the warning %q", second, launch.ID, wantWarning)
		}
		wantRedirects(t, s, before, []content.Redirect{launch})
	})

	// The redirect of the sample site from /en/jobs/ goes to a draft page. The page gets the path when it is published for
	// the first time, which the redirects would duplicate; they go, also those that only differ in the slash at the end, and
	// the one with a query stays.
	t.Run("redirects from the new path to the entry", func(t *testing.T) {
		ed, s, removed := jobsSite(t)
		before := storedRedirects(t, s)
		res, err := publishNext(t, ed, s, op, draftEN, jobsRevision)
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		for _, id := range removed {
			want = append(want, docName(content.RefRedirect, id)+`: deleted: its from is the new path "/en/jobs/" of `+docName(content.RefEntry, draftEN))
		}
		if res.Redirect != "" || !res.Publishes || !slices.Equal(lines(res.Warnings), want) {
			t.Errorf("Publish() = %+v, want no redirect and the warnings\n%s", res, strings.Join(want, "\n"))
		}
		wantRedirects(t, s, before, nil, removed...)
		wantBuilt(t, builtRedirects(t, exportSite(t, s)), "/en/jobs/?ref=a", "/en/jobs/")
	})

	// They are deleted in the order of their IDs, so that the same documents give the same store. The order of a map is
	// only partly random for a map this small (a wrong order passes now and then), so the publish is tried again and again.
	t.Run("the order of the deletions", func(t *testing.T) {
		for range 10 {
			ed, s, removed := jobsSite(t)
			must(t)(publishNext(t, ed, s, op, draftEN, jobsRevision))
			for i := 1; i < len(removed); i++ {
				if a, b := lastSeq(t, s, content.RefRedirect, removed[i-1]), lastSeq(t, s, content.RefRedirect, removed[i]); a >= b {
					t.Fatalf("the redirect %s was deleted at %d and %s at %d, want them in the order of their IDs", removed[i-1], a, removed[i], b)
				}
			}
		}
	})

	// A redirect from the new path to another entry duplicates the output path of the page: the publish fails as the build
	// does, and the redirect stays.
	t.Run("a redirect to another entry from the new path", func(t *testing.T) {
		ed, s := newSample(t)
		put(t, s, content.RefRedirect, other, redirectDoc(t, content.Redirect{ID: other, From: "/en/careers/", To: aboutEN, Status: 301}))
		saved := saveRevision(t, ed, s, draftEN, careersRevision)
		snap, index := snapshot(t, s), indexOf(t, s)
		_, err := ed.Publish(t.Context(), op, draftEN, entrySeq(t, s, draftEN), saved.Revision)
		got := problemLines(t, err)

		dir := exportSite(t, s)
		writeSite(t, dir, docName(content.RefEntry, draftEN), published(t, read(t, s, content.RefEntry, draftEN).Data, saved.Revision))
		writeSite(t, dir, revisionName(saved.Revision), string(revisionData(t, s, saved.Revision)))
		want := buildErrors(t, dir)
		if len(want) != 1 || !slices.Equal(got, want) || !strings.Contains(got[0], `/from: from "/en/careers/" is the output path "/en/careers/" of `+revisionName(saved.Revision)) {
			t.Errorf("the problems of Publish():\n%s\nthe problems of the build:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		sameStore(t, s, snap, index, "Publish()")
	})

	// The redirect to the old path is in the model that the checks see: another page that has the path makes it a duplicate.
	t.Run("a page at the old path", func(t *testing.T) {
		ed, s := newSample(t)
		page, rev := testID(content.RefEntry, 1), testID(content.RefRevision, 1)
		put(t, s, content.RefEntry, page,
			storedEntry(page, maxNumber(t, s)+1, content.TypePost, "ja", testID(content.RefTranslationGroup, 1), content.StatusPublished, rev))
		addStoredRevision(t, s, page, rev, string(sampleFile(t, revisionName(postJARev))))
		ids := predictIDs(t, content.RefRevision, content.RefRedirect)
		saved := saveRevision(t, ed, s, postJA, postRevision(t, postSlug, `"slug": "launch"`))
		snap, index := snapshot(t, s), indexOf(t, s)
		_, err := ed.Publish(t.Context(), op, postJA, entrySeq(t, s, postJA), saved.Revision)
		got := problemLines(t, err)

		dir := exportSite(t, s)
		writeSite(t, dir, docName(content.RefEntry, postJA), published(t, read(t, s, content.RefEntry, postJA).Data, saved.Revision))
		writeSite(t, dir, revisionName(saved.Revision), string(revisionData(t, s, saved.Revision)))
		writeSite(t, dir, docName(content.RefRedirect, ids[1]), redirectDoc(t, content.Redirect{ID: ids[1], From: postJAPath, To: postJA, Status: 301}))
		want := buildErrors(t, dir)
		if len(want) != 1 || !slices.Equal(got, want) || !strings.HasPrefix(got[0], docName(content.RefRedirect, ids[1])+":1:") ||
			!strings.Contains(got[0], `/from: from "`+postJAPath+`" is the output path "`+postJAPath+`" of `+revisionName(rev)) {
			t.Errorf("the problems of Publish():\n%s\nthe problems of the build:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		sameStore(t, s, snap, index, "Publish()")
	})

	// A publish that fails does not delete the redirect that it was to delete.
	t.Run("a failure that keeps the redirect", func(t *testing.T) {
		const jobs = "rdr_01JC0000000000000000000006"
		ed, s := newSample(t)
		page, rev := testID(content.RefEntry, 1), testID(content.RefRevision, 1)
		put(t, s, content.RefEntry, page,
			storedEntry(page, maxNumber(t, s)+1, content.TypePage, "en", testID(content.RefTranslationGroup, 1), content.StatusPublished, rev))
		addStoredRevision(t, s, page, rev, `{"schemaVersion":1,"title":"Jobs","slug":"jobs"}`)
		saved := saveRevision(t, ed, s, draftEN, `{"schemaVersion":1,"title":"Jobs again","slug":"jobs"}`)
		snap, index := snapshot(t, s), indexOf(t, s)
		_, err := ed.Publish(t.Context(), op, draftEN, entrySeq(t, s, draftEN), saved.Revision)
		got := problemLines(t, err)
		if !strings.Contains(strings.Join(got, "\n"), `slug "jobs" is also used by`) {
			t.Errorf("the problems of Publish():\n%s\nwant the slug that two pages have", strings.Join(got, "\n"))
		}
		sameStore(t, s, snap, index, "Publish()")
		if _, ok := storedRedirects(t, s)[jobs]; !ok {
			t.Errorf("the redirect %s is gone after the publish that failed", jobs)
		}
	})
}

// TestPublishRedirectOperationID checks that a retry of a publish with an operation ID returns the redirect of the first
// call, and that the record of the operation keeps it.
func TestPublishRedirectOperationID(t *testing.T) {
	ed, s := newSample(t)
	saved := saveRevision(t, ed, s, postJA, postRevision(t, postSlug, `"slug": "launch"`))
	op := Op{By: userID, ID: testOpID(21)}
	seq := entrySeq(t, s, postJA)
	before := storedRedirects(t, s)
	first, err := ed.Publish(t.Context(), op, postJA, seq, saved.Revision)
	if err != nil || !strings.HasPrefix(first.Redirect, "rdr_") {
		t.Fatalf("Publish() = %+v, %v; want a redirect", first, err)
	}
	after, index := snapshot(t, s), indexOf(t, s)

	// The base is stale now: the retry gets the recorded result, and nothing is done.
	again, err := ed.Publish(t.Context(), op, postJA, seq, saved.Revision)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Errorf("a retry of Publish() = %+v, %v; want the first result %+v", again, err, first)
	}
	sameStore(t, s, after, index, "a retry of Publish()")
	wantRedirects(t, s, before, []content.Redirect{{ID: first.Redirect, From: postJAPath, To: postJA, Status: 301}})

	err = s.View(t.Context(), func(tx *store.Tx) error {
		data, ok, err := tx.Operation(op.ID)
		if want := `"redirect":"` + first.Redirect + `"`; err != nil || !ok || !strings.Contains(string(data), want) {
			t.Errorf("Operation() = %s, %t, %v; want the record with %s", data, ok, err, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestPublishRedirectCategory checks the redirect for an entry whose path changes with its first category, in a site whose
// posts have the category in their paths.
func TestPublishRedirectCategory(t *testing.T) {
	op := Op{By: userID}
	s := newStore(t)
	ed := testEditor(t, s)
	ed.schema = content.SiteFile{Name: "site/schema.yaml", Data: []byte("schemaVersion: 1\ntypes:\n  post:\n    url: /{category}/{slug}/\n")}
	const settings = `{
  "schemaVersion": 1,
  "name": { "ja": "サイト" },
  "url": "https://example.com",
  "timeZone": "Asia/Tokyo",
  "languages": ["ja"],
  "defaultLanguage": "ja"
}`
	must(t)(ed.SaveSettings(t.Context(), op, 0, []byte(settings)))
	news := must(t)(ed.Create(t.Context(), op, content.RefTerm, []byte(termJSON("category", "ja", "", "ニュース", "news"))))
	press := must(t)(ed.Create(t.Context(), op, content.RefTerm, []byte(termJSON("category", "ja", "", "プレス", "press"))))
	entry := newDraft(t, ed, content.TypePost, "ja")
	post := func(category string) string {
		return `{"schemaVersion":1,"title":"a","slug":"p","date":"2026-10-30T09:00:00+09:00","terms":{"category":["` + category + `"]}}`
	}
	first := must(t)(ed.SaveRevision(t.Context(), op, entry, "", []byte(post(news.ID))))
	firstPublish := must(t)(ed.Publish(t.Context(), op, entry, entrySeq(t, s, entry), first.Revision))
	if got := entryPath(t, ed, s, entry); got != "/news/p/" || firstPublish.Redirect != "" {
		t.Fatalf("the page is %q after Publish() = %+v, want /news/p/ without a redirect", got, firstPublish)
	}

	res, err := publishNext(t, ed, s, op, entry, post(press.ID))
	if err != nil {
		t.Fatal(err)
	}
	if got := entryPath(t, ed, s, entry); got != "/press/p/" {
		t.Errorf("the page of the entry is %q, want /press/p/", got)
	}
	want := content.Redirect{ID: res.Redirect, From: "/news/p/", To: entry, Status: 301}
	if !strings.HasPrefix(res.Redirect, "rdr_") || len(res.Warnings) != 0 {
		t.Fatalf("Publish() = %+v, want a redirect without warnings", res)
	}
	wantRedirects(t, s, nil, []content.Redirect{want})
}

// TestPublishBeforeOnce checks that Publish makes the model of the site before the change once for the old path of the
// entry and for the checks, which both need it: the sample site has an old warning, so the checks need it too.
func TestPublishBeforeOnce(t *testing.T) {
	ed, s := newSample(t)
	saved := saveRevision(t, ed, s, postJA, postRevision(t, postSlug, `"slug": "launch"`))
	runs := 0
	ed.wrap = func(r reader) reader {
		runs++
		return r
	}
	res, err := ed.Publish(t.Context(), Op{By: userID}, postJA, entrySeq(t, s, postJA), saved.Revision)
	if err != nil || res.Redirect == "" {
		t.Fatalf("Publish() = %+v, %v; want a redirect", res, err)
	}
	// The Update, and the View of the site before the change.
	if runs != 2 {
		t.Errorf("Publish() made %d runs of reads, want 2", runs)
	}
}

// failingSummaryFrom wraps a reader so that the reads of the summary of the entry fail with err from the call number from
// on, counting the calls in *calls; a from of 0 never fails.
type failingSummaryFrom struct {
	reader
	entry string
	from  int
	calls *int
	err   error
}

func (f failingSummaryFrom) Summary(kind content.RefKind, id string) (store.Summary, error) {
	if kind == content.RefEntry && id == f.entry {
		*f.calls++
		if f.from > 0 && *f.calls >= f.from {
			return store.Summary{}, f.err
		}
	}
	return f.reader.Summary(kind, id)
}

// TestPublishRedirectLookupFailure checks that a failure to read the summary of the entry for the redirect, the last read
// of it, fails Publish with that error, not with problems of the redirect, and saves nothing.
func TestPublishRedirectLookupFailure(t *testing.T) {
	errRead := errors.New("the index cannot be read")
	// publish publishes a new slug of postJA in a new sample site, with the reads of the summary of the entry in the Update
	// failing from the call number from on, and returns the number of calls.
	publish := func(t *testing.T, from int) (calls int, err error) {
		ed, s := newSample(t)
		saved := saveRevision(t, ed, s, postJA, postRevision(t, postSlug, `"slug": "launch"`))
		first := true
		ed.wrap = func(r reader) reader {
			if !first {
				return r
			}
			first = false
			return failingSummaryFrom{reader: r, entry: postJA, from: from, calls: &calls, err: errRead}
		}
		before, index := snapshot(t, s), indexOf(t, s)
		_, err = ed.Publish(t.Context(), Op{By: userID}, postJA, entrySeq(t, s, postJA), saved.Revision)
		if err != nil {
			sameStore(t, s, before, index, "Publish()")
		}
		return calls, err
	}
	calls, err := publish(t, 0)
	if err != nil || calls < 2 {
		t.Fatalf("Publish() made %d reads of the summary of the entry, with the error %v; want at least 2, and no error", calls, err)
	}
	_, err = publish(t, calls)
	var problems content.Errors
	if !errors.Is(err, errRead) || errors.As(err, &problems) {
		t.Errorf("Publish() = %v, want the error of the read, not problems", err)
	}
}
