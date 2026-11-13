package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// findProblems returns every problem that Check of s finds, in the order in which it finds them.
func findProblems(t *testing.T, s *Store) []Problem {
	t.Helper()
	var got []Problem
	if err := s.Check(t.Context(), func(p Problem) bool {
		got = append(got, p)
		return true
	}); err != nil {
		t.Fatalf("Check() = %v", err)
	}
	return got
}

func problemLines(ps []Problem) string {
	if len(ps) == 0 {
		return " none"
	}
	var b strings.Builder
	for _, p := range ps {
		fmt.Fprintf(&b, "\n\t%+v", p)
	}
	return b.String()
}

// wantProblems checks that got, the problems of what, are want, in this order.
func wantProblems(t *testing.T, what string, got []Problem, want ...Problem) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s: Check() found%s\nwant%s", what, problemLines(got), problemLines(want))
	}
}

// damage runs f in an update of the backend of s, past the shared code and its records.
func damage(t *testing.T, s *Store, f func(b backendTx) error) {
	t.Helper()
	if err := s.b.update(t.Context(), f); err != nil {
		t.Fatalf("damaging the store = %v", err)
	}
}

// fillChecked puts the original that the medium of fillIndexStore refers to, and then what fillIndexStore puts.
func fillChecked(t *testing.T, s *Store) {
	t.Helper()
	putOriginal(t, s, []byte("x"))
	fillIndexStore(t, s)
}

// rebuild runs RebuildIndex in an Update of s, and returns whether it has written.
func rebuild(t *testing.T, s *Store) (changed bool) {
	t.Helper()
	if err := s.Update(t.Context(), func(tx *Tx) error {
		if err := tx.RebuildIndex(); err != nil {
			return err
		}
		changed = tx.Changed()
		return recorded(tx)
	}); err != nil {
		t.Fatalf("RebuildIndex() = %v", err)
	}
	return changed
}

// dropSummary deletes the row of summaries of the document of kind with id.
func dropSummary(kind content.RefKind, id string) func(b backendTx) error {
	return func(b backendTx) error { return b.indexDelete(indexSummaries, docKey(kind, id)) }
}

func TestProblemString(t *testing.T) {
	for _, tt := range []struct {
		p    Problem
		want string
	}{
		{Problem{Area: AreaEngine, Detail: "bad CRC"}, "engine: bad CRC"},
		{Problem{Area: AreaIndex, Target: "summaries term trm_1", Detail: "the row is missing"},
			"index: summaries term trm_1: the row is missing"},
		{Problem{Area: AreaOriginal, Target: "sha256:ab", Detail: "a\nb\r\nc\rd"}, "original: sha256:ab: a b c d"},
	} {
		if got := tt.p.String(); got != tt.want {
			t.Errorf("%+v.String() = %q, want %q", tt.p, got, tt.want)
		}
	}
	if AreaEngine != "engine" || AreaIndex != "index" || AreaOriginal != "original" {
		t.Errorf("the areas are %q, %q and %q", AreaEngine, AreaIndex, AreaOriginal)
	}
}

// TestCheckRight checks that a store that is right has no problem, empty or not, for its writer, and for a reader
// beside it.
func TestCheckRight(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		eachStore(t, func(t *testing.T, s *Store) {
			wantProblems(t, "an empty store", findProblems(t, s))
		})
	})
	t.Run("filled", func(t *testing.T) {
		eachStore(t, func(t *testing.T, s *Store) {
			fillChecked(t, s)
			wantProblems(t, "a filled store", findProblems(t, s))
		})
	})
	t.Run("reader", func(t *testing.T) {
		forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
			s, dir := newStore(t, tb)
			wantProblems(t, "the empty store of the writer", findProblems(t, s))
			r, err := openLogReader(dir, tb.options())
			if err != nil {
				t.Fatalf("openLogReader() = %v", err)
			}
			t.Cleanup(func() { _ = r.Close() })
			wantProblems(t, "the empty store of the reader", findProblems(t, r))
			fillChecked(t, s)
			wantProblems(t, "the filled store of the writer", findProblems(t, s))
			wantProblems(t, "the filled store of the reader", findProblems(t, r))
		})
	})
}

