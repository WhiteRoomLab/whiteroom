package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// logPlacements are the placements of objects that the tests of the log backend run on. With a threshold of 50 bytes,
// hybrid puts the JSON of docJSON in files, and that of revJSON and of the settings document inline.
var logPlacements = []struct {
	name string
	o    logdb.Options
}{
	{"inline", logdb.Options{Placement: logdb.Inline}},
	{"files", logdb.Options{Placement: logdb.Files}},
	{"hybrid", logdb.Options{Placement: logdb.Hybrid, Threshold: 50}},
}

// logOpts returns the options of the log backend with o.
func logOpts(o logdb.Options) options {
	return options{log: &o}
}

// newLogStore creates a store of the log backend with o in a new directory, and returns it with the directory. The end
// of the test closes it.
func newLogStore(t *testing.T, o logdb.Options) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	s, err := create(dir, logOpts(o))
	if err != nil {
		t.Fatalf("create() = %v", err)
	}
	s.now = func() time.Time { return testTime }
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

// reopenLog opens the store of the log backend in dir with o. The end of the test closes it.
func reopenLog(t *testing.T, dir string, o logdb.Options) *Store {
	t.Helper()
	s, err := open(dir, logOpts(o))
	if err != nil {
		t.Fatalf("open() = %v", err)
	}
	s.now = func() time.Time { return testTime }
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// logDB returns the engine of s, a store of the log backend.
func logDB(t *testing.T, s *Store) *logdb.DB {
	t.Helper()
	b, ok := s.b.(*logBackend)
	if !ok {
		t.Fatalf("the backend of the store is %T, want the log backend", s.b)
	}
	return b.db
}

// rawUpdate runs f in an Update of the engine of s, past the backend.
func rawUpdate(t *testing.T, s *Store, f func(tx *logdb.Tx) error) {
	t.Helper()
	if err := logDB(t, s).Update(t.Context(), f); err != nil {
		t.Fatalf("Update() of the engine = %v", err)
	}
}

// show returns v, a result of a method of Tx, as text, with the hash and the size of the data in place of the data.
func show(v any) string {
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Slice && rv.IsNil() {
		return "nil"
	}
	switch v := v.(type) {
	case []byte:
		return fmt.Sprintf("%d bytes %s", len(v), sum(v))
	case Document:
		return fmt.Sprintf("%+v %s", v.Version, show(v.Data))
	case []Document:
		var docs []string
		for _, d := range v {
			docs = append(docs, show(d))
		}
		return "[" + strings.Join(docs, "; ") + "]"
	case Revision:
		data := v.Data
		v.Data = nil
		return fmt.Sprintf("%+v %s", v, show(data))
	}
	return fmt.Sprintf("%+v", v)
}

// recorder returns a function that adds to out a line with what, and what a method returns.
func recorder(out *[]string) func(what string) func(v any, err error) {
	return func(what string) func(any, error) {
		return func(v any, err error) {
			line := what + ": " + show(v)
			if err != nil {
				line += fmt.Sprintf(" error %q (not exist %v, exist %v)", err, errors.Is(err, fs.ErrNotExist),
					errors.Is(err, fs.ErrExist))
			}
			*out = append(*out, line)
		}
	}
}

// roundTripRevisions are the IDs of the revisions of roundTrip; it never adds the last one.
var roundTripRevisions = []string{revisionID, "rev_01JE0000000000000000000001", "rev_01JE0000000000000000000002",
	"rev_01JE0000000000000000000003"}

// roundTrip runs the same Updates on s, of any backend, and returns what they return, a line each. Some fail in the
// shared code between writes, and one is rolled back.
func roundTrip(t *testing.T, s *Store) []string {
	t.Helper()
	n := 0
	s.now = func() time.Time {
		n++
		return testTime.Add(time.Duration(n) * time.Millisecond)
	}
	var out []string
	rec := recorder(&out)
	run := func(f func(tx *Tx) error) {
		out = append(out, fmt.Sprintf("Update: %v", s.Update(t.Context(), f)))
	}
	first, second := docJSON(content.RefEntry, entryID), append(docJSON(content.RefEntry, entryID), '\n')
	term, otherTerm := docJSON(content.RefTerm, termID), append(docJSON(content.RefTerm, termID), ' ')
	revs := roundTripRevisions
	// A document of every kind, the page before the entry, and a revision.
	run(func(tx *Tx) error {
		rec("Put")(tx.Put(content.RefEntry, pageID, docJSON(content.RefEntry, pageID), userID))
		for _, d := range testDocs {
			rec("Put")(tx.Put(d.kind, d.id, docJSON(d.kind, d.id), userID))
		}
		rec("AddRevision")(tx.AddRevision(entryID, revs[0], revJSON("First"), userID))
		return nil
	})
	// The same data again, which saves nothing, then other data.
	run(func(tx *Tx) error {
		rec("Put")(tx.Put(content.RefEntry, entryID, first, otherUser))
		rec("Put")(tx.Put(content.RefEntry, entryID, second, otherUser))
		rec("Put")(tx.Put(content.RefTerm, termID, otherTerm, userID))
		rec("AddRevision")(tx.AddRevision(entryID, revs[1], revJSON("Second"), otherUser))
		return nil
	})
	// Rolled back: the next Update gets the same seqs.
	run(func(tx *Tx) error {
		rec("Put")(tx.Put(content.RefEntry, entryID, first, userID))
		rec("Delete")(tx.Delete(content.RefTerm, termID, userID))
		rec("AddRevision")(tx.AddRevision(entryID, revs[2], revJSON("Third"), userID))
		return errors.New("test")
	})
	run(func(tx *Tx) error {
		rec("Delete")(tx.Delete(content.RefTerm, termID, userID))
		rec("Delete")(tx.Delete(content.RefTerm, termID, userID))
		rec("AddRevision")(tx.AddRevision(entryID, revs[2], revJSON("Third"), userID))
		rec("AddRevision")(tx.AddRevision(pageID, revs[2], revJSON("Third"), userID))
		rec("Put")(tx.Put(content.RefTerm, termID, term, userID))
		return nil
	})
	// A deleted entry, which keeps its revisions, then the entry again.
	run(func(tx *Tx) error {
		rec("Delete")(tx.Delete(content.RefEntry, entryID, userID))
		rec("AddRevision")(tx.AddRevision(entryID, revs[3], revJSON("Fourth"), userID))
		rec("Put")(tx.Put(content.RefEntry, entryID, second, userID))
		return nil
	})
	return out
}

// readAll returns what a View of s reads of the documents and revisions of roundTrip, a line each.
func readAll(t *testing.T, s *Store) []string {
	t.Helper()
	type key struct {
		kind content.RefKind
		id   string
	}
	keys := []key{{content.RefEntry, pageID}}
	for _, d := range testDocs {
		keys = append(keys, key{d.kind, d.id})
	}
	var out []string
	rec := recorder(&out)
	view(t, s, func(tx *Tx) error {
		for _, k := range keys {
			rec("Document")(tx.Document(k.kind, k.id))
			versions, err := tx.History(k.kind, k.id)
			rec("History")(versions, err)
			for _, v := range versions {
				if v.Hash != "" {
					rec("Object")(tx.Object(v.Hash))
				}
			}
			rec("Documents")(tx.Documents(k.kind))
		}
		for _, id := range roundTripRevisions {
			rec("Revision")(tx.Revision(id))
		}
		for _, entry := range []string{entryID, pageID} {
			rec("Revisions")(tx.Revisions(entry))
		}
		rec("Object")(tx.Object("sha256:" + strings.Repeat("0", 64)))
		return nil
	})
	return out
}

// afterReopen runs an Update on s, whose seqs go on from those of roundTrip, and returns what it returns.
func afterReopen(t *testing.T, s *Store) []string {
	t.Helper()
	s.now = func() time.Time { return testTime.Add(time.Hour) }
	var out []string
	rec := recorder(&out)
	update(t, s, func(tx *Tx) error {
		rec("Put")(tx.Put(content.RefEntry, pageID, append(docJSON(content.RefEntry, pageID), '\n'), otherUser))
		rec("AddRevision")(tx.AddRevision(pageID, "rev_01JE0000000000000000000004", revJSON("Page"), otherUser))
		return nil
	})
	return out
}

// compareLines checks that got, the lines of the log backend, are want, those of SQLite.
func compareLines(t *testing.T, what string, got, want []string) {
	t.Helper()
	for i := range max(len(got), len(want)) {
		var g, w string
		if i < len(got) {
			g = got[i]
		}
		if i < len(want) {
			w = want[i]
		}
		if g != w {
			t.Errorf("%s: line %d is %q, want %q as on SQLite", what, i+1, g, w)
			return
		}
	}
}

// TestLogRoundTrip runs the same Updates on SQLite and on the log backend, in each placement, and checks that both
// return and read the same, also after the store is closed and opened again.
func TestLogRoundTrip(t *testing.T) {
	ref, refDir := newStore(t)
	want, wantRead := roundTrip(t, ref), readAll(t, ref)
	if err := ref.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	ref, err := Open(refDir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	t.Cleanup(func() { _ = ref.Close() })
	compareLines(t, "the reads of SQLite after Open()", readAll(t, ref), wantRead)
	wantNext := afterReopen(t, ref)
	for _, p := range logPlacements {
		t.Run(p.name, func(t *testing.T) {
			s, dir := newLogStore(t, p.o)
			compareLines(t, "the Updates", roundTrip(t, s), want)
			compareLines(t, "the reads", readAll(t, s), wantRead)
			if err := s.Close(); err != nil {
				t.Fatalf("Close() = %v", err)
			}
			s = reopenLog(t, dir, p.o)
			compareLines(t, "the reads after open()", readAll(t, s), wantRead)
			compareLines(t, "the Update after open()", afterReopen(t, s), wantNext)
		})
	}
}

// rawTable returns the keys and values of table in the engine of s.
func rawTable(t *testing.T, s *Store, table uint32) [][2]string {
	t.Helper()
	var kv [][2]string
	if err := logDB(t, s).View(t.Context(), func(tx *logdb.Tx) error {
		tx.Scan(table, nil, func(key, value []byte) bool {
			kv = append(kv, [2]string{string(key), string(value)})
			return true
		})
		return nil
	}); err != nil {
		t.Fatalf("View() of the engine = %v", err)
	}
	return kv
}

// TestLogTables checks the keys and values of the tables of the log backend against its schema.
func TestLogTables(t *testing.T) {
	s, _ := newLogStore(t, logdb.Options{})
	putWithRevision(t, s)
	data, rev := docJSON(content.RefEntry, entryID), revJSON("About")
	// Fields of fewer than 128 bytes: their lengths are a byte.
	field := func(b string) string { return string([]byte{byte(len(b))}) + b }
	hash := func(b []byte) string {
		sum := sha256.Sum256(b)
		return string(sum[:])
	}
	seq := func(n uint64) string { return string(binary.BigEndian.AppendUint64(nil, n)) }
	tail := field("2026-11-03T08:00:00.120000000Z") + field(userID) // saved_at and the user
	version := "\x01" + field(hash(data)) + tail
	want := map[uint32][][2]string{
		tableMeta:           {{"revisions", "\x01"}, {"schema", "\x01"}, {"versions", "\x01"}},
		tableDocuments:      {{"entry\x00" + entryID, version}},
		tableHistory:        {{"entry\x00" + entryID + "\x00" + seq(1), version}},
		tableRevisions:      {{revisionID, "\x01" + field(entryID) + hash(rev) + tail}},
		tableEntryRevisions: {{entryID + "\x00" + seq(1), revisionID}},
	}
	check := func(when string) {
		t.Helper()
		for table := range uint32(7) {
			if got := rawTable(t, s, table); !slices.Equal(got, want[table]) {
				t.Errorf("table %d %s = %q, want %q", table, when, got, want[table])
			}
		}
	}
	check("after the Put")

	update(t, s, func(tx *Tx) error {
		_, err := tx.Delete(content.RefEntry, entryID, userID)
		return err
	})
	want[tableMeta][2][1] = "\x02"
	delete(want, tableDocuments)
	deletion := [2]string{"entry\x00" + entryID + "\x00" + seq(2), "\x02\x00" + tail}
	want[tableHistory] = append(want[tableHistory], deletion)
	check("after the Delete")
}

// TestLogDamage checks the errors of the reads of values that the log backend cannot read, or that name what the store
// does not have: none of them is an error of something that does not exist.
func TestLogDamage(t *testing.T) {
	data, rev := docJSON(content.RefEntry, entryID), revJSON("About")
	sumData, sumRev, other := sha256.Sum256(data), sha256.Sum256(rev), sha256.Sum256([]byte("other"))
	at := testTime.UTC().Format(savedAtLayout)
	version := func(seq uint64, sum []byte, savedAt string) []byte {
		b := appendField(binary.AppendUvarint(nil, seq), sum)
		return appendField(appendField(b, []byte(savedAt)), []byte(userID))
	}
	parseErr := func(s string) string {
		_, err := parseSavedAt(s)
		return err.Error()
	}
	current := string(docKey(content.RefEntry, entryID))
	document := fmt.Sprintf("documents %q: ", current)
	revision := fmt.Sprintf("revisions %q: ", revisionID)
	newRevision := "rev_01JE0000000000000000000009"
	ops := map[string]func(tx *Tx) error{
		"Document":  func(tx *Tx) error { _, err := tx.Document(content.RefEntry, entryID); return err },
		"Documents": func(tx *Tx) error { _, err := tx.Documents(content.RefEntry); return err },
		"History":   func(tx *Tx) error { _, err := tx.History(content.RefEntry, entryID); return err },
		"Delete":    func(tx *Tx) error { _, err := tx.Delete(content.RefEntry, entryID, userID); return err },
		"Put": func(tx *Tx) error {
			_, err := tx.Put(content.RefEntry, pageID, docJSON(content.RefEntry, pageID), userID)
			return err
		},
		"AddRevision": func(tx *Tx) error {
			_, err := tx.AddRevision(entryID, newRevision, revJSON("New"), userID)
			return err
		},
		"Revision":  func(tx *Tx) error { _, err := tx.Revision(revisionID); return err },
		"Revisions": func(tx *Tx) error { _, err := tx.Revisions(entryID); return err },
	}
	readCurrent := func(want string) map[string]string {
		return map[string]string{"Document": want, "Documents": want, "Delete": want, "AddRevision": want}
	}
	noUser := version(1, sumData[:], at)
	noUser = noUser[:len(noUser)-len(userID)]
	for _, tt := range []struct {
		name  string
		table uint32
		key   string
		value []byte
		want  map[string]string // the errors of ops; the others succeed
	}{
		{"truncated counter of versions", tableMeta, metaVersions, []byte{0x80},
			map[string]string{"Delete": `meta "versions": invalid value`, "Put": `meta "versions": invalid value`}},
		{"counter of versions with a byte after it", tableMeta, metaVersions, []byte{1, 0},
			map[string]string{"Delete": `meta "versions": invalid value`, "Put": `meta "versions": invalid value`}},
		{"largest counter of versions", tableMeta, metaVersions, binary.AppendUvarint(nil, math.MaxInt64),
			map[string]string{"Delete": `meta "versions": invalid value`, "Put": `meta "versions": invalid value`}},
		{"truncated counter of revisions", tableMeta, metaRevisions, []byte{0x80},
			map[string]string{"AddRevision": `meta "revisions": invalid value`}},
		{"truncated version", tableDocuments, current, []byte{1}, readCurrent(document + "invalid value")},
		{"version with a byte after it", tableDocuments, current, append(version(1, sumData[:], at), 0),
			readCurrent(document + "invalid value")},
		{"version with a short hash", tableDocuments, current, version(1, sumData[:31], at),
			readCurrent(document + "invalid value")},
		{"version that ends after the length of the user", tableDocuments, current, noUser,
			readCurrent(document + "invalid value")},
		{"version with a seq larger than an int64", tableDocuments, current, version(1<<63, sumData[:], at),
			readCurrent(document + "invalid value")},
		{"deletion as the current version", tableDocuments, current, version(1, nil, at),
			readCurrent(document + "invalid value")},
		{"version with an invalid saved_at", tableDocuments, current, version(1, sumData[:], "x"),
			readCurrent("version 1: " + parseErr("x"))},
		{"version of an object that the store does not have", tableDocuments, current, version(1, other[:], at),
			map[string]string{
				"Document":  document + "the object " + hashText(other[:]) + " is missing",
				"Documents": document + "the object " + hashText(other[:]) + " is missing",
			}},
		{"truncated version in the history", tableHistory, string(historyKey(content.RefEntry, entryID, 1)), []byte{1},
			map[string]string{
				"History": fmt.Sprintf("history %q: invalid value", historyKey(content.RefEntry, entryID, 1)),
			}},
		{"truncated revision", tableRevisions, revisionID, []byte{1},
			map[string]string{"Revision": revision + "invalid value", "Revisions": revision + "invalid value"}},
		{"revision with a byte after it", tableRevisions, revisionID,
			append(appendRevision(1, entryID, sumRev, at, userID), 0),
			map[string]string{"Revision": revision + "invalid value", "Revisions": revision + "invalid value"}},
		{"revision with an invalid saved_at", tableRevisions, revisionID,
			appendRevision(1, entryID, sumRev, "x", userID),
			map[string]string{
				"Revision":  "revision " + revisionID + ": " + parseErr("x"),
				"Revisions": "revision " + revisionID + ": " + parseErr("x"),
			}},
		{"revision of an object that the store does not have", tableRevisions, revisionID,
			appendRevision(1, entryID, other, at, userID),
			map[string]string{"Revision": revision + "the object " + hashText(other[:]) + " is missing"}},
		{"revision of the entry that is missing", tableEntryRevisions, string(entryKey(entryID, 2)),
			[]byte(newRevision),
			map[string]string{
				"Revisions": fmt.Sprintf("entryRevisions %q: the revision %s is missing", entryKey(entryID, 2),
					newRevision),
			}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newLogStore(t, logdb.Options{})
			putWithRevision(t, s)
			rawUpdate(t, s, func(tx *logdb.Tx) error { return tx.Put(tt.table, []byte(tt.key), tt.value) })
			errRollback := errors.New("rollback")
			for _, name := range slices.Sorted(maps.Keys(ops)) {
				var err error
				if uerr := s.Update(t.Context(), func(tx *Tx) error {
					err = ops[name](tx)
					return errRollback
				}); !errors.Is(uerr, errRollback) {
					t.Fatalf("Update() = %v", uerr)
				}
				want, fails := tt.want[name]
				switch {
				case !fails && err != nil:
					t.Errorf("%s() = %v, want no error", name, err)
				case fails && (err == nil || err.Error() != want):
					t.Errorf("%s() = %v, want an error %q", name, err, want)
				case errors.Is(err, fs.ErrNotExist):
					t.Errorf("%s() = %v, which wraps fs.ErrNotExist", name, err)
				}
			}
		})
	}

	t.Run("missing file of an object", func(t *testing.T) {
		s, dir := newLogStore(t, logdb.Options{Placement: logdb.Files})
		putWithRevision(t, s)
		digits := fmt.Sprintf("%x", sumData)
		file := filepath.Join(dir, "logdb", "objects", digits[:2], digits)
		remove(t, file)
		view(t, s, func(tx *Tx) error {
			for name, err := range map[string]error{
				"Document":  ops["Document"](tx),
				"Documents": ops["Documents"](tx),
				"Object":    func() error { _, err := tx.Object(sum(data)); return err }(),
			} {
				want := file + ": missing file"
				if err == nil || err.Error() != want || errors.Is(err, fs.ErrNotExist) {
					t.Errorf("%s() = %v, want an error %q that does not wrap fs.ErrNotExist", name, err, want)
				}
			}
			for _, name := range []string{"History", "Revision", "Revisions"} {
				if err := ops[name](tx); err != nil {
					t.Errorf("%s() = %v", name, err)
				}
			}
			return nil
		})
	})

	t.Run("errors of Documents in the order of the IDs", func(t *testing.T) {
		s, dir := newLogStore(t, logdb.Options{Placement: logdb.Files})
		putWithRevision(t, s)
		put(t, s, content.RefEntry, pageID, docJSON(content.RefEntry, pageID), userID, Version{Seq: 2,
			Kind: content.RefEntry, ID: pageID, Hash: sum(docJSON(content.RefEntry, pageID)), SavedAt: testTime.UTC(),
			SavedBy: userID})
		// The data of the entry, before the page, has another hash; the version of the page does not decode.
		digits := fmt.Sprintf("%x", sumData)
		file := filepath.Join(dir, "logdb", "objects", digits[:2], digits)
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		b[16] ^= 1 // the first byte after the header
		if err := os.WriteFile(file, b, 0o600); err != nil {
			t.Fatal(err)
		}
		rawUpdate(t, s, func(tx *logdb.Tx) error {
			return tx.Put(tableDocuments, docKey(content.RefEntry, pageID), []byte{1})
		})
		view(t, s, func(tx *Tx) error {
			want := "object " + sum(data) + ": the data has the hash " + sum(append([]byte{data[0] ^ 1}, data[1:]...))
			if docs, err := tx.Documents(content.RefEntry); docs != nil || err == nil || err.Error() != want {
				t.Errorf("Documents() = %+v, %v; want an error %q", docs, err, want)
			}
			return nil
		})
	})

	t.Run("the backend", func(t *testing.T) {
		s, _ := newLogStore(t, logdb.Options{})
		if err := s.b.update(t.Context(), func(b backendTx) error {
			want := "object " + sum(rev) + ": the data has the hash " + sum(data)
			if err := b.addObject(sum(rev), data); err == nil || err.Error() != want {
				t.Errorf("addObject() of other data = %v, want an error %q", err, want)
			}
			if err := b.addObject("sha256:x", data); err == nil {
				t.Error("addObject() with an invalid hash = nil, want an error")
			}
			key := historyKey(content.RefEntry, entryID, 1)
			want = fmt.Sprintf("history %q: missing", key)
			if err := b.setCurrent(content.RefEntry, entryID, 1); err == nil || err.Error() != want {
				t.Errorf("setCurrent() of a missing version = %v, want an error %q", err, want)
			}

			// The shared code takes no ID that begins with another, but the keys must not mix them up.
			if err := b.addObject(sum(data), data); err != nil {
				return err
			}
			for _, id := range []string{"a", "ab"} {
				seq, err := b.addVersion(content.RefEntry, id, sum(data), at, userID)
				if err != nil {
					return err
				}
				if err := b.setCurrent(content.RefEntry, id, seq); err != nil {
					return err
				}
				if _, err := b.addRevision("rev_"+id, id, sum(data), at, userID); err != nil {
					return err
				}
			}
			h, herr := b.history(content.RefEntry, "a")
			revs, rerr := b.revisions("a")
			docs, derr := b.documents(content.RefEntry)
			if len(h) != 1 || len(revs) != 1 || len(docs) != 2 || docs[0].ID != "a" || docs[1].ID != "ab" {
				t.Errorf("the versions of a = %+v, %v; its revisions %+v, %v; the documents %+v, %v; want those of a, "+
					"and a and ab", h, herr, revs, rerr, docs, derr)
			}
			return nil
		}); err != nil {
			t.Fatalf("update() = %v", err)
		}
	})
}

// modes returns the modes of the files and directories in dir, by their paths in dir.
func modes(t *testing.T, dir string) map[string]fs.FileMode {
	t.Helper()
	m := map[string]fs.FileMode{}
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		m[filepath.ToSlash(rel)] = info.Mode()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestLogLayout checks the files that the log backend makes in a data directory, and the errors of open for files that
// are not those.
func TestLogLayout(t *testing.T) {
	const dirMode, fileMode = fs.ModeDir | 0o700, fs.FileMode(0o600)
	s, dir := newLogStore(t, logdb.Options{Placement: logdb.Files})
	want := map[string]fs.FileMode{
		".": dirMode, "files": dirMode, "logdb": dirMode, "logdb/head": fileMode, "logdb/lock": dirMode,
		"logdb/lock/entry": fileMode, "logdb/lock/readers": fileMode, "logdb/lock/writer": fileMode,
		"logdb/log": dirMode, "logdb/log/0000000000000001-0000000000000001.log": fileMode, "logdb/objects": dirMode,
	}
	if got := modes(t, dir); !maps.Equal(got, want) {
		t.Errorf("the data directory after create() has %v, want %v", got, want)
	}
	putWithRevision(t, s)
	original := []byte("GIF89a")
	putOriginal(t, s, original)
	for parent, data := range map[string][][]byte{
		"logdb/objects": {docJSON(content.RefEntry, entryID), revJSON("About")},
		"files":         {original},
	} {
		for _, d := range data {
			digits := strings.TrimPrefix(sum(d), "sha256:")
			want[parent+"/"+digits[:2]], want[parent+"/"+digits[:2]+"/"+digits] = dirMode, fileMode
		}
	}
	if got := modes(t, dir); !maps.Equal(got, want) {
		t.Errorf("the data directory after the writes has %v, want %v", got, want)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if got, want := names(t, dir), []string{"files", "logdb"}; !slices.Equal(got, want) {
		t.Errorf("the data directory after Close() has %q, want %q", got, want)
	}
	if got, want := names(t, filepath.Join(dir, "logdb")), []string{"checkpoint", "head", "lock", "log",
		"objects"}; !slices.Equal(got, want) {
		t.Errorf("logdb/ after Close() has %q, want %q", got, want)
	}
	if got := modeOf(t, filepath.Join(dir, "logdb", "checkpoint")); got != fileMode {
		t.Errorf("mode of the checkpoint = %v, want %v", got, fileMode)
	}

	t.Run("durable", func(t *testing.T) {
		fsys := simfs.New("data")
		s, err := create("data", options{log: &logdb.Options{}, fs: fsys})
		if err != nil {
			t.Fatalf("create() = %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		crashed := fsys.Crash(simfs.Model{Name: "lose-all"})
		c, err := open("data", options{log: &logdb.Options{}, fs: crashed})
		if err != nil {
			t.Fatalf("open() after a crash right after create() = %v", err)
		}
		if err := c.Close(); err != nil {
			t.Errorf("Close() = %v", err)
		}
	})

	t.Run("errors of open", func(t *testing.T) {
		for _, tt := range []struct {
			name  string
			setup func(t *testing.T, path func(name string) string)
			want  string // after the data directory
		}{
			{"missing logdb", func(t *testing.T, path func(string) string) {
				if err := os.RemoveAll(path("logdb")); err != nil {
					t.Fatal(err)
				}
			}, "/logdb: missing directory"},
			{"logdb as a file", func(t *testing.T, path func(string) string) {
				if err := os.RemoveAll(path("logdb")); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path("logdb"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}, "/logdb: must be a directory"},
			{"logdb as a symbolic link", func(t *testing.T, path func(string) string) {
				rename(t, path("logdb"), path("other"))
				symlink(t, "other", path("logdb"))
			}, "/logdb: symbolic links are not allowed"},
			{"missing files", func(t *testing.T, path func(string) string) { remove(t, path("files")) },
				"/files: missing directory"},
			{"missing logdb and files", func(t *testing.T, path func(string) string) {
				remove(t, path("files"))
				if err := os.RemoveAll(path("logdb")); err != nil {
					t.Fatal(err)
				}
			}, "/logdb: missing directory"},
			{"empty logdb", func(t *testing.T, path func(string) string) {
				if err := os.RemoveAll(path("logdb")); err != nil {
					t.Fatal(err)
				}
				mkdir(t, path("logdb"))
			}, "/logdb/lock: missing directory"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				s, dir := newLogStore(t, logdb.Options{})
				if err := s.Close(); err != nil {
					t.Fatalf("Close() = %v", err)
				}
				tt.setup(t, func(name string) string { return filepath.Join(dir, name) })
				for name, open := range map[string]func(string, options) (*Store, error){
					"open": open, "openLogReader": openLogReader,
				} {
					s, err := open(dir, logOpts(logdb.Options{}))
					if err == nil {
						_ = s.Close()
					}
					if want := dir + tt.want; err == nil || err.Error() != want {
						t.Errorf("%s() = %v, want %q", name, err, want)
					}
				}
			})
		}

		_, logDir := newLogStore(t, logdb.Options{})
		if s, err := Open(logDir); err == nil || err.Error() != logDir+"/whiteroom.db: missing file" {
			if err == nil {
				_ = s.Close()
			}
			t.Errorf("Open() of a data directory of the log backend = %v, want an error", err)
		}
		_, sqliteDir := newStore(t)
		if s, err := open(sqliteDir, logOpts(logdb.Options{})); err == nil ||
			err.Error() != sqliteDir+"/logdb: missing directory" {
			if err == nil {
				_ = s.Close()
			}
			t.Errorf("open() of a data directory of SQLite = %v, want an error", err)
		}
	})

	t.Run("errors of create", func(t *testing.T) {
		_, dir := newLogStore(t, logdb.Options{})
		before := names(t, dir)
		if s, err := create(dir, logOpts(logdb.Options{})); err == nil ||
			err.Error() != dir+": not empty; a new data directory needs a missing or empty directory" {
			if err == nil {
				_ = s.Close()
			}
			t.Errorf("create() in a data directory = %v, want an error", err)
		}
		if got := names(t, dir); !slices.Equal(got, before) {
			t.Errorf("create() has changed the directory: %q, want %q", got, before)
		}
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if s, err := create(file, logOpts(logdb.Options{})); err == nil ||
			err.Error() != "mkdir "+file+": not a directory" {
			if err == nil {
				_ = s.Close()
			}
			t.Errorf("create() of a file = %v, want an error", err)
		}
		fsys := simfs.New("data")
		if err := fsys.Mkdir("x", 0o700); err != nil {
			t.Fatal(err)
		}
		if s, err := create("data", options{log: &logdb.Options{}, fs: fsys}); err == nil ||
			err.Error() != "data: not empty; a new data directory needs a missing or empty directory" {
			if err == nil {
				_ = s.Close()
			}
			t.Errorf("create() in a file system that is not empty = %v, want an error", err)
		}
		for name, f := range map[string]func(string, options) (*Store, error){"create": create, "open": open} {
			if s, err := f("data", options{fs: simfs.New("data")}); !errors.Is(err, errSQLiteFS) {
				if err == nil {
					_ = s.Close()
				}
				t.Errorf("%s() of SQLite in a file system = %v, want %v", name, err, errSQLiteFS)
			}
		}
	})
}

// snapshot returns the mode, and the data of a regular file, of each file and directory in dir, by its path in dir.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	for name, mode := range modes(t, dir) {
		m[name] = mode.String()
		if mode.IsRegular() {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			m[name] += " " + string(data)
		}
	}
	return m
}

// TestLogSchemaVersion checks that open and openLogReader refuse a store without the schema of the log backend, or of
// another one, and change no file.
func TestLogSchemaVersion(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value []byte // of "schema" in meta; nil: none
		want  string // after "<dir>/logdb: "
	}{
		{"newer", []byte{2}, "the store schema is of version 2, newer than this program knows (1)"},
		{"much newer", binary.AppendUvarint(nil, 300),
			"the store schema is of version 300, newer than this program knows (1)"},
		{"missing", nil, "not a store of whiteroom (no schema version)"},
		{"zero", []byte{0}, `meta "schema": invalid value`},
		{"empty", []byte{}, `meta "schema": invalid value`},
		{"truncated", []byte{0x81}, `meta "schema": invalid value`},
		{"with a byte after it", []byte{1, 0}, `meta "schema": invalid value`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, dir := newLogStore(t, logdb.Options{})
			putWithRevision(t, s)
			rawUpdate(t, s, func(tx *logdb.Tx) error {
				if tt.value == nil {
					return tx.Delete(tableMeta, []byte(metaSchema))
				}
				return tx.Put(tableMeta, []byte(metaSchema), tt.value)
			})
			if err := s.Close(); err != nil {
				t.Fatalf("Close() = %v", err)
			}
			before := snapshot(t, dir)
			for name, open := range map[string]func(string, options) (*Store, error){
				"open": open, "openLogReader": openLogReader,
			} {
				s, err := open(dir, logOpts(logdb.Options{}))
				if err == nil {
					_ = s.Close()
				}
				if want := filepath.Join(dir, "logdb") + ": " + tt.want; err == nil || err.Error() != want {
					t.Errorf("%s() = %v, want %q", name, err, want)
				}
			}
			if after := snapshot(t, dir); !maps.Equal(after, before) {
				t.Errorf("open() has changed the files of the store")
			}

			// The engine opens at once: the refusals have released their locks.
			fsys, err := vfs.OpenRoot(filepath.Join(dir, "logdb"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = fsys.Close() }()
			d, err := logdb.Open(fsys, logdb.Options{WriterWait: time.Millisecond})
			if err != nil {
				t.Fatalf("Open() of the engine = %v", err)
			}
			err = d.Update(t.Context(), func(tx *logdb.Tx) error {
				return tx.Put(tableMeta, []byte(metaSchema), []byte{logSchemaVersion})
			})
			if err := errors.Join(err, d.Close()); err != nil {
				t.Fatalf("the schema again = %v", err)
			}
			data := docJSON(content.RefEntry, entryID)
			v := Version{Seq: 1, Kind: content.RefEntry, ID: entryID, Hash: sum(data), SavedAt: testTime.UTC(),
				SavedBy: userID}
			checkDocument(t, reopenLog(t, dir, logdb.Options{}), Document{v, data}, []Version{v})
		})
	}
}

// checkNoDocument checks that s has no document of kind with id, and has never had one.
func checkNoDocument(t *testing.T, s *Store, kind content.RefKind, id string) {
	t.Helper()
	view(t, s, func(tx *Tx) error {
		if d, err := tx.Document(kind, id); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Document(%q) = %+v, %v; want an error that wraps fs.ErrNotExist", id, d.Version, err)
		}
		if h, err := tx.History(kind, id); h != nil || err != nil {
			t.Errorf("History(%q) = %+v, %v; want none", id, h, err)
		}
		return nil
	})
}

// event is an audit event as Store.events gives it.
type event struct {
	number uint64
	data   string
}

// allEvents returns the audit events of s.
func allEvents(t *testing.T, s *Store) []event {
	t.Helper()
	var got []event
	if err := s.events(t.Context(), func(number uint64, data []byte) bool {
		got = append(got, event{number, string(data)})
		return true
	}); err != nil {
		t.Fatalf("events() = %v", err)
	}
	return got
}

func TestLogEvents(t *testing.T) {
	s, dir := newLogStore(t, logdb.Options{})
	update(t, s, func(tx *Tx) error {
		if _, err := tx.Put(content.RefEntry, entryID, docJSON(content.RefEntry, entryID), userID); err != nil {
			return err
		}
		return tx.appendEvent([]byte("put"))
	})
	want := []event{{1, "put"}}
	if got := allEvents(t, s); !slices.Equal(got, want) {
		t.Errorf("events() = %v, want %v", got, want)
	}

	// An Update that fails leaves neither the event nor the document, and the next event has the next number.
	errTest := errors.New("test")
	putPage := func(tx *Tx) error {
		if err := tx.appendEvent([]byte("lost")); err != nil {
			return err
		}
		_, err := tx.Put(content.RefEntry, pageID, docJSON(content.RefEntry, pageID), userID)
		return err
	}
	if err := s.Update(t.Context(), func(tx *Tx) error {
		return errors.Join(putPage(tx), errTest)
	}); !errors.Is(err, errTest) {
		t.Errorf("Update() = %v, want the error of f", err)
	}
	failAt(s, "logdb:update:written", errTest)
	if err := s.Update(t.Context(), putPage); !errors.Is(err, errTest) {
		t.Errorf("Update() that fails in the engine = %v, want the error of the hook", err)
	}
	s.hook = nil
	checkNoDocument(t, s, content.RefEntry, pageID)
	var kept *Tx
	update(t, s, func(tx *Tx) error {
		kept = tx
		return errors.Join(tx.appendEvent([]byte("a")), tx.appendEvent(nil))
	})
	want = append(want, event{2, "a"}, event{3, ""})
	if got := allEvents(t, s); !slices.Equal(got, want) {
		t.Errorf("events() = %v, want %v", got, want)
	}
	if err := kept.appendEvent([]byte("late")); err == nil || err.Error() != "the transaction has ended" {
		t.Errorf("appendEvent() after the Update = %v, want an error", err)
	}
	view(t, s, func(tx *Tx) error {
		if err := tx.appendEvent([]byte("view")); err == nil || err.Error() != "read-only transaction" {
			t.Errorf("appendEvent() in View() = %v, want an error", err)
		}
		return nil
	})

	n := 0
	if err := s.events(t.Context(), func(uint64, []byte) bool { n++; return false }); err != nil || n != 1 {
		t.Errorf("events() that stops = %v after %d events, want nil after 1", err, n)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.events(canceled, func(uint64, []byte) bool { return true }); !errors.Is(err, context.Canceled) {
		t.Errorf("events() with a canceled context = %v, want context.Canceled", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	s = reopenLog(t, dir, logdb.Options{})
	update(t, s, func(tx *Tx) error { return tx.appendEvent([]byte("b")) })
	if got, want := allEvents(t, s), append(want, event{4, "b"}); !slices.Equal(got, want) {
		t.Errorf("events() after open() = %v, want %v", got, want)
	}

	q, _ := newStore(t)
	if err := q.Update(t.Context(), func(tx *Tx) error { return tx.appendEvent([]byte("x")) }); err == nil ||
		err.Error() != "audit events need the log backend" {
		t.Errorf("appendEvent() on SQLite = %v, want an error", err)
	}
	view(t, q, func(tx *Tx) error {
		if err := tx.appendEvent([]byte("view")); err == nil || err.Error() != "read-only transaction" {
			t.Errorf("appendEvent() in View() on SQLite = %v, want an error", err)
		}
		return nil
	})
	if err := q.events(t.Context(), func(uint64, []byte) bool { return true }); err == nil ||
		err.Error() != "audit events need the log backend" {
		t.Errorf("events() on SQLite = %v, want an error", err)
	}
}

// TestLogHooks checks the points of the engine at which the hook of a store of the log backend is called, and that an
// Update that fails at one of them before its commit point leaves nothing.
func TestLogHooks(t *testing.T) {
	data := docJSON(content.RefEntry, entryID)
	all := []string{"commit", "logdb:update:objects", "logdb:update:written", "logdb:update:synced",
		"logdb:update:published"}
	for i, point := range all {
		t.Run(point, func(t *testing.T) {
			s, _ := newLogStore(t, logdb.Options{Placement: logdb.Files})
			errHook := errors.New("hook")
			called := failAt(s, point, errHook)
			err := s.Update(t.Context(), func(tx *Tx) error {
				_, err := tx.Put(content.RefEntry, entryID, data, userID)
				return err
			})
			fails := i <= 2 // before the commit point, the Sync of the record
			want := all
			if fails {
				want = all[:i+1]
			}
			if got := called(); !slices.Equal(got, want) {
				t.Errorf("the hook has been called at %q, want %q", got, want)
			}
			s.hook = nil
			v := Version{Seq: 1, Kind: content.RefEntry, ID: entryID, Hash: sum(data), SavedAt: testTime.UTC(),
				SavedBy: userID}
			if !fails {
				if err != nil {
					t.Errorf("Update() = %v, want nil: the hook cannot fail after the commit point", err)
				}
				checkDocument(t, s, Document{v, data}, []Version{v})
				return
			}
			if !errors.Is(err, errHook) || err.Error() != errHook.Error() {
				t.Errorf("Update() = %v, want the error of the hook", err)
			}
			checkMissing(t, s, content.RefEntry, entryID, "entries/"+entryID+".json", nil)
			put(t, s, content.RefEntry, entryID, data, userID, v)
		})
	}
}

func TestLogSecondWriter(t *testing.T) {
	o := logdb.Options{WriterWait: 50 * time.Millisecond}
	s, dir := newLogStore(t, o)
	putWithRevision(t, s)
	start := time.Now()
	second, err := open(dir, logOpts(o))
	if err == nil {
		_ = second.Close()
	}
	if want := dir + "/logdb: another process holds the writer lock"; err == nil || err.Error() != want {
		t.Errorf("open() while the store is open = %v, want %q", err, want)
	}
	if d := time.Since(start); d < o.WriterWait {
		t.Errorf("open() has waited %v, want at least %v", d, o.WriterWait)
	}
	update(t, s, func(tx *Tx) error {
		_, err := tx.Put(content.RefEntry, pageID, docJSON(content.RefEntry, pageID), userID)
		return err
	})
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	s = reopenLog(t, dir, o)
	view(t, s, func(tx *Tx) error {
		if docs, err := tx.Documents(content.RefEntry); err != nil || len(docs) != 2 {
			t.Errorf("Documents() = %+v, %v; want the entry and the page", docs, err)
		}
		return nil
	})
}

// TestLogCloseReleasesFiles checks that Close, and an open that fails, close the files of the log backend.
func TestLogCloseReleasesFiles(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("counts the files in /proc/self/fd")
	}
	files := func() int {
		t.Helper()
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	o := logdb.Options{WriterWait: 10 * time.Millisecond}
	s, dir := newLogStore(t, o)
	putWithRevision(t, s)
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	r := Revision{Seq: 1, ID: revisionID, Entry: entryID, Hash: sum(revJSON("About")), SavedAt: testTime.UTC(),
		SavedBy: userID, Data: revJSON("About")}
	before := 0
	for i, open := range []func(string, options) (*Store, error){open, open, openLogReader} {
		if i == 1 {
			before = files() // after the first, which may make the runtime open files of its own
		}
		s, err := open(dir, logOpts(o))
		if err != nil {
			t.Fatalf("open() = %v", err)
		}
		checkRevisions(t, s, entryID, []Revision{r})
		// A second writer fails; a second reader does not.
		second, err := open(dir, logOpts(o))
		if err == nil {
			err = second.Close()
		}
		if isWriter := i < 2; isWriter != (err != nil) {
			t.Errorf("the second store = %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}
	}
	if after := files(); after != before {
		t.Errorf("the process has %d open files after the stores have closed, want %d", after, before)
	}
}

// TestLogReader checks that a store of openLogReader sees the commits of the writer once they are durable, and that it
// writes nothing.
func TestLogReader(t *testing.T) {
	w, dir := newLogStore(t, logdb.Options{})
	putWithRevision(t, w)
	original := []byte("GIF89a")
	putOriginal(t, w, original)
	update(t, w, func(tx *Tx) error { return tx.appendEvent([]byte("put")) })
	r, err := openLogReader(dir, options{})
	if err != nil {
		t.Fatalf("openLogReader() = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	readAs := func(s *Store) []string {
		t.Helper()
		var out []string
		rec := recorder(&out)
		view(t, s, func(tx *Tx) error {
			for _, id := range []string{entryID, pageID} {
				rec("Document")(tx.Document(content.RefEntry, id))
				rec("History")(tx.History(content.RefEntry, id))
			}
			rec("Documents")(tx.Documents(content.RefEntry))
			rec("Revision")(tx.Revision(revisionID))
			rec("Revisions")(tx.Revisions(entryID))
			return nil
		})
		return out
	}
	compareLines(t, "the reads of the reader", readAs(r), readAs(w))
	if got, want := allEvents(t, r), []event{{1, "put"}}; !slices.Equal(got, want) {
		t.Errorf("events() of the reader = %v, want %v", got, want)
	}
	if got, err := readOriginal(t, r, sum(original)); err != nil || !bytes.Equal(got, original) {
		t.Errorf("OpenOriginal() of the reader reads %q, %v; want %q", got, err, original)
	}

	// The record of the page is written but not synced: the reader does not see it until the Update has returned.
	reached, wait := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(wait) })
	t.Cleanup(release)
	w.hook = func(point string) error {
		if point == "logdb:update:written" {
			close(reached)
			<-wait
		}
		return nil
	}
	done := make(chan error, 1)
	go func() {
		done <- w.Update(context.Background(), func(tx *Tx) error {
			_, err := tx.Put(content.RefEntry, pageID, docJSON(content.RefEntry, pageID), userID)
			return err
		})
	}()
	select {
	case <-reached:
	case err := <-done:
		t.Fatalf("Update() = %v before the point of the hook", err)
	}
	checkNoDocument(t, r, content.RefEntry, pageID)
	release()
	if err := <-done; err != nil {
		t.Fatalf("Update() = %v", err)
	}
	w.hook = nil
	compareLines(t, "the reads of the reader after the Update", readAs(r), readAs(w))

	called := false
	if err := r.Update(t.Context(), func(*Tx) error {
		called = true
		return nil
	}); err == nil || err.Error() != "read-only transaction" || called {
		t.Errorf("Update() of the reader = %v (f called: %v), want an error", err, called)
	}
	files := names(t, filepath.Join(dir, "files"))
	if o, err := r.PutOriginal(t.Context(), bytes.NewReader([]byte("other"))); err == nil ||
		err.Error() != "the store is opened to read only" {
		t.Errorf("PutOriginal() of the reader = %+v, %v; want an error", o, err)
	}
	if got := names(t, filepath.Join(dir, "files")); !slices.Equal(got, files) {
		t.Errorf("files/ after PutOriginal() of the reader has %q, want %q", got, files)
	}

	// Without a writer, a reader reads to the end of the log.
	if err := errors.Join(r.Close(), w.Close()); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	r, err = openLogReader(dir, logOpts(logdb.Options{}))
	if err != nil {
		t.Fatalf("openLogReader() without a writer = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	w = reopenLog(t, dir, logdb.Options{})
	compareLines(t, "the reads of the reader without a writer", readAs(r), readAs(w))
}

// TestBackupLog checks that backupLog copies a store of the log backend, while its writer runs or not, to a data
// directory that open opens with the same documents, revisions, events and originals.
func TestBackupLog(t *testing.T) {
	o := logdb.Options{Placement: logdb.Hybrid, Threshold: 50}
	s, dir := newLogStore(t, o)
	putWithRevision(t, s)
	originals := [][]byte{[]byte("GIF89a"), bytes.Repeat([]byte("original "), 1000)}
	for _, data := range originals {
		putOriginal(t, s, data)
	}
	update(t, s, func(tx *Tx) error {
		if _, err := tx.Put(content.RefTerm, termID, docJSON(content.RefTerm, termID), userID); err != nil {
			return err
		}
		return tx.appendEvent([]byte("term"))
	})
	// A temporary file of PutOriginal that a crash has left.
	if err := os.WriteFile(filepath.Join(dir, "files", ".tmp-0123456789abcdef"), []byte("GIF"), 0o600); err != nil {
		t.Fatal(err)
	}
	read := func(s *Store) []string {
		t.Helper()
		lines := readAll(t, s)
		for _, e := range allEvents(t, s) {
			lines = append(lines, fmt.Sprintf("event %d %q", e.number, e.data))
		}
		return lines
	}
	check := func(t *testing.T, copied string, want []string) *Store {
		t.Helper()
		c := reopenLog(t, copied, o)
		compareLines(t, "the reads of the copy", read(c), want)
		for _, data := range originals {
			if got, err := readOriginal(t, c, sum(data)); err != nil || !bytes.Equal(got, data) {
				t.Errorf("the original of %d bytes in the copy reads %d bytes, %v", len(data), len(got), err)
			}
		}
		gotFiles, wantFiles := names(t, filepath.Join(copied, "files")), names(t, filepath.Join(dir, "files"))[1:]
		if !slices.Equal(gotFiles, wantFiles) {
			t.Errorf("files/ of the copy has %q, want %q", gotFiles, wantFiles)
		}
		for name, mode := range modes(t, copied) {
			if want := fs.FileMode(0o600); mode.IsDir() && mode != fs.ModeDir|0o700 || !mode.IsDir() && mode != want {
				t.Errorf("mode of %s in the copy = %v", name, mode)
			}
		}
		return c
	}

	copied := filepath.Join(t.TempDir(), "copy")
	if err := backupLog(t.Context(), dir, copied); err != nil {
		t.Fatalf("backupLog() while the writer runs = %v", err)
	}
	c := check(t, copied, read(s))
	update(t, c, func(tx *Tx) error {
		v, err := tx.Put(content.RefEntry, pageID, docJSON(content.RefEntry, pageID), userID)
		if err == nil && v.Seq != 3 {
			t.Errorf("Put() in the copy = %+v, want the seq 3", v)
		}
		return err
	})

	update(t, s, func(tx *Tx) error {
		_, err := tx.Put(content.RefAuthor, authorID, docJSON(content.RefAuthor, authorID), userID)
		return err
	})
	want := read(s)
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	copied = filepath.Join(t.TempDir(), "copy")
	if err := backupLog(t.Context(), dir, copied); err != nil {
		t.Fatalf("backupLog() without a writer = %v", err)
	}
	check(t, copied, want)

	t.Run("errors", func(t *testing.T) {
		if err := backupLog(t.Context(), dir, copied); err == nil ||
			err.Error() != copied+": not empty; a new data directory needs a missing or empty directory" {
			t.Errorf("backupLog() to a data directory = %v, want an error", err)
		}
		_, sqliteDir := newStore(t)
		if err := backupLog(t.Context(), sqliteDir, filepath.Join(t.TempDir(), "copy")); err == nil ||
			err.Error() != sqliteDir+"/logdb: missing directory" {
			t.Errorf("backupLog() of a data directory of SQLite = %v, want an error", err)
		}
		canceled, cancel := context.WithCancel(t.Context())
		cancel()
		if err := backupLog(canceled, dir, filepath.Join(t.TempDir(), "copy")); !errors.Is(err, context.Canceled) {
			t.Errorf("backupLog() with a canceled context = %v, want context.Canceled", err)
		}
		// The copy of files/ checks the context too.
		src, dst := simfs.New("src"), simfs.New("dst")
		for _, fsys := range []*simfs.FS{src, dst} {
			if err := fsys.Mkdir("files", 0o700); err != nil {
				t.Fatal(err)
			}
		}
		f, err := src.OpenFile("files/a", os.O_WRONLY|os.O_CREATE, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if err := copyFiles(canceled, src, dst, "files"); !errors.Is(err, context.Canceled) {
			t.Errorf("copyFiles() with a canceled context = %v, want context.Canceled", err)
		}
	})

	t.Run("durable", func(t *testing.T) {
		src := simfs.New("data")
		s, err := create("data", options{log: &o, fs: src})
		if err != nil {
			t.Fatalf("create() = %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		s.now = func() time.Time { return testTime }
		putWithRevision(t, s)
		for _, data := range originals {
			putOriginal(t, s, data)
		}
		dst := simfs.New("copy")
		if err := backup(t.Context(), "data", src, "copy", dst); err != nil {
			t.Fatalf("backup() = %v", err)
		}
		c, err := open("copy", options{log: &o, fs: dst.Crash(simfs.Model{Name: "lose-all"})})
		if err != nil {
			t.Fatalf("open() of the copy after a crash = %v", err)
		}
		t.Cleanup(func() { _ = c.Close() })
		compareLines(t, "the reads of the copy after a crash", readAll(t, c), readAll(t, s))
		for _, data := range originals {
			if got, err := readOriginal(t, c, sum(data)); err != nil || !bytes.Equal(got, data) {
				t.Errorf("the original of %d bytes in the copy reads %d bytes, %v", len(data), len(got), err)
			}
		}
	})
}
