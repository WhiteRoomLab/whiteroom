package wpimport

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/render"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

// The WXR of the bodies of the classic editor, and the golden files of TestClassicBodies, which go test -update writes, as
// bodiesOut holds those of TestBodies: <post ID>.json, what Body made of the content of the post (the number of the blocks
// before the read more tag, and the blocks); report.txt, the report of the import; and render.txt, the warnings of the
// renderer of the example theme.
const (
	classicFile = "classic.xml"
	classicOut  = "testdata/out-classic"
)

// classicNotImported says whether the item is a form that the import does not make a form of: it is given no ID.
func classicNotImported(it *wxr.Item) bool { return it.Type == formType && it.ID == 141 }

// classicCorrected are the settings after the person who imports has corrected them: those of bodiesSettings, and the names that
// the HTML of the classic editor in classic.xml uses.
func classicCorrected() *Settings {
	s := bodiesSettings(false)
	s.Site.Name, s.Site.Tagline = "クラシックエディターの見本サイト", "手で書いた、クラシックエディターの本文の見本です"
	s.Blocks.Marks["hl"] = "marker"
	s.Blocks.ImageSizes["huge"] = ""
	return s
}

// TestClassicBodies converts the content of the posts and the pages of testdata/classic.xml, which are written with the classic
// editor, and compares the blocks and the report with the golden files in testdata/out-classic, which go test -update
// rewrites. The blocks pass as the body of a draft revision, and the example theme renders them.
func TestClassicBodies(t *testing.T) {
	e, rep := readBodiesFile(t, classicFile)
	imp, err := newImportOf(classicFile, e, rep, classicNotImported)
	if err != nil {
		t.Fatal(err)
	}
	results := convertBodiesWith(t, imp, classicCorrected(), func(*wxr.Item) bool { return true })
	if len(results) != len(bodyPosts(e)) {
		t.Fatalf("%d bodies were converted, want %d", len(results), len(bodyPosts(e)))
	}
	checkTargets(t, imp.Report, results)

	golden := make(map[string]string)
	report := reportText(t, imp.Report)
	golden["report.txt"] = report

	th, err := theme.Load(os.DirFS("../theme/testdata/example"))
	if err != nil {
		t.Fatalf("theme.Load = %v", err)
	}
	tr, err := th.Renderer("ja", "")
	if err != nil {
		t.Fatal(err)
	}
	renderer := render.New(tr, newBodiesResolver(t, imp))
	var warnings strings.Builder
	features := make(map[string]bool)
	for _, r := range results {
		golden[strconv.FormatInt(r.item.ID, 10)+".json"] = bodyDocument(t, r.res)
		bodyFeatures(r.res.Blocks, features)
		_, _, ws, err := renderer.Document(r.res.Blocks)
		if err != nil {
			t.Errorf("Document of the body of the post %d = %v", r.item.ID, err)
		}
		for _, w := range ws {
			fmt.Fprintf(&warnings, "%d: %s\n", r.item.ID, w)
		}
	}
	golden["render.txt"] = warnings.String()

	// The bodies have all that the conversion of the classic editor makes: if one of these goes, they no longer show it.
	for _, want := range []string{
		"block:paragraph", "block:heading", "block:list", "block:listItem", "block:quote", "block:table", "block:tableRow",
		"block:tableCell", "block:image", "block:separator", "block:code", "block:embed", "block:group", "block:form", "block:legacy",
		"inline:text", "inline:hardBreak", "mark:bold", "mark:italic", "mark:underline", "mark:strike", "mark:highlight", "mark:link",
		"link:entry", "link:media", "link:path", "link:external", "prop:anchor", "prop:className",
	} {
		if !features[want] {
			t.Errorf("no body has %s", want)
		}
	}
	// ...and the kinds of the sentences of the report that the conversion writes for it.
	for _, want := range []string{
		"page breaks are removed", "of the more link is not imported", "the more tag", "the gallery is",
		"is not converted; it is a legacy block", "the text has the shortcode", "on its own line is text here",
		"the iframe is an embed block here", "made with Elementor", "with their content", "whiteroom has no such settings",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report has no sentence with %q", want)
		}
	}
	// At least two bodies have a read more tag.
	more := 0
	for _, r := range results {
		if r.res.More >= 0 {
			more++
		}
	}
	if more < 2 {
		t.Errorf("%d bodies have a read more tag, want at least 2", more)
	}

	if *update {
		if err := os.RemoveAll(classicOut); err != nil {
			t.Fatal(err)
		}
	}
	names := slices.Sorted(maps.Keys(golden))
	for _, name := range names {
		checkGolden(t, filepath.Join(classicOut, name), golden[name])
	}
	if got := goldenFiles(t, classicOut); !slices.Equal(got, names) {
		t.Errorf("%s has %q, want %q", classicOut, got, names)
	}
}

// TestClassicProposal proposes the settings for classic.xml: the names that the classic editor's HTML uses are counted for
// the person who imports to map them, as those of the blocks of the block editor are. The sizes of the images are the first
// class size-<name> of each img, and the size of each [gallery], which is thumbnail if it has none; the classes are those of
// the span elements. The page made with Elementor is not read.
func TestClassicProposal(t *testing.T) {
	e, rep := readBodiesFile(t, classicFile)
	imp := New(classicFile, e, rep)
	s, err := imp.ProposeSettings(nil)
	if err != nil {
		t.Fatalf("ProposeSettings = %v", err)
	}
	want := emptyBlockSettings()
	want.ImageSizes = map[string]string{"thumbnail": "thumbnail", "medium": "medium", "large": "large", "huge": "huge"}
	want.Marks = map[string]string{"hl": ""}
	if !reflect.DeepEqual(s.Blocks, want) {
		t.Errorf("Blocks = %+v, want %+v", s.Blocks, want)
	}
	sameSet(t, "the entries", blockEntries(imp.Report), []string{
		`review: the images use 4 image sizes of WordPress: "medium" (5 times), "thumbnail" (5 times), "large" (2 times), "huge" (1 time)` +
			"; blocks.imageSizes maps them to the image sizes of the theme of the site",
		`review: the text uses 1 class of inline elements: "hl" (1 time)` +
			"; blocks.marks maps them to the highlight variants of the theme of the site, and the others keep only their text",
	})
}
