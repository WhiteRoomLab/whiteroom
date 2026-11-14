//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/build"
)

const (
	renamedSite = "Other Inc."
	aboutPage   = "/en/about/"
)

// twoReleases builds a copy of the sample site into a new output directory, then builds it again with another name of the
// site in English, and returns the output directory and the names of the first release, which is left, and of the second,
// which is published.
func twoReleases(t *testing.T) (out, older, newer string) {
	t.Helper()
	site := copySample(t)
	out, cache := t.TempDir(), t.TempDir()
	buildCopy := func() string {
		t.Helper()
		if code, _, stderr := runCmd(t, "build", "-out", out, "-cache", cache, site); code != 0 {
			t.Fatalf("whiteroom build of a copy of the sample site = %d, with stderr:\n%s", code, stderr)
		}
		return currentOf(t, out)
	}
	older = buildCopy()
	settings := filepath.Join(site, "site", "settings.json")
	data, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	renamed := strings.Replace(string(data), `"en": "Example Inc."`, `"en": "`+renamedSite+`"`, 1)
	if renamed == string(data) {
		t.Fatalf(`%s has no "en": "Example Inc."`, settings)
	}
	if err := os.WriteFile(settings, []byte(renamed), 0o644); err != nil {
		t.Fatal(err)
	}
	newer = buildCopy()
	if older == newer {
		t.Fatalf("the sites with two names of the site make the same release %s", older)
	}
	return out, older, newer
}

// releasesOf returns the releases that out holds: the published one and the names.
func releasesOf(t *testing.T, out string) build.Releases {
	t.Helper()
	rel, err := build.ListReleases(out)
	if err != nil {
		t.Fatal(err)
	}
	return rel
}

// checkReleases checks that out holds the releases names, and that the release current is published.
func checkReleases(t *testing.T, what, out, current string, names ...string) {
	t.Helper()
	slices.Sort(names)
	if got := releasesOf(t, out); got.Current != current || !slices.Equal(got.Names, names) {
		t.Errorf("%s: %s holds the releases %v and publishes %q, want %v and %q", what, out, got.Names, got.Current, names, current)
	}
	if got := currentOf(t, out); got != current {
		t.Errorf("%s: current points to %s, want %s", what, got, current)
	}
}

