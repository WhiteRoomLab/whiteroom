package sanitize

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/html"
)

type sanitizeTest struct {
	name    string
	in      string
	want    string
	changes string // one line each, with " (N times)" as render writes them
}

// absolute is the reason of content.CheckHref for relative URLs.
const absolute = `must be an absolute URL or a path starting with "/"`

// formatChanges returns changes one per line, with " (N times)" for a Count above 1.
func formatChanges(changes []Change) string {
	var b strings.Builder
	for i, c := range changes {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(c.Msg)
		if c.Count > 1 {
			fmt.Fprintf(&b, " (%d times)", c.Count)
		}
	}
	return b.String()
}

// runTests sanitizes the input of each test as kind, and checks the output, the changes and checkPolicy.
func runTests(t *testing.T, kind Kind, tests []sanitizeTest) {
	t.Helper()
	for _, tt := range tests {
		got, changes, err := Sanitize(tt.in, Options{Kind: kind})
		if err != nil {
			t.Errorf("%s (kind %d): %v", tt.name, kind, err)
			continue
		}
		if got != tt.want {
			t.Errorf("%s (kind %d): Sanitize(%q)\n got %q\nwant %q", tt.name, kind, tt.in, got, tt.want)
		}
		if got := formatChanges(changes); got != tt.changes {
			t.Errorf("%s (kind %d): changes\n got %q\nwant %q", tt.name, kind, got, tt.changes)
		}
		checkPolicy(t, tt.name, kind, got)
	}
}

// checkPolicy parses out as Sanitize parses its input and checks that it has only what kind keeps: text and the
// HTML elements and attributes of the policy, with valid URLs, classes, targets, rels and ids, no content in iframe
// elements, and no element nested more than maxDepth deep.
func checkPolicy(t *testing.T, name string, kind Kind, out string) {
	t.Helper()
	opts, _ := limits(len(out))
	nodes, err := html.ParseFragment(out, &html.Node{Type: html.ElementNode, Name: "div"}, opts)
	if err != nil {
		t.Errorf("%s (kind %d): parsing the output: %v", name, kind, err)
		return
	}
	type item struct {
		n     *html.Node
		depth int
	}
	var stack []item
	for _, n := range slices.Backward(nodes) {
		stack = append(stack, item{n, 1})
	}
	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n := it.n
		for c := n.LastChild; c != nil; c = c.PrevSibling {
			stack = append(stack, item{c, it.depth + 1})
		}
		if n.Type == html.TextNode {
			continue
		}
		if n.Type != html.ElementNode || n.Namespace != html.HTML ||
			!elements[n.Name] && (kind != HTML || !htmlElements[n.Name]) {
			t.Errorf("%s (kind %d): the output %q has %+v", name, kind, out, *n)
			continue
		}
		if it.depth > maxDepth {
			t.Errorf("%s (kind %d): the output has an element nested %d deep", name, kind, it.depth)
		}
		if n.Name == "iframe" && n.FirstChild != nil {
			t.Errorf("%s (kind %d): the output %q has an iframe element with content", name, kind, out)
		}
		for _, a := range n.Attrs {
			if problem := attrProblem(n.Name, a); problem != "" {
				t.Errorf("%s (kind %d): the output %q has %s attribute %+v of <%s>", name, kind, out, problem, a, n.Name)
			}
		}
	}
}

// attrProblem returns what is wrong with a, an attribute of an element el in an output, or "".
func attrProblem(el string, a html.Attr) string {
	switch {
	case a.Namespace != "" || !globalAttrs[a.Name] && !slices.Contains(elementAttrs[el], a.Name):
		return "a disallowed"
	case urlAttrs[[2]string{el, a.Name}] != noURL:
		if content.NormalizeHref(a.Value) != a.Value || checkURL(a.Value, urlAttrs[[2]string{el, a.Name}]) != "" {
			return "an invalid URL in the"
		}
	case a.Name == "class":
		for _, c := range strings.Fields(a.Value) {
			if content.ReservedClass(c) {
				return "a reserved class in the"
			}
		}
	case a.Name == "id":
		if content.CheckName(a.Value) != nil {
			return "an invalid"
		}
	case a.Name == "target":
		if a.Value != "_blank" {
			return "an invalid"
		}
	case a.Name == "rel":
		for _, v := range strings.Fields(a.Value) {
			if !slices.Contains(relValues, v) {
				return "an invalid value in the"
			}
		}
	}
	return ""
}

// allowedLegacy has every element and attribute that both kinds keep, as Render writes them.
const allowedLegacy = `<article dir="ltr" lang="en" title="t" id="top" class="c1 c2">` +
	`<header><hgroup><h2>H2</h2><p>sub</p></hgroup></header>` +
	`<nav><menu><li value="3">item</li></menu></nav>` +
	`<section><h3>H3</h3><h4>H4</h4><h5>H5</h5><h6>H6</h6><p>` +
	`<a href="https://example.com/?a=1&amp;b=2" hreflang="en" type="text/html" target="_blank" ` +
	`rel="noopener nofollow sponsored ugc noreferrer">a</a> ` +
	`<abbr title="abbreviation">abbr</abbr> <b>b</b> <bdi>bdi</bdi> <bdo dir="rtl">bdo</bdo><br>` +
	`<cite>cite</cite> <code>code</code> <data value="1">data</data> ` +
	`<del cite="/del" datetime="2026-11-01">del</del> <dfn>dfn</dfn> <em>em</em> <i>i</i> ` +
	`<ins cite="https://example.com/ins" datetime="2026-11-01">ins</ins> <kbd>kbd</kbd> <mark>mark</mark> <q>q</q> ` +
	`<ruby>漢<rp>(</rp><rt>かん</rt><rp>)</rp></ruby> <s>s</s> <samp>samp</samp> <small>small</small> ` +
	`<span>span</span> <strong>strong</strong> <sub>sub</sub> <sup>sup</sup> ` +
	`<time datetime="2026-11-01">time</time> <u>u</u> <var>var</var><wbr>` +
	`<img src="/media/a.jpg" alt="alt" width="10" height="20" loading="lazy"></p></section>` +
	`<aside><address>address</address></aside>` +
	`<blockquote cite="https://example.com/q"><p>quote</p></blockquote>` +
	`<details open=""><summary>summary</summary><div>div</div></details>` +
	`<dl><dt>dt</dt><dd>dd</dd></dl>` +
	`<figure><pre>pre</pre><figcaption>figcaption</figcaption></figure><hr>` +
	`<ol reversed="" start="2" type="a"><li>li</li></ol><ul><li>li</li></ul><search>search</search>` +
	`<table><caption>caption</caption><colgroup span="2"><col span="1"></colgroup>` +
	`<thead><tr><th abbr="h" colspan="1" headers="x" rowspan="1" scope="col">th</th></tr></thead>` +
	`<tbody><tr><td colspan="1" headers="y" rowspan="1">td</td></tr></tbody>` +
	`<tfoot><tr><td>tf</td></tr></tfoot></table>` +
	`<footer>footer</footer></article>`

