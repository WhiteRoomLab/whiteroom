//go:build linux

package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// maintenanceArgs returns the arguments of the maintenance command named command on the data directory data: the arguments
// that accept-break needs are those of a record that a store with no record refuses, and repair is the one that changes nothing.
func maintenanceArgs(command, data string) []string {
	if command == "accept-break" {
		return []string{command, "-data", data, "-log", "login", "-record", "2"}
	}
	return []string{command, "-data", data}
}

// withoutHead returns the files of the data directory dir, except the head of the engine, which each open of the engine
// writes again.
func withoutHead(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := files(t, dir)
	delete(m, "logdb/head")
	return m
}

// auditChain returns the records 1 to n of the audit log named log that chain.
func auditChain(log string, n int) [][]byte {
	var recs [][]byte
	prev := ""
	for i := 1; i <= n; i++ {
		rec := fmt.Appendf(nil, `{"log":%q,"n":%d,"at":"2026-01-01T00:00:00Z","action":"x"}`, log, i)
		if prev != "" {
			rec = fmt.Appendf(nil, `{"log":%q,"n":%d,"prev":%q,"at":"2026-01-01T00:00:00Z","action":"x"}`, log, i, prev)
		}
		recs = append(recs, rec)
		prev = recordHash(rec)
	}
	return recs
}

// breakStore writes the records 1 to 5 of the login history and of the activity log into the data directory d.data, with the
// record 3 of each changed: the record 4 of each is the break. The commits have the marks of now. It returns the records
// that the logs have by their names, with the record 4 as it is.
func breakStore(t *testing.T, d dirs) map[string][][]byte {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	chains := map[string][][]byte{"login": auditChain("login", 5), "activity": auditChain("activity", 5)}
	var commits []commitAt
	for i := range 5 {
		var events [][]byte
		for _, log := range []string{"login", "activity"} {
			rec := chains[log][i]
			if i == 2 {
				rec = bytes.Replace(rec, []byte(`"x"`), []byte(`"changed"`), 1)
			}
			events = append(events, rec)
		}
		commits = append(commits, commitAt{now, events})
	}
	writeCommits(t, d.data, commits...)
	return chains
}

