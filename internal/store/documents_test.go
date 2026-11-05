package store

import (
	"bytes"
	"errors"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/sitedir"
)

// testDocs are a document of every kind of build.Source.Documents and the settings document, with their names in
// errors.
var testDocs = []struct {
	kind content.RefKind
	id   string
	file string
}{
	{content.RefEntry, entryID, "entries/" + entryID + ".json"},
	{content.RefTerm, termID, "terms/" + termID + ".json"},
	{content.RefMedia, mediaID, "media/" + mediaID + ".json"},
	{content.RefAuthor, authorID, "authors/" + authorID + ".json"},
	{content.RefMenu, menuID, "menus/" + menuID + ".json"},
	{content.RefForm, formID, "forms/" + formID + ".json"},
	{content.RefRedirect, redirectID, "redirects/" + redirectID + ".json"},
	{SettingsKind, "", "site/settings.json"},
}

// put puts data as the document of kind with id by the user by, in an Update of s, and checks that Put returns want.
func put(t *testing.T, s *Store, kind content.RefKind, id string, data []byte, by string, want Version) {
	t.Helper()
	update(t, s, func(tx *Tx) error {
		v, err := tx.Put(kind, id, data, by)
		if err == nil && v != want {
			t.Errorf("Put() = %+v, want %+v", v, want)
		}
		return err
	})
}

// checkDocument checks what a View of s reads of want, the current version of a document, and of history, all its
// versions: Document, Documents (want alone), History, and Object of the versions but the deletions.
func checkDocument(t *testing.T, s *Store, want Document, history []Version) {
	t.Helper()
	view(t, s, func(tx *Tx) error {
		if d, err := tx.Document(want.Kind, want.ID); err != nil || d.Version != want.Version ||
			!bytes.Equal(d.Data, want.Data) {
			t.Errorf("Document() = %+v, %q, %v; want %+v, %q", d.Version, d.Data, err, want.Version, want.Data)
		}
		if docs, err := tx.Documents(want.Kind); err != nil || len(docs) != 1 || docs[0].Version != want.Version ||
			!bytes.Equal(docs[0].Data, want.Data) {
			t.Errorf("Documents() = %+v, %v; want %+v alone", docs, err, want.Version)
		}
		checkHistory(t, tx, want.Kind, want.ID, history)
		return nil
	})
}

// checkMissing checks what a View of s reads of the document of kind with id, file in errors, which does not exist,
// and of history, all its versions.
func checkMissing(t *testing.T, s *Store, kind content.RefKind, id, file string, history []Version) {
	t.Helper()
	view(t, s, func(tx *Tx) error {
		d, err := tx.Document(kind, id)
		if want := file + ": not found"; !errors.Is(err, fs.ErrNotExist) || err.Error() != want {
			t.Errorf("Document() = %+v, %v; want an error %q that wraps fs.ErrNotExist", d.Version, err, want)
		}
		if docs, err := tx.Documents(kind); err != nil || docs != nil {
			t.Errorf("Documents() = %+v, %v; want none", docs, err)
		}
		checkHistory(t, tx, kind, id, history)
		return nil
	})
}

// checkHistory checks that the versions of the document of kind with id are want, and that Object reads the data of
// each but the deletions.
func checkHistory(t *testing.T, tx *Tx, kind content.RefKind, id string, want []Version) {
	t.Helper()
	if got, err := tx.History(kind, id); err != nil || !slices.Equal(got, want) {
		t.Errorf("History() = %+v, %v; want %+v", got, err, want)
	}
	for _, v := range want {
		if v.Hash == "" {
			continue
		}
		if data, err := tx.Object(v.Hash); err != nil || sum(data) != v.Hash {
			t.Errorf("Object(%q) = %q, %v", v.Hash, data, err)
		}
	}
}

