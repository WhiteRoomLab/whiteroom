package store

import (
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

const (
	revisionX    = "rev_01JE0000000000000000000091"
	revisionY    = "rev_01JE0000000000000000000092"
	missingEntry = "ent_01JE0000000000000000000098"
	lostTerm     = "trm_01JE0000000000000000000099"
)

// historyProblems returns the problems of ps that are of the history.
func historyProblems(ps []Problem) []Problem {
	return slices.DeleteFunc(slices.Clone(ps), func(p Problem) bool { return p.Area != AreaHistory })
}

// versionsOf returns the history of the document of kind with id.
func versionsOf(t *testing.T, s *Store, kind content.RefKind, id string) []Version {
	t.Helper()
	var versions []Version
	view(t, s, func(tx *Tx) (err error) {
		versions, err = tx.History(kind, id)
		return err
	})
	if len(versions) == 0 {
		t.Fatalf("History(%q, %q) = no version", kind, id)
	}
	return versions
}

// changes returns the change that makes each of cs, in order.
func changes(cs ...func(b backendTx) error) func(b backendTx) error {
	return func(b backendTx) error {
		for _, c := range cs {
			if err := c(b); err != nil {
				return err
			}
		}
		return nil
	}
}

// rawPut returns a change that puts a row into table past the shared code and the rules of the backend.
func rawPut(table uint32, key, value []byte) func(b backendTx) error {
	return func(b backendTx) error { return b.(*logTx).tx.Put(table, key, value) }
}

// noObject is the hash of an object that no store has, made from name.
func noObject(name string) string { return sum([]byte("no such object of " + name)) }

func missingObject(name string) string { return "the object " + noObject(name) + " is missing" }

// addUnreadVersion returns a change that saves a version of the document of kind with id that refers to an object that the
// store does not have, and then one of data, which becomes the current version, and a function that returns the seq of the
// first, once the change has run.
func addUnreadVersion(kind content.RefKind, id string, data []byte) (change func(b backendTx) error, first func() int64) {
	var seq int64
	change = func(b backendTx) error {
		at := testTime.UTC().Format(savedAtLayout)
		var err error
		if seq, err = b.addVersion(kind, id, noObject(id), at, userID); err != nil {
			return err
		}
		if err := b.addObject(hashOf(data), data); err != nil {
			return err
		}
		last, err := b.addVersion(kind, id, hashOf(data), at, userID)
		if err != nil {
			return err
		}
		return b.setCurrent(kind, id, last)
	}
	return change, func() int64 { return seq }
}

// addUnreadRevision returns a change that saves the revision id of the entry entry with an object that the store does not
// have, and then revisionY, with one that it has, so that id is not the last revision of the entry.
func addUnreadRevision(entry, id string) func(b backendTx) error {
	return func(b backendTx) error {
		at := testTime.UTC().Format(savedAtLayout)
		if _, err := b.addRevision(id, entry, noObject(id), at, userID); err != nil {
			return err
		}
		data := revJSON("after " + id)
		if err := b.addObject(hashOf(data), data); err != nil {
			return err
		}
		_, err := b.addRevision(revisionY, entry, hashOf(data), at, userID)
		return err
	}
}

// historyDamage is a damage of the tables of the store that newHistoryStore makes, and the problems that it makes.
type historyDamage struct {
	name string
	// only is true if the damage changes more than the history, and only the problems of the history are looked at.
	only bool
	// make returns the change, and the problems, which it may know only once the change has run.
	make func(t *testing.T, s *Store) (change func(b backendTx) error, want func() []Problem)
}

// problems returns the function of a damage that returns want.
func problems(want ...Problem) func() []Problem { return func() []Problem { return want } }

func historyDamages() []historyDamage {
	at := testTime.UTC().Format(savedAtLayout)
	older := func(name string, n int) historyDamage {
		return historyDamage{name, false, func(t *testing.T, s *Store) (func(b backendTx) error, func() []Problem) {
			vs := versionsOf(t, s, content.RefTerm, term4)
			return func(b backendTx) error { return b.setCurrent(content.RefTerm, term4, vs[n].Seq) },
				problems(Problem{AreaHistory, "documents term " + term4,
					fmt.Sprintf("it is version %d, but the last version of its history is %d", vs[n].Seq, vs[2].Seq)})
		}}
	}
	return []historyDamage{
		// The three versions of term4 give the same summary, so that nothing but the history tells which one is current.
		older("a row of the first version, which gives the same summary", 0),
		older("a row of the middle version", 1),
		{"a row that differs from the last version in its time", false, func(t *testing.T, s *Store) (func(b backendTx) error, func() []Problem) {
			vs := versionsOf(t, s, content.RefTerm, term4)
			d, err := digest(vs[2].Hash)
			if err != nil {
				t.Fatal(err)
			}
			row := appendVersion(vs[2].Seq, d[:], "2000-01-01T00:00:00.000000000Z", vs[2].SavedBy)
			return rawPut(tableDocuments, docKey(content.RefTerm, term4), row),
				problems(Problem{AreaHistory, "documents term " + term4, fmt.Sprintf("it differs from the version %d of its history", vs[2].Seq)})
		}},
		{"a row of a document whose last version is a deletion", true, func(t *testing.T, s *Store) (func(b backendTx) error, func() []Problem) {
			vs := versionsOf(t, s, content.RefMedia, medium2)
			last := vs[len(vs)-1]
			if last.Hash != "" {
				t.Fatalf("the last version of %s = %+v, want a deletion", medium2, last)
			}
			return func(b backendTx) error { return b.setCurrent(content.RefMedia, medium2, vs[0].Seq) },
				problems(Problem{AreaHistory, "documents media " + medium2,
					fmt.Sprintf("the row is there, but the last version of its history, %d, is a deletion", last.Seq)})
		}},
		{"no row of a document that is not deleted", true, func(t *testing.T, s *Store) (func(b backendTx) error, func() []Problem) {
			vs := versionsOf(t, s, content.RefTerm, term3)
			return func(b backendTx) error { return b.deleteCurrent(content.RefTerm, term3) },
				problems(Problem{AreaHistory, "documents term " + term3,
					fmt.Sprintf("the row is missing; the last version of its history is %d", vs[len(vs)-1].Seq)})
		}},
		{"a row of a document that has no history", true, func(t *testing.T, s *Store) (func(b backendTx) error, func() []Problem) {
			return func(b backendTx) error {
					row, ok, err := b.(*logTx).tx.Get(tableDocuments, docKey(content.RefTerm, term2))
					if err != nil {
						return err
					}
					if !ok {
						return fmt.Errorf("the row of %s is missing", term2)
					}
					return b.(*logTx).tx.Put(tableDocuments, docKey(content.RefTerm, lostTerm), row)
				},
				problems(Problem{AreaHistory, "documents term " + lostTerm, "the document has no version in its history"})
		}},
		{"a version that is not the last, whose object is missing", false, func(t *testing.T, s *Store) (func(b backendTx) error, func() []Problem) {
			change, first := addUnreadVersion(content.RefTerm, term2, docJSON(content.RefTerm, term2))
			return change, func() []Problem {
				return []Problem{{AreaHistory, versionTarget(content.RefTerm, term2, first()), missingObject(term2)}}
			}
		}},
		{"the last version, which its row does not name, and whose object is missing", false, func(t *testing.T, s *Store) (func(b backendTx) error, func() []Problem) {
			vs := versionsOf(t, s, content.RefTerm, term4)
			var last int64
			return func(b backendTx) (err error) {
					last, err = b.addVersion(content.RefTerm, term4, noObject(term4), at, userID)
					return err
				}, func() []Problem {
					return []Problem{
						{AreaHistory, "documents term " + term4,
							fmt.Sprintf("it is version %d, but the last version of its history is %d", vs[2].Seq, last)},
						{AreaHistory, versionTarget(content.RefTerm, term4, last), missingObject(term4)},
					}
				}
		}},
		{"a revision that is not the last of an entry without a document, whose object is missing", false, func(t *testing.T, s *Store) (func(b backendTx) error, func() []Problem) {
			return addUnreadRevision(missingEntry, revisionX),
				problems(Problem{AreaHistory, "revisions " + revisionX, missingObject(revisionX)})
		}},
		{"the last revision of an entry without a document, whose object is missing", false, func(t *testing.T, s *Store) (func(b backendTx) error, func() []Problem) {
			return func(b backendTx) error {
					_, err := b.addRevision(revisionX, missingEntry, noObject(revisionX), at, userID)
					return err
				},
				problems(Problem{AreaHistory, "revisions " + revisionX, missingObject(revisionX)})
		}},
		{"a revision that is not the last of an entry, whose object is missing", true, func(t *testing.T, s *Store) (func(b backendTx) error, func() []Problem) {
			return addUnreadRevision(page3, revisionX),
				problems(Problem{AreaHistory, "revisions " + revisionX, missingObject(revisionX)})
		}},
		// The index finds this one too, and stops at it.
		{"the last revision of an entry that has a document, whose object is missing", true, func(t *testing.T, s *Store) (func(b backendTx) error, func() []Problem) {
			return func(b backendTx) error {
					_, err := b.addRevision(revisionX, entryID, noObject(revisionX), at, userID)
					return err
				},
				problems(Problem{AreaHistory, "revisions " + revisionX, missingObject(revisionX)})
		}},
		{"the current version of a document, whose object is missing", true, func(t *testing.T, s *Store) (func(b backendTx) error, func() []Problem) {
			var seq int64
			return func(b backendTx) (err error) {
					if seq, err = b.addVersion(content.RefTerm, term2, noObject(term2), at, userID); err != nil {
						return err
					}
					return b.setCurrent(content.RefTerm, term2, seq)
				}, func() []Problem {
					return []Problem{{AreaHistory, versionTarget(content.RefTerm, term2, seq), missingObject(term2)}}
				}
		}},
		{"a row of entryRevisions that names a missing revision", false, func(t *testing.T, s *Store) (func(b backendTx) error, func() []Problem) {
			return rawPut(tableEntryRevisions, entryKey(missingEntry, 77), []byte(revisionX)),
				problems(Problem{AreaHistory, "entryRevisions " + missingEntry + " revision 77", "the revision " + revisionX + " is missing"})
		}},
		{"a row of entryRevisions that names a revision of another entry", false, func(t *testing.T, s *Store) (func(b backendTx) error, func() []Problem) {
			return rawPut(tableEntryRevisions, entryKey(missingEntry, 78), []byte(revisionID)),
				problems(Problem{AreaHistory, "entryRevisions " + missingEntry + " revision 78",
					"the revision " + revisionID + " is of the entry " + entryID})
		}},
		{"a revision that its entry does not list", false, func(t *testing.T, s *Store) (func(b backendTx) error, func() []Problem) {
			data := revJSON("unlisted")
			d, err := digest(hashOf(data))
			if err != nil {
				t.Fatal(err)
			}
			return func(b backendTx) error {
					if err := b.addObject(hashOf(data), data); err != nil {
						return err
					}
					seq, err := b.(*logTx).next(metaRevisions)
					if err != nil {
						return err
					}
					return b.(*logTx).tx.Put(tableRevisions, []byte(revisionX), appendRevision(seq, missingEntry, d, at, userID))
				},
				problems(Problem{AreaHistory, "revisions " + revisionX, "the entry " + missingEntry + " does not list it in entryRevisions"})
		}},
		{"a row of documents that does not decode", true, func(t *testing.T, s *Store) (func(b backendTx) error, func() []Problem) {
			return rawPut(tableDocuments, docKey(content.RefTerm, term2), []byte{0xff}),
				problems(Problem{AreaHistory, "documents term " + term2, "the row cannot be decoded"})
		}},
		{"a row of history that does not decode", false, func(t *testing.T, s *Store) (func(b backendTx) error, func() []Problem) {
			return rawPut(tableHistory, historyKey(content.RefTerm, lostTerm, 5), []byte{0xff}),
				problems(Problem{AreaHistory, versionTarget(content.RefTerm, lostTerm, 5), "the row cannot be decoded"})
		}},
		{"a key of documents that does not decode", false, func(t *testing.T, s *Store) (func(b backendTx) error, func() []Problem) {
			return rawPut(tableDocuments, []byte("garbage"), []byte{1}),
				problems(Problem{Area: AreaHistory, Detail: `documents "garbage": invalid key`})
		}},
		{"a key of history that does not decode", false, func(t *testing.T, s *Store) (func(b backendTx) error, func() []Problem) {
			return rawPut(tableHistory, []byte("garbage"), []byte{1}),
				problems(Problem{Area: AreaHistory, Detail: `history "garbage": invalid key`})
		}},
		{"a row of revisions that does not decode", false, func(t *testing.T, s *Store) (func(b backendTx) error, func() []Problem) {
			return rawPut(tableRevisions, []byte("rev_bad"), []byte{0xff}),
				problems(Problem{AreaHistory, "revisions rev_bad", "the row cannot be decoded"})
		}},
		{"a key of entryRevisions that does not decode", false, func(t *testing.T, s *Store) (func(b backendTx) error, func() []Problem) {
			return rawPut(tableEntryRevisions, []byte("short"), []byte(revisionX)),
				problems(Problem{Area: AreaHistory, Detail: `entryRevisions "short": invalid key`})
		}},
	}
}

// newHistoryStore makes a store of tb with the documents of fillChecked and fillHistory.
func newHistoryStore(t *testing.T, tb testBackend) *Store {
	t.Helper()
	s, _ := newStore(t, tb)
	fillChecked(t, s)
	fillHistory(t, s)
	return s
}

// TestCheckHistory checks that Check finds each difference between the tables of the versions and the revisions, and
// between them and their objects, as the one problem of that row, and no other where the damage does not reach the index.
func TestCheckHistory(t *testing.T) {
	for _, d := range historyDamages() {
		t.Run(d.name, func(t *testing.T) {
			forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
				s := newHistoryStore(t, tb)
				change, want := d.make(t, s)
				damage(t, s, change)
				got := findProblems(t, s)
				if d.only {
					got = historyProblems(got)
				}
				wantProblems(t, d.name, got, want()...)
			})
		})
	}
}

