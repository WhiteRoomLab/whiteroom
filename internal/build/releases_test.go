//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package build

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

const (
	privacyRevision = "rev_01JBD1M2N3P4Q5R6S7T8V9W0X1" // the revision of the English privacy page of the sample site
	privacyPage     = "public/en/privacy/index.html"   // its file in a release
)

// retitled returns src, the sample site, with another title of its English privacy page, which changes that page and
// the name of the release.
func retitled(t *testing.T, src Source, title string) Source {
	t.Helper()
	f, err := src.Revision(privacyRevision)
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Replace(f.Data, []byte(`"title": "Privacy policy"`), []byte(`"title": "`+title+`"`), 1)
	if bytes.Equal(data, f.Data) {
		t.Fatalf("%s has no title to change", f.Name)
	}
	return changedSource{Source: src, files: []content.SiteFile{{Name: f.Name, Data: data}}}
}

// foreignDir returns an output directory that holds what the build has not made, and the errors of checking it.
func foreignDir(t *testing.T) (dir, errs string) {
	t.Helper()
	dir = makeDir(t, "README", "bin/tool", "releases/x", "releases/"+releaseA+"/", "current -> /srv/www")
	errs = strings.Join([]string{
		filepath.Join(dir, "README") + ": not made by the build; " + outMust,
		filepath.Join(dir, "bin") + ": not made by the build; " + outMust,
		filepath.Join(dir, "current") + `: not made by the build; it points to "/srv/www", not to a release`,
		filepath.Join(dir, "releases", "x") + ": not made by the build; " + releasesMust,
	}, "\n")
	return dir, errs
}

// checkListed checks that ListReleases of out returns the release current and releases.
func checkListed(t *testing.T, what, out, current string, releases ...string) {
	t.Helper()
	want := Releases{Current: current, Names: slices.Sorted(slices.Values(releases))}
	if got, err := ListReleases(out); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("%s: ListReleases() = %+v, %v, want %+v", what, got, err, want)
	}
}

func checkUnlocked(t *testing.T, what, out string) {
	t.Helper()
	unlock, err := lockOut(openRoot(t, out))
	if err != nil {
		t.Errorf("%s: lockOut() = %v, want no build to hold the lock", what, err)
		return
	}
	unlock()
}

// withCurrent returns lines, a tree of an output directory, with the line of current changed to point to the release
// name.
func withCurrent(t *testing.T, lines []string, name string) []string {
	t.Helper()
	lines = slices.Clone(lines)
	i := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "current ") })
	if i < 0 {
		t.Fatalf("no current in %q", lines)
	}
	lines[i] = lines[i][:strings.LastIndex(lines[i], " ")+1] + "releases/" + name
	return lines
}

// TestListReleases checks the releases that ListReleases finds in output directories that hold only what the build
// makes, in the order of their names, and that it writes nothing.
func TestListReleases(t *testing.T) {
	for _, tt := range []struct {
		what    string
		entries []string
		want    Releases
	}{
		{"empty", nil, Releases{}},
		{"first build", []string{".lock", "releases/"}, Releases{}},
		{
			"releases",
			[]string{
				".lock",
				"releases/" + releaseA + "/public/index.html",
				"releases/" + releaseB + "/",
				"releases/" + releaseC + "/",
				"current -> releases/" + releaseB,
			},
			Releases{Current: releaseB, Names: []string{releaseC, releaseA, releaseB}},
		},
		{"no current", []string{".lock", "releases/" + releaseA + "/"}, Releases{Names: []string{releaseA}}},
		{
			"a current to a release that does not exist",
			[]string{".lock", "releases/" + releaseA + "/", "current -> releases/" + releaseB},
			Releases{Names: []string{releaseA}},
		},
		{"a current without releases", []string{"current -> releases/" + releaseA}, Releases{}},
		{
			"stale",
			[]string{
				".lock",
				"releases/" + releaseA + "/",
				"releases/.tmp-" + randomA + "/public/",
				"current -> releases/" + releaseA,
				"current.tmp-" + randomB + " -> releases/" + releaseB,
			},
			Releases{Current: releaseA, Names: []string{releaseA}},
		},
	} {
		dir := makeDir(t, tt.entries...)
		before := tree(t, dir)
		if got, err := ListReleases(dir); err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: ListReleases() = %+v, %v, want %+v", tt.what, got, err, tt.want)
		}
		checkTree(t, tt.what, dir, before)
	}
}