func TestDocuments(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		later := testTime.Add(time.Hour)
		for _, tt := range testDocs {
			t.Run(string(tt.kind), func(t *testing.T) {
				s, _ := newStore(t, tb)
				// The same JSON, but other bytes.
				first, second := docJSON(tt.kind, tt.id), append(docJSON(tt.kind, tt.id), '\n')
				checkMissing(t, s, tt.kind, tt.id, tt.file, nil)

				v1 := Version{Seq: 1, Kind: tt.kind, ID: tt.id, Hash: sum(first), SavedAt: testTime.UTC(),
					SavedBy: userID}
				put(t, s, tt.kind, tt.id, first, userID, v1)
				checkDocument(t, s, Document{v1, first}, []Version{v1})
				s.now = func() time.Time { return later }
				put(t, s, tt.kind, tt.id, first, otherUser, v1)
				checkDocument(t, s, Document{v1, first}, []Version{v1})
				v2 := Version{Seq: 2, Kind: tt.kind, ID: tt.id, Hash: sum(second), SavedAt: later.UTC(),
					SavedBy: otherUser}
				put(t, s, tt.kind, tt.id, second, otherUser, v2)
				checkDocument(t, s, Document{v2, second}, []Version{v1, v2})

				savedAt := []string{"2026-11-03T08:00:00.120000000Z", "2026-11-03T09:00:00.120000000Z"}
				if tt.kind == SettingsKind {
					err := s.Update(t.Context(), func(tx *Tx) error {
						_, err := tx.Delete(tt.kind, tt.id, userID)
						return err
					})
					if want := "the settings document cannot be deleted"; err == nil || err.Error() != want {
						t.Errorf("Delete() = %v, want %q", err, want)
					}
					checkDocument(t, s, Document{v2, second}, []Version{v1, v2})
				} else {
					v3 := Version{Seq: 3, Kind: tt.kind, ID: tt.id, SavedAt: later.UTC(), SavedBy: userID}
					update(t, s, func(tx *Tx) error {
						v, err := tx.Delete(tt.kind, tt.id, userID)
						if err == nil && v != v3 {
							t.Errorf("Delete() = %+v, want %+v", v, v3)
						}
						return err
					})
					checkMissing(t, s, tt.kind, tt.id, tt.file, []Version{v1, v2, v3})
					other := tt.id[:len("ent_")] + "01JE0000000000000000000000"
					for _, id := range []string{tt.id, other} {
						file := strings.Replace(tt.file, tt.id, id, 1)
						err := s.Update(t.Context(), func(tx *Tx) error {
							_, err := tx.Delete(tt.kind, id, userID)
							return err
						})
						if want := file + ": not found"; !errors.Is(err, fs.ErrNotExist) || err.Error() != want {
							t.Errorf("Delete() of %s = %v, want an error %q that wraps fs.ErrNotExist", file, err, want)
						}
					}

					v4 := Version{Seq: 4, Kind: tt.kind, ID: tt.id, Hash: sum(first), SavedAt: later.UTC(),
						SavedBy: userID}
					put(t, s, tt.kind, tt.id, first, userID, v4)
					checkDocument(t, s, Document{v4, first}, []Version{v1, v2, v3, v4})
					savedAt = append(savedAt, savedAt[1], savedAt[1])
				}
				if got := savedAts(t, s, "versions"); !slices.Equal(got, savedAt) {
					t.Errorf("saved_at = %q, want %q", got, savedAt)
				}
				if n := count(t, s, "objects"); n != 2 {
					t.Errorf("objects = %d, want 2", n)
				}
				checkConsistent(t, s)
			})
		}
	})
}

// TestSavedAt checks that the versions of an Update have the time when it began, which it takes from s.now once.
func TestSavedAt(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, _ := newStore(t, tb)
		n := 0
		s.now = func() time.Time {
			n++
			return testTime.Add(time.Duration(n) * time.Nanosecond)
		}
		var got []Version
		add := func(v Version, err error) error {
			got = append(got, v)
			return err
		}
		update(t, s, func(tx *Tx) error {
			return errors.Join(
				add(tx.Put(content.RefEntry, entryID, docJSON(content.RefEntry, entryID), userID)),
				add(tx.Put(content.RefTerm, termID, docJSON(content.RefTerm, termID), userID)),
				add(tx.Delete(content.RefTerm, termID, userID)),
			)
		})
		update(t, s, func(tx *Tx) error {
			return add(tx.Put(content.RefEntry, pageID, docJSON(content.RefEntry, pageID), userID))
		})
		view(t, s, func(*Tx) error { return nil })
		if n != 2 {
			t.Errorf("calls of s.now = %d, want 2", n)
		}
		for i, v := range got {
			want := testTime.Add(time.Nanosecond).UTC()
			if i == len(got)-1 {
				want = want.Add(time.Nanosecond)
			}
			if v.SavedAt != want {
				t.Errorf("SavedAt of version %d = %v, want %v", v.Seq, v.SavedAt, want)
			}
		}
		first, second := "2026-11-03T08:00:00.120000001Z", "2026-11-03T08:00:00.120000002Z"
		if got, want := savedAts(t, s, "versions"), []string{first, first, first, second}; !slices.Equal(got, want) {
			t.Errorf("saved_at = %q, want %q", got, want)
		}
		checkConsistent(t, s)
	})
}

