package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/build"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// testEnv is the env of a command with the standard input stdin, which writes to stdout and stderr, makes the accounts
// with cheap password hashes, and gets no minute.
func testEnv(stdin io.Reader, stdout, stderr io.Writer) env {
	return env{
		stdin: stdin, stdout: stdout, stderr: stderr,
		newAccounts: func(s *store.Store) (*account.Manager, error) { return account.NewWithIterations(s, 1) },
		ticks:       func() (<-chan time.Time, func()) { return nil, func() {} },
	}
}

// runCmd runs the command with args and an empty standard input, and returns its exit code and what it has written to
// stdout and stderr.
func runCmd(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	return runCmdIn(t, strings.NewReader(""), args...)
}

// runCmdIn is runCmd with the standard input stdin.
func runCmdIn(t *testing.T, stdin io.Reader, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var o, e bytes.Buffer
	code = run(t.Context(), args, testEnv(stdin, &o, &e))
	return code, o.String(), e.String()
}

// TestRun checks the exit codes and the output of the command for the usage and for errors in the arguments, none of
// which builds or serves.
func TestRun(t *testing.T) {
	code, stdout, help := runCmd(t, "build", "-h")
	if code != 0 || stdout != "" || !strings.HasPrefix(help, buildUsage) {
		t.Fatalf("whiteroom build -h = %d, with stdout %q and stderr:\n%s\nwant 0, and the usage on stderr", code, stdout,
			help)
	}
	for _, name := range []string{"-cache DIR", "-config FILE", "-data DIR", "-memory MiB", "-out DIR", "-theme DIR", "-workers N"} {
		if !strings.Contains(help, "\n  "+name+"\n") {
			t.Errorf("the usage of whiteroom build has no flag %s:\n%s", name, help)
		}
	}
	code, stdout, serveHelp := runCmd(t, "serve", "public", "-h")
	if code != 0 || stdout != "" || !strings.HasPrefix(serveHelp, servePublicUsage) {
		t.Fatalf("whiteroom serve public -h = %d, with stdout %q and stderr:\n%s\nwant 0, and the usage on stderr", code,
			stdout, serveHelp)
	}
	if !strings.Contains(serveHelp, "\n  -addr ADDR\n") || !strings.Contains(serveHelp, `(default "127.0.0.1:8080")`) {
		t.Errorf("the usage of whiteroom serve public has no flag -addr ADDR with its default:\n%s", serveHelp)
	}

	code, stdout, adminHelp := runCmd(t, "serve", "admin", "-h")
	if code != 0 || stdout != "" || !strings.HasPrefix(adminHelp, serveAdminUsage) {
		t.Fatalf("whiteroom serve admin -h = %d, with stdout %q and stderr:\n%s\nwant 0, and the usage on stderr", code,
			stdout, adminHelp)
	}
	for _, name := range []string{"-addr ADDR", "-cache DIR", "-config FILE", "-data DIR", "-memory MiB", "-origin URL", "-out DIR",
		"-socket PATH", "-theme DIR", "-trusted-proxy PREFIX", "-workers N"} {
		if !strings.Contains(adminHelp, "\n  "+name+"\n") {
			t.Errorf("the usage of whiteroom serve admin has no flag %s:\n%s", name, adminHelp)
		}
	}
	if !strings.Contains(adminHelp, defaultAdminAddr) {
		t.Errorf("the usage of whiteroom serve admin does not tell the default of -addr, %s:\n%s", defaultAdminAddr, adminHelp)
	}
	if !strings.Contains(usage, "\n  whiteroom serve admin [flags] [SITE]\n") {
		t.Errorf("the list of the commands has no whiteroom serve admin:\n%s", usage)
	}

	code, stdout, rollbackHelp := runCmd(t, "rollback", "-h")
	if code != 0 || stdout != "" || rollbackHelp != rollbackUsage {
		t.Fatalf("whiteroom rollback -h = %d, with stdout %q and stderr:\n%s\nwant 0, and the usage on stderr", code, stdout,
			rollbackHelp)
	}
	for _, line := range []string{"whiteroom rollback OUT [RELEASE]", "whiteroom build -data DIR [flags] [SITE]"} {
		if !strings.Contains(usage, "\n  "+line+"\n") {
			t.Errorf("the list of the commands has no %s:\n%s", line, usage)
		}
	}

	code, stdout, initHelp := runCmd(t, "init", "-h")
	if code != 0 || stdout != "" || !strings.HasPrefix(initHelp, initUsage) {
		t.Fatalf("whiteroom init -h = %d, with stdout %q and stderr:\n%s\nwant 0, and the usage on stderr", code, stdout,
			initHelp)
	}
	for _, name := range []string{"-config FILE", "-data DIR", "-email ADDRESS", "-login LOGIN", "-name NAME", "-theme DIR"} {
		if !strings.Contains(initHelp, "\n  "+name+"\n") {
			t.Errorf("the usage of whiteroom init has no flag %s:\n%s", name, initHelp)
		}
	}
	if !strings.Contains(usage, "\n  whiteroom init [flags] [SITE]\n") {
		t.Errorf("the list of the commands has no whiteroom init:\n%s", usage)
	}

	unknown := "whiteroom: unknown command \"nope\"\nRun \"whiteroom help\" for usage.\n"
	tooMany := "whiteroom build: too many arguments: give at most one SITE, after the flags\n" + help
	initUsageErr := func(msg string) string { return "whiteroom init: " + msg + "\n" + initHelp }
	admin := []string{"-login", "a", "-name", "b", "-email", "c@example.com"}
	withAdmin := func(more ...string) []string { return append(append([]string{"init"}, admin...), more...) }
	tooManyInit := initUsageErr("too many arguments: give at most one SITE, after the flags")
	tooManyOuts := "whiteroom serve public: too many arguments: give at most one OUT, after the flags\n" + serveHelp
	adminErr := func(msg string) string { return "whiteroom serve admin: " + msg + "\n" + adminHelp }
	rollbackErr := func(msg string) string { return "whiteroom rollback: " + msg + "\n" + rollbackHelp }
	memory := func(m string) string {
		return "whiteroom build: -memory " + m + " is out of range (0 to 8796093022207)\n" + help
	}
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	_, openErr := os.Open(missing)
	broken := filepath.Join(t.TempDir(), "broken.yaml")
	if err := os.WriteFile(broken, []byte("data:\t/a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		args           []string
		code           int
		stdout, stderr string
	}{
		{nil, 2, "", usage},
		{[]string{"nope"}, 2, "", unknown},
		{[]string{"help"}, 0, usage, ""},
		{[]string{"-h"}, 0, usage, ""},
		{[]string{"-help"}, 0, usage, ""},
		{[]string{"--help"}, 0, usage, ""},
		{[]string{"build", "-x"}, 2, "", "flag provided but not defined: -x\n" + help},
		{[]string{"build", "-workers", "x"}, 2, "", "invalid value \"x\" for flag -workers: parse error\n" + help},
		{[]string{"build", "a", "b"}, 2, "", tooMany},
		{[]string{"build", "a", "-out", "b"}, 2, "", tooMany},
		{[]string{"build", "-memory", "-1"}, 2, "", memory("-1")},
		{[]string{"build", "-memory", "8796093022208"}, 2, "", memory("8796093022208")},
		{[]string{"build", "-workers", "-1"}, 2, "", "whiteroom build: -workers -1 is out of range (0 or more)\n" + help},
		{[]string{"build", "-data", "d", "-workers", "-1"}, 2, "",
			"whiteroom build: -workers -1 is out of range (0 or more)\n" + help},
		{[]string{"build", "-data", "d", "a", "b"}, 2, "", tooMany},
		{[]string{"build", "-config", missing}, 2, "", "whiteroom build: " + openErr.Error() + "\n"},
		{[]string{"build", "-config", broken}, 2, "", broken + ":1:6: tab characters are not allowed; use spaces\n"},
		{[]string{"init", "-x"}, 2, "", "flag provided but not defined: -x\n" + initHelp},
		{[]string{"init"}, 2, "", initUsageErr("-login is required")},
		{[]string{"init", "-login", "a"}, 2, "", initUsageErr("-name is required")},
		{[]string{"init", "-login", "a", "-name", "b"}, 2, "", initUsageErr("-email is required")},
		{withAdmin(), 2, "", initUsageErr("no data directory: give -data DIR, or data in the config file")},
		{withAdmin("-data", "d", "x", "y"), 2, "", tooManyInit},
		{[]string{"init", "x", "y"}, 2, "", tooManyInit},
		{withAdmin("-data", "d", "-theme", "t"), 2, "",
			initUsageErr("-theme is for SITE: give the directory SITE of the site to import")},
		{withAdmin("-config", missing), 2, "", "whiteroom init: " + openErr.Error() + "\n"},
		{withAdmin("-config", broken), 2, "", broken + ":1:6: tab characters are not allowed; use spaces\n"},
		{[]string{"rollback"}, 2, "", rollbackErr("no output directory: give OUT")},
		{[]string{"rollback", "a", "b", "c"}, 2, "", rollbackErr("too many arguments: give OUT and at most one RELEASE")},
		{[]string{"rollback", "-x"}, 2, "", "flag provided but not defined: -x\n" + rollbackHelp},
		{[]string{"rollback", "-config", "c.yaml", "a"}, 2, "", "flag provided but not defined: -config\n" + rollbackHelp},
		{[]string{"rollback", "-data", "d", "a"}, 2, "", "flag provided but not defined: -data\n" + rollbackHelp},
		{[]string{"serve"}, 2, "", usage},
		{[]string{"serve", "-h"}, 0, usage, ""},
		{[]string{"serve", "help"}, 0, usage, ""},
		{[]string{"serve", "nope"}, 2, "", "whiteroom serve: unknown server \"nope\"\nRun \"whiteroom help\" for usage.\n"},
		{[]string{"serve", "admin"}, 2, "", adminErr("no data directory: give -data DIR, or data in the config file")},
		{[]string{"serve", "admin", "-data", "d", "a", "b"}, 2, "",
			adminErr("too many arguments: give at most one SITE, after the flags")},
		{[]string{"serve", "admin", "-data", "d", "-addr", "localhost:8081"}, 2, "",
			adminErr(`-addr "localhost:8081" is not a loopback IP address and a port, such as 127.0.0.1:8081 or [::1]:8081`)},
		{[]string{"serve", "admin", "-data", "d", "-addr", "127.0.0.1:8081", "-socket", "s"}, 2, "",
			adminErr("-addr and -socket are both given: give one of them, as the server listens on one")},
		{[]string{"serve", "admin", "-data", "d", "-socket", "s"}, 2, "",
			adminErr("no origin: a socket has no address that the origin can be made of: give -origin URL, or origin in the " +
				"config file")},
		{[]string{"serve", "admin", "-data", "d", "-socket", "", "-origin", "http://localhost"}, 2, "",
			adminErr("-socket must not be empty")},
		{[]string{"serve", "admin", "-config", missing}, 2, "", "whiteroom serve admin: " + openErr.Error() + "\n"},
		{[]string{"serve", "admin", "-config", broken}, 2, "", broken + ":1:6: tab characters are not allowed; use spaces\n"},
		{[]string{"serve", "public", "-x"}, 2, "", "flag provided but not defined: -x\n" + serveHelp},
		{[]string{"serve", "public", "a", "b"}, 2, "", tooManyOuts},
		{[]string{"serve", "public", "a", "-addr", "b"}, 2, "", tooManyOuts},
	} {
		code, stdout, stderr := runCmd(t, tt.args...)
		if code != tt.code || stdout != tt.stdout || stderr != tt.stderr {
			t.Errorf("whiteroom %s = %d, with stdout:\n%s\nand stderr:\n%s\nwant %d, with stdout:\n%s\nand stderr:\n%s",
				strings.Join(tt.args, " "), code, stdout, stderr, tt.code, tt.stdout, tt.stderr)
		}
	}
}

// TestPrintError checks how the errors of builds print, a line for each: the problems of the site, with the paths of
// their files from the working directory, in the directory of -theme for those of the theme if it is given; each part
// of a join, also when one has problems; "interrupted" for the error of the context when it is done; and any other
// error after the name of the command.
func TestPrintError(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	in := func(dir, file string) string { return filepath.Join(dir, filepath.FromSlash(file)) }
	missingTitle := &content.Error{
		File: "revisions/rev_01JB8Y6T2RG7M4XK9P2D5W8NQ3.json", Line: 1, Column: 1, Path: "/title", Msg: "missing title",
	}
	missingTitleLine := in("site", missingTitle.File) + ":1:1: /title: missing title"
	problems := content.Errors{
		missingTitle,
		{File: "theme", Msg: "no translation"},
		{File: "theme/templates/entry.html", Line: 2, Column: 3, Msg: "unexpected EOF"},
		{File: "themes/a.json", Msg: "not the theme"},
		{Path: "/url", Msg: "no file"},
	}
	storedProblems := content.Errors{
		missingTitle,
		{File: "site/schema.yaml", Line: 1, Column: 2, Msg: "not YAML"},
		{File: "site", Msg: "missing directory"},
		{File: "site/settings.json", Msg: "not found"},
		{File: "theme", Msg: "no translation"},
		{File: "theme/templates/entry.html", Line: 2, Column: 3, Msg: "unexpected EOF"},
		{File: "themes/a.json", Msg: "not the theme"},
		{Path: "/url", Msg: "no file"},
	}
	for _, tt := range []struct {
		what   string
		ctx    context.Context
		theme  string
		stored bool
		err    error
		want   []string
	}{
		{
			what:   "problems of a data directory",
			ctx:    t.Context(),
			stored: true,
			err:    storedProblems,
			want: []string{
				missingTitle.File + ":1:1: /title: missing title",
				in("site", "site/schema.yaml") + ":1:2: not YAML",
				in("site", "site") + ": missing directory",
				"site/settings.json: not found",
				in("site", "theme") + ": no translation",
				in("site", "theme/templates/entry.html") + ":2:3: unexpected EOF",
				"themes/a.json: not the theme",
				"/url: no file",
			},
		},
		{
			what:   "problems of a data directory with -theme",
			ctx:    t.Context(),
			theme:  "../example",
			stored: true,
			err:    storedProblems,
			want: []string{
				missingTitle.File + ":1:1: /title: missing title",
				in("site", "site/schema.yaml") + ":1:2: not YAML",
				in("site", "site") + ": missing directory",
				"site/settings.json: not found",
				in("../example", "") + ": no translation",
				in("../example", "templates/entry.html") + ":2:3: unexpected EOF",
				"themes/a.json: not the theme",
				"/url: no file",
			},
		},
		{
			what: "problems",
			ctx:  t.Context(),
			err:  problems,
			want: []string{
				missingTitleLine,
				in("site", "theme") + ": no translation",
				in("site", "theme/templates/entry.html") + ":2:3: unexpected EOF",
				in("site", "themes/a.json") + ": not the theme",
				"/url: no file",
			},
		},
		{
			what:  "problems with -theme",
			ctx:   t.Context(),
			theme: "../example",
			err:   problems,
			want: []string{
				missingTitleLine,
				in("../example", "") + ": no translation",
				in("../example", "templates/entry.html") + ":2:3: unexpected EOF",
				in("site", "themes/a.json") + ": not the theme",
				"/url: no file",
			},
		},
		{what: "a problem", ctx: t.Context(), err: missingTitle, want: []string{missingTitleLine}},
		{
			what: "wrapped problems",
			ctx:  t.Context(),
			err:  fmt.Errorf("publishing the media: %w", content.Errors{missingTitle}),
			want: []string{missingTitleLine},
		},
		{
			what: "problems and another error",
			ctx:  t.Context(),
			err:  errors.Join(content.Errors{missingTitle}, errors.New("x")),
			want: []string{missingTitleLine, "whiteroom build: x"},
		},
		{
			what: "joins in a join",
			ctx:  t.Context(),
			err:  errors.Join(errors.Join(errors.New("x"), missingTitle), errors.Join(errors.New("y"))),
			want: []string{"whiteroom build: x", missingTitleLine, "whiteroom build: y"},
		},
		{what: "an error", ctx: t.Context(), err: errors.New("x"), want: []string{"whiteroom build: x"}},
		{what: "interrupted", ctx: canceled, err: context.Canceled, want: []string{"whiteroom build: interrupted"}},
		{
			what: "interrupted, with another error",
			ctx:  canceled,
			err:  errors.Join(context.Canceled, errors.New("x")),
			want: []string{"whiteroom build: interrupted", "whiteroom build: x"},
		},
		{
			what: "another error, after the context is done",
			ctx:  canceled,
			err:  errors.New("x"),
			want: []string{"whiteroom build: x"},
		},
	} {
		var b strings.Builder
		errorPrinter{w: &b, command: "whiteroom build", site: "site", theme: tt.theme, stored: tt.stored}.printError(tt.ctx, tt.err)
		if want := strings.Join(tt.want, "\n") + "\n"; b.String() != want {
			t.Errorf("%s: printed:\n%s\nwant:\n%s", tt.what, b.String(), want)
		}
	}
}

// TestSummary checks the summary of builds, with the plural of the numbers.
func TestSummary(t *testing.T) {
	warnings := func(n int) []*content.Error { return make([]*content.Error, n) }
	for _, tt := range []struct {
		res  build.Result
		want string
	}{
		{
			build.Result{Release: "0123456789abcdef", Changed: true, Pages: 50, Files: 69, Warnings: warnings(5)},
			"published release 0123456789abcdef in out: 50 pages, 69 files, 5 warnings",
		},
		{
			build.Result{Release: "0123456789abcdef", Pages: 2, Files: 3, Warnings: warnings(1)},
			"release 0123456789abcdef in out is already published: 2 pages, 3 files, 1 warning",
		},
		{
			build.Result{Release: "0123456789abcdef", Changed: true, Pages: 1, Files: 1},
			"published release 0123456789abcdef in out: 1 page, 1 file",
		},
	} {
		if got := summary(&tt.res, "out"); got != tt.want {
			t.Errorf("summary(%+v) = %q, want %q", tt.res, got, tt.want)
		}
	}
}

// TestSiteURL checks the URLs that whiteroom serve public prints: with localhost for an unspecified IP, and with the
// bytes of the path of the site URL that URLs cannot have percent-encoded in upper case, and the others as they are.
func TestSiteURL(t *testing.T) {
	for _, tt := range []struct{ addr, base, want string }{
		{"127.0.0.1:8080", "", "http://127.0.0.1:8080/"},
		{"127.0.0.1:41234", "/blog", "http://127.0.0.1:41234/blog/"},
		{"0.0.0.0:8080", "", "http://localhost:8080/"},
		{"[::]:8080", "/blog", "http://localhost:8080/blog/"},
		{"[::1]:8080", "", "http://[::1]:8080/"},
		{"127.0.0.1:8080", "/ブ%20ログ", "http://127.0.0.1:8080/%E3%83%96%20%E3%83%AD%E3%82%B0/"},
		{"127.0.0.1:8080", "/a(b)/it's/%e6", "http://127.0.0.1:8080/a(b)/it's/%e6/"},
		{"127.0.0.1:8080", "/a b\x01\x7f", "http://127.0.0.1:8080/a%20b%01%7F/"},
	} {
		if got := siteURL(tt.addr, tt.base); got != tt.want {
			t.Errorf("siteURL(%q, %q) = %q, want %q", tt.addr, tt.base, got, tt.want)
		}
	}
}
