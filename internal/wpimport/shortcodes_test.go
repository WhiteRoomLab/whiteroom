package wpimport

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/html"
	"github.com/WhiteRoomLab/whiteroom/internal/wpformat"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

const scPost = 101 // the post that bodyItem converts: the attachments of the galleries are its children

// shortcodeExport is mediaExport, and the attachments of the post 101: 41, 42, 43, 47 and 48 are images that are imported (41
// and 48 have an excerpt, 43 has no alt), 44 is one that is not imported, 45 is a PDF that is imported, and 46 is an image of
// another post; and the forms 34 and 35 of Contact Form 7 with the numbers 5 and 6, which the old shortcode names, of which 34
// is imported.
func shortcodeExport() *wxr.Export {
	e := mediaExport()
	att := func(id, parent, menu int64, ext, excerpt string, alt bool) {
		name := "g" + strconv.FormatInt(id, 10) + ext
		meta := wxr.MetaList{{Key: "_wp_attached_file", Value: "2026/10/" + name}}
		if alt {
			meta = append(meta, wxr.Meta{Key: "_wp_attachment_image_alt", Value: "Alt " + strconv.FormatInt(id, 10)})
		}
		e.Items = append(e.Items, wxr.Item{
			ID: id, Type: "attachment", Status: "inherit", Title: "G" + strconv.FormatInt(id, 10), Parent: parent, MenuOrder: menu,
			Excerpt: excerpt, Link: "https://example.com/" + name + "/", AttachmentURL: "https://example.com/wp-content/uploads/2026/10/" + name,
			Meta: meta,
		})
	}
	att(41, scPost, 3, ".jpg", "First <em>one</em>", true)
	att(42, scPost, 1, ".jpg", "", true)
	att(43, scPost, 2, ".jpg", "<img src=\"x.png\">", false)
	att(44, scPost, 0, ".jpg", "", true)
	att(45, scPost, 5, ".pdf", "", false)
	att(46, 999, 0, ".jpg", "", true)
	att(47, scPost, 1, ".jpg", "", true)
	att(48, scPost, 4, ".jpg", "  \n", true)
	form := func(id int64, title, hash, old string) {
		e.Items = append(e.Items, wxr.Item{
			ID: id, Type: formType, Status: "publish", Title: title,
			Meta: wxr.MetaList{{Key: "_hash", Value: hash}, {Key: "_old_cf7_unit_id", Value: old}},
		})
	}
	form(34, "Old form", "abcdef0123456789", "5")
	form(35, "Old quote", "fedcba9876543210", " 6 ")
	return e
}

// shortcodeIDs are the IDs in the site of the things of shortcodeExport that makeShortcodeImport imports, by the post IDs.
type shortcodeIDs map[int64]string

// makeShortcodeImport is the import of shortcodeExport in which the same things as in mediaExport are imported, and the images
// 41 to 43, 46 to 48, the PDF 45 and the form 34; and the IDs of the things of the export that have one.
func makeShortcodeImport() (*Import, shortcodeIDs, error) {
	e := shortcodeExport()
	imp := New("site.xml", e, NewReport(Info{File: "site.xml"}))
	ids := make(shortcodeIDs)
	for _, a := range []struct {
		id   int64
		kind content.RefKind
		ref  content.Ref
	}{
		{12, content.RefEntry, content.Ref{Type: content.TypePage, Language: "ja"}},
		{21, content.RefMedia, content.Ref{Type: content.MediaImage}},
		{22, content.RefMedia, content.Ref{Type: content.MediaImage}},
		{23, content.RefMedia, content.Ref{Type: content.MediaPDF}},
		{31, content.RefForm, content.Ref{}},
		{34, content.RefForm, content.Ref{}},
		{41, content.RefMedia, content.Ref{Type: content.MediaImage}},
		{42, content.RefMedia, content.Ref{Type: content.MediaImage}},
		{43, content.RefMedia, content.Ref{Type: content.MediaImage}},
		{45, content.RefMedia, content.Ref{Type: content.MediaPDF}},
		{46, content.RefMedia, content.Ref{Type: content.MediaImage}},
		{47, content.RefMedia, content.Ref{Type: content.MediaImage}},
		{48, content.RefMedia, content.Ref{Type: content.MediaImage}},
	} {
		id, err := imp.IDs.Assign(PostObject(e.Post(a.id)), a.kind)
		if err != nil {
			return nil, nil, err
		}
		imp.AddRef(a.kind, id, a.ref)
		ids[a.id] = id
	}
	// Not imported, but with an ID, as the plan gives one to every attachment.
	for _, post := range []int64{44, 32, 35} {
		id, err := imp.IDs.Assign(PostObject(e.Post(post)), content.RefMedia)
		if err != nil {
			return nil, nil, err
		}
		ids[post] = id
	}
	return imp, ids, nil
}

func shortcodeImport(t testing.TB) *Import {
	t.Helper()
	imp, _, err := makeShortcodeImport()
	if err != nil {
		t.Fatal(err)
	}
	return imp
}

// scIDs are the IDs of the things of shortcodeExport, by their post IDs.
var scIDs = func() shortcodeIDs {
	_, ids, err := makeShortcodeImport()
	if err != nil {
		panic(err)
	}
	return ids
}()

func jsonEmbed(n int, provider, url string) string {
	return jsonBlock("embed", n, "", fmt.Sprintf(`"provider":%q,"url":%q`, provider, url), "")
}

func jsonFormBlock(n int, form string) string {
	return jsonBlock("form", n, "", fmt.Sprintf(`"form":%q`, form), "")
}

// scImg is an image of the post 21 as the classic editor writes it in a caption, with the class.
func scImg(class string) string {
	return `<img class="` + class + `" src="` + photoURL + `" alt="Photo" width="300" height="200" />`
}

// checkNoPieces checks that nothing of the conversion of text, the blocks and the report, has the name of the elements that stand
// for shortcodes and URLs, unless the text has it: they are gone from the result, or put back as what they stood for.
func checkNoPieces(t testing.TB, text string, imp *Import, res *BodyResult) {
	t.Helper()
	if strings.Contains(strings.ToLower(text), pieceBase) {
		return
	}
	doc, err := res.Blocks.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(doc), pieceBase) {
		t.Errorf("the blocks have the name of the elements that stand for the pieces: %.300s", doc)
	}
	for _, e := range imp.Report.Entries() {
		if strings.Contains(e.Message, pieceBase) {
			t.Errorf("the report has the name of the elements that stand for the pieces: %s", e.Message)
		}
	}
}

func TestShortcodeNames(t *testing.T) {
	for _, tt := range []struct {
		name string
		next byte
		want bool
	}{
		{"caption", ']', true},
		{"gallery", ' ', true},
		{"gallery", '.', true},
		{"wp_caption", ']', true},
		{"contact-form-7", ' ', true},
		{"contact-form", ' ', true},
		{"embed", ']', true},
		{"video", 0, true},
		{"audio", ']', true},
		{"playlist", ']', true},
		{"Caption", ']', false},
		{"my_button", ']', true},
		{"vc_row", ' ', true},
		{"foo-bar", ' ', true},
		{"foo", '/', true},
		{"foo", '\n', true},
		{"foo", '\t', true},
		{"foo", '=', false},
		{"foo", '.', false},
		{"foo", '"', false},
		{"foo", 0, false},
		{"foo", '<', true}, // wpautop puts a br before a line break in a tag
		{"foo", '>', false},
		{"foo", '&', false},
		{"my_shortcode", '<', true},
		{"Foo", '<', false},
		{"a1", ']', true},
		{"PR", ']', false},
		{"Foo", ']', false},
		{"fooBar", ']', false},
		{"1", ']', false},
		{"1a", ']', false},
		{"_a", ']', false},
		{"-a", ']', false},
		{"", ']', false},
		{"a", ']', true},
	} {
		if got := isShortcodeName(tt.name, tt.next); got != tt.want {
			t.Errorf("isShortcodeName(%q, %q) = %v, want %v", tt.name, tt.next, got, tt.want)
		}
	}
}

func TestShortcodeNamesInText(t *testing.T) {
	// What is not the shape of a shortcode is text, and so is the text that WordPress itself takes for a shortcode of no one.
	found := func(s string) []string {
		var names []string
		for _, sc := range wpformat.FindShortcodes(s, textShortcodeFilter()) {
			names = append(names, sc.Name)
		}
		return names
	}
	for in, want := range map[string]string{
		"[PR] [1] [注1] [a.b] [A] [a b]x": "a",
		"[foo-bar x] [foo/] [foo]":       "foo-bar,foo,foo",
		"[foo=1]":                        "",
		"[ foo]":                         "",
	} {
		if got := strings.Join(found(in), ","); got != want {
			t.Errorf("the shortcodes of %q are %q, want %q", in, got, want)
		}
	}
}

func TestPieceName(t *testing.T) {
	var all strings.Builder
	all.WriteString("<x-wr-piece>")
	for i := 2; i < pieceNames; i++ {
		fmt.Fprintf(&all, "<x-wr-piece-%d>", i)
	}
	for _, tt := range []struct{ name, text, want string }{
		{"empty", "", "x-wr-piece"},
		{"none", "<p>x</p> <x-wr>", "x-wr-piece"},
		{"the name", "a <x-wr-piece>b", "x-wr-piece-2"},
		{"a name in capitals", "<X-WR-PIECE data-n='1'>", "x-wr-piece-2"},
		{"a longer name", "<x-wr-pieces>", "x-wr-piece-2"},
		{"the second name", "<x-wr-piece><X-WR-PIECE-2 a>", "x-wr-piece-3"},
		{"the second and the third", "<x-wr-piece-2><x-wr-piece-3><x-wr-piece>", "x-wr-piece-4"},
		{"a name that the longer name starts with", "<x-wr-piece><x-wr-piece-23>", "x-wr-piece-3"},
		{"a name that starts with the first digit", "<x-wr-piece><x-wr-piece-12>", "x-wr-piece-2"},
		{"a number with a zero", "<x-wr-piece><x-wr-piece-02>", "x-wr-piece-2"},
		{"a dash and no number", "<x-wr-piece-><x-wr-piece-x>", "x-wr-piece-2"},
		{"a name that is not at the start of an element", "x-wr-piece <b x-wr-piece>", "x-wr-piece"},
		{"a long number", "<x-wr-piece><x-wr-piece-123456789>", "x-wr-piece-2"},
		{"a < at the end of the text", "a<", "x-wr-piece"},
		{"a name and a < at the end of the text", "<x-wr-piece-2><", "x-wr-piece-3"},
		{"all the names", all.String(), ""},
	} {
		if got := pieceNameFor(tt.text); got != tt.want {
			t.Errorf("%s: pieceNameFor(%.60q) = %q, want %q", tt.name, tt.text, got, tt.want)
		}
	}
}

