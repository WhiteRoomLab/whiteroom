package render

import (
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/out")

const goldenOut = "testdata/out" // the golden files, in a directory for each theme and language

// goldenDocuments are the files of the documents that TestGolden renders.
var goldenDocuments = []string{
	"../content/testdata/every-block.json",
	"../content/testdata/about-page-body.json",
	"testdata/every-mark.json",
	"testdata/every-style.json",
}

// goldenSites are the languages in which TestGolden renders the documents, with the bases of the URLs of their sites.
var goldenSites = []struct{ language, base string }{{"ja", ""}, {"en", "/blog"}}

// goldenTheme is a theme in which TestGolden renders the documents, with the name of its directory in goldenOut.
type goldenTheme struct {
	name string
	th   *theme.Theme
}

// goldenThemes returns the themes of TestGolden: default, which has no block templates, so that the default templates
// of the core render the blocks, and no presets, so that styles keep only their values written directly; and example,
// the sample theme of internal/theme.
func goldenThemes(t testing.TB) []goldenTheme {
	t.Helper()
	example, err := theme.Load(os.DirFS("../theme/testdata/example"))
	if err != nil {
		t.Fatalf("theme.Load() error:\n%v", err)
	}
	return []goldenTheme{{"default", testTheme(t, nil)}, {"example", example}}
}

// goldenResolver returns a Resolver with what the golden documents refer to: entries, one of them not published; the
// media of office-image.json and company-guide-pdf.json and others made up, at /media/<file name>, and the files of
// their images; and the form of contact-form.json.
func goldenResolver(t testing.TB) testResolver {
	t.Helper()
	var office, guide content.Media
	decodeTestdata(t, "office-image.json", office.UnmarshalJSON)
	decodeTestdata(t, "company-guide-pdf.json", guide.UnmarshalJSON)
	var contact content.Form
	decodeTestdata(t, "contact-form.json", contact.UnmarshalJSON)
	media := make(map[string]testMedium)
	for _, m := range []*content.Media{
		&office,
		&guide,
		{ID: "med_01JB8Z3K5RJ4W7Z1D5S8X2F6C9", FileName: "pattern.png", MIMEType: "image/png", Width: 400, Height: 300},
		{ID: "med_01JB8Z3K5XQ2F5H8K3M6R9V4Y7", FileName: "製品.jpg", MIMEType: "image/jpeg", Width: 1200, Height: 1200},
		{ID: "med_01JB8Z3K5SK6Y9B3E7T1A4G8H2", FileName: "catalog.pdf", MIMEType: "application/pdf"},
		{ID: "med_01JB8Z3K5WN9C2E6H1K4P7S3T8", FileName: "brochure.pdf", MIMEType: "application/pdf"},
	} {
		media[m.ID] = testMedium{m, "/media/" + m.FileName}
	}
	return testResolver{
		entries: map[string][2]string{
			"ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3": {"サービス", "/services/"},
			"ent_01JB8Y6T2SA3F6H9J2K5M8N1P4": {"お問い合わせ", "/contact/"},
			jaPrivacy:                        {"プライバシーポリシー", "/privacy/"},
			enPrivacy:                        {"Privacy Policy", "/en/privacy/"},
			draftEntry:                       {"下書き", ""},
		},
		media: media,
		forms: map[string]*content.Form{contactForm: &contact},
	}
}

// goldenBlocks returns the blocks of the document in the file p.
func goldenBlocks(t *testing.T, p string) content.Blocks {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return decodeBlocks(t, string(b))
}

// TestGolden renders the golden documents in each theme and language, and compares them with the files in
// testdata/out, which go test -update rewrites: <theme>/<language>/<document>.html, the HTML and a final newline,
// <document>.txt, the warnings, one on each line, and <document>.css, the CSS of the page, which is the same in every
// language; and <theme>/site.css, the CSS of the site.
func TestGolden(t *testing.T) {
	res := goldenResolver(t)
	var themes []string
	for _, gt := range goldenThemes(t) {
		themes = append(themes, gt.name)
		entries := []string{"site.css"}
		css := make(map[string]string) // the CSS of each document in the first language
		for _, site := range goldenSites {
			entries = append(entries, site.language)
			r := newRenderer(t, gt.th, site.language, site.base, res)
			dir := filepath.Join(goldenOut, gt.name, site.language)
			if *update {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			var names []string
			for _, p := range goldenDocuments {
				doc := strings.TrimSuffix(filepath.Base(p), ".json")
				html, c, warnings, err := r.Document(goldenBlocks(t, p))
				if err != nil {
					t.Fatalf("Document(%s) in %s with the %s theme error: %v", doc, site.language, gt.name, err)
				}
				if first, ok := css[doc]; !ok {
					css[doc] = c.String()
				} else if c.String() != first {
					t.Errorf("the CSS of %s with the %s theme in %s differs from that in %s:\n%s",
						doc, gt.name, site.language, goldenSites[0].language, c)
				}
				var lines strings.Builder
				for _, w := range warnings {
					lines.WriteString(w.String() + "\n")
				}
				for _, f := range []struct{ name, got string }{
					{doc + ".html", string(html[0]) + "\n"},
					{doc + ".txt", lines.String()},
					{doc + ".css", c.String()},
				} {
					names = append(names, f.name)
					checkGolden(t, filepath.Join(dir, f.name), "Document()", f.got)
				}
			}
			checkDir(t, dir, names)
		}
		checkGolden(t, filepath.Join(goldenOut, gt.name, "site.css"), "SiteCSS()", SiteCSS(gt.th.Settings))
		checkDir(t, filepath.Join(goldenOut, gt.name), entries)
	}
	checkDir(t, goldenOut, themes)
}

// checkGolden compares got, what call returned, with the golden file, which go test -update writes instead.
func checkGolden(t *testing.T, file, call, got string) {
	t.Helper()
	if *update {
		if err := os.WriteFile(file, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Errorf("%v; go test -update writes it", err)
	} else if got != string(want) {
		t.Errorf("%s differs from %s; go test -update rewrites it:\n%s", call, file, got)
	}
}

// checkDir checks that the directory dir holds the entries names and no others.
func checkDir(t *testing.T, dir string, names []string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if want := slices.Sorted(slices.Values(names)); !slices.Equal(got, want) {
		t.Errorf("%s has %q, want %q", dir, got, want)
	}
}

// TestEveryMarkValid checks that every-mark.json is valid content, which is what the renderer expects.
func TestEveryMarkValid(t *testing.T) {
	if err := goldenBlocks(t, "testdata/every-mark.json").Validate(content.ValidateOptions{}); err != nil {
		t.Errorf("Validate() error:\n%v", err)
	}
}

// TestEveryStyleValid checks that every-style.json is valid content without a theme, so that its warnings in
// TestGolden are those of the themes.
func TestEveryStyleValid(t *testing.T) {
	if err := goldenBlocks(t, "testdata/every-style.json").Validate(content.ValidateOptions{}); err != nil {
		t.Errorf("Validate() error:\n%v", err)
	}
}
