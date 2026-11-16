package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver/dbtest"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// The tests of the windows of the store: the round trips of Documents, Revisions, Check and ReadAhead, which are counted
// as the Write calls of the connections of the client (a request, and a window of reads, are one each), and that no read
// ahead changes a result or an error.

// writeCounter is the Dialer of a store of the harness that counts the Write calls of the connections that it makes, and
// can end the connection of one of them. It gives back to the harness what the store gave it when it is closed.
type writeCounter struct {
	dbclient.Dialer
	writes    atomic.Int64
	cutAt     atomic.Int64 // the number of the Write call that ends its connection and fails; 0: none
	readBytes atomic.Int64 // what the connections have read
	cutReadAt atomic.Int64 // the number of bytes that the connections read before one is ended; 0: none
}

func (w *writeCounter) Dial(ctx context.Context, role dbwire.Role) (net.Conn, error) {
	conn, err := w.Dialer.Dial(ctx, role)
	if err != nil {
		return nil, err
	}
	return &countedConn{Conn: conn, w: w}, nil
}

func (w *writeCounter) String() string { return w.Dialer.(fmt.Stringer).String() }

func (w *writeCounter) Close() error { return w.Dialer.(interface{ Close() error }).Close() }

// cut makes the n-th Write call from now end its connection.
func (w *writeCounter) cut(n int64) { w.cutAt.Store(w.writes.Load() + n) }

type countedConn struct {
	net.Conn
	w *writeCounter
}

func (c *countedConn) Read(p []byte) (int, error) {
	if at := c.w.cutReadAt.Load(); at != 0 {
		read := c.w.readBytes.Load()
		if read >= at {
			c.w.cutReadAt.CompareAndSwap(at, 0)
			_ = c.Close()
			return 0, io.ErrUnexpectedEOF
		}
		p = p[:min(int64(len(p)), at-read)]
	}
	n, err := c.Conn.Read(p)
	c.w.readBytes.Add(int64(n))
	return n, err
}

func (c *countedConn) Write(p []byte) (int, error) {
	n := c.w.writes.Add(1)
	if at := c.w.cutAt.Load(); at != 0 && n >= at && c.w.cutAt.CompareAndSwap(at, 0) {
		_ = c.Close()
		return 0, io.ErrClosedPipe
	}
	return c.Conn.Write(p)
}

// countedStore makes a store of the log backend, whose connections are counted by the writeCounter that it returns.
func countedStore(t *testing.T) (*Store, *writeCounter) {
	t.Helper()
	return countedStoreOf(t, oneBackend[0].options())
}

