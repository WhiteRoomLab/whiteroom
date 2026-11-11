package store

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// lastCommit returns the number of the last commit of the engine of s, and false for the model, which has none.
func lastCommit(t *testing.T, s *Store) (uint64, bool) {
	t.Helper()
	b, ok := s.b.(*logBackend)
	if !ok {
		return 0, false
	}
	var seq uint64
	if err := b.db.View(t.Context(), func(tx *logdb.Tx) error {
		seq = tx.Seq()
		return nil
	}); err != nil {
		t.Fatalf("View() of the engine = %v", err)
	}
	return seq, true
}

// reachedStore is what the tests of ErrUnrecorded read: every kind of row that the writes of calls make.
var reachedStore = diffReads{
	kinds:      []content.RefKind{content.RefEntry},
	docs:       []diffDoc{{diffKind{kind: content.RefEntry}, entryID}},
	revs:       []string{revisionID},
	entries:    []string{entryID},
	hashes:     []string{sum(docJSON(content.RefEntry, entryID)), sum(revJSON("About"))},
	operations: []string{"op_A"},
	accounts:   []string{userID},
	sessions:   []string{sessionKeyOf("a")},
}

// storeState returns the text of the reads of reachedStore, and the heads of the audit logs.
func storeState(t *testing.T, s *Store) string {
	t.Helper()
	var text string
	view(t, s, func(tx *Tx) error {
		text = readsText(tx, reachedStore)
		return nil
	})
	heads, err := s.AuditHeads(t.Context())
	if err != nil {
		t.Fatalf("AuditHeads() = %v", err)
	}
	return fmt.Sprintf("%s%v", text, heads)
}

// TestTxMethodsAreCalled checks that calls has a call of every method of Tx but Record and Changed, so that a method
// that is added later is in the tests that go through calls, among them TestUnrecordedWrite for the writes.
func TestTxMethodsAreCalled(t *testing.T) {
	all := calls(new(Tx))
	typ := reflect.TypeFor[*Tx]()
	for i := range typ.NumMethod() {
		name := typ.Method(i).Name
		if _, ok := all[name]; !ok && name != "Record" && name != "Changed" {
			t.Errorf("calls has no call of the method %s of Tx: add it, also to writes if it writes", name)
		}
	}
	for _, name := range writes {
		if _, ok := all[name]; !ok {
			t.Errorf("writes names %s, which calls does not have", name)
		}
	}
}

// TestUnrecordedWrite checks, for each write of the interface of the store, that an Update whose function makes it and
// adds no record fails with ErrUnrecorded and commits nothing, and that one that adds a record commits both.
func TestUnrecordedWrite(t *testing.T) {
	data := docJSON(content.RefEntry, entryID)
	putEntry := func(tx *Tx) error {
		_, err := tx.Put(content.RefEntry, entryID, data, userID)
		return err
	}
	setups := map[string]func(tx *Tx) error{
		"Delete":      putEntry,
		"AddRevision": putEntry,
		// An index that lacks the summary of the entry, which RebuildIndex writes again.
		"RebuildIndex": func(tx *Tx) error {
			if err := putEntry(tx); err != nil {
				return err
			}
			return tx.b.indexDelete(indexSummaries, docKey(content.RefEntry, entryID))
		},
		"DeleteAccount": func(tx *Tx) error { return tx.PutAccount(userID, []byte("row")) },
		"DeleteSession": func(tx *Tx) error { return tx.PutSession(sessionKeyOf("a"), []byte("session")) },
	}
	for _, name := range writes {
		t.Run(name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, s *Store) {
				if setup := setups[name]; setup != nil {
					update(t, s, setup)
				}
				write := func(tx *Tx) error { return calls(tx)[name]() }
				before := storeState(t, s)
				seq, hasSeq := lastCommit(t, s)

				// The Update is refused before the point of its commit, where the hook of the tests is called.
				errHook := errors.New("hook")
				called := failAt(s, "commit", errHook)
				err := s.Update(t.Context(), write)
				s.hook = nil
				if !errors.Is(err, ErrUnrecorded) || errors.Is(err, errHook) {
					t.Fatalf("Update() with %s and no record = %v, want ErrUnrecorded", name, err)
				}
				if got := called(); len(got) != 0 {
					t.Errorf("the hook has been called at %q in the Update that is refused, want no call", got)
				}
				if after := storeState(t, s); after != before {
					t.Errorf("the store has changed in the Update that is refused:\nbefore:\n%s\nafter:\n%s", before, after)
				}
				if got, _ := lastCommit(t, s); hasSeq && got != seq {
					t.Errorf("the last commit is %d after the Update that is refused, want %d", got, seq)
				}

				// With a record, the write and the record are one commit.
				if err := s.Update(t.Context(), func(tx *Tx) error {
					if err := write(tx); err != nil {
						return err
					}
					return tx.Record(eventRecord(name))
				}); err != nil {
					t.Fatalf("Update() with %s and a record = %v", name, err)
				}
				if after := storeState(t, s); after == before {
					t.Errorf("the store has not changed in the Update with a record")
				}
				if got, _ := lastCommit(t, s); hasSeq && got != seq+1 {
					t.Errorf("the last commit is %d after the Update with a record, want %d", got, seq+1)
				}
				records := allRecords(t, s)
				if last := records[len(records)-1]; last.Action != audit.StoreWrite || last.Detail["event"] != name {
					t.Errorf("the last record is %+v, want the one that the Update added", last)
				}
				if err := s.VerifyAudit(t.Context(), nil); err != nil {
					t.Errorf("VerifyAudit() = %v", err)
				}
			})
		})
	}
}

