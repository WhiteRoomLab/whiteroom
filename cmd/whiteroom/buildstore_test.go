//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package main

import (
	"cmp"
	"context"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// schemaOnly removes from site, a copy of the sample site, all but what a build from a data directory reads of it: the
// schema file, and the theme if keepTheme is true.
func schemaOnly(t *testing.T, site string, keepTheme bool) {
	t.Helper()
	entries, err := os.ReadDir(site)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "site" || e.Name() == "theme" && keepTheme {
			continue
		}
		if err := os.RemoveAll(filepath.Join(site, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
	inner, err := os.ReadDir(filepath.Join(site, "site"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range inner {
		if e.Name() != "schema.yaml" {
			if err := os.RemoveAll(filepath.Join(site, "site", e.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// storedWarnings returns the warnings of a build of the sample site from a data directory, with the site directory site
// and the argument theme of -theme, "" for site/theme: the documents have the names that they have in the data directory,
// and the theme the path from the working directory.
func storedWarnings(t *testing.T, site, theme string) string {
	t.Helper()
	var b strings.Builder
	for line := range strings.Lines(readGolden(t, "warnings.txt")) {
		file, rest, _ := strings.Cut(line, ":")
		if file == "theme" {
			file = cmp.Or(theme, filepath.Join(site, "theme"))
		}
		b.WriteString(file + ":" + rest)
	}
	return b.String()
}

// buildStored runs whiteroom build of the data directory, the output directory, the cache directory and the site directory of
// s, with the flags more before SITE.
func buildStored(t *testing.T, s adminSite, more ...string) (code int, stdout, stderr string) {
	t.Helper()
	args := append([]string{"build", "-data", s.data, "-out", s.out, "-cache", s.cache}, more...)
	return runCmd(t, append(args, s.site)...)
}

// storedRoot makes a directory with the data directory data/ that whiteroom init has made of the sample site, the site
// directory site/ that has only the schema file, the example theme in theme/, and an empty conf/.
func storedRoot(t *testing.T) string {
	t.Helper()
	root := sampleRoot(t)
	site := filepath.Join(root, "site")
	code, _, stderr := runInitCmd(t, initPassword+"\n",
		adminFlags("-data", filepath.Join(root, "data"), "-theme", filepath.Join(root, "theme"), site)...)
	if code != 0 {
		t.Fatalf("whiteroom init of the sample site = %d, with stderr:\n%s", code, stderr)
	}
	schemaOnly(t, site, false)
	return root
}

// TestBuildData builds the sample site from the data directory that whiteroom init has made of it, from a site directory that
// has only its schema file and its theme: the release has the name of the release that the build from the directory makes,
// the output is that of the build from the directory, with the names of the documents of the data directory in the warnings,
// and the second build finds the release published. With -theme the theme of SITE is not read. The files and the heads of
// the audit logs of the data directory are as they were.
func TestBuildData(t *testing.T) {
	ref := t.TempDir()
	buildSample(t, ref, t.TempDir())
	release := currentOf(t, ref)

	s := initSample(t)
	schemaOnly(t, s.site, true)
	files, heads := manifestOf(t, s.data), storedHeads(t, s.data)
	warnings := storedWarnings(t, s.site, "")
	for _, published := range []bool{true, false} {
		code, stdout, stderr := buildStored(t, s)
		if code != 0 {
			t.Fatalf("whiteroom build -data = %d, with stderr:\n%s", code, stderr)
		}
		if want := sampleSummary(t, s.out, published); stdout != want {
			t.Errorf("whiteroom build -data printed %q, want %q", stdout, want)
		}
		if got := currentOf(t, s.out); got != release {
			t.Errorf("whiteroom build -data published the release %s, the build of the directory %s", got, release)
		}
		if stderr != warnings {
			t.Errorf("whiteroom build -data printed the warnings:\n%s\nwant:\n%s", stderr, warnings)
		}
	}

	if err := os.RemoveAll(filepath.Join(s.site, "theme")); err != nil {
		t.Fatal(err)
	}
	out, cache := t.TempDir(), t.TempDir()
	code, stdout, stderr := runCmd(t, "build", "-data", s.data, "-theme", exampleTheme, "-out", out, "-cache", cache, s.site)
	if code != 0 {
		t.Fatalf("whiteroom build -data -theme = %d, with stderr:\n%s", code, stderr)
	}
	if want := sampleSummary(t, out, true); stdout != want {
		t.Errorf("whiteroom build -data -theme printed %q, want %q", stdout, want)
	}
	if got := currentOf(t, out); got != release {
		t.Errorf("whiteroom build -data -theme published the release %s, the build of the directory %s", got, release)
	}
	if want := storedWarnings(t, s.site, exampleTheme); stderr != want {
		t.Errorf("whiteroom build -data -theme printed the warnings:\n%s\nwant:\n%s", stderr, want)
	}

	if got := manifestOf(t, s.data); got != files {
		t.Errorf("the files of the data directory are changed by the builds:\n%s\nwere:\n%s", got, files)
	}
	if got := storedHeads(t, s.data); !maps.Equal(got, heads) {
		t.Errorf("the heads of the audit logs are changed by the builds: %v, were %v", got, heads)
	}
}

// TestBuildDataDefaults builds from a data directory with only SITE: the output and the cache directories are those of SITE,
// and nothing is made in the data directory.
func TestBuildDataDefaults(t *testing.T) {
	s := initSample(t)
	schemaOnly(t, s.site, true)
	before := manifestOf(t, s.data)
	code, stdout, stderr := runCmd(t, "build", "-data", s.data, s.site)
	if code != 0 {
		t.Fatalf("whiteroom build -data %s = %d, with stderr:\n%s", s.site, code, stderr)
	}
	out := filepath.Join(s.site, "out")
	if want := sampleSummary(t, out, true); stdout != want {
		t.Errorf("whiteroom build -data printed %q, want %q", stdout, want)
	}
	if want := storedWarnings(t, s.site, ""); stderr != want {
		t.Errorf("whiteroom build -data printed the warnings:\n%s\nwant:\n%s", stderr, want)
	}
	if _, err := os.Stat(filepath.Join(s.site, "cache", "CACHEDIR.TAG")); err != nil {
		t.Errorf("the cache directory of SITE is not made: %v", err)
	}
	for _, name := range []string{"out", "cache"} {
		checkMissing(t, "the defaults", filepath.Join(s.data, name))
	}
	if got := manifestOf(t, s.data); got != before {
		t.Errorf("the files of the data directory are changed by the build:\n%s\nwere:\n%s", got, before)
	}
}

// TestBuildDataConfig builds from a data directory that a config file gives, with the paths of the file from its directory:
// the data directory, the site directory with only the schema file, the theme, the output and the cache directories, and
// that -data wins over the item data, and that no item data is a build from the site directory.
func TestBuildDataConfig(t *testing.T) {
	ref := t.TempDir()
	buildSample(t, ref, t.TempDir())
	release := currentOf(t, ref)
	const items = "site: ../site\ntheme: ../theme\nout: ../out\ncache: ../cache\n"

	t.Run("the items of the file", func(t *testing.T) {
		root := storedRoot(t)
		file := writeConfig(t, filepath.Join(root, "conf"), "whiteroom.yaml", "# the settings of every command\n"+
			"data: ../data\n"+items+"addr: 127.0.0.1:8081\nsocket: ../run/admin.sock\norigin: https://admin.example.com\n"+
			"trustedProxies:\n  - 10.0.0.0/8\n  - ::1\nworkers: 2\nmemory: 256\n")
		code, stdout, stderr := runCmd(t, "build", "-config", file)
		if code != 0 {
			t.Fatalf("whiteroom build -config = %d, with stderr:\n%s", code, stderr)
		}
		out := filepath.Join(root, "out")
		if want := sampleSummary(t, out, true); stdout != want {
			t.Errorf("whiteroom build -config printed %q, want %q", stdout, want)
		}
		if got := currentOf(t, out); got != release {
			t.Errorf("whiteroom build -config published the release %s, the build of the directory %s", got, release)
		}
		if want := storedWarnings(t, filepath.Join(root, "site"), filepath.Join(root, "theme")); stderr != want {
			t.Errorf("whiteroom build -config printed the warnings:\n%s\nwant:\n%s", stderr, want)
		}
		if _, err := os.Stat(filepath.Join(root, "cache", "CACHEDIR.TAG")); err != nil {
			t.Errorf("the cache directory of the file is not made: %v", err)
		}
		checkMissing(t, "the items of the file", filepath.Join(root, "run"))
	})

	t.Run("the flag wins over the item", func(t *testing.T) {
		root := storedRoot(t)
		conf := filepath.Join(root, "conf")
		missing := filepath.Join(root, "missing-data")
		bad := writeConfig(t, conf, "bad.yaml", "data: ../missing-data\n"+items)
		code, stdout, stderr := runCmd(t, "build", "-config", bad, "-data", filepath.Join(root, "data"))
		if code != 0 {
			t.Fatalf("whiteroom build -config -data = %d, with the config file of a missing data directory, and stderr:\n%s", code, stderr)
		}
		if got := currentOf(t, filepath.Join(root, "out")); got != release {
			t.Errorf("whiteroom build -config -data published the release %s, the build of the directory %s", got, release)
		}
		if want := sampleSummary(t, filepath.Join(root, "out"), true); stdout != want {
			t.Errorf("whiteroom build -config -data printed %q, want %q", stdout, want)
		}

		good := writeConfig(t, conf, "good.yaml", "data: ../data\nsite: ../site\ntheme: ../theme\nout: ../out2\ncache: ../cache2\n")
		failingBuild(t, "-data of a missing directory over the item", t.Context(), "whiteroom build: open "+missing+
			": no such file or directory\n", "-config", good, "-data", missing)
		checkMissing(t, "-data of a missing directory over the item", filepath.Join(root, "out2"))
	})

	t.Run("no item data", func(t *testing.T) {
		root := storedRoot(t)
		file := writeConfig(t, filepath.Join(root, "conf"), "whiteroom.yaml", items)
		failingBuild(t, "a site directory without documents", t.Context(),
			filepath.Join(root, "site", "site", "settings.json")+": missing file\n", "-config", file)
		checkMissing(t, "a site directory without documents", filepath.Join(root, "out"))
	})
}

// TestBuildDataBesideAdmin builds from the data directory that whiteroom serve admin has open: with the output and the
// cache directories of its own, the release is that of the server, also after the server has published another; and into the
// output directory of the server, while it does not build, it finds the same release published.
func TestBuildDataBesideAdmin(t *testing.T) {
	s := initSample(t)
	a := startAdmin(t, t.Context(), s.args()...)
	cookie := a.login(t)
	if built := a.waitBuilt(t, cookie); failedBuild(built) || built.Last.Release == "" {
		t.Fatalf("the start build has %+v, want a release\nstderr:\n%s", built.Last, a.stderr)
	}
	first := currentOf(t, s.out)

	outB, cacheB := t.TempDir(), t.TempDir()
	code, stdout, stderr := runCmd(t, "build", "-data", s.data, "-out", outB, "-cache", cacheB, s.site)
	if code != 0 {
		t.Fatalf("whiteroom build -data beside the server = %d, with stderr:\n%s", code, stderr)
	}
	if want := sampleSummary(t, outB, true); stdout != want {
		t.Errorf("whiteroom build -data beside the server printed %q, want %q", stdout, want)
	}
	if got := currentOf(t, outB); got != first {
		t.Errorf("the build beside the server published the release %s, the server %s", got, first)
	}

	code, stdout, stderr = runCmd(t, "build", "-data", s.data, "-out", s.out, "-cache", s.cache, s.site)
	if code != 0 {
		t.Fatalf("whiteroom build -data into the output directory of the server = %d, with stderr:\n%s", code, stderr)
	}
	if want := sampleSummary(t, s.out, false); stdout != want {
		t.Errorf("whiteroom build -data into the output directory of the server printed %q, want %q", stdout, want)
	}

	const slug = "beside-the-server"
	id := a.publishPage(t, cookie, "en", "Published beside the build", slug)
	if built := a.waitPublished(t, cookie, id); failedBuild(built) || built.Last.Release == "" {
		t.Fatalf("the build of the publishing has %+v, want a release\nstderr:\n%s", built.Last, a.stderr)
	}
	second := currentOf(t, s.out)
	if second == first {
		t.Fatalf("the server has published the release %s, as before the page", second)
	}
	code, stdout, stderr = runCmd(t, "build", "-data", s.data, "-out", outB, "-cache", cacheB, s.site)
	if code != 0 {
		t.Fatalf("whiteroom build -data after the page = %d, with stderr:\n%s", code, stderr)
	}
	if want := "published release " + second + " in " + outB + ": "; !strings.HasPrefix(stdout, want) {
		t.Errorf("whiteroom build -data after the page printed %q, want it to start with %q", stdout, want)
	}
	if got := currentOf(t, outB); got != second {
		t.Errorf("the build after the page published the release %s, the server %s", got, second)
	}
	if _, err := os.Stat(filepath.Join(outB, "current", "public", "en", slug, "index.html")); err != nil {
		t.Errorf("the page of the server is not in the build beside it: %v", err)
	}

	if code := a.stop(t); code != 0 {
		t.Errorf("whiteroom serve admin = %d after its context is done, want 0", code)
	}
}

// TestBuildDataErrors checks the builds from a data directory that fail, which print what is wrong on stderr and nothing on
// stdout, and leave the data directory as it is: a data directory without settings, which make no output and cache
// directory; one that is missing or empty; a SITE without the schema file or the theme; documents that the schema does not
// accept, which name their files in the data directory; and a schema that is not YAML.
func TestBuildDataErrors(t *testing.T) {
	t.Run("without settings", func(t *testing.T) {
		s := initEmpty(t)
		code, stdout, stderr := buildStored(t, s, "-theme", s.theme)
		if code != 1 || stdout != "" || !strings.HasPrefix(stderr, "whiteroom build: ") || strings.Count(stderr, "\n") != 1 ||
			!strings.Contains(stderr, "no site settings") || !strings.Contains(stderr, s.data) {
			t.Errorf("whiteroom build -data of a data directory without settings = %d, with stdout %q and stderr:\n%s\nwant 1, "+
				"and a line that tells that the data directory %s has no site settings", code, stdout, stderr, s.data)
		}
		for _, dir := range []string{s.out, s.cache} {
			checkMissing(t, "a data directory without settings", dir)
		}
	})

	t.Run("a missing data directory", func(t *testing.T) {
		s := initSample(t)
		missing := filepath.Join(t.TempDir(), "missing")
		failingBuild(t, "a missing data directory", t.Context(), "whiteroom build: open "+missing+": no such file or directory\n",
			"-data", missing, "-out", s.out, "-cache", s.cache, s.site)
		for _, name := range []string{missing, s.out, s.cache} {
			checkMissing(t, "a missing data directory", name)
		}
	})

	t.Run("an empty data directory", func(t *testing.T) {
		s := initSample(t)
		empty := t.TempDir()
		failingBuild(t, "an empty data directory", t.Context(), "whiteroom build: "+filepath.Join(empty, "logdb")+
			": missing directory\n", "-data", empty, "-out", s.out, "-cache", s.cache, s.site)
		checkEmpty(t, "an empty data directory", empty)
		for _, name := range []string{s.out, s.cache} {
			checkMissing(t, "an empty data directory", name)
		}
	})

	t.Run("no schema file", func(t *testing.T) {
		s := initSample(t)
		schemaOnly(t, s.site, true)
		before := manifestOf(t, s.data)
		if err := os.Remove(filepath.Join(s.site, "site", "schema.yaml")); err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(s.site, "site", "schema.yaml") + ": missing file\n"
		failingBuild(t, "a SITE without the schema file", t.Context(), want, "-data", s.data, "-out", s.out, "-cache", s.cache, s.site)
		// SITE is opened first: its problem comes before that of the data directory, which is not opened.
		missing := filepath.Join(t.TempDir(), "missing")
		failingBuild(t, "a SITE without the schema file, and a missing data directory", t.Context(), want,
			"-data", missing, "-out", s.out, "-cache", s.cache, s.site)
		for _, name := range []string{s.out, s.cache} {
			checkMissing(t, "a SITE without the schema file", name)
		}
		if got := manifestOf(t, s.data); got != before {
			t.Errorf("the files of the data directory are changed:\n%s\nwere:\n%s", got, before)
		}
	})

	t.Run("no theme", func(t *testing.T) {
		s := initSample(t)
		schemaOnly(t, s.site, false)
		failingBuild(t, "a SITE without a theme", t.Context(), filepath.Join(s.site, "theme")+": missing directory\n",
			"-data", s.data, "-out", s.out, "-cache", s.cache, s.site)
		for _, name := range []string{s.out, s.cache} {
			checkMissing(t, "a SITE without a theme", name)
		}
	})

	t.Run("a problem in a document", func(t *testing.T) {
		s := initSample(t)
		schemaOnly(t, s.site, true)
		schema := filepath.Join(s.site, "site", "schema.yaml")
		data, err := os.ReadFile(schema)
		if err != nil {
			t.Fatal(err)
		}
		strict := strings.Replace(string(data), "maxLength: 100", "maxLength: 1", 1)
		if strict == string(data) {
			t.Fatalf("%s has no maxLength: 100", schema)
		}
		if err := os.WriteFile(schema, []byte(strict), 0o644); err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr := buildStored(t, s)
		if code != 1 || stdout != "" {
			t.Errorf("whiteroom build -data of a document that the schema does not accept = %d, with stdout %q, want 1, and none", code,
				stdout)
		}
		found := false
		for line := range strings.Lines(stderr) {
			if strings.Contains(line, s.site) {
				t.Errorf("the line %q has the site directory, which has no document", line)
			}
			if strings.HasPrefix(line, "revisions/rev_01JC0000000000000000000002.json:") && strings.Contains(line, "/fields/client: too long") {
				found = true
			}
		}
		if !found {
			t.Errorf("whiteroom build -data printed:\n%s\nwant a line of revisions/rev_01JC0000000000000000000002.json about /fields/client", stderr)
		}
		checkMissing(t, "a document that the schema does not accept", filepath.Join(s.out, "current"))
	})

	t.Run("a schema that is not YAML", func(t *testing.T) {
		s := initSample(t)
		schemaOnly(t, s.site, true)
		schema := filepath.Join(s.site, "site", "schema.yaml")
		if err := os.WriteFile(schema, []byte("a: [\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr := buildStored(t, s)
		if code != 1 || stdout != "" || !strings.HasPrefix(stderr, schema+":") {
			t.Errorf("whiteroom build -data with a schema that is not YAML = %d, with stdout %q and stderr:\n%s\nwant 1, none, and "+
				"a problem of %s", code, stdout, stderr, schema)
		}
		checkMissing(t, "a schema that is not YAML", filepath.Join(s.out, "current"))
	})

	t.Run("canceled", func(t *testing.T) {
		s := initSample(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		failingBuild(t, "canceled", ctx, "whiteroom build: interrupted\n", "-data", s.data, "-out", s.out, "-cache", s.cache, s.site)
		for _, name := range []string{s.out, s.cache} {
			checkMissing(t, "canceled", name)
		}
	})

	t.Run("usage errors", func(t *testing.T) {
		s := initSample(t)
		for _, args := range [][]string{
			{"-workers", "-1", s.site}, {"-memory", "-1", s.site}, {"-memory", "8796093022208", s.site}, {s.site, s.site},
		} {
			code, stdout, stderr := runCmd(t, append([]string{"build", "-data", s.data, "-out", s.out, "-cache", s.cache}, args...)...)
			if code != 2 || stdout != "" || !strings.HasPrefix(stderr, "whiteroom build: ") || !strings.Contains(stderr, buildUsage) {
				t.Errorf("whiteroom build -data %s = %d, with stdout %q and stderr:\n%s\nwant 2, and a line and the usage",
					strings.Join(args, " "), code, stdout, stderr)
			}
			checkMissing(t, strings.Join(args, " "), s.out)
		}
	})
}

// releaseOfSample builds the sample site from its directory, with the example theme and the flags more, into a new output
// directory and a new cache directory, and returns the name of the release.
func releaseOfSample(t *testing.T, more ...string) string {
	t.Helper()
	out := t.TempDir()
	args := append([]string{"build", "-theme", exampleTheme, "-out", out, "-cache", t.TempDir()}, more...)
	if code, _, stderr := runCmd(t, append(args, sampleSite)...); code != 0 {
		t.Fatalf("whiteroom build %s of the sample site = %d, with stderr:\n%s", strings.Join(more, " "), code, stderr)
	}
	return currentOf(t, out)
}

// TestBuildDataURL builds from a data directory with -url and with the item url of a config file: the release is that of the
// build of the site directory with the same URL, and another than that of no URL, from the data directory that whiteroom
// init made without a URL, from the copy that whiteroom backup took, and from a copy of that as the restored data directory.
// The flag wins over the item. The files and the heads of the audit logs of the data directory are as they were.
func TestBuildDataURL(t *testing.T) {
	const (
		staging = "https://staging.example.com"
		other   = "https://other.example.com"
	)
	plain, changed := releaseOfSample(t), releaseOfSample(t, "-url", staging)
	if plain == changed {
		t.Fatalf("the release of the URL %s is %s, that of no URL", staging, plain)
	}

	s := initSample(t)
	schemaOnly(t, s.site, true)
	files, heads := manifestOf(t, s.data), storedHeads(t, s.data)
	// fromData builds from the data directory data into new directories, with the flags more, and returns the name of the
	// release.
	fromData := func(t *testing.T, data string, more ...string) string {
		t.Helper()
		out := t.TempDir()
		args := append([]string{"build", "-data", data, "-out", out, "-cache", t.TempDir()}, more...)
		code, stdout, stderr := runCmd(t, append(args, s.site)...)
		if want := storedWarnings(t, s.site, ""); code != 0 || stderr != want {
			t.Fatalf("whiteroom build -data %s = %d, with stderr:\n%s\nwant 0, and the warnings:\n%s", strings.Join(more, " "), code, stderr, want)
		}
		name := currentOf(t, out)
		if want := "published release " + name + " in " + out + ": "; !strings.HasPrefix(stdout, want) {
			t.Errorf("whiteroom build -data %s printed %q, want it to start with %q", strings.Join(more, " "), stdout, want)
		}
		return name
	}

	t.Run("the flag", func(t *testing.T) {
		if got := fromData(t, s.data, "-url", staging); got != changed {
			t.Errorf("whiteroom build -data -url %s is the release %s, the build of the directory %s", staging, got, changed)
		}
		if got := fromData(t, s.data); got != plain {
			t.Errorf("whiteroom build -data is the release %s after the build with the URL, want %s", got, plain)
		}
	})

	t.Run("the item of the config file", func(t *testing.T) {
		build := func(what, file string, more ...string) string {
			t.Helper()
			code, _, stderr := runCmd(t, append([]string{"build", "-config", file}, more...)...)
			if want := storedWarnings(t, s.site, ""); code != 0 || stderr != want {
				t.Fatalf("%s: whiteroom build -config = %d, with stderr:\n%s\nwant 0, and the warnings:\n%s", what, code, stderr, want)
			}
			return currentOf(t, s.out)
		}
		if got := build("the item", s.config(t, "url: "+staging)); got != changed {
			t.Errorf("the item url of %s is the release %s, the flag %s", staging, got, changed)
		}
		if got := build("another item", s.config(t, "url: "+other)); got == plain || got == changed {
			t.Errorf("the item url of %s is the release %s, which is that of no URL (%s) or of %s (%s)", other, got, plain, staging, changed)
		}
		if got := build("the flag over the item", s.config(t, "url: "+other), "-url", staging); got != changed {
			t.Errorf("-url %s over the item url of %s is the release %s, want %s", staging, other, got, changed)
		}
	})

	t.Run("a copy", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "backup")
		if code, _, stderr := runCmd(t, "backup", "-data", s.data, dest); code != 0 {
			t.Fatalf("whiteroom backup = %d, with stderr:\n%s", code, stderr)
		}
		restored := filepath.Join(t.TempDir(), "restored")
		if err := os.CopyFS(restored, os.DirFS(dest)); err != nil {
			t.Fatal(err)
		}
		for _, data := range []string{dest, restored} {
			if got := fromData(t, data, "-url", staging); got != changed {
				t.Errorf("whiteroom build -data %s -url %s is the release %s, want %s", data, staging, got, changed)
			}
			if got := fromData(t, data); got != plain {
				t.Errorf("whiteroom build -data %s is the release %s, want %s", data, got, plain)
			}
		}
	})

	if got := manifestOf(t, s.data); got != files {
		t.Errorf("the files of the data directory are changed by the builds:\n%s\nwere:\n%s", got, files)
	}
	if got := storedHeads(t, s.data); !maps.Equal(got, heads) {
		t.Errorf("the heads of the audit logs are changed by the builds: %v, were %v", got, heads)
	}
}
