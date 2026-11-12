package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/sitedir"
)

// indexRows are the rows of the tables summaries and references, with their keys and values as text, by table.
type indexRows map[indexTable]map[string]string

// storedIndex returns the rows of summaries and references that tx has.
func storedIndex(tx *Tx) (indexRows, error) {
	rows := indexRows{}
	for _, table := range []indexTable{indexSummaries, indexReferences} {
		rows[table] = map[string]string{}
		if err := tx.b.indexScan(table, nil, func(key, value []byte) bool {
			rows[table][string(key)] = string(value)
			return true
		}); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

// computedIndex returns the rows of summaries and references that tx must have, from the documents and the revisions
// alone, and the largest number of the entries.
func computedIndex(tx *Tx) (indexRows, int64, error) {
	rows := indexRows{indexSummaries: {}, indexReferences: {}}
	largest, err := tx.computeIndex(func(table indexTable, key, value []byte) error {
		if _, ok := rows[table][string(key)]; ok {
			return fmt.Errorf("the row %q of %s is computed twice", key, indexTableNames[table])
		}
		rows[table][string(key)] = string(value)
		return nil
	})
	return rows, largest, err
}

// diffIndex describes how the rows got differ from those that want, or returns "" if they are the same.
func diffIndex(got, want indexRows) string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	for _, table := range []indexTable{indexSummaries, indexReferences} {
		name := indexTableNames[table]
		for _, key := range slices.Sorted(maps.Keys(want[table])) {
			g, ok := got[table][key]
			switch {
			case !ok:
				add("%s has no row %q, want %q", name, key, want[table][key])
			case g != want[table][key]:
				add("%s has %q at %q, want %q", name, g, key, want[table][key])
			}
		}
		for _, key := range slices.Sorted(maps.Keys(got[table])) {
			if _, ok := want[table][key]; !ok {
				add("%s has the row %q, which nothing gives", name, key)
			}
		}
	}
	if len(problems) > 3 {
		problems = append(problems[:3], fmt.Sprintf("and %d more", len(problems)-3))
	}
	return strings.Join(problems, "; ")
}

// indexProblem returns how the index of s differs from what its documents and revisions give: the rows of summaries and
// references, and a last number that is smaller than the largest of the current entries. It returns "" if there is no
// difference, and for a store of the table schema 1, which has no index.
func indexProblem(t *testing.T, s *Store) (string, error) {
	t.Helper()
	var problem string
	err := s.View(t.Context(), func(tx *Tx) error {
		if ready, err := tx.b.indexReady(); err != nil || !ready {
			return err
		}
		got, err := storedIndex(tx)
		if err != nil {
			return err
		}
		want, largest, err := computedIndex(tx)
		if err != nil {
			return err
		}
		if problem = diffIndex(got, want); problem != "" {
			return nil
		}
		if last, err := tx.b.lastNumber(); err != nil || last < largest {
			problem = fmt.Sprintf("the last number is %d, and the largest of the entries %d (%v)", last, largest, err)
			return nil
		}
		return nil
	})
	return problem, err
}

// eachStore runs f in a subtest for a store of the model, and for one of each backend of allBackends.
func eachStore(t *testing.T, f func(t *testing.T, s *Store)) {
	t.Helper()
	t.Run("model", func(t *testing.T) {
		s := newModelStore()
		t.Cleanup(func() { _ = s.Close() })
		f(t, s)
	})
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, _ := newStore(t, tb)
		f(t, s)
	})
}

// putAll puts, in one Update of s, the documents data, by their kinds and IDs.
func putAll(t *testing.T, s *Store, docs ...indexDoc) {
	t.Helper()
	update(t, s, func(tx *Tx) error {
		for _, d := range docs {
			if _, err := tx.Put(d.kind, d.id, d.data, userID); err != nil {
				return fmt.Errorf("Put(%q, %q) = %w", d.kind, d.id, err)
			}
		}
		return nil
	})
}

type indexDoc struct {
	kind content.RefKind
	id   string
	data []byte
}

const (
	medium2    = "med_01JE0000000000000000000002"
	term2      = "trm_01JE0000000000000000000002"
	page2      = "ent_01JE0000000000000000000002"
	revision2  = "rev_01JE0000000000000000000002"
	revision3  = "rev_01JE0000000000000000000003"
	revision4  = "rev_01JE0000000000000000000004"
	revision5  = "rev_01JE0000000000000000000005"
	otherEntry = "ent_01JE0000000000000000000009"
	lateEntry  = "ent_01JE0000000000000000000010"
	thirdEntry = "ent_01JE0000000000000000000011"
	page3      = "ent_01JE0000000000000000000012"
)

// entryJSON returns the JSON of the entry id with the fields that follow its ID.
func entryJSON(id, fields string) []byte {
	return []byte(`{"schemaVersion": 1, "id": "` + id + `", ` + fields + `}`)
}

// publishedEntry returns the JSON of the entry id, which publishes the revision rev, with the number n.
func publishedEntry(id string, n int, rev string) []byte {
	return entryJSON(id, fmt.Sprintf(`"number": %d, "status": "published", "publishedRevision": "%s"`, n, rev))
}

// scheduledAt is the time at which the entries of the tests schedule their revisions.
const scheduledAt = "2026-11-10T09:00:00+09:00"

// scheduledEntry returns the JSON of the entry id, which schedules the revision rev, with the number n.
func scheduledEntry(id string, n int, rev string) []byte {
	return entryJSON(id, fmt.Sprintf(`"number": %d, "status": "scheduled", "scheduledRevision": "%s", `+
		`"scheduledAt": "%s"`, n, rev, scheduledAt))
}

// revisionJSON returns the JSON of a revision with what the index reads of it: the fields that follow its schema
// version.
func revisionJSON(fields string) []byte {
	return []byte(`{"schemaVersion": 1, ` + fields + `}`)
}

// checkReferrers checks that Referrers of the document of kind with id are want, "kind id role" each.
func checkReferrers(t *testing.T, tx *Tx, kind content.RefKind, id string, want ...string) {
	t.Helper()
	refs, err := tx.Referrers(kind, id)
	if err != nil {
		t.Errorf("Referrers(%q, %q) = %v", kind, id, err)
		return
	}
	var got []string
	for _, r := range refs {
		got = append(got, fmt.Sprintf("%s %s %s", r.Kind, r.ID, r.Role))
	}
	if !slices.Equal(got, want) {
		t.Errorf("Referrers(%q, %q) = %q, want %q", kind, id, got, want)
	}
}

// checkSummary checks that Summary of the document of kind with id is want, but for Kind and ID.
func checkSummary(t *testing.T, tx *Tx, kind content.RefKind, id string, want Summary) {
	t.Helper()
	got, err := tx.Summary(kind, id)
	if err != nil {
		t.Errorf("Summary(%q, %q) = %v", kind, id, err)
		return
	}
	want.Kind, want.ID = kind, id
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Summary(%q, %q) = %s, want %s", kind, id, summaryText(got), summaryText(want))
	}
}

// checkNoSummary checks that Summary of the document of kind with id fails with an error that wraps fs.ErrNotExist.
func checkNoSummary(t *testing.T, tx *Tx, kind content.RefKind, id string) {
	t.Helper()
	if sum, err := tx.Summary(kind, id); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Summary(%q, %q) = %s, %v; want an error that wraps fs.ErrNotExist", kind, id, summaryText(sum), err)
	}
}

// TestIndexSampleSite puts the sample site of internal/build, its documents and then the revisions that its entries
// publish, and checks the index against what the files of the site say: the summary of each entry, the referrers of
// every ID that the files hold (found in their text, not as JSON), the last number and the number of documents; and
// some summaries exactly.
func TestIndexSampleSite(t *testing.T) {
	site := filepath.Join("..", "build", "testdata", "site")
	dir, err := sitedir.Open(site)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dir.Close() })
	updates, _ := sampleSiteSteps(t)

	// The files of the site: the documents of each kind, and the revisions that its entries publish.
	docs := map[content.RefKind]map[string]content.SiteFile{}
	for _, kind := range indexKinds {
		if kind == SettingsKind {
			settings, _, err := dir.Settings()
			if err != nil {
				t.Fatalf("Settings() of the sample site = %v", err)
			}
			docs[kind] = map[string]content.SiteFile{"": settings}
		} else if docs[kind], err = dir.Documents(kind); err != nil {
			t.Fatalf("Documents(%q) of the sample site = %v", kind, err)
		}
	}
	entries := map[string]content.Entry{}
	revisions := map[string]content.Revision{} // by the entries that publish them
	largest := 0
	for id, f := range docs[content.RefEntry] {
		var e content.Entry
		if err := e.UnmarshalJSON(f.Data); err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		entries[id] = e
		largest = max(largest, e.Number)
	}

	// What refers to what, by the IDs among the strings in the text of the files: the documents refer to them, and so
	// do the published and the last revision, which are one revision, of the entry that publishes it.
	idPattern := regexp.MustCompile(`"((?:ent|trm|med|aut|frm)_[0-9A-HJKMNP-TV-Z]{26})"`)
	want := map[string][]string{} // by the IDs referred to, "kind id role" each
	scan := func(data []byte, self string, kind content.RefKind, id string, roles ...Role) {
		seen := map[string]bool{}
		for _, m := range idPattern.FindAllSubmatch(data, -1) {
			target := string(m[1])
			if target != self && !seen[target] {
				seen[target] = true
				for _, role := range roles {
					want[target] = append(want[target], fmt.Sprintf("%s %s %s", kind, id, role))
				}
			}
		}
	}
	for kind, files := range docs {
		for id, f := range files {
			scan(f.Data, id, kind, id, RoleDocument)
		}
	}
	for id, e := range entries {
		if e.PublishedRevision == "" {
			continue
		}
		f, err := dir.Revision(e.PublishedRevision)
		if err != nil {
			t.Fatalf("Revision(%q) of the sample site = %v", e.PublishedRevision, err)
		}
		var rev content.Revision
		if err := rev.UnmarshalJSON(f.Data); err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		revisions[id] = rev
		scan(f.Data, id, content.RefEntry, id, RolePublished, RoleLatest)
	}
	for _, referrers := range want {
		slices.Sort(referrers)
	}

	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, _ := newStore(t, tb)
		for _, u := range updates {
			update(t, s, func(tx *Tx) error {
				for _, op := range u.ops {
					if _, err := op.run(tx); err != nil {
						return fmt.Errorf("%s: %w", op.desc, err)
					}
				}
				return nil
			})
		}
		checkConsistent(t, s)

		const (
			about     = "ent_01JB8W2N5KD3F6H9J1M4P7R2T5"
			aboutRev  = "rev_01JB8X9Y3ZE5G8K1N4Q7S2V6W9"
			news      = "trm_01JBA4C7E2G5J8K1M4P7R2T5W8"
			childTerm = "trm_01JBA4C7F3H6K9N2Q5S8V1X4Z7"
			companyRd = "rdr_01JC0000000000000000000001"
			header    = "mnu_01JBD2A3B4C5D6E7F8G9H0J1K2"
		)
		view(t, s, func(tx *Tx) error {
			checkSummary(t, tx, content.RefEntry, about, Summary{
				Entry: &content.Entry{
					ID: about, Number: 2, Type: "page", Language: "ja",
					TranslationGroup: "tg_01JB8X4M7NB5D8F1H4J7K2M6P9", Status: content.StatusPublished,
					PublishedRevision: aboutRev, CreatedAt: "2026-10-25T10:00:00+09:00", CreatedBy: userID,
				},
				Published: &content.Revision{Slug: "about"},
				Latest:    aboutRev,
			})
			checkSummary(t, tx, content.RefTerm, childTerm, Summary{Term: &content.Term{
				ID: childTerm, Taxonomy: "category", Language: "ja", TranslationGroup: "tg_01JBA4C7K7N1R4T7W1Z4C7E1G4",
				Slug: "press-release", Parent: news,
			}})
			checkSummary(t, tx, content.RefRedirect, companyRd, Summary{Redirect: &content.Redirect{
				ID: companyRd, From: "/company/", To: about, Status: 301,
			}})
			checkSummary(t, tx, content.RefMedia, mediaID, Summary{Media: &content.Media{
				ID: mediaID, Hash: "sha256:685d00bf889d89794ae0c30bdfbe8d60e77e60113793348586508867a82795c1",
				MIMEType: "image/jpeg",
			}})
			checkSummary(t, tx, content.RefAuthor, authorID,
				Summary{Author: &content.AuthorProfile{ID: authorID, Slug: "yamada"}})
			checkSummary(t, tx, content.RefForm, formID, Summary{Form: &content.Form{ID: formID}})
			checkSummary(t, tx, content.RefMenu, header, Summary{Menu: &content.Menu{
				ID: header, Language: "ja", Location: "header",
			}})
			checkSummary(t, tx, SettingsKind, "", Summary{})
			checkNoSummary(t, tx, content.RefEntry, otherEntry)

			// The summary of every entry has its document, and its published revision, which is also its last one.
			for id, e := range entries {
				sum, err := tx.Summary(content.RefEntry, id)
				if err != nil {
					t.Errorf("Summary(%q) = %v", id, err)
					continue
				}
				var published *content.Revision
				if rev, ok := revisions[id]; ok {
					published = &content.Revision{
						Slug: rev.Slug, Parent: rev.Parent, Date: rev.Date, Author: rev.Author, Sticky: rev.Sticky,
						Terms: rev.Terms,
					}
				}
				if !reflect.DeepEqual(sum.Entry, &e) || !reflect.DeepEqual(sum.Published, published) ||
					sum.Latest != e.PublishedRevision {
					t.Errorf("Summary(%q) = %s, want the entry %+v, published %+v and last %q", id, summaryText(sum), e,
						published, e.PublishedRevision)
				}
			}

			// The referrers of every ID of the sample: those that its files give, and none for the rest.
			unchecked := maps.Clone(want)
			for kind, files := range docs {
				for id := range files {
					if kind != SettingsKind {
						checkReferrers(t, tx, kind, id, want[id]...)
					}
					delete(unchecked, id)
				}
			}
			for target, referrers := range unchecked {
				t.Errorf("the sample site refers to %s, which has no document: %q", target, referrers)
			}
			checkReferrers(t, tx, SettingsKind, "")

			if n, err := tx.LastNumber(); err != nil || n != largest {
				t.Errorf("LastNumber() = %d, %v; want %d, the largest number of the entries", n, err, largest)
			}
			for kind, files := range docs {
				sums, err := tx.Summaries(kind)
				if err != nil || len(sums) != len(files) {
					t.Errorf("Summaries(%q) = %d summaries, %v; want %d", kind, len(sums), err, len(files))
				}
				if !slices.IsSortedFunc(sums, func(a, b Summary) int { return strings.Compare(a.ID, b.ID) }) {
					t.Errorf("Summaries(%q) are not in the order of their IDs", kind)
				}
			}
			return nil
		})
	})
}

