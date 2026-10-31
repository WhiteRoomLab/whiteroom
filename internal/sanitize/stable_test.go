package sanitize

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/html"
)

const unwrapped = "removed <button> element, keeping its content"

// stableTests have HTML that cleaning leaves in a form that the parser builds into another tree, which Sanitize parses
// and cleans again, and ids that the parser copies.
var stableTests = []sanitizeTest{
	{
		name:    "div in p",
		in:      `<p><button><div>x</div></button></p>`,
		want:    `<p></p><div>x</div><p></p>`,
		changes: unwrapped,
	},
	{
		name:    "div in p and formatting elements",
		in:      `<p><b><button><div>c</div></button>d</b>e</p>`,
		want:    `<p><b></b></p><div><b>c</b></div><b>d</b>e<p></p>`,
		changes: unwrapped,
	},
	{
		name:    "table in p",
		in:      `<p><button><table><tr><td>x</td></tr></table></button></p>`,
		want:    `<p></p><table><tbody><tr><td>x</td></tr></tbody></table><p></p>`,
		changes: unwrapped,
	},
	{
		name:    "heading in heading",
		in:      `<h2><button><h3>x</h3></button></h2>`,
		want:    `<h2></h2><h3>x</h3>`,
		changes: unwrapped,
	},
	{
		name:    "list item in list item",
		in:      `<ul><li><button><li>x</li></button></li></ul>`,
		want:    `<ul><li></li><li>x</li></ul>`,
		changes: unwrapped,
	},
	{
		// The parser puts the second a element in the first, before the table (13.3).
		name: "a in a through a table",
		in:   `<a href="/a" id="a"><table><a href="/b" id="b">x</a></table></a>`,
		want: `<a href="/a" id="a"></a><a href="/b" id="b">x</a><table></table>`,
	},
	{
		name:    "copy of a formatting element with an id",
		in:      `<p><b id=x>a</p>b`,
		want:    `<p><b id="x">a</b></p><b>b</b>`,
		changes: `removed id attribute of <b>: duplicate id "x"`,
	},
	{
		name: "copies of formatting elements with ids",
		in:   `<p><b id=x><i id=y>a</p>b<p>c`,
		want: `<p><b id="x"><i id="y">a</i></b></p><b><i>b<p>c</p></i></b>`,
		changes: `removed id attribute of <b>: duplicate id "x"` + "\n" +
			`removed id attribute of <i>: duplicate id "y"`,
	},
	{
		name: "duplicate ids",
		in:   `<h2 id="intro">a</h2><p id="intro">b</p><h1 id="intro">c</h1><p id="Intro">d</p>`,
		want: `<h2 id="intro">a</h2><p>b</p><h2>c</h2><p id="Intro">d</p>`,
		changes: `removed id attribute of <p>: duplicate id "intro"` + "\n" +
			"changed <h1> to <h2>\n" +
			`removed id attribute of <h2>: duplicate id "intro"`,
	},
	{
		name:    "line feeds in pre",
		in:      "<pre>\n\nx</pre><pre>\ny</pre><pre><b>\nz</b></pre><pre><font>\n\nw</font></pre>",
		want:    "<pre>\n\nx</pre><pre>y</pre><pre><b>\nz</b></pre><pre>\n\n\nw</pre>",
		changes: "removed <font> element, keeping its content",
	},
	{
		// Render writes carriage returns as they are, which the parser reads as line feeds.
		name: "carriage returns",
		in:   `<p>a&#13;b</p><p title="c&#13;&#10;d">e</p>`,
		want: "<p>a\nb</p><p title=\"c\nd\">e</p>",
	},
}

func TestSanitizeStable(t *testing.T) {
	for _, kind := range []Kind{Legacy, HTML} {
		runTests(t, kind, stableTests)
	}
}