// indexDamages are changes of the rows of the index of the store that fillChecked makes, with the one problem that each
// makes.
func indexDamages() []struct {
	name   string
	change func(b backendTx) error
	want   Problem
} {
	const missing, differs, extra = "the row is missing", "the row differs from the one that the documents give",
		"the row is not given by any document"
	return []struct {
		name   string
		change func(b backendTx) error
		want   Problem
	}{
		{"a missing summary", dropSummary(content.RefTerm, termID),
			Problem{AreaIndex, "summaries term " + termID, missing}},
		{"a missing summary of the settings", dropSummary(SettingsKind, ""),
			Problem{AreaIndex, "summaries settings", missing}},
		{"a changed summary", func(b backendTx) error {
			return b.indexPut(indexSummaries, docKey(content.RefTerm, termID), []byte("{}"))
		}, Problem{AreaIndex, "summaries term " + termID, differs}},
		{"an extra summary", func(b backendTx) error {
			return b.indexPut(indexSummaries, docKey(content.RefMedia, medium2), []byte("{}"))
		}, Problem{AreaIndex, "summaries media " + medium2, extra}},
		{"a missing reference", func(b backendTx) error {
			return b.indexDelete(indexReferences, refKey(mediaID, content.RefAuthor, authorID, RoleDocument))
		}, Problem{AreaIndex, "references author " + authorID + " -> " + mediaID + " (document)", missing}},
		{"an extra reference", func(b backendTx) error {
			return b.indexPut(indexReferences, refKey(formID, content.RefAuthor, authorID, RoleDocument), nil)
		}, Problem{AreaIndex, "references author " + authorID + " -> " + formID + " (document)", extra}},
		{"an extra reference of the settings", func(b backendTx) error {
			return b.indexPut(indexReferences, refKey(formID, SettingsKind, "", RoleDocument), nil)
		}, Problem{AreaIndex, "references settings -> " + formID + " (document)", extra}},
		{"a key that the index cannot have", func(b backendTx) error {
			return b.indexPut(indexReferences, []byte("garbage"), nil)
		}, Problem{AreaIndex, `references "garbage"`, extra}},
		{"a last number below the largest", func(b backendTx) error { return b.setLastNumber(1) },
			Problem{AreaIndex, "last number", "it is 1, but the largest number of the entries is 8"}},
	}
}

// TestCheckIndex checks that Check finds each difference between the index and what the documents give as the one
// problem of that row, and that RebuildIndex ends the problems.
func TestCheckIndex(t *testing.T) {
	for _, d := range indexDamages() {
		t.Run(d.name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, s *Store) {
				fillChecked(t, s)
				damage(t, s, d.change)
				wantProblems(t, d.name, findProblems(t, s), d.want)
				if !rebuild(t, s) {
					t.Errorf("RebuildIndex() has written nothing, want the repair of: %v", d.want)
				}
				wantProblems(t, "after RebuildIndex()", findProblems(t, s))
			})
		})
	}

	t.Run("all at once", func(t *testing.T) {
		eachStore(t, func(t *testing.T, s *Store) {
			fillChecked(t, s)
			var want []Problem
			for _, d := range indexDamages() {
				// The change of the summary of the term would be that of its missing row.
				if d.name == "a changed summary" {
					continue
				}
				damage(t, s, d.change)
				want = append(want, d.want)
			}
			got := findProblems(t, s)
			if last := got[len(got)-1]; last != want[len(want)-1] {
				t.Errorf("the last problem is %+v, want the last number: %+v", last, want[len(want)-1])
			}
			cmp := func(a, b Problem) int { return strings.Compare(a.String(), b.String()) }
			slices.SortFunc(got, cmp)
			slices.SortFunc(want, cmp)
			wantProblems(t, "all the changes", got, want...)
			rebuild(t, s)
			wantProblems(t, "after RebuildIndex()", findProblems(t, s))
		})
	})

	t.Run("a right index is not written", func(t *testing.T) {
		eachStore(t, func(t *testing.T, s *Store) {
			fillChecked(t, s)
			wantProblems(t, "before", findProblems(t, s))
			if rebuild(t, s) {
				t.Errorf("RebuildIndex() of an index that Check finds right has written")
			}
		})
	})

	t.Run("a last number that cannot be read", func(t *testing.T) {
		forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
			s, _ := newStore(t, tb)
			fillChecked(t, s)
			rawUpdate(t, s, func(tx *logdb.Tx) error { return tx.Put(tableMeta, []byte(metaNumbers), []byte{0x80}) })
			wantProblems(t, "the last number", findProblems(t, s),
				Problem{AreaIndex, "last number", `meta "numbers": invalid value`})
		})
	})

	t.Run("a document that does not decode has no row", func(t *testing.T) {
		forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
			s, _ := newStore(t, tb)
			fillChecked(t, s)
			injectDocument(t, s, content.RefEntry, otherEntry, []byte(`{"schemaVersion": 2}`))
			wantProblems(t, "an entry of a later schema", findProblems(t, s))
			// A row that such a document has is one that nothing gives.
			damage(t, s, func(b backendTx) error {
				return b.indexPut(indexSummaries, docKey(content.RefEntry, otherEntry), []byte("{}"))
			})
			wantProblems(t, "a row of an entry of a later schema", findProblems(t, s),
				Problem{AreaIndex, "summaries entry " + otherEntry, "the row is not given by any document"})
		})
	})
}