// countedStoreOf is countedStore with the options o.
func countedStoreOf(t *testing.T, o options) (*Store, *writeCounter) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := dbtest.Init(dir, o.daemonOptions(nil)); err != nil {
		t.Fatal(err)
	}
	w := new(writeCounter)
	s, err := start(dir, o, true, func(d dbclient.Dialer) (*Store, error) {
		w.Dialer = d
		return createOn(context.Background(), w, o.clock(), o.client())
	})
	if err != nil {
		t.Fatalf("createOn() = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, w
}

// The IDs of the sites of the tests of this file, which sort as i does.
func aheadEntry(i int) string { return fmt.Sprintf("ent_01JF%022d", i) }
func aheadTerm(i int) string  { return fmt.Sprintf("trm_01JF%022d", i) }

// aheadRevision is the ID of the revision k of the entry i: 0 is that which it publishes, 1 the draft that follows it.
func aheadRevision(k, i int) string { return fmt.Sprintf("rev_01JF%d%021d", k, i) }

// putAheadTerms puts the terms 0 to n-1 into s, a hundred in an Update.
func putAheadTerms(t *testing.T, s *Store, n int) {
	t.Helper()
	for from := 0; from < n; from += 100 {
		update(t, s, func(tx *Tx) error {
			for i := from; i < min(from+100, n); i++ {
				if _, err := tx.Put(content.RefTerm, aheadTerm(i), docJSON(content.RefTerm, aheadTerm(i)), userID); err != nil {
					return err
				}
			}
			return nil
		})
	}
}

// putAheadSite puts the published entries 0 to n-1 into s, each with the revision that it publishes and a draft after
// it, which refer to the term aheadTerm(0), a hundred entries in an Update.
func putAheadSite(t *testing.T, s *Store, n int) {
	t.Helper()
	for from := 0; from < n; from += 100 {
		update(t, s, func(tx *Tx) error {
			for i := from; i < min(from+100, n); i++ {
				id := aheadEntry(i)
				if _, err := tx.Put(content.RefEntry, id, publishedEntry(id, i+1, aheadRevision(0, i)), userID); err != nil {
					return err
				}
				for k, title := range []string{"published", "draft"} {
					data := revisionJSON(fmt.Sprintf(`"title": "%s %d", "slug": "post-%d", "terms": {"category": [%q]}`,
						title, i, i, aheadTerm(0)))
					if _, err := tx.AddRevision(id, aheadRevision(k, i), data, userID); err != nil {
						return err
					}
				}
			}
			return nil
		})
	}
}

// counted runs f and returns how many Write calls the store's connections made in it.
func counted(w *writeCounter, f func()) int {
	before := w.writes.Load()
	f()
	return int(w.writes.Load() - before)
}

// warm makes a connection for the Views of s, so that the count of what follows has no dial.
func warm(t *testing.T, s *Store) {
	t.Helper()
	view(t, s, func(tx *Tx) error {
		_, err := tx.LastNumber()
		return err
	})
}

// TestDocumentsRoundTrips checks that Documents reads the objects of 600 documents in a few windows, and returns the
// documents in the order of their IDs.
func TestDocumentsRoundTrips(t *testing.T) {
	s, w := countedStore(t)
	putAheadTerms(t, s, 600)
	warm(t, s)
	var docs []Document
	n := counted(w, func() {
		view(t, s, func(tx *Tx) (err error) {
			docs, err = tx.Documents(content.RefTerm)
			return err
		})
	})
	t.Logf("Documents of 600 documents: %d Write calls (the View's Snapshot and Release included)", n)
	if n > 10 {
		t.Errorf("Documents() of 600 documents made %d round trips, want at most 10", n)
	}
	if len(docs) != 600 {
		t.Fatalf("Documents() = %d documents, want 600", len(docs))
	}
	for i, d := range docs {
		if want := aheadTerm(i); d.ID != want || d.Kind != content.RefTerm || string(d.Data) != string(docJSON(content.RefTerm, want)) {
			t.Fatalf("document %d = %q %q %q, want %q %q", i, d.Kind, d.ID, d.Data, content.RefTerm, want)
		}
	}
	view(t, s, func(tx *Tx) error {
		for _, kind := range []content.RefKind{content.RefEntry, content.RefMedia} {
			if docs, err := tx.Documents(kind); err != nil || docs != nil {
				t.Errorf("Documents(%q) of a kind that has none = %v, %v; want nil, nil", kind, docs, err)
			}
		}
		return nil
	})
}

// TestRevisionsRoundTrips checks that Revisions reads the rows of the revisions of an entry with one window.
func TestRevisionsRoundTrips(t *testing.T) {
	s, w := countedStore(t)
	update(t, s, func(tx *Tx) error {
		if _, err := tx.Put(content.RefEntry, entryID, docJSON(content.RefEntry, entryID), userID); err != nil {
			return err
		}
		for i := range 100 {
			if _, err := tx.AddRevision(entryID, aheadRevision(0, i), revJSON(fmt.Sprintf("revision %d", i)), userID); err != nil {
				return err
			}
		}
		return nil
	})
	view(t, s, func(tx *Tx) error {
		if _, err := tx.LastNumber(); err != nil { // the snapshot
			return err
		}
		var revs []Revision
		var err error
		n := counted(w, func() { revs, err = tx.Revisions(entryID) })
		t.Logf("Revisions of an entry with 100 revisions: %d Write calls", n)
		switch {
		case err != nil || len(revs) != 100:
			t.Errorf("Revisions() = %d revisions, %v; want 100", len(revs), err)
		case n > 2:
			t.Errorf("Revisions() of an entry with 100 revisions made %d round trips, want at most 2: the scan and the window", n)
		}
		for i, r := range revs {
			if r.ID != aheadRevision(0, i) || r.Seq != int64(i+1) || r.Data != nil {
				t.Errorf("revision %d = %+v, want %s with seq %d and no data", i, r, aheadRevision(0, i), i+1)
				break
			}
		}
		if revs, err := tx.Revisions(pageID); revs != nil || err != nil {
			t.Errorf("Revisions() of an entry that has none = %v, %v; want nil, nil", revs, err)
		}
		return nil
	})
}

// TestCheckRoundTrips checks the round trips of Check of a store of 300 entries, each with a published revision and a
// later draft, and that it finds no problem.
func TestCheckRoundTrips(t *testing.T) {
	s, w := countedStore(t)
	putAheadSite(t, s, 300)
	var problems []Problem
	n := counted(w, func() { problems = findProblems(t, s) })
	t.Logf("Check of 300 entries: %d Write calls", n)
	wantProblems(t, "300 entries", problems)
	if n > 120 {
		t.Errorf("Check() of 300 entries made %d round trips, want at most 120", n)
	}
}

// aheadNames are the documents and the revisions that the tests read, with the names that nothing has.
type aheadNames struct {
	docs []DocumentID
	revs []string
}

func (n aheadNames) ahead() Ahead {
	return Ahead{Summaries: n.docs, Documents: n.docs, Revisions: n.revs}
}

// readLines reads the summary and the document of each name of n, and the revision of each of its revisions, and returns
// the results and the errors as lines.
func (n aheadNames) readLines(tx *Tx) []string {
	var lines []string
	line := func(call string, v any, err error) {
		lines = append(lines, fmt.Sprintf("%s = %s, %v (not exist %v)", call, show(v), err, errors.Is(err, fs.ErrNotExist)))
	}
	for _, d := range n.docs {
		sum, err := tx.Summary(d.Kind, d.ID)
		line(fmt.Sprintf("Summary(%q, %q)", d.Kind, d.ID), sum, err)
		doc, err := tx.Document(d.Kind, d.ID)
		line(fmt.Sprintf("Document(%q, %q)", d.Kind, d.ID), doc, err)
	}
	for _, id := range n.revs {
		rev, err := tx.Revision(id)
		line(fmt.Sprintf("Revision(%q)", id), rev, err)
	}
	return lines
}

// TestReadAheadRoundTrips checks that the calls that follow a ReadAhead ask the daemon nothing, and that the ReadAhead
// of 300 summaries, 300 documents and 300 revisions makes a few round trips.
func TestReadAheadRoundTrips(t *testing.T) {
	s, w := countedStore(t)
	putAheadSite(t, s, 300)
	var names aheadNames
	for i := range 300 {
		names.docs = append(names.docs, DocumentID{content.RefEntry, aheadEntry(i)})
		names.revs = append(names.revs, aheadRevision(0, i))
	}
	var without []string
	view(t, s, func(tx *Tx) error {
		without = names.readLines(tx)
		return nil
	})
	warm(t, s)
	view(t, s, func(tx *Tx) error {
		var err error
		ahead := counted(w, func() { err = tx.ReadAhead(names.ahead()) })
		if err != nil {
			t.Fatalf("ReadAhead() = %v", err)
		}
		var lines []string
		reads := counted(w, func() { lines = names.readLines(tx) })
		t.Logf("ReadAhead of 300 summaries, 300 documents and 300 revisions: %d Write calls (the snapshot included), then "+
			"300 calls of each of Summary, Document and Revision: %d", ahead, reads)
		if ahead > 12 {
			t.Errorf("ReadAhead() made %d round trips, want at most 12", ahead)
		}
		if reads != 0 {
			t.Errorf("Summary(), Document() and Revision() after ReadAhead() made %d round trips, want none", reads)
		}
		if !slices.Equal(lines, without) {
			t.Errorf("the results after ReadAhead() are not those without it")
		}
		return nil
	})
}

// TestReadAheadDrops checks that a ReadAhead drops the data that an earlier one has read and nobody has taken, so that the
// call that needs them asks the daemon again, and that those of the last one are taken without a request.
func TestReadAheadDrops(t *testing.T) {
	s, w := countedStore(t)
	putAheadSite(t, s, 3)
	view(t, s, func(tx *Tx) error {
		first, second := DocumentID{content.RefEntry, aheadEntry(0)}, DocumentID{content.RefEntry, aheadEntry(1)}
		for _, a := range []Ahead{{Documents: []DocumentID{first}}, {Documents: []DocumentID{second}}} {
			if err := tx.ReadAhead(a); err != nil {
				t.Fatalf("ReadAhead() = %v", err)
			}
		}
		document := func(d DocumentID) (n int) {
			return counted(w, func() {
				if _, err := tx.Document(d.Kind, d.ID); err != nil {
					t.Errorf("Document(%q) = %v", d.ID, err)
				}
			})
		}
		if n := document(second); n != 0 {
			t.Errorf("Document() of what the last ReadAhead() has read made %d round trips, want none", n)
		}
		if n := document(first); n != 1 {
			t.Errorf("Document() of what the first ReadAhead() has read, and the second has dropped, made %d round trips, want 1 (the data)", n)
		}
		return nil
	})
}

// readAheadSite is a store for the tests of ReadAhead: documents of every kind, one that is deleted, one that has a version
// whose object is missing, entries whose revisions are there, are not there, and are there after the entry is deleted,
// and names that nothing has.
type readAheadSite struct {
	s     *Store
	names aheadNames
}

// The documents and revisions of newReadAheadSite besides those of the IDs of the other tests.
const (
	siteDeleted = "trm_01JF0000000000000000000001" // deleted
	siteNoData  = "trm_01JF0000000000000000000002" // its version has no object
	siteLost    = "ent_01JF0000000000000000000003" // publishes a revision that does not exist, until the Update adds it
	siteGone    = "ent_01JF0000000000000000000004" // deleted, with a revision
	siteNew     = "trm_01JF0000000000000000009998" // does not exist, until the Update puts it
	siteNever   = "ent_01JF0000000000000000009999" // never exists
	siteNoRev   = "rev_01JF0000000000000000009999" // never exists, until the Update adds it
)

func newReadAheadSite(t *testing.T, tb testBackend) *readAheadSite {
	t.Helper()
	s, _ := newStore(t, tb)
	update(t, s, func(tx *Tx) error {
		for _, d := range []struct {
			kind content.RefKind
			id   string
			data []byte
		}{
			{SettingsKind, "", docJSON(SettingsKind, "")},
			{content.RefEntry, entryID, publishedEntry(entryID, 1, revisionID)},
			{content.RefEntry, pageID, entryJSON(pageID, `"number": 2, "status": "draft"`)},
			{content.RefEntry, siteLost, publishedEntry(siteLost, 3, siteNoRev)},
			{content.RefEntry, siteGone, entryJSON(siteGone, `"number": 4, "status": "draft"`)},
			{content.RefTerm, termID, docJSON(content.RefTerm, termID)},
			{content.RefTerm, siteDeleted, docJSON(content.RefTerm, siteDeleted)},
			{content.RefMedia, mediaID, docJSON(content.RefMedia, mediaID)},
			{content.RefAuthor, authorID, docJSON(content.RefAuthor, authorID)},
			{content.RefMenu, menuID, docJSON(content.RefMenu, menuID)},
			{content.RefForm, formID, docJSON(content.RefForm, formID)},
			{content.RefRedirect, redirectID, docJSON(content.RefRedirect, redirectID)},
		} {
			if _, err := tx.Put(d.kind, d.id, d.data, userID); err != nil {
				return err
			}
		}
		for _, r := range []struct{ entry, id, title string }{
			{entryID, revisionID, "About"}, {pageID, aheadRevision(0, 1), "Page"}, {siteGone, aheadRevision(0, 4), "Gone"},
		} {
			if _, err := tx.AddRevision(r.entry, r.id, revJSON(r.title), userID); err != nil {
				return err
			}
		}
		if _, err := tx.Delete(content.RefTerm, siteDeleted, userID); err != nil {
			return err
		}
		_, err := tx.Delete(content.RefEntry, siteGone, userID)
		return err
	})
	// A document whose current version has no object, as a store that lost one has.
	damage(t, s, func(b backendTx) error {
		seq, err := b.addVersion(content.RefTerm, siteNoData, sum([]byte("no such object")), testTime.UTC().Format(savedAtLayout), userID)
		if err != nil {
			return err
		}
		return b.setCurrent(content.RefTerm, siteNoData, seq)
	})
	site := &readAheadSite{s: s}
	site.names.docs = []DocumentID{
		{SettingsKind, ""}, {content.RefEntry, entryID}, {content.RefEntry, pageID}, {content.RefEntry, siteLost},
		{content.RefEntry, siteGone}, {content.RefTerm, termID}, {content.RefTerm, siteDeleted},
		{content.RefTerm, siteNoData}, {content.RefMedia, mediaID}, {content.RefAuthor, authorID}, {content.RefMenu, menuID},
		{content.RefForm, formID}, {content.RefRedirect, redirectID},
		// Names that nothing has, and names that are not valid.
		{content.RefTerm, siteNew}, {content.RefEntry, siteNever}, {content.RefKind("page"), "x"}, {content.RefEntry, ""},
		{SettingsKind, "x"},
	}
	site.names.revs = []string{revisionID, aheadRevision(0, 1), aheadRevision(0, 4), siteNoRev, "rev_x", ""}
	return site
}

// TestReadAheadChangesNothing checks that Summary, Document and Revision return the same results and errors after a
// ReadAhead of everything that they read, in a View and in an Update, also after the Update has put, deleted and added
// revisions, and for a store whose objects are changed.
func TestReadAheadChangesNothing(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		site := newReadAheadSite(t, tb)
		s, names := site.s, site.names
		t.Run("View", func(t *testing.T) {
			var without, with []string
			view(t, s, func(tx *Tx) error {
				without = names.readLines(tx)
				return nil
			})
			view(t, s, func(tx *Tx) error {
				if err := tx.ReadAhead(names.ahead()); err != nil {
					t.Fatalf("ReadAhead() = %v", err)
				}
				with = names.readLines(tx)
				return nil
			})
			if !slices.Equal(with, without) {
				t.Errorf("the reads after ReadAhead():\n%s\nthe reads without it:\n%s", strings.Join(with, "\n"), strings.Join(without, "\n"))
			}
			if len(with) != 2*len(names.docs)+len(names.revs) {
				t.Errorf("%d lines, want %d", len(with), 2*len(names.docs)+len(names.revs))
			}
		})
		t.Run("Update", func(t *testing.T) {
			// The Update puts, deletes and adds, then reads; it ends with errAbort, so that the two runs start alike.
			run := func(ahead bool) (lines []string) {
				err := s.Update(t.Context(), func(tx *Tx) error {
					if ahead {
						if err := tx.ReadAhead(names.ahead()); err != nil {
							return err
						}
					}
					lines = append(lines, "before the writes:")
					lines = append(lines, names.readLines(tx)...)
					for _, f := range []func() error{
						func() error {
							_, err := tx.Put(content.RefEntry, pageID, publishedEntry(pageID, 2, revisionID), userID)
							return err
						},
						func() error {
							_, err := tx.Put(content.RefEntry, entryID, entryJSON(entryID, `"number": 1, "status": "draft"`), userID)
							return err
						},
						func() error { _, err := tx.Delete(content.RefTerm, termID, userID); return err },
						func() error { _, err := tx.Delete(content.RefEntry, pageID, userID); return err },
						func() error {
							_, err := tx.Put(content.RefEntry, pageID, publishedEntry(pageID, 2, aheadRevision(0, 5)), userID)
							return err
						},
						func() error {
							_, err := tx.Put(content.RefTerm, siteNew, docJSON(content.RefTerm, siteNew), userID)
							return err
						},
						func() error {
							_, err := tx.AddRevision(pageID, aheadRevision(0, 5), revJSON("Added"), userID)
							return err
						},
						func() error { _, err := tx.AddRevision(entryID, siteNoRev, revJSON("Late"), userID); return err },
					} {
						lines = append(lines, fmt.Sprintf("a write = %v", f()))
					}
					lines = append(lines, "after the writes:")
					lines = append(lines, names.readLines(tx)...)
					return errAbort
				})
				if !errors.Is(err, errAbort) {
					t.Fatalf("Update() = %v, want errAbort", err)
				}
				return lines
			}
			without, with := run(false), run(true)
			if !slices.Equal(with, without) {
				for i := range max(len(with), len(without)) {
					if line(with, i) != line(without, i) {
						t.Errorf("line %d with ReadAhead():\n%s\nwithout it:\n%s", i, line(with, i), line(without, i))
						break
					}
				}
			}
		})
		t.Run("changed objects", func(t *testing.T) {
			corruptObjects(t, s)
			var without, with []string
			view(t, s, func(tx *Tx) error {
				without = names.readLines(tx)
				return nil
			})
			view(t, s, func(tx *Tx) error {
				if err := tx.ReadAhead(names.ahead()); err != nil {
					t.Fatalf("ReadAhead() of a store whose objects are changed = %v", err)
				}
				with = names.readLines(tx)
				return nil
			})
			if !slices.Equal(with, without) || !strings.Contains(strings.Join(with, "\n"), ": the data has the hash ") {
				t.Errorf("the reads after ReadAhead():\n%s\nthe reads without it:\n%s", strings.Join(with, "\n"), strings.Join(without, "\n"))
			}
		})
	})
}

