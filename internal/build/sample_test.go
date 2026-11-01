//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package build

import (
	"crypto/sha256"
	"encoding/json/jsontext"
	"flag"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/sitedir"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/out")

const (
	sampleSite = "testdata/site" // the sample site, whose files/ and media/ go generate writes
	sampleOut  = "testdata/out"  // the golden files of its release
)

// sampleFiles are the files of the release of the sample site that sampleOut has copies of, at their paths in the
// release, with those in public/css: the pages of the paths in the table of 6.11.9 of the concept, the English
// translation of its post, the services page with the paragraph of 6.11.2 and the contact page; the 404 pages, the
// feeds, the sitemap and the files for the servers.
var sampleFiles = []string{
	"public/index.html",
	"public/2026/10/26/新サービス/index.html",
	"public/en/2026/10/25/new-service/index.html",
	"public/about/index.html",
	"public/news/index.html",
	"public/news/page/2/index.html",
	"public/en/index.html",
	"public/en/page/2/index.html",
	"public/en/about/index.html",
	"public/category/news/index.html",
	"public/category/news/press-release/index.html",
	"public/tag/イベント/index.html",
	"public/en/tag/events/index.html",
	"public/author/yamada/index.html",
	"public/works/index.html",
	"public/works/example-corporate-site/index.html",
	"public/services/index.html",
	"public/contact/index.html",
	"public/404.html",
	"public/en/404.html",
	"public/feed/index.xml",
	"public/en/feed/index.xml",
	"public/sitemap.xml",
	"public/.htaccess",
	"server.json",
	"nginx.conf",
}

// sampleSource is the sample site with the example theme of internal/theme, which the site does not copy.
type sampleSource struct {
	*sitedir.Dir
	theme fs.FS
}

func (s sampleSource) Theme() (fs.FS, error) { return s.theme, nil }

// openSample opens the sample site, which it closes at the end of the test.
func openSample(t *testing.T) sampleSource {
	t.Helper()
	d, err := sitedir.Open(sampleSite)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return sampleSource{Dir: d, theme: openRoot(t, "../theme/testdata/example").FS()}
}

// manifest returns a line for each file of the release in dir, in the order of their paths: its path in the release,
// its size and the hex digits of its SHA-256.
func manifest(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	for _, name := range listFiles(t, dir) {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s %d %x\n", name, len(data), sha256.Sum256(data))
	}
	return b.String()
}