// TestIndexEntryRevisions follows the summary of an entry through the orders in which its document and its revisions
// are saved: the published revision that is not there yet, that is added, that changes to a newer and to an older one,
// drafts, the deletion, and the entry again; and entries that publish a revision of another entry.
func TestIndexEntryRevisions(t *testing.T) {
	revA := revisionJSON(`"title": "A", "slug": "a", "date": "2026-11-03T08:00:00+09:00", "author": "` + authorID +
		`", "sticky": true, "parent": "` + pageID + `", "featuredImage": "` + mediaID + `", "terms": {"category": ["` +
		termID + `"]}`)
	projectionA := &content.Revision{
		Slug: "a", Parent: pageID, Date: "2026-11-03T08:00:00+09:00", Author: authorID, Sticky: true,
		Terms: content.Terms{"category": {termID}},
	}
	revB := revisionJSON(`"title": "B", "slug": "b", "featuredImage": "` + medium2 + `"`)
	revC := revisionJSON(`"title": "C", "slug": "c"`)
	eachStore(t, func(t *testing.T, s *Store) {
		entry := func(n int, rev string) *content.Entry {
			return &content.Entry{ID: entryID, Number: n, Status: content.StatusPublished, PublishedRevision: rev}
		}
		addRevision := func(entry, id string, data []byte) {
			t.Helper()
			update(t, s, func(tx *Tx) error {
				_, err := tx.AddRevision(entry, id, data, userID)
				return err
			})
			checkConsistent(t, s)
		}
		put := func(id string, data []byte) {
			t.Helper()
			putAll(t, s, indexDoc{content.RefEntry, id, data})
			checkConsistent(t, s)
		}
		check := func(f func(tx *Tx)) {
			t.Helper()
			view(t, s, func(tx *Tx) error {
				f(tx)
				return nil
			})
		}
		by := func(roles ...string) []string {
			var refs []string
			for _, role := range roles {
				refs = append(refs, "entry "+entryID+" "+role)
			}
			return refs
		}
		checkNumber := func(tx *Tx, want int) {
			t.Helper()
			if n, err := tx.LastNumber(); err != nil || n != want {
				t.Errorf("LastNumber() = %d, %v; want %d", n, err, want)
			}
		}

		// The entry publishes a revision that is not there yet.
		put(entryID, publishedEntry(entryID, 5, revisionID))
		check(func(tx *Tx) {
			checkSummary(t, tx, content.RefEntry, entryID, Summary{Entry: entry(5, revisionID)})
			checkReferrers(t, tx, content.RefMedia, mediaID)
			checkNumber(tx, 5)
		})

		// The revision is added: it is the published and the last revision.
		addRevision(entryID, revisionID, revA)
		check(func(tx *Tx) {
			checkSummary(t, tx, content.RefEntry, entryID, Summary{
				Entry: entry(5, revisionID), Published: projectionA, Latest: revisionID,
			})
			checkReferrers(t, tx, content.RefMedia, mediaID, by("latest", "published")...)
			checkReferrers(t, tx, content.RefAuthor, authorID, by("latest", "published")...)
			checkReferrers(t, tx, content.RefTerm, termID, by("latest", "published")...)
			checkReferrers(t, tx, content.RefEntry, pageID, by("latest", "published")...)
		})

		// A draft: the last revision changes, and the published one does not.
		addRevision(entryID, revision2, revB)
		check(func(tx *Tx) {
			checkSummary(t, tx, content.RefEntry, entryID, Summary{
				Entry: entry(5, revisionID), Published: projectionA, Latest: revision2,
			})
			checkReferrers(t, tx, content.RefMedia, mediaID, by("published")...)
			checkReferrers(t, tx, content.RefMedia, medium2, by("latest")...)
			checkReferrers(t, tx, content.RefAuthor, authorID, by("published")...)
		})

		// The entry publishes the newer revision.
		put(entryID, publishedEntry(entryID, 5, revision2))
		check(func(tx *Tx) {
			checkSummary(t, tx, content.RefEntry, entryID, Summary{
				Entry: entry(5, revision2), Published: &content.Revision{Slug: "b"}, Latest: revision2,
			})
			checkReferrers(t, tx, content.RefMedia, mediaID)
			checkReferrers(t, tx, content.RefMedia, medium2, by("latest", "published")...)
			checkReferrers(t, tx, content.RefAuthor, authorID)
		})

		// And the older one again, with a number that is not the largest that the entries have had.
		put(entryID, publishedEntry(entryID, 2, revisionID))
		check(func(tx *Tx) {
			checkSummary(t, tx, content.RefEntry, entryID, Summary{
				Entry: entry(2, revisionID), Published: projectionA, Latest: revision2,
			})
			checkReferrers(t, tx, content.RefMedia, mediaID, by("published")...)
			checkReferrers(t, tx, content.RefMedia, medium2, by("latest")...)
			checkNumber(tx, 5)
		})

		// A draft without references takes the place of the last revision.
		addRevision(entryID, revision3, revC)
		check(func(tx *Tx) {
			checkSummary(t, tx, content.RefEntry, entryID, Summary{
				Entry: entry(2, revisionID), Published: projectionA, Latest: revision3,
			})
			checkReferrers(t, tx, content.RefMedia, medium2)
		})

		// Deleted, the entry has no summary and no references; its revisions and its number stay.
		update(t, s, func(tx *Tx) error {
			_, err := tx.Delete(content.RefEntry, entryID, userID)
			return err
		})
		checkConsistent(t, s)
		check(func(tx *Tx) {
			checkNoSummary(t, tx, content.RefEntry, entryID)
			checkReferrers(t, tx, content.RefMedia, mediaID)
			checkReferrers(t, tx, content.RefEntry, pageID)
			if sums, err := tx.Summaries(content.RefEntry); err != nil || len(sums) != 0 {
				t.Errorf("Summaries() after the Delete = %d summaries, %v; want none", len(sums), err)
			}
			if revs, err := tx.Revisions(entryID); err != nil || len(revs) != 3 {
				t.Errorf("Revisions() after the Delete = %d revisions, %v; want 3", len(revs), err)
			}
			checkNumber(tx, 5)
		})

		// The entry again: its last revision and its published one are found among its revisions.
		put(entryID, publishedEntry(entryID, 7, revisionID))
		check(func(tx *Tx) {
			checkSummary(t, tx, content.RefEntry, entryID, Summary{
				Entry: entry(7, revisionID), Published: projectionA, Latest: revision3,
			})
			checkReferrers(t, tx, content.RefMedia, mediaID, by("published")...)
			checkNumber(tx, 7)
		})

		// Entries that publish a revision that is not there yet get it when it is added, to another entry; and an entry
		// that publishes a revision of another entry that is there gets it at once.
		put(page2, publishedEntry(page2, 8, revision4))
		put(thirdEntry, publishedEntry(thirdEntry, 6, revision4))
		put(otherEntry, entryJSON(otherEntry, `"number": 9`))
		check(func(tx *Tx) {
			checkSummary(t, tx, content.RefEntry, page2, Summary{Entry: &content.Entry{
				ID: page2, Number: 8, Status: content.StatusPublished, PublishedRevision: revision4,
			}})
		})
		addRevision(otherEntry, revision4, revA)
		put(page2, publishedEntry(page2, 10, revision4))
		check(func(tx *Tx) {
			checkSummary(t, tx, content.RefEntry, page2, Summary{
				Entry: &content.Entry{
					ID: page2, Number: 10, Status: content.StatusPublished, PublishedRevision: revision4,
				},
				Published: projectionA,
			})
			checkSummary(t, tx, content.RefEntry, thirdEntry, Summary{
				Entry: &content.Entry{
					ID: thirdEntry, Number: 6, Status: content.StatusPublished, PublishedRevision: revision4,
				},
				Published: projectionA,
			})
			checkSummary(t, tx, content.RefEntry, otherEntry, Summary{
				Entry: &content.Entry{ID: otherEntry, Number: 9}, Latest: revision4,
			})
			checkReferrers(t, tx, content.RefMedia, mediaID, "entry "+entryID+" published", "entry "+page2+" published",
				"entry "+otherEntry+" latest", "entry "+thirdEntry+" published")
			checkNumber(tx, 10)
		})
		put(lateEntry, publishedEntry(lateEntry, 4, revision4))
		check(func(tx *Tx) {
			checkSummary(t, tx, content.RefEntry, lateEntry, Summary{
				Entry: &content.Entry{
					ID: lateEntry, Number: 4, Status: content.StatusPublished, PublishedRevision: revision4,
				},
				Published: projectionA,
			})
		})

		// A revision that is there when its entry publishes it: the summary has it, and keeps the last revision.
		put(page3, entryJSON(page3, `"number": 11`))
		addRevision(page3, revision5, revB)
		put(page3, publishedEntry(page3, 11, revision5))
		check(func(tx *Tx) {
			checkSummary(t, tx, content.RefEntry, page3, Summary{
				Entry: &content.Entry{
					ID: page3, Number: 11, Status: content.StatusPublished, PublishedRevision: revision5,
				},
				Published: &content.Revision{Slug: "b"}, Latest: revision5,
			})
			checkReferrers(t, tx, content.RefMedia, medium2, "entry "+page3+" latest", "entry "+page3+" published")
			checkNumber(tx, 11)
		})
	})
}

// TestIndexScheduled follows the references of entries that schedule a revision through the orders in which their
// documents and revisions are saved, as TestIndexEntryRevisions does for the published one: the revision that is not
// there yet, that is added, that is not the last one, that the entry changes or drops, and the entry that is deleted;
// and entries that schedule a revision of another entry, or publish and schedule the same one.
func TestIndexScheduled(t *testing.T) {
	revA := revisionJSON(`"title": "A", "slug": "a", "author": "` + authorID + `", "parent": "` + pageID +
		`", "featuredImage": "` + mediaID + `", "terms": {"category": ["` + termID + `"]}`)
	revB := revisionJSON(`"title": "B", "slug": "b", "featuredImage": "` + medium2 + `"`)
	eachStore(t, func(t *testing.T, s *Store) {
		// scheduling returns the state of the entry id with the number n, which schedules rev.
		scheduling := func(id string, n int, rev string) *content.Entry {
			return &content.Entry{
				ID: id, Number: n, Status: content.StatusScheduled, ScheduledRevision: rev, ScheduledAt: scheduledAt,
			}
		}
		// both returns the JSON of the entry id with the number n, which publishes and schedules rev.
		both := func(id string, n int, rev string) []byte {
			return entryJSON(id, fmt.Sprintf(`"number": %d, "status": "published", "publishedRevision": "%s", `+
				`"scheduledRevision": "%s", "scheduledAt": "%s"`, n, rev, rev, scheduledAt))
		}
		bothState := func(id string, n int, rev string) *content.Entry {
			return &content.Entry{
				ID: id, Number: n, Status: content.StatusPublished, PublishedRevision: rev, ScheduledRevision: rev,
				ScheduledAt: scheduledAt,
			}
		}
		addRevision := func(entry, id string, data []byte) {
			t.Helper()
			update(t, s, func(tx *Tx) error {
				_, err := tx.AddRevision(entry, id, data, userID)
				return err
			})
			checkConsistent(t, s)
		}
		put := func(id string, data []byte) {
			t.Helper()
			putAll(t, s, indexDoc{content.RefEntry, id, data})
			checkConsistent(t, s)
		}
		check := func(f func(tx *Tx)) {
			t.Helper()
			view(t, s, func(tx *Tx) error {
				f(tx)
				return nil
			})
		}
		// by returns the referrers that checkReferrers takes for the entry id in each of roles.
		by := func(id string, roles ...string) []string {
			var refs []string
			for _, role := range roles {
				refs = append(refs, "entry "+id+" "+role)
			}
			return refs
		}

		// The entry schedules a revision that is not there yet: it has no references.
		put(entryID, scheduledEntry(entryID, 5, revisionID))
		check(func(tx *Tx) {
			checkSummary(t, tx, content.RefEntry, entryID, Summary{Entry: scheduling(entryID, 5, revisionID)})
			checkReferrers(t, tx, content.RefMedia, mediaID)
		})

		// The revision is added: it is the scheduled and the last one, and the entry refers to what it holds in both
		// roles, but has no published revision.
		addRevision(entryID, revisionID, revA)
		check(func(tx *Tx) {
			checkSummary(t, tx, content.RefEntry, entryID, Summary{
				Entry: scheduling(entryID, 5, revisionID), Latest: revisionID,
			})
			checkReferrers(t, tx, content.RefMedia, mediaID, by(entryID, "latest", "scheduled")...)
			checkReferrers(t, tx, content.RefAuthor, authorID, by(entryID, "latest", "scheduled")...)
			checkReferrers(t, tx, content.RefTerm, termID, by(entryID, "latest", "scheduled")...)
			checkReferrers(t, tx, content.RefEntry, pageID, by(entryID, "latest", "scheduled")...)
		})

		// A draft: the last revision changes, and the scheduled one does not.
		addRevision(entryID, revision2, revB)
		check(func(tx *Tx) {
			checkSummary(t, tx, content.RefEntry, entryID, Summary{
				Entry: scheduling(entryID, 5, revisionID), Latest: revision2,
			})
			checkReferrers(t, tx, content.RefMedia, mediaID, by(entryID, "scheduled")...)
			checkReferrers(t, tx, content.RefMedia, medium2, by(entryID, "latest")...)
			checkReferrers(t, tx, content.RefAuthor, authorID, by(entryID, "scheduled")...)
		})

		// The entry schedules the newer revision.
		put(entryID, scheduledEntry(entryID, 5, revision2))
		check(func(tx *Tx) {
			checkSummary(t, tx, content.RefEntry, entryID, Summary{
				Entry: scheduling(entryID, 5, revision2), Latest: revision2,
			})
			checkReferrers(t, tx, content.RefMedia, mediaID)
			checkReferrers(t, tx, content.RefMedia, medium2, by(entryID, "latest", "scheduled")...)
			checkReferrers(t, tx, content.RefAuthor, authorID)
		})

		// A Put without the items removes the role, and the entry no longer names the revision.
		put(entryID, entryJSON(entryID, `"number": 5, "status": "draft"`))
		check(func(tx *Tx) {
			checkSummary(t, tx, content.RefEntry, entryID, Summary{
				Entry: &content.Entry{ID: entryID, Number: 5, Status: content.StatusDraft}, Latest: revision2,
			})
			checkReferrers(t, tx, content.RefMedia, medium2, by(entryID, "latest")...)
		})

		// The entry schedules the older revision, which is not the last one; and again with another time, which keeps
		// what the entry refers to.
		put(entryID, scheduledEntry(entryID, 5, revisionID))
		const later = "2026-12-01T00:00:00+09:00"
		put(entryID, entryJSON(entryID, `"number": 5, "status": "scheduled", "scheduledRevision": "`+revisionID+
			`", "scheduledAt": "`+later+`"`))
		check(func(tx *Tx) {
			want := scheduling(entryID, 5, revisionID)
			want.ScheduledAt = later
			checkSummary(t, tx, content.RefEntry, entryID, Summary{Entry: want, Latest: revision2})
			checkReferrers(t, tx, content.RefMedia, mediaID, by(entryID, "scheduled")...)
			checkReferrers(t, tx, content.RefMedia, medium2, by(entryID, "latest")...)
		})

		// Deleted, the entry has no summary and no references; its revisions stay.
		update(t, s, func(tx *Tx) error {
			_, err := tx.Delete(content.RefEntry, entryID, userID)
			return err
		})
		checkConsistent(t, s)
		check(func(tx *Tx) {
			checkNoSummary(t, tx, content.RefEntry, entryID)
			checkReferrers(t, tx, content.RefMedia, mediaID)
			checkReferrers(t, tx, content.RefMedia, medium2)
		})

		// Entries that schedule a revision that is not there yet get it when it is added, to another entry; and an
		// entry that schedules a revision of another entry that is there gets it at once.
		put(page2, scheduledEntry(page2, 8, revision4))
		put(thirdEntry, scheduledEntry(thirdEntry, 6, revision4))
		put(otherEntry, entryJSON(otherEntry, `"number": 9`))
		check(func(tx *Tx) {
			checkSummary(t, tx, content.RefEntry, page2, Summary{Entry: scheduling(page2, 8, revision4)})
			checkReferrers(t, tx, content.RefMedia, mediaID)
		})
		addRevision(otherEntry, revision4, revA)
		check(func(tx *Tx) {
			checkSummary(t, tx, content.RefEntry, thirdEntry, Summary{Entry: scheduling(thirdEntry, 6, revision4)})
			checkSummary(t, tx, content.RefEntry, otherEntry, Summary{
				Entry: &content.Entry{ID: otherEntry, Number: 9}, Latest: revision4,
			})
			checkReferrers(t, tx, content.RefMedia, mediaID,
				"entry "+page2+" scheduled", "entry "+otherEntry+" latest", "entry "+thirdEntry+" scheduled")
		})
		put(lateEntry, scheduledEntry(lateEntry, 4, revision4))
		check(func(tx *Tx) {
			checkReferrers(t, tx, content.RefMedia, mediaID, "entry "+page2+" scheduled", "entry "+otherEntry+" latest",
				"entry "+lateEntry+" scheduled", "entry "+thirdEntry+" scheduled")
		})

		// The revision of an entry that is deleted.
		put(page3, scheduledEntry(page3, 11, revision2))
		check(func(tx *Tx) {
			checkReferrers(t, tx, content.RefMedia, medium2, by(page3, "scheduled")...)
		})

		// An entry that publishes and schedules the same revision, which is its last one, has the roles published and
		// scheduled, and latest.
		put(pageID, entryJSON(pageID, `"number": 12`))
		addRevision(pageID, revision5, revB)
		put(pageID, both(pageID, 12, revision5))
		check(func(tx *Tx) {
			checkSummary(t, tx, content.RefEntry, pageID, Summary{
				Entry: bothState(pageID, 12, revision5), Published: &content.Revision{Slug: "b"}, Latest: revision5,
			})
			checkReferrers(t, tx, content.RefMedia, medium2,
				slices.Concat(by(pageID, "latest", "published", "scheduled"), by(page3, "scheduled"))...)
		})

		// Such an entry has one reference that names the revision, so that the revision, added later, gives it both roles
		// at once.
		put(lateEntry, both(lateEntry, 4, revision3))
		addRevision(otherEntry, revision3, revB)
		check(func(tx *Tx) {
			checkSummary(t, tx, content.RefEntry, lateEntry, Summary{
				Entry: bothState(lateEntry, 4, revision3), Published: &content.Revision{Slug: "b"},
			})
			checkReferrers(t, tx, content.RefMedia, mediaID,
				slices.Concat(by(page2, "scheduled"), by(thirdEntry, "scheduled"))...)
			checkReferrers(t, tx, content.RefMedia, medium2, slices.Concat(
				by(pageID, "latest", "published", "scheduled"), by(otherEntry, "latest"),
				by(lateEntry, "published", "scheduled"), by(page3, "scheduled"))...)
		})

		// The index is right, so RebuildIndex writes nothing.
		if w := rebuildWrites(t, s); len(w) != 0 {
			t.Errorf("RebuildIndex() of an index that is right wrote %q, want nothing", w)
		}
	})
}

