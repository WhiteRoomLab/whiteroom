package store

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// createRecordJSON is the record store.create of a store that was made at testTime.
const createRecordJSON = `{"log":"activity","n":1,"at":"2026-11-03T08:00:00.12Z","actor":"system",` +
	`"action":"store.create"}`

func createRecordSum() []byte {
	sum := sha256.Sum256([]byte(createRecordJSON))
	return sum[:]
}

// rawEvents returns the audit events of the engine of s as it keeps them.
func rawEvents(t *testing.T, s *Store) []logdb.Event {
	t.Helper()
	var got []logdb.Event
	if err := logDB(t, s).Events(t.Context(), func(e logdb.Event) bool {
		got = append(got, e)
		return true
	}); err != nil {
		t.Fatalf("Events() of the engine = %v", err)
	}
	return got
}

func headOf(t *testing.T, s *Store, log audit.Log) (audit.Head, bool) {
	t.Helper()
	heads, err := s.AuditHeads(t.Context())
	if err != nil {
		t.Fatalf("AuditHeads() = %v", err)
	}
	for _, h := range heads {
		if h.Log == log {
			return h, true
		}
	}
	return audit.Head{}, false
}

// loginRecord returns a record of the login history.
func loginRecord(action audit.Action, login string) audit.Record {
	return audit.Record{Log: audit.LoginLog, Action: action, Login: login,
		Client: audit.Client{Addr: "192.0.2.1", UserAgent: "test"}}
}

// closeAndRewrite closes s, rewrites the engine of its data directory dir with r, as a maintenance does, and opens the
// store again.
func closeAndRewrite(t *testing.T, s *Store, dir string, r logdb.Rewrite) *Store {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	rewriteEngine(t, dir, r)
	return reopenLog(t, dir, logdb.Options{})
}