// allowedHTML has every element and attribute that only the HTML kind keeps, as Render writes them.
const allowedHTML = `<iframe src="https://www.youtube.com/embed/x" width="560" height="315" loading="lazy" ` +
	`allow="autoplay" allowfullscreen="" referrerpolicy="strict-origin" sandbox="allow-scripts"></iframe>` +
	`<video src="https://example.com/v.mp4" poster="/media/p.jpg" controls="" width="640" height="360" ` +
	`autoplay="" muted="" loop="" playsinline="" preload="none">` +
	`<source src="/media/v.webm" type="video/webm">` +
	`<track src="/media/v.vtt" kind="subtitles" srclang="en" label="English" default=""></video>` +
	`<audio src="/media/a.mp3" controls="" autoplay="" muted="" loop="" preload="auto">fallback</audio>` +
	`<source src="https://example.com/a.ogg"><track src="https://example.com/a.vtt">`

func TestSanitizeAllowed(t *testing.T) {
	for _, kind := range []Kind{Legacy, HTML} {
		runTests(t, kind, []sanitizeTest{{name: "both kinds", in: allowedLegacy, want: allowedLegacy}})
	}
	runTests(t, HTML, []sanitizeTest{{name: "HTML kind", in: allowedHTML, want: allowedHTML}})
	runTests(t, Legacy, []sanitizeTest{{
		name: "HTML kind as legacy",
		in:   allowedHTML,
		changes: "removed <iframe> element and its content\n" +
			"removed <video> element and its content\n" +
			"removed <audio> element and its content\n" +
			"removed <source> element\n" +
			"removed <track> element",
	}})
	// Kinds other than HTML are as strict as Legacy.
	runTests(t, Kind(2), []sanitizeTest{{
		name:    "unknown kind",
		in:      `<iframe src="https://www.youtube.com/embed/x"></iframe>`,
		changes: "removed <iframe> element and its content",
	}})

	// The tests above have every element and attribute that the policy keeps.
	nodes, err := html.ParseFragment(allowedLegacy+allowedHTML, &html.Node{Type: html.ElementNode, Name: "div"},
		html.Options{Scripting: true})
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[[2]string]bool) // element and attribute names, "" for the element itself
	for len(nodes) > 0 {
		n := nodes[len(nodes)-1]
		nodes = nodes[:len(nodes)-1]
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			nodes = append(nodes, c)
		}
		seen[[2]string{n.Name, ""}] = true
		for _, a := range n.Attrs {
			seen[[2]string{n.Name, a.Name}], seen[[2]string{"", a.Name}] = true, true
		}
	}
	for _, names := range []map[string]bool{elements, htmlElements} {
		for el := range names {
			if !seen[[2]string{el, ""}] {
				t.Errorf("no test keeps <%s>", el)
			}
		}
	}
	for a := range globalAttrs {
		if !seen[[2]string{"", a}] {
			t.Errorf("no test keeps the global attribute %s", a)
		}
	}
	for el, attrs := range elementAttrs {
		for _, a := range attrs {
			if !seen[[2]string{el, a}] {
				t.Errorf("no test keeps %s of <%s>", a, el)
			}
		}
	}
}