// TestSanitizeID checks that Sanitize calls Options.ID once for each id that it keeps otherwise, in document order,
// and removes the ids for which it returns false.
func TestSanitizeID(t *testing.T) {
	tests := []struct {
		name, in, want string
		rejected       []string
		calls          []string
		changes        string
	}{
		{
			name: "ids",
			in: `<h2 id="a">1</h2><p id="b c">2</p><p><b id="d">3</p>4</b><h1 id="e">5</h1><h2 id="a">6</h2>` +
				`<p id="f">7</p>`,
			want:     `<h2 id="a">1</h2><p>2</p><p><b id="d">3</b></p><b>4</b><h2>5</h2><h2>6</h2><p id="f">7</p>`,
			rejected: []string{"e"},
			calls:    []string{"a", "d", "e", "f"},
			changes: `removed id attribute of <p>: invalid id "b c": use ASCII letters, digits, "-" and "_"` + "\n" +
				`removed id attribute of <b>: duplicate id "d"` + "\n" +
				"changed <h1> to <h2>\n" +
				`removed id attribute of <h2>: duplicate id "a"` + "\n" +
				`removed id attribute of <h2>: anchor "e" is already used`,
		},
		{
			// Without its id, the first b element is the same as the others for the Noah's Ark clause.
			name:     "id of a formatting element",
			in:       `<b id="x"><b><b><b>y</b></b></b>z</b>`,
			want:     `<b><b><b><b>y</b></b></b>z</b>`,
			rejected: []string{"x"},
			calls:    []string{"x"},
			changes:  `removed id attribute of <b>: anchor "x" is already used`,
		},
		{
			name:     "all rejected",
			in:       `<h2 id="a">1</h2><h3 id="b">2</h3>`,
			want:     `<h2>1</h2><h3>2</h3>`,
			rejected: []string{"a", "b"},
			calls:    []string{"a", "b"},
			changes: `removed id attribute of <h2>: anchor "a" is already used` + "\n" +
				`removed id attribute of <h3>: anchor "b" is already used`,
		},
		{name: "no ids", in: `<p>1</p>`, want: `<p>1</p>`},
	}
	for _, tt := range tests {
		var calls []string
		id := func(id string) bool {
			calls = append(calls, id)
			return !slices.Contains(tt.rejected, id)
		}
		got, changes, err := Sanitize(tt.in, Options{Kind: Legacy, ID: id})
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if got != tt.want || !slices.Equal(calls, tt.calls) || formatChanges(changes) != tt.changes {
			t.Errorf("%s: Sanitize(%q) = %q with the calls %q and the changes\n%s\nwant %q with %q and\n%s", tt.name,
				tt.in, got, calls, formatChanges(changes), tt.want, tt.calls, tt.changes)
		}
		checkPolicy(t, tt.name, Legacy, got)
	}
}

// TestSanitizeURL checks that Sanitize replaces each URL that it keeps, and only those, with what Options.URL returns
// for it, once for each element, in document order.
func TestSanitizeURL(t *testing.T) {
	const in = `<p><a href="/about/" title="/x" hreflang="en">a</a> <a href="#top">b</a> ` +
		`<a href="javascript:alert(1)">c</a></p><blockquote cite="https://example.com/q"><p>d</p></blockquote>` +
		`<del cite="/d">e</del><ins cite="mailto:a@example.com">f</ins><img src="/media/a.jpg" alt="/alt">` +
		`<p><a href="/copy"><b>g</p>h</b></a><video src="/v.mp4" poster="https://example.com/p.jpg">` +
		`<source src="/v.webm"><track src="/v.vtt"></video><audio src="/a.mp3"></audio>` +
		`<iframe src="https://www.youtube.com/embed/x"></iframe>`
	const want = `<p><a href="https://u.example/1" title="/x" hreflang="en">a</a> <a href="https://u.example/2">b</a> ` +
		`<a>c</a></p><blockquote cite="https://u.example/3"><p>d</p></blockquote>` +
		`<del cite="https://u.example/4">e</del><ins cite="https://u.example/5">f</ins>` +
		`<img src="https://u.example/6" alt="/alt"><p><a href="https://u.example/7"><b>g</b></a></p>` +
		`<a href="https://u.example/8"><b>h</b></a><video src="https://u.example/9" poster="https://u.example/10">` +
		`<source src="https://u.example/11"><track src="https://u.example/12"></video>` +
		`<audio src="https://u.example/13"></audio><iframe src="https://u.example/14"></iframe>`
	wantCalls := []string{
		"/about/", "#top", "https://example.com/q", "/d", "mailto:a@example.com", "/media/a.jpg", "/copy", "/copy",
		"/v.mp4", "https://example.com/p.jpg", "/v.webm", "/v.vtt", "/a.mp3", "https://www.youtube.com/embed/x",
	}
	var calls []string
	url := func(u string) string {
		calls = append(calls, u)
		return "https://u.example/" + strconv.Itoa(len(calls))
	}
	got, changes, err := Sanitize(in, Options{Kind: HTML, URL: url})
	if err != nil {
		t.Fatal(err)
	}
	wantChanges := `removed href attribute of <a>: invalid URL "javascript:alert(1)": scheme "javascript" is not allowed`
	if got != want || !slices.Equal(calls, wantCalls) || formatChanges(changes) != wantChanges {
		t.Errorf("Sanitize(%q) = %q with the calls %q and the changes\n%s\nwant %q with %q and\n%s", in, got, calls,
			formatChanges(changes), want, wantCalls, wantChanges)
	}
	checkPolicy(t, "URL", HTML, got)
}

