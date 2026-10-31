package html

import "testing"

// TestParseFragment parses fragments for the contexts that the WPT tests do not have: a div element (the context of the
// sanitizer), noscript, contexts with attributes or ancestors. fragmentDump checks that the nodes have no parent.
func TestParseFragment(t *testing.T) {
	element := func(ns Namespace, name string, attrs ...Attr) *Node {
		return &Node{Type: ElementNode, Namespace: ns, Name: name, Attrs: attrs}
	}
	div := element(HTML, "div")
	form := element(HTML, "form")
	inForm := element(HTML, "div")
	section := element(HTML, "section")
	form.AppendChild(section)
	section.AppendChild(inForm)
	templateInForm := element(HTML, "template")
	form.AppendChild(templateInForm)
	tests := []struct {
		name      string
		context   *Node
		in        string
		scripting bool
		want      string
	}{
		{"table", div, "<table><tr><td>a</td></tr></table>b", true, `| <table>
|   <tbody>
|     <tr>
|       <td>
|         "a"
| "b"`},
		{"cell outside a table", div, "<td>a</td>", true, `| "a"`},
		{"template", div, "<template><td>a</td></template>b", true, `| <template>
|   content
|     <td>
|       "a"
| "b"`},
		{"svg broken out of", div, "<svg><p>a</svg>b", true, `| <svg svg>
| <p>
|   "ab"`},
		{"CDATA in svg", div, "<svg><![CDATA[a<b>&amp;\x00]]>c</svg>", true, `| <svg svg>
|   "a<b>&amp;` + replacement + `c"`},
		{"CDATA in HTML", div, "<![CDATA[a]]>b", true, `| <!-- [CDATA[a]] -->
| "b"`},
		{"processing instruction in svg", div, "<svg><?pi data?></svg>", true, `| <svg svg>
|   <?pi data?>`},
		{"noscript with scripting", div, "<noscript><p>a</p></noscript>", true, `| <noscript>
|   "<p>a</p>"`},
		{"noscript without scripting", div, "<noscript><p>a</p></noscript>", false, `| <noscript>
|   <p>
|     "a"`},
		{"noscript context with scripting", element(HTML, "noscript"), "<p>a</p>", true, `| "<p>a</p>"`},
		{"noscript context without scripting", element(HTML, "noscript"), "<p>a</p>", false, `| <p>
|   "a"`},
		{"SVG context", element(SVG, "g"), "<rect/><clippath>a</clippath><![CDATA[b]]><p>c", true, `| <svg rect>
| <svg clipPath>
|   "a"
| "b"
| <p>
|   "c"`},
		{"annotation-xml context for HTML", element(MathML, "annotation-xml", Attr{Name: "encoding", Value: "Text/HTML"}),
			"<section>a</section>", true, `| <section>
|   "a"`},
		{"annotation-xml context", element(MathML, "annotation-xml"), "<section>a</section>", true, `| <math section>
|   "a"`},
		{"template context", element(HTML, "template"), "<td>a</td>", true, `| <td>
|   "a"`},
		{"form context", form, "<form>a", true, `| "a"`},
		{"context in a form", inForm, "<form><input></form><form>", true, `| <input>
| <form>`},
		{"template context in a form", templateInForm, "<form>", true, `| <form>`},
		{"no input", div, "", true, ""},
	}
	for _, tt := range tests {
		got, err := fragmentDump(t, tt.in, tt.context, tt.scripting)
		if err != nil {
			t.Errorf("%s: %+q: %v", tt.name, tt.in, err)
		} else if got != tt.want {
			t.Errorf("%s: %+q:\ngot:\n%s\nwant:\n%s", tt.name, tt.in, got, tt.want)
		}
	}
}

func TestParseFragmentContextError(t *testing.T) {
	tests := []struct {
		name    string
		context *Node
	}{
		{"nil", nil},
		{"a text node", &Node{Type: TextNode, Data: "div"}},
		{"a document fragment", &Node{Type: DocumentFragmentNode}},
		{"an element in no namespace", &Node{Type: ElementNode, Namespace: MathML + 1, Name: "div"}},
	}
	for _, tt := range tests {
		if nodes, err := ParseFragment("a", tt.context, Options{}); err == nil || nodes != nil {
			t.Errorf("%s: nodes %v, error %v; want no nodes and an error", tt.name, nodes, err)
		}
	}
}