// TestSanitizeElements checks the elements that are removed, with their content or without, as both kinds remove
// them.
func TestSanitizeElements(t *testing.T) {
	tests := []sanitizeTest{
		{
			name:    "script",
			in:      `<p>a<script>alert(1)</script>b</p><script src="https://example.com/x.js"></script>`,
			want:    `<p>ab</p>`,
			changes: "removed <script> element and its content (2 times)",
		},
		{
			name: "elements removed with their content",
			in: `<style>p{}</style><template><p>t</p></template><noscript><p>n</p></noscript>` +
				`<noembed>e</noembed><noframes>f</noframes><object data="/x"><p>fallback</p></object>` +
				`<embed src="/x"><applet><p>a</p></applet><select><option>o</option></select>` +
				`<datalist><option>d</option></datalist><optgroup><option>g</option></optgroup><option>o</option>` +
				`<textarea>t</textarea><title>t</title><canvas><p>c</p></canvas><param name="x"><base href="/">` +
				`<link rel="stylesheet" href="/x.css"><meta charset="utf-8"><input type="text"><keygen>`,
			changes: "removed <style> element and its content\n" +
				"removed <template> element and its content\n" +
				"removed <noscript> element and its content\n" +
				"removed <noembed> element and its content\n" +
				"removed <noframes> element and its content\n" +
				"removed <object> element and its content\n" +
				"removed <embed> element\n" +
				"removed <applet> element and its content\n" +
				"removed <select> element and its content\n" +
				"removed <datalist> element and its content\n" +
				"removed <optgroup> element and its content\n" +
				"removed <option> element and its content\n" +
				"removed <textarea> element and its content\n" +
				"removed <title> element and its content\n" +
				"removed <canvas> element and its content\n" +
				"removed <param> element\n" +
				"removed <base> element\n" +
				"removed <link> element\n" +
				"removed <meta> element\n" +
				"removed <input> element\n" +
				"removed <keygen> element",
		},
		{
			name: "elements unwrapped",
			in: `<center>c</center><font face="x" color="red">f</font><main>m</main><dialog open>d</dialog>` +
				`<form action="/x"><label>l</label><button onclick="x">b</button><fieldset><legend>g</legend>` +
				`s</fieldset><output>o</output><progress>p</progress><meter>m</meter></form>` +
				`<picture><img src="/a.jpg"></picture><map name="m"><area href="/x"></map><marquee>q</marquee>` +
				`<my-element>e</my-element><big>b</big><nobr>n</nobr><tt>t</tt><strike>s</strike><blink>k</blink>` +
				`<slot>s</slot><basefont><bgsound><listing>l</listing><rb>r</rb><rtc>c</rtc><image src="/x.png">`,
			want: `cfmdlbgsopm<img src="/a.jpg">qebntskslrc<img src="/x.png">`,
			changes: "removed <center> element, keeping its content\n" +
				"removed <font> element, keeping its content\n" +
				"removed <main> element, keeping its content\n" +
				"removed <dialog> element, keeping its content\n" +
				"removed <form> element, keeping its content\n" +
				"removed <label> element, keeping its content\n" +
				"removed <button> element, keeping its content\n" +
				"removed <fieldset> element, keeping its content\n" +
				"removed <legend> element, keeping its content\n" +
				"removed <output> element, keeping its content\n" +
				"removed <progress> element, keeping its content\n" +
				"removed <meter> element, keeping its content\n" +
				"removed <picture> element, keeping its content\n" +
				"removed <map> element, keeping its content\n" +
				"removed <area> element\n" +
				"removed <marquee> element, keeping its content\n" +
				"removed <my-element> element, keeping its content\n" +
				"removed <big> element, keeping its content\n" +
				"removed <nobr> element, keeping its content\n" +
				"removed <tt> element, keeping its content\n" +
				"removed <strike> element, keeping its content\n" +
				"removed <blink> element, keeping its content\n" +
				"removed <slot> element, keeping its content\n" +
				"removed <basefont> element\n" +
				"removed <bgsound> element\n" +
				"removed <listing> element, keeping its content\n" +
				"removed <rb> element, keeping its content\n" +
				"removed <rtc> element, keeping its content",
		},
		{
			name:    "unwrapped in place",
			in:      `<p>a<font>b<b>c</b><script>x</script>d</font>e</p>`,
			want:    `<p>ab<b>c</b>de</p>`,
			changes: "removed <font> element, keeping its content\nremoved <script> element and its content",
		},
		{
			name: "text of xmp and plaintext",
			in:   `<xmp><script>alert(1)</script>&amp;</xmp><plaintext></plaintext><img src=x onerror=alert(1)>`,
			want: `&lt;script&gt;alert(1)&lt;/script&gt;&amp;amp;&lt;/plaintext&gt;&lt;img src=x onerror=alert(1)&gt;`,
			changes: "removed <xmp> element, keeping its content\n" +
				"removed <plaintext> element, keeping its content",
		},
		{
			name: "noscript, parsed with scripting",
			in:   `<noscript><p title="</noscript><img src=x onerror=alert(1)>"></noscript>`,
			want: `"&gt;`,
			changes: "removed <noscript> element and its content\n" + `removed <img> element: invalid src "x": ` +
				`must be an absolute URL or a path starting with "/"`,
		},
		{
			name:    "template content",
			in:      `<template><script>alert(1)</script><img src="/a.jpg"></template>x`,
			want:    `x`,
			changes: "removed <template> element and its content",
		},
		{
			name:    "svg and math",
			in:      `<svg><script>alert(1)</script><a href="javascript:alert(1)">x</a></svg><math><mi>y</mi></math>z`,
			want:    `z`,
			changes: "removed <svg> element and its content\nremoved <math> element and its content",
		},
		{
			name:    "HTML in svg",
			in:      `<svg><foreignObject><p>a</p><img src="/a.jpg"></foreignObject><desc><b>b</b></desc></svg>`,
			changes: "removed <svg> element and its content",
		},
		{
			name: "HTML after svg",
			in:   `<svg><p>a</p></svg><math><mtext><table><tr><td>b</td></tr></table></mtext></math>`,
			want: `<p>a</p>`,
			changes: "removed <svg> element and its content\n" +
				"removed <math> element and its content",
		},
		{
			name: "style in svg",
			in: `<svg></p><style><a id="</style><img src=1 onerror=alert(1)>"></svg>` +
				`<math><mtext><table><mglyph><style><img src=1 onerror=alert(1)></style></table></mtext></math>`,
			want: `<p></p>"&gt;`,
			changes: "removed <svg> element and its content\n" +
				"removed <style> element and its content\n" +
				`removed <img> element: invalid src "1": must be an absolute URL or a path starting with "/"` + "\n" +
				"removed <math> element and its content",
		},
		{
			name:    "svg in a table",
			in:      `<table><tr><td>a</td></tr><svg><g/></svg></table>`,
			want:    `<table><tbody><tr><td>a</td></tr></tbody></table>`,
			changes: "removed <svg> element and its content",
		},
		{
			name:    "end tag of a link in svg",
			in:      `<a href="/x"><svg><foreignObject></a>x</foreignObject></svg>y</a>`,
			want:    `<a href="/x">y</a>`,
			changes: "removed <svg> element and its content",
		},
		{
			name:    "h1",
			in:      `<h1 id="title" class="t">Title</h1><h1>Again</h1>`,
			want:    `<h2 id="title" class="t">Title</h2><h2>Again</h2>`,
			changes: "changed <h1> to <h2> (2 times)",
		},
		{
			name:    "h1 with a removed attribute",
			in:      `<h1 style="color:red">Title</h1>`,
			want:    `<h2>Title</h2>`,
			changes: "changed <h1> to <h2>\nremoved style attribute of <h2>",
		},
		{
			name: "comments, processing instructions and doctypes",
			in:   `<!-- c --><?php echo 1 ?>a<!DOCTYPE html>b<![CDATA[c]]><p>d<!--[if IE]><script></script><![endif]--></p>`,
			want: `ab<p>d</p>`,
		},
		{
			name: "shortcodes",
			in:   `[gallery ids="1,2"]<p>[caption id="attachment_1" align="alignnone"]text[/caption]</p>`,
			want: `[gallery ids="1,2"]<p>[caption id="attachment_1" align="alignnone"]text[/caption]</p>`,
		},
		{name: "empty"},
		{name: "text", in: "a &amp; b &lt; c > d \"e\" 'f' &copy;", want: "a &amp; b &lt; c &gt; d \"e\" 'f' ©"},
		{name: "unclosed tags", in: `<p><b>bold<i>both</b>italic`, want: `<p><b>bold<i>both</i></b><i>italic</i></p>`},
		{
			name: "broken quotes",
			in: `<p title="a>b<script>alert(1)</script>">1</p><p title='a" onclick="alert(1)'>2</p>` +
				`<p title=a"b onclick=alert(1)>3</p><a href="/x' onclick='alert(1)">4</a>`,
			want: `<p title="a&gt;b&lt;script&gt;alert(1)&lt;/script&gt;">1</p>` +
				`<p title="a&#34; onclick=&#34;alert(1)">2</p><p title="a&#34;b">3</p>` +
				`<a href="/x&#39; onclick=&#39;alert(1)">4</a>`,
			changes: "removed onclick attribute of <p>",
		},
		{name: "tag at the end", in: `<p>x</p><img src="/a.jpg" onerror="alert(1)"`, want: `<p>x</p>`},
		{name: "attribute at the end", in: `<p>x</p><img src="/a.jpg" title="a`, want: `<p>x</p>`},
		{
			name: "copies of formatting elements",
			in:   `<a href="https://example.com/"><b>bold<p>para</a> after</b>`,
			want: `<a href="https://example.com/"><b>bold</b></a><b></b>` +
				`<p><b><a href="https://example.com/">para</a> after</b></p>`,
		},
		{
			name:    "foster parenting",
			in:      `<table><tr><td>a</td></tr><script>x</script><font>y</font></table>`,
			want:    `y<table><tbody><tr><td>a</td></tr></tbody></table>`,
			changes: "removed <font> element, keeping its content\nremoved <script> element and its content",
		},
		{
			name:    "line feeds in pre",
			in:      "<pre>\nx</pre><pre><font>\ny</font></pre>",
			want:    "<pre>x</pre><pre>\n\ny</pre>",
			changes: "removed <font> element, keeping its content",
		},
	}
	for _, kind := range []Kind{Legacy, HTML} {
		runTests(t, kind, tests)
	}
}

