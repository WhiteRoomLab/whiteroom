//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver/dbtest"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
	"github.com/WhiteRoomLab/whiteroom/internal/store/storetest"
)

// auditHeadsOf returns the heads of the audit logs of the data directory dir, as a reader of its daemon sees them, in the order
// of the store: a copy that no daemon serves gets a new one, which runs in the process until the last reader is closed.
func auditHeadsOf(t *testing.T, dir string) []audit.Head {
	t.Helper()
	s, err := storetest.OpenReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	heads, err := s.AuditHeads(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return heads
}

// printedHeads checks the stdout of whiteroom backup of the store of the daemon whose sockets are in the directory db into
// dest, a line that tells it and the heads, each as audit.Head prints it, and returns the heads.
func printedHeads(t *testing.T, what, db, dest, stdout string) []audit.Head {
	t.Helper()
	first, rest, _ := strings.Cut(stdout, "\n")
	if want := "backed up the store of " + db + " to " + dest; first != want {
		t.Errorf("%s: whiteroom backup printed the first line %q, want %q", what, first, want)
	}
	var heads []audit.Head
	for line := range strings.Lines(rest) {
		line = strings.TrimSuffix(line, "\n")
		h, err := audit.ParseHead(line)
		if err != nil || h.String() != line {
			t.Errorf("%s: whiteroom backup printed the line %q, which is not a head as audit.Head prints it (%v)", what, line, err)
			continue
		}
		heads = append(heads, h)
	}
	return heads
}

// checkModes checks that every directory below dir has the mode 0700, and every file 0600, and dir itself 0700 if dirToo
// is true.
func checkModes(t *testing.T, what, dir string, dirToo bool) {
	t.Helper()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case p == dir && !dirToo:
		case d.IsDir() && info.Mode().Perm() != 0o700, info.Mode().IsRegular() && info.Mode().Perm() != 0o600,
			!d.IsDir() && !info.Mode().IsRegular():
			t.Errorf("%s: %s has the mode %v, want 0700 for a directory and 0600 for a file", what, p, info.Mode())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// checkCopy checks that dir, a copy that no process has open, is a whole data directory: Check finds no problem, the
// chains of its audit logs agree with heads, and a writer can open it, and adds no record.
func checkCopy(t *testing.T, what, dir string, heads []audit.Head) {
	t.Helper()
	r, err := storetest.OpenReader(dir)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	var problems []string
	err = r.Check(t.Context(), func(p store.Problem) bool {
		problems = append(problems, p.String())
		return true
	})
	if err != nil || len(problems) > 0 {
		t.Errorf("%s: Check of the copy = %v, with the problems %q, want none", what, err, problems)
	}
	if err := r.VerifyAudit(t.Context(), heads); err != nil {
		t.Errorf("%s: VerifyAudit of the copy with the heads that whiteroom backup printed: %v", what, err)
	}
	if err := r.Close(); err != nil {
		t.Error(err)
	}
	checkUnlocked(t, what+": the copy as its writer", dir)
	if got := auditHeadsOf(t, dir); !slices.Equal(got, heads) {
		t.Errorf("%s: the heads of the copy are %v after its writer has opened it, want %v", what, got, heads)
	}
}

// stateOf describes what is at path: "missing", or its entries with their modes and sizes, and for a directory the SHA-256
// of every file, or for a file its bytes.
func stateOf(t *testing.T, path string) string {
	t.Helper()
	s := snapshot(t, path)
	if s == "missing" {
		return s
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() {
		return s + manifestOf(t, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return s + string(data)
}

// failingBackup runs whiteroom backup with args and ctx, and checks that it fails with nothing on stdout, and with one
// line on stderr after the name of the command. It returns the line.
func failingBackup(t *testing.T, what string, ctx context.Context, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(ctx, append([]string{"backup"}, args...), testEnv(strings.NewReader(""), &stdout, &stderr))
	if code != 1 || stdout.Len() > 0 || !strings.HasPrefix(stderr.String(), "whiteroom backup: ") ||
		strings.Count(stderr.String(), "\n") != 1 {
		t.Errorf("%s: whiteroom backup %s = %d, with stdout %q and stderr:\n%s\nwant 1, and one line after the name of the command",
			what, strings.Join(args, " "), code, stdout.String(), stderr.String())
	}
	return stderr.String()
}

// TestBackup copies the store that whiteroom init has made of the sample site into a directory that is missing, and into an
// empty one, through its daemon, and checks the output: a line that tells the copy and the heads of the audit logs, which are
// those of the store and of the copy, which a new daemon opens, nothing on stderr; the modes of the copy; that the files of the
// data directory are as they were, with the hash of each; and that the copy is whole.
func TestBackup(t *testing.T) {
	for _, preexisting := range []bool{false, true} {
		what := "a missing DEST"
		s := initSample(t)
		dest := filepath.Join(t.TempDir(), "backup")
		if preexisting {
			what = "an empty DEST"
			if err := os.Mkdir(dest, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		before, heads := stateOf(t, s.data), auditHeadsOf(t, s.data)
		if len(heads) == 0 {
			t.Fatalf("the data directory of the sample site has no head of an audit log")
		}
		code, stdout, stderr := runCmd(t, "backup", "-db", s.db, dest)
		if code != 0 || stderr != "" {
			t.Fatalf("%s: whiteroom backup = %d, with stderr:\n%s", what, code, stderr)
		}
		printed := printedHeads(t, what, s.db, dest, stdout)
		if !slices.Equal(printed, heads) {
			t.Errorf("%s: whiteroom backup printed the heads %v, the data directory has %v", what, printed, heads)
		}
		if got := auditHeadsOf(t, dest); !slices.Equal(got, heads) {
			t.Errorf("%s: the copy has the heads %v, the data directory %v", what, got, heads)
		}
		checkDirs(t, what, dest, !preexisting)
		checkModes(t, what, dest, !preexisting)
		originals := manifestOf(t, filepath.Join(s.data, "files"))
		if originals == "" {
			t.Fatalf("the data directory of the sample site has no original")
		}
		if got := manifestOf(t, filepath.Join(dest, "files")); got != originals {
			t.Errorf("%s: the originals of the copy are:\n%s\nwant:\n%s", what, got, originals)
		}
		checkCopy(t, what, dest, printed)
		if after := stateOf(t, s.data); after != before {
			t.Errorf("%s: the data directory is changed by the backup:\n%s\nwas:\n%s", what, after, before)
		}
	}
}

// TestBackupHeadsOfTheCopy takes a backup while a commit is made after the daemon has fixed what it copies, as a writer does
// beside a whiteroom serve admin: the heads that the command prints are those of the copy, which a new daemon shows and which
// the audit logs of the copy agree with, and not those of the store, which has moved on.
func TestBackupHeadsOfTheCopy(t *testing.T) {
	var act atomic.Pointer[func() error]
	hook := func(point string) error {
		if f := act.Load(); f != nil && point == "backup:sending" {
			return (*f)()
		}
		return nil
	}
	db := newDBOptions(t, dbtest.Options{Limits: &dbLimits, Hook: hook})
	if code, _, stderr := initDB(t, db, initPassword+"\n", "-theme", exampleTheme, sampleSite); code != 0 {
		t.Fatalf("whiteroom init = %d, with stderr:\n%s", code, stderr)
	}
	live := auditHeadsOf(t, db.data)

	// The daemon lets a commit in once it has fixed the position of the copy, before it sends the files.
	invite := func() error {
		s, err := storetest.Open(db.data)
		if err != nil {
			return err
		}
		m, err := account.NewWithIterations(s, 1)
		if err == nil {
			in := account.Input{Login: "yamada", Name: "Yamada", Email: "yamada@example.com", Role: account.RoleEditor}
			_, _, err = m.Invite(t.Context(), account.System, in)
		}
		return errors.Join(err, s.Close())
	}
	act.Store(&invite)
	dest := filepath.Join(t.TempDir(), "backup")
	code, stdout, stderr := runCmd(t, "backup", "-db", db.sockets, dest)
	act.Store(nil)
	if code != 0 || stderr != "" {
		t.Fatalf("whiteroom backup = %d, with stderr:\n%s", code, stderr)
	}
	if now := auditHeadsOf(t, db.data); slices.Equal(now, live) {
		t.Fatalf("the heads of the store are those of before the backup: the commit of the test was not made")
	}
	printed := printedHeads(t, "a commit while the copy is sent", db.sockets, dest, stdout)
	if !slices.Equal(printed, live) {
		t.Errorf("whiteroom backup printed the heads %v, want those that the store had when the copy was fixed, %v", printed, live)
	}
	checkCopy(t, "a commit while the copy is sent", dest, printed)
}

// TestBackupConfig copies the store of the daemon that a config file gives, with the path of the file from its directory, and
// the DEST from the working directory; -db wins over the item db; and a problem in the file, or no database, is a usage error
// that makes nothing.
func TestBackupConfig(t *testing.T) {
	root, db := storedRoot(t)
	conf := filepath.Join(root, "conf")
	heads := auditHeadsOf(t, db.data)
	work := emptyDir(t)
	help := func() string {
		_, _, help := runCmd(t, "backup", "-h")
		return help
	}()
	missing := filepath.Join(root, "missing-run")

	t.Run("the item db", func(t *testing.T) {
		file := writeConfig(t, conf, "whiteroom.yaml", "# the settings of every command\ndb: ../run\naddr: 127.0.0.1:8081\n"+
			"workers: 2\nmemory: 256\n")
		code, stdout, stderr := runCmd(t, "backup", "-config", file, "copy")
		if code != 0 || stderr != "" {
			t.Fatalf("whiteroom backup -config = %d, with stderr:\n%s", code, stderr)
		}
		if got := printedHeads(t, "the item db", db.sockets, "copy", stdout); !slices.Equal(got, heads) {
			t.Errorf("whiteroom backup -config printed the heads %v, the store has %v", got, heads)
		}
		checkCopy(t, "the item db", filepath.Join(work, "copy"), heads)
		for _, dir := range []string{conf, root} {
			checkMissing(t, "DEST is from the working directory", filepath.Join(dir, "copy"))
		}
	})

	t.Run("the flag wins over the item", func(t *testing.T) {
		bad := writeConfig(t, conf, "bad.yaml", "db: ../missing-run\n")
		code, stdout, stderr := runCmd(t, "backup", "-config", bad, "-db", db.sockets, "copy-flag")
		if code != 0 || stderr != "" {
			t.Fatalf("whiteroom backup -config -db = %d, with the config file of a missing daemon, and stderr:\n%s", code, stderr)
		}
		if got := printedHeads(t, "-db", db.sockets, "copy-flag", stdout); !slices.Equal(got, heads) {
			t.Errorf("whiteroom backup -config -db printed the heads %v, the store has %v", got, heads)
		}

		good := writeConfig(t, conf, "good.yaml", "db: ../run\n")
		if got := failingBackup(t, "-db of a missing directory over the item", t.Context(), "-config", good, "-db", missing,
			"copy-missing"); !strings.HasPrefix(got, "whiteroom backup: "+missing+": cannot reach the daemon: ") {
			t.Errorf("-db of a missing directory over the item: whiteroom backup printed %q, want that the daemon cannot be reached", got)
		}
		checkMissing(t, "-db of a missing directory over the item", filepath.Join(work, "copy-missing"))
	})

	t.Run("usage errors", func(t *testing.T) {
		unknown := writeConfig(t, conf, "unknown.yaml", "db: ../run\nsnack: 1\n")
		workers := writeConfig(t, conf, "workers.yaml", "db: ../run\nworkers: -1\n")
		noDB := writeConfig(t, conf, "no-db.yaml", "addr: 127.0.0.1:8081\n")
		dataOnly := writeConfig(t, conf, "data-only.yaml", "data: ../data\n")
		for _, tt := range []struct {
			what   string
			args   []string
			stderr string
		}{
			{"an unknown item", []string{"-config", unknown, "copy-usage"}, unknown + ":2:1: /snack: unknown item\n"},
			{"an item that the command does not use, with a wrong value", []string{"-config", workers, "copy-usage"},
				workers + ":2:1: /workers: must be an integer of 0 or more\n"},
			{"no item db", []string{"-config", noDB, "copy-usage"}, "whiteroom backup: " + errNoDB + "\n" + help},
			{"the item data, which is the daemon's", []string{"-config", dataOnly, "copy-usage"}, "whiteroom backup: " + errNoDB + "\n" + help},
		} {
			code, stdout, stderr := runCmd(t, append([]string{"backup"}, tt.args...)...)
			if code != 2 || stdout != "" || stderr != tt.stderr {
				t.Errorf("%s: whiteroom backup %s = %d, with stdout %q and stderr:\n%s\nwant 2, and stderr:\n%s", tt.what,
					strings.Join(tt.args, " "), code, stdout, stderr, tt.stderr)
			}
			checkMissing(t, tt.what, filepath.Join(work, "copy-usage"))
		}
	})
}

// buildReleaseFrom builds the sample site from the store of the daemon whose sockets are in the directory db into a new output
// directory and a new cache directory, with the schema file and the theme of site, and returns the name of the release that it
// publishes.
func buildReleaseFrom(t *testing.T, db, site string) string {
	t.Helper()
	out := t.TempDir()
	if code, _, stderr := runCmd(t, "build", "-db", db, "-out", out, "-cache", t.TempDir(), site); code != 0 {
		t.Fatalf("whiteroom build -db %s = %d, with stderr:\n%s", db, code, stderr)
	}
	return currentOf(t, out)
}

// TestBackupBuildsSameRelease takes the copy of a data directory and builds the release that the data directory makes,
// from the copy, from a copy of the copy, as the restored data directory, and that a whiteroom serve admin makes of the
// restored data directory at its start, with the password of the first administrator; and the copy that is taken while a
// whiteroom serve admin runs on the data directory makes the release that it has published.
func TestBackupBuildsSameRelease(t *testing.T) {
	s := initSample(t)
	schemaOnly(t, s.site, true)
	release := buildReleaseFrom(t, s.db, s.site)

	dest := filepath.Join(t.TempDir(), "backup")
	if code, _, stderr := runCmd(t, "backup", "-db", s.db, dest); code != 0 {
		t.Fatalf("whiteroom backup = %d, with stderr:\n%s", code, stderr)
	}
	if got := buildReleaseFrom(t, dbOn(t, dest).sockets, s.site); got != release {
		t.Errorf("the build from the copy makes the release %s, the data directory %s", got, release)
	}

	restored := filepath.Join(t.TempDir(), "restored")
	if err := os.CopyFS(restored, os.DirFS(dest)); err != nil {
		t.Fatal(err)
	}
	restoredDB := dbOn(t, restored)
	if got := buildReleaseFrom(t, restoredDB.sockets, s.site); got != release {
		t.Errorf("the build from the restored data directory makes the release %s, the data directory %s", got, release)
	}
	r := adminSite{data: restored, db: restoredDB.sockets, site: s.site, out: filepath.Join(t.TempDir(), "out"), cache: filepath.Join(t.TempDir(), "cache")}
	a := startAdmin(t, t.Context(), r.args()...)
	cookie := a.login(t)
	if built := a.waitBuilt(t, cookie); failedBuild(built) || built.Last.Release != release {
		t.Errorf("the start build of the restored data directory has %+v, want the release %s\nstderr:\n%s", built.Last, release, a.stderr)
	}
	if got := currentOf(t, r.out); got != release {
		t.Errorf("the server of the restored data directory has published the release %s, want %s", got, release)
	}
	if code := a.stop(t); code != 0 {
		t.Errorf("whiteroom serve admin = %d after its context is done, want 0", code)
	}

	// A copy that is taken while the server runs, after it has published another release.
	a = startAdmin(t, t.Context(), s.args()...)
	cookie = a.login(t)
	if built := a.waitBuilt(t, cookie); failedBuild(built) || built.Last.Release != release {
		t.Fatalf("the start build has %+v, want the release %s\nstderr:\n%s", built.Last, release, a.stderr)
	}
	id := a.publishPage(t, cookie, "en", "A page before the backup", "before-the-backup")
	if built := a.waitPublished(t, cookie, id); failedBuild(built) || built.Last.Release == "" {
		t.Fatalf("the build of the publishing has %+v, want a release\nstderr:\n%s", built.Last, a.stderr)
	}
	second := currentOf(t, s.out)
	if second == release {
		t.Fatalf("the server has published the release %s, as before the page", second)
	}
	live := filepath.Join(t.TempDir(), "live")
	code, stdout, stderr := runCmd(t, "backup", "-db", s.db, live)
	if code != 0 || stderr != "" {
		t.Fatalf("whiteroom backup beside the server = %d, with stderr:\n%s", code, stderr)
	}
	heads := printedHeads(t, "beside the server", s.db, live, stdout)
	if got := buildReleaseFrom(t, dbOn(t, live).sockets, s.site); got != second {
		t.Errorf("the build from the copy that was taken beside the server makes the release %s, the server has published %s", got, second)
	}
	r2, err := storetest.OpenReader(live)
	if err != nil {
		t.Fatal(err)
	}
	var problems []string
	err = r2.Check(t.Context(), func(p store.Problem) bool {
		problems = append(problems, p.String())
		return true
	})
	if err != nil || len(problems) > 0 {
		t.Errorf("Check of the copy that was taken beside the server = %v, with the problems %q, want none", err, problems)
	}
	if err := r2.VerifyAudit(t.Context(), heads); err != nil {
		t.Errorf("VerifyAudit of the copy that was taken beside the server, with its heads: %v", err)
	}
	if err := r2.Close(); err != nil {
		t.Error(err)
	}
	if code := a.stop(t); code != 0 {
		t.Errorf("whiteroom serve admin = %d after its context is done, want 0", code)
	}
}

// TestBackupErrors checks the backups that fail, which print one line on stderr and nothing on stdout, and leave the data
// directory as it was: a DEST that is not missing or empty is not changed; a DEST without a parent, a daemon that cannot be
// reached, and a DEST that is the data directory make nothing; a DEST that is made but whose name cannot be synced is removed,
// while one that was there is used; and a backup that fails after it has begun, by a context that is done or an original that
// cannot be read, leaves DEST as it was, and can be tried again.
func TestBackupErrors(t *testing.T) {
	t.Parallel()
	const need = "; DEST must be missing or an empty directory\n"

	t.Run("a DEST that is not missing or empty", func(t *testing.T) {
		s := initSample(t)
		old := filepath.Join(t.TempDir(), "old")
		if code, _, stderr := runCmd(t, "backup", "-db", s.db, old); code != 0 {
			t.Fatalf("whiteroom backup = %d, with stderr:\n%s", code, stderr)
		}
		withFile, withDir := t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(withFile, "notes.txt"), []byte("not a backup"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(withDir, "logdb"), 0o700); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		before := stateOf(t, s.data)
		for _, tt := range []struct{ what, dest, stderr string }{
			{"an old copy", old, "whiteroom backup: " + old + ": not empty" + need},
			{"a directory with a file", withFile, "whiteroom backup: " + withFile + ": not empty" + need},
			{"a directory with an empty logdb", withDir, "whiteroom backup: " + withDir + ": not empty" + need},
			{"a file", file, "whiteroom backup: " + file + ": not a directory" + need},
		} {
			dest := stateOf(t, tt.dest)
			if got := failingBackup(t, tt.what, t.Context(), "-db", s.db, tt.dest); got != tt.stderr {
				t.Errorf("%s: whiteroom backup printed %q, want %q", tt.what, got, tt.stderr)
			}
			if after := stateOf(t, tt.dest); after != dest {
				t.Errorf("%s: DEST is changed:\n%s\nwas:\n%s", tt.what, after, dest)
			}
		}
		if after := stateOf(t, s.data); after != before {
			t.Errorf("the data directory is changed:\n%s\nwas:\n%s", after, before)
		}
	})

	t.Run("a DEST without a parent", func(t *testing.T) {
		s := initSample(t)
		parent := filepath.Join(t.TempDir(), "no-parent")
		dest := filepath.Join(parent, "backup")
		before := stateOf(t, s.data)
		got := failingBackup(t, "a DEST without a parent", t.Context(), "-db", s.db, dest)
		if !strings.Contains(got, "no such file or directory") {
			t.Errorf("a DEST without a parent: whiteroom backup printed %q, want a missing file or directory", got)
		}
		for _, name := range []string{parent, dest} {
			checkMissing(t, "a DEST without a parent", name)
		}
		if after := stateOf(t, s.data); after != before {
			t.Errorf("a DEST without a parent: the data directory is changed:\n%s\nwas:\n%s", after, before)
		}
	})

	t.Run("a DEST whose name cannot be synced", func(t *testing.T) {
		const what = "a DEST whose name cannot be synced"
		if os.Geteuid() == 0 {
			t.Skip("root can open every directory")
		}
		s := initSample(t)
		// Write and search, but not read: DEST can be made in it, but it cannot be opened to sync the name of DEST.
		parent := t.TempDir()
		there := filepath.Join(parent, "there")
		if err := os.Mkdir(there, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(parent, 0o300); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
		dest := filepath.Join(parent, "backup")
		before := stateOf(t, s.data)
		got := failingBackup(t, what, t.Context(), "-db", s.db, dest)
		if want := "whiteroom backup: open " + parent + ": permission denied\n"; got != want {
			t.Errorf("%s: whiteroom backup printed %q, want %q", what, got, want)
		}
		checkMissing(t, what, dest)
		if after := stateOf(t, s.data); after != before {
			t.Errorf("%s: the data directory is changed:\n%s\nwas:\n%s", what, after, before)
		}
		// The name of a DEST that was there is not synced.
		if code, _, stderr := runCmd(t, "backup", "-db", s.db, there); code != 0 {
			t.Errorf("a DEST that was there, in the same directory: whiteroom backup = %d, with stderr:\n%s", code, stderr)
		}
	})

	t.Run("a daemon that cannot be reached", func(t *testing.T) {
		// DEST is not made: the command connects to the daemon first. Each case waits for the client's 2 seconds, so they
		// run together, each with a DEST of its own.
		for _, tt := range []struct {
			what string
			db   func(t *testing.T) string
		}{
			{"a missing directory", func(t *testing.T) string { return filepath.Join(t.TempDir(), "missing") }},
			{"an empty directory", func(t *testing.T) string { return t.TempDir() }},
		} {
			for _, destKind := range []string{"missing", "empty"} {
				t.Run(tt.what+", DEST "+destKind, func(t *testing.T) {
					t.Parallel()
					db, dest := tt.db(t), filepath.Join(t.TempDir(), "dest")
					if destKind == "empty" {
						if err := os.Mkdir(dest, 0o755); err != nil {
							t.Fatal(err)
						}
					}
					was := stateOf(t, dest)
					got := failingBackup(t, tt.what, t.Context(), "-db", db, dest)
					if want := "whiteroom backup: " + db + ": cannot reach the daemon: "; !strings.HasPrefix(got, want) ||
						!strings.Contains(got, filepath.Join(db, "reader.sock")) {
						t.Errorf("%s: whiteroom backup printed %q, want that the daemon cannot be reached, with the name of the socket", tt.what, got)
					}
					if destKind == "missing" {
						checkMissing(t, tt.what+", DEST missing", dest)
					} else if after := stateOf(t, dest); after != was {
						t.Errorf("%s: the empty DEST is now:\n%s\nwas:\n%s", tt.what, after, was)
					}
				})
			}
		}
	})

	t.Run("what another process puts in DEST meanwhile", func(t *testing.T) {
		// DEST is empty when the command looks at it, and the command waits 2 seconds for a daemon that cannot be reached:
		// the failure removes nothing that it has not made.
		t.Parallel()
		db, dest := t.TempDir(), t.TempDir()
		done := async(func() string {
			return failingBackup(t, "a daemon that cannot be reached", t.Context(), "-db", db, dest)
		})
		time.Sleep(500 * time.Millisecond)
		keep := filepath.Join(dest, "files", "keep")
		if err := os.MkdirAll(filepath.Dir(keep), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keep, []byte("not the command's"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := receive(t, done, "whiteroom backup"); !strings.Contains(got, "cannot reach the daemon") {
			t.Errorf("whiteroom backup printed %q, want that the daemon cannot be reached", got)
		}
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("what another process put in DEST: %v, want it kept", err)
		}
	})

	t.Run("a DEST that is the data directory", func(t *testing.T) {
		// It is not empty, which the command finds before it connects to the daemon.
		s := initSample(t)
		before := stateOf(t, s.data)
		want := "whiteroom backup: " + s.data + ": not empty; DEST must be missing or an empty directory\n"
		if got := failingBackup(t, "the data directory", t.Context(), "-db", s.db, s.data); got != want {
			t.Errorf("whiteroom backup printed %q, want %q", got, want)
		}
		if after := stateOf(t, s.data); after != before {
			t.Errorf("the data directory is changed:\n%s\nwas:\n%s", after, before)
		}
	})

	t.Run("a DEST in the data directory", func(t *testing.T) {
		// The daemon sends the files of its data directory as it finds them, and would send what the command writes there
		// too (#124): the command compares DEST and its parents with the data directory that the daemon tells.
		s := initSample(t)
		empty := filepath.Join(s.data, "empty")
		if err := os.Mkdir(empty, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(filepath.Join(s.data, "files"), link); err != nil {
			t.Fatal(err)
		}
		before := stateOf(t, s.data)
		for _, tt := range []struct{ what, dest string }{
			{"a missing DEST in files", filepath.Join(s.data, "files", "b")},
			{"a missing DEST in the data directory", filepath.Join(s.data, "elsewhere")},
			{"an empty DEST in the data directory", empty},
			{"a DEST through a symbolic link", filepath.Join(link, "b")},
		} {
			want := "whiteroom backup: " + tt.dest + ": DEST is in the data directory of the daemon\n"
			if got := failingBackup(t, tt.what, t.Context(), "-db", s.db, tt.dest); got != want {
				t.Errorf("%s: whiteroom backup printed %q, want %q", tt.what, got, want)
			}
		}
		if after := stateOf(t, s.data); after != before {
			t.Errorf("the data directory is changed:\n%s\nwas:\n%s", after, before)
		}
		// Beside the data directory is outside it.
		if code, _, stderr := runCmd(t, "backup", "-db", s.db, s.data+"-copy"); code != 0 {
			t.Errorf("a DEST beside the data directory: whiteroom backup = %d, with stderr:\n%s", code, stderr)
		}
	})

	t.Run("a context that is done", func(t *testing.T) {
		s := initSample(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		before := stateOf(t, s.data)
		missing := filepath.Join(t.TempDir(), "backup")
		empty := filepath.Join(t.TempDir(), "backup")
		if err := os.Mkdir(empty, 0o755); err != nil {
			t.Fatal(err)
		}
		emptied := stateOf(t, empty)
		const want = "whiteroom backup: interrupted\n"
		for _, dest := range []string{missing, empty} {
			if got := failingBackup(t, "a context that is done", ctx, "-db", s.db, dest); got != want {
				t.Errorf("a context that is done: whiteroom backup printed %q, want %q", got, want)
			}
		}
		checkMissing(t, "a context that is done, DEST missing", missing)
		if after := stateOf(t, empty); after != emptied {
			t.Errorf("a context that is done: the empty DEST is now:\n%s\nwas:\n%s", after, emptied)
		}
		if after := stateOf(t, s.data); after != before {
			t.Errorf("a context that is done: the data directory is changed:\n%s\nwas:\n%s", after, before)
		}
	})

	t.Run("an original that cannot be read", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root: an original that cannot be read is skipped")
		}
		for _, preexisting := range []bool{false, true} {
			what := "a missing DEST"
			s := initSample(t)
			dest := filepath.Join(t.TempDir(), "backup")
			if preexisting {
				what = "an empty DEST"
				if err := os.Mkdir(dest, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			var original string
			err := filepath.WalkDir(filepath.Join(s.data, "files"), func(p string, d fs.DirEntry, err error) error {
				if err == nil && original == "" && d.Type().IsRegular() {
					original = p
				}
				return err
			})
			if err != nil || original == "" {
				t.Fatalf("an original of the data directory of the sample site: %q, %v", original, err)
			}
			before, destBefore := stateOf(t, s.data), stateOf(t, dest)
			if err := os.Chmod(original, 0); err != nil {
				t.Fatal(err)
			}
			got := failingBackup(t, what, t.Context(), "-db", s.db, dest)
			if err := os.Chmod(original, 0o600); err != nil {
				t.Fatal(err)
			}
			rel, err := filepath.Rel(s.data, original)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got, filepath.ToSlash(rel)) || !strings.Contains(got, "permission denied") {
				t.Errorf("%s: whiteroom backup printed %q, want the original %s and a permission that is denied", what, got, rel)
			}
			if after := stateOf(t, dest); after != destBefore {
				t.Errorf("%s: DEST is now:\n%s\nwas:\n%s", what, after, destBefore)
			}
			if after := stateOf(t, s.data); after != before {
				t.Errorf("%s: the data directory is changed:\n%s\nwas:\n%s", what, after, before)
			}

			// Again, with the same arguments, now that the original can be read.
			code, stdout, stderr := runCmd(t, "backup", "-db", s.db, dest)
			if code != 0 || stderr != "" {
				t.Fatalf("%s: whiteroom backup again = %d, with stderr:\n%s", what, code, stderr)
			}
			checkCopy(t, what+", again", dest, printedHeads(t, what+", again", s.db, dest, stdout))
		}
	})
}

// linkedDecoy makes work/link, a symbolic link to the directory other/sub, and work/x, a data directory that has a file in
// logdb/ and in files/. The path work/link/../x is for the kernel other/x, which is missing, and not work/x.
func linkedDecoy(t *testing.T) (work, other string) {
	t.Helper()
	work, other = t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(other, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(other, "sub"), filepath.Join(work, "link")); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"logdb", "files"} {
		if err := os.MkdirAll(filepath.Join(work, "x", dir), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(work, "x", dir, "keep"), []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return work, other
}

// TestBackupResolvedPaths checks whiteroom backup with the paths as the kernel resolves them, which are not those that a
// clean of the text gives when a ".." follows a symbolic link, or the working directory is entered through one: the cleanup
// of a backup that fails removes what it made in the directory that DEST is for the kernel, and leaves the directory beside
// the link; a missing DEST with a trailing separator, and a relative DEST from a working directory that is a link, take the
// backup; and a DEST that is in the data directory for the kernel is refused, and nothing is made in it.
func TestBackupResolvedPaths(t *testing.T) {
	t.Run("the cleanup of a backup that fails", func(t *testing.T) {
		s := initSample(t)
		work, other := linkedDecoy(t)
		decoy := stateOf(t, filepath.Join(work, "x"))
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		dest := filepath.Join(work, "link") + "/../x"
		if got, want := failingBackup(t, "the cleanup", ctx, "-db", s.db, dest), "whiteroom backup: interrupted\n"; got != want {
			t.Errorf("whiteroom backup printed %q, want %q", got, want)
		}
		if after := stateOf(t, filepath.Join(work, "x")); after != decoy {
			t.Errorf("the directory beside the link is changed:\n%s\nwas:\n%s", after, decoy)
		}
		checkMissing(t, "the directory that DEST is for the kernel", filepath.Join(other, "x"))
	})

	t.Run("a missing DEST with a trailing separator", func(t *testing.T) {
		for _, suffix := range []string{"/", "//"} {
			s := initSample(t)
			dest := filepath.Join(t.TempDir(), "backup") + suffix
			code, stdout, stderr := runCmd(t, "backup", "-db", s.db, dest)
			if code != 0 || stderr != "" {
				t.Fatalf("whiteroom backup of DEST %q = %d, with stderr:\n%s", dest, code, stderr)
			}
			checkCopy(t, "DEST "+dest, dest, printedHeads(t, "DEST "+dest, s.db, dest, stdout))
		}
	})

	t.Run("a working directory that is a link", func(t *testing.T) {
		s := initSample(t)
		site, releases := t.TempDir(), t.TempDir()
		for _, name := range []string{"42", "backups"} {
			if err := os.Mkdir(filepath.Join(releases, name), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		current := filepath.Join(site, "current")
		if err := os.Symlink(filepath.Join(releases, "42"), current); err != nil {
			t.Fatal(err)
		}
		t.Chdir(current)
		code, stdout, stderr := runCmd(t, "backup", "-db", s.db, "../backups/b1")
		if code != 0 || stderr != "" {
			t.Fatalf("whiteroom backup of ../backups/b1 = %d, with stderr:\n%s", code, stderr)
		}
		checkCopy(t, "../backups/b1", filepath.Join(releases, "backups", "b1"),
			printedHeads(t, "../backups/b1", s.db, "../backups/b1", stdout))
		checkMissing(t, "the directory beside the link", filepath.Join(site, "backups"))
	})
}
