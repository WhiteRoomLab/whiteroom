package html

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	nbsp := string(rune(0xA0))
	nul := string(rune(0))
	tests := []struct {
		name, in, want string
	}{
		{"text", `a&amp;b&lt;c&gt;d"e'f&nbsp;g`, `a&amp;b&lt;c&gt;d"e'f` + nbsp + "g"},
		{"attribute values", `<p title='a&amp;b&lt;c&gt;d"e&#39;f&nbsp;' class=x id="">x</p>`,
			`<p title="a&amp;b&lt;c&gt;d&#34;e&#39;f` + nbsp + `" class="x" id="">x</p>`},
		{"attributes in order", `<a z=1 b=2 m=3></a>`, `<a z="1" b="2" m="3"></a>`},
		{"raw text", "<style>a&b<c></style><script>a&b<c></script><xmp>a&b<c></xmp><iframe>a&b<c></iframe>" +
			"<noembed>a&b<c></noembed><noframes>a&b<c></noframes><noscript>a&b<c></noscript>",
			"<style>a&b<c></style><script>a&b<c></script><xmp>a&b<c></xmp><iframe>a&b<c></iframe>" +
				"<noembed>a&b<c></noembed><noframes>a&b<c></noframes><noscript>a&b<c></noscript>"},
		{"script ended in a double escape", "<p><script><!--<script a", "<p><script><!--<script a"},
		{"script ended in an escape", "<p><script><!--<script></script>a", "<p><script><!--<script></script>a</script></p>"},
		{"RCDATA", "<textarea>a&amp;b&lt;c</textarea><title>a&amp;b&lt;c</title>",
			"<textarea>a&amp;b&lt;c</textarea><title>a&amp;b&lt;c</title>"},
		{"text of SVG style", "<svg><style>a&amp;b&lt;c</style><script>d&gt;</script></svg>",
			"<svg><style>a&amp;b&lt;c</style><script>d&gt;</script></svg>"},
		{"void elements", "<br><img src=x><input type=text><hr><wbr><area><embed><source><track><base><link><meta>" +
			"<basefont><bgsound><keygen><param>",
			`<br><img src="x"><input type="text"><hr><wbr><area><embed><source><track><base><link><meta>` +
				"<basefont><bgsound><keygen><param>"},
		{"elements named as void in SVG", "<svg><input></input><area/><source>a</source></svg>",
			"<svg><input></input><area></area><source>a</source></svg>"},
		{"template", "<template><td>a</td></template><template><p>b<template>c</template></template>",
			"<template><td>a</td></template><template><p>b<template>c</template></p></template>"},
		{"foreign elements and attributes",
			`<svg viewbox="0 0 1 1" xlink:href="#a" xml:lang="en" xmlns="http://www.w3.org/2000/svg" ` +
				`xmlns:xlink="http://www.w3.org/1999/xlink" xlink:other=1><clippath><path d="M0"/></clippath>` +
				`<foreignobject><p>x</p></foreignobject></svg><math definitionurl="u"><mi>x</mi><annotation-xml ` +
				`encoding="text/html"><div>y</div></annotation-xml></math>`,
			`<svg viewBox="0 0 1 1" xlink:href="#a" xml:lang="en" xmlns="http://www.w3.org/2000/svg" ` +
				`xmlns:xlink="http://www.w3.org/1999/xlink" xlink:other="1"><clipPath><path d="M0"></path></clipPath>` +
				`<foreignObject><p>x</p></foreignObject></svg><math definitionURL="u"><mi>x</mi><annotation-xml ` +
				`encoding="text/html"><div>y</div></annotation-xml></math>`},
		{"comments and processing instructions", "<!--a-->b<!---->c<?pi data?><?pi?><svg><?x y?></svg>",
			"<!--a-->b<!---->c<?pi data?><?pi ?><svg><?x y?></svg>"},
		{"U+0000", "<?pi a" + nul + "b?><svg>c" + nul + "d</svg>", "<?pi a" + replacement + "b?><svg>c" + replacement +
			"d</svg>"},
		{"line feed after pre, textarea and listing",
			"<pre>\n\na</pre><textarea>\n\nb</textarea><listing>\n\nc</listing>",
			"<pre>\n\na</pre><textarea>\n\nb</textarea><listing>\n\nc</listing>"},
		{"line feed dropped", "<pre>\na</pre><textarea>\nb</textarea>", "<pre>a</pre><textarea>b</textarea>"},
		{"line feed after other elements", "<div>\na</div><pre><b>\nc</b></pre><svg><textarea>\nd</textarea></svg>",
			"<div>\na</div><pre><b>\nc</b></pre><svg><textarea>\nd</textarea></svg>"},
		{"plaintext", "<section><plaintext>a&amp;<b>c</section>d", "<section><plaintext>a&amp;<b>c</section>d"},
		{"element in plaintext", "<p><b>a</p><plaintext>b</b>c", "<p><b>a</b></p><plaintext><b>b&lt;/b&gt;c</b>"},
	}
	div := &Node{Type: ElementNode, Namespace: HTML, Name: "div"}
	for _, tt := range tests {
		nodes, err := ParseFragment(tt.in, div, Options{Scripting: true})
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if got := Render(nodes...); got != tt.want {
			t.Errorf("%s: Render of %+q\n got %+q\nwant %+q", tt.name, tt.in, got, tt.want)
		}
	}
}

