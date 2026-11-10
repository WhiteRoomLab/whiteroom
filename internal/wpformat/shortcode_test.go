package wpformat

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

var shortcodeTestNames = []string{"caption", "foo", "foo-bar", "gallery"}

func acceptTestNames(name string, _ byte) bool {
	return slices.Contains(shortcodeTestNames, name)
}

func acceptAll(string, byte) bool { return true }

type wantShortcode struct {
	text, name, attrs, content string
	// flags has C for Closed, S for SelfClosing, O for Open, and X for Close.
	flags string
}

func sc(text, name, attrs, content, flags string) wantShortcode {
	return wantShortcode{text, name, attrs, content, flags}
}

func flagsOf(c Shortcode) string {
	var b strings.Builder
	for _, f := range []struct {
		on   bool
		flag byte
	}{{c.Closed, 'C'}, {c.SelfClosing, 'S'}, {c.Open, 'O'}, {c.Close, 'X'}} {
		if f.on {
			b.WriteByte(f.flag)
		}
	}
	return b.String()
}

// shortcodesOf describes what FindShortcodes returned, and checks the ranges.
func shortcodesOf(t *testing.T, s string, got []Shortcode) []wantShortcode {
	t.Helper()
	var out []wantShortcode
	end := 0
	for _, c := range got {
		if c.Start < end || c.End <= c.Start || c.End > len(s) {
			t.Fatalf("the range %d:%d is wrong after %d in %q", c.Start, c.End, end, s)
		}
		end = c.End
		out = append(out, wantShortcode{s[c.Start:c.End], c.Name, c.Attrs, c.Content, flagsOf(c)})
	}
	return out
}