// TestSanitizeMedia checks the elements that only the HTML kind keeps.
func TestSanitizeMedia(t *testing.T) {
	runTests(t, HTML, []sanitizeTest{
		{
			name:    "content of iframe",
			in:      `<iframe src="https://example.com/">fallback <b>x</b><script>alert(1)</script></iframe>`,
			want:    `<iframe src="https://example.com/"></iframe>`,
			changes: "removed the content of <iframe>",
		},
		{
			name: "whitespace in iframe",
			in:   "<iframe src=\"https://example.com/\">\n </iframe>",
			want: `<iframe src="https://example.com/"></iframe>`,
		},
		{
			name: "attributes of iframe",
			in:   `<iframe src="https://example.com/" srcdoc="<script>alert(1)</script>" name="x" onload="x"></iframe>`,
			want: `<iframe src="https://example.com/"></iframe>`,
			changes: "removed srcdoc attribute of <iframe>\n" +
				"removed name attribute of <iframe>\n" +
				"removed onload attribute of <iframe>",
		},
		{
			name: "media without src",
			in:   `<video controls><source src="/v.webm"><source><track kind="captions"></video><audio></audio>`,
			want: `<video controls=""><source src="/v.webm"></video><audio></audio>`,
			changes: "removed <source> element: missing src\n" +
				"removed <track> element: missing src",
		},
		{
			name:    "source in picture",
			in:      `<picture><source srcset="/a.webp" type="image/webp"><img src="/a.jpg" alt="a"></picture>`,
			want:    `<img src="/a.jpg" alt="a">`,
			changes: "removed <picture> element, keeping its content\nremoved <source> element: missing src",
		},
		{
			name:    "fallback content",
			in:      `<video src="/v.mp4"><p>Your browser <a href="/v.mp4" onclick="x">cannot</a> play it.</p></video>`,
			want:    `<video src="/v.mp4"><p>Your browser <a href="/v.mp4">cannot</a> play it.</p></video>`,
			changes: "removed onclick attribute of <a>",
		},
	})
}