// dropIndexTables makes the store of s, which its writer has open, one of the table schema 1: without the rows of the
// index, the operations, the accounts and the sessions, and without the number.
func dropIndexTables(t *testing.T, s *Store) {
	t.Helper()
	rawUpdate(t, s, func(tx *logdb.Tx) error {
		for _, table := range []uint32{tableSummaries, tableReferences, tableOperations, tableAccounts, tableSessions} {
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
		return errors.Join(tx.Delete(tableMeta, []byte(metaNumbers)), tx.Put(tableMeta, []byte(metaSchema), []byte{1}))
	})
}

// TestCheckNoIndex checks that a store of the table schema 1 has the one problem that it has no index, and that Check
// goes on to the originals.
func TestCheckNoIndex(t *testing.T) {
	noIndex := Problem{Area: AreaIndex, Detail: ErrNoIndex.Error()}
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, dir := newStore(t, tb)
		fillChecked(t, s)
		dropIndexTables(t, s)
		wantProblems(t, "the writer of a store of the schema 1", findProblems(t, s), noIndex)

		if err := os.Remove(originalPath(dir, sum([]byte("x")))); err != nil {
			t.Fatal(err)
		}
		hash := sum([]byte("x"))
		wantProblems(t, "a store of the schema 1 without an original", findProblems(t, s), noIndex,
			Problem{AreaOriginal, hash, originalName(hash) + ": not found (of the media " + mediaID + ")"})

		// A reader of the store as an older program left it.
		if err := s.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}
		dropAudit(t, dir)
		r, err := openLogReader(dir, tb.options())
		if err != nil {
			t.Fatalf("openLogReader() of a store of the schema 1 = %v", err)
		}
		t.Cleanup(func() { _ = r.Close() })
		wantProblems(t, "the reader of a store of the schema 1", findProblems(t, r), noIndex,
			Problem{AreaOriginal, hash, originalName(hash) + ": not found (of the media " + mediaID + ")"})
	})
}

// originalsFixture is a store with originals and the media documents that refer to them.
type originalsFixture struct {
	s    *Store
	dir  string
	data map[string][]byte // the originals, by name
}

const (
	medium3 = "med_01JE0000000000000000000003"
	medium4 = "med_01JE0000000000000000000004"
	medium9 = "med_01JE0000000000000000000009"
)

