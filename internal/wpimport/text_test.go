package wpimport

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/html"
)

func TestHTMLText(t *testing.T) {
	nbsp := string(rune(0xA0))
	tests := []struct {
		name string
		in   string
		want string
		lost []string
	}{
		{"nothing", "", "", nil},
		{"text", "Notices from the company", "Notices from the company", nil},
		{"text with line breaks", "会社からのお知らせです。\n新しいサービスやイベントの情報を掲載します。", "会社からのお知らせです。\n新しいサービスやイベントの情報を掲載します。", nil},
		{"paragraphs", "<p>a</p><p>b</p>", "a\n\nb", nil},
		{"paragraphs on separate lines", "<p>a</p>\n\n<p>b</p>\n", "a\n\nb", nil},
		{"unclosed paragraphs", "<p>a<p>b", "a\n\nb", nil},
		{"br", "a<br>b", "a\nb", nil},
		{"br with a slash", "a<br />b", "a\nb", nil},
		{"two br", "a<br><br>b", "a\n\nb", nil},
		{"many br", "a<br><br><br><br>b", "a\n\nb", nil},
		{"br at the ends", "<br>a<br>", "a", nil},
		{"link", `<a href="https://example.com/">サイト</a>のお知らせ`, "サイトのお知らせ", []string{`a link to "https://example.com/"`}},
		{"link with spaces around the address", `<a href="  /about/ ">About</a>`, "About", []string{`a link to "/about/"`}},
		{"link without an address", "<a>no address</a>", "no address", []string{"<a>"}},
		{"link with an empty address", `<a href="">empty</a>`, "empty", []string{"<a>"}},
		{"anchor", `<a name="top">here</a>`, "here", []string{"<a>"}},
		{"strong, em and span", "<strong>a</strong> <em>b</em> <span>c</span>", "a b c", []string{"<strong>", "<em>", "<span>"}},
		{"the same element twice", "<b>a</b> <b>b</b> <i>c</i> <b>d</b>", "a b c d", []string{"<b>", "<i>"}},
		{"script", "<script>alert(1)</script>y", "y", []string{"<script>"}},
		{"style", "<style>p { color: red }</style>y", "y", []string{"<style>"}},
		{"content of the other dropped elements", "a<iframe src=x>fallback</iframe><object>o</object><video>v</video><audio>u</audio><canvas>c</canvas><embed src=x>b",
			"ab", []string{"<iframe>", "<object>", "<video>", "<audio>", "<canvas>", "<embed>"}},
		{"noscript and template", "<noscript>n</noscript><template>t</template>y", "y", []string{"<noscript>", "<template>"}},
		{"svg", "<svg><title>t</title><path d=x></path></svg>y", "y", []string{"<svg>"}},
		{"math", "<math><mi>x</mi></math>y", "y", []string{"<math>"}},
		{"image", "<img src=a alt=b>", "", []string{`an image "a"`}},
		{"image among text", `a<img src="/wp-content/uploads/a.png">b`, "ab", []string{`an image "/wp-content/uploads/a.png"`}},
		{"image with a long address", `<img src=" ` + strings.Repeat("a", 300) + ` ">`, "", []string{`an image "` + strings.Repeat("a", 300) + `"`}},
		{"image without an address", `<img alt="b">`, "", []string{"<img>"}},
		{"image in a link", `<a href="/x/"><img src="/a.png"></a>`, "", []string{`a link to "/x/"`, `an image "/a.png"`}},
		{"unordered list", "<ul><li>one</li><li>two</li></ul>", "one\ntwo", []string{"<ul>", "<li>"}},
		{"list after text", "List:<ul><li>one</li></ul>after", "List:\n\none\n\nafter", []string{"<ul>", "<li>"}},
		{"unclosed list items", "<ul><li>one<li>two</ul>", "one\ntwo", []string{"<ul>", "<li>"}},
		{"nested divs", "<div>a<div>b</div>c</div>", "a\nb\nc", []string{"<div>"}},
		{"headings", "<h2>Title</h2><p>text</p>", "Title\n\ntext", []string{"<h2>"}},
		{"table", "<table><tr><td>a</td><td>b</td></tr><tr><td>c</td><td>d</td></tr></table>", "a b\nc d", []string{"<table>", "<tbody>", "<tr>", "<td>"}},
		{"table with a head", "<table><thead><tr><th>h1</th><th>h2</th></tr></thead></table>", "h1 h2", []string{"<table>", "<thead>", "<tr>", "<th>"}},
		{"horizontal rule", "a<hr>b", "a\n\nb", []string{"<hr>"}},
		{"preformatted text", "<pre>  x\n  y</pre>", "x\n  y", []string{"<pre>"}},
		{"blockquote", "<blockquote>q</blockquote>after", "q\n\nafter", []string{"<blockquote>"}},
		{"comment", "x<!-- hidden -->y", "xy", nil},
		{"comment only", "<!-- hidden -->", "", nil},
		{"doctype", "<!DOCTYPE html>x", "x", nil},
		{"processing instruction", "x<?php echo 1; ?>y", "xy", nil},
		{"named references", "News &amp; Events &lt;b&gt; &quot;q&quot; &hellip;", `News & Events <b> "q" ` + string(rune(0x2026)), nil},
		{"non-breaking space", "a&nbsp;b", "a" + nbsp + "b", nil},
		{"numeric references", "&#65;&#x42;&#12354;", "AB" + string(rune(12354)), nil},
		{"a reference without a semicolon", "a &amp b", "a & b", nil},
		{"an unknown reference", "a &zz; b &", "a &zz; b &", nil},
		{"a bare ampersand", "Q&A", "Q&A", nil},
		{"a less-than sign that starts no tag", "1 < 2 and 3 > 2", "1 < 2 and 3 > 2", nil},
		{"an escaped tag is text", "&lt;strong&gt;x&lt;/strong&gt;", "<strong>x</strong>", nil},
		{"CRLF", "a\r\nb", "a\nb", nil},
		{"CR", "a\rb", "a\nb", nil},
		{"CR from a reference", "a&#13;b", "a\nb", nil},
		{"three line breaks", "a\n\n\nb", "a\n\nb", nil},
		{"many line breaks", "a\n\n\n\n\n\n\nb", "a\n\nb", nil},
		{"line breaks with spaces", "a\n \t \n \n\nb", "a\n\nb", nil},
		{"space at the ends of lines", "a  \t\nb \t \n\nc", "a\nb\n\nc", nil},
		{"space at the ends of the text", "  \t\n  a  b  \n\t ", "a  b", nil},
		{"line breaks only", "\n\n \n", "", nil},
		{"space only", " \t ", "", nil},
		{"only markup", "<p> </p><div></div>", "", []string{"<div>"}},
		{"indentation stays", "a\n  b\n    c", "a\n  b\n    c", nil},
		{"unclosed elements", "<p>unclosed <b>bold", "unclosed bold", []string{"<b>"}},
		{"closing tags with no start", "a</p>b</div>c", "a\n\nbc", nil},
		{"element with an odd name", "<my_tag>x</my_tag>y", "xy", []string{`<"my_tag">`}},
		{"element with a custom name", "<foo-bar>x</foo-bar>", "x", []string{"<foo-bar>"}},
		{"upper case names", "<STRONG>x</STRONG>", "x", []string{"<strong>"}},
		{"textarea", "<textarea>in &amp; out</textarea>", "in & out", []string{"<textarea>"}},
		{"title", "<title>t</title>x", "tx", []string{"<title>"}},
		{"a form", "<form><input type=text name=q><button>Go</button></form>", "Go", []string{"<form>", "<input>", "<button>"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, lost, err := htmlText(tt.in)
			if err != nil {
				t.Fatalf("htmlText(%q) = %v", tt.in, err)
			}
			if got != tt.want || !slices.Equal(lost, tt.lost) {
				t.Errorf("htmlText(%q) =\n%q\nlost %q\nwant\n%q\nlost %q", tt.in, got, lost, tt.want, tt.lost)
			}
		})
	}
}