func TestDocumentsOrder(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, _ := newStore(t, tb)
		ids := []string{pageID, entryID, "ent_01JE0000000000000000000000", "ent_01JA0000000000000000000000"}
		update(t, s, func(tx *Tx) error {
			for _, id := range ids {
				if _, err := tx.Put(content.RefEntry, id, docJSON(content.RefEntry, id), userID); err != nil {
					return err
				}
			}
			_, err := tx.Put(content.RefTerm, termID, docJSON(content.RefTerm, termID), userID)
			return err
		})
		view(t, s, func(tx *Tx) error {
			docs, err := tx.Documents(content.RefEntry)
			var got []string
			for _, d := range docs {
				got = append(got, d.ID)
			}
			if want := slices.Sorted(slices.Values(ids)); err != nil || !slices.Equal(got, want) {
				t.Errorf("IDs of Documents() = %q, %v; want %q", got, err, want)
			}
			return nil
		})
	})
}

func TestPutErrors(t *testing.T) {
	const (
		entry = content.RefEntry
		use   = ` and a ULID (26 characters in upper case)`
	)
	entryFile := "entries/" + entryID + ".json"
	valid := docJSON(entry, entryID)
	largest, big := make([]byte, content.MaxDocumentBytes), make([]byte, content.MaxDocumentBytes+1)
	tests := []struct {
		name    string
		kind    content.RefKind
		id      string
		data    []byte
		by      string
		want    string
		problem bool // content.Errors
	}{
		{name: "unsupported kind", kind: "page", id: entryID, data: valid, by: userID, want: `unsupported kind "page"`},
		{name: "revision", kind: content.RefRevision, id: revisionID, data: valid, by: userID,
			want: `unsupported kind "revision"`},
		{name: "user", kind: content.RefUser, id: userID, data: valid, by: userID, want: `unsupported kind "user"`},
		{name: "no kind", id: entryID, data: valid, by: userID, want: `unsupported kind ""`},
		{name: "kind of a directory", kind: "entries", id: entryID, data: valid, by: userID,
			want: `unsupported kind "entries"`},
		{name: "invalid ID", kind: entry, id: "ent_x", data: valid, by: userID,
			want: `invalid entry ID "ent_x": use "ent_"` + use},
		{name: "ID of another kind", kind: entry, id: termID, data: valid, by: userID,
			want: `invalid entry ID "` + termID + `": use "ent_"` + use},
		{name: "path as an ID", kind: entry, id: "ent_/../../site/settings", data: valid, by: userID,
			want: `invalid entry ID "ent_/../../site/settings": use "ent_"` + use},
		{name: "no ID", kind: entry, data: valid, by: userID, want: `invalid entry ID "": use "ent_"` + use},
		{name: "settings with an ID", kind: SettingsKind, id: entryID, data: docJSON(SettingsKind, ""), by: userID,
			want: "the settings document has no ID"},
		{name: "invalid user", kind: entry, id: entryID, data: valid, by: "usr_x",
			want: `invalid user ID "usr_x": use "usr_"` + use},
		{name: "ID of another kind as the user", kind: entry, id: entryID, data: valid, by: entryID,
			want: `invalid user ID "` + entryID + `": use "usr_"` + use},
		{name: "no user", kind: entry, id: entryID, data: valid, want: `invalid user ID "": use "usr_"` + use},
		{name: "too large", kind: entry, id: entryID, data: big, by: userID,
			want: entryFile + ": too large: 67108865 bytes (max 67108864)", problem: true},
		{name: "too large settings", kind: SettingsKind, data: big, by: userID,
			want: "site/settings.json: too large: 67108865 bytes (max 67108864)", problem: true},
		{name: "largest", kind: entry, id: entryID, data: largest, by: userID,
			want: entryFile + `:1:1: invalid character '\x00' at start of value`, problem: true},
		{name: "empty", kind: entry, id: entryID, by: userID,
			want: entryFile + ":1:1: unexpected end of JSON input", problem: true},
		{name: "empty object", kind: entry, id: entryID, data: []byte("{}"), by: userID,
			want: entryFile + ":1:1: empty object is not allowed", problem: true},
		{name: "array", kind: entry, id: entryID, data: []byte("[]"), by: userID,
			want: entryFile + ":1:1: expected object, got array", problem: true},
		{name: "syntax error", kind: entry, id: entryID, data: []byte(`{"schemaVersion": 1,}`), by: userID,
			want: entryFile + ":1:20: invalid character ',' at start of value", problem: true},
		{name: "invalid UTF-8", kind: entry, id: entryID, data: []byte("{\"schemaVersion\": 1, \"type\": \"\xff\"}"),
			by: userID, want: entryFile + ":1:31: invalid UTF-8", problem: true},
		{name: "data after the value", kind: entry, id: entryID, data: []byte(`{"schemaVersion": 1} {}`), by: userID,
			want: entryFile + ":1:22: unexpected data after value", problem: true},
		{name: "missing schemaVersion", kind: entry, id: entryID, data: []byte(`{"id": "` + entryID + `"}`), by: userID,
			want: entryFile + `:1:1: missing key "schemaVersion"`, problem: true},
		{name: "unsupported schemaVersion", kind: entry, id: entryID, data: []byte(`{"schemaVersion": 2}`), by: userID,
			want: entryFile + ":1:2: /schemaVersion: unsupported schemaVersion 2 (use 1)", problem: true},
		{name: "unknown key", kind: entry, id: entryID, data: []byte(`{"schemaVersion": 1, "foo": 2}`), by: userID,
			want: entryFile + ":1:22: /foo: unknown key", problem: true},
		{name: "duplicate key", kind: entry, id: entryID, data: []byte(`{"schemaVersion": 1, "schemaVersion": 1}`),
			by: userID, want: entryFile + ":1:22: duplicate key", problem: true},
		{name: "term as an entry", kind: entry, id: entryID,
			data: []byte(`{"schemaVersion": 1, "id": "` + entryID + `", "taxonomy": "category"}`), by: userID,
			want: entryFile + ":1:62: /taxonomy: unknown key", problem: true},
		{name: "schema file in the settings document", kind: SettingsKind,
			data: []byte(`{"schemaVersion": 1, "types": {}}`), by: userID,
			want: "site/settings.json:1:22: /types: unknown key; it belongs in the schema file", problem: true},
		{name: "ID of another document", kind: entry, id: entryID, data: docJSON(entry, pageID), by: userID,
			want:    entryFile + `:1:22: /id: id "` + pageID + `" differs from the ID of the document, "` + entryID + `"`,
			problem: true},
		{name: "ID of another document on its line", kind: content.RefTerm, id: termID,
			data: []byte("{\n  \"schemaVersion\": 1,\n  \"id\": \"" + entryID + "\"\n}\n"), by: userID,
			want: "terms/" + termID + `.json:3:3: /id: id "` + entryID + `" differs from the ID of the document, "` +
				termID + `"`,
			problem: true},
	}
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, _ := newStore(t, tb)
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				var err error
				update(t, s, func(tx *Tx) error {
					_, err = tx.Put(tt.kind, tt.id, tt.data, tt.by)
					return nil
				})
				var es content.Errors
				if err == nil || err.Error() != tt.want || errors.As(err, &es) != tt.problem {
					t.Errorf("Put() = %v (%T), want %q", err, err, tt.want)
				}
				checkEmpty(t, s)
			})
		}
	})
}