// jsonf returns the JSON that format makes of args, which are IDs that need no escapes.
func jsonf(format string, args ...any) []byte {
	return fmt.Appendf(nil, format, args...)
}

// TestIndexPublishedRevisionIDs checks that only an ID of a revision that an entry publishes or schedules makes a row of
// references, so that no string in a document can make the keys of the rows of other documents unreadable.
func TestIndexPublishedRevisionIDs(t *testing.T) {
	nul := string(rune(92)) + "u0000" // the escape of a NUL in JSON
	odd := []string{
		"x", "rev_x", strings.ToLower(revisionID), revisionID + "0", entryID + nul + "entry" + nul + page2 + nul + "document",
	}
	eachStore(t, func(t *testing.T, s *Store) {
		var docs []indexDoc
		for i, rev := range odd {
			id := fmt.Sprintf("ent_01JE%022d", 100+i)
			docs = append(docs, indexDoc{content.RefEntry, id, publishedEntry(id, i+1, rev)})
			id = fmt.Sprintf("ent_01JE%022d", 300+i)
			docs = append(docs, indexDoc{content.RefEntry, id, scheduledEntry(id, len(odd)+i+1, rev)})
		}
		putAll(t, s, docs...)
		checkConsistent(t, s)
		view(t, s, func(tx *Tx) error {
			rows, err := storedIndex(tx)
			if err != nil {
				t.Fatalf("the rows of the index = %v", err)
			}
			if n := len(rows[indexReferences]); n != 0 {
				t.Errorf("the rows of references = %d, want none: no entry publishes or schedules an ID of a revision", n)
			}
			if sums, err := tx.Summaries(content.RefEntry); err != nil || len(sums) != 2*len(odd) {
				t.Errorf("Summaries() = %d summaries, %v; want %d", len(sums), err, 2*len(odd))
			}
			checkReferrers(t, tx, content.RefEntry, entryID)
			return nil
		})
		// An ID that is one makes a row, which AddRevision finds.
		id, scheduler := "ent_01JE0000000000000000000200", "ent_01JE0000000000000000000201"
		putAll(t, s, indexDoc{content.RefEntry, id, publishedEntry(id, 9, revisionID)},
			indexDoc{content.RefEntry, scheduler, scheduledEntry(scheduler, 10, revisionID)},
			indexDoc{content.RefEntry, otherEntry, entryJSON(otherEntry, `"number": 1`)})
		update(t, s, func(tx *Tx) error {
			_, err := tx.AddRevision(otherEntry, revisionID, revisionJSON(`"slug": "x", "featuredImage": "`+mediaID+`"`),
				userID)
			return err
		})
		view(t, s, func(tx *Tx) error {
			if sum, err := tx.Summary(content.RefEntry, id); err != nil || sum.Published == nil || sum.Published.Slug != "x" {
				t.Errorf("Summary() = %s, %v; want the published revision", summaryText(sum), err)
			}
			checkReferrers(t, tx, content.RefMedia, mediaID,
				"entry "+otherEntry+" latest", "entry "+id+" published", "entry "+scheduler+" scheduled")
			return nil
		})
		checkConsistent(t, s)
	})
}

// fillIndexStore puts a document of every kind that refers to others, revisions of two entries, and operations, in two
// Updates, the second of which deletes and changes some.
func fillIndexStore(t *testing.T, s *Store) {
	t.Helper()
	revA := jsonf(`{"schemaVersion": 1, "title": "A", "slug": "a", "featuredImage": %q, "terms": {"category": [%q]}}`,
		mediaID, termID)
	revB := jsonf(`{"schemaVersion": 1, "title": "B", "slug": "b", "featuredImage": %q}`, medium2)
	revC := jsonf(`{"schemaVersion": 1, "title": "C", "parent": %q, "body": [{"type": "paragraph", `+
		`"id": "b_00000001", "content": [{"type": "text", "text": %q}]}]}`, entryID, term2)
	update(t, s, func(tx *Tx) error {
		for _, d := range []indexDoc{
			{content.RefEntry, entryID, publishedEntry(entryID, 5, revisionID)},
			{content.RefEntry, pageID, entryJSON(pageID, `"number": 8, "status": "draft"`)},
			{content.RefTerm, termID, jsonf(`{"schemaVersion": 1, "id": %q, "slug": "a", "parent": %q}`, termID,
				term2)},
			{content.RefTerm, term2, docJSON(content.RefTerm, term2)},
			{content.RefMedia, mediaID, jsonf(`{"schemaVersion": 1, "id": %q, "hash": %q, "mimeType": "image/jpeg"}`,
				mediaID, sum([]byte("x")))},
			{content.RefMedia, medium2, docJSON(content.RefMedia, medium2)},
			{content.RefAuthor, authorID, jsonf(`{"schemaVersion": 1, "id": %q, "slug": "a", "avatar": %q}`, authorID,
				mediaID)},
			{content.RefMenu, menuID, jsonf(`{"schemaVersion": 1, "id": %q, "language": "ja", "location": "header", `+
				`"items": [{"entry": %q}, {"term": %q}]}`, menuID, entryID, termID)},
			{content.RefForm, formID, jsonf(`{"schemaVersion": 1, "id": %q, "thanksPage": {"ja": %q}}`, formID,
				pageID)},
			{content.RefRedirect, redirectID, jsonf(`{"schemaVersion": 1, "id": %q, "from": "/old", "to": %q, `+
				`"status": 301}`, redirectID, entryID)},
			{SettingsKind, "", jsonf(`{"schemaVersion": 1, "logo": %q, "frontPage": {"ja": %q}}`, mediaID, entryID)},
		} {
			if _, err := tx.Put(d.kind, d.id, d.data, userID); err != nil {
				return fmt.Errorf("Put(%q, %q) = %w", d.kind, d.id, err)
			}
		}
		for _, r := range []struct {
			entry, id string
			data      []byte
		}{{entryID, revisionID, revA}, {entryID, revision2, revB}, {pageID, revision3, revC}} {
			if _, err := tx.AddRevision(r.entry, r.id, r.data, userID); err != nil {
				return fmt.Errorf("AddRevision(%q, %q) = %w", r.entry, r.id, err)
			}
		}
		return tx.PutOperation("op_A", []byte("first"))
	})
	update(t, s, func(tx *Tx) error {
		if _, err := tx.Delete(content.RefMedia, medium2, userID); err != nil {
			return err
		}
		term := jsonf(`{"schemaVersion": 1, "id": %q, "slug": "b"}`, termID)
		if _, err := tx.Put(content.RefTerm, termID, term, otherUser); err != nil {
			return err
		}
		return errors.Join(tx.PutOperation("op_A", []byte("second")), tx.PutOperation("op_B", nil))
	})
}

// indexState is what the index of a store has: its rows, the last number, and the data of some operations.
type indexState struct {
	rows   indexRows
	number int
	ops    map[string]string
}

// readIndexState reads the index of s, and its operations ops that it has.
func readIndexState(t *testing.T, s *Store, ops ...string) indexState {
	t.Helper()
	st := indexState{ops: map[string]string{}}
	view(t, s, func(tx *Tx) error {
		var err error
		if st.rows, err = storedIndex(tx); err != nil {
			t.Fatalf("the rows of the index = %v", err)
		}
		if st.number, err = tx.LastNumber(); err != nil {
			t.Fatalf("LastNumber() = %v", err)
		}
		for _, id := range ops {
			data, ok, err := tx.Operation(id)
			if err != nil {
				t.Fatalf("Operation(%q) = %v", id, err)
			}
			if ok {
				st.ops[id] = string(data)
			}
		}
		return nil
	})
	return st
}

// checkIndexState checks that s has the index state want, with the operations ops.
func checkIndexState(t *testing.T, s *Store, want indexState, ops ...string) {
	t.Helper()
	got := readIndexState(t, s, ops...)
	if problem := diffIndex(got.rows, want.rows); problem != "" {
		t.Errorf("the rows of the index are not those from before: %s", problem)
	}
	if got.number != want.number || !maps.Equal(got.ops, want.ops) {
		t.Errorf("the last number and the operations are %d and %q, want %d and %q", got.number, got.ops, want.number,
			want.ops)
	}
}

// TestIndexRollback checks that an Update that writes to the index and then fails, or is canceled, leaves the index and
// the operations as they were, and that what it writes can be read in it.
func TestIndexRollback(t *testing.T) {
	ops := []string{"op_A", "op_B", "op_C"}
	eachStore(t, func(t *testing.T, s *Store) {
		fillIndexStore(t, s)
		before := readIndexState(t, s, ops...)
		writes := func(tx *Tx) error {
			if _, err := tx.Put(content.RefEntry, page2, publishedEntry(page2, 40, revision4), userID); err != nil {
				return err
			}
			withMedium := jsonf(`{"schemaVersion": 1, "title": "P", "featuredImage": %q}`, medium2)
			if _, err := tx.AddRevision(page2, revision4, withMedium, userID); err != nil {
				return err
			}
			if _, err := tx.AddRevision(entryID, revision5, withMedium, userID); err != nil {
				return err
			}
			if _, err := tx.Delete(content.RefTerm, term2, userID); err != nil {
				return err
			}
			if _, err := tx.Put(SettingsKind, "", docJSON(SettingsKind, ""), userID); err != nil {
				return err
			}
			if err := tx.PutOperation("op_C", []byte("third")); err != nil {
				return err
			}
			if err := tx.DeleteOperation("op_B"); err != nil {
				return err
			}
			if _, ok, err := tx.Operation("op_B"); err != nil {
				return err
			} else if ok {
				return errors.New("Operation() after DeleteOperation() finds the data, want none")
			}
			// What the Update has written is in the index for the reads in it.
			sum, err := tx.Summary(content.RefEntry, page2)
			switch {
			case err != nil:
				return err
			case sum.Latest != revision4 || sum.Published == nil:
				return fmt.Errorf("Summary() = %s, want the revision published and last", summaryText(sum))
			}
			if n, err := tx.LastNumber(); err != nil {
				return err
			} else if n != 40 {
				return fmt.Errorf("LastNumber() = %d, want 40", n)
			}
			return nil
		}

		errTest := errors.New("test")
		if err := s.Update(t.Context(), func(tx *Tx) error {
			return errors.Join(writes(tx), errTest)
		}); !errors.Is(err, errTest) {
			t.Fatalf("Update() = %v, want the error of f", err)
		}
		checkIndexState(t, s, before, ops...)
		checkConsistent(t, s)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		if err := s.Update(ctx, withRecord(func(tx *Tx) error {
			if err := writes(tx); err != nil {
				return err
			}
			cancel()
			return nil
		})); !errors.Is(err, context.Canceled) {
			t.Fatalf("Update() that is canceled = %v, want context.Canceled", err)
		}
		checkIndexState(t, s, before, ops...)
		checkConsistent(t, s)

		// The same writes, committed: the state is another one, and it is the one that the documents give.
		update(t, s, func(tx *Tx) error { return writes(tx) })
		after := readIndexState(t, s, ops...)
		if _, deleted := after.ops["op_B"]; after.number != 40 || after.ops["op_C"] != "third" || deleted ||
			reflect.DeepEqual(after.rows, before.rows) {
			t.Errorf("the state after the committed Update is that before it: number %d, operations %q", after.number,
				after.ops)
		}
		checkConsistent(t, s)
	})
}

