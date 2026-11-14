package edit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/build"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/mediafile"
	"github.com/WhiteRoomLab/whiteroom/internal/sitedir"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// A Source is a build.Source.
var (
	_ Source       = build.Source(nil)
	_ build.Source = Source(nil)
)

// openSite opens the site directory dir, with the example theme, as the Source of an import; the test closes it.
func openSite(t *testing.T, dir string) source {
	t.Helper()
	return openSiteWith(t, dir, os.DirFS(themeDir))
}

func openSiteWith(t *testing.T, dir string, th fs.FS) source {
	t.Helper()
	d, err := sitedir.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return source{Dir: d, theme: th}
}

func openSample(t *testing.T) source {
	t.Helper()
	return openSite(t, sampleDir)
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

// importSite imports the site directory dir, with the example theme, into s as userID.
func importSite(t *testing.T, s *store.Store, dir string) (content.Errors, error) {
	t.Helper()
	return Import(t.Context(), s, openSite(t, dir), userID)
}

// newImported returns a store that has the sample site, imported, and an Editor for it as newSample makes one.
func newImported(t *testing.T) (*Editor, *store.Store) {
	t.Helper()
	s := newStore(t)
	if _, err := Import(t.Context(), s, openSample(t), userID); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	return testEditor(t, s), s
}

// indexState is what the index of s answers: the summaries of every kind and the referrers of every ID that documents
// refer to.
type indexState struct {
	Summaries map[content.RefKind][]store.Summary
	Referrers map[string][]store.Referrer // by the kind and the ID
	Last      int
}

func readIndex(t *testing.T, tx *store.Tx) indexState {
	t.Helper()
	st := indexState{Summaries: make(map[content.RefKind][]store.Summary), Referrers: make(map[string][]store.Referrer)}
	for _, kind := range importKinds {
		sums, err := tx.Summaries(kind)
		if err != nil {
			t.Fatalf("Summaries(%q) = %v", kind, err)
		}
		st.Summaries[kind] = sums
		if !refKinds[kind] {
			continue
		}
		for _, sum := range sums {
			if st.Referrers[string(kind)+" "+sum.ID], err = tx.Referrers(kind, sum.ID); err != nil {
				t.Fatalf("Referrers(%q, %q) = %v", kind, sum.ID, err)
			}
		}
	}
	var err error
	if st.Last, err = tx.LastNumber(); err != nil {
		t.Fatalf("LastNumber() = %v", err)
	}
	return st
}

func indexOf(t *testing.T, s *store.Store) indexState {
	t.Helper()
	var st indexState
	err := s.View(t.Context(), func(tx *store.Tx) error {
		st = readIndex(t, tx)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func sameIndex(t *testing.T, what string, got, want indexState) {
	t.Helper()
	if got.Last != want.Last {
		t.Errorf("%s: LastNumber() = %d, want %d", what, got.Last, want.Last)
	}
	for _, kind := range importKinds {
		if !reflect.DeepEqual(got.Summaries[kind], want.Summaries[kind]) {
			t.Errorf("%s: the summaries of %s differ:\n%s\nwant\n%s", what, kind, describeSummaries(got.Summaries[kind]),
				describeSummaries(want.Summaries[kind]))
		}
	}
	for key, refs := range want.Referrers {
		if !slices.Equal(got.Referrers[key], refs) {
			t.Errorf("%s: the referrers of %s = %v, want %v", what, key, got.Referrers[key], refs)
		}
	}
	if len(got.Referrers) != len(want.Referrers) {
		t.Errorf("%s: %d IDs have referrers, want %d", what, len(got.Referrers), len(want.Referrers))
	}
}

func describeSummaries(sums []store.Summary) string {
	var lines []string
	for _, sum := range sums {
		lines = append(lines, string(sum.Kind)+" "+sum.ID+" latest "+sum.Latest)
	}
	return strings.Join(lines, "\n")
}

// sampleRedirectWarnings returns the lines of the warnings of the build of the sample site that are about the redirects that
// are left out because they go to entries that are not published, which are the warnings of the checks of the site that
// the save operations run.
func sampleRedirectWarnings(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile("../build/testdata/out/warnings.txt")
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(l, "redirects/") && strings.HasSuffix(l, " is not published; the redirect is left out") {
			want = append(want, l)
		}
	}
	return want
}

// TestImportSampleSite imports the sample site of the build into a new store, and checks that the store has the bytes of
// its files, the originals of its media and the numbers of its entries, that its index is the one that RebuildIndex makes and
// the one that the store's own API makes of the same documents, and that a second import is refused.
func TestImportSampleSite(t *testing.T) {
	t.Parallel()
	src := openSample(t)
	s := newStore(t)
	warnings, err := Import(t.Context(), s, src, userID)
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if want := sampleRedirectWarnings(t); len(want) == 0 || !slices.Equal(lines(warnings), want) {
		t.Errorf("Import() warnings:\n%s\nwant\n%s", strings.Join(lines(warnings), "\n"), strings.Join(want, "\n"))
	}

	// Every document, the settings and the revisions that entries publish are stored with the bytes of their files.
	settings, _, err := src.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, s, store.SettingsKind, ""); !bytes.Equal(got.Data, settings.Data) || got.SavedBy != userID {
		t.Errorf("the settings are %q saved by %s, want the file site/settings.json and %s", got.Data, got.SavedBy, userID)
	}
	counts := make(map[content.RefKind]int)
	published := make(map[string]string) // the entries by the revisions that they publish
	largest := 0
	var media []content.Media
	for _, kind := range importKinds {
		files, err := src.Documents(kind)
		if err != nil {
			t.Fatal(err)
		}
		for id, f := range files {
			counts[kind]++
			got := read(t, s, kind, id)
			if !bytes.Equal(got.Data, f.Data) || got.SavedBy != userID {
				t.Errorf("%s is %q saved by %s, want its file and %s", f.Name, got.Data, got.SavedBy, userID)
			}
			if kind == content.RefEntry {
				e := decodeEntry(t, f.Data)
				largest = max(largest, e.Number)
				if e.PublishedRevision != "" {
					published[e.PublishedRevision] = id
				}
			}
			if kind == content.RefMedia {
				var m content.Media
				if err := m.UnmarshalJSON(f.Data); err != nil {
					t.Fatal(err)
				}
				media = append(media, m)
			}
		}
	}
	if len(published) == 0 || len(media) == 0 || len(counts) != len(importKinds) {
		t.Fatalf("the sample site has %d published revisions, %d media and documents of %d kinds, want all of the %d kinds",
			len(published), len(media), len(counts), len(importKinds))
	}
	for id, entry := range published {
		f, err := src.Revision(id)
		if err != nil {
			t.Fatal(err)
		}
		err = s.View(t.Context(), func(tx *store.Tx) error {
			got, err := tx.Revision(id)
			if err != nil || got.Entry != entry || !bytes.Equal(got.Data, f.Data) || got.SavedBy != userID {
				t.Errorf("Revision(%q) = %+v, %v; want the file %s of %s saved by %s", id, got, err, f.Name, entry, userID)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// Every original, with its hash and its size.
	if len(media) != counts[content.RefMedia] {
		t.Fatalf("%d media documents, want %d", len(media), counts[content.RefMedia])
	}
	for _, m := range media {
		f, err := src.OpenMedia(m.Hash)
		if err != nil {
			t.Fatal(err)
		}
		file, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if got := readOriginal(t, s, m.Hash); !bytes.Equal(got, file) || len(got) != m.Size || fileHash(got) != m.Hash {
			t.Errorf("the original of %s has %d bytes and the hash %s, want %d bytes and %s", m.ID, len(got), fileHash(got), m.Size, m.Hash)
		}
	}

	// The index: the numbers, the counts, what RebuildIndex makes of the documents, and what the store's own API makes of
	// them.
	got := indexOf(t, s)
	if got.Last != largest || largest < 2 {
		t.Errorf("LastNumber() = %d, want %d, the largest number of the entries", got.Last, largest)
	}
	for kind, n := range counts {
		if len(got.Summaries[kind]) != n {
			t.Errorf("%d summaries of %s, want %d", len(got.Summaries[kind]), kind, n)
		}
	}
	errRollBack := errors.New("roll back")
	err = s.Update(t.Context(), func(tx *store.Tx) error {
		if err := tx.RebuildIndex(); err != nil {
			return err
		}
		sameIndex(t, "after RebuildIndex", readIndex(t, tx), got)
		return errRollBack
	})
	if !errors.Is(err, errRollBack) {
		t.Fatalf("Update() = %v, want the error that rolls it back", err)
	}
	_, other := newSample(t)
	sameIndex(t, "the store of putSite", indexOf(t, other), got)

	// A second import is refused, and changes nothing.
	before := snapshot(t, s)
	again, err := Import(t.Context(), s, src, userID)
	if err == nil || err.Error() != errNotEmpty.Error() || again != nil || snapshot(t, s) != before {
		t.Errorf("the second Import() = %v, %v; want the error %q and no change", again, err, errNotEmpty)
	}
	sameIndex(t, "after the second Import", indexOf(t, s), got)
}

// storedFiles returns the bytes of every file of a site directory that s has: the settings, the current documents and the
// revisions of the entries, by their names.
func storedFiles(t *testing.T, s *store.Store) map[string]string {
	t.Helper()
	files := make(map[string]string)
	err := s.View(t.Context(), func(tx *store.Tx) error {
		settings, err := tx.Document(store.SettingsKind, "")
		if err != nil {
			return err
		}
		files[settingsName] = string(settings.Data)
		for _, kind := range importKinds {
			docs, err := tx.Documents(kind)
			if err != nil {
				return err
			}
			for _, d := range docs {
				files[docName(kind, d.ID)] = string(d.Data)
				if kind != content.RefEntry {
					continue
				}
				revs, err := tx.Revisions(d.ID)
				if err != nil {
					return err
				}
				for _, r := range revs {
					rev, err := tx.Revision(r.ID)
					if err != nil {
						return err
					}
					files[revisionName(r.ID)] = string(rev.Data)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// siteFiles returns the bytes of every file of the site directory dir that an Import stores: the settings, the documents and
// the revisions that the published entries publish and the scheduled entries schedule, by their names.
func siteFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	src := openSite(t, dir)
	settings, _, err := src.Settings()
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{settings.Name: string(settings.Data)}
	for _, kind := range importKinds {
		docs, err := src.Documents(kind)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range docs {
			files[f.Name] = string(f.Data)
			if kind != content.RefEntry {
				continue
			}
			e := decodeEntry(t, f.Data)
			for status, id := range map[content.Status]string{content.StatusPublished: e.PublishedRevision, content.StatusScheduled: e.ScheduledRevision} {
				if e.Status != status {
					continue
				}
				rev, err := src.Revision(id)
				if err != nil {
					t.Fatal(err)
				}
				files[rev.Name] = string(rev.Data)
			}
		}
	}
	return files
}

func sameFiles(t *testing.T, got, want map[string]string) {
	t.Helper()
	for _, name := range slices.Sorted(maps.Keys(want)) {
		if g, ok := got[name]; !ok {
			t.Errorf("%s is not in the store", name)
		} else if g != want[name] {
			t.Errorf("%s is\n%s\nwant\n%s", name, g, want[name])
		}
	}
	for _, name := range slices.Sorted(maps.Keys(got)) {
		if _, ok := want[name]; !ok {
			t.Errorf("the store has %s, which it should not have", name)
		}
	}
}

// spySource passes the reads of a Source on, and records the names of the methods that it is asked to call. It calls
// the hook of a method before it, and fails it with the error that fails has for it.
type spySource struct {
	Source
	calls []string
	hooks map[string]func()
	fails map[string]error
}

func newSpy(t *testing.T, dir string) *spySource {
	t.Helper()
	return &spySource{Source: openSite(t, dir)}
}

func (s *spySource) enter(name string) error {
	s.calls = append(s.calls, name)
	if hook := s.hooks[name]; hook != nil {
		hook()
	}
	return s.fails[name]
}

func (s *spySource) Settings() (settings, schema content.SiteFile, err error) {
	if err := s.enter("Settings"); err != nil {
		return content.SiteFile{}, content.SiteFile{}, err
	}
	return s.Source.Settings()
}

func (s *spySource) Documents(kind content.RefKind) (map[string]content.SiteFile, error) {
	if err := s.enter("Documents"); err != nil {
		return nil, err
	}
	return s.Source.Documents(kind)
}

func (s *spySource) Revision(id string) (content.SiteFile, error) {
	if err := s.enter("Revision"); err != nil {
		return content.SiteFile{}, err
	}
	return s.Source.Revision(id)
}

func (s *spySource) OpenMedia(hash string) (fs.File, error) {
	if err := s.enter("OpenMedia"); err != nil {
		return nil, err
	}
	return s.Source.OpenMedia(hash)
}

func (s *spySource) Theme() (fs.FS, error) {
	if err := s.enter("Theme"); err != nil {
		return nil, err
	}
	return s.Source.Theme()
}

// importRefuses imports the site directory dir into a new store, which it must refuse with the problems want, without
// their positions, and stores no document. If buildAccepts, the build accepts the site: the Import is stricter.
func importRefuses(t *testing.T, dir string, buildAccepts bool, want ...string) {
	t.Helper()
	if buildAccepts {
		if got := buildErrors(t, dir); len(got) != 0 {
			t.Fatalf("the build refuses the site:\n%s", strings.Join(got, "\n"))
		}
	}
	s := newStore(t)
	warnings, err := importSite(t, s, dir)
	got := stripPositions(problemLines(t, err))
	if !slices.Equal(got, want) || warnings != nil {
		t.Errorf("the problems of Import() (with warnings %v):\n%s\nwant\n%s", warnings, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	checkNoImport(t, s)
}

// An original of the sample site by the ID of its medium: the path of its file in a site directory, and the file.
func sampleOriginal(t *testing.T, id string) (name string, file []byte) {
	t.Helper()
	m, file := sampleMedium(t, id)
	return "files/" + strings.TrimPrefix(m.Hash, "sha256:"), file
}

// TestImportErrors imports copies of the sample site that the build refuses: the Import fails with the lines of the
// build's error where the build and the Import say the same of the problem, in the stages that they have in common; and
// with the problems of what only the Import checks, which are the originals of all the media, the entries that are not
// published, and the other texts of the problems of images.
func TestImportErrors(t *testing.T) {
	t.Parallel()
	notPDF := []byte("this is not a PDF file")
	pdfName, pdfFile := sampleOriginal(t, pdfMedium)
	pngName, pngFile := sampleOriginal(t, pngMedium)
	logoName, _ := sampleOriginal(t, logoMedium)
	jpegName, _ := sampleOriginal(t, jpegMedium)
	pdfDoc, pngDoc, jpegDoc := docName(content.RefMedia, pdfMedium), docName(content.RefMedia, pngMedium), docName(content.RefMedia, jpegMedium)
	flipped := func(file []byte) []byte {
		out := slices.Clone(file)
		out[len(out)/2] ^= 1
		return out
	}

	// brokenTheme is the example theme without the template of lists.
	brokenTheme := func(t *testing.T) fs.FS {
		broken := filepath.Join(t.TempDir(), "theme")
		if err := os.CopyFS(broken, os.DirFS(themeDir)); err != nil {
			t.Fatal(err)
		}
		removeSite(t, broken, "templates/list.html")
		return os.DirFS(broken)
	}

	// The problems that the build finds as well, by the same lines.
	for _, tt := range []struct {
		name   string
		msg    string // in a line of the error
		theme  func(t *testing.T) fs.FS
		change func(t *testing.T, dir string)
	}{
		{name: "a site without settings", msg: "site/settings.json: missing file", change: func(t *testing.T, dir string) {
			removeSite(t, dir, settingsName)
		}},
		{name: "broken JSON in a term", msg: "unexpected end of JSON input", change: func(t *testing.T, dir string) {
			writeSite(t, dir, newsName, "{")
		}},
		{name: "a document with the ID of another", msg: "differs from the ID of the document", change: func(t *testing.T, dir string) {
			changeSite(t, dir, newsName, `"id": "`+newsID+`"`, `"id": "`+pressTerm+`"`)
		}},
		{name: "a document with a name that is not an ID", msg: "invalid name: use <ID>.json", change: func(t *testing.T, dir string) {
			writeSite(t, dir, "terms/news.json", "{}")
		}},
		{name: "a time zone that does not exist", msg: "/timeZone: unknown time zone", change: func(t *testing.T, dir string) {
			changeSite(t, dir, settingsName, `"Asia/Tokyo"`, `"Asia/Nowhere"`)
		}},
		// The problems of the settings and of the schema file are in the order of the validation, not sorted by file.
		{name: "settings and a schema file that do not validate", msg: `/types/works/taxonomies/0: unknown taxonomy "works_bogus"`, change: func(t *testing.T, dir string) {
			changeSite(t, dir, settingsName, `"Asia/Tokyo"`, `"Asia/Nowhere"`)
			changeSite(t, dir, "site/schema.yaml", "taxonomies: [works_category]", "taxonomies: [works_bogus]")
		}},
		// The settings are validated with the lookup of the documents that have been read.
		{name: "settings that refer to a medium that does not exist", msg: `/logo: media "med_01JBG3N6V3X6Z9C2E5G8J1K4M8" does not exist`, change: func(t *testing.T, dir string) {
			changeSite(t, dir, settingsName, `"logo": "`+logoMedium+`"`, `"logo": "med_01JBG3N6V3X6Z9C2E5G8J1K4M8"`)
		}},
		{name: "a theme that does not load", msg: "theme/templates/list.html: missing file", theme: brokenTheme},
		// The problems of the settings come before those of the theme, each in its own order.
		{name: "settings and a theme that do not load", msg: "theme/templates/list.html: missing file", change: func(t *testing.T, dir string) {
			changeSite(t, dir, settingsName, `"Asia/Tokyo"`, `"Asia/Nowhere"`)
		}, theme: brokenTheme},
		{name: "a published revision that does not validate", msg: "/slug: character ' ' is not allowed in slugs", change: func(t *testing.T, dir string) {
			changeSite(t, dir, revisionName(servicesRev), `"slug": "services"`, `"slug": "a b"`)
		}},
		// A published revision needs what publishing needs, which a draft does not.
		{name: "a published revision that lacks a required item", msg: "/title: missing title", change: func(t *testing.T, dir string) {
			changeSite(t, dir, revisionName(contactRev), `"title": "お問い合わせ",`+"\n  ", "")
		}},
		{name: "a published entry whose revision file is missing", msg: `/publishedRevision: revision "` + contactRev + `" does not exist`, change: func(t *testing.T, dir string) {
			removeSite(t, dir, revisionName(contactRev))
		}},
		{name: "an original that is missing", msg: "/hash: no original with this hash", change: func(t *testing.T, dir string) {
			removeSite(t, dir, jpegName)
		}},
		{name: "an original with another size than its document", msg: "/size: the original has 36153 bytes, not 36154", change: func(t *testing.T, dir string) {
			changeSite(t, dir, jpegDoc, `"size": 36153`, `"size": 36154`)
		}},
		{name: "a PDF original with another hash than its document", msg: "/hash: the original has the hash sha256:", change: func(t *testing.T, dir string) {
			writeSite(t, dir, pdfName, string(flipped(pdfFile)))
		}},
		{name: "an original that is not a PDF", msg: "/mimeType: the original is not application/pdf: ", change: func(t *testing.T, dir string) {
			removeSite(t, dir, pdfName)
			writeSite(t, dir, "files/"+strings.TrimPrefix(fileHash(notPDF), "sha256:"), string(notPDF))
			changeSite(t, dir, pdfDoc, `"hash": "`+fileHash(pdfFile)+`"`, `"hash": "`+fileHash(notPDF)+`"`)
			changeSite(t, dir, pdfDoc, `"size": 676`, `"size": `+strconv.Itoa(len(notPDF)))
		}},
		{name: "an original of another format than its PDF document", msg: "/mimeType: the original is image/png, not application/pdf", change: func(t *testing.T, dir string) {
			file := testPNG(t, 4, 4, 1)
			removeSite(t, dir, pdfName)
			writeSite(t, dir, "files/"+strings.TrimPrefix(fileHash(file), "sha256:"), string(file))
			changeSite(t, dir, pdfDoc, `"hash": "`+fileHash(pdfFile)+`"`, `"hash": "`+fileHash(file)+`"`)
			changeSite(t, dir, pdfDoc, `"size": 676`, `"size": `+strconv.Itoa(len(file)))
		}},
		{name: "an original of another format than its document", msg: "/mimeType: the original is image/png, not image/jpeg", change: func(t *testing.T, dir string) {
			changeSite(t, dir, pngDoc, `"mimeType": "image/png"`, `"mimeType": "image/jpeg"`)
		}},
		{name: "an original of another width than its document", msg: "/width: the original is 256x256 pixels as displayed, not 255x256", change: func(t *testing.T, dir string) {
			changeSite(t, dir, pngDoc, `"width": 256`, `"width": 255`)
		}},
		{name: "an original of another height than its document", msg: "/height: the original is 256x256 pixels as displayed, not 256x255", change: func(t *testing.T, dir string) {
			changeSite(t, dir, pngDoc, `"height": 256`, `"height": 255`)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := copySite(t)
			if tt.change != nil {
				tt.change(t, dir)
			}
			th := os.DirFS(themeDir)
			if tt.theme != nil {
				th = tt.theme(t)
			}
			t.Logf("the problems:\n%s", strings.Join(matchBuild(t, dir, th, tt.msg), "\n"))
		})
	}

	// The problems that the Import finds and the build does not, or does not word so.
	logo, logoFile := sampleMedium(t, logoMedium)
	garbage := append([]byte("this is not an image, but it has as many bytes as the logo has. "), make([]byte, logo.Size)...)[:logo.Size]
	_, probeErr := mediafile.Probe(bytes.NewReader(garbage))
	if probeErr == nil {
		t.Fatal("Probe() accepts the garbage")
	}
	for _, tt := range []struct {
		name         string
		buildAccepts bool
		change       func(t *testing.T, dir string)
		want         []string
	}{
		{"a medium that nothing uses, whose original is missing", true, func(t *testing.T, dir string) {
			addDoc(t, dir, content.RefMedia, testID(content.RefMedia, 1), `{
  "schemaVersion": 1,
  "hash": "sha256:`+strings.Repeat("ab", 32)+`",
  "fileName": "unused.png",
  "mimeType": "image/png",
  "size": 100,
  "width": 10,
  "height": 10,
  "uploadedAt": "2026-10-25T10:00:00+09:00",
  "uploadedBy": "`+userID+`"
}`)
		}, []string{docName(content.RefMedia, testID(content.RefMedia, 1)) + ": /hash: no original with this hash"}},
		{"an image original with another hash than its document", false, func(t *testing.T, dir string) {
			writeSite(t, dir, pngName, string(flipped(pngFile)))
		}, []string{pngDoc + ": /hash: the original has the hash " + fileHash(flipped(pngFile))}},
		{"an image original that cannot be read", false, func(t *testing.T, dir string) {
			removeSite(t, dir, logoName)
			writeSite(t, dir, "files/"+strings.TrimPrefix(fileHash(garbage), "sha256:"), string(garbage))
			changeSite(t, dir, docName(content.RefMedia, logoMedium), `"hash": "`+fileHash(logoFile)+`"`, `"hash": "`+fileHash(garbage)+`"`)
		}, []string{docName(content.RefMedia, logoMedium) + ": cannot read the original: " + probeErr.Error()}},
		{"an entry that is not published and does not validate", true, func(t *testing.T, dir string) {
			changeSite(t, dir, docName(content.RefEntry, draftEN), `"language": "en"`, `"language": "fr"`)
		}, []string{docName(content.RefEntry, draftEN) + `: /language: language "fr" is not in the site settings (use ja, en)`}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := copySite(t)
			tt.change(t, dir)
			importRefuses(t, dir, tt.buildAccepts, tt.want...)
		})
	}

	t.Run("a user that is not valid", func(t *testing.T) {
		src := newSpy(t, sampleDir)
		s := newStore(t)
		for _, by := range []string{"", "usr_x", "ent_01JB8W2N5KD3F6H9J1M4P7R2T5"} {
			warnings, err := Import(t.Context(), s, src, by)
			want := content.CheckID(content.RefUser, by)
			if err == nil || err.Error() != want.Error() || warnings != nil {
				t.Errorf("Import() as %q = %v, %v; want the error %q", by, warnings, err, want)
			}
		}
		if len(src.calls) != 0 {
			t.Errorf("Import() has read the source: %v", src.calls)
		}
		checkNoImport(t, s)
	})

	// An error of the source that is not a problem of the site is the error, as the build returns it.
	t.Run("a source that fails", func(t *testing.T) {
		errRead := errors.New("cannot read")
		for _, method := range []string{"Settings", "Documents", "Theme", "Revision", "OpenMedia"} {
			src := newSpy(t, sampleDir)
			src.fails = map[string]error{method: errRead}
			s := newStore(t)
			warnings, err := Import(t.Context(), s, src, userID)
			var problems content.Errors
			if !errors.Is(err, errRead) || errors.As(err, &problems) || warnings != nil {
				t.Errorf("Import() with %s failing = %v, %v; want the error of the source", method, warnings, err)
			}
			checkNoImport(t, s)
		}
	})
}

// TestImportStages checks that a stage stops the Import at its problems, as the build's stages do, with the lines of the
// build's error: the problems of a later stage are not found. The Import checks the originals before the checks across the
// documents, which are in its Update, and the build after them.
func TestImportStages(t *testing.T) {
	t.Parallel()
	const aut = "aut_01JBA4C7N9Q3T6W9Z3C6F9H2K5"
	jpegName, _ := sampleOriginal(t, jpegMedium)
	badSlug := func(t *testing.T, dir string) {
		changeSite(t, dir, revisionName(servicesRev), `"slug": "services"`, `"slug": "a b"`)
	}
	duplicate := func(t *testing.T, dir string) {
		changeSite(t, dir, revisionName(contactRev), `"slug": "contact"`, `"slug": "about"`)
	}
	badZone := func(t *testing.T, dir string) {
		changeSite(t, dir, settingsName, `"Asia/Tokyo"`, `"Asia/Nowhere"`)
	}
	noOriginal := func(t *testing.T, dir string) { removeSite(t, dir, jpegName) }
	brokenTerm := func(t *testing.T, dir string) { writeSite(t, dir, newsName, "{") }
	brokenAuthor := func(t *testing.T, dir string) { writeSite(t, dir, docName(content.RefAuthor, aut), "{") }
	badID := func(t *testing.T, dir string) {
		changeSite(t, dir, newsName, `"id": "`+newsID+`"`, `"id": "`+pressTerm+`"`)
	}
	noAvatar := func(t *testing.T, dir string) {
		changeSite(t, dir, docName(content.RefAuthor, aut), `"avatar": "`+pngMedium+`"`, `"avatar": "med_01JB8Z3K5RJ4W7Z1D5S8X2F6C8"`)
	}
	for _, tt := range []struct {
		name    string
		msg     string // in the first line, which is the problem of the stage that stops the Import
		n       int    // the number of the lines, all of the stage
		changes []func(t *testing.T, dir string)
	}{
		{"documents that do not decode, settings and validation", "unexpected end of JSON input", 1, []func(*testing.T, string){brokenTerm, badZone, badSlug}},
		{"an ID that differs, settings and validation", "differs from the ID of the document", 1, []func(*testing.T, string){badID, badZone, badSlug}},
		// The problems are sorted by file, not in the order that the documents are read.
		{"documents of two kinds that do not decode", "unexpected end of JSON input", 2, []func(*testing.T, string){brokenTerm, brokenAuthor}},
		{"settings and validation", "/timeZone: unknown time zone", 1, []func(*testing.T, string){badZone, badSlug}},
		{"documents and revisions that are not valid", "/avatar: media ", 2, []func(*testing.T, string){noAvatar, badSlug}},
		{"validation and the checks across the documents", "/slug: character ' ' is not allowed in slugs", 1, []func(*testing.T, string){badSlug, duplicate}},
		{"validation and originals", "/slug: character ' ' is not allowed in slugs", 1, []func(*testing.T, string){badSlug, noOriginal}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := copySite(t)
			for _, change := range tt.changes {
				change(t, dir)
			}
			got := importMatchesBuild(t, dir, tt.msg)
			if len(got) != tt.n || !strings.Contains(got[0], tt.msg) {
				t.Errorf("the problems:\n%s\nwant %d, the first with %q", strings.Join(got, "\n"), tt.n, tt.msg)
			}
		})
	}

	t.Run("originals and the checks across the documents", func(t *testing.T) {
		dir := copySite(t)
		duplicate(t, dir)
		noOriginal(t, dir)
		built := buildErrors(t, dir)
		if len(built) != 1 || !strings.Contains(built[0], "/slug: slug \"about\" is also used by") {
			t.Fatalf("the problems of the build:\n%s\nwant the duplicate slug", strings.Join(built, "\n"))
		}
		importRefuses(t, dir, false, docName(content.RefMedia, jpegMedium)+": /hash: no original with this hash")
		// Without the missing original, the Import finds the duplicate, with the lines of the build.
		writeSite(t, dir, jpegName, string(sampleFile(t, jpegName)))
		if got := importMatchesBuild(t, dir, "/slug: slug \"about\" is also used by"); !slices.Equal(got, built) {
			t.Errorf("the problems of Import():\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(built, "\n"))
		}
	})
}

// TestImportBuildOnly imports sites that the build refuses and the Import does not: the duplicates of the output paths that
// the build finds only after it has written the files of the release, and the images that the build cannot publish, which
// the Import does not refuse.
func TestImportBuildOnly(t *testing.T) {
	t.Parallel()
	accepts := func(t *testing.T, dir string) {
		t.Helper()
		s := newStore(t)
		if _, err := importSite(t, s, dir); err != nil {
			t.Errorf("Import() error = %v", err)
		}
		sameFiles(t, storedFiles(t, s), siteFiles(t, dir))
	}

	t.Run("a redirect from the path of the file of a medium", func(t *testing.T) {
		dir := copySite(t)
		from := "/media/" + pdfMedium + "/会社案内.pdf"
		addDoc(t, dir, content.RefRedirect, testID(content.RefRedirect, 1), redirectJSON(from, "/services/"))
		want := `is the output path "` + from + `" of ` + docName(content.RefMedia, pdfMedium)
		if got := buildErrors(t, dir); len(got) != 1 || !strings.Contains(got[0], want) {
			t.Fatalf("the problems of the build:\n%s\nwant the output path of the file of the medium", strings.Join(got, "\n"))
		}
		accepts(t, dir)
	})

	t.Run("an image with a side of more than 65535 pixels", func(t *testing.T) {
		dir := copySite(t)
		file := pngHeader(70000, 10)
		id := testID(content.RefMedia, 1)
		addDoc(t, dir, content.RefMedia, id, `{
  "schemaVersion": 1,
  "hash": "`+fileHash(file)+`",
  "fileName": "wide.png",
  "mimeType": "image/png",
  "size": `+strconv.Itoa(len(file))+`,
  "width": 70000,
  "height": 10,
  "uploadedAt": "2026-10-25T10:00:00+09:00",
  "uploadedBy": "`+userID+`"
}`)
		writeSite(t, dir, "files/"+strings.TrimPrefix(fileHash(file), "sha256:"), string(file))
		// The site does not use it, so the build accepts it too, until the logo is the image.
		accepts(t, dir)
		changeSite(t, dir, settingsName, `"logo": "`+logoMedium+`"`, `"logo": "`+id+`"`)
		if got := buildErrors(t, dir); len(got) != 1 || !strings.Contains(got[0], "too long a side: 70000x10 (max 65535)") {
			t.Fatalf("the problems of the build:\n%s\nwant the side that is too long", strings.Join(got, "\n"))
		}
		accepts(t, dir)
	})
}

// normalizedMedium returns the JSON that MarshalJSON writes of the medium of data after Normalize.
func normalizedMedium(t *testing.T, data string) string {
	t.Helper()
	var m content.Media
	if err := m.UnmarshalJSON([]byte(data)); err != nil {
		t.Fatal(err)
	}
	m.Normalize()
	return mediaText(t, m)
}

// TestImportNormalized checks that the Import normalizes the documents and the published revisions as a save does, before it
// validates them, and stores what MarshalJSON writes of them where that changes them: the build refuses values that are not
// normalized, and accepts what the Import has stored.
func TestImportNormalized(t *testing.T) {
	t.Parallel()
	site, err := content.LoadSite(content.SiteFile{Name: settingsName, Data: sampleFile(t, settingsName)},
		content.SiteFile{Name: "site/schema.yaml", Data: sampleFile(t, "site/schema.yaml")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	normalizedRevision := func(typ, data string) string {
		rev := decodeRevision(t, []byte(data))
		rev.Normalize(site.Type(typ))
		out, err := rev.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}

	dir := copySite(t)
	// か and U+3099, which in NFC are the one character が.
	decomposed := "か" + string(rune(0x3099))
	const worksRev = "rev_01JC0000000000000000000002" // of a works entry, which has a field client
	jpegDoc, servicesFile, worksFile := docName(content.RefMedia, jpegMedium), revisionName(servicesRev), revisionName(worksRev)
	changeSite(t, dir, newsName, `"name": "お知らせ"`, `"name": "お知らせ`+decomposed+`"`)
	changeSite(t, dir, jpegDoc, `"fileName": "会社外観.jpg"`, `"fileName": " 会社外観.jpg "`)
	changeSite(t, dir, servicesFile, `"slug": "services"`, `"slug": "Services"`)
	changeSite(t, dir, worksFile, `"client": "〇〇株式会社"`, `"client": "〇〇株式会社`+decomposed+`"`)
	changed := []string{newsName, jpegDoc, servicesFile, worksFile}
	refused := strings.Join(buildErrors(t, dir), "\n")
	for _, name := range changed {
		if !strings.Contains(refused, name+":") {
			t.Fatalf("the problems of the build:\n%s\nwant one in %s", refused, name)
		}
	}

	// What the store has is what MarshalJSON writes of each of them after Normalize, with the fields of the type of the entry.
	want := siteFiles(t, dir)
	want[newsName] = changedTerm(t, []byte(want[newsName]), func(x *content.Term) { x.Normalize() })
	want[jpegDoc] = normalizedMedium(t, want[jpegDoc])
	want[servicesFile] = normalizedRevision(content.TypePage, want[servicesFile])
	want[worksFile] = normalizedRevision("works", want[worksFile])

	s := newStore(t)
	if _, err := importSite(t, s, dir); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	got := storedFiles(t, s)
	sameFiles(t, got, want)
	for name, text := range map[string]string{
		newsName: `"name":"お知らせが"`, jpegDoc: `"fileName":"会社外観.jpg"`, servicesFile: `"slug":"services"`, worksFile: `"client":"〇〇株式会社が"`,
	} {
		if !strings.Contains(got[name], text) {
			t.Errorf("%s is %s, want it normalized: %s", name, got[name], text)
		}
	}
	for _, name := range changed {
		writeSite(t, dir, name, got[name])
	}
	if lines := buildErrors(t, dir); len(lines) != 0 {
		t.Errorf("the build refuses the documents that the Import has stored:\n%s", strings.Join(lines, "\n"))
	}
}

// TestImportLocated checks that the problems that the Import finds across the documents are located in the files that the
// source gave, also where it stores other bytes of them, which it does for a document that it has normalized.
func TestImportLocated(t *testing.T) {
	t.Parallel()
	dir := copySite(t)
	id := testID(content.RefTerm, 1)
	name := docName(content.RefTerm, id)
	decomposed := "か" + string(rune(0x3099))
	data := replaceOnce(t, termJSON("category", "ja", testID(content.RefTranslationGroup, 1), "お知らせ"+decomposed, "news"),
		"\"schemaVersion\": 1,\n", "\"schemaVersion\": 1,\n  \"id\": \""+id+"\",\n")
	writeSite(t, dir, name, data)
	// The build refuses it for the name, which is not in NFC, before it finds the slug.
	if got := buildErrors(t, dir); len(got) != 1 || !strings.Contains(got[0], "/name: ") {
		t.Fatalf("the problems of the build:\n%s\nwant the name", strings.Join(got, "\n"))
	}

	s := newStore(t)
	want := where(t, name, data, `"slug"`) + `/slug: slug "news" is also used by ` + docName(content.RefTerm, newsID)
	_, err := importSite(t, s, dir)
	if got := problemLines(t, err); !slices.Equal(got, []string{want}) {
		t.Errorf("the problems of Import():\n%s\nwant\n%s", strings.Join(got, "\n"), want)
	}
	checkNoImport(t, s)
}

// TestImportAsImport checks that the Import validates a published revision as the build does, as an import without the
// theme: it accepts a legacy block, an image without alt and a style that the theme does not allow, which a save of a new
// revision refuses.
func TestImportAsImport(t *testing.T) {
	t.Parallel()
	const (
		legacy = `{"type":"legacy","id":"b_00000001","attrs":{"html":"<p>old</p>","reason":"imported"}}`
		image  = `{"type":"image","id":"b_00000002","attrs":{"media":"` + jpegMedium + `"}}`
		red    = `{"type":"paragraph","id":"b_00000003","style":{"textColor":{"value":"#ff0000"}},"content":[{"type":"text","text":"x"}]}`
		page   = `{"schemaVersion":1,"title":"a","slug":"imported","body":[` + legacy + `,` + image + `,` + red + `]}`
	)
	dir := copySite(t)
	entry, rev := testID(content.RefEntry, 1), testID(content.RefRevision, 1)
	writeSite(t, dir, docName(content.RefEntry, entry),
		storedEntry(entry, 301, content.TypePage, "ja", testID(content.RefTranslationGroup, 1), content.StatusPublished, rev))
	writeSite(t, dir, revisionName(rev), page)
	if got := buildErrors(t, dir); len(got) != 0 {
		t.Fatalf("the problems of the build:\n%s", strings.Join(got, "\n"))
	}
	s := newStore(t)
	if _, err := importSite(t, s, dir); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if got := revisionData(t, s, rev); string(got) != page {
		t.Errorf("the revision is %s, want the bytes of its file", got)
	}

	ed := testEditor(t, s)
	_, err := ed.SaveRevision(t.Context(), Op{By: userID}, newDraft(t, ed, content.TypePage, "ja"), "", []byte(page))
	got := problemLines(t, err)
	if len(got) != 3 || !strings.HasSuffix(got[0], "/body/0: legacy blocks can only be created by import") ||
		!strings.HasSuffix(got[1], "/body/1/attrs/alt: missing alt; set decorative for a decorative image") ||
		!strings.HasSuffix(got[2], "/body/2/style/textColor/value: the theme allows only presets for color") {
		t.Errorf("the problems of SaveRevision():\n%s", strings.Join(got, "\n"))
	}
}

// TestImportWithURL checks ImportOptions.URL: the checks of the import, those of each document and those across the
// documents, use it instead of the URL of the settings, which the store keeps as the source has it; the Import without it
// accepts what only that URL refuses; a URL that does not pass is an error before the source is read.
func TestImportWithURL(t *testing.T) {
	t.Parallel()
	const staging = "https://staging.example.com"
	redirects := func(t *testing.T, rs ...[2]string) (dir string, names []string, data []string) {
		t.Helper()
		dir = copySite(t)
		for i, r := range rs {
			id := testID(content.RefRedirect, i+1)
			names, data = append(names, docName(content.RefRedirect, id)), append(data, indentRedirect(t, id, r[0], r[1]))
			writeSite(t, dir, names[i], data[i])
		}
		return dir, names, data
	}
	with := func(t *testing.T, dir string) (*store.Store, content.Errors, error) {
		t.Helper()
		s := newStore(t)
		warnings, err := ImportWith(t.Context(), s, openSite(t, dir), userID, ImportOptions{URL: staging})
		return s, warnings, err
	}

	t.Run("a redirect to itself", func(t *testing.T) {
		dir, names, data := redirects(t, [2]string{"/staging-only", staging + "/staging-only"})
		if _, err := importSite(t, newStore(t), dir); err != nil {
			t.Fatalf("Import() = %v, want the site to be accepted: the redirect goes out of it", err)
		}
		s, warnings, err := with(t, dir)
		want := where(t, names[0], data[0], `"to"`) + "/to: redirects to itself"
		if got := problemLines(t, err); !slices.Equal(got, []string{want}) || warnings != nil {
			t.Errorf("ImportWith() = %v, %v; want the problem %q", warnings, got, want)
		}
		checkNoImport(t, s)
	})

	t.Run("a loop of redirects", func(t *testing.T) {
		dir, names, data := redirects(t, [2]string{"/loop-a", staging + "/loop-b"}, [2]string{"/loop-b", staging + "/loop-a"})
		if _, err := importSite(t, newStore(t), dir); err != nil {
			t.Fatalf("Import() = %v, want the site to be accepted: the redirects go out of it", err)
		}
		s, warnings, err := with(t, dir)
		cycle := strings.Join([]string{testID(content.RefRedirect, 1), testID(content.RefRedirect, 2), testID(content.RefRedirect, 1)}, " -> ")
		want := []string{
			where(t, names[0], data[0], `"to"`) + "/to: redirect loop: " + cycle,
			where(t, names[1], data[1], `"to"`) + "/to: redirect loop: " + cycle,
		}
		if got := problemLines(t, err); !slices.Equal(got, want) || warnings != nil {
			t.Errorf("ImportWith() = %v, %v; want the problems\n%s", warnings, got, strings.Join(want, "\n"))
		}
		checkNoImport(t, s)
	})

	t.Run("the settings stay as they are", func(t *testing.T) {
		dir, _, _ := redirects(t, [2]string{"/production-only", "https://example.com/production-only"})
		if _, err := importSite(t, newStore(t), dir); err == nil {
			t.Fatal("Import() accepted a redirect to itself")
		}
		s, _, err := with(t, dir)
		if err != nil {
			t.Fatalf("ImportWith() with the URL %s = %v, want a redirect to the URL of the settings to go out of the site", staging, err)
		}
		if got := read(t, s, store.SettingsKind, ""); !bytes.Equal(got.Data, sampleFile(t, settingsName)) ||
			!strings.Contains(string(got.Data), `"url": "https://example.com"`) {
			t.Errorf("the settings that ImportWith() stored are\n%s\nwant the bytes of the file, with the URL of the settings", got.Data)
		}
	})

	t.Run("the sample site", func(t *testing.T) {
		s, warnings, err := with(t, sampleDir)
		if err != nil {
			t.Fatalf("ImportWith() of the sample site = %v", err)
		}
		_, plain := newImported(t)
		sameIndex(t, "ImportWith() of the sample site", indexOf(t, s), indexOf(t, plain))
		if want := sampleRedirectWarnings(t); len(want) == 0 || !slices.Equal(lines(warnings), want) {
			t.Errorf("the warnings of ImportWith() of the sample site:\n%v\nwant\n%s", warnings, strings.Join(want, "\n"))
		}
	})

	t.Run("a URL that does not pass", func(t *testing.T) {
		for _, bad := range []string{
			"https://staging.example.com/", "https://Staging.example.com", "ftp://staging.example.com",
			"https://user:s3cret@staging.example.com", "staging.example.com",
		} {
			s := newStore(t)
			spy := &spySource{Source: openSample(t)}
			warnings, err := ImportWith(t.Context(), s, spy, userID, ImportOptions{URL: bad})
			var es content.Errors
			switch {
			case err == nil, errors.As(err, &es), !strings.HasPrefix(err.Error(), "invalid site URL: "):
				t.Errorf("ImportWith() with the URL %q = %v, want an error that is not about the documents, starting with %q",
					bad, err, "invalid site URL: ")
			case strings.Contains(err.Error(), bad), strings.Contains(err.Error(), "s3cret"):
				t.Errorf("the error of ImportWith() with the URL %q is %q, which has it in it", bad, err)
			}
			if warnings != nil || len(spy.calls) != 0 {
				t.Errorf("ImportWith() with the URL %q returned %v and read the source %v", bad, warnings, spy.calls)
			}
			checkNoImport(t, s)
		}
	})
}

// TestImportRevisions checks which revisions the Import stores: those that the entries publish or schedule, and no other.
func TestImportRevisions(t *testing.T) {
	t.Parallel()
	// The site directory does not say whose a revision is, and the build never reads the revisions that no entry publishes.
	t.Run("revisions that no entry publishes", func(t *testing.T) {
		dir := copySite(t)
		orphan := testID(content.RefRevision, 1)
		writeSite(t, dir, revisionName(orphan), "this is not JSON")
		if got := buildErrors(t, dir); len(got) != 0 {
			t.Fatalf("the problems of the build:\n%s", strings.Join(got, "\n"))
		}
		s := newStore(t)
		if _, err := importSite(t, s, dir); err != nil {
			t.Fatalf("Import() error = %v", err)
		}
		files := storedFiles(t, s)
		sameFiles(t, files, siteFiles(t, sampleDir))
		if _, ok := files[revisionName(orphan)]; ok {
			t.Errorf("the store has the revision %s that no entry publishes", orphan)
		}
	})

	// Two entries can publish one revision; it is stored once, with the first of them in the order of IDs.
	t.Run("a revision that two entries publish", func(t *testing.T) {
		dir := copySite(t)
		changeSite(t, dir, docName(content.RefEntry, aboutEN), `"publishedRevision": "`+aboutENRev+`"`, `"publishedRevision": "`+aboutRev+`"`)
		removeSite(t, dir, revisionName(aboutENRev))
		s := newStore(t)
		if _, err := importSite(t, s, dir); err != nil {
			t.Fatalf("Import() error = %v", err)
		}
		if got := revisionIDs(t, s, aboutJA); !slices.Equal(got, []string{aboutRev}) {
			t.Errorf("the revisions of %s are %v, want only %s", aboutJA, got, aboutRev)
		}
		if got := revisionIDs(t, s, aboutEN); len(got) != 0 {
			t.Errorf("the revisions of %s are %v, want none", aboutEN, got)
		}
		if sum := summaryOf(t, s, aboutEN); sum.Published == nil || sum.Published.Slug != "about" {
			t.Errorf("the summary of %s is %+v, want the published revision %s", aboutEN, sum, aboutRev)
		}
		sameFiles(t, storedFiles(t, s), siteFiles(t, dir))
	})
}

// TestImportNotEmpty checks that the Import refuses a store that has a document of any kind, the settings included,
// before it reads the source or saves an original, and takes a store whose documents are all deleted.
func TestImportNotEmpty(t *testing.T) {
	t.Parallel()
	src := openSample(t)
	settings, _, err := src.Settings()
	if err != nil {
		t.Fatal(err)
	}
	_, jpegFile := sampleMedium(t, jpegMedium)
	for _, kind := range slices.Concat([]content.RefKind{store.SettingsKind}, importKinds) {
		t.Run(string(kind), func(t *testing.T) {
			s := newStore(t)
			if kind == store.SettingsKind {
				put(t, s, kind, "", string(settings.Data))
			} else {
				files, err := src.Documents(kind)
				if err != nil {
					t.Fatal(err)
				}
				id := slices.Sorted(maps.Keys(files))[0]
				put(t, s, kind, id, string(files[id].Data))
			}
			before := snapshot(t, s)
			spy := &spySource{Source: src}
			warnings, err := Import(t.Context(), s, spy, userID)
			if err == nil || err.Error() != errNotEmpty.Error() || warnings != nil {
				t.Errorf("Import() = %v, %v; want the error %q", warnings, err, errNotEmpty)
			}
			if len(spy.calls) != 0 || snapshot(t, s) != before || hasOriginal(t, s, fileHash(jpegFile)) {
				t.Errorf("Import() has read the source %v, changed the store or saved an original", spy.calls)
			}
		})
	}

	t.Run("deleted documents", func(t *testing.T) {
		s := newStore(t)
		id := testID(content.RefTerm, 1)
		put(t, s, content.RefTerm, id, termJSON("tag", "ja", testID(content.RefTranslationGroup, 1), "お祭り", "festival"))
		err := s.Update(t.Context(), storeWrite(func(tx *store.Tx) error {
			_, err := tx.Delete(content.RefTerm, id, userID)
			return err
		}))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Import(t.Context(), s, src, userID); err != nil {
			t.Errorf("Import() into a store whose documents are deleted: %v", err)
		}
	})

	// The Update checks again, as a document can be saved while the source is read.
	t.Run("a document that is saved while the source is read", func(t *testing.T) {
		s := newStore(t)
		id := testID(content.RefTerm, 1)
		spy := &spySource{Source: src, hooks: map[string]func(){"Theme": func() {
			put(t, s, content.RefTerm, id, termJSON("tag", "ja", testID(content.RefTranslationGroup, 1), "お祭り", "festival"))
		}}}
		warnings, err := Import(t.Context(), s, spy, userID)
		if err == nil || err.Error() != errNotEmpty.Error() || warnings != nil {
			t.Errorf("Import() = %v, %v; want the error %q", warnings, err, errNotEmpty)
		}
		if got := snapshot(t, s); !strings.HasPrefix(got, "term "+id+" ") || strings.Contains(got, "\n") {
			t.Errorf("the store has\n%s\nwant only the term that was saved", got)
		}
		// The originals were saved before the Update: nothing uses them.
		if !hasOriginal(t, s, fileHash(jpegFile)) {
			t.Error("the store has no original, which are saved before the Update")
		}
	})
}

// TestImportRetry checks that a store that a failed Import has left, whichever stage failed, takes the Import of the site
// without the problem, with the originals that the failed one saved: the Import of the Update has rolled back everything,
// the index included.
func TestImportRetry(t *testing.T) {
	t.Parallel()
	jpegDoc := docName(content.RefMedia, jpegMedium)
	for _, tt := range []struct {
		name   string
		change func(t *testing.T, dir string)
	}{
		{"a problem that the Update finds", func(t *testing.T, dir string) {
			changeSite(t, dir, revisionName(contactRev), `"slug": "contact"`, `"slug": "about"`)
		}},
		{"a problem of the originals, which the stage before has saved", func(t *testing.T, dir string) {
			changeSite(t, dir, jpegDoc, `"size": 36153`, `"size": 36154`)
		}},
		{"a problem of validation", func(t *testing.T, dir string) {
			changeSite(t, dir, revisionName(servicesRev), `"slug": "services"`, `"slug": "a b"`)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			broken := copySite(t)
			tt.change(t, broken)
			s := newStore(t)
			if _, err := importSite(t, s, broken); err == nil {
				t.Fatal("Import() of the site with the problem has no error")
			}
			checkNoImport(t, s)
			if _, err := importSite(t, s, sampleDir); err != nil {
				t.Fatalf("Import() of the sample site after a failed one: %v", err)
			}
			sameFiles(t, storedFiles(t, s), siteFiles(t, sampleDir))
			_, other := newSample(t)
			sameIndex(t, "after the failed Import", indexOf(t, s), indexOf(t, other))
		})
	}
}

// TestImportContext checks that an Import stops when its context is done, and stores no document.
func TestImportContext(t *testing.T) {
	t.Parallel()
	t.Run("before it starts", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		s := newStore(t)
		warnings, err := Import(ctx, s, openSample(t), userID)
		if !errors.Is(err, context.Canceled) || warnings != nil {
			t.Errorf("Import() = %v, %v; want context.Canceled", warnings, err)
		}
		checkNoImport(t, s)
	})

	t.Run("while the source is read", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		s := newStore(t)
		spy := &spySource{Source: openSample(t), hooks: map[string]func(){"Theme": cancel}}
		warnings, err := Import(ctx, s, spy, userID)
		if !errors.Is(err, context.Canceled) || warnings != nil {
			t.Errorf("Import() = %v, %v; want context.Canceled", warnings, err)
		}
		if slices.Contains(spy.calls, "OpenMedia") {
			t.Errorf("Import() has read the originals after its context was done: %v", spy.calls)
		}
		checkNoImport(t, s)
	})
}