func TestPhpNumbers(t *testing.T) {
	for in, want := range map[string]int64{
		"": 0, "12": 12, " 12": 12, "\t\n12abc": 12, "+5": 5, "-5": -5, "- 5": 0, "abc": 0, "1.9": 1, "0x1A": 0, "007": 7,
		"9223372036854775807": 9223372036854775807, "9223372036854775808": 9223372036854775807,
		"-9223372036854775808": -9223372036854775808, "-9223372036854775809": -9223372036854775808, "99999999999999999999999": 9223372036854775807,
		"\v\f\r 3": 3, "\xc2\xa03": 0,
	} {
		if got := phpToInt(in); got != want {
			t.Errorf("phpToInt(%q) = %d, want %d", in, got, want)
		}
	}
	for in, want := range map[string][]int64{
		"":                        nil,
		"1,2,3":                   {1, 2, 3},
		"3, 1 ,2":                 {3, 1, 2},
		"1 1\t2\n1,,2":            {1, 2},
		"0,abc,-4,5x,1.5":         {4, 5, 1},
		",,,":                     nil,
		"9223372036854775808,7":   {9223372036854775807, 7},
		"-9223372036854775808, 2": {2},
		"1e3":                     {1},
	} {
		got := idList(in)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("idList(%q) = %v, want %v", in, got, want)
		}
	}
	for in, want := range map[string]bool{"": true, "0": true, "00": false, "1": false, " ": false, "a": false} {
		if got := phpEmpty(in); got != want {
			t.Errorf("phpEmpty(%q) = %v, want %v", in, got, want)
		}
	}
	if got := phpTrim(" \t\n\r\x00\v\fx \x00"); got != "\fx" {
		t.Errorf("phpTrim = %q, want the form feed to stay", got)
	}
}