// TestReadAheadEnded checks that ReadAhead fails after the end of the transaction as a read does, and in a View that has
// not read yet.
func TestReadAheadEnded(t *testing.T) {
	s, _ := countedStore(t)
	putAheadSite(t, s, 1)
	var kept []*Tx
	view(t, s, func(tx *Tx) error {
		kept = append(kept, tx)
		return nil
	})
	update(t, s, func(tx *Tx) error {
		kept = append(kept, tx)
		return nil
	})
	for i, tx := range kept {
		_, readErr := tx.Document(content.RefEntry, aheadEntry(0))
		err := tx.ReadAhead(Ahead{Documents: []DocumentID{{content.RefEntry, aheadEntry(0)}}})
		if err == nil || readErr == nil || err.Error() != readErr.Error() {
			t.Errorf("ReadAhead() after the end of transaction %d = %v, want the error of a read, %v", i, err, readErr)
		}
	}
	// The first call of a View takes its snapshot.
	view(t, s, func(tx *Tx) error {
		if err := tx.ReadAhead(Ahead{Documents: []DocumentID{{content.RefEntry, aheadEntry(0)}}}); err != nil {
			t.Errorf("ReadAhead() as the first call of a View = %v", err)
		}
		if err := tx.ReadAhead(Ahead{}); err != nil {
			t.Errorf("ReadAhead() of nothing = %v", err)
		}
		return nil
	})
}