// TestRebuildIndex checks that RebuildIndex leaves the index and the operations as they are, in a store of every kind
// of backend, and that it makes the index again, in a store of the log backend whose rows are removed or damaged.
func TestRebuildIndex(t *testing.T) {
	ops := []string{"op_A", "op_B"}
	rebuild := func(t *testing.T, s *Store) {
		t.Helper()
		update(t, s, func(tx *Tx) error { return tx.RebuildIndex() })
	}
	eachStore(t, func(t *testing.T, s *Store) {
		fillIndexStore(t, s)
		checkConsistent(t, s)
		before := readIndexState(t, s, ops...)
		// 2 entries, 2 terms, 1 medium (the other is deleted), an author, a menu, a form, a redirect and the settings.
		if n := len(before.rows[indexSummaries]); n != 10 {
			t.Fatalf("the index has %d summaries, want 10", n)
		}
		if before.number != 8 || before.ops["op_A"] != "second" || len(before.ops) != 2 {
			t.Fatalf("the last number and the operations are %d and %q, want 8 and the second data of op_A and op_B",
				before.number, before.ops)
		}
		rebuild(t, s)
		checkIndexState(t, s, before, ops...)
		checkConsistent(t, s)

		// An Update that rebuilds and then fails changes nothing.
		errTest := errors.New("test")
		if err := s.Update(t.Context(), func(tx *Tx) error {
			return errors.Join(tx.RebuildIndex(), errTest)
		}); !errors.Is(err, errTest) {
			t.Errorf("Update() = %v, want the error of f", err)
		}
		checkIndexState(t, s, before, ops...)
	})

	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, _ := newStore(t, tb)
		fillIndexStore(t, s)
		before := readIndexState(t, s, ops...)
		rawUpdate(t, s, func(tx *logdb.Tx) error {
			var first []byte
			tx.Scan(tableReferences, nil, func(key, _ []byte) bool {
				first = bytes.Clone(key)
				return false
			})
			return errors.Join(
				tx.Delete(tableSummaries, docKey(content.RefEntry, pageID)),
				tx.Put(tableSummaries, docKey(content.RefTerm, termID), []byte("not JSON")),
				tx.Put(tableSummaries, docKey(content.RefForm, formID), []byte(`{"unknown": 1}`)),
				tx.Delete(tableReferences, first),
				tx.Put(tableReferences, refKey(medium2, content.RefTerm, termID, RoleDocument), nil),
				tx.Delete(tableMeta, []byte(metaNumbers)),
			)
		})
		if problem, err := indexProblem(t, s); err != nil || problem == "" {
			t.Errorf("the problem of the damaged index = %q, %v; want one", problem, err)
		}
		view(t, s, func(tx *Tx) error {
			for _, tt := range []struct {
				kind content.RefKind
				id   string
			}{{content.RefTerm, termID}, {content.RefForm, formID}} {
				want := fmt.Sprintf("summaries %q: invalid value", docKey(tt.kind, tt.id))
				_, err := tx.Summary(tt.kind, tt.id)
				if err == nil || err.Error() != want || errors.Is(err, fs.ErrNotExist) {
					t.Errorf("Summary(%q, %q) = %v, want an error %q that does not wrap fs.ErrNotExist", tt.kind,
						tt.id, err, want)
				}
				if _, err := tx.Summaries(tt.kind); err == nil || err.Error() != want {
					t.Errorf("Summaries(%q) = %v, want an error %q", tt.kind, err, want)
				}
			}
			checkNoSummary(t, tx, content.RefEntry, pageID)
			if n, err := tx.LastNumber(); err != nil || n != 0 {
				t.Errorf("LastNumber() without the number = %d, %v; want 0", n, err)
			}
			return nil
		})
		// A Put of a document with a damaged summary fails: the old references are not known.
		err := s.Update(t.Context(), func(tx *Tx) error {
			_, err := tx.Put(content.RefTerm, termID, docJSON(content.RefTerm, termID), userID)
			return err
		})
		want := fmt.Sprintf("summaries %q: invalid value", docKey(content.RefTerm, termID))
		if err == nil || err.Error() != want {
			t.Errorf("Put() of a document with a damaged summary = %v, want an error %q", err, want)
		}

		rebuild(t, s)
		checkIndexState(t, s, before, ops...)
		checkConsistent(t, s)
	})
}

// indexWrites is a backendTx that notes the writes to the index that go through it, as indexWrite says.
type indexWrites struct {
	backendTx
	writes []string
}

// indexWrite is how indexWrites notes the write op, "put" or "delete", of the row key of table.
func indexWrite(op string, table indexTable, key []byte) string {
	return fmt.Sprintf("%s %s %q", op, indexTableNames[table], key)
}

func (w *indexWrites) indexPut(table indexTable, key, value []byte) error {
	w.writes = append(w.writes, indexWrite("put", table, key))
	return w.backendTx.indexPut(table, key, value)
}

func (w *indexWrites) indexDelete(table indexTable, key []byte) error {
	w.writes = append(w.writes, indexWrite("delete", table, key))
	return w.backendTx.indexDelete(table, key)
}

func (w *indexWrites) setLastNumber(n int64) error {
	w.writes = append(w.writes, fmt.Sprintf("number %d", n))
	return w.backendTx.setLastNumber(n)
}

// rebuildWrites runs RebuildIndex in an Update of s, and returns its writes to the index, sorted.
func rebuildWrites(t *testing.T, s *Store) []string {
	t.Helper()
	w := new(indexWrites)
	update(t, s, func(tx *Tx) error {
		w.backendTx = tx.b
		tx.b = w
		return tx.RebuildIndex()
	})
	slices.Sort(w.writes)
	return w.writes
}

// TestRebuildIndexWrites checks that RebuildIndex writes only what the index lacks, has too much, or has with another
// value: nothing if the index is right, and the one write that repairs each kind of damage; and that in the engine it
// commits nothing for an index that is right, and little for a row that is missing.
func TestRebuildIndexWrites(t *testing.T) {
	ops := []string{"op_A", "op_B"}
	pageRow := docKey(content.RefEntry, pageID)
	termRow := docKey(content.RefTerm, termID)
	menuRow := docKey(content.RefMenu, menuID)
	extraRow := docKey(content.RefTerm, "trm_01JE0000000000000000000077")
	published := refKey(mediaID, content.RefEntry, entryID, RolePublished)
	publishedTerm := refKey(termID, content.RefEntry, entryID, RolePublished)
	extraRef := refKey(medium2, content.RefTerm, termID, RoleDocument)
	junk := []byte("junk")

	eachStore(t, func(t *testing.T, s *Store) {
		fillIndexStore(t, s)
		before := readIndexState(t, s, ops...)
		// The row of another document, to put as the value of a row.
		var formRow []byte
		view(t, s, func(tx *Tx) error {
			v, _, err := tx.b.indexGet(indexSummaries, docKey(content.RefForm, formID))
			formRow = bytes.Clone(v)
			return err
		})
		if w := rebuildWrites(t, s); len(w) != 0 {
			t.Errorf("RebuildIndex() of an index that is right wrote %q, want nothing", w)
		}

		put := func(table indexTable, key, value []byte) func(backendTx) error {
			return func(b backendTx) error { return b.indexPut(table, key, value) }
		}
		del := func(table indexTable, key []byte) func(backendTx) error {
			return func(b backendTx) error { return b.indexDelete(table, key) }
		}
		damages := []struct {
			name   string
			do     func(b backendTx) error
			repair string // the write that repairs it
		}{
			{"a summary that is missing", del(indexSummaries, pageRow), indexWrite("put", indexSummaries, pageRow)},
			{"a summary with another value", put(indexSummaries, termRow, []byte("not JSON")),
				indexWrite("put", indexSummaries, termRow)},
			{"a summary that is the row of another document", put(indexSummaries, menuRow, formRow),
				indexWrite("put", indexSummaries, menuRow)},
			{"a summary that nothing gives", put(indexSummaries, extraRow, formRow),
				indexWrite("delete", indexSummaries, extraRow)},
			{"a summary with a key that is not one", put(indexSummaries, junk, formRow),
				indexWrite("delete", indexSummaries, junk)},
			{"a reference that is missing", del(indexReferences, published),
				indexWrite("put", indexReferences, published)},
			{"a reference with a value", put(indexReferences, publishedTerm, []byte("x")),
				indexWrite("put", indexReferences, publishedTerm)},
			{"a reference that nothing gives", put(indexReferences, extraRef, nil),
				indexWrite("delete", indexReferences, extraRef)},
			{"a reference with a key that is not one", put(indexReferences, junk, nil),
				indexWrite("delete", indexReferences, junk)},
			{"a last number below the entries", func(b backendTx) error { return b.setLastNumber(3) }, "number 8"},
		}
		var all []string
		for _, d := range damages {
			all = append(all, d.repair)
			t.Run(d.name, func(t *testing.T) {
				update(t, s, func(tx *Tx) error { return d.do(tx.b) })
				if got := rebuildWrites(t, s); !slices.Equal(got, []string{d.repair}) {
					t.Errorf("RebuildIndex() wrote %q, want %q", got, d.repair)
				}
				checkIndexState(t, s, before, ops...)
				checkConsistent(t, s)
			})
		}

		// All the damage at once.
		update(t, s, func(tx *Tx) error {
			for _, d := range damages {
				if err := d.do(tx.b); err != nil {
					return err
				}
			}
			return nil
		})
		slices.Sort(all)
		if got := rebuildWrites(t, s); !slices.Equal(got, all) {
			t.Errorf("RebuildIndex() of all the damage wrote %q, want %q", got, all)
		}
		checkIndexState(t, s, before, ops...)
		checkConsistent(t, s)
	})

	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, c := benchStore(t, tb, filepath.Join(t.TempDir(), "data"), true)
		t.Cleanup(func() { benchClose(t, s, c) })
		fillIndexStore(t, s)
		lastCommit := func() (seq uint64) {
			t.Helper()
			if err := logDB(t, s).View(t.Context(), func(tx *logdb.Tx) error {
				seq = tx.Seq()
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			return seq
		}
		rebuild := func() { update(t, s, func(tx *Tx) error { return tx.RebuildIndex() }) }

		seq, counts := lastCommit(), c.Counts()
		rebuild()
		if got, now := lastCommit(), c.Counts(); got != seq || now != counts {
			t.Errorf("RebuildIndex() of an index that is right has written: the last commit is %d, want %d; the files "+
				"have %+v, want %+v", got, seq, now, counts)
		}

		update(t, s, func(tx *Tx) error { return tx.b.indexDelete(indexReferences, published) })
		seq, counts = lastCommit(), c.Counts()
		rebuild()
		const most = 1024
		if got, written := lastCommit(), c.Counts().BytesWritten-counts.BytesWritten; got != seq+1 || written > most {
			t.Errorf("RebuildIndex() of an index with a row missing has made %d commits and written %d bytes, want 1 "+
				"and at most %d", got-seq, written, most)
		}
		checkConsistent(t, s)
	})
}

// decodesAsRow reports whether value is a row of summaries by its form alone, whatever document it is the row of.
func decodesAsRow(value []byte) bool {
	var r summaryRow
	return json.Unmarshal(value, &r, json.RejectUnknownMembers(true)) == nil
}

