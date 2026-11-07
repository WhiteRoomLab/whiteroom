package store

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// IDs from the tests of internal/content.
const (
	entryID    = "ent_01JB8W2N5KD3F6H9J1M4P7R2T5"
	pageID     = "ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3"
	termID     = "trm_01JBA4C7E2G5J8K1M4P7R2T5W8"
	mediaID    = "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5"
	authorID   = "aut_01JBA4C7N9Q3T6W9Z3C6F9H2K5"
	menuID     = "mnu_01JBD2M5N6P7Q8R9S0T1V2W3X4"
	formID     = "frm_01JB8Z3K5VP1D4G7J2M5Q8T3W6"
	redirectID = "rdr_01JBD3A4B5C6D7E8F9G0H1J2K3"
	revisionID = "rev_01JB8X9Y3ZE5G8K1N4Q7S2V6W9"
	userID     = "usr_01JB8T7H4MA2C5E8G1J4K7N3P6"
	otherUser  = "usr_01JB8T7H4MA2C5E8G1J4K7N3P7"
)

// testTime is the time that s.now gives in the stores of newStore.
var testTime = time.Date(2026, 11, 3, 17, 0, 0, 120_000_000, time.FixedZone("JST", 9*60*60))

// update runs f in an Update of s, and stops the test if it fails.
func update(t *testing.T, s *Store, f func(*Tx) error) {
	t.Helper()
	if err := s.Update(t.Context(), f); err != nil {
		t.Fatalf("Update() = %v", err)
	}
}

// view runs f in a View of s, and stops the test if it fails.
func view(t *testing.T, s *Store, f func(*Tx) error) {
	t.Helper()
	if err := s.View(t.Context(), f); err != nil {
		t.Fatalf("View() = %v", err)
	}
}

// docJSON returns the smallest JSON of the document of kind with id that Put takes, which has the id unless it is the
// settings document.
func docJSON(kind content.RefKind, id string) []byte {
	if kind == SettingsKind {
		return []byte(`{"schemaVersion": 1}`)
	}
	return []byte(`{"schemaVersion": 1, "id": "` + id + `"}`)
}

// revJSON returns the JSON of a revision with title, which AddRevision takes.
func revJSON(title string) []byte {
	return []byte(`{"schemaVersion": 1, "title": "` + title + `"}`)
}

// sum returns the hash of data, as Version.Hash has it.
func sum(data []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data))
}

// checkEmpty checks that s has no object, no version, no current document, no revision, and nothing in the index or the
// operations.
func checkEmpty(t *testing.T, s *Store) {
	t.Helper()
	tables := []string{"objects", "versions", "documents", "revisions", "summaries", "references", "operations"}
	for _, table := range tables {
		if n := count(t, s, table); n != 0 {
			t.Errorf("rows of %s = %d, want 0", table, n)
		}
	}
}

// modeOf returns the mode of name, without following a symbolic link.
func modeOf(t *testing.T, name string) fs.FileMode {
	t.Helper()
	info, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode()
}

// names returns the names of the entries of dir, sorted.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// symlink makes a symbolic link at name to target.
func symlink(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		t.Skipf("cannot make symbolic links: %v", err)
	}
}

func TestCreate(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		l := logLayout()
		parent := filepath.Join(t.TempDir(), "parent")
		dir := filepath.Join(parent, "data")
		s, err := tb.create(dir)
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		if got, want := modeOf(t, parent), fs.ModeDir|0o700; got != want {
			t.Errorf("mode of %s = %v, want %v", parent, got, want)
		}
		for _, name := range slices.Sorted(maps.Keys(l.created)) {
			p := filepath.Join(dir, name)
			if got := modeOf(t, p); got != l.created[name] {
				t.Errorf("mode of %s = %v, want %v", p, got, l.created[name])
			}
		}
		if got := modes(t, dir); !maps.Equal(got, l.created) {
			t.Errorf("the data directory after Create() has %v, want %v", got, l.created)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}
		var top []string // the names in the data directory
		for name := range l.closed {
			if name != "." && !strings.Contains(name, "/") {
				top = append(top, name)
			}
		}
		slices.Sort(top)
		if got := names(t, dir); !slices.Equal(got, top) {
			t.Errorf("the data directory after Close() has %q, want %q", got, top)
		}
		if got := names(t, filepath.Join(dir, "files")); got != nil {
			t.Errorf("files/ has %q, want nothing", got)
		}
		if got := modes(t, dir); !maps.Equal(got, l.closed) {
			t.Errorf("the modes in the data directory after Close() are %v, want %v", got, l.closed)
		}

		// An empty directory keeps its mode.
		dir = filepath.Join(t.TempDir(), "data")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		s, err = tb.create(dir)
		if err != nil {
			t.Fatalf("Create() in an empty directory = %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		if got := modeOf(t, dir); got != fs.ModeDir|0o750 {
			t.Errorf("mode of the empty directory after Create() = %v, want %v", got, fs.ModeDir|0o750)
		}
		update(t, s, func(tx *Tx) error {
			_, err := tx.Put(content.RefEntry, entryID, docJSON(content.RefEntry, entryID), userID)
			return err
		})
	})
}

