package main

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a buffer that several goroutines may write and read: the daemon writes the heads and its log lines
// while a test looks at them.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// testEnv is the env of a command that writes to stdout and stderr, and gets the times from ticks, none if it is nil.
func testEnv(stdout, stderr io.Writer, ticks <-chan time.Time) env {
	return env{stdout: stdout, stderr: stderr, ticks: func() (<-chan time.Time, func()) { return ticks, func() {} }}
}

// runCmd runs the command with args, and returns its exit code and what it has written to stdout and stderr.
func runCmd(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	// A command that should end at once and serves instead ends with the deadline, and the test fails.
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	return runCmdIn(ctx, t, args...)
}

func runCmdIn(ctx context.Context, t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var o, e syncBuffer
	code = run(ctx, args, testEnv(&o, &e, nil))
	return code, o.String(), e.String()
}

// files returns the mode and the bytes of each file in dir, by its path in dir.
func files(t testing.TB, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		m[filepath.ToSlash(rel)] = info.Mode().String()
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			m[filepath.ToSlash(rel)] += " " + string(data)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return m
}

// writeFile writes the file name in dir, and returns its path.
func writeFile(t testing.TB, dir, name, text string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestUsage checks the exit codes and the output of the command for the usage and for errors in the arguments, none of
// which opens a data directory.
func TestUsage(t *testing.T) {
	// The relative paths of the arguments, which a command that goes on would use, are those of an empty directory.
	t.Chdir(t.TempDir())
	help := map[string]string{}
	for _, c := range []struct {
		name, usage string
		flags       []string
	}{
		{"serve", serveUsage, []string{"-config FILE", "-data DIR", "-db DIR", "-writer-user USER", "-writer-group GROUP",
			"-reader-group GROUP", "-max-readers N", "-commit-rate N", "-min-free BYTES", "-min-free-inodes N",
			"-snapshot-idle DURATION", "-stall DURATION", "-max-staged BYTES"}},
		{"init", initUsage, []string{"-config FILE", "-data DIR"}},
		{"compact", compactUsage, []string{"-config FILE", "-data DIR"}},
		{"gc", gcUsage, []string{"-config FILE", "-data DIR"}},
		{"check", checkUsage, []string{"-config FILE", "-data DIR"}},
		{"accept-break", acceptUsage, []string{"-config FILE", "-data DIR", "-log LOG", "-record N"}},
		{"repair", repairUsage, []string{"-config FILE", "-data DIR", "-apply", "-keep DIR", "-truncate", "-drop-unreadable-records"}},
	} {
		code, stdout, stderr := runCmd(t, c.name, "-h")
		if code != 0 || stdout != "" || !strings.HasPrefix(stderr, c.usage) {
			t.Fatalf("whiteroom-db %s -h = %d, with stdout %q and stderr:\n%s\nwant 0, and the usage on stderr", c.name, code,
				stdout, stderr)
		}
		for _, flag := range c.flags {
			if !strings.Contains(stderr, "\n  "+flag+"\n") {
				t.Errorf("the usage of whiteroom-db %s has no flag %s:\n%s", c.name, flag, stderr)
			}
		}
		if !strings.Contains(usage, "\n  whiteroom-db "+c.name+" [flags]\n") || !strings.Contains(usage, `"whiteroom-db `+c.name+` -h"`) {
			t.Errorf("the list of the commands has no whiteroom-db %s:\n%s", c.name, usage)
		}
		help[c.name] = stderr
	}
	for _, text := range []string{"(default 64)", "(default 50)", "(default 1073741824)", "(default 65536)", "(default 5m0s)",
		"(default 30s)", "(default 268435446)"} {
		if !strings.Contains(help["serve"], text) {
			t.Errorf("the usage of whiteroom-db serve does not tell the default %s:\n%s", text, help["serve"])
		}
	}
	for _, text := range []string{"must exist", "serve does not make it", "writer.sock", "reader.sock", "0660", "standard error",
		"data, db, writerUser, writerGroup, readerGroup,\nmaxReaders, commitRate, minFree, minFreeInodes, snapshotIdle, stall and maxStaged"} {
		if !strings.Contains(serveUsage, text) {
			t.Errorf("the usage of whiteroom-db serve does not say %q", text)
		}
	}
	for _, name := range []string{"compact", "gc", "check", "accept-break", "repair"} {
		if !strings.Contains(help[name], "No daemon may have the data directory open") {
			t.Errorf("the usage of whiteroom-db %s does not say that the daemon must be stopped:\n%s", name, help[name])
		}
	}
	// compact drops the login records older than one year, and says so, and nothing else is dropped.
	compactWords := strings.Join(strings.Fields(help["compact"]), " ")
	for _, text := range []string{"drops the login records older than one year, and nothing else", "one calendar year before the earlier",
		"The activity log is never dropped", "stops at the first that is not older than the cutoff or has no mark after it",
		"A commit that has no mark (one that a loss of events left without it) has the time of the first mark after it in the log",
		"deleted the login records A to B, from before TIME", "A data directory of an older table schema than this program's is compacted",
		"no login record is dropped from it", "The first writer that connects through",
		"but for what a deletion adds (the new start of the chain, the event of the deletion and a mark)",
		"A compaction that stops before its end leaves the login records that it was to drop",
		"drops them, whatever their age, if they are exactly the records before the chain start",
		"a hole that \"whiteroom-db repair\" recorded, or a gap that \"whiteroom-db accept-break\" accepted, passes",
		"The event of that deletion is not needed; TIME is then the cutoff of that earlier run",
		"deleted the login records A to B, which the deletion of the records before record K at TIME left; the store has no event of that deletion",
		"They go also when the command then stops at the first record that remains",
		"If they are not exactly that, it keeps them, and says where it stopped"} {
		if !strings.Contains(compactWords, text) {
			t.Errorf("the usage of whiteroom-db compact does not say %q:\n%s", text, help["compact"])
		}
	}
	if strings.Contains(compactWords, "Nothing is dropped") || !strings.Contains(strings.Join(strings.Fields(usage), " "), "delete the login records older than one year") {
		t.Errorf("the usages of whiteroom-db compact do not say what it deletes:\n%s\n%s", usage, help["compact"])
	}
	// accept-break changes no record, says which traces it leaves and what it refuses, and says that a gap that whiteroom-db repair
	// makes is recorded as a hole and one that it does not make is accepted here.
	acceptWords := strings.Join(strings.Fields(help["accept-break"]), " ")
	for _, text := range []string{"changes no record and drops none", "leaves three traces", "a row of the table chains",
		"an event of the daemon in the log", "checks the log again from record N", "tells the break or the gap in a note, not as a problem",
		"A later change to record N undoes the acceptance", "refuses, and changes nothing", "record 1 with no prev",
		"while records before it that the deletion left are still in the store", "accept the break then",
		"The first record that is read is a break too, if it is the one that the log starts with and no record before it is in the store",
		"a record that follows the one before it", "a break that is accepted already", "a chain whose rows cannot be read",
		"an older table schema than this program's", "an event that is no record", "no record number to name it by",
		"records as a hole a gap that it makes itself", "and tells a gap that it did not make",
		"A break is a record whose number is that of the one before it plus one", "A gap is records that are missing",
		"or the gap before it", "the head of the log names records that the log does not have",
		"a record that follows a hole that", "recorded or a gap that is accepted already",
		"a record whose number is below that of a record before it", "the gap before a record whose prev is not the hash of a record",
		"for which it names the record to accept the gap with", "goes on reporting such a gap until it is accepted here, and an event that is no record",
		"after a gap at the end of an activity log that has none", "goes on reporting the empty log until the writer has added the next record",
		"accepted the break at record N of the audit log LOG in DATA; whiteroom doctor checks the log again from it",
		"accepted the gap of the records A to B before record N of the audit log LOG in DATA; whiteroom doctor checks the log again from record N",
		`("the gap of record A" if it is one record)`, "at the end of the audit log LOG in DATA; whiteroom doctor checks that the log ends with record A-1",
		`(or "that the log has no record")`,
		`starts with "whiteroom-db accept-break:"`, "cannot accept the changes that it made to the log"} {
		if !strings.Contains(acceptWords, text) {
			t.Errorf("the usage of whiteroom-db accept-break does not say %q:\n%s", text, help["accept-break"])
		}
	}
	if strings.Contains(acceptWords, "or its number is not that of that record plus one") {
		t.Errorf("the usage of whiteroom-db accept-break says that a record whose number does not follow is a break:\n%s", help["accept-break"])
	}
	if !strings.Contains(strings.Join(strings.Fields(usage), " "), "accept the break at record -record N of the audit log -log LOG") {
		t.Errorf("the list of the commands does not say what accept-break does:\n%s", usage)
	}
	// After a failed sync, the five refuse the data directory until the machine has restarted, and then remove the mark.
	for _, name := range []string{"serve", "compact", "gc", "accept-break", "repair"} {
		words := strings.Join(strings.Fields(help[name]), " ")
		for _, text := range []string{"After a sync has failed", "<DATA>/stopped", "until the machine has restarted"} {
			if !strings.Contains(words, text) {
				t.Errorf("the usage of whiteroom-db %s does not say %q:\n%s", name, text, help[name])
			}
		}
		if !strings.Contains(words, "removes the mark") {
			t.Errorf("the usage of whiteroom-db %s does not say that it removes the mark:\n%s", name, help[name])
		}
	}
	// check refuses it too, and writes nothing: it leaves a mark of an earlier boot, and does not need to write in the directory.
	checkWords := strings.Join(strings.Fields(help["check"]), " ")
	for _, text := range []string{"After a sync has failed", "<DATA>/stopped", "until the machine has restarted",
		"is left where it is", "checks a data directory that it cannot write in", "change nothing", "writes nothing",
		"stopped after 1000 of them", "engine or original", "does not look at the rows, the table schema among them",
		`a copy whose schema is newer than this program's passes the check, and "whiteroom-db serve" refuses it`} {
		if !strings.Contains(checkWords, text) {
			t.Errorf("the usage of whiteroom-db check does not say %q:\n%s", text, help["check"])
		}
	}
	if strings.Contains(checkWords, "removes the mark") {
		t.Errorf("the usage of whiteroom-db check says that it removes the mark:\n%s", help["check"])
	}

	// repair says what it keeps, drops and leaves, what -apply needs and what the output is, and that it changes nothing without it.
	repairWords := strings.Join(strings.Fields(help["repair"]), " ")
	for _, text := range []string{"Without -apply it only shows what a repair would drop and lose, and changes nothing",
		"not the mark <DATA>/stopped", "works on a data directory that it cannot write in",
		"keeps the state of the last commit that comes before the first damage that cannot be read",
		"the state of the checkpoint, which covers the damage before it", "It drops what cannot be read",
		"the objects whose data cannot be read or does not have their hash", "Nothing in files/ is changed",
		"A row that names a dropped object stays", "the original of a medium that is missing",
		"drops them only with -truncate: it refuses, and changes nothing, if it must drop them and -truncate is not given",
		"refuses a store that it would leave without a commit: restore that store from a copy",
		"records each hole, in the same generation, in the table chains and with an event of the daemon",
		"A store of the table schema 8 or 9 gets the schema 10 for that; one of an older schema is refused",
		"A gap that is no hole of this repair is told in a \"gap:\" line, and stays what it was",
		`until an operator who has looked into it accepts it with "whiteroom-db accept-break"`, "A gap that was accepted so is recorded already",
		"is kept, and its number is told, unless -drop-unreadable-records is given",
		"-apply -keep DIR first copies the files of logdb/, as they are, to DIR/logdb/, and makes them durable, and then repairs",
		"DIR must be missing, in a directory that exists, or empty, and must not be the data directory or in it",
		"It must not be a symbolic link itself (the directories that hold it may be)",
		"keep it as private as the data directory", "-keep and -apply need each other",
		"-truncate and -drop-unreadable-records without -apply change what is shown",
		`"kept the files of DATA/logdb in DIR/logdb"`, `"repaired DATA: generation G, sequence S"`, `"nothing to repair in DATA"`,
		`after "whiteroom-db repair:"`, "2 for a usage error", "without -apply the mark is left where it is",
		"A sync that fails in the repair itself makes it leave the mark; one that fails in the copy does not"} {
		if !strings.Contains(repairWords, text) {
			t.Errorf("the usage of whiteroom-db repair does not say %q:\n%s", text, help["repair"])
		}
	}
	if !strings.Contains(strings.Join(strings.Fields(usage), " "), "show what a repair of a damaged data directory would drop and lose, and change nothing") {
		t.Errorf("the list of the commands does not say what repair does:\n%s", usage)
	}

	missing := filepath.Join(t.TempDir(), "missing.yaml")
	_, openErr := os.Open(missing)
	broken := writeFile(t, t.TempDir(), "broken.yaml", "data:\t/a\n")
	usageErr := func(command, msg string) string { return "whiteroom-db " + command + ": " + msg + "\n" + help[command] }
	unknown := "whiteroom-db: unknown command \"nope\"\nRun \"whiteroom-db help\" for usage.\n"
	brokenLine := broken + ":1:6: tab characters are not allowed; use spaces\n"
	for _, tt := range []struct {
		args           []string
		code           int
		stdout, stderr string
	}{
		{nil, 2, "", usage},
		{[]string{"nope"}, 2, "", unknown},
		{[]string{"x"}, 2, "", "whiteroom-db: unknown command \"x\"\nRun \"whiteroom-db help\" for usage.\n"},
		{[]string{"help"}, 0, usage, ""},
		{[]string{"-h"}, 0, usage, ""},
		{[]string{"-help"}, 0, usage, ""},
		{[]string{"--help"}, 0, usage, ""},
		{[]string{"serve", "-x"}, 2, "", "flag provided but not defined: -x\n" + help["serve"]},
		{[]string{"serve", "-max-readers", "x"}, 2, "", "invalid value \"x\" for flag -max-readers: parse error\n" + help["serve"]},
		{[]string{"serve", "-stall", "x"}, 2, "", "invalid value \"x\" for flag -stall: parse error\n" + help["serve"]},
		{[]string{"serve", "-data", "d", "-db", "s", "x"}, 2, "",
			usageErr("serve", "unexpected argument \"x\": the command takes flags only")},
		{[]string{"serve"}, 2, "", usageErr("serve", "no data directory: give -data DIR, or data in the config file")},
		{[]string{"serve", "-db", "s"}, 2, "", usageErr("serve", "no data directory: give -data DIR, or data in the config file")},
		{[]string{"serve", "-data", "d"}, 2, "", usageErr("serve", "no socket directory: give -db DIR, or db in the config file")},
		{[]string{"serve", "-data", "", "-db", "s"}, 2, "",
			usageErr("serve", "no data directory: give -data DIR, or data in the config file")},
		{[]string{"serve", "-data", "d", "-db", ""}, 2, "",
			usageErr("serve", "no socket directory: give -db DIR, or db in the config file")},
		{[]string{"serve", "-config", missing}, 2, "", "whiteroom-db serve: " + openErr.Error() + "\n"},
		{[]string{"serve", "-config", broken}, 2, "", brokenLine},
		{[]string{"init", "-x"}, 2, "", "flag provided but not defined: -x\n" + help["init"]},
		{[]string{"init"}, 2, "", usageErr("init", "no data directory: give -data DIR, or data in the config file")},
		{[]string{"init", "-data", "d", "x"}, 2, "", usageErr("init", "unexpected argument \"x\": the command takes flags only")},
		{[]string{"init", "-config", missing}, 2, "", "whiteroom-db init: " + openErr.Error() + "\n"},
		{[]string{"init", "-config", broken}, 2, "", brokenLine},
		{[]string{"compact"}, 2, "", usageErr("compact", "no data directory: give -data DIR, or data in the config file")},
		{[]string{"compact", "-x"}, 2, "", "flag provided but not defined: -x\n" + help["compact"]},
		{[]string{"compact", "-data", "d", "x"}, 2, "",
			usageErr("compact", "unexpected argument \"x\": the command takes flags only")},
		{[]string{"compact", "-config", missing}, 2, "", "whiteroom-db compact: " + openErr.Error() + "\n"},
		{[]string{"compact", "-config", broken}, 2, "", brokenLine},
		{[]string{"gc"}, 2, "", usageErr("gc", "no data directory: give -data DIR, or data in the config file")},
		{[]string{"gc", "-x"}, 2, "", "flag provided but not defined: -x\n" + help["gc"]},
		{[]string{"gc", "-data", "d", "x"}, 2, "", usageErr("gc", "unexpected argument \"x\": the command takes flags only")},
		{[]string{"gc", "-config", missing}, 2, "", "whiteroom-db gc: " + openErr.Error() + "\n"},
		{[]string{"gc", "-config", broken}, 2, "", brokenLine},
		{[]string{"check"}, 2, "", usageErr("check", "no data directory: give -data DIR, or data in the config file")},
		{[]string{"check", "-x"}, 2, "", "flag provided but not defined: -x\n" + help["check"]},
		{[]string{"check", "-data", ""}, 2, "", usageErr("check", "no data directory: give -data DIR, or data in the config file")},
		{[]string{"check", "-data", "d", "x"}, 2, "", usageErr("check", "unexpected argument \"x\": the command takes flags only")},
		{[]string{"check", "-config", missing}, 2, "", "whiteroom-db check: " + openErr.Error() + "\n"},
		{[]string{"check", "-config", broken}, 2, "", brokenLine},
		{[]string{"accept-break"}, 2, "", usageErr("accept-break", "no data directory: give -data DIR, or data in the config file")},
		{[]string{"accept-break", "-x"}, 2, "", "flag provided but not defined: -x\n" + help["accept-break"]},
		{[]string{"accept-break", "-data", "d"}, 2, "", usageErr("accept-break", "no audit log: give -log login or -log activity")},
		{[]string{"accept-break", "-data", "d", "-record", "3"}, 2, "",
			usageErr("accept-break", "no audit log: give -log login or -log activity")},
		{[]string{"accept-break", "-data", "d", "-log", "x", "-record", "3"}, 2, "",
			usageErr("accept-break", `the audit log "x" is neither login nor activity`)},
		{[]string{"accept-break", "-data", "d", "-log", "", "-record", "3"}, 2, "",
			usageErr("accept-break", `the audit log "" is neither login nor activity`)},
		{[]string{"accept-break", "-data", "d", "-log", "Login", "-record", "3"}, 2, "",
			usageErr("accept-break", `the audit log "Login" is neither login nor activity`)},
		{[]string{"accept-break", "-data", "d", "-log", "login"}, 2, "",
			usageErr("accept-break", "no record: give -record N, the number that whiteroom doctor reports")},
		{[]string{"accept-break", "-data", "d", "-log", "login", "-record", "0"}, 2, "",
			usageErr("accept-break", "the records are numbered from 1, not 0")},
		{[]string{"accept-break", "-data", "d", "-log", "login", "-record", "-1"}, 2, "",
			"invalid value \"-1\" for flag -record: parse error\n" + help["accept-break"]},
		{[]string{"accept-break", "-data", "d", "-log", "login", "-record", "x"}, 2, "",
			"invalid value \"x\" for flag -record: parse error\n" + help["accept-break"]},
		{[]string{"accept-break", "-data", "d", "-log", "login", "-record", "3", "x"}, 2, "",
			usageErr("accept-break", "unexpected argument \"x\": the command takes flags only")},
		{[]string{"accept-break", "-config", missing}, 2, "", "whiteroom-db accept-break: " + openErr.Error() + "\n"},
		{[]string{"accept-break", "-config", broken}, 2, "", brokenLine},
		{[]string{"repair"}, 2, "", usageErr("repair", "no data directory: give -data DIR, or data in the config file")},
		{[]string{"repair", "-x"}, 2, "", "flag provided but not defined: -x\n" + help["repair"]},
		{[]string{"repair", "-data", ""}, 2, "", usageErr("repair", "no data directory: give -data DIR, or data in the config file")},
		{[]string{"repair", "-data", "d", "x"}, 2, "", usageErr("repair", "unexpected argument \"x\": the command takes flags only")},
		{[]string{"repair", "-data", "d", "-apply"}, 2, "",
			usageErr("repair", "no copy of logdb/: give -keep DIR, the directory for it, with -apply")},
		{[]string{"repair", "-data", "d", "-apply", "-keep", ""}, 2, "",
			usageErr("repair", "no copy of logdb/: give -keep DIR, the directory for it, with -apply")},
		{[]string{"repair", "-data", "d", "-keep", "k"}, 2, "", usageErr("repair", "-keep is for -apply: give -apply too, or leave out -keep")},
		{[]string{"repair", "-data", "d", "-keep", ""}, 2, "", usageErr("repair", "-keep is for -apply: give -apply too, or leave out -keep")},
		{[]string{"repair", "-data", "d", "-truncate", "-keep", "k"}, 2, "",
			usageErr("repair", "-keep is for -apply: give -apply too, or leave out -keep")},
		{[]string{"repair", "-data", "d", "-apply=x"}, 2, "", "invalid boolean value \"x\" for -apply: parse error\n" + help["repair"]},
		{[]string{"repair", "-config", missing}, 2, "", "whiteroom-db repair: " + openErr.Error() + "\n"},
		{[]string{"repair", "-config", broken}, 2, "", brokenLine},
	} {
		code, stdout, stderr := runCmd(t, tt.args...)
		if code != tt.code || stdout != tt.stdout || stderr != tt.stderr {
			t.Errorf("whiteroom-db %s = %d, with stdout:\n%s\nand stderr:\n%s\nwant %d, with stdout:\n%s\nand stderr:\n%s",
				strings.Join(tt.args, " "), code, stdout, stderr, tt.code, tt.stdout, tt.stderr)
		}
	}
}

func TestInit(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "parent", "data")
	code, stdout, stderr := runCmd(t, "init", "-data", data)
	if want := "created the data directory " + data + "\n"; code != 0 || stdout != want || stderr != "" {
		t.Fatalf("whiteroom-db init = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, want)
	}
	for _, name := range []string{"logdb", "files"} {
		info, err := os.Lstat(filepath.Join(data, name))
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Errorf("%s after init is %v (%v), want a directory with the mode 0700", name, info, err)
		}
	}
	before := files(t, data)

	// A directory that is not empty is refused as dbserver.CreateDir says, and nothing is made.
	code, stdout, stderr = runCmd(t, "init", "-data", data)
	want := "whiteroom-db init: " + data + ": not empty; a new data directory needs a missing or empty directory\n"
	if code != 1 || stdout != "" || stderr != want {
		t.Errorf("a second whiteroom-db init = %d, with stdout %q and stderr %q, want 1, nothing and %q", code, stdout, stderr, want)
	}
	if got := files(t, data); !maps.Equal(got, before) {
		t.Errorf("the second init changed the data directory:\n%v\nwas\n%v", got, before)
	}
	notEmpty := filepath.Join(root, "other")
	writeFile(t, notEmpty, "README", "x")
	code, _, stderr = runCmd(t, "init", "-data", notEmpty)
	if code != 1 || !strings.HasSuffix(stderr, ": not empty; a new data directory needs a missing or empty directory\n") {
		t.Errorf("init of a directory that is not empty = %d, with stderr %q", code, stderr)
	}
	if got := files(t, notEmpty); len(got) != 2 {
		t.Errorf("init of a directory that is not empty made %v", got)
	}

	// The mark of a failed sync is a file: a directory that holds only it is not empty, and is left as it is.
	marked := filepath.Join(root, "marked")
	writeFile(t, marked, "stopped", "boot: x\n")
	beforeMarked := files(t, marked)
	code, stdout, stderr = runCmd(t, "init", "-data", marked)
	want = "whiteroom-db init: " + marked + ": not empty; a new data directory needs a missing or empty directory\n"
	if code != 1 || stdout != "" || stderr != want {
		t.Errorf("init of a directory that holds only a mark = %d, with stdout %q and stderr %q, want 1, nothing and %q", code, stdout,
			stderr, want)
	}
	if got := files(t, marked); !maps.Equal(got, beforeMarked) {
		t.Errorf("init changed the directory that holds only a mark:\n%v\nwas\n%v", got, beforeMarked)
	}

	// An empty directory is used as it is.
	empty := filepath.Join(root, "empty")
	if err := os.Mkdir(empty, 0o750); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCmd(t, "init", "-data", empty); code != 0 {
		t.Errorf("init of an empty directory = %d, with stderr %q", code, stderr)
	}
}