// TestIndexDamagedRows checks that the reads of the index fail, with the row or the key in the error, for the rows and
// keys that the index cannot have, which damage or another program may leave, for each way in which they are wrong; and
// that RebuildIndex repairs them.
func TestIndexDamagedRows(t *testing.T) {
	ops := []string{"op_A", "op_B"}
	const nul = "\x00"
	join := func(fields ...string) string { return strings.Join(fields, nul) }
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, _ := newStore(t, tb)
		fillIndexStore(t, s)
		before := readIndexState(t, s, ops...)
		write := func(t *testing.T, table uint32, key string, value []byte) {
			t.Helper()
			rawUpdate(t, s, func(tx *logdb.Tx) error { return tx.Put(table, []byte(key), value) })
		}
		repair := func(t *testing.T) {
			t.Helper()
			update(t, s, func(tx *Tx) error { return tx.RebuildIndex() })
			checkIndexState(t, s, before, ops...)
			checkConsistent(t, s)
		}
		// row returns the value of the row of summaries of the document of kind with id.
		row := func(kind content.RefKind, id string) []byte {
			t.Helper()
			var value []byte
			if err := logDB(t, s).View(t.Context(), func(tx *logdb.Tx) error {
				value = bytes.Clone(mustGet(tx, tableSummaries, string(docKey(kind, id))))
				return nil
			}); err != nil || value == nil {
				t.Fatalf("the row of the %s %q = %q, %v", kind, id, value, err)
			}
			return value
		}
		checkError := func(t *testing.T, what string, err error, want string) {
			t.Helper()
			if err == nil || err.Error() != want || errors.Is(err, fs.ErrNotExist) {
				t.Errorf("%s = %v, want an error %q that does not wrap fs.ErrNotExist", what, err, want)
			}
		}

		t.Run("values", func(t *testing.T) {
			for _, tt := range []struct {
				name   string
				kind   content.RefKind
				id     string
				change func(r *summaryRow)
			}{
				{"an entry with the pointer of a term", content.RefEntry, pageID, func(r *summaryRow) {
					r.Entry, r.Term = nil, &content.Term{ID: termID}
				}},
				{"an entry without its pointer", content.RefEntry, pageID, func(r *summaryRow) { r.Entry = nil }},
				{"an entry with the pointers of a term and a form", content.RefEntry, pageID, func(r *summaryRow) {
					r.Term, r.Form = &content.Term{ID: termID}, &content.Form{ID: formID}
				}},
				{"a term with the pointer of an entry", content.RefTerm, termID, func(r *summaryRow) {
					r.Term, r.Entry = nil, &content.Entry{ID: pageID}
				}},
				{"a term with the pointer of a medium", content.RefTerm, termID, func(r *summaryRow) {
					r.Term, r.Media = nil, &content.Media{ID: mediaID}
				}},
				{"a term with the pointers of a term and a medium", content.RefTerm, termID, func(r *summaryRow) {
					r.Media = &content.Media{ID: mediaID}
				}},
				{"a term with a published revision", content.RefTerm, termID, func(r *summaryRow) {
					r.Published = &content.Revision{Slug: "x"}
				}},
				{"a term with a last revision", content.RefTerm, termID, func(r *summaryRow) { r.Latest = revisionID }},
				{"a form without its pointer", content.RefForm, formID, func(r *summaryRow) { r.Form = nil }},
				{"the settings document with a pointer", SettingsKind, "", func(r *summaryRow) {
					r.Form = &content.Form{ID: formID}
				}},
				{"a term with the role published", content.RefTerm, termID, func(r *summaryRow) {
					r.setRefs(RolePublished, []string{mediaID})
				}},
				{"a term with the role latest", content.RefTerm, termID, func(r *summaryRow) {
					r.setRefs(RoleLatest, []string{mediaID})
				}},
				{"a term with the role scheduled", content.RefTerm, termID, func(r *summaryRow) {
					r.setRefs(RoleScheduled, []string{mediaID})
				}},
				{"a term with the role publishes", content.RefTerm, termID, func(r *summaryRow) {
					r.setRefs(rolePublishes, []string{revisionID})
				}},
				{"the settings document with the role latest", SettingsKind, "", func(r *summaryRow) {
					r.setRefs(RoleLatest, []string{mediaID})
				}},
				{"the settings document with the role scheduled", SettingsKind, "", func(r *summaryRow) {
					r.setRefs(RoleScheduled, []string{mediaID})
				}},
				{"an entry with an unknown role", content.RefEntry, pageID, func(r *summaryRow) {
					r.setRefs("draft", []string{mediaID})
				}},
				{"a term with an unknown role", content.RefTerm, termID, func(r *summaryRow) {
					r.setRefs("draft", []string{mediaID})
				}},
			} {
				t.Run(tt.name, func(t *testing.T) {
					r, err := readRow(tt.kind, docKey(tt.kind, tt.id), row(tt.kind, tt.id))
					if err != nil {
						t.Fatal(err)
					}
					tt.change(&r)
					value, err := r.marshal()
					if err != nil {
						t.Fatal(err)
					}
					if !decodesAsRow(value) {
						t.Fatalf("the row %s does not decode, so it does not test the kind of the document", value)
					}
					key := docKey(tt.kind, tt.id)
					write(t, tableSummaries, string(key), value)
					want := fmt.Sprintf("summaries %q: invalid value", key)
					view(t, s, func(tx *Tx) error {
						_, err := tx.Summary(tt.kind, tt.id)
						checkError(t, "Summary()", err, want)
						_, err = tx.Summaries(tt.kind)
						checkError(t, "Summaries()", err, want)
						return nil
					})
					// A write of the document fails: the old references are not known.
					err = s.Update(t.Context(), func(tx *Tx) error {
						_, err := tx.Put(tt.kind, tt.id, docJSON(tt.kind, tt.id), userID)
						return err
					})
					checkError(t, "Put()", err, want)
					if tt.kind == content.RefEntry {
						err = s.Update(t.Context(), func(tx *Tx) error {
							_, err := tx.AddRevision(tt.id, revision4, revisionJSON(`"slug": "x"`), userID)
							return err
						})
						checkError(t, "AddRevision()", err, want)
					}
					repair(t)
				})
			}
		})

		t.Run("keys of summaries", func(t *testing.T) {
			for _, tt := range []struct {
				name string
				kind content.RefKind
				id   string // of the key
				like string // the ID of a document of the kind, whose row the row has
			}{
				{"an ID that is not one", content.RefTerm, "trm_x", termID},
				{"an ID of another kind", content.RefTerm, pageID, termID},
				{"an ID in lower case", content.RefTerm, strings.ToLower(termID), termID},
				{"an ID with a byte more", content.RefTerm, termID + "0", termID},
				{"an ID with a NUL", content.RefTerm, term2 + nul + "x", termID},
				{"an empty ID", content.RefTerm, "", termID},
				{"an entry with the ID of a term", content.RefEntry, termID, entryID},
				{"the settings document with an ID", SettingsKind, entryID, ""},
			} {
				t.Run(tt.name, func(t *testing.T) {
					key := docKey(tt.kind, tt.id)
					write(t, tableSummaries, string(key), row(tt.kind, tt.like))
					view(t, s, func(tx *Tx) error {
						_, err := tx.Summaries(tt.kind)
						checkError(t, "Summaries()", err, fmt.Sprintf("summaries %q: invalid key", key))
						// The rows of the other kinds are read.
						if sums, err := tx.Summaries(content.RefMedia); err != nil || len(sums) != 1 {
							t.Errorf("Summaries(media) = %d summaries, %v; want 1", len(sums), err)
						}
						return nil
					})
					repair(t)
				})
			}
		})

		// The keys of references that no document can make, under the ID of a medium, which Referrers reads; and
		// under that of a revision, which AddRevision reads for the entries that publish it.
		type keyCase struct{ name, rest string }
		invalidKeys := func(t *testing.T, target string, cases []keyCase, read func(tx *Tx) error) {
			t.Helper()
			for _, tt := range cases {
				t.Run(tt.name, func(t *testing.T) {
					key := join(target, tt.rest)
					write(t, tableReferences, key, nil)
					err := s.Update(t.Context(), read)
					checkError(t, "the read", err, fmt.Sprintf("references %q: invalid key", key))
					repair(t)
				})
			}
		}
		t.Run("keys of references for Referrers", func(t *testing.T) {
			invalidKeys(t, mediaID, []keyCase{
				{"nothing after the target", ""},
				{"a kind alone", "entry"},
				{"no role field", join("entry", entryID)},
				{"a field more", join("entry", entryID, "document", "x")},
				{"a kind without summaries", join("revision", revisionID, "document")},
				{"an unknown kind", join("page", entryID, "document")},
				{"an empty kind", join("", entryID, "document")},
				{"an ID that is not one", join("entry", "ent_x", "document")},
				{"an ID of another kind", join("entry", termID, "document")},
				{"an empty ID", join("entry", "", "document")},
				{"the settings document with an ID", join("settings", entryID, "document")},
				{"an unknown role", join("entry", entryID, "draft")},
				{"an empty role", join("entry", entryID, "")},
				{"publishes, which only a revision is the target of", join("entry", entryID, "publishes")},
				{"published of a term", join("term", termID, "published")},
				{"latest of the settings document", join("settings", "", "latest")},
				{"scheduled of a term", join("term", termID, "scheduled")},
				{"scheduled of the settings document", join("settings", "", "scheduled")},
				{"publishes of a term", join("term", termID, "publishes")},
			}, func(tx *Tx) error {
				_, err := tx.Referrers(content.RefMedia, mediaID)
				return err
			})
			// A target that no document refers to.
			invalidKeys(t, menuID, []keyCase{
				{"a menu as the target", join("entry", entryID, "document")},
			}, func(tx *Tx) error {
				_, err := tx.Referrers(content.RefMenu, menuID)
				return err
			})
		})
		t.Run("keys of references for AddRevision", func(t *testing.T) {
			invalidKeys(t, revision4, []keyCase{
				{"nothing after the target", ""},
				{"no role field", join("entry", otherEntry)},
				{"a field more", join("entry", otherEntry, "publishes", "x")},
				{"a kind without summaries", join("revision", revision5, "publishes")},
				{"an unknown kind", join("page", otherEntry, "publishes")},
				{"an ID that is not one", join("entry", "ent_x", "publishes")},
				{"an ID of another kind", join("entry", termID, "publishes")},
				{"the settings document with an ID", join("settings", otherEntry, "publishes")},
				{"an unknown role", join("entry", otherEntry, "draft")},
				{"publishes of a term", join("term", termID, "publishes")},
				{"publishes of the settings document", join("settings", "", "publishes")},
				{"document, which a revision is not the target of", join("entry", otherEntry, "document")},
				{"published, which a revision is not the target of", join("entry", otherEntry, "published")},
				{"scheduled, which a revision is not the target of", join("entry", otherEntry, "scheduled")},
			}, func(tx *Tx) error {
				_, err := tx.AddRevision(pageID, revision4, revisionJSON(`"slug": "x"`), userID)
				return err
			})
			// With no damage, the revision is added, and the entries that publish it get it.
			update(t, s, func(tx *Tx) error {
				data := publishedEntry(otherEntry, 9, revision4)
				if _, err := tx.Put(content.RefEntry, otherEntry, data, userID); err != nil {
					return err
				}
				_, err := tx.AddRevision(pageID, revision4, revisionJSON(`"slug": "x"`), userID)
				return err
			})
			checkConsistent(t, s)
			view(t, s, func(tx *Tx) error {
				if sum, err := tx.Summary(content.RefEntry, otherEntry); err != nil || sum.Published == nil ||
					sum.Published.Slug != "x" {
					t.Errorf("Summary() = %s, %v; want the published revision", summaryText(sum), err)
				}
				return nil
			})
		})
	})
}

// TestSplitRefKey checks which keys of references the index can have, and the document and role that they say.
func TestSplitRefKey(t *testing.T) {
	ids := map[content.RefKind]string{
		content.RefEntry: entryID, content.RefTerm: termID, content.RefMedia: mediaID, content.RefAuthor: authorID,
		content.RefMenu: menuID, content.RefForm: formID, content.RefRedirect: redirectID, SettingsKind: "",
	}
	if len(ids) != len(indexKinds) {
		t.Fatalf("the test has the IDs of %d kinds, and the index has documents of %d", len(ids), len(indexKinds))
	}
	// The parser takes the kinds that checkKey does.
	for kind := range decoders {
		if !slices.Contains(indexKinds, kind) {
			t.Errorf("the kind %q is one of a document, and the index has no summaries of it", kind)
		}
	}
	for _, kind := range indexKinds {
		id, ok := ids[kind]
		if !ok {
			t.Fatalf("the test has no ID of the kind %q", kind)
		}
		key := refKey(termID, kind, id, RoleDocument)
		if got, ok := splitRefKey(key); !ok || got != (Referrer{Kind: kind, ID: id, Role: RoleDocument}) {
			t.Errorf("splitRefKey(%q) = %+v, %v; want the %s %q with the role document", key, got, ok, kind, id)
		}
	}
	for _, tt := range []struct {
		target string
		role   Role
		valid  bool
	}{
		{termID, RoleDocument, true}, {mediaID, RolePublished, true}, {formID, RoleLatest, true},
		{entryID, RoleScheduled, true}, {revisionID, rolePublishes, true}, {authorID, RoleDocument, true},
		{revisionID, RoleDocument, false}, {revisionID, RolePublished, false}, {revisionID, RoleLatest, false},
		{revisionID, RoleScheduled, false}, {termID, rolePublishes, false}, {menuID, RoleDocument, false},
		{userID, RoleDocument, false},
		{"", RoleDocument, false}, {termID, "", false},
	} {
		key := refKey(tt.target, content.RefEntry, entryID, tt.role)
		got, ok := splitRefKey(key)
		want := Referrer{Kind: content.RefEntry, ID: entryID, Role: tt.role}
		if ok != tt.valid || ok && got != want || !ok && got != (Referrer{}) {
			t.Errorf("splitRefKey(%q) = %+v, %v; want %+v, %v", key, got, ok, want, tt.valid)
		}
	}
	for _, kind := range indexKinds {
		if kind == content.RefEntry {
			continue
		}
		for _, role := range []Role{RolePublished, RoleLatest, RoleScheduled, rolePublishes} {
			target := termID
			if role == rolePublishes {
				target = revisionID
			}
			if got, ok := splitRefKey(refKey(target, kind, ids[kind], role)); ok {
				t.Errorf("splitRefKey() of the %s %q with the role %s = %+v, true; want false: only an entry has it",
					kind, ids[kind], role, got)
			}
		}
	}
}

// injectDocument makes data the current version of the document of kind with id behind the back of the store, past its
// index, as a document that another version of this program has written.
func injectDocument(t *testing.T, s *Store, kind content.RefKind, id string, data []byte) {
	t.Helper()
	rawUpdate(t, s, func(tx *logdb.Tx) error {
		sum, err := tx.PutObject(data)
		if err != nil {
			return err
		}
		last, _ := decodeUint(mustGet(tx, tableMeta, metaVersions))
		seq := int64(last) + 1
		version := appendVersion(seq, sum[:], testTime.UTC().Format(savedAtLayout), userID)
		return errors.Join(
			tx.Put(tableMeta, []byte(metaVersions), binary.AppendUvarint(nil, uint64(seq))),
			tx.Put(tableHistory, historyKey(kind, id, seq), version),
			tx.Put(tableDocuments, docKey(kind, id), version),
		)
	})
}

// injectRevision adds the revision id of the entry entry with data behind the back of the store, past its index.
func injectRevision(t *testing.T, s *Store, entry, id string, data []byte) {
	t.Helper()
	rawUpdate(t, s, func(tx *logdb.Tx) error {
		sum, err := tx.PutObject(data)
		if err != nil {
			return err
		}
		last, _ := decodeUint(mustGet(tx, tableMeta, metaRevisions))
		seq := int64(last) + 1
		value := appendRevision(seq, entry, sum, testTime.UTC().Format(savedAtLayout), userID)
		return errors.Join(
			tx.Put(tableMeta, []byte(metaRevisions), binary.AppendUvarint(nil, uint64(seq))),
			tx.Put(tableRevisions, []byte(id), value),
			tx.Put(tableEntryRevisions, entryKey(entry, seq), []byte(id)),
		)
	})
}

// mustGet returns the value of key in table, or nil.
func mustGet(tx *logdb.Tx, table uint32, key string) []byte {
	v, _ := tx.Get(table, []byte(key))
	return v
}

// TestIndexUndecodable checks that documents and revisions that do not decode, as those that a later version of the
// program has written, have no summary and no projection, make neither Put nor RebuildIndex fail, and are not in the
// way of the other documents.
func TestIndexUndecodable(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, _ := newStore(t, tb)
		fillIndexStore(t, s)
		later := `"schemaVersion": 2`
		// An entry document, and the revision that an entry publishes, which do not decode.
		injectDocument(t, s, content.RefEntry, otherEntry, []byte(`{`+later+`, "id": "`+otherEntry+`"}`))
		injectDocument(t, s, content.RefTerm, term2, []byte(`{`+later+`, "id": "`+term2+`", "parent": "`+termID+`"}`))
		putAll(t, s, indexDoc{content.RefEntry, lateEntry, publishedEntry(lateEntry, 12, revision5)})
		injectRevision(t, s, lateEntry, revision5, []byte(`{`+later+`, "featuredImage": "`+medium2+`"}`))
		// An entry that schedules the revision that does not decode.
		putAll(t, s, indexDoc{content.RefEntry, thirdEntry, scheduledEntry(thirdEntry, 3, revision5)})

		update(t, s, func(tx *Tx) error { return tx.RebuildIndex() })
		checkConsistent(t, s)
		view(t, s, func(tx *Tx) error {
			checkNoSummary(t, tx, content.RefEntry, otherEntry)
			checkNoSummary(t, tx, content.RefTerm, term2)
			// The revision does not decode, so it is not projected; the JSON still says what it refers to, for the entry
			// whose last revision it is, and not for the one that schedules it.
			checkSummary(t, tx, content.RefEntry, lateEntry, Summary{
				Entry: &content.Entry{
					ID: lateEntry, Number: 12, Status: content.StatusPublished, PublishedRevision: revision5,
				},
				Latest: revision5,
			})
			checkSummary(t, tx, content.RefEntry, thirdEntry, Summary{Entry: &content.Entry{
				ID: thirdEntry, Number: 3, Status: content.StatusScheduled, ScheduledRevision: revision5,
				ScheduledAt: scheduledAt,
			}})
			checkReferrers(t, tx, content.RefMedia, medium2, "entry "+entryID+" latest", "entry "+lateEntry+" latest")
			// The term that does not decode refers to nothing in the index.
			checkReferrers(t, tx, content.RefTerm, termID, "entry "+entryID+" published", "menu "+menuID+" document")
			if docs, err := tx.Documents(content.RefTerm); err != nil || len(docs) != 2 {
				t.Errorf("Documents() = %d documents, %v; want both terms", len(docs), err)
			}
			return nil
		})

		// Saving documents goes on, also those that have no summary or that publish the revision that does not decode.
		update(t, s, func(tx *Tx) error {
			for _, d := range []indexDoc{
				{content.RefEntry, lateEntry, publishedEntry(lateEntry, 12, revision5)},
				{content.RefEntry, lateEntry, publishedEntry(lateEntry, 13, revision5)},
				{content.RefTerm, termID, docJSON(content.RefTerm, termID)},
			} {
				if _, err := tx.Put(d.kind, d.id, d.data, userID); err != nil {
					return err
				}
			}
			if _, err := tx.AddRevision(otherEntry, revision4, revisionJSON(`"slug": "x"`), userID); err != nil {
				return err
			}
			_, err := tx.Delete(content.RefEntry, otherEntry, userID)
			return err
		})
		checkConsistent(t, s)
		view(t, s, func(tx *Tx) error {
			checkNoSummary(t, tx, content.RefEntry, otherEntry)
			if n, err := tx.LastNumber(); err != nil || n != 13 {
				t.Errorf("LastNumber() = %d, %v; want 13", n, err)
			}
			return nil
		})
	})
}