func TestRenderDocument(t *testing.T) {
	tests := []struct{ in, want string }{
		{"<!DOCTYPE html><title>a</title><p>b",
			"<!DOCTYPE html><html><head><title>a</title></head><body><p>b</p></body></html>"},
		{`<!DOCTYPE html PUBLIC "-//W3C//DTD HTML 4.01//EN" "http://www.w3.org/TR/html4/strict.dtd"><!--c-->`,
			"<!DOCTYPE html><!--c--><html><head></head><body></body></html>"},
		{"<!doctype><html lang=en><frameset><frame></frameset> <!--a-->",
			`<!DOCTYPE ><html lang="en"><head></head><frameset><frame></frameset> <!--a--></html>`},
	}
	for _, tt := range tests {
		doc, err := Parse(tt.in, Options{Scripting: true})
		if err != nil {
			t.Fatal(err)
		}
		if got := Render(doc); got != tt.want {
			t.Errorf("Render of %+q\n got %+q\nwant %+q", tt.in, got, tt.want)
		}
	}
}

// TestRenderTrees renders trees that the parser does not make.
func TestRenderTrees(t *testing.T) {
	nul := string(rune(0))
	text := func(s string) *Node { return &Node{Type: TextNode, Data: s} }
	element := func(ns Namespace, name string, attrs []Attr, children ...*Node) *Node {
		n := &Node{Type: ElementNode, Namespace: ns, Name: name, Attrs: attrs}
		for _, c := range children {
			n.AppendChild(c)
		}
		return n
	}
	html := func(name string, children ...*Node) *Node { return element(HTML, name, nil, children...) }
	plaintext := html("plaintext", text("a"))
	tests := []struct {
		name  string
		nodes []*Node
		want  string
	}{
		{"nodes", []*Node{text("a"), html("b", text("c")), text("d")}, "a<b>c</b>d"},
		{"document fragment", []*Node{{Type: DocumentFragmentNode}, element(HTML, "p", nil)}, "<p></p>"},
		{"U+0000", []*Node{
			element(HTML, "p"+nul, []Attr{{Name: "a" + nul, Value: "b" + nul}}, text("c"+nul)),
			html("script", text("d"+nul)),
			{Type: CommentNode, Data: "e" + nul},
			{Type: ProcessingInstructionNode, Name: "f", Data: "g" + nul},
		}, "<p" + replacement + " a" + replacement + `="b` + replacement + `">c` + replacement + "</p" + replacement +
			"><script>d" + replacement + "</script><!--e" + replacement + "--><?f g" + replacement + "?>"},
		{"children of void elements", []*Node{html("br", text("a")), html("col", text("b")), html("frame")},
			"<br><col><frame>"},
		{"template without contents", []*Node{html("template", text("a"))}, "<template>a</template>"},
		{"template contents, not children", []*Node{func() *Node {
			n := html("template", text("a"))
			n.Content = &Node{Type: DocumentFragmentNode}
			n.Content.AppendChild(text("b"))
			return n
		}()}, "<template>b</template>"},
		{"text of a template in raw text", []*Node{func() *Node {
			n := html("template")
			n.Content = &Node{Type: DocumentFragmentNode}
			n.Content.AppendChild(text("<a>"))
			return html("style", n)
		}()}, "<style><template>&lt;a&gt;</template></style>"},
		{"attribute namespaces", []*Node{element(SVG, "a", []Attr{
			{Namespace: "xmlns", Name: "xmlns", Value: "1"}, {Namespace: "xmlns", Name: "x", Value: "2"},
			{Namespace: "xml", Name: "space", Value: "3"}, {Namespace: "xlink", Name: "href", Value: "4"},
			{Namespace: "other", Name: "y", Value: "5"},
		})}, `<a xmlns="1" xmlns:x="2" xml:space="3" xlink:href="4" other:y="5"></a>`},
		{"text in raw text with a parent", []*Node{html("xmp", text("<&>")).FirstChild}, "<&>"},
		{"after plaintext", []*Node{html("div", plaintext, html("p")), text("b")}, "<div><plaintext>a"},
		{"after a script ended in a double escape", []*Node{html("div", html("script", text("<!--<script a")), html("p")),
			text("b")}, "<div><script><!--<script a"},
		{"in plaintext", []*Node{html("plaintext", html("b", text("<")), text("&"))}, "<plaintext><b>&lt;</b>&"},
		{"doctype", []*Node{{Type: DoctypeNode, Name: "html", PublicID: "a", SystemID: "b"}}, "<!DOCTYPE html>"},
	}
	for _, tt := range tests {
		if got := Render(tt.nodes...); got != tt.want {
			t.Errorf("%s: got %+q, want %+q", tt.name, got, tt.want)
		}
	}
}

