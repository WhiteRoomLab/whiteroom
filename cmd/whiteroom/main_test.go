package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/build"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// runCmd runs the command with args and returns its exit code and what it has written to stdout and stderr.
func runCmd(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var o, e bytes.Buffer
	code = run(t.Context(), args, &o, &e)
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
	for _, name := range []string{"-cache DIR", "-config FILE", "-memory MiB", "-out DIR", "-theme DIR", "-workers N"} {
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

	unknown := "whiteroom: unknown command \"nope\"\nRun \"whiteroom help\" for usage.\n"
	tooMany := "whiteroom build: too many arguments: give at most one SITE, after the flags\n" + help
	tooManyOuts := "whiteroom serve public: too many arguments: give at most one OUT, after the flags\n" + serveHelp
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
		{[]string{"build", "-config", missing}, 2, "", "whiteroom build: " + openErr.Error() + "\n"},
		{[]string{"build", "-config", broken}, 2, "", broken + ":1:6: tab characters are not allowed; use spaces\n"},
		{[]string{"serve"}, 2, "", usage},
		{[]string{"serve", "-h"}, 0, usage, ""},
		{[]string{"serve", "help"}, 0, usage, ""},
		{[]string{"serve", "nope"}, 2, "", "whiteroom serve: unknown server \"nope\"\nRun \"whiteroom help\" for usage.\n"},
		{[]string{"serve", "admin"}, 2, "", "whiteroom serve: admin is not available yet\n"},
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
	for _, tt := range []struct {
		what  string
		ctx   context.Context
		theme string
		err   error
		want  []string
	}{
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
		errorPrinter{w: &b, site: "site", theme: tt.theme}.printError(tt.ctx, tt.err)
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
