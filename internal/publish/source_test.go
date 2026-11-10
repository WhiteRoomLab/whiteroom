package publish

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/build"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
	"github.com/WhiteRoomLab/whiteroom/internal/sitedir"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

const (
	sampleDir = "../build/testdata/site"    // the sample site of internal/build, a site directory
	themeDir  = "../theme/testdata/example" // its theme, which the site does not copy
	goldenDir = "../build/testdata/out"     // the golden files of the release of the sample site
	userID    = "usr_01JB8T7H4MA2C5E8G1J4K7N3P6"
)

var siteKinds = []content.RefKind{
	content.RefEntry, content.RefTerm, content.RefMedia, content.RefAuthor, content.RefMenu, content.RefForm,
	content.RefRedirect,
}

// siteDir is a site directory with the example theme, which the directory does not copy.
type siteDir struct {
	*sitedir.Dir
	theme fs.FS
}

func (s siteDir) Theme() (fs.FS, error) { return s.theme, nil }

// exampleTheme returns the files of the example theme, which the test closes.
func exampleTheme(t *testing.T) fs.FS {
	t.Helper()
	root, err := os.OpenRoot(themeDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root.FS()
}

// openSite opens the site directory dir with the example theme, which the test closes.
func openSite(t *testing.T, dir string) siteDir {
	t.Helper()
	d, err := sitedir.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return siteDir{Dir: d, theme: exampleTheme(t)}
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Create(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// importSample returns a new store that has the sample site, imported.
func importSample(t *testing.T) *store.Store {
	t.Helper()
	s := newStore(t)
	if _, err := edit.Import(t.Context(), s, openSite(t, sampleDir), userID); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	return s
}

func schemaFile(t *testing.T) content.SiteFile {
	t.Helper()
	return content.SiteFile{Name: "site/schema.yaml", Data: readFile(t, filepath.Join(sampleDir, "site", "schema.yaml"))}
}

// snapshotOf takes a snapshot of s with the schema file of the sample site and the example theme.
func snapshotOf(t *testing.T, s *store.Store) *Source {
	t.Helper()
	src, err := Snapshot(t.Context(), s, schemaFile(t), exampleTheme(t))
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	return src
}

func readFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// buildOptions returns the options to build src into new temporary directories.
func buildOptions(t *testing.T, src build.Source) build.Options {
	t.Helper()
	return build.Options{Source: src, Out: t.TempDir(), CacheDir: t.TempDir(), Workers: 4}
}

// buildSite builds src and returns the result and the directory of the release.
func buildSite(t *testing.T, src build.Source) (*build.Result, string) {
	t.Helper()
	opts := buildOptions(t, src)
	res, err := build.Build(t.Context(), opts)
	if err != nil {
		t.Fatalf("Build() error:\n%v", err)
	}
	return res, filepath.Join(opts.Out, "releases", res.Release)
}

// buildError builds src, which must fail, and returns the text of its error.
func buildError(t *testing.T, src build.Source) string {
	t.Helper()
	res, err := build.Build(t.Context(), buildOptions(t, src))
	if err == nil {
		t.Fatalf("Build() = %+v, want an error", res)
	}
	return err.Error()
}

// manifest returns a line for each file of the release in dir, in the order of their paths: its path in the release,
// its size and the hex digits of its SHA-256.
func manifest(t *testing.T, dir string) string {
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
	slices.Sort(names)
	var b strings.Builder
	for _, name := range names {
		data := readFile(t, filepath.Join(dir, filepath.FromSlash(name)))
		fmt.Fprintf(&b, "%s %d %x\n", name, len(data), sha256.Sum256(data))
	}
	return b.String()
}

// warnings returns the warnings of res, one on each line.
func warnings(res *build.Result) string {
	var b strings.Builder
	for _, w := range res.Warnings {
		b.WriteString(w.Error() + "\n")
	}
	return b.String()
}

// firstDiff returns the first line in which got and want, two different texts, differ, with its number.
func firstDiff(got, want string) string {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := range min(len(g), len(w)) {
		if g[i] != w[i] {
			return fmt.Sprintf("line %d is %q, want %q", i+1, g[i], w[i])
		}
	}
	return fmt.Sprintf("%d lines, want %d", len(g), len(w))
}

// sameFile reports whether the files got and want have the same name and bytes.
func sameFile(got, want content.SiteFile) bool {
	return got.Name == want.Name && bytes.Equal(got.Data, want.Data)
}

func describe(f content.SiteFile) string { return fmt.Sprintf("%s (%d bytes)", f.Name, len(f.Data)) }

// diffFiles tells how got, the files of the documents of a kind by ID, differ from want, or returns "".
func diffFiles(got, want map[string]content.SiteFile) string {
	for _, id := range slices.Sorted(maps.Keys(want)) {
		switch g, ok := got[id]; {
		case !ok:
			return id + " is missing"
		case !sameFile(g, want[id]):
			return fmt.Sprintf("%s is %s, want %s", id, describe(g), describe(want[id]))
		}
	}
	for _, id := range slices.Sorted(maps.Keys(got)) {
		if _, ok := want[id]; !ok {
			return id + " is not wanted"
		}
	}
	return ""
}

// replaceOnce returns s with the one occurrence of old replaced with new; the test fails if s has none or more.
func replaceOnce(t *testing.T, s, old, new string) string {
	t.Helper()
	if n := strings.Count(s, old); n != 1 {
		t.Fatalf("%q occurs %d times in %q, want once", old, n, s)
	}
	return strings.Replace(s, old, new, 1)
}

// testID returns the ID of an entity of kind, numbered n, that the sample site does not have.
func testID(kind content.RefKind, n int) string {
	return content.FormatID(kind, [16]byte{0: 0x01, 1: 0xa0, 15: byte(n)})
}

// put puts the document of kind with id into s with the store's own API, which checks nothing against the site.
func put(t *testing.T, s *store.Store, kind content.RefKind, id, data string) {
	t.Helper()
	err := s.Update(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Put(kind, id, []byte(data), userID)
		return err
	})
	if err != nil {
		t.Fatalf("Put(%q, %q) = %v", kind, id, err)
	}
}

// readOriginal returns the bytes of the original with hash that src opens.
func readOriginal(t *testing.T, src build.Source, hash string) []byte {
	t.Helper()
	f, err := src.OpenMedia(hash)
	if err != nil {
		t.Fatalf("OpenMedia(%q) error = %v", hash, err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("reading the original %q: %v", hash, err)
	}
	return data
}

// publishedRevisions returns the IDs of the revisions that entries, the files of entries, publish.
func publishedRevisions(t *testing.T, entries map[string]content.SiteFile) []string {
	t.Helper()
	var ids []string
	for _, id := range slices.Sorted(maps.Keys(entries)) {
		var e content.Entry
		if err := e.UnmarshalJSON(entries[id].Data); err != nil {
			t.Fatal(err)
		}
		if e.PublishedRevision != "" {
			ids = append(ids, e.PublishedRevision)
		}
	}
	return ids
}

func originalHashes(t *testing.T, media map[string]content.SiteFile) []string {
	t.Helper()
	var hashes []string
	for _, id := range slices.Sorted(maps.Keys(media)) {
		var m content.Media
		if err := m.UnmarshalJSON(media[id].Data); err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, m.Hash)
	}
	return hashes
}

// TestSnapshotSampleSite checks that the sample site of the build, imported into a store, builds from its snapshot to
// the release that the build of the site directory makes: the same release, the files of the golden manifest and the
// warnings of the golden file.
func TestSnapshotSampleSite(t *testing.T) {
	t.Parallel()
	fromStore, storeRelease := buildSite(t, snapshotOf(t, importSample(t)))
	fromDir, dirRelease := buildSite(t, openSite(t, sampleDir))

	if fromStore.Release != fromDir.Release {
		t.Errorf("the release of the store is %s, want %s, that of the site directory", fromStore.Release, fromDir.Release)
	}
	if fromStore.Pages != fromDir.Pages || fromStore.Files != fromDir.Files || fromStore.Changed != fromDir.Changed {
		t.Errorf("Build() of the store = %+v, want the pages, the files and Changed of %+v", fromStore, fromDir)
	}
	wantManifest := string(readFile(t, filepath.Join(goldenDir, "manifest.txt")))
	for _, tt := range []struct{ what, release string }{{"the store", storeRelease}, {"the site directory", dirRelease}} {
		if got := manifest(t, tt.release); got != wantManifest {
			t.Errorf("the manifest of the release of %s differs from the golden file: %s", tt.what, firstDiff(got, wantManifest))
		}
	}
	wantWarnings := string(readFile(t, filepath.Join(goldenDir, "warnings.txt")))
	if wantWarnings == "" {
		t.Fatal("the golden warnings are empty, so that the comparison proves nothing")
	}
	if got := warnings(fromStore); got != wantWarnings {
		t.Errorf("the warnings of the store differ from the golden file: %s", firstDiff(got, wantWarnings))
	}
}

// TestSnapshotDocuments checks that a snapshot of the imported sample site gives the files of the site directory: the
// settings, the documents of every kind, the published revisions and the originals.
func TestSnapshotDocuments(t *testing.T) {
	t.Parallel()
	src := snapshotOf(t, importSample(t))
	dir := openSite(t, sampleDir)

	settings, schema, err := src.Settings()
	if err != nil {
		t.Fatalf("Settings() error = %v", err)
	}
	wantSettings, wantSchema, err := dir.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if !sameFile(settings, wantSettings) || !sameFile(schema, wantSchema) {
		t.Errorf("Settings() = %s, %s; want the files %s and %s", describe(settings), describe(schema), describe(wantSettings),
			describe(wantSchema))
	}

	want := make(map[content.RefKind]map[string]content.SiteFile)
	for _, kind := range siteKinds {
		got, err := src.Documents(kind)
		if err != nil {
			t.Fatalf("Documents(%q) error = %v", kind, err)
		}
		if want[kind], err = dir.Documents(kind); err != nil {
			t.Fatal(err)
		}
		if len(want[kind]) == 0 {
			t.Fatalf("the sample site has no %s", kind)
		}
		if d := diffFiles(got, want[kind]); d != "" {
			t.Errorf("Documents(%q): %s", kind, d)
		}
		// A new map each time.
		clear(got)
		if again, err := src.Documents(kind); err != nil || len(again) != len(want[kind]) {
			t.Errorf("Documents(%q) again = %d files, %v; want %d files", kind, len(again), err, len(want[kind]))
		}
	}

	revisions := publishedRevisions(t, want[content.RefEntry])
	hashes := originalHashes(t, want[content.RefMedia])
	if len(revisions) == 0 || len(hashes) == 0 {
		t.Fatalf("the sample site has %d published revisions and %d originals", len(revisions), len(hashes))
	}
	for _, id := range revisions {
		wantRevision, err := dir.Revision(id)
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if got, err := src.Revision(id); err != nil || !sameFile(got, wantRevision) {
				t.Errorf("Revision(%q) = %s, %v; want the file %s", id, describe(got), err, describe(wantRevision))
			}
		}
	}
	for _, hash := range hashes {
		got, wantOriginal := readOriginal(t, src, hash), readOriginal(t, dir, hash)
		if len(wantOriginal) == 0 || !bytes.Equal(got, wantOriginal) {
			t.Errorf("OpenMedia(%q) has %d bytes, want the %d bytes of the file", hash, len(got), len(wantOriginal))
		}
	}
	th, err := src.Theme()
	if err != nil {
		t.Fatalf("Theme() error = %v", err)
	}
	if got, err := fs.ReadFile(th, "theme.yaml"); err != nil || !bytes.Equal(got, readFile(t, filepath.Join(themeDir, "theme.yaml"))) {
		t.Errorf("Theme() has theme.yaml with %d bytes, %v; want the file of the example theme", len(got), err)
	}
}

// TestSnapshotFixed checks that what is saved after a snapshot does not change it: the settings and the documents that
// it returns, and the site that it builds.
func TestSnapshotFixed(t *testing.T) {
	t.Parallel()
	s := importSample(t)
	old := snapshotOf(t, s)
	oldSettings, _, err := old.Settings()
	if err != nil {
		t.Fatal(err)
	}
	oldTerms, err := old.Documents(content.RefTerm)
	if err != nil {
		t.Fatal(err)
	}

	renamed := replaceOnce(t, string(oldTerms[newsTerm].Data), `"name": "お知らせ"`, `"name": "Latest news"`)
	extraID := testID(content.RefTerm, 1)
	extra := fmt.Sprintf(`{
  "schemaVersion": 1,
  "id": %q,
  "taxonomy": "category",
  "language": "ja",
  "translationGroup": %q,
  "name": "Extra",
  "slug": "extra"
}
`, extraID, testID(content.RefTranslationGroup, 1))
	newSettings := replaceOnce(t, string(oldSettings.Data), "Webサイトの企画と制作", "Websites and more")
	put(t, s, content.RefTerm, newsTerm, renamed)
	put(t, s, content.RefTerm, extraID, extra)
	put(t, s, store.SettingsKind, "", newSettings)

	if settings, _, err := old.Settings(); err != nil || !bytes.Equal(settings.Data, oldSettings.Data) {
		t.Errorf("Settings() after a save = %s, %v; want the settings of the snapshot", describe(settings), err)
	}
	if terms, err := old.Documents(content.RefTerm); err != nil || diffFiles(terms, oldTerms) != "" {
		t.Errorf("Documents(%q) after a save: %s, %v; want the terms of the snapshot", content.RefTerm, diffFiles(terms, oldTerms), err)
	}

	fresh := snapshotOf(t, s)
	if settings, _, err := fresh.Settings(); err != nil || string(settings.Data) != newSettings {
		t.Errorf("Settings() of a new snapshot = %s, %v; want the saved settings", describe(settings), err)
	}
	wantTerms := maps.Clone(oldTerms)
	wantTerms[newsTerm] = content.SiteFile{Name: oldTerms[newsTerm].Name, Data: []byte(renamed)}
	wantTerms[extraID] = content.SiteFile{Name: "terms/" + extraID + ".json", Data: []byte(extra)}
	if terms, err := fresh.Documents(content.RefTerm); err != nil || diffFiles(terms, wantTerms) != "" {
		t.Errorf("Documents(%q) of a new snapshot: %s, %v; want the saved terms", content.RefTerm, diffFiles(terms, wantTerms), err)
	}

	oldRes, oldRelease := buildSite(t, old)
	freshRes, freshRelease := buildSite(t, fresh)
	if oldRes.Release == freshRes.Release {
		t.Errorf("the builds of the two snapshots make the release %s", oldRes.Release)
	}
	golden := string(readFile(t, filepath.Join(goldenDir, filepath.FromSlash(newsPage))))
	if got := string(readFile(t, filepath.Join(oldRelease, filepath.FromSlash(newsPage)))); got != golden {
		t.Errorf("the page of the old snapshot differs from the golden file: %s", firstDiff(got, golden))
	}
	got := string(readFile(t, filepath.Join(freshRelease, filepath.FromSlash(newsPage))))
	if want := `<meta property="og:title" content="Latest news">`; !strings.Contains(got, want) {
		t.Errorf("the page of the new snapshot lacks %s", want)
	}
	if strings.Contains(got, `content="お知らせ"`) {
		t.Errorf("the page of the new snapshot has the old name of the term")
	}
}

// TestSnapshotConsistent checks that the documents of every kind in a snapshot are those of one commit: a store whose
// term and redirect are always saved together in one Update, as one pair, never shows a snapshot with two pairs.
func TestSnapshotConsistent(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	termID, redirectID := testID(content.RefTerm, 1), testID(content.RefRedirect, 1)
	save := func(n int) error {
		term := fmt.Sprintf(`{"schemaVersion": 1, "id": %q, "taxonomy": "category", "language": "ja", "translationGroup": %q, `+
			`"name": "n%d", "slug": "n%d"}`, termID, testID(content.RefTranslationGroup, 1), n, n)
		redirect := fmt.Sprintf(`{"schemaVersion": 1, "id": %q, "from": "/n%d/", "to": "/", "status": 301}`, redirectID, n)
		return s.Update(t.Context(), func(tx *store.Tx) error {
			if _, err := tx.Put(content.RefTerm, termID, []byte(term), userID); err != nil {
				return err
			}
			_, err := tx.Put(content.RefRedirect, redirectID, []byte(redirect), userID)
			return err
		})
	}
	if err := save(0); err != nil {
		t.Fatal(err)
	}

	const saves = 60
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for n := 1; n <= saves; n++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := save(n); err != nil {
				t.Errorf("save(%d) error = %v", n, err)
				return
			}
		}
	}()
	defer func() {
		close(stop)
		<-done
	}()
	schema, theme := schemaFile(t), exampleTheme(t)
	snapshots := 0
	for last := false; !last; {
		select {
		case <-done:
			last = true // one more snapshot, of the last save
		default:
		}
		src, err := Snapshot(t.Context(), s, schema, theme)
		if err != nil {
			t.Fatalf("Snapshot() error = %v", err)
		}
		snapshots++
		terms, err := src.Documents(content.RefTerm)
		if err != nil {
			t.Fatal(err)
		}
		redirects, err := src.Documents(content.RefRedirect)
		if err != nil {
			t.Fatal(err)
		}
		var term content.Term
		var redirect content.Redirect
		if err := term.UnmarshalJSON(terms[termID].Data); err != nil {
			t.Fatal(err)
		}
		if err := redirect.UnmarshalJSON(redirects[redirectID].Data); err != nil {
			t.Fatal(err)
		}
		if redirect.From != "/"+term.Slug+"/" {
			t.Fatalf("snapshot %d has the term %q and the redirect from %q, which no commit has", snapshots, term.Slug, redirect.From)
		}
	}
	t.Logf("%d snapshots, %d saves", snapshots, saves)
}

