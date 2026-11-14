//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// writeConfig writes text as the file name in dir, which it makes, and returns the path of the file.
func writeConfig(t *testing.T, dir, name, text string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, name)
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

// sameConfig reports whether a and b are the same, a list of trusted proxies being the same as one with the same items.
func sameConfig(a, b config) bool {
	same := slices.Equal(a.trustedProxies, b.trustedProxies)
	a.trustedProxies, b.trustedProxies = nil, nil
	return same && reflect.DeepEqual(a, b)
}

// linesOf returns the lines of the problems that the file has, each line after the path of the file, which the lines
// follow without it, with the line breaks.
func linesOf(file string, lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(file + ":" + l + "\n")
	}
	return b.String()
}

// sizedConfig returns a config file of n bytes that gives the item data, with a comment for the rest.
func sizedConfig(n int) string {
	head := "data: a\n# "
	return head + strings.Repeat("x", n-len(head)-1) + "\n"
}

// TestLoadConfig checks the items of config files: that every item is read, with the paths from the directory of the
// file, unless they are absolute, and that nothing is read for no file; and that each kind of problem is reported, a
// line for each, in the order of their positions in the file, and without the values that the file gives.
func TestLoadConfig(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "conf")
	abs := filepath.Join(root, "abs")
	all := fmt.Sprintf(`# the settings of the staging server
data: data
site: ../site
theme: %s
out: out/releases
cache: ./cache
addr: 127.0.0.1:8081
socket: %s
origin: https://admin.example.com
trustedProxies:
  - 10.0.0.0/8
  - ::1
workers: 4
memory: 512
`, strconv.Quote(abs), strconv.Quote(filepath.Join(abs, "admin.sock")))
	in := func(name string) string { return filepath.Join(dir, name) }
	for _, tt := range []struct {
		what string
		text string
		want config
	}{
		{
			"every item", all,
			config{
				data: in("data"), site: filepath.Join(root, "site"), theme: abs, out: in("out/releases"), cache: in("cache"),
				addr: "127.0.0.1:8081", socket: filepath.Join(abs, "admin.sock"), origin: "https://admin.example.com",
				trustedProxies: []string{"10.0.0.0/8", "::1"}, workers: 4, memory: 512,
			},
		},
		{"one item", "out: o\n", config{out: in("o")}},
		{"an address with brackets", "addr: \"[::1]:8081\"\n", config{addr: "[::1]:8081"}},
		{"numbers of 0", "workers: 0\nmemory: 0\n", config{}},
		{"the largest memory", fmt.Sprintf("memory: %d\n", maxMemory), config{memory: maxMemory}},
		{"no proxies", "trustedProxies: []\n", config{}},
		{"an empty mapping", "{}\n", config{}},
		{"the largest size", sizedConfig(maxConfigSize), config{data: in("a")}},
	} {
		file := writeConfig(t, dir, "whiteroom.yaml", tt.text)
		got, err := loadConfig(file)
		if err != nil || !sameConfig(got, tt.want) {
			t.Errorf("%s: loadConfig = %+v, %v\nwant %+v", tt.what, got, err, tt.want)
		}
	}

	t.Run("a relative path of the file", func(t *testing.T) {
		writeConfig(t, filepath.Join(root, "rel", "conf"), "whiteroom.yaml", "data: data\nsite: ../site\ntheme: /abs\n")
		t.Chdir(filepath.Join(root, "rel"))
		got, err := loadConfig(filepath.Join("conf", "whiteroom.yaml"))
		want := config{data: filepath.Join("conf", "data"), site: "site", theme: "/abs"}
		if err != nil || !sameConfig(got, want) {
			t.Errorf("loadConfig = %+v, %v\nwant %+v", got, err, want)
		}
		writeConfig(t, filepath.Join(root, "rel", "conf"), "whiteroom.yaml", "data:\t/a\n")
		_, err = loadConfig(filepath.Join("conf", "whiteroom.yaml"))
		if want := filepath.Join("conf", "whiteroom.yaml") + ":1:6: tab characters are not allowed; use spaces"; err == nil ||
			err.Error() != want {
			t.Errorf("loadConfig of a broken file = %v, want %s", err, want)
		}
	})

	t.Run("no file", func(t *testing.T) {
		writeConfig(t, filepath.Join(root, "none"), "whiteroom.yaml", "data:\t/a\n")
		writeConfig(t, filepath.Join(root, "none"), "config.yaml", "data:\t/a\n")
		t.Chdir(filepath.Join(root, "none"))
		if got, err := loadConfig(""); err != nil || !sameConfig(got, config{}) {
			t.Errorf("loadConfig(\"\") = %+v, %v, want the zero config", got, err)
		}
	})

	const (
		notMapping = "must be a mapping of items"
		notString  = "must be a string"
		notWorkers = "must be an integer of 0 or more"
		notMemory  = "must be an integer from 0 to 8796093022207"
		notList    = "must be a list of strings"
		empty      = "must not be empty"
	)
	for _, tt := range []struct {
		what string
		text string
		want []string // the lines after the file
		vals []string // what the file gives, which no line has
	}{
		{"an unknown item", "unknown: 1\n", []string{"1:1: /unknown: unknown item"}, nil},
		{"an unknown item with a slash", "a: 1\n\"b/c~d\": 1\n", []string{"1:1: /a: unknown item", "2:1: /b~1c~0d: unknown item"}, nil},
		{"a number for a path", "data: 123\n", []string{"1:1: /data: " + notString}, []string{"123"}},
		{"a number for an address", "addr: 8081\n", []string{"1:1: /addr: " + notString}, []string{"8081"}},
		{"a list for an origin", "origin: [secret]\n", []string{"1:1: /origin: " + notString}, []string{"secret"}},
		{"a text for workers", "workers: \"4\"\n", []string{"1:1: /workers: " + notWorkers}, []string{"4"}},
		{"null for workers", "workers: null\n", []string{"1:10: null is not allowed; remove the key"}, nil},
		{"true for memory", "memory: true\n", []string{"1:1: /memory: " + notMemory}, nil},
		{"a text for proxies", "trustedProxies: 10.0.0.1\n", []string{"1:1: /trustedProxies: " + notList}, []string{"10.0.0.1"}},
		{"a number in proxies", "trustedProxies: [1]\n", []string{"1:18: /trustedProxies/0: " + notString}, []string{"1"}},
		{"a fraction for memory", "memory: 1.5\n", []string{"1:1: /memory: " + notMemory}, []string{"1.5"}},
		{"a negative workers", "workers: -1\n", []string{"1:1: /workers: " + notWorkers}, []string{"-1"}},
		{"a negative memory", "memory: -1\n", []string{"1:1: /memory: " + notMemory}, []string{"-1"}},
		{"a memory over the largest", fmt.Sprintf("memory: %d\n", maxMemory+1), []string{"1:1: /memory: " + notMemory},
			[]string{strconv.FormatInt(maxMemory+1, 10)}},
		{"a workers over the integers", "workers: 99999999999999999999\n", []string{"1:1: /workers: " + notWorkers},
			[]string{"99999999999999999999"}},
		{"an empty path", "data: \"\"\n", []string{"1:1: /data: " + empty}, nil},
		{"an empty address", "addr: \"\"\n", []string{"1:1: /addr: " + empty}, nil},
		{"an empty proxy", "trustedProxies: [\"\"]\n", []string{"1:18: /trustedProxies/0: " + empty}, nil},
		{
			"three problems", "workers: \"4\"\nmemory: 1.5\ndata: 123\n",
			[]string{"1:1: /workers: " + notWorkers, "2:1: /memory: " + notMemory, "3:1: /data: " + notString},
			[]string{"4", "1.5", "123"},
		},
		{
			"problems in a list and after it", "trustedProxies:\n  - \"\"\n  - 5\nworkers: -1\nsocket: 4\n",
			[]string{
				"2:5: /trustedProxies/0: " + empty, "3:5: /trustedProxies/1: " + notString, "4:1: /workers: " + notWorkers,
				"5:1: /socket: " + notString,
			},
			[]string{"-1"},
		},
		{"a list", "- a\n- b\n", []string{"1:1: " + notMapping}, nil},
		{"a list after a comment", "# the settings\n- a\n", []string{"2:1: " + notMapping}, nil},
		{"a text", "secret\n", []string{"1:1: " + notMapping}, []string{"secret"}},
		{"a number", "5\n", []string{"1:1: " + notMapping}, nil},
		{"null", "~\n", []string{"1:1: null is not allowed"}, nil},
		{"an empty file", "", []string{"1:1: empty document"}, nil},
		{"only a comment", "# only a comment\n", []string{"1:1: empty document"}, nil},
		{"an item twice", "data: /a\ndata: /b\n", []string{"2:1: duplicate key \"data\""}, nil},
		{"an item without a value", "data: /a\nsocket:\n", []string{"2:1: missing value"}, nil},
		{"a tab", "data:\t/a\n", []string{"1:6: tab characters are not allowed; use spaces"}, nil},
	} {
		file := writeConfig(t, dir, "whiteroom.yaml", tt.text)
		got, err := loadConfig(file)
		var es content.Errors
		if !errors.As(err, &es) {
			t.Errorf("%s: loadConfig = %+v, %v, want problems", tt.what, got, err)
			continue
		}
		var lines []string
		for _, e := range es {
			lines = append(lines, e.Error())
			for _, v := range tt.vals {
				if strings.Contains(e.Msg+e.Path, v) {
					t.Errorf("%s: the problem %q has the value %q that the file gives", tt.what, e.Error(), v)
				}
			}
		}
		if want := linesOf(file, tt.want); strings.Join(lines, "\n")+"\n" != want || !sameConfig(got, config{}) {
			t.Errorf("%s: loadConfig = %+v, with the problems:\n%s\nwant:\n%s", tt.what, got, strings.Join(lines, "\n"), want)
		}
	}

	missing := filepath.Join(root, "missing.yaml")
	if _, err := loadConfig(missing); !errors.Is(err, fs.ErrNotExist) || errors.As(err, new(content.Errors)) {
		t.Errorf("loadConfig of a missing file = %v, want an error of fs.ErrNotExist", err)
	}
	if _, err := loadConfig(dir); err == nil || errors.As(err, new(content.Errors)) {
		t.Errorf("loadConfig of a directory = %v, want an error that is not about the content", err)
	}
	large := writeConfig(t, dir, "large.yaml", sizedConfig(maxConfigSize+1))
	_, err := loadConfig(large)
	if want := large + " is larger than 1048576 bytes"; err == nil || err.Error() != want || errors.As(err, new(content.Errors)) {
		t.Errorf("loadConfig of a file of %d bytes = %v, want %s", maxConfigSize+1, err, want)
	}
}