func TestDeleteErrors(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		const use = ` and a ULID (26 characters in upper case)`
		s, _ := newStore(t, tb)
		data := docJSON(content.RefEntry, entryID)
		v := Version{Seq: 1, Kind: content.RefEntry, ID: entryID, Hash: sum(data), SavedAt: testTime.UTC(),
			SavedBy: userID}
		put(t, s, content.RefEntry, entryID, data, userID, v)
		put(t, s, SettingsKind, "", docJSON(SettingsKind, ""), userID, Version{
			Seq: 2, Kind: SettingsKind, Hash: sum(docJSON(SettingsKind, "")), SavedAt: testTime.UTC(), SavedBy: userID,
		})
		for _, tt := range []struct {
			name     string
			kind     content.RefKind
			id, by   string
			want     string
			notExist bool
		}{
			{"unsupported kind", "page", entryID, userID, `unsupported kind "page"`, false},
			{"revision", content.RefRevision, revisionID, userID, `unsupported kind "revision"`, false},
			{"invalid ID", content.RefEntry, "ent_x", userID, `invalid entry ID "ent_x": use "ent_"` + use, false},
			{"ID of another kind", content.RefTerm, entryID, userID,
				`invalid term ID "` + entryID + `": use "trm_"` + use, false},
			{"settings with an ID", SettingsKind, entryID, userID, "the settings document has no ID", false},
			{"settings", SettingsKind, "", userID, "the settings document cannot be deleted", false},
			{"invalid user", content.RefEntry, entryID, "usr_x", `invalid user ID "usr_x": use "usr_"` + use, false},
			{"no user", content.RefEntry, entryID, "", `invalid user ID "": use "usr_"` + use, false},
			{"missing", content.RefEntry, pageID, userID, "entries/" + pageID + ".json: not found", true},
			{"missing of another kind", content.RefTerm, termID, userID, "terms/" + termID + ".json: not found", true},
		} {
			t.Run(tt.name, func(t *testing.T) {
				var err error
				update(t, s, func(tx *Tx) error {
					_, err = tx.Delete(tt.kind, tt.id, tt.by)
					return nil
				})
				if err == nil || err.Error() != tt.want || errors.Is(err, fs.ErrNotExist) != tt.notExist {
					t.Errorf("Delete() = %v, want %q", err, tt.want)
				}
				checkDocument(t, s, Document{v, data}, []Version{v})
			})
		}
		if n := count(t, s, "versions"); n != 2 {
			t.Errorf("versions = %d, want 2", n)
		}
		checkConsistent(t, s)
	})
}