// TestRewriteURLs checks that rewriteURLs replaces the URL of each element once, also when elements share an Attrs
// array, as the copies of formatting elements that the parser makes do.
func TestRewriteURLs(t *testing.T) {
	attrs := []html.Attr{{Name: "href", Value: "/x"}, {Name: "title", Value: "/t"}}
	root := &html.Node{Type: html.DocumentFragmentNode}
	for range 2 {
		a := &html.Node{Type: html.ElementNode, Name: "a", Attrs: attrs}
		a.AppendChild(&html.Node{Type: html.TextNode, Data: "x"})
		root.AppendChild(a)
	}
	n := 0
	rewriteURLs(root, func(u string) string {
		n++
		return fmt.Sprintf("%s/%d", u, n)
	})
	want := `<a href="/x/1" title="/t">x</a><a href="/x/2" title="/t">x</a>`
	if got := html.Render(root); got != want || attrs[0].Value != "/x" {
		t.Errorf("got %q and the shared href %q, want %q and /x", got, attrs[0].Value, want)
	}
}

// TestReserve checks that reserve removes the id of each element that the ID function rejects, also when elements
// share an Attrs array.
func TestReserve(t *testing.T) {
	attrs := []html.Attr{{Name: "id", Value: "x"}, {Name: "class", Value: "c"}}
	root := &html.Node{Type: html.DocumentFragmentNode}
	for range 2 {
		b := &html.Node{Type: html.ElementNode, Name: "b", Attrs: attrs}
		b.AppendChild(&html.Node{Type: html.TextNode, Data: "x"})
		root.AppendChild(b)
	}
	var c cleaner
	removed := c.reserve(root, func(string) bool { return false })
	want := `<b class="c">x</b><b class="c">x</b>`
	if got := html.Render(root); !removed || got != want || attrs[0].Value != "x" {
		t.Errorf("reserve: %t, %q and the shared id %q, want true, %q and x", removed, got, attrs[0].Value, want)
	}
	if got, want := formatChanges(c.changes.result()), `removed id attribute of <b>: anchor "x" is already used (2 times)`; got != want {
		t.Errorf("changes %q, want %q", got, want)
	}
}

// TestSanitizeErrors checks the errors of Sanitize: ErrLimit for the inputs that pass the limits of the parser, in
// the first parse or a later one, and ErrUnstable for HTML that does not settle within maxPasses.
func TestSanitizeErrors(t *testing.T) {
	numbered := func(format string, n int) string {
		var b strings.Builder
		for i := range n {
			fmt.Fprintf(&b, format, i)
		}
		return b.String()
	}
	// Inputs whose first parse is within the limits, but not a later one. The parser copies the 100 b elements of
	// copies, which differ, into each of the 50 div elements that it moves out of the p element when it parses the
	// output: 10000 copies. The Noah's Ark clause compares each of the last 5000 b elements of expensive with the 255
	// around them, attribute by attribute, in more steps than the limits for its length allow: a long comment raises
	// the limits of the first parse, but not those of the output, which sanitizing it again would have.
	copies := "<p>" + numbered("<b class=b%d>", 100) + strings.Repeat("<button><div>x</div></button>", 50)
	expensive := numbered(`<b dir="" lang="" title="" class="c" id="a%d">`, 255) +
		numbered(`<b id="b%d" class="c" title="" lang="" dir=""></b>`, 5000) + strings.Repeat("</b>", 255)
	later := []string{copies, "<!--" + strings.Repeat("x", 2*len(expensive)) + "-->" + expensive}
	for _, in := range later {
		opts, _ := limits(len(in))
		if _, err := html.ParseFragment(in, &html.Node{Type: html.ElementNode, Name: "div"}, opts); err != nil {
			t.Fatalf("the first parse of %.80q...: %v", in, err)
		}
	}
	long := strings.Repeat("a", 63)
	for _, in := range append(later,
		expensive,
		// Inputs of internal/html/limits_test.go that pass the limits.
		strings.Repeat("<div>", 10000)+strings.Repeat("</p>", 10000),
		"<p>"+numbered("<font a=%d>", 1000)+strings.Repeat("</p><p>x", 10000),
		strings.Repeat("<table><tr><td>", 5000)+strings.Repeat("<form>", 5000),
		"<svg>"+strings.Repeat("<g>", 10000)+strings.Repeat("</x>", 10000),
		"<svg>"+strings.Repeat("<"+long+"1>", 1500)+strings.Repeat("</"+long+"2>", 1500),
		"<b>"+strings.Repeat("<span>", 10000)+"<div>"+strings.Repeat("<div>", 10000)+"</b>",
		"<html"+numbered(" a%d", 10000)+"><html"+numbered(" b%d", 10000)+">",
		numbered("<font a=%d>", 4000),
		`<p><b title='`+strings.Repeat(`"`, 4000)+`'>`+strings.Repeat("</p><p>x", 1000),
	) {
		got, changes, err := Sanitize(in, Options{Kind: HTML})
		if !errors.Is(err, ErrLimit) || got != "" || changes != nil {
			t.Errorf("Sanitize(%.80q...): %q, %d changes and the error %v, want ErrLimit", in, got, len(changes), err)
		}
	}

	defer func(n int) { maxPasses = n }(maxPasses)
	maxPasses = 1
	got, changes, err := Sanitize(`<a><table><a>`, Options{Kind: HTML})
	if !errors.Is(err, ErrUnstable) || errors.Is(err, ErrLimit) || got != "" || changes != nil {
		t.Errorf("one pass: %q, %d changes and the error %v, want ErrUnstable", got, len(changes), err)
	}
	maxPasses = 2
	got, _, err = Sanitize(`<a><table><a>`, Options{Kind: HTML})
	if want := `<a></a><a></a><table></table>`; got != want || err != nil {
		t.Errorf("two passes: %q and the error %v, want %q", got, err, want)
	}
}

