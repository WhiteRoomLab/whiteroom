package render

import (
	"maps"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// testTheme loads the smallest valid theme with files, by path: theme.yaml (schemaVersion 1 unless files has it),
// templates/entry.html and templates/list.html.
func testTheme(t *testing.T, files map[string]string) *theme.Theme {
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
// media with the paths of their files, and forms, by ID.
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

func (r testResolver) Form(id string) (*content.Form, bool) {
	f, ok := r.forms[id]
	return f, ok
}

func newRenderer(t *testing.T, th *theme.Theme, language, base string, res Resolver) *Renderer {
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
	html, warnings := r.RichText(rt)
	if want := "会社概要と会社案内"; string(html) != want {
		t.Errorf("RichText() = %q, want %q", html, want)
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
	if f, ok := r.res.Form("frm_01JB8Y6T2SA3F6H9J2K5M8N1P4"); f != nil || ok {
		t.Errorf("Form() = %v, %v, want nothing", f, ok)
	}
}

// TestNew checks what New takes from the theme.Renderer and the Resolver.
func TestNew(t *testing.T) {
	th := testTheme(t, map[string]string{
		"theme.yaml": "schemaVersion: 1\nmarks:\n  highlight:\n    variants:\n      marker: マーカー\n      wave: 波線\n",
	})
	tr, err := th.Renderer("en-US", "/blog")
	if err != nil {
		t.Fatal(err)
	}
	r := New(tr, testResolver{entries: map[string][2]string{"ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3": {"About", "/about/"}}})
	if r.tr != tr || r.language != "en-US" || r.base != "/blog" {
		t.Errorf("tr, language, base = %p, %q, %q, want %p, en-US and /blog", r.tr, r.language, r.base, tr)
	}
	if _, path, ok := r.res.Entry("ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3"); path != "/about/" || !ok {
		t.Errorf("res.Entry() = %q, %v, want the entry of the Resolver", path, ok)
	}
	if want := map[string]bool{"marker": true, "wave": true}; !maps.Equal(r.highlights, want) {
		t.Errorf("highlights = %v, want %v", r.highlights, want)
	}
	// A zero Theme has no settings.
	if r := newRenderer(t, &theme.Theme{}, "ja", "", nil); len(r.highlights) != 0 {
		t.Errorf("highlights of a zero Theme = %v, want none", r.highlights)
	}
}
