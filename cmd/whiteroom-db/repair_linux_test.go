//go:build linux

package main

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// repairStore writes the commits 1 to n into the data directory data, which no process has open, as the daemon of a store that
// has audit records writes them: the commit i has the activity record i, the login record i/2 if i is even, the lines of the
// heads, and a mark; each commit has a segment of the log of its own, and the last has the checkpoint that Close writes.
func repairStore(t *testing.T, data string, n int) {
	t.Helper()
	repairStoreOf(t, data, n, dbwire.TableVersion)
}

// repairStoreOf is repairStore for a store of the table schema schema.
func repairStoreOf(t *testing.T, data string, n int, schema uint64) {
	t.Helper()
	o := storeOptions()
	o.Log.SegmentBytes = 1
	o.Log.CheckpointBytes = -1
	srv, err := dbserver.OpenDir(data, o)
	if err != nil {
		t.Fatal(err)
	}
	var act, login [][]byte
	prev := map[string]string{}
	next := func(log string, recs *[][]byte) (rec []byte, head string) {
		i := len(*recs) + 1
		if p := prev[log]; p == "" {
			rec = fmt.Appendf(nil, `{"log":%q,"n":%d,"at":"2026-01-01T00:00:00Z","action":"x"}`, log, i)
		} else {
			rec = fmt.Appendf(nil, `{"log":%q,"n":%d,"prev":%q,"at":"2026-01-01T00:00:00Z","action":"x"}`, log, i, p)
		}
		*recs = append(*recs, rec)
		prev[log] = recordHash(rec)
		return rec, fmt.Sprintf("audit head %s %d %s", log, i, prev[log])
	}
	for i := 1; i <= n; i++ {
		if err := dbserver.EngineOf(srv).Update(t.Context(), func(tx *logdb.Tx) error {
			if i == 1 {
				if err := tx.Put(dbwire.TableMeta, []byte(dbwire.MetaSchema), binary.AppendUvarint(nil, schema)); err != nil {
					return err
				}
			}
			rec, head := next(dbwire.LogActivity, &act)
			if err := tx.AppendEvent(rec); err != nil {
				return err
			}
			if err := tx.Put(dbwire.TableHeads, []byte(dbwire.LogActivity), []byte(head)); err != nil {
				return err
			}
			if i%2 == 0 {
				rec, head := next(dbwire.LogLogin, &login)
				if err := tx.AppendEvent(rec); err != nil {
					return err
				}
				if err := tx.Put(dbwire.TableHeads, []byte(dbwire.LogLogin), []byte(head)); err != nil {
					return err
				}
			}
			if err := tx.Put(2, []byte(fmt.Sprintf("row%d", i)), []byte("v")); err != nil {
				return err
			}
			return tx.AppendEvent(dbwire.AppendMark(nil, time.Now().UTC().Truncate(time.Second)))
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
}

// writeEvents commits the events, and a mark, to the data directory data, which no process has open.
func writeEvents(t *testing.T, data string, events ...[]byte) {
	t.Helper()
	srv, err := dbserver.OpenDir(data, storeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := dbserver.EngineOf(srv).Update(t.Context(), func(tx *logdb.Tx) error {
		for _, e := range events {
			if err := tx.AppendEvent(e); err != nil {
				return err
			}
		}
		return tx.AppendEvent(dbwire.AppendMark(nil, time.Now().UTC().Truncate(time.Second)))
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
}

func segmentFile(data string, commit int) string {
	return filepath.Join(data, "logdb", "log", fmt.Sprintf("0000000000000001-%016x.log", commit))
}

// fileBytes returns the regular files under root, with what they hold, by their paths from root.
func fileBytes(t testing.TB, root string) map[string]string {
	t.Helper()
	m := map[string]string{}
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		m[filepath.ToSlash(rel)] = string(data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return m
}

// holesOf returns what the data directory has of the holes: the keys of the rows of the table chains for them, and the words of
// the events of the daemon after the last record, "hole" for the event of a hole and "mark" for a mark.
func holesOf(t *testing.T, data string) (rows []string, events []string) {
	t.Helper()
	err := dbserver.Maintain(t.Context(), data, storeOptions(), func(m *logdb.Maintenance) error {
		if err := m.View(t.Context(), func(tx *logdb.Tx) error {
			for _, log := range []string{dbwire.LogLogin, dbwire.LogActivity} {
				tx.Scan(dbwire.TableChains, dbwire.ChainHolePrefix(log), func(k, v []byte) bool {
					name, first, ok := dbwire.ParseChainHoleKey(k)
					if h, valid := dbwire.ParseChainHole(first, v); ok && valid {
						rows = append(rows, fmt.Sprintf("%s %d-%d", name, first, h.Last))
					}
					return true
				})
			}
			return nil
		}); err != nil {
			return err
		}
		return m.Events(t.Context(), func(e logdb.Event) bool {
			switch _, isHole := dbwire.ParseHole(e.Data); {
			case isHole:
				events = append(events, "hole")
			case dbwire.IsDaemonEvent(e.Data):
				events = append(events, "mark")
			default:
				events = append(events, "record")
			}
			return true
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return rows, events
}

// The lines of a repair of the store that repairStore makes, with the commits 4 and 8 damaged: each of the records of those
// commits is a hole, and the checkpoint covers them.
func damagedLines(data string) []string {
	return []string{
		"drop: engine: " + segmentFile(data, 4) + " at offset 64: bad record CRC (sequence 4, events 8 to 10)",
		"drop: engine: " + segmentFile(data, 8) + " at offset 64: bad record CRC (sequence 8, events 18 to 20)",
		`hole: audit log "login": record 2; recorded in the table chains`,
		`hole: audit log "login": record 4; recorded in the table chains`,
		`hole: audit log "activity": record 4; recorded in the table chains`,
		`hole: audit log "activity": record 8; recorded in the table chains`,
	}
}

func text(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

func TestRepair(t *testing.T) {
	d := newDirs(t)
	repairStore(t, d.data, 8)
	editFile(t, segmentFile(d.data, 4), flipByte(100))
	editFile(t, segmentFile(d.data, 8), flipByte(100))
	copied := cloneTree(t, d.data)
	keep := filepath.Join(d.root, "keep")
	before := files(t, d.data)
	lines := damagedLines(d.data)

	if code, stdout, _ := runCmd(t, "check", "-data", d.data); code != 1 || !strings.Contains(stdout, "engine: ") {
		t.Fatalf("whiteroom-db check of the damaged directory = %d, with stdout %q, want 1 and the damage", code, stdout)
	}

	// Shown, it changes nothing, with the flags too, which change what is shown only if they are of use.
	show := text(append(lines, fmt.Sprintf("2 things to repair in %s; whiteroom-db repair -apply -keep DIR repairs them", d.data))...)
	for _, args := range [][]string{{"repair", "-data", d.data}, {"repair", "-data", d.data, "-truncate"}} {
		code, stdout, stderr := runCmd(t, args...)
		if code != 0 || stdout != show || stderr != "" {
			t.Errorf("whiteroom-db %v = %d, with stdout:\n%s\nand stderr %q\nwant 0, with stdout:\n%s", args[1:], code, stdout, stderr, show)
		}
	}
	// With the config file for the data directory.
	cfg := writeFile(t, d.root, "config.yaml", "data: "+d.data+"\n")
	if code, stdout, stderr := runCmd(t, "repair", "-config", cfg); code != 0 || stdout != show || stderr != "" {
		t.Errorf("whiteroom-db repair -config = %d, with stdout %q and stderr %q, want 0 and the lines of the repair", code, stdout, stderr)
	}
	if got := files(t, d.data); !maps.Equal(got, before) {
		t.Errorf("the repair that is shown changed the files:\n%v\nwas\n%v", got, before)
	}
	if _, err := os.Lstat(keep); !os.IsNotExist(err) {
		t.Errorf("the repair that is shown made %s: %v", keep, err)
	}

	// Applied: the copy first, and then what it dropped, and then the generation.
	code, stdout, stderr := runCmd(t, "repair", "-data", d.data, "-apply", "-keep", keep)
	want := text(append(append([]string{fmt.Sprintf("kept the files of %s in %s", filepath.Join(d.data, "logdb"), filepath.Join(keep, "logdb"))},
		lines...), fmt.Sprintf("repaired %s: generation 2, sequence 8", d.data))...)
	if code != 0 || stdout != want || stderr != "" {
		t.Fatalf("whiteroom-db repair -apply = %d, with stdout:\n%s\nand stderr %q\nwant 0, with stdout:\n%s", code, stdout, stderr, want)
	}
	if got, was := fileBytes(t, filepath.Join(keep, "logdb")), fileBytes(t, filepath.Join(copied, "logdb")); !maps.Equal(got, was) {
		t.Errorf("the copy has %d files, want those of logdb/ as they were (%d), byte for byte", len(got), len(was))
	}
	if info, err := os.Stat(keep); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the directory of the copy is %v, %v, want one with the mode 0700", info, err)
	}
	rows, events := holesOf(t, d.data)
	if strings.Join(rows, ",") != "login 2-2,login 4-4,activity 4-4,activity 8-8" {
		t.Errorf("the rows of the holes are %v", rows)
	}
	if tail := strings.Join(events[len(events)-5:], " "); tail != "hole hole hole hole mark" {
		t.Errorf("the events end with %q, want the events of the four holes and a mark", tail)
	}

	// Nothing is left to repair or to find, the damage is in the copy, and the daemon serves what the repair kept.
	for _, args := range [][]string{{"repair", "-data", d.data}, {"repair", "-data", d.data, "-apply", "-keep", filepath.Join(d.root, "keep2")}} {
		code, stdout, stderr = runCmd(t, args...)
		if want := "nothing to repair in " + d.data + "\n"; code != 0 || stdout != want || stderr != "" {
			t.Errorf("whiteroom-db %v after the repair = %d, with stdout %q and stderr %q, want 0 and %q", args[1:], code, stdout, stderr, want)
		}
	}
	if _, err := os.Lstat(filepath.Join(d.root, "keep2")); !os.IsNotExist(err) {
		t.Errorf("the repair that had nothing to do made its copy: %v", err)
	}
	if code, stdout, stderr = runCmd(t, "check", "-data", d.data); code != 0 || stdout != "no problems found in "+d.data+"\n" || stderr != "" {
		t.Errorf("whiteroom-db check after the repair = %d, with stdout %q and stderr %q, want it to find nothing", code, stdout, stderr)
	}
	kd := filepath.Join(d.root, "kept")
	if err := os.CopyFS(filepath.Join(kd, "logdb"), os.DirFS(filepath.Join(keep, "logdb"))); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(kd, "files"), 0o700); err != nil {
		t.Fatal(err)
	}
	if code, stdout, _ = runCmd(t, "check", "-data", kd); code != 1 || !strings.Contains(stdout, "engine: ") {
		t.Errorf("whiteroom-db check of the copy = %d, with stdout %q, want 1 and the damage again", code, stdout)
	}
	s := d.startServe(t, d.args()...)
	mustCommit(t, openClient(t, d.socks, dbwire.RoleWriter, dbclient.Options{}), row{2, "after", "1"})
	if v, found := value(t, openClient(t, d.socks, dbwire.RoleReader, dbclient.Options{}), "row3"); !found || v != "v" {
		t.Errorf("the daemon serves %q, %v for a row that the repair kept", v, found)
	}
	if code := s.stop(); code != 0 {
		t.Fatalf("whiteroom-db serve = %d", code)
	}
	if code, stdout, stderr = runCmd(t, "check", "-data", d.data); code != 0 {
		t.Errorf("whiteroom-db check after the daemon has run = %d, with stdout %q and stderr %q", code, stdout, stderr)
	}
}

// TestRepairRaisesSchema checks that a repair that records holes in a store of the table schema 8 or 9 raises it to the
// schema 10, tells so, and that a store of the schema 10 is left as it is.
func TestRepairRaisesSchema(t *testing.T) {
	schemaOf := func(t *testing.T, data string) uint64 {
		t.Helper()
		var version uint64
		err := dbserver.Maintain(t.Context(), data, storeOptions(), func(m *logdb.Maintenance) error {
			return m.View(t.Context(), func(tx *logdb.Tx) error {
				v, _ := tx.Get(dbwire.TableMeta, []byte(dbwire.MetaSchema))
				version, _ = binary.Uvarint(v)
				return nil
			})
		})
		if err != nil {
			t.Fatal(err)
		}
		return version
	}
	for _, from := range []uint64{8, 9, 10} {
		t.Run(fmt.Sprintf("schema %d", from), func(t *testing.T) {
			d := newDirs(t)
			repairStoreOf(t, d.data, 4, from)
			editFile(t, segmentFile(d.data, 4), flipByte(100))
			lines := []string{
				"kept the files of " + filepath.Join(d.data, "logdb") + " in " + filepath.Join(d.root, "keep", "logdb"),
				"drop: engine: " + segmentFile(d.data, 4) + " at offset 64: bad record CRC (sequence 4, events 8 to 10)",
				`hole: audit log "login": record 2; recorded in the table chains`,
				`hole: audit log "activity": record 4; recorded in the table chains`,
			}
			if from < dbwire.TableVersion {
				lines = append(lines, fmt.Sprintf("schema: the table schema %d is raised to %d, which has the rows of the holes", from,
					dbwire.TableVersion))
			}
			lines = append(lines, fmt.Sprintf("repaired %s: generation 2, sequence 4", d.data))
			want := text(lines...)
			code, stdout, stderr := runCmd(t, "repair", "-data", d.data, "-apply", "-keep", filepath.Join(d.root, "keep"))
			if code != 0 || stdout != want || stderr != "" {
				t.Fatalf("whiteroom-db repair -apply = %d, with stdout:\n%s\nand stderr %q\nwant 0, with stdout:\n%s", code, stdout, stderr, want)
			}
			if got := schemaOf(t, d.data); got != dbwire.TableVersion {
				t.Errorf("the table schema after the repair is %d, want %d", got, dbwire.TableVersion)
			}
			if rows, _ := holesOf(t, d.data); strings.Join(rows, ",") != "login 2-2,activity 4-4" {
				t.Errorf("the rows of the holes are %v", rows)
			}
		})
	}
}

func TestRepairTruncate(t *testing.T) {
	d := newDirs(t)
	repairStore(t, d.data, 8)
	if err := os.Remove(filepath.Join(d.data, "logdb", "checkpoint")); err != nil {
		t.Fatal(err)
	}
	editFile(t, segmentFile(d.data, 4), flipByte(100))
	keep := filepath.Join(d.root, "keep")
	before := files(t, d.data)
	damage := fmt.Sprintf("%s at offset 64: bad record CRC", segmentFile(d.data, 4))
	span := "drop: engine: " + damage + " (sequence 4, events 8 to 10)"
	loseText := "lose: the commits 5 to 8 after " + damage

	// Shown, with and without the flag.
	code, stdout, stderr := runCmd(t, "repair", "-data", d.data)
	want := text(span, loseText+"; only with -truncate",
		fmt.Sprintf("2 things to repair in %s; whiteroom-db repair -apply -keep DIR -truncate repairs them", d.data))
	if code != 0 || stdout != want || stderr != "" {
		t.Errorf("whiteroom-db repair = %d, with stdout:\n%s\nand stderr %q\nwant 0, with stdout:\n%s", code, stdout, stderr, want)
	}
	code, stdout, stderr = runCmd(t, "repair", "-data", d.data, "-truncate")
	want = text(span, loseText, fmt.Sprintf("2 things to repair in %s; whiteroom-db repair -apply -keep DIR -truncate repairs them", d.data))
	if code != 0 || stdout != want || stderr != "" {
		t.Errorf("whiteroom-db repair -truncate = %d, with stdout:\n%s\nand stderr %q\nwant 0, with stdout:\n%s", code, stdout, stderr, want)
	}

	// Applied without it, it refuses, and changes nothing, and makes no copy.
	code, stdout, stderr = runCmd(t, "repair", "-data", d.data, "-apply", "-keep", keep)
	wantErr := fmt.Sprintf("whiteroom-db repair: the commits 5 to 8 come after the damage at %s offset 64 (bad record CRC): repair with "+
		"-truncate to drop them, or restore the store from a copy\n", segmentFile(d.data, 4))
	if code != 1 || stdout != text(span, loseText+"; only with -truncate") || stderr != wantErr {
		t.Errorf("whiteroom-db repair -apply = %d, with stdout %q and stderr %q, want 1, the lines of what it would drop and %q", code,
			stdout, stderr, wantErr)
	}
	if got := files(t, d.data); !maps.Equal(got, before) {
		t.Errorf("the refusal changed the files:\n%v\nwas\n%v", got, before)
	}
	if _, err := os.Lstat(keep); !os.IsNotExist(err) {
		t.Errorf("the refusal made %s: %v", keep, err)
	}

	code, stdout, stderr = runCmd(t, "repair", "-data", d.data, "-apply", "-keep", keep, "-truncate")
	want = text("kept the files of "+filepath.Join(d.data, "logdb")+" in "+filepath.Join(keep, "logdb"), span, loseText,
		fmt.Sprintf("repaired %s: generation 2, sequence 3", d.data))
	if code != 0 || stdout != want || stderr != "" {
		t.Errorf("whiteroom-db repair -apply -truncate = %d, with stdout:\n%s\nand stderr %q\nwant 0, with stdout:\n%s", code, stdout, stderr, want)
	}
	if code, stdout, _ = runCmd(t, "repair", "-data", d.data); code != 0 || stdout != "nothing to repair in "+d.data+"\n" {
		t.Errorf("whiteroom-db repair after the repair = %d, with stdout %q, want nothing to repair", code, stdout)
	}
	if rows, _ := holesOf(t, d.data); len(rows) != 0 {
		t.Errorf("the repair that drops the commits after the damage recorded the holes %v: the records are not lost, but dropped", rows)
	}
}

func TestRepairUnreadable(t *testing.T) {
	d := newDirs(t)
	repairStore(t, d.data, 8)
	writeEvents(t, d.data, []byte("neither a record nor an event of the daemon"))
	keep := filepath.Join(d.root, "keep")
	before := files(t, d.data)

	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, text("unreadable: event 21", "nothing to repair in "+d.data)},
		{[]string{"-truncate"}, text("unreadable: event 21", "nothing to repair in "+d.data)},
		{[]string{"-drop-unreadable-records"}, text("unreadable: event 21; dropped",
			fmt.Sprintf("1 thing to repair in %s; whiteroom-db repair -apply -keep DIR -drop-unreadable-records repairs it", d.data))},
	} {
		code, stdout, stderr := runCmd(t, append([]string{"repair", "-data", d.data}, tc.args...)...)
		if code != 0 || stdout != tc.want || stderr != "" {
			t.Errorf("whiteroom-db repair %v = %d, with stdout:\n%s\nand stderr %q\nwant 0, with stdout:\n%s", tc.args, code, stdout, stderr, tc.want)
		}
	}
	// Applied without the flag the event is kept, and nothing is done.
	code, stdout, stderr := runCmd(t, "repair", "-data", d.data, "-apply", "-keep", keep)
	if want := text("unreadable: event 21", "nothing to repair in "+d.data); code != 0 || stdout != want || stderr != "" {
		t.Errorf("whiteroom-db repair -apply = %d, with stdout %q and stderr %q, want 0 and %q", code, stdout, stderr, want)
	}
	if got := files(t, d.data); !maps.Equal(got, before) {
		t.Errorf("the repair that keeps the event changed the files")
	}
	if _, err := os.Lstat(keep); !os.IsNotExist(err) {
		t.Errorf("the repair that had nothing to do made %s: %v", keep, err)
	}

	code, stdout, stderr = runCmd(t, "repair", "-data", d.data, "-apply", "-keep", keep, "-drop-unreadable-records")
	want := text("kept the files of "+filepath.Join(d.data, "logdb")+" in "+filepath.Join(keep, "logdb"), "unreadable: event 21; dropped",
		fmt.Sprintf("repaired %s: generation 2, sequence 9", d.data))
	if code != 0 || stdout != want || stderr != "" {
		t.Fatalf("whiteroom-db repair -apply -drop-unreadable-records = %d, with stdout:\n%s\nand stderr %q\nwant 0, with stdout:\n%s", code, stdout, stderr, want)
	}
	if code, stdout, _ = runCmd(t, "repair", "-data", d.data); code != 0 || stdout != "nothing to repair in "+d.data+"\n" {
		t.Errorf("whiteroom-db repair after the repair = %d, with stdout %q, want nothing to repair", code, stdout)
	}
}

func TestRepairKeepRefused(t *testing.T) {
	d := newDirs(t)
	repairStore(t, d.data, 8)
	editFile(t, segmentFile(d.data, 4), flipByte(100))
	before := files(t, d.data)
	full := filepath.Join(d.root, "full")
	if err := os.Mkdir(full, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, full, "x", "x")
	linked := filepath.Join(d.root, "linked")
	if err := os.Mkdir(linked, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d.root, "link")
	if err := os.Symlink(linked, link); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ keep, want string }{
		{full, full + ": not empty; -keep needs a missing or empty directory"},
		{link, link + ": a symbolic link; -keep needs a missing or empty directory itself, not a link to one"},
		{d.data, d.data + ": -keep is the data directory: the copy of logdb/ must be kept apart from it"},
		{filepath.Join(d.data, "keep"), filepath.Join(d.data, "keep") + ": -keep is in the data directory: the copy of logdb/ must be kept apart from it"},
	} {
		code, stdout, stderr := runCmd(t, "repair", "-data", d.data, "-apply", "-keep", tc.keep)
		if code != 1 || stdout != "" || stderr != "whiteroom-db repair: "+tc.want+"\n" {
			t.Errorf("whiteroom-db repair -apply -keep %s = %d, with stdout %q and stderr %q, want 1, nothing and %q", tc.keep, code, stdout,
				stderr, tc.want)
		}
	}
	if got := files(t, d.data); !maps.Equal(got, before) {
		t.Errorf("the refusals changed the files:\n%v\nwas\n%v", got, before)
	}
	if got := fileBytes(t, full); len(got) != 1 {
		t.Errorf("the refusal changed the directory that was not empty: %v", got)
	}
	if got := fileBytes(t, linked); len(got) != 0 {
		t.Errorf("the refusal of the link wrote in what it points to: %v", got)
	}
	// An empty directory takes the copy.
	empty := filepath.Join(d.root, "empty")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCmd(t, "repair", "-data", d.data, "-apply", "-keep", empty); code != 0 {
		t.Fatalf("whiteroom-db repair -apply -keep with an empty directory = %d, with stderr %q", code, stderr)
	}
	if got := fileBytes(t, filepath.Join(empty, "logdb")); len(got) == 0 {
		t.Error("the empty directory has no copy")
	}
}

// TestRepairInUse checks that repair waits for about two seconds for a daemon that has the data directory open, with and
// without -apply, then fails with the way out, and changes nothing, and that the daemon still serves.
func TestRepairInUse(t *testing.T) {
	d := newDirs(t)
	s := d.startServe(t, d.args()...)
	w := openClient(t, d.socks, dbwire.RoleWriter, dbclient.Options{})
	mustCommit(t, w, row{2, "a", "1"})
	before := files(t, d.data)
	keep := filepath.Join(d.root, "keep")
	for _, args := range [][]string{{"repair", "-data", d.data}, {"repair", "-data", d.data, "-apply", "-keep", keep}} {
		start := time.Now()
		code, stdout, stderr := runCmd(t, args...)
		want := fmt.Sprintf("whiteroom-db repair: %s: the data directory is in use; stop whiteroom-db serve on it first\n", d.data)
		if code != 1 || stdout != "" || stderr != want {
			t.Errorf("whiteroom-db %v on a directory in use = %d, with stdout %q and stderr %q, want 1, nothing and %q", args[1:], code, stdout,
				stderr, want)
		}
		if took := time.Since(start); took < 1500*time.Millisecond || took > 8*time.Second {
			t.Errorf("whiteroom-db %v on a directory in use took %v, want about 2 seconds", args[1:], took)
		}
		if got := files(t, d.data); !maps.Equal(got, before) {
			t.Errorf("whiteroom-db %v on a directory in use changed the files", args[1:])
		}
		if _, err := os.Lstat(keep); !os.IsNotExist(err) {
			t.Errorf("whiteroom-db %v on a directory in use made %s: %v", args[1:], keep, err)
		}
	}
	mustCommit(t, w, row{2, "b", "2"}) // the writer still commits
	if code := s.stop(); code != 0 {
		t.Fatalf("whiteroom-db serve = %d", code)
	}
}

// TestRepairLines checks the lines of what a repair recovers, removes and drops that are no loss of records: a torn tail that
// Open would cut off, a file that is left, an object that cannot be read.
func TestRepairLines(t *testing.T) {
	t.Run("recover and remove", func(t *testing.T) {
		d := newDirs(t)
		repairStore(t, d.data, 4)
		if err := os.Remove(filepath.Join(d.data, "logdb", "checkpoint")); err != nil {
			t.Fatal(err)
		}
		last := segmentFile(d.data, 4)
		editFile(t, last, func(b []byte) []byte { return b[:len(b)-10] })
		temp := filepath.Join(d.data, "logdb", ".tmp-checkpoint-0123456789abcdef")
		writeFile(t, filepath.Dir(temp), filepath.Base(temp), "x")
		keep := filepath.Join(d.root, "keep")
		code, stdout, stderr := runCmd(t, "repair", "-data", d.data)
		if code != 0 || stderr != "" || !strings.Contains(stdout, "recover: engine: "+last+" at offset ") ||
			!strings.Contains(stdout, "a torn tail, which Open would cut off\n") || !strings.Contains(stdout, "remove: "+temp+"\n") ||
			!strings.HasSuffix(stdout, "things to repair in "+d.data+"; whiteroom-db repair -apply -keep DIR repairs them\n") {
			t.Errorf("whiteroom-db repair = %d, with stdout:\n%s\nand stderr %q, want the lines of the torn tail and the file", code, stdout, stderr)
		}
		code, stdout, stderr = runCmd(t, "repair", "-data", d.data, "-apply", "-keep", keep)
		if code != 0 || stderr != "" || !strings.HasSuffix(stdout, fmt.Sprintf("repaired %s: generation 2, sequence 3\n", d.data)) {
			t.Errorf("whiteroom-db repair -apply = %d, with stdout:\n%s\nand stderr %q", code, stdout, stderr)
		}
		if _, err := os.Lstat(temp); !os.IsNotExist(err) {
			t.Errorf("the file that was left is still there: %v", err)
		}
		if code, stdout, _ = runCmd(t, "check", "-data", d.data); code != 0 {
			t.Errorf("whiteroom-db check after the repair = %d, with stdout %q", code, stdout)
		}
	})

	// A record that was lost before the repair is a gap that it does not make: the line names the command that accepts it, with the
	// number of the record after the gap, or, for a gap at the end of the log, the number of its head.
	t.Run("a gap in the middle", func(t *testing.T) {
		d := newDirs(t)
		repairStore(t, d.data, 4)
		// The records 1 to 4 of the activity log are there, and the record 6 names a record 5 that is not.
		lost := sha256.Sum256([]byte("the record 5"))
		rec := fmt.Appendf(nil, `{"log":"activity","n":6,"prev":"sha256:%x","at":"2026-01-01T00:00:00Z","action":"x"}`, lost)
		writeEvents(t, d.data, rec)
		code, stdout, stderr := runCmd(t, "repair", "-data", d.data)
		want := `gap: audit log "activity": record 5, which this repair does not make: whiteroom doctor reports it until it is accepted ` +
			"with whiteroom-db accept-break -log activity -record 6\n" + "nothing to repair in " + d.data + "\n"
		if code != 0 || stderr != "" || stdout != want {
			t.Errorf("whiteroom-db repair = %d, with stdout:\n%s\nand stderr %q, want\n%s", code, stdout, stderr, want)
		}
		// whiteroom-db accept-break accepts it, and the repair has no gap to tell.
		if code, _, stderr := runCmd(t, "accept-break", "-data", d.data, "-log", "activity", "-record", "6"); code != 0 || stderr != "" {
			t.Fatalf("whiteroom-db accept-break = %d, with stderr %q", code, stderr)
		}
		if code, stdout, stderr := runCmd(t, "repair", "-data", d.data); code != 0 || stderr != "" || stdout != "nothing to repair in "+d.data+"\n" {
			t.Errorf("whiteroom-db repair after the acceptance = %d, with stdout %q and stderr %q", code, stdout, stderr)
		}
	})
	t.Run("a gap at the end", func(t *testing.T) {
		d := newDirs(t)
		repairStore(t, d.data, 4)
		// The head of the activity log names the record 6, and the log has the records 1 to 4.
		hash := sha256.Sum256([]byte("the record 6"))
		putHeads(t, d.data, map[string]uint64{"activity": 6}, map[string][sha256.Size]byte{"activity": hash})
		code, stdout, stderr := runCmd(t, "repair", "-data", d.data)
		want := `gap: audit log "activity": records 5 to 6, which this repair does not make: whiteroom doctor reports it until it is ` +
			"accepted with whiteroom-db accept-break -log activity -record 6\n" + "nothing to repair in " + d.data + "\n"
		if code != 0 || stderr != "" || stdout != want {
			t.Errorf("whiteroom-db repair = %d, with stdout:\n%s\nand stderr %q, want\n%s", code, stdout, stderr, want)
		}
		if code, _, stderr := runCmd(t, "accept-break", "-data", d.data, "-log", "activity", "-record", "6"); code != 0 || stderr != "" {
			t.Fatalf("whiteroom-db accept-break = %d, with stderr %q", code, stderr)
		}
		if code, stdout, stderr := runCmd(t, "repair", "-data", d.data); code != 0 || stderr != "" || stdout != "nothing to repair in "+d.data+"\n" {
			t.Errorf("whiteroom-db repair after the acceptance = %d, with stdout %q and stderr %q", code, stdout, stderr)
		}
	})

	// A gap that accept-break refuses is told without the command, which would send the operator to a refusal.
	t.Run("a gap that accept-break refuses", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Second)
		recs := loginChain(5)
		for _, tc := range []struct {
			name  string
			build func(t *testing.T, data string)
			line  string
		}{
			{"a gap among the records that a deletion left", func(t *testing.T, data string) {
				// The records 1, 3 and 4 are there, the chain starts at the record 5, and the record 2 is lost.
				writeCommits(t, data, commitAt{now, [][]byte{recs[0], recs[2], recs[3]}})
				writeStart(t, data, 5, sha256.Sum256(recs[3]), now)
			}, `gap: audit log "login": record 2, which this repair does not make: whiteroom doctor goes on reporting it`},
			{"a record whose prev is no hash", func(t *testing.T, data string) {
				odd := fmt.Appendf(nil, `{"log":"login","n":4,"prev":"nonsense","at":"2026-01-01T00:00:00Z","action":"x"}`)
				writeCommits(t, data, commitAt{now, [][]byte{recs[0], recs[1], odd}})
			}, `gap: audit log "login": record 3, which this repair does not make: whiteroom doctor goes on reporting it`},
			{"a gap across the chain start from a record with another hash", func(t *testing.T, data string) {
				// The record 2 is the one before the chain start, and the chain start has the hash of another record.
				writeCommits(t, data, commitAt{now, [][]byte{recs[0], recs[1], recs[3]}})
				writeStart(t, data, 3, sha256.Sum256([]byte("another record")), now)
			}, `gap: audit log "login": record 3, which this repair does not make: whiteroom doctor goes on reporting it`},
			{"a gap at the end of an activity log that has no record", func(t *testing.T, data string) {
				writeCommits(t, data, commitAt{now, nil}) // the schema of the store
				hash := sha256.Sum256([]byte("the record 3"))
				putHeads(t, data, map[string]uint64{"activity": 3}, map[string][sha256.Size]byte{"activity": hash})
			}, `gap: audit log "activity": records 1 to 3, which this repair does not make: whiteroom doctor goes on reporting it`},
			{"a gap at the end of a login history that has no record", func(t *testing.T, data string) {
				writeCommits(t, data, commitAt{now, nil}) // the schema of the store
				hash := sha256.Sum256([]byte("the record 3"))
				putHeads(t, data, map[string]uint64{"login": 3}, map[string][sha256.Size]byte{"login": hash})
			}, `gap: audit log "login": records 1 to 3, which this repair does not make: whiteroom doctor reports it until it is accepted ` +
				"with whiteroom-db accept-break -log login -record 3"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				d := newDirs(t)
				tc.build(t, d.data)
				code, stdout, stderr := runCmd(t, "repair", "-data", d.data)
				want := tc.line + "\n" + "nothing to repair in " + d.data + "\n"
				if code != 0 || stderr != "" || stdout != want {
					t.Errorf("whiteroom-db repair = %d, with stdout:\n%s\nand stderr %q, want\n%s", code, stdout, stderr, want)
				}
			})
		}
	})

	t.Run("an object", func(t *testing.T) {
		d := newDirs(t)
		repairStore(t, d.data, 3)
		srv, err := dbserver.OpenDir(d.data, storeOptions())
		if err != nil {
			t.Fatal(err)
		}
		var h [32]byte
		if err := dbserver.EngineOf(srv).Update(t.Context(), func(tx *logdb.Tx) error {
			var err error
			if h, err = tx.PutObject([]byte(strings.Repeat("an object that is a file ", 6000))); err != nil {
				return err
			}
			return tx.Put(2, []byte("object"), h[:])
		}); err != nil {
			t.Fatal(err)
		}
		if err := srv.Close(); err != nil {
			t.Fatal(err)
		}
		obj := filepath.Join(d.data, "logdb", "objects", fmt.Sprintf("%x", h[:1]), fmt.Sprintf("%x", h))
		editFile(t, obj, flipByte(-1))
		code, stdout, stderr := runCmd(t, "repair", "-data", d.data)
		prefix := fmt.Sprintf("drop: object %x: ", h)
		if code != 0 || stderr != "" || !strings.HasPrefix(stdout, prefix+obj) || strings.Count(stdout, "\n") != 2 ||
			!strings.Contains(stdout, "the data has the hash") || strings.Contains(stdout, fmt.Sprintf("object %x: object", h)) ||
			!strings.HasSuffix(stdout, "1 thing to repair in "+d.data+"; whiteroom-db repair -apply -keep DIR repairs it\n") {
			t.Errorf("whiteroom-db repair = %d, with stdout:\n%s\nand stderr %q, want the line of the object", code, stdout, stderr)
		}
		if code, stdout, stderr = runCmd(t, "repair", "-data", d.data, "-apply", "-keep", filepath.Join(d.root, "keep")); code != 0 ||
			!strings.HasSuffix(stdout, fmt.Sprintf("repaired %s: generation 2, sequence 4\n", d.data)) {
			t.Errorf("whiteroom-db repair -apply = %d, with stdout:\n%s\nand stderr %q", code, stdout, stderr)
		}
		if _, err := os.Lstat(obj); !os.IsNotExist(err) {
			t.Errorf("the file of the object that was dropped is still there: %v", err)
		}
	})
}