// rewriteEngine compacts the engine of the data directory dir, which no process has open, as r says.
func rewriteEngine(t *testing.T, dir string, r logdb.Rewrite) {
	t.Helper()
	fsys, err := vfs.OpenRoot(filepath.Join(dir, "logdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fsys.Close() }()
	rewriteEngineIn(t, fsys, r)
}

// rewriteEngineIn compacts the engine in fsys, its directory logdb/, which no process has open, as r says.
func rewriteEngineIn(t *testing.T, fsys vfs.FS, r logdb.Rewrite) {
	t.Helper()
	if err := logdb.Maintain(t.Context(), fsys, logdb.Options{}, func(m *logdb.Maintenance) error {
		return m.Compact(t.Context(), r)
	}); err != nil {
		t.Fatalf("Compact() = %v", err)
	}
}

func TestCreateRecord(t *testing.T) {
	s, dir := newLogStore(t, logdb.Options{})
	// The schema, the record store.create and its head are in the one commit of Create.
	var seq uint64
	if err := logDB(t, s).View(t.Context(), func(tx *logdb.Tx) error {
		seq = tx.Seq()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if seq != 1 {
		t.Errorf("the last commit after Create() is %d, want 1", seq)
	}
	events := rawEvents(t, s)
	if len(events) != 1 || string(events[0].Data) != createRecordJSON || events[0].Number != 1 || events[0].Seq != 1 {
		t.Errorf("the events after Create() = %+v, want the record %s", events, createRecordJSON)
	}
	if got, want := rawTable(t, s, tableMeta), [][2]string{{"activityLog", "\x01" + string(createRecordSum())},
		{"schema", "\x06"}}; !slices.Equal(got, want) {
		t.Errorf("meta after Create() = %q, want %q", got, want)
	}
	heads, err := s.AuditHeads(t.Context())
	if want := []audit.Head{{Log: audit.ActivityLog, N: 1, Hash: [32]byte(createRecordSum())}}; err != nil ||
		!slices.Equal(heads, want) {
		t.Errorf("AuditHeads() = %v, %v; want %v", heads, err, want)
	}
	r := allRecords(t, s)
	if len(r) != 1 || r[0].Action != audit.StoreCreate || r[0].Actor != audit.System || r[0].N != 1 || r[0].Prev != "" ||
		!r[0].At.Equal(testTime) {
		t.Errorf("the records after Create() = %+v, want store.create of the system", r)
	}
	if err := s.VerifyAudit(t.Context(), nil); err != nil {
		t.Errorf("VerifyAudit() after Create() = %v", err)
	}

	// Open adds nothing to a store that is of the schema 6 already.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = reopenLog(t, dir, logdb.Options{})
	if got := len(rawEvents(t, s)); got != 1 {
		t.Errorf("the events after open() = %d, want 1", got)
	}
}

func TestRecordNumbers(t *testing.T) {
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, dir := newStore(t, tb)
		var puts []Version
		later := testTime.Add(90 * time.Second)
		steps := []struct {
			at   time.Time
			logs []audit.Record
		}{
			{testTime, []audit.Record{loginRecord(audit.Login, "a"), eventRecord("1"), loginRecord(audit.Logout, "a")}},
			{later, []audit.Record{eventRecord("2")}},
			{later.Add(time.Second), []audit.Record{loginRecord(audit.LoginFailure, "b"), loginRecord(audit.SessionEnd, "")}},
		}
		for _, st := range steps {
			s.now = func() time.Time { return st.at }
			update(t, s, func(tx *Tx) error {
				for _, r := range st.logs {
					if err := tx.Record(r); err != nil {
						return err
					}
				}
				// The versions and the records of an Update have the time at which it began.
				v, err := tx.Put(content.RefEntry, entryID, docJSON(content.RefEntry, entryID), userID)
				if err != nil {
					return err
				}
				puts = append(puts, v)
				return nil
			})
		}
		records := allRecords(t, s)
		// The numbers of a log start at 1, and the records of an Update are in the order of their calls.
		wantLogs := []audit.Log{audit.ActivityLog, audit.LoginLog, audit.ActivityLog, audit.LoginLog, audit.ActivityLog,
			audit.LoginLog, audit.LoginLog}
		wantN := []uint64{1, 1, 2, 2, 3, 3, 4}
		wantActions := []audit.Action{audit.StoreCreate, audit.Login, audit.StoreWrite, audit.Logout, audit.StoreWrite,
			audit.LoginFailure, audit.SessionEnd}
		if len(records) != len(wantN) {
			t.Fatalf("the records = %d, want %d", len(records), len(wantN))
		}
		events := rawEvents(t, s)
		var prevSum = map[audit.Log]string{}
		for i, r := range records {
			if r.Log != wantLogs[i] || r.N != wantN[i] || r.Action != wantActions[i] {
				t.Errorf("record %d is %s %d %s, want %s %d %s", i, r.Log, r.N, r.Action, wantLogs[i], wantN[i],
					wantActions[i])
			}
			if prev, want := r.Prev, prevSum[r.Log]; prev != want {
				t.Errorf("record %d has prev %q, want %q: the hash of the record before it in its log", i, prev, want)
			}
			sum := sha256.Sum256(events[i].Data)
			prevSum[r.Log] = "sha256:" + hex.EncodeToString(sum[:])
		}
		// The time is that of the Update, which the version has too.
		wantAt := []time.Time{testTime, testTime, testTime, testTime, later, later.Add(time.Second), later.Add(time.Second)}
		for i, r := range records {
			if !r.At.Equal(wantAt[i]) || r.At.Location() != time.UTC {
				t.Errorf("record %d has the time %v, want %v in UTC", i, r.At, wantAt[i])
			}
		}
		if !puts[0].SavedAt.Equal(records[1].At) {
			t.Errorf("the version saved at %v and the record at %v differ", puts[0].SavedAt, records[1].At)
		}
		// The heads are those of the last records.
		for _, log := range []audit.Log{audit.LoginLog, audit.ActivityLog} {
			h, ok := headOf(t, s, log)
			last := slices.IndexFunc(records, func(r audit.Record) bool { return r.Log == log })
			for i, r := range records {
				if r.Log == log {
					last = i
				}
			}
			if want := sha256.Sum256(events[last].Data); !ok || h.N != records[last].N || h.Hash != want {
				t.Errorf("the head of %s = %+v, %v; want %d and %x", log, h, ok, records[last].N, want)
			}
		}
		heads, err := s.AuditHeads(t.Context())
		if err != nil || len(heads) != 2 || heads[0].Log != audit.LoginLog || heads[1].Log != audit.ActivityLog {
			t.Errorf("AuditHeads() = %v, %v; want the login history and the activity log, in this order", heads, err)
		}
		if err := s.VerifyAudit(t.Context(), heads); err != nil {
			t.Errorf("VerifyAudit() = %v", err)
		}

		// The numbers go on after the store is closed and opened again.
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		s = reopenLog(t, dir, *tb.log)
		update(t, s, func(tx *Tx) error { return tx.Record(loginRecord(audit.Login, "c")) })
		all := allRecords(t, s)
		if last := all[len(all)-1]; last.Log != audit.LoginLog || last.N != 5 || last.Login != "c" {
			t.Errorf("the last record after open() = %+v, want the login 5", last)
		}
		if err := s.VerifyAudit(t.Context(), nil); err != nil {
			t.Errorf("VerifyAudit() after open() = %v", err)
		}
	})
}

func TestRecordRefused(t *testing.T) {
	s, _ := newLogStore(t, logdb.Options{})
	update(t, s, func(tx *Tx) error { return tx.Record(eventRecord("first")) })
	before := recordLines(allRecords(t, s))
	headsBefore, err := s.AuditHeads(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	seq := func() uint64 {
		var seq uint64
		if err := logDB(t, s).View(t.Context(), func(tx *logdb.Tx) error {
			seq = tx.Seq()
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return seq
	}
	seqBefore := seq()
	for _, tt := range []struct {
		name   string
		change func(r *audit.Record)
		want   string
	}{
		{"a number", func(r *audit.Record) { r.N = 7 }, "audit record: the store sets n, prev and at"},
		{"a prev", func(r *audit.Record) { r.Prev = "sha256:" + strings.Repeat("0", 64) }, "audit record: the store sets"},
		{"a time", func(r *audit.Record) { r.At = testTime }, "audit record: the store sets"},
		{"an action of the other log", func(r *audit.Record) { r.Action = audit.Login }, "audit record: action:"},
		{"an actor that is no user", func(r *audit.Record) { r.Actor = "alice" }, "audit record: actor:"},
		{"a long detail", func(r *audit.Record) { r.Detail = map[string]string{"a": strings.Repeat("x", 2049)} },
			"audit record: detail a: too long"},
		{"no log", func(r *audit.Record) { r.Log = "" }, "audit record: log:"},
		{"a client in the activity log", func(r *audit.Record) { r.Client = audit.Client{Addr: "192.0.2.1", UserAgent: "test"} },
			"audit record: client: the activity log has none"},
		{"a login name in the activity log", func(r *audit.Record) { r.Login = "yamada" },
			"audit record: login: the activity log has none"},
		{"a session in the activity log", func(r *audit.Record) { r.Session = strings.Repeat("0f", 32) },
			"audit record: session: the activity log has none"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := eventRecord("refused")
			tt.change(&r)
			// The Update ignores the error, so that it commits what it has written, which is nothing.
			update(t, s, func(tx *Tx) error {
				if err := tx.Record(r); err == nil || !strings.HasPrefix(err.Error(), tt.want) {
					t.Errorf("Record() = %v, want an error that starts with %q", err, tt.want)
				}
				return nil
			})
			if got := recordLines(allRecords(t, s)); !slices.Equal(got, before) {
				t.Errorf("the records after the refusal = %q, want %q", got, before)
			}
			if heads, err := s.AuditHeads(t.Context()); err != nil || !slices.Equal(heads, headsBefore) {
				t.Errorf("AuditHeads() after the refusal = %v, %v; want %v", heads, err, headsBefore)
			}
			if got := seq(); got != seqBefore {
				t.Errorf("the last commit is %d after the refusal, want %d: nothing is written", got, seqBefore)
			}
		})
	}

	// A record that is refused leaves the ones that the Update has added before it.
	update(t, s, func(tx *Tx) error {
		if err := tx.Record(eventRecord("kept")); err != nil {
			return err
		}
		bad := eventRecord("bad")
		bad.Actor = "alice"
		if err := tx.Record(bad); err == nil {
			t.Errorf("Record() of a record that Check refuses = nil")
		}
		return tx.Record(eventRecord("after"))
	})
	want := append(before, "activity 3 store.write kept", "activity 4 store.write after")
	if got := recordLines(allRecords(t, s)); !slices.Equal(got, want) {
		t.Errorf("the records = %q, want %q", got, want)
	}
	if err := s.VerifyAudit(t.Context(), nil); err != nil {
		t.Errorf("VerifyAudit() = %v", err)
	}
}

func TestRecordAtomic(t *testing.T) {
	errTest := errors.New("test")
	for _, tt := range []struct {
		name string
		run  func(t *testing.T, s *Store, records func(tx *Tx) error)
	}{
		{"the function fails", func(t *testing.T, s *Store, records func(tx *Tx) error) {
			err := s.Update(t.Context(), func(tx *Tx) error { return errors.Join(records(tx), errTest) })
			if !errors.Is(err, errTest) {
				t.Errorf("Update() = %v, want the error of f", err)
			}
		}},
		{"the function panics", func(t *testing.T, s *Store, records func(tx *Tx) error) {
			defer func() {
				if recover() == nil {
					t.Errorf("Update() has not panicked")
				}
			}()
			_ = s.Update(t.Context(), func(tx *Tx) error {
				_ = records(tx)
				panic("test")
			})
		}},
		{"the context ends", func(t *testing.T, s *Store, records func(tx *Tx) error) {
			ctx, cancel := context.WithCancel(t.Context())
			err := s.Update(ctx, func(tx *Tx) error {
				err := records(tx)
				cancel()
				return err
			})
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Update() = %v, want context.Canceled", err)
			}
		}},
		{"the hook of the store fails at the commit", func(t *testing.T, s *Store, records func(tx *Tx) error) {
			failAt(s, "commit", errTest)
			if err := s.Update(t.Context(), records); !errors.Is(err, errTest) {
				t.Errorf("Update() = %v, want the error of the hook", err)
			}
			s.hook = nil
		}},
		{"the engine fails before its commit point", func(t *testing.T, s *Store, records func(tx *Tx) error) {
			failAt(s, "logdb:update:written", errTest)
			if err := s.Update(t.Context(), records); !errors.Is(err, errTest) {
				t.Errorf("Update() = %v, want the error of the hook", err)
			}
			s.hook = nil
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newLogStore(t, logdb.Options{})
			update(t, s, func(tx *Tx) error { return tx.Record(eventRecord("first")) })
			update(t, s, func(tx *Tx) error { return tx.Record(loginRecord(audit.Login, "a")) })
			records := recordLines(allRecords(t, s))
			heads, err := s.AuditHeads(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			tt.run(t, s, func(tx *Tx) error {
				return errors.Join(tx.Record(eventRecord("lost")), tx.Record(loginRecord(audit.Logout, "a")),
					tx.Record(eventRecord("lost too")))
			})
			if got := recordLines(allRecords(t, s)); !slices.Equal(got, records) {
				t.Errorf("the records = %q, want %q: the Update has left none", got, records)
			}
			if got, err := s.AuditHeads(t.Context()); err != nil || !slices.Equal(got, heads) {
				t.Errorf("AuditHeads() = %v, %v; want %v: the Update has left none", got, err, heads)
			}
			if got := len(rawEvents(t, s)); got != len(records) {
				t.Errorf("the events = %d, want %d", got, len(records))
			}
			// The next record has the next number, and the chain is whole.
			update(t, s, func(tx *Tx) error { return tx.Record(eventRecord("next")) })
			want := append(records, "activity 3 store.write next")
			if got := recordLines(allRecords(t, s)); !slices.Equal(got, want) {
				t.Errorf("the records after the next Update = %q, want %q", got, want)
			}
			if err := s.VerifyAudit(t.Context(), nil); err != nil {
				t.Errorf("VerifyAudit() = %v", err)
			}
		})
	}
}

// TestChanged checks which writes make Changed true, in the model and in the log backend: those that write something,
// and not a Put of the data that the document has, nor a RebuildIndex of an index that is right, nor a record.
func TestChanged(t *testing.T) {
	data := docJSON(content.RefEntry, entryID)
	eachStore(t, func(t *testing.T, s *Store) {
		update(t, s, func(tx *Tx) error {
			if _, err := tx.Put(content.RefEntry, entryID, data, userID); err != nil {
				return err
			}
			if _, err := tx.AddRevision(entryID, revisionID, revJSON("A"), userID); err != nil {
				return err
			}
			return errors.Join(tx.PutAccount(userID, []byte("row")), tx.PutSession(sessionKeyOf("a"), []byte("s")),
				tx.PutOperation("op_B", []byte("b")))
		})
		for _, tt := range []struct {
			name string
			do   func(tx *Tx) error
			want bool
		}{
			{"nothing", func(tx *Tx) error { return nil }, false},
			{"reads", func(tx *Tx) error {
				_, err := tx.Document(content.RefEntry, entryID)
				if err == nil {
					_, err = tx.Revisions(entryID)
				}
				if err == nil {
					_, _, err = tx.Operation("op_A")
				}
				return err
			}, false},
			{"a Put of a new document", func(tx *Tx) error {
				_, err := tx.Put(content.RefEntry, pageID, docJSON(content.RefEntry, pageID), userID)
				return err
			}, true},
			{"a Put of the data that the document has", func(tx *Tx) error {
				_, err := tx.Put(content.RefEntry, entryID, data, otherUser)
				return err
			}, false},
			{"a Put of other data", func(tx *Tx) error {
				_, err := tx.Put(content.RefEntry, entryID, entryJSON(entryID, `"number": 2`), userID)
				return err
			}, true},
			{"a Delete", func(tx *Tx) error {
				_, err := tx.Delete(content.RefEntry, entryID, userID)
				return err
			}, true},
			{"a Delete of a document that is missing", func(tx *Tx) error {
				_, err := tx.Delete(content.RefEntry, pageID, userID)
				if err == nil {
					return errors.New("Delete() of a document that is missing = nil")
				}
				return nil
			}, false},
			{"an AddRevision", func(tx *Tx) error {
				_, err := tx.AddRevision(entryID, revision2, revJSON("B"), userID)
				return err
			}, true},
			{"a PutOperation", func(tx *Tx) error { return tx.PutOperation("op_A", []byte("x")) }, true},
			{"a DeleteOperation", func(tx *Tx) error { return tx.DeleteOperation("op_B") }, true},
			{"a DeleteOperation of an operation that has no data", func(tx *Tx) error { return tx.DeleteOperation("op_Z") }, false},
			{"a PutAccount", func(tx *Tx) error { return tx.PutAccount(otherUser, []byte("row")) }, true},
			{"a DeleteAccount", func(tx *Tx) error { return tx.DeleteAccount(userID) }, true},
			{"a PutSession", func(tx *Tx) error { return tx.PutSession(sessionKeyOf("b"), []byte("s")) }, true},
			{"a DeleteSession", func(tx *Tx) error { return tx.DeleteSession(sessionKeyOf("a")) }, true},
			{"a DeleteSession of a session that is missing", func(tx *Tx) error {
				if err := tx.DeleteSession(sessionKeyOf("z")); err == nil {
					return errors.New("DeleteSession() of a row that is missing = nil")
				}
				return nil
			}, false},
			{"a RebuildIndex of an index that is right", func(tx *Tx) error { return tx.RebuildIndex() }, false},
			{"a write that fails", func(tx *Tx) error {
				if _, err := tx.Put(content.RefEntry, "ent_x", data, userID); err == nil {
					return errors.New("Put() with an invalid ID = nil")
				}
				return nil
			}, false},
		} {
			t.Run(tt.name, func(t *testing.T) {
				var before, after bool
				err := s.Update(t.Context(), func(tx *Tx) error {
					before = tx.Changed()
					if err := tt.do(tx); err != nil {
						return err
					}
					after = tx.Changed()
					return errors.New("not committed") // each case starts from the same store
				})
				if err == nil || err.Error() != "not committed" {
					t.Fatalf("Update() = %v", err)
				}
				if before || after != tt.want {
					t.Errorf("Changed() before and after = %v and %v, want false and %v", before, after, tt.want)
				}
			})
		}
		t.Run("a View", func(t *testing.T) {
			view(t, s, func(tx *Tx) error {
				if tx.Changed() {
					t.Errorf("Changed() in a View = true")
				}
				return nil
			})
		})
	})

	// A RebuildIndex that writes rows. The model has no index to damage from outside.
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, _ := newStore(t, tb)
		fillIndexStore(t, s)
		rawUpdate(t, s, func(tx *logdb.Tx) error {
			return tx.Delete(tableSummaries, docKey(content.RefEntry, entryID))
		})
		update(t, s, func(tx *Tx) error {
			if err := tx.RebuildIndex(); err != nil {
				return err
			}
			if !tx.Changed() {
				t.Errorf("Changed() after a RebuildIndex that writes rows = false")
			}
			return nil
		})
		update(t, s, func(tx *Tx) error {
			if err := tx.RebuildIndex(); err != nil {
				return err
			}
			if tx.Changed() {
				t.Errorf("Changed() after a RebuildIndex of the index that is right = true")
			}
			return nil
		})
	})

	// A record changes nothing that Changed tells of, and a write after it does.
	forBackends(t, allBackends, func(t *testing.T, tb testBackend) {
		s, _ := newStore(t, tb)
		update(t, s, func(tx *Tx) error {
			if err := errors.Join(tx.Record(eventRecord("a")), tx.Record(loginRecord(audit.Login, "x"))); err != nil {
				return err
			}
			if tx.Changed() {
				t.Errorf("Changed() after records = true")
			}
			if _, err := tx.Put(content.RefEntry, entryID, data, userID); err != nil {
				return err
			}
			if !tx.Changed() {
				t.Errorf("Changed() after records and a Put = false")
			}
			return tx.Record(eventRecord("b"))
		})
	})
}

func TestReadAudit(t *testing.T) {
	s, dir := newLogStore(t, logdb.Options{})
	entryRec := func(action audit.Action, id, actor string) audit.Record {
		return audit.Record{Log: audit.ActivityLog, Actor: actor, Action: action, Target: audit.Target{Kind: "entry", ID: id}}
	}
	// The records, with the minutes after testTime at which the Update that adds them begins.
	steps := []struct {
		minute int
		recs   []audit.Record
	}{
		{1, []audit.Record{
			entryRec(audit.EntryCreate, entryID, userID),
			{Log: audit.LoginLog, Action: audit.Login, Account: userID, Session: strings.Repeat("a", 64)},
		}},
		{2, []audit.Record{
			entryRec(audit.EntryPublish, pageID, otherUser),
			{Log: audit.ActivityLog, Actor: userID, Action: audit.AccountUpdate,
				Target: audit.Target{Kind: "user", ID: otherUser}},
		}},
		{3, []audit.Record{
			{Log: audit.ActivityLog, Actor: audit.System, Action: audit.TermCreate,
				Target: audit.Target{Kind: "term", ID: termID}},
			loginRecord(audit.LoginFailure, "x"),
			entryRec(audit.EntryDelete, entryID, userID),
		}},
	}
	for _, st := range steps {
		s.now = func() time.Time { return testTime.Add(time.Duration(st.minute) * time.Minute) }
		update(t, s, func(tx *Tx) error {
			for _, r := range st.recs {
				if err := tx.Record(r); err != nil {
					return err
				}
			}
			return nil
		})
	}
	all := []string{
		"activity 1 store.create", "activity 2 entry.create", "login 1 login", "activity 3 entry.publish",
		"activity 4 account.update", "activity 5 term.create", "login 2 login.failure", "activity 6 entry.delete",
	}
	read := func(t *testing.T, s *Store, f audit.Filter) []string {
		t.Helper()
		var got []string
		if err := s.ReadAudit(t.Context(), f, func(r audit.Record) bool {
			got = append(got, fmt.Sprintf("%s %d %s", r.Log, r.N, r.Action))
			return true
		}); err != nil {
			t.Fatalf("ReadAudit(%+v) = %v", f, err)
		}
		return got
	}
	minute := func(n int) time.Time { return testTime.Add(time.Duration(n) * time.Minute) }
	for _, tt := range []struct {
		name string
		f    audit.Filter
		want []string
	}{
		{"all", audit.Filter{}, all},
		{"the activity log", audit.Filter{Log: audit.ActivityLog},
			[]string{all[0], all[1], all[3], all[4], all[5], all[7]}},
		{"the login history", audit.Filter{Log: audit.LoginLog}, []string{all[2], all[6]}},
		{"from a time", audit.Filter{From: minute(2)}, all[3:]},
		{"from the time of the first", audit.Filter{From: testTime}, all},
		{"up to a time", audit.Filter{To: minute(2)}, all[:3]},
		{"between two times", audit.Filter{From: minute(1), To: minute(3)}, all[1:5]},
		{"an empty range", audit.Filter{From: minute(3), To: minute(1)}, nil},
		{"the actor", audit.Filter{Account: userID}, []string{all[1], all[2], all[4], all[7]}},
		{"the actor of an Update", audit.Filter{Account: otherUser}, []string{all[3], all[4]}},
		{"the system", audit.Filter{Account: audit.System}, []string{all[0], all[5]}},
		{"an action", audit.Filter{Action: "entry.create"}, []string{all[1]}},
		{"a prefix", audit.Filter{Action: "entry."}, []string{all[1], all[3], all[7]}},
		{"a prefix of the login history", audit.Filter{Action: "login."}, []string{all[6]}},
		{"the target", audit.Filter{Target: audit.Target{Kind: "entry", ID: entryID}}, []string{all[1], all[7]}},
		{"the kind of the target", audit.Filter{Target: audit.Target{Kind: "term"}}, []string{all[5]}},
		{"the ID of the target", audit.Filter{Target: audit.Target{ID: pageID}}, []string{all[3]}},
		{"a target that nothing has", audit.Filter{Target: audit.Target{Kind: "media"}}, nil},
		{"every field", audit.Filter{Log: audit.ActivityLog, From: minute(1), To: minute(4), Account: userID,
			Action: "entry.", Target: audit.Target{Kind: "entry", ID: entryID}}, []string{all[1], all[7]}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := read(t, s, tt.f); !slices.Equal(got, tt.want) {
				t.Errorf("ReadAudit(%+v) = %q, want %q", tt.f, got, tt.want)
			}
		})
	}

	t.Run("a filter that is wrong", func(t *testing.T) {
		called := false
		err := s.ReadAudit(t.Context(), audit.Filter{Action: "entry.teleport"}, func(audit.Record) bool {
			called = true
			return true
		})
		if err == nil || called || !strings.HasPrefix(err.Error(), "filter action:") {
			t.Errorf("ReadAudit() with a filter that Check refuses = %v (called: %v), want its error", err, called)
		}
	})

	t.Run("the records are whole", func(t *testing.T) {
		var got []audit.Record
		if err := s.ReadAudit(t.Context(), audit.Filter{Action: "entry.publish"}, func(r audit.Record) bool {
			got = append(got, r)
			return true
		}); err != nil {
			t.Fatal(err)
		}
		want := entryRec(audit.EntryPublish, pageID, otherUser)
		if len(got) != 1 {
			t.Fatalf("records = %d, want 1", len(got))
		}
		r := got[0]
		if r.N != 3 || r.Prev == "" || !r.At.Equal(minute(2)) || r.Log != want.Log || r.Actor != want.Actor ||
			r.Action != want.Action || r.Target != want.Target {
			t.Errorf("record = %+v, want the one that was added with the number 3 and the time %v", r, minute(2))
		}
	})

	t.Run("it stops", func(t *testing.T) {
		n := 0
		stopAtThird := func(audit.Record) bool { n++; return n < 3 }
		if err := s.ReadAudit(t.Context(), audit.Filter{}, stopAtThird); err != nil || n != 3 {
			t.Errorf("ReadAudit() that stops after the third = %v after %d records, want nil after 3", err, n)
		}
		n = 0
		stopAtFirst := func(audit.Record) bool { n++; return false }
		if err := s.ReadAudit(t.Context(), audit.Filter{Log: audit.LoginLog}, stopAtFirst); err != nil || n != 1 {
			t.Errorf("ReadAudit() that stops at the first of the login history = %v after %d records, want nil after 1", err, n)
		}
	})

	t.Run("a reader", func(t *testing.T) {
		r, err := openLogReader(dir, options{})
		if err != nil {
			t.Fatalf("openLogReader() = %v", err)
		}
		t.Cleanup(func() { _ = r.Close() })
		if got := read(t, r, audit.Filter{}); !slices.Equal(got, all) {
			t.Errorf("ReadAudit() of the reader = %q, want %q", got, all)
		}
		heads, err := r.AuditHeads(t.Context())
		if want, _ := s.AuditHeads(t.Context()); err != nil || !slices.Equal(heads, want) || len(heads) != 2 {
			t.Errorf("AuditHeads() of the reader = %v, %v; want %v", heads, err, want)
		}
		if err := r.VerifyAudit(t.Context(), heads); err != nil {
			t.Errorf("VerifyAudit() of the reader = %v", err)
		}
		// What the writer adds after, the reader reads in its next call.
		update(t, s, func(tx *Tx) error { return tx.Record(loginRecord(audit.Logout, "x")) })
		want := []string{all[2], all[6], "login 3 logout"}
		if got := read(t, r, audit.Filter{Log: audit.LoginLog}); !slices.Equal(got, want) {
			t.Errorf("ReadAudit() of the reader after the writer has added a record = %q, want %q", got, want)
		}
		if got, err := r.AuditHeads(t.Context()); err != nil || len(got) != 2 || got[0].N != 3 {
			t.Errorf("AuditHeads() of the reader after the writer has added a record = %v, %v", got, err)
		}
		if err := r.Update(t.Context(), func(tx *Tx) error { return tx.Record(eventRecord("x")) }); err == nil ||
			err.Error() != "read-only transaction" {
			t.Errorf("Update() of the reader = %v, want read-only transaction", err)
		}
	})

	t.Run("a canceled context", func(t *testing.T) {
		canceled, cancel := context.WithCancel(t.Context())
		cancel()
		all := func(audit.Record) bool { return true }
		if err := s.ReadAudit(canceled, audit.Filter{}, all); !errors.Is(err, context.Canceled) {
			t.Errorf("ReadAudit() = %v, want context.Canceled", err)
		}
		if _, err := s.AuditHeads(canceled); !errors.Is(err, context.Canceled) {
			t.Errorf("AuditHeads() = %v, want context.Canceled", err)
		}
		if err := s.VerifyAudit(canceled, nil); !errors.Is(err, context.Canceled) {
			t.Errorf("VerifyAudit() = %v, want context.Canceled", err)
		}
	})

	t.Run("an event that is no record", func(t *testing.T) {
		rawUpdate(t, s, func(tx *logdb.Tx) error { return tx.AppendEvent([]byte("not a record")) })
		n := 0
		err := s.ReadAudit(t.Context(), audit.Filter{}, func(audit.Record) bool { n++; return true })
		want := "audit event 10: not a record of the audit log"
		if !errors.Is(err, audit.ErrBroken) || err == nil || err.Error() != want || n != 9 {
			t.Errorf("ReadAudit() = %v after %d records, want %q that wraps audit.ErrBroken after 9", err, n, want)
		}
	})
}

// TestRecordEachStore checks the records in the model and in the log backend.
func TestRecordEachStore(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		before := allRecords(t, s)
		var activity, login uint64
		for _, r := range before {
			if r.Log == audit.ActivityLog {
				activity = r.N
			}
		}
		update(t, s, func(tx *Tx) error {
			return errors.Join(tx.Record(eventRecord("a")), tx.Record(loginRecord(audit.Login, "x")),
				tx.Record(eventRecord("b")))
		})
		update(t, s, func(tx *Tx) error { return tx.Record(loginRecord(audit.Logout, "x")) })
		// An Update that fails leaves no record and no head.
		errTest := errors.New("test")
		if err := s.Update(t.Context(), func(tx *Tx) error {
			return errors.Join(tx.Record(eventRecord("lost")), tx.Record(loginRecord(audit.Login, "lost")), errTest)
		}); !errors.Is(err, errTest) {
			t.Errorf("Update() = %v, want the error of f", err)
		}
		got := allRecords(t, s)[len(before):]
		want := []string{
			fmt.Sprintf("activity %d store.write a", activity+1), fmt.Sprintf("login %d login ", login+1),
			fmt.Sprintf("activity %d store.write b", activity+2), fmt.Sprintf("login %d logout ", login+2),
		}
		if lines := recordLines(got); !slices.Equal(lines, want) {
			t.Errorf("the records = %q, want %q", lines, want)
		}
		heads, err := s.AuditHeads(t.Context())
		if err != nil || len(heads) != 2 || heads[0].N != login+2 || heads[1].N != activity+2 {
			t.Errorf("AuditHeads() = %v, %v; want the numbers %d and %d", heads, err, login+2, activity+2)
		}
		if err := s.VerifyAudit(t.Context(), heads); err != nil {
			t.Errorf("VerifyAudit() = %v", err)
		}
		if got := len(allRecords(t, s)); got != len(before)+4 {
			t.Errorf("the records = %d, want %d", got, len(before)+4)
		}
	})
}