func TestCreateErrors(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		notEmpty := t.TempDir()
		if err := os.WriteFile(filepath.Join(notEmpty, "README.md"), []byte("# Data\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, data := newStore(t, tb)
		for _, dir := range []string{notEmpty, data} {
			before := names(t, dir)
			s, err := tb.create(dir)
			if err == nil {
				_ = s.Close()
			}
			if want := dir + ": not empty; a new data directory needs a missing or empty directory"; err == nil ||
				err.Error() != want {
				t.Errorf("Create(%q) = %v, want %q", dir, err, want)
			}
			if got := names(t, dir); !slices.Equal(got, before) {
				t.Errorf("Create(%q) has changed the directory: %q, want %q", dir, got, before)
			}
		}

		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := tb.create(file)
		if err == nil {
			_ = s.Close()
		}
		if want := "mkdir " + file + ": not a directory"; err == nil || err.Error() != want {
			t.Errorf("Create() of a file = %v, want %q", err, want)
		}
	})
}

func TestOpen(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, dir := newStore(t, tb)
		data := docJSON(content.RefEntry, entryID)
		var want Version
		update(t, s, func(tx *Tx) error {
			var err error
			want, err = tx.Put(content.RefEntry, entryID, data, userID)
			return err
		})
		if err := s.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}

		s, err := tb.open(dir)
		if err != nil {
			t.Fatalf("Open() = %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		view(t, s, func(tx *Tx) error {
			d, err := tx.Document(content.RefEntry, entryID)
			if err != nil || d.Version != want || !bytes.Equal(d.Data, data) {
				t.Errorf("Document() after Open() = %+v, %q, %v; want %+v, %q", d.Version, d.Data, err, want, data)
			}
			return nil
		})
		update(t, s, func(tx *Tx) error {
			v, err := tx.Put(content.RefEntry, pageID, docJSON(content.RefEntry, pageID), userID)
			if err == nil && (v.Seq != want.Seq+1 || v.SavedAt.Location() != time.UTC) {
				t.Errorf("Put() after Open() = %+v, want the seq %d in UTC", v, want.Seq+1)
			}
			return err
		})
	})
}

func TestOpenErrors(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		opens := map[string]func(dir string) (*Store, error){
			"Open":          tb.open,
			"openLogReader": func(dir string) (*Store, error) { return openLogReader(dir, tb.options()) },
			"OpenReader":    OpenReader,
		}
		for _, tt := range append(logOpenErrors(), []openErrorCase{
			{
				name:  "missing files",
				setup: func(t *testing.T, path func(string) string) { remove(t, path("files")) },
				file:  "files",
				want:  "missing directory",
			},
			{
				name: "files as a symbolic link",
				setup: func(t *testing.T, path func(string) string) {
					rename(t, path("files"), path("other"))
					symlink(t, "other", path("files"))
				},
				file: "files",
				want: "symbolic links are not allowed",
			},
			{
				name: "files as a file",
				setup: func(t *testing.T, path func(string) string) {
					remove(t, path("files"))
					if err := os.WriteFile(path("files"), nil, 0o600); err != nil {
						t.Fatal(err)
					}
				},
				file: "files",
				want: "must be a directory",
			},
		}...) {
			t.Run(tt.name, func(t *testing.T) {
				s, dir := newStore(t, tb)
				if err := s.Close(); err != nil {
					t.Fatalf("Close() = %v", err)
				}
				path := func(name string) string { return filepath.Join(dir, name) }
				tt.setup(t, path)
				for name, open := range opens {
					s, err := open(dir)
					if err == nil {
						_ = s.Close()
					}
					if want := path(tt.file) + ": " + tt.want; err == nil || err.Error() != want {
						t.Errorf("%s() = %v, want %q", name, err, want)
					}
				}
			})
		}

		missing := filepath.Join(t.TempDir(), "missing")
		if s, err := tb.open(missing); !errors.Is(err, fs.ErrNotExist) {
			if err == nil {
				_ = s.Close()
			}
			t.Errorf("Open() of a missing directory = %v, want an error that wraps fs.ErrNotExist", err)
		}
		if _, err := os.Lstat(missing); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Lstat() of the missing directory after Open() = %v, want fs.ErrNotExist", err)
		}
	})
}