// newOriginalsFixture makes a store with the originals "a", "b", "stray" and "old", and the media documents: mediaID
// and medium2 refer to "a", medium3 to "b" (it referred to "old" before), and medium4 has no hash. "stray" is
// referred to by no document.
func newOriginalsFixture(t *testing.T) *originalsFixture {
	t.Helper()
	s, dir := newStore(t, oneBackend[0])
	f := &originalsFixture{s: s, dir: dir, data: map[string][]byte{}}
	for _, name := range []string{"a", "b", "stray", "old"} {
		f.data[name] = bytes.Repeat([]byte("the original "+name+" "), 100)
		putOriginal(t, s, f.data[name])
	}
	update(t, s, func(tx *Tx) error {
		for id, name := range map[string]string{mediaID: "a", medium2: "a", medium3: "old"} {
			if _, err := tx.Put(content.RefMedia, id, mediaJSON(id, f.data[name]), userID); err != nil {
				return err
			}
		}
		_, err := tx.Put(content.RefMedia, medium4, docJSON(content.RefMedia, medium4), userID)
		return err
	})
	update(t, s, func(tx *Tx) error {
		_, err := tx.Put(content.RefMedia, medium3, mediaJSON(medium3, f.data["b"]), userID)
		return err
	})
	return f
}

func (f *originalsFixture) hash(name string) string { return sum(f.data[name]) }

func (f *originalsFixture) path(name string) string { return originalPath(f.dir, f.hash(name)) }

// TestCheckOriginals checks that Check reads the originals that the current media documents refer to, and finds each
// one that is missing, changed or not readable, once, with the media that refer to it.
func TestCheckOriginals(t *testing.T) {
	t.Run("right", func(t *testing.T) {
		wantProblems(t, "the originals", findProblems(t, newOriginalsFixture(t).s))
	})

	of := " (of the media " + mediaID + ", " + medium2 + ")"
	for _, tt := range []struct {
		name   string
		change func(t *testing.T, f *originalsFixture, path string, data []byte)
		reason func(f *originalsFixture) string
	}{
		{"a missing original", func(t *testing.T, _ *originalsFixture, path string, _ []byte) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}, func(f *originalsFixture) string { return originalName(f.hash("a")) + ": not found" }},
		{"a changed byte", func(t *testing.T, _ *originalsFixture, path string, data []byte) {
			changed := bytes.Clone(data)
			changed[len(changed)/2] ^= 1
			writeFile(t, path, changed)
		}, func(f *originalsFixture) string {
			changed := bytes.Clone(f.data["a"])
			changed[len(changed)/2] ^= 1
			return "original " + f.hash("a") + ": the data has the hash " + sum(changed)
		}},
		{"a shorter original", func(t *testing.T, _ *originalsFixture, path string, data []byte) {
			writeFile(t, path, data[:len(data)-1])
		}, func(f *originalsFixture) string {
			return "original " + f.hash("a") + ": the data has the hash " + sum(f.data["a"][:len(f.data["a"])-1])
		}},
		{"an empty original", func(t *testing.T, _ *originalsFixture, path string, _ []byte) {
			writeFile(t, path, nil)
		}, func(f *originalsFixture) string {
			return "original " + f.hash("a") + ": the data has the hash " + sum(nil)
		}},
		{"a symbolic link", func(t *testing.T, f *originalsFixture, path string, _ []byte) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			symlink(t, f.path("stray"), path)
		}, func(f *originalsFixture) string {
			return originalName(f.hash("a")) + ": symbolic links are not allowed"
		}},
		{"a directory", func(t *testing.T, _ *originalsFixture, path string, _ []byte) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			mkdir(t, path)
		}, func(f *originalsFixture) string { return originalName(f.hash("a")) + ": must be a regular file" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newOriginalsFixture(t)
			tt.change(t, f, f.path("a"), f.data["a"])
			// Two media refer to it, and it is one problem.
			wantProblems(t, tt.name, findProblems(t, f.s),
				Problem{AreaOriginal, f.hash("a"), tt.reason(f) + of})
		})
	}

	t.Run("the order of the hashes", func(t *testing.T) {
		f := newOriginalsFixture(t)
		for _, name := range []string{"a", "b"} {
			if err := os.Remove(f.path(name)); err != nil {
				t.Fatal(err)
			}
		}
		want := []Problem{
			{AreaOriginal, f.hash("a"), originalName(f.hash("a")) + ": not found (of the media " + mediaID + ", " + medium2 + ")"},
			{AreaOriginal, f.hash("b"), originalName(f.hash("b")) + ": not found (of the media " + medium3 + ")"},
		}
		slices.SortFunc(want, func(a, b Problem) int { return strings.Compare(a.Target, b.Target) })
		wantProblems(t, "two missing originals", findProblems(t, f.s), want...)
	})

	t.Run("originals that no current media document refers to", func(t *testing.T) {
		f := newOriginalsFixture(t)
		// "stray" is in no media document, and "old" only in an earlier version of medium3.
		for _, name := range []string{"stray", "old"} {
			if err := os.Remove(f.path(name)); err != nil {
				t.Fatal(err)
			}
		}
		wantProblems(t, "originals that nothing refers to", findProblems(t, f.s))
	})

	t.Run("a media document that does not decode", func(t *testing.T) {
		f := newOriginalsFixture(t)
		injectDocument(t, f.s, content.RefMedia, medium9, []byte(`{"schemaVersion": 2}`))
		wantProblems(t, "a medium of a later schema", findProblems(t, f.s))
	})
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// logFS returns the directory logdb/ of s, a store of the log backend.
func logFS(t *testing.T, s *Store) vfs.FS {
	t.Helper()
	b, ok := s.b.(*logBackend)
	if !ok {
		t.Fatalf("the backend of the store is %T, want the log backend", s.b)
	}
	return b.fsys
}