// TestListReleasesErrors checks the errors of ListReleases: those of checking the output directory as the build does,
// sorted by path, and a missing directory, and that it writes nothing.
func TestListReleasesErrors(t *testing.T) {
	dir, errs := foreignDir(t)
	before := tree(t, dir)
	if got, err := ListReleases(dir); err == nil || err.Error() != errs || !reflect.DeepEqual(got, Releases{}) {
		t.Errorf("ListReleases() of foreign files = %+v, %v, want the errors:\n%s", got, err, errs)
	}
	checkTree(t, "foreign files", dir, before)

	missing := filepath.Join(t.TempDir(), "missing")
	if got, err := ListReleases(missing); !errors.Is(err, fs.ErrNotExist) || !reflect.DeepEqual(got, Releases{}) {
		t.Errorf("ListReleases() of a missing directory = %+v, %v, want an error that wraps %v", got, err, fs.ErrNotExist)
	}
	if _, err := os.Lstat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ListReleases() made %s (%v)", missing, err)
	}
	got, err := ListReleases("")
	if err == nil || err.Error() != "no output directory" || !reflect.DeepEqual(got, Releases{}) {
		t.Errorf("ListReleases(%q) = %+v, %v, want the error %q", "", got, err, "no output directory")
	}
}

// TestRollbackErrors checks that Rollback refuses names that are not 16 lower case hex digits, releases that the output
// directory does not have (a temporary directory is none), and output directories that hold what the build has not
// made or do not exist, with the errors, and leaves them as they are, without a lock file.
func TestRollbackErrors(t *testing.T) {
	base := []string{
		".lock",
		"releases/" + releaseA + "/public/a",
		"releases/.tmp-" + randomA + "/public/new",
		"current -> releases/" + releaseA,
	}
	foreign, foreignErrs := foreignDir(t)
	for _, tt := range []struct {
		what    string
		dir     string   // an output directory, if entries is nil
		entries []string // the entries of a new output directory
		name    string
		want    string // the errors, if they are not that the output directory has no such release
	}{
		{what: "an unknown release", entries: base, name: releaseB},
		{what: "a temporary directory", entries: base, name: randomA},
		{what: "an empty directory", entries: []string{}, name: releaseA},
		{what: "a directory without a release", entries: []string{".lock", "releases/"}, name: releaseA},
		{what: "foreign files", dir: foreign, name: releaseA, want: foreignErrs},
	} {
		dir := tt.dir
		if tt.entries != nil {
			dir = makeDir(t, tt.entries...)
		}
		want := cmp.Or(tt.want, filepath.Join(dir, "releases", tt.name)+": no such release")
		before := tree(t, dir)
		if changed, err := Rollback(dir, tt.name); changed || err == nil || err.Error() != want {
			t.Errorf("%s: Rollback() = %v, %v, want the error %q", tt.what, changed, err, want)
		}
		checkTree(t, tt.what, dir, before)
	}

	dir := makeDir(t, base...)
	before := tree(t, dir)
	for _, name := range []string{
		"", "latest", strings.ToUpper(releaseB), releaseA[:15], releaseA + "0", "releases/" + releaseA, "../" + releaseA[3:],
		releaseA + "\n", " " + releaseA, "current",
	} {
		want := fmt.Sprintf("invalid release name %q", name)
		if changed, err := Rollback(dir, name); changed || err == nil || err.Error() != want {
			t.Errorf("Rollback(%q) = %v, %v, want the error %q", name, changed, err, want)
		}
	}
	checkTree(t, "invalid names", dir, before)

	// The arguments come first: no directory is opened for a name that cannot be one.
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := Rollback(missing, "latest"); err == nil || err.Error() != `invalid release name "latest"` {
		t.Errorf("Rollback() of a missing directory with an invalid name = %v", err)
	}
	if changed, err := Rollback(missing, releaseA); changed || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Rollback() of a missing directory = %v, %v, want an error that wraps %v", changed, err, fs.ErrNotExist)
	}
	if _, err := os.Lstat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Rollback() made %s (%v)", missing, err)
	}
	if changed, err := Rollback("", releaseA); changed || err == nil || err.Error() != "no output directory" {
		t.Errorf("Rollback(%q) = %v, %v, want the error %q", "", changed, err, "no output directory")
	}
}

