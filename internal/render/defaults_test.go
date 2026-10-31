package render

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestDefaultTemplatesAsTheme checks that the default templates, copied to the blocks/ of a theme, load and render the
// golden documents as the core renders them for a theme without block templates.
func TestDefaultTemplatesAsTheme(t *testing.T) {
	res := goldenResolver(t)
	core, copied := testTheme(t, nil), testTheme(t, defaultBlockFiles(t))
	for _, site := range goldenSites {
		want := newRenderer(t, core, site.language, site.base, res)
		got := newRenderer(t, copied, site.language, site.base, res)
		for bt := range defaultTemplates {
			if !got.tr.HasBlock(bt) {
				t.Errorf("the theme has no blocks/%s.html", bt)
			}
		}
		for _, p := range goldenDocuments {
			doc := strings.TrimSuffix(filepath.Base(p), ".json")
			bs := goldenBlocks(t, p)
			wantHTML, wantWarnings, err := want.Document(bs)
			if err != nil {
				t.Fatalf("Document(%s) in %s error: %v", doc, site.language, err)
			}
			gotHTML, gotWarnings, err := got.Document(bs)
			if err != nil {
				t.Fatalf("Document(%s) in %s with the default templates in blocks/ error: %v", doc, site.language, err)
			}
			if !slices.Equal(gotHTML, wantHTML) {
				t.Errorf("Document(%s) in %s with the default templates in blocks/ =\n%s\nwant\n%s",
					doc, site.language, gotHTML, wantHTML)
			}
			if !slices.Equal(gotWarnings, wantWarnings) {
				t.Errorf("Document(%s) in %s with the default templates in blocks/: warnings\n%q\nwant\n%q",
					doc, site.language, gotWarnings, wantWarnings)
			}
		}
	}
}
