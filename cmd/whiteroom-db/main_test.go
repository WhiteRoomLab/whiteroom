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
	for _, name := range []string{"compact", "gc"} {
		if !strings.Contains(help[name], "No daemon may have the data directory open") {
			t.Errorf("the usage of whiteroom-db %s does not say that the daemon must be stopped:\n%s", name, help[name])
		}
	}
	// After a failed sync, the three refuse the data directory until the machine has restarted, and then remove the mark.
	for _, name := range []string{"serve", "compact", "gc"} {
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