// TestRollbackLayouts checks what Rollback leaves in output directories that hold only what the build makes: only
// current changes, and what builds that did not end left stays.
func TestRollbackLayouts(t *testing.T) {
	releases := []string{
		".lock",
		"releases/" + releaseA + "/public/a",
		"releases/" + releaseB + "/public/b",
		"releases/" + releaseC + "/public/c",
	}
	stale := []string{"releases/.tmp-" + randomA + "/public/new", "current.tmp-" + randomB + " -> releases/" + releaseC}
	pointing := func(name string, more ...string) []string {
		return append([]string{"current -> releases/" + name}, more...)
	}
	for _, tt := range []struct {
		what    string
		entries []string // besides the releases
		name    string
		changed bool
		want    []string // besides the releases
	}{
		{"another release", pointing(releaseA), releaseB, true, pointing(releaseB)},
		{"the release of current", pointing(releaseB), releaseB, false, pointing(releaseB)},
		{"no current", nil, releaseC, true, pointing(releaseC)},
		{
			"a current to a release that does not exist",
			pointing(strings.Repeat("e", 16)), releaseA, true, pointing(releaseA),
		},
		{"stale", pointing(releaseA, stale...), releaseB, true, pointing(releaseB, stale...)},
		{"stale and the release of current", pointing(releaseA, stale...), releaseA, false, pointing(releaseA, stale...)},
	} {
		dir := makeDir(t, append(slices.Clone(releases), tt.entries...)...)
		changed, err := Rollback(dir, tt.name)
		if changed != tt.changed || err != nil {
			t.Errorf("%s: Rollback() = %v, %v, want %v", tt.what, changed, err, tt.changed)
		}
		checkTree(t, tt.what, dir, tree(t, makeDir(t, append(slices.Clone(releases), tt.want...)...)))
		checkUnlocked(t, tt.what, dir)
	}
}

// TestRollbackLocked checks that Rollback fails at once when a build holds the lock of the output directory, and
// leaves it as it is, that ListReleases does not need the lock, and that Rollback unlocks the directory when it ends.
func TestRollbackLocked(t *testing.T) {
	dir := makeDir(t,
		".lock", "releases/"+releaseA+"/public/a", "releases/"+releaseB+"/public/b", "current -> releases/"+releaseA)
	unlock, err := lockOut(openRoot(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	before := tree(t, dir)
	want := "another build is running in " + dir
	if changed, err := Rollback(dir, releaseB); changed || err == nil || err.Error() != want {
		t.Errorf("Rollback() while locked = %v, %v, want the error %q", changed, err, want)
	}
	checkTree(t, "locked", dir, before)
	checkListed(t, "locked", dir, releaseA, releaseA, releaseB)
	unlock()

	for _, name := range []string{releaseB, releaseB, releaseA} {
		if _, err := Rollback(dir, name); err != nil {
			t.Errorf("Rollback(%q) after the lock = %v", name, err)
		}
		checkUnlocked(t, "Rollback("+name+")", dir)
	}
	if got := currentOf(t, dir); got != releaseA {
		t.Errorf("current points to %s, want %s", got, releaseA)
	}
}

// TestRollbackRecheck checks the part of Rollback that runs under the lock, which checks the output directory again: a
// build that has ended since the first check may have removed the release, and current must not point to a release
// that is gone.
func TestRollbackRecheck(t *testing.T) {
	dir := makeDir(t, ".lock", "releases/"+releaseA+"/public/a", "current -> releases/"+releaseA)
	before := tree(t, dir)
	want := filepath.Join(dir, "releases", releaseB) + ": no such release"
	if changed, err := rollbackLocked(openRoot(t, dir), releaseB); changed || err == nil || err.Error() != want {
		t.Errorf("rollbackLocked() to a release that is gone = %v, %v, want the error %q", changed, err, want)
	}
	checkTree(t, "a release that is gone", dir, before)

	dir = makeDir(t, "README", "releases/"+releaseA+"/public/a")
	before = tree(t, dir)
	want = filepath.Join(dir, "README") + ": not made by the build; " + outMust
	if changed, err := rollbackLocked(openRoot(t, dir), releaseA); changed || err == nil || err.Error() != want {
		t.Errorf("rollbackLocked() with a foreign file = %v, %v, want the error %q", changed, err, want)
	}
	checkTree(t, "a foreign file", dir, before)

	dir = makeDir(t,
		".lock", "releases/"+releaseA+"/public/a", "releases/"+releaseB+"/public/b", "current -> releases/"+releaseA)
	for _, tt := range []struct {
		name    string
		changed bool
	}{{releaseA, false}, {releaseB, true}, {releaseB, false}} {
		if changed, err := rollbackLocked(openRoot(t, dir), tt.name); changed != tt.changed || err != nil {
			t.Errorf("rollbackLocked(%q) = %v, %v, want %v", tt.name, changed, err, tt.changed)
		}
	}
	if got := currentOf(t, dir); got != releaseB {
		t.Errorf("current points to %s, want %s", got, releaseB)
	}
}

// TestRollbackReadOnly checks that a Rollback that cannot make the new link leaves the output directory as it was, and
// unlocks it.
func TestRollbackReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may write in any directory")
	}
	dir := makeDir(t,
		".lock", "releases/"+releaseA+"/public/a", "releases/"+releaseB+"/public/b", "current -> releases/"+releaseA)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	before := tree(t, dir)
	pattern := regexp.QuoteMeta(dir) + `: symlinkat releases/` + releaseB + ` current\.tmp-[0-9a-f]{16}: permission denied`
	changed, err := Rollback(dir, releaseB)
	if changed || !errors.Is(err, fs.ErrPermission) || !regexp.MustCompile("^"+pattern+"$").MatchString(err.Error()) {
		t.Errorf("Rollback() in a read-only directory = %v, %v, want an error that matches %s", changed, err, pattern)
	}
	checkTree(t, "read-only", dir, before)
	checkUnlocked(t, "read-only", dir)
}