// TestHTMLTextPlain checks that a text with no markup and no character reference is not read as HTML, but is tidied
// as the text of HTML is.
func TestHTMLTextPlain(t *testing.T) {
	for _, in := range []string{"", "a", "a\nb", "日本語\n\n日本語", "1 + 1 = 2", "a  b", "x\ty"} {
		got, lost, err := htmlText(in)
		if got != in || lost != nil || err != nil {
			t.Errorf("htmlText(%q) = %q, %q, %v, want it as it is", in, got, lost, err)
		}
	}
	for in, want := range map[string]string{
		" a ":        "a",
		"a \nb\t\n":  "a\nb",
		"a\r\nb\r\n": "a\nb",
		"a\n\n\n\nb": "a\n\nb",
	} {
		if got, lost, err := htmlText(in); got != want || lost != nil || err != nil {
			t.Errorf("htmlText(%q) = %q, %q, %v, want %q", in, got, lost, err, want)
		}
	}
}

func TestHTMLTextLimit(t *testing.T) {
	// The end tags of p make the parser look through all the open elements again and again.
	in := strings.Repeat("<div>", 10000) + strings.Repeat("</p>", 10000)
	got, lost, err := htmlText(in)
	if !errors.Is(err, html.ErrLimit) || got != "" || lost != nil {
		t.Errorf("htmlText(nested divs and p end tags) = %q, %q, %v, want html.ErrLimit", got, lost, err)
	}
}

