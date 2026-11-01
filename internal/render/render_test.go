package render

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// testTheme loads the smallest valid theme with files, by path: theme.yaml (schemaVersion 1 unless files has it),
// templates/entry.html and templates/list.html.
func testTheme(t testing.TB, files map[string]string) *theme.Theme {
	t.Helper()
	fsys := fstest.MapFS{
		"theme.yaml":           {Data: []byte("schemaVersion: 1\n")},
		"templates/entry.html": {Data: []byte("<p>entry</p>\n")},
		"templates/list.html":  {Data: []byte("<p>list</p>\n")},
	}
	for name, s := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(s)}
	}
	th, err := theme.Load(fsys)
	if err != nil {
		t.Fatalf("theme.Load() error:\n%v", err)
	}
	return th
}

// testResolver finds what its maps have: the title and the path of each entry ("" for one that is not published),
// media with the paths of their files and the files of their images, and forms, by ID.
type testResolver struct {
	entries map[string][2]string
	media   map[string]testMedium
	forms   map[string]*content.Form
}

type testMedium struct {
	m    *content.Media
	path string
}

func (r testResolver) Entry(id string) (title, path string, ok bool) {
	e, ok := r.entries[id]
	return e[0], e[1], ok
}

func (r testResolver) Media(id string) (*content.Media, string, bool) {
	m, ok := r.media[id]
	return m.m, m.path, ok
}

// Image returns the files of the medium with the ID much as the build makes them (6.6.2), but more simply: none for a
// medium without a width and a height. A GIF is an animation: its published file alone, shown at its size fit into
// size. At the default size, the published file, with a file 640 pixels wide before it in the srcset if it is wider;
// at an image size, the file of that size, then that of twice the size if it is wider. The path of a file is that of
// the medium, with "-<width>x<height>" before the extension unless it is the published file, and "?v=" and the first 8
// hex digits of the SHA-256 of the path after it.
func (r testResolver) Image(id string, size *content.ImageSize) (ImageFiles, bool) {
	x, ok := r.media[id]
	if !ok || x.m == nil || x.m.Width <= 0 || x.m.Height <= 0 {
		return ImageFiles{}, false
	}
	w, h := x.m.Width, x.m.Height
	file := func(s [2]int) string {
		p := x.path
		if s != [2]int{w, h} {
			ext := path.Ext(p)
			p = fmt.Sprintf("%s-%dx%d%s", strings.TrimSuffix(p, ext), s[0], s[1], ext)
		}
		sum := sha256.Sum256([]byte(p))
		return p + "?v=" + hex.EncodeToString(sum[:4])
	}
	src := [2]int{w, h}
	var srcset [][2]int
	switch {
	case strings.HasSuffix(x.path, ".gif"):
		if size != nil {
			shown := scaleImage(w, h, size.Width, size.Height, false)
			return ImageFiles{Src: file(src), Width: shown[0], Height: shown[1]}, true
		}
	case size == nil:
		if w > 640 {
			srcset = [][2]int{scaleImage(w, h, 640, 0, false), src}
		}
	default:
		src = scaleImage(w, h, size.Width, size.Height, size.Crop)
		if double := scaleImage(w, h, 2*size.Width, 2*size.Height, size.Crop); double[0] > src[0] {
			srcset = [][2]int{src, double}
		}
	}
	files := ImageFiles{Src: file(src), Width: src[0], Height: src[1]}
	for _, s := range srcset {
		files.Srcset = append(files.Srcset, ImageFile{Path: file(s), Width: s[0]})
	}
	return files, true
}

// scaleImage returns the size of an image w x h scaled down, if it is larger, to fit into maxW x maxH (0: no limit),
// or with crop, to maxW x maxH, or to the largest size with their ratio that the image has.
func scaleImage(w, h, maxW, maxH int, crop bool) [2]int {
	switch {
	case crop && w >= maxW && h >= maxH:
		return [2]int{maxW, maxH}
	case crop && w*maxH <= h*maxW:
		return [2]int{w, max((w*maxH+maxW/2)/maxW, 1)}
	case crop:
		return [2]int{max((h*maxW+maxH/2)/maxH, 1), h}
	case maxW > 0 && maxW < w && (maxH == 0 || maxW*h <= maxH*w):
		return [2]int{maxW, max((h*maxW+w/2)/w, 1)}
	case maxH > 0 && maxH < h:
		return [2]int{max((w*maxH+h/2)/h, 1), maxH}
	}
	return [2]int{w, h}
}

func (r testResolver) Form(id string) (*content.Form, bool) {
	f, ok := r.forms[id]
	return f, ok
}

// decodeTestdata decodes the file of ../content/testdata with the name with decode, such as the UnmarshalJSON method of
// a content.Form.
func decodeTestdata(t testing.TB, name string, decode func([]byte) error) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "content", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := decode(b); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

func newRenderer(t testing.TB, th *theme.Theme, language, base string, res Resolver) *Renderer {
	t.Helper()
	tr, err := th.Renderer(language, base)
	if err != nil {
		t.Fatalf("Renderer(%q, %q) error: %v", language, base, err)
	}
	return New(tr, res)
}

// warningStrings returns the String of each of ws.
func warningStrings(ws []Warning) []string {
	var s []string
	for _, w := range ws {
		s = append(s, w.String())
	}
	return s
}