// TestAcceptBreak checks accept-break on a data directory: the line that it prints, the row and the events that it leaves,
// which a daemon serves, the refusals, which change nothing, and that the check of the directory finds nothing wrong.
func TestAcceptBreak(t *testing.T) {
	d := newDirs(t)
	chains := breakStore(t, d)
	refused := func(args []string, text string) {
		t.Helper()
		before := withoutHead(t, d.data)
		code, stdout, stderr := runCmd(t, args...)
		if want := "whiteroom-db accept-break: " + text + "\n"; code != 1 || stdout != "" || stderr != want {
			t.Errorf("whiteroom-db %v = %d, with stdout %q and stderr %q, want 1, nothing and %q", args[1:], code, stdout, stderr, want)
		}
		if got := withoutHead(t, d.data); !maps.Equal(got, before) {
			t.Errorf("the refusal of whiteroom-db %v changed the files:\n%v\nwas\n%v", args[1:], got, before)
		}
	}
	accept := func(log, n string) []string {
		return []string{"accept-break", "-data", d.data, "-log", log, "-record", n}
	}

	for _, log := range []string{"login", "activity"} {
		refused(accept(log, "3"), "record 3 follows record 2: there is no break to accept")
		refused(accept(log, "1"), "record 1 has no prev, as the first record of a log has none: there is no break to accept")
		refused(accept(log, "9"), fmt.Sprintf("record 9 of the audit log %s is not in the store", log))
	}

	var starts, ends [2]time.Time
	for i, log := range []string{"login", "activity"} {
		starts[i] = time.Now().UTC().Truncate(time.Second)
		code, stdout, stderr := runCmd(t, accept(log, "4")...)
		want := "accepted the break at record 4 of the audit log " + log + " in " + d.data + "; whiteroom doctor checks the log again from it\n"
		if code != 0 || stdout != want || stderr != "" {
			t.Fatalf("whiteroom-db accept-break = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, want)
		}
		ends[i] = time.Now().UTC().Add(time.Second)
	}

	// A daemon serves the row of each break and the event of its acceptance, each followed by the mark of its command.
	s := d.startServe(t, d.args()...)
	r := openClient(t, d.socks, dbwire.RoleReader, dbclient.Options{})
	var tail []string
	marks := map[time.Time]bool{}
	if err := r.View(t.Context(), func(tx *dbclient.Tx) error {
		for i, log := range []string{"login", "activity"} {
			v, found, err := tx.Get(dbwire.TableChains, dbwire.ChainBreakKey(log, 4))
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("the table chains has no row of the break at record 4 of the %s log", log)
			}
			row, ok := dbwire.ParseChainBreak(v)
			if !ok {
				return errors.New("the row of the break cannot be read")
			}
			if want := sha256.Sum256(chains[log][3]); row.Hash != want || row.At.Before(starts[i]) || row.At.After(ends[i]) {
				t.Errorf("the row of the break of the %s log is %x at %v, want the hash %x of record 4 between %v and %v", log,
					row.Hash[:4], row.At, want[:4], starts[i], ends[i])
			}
			marks[row.At] = true
		}
		return tx.Events(t.Context(), func(_ uint64, data []byte) bool {
			switch a, isAccepted := dbwire.ParseAccepted(data); {
			case isAccepted:
				tail = append(tail, fmt.Sprintf("%s %d %x", a.Log, a.N, a.Hash[:4]))
			default:
				if at, isMark := dbwire.ParseMark(data); isMark && marks[at] {
					tail = append(tail, "mark")
				}
			}
			return true
		})
	}); err != nil {
		t.Fatal(err)
	}
	login, activity := sha256.Sum256(chains["login"][3]), sha256.Sum256(chains["activity"][3])
	want := []string{fmt.Sprintf("login 4 %x", login[:4]), "mark", fmt.Sprintf("activity 4 %x", activity[:4]), "mark"}
	if tail = tail[max(0, len(tail)-len(want)):]; !slices.Equal(tail, want) {
		t.Errorf("the events of the acceptances are %v, want %v: each followed by the mark of its time", tail, want)
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

	// The same break is not accepted twice, and nothing else is changed by the command that refuses it.
	for _, log := range []string{"login", "activity"} {
		refused(accept(log, "4"), fmt.Sprintf("the break at record 4 of the audit log %s is already accepted", log))
		refused(accept(log, "5"), "record 5 follows record 4: there is no break to accept")
	}
}

// TestAcceptBreakFirst checks accept-break on a break at the first record that is read: record 1 with a prev, and the first
// record that remains after a deletion, whose prev is not the hash that the chain start has. The first is accepted like any break,
// and the second is refused while the records that the deletion left are before it, and accepted without them.
func TestAcceptBreakFirst(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	wrong := sha256.Sum256([]byte("another record"))
	accept := func(d dirs, n string) []string {
		return []string{"accept-break", "-data", d.data, "-log", "login", "-record", n}
	}
	line := func(d dirs, n string) string {
		return "accepted the break at record " + n + " of the audit log login in " + d.data + "; whiteroom doctor checks the log again from it\n"
	}

	t.Run("record 1", func(t *testing.T) {
		d := newDirs(t)
		one := loginRecord(1, recordHash([]byte("a record that was before it")))
		writeCommits(t, d.data, commitAt{now, [][]byte{one, loginRecord(2, recordHash(one))}})
		code, stdout, stderr := runCmd(t, accept(d, "1")...)
		if code != 0 || stdout != line(d, "1") || stderr != "" {
			t.Fatalf("whiteroom-db accept-break = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, line(d, "1"))
		}
		code, stdout, stderr = runCmd(t, accept(d, "1")...)
		if want := "whiteroom-db accept-break: the break at record 1 of the audit log login is already accepted\n"; code != 1 || stdout != "" || stderr != want {
			t.Errorf("a second whiteroom-db accept-break = %d, with stdout %q and stderr %q, want 1, nothing and %q", code, stdout, stderr, want)
		}
		if code, stdout, stderr := runCmd(t, "check", "-data", d.data); code != 0 || stdout != "no problems found in "+d.data+"\n" || stderr != "" {
			t.Errorf("whiteroom-db check = %d, with stdout %q and stderr %q", code, stdout, stderr)
		}
	})
	t.Run("the first record that remains", func(t *testing.T) {
		d := newDirs(t)
		chain := loginChain(4)
		writeCommits(t, d.data, commitAt{now, [][]byte{chain[2], chain[3]}})
		writeDeletion(t, d.data, 3, wrong, now.AddDate(-1, 0, 0), now)
		code, stdout, stderr := runCmd(t, accept(d, "3")...)
		if code != 0 || stdout != line(d, "3") || stderr != "" {
			t.Fatalf("whiteroom-db accept-break = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, line(d, "3"))
		}
	})
	t.Run("the first record that remains, with the records before it still there, which are not what the deletion left", func(t *testing.T) {
		d := newDirs(t)
		chain := loginChain(4)
		writeCommits(t, d.data, commitAt{now, chain})
		writeDeletion(t, d.data, 3, wrong, now.AddDate(-1, 0, 0), now)
		before := withoutHead(t, d.data)
		code, stdout, stderr := runCmd(t, accept(d, "3")...)
		want := "whiteroom-db accept-break: record 3 comes after records before it that an earlier deletion left, which are still in the " +
			"store: whiteroom-db compact drops them if they are what the deletion left; accept the break at record 3 then\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Errorf("whiteroom-db accept-break = %d, with stdout %q and stderr %q, want 1, nothing and %q", code, stdout, stderr, want)
		}
		if got := withoutHead(t, d.data); !maps.Equal(got, before) {
			t.Error("the refusal changed the files")
		}
	})
	t.Run("the first record that remains, with the records before it still there, which compact drops", func(t *testing.T) {
		// The records 1 and 2 are exactly what the deletion left, and the record 3 does not have the hash that the chain start has
		// of the record 2: accept-break sends the operator to compact, which drops them and stops at the record 3, and then
		// accept-break accepts the break.
		d := newDirs(t)
		chain := loginChain(4)
		three := loginRecord(3, recordHash(chain[0]))
		cutoff := now.AddDate(-1, 0, 0)
		writeCommits(t, d.data, commitAt{now, [][]byte{chain[0], chain[1], three}})
		writeDeletion(t, d.data, 3, sha256.Sum256(chain[1]), cutoff, now)
		refusal := "whiteroom-db accept-break: record 3 comes after records before it that an earlier deletion left, which are still " +
			"in the store: whiteroom-db compact drops them if they are what the deletion left; accept the break at record 3 then\n"
		if code, stdout, stderr := runCmd(t, accept(d, "3")...); code != 1 || stdout != "" || stderr != refusal {
			t.Fatalf("whiteroom-db accept-break = %d, with stdout %q and stderr %q, want 1, nothing and %q", code, stdout, stderr, refusal)
		}
		want := "compacted " + d.data + "\ndeleted the login records 1 to 2, from before " + cutoff.Format(time.RFC3339) + "\n" +
			"the deletion of login records stopped at record 3: prev is not the hash that the chain start has of record 2; " +
			"check the store with whiteroom doctor\n"
		if code, stdout, stderr := runCmd(t, "compact", "-data", d.data); code != 0 || stdout != want || stderr != "" {
			t.Fatalf("whiteroom-db compact = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, want)
		}
		code, stdout, stderr := runCmd(t, accept(d, "3")...)
		if code != 0 || stdout != line(d, "3") || stderr != "" {
			t.Fatalf("whiteroom-db accept-break = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, line(d, "3"))
		}
		if code, stdout, stderr := runCmd(t, "check", "-data", d.data); code != 0 || stdout != "no problems found in "+d.data+"\n" || stderr != "" {
			t.Errorf("whiteroom-db check = %d, with stdout %q and stderr %q", code, stdout, stderr)
		}
	})
	t.Run("the first record that remains, with the hash of the chain start", func(t *testing.T) {
		d := newDirs(t)
		chain := loginChain(4)
		writeCommits(t, d.data, commitAt{now, chain[2:]})
		writeDeletion(t, d.data, 3, sha256.Sum256(chain[1]), now.AddDate(-1, 0, 0), now)
		code, stdout, stderr := runCmd(t, accept(d, "3")...)
		want := "whiteroom-db accept-break: record 3 has as its prev the hash that the chain start has of record 2: there is no break to accept\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Errorf("whiteroom-db accept-break = %d, with stdout %q and stderr %q, want 1, nothing and %q", code, stdout, stderr, want)
		}
	})
}

// putHeads puts the heads of the audit logs, by their names, in the table heads of the data directory data, which no process has
// open: the number of each head, and the hash that its line names.
func putHeads(t *testing.T, data string, heads map[string]uint64, hashes map[string][sha256.Size]byte) {
	t.Helper()
	srv, err := dbserver.OpenDir(data, storeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := dbserver.EngineOf(srv).Update(t.Context(), func(tx *logdb.Tx) error {
		for log, n := range heads {
			line := fmt.Sprintf("audit head %s %d sha256:%x", log, n, hashes[log])
			if err := tx.Put(dbwire.TableHeads, []byte(log), []byte(line)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestAcceptGap checks accept-break on a gap in each place: the lines that it prints, the rows and the events that it leaves,
// which a daemon serves, the refusals, which change nothing, and that the check of the directory finds nothing wrong.
func TestAcceptGap(t *testing.T) {
	d := newDirs(t)
	now := time.Now().UTC().Truncate(time.Second)
	login, activity := auditChain("login", 5), auditChain("activity", 5)
	// The login history has the records 1, 2, 4 and 5, and its head names the record 7: the record 3 and the records 6 and 7 are
	// missing. The activity log has the records 3 to 5: the records 1 and 2 are missing.
	var commits []commitAt
	commits = append(commits, commitAt{now, [][]byte{login[0], activity[2]}}, commitAt{now, [][]byte{login[1], activity[3]}},
		commitAt{now, [][]byte{login[3], activity[4]}}, commitAt{now, [][]byte{login[4]}})
	writeCommits(t, d.data, commits...)
	endHash := sha256.Sum256([]byte("the record 7 of the login history"))
	putHeads(t, d.data, map[string]uint64{"login": 7}, map[string][sha256.Size]byte{"login": endHash})

	refused := func(args []string, text string) {
		t.Helper()
		before := withoutHead(t, d.data)
		code, stdout, stderr := runCmd(t, args...)
		if want := "whiteroom-db accept-break: " + text + "\n"; code != 1 || stdout != "" || stderr != want {
			t.Errorf("whiteroom-db %v = %d, with stdout %q and stderr %q, want 1, nothing and %q", args[1:], code, stdout, stderr, want)
		}
		if got := withoutHead(t, d.data); !maps.Equal(got, before) {
			t.Errorf("the refusal of whiteroom-db %v changed the files:\n%v\nwas\n%v", args[1:], got, before)
		}
	}
	accept := func(log, n string) []string {
		return []string{"accept-break", "-data", d.data, "-log", log, "-record", n}
	}
	accepted := func(args []string, line string) {
		t.Helper()
		code, stdout, stderr := runCmd(t, args...)
		if want := line + "\n"; code != 0 || stdout != want || stderr != "" {
			t.Fatalf("whiteroom-db %v = %d, with stdout %q and stderr %q, want 0, %q and nothing", args[1:], code, stdout, stderr, want)
		}
	}

	refused(accept("login", "3"), "record 3 of the audit log login is not in the store: it is in the gap of record 3 before record 4; "+
		"accept the gap with -record 4")
	refused(accept("login", "6"), "record 6 of the audit log login is not in the store")
	refused(accept("login", "5"), "record 5 follows record 4: there is no break to accept")
	refused(accept("activity", "2"), "record 2 of the audit log activity is not in the store: it is in the gap of the records 1 to 2 "+
		"before record 3; accept the gap with -record 3")

	accepted(accept("login", "4"), "accepted the gap of record 3 before record 4 of the audit log login in "+d.data+
		"; whiteroom doctor checks the log again from record 4")
	accepted(accept("activity", "3"), "accepted the gap of the records 1 to 2 before record 3 of the audit log activity in "+d.data+
		"; whiteroom doctor checks the log again from record 3")
	accepted(accept("login", "7"), "accepted the gap of the records 6 to 7 at the end of the audit log login in "+d.data+
		"; whiteroom doctor checks that the log ends with record 5")

	// A daemon serves the rows of the gaps and the events of their acceptances, each followed by the mark of its command.
	s := d.startServe(t, d.args()...)
	r := openClient(t, d.socks, dbwire.RoleReader, dbclient.Options{})
	var tail []string
	if err := r.View(t.Context(), func(tx *dbclient.Tx) error {
		for _, want := range []struct {
			log         string
			first, last uint64
			hash        [sha256.Size]byte
		}{{"login", 3, 3, sha256.Sum256(login[2])}, {"activity", 1, 2, sha256.Sum256(activity[1])}, {"login", 6, 7, endHash}} {
			v, found, err := tx.Get(dbwire.TableChains, dbwire.ChainGapKey(want.log, want.first))
			if err != nil {
				return err
			}
			row, ok := dbwire.ParseChainHole(want.first, v)
			if !found || !ok || row.Last != want.last || row.Hash != want.hash || row.At.Before(now) {
				t.Errorf("the row of the gap of the %s log from the record %d is %+v (found %v, valid %v), want the record %d, the hash %x "+
					"and a time of the commands", want.log, want.first, row, found, ok, want.last, want.hash[:4])
			}
		}
		return tx.Events(t.Context(), func(_ uint64, data []byte) bool {
			if g, isGap := dbwire.ParseGap(data); isGap {
				tail = append(tail, fmt.Sprintf("%s %d-%d %x", g.Log, g.First, g.Last, g.Hash[:4]))
			} else if _, isMark := dbwire.ParseMark(data); isMark && len(tail) > 0 {
				tail = append(tail, "mark")
			}
			return true
		})
	}); err != nil {
		t.Fatal(err)
	}
	l3, a2 := sha256.Sum256(login[2]), sha256.Sum256(activity[1])
	want := []string{fmt.Sprintf("login 3-3 %x", l3[:4]), "mark", fmt.Sprintf("activity 1-2 %x", a2[:4]), "mark",
		fmt.Sprintf("login 6-7 %x", endHash[:4]), "mark"}
	if !slices.Equal(tail, want) {
		t.Errorf("the events of the acceptances are %v, want %v: each followed by the mark of its time", tail, want)
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

	// The same gaps are not accepted twice.
	refused(accept("login", "4"), "record 4 comes after the record 3 whose gap is accepted already")
	refused(accept("activity", "3"), "record 3 comes after the records 1 to 2 whose gap is accepted already")
	refused(accept("login", "7"), "the gap of the records 6 to 7 at the end of the audit log login is accepted already")
}

// putChainRow puts the row of the table chains with the key and the value into the data directory data, which no process has open.
func putChainRow(t *testing.T, data string, key, value []byte) {
	t.Helper()
	srv, err := dbserver.OpenDir(data, storeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := dbserver.EngineOf(srv).Update(t.Context(), func(tx *logdb.Tx) error { return tx.Put(dbwire.TableChains, key, value) }); err != nil {
		t.Fatal(err)
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestAcceptGapAfterRow checks that the gap that whiteroom-db repair tells for a record that is missing after a hole that it
// recorded is the gap that whiteroom-db accept-break writes: it starts after the hole, and lies over no row.
func TestAcceptGapAfterRow(t *testing.T) {
	d := newDirs(t)
	now := time.Now().UTC().Truncate(time.Second)
	recs := loginChain(5)
	// The records 3 and 4 of the login history are lost, and the row of a hole records the record 3.
	writeCommits(t, d.data, commitAt{now, [][]byte{recs[0], recs[1], recs[4]}})
	putChainRow(t, d.data, dbwire.ChainHoleKey("login", 3),
		dbwire.AppendChainHole(nil, dbwire.ChainHole{Last: 3, Hash: sha256.Sum256(recs[2]), At: now}))

	code, stdout, stderr := runCmd(t, "repair", "-data", d.data)
	want := `gap: audit log "login": record 4, which this repair does not make: whiteroom doctor reports it until it is accepted ` +
		"with whiteroom-db accept-break -log login -record 5\n" + "nothing to repair in " + d.data + "\n"
	if code != 0 || stderr != "" || stdout != want {
		t.Fatalf("whiteroom-db repair = %d, with stdout:\n%s\nand stderr %q, want\n%s", code, stdout, stderr, want)
	}
	code, stdout, stderr = runCmd(t, "accept-break", "-data", d.data, "-log", "login", "-record", "5")
	line := "accepted the gap of record 4 before record 5 of the audit log login in " + d.data +
		"; whiteroom doctor checks the log again from record 5\n"
	if code != 0 || stdout != line || stderr != "" {
		t.Fatalf("whiteroom-db accept-break = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, line)
	}
	if code, stdout, stderr := runCmd(t, "repair", "-data", d.data); code != 0 || stderr != "" || stdout != "nothing to repair in "+d.data+"\n" {
		t.Errorf("whiteroom-db repair after the acceptance = %d, with stdout %q and stderr %q", code, stdout, stderr)
	}
	code, stdout, stderr = runCmd(t, "accept-break", "-data", d.data, "-log", "login", "-record", "5")
	refusal := "whiteroom-db accept-break: record 5 comes after the record 3 that whiteroom-db repair dropped and the record 4 whose gap is " +
		"accepted already: there is no break to accept\n"
	if code != 1 || stdout != "" || stderr != refusal {
		t.Errorf("a second whiteroom-db accept-break = %d, with stdout %q and stderr %q, want 1, nothing and %q", code, stdout, stderr, refusal)
	}
}

// TestAcceptEndGapAfterRow checks the line for a gap at the end of a log that comes after a hole or an accepted gap: it names the last
// record that the log has, which the row follows, and not the end of the row, which is no record.
func TestAcceptEndGapAfterRow(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	recs := loginChain(5)
	for _, kind := range []string{"hole", "gap"} {
		t.Run(kind, func(t *testing.T) {
			d := newDirs(t)
			// The login history has the records 1 to 3 and a row for the record 4, and its head names the record 6.
			writeCommits(t, d.data, commitAt{now, [][]byte{recs[0], recs[1], recs[2]}})
			key := dbwire.ChainHoleKey("login", 4)
			if kind == "gap" {
				key = dbwire.ChainGapKey("login", 4)
			}
			putChainRow(t, d.data, key, dbwire.AppendChainHole(nil, dbwire.ChainHole{Last: 4, Hash: sha256.Sum256(recs[3]), At: now}))
			putHeads(t, d.data, map[string]uint64{"login": 6}, map[string][sha256.Size]byte{"login": sha256.Sum256([]byte("the record 6"))})

			code, stdout, stderr := runCmd(t, "repair", "-data", d.data)
			want := `gap: audit log "login": records 5 to 6, which this repair does not make: whiteroom doctor reports it until it is accepted ` +
				"with whiteroom-db accept-break -log login -record 6\n" + "nothing to repair in " + d.data + "\n"
			if code != 0 || stderr != "" || stdout != want {
				t.Fatalf("whiteroom-db repair = %d, with stdout:\n%s\nand stderr %q, want\n%s", code, stdout, stderr, want)
			}
			code, stdout, stderr = runCmd(t, "accept-break", "-data", d.data, "-log", "login", "-record", "6")
			line := "accepted the gap of the records 5 to 6 at the end of the audit log login in " + d.data +
				"; whiteroom doctor checks that the log ends with record 3\n"
			if code != 0 || stdout != line || stderr != "" {
				t.Fatalf("whiteroom-db accept-break = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, line)
			}
		})
	}
}

// TestAcceptGapAgain checks that a gap row whose hash is no longer that of the record after it (the record has been rewritten) is
// accepted again, and that whiteroom-db repair tells that gap with the command: the row is replaced, and a hole row is not.
func TestAcceptGapAgain(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	recs := loginChain(4)
	for _, kind := range []string{"gap", "hole"} {
		t.Run(kind, func(t *testing.T) {
			d := newDirs(t)
			// The record 3 is lost and recorded, with a hash that is not the one that the record 4 has as its prev.
			writeCommits(t, d.data, commitAt{now, [][]byte{recs[0], recs[1], recs[3]}})
			key := dbwire.ChainGapKey("login", 3)
			if kind == "hole" {
				key = dbwire.ChainHoleKey("login", 3)
			}
			putChainRow(t, d.data, key, dbwire.AppendChainHole(nil, dbwire.ChainHole{Last: 3, Hash: sha256.Sum256([]byte("another record")), At: now}))

			code, stdout, stderr := runCmd(t, "repair", "-data", d.data)
			want := `gap: audit log "login": record 3, which this repair does not make: whiteroom doctor reports it until it is accepted ` +
				"with whiteroom-db accept-break -log login -record 4\n" + "nothing to repair in " + d.data + "\n"
			if code != 0 || stderr != "" || stdout != want {
				t.Fatalf("whiteroom-db repair = %d, with stdout:\n%s\nand stderr %q, want\n%s", code, stdout, stderr, want)
			}
			code, stdout, stderr = runCmd(t, "accept-break", "-data", d.data, "-log", "login", "-record", "4")
			line := "accepted the gap of record 3 before record 4 of the audit log login in " + d.data +
				"; whiteroom doctor checks the log again from record 4\n"
			if code != 0 || stdout != line || stderr != "" {
				t.Fatalf("whiteroom-db accept-break = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, line)
			}
			if code, stdout, stderr := runCmd(t, "repair", "-data", d.data); code != 0 || stderr != "" || stdout != "nothing to repair in "+d.data+"\n" {
				t.Errorf("whiteroom-db repair after the acceptance = %d, with stdout %q and stderr %q", code, stdout, stderr)
			}
			if code, _, stderr := runCmd(t, "accept-break", "-data", d.data, "-log", "login", "-record", "4"); code != 1 ||
				!strings.Contains(stderr, "is no break to accept") && !strings.Contains(stderr, "accepted already") {
				t.Errorf("a second whiteroom-db accept-break = %d, with stderr %q, want a refusal", code, stderr)
			}
		})
	}
}

// TestAcceptGapOfEmptyLog checks the gap at the end of a log that has no record, and the refusal of a record that is a gap with
// no hash of the record before it.
func TestAcceptGapOfEmptyLog(t *testing.T) {
	d := newDirs(t)
	now := time.Now().UTC().Truncate(time.Second)
	login := auditChain("login", 5)
	// The record 4 of the login history has no hash as its prev, and the head of the activity log names three records that are not
	// there.
	odd := fmt.Appendf(nil, `{"log":"login","n":4,"prev":"nonsense","at":"2026-01-01T00:00:00Z","action":"x"}`)
	writeCommits(t, d.data, commitAt{now, [][]byte{login[0], login[1], odd}})
	hash := sha256.Sum256([]byte("the record 3 of the activity log"))
	putHeads(t, d.data, map[string]uint64{"activity": 3}, map[string][sha256.Size]byte{"activity": hash})

	code, stdout, stderr := runCmd(t, "accept-break", "-data", d.data, "-log", "login", "-record", "4")
	want := "whiteroom-db accept-break: record 4 does not follow record 2 in number, and its prev is not the hash of a record " +
		"(sha256: and 64 lower case hexadecimal digits), so the gap before it cannot be accepted\n"
	if code != 1 || stdout != "" || stderr != want {
		t.Errorf("whiteroom-db accept-break = %d, with stdout %q and stderr %q, want 1, nothing and %q", code, stdout, stderr, want)
	}
	// The activity log always has a record: whiteroom doctor goes on reporting the empty log until the writer adds one.
	code, stdout, stderr = runCmd(t, "accept-break", "-data", d.data, "-log", "activity", "-record", "3")
	line := "accepted the gap of the records 1 to 3 at the end of the audit log activity in " + d.data +
		"; whiteroom doctor goes on reporting that the log has no record until the writer adds its next record\n"
	if code != 0 || stdout != line || stderr != "" {
		t.Errorf("whiteroom-db accept-break = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, line)
	}

	// The login history may have none: whiteroom doctor then checks that it has none.
	d = newDirs(t)
	writeCommits(t, d.data, commitAt{now, nil})
	putHeads(t, d.data, map[string]uint64{"login": 3}, map[string][sha256.Size]byte{"login": hash})
	code, stdout, stderr = runCmd(t, "accept-break", "-data", d.data, "-log", "login", "-record", "3")
	line = "accepted the gap of the records 1 to 3 at the end of the audit log login in " + d.data +
		"; whiteroom doctor checks that the log has no record\n"
	if code != 0 || stdout != line || stderr != "" {
		t.Errorf("whiteroom-db accept-break = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, line)
	}
}

// TestAcceptBreakInUse checks that accept-break waits for about two seconds for a daemon that has the data directory open,
// then fails with the way out, and changes nothing, and that the daemon still serves.
func TestAcceptBreakInUse(t *testing.T) {
	d := newDirs(t)
	breakStore(t, d)
	s := d.startServe(t, d.args()...)
	w := openClient(t, d.socks, dbwire.RoleWriter, dbclient.Options{})
	before := files(t, d.data)
	start := time.Now()
	code, stdout, stderr := runCmd(t, "accept-break", "-data", d.data, "-log", "login", "-record", "4")
	want := fmt.Sprintf("whiteroom-db accept-break: %s: the data directory is in use; stop whiteroom-db serve on it first\n", d.data)
	if code != 1 || stdout != "" || stderr != want {
		t.Errorf("whiteroom-db accept-break on a directory in use = %d, with stdout %q and stderr %q, want 1, nothing and %q", code,
			stdout, stderr, want)
	}
	if took := time.Since(start); took < 1500*time.Millisecond || took > 8*time.Second {
		t.Errorf("whiteroom-db accept-break on a directory in use took %v, want about 2 seconds", took)
	}
	if got := files(t, d.data); !maps.Equal(got, before) {
		t.Errorf("accept-break on a directory in use changed the files:\n%v\nwas\n%v", got, before)
	}
	mustCommit(t, w, row{2, "a", "1"}) // the writer still commits
	if code := s.stop(); code != 0 {
		t.Fatalf("whiteroom-db serve = %d", code)
	}
}