func TestHTMLTextDeep(t *testing.T) {
	// The walk has no recursion, so HTML as deep as it is long is read at once. The parser looks through the open
	// elements at each block element, and stops block elements that are nested thousands deep with html.ErrLimit.
	for _, tt := range []struct {
		name        string
		open, close string
		depth       int
		lost        []string
	}{
		{"spans", "<span>", "</span>", 100000, []string{"<span>"}},
		{"custom elements", "<x-y>", "</x-y>", 100000, []string{"<x-y>"}},
		{"divs", "<div>", "</div>", 1000, []string{"<div>"}},
		{"lists", "<ul><li>", "</li></ul>", 400, []string{"<ul>", "<li>"}},
	} {
		in := strings.Repeat(tt.open, tt.depth) + "deep" + strings.Repeat(tt.close, tt.depth)
		got, lost, err := htmlText(in)
		if err != nil || got != "deep" || !slices.Equal(lost, tt.lost) {
			t.Errorf("%s: htmlText = %q, %q, %v, want deep, %q", tt.name, got, lost, err, tt.lost)
		}
	}
	in := strings.Repeat("<div>", 100000) + "deep" + strings.Repeat("</div>", 100000)
	if got, lost, err := htmlText(in); !errors.Is(err, html.ErrLimit) || got != "" || lost != nil {
		t.Errorf("htmlText(100000 divs) = %q, %q, %v, want html.ErrLimit", got, lost, err)
	}
}

// TestHTMLTextDropsMarkup checks, for the text of a long post, that nothing of the markup is left.
func TestHTMLTextDropsMarkup(t *testing.T) {
	in := `<!-- wp:paragraph --><p class="x">Lorem <strong>ipsum</strong> <a href="https://example.com/?a=1&amp;b=2">dolor</a></p><!-- /wp:paragraph -->` +
		`<p>Sit&nbsp;amet<br />consectetur</p><script>var x = "<p>";</script><ul><li>one</li><li>two <em>three</em></li></ul>`
	got, lost, err := htmlText(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(got, "<>") {
		t.Errorf("htmlText = %q, want no markup", got)
	}
	want := "Lorem ipsum dolor\n\nSit" + string(rune(0xA0)) + "amet\nconsectetur\n\none\ntwo three"
	if got != want {
		t.Errorf("htmlText = %q, want %q", got, want)
	}
	wantLost := []string{"<strong>", `a link to "https://example.com/?a=1&b=2"`, "<script>", "<ul>", "<li>", "<em>"}
	if !slices.Equal(lost, wantLost) {
		t.Errorf("lost %q, want %q", lost, wantLost)
	}
}

func TestLostMessage(t *testing.T) {
	items := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("<e%d>", i+1)
		}
		return out
	}
	const prefix = "kept only the text of the HTML; lost "
	for _, tt := range []struct {
		lost []string
		want string
	}{
		{[]string{"<b>"}, prefix + "<b>"},
		{[]string{`a link to "/x/"`, "<b>"}, prefix + `a link to "/x/", <b>`},
		{items(10), prefix + strings.Join(items(10), ", ")},
		{items(11), prefix + strings.Join(items(10), ", ") + ", and 1 more"},
		{items(25), prefix + strings.Join(items(10), ", ") + ", and 15 more"},
	} {
		if got := lostMessage(tt.lost); got != tt.want {
			t.Errorf("lostMessage(%d items) = %q, want %q", len(tt.lost), got, tt.want)
		}
	}
}