// TestSanitizeURLs checks each kind of URL with allowed and rejected values.
func TestSanitizeURLs(t *testing.T) {
	const credentials = "credentials in URLs are not allowed"
	tab, replacement := string(rune(9)), string(rune(0xFFFD))
	invalidHref := func(v, reason string) string {
		return fmt.Sprintf("removed href attribute of <a>: invalid URL %q: %s", v, reason)
	}
	links := []sanitizeTest{
		{
			name: "allowed links",
			in: `<a href="https://example.com/a?b=1&amp;c=2#d">1</a><a href="http://example.com/">2</a>` +
				`<a href="HTTPS://EXAMPLE.COM/">3</a><a href="mailto:info@example.com?subject=hi">4</a>` +
				`<a href="tel:+81-3-1234-5678">5</a><a href="/about/">6</a><a href="/">7</a><a href="/?p=1">8</a>` +
				`<a href="#top">9</a><a href="#">10</a><a href="https://例え.jp/パス">11</a>`,
			want: `<a href="https://example.com/a?b=1&amp;c=2#d">1</a><a href="http://example.com/">2</a>` +
				`<a href="HTTPS://EXAMPLE.COM/">3</a><a href="mailto:info@example.com?subject=hi">4</a>` +
				`<a href="tel:+81-3-1234-5678">5</a><a href="/about/">6</a><a href="/">7</a><a href="/?p=1">8</a>` +
				`<a href="#top">9</a><a href="#">10</a><a href="https://例え.jp/パス">11</a>`,
		},
		{
			name: "normalized links",
			in:   "<a href=\" https://example.com/ \">1</a><a href=\"&#9;/a&#10;b\">2</a><a href=\"&#x23;top\">3</a>",
			want: `<a href="https://example.com/">1</a><a href="/ab">2</a><a href="#top">3</a>`,
		},
		{
			name: "javascript",
			in: `<a href="javascript:alert(1)">1</a><a href="JaVaScRiPt:alert(1)">2</a>` +
				`<a href=" javascript:alert(1)">3</a>`,
			want: `<a>1</a><a>2</a><a>3</a>`,
			changes: invalidHref("javascript:alert(1)", `scheme "javascript" is not allowed`) + "\n" +
				invalidHref("JaVaScRiPt:alert(1)", `scheme "javascript" is not allowed`) + "\n" +
				invalidHref(" javascript:alert(1)", `scheme "javascript" is not allowed`),
		},
		{
			name: "schemes hidden by character references",
			in: `<a href="&#106;avascript:alert(1)">1</a><a href="javascript&colon;alert(1)">2</a>` +
				`<a href="&#x6A&#x61&#x76&#x61&#x73&#x63&#x72&#x69&#x70&#x74&#x3A;alert(1)">3</a>`,
			want:    `<a>1</a><a>2</a><a>3</a>`,
			changes: invalidHref("javascript:alert(1)", `scheme "javascript" is not allowed`) + " (3 times)",
		},
		{
			name: "schemes split by tabs and line feeds",
			in: "<a href=\"jav&#x09;ascript:alert(1)\">1</a><a href=\"java" + tab + "script:alert(1)\">2</a>" +
				"<a href=\"java\nscript:alert(1)\">3</a><a href=\"&#1;javascript:alert(1)\">4</a>" +
				"<a href=\"java&Tab;script:alert(1)\">5</a>",
			want: `<a>1</a><a>2</a><a>3</a><a>4</a><a>5</a>`,
			changes: invalidHref("jav"+tab+"ascript:alert(1)", `scheme "javascript" is not allowed`) + "\n" +
				invalidHref("java"+tab+"script:alert(1)", `scheme "javascript" is not allowed`) + " (2 times)\n" +
				invalidHref("java\nscript:alert(1)", `scheme "javascript" is not allowed`) + "\n" +
				invalidHref(string(rune(1))+"javascript:alert(1)", `scheme "javascript" is not allowed`),
		},
		{
			name:    "NUL before a scheme",
			in:      `<a href="&#0;javascript:alert(1)">1</a>`,
			want:    `<a>1</a>`,
			changes: invalidHref(replacement+"javascript:alert(1)", "invalid URL"),
		},
		{
			name: "other schemes",
			in: `<a href="data:text/html,&lt;script&gt;alert(1)&lt;/script&gt;">1</a><a href="vbscript:msgbox(1)">2</a>` +
				`<a href="file:///etc/passwd">3</a><a href="ftp://example.com/">4</a>`,
			want: `<a>1</a><a>2</a><a>3</a><a>4</a>`,
			changes: invalidHref("data:text/html,<script>alert(1)</script>", `scheme "data" is not allowed`) + "\n" +
				invalidHref("vbscript:msgbox(1)", `scheme "vbscript" is not allowed`) + "\n" +
				invalidHref("file:///etc/passwd", `scheme "file" is not allowed`) + "\n" +
				invalidHref("ftp://example.com/", `scheme "ftp" is not allowed`),
		},
		{
			name: "hosts",
			in: `<a href="//evil.example/">1</a><a href="/\evil.example/">2</a><a href="https:\\evil.example/">3</a>` +
				`<a href="https://user:pass@example.com/">4</a><a href="https://evil.example\@example.com/">5</a>` +
				`<a href="https:evil.example">6</a><a href="https:///evil.example">7</a>`,
			want: `<a>1</a><a>2</a><a>3</a><a>4</a><a>5</a><a>6</a><a>7</a>`,
			changes: invalidHref("//evil.example/", "protocol-relative URLs are not allowed") + "\n" +
				invalidHref(`/\evil.example/`, "backslashes are not allowed") + "\n" +
				invalidHref(`https:\\evil.example/`, "backslashes are not allowed") + "\n" +
				invalidHref("https://user:pass@example.com/", credentials) + "\n" +
				invalidHref(`https://evil.example\@example.com/`, "backslashes are not allowed") + "\n" +
				invalidHref("https:evil.example", "missing host") + "\n" +
				invalidHref("https:///evil.example", "missing host"),
		},
		{
			name: "relative URLs",
			in:   `<a href="about/">1</a><a href="./a">2</a><a href="../a">3</a><a href="?q=1">4</a><a href="">5</a>`,
			want: `<a>1</a><a>2</a><a>3</a><a>4</a><a>5</a>`,
			changes: invalidHref("about/", absolute) + "\n" +
				invalidHref("./a", absolute) + "\n" +
				invalidHref("../a", absolute) + "\n" +
				invalidHref("?q=1", absolute) + "\n" +
				invalidHref("", "empty URL"),
		},
		{
			name: "cite",
			in: `<blockquote cite="https://example.com/">1</blockquote><del cite="#c">2</del>` +
				`<ins cite="mailto:a@example.com">3</ins><blockquote cite="javascript:alert(1)">4</blockquote>` +
				`<del cite="del">5</del><ins cite="//example.com/">6</ins>`,
			want: `<blockquote cite="https://example.com/">1</blockquote><del cite="#c">2</del>` +
				`<ins cite="mailto:a@example.com">3</ins><blockquote>4</blockquote><del>5</del><ins>6</ins>`,
			changes: `removed cite attribute of <blockquote>: invalid URL "javascript:alert(1)": ` +
				`scheme "javascript" is not allowed` + "\n" +
				`removed cite attribute of <del>: invalid URL "del": ` + absolute + "\n" +
				`removed cite attribute of <ins>: invalid URL "//example.com/": protocol-relative URLs are not allowed`,
		},
		{
			name: "images",
			in: `<img src="/media/a.jpg"><img src="https://example.com/a.png"><img src="http://example.com/a.png">` +
				`<img src=" /media/b.jpg">` +
				`<img src="mailto:a@example.com"><img src="tel:1"><img src="#a"><img src="a.png">` +
				`<img src="//cdn.example/a.png"><img src="javascript:alert(1)"><img alt="none"><img src="">` +
				`<img src="data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAC">`,
			want: `<img src="/media/a.jpg"><img src="https://example.com/a.png"><img src="http://example.com/a.png">` +
				`<img src="/media/b.jpg">`,
			changes: `removed <img> element: invalid src "mailto:a@example.com": scheme "mailto" is not allowed` + "\n" +
				`removed <img> element: invalid src "tel:1": scheme "tel" is not allowed` + "\n" +
				`removed <img> element: invalid src "#a": ` + absolute + "\n" +
				`removed <img> element: invalid src "a.png": ` + absolute + "\n" +
				`removed <img> element: invalid src "//cdn.example/a.png": protocol-relative URLs are not allowed` +
				"\n" +
				`removed <img> element: invalid src "javascript:alert(1)": scheme "javascript" is not allowed` + "\n" +
				"removed <img> element: missing src\n" +
				`removed <img> element: invalid src "": empty URL` + "\n" +
				`removed <img> element: invalid src "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFc...": ` +
				`scheme "data" is not allowed`,
		},
	}
	for _, kind := range []Kind{Legacy, HTML} {
		runTests(t, kind, links)
	}
	runTests(t, HTML, []sanitizeTest{
		{
			name: "allowed media",
			in: `<video src="https://example.com/v.mp4" poster="https://example.com/p.jpg"></video>` +
				`<video src="/media/v.mp4" poster="/media/p.jpg"></video><audio src="/media/a.mp3"></audio>` +
				`<source src="/media/a.ogg"><track src="https://example.com/a.vtt">`,
			want: `<video src="https://example.com/v.mp4" poster="https://example.com/p.jpg"></video>` +
				`<video src="/media/v.mp4" poster="/media/p.jpg"></video><audio src="/media/a.mp3"></audio>` +
				`<source src="/media/a.ogg"><track src="https://example.com/a.vtt">`,
		},
		{
			name: "rejected media",
			in: `<video src="http://example.com/v.mp4" poster="javascript:alert(1)"></video>` +
				`<audio src="mailto:a@example.com"></audio><audio src="#a"></audio>` +
				`<source src="http://example.com/a.ogg"><track src="data:text/vtt,x">`,
			want: `<video></video><audio></audio><audio></audio>`,
			changes: `removed src attribute of <video>: invalid URL "http://example.com/v.mp4": ` +
				`scheme "http" is not allowed` + "\n" +
				`removed poster attribute of <video>: invalid URL "javascript:alert(1)": ` +
				`scheme "javascript" is not allowed` + "\n" +
				`removed src attribute of <audio>: invalid URL "mailto:a@example.com": ` +
				`scheme "mailto" is not allowed` + "\n" +
				`removed src attribute of <audio>: invalid URL "#a": ` + absolute + "\n" +
				`removed <source> element: invalid src "http://example.com/a.ogg": scheme "http" is not allowed` +
				"\n" +
				`removed <track> element: invalid src "data:text/vtt,x": scheme "data" is not allowed`,
		},
		{
			name: "frames",
			in: `<iframe src="https://www.youtube.com/embed/x"></iframe><iframe src="http://example.com/"></iframe>` +
				`<iframe src="/embed/x"></iframe><iframe src="javascript:alert(1)"></iframe>` +
				`<iframe src="data:text/html,x"></iframe><iframe src="//www.youtube.com/embed/x"></iframe>` +
				`<iframe src="mailto:a@example.com"></iframe><iframe></iframe>`,
			want: `<iframe src="https://www.youtube.com/embed/x"></iframe>`,
			changes: `removed <iframe> element: invalid src "http://example.com/": scheme "http" is not allowed` + "\n" +
				`removed <iframe> element: invalid src "/embed/x": must be an https URL` + "\n" +
				`removed <iframe> element: invalid src "javascript:alert(1)": scheme "javascript" is not allowed` +
				"\n" +
				`removed <iframe> element: invalid src "data:text/html,x": scheme "data" is not allowed` + "\n" +
				`removed <iframe> element: invalid src "//www.youtube.com/embed/x": ` +
				"protocol-relative URLs are not allowed\n" +
				`removed <iframe> element: invalid src "mailto:a@example.com": scheme "mailto" is not allowed` + "\n" +
				"removed <iframe> element: missing src",
		},
	})
}