// TestAuditHeads checks the heads of the two logs.
func TestAuditHeads(t *testing.T) {
	s, _ := newLogStore(t, logdb.Options{})
	// The activity log has the record store.create; the login history has none.
	if h, ok := headOf(t, s, audit.LoginLog); ok {
		t.Errorf("the head of the login history = %+v, want none", h)
	}
	update(t, s, func(tx *Tx) error { return tx.Record(loginRecord(audit.Login, "a")) })
	events := rawEvents(t, s)
	heads, err := s.AuditHeads(t.Context())
	want := []audit.Head{
		{Log: audit.LoginLog, N: 1, Hash: sha256.Sum256(events[1].Data)},
		{Log: audit.ActivityLog, N: 1, Hash: sha256.Sum256(events[0].Data)},
	}
	if err != nil || !slices.Equal(heads, want) {
		t.Errorf("AuditHeads() = %v, %v; want %v", heads, err, want)
	}
	if got := heads[0].String(); got != fmt.Sprintf("audit head login 1 sha256:%x", sha256.Sum256(events[1].Data)) {
		t.Errorf("the line of the head = %q", got)
	}
	// A head is the one that ParseHead reads.
	for _, h := range heads {
		if got, err := audit.ParseHead("journal: " + h.String()); err != nil || got != h {
			t.Errorf("ParseHead(%q) = %+v, %v; want %+v", h.String(), got, err, h)
		}
	}
}

