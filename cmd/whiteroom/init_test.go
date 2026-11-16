//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package main

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver/dbtest"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
	"github.com/WhiteRoomLab/whiteroom/internal/store/storetest"
)

const (
	initPassword = "violet kettle harbour lantern"
	initLogin    = "admin"
	initName     = "Site Admin"
	initEmail    = "admin@example.com"
)

// adminFlags returns the flags that give the first administrator, and then extra.
func adminFlags(extra ...string) []string {
	return append([]string{"-login", initLogin, "-name", initName, "-email", initEmail}, extra...)
}

// runInitCmd runs whiteroom init with args and the text stdin as the standard input.
func runInitCmd(t *testing.T, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	return runCmdIn(t, strings.NewReader(stdin), append([]string{"init"}, args...)...)
}

// initHelp returns the usage that whiteroom init -h prints, which the usage errors print too.
func initHelp(t *testing.T) string {
	t.Helper()
	code, stdout, help := runCmd(t, "init", "-h")
	if code != 0 || stdout != "" || !strings.HasPrefix(help, initUsage) {
		t.Fatalf("whiteroom init -h = %d, with stdout %q and stderr:\n%s\nwant 0, and the usage on stderr", code, stdout, help)
	}
	return help
}

// snapshot describes what is at path, the entries below it with their modes and sizes, or "missing".
func snapshot(t *testing.T, path string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(path, p)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s %v %d\n", rel, info.Mode(), info.Size())
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return "missing"
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// dataHas reports whether a file below the data directory dir has the bytes needle.
func dataHas(t *testing.T, dir, needle string) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		found = found || bytes.Contains(data, []byte(needle))
		return err
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return found
}

// checkNoSecret checks that the secret is not in what a command printed, nor in a file of the data directory dir.
func checkNoSecret(t *testing.T, what, secret, stdout, stderr, dir string) {
	t.Helper()
	if strings.Contains(stdout, secret) || strings.Contains(stderr, secret) {
		t.Errorf("%s: the secret is in the output:\n%s\n%s", what, stdout, stderr)
	}
	if dataHas(t, dir, secret) {
		t.Errorf("%s: the secret is in a file of %s", what, dir)
	}
}

// initialized is what the data directory that init has made holds, as the accounts and the store of the tests see it.
type initialized struct {
	accounts []account.Account
	records  []audit.Record // of the activity log
	docs     map[string]string
}

var documentKinds = []content.RefKind{
	store.SettingsKind, content.RefEntry, content.RefTerm, content.RefMedia, content.RefAuthor, content.RefMenu,
	content.RefForm, content.RefRedirect,
}