// TestSanitizeAttributes checks the attributes that are removed, and the values of class, id, target and rel.
func TestSanitizeAttributes(t *testing.T) {
	tests := []sanitizeTest{
		{
			name: "disallowed attributes",
			in: `<p style="color:red" onclick="alert(1)" data-x="1" aria-label="l" role="note" xmlns="x">a</p>` +
				`<a name="old" href="/x" ping="/p" download>b</a><img src="/a.jpg" srcset="/b.jpg 2x" ` +
				`sizes="100vw" onerror="alert(1)" usemap="#m"><div href="/x" src="/x" cite="/x">c</div>` +
				`<q cite="https://example.com/">d</q><details open ontoggle="alert(1)" name="n">e</details>`,
			want: `<p>a</p><a href="/x">b</a><img src="/a.jpg"><div>c</div><q>d</q><details open="">e</details>`,
			changes: "removed style attribute of <p>\n" +
				"removed onclick attribute of <p>\n" +
				"removed data-x attribute of <p>\n" +
				"removed aria-label attribute of <p>\n" +
				"removed role attribute of <p>\n" +
				"removed xmlns attribute of <p>\n" +
				"removed name attribute of <a>\n" +
				"removed ping attribute of <a>\n" +
				"removed download attribute of <a>\n" +
				"removed srcset attribute of <img>\n" +
				"removed sizes attribute of <img>\n" +
				"removed onerror attribute of <img>\n" +
				"removed usemap attribute of <img>\n" +
				"removed href attribute of <div>\n" +
				"removed src attribute of <div>\n" +
				"removed cite attribute of <div>\n" +
				"removed cite attribute of <q>\n" +
				"removed ontoggle attribute of <details>\n" +
				"removed name attribute of <details>",
		},
		{
			name: "values kept as they are",
			in: `<table><tr><td colspan="javascript:alert(1)">a</td></tr></table>` +
				`<p title="&lt;script&gt;" lang="x&quot;y">b</p><ol type="'"><li value="&amp;">c</li></ol>`,
			want: `<table><tbody><tr><td colspan="javascript:alert(1)">a</td></tr></tbody></table>` +
				`<p title="&lt;script&gt;" lang="x&#34;y">b</p><ol type="&#39;"><li value="&amp;">c</li></ol>`,
		},
		{
			name: "class",
			in: "<p class=\" a  b&#9;c\nd b-0paragr1 hl-1a2b3c4d B-0PARAGR1 b-0paragr1 b-0para hl-1a2b3c4g\">x</p>" +
				`<div class="b-00000000">y</div><div class="">z</div><span class=" ">w</span>`,
			want: `<p class="a b c d B-0PARAGR1 b-0para hl-1a2b3c4g">x</p><div>y</div><div>z</div><span>w</span>`,
			changes: `removed class "b-0paragr1" of <p>: reserved for the core (2 times)` + "\n" +
				`removed class "hl-1a2b3c4d" of <p>: reserved for the core` + "\n" +
				`removed class "b-00000000" of <div>: reserved for the core`,
		},
		{
			name: "id",
			in: `<h2 id="intro-1_A">a</h2><h2 id="a b">b</h2><p id="">c</p><p id="見出し">d</p><p id="a.b">e</p>` +
				`<h1 id="#x">f</h1>`,
			want: `<h2 id="intro-1_A">a</h2><h2>b</h2><p>c</p><p>d</p><p>e</p><h2>f</h2>`,
			changes: `removed id attribute of <h2>: invalid id "a b": use ASCII letters, digits, "-" and "_"` + "\n" +
				"removed id attribute of <p>: empty id\n" +
				`removed id attribute of <p>: invalid id "見出し": use ASCII letters, digits, "-" and "_"` + "\n" +
				`removed id attribute of <p>: invalid id "a.b": use ASCII letters, digits, "-" and "_"` + "\n" +
				"changed <h1> to <h2>\n" +
				`removed id attribute of <h2>: invalid id "#x": use ASCII letters, digits, "-" and "_"`,
		},
		{
			name: "target",
			in: `<a href="/" target="_blank">1</a><a target="_BLANK">2</a><a target="_Blank" rel="nofollow">3</a>` +
				`<a target="_self">4</a><a target="_top">5</a><a target="name">6</a><a target=" _blank">7</a>` +
				`<a target="">8</a><a target="_blan` + string(rune(0x212A)) + `">9</a>`,
			want: `<a href="/" target="_blank" rel="noopener">1</a><a target="_blank" rel="noopener">2</a>` +
				`<a target="_blank" rel="noopener nofollow">3</a><a>4</a><a>5</a><a>6</a><a>7</a><a>8</a><a>9</a>`,
			changes: "removed target attribute of <a>: only _blank is allowed (6 times)",
		},
		{
			name: "rel",
			in: `<a rel="NoFollow external nofollow UGC sponsored noreferrer opener noopener">1</a>` +
				`<a href="/" rel="external">2</a><a rel="">3</a><a rel=" ">4</a>` +
				"<a rel=\"nofollow&#9;ugc\nsponsored\">5</a>",
			want: `<a rel="nofollow ugc sponsored noreferrer noopener">1</a><a href="/">2</a><a>3</a><a>4</a>` +
				`<a rel="nofollow ugc sponsored">5</a>`,
			changes: `removed rel value "external" of <a> (2 times)` + "\n" + `removed rel value "opener" of <a>`,
		},
		{
			name: "rel of new tabs",
			in: `<a target="_blank" rel="nofollow noopener">1</a><a rel="ugc" target="_blank">2</a>` +
				`<a target="_blank" rel="external">3</a><a rel="noopener noopener" target="_blank">4</a>`,
			want: `<a target="_blank" rel="noopener nofollow">1</a><a rel="noopener ugc" target="_blank">2</a>` +
				`<a target="_blank" rel="noopener">3</a><a rel="noopener" target="_blank">4</a>`,
			changes: `removed rel value "external" of <a>`,
		},
	}
	for _, kind := range []Kind{Legacy, HTML} {
		runTests(t, kind, tests)
	}
}