var findShortcodeTests = []struct {
	name, in string
	want     []wantShortcode
}{
	{"empty", "", nil},
	{"no bracket", "a gallery b", nil},
	{"one", `[gallery]`, []wantShortcode{sc(`[gallery]`, "gallery", "", "", "")}},
	{"in text", `a [gallery] b`, []wantShortcode{sc(`[gallery]`, "gallery", "", "", "")}},
	{"two", `[gallery][caption]`, []wantShortcode{sc(`[gallery]`, "gallery", "", "", ""), sc(`[caption]`, "caption", "", "", "")}},
	{"Japanese around", "日本語[gallery]日本語", []wantShortcode{sc(`[gallery]`, "gallery", "", "", "")}},
	{"not registered", `[unknown]`, nil},
	{"upper case is another name", `[Foo]`, nil},
	{"name with a letter after it", `[fooX]`, nil},
	{"name with an underscore after it", `[foo_x]`, nil},
	{"a longer name", `[foo-bar]`, []wantShortcode{sc(`[foo-bar]`, "foo-bar", "", "", "")}},
	{"a longer name that is not there", `[foo-baz]`, nil},
	{"a dot ends the name", `[foo.x]`, []wantShortcode{sc(`[foo.x]`, "foo", ".x", "", "")}},
	{"an equals sign ends the name", `[foo=1]`, []wantShortcode{sc(`[foo=1]`, "foo", "=1", "", "")}},
	{"a tab ends the name", "[foo\tx]", []wantShortcode{sc("[foo\tx]", "foo", "\tx", "", "")}},
	{"a line break ends the name", "[foo\nx]", []wantShortcode{sc("[foo\nx]", "foo", "\nx", "", "")}},
	{"a space before the name", `[ foo]`, nil},
	{"a quote ends the name", `[foo"1"]`, []wantShortcode{sc(`[foo"1"]`, "foo", `"1"`, "", "")}},
	{"no end", `[foo`, nil},
	{"no end, with attributes", `[foo a="1"`, nil},
	{"a closing tag only", `[/foo]`, nil},
	{"a closing tag first", `[/foo][foo]`, []wantShortcode{sc(`[foo]`, "foo", "", "", "")}},

	{"escaped", `[[gallery]]`, []wantShortcode{sc(`[[gallery]]`, "gallery", "", "", "OX")}},
	{"open bracket only", `[[gallery]`, []wantShortcode{sc(`[[gallery]`, "gallery", "", "", "O")}},
	{"close bracket only", `[gallery]]`, []wantShortcode{sc(`[gallery]]`, "gallery", "", "", "X")}},
	{"three brackets each way", `[[[gallery]]]`, []wantShortcode{sc(`[[gallery]]`, "gallery", "", "", "OX")}},
	{"two close brackets left", `[foo]]]`, []wantShortcode{sc(`[foo]]`, "foo", "", "", "X")}},
	{"escaped with a closing tag", `[[foo]x[/foo]]`, []wantShortcode{sc(`[[foo]x[/foo]]`, "foo", "", "x", "COX")}},
	{"close bracket after a closing tag", `[foo]a[/foo]]`, []wantShortcode{sc(`[foo]a[/foo]]`, "foo", "", "a", "CX")}},
	{"double brackets and a name that is not there", `[[bar]]`, nil},
	{"double brackets and a space", `[[ foo]`, nil},
	{"double brackets, then a shortcode", `[[ [foo]`, []wantShortcode{sc(`[foo]`, "foo", "", "", "")}},

	{"self closing", `[gallery/]`, []wantShortcode{sc(`[gallery/]`, "gallery", "", "", "S")}},
	{"self closing after a space", `[gallery /]`, []wantShortcode{sc(`[gallery /]`, "gallery", " ", "", "S")}},
	{"self closing with attributes", `[gallery a="1"/]`, []wantShortcode{sc(`[gallery a="1"/]`, "gallery", ` a="1"`, "", "S")}},
	{"self closing, and a closing tag that is not for it", `[foo/]x[/foo]`, []wantShortcode{sc(`[foo/]`, "foo", "", "", "S")}},
	{"a slash that ends the attributes", `[foo a=/]`, []wantShortcode{sc(`[foo a=/]`, "foo", " a=", "", "S")}},
	{"a slash that does not", `[foo /a]`, []wantShortcode{sc(`[foo /a]`, "foo", " /a", "", "")}},
	{"two slashes", `[foo //]`, []wantShortcode{sc(`[foo //]`, "foo", " /", "", "S")}},
	{"a slash and a space", `[foo/ ]`, []wantShortcode{sc(`[foo/ ]`, "foo", "/ ", "", "")}},
	{"self closing and a close bracket", `[foo/]]`, []wantShortcode{sc(`[foo/]]`, "foo", "", "", "SX")}},

	{"the attributes end at the first bracket", `[gallery a="x]y"]`, []wantShortcode{sc(`[gallery a="x]`, "gallery", ` a="x`, "", "")}},
	{"a closing tag in the attributes", `[foo a="[/foo]"]x[/foo]`, []wantShortcode{sc(`[foo a="[/foo]"]x[/foo]`, "foo", ` a="[/foo`, `"]x`, "C")}},
	{"an open bracket in the attributes", `[foo a="[gallery]"]`, []wantShortcode{sc(`[foo a="[gallery]`, "foo", ` a="[gallery`, "", "")}},
	{"an open bracket in the attributes, with no name", `[foo [x]`, []wantShortcode{sc(`[foo [x]`, "foo", " [x", "", "")}},

	{"content", `[gallery a=1]x[/gallery]`, []wantShortcode{sc(`[gallery a=1]x[/gallery]`, "gallery", " a=1", "x", "C")}},
	{"empty content", `[foo][/foo]`, []wantShortcode{sc(`[foo][/foo]`, "foo", "", "", "C")}},
	{"content with an element", `[caption]<img src="a"> c[/caption]`, []wantShortcode{sc(`[caption]<img src="a"> c[/caption]`, "caption", "", `<img src="a"> c`, "C")}},
	{"content with line breaks", "[caption]a\n\nb[/caption]", []wantShortcode{sc("[caption]a\n\nb[/caption]", "caption", "", "a\n\nb", "C")}},
	{"the second closing tag is left", `[gallery]x[/gallery][/gallery]`, []wantShortcode{sc(`[gallery]x[/gallery]`, "gallery", "", "x", "C")}},
	{"the first closing tag ends it", `[foo]a[foo]b[/foo]c[/foo]`, []wantShortcode{sc(`[foo]a[foo]b[/foo]`, "foo", "", "a[foo]b", "C")}},
	{"a shortcode in the content is not found", `[foo]x[gallery]y[/foo]`, []wantShortcode{sc(`[foo]x[gallery]y[/foo]`, "foo", "", "x[gallery]y", "C")}},
	{"a name that is a prefix", `[foo][foo-bar]x[/foo-bar][/foo]`, []wantShortcode{sc(`[foo][foo-bar]x[/foo-bar][/foo]`, "foo", "", "[foo-bar]x[/foo-bar]", "C")}},
	{"a closing tag of another name", `[foo-bar]x[/foo]`, []wantShortcode{sc(`[foo-bar]`, "foo-bar", "", "", "")}},
	{"a closing tag in another case", `[foo]x[/FOO]`, []wantShortcode{sc(`[foo]`, "foo", "", "", "")}},
	{"a closing tag with a space", `[foo]x[/foo ]`, []wantShortcode{sc(`[foo]`, "foo", "", "", "")}},
	{"a closing tag with a longer name", `[foo]x[/foo-bar]`, []wantShortcode{sc(`[foo]`, "foo", "", "", "")}},
	{"a closing tag with no bracket", `[foo]x[/foo`, []wantShortcode{sc(`[foo]`, "foo", "", "", "")}},
	{"no closing tag", `[foo]x`, []wantShortcode{sc(`[foo]`, "foo", "", "", "")}},
	{"the next shortcode after a closed one", `[foo]a[/foo] [gallery]`, []wantShortcode{sc(`[foo]a[/foo]`, "foo", "", "a", "C"), sc(`[gallery]`, "gallery", "", "", "")}},
	{"a closing tag far away", "[foo]" + strings.Repeat("[gallery]", 3) + "[/foo]", []wantShortcode{sc("[foo]"+strings.Repeat("[gallery]", 3)+"[/foo]", "foo", "", strings.Repeat("[gallery]", 3), "C")}},
	{"two names with no closing tag, then closing tags", `[foo][gallery]x[/gallery]y[/foo]`, []wantShortcode{sc(`[foo][gallery]x[/gallery]y[/foo]`, "foo", "", "[gallery]x[/gallery]y", "C")}},
	{"closing tags for names that come later", `[foo]x[gallery]y[/gallery][/foo]`, []wantShortcode{sc(`[foo]x[gallery]y[/gallery][/foo]`, "foo", "", "x[gallery]y[/gallery]", "C")}},
	{"an unclosed one, and a closed one after it", `[foo]a [gallery]b[/gallery] c[/caption]`, []wantShortcode{sc(`[foo]`, "foo", "", "", ""), sc(`[gallery]b[/gallery]`, "gallery", "", "b", "C")}},
	{"closing tags that were passed", `[foo] [gallery]a [caption]b[/caption] [/gallery] [caption]c[/caption]`, []wantShortcode{
		sc(`[foo]`, "foo", "", "", ""),
		sc(`[gallery]a [caption]b[/caption] [/gallery]`, "gallery", "", "a [caption]b[/caption] ", "C"),
		sc(`[caption]c[/caption]`, "caption", "", "c", "C"),
	}},

	{"in an attribute", `<a href="[gallery]">x</a>`, nil},
	{"in an attribute, then in text", `<a href="[gallery]">[caption]</a>`, []wantShortcode{sc(`[caption]`, "caption", "", "", "")}},
	{"in a comment", `<!-- [gallery] -->`, nil},
	{"in CDATA", `<![CDATA[ [gallery] ]]>`, nil},
	{"in an element that does not end", `<a href="[gallery]`, nil},
	{"after an element that does not end", `<a [gallery]`, nil},
	{"an element start in text", `a < b [gallery]`, nil},
	{"a bracket in an element before the shortcode", `<a title="]">[gallery]`, []wantShortcode{sc(`[gallery]`, "gallery", "", "", "")}},
	{"a bracket in an element, in the attributes", `[gallery a="<b title='x]'>"]`, []wantShortcode{sc(`[gallery a="<b title='x]'>"]`, "gallery", ` a="<b title='x]'>"`, "", "")}},
	{"an element in the attributes, no bracket", `[foo <b>x</b> ]`, []wantShortcode{sc(`[foo <b>x</b> ]`, "foo", ` <b>x</b> `, "", "")}},
	{"an element right after the name", `[foo<b>]`, []wantShortcode{sc(`[foo<b>]`, "foo", "<b>", "", "")}},
	{"an element that ends in a slash", `[foo <b/>]`, []wantShortcode{sc(`[foo <b/>]`, "foo", " <b/>", "", "")}},
	{"an element in the name", `[fo<b>o]`, nil},
	{"an element after the bracket", `[<b>foo]`, nil},
	{"a closing tag in an attribute", `[foo]<b title="[/foo]">x[/foo]`, []wantShortcode{sc(`[foo]<b title="[/foo]">x[/foo]`, "foo", "", `<b title="[/foo]">x`, "C")}},
	{"a closing tag in a comment", `[foo]<!-- [/foo] -->[/foo]`, []wantShortcode{sc(`[foo]<!-- [/foo] -->[/foo]`, "foo", "", "<!-- [/foo] -->", "C")}},
	{"a closing tag in CDATA", `[foo]<![CDATA[ [/foo] ]]>[/foo]`, []wantShortcode{sc(`[foo]<![CDATA[ [/foo] ]]>[/foo]`, "foo", "", "<![CDATA[ [/foo] ]]>", "C")}},
	{"a closing tag between elements", `[foo]a<b>b[/foo]c</b>`, []wantShortcode{sc(`[foo]a<b>b[/foo]`, "foo", "", "a<b>b", "C")}},
	{"a closing tag in an element only", `[foo]<b title="[/foo]">`, []wantShortcode{sc(`[foo]`, "foo", "", "", "")}},
	{"a close bracket in an element after the shortcode", `[foo]<b title="]">`, []wantShortcode{sc(`[foo]`, "foo", "", "", "")}},
	{"a shortcode over elements", `[foo]<p>x</p>[/foo]`, []wantShortcode{sc(`[foo]<p>x</p>[/foo]`, "foo", "", "<p>x</p>", "C")}},
	{"a shortcode in a paragraph", `<p>[foo]</p>`, []wantShortcode{sc(`[foo]`, "foo", "", "", "")}},
	{"an element that has the name", `<p [foo]>[foo]`, []wantShortcode{sc(`[foo]`, "foo", "", "", "")}},
}

