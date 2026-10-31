package html

import (
	"flag"
	"fmt"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

var wptList = flag.Bool("wpt.list", false, "report each failing run of a WPT tree construction test as an error")

// wptFailures is the number of failing runs of the WPT document tests; phase 5 brings it to 0.
const wptFailures = 424

// wptSkips are the WPT tests that are not run, by file and line, with the reason; they have wptSkippedRuns runs.
var wptSkips = map[string]string{
	"webkit02.dat:692": noSelectedcontent,
	"webkit02.dat:706": noSelectedcontent,
	"webkit02.dat:732": noSelectedcontent,
	"webkit02.dat:748": noSelectedcontent,
}

const (
	wptSkippedRuns    = 8
	noSelectedcontent = "selectedcontent cloning is DOM behavior of option elements, outside tree construction"
)

// treeTest is a test of a .dat file of the WPT tree construction tests (see testdata_test.go and
// html/syntax/parsing/resources/README.md of web-platform-tests).
type treeTest struct {
	line      int    // the line of #data, or of #description before it
	data      string // the input
	context   string // the context element of a #document-fragment test, or ""
	scripting []bool // the scripting flags to run the test with
	document  string // the expected tree, as dumpTree writes it
}

// readTreeTests reads the tests of the .dat file text. The data can have CR and NUL characters: only LF separates
// lines.
func readTreeTests(t *testing.T, name, text string) []treeTest {
	t.Helper()
	var tests []treeTest
	var section string
	var lines []string
	end := func() {
		if len(tests) == 0 {
			if len(lines) > 0 {
				t.Fatalf("%s: text before the first test", name)
			}
			return
		}
		tt := &tests[len(tests)-1]
		s := strings.Join(lines, "\n")
		switch section {
		case "#data":
			tt.data = s
		case "#document-fragment":
			tt.context = s
		case "#document":
			tt.document = strings.TrimRight(s, "\n")
		}
		lines = lines[:0]
	}
	for i, l := range strings.Split(text, "\n") {
		switch l {
		case "#description", "#data", "#errors", "#new-errors", "#document-fragment", "#script-off", "#script-on",
			"#document":
		default:
			lines = append(lines, l)
			continue
		}
		end()
		if l == "#description" || l == "#data" && section != "#description" {
			tests = append(tests, treeTest{line: i + 1, scripting: []bool{false, true}})
		}
		switch l {
		case "#script-off":
			tests[len(tests)-1].scripting = []bool{false}
		case "#script-on":
			tests[len(tests)-1].scripting = []bool{true}
		}
		section = l
	}
	end()
	for _, tt := range tests {
		if tt.document == "" && tt.context == "" {
			t.Fatalf("%s:%d: a document test without a tree", name, tt.line)
		}
	}
	return tests
}

// dumpTree writes the children of n as the tests write trees: a line for each node, after "| " and two spaces for each
// ancestor below n; the attributes of an element on the lines after it, sorted by name; the template contents of a
// template element under a "content" line. It checks the links between the nodes.
func dumpTree(t *testing.T, n *Node) string {
	t.Helper()
	type item struct {
		n       *Node
		depth   int
		content bool // the "content" line of the template contents n
	}
	var lines []string
	var stack []item
	pushChildren := func(parent *Node, depth int) {
		var prev *Node
		var children []*Node
		for c := parent.FirstChild; c != nil; c = c.NextSibling {
			if c.Parent != parent || c.PrevSibling != prev {
				t.Errorf("a child of a %d node %q has a wrong Parent or PrevSibling", parent.Type, parent.Name)
			}
			children = append(children, c)
			prev = c
		}
		if parent.LastChild != prev {
			t.Errorf("LastChild of a %d node %q is not its last child", parent.Type, parent.Name)
		}
		for i := len(children) - 1; i >= 0; i-- {
			stack = append(stack, item{n: children[i], depth: depth})
		}
	}
	pushChildren(n, 0)
	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		indent := "| " + strings.Repeat("  ", it.depth)
		if it.content {
			lines = append(lines, indent+"content")
			pushChildren(it.n, it.depth+1)
			continue
		}
		switch c := it.n; c.Type {
		case ElementNode:
			prefix := map[Namespace]string{HTML: "", SVG: "svg ", MathML: "math "}[c.Namespace]
			lines = append(lines, indent+"<"+prefix+c.Name+">")
			attrs := slices.Clone(c.Attrs)
			name := func(a Attr) string {
				if a.Namespace != "" {
					return a.Namespace + " " + a.Name
				}
				return a.Name
			}
			slices.SortStableFunc(attrs, func(a, b Attr) int {
				return slices.Compare(utf16.Encode([]rune(name(a))), utf16.Encode([]rune(name(b))))
			})
			for _, a := range attrs {
				lines = append(lines, indent+"  "+name(a)+`="`+a.Value+`"`)
			}
			pushChildren(c, it.depth+1)
			if c.Content != nil {
				stack = append(stack, item{n: c.Content, depth: it.depth + 1, content: true})
			}
		case TextNode:
			lines = append(lines, indent+`"`+c.Data+`"`)
		case CommentNode:
			lines = append(lines, indent+"<!-- "+c.Data+" -->")
		case DoctypeNode:
			ids := ""
			if c.PublicID != "" || c.SystemID != "" {
				ids = fmt.Sprintf(` "%s" "%s"`, c.PublicID, c.SystemID)
			}
			lines = append(lines, indent+"<!DOCTYPE "+c.Name+ids+">")
		case ProcessingInstructionNode:
			lines = append(lines, indent+"<?"+c.Name+" "+c.Data+"?>")
		case DocumentNode, DocumentFragmentNode:
			t.Errorf("a node of type %d in a tree", c.Type)
		}
	}
	return strings.Join(lines, "\n")
}