// TestRollbackAtOnce checks that Rollback switches current at once: while Rollback switches it between two releases
// time after time, readers always find current a link to one of them and the file of a release through it, and
// ListReleases always finds both releases and one of them as current. The link itself is read with readlink(2), which
// never fails while a rename replaces the link, so a switch that removes current before it makes it again fails the
// test; only the read through the link may be read again (readThrough).
func TestRollbackAtOnce(t *testing.T) {
	dir := makeDir(t,
		".lock", "releases/"+releaseA+"/public/page", "releases/"+releaseB+"/public/page", "current -> releases/"+releaseA)
	for _, name := range []string{releaseA, releaseB} {
		if err := os.WriteFile(filepath.Join(dir, "releases", name, "public", "page"), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	current := filepath.Join(dir, "current")
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				if target, err := os.Readlink(current); err != nil ||
					(target != "releases/"+releaseA && target != "releases/"+releaseB) {
					t.Errorf("current = %q, %v, want a link to a release", target, err)
					return
				}
				page, err := readThrough(filepath.Join(current, "public", "page"))
				if err != nil || (string(page) != releaseA && string(page) != releaseB) {
					t.Errorf("the page through current = %q, %v, want that of a release", page, err)
					return
				}
				got, err := ListReleases(dir)
				if err != nil || !slices.Equal(got.Names, []string{releaseA, releaseB}) ||
					(got.Current != releaseA && got.Current != releaseB) {
					t.Errorf("ListReleases() = %+v, %v, want both releases and one as current", got, err)
					return
				}
			}
		})
	}
	for i := range 200 {
		name := []string{releaseB, releaseA}[i%2]
		if changed, err := Rollback(dir, name); !changed || err != nil {
			t.Errorf("Rollback(%q) = %v, %v, want true", name, changed, err)
			break
		}
	}
	close(stop)
	wg.Wait()
	checkReleases(t, "after the switches", dir, releaseA, releaseA, releaseB)
}

// readThrough reads name, a path through current, again at once if the file does not exist, at most 3 more times. On
// Linux a read that follows a link may fail with ENOENT for a moment when a rename replaces the link: ext4 clears the
// replaced link while a path walk that takes no locks may still read it (4.3). The bound makes a current that stays
// unreadable fail the test instead of hanging it.
func readThrough(name string) ([]byte, error) {
	data, err := os.ReadFile(name)
	for i := 0; i < 3 && errors.Is(err, fs.ErrNotExist); i++ {
		data, err = os.ReadFile(name)
	}
	return data, err
}

