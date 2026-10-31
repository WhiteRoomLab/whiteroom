package html

import (
	"encoding/json/v2"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// testLimits returns opts with the limits for an input of n bytes that the tests check, and that Sanitize is to set.
func testLimits(opts Options, n int) Options {
	opts.MaxNodes, opts.MaxSteps = n+1024, 64*n+65536
	return opts
}

// fuzzContexts are the contexts of fragments in FuzzParse: a div element, the context of Sanitize, then those of the
// WPT tests, and noscript and an annotation-xml element that is an HTML integration point.
var fuzzContexts = func() []*Node {
	names := []string{
		"div", "body", "caption", "colgroup", "frameset", "head", "html", "math annotation-xml", "math malignmark",
		"math math", "math mfenced", "math mi", "math mn", "math mo", "math ms", "math mtext", "plaintext", "script",
		"select", "style", "svg desc", "svg foreignObject", "svg path", "svg svg", "svg title", "table", "tbody", "td",
		"template", "textarea", "tfoot", "thead", "title", "tr", "xmp", "noscript",
	}
	var contexts []*Node
	for _, s := range names {
		contexts = append(contexts, wptContext(s))
	}
	return append(contexts, &Node{Type: ElementNode, Namespace: MathML, Name: "annotation-xml",
		Attrs: []Attr{{Name: "encoding", Value: "text/html"}}})
}()

// fuzzContext returns the index in fuzzContexts of the context of a WPT test, or 0 for a document.
func fuzzContext(t testing.TB, context string) uint8 {
	if context == "" {
		return 0
	}
	c := wptContext(context)
	for i, n := range fuzzContexts {
		if n.Namespace == c.Namespace && n.Name == c.Name && len(n.Attrs) == 0 {
			return uint8(i)
		}
	}
	t.Fatalf("context %q is not in fuzzContexts", context)
	return 0
}

// tokenizerInputs returns the inputs of the html5lib tokenizer tests that a Go string can hold.
func tokenizerInputs(t testing.TB) []string {
	var inputs []string
	for _, f := range readTestFiles(t, "html5lib-tokenizer.json.gz") {
		var file struct {
			Tests []struct {
				Input         string `json:"input"`
				DoubleEscaped bool   `json:"doubleEscaped"`
			} `json:"tests"`
		}
		if err := json.Unmarshal([]byte(f.Text), &file); err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		for _, tt := range file.Tests {
			s, ok := tt.Input, true
			if tt.DoubleEscaped {
				s, ok = unescapeTest(s)
			}
			if ok {
				inputs = append(inputs, s)
			}
		}
	}
	return inputs
}

// addTreeTests adds the input of each WPT tree construction test to f, with the index of its context in fuzzContexts
// if withContext.
func addTreeTests(f *testing.F, withContext bool) {
	for _, file := range readTestFiles(f, "wpt-tree-construction.json.gz") {
		for _, tt := range readTreeTests(f, file.Name, file.Text) {
			if withContext {
				f.Add(tt.data, fuzzContext(f, tt.context))
			} else {
				f.Add(tt.data)
			}
		}
	}
}

// pathological are inputs whose parse takes many steps or makes many nodes, small enough for seeds.
var pathological = []string{
	strings.Repeat("<div>", 300) + strings.Repeat("</p>", 300),
	strings.Repeat("<font a=1><font a=2><font a=3><font a=4>", 30) + strings.Repeat("</p>x", 100),
	"<b " + strings.Repeat("a", 64) + "=1>" + strings.Repeat("<p>x</p>", 100),
	"<svg>" + strings.Repeat("<g>", 300) + strings.Repeat("</x>", 300),
	strings.Repeat("<table><tr><td>", 100) + strings.Repeat("<form>", 100),
	"<b>" + strings.Repeat("<span>", 100) + "<div>" + strings.Repeat("<div>", 100) + "</b>",
	"<html a b c><html a d e><body f><body f g>",
	strings.Repeat("<template>", 100) + strings.Repeat("<a><table><a>", 30),
	strings.Repeat("<math><mi><svg><foreignObject><p>", 50) + strings.Repeat("</x></p>", 50),
}

// FuzzParse checks that Parse, and ParseFragment for a div element and for the context in fuzzContexts that c picks,
// each with scripting enabled and disabled and with the limits of testLimits, never panic and return either ErrLimit or
// a tree whose links are right (as dumpTree checks them), the nodes of a fragment without a parent; that the limits
// counted every node and attribute of the tree; and that with the limits the tree of a fragment for a div element is
// the one without them. The fuzzing engine's timeout catches a parse that takes long despite the limits.
func FuzzParse(f *testing.F) {
	addTreeTests(f, true)
	for _, s := range tokenizerInputs(f) {
		f.Add(s, uint8(0))
	}
	for i, s := range pathological {
		f.Add(s, uint8(i))
	}
	div := fuzzContexts[0]
	f.Fuzz(func(t *testing.T, s string, c uint8) {
		context := fuzzContexts[int(c)%len(fuzzContexts)]
		for _, scripting := range []bool{false, true} {
			opts := testLimits(Options{Scripting: scripting}, len(s))
			for _, context := range []*Node{nil, div, context} {
				p, err := checkParse(t, s, context, opts)
				if context != div || !scripting || err != nil {
					continue
				}
				unlimited, err := checkParse(t, s, context, Options{Scripting: true})
				if err != nil {
					t.Fatalf("%+q without limits: %v", s, err)
				}
				if got, want := dumpTree(t, p.redirectTo), dumpTree(t, unlimited.redirectTo); got != want {
					t.Errorf("%+q with limits:\n%s\nwithout:\n%s", s, got, want)
				}
			}
		}
	})
}

// checkParse parses s as a document, or as a fragment for context if it is not nil, and checks the result as FuzzParse
// says.
func checkParse(t *testing.T, s string, context *Node, opts Options) (*parser, error) {
	t.Helper()
	name := "a document"
	if context != nil {
		name = "a fragment for " + context.Name
	}
	if context == nil {
		doc, err := Parse(s, opts)
		if (err == nil) == (doc == nil) || err != nil && !errors.Is(err, ErrLimit) {
			t.Fatalf("%+q as %s, %+v: a tree %t and the error %v", s, name, opts, doc != nil, err)
		}
		if err == nil {
			dumpTree(t, doc)
		}
	} else {
		nodes, err := ParseFragment(s, context, opts)
		if err != nil && (!errors.Is(err, ErrLimit) || nodes != nil) {
			t.Fatalf("%+q as %s, %+v: %d nodes and the error %v", s, name, opts, len(nodes), err)
		}
		if err == nil {
			if _, err := dumpNodes(t, nodes); err != nil {
				t.Fatalf("%+q as %s, %+v: %v", s, name, opts, err)
			}
		}
	}
	p, err := parse(s, context, opts)
	if err != nil {
		return p, err
	}
	counted := 0
	walk(p.doc, func(n *Node) { counted += 1 + len(n.Attrs) })
	if context != nil {
		walk(p.redirectTo, func(n *Node) { counted += 1 + len(n.Attrs) })
	}
	if counted > p.nodes {
		t.Errorf("%+q as %s: %d nodes and attributes in the tree, but %d counted", s, name, counted, p.nodes)
	}
	return p, nil
}

// FuzzRoundTrip checks that rendering the nodes of a fragment for a div element, parsed with scripting (as Sanitize
// parses), gives valid UTF-8 without U+0000, and that parsing and rendering that again reaches HTML that gives itself
// within 4 times; and, as checkRoundTrip says, that if the first HTML gives itself, the tree that it gives is the one
// that it was written from.
func FuzzRoundTrip(f *testing.F) {
	addTreeTests(f, false)
	for _, s := range []string{
		"<p>a<plaintext>b</p><table>c",
		"<script><!--<script></script>x",
		"<pre>\n\na</pre><textarea>\n\nb</textarea><listing>\nc</listing>",
		"<?a b\x00c?>\x00<svg>\x00</svg>",
		`<form id="outer"><div></form><form id="inner"><input>`,
		"<a><table><a>",
		`<svg></p><style><a id="</style><img src=1 onerror=alert(1)>">`,
		"<math><mtext><table><mglyph><style><img src=x onerror=alert(1)>",
		"<mAth><mteXt><tABle><mglYph>",
		`<noscript><p title="</noscript><img src=x onerror=alert(1)>">`,
		"<template><style></template><img src=x onerror=alert(1)></style></template>",
		"<select><option><svg><style><img src=x onerror=alert(1)></style>",
		"<a href=\"javas&#99;ript:x\" title='a\"b'>&lt;&amp;&gt;&#0;</a>",
	} {
		f.Add(s)
	}
	for _, s := range pathological {
		f.Add(s)
	}
	div := fuzzContexts[0]
	f.Fuzz(func(t *testing.T, s string) {
		out, tree := renderParsed(t, s, div)
		if !utf8.ValidString(out) || strings.IndexByte(out, 0) >= 0 {
			t.Errorf("%+q renders as %+q, which is not valid UTF-8 or has U+0000", s, out)
		}
		const most = 4
		for i := 1; ; i++ {
			out2, tree2 := renderParsed(t, out, div)
			if i == 1 {
				checkRoundTrip(t, s, out, out2, tree, tree2, "a fragment for div")
			}
			if out2 == out {
				return
			}
			if i == most {
				t.Fatalf("%+q gives no fixed point in %d iterations: %+q, then %+q", s, most, out, out2)
			}
			out = out2
		}
	})
}