// TestIndexUpgrade checks that a store of the table schema 1, as the program wrote it before the index, can be read but
// for its index by a reader, gets its index, equal to the one that it would have had, and the schema 6 with the record
// store.upgrade when its writer opens it, once; that a store of the schema 2, which has the index without the scheduled
// items, of the schema 3, which has no accounts, of the schema 4, which has no sessions, or of the schema 5, which has
// no audit log, gets the schema 6, the record and its head in one commit that writes nothing else and reads no
// document; and that a failure of any of them leaves the store as it was. A damaged object stops the upgrade of the
// schema 1, which makes the index from the documents, and not that of the schema 2 to 5, which leaves the object to the
// reads that use it. The stores of the schemas before 6 are made of a store of the schema 6 as the programs of those
// schemas left them: without the audit events and the heads.
func TestIndexUpgrade(t *testing.T) {
	ops := []string{"op_A", "op_B"}
	// clearTables deletes every row of the tables.
	clearTables := func(tx *logdb.Tx, tables ...uint32) error {
		for _, table := range tables {
			var keys [][]byte
			tx.Scan(table, nil, func(key, _ []byte) bool {
				keys = append(keys, bytes.Clone(key))
				return true
			})
			for _, key := range keys {
				if err := tx.Delete(table, key); err != nil {
					return err
				}
			}
		}
		return nil
	}
	// toSchema1 removes the rows of the tables that the schema 2 has, and the number, and makes the schema 1.
	toSchema1 := func(t *testing.T, s *Store) {
		t.Helper()
		rawUpdate(t, s, func(tx *logdb.Tx) error {
			err := clearTables(tx, tableSummaries, tableReferences, tableOperations, tableAccounts, tableSessions)
			if err != nil {
				return err
			}
			return errors.Join(tx.Delete(tableMeta, []byte(metaNumbers)),
				tx.Put(tableMeta, []byte(metaSchema), []byte{1}))
		})
	}
	// toSchemaWithIndex returns the function that makes the schema 2, 3, 4 or 5: the schemas 2, 3 and 4 have no sessions,
	// the schemas 2 and 3 have no accounts, while the schemas 4 and 5 have one, and the schema 5 has a session. The rows
	// of the index stay: in a store without entries that have scheduled items, which no program of the schema 2 could
	// save, they are those of the schemas 3, 4, 5 and 6.
	toSchemaWithIndex := func(schema byte) func(t *testing.T, s *Store) {
		return func(t *testing.T, s *Store) {
			t.Helper()
			rawUpdate(t, s, func(tx *logdb.Tx) error {
				if err := clearTables(tx, tableAccounts, tableSessions); err != nil {
					return err
				}
				if schema >= 4 {
					if err := tx.Put(tableAccounts, []byte(userID), []byte("row")); err != nil {
						return err
					}
				}
				if schema >= 5 {
					if err := tx.Put(tableSessions, []byte(sessionKeyOf("a")), []byte("session")); err != nil {
						return err
					}
				}
				return tx.Put(tableMeta, []byte(metaSchema), []byte{schema})
			})
		}
	}
	toOlder := map[uint64]func(t *testing.T, s *Store){2: toSchemaWithIndex(2), 3: toSchemaWithIndex(3),
		4: toSchemaWithIndex(4), 5: toSchemaWithIndex(5)}
	// upgradeRecord is the record store.upgrade that the writer of a store of the schema from adds when it opens it, at
	// testTime.
	upgradeRecord := func(from uint64) string {
		return fmt.Sprintf(`{"log":"activity","n":1,"at":"2026-11-03T08:00:00.12Z","actor":"system",`+
			`"action":"store.upgrade","detail":{"from":"%d","to":"%d"}}`, from, logSchemaVersion)
	}
	schemaOf := func(t *testing.T, s *Store) uint64 {
		t.Helper()
		var version uint64
		if err := logDB(t, s).View(t.Context(), func(tx *logdb.Tx) error {
			version, _ = decodeUint(mustGet(tx, tableMeta, metaSchema))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return version
	}
	seqOf := func(t *testing.T, s *Store) uint64 {
		t.Helper()
		var seq uint64
		if err := logDB(t, s).View(t.Context(), func(tx *logdb.Tx) error {
			seq = tx.Seq()
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return seq
	}
	// tablesOf returns the rows of every table of the engine of s, which the schema has, by the number of the table.
	tablesOf := func(t *testing.T, s *Store) map[uint32][][2]string {
		t.Helper()
		tables := map[uint32][][2]string{}
		for table := tableMeta; table <= tableSessions; table++ {
			tables[table] = rawTable(t, s, table)
		}
		return tables
	}
	// upgradedTablesOf returns the rows of the tables of s, a store of the schema from, 2 to 5, as its upgrade must leave
	// them: as they are, but for the schema 6 and the head of the activity log, which has the record store.upgrade. It
	// returns the last commit of s too.
	upgradedTablesOf := func(t *testing.T, s *Store, from uint64) (map[uint32][][2]string, uint64) {
		t.Helper()
		tables := tablesOf(t, s)
		for i, kv := range tables[tableMeta] {
			if kv[0] == metaSchema {
				tables[tableMeta][i][1] = string(binary.AppendUvarint(nil, logSchemaVersion))
			}
		}
		sum := sha256.Sum256([]byte(upgradeRecord(from)))
		head := [2]string{metaActivityLog, "\x01" + string(sum[:])}
		tables[tableMeta] = slices.Insert(tables[tableMeta], 0, head) // "activityLog" comes before the other keys
		return tables, seqOf(t, s)
	}
	// checkUpgradeRecord checks that s, which has just upgraded a store of the schema from, has the head of the record
	// store.upgrade, and if readLog is true, that this is the only record in its audit log, and that the log is whole.
	// A log with a changed byte cannot be read.
	checkUpgradeRecord := func(t *testing.T, s *Store, from uint64, readLog bool) {
		t.Helper()
		sum := sha256.Sum256([]byte(upgradeRecord(from)))
		heads, err := s.AuditHeads(t.Context())
		if want := []audit.Head{{Log: audit.ActivityLog, N: 1, Hash: sum}}; err != nil || !slices.Equal(heads, want) {
			t.Errorf("AuditHeads() after Open() = %v, %v; want %v", heads, err, want)
		}
		if !readLog {
			return
		}
		events := rawEvents(t, s)
		if len(events) != 1 || string(events[0].Data) != upgradeRecord(from) {
			t.Errorf("the events after Open() = %d; want the one record %s", len(events), upgradeRecord(from))
		}
		if err := s.VerifyAudit(t.Context(), heads); err != nil {
			t.Errorf("VerifyAudit() after Open() = %v", err)
		}
	}
	// checkOnlySchema checks that s, which has just upgraded a store of the schema from, 2 to 5, whose last commit was
	// seq, has the schema 6 and the record store.upgrade in one commit more, and the rows of the tables that
	// upgradedTablesOf has returned. It reads the log too if readLog is true, as checkUpgradeRecord does.
	checkOnlySchema := func(t *testing.T, s *Store, want map[uint32][][2]string, seq, from uint64, readLog bool) {
		t.Helper()
		if v := schemaOf(t, s); v != logSchemaVersion {
			t.Errorf("the schema after Open() = %d, want %d", v, logSchemaVersion)
		}
		if after := seqOf(t, s); after != seq+1 {
			t.Errorf("the last commit after Open() is %d, want %d: one commit more", after, seq+1)
		}
		if got := tablesOf(t, s); !reflect.DeepEqual(got, want) {
			for table := tableMeta; table <= tableSessions; table++ {
				if !reflect.DeepEqual(got[table], want[table]) {
					t.Errorf("table %d after Open() = %q, want %q: only the schema and the head change", table, got[table],
						want[table])
				}
			}
		}
		checkUpgradeRecord(t, s, from, readLog)
	}
	// acceptsAccounts checks that s, a store that has been upgraded, takes a row in accounts and reads it.
	acceptsAccounts := func(t *testing.T, s *Store) {
		t.Helper()
		update(t, s, func(tx *Tx) error { return tx.PutAccount(userID, []byte("row")) })
		view(t, s, func(tx *Tx) error {
			if data, ok, err := tx.Account(userID); err != nil || !ok || string(data) != "row" {
				t.Errorf("Account() after the upgrade = %q, %v, %v; want the row that was put", data, ok, err)
			}
			return nil
		})
	}
	// acceptsSessions checks that s, a store that has been upgraded, takes a row in sessions and reads it.
	acceptsSessions := func(t *testing.T, s *Store) {
		t.Helper()
		key := sessionKeyOf("a")
		update(t, s, func(tx *Tx) error { return tx.PutSession(key, []byte("session")) })
		view(t, s, func(tx *Tx) error {
			if data, ok, err := tx.Session(key); err != nil || !ok || string(data) != "session" {
				t.Errorf("Session() after the upgrade = %q, %v, %v; want the row that was put", data, ok, err)
			}
			return nil
		})
	}
	// secondOpen closes s, which has just upgraded the store in dir, and opens the store again with o: that commits
	// nothing, and changes no file but head, which every Open writes. It checks the index against the documents only if
	// indexed is true, which a damaged object makes fail.
	secondOpen := func(t *testing.T, s *Store, dir string, o logdb.Options, indexed bool) {
		t.Helper()
		seq := seqOf(t, s)
		if err := s.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}
		first := snapshot(t, dir)
		s = reopenLog(t, dir, o)
		if v := schemaOf(t, s); v != logSchemaVersion {
			t.Errorf("the schema after a second Open() = %d, want %d", v, logSchemaVersion)
		}
		if again := seqOf(t, s); again != seq {
			t.Errorf("the last commit after a second Open() is %d, want %d, that of the first", again, seq)
		}
		if indexed {
			checkConsistent(t, s)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}
		second := snapshot(t, dir)
		delete(first, "logdb/head")
		delete(second, "logdb/head")
		if !maps.Equal(first, second) {
			t.Errorf("a second Open() has changed the files of the store")
		}
	}

	t.Run("schema 1", func(t *testing.T) {
		forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
			s, dir := newStore(t, tb)
			fillIndexStore(t, s)
			// A document that does not decode has no summary in the index that the upgrade makes.
			injectDocument(t, s, content.RefEntry, otherEntry, []byte(`{"schemaVersion": 2}`))
			want := readIndexState(t, s, ops...)
			toSchema1(t, s)
			if err := s.Close(); err != nil {
				t.Fatalf("Close() = %v", err)
			}
			dropAudit(t, dir)

			// A reader reads everything but the index.
			r, err := openLogReader(dir, tb.options())
			if err != nil {
				t.Fatalf("openLogReader() of a store of the schema 1 = %v", err)
			}
			view(t, r, func(tx *Tx) error {
				if docs, err := tx.Documents(content.RefEntry); err != nil || len(docs) != 3 {
					t.Errorf("Documents() of a store of the schema 1 = %d documents, %v; want 3", len(docs), err)
				}
				if rev, err := tx.Revision(revisionID); err != nil || rev.Entry != entryID {
					t.Errorf("Revision() of a store of the schema 1 = %+v, %v", rev, err)
				}
				// The accounts and the sessions need no index.
				if rows, err := tx.Accounts(); err != nil || len(rows) != 0 {
					t.Errorf("Accounts() of a store of the schema 1 = %s, %v; want none", show(rows), err)
				}
				if data, ok, err := tx.Account(userID); data != nil || ok || err != nil {
					t.Errorf("Account() of a store of the schema 1 = %q, %v, %v; want none", data, ok, err)
				}
				if rows, err := tx.Sessions(); err != nil || len(rows) != 0 {
					t.Errorf("Sessions() of a store of the schema 1 = %s, %v; want none", show(rows), err)
				}
				if data, ok, err := tx.Session(sessionKeyOf("a")); data != nil || ok || err != nil {
					t.Errorf("Session() of a store of the schema 1 = %q, %v, %v; want none", data, ok, err)
				}
				for name, err := range map[string]error{
					"Summary":   func() error { _, err := tx.Summary(content.RefEntry, entryID); return err }(),
					"Summaries": func() error { _, err := tx.Summaries(content.RefEntry); return err }(),
					"Referrers": func() error { _, err := tx.Referrers(content.RefMedia, mediaID); return err }(),
					"LastNumber": func() error {
						_, err := tx.LastNumber()
						return err
					}(),
				} {
					if !errors.Is(err, ErrNoIndex) {
						t.Errorf("%s() of a store of the schema 1 = %v, want ErrNoIndex", name, err)
					}
				}
				return nil
			})
			if _, err := r.AuditHeads(t.Context()); !errors.Is(err, ErrNoAuditLog) {
				t.Errorf("AuditHeads() of a store of the schema 1 = %v, want ErrNoAuditLog", err)
			}
			if err := r.Close(); err != nil {
				t.Fatalf("Close() of the reader = %v", err)
			}

			// Its writer builds the index in one commit, and sets the schema 6 and the record.
			s = reopenLog(t, dir, *tb.log)
			if v := schemaOf(t, s); v != logSchemaVersion {
				t.Errorf("the schema after Open() = %d, want %d", v, logSchemaVersion)
			}
			checkUpgradeRecord(t, s, 1, true)
			checkConsistent(t, s)
			checkIntegrity(t, s)
			got := readIndexState(t, s, ops...)
			if problem := diffIndex(got.rows, want.rows); problem != "" || got.number != want.number {
				t.Errorf("the index after Open() differs from the one that the store had: %s; the last number %d, want %d",
					problem, got.number, want.number)
			}
			if len(got.ops) != 0 {
				t.Errorf("the operations after Open() = %q, want none: a store of the schema 1 has none", got.ops)
			}
			acceptsAccounts(t, s)
			acceptsSessions(t, s)
			secondOpen(t, s, dir, *tb.log, true)
		})
	})

	// A store of the schema 2, which has the index without the scheduled items, one of the schema 3, which has no
	// accounts, one of the schema 4, which has no sessions, and one of the schema 5, which has no audit log, get the
	// schema 6 with the index, the accounts and the sessions that they have.
	for _, schema := range []uint64{2, 3, 4, 5} {
		t.Run(fmt.Sprintf("schema %d", schema), func(t *testing.T) {
			forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
				s, dir := newStore(t, tb)
				fillIndexStore(t, s)
				// An entry that is scheduled without the items, which a program of the schema 2 or 3 could save as the store does
				// not check it against the site, and a document that does not decode.
				putAll(t, s, indexDoc{content.RefEntry, page2, entryJSON(page2, `"number": 20, "status": "scheduled"`)})
				injectDocument(t, s, content.RefEntry, otherEntry, []byte(`{"schemaVersion": 2}`))
				want := readIndexState(t, s, ops...)
				toOlder[schema](t, s)
				if err := s.Close(); err != nil {
					t.Fatalf("Close() = %v", err)
				}
				dropAudit(t, dir)

				// A reader reads the index as it is, the operations, the accounts and the sessions that the schema has, and
				// finds no audit log.
				r, err := openLogReader(dir, tb.options())
				if err != nil {
					t.Fatalf("openLogReader() of a store of the schema %d = %v", schema, err)
				}
				if v := schemaOf(t, r); v != schema {
					t.Errorf("the schema read by the reader = %d, want %d", v, schema)
				}
				got := readIndexState(t, r, ops...)
				if problem := diffIndex(got.rows, want.rows); problem != "" || got.number != want.number ||
					!maps.Equal(got.ops, want.ops) || len(got.ops) == 0 {
					t.Errorf("the index read in a store of the schema %d differs: %s; the last number %d, want %d; the "+
						"operations %q, want %q", schema, problem, got.number, want.number, got.ops, want.ops)
				}
				var wantAccounts []AccountRow
				if schema >= 4 {
					wantAccounts = []AccountRow{{userID, []byte("row")}}
				}
				var wantSessions []SessionRow
				if schema >= 5 {
					wantSessions = []SessionRow{{sessionKeyOf("a"), []byte("session")}}
				}
				checkAccounts(t, r, wantAccounts, otherUser)
				checkSessions(t, r, wantSessions, sessionKeyOf("b"))
				if _, err := r.AuditHeads(t.Context()); !errors.Is(err, ErrNoAuditLog) {
					t.Errorf("AuditHeads() of a store of the schema %d = %v, want ErrNoAuditLog", schema, err)
				}
				wantTables, seq := upgradedTablesOf(t, r, schema)
				if err := r.Close(); err != nil {
					t.Fatalf("Close() of the reader = %v", err)
				}

				// Its writer sets the schema 6 and the record in one commit, and writes nothing else.
				s = reopenLog(t, dir, *tb.log)
				checkOnlySchema(t, s, wantTables, seq, schema, true)
				checkConsistent(t, s)
				checkIntegrity(t, s)
				checkAccounts(t, s, wantAccounts, otherUser)
				checkSessions(t, s, wantSessions, sessionKeyOf("b"))
				got = readIndexState(t, s, ops...)
				if problem := diffIndex(got.rows, want.rows); problem != "" || got.number != want.number ||
					!maps.Equal(got.ops, want.ops) {
					t.Errorf("the index after Open() differs from the one that the store had: %s; the last number %d, want %d; "+
						"the operations %q, want %q", problem, got.number, want.number, got.ops, want.ops)
				}
				acceptsAccounts(t, s)
				acceptsSessions(t, s)
				secondOpen(t, s, dir, *tb.log, true)
			})
		})
	}

	t.Run("failure", func(t *testing.T) {
		// A damaged object stops the upgrade of the schema 1, which makes its index from the documents.
		t.Run("schema 1", func(t *testing.T) {
			o := logdb.Options{Placement: logdb.Files, WriterWait: writerWait}
			s, dir := newLogStore(t, o)
			fillIndexStore(t, s)
			toSchema1(t, s)
			corruptObjects(t, s)
			if err := s.Close(); err != nil {
				t.Fatalf("Close() = %v", err)
			}
			dropAudit(t, dir)
			// The error says where the upgrade failed, and wraps the cause.
			upgrade := filepath.Join(dir, "logdb") + ": cannot upgrade the table schema to 6, so the store is unchanged: "
			var first string
			for i := range 2 {
				c, err := open(dir, logOpts(o))
				if err == nil {
					_ = c.Close()
				}
				var cause string
				if u := errors.Unwrap(err); u != nil {
					cause = u.Error()
				}
				// The second failure is the same: the first has released the lock.
				switch {
				case err == nil || err.Error() != upgrade+cause || !strings.HasPrefix(cause, "object sha256:") ||
					!strings.Contains(cause, ": the data has the hash "):
					t.Fatalf("open() of a store of the schema 1 with a changed object = %v, want the error of its "+
						"upgrade and, after it, the error of its hash", err)
				case i == 0:
					first = err.Error()
				case err.Error() != first:
					t.Errorf("the second open() = %v, want the error of the first, %v", err, first)
				}
			}
			r, err := openLogReader(dir, logOpts(o))
			if err != nil {
				t.Fatalf("openLogReader() after the failures = %v", err)
			}
			t.Cleanup(func() { _ = r.Close() })
			if v := schemaOf(t, r); v != 1 {
				t.Errorf("the schema after the failed Open() = %d, want 1", v)
			}
			view(t, r, func(tx *Tx) error {
				if _, err := tx.Summary(content.RefEntry, entryID); !errors.Is(err, ErrNoIndex) {
					t.Errorf("Summary() = %v, want ErrNoIndex", err)
				}
				return nil
			})
		})

		// A write to the log that fails in the Update of the upgrade fails Open, and leaves the store as it was.
		t.Run("write fault", func(t *testing.T) {
			for _, schema := range []uint64{2, 3, 4, 5} {
				t.Run(fmt.Sprintf("schema %d", schema), func(t *testing.T) {
					o := logdb.Options{Placement: logdb.Files, WriterWait: writerWait}
					fsys := simfs.New("data")
					opts := options{log: &o, fs: fsys, now: func() time.Time { return testTime }}
					s, err := create("data", opts)
					if err != nil {
						t.Fatalf("create() = %v", err)
					}
					t.Cleanup(func() { _ = s.Close() })
					fillIndexStore(t, s)
					toOlder[schema](t, s)
					want := readIndexState(t, s, ops...)
					if err := s.Close(); err != nil {
						t.Fatalf("Close() = %v", err)
					}
					logFS, err := fsys.Sub("logdb")
					if err != nil {
						t.Fatal(err)
					}
					rewriteEngineIn(t, logFS, dropAuditRewrite())
					_ = logFS.Close()
					reader, err := openLogReader("data", opts)
					if err != nil {
						t.Fatalf("openLogReader() of a store of the schema %d = %v", schema, err)
					}
					wantTables, seq := upgradedTablesOf(t, reader, schema)
					if err := reader.Close(); err != nil {
						t.Fatalf("Close() of the reader = %v", err)
					}

					// Open of the engine writes nothing to the log, so the first write is that of the Update of the upgrade.
					fsys.Inject(simfs.Fault{Match: func(op simfs.Op) bool {
						return op.Kind == simfs.OpWrite && strings.HasPrefix(op.Path, "logdb/log/")
					}, Err: syscall.EIO, Times: 1})
					c, err := open("data", opts)
					if err == nil {
						_ = c.Close()
					}
					var cause string
					if u := errors.Unwrap(err); u != nil {
						cause = u.Error()
					}
					upgrade := "data/logdb: cannot upgrade the table schema to 6, so the store is unchanged: "
					if err == nil || !errors.Is(err, syscall.EIO) || err.Error() != upgrade+cause {
						t.Fatalf("open() with a write that fails = %v, want the error of its upgrade, which wraps EIO", err)
					}

					// A reader sees the store as it was.
					r, err := openLogReader("data", opts)
					if err != nil {
						t.Fatalf("openLogReader() after the failure = %v", err)
					}
					if v := schemaOf(t, r); v != schema {
						t.Errorf("the schema after the failed Open() = %d, want %d", v, schema)
					}
					if after := seqOf(t, r); after != seq {
						t.Errorf("the last commit after the failed Open() is %d, want %d: nothing is committed", after, seq)
					}
					checkIndexState(t, r, want, ops...)
					if err := r.Close(); err != nil {
						t.Fatalf("Close() of the reader = %v", err)
					}

					// An Open without the fault upgrades it.
					again, err := open("data", opts)
					if err != nil {
						t.Fatalf("open() after the failure = %v", err)
					}
					t.Cleanup(func() { _ = again.Close() })
					checkOnlySchema(t, again, wantTables, seq, schema, true)
					checkConsistent(t, again)
					checkIntegrity(t, again)
					checkIndexState(t, again, want, ops...)
					acceptsAccounts(t, again)
					acceptsSessions(t, again)
				})
			}
		})
	})

	// A damaged object does not stop the upgrade of the schema 2 to 5, which reads no document: the reads that use the
	// object fail as before.
	t.Run("damaged object", func(t *testing.T) {
		for _, schema := range []uint64{2, 3, 4, 5} {
			t.Run(fmt.Sprintf("schema %d", schema), func(t *testing.T) {
				forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
					s, dir := newStore(t, tb)
					fillIndexStore(t, s)
					toOlder[schema](t, s)
					var doc Document
					view(t, s, func(tx *Tx) (err error) {
						doc, err = tx.Document(content.RefEntry, entryID)
						return err
					})
					want := readIndexState(t, s, ops...)
					if err := s.Close(); err != nil {
						t.Fatalf("Close() = %v", err)
					}
					dropAudit(t, dir)
					r, err := openLogReader(dir, tb.options())
					if err != nil {
						t.Fatalf("openLogReader() of a store of the schema %d = %v", schema, err)
					}
					wantTables, seq := upgradedTablesOf(t, r, schema)
					corrupt := corruptObjects(t, r)
					if err := r.Close(); err != nil {
						t.Fatalf("Close() of the reader = %v", err)
					}

					// Its writer sets the schema 6 and the record in one commit, and writes nothing else.
					s = reopenLog(t, dir, *tb.log)
					checkOnlySchema(t, s, wantTables, seq, schema, false)
					checkIndexState(t, s, want, ops...)
					view(t, s, func(tx *Tx) error {
						wantErr := "object " + doc.Hash + ": the data has the hash " + sum(corrupt(doc.Data))
						if _, err := tx.Document(content.RefEntry, entryID); err == nil || err.Error() != wantErr {
							t.Errorf("Document() of a damaged object = %v, want %q", err, wantErr)
						}
						return nil
					})
					acceptsAccounts(t, s)
					acceptsSessions(t, s)
					secondOpen(t, s, dir, *tb.log, false)
				})
			})
		}
	})
}