// siteChange is a document that a test puts into a store with its own API and writes into a copy of the site directory.
type siteChange struct {
	kind  content.RefKind
	id    string
	field string // the text of the document that changes, and its replacement
	with  string
}

// TestSnapshotProblems checks that a store whose documents have problems builds to the errors that a site directory
// with the same files gives, with the same names of files, lines and columns.
func TestSnapshotProblems(t *testing.T) {
	t.Parallel()
	const page = "ent_01JBD1Y2Z3A4B5C6D7E8F9G0H1" // a published page
	const missing = "rev_01JBD1Y2Z3A4B5C6D7E8F9G0H2"
	const term = "trm_01JBA4C7F3H6K9N2Q5S8V1X4Z7" // press-release, a category
	changes := []siteChange{
		{content.RefEntry, page, `"publishedRevision": "rev_01JBD1Y2Z3A4B5C6D7E8F9G0H1"`, `"publishedRevision": "` + missing + `"`},
		{content.RefTerm, term, `"slug": "press-release"`, `"slug": "Press-Release"`},
	}

	s := importSample(t)
	dir := filepath.Join(t.TempDir(), "site")
	if err := os.CopyFS(dir, os.DirFS(sampleDir)); err != nil {
		t.Fatal(err)
	}
	for _, c := range changes {
		name := content.DocumentDir(c.kind) + "/" + c.id + ".json"
		data := replaceOnce(t, string(readFile(t, filepath.Join(sampleDir, name))), c.field, c.with)
		put(t, s, c.kind, c.id, data)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, want := buildError(t, snapshotOf(t, s)), buildError(t, openSite(t, dir))
	if got != want {
		t.Errorf("the build of the store fails with:\n%s\nwant the errors of the site directory:\n%s", got, want)
	}
	lines := strings.Split(want, "\n")
	for _, re := range []string{
		`^entries/` + page + `\.json:\d+:\d+: /publishedRevision: revision "` + missing + `" does not exist$`,
		`^terms/` + term + `\.json:\d+:\d+: /slug: `,
	} {
		if !slices.ContainsFunc(lines, regexp.MustCompile(re).MatchString) {
			t.Errorf("the errors of the site directory have no line that matches %s:\n%s", re, want)
		}
	}
}

// TestSnapshotErrors checks the errors of a snapshot of a store without documents, and the errors of the revisions and
// the originals that it does not have.
func TestSnapshotErrors(t *testing.T) {
	t.Parallel()
	src := snapshotOf(t, newStore(t))

	const notFound = "site/settings.json: not found"
	if _, _, err := src.Settings(); !errors.Is(err, fs.ErrNotExist) || err.Error() != notFound {
		t.Errorf("Settings() error = %v, want %q, which wraps fs.ErrNotExist", err, notFound)
	}
	res, err := build.Build(t.Context(), buildOptions(t, src))
	if res != nil || !errors.Is(err, fs.ErrNotExist) || err.Error() != notFound {
		t.Errorf("Build() = %+v, %v; want the error %q, which wraps fs.ErrNotExist", res, err, notFound)
	}

	for _, kind := range siteKinds {
		if docs, err := src.Documents(kind); err != nil || docs == nil || len(docs) != 0 {
			t.Errorf("Documents(%q) = %v, %v; want no documents", kind, docs, err)
		}
	}
	for _, kind := range []content.RefKind{store.SettingsKind, "page", content.RefRevision, content.RefUser, ""} {
		if docs, err := src.Documents(kind); err == nil || docs != nil || err.Error() != fmt.Sprintf("unsupported kind %q", kind) {
			t.Errorf("Documents(%q) = %v, %v; want the error unsupported kind", kind, docs, err)
		}
	}

	if f, err := src.Revision(testID(content.RefRevision, 1)); !errors.Is(err, fs.ErrNotExist) || f.Name != "" || f.Data != nil {
		t.Errorf("Revision() of a missing revision = %s, %v; want an error that wraps fs.ErrNotExist", describe(f), err)
	}
	if f, err := src.Revision("rev_x"); err == nil || errors.Is(err, fs.ErrNotExist) || f.Data != nil {
		t.Errorf("Revision() of an invalid ID = %s, %v; want an error that is not fs.ErrNotExist", describe(f), err)
	}
	sum := sha256.Sum256([]byte("no such original"))
	if f, err := src.OpenMedia("sha256:" + hex.EncodeToString(sum[:])); !errors.Is(err, fs.ErrNotExist) || f != nil {
		t.Errorf("OpenMedia() of a missing original = %v, %v; want an error that wraps fs.ErrNotExist", f, err)
	}
	if f, err := src.OpenMedia("sha256:x"); err == nil || errors.Is(err, fs.ErrNotExist) || f != nil {
		t.Errorf("OpenMedia() of an invalid hash = %v, %v; want an error that is not fs.ErrNotExist", f, err)
	}
}

// TestSnapshotCanceled checks that Snapshot fails if its context has ended, without a Source.
func TestSnapshotCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	src, err := Snapshot(ctx, newStore(t), schemaFile(t), exampleTheme(t))
	if src != nil || !errors.Is(err, context.Canceled) {
		t.Errorf("Snapshot() = %v, %v; want the error %v", src, err, context.Canceled)
	}
}