// TestDefaults checks the options of the engine of Create, Open and OpenReader (3.4): objects of 64 KiB or more go in
// files, and a second Open waits 2 seconds for the writer lock.
func TestDefaults(t *testing.T) {
	t.Parallel()
	// paddedEntry returns the JSON of the entry id with size bytes.
	paddedEntry := func(id string, size int) []byte {
		head, tail := `{"schemaVersion": 1, "id": "`+id+`", "type": "`, `"}`
		return []byte(head + strings.Repeat("x", size-len(head)-len(tail)) + tail)
	}
	small, large := paddedEntry(entryID, 64<<10-1), paddedEntry(pageID, 64<<10)
	dir := filepath.Join(t.TempDir(), "data")
	s, err := Create(dir)
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	update(t, s, func(tx *Tx) error {
		if _, err := tx.Put(content.RefEntry, entryID, small, userID); err != nil {
			return err
		}
		_, err := tx.Put(content.RefEntry, pageID, large, userID)
		return err
	})
	digits := strings.TrimPrefix(sum(large), "sha256:")
	objects := filepath.Join(dir, "logdb", "objects")
	if got := names(t, objects); !slices.Equal(got, []string{digits[:2]}) {
		t.Errorf("logdb/objects/ has %q, want the directory of the larger object alone, %q", got, digits[:2])
	}
	if got := names(t, filepath.Join(objects, digits[:2])); !slices.Equal(got, []string{digits}) {
		t.Errorf("logdb/objects/%s has %q, want %q", digits[:2], got, digits)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	s, err = Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	view(t, s, func(tx *Tx) error {
		for id, want := range map[string][]byte{entryID: small, pageID: large} {
			if d, err := tx.Document(content.RefEntry, id); err != nil || !bytes.Equal(d.Data, want) {
				t.Errorf("Document(%q) after Open() = %d bytes, %v; want %d bytes", id, len(d.Data), err, len(want))
			}
		}
		return nil
	})

	// The writer holds the lock: a second writer gives up after the 2 seconds of the engine, and a reader opens.
	start := time.Now()
	second, err := Open(dir)
	if err == nil {
		_ = second.Close()
	}
	waited := time.Since(start)
	if want := dir + "/logdb: another process holds the writer lock"; err == nil || err.Error() != want {
		t.Errorf("a second Open() = %v, want %q", err, want)
	}
	if waited < 2*time.Second || waited >= 10*time.Second {
		t.Errorf("a second Open() has waited %v, want 2 seconds or more, and less than 10", waited)
	}
	r, err := OpenReader(dir)
	if err != nil {
		t.Fatalf("OpenReader() = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	view(t, r, func(tx *Tx) error {
		docs, err := tx.Documents(content.RefEntry)
		if err != nil || len(docs) != 2 || docs[0].ID != entryID || docs[1].ID != pageID ||
			!bytes.Equal(docs[0].Data, small) || !bytes.Equal(docs[1].Data, large) {
			t.Errorf("Documents() of the reader = %d documents, %v; want the entry and the page, with the data put",
				len(docs), err)
		}
		return nil
	})
}

func remove(t *testing.T, name string) {
	t.Helper()
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
}

func rename(t *testing.T, from, to string) {
	t.Helper()
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, name string) {
	t.Helper()
	if err := os.Mkdir(name, 0o700); err != nil {
		t.Fatal(err)
	}
}

// calls are calls of every method of tx, by name, on the entry entryID or its data, and on its revision revisionID or
// its data, revJSON("About"), and on the operation op_A, which return their errors.
func calls(tx *Tx) map[string]func() error {
	data := docJSON(content.RefEntry, entryID)
	return map[string]func() error{
		"Document":  func() error { _, err := tx.Document(content.RefEntry, entryID); return err },
		"Documents": func() error { _, err := tx.Documents(content.RefEntry); return err },
		"History":   func() error { _, err := tx.History(content.RefEntry, entryID); return err },
		"Object":    func() error { _, err := tx.Object(sum(data)); return err },
		"Put":       func() error { _, err := tx.Put(content.RefEntry, entryID, data, userID); return err },
		"Delete":    func() error { _, err := tx.Delete(content.RefEntry, entryID, userID); return err },
		"Revision":  func() error { _, err := tx.Revision(revisionID); return err },
		"Revisions": func() error { _, err := tx.Revisions(entryID); return err },
		"AddRevision": func() error {
			_, err := tx.AddRevision(entryID, revisionID, revJSON("About"), userID)
			return err
		},
		"Summary":      func() error { _, err := tx.Summary(content.RefEntry, entryID); return err },
		"Summaries":    func() error { _, err := tx.Summaries(content.RefEntry); return err },
		"Referrers":    func() error { _, err := tx.Referrers(content.RefEntry, entryID); return err },
		"LastNumber":   func() error { _, err := tx.LastNumber(); return err },
		"Operation":    func() error { _, _, err := tx.Operation("op_A"); return err },
		"PutOperation": func() error { return tx.PutOperation("op_A", []byte("data")) },
		"RebuildIndex": tx.RebuildIndex,
	}
}

// writes are the methods of Tx that write.
var writes = []string{"Put", "Delete", "AddRevision", "PutOperation", "RebuildIndex"}

// putWithRevision puts the entry entryID with its revision revisionID, whose data calls has, in an Update of s.
func putWithRevision(t *testing.T, s *Store) {
	t.Helper()
	update(t, s, func(tx *Tx) error {
		if _, err := tx.Put(content.RefEntry, entryID, docJSON(content.RefEntry, entryID), userID); err != nil {
			return err
		}
		_, err := tx.AddRevision(entryID, revisionID, revJSON("About"), userID)
		return err
	})
}

func TestViewCannotWrite(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, _ := newStore(t, tb)
		putWithRevision(t, s)
		view(t, s, func(tx *Tx) error {
			for name, call := range calls(tx) {
				err := call()
				if slices.Contains(writes, name) {
					if want := "read-only transaction"; err == nil || err.Error() != want {
						t.Errorf("%s() in View() = %v, want %q", name, err, want)
					}
				} else if err != nil {
					t.Errorf("%s() in View() = %v", name, err)
				}
			}
			return nil
		})
		for _, table := range []string{"versions", "revisions"} {
			if n := count(t, s, table); n != 1 {
				t.Errorf("%s after the writes in View() = %d, want 1", table, n)
			}
		}
	})
}

