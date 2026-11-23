//go:build linux

package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// maintenanceArgs returns the arguments of the maintenance command named command on the data directory data: the arguments
// that accept-break needs are those of a record that a store with no record refuses.
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
		rec := fmt.Appendf(nil, `{"log":%q,"n":%d,"action":"x"}`, log, i)
		if prev != "" {
			rec = fmt.Appendf(nil, `{"log":%q,"n":%d,"prev":%q,"action":"x"}`, log, i, prev)
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
		refused(accept(log, "1"), fmt.Sprintf("record 1 is the first record that the audit log %s is checked from: a break there is not accepted", log))
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