// damageDocument makes the current document of kind with id one that Documents cannot read, with the data in the
// document table: a value that does not decode (broken), or a version whose object does not exist.
func damageDocument(t *testing.T, s *Store, id string, broken bool) {
	t.Helper()
	damage(t, s, func(b backendTx) error {
		if broken {
			return b.(*logTx).tx.Put(tableDocuments, docKey(content.RefTerm, id), []byte{0xff})
		}
		seq, err := b.addVersion(content.RefTerm, id, sum([]byte("no such object "+id)), testTime.UTC().Format(savedAtLayout), userID)
		if err != nil {
			return err
		}
		return b.setCurrent(content.RefTerm, id, seq)
	})
}

// TestDocumentsFirstProblem checks that Documents reports the first problem in the order of the IDs when a document
// whose object is missing, and a row that does not decode, are in one chunk of the objects that it reads at once, and in
// two chunks, in either order, and returns the documents before it.
func TestDocumentsFirstProblem(t *testing.T) {
	missing := func(i int) string {
		return fmt.Sprintf("documents %q: the object %s is missing", docKey(content.RefTerm, aheadTerm(i)),
			sum([]byte("no such object "+aheadTerm(i))))
	}
	broken := func(i int) string {
		return fmt.Sprintf("documents %q: invalid value", docKey(content.RefTerm, aheadTerm(i)))
	}
	for _, tt := range []struct {
		name         string
		missing, bad int // the documents: the missing object, and the row that does not decode
		first        int // of the problem that is reported, which is also the number of the documents before it
		want         string
	}{
		{"an object before a row, in one chunk", 5, 10, 5, missing(5)},
		{"a row before an object, in one chunk", 10, 5, 5, broken(5)},
		{"an object before a row, in two chunks", 5, 270, 5, missing(5)},
		{"a row before an object, in two chunks", 270, 5, 5, broken(5)},
		{"an object in the second chunk, then a row", 270, 290, 270, missing(270)},
		{"the last row of a chunk and the first of the next", 256, 255, 255, broken(255)},
		{"an object in the last place of a chunk", 255, 300, 255, missing(255)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := countedStore(t)
			putAheadTerms(t, s, 301)
			damageDocument(t, s, aheadTerm(tt.missing), false)
			damageDocument(t, s, aheadTerm(tt.bad), true)
			var docs []Document
			var err error
			if verr := s.b.view(t.Context(), func(b backendTx) error {
				docs, err = b.documents(content.RefTerm)
				return nil
			}); verr != nil {
				t.Fatal(verr)
			}
			if err == nil || err.Error() != tt.want || len(docs) != tt.first {
				t.Errorf("documents() = %d documents, %v; want %d documents and the error %q", len(docs), err, tt.first, tt.want)
			}
			for i, d := range docs {
				if d.ID != aheadTerm(i) {
					t.Fatalf("document %d is %s, want %s", i, d.ID, aheadTerm(i))
				}
			}
			err = s.View(t.Context(), func(tx *Tx) error {
				_, err := tx.Documents(content.RefTerm)
				return err
			})
			if err == nil || err.Error() != tt.want {
				t.Errorf("Documents() = %v, want the error %q", err, tt.want)
			}
		})
	}
}