// TestCheckEngine checks that Check gives the one problem of the engine when its files are damaged, and goes on to the
// next areas.
func TestCheckEngine(t *testing.T) {
	areas := func(ps []Problem) []string {
		var got []string
		for _, p := range ps {
			got = append(got, p.Area)
		}
		return got
	}
	t.Run("a segment", func(t *testing.T) {
		forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
			s, _ := newStore(t, tb)
			fillChecked(t, s)
			flipByte(t, logFS(t, s), firstSegment, 0)
			got := findProblems(t, s)
			if len(got) != 1 || got[0].Area != AreaEngine || got[0].Target != "" || got[0].Detail == "" {
				t.Errorf("Check() of a store with a changed segment found%s\nwant the one problem of the engine",
					problemLines(got))
			}
		})
	})

	t.Run("an object", func(t *testing.T) {
		forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
			s, _ := newStore(t, tb)
			fillChecked(t, s)
			corruptObjects(t, s)
			got := findProblems(t, s)
			// The documents have other data than their hashes, which stops the index and the media documents as well.
			if len(got) != 3 || !slices.Equal(areas(got), []string{AreaEngine, AreaIndex, AreaOriginal}) {
				t.Fatalf("Check() of a store with changed objects found%s\nwant a problem of each area, in this order",
					problemLines(got))
			}
			if got[0].Target != "" || got[0].Detail == "" {
				t.Errorf("the problem of the engine = %+v, want one without a target, with a detail", got[0])
			}
			if want := "the rows that the documents give cannot be computed: object sha256:"; !strings.HasPrefix(got[1].Detail, want) {
				t.Errorf("the problem of the index = %+v, want the detail to start with %q", got[1], want)
			}
			if want := "the media documents cannot be read: object sha256:"; !strings.HasPrefix(got[2].Detail, want) {
				t.Errorf("the problem of the originals = %+v, want the detail to start with %q", got[2], want)
			}
		})
	})
}

// TestCheckStages checks that the problems of the areas come in their order, one area after another.
func TestCheckStages(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, dir := newStore(t, tb)
		fillChecked(t, s)
		hash := sum([]byte("x"))
		if err := os.Remove(originalPath(dir, hash)); err != nil {
			t.Fatal(err)
		}
		damage(t, s, dropSummary(content.RefTerm, termID))
		index := Problem{AreaIndex, "summaries term " + termID, "the row is missing"}
		original := Problem{AreaOriginal, hash, originalName(hash) + ": not found (of the media " + mediaID + ")"}
		wantProblems(t, "an index and an original", findProblems(t, s), index, original)

		flipByte(t, logFS(t, s), firstSegment, 0)
		got := findProblems(t, s)
		if len(got) != 3 || got[0].Area != AreaEngine || got[1] != index || got[2] != original {
			t.Errorf("Check() found%s\nwant a problem of the engine, then%s", problemLines(got),
				problemLines([]Problem{index, original}))
		}
	})
}