// TestCorruptObject changes the data of the objects of an entry and its revision behind the back of the store (on
// SQLite, past the triggers of the tables that keep it from changing), and checks that the reads of the data fail.
func TestCorruptObject(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, _ := newStore(t, tb)
		putWithRevision(t, s)
		corrupt := corruptObjects(t, s)
		changed := func(data []byte) string {
			return "object " + sum(data) + ": the data has the hash " + sum(corrupt(data))
		}
		entry, rev := changed(docJSON(content.RefEntry, entryID)), changed(revJSON("About"))
		// History and Revisions read no data.
		wants := map[string]string{"Document": entry, "Documents": entry, "Object": entry, "Revision": rev}
		view(t, s, func(tx *Tx) error {
			for name, call := range calls(tx) {
				if slices.Contains(writes, name) {
					continue
				}
				err := call()
				if want, ok := wants[name]; ok && (err == nil || err.Error() != want) {
					t.Errorf("%s() of a changed object = %v, want %q", name, err, want)
				} else if !ok && err != nil {
					t.Errorf("%s() = %v", name, err)
				}
			}
			if h, err := tx.History(content.RefEntry, entryID); err != nil || len(h) != 1 {
				t.Errorf("History() = %+v, %v; want the version", h, err)
			}
			if revs, err := tx.Revisions(entryID); err != nil || len(revs) != 1 {
				t.Errorf("Revisions() = %+v, %v; want the revision", revs, err)
			}
			return nil
		})
	})
}