// TestRefsOf checks which strings of a JSON document are references.
func TestRefsOf(t *testing.T) {
	const tg = "tg_01JB8X4M7NB5D8F1H4J7K2M6P9"
	quote := func(s string) string { return `"` + s + `"` }
	array := func(ss ...string) string {
		quoted := make([]string, len(ss))
		for i, s := range ss {
			quoted[i] = quote(s)
		}
		return "[" + strings.Join(quoted, ", ") + "]"
	}
	// "trm_" and the ULID, with the first letter written as an escape of JSON.
	escaped := `"` + string(rune(92)) + "u0074" + termID[1:] + `"`
	notIDs := array("ent_x", strings.ToLower(entryID), entryID+"0", entryID[:len(entryID)-1], "ent_8"+entryID[5:],
		"ent_01JB8W2N5KD3F6H9J1M4P7R2TU", " "+entryID, "see "+entryID+" here", "", "ent_")
	for _, tt := range []struct {
		name string
		json string
		self string
		want []string
	}{
		{"a string at the root", quote(termID), "", []string{termID}},
		{"strings in an array", array(termID, entryID), "", []string{entryID, termID}},
		{"strings in nested objects and arrays",
			`{"a": {"b": [{"c": ` + quote(mediaID) + `}], "d": ` + quote(authorID) + `}, "e": ` + quote(formID) + `}`,
			"", []string{authorID, formID, mediaID}},
		{"the names of objects are not values", `{` + quote(termID) + `: "x", ` + quote(entryID) + `: 1}`, "", nil},
		{"the value of a name that is an ID", `{` + quote(entryID) + `: ` + quote(termID) + `}`, "", []string{termID}},
		{"the ID of the document itself", `{"id": ` + quote(entryID) + `, "parent": ` + quote(pageID) + `}`, entryID,
			[]string{pageID}},
		{"IDs of other kinds are not references", array(revisionID, userID, tg, menuID, redirectID, formID), "",
			[]string{formID}},
		{"strings that are not IDs", notIDs, "", nil},
		{"duplicates once", `[` + quote(termID) + `, ` + quote(termID) + `, {"x": ` + quote(termID) + `}]`, "",
			[]string{termID}},
		{"values that are not strings", `[1, true, false, null, 1.5, ` + quote(termID) + `]`, "", []string{termID}},
		{"a string with an escape", escaped, "", []string{termID}},
		{"nothing", `{}`, "", nil},
		{"nothing in an array", `[]`, "", nil},
		{"deep", strings.Repeat("[", 100) + quote(termID) + strings.Repeat("]", 100), "", []string{termID}},
		{"a revision with the entry as self",
			`{"schemaVersion": 1, "parent": ` + quote(entryID) + `, "featuredImage": ` + quote(mediaID) + `}`, entryID,
			[]string{mediaID}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := refsOf([]byte(tt.json), tt.self); !slices.Equal(got, tt.want) {
				t.Errorf("refsOf(%s, %q) = %q, want %q", tt.json, tt.self, got, tt.want)
			}
		})
	}
}

// TestOperations checks the operations that the store keeps for the save operations.
func TestOperations(t *testing.T) {
	long := "op_" + strings.Repeat("x", 253)
	eachStore(t, func(t *testing.T, s *Store) {
		view(t, s, func(tx *Tx) error {
			if data, ok, err := tx.Operation("op_A"); data != nil || ok || err != nil {
				t.Errorf("Operation() of an operation that is missing = %q, %v, %v; want none", data, ok, err)
			}
			return nil
		})
		update(t, s, func(tx *Tx) error {
			for id, data := range map[string][]byte{"op_A": []byte("first"), "op_B": nil, long: []byte("long")} {
				if err := tx.PutOperation(id, data); err != nil {
					return err
				}
			}
			// What the Update has put is there for the reads in it, as a copy.
			data, ok, err := tx.Operation("op_A")
			if err != nil || !ok || string(data) != "first" {
				t.Errorf("Operation() in the Update = %q, %v, %v; want first", data, ok, err)
			}
			data[0] = 'X'
			return nil
		})
		check := func(want map[string]string) {
			t.Helper()
			view(t, s, func(tx *Tx) error {
				for id, wantData := range want {
					data, ok, err := tx.Operation(id)
					if err != nil || ok != (wantData != "-") || ok && string(data) != wantData {
						t.Errorf("Operation(%.8q) = %q, %v, %v; want %q", id, data, ok, err, wantData)
					}
					if ok && data == nil {
						t.Errorf("Operation(%.8q) has nil data, want data that are not nil", id)
					}
				}
				return nil
			})
		}
		check(map[string]string{"op_A": "first", "op_B": "", long: "long", "op_C": "-"})

		// The data of an operation are replaced.
		update(t, s, func(tx *Tx) error { return tx.PutOperation("op_A", []byte("second")) })
		check(map[string]string{"op_A": "second", "op_B": "", long: "long"})

		for _, tt := range []struct{ id, want string }{
			{"", "invalid operation ID: it is empty"},
			{long + "x", "invalid operation ID: it has 257 bytes (max 256)"},
		} {
			update(t, s, func(tx *Tx) error {
				if err := tx.PutOperation(tt.id, []byte("x")); err == nil || err.Error() != tt.want {
					t.Errorf("PutOperation(%.8q) = %v, want %q", tt.id, err, tt.want)
				}
				if _, _, err := tx.Operation(tt.id); err == nil || err.Error() != tt.want {
					t.Errorf("Operation(%.8q) = %v, want %q", tt.id, err, tt.want)
				}
				return nil
			})
		}
		view(t, s, func(tx *Tx) error {
			if err := tx.PutOperation("op_C", nil); err == nil || err.Error() != "read-only transaction" {
				t.Errorf("PutOperation() in a View = %v, want a read-only transaction", err)
			}
			return nil
		})
		check(map[string]string{"op_C": "-"})
	})

	// The operations stay when the store is closed and opened again.
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, dir := newStore(t, tb)
		update(t, s, func(tx *Tx) error { return tx.PutOperation("op_A", []byte("first")) })
		if err := s.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}
		s = reopenLog(t, dir, *tb.log)
		if got := readIndexState(t, s, "op_A"); got.ops["op_A"] != "first" {
			t.Errorf("the operations after open() = %q, want first", got.ops)
		}
	})
}

// listOperations returns the rows that Operations passes as "<first 8 bytes of the ID>=<data>", until it has passed
// stopAfter of them (0: to the end).
func listOperations(t *testing.T, tx *Tx, stopAfter int) []string {
	t.Helper()
	var got []string
	err := tx.Operations(func(id string, data []byte) bool {
		if data == nil {
			t.Errorf("Operations() passes nil data for %.8q, want data that are not nil", id)
		}
		got = append(got, fmt.Sprintf("%.8s=%s", id, data))
		return stopAfter == 0 || len(got) < stopAfter
	})
	if err != nil {
		t.Errorf("Operations() = %v", err)
	}
	return got
}

