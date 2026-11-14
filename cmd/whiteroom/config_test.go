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
url: https://staging.example.com
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
				url: "https://staging.example.com", trustedProxies: []string{"10.0.0.0/8", "::1"}, workers: 4, memory: 512,
			},
		},
		{"a url as written", "url: \"HTTPS://Example.com/\"\n", config{url: "HTTPS://Example.com/"}},
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
		{"a number for a url", "url: 123\n", []string{"1:1: /url: " + notString}, []string{"123"}},
		{"a list for a url", "url: [https://secret.example.com]\n", []string{"1:1: /url: " + notString},
			[]string{"secret.example.com"}},
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
		{"an empty url", "url: \"\"\n", []string{"1:1: /url: " + empty}, nil},
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

// TestSiteURLOverride checks the URL that replaces the url of the site settings: the flag, then the url of the config
// file, then none, a flag of "" being no flag; and that one which does not pass the rule of the url of the settings is
// told in one line, which says which of the two is wrong and why, and not what it is, so that a secret in a URL is not
// printed. The item that does not pass is not looked at if the flag gives another.
func TestSiteURLOverride(t *testing.T) {
	const (
		staging = "https://staging.example.com"
		other   = "https://other.example.com"
	)
	for _, tt := range []struct {
		what, flag string
		cfg        config
		want       string
		line       string // what it prints if the URL does not pass
	}{
		{"none", "", config{}, "", ""},
		{"the flag", staging, config{}, staging, ""},
		{"the item", "", config{url: staging}, staging, ""},
		{"the flag wins over the item", staging, config{url: other}, staging, ""},
		{"a flag of no text, and the item", "", config{url: other}, other, ""},
		{"a path", staging + "/blog", config{}, staging + "/blog", ""},
		{"a flag, and an item that does not pass", staging, config{url: other + "/"}, staging, ""},
		{"a flag that does not pass", other + "/", config{url: staging}, "",
			"whiteroom build: -url is not a valid site URL: must not end with \"/\"\n"},
		{"an item that does not pass", "", config{url: "https://EXAMPLE.com"}, "",
			"whiteroom build: url of the config file is not a valid site URL: the host must be in lower case\n"},
		{"credentials", "https://user:secret@example.com", config{}, "",
			"whiteroom build: -url is not a valid site URL: credentials in URLs are not allowed\n"},
	} {
		var b strings.Builder
		got, ok := siteURLOverride(&b, "whiteroom build", tt.flag, tt.cfg)
		if got != tt.want || ok != (tt.line == "") || b.String() != tt.line {
			t.Errorf("%s: siteURLOverride(%q, %+v) = %q, %t, and printed %q; want %q, %t, and %q", tt.what, tt.flag, tt.cfg, got, ok,
				b.String(), tt.want, tt.line == "", tt.line)
		}
	}
}

// badSiteURLs are values that do not pass the rule of the url of the site settings, with why.
var badSiteURLs = []struct{ value, reason string }{
	{"https://example.com/", `must not end with "/"`},
	{"https://EXAMPLE.com", "the host must be in lower case"},
	{"ftp://example.com", `scheme "ftp" is not allowed`},
	{"https://user:secret@example.com", "credentials in URLs are not allowed"},
	{"example.com", `must be an absolute URL or a path starting with "/"`},
	{"https://example.com:443", "port 443 is the default for https; omit it"},
	{"https://example.com/blog?page=2", "queries and fragments are not allowed"},
}