// renderParsed parses s with scripting as a document, or as a fragment for context if it is not nil, and returns
// what Render writes of it and its tree: the document, or a copy of context with the nodes of the fragment as its
// children, whose Text children Render writes as they are if the context is a raw text element.
func renderParsed(t testing.TB, s string, context *Node) (string, *Node) {
	t.Helper()
	if context == nil {
		doc, err := Parse(s, Options{Scripting: true})
		if err != nil {
			t.Fatalf("%+q: %v", s, err)
		}
		return Render(doc), doc
	}
	nodes, err := ParseFragment(s, context, Options{Scripting: true})
	if err != nil {
		t.Fatalf("%+q in %s: %v", s, context.Name, err)
	}
	parent := &Node{
		Type: ElementNode, Namespace: context.Namespace, Name: context.Name, Attrs: slices.Clone(context.Attrs),
	}
	for _, n := range nodes {
		parent.AppendChild(n)
	}
	return Render(nodes...), parent
}

// walk calls f for each node of the tree of n, template contents too, in no order.
func walk(n *Node, f func(*Node)) {
	stack := []*Node{n}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		f(n)
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			stack = append(stack, c)
		}
		if n.Content != nil {
			stack = append(stack, n.Content)
		}
	}
}

// hasPlaintext reports whether the tree of n has an HTML plaintext element, after whose children no HTML can give a
// node again, and in which no HTML can give an element again.
func hasPlaintext(n *Node) bool {
	found := false
	walk(n, func(n *Node) { found = found || n.isHTML("plaintext") })
	return found
}