// TestCheckHistoryChangedObjects checks that each version that is not a deletion, and each revision, whose object has
// other data than its hash, is a problem that gives the hash of the data, in the order of the keys of the versions and then
// of the IDs of the revisions.
func TestCheckHistoryChangedObjects(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, _ := newStore(t, tb)
		fillChecked(t, s)
		type object struct {
			target, hash string
			data         []byte
		}
		var objects []object
		view(t, s, func(tx *Tx) error {
			read := func(target, hash string) error {
				data, err := tx.Object(hash)
				objects = append(objects, object{target, hash, data})
				return err
			}
			if err := tx.b.allVersions(func(v Version, err error) bool {
				if err != nil {
					t.Fatalf("allVersions() = %v", err)
				}
				if v.Hash != "" {
					if err := read(versionTarget(v.Kind, v.ID, v.Seq), v.Hash); err != nil {
						t.Fatalf("the object of %+v = %v", v, err)
					}
				}
				return true
			}); err != nil {
				return err
			}
			return tx.b.allRevisions(func(r Revision, err error) bool {
				if err != nil {
					t.Fatalf("allRevisions() = %v", err)
				}
				if err := read("revisions "+r.ID, r.Hash); err != nil {
					t.Fatalf("the object of %+v = %v", r, err)
				}
				return true
			})
		})
		if len(objects) != 15 {
			t.Fatalf("the store has %d versions and revisions with an object, want 15", len(objects))
		}
		corrupt := corruptObjects(t, s)
		var want []Problem
		for _, o := range objects {
			want = append(want, Problem{AreaHistory, o.target, "object " + o.hash + ": the data has the hash " + sum(corrupt(o.data))})
		}
		wantProblems(t, "versions and revisions with other data", historyProblems(findProblems(t, s)), want...)
	})
}