func TestObjectErrors(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, _ := newStore(t, tb)
		digits := strings.Repeat("0", 64)
		view(t, s, func(tx *Tx) error {
			for _, h := range []string{
				"sha256:" + strings.Repeat("A", 64),
				digits,
				"sha256:" + digits[:63],
				"sha256:" + digits + "0",
				"sha512:" + digits,
				"SHA256:" + digits,
				"",
			} {
				data, err := tx.Object(h)
				if want := `invalid hash "` + h + `": use "sha256:" and 64 lower case hex digits`; data != nil ||
					err == nil || err.Error() != want {
					t.Errorf("Object(%q) = %q, %v; want an error %q", h, data, err, want)
				}
			}
			data, err := tx.Object("sha256:" + digits)
			if want := "object sha256:" + digits + ": not found"; data != nil || !errors.Is(err, fs.ErrNotExist) ||
				err.Error() != want {
				t.Errorf("Object() of a missing object = %q, %v; want an error %q that wraps fs.ErrNotExist", data, err,
					want)
			}
			return nil
		})
	})
}

func TestReadErrors(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, _ := newStore(t, tb)
		const use = ` and a ULID (26 characters in upper case)`
		view(t, s, func(tx *Tx) error {
			for _, tt := range []struct {
				kind content.RefKind
				id   string
				want string
			}{
				{"page", entryID, `unsupported kind "page"`},
				{content.RefRevision, revisionID, `unsupported kind "revision"`},
				{content.RefEntry, "ent_x", `invalid entry ID "ent_x": use "ent_"` + use},
				{content.RefEntry, "", `invalid entry ID "": use "ent_"` + use},
				{SettingsKind, entryID, "the settings document has no ID"},
			} {
				if d, err := tx.Document(tt.kind, tt.id); err == nil || err.Error() != tt.want {
					t.Errorf("Document(%q, %q) = %+v, %v; want an error %q", tt.kind, tt.id, d.Version, err, tt.want)
				}
				if h, err := tx.History(tt.kind, tt.id); h != nil || err == nil || err.Error() != tt.want {
					t.Errorf("History(%q, %q) = %+v, %v; want an error %q", tt.kind, tt.id, h, err, tt.want)
				}
			}
			for _, kind := range []content.RefKind{"page", content.RefRevision, content.RefUser, ""} {
				if docs, err := tx.Documents(kind); docs != nil || err == nil ||
					err.Error() != `unsupported kind "`+string(kind)+`"` {
					t.Errorf("Documents(%q) = %+v, %v; want an error", kind, docs, err)
				}
			}
			return nil
		})
	})
}

// TestSampleSite puts the settings document and the documents of every kind of the sample site of internal/build in
// one Update, and reads them back.
func TestSampleSite(t *testing.T) {
	d, err := sitedir.Open(filepath.Join("..", "build", "testdata", "site"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	settings, _, err := d.Settings()
	if err != nil {
		t.Fatalf("Settings() = %v", err)
	}
	files := map[content.RefKind]map[string]content.SiteFile{SettingsKind: {"": settings}}
	for _, tt := range testDocs {
		if tt.kind == SettingsKind {
			continue
		}
		docs, err := d.Documents(tt.kind)
		if err != nil || len(docs) == 0 {
			t.Fatalf("Documents(%q) of the sample site = %d documents, %v", tt.kind, len(docs), err)
		}
		files[tt.kind] = docs
	}

	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, _ := newStore(t, tb)
		n := 0
		update(t, s, func(tx *Tx) error {
			for kind, docs := range files {
				// Backwards, so that Documents has to sort them.
				for _, id := range slices.Backward(slices.Sorted(maps.Keys(docs))) {
					if _, err := tx.Put(kind, id, docs[id].Data, userID); err != nil {
						return err
					}
					n++
				}
			}
			return nil
		})
		view(t, s, func(tx *Tx) error {
			for kind, docs := range files {
				got, err := tx.Documents(kind)
				if err != nil {
					return err
				}
				var ids []string
				for _, d := range got {
					ids = append(ids, d.ID)
					if f := docs[d.ID]; !bytes.Equal(d.Data, f.Data) || d.Hash != sum(f.Data) {
						t.Errorf("Documents(%q) has %s with %q (%s), want %q", kind, d.ID, d.Data, d.Hash, f.Data)
					}
				}
				if want := slices.Sorted(maps.Keys(docs)); !slices.Equal(ids, want) {
					t.Errorf("IDs of Documents(%q) = %q, want %q", kind, ids, want)
				}
			}
			return nil
		})
		if got := count(t, s, "versions"); got != n {
			t.Errorf("versions = %d, want %d", got, n)
		}
		checkConsistent(t, s)
	})
}
