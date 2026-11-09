package edit

import (
	"errors"
	"io/fs"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/build"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/sitedir"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

const (
	sampleDir = "../build/testdata/site"    // the sample site of internal/build, a site directory
	themeDir  = "../theme/testdata/example" // its theme, which the site does not copy
	userID    = "usr_01JB8T7H4MA2C5E8G1J4K7N3P6"
)

// testNow is the time of the clock of the Editors of the tests, which makes the IDs that they make predictable: with the
// random bits of testSeed, a new Editor makes the same IDs in the same order.
var testNow = time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)

var testSeed = [32]byte{'e', 'd', 'i', 't'}

// documentKinds are the kinds of the documents of a site directory, in the order in which the tests put them.
var documentKinds = []content.RefKind{
	content.RefEntry, content.RefTerm, content.RefMedia, content.RefAuthor, content.RefMenu, content.RefForm,
	content.RefRedirect,
}

// testID returns the ID of an entity of kind, numbered n, that the sample site does not have and no Editor of the tests
// makes: it sorts after the IDs of the sample site and before those of the Editors.
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

// testEditor returns an Editor of s for the sample site, with the clock testNow and the random bits of testSeed. s may
// be nil for an Editor that only makes IDs.
func testEditor(t *testing.T, s *store.Store) *Editor {
	t.Helper()
	schema, err := os.ReadFile(filepath.Join(sampleDir, "site", "schema.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	th, err := theme.Load(os.DirFS(themeDir))
	if err != nil {
		t.Fatal(err)
	}
	e := New(s, content.SiteFile{Name: "site/schema.yaml", Data: schema}, th)
	e.now = func() time.Time { return testNow }
	e.ids = content.NewIDSource(rand.NewChaCha8(testSeed))
	return e
}

// newSample returns a store in a temporary directory that has the sample site, put with the store's own API, and an
// Editor for it.
func newSample(t *testing.T) (*Editor, *store.Store) {
	t.Helper()
	s, err := store.Create(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	putSample(t, s)
	return testEditor(t, s), s
}

// putSample puts the settings, every document and every revision of the sample site, and the originals of its media, into
// s: the revisions after the entries that publish them.
func putSample(t *testing.T, s *store.Store) {
	t.Helper()
	putSite(t, s, sampleDir)
}

// putSite puts the site directory dir into s as putSample does: the settings, every document, the revisions that entries
// publish after their entries, and the originals if it has files/.
func putSite(t *testing.T, s *store.Store, dir string) {
	t.Helper()
	d, err := sitedir.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	settings, _, err := d.Settings()
	if err != nil {
		t.Fatal(err)
	}
	publisher := make(map[string]string) // the entries by the revisions that they publish
	err = s.Update(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Put(store.SettingsKind, "", settings.Data, userID); err != nil {
			return err
		}
		for _, kind := range documentKinds {
			docs, err := d.Documents(kind)
			if err != nil {
				return err
			}
			for _, id := range slices.Sorted(maps.Keys(docs)) {
				if _, err := tx.Put(kind, id, docs[id].Data, userID); err != nil {
					return err
				}
				if kind != content.RefEntry {
					continue
				}
				var e content.Entry
				if err := e.UnmarshalJSON(docs[id].Data); err != nil {
					return err
				}
				if e.PublishedRevision != "" {
					publisher[e.PublishedRevision] = id
				}
			}
		}
		for _, id := range slices.Sorted(maps.Keys(publisher)) {
			f, err := d.Revision(id)
			if err != nil {
				return err
			}
			if _, err := tx.AddRevision(publisher[id], id, f.Data, userID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(filepath.Join(dir, "files"))
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		r, err := os.Open(filepath.Join(dir, "files", f.Name()))
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.PutOriginal(t.Context(), r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
}

// sampleFile returns the file name of the sample site.
func sampleFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sampleDir, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// replaceOnce returns s with the one occurrence of old replaced with new; the test fails if s has none or more.
func replaceOnce(t *testing.T, s, old, new string) string {
	t.Helper()
	if n := strings.Count(s, old); n != 1 {
		t.Fatalf("%q occurs %d times in %q, want once", old, n, s)
	}
	return strings.Replace(s, old, new, 1)
}

// read returns the current version of the document of kind with id in s.
func read(t *testing.T, s *store.Store, kind content.RefKind, id string) store.Document {
	t.Helper()
	var d store.Document
	err := s.View(t.Context(), func(tx *store.Tx) (err error) {
		d, err = tx.Document(kind, id)
		return err
	})
	if err != nil {
		t.Fatalf("Document(%q, %q) = %v", kind, id, err)
	}
	return d
}

// snapshot describes the current documents of s, the settings document included, each with its version and its hash,
// and its revisions, so that a test can see that an operation has changed nothing.
func snapshot(t *testing.T, s *store.Store) string {
	t.Helper()
	var lines []string
	err := s.View(t.Context(), func(tx *store.Tx) error {
		for _, kind := range slices.Concat(documentKinds, []content.RefKind{store.SettingsKind}) {
			docs, err := tx.Documents(kind)
			if err != nil {
				return err
			}
			for _, d := range docs {
				lines = append(lines, string(kind)+" "+d.ID+" "+strconv.FormatInt(d.Seq, 10)+" "+d.Hash)
			}
		}
		sums, err := tx.Summaries(content.RefEntry)
		if err != nil {
			return err
		}
		for _, sum := range sums {
			revs, err := tx.Revisions(sum.ID)
			if err != nil {
				return err
			}
			for _, r := range revs {
				lines = append(lines, "revision "+r.ID+" "+r.Entry+" "+r.Hash)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}

// problemLines returns the lines of err, which must be content.Errors.
func problemLines(t *testing.T, err error) []string {
	t.Helper()
	var es content.Errors
	if !errors.As(err, &es) || len(es) == 0 {
		t.Fatalf("error = %v, want problems of documents (content.Errors)", err)
	}
	return strings.Split(es.Error(), "\n")
}

// copySite returns a copy of the sample site directory in a temporary directory, which a test can change.
func copySite(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "site")
	if err := os.CopyFS(dir, os.DirFS(sampleDir)); err != nil {
		t.Fatal(err)
	}
	return dir
}

// source is a site directory with the example theme, which the directory does not copy.
type source struct {
	*sitedir.Dir
	theme fs.FS
}

func (s source) Theme() (fs.FS, error) { return s.theme, nil }

// buildErrors builds the site directory dir with the example theme into temporary directories and returns the lines of
// its error, or none if the build succeeds. An error that is not the problems of the site fails the test.
func buildErrors(t *testing.T, dir string) []string {
	t.Helper()
	d, err := sitedir.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	_, err = build.Build(t.Context(), build.Options{
		Source:   source{Dir: d, theme: os.DirFS(themeDir)},
		Out:      t.TempDir(),
		CacheDir: t.TempDir(),
		Workers:  2,
	})
	if err == nil {
		return nil
	}
	return problemLines(t, err)
}

var positions = regexp.MustCompile(`^([^:]+):\d+(?::\d+)?: `)

// stripPositions turns the lines `file:line:col: pointer: msg` into `file: pointer: msg`.
func stripPositions(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = positions.ReplaceAllString(l, "$1: ")
	}
	return out
}

// must returns a function that returns the result of an operation, as in must(t)(ed.Create(...)), and fails the test if
// the operation has an error.
func must(t *testing.T) func(Result, error) Result {
	return func(res Result, err error) Result {
		t.Helper()
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		return res
	}
}
