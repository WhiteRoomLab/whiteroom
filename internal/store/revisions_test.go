package store

import (
	"bytes"
	"database/sql"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/sitedir"
)

// addRevision adds want with AddRevision in tx, and checks that AddRevision returns it.
func addRevision(t *testing.T, tx *Tx, want Revision) error {
	t.Helper()
	r, err := tx.AddRevision(want.Entry, want.ID, want.Data, want.SavedBy)
	if err == nil && !reflect.DeepEqual(r, want) {
		t.Errorf("AddRevision() = %+v, want %+v", r, want)
	}
	return err
}

// checkRevisions checks what a View of s reads of want, all the revisions of the entry entry: Revision and Object of
// each, and Revisions.
func checkRevisions(t *testing.T, s *Store, entry string, want []Revision) {
	t.Helper()
	view(t, s, func(tx *Tx) error {
		var listed []Revision
		for _, w := range want {
			if r, err := tx.Revision(w.ID); err != nil || !reflect.DeepEqual(r, w) {
				t.Errorf("Revision(%q) = %+v, %v; want %+v", w.ID, r, err, w)
			}
			if data, err := tx.Object(w.Hash); err != nil || !bytes.Equal(data, w.Data) {
				t.Errorf("Object(%q) = %q, %v; want %q", w.Hash, data, err, w.Data)
			}
			w.Data = nil
			listed = append(listed, w)
		}
		if got, err := tx.Revisions(entry); err != nil || !reflect.DeepEqual(got, listed) {
			t.Errorf("Revisions(%q) = %+v, %v; want %+v", entry, got, err, listed)
		}
		return nil
	})
}

func TestRevisions(t *testing.T) {
	s, _ := newStore(t)
	later := testTime.Add(time.Hour)
	// In the reverse order of saving, so that Revisions has to keep the order of saving.
	ids := []string{
		"rev_01JE0000000000000000000004",
		"rev_01JE0000000000000000000003",
		"rev_01JE0000000000000000000002",
		"rev_01JE0000000000000000000001",
	}
	first, second := revJSON("First"), revJSON("Second")
	at, atLater := testTime.UTC(), later.UTC()
	r1 := Revision{Seq: 1, ID: ids[0], Entry: entryID, Hash: sum(first), SavedAt: at, SavedBy: userID, Data: first}
	r2 := Revision{Seq: 2, ID: ids[1], Entry: entryID, Hash: sum(second), SavedAt: atLater, SavedBy: otherUser,
		Data: second}
	r3 := Revision{Seq: 3, ID: ids[2], Entry: pageID, Hash: sum(second), SavedAt: atLater, SavedBy: userID,
		Data: second}
	r4 := Revision{Seq: 4, ID: ids[3], Entry: entryID, Hash: sum(first), SavedAt: atLater, SavedBy: userID, Data: first}

	// An entry with its first revision, which f reads.
	update(t, s, func(tx *Tx) error {
		if _, err := tx.Put(content.RefEntry, entryID, docJSON(content.RefEntry, entryID), userID); err != nil {
			return err
		}
		if err := addRevision(t, tx, r1); err != nil {
			return err
		}
		if r, err := tx.Revision(r1.ID); err != nil || !reflect.DeepEqual(r, r1) {
			t.Errorf("Revision() in Update() = %+v, %v; want %+v", r, err, r1)
		}
		return nil
	})
	checkRevisions(t, s, entryID, []Revision{r1})

	s.now = func() time.Time { return later }
	update(t, s, func(tx *Tx) error { return addRevision(t, tx, r2) })
	// The data of r2 for another entry, and that of r1 again.
	update(t, s, func(tx *Tx) error {
		if _, err := tx.Put(content.RefEntry, pageID, docJSON(content.RefEntry, pageID), userID); err != nil {
			return err
		}
		return errors.Join(addRevision(t, tx, r3), addRevision(t, tx, r4))
	})
	checkRevisions(t, s, entryID, []Revision{r1, r2, r4})
	checkRevisions(t, s, pageID, []Revision{r3})
	checkRevisions(t, s, "ent_01JE0000000000000000000000", nil)
	if n := count(t, s, "objects"); n != 4 {
		t.Errorf("objects = %d, want 4", n)
	}
	t1, t2 := "2026-11-03T08:00:00.120000000Z", "2026-11-03T09:00:00.120000000Z"
	if got, want := savedAts(t, s, "revisions"), []string{t1, t2, t2, t2}; !slices.Equal(got, want) {
		t.Errorf("saved_at of the revisions = %q, want %q", got, want)
	}

	// The revisions of a deleted entry stay.
	update(t, s, func(tx *Tx) error {
		_, err := tx.Delete(content.RefEntry, entryID, userID)
		return err
	})
	checkRevisions(t, s, entryID, []Revision{r1, r2, r4})
	checkRevisions(t, s, pageID, []Revision{r3})
	checkConsistent(t, s)
}