func TestAuditHeadInvalid(t *testing.T) {
	// A head that cannot be read is an error.
	for name, value := range map[string][]byte{
		"empty": {}, "without the hash": {1}, "a hash that is short": append([]byte{1}, make([]byte, 31)...),
		"a byte after the hash": append([]byte{1}, make([]byte, 33)...),
		"the number 0":          append([]byte{0}, make([]byte, 32)...),
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newLogStore(t, logdb.Options{})
			rawUpdate(t, s, func(tx *logdb.Tx) error { return tx.Put(tableMeta, []byte(metaActivityLog), value) })
			const want = `meta "activityLog": invalid value`
			if _, err := s.AuditHeads(t.Context()); err == nil || err.Error() != want {
				t.Errorf("AuditHeads() = %v, want %q", err, want)
			}
			err := s.Update(t.Context(), func(tx *Tx) error { return tx.Record(eventRecord("x")) })
			if err == nil || err.Error() != want {
				t.Errorf("Record() = %v, want %q", err, want)
			}
			if err := s.VerifyAudit(t.Context(), nil); err == nil || err.Error() != want {
				t.Errorf("VerifyAudit() = %v, want %q", err, want)
			}
		})
	}
}

// toSchema5 makes the store in dir, which s has open, one of the table schema 5: it has no audit log, as the program
// before the schema 6 left it. s is closed.
func toSchema5(t *testing.T, s *Store, dir string) {
	t.Helper()
	rawUpdate(t, s, func(tx *logdb.Tx) error { return tx.Put(tableMeta, []byte(metaSchema), []byte{5}) })
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	dropAudit(t, dir)
}