// TestSnapshotConcurrent reads a snapshot from several goroutines at once, the revisions and the originals as a build
// does; run with -race.
func TestSnapshotConcurrent(t *testing.T) {
	t.Parallel()
	src := snapshotOf(t, importSample(t))
	dir := openSite(t, sampleDir)

	entries, err := src.Documents(content.RefEntry)
	if err != nil {
		t.Fatal(err)
	}
	media, err := src.Documents(content.RefMedia)
	if err != nil {
		t.Fatal(err)
	}
	revisions := make(map[string]content.SiteFile) // the files of the published revisions, by ID
	for _, id := range publishedRevisions(t, entries) {
		if revisions[id], err = dir.Revision(id); err != nil {
			t.Fatal(err)
		}
	}
	originals := make(map[string][]byte) // by hash
	for _, hash := range originalHashes(t, media) {
		originals[hash] = readOriginal(t, dir, hash)
	}
	if len(revisions) == 0 || len(originals) == 0 {
		t.Fatalf("the sample site has %d published revisions and %d originals", len(revisions), len(originals))
	}

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for _, id := range slices.Sorted(maps.Keys(revisions)) {
				if got, err := src.Revision(id); err != nil || !sameFile(got, revisions[id]) {
					t.Errorf("goroutine %d: Revision(%q) = %s, %v; want the file %s", i, id, describe(got), err, describe(revisions[id]))
				}
			}
			for _, hash := range slices.Sorted(maps.Keys(originals)) {
				f, err := src.OpenMedia(hash)
				if err != nil {
					t.Errorf("goroutine %d: OpenMedia(%q) error = %v", i, hash, err)
					continue
				}
				got, err := io.ReadAll(f)
				_ = f.Close()
				if err != nil || !bytes.Equal(got, originals[hash]) {
					t.Errorf("goroutine %d: the original %q has %d bytes, %v; want %d", i, hash, len(got), err, len(originals[hash]))
				}
			}
			if _, _, err := src.Settings(); err != nil {
				t.Errorf("goroutine %d: Settings() error = %v", i, err)
			}
			if docs, err := src.Documents(content.RefEntry); err != nil || diffFiles(docs, entries) != "" {
				t.Errorf("goroutine %d: Documents(%q): %s, %v", i, content.RefEntry, diffFiles(docs, entries), err)
			}
		})
	}
	wg.Wait()
}