// TestBuildURLFlag builds the sample site with -url and with the item url of a config file: the release is another than
// that of the URL of the settings, and the same for the flag and the item; the flag wins over the item, and the item is
// not looked at then; the URL of the settings, and a flag of no text, give the release of no URL; and the site directory is
// as it was. A value that does not pass is a usage error of one line, with no usage text and not the value, before the
// output and the cache directories are made, whether the flag or the item gives it.
func TestBuildURLFlag(t *testing.T) {
	const (
		staging = "https://staging.example.com"
		other   = "https://other.example.com"
	)
	warnings := sampleWarnings(t, sampleSite, exampleTheme)
	// release builds the sample site with the flags more, into a new output directory, and returns the name of its release.
	release := func(t *testing.T, more ...string) string {
		t.Helper()
		out := t.TempDir()
		args := append([]string{"build", "-theme", exampleTheme, "-out", out, "-cache", t.TempDir()}, more...)
		code, stdout, stderr := runCmd(t, append(args, sampleSite)...)
		if code != 0 || stderr != warnings {
			t.Fatalf("whiteroom build %s = %d, with stderr:\n%s\nwant 0, and the warnings of the sample site:\n%s", strings.Join(more, " "),
				code, stderr, warnings)
		}
		name := currentOf(t, out)
		if want := "published release " + name + " in " + out + ": "; !strings.HasPrefix(stdout, want) {
			t.Errorf("whiteroom build %s printed %q, want it to start with %q", strings.Join(more, " "), stdout, want)
		}
		return name
	}
	conf, files := t.TempDir(), 0
	// item writes a config file with the item url u, and returns its path.
	item := func(u string) string {
		files++
		return writeConfig(t, conf, "url-"+strconv.Itoa(files)+".yaml", "url: "+strconv.Quote(u)+"\n")
	}

	plain := release(t)
	changed := release(t, "-url", staging)
	if changed == plain {
		t.Fatalf("the release of the URL %s is %s, that of the URL of the settings", staging, plain)
	}
	if again := release(t, "-url", staging); again != changed {
		t.Errorf("a second build with the URL %s is the release %s, the first %s", staging, again, changed)
	}

	t.Run("the item of the config file", func(t *testing.T) {
		if got := release(t, "-config", item(staging)); got != changed {
			t.Errorf("the item url of %s is the release %s, the flag %s", staging, got, changed)
		}
		if got := release(t, "-config", item(other)); got == plain || got == changed {
			t.Errorf("the item url of %s is the release %s, which is that of the URL of the settings (%s) or of %s (%s)", other, got, plain,
				staging, changed)
		}
	})

	t.Run("the flag wins over the item", func(t *testing.T) {
		if got := release(t, "-config", item(other), "-url", staging); got != changed {
			t.Errorf("-url %s over the item url of %s is the release %s, want %s", staging, other, got, changed)
		}
		// An item that does not pass is not looked at.
		if got := release(t, "-config", item("https://example.com/"), "-url", staging); got != changed {
			t.Errorf("-url %s over an item that does not pass is the release %s, want %s", staging, got, changed)
		}
	})

	t.Run("the URL of the settings, and no text", func(t *testing.T) {
		for _, more := range [][]string{{"-url", "https://example.com"}, {"-url", ""}, {"-config", item("https://example.com")}} {
			if got := release(t, more...); got != plain {
				t.Errorf("whiteroom build %s is the release %s, want %s, that of no URL", strings.Join(more, " "), got, plain)
			}
		}
		if got := release(t, "-config", item(staging), "-url", ""); got != changed {
			t.Errorf("-url of no text over the item url of %s is the release %s, want %s", staging, got, changed)
		}
	})

	t.Run("the settings are as they were", func(t *testing.T) {
		dir := copySample(t)
		before := manifestOf(t, dir)
		code, _, stderr := runCmd(t, "build", "-url", staging, "-out", t.TempDir(), "-cache", t.TempDir(), dir)
		if code != 0 {
			t.Fatalf("whiteroom build -url %s = %d, with stderr:\n%s", staging, code, stderr)
		}
		if after := manifestOf(t, dir); after != before {
			t.Errorf("the files of the site directory are changed by the build:\n%s\nwere:\n%s", after, before)
		}
		settings, err := os.ReadFile(filepath.Join(dir, "site", "settings.json"))
		if err != nil || !strings.Contains(string(settings), `"url": "https://example.com"`) || strings.Contains(string(settings), "staging") {
			t.Errorf("site/settings.json after the build is (%v):\n%s\nwant the URL of the settings", err, settings)
		}
	})

	t.Run("a URL that does not pass", func(t *testing.T) {
		for _, tt := range badSiteURLs {
			for _, fromItem := range []bool{false, true} {
				dir := copySample(t)
				before := manifestOf(t, dir)
				what, args := "-url", []string{"-url", tt.value}
				if fromItem {
					what, args = "url of the config file", []string{"-config", item(tt.value)}
				}
				want := "whiteroom build: " + what + " is not a valid site URL: " + tt.reason + "\n"
				code, stdout, stderr := runCmd(t, append(append([]string{"build"}, args...), dir)...)
				if code != 2 || stdout != "" || stderr != want {
					t.Errorf("%s of %q: whiteroom build = %d, with stdout %q and stderr:\n%s\nwant 2, with no stdout and:\n%s", what,
						tt.value, code, stdout, stderr, want)
				}
				if strings.Contains(stderr, tt.value) || strings.Contains(stderr, "secret") || strings.Contains(stderr, "Usage:") {
					t.Errorf("%s of %q: whiteroom build printed %q, which has the value or the usage", what, tt.value, stderr)
				}
				for _, name := range []string{"out", "cache"} {
					checkMissing(t, what+" of "+tt.value, filepath.Join(dir, name))
				}
				if after := manifestOf(t, dir); after != before {
					t.Errorf("%s of %q: the site directory is changed:\n%s\nwas:\n%s", what, tt.value, after, before)
				}
			}
		}
	})
}