// pageIsRenamed reports whether the page of the English site in the published release of out has the new name of the site.
func pageIsRenamed(t *testing.T, out string) bool {
	t.Helper()
	page, err := os.ReadFile(filepath.Join(out, "current", "public", "en", "about", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(page), renamedSite)
}

// TestRollback goes back and forth between the two releases of an output directory, with the other release that is left and
// with its name, while whiteroom serve public serves the directory, which gives the page of the release that is published
// from the request after the command; a rollback to the published release changes nothing and prints nothing on stderr; and
// a rollback that changes the published release warns on stderr that a server builds again.
func TestRollback(t *testing.T) {
	out, older, newer := twoReleases(t)
	checkReleases(t, "the builds", out, newer, older, newer)
	if !pageIsRenamed(t, out) {
		t.Fatalf("the page of the release %s has not the name %q", newer, renamedSite)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	line, wait := startServe(t, ctx, "-addr", "127.0.0.1:0", out)
	origin := originOf(t, line, out, "")
	client := newClient()
	defer client.CloseIdleConnections()
	served := func() bool {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, origin+aboutPage, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", aboutPage, err)
		}
		defer func() { _ = res.Body.Close() }()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatalf("GET %s: %v", aboutPage, err)
		}
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", aboutPage, res.StatusCode)
		}
		return strings.Contains(string(body), renamedSite)
	}
	if !served() {
		t.Fatalf("serve public does not give the page of the release %s, which is published", newer)
	}

	rollback := func(what string, wantCurrent, wasCurrent string, args ...string) {
		t.Helper()
		code, stdout, stderr := runCmd(t, append([]string{"rollback", out}, args...)...)
		if code != 0 {
			t.Fatalf("%s: whiteroom rollback %s = %d, with stderr:\n%s", what, strings.Join(args, " "), code, stderr)
		}
		if want := "rolled back to release " + wantCurrent + " in " + out + " (was " + wasCurrent + ")\n"; stdout != want {
			t.Errorf("%s: whiteroom rollback printed %q, want %q", what, stdout, want)
		}
		if !strings.HasPrefix(stderr, "whiteroom rollback: warning: ") || strings.Count(stderr, "\n") != 1 ||
			!strings.Contains(stderr, "POST /api/releases/{name}/rollback") {
			t.Errorf("%s: whiteroom rollback printed on stderr:\n%s\nwant a line that warns that a server builds again, and names "+
				"POST /api/releases/{name}/rollback", what, stderr)
		}
		checkReleases(t, what, out, wantCurrent, older, newer)
		if got, want := pageIsRenamed(t, out), wantCurrent == newer; got != want {
			t.Errorf("%s: the page of the published release has the new name: %t, want %t", what, got, want)
		}
		if got, want := served(), wantCurrent == newer; got != want {
			t.Errorf("%s: serve public gives the page with the new name: %t, want %t", what, got, want)
		}
	}
	rollback("to the other release", older, newer)
	rollback("back to the other release", newer, older)
	rollback("to a release by its name", older, newer, older)
	rollback("to a release by its name, again", newer, older, newer)
	rollback("to the other release, again", older, newer)

	for range 2 {
		code, stdout, stderr := runCmd(t, "rollback", out, older)
		if want := "release " + older + " in " + out + " is already published\n"; code != 0 || stdout != want || stderr != "" {
			t.Errorf("whiteroom rollback to the published release = %d, with stdout %q and stderr %q, want 0, %q and none", code,
				stdout, stderr, want)
		}
		checkReleases(t, "to the published release", out, older, older, newer)
	}

	cancel()
	wait()
}