// TestPrintConfigError checks that the problems in a config file print a line each, and that any other error prints
// after the name of the command.
func TestPrintConfigError(t *testing.T) {
	problems := content.Errors{
		{File: "a.yaml", Line: 2, Column: 3, Path: "/data", Msg: "must be a string"},
		{File: "a.yaml", Line: 4, Column: 1, Msg: "unknown"},
	}
	for _, tt := range []struct {
		err  error
		want string
	}{
		{problems, "a.yaml:2:3: /data: must be a string\na.yaml:4:1: unknown\n"},
		{fmt.Errorf("x: %w", problems), "a.yaml:2:3: /data: must be a string\na.yaml:4:1: unknown\n"},
		{errors.New("open a.yaml: no such file"), "whiteroom serve admin: open a.yaml: no such file\n"},
	} {
		var b strings.Builder
		printConfigError(&b, "whiteroom serve admin", tt.err)
		if b.String() != tt.want {
			t.Errorf("printConfigError(%v) printed:\n%s\nwant:\n%s", tt.err, b.String(), tt.want)
		}
	}
}

// TestFlagGiven checks that a flag is given if the command line has it, also with its default value, and not if it
// does not.
func TestFlagGiven(t *testing.T) {
	for _, tt := range []struct {
		args []string
		a, b bool
	}{
		{nil, false, false},
		{[]string{"-a", "0"}, true, false},
		{[]string{"-b=x"}, false, true},
		{[]string{"-b", "", "-a=0", "rest"}, true, true},
	} {
		flags := flag.NewFlagSet("test", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		flags.Int("a", 0, "")
		flags.String("b", "x", "")
		if err := flags.Parse(tt.args); err != nil {
			t.Fatal(err)
		}
		if a, b := flagGiven(flags, "a"), flagGiven(flags, "b"); a != tt.a || b != tt.b {
			t.Errorf("flagGiven after %v = %t for a and %t for b, want %t and %t", tt.args, a, b, tt.a, tt.b)
		}
		if flagGiven(flags, "c") {
			t.Errorf("flagGiven after %v is true for c, which is not a flag", tt.args)
		}
	}
}

// TestBuildSettings checks the order of the values of whiteroom build: what the command line gives, then the config
// file, then the defaults; and for the numbers, that a flag with 0 gives 0.
func TestBuildSettings(t *testing.T) {
	cfg := config{site: "cs", out: "co", cache: "cc", theme: "ct", workers: 3, memory: 7}
	for _, tt := range []struct {
		what string
		cfg  config
		args []string
		want buildSettings
	}{
		{"no config", config{}, nil, buildSettings{site: ".", out: "out", cache: "cache"}},
		{"only a site", config{site: "cs"}, nil,
			buildSettings{site: "cs", out: filepath.Join("cs", "out"), cache: filepath.Join("cs", "cache")}},
		{"a site and its argument", config{site: "cs"}, []string{"a"},
			buildSettings{site: "a", out: filepath.Join("a", "out"), cache: filepath.Join("a", "cache")}},
		{"the config", cfg, nil, buildSettings{site: "cs", out: "co", cache: "cc", theme: "ct", workers: 3, memory: 7}},
		{"the config and a site argument", cfg, []string{"a"},
			buildSettings{site: "a", out: "co", cache: "cc", theme: "ct", workers: 3, memory: 7}},
		{"the config and the flags", cfg, []string{"-out", "o", "-cache", "c", "-theme", "t", "-workers", "5", "-memory", "9", "a"},
			buildSettings{site: "a", out: "o", cache: "c", theme: "t", workers: 5, memory: 9}},
		{"the config and the flags of 0", cfg, []string{"-workers", "0", "-memory=0"},
			buildSettings{site: "cs", out: "co", cache: "cc", theme: "ct"}},
		{"a flag of the config with another flag", config{workers: 3, memory: 7}, []string{"-workers=2"},
			buildSettings{site: ".", out: "out", cache: "cache", workers: 2, memory: 7}},
	} {
		flags, f := newBuildFlags(io.Discard)
		if err := flags.Parse(tt.args); err != nil {
			t.Fatal(err)
		}
		if got := f.settings(flags, tt.cfg); got != tt.want {
			t.Errorf("%s: settings = %+v, want %+v", tt.what, got, tt.want)
		}
	}
}

// emptyDir makes the working directory of the test a new empty directory, which it returns, so that a file that a
// command makes by mistake in the working directory shows. The paths of the test must not be relative.
func emptyDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	return dir
}

