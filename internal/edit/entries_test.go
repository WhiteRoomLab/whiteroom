package edit

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// Entries and terms of the sample site that the tests use.
const (
	aboutJA    = "ent_01JB8W2N5KD3F6H9J1M4P7R2T5" // a published page in ja, translated by aboutEN
	aboutEN    = "ent_01JB8Y6T2VS4W7Z2B5D8F1H6J9" // a published page in en
	servicesJA = "ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3" // a published page in ja without a translation
	contactJA  = "ent_01JBD1Y2Z3A4B5C6D7E8F9G0H1" // a published page in ja without a translation
	homeJA     = "ent_01JBG3N6S1V4X7Z0C3E6G9J2K5" // a published page in ja that links to servicesJA and postJA
	postJA     = "ent_01JB8Y6T2WT6X9C3E6G9J2K5M8" // a published post in ja, translated by postEN
	postEN     = "ent_01JC0000000000000000000001" // a published post in en
	draftEN    = "ent_01JC0000000000000000000003" // a draft page in en without revisions, which a menu and a redirect use

	aboutRev = "rev_01JB8X9Y3ZE5G8K1N4Q7S2V6W9" // the published revision of aboutJA

	tagJA = "trm_01JBA4C7G4J7M1P4R7T1W4Y7B1" // a tag in ja, translated by tagEN
	tagEN = "trm_01JBA4C7H5K8N3Q6S9V3X6Z9C2" // a tag in en

	// testNowJST is testNow in the time zone of the sample site.
	testNowJST = "2026-11-07T19:00:00+09:00"
)

func lines(es content.Errors) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Error())
	}
	return out
}

// newDraft makes a draft entry of the type typ in language with ed and returns its ID.
func newDraft(t *testing.T, ed *Editor, typ, language string) string {
	t.Helper()
	return must(t)(ed.CreateEntry(t.Context(), Op{By: userID}, typ, language)).ID
}

func revisionData(t *testing.T, s *store.Store, id string) []byte {
	t.Helper()
	var data []byte
	err := s.View(t.Context(), func(tx *store.Tx) error {
		r, err := tx.Revision(id)
		data = r.Data
		return err
	})
	if err != nil {
		t.Fatalf("Revision(%q) = %v", id, err)
	}
	return data
}

func summaryOf(t *testing.T, s *store.Store, id string) store.Summary {
	t.Helper()
	var sum store.Summary
	err := s.View(t.Context(), func(tx *store.Tx) (err error) {
		sum, err = tx.Summary(content.RefEntry, id)
		return err
	})
	if err != nil {
		t.Fatalf("Summary(%q) = %v", id, err)
	}
	return sum
}