func TestAddRevisionErrors(t *testing.T) {
	const use = ` and a ULID (26 characters in upper case)`
	deleted, missing := "ent_01JE0000000000000000000000", "ent_01JE0000000000000000000001"
	s, _ := newStore(t)
	data := revJSON("About")
	existing := Revision{
		Seq: 1, ID: revisionID, Entry: entryID, Hash: sum(data), SavedAt: testTime.UTC(), SavedBy: userID, Data: data,
	}
	update(t, s, func(tx *Tx) error {
		for _, id := range []string{entryID, pageID, deleted} {
			if _, err := tx.Put(content.RefEntry, id, docJSON(content.RefEntry, id), userID); err != nil {
				return err
			}
		}
		if _, err := tx.Delete(content.RefEntry, deleted, userID); err != nil {
			return err
		}
		return addRevision(t, tx, existing)
	})

	id := "rev_01JE0000000000000000000000"
	file, existingFile := "revisions/"+id+".json", "revisions/"+revisionID+".json"
	largest, big := make([]byte, content.MaxDocumentBytes), make([]byte, content.MaxDocumentBytes+1)
	for _, tt := range []struct {
		name            string
		entry, id       string
		data            []byte
		by              string
		want            string
		problem         bool // content.Errors
		notExist, exist bool
	}{
		{name: "invalid entry ID", entry: "ent_x", id: id, data: data, by: userID,
			want: `invalid entry ID "ent_x": use "ent_"` + use},
		{name: "ID of another kind as the entry", entry: termID, id: id, data: data, by: userID,
			want: `invalid entry ID "` + termID + `": use "ent_"` + use},
		{name: "no entry", id: id, data: data, by: userID, want: `invalid entry ID "": use "ent_"` + use},
		{name: "invalid ID", entry: entryID, id: "rev_x", data: data, by: userID,
			want: `invalid revision ID "rev_x": use "rev_"` + use},
		{name: "ID of another kind", entry: entryID, id: entryID, data: data, by: userID,
			want: `invalid revision ID "` + entryID + `": use "rev_"` + use},
		{name: "path as an ID", entry: entryID, id: "rev_/../../site/settings", data: data, by: userID,
			want: `invalid revision ID "rev_/../../site/settings": use "rev_"` + use},
		{name: "no ID", entry: entryID, data: data, by: userID, want: `invalid revision ID "": use "rev_"` + use},
		{name: "invalid user", entry: entryID, id: id, data: data, by: "usr_x",
			want: `invalid user ID "usr_x": use "usr_"` + use},
		{name: "no user", entry: entryID, id: id, data: data, want: `invalid user ID "": use "usr_"` + use},
		{name: "too large", entry: entryID, id: id, data: big, by: userID,
			want: file + ": too large: 67108865 bytes (max 67108864)", problem: true},
		{name: "largest", entry: entryID, id: id, data: largest, by: userID,
			want: file + `:1:1: invalid character '\x00' at start of value`, problem: true},
		{name: "empty", entry: entryID, id: id, by: userID,
			want: file + ":1:1: unexpected end of JSON input", problem: true},
		{name: "missing schemaVersion", entry: entryID, id: id, data: []byte(`{"title": "About"}`), by: userID,
			want: file + `:1:1: missing key "schemaVersion"`, problem: true},
		{name: "id in the revision", entry: entryID, id: id, data: []byte(`{"schemaVersion": 1, "id": "` + id + `"}`),
			by: userID, want: file + ":1:22: /id: unknown key", problem: true},
		{name: "missing entry", entry: missing, id: id, data: data, by: userID,
			want: "entries/" + missing + ".json: not found", notExist: true},
		{name: "deleted entry", entry: deleted, id: id, data: data, by: userID,
			want: "entries/" + deleted + ".json: not found", notExist: true},
		{name: "existing ID", entry: entryID, id: revisionID, data: revJSON("Other"), by: userID,
			want: existingFile + ": already exists", exist: true},
		{name: "existing ID with its data", entry: entryID, id: revisionID, data: data, by: userID,
			want: existingFile + ": already exists", exist: true},
		{name: "existing ID for another entry", entry: pageID, id: revisionID, data: data, by: userID,
			want: existingFile + ": already exists", exist: true},

		// The first problem is the error.
		{name: "invalid entry ID and ID", entry: "ent_x", id: "rev_x", data: data, by: userID,
			want: `invalid entry ID "ent_x": use "ent_"` + use},
		{name: "invalid ID and user", entry: entryID, id: "rev_x", data: data, by: "usr_x",
			want: `invalid revision ID "rev_x": use "rev_"` + use},
		{name: "invalid user and data", entry: entryID, id: id, by: "usr_x",
			want: `invalid user ID "usr_x": use "usr_"` + use},
		{name: "invalid data for a missing entry", entry: missing, id: id, by: userID,
			want: file + ":1:1: unexpected end of JSON input", problem: true},
		{name: "existing ID for a missing entry", entry: missing, id: revisionID, data: data, by: userID,
			want: "entries/" + missing + ".json: not found", notExist: true},
		{name: "invalid data with an existing ID", entry: entryID, id: revisionID, by: userID,
			want: existingFile + ":1:1: unexpected end of JSON input", problem: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			update(t, s, func(tx *Tx) error {
				_, err = tx.AddRevision(tt.entry, tt.id, tt.data, tt.by)
				return nil
			})
			var es content.Errors
			if err == nil || err.Error() != tt.want || errors.As(err, &es) != tt.problem ||
				errors.Is(err, fs.ErrNotExist) != tt.notExist || errors.Is(err, fs.ErrExist) != tt.exist {
				t.Errorf("AddRevision() = %v (%T), want %q", err, err, tt.want)
			}
			for table, want := range map[string]int{"objects": 4, "versions": 4, "documents": 2, "revisions": 1} {
				if n := count(t, s, table); n != want {
					t.Errorf("rows of %s after AddRevision() = %d, want %d", table, n, want)
				}
			}
		})
	}
	checkRevisions(t, s, entryID, []Revision{existing})
}