// TestListOperations checks that Operations passes every row of the operations in the order of the IDs, in a View and in
// an Update that sees what it has written, stops when fn returns false, passes a copy of the data, and lets fn delete
// the rows that it is passed.
func TestListOperations(t *testing.T) {
	long := "op_" + strings.Repeat("x", 253)
	eachStore(t, func(t *testing.T, s *Store) {
		view(t, s, func(tx *Tx) error {
			if got := listOperations(t, tx, 0); got != nil {
				t.Errorf("Operations() of a store without operations passes %q, want none", got)
			}
			return nil
		})
		want := []string{"op_A=first", "op_B=", "op_xxxxx=long"}
		update(t, s, func(tx *Tx) error {
			for id, data := range map[string][]byte{long: []byte("long"), "op_B": nil, "op_A": []byte("first")} {
				if err := tx.PutOperation(id, data); err != nil {
					return err
				}
			}
			if got := listOperations(t, tx, 0); !slices.Equal(got, want) {
				t.Errorf("Operations() in the Update passes %q, want %q", got, want)
			}
			return nil
		})
		view(t, s, func(tx *Tx) error {
			if got := listOperations(t, tx, 0); !slices.Equal(got, want) {
				t.Errorf("Operations() passes %q, want %q", got, want)
			}
			for n := 1; n <= 2; n++ {
				if got := listOperations(t, tx, n); !slices.Equal(got, want[:n]) {
					t.Errorf("Operations() that stops after %d rows passes %q, want %q", n, got, want[:n])
				}
			}
			// The data are a copy: fn may change them.
			if err := tx.Operations(func(_ string, data []byte) bool {
				clear(data)
				return true
			}); err != nil {
				t.Errorf("Operations() = %v", err)
			}
			if got := listOperations(t, tx, 0); !slices.Equal(got, want) {
				t.Errorf("Operations() after fn has changed the data passes %q, want %q", got, want)
			}
			return nil
		})

		// fn may delete the rows that it is passed: it still sees all of them, in order.
		update(t, s, func(tx *Tx) error {
			var seen []string
			if err := tx.Operations(func(id string, _ []byte) bool {
				seen = append(seen, id)
				if err := tx.DeleteOperation(id); err != nil {
					t.Errorf("DeleteOperation(%.8q) in Operations() = %v", id, err)
					return false
				}
				return true
			}); err != nil {
				t.Errorf("Operations() = %v", err)
			}
			if wantIDs := []string{"op_A", "op_B", long}; !slices.Equal(seen, wantIDs) {
				t.Errorf("Operations() that deletes passes %q, want %q", seen, wantIDs)
			}
			if got := listOperations(t, tx, 0); got != nil {
				t.Errorf("Operations() after the deletions passes %q, want none", got)
			}
			return nil
		})
		view(t, s, func(tx *Tx) error {
			if got := listOperations(t, tx, 0); got != nil {
				t.Errorf("Operations() after the Update passes %q, want none", got)
			}
			return nil
		})
	})

	// The rows stay when the store is closed and opened again, and a row that is not an ID of an operation fails the read.
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, dir := newStore(t, tb)
		update(t, s, func(tx *Tx) error {
			return errors.Join(tx.PutOperation("op_A", []byte("first")), tx.PutOperation("op_B", []byte("second")))
		})
		if err := s.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}
		s = reopenLog(t, dir, *tb.log)
		view(t, s, func(tx *Tx) error {
			if got, want := listOperations(t, tx, 0), []string{"op_A=first", "op_B=second"}; !slices.Equal(got, want) {
				t.Errorf("Operations() after open() passes %q, want %q", got, want)
			}
			return nil
		})
		for _, tt := range []struct {
			key    string
			passed []string // the rows that sort before the invalid one
		}{
			{"", nil},
			{"p" + strings.Repeat("k", maxOperationID), []string{"op_A", "op_B"}},
		} {
			rawUpdate(t, s, func(tx *logdb.Tx) error { return tx.Put(tableOperations, []byte(tt.key), []byte("x")) })
			want := fmt.Sprintf("operations %q: invalid key", tt.key)
			view(t, s, func(tx *Tx) error {
				var passed []string
				err := tx.Operations(func(id string, _ []byte) bool {
					passed = append(passed, id)
					return true
				})
				if err == nil || err.Error() != want {
					t.Errorf("Operations() with a row whose key has %d bytes = %v, want %q", len(tt.key), err, want)
				}
				if !slices.Equal(passed, tt.passed) {
					t.Errorf("Operations() with a row whose key has %d bytes passes %q before the error, want %q",
						len(tt.key), passed, tt.passed)
				}
				return nil
			})
			rawUpdate(t, s, func(tx *logdb.Tx) error { return tx.Delete(tableOperations, []byte(tt.key)) })
		}
	})
}

// TestDeleteOperation checks DeleteOperation: it deletes the data of an operation and nothing else, does nothing for an
// operation that has none, and fails for an ID that is not valid and in a View.
func TestDeleteOperation(t *testing.T) {
	long := "op_" + strings.Repeat("x", 253)
	eachStore(t, func(t *testing.T, s *Store) {
		update(t, s, func(tx *Tx) error {
			for _, id := range []string{"op_A", "op_B", long} {
				if err := tx.PutOperation(id, []byte(fmt.Sprintf("data %.5s", id))); err != nil {
					return err
				}
			}
			return nil
		})
		isThere := func(tx *Tx, id string) bool {
			t.Helper()
			_, ok, err := tx.Operation(id)
			if err != nil {
				t.Errorf("Operation(%.8q) = %v", id, err)
			}
			return ok
		}

		// An operation that has no data is not an error and writes nothing.
		var changed bool
		err := s.Update(t.Context(), func(tx *Tx) error {
			if err := tx.DeleteOperation("op_Z"); err != nil {
				return err
			}
			changed = tx.Changed()
			return nil
		})
		if err != nil || changed {
			t.Errorf("DeleteOperation() of an operation that has no data: Update() = %v, Changed() = %v; want nil and false", err,
				changed)
		}

		// One that has data: they are gone in the Update, and after it; the others stay.
		update(t, s, func(tx *Tx) error {
			if err := tx.DeleteOperation("op_B"); err != nil {
				return err
			}
			if !tx.Changed() {
				t.Errorf("Changed() after a DeleteOperation of an operation that has data = false")
			}
			if isThere(tx, "op_B") || !isThere(tx, "op_A") || !isThere(tx, long) {
				t.Errorf("Operation() in the Update after DeleteOperation(op_B): op_B %v, op_A %v, the long one %v; "+
					"want false, true, true", isThere(tx, "op_B"), isThere(tx, "op_A"), isThere(tx, long))
			}
			// Twice is not an error.
			return tx.DeleteOperation("op_B")
		})
		view(t, s, func(tx *Tx) error {
			if got, want := listOperations(t, tx, 0), []string{"op_A=data op_A", "op_xxxxx=data op_xx"}; !slices.Equal(got, want) {
				t.Errorf("the operations after DeleteOperation(op_B) are %q, want %q", got, want)
			}
			return nil
		})

		// The longest ID.
		update(t, s, func(tx *Tx) error { return tx.DeleteOperation(long) })
		view(t, s, func(tx *Tx) error {
			if isThere(tx, long) || !isThere(tx, "op_A") {
				t.Errorf("Operation() after DeleteOperation of the long ID: the long one %v, op_A %v; want false, true",
					isThere(tx, long), isThere(tx, "op_A"))
			}
			return nil
		})

		// An Update that deletes and fails leaves the data.
		errTest := errors.New("test")
		if err := s.Update(t.Context(), func(tx *Tx) error {
			return errors.Join(tx.DeleteOperation("op_A"), errTest)
		}); !errors.Is(err, errTest) {
			t.Errorf("Update() = %v, want the error of f", err)
		}
		view(t, s, func(tx *Tx) error {
			if !isThere(tx, "op_A") {
				t.Errorf("Operation(op_A) after an Update that deleted it and failed = false, want true")
			}
			return nil
		})

		for _, tt := range []struct{ id, want string }{
			{"", "invalid operation ID: it is empty"},
			{long + "x", "invalid operation ID: it has 257 bytes (max 256)"},
		} {
			update(t, s, func(tx *Tx) error {
				if err := tx.DeleteOperation(tt.id); err == nil || err.Error() != tt.want {
					t.Errorf("DeleteOperation(%.8q) = %v, want %q", tt.id, err, tt.want)
				}
				return nil
			})
		}
		view(t, s, func(tx *Tx) error {
			if err := tx.DeleteOperation("op_A"); err == nil || err.Error() != "read-only transaction" {
				t.Errorf("DeleteOperation() in a View = %v, want a read-only transaction", err)
			}
			if !isThere(tx, "op_A") {
				t.Errorf("Operation(op_A) after a DeleteOperation in a View = false, want true")
			}
			return nil
		})
	})

	// A deletion needs a record, as every write does, and the data stay gone when the store is closed and opened again.
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, dir := newStore(t, tb)
		update(t, s, func(tx *Tx) error {
			return errors.Join(tx.PutOperation("op_A", []byte("first")), tx.PutOperation("op_B", []byte("second")))
		})
		if err := s.Update(t.Context(), func(tx *Tx) error { return tx.DeleteOperation("op_A") }); !errors.Is(err, ErrUnrecorded) {
			t.Errorf("Update() that deletes an operation and adds no record = %v, want ErrUnrecorded", err)
		}
		if got := readIndexState(t, s, "op_A", "op_B"); got.ops["op_A"] != "first" || got.ops["op_B"] != "second" {
			t.Errorf("the operations after the Update that is refused = %q, want both", got.ops)
		}
		if err := s.Update(t.Context(), func(tx *Tx) error {
			return errors.Join(tx.DeleteOperation("op_A"), tx.Record(eventRecord("delete")))
		}); err != nil {
			t.Fatalf("Update() that deletes an operation and adds a record = %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}
		s = reopenLog(t, dir, *tb.log)
		if got := readIndexState(t, s, "op_A", "op_B"); len(got.ops) != 1 || got.ops["op_B"] != "second" {
			t.Errorf("the operations after open() = %q, want op_B alone", got.ops)
		}
		if n := count(t, s, "operations"); n != 1 {
			t.Errorf("rows of operations = %d, want 1", n)
		}
	})
}

// TestIndexErrors checks the errors of the reads of the index for names that no document has.
func TestIndexErrors(t *testing.T) {
	const use = ` and a ULID (26 characters in upper case)`
	eachStore(t, func(t *testing.T, s *Store) {
		fillIndexStore(t, s)
		view(t, s, func(tx *Tx) error {
			for _, tt := range []struct {
				call func() error
				want string
			}{
				{func() error { _, err := tx.Summary("page", entryID); return err }, `unsupported kind "page"`},
				{func() error { _, err := tx.Summary(content.RefRevision, revisionID); return err },
					`unsupported kind "revision"`},
				{func() error { _, err := tx.Summary(content.RefEntry, "ent_x"); return err },
					`invalid entry ID "ent_x": use "ent_"` + use},
				{func() error { _, err := tx.Summary(content.RefEntry, termID); return err },
					`invalid entry ID "` + termID + `": use "ent_"` + use},
				{func() error { _, err := tx.Summary(SettingsKind, entryID); return err },
					`the settings document has no ID`},
				{func() error { _, err := tx.Summary(content.RefEntry, otherEntry); return err },
					`entries/` + otherEntry + `.json: not found`},
				{func() error { _, err := tx.Summary(content.RefMedia, medium2); return err },
					`media/` + medium2 + `.json: not found`},
				{func() error { _, err := tx.Summaries("page"); return err }, `unsupported kind "page"`},
				{func() error { _, err := tx.Summaries(""); return err }, `unsupported kind ""`},
				{func() error { _, err := tx.Referrers("page", entryID); return err }, `unsupported kind "page"`},
				{func() error { _, err := tx.Referrers(content.RefTerm, "trm_x"); return err },
					`invalid term ID "trm_x": use "trm_"` + use},
				{func() error { _, err := tx.Referrers(SettingsKind, entryID); return err },
					`the settings document has no ID`},
			} {
				if err := tt.call(); err == nil || err.Error() != tt.want {
					t.Errorf("error = %v, want %q", err, tt.want)
				}
			}
			for _, call := range []func() error{
				func() error { _, err := tx.Summary(content.RefEntry, otherEntry); return err },
				func() error { _, err := tx.Summary(content.RefMedia, medium2); return err },
			} {
				if err := call(); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("error = %v, want an error that wraps fs.ErrNotExist", err)
				}
			}
			// A medium that is deleted still has what refers to it, and an ID that nothing refers to has none.
			checkReferrers(t, tx, content.RefMedia, medium2, "entry "+entryID+" latest")
			checkReferrers(t, tx, content.RefTerm, "trm_01JE0000000000000000000099")
			return nil
		})
	})
}

// TestDiffVariants checks that the data that the generator of the differential tests means to be valid decode, so that
// it exercises the index: the data of diffIndexData for each document of their kinds, and those of diffRevisionData;
// and that its IDs and rows of accounts, and its keys and rows of sessions, are valid or invalid as it means.
func TestDiffVariants(t *testing.T) {
	for _, k := range diffKinds {
		for _, v := range diffIndexData[k.kind] {
			for _, id := range k.ids {
				if _, err := checkData(k.kind, id, v.data(id)); err != nil {
					t.Errorf("%s of %q: %v", v.name, id, err)
				}
			}
		}
	}
	for kind := range diffIndexData {
		if !slices.ContainsFunc(diffKinds, func(k diffKind) bool { return k.kind == kind }) {
			t.Errorf("diffIndexData has the kind %q, which the generator has no documents of", kind)
		}
	}
	for _, v := range diffRevisionData {
		if !v.valid {
			continue
		}
		if _, err := decodeRevisionData(revisionID, v.data(revisionID)); err != nil {
			t.Errorf("revision %s: %v", v.name, err)
		}
	}
	for _, id := range diffAccounts {
		if err := content.CheckID(content.RefUser, id); err != nil {
			t.Errorf("the account %q is meant to be valid: %v", id, err)
		}
	}
	for _, id := range diffInvalidAccounts {
		if content.CheckID(content.RefUser, id) == nil {
			t.Errorf("the account %q is meant to be invalid", id)
		}
	}
	for _, v := range diffAccountData {
		if n := len(v.data()); n < 1 || n > maxAccountBytes {
			t.Errorf("the row %s has %d bytes, want 1 to %d", v.name, n, maxAccountBytes)
		}
	}
	for _, v := range diffInvalidAccountData {
		if n := len(v.data()); n >= 1 && n <= maxAccountBytes {
			t.Errorf("the row %s has %d bytes, want none or more than %d", v.name, n, maxAccountBytes)
		}
	}
	for _, key := range diffSessions {
		if err := checkSessionKey(key); err != nil {
			t.Errorf("the session %q is meant to be valid: %v", key, err)
		}
	}
	for _, key := range diffInvalidSessions {
		if checkSessionKey(key) == nil {
			t.Errorf("the session %q is meant to be invalid", key)
		}
	}
	for _, v := range diffSessionData {
		if n := len(v.data()); n < 1 || n > maxSessionBytes {
			t.Errorf("the session row %s has %d bytes, want 1 to %d", v.name, n, maxSessionBytes)
		}
	}
	for _, v := range diffInvalidSessionData {
		if n := len(v.data()); n >= 1 && n <= maxSessionBytes {
			t.Errorf("the session row %s has %d bytes, want none or more than %d", v.name, n, maxSessionBytes)
		}
	}
}