// parseDump parses s as a document and dumps the tree; a panic is returned as an error.
func parseDump(t *testing.T, s string, scripting bool) (dump string, err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()
	doc, err := Parse(s, Options{Scripting: scripting})
	if err != nil {
		return "", err
	}
	return dumpTree(t, doc), nil
}

// TestWPTTreeConstruction runs the document tests of the WPT tree construction tests, each in the scripting modes it
// asks for, and checks the number of failing runs; -wpt.list reports them. Parse errors are not checked. The tests of
// scripted_*.dat need scripts and are not run; neither are those of wptSkips nor, until fragment parsing exists, the
// fragment tests.
func TestWPTTreeConstruction(t *testing.T) {
	failed, skipped := 0, 0
	skips := make(map[string]bool)
	for _, f := range readTestFiles(t, "wpt-tree-construction.json.gz") {
		if strings.HasPrefix(f.Name, "scripted_") {
			t.Logf("%s: not run; its tests need scripts", f.Name)
			continue
		}
		tests := readTreeTests(t, f.Name, f.Text)
		runs, fails, fragments := 0, 0, 0
		for _, tt := range tests {
			if tt.context != "" {
				fragments++
				continue
			}
			name := fmt.Sprintf("%s:%d", f.Name, tt.line)
			if reason, ok := wptSkips[name]; ok {
				t.Logf("%s: not run; %s", name, reason)
				skips[name] = true
				skipped += len(tt.scripting)
				continue
			}
			for _, scripting := range tt.scripting {
				runs++
				got, err := parseDump(t, tt.data, scripting)
				if err != nil {
					t.Errorf("%s:%d, scripting %t: input %+q: %v", f.Name, tt.line, scripting, tt.data, err)
				} else if got == tt.document {
					continue
				}
				fails++
				if *wptList {
					t.Errorf("%s:%d, scripting %t: input %+q\nwant:\n%s\ngot:\n%s", f.Name, tt.line, scripting, tt.data,
						tt.document, got)
				}
			}
		}
		t.Logf("%s: %d tests; %d runs of the document tests, %d failed; %d fragment tests not run", f.Name,
			len(tests), runs, fails, fragments)
		failed += fails
	}
	if failed != wptFailures {
		t.Errorf("%d runs failed, want %d", failed, wptFailures)
	}
	if skipped != wptSkippedRuns || len(skips) != len(wptSkips) {
		t.Errorf("%d runs of %d tests skipped, want %d runs of %d tests", skipped, len(skips), wptSkippedRuns,
			len(wptSkips))
	}
}

// TestParseTableModes parses markup that reaches rules of the table modes that no WPT document test reaches.
func TestParseTableModes(t *testing.T) {
	tests := []struct{ in, want string }{
		{
			// "in column group" with the template as the current node: other characters than whitespace are ignored.
			"<template><col>a b</template>",
			`| <html>
|   <head>
|     <template>
|       content
|         <col>
|         " "
|   <body>`,
		},
		{
			// "in table" in a template: no table in table scope, so the table start tag is ignored.
			"<template><tbody></tbody><table>x</template>",
			`| <html>
|   <head>
|     <template>
|       content
|         <tbody>
|         "x"
|   <body>`,
		},
		{
			"<table><colgroup> <col> <?pi x></colgroup></table>",
			`| <html>
|   <head>
|   <body>
|     <table>
|       <colgroup>
|         " "
|         <col>
|         " "
|         <?pi x?>`,
		},
	}
	for _, tt := range tests {
		got, err := parseDump(t, tt.in, true)
		if err != nil {
			t.Errorf("%q: %v", tt.in, err)
		} else if got != tt.want {
			t.Errorf("%q:\ngot:\n%s\nwant:\n%s", tt.in, got, tt.want)
		}
	}
}

