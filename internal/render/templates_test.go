package render

import (
	"io/fs"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// defaultBlockFiles returns the default templates as the files of the blocks/ of a theme, by path.
func defaultBlockFiles(t *testing.T) map[string]string {
	t.Helper()
	paths, err := fs.Glob(defaultFS, "blocks/*")
	if err != nil {
		t.Fatal(err)
	}
	files := make(map[string]string)
	for _, p := range paths {
		b, err := fs.ReadFile(defaultFS, p)
		if err != nil {
			t.Fatal(err)
		}
		files[p] = string(b)
	}
	return files
}

func TestDefaultBlocks(t *testing.T) {
	types := []content.BlockType{
		content.BlockParagraph, content.BlockHeading, content.BlockList, content.BlockListItem, content.BlockQuote,
		content.BlockImage, content.BlockTable, content.BlockTableRow, content.BlockTableCell, content.BlockButtons,
		content.BlockButton, content.BlockSeparator, content.BlockSpacer, content.BlockCode, content.BlockEmbed,
		content.BlockColumns, content.BlockColumn, content.BlockGroup, content.BlockFile, content.BlockForm,
	}
	var paths []string
	for _, bt := range types {
		paths = append(paths, "blocks/"+string(bt)+".html")
	}
	slices.Sort(paths)
	files := defaultBlockFiles(t)
	if got := slices.Sorted(maps.Keys(files)); !slices.Equal(got, paths) {
		t.Errorf("embedded files:\n%q\nwant:\n%q", got, paths)
	}
	if got := slices.Sorted(maps.Keys(defaultTemplates)); !slices.Equal(got, slices.Sorted(slices.Values(types))) {
		t.Errorf("default templates: %q, want %q", got, types)
	}
	for p, s := range files {
		if strings.TrimRight(s, "\r\n")+"\n" != s {
			t.Errorf("%s does not end with exactly one newline: %q", p, s)
		}
	}
	// A theme can copy them to its blocks/.
	testTheme(t, files)
}