// TestSampleSite builds the sample site, which collects the examples of 6.11 of the concept, with the example theme,
// and compares its release with the golden files in testdata/out, which go test -update rewrites: manifest.txt, a line
// for each file of the release (manifest); warnings.txt, the warnings of the build, one on each line; and copies of the
// files of sampleFiles and of the CSS files of the site and of the pages, at their paths in the release.
func TestSampleSite(t *testing.T) {
	out := t.TempDir()
	res := mustBuild(t, openSample(t), out, t.TempDir(), 4)
	release := filepath.Join(out, "releases", res.Release)
	var warnings strings.Builder
	for _, w := range res.Warnings {
		warnings.WriteString(w.Error() + "\n")
	}
	golden := map[string]string{"manifest.txt": manifest(t, release), "warnings.txt": warnings.String()}
	copies := slices.Clone(sampleFiles)
	for _, name := range listFiles(t, filepath.Join(release, publicDir, "css")) {
		copies = append(copies, publicDir+"/css/"+name)
	}
	for _, name := range copies {
		data, err := os.ReadFile(filepath.Join(release, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		golden[name] = string(data)
		switch path.Ext(name) {
		case ".xml":
			checkXML(t, name, data)
		case ".json":
			if !jsontext.Value(data).IsValid() {
				t.Errorf("%s is not valid JSON", name)
			}
		}
	}

	pages, files := 0, 0
	for _, name := range listFiles(t, filepath.Join(release, publicDir)) {
		files++
		if path.Ext(name) == ".html" {
			pages++
		}
	}
	if res.Pages != pages || res.Files != files {
		t.Errorf("Build() = %+v, want %d pages and %d files", res, pages, files)
	}

	if *update {
		if err := os.RemoveAll(sampleOut); err != nil {
			t.Fatal(err)
		}
	}
	names := slices.Sorted(maps.Keys(golden))
	for _, name := range names {
		checkGolden(t, filepath.Join(sampleOut, filepath.FromSlash(name)), golden[name])
	}
	if got := listFiles(t, sampleOut); !slices.Equal(got, names) {
		t.Errorf("%s has %q, want %q", sampleOut, got, names)
	}
}

// checkGolden compares got with the golden file, which go test -update writes instead.
func checkGolden(t *testing.T, file, got string) {
	t.Helper()
	if *update {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	switch {
	case err != nil:
		t.Errorf("%v; go test -update writes it", err)
	case got != string(want):
		t.Errorf("%s differs from what the build wrote, %s; go test -update rewrites it",
			file, firstDiff(got, string(want)))
	}
}

// firstDiff returns the first line in which got and want, two different texts, differ, with its number.
func firstDiff(got, want string) string {
	g, w := strings.SplitAfter(got, "\n"), strings.SplitAfter(want, "\n")
	i := 0
	for i < len(g)-1 && i < len(w)-1 && g[i] == w[i] {
		i++
	}
	return fmt.Sprintf("at line %d: %q, want %q", i+1, g[i], w[i])
}

// TestSampleSiteDeterministic checks that two builds of the sample site give the same release with the same files: the
// first with an empty cache and eight workers, the second with what the first left in the cache and one worker.
func TestSampleSiteDeterministic(t *testing.T) {
	src, cache := openSample(t), t.TempDir()
	var first *Result
	var files string
	for _, workers := range []int{8, 1} {
		out := t.TempDir()
		res := mustBuild(t, src, out, cache, workers)
		m := manifest(t, filepath.Join(out, "releases", res.Release))
		if first == nil {
			first, files = res, m
			if len(namesIn(t, cache)) == 0 {
				t.Fatalf("the build left nothing in the cache directory")
			}
			continue
		}
		if !reflect.DeepEqual(res, first) || m != files {
			t.Errorf("Build() with %d workers and a warm cache = %+v, with the files:\n%s\nwant %+v, with:\n%s",
				workers, res, m, first, files)
		}
	}
}

// changedSource is a Source with more documents and revisions than another, and another schema file if schema is not
// "".
type changedSource struct {
	Source
	schema string
	files  []content.SiteFile // documents and revisions, named as in a site directory
}

func (s changedSource) Settings() (settings, schema content.SiteFile, err error) {
	settings, schema, err = s.Source.Settings()
	if s.schema != "" {
		schema.Data = []byte(s.schema)
	}
	return settings, schema, err
}

func (s changedSource) Documents(kind content.RefKind) (map[string]content.SiteFile, error) {
	docs, err := s.Source.Documents(kind)
	if err != nil {
		return nil, err
	}
	for _, f := range s.files {
		if dir, name, _ := strings.Cut(f.Name, "/"); dir == kindDirs[kind] {
			docs[strings.TrimSuffix(name, ".json")] = f
		}
	}
	return docs, nil
}

func (s changedSource) Revision(id string) (content.SiteFile, error) {
	for _, f := range s.files {
		if f.Name == "revisions/"+id+".json" {
			return f, nil
		}
	}
	return s.Source.Revision(id)
}

// TestSampleSiteDuplicate checks that builds of the sample site with two outputs at a path, after a build that has
// published a release, fail with the errors of the duplicates, at their places in the documents, and leave the output
// directory as it was: current, the releases and every file in them. That holds also for a duplicate that the build
// finds only after it has written the files of the release, such as that of the file of a medium.
func TestSampleSiteDuplicate(t *testing.T) {
	src := openSample(t)
	out, cache := t.TempDir(), t.TempDir()
	mustBuild(t, src, out, cache, 4)

	_, schema, err := src.Settings()
	if err != nil {
		t.Fatal(err)
	}
	postName := strings.Replace(string(schema.Data), "url: /{yyyy}/{mm}/{dd}/{slug}/", "url: /{slug}/", 1)
	if postName == string(schema.Data) {
		t.Fatal("the schema of the sample site has no URL rule of posts")
	}
	redirect := content.SiteFile{Name: "redirects/rdr_01JC0000000000000000000007.json", Data: []byte(`{
  "schemaVersion": 1,
  "id": "rdr_01JC0000000000000000000007",
  "from": "/about/",
  "to": "/",
  "status": 301
}
`)}
	post := content.SiteFile{Name: "entries/ent_01JC0000000000000000000004.json", Data: []byte(`{
  "schemaVersion": 1,
  "id": "ent_01JC0000000000000000000004",
  "number": 137,
  "type": "post",
  "language": "ja",
  "translationGroup": "tg_01JC0000000000000000000004",
  "status": "published",
  "publishedRevision": "rev_01JC0000000000000000000004",
  "createdAt": "2026-10-30T17:00:00+09:00",
  "createdBy": "usr_01JB8T7H4MA2C5E8G1J4K7N3P6"
}
`)}
	revision := content.SiteFile{Name: "revisions/rev_01JC0000000000000000000004.json", Data: []byte(`{
  "schemaVersion": 1,
  "title": "お問い合わせの受付時間",
  "slug": "contact",
  "date": "2026-10-31T10:00:00+09:00",
  "author": "aut_01JBA4C7N9Q3T6W9Z3C6F9H2K5",
  "terms": { "category": ["trm_01JBA4C7E2G5J8K1M4P7R2T5W8"] },
  "body": [
    {
      "type": "paragraph",
      "id": "b_post0024",
      "content": [{ "type": "text", "text": "お問い合わせは、平日の10時から17時まで受け付けています。" }]
    }
  ]
}
`)}
	mediaRedirect := content.SiteFile{Name: "redirects/rdr_01JC0000000000000000000008.json", Data: []byte(`{
  "schemaVersion": 1,
  "id": "rdr_01JC0000000000000000000008",
  "from": "/media/med_01JB8Z3K5TM8A1C5F9V3J6K2N7/会社案内.pdf",
  "to": "/services/",
  "status": 301
}
`)}
	for _, tt := range []struct {
		what string
		src  Source
		want string
	}{
		{
			what: "a redirect from the path of a page",
			src:  changedSource{Source: src, files: []content.SiteFile{redirect}},
			want: redirect.Name + `:4:3: /from: from "/about/" is the output path "/about/" of ` +
				"revisions/rev_01JB8X9Y3ZE5G8K1N4Q7S2V6W9.json",
		},
		{
			what: "a post at the path of a page",
			src:  changedSource{Source: src, schema: postName, files: []content.SiteFile{post, revision}},
			want: revision.Name + `:4:3: /slug: output path "/contact/" is also that of ` +
				"revisions/rev_01JBD1Y2Z3A4B5C6D7E8F9G0H1.json",
		},
		{
			what: "a redirect from the path of the file of a medium",
			src:  changedSource{Source: src, files: []content.SiteFile{mediaRedirect}},
			want: mediaRedirect.Name + `:4:3: /from: from "/media/med_01JB8Z3K5TM8A1C5F9V3J6K2N7/会社案内.pdf" is the ` +
				`output path "/media/med_01JB8Z3K5TM8A1C5F9V3J6K2N7/会社案内.pdf" of ` +
				"media/med_01JB8Z3K5TM8A1C5F9V3J6K2N7.json",
		},
	} {
		failingBuild(t, tt.what, t.Context(), buildOptions(tt.src, out, cache, 4), tt.want)
	}
}
