//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// state is what a daemon serves: the tables, the events with their numbers, and the objects that the test knows.
type state struct {
	Seq     uint64
	Tables  map[uint32][]string
	Events  []string
	Objects []string
}

func stateOf(t *testing.T, c *dbclient.Client, objects ...[32]byte) state {
	t.Helper()
	st := state{Tables: map[uint32][]string{}}
	if err := c.View(t.Context(), func(tx *dbclient.Tx) error {
		st.Seq = tx.Seq()
		for table := uint32(1); table <= 16; table++ {
			if err := tx.Scan(table, nil, func(k, v []byte) bool {
				st.Tables[table] = append(st.Tables[table], fmt.Sprintf("%q=%q", k, v))
				return true
			}); err != nil {
				return err
			}
		}
		for _, h := range objects {
			data, found, err := tx.Object(h)
			if err != nil {
				return err
			}
			st.Objects = append(st.Objects, fmt.Sprintf("%x:%v:%x", h, found, sha256.Sum256(data)))
		}
		return tx.Events(t.Context(), func(number uint64, data []byte) bool {
			st.Events = append(st.Events, fmt.Sprintf("%d:%q", number, data))
			return true
		})
	}); err != nil {
		t.Fatalf("reading the state: %v", err)
	}
	return st
}

func TestMaintain(t *testing.T) {
	d := newDirs(t)
	object := bytes.Repeat([]byte("o"), 100<<10) // over the 64 KiB from which the engine keeps an object in a file
	oh := sha256.Sum256(object)
	original := bytes.Repeat([]byte("GIF89a"), 1000)
	var (
		before state
		ohash  [32]byte
	)
	func() {
		s := d.startServe(t, d.args()...)
		w := openClient(t, d.socks, dbwire.RoleWriter, dbclient.Options{})
		mustCommit(t, w, row{2, "a", "1"}, row{2, "b", "2"})
		if err := w.Update(t.Context(), func(tx *dbclient.Tx) error {
			if err := tx.PutObject(oh, object); err != nil {
				return err
			}
			if err := tx.Put(2, []byte("object"), oh[:]); err != nil {
				return err
			}
			return tx.AppendEvent([]byte("object"))
		}); err != nil {
			t.Fatalf("committing an object = %v", err)
		}
		var err error
		if ohash, _, err = w.PutOriginal(t.Context(), bytes.NewReader(original)); err != nil {
			t.Fatalf("PutOriginal() = %v", err)
		}
		mustCommit(t, w, row{2, "a", "3"})
		before = stateOf(t, w, oh)
		if _, err := os.Stat(filepath.Join(d.data, "logdb", "objects", fmt.Sprintf("%x", oh[:1]), fmt.Sprintf("%x", oh))); err != nil {
			t.Fatalf("the object has no file: %v", err)
		}

		// While the daemon runs, compact and gc wait for about two seconds, fail, and change nothing.
		files0 := files(t, d.data)
		for _, command := range []string{"compact", "gc"} {
			start := time.Now()
			code, stdout, stderr := runCmd(t, command, "-data", d.data)
			want := fmt.Sprintf("whiteroom-db %s: %s: the data directory is in use; stop whiteroom-db serve on it first\n", command, d.data)
			if code != 1 || stdout != "" || stderr != want {
				t.Errorf("whiteroom-db %s on a directory in use = %d, with stdout %q and stderr %q, want 1, nothing and %q",
					command, code, stdout, stderr, want)
			}
			if took := time.Since(start); took < 1500*time.Millisecond || took > 8*time.Second {
				t.Errorf("whiteroom-db %s on a directory in use took %v, want about 2 seconds", command, took)
			}
		}
		if got := files(t, d.data); !maps.Equal(got, files0) {
			t.Errorf("compact and gc on a directory in use changed the files:\n%v\nwas\n%v", got, files0)
		}
		if got := stateOf(t, w, oh); !reflect.DeepEqual(got, before) {
			t.Errorf("the daemon serves %+v after the refusals, was %+v", got, before)
		}
		mustCommit(t, w, row{2, "a", "3"}) // the writer still commits
		before = stateOf(t, w, oh)
		if code := s.stop(); code != 0 {
			t.Fatalf("whiteroom-db serve = %d", code)
		}
		_ = w.Close() // its lock would keep out the writer below
	}()

	// Garbage: the file of an object that no commit names, and two temporary files.
	objects := filepath.Join(d.data, "logdb", "objects", "ab")
	if err := os.Mkdir(objects, 0o700); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(objects, strings.Repeat("ab", 32))
	for _, name := range []string{orphan, filepath.Join(objects, ".tmp-1234"), filepath.Join(d.data, "logdb", ".tmp-checkpoint-1")} {
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	logNames := func() string {
		entries, err := os.ReadDir(filepath.Join(d.data, "logdb", "log"))
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return strings.Join(names, " ")
	}
	segments := logNames()

	code, stdout, stderr := runCmd(t, "compact", "-data", d.data)
	if want := "compacted " + d.data + "\n"; code != 0 || stdout != want || stderr != "" {
		t.Fatalf("whiteroom-db compact = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, want)
	}
	if got := logNames(); got == segments {
		t.Errorf("the log has the segments %q after compact, as before: want a new generation", got)
	}
	code, stdout, stderr = runCmd(t, "gc", "-data", d.data)
	want := "collected garbage in " + d.data + ": 0 segments, 1 object files, 2 temporary files\n"
	if code != 0 || stdout != want || stderr != "" {
		t.Fatalf("whiteroom-db gc = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, want)
	}
	for _, name := range []string{orphan, filepath.Join(objects, ".tmp-1234"), filepath.Join(d.data, "logdb", ".tmp-checkpoint-1")} {
		if _, err := os.Lstat(name); !os.IsNotExist(err) {
			t.Errorf("%s after gc: %v, want it removed", name, err)
		}
	}
	code, stdout, stderr = runCmd(t, "gc", "-data", d.data)
	want = "collected garbage in " + d.data + ": 0 segments, 0 object files, 0 temporary files\n"
	if code != 0 || stdout != want || stderr != "" {
		t.Errorf("a second whiteroom-db gc = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, want)
	}

	// A daemon on the directory serves what it served before.
	s := d.startServe(t, d.args()...)
	r := openClient(t, d.socks, dbwire.RoleReader, dbclient.Options{})
	if after := stateOf(t, r, oh); !reflect.DeepEqual(after, before) {
		t.Errorf("the daemon after compact and gc serves\n%+v\nwant\n%+v", after, before)
	}
	got, err := r.Original(t.Context(), ohash)
	if err != nil {
		t.Fatalf("Original() = %v", err)
	}
	data, err := io.ReadAll(got)
	_ = got.Close()
	if err != nil || !bytes.Equal(data, original) {
		t.Errorf("the original after compact and gc: %d bytes (%v), want the %d that were put", len(data), err, len(original))
	}
	if err := r.Verify(t.Context()); err != nil {
		t.Errorf("Verify() after compact and gc = %v", err)
	}
	w := openClient(t, d.socks, dbwire.RoleWriter, dbclient.Options{})
	mustCommit(t, w, row{2, "a", "4"})
	s.stop()
}

// loginRecord returns a login record of a chain: the record n, whose prev is prev, with a member that the daemon ignores.
func loginRecord(n int, prev string) []byte {
	if prev == "" {
		return fmt.Appendf(nil, `{"log":"login","n":%d,"action":"x"}`, n)
	}
	return fmt.Appendf(nil, `{"log":"login","n":%d,"prev":%q,"action":"x"}`, n, prev)
}

func recordHash(data []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(data)) }

// loginChain returns the records 1 to n of a login history that chain.
func loginChain(n int) [][]byte {
	var recs [][]byte
	prev := ""
	for i := 1; i <= n; i++ {
		rec := loginRecord(i, prev)
		recs = append(recs, rec)
		prev = recordHash(rec)
	}
	return recs
}

// commitAt is a commit of the engine of a data directory: its events, then the mark of the daemon with the time at.
type commitAt struct {
	at     time.Time
	events [][]byte
}

// writeCommits writes the commits to the data directory data, which no process has open, straight into the engine, as a
// daemon whose clock was at those times has written them.
func writeCommits(t *testing.T, data string, commits ...commitAt) {
	t.Helper()
	srv, err := dbserver.OpenDir(data, storeOptions())
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range commits {
		if err := dbserver.EngineOf(srv).Update(t.Context(), func(tx *logdb.Tx) error {
			if i == 0 {
				if err := tx.Put(dbwire.TableMeta, []byte(dbwire.MetaSchema), binary.AppendUvarint(nil, dbwire.TableVersion)); err != nil {
					return err
				}
			}
			for _, e := range c.events {
				if err := tx.AppendEvent(e); err != nil {
					return err
				}
			}
			return tx.AppendEvent(dbwire.AppendMark(nil, c.at))
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
}

// setSchema puts the schema row of the data directory data, which no process has open, to version.
func setSchema(t *testing.T, data string, version uint64) {
	t.Helper()
	srv, err := dbserver.OpenDir(data, storeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := dbserver.EngineOf(srv).Update(t.Context(), func(tx *logdb.Tx) error {
		return tx.Put(dbwire.TableMeta, []byte(dbwire.MetaSchema), binary.AppendUvarint(nil, version))
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestCompactDeletesLogin checks the deletion of the login records older than one year, which compact does with the time of
// its process: its output, the store that it leaves, and what it does not drop.
func TestCompactDeletesLogin(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	months := func(n int) time.Time { return now.AddDate(0, -n, 0) }
	cutoff := now.AddDate(-1, 0, 0).Format(time.RFC3339) // the newest mark is that of now, which is not after the time of the process
	activity := []byte(`{"log":"activity","n":1,"action":"x"}`)

	t.Run("old records", func(t *testing.T) {
		d := newDirs(t)
		recs := loginChain(5)
		writeCommits(t, d.data,
			commitAt{months(40), [][]byte{activity, recs[0]}}, commitAt{months(30), [][]byte{recs[1]}},
			commitAt{months(13), [][]byte{recs[2]}}, commitAt{months(11), [][]byte{recs[3]}}, commitAt{now, [][]byte{recs[4]}})
		code, stdout, stderr := runCmd(t, "compact", "-data", d.data)
		want := "compacted " + d.data + "\ndeleted the login records 1 to 3, from before " + cutoff + "\n"
		if code != 0 || stdout != want || stderr != "" {
			t.Fatalf("whiteroom-db compact = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, want)
		}

		// A daemon serves the store without the records 1 to 3, and with the activity record, the start of the chain, and the
		// event of the deletion; the check of the directory finds nothing wrong.
		s := d.startServe(t, d.args()...)
		r := openClient(t, d.socks, dbwire.RoleReader, dbclient.Options{})
		var start dbwire.ChainStart
		var logins []int
		var activities, deletions, marks int
		if err := r.View(t.Context(), func(tx *dbclient.Tx) error {
			v, found, err := tx.Get(dbwire.TableChains, dbwire.ChainStartKey(dbwire.LogLogin))
			if err != nil {
				return err
			}
			if !found {
				return errors.New("the table chains has no chain start")
			}
			var ok bool
			if start, ok = dbwire.ParseChainStart(v); !ok {
				return errors.New("the chain start cannot be read")
			}
			return tx.Events(t.Context(), func(_ uint64, data []byte) bool {
				log, n, _, isRecord := dbwire.ReadRecordID(data)
				_, isMark := dbwire.ParseMark(data)
				_, isDeletion := dbwire.ParseDeleted(data)
				switch {
				case isRecord && log == dbwire.LogLogin:
					logins = append(logins, int(n))
				case isRecord:
					activities++
				case isMark:
					marks++
				case isDeletion:
					deletions++
				}
				return true
			})
		}); err != nil {
			t.Fatal(err)
		}
		if start.First != 4 || start.Prev != sha256.Sum256(recs[2]) {
			t.Errorf("the chain start is %+v, want record 4 after the hash of record 3", start)
		}
		if !slices.Equal(logins, []int{4, 5}) || activities != 1 || deletions != 1 || marks != 6 {
			t.Errorf("the store has the login records %v, %d activity records, %d deletions and %d marks, want 4 and 5, 1, 1 and 6",
				logins, activities, deletions, marks)
		}
		if err := r.Verify(t.Context()); err != nil {
			t.Errorf("Verify() = %v", err)
		}
		if code := s.stop(); code != 0 {
			t.Fatalf("whiteroom-db serve = %d", code)
		}
		if code, stdout, stderr := runCmd(t, "check", "-data", d.data); code != 0 || stdout != "no problems found in "+d.data+"\n" || stderr != "" {
			t.Errorf("whiteroom-db check = %d, with stdout %q and stderr %q", code, stdout, stderr)
		}

		// Nothing is old enough now: the output is that of a compaction.
		code, stdout, stderr = runCmd(t, "compact", "-data", d.data)
		if want := "compacted " + d.data + "\n"; code != 0 || stdout != want || stderr != "" {
			t.Errorf("a second whiteroom-db compact = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, want)
		}
	})

	t.Run("a store of an older table schema", func(t *testing.T) {
		d := newDirs(t)
		recs := loginChain(3)
		writeCommits(t, d.data, commitAt{months(40), [][]byte{activity, recs[0]}}, commitAt{months(30), [][]byte{recs[1]}},
			commitAt{now, [][]byte{recs[2]}})
		setSchema(t, d.data, dbwire.TableVersion-1)
		want := "compacted " + d.data + "\n" + fmt.Sprintf("kept the login records older than one year: the store has the table schema %d, "+
			"and the first writer that connects through whiteroom-db serve (whiteroom serve admin, for one) upgrades it to %d\n",
			dbwire.TableVersion-1, dbwire.TableVersion)
		for range 2 { // the second call finds the same store
			code, stdout, stderr := runCmd(t, "compact", "-data", d.data)
			if code != 0 || stdout != want || stderr != "" {
				t.Fatalf("whiteroom-db compact = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, want)
			}
		}
		s := d.startServe(t, d.args()...)
		r := openClient(t, d.socks, dbwire.RoleReader, dbclient.Options{})
		var logins []int
		if err := r.View(t.Context(), func(tx *dbclient.Tx) error {
			_, found, err := tx.Get(dbwire.TableChains, dbwire.ChainStartKey(dbwire.LogLogin))
			if err == nil && found {
				err = errors.New("the table chains of the store of the older schema has a chain start")
			}
			if err != nil {
				return err
			}
			return tx.Events(t.Context(), func(_ uint64, data []byte) bool {
				if log, n, _, ok := dbwire.ReadRecordID(data); ok && log == dbwire.LogLogin {
					logins = append(logins, int(n))
				}
				return true
			})
		}); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(logins, []int{1, 2, 3}) {
			t.Errorf("the login records are %v, want 1 to 3", logins)
		}
		s.stop()
	})

	t.Run("no old records", func(t *testing.T) {
		d := newDirs(t)
		recs := loginChain(3)
		writeCommits(t, d.data, commitAt{months(11), [][]byte{activity, recs[0]}}, commitAt{months(2), [][]byte{recs[1]}},
			commitAt{now, [][]byte{recs[2]}})
		code, stdout, stderr := runCmd(t, "compact", "-data", d.data)
		if want := "compacted " + d.data + "\n"; code != 0 || stdout != want || stderr != "" {
			t.Errorf("whiteroom-db compact = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, want)
		}
		s := d.startServe(t, d.args()...)
		r := openClient(t, d.socks, dbwire.RoleReader, dbclient.Options{})
		if err := r.View(t.Context(), func(tx *dbclient.Tx) error {
			_, found, err := tx.Get(dbwire.TableChains, dbwire.ChainStartKey(dbwire.LogLogin))
			if err == nil && found {
				err = errors.New("the table chains has a chain start")
			}
			return err
		}); err != nil {
			t.Error(err)
		}
		s.stop()
	})

	t.Run("a record that does not follow", func(t *testing.T) {
		d := newDirs(t)
		recs := loginChain(3)
		bad := loginRecord(3, recordHash(recs[0]))
		writeCommits(t, d.data, commitAt{months(40), [][]byte{recs[0]}}, commitAt{months(30), [][]byte{recs[1]}},
			commitAt{months(13), [][]byte{bad}}, commitAt{now, nil})
		stopped := "the deletion of login records stopped at record 3: prev is not the hash of record 2; check the store with whiteroom doctor\n"
		code, stdout, stderr := runCmd(t, "compact", "-data", d.data)
		want := "compacted " + d.data + "\ndeleted the login records 1 to 1, from before " + cutoff + "\n" + stopped
		if code != 0 || stdout != want || stderr != "" {
			t.Errorf("whiteroom-db compact = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, want)
		}
		// A second call deletes nothing and says it again.
		code, stdout, stderr = runCmd(t, "compact", "-data", d.data)
		want = "compacted " + d.data + "\n" + stopped
		if code != 0 || stdout != want || stderr != "" {
			t.Errorf("a second whiteroom-db compact = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, want)
		}
	})
}

// writeDeletion commits to the data directory data, which no process has open, the chain start before the record first, the
// event of the deletion that made it, which has cutoff, and a mark of at, as a deletion that has not compacted leaves them.
func writeDeletion(t *testing.T, data string, first uint64, prev [sha256.Size]byte, cutoff, at time.Time) {
	t.Helper()
	srv, err := dbserver.OpenDir(data, storeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := dbserver.EngineOf(srv).Update(t.Context(), func(tx *logdb.Tx) error {
		start := dbwire.ChainStart{First: first, Prev: prev, At: at}
		if err := tx.Put(dbwire.TableChains, dbwire.ChainStartKey(dbwire.LogLogin), dbwire.AppendChainStart(nil, start)); err != nil {
			return err
		}
		deleted := dbwire.Deleted{Log: dbwire.LogLogin, First: first, Prev: prev, Cutoff: cutoff}
		if err := tx.AppendEvent(dbwire.AppendDeleted(nil, deleted)); err != nil {
			return err
		}
		return tx.AppendEvent(dbwire.AppendMark(nil, at))
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestCompactDropsLeftovers checks that compact drops the login records that a deletion that stopped before its compaction has
// left, with the cutoff of that deletion on its line, and keeps them, and says where it stopped, if they are not exactly the
// records before the chain start.
func TestCompactDropsLeftovers(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	months := func(n int) time.Time { return now.AddDate(0, -n, 0) }
	recs := loginChain(4)
	build := func(t *testing.T, prev [sha256.Size]byte) dirs {
		d := newDirs(t)
		writeCommits(t, d.data, commitAt{months(30), [][]byte{[]byte(`{"log":"activity","n":1,"action":"x"}`), recs[0]}}, commitAt{months(25), [][]byte{recs[1]}},
			commitAt{months(2), [][]byte{recs[2]}}, commitAt{now, [][]byte{recs[3]}})
		writeDeletion(t, d.data, 3, prev, months(20), now)
		return d
	}
	t.Run("exactly the records before the chain start", func(t *testing.T) {
		d := build(t, sha256.Sum256(recs[1]))
		// The line says the cutoff of the deletion that left the records, which is not that of this call.
		want := "compacted " + d.data + "\ndeleted the login records 1 to 2, from before " + months(20).Format(time.RFC3339) + "\n"
		code, stdout, stderr := runCmd(t, "compact", "-data", d.data)
		if code != 0 || stdout != want || stderr != "" {
			t.Fatalf("whiteroom-db compact = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, want)
		}
		code, stdout, stderr = runCmd(t, "compact", "-data", d.data)
		if want := "compacted " + d.data + "\n"; code != 0 || stdout != want || stderr != "" {
			t.Errorf("a second whiteroom-db compact = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, want)
		}
	})
	t.Run("not the records before the chain start", func(t *testing.T) {
		d := build(t, sha256.Sum256([]byte("another record")))
		stopped := "the deletion of login records stopped at record 2: its hash is not the hash that the chain start has of it; " +
			"check the store with whiteroom doctor\n"
		for range 2 {
			code, stdout, stderr := runCmd(t, "compact", "-data", d.data)
			if want := "compacted " + d.data + "\n" + stopped; code != 0 || stdout != want || stderr != "" {
				t.Fatalf("whiteroom-db compact = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, want)
			}
		}
	})
}

// TestMaintainRefused checks that compact, gc and accept-break refuse a data directory that serve refuses, with the same words, and leave
// it as it was.
func TestMaintainRefused(t *testing.T) {
	d := newDirs(t)
	s := d.startServe(t, d.args()...)
	mustCommit(t, openClient(t, d.socks, dbwire.RoleWriter, dbclient.Options{}), row{2, "a", "1"})
	s.stop()

	// A store of a table schema that this program does not know.
	srv, err := dbserver.OpenDir(d.data, storeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := dbserver.EngineOf(srv).Update(t.Context(), func(tx *logdb.Tx) error {
		return tx.Put(dbwire.TableMeta, []byte(dbwire.MetaSchema), binary.AppendUvarint(nil, dbwire.TableVersion+1))
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	before := files(t, d.data)
	reason := fmt.Sprintf("%s: the store schema is of version %d, newer than this program knows (%d)\n",
		filepath.Join(d.data, "logdb"), dbwire.TableVersion+1, dbwire.TableVersion)
	for _, args := range [][]string{{"compact", "-data", d.data}, {"gc", "-data", d.data}, maintenanceArgs("accept-break", d.data),
		append([]string{"serve"}, d.args()...)} {
		code, stdout, stderr := runCmd(t, args...)
		if want := "whiteroom-db " + args[0] + ": " + reason; code != 1 || stdout != "" || stderr != want {
			t.Errorf("whiteroom-db %s of a newer schema = %d, with stdout %q and stderr %q, want 1, nothing and %q", args[0], code,
				stdout, stderr, want)
		}
	}
	if got := files(t, d.data); !maps.Equal(got, before) {
		t.Errorf("the commands that refused a newer schema changed the files:\n%v\nwas\n%v", got, before)
	}

	// A directory that is not a data directory, or not there, is left as it is.
	empty := filepath.Join(d.root, "empty")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(d.root, "missing")
	for _, command := range []string{"compact", "gc", "accept-break"} {
		code, stdout, stderr := runCmd(t, maintenanceArgs(command, empty)...)
		if want := "whiteroom-db " + command + ": " + filepath.Join(empty, "logdb") + ": missing directory\n"; code != 1 || stdout != "" || stderr != want {
			t.Errorf("whiteroom-db %s of an empty directory = %d, with stdout %q and stderr %q, want 1, nothing and %q", command, code,
				stdout, stderr, want)
		}
		code, stdout, stderr = runCmd(t, maintenanceArgs(command, missing)...)
		if code != 1 || stdout != "" || !strings.HasPrefix(stderr, "whiteroom-db "+command+": ") || !strings.Contains(stderr, missing) {
			t.Errorf("whiteroom-db %s of a missing directory = %d, with stdout %q and stderr %q, want 1 and the error", command, code,
				stdout, stderr)
		}
	}
	if entries, err := os.ReadDir(empty); err != nil || len(entries) != 0 {
		t.Errorf("compact, gc and accept-break made %v (%v) in an empty directory", entries, err)
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Errorf("compact, gc or accept-break made a missing directory: %v", err)
	}

	// A signal while they wait for a daemon ends them, as an interruption and not as a directory in use.
	d = newDirs(t)
	s = d.startServe(t, d.args()...)
	for _, command := range []string{"compact", "gc", "accept-break"} {
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(300*time.Millisecond, cancel)
		start := time.Now()
		code, stdout, stderr := runCmdIn(ctx, t, maintenanceArgs(command, d.data)...)
		cancel()
		if want := "whiteroom-db " + command + ": interrupted\n"; code != 1 || stdout != "" || stderr != want {
			t.Errorf("whiteroom-db %s interrupted = %d, with stdout %q and stderr %q, want 1, nothing and %q", command, code, stdout,
				stderr, want)
		}
		if took := time.Since(start); took > time.Second {
			t.Errorf("whiteroom-db %s took %v to end after the interruption", command, took)
		}
	}
	s.stop()
}