func TestSanitizeDepth(t *testing.T) {
	const message = "removed elements nested more than 256 deep, keeping their content"
	nested := func(n int, inner string) string {
		return strings.Repeat("<div>", n) + inner + strings.Repeat("</div>", n)
	}
	tests := []sanitizeTest{
		{name: "256 deep", in: nested(256, "x"), want: nested(256, "x")},
		{name: "257 deep", in: nested(257, "x"), want: nested(256, "x"), changes: message},
		{name: "300 deep", in: nested(300, "x"), want: nested(256, "x"), changes: message},
		{
			name:    "elements past the depth",
			in:      nested(255, "<p>a<b>b<i>c</i></b><br>d<img src=\"/a.jpg\"><script>x</script></p>"),
			want:    nested(255, "<p>abcd</p>"),
			changes: message + "\nremoved <script> element and its content",
		},
		{
			name:    "elements past the depth twice",
			in:      nested(256, "<b>a</b>") + nested(257, "<i>b</i>"),
			want:    nested(256, "a") + nested(256, "b"),
			changes: message,
		},
		{
			name:    "depth of unwrapped elements",
			in:      strings.Repeat("<font>", 300) + nested(256, "x") + strings.Repeat("</font>", 300),
			want:    nested(256, "x"),
			changes: "removed <font> element, keeping its content (300 times)",
		},
	}
	for _, kind := range []Kind{Legacy, HTML} {
		runTests(t, kind, tests)
	}
}

func TestSanitizeChanges(t *testing.T) {
	long, dotted := strings.Repeat("x", 100), strings.Repeat("x.", 50)
	// 63 bytes, then a character of 3 bytes over the 64th byte.
	japanese := strings.Repeat("a", 63) + "見出し"
	esc := string(rune(0x1B))
	var many, manyWant strings.Builder
	for i := range 150 {
		fmt.Fprintf(&many, " a%d", i)
		if i < 100 {
			if i > 0 {
				manyWant.WriteByte('\n')
			}
			fmt.Fprintf(&manyWant, "removed a%d attribute of <p>", i)
		}
	}
	tests := []sanitizeTest{
		{
			name:    "counted",
			in:      `<p style="a">1</p><font>2</font><p style="b">3</p><p style="c">4</p>`,
			want:    `<p>1</p>2<p>3</p><p>4</p>`,
			changes: "removed style attribute of <p> (3 times)\nremoved <font> element, keeping its content",
		},
		{
			name: "long values",
			in: `<a href="javascript:` + long + `">1</a><a href="` + japanese + `">2</a><p class="b-0paragr1">3</p>` +
				`<p id="` + dotted + `">4</p><p id="` + long + `">5</p>`,
			want: `<a>1</a><a>2</a><p>3</p><p>4</p><p id="` + long + `">5</p>`,
			changes: `removed href attribute of <a>: invalid URL "javascript:` + long[:53] + `...": ` +
				`scheme "javascript" is not allowed` + "\n" +
				`removed href attribute of <a>: invalid URL "` + japanese[:63] + `...": ` + absolute + "\n" +
				`removed class "b-0paragr1" of <p>: reserved for the core` + "\n" +
				`removed id attribute of <p>: invalid id "` + dotted[:64] + `...": use ASCII letters, digits, "-" and "_"`,
		},
		{
			name:    "id longer than content allows",
			in:      `<p id="` + strings.Repeat("x", content.MaxTextBytes+1) + `">1</p>`,
			want:    `<p>1</p>`,
			changes: `removed id attribute of <p>: invalid id "` + long[:64] + `...": too long`,
		},
		{
			name:    "long schemes",
			in:      `<a href="` + long + `:x">1</a>`,
			want:    `<a>1</a>`,
			changes: `removed href attribute of <a>: invalid URL "` + long[:64] + `...": scheme "` + long[:56] + `...`,
		},
		{
			name: "long names",
			in:   `<p ` + long + `="1">1</p><` + long + `>2</` + long + `>`,
			want: `<p>1</p>2`,
			changes: "removed " + long[:64] + "... attribute of <p>\n" +
				"removed <" + long[:64] + "...> element, keeping its content",
		},
		{
			name: "names with escapes",
			in:   `<p ` + esc + `[31m="1" "a"=2 b\c=3>1</p><x` + esc + `[2J>2</x` + esc + `[2J>`,
			want: `<p>1</p>2`,
			changes: fmt.Sprintf("removed %q attribute of <p>\n", esc+"[31m") +
				`removed "\"a\"" attribute of <p>` + "\n" +
				`removed "b\\c" attribute of <p>` + "\n" +
				fmt.Sprintf("removed <%q> element, keeping its content", "x"+esc+"[2j"),
		},
		{
			name:    "at most 100 messages",
			in:      `<p` + many.String() + `>1</p>`,
			want:    `<p>1</p>`,
			changes: manyWant.String() + "\nand 50 more changes",
		},
		{
			name: "counted past 100 messages",
			in:   `<p` + many.String()[:strings.Index(many.String(), " a101")] + `>1</p><p a0 a100>2</p>`,
			want: `<p>1</p><p>2</p>`,
			changes: strings.Replace(manyWant.String(), "of <p>", "of <p> (2 times)", 1) +
				"\nand 2 more changes",
		},
		{
			name:    "one more change",
			in:      `<p` + many.String()[:strings.Index(many.String(), " a101")] + `>1</p>`,
			want:    `<p>1</p>`,
			changes: manyWant.String() + "\nand 1 more change",
		},
	}
	for _, kind := range []Kind{Legacy, HTML} {
		runTests(t, kind, tests)
	}
}

