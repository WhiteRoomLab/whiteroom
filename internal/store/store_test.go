package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver/dbtest"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
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

// recorded ends the Update of tx for a test that writes: if it has written and has added no record, it adds the record
// store.write that an Update needs (ErrUnrecorded), so that the tests need not tell what they write.
func recorded(tx *Tx) error {
	if tx.records > 0 || !tx.Changed() {
		return nil
	}
	return tx.Record(eventRecord(""))
}

// withRecord returns f followed by recorded, for an Update of a test that writes.
func withRecord(f func(*Tx) error) func(*Tx) error {
	return func(tx *Tx) error {
		if err := f(tx); err != nil {
			return err
		}
		return recorded(tx)
	}
}

// update runs f in an Update of s, with the record store.write if f writes without one, and stops the test if it fails.
func update(t *testing.T, s *Store, f func(*Tx) error) {
	t.Helper()
	if err := s.Update(t.Context(), withRecord(f)); err != nil {
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

// checkEmpty checks that s has no object, no version, no current document, no revision, and nothing in the index, the
// operations, the accounts or the sessions.
func checkEmpty(t *testing.T, s *Store) {
	t.Helper()
	tables := []string{
		"objects", "versions", "documents", "revisions", "summaries", "references", "operations", "accounts",
		"sessions",
	}
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

// newDaemonDir makes a new data directory with the daemon's init, as whiteroom-db init does, and returns it.
func newDaemonDir(t *testing.T, o options) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := dbtest.Init(dir, o.daemonOptions(nil)); err != nil {
		t.Fatalf("Init() = %v", err)
	}
	return dir
}

// createOnDir makes the store of the data directory dir, which a daemon has made, as Create does.
func createOnDir(dir string, o options) (*Store, error) {
	return start(dir, o, true, func(d dbclient.Dialer) (*Store, error) {
		return createOn(context.Background(), d, o.clock(), o.client())
	})
}

// state is the tables, the objects and the events of the engine of s, as the tests compare them.
func state(t *testing.T, s *Store) string {
	t.Helper()
	var b strings.Builder
	d := logDB(t, s)
	if err := d.View(t.Context(), func(tx *logdb.Tx) error {
		fmt.Fprintf(&b, "seq %d\n", tx.Seq())
		for table := uint32(1); table <= 11; table++ {
			tx.Scan(table, nil, func(k, v []byte) bool {
				fmt.Fprintf(&b, "%d %q %q\n", table, k, v)
				return true
			})
		}
		tx.Objects(func(h [sha256.Size]byte, size int64) bool {
			fmt.Fprintf(&b, "object %x %d\n", h, size)
			return true
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.Events(t.Context(), func(e logdb.Event) bool {
		fmt.Fprintf(&b, "event %d %d %q\n", e.Seq, e.Number, e.Data)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestCreate(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		// A new store has the schema 8 and the record store.create, with the mark of the daemon after it, and nothing else.
		dir := newDaemonDir(t, tb.options())
		s, err := createOnDir(dir, tb.options())
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		for _, table := range []string{"objects", "versions", "documents", "revisions", "summaries", "references",
			"operations", "accounts", "sessions"} {
			if n := count(t, s, table); n != 0 {
				t.Errorf("rows of %s after Create() = %d, want 0", table, n)
			}
		}
		events := allEvents(t, s)
		if len(events) != 2 || string(events[0].Data) != createRecordJSON || events[0].Number != 1 ||
			events[0].Seq != 1 || events[1].Number != 2 || events[1].Seq != 1 {
			t.Errorf("the events after Create() = %+v, want the record %s and the mark of the daemon", events, createRecordJSON)
		}
		if rs := allRecords(t, s); len(rs) != 1 || rs[0].Action != audit.StoreCreate {
			t.Errorf("the records after Create() = %+v, want store.create alone", rs)
		}
		if err := s.VerifyAudit(t.Context(), nil); err != nil {
			t.Errorf("VerifyAudit() after Create() = %v", err)
		}
		update(t, s, func(tx *Tx) error {
			_, err := tx.Put(content.RefEntry, entryID, docJSON(content.RefEntry, entryID), userID)
			return err
		})
		if err := s.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}

		// Create does not make a store of one that has commits, and writes nothing.
		for range 2 {
			s, err = start(dir, tb.options(), false, func(d dbclient.Dialer) (*Store, error) {
				return openOn(context.Background(), d, dbwire.RoleReader, time.Now, dbclient.Options{})
			})
			if err != nil {
				t.Fatal(err)
			}
			before := state(t, s)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = createOnDir(dir, tb.options())
			if err == nil {
				_ = s.Close()
			}
			if want := dir + ": the store is not new; a new store needs a data directory that whiteroom-db init has just made"; err == nil || err.Error() != want {
				t.Errorf("Create() of a store with commits = %v, want %q", err, want)
			}
			s, err = start(dir, tb.options(), false, func(d dbclient.Dialer) (*Store, error) {
				return openOn(context.Background(), d, dbwire.RoleReader, time.Now, dbclient.Options{})
			})
			if err != nil {
				t.Fatal(err)
			}
			if after := state(t, s); after != before {
				t.Errorf("Create() of a store with commits has changed it:\n%s\nwant\n%s", after, before)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
		}

		// The harness's create makes the data directory too, which is the daemon's init.
		s, err = tb.create(filepath.Join(t.TempDir(), "other"))
		if err != nil {
			t.Fatalf("create() = %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

// TestBackupSyncParent checks that Backup syncs the directory that holds a data directory that it makes, with a directory
// in which it can make it but which it cannot open: it fails, and names it. It does not sync it for a data directory
// that was there (fspath.TestSyncParent checks the function that it uses, and dbserver.TestCreateDirMakesDirs that
// whiteroom-db init makes the directories of a data directory in the same way).
func TestBackupSyncParent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open every directory")
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(parent, "empty")
	copied := filepath.Join(parent, "copied")
	for _, dir := range []string{empty, copied} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Write and search, but not read.
	if err := os.Chmod(parent, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	denied := func(what, name string, err error) {
		t.Helper()
		if pe, ok := errors.AsType[*fs.PathError](err); !ok || pe.Op != "open" || pe.Path != name ||
			!errors.Is(err, fs.ErrPermission) {
			t.Errorf("%s = %v, want that %s cannot be opened", what, err, name)
		}
	}

	s, err := create(empty, options{})
	if err != nil {
		t.Fatalf("create() in an empty directory = %v", err)
	}
	update(t, s, func(tx *Tx) error { return putPair(tx, 1) })
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = backup(t.Context(), empty, nil, filepath.Join(parent, "copy"), nil)
	denied("Backup() to a missing directory", parent, err)
	if _, err := backup(t.Context(), empty, nil, copied, nil); err != nil {
		t.Fatalf("Backup() to an empty directory = %v", err)
	}
	if s, err = open(copied, options{}); err != nil {
		t.Fatalf("Open() of the copy = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	view(t, s, func(tx *Tx) error {
		if n, err := checkPairView(tx); err != nil || n != 1 {
			t.Errorf("the copy has the pair of %d, %v; want 1", n, err)
		}
		return nil
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

		// The writer holds the place of the writer: a second Open fails after its wait, and a reader opens.
		o := tb.options()
		o.wait = 50 * time.Millisecond
		start := time.Now()
		second, err := open(dir, o)
		if err == nil {
			_ = second.Close()
		}
		if want := dir + ": another process is connected as the writer"; err == nil || err.Error() != want {
			t.Errorf("a second Open() = %v, want %q", err, want)
		}
		if waited := time.Since(start); waited < o.wait || waited >= time.Second {
			t.Errorf("a second Open() has waited %v, want the wait of %v or more, and less than a second", waited, o.wait)
		}
		r, err := openLogReader(dir, tb.options())
		if err != nil {
			t.Fatalf("OpenReader() beside the writer = %v", err)
		}
		view(t, r, func(tx *Tx) error {
			if d, err := tx.Document(content.RefEntry, entryID); err != nil || d.Version != want {
				t.Errorf("Document() of the reader = %+v, %v; want %+v", d.Version, err, want)
			}
			return nil
		})
		if err := r.Close(); err != nil {
			t.Fatalf("Close() of the reader = %v", err)
		}

		if err := s.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}
		s, err = tb.open(dir)
		if err != nil {
			t.Fatalf("Open() after the first writer has closed = %v", err)
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

// TestOpenNoStore checks that Open and OpenReader of the daemon of a data directory that whiteroom-db init has just
// made, whose store nobody has made, fail with an error that wraps ErrNoStore, and write nothing.
func TestOpenNoStore(t *testing.T) {
	forBackends(t, oneBackend, func(t *testing.T, tb testBackend) {
		dir := newDaemonDir(t, tb.options())
		for name, open := range map[string]func(string, options) (*Store, error){"Open": open, "OpenReader": openLogReader} {
			s, err := open(dir, tb.options())
			if err == nil {
				_ = s.Close()
			}
			if want := dir + ": not a store of whiteroom (no schema version)"; !errors.Is(err, ErrNoStore) ||
				err.Error() != want {
				t.Errorf("%s() = %v, want an error that wraps ErrNoStore, with the text %q", name, err, want)
			}
		}
		s, err := createOnDir(dir, tb.options())
		if err != nil {
			t.Fatalf("Create() after the failed opens = %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

// TestOpenSends checks that the open of a store of the current schema, as a reader or as a writer, sends no Commit: the
// sequence number of the daemon stays.
func TestOpenSends(t *testing.T) {
	forBackends(t, oneBackend, func(t *testing.T, tb testBackend) {
		s, dir := newStore(t, tb)
		putWithRevision(t, s)
		seq := seqOf(t, s)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		for name, open := range map[string]func(string, options) (*Store, error){"open": open, "openLogReader": openLogReader} {
			s, err := open(dir, tb.options())
			if err != nil {
				t.Fatalf("%s() = %v", name, err)
			}
			if got := seqOf(t, s); got != seq {
				t.Errorf("the last commit after %s() is %d, want %d: the open sends no commit", name, got, seq)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
		}
	})
}

// seqOf returns the sequence number of the last commit of the daemon of s.
func seqOf(t *testing.T, s *Store) uint64 {
	t.Helper()
	var seq uint64
	view(t, s, func(tx *Tx) error {
		seq = tx.b.(*logTx).tx.Seq()
		return nil
	})
	return seq
}

// TestDefaults checks the options of the engine that the daemon of the tests has when a test gives none, which are those
// of whiteroom-db serve (3.4): objects of 64 KiB or more go in files. The wait for the writer is that of the client,
// which TestOpen checks.
func TestDefaults(t *testing.T) {
	t.Parallel()
	// paddedEntry returns the JSON of the entry id with size bytes.
	paddedEntry := func(id string, size int) []byte {
		head, tail := `{"schemaVersion": 1, "id": "`+id+`", "type": "`, `"}`
		return []byte(head + strings.Repeat("x", size-len(head)-len(tail)) + tail)
	}
	small, large := paddedEntry(entryID, 64<<10-1), paddedEntry(pageID, 64<<10)
	dir := filepath.Join(t.TempDir(), "data")
	s, err := create(dir, options{})
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

	s, err = open(dir, options{})
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
	r, err := openLogReader(dir, options{})
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
// its data, revJSON("About"), on the operation op_A, on the account userID, and on the session whose key is that of
// "a", which return their errors.
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
		"ReadAhead": func() error {
			return tx.ReadAhead(Ahead{
				Summaries: []DocumentID{{content.RefEntry, entryID}}, Documents: []DocumentID{{content.RefEntry, entryID}},
				Revisions: []string{revisionID},
			})
		},
		"Summary":         func() error { _, err := tx.Summary(content.RefEntry, entryID); return err },
		"Summaries":       func() error { _, err := tx.Summaries(content.RefEntry); return err },
		"Referrers":       func() error { _, err := tx.Referrers(content.RefEntry, entryID); return err },
		"LastNumber":      func() error { _, err := tx.LastNumber(); return err },
		"Operation":       func() error { _, _, err := tx.Operation("op_A"); return err },
		"Operations":      func() error { return tx.Operations(func(string, []byte) bool { return true }) },
		"PutOperation":    func() error { return tx.PutOperation("op_A", []byte("data")) },
		"DeleteOperation": func() error { return tx.DeleteOperation("op_A") },
		"RebuildIndex":    tx.RebuildIndex,
		"Account":         func() error { _, _, err := tx.Account(userID); return err },
		"Accounts":        func() error { _, err := tx.Accounts(); return err },
		"PutAccount":      func() error { return tx.PutAccount(userID, []byte("data")) },
		"DeleteAccount":   func() error { return tx.DeleteAccount(userID) },
		"Session":         func() error { _, _, err := tx.Session(sessionKeyOf("a")); return err },
		"Sessions":        func() error { _, err := tx.Sessions(); return err },
		"PutSession":      func() error { return tx.PutSession(sessionKeyOf("a"), []byte("data")) },
		"DeleteSession":   func() error { return tx.DeleteSession(sessionKeyOf("a")) },
	}
}

var writes = []string{
	"Put", "Delete", "AddRevision", "PutOperation", "DeleteOperation", "RebuildIndex", "PutAccount", "DeleteAccount",
	"PutSession", "DeleteSession",
}

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