// problemOf is the problem of the row key of table, a row that is missing or differs.
func problemOf(table indexTable, key []byte, detail string) Problem {
	return Problem{AreaIndex, rowName(table, key), detail}
}

// TestCheckChunks checks that Check of 300 entries, which it reads in two chunks, reports the problems of the rows in the
// order of the documents across the boundary of the chunks, stops where found stops it, and, for a document whose rows
// cannot be computed, compares the rows of the documents before it first.
func TestCheckChunks(t *testing.T) {
	const (
		missing = "the row is missing"
		differs = "the row differs from the one that the documents give"
	)
	s, _ := countedStore(t)
	putAheadSite(t, s, 300)
	summary := func(i int) []byte { return docKey(content.RefEntry, aheadEntry(i)) }
	extra := docKey(content.RefTerm, aheadTerm(77))
	published := refKey(aheadTerm(0), content.RefEntry, aheadEntry(100), RolePublished)
	damage(t, s, func(b backendTx) error {
		for _, i := range []int{3, 255, 256, 299} {
			if err := b.indexDelete(indexSummaries, summary(i)); err != nil {
				return err
			}
		}
		for _, f := range []func() error{
			func() error { return b.indexPut(indexSummaries, summary(260), []byte("junk")) },
			func() error { return b.indexDelete(indexReferences, published) },
			func() error { return b.indexPut(indexSummaries, extra, []byte("{}")) },
		} {
			if err := f(); err != nil {
				return err
			}
		}
		return nil
	})
	want := []Problem{
		problemOf(indexSummaries, summary(3), missing),
		problemOf(indexReferences, published, missing),
		problemOf(indexSummaries, summary(255), missing),
		problemOf(indexSummaries, summary(256), missing),
		problemOf(indexSummaries, summary(260), differs),
		problemOf(indexSummaries, summary(299), missing),
		{AreaIndex, rowName(indexSummaries, extra), "the row is not given by any document"},
	}
	wantProblems(t, "the damaged rows", findProblems(t, s), want...)
	for n := 1; n <= len(want); n++ {
		var got []Problem
		if err := s.Check(t.Context(), func(p Problem) bool {
			got = append(got, p)
			return len(got) < n
		}); err != nil {
			t.Fatalf("Check() that stops at the problem %d = %v", n, err)
		}
		wantProblems(t, fmt.Sprintf("a Check that stops at the problem %d", n), got, want[:n]...)
	}

	// A Check whose found ends its context stops at the next row, also in the same chunk, where the next problem is.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var seen []Problem
	err := s.Check(ctx, func(p Problem) bool {
		seen = append(seen, p)
		cancel()
		return true
	})
	if !errors.Is(err, context.Canceled) || len(seen) != 1 {
		t.Errorf("Check() that cancels its context at the first problem = %v after %d problems, want context.Canceled and 1", err, len(seen))
	}

	// An entry in the second chunk whose rows cannot be computed: the rows before it come first, the others are not seen.
	broken := aheadRevision(0, 5000)
	damage(t, s, func(b backendTx) error {
		return b.(*logTx).tx.Put(tableEntryRevisions, entryKey(aheadEntry(270), 999), []byte(broken))
	})
	cannot := fmt.Sprintf("the rows that the documents give cannot be computed: entryRevisions %q: the revision %s is missing",
		entryKey(aheadEntry(270), 999), broken)
	wantProblems(t, "an entry whose rows cannot be computed", findProblems(t, s),
		problemOf(indexSummaries, summary(3), missing), problemOf(indexReferences, published, missing),
		problemOf(indexSummaries, summary(255), missing), problemOf(indexSummaries, summary(256), missing),
		problemOf(indexSummaries, summary(260), differs), Problem{Area: AreaIndex, Detail: cannot})
}