func TestSanitizeLimits(t *testing.T) {
	quotes := strings.Repeat(`"`, 4000)
	// fonts returns n font elements that the parser copies into each of m p elements.
	fonts := func(n, m int) string {
		var b strings.Builder
		b.WriteString("<p>")
		for i := range n {
			fmt.Fprintf(&b, "<font a=%d>", i)
		}
		return b.String() + strings.Repeat("</p><p>x", m)
	}
	tests := []struct {
		name string
		in   string
		err  bool
	}{
		{"steps of the parser", strings.Repeat("<div>", 10000) + strings.Repeat("</p>", 10000), true},
		{"nodes of the parser", fonts(1000, 10000), true},
		{"within the limits of the parser", fonts(20, 10), false},
		// The parser copies the b element with its title into each p element: Render would write 5 bytes for each
		// quote in each copy.
		{"long output", `<p><b title='` + quotes + `'>` + strings.Repeat("</p><p>x", 18), true},
		{"long attributes removed", `<p><b data-x='` + quotes + `'>` + strings.Repeat("</p><p>x", 18), false},
	}
	for _, tt := range tests {
		got, changes, err := Sanitize(tt.in, Options{Kind: HTML})
		if !tt.err {
			if err != nil {
				t.Errorf("%s: %v", tt.name, err)
			}
			continue
		}
		if !errors.Is(err, ErrLimit) || !errors.Is(err, html.ErrLimit) || got != "" || changes != nil {
			t.Errorf("%s: %q, %d changes and the error %v, want ErrLimit and nothing else", tt.name, got,
				len(changes), err)
		}
	}
	for _, tt := range []struct{ n, nodes, steps, out int }{
		{0, 1024, 65536, 65536},
		{1 << 20, 1<<20 + 1024, 64<<20 + 65536, 16<<20 + 65536},
		{math.MaxInt, (math.MaxInt-65536)/64 + 1024, (math.MaxInt-65536)/64*64 + 65536,
			(math.MaxInt-65536)/64*16 + 65536},
	} {
		opts, out := limits(tt.n)
		if !opts.Scripting || opts.MaxNodes != tt.nodes || opts.MaxSteps != tt.steps || out != tt.out {
			t.Errorf("limits(%d) = %+v, %d; want MaxNodes %d, MaxSteps %d, %d", tt.n, opts, out, tt.nodes, tt.steps,
				tt.out)
		}
	}
}

// TestClean cleans trees that the parser does not make for the content of a div element.
func TestClean(t *testing.T) {
	element := func(name string, children ...*html.Node) *html.Node {
		n := &html.Node{Type: html.ElementNode, Name: name}
		for _, c := range children {
			n.AppendChild(c)
		}
		return n
	}
	text := func(s string) *html.Node { return &html.Node{Type: html.TextNode, Data: s} }
	iframe := element("iframe", element("script", text("alert(1)")), text("b"))
	iframe.Attrs = []html.Attr{{Name: "src", Value: "https://example.com/"}}
	template := element("template")
	template.Content = &html.Node{Type: html.DocumentFragmentNode}
	template.Content.AppendChild(element("script", text("alert(1)")))
	link := element("a", text("e"))
	link.Attrs = []html.Attr{
		{Namespace: "xlink", Name: "href", Value: "javascript:alert(1)"}, {Name: "href", Value: "/e"},
		{Name: "rel", Value: "opener"}, {Name: "target", Value: "_blank"}, {Name: "rel", Value: "nofollow"},
		{Name: "href", Value: "javascript:alert(1)"}, {Name: "target", Value: "x"},
	}
	svgP := element("p", text("f"))
	svgP.Namespace = html.SVG
	escaped := element("m" + string(rune(0x1B)))
	escaped.Namespace = html.MathML
	fragment := &html.Node{Type: html.DocumentFragmentNode}
	fragment.AppendChild(element("p", text("g")))
	root := &html.Node{Type: html.DocumentFragmentNode}
	for _, n := range []*html.Node{
		element("html", element("head", element("title", text("t"))), element("body", text("a"))),
		element("frameset", element("frame")),
		element("frame"),
		iframe,
		element("noscript", element("img")),
		template,
		element("textarea", element("b", text("c"))),
		element("div", text("d"), link, svgP, escaped, fragment, &html.Node{Type: html.DoctypeNode, Name: "html"}),
		{Type: html.CommentNode, Data: "c"},
		{Type: html.ProcessingInstructionNode, Name: "pi", Data: "d"},
	} {
		root.AppendChild(n)
	}
	c := cleaner{kind: HTML}
	c.clean(root)
	want := `a<iframe src="https://example.com/"></iframe>` +
		`<div>d<a href="/e" rel="noopener" target="_blank">e</a></div>`
	if got := html.Render(root); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	want = "removed <html> element, keeping its content\n" +
		"removed <head> element and its content\n" +
		"removed <body> element, keeping its content\n" +
		"removed <frameset> element and its content\n" +
		"removed <frame> element\n" +
		"removed the content of <iframe>\n" +
		"removed <noscript> element and its content\n" +
		"removed <template> element and its content\n" +
		"removed <textarea> element and its content\n" +
		"removed xlink:href attribute of <a>\n" +
		`removed rel value "opener" of <a>` + "\n" +
		"removed <p> element and its content\n" +
		fmt.Sprintf("removed <%q> element and its content", "m"+string(rune(0x1B)))
	if got := formatChanges(c.changes.result()); got != want {
		t.Errorf("changes\n got %q\nwant %q", got, want)
	}
}

// TestPolicy checks that the tables of the policy agree with each other, and that no attribute that holds a URL in
// HTML is kept unchecked.
func TestPolicy(t *testing.T) {
	for name := range elements {
		if htmlElements[name] || dropped[name] {
			t.Errorf("<%s> is kept and removed", name)
		}
	}
	for name := range htmlElements {
		if dropped[name] {
			t.Errorf("<%s> is kept by the HTML kind and always removed", name)
		}
	}
	if elements["h1"] {
		t.Error("<h1> is kept, not changed to <h2>")
	}
	for el, attrs := range elementAttrs {
		if !elements[el] && !htmlElements[el] {
			t.Errorf("<%s> has attributes but is not kept", el)
		}
		for _, a := range attrs {
			if globalAttrs[a] {
				t.Errorf("%s of <%s> is global", a, el)
			}
		}
	}
	urlNames := []string{
		"action", "archive", "background", "cite", "classid", "codebase", "data", "dynsrc", "formaction", "href",
		"icon", "itemid", "itemtype", "longdesc", "lowsrc", "manifest", "ping", "poster", "profile", "src", "srcset",
		"usemap", "xmlns",
	}
	for name := range globalAttrs {
		if slices.Contains(urlNames, name) || strings.HasPrefix(name, "on") || name == "style" {
			t.Errorf("global attribute %s", name)
		}
	}
	for el, attrs := range elementAttrs {
		for _, a := range attrs {
			if strings.HasPrefix(a, "on") || slices.Contains([]string{"style", "srcdoc", "srcset", "formaction"}, a) {
				t.Errorf("%s of <%s> is kept", a, el)
			}
			if slices.Contains(urlNames, a) && urlAttrs[[2]string{el, a}] == noURL {
				t.Errorf("%s of <%s> holds a URL, which is not checked", a, el)
			}
		}
	}
	for key, kind := range urlAttrs {
		if !slices.Contains(elementAttrs[key[0]], key[1]) {
			t.Errorf("%s of <%s> is checked as a URL but not kept", key[1], key[0])
		}
		if len(schemes[kind]) == 0 {
			t.Errorf("%s of <%s> allows no URLs", key[1], key[0])
		}
	}
	for name := range needSrc {
		if urlAttrs[[2]string{name, "src"}] == noURL {
			t.Errorf("<%s> needs a src that is not checked", name)
		}
	}
}