func TestStripTags(t *testing.T) {
	for in, want := range map[string]string{
		"":                       "",
		"https://a/b":            "https://a/b",
		"<br />https://a/b<br>":  "https://a/b",
		"<p>https://a</p>":       "https://a",
		"a<b":                    "a<b",
		"a<b>c<d":                "ac<d",
		"<a href=\"x>y\">z</a>":  "y\">z",
		"<><>":                   "",
		"<!-- c -->x":            "x",
		"x < y and y > z":        "x  z",
		"https://a/?b=1&amp;c=2": "https://a/?b=1&amp;c=2",
	} {
		if got := stripTags(in); got != want {
			t.Errorf("stripTags(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPlaceWalker(t *testing.T) {
	// The positions that are marked with ^ are asked about, in order; the rest of the text is only text. The walker says that a
	// position is blocked if it is in a tag, a comment, a CDATA section, or the text of an element that is read as it is.
	for _, tt := range []struct {
		name string
		in   string
		want string // for each ^: b if the position is blocked, t if not
	}{
		{"text", "a ^ b", "t"},
		{"a tag", "a <b ^class=x> ^b", "bt"},
		{"the end of a tag is after it", "<b>^x", "t"},
		{"inside a tag", "<b^>x", "b"},
		{"a comment", "<!-- ^x --> ^y", "bt"},
		{"a comment that is not closed", "a <!-- ^x ^y", "bb"},
		{"a CDATA section", "<![CDATA[ ^x ]]> ^y", "bt"},
		{"a quoted value that has a >", `<a title="x > ^y">^z</a>`, "bt"},
		{"a value in single quotes", `<a title='x > ^y' href=z>^z</a>`, "bt"},
		{"a value that is not quoted", `<a title=x>^y`, "t"},
		{"a quote that is never closed", `<a title="x > ^y`, "b"},
		{"an end tag with a quoted value that has a >", `</a b=">^x">^y`, "bt"},
		{"an end tag", `</a>^x</p >^y`, "tt"},
		{"a slash in a tag", `<br/>^x <img src=a/^b>`, "tb"},
		{"script", "<script>^var a;</script>^x", "bt"},
		{"script in capitals", "<SCRIPT type=x>^a</Script >^b", "bt"},
		{"script that is not closed", "<script>^a ^b", "bb"},
		{"style and title and textarea", "<style>^a</style><title>^b</title><textarea>^c</textarea>^d", "bbbt"},
		{"xmp, iframe, noembed, noframes and noscript", "<xmp>^a</xmp><iframe>^b</iframe><noembed>^c</noembed><noframes>^d</noframes><noscript>^e</noscript>^f", "bbbbbt"},
		{"plaintext", "<plaintext>^a </plaintext> ^b", "bb"},
		{"an element that is not one", "<scripts>^a</scripts> <script-x>^b", "tt"},
		{"an end tag that is not the end", "<script>^a</scripts>^b</script>^c", "bbt"},
		{"an end tag in a comment is no end", "<script>^a<!-- </script> -->^b</script>^c", "btt"},
		{"a < with a space is an element for WordPress", "a < ^b > ^c", "bt"},
		{"a tag that ends the text", "<b", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var text strings.Builder
			var at []int
			for _, r := range strings.Split(tt.in, "^") {
				if text.Len() > 0 || len(at) > 0 {
					at = append(at, text.Len())
				}
				text.WriteString(r)
			}
			w := placeWalker{s: text.String()}
			got := ""
			for _, p := range at {
				if w.blocked(p) {
					got += "b"
				} else {
					got += "t"
				}
			}
			if got != tt.want {
				t.Errorf("%q: blocked = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestClassicShortcodeNameFollowedByLineBreak(t *testing.T) {
	// wpautop puts a br before a line break, so the name is followed by "<": it is the name of a shortcode as if by a space.
	const sc = "the shortcode [my_shortcode] is not converted"
	runClassic(t, []classicCase{
		{name: "attributes on lines of their own", text: "[my_shortcode\n  a=\"1\"\n  b=\"2\"]",
			want:  jsonList(jsonLegacy(0, "[my_shortcode<br />\n  a=\"1\"<br />\n  b=\"2\"]", sc)),
			lines: []string{legacyLine(sc, "/body/0")}},
		{name: "in a sentence", text: "Text [my_shortcode\n a=\"1\"] more",
			want:  jsonList(jsonLegacy(1, "<p>Text [my_shortcode<br />\n a=\"1\"] more</p>", "the text has the shortcode [my_shortcode], which whiteroom does not run")),
			lines: []string{legacyLine("the text has the shortcode [my_shortcode], which whiteroom does not run", "/body/0")}},
		{name: "a name that has the shape only in lower case", text: "[My_shortcode\n a=\"1\"]",
			want: jsonList(jsonBlock("paragraph", 0, "", "", jsonList(jsonText("[My_shortcode"), jsonBreak, jsonText(`a="1"]`))))},
		{name: "a name in the tag of an element", text: `<abbr title="[my_shortcode` + "\n" + `x]">a</abbr>`,
			want:  jsonList(jsonBlock("paragraph", 0, "", "", jsonList(jsonText("a")))),
			lines: []string{"changed: kept only the text of <abbr> -> /body/0"}},
	})
}

func TestClassicShortcodesInCodeDoNotUseTheLimit(t *testing.T) {
	// The brackets that are left as text in code are not counted among the shortcodes that the conversion makes elements of.
	lowerLimit(t, &maxWPBlocks, 10)
	code := strings.Repeat("a[i]", 50)
	runClassic(t, []classicCase{
		{name: "many brackets in code, and a shortcode after them", text: "<pre>" + code + "</pre>\n\n[box]x[/box]",
			want: jsonList(jsonBlock("code", 0, "", "", strconv.Quote(code)),
				jsonLegacy(1, "[box]x[/box]", "the shortcode [box] is not converted")),
			lines: []string{legacyLine("the shortcode [box] is not converted", "/body/1")}},
		{name: "many brackets in code elements in a paragraph, and a shortcode in it", text: "<code>" + code + "</code> [box]x[/box] <code>" + code + "</code>",
			want: jsonList(jsonLegacy(1, "<p><code>"+code+"</code> [box]x[/box] <code>"+code+"</code></p>",
				"the text has the shortcode [box], which whiteroom does not run")),
			lines: []string{legacyLine("the text has the shortcode [box], which whiteroom does not run", "/body/0")}},
	})
}

func TestPlaceWalkerInCode(t *testing.T) {
	// The positions that are marked with ^ are asked about, in order: y if one is in a pre or code element, n if not.
	for _, tt := range []struct{ name, in, want string }{
		{"text", "a ^ b", "n"},
		{"a pre", "<pre>^a</pre>^b", "yn"},
		{"a code", "x <code>^y</code> ^z", "yn"},
		{"a code in a pre", "<pre><code>^a</code>^b</pre>^c", "yyn"},
		{"capitals", "<PRE class=x>^a</Pre >^b", "yn"},
		{"a slash is no end", "<code/>^a</code>^b", "yn"},
		{"a pre that is not closed", "<pre>^a ^b", "yy"},
		{"an end tag with nothing open", "</code>^a<code>^b", "ny"},
		{"two pre elements", "<pre>^a</pre>^b<pre>^c</pre>^d", "ynyn"},
		{"names that are longer", "<prefix>^a<codes>^b<pre-x>^c<code_>^d", "nnnn"},
		{"a pre in a comment", "<!-- <pre> -->^a", "n"},
		{"a pre in the text of a script", "<script>^a<pre>^b</script>^c", "nnn"},
		{"a pre in a value", `<a title="<pre>">^a`, "n"},
		{"a tag that is cut", "<pre>^a<code", "y"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var text strings.Builder
			var at []int
			for i, r := range strings.Split(tt.in, "^") {
				if i > 0 {
					at = append(at, text.Len())
				}
				text.WriteString(r)
			}
			w := placeWalker{s: text.String()}
			got := ""
			for _, p := range at {
				if w.inCode(p) {
					got += "y"
				} else {
					got += "n"
				}
			}
			if got != tt.want {
				t.Errorf("%q: in code = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestPlaceholdersAreRestored(t *testing.T) {
	c := newBodyConv(bodyImport(), BodyOptions{Site: bodySite(false), Item: bodyItem("x"), Target: bodyTarget, Key: bodyKey})
	c.pieces = []*classicPiece{
		{name: "x-wr-piece", text: "[a]"},
		{name: "x-wr-piece", text: "https://u/?a=1&amp;b=2"},
		{name: "x-wr-piece-2", text: "[b x=<y>]"},
	}
	for _, tt := range []struct{ name, in, want string }{
		{"nothing", "<p>x</p>", "<p>x</p>"},
		{"a piece", `<p><x-wr-piece data-n="0"></x-wr-piece></p>`, "<p>[a]</p>"},
		{"two pieces and a name with a number", `<x-wr-piece data-n="1"></x-wr-piece>-<x-wr-piece-2 data-n="2"></x-wr-piece-2>`, "https://u/?a=1&amp;b=2-[b x=<y>]"},
		{"a piece of the other name", `<x-wr-piece data-n="2"></x-wr-piece>`, `<x-wr-piece data-n="2"></x-wr-piece>`},
		{"a piece that is not there", `<x-wr-piece data-n="3"></x-wr-piece>`, `<x-wr-piece data-n="3"></x-wr-piece>`},
		{"an element that is changed", `<x-wr-piece data-n="0" class="x"></x-wr-piece>`, `<x-wr-piece data-n="0" class="x"></x-wr-piece>`},
		{"an end tag of another name", `<x-wr-piece data-n="0"></x-wr-piece-2>`, `<x-wr-piece data-n="0"></x-wr-piece-2>`},
		{"no number", `<x-wr-piece data-n=""></x-wr-piece>`, `<x-wr-piece data-n=""></x-wr-piece>`},
		{"a number that is too big", `<x-wr-piece data-n="99999999999999999999999"></x-wr-piece>`, `<x-wr-piece data-n="99999999999999999999999"></x-wr-piece>`},
		{"cut", `<x-wr-piece data-n="0"></x-wr-pie`, `<x-wr-piece data-n="0"></x-wr-pie`},
		{"cut at the name", `a<x-wr-piece`, `a<x-wr-piece`},
		{"a piece after another's text, which is not read again", `<x-wr-piece data-n="2"></x-wr-piece>`, `<x-wr-piece data-n="2"></x-wr-piece>`},
	} {
		if got := c.restorePieces(tt.in); got != tt.want {
			t.Errorf("%s: restorePieces(%q) = %q, want %q", tt.name, tt.in, got, tt.want)
		}
	}
	// The text of a piece is put in as it is: what looks like an element in it is not read again.
	c.pieces = append(c.pieces, &classicPiece{name: "x-wr-piece", text: `<x-wr-piece data-n="0"></x-wr-piece>`})
	if got, want := c.restorePieces(`<x-wr-piece data-n="3"></x-wr-piece>`), `<x-wr-piece data-n="0"></x-wr-piece>`; got != want {
		t.Errorf("restorePieces = %q, want %q", got, want)
	}
}

func TestStartTagEnd(t *testing.T) {
	for in, want := range map[string]int{
		"<b>x":                    3,
		"<b class=x>":             11,
		`<b a="x>y" b>z`:          13,
		`<b a='x>y'>`:             11,
		"<b a=x>y>":               7,
		`<b a = "x" >`:            12,
		`<b a=">`:                 7,
		"<b a b c>":               9,
		"<b/>":                    4,
		"<b / a=x/>":              10,
		"<b =>x>":                 5,
		"<b":                      2,
		`<b a="x"y="z>">w`:        15,
		"<b a=\"x\"\n\tb='>'\r>x": 17,
	} {
		if got := startTagEnd(in, 0); got != want {
			t.Errorf("startTagEnd(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestClassicShortcodeCaption(t *testing.T) {
	const reasonCaptionAttr = "the shortcode [caption] has the attribute caption and content other than the image"
	photo, about := mediaIDs.photo, mediaIDs.about
	rich := func(text string, marks ...string) string { return jsonText(text, marks...) }
	runClassic(t, []classicCase{
		{name: "an image and the text after it", text: `[caption id="attachment_21" align="alignleft" width="300"]` + scImg("size-medium wp-image-21") + ` Caption <em>text</em>[/caption]`,
			want: jsonList(jsonImage(0, photo, "", `"alt":"Photo","caption":[`+rich("Caption ")+`,`+rich("text", `{"type":"italic"}`)+`],"size":"medium","align":"left"`))},
		{name: "wp_caption is the same", text: `[wp_caption width="300" align="aligncenter"]` + scImg("wp-image-21") + ` Words[/wp_caption]`,
			want: jsonList(jsonImage(0, photo, "", `"alt":"Photo","caption":[`+rich("Words")+`],"align":"center"`))},
		{name: "the alignment of the image wins over the one of the shortcode", text: `[caption width="300" align="alignright"]` + scImg("alignleft wp-image-21") + ` Words[/caption]`,
			want: jsonList(jsonImage(0, photo, "", `"alt":"Photo","caption":[`+rich("Words")+`],"align":"left"`))},
		{name: "alignnone is no alignment", text: `[caption width="300" align="alignnone"]` + scImg("wp-image-21") + ` Words[/caption]`,
			want: jsonList(jsonImage(0, photo, "", `"alt":"Photo","caption":[`+rich("Words")+`]`))},
		{name: "a link around the image", text: `[caption width="300"]<a href="https://example.com/about/">` + scImg("wp-image-21") + `</a> Words[/caption]`,
			want: jsonList(jsonImage(0, photo, "", `"alt":"Photo","caption":[`+rich("Words")+`],"link":{"entry":"`+about+`"}`))},
		{name: "a link around the image and a space", text: `[caption width="300"]<a href="https://example.com/about/"> ` + scImg("wp-image-21") + ` </a>Words[/caption]`,
			want: jsonList(jsonImage(0, photo, "", `"alt":"Photo","caption":[`+rich("Words")+`],"link":{"entry":"`+about+`"}`))},
		{name: "the text after the link is the caption, as WordPress takes the first image and what follows it", text: `[caption width="300"]before <a href="https://example.com/about/">` + scImg("wp-image-21") + `</a> after <b>bold</b>[/caption]`,
			want: jsonList(jsonImage(0, photo, "", `"alt":"Photo","caption":[`+rich("after ")+`,`+rich("bold", jsonBold)+`],"link":{"entry":"`+about+`"}`))},
		{name: "a link that holds text beside the image is the link of the image, and what follows the image is the caption", text: `[caption width="300"]<a href="https://example.com/about/">` + scImg("wp-image-21") + ` in the link</a> out[/caption]`,
			want: jsonList(jsonImage(0, photo, "", `"alt":"Photo","caption":[`+rich("in the link out")+`],"link":{"entry":"`+about+`"}`))},
		{name: "what follows the image is the caption wherever it is", text: `[caption width="300"]<em><span>` + scImg("wp-image-21") + ` one</span> two</em> three[/caption]`,
			want: jsonList(jsonImage(0, photo, "", `"alt":"Photo","caption":[`+rich("one two three")+`]`))},
		{name: "the caption is the attribute", text: `[caption width="300" caption="Via <b>attr</b> &amp; more"]` + scImg("wp-image-21") + `[/caption]`,
			want: jsonList(jsonImage(0, photo, "", `"alt":"Photo","caption":[`+rich("Via ")+`,`+rich("attr", jsonBold)+`,`+rich(" & more")+`]`))},
		{name: "an attribute that is empty is a caption that is empty", text: `[caption width="300" caption=""]` + scImg("wp-image-21") + `[/caption]`,
			want: jsonList(jsonImage(0, photo, "", `"alt":"Photo"`))},
		// WordPress shows all of the content when the attribute is there, so the text beside the image would be lost.
		{name: "the attribute and text beside the image: a legacy block",
			text:  `[caption width="300" caption="C"]before ` + scImg("wp-image-21") + ` after[/caption]`,
			want:  jsonList(jsonLegacy(0, `[caption width="300" caption="C"]before `+scImg("wp-image-21")+` after[/caption]`, reasonCaptionAttr)),
			lines: []string{legacyLine(reasonCaptionAttr, "/body/0")}},
		{name: "an empty attribute and text beside the image: a legacy block",
			text:  `[caption width="300" caption=""]` + scImg("wp-image-21") + ` text[/caption]`,
			want:  jsonList(jsonLegacy(0, `[caption width="300" caption=""]`+scImg("wp-image-21")+` text[/caption]`, reasonCaptionAttr)),
			lines: []string{legacyLine(reasonCaptionAttr, "/body/0")}},
		{name: "the attribute and text in the link of the image: a legacy block",
			text:  `[caption width="300" caption="C"]<a href="https://example.com/about/">` + scImg("wp-image-21") + ` in the link</a>[/caption]`,
			want:  jsonList(jsonLegacy(0, `[caption width="300" caption="C"]<a href="https://example.com/about/">`+scImg("wp-image-21")+` in the link</a>[/caption]`, reasonCaptionAttr)),
			lines: []string{legacyLine(reasonCaptionAttr, "/body/0")}},
		{name: "the attribute and an element beside the image: a legacy block",
			text:  `[caption width="300" caption="C"]<em>` + scImg("wp-image-21") + `</em><b>b</b>[/caption]`,
			want:  jsonList(jsonLegacy(0, `[caption width="300" caption="C"]<em>`+scImg("wp-image-21")+`</em><b>b</b>[/caption]`, reasonCaptionAttr)),
			lines: []string{legacyLine(reasonCaptionAttr, "/body/0")}},
		{name: "the attribute and only white space, line breaks and comments beside the image",
			text: "[caption width=\"300\" caption=\"C\"] <!-- c -->" + scImg("wp-image-21") + "&nbsp;<br />\n[/caption]",
			want: jsonList(jsonImage(0, photo, "", `"alt":"Photo","caption":[`+rich("C")+`]`))},
		{name: "no width, no caption", text: `[caption]` + scImg("wp-image-21") + ` text[/caption]`, want: jsonList(jsonImage(0, photo, "", `"alt":"Photo"`))},
		{name: "a width of 0", text: `[caption width="0"]` + scImg("wp-image-21") + ` text[/caption]`, want: jsonList(jsonImage(0, photo, "", `"alt":"Photo"`))},
		{name: "a width that is not a number", text: `[caption width="wide"]` + scImg("wp-image-21") + ` text[/caption]`, want: jsonList(jsonImage(0, photo, "", `"alt":"Photo"`))},
		{name: "a width with a unit", text: `[caption width="300px"]` + scImg("wp-image-21") + ` text[/caption]`,
			want: jsonList(jsonImage(0, photo, "", `"alt":"Photo","caption":[`+rich("text")+`]`))},
		{name: "a caption that is only a line break", text: "[caption width=\"300\"]" + scImg("wp-image-21") + "<br />\n[/caption]", want: jsonList(jsonImage(0, photo, "", `"alt":"Photo"`))},
		{name: "a caption of lines", text: "[caption width=\"300\"]" + scImg("wp-image-21") + " one\ntwo[/caption]",
			want: jsonList(jsonImage(0, photo, "", `"alt":"Photo","caption":[`+rich("one")+`,`+jsonBreak+`,`+rich("two")+`]`))},
		{name: "the id of the attachment is the one of the attribute id", text: `[caption id="attachment_22" width="300"]<img src="` + bannerURL + `" alt="B"> Words[/caption]`,
			want:  jsonList(jsonImage(0, mediaIDs.banner, "", `"alt":"B","caption":[`+rich("Words")+`]`)),
			lines: []string{}},
		{name: "the classes of the shortcode and of the image, and the loss of the settings of the image", text: `[caption width="300" class="boxed more"]<img class="foo wp-image-21" style="margin: 0" src="` + photoURL + `" alt="P"> Words[/caption]`,
			want: jsonList(jsonImage(0, photo, `"className":"boxed"`, `"alt":"P","caption":[`+rich("Words")+`]`)),
			lines: []string{
				`skipped: the classes "more", "foo" are not imported: whiteroom blocks have one class name -> /body/0`,
				elementLost("style.margin", "<img>", "/body/0"),
			}},
		{name: "an image that is not in the site", text: `[caption width="300"]<img src="https://other.example.org/x.png"> Words[/caption]`,
			want:  jsonList(jsonLegacy(0, `[caption width="300"]<img src="https://other.example.org/x.png"> Words[/caption]`, notImage)),
			lines: []string{legacyLine(notImage, "/body/0")}},
		{name: "an image that is a PDF", text: `[caption width="300"]<img src="` + reportURL + `"> Words[/caption]`,
			want:  jsonList(jsonLegacy(0, `[caption width="300"]<img src="`+reportURL+`"> Words[/caption]`, pdfImage)),
			lines: []string{legacyLine(pdfImage, "/body/0")}},
		{name: "no image", text: `[caption width="300"]Words only[/caption]`,
			want:  jsonList(jsonLegacy(0, `[caption width="300"]Words only[/caption]`, "the caption has no image")),
			lines: []string{legacyLine("the caption has no image", "/body/0")}},
		{name: "a caption that cannot be text", text: `[caption width="300"]` + scImg("wp-image-21") + ` <img src="x.png">[/caption]`,
			want:  jsonList(jsonLegacy(1, `[caption width="300"]`+scImg("wp-image-21")+` <img src="x.png">[/caption]`, "the text has <img>, which rich text cannot have")),
			lines: []string{legacyLine("the text has <img>, which rich text cannot have", "/body/0")}},
		{name: "a caption with a shortcode in it", text: `[caption width="300"]` + scImg("wp-image-21") + ` [br][/caption]`,
			want: jsonList(jsonImage(0, photo, "", `"alt":"Photo","caption":[`+rich("[br]")+`]`))},
		{name: "a caption alone on a line of a paragraph", text: "Before\n[caption width=\"300\"]" + scImg("wp-image-21") + " Words[/caption]\nAfter",
			want: jsonList(jsonPara(0, "Before"), jsonImage(1, photo, "", `"alt":"Photo","caption":[`+rich("Words")+`]`), jsonPara(2, "After"))},
		{name: "a caption in a sentence", text: `A [caption width="300"]` + scImg("wp-image-21") + ` Words[/caption] b`,
			want:  jsonList(jsonLegacy(1, `<p>A [caption width="300"]`+scImg("wp-image-21")+` Words[/caption] b</p>`, "the text has the shortcode [caption], which whiteroom does not run")),
			lines: []string{legacyLine("the text has the shortcode [caption], which whiteroom does not run", "/body/0")}},
		{name: "two captions side by side", text: `[caption width="300"]` + scImg("wp-image-21") + ` One[/caption][caption width="300"]` + scImg("wp-image-21") + ` Two[/caption]`,
			want: jsonList(jsonLegacy(1, `<p>[caption width="300"]`+scImg("wp-image-21")+` One[/caption][caption width="300"]`+scImg("wp-image-21")+` Two[/caption]</p>`,
				"the text has the shortcode [caption], which whiteroom does not run")),
			lines: []string{legacyLine("the text has the shortcode [caption], which whiteroom does not run", "/body/0")}},
	})
}

// galleryImage is the JSON of the image of the attachment post in a gallery: the n-th ID, the caption (as JSON of its pieces, or
// ""), the size, and the link to the medium unless noLink.
func galleryImage(n int, post int64, caption, size string, noLink bool) string {
	attrs := ""
	if caption != "" {
		attrs += `"caption":[` + caption + `],`
	}
	if !noLink {
		attrs += `"link":{"media":"` + scIDs[post] + `"},`
	}
	attrs += `"size":"` + size + `"`
	return jsonImage(n, scIDs[post], "", attrs)
}

func TestClassicShortcodeGallery(t *testing.T) {
	notImported := func(post int64) string {
		return fmt.Sprintf("skipped: the image %d of the gallery is not imported: it is not an imported image -> /body", post)
	}
	gallery := func(images int, pointer string) string {
		return fmt.Sprintf("changed: the gallery is %s here; whiteroom has no gallery blocks yet (stage 2) -> %s", count(images, "image"), pointer)
	}
	noAlt := func(pointer string) string { return noAltLine + pointer }
	lostOrder := "skipped: lost orderby of the shortcode [gallery]; whiteroom has no such settings -> /body/0"
	first := jsonText("First ") + `,` + jsonText("one", `{"type":"italic"}`)
	const thumb = "thumbnail"
	// The attachments of the post that is converted are in the order of menu_order and then ID: 44 (not imported), 42, 47, 43 (no
	// alt), 41 (a caption), 48, and 45, a PDF, which a gallery does not have.
	runClassicIn(t, shortcodeImport, []classicCase{
		{name: "the attachments of the post in the order of menu_order and ID", text: `[gallery]`,
			want: jsonList(galleryImage(0, 42, "", thumb, false), galleryImage(1, 47, "", thumb, false), galleryImage(2, 43, "", thumb, false),
				galleryImage(3, 41, first, thumb, false), galleryImage(4, 48, "", thumb, false)),
			lines: []string{notImported(44), gallery(5, "/body/0"), noAlt("/body/2/attrs/alt")}},
		{name: "the other way with order=DESC", text: `[gallery order="DESC"]`,
			want: jsonList(galleryImage(0, 48, "", thumb, false), galleryImage(1, 41, first, thumb, false), galleryImage(2, 43, "", thumb, false),
				galleryImage(3, 47, "", thumb, false), galleryImage(4, 42, "", thumb, false)),
			lines: []string{notImported(44), gallery(5, "/body/0"), noAlt("/body/2/attrs/alt")}},
		{name: "order is not case sensitive and DESC is the only other way", text: `[gallery order="desc" exclude="41,43,45,47,48,44"]`,
			want:  jsonList(galleryImage(0, 42, "", thumb, false)),
			lines: []string{gallery(1, "/body/0")}},
		{name: "exclude", text: `[gallery exclude="42, 47"]`,
			want:  jsonList(galleryImage(0, 43, "", thumb, false), galleryImage(1, 41, first, thumb, false), galleryImage(2, 48, "", thumb, false)),
			lines: []string{notImported(44), gallery(3, "/body/0"), noAlt("/body/0/attrs/alt")}},
		{name: "the children of another post", text: `[gallery id="999"]`,
			want: jsonList(galleryImage(0, 46, "", thumb, false)), lines: []string{gallery(1, "/body/0")}},
		{name: "the children of a post that has none", text: `[gallery id="12"]`, want: "[]", lines: []string{"skipped: the empty gallery is removed -> /body"}},
		{name: "the ids in the order they are written", text: `[gallery ids="41,42,43"]`,
			want:  jsonList(galleryImage(0, 41, first, thumb, false), galleryImage(1, 42, "", thumb, false), galleryImage(2, 43, "", thumb, false)),
			lines: []string{gallery(3, "/body/0"), noAlt("/body/2/attrs/alt")}},
		{name: "the ids, once, with spaces and commas, the size, and a link to the file", text: `[gallery ids="43, 41 ,,42,41 -42 0 x" size="large" link="file"]`,
			want:  jsonList(galleryImage(0, 43, "", "large", false), galleryImage(1, 41, first, "large", false), galleryImage(2, 42, "", "large", false)),
			lines: []string{gallery(3, "/body/0"), noAlt("/body/0/attrs/alt")}},
		{name: "no link", text: `[gallery ids="42" link="none"]`, want: jsonList(galleryImage(0, 42, "", thumb, true)), lines: []string{gallery(1, "/body/0")}},
		{name: "a link to the page is a link to the medium, as is any other", text: `[gallery ids="42" link="post"]`, want: jsonList(galleryImage(0, 42, "", thumb, false)),
			lines: []string{gallery(1, "/body/0")}},
		{name: "the ids ordered by menu_order", text: `[gallery ids="41,43,42" orderby="menu_order"]`,
			want:  jsonList(galleryImage(0, 42, "", thumb, false), galleryImage(1, 43, "", thumb, false), galleryImage(2, 41, first, thumb, false)),
			lines: []string{gallery(3, "/body/0"), noAlt("/body/1/attrs/alt")}},
		{name: "the ids ordered by menu_order and ID the other way", text: `[gallery ids="42,47,43,41" orderby="menu_order ID" order="DESC"]`,
			want:  jsonList(galleryImage(0, 41, first, thumb, false), galleryImage(1, 43, "", thumb, false), galleryImage(2, 47, "", thumb, false), galleryImage(3, 42, "", thumb, false)),
			lines: []string{gallery(4, "/body/0"), noAlt("/body/1/attrs/alt")}},
		{name: "the ids ordered by the ids and then by menu_order", text: `[gallery ids="47,42,43" orderby="post__in menu_order"]`,
			want:  jsonList(galleryImage(0, 47, "", thumb, false), galleryImage(1, 42, "", thumb, false), galleryImage(2, 43, "", thumb, false)),
			lines: []string{gallery(3, "/body/0"), noAlt("/body/2/attrs/alt")}},
		{name: "an order that is not followed", text: `[gallery ids="43,41,42" orderby="rand"]`,
			want:  jsonList(galleryImage(0, 43, "", thumb, false), galleryImage(1, 41, first, thumb, false), galleryImage(2, 42, "", thumb, false)),
			lines: []string{gallery(3, "/body/0"), noAlt("/body/0/attrs/alt"), lostOrder}},
		{name: "an order of which one key is followed", text: `[gallery ids="43,41,42" orderby="title menu_order"]`,
			want:  jsonList(galleryImage(0, 42, "", thumb, false), galleryImage(1, 43, "", thumb, false), galleryImage(2, 41, first, thumb, false)),
			lines: []string{gallery(3, "/body/0"), noAlt("/body/1/attrs/alt"), lostOrder}},
		{name: "include is ordered by menu_order and ID", text: `[gallery include="41,43,42"]`,
			want:  jsonList(galleryImage(0, 42, "", thumb, false), galleryImage(1, 43, "", thumb, false), galleryImage(2, 41, first, thumb, false)),
			lines: []string{gallery(3, "/body/0"), noAlt("/body/1/attrs/alt")}},
		{name: "ids that are empty do not win over include", text: `[gallery ids="0" include="41"]`,
			want: jsonList(galleryImage(0, 41, first, thumb, false)), lines: []string{gallery(1, "/body/0")}},
		{name: "include wins over exclude and the post", text: `[gallery include="42" exclude="42" id="999"]`,
			want: jsonList(galleryImage(0, 42, "", thumb, false)), lines: []string{gallery(1, "/body/0")}},
		{name: "attachments that are not images or not imported", text: `[gallery ids="41,44,45,999,12,0"]`,
			want:  jsonList(galleryImage(0, 41, first, thumb, false)),
			lines: []string{notImported(44), notImported(999), notImported(12), gallery(1, "/body/0")}},
		{name: "nothing to show", text: `[gallery ids="45,44"]`, want: "[]",
			lines: []string{notImported(44), "skipped: the empty gallery is removed -> /body"}},
		{name: "settings that are lost", text: `[gallery ids="42" columns="3" itemtag="div" icontag="a" captiontag="p" orderby="ID"]`,
			want:  jsonList(galleryImage(0, 42, "", thumb, false)),
			lines: []string{gallery(1, "/body/0"), "skipped: lost columns of the shortcode [gallery]; whiteroom has no such settings -> /body/0"}},
		{name: "an empty size is the default", text: `[gallery ids="42" size=""]`, want: jsonList(galleryImage(0, 42, "", thumb, false)), lines: []string{gallery(1, "/body/0")}},
		{name: "a gallery on a line of its own among the lines of a paragraph", text: "Before\n[gallery ids=\"42\"]\nAfter",
			want:  jsonList(jsonPara(0, "Before"), galleryImage(1, 42, "", thumb, false), jsonPara(2, "After")),
			lines: []string{gallery(1, "/body/1")}},
		{name: "a gallery in a group", text: `<div>[gallery ids="42"]</div>`,
			want:  jsonList(jsonHolder("group", 0, "", "", galleryImage(1, 42, "", thumb, false))),
			lines: []string{gallery(1, "/body/0/content/0")}},
		{name: "a gallery in a sentence", text: `Look: [gallery ids="42"]`,
			want:  jsonList(jsonLegacy(1, `<p>Look: [gallery ids="42"]</p>`, "the text has the shortcode [gallery], which whiteroom does not run")),
			lines: []string{legacyLine("the text has the shortcode [gallery], which whiteroom does not run", "/body/0")}},
	})
}

const (
	ytURL    = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	vimeoURL = "https://vimeo.com/76979871"
	xURL     = "https://x.com/user/status/1234567890"
)

func TestClassicShortcodeEmbed(t *testing.T) {
	cannot := func(why string) string { return "the URL cannot be embedded: " + why }
	const notProvider = "not a URL that a provider can embed"
	runClassic(t, []classicCase{
		{name: "YouTube", text: `[embed]` + ytURL + `[/embed]`, want: jsonList(jsonEmbed(0, "youtube", ytURL))},
		{name: "Vimeo and X, as the paragraphs they are in", text: "[embed]" + vimeoURL + "[/embed]\n\n[embed]" + xURL + "[/embed]",
			want: jsonList(jsonEmbed(0, "vimeo", vimeoURL), jsonEmbed(1, "x", xURL))},
		{name: "twitter.com is X", text: `[embed]https://twitter.com/user/status/1[/embed]`, want: jsonList(jsonEmbed(0, "x", "https://twitter.com/user/status/1"))},
		{name: "the address in the attribute src", text: `[embed src="` + ytURL + `"]`, want: jsonList(jsonEmbed(0, "youtube", ytURL))},
		{name: "the content wins over the attribute src", text: `[embed src="` + vimeoURL + `"]` + ytURL + `[/embed]`, want: jsonList(jsonEmbed(0, "youtube", ytURL))},
		{name: "the attribute src if the content is empty", text: `[embed src="` + vimeoURL + `"] [/embed]`, want: jsonList(jsonEmbed(0, "vimeo", vimeoURL))},
		{name: "tags and white space around the address", text: "[embed]\n<p><a href=\"x\">\n" + ytURL + "</a><br /></p>\n[/embed]", want: jsonList(jsonEmbed(0, "youtube", ytURL))},
		{name: "character references in the address", text: `[embed]` + ytURL + `&amp;t=10&#38;x=1[/embed]`, want: jsonList(jsonEmbed(0, "youtube", ytURL+"&t=10&x=1"))},
		{name: "the size is lost", text: `[embed width="100" height="50"]` + ytURL + `[/embed]`, want: jsonList(jsonEmbed(0, "youtube", ytURL)),
			lines: []string{"skipped: lost width, height of the shortcode [embed]; whiteroom has no such settings -> /body/0"}},
		{name: "a size that is empty is nothing", text: `[embed width="" height="5"]` + ytURL + `[/embed]`, want: jsonList(jsonEmbed(0, "youtube", ytURL)),
			lines: []string{"skipped: lost height of the shortcode [embed]; whiteroom has no such settings -> /body/0"}},
		{name: "an address that no provider embeds", text: `[embed]https://example.org/x[/embed]`,
			want:  jsonList(jsonLegacy(0, `[embed]https://example.org/x[/embed]`, cannot(notProvider))),
			lines: []string{legacyLine(cannot(notProvider), "/body/0")}},
		// WordPress embeds the http address of a provider too, so it is looked for as an https address, and is one.
		{name: "an address with http is embedded as an https address", text: `[embed]http://www.youtube.com/watch?v=dQw4w9WgXcQ[/embed]`,
			want: jsonList(jsonEmbed(0, "youtube", ytURL))},
		{name: "an address with http of Vimeo", text: `[embed]http://vimeo.com/76979871[/embed]`, want: jsonList(jsonEmbed(0, "vimeo", vimeoURL))},
		{name: "an address with http in capitals, and the other providers", text: "[embed]HTTP://vimeo.com/76979871[/embed]\n\n[embed src=\"Http://x.com/user/status/1234567890\"]",
			want: jsonList(jsonEmbed(0, "vimeo", vimeoURL), jsonEmbed(1, "x", xURL))},
		{name: "an address with http that no provider embeds stays as it was written", text: `[embed]http://example.org/x[/embed]`,
			want:  jsonList(jsonLegacy(0, `[embed]http://example.org/x[/embed]`, cannot(notProvider))),
			lines: []string{legacyLine(cannot(notProvider), "/body/0")}},
		{name: "an address that is not allowed", text: `[embed]javascript:alert(1)[/embed]`,
			want:  jsonList(jsonLegacy(0, `[embed]javascript:alert(1)[/embed]`, cannot(`scheme "javascript" is not allowed`))),
			lines: []string{legacyLine(cannot(`scheme "javascript" is not allowed`), "/body/0")}},
		{name: "no address", text: `[embed][/embed]`, want: jsonList(jsonLegacy(0, `[embed][/embed]`, "the embed has no URL")),
			lines: []string{legacyLine("the embed has no URL", "/body/0")}},
		{name: "no address and no content", text: `[embed]`, want: jsonList(jsonLegacy(0, `[embed]`, "the embed has no URL")),
			lines: []string{legacyLine("the embed has no URL", "/body/0")}},
		{name: "an address that is too long", text: `[embed]https://example.org/` + strings.Repeat("a", content.MaxTextBytes) + `[/embed]`,
			want:  "[]",
			lines: []string{fmt.Sprintf("skipped: the shortcode [embed] is not imported: its HTML is too long: %d bytes (max %d) -> /body", len(`[embed]https://example.org/`)+content.MaxTextBytes+len(`[/embed]`), content.MaxTextBytes)}},
		{name: "found before the paragraphs are made: on lines of its own in a paragraph", text: "Before\n[embed]\n" + ytURL + "\n[/embed]\nAfter",
			want: jsonList(jsonPara(0, "Before"), jsonEmbed(1, "youtube", ytURL), jsonPara(2, "After"))},
		{name: "in a sentence", text: `See [embed]` + ytURL + `[/embed] now`,
			want:  jsonList(jsonLegacy(1, `<p>See [embed]`+ytURL+`[/embed] now</p>`, "the text has the shortcode [embed], which whiteroom does not run")),
			lines: []string{legacyLine("the text has the shortcode [embed], which whiteroom does not run", "/body/0")}},
		{name: "an embed that is escaped", text: `Use [[embed]] and a URL.`, want: jsonList(jsonPara(0, `Use [embed] and a URL.`))},
		// WordPress takes the opening and the closing tag together, and the outer brackets of the pair are the ones it removes.
		{name: "an embed and its end that are escaped", text: `[[embed]]` + ytURL + `[[/embed]]`, want: jsonList(jsonPara(0, `[embed]]`+ytURL+`[[/embed]`))},
		{name: "in a group", text: `<div>[embed]` + ytURL + `[/embed]</div>`, want: jsonList(jsonHolder("group", 0, "", "", jsonEmbed(1, "youtube", ytURL)))},
	})
}

func TestClassicAutoembed(t *testing.T) {
	record := func(url, pointer string) string {
		return fmt.Sprintf("review: the URL %q on its own line is text here; WordPress may have shown it as an embed, and whiteroom embeds only YouTube, Vimeo and X -> %s", url, pointer)
	}
	runClassic(t, []classicCase{
		{name: "YouTube on a line of its own", text: ytURL, want: jsonList(jsonEmbed(0, "youtube", ytURL))},
		{name: "line breaks of CR and LF", text: "Text\r\n\r\n" + ytURL + "\r\n\r\nAfter\r\n" + vimeoURL + "\r",
			want: jsonList(jsonPara(0, "Text"), jsonEmbed(1, "youtube", ytURL), jsonPara(2, "After"), jsonEmbed(3, "vimeo", vimeoURL))},
		{name: "Vimeo and X", text: vimeoURL + "\n\n" + xURL, want: jsonList(jsonEmbed(0, "vimeo", vimeoURL), jsonEmbed(1, "x", xURL))},
		{name: "in a paragraph of its own", text: "<p>" + ytURL + "</p>", want: jsonList(jsonEmbed(0, "youtube", ytURL))},
		{name: "in a paragraph of its own, with white space and attributes", text: "<p class=\"x\">\n " + ytURL + " \n</p>", want: jsonList(jsonEmbed(0, "youtube", ytURL))},
		{name: "in the lines of a paragraph, which is cut", text: "Before\n" + ytURL + "\nAfter",
			want: jsonList(jsonPara(0, "Before"), jsonEmbed(1, "youtube", ytURL), jsonPara(2, "After"))},
		{name: "the scheme in capitals", text: "HTTPS://WWW.YOUTUBE.COM/watch?v=dQw4w9WgXcQ", want: jsonList(jsonEmbed(0, "youtube", "HTTPS://WWW.YOUTUBE.COM/watch?v=dQw4w9WgXcQ"))},
		{name: "character references", text: ytURL + "&amp;t=5", want: jsonList(jsonEmbed(0, "youtube", ytURL+"&t=5"))},
		{name: "with spaces around it", text: "  \t" + ytURL + "  ", want: jsonList(jsonEmbed(0, "youtube", ytURL))},
		{name: "not on a line of its own", text: "See " + ytURL + " now\n\n" + ytURL + " and more\n\nand " + ytURL,
			want: jsonList(jsonPara(0, "See "+ytURL+" now"), jsonPara(1, ytURL+" and more"), jsonPara(2, "and "+ytURL))},
		{name: "in a link", text: `<a href="` + ytURL + `">` + ytURL + `</a>`,
			want: jsonList(jsonBlock("paragraph", 0, "", "", jsonList(jsonText(ytURL, `{"type":"link","attrs":{"href":"`+ytURL+`"}}`))))},
		{name: "a URL that no provider embeds stays text, and is said to be", text: "https://example.org/page",
			want: jsonList(jsonPara(0, "https://example.org/page")), lines: []string{record("https://example.org/page", "/body/0")}},
		// WordPress embeds the http address of a provider too, so it is looked for as an https address, and is one.
		{name: "a URL with http is embedded as an https URL", text: "http://www.youtube.com/watch?v=dQw4w9WgXcQ", want: jsonList(jsonEmbed(0, "youtube", ytURL))},
		{name: "a URL with http in capitals, in a paragraph of its own, and among lines", text: "HTTP://youtu.be/dQw4w9WgXcQ\n\n<p>Http://vimeo.com/76979871</p>\n\nBefore\nhttp://x.com/user/status/1234567890\nAfter",
			want: jsonList(jsonEmbed(0, "youtube", "https://youtu.be/dQw4w9WgXcQ"), jsonEmbed(1, "vimeo", vimeoURL), jsonPara(2, "Before"), jsonEmbed(3, "x", xURL), jsonPara(4, "After"))},
		{name: "a URL with http that no provider embeds is text as it was written", text: "http://example.org/page",
			want: jsonList(jsonPara(0, "http://example.org/page")), lines: []string{record("http://example.org/page", "/body/0")}},
		{name: "a URL with http and a port is text as it was written", text: "http://www.youtube.com:8080/watch?v=dQw4w9WgXcQ",
			want: jsonList(jsonPara(0, "http://www.youtube.com:8080/watch?v=dQw4w9WgXcQ")), lines: []string{record("http://www.youtube.com:8080/watch?v=dQw4w9WgXcQ", "/body/0")}},
		{name: "a URL that is text among lines is said to be so at the paragraph", text: "One\nhttps://example.org/a\nTwo\nhttps://example.org/b",
			want:  jsonList(jsonBlock("paragraph", 0, "", "", jsonList(jsonText("One"), jsonBreak, jsonText("https://example.org/a"), jsonBreak, jsonText("Two"), jsonBreak, jsonText("https://example.org/b")))),
			lines: []string{record("https://example.org/a", "/body/0"), record("https://example.org/b", "/body/0")}},
		{name: "a URL that is text and a URL that is embedded", text: "https://example.org/a\n" + ytURL + "\nhttps://example.org/b",
			want:  jsonList(jsonPara(0, "https://example.org/a"), jsonEmbed(1, "youtube", ytURL), jsonPara(2, "https://example.org/b")),
			lines: []string{record("https://example.org/a", "/body/0"), record("https://example.org/b", "/body/2")}},
		{name: "a URL that is text in a list item and a cell", text: "<ul><li>\n" + ytURL + "\n</li></ul>\n<table><tr><td>\n" + vimeoURL + "\n</td></tr></table>",
			want: jsonList(
				jsonBlock("list", 0, "", "", jsonList(jsonItem(jsonPara(1, ytURL)))),
				jsonBlock("table", 2, "", "", jsonList(jsonRow("", jsonCell("", vimeoURL))))),
			lines: []string{record(ytURL, "/body/0/content/0/content/0"), record(vimeoURL, "/body/1/content/0/content/0/content")}},
		{name: "a URL in a quote and in a group is a block of its own", text: "<blockquote>\n" + ytURL + "\n</blockquote><div>\n" + vimeoURL + "\n</div>",
			want: jsonList(jsonHolder("quote", 0, "", "", jsonEmbed(1, "youtube", ytURL)), jsonHolder("group", 2, "", "", jsonEmbed(3, "vimeo", vimeoURL)))},
		{name: "a URL in a code block is code", text: "<pre>\n" + ytURL + "\n</pre>", want: jsonList(jsonBlock("code", 0, "", "", `"`+ytURL+`\n"`))},
		{name: "a URL in a comment, a tag, a script and a textarea is nothing", text: "<!--\n" + ytURL + "\n-->\n<p class=\"" + ytURL + "\">x</p>\n<textarea>\n" + ytURL + "\n</textarea>",
			want: jsonList(jsonBlock("paragraph", 0, "", "", jsonList(jsonText("x"))), jsonLegacy(2, "<p><textarea>&lt;br /&gt;\n"+ytURL+"&lt;br /&gt;\n</textarea></p>", "the text has <textarea>, which rich text cannot have")),
			lines: []string{
				legacyLine("the text has <textarea>, which rich text cannot have", "/body/1"),
				`skipped: the class "` + ytURL + `" is not imported: invalid name "` + ytURL + `": use ASCII letters, digits, "-" and "_" -> /body/0`,
			}},
		{name: "a URL with a character that ends it", text: ytURL + "\"\n\n" + ytURL + "<b>x</b>", want: jsonList(
			jsonPara(0, ytURL+"\""), jsonBlock("paragraph", 1, "", "", jsonList(jsonText(ytURL), jsonText("x", jsonBold))))},
	})
}

func TestClassicShortcodeForms(t *testing.T) {
	form, old := mediaIDs.contact, scIDs[34]
	missing := func(reason string) (string, []string) {
		return reason, []string{legacyLine(reason, "/body/0")}
	}
	legacy := func(shortcode, reason string) classicCase {
		reason, lines := missing(reason)
		return classicCase{name: shortcode, text: shortcode, want: jsonList(jsonLegacy(0, shortcode, reason)), lines: lines}
	}
	cases := []classicCase{
		{name: "by the hash", text: `[contact-form-7 id="1a2b3c4d5e6f7a8b" title="Other"]`, want: jsonList(jsonFormBlock(0, form))},
		{name: "by the start of the hash, which has 7 digits", text: `[contact-form-7 id="1a2b3c4"]`, want: jsonList(jsonFormBlock(0, form))},
		{name: "by the hash, with space around it", text: `[contact-form-7 id=" 1a2b3c4 "]`, want: jsonList(jsonFormBlock(0, form))},
		{name: "by a hash of another form", text: `[contact-form-7 id="abcdef0"]`, want: jsonList(jsonFormBlock(0, old))},
		{name: "by the ID if the hash is shorter than 7", text: `[contact-form-7 id="31"]`, want: jsonList(jsonFormBlock(0, form))},
		{name: "by the title, the first of the forms that have it, if the ID is of no form", text: `[contact-form-7 id="12" title="Contact"]`, want: jsonList(jsonFormBlock(0, form))},
		{name: "by the title if the hash is in capitals, which is none", text: `[contact-form-7 id="1A2B3C4D" title="Contact"]`, want: jsonList(jsonFormBlock(0, form))},
		{name: "by the title only", text: `[contact-form-7 title=" Old form "]`, want: jsonList(jsonFormBlock(0, old))},
		{name: "in single quotes and without quotes", text: `[contact-form-7 id='1a2b3c4' title=x]`, want: jsonList(jsonFormBlock(0, form))},
		{name: "the attributes that say how the form is shown are lost, but output=form is the default", text: `[contact-form-7 id="31" html_class="wide" output="form"]`,
			want: jsonList(jsonFormBlock(0, form)), lines: []string{"skipped: lost html_class of the shortcode [contact-form-7]; whiteroom has no such settings -> /body/0"}},
		{name: "all of them", text: `[contact-form-7 id="31" output="raw" html_title="T" html_name="n" html_id="i" html_class="" other="x"]`,
			want:  jsonList(jsonFormBlock(0, form)),
			lines: []string{"skipped: lost html_id, html_name, html_title, output of the shortcode [contact-form-7]; whiteroom has no such settings -> /body/0"}},
		{name: "by the number of Contact Form 7 before 4", text: `[contact-form 5 "Title"]`, want: jsonList(jsonFormBlock(0, old))},
		{name: "by the number in quotes", text: `[contact-form "5"]`, want: jsonList(jsonFormBlock(0, old))},
		{name: "by the first attribute, whatever its name", text: `[contact-form id=5]`, want: jsonList(jsonFormBlock(0, old))},
		{name: "by the number with a zero", text: `[contact-form 05]`, want: jsonList(jsonFormBlock(0, old))},
		{name: "by the number that has space in the data", text: `[contact-form 6]`, want: jsonList(jsonLegacy(0, `[contact-form 6]`, "the form 6 is not imported")),
			lines: []string{legacyLine("the form 6 is not imported", "/body/0")}},
		{name: "a form in a paragraph of lines", text: "Before\n[contact-form-7 id=\"31\"]\nAfter", want: jsonList(jsonPara(0, "Before"), jsonFormBlock(1, form), jsonPara(2, "After"))},
		{name: "a form in a sentence", text: `Write: [contact-form-7 id="31"]`,
			want:  jsonList(jsonLegacy(1, `<p>Write: [contact-form-7 id="31"]</p>`, "the text has the shortcode [contact-form-7], which whiteroom does not run")),
			lines: []string{legacyLine("the text has the shortcode [contact-form-7], which whiteroom does not run", "/body/0")}},
		legacy(`[contact-form-7 id="1A2B3C4"]`, "the form is not imported"),
		legacy(`[contact-form-7 id="32"]`, "the form 32 is not imported"),
		legacy(`[contact-form-7 title="Quote"]`, `the form "Quote" is not imported`),
		legacy(`[contact-form-7 id="9999" title="Missing"]`, `the form "Missing" is not imported`),
		legacy(`[contact-form-7]`, "the form is not imported"),
		legacy(`[contact-form-7 id="0"]`, "the form is not imported"),
		legacy(`[contact-form 7]`, "the form 7 is not imported"),
		legacy(`[contact-form]`, "the form is not imported"),
		legacy(`[contact-form abc]`, "the form is not imported"),
	}
	runClassicIn(t, shortcodeImport, cases)
}

func TestClassicShortcodeOthers(t *testing.T) {
	legacy := func(text, reason string) classicCase {
		return classicCase{name: text, text: text, want: jsonList(jsonLegacy(0, text, reason)), lines: []string{legacyLine(reason, "/body/0")}}
	}
	runClassic(t, []classicCase{
		legacy(`[video src="a.mp4"]`, "whiteroom has no video blocks yet (stage 2)"),
		legacy(`[audio src="a.mp3"][/audio]`, "whiteroom has no audio blocks yet (stage 2)"),
		legacy(`[playlist ids="1,2" type="video"]`, "whiteroom has no playlist blocks"),
		legacy(`[box]Hello[/box]`, "the shortcode [box] is not converted"),
		legacy(`[vc_row][vc_column]x[/vc_column][/vc_row]`, "the shortcode [vc_row] is not converted"),
		legacy(`[my_button url="/x" /]`, "the shortcode [my_button] is not converted"),
		legacy(`[foo-bar x]`, "the shortcode [foo-bar] is not converted"),
		legacy(`[foo/]`, "the shortcode [foo] is not converted"),
		legacy("[foo\ta=1]", "the shortcode [foo] is not converted"),
		{name: "a shortcode with a long content is one legacy block", text: "[box]\n\nOne\n\nTwo <b>bold</b>\n\n[/box]\n\nAfter",
			want:  jsonList(jsonLegacy(0, "[box]</p>\n<p>One</p>\n<p>Two <b>bold</b></p>\n<p>[/box]", "the shortcode [box] is not converted"), jsonPara(1, "After")),
			lines: []string{legacyLine("the shortcode [box] is not converted", "/body/0")}},
		{name: "shortcodes inside a shortcode are its text", text: "[a][b]x[/b][/a]",
			want: jsonList(jsonLegacy(0, "[a][b]x[/b][/a]", "the shortcode [a] is not converted")), lines: []string{legacyLine("the shortcode [a] is not converted", "/body/0")}},
		{name: "an opening tag that closes far away takes everything", text: "[a]one\n\ntwo\n\n[/a]\n\nthree",
			want:  jsonList(jsonLegacy(0, "[a]one</p>\n<p>two</p>\n<p>[/a]", "the shortcode [a] is not converted"), jsonPara(1, "three")),
			lines: []string{legacyLine("the shortcode [a] is not converted", "/body/0")}},
		{name: "a closing tag alone is text", text: "one [/a] two", want: jsonList(jsonPara(0, "one [/a] two"))},
		{name: "an opening tag that is not closed, on a line of its own", text: "[a]\ntext", want: jsonList(jsonLegacy(0, "[a]", "the shortcode [a] is not converted"), jsonPara(1, "text")),
			lines: []string{legacyLine("the shortcode [a] is not converted", "/body/0")}},
		{name: "two shortcodes on lines of their own", text: "[a]\n[b x=1]",
			want:  jsonList(jsonLegacy(0, "[a]", "the shortcode [a] is not converted"), jsonLegacy(1, "[b x=1]", "the shortcode [b] is not converted")),
			lines: []string{legacyLine("the shortcode [a] is not converted", "/body/0"), legacyLine("the shortcode [b] is not converted", "/body/1")}},
		{name: "a shortcode on a line of its own after an image", text: classicImg("wp-image-21") + "<br>[box]",
			want:  jsonList(jsonImage(0, mediaIDs.photo, "", `"alt":"Photo"`), jsonLegacy(1, "[box]", "the shortcode [box] is not converted")),
			lines: []string{legacyLine("the shortcode [box] is not converted", "/body/1")}},
		{name: "a shortcode on a line with an image is in a sentence", text: classicImg("wp-image-21") + "[box]",
			want:  jsonList(jsonLegacy(2, `<p>`+strings.Replace(classicImg("wp-image-21"), " />", ">", 1)+`[box]</p>`, "the text has the shortcode [box], which whiteroom does not run")),
			lines: []string{legacyLine("the text has the shortcode [box], which whiteroom does not run", "/body/0")}},
	})
}

func TestClassicShortcodeIsText(t *testing.T) {
	// What does not have the shape of a shortcode is text.
	for _, text := range []string{
		"[PR] [1] [注1] [a.b] [A_b] [Foo] [=x] [ x] [] [/x] [x=1] [*] [a'b] [a\"b] [1a] [-a] [_a]",
		"a [ b ] c [[ d ]]",
		"x ] y [ z",
		"[Caption] [GALLERY] [Embed]",
	} {
		runClassic(t, []classicCase{{name: text, text: text, want: jsonList(jsonPara(0, text))}})
	}
}

func TestClassicShortcodeEscapes(t *testing.T) {
	runClassic(t, []classicCase{
		{name: "a shortcode in double brackets is the text of the shortcode", text: `Write [[gallery ids="21"]] to show a gallery.`,
			want: jsonList(jsonPara(0, `Write [gallery ids="21"] to show a gallery.`))},
		{name: "on a line of its own", text: `[[contact-form-7 id="31"]]`, want: jsonList(jsonPara(0, `[contact-form-7 id="31"]`))},
		{name: "an escaped shortcode of a name that is not known, and an opening bracket before what is none", text: `[[box]] and [[Box]x`, want: jsonList(jsonPara(0, `[box] and [[Box]x`))},
		// WordPress takes the first and the last bracket of the shortcode with its closing tag as the ones that make it an escape.
		{name: "an escaped shortcode with a closing tag", text: `[[box]]x[/box]]`, want: jsonList(jsonPara(0, `[box]]x[/box]`))},
		{name: "an opening bracket and a shortcode", text: "[[box]\nx", want: jsonList(jsonLegacy(1, "<p>[[box]<br>\nx</p>", "the text has the shortcode [box], which whiteroom does not run")),
			lines: []string{legacyLine("the text has the shortcode [box], which whiteroom does not run", "/body/0")}},
		{name: "a shortcode and a closing bracket, on lines of their own", text: "a\n[box]]\nb",
			want:  jsonList(jsonLegacy(1, "<p>a<br>\n[box]]<br>\nb</p>", "the text has the shortcode [box], which whiteroom does not run")),
			lines: []string{legacyLine("the text has the shortcode [box], which whiteroom does not run", "/body/0")}},
		{name: "escapes in a heading and a list are text", text: "<h2>[[a]]</h2>\n<ul><li>[[b]]</li></ul>",
			want: jsonList(jsonHeading(0, "", `"level":2`, "[a]"), jsonBlock("list", 1, "", "", jsonList(jsonItem(jsonPara(2, "[b]")))))},
		{name: "an escaped shortcode is not read again as text of the editor", text: `[[[box]]]`, want: jsonList(jsonPara(0, `[[box]]`))},
	})
}

func TestClassicShortcodeInElements(t *testing.T) {
	const sc = "the text has the shortcode [foo], which whiteroom does not run"
	// What is in a tag, a comment, or an element whose text is read as it is, is not run, and is not a shortcode in the text.
	runClassic(t, []classicCase{
		{name: "a shortcode in a script is gone with the script", text: "<script>var a = \"[foo]\"; var b = '[gallery ids=21]';</script>\n\nafter",
			want:  jsonList(jsonPara(1, "after")),
			lines: []string{"skipped: removed <script> with their content -> /body"}},
		{name: "in a style", text: "<style>a::after { content: \"[foo]\" }</style>\n\nafter", want: jsonList(jsonPara(0, "after")),
			lines: []string{"skipped: removed <style> with their content -> /body"}},
		{name: "in a comment", text: "<!-- [gallery ids=\"21\"] -->\n\nafter", want: jsonList(jsonPara(0, "after"))},
		{name: "in an attribute", text: `<a href="/x" title="[gallery ids=21]">x</a>`,
			want: jsonList(jsonBlock("paragraph", 0, "", "", jsonList(jsonText("x", `{"type":"link","attrs":{"href":"/x"}}`))))},
		{name: "in an attribute that has a greater than sign", text: `<a href="/x" title="1 > [foo]">x</a>`,
			want: jsonList(jsonBlock("paragraph", 0, "", "", jsonList(jsonText("x", `{"type":"link","attrs":{"href":"/x"}}`))))},
		{name: "after a tag with a greater than sign in a value, in the text", text: `<a title="1 >" href="/x">x</a> [foo]`,
			want:  jsonList(jsonLegacy(1, `<p><a title="1 &gt;" href="/x">x</a> [foo]</p>`, sc)),
			lines: []string{legacyLine(sc, "/body/0")}},
		{name: "in a textarea, the text of which is not read as elements", text: "<textarea>[foo]</textarea>",
			want:  jsonList(jsonLegacy(1, "<p><textarea>[foo]</textarea></p>", "the text has <textarea>, which rich text cannot have")),
			lines: []string{legacyLine("the text has <textarea>, which rich text cannot have", "/body/0")}},
		// WordPress cuts the HTML at the first ">" of "<<textarea>", where the parser reads a "<" and a tag: the shortcode is in the text of
		// the textarea. The shortcodes of a part like it are text, as they were written, and the report says so.
		{name: "a shortcode where the parser and WordPress cut the HTML in different places", text: "<<teXtAreA>[a] [gallery ids=\"21\"]",
			want: jsonList(jsonLegacy(1, "<p>&lt;<textarea>[a] [gallery ids=\"21\"]&lt;/p&gt;\n</textarea></p>", "the text has <textarea>, which rich text cannot have")),
			lines: []string{
				legacyLine("the text has <textarea>, which rich text cannot have", "/body/0"),
				"review: the shortcodes and URLs in a part of the HTML of the classic editor are left as text: the HTML around them is not made of elements -> /body",
			}},
		{name: "in a title, an xmp, an iframe, a noembed and a noscript", text: "<xmp>[foo]</xmp>\n\n<iframe>[foo]</iframe>\n\n<noembed>[foo]</noembed>\n\n<noscript>[foo]</noscript>\n\nafter",
			want: jsonList(
				jsonLegacy(0, "<xmp>[foo]</xmp>", "the element <xmp> is not converted"),
				jsonLegacy(1, "<iframe>[foo]</iframe>", "the iframe cannot be embedded: empty URL"),
				jsonPara(4, "after")),
			lines: []string{
				legacyLine("the element <xmp> is not converted", "/body/0"), legacyLine("the iframe cannot be embedded: empty URL", "/body/1"),
				"skipped: removed <noembed>, <noscript> with their content -> /body",
			}},
		{name: "in a script that is not closed, which takes the rest", text: "<script>[foo]\n\n[gallery ids=21]",
			lines: []string{"skipped: removed <script> with their content -> /body"}, want: "[]"},
		// In code, the brackets are the code's own unless the name is one that WordPress registers.
		{name: "in a pre, a name that is not registered is text", text: "<pre>[foo] a[i] = b[key];</pre>", want: jsonList(jsonBlock("code", 0, "", "", `"[foo] a[i] = b[key];"`))},
		{name: "in a pre, an escaped shortcode that is not registered stays as it was written", text: "<pre>[[foo]]</pre>", want: jsonList(jsonBlock("code", 0, "", "", `"[[foo]]"`))},
		{name: "in a pre, a registered name is a shortcode", text: "<pre>[gallery ids=21]</pre>",
			want:  jsonList(jsonLegacy(0, "<pre>[gallery ids=21]</pre>", "the text has the shortcode [gallery], which whiteroom does not run")),
			lines: []string{legacyLine("the text has the shortcode [gallery], which whiteroom does not run", "/body/0")}},
		{name: "in a pre, an escaped registered name is the text of the shortcode", text: "<pre>[[gallery]]</pre>", want: jsonList(jsonBlock("code", 0, "", "", `"[gallery]"`))},
		{name: "in a pre, a name that is not registered in a shortcode that is, is text", text: "<pre>[gallery]a[i][/gallery]</pre>",
			want:  jsonList(jsonLegacy(0, "<pre>[gallery]a[i][/gallery]</pre>", "the text has the shortcode [gallery], which whiteroom does not run")),
			lines: []string{legacyLine("the text has the shortcode [gallery], which whiteroom does not run", "/body/0")}},
		{name: "in a code element in a pre and in a sentence", text: "<pre><code>a[i]</code></pre>\n\nUse <code>a[i]</code> and <code>[[key]]</code>.",
			want: jsonList(jsonBlock("code", 0, "", "", `"a[i]"`),
				jsonRich(1, "", jsonText("Use "), jsonText("a[i]", `{"type":"code"}`), jsonText(" and "), jsonText("[[key]]", `{"type":"code"}`), jsonText(".")))},
		{name: "after the code, a name that is not registered is a shortcode again", text: "<p><code>a[i]</code> and [foo]</p>\n\n<pre>b[j]</pre>\n\nc [bar]",
			want: jsonList(jsonLegacy(1, "<p><code>a[i]</code> and [foo]</p>", sc), jsonBlock("code", 2, "", "", `"b[j]"`),
				jsonLegacy(4, "<p>c [bar]</p>", "the text has the shortcode [bar], which whiteroom does not run")),
			lines: []string{legacyLine(sc, "/body/0"), legacyLine("the text has the shortcode [bar], which whiteroom does not run", "/body/2")}},
		{name: "a shortcode after the pre is a shortcode", text: "<pre>a[b]</pre>[c]",
			want:  jsonList(jsonBlock("code", 0, "", "", `"a[b]"`), jsonLegacy(1, "[c]", "the shortcode [c] is not converted")),
			lines: []string{legacyLine("the shortcode [c] is not converted", "/body/1")}},
		{name: "the end tag of a code element in a pre does not end the pre", text: "<pre><code>a[i]</code>b[j]</pre>",
			want:  jsonList(jsonBlock("code", 0, "", "", `"a[i]b[j]"`)),
			lines: []string{"changed: kept only the text of <code> -> /body/0"}},
		{name: "in a heading", text: "<h2>Head [foo]</h2>", want: jsonList(jsonLegacy(1, "<h2>Head [foo]</h2>", sc)), lines: []string{legacyLine(sc, "/body/0")}},
		{name: "in a list", text: "<ul><li>one</li><li>[foo] two</li></ul>", want: jsonList(jsonLegacy(3, "<ul>\n<li>one</li>\n<li>[foo] two</li>\n</ul>", sc)),
			lines: []string{legacyLine(sc, "/body/0")}},
		{name: "in a list item that is the shortcode only", text: "<ul><li>[foo]</li></ul>", want: jsonList(jsonLegacy(2, "<ul>\n<li>[foo]</li>\n</ul>", sc)),
			lines: []string{legacyLine(sc, "/body/0")}},
		{name: "in a table", text: "<table><tr><td>a</td><td>[foo]</td></tr></table>",
			want:  jsonList(jsonLegacy(1, "<table>\n<tbody><tr>\n<td>a</td>\n<td>[foo]</td>\n</tr>\n</tbody></table>", sc)),
			lines: []string{legacyLine(sc, "/body/0")}},
		{name: "in the caption of a table", text: "<table><caption>[foo]</caption><tr><td>a</td></tr></table>",
			want:  jsonList(jsonLegacy(1, "<table>\n<caption>[foo]</caption>\n<tbody><tr>\n<td>a</td>\n</tr>\n</tbody></table>", sc)),
			lines: []string{legacyLine(sc, "/body/0")}},
		{name: "in the caption of an image", text: `<figure><img class="wp-image-21" src="` + photoURL + `" alt="P"><figcaption>[foo]</figcaption></figure>`,
			want:  jsonList(jsonLegacy(1, `<figure><img class="wp-image-21" src="`+photoURL+`" alt="P"><figcaption>[foo]</figcaption></figure>`, sc)),
			lines: []string{legacyLine(sc, "/body/0")}},
		{name: "in a paragraph of a quote, which is a legacy block in the quote", text: "<blockquote><p>One</p><p>Two [foo]</p></blockquote>",
			want:  jsonList(jsonHolder("quote", 0, "", "", jsonPara(1, "One"), jsonLegacy(3, "<p>Two [foo]</p>", sc))),
			lines: []string{legacyLine(sc, "/body/0/content/1")}},
		{name: "in the citation of a quote", text: "<blockquote><p>One</p><cite>[foo]</cite></blockquote>",
			want:  jsonList(jsonLegacy(1, "<blockquote>\n<p>One</p>\n<p><cite>[foo]</cite></p></blockquote>", sc)),
			lines: []string{legacyLine(sc, "/body/0")}},
		{name: "in the summary of details", text: "<details><summary>[foo]</summary>x</details>",
			want:  jsonList(jsonLegacy(1, "<details>\n<summary>[foo]</summary>\n<p>x</p></details>", sc)),
			lines: []string{legacyLine(sc, "/body/0")}},
		{name: "in an element that is not converted", text: "<dl><dt>[foo]</dt></dl>",
			want:  jsonList(jsonLegacy(0, "<dl>\n<dt>[foo]</dt>\n</dl>", "the element <dl> is not converted")),
			lines: []string{legacyLine("the element <dl> is not converted", "/body/0")}},
		{name: "two names in a text", text: "<h2>[foo] and [bar] and [foo]</h2>",
			want:  jsonList(jsonLegacy(1, "<h2>[foo] and [bar] and [foo]</h2>", "the text has the shortcodes [foo], [bar], which whiteroom does not run")),
			lines: []string{legacyLine("the text has the shortcodes [foo], [bar], which whiteroom does not run", "/body/0")}},
		{name: "many names in a text", text: "<h2>[a] [b] [c] [d] [e] [f] [g] [h] [i] [j] [k] [l]</h2>",
			want: jsonList(jsonLegacy(1, "<h2>[a] [b] [c] [d] [e] [f] [g] [h] [i] [j] [k] [l]</h2>",
				"the text has the shortcodes [a], [b], [c], [d], [e], [f], [g], [h], [i], [j], and 2 more, which whiteroom does not run")),
			lines: []string{legacyLine("the text has the shortcodes [a], [b], [c], [d], [e], [f], [g], [h], [i], [j], and 2 more, which whiteroom does not run", "/body/0")}},
		{name: "an element in the text wins over the shortcode as the reason", text: "<h2>[foo] <img src=\"x.png\"></h2>",
			want:  jsonList(jsonLegacy(1, "<h2>[foo] <img src=\"https://example.com/x.png\"></h2>", "the text has <img>, which rich text cannot have")),
			lines: []string{legacyLine("the text has <img>, which rich text cannot have", "/body/0")}},
	})
}

func TestClassicShortcodeWithUrls(t *testing.T) {
	runClassic(t, []classicCase{
		{name: "a URL on a line of its own in a shortcode is found before the paragraphs are made, and is its text again", text: "[box]\n" + ytURL + "\n[/box]",
			want:  jsonList(jsonLegacy(0, "[box]<br />\n"+ytURL+"<br />\n[/box]", "the shortcode [box] is not converted")),
			lines: []string{legacyLine("the shortcode [box] is not converted", "/body/0")}},
		{name: "an [embed] in a shortcode too", text: "[box]\n[embed]" + ytURL + "[/embed]\n[/box]",
			want:  jsonList(jsonLegacy(0, "[box]<br />\n[embed]"+ytURL+"[/embed]<br />\n[/box]", "the shortcode [box] is not converted")),
			lines: []string{legacyLine("the shortcode [box] is not converted", "/body/0")}},
		{name: "an [embed] and a URL, and a shortcode in a sentence", text: "x [box]\n" + ytURL + "\n[embed]" + vimeoURL + "[/embed] [/box]",
			want:  jsonList(jsonLegacy(1, "<p>x [box]<br />\n"+ytURL+"<br />\n[embed]"+vimeoURL+"[/embed] [/box]</p>", "the text has the shortcode [box], which whiteroom does not run")),
			lines: []string{legacyLine("the text has the shortcode [box], which whiteroom does not run", "/body/0")}},
		{name: "a caption with a URL on a line of its own", text: "[caption width=\"300\"]" + scImg("wp-image-21") + "\n" + ytURL + "\n[/caption]",
			// The URL was found before the shortcode was read, so it is the text of the caption; the line breaks around it are not.
			want:  jsonList(jsonImage(0, mediaIDs.photo, "", `"alt":"Photo","caption":[`+jsonText(ytURL)+`]`)),
			lines: []string{}},
		{name: "a shortcode right after a URL", text: ytURL + "\n[gallery ids=\"21\"]", want: jsonList(jsonEmbed(0, "youtube", ytURL),
			jsonImage(1, mediaIDs.photo, "", `"link":{"media":"`+mediaIDs.photo+`"},"size":"thumbnail"`)),
			lines: []string{
				"changed: the gallery is 1 image here; whiteroom has no gallery blocks yet (stage 2) -> /body/1",
			}},
	})
}

func TestClassicShortcodeNameCollision(t *testing.T) {
	// The elements that stand for the shortcodes are called so that no element of the text is.
	htmls := func(res *BodyResult) []string {
		var out []string
		for _, b := range res.Blocks {
			out = append(out, b.Attrs.HTML)
		}
		return out
	}
	t.Run("elements of the text with the name, in a sentence with a shortcode", func(t *testing.T) {
		imp := mediaImport(t)
		in := `<x-wr-piece data-n="0">t</x-wr-piece> and <X-WR-PIECE-2 data-n="0">u</X-WR-PIECE-2> and [box]x[/box] and ` + ytURL
		res := convertMedia(t, imp, bodySite(false), nil, in)
		want := `<p><x-wr-piece data-n="0">t</x-wr-piece> and <x-wr-piece-2 data-n="0">u</x-wr-piece-2> and [box]x[/box] and ` + ytURL + `</p>`
		if got := htmls(res); len(got) != 1 || got[0] != want {
			t.Errorf("the HTML of the blocks is %q, want %q", got, want)
		}
	})
	t.Run("elements of the text with the name, and the shortcodes on lines of their own", func(t *testing.T) {
		imp := mediaImport(t)
		in := `<x-wr-piece data-n="0">t</x-wr-piece>` + "\n[box]one[/box]\n" + ytURL + "\n[box]two[/box]"
		res := convertMedia(t, imp, bodySite(false), nil, in)
		if got, want := htmls(res), []string{"", "[box]one[/box]", "", "[box]two[/box]"}; fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("the HTML of the blocks is %q, want %q", got, want)
		}
		if res.Blocks[0].Type != content.BlockParagraph || res.Blocks[0].Text[0].Text != "t" || res.Blocks[2].Type != content.BlockEmbed {
			t.Errorf("the blocks are %s, want a paragraph of t, the shortcodes, and an embed", bodyDocument(t, res))
		}
	})
	t.Run("the element of a shortcode of another source is not put back", func(t *testing.T) {
		in := freeform("[box]one[/box]") + freeform(`<x-wr-piece data-n="0">t</x-wr-piece> [box]two[/box]`) + freeform("[box]three[/box]")
		imp := mediaImport(t)
		res := convertMedia(t, imp, bodySite(false), nil, in)
		want := []string{"[box]one[/box]", `<p><x-wr-piece data-n="0">t</x-wr-piece> [box]two[/box]</p>`, "[box]three[/box]"}
		if got := htmls(res); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("the HTML of the blocks is %q, want %q", got, want)
		}
	})
	t.Run("every name taken: the shortcodes stay text", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("<x-wr-piece></x-wr-piece>")
		for i := 2; i < pieceNames; i++ {
			fmt.Fprintf(&b, "<x-wr-piece-%d></x-wr-piece-%d>", i, i)
		}
		imp := mediaImport(t)
		res := convertMedia(t, imp, bodySite(false), nil, "[gallery ids=\"21\"]\n\n"+b.String())
		if len(res.Blocks) == 0 || res.Blocks[0].Type != content.BlockParagraph || res.Blocks[0].Text[0].Text != `[gallery ids="21"]` {
			t.Errorf("the blocks are %s, want the shortcode as the text of a paragraph", bodyDocument(t, res))
		}
		wantLines(t, imp.Report, bodyLines("review: the shortcodes and URLs of the HTML of the classic editor are left as text: it has elements of all the names that stand for them -> /body"))
	})
}

func TestRenderWithout(t *testing.T) {
	const in = "<b>a</b>b<i>c</i><u>d</u>e"
	for _, tt := range []struct {
		name string
		skip []int // the children to leave out
		want string
	}{
		{"none", nil, "<p>" + in + "</p>"},
		{"the first", []int{0}, "<p>b<i>c</i><u>d</u>e</p>"},
		{"the last", []int{4}, "<p><b>a</b>b<i>c</i><u>d</u></p>"},
		{"two that are apart", []int{1, 3}, "<p><b>a</b><i>c</i>e</p>"},
		{"two that are next to each other", []int{2, 3}, "<p><b>a</b>be</p>"},
		{"all", []int{0, 1, 2, 3, 4}, "<p></p>"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pn := richRoot(t, in)
			pn.Name = "p"
			var kids []*html.Node
			for ch := pn.FirstChild; ch != nil; ch = ch.NextSibling {
				kids = append(kids, ch)
			}
			var skip []*html.Node
			for _, i := range tt.skip {
				skip = append(skip, kids[i])
			}
			if got := renderWithout(pn, skip); got != tt.want {
				t.Errorf("renderWithout = %q, want %q", got, tt.want)
			}
			// The children are put back, each where it was.
			if got, want := html.Render(pn), "<p>"+in+"</p>"; got != want {
				t.Errorf("the paragraph is %q after, want %q", got, want)
			}
			for i, ch := range kids {
				if ch.Parent != pn || i > 0 && ch.PrevSibling != kids[i-1] {
					t.Errorf("the child %d is not where it was", i)
				}
			}
		})
	}
}

func TestClassicParagraphItemsWithPieces(t *testing.T) {
	c := newBodyConv(bodyImport(), BodyOptions{Site: bodySite(false), Item: bodyItem("x"), Target: bodyTarget, Key: bodyKey})
	c.pieceName = pieceBase
	c.pieces = []*classicPiece{
		{kind: pieceShortcode, name: pieceBase, sc: wpformat.Shortcode{Name: "a"}},
		{kind: pieceTextURL, name: pieceBase, url: "https://e.example/"},
		{kind: pieceEmbedURL, name: pieceBase, url: ytURL, provider: "youtube"},
	}
	piece := func(n int) string { return fmt.Sprintf(`<x-wr-piece data-n="%d"></x-wr-piece>`, n) }
	img := `<img src="a.png">`
	for _, tt := range []struct {
		name string
		in   string
		want string
	}{
		{"a shortcode", piece(0), "piece"},
		{"a shortcode with white space and a comment around it", " <!-- c -->" + piece(0) + " \n", "piece"},
		{"a URL that is embedded", piece(2), "piece"},
		{"a URL that is text is text", piece(1), "text(" + piece(1) + ")"},
		{"pieces on lines of their own", "x<br>" + piece(0) + "<br>" + piece(1) + "<br>" + piece(2) + "<br>y", "text(x) piece text(" + piece(1) + ") piece text(y)"},
		{"a shortcode and text", "a " + piece(0), "text(a " + piece(0) + ")"},
		{"two shortcodes", piece(0) + piece(2), "text(" + piece(0) + piece(2) + ")"},
		{"a shortcode and an image are not alone", img + piece(0), "image text(" + piece(0) + ")"},
		{"a shortcode and a read more tag", piece(0) + "<!--more-->", "text(" + piece(0) + "<!--more-->) more"},
		{"an element that is not a piece", `<x-wr-piece data-n="3"></x-wr-piece>`, "text(" + piece(3) + ")"},
		{"an element of another name", `<x-wr-piece-2 data-n="0"></x-wr-piece-2>`, "text(<x-wr-piece-2 data-n=\"0\"></x-wr-piece-2>)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pn := richRoot(t, tt.in)
			pn.Name = "p"
			var got []string
			for _, it := range c.paragraphItems(pn) {
				switch it.kind {
				case itemText:
					var b strings.Builder
					for n := it.first; ; n = n.NextSibling {
						b.WriteString(html.Render(n))
						if n == it.last {
							break
						}
					}
					got = append(got, "text("+b.String()+")")
				case itemImage:
					got = append(got, "image")
				case itemFrame:
					got = append(got, "frame")
				case itemPiece:
					got = append(got, "piece")
				case itemMore:
					got = append(got, "more")
				}
			}
			if s := strings.Join(got, " "); s != tt.want {
				t.Errorf("the items of %q are %q, want %q", tt.in, s, tt.want)
			}
		})
	}
}