// TestCheckStop checks that Check stops at the problem for which found returns false, and returns nil.
func TestCheckStop(t *testing.T) {
	stopAt := func(t *testing.T, s *Store, n int) int {
		t.Helper()
		calls := 0
		if err := s.Check(t.Context(), func(Problem) bool {
			calls++
			return calls < n
		}); err != nil {
			t.Fatalf("Check() that stops at the problem %d = %v, want nil", n, err)
		}
		return calls
	}
	t.Run("the index and the originals", func(t *testing.T) {
		forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
			s, dir := newStore(t, tb)
			fillChecked(t, s)
			for _, f := range []func(b backendTx) error{
				dropSummary(content.RefTerm, termID), dropSummary(content.RefAuthor, authorID),
				dropSummary(content.RefMenu, menuID),
			} {
				damage(t, s, f)
			}
			// The fourth problem is that of the original, which Check does not look at before the three of the index.
			if err := os.Remove(originalPath(dir, sum([]byte("x")))); err != nil {
				t.Fatal(err)
			}
			if total := len(findProblems(t, s)); total != 4 {
				t.Fatalf("Check() found %d problems, want 4", total)
			}
			for n := 1; n <= 4; n++ {
				if calls := stopAt(t, s, n); calls != n {
					t.Errorf("found is called %d times by a Check that stops at the problem %d", calls, n)
				}
			}
		})
	})
	t.Run("the engine", func(t *testing.T) {
		forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
			s, _ := newStore(t, tb)
			fillChecked(t, s)
			damage(t, s, dropSummary(content.RefTerm, termID))
			flipByte(t, logFS(t, s), firstSegment, 0)
			if total := len(findProblems(t, s)); total != 2 {
				t.Fatalf("Check() found %d problems, want 2", total)
			}
			if calls := stopAt(t, s, 1); calls != 1 {
				t.Errorf("found is called %d times by a Check that stops at the problem of the engine, want 1", calls)
			}
		})
	})
	t.Run("the originals", func(t *testing.T) {
		f := newOriginalsFixture(t)
		for _, name := range []string{"a", "b"} {
			if err := os.Remove(f.path(name)); err != nil {
				t.Fatal(err)
			}
		}
		if calls := stopAt(t, f.s, 1); calls != 1 {
			t.Errorf("found is called %d times by a Check that stops at the first of two originals, want 1", calls)
		}
	})
}

// cancelOnRead is a file system whose files cancel a context when they are first read.
type cancelOnRead struct {
	vfs.FS
	cancel context.CancelFunc
}

func (f cancelOnRead) Open(name string) (vfs.File, error) {
	file, err := f.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return cancelFile{File: file, cancel: f.cancel}, nil
}

type cancelFile struct {
	vfs.File
	cancel context.CancelFunc
}

func (f cancelFile) Read(p []byte) (int, error) {
	f.cancel()
	return f.File.Read(p)
}