// dropHeads is the Value of a rewrite that removes the heads of the audit logs from meta.
func dropHeads(table uint32, key, value []byte) ([]byte, bool) {
	isHead := table == tableMeta && (string(key) == metaLoginLog || string(key) == metaActivityLog)
	return value, !isHead
}

// dropAuditRewrite is the rewrite that removes the audit events and the heads from a store.
func dropAuditRewrite() logdb.Rewrite {
	return logdb.Rewrite{Event: func(logdb.Event) ([]byte, bool) { return nil, false }, Value: dropHeads}
}

// dropAudit removes the audit events and the heads from the store in dir, which no process has open.
func dropAudit(t *testing.T, dir string) {
	t.Helper()
	rewriteEngine(t, dir, dropAuditRewrite())
}

func TestNoAuditLog(t *testing.T) {
	s, dir := newLogStore(t, logdb.Options{})
	putWithRevision(t, s)
	toSchema5(t, s, dir)
	r, err := openLogReader(dir, options{})
	if err != nil {
		t.Fatalf("openLogReader() of a store of the schema 5 = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if got := len(rawEvents(t, r)); got != 0 {
		t.Fatalf("the store of the schema 5 has %d events, want none", got)
	}
	called := false
	errs := map[string]error{
		"ReadAudit":   r.ReadAudit(t.Context(), audit.Filter{}, func(audit.Record) bool { called = true; return true }),
		"VerifyAudit": r.VerifyAudit(t.Context(), nil),
	}
	_, errs["AuditHeads"] = r.AuditHeads(t.Context())
	for name, err := range errs {
		if !errors.Is(err, ErrNoAuditLog) {
			t.Errorf("%s() of a reader of the table schema 5 = %v, want ErrNoAuditLog", name, err)
		}
	}
	const want = "the store has a table schema before 6, which has no audit log; open it as its writer once to upgrade it"
	if ErrNoAuditLog.Error() != want {
		t.Errorf("ErrNoAuditLog = %q, want %q", ErrNoAuditLog, want)
	}
	if called {
		t.Errorf("ReadAudit() has called its function")
	}

	// Its writer upgrades it, and the reader that was opened first reads the audit log then.
	w := reopenLog(t, dir, logdb.Options{})
	if got := recordLines(allRecords(t, w)); !slices.Equal(got, []string{"activity 1 store.upgrade "}) {
		t.Errorf("the records after the upgrade = %q, want store.upgrade", got)
	}
	if got := recordLines(allRecords(t, r)); !slices.Equal(got, []string{"activity 1 store.upgrade "}) {
		t.Errorf("the records of the reader after the upgrade = %q, want store.upgrade", got)
	}
	if err := w.VerifyAudit(t.Context(), nil); err != nil {
		t.Errorf("VerifyAudit() after the upgrade = %v", err)
	}
}

// TestUpgradeTrialEvent checks that the upgrade of a store of the table schema 5 does not look at the audit events that
// it may have, which a trial before the schema 6 wrote (a store in use has none): they are no records, and VerifyAudit
// and ReadAudit find them.
func TestUpgradeTrialEvent(t *testing.T) {
	s, dir := newLogStore(t, logdb.Options{})
	rawUpdate(t, s, func(tx *logdb.Tx) error { return tx.Put(tableMeta, []byte(metaSchema), []byte{5}) })
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	rewriteEngine(t, dir, logdb.Rewrite{
		Event: func(logdb.Event) ([]byte, bool) { return []byte("pair 1"), true },
		Value: dropHeads,
	})

	w := reopenLog(t, dir, logdb.Options{})
	events := rawEvents(t, w)
	const upgrade = `{"log":"activity","n":1,"at":"2026-11-03T08:00:00.12Z","actor":"system","action":"store.upgrade",` +
		`"detail":{"from":"5","to":"6"}}`
	if len(events) != 2 || string(events[0].Data) != "pair 1" || string(events[1].Data) != upgrade {
		t.Fatalf("the events after the upgrade = %d, want the trial event and the record store.upgrade", len(events))
	}
	const want = "audit event 1: not a record of the audit log"
	err := w.ReadAudit(t.Context(), audit.Filter{}, func(audit.Record) bool { return true })
	if !errors.Is(err, audit.ErrBroken) || err == nil || err.Error() != want {
		t.Errorf("ReadAudit() = %v, want %q that wraps audit.ErrBroken", err, want)
	}
	if err := w.VerifyAudit(t.Context(), nil); !errors.Is(err, audit.ErrBroken) || err == nil || err.Error() != want {
		t.Errorf("VerifyAudit() = %v, want %q that wraps audit.ErrBroken", err, want)
	}
	// The records go on from the record of the upgrade.
	update(t, w, func(tx *Tx) error { return tx.Record(eventRecord("after")) })
	if h, ok := headOf(t, w, audit.ActivityLog); !ok || h.N != 2 {
		t.Errorf("the head of the activity log = %+v, %v; want the record 2", h, ok)
	}
}

// auditFixture makes a store with 6 records of the activity log and 3 of the login history, interleaved, and returns it
// with its data directory and its heads, which stand for the heads that were written outside it. The events are 1
// store.create (activity 1), 2 event 0 (activity 2), 3 a failure (login 1), 4 event 1 (activity 3), 5 event 2
// (activity 4), 6 a failure (login 2), 7 event 3 (activity 5), 8 event 4 (activity 6) and 9 a failure (login 3).
func auditFixture(t *testing.T) (s *Store, dir string, heads []audit.Head) {
	t.Helper()
	s, dir = newLogStore(t, logdb.Options{})
	for i := range 5 {
		s.now = func() time.Time { return testTime.Add(time.Duration(i) * time.Second) }
		update(t, s, func(tx *Tx) error {
			if err := tx.Record(eventRecord(fmt.Sprint(i))); err != nil {
				return err
			}
			if i%2 == 0 {
				return tx.Record(loginRecord(audit.LoginFailure, fmt.Sprint(i)))
			}
			return nil
		})
	}
	heads, err := s.AuditHeads(t.Context())
	if err != nil || len(heads) != 2 || heads[0].N != 3 || heads[1].N != 6 {
		t.Fatalf("AuditHeads() = %v, %v", heads, err)
	}
	if err := s.VerifyAudit(t.Context(), heads); err != nil {
		t.Fatalf("VerifyAudit() of the store before the changes = %v", err)
	}
	return s, dir, heads
}

// headValue is the value of the head h in meta.
func headValue(h audit.Head) []byte {
	return append(binary.AppendUvarint(nil, h.N), h.Hash[:]...)
}

// replaced returns data with the first old in it replaced by new, which must be there.
func replaced(t *testing.T, data []byte, old, new string) []byte {
	t.Helper()
	s := string(data)
	if !strings.Contains(s, old) {
		t.Fatalf("%q is not in %s", old, s)
	}
	return []byte(strings.Replace(s, old, new, 1))
}

// rewriteEvents is the rewrite that replaces the data of the events replace, by their numbers, drops those of drop, and
// sets the heads in meta to heads (a log without a head loses it; the other logs keep theirs).
func rewriteEvents(replace map[uint64][]byte, drop []uint64, heads map[audit.Log]*audit.Head) logdb.Rewrite {
	return logdb.Rewrite{
		Event: func(e logdb.Event) ([]byte, bool) {
			if slices.Contains(drop, e.Number) {
				return nil, false
			}
			if data, ok := replace[e.Number]; ok {
				return data, true
			}
			return e.Data, true
		},
		Value: func(table uint32, key, value []byte) ([]byte, bool) {
			if table != tableMeta {
				return value, true
			}
			for log, name := range metaHeads {
				if string(key) != name {
					continue
				}
				if h, ok := heads[log]; ok {
					if h == nil {
						return nil, false
					}
					return headValue(*h), true
				}
			}
			return value, true
		},
	}
}

// rechain returns the events with the records of log from the number from on made again by change, as a chain that
// holds: each has the hash of the one before it, which is a new one after the first that change has made. It returns
// the head of log that goes with them.
func rechain(t *testing.T, events []logdb.Event, log audit.Log, from uint64, change func(r *audit.Record)) (
	replace map[uint64][]byte, head audit.Head) {
	t.Helper()
	replace = map[uint64][]byte{}
	var prev *[sha256.Size]byte
	for _, e := range events {
		r, err := audit.Decode(e.Data)
		if err != nil {
			t.Fatal(err)
		}
		if r.Log != log {
			continue
		}
		data := e.Data
		if r.N >= from {
			change(&r)
			r.Prev = ""
			if prev != nil {
				r.Prev = hashText(prev[:])
			}
			if data, err = audit.Encode(r); err != nil {
				t.Fatal(err)
			}
			replace[e.Number] = data
		}
		sum := sha256.Sum256(data)
		prev, head = &sum, audit.Head{Log: log, N: r.N, Hash: sum}
	}
	return replace, head
}

func TestVerifyAudit(t *testing.T) {
	type fixture struct {
		events []logdb.Event
		heads  []audit.Head
	}
	const same = "same as without the heads"
	for _, tt := range []struct {
		name     string
		rewrite  func(t *testing.T, f fixture) logdb.Rewrite
		want     string // the error of VerifyAudit(nil); "" for none
		anchored string // the error of VerifyAudit(heads) with the heads before the rewrite; same, or "" for none
	}{
		{"a record changed", func(t *testing.T, f fixture) logdb.Rewrite {
			data := replaced(t, f.events[3].Data, `"event":"1"`, `"event":"X"`)
			return rewriteEvents(map[uint64][]byte{4: data}, nil, nil)
		}, `audit log "activity": record 4 (event 5): prev is not the hash of record 3`, same},
		{"the last record of a log changed", func(t *testing.T, f fixture) logdb.Rewrite {
			data := replaced(t, f.events[7].Data, `"event":"4"`, `"event":"Z"`)
			return rewriteEvents(map[uint64][]byte{8: data}, nil, nil)
		}, `audit log "activity": record 6 (event 8): its hash is not that of the head in the table`,
			`audit log "activity": record 6 (event 8): its hash is not that of the head kept outside the store`},
		{"the last record of the other log changed", func(t *testing.T, f fixture) logdb.Rewrite {
			data := replaced(t, f.events[8].Data, `"login":"4"`, `"login":"Z"`)
			return rewriteEvents(map[uint64][]byte{9: data}, nil, nil)
		}, `audit log "login": record 3 (event 9): its hash is not that of the head in the table`,
			`audit log "login": record 3 (event 9): its hash is not that of the head kept outside the store`},
		{"a record dropped", func(t *testing.T, f fixture) logdb.Rewrite {
			return rewriteEvents(nil, []uint64{4}, nil)
		}, `audit log "activity": record 4 (event 5): the number does not follow that of record 2`, same},
		{"a record dropped from the other log", func(t *testing.T, f fixture) logdb.Rewrite {
			return rewriteEvents(nil, []uint64{6}, nil)
		}, `audit log "login": record 3 (event 9): the number does not follow that of record 1`, same},
		{"the first record dropped", func(t *testing.T, f fixture) logdb.Rewrite {
			return rewriteEvents(nil, []uint64{1}, nil)
		}, `audit log "activity": record 2 (event 2): the first record of a log is number 1`, same},
		{"the last record dropped", func(t *testing.T, f fixture) logdb.Rewrite {
			return rewriteEvents(nil, []uint64{9}, nil)
		}, `audit log "login": the head in the table is record 3, but the last record is record 2 (event 6)`, same},
		{"records out of order", func(t *testing.T, f fixture) logdb.Rewrite {
			return rewriteEvents(map[uint64][]byte{4: f.events[4].Data, 5: f.events[3].Data}, nil, nil)
		}, `audit log "activity": record 4 (event 4): the number does not follow that of record 2`, same},
		{"a record twice", func(t *testing.T, f fixture) logdb.Rewrite {
			return rewriteEvents(map[uint64][]byte{5: f.events[3].Data}, nil, nil)
		}, `audit log "activity": record 3 (event 5): the number does not follow that of record 3`, same},
		{"the first record with a prev", func(t *testing.T, f fixture) logdb.Rewrite {
			r, err := audit.Decode(f.events[2].Data)
			if err != nil {
				t.Fatal(err)
			}
			r.Prev = "sha256:" + strings.Repeat("0", 64)
			data, err := audit.Encode(r)
			if err != nil {
				t.Fatal(err)
			}
			return rewriteEvents(map[uint64][]byte{3: data}, nil, nil)
		}, `audit log "login": record 1 (event 3): the first record has the hash of one before it`, same},
		{"the hash of the head changed", func(t *testing.T, f fixture) logdb.Rewrite {
			h := f.heads[1]
			h.Hash[0] ^= 1
			return rewriteEvents(nil, nil, map[audit.Log]*audit.Head{audit.ActivityLog: &h})
		}, `audit log "activity": record 6 (event 8): its hash is not that of the head in the table`, same},
		{"the number of the head changed", func(t *testing.T, f fixture) logdb.Rewrite {
			h := f.heads[1]
			h.N = 5
			return rewriteEvents(nil, nil, map[audit.Log]*audit.Head{audit.ActivityLog: &h})
		}, `audit log "activity": the head in the table is record 5, but the last record is record 6 (event 8)`, same},
		{"the number of the head raised", func(t *testing.T, f fixture) logdb.Rewrite {
			h := f.heads[0]
			h.N = 4
			return rewriteEvents(nil, nil, map[audit.Log]*audit.Head{audit.LoginLog: &h})
		}, `audit log "login": the head in the table is record 4, but the last record is record 3 (event 9)`, same},
		{"the head dropped", func(t *testing.T, f fixture) logdb.Rewrite {
			return rewriteEvents(nil, nil, map[audit.Log]*audit.Head{audit.ActivityLog: nil})
		}, `audit log "activity": the table has no head, but the log has records, the last of which is record 6 (event 8)`,
			same},
		{"the head of a log without records", func(t *testing.T, f fixture) logdb.Rewrite {
			return rewriteEvents(nil, []uint64{3, 6, 9}, nil)
		}, `audit log "login": the table has the head of record 3, but the log has no record`, same},
		{"an event that is no record", func(t *testing.T, f fixture) logdb.Rewrite {
			return rewriteEvents(map[uint64][]byte{5: []byte("junk")}, nil, nil)
		}, `audit event 5: not a record of the audit log`, same},
		{"an event with a JSON that is not a record", func(t *testing.T, f fixture) logdb.Rewrite {
			return rewriteEvents(map[uint64][]byte{5: []byte(`{"log":"activity","n":4}`)}, nil, nil)
		}, `audit event 5: not a record of the audit log`, same},
		{"the last record dropped with the head", func(t *testing.T, f fixture) logdb.Rewrite {
			h := audit.Head{Log: audit.ActivityLog, N: 5, Hash: sha256.Sum256(f.events[6].Data)}
			return rewriteEvents(nil, []uint64{8}, map[audit.Log]*audit.Head{audit.ActivityLog: &h})
		}, "", `audit log "activity": the head kept outside the store is record 6, but the log ends with record 5 (event 7)`},
		{"the records of a log dropped with the head", func(t *testing.T, f fixture) logdb.Rewrite {
			return rewriteEvents(nil, []uint64{3, 6, 9}, map[audit.Log]*audit.Head{audit.LoginLog: nil})
		}, "", `audit log "login": the head kept outside the store is record 3, but the log has no record`},
		{"the chain made again from a record on", func(t *testing.T, f fixture) logdb.Rewrite {
			replace, head := rechain(t, f.events, audit.ActivityLog, 3, func(r *audit.Record) {
				r.Detail = map[string]string{"event": "forged"}
			})
			return rewriteEvents(replace, nil, map[audit.Log]*audit.Head{audit.ActivityLog: &head})
		}, "", `audit log "activity": record 6 (event 8): its hash is not that of the head kept outside the store`},
		{"the chain made again from the first", func(t *testing.T, f fixture) logdb.Rewrite {
			replace, head := rechain(t, f.events, audit.ActivityLog, 1, func(r *audit.Record) {
				r.At = r.At.Add(time.Hour)
			})
			return rewriteEvents(replace, nil, map[audit.Log]*audit.Head{audit.ActivityLog: &head})
		}, "", `audit log "activity": record 6 (event 8): its hash is not that of the head kept outside the store`},
		{"the chain of the login history made again from its second record", func(t *testing.T, f fixture) logdb.Rewrite {
			replace, head := rechain(t, f.events, audit.LoginLog, 2, func(r *audit.Record) {
				r.Login = "forged"
			})
			return rewriteEvents(replace, nil, map[audit.Log]*audit.Head{audit.LoginLog: &head})
		}, "", `audit log "login": record 3 (event 9): its hash is not that of the head kept outside the store`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, dir, heads := auditFixture(t)
			events := rawEvents(t, s)
			s = closeAndRewrite(t, s, dir, tt.rewrite(t, fixture{events, heads}))
			reader, err := openLogReader(dir, options{})
			if err != nil {
				t.Fatalf("openLogReader() = %v", err)
			}
			t.Cleanup(func() { _ = reader.Close() })
			for who, st := range map[string]*Store{"the writer": s, "a reader": reader} {
				anchored := tt.anchored
				if anchored == same {
					anchored = tt.want
				}
				for _, c := range []struct {
					anchors []audit.Head
					want    string
				}{{nil, tt.want}, {heads, anchored}} {
					err := st.VerifyAudit(t.Context(), c.anchors)
					switch {
					case c.want == "" && err != nil:
						t.Errorf("VerifyAudit(%d anchors) of %s = %v, want nil", len(c.anchors), who, err)
					case c.want != "" && (err == nil || err.Error() != c.want || !errors.Is(err, audit.ErrBroken)):
						t.Errorf("VerifyAudit(%d anchors) of %s = %v, want %q that wraps audit.ErrBroken", len(c.anchors), who,
							err, c.want)
					}
				}
			}
			// The records that can be read are read: ReadAudit does not verify.
			if tt.name == "a record dropped" {
				if got := len(allRecords(t, s)); got != 8 {
					t.Errorf("ReadAudit() reads %d records, want the 8 that are there", got)
				}
			}
		})
	}
}