// TestRollback builds the sample site and a change of it into one output directory, and checks Rollback to the first
// release, which makes the files of the first build the ones that current has, and nothing else; Rollback to the same
// release and to a release that it has left aside; what it refuses; and that a build after a rollback keeps its new
// release and the release of the rollback, and removes the other, while a Rollback is refused during the build.
func TestRollback(t *testing.T) {
	src := openSample(t)
	out, cache := t.TempDir(), t.TempDir()
	first := mustBuild(t, src, out, cache, 4)
	second := mustBuild(t, retitled(t, src, "Privacy notice"), out, cache, 4)
	a, b := first.Release, second.Release
	if a == b || !second.Changed {
		t.Fatalf("Build() with another title = %+v after %+v, want a new release", second, first)
	}
	checkReleases(t, "two builds", out, b, a, b)
	checkListed(t, "two builds", out, b, a, b)
	releaseDir := func(name string) string { return filepath.Join(out, "releases", name) }
	pageA, pageB := readFile(t, releaseDir(a), privacyPage), readFile(t, releaseDir(b), privacyPage)
	if !strings.Contains(pageA, "Privacy policy") || !strings.Contains(pageB, "Privacy notice") || pageA == pageB {
		t.Fatalf("the privacy pages of the releases are:\n%s\nand:\n%s", pageA, pageB)
	}
	filesA, filesB := manifest(t, releaseDir(a)), manifest(t, releaseDir(b))
	current := filepath.Join(out, "current")
	if readFile(t, current, privacyPage) != pageB || manifest(t, current) != filesB {
		t.Errorf("the files through current are not those of the second build")
	}

	before := tree(t, out)
	if changed, err := Rollback(out, a); !changed || err != nil {
		t.Fatalf("Rollback() to the first release = %v, %v, want true", changed, err)
	}
	checkReleases(t, "rolled back", out, a, a, b)
	checkListed(t, "rolled back", out, a, a, b)
	checkTree(t, "rolled back", out, withCurrent(t, before, a))
	if readFile(t, current, privacyPage) != pageA || manifest(t, current) != filesA {
		t.Errorf("the files through current are not those of the first build")
	}

	before = tree(t, out)
	if changed, err := Rollback(out, a); changed || err != nil {
		t.Errorf("Rollback() to the release of current = %v, %v, want false", changed, err)
	}
	checkTree(t, "rolled back again", out, before)

	refused := func(what, name, want string) {
		t.Helper()
		if changed, err := Rollback(out, name); changed || err == nil || err.Error() != want {
			t.Errorf("Rollback() %s = %v, %v, want the error %q", what, changed, err, want)
		}
	}
	refused("to an unknown release", releaseA, filepath.Join(out, "releases", releaseA)+": no such release")
	refused("to an invalid name", "latest", `invalid release name "latest"`)
	unlock, err := lockOut(openRoot(t, out))
	if err != nil {
		t.Fatal(err)
	}
	refused("while a build holds the lock", b, "another build is running in "+out)
	checkListed(t, "locked", out, a, a, b)
	unlock()
	readme := filepath.Join(out, "README")
	if err := os.WriteFile(readme, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	foreign := readme + ": not made by the build; " + outMust
	refused("with a file that the build has not made", b, foreign)
	if got, err := ListReleases(out); err == nil || err.Error() != foreign || !reflect.DeepEqual(got, Releases{}) {
		t.Errorf("ListReleases() with a file that the build has not made = %+v, %v, want the error %q", got, err, foreign)
	}
	if err := os.Remove(readme); err != nil {
		t.Fatal(err)
	}
	checkTree(t, "what Rollback refused", out, before)

	// The release that a rollback left aside is still one to roll back to.
	for _, name := range []string{b, a, b, a} {
		if changed, err := Rollback(out, name); !changed || err != nil {
			t.Fatalf("Rollback(%s) = %v, %v, want true", name, changed, err)
		}
		checkReleases(t, "back and forth", out, name, a, b)
		checkListed(t, "back and forth", out, name, a, b)
	}

	// A build makes a third release, during which Rollback fails and ListReleases does not.
	var once sync.Once
	var rollbackErr, listErr error
	var listed Releases
	hooked := hookedSource{Source: retitled(t, src, "Privacy notice 2"), hook: func(string) {
		once.Do(func() {
			_, rollbackErr = Rollback(out, b)
			listed, listErr = ListReleases(out)
		})
	}}
	third := mustBuild(t, hooked, out, cache, 4)
	if want := "another build is running in " + out; rollbackErr == nil || rollbackErr.Error() != want {
		t.Errorf("Rollback() during a build = %v, want the error %q", rollbackErr, want)
	}
	if want := (Releases{Current: a, Names: slices.Sorted(slices.Values([]string{a, b}))}); listErr != nil ||
		!reflect.DeepEqual(listed, want) {
		t.Errorf("ListReleases() during a build = %+v, %v, want %+v", listed, listErr, want)
	}
	c := third.Release
	if c == a || c == b || !third.Changed {
		t.Fatalf("Build() after the rollback = %+v, want a new release", third)
	}
	checkReleases(t, "a build after a rollback", out, c, a, c)
	checkListed(t, "a build after a rollback", out, c, a, c)

	removed := filepath.Join(out, "releases", b) + ": no such release"
	if changed, err := Rollback(out, b); changed || err == nil || err.Error() != removed {
		t.Errorf("Rollback() to a release that a build removed = %v, %v, want the error %q", changed, err, removed)
	}
	if changed, err := Rollback(out, a); !changed || err != nil {
		t.Fatalf("Rollback() to the release that the build kept = %v, %v, want true", changed, err)
	}

	// The site of the release of the rollback again: the build has nothing to change, and removes nothing.
	again := mustBuild(t, src, out, cache, 4)
	if again.Release != a || again.Changed {
		t.Errorf("Build() of the site of the rollback = %+v, want %s unchanged", again, a)
	}
	checkReleases(t, "the site of the rollback again", out, a, a, c)
	if readFile(t, current, privacyPage) != pageA || manifest(t, current) != filesA {
		t.Errorf("the files through current are not those of the first build")
	}
}

// TestRemovalStopped builds the sample site and a change of it, and then a third site, whose build cannot remove the
// first release entirely. It checks that what is left of the first release is no release: the build returns its result
// with the error, ListReleases does not list it, Rollback refuses it, and the next build removes it. It then builds
// the first site again, which makes its release anew, whole, and does not take the rest for it.
func TestRemovalStopped(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the modes of directories")
	}
	src := openSample(t)
	out, cache := t.TempDir(), t.TempDir()
	a := mustBuild(t, src, out, cache, 4).Release
	filesA := manifest(t, filepath.Join(out, "releases", a))
	b := mustBuild(t, retitled(t, src, "Privacy notice"), out, cache, 4).Release
	makeReadOnly(t, out, filepath.Join(out, "releases", a, "public", "en"))

	// The release that this build publishes and the one of current are kept; a is removed.
	third := retitled(t, src, "Privacy notice 2")
	res, err := Build(t.Context(), buildOptions(third, out, cache, 4))
	pattern := "^" + regexp.QuoteMeta(out+": RemoveAll releases/.tmp-") + "[0-9a-f]{16}: permission denied$"
	if res == nil || !errors.Is(err, fs.ErrPermission) || !regexp.MustCompile(pattern).MatchString(err.Error()) {
		t.Fatalf("Build() = %+v, %v, want a result and an error that matches %s", res, err, pattern)
	}
	c := res.Release
	if c == a || c == b || !res.Changed {
		t.Fatalf("Build() = %+v, want a new release", res)
	}
	if _, err := os.Lstat(filepath.Join(out, "releases", a)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the rest of the release %s is still under its name (%v)", a, err)
	}
	if left := staleNames(t, out); len(left) != 1 {
		t.Errorf("the temporary directories are %q, want the one that holds the rest of the release %s", left, a)
	}
	checkListed(t, "a removal that stopped", out, c, b, c)
	before := tree(t, out)
	want := filepath.Join(out, "releases", a) + ": no such release"
	if changed, err := Rollback(out, a); changed || err == nil || err.Error() != want {
		t.Errorf("Rollback() to the rest of a release = %v, %v, want the error %q", changed, err, want)
	}
	checkTree(t, "Rollback() to the rest of a release", out, before)

	writable(out)
	if again := mustBuild(t, third, out, cache, 4); again.Release != c || again.Changed {
		t.Errorf("Build() of the same site = %+v, want %s unchanged", again, c)
	}
	checkReleases(t, "the rest removed", out, c, b, c)

	if again := mustBuild(t, src, out, cache, 4); again.Release != a || !again.Changed {
		t.Errorf("Build() of the first site again = %+v, want %s, changed", again, a)
	}
	checkReleases(t, "the first site again", out, a, a, c)
	current := filepath.Join(out, "current")
	if _, err := os.Stat(filepath.Join(current, publicDir, "index.html")); err != nil {
		t.Errorf("the front page of the release %s: %v", a, err)
	}
	if manifest(t, current) != filesA {
		t.Errorf("the files through current are not those of the first build")
	}
}