func TestInitConfig(t *testing.T) {
	root := t.TempDir()
	conf := filepath.Join(root, "conf")
	file := writeFile(t, conf, "whiteroom.yaml", "data: ../fromfile\nsite: ../site\nout: ../out\n")
	// The data directory of the file is relative to the file, not to the working directory.
	code, stdout, stderr := runCmd(t, "init", "-config", file)
	fromFile := filepath.Join(conf, "..", "fromfile")
	if want := "created the data directory " + fromFile + "\n"; code != 0 || stdout != want || stderr != "" {
		t.Fatalf("whiteroom-db init -config = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, want)
	}
	if _, err := os.Stat(filepath.Join(fromFile, "logdb")); err != nil {
		t.Errorf("the data directory of the file has no logdb: %v", err)
	}

	// The flag wins.
	flagData := filepath.Join(root, "fromflag")
	other := writeFile(t, conf, "other.yaml", "data: ../notmade\n")
	if code, _, stderr := runCmd(t, "init", "-config", other, "-data", flagData); code != 0 {
		t.Fatalf("whiteroom-db init -config -data = %d, with stderr %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(flagData, "logdb")); err != nil {
		t.Errorf("the data directory of the flag has no logdb: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "notmade")); !os.IsNotExist(err) {
		t.Errorf("init made the data directory of the file, which the flag overrides: %v", err)
	}

	// A flag that is given counts also when it is empty: there is no data directory then.
	code, _, stderr = runCmd(t, "init", "-config", other, "-data", "")
	if code != 2 || !strings.HasPrefix(stderr, "whiteroom-db init: no data directory: give -data DIR, or data in the config file\n") {
		t.Errorf("init with an empty -data = %d, with stderr %q, want 2 and the usage error", code, stderr)
	}
	if _, err := os.Lstat(filepath.Join(root, "notmade")); !os.IsNotExist(err) {
		t.Errorf("init made the data directory of the file for an empty -data: %v", err)
	}

	// The items of whiteroom are checked and not used; a problem is reported before anything is made.
	bad := writeFile(t, conf, "bad.yaml", "data: ../baddata\nworkers: -1\n")
	code, stdout, stderr = runCmd(t, "init", "-config", bad)
	if want := bad + ":2:1: /workers: must be an integer of 0 or more\n"; code != 2 || stdout != "" || stderr != want {
		t.Errorf("init with a bad item = %d, with stdout %q and stderr %q, want 2, nothing and %q", code, stdout, stderr, want)
	}
	unknown := writeFile(t, conf, "unknown.yaml", "data: ../baddata\ncolor: red\n")
	code, _, stderr = runCmd(t, "init", "-config", unknown)
	if want := unknown + ":2:1: /color: unknown item\n"; code != 2 || stderr != want {
		t.Errorf("init with an unknown item = %d, with stderr %q, want 2 and %q", code, stderr, want)
	}
	if _, err := os.Lstat(filepath.Join(root, "baddata")); !os.IsNotExist(err) {
		t.Errorf("init made a directory for a file with a problem: %v", err)
	}

	// Without -config no file is read, whatever the working directory has.
	work := t.TempDir()
	for _, name := range []string{"whiteroom.yaml", "config.yaml", "whiteroom-db.yaml"} {
		writeFile(t, work, name, "data:\t/nowhere\n")
	}
	t.Chdir(work)
	if code, _, stderr := runCmd(t, "init", "-data", "made"); code != 0 {
		t.Errorf("init without -config in a directory with files named like config files = %d, with stderr %q", code, stderr)
	}
}