// TestCheckHistoryOrder checks the order of the problems of the history: the rows of documents, as the walk of the history
// meets them and then those that no history belongs to, then the rows of entryRevisions, the revisions that no row lists,
// the objects of the versions and then those of the revisions.
func TestCheckHistoryOrder(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s := newHistoryStore(t, tb)
		term4Versions := versionsOf(t, s, content.RefTerm, term4)
		unread, first := addUnreadVersion(content.RefTerm, term2, docJSON(content.RefTerm, term2))
		damage(t, s, changes(
			unread,
			addUnreadRevision(missingEntry, revisionX),
			func(b backendTx) error { return b.setCurrent(content.RefTerm, term4, term4Versions[0].Seq) },
			rawPut(tableDocuments, docKey(content.RefMedia, medium9), []byte{0xff}),
			rawPut(tableEntryRevisions, entryKey(missingEntry, 77), []byte(revisionY+"x"))))
		wantProblems(t, "problems of every part", historyProblems(findProblems(t, s)),
			Problem{AreaHistory, "documents term " + term4,
				fmt.Sprintf("it is version %d, but the last version of its history is %d", term4Versions[0].Seq, term4Versions[2].Seq)},
			Problem{AreaHistory, "documents media " + medium9, "the row cannot be decoded"},
			Problem{AreaHistory, "entryRevisions " + missingEntry + " revision 77", "the revision " + revisionY + "x is missing"},
			Problem{AreaHistory, versionTarget(content.RefTerm, term2, first()), missingObject(term2)},
			Problem{AreaHistory, "revisions " + revisionX, missingObject(revisionX)})
	})
}

