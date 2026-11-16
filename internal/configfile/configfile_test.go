//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package configfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// itemNames are the items of the config file: every one has a field of File, and is not "unknown item".
var itemNames = []string{
	"data", "site", "theme", "out", "cache", "tmp", "db", "socket", "addr", "origin", "url", "trustedProxies", "workers", "memory",
	"writerUser", "writerGroup", "readerGroup", "maxReaders", "maxStaged", "commitRate", "minFree", "snapshotIdle", "stall",
}

// writeFile writes text as the file name in dir, which it makes, and returns the path of the file.
func writeFile(t *testing.T, dir, name, text string) string {
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

// sameFile reports whether a and b give the same values, whatever items they say they have, a list of trusted proxies
// being the same as one with the same items.
func sameFile(a, b File) bool {
	same := slices.Equal(a.TrustedProxies, b.TrustedProxies)
	a.TrustedProxies, b.TrustedProxies = nil, nil
	a.items, b.items = nil, nil
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

// sizedFile returns a config file of n bytes that gives the item data, with a comment for the rest.
func sizedFile(n int) string {
	head := "data: a\n# "
	return head + strings.Repeat("x", n-len(head)-1) + "\n"
}

func TestFileItems(t *testing.T) {
	typ := reflect.TypeFor[File]()
	fields := 0
	for _, f := range reflect.VisibleFields(typ) {
		if f.IsExported() {
			fields++
		}
	}
	if fields != len(itemNames) {
		t.Errorf("File has %d exported fields, and there are %d items", fields, len(itemNames))
	}
}

// TestLoad checks that every item is read, with the paths from the directory of the file, unless they are absolute, and
// that nothing is read for no file.
func TestLoad(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "conf")
	abs := filepath.Join(root, "abs")
	all := fmt.Sprintf(`# the settings of the staging server
data: data
site: ../site
theme: %s
out: out/releases
cache: ./cache
tmp: spool/tmp
db: run
addr: 127.0.0.1:8081
socket: %s
origin: https://admin.example.com
url: https://staging.example.com
trustedProxies:
  - 10.0.0.0/8
  - ::1
workers: 4
memory: 512
writerUser: whiteroom
writerGroup: 1001
readerGroup: "www-data"
maxReaders: 16
maxStaged: 1048576
commitRate: 100
minFree: 2097152
snapshotIdle: 30s
stall: 1m30s
`, strconv.Quote(abs), strconv.Quote(filepath.Join(abs, "admin.sock")))
	in := func(name string) string { return filepath.Join(dir, name) }
	for _, tt := range []struct {
		what string
		text string
		want File
	}{
		{
			"every item", all,
			File{
				Data: in("data"), Site: filepath.Join(root, "site"), Theme: abs, Out: in("out/releases"), Cache: in("cache"),
				Tmp: in("spool/tmp"), DB: in("run"), Addr: "127.0.0.1:8081", Socket: filepath.Join(abs, "admin.sock"),
				Origin: "https://admin.example.com", URL: "https://staging.example.com",
				TrustedProxies: []string{"10.0.0.0/8", "::1"}, Workers: 4, Memory: 512,
				WriterUser: "whiteroom", WriterGroup: "1001", ReaderGroup: "www-data", MaxReaders: 16, MaxStaged: 1048576,
				CommitRate: 100, MinFree: 2097152, SnapshotIdle: 30 * time.Second, Stall: 90 * time.Second,
			},
		},
		{"a url as written", "url: \"HTTPS://Example.com/\"\n", File{URL: "HTTPS://Example.com/"}},
		{"one item", "out: o\n", File{Out: in("o")}},
		{"db, from the directory of the file", "db: d\n", File{DB: in("d")}},
		{"db, an absolute path", "db: " + strconv.Quote(abs) + "\n", File{DB: abs}},
		{"tmp, from the directory of the file", "tmp: t\n", File{Tmp: in("t")}},
		{"tmp, an absolute path", "tmp: " + strconv.Quote(abs) + "\n", File{Tmp: abs}},
		{"an address with brackets", "addr: \"[::1]:8081\"\n", File{Addr: "[::1]:8081"}},
		{"numbers of 0", "workers: 0\nmemory: 0\ncommitRate: 0\nminFree: 0\n", File{}},
		{"the largest memory", fmt.Sprintf("memory: %d\n", MaxMemory), File{Memory: MaxMemory}},
		{"no proxies", "trustedProxies: []\n", File{}},
		{"an empty mapping", "{}\n", File{}},
		{"the largest size", sizedFile(MaxSize), File{Data: in("a")}},
		{"a user by name", "writerUser: www\n", File{WriterUser: "www"}},
		{"a user by a quoted number", "writerUser: \"1000\"\n", File{WriterUser: "1000"}},
		{"a user by a number", "writerUser: 1000\n", File{WriterUser: "1000"}},
		{"a user by the number 0", "writerUser: 0\n", File{WriterUser: "0"}},
		{"a group by name", "writerGroup: www\nreaderGroup: web\n", File{WriterGroup: "www", ReaderGroup: "web"}},
		{"a group by number", "writerGroup: 33\nreaderGroup: \"34\"\n", File{WriterGroup: "33", ReaderGroup: "34"}},
		{"the smallest limits", "maxReaders: 1\nmaxStaged: 1\n", File{MaxReaders: 1, MaxStaged: 1}},
		{"the largest limits", "maxStaged: 9223372036854775807\nminFree: 9223372036854775807\n",
			File{MaxStaged: 1<<63 - 1, MinFree: 1<<63 - 1}},
		{"durations of 0", "snapshotIdle: \"0s\"\nstall: \"0\"\n", File{}},
		{"durations", "snapshotIdle: 30s\nstall: 5m\n", File{SnapshotIdle: 30 * time.Second, Stall: 5 * time.Minute}},
		{"a duration of several units", "stall: \"1h2m3.5s\"\n", File{Stall: time.Hour + 2*time.Minute + 3500*time.Millisecond}},
	} {
		file := writeFile(t, dir, "whiteroom.yaml", tt.text)
		got, err := Load(file)
		if err != nil || !sameFile(got, tt.want) {
			t.Errorf("%s: Load = %+v, %v\nwant %+v", tt.what, got, err, tt.want)
		}
	}

	t.Run("a relative path of the file", func(t *testing.T) {
		writeFile(t, filepath.Join(root, "rel", "conf"), "whiteroom.yaml", "data: data\nsite: ../site\ntheme: /abs\ndb: run\n")
		t.Chdir(filepath.Join(root, "rel"))
		got, err := Load(filepath.Join("conf", "whiteroom.yaml"))
		want := File{Data: filepath.Join("conf", "data"), Site: "site", Theme: "/abs", DB: filepath.Join("conf", "run")}
		if err != nil || !sameFile(got, want) {
			t.Errorf("Load = %+v, %v\nwant %+v", got, err, want)
		}
		writeFile(t, filepath.Join(root, "rel", "conf"), "whiteroom.yaml", "data:\t/a\n")
		_, err = Load(filepath.Join("conf", "whiteroom.yaml"))
		if want := filepath.Join("conf", "whiteroom.yaml") + ":1:6: tab characters are not allowed; use spaces"; err == nil ||
			err.Error() != want {
			t.Errorf("Load of a broken file = %v, want %s", err, want)
		}
	})

	// A socket that starts with @ is kept, for unixsock.Listen to refuse, wherever the file is; and ./ in front of a name that
	// starts with @ stays, in a file in the working directory, as Join would cut it.
	t.Run("a socket that starts with @", func(t *testing.T) {
		at := filepath.Join(root, "at")
		if err := os.MkdirAll(at, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Chdir(at)
		sep := string(filepath.Separator)
		for _, tt := range []struct{ what, file, text, want string }{
			{"./@ of a file in the working directory", "whiteroom.yaml", "socket: ./@admin.sock\n", "." + sep + "@admin.sock"},
			{"@ of a file in the working directory", "whiteroom.yaml", "socket: \"@admin.sock\"\n", "@admin.sock"},
			{"@ of a file in another directory", "conf/whiteroom.yaml", "socket: \"@admin.sock\"\n", "@admin.sock"},
			{"@ of a file with an absolute path", filepath.Join(at, "abs.yaml"), "socket: \"@admin.sock\"\n", "@admin.sock"},
			{"./@ of a file in another directory", "conf/whiteroom.yaml", "socket: ./@admin.sock\n", "conf" + sep + "@admin.sock"},
			{"a name of a file in the working directory", "whiteroom.yaml", "socket: admin.sock\n", "admin.sock"},
			{"a name of a file in another directory", "conf/whiteroom.yaml", "socket: run/admin.sock\n", "conf" + sep + "run" + sep + "admin.sock"},
			{"an absolute path", "whiteroom.yaml", "socket: /run/admin.sock\n", "/run/admin.sock"},
		} {
			file := tt.file
			if !filepath.IsAbs(file) {
				file = filepath.FromSlash(file)
			}
			writeFile(t, filepath.Dir(file), filepath.Base(file), tt.text)
			got, err := Load(file)
			if err != nil || got.Socket != tt.want {
				t.Errorf("%s: Load = %+v, %v\nwant the socket %q", tt.what, got, err, tt.want)
			}
		}
	})

	// The paths are from the directory that the kernel takes the directory of the file for: link/.. is the parent of what
	// link points to, not the working directory.
	t.Run("a file through a symbolic link", func(t *testing.T) {
		site := filepath.Join(root, "linked", "site")
		writeFile(t, site, "whiteroom.yaml", "site: .\nout: out\ndb: run\n")
		work := filepath.Join(root, "linked", "work")
		for _, d := range []string{filepath.Join(site, "entries"), work} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(filepath.Join(site, "entries"), filepath.Join(work, "link")); err != nil {
			t.Fatal(err)
		}
		t.Chdir(work)
		got, err := Load(filepath.FromSlash("link/../whiteroom.yaml"))
		want := File{Site: filepath.FromSlash("link/.."), Out: filepath.FromSlash("link/../out"), DB: filepath.FromSlash("link/../run")}
		if err != nil || !sameFile(got, want) {
			t.Errorf("Load = %+v, %v\nwant %+v", got, err, want)
		}
		if resolved, err := filepath.EvalSymlinks(got.Site); err != nil || resolved != site {
			t.Errorf("the site %s is %s, %v; want %s", got.Site, resolved, err, site)
		}
	})

	t.Run("no file", func(t *testing.T) {
		writeFile(t, filepath.Join(root, "none"), "whiteroom.yaml", "data:\t/a\n")
		writeFile(t, filepath.Join(root, "none"), "config.yaml", "data:\t/a\n")
		t.Chdir(filepath.Join(root, "none"))
		if got, err := Load(""); err != nil || !reflect.DeepEqual(got, File{}) {
			t.Errorf("Load(\"\") = %+v, %v, want the zero File", got, err)
		}
	})
}

// TestLoadAll checks that one file may give every item of both executables, and that Has says so for each of them.
func TestLoadAll(t *testing.T) {
	values := map[string]string{
		"data": "d", "site": "s", "theme": "t", "out": "o", "cache": "c", "tmp": "m", "db": "b", "socket": "k", "addr": "127.0.0.1:1",
		"origin": "http://o", "url": "https://example.com", "trustedProxies": "[10.0.0.1]", "workers": "1", "memory": "1",
		"writerUser": "u", "writerGroup": "g", "readerGroup": "r", "maxReaders": "1", "maxStaged": "1", "commitRate": "1",
		"minFree": "1", "snapshotIdle": "1s", "stall": "1s",
	}
	var b strings.Builder
	for _, item := range itemNames {
		v, ok := values[item]
		if !ok {
			t.Fatalf("the test has no value for the item %s", item)
		}
		b.WriteString(item + ": " + v + "\n")
	}
	file := writeFile(t, t.TempDir(), "whiteroom.yaml", b.String())
	f, err := Load(file)
	if err != nil {
		t.Fatalf("Load of a file with every item = %v", err)
	}
	for _, item := range itemNames {
		if !f.Has(item) {
			t.Errorf("Has(%q) = false for a file that gives it", item)
		}
		other := writeFile(t, t.TempDir(), "whiteroom.yaml", item+": "+values[item]+"\n")
		g, err := Load(other)
		if err != nil {
			t.Errorf("Load of a file with the item %s = %v", item, err)
			continue
		}
		for _, name := range itemNames {
			if g.Has(name) != (name == item) {
				t.Errorf("Has(%q) = %t for a file that gives only %s", name, g.Has(name), item)
			}
		}
	}
}

// TestHas checks that an item is there with a value of zero, and that nothing is there for no file.
func TestHas(t *testing.T) {
	file := writeFile(t, t.TempDir(), "whiteroom.yaml",
		"commitRate: 0\nworkers: 0\nmemory: 0\nminFree: 0\ntrustedProxies: []\nsnapshotIdle: \"0s\"\nwriterUser: 0\n")
	f, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	there := []string{"commitRate", "workers", "memory", "minFree", "trustedProxies", "snapshotIdle", "writerUser"}
	for _, item := range itemNames {
		if got, want := f.Has(item), slices.Contains(there, item); got != want {
			t.Errorf("Has(%q) = %t, want %t", item, got, want)
		}
	}
	for _, item := range []string{"", "nothing", "CommitRate", "commitrate", "/commitRate"} {
		if f.Has(item) {
			t.Errorf("Has(%q) = true, which is no item", item)
		}
	}

	for _, text := range []string{"{}\n", "commitRate: 1\n"} {
		g, err := Load(writeFile(t, t.TempDir(), "whiteroom.yaml", text))
		if err != nil {
			t.Fatal(err)
		}
		if g.Has("stall") || g.Has("maxReaders") {
			t.Errorf("Has is true for an item that %q does not give", text)
		}
	}
	none, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range itemNames {
		if none.Has(item) {
			t.Errorf("Has(%q) = true for no file", item)
		}
	}
	var zero File
	if zero.Has("commitRate") {
		t.Error("the zero File has an item")
	}
}

// TestLoadProblems checks that each kind of problem is reported, a line for each, in the order of their positions in the
// file, and without the values that the file gives.
func TestLoadProblems(t *testing.T) {
	dir := t.TempDir()
	const (
		notMapping  = "must be a mapping of items"
		notString   = "must be a string"
		notWorkers  = "must be an integer of 0 or more"
		notMemory   = "must be an integer from 0 to 8796093022207"
		notList     = "must be a list of strings"
		empty       = "must not be empty"
		notAccount  = "must be a name, or a number of 0 or more"
		notPositive = "must be an integer of 1 or more"
		notDuration = "must be a duration of 0 or more, such as 30s or 5m"
	)
	for _, tt := range []struct {
		what string
		text string
		want []string // the lines after the file
		vals []string // what the file gives, which no line has
	}{
		{"an unknown item", "unknown: 1\n", []string{"1:1: /unknown: unknown item"}, nil},
		{"an unknown item with a slash", "a: 1\n\"b/c~d\": 1\n", []string{"1:1: /a: unknown item", "2:1: /b~1c~0d: unknown item"}, nil},
		{"an item in another case", "commitrate: 1\nWriterUser: x\n", []string{"1:1: /commitrate: unknown item", "2:1: /WriterUser: unknown item"}, nil},
		{"a number for a path", "data: 123\n", []string{"1:1: /data: " + notString}, []string{"123"}},
		{"a number for db", "db: 123\n", []string{"1:1: /db: " + notString}, []string{"123"}},
		{"a list for db", "db: [secret]\n", []string{"1:1: /db: " + notString}, []string{"secret"}},
		{"an empty db", "db: \"\"\n", []string{"1:1: /db: " + empty}, nil},
		{"a number for tmp", "tmp: 123\n", []string{"1:1: /tmp: " + notString}, []string{"123"}},
		{"a list for tmp", "tmp: [secret]\n", []string{"1:1: /tmp: " + notString}, []string{"secret"}},
		{"an empty tmp", "tmp: \"\"\n", []string{"1:1: /tmp: " + empty}, nil},
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
		{"a memory over the largest", fmt.Sprintf("memory: %d\n", MaxMemory+1), []string{"1:1: /memory: " + notMemory},
			[]string{strconv.FormatInt(MaxMemory+1, 10)}},
		{"a workers over the integers", "workers: 99999999999999999999\n", []string{"1:1: /workers: " + notWorkers},
			[]string{"99999999999999999999"}},
		{"an empty path", "data: \"\"\n", []string{"1:1: /data: " + empty}, nil},
		{"an empty address", "addr: \"\"\n", []string{"1:1: /addr: " + empty}, nil},
		{"an empty url", "url: \"\"\n", []string{"1:1: /url: " + empty}, nil},
		{"an empty proxy", "trustedProxies: [\"\"]\n", []string{"1:18: /trustedProxies/0: " + empty}, nil},

		{"a negative writerUser", "writerUser: -1\n", []string{"1:1: /writerUser: " + notAccount}, []string{"-1"}},
		{"a fraction for writerGroup", "writerGroup: 1.5\n", []string{"1:1: /writerGroup: " + notAccount}, []string{"1.5"}},
		{"true for readerGroup", "readerGroup: true\n", []string{"1:1: /readerGroup: " + notAccount}, nil},
		{"a list for writerUser", "writerUser: [secret]\n", []string{"1:1: /writerUser: " + notAccount}, []string{"secret"}},
		{"an empty writerUser", "writerUser: \"\"\n", []string{"1:1: /writerUser: " + notAccount}, nil},
		{"an empty readerGroup", "readerGroup: \"\"\n", []string{"1:1: /readerGroup: " + notAccount}, nil},
		{"a writerGroup over the integers", "writerGroup: 99999999999999999999\n", []string{"1:1: /writerGroup: " + notAccount},
			[]string{"99999999999999999999"}},
		{"an exponent for readerGroup", "readerGroup: 1e3\n", []string{"1:1: /readerGroup: " + notAccount}, []string{"1e3"}},
		{"a mapping for writerUser", "writerUser:\n  name: secret\n", []string{"1:1: /writerUser: " + notAccount}, []string{"secret"}},

		{"a maxReaders of 0", "maxReaders: 0\n", []string{"1:1: /maxReaders: " + notPositive}, nil},
		{"a negative maxReaders", "maxReaders: -1\n", []string{"1:1: /maxReaders: " + notPositive}, []string{"-1"}},
		{"a text for maxReaders", "maxReaders: \"4\"\n", []string{"1:1: /maxReaders: " + notPositive}, []string{"4"}},
		{"a fraction for maxReaders", "maxReaders: 1.5\n", []string{"1:1: /maxReaders: " + notPositive}, []string{"1.5"}},
		{"a maxReaders over the integers", "maxReaders: 99999999999999999999\n", []string{"1:1: /maxReaders: " + notPositive},
			[]string{"99999999999999999999"}},
		{"a maxStaged of 0", "maxStaged: 0\n", []string{"1:1: /maxStaged: " + notPositive}, nil},
		{"a negative maxStaged", "maxStaged: -5\n", []string{"1:1: /maxStaged: " + notPositive}, []string{"-5"}},
		{"true for maxStaged", "maxStaged: true\n", []string{"1:1: /maxStaged: " + notPositive}, nil},
		{"a maxStaged over int64", "maxStaged: 9223372036854775808\n", []string{"1:1: /maxStaged: " + notPositive},
			[]string{"9223372036854775808"}},
		{"a negative commitRate", "commitRate: -1\n", []string{"1:1: /commitRate: " + notWorkers}, []string{"-1"}},
		{"a text for commitRate", "commitRate: \"5\"\n", []string{"1:1: /commitRate: " + notWorkers}, []string{"5"}},
		{"a fraction for commitRate", "commitRate: 0.5\n", []string{"1:1: /commitRate: " + notWorkers}, []string{"0.5"}},
		{"a negative minFree", "minFree: -1\n", []string{"1:1: /minFree: " + notWorkers}, []string{"-1"}},
		{"a text for minFree", "minFree: \"1GiB\"\n", []string{"1:1: /minFree: " + notWorkers}, []string{"1GiB"}},
		{"a minFree over int64", "minFree: 9223372036854775808\n", []string{"1:1: /minFree: " + notWorkers},
			[]string{"9223372036854775808"}},

		{"a negative snapshotIdle", "snapshotIdle: \"-1s\"\n", []string{"1:1: /snapshotIdle: " + notDuration}, []string{"-1s"}},
		{"a text for snapshotIdle", "snapshotIdle: soon\n", []string{"1:1: /snapshotIdle: " + notDuration}, []string{"soon"}},
		{"a number for stall", "stall: 31\n", []string{"1:1: /stall: " + notDuration}, []string{"31"}},
		{"a number of 0 for stall", "stall: 0\n", []string{"1:1: /stall: " + notDuration}, nil},
		{"a duration without a unit", "stall: \"31\"\n", []string{"1:1: /stall: " + notDuration}, []string{"31"}},
		{"an empty stall", "stall: \"\"\n", []string{"1:1: /stall: " + notDuration}, nil},
		{"a list for stall", "stall: [secret]\n", []string{"1:1: /stall: " + notDuration}, []string{"secret"}},
		{"true for snapshotIdle", "snapshotIdle: true\n", []string{"1:1: /snapshotIdle: " + notDuration}, nil},
		{"a negative stall", "stall: -1m\n", []string{"1:1: /stall: " + notDuration}, []string{"-1m"}},

		{
			"three problems", "workers: \"4\"\nmemory: 1.5\ndata: 123\n",
			[]string{"1:1: /workers: " + notWorkers, "2:1: /memory: " + notMemory, "3:1: /data: " + notString},
			[]string{"4", "1.5", "123"},
		},
		{
			"problems of the items of the daemon in the order of the file", "stall: soon\nmaxReaders: 0\nwriterUser: -1\ndb: 5\nsnapshotIdle: x\n",
			[]string{
				"1:1: /stall: " + notDuration, "2:1: /maxReaders: " + notPositive, "3:1: /writerUser: " + notAccount,
				"4:1: /db: " + notString, "5:1: /snapshotIdle: " + notDuration,
			},
			[]string{"soon"},
		},
		{
			"problems in a list and after it", "trustedProxies:\n  - \"\"\n  - 5\nworkers: -1\nsocket: 4\n",
			[]string{
				"2:5: /trustedProxies/0: " + empty, "3:5: /trustedProxies/1: " + notString, "4:1: /workers: " + notWorkers,
				"5:1: /socket: " + notString,
			},
			[]string{"-1"},
		},
		{"problems in one line of a flow list", "trustedProxies: [5, \"\", a, 7]\n",
			[]string{"1:18: /trustedProxies/0: " + notString, "1:21: /trustedProxies/1: " + empty, "1:28: /trustedProxies/3: " + notString},
			[]string{"5", "7"}},
		{"problems in one line of a flow mapping, in the order of the columns", "{workers: \"4\", data: 5}\n",
			[]string{"1:2: /workers: " + notWorkers, "1:16: /data: " + notString}, []string{"4", "5"}},
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
		file := writeFile(t, dir, "whiteroom.yaml", tt.text)
		got, err := Load(file)
		var ps Problems
		if !errors.As(err, &ps) {
			t.Errorf("%s: Load = %+v, %v, want problems", tt.what, got, err)
			continue
		}
		var lines []string
		for _, p := range ps {
			lines = append(lines, p.Error())
			if p.File != file {
				t.Errorf("%s: the problem %q is in the file %q, want %q", tt.what, p.Error(), p.File, file)
			}
			for _, v := range tt.vals {
				if strings.Contains(p.Msg+p.Path, v) {
					t.Errorf("%s: the problem %q has the value %q that the file gives", tt.what, p.Error(), v)
				}
			}
		}
		if want := linesOf(file, tt.want); strings.Join(lines, "\n")+"\n" != want || !reflect.DeepEqual(got, File{}) {
			t.Errorf("%s: Load = %+v, with the problems:\n%s\nwant:\n%s", tt.what, got, strings.Join(lines, "\n"), want)
		}
		if want := strings.TrimSuffix(linesOf(file, tt.want), "\n"); err.Error() != want {
			t.Errorf("%s: the error is %q, want %q", tt.what, err.Error(), want)
		}
	}
}

// TestLoadErrors checks the errors of a file that cannot be read, which are not Problems, and the largest file.
func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.yaml")
	if _, err := Load(missing); !errors.Is(err, fs.ErrNotExist) || errors.As(err, new(Problems)) {
		t.Errorf("Load of a missing file = %v, want an error of fs.ErrNotExist", err)
	}
	if _, err := Load(dir); err == nil || errors.As(err, new(Problems)) {
		t.Errorf("Load of a directory = %v, want an error that is not about the content", err)
	}
	large := writeFile(t, dir, "large.yaml", sizedFile(MaxSize+1))
	got, err := Load(large)
	if want := large + " is larger than 1048576 bytes"; err == nil || err.Error() != want || errors.As(err, new(Problems)) ||
		!reflect.DeepEqual(got, File{}) {
		t.Errorf("Load of a file of %d bytes = %+v, %v, want %s", MaxSize+1, got, err, want)
	}
	if MaxSize != 1<<20 || MaxMemory != 1<<43-1 {
		t.Errorf("MaxSize = %d and MaxMemory = %d, want 1 MiB and 8796093022207", MaxSize, MaxMemory)
	}
}

// TestProblemError checks the line of a problem for each combination of the fields that it has, and that of a list.
func TestProblemError(t *testing.T) {
	for _, tt := range []struct {
		p    Problem
		want string
	}{
		{Problem{File: "a.yaml", Line: 2, Column: 3, Path: "/data", Msg: "must be a string"}, "a.yaml:2:3: /data: must be a string"},
		{Problem{File: "a.yaml", Line: 2, Path: "/data", Msg: "m"}, "a.yaml:2: /data: m"},
		{Problem{File: "a.yaml", Path: "/data", Msg: "m"}, "a.yaml: /data: m"},
		{Problem{File: "a.yaml", Column: 3, Msg: "m"}, "a.yaml: m"},
		{Problem{File: "a.yaml", Msg: "m"}, "a.yaml: m"},
		{Problem{Line: 2, Column: 3, Msg: "m"}, "2:3: m"},
		{Problem{Line: 2, Msg: "m"}, "2: m"},
		{Problem{Path: "/data", Msg: "m"}, "/data: m"},
		{Problem{Msg: "m"}, "m"},
	} {
		if got := tt.p.Error(); got != tt.want {
			t.Errorf("%+v: Error() = %q, want %q", tt.p, got, tt.want)
		}
	}
	ps := Problems{{File: "a.yaml", Line: 2, Column: 3, Path: "/data", Msg: "x"}, {File: "a.yaml", Line: 4, Column: 1, Msg: "y"}}
	if got, want := ps.Error(), "a.yaml:2:3: /data: x\na.yaml:4:1: y"; got != want {
		t.Errorf("Problems.Error() = %q, want %q", got, want)
	}
	if got := (Problems{}).Error(); got != "" {
		t.Errorf("Problems{}.Error() = %q, want it empty", got)
	}
}