// documentsOf returns the current documents of s, with who saved them, by their kinds and IDs.
func documentsOf(t *testing.T, s *store.Store) map[string]string {
	t.Helper()
	docs := make(map[string]string)
	err := s.View(t.Context(), func(tx *store.Tx) error {
		for _, kind := range documentKinds {
			list, err := tx.Documents(kind)
			if err != nil {
				return err
			}
			for _, d := range list {
				docs[string(kind)+"/"+d.ID] = d.SavedBy + " " + string(d.Data)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return docs
}

// inspect opens the data directory dir, closes it again, and returns what it holds. If it has an account, the password
// must be that of its login name.
func inspect(t *testing.T, dir, password string) initialized {
	t.Helper()
	s, err := storetest.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	m, err := account.NewWithIterations(s, 1)
	if err != nil {
		t.Fatal(err)
	}
	var got initialized
	if got.accounts, err = m.List(t.Context()); err != nil {
		t.Fatal(err)
	}
	err = s.ReadAudit(t.Context(), audit.Filter{Log: audit.ActivityLog}, func(r audit.Record) bool {
		got.records = append(got.records, r)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	got.docs = documentsOf(t, s)
	if len(got.accounts) == 1 {
		login := got.accounts[0].Login
		if _, err := m.VerifyPasswordFactor(t.Context(), login, password); err != nil {
			t.Errorf("the account %s cannot log in with the password: %v", login, err)
		}
		if _, err := m.VerifyPasswordFactor(t.Context(), login, password+"x"); !errors.Is(err, account.ErrBadCredentials) {
			t.Errorf("the account %s can log in with another password: %v", login, err)
		}
	}
	return got
}

// checkAdmin checks that got has only the first administrator, as the flags of adminFlags give it, and returns it.
func checkAdmin(t *testing.T, what string, got initialized) account.Account {
	t.Helper()
	if len(got.accounts) != 1 {
		t.Fatalf("%s: %d accounts, want 1", what, len(got.accounts))
	}
	a := got.accounts[0]
	if a.Login != initLogin || a.Name != initName || a.Email != initEmail || a.Role != account.RoleAdmin ||
		a.Status != account.StatusActive || a.CreatedBy != "" || a.Author != "" || content.CheckID(content.RefUser, a.ID) != nil {
		t.Errorf("%s: the account is %+v, want an active administrator %s, %q, %s with a user ID", what, a, initLogin,
			initName, initEmail)
	}
	return a
}

// checkRecords checks the actions and the actors of the records of the activity log.
func checkRecords(t *testing.T, what string, got []audit.Record, actions []audit.Action, actors []string) {
	t.Helper()
	var gotActions []audit.Action
	var gotActors []string
	for _, r := range got {
		gotActions = append(gotActions, r.Action)
		gotActors = append(gotActors, r.Actor)
	}
	if !slices.Equal(gotActions, actions) || !slices.Equal(gotActors, actors) {
		t.Errorf("%s: the activity log has the actions %v by %v, want %v by %v", what, gotActions, gotActors, actions, actors)
	}
}

// checkDirs checks that the data directory dir has logdb/ and files/ of the mode 0700, and itself too if dir0700 is true.
func checkDirs(t *testing.T, what, dir string, dir0700 bool) {
	t.Helper()
	paths := []string{filepath.Join(dir, "logdb"), filepath.Join(dir, "files")}
	if dir0700 {
		paths = append(paths, dir)
	}
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Errorf("%s: %s is %v (%v), want a directory of the mode 0700", what, p, info, err)
		}
	}
}

// initDB runs whiteroom init on the daemon db, with the first administrator, the flags more, and the text stdin as the
// standard input.
func initDB(t *testing.T, db testDB, stdin string, more ...string) (code int, stdout, stderr string) {
	t.Helper()
	return runInitCmd(t, stdin, adminFlags(append([]string{"-db", db.sockets}, more...)...)...)
}

// wantInitialized is what whiteroom init prints for the daemon db, with the administrator admin and, if site is not "", the
// site that it imports.
func wantInitialized(db testDB, admin account.Account, site string) string {
	want := "initialized the store of " + db.sockets + "\ncreated the administrator " + initLogin + " (" + admin.ID + ")\n"
	if site != "" {
		want += "imported the site " + site + "\n"
	}
	return want
}

// checkStoreMade checks that the store of db has been made and has no account and no document: what a whiteroom init that
// failed before it made the administrator leaves, which the next init uses.
func checkStoreMade(t *testing.T, what string, db testDB) {
	t.Helper()
	got := inspect(t, db.data, initPassword)
	checkRecords(t, what, got.records, []audit.Action{audit.StoreCreate}, []string{audit.System})
	if len(got.accounts) != 0 || len(got.docs) != 0 {
		t.Errorf("%s: the store has %d accounts and %d documents, want none", what, len(got.accounts), len(got.docs))
	}
}

// TestInit makes the first administrator of new stores with the password on a pipe, in the ways that the input can end. It
// checks the output, the one account, who can log in, the records, that there are no documents, the modes of the data
// directory, the commits that init makes, and that nothing has the password.
func TestInit(t *testing.T) {
	for _, tt := range []struct{ what, stdin string }{
		{"a line", initPassword + "\n"},
		{"a line that ends with CR LF", initPassword + "\r\n"},
		{"the end of the input", initPassword},
		{"more lines", initPassword + "\nthe rest\n"},
	} {
		db := newDB(t)
		if n := commitsOf(t, db); n != 0 {
			t.Fatalf("%s: a new store has the commit %d, want none", tt.what, n)
		}
		code, stdout, stderr := initDB(t, db, tt.stdin)
		if code != 0 || stderr != "" {
			t.Fatalf("%s: whiteroom init = %d, with stderr:\n%s", tt.what, code, stderr)
		}
		got := inspect(t, db.data, initPassword)
		admin := checkAdmin(t, tt.what, got)
		if want := wantInitialized(db, admin, ""); stdout != want {
			t.Errorf("%s: whiteroom init printed:\n%s\nwant:\n%s", tt.what, stdout, want)
		}
		checkRecords(t, tt.what, got.records, []audit.Action{audit.StoreCreate, audit.AccountCreateFirst},
			[]string{audit.System, audit.System})
		if len(got.docs) != 0 {
			t.Errorf("%s: the store has documents %v, want none", tt.what, slices.Sorted(maps.Keys(got.docs)))
		}
		if n := commitsOf(t, db); n != 2 {
			t.Errorf("%s: init made the commits up to %d, want 2: the store and the administrator", tt.what, n)
		}
		checkDirs(t, tt.what, db.data, true)
		checkNoSecret(t, tt.what, initPassword, stdout, stderr, db.data)
		if !dataHas(t, db.data, initLogin) {
			t.Errorf("%s: the scan of %s does not find the login name, which the data has", tt.what, db.data)
		}
	}
}

// sampleImportWarnings returns the warnings that an import of the sample site in site prints: those of its redirects,
// with the paths of their files from the working directory.
func sampleImportWarnings(t *testing.T, site string) string {
	t.Helper()
	var b strings.Builder
	for line := range strings.Lines(readGolden(t, "warnings.txt")) {
		if strings.HasPrefix(line, "redirects/") && strings.HasSuffix(line, " is not published; the redirect is left out\n") {
			file, rest, _ := strings.Cut(line, ":")
			b.WriteString(filepath.Join(site, filepath.FromSlash(file)) + ":" + rest)
		}
	}
	return b.String()
}

// TestInitImport initializes a store with the sample site, with the example theme of -theme: the documents are those of
// edit.Import, saved by the administrator, who also does the import in the records, and the warnings are those of the
// redirects, printed once; init makes 3 commits, and the trial import of the site, which comes before them, none.
func TestInitImport(t *testing.T) {
	db := newDB(t)
	code, stdout, stderr := initDB(t, db, initPassword+"\n", "-theme", exampleTheme, sampleSite)
	if code != 0 {
		t.Fatalf("whiteroom init of the sample site = %d, with stderr:\n%s", code, stderr)
	}
	if want := sampleImportWarnings(t, sampleSite); want == "" || stderr != want {
		t.Errorf("whiteroom init printed the warnings:\n%s\nwant:\n%s", stderr, want)
	}
	got := inspect(t, db.data, initPassword)
	admin := checkAdmin(t, "the sample site", got)
	if want := wantInitialized(db, admin, sampleSite); stdout != want {
		t.Errorf("whiteroom init printed:\n%s\nwant:\n%s", stdout, want)
	}
	checkRecords(t, "the sample site", got.records,
		[]audit.Action{audit.StoreCreate, audit.AccountCreateFirst, audit.SiteImport},
		[]string{audit.System, audit.System, admin.ID})
	if n := commitsOf(t, db); n != 3 {
		t.Errorf("init made the commits up to %d, want 3: the store, the administrator and the site", n)
	}
	checkDirs(t, "the sample site", db.data, true)
	checkNoSecret(t, "the sample site", initPassword, stdout, stderr, db.data)

	// The documents are those that edit.Import stores of the same site for the same user.
	src, closeSite, err := openSite(sampleSite, exampleTheme)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSite()
	ref, err := storetest.Create(filepath.Join(t.TempDir(), "ref"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ref.Close() }()
	if _, err := edit.Import(t.Context(), ref, src, admin.ID); err != nil {
		t.Fatal(err)
	}
	want := documentsOf(t, ref)
	if len(want) < 10 || !maps.Equal(got.docs, want) {
		t.Errorf("the store has %d documents, which are not the %d that edit.Import stores", len(got.docs), len(want))
	}
	for key, doc := range got.docs {
		if saver, _, _ := strings.Cut(doc, " "); saver != admin.ID {
			t.Errorf("%s is saved by %s, want %s", key, saver, admin.ID)
		}
	}
	kinds := make(map[string]bool)
	for key := range got.docs {
		kind, _, _ := strings.Cut(key, "/")
		kinds[kind] = true
	}
	if len(kinds) != len(documentKinds) {
		t.Errorf("the store has documents of the kinds %v, want all of %d kinds", slices.Sorted(maps.Keys(kinds)), len(documentKinds))
	}
}

// emailLine is the line that whiteroom init prints for the email address nobody, which is not one.
func emailLine() string {
	return "whiteroom init: -email: " + content.CheckEmail("nobody").Error() + "\n"
}

// initCase is a whiteroom init that fails, with the text on its standard input, its exit code, and what it prints on
// stderr.
type initCase struct {
	what string
	// args returns the arguments after init, for the daemon db.
	args  func(t *testing.T, db testDB) []string
	stdin string
	// unread says that the command must fail before it asks for the password: its standard input is not read.
	unread bool
	secret string // that must not appear anywhere; initPassword if ""
	code   int
	// stderr is what it prints; if prefixes is true, it is the prefixes of the lines.
	stderr   func(db testDB) []string
	prefixes bool
}

// TestInitErrors checks the whiteroom init that fail: the exit code, the lines of stderr, that the standard input is not read
// where the command fails before the password, what the store is after it: not made after a usage error, and made, with no
// account and no document, after any other failure; that init can be run again at once on that store; and that no password is
// in the output or in the data directory.
func TestInitErrors(t *testing.T) {
	help := initHelp(t)
	usageErr := func(msg string) func(testDB) []string {
		return func(testDB) []string { return []string{"whiteroom init: " + msg + "\n" + help} }
	}
	line := func(format string, args ...any) func(testDB) []string {
		return func(testDB) []string { return []string{"whiteroom init: " + fmt.Sprintf(format, args...) + "\n"} }
	}
	flagsOnly := func(extra ...string) func(*testing.T, testDB) []string {
		return func(t *testing.T, db testDB) []string {
			return adminFlags(append([]string{"-db", db.sockets}, extra...)...)
		}
	}
	missing := filepath.Join(t.TempDir(), "missing")
	siteEmpty := t.TempDir()
	siteNoTheme := copySample(t)
	if err := os.RemoveAll(filepath.Join(siteNoTheme, "theme")); err != nil {
		t.Fatal(err)
	}
	siteBroken := copySample(t)
	revision := filepath.Join(siteBroken, "revisions", "rev_01JB8Y6T2RG7M4XK9P2D5W8NQ3.json")
	original, err := os.ReadFile(revision)
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(original), "  \"title\": \"サービス\",\n", "", 1)
	if broken == string(original) {
		t.Fatalf("%s has no title サービス", revision)
	}
	if err := os.WriteFile(revision, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	shortSecret := "qwerty"
	sameAsLogin := "administrator-one"
	long := strings.Repeat("p", maxPasswordLine+1)

	cases := []initCase{
		{"no -login", func(t *testing.T, db testDB) []string {
			return []string{"-name", initName, "-email", initEmail, "-db", db.sockets}
		}, initPassword + "\n", true, "", 2, usageErr("-login is required"), false},
		{"an empty -login", func(t *testing.T, db testDB) []string {
			return []string{"-login", "", "-name", initName, "-email", initEmail, "-db", db.sockets}
		}, initPassword + "\n", true, "", 2, usageErr("-login is required"), false},
		{"no -name", func(t *testing.T, db testDB) []string {
			return []string{"-login", initLogin, "-email", initEmail, "-db", db.sockets}
		}, initPassword + "\n", true, "", 2, usageErr("-name is required"), false},
		{"no -email", func(t *testing.T, db testDB) []string {
			return []string{"-login", initLogin, "-name", initName, "-db", db.sockets}
		}, initPassword + "\n", true, "", 2, usageErr("-email is required"), false},
		{"no database", func(t *testing.T, db testDB) []string { return adminFlags() },
			initPassword + "\n", true, "", 2, usageErr(errNoDB), false},
		{"an empty -db", func(t *testing.T, db testDB) []string { return adminFlags("-db", "") },
			initPassword + "\n", true, "", 2, usageErr(errNoDB), false},
		{"two arguments", flagsOnly("a", "b"), initPassword + "\n", true, "", 2,
			usageErr("too many arguments: give at most one SITE, after the flags"), false},
		{"-theme without SITE", flagsOnly("-theme", exampleTheme), initPassword + "\n", true, "", 2,
			usageErr("-theme is for SITE: give the directory SITE of the site to import"), false},

		{"a missing site", flagsOnly(missing), initPassword + "\n", true, "", 1,
			line("open %s: no such file or directory", missing), false},
		{"an empty site directory", flagsOnly(siteEmpty), initPassword + "\n", true, "", 1,
			func(testDB) []string {
				return []string{filepath.Join(siteEmpty, "site", "schema.yaml") + ": missing file\n" +
					filepath.Join(siteEmpty, "site", "settings.json") + ": missing file\n"}
			}, false},
		{"a site without a theme", flagsOnly(siteNoTheme), initPassword + "\n", true, "", 1,
			func(testDB) []string { return []string{filepath.Join(siteNoTheme, "theme") + ": missing directory\n"} }, false},
		{"a missing theme", flagsOnly("-theme", missing, sampleSite), initPassword + "\n", true, "", 1,
			line("open %s: no such file or directory", missing), false},
		{"a site with a broken document", flagsOnly(siteBroken), initPassword + "\n", true, "", 1,
			func(testDB) []string { return []string{revision + ":1:1: /title: missing title\n"} }, false},

		{"no standard input", flagsOnly(), "", false, "", 1, line("no password was read from the standard input"), false},
		{"an empty line", flagsOnly(), "\n" + initPassword + "\n", false, "", 1,
			line("no password was read from the standard input"), false},
		{"a line that is too long", flagsOnly(), long + "\n", false, long[:20], 1,
			line("the password is longer than %d bytes", maxPasswordLine), false},

		{"a short password", flagsOnly(), shortSecret + "\n" + initPassword + "\n", false, shortSecret, 1,
			line("the password: too short: %d characters (min 15)", len(shortSecret)), false},
		{"the login name as the password", func(t *testing.T, db testDB) []string {
			return adminFlags("-login", sameAsLogin, "-db", db.sockets)
		}, sameAsLogin + "\n", false, sameAsLogin, 1,
			line("the password: the same as the login name, the email address or the name"), false},
		{"an invalid -email", flagsOnly("-email", "nobody"), initPassword + "\n", false, "", 1,
			func(testDB) []string { return []string{emailLine()} }, false},
		{"an invalid -login", flagsOnly("-login", "Bad Login"), initPassword + "\n", false, "", 1,
			func(testDB) []string { return []string{"whiteroom init: -login: invalid login name \"bad login\": "} }, true},
		{"problems in the flags and the password", flagsOnly("-login", "Bad Login", "-email", "nobody"),
			shortSecret + "\n", false, shortSecret, 1,
			func(testDB) []string {
				return []string{
					"whiteroom init: -login: invalid login name \"bad login\": ",
					strings.TrimSuffix(emailLine(), "\n"),
					fmt.Sprintf("whiteroom init: the password: too short: %d characters (min 15)", len(shortSecret)),
				}
			}, true},
	}

	for _, tt := range cases {
		db := newDB(t)
		args := tt.args(t, db)
		before := snapshot(t, db.data)
		var stdin io.Reader = strings.NewReader(tt.stdin)
		if tt.unread {
			stdin = failingReader{t}
		}
		var o, e bytes.Buffer
		code := run(t.Context(), append([]string{"init"}, args...), testEnv(stdin, &o, &e))
		stdout, stderr := o.String(), e.String()
		want := tt.stderr(db)
		switch {
		case code != tt.code || stdout != "":
			t.Errorf("%s: whiteroom init = %d, with stdout %q and stderr:\n%s\nwant %d, and nothing on stdout", tt.what, code,
				stdout, stderr, tt.code)
		case !tt.prefixes && stderr != strings.Join(want, ""):
			t.Errorf("%s: whiteroom init printed:\n%s\nwant:\n%s", tt.what, stderr, strings.Join(want, ""))
		case tt.prefixes:
			lines := strings.SplitAfter(stderr, "\n")
			lines = lines[:len(lines)-1]
			if len(lines) != len(want) {
				t.Errorf("%s: whiteroom init printed %d lines:\n%s\nwant %d, those that start with %q", tt.what, len(lines), stderr,
					len(want), want)
				break
			}
			for i, l := range lines {
				if !strings.HasPrefix(l, want[i]) {
					t.Errorf("%s: line %d is %q, want it to start with %q", tt.what, i+1, l, want[i])
				}
			}
		}
		checkNoSecret(t, tt.what, cmp.Or(tt.secret, initPassword), stdout, stderr, db.data)
		if tt.code == 2 {
			// A usage error is found before the command connects: nothing is made.
			if after := snapshot(t, db.data); after != before || commitsOf(t, db) != 0 {
				t.Errorf("%s: the data directory changed:\n%s\nwas:\n%s", tt.what, after, before)
			}
		} else {
			checkStoreMade(t, tt.what, db)
		}

		// Init can be run again at once, on the store that the failure leaves.
		code, stdout, stderr = initDB(t, db, initPassword+"\n")
		if code != 0 || stderr != "" {
			t.Errorf("%s: whiteroom init again = %d, with stdout %q and stderr:\n%s", tt.what, code, stdout, stderr)
			continue
		}
		checkAdmin(t, tt.what+", again", inspect(t, db.data, initPassword))
	}
}

// refusedInit checks that whiteroom init of the daemon db, which does not read its standard input, is refused with the line
// that the store has what, accounts or documents, and that it changes nothing: not a file of the data directory, not the
// number of the last commit.
func refusedInit(t *testing.T, what string, db testDB, what2 string, more ...string) {
	t.Helper()
	before, commits := stateOf(t, db.data), commitsOf(t, db)
	var o, e bytes.Buffer
	args := append([]string{"init"}, adminFlags(append([]string{"-db", db.sockets}, more...)...)...)
	code := run(t.Context(), args, testEnv(failingReader{t}, &o, &e))
	want := "whiteroom init: the store of " + db.sockets + " has " + what2 + ": init is for a new store\n"
	if code != 1 || o.Len() != 0 || e.String() != want {
		t.Errorf("%s: whiteroom init = %d, with stdout %q and stderr:\n%s\nwant 1, nothing, and %q", what, code, o.String(), e.String(), want)
	}
	if after := stateOf(t, db.data); after != before {
		t.Errorf("%s: the data directory changed:\n%s\nwas:\n%s", what, after, before)
	}
	if n := commitsOf(t, db); n != commits {
		t.Errorf("%s: the last commit is %d, was %d", what, n, commits)
	}
}

// TestInitUsedStore checks that whiteroom init refuses a store that has an account, and one that has a document of any kind,
// the settings included, and writes nothing to it: the files of the data directory are as they were, with the hash of each,
// and so is the number of the last commit, also when the SITE that it is given cannot be opened, which is not looked at.
func TestInitUsedStore(t *testing.T) {
	t.Run("an account", func(t *testing.T) {
		db := newDB(t)
		if code, _, stderr := initDB(t, db, initPassword+"\n"); code != 0 {
			t.Fatalf("whiteroom init = %d, with stderr:\n%s", code, stderr)
		}
		refusedInit(t, "an account", db, "accounts")
		refusedInit(t, "an account, and a SITE", db, "accounts", sampleSite)
		refusedInit(t, "an account, and a SITE that is none", db, "accounts", filepath.Join(t.TempDir(), "none"))
		checkAdmin(t, "the account", inspect(t, db.data, initPassword))
	})

	t.Run("the documents of the sample site, saved by a user without an account", func(t *testing.T) {
		db := newDB(t)
		s, err := store.Create(dbDialer(db.sockets))
		if err != nil {
			t.Fatal(err)
		}
		src, closeSite, err := openSite(sampleSite, exampleTheme)
		if err != nil {
			t.Fatal(err)
		}
		defer closeSite()
		if _, err := edit.Import(t.Context(), s, src, content.FormatID(content.RefUser, [16]byte{0: 0x02, 15: 1})); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		refusedInit(t, "the documents", db, "documents")
		refusedInit(t, "the documents, and a SITE", db, "documents", sampleSite)
	})

	// One document of each kind, with nothing else, is enough, the settings among them.
	src, closeSite, err := openSite(sampleSite, exampleTheme)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeSite)
	for _, kind := range newStoreKinds {
		t.Run(string(kind), func(t *testing.T) {
			id, data := sampleDocument(t, src, kind)
			db := newDB(t)
			s, err := store.Create(dbDialer(db.sockets))
			if err != nil {
				t.Fatal(err)
			}
			err = s.Update(t.Context(), func(tx *store.Tx) error {
				if _, err := tx.Put(kind, id, data, "usr_01JB8Y6T2RG7M4XK9P2D5W8NQ3"); err != nil {
					return err
				}
				return tx.Record(audit.Record{Log: audit.ActivityLog, Actor: audit.System, Action: audit.StoreWrite})
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			refusedInit(t, "a document of the kind "+string(kind), db, "documents")
		})
	}
}

// sampleDocument returns the ID and the bytes of a document of the kind in the sample site: the settings, which have no ID,
// or the first of the documents of the kind.
func sampleDocument(t *testing.T, src edit.Source, kind content.RefKind) (id string, data []byte) {
	t.Helper()
	if kind == store.SettingsKind {
		settings, _, err := src.Settings()
		if err != nil {
			t.Fatal(err)
		}
		return "", settings.Data
	}
	files, err := src.Documents(kind)
	if err != nil || len(files) == 0 {
		t.Fatalf("the documents of the kind %s in the sample site: %d, %v", kind, len(files), err)
	}
	id = slices.Sorted(maps.Keys(files))[0]
	return id, files[id].Data
}

// TestInitTrial checks that the site is tried before the password is asked for, and that the trial commits nothing: a site
// with a problem, in any stage of the import, fails with the problems, without a read of the standard input and without an
// administrator, and a second init with the site made right works on the store that the first has made; and the number of the
// last commit, when the password is asked for, is that of the store that the command has just made, so that the trial has not
// committed.
func TestInitTrial(t *testing.T) {
	db := newDB(t)
	site := copySample(t)
	file := filepath.Join(site, "revisions", "rev_01JBD1Y2Z3A4B5C6D7E8F9G0H1.json")
	changeSlug := func(from, to string) {
		t.Helper()
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		changed := strings.Replace(string(data), from, to, 1)
		if changed == string(data) {
			t.Fatalf("%s has no %s", file, from)
		}
		if err := os.WriteFile(file, []byte(changed), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The slug of the page "contact" is that of the page "about": only the last check of the import finds it.
	changeSlug(`"slug": "contact"`, `"slug": "about"`)
	var o, e bytes.Buffer
	code := run(t.Context(), append([]string{"init"}, adminFlags("-db", db.sockets, "-theme", exampleTheme, site)...),
		testEnv(failingReader{t}, &o, &e))
	if code != 1 || o.Len() != 0 || !strings.Contains(e.String(), `/slug: slug "about" is also used by`) {
		t.Fatalf("whiteroom init of a site with a duplicate slug = %d, with stdout %q and stderr:\n%s\nwant 1, nothing and the problem",
			code, o.String(), e.String())
	}
	checkStoreMade(t, "a site with a duplicate slug", db)
	if n := commitsOf(t, db); n != 1 {
		t.Errorf("the last commit is %d after the trial of a site with a problem, want 1, that of the store", n)
	}

	// The site made right: the store that the first init has made is used, and the number of the last commit, when the password
	// is asked for, is that of the store.
	changeSlug(`"slug": "about"`, `"slug": "contact"`)
	var atPassword uint64
	stdin := &actingReader{act: func() { atPassword = commitsOf(t, db) }, text: strings.NewReader(initPassword + "\n")}
	code, stdout, stderr := runCmdIn(t, stdin, append([]string{"init"}, adminFlags("-db", db.sockets, "-theme", exampleTheme, site)...)...)
	if code != 0 {
		t.Fatalf("whiteroom init of the site made right = %d, with stderr:\n%s", code, stderr)
	}
	if atPassword != 1 {
		t.Errorf("the last commit is %d when the password is asked for, want 1, that of the store: the trial has committed", atPassword)
	}
	got := inspect(t, db.data, initPassword)
	admin := checkAdmin(t, "the site made right", got)
	if want := wantInitialized(db, admin, site); stdout != want {
		t.Errorf("whiteroom init printed:\n%s\nwant:\n%s", stdout, want)
	}
	checkRecords(t, "the site made right", got.records,
		[]audit.Action{audit.StoreCreate, audit.AccountCreateFirst, audit.SiteImport}, []string{audit.System, audit.System, admin.ID})
	if n := commitsOf(t, db); n != 3 {
		t.Errorf("the last commit is %d, want 3", n)
	}

	// On a store that is new, which has no commit at all, the trial comes after the commit of the store, and commits nothing.
	fresh := newDB(t)
	var freshAtPassword uint64
	stdin = &actingReader{act: func() { freshAtPassword = commitsOf(t, fresh) }, text: strings.NewReader(initPassword + "\n")}
	if code, _, stderr := runCmdIn(t, stdin, append([]string{"init"}, adminFlags("-db", fresh.sockets, "-theme", exampleTheme, sampleSite)...)...); code != 0 {
		t.Fatalf("whiteroom init of the sample site = %d, with stderr:\n%s", code, stderr)
	}
	if freshAtPassword != 1 {
		t.Errorf("the last commit is %d when the password is asked for of a new store, want 1", freshAtPassword)
	}
}

// fileNames returns the names of the files below dir, with "/", in order.
func fileNames(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	err := fs.WalkDir(os.DirFS(dir), ".", func(name string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			names = append(names, name)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// TestInitFailsAfterAdmin checks the failures of whiteroom init once the administrator is made, which are the failures of the
// import of SITE, though its trial has passed (an original that the site has lost since, a commit that the daemon fails, a
// command that is interrupted): the error, and then, on stderr, that the store has the administrator and, unless the daemon
// stored the import as the command stopped, no site, and how to start over, with exit code 1; the store as it is, with the
// administrator, and without a document; no file of the data directory removed; and that init is refused for the store
// from then on.
func TestInitFailsAfterAdmin(t *testing.T) {
	advice := func(db testDB) string {
		return "whiteroom init: the store of " + db.sockets + " now has the administrator " + initLogin + ", and no site, unless " +
			"the daemon stored the import as the command stopped; to start over, stop whiteroom-db, remove its data directory, " +
			"make it again with whiteroom-db init, start it, and run whiteroom init again\n"
	}
	check := func(t *testing.T, db testDB, code int, stdout, stderr string, before []string, problem string) {
		t.Helper()
		if code != 1 || stdout != "" || !strings.HasSuffix(stderr, advice(db)) || !strings.Contains(stderr, problem) {
			t.Errorf("whiteroom init = %d, with stdout %q and stderr:\n%s\nwant 1, nothing, %q, and the advice:\n%s", code, stdout,
				stderr, problem, advice(db))
		}
		if strings.Count(stderr, "\n") < 2 {
			t.Errorf("stderr is %q, want the error and the advice, in this order", stderr)
		}
		got := inspect(t, db.data, initPassword)
		checkAdmin(t, "the store after the failure", got)
		checkRecords(t, "the store after the failure", got.records, []audit.Action{audit.StoreCreate, audit.AccountCreateFirst},
			[]string{audit.System, audit.System})
		if len(got.docs) != 0 {
			t.Errorf("the store has %d documents, want none", len(got.docs))
		}
		after := fileNames(t, db.data)
		for _, name := range before {
			if !slices.Contains(after, name) {
				t.Errorf("the file %s of the data directory is gone", name)
			}
		}
		checkNoSecret(t, "a failure after the administrator", initPassword, stdout, stderr, db.data)
		refusedInit(t, "init after the failure", db, "accounts")
	}

	t.Run("an original that is gone when the import reads it again", func(t *testing.T) {
		db := newDB(t)
		site := copySample(t)
		originals := fileNames(t, filepath.Join(site, "files"))
		if len(originals) == 0 {
			t.Fatalf("%s has no original", site)
		}
		var before []string
		stdin := &actingReader{text: strings.NewReader(initPassword + "\n"), act: func() {
			before = fileNames(t, db.data)
			if err := os.Remove(filepath.Join(site, "files", filepath.FromSlash(originals[0]))); err != nil {
				t.Error(err)
			}
		}}
		code, stdout, stderr := runCmdIn(t, stdin, append([]string{"init"}, adminFlags("-db", db.sockets, site)...)...)
		check(t, db, code, stdout, stderr, before, "no original with this hash")
	})

	t.Run("a commit that the daemon fails", func(t *testing.T) {
		// The third commit, that of the import, whose Update is only tried first, fails in the daemon.
		var commits atomic.Int32
		hook := func(point string) error {
			if point == "commit:gate" && commits.Add(1) == 3 {
				return errors.New("the disk is gone")
			}
			return nil
		}
		db := newDBOptions(t, dbtest.Options{Limits: &dbLimits, Hook: hook})
		var before []string
		stdin := &actingReader{text: strings.NewReader(initPassword + "\n"), act: func() { before = fileNames(t, db.data) }}
		code, stdout, stderr := runCmdIn(t, stdin, append([]string{"init"}, adminFlags("-db", db.sockets, "-theme", exampleTheme, sampleSite)...)...)
		check(t, db, code, stdout, stderr, before, "")
		if strings.Contains(stderr, "the disk is gone") || commits.Load() != 3 {
			t.Errorf("stderr is:\n%s\nafter %d commits, want the daemon's text, with no word of the disk, after 3", stderr, commits.Load())
		}
	})

	t.Run("the command is interrupted while the import is written", func(t *testing.T) {
		// The context ends when the daemon takes the third commit, which it fails, so that the store has no site whichever of
		// the two the command sees first.
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var commits atomic.Int32
		hook := func(point string) error {
			if point == "commit:gate" && commits.Add(1) == 3 {
				cancel()
				return errors.New("the disk is gone")
			}
			return nil
		}
		db := newDBOptions(t, dbtest.Options{Limits: &dbLimits, Hook: hook})
		var before []string
		stdin := &actingReader{text: strings.NewReader(initPassword + "\n"), act: func() { before = fileNames(t, db.data) }}
		var o, e bytes.Buffer
		args := append([]string{"init"}, adminFlags("-db", db.sockets, "-theme", exampleTheme, sampleSite)...)
		code := run(ctx, args, testEnv(stdin, &o, &e))
		check(t, db, code, o.String(), e.String(), before, "")
		if strings.Contains(e.String(), "the disk is gone") {
			t.Errorf("stderr is:\n%s\nwant no word of the disk, which the daemon does not tell", &e)
		}
	})
}

// TestInitDefaultCommitRate runs whiteroom init of the sample site on a daemon with the default limit of commits per second,
// which is whiteroom-db's, 50: its 3 commits are not slowed down. The free space is not checked, as in the other tests.
func TestInitDefaultCommitRate(t *testing.T) {
	db := newDBWith(t, dbwire.Limits{MinFree: -1})
	start := time.Now()
	code, _, stderr := initDB(t, db, initPassword+"\n", "-theme", exampleTheme, sampleSite)
	if code != 0 {
		t.Fatalf("whiteroom init = %d, with stderr:\n%s", code, stderr)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("whiteroom init took %v on a daemon of the default limits", elapsed)
	}
	if n := commitsOf(t, db); n != 3 {
		t.Errorf("the last commit is %d, want 3", n)
	}
}

// reachFail checks the output of a command that cannot reach the daemon at the sockets in dir: it names the socket, which is
// dialed with the role of the command.
func reachFail(t *testing.T, what, out string, socket string) {
	t.Helper()
	if !strings.Contains(out, ": "+dbclient.ErrUnavailable.Error()+": ") || !strings.Contains(out, socket) {
		t.Errorf("%s printed:\n%s\nwant the socket %s, and that the daemon cannot be reached", what, out, socket)
	}
}

// TestNoDaemon runs the commands that connect to a daemon where there is none, a directory of sockets that has none: each
// fails with exit code 1 and a text that names the socket that it dials, the writer's for init and the account commands, the
// reader's for the others, and makes nothing: no output directory, no DEST. It takes the wait of the client, so the commands
// run at the same time.
func TestNoDaemon(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "wr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	writer, reader := filepath.Join(dir, dbwire.WriterSocket), filepath.Join(dir, dbwire.ReaderSocket)
	work := t.TempDir()
	out, cache, dest := filepath.Join(work, "out"), filepath.Join(work, "cache"), filepath.Join(work, "dest")
	for _, tt := range []struct {
		what   string
		args   []string
		socket string
		// stdout is where the text is: doctor tells the problem on stdout, with the last line.
		stdout bool
	}{
		{"init", append([]string{"init"}, adminFlags("-db", dir)...), writer, false},
		{"init of a site", append([]string{"init"}, adminFlags("-db", dir, "-theme", exampleTheme, sampleSite)...), writer, false},
		{"account reset", []string{"account", "reset", "-db", dir, initLogin}, writer, false},
		{"account delete", []string{"account", "delete", "-db", dir, "usr_x"}, writer, false},
		{"backup", []string{"backup", "-db", dir, dest}, reader, false},
		{"build", []string{"build", "-db", dir, "-theme", exampleTheme, "-out", out, "-cache", cache, sampleSite}, reader, false},
		{"doctor", []string{"doctor", "-db", dir}, reader, true},
	} {
		t.Run(tt.what, func(t *testing.T) {
			t.Parallel()
			var o, e bytes.Buffer
			code := run(t.Context(), tt.args, testEnv(failingReader{t}, &o, &e))
			name := "whiteroom " + strings.Join(tt.args[:1], " ")
			if tt.args[0] == "account" {
				name += " " + tt.args[1]
			}
			switch {
			case tt.stdout:
				lines := strings.Split(strings.TrimSuffix(o.String(), "\n"), "\n")
				if code != 1 || e.Len() != 0 || len(lines) != 2 || !strings.HasPrefix(lines[0], "open: "+dir+": ") ||
					lines[1] != "1 problem found in "+dir {
					t.Errorf("%s = %d, with stdout:\n%s\nand stderr %q, want 1, the problem open and the count", name, code, &o, &e)
				}
				reachFail(t, name, o.String(), tt.socket)
			default:
				if code != 1 || o.Len() != 0 || strings.Count(e.String(), "\n") != 1 || !strings.HasPrefix(e.String(), name+": "+dir+": ") {
					t.Errorf("%s = %d, with stdout %q and stderr:\n%s\nwant 1, nothing, and one line that starts with the directory", name,
						code, &o, &e)
				}
				reachFail(t, name, e.String(), tt.socket)
			}
		})
	}
	t.Cleanup(func() {
		for _, name := range []string{out, cache, dest} {
			checkMissing(t, "a command without a daemon", name)
		}
	})
}

// fakeDaemon listens on the two sockets of a new directory, and gives each connection to serve with the role of its socket,
// as a daemon of another version of the protocol would be. It returns the directory.
func fakeDaemon(t *testing.T, serve func(role dbwire.Role, rd *dbwire.Reader, wr *dbwire.Writer)) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "wr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	var wg sync.WaitGroup
	var mu sync.Mutex
	var conns []net.Conn
	var listeners []net.Listener
	for _, s := range []struct {
		name string
		role dbwire.Role
	}{{dbwire.WriterSocket, dbwire.RoleWriter}, {dbwire.ReaderSocket, dbwire.RoleReader}} {
		l, err := net.Listen("unix", filepath.Join(dir, s.name))
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, l)
		wg.Go(func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				mu.Lock()
				conns = append(conns, c)
				mu.Unlock()
				wg.Go(func() {
					defer func() { _ = c.Close() }()
					serve(s.role, dbwire.NewReader(c, 1<<20), dbwire.NewWriter(c, 1<<20))
				})
			}
		})
	}
	t.Cleanup(func() {
		for _, l := range listeners {
			_ = l.Close()
		}
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return dir
}

// TestDaemonOfAnotherVersion runs the commands against a daemon that speaks another version of the protocol, which refuses
// the Hello, or answers it with its own version: each fails with exit code 1, a text that has the version of the daemon and
// that of the program, and makes nothing.
func TestDaemonOfAnotherVersion(t *testing.T) {
	hello := func(rd *dbwire.Reader) {
		if kind, err := rd.Next(); err != nil || kind != dbwire.KindHello {
			return
		}
		_ = rd.Discard()
	}
	for _, d := range []struct {
		what  string
		serve func(role dbwire.Role, rd *dbwire.Reader, wr *dbwire.Writer)
		want  string
	}{
		{"a Hello that is refused", func(_ dbwire.Role, rd *dbwire.Reader, wr *dbwire.Writer) {
			hello(rd)
			_ = wr.Fail(dbwire.CodeVersion, fmt.Sprintf("protocol version %d, but the daemon speaks version 2", dbwire.Version))
		}, fmt.Sprintf("version %d, but the daemon speaks version 2", dbwire.Version)},
		{"a Hello that is answered", func(role dbwire.Role, rd *dbwire.Reader, wr *dbwire.Writer) {
			hello(rd)
			wr.Begin(dbwire.KindOK)
			e := &dbwire.Enc{W: wr}
			e.HelloReply(dbwire.HelloReply{Version: 2, Role: role, Frame: 4096, MaxKey: 4096})
			_ = wr.End()
		}, fmt.Sprintf("the daemon speaks version 2, this program %d", dbwire.Version)},
	} {
		dir := fakeDaemon(t, d.serve)
		work := t.TempDir()
		out, cache, dest := filepath.Join(work, "out"), filepath.Join(work, "cache"), filepath.Join(work, "dest")
		for _, args := range [][]string{
			append([]string{"init"}, adminFlags("-db", dir)...),
			{"account", "reset", "-db", dir, initLogin},
			{"account", "delete", "-db", dir, "usr_x"},
			{"backup", "-db", dir, dest},
			{"build", "-db", dir, "-theme", exampleTheme, "-out", out, "-cache", cache, sampleSite},
			{"doctor", "-db", dir},
		} {
			var o, e bytes.Buffer
			code := run(t.Context(), args, testEnv(failingReader{t}, &o, &e))
			text := o.String() + e.String()
			if code != 1 || !strings.Contains(text, d.want) || strings.Count(text, "\n") > 2 {
				t.Errorf("%s: whiteroom %s = %d, with stdout %q and stderr %q, want 1, and %q", d.what, args[0], code, &o, &e, d.want)
			}
		}
		for _, name := range []string{out, cache, dest} {
			checkMissing(t, d.what, name)
		}
	}
}

// TestInitFailures checks the failures that the arguments do not cause: a context that is done, before anything, before the
// password and once the store is made, and an error of the accounts. The store that they leave has no account, as that of a
// failure of the site does, and init can be run again.
func TestInitFailures(t *testing.T) {
	cheap := func(s *store.Store) (*account.Manager, error) { return account.NewWithIterations(s, 1) }
	runWith := func(ctx context.Context, newAccounts func(*store.Store) (*account.Manager, error), args ...string) (int, string, string) {
		var o, e bytes.Buffer
		e2 := testEnv(strings.NewReader(initPassword+"\n"), &o, &e)
		e2.newAccounts = newAccounts
		code := run(ctx, append([]string{"init"}, args...), e2)
		return code, o.String(), e.String()
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	late, cancelLate := context.WithCancel(t.Context())
	defer cancelLate()
	lateAccounts := func(s *store.Store) (*account.Manager, error) {
		cancelLate()
		return cheap(s)
	}
	noBits := errors.New("no random bits")
	for _, tt := range []struct {
		what        string
		ctx         context.Context
		newAccounts func(*store.Store) (*account.Manager, error)
		stderr      string
		made        bool // whether the store is made
	}{
		{"a context that is done", canceled, cheap, "whiteroom init: interrupted\n", false},
		{"a context that is done once the store is made", late, lateAccounts, "whiteroom init: interrupted\n", true},
		{"accounts that cannot be made", t.Context(), func(*store.Store) (*account.Manager, error) { return nil, noBits },
			"whiteroom init: no random bits\n", true},
	} {
		db := newDB(t)
		code, stdout, stderr := runWith(tt.ctx, tt.newAccounts, adminFlags("-db", db.sockets)...)
		if code != 1 || stdout != "" || stderr != tt.stderr {
			t.Errorf("%s: whiteroom init = %d, with stdout %q and stderr:\n%s\nwant 1, and %q", tt.what, code, stdout, stderr,
				tt.stderr)
		}
		if tt.made {
			checkStoreMade(t, tt.what, db)
		} else if n := commitsOf(t, db); n != 0 {
			t.Errorf("%s: the last commit is %d, want none: nothing is made", tt.what, n)
		}
		checkNoSecret(t, tt.what, initPassword, stdout, stderr, db.data)
		if code, _, stderr := initDB(t, db, initPassword+"\n"); code != 0 {
			t.Errorf("%s: whiteroom init again = %d, with stderr:\n%s", tt.what, code, stderr)
		}
	}
}

// TestInitBesideWriter runs whiteroom init while another process is connected as the writer of the daemon, such as a whiteroom
// serve admin: it fails after the wait of the client, with the sentence of the writer lock, without reading its standard input,
// and the store is as it was.
func TestInitBesideWriter(t *testing.T) {
	t.Parallel()
	db := newDB(t)
	writer, err := store.Create(dbDialer(db.sockets))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	before, commits := stateOf(t, db.data), commitsOf(t, db)
	var o, e bytes.Buffer
	start := time.Now()
	code := run(t.Context(), append([]string{"init"}, adminFlags("-db", db.sockets)...), testEnv(failingReader{t}, &o, &e))
	if want := "whiteroom init: " + db.sockets + ": another process is connected as the writer\n"; code != 1 || o.Len() != 0 ||
		e.String() != want {
		t.Errorf("whiteroom init beside a writer = %d, with stdout %q and stderr %q, want 1, nothing and %q", code, o.String(),
			e.String(), want)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("whiteroom init took %v, want the wait of the client, a few seconds", elapsed)
	}
	if after := stateOf(t, db.data); after != before || commitsOf(t, db) != commits {
		t.Errorf("the store has changed:\n%s\nwas:\n%s", after, before)
	}
}

// actingReader is a standard input that runs act when it is first read, as another process does while the password is
// typed, and then reads text.
type actingReader struct {
	act  func()
	done bool
	text io.Reader
}

func (r *actingReader) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		r.act()
	}
	return r.text.Read(p)
}

// TestInitInterruptedAtPassword checks that whiteroom init, which waits for the password on an input that gives nothing, as a
// pipe whose writer is silent, ends when its context is done after it has begun to read: it prints interrupted, returns 1,
// and has made no administrator; the store, which it made before, is as an earlier init that was stopped leaves it.
func TestInitInterruptedAtPassword(t *testing.T) {
	db := newDB(t)
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	stdin := &actingReader{act: cancel, text: pr}
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, append([]string{"init"}, adminFlags("-db", db.sockets)...), testEnv(stdin, &stdout, &stderr))
	}()
	select {
	case code := <-done:
		if want := "whiteroom init: interrupted\n"; code != 1 || stdout.Len() != 0 || stderr.String() != want {
			t.Errorf("whiteroom init = %d, with stdout %q and stderr %q, want 1, nothing and %q", code, stdout.String(),
				stderr.String(), want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("whiteroom init has not returned in 10 s after its context is done")
	}
	checkStoreMade(t, "an init that is interrupted", db)
}

// failingReader is a standard input that must not be read.
type failingReader struct{ t *testing.T }

func (r failingReader) Read([]byte) (int, error) {
	r.t.Error("the standard input is read")
	return 0, io.EOF
}

// TestInitConfig initializes stores with config files: the daemon from db, from the directory of the file, and from -db over
// it; no database is a usage error, also for a file that has data, which is no more used; the site of the file is not
// imported; the theme of the file, with SITE, is the theme, unless -theme is given; a theme that is not used is no error; and a
// problem in the file is a usage error before anything is read or made.
func TestInitConfig(t *testing.T) {
	root := sampleRoot(t)
	conf := filepath.Join(root, "conf")
	theme, err := filepath.Abs(exampleTheme)
	if err != nil {
		t.Fatal(err)
	}
	site := filepath.Join(root, "site")
	missing := filepath.Join(root, "missing")
	file := func(text string) string { return writeConfig(t, conf, "whiteroom.yaml", text) }
	ok := func(what string, db testDB, stdout, stderr string, code int, imported bool) initialized {
		t.Helper()
		if code != 0 {
			t.Fatalf("%s: whiteroom init = %d, with stderr:\n%s", what, code, stderr)
		}
		got := inspect(t, db.data, initPassword)
		admin := checkAdmin(t, what, got)
		sitePath := ""
		if imported {
			sitePath = site
		}
		if want := wantInitialized(db, admin, sitePath); stdout != want {
			t.Errorf("%s: whiteroom init printed:\n%s\nwant:\n%s", what, stdout, want)
		}
		if imported == (len(got.docs) == 0) {
			t.Errorf("%s: the store has %d documents, want some if the site is imported (%v), and none if not", what,
				len(got.docs), imported)
		}
		checkNoSecret(t, what, initPassword, stdout, stderr, db.data)
		return got
	}
	help := initHelp(t)

	// The daemon from the file.
	db := newDB(t)
	cfg := file("db: " + strconv.Quote(db.sockets) + "\n")
	code, stdout, stderr := runInitCmd(t, initPassword+"\n", adminFlags("-config", cfg)...)
	ok("db of the file", db, stdout, stderr, code, false)

	// A path of the file is from its directory: the directory of the sockets is beside the directory of the file.
	short, err := os.MkdirTemp("", "wr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	relative := newDBAt(t, filepath.Join(short, "run"))
	cfg = writeConfig(t, filepath.Join(short, "conf"), "whiteroom.yaml", "db: ../run\n")
	code, stdout, stderr = runInitCmd(t, initPassword+"\n", adminFlags("-config", cfg)...)
	ok("db from the directory of the file", relative, stdout, stderr, code, false)

	// -db over db: the item names a directory that is none.
	other := newDB(t)
	cfg = file("db: " + strconv.Quote(missing) + "\n")
	code, stdout, stderr = runInitCmd(t, initPassword+"\n", adminFlags("-config", cfg, "-db", other.sockets)...)
	ok("-db over db", other, stdout, stderr, code, false)
	checkMissing(t, "-db over db: the directory of the item", missing)

	// No database, with the item data, which is the daemon's, and not used, though it is checked.
	for _, text := range []string{"theme: ../theme\n", "data: " + strconv.Quote(other.data) + "\n"} {
		code, stdout, stderr = runInitCmd(t, initPassword+"\n", adminFlags("-config", file(text))...)
		if want := "whiteroom init: " + errNoDB + "\n" + help; code != 2 || stdout != "" || stderr != want {
			t.Errorf("no db (%q): whiteroom init = %d, with stdout %q and stderr:\n%s\nwant 2, and:\n%s", text, code, stdout, stderr, want)
		}
	}
	bad := file("data: 5\ndb: " + strconv.Quote(other.sockets) + "\n")
	var o, e bytes.Buffer
	code = run(t.Context(), append([]string{"init"}, adminFlags("-config", bad)...), testEnv(failingReader{t}, &o, &e))
	if want := bad + ":1:1: /data: must be a string\n"; code != 2 || o.Len() != 0 || e.String() != want {
		t.Errorf("a data that is no string: whiteroom init = %d, with stdout %q and stderr %q, want 2, nothing and %q", code,
			o.String(), e.String(), want)
	}

	// The site of the file is not imported, and its theme is not used without SITE.
	db = newDB(t)
	cfg = file("db: " + strconv.Quote(db.sockets) + "\nsite: ../site\ntheme: ../missing\n")
	code, stdout, stderr = runInitCmd(t, initPassword+"\n", adminFlags("-config", cfg)...)
	ok("site of the file", db, stdout, stderr, code, false)

	// SITE, with the theme of the file: SITE has no theme/ of its own.
	if _, err := os.Stat(filepath.Join(site, "theme")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s has a theme: %v", site, err)
	}
	db = newDB(t)
	cfg = file("db: " + strconv.Quote(db.sockets) + "\ntheme: ../theme\n")
	code, stdout, stderr = runInitCmd(t, initPassword+"\n", adminFlags("-config", cfg, site)...)
	ok("SITE and theme of the file", db, stdout, stderr, code, true)

	// -theme over the theme of the file.
	db = newDB(t)
	cfg = file("db: " + strconv.Quote(db.sockets) + "\ntheme: ../missing\n")
	code, stdout, stderr = runInitCmd(t, initPassword+"\n", adminFlags("-config", cfg, "-theme", theme, site)...)
	ok("-theme over theme", db, stdout, stderr, code, true)

	// The theme of the file is read: a theme that is missing fails, after the store is made, with no administrator.
	db = newDB(t)
	cfg = file("db: " + strconv.Quote(db.sockets) + "\ntheme: ../missing\n")
	o.Reset()
	e.Reset()
	code = run(t.Context(), append([]string{"init"}, adminFlags("-config", cfg, site)...), testEnv(failingReader{t}, &o, &e))
	if want := "whiteroom init: open " + missing + ": no such file or directory\n"; code != 1 || o.Len() != 0 || e.String() != want {
		t.Errorf("a missing theme of the file: whiteroom init = %d, with stdout %q and stderr:\n%s\nwant 1, and %q", code, o.String(),
			e.String(), want)
	}
	checkStoreMade(t, "a missing theme of the file", db)

	// A problem in the file: before the standard input is read, and nothing is made.
	db = newDB(t)
	cfg = file("db: " + strconv.Quote(db.sockets) + "\nbogus: 1\n")
	o.Reset()
	e.Reset()
	code = run(t.Context(), append([]string{"init"}, adminFlags("-config", cfg)...), testEnv(failingReader{t}, &o, &e))
	if want := cfg + ":2:1: /bogus: unknown item\n"; code != 2 || o.Len() != 0 || e.String() != want {
		t.Errorf("a problem in the file: whiteroom init = %d, with stdout %q and stderr:\n%s\nwant 2, and %q", code, o.String(),
			e.String(), want)
	}
	if n := commitsOf(t, db); n != 0 {
		t.Errorf("a problem in the file: the last commit is %d, want none", n)
	}

	// No file is read without -config.
	t.Chdir(conf)
	db = newDB(t)
	file("db: " + strconv.Quote(db.sockets) + "\n")
	code, stdout, stderr = runInitCmd(t, initPassword+"\n", adminFlags()...)
	if code != 2 || stdout != "" || !strings.HasPrefix(stderr, "whiteroom init: no database") {
		t.Errorf("no -config: whiteroom init = %d, with stdout %q and stderr:\n%s\nwant 2, and no database", code, stdout, stderr)
	}
}

// TestInitURL initializes stores with the sample site with -url and with the item url of a config file: a redirect that leads
// back to itself under the URL, which is none under the URL of the settings and under another URL, is a problem of the import,
// which fails in the trial, before the password, and leaves the store without an administrator; the settings that the import
// stores keep the url that is written in them. The flag wins over the item. Without SITE, -url is a usage error, and the item
// is not used and not looked at. A URL that does not pass is a usage error that reads no standard input and makes nothing.
func TestInitURL(t *testing.T) {
	const (
		staging = "https://staging.example.com"
		other   = "https://other.example.com"
		id      = "rdr_01JC0000000000000000000009"
	)
	// site is a copy of the sample site with a redirect from /staging-only to the same path at the staging URL.
	site := copySample(t)
	file := filepath.Join(site, "redirects", id+".json")
	redirect := "{\n  \"schemaVersion\": 1,\n  \"id\": \"" + id + "\",\n  \"from\": \"/staging-only\",\n  \"to\": \"" + staging +
		"/staging-only\",\n  \"status\": 301\n}\n"
	if err := os.WriteFile(file, []byte(redirect), 0o644); err != nil {
		t.Fatal(err)
	}
	problem := file + ":5:3: /to: redirects to itself\n"
	conf := t.TempDir()
	files := 0
	// item returns the path of a config file with the item url u, and the daemon db.
	item := func(db testDB, u string) string {
		files++
		return writeConfig(t, conf, "url-"+strconv.Itoa(files)+".yaml", "url: "+strconv.Quote(u)+"\ndb: "+strconv.Quote(db.sockets)+"\n")
	}

	// imports checks that whiteroom init of dir, with the flags more before it, makes a store with the site in it, and
	// returns the daemon. more may be made of the daemon, which item gives it.
	imports := func(what, dir string, more func(db testDB) []string) testDB {
		t.Helper()
		db := newDB(t)
		args := append(adminFlags("-theme", exampleTheme), more(db)...)
		code, stdout, stderr := runInitCmd(t, initPassword+"\n", append(args, dir)...)
		if want := sampleImportWarnings(t, dir); code != 0 || stderr != want || !strings.HasSuffix(stdout, "imported the site "+dir+"\n") {
			t.Fatalf("%s: whiteroom init = %d, with stdout:\n%s\nand stderr:\n%s\nwant 0, the import, and the warnings:\n%s", what, code,
				stdout, stderr, want)
		}
		if got := inspect(t, db.data, initPassword); len(got.docs) < 10 {
			t.Errorf("%s: the store has %d documents, want those of the site", what, len(got.docs))
		}
		return db
	}
	// refused checks that whiteroom init of the site, with the flags more before it, is refused for the redirect that leads
	// back to itself, before the password is read, and leaves a store that has no administrator.
	refused := func(what string, more func(db testDB) []string) {
		t.Helper()
		db := newDB(t)
		args := append(adminFlags("-theme", exampleTheme), more(db)...)
		var o, e bytes.Buffer
		code := run(t.Context(), append([]string{"init"}, append(args, site)...), testEnv(failingReader{t}, &o, &e))
		if code != 1 || o.Len() != 0 || !strings.HasSuffix(e.String(), problem) {
			t.Errorf("%s: whiteroom init = %d, with stdout %q and stderr:\n%s\nwant 1, and the problem, which is the last line:\n%s", what,
				code, o.String(), e.String(), problem)
		}
		checkStoreMade(t, what, db)
		checkNoSecret(t, what, initPassword, o.String(), e.String(), db.data)
	}
	flags := func(more ...string) func(testDB) []string {
		return func(db testDB) []string { return append([]string{"-db", db.sockets}, more...) }
	}
	config := func(u string, more ...string) func(testDB) []string {
		return func(db testDB) []string { return append([]string{"-config", item(db, u)}, more...) }
	}

	imports("no URL", site, flags())
	refused("-url", flags("-url", staging))
	imports("-url of another site", site, flags("-url", other))
	imports("-url of the URL of the settings", site, flags("-url", "https://example.com"))
	refused("the item", config(staging))
	imports("the item of another site", site, config(other))
	refused("-url over the item", config(other, "-url", staging))
	imports("-url over the item that leads back to itself", site, config(staging, "-url", "https://example.com"))
	refused("-url of no text over the item", config(staging, "-url", ""))
	imports("-url over an item that does not pass", sampleSite, config("https://example.com/", "-url", staging))

	// The settings that are stored have the url that is written in them.
	db := imports("-url of the sample site", sampleSite, flags("-url", staging))
	if dataHas(t, db.data, "staging.example.com") || !dataHas(t, db.data, "https://example.com") {
		t.Errorf("the data directory has the URL of the import (%t), or lacks the url of the settings (%t)",
			dataHas(t, db.data, "staging.example.com"), !dataHas(t, db.data, "https://example.com"))
	}

	t.Run("no SITE", func(t *testing.T) {
		db := newDB(t)
		var o, e bytes.Buffer
		code := run(t.Context(), append([]string{"init"}, adminFlags("-db", db.sockets, "-url", staging)...), testEnv(failingReader{t}, &o, &e))
		want := "whiteroom init: -url is for SITE: give the directory SITE of the site to import\n" + initHelp(t)
		if code != 2 || o.Len() != 0 || e.String() != want {
			t.Errorf("whiteroom init -url without a SITE = %d, with stdout %q and stderr:\n%s\nwant 2, and:\n%s", code, o.String(), e.String(), want)
		}
		if n := commitsOf(t, db); n != 0 {
			t.Errorf("-url without a SITE: the last commit is %d, want none", n)
		}
		// The item is used with a SITE only, and is not looked at without it.
		for _, value := range []string{staging, "https://example.com/"} {
			db := newDB(t)
			code, stdout, stderr := runInitCmd(t, initPassword+"\n", adminFlags("-config", item(db, value))...)
			if code != 0 || stderr != "" || strings.Contains(stdout, "imported") {
				t.Errorf("the item %s without a SITE: whiteroom init = %d, with stdout %q and stderr:\n%s\nwant 0, and no import", value, code,
					stdout, stderr)
			}
			checkAdmin(t, "the item "+value+" without a SITE", inspect(t, db.data, initPassword))
		}
	})

	t.Run("a URL that does not pass", func(t *testing.T) {
		for _, tt := range badSiteURLs[:3] {
			for _, fromItem := range []bool{false, true} {
				db := newDB(t)
				what, args := "-url", []string{"-url", tt.value}
				if fromItem {
					what, args = "url of the config file", []string{"-config", item(db, tt.value)}
				}
				var o, e bytes.Buffer
				code := run(t.Context(), append(append([]string{"init"}, adminFlags("-db", db.sockets)...), append(args, site)...),
					testEnv(failingReader{t}, &o, &e))
				want := "whiteroom init: " + what + " is not a valid site URL: " + tt.reason + "\n"
				if code != 2 || o.Len() != 0 || e.String() != want {
					t.Errorf("%s of %q: whiteroom init = %d, with stdout %q and stderr:\n%s\nwant 2, with no stdout and:\n%s", what, tt.value, code,
						o.String(), e.String(), want)
				}
				if n := commitsOf(t, db); n != 0 {
					t.Errorf("%s of %q: the last commit is %d, want none", what, tt.value, n)
				}
			}
		}
	})
}