// TestRollbackErrors checks the arguments and the failures of whiteroom rollback, which print one line after the name of
// the command on stderr, nothing on stdout, and leave the output directory as it was.
func TestRollbackErrors(t *testing.T) {
	out, older, newer := twoReleases(t)
	fail := func(what string, wantErr string, args ...string) {
		t.Helper()
		code, stdout, stderr := runCmd(t, append([]string{"rollback"}, args...)...)
		if code != 1 || stdout != "" || !strings.HasPrefix(stderr, "whiteroom rollback: "+wantErr) || strings.Count(stderr, "\n") != 1 {
			t.Errorf("%s: whiteroom rollback %s = %d, with stdout %q and stderr:\n%s\nwant 1, none, and one line that starts "+
				"with %q", what, strings.Join(args, " "), code, stdout, stderr, "whiteroom rollback: "+wantErr)
		}
		checkReleases(t, what, out, newer, older, newer)
	}
	fail("an invalid name", `invalid release name "x"`, out, "x")
	fail("an upper case name", `invalid release name "0123456789ABCDEF"`, out, "0123456789ABCDEF")
	fail("an empty name", `invalid release name ""`, out, "")
	gone := "0123456789abcdef"
	fail("a release that is not left", filepath.Join(out, "releases", gone)+": no such release", out, gone)
	checkMissing(t, "a release that is not left", filepath.Join(out, "releases", gone))

	missing := filepath.Join(t.TempDir(), "missing")
	code, stdout, stderr := runCmd(t, "rollback", missing, older)
	_, openErr := os.OpenRoot(missing)
	if want := "whiteroom rollback: " + openErr.Error() + "\n"; code != 1 || stdout != "" || stderr != want {
		t.Errorf("whiteroom rollback of a missing directory = %d, with stdout %q and stderr:\n%s\nwant 1, none, and %q", code, stdout,
			stderr, want)
	}
	checkMissing(t, "a missing directory", missing)
	code, stdout, stderr = runCmd(t, "rollback", "")
	if want := "whiteroom rollback: no output directory\n"; code != 1 || stdout != "" || stderr != want {
		t.Errorf("whiteroom rollback of the empty name = %d, with stdout %q and stderr:\n%s\nwant 1, none, and %q", code, stdout, stderr,
			want)
	}

	foreign := t.TempDir()
	if err := os.WriteFile(filepath.Join(foreign, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runCmd(t, "rollback", foreign, older)
	if code != 1 || stdout != "" || !strings.HasPrefix(stderr, "whiteroom rollback: ") || strings.Count(stderr, "\n") != 1 {
		t.Errorf("whiteroom rollback of a directory that a build has not made = %d, with stdout %q and stderr:\n%s\nwant 1, none, "+
			"and one line", code, stdout, stderr)
	}
	if entries, err := os.ReadDir(foreign); err != nil || len(entries) != 1 {
		t.Errorf("whiteroom rollback of a directory that a build has not made leaves %v (%v), want a.txt only", entries, err)
	}

	// With only one release, there is no other to go back to; and nothing is published without a release.
	site := copySample(t)
	one, cache := t.TempDir(), t.TempDir()
	if code, _, stderr := runCmd(t, "build", "-out", one, "-cache", cache, site); code != 0 {
		t.Fatalf("whiteroom build = %d, with stderr:\n%s", code, stderr)
	}
	only := currentOf(t, one)
	code, stdout, stderr = runCmd(t, "rollback", one)
	if code != 1 || stdout != "" || !strings.HasPrefix(stderr, "whiteroom rollback: no release to roll back to") ||
		strings.Count(stderr, "\n") != 1 {
		t.Errorf("whiteroom rollback of one release = %d, with stdout %q and stderr:\n%s\nwant 1, none, and one line", code, stdout,
			stderr)
	}
	if got := currentOf(t, one); got != only {
		t.Errorf("whiteroom rollback of one release: current points to %s, want %s", got, only)
	}
}

// TestOtherRelease checks the choice of the release when the command names none.
func TestOtherRelease(t *testing.T) {
	const a, b, c = "00000000000000aa", "00000000000000bb", "00000000000000cc"
	for _, tt := range []struct {
		what string
		rel  build.Releases
		want string
		err  string // "" for no error
	}{
		{"the other of two", build.Releases{Current: b, Names: []string{a, b}}, a, ""},
		{"the other of two, the first published", build.Releases{Current: a, Names: []string{a, b}}, b, ""},
		{"only the published one", build.Releases{Current: a, Names: []string{a}}, "", "no release to roll back to: no other release is left"},
		{
			"two others", build.Releases{Current: b, Names: []string{a, b, c}}, "",
			"2 releases are left besides the published one: give RELEASE, one of " + a + ", " + c,
		},
		{
			"two others, in the order of the names", build.Releases{Current: a, Names: []string{c, b, a}}, "",
			"2 releases are left besides the published one: give RELEASE, one of " + b + ", " + c,
		},
		{
			"one release and no published one", build.Releases{Names: []string{a}}, "",
			"no release is published, so there is no release to go back to: give RELEASE, one of " + a,
		},
		{
			"two releases and no published one", build.Releases{Names: []string{a, b}}, "",
			"no release is published, so there is no release to go back to: give RELEASE, one of " + a + ", " + b,
		},
		{"no release", build.Releases{}, "", "no release to roll back to: no other release is left"},
	} {
		got, err := otherRelease(tt.rel)
		switch {
		case tt.err == "" && (err != nil || got != tt.want):
			t.Errorf("%s: otherRelease(%+v) = %q, %v, want %q", tt.what, tt.rel, got, err, tt.want)
		case tt.err != "" && (got != "" || err == nil || err.Error() != tt.err):
			t.Errorf("%s: otherRelease(%+v) = %q, %v, want the error %q", tt.what, tt.rel, got, err, tt.err)
		}
	}
}

// TestRollbackWithoutCurrent rolls back an output directory that has releases and no published one: without the name of the
// release it names the releases that there are, and with the name it publishes the release, and tells no release that it
// replaces.
func TestRollbackWithoutCurrent(t *testing.T) {
	out, older, newer := twoReleases(t)
	if err := os.Remove(filepath.Join(out, "current")); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCmd(t, "rollback", out)
	names := slices.Sorted(slices.Values([]string{older, newer}))
	want := "whiteroom rollback: no release is published, so there is no release to go back to: give RELEASE, one of " +
		strings.Join(names, ", ") + "\n"
	if code != 1 || stdout != "" || stderr != want {
		t.Errorf("whiteroom rollback of releases without current = %d, with stdout %q and stderr:\n%s\nwant 1, none, and %q", code, stdout,
			stderr, want)
	}
	checkMissing(t, "the failed rollback", filepath.Join(out, "current"))

	code, stdout, stderr = runCmd(t, "rollback", out, older)
	if want := "rolled back to release " + older + " in " + out + "\n"; code != 0 || stdout != want ||
		!strings.Contains(stderr, "POST /api/releases/{name}/rollback") {
		t.Fatalf("whiteroom rollback %s of releases without current = %d, with stdout %q and stderr:\n%s\nwant 0, %q, and the warning",
			older, code, stdout, stderr, want)
	}
	checkReleases(t, "the rollback", out, older, older, newer)
}

// TestRollbackBesideAdmin rolls back the output directory of a running whiteroom serve admin, which is not building: the
// published release is the one before, and the next build of the server publishes the release of the store again, as the
// warning of the command says.
func TestRollbackBesideAdmin(t *testing.T) {
	s := initSample(t)
	a := startAdmin(t, t.Context(), s.args()...)
	cookie := a.login(t)
	if built := a.waitBuilt(t, cookie); failedBuild(built) || built.Last.Release == "" {
		t.Fatalf("the start build has %+v, want a release\nstderr:\n%s", built.Last, a.stderr)
	}
	first := currentOf(t, s.out)
	id := a.publishPage(t, cookie, "en", "Published before the rollback", "before-the-rollback")
	if built := a.waitPublished(t, cookie, id); failedBuild(built) || built.Last.Release == "" {
		t.Fatalf("the build of the publishing has %+v, want a release\nstderr:\n%s", built.Last, a.stderr)
	}
	second := currentOf(t, s.out)
	if second == first {
		t.Fatalf("the server has published the release %s, as before the page", second)
	}

	code, stdout, stderr := runCmd(t, "rollback", s.out)
	if want := "rolled back to release " + first + " in " + s.out + " (was " + second + ")\n"; code != 0 || stdout != want ||
		!strings.Contains(stderr, "POST /api/releases/{name}/rollback") {
		t.Fatalf("whiteroom rollback beside the server = %d, with stdout %q and stderr:\n%s\nwant 0, %q, and the warning", code, stdout,
			stderr, want)
	}
	checkReleases(t, "the rollback", s.out, first, first, second)

	status, answer := a.exchange(t, http.MethodPost, "/api/builds", "", "Cookie", cookie)
	if status != http.StatusAccepted {
		t.Fatalf("POST /api/builds = %d, with the body %s, want 202", status, answer)
	}
	a.waitFor(t, adminWait, "the build that publishes the release of the store again", func() bool {
		return currentOf(t, s.out) == second
	})
	if built := a.waitBuilt(t, cookie); failedBuild(built) || built.Last.Release != second {
		t.Errorf("the build after the rollback has %+v, want the release %s", built.Last, second)
	}
	checkReleases(t, "the build after the rollback", s.out, second, first, second)

	if code := a.stop(t); code != 0 {
		t.Errorf("whiteroom serve admin = %d after its context is done, want 0", code)
	}
}