// TestUnrecordedRules checks what an Update needs and what it does not: a record, if it has written anything but
// records (Changed), and none if it has written nothing, or only records.
func TestUnrecordedRules(t *testing.T) {
	put := func(tx *Tx) error {
		_, err := tx.Put(content.RefEntry, entryID, docJSON(content.RefEntry, entryID), otherUser)
		return err
	}
	errTest := errors.New("test")
	for _, tt := range []struct {
		name    string
		f       func(tx *Tx) error
		err     error // what errors.Is finds in the error of Update; nil for none
		commits bool
		records int // the records that the Update adds
	}{
		{"nothing", func(tx *Tx) error { return nil }, nil, false, 0},
		{"reads", func(tx *Tx) error {
			_, err := tx.Document(content.RefEntry, pageID)
			return err
		}, nil, false, 0},
		{"a record", func(tx *Tx) error { return tx.Record(eventRecord("a")) }, nil, true, 1},
		{"records of both logs", func(tx *Tx) error {
			return errors.Join(tx.Record(eventRecord("a")), tx.Record(loginRecord(audit.Login, "a")))
		}, nil, true, 2},
		{"a write and a record", func(tx *Tx) error {
			return errors.Join(put(tx), tx.Record(eventRecord("a")))
		}, nil, true, 1},
		{"a write and a record of the login history", func(tx *Tx) error {
			return errors.Join(put(tx), tx.Record(loginRecord(audit.Logout, "a")))
		}, nil, true, 1},
		{"a record and a write", func(tx *Tx) error {
			return errors.Join(tx.Record(eventRecord("a")), tx.PutOperation("op_A", []byte("x")))
		}, nil, true, 1},
		{"a write, a record and a write", func(tx *Tx) error {
			return errors.Join(tx.PutOperation("op_A", []byte("x")), tx.Record(eventRecord("a")),
				tx.PutAccount(userID, []byte("row")))
		}, nil, true, 1},
		{"a write alone", put, ErrUnrecorded, false, 0},
		{"a Put of the data that the document has", func(tx *Tx) error {
			_, err := tx.Put(content.RefEntry, pageID, docJSON(content.RefEntry, pageID), otherUser)
			return err
		}, nil, false, 0},
		{"a RebuildIndex of an index that is right", func(tx *Tx) error { return tx.RebuildIndex() }, nil, false, 0},
		{"writes that fail, which f does not return", func(tx *Tx) error {
			_, _ = tx.Put(content.RefEntry, "ent_x", docJSON(content.RefEntry, entryID), userID)
			_, _ = tx.Delete(content.RefTerm, termID, userID)
			_ = tx.DeleteSession(sessionKeyOf("z"))
			return nil
		}, nil, false, 0},
		{"a write and a record that is refused, which f does not return", func(tx *Tx) error {
			bad := eventRecord("bad")
			bad.Actor = "alice"
			_ = tx.Record(bad)
			return put(tx)
		}, ErrUnrecorded, false, 0},
		{"a write and a record, and then an error", func(tx *Tx) error {
			return errors.Join(put(tx), tx.Record(eventRecord("a")), errTest)
		}, errTest, false, 0},
		{"a write and then an error", func(tx *Tx) error {
			return errors.Join(put(tx), errTest)
		}, errTest, false, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, s *Store) {
				// The page is there, for the Put of the data that it has.
				update(t, s, func(tx *Tx) error {
					_, err := tx.Put(content.RefEntry, pageID, docJSON(content.RefEntry, pageID), userID)
					return err
				})
				before := len(allRecords(t, s))
				seq, hasSeq := lastCommit(t, s)
				err := s.Update(t.Context(), tt.f)
				switch {
				case tt.err == nil && err != nil:
					t.Fatalf("Update() = %v, want nil", err)
				case tt.err != nil && !errors.Is(err, tt.err):
					t.Fatalf("Update() = %v, want an error that wraps %v", err, tt.err)
				case errors.Is(tt.err, ErrUnrecorded) && err.Error() != "the transaction writes but adds no audit record":
					t.Errorf("Update() = %q, want the text of ErrUnrecorded", err)
				}
				if got := len(allRecords(t, s)) - before; got != tt.records {
					t.Errorf("the Update has added %d records, want %d", got, tt.records)
				}
				if got, _ := lastCommit(t, s); hasSeq && (got != seq) != tt.commits {
					t.Errorf("the last commit went from %d to %d, want a commit: %v", seq, got, tt.commits)
				}
				if err := s.VerifyAudit(t.Context(), nil); err != nil {
					t.Errorf("VerifyAudit() = %v", err)
				}
			})
		})
	}
}

