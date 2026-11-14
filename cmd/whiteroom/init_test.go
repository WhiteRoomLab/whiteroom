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
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
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
	s, err := store.Open(dir)
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

// TestInit makes data directories with the password on a pipe: of a missing directory, with parents that are missing too,
// and of an empty one. It checks the output, the one account, who can log in, the records, that there are no documents, the
// modes and that nothing has the password.
func TestInit(t *testing.T) {
	for _, tt := range []struct {
		what    string
		prepare func(t *testing.T, data string)
		stdin   string
		made    bool // the directory is made by init
	}{
		{"a missing directory", func(t *testing.T, data string) {}, initPassword + "\n", true},
		{"an empty directory", func(t *testing.T, data string) {
			if err := os.Mkdir(data, 0o755); err != nil {
				t.Fatal(err)
			}
		}, initPassword + "\r\n", false},
		{"the end of the input", func(t *testing.T, data string) {}, initPassword, true},
		{"more lines", func(t *testing.T, data string) {}, initPassword + "\nthe rest\n", true},
	} {
		data := filepath.Join(t.TempDir(), "parent", "data")
		if !tt.made {
			data = filepath.Join(t.TempDir(), "data")
		}
		tt.prepare(t, data)
		code, stdout, stderr := runInitCmd(t, tt.stdin, adminFlags("-data", data)...)
		if code != 0 || stderr != "" {
			t.Fatalf("%s: whiteroom init = %d, with stderr:\n%s", tt.what, code, stderr)
		}
		got := inspect(t, data, initPassword)
		admin := checkAdmin(t, tt.what, got)
		wantOut := "created the data directory " + data + "\ncreated the administrator " + initLogin + " (" + admin.ID + ")\n"
		if stdout != wantOut {
			t.Errorf("%s: whiteroom init printed:\n%s\nwant:\n%s", tt.what, stdout, wantOut)
		}
		checkRecords(t, tt.what, got.records, []audit.Action{audit.StoreCreate, audit.AccountCreateFirst},
			[]string{audit.System, audit.System})
		if len(got.docs) != 0 {
			t.Errorf("%s: the store has documents %v, want none", tt.what, slices.Sorted(maps.Keys(got.docs)))
		}
		checkDirs(t, tt.what, data, tt.made)
		checkNoSecret(t, tt.what, initPassword, stdout, stderr, data)
		if !dataHas(t, data, initLogin) {
			t.Errorf("%s: the scan of %s does not find the login name, which the data has", tt.what, data)
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

// TestInitImport makes a data directory with the sample site, with the example theme of -theme: the documents are those
// of edit.Import, saved by the administrator, who also does the import in the records, and the warnings are those of
// the redirects.
func TestInitImport(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	code, stdout, stderr := runInitCmd(t, initPassword+"\n", adminFlags("-data", data, "-theme", exampleTheme, sampleSite)...)
	if code != 0 {
		t.Fatalf("whiteroom init of the sample site = %d, with stderr:\n%s", code, stderr)
	}
	if want := sampleImportWarnings(t, sampleSite); want == "" || stderr != want {
		t.Errorf("whiteroom init printed the warnings:\n%s\nwant:\n%s", stderr, want)
	}
	got := inspect(t, data, initPassword)
	admin := checkAdmin(t, "the sample site", got)
	wantOut := "created the data directory " + data + "\ncreated the administrator " + initLogin + " (" + admin.ID + ")\n" +
		"imported the site " + sampleSite + "\n"
	if stdout != wantOut {
		t.Errorf("whiteroom init printed:\n%s\nwant:\n%s", stdout, wantOut)
	}
	checkRecords(t, "the sample site", got.records,
		[]audit.Action{audit.StoreCreate, audit.AccountCreateFirst, audit.SiteImport},
		[]string{audit.System, audit.System, admin.ID})
	checkDirs(t, "the sample site", data, true)
	checkNoSecret(t, "the sample site", initPassword, stdout, stderr, data)

	// The documents are those that edit.Import stores of the same site for the same user.
	src, closeSite, err := openSite(sampleSite, exampleTheme)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSite()
	ref, err := store.Create(filepath.Join(t.TempDir(), "ref"))
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

// initCase is a whiteroom init that fails, with the text on its standard input, its exit code, what it prints on stderr,
// and whether the data directory is as it was and may be made again.
type initCase struct {
	what string
	// prepare changes the data directory, which is missing or empty, and returns the arguments after init.
	prepare func(t *testing.T, data string) []string
	stdin   string
	secret  string // that must not appear anywhere; initPassword if ""
	code    int
	// stderr is what it prints; if prefixes is true, it is the prefixes of the lines.
	stderr   func(data string) []string
	prefixes bool
	// retry says that init with the right flags must make the data directory afterwards.
	retry bool
}

// TestInitErrors checks the whiteroom init that fail: the exit code, the lines of stderr, that the data directory is as
// it was, a missing one or an empty one, that init can be run again at once, and that no password is in the output or in
// the data directory.
func TestInitErrors(t *testing.T) {
	help := initHelp(t)
	usageErr := func(msg string) func(string) []string {
		return func(string) []string { return []string{"whiteroom init: " + msg + "\n" + help} }
	}
	line := func(format string, args ...any) func(string) []string {
		return func(data string) []string {
			return []string{"whiteroom init: " + fmt.Sprintf(strings.ReplaceAll(format, "DATA", data), args...) + "\n"}
		}
	}
	notEmpty := "DATA: not empty; the data directory must be missing or an empty directory"
	flagsOnly := func(extra ...string) func(*testing.T, string) []string {
		return func(t *testing.T, data string) []string {
			return adminFlags(append([]string{"-data", data}, extra...)...)
		}
	}
	write := func(t *testing.T, name string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
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
		{"no -login", func(t *testing.T, data string) []string {
			return []string{"-name", initName, "-email", initEmail, "-data", data}
		}, initPassword + "\n", "", 2, usageErr("-login is required"), false, true},
		{"an empty -login", func(t *testing.T, data string) []string {
			return []string{"-login", "", "-name", initName, "-email", initEmail, "-data", data}
		}, initPassword + "\n", "", 2, usageErr("-login is required"), false, true},
		{"no -name", func(t *testing.T, data string) []string {
			return []string{"-login", initLogin, "-email", initEmail, "-data", data}
		}, initPassword + "\n", "", 2, usageErr("-name is required"), false, true},
		{"no -email", func(t *testing.T, data string) []string {
			return []string{"-login", initLogin, "-name", initName, "-data", data}
		}, initPassword + "\n", "", 2, usageErr("-email is required"), false, true},
		{"no data directory", func(t *testing.T, data string) []string { return adminFlags() },
			initPassword + "\n", "", 2, usageErr("no data directory: give -data DIR, or data in the config file"), false, true},
		{"two arguments", flagsOnly("a", "b"), initPassword + "\n", "", 2,
			usageErr("too many arguments: give at most one SITE, after the flags"), false, true},
		{"-theme without SITE", flagsOnly("-theme", exampleTheme), initPassword + "\n", "", 2,
			usageErr("-theme is for SITE: give the directory SITE of the site to import"), false, true},

		{"a file in the data directory", func(t *testing.T, data string) []string {
			write(t, filepath.Join(data, "x"))
			return adminFlags("-data", data)
		}, initPassword + "\n", "", 1, line(notEmpty), false, false},
		{"a directory in the data directory", func(t *testing.T, data string) []string {
			if err := os.MkdirAll(filepath.Join(data, "sub"), 0o755); err != nil {
				t.Fatal(err)
			}
			return adminFlags("-data", data)
		}, initPassword + "\n", "", 1, line(notEmpty), false, false},
		{"a files directory that the data directory has", func(t *testing.T, data string) []string {
			write(t, filepath.Join(data, "files", "ab", "cd"))
			return adminFlags("-data", data)
		}, initPassword + "\n", "", 1, line(notEmpty), false, false},
		{"a logdb directory that the data directory has", func(t *testing.T, data string) []string {
			write(t, filepath.Join(data, "logdb", "x"))
			return adminFlags("-data", data)
		}, initPassword + "\n", "", 1, line(notEmpty), false, false},
		{"a file as the data directory", func(t *testing.T, data string) []string {
			if err := os.RemoveAll(data); err != nil {
				t.Fatal(err)
			}
			write(t, data)
			return adminFlags("-data", data)
		}, initPassword + "\n", "", 1,
			line("DATA: not a directory; the data directory must be missing or an empty directory"), false, false},

		{"a missing site", flagsOnly(missing), initPassword + "\n", "", 1,
			line("open %s: no such file or directory", missing), false, true},
		{"an empty site directory", flagsOnly(siteEmpty), initPassword + "\n", "", 1,
			func(string) []string {
				return []string{filepath.Join(siteEmpty, "site", "schema.yaml") + ": missing file\n" +
					filepath.Join(siteEmpty, "site", "settings.json") + ": missing file\n"}
			}, false, true},
		{"a site without a theme", flagsOnly(siteNoTheme), initPassword + "\n", "", 1,
			func(string) []string { return []string{filepath.Join(siteNoTheme, "theme") + ": missing directory\n"} }, false, true},
		{"a missing theme", flagsOnly("-theme", missing, sampleSite), initPassword + "\n", "", 1,
			line("open %s: no such file or directory", missing), false, true},

		{"no standard input", flagsOnly(), "", "", 1, line("no password was read from the standard input"), false, true},
		{"an empty line", flagsOnly(), "\n" + initPassword + "\n", "", 1,
			line("no password was read from the standard input"), false, true},
		{"a line that is too long", flagsOnly(), long + "\n", long[:20], 1,
			line("the password is longer than %d bytes", maxPasswordLine), false, true},

		{"a short password", flagsOnly(), shortSecret + "\n" + initPassword + "\n", shortSecret, 1,
			line("the password: too short: %d characters (min 15)", len(shortSecret)), false, true},
		{"the login name as the password", func(t *testing.T, data string) []string {
			return adminFlags("-login", sameAsLogin, "-data", data)
		}, sameAsLogin + "\n", sameAsLogin, 1,
			line("the password: the same as the login name, the email address or the name"), false, true},
		{"an invalid -email", flagsOnly("-email", "nobody"), initPassword + "\n", "", 1,
			func(string) []string { return []string{emailLine()} }, false, true},
		{"an invalid -login", flagsOnly("-login", "Bad Login"), initPassword + "\n", "", 1,
			func(string) []string { return []string{"whiteroom init: -login: invalid login name \"bad login\": "} }, true, true},
		{"problems in the flags and the password", flagsOnly("-login", "Bad Login", "-email", "nobody"),
			shortSecret + "\n", shortSecret, 1,
			func(string) []string {
				return []string{
					"whiteroom init: -login: invalid login name \"bad login\": ",
					strings.TrimSuffix(emailLine(), "\n"),
					fmt.Sprintf("whiteroom init: the password: too short: %d characters (min 15)", len(shortSecret)),
				}
			}, true, true},
		{"a site with a broken document", flagsOnly(siteBroken), initPassword + "\n", "", 1,
			func(string) []string { return []string{revision + ":1:1: /title: missing title\n"} }, false, true},
	}

	for _, preexisting := range []bool{false, true} {
		for _, tt := range cases {
			what := tt.what
			if preexisting {
				what += " in an empty directory"
			}
			data := filepath.Join(t.TempDir(), "data")
			if preexisting {
				if err := os.Mkdir(data, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			args := tt.prepare(t, data)
			before := snapshot(t, data)
			code, stdout, stderr := runInitCmd(t, tt.stdin, args...)
			want := tt.stderr(data)
			switch {
			case code != tt.code || stdout != "":
				t.Errorf("%s: whiteroom init = %d, with stdout %q and stderr:\n%s\nwant %d, and nothing on stdout", what, code,
					stdout, stderr, tt.code)
			case !tt.prefixes && stderr != strings.Join(want, ""):
				t.Errorf("%s: whiteroom init printed:\n%s\nwant:\n%s", what, stderr, strings.Join(want, ""))
			case tt.prefixes:
				lines := strings.SplitAfter(stderr, "\n")
				lines = lines[:len(lines)-1]
				if len(lines) != len(want) {
					t.Errorf("%s: whiteroom init printed %d lines:\n%s\nwant %d, those that start with %q", what, len(lines), stderr,
						len(want), want)
					break
				}
				for i, l := range lines {
					if !strings.HasPrefix(l, want[i]) {
						t.Errorf("%s: line %d is %q, want it to start with %q", what, i+1, l, want[i])
					}
				}
			}
			if after := snapshot(t, data); after != before {
				t.Errorf("%s: the data directory changed:\n%s\nwas:\n%s", what, after, before)
			}
			secret := cmp.Or(tt.secret, initPassword)
			checkNoSecret(t, what, secret, stdout, stderr, data)
			if !tt.retry {
				continue
			}
			code, stdout, stderr = runInitCmd(t, initPassword+"\n", adminFlags("-data", data)...)
			if code != 0 || stderr != "" {
				t.Errorf("%s: whiteroom init again = %d, with stdout %q and stderr:\n%s", what, code, stdout, stderr)
				continue
			}
			checkAdmin(t, what+", again", inspect(t, data, initPassword))
		}
	}
}

// TestInitFailures checks the failures that the arguments do not cause: a parent that is a file, a context that is done,
// before the password and once the store is made, an error of the accounts, and a directory that cannot be written. The
// last three leave no more than the directory that was there.
func TestInitFailures(t *testing.T) {
	// A parent of the data directory that is a file.
	parent := filepath.Join(t.TempDir(), "parent")
	if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(parent, "data")
	code, stdout, stderr := runInitCmd(t, initPassword+"\n", adminFlags("-data", data)...)
	if want := "whiteroom init: stat " + data + ": not a directory\n"; code != 1 || stdout != "" || stderr != want {
		t.Errorf("a parent that is a file: whiteroom init = %d, with stdout %q and stderr:\n%s\nwant 1, and %q", code, stdout,
			stderr, want)
	}

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
	}{
		{"a context that is done", canceled, cheap, "whiteroom init: interrupted\n"},
		{"a context that is done once the store is made", late, lateAccounts, "whiteroom init: interrupted\n"},
		{"accounts that cannot be made", t.Context(), func(*store.Store) (*account.Manager, error) { return nil, noBits },
			"whiteroom init: no random bits\n"},
	} {
		for _, preexisting := range []bool{false, true} {
			what := tt.what
			data := filepath.Join(t.TempDir(), "data")
			if preexisting {
				what += " in an empty directory"
				if err := os.Mkdir(data, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshot(t, data)
			code, stdout, stderr := runWith(tt.ctx, tt.newAccounts, adminFlags("-data", data)...)
			if code != 1 || stdout != "" || stderr != tt.stderr {
				t.Errorf("%s: whiteroom init = %d, with stdout %q and stderr:\n%s\nwant 1, and %q", what, code, stdout, stderr,
					tt.stderr)
			}
			if after := snapshot(t, data); after != before {
				t.Errorf("%s: the data directory changed:\n%s\nwas:\n%s", what, after, before)
			}
			checkNoSecret(t, what, initPassword, stdout, stderr, data)
		}
	}

	// A directory that cannot be written: the store is not made, and the directory stays as it was.
	if os.Geteuid() == 0 {
		t.Log("running as root: a directory that cannot be written is skipped")
		return
	}
	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	before := snapshot(t, locked)
	code, stdout, stderr = runWith(t.Context(), cheap, adminFlags("-data", locked)...)
	if code != 1 || stdout != "" || !strings.HasPrefix(stderr, "whiteroom init: ") || strings.Count(stderr, "\n") != 1 {
		t.Errorf("a directory that cannot be written: whiteroom init = %d, with stdout %q and stderr:\n%s", code, stdout, stderr)
	}
	if after := snapshot(t, locked); after != before {
		t.Errorf("a directory that cannot be written: it changed:\n%s\nwas:\n%s", after, before)
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

// TestInitDataChanged checks whiteroom init when the data directory changes while the password is read: a store that
// another process has made there, or files in logdb/ and files/, are refused and left as they are; an empty directory
// is used, and stays if init fails later, since init did not make it.
func TestInitDataChanged(t *testing.T) {
	notEmpty := func(data string) string {
		return "whiteroom init: " + data + ": not empty; the data directory must be missing or an empty directory\n"
	}
	anotherStore := func(t *testing.T, data string) {
		t.Helper()
		s, err := store.Create(data)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	someFiles := func(t *testing.T, data string) {
		t.Helper()
		for _, name := range []string{"logdb/segment", "files/ab/cd"} {
			file := filepath.Join(data, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte("another process"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	emptyDir := func(t *testing.T, data string) {
		t.Helper()
		if err := os.Mkdir(data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		what        string
		preexisting bool
		make        func(t *testing.T, data string)
	}{
		{"a store made by another process", false, anotherStore},
		{"a store made by another process in an empty directory", true, anotherStore},
		{"files made by another process", false, someFiles},
		{"files made by another process in an empty directory", true, someFiles},
	} {
		data := filepath.Join(t.TempDir(), "data")
		if tt.preexisting {
			emptyDir(t, data)
		}
		var made string
		stdin := &actingReader{act: func() { tt.make(t, data); made = snapshot(t, data) }, text: strings.NewReader(initPassword + "\n")}
		code, stdout, stderr := runCmdIn(t, stdin, append([]string{"init"}, adminFlags("-data", data)...)...)
		if code != 1 || stdout != "" || stderr != notEmpty(data) {
			t.Errorf("%s: whiteroom init = %d, with stdout %q and stderr:\n%s\nwant 1, and %q", tt.what, code, stdout, stderr,
				notEmpty(data))
		}
		if after := snapshot(t, data); made == "" || after != made {
			t.Errorf("%s: the data directory is:\n%s\nwant it as the other process left it:\n%s", tt.what, after, made)
		}
		checkNoSecret(t, tt.what, initPassword, stdout, stderr, data)
	}

	// The store that another process made is whole: it opens, and has only the record of its making.
	data := filepath.Join(t.TempDir(), "data")
	stdin := &actingReader{act: func() { anotherStore(t, data) }, text: strings.NewReader(initPassword + "\n")}
	if code, _, stderr := runCmdIn(t, stdin, append([]string{"init"}, adminFlags("-data", data)...)...); code != 1 {
		t.Fatalf("whiteroom init = %d, with stderr:\n%s", code, stderr)
	}
	got := inspect(t, data, initPassword)
	checkRecords(t, "the store of another process", got.records, []audit.Action{audit.StoreCreate}, []string{audit.System})
	if len(got.accounts) != 0 {
		t.Errorf("the store of another process has %d accounts, want none", len(got.accounts))
	}

	// An empty directory that is made meanwhile is used, which is as good as an empty one that was there.
	data = filepath.Join(t.TempDir(), "data")
	stdin = &actingReader{act: func() { emptyDir(t, data) }, text: strings.NewReader(initPassword + "\n")}
	code, stdout, stderr := runCmdIn(t, stdin, append([]string{"init"}, adminFlags("-data", data)...)...)
	if code != 0 || stderr != "" {
		t.Fatalf("an empty directory made meanwhile: whiteroom init = %d, with stderr:\n%s", code, stderr)
	}
	admin := checkAdmin(t, "an empty directory made meanwhile", inspect(t, data, initPassword))
	if want := "created the data directory " + data + "\ncreated the administrator " + initLogin + " (" + admin.ID + ")\n"; stdout != want {
		t.Errorf("an empty directory made meanwhile: whiteroom init printed:\n%s\nwant:\n%s", stdout, want)
	}

	// If init then fails, the directory stays, as an empty directory that was there does, for init did not make it.
	data = filepath.Join(t.TempDir(), "data")
	var made string
	stdin = &actingReader{act: func() { emptyDir(t, data); made = snapshot(t, data) }, text: strings.NewReader(initPassword + "\n")}
	code, stdout, stderr = runCmdIn(t, stdin, append([]string{"init"}, adminFlags("-data", data, "-email", "nobody")...)...)
	if code != 1 || stdout != "" || stderr != emailLine() {
		t.Errorf("an empty directory made meanwhile, and a flag refused: whiteroom init = %d, with stdout %q and stderr:\n%s\nwant 1, and %q",
			code, stdout, stderr, emailLine())
	}
	if after := snapshot(t, data); made == "" || after != made {
		t.Errorf("an empty directory made meanwhile, and a flag refused: the data directory is:\n%s\nwant the empty directory:\n%s",
			after, made)
	}
}

// failingReader is a standard input that must not be read.
type failingReader struct{ t *testing.T }

func (r failingReader) Read([]byte) (int, error) {
	r.t.Error("the standard input is read")
	return 0, io.EOF
}

// TestInitConfig makes data directories with config files: the data directory from data, from the directory of the file,
// and from -data over it; no data directory is a usage error; the site of the file is not imported; the theme of the
// file, with SITE, is the theme, unless -theme is given; a theme that is not used is no error; and a problem in the file
// is a usage error before anything is read or made.
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
	ok := func(what string, stdout, stderr string, code int, data string, imported bool) initialized {
		t.Helper()
		if code != 0 {
			t.Fatalf("%s: whiteroom init = %d, with stderr:\n%s", what, code, stderr)
		}
		got := inspect(t, data, initPassword)
		admin := checkAdmin(t, what, got)
		want := "created the data directory " + data + "\ncreated the administrator " + initLogin + " (" + admin.ID + ")\n"
		if imported {
			want += "imported the site " + site + "\n"
		}
		if stdout != want {
			t.Errorf("%s: whiteroom init printed:\n%s\nwant:\n%s", what, stdout, want)
		}
		if imported == (len(got.docs) == 0) {
			t.Errorf("%s: the store has %d documents, want some if the site is imported (%v), and none if not", what,
				len(got.docs), imported)
		}
		checkNoSecret(t, what, initPassword, stdout, stderr, data)
		return got
	}
	help := initHelp(t)

	// The data directory from the file: from the directory of the file; -data over it.
	data := filepath.Join(conf, "data")
	cfg := file("data: data\n")
	code, stdout, stderr := runInitCmd(t, initPassword+"\n", adminFlags("-config", cfg)...)
	ok("data of the file", stdout, stderr, code, data, false)

	other := filepath.Join(t.TempDir(), "other")
	cfg = file("data: data2\n")
	code, stdout, stderr = runInitCmd(t, initPassword+"\n", adminFlags("-config", cfg, "-data", other)...)
	ok("-data over data", stdout, stderr, code, other, false)
	if s := snapshot(t, filepath.Join(conf, "data2")); s != "missing" {
		t.Errorf("-data over data: the directory of data is made:\n%s", s)
	}

	// No data directory.
	code, stdout, stderr = runInitCmd(t, initPassword+"\n", adminFlags("-config", file("theme: ../theme\n"))...)
	if want := "whiteroom init: no data directory: give -data DIR, or data in the config file\n" + help; code != 2 || stdout != "" ||
		stderr != want {
		t.Errorf("no data: whiteroom init = %d, with stdout %q and stderr:\n%s\nwant 2, and:\n%s", code, stdout, stderr, want)
	}

	// The site of the file is not imported, and its theme is not used without SITE.
	data = filepath.Join(t.TempDir(), "data")
	cfg = file("data: " + data + "\nsite: ../site\ntheme: ../missing\n")
	code, stdout, stderr = runInitCmd(t, initPassword+"\n", adminFlags("-config", cfg)...)
	ok("site of the file", stdout, stderr, code, data, false)

	// SITE, with the theme of the file: SITE has no theme/ of its own.
	if _, err := os.Stat(filepath.Join(site, "theme")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s has a theme: %v", site, err)
	}
	data = filepath.Join(t.TempDir(), "data")
	cfg = file("data: " + data + "\ntheme: ../theme\n")
	code, stdout, stderr = runInitCmd(t, initPassword+"\n", adminFlags("-config", cfg, site)...)
	ok("SITE and theme of the file", stdout, stderr, code, data, true)

	// -theme over the theme of the file.
	data = filepath.Join(t.TempDir(), "data")
	cfg = file("data: " + data + "\ntheme: ../missing\n")
	code, stdout, stderr = runInitCmd(t, initPassword+"\n", adminFlags("-config", cfg, "-theme", theme, site)...)
	ok("-theme over theme", stdout, stderr, code, data, true)

	// The theme of the file is read: a theme that is missing fails.
	data = filepath.Join(t.TempDir(), "data")
	cfg = file("data: " + data + "\ntheme: ../missing\n")
	code, stdout, stderr = runInitCmd(t, initPassword+"\n", adminFlags("-config", cfg, site)...)
	if want := "whiteroom init: open " + missing + ": no such file or directory\n"; code != 1 || stdout != "" || stderr != want {
		t.Errorf("a missing theme of the file: whiteroom init = %d, with stdout %q and stderr:\n%s\nwant 1, and %q", code, stdout,
			stderr, want)
	}
	if s := snapshot(t, data); s != "missing" {
		t.Errorf("a missing theme of the file: the data directory is made:\n%s", s)
	}

	// A problem in the file: before the standard input is read, and nothing is made.
	data = filepath.Join(t.TempDir(), "data")
	cfg = file("data: " + data + "\nbogus: 1\n")
	var o, e bytes.Buffer
	code = run(t.Context(), append([]string{"init"}, adminFlags("-config", cfg)...), testEnv(failingReader{t}, &o, &e))
	if want := cfg + ":2:1: /bogus: unknown item\n"; code != 2 || o.Len() != 0 || e.String() != want {
		t.Errorf("a problem in the file: whiteroom init = %d, with stdout %q and stderr:\n%s\nwant 2, and %q", code, o.String(),
			e.String(), want)
	}
	if s := snapshot(t, data); s != "missing" {
		t.Errorf("a problem in the file: the data directory is made:\n%s", s)
	}

	// No file is read without -config.
	t.Chdir(conf)
	data = filepath.Join(t.TempDir(), "data")
	file("data: " + data + "\n")
	code, stdout, stderr = runInitCmd(t, initPassword+"\n", adminFlags()...)
	if code != 2 || stdout != "" || !strings.HasPrefix(stderr, "whiteroom init: no data directory") {
		t.Errorf("no -config: whiteroom init = %d, with stdout %q and stderr:\n%s\nwant 2, and no data directory", code, stdout, stderr)
	}
}

// TestInitURL imports a site with -url and with the item url of a config file: a redirect that leads back to itself under the
// URL, which is none under the URL of the settings and under another URL, is a problem of the import, which makes no data
// directory; the settings that the import stores keep the url that is written in them. The flag wins over the item. Without
// SITE, -url is a usage error, and the item is not used and not looked at. A URL that does not pass is a usage error that
// reads no standard input and makes no data directory.
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
	// item returns the path of a config file with the item url u.
	item := func(u string) string {
		files++
		return writeConfig(t, conf, "url-"+strconv.Itoa(files)+".yaml", "url: "+strconv.Quote(u)+"\n")
	}

	// imports checks that whiteroom init of dir, with the flags more before it, makes a data directory with the site in it,
	// and returns the data directory.
	imports := func(what, dir string, more ...string) string {
		t.Helper()
		data := filepath.Join(t.TempDir(), "data")
		args := append(adminFlags("-data", data, "-theme", exampleTheme), more...)
		code, stdout, stderr := runInitCmd(t, initPassword+"\n", append(args, dir)...)
		if want := sampleImportWarnings(t, dir); code != 0 || stderr != want || !strings.HasSuffix(stdout, "imported the site "+dir+"\n") {
			t.Fatalf("%s: whiteroom init = %d, with stdout:\n%s\nand stderr:\n%s\nwant 0, the import, and the warnings:\n%s", what, code, stdout,
				stderr, want)
		}
		if got := inspect(t, data, initPassword); len(got.docs) < 10 {
			t.Errorf("%s: the store has %d documents, want those of the site", what, len(got.docs))
		}
		return data
	}
	// refused checks that whiteroom init of the site, with the flags more before it, is refused for the redirect that leads
	// back to itself, and makes no data directory.
	refused := func(what string, more ...string) {
		t.Helper()
		data := filepath.Join(t.TempDir(), "data")
		args := append(adminFlags("-data", data, "-theme", exampleTheme), more...)
		code, stdout, stderr := runInitCmd(t, initPassword+"\n", append(args, site)...)
		if code != 1 || stdout != "" || !strings.HasSuffix(stderr, problem) {
			t.Errorf("%s: whiteroom init = %d, with stdout %q and stderr:\n%s\nwant 1, and the problem, which is the last line:\n%s", what, code,
				stdout, stderr, problem)
		}
		if s := snapshot(t, data); s != "missing" {
			t.Errorf("%s: the data directory is made:\n%s", what, s)
		}
		checkNoSecret(t, what, initPassword, stdout, stderr, data)
	}

	imports("no URL", site)
	refused("-url", "-url", staging)
	imports("-url of another site", site, "-url", other)
	imports("-url of the URL of the settings", site, "-url", "https://example.com")
	refused("the item", "-config", item(staging))
	imports("the item of another site", site, "-config", item(other))
	refused("-url over the item", "-config", item(other), "-url", staging)
	imports("-url over the item that leads back to itself", site, "-config", item(staging), "-url", "https://example.com")
	refused("-url of no text over the item", "-config", item(staging), "-url", "")
	imports("-url over an item that does not pass", sampleSite, "-config", item("https://example.com/"), "-url", staging)

	// The settings that are stored have the url that is written in them.
	data := imports("-url of the sample site", sampleSite, "-url", staging)
	if dataHas(t, data, "staging.example.com") || !dataHas(t, data, "https://example.com") {
		t.Errorf("the data directory has the URL of the import (%t), or lacks the url of the settings (%t)",
			dataHas(t, data, "staging.example.com"), !dataHas(t, data, "https://example.com"))
	}

	t.Run("no SITE", func(t *testing.T) {
		data := filepath.Join(t.TempDir(), "data")
		var o, e bytes.Buffer
		code := run(t.Context(), append([]string{"init"}, adminFlags("-data", data, "-url", staging)...), testEnv(failingReader{t}, &o, &e))
		want := "whiteroom init: -url is for SITE: give the directory SITE of the site to import\n" + initHelp(t)
		if code != 2 || o.Len() != 0 || e.String() != want {
			t.Errorf("whiteroom init -url without a SITE = %d, with stdout %q and stderr:\n%s\nwant 2, and:\n%s", code, o.String(), e.String(), want)
		}
		if s := snapshot(t, data); s != "missing" {
			t.Errorf("-url without a SITE: the data directory is made:\n%s", s)
		}
		// The item is used with a SITE only, and is not looked at without it.
		for _, value := range []string{staging, "https://example.com/"} {
			data := filepath.Join(t.TempDir(), "data")
			code, stdout, stderr := runInitCmd(t, initPassword+"\n", adminFlags("-data", data, "-config", item(value))...)
			if code != 0 || stderr != "" || strings.Contains(stdout, "imported") {
				t.Errorf("the item %s without a SITE: whiteroom init = %d, with stdout %q and stderr:\n%s\nwant 0, and no import", value, code,
					stdout, stderr)
			}
			checkAdmin(t, "the item "+value+" without a SITE", inspect(t, data, initPassword))
		}
	})

	t.Run("a URL that does not pass", func(t *testing.T) {
		for _, tt := range badSiteURLs[:3] {
			for _, fromItem := range []bool{false, true} {
				what, args := "-url", []string{"-url", tt.value}
				if fromItem {
					what, args = "url of the config file", []string{"-config", item(tt.value)}
				}
				data := filepath.Join(t.TempDir(), "data")
				var o, e bytes.Buffer
				code := run(t.Context(), append(append([]string{"init"}, adminFlags("-data", data)...), append(args, site)...),
					testEnv(failingReader{t}, &o, &e))
				want := "whiteroom init: " + what + " is not a valid site URL: " + tt.reason + "\n"
				if code != 2 || o.Len() != 0 || e.String() != want {
					t.Errorf("%s of %q: whiteroom init = %d, with stdout %q and stderr:\n%s\nwant 2, with no stdout and:\n%s", what, tt.value, code,
						o.String(), e.String(), want)
				}
				if s := snapshot(t, data); s != "missing" {
					t.Errorf("%s of %q: the data directory is made:\n%s", what, tt.value, s)
				}
			}
		}
	})
}