func TestEnded(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, _ := newStore(t, tb)
		errTest := errors.New("test")
		var kept []*Tx
		keep := func(tx *Tx) error {
			kept = append(kept, tx)
			return nil
		}
		update(t, s, keep)
		view(t, s, keep)
		if err := s.Update(t.Context(), func(tx *Tx) error {
			kept = append(kept, tx)
			return errTest
		}); !errors.Is(err, errTest) {
			t.Fatalf("Update() = %v, want the error of f", err)
		}
		for i, tx := range kept {
			for name, call := range calls(tx) {
				if err, want := call(), "the transaction has ended"; err == nil || err.Error() != want {
					t.Errorf("%s() of transaction %d after f = %v, want %q", name, i, err, want)
				}
			}
		}
		checkEmpty(t, s)
	})
}

func TestUpdateError(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, _ := newStore(t, tb)
		data := docJSON(content.RefEntry, entryID)
		var first Version
		update(t, s, func(tx *Tx) error {
			var err error
			first, err = tx.Put(content.RefEntry, entryID, data, userID)
			return err
		})

		others := []struct {
			kind content.RefKind
			id   string
		}{{content.RefEntry, pageID}, {content.RefTerm, termID}}
		errTest := errors.New("test")
		err := s.Update(t.Context(), func(tx *Tx) error {
			if _, err := tx.Delete(content.RefEntry, entryID, userID); err != nil {
				return err
			}
			for _, o := range others {
				if _, err := tx.Put(o.kind, o.id, docJSON(o.kind, o.id), userID); err != nil {
					return err
				}
			}
			// f reads what it has written.
			if docs, err := tx.Documents(content.RefEntry); err != nil || len(docs) != 1 || docs[0].ID != pageID {
				t.Errorf("Documents() in Update() = %+v, %v; want the page alone", docs, err)
			}
			return errTest
		})
		if !errors.Is(err, errTest) || err.Error() != errTest.Error() {
			t.Errorf("Update() = %v, want the error of f", err)
		}
		view(t, s, func(tx *Tx) error {
			if d, err := tx.Document(content.RefEntry, entryID); err != nil || d.Version != first {
				t.Errorf("Document() after Update() = %+v, %v; want %+v", d.Version, err, first)
			}
			for _, o := range others {
				if h, err := tx.History(o.kind, o.id); err != nil || h != nil {
					t.Errorf("History(%q) after Update() = %+v, %v; want none", o.id, h, err)
				}
			}
			return nil
		})
		for table, want := range map[string]int{"objects": 1, "versions": 1, "documents": 1} {
			if n := count(t, s, table); n != want {
				t.Errorf("rows of %s after Update() = %d, want %d", table, n, want)
			}
		}
		checkConsistent(t, s)

		if err := s.View(t.Context(), func(*Tx) error { return errTest }); !errors.Is(err, errTest) ||
			err.Error() != errTest.Error() {
			t.Errorf("View() = %v, want the error of f", err)
		}
	})
}