// TestConfigURLOfOtherCommands checks that the commands which do not take the URL of the site take the item url of a config
// file as a string and no more: a value that does not pass the rule of the url of the site settings is no problem for them,
// and one that is not a string is.
func TestConfigURLOfOtherCommands(t *testing.T) {
	root := storedRoot(t)
	conf := filepath.Join(root, "conf")
	for _, value := range []string{"\"https://Example.com/\"", "\"ftp://x\""} {
		file := writeConfig(t, conf, "bad-url.yaml", "data: ../data\nurl: "+value+"\n")
		dest := filepath.Join(t.TempDir(), "copy")
		if code, _, stderr := runCmd(t, "backup", "-config", file, dest); code != 0 || stderr != "" {
			t.Errorf("whiteroom backup -config with url: %s = %d, with stderr:\n%s\nwant 0, and none", value, code, stderr)
		}
		code, stdout, stderr := runCmd(t, "doctor", "-config", file)
		checkClean(t, "doctor without SITE, url: "+value, filepath.Join(root, "data"), code, stdout, stderr, true)
		code, _, stderr = runCmdIn(t, strings.NewReader(changedPassword+"\n"), "account", "reset", "-config", file, initLogin)
		if code != 0 || stderr != "" {
			t.Errorf("whiteroom account reset -config with url: %s = %d, with stderr:\n%s\nwant 0, and none", value, code, stderr)
		}
	}

	file := writeConfig(t, conf, "number-url.yaml", "data: ../data\nurl: 123\n")
	want := file + ":2:1: /url: must be a string\n"
	for _, args := range [][]string{
		{"backup", "-config", file, filepath.Join(t.TempDir(), "copy")}, {"doctor", "-config", file},
		{"account", "reset", "-config", file, initLogin}, {"account", "delete", "-config", file, "usr_x"},
	} {
		if code, stdout, stderr := runCmd(t, args...); code != 2 || stdout != "" || stderr != want {
			t.Errorf("whiteroom %s = %d, with stdout %q and stderr:\n%s\nwant 2, with no stdout and:\n%s", strings.Join(args[:2], " "), code,
				stdout, stderr, want)
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