// TestCheckContext checks that the end of its context is the only error of Check, in each area.
func TestCheckContext(t *testing.T) {
	t.Run("ended", func(t *testing.T) {
		eachStore(t, func(t *testing.T, s *Store) {
			fillChecked(t, s)
			damage(t, s, dropSummary(content.RefTerm, termID))
			canceled, cancel := context.WithCancel(t.Context())
			cancel()
			expired, cancelExpired := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
			defer cancelExpired()
			for _, tt := range []struct {
				name string
				ctx  context.Context
				want error
			}{{"canceled", canceled, context.Canceled}, {"expired", expired, context.DeadlineExceeded}} {
				calls := 0
				err := s.Check(tt.ctx, func(Problem) bool {
					calls++
					return true
				})
				if !errors.Is(err, tt.want) || calls != 0 {
					t.Errorf("Check() with a %s context = %v, found called %d times; want %v and no call", tt.name, err,
						calls, tt.want)
				}
			}
		})
	})

	t.Run("in the index", func(t *testing.T) {
		eachStore(t, func(t *testing.T, s *Store) {
			fillChecked(t, s)
			for _, f := range []func(b backendTx) error{
				dropSummary(content.RefTerm, termID), dropSummary(content.RefAuthor, authorID),
				dropSummary(content.RefMenu, menuID),
			} {
				damage(t, s, f)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			err := s.Check(ctx, func(Problem) bool {
				calls++
				cancel()
				return true
			})
			if !errors.Is(err, context.Canceled) || calls != 1 {
				t.Errorf("Check() that cancels its context at the first problem = %v, found called %d times; want "+
					"context.Canceled and 1", err, calls)
			}
		})
	})

	t.Run("in an original", func(t *testing.T) {
		s, _ := newStore(t, oneBackend[0])
		data := bytes.Repeat([]byte("the original "), 5000)
		putOriginal(t, s, data)
		update(t, s, func(tx *Tx) error {
			_, err := tx.Put(content.RefMedia, mediaID, mediaJSON(mediaID, data), userID)
			return err
		})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		s.files = cancelOnRead{FS: s.files, cancel: cancel}
		calls := 0
		err := s.Check(ctx, func(Problem) bool {
			calls++
			return true
		})
		if !errors.Is(err, context.Canceled) || calls != 0 {
			t.Errorf("Check() whose context ends while it reads an original = %v, found called %d times; want "+
				"context.Canceled and no call", err, calls)
		}
	})
}

// TestCheckWritesNothing checks that Check changes no file of the data directory and adds no record, whether the store
// is open for writing or for reading, also when it finds problems.
func TestCheckWritesNothing(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, dir := newStore(t, tb)
		fillChecked(t, s)
		damage(t, s, dropSummary(content.RefTerm, termID))
		if err := os.Remove(originalPath(dir, sum([]byte("x")))); err != nil {
			t.Fatal(err)
		}
		r, err := openLogReader(dir, tb.options())
		if err != nil {
			t.Fatalf("openLogReader() = %v", err)
		}
		t.Cleanup(func() { _ = r.Close() })
		for name, c := range map[string]*Store{"writer": s, "reader": r} {
			before, events := snapshot(t, dir), len(rawEvents(t, s))
			heads, err := s.AuditHeads(t.Context())
			if err != nil {
				t.Fatalf("AuditHeads() = %v", err)
			}
			if got := findProblems(t, c); len(got) != 2 {
				t.Errorf("Check() of the %s found%s\nwant 2 problems", name, problemLines(got))
			}
			if after := snapshot(t, dir); !maps.Equal(before, after) {
				t.Errorf("Check() of the %s has changed the files of the data directory", name)
			}
			after, err := s.AuditHeads(t.Context())
			if err != nil || !slices.Equal(heads, after) || len(rawEvents(t, s)) != events {
				t.Errorf("Check() of the %s has changed the audit log: the heads %v, then %v (%v); the events %d, then %d",
					name, heads, after, err, events, len(rawEvents(t, s)))
			}
		}
	})
}