// TestUnrecordedLastNumber checks that a RebuildIndex that writes only the last number, the rest of the index being
// right, is a write that needs a record.
func TestUnrecordedLastNumber(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		update(t, s, func(tx *Tx) error {
			if _, err := tx.Put(content.RefEntry, entryID, entryJSON(entryID, `"number": 3`), userID); err != nil {
				return err
			}
			return tx.b.setLastNumber(0)
		})
		last := func() int {
			var n int
			view(t, s, func(tx *Tx) (err error) {
				n, err = tx.LastNumber()
				return err
			})
			return n
		}
		if got := last(); got != 0 {
			t.Fatalf("LastNumber() = %d before the rebuild, want 0", got)
		}
		err := s.Update(t.Context(), func(tx *Tx) error { return tx.RebuildIndex() })
		if !errors.Is(err, ErrUnrecorded) || last() != 0 {
			t.Fatalf("Update() with RebuildIndex = %v, LastNumber() = %d; want ErrUnrecorded and 0", err, last())
		}
		update(t, s, func(tx *Tx) error { return tx.RebuildIndex() })
		if got := last(); got != 3 {
			t.Errorf("LastNumber() after the rebuild = %d, want 3", got)
		}
	})
}

// TestUnrecordedLeavesNoTrace checks that an Update that is refused leaves nothing behind: the next one gets the seqs
// and the record numbers that it would have got without it.
func TestUnrecordedLeavesNoTrace(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		before := len(allRecords(t, s))
		err := s.Update(t.Context(), func(tx *Tx) error {
			if _, err := tx.Put(content.RefEntry, entryID, docJSON(content.RefEntry, entryID), userID); err != nil {
				return err
			}
			_, err := tx.AddRevision(entryID, revisionID, revJSON("About"), userID)
			return err
		})
		if !errors.Is(err, ErrUnrecorded) {
			t.Fatalf("Update() = %v, want ErrUnrecorded", err)
		}
		var v Version
		var r Revision
		update(t, s, func(tx *Tx) error {
			var err error
			if v, err = tx.Put(content.RefEntry, entryID, docJSON(content.RefEntry, entryID), userID); err != nil {
				return err
			}
			r, err = tx.AddRevision(entryID, revisionID, revJSON("About"), userID)
			return err
		})
		if v.Seq != 1 || r.Seq != 1 {
			t.Errorf("the seqs of the version and of the revision are %d and %d, want 1 and 1", v.Seq, r.Seq)
		}
		got, want := recordLines(allRecords(t, s))[before:], []string{fmt.Sprintf("activity %d store.write ", before+1)}
		if !slices.Equal(got, want) {
			t.Errorf("the records of the Updates = %q, want %q", got, want)
		}
	})
}