// TestCheckChangedObjectInAChunk checks that Check of a store in which a document has other data than its hash compares
// the rows of the documents before it in its chunk first, as it did when it made the rows one by one.
func TestCheckChangedObjectInAChunk(t *testing.T) {
	s, _ := countedStore(t)
	put := func(from, to int) {
		update(t, s, func(tx *Tx) error {
			for i := from; i < to; i++ {
				if _, err := tx.Put(content.RefTerm, aheadTerm(i), docJSON(content.RefTerm, aheadTerm(i)), userID); err != nil {
					return err
				}
			}
			return nil
		})
	}
	// The objects of the terms 10 to 19 are changed; those of the terms 0 to 9, which are put after that, are not.
	put(10, 20)
	corrupt := corruptObjects(t, s)
	put(0, 10)
	missing := docKey(content.RefTerm, aheadTerm(3))
	damage(t, s, func(b backendTx) error { return b.indexDelete(indexSummaries, missing) })
	changed := docJSON(content.RefTerm, aheadTerm(10))
	var got []Problem
	for _, p := range findProblems(t, s) {
		if p.Area == AreaIndex {
			got = append(got, p)
		}
	}
	wantProblems(t, "a changed object after a row that is missing, in one chunk", got,
		problemOf(indexSummaries, missing, "the row is missing"),
		Problem{Area: AreaIndex, Detail: "the rows that the documents give cannot be computed: object " + sum(changed) +
			": the data has the hash " + sum(corrupt(changed))})
}

// TestRebuildIndexChunks checks that RebuildIndex of 300 entries, which it reads in two chunks, writes the rows that are
// wrong on either side of the boundary of the chunks, and nothing for an index that is right.
func TestRebuildIndexChunks(t *testing.T) {
	s, c := countedStore(t)
	putAheadSite(t, s, 300)
	var w []string
	n := counted(c, func() { w = rebuildWrites(t, s) })
	t.Logf("RebuildIndex of 300 entries: %d Write calls", n)
	if len(w) != 0 {
		t.Fatalf("RebuildIndex() of an index that is right wrote %q, want nothing", w)
	}
	if n > 120 {
		t.Errorf("RebuildIndex() of 300 entries made %d round trips, want at most 120", n)
	}
	summary := func(i int) []byte { return docKey(content.RefEntry, aheadEntry(i)) }
	extra := docKey(content.RefTerm, aheadTerm(77))
	published := refKey(aheadTerm(0), content.RefEntry, aheadEntry(100), RolePublished)
	extraRef := refKey(aheadTerm(1), content.RefEntry, aheadEntry(257), RoleDocument)
	damage(t, s, func(b backendTx) error {
		for _, i := range []int{3, 255, 256, 299} {
			if err := b.indexDelete(indexSummaries, summary(i)); err != nil {
				return err
			}
		}
		for _, f := range []func() error{
			func() error { return b.indexPut(indexSummaries, summary(260), []byte("junk")) },
			func() error { return b.indexDelete(indexReferences, published) },
			func() error { return b.indexPut(indexSummaries, extra, []byte("{}")) },
			func() error { return b.indexPut(indexReferences, extraRef, nil) },
		} {
			if err := f(); err != nil {
				return err
			}
		}
		return nil
	})
	want := []string{
		indexWrite("delete", indexReferences, extraRef), indexWrite("delete", indexSummaries, extra),
		indexWrite("put", indexReferences, published),
	}
	for _, i := range []int{3, 255, 256, 260, 299} {
		want = append(want, indexWrite("put", indexSummaries, summary(i)))
	}
	slices.Sort(want)
	if got := rebuildWrites(t, s); !slices.Equal(got, want) {
		t.Errorf("RebuildIndex() wrote %q, want %q", got, want)
	}
	checkConsistent(t, s)
	wantProblems(t, "the rebuilt index", findProblems(t, s))
}

// TestDocumentsConnectionEndsInAWindow checks that Documents of objects that are all changed, when the connection ends
// while the replies of the window of their objects are read, reports the first of them, which was read, as it does without
// the window; and that it reports the end of the connection when nothing is changed.
func TestDocumentsConnectionEndsInAWindow(t *testing.T) {
	s, w := countedStore(t)
	putAheadTerms(t, s, 300)
	documents := func() (err error) {
		return s.View(t.Context(), func(tx *Tx) error {
			_, err := tx.Documents(content.RefTerm)
			return err
		})
	}
	// bytes returns what the connections read in f.
	bytes := func(f func()) int64 {
		before := w.readBytes.Load()
		f()
		return w.readBytes.Load() - before
	}
	warm(t, s)
	scanned := bytes(func() {
		view(t, s, func(tx *Tx) error {
			return tx.b.(*logTx).tx.Scan(tableDocuments, docKey(content.RefTerm, ""), func(_, _ []byte) bool { return true })
		})
	})
	total := bytes(func() {
		if err := documents(); err != nil {
			t.Fatalf("Documents() = %v", err)
		}
	})
	// A quarter of the way through the replies to the window of the first 256 objects.
	cutIn := func() error {
		w.cutReadAt.Store(w.readBytes.Load() + scanned + (total-scanned)/4)
		defer w.cutReadAt.Store(0)
		return documents()
	}
	if err := cutIn(); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Documents() when the connection ends while the objects are read = %v, want an error that is ErrUnavailable", err)
	}
	if err := documents(); err != nil {
		t.Errorf("Documents() after the connection ended = %v, want it to work on a new connection", err)
	}
	corrupt := corruptObjects(t, s)
	want := "object " + sum(docJSON(content.RefTerm, aheadTerm(0))) + ": the data has the hash " +
		sum(corrupt(docJSON(content.RefTerm, aheadTerm(0))))
	if err := documents(); err == nil || err.Error() != want {
		t.Fatalf("Documents() of changed objects = %v, want %q", err, want)
	}
	if err := cutIn(); err == nil || err.Error() != want {
		t.Errorf("Documents() of changed objects when the connection ends while the objects are read = %v, want the first that "+
			"was read, %q", err, want)
	}
}