// TestCheckOlderOriginals checks that Check reads the originals that only an earlier version of a medium refers to, also
// those of a medium that is deleted, and tells them from those that the current documents refer to.
func TestCheckOlderOriginals(t *testing.T) {
	const medium5 = "med_01JE0000000000000000000005"
	earlier := func(t *testing.T, f *originalsFixture, id string, n int) int64 {
		return versionsOf(t, f.s, content.RefMedia, id)[n].Seq
	}

	t.Run("an original that only an earlier version refers to", func(t *testing.T) {
		for _, tt := range []struct {
			name   string
			change func(t *testing.T, f *originalsFixture)
			reason func(f *originalsFixture) string
		}{
			{"missing", func(t *testing.T, f *originalsFixture) {
				if err := os.Remove(f.path("old")); err != nil {
					t.Fatal(err)
				}
			}, func(f *originalsFixture) string { return originalName(f.hash("old")) + ": not found" }},
			{"changed", func(t *testing.T, f *originalsFixture) {
				changed := append([]byte{'x'}, f.data["old"][1:]...)
				writeFile(t, f.path("old"), changed)
			}, func(f *originalsFixture) string {
				changed := append([]byte{'x'}, f.data["old"][1:]...)
				return "original " + f.hash("old") + ": the data has the hash " + sum(changed)
			}},
		} {
			t.Run(tt.name, func(t *testing.T) {
				f := newOriginalsFixture(t)
				tt.change(t, f)
				wantProblems(t, tt.name, findProblems(t, f.s), Problem{AreaOriginal, f.hash("old"),
					fmt.Sprintf("%s (only of earlier versions: media %s version %d)", tt.reason(f), medium3, earlier(t, f, medium3, 0))})
			})
		}
	})

	t.Run("an original of the current documents and of an earlier version is one problem", func(t *testing.T) {
		f := newOriginalsFixture(t)
		// medium2 refers to "old" now, and "a" only in its first version: "old" is in the current medium2 and in the
		// first version of medium3, and "a" in the current mediaID and in the first version of medium2.
		update(t, f.s, putDoc(content.RefMedia, medium2, mediaJSON(medium2, f.data["old"])))
		for _, name := range []string{"a", "old"} {
			if err := os.Remove(f.path(name)); err != nil {
				t.Fatal(err)
			}
		}
		wantProblems(t, "two originals", findProblems(t, f.s),
			Problem{AreaOriginal, f.hash("a"), fmt.Sprintf("%s: not found (of the media %s, and of earlier versions: media %s version %d)",
				originalName(f.hash("a")), mediaID, medium2, earlier(t, f, medium2, 0))},
			Problem{AreaOriginal, f.hash("old"), fmt.Sprintf("%s: not found (of the media %s, and of earlier versions: media %s version %d)",
				originalName(f.hash("old")), medium2, medium3, earlier(t, f, medium3, 0))})
	})

	t.Run("an original of a medium that is deleted", func(t *testing.T) {
		f := newOriginalsFixture(t)
		update(t, f.s, putDoc(content.RefMedia, medium5, mediaJSON(medium5, f.data["stray"])))
		update(t, f.s, func(tx *Tx) error {
			_, err := tx.Delete(content.RefMedia, medium5, userID)
			return err
		})
		wantProblems(t, "a deleted medium with an original", findProblems(t, f.s))
		if err := os.Remove(f.path("stray")); err != nil {
			t.Fatal(err)
		}
		wantProblems(t, "a deleted medium without its original", findProblems(t, f.s), Problem{AreaOriginal, f.hash("stray"),
			fmt.Sprintf("%s: not found (only of earlier versions: media %s version %d)", originalName(f.hash("stray")), medium5,
				earlier(t, f, medium5, 0))})
	})

	t.Run("a version that gives no original", func(t *testing.T) {
		f := newOriginalsFixture(t)
		// An earlier version that does not decode: it refers to no original, and is no problem.
		injectDocument(t, f.s, content.RefMedia, medium9, []byte(`{"schemaVersion": 2}`))
		update(t, f.s, putDoc(content.RefMedia, medium9, docJSON(content.RefMedia, medium9)))
		wantProblems(t, "a version that does not decode", findProblems(t, f.s))
	})
}