// TestVerifyAuditNeedsTheActivityLog checks that a store of the table schema 6 whose activity log has neither a record nor
// a head is found without heads that were kept outside the store: every such store has store.create or store.upgrade as
// the first record of its activity log. The login history may have none.
func TestVerifyAuditNeedsTheActivityLog(t *testing.T) {
	const missing = `audit log "activity": the log has no record, but every store of the table schema 6 or later has ` +
		`store.create or store.upgrade as its first`
	check := func(t *testing.T, dir string, writer *Store, want string) {
		t.Helper()
		reader, err := openLogReader(dir, options{})
		if err != nil {
			t.Fatalf("openLogReader() = %v", err)
		}
		t.Cleanup(func() { _ = reader.Close() })
		for who, st := range map[string]*Store{"the writer": writer, "a reader": reader} {
			err := st.VerifyAudit(t.Context(), nil)
			switch {
			case want == "" && err != nil:
				t.Errorf("VerifyAudit() of %s = %v, want nil", who, err)
			case want != "" && (err == nil || err.Error() != want || !errors.Is(err, audit.ErrBroken)):
				t.Errorf("VerifyAudit() of %s = %v, want %q that wraps audit.ErrBroken", who, err, want)
			}
		}
	}

	t.Run("a store with documents whose records and heads are all gone", func(t *testing.T) {
		s, dir := newLogStore(t, logdb.Options{})
		putWithRevision(t, s)
		s = closeAndRewrite(t, s, dir, dropAuditRewrite())
		if heads, err := s.AuditHeads(t.Context()); err != nil || len(heads) != 0 {
			t.Fatalf("AuditHeads() = %v, %v; want no head", heads, err)
		}
		check(t, dir, s, missing)
	})

	// The events of auditFixture are 1, 2, 4, 5, 7 and 8 of the activity log, and 3, 6 and 9 of the login history.
	for _, tt := range []struct {
		name    string
		rewrite logdb.Rewrite
		want    string
	}{
		{"every record and both heads dropped", dropAuditRewrite(), missing},
		{"the activity log dropped with its head",
			rewriteEvents(nil, []uint64{1, 2, 4, 5, 7, 8}, map[audit.Log]*audit.Head{audit.ActivityLog: nil}), missing},
		{"the activity log dropped, and its head kept",
			rewriteEvents(nil, []uint64{1, 2, 4, 5, 7, 8}, nil),
			`audit log "activity": the table has the head of record 6, but the log has no record`},
		{"the login history dropped with its head",
			rewriteEvents(nil, []uint64{3, 6, 9}, map[audit.Log]*audit.Head{audit.LoginLog: nil}), ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, dir, heads := auditFixture(t)
			s = closeAndRewrite(t, s, dir, tt.rewrite)
			check(t, dir, s, tt.want)
			// The heads that were kept outside the store find what the store alone does not: the login history is gone.
			if tt.want == "" {
				err := s.VerifyAudit(t.Context(), heads)
				if want := `audit log "login": the head kept outside the store is record 3, but the log has no record`; err == nil ||
					err.Error() != want {
					t.Errorf("VerifyAudit() with the heads kept outside = %v, want %q", err, want)
				}
			}
		})
	}
}