// TestReadAheadBeyondTheBudget checks that two documents together over the 64 MiB that a transaction keeps of the data that
// it reads ahead are read as they are without it: what the client does not keep, the call reads.
func TestReadAheadBeyondTheBudget(t *testing.T) {
	if raceEnabled {
		t.Skip("the two documents of 33 MiB take 9 seconds with -race; the bound has its own tests in internal/dbclient")
	}
	s, _ := countedStore(t)
	ids := []string{aheadTerm(1), aheadTerm(2)}
	data := map[string][]byte{}
	for _, id := range ids {
		data[id] = withField(diffKinds[1], id, strings.Repeat(id[len(id)-1:], 33<<20))
	}
	update(t, s, func(tx *Tx) error {
		for _, id := range ids {
			if _, err := tx.Put(content.RefTerm, id, data[id], userID); err != nil {
				return err
			}
		}
		return nil
	})
	ahead := Ahead{Documents: []DocumentID{{content.RefTerm, ids[0]}, {content.RefTerm, ids[1]}}}
	view(t, s, func(tx *Tx) error {
		if err := tx.ReadAhead(ahead); err != nil {
			t.Fatalf("ReadAhead() = %v", err)
		}
		for _, id := range ids {
			if d, err := tx.Document(content.RefTerm, id); err != nil || !bytes.Equal(d.Data, data[id]) {
				t.Errorf("Document(%q) = %d bytes, %v; want %d bytes", id, len(d.Data), err, len(data[id]))
			}
		}
		if err := tx.ReadAhead(ahead); err != nil {
			t.Fatalf("ReadAhead() = %v", err)
		}
		docs, err := tx.Documents(content.RefTerm)
		if err != nil || len(docs) != 2 {
			t.Fatalf("Documents() = %d documents, %v; want 2", len(docs), err)
		}
		for i, d := range docs {
			if d.ID != ids[i] || !bytes.Equal(d.Data, data[ids[i]]) {
				t.Errorf("document %d = %q with %d bytes, want %q with %d", i, d.ID, len(d.Data), ids[i], len(data[ids[i]]))
			}
		}
		return nil
	})
}

// TestChunkDropsWhatItKept checks that what the read ahead of a chunk of entries keeps, which includes the first page of
// the revisions of each, is dropped when the rows of the chunk are made: the scan of the revisions of an entry then asks
// the daemon again, and asks nothing while the chunk is made.
func TestChunkDropsWhatItKept(t *testing.T) {
	s, w := countedStore(t)
	putAheadSite(t, s, 3)
	view(t, s, func(tx *Tx) error {
		called := false
		_, err := tx.computeIndexRows(func(rows []indexRow) error {
			if called || len(rows) == 0 || rows[0].table != indexSummaries {
				return nil
			}
			called = true
			var revs []Revision
			var err error
			if n := counted(w, func() { revs, err = tx.Revisions(aheadEntry(0)) }); err != nil || len(revs) != 2 || n != 1 {
				t.Errorf("Revisions() once the rows of the chunk are made = %d revisions, %v, %d round trips; want 2, nil, 1",
					len(revs), err, n)
			}
			return nil
		})
		if err != nil || !called {
			t.Errorf("computeIndexRows() = %v, emit called: %v; want nil and true", err, called)
		}
		return nil
	})
}

// TestAheadConnectionEnds checks that Documents, Check and ReadAhead of a store whose connection ends in the middle of a
// window or between two return an error that is ErrUnavailable (Check: and no problem), and that the next call works on a
// new connection.
func TestAheadConnectionEnds(t *testing.T) {
	s, w := countedStore(t)
	putAheadSite(t, s, 20)
	putAheadTerms(t, s, 20)
	var names aheadNames
	for i := range 20 {
		names.docs = append(names.docs, DocumentID{content.RefEntry, aheadEntry(i)}, DocumentID{content.RefTerm, aheadTerm(i)})
		names.revs = append(names.revs, aheadRevision(0, i), aheadRevision(1, i))
	}
	ops := []struct {
		name string
		do   func() error
	}{
		{"Documents", func() error {
			return s.View(t.Context(), func(tx *Tx) error {
				docs, err := tx.Documents(content.RefEntry)
				if err == nil && len(docs) != 20 {
					t.Errorf("Documents() = %d documents, want 20", len(docs))
				}
				return err
			})
		}},
		{"ReadAhead", func() error {
			return s.View(t.Context(), func(tx *Tx) error { return tx.ReadAhead(names.ahead()) })
		}},
	}
	for _, c := range ops {
		warm(t, s)
		total := counted(w, func() {
			if err := c.do(); err != nil {
				t.Fatalf("%s() = %v", c.name, err)
			}
		})
		failed := 0
		for n := 1; n <= total+1; n++ {
			w.cut(int64(n))
			err := c.do()
			w.cutAt.Store(0)
			switch {
			case err == nil:
			case !errors.Is(err, ErrUnavailable) || errors.Is(err, fs.ErrNotExist):
				t.Errorf("%s() with the connection ended at the Write call %d = %v, want an error that is ErrUnavailable", c.name, n, err)
			default:
				failed++
			}
			if err := c.do(); err != nil {
				t.Errorf("%s() after the connection ended at the Write call %d = %v, want it to work on a new connection", c.name, n, err)
			}
		}
		if failed < total-2 { // the first write is retried on a new connection, and a Release that fails is not an error
			t.Errorf("%s() failed %d times when the connection ended at each of its %d Write calls, want almost all", c.name, failed, total)
		}
	}

	// Check cannot be made when the connection ends: that is its error, and no problem of the store, which has none. The
	// media are for its last area, the originals.
	for i := range 3 {
		data, id := fmt.Appendf(nil, "an original %d", i), fmt.Sprintf("med_01JH%022d", i)
		putOriginal(t, s, data)
		update(t, s, putDoc(content.RefMedia, id, mediaJSON(id, data)))
	}
	total := counted(w, func() { wantProblems(t, "a store that is right", findProblems(t, s)) })
	failed := 0
	for n := 1; n <= total+1; n++ {
		w.cut(int64(n))
		var got []Problem
		err := s.Check(t.Context(), func(p Problem) bool {
			got = append(got, p)
			return true
		})
		w.cutAt.Store(0)
		switch {
		case err == nil:
		case !errors.Is(err, ErrUnavailable) || errors.Is(err, fs.ErrNotExist):
			t.Errorf("Check() with the connection ended at the Write call %d = %v, want an error that is ErrUnavailable", n, err)
		default:
			failed++
		}
		if len(got) != 0 {
			t.Errorf("Check() with the connection ended at the Write call %d found %+v, want no problem: the store has none", n, got)
		}
		wantProblems(t, fmt.Sprintf("the Check after the connection ended at the Write call %d", n), findProblems(t, s))
	}
	if failed < total/2 { // the first write of a connection that was kept is tried again on a new one
		t.Errorf("Check() failed %d times when the connection ended at each of its %d Write calls, want most", failed, total)
	}

	// The connection ends in the middle of an original: the request for it was answered, so it is not sent again.
	big, bigID := make([]byte, 512<<10), "med_01JH0000000000000000000009"
	putOriginal(t, s, big)
	update(t, s, putDoc(content.RefMedia, bigID, mediaJSON(bigID, big)))
	before := w.readBytes.Load()
	wantProblems(t, "a store that is right", findProblems(t, s))
	read := w.readBytes.Load() - before
	for _, back := range []int64{64 << 10, 256 << 10} {
		w.cutReadAt.Store(w.readBytes.Load() + read - back)
		var got []Problem
		err := s.Check(t.Context(), func(p Problem) bool {
			got = append(got, p)
			return true
		})
		w.cutReadAt.Store(0)
		if !errors.Is(err, ErrUnavailable) || len(got) != 0 {
			t.Errorf("Check() with the connection ended %d bytes before the end of its reads = %v, with the problems %+v; want an "+
				"error that is ErrUnavailable and no problem", back, err, got)
		}
		wantProblems(t, "the Check after the connection ended in an original", findProblems(t, s))
	}
}