func TestDoctypeMode(t *testing.T) {
	const (
		strict401 = `"-//W3C//DTD HTML 4.01//EN"`
		loose401  = `"-//W3C//DTD HTML 4.01 Transitional//EN"`
		looseDTD  = `"http://www.w3.org/TR/html4/loose.dtd"`
	)
	tests := []struct {
		doctype string
		want    documentMode
	}{
		{"<!DOCTYPE html>", noQuirks},
		{"<!doctype HTML>", noQuirks},
		{`<!DOCTYPE html SYSTEM "about:legacy-compat">`, noQuirks},
		{"<!DOCTYPE html PUBLIC " + strict401 + ">", noQuirks},
		{"<!DOCTYPE html PUBLIC " + strict401 + ` "http://www.w3.org/TR/html4/strict.dtd">`, noQuirks},
		{"<!DOCTYPE>", quirks},
		{"<!DOCTYPE htm>", quirks},
		{"<!DOCTYPE html PUBLIC>", quirks},
		{`<!DOCTYPE html PUBLIC "HTML">`, quirks},
		{`<!DOCTYPE html PUBLIC "html">`, quirks},
		{`<!DOCTYPE html PUBLIC "-//W3O//DTD W3 HTML Strict 3.0//EN//">`, quirks},
		{`<!DOCTYPE html PUBLIC "-//W3O//DTD W3 HTML Strict 3.0//EN//x">`, noQuirks},
		{`<!DOCTYPE html PUBLIC "-//IETF//DTD HTML//EN">`, quirks},
		{`<!DOCTYPE html PUBLIC "-//ietf//dtd html//en">`, quirks},
		{`<!DOCTYPE html PUBLIC "-//WebTechs//DTD Mozilla HTML//EN">`, quirks},
		{`<!DOCTYPE html PUBLIC "+//Silmaril//dtd html Pro v0r11 19970101//EN">`, quirks},
		{`<!DOCTYPE html SYSTEM "http://www.ibm.com/data/dtd/v11/ibmxhtml1-transitional.dtd">`, quirks},
		{`<!DOCTYPE html SYSTEM "HTTP://WWW.IBM.COM/data/dtd/v11/ibmxhtml1-transitional.dtd">`, quirks},
		{"<!DOCTYPE html PUBLIC " + loose401 + ">", quirks},
		{"<!DOCTYPE html PUBLIC " + loose401 + ` "">`, quirks},
		{"<!DOCTYPE html PUBLIC " + loose401 + " " + looseDTD + ">", limitedQuirks},
		{`<!DOCTYPE html PUBLIC "-//W3C//DTD HTML 4.01 Frameset//EN" ` + looseDTD + ">", limitedQuirks},
		{`<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Transitional//EN">`, limitedQuirks},
		{`<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Frameset//EN" "">`, limitedQuirks},
		{`<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.1//EN">`, noQuirks},
	}
	for _, tt := range tests {
		tok := newTokenizer(tt.doctype).next()
		if tok.typ != doctypeToken {
			t.Fatalf("%s: a token of type %d", tt.doctype, tok.typ)
		}
		if got := doctypeMode(&tok); got != tt.want {
			t.Errorf("%s: mode %d, want %d", tt.doctype, got, tt.want)
		}
	}
}

// TestParseTextLinear parses 2^18 characters, each a token of its own, that all go into one Text node: copying the data
// of the node at each append would take minutes.
func TestParseTextLinear(t *testing.T) {
	const n = 1 << 18
	start := time.Now()
	doc, err := Parse(strings.Repeat("a</x>", n), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("%v for %d characters", d, n)
	}
	body := doc.LastChild.LastChild
	if body.Name != "body" || body.FirstChild == nil || body.FirstChild != body.LastChild ||
		body.FirstChild.Data != strings.Repeat("a", n) {
		t.Errorf("the body is not one Text node of %d characters", n)
	}
}