// post returns a blog post of at least size bytes, as WordPress writes posts, with the sloppy markup of older posts.
func post(size int) string {
	var b strings.Builder
	for i := 0; b.Len() < size; i++ {
		fmt.Fprintf(&b, `<!-- wp:heading -->
<h2 class="wp-block-heading" id="section-%d">Section %d: notes &amp; more</h2>
<!-- /wp:heading -->
<p>Lorem ipsum <strong>dolor</strong> sit amet, <a href="https://example.com/posts/%d/?ref=blog&amp;utm_source=feed">consectetur adipiscing</a> elit; <em>sed do</em> <code>go test ./...</code> tempor&nbsp;incididunt &lt;et&gt; dolore. 日本語の文も入ります。<br>
Ut enim ad <mark class="has-inline-color">minim</mark> veniam, <a href="/about/#team" target="_blank" rel="noopener">nostrud</a>&hellip;</p>
<ul class="wp-block-list"><li>First <b>bold</b></li><li>Second<ul><li>Nested <i>item</i></li></ul></li><li>Third &mdash; <a href="https://example.org/a?b=c&amp;d=e">link</a></li></ul>
<figure class="wp-block-table"><table><thead><tr><th>Name</th><th>Value</th></tr></thead><tbody><tr><td>alpha</td><td>%d</td></tr><tr><td>beta</td><td><code>x &lt; y</code></td></tr></tbody></table><figcaption>Table %d</figcaption></figure>
<figure class="wp-block-image size-large"><img decoding="async" width="1024" height="768" src="https://example.com/wp-content/uploads/2026/10/photo-%d.jpg" alt="A photo" class="wp-image-%d" srcset="https://example.com/a-300.jpg 300w" sizes="100vw"><figcaption>Caption with <a href="https://example.com/">a link</a></figcaption></figure>
<blockquote class="wp-block-quote"><p>A quote with <abbr title="HyperText Markup Language">HTML</abbr>.</p><cite>Someone</cite></blockquote>
<pre class="wp-block-code"><code>fmt.Println("&lt;hello&gt;", %d)
</code></pre>
<div class="embed"><iframe src="https://www.youtube.com/embed/abc%d" width="560" height="315" allowfullscreen></iframe></div>
<p align="center"><font color="#333333" face="Verdana">An older paragraph<br />
with <b>bold <i>and italic</b> text</i> and an unclosed paragraph
<p>Another one, <span style="color: red">red</span>, &copy; 2026
<ul><li>one<li>two<li>three</ul>
<p><b>Bold that runs</p><p>into the next paragraph</b></p>
<script>var x = %d;</script>

`, i, i, i, i, i, i, i, i, i, i)
	}
	return b.String()
}

// TestSanitizeRealistic sanitizes a long blog post: it must stay within the limits, in a time linear in its length,
// and give stable HTML.
func TestSanitizeRealistic(t *testing.T) {
	s := post(256 << 10)
	for _, kind := range []Kind{Legacy, HTML} {
		start := time.Now()
		out, changes, err := Sanitize(s, Options{Kind: kind})
		t.Logf("kind %d: %d bytes into %d bytes with %d messages in %v", kind, len(s), len(out), len(changes),
			time.Since(start))
		if err != nil {
			t.Fatalf("kind %d: %v", kind, err)
		}
		checkPolicy(t, "a blog post", kind, out)
		if again, changes, err := Sanitize(out, Options{Kind: kind}); again != out || changes != nil || err != nil {
			t.Errorf("kind %d: sanitizing the output again gives %d bytes, %d changes and the error %v", kind,
				len(again), len(changes), err)
		}
	}
}