// TestScanConnectionEnds ends the connection of a View at each of the Write calls of a read that scans a table, whose rows
// come in more than one page, and checks that the read is either whole or an error that is ErrUnavailable: a scan that
// the end of the connection has cut is no list that ends early.
func TestScanConnectionEnds(t *testing.T) {
	o := oneBackend[0].options()
	o.limits = &dbwire.Limits{Frame: 4096, CommitsPerSecond: -1, MinFree: -1}
	s, w := countedStoreOf(t, o)
	putAheadSite(t, s, 60)
	const revisions = 150
	update(t, s, func(tx *Tx) error {
		for i := range revisions {
			id := fmt.Sprintf("rev_01JG%022d", i)
			if _, err := tx.AddRevision(aheadEntry(0), id, revJSON(fmt.Sprintf("a revision %d", i)), userID); err != nil {
				return err
			}
		}
		return nil
	})
	update(t, s, func(tx *Tx) error {
		for i := range 40 {
			if err := tx.PutOperation(fmt.Sprintf("operation-%03d", i), bytes.Repeat([]byte("o"), 200)); err != nil {
				return err
			}
			if err := tx.PutAccount(fmt.Sprintf("usr_01JF%022d", i), bytes.Repeat([]byte("a"), 400)); err != nil {
				return err
			}
			if err := tx.PutSession(fmt.Sprintf("%064x", i), bytes.Repeat([]byte("s"), 150)); err != nil {
				return err
			}
		}
		return nil
	})

	// Each read returns what it read as a value to compare. whole says that a read that fails has called its function for
	// the rows that came before.
	ops := []struct {
		name  string
		pages int // the least number of pages of the scan
		read  func(tx *Tx) (any, error)
	}{
		{"Revisions", 3, func(tx *Tx) (any, error) { return tx.Revisions(aheadEntry(0)) }},
		{"Summaries", 2, func(tx *Tx) (any, error) { return tx.Summaries(content.RefEntry) }},
		{"Referrers", 2, func(tx *Tx) (any, error) { return tx.Referrers(content.RefTerm, aheadTerm(0)) }},
		{"Operations", 2, func(tx *Tx) (any, error) {
			var ids []string
			err := tx.Operations(func(id string, _ []byte) bool { ids = append(ids, id); return true })
			return ids, err
		}},
		{"Accounts", 2, func(tx *Tx) (any, error) { return tx.Accounts() }},
		{"Sessions", 2, func(tx *Tx) (any, error) { return tx.Sessions() }},
	}
	for _, c := range ops {
		do := func() (got any, err error) {
			err = s.View(t.Context(), func(tx *Tx) error {
				var err error
				got, err = c.read(tx)
				return err
			})
			return got, err
		}
		warm(t, s)
		var want any
		total := counted(w, func() {
			var err error
			if want, err = do(); err != nil {
				t.Fatalf("%s() = %v", c.name, err)
			}
		})
		if n := reflect.ValueOf(want).Len(); n < c.pages*10 || total < c.pages+1 {
			t.Fatalf("%s() has %d rows and makes %d Write calls, want the pages of %d", c.name, n, total, c.pages)
		}
		failed := 0
		for n := 1; n <= total+1; n++ {
			w.cut(int64(n))
			got, err := do()
			w.cutAt.Store(0)
			switch {
			case err == nil:
				if !reflect.DeepEqual(got, want) {
					t.Errorf("%s() with the connection ended at the Write call %d = %d rows and no error, want all %d", c.name, n,
						reflect.ValueOf(got).Len(), reflect.ValueOf(want).Len())
				}
			case !errors.Is(err, ErrUnavailable) || errors.Is(err, fs.ErrNotExist):
				t.Errorf("%s() with the connection ended at the Write call %d = %v, want an error that is ErrUnavailable", c.name, n, err)
			default:
				failed++
				if c.name != "Operations" && !reflect.ValueOf(got).IsZero() {
					t.Errorf("%s() with the connection ended at the Write call %d = %d rows and the error %v, want no rows", c.name,
						n, reflect.ValueOf(got).Len(), err)
				}
			}
			if _, err := do(); err != nil { // a connection for the next one
				t.Errorf("%s() after the connection ended at the Write call %d = %v, want it to work on a new connection", c.name, n, err)
			}
		}
		if failed < total-2 { // the first write is retried on a new connection, and a Release that fails is not an error
			t.Errorf("%s() failed %d times when the connection ended at each of its %d Write calls, want almost all", c.name, failed, total)
		}
	}
}