func TestDecodeRefs(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"", ""},
		{"News", "News"},
		{"News &amp; Events", "News & Events"},
		{"&lt;b&gt;bold&lt;/b&gt;", "<b>bold</b>"},
		{"&quot;q&quot; &#39;s&#39; &apos;", `"q" 's' '`},
		{"a&nbsp;b", "a" + string(rune(0xA0)) + "b"},
		{"&#12354;&#x3044;", string(rune(12354)) + string(rune(0x3044))},
		{"お知らせ &amp; イベント", "お知らせ & イベント"},
		{"<b>x</b>", "<b>x</b>"},
		{"<b>x &amp; y</b>", "<b>x & y</b>"},
		{"x</title>y &amp; z", "x</title>y & z"},
		{"&zz;", "&zz;"},
		{"&amp;amp;", "&amp;"},
		{"a &amp b", "a & b"},
		{"a & b", "a & b"},
		{"1 < 2 &amp; 3 > 2", "1 < 2 & 3 > 2"},
		{"a\nb &amp; c", "a\nb & c"},
	} {
		if got := decodeRefs(tt.in); got != tt.want {
			t.Errorf("decodeRefs(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	// A string of any size is read within the limits of the parser.
	long := strings.Repeat("a&amp;", 100000)
	if got := decodeRefs(long); got != strings.Repeat("a&", 100000) {
		t.Errorf("decodeRefs of a long string = %d bytes starting %q", len(got), got[:20])
	}
}

func TestOneLine(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want string
		set  bool
	}{
		{"", "", false},
		{"News", "News", false},
		{"News and Events", "News and Events", false},
		{"News\nEvents", "News Events", true},
		{"News\r\nEvents", "News  Events", true},
		{"News\rEvents", "News Events", true},
		{"News\tEvents", "News Events", true},
		{"News\vEvents", "News Events", true},
		{"News\fEvents", "News Events", true},
		{"News" + string(rune(0x85)) + "Events", "News Events", true},
		{"News" + string(rune(0x2028)) + "Events", "News Events", true},
		{"News" + string(rune(0x2029)) + "Events", "News Events", true},
		{"  News  ", "News", false},
		{"\nNews\n", "News", true},
		{"\t\n \r", "", true},
		{" ", "", false},
		{"お知らせ\nイベント", "お知らせ イベント", true},
		{"News" + string(rune(0xA0)) + "Events", "News" + string(rune(0xA0)) + "Events", false},
		{string(rune(0x3000)) + "News", string(rune(0x3000)) + "News", false},
	} {
		got, set := oneLine(tt.in)
		if got != tt.want || set != tt.set {
			t.Errorf("oneLine(%q) = %q, %v, want %q, %v", tt.in, got, set, tt.want, tt.set)
		}
	}
}

func TestTagText(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"div", "<div>"},
		{"h2", "<h2>"},
		{"foo-bar", "<foo-bar>"},
		{"foreignObject", "<foreignObject>"},
		{"my_tag", `<"my_tag">`},
		{"a:b", `<"a:b">`},
		{"a\nb", `<"a\nb">`},
		{"タグ", `<"タグ">`},
		{"", "<>"},
	} {
		if got := tagText(tt.in); got != tt.want {
			t.Errorf("tagText(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

// FuzzHTMLText checks that htmlText does not panic, and that the text it returns is tidy: valid UTF-8 if the input is,
// with no CR, no markup that comes from an element, no space at the ends of lines, and no two empty lines in a row.
func FuzzHTMLText(f *testing.F) {
	for _, s := range []string{
		"", "plain\ntext", "<p>a</p><p>b</p>", "a<br>b", `<a href="https://example.com/">サイト</a>`, "<script>x</script>y",
		"<img src=a>", "<ul><li>one<li>two</ul>", "<table><tr><td>a<td>b</table>", "x<!-- c -->y", "&amp;&lt;&#65;&zz;",
		"<svg><path/></svg>", "<b><i></b></i>", "<div><p>a</div>b</p>", "a\r\nb\rc", "<textarea>&amp;</textarea>",
		"<noscript><p>x</noscript>", "<template><p>x</template>", "<math><mi>x</mi></math>", "<form><form>", "<hr><hr>",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, lost, err := htmlText(s)
		if err != nil {
			if !errors.Is(err, html.ErrLimit) || got != "" || lost != nil {
				t.Fatalf("htmlText(%q) = %q, %q, %v", s, got, lost, err)
			}
			return
		}
		if utf8.ValidString(s) && !utf8.ValidString(got) {
			t.Fatalf("htmlText(%q) = %q, not valid UTF-8", s, got)
		}
		if strings.Contains(got, "\r") || strings.Contains(got, "\n\n\n") || strings.Trim(got, " \t\n") != got {
			t.Fatalf("htmlText(%q) = %q, not tidy", s, got)
		}
		for _, line := range strings.Split(got, "\n") {
			if strings.TrimRight(line, " \t") != line {
				t.Fatalf("htmlText(%q) = %q, has space at the end of a line", s, got)
			}
		}
		seen := make(map[string]bool)
		for _, l := range lost {
			if l == "" || seen[l] || strings.ContainsAny(l, "\n\r") {
				t.Fatalf("htmlText(%q) lost %q, want each once and on one line", s, lost)
			}
			seen[l] = true
		}
	})
}