func TestWarningString(t *testing.T) {
	tests := []struct {
		w    Warning
		want string
	}{
		{
			Warning{Path: "/2/attrs/link", Msg: `entry "ent_01JB8Y6T2WT6X9C3E6G9J2K5M8" is not published; rendered without the link`},
			`/2/attrs/link: entry "ent_01JB8Y6T2WT6X9C3E6G9J2K5M8" is not published; rendered without the link`,
		},
		{Warning{List: 1, Path: "/0/content/3/marks/1", Msg: "m"}, "/0/content/3/marks/1: m"},
		{Warning{Msg: "m"}, "m"},
		{Warning{List: 2, Msg: "m"}, "m"},
		{Warning{}, ""},
	}
	for _, tt := range tests {
		if got := tt.w.String(); got != tt.want {
			t.Errorf("%+v.String() = %q, want %q", tt.w, got, tt.want)
		}
	}
}

// TestNilResolver checks that a Renderer without a Resolver finds nothing.
func TestNilResolver(t *testing.T) {
	r := newRenderer(t, testTheme(t, nil), "ja", "", nil)
	rt := content.RichText{
		{Type: content.InlineText, Text: "会社概要", Marks: []content.Mark{
			{Type: content.MarkLink, Link: &content.Link{Entry: "ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3"}},
		}},
		{Type: content.InlineText, Text: "と"},
		{Type: content.InlineText, Text: "会社案内", Marks: []content.Mark{
			{Type: content.MarkLink, Link: &content.Link{Media: "med_01JB8Z3K5TM8A1C5F9V3J6K2N7"}},
		}},
	}
	html, css, warnings := r.RichText(rt)
	if want := "会社概要と会社案内"; string(html) != want || css.rules != nil {
		t.Errorf("RichText() = %q, %q, want %q and no CSS", html, css, want)
	}
	want := []string{
		`/0/marks/0: entry "ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3" does not exist; rendered without the link`,
		`/2/marks/0: media "med_01JB8Z3K5TM8A1C5F9V3J6K2N7" does not exist; rendered without the link`,
	}
	if got := warningStrings(warnings); !slices.Equal(got, want) {
		t.Errorf("warnings:\n%q\nwant:\n%q", got, want)
	}
	if title, path, ok := r.res.Entry("ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3"); title != "" || path != "" || ok {
		t.Errorf("Entry() = %q, %q, %v, want nothing", title, path, ok)
	}
	if m, path, ok := r.res.Media("med_01JB8Z3K5TM8A1C5F9V3J6K2N7"); m != nil || path != "" || ok {
		t.Errorf("Media() = %v, %q, %v, want nothing", m, path, ok)
	}
	if files, ok := r.res.Image("med_01JB8Z3K5QH2V6T9C4R7Y1E8B5", nil); !reflect.DeepEqual(files, ImageFiles{}) || ok {
		t.Errorf("Image() = %+v, %v, want nothing", files, ok)
	}
	if f, ok := r.res.Form("frm_01JB8Y6T2SA3F6H9J2K5M8N1P4"); f != nil || ok {
		t.Errorf("Form() = %v, %v, want nothing", f, ok)
	}
}

// TestNew checks what New takes from the theme.Renderer and the Resolver.
func TestNew(t *testing.T) {
	th := testTheme(t, map[string]string{
		"theme.yaml": "schemaVersion: 1\nblocks:\n  paragraph:\n    variants:\n      lead: リード\n      note: 注記\n" +
			"  code:\n    styles: [fontSize]\n  group:\n    variants:\n      box: ボックス\n" +
			"marks:\n  highlight:\n    variants:\n      marker: マーカー\n      wave: 波線\n",
	})
	tr, err := th.Renderer("en-US", "/blog")
	if err != nil {
		t.Fatal(err)
	}
	r := New(tr, testResolver{entries: map[string][2]string{"ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3": {"About", "/about/"}}})
	if r.tr != tr || r.language != "en-US" || r.base != "/blog" || r.settings != th.Settings || r.settings == nil {
		t.Errorf("tr, language, base, settings = %p, %q, %q, %p, want %p, en-US, /blog and %p",
			r.tr, r.language, r.base, r.settings, tr, th.Settings)
	}
	if _, path, ok := r.res.Entry("ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3"); path != "/about/" || !ok {
		t.Errorf("res.Entry() = %q, %v, want the entry of the Resolver", path, ok)
	}
	if want := map[string]bool{"marker": true, "wave": true}; !maps.Equal(r.highlights, want) {
		t.Errorf("highlights = %v, want %v", r.highlights, want)
	}
	want := map[content.BlockType]map[string]bool{
		content.BlockParagraph: {"lead": true, "note": true}, content.BlockGroup: {"box": true},
	}
	if !maps.EqualFunc(r.variants, want, maps.Equal) {
		t.Errorf("variants = %v, want %v", r.variants, want)
	}
	// A zero Theme has no settings.
	r = newRenderer(t, &theme.Theme{}, "ja", "", nil)
	if len(r.highlights) != 0 || len(r.variants) != 0 || r.settings != nil {
		t.Errorf("highlights, variants and settings of a zero Theme = %v, %v, %p, want none",
			r.highlights, r.variants, r.settings)
	}
}