func TestRevisionReadErrors(t *testing.T) {
	const use = ` and a ULID (26 characters in upper case)`
	s, _ := newStore(t)
	view(t, s, func(tx *Tx) error {
		for _, id := range []string{"rev_x", entryID, "rev_/../../site/settings", ""} {
			r, err := tx.Revision(id)
			if want := `invalid revision ID "` + id + `": use "rev_"` + use; !reflect.DeepEqual(r, Revision{}) ||
				err == nil || err.Error() != want {
				t.Errorf("Revision(%q) = %+v, %v; want an error %q", id, r, err, want)
			}
		}
		for _, entry := range []string{"ent_x", revisionID, ""} {
			revs, err := tx.Revisions(entry)
			if want := `invalid entry ID "` + entry + `": use "ent_"` + use; revs != nil || err == nil ||
				err.Error() != want {
				t.Errorf("Revisions(%q) = %+v, %v; want an error %q", entry, revs, err, want)
			}
		}
		r, err := tx.Revision(revisionID)
		if want := "revisions/" + revisionID + ".json: not found"; !reflect.DeepEqual(r, Revision{}) ||
			!errors.Is(err, fs.ErrNotExist) || err.Error() != want {
			t.Errorf("Revision() of a missing revision = %+v, %v; want an error %q that wraps fs.ErrNotExist", r, err,
				want)
		}
		if revs, err := tx.Revisions(entryID); revs != nil || err != nil {
			t.Errorf("Revisions() of an entry without revisions = %+v, %v; want none", revs, err)
		}
		return nil
	})
}