// normalize drops from the tree of n what Render does not write: the namespaces of elements, which the parser takes
// from where they are, but for those of attributes, which it writes in their names; the identifiers of doctypes; where
// one of adjacent Text nodes ends. It also replaces U+0000 in processing instructions with U+FFFD, as Render does.
func normalize(n *Node) {
	walk(n, func(n *Node) {
		switch n.Type {
		case DoctypeNode:
			n.PublicID, n.SystemID = "", ""
		case ProcessingInstructionNode:
			n.Data = strings.ReplaceAll(n.Data, "\x00", replacement)
		case ElementNode:
			n.Namespace = HTML
			for i, a := range n.Attrs {
				if a.Namespace != "" && (a.Namespace != "xmlns" || a.Name != "xmlns") {
					n.Attrs[i] = Attr{Name: a.Namespace + ":" + a.Name, Value: a.Value}
				} else {
					n.Attrs[i].Namespace = ""
				}
			}
		case DocumentNode, DocumentFragmentNode, TextNode, CommentNode:
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			for c.Type == TextNode && c.NextSibling != nil && c.NextSibling.Type == TextNode {
				c.Data += c.NextSibling.Data
				n.RemoveChild(c.NextSibling)
			}
		}
	})
}

// checkRoundTrip checks the HTML out that Render wrote of tree, the tree of s, and out2 and tree2, those of out: if
// out2 is out, then the trees must be the same, unless tree has a plaintext element.
func checkRoundTrip(t testing.TB, s, out, out2 string, tree, tree2 *Node, in string) {
	t.Helper()
	if out2 != out || hasPlaintext(tree) {
		return
	}
	normalize(tree)
	normalize(tree2)
	if d, d2 := dumpTree(t, tree), dumpTree(t, tree2); d != d2 {
		t.Errorf("%s: %+q and its HTML %+q give different trees:\n%s\nand\n%s", in, s, out, d, d2)
	}
}

// TestRenderRoundTrip renders the tree of each WPT test input, parsed with scripting, then parses and renders the HTML
// again until it does not change: within 3 times. The trees that do not give themselves again (13.3) change; a tree
// whose HTML gives the same HTML again must give itself again (see checkRoundTrip).
func TestRenderRoundTrip(t *testing.T) {
	const most = 3
	iterations := make([]int, most+1)
	for _, f := range readTestFiles(t, "wpt-tree-construction.json.gz") {
		for _, tt := range readTreeTests(t, f.Name, f.Text) {
			var context *Node
			name := fmt.Sprintf("%s:%d", f.Name, tt.line)
			if tt.context != "" {
				context = wptContext(tt.context)
				name += " in " + tt.context
			}
			out, tree := renderParsed(t, tt.data, context)
			for i := 1; ; i++ {
				out2, tree2 := renderParsed(t, out, context)
				if i == 1 {
					checkRoundTrip(t, tt.data, out, out2, tree, tree2, name)
				}
				if out2 == out {
					iterations[i]++
					break
				}
				if i == most {
					t.Errorf("%s: %+q gives no fixed point in %d iterations: %+q, then %+q", name, tt.data, most, out,
						out2)
					break
				}
				out = out2
			}
		}
	}
	t.Logf("inputs by the iterations to a fixed point: %v", iterations[1:])
}

// TestRenderExamples renders the examples of trees that do not give themselves again in 13.3, as 13.3 says, and
// checks that their HTML gives the tree that 13.3 says once, and itself after that; and the example of the line feed
// in pre, which gives itself again.
func TestRenderExamples(t *testing.T) {
	tests := []struct{ in, first, second string }{
		{
			`<form id="outer"><div></form><form id="inner"><input>`,
			`<html><head></head><body><form id="outer"><div><form id="inner"><input></form></div></form></body></html>`,
			`<html><head></head><body><form id="outer"><div><input></div></form></body></html>`,
		},
		{
			"<a><table><a>",
			"<html><head></head><body><a><a></a><table></table></a></body></html>",
			"<html><head></head><body><a></a><a></a><table></table></body></html>",
		},
		{
			"<pre>\n\nHello.</pre>",
			"<html><head></head><body><pre>\n\nHello.</pre></body></html>",
			"<html><head></head><body><pre>\n\nHello.</pre></body></html>",
		},
	}
	for _, tt := range tests {
		first, _ := renderParsed(t, tt.in, nil)
		second, _ := renderParsed(t, first, nil)
		third, _ := renderParsed(t, second, nil)
		if first != tt.first || second != tt.second || third != second {
			t.Errorf("%+q renders as\n%+q, then\n%+q, then\n%+q; want\n%+q, then\n%+q twice", tt.in, first, second,
				third, tt.first, tt.second)
		}
	}
}