func TestFindShortcodes(t *testing.T) {
	for _, tt := range findShortcodeTests {
		t.Run(tt.name, func(t *testing.T) {
			got := FindShortcodes(tt.in, acceptTestNames)
			if d := shortcodesOf(t, tt.in, got); !slices.Equal(d, tt.want) {
				t.Errorf("FindShortcodes(%q) = %+v, want %+v", tt.in, d, tt.want)
			}
			if want := referenceShortcodes(tt.in, acceptTestNames); !slices.Equal(got, want) {
				t.Errorf("FindShortcodes(%q) = %+v, and the plain search made %+v", tt.in, got, want)
			}
		})
	}
}

func TestFindShortcodesRanges(t *testing.T) {
	s := "ab [[foo a=1]x[/foo]] cd [gallery/]] [caption]"
	got := FindShortcodes(s, acceptTestNames)
	want := []Shortcode{
		{Start: 3, End: 21, Name: "foo", Attrs: " a=1", Content: "x", Closed: true, Open: true, Close: true},
		{Start: 25, End: 36, Name: "gallery", SelfClosing: true, Close: true},
		{Start: 37, End: 46, Name: "caption"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("FindShortcodes(%q) = %+v, want %+v", s, got, want)
	}
}

func TestFindShortcodesName(t *testing.T) {
	type call struct {
		name string
		next byte
	}
	tests := []struct {
		in   string
		want []call
	}{
		{"[foo.x][gallery]", []call{{"foo", '.'}, {"gallery", ']'}}},
		{"[foo-bar baz]", []call{{"foo-bar", ' '}}},
		{"[[foo]]", []call{{"foo", ']'}}},
		{"[foo", []call{{"foo", 0}}},
		{"[a<b>]", []call{{"a", '<'}}},
		{"[a/]", []call{{"a", '/'}}},
		{"[ a]", nil},
		{"[]", nil},
		{"[[]", nil},
		{"[[[b]", []call{{"b", ']'}}},
		{"<a href=\"[b]\">[c=1]", []call{{"c", '='}}},
	}
	for _, tt := range tests {
		var got []call
		FindShortcodes(tt.in, func(name string, next byte) bool {
			got = append(got, call{name, next})
			return false
		})
		if !slices.Equal(got, tt.want) {
			t.Errorf("FindShortcodes(%q) asked about %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestFindShortcodesAt(t *testing.T) {
	type call struct {
		at   int
		name string
	}
	tests := []struct {
		in   string
		take string // the name that isName accepts, if any
		want []call
	}{
		{"[a] [b]", "", []call{{0, "a"}, {4, "b"}}},
		{"x [[a]] [b]", "", []call{{2, "a"}, {8, "b"}}},
		{"[[[b]", "", []call{{1, "b"}}},
		{"<p title=\"[a]\">[b]", "", []call{{15, "b"}}},
		// The places inside a shortcode are not asked about.
		{"[a][b][/a][c]", "a", []call{{0, "a"}, {10, "c"}}},
		{"[a [b]] [c]", "", []call{{0, "a"}, {3, "b"}, {8, "c"}}},
	}
	for _, tt := range tests {
		var got []call
		FindShortcodesAt(tt.in, func(at int, name string, _ byte) bool {
			got = append(got, call{at, name})
			return name == tt.take
		})
		if !slices.Equal(got, tt.want) {
			t.Errorf("FindShortcodesAt(%q) asked about %v, want %v", tt.in, got, tt.want)
		}
		for _, c := range got {
			if tt.in[c.at] != '[' {
				t.Errorf("FindShortcodesAt(%q) gave the position %d, which is no bracket", tt.in, c.at)
			}
		}
	}
	// It finds what FindShortcodes finds.
	for _, tt := range findShortcodeTests {
		got := FindShortcodesAt(tt.in, func(_ int, name string, next byte) bool { return acceptTestNames(name, next) })
		if want := FindShortcodes(tt.in, acceptTestNames); !slices.Equal(got, want) {
			t.Errorf("FindShortcodesAt(%q) = %+v, want %+v", tt.in, got, want)
		}
	}
}

func TestElementEnd(t *testing.T) {
	for _, tt := range []struct {
		in   string
		at   int
		want int
	}{
		{"<b>x</b>", 0, 3},
		{"a<b>x</b>", 1, 4},
		{"<!-- x > y -->z", 0, 14},
		{"<!-->z-->", 0, 5},
		{"<![CDATA[ a > b ]]>c", 0, 19},
		{`<a title=">">`, 0, 11},
		{"<b", 0, 2},
		{"<!-- never", 0, 10},
		{"x<", 1, 2},
	} {
		if got := ElementEnd(tt.in, tt.at); got != tt.want {
			t.Errorf("ElementEnd(%q, %d) = %d, want %d", tt.in, tt.at, got, tt.want)
		}
	}
}

func TestFindShortcodesNext(t *testing.T) {
	// A rule that uses the byte after the name: names that start with x are shortcodes only before a space, a bracket, or a slash.
	isName := func(name string, next byte) bool {
		return strings.HasPrefix(name, "x") && (next == ' ' || next == ']' || next == '/')
	}
	tests := []struct {
		in   string
		want []string
	}{
		{"[xa]", []string{"[xa]"}},
		{"[xa b]", []string{"[xa b]"}},
		{"[xa/]", []string{"[xa/]"}},
		{"[xa.b]", nil},
		{`[xa"b"]`, nil},
		{"[xa<b>]", nil},
		{"[xa=1]", nil},
		{"[ya]", nil},
		{"[xa] [xb.c] [xd e]", []string{"[xa]", "[xd e]"}},
	}
	for _, tt := range tests {
		var got []string
		for _, c := range FindShortcodes(tt.in, isName) {
			got = append(got, tt.in[c.Start:c.End])
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("FindShortcodes(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFindShortcodesSeparated(t *testing.T) {
	// Many shortcodes with different names, closed in the opposite order, and others that no closing tag ends.
	var b strings.Builder
	const n = 300
	for i := range n {
		fmt.Fprintf(&b, "[n%d]", i)
	}
	for i := n - 1; i >= 0; i-- {
		fmt.Fprintf(&b, "[/n%d]", i)
	}
	s := b.String()
	got := FindShortcodes(s, acceptAll)
	if len(got) != 1 || !got[0].Closed || got[0].Name != "n0" || got[0].Start != 0 || got[0].End != len(s) {
		t.Errorf("FindShortcodes of nested names = %d shortcodes, the first %+v", len(got), got[0])
	}
	var open strings.Builder
	for i := range n {
		fmt.Fprintf(&open, "[m%d]", i)
	}
	for i := range n {
		fmt.Fprintf(&open, "[/m%d]", i)
	}
	if got := FindShortcodes(open.String(), acceptAll); len(got) != 1 || got[0].Name != "m0" || !got[0].Closed {
		t.Errorf("FindShortcodes = %d shortcodes, want 1 that is closed", len(got))
	}
	// Each name has a closing tag, but only after all of the opening tags: the closing tags are found by the names.
	var late strings.Builder
	for i := range n {
		fmt.Fprintf(&late, "[p%d]", i)
	}
	late.WriteString("[x]")
	for i := range n {
		fmt.Fprintf(&late, "[/p%d]", i)
	}
	got = FindShortcodes(late.String(), acceptAll)
	if len(got) != 1 || got[0].Name != "p0" || !got[0].Closed {
		t.Errorf("FindShortcodes = %d shortcodes, want 1 that is closed", len(got))
	}
}