// revisionIDs returns the IDs of the revisions of the entry id in the order of saving.
func revisionIDs(t *testing.T, s *store.Store, id string) []string {
	t.Helper()
	var ids []string
	err := s.View(t.Context(), func(tx *store.Tx) error {
		revs, err := tx.Revisions(id)
		for _, r := range revs {
			ids = append(ids, r.ID)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func maxNumber(t *testing.T, s *store.Store) int {
	t.Helper()
	var n int
	err := s.View(t.Context(), func(tx *store.Tx) error {
		sums, err := tx.Summaries(content.RefEntry)
		for _, sum := range sums {
			n = max(n, sum.Entry.Number)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// addStoredRevision adds the revision id to the entry with the store's own API, which validates nothing against the site.
func addStoredRevision(t *testing.T, s *store.Store, entry, id, data string) {
	t.Helper()
	err := s.Update(t.Context(), func(tx *store.Tx) error {
		_, err := tx.AddRevision(entry, id, []byte(data), userID)
		return err
	})
	if err != nil {
		t.Fatalf("AddRevision(%q, %q) = %v", entry, id, err)
	}
}

// storedEntry returns the JSON of an entry as the store's own API takes it, with the status and the published revision given.
func storedEntry(id string, number int, typ, language, group string, status content.Status, published string) string {
	e := content.Entry{
		ID: id, Number: number, Type: typ, Language: language, TranslationGroup: group, Status: status,
		PublishedRevision: published, CreatedAt: "2026-10-25T10:00:00+09:00", CreatedBy: userID,
	}
	data, err := e.MarshalJSON()
	if err != nil {
		panic(err)
	}
	return string(data)
}

func decodeRevision(t *testing.T, data []byte) content.Revision {
	t.Helper()
	var r content.Revision
	if err := r.UnmarshalJSON(data); err != nil {
		t.Fatalf("UnmarshalJSON(%s) = %v", data, err)
	}
	return r
}

func decodeEntry(t *testing.T, data []byte) content.Entry {
	t.Helper()
	var e content.Entry
	if err := e.UnmarshalJSON(data); err != nil {
		t.Fatalf("UnmarshalJSON(%s) = %v", data, err)
	}
	return e
}

// TestCreateEntry checks the entry that CreateEntry makes: the next number of the site, a group of its own, the time of the
// operation in the time zone of the site, a draft without a revision.
func TestCreateEntry(t *testing.T) {
	ed, s := newSample(t)
	ids := predictIDs(t, content.RefEntry, content.RefTranslationGroup)
	number := maxNumber(t, s) + 1
	res, err := ed.CreateEntry(t.Context(), Op{By: userID}, content.TypePost, "ja")
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != content.RefEntry || res.ID != ids[0] || res.Number != number || res.Revision != "" || len(res.Warnings) != 0 {
		t.Errorf("CreateEntry() = %+v, want the entry %s with the number %d and no revision", res, ids[0], number)
	}
	doc := read(t, s, content.RefEntry, res.ID)
	if doc.Version != res.Version || res.Version.SavedBy != userID || res.Version.Kind != content.RefEntry || res.Version.ID != res.ID {
		t.Errorf("Document() = %+v, CreateEntry() returned the version %+v", doc.Version, res.Version)
	}
	want := content.Entry{
		ID: ids[0], Number: number, Type: content.TypePost, Language: "ja", TranslationGroup: ids[1],
		Status: content.StatusDraft, CreatedAt: testNowJST, CreatedBy: userID,
	}
	if got := decodeEntry(t, doc.Data); got != want {
		t.Errorf("the entry is %+v, want %+v", got, want)
	}
	if again, err := want.MarshalJSON(); err != nil || !bytes.Equal(again, doc.Data) {
		t.Errorf("the stored bytes are %s, MarshalJSON writes %s, %v", doc.Data, again, err)
	}
	if sum := summaryOf(t, s, res.ID); *sum.Entry != want || sum.Latest != "" || sum.Published != nil {
		t.Errorf("Summary() = %+v %+v, want the entry %+v without revisions", sum, sum.Entry, want)
	}
	if revs := revisionIDs(t, s, res.ID); len(revs) != 0 {
		t.Errorf("the entry has the revisions %v", revs)
	}

	// The next entry has the next number and a group of its own, and the number of an entry that is deleted is not used again.
	second := must(t)(ed.CreateEntry(t.Context(), Op{By: userID}, content.TypePage, "en"))
	if second.Number != number+1 || second.ID <= res.ID {
		t.Errorf("the second CreateEntry() = %+v, want the number %d and a larger ID", second, number+1)
	}
	if a, b := decodeEntry(t, read(t, s, content.RefEntry, second.ID).Data), want; a.TranslationGroup == b.TranslationGroup {
		t.Errorf("two entries are in the group %s", a.TranslationGroup)
	}
	must(t)(ed.Delete(t.Context(), Op{By: userID}, content.RefEntry, second.ID, second.Version.Seq))
	if third := must(t)(ed.CreateEntry(t.Context(), Op{By: userID}, content.TypePage, "en")); third.Number != number+2 {
		t.Errorf("CreateEntry() after the deletion of the last entry has the number %d, want %d", third.Number, number+2)
	}
}

// TestCreateEntryTime checks the time: to the second, as it is in the time zone of the site, which is UTC here, and the
// first number of a site.
func TestCreateEntryTime(t *testing.T) {
	s, err := store.Create(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ed := testEditor(t, s)
	settings := `{
  "schemaVersion": 1,
  "name": { "ja": "サイト" },
  "url": "https://example.com",
  "timeZone": "UTC",
  "languages": ["ja"],
  "defaultLanguage": "ja"
}`
	must(t)(ed.SaveSettings(t.Context(), Op{By: userID}, 0, []byte(settings)))
	ed.now = func() time.Time { return testNow.Add(999 * time.Millisecond) }
	res := must(t)(ed.CreateEntry(t.Context(), Op{By: userID}, content.TypePage, "ja"))
	e := decodeEntry(t, read(t, s, content.RefEntry, res.ID).Data)
	if e.CreatedAt != "2026-11-07T10:00:00Z" || e.Number != 1 || res.Number != 1 {
		t.Errorf("CreatedAt = %q, number %d; want 2026-11-07T10:00:00Z (to the second) and the number 1", e.CreatedAt, e.Number)
	}
}

// TestCreateEntryErrors checks what CreateEntry refuses: its problems are in the document that it would make.
func TestCreateEntryErrors(t *testing.T) {
	ids := predictIDs(t, content.RefEntry, content.RefTranslationGroup)
	name := docName(content.RefEntry, ids[0])
	doc := func(typ, language string) string {
		e := content.Entry{
			ID: ids[0], Number: 137, Type: typ, Language: language, TranslationGroup: ids[1], Status: content.StatusDraft,
			CreatedAt: testNowJST, CreatedBy: userID,
		}
		data, err := e.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	noLanguage := `/language: language "xx" is not in the site settings (use ja, en)`
	for _, tt := range []struct {
		name          string
		op            Op
		typ, language string
		want          string
	}{
		{"an unknown type", Op{By: userID}, "nope", "ja", where(t, name, doc("nope", "ja"), `"type"`) + `/type: unknown type "nope"`},
		{"an unknown language", Op{By: userID}, content.TypePost, "xx", where(t, name, doc(content.TypePost, "xx"), `"language"`) + noLanguage},
		{"both", Op{By: userID}, "nope", "xx", where(t, name, doc("nope", "xx"), `"type"`) + `/type: unknown type "nope"` + "\n" +
			where(t, name, doc("nope", "xx"), `"language"`) + noLanguage},
		{"no type", Op{By: userID}, "", "ja", name + ":1:1: /type: missing type"},
		{"a malformed user ID", Op{By: "usr_x"}, content.TypePost, "ja", `invalid user ID "usr_x": use "usr_" and a ULID (26 characters in upper case)`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			before := snapshot(t, s)
			res, err := ed.CreateEntry(t.Context(), tt.op, tt.typ, tt.language)
			if err == nil || err.Error() != tt.want {
				t.Errorf("CreateEntry() = %+v, %v\nwant the error %q", res, err, tt.want)
			}
			if got := snapshot(t, s); got != before {
				t.Errorf("CreateEntry() has changed the store:\n%s\nwant\n%s", got, before)
			}
		})
	}
}

func TestCreateEntryNoSettings(t *testing.T) {
	s, err := store.Create(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	_, err = testEditor(t, s).CreateEntry(t.Context(), Op{By: userID}, content.TypePage, "ja")
	if !errors.Is(err, fs.ErrNotExist) || err == nil || err.Error() != "site/settings.json: not found; save the settings first" {
		t.Errorf("CreateEntry() on a store without settings = %v", err)
	}
}

// TestCreateEntryConcurrent checks that entries made at the same time have numbers of their own, one after another.
func TestCreateEntryConcurrent(t *testing.T) {
	ed, s := newSample(t)
	first := maxNumber(t, s) + 1
	const goroutines = 8
	numbers := make([]int, goroutines)
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := ed.CreateEntry(t.Context(), Op{By: userID}, content.TypePage, "ja")
			if err != nil {
				t.Error(err)
			}
			numbers[g] = res.Number
		}()
	}
	wg.Wait()
	slices.Sort(numbers)
	for i, n := range numbers {
		if n != first+i {
			t.Fatalf("the numbers are %v, want %d to %d", numbers, first, first+goroutines-1)
		}
	}
}

// TestSaveRevisionConcurrent checks that of several saves with the same base, exactly one succeeds: the others find the
// revision that it added and fail with ErrConflict.
func TestSaveRevisionConcurrent(t *testing.T) {
	ed, s := newSample(t)
	entry := newDraft(t, ed, content.TypePage, "ja")
	const goroutines = 8
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data := fmt.Sprintf(`{"schemaVersion":1,"title":"t%d","slug":"s%d"}`, g, g)
			_, errs[g] = ed.SaveRevision(t.Context(), Op{By: userID}, entry, "", []byte(data))
		}()
	}
	wg.Wait()
	var saved, conflicts int
	for _, err := range errs {
		switch {
		case err == nil:
			saved++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Errorf("SaveRevision() = %v, want success or a conflict", err)
		}
	}
	if got := revisionIDs(t, s, entry); saved != 1 || conflicts != goroutines-1 || len(got) != 1 {
		t.Errorf("%d saves succeeded and %d conflicted, and the entry has the revisions %v; want one, %d and one revision", saved, conflicts, got, goroutines-1)
	}
}

// TestCreateEntryOperationID checks that a retry of the operation returns the same entry, and that another request with the
// same operation ID fails.
func TestCreateEntryOperationID(t *testing.T) {
	ed, s := newSample(t)
	op := Op{By: userID, ID: testOpID(5)}
	first := must(t)(ed.CreateEntry(t.Context(), op, content.TypePost, "ja"))
	after := snapshot(t, s)
	again, err := ed.CreateEntry(t.Context(), op, content.TypePost, "ja")
	if err != nil || !reflect.DeepEqual(first, again) || snapshot(t, s) != after {
		t.Errorf("a retry of CreateEntry() = %+v, %v; want %+v and no change", again, err, first)
	}
	_, err = ed.CreateEntry(t.Context(), op, content.TypePage, "ja")
	if want := fmt.Sprintf("operation ID %q was used for another request", op.ID); err == nil || err.Error() != want {
		t.Errorf("CreateEntry() of another type with the same operation ID = %v, want %q", err, want)
	}
}

// TestSaveRevision adds revisions to an entry: the bytes that are stored, the base, and the result.
func TestSaveRevision(t *testing.T) {
	ed, s := newSample(t)
	op := Op{By: userID}
	ids := predictIDs(t, content.RefEntry, content.RefTranslationGroup, content.RefRevision, content.RefRevision)
	entry := newDraft(t, ed, content.TypePage, "ja")
	first := `{
  "schemaVersion": 1,
  "title": "新しいページ",
  "slug": "new-page",
  "body": [
    { "type": "paragraph", "id": "b_00000001", "content": [{ "type": "text", "text": "本文" }] }
  ]
}`
	entryDoc := read(t, s, content.RefEntry, entry)
	res, err := ed.SaveRevision(t.Context(), op, entry, "", []byte(first))
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != content.RefEntry || res.ID != entry || res.Revision != ids[2] || len(res.Warnings) != 0 || res.Number != 0 {
		t.Errorf("SaveRevision() = %+v, want the revision %s of %s without warnings", res, ids[2], entry)
	}
	// The entry document is as it was, and its version is in the result.
	if now := read(t, s, content.RefEntry, entry); now.Version != entryDoc.Version || res.Version != entryDoc.Version {
		t.Errorf("the entry is at %+v, SaveRevision() returned %+v, want %+v", now.Version, res.Version, entryDoc.Version)
	}
	// The bytes that were given are stored as they are, since normalizing changes nothing.
	if got := revisionData(t, s, res.Revision); string(got) != first {
		t.Errorf("the stored revision is %s, want the bytes that were given", got)
	}
	if sum := summaryOf(t, s, entry); sum.Latest != res.Revision || sum.Published != nil || sum.Entry.Status != content.StatusDraft {
		t.Errorf("Summary() = %+v, want the latest revision %s of a draft", sum, res.Revision)
	}
	var stored store.Revision
	err = s.View(t.Context(), func(tx *store.Tx) (err error) {
		stored, err = tx.Revision(res.Revision)
		return err
	})
	if err != nil || stored.Entry != entry || stored.SavedBy != userID {
		t.Errorf("Revision() = %+v, %v; want a revision of %s saved by %s", stored, err, entry, userID)
	}

	// The next revision has the first as its base.
	second := strings.Replace(first, "本文", "本文を書きました", 1)
	res2 := must(t)(ed.SaveRevision(t.Context(), op, entry, res.Revision, []byte(second)))
	if res2.Revision != ids[3] || summaryOf(t, s, entry).Latest != ids[3] {
		t.Errorf("the second SaveRevision() = %+v, want the revision %s", res2, ids[3])
	}
	if got := revisionIDs(t, s, entry); !slices.Equal(got, []string{ids[2], ids[3]}) {
		t.Errorf("the revisions are %v, want both in order", got)
	}
	if got := revisionData(t, s, ids[2]); string(got) != first {
		t.Errorf("the first revision is %s, want it as it was", got)
	}
}

// TestSaveRevisionOfAPublishedEntry saves a revision of an entry that is published: the entry is not changed, and the
// revision that it publishes stays.
func TestSaveRevisionOfAPublishedEntry(t *testing.T) {
	ed, s := newSample(t)
	before := read(t, s, content.RefEntry, aboutJA)
	old := string(revisionData(t, s, aboutRev))
	data := replaceOnce(t, old, `"title": "`, `"title": "新しい `)
	res, err := ed.SaveRevision(t.Context(), Op{By: userID}, aboutJA, aboutRev, []byte(data))
	if err != nil || len(res.Warnings) != 0 {
		t.Fatalf("SaveRevision() = %+v, %v; want no warnings", res, err)
	}
	sum := summaryOf(t, s, aboutJA)
	if sum.Latest != res.Revision || sum.Entry.PublishedRevision != aboutRev || sum.Published == nil {
		t.Errorf("Summary() = %+v %+v, want the latest revision %s and the published one %s", sum, sum.Entry, res.Revision, aboutRev)
	}
	if now := read(t, s, content.RefEntry, aboutJA); now.Version != before.Version || res.Version != before.Version {
		t.Errorf("the entry is at %+v, SaveRevision() returned %+v, want %+v", now.Version, res.Version, before.Version)
	}
}

// TestSaveRevisionErrors checks what SaveRevision refuses, and that it saves nothing.
func TestSaveRevisionErrors(t *testing.T) {
	const missing = "ent_01JBZ0000000000000000000E9"
	ids := predictIDs(t, content.RefEntry, content.RefTranslationGroup, content.RefRevision)
	revName := revisionName(ids[2])
	valid := `{"schemaVersion":1,"title":"a","slug":"a"}`
	lineBreak := `{"schemaVersion":1,"title":"a\nb","slug":"x"}`
	noMedium := `{"schemaVersion":1,"featuredImage":"` + testID(content.RefMedia, 3) + `"}`
	withDate := `{"schemaVersion":1,"date":"2026-10-25T10:00:00+09:00"}`
	user := Op{By: userID}
	otherRev := testID(content.RefRevision, 7)

	for _, tt := range []struct {
		name string
		call func(ed *Editor, entry string) (Result, error)
		want func(entry string) string
		is   error
	}{
		{"a stale base for an entry without revisions", func(ed *Editor, entry string) (Result, error) {
			return ed.SaveRevision(t.Context(), user, entry, otherRev, []byte(valid))
		}, func(entry string) string {
			return docName(content.RefEntry, entry) + ": changed since it was read: its latest revision is none, not " + otherRev
		}, ErrConflict},
		{"an entry that does not exist", func(ed *Editor, entry string) (Result, error) {
			return ed.SaveRevision(t.Context(), user, missing, "", []byte(valid))
		}, func(string) string { return "entries/" + missing + ".json: not found" }, fs.ErrNotExist},
		{"a malformed entry ID", func(ed *Editor, entry string) (Result, error) {
			return ed.SaveRevision(t.Context(), user, "ent_x", "", []byte(valid))
		}, func(string) string {
			return `invalid entry ID "ent_x": use "ent_" and a ULID (26 characters in upper case)`
		}, nil},
		{"a malformed base", func(ed *Editor, entry string) (Result, error) {
			return ed.SaveRevision(t.Context(), user, entry, "rev_x", []byte(valid))
		}, func(string) string {
			return `invalid revision ID "rev_x": use "rev_" and a ULID (26 characters in upper case)`
		}, nil},
		{"a malformed user ID", func(ed *Editor, entry string) (Result, error) {
			return ed.SaveRevision(t.Context(), Op{By: "x"}, entry, "", []byte(valid))
		}, func(string) string { return `invalid user ID "x": use "usr_" and a ULID (26 characters in upper case)` }, nil},
		{"not JSON", func(ed *Editor, entry string) (Result, error) {
			return ed.SaveRevision(t.Context(), user, entry, "", []byte("{\n  \"schemaVersion\": 1,\n  oops"))
		}, func(string) string { return revName + ":3:3: invalid character 'o' at start of value" }, nil},
		{"a title with a line break", func(ed *Editor, entry string) (Result, error) {
			return ed.SaveRevision(t.Context(), user, entry, "", []byte(lineBreak))
		}, func(string) string {
			return where(t, revName, lineBreak, `"title"`) + "/title: contains a line break or tab"
		}, nil},
		{"a medium that does not exist", func(ed *Editor, entry string) (Result, error) {
			return ed.SaveRevision(t.Context(), user, entry, "", []byte(noMedium))
		}, func(string) string {
			return where(t, revName, noMedium, `"featuredImage"`) + `/featuredImage: media "` + testID(content.RefMedia, 3) + `" does not exist`
		}, nil},
		{"an item that the type of the entry does not have", func(ed *Editor, entry string) (Result, error) {
			return ed.SaveRevision(t.Context(), user, entry, "", []byte(withDate))
		}, func(string) string { return where(t, revName, withDate, `"date"`) + "/date: not allowed for page" }, nil},
		{"64 MiB and one byte", func(ed *Editor, entry string) (Result, error) {
			return ed.SaveRevision(t.Context(), user, entry, "", bytes.Repeat([]byte(" "), content.MaxDocumentBytes+1))
		}, func(string) string { return revName + ": too large: 67108865 bytes (max 67108864)" }, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			entry := newDraft(t, ed, content.TypePage, "ja")
			before := snapshot(t, s)
			res, err := tt.call(ed, entry)
			if want := tt.want(entry); err == nil || err.Error() != want || tt.is != nil && !errors.Is(err, tt.is) {
				t.Errorf("SaveRevision() = %+v, %v\nwant the error %q wrapping %v", res, err, want, tt.is)
			}
			if got := snapshot(t, s); got != before {
				t.Errorf("SaveRevision() has changed the store:\n%s\nwant\n%s", got, before)
			}
		})
	}
}

// TestSaveRevisionConflict checks the base of a revision: it must be the latest.
func TestSaveRevisionConflict(t *testing.T) {
	ed, s := newSample(t)
	op := Op{By: userID}
	entry := newDraft(t, ed, content.TypePage, "ja")
	data := `{"schemaVersion":1,"title":"a","slug":"a"}`
	first := must(t)(ed.SaveRevision(t.Context(), op, entry, "", []byte(data)))
	name := docName(content.RefEntry, entry)

	// No base while there is a revision.
	_, err := ed.SaveRevision(t.Context(), op, entry, "", []byte(data))
	if want := name + ": changed since it was read: its latest revision is " + first.Revision + ", not none"; !errors.Is(err, ErrConflict) || err.Error() != want {
		t.Errorf("SaveRevision() without a base = %v, want %q wrapping ErrConflict", err, want)
	}

	// The first revision is not the latest after the second.
	second := must(t)(ed.SaveRevision(t.Context(), op, entry, first.Revision, []byte(data)))
	after := snapshot(t, s)
	_, err = ed.SaveRevision(t.Context(), op, entry, first.Revision, []byte(data))
	want := name + ": changed since it was read: its latest revision is " + second.Revision + ", not " + first.Revision
	if !errors.Is(err, ErrConflict) || err.Error() != want || snapshot(t, s) != after {
		t.Errorf("SaveRevision() with an old base = %v, want %q wrapping ErrConflict, and no change", err, want)
	}
	if got := revisionIDs(t, s, entry); !slices.Equal(got, []string{first.Revision, second.Revision}) {
		t.Errorf("the revisions are %v, want the first two", got)
	}
}

// TestSaveRevisionWarnings checks the items that only a revision to be published needs: they are warnings, located in the
// bytes that were given, and the revision is saved.
func TestSaveRevisionWarnings(t *testing.T) {
	ids := predictIDs(t, content.RefEntry, content.RefTranslationGroup, content.RefRevision)
	revName := revisionName(ids[2])
	op := Op{By: userID}
	for _, tt := range []struct {
		name string
		typ  string
		data string
		want []string
	}{
		{"no title", content.TypePage, `{"schemaVersion":1,"slug":"x"}`, []string{revName + ":1:1: /title: missing title"}},
		{"nothing", content.TypePage, `{"schemaVersion":1}`,
			[]string{revName + ":1:1: /slug: missing slug", revName + ":1:1: /title: missing title"}},
		{"a post without a date", content.TypePost, `{"schemaVersion":1,"title":"a","slug":"x"}`, []string{revName + ":1:1: /date: missing date"}},
		{"a required field", "works", `{"schemaVersion":1,"title":"a","slug":"x"}`, []string{revName + ":1:1: /fields/client: missing client"}},
		// The site is not checked as if the revision were published while it lacks what publishing needs: the slug is taken.
		{"a slug that is taken is not reported", content.TypePage, `{"schemaVersion":1,"slug":"about"}`,
			[]string{revName + ":1:1: /title: missing title"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			entry := newDraft(t, ed, tt.typ, "ja")
			res, err := ed.SaveRevision(t.Context(), op, entry, "", []byte(tt.data))
			if err != nil {
				t.Fatal(err)
			}
			if got := lines(res.Warnings); !slices.Equal(got, tt.want) {
				t.Errorf("SaveRevision() warnings:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
			if got := revisionData(t, s, res.Revision); string(got) != tt.data || summaryOf(t, s, entry).Latest != res.Revision {
				t.Errorf("the stored revision is %s, want the saved bytes that were given", got)
			}
		})
	}
}

// TestSaveRevisionKept checks the rules of a draft that depend on the previous revision: new legacy blocks, images without
// alt and styles that the theme does not allow are errors, and those of the latest revision are accepted as they were.
func TestSaveRevisionKept(t *testing.T) {
	op := Op{By: userID}
	const (
		legacy = `{"type":"legacy","id":"b_00000001","attrs":{"html":"<p>old</p>","reason":"imported"}}`
		image  = `{"type":"image","id":"b_00000002","attrs":{"media":"med_01JB8Z3K5QH2V6T9C4R7Y1E8B5"}}`
		red    = `{"type":"paragraph","id":"b_00000003","style":{"textColor":{"value":"#ff0000"}},"content":[{"type":"text","text":"x"}]}`
	)
	page := func(title, block string) string {
		return `{"schemaVersion":1,"title":"` + title + `","slug":"a","body":[` + block + `]}`
	}
	ids := predictIDs(t, content.RefEntry, content.RefTranslationGroup, content.RefRevision)
	revName := revisionName(ids[2])

	// The previous revision came by an import, which the store's own API stands for.
	for _, tt := range []struct {
		name    string
		block   string
		changed string // the block, changed
		key     string // the text at which the problem of the changed block is
		want    string
	}{
		{"a legacy block", legacy, strings.Replace(legacy, "old", "new", 1), `{"type":"legacy"`,
			"/body/0: legacy blocks can only be created by import"},
		{"an image without alt", image, strings.Replace(image, "QH2V6T9C4R7Y1E8B5", "RJ4W7Z1D5S8X2F6C9", 1), `"attrs"`,
			"/body/0/attrs/alt: missing alt; set decorative for a decorative image"},
		{"a style that the theme does not allow", red, strings.Replace(red, "#ff0000", "#00ff00", 1), `"value"`,
			"/body/0/style/textColor/value: the theme allows only presets for color"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			entry := newDraft(t, ed, content.TypePage, "ja")
			prev := testID(content.RefRevision, 1)
			addStoredRevision(t, s, entry, prev, page("a", tt.block))

			// Changed, it is refused.
			before := snapshot(t, s)
			data := page("a", tt.changed)
			want := where(t, revName, data, tt.key) + tt.want
			if _, err := ed.SaveRevision(t.Context(), op, entry, prev, []byte(data)); err == nil || err.Error() != want {
				t.Errorf("SaveRevision() of a changed block = %v, want %q", err, want)
			}
			if got := snapshot(t, s); got != before {
				t.Errorf("SaveRevision() has changed the store:\n%s\nwant\n%s", got, before)
			}

			// As it was, it is accepted, with another title, which the rules do not look at.
			res, err := ed.SaveRevision(t.Context(), op, entry, prev, []byte(page("b", tt.block)))
			if err != nil || len(res.Warnings) != 0 {
				t.Errorf("SaveRevision() of the block as it was = %+v, %v; want no problem", res, err)
			}
		})
	}
}

// TestSaveRevisionTheme checks the theme: the items and the presets that it has are accepted, the others are errors.
func TestSaveRevisionTheme(t *testing.T) {
	op := Op{By: userID}
	para := func(style string) string {
		return `{"schemaVersion":1,"title":"a","slug":"a","body":[{"type":"paragraph","id":"b_00000003","style":` + style +
			`,"content":[{"type":"text","text":"x"}]}]}`
	}
	ids := predictIDs(t, content.RefEntry, content.RefTranslationGroup, content.RefRevision)
	revName := revisionName(ids[2])
	for _, tt := range []struct {
		name, style, key, want string
	}{
		{"a preset", `{"textColor":{"preset":"primary"}}`, "", ""},
		{"a preset that the theme does not have", `{"textColor":{"preset":"nope"}}`, `"preset"`,
			`/body/0/style/textColor/preset: the theme has no color preset "nope"`},
		{"a value written directly", `{"textColor":{"value":"#ff0000"}}`, `"value"`,
			"/body/0/style/textColor/value: the theme allows only presets for color"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, _ := newSample(t)
			entry := newDraft(t, ed, content.TypePage, "ja")
			data := para(tt.style)
			res, err := ed.SaveRevision(t.Context(), op, entry, "", []byte(data))
			if tt.want == "" {
				if err != nil || len(res.Warnings) != 0 {
					t.Errorf("SaveRevision() = %+v, %v; want no problem", res, err)
				}
				return
			}
			if want := where(t, revName, data, tt.key) + tt.want; err == nil || err.Error() != want {
				t.Errorf("SaveRevision() = %+v, %v\nwant the error %q", res, err, want)
			}
		})
	}
}

// TestSaveRevisionNormalized checks, for revisions, that what is stored is what MarshalJSON writes if normalizing has
// changed the revision, and that the problems are located in the bytes that were given.
func TestSaveRevisionNormalized(t *testing.T) {
	ed, s := newSample(t)
	ids := predictIDs(t, content.RefEntry, content.RefTranslationGroup, content.RefRevision)
	entry := newDraft(t, ed, content.TypePage, "ja")
	// A slug in upper case is normalized, and then it is the slug of the page about: a warning that is at the slug that was
	// given, in line 4.
	data := "{\n  \"schemaVersion\": 1,\n  \"title\": \"概要\",\n  \"slug\": \"About\"\n}"
	res, err := ed.SaveRevision(t.Context(), Op{By: userID}, entry, "", []byte(data))
	if err != nil {
		t.Fatal(err)
	}
	want := where(t, revisionName(ids[2]), data, `"slug"`) + `/slug: slug "about" is also used by revisions/` + aboutRev + ".json"
	if got := lines(res.Warnings); !slices.Equal(got, []string{want}) || !strings.HasPrefix(want, revisionName(ids[2])+":4:3: ") {
		t.Errorf("SaveRevision() warnings = %q, want %q (at line 4)", got, want)
	}
	rev := decodeRevision(t, []byte(data))
	rev.Normalize(nil)
	wantBytes, err := rev.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if got := revisionData(t, s, res.Revision); !bytes.Equal(got, wantBytes) || rev.Slug != "about" {
		t.Errorf("the stored revision is %s, want %s", got, wantBytes)
	}
}

// TestSaveRevisionOperationID checks that a retry returns the same result and adds no revision.
func TestSaveRevisionOperationID(t *testing.T) {
	ed, s := newSample(t)
	entry := newDraft(t, ed, content.TypePage, "ja")
	op := Op{By: userID, ID: testOpID(6)}
	data := `{"schemaVersion":1,"slug":"x"}`
	first := must(t)(ed.SaveRevision(t.Context(), op, entry, "", []byte(data)))
	if len(first.Warnings) == 0 {
		t.Fatal("the revision has no warning to keep")
	}
	after := snapshot(t, s)
	again, err := ed.SaveRevision(t.Context(), op, entry, "", []byte(data))
	if err != nil || !reflect.DeepEqual(first, again) || snapshot(t, s) != after {
		t.Errorf("a retry of SaveRevision() = %+v, %v; want %+v and no change", again, err, first)
	}
	if got := revisionIDs(t, s, entry); len(got) != 1 {
		t.Errorf("the entry has the revisions %v, want one", got)
	}
}

// failSummaryOf makes the summary of one ID fail, which is a lookup of validation when it is not the entry of the operation.
type failSummaryOf struct {
	reader
	id  string
	err error
}

func (f failSummaryOf) Summary(kind content.RefKind, id string) (store.Summary, error) {
	if id == f.id {
		return store.Summary{}, f.err
	}
	return f.reader.Summary(kind, id)
}

// TestSaveRevisionLookupFailure checks that a failure of the lookup of a reference fails the operation with its error and
// not with problems of validation.
func TestSaveRevisionLookupFailure(t *testing.T) {
	ed, s := newSample(t)
	entry := newDraft(t, ed, content.TypePage, "ja")
	errRead := errors.New("the index cannot be read")
	ed.wrap = func(r reader) reader { return failSummaryOf{r, servicesJA, errRead} }
	before := snapshot(t, s)
	data := `{"schemaVersion":1,"title":"a","slug":"a","parent":"` + servicesJA + `"}`
	_, err := ed.SaveRevision(t.Context(), Op{By: userID}, entry, "", []byte(data))
	var problems content.Errors
	if !errors.Is(err, errRead) || errors.As(err, &problems) || snapshot(t, s) != before {
		t.Errorf("SaveRevision() = %v, want the error of the read, not problems, and no change", err)
	}
}

// published returns the JSON of the entry whose document is data, published with the revision rev.
func published(t *testing.T, data []byte, rev string) string {
	t.Helper()
	e := decodeEntry(t, data)
	e.Status, e.PublishedRevision = content.StatusPublished, rev
	out, err := e.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestSaveRevisionParity checks the warnings for the problems that publishing a revision would bring against the build: they
// are the lines of its error for a copy of the sample site in which the entry is published with the revision, with the same
// files, pointers, messages and positions.
func TestSaveRevisionParity(t *testing.T) {
	op := Op{By: userID}
	for _, tt := range []struct {
		name          string
		entry         string // the entry of the sample site that gets the revision, or "" for a new draft entry of typ and language
		typ, language string
		data          func(old string) string // the revision, from the one that the entry has; old is "" for a new entry
	}{
		{"a slug that another page has", "", content.TypePage, "ja", func(string) string {
			return `{
  "schemaVersion": 1,
  "title": "会社概要（新）",
  "slug": "about"
}`
		}},
		{"the output path of a redirect", "", content.TypePage, "ja", func(string) string {
			return `{
  "schemaVersion": 1,
  "title": "会社",
  "slug": "company"
}`
		}},
		{"a parent that is not published", "", content.TypePage, "en", func(string) string {
			return `{
  "schemaVersion": 1,
  "title": "Team",
  "slug": "team",
  "parent": "` + draftEN + `"
}`
		}},
		{"a post with the slug of another", "", content.TypePost, "ja", func(string) string {
			return `{
  "schemaVersion": 1,
  "title": "新サービス（再）",
  "slug": "新サービス",
  "date": "2026-10-26T00:30:00+09:00"
}`
		}},
		{"a new revision of a published page that takes the slug of a later page", aboutJA, "", "", func(old string) string {
			return replaceOnce(t, old, `"slug": "about"`, `"slug": "services"`)
		}},
		{"the draft that a redirect goes to, with the path of the redirect", draftEN, "", "", func(string) string {
			return `{
  "schemaVersion": 1,
  "title": "Jobs",
  "slug": "jobs"
}`
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := newParity(t)
			id, base, old := tt.entry, "", ""
			if id == "" {
				id = newDraft(t, p.ed, tt.typ, tt.language)
			} else if sum := summaryOf(t, p.s, id); sum.Latest != "" {
				base, old = sum.Latest, string(revisionData(t, p.s, sum.Latest))
			}
			data := tt.data(old)
			res, err := p.ed.SaveRevision(t.Context(), op, id, base, []byte(data))
			if err != nil {
				t.Fatal(err)
			}
			p.write(revisionName(res.Revision), data)
			p.write(docName(content.RefEntry, id), published(t, read(t, p.s, content.RefEntry, id).Data, res.Revision))
			got, want := lines(res.Warnings), buildErrors(t, p.dir)
			if len(want) == 0 || !slices.Equal(got, want) {
				t.Errorf("the warnings of SaveRevision():\n%s\nthe problems of the build:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			t.Logf("the problems:\n%s", strings.Join(got, "\n"))
		})
	}
}

// worksRevision is the JSON of a revision of a works entry with the client and the year, which keeps one value in every
// translation (translatable: false in the schema of the sample site).
func worksRevision(client, year string) string {
	return `{"schemaVersion":1,"title":"A","slug":"a","fields":{"client":"` + client + `","year":` + year + `}}`
}

// TestSaveRevisionSynced checks that a field that keeps one value in every translation, which differs from the value in
// the latest revision of another translation, is a warning, which is at the field in the bytes that were given; the values
// are not copied.
func TestSaveRevisionSynced(t *testing.T) {
	ed, s := newSample(t)
	op := Op{By: userID}
	ja := newDraft(t, ed, "works", "ja")
	first := must(t)(ed.SaveRevision(t.Context(), op, ja, "", []byte(worksRevision("C", "2020"))))
	if len(first.Warnings) != 0 {
		t.Fatalf("the first revision has the warnings %v", first.Warnings)
	}
	tr := must(t)(ed.CreateTranslation(t.Context(), op, ja, "en"))
	if len(tr.Warnings) != 0 {
		t.Fatalf("the translation has the warnings %v", tr.Warnings)
	}
	warning := func(revision, other, language, data string) string {
		return where(t, revisionName(revision), data, `"year"`) + "/fields/year: differs from the value in " + revisionName(other) +
			", the latest revision of the translation in " + language +
			"; a field with translatable: false has one value in every translation"
	}

	// A year that differs in the translation, which is saved as it is.
	data := worksRevision("C", "2021")
	second := must(t)(ed.SaveRevision(t.Context(), op, tr.ID, tr.Revision, []byte(data)))
	if got, want := lines(second.Warnings), []string{warning(second.Revision, first.Revision, "ja", data)}; !slices.Equal(got, want) {
		t.Errorf("SaveRevision() warnings:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got := revisionData(t, s, second.Revision); string(got) != data {
		t.Errorf("the stored revision is %s, want it as it was given", got)
	}
	if got := revisionData(t, s, first.Revision); string(got) != worksRevision("C", "2020") {
		t.Errorf("the revision of the other translation is %s, want it as it was", got)
	}

	// The same year in the other direction names the latest revision of the translation, which is the second.
	data = worksRevision("C", "2020")
	third := must(t)(ed.SaveRevision(t.Context(), op, ja, first.Revision, []byte(data)))
	if got, want := lines(third.Warnings), []string{warning(third.Revision, second.Revision, "en", data)}; !slices.Equal(got, want) {
		t.Errorf("SaveRevision() warnings:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// The same value written differently is the same, and a field that other translations do not have is a warning at
	// the nearest place that the bytes have: the object of the fields.
	data = worksRevision("C", "2021.0")
	if res := must(t)(ed.SaveRevision(t.Context(), op, ja, third.Revision, []byte(data))); len(res.Warnings) != 0 {
		t.Errorf("SaveRevision() of 2021.0 against 2021 has the warnings %v", res.Warnings)
	}
	latest := summaryOf(t, s, ja).Latest
	data = `{"schemaVersion":1,"title":"A","slug":"a","fields":{"client":"C"}}`
	res := must(t)(ed.SaveRevision(t.Context(), op, ja, latest, []byte(data)))
	want := where(t, revisionName(res.Revision), data, `"fields"`) + "/fields/year: differs from the value in " + revisionName(second.Revision) +
		", the latest revision of the translation in en; a field with translatable: false has one value in every translation"
	if got := lines(res.Warnings); !slices.Equal(got, []string{want}) {
		t.Errorf("SaveRevision() without the year has the warnings %q, want %q", got, want)
	}
}

// TestSaveRevisionSyncedType checks that the entries of another type in the group are not compared.
func TestSaveRevisionSyncedType(t *testing.T) {
	ed, s := newSample(t)
	w := newDraft(t, ed, "works", "ja")
	group := decodeEntry(t, read(t, s, content.RefEntry, w).Data).TranslationGroup
	other := testID(content.RefEntry, 5)
	put(t, s, content.RefEntry, other, storedEntry(other, 500, content.TypePost, "en", group, content.StatusDraft, ""))
	addStoredRevision(t, s, other, testID(content.RefRevision, 5), `{"schemaVersion":1,"title":"x","slug":"x","fields":{"year":1999}}`)
	res := must(t)(ed.SaveRevision(t.Context(), Op{By: userID}, w, "", []byte(worksRevision("C", "2020"))))
	if len(res.Warnings) != 0 {
		t.Errorf("SaveRevision() has the warnings %v, want none: the other entry of the group is a post", res.Warnings)
	}
}

// TestCreateTranslation makes the translations of entries of the sample site: the entry, and the copy of the latest revision
// with its links, terms and parent moved.
func TestCreateTranslation(t *testing.T) {
	op := Op{By: userID}
	t.Run("links", func(t *testing.T) {
		ed, s := newSample(t)
		ids := predictIDs(t, content.RefEntry, content.RefRevision)
		number := maxNumber(t, s) + 1
		homeBefore := read(t, s, content.RefEntry, homeJA)
		res, err := ed.CreateTranslation(t.Context(), op, homeJA, "en")
		if err != nil {
			t.Fatal(err)
		}
		if res.Kind != content.RefEntry || res.ID != ids[0] || res.Revision != ids[1] || res.Number != number {
			t.Fatalf("CreateTranslation() = %+v, want the entry %s with the revision %s and the number %d", res, ids[0], ids[1], number)
		}

		// The entry has the type and the translation group of the source, in the language, a draft.
		doc := read(t, s, content.RefEntry, res.ID)
		src := decodeEntry(t, homeBefore.Data)
		want := content.Entry{
			ID: ids[0], Number: number, Type: content.TypePage, Language: "en", TranslationGroup: src.TranslationGroup,
			Status: content.StatusDraft, CreatedAt: testNowJST, CreatedBy: userID,
		}
		if got := decodeEntry(t, doc.Data); got != want || doc.Version != res.Version {
			t.Errorf("the entry is %+v at %+v, want %+v at %+v", got, doc.Version, want, res.Version)
		}
		if again, err := want.MarshalJSON(); err != nil || !bytes.Equal(again, doc.Data) {
			t.Errorf("the stored entry is %s, MarshalJSON writes %s, %v", doc.Data, again, err)
		}
		// The source is as it was.
		if now := read(t, s, content.RefEntry, homeJA); now.Version != homeBefore.Version {
			t.Errorf("the source is at %+v, want %+v", now.Version, homeBefore.Version)
		}

		// The revision is the copy with the link to the post moved to its translation, in the form that MarshalJSON writes.
		rev := decodeRevision(t, revisionData(t, s, homeRev))
		rev.Body[2].Children[0].Text[1].Marks[0].Link.Entry = postEN
		wantRev, err := rev.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if got := revisionData(t, s, res.Revision); !bytes.Equal(got, wantRev) {
			t.Errorf("the revision is\n%s\nwant\n%s", got, wantRev)
		}
		if sum := summaryOf(t, s, res.ID); sum.Latest != res.Revision || sum.Published != nil {
			t.Errorf("Summary() = %+v, want the revision %s of a draft", sum, res.Revision)
		}
		if got := revisionIDs(t, s, res.ID); !slices.Equal(got, []string{res.Revision}) {
			t.Errorf("the revisions are %v", got)
		}

		// The link to the page without a translation is kept, with a warning at it in the new revision.
		link := `"entry":"` + servicesJA + `"`
		wantWarning := where(t, revisionName(res.Revision), string(wantRev), link) +
			"/body/1/content/1/content/0/attrs/link/entry: entry \"" + servicesJA + "\" has no translation in en; the link still goes to it"
		if got := lines(res.Warnings); !slices.Equal(got, []string{wantWarning}) {
			t.Errorf("CreateTranslation() warnings:\n%s\nwant\n%s", strings.Join(got, "\n"), wantWarning)
		}
	})

	t.Run("terms", func(t *testing.T) {
		const post, postRev = "ent_01JD0000000000000000000002", "rev_01JD0000000000000000000002"
		ed, s := newSample(t)
		res, err := ed.CreateTranslation(t.Context(), op, post, "en")
		if err != nil {
			t.Fatal(err)
		}
		// The tag has a translation, which is used, and the category has none, which is left out.
		rev := decodeRevision(t, revisionData(t, s, postRev))
		const news = "trm_01JBA4C7E2G5J8K1M4P7R2T5W8"
		if got := rev.Terms; len(got["category"]) != 1 || got["category"][0] != news || got["tag"][0] != tagJA {
			t.Fatalf("the terms of the source are %v", got)
		}
		rev.Terms = content.Terms{"tag": {tagEN}}
		wantRev, err := rev.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if got := revisionData(t, s, res.Revision); !bytes.Equal(got, wantRev) {
			t.Errorf("the revision is\n%s\nwant\n%s", got, wantRev)
		}
		wantWarning := where(t, revisionName(res.Revision), string(wantRev), `"terms"`) +
			`/terms/category/0: term "` + news + `" has no translation in en; it is left out`
		if got := lines(res.Warnings); !slices.Equal(got, []string{wantWarning}) {
			t.Errorf("CreateTranslation() warnings:\n%s\nwant\n%s", strings.Join(got, "\n"), wantWarning)
		}
		if e := decodeEntry(t, read(t, s, content.RefEntry, res.ID).Data); e.Type != content.TypePost || e.Language != "en" {
			t.Errorf("the entry is %+v, want a post in en", e)
		}
	})

	t.Run("the parent and the entry itself", func(t *testing.T) {
		ed, s := newSample(t)
		page := newDraft(t, ed, content.TypePage, "ja")
		data := func(parent string) string {
			return `{"schemaVersion":1,"title":"チーム","slug":"team","parent":"` + parent + `","body":[` +
				`{"type":"paragraph","id":"b_00000001","content":[` +
				`{"type":"text","text":"このページ","marks":[{"type":"link","attrs":{"entry":"` + page + `"}}]},` +
				`{"type":"text","text":"と連絡先","marks":[{"type":"link","attrs":{"entry":"` + contactJA + `"}}]}]}]}`
		}
		first := must(t)(ed.SaveRevision(t.Context(), op, page, "", []byte(data(aboutJA))))
		res, err := ed.CreateTranslation(t.Context(), op, page, "en")
		if err != nil {
			t.Fatal(err)
		}
		// The parent is the translation of the parent; the link to the page itself goes to its translation; the link to the
		// page that has none stays, with a warning.
		rev := decodeRevision(t, revisionData(t, s, first.Revision))
		rev.Parent = aboutEN
		rev.Body[0].Text[0].Marks[0].Link.Entry = res.ID
		wantRev, err := rev.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if got := revisionData(t, s, res.Revision); !bytes.Equal(got, wantRev) {
			t.Errorf("the revision is\n%s\nwant\n%s", got, wantRev)
		}
		wantWarning := where(t, revisionName(res.Revision), string(wantRev), `"entry":"`+contactJA+`"`) +
			"/body/0/content/1/marks/0/attrs/entry: entry \"" + contactJA + "\" has no translation in en; the link still goes to it"
		if got := lines(res.Warnings); !slices.Equal(got, []string{wantWarning}) {
			t.Errorf("CreateTranslation() warnings:\n%s\nwant\n%s", strings.Join(got, "\n"), wantWarning)
		}
	})

	t.Run("a parent that has no translation", func(t *testing.T) {
		ed, s := newSample(t)
		page := newDraft(t, ed, content.TypePage, "ja")
		first := must(t)(ed.SaveRevision(t.Context(), op, page, "", []byte(`{"schemaVersion":1,"title":"チーム","slug":"team","parent":"`+contactJA+`"}`)))
		res, err := ed.CreateTranslation(t.Context(), op, page, "en")
		if err != nil {
			t.Fatal(err)
		}
		rev := decodeRevision(t, revisionData(t, s, first.Revision))
		rev.Parent = ""
		wantRev, err := rev.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if got := revisionData(t, s, res.Revision); !bytes.Equal(got, wantRev) {
			t.Errorf("the revision is\n%s\nwant\n%s", got, wantRev)
		}
		// The parent is not in the new revision, so the warning is at its root.
		wantWarning := revisionName(res.Revision) + ":1:1: /parent: entry \"" + contactJA + "\" has no translation in en; the parent is left out"
		if got := lines(res.Warnings); !slices.Equal(got, []string{wantWarning}) {
			t.Errorf("CreateTranslation() warnings:\n%s\nwant\n%s", strings.Join(got, "\n"), wantWarning)
		}
	})
}

// homeRev is the revision of the home page in the sample site.
const homeRev = "rev_01JBG3N6S1V4X7Z0C3E6G9J2K5"

// TestCreateTranslationErrors checks what CreateTranslation refuses, and that it saves nothing.
func TestCreateTranslationErrors(t *testing.T) {
	const missing = "ent_01JBZ0000000000000000000E9"
	user := Op{By: userID}
	ids := predictIDs(t, content.RefEntry, content.RefRevision)
	// The entry that the Editor would make for the page about, translated into the language of its translation.
	aboutGroup := "tg_01JB8X4M7NB5D8F1H4J7K2M6P9"
	doc := func(language string) string {
		e := content.Entry{
			ID: ids[0], Number: 137, Type: content.TypePage, Language: language, TranslationGroup: aboutGroup, Status: content.StatusDraft,
			CreatedAt: testNowJST, CreatedBy: userID,
		}
		data, err := e.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	name := docName(content.RefEntry, ids[0])
	for _, tt := range []struct {
		name string
		call func(ed *Editor) (Result, error)
		want string
		is   error
	}{
		{"the language of the source", func(ed *Editor) (Result, error) {
			return ed.CreateTranslation(t.Context(), user, aboutJA, "ja")
		}, where(t, name, doc("ja"), `"language"`) + `/language: language "ja" is that of the entry it translates`, nil},
		{"a language that the site does not have", func(ed *Editor) (Result, error) {
			return ed.CreateTranslation(t.Context(), user, aboutJA, "xx")
		}, where(t, name, doc("xx"), `"language"`) + `/language: language "xx" is not in the site settings (use ja, en)`, nil},
		{"no language", func(ed *Editor) (Result, error) {
			return ed.CreateTranslation(t.Context(), user, aboutJA, "")
		}, name + ":1:1: /language: missing language", nil},
		{"a source that does not exist", func(ed *Editor) (Result, error) {
			return ed.CreateTranslation(t.Context(), user, missing, "en")
		}, "entries/" + missing + ".json: not found", fs.ErrNotExist},
		{"a malformed source", func(ed *Editor) (Result, error) {
			return ed.CreateTranslation(t.Context(), user, "ent_x", "en")
		}, `invalid entry ID "ent_x": use "ent_" and a ULID (26 characters in upper case)`, nil},
		{"a malformed user ID", func(ed *Editor) (Result, error) {
			return ed.CreateTranslation(t.Context(), Op{By: "x"}, aboutJA, "en")
		}, `invalid user ID "x": use "usr_" and a ULID (26 characters in upper case)`, nil},
		{"a source without a revision", func(ed *Editor) (Result, error) {
			return ed.CreateTranslation(t.Context(), user, draftEN, "ja")
		}, "entries/" + draftEN + ".json: has no revision to translate", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			before := snapshot(t, s)
			res, err := tt.call(ed)
			if err == nil || err.Error() != tt.want || tt.is != nil && !errors.Is(err, tt.is) {
				t.Errorf("CreateTranslation() = %+v, %v\nwant the error %q wrapping %v", res, err, tt.want, tt.is)
			}
			if got := snapshot(t, s); got != before {
				t.Errorf("CreateTranslation() has changed the store:\n%s\nwant\n%s", got, before)
			}
		})
	}
}

// TestCreateTranslationNoGroup checks that an entry without a translation group, which only the store's own API lets in,
// cannot be translated: a translation would join no group.
func TestCreateTranslationNoGroup(t *testing.T) {
	ed, s := newSample(t)
	id := testID(content.RefEntry, 2)
	put(t, s, content.RefEntry, id, `{"schemaVersion":1,"id":"`+id+`","number":502,"type":"page","language":"ja","status":"draft",`+
		`"createdAt":"2026-10-25T10:00:00+09:00","createdBy":"`+userID+`"}`)
	addStoredRevision(t, s, id, testID(content.RefRevision, 2), `{"schemaVersion":1,"title":"a","slug":"a"}`)
	before := snapshot(t, s)
	_, err := ed.CreateTranslation(t.Context(), Op{By: userID}, id, "en")
	if want := docName(content.RefEntry, id) + ": has no translation group"; err == nil || err.Error() != want || snapshot(t, s) != before {
		t.Errorf("CreateTranslation() = %v, want %q and no change", err, want)
	}
}

// TestCreateTranslationGroupTaken checks that a source whose translation group has an entry in the language already is an
// error, which is the build's for the site in which the entry that would be made exists.
func TestCreateTranslationGroupTaken(t *testing.T) {
	p := newParity(t)
	ids := predictIDs(t, content.RefEntry, content.RefRevision)
	_, err := p.ed.CreateTranslation(t.Context(), Op{By: userID}, postJA, "en")
	got := problemLines(t, err)
	if len(got) != 1 || !strings.Contains(got[0], `/translationGroup: translation group "tg_01JBG3N6Q9S2V5X8Z1B4D7F0H3" already has an entry in the language "en": `+docName(content.RefEntry, postEN)) {
		t.Fatalf("CreateTranslation() = %q, want the error of the translation group", got)
	}
	// The build, for a copy of the site that has the draft entry that the Editor would have made.
	e := content.Entry{
		ID: ids[0], Number: maxNumber(t, p.s) + 1, Type: content.TypePost, Language: "en", TranslationGroup: "tg_01JBG3N6Q9S2V5X8Z1B4D7F0H3",
		Status: content.StatusDraft, CreatedAt: testNowJST, CreatedBy: userID,
	}
	data, err := e.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	p.write(docName(content.RefEntry, ids[0]), string(data))
	if want := buildErrors(t, p.dir); !slices.Equal(got, want) {
		t.Errorf("the error of CreateTranslation():\n%s\nthe error of the build:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if snap := snapshot(t, p.s); snap != p.base {
		t.Errorf("CreateTranslation() has changed the store:\n%s\nwant\n%s", snap, p.base)
	}
}

// TestCreateTranslationChecks checks that the revision of a translation is checked as the revision that SaveRevision gets: the
// errors of validation fail the operation, the items that publishing needs and what publishing would bring are warnings.
func TestCreateTranslationChecks(t *testing.T) {
	op := Op{By: userID}
	// A source whose revision came by the store's own API: with no title; and with a title with a line break.
	source := func(t *testing.T, s *store.Store, n int, data string) string {
		id, group := testID(content.RefEntry, n), testID(content.RefTranslationGroup, n)
		put(t, s, content.RefEntry, id, storedEntry(id, 500+n, content.TypePage, "ja", group, content.StatusDraft, ""))
		addStoredRevision(t, s, id, testID(content.RefRevision, n), data)
		return id
	}

	t.Run("a title that is missing", func(t *testing.T) {
		ed, s := newSample(t)
		id := source(t, s, 1, `{"schemaVersion":1,"slug":"x"}`)
		res, err := ed.CreateTranslation(t.Context(), op, id, "en")
		if err != nil {
			t.Fatal(err)
		}
		want := revisionName(res.Revision) + ":1:1: /title: missing title"
		if got := lines(res.Warnings); !slices.Equal(got, []string{want}) {
			t.Errorf("CreateTranslation() warnings = %q, want %q", got, want)
		}
	})

	t.Run("an error of validation", func(t *testing.T) {
		ed, s := newSample(t)
		id := source(t, s, 2, `{"schemaVersion":1,"title":"a\nb","slug":"x"}`)
		before := snapshot(t, s)
		ids := predictIDs(t, content.RefEntry, content.RefRevision)
		copied := `{"schemaVersion":1,"title":"a\nb","slug":"x"}`
		_, err := ed.CreateTranslation(t.Context(), op, id, "en")
		want := where(t, revisionName(ids[1]), copied, `"title"`) + "/title: contains a line break or tab"
		if err == nil || err.Error() != want {
			t.Errorf("CreateTranslation() = %v, want the error %q", err, want)
		}
		if got := snapshot(t, s); got != before {
			t.Errorf("CreateTranslation() has changed the store:\n%s\nwant\n%s", got, before)
		}
	})

	t.Run("a slug that a published page has", func(t *testing.T) {
		ed, s := newSample(t)
		// A published page in en, which only the store's own API can make, has the slug of the page that is translated.
		other, rev := testID(content.RefEntry, 8), testID(content.RefRevision, 8)
		put(t, s, content.RefEntry, other, storedEntry(other, 508, content.TypePage, "en", testID(content.RefTranslationGroup, 8), content.StatusPublished, rev))
		addStoredRevision(t, s, other, rev, `{"schemaVersion":1,"title":"Contact","slug":"contact"}`)
		ids := predictIDs(t, content.RefEntry, content.RefRevision)
		res, err := ed.CreateTranslation(t.Context(), op, contactJA, "en")
		if err != nil {
			t.Fatal(err)
		}
		copied := string(revisionData(t, s, res.Revision))
		want := where(t, revisionName(ids[1]), copied, `"slug"`) + `/slug: slug "contact" is also used by ` + revisionName(rev)
		if got := lines(res.Warnings); !slices.Equal(got, []string{want}) {
			t.Errorf("CreateTranslation() warnings:\n%s\nwant\n%s", strings.Join(got, "\n"), want)
		}
	})

	t.Run("a legacy block, an image without alt and a style that the theme does not allow in the source", func(t *testing.T) {
		ed, s := newSample(t)
		blocks := `[` +
			`{"type":"legacy","id":"b_00000001","attrs":{"html":"<p>old</p>","reason":"imported"}},` +
			`{"type":"image","id":"b_00000002","attrs":{"media":"med_01JB8Z3K5QH2V6T9C4R7Y1E8B5"}},` +
			`{"type":"paragraph","id":"b_00000003","style":{"textColor":{"value":"#ff0000"}},"content":[{"type":"text","text":"x"}]}]`
		id := source(t, s, 3, `{"schemaVersion":1,"title":"a","slug":"x","body":`+blocks+`}`)
		res, err := ed.CreateTranslation(t.Context(), op, id, "en")
		if err != nil || len(res.Warnings) != 0 {
			t.Errorf("CreateTranslation() = %+v, %v; want a translation that keeps what the source has, as it was", res, err)
		}
	})
}

// TestCreateTranslationOperationID checks that a retry returns the same translation.
func TestCreateTranslationOperationID(t *testing.T) {
	ed, s := newSample(t)
	op := Op{By: userID, ID: testOpID(7)}
	first := must(t)(ed.CreateTranslation(t.Context(), op, homeJA, "en"))
	after := snapshot(t, s)
	again, err := ed.CreateTranslation(t.Context(), op, homeJA, "en")
	if err != nil || !reflect.DeepEqual(first, again) || snapshot(t, s) != after {
		t.Errorf("a retry of CreateTranslation() = %+v, %v; want %+v and no change", again, err, first)
	}
	if len(first.Warnings) != 1 {
		t.Errorf("the result has the warnings %v, want the one that the retry returns", first.Warnings)
	}
}

// TestDeleteEntry deletes entries: a draft that nothing uses, which keeps its revisions, and what it refuses.
func TestDeleteEntry(t *testing.T) {
	op := Op{By: userID}

	t.Run("a draft", func(t *testing.T) {
		ed, s := newSample(t)
		entry := newDraft(t, ed, content.TypePage, "ja")
		first := must(t)(ed.SaveRevision(t.Context(), op, entry, "", []byte(`{"schemaVersion":1,"title":"a","slug":"a"}`)))
		second := must(t)(ed.SaveRevision(t.Context(), op, entry, first.Revision, []byte(`{"schemaVersion":1,"title":"b","slug":"a"}`)))
		doc := read(t, s, content.RefEntry, entry)
		res, err := ed.Delete(t.Context(), op, content.RefEntry, entry, doc.Seq)
		if err != nil {
			t.Fatal(err)
		}
		if res.Kind != content.RefEntry || res.ID != entry || res.Version.Hash != "" || res.Version.Seq <= doc.Seq ||
			res.Version.SavedBy != userID || len(res.Warnings) != 0 || res.Revision != "" {
			t.Errorf("Delete() = %+v, want the version that marks the deletion", res)
		}
		err = s.View(t.Context(), func(tx *store.Tx) error {
			if d, err := tx.Document(content.RefEntry, entry); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("Document() after Delete() = %+v, %v; want fs.ErrNotExist", d, err)
			}
			if sum, err := tx.Summary(content.RefEntry, entry); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("Summary() after Delete() = %+v, %v; want fs.ErrNotExist", sum, err)
			}
			h, err := tx.History(content.RefEntry, entry)
			if err != nil || len(h) != 2 || h[1] != res.Version {
				t.Errorf("History() = %+v, %v; want the deletion as the last version", h, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		// The revisions stay, with their JSON.
		if got := revisionIDs(t, s, entry); !slices.Equal(got, []string{first.Revision, second.Revision}) {
			t.Errorf("Revisions() = %v, want both revisions to stay", got)
		}
		if got := string(revisionData(t, s, second.Revision)); got != `{"schemaVersion":1,"title":"b","slug":"a"}` {
			t.Errorf("the revision is %s, want it to stay as it was", got)
		}
	})

	t.Run("an entry that is published", func(t *testing.T) {
		ed, s := newSample(t)
		before := snapshot(t, s)
		_, err := ed.Delete(t.Context(), op, content.RefEntry, aboutJA, read(t, s, content.RefEntry, aboutJA).Seq)
		want := docName(content.RefEntry, aboutJA) + ": /status: cannot delete a published or scheduled entry; unpublish it first"
		if err == nil || err.Error() != want || snapshot(t, s) != before {
			t.Errorf("Delete() of a published entry = %v, want %q and no change", err, want)
		}
	})

	t.Run("an entry that is scheduled", func(t *testing.T) {
		ed, s := newSample(t)
		id := testID(content.RefEntry, 4)
		put(t, s, content.RefEntry, id, storedEntry(id, 504, content.TypePage, "ja", testID(content.RefTranslationGroup, 4), content.StatusScheduled, ""))
		before := snapshot(t, s)
		_, err := ed.Delete(t.Context(), op, content.RefEntry, id, read(t, s, content.RefEntry, id).Seq)
		want := docName(content.RefEntry, id) + ": /status: cannot delete a published or scheduled entry; unpublish it first"
		if err == nil || err.Error() != want || snapshot(t, s) != before {
			t.Errorf("Delete() of a scheduled entry = %v, want %q and no change", err, want)
		}
	})

	t.Run("an entry that is archived", func(t *testing.T) {
		ed, s := newSample(t)
		id := testID(content.RefEntry, 4)
		put(t, s, content.RefEntry, id, storedEntry(id, 504, content.TypePage, "ja", testID(content.RefTranslationGroup, 4), content.StatusArchived, ""))
		if _, err := ed.Delete(t.Context(), op, content.RefEntry, id, read(t, s, content.RefEntry, id).Seq); err != nil {
			t.Errorf("Delete() of an archived entry = %v", err)
		}
	})

	t.Run("an entry that a menu and a redirect use", func(t *testing.T) {
		ed, s := newSample(t)
		before := snapshot(t, s)
		_, err := ed.Delete(t.Context(), op, content.RefEntry, draftEN, read(t, s, content.RefEntry, draftEN).Seq)
		want := docName(content.RefEntry, draftEN) + ": cannot delete: used by menus/mnu_01JC0000000000000000000001.json, redirects/rdr_01JC0000000000000000000006.json"
		if got := usedBy(t, docName(content.RefEntry, draftEN), draftEN); !slices.Equal(got, []string{"menus/mnu_01JC0000000000000000000001.json", "redirects/rdr_01JC0000000000000000000006.json"}) {
			t.Fatalf("the sample site has the files %v that use the entry", got)
		}
		if err == nil || err.Error() != want || snapshot(t, s) != before {
			t.Errorf("Delete() = %v, want %q and no change", err, want)
		}
	})

	t.Run("an entry that the published revision of another entry uses", func(t *testing.T) {
		ed, s := newSample(t)
		draft := newDraft(t, ed, content.TypePage, "ja")
		// A published page whose published revision links to the draft, and a later revision that does not.
		other, pub, later := testID(content.RefEntry, 6), testID(content.RefRevision, 6), testID(content.RefRevision, 7)
		group := testID(content.RefTranslationGroup, 6)
		put(t, s, content.RefEntry, other, storedEntry(other, 506, content.TypePage, "ja", group, content.StatusDraft, ""))
		addStoredRevision(t, s, other, pub, `{"schemaVersion":1,"title":"a","slug":"a","body":[{"type":"paragraph","id":"b_00000001","content":[`+
			`{"type":"text","text":"x","marks":[{"type":"link","attrs":{"entry":"`+draft+`"}}]}]}]}`)
		put(t, s, content.RefEntry, other, storedEntry(other, 506, content.TypePage, "ja", group, content.StatusPublished, pub))
		addStoredRevision(t, s, other, later, `{"schemaVersion":1,"title":"a","slug":"a"}`)
		before := snapshot(t, s)
		_, err := ed.Delete(t.Context(), op, content.RefEntry, draft, read(t, s, content.RefEntry, draft).Seq)
		want := docName(content.RefEntry, draft) + ": cannot delete: used by " + revisionName(pub)
		if err == nil || err.Error() != want || snapshot(t, s) != before {
			t.Errorf("Delete() = %v, want %q and no change", err, want)
		}
	})

	t.Run("an entry that only the latest revision of a draft uses", func(t *testing.T) {
		ed, s := newSample(t)
		target := newDraft(t, ed, content.TypePage, "ja")
		user := newDraft(t, ed, content.TypePage, "ja")
		rev := must(t)(ed.SaveRevision(t.Context(), op, user, "", []byte(`{"schemaVersion":1,"title":"a","slug":"a","body":[{"type":"paragraph","id":"b_00000001","content":[`+
			`{"type":"text","text":"x","marks":[{"type":"link","attrs":{"entry":"`+target+`"}}]}]}]}`))).Revision
		res, err := ed.Delete(t.Context(), op, content.RefEntry, target, read(t, s, content.RefEntry, target).Seq)
		if err != nil {
			t.Fatalf("Delete() = %v", err)
		}
		want := docName(content.RefEntry, target) + ": deleted although used by " + revisionName(rev) + ", the latest revision of " +
			docName(content.RefEntry, user) + ", which is not published"
		if got := lines(res.Warnings); !slices.Equal(got, []string{want}) {
			t.Errorf("Delete() warnings:\n%s\nwant\n%s", strings.Join(got, "\n"), want)
		}
	})

	t.Run("an entry that only its own revision uses", func(t *testing.T) {
		ed, s := newSample(t)
		entry := newDraft(t, ed, content.TypePage, "ja")
		must(t)(ed.SaveRevision(t.Context(), op, entry, "", []byte(`{"schemaVersion":1,"title":"a","slug":"a","body":[{"type":"paragraph","id":"b_00000001","content":[`+
			`{"type":"text","text":"x","marks":[{"type":"link","attrs":{"entry":"`+entry+`"}}]}]}]}`)))
		res, err := ed.Delete(t.Context(), op, content.RefEntry, entry, read(t, s, content.RefEntry, entry).Seq)
		if err != nil || len(res.Warnings) != 0 {
			t.Errorf("Delete() = %+v, %v; want no refusal and no warning", res, err)
		}
	})

	t.Run("a translation that is made again", func(t *testing.T) {
		ed, s := newSample(t)
		page := newDraft(t, ed, content.TypePage, "ja")
		must(t)(ed.SaveRevision(t.Context(), op, page, "", []byte(`{"schemaVersion":1,"title":"a","slug":"a"}`)))
		first := must(t)(ed.CreateTranslation(t.Context(), op, page, "en"))
		if _, err := ed.CreateTranslation(t.Context(), op, page, "en"); err == nil {
			t.Fatal("CreateTranslation() into a language that the group has succeeded")
		}
		must(t)(ed.Delete(t.Context(), op, content.RefEntry, first.ID, first.Version.Seq))
		second, err := ed.CreateTranslation(t.Context(), op, page, "en")
		if err != nil || second.ID == first.ID || second.Number != first.Number+1 {
			t.Fatalf("CreateTranslation() after the deletion of the translation = %+v, %v; want a new entry with the next number", second, err)
		}
		// The revision of the entry that was deleted stays, and the new entry has its own.
		if got := revisionIDs(t, s, first.ID); !slices.Equal(got, []string{first.Revision}) {
			t.Errorf("the revisions of the deleted entry are %v, want %s", got, first.Revision)
		}
		if got := revisionIDs(t, s, second.ID); !slices.Equal(got, []string{second.Revision}) {
			t.Errorf("the revisions of the new entry are %v, want %s", got, second.Revision)
		}
	})

	t.Run("a stale base", func(t *testing.T) {
		ed, s := newSample(t)
		entry := newDraft(t, ed, content.TypePage, "ja")
		seq := read(t, s, content.RefEntry, entry).Seq
		before := snapshot(t, s)
		_, err := ed.Delete(t.Context(), op, content.RefEntry, entry, seq+1)
		want := fmt.Sprintf("%s: changed since it was read: it is at version %d, not %d", docName(content.RefEntry, entry), seq, seq+1)
		if !errors.Is(err, ErrConflict) || err.Error() != want || snapshot(t, s) != before {
			t.Errorf("Delete() with a stale base = %v, want %q wrapping ErrConflict, and no change", err, want)
		}
	})

	t.Run("an entry that does not exist", func(t *testing.T) {
		ed, _ := newSample(t)
		missing := testID(content.RefEntry, 9)
		_, err := ed.Delete(t.Context(), op, content.RefEntry, missing, 1)
		if want := "entries/" + missing + ".json: not found"; !errors.Is(err, fs.ErrNotExist) || err == nil || err.Error() != want {
			t.Errorf("Delete() = %v, want %q wrapping fs.ErrNotExist", err, want)
		}
	})
}

// TestSampleEntries saves the latest revision of every entry of the sample site again, which is valid and brings no
// warning, and translates every entry into the other language, which succeeds with the warnings that a translation has
// unless the translation group has the language already.
func TestSampleEntries(t *testing.T) {
	ed, s := newSample(t)
	op := Op{By: userID}
	var sums []store.Summary
	err := s.View(t.Context(), func(tx *store.Tx) (err error) {
		sums, err = tx.Summaries(content.RefEntry)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var saved, translated, taken int
	for _, sum := range sums {
		if sum.Latest == "" {
			continue
		}
		res, err := ed.SaveRevision(t.Context(), op, sum.ID, sum.Latest, revisionData(t, s, sum.Latest))
		if err != nil || len(res.Warnings) != 0 {
			t.Errorf("SaveRevision() of the revision of %s again = %v, %v; want a revision without warnings", sum.ID, res.Warnings, err)
			continue
		}
		saved++
		language := "en"
		if sum.Entry.Language == "en" {
			language = "ja"
		}
		res, err = ed.CreateTranslation(t.Context(), op, sum.ID, language)
		if err != nil {
			if strings.Contains(err.Error(), "already has an entry in the language") {
				taken++
			} else {
				t.Errorf("CreateTranslation(%s, %s) = %v", sum.ID, language, err)
			}
			continue
		}
		translated++
		for _, w := range res.Warnings {
			// A slug that a page or a post of the other language has already is the only other problem that the sample has.
			if !strings.Contains(w.Msg, " has no translation in ") && !strings.Contains(w.Msg, "is also used by revisions/") {
				t.Errorf("CreateTranslation(%s, %s) has the warning %v", sum.ID, language, w)
			}
		}
	}
	if saved == 0 || translated == 0 || taken == 0 {
		t.Errorf("%d entries saved, %d translated and %d with the language taken, want some of each", saved, translated, taken)
	}
}
