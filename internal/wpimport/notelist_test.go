package wpimport

import (
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wpblock"
)

// listConverter is the converter of a block that makes a separator, and notes things at it for each of the calls in its
// attribute calls: pairs of the name of the block and the names, separated by commas. If the attribute fail is true, it
// then fails; if at is "body", it notes at the body.
func listConverter(c *bodyConv, _ string, b *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
	blk := c.newBlock(content.BlockSeparator)
	at := noteAt{id: blk.ID}
	if where, _ := b.Attrs.Get("at").Str(); where == "body" {
		at = noteAt{}
	}
	for _, call := range b.Attrs.Get("calls").Array {
		name, _ := call.Array[0].Str()
		names, _ := call.Array[1].Str()
		if names == "" {
			c.lost(at, name)
		} else {
			c.lost(at, name, strings.Split(names, ",")...)
		}
	}
	if fail, _ := b.Attrs.Get("fail").IsTrue(); fail {
		return nil, legacyf("it fails")
	}
	return []content.Block{blk}, nil
}

func TestBodyNoteLists(t *testing.T) {
	withConverters(t, map[string]converter{"test/lost": listConverter})
	const tail = "; whiteroom has no such settings"
	lost := func(names, block, pointer string) string {
		return `skipped: lost ` + names + ` of the block "` + block + `"` + tail + " -> " + pointer
	}
	for _, tt := range []struct {
		name  string
		text  string
		lines []string
	}{
		{"one call", `<!-- wp:test/lost {"calls":[["core/x","a,b"]]} /-->`,
			[]string{lost("a, b", "core/x", "/body/0")}},
		{"calls for the same block are one note", `<!-- wp:test/lost {"calls":[["core/x","a,b"],["core/x","b,c"],["core/x","a,d,c"]]} /-->`,
			[]string{lost("a, b, c, d", "core/x", "/body/0")}},
		{"another name of the block is another note", `<!-- wp:test/lost {"calls":[["core/x","a"],["core/y","a"],["core/x","b"]]} /-->`,
			[]string{lost("a, b", "core/x", "/body/0"), lost("a", "core/y", "/body/0")}},
		{"another block is another note", `<!-- wp:test/lost {"calls":[["core/x","a"]]} /--><!-- wp:test/lost {"calls":[["core/x","a,b"]]} /-->`,
			[]string{lost("a", "core/x", "/body/0"), lost("a, b", "core/x", "/body/1")}},
		{"the body is a place too", `<!-- wp:test/lost {"at":"body","calls":[["core/x","a"],["core/x","b"]]} /-->`,
			[]string{lost("a, b", "core/x", "/body")}},
		{"ten things", `<!-- wp:test/lost {"calls":[["core/x","a,b,c,d,e,f,g,h,i,j"]]} /-->`,
			[]string{lost("a, b, c, d, e, f, g, h, i, j", "core/x", "/body/0")}},
		{"more than ten things", `<!-- wp:test/lost {"calls":[["core/x","a,b,c,d,e,f,g,h,i,j,k"],["core/x","l,a,m"]]} /-->`,
			[]string{lost("a, b, c, d, e, f, g, h, i, j, and 3 more", "core/x", "/body/0")}},
		{"things with names that need quotes", `<!-- wp:test/lost {"calls":[["core/x","a b,c.d,,e\"f"]]} /-->`,
			[]string{lost(`"a b", c.d, "", "e\"f"`, "core/x", "/body/0")}},
		{"no things", `<!-- wp:test/lost {"calls":[["core/x",""]]} /-->`, nil},
		{"a block that fails leaves no note", `<!-- wp:test/lost {"fail":true,"calls":[["core/x","a"]]} /-->`,
			[]string{legacyLine("it fails", "/body/0")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, imp := convertBody(t, bodySite(false), tt.text)
			wantLines(t, imp.Report, bodyLines(tt.lines...))
		})
	}
}