// TestDropIndexRow checks that DropIndexRow deletes the row of summaries and nothing else.
func TestDropIndexRow(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		fillChecked(t, s)
		before := readIndexState(t, s)
		if err := DropIndexRow(t.Context(), s, content.RefTerm, termID); err != nil {
			t.Fatalf("DropIndexRow() = %v", err)
		}
		wantProblems(t, "a store without the summary of a term", findProblems(t, s),
			Problem{AreaIndex, "summaries term " + termID, "the row is missing"})
		delete(before.rows[indexSummaries], string(docKey(content.RefTerm, termID)))
		after := readIndexState(t, s)
		if problem := diffIndex(after.rows, before.rows); problem != "" || after.number != before.number {
			t.Errorf("DropIndexRow() has changed more than the row: %s; the last number %d, was %d", problem, after.number,
				before.number)
		}
		view(t, s, func(tx *Tx) error {
			if d, err := tx.Document(content.RefTerm, termID); err != nil || len(d.Data) == 0 {
				t.Errorf("Document() after DropIndexRow() = %v, %v; want the document", d, err)
			}
			return nil
		})

		want := fmt.Sprintf("term %s: no row of summaries", termID)
		if err := DropIndexRow(t.Context(), s, content.RefTerm, termID); err == nil || err.Error() != want {
			t.Errorf("DropIndexRow() of a row that is not there = %v, want %q", err, want)
		}
		if err := DropIndexRow(t.Context(), s, SettingsKind, ""); err != nil {
			t.Fatalf("DropIndexRow() of the settings = %v", err)
		}
		wantProblems(t, "a store without two summaries", findProblems(t, s),
			Problem{AreaIndex, "summaries term " + termID, "the row is missing"},
			Problem{AreaIndex, "summaries settings", "the row is missing"})
	})

	t.Run("no record", func(t *testing.T) {
		forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
			s, dir := newStore(t, tb)
			fillChecked(t, s)
			heads, err := s.AuditHeads(t.Context())
			if err != nil {
				t.Fatalf("AuditHeads() = %v", err)
			}
			events := len(rawEvents(t, s))
			if err := DropIndexRow(t.Context(), s, content.RefTerm, termID); err != nil {
				t.Fatalf("DropIndexRow() = %v", err)
			}
			after, err := s.AuditHeads(t.Context())
			if err != nil || !slices.Equal(heads, after) || len(rawEvents(t, s)) != events {
				t.Errorf("DropIndexRow() has changed the audit log: the heads %v, then %v (%v); the events %d, then %d", heads,
					after, err, events, len(rawEvents(t, s)))
			}
			r, err := openLogReader(dir, tb.options())
			if err != nil {
				t.Fatalf("openLogReader() = %v", err)
			}
			t.Cleanup(func() { _ = r.Close() })
			if err := DropIndexRow(t.Context(), r, content.RefAuthor, authorID); err == nil {
				t.Errorf("DropIndexRow() of a reader = nil, want an error")
			}
		})
	})
}

// TestCheckWhileWriting checks that a reader finds no problem in a store whose writer commits all the time.
func TestCheckWhileWriting(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		o := *tb.log
		o.SegmentBytes, o.CheckpointBytes = 4096, 8192
		s, dir := newLogStore(t, o)
		r, err := openLogReader(dir, logOpts(o))
		if err != nil {
			t.Fatalf("openLogReader() = %v", err)
		}
		t.Cleanup(func() { _ = r.Close() })
		var committed atomic.Int64
		stop := make(chan struct{})
		ended := make(chan error, 1)
		go func() {
			for n := 1; ; n++ {
				select {
				case <-stop:
					ended <- nil
					return
				default:
				}
				var original []byte
				if n%5 == 0 {
					original = backupOriginal(n)
					if _, err := s.PutOriginal(t.Context(), bytes.NewReader(original)); err != nil {
						ended <- fmt.Errorf("PutOriginal() of the pair %d = %w", n, err)
						return
					}
				}
				if err := s.Update(t.Context(), withRecord(func(tx *Tx) error {
					if original != nil {
						id := backupMedia(n)
						if _, err := tx.Put(content.RefMedia, id, mediaJSON(id, original), userID); err != nil {
							return err
						}
					}
					return putPair(tx, n)
				})); err != nil {
					ended <- fmt.Errorf("Update() of the pair %d = %w", n, err)
					return
				}
				committed.Store(int64(n))
				time.Sleep(200 * time.Microsecond)
			}
		}()
		defer func() {
			close(stop)
			if err := <-ended; err != nil {
				t.Error(err)
			}
		}()
		for i := range 8 {
			for deadline := time.Now().Add(10 * time.Second); committed.Load() < int64(10*(i+1)); {
				if time.Now().After(deadline) {
					t.Fatalf("the writer has committed %d pairs in 10 seconds", committed.Load())
				}
				time.Sleep(time.Millisecond)
			}
			wantProblems(t, fmt.Sprintf("the store at the pair %d", committed.Load()), findProblems(t, r))
		}
	})
}