func TestVerifyAuditAnchors(t *testing.T) {
	s, _, heads := auditFixture(t)
	wrong := sha256.Sum256([]byte("another record"))
	for _, tt := range []struct {
		name    string
		anchors []audit.Head
		want    string
	}{
		{"none", nil, ""},
		{"the heads", heads, ""},
		{"the heads twice", append(slices.Clone(heads), heads...), ""},
		{"one log", heads[1:], ""},
		{"a number past the end", []audit.Head{{Log: audit.ActivityLog, N: 9, Hash: wrong}},
			`audit log "activity": the head kept outside the store is record 9, but the log ends with record 6 (event 8)`},
		{"the number past the end by one", []audit.Head{{Log: audit.LoginLog, N: 4, Hash: heads[0].Hash}},
			`audit log "login": the head kept outside the store is record 4, but the log ends with record 3 (event 9)`},
		{"a hash that is wrong", []audit.Head{{Log: audit.LoginLog, N: 2, Hash: wrong}},
			`audit log "login": record 2 (event 6): its hash is not that of the head kept outside the store`},
		{"a hash of the other log", []audit.Head{{Log: audit.LoginLog, N: 3, Hash: heads[1].Hash}},
			`audit log "login": record 3 (event 9): its hash is not that of the head kept outside the store`},
		{"two that differ at a number", []audit.Head{heads[0], {Log: audit.LoginLog, N: 3, Hash: wrong}},
			`audit log "login": record 3 (event 9): its hash is not that of the head kept outside the store`},
		{"a log that is not one", []audit.Head{{Log: "audit", N: 1}}, "anchor 1: not the head of an audit log"},
		{"no log", []audit.Head{heads[0], {N: 1}}, "anchor 2: not the head of an audit log"},
		{"the number 0", []audit.Head{{Log: audit.LoginLog}}, "anchor 1: not the head of an audit log"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := s.VerifyAudit(t.Context(), tt.anchors)
			switch {
			case tt.want == "" && err != nil:
				t.Errorf("VerifyAudit() = %v, want nil", err)
			case tt.want != "" && (err == nil || err.Error() != tt.want):
				t.Errorf("VerifyAudit() = %v, want %q", err, tt.want)
			case strings.HasPrefix(tt.want, "anchor") && errors.Is(err, audit.ErrBroken):
				t.Errorf("VerifyAudit() = %v wraps audit.ErrBroken: the anchors are wrong, the log is not", err)
			}
		})
	}

	// The heads of an earlier time are right as long as the log has the records, and a log that has grown has them.
	update(t, s, func(tx *Tx) error {
		return errors.Join(tx.Record(eventRecord("more")), tx.Record(loginRecord(audit.Logout, "more")))
	})
	if err := s.VerifyAudit(t.Context(), heads); err != nil {
		t.Errorf("VerifyAudit() with the heads from before the last Update = %v", err)
	}
	newer, err := s.AuditHeads(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyAudit(t.Context(), append(slices.Clone(heads), newer...)); err != nil {
		t.Errorf("VerifyAudit() with the heads from before and after = %v", err)
	}

	// A store without a record of the login history has no head to be kept outside.
	fresh, _ := newLogStore(t, logdb.Options{})
	err = fresh.VerifyAudit(t.Context(), []audit.Head{{Log: audit.LoginLog, N: 1, Hash: wrong}})
	want := `audit log "login": the head kept outside the store is record 1, but the log has no record`
	if err == nil || err.Error() != want || !errors.Is(err, audit.ErrBroken) {
		t.Errorf("VerifyAudit() with a head of a log that has no record = %v, want %q", err, want)
	}
}