// checkEmpty checks that the directory dir has nothing in it.
func checkEmpty(t *testing.T, what, dir string) {
	t.Helper()
	if entries, err := os.ReadDir(dir); err != nil || len(entries) > 0 {
		t.Errorf("%s: %s holds %v (%v)", what, dir, entries, err)
	}
}

// sampleRoot makes a new directory with the sample site in site/ without a theme, the example theme in theme/ and an
// empty conf/, and returns it. The working directory must be that of the package.
func sampleRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.CopyFS(filepath.Join(root, "site"), os.DirFS(sampleSite)); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(filepath.Join(root, "theme"), os.DirFS(exampleTheme)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "conf"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestBuildConfig checks whiteroom build with config files: that the paths of the file are from its directory, and
// that the flags and the argument win over the file, which wins over the defaults; that the items that the build does
// not use do no harm; that a problem in the file is a usage error with no output; and that no file is read without
// -config.
func TestBuildConfig(t *testing.T) {
	// What reads the golden files, and the sample site, by their relative paths, comes before the subtests go to another
	// working directory.
	refOut := t.TempDir()
	buildSample(t, refOut, t.TempDir())
	release, refSummary := currentOf(t, refOut), sampleSummary(t, refOut, true)

	// published checks a build of the sample site that has returned code, with stdout and stderr: that it published the
	// release of buildSample in out, with its summary, and printed the warnings.
	published := func(t *testing.T, what string, code int, stdout, stderr, out, warnings string) {
		t.Helper()
		if code != 0 {
			t.Fatalf("%s: whiteroom build = %d, with stderr:\n%s", what, code, stderr)
		}
		if got := currentOf(t, out); got != release {
			t.Errorf("%s: %s is at the release %s, want %s", what, out, got, release)
		}
		if want := strings.Replace(refSummary, " in "+refOut+":", " in "+out+":", 1); stdout != want {
			t.Errorf("%s: whiteroom build printed %q, want %q", what, stdout, want)
		}
		if stderr != warnings {
			t.Errorf("%s: whiteroom build printed the warnings:\n%s\nwant:\n%s", what, stderr, warnings)
		}
	}

	t.Run("the paths of the file", func(t *testing.T) {
		root := sampleRoot(t)
		file := writeConfig(t, filepath.Join(root, "conf"), "whiteroom.yaml",
			"site: ../site\ntheme: ../theme\nout: ../out\ncache: ../cache\n")
		warnings := sampleWarnings(t, filepath.Join(root, "site"), filepath.Join(root, "theme"))
		cwd := emptyDir(t)
		code, stdout, stderr := runCmd(t, "build", "-config", file)
		published(t, "the paths of the file", code, stdout, stderr, filepath.Join(root, "out"), warnings)
		checkEmpty(t, "the paths of the file", cwd)
		if _, err := os.Stat(filepath.Join(root, "cache")); err != nil {
			t.Errorf("the paths of the file: %v", err)
		}
	})

	t.Run("items that the build does not use", func(t *testing.T) {
		root := sampleRoot(t)
		file := writeConfig(t, filepath.Join(root, "conf"), "whiteroom.yaml", `# the settings of every command
site: ../site
theme: ../theme
out: ../out
cache: ../cache
addr: 127.0.0.1:8081
socket: ../run/admin.sock
origin: https://admin.example.com
trustedProxies:
  - 10.0.0.0/8
  - ::1
workers: 2
memory: 256
`)
		warnings := sampleWarnings(t, filepath.Join(root, "site"), filepath.Join(root, "theme"))
		cwd := emptyDir(t)
		code, stdout, stderr := runCmd(t, "build", "-config", file)
		published(t, "items that the build does not use", code, stdout, stderr, filepath.Join(root, "out"), warnings)
		checkEmpty(t, "items that the build does not use", cwd)
		checkMissing(t, "items that the build does not use", filepath.Join(root, "run"))
	})

	t.Run("the defaults from the site", func(t *testing.T) {
		dir := copySample(t)
		root := filepath.Dir(dir)
		file := writeConfig(t, filepath.Join(root, "conf"), "whiteroom.yaml", "site: ../site\n")
		warnings := sampleWarnings(t, dir, "")
		cwd := emptyDir(t)
		code, stdout, stderr := runCmd(t, "build", "-config", file)
		published(t, "the defaults from the site", code, stdout, stderr, filepath.Join(dir, "out"), warnings)
		checkEmpty(t, "the defaults from the site", cwd)
		if _, err := os.Stat(filepath.Join(dir, "cache", "CACHEDIR.TAG")); err != nil {
			t.Errorf("the defaults from the site: %v", err)
		}
	})

	t.Run("the flags and the argument win", func(t *testing.T) {
		root := sampleRoot(t)
		file := writeConfig(t, filepath.Join(root, "conf"), "whiteroom.yaml",
			"site: ../missing-site\ntheme: ../missing-theme\nout: ../cfg-out\ncache: ../cfg-cache\nworkers: 1\n")
		warnings := sampleWarnings(t, filepath.Join(root, "site"), filepath.Join(root, "theme"))
		cwd := emptyDir(t)
		out, cache := filepath.Join(root, "out"), filepath.Join(root, "cache")
		code, stdout, stderr := runCmd(t, "build", "-config", file, "-theme", filepath.Join(root, "theme"), "-out", out,
			"-cache", cache, "-workers", "0", filepath.Join(root, "site"))
		published(t, "the flags and the argument win", code, stdout, stderr, out, warnings)
		checkEmpty(t, "the flags and the argument win", cwd)
		for _, name := range []string{"cfg-out", "cfg-cache"} {
			checkMissing(t, "the flags and the argument win", filepath.Join(root, name))
		}
		if _, err := os.Stat(cache); err != nil {
			t.Errorf("the flags and the argument win: %v", err)
		}
	})

	t.Run("the argument wins only over the site", func(t *testing.T) {
		root := sampleRoot(t)
		file := writeConfig(t, filepath.Join(root, "conf"), "whiteroom.yaml",
			"site: ../missing-site\ntheme: ../theme\nout: ../out\ncache: ../cache\n")
		warnings := sampleWarnings(t, filepath.Join(root, "site"), filepath.Join(root, "theme"))
		emptyDir(t)
		code, stdout, stderr := runCmd(t, "build", "-config", file, filepath.Join(root, "site"))
		published(t, "the argument wins only over the site", code, stdout, stderr, filepath.Join(root, "out"), warnings)
	})

	t.Run("a problem in the file", func(t *testing.T) {
		root := sampleRoot(t)
		head := "site: ../site\ntheme: ../theme\nout: ../out\ncache: ../cache\n"
		cwd := emptyDir(t)
		for _, tt := range []struct {
			what, text string
			want       []string // the lines after the file
		}{
			{"a negative number", head + "workers: -1\n", []string{"5:1: /workers: must be an integer of 0 or more"}},
			{"two problems", head + "workers: \"4\"\nunknown: 1\n",
				[]string{"5:1: /workers: must be an integer of 0 or more", "6:1: /unknown: unknown item"}},
			{"a syntax error", "site: ../site\nout:\t../out\n", []string{"2:5: tab characters are not allowed; use spaces"}},
			{"a list", "- ../site\n", []string{"1:1: must be a mapping of items"}},
			{"an empty file", "", []string{"1:1: empty document"}},
		} {
			file := writeConfig(t, filepath.Join(root, "conf"), "whiteroom.yaml", tt.text)
			code, stdout, stderr := runCmd(t, "build", "-config", file)
			if want := linesOf(file, tt.want); code != 2 || stdout != "" || stderr != want {
				t.Errorf("%s: whiteroom build = %d, with stdout %q and stderr:\n%s\nwant 2, with no stdout and stderr:\n%s",
					tt.what, code, stdout, stderr, want)
			}
			for _, name := range []string{"out", "cache"} {
				checkMissing(t, tt.what, filepath.Join(root, name))
			}
			checkEmpty(t, tt.what, cwd)
		}
	})

	t.Run("no config file without -config", func(t *testing.T) {
		root := sampleRoot(t)
		warnings := sampleWarnings(t, filepath.Join(root, "site"), filepath.Join(root, "theme"))
		cwd := emptyDir(t)
		for _, name := range []string{"whiteroom.yaml", "config.yaml"} {
			writeConfig(t, cwd, name, "data:\t/a\n")
		}
		out, cache := filepath.Join(root, "out"), filepath.Join(root, "cache")
		code, stdout, stderr := runCmd(t, "build", "-theme", filepath.Join(root, "theme"), "-out", out, "-cache", cache,
			filepath.Join(root, "site"))
		published(t, "no config file without -config", code, stdout, stderr, out, warnings)
	})
}