// TestCorruptSavedAt changes the saved time of a revision, which the triggers of the tables keep from changing, and
// checks that the reads of the revision fail.
func TestCorruptSavedAt(t *testing.T) {
	s, _ := newStore(t)
	putWithRevision(t, s)
	if err := s.db.Write(t.Context(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(t.Context(), "DROP TRIGGER revisions_no_update"); err != nil {
			return err
		}
		_, err := tx.ExecContext(t.Context(), "UPDATE revisions SET saved_at = 'x'")
		return err
	}); err != nil {
		t.Fatalf("Write() = %v", err)
	}
	check := func(what string, err error) {
		t.Helper()
		var pe *time.ParseError
		if want := "revision " + revisionID + `: parsing time "x"`; !errors.As(err, &pe) ||
			!strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s = %v, want an error %q... that wraps a *time.ParseError", what, err, want)
		}
	}
	view(t, s, func(tx *Tx) error {
		_, err := tx.Revision(revisionID)
		check("Revision()", err)
		_, err = tx.Revisions(entryID)
		check("Revisions()", err)
		return nil
	})
}

// TestSampleSiteRevisions adds every revision of the sample site of internal/build, with the entry that publishes it,
// in one Update, and reads them back.
func TestSampleSiteRevisions(t *testing.T) {
	site := filepath.Join("..", "build", "testdata", "site")
	d, err := sitedir.Open(site)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	entries, err := d.Documents(content.RefEntry)
	if err != nil {
		t.Fatalf("Documents() of the sample site = %v", err)
	}
	publisher := make(map[string]string) // the entries by the revisions that they publish
	for id, f := range entries {
		var e content.Entry
		if err := e.UnmarshalJSON(f.Data); err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		if e.PublishedRevision != "" {
			publisher[e.PublishedRevision] = id
		}
	}
	files, err := os.ReadDir(filepath.Join(site, "revisions"))
	if err != nil {
		t.Fatal(err)
	}
	revs := make(map[string]content.SiteFile)
	for _, f := range files {
		id := strings.TrimSuffix(f.Name(), ".json")
		if _, ok := publisher[id]; !ok {
			t.Fatalf("no entry of the sample site publishes %s", f.Name())
		}
		if revs[id], err = d.Revision(id); err != nil {
			t.Fatalf("Revision(%q) of the sample site = %v", id, err)
		}
	}

	s, _ := newStore(t)
	update(t, s, func(tx *Tx) error {
		for _, id := range slices.Sorted(maps.Keys(revs)) {
			entry := publisher[id]
			if _, err := tx.Put(content.RefEntry, entry, entries[entry].Data, userID); err != nil {
				return err
			}
			if _, err := tx.AddRevision(entry, id, revs[id].Data, userID); err != nil {
				return err
			}
		}
		return nil
	})
	view(t, s, func(tx *Tx) error {
		for id, f := range revs {
			entry := publisher[id]
			if r, err := tx.Revision(id); err != nil || r.Entry != entry || r.Hash != sum(f.Data) ||
				!bytes.Equal(r.Data, f.Data) {
				t.Errorf("Revision(%q) = %+v, %v; want the revision of %s with %q", id, r, err, entry, f.Data)
			}
			if list, err := tx.Revisions(entry); err != nil || len(list) != 1 || list[0].ID != id {
				t.Errorf("Revisions(%q) = %+v, %v; want %s alone", entry, list, err, id)
			}
		}
		return nil
	})
	if n := count(t, s, "revisions"); n != len(revs) {
		t.Errorf("revisions = %d, want %d", n, len(revs))
	}
	checkConsistent(t, s)
}