// TestVerifyAuditWhileWriting checks that VerifyAudit, which reads the events and the heads of one snapshot, finds
// nothing wrong in a store whose writer goes on writing, and that ReadAudit sees the records grow.
func TestVerifyAuditWhileWriting(t *testing.T) {
	s, _ := newLogStore(t, logdb.Options{})
	done := make(chan struct{})
	var werr error
	go func() {
		defer close(done)
		for i := range 400 {
			if werr = s.Update(context.Background(), func(tx *Tx) error {
				return errors.Join(tx.Record(eventRecord(fmt.Sprint(i))), tx.Record(loginRecord(audit.Login, "a")))
			}); werr != nil {
				return
			}
		}
	}()
	verified, last := 0, 0
	for running := true; running; verified++ {
		select {
		case <-done:
			running = false
		default:
		}
		if err := s.VerifyAudit(t.Context(), nil); err != nil {
			t.Errorf("VerifyAudit() while the writer writes = %v", err)
			break
		}
		n := len(allRecords(t, s))
		if n < last {
			t.Errorf("the records = %d after %d", n, last)
		}
		last = n
	}
	<-done
	if werr != nil {
		t.Fatalf("Update() = %v", werr)
	}
	if want := 1 + 2*400; last > want {
		t.Errorf("the records = %d, at most %d", last, want)
	}
	if err := s.VerifyAudit(t.Context(), nil); err != nil {
		t.Errorf("VerifyAudit() at the end = %v", err)
	}
	t.Logf("%d verifications while the writer wrote 800 records", verified)
}
