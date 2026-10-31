package html

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// timeBound is the most time that the parses of the tests that take time linear in their input take: generous, for
// slow machines and the race detector, but far below what parses quadratic in the input would take.
const timeBound = 10 * time.Second

// counted parses s as a document, or as a fragment for context if it is not nil, and returns the nodes and the steps
// that the parse counted.
func counted(s string, context *Node, opts Options) (nodes, steps int, err error) {
	p, err := parse(s, context, opts)
	return p.nodes, p.steps, err
}

// numbered returns format written with 0, 1, ..., n-1.
func numbered(format string, n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, format, i)
	}
	return b.String()
}

// blogPost returns a long blog post of at least size bytes, as WordPress writes posts: block comments, headings,
// paragraphs with inline marks, links and character references, lists, tables, quotes, code, images and embeds, with
// some of the sloppy markup of older posts (unclosed p and li elements, misnested formatting elements, font).
func blogPost(size int) string {
	var b strings.Builder
	for i := 0; b.Len() < size; i++ {
		fmt.Fprintf(&b, `<!-- wp:heading -->
<h2 class="wp-block-heading" id="section-%d">Section %d: notes on parsing &amp; rendering</h2>
<!-- /wp:heading -->

<!-- wp:paragraph -->
<p>Lorem ipsum <strong>dolor</strong> sit amet, <a href="https://example.com/posts/%d/?ref=blog&amp;utm_source=feed">consectetur adipiscing</a> elit; <em>sed do</em> eiusmod <code>go test ./...</code> tempor&nbsp;incididunt ut labore &lt;et&gt; dolore magna aliqua. 日本語の文も入ります。<br>
Ut enim ad <mark class="has-inline-color">minim</mark> veniam, quis <a href="/about/#team" target="_blank" rel="noopener">nostrud</a> exercitation&hellip;</p>
<!-- /wp:paragraph -->

<!-- wp:list -->
<ul class="wp-block-list"><li>First item with <b>bold</b> text</li><li>Second item<ul><li>Nested <i>item</i></li><li>Another</li></ul></li><li>Third item &mdash; with a <a href="https://example.org/a?b=c&amp;d=e">link</a></li></ul>
<!-- /wp:list -->

<!-- wp:table -->
<figure class="wp-block-table"><table class="has-fixed-layout"><thead><tr><th>Name</th><th>Value</th><th>Notes</th></tr></thead><tbody><tr><td>alpha</td><td>%d</td><td><em>first</em></td></tr><tr><td>beta</td><td>2.5</td><td>second &amp; third</td></tr><tr><td>gamma</td><td>-1</td><td><code>x &lt; y</code></td></tr></tbody></table><figcaption class="wp-element-caption">Table %d</figcaption></figure>
<!-- /wp:table -->

<!-- wp:image {"id":%d,"sizeSlug":"large"} -->
<figure class="wp-block-image size-large"><img decoding="async" width="1024" height="768" src="https://example.com/wp-content/uploads/2026/10/photo-%d-1024x768.jpg" alt="A photo" class="wp-image-%d" srcset="https://example.com/wp-content/uploads/2026/10/photo-%d-1024x768.jpg 1024w, https://example.com/wp-content/uploads/2026/10/photo-%d-300x225.jpg 300w" sizes="(max-width: 1024px) 100vw, 1024px"><figcaption>Caption with <a href="https://example.com/">a link</a></figcaption></figure>
<!-- /wp:image -->

<!-- wp:quote -->
<blockquote class="wp-block-quote"><p>A quote, <q>nested</q>, with <abbr title="HyperText Markup Language">HTML</abbr>.</p><cite>Someone</cite></blockquote>
<!-- /wp:quote -->

<!-- wp:code -->
<pre class="wp-block-code"><code>func main() {
	fmt.Println("&lt;hello&gt;", %d)
}
</code></pre>
<!-- /wp:code -->

<!-- wp:html -->
<div class="embed"><iframe src="https://www.youtube.com/embed/abc%d" width="560" height="315" allowfullscreen></iframe></div>
<details><summary>More</summary><p>Hidden <kbd>Ctrl</kbd>+<kbd>C</kbd>, H<sub>2</sub>O, x<sup>2</sup>, <del>old</del> <ins>new</ins>.</p><dl><dt>Term</dt><dd>Definition</dd></dl></details>
<!-- /wp:html -->

<!-- wp:freeform -->
<p align="center"><font color="#333333" face="Verdana, Arial">An older paragraph<br />
with <b>bold <i>and italic</b> text</i> and an unclosed paragraph
<p>Another one, <span style="color: red">red</span>, &copy; 2026
<ul><li>one<li>two<li>three</ul>
<p><b>Bold that runs</p><p>into the next paragraph</b></p>
<ul><li><a href="https://example.com/2009/05/an-old-post-whose-link-is-not-closed/">An unclosed link<li>in a list<li>runs through its items</ul>
<hr>
<!-- /wp:freeform -->

`, i, i, i, i, i, i, i, i, i, i, i, i)
	}
	return b.String()
}

// TestLimitsRealistic parses a long blog post with the limits of testLimits, as a document and as a fragment for a div
// element: it must stay well inside them, and give the tree that it gives without them.
func TestLimitsRealistic(t *testing.T) {
	s := blogPost(1 << 20)
	for _, context := range []*Node{nil, fuzzContexts[0]} {
		opts := testLimits(Options{Scripting: true}, len(s))
		p, err := parse(s, context, opts)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%d bytes: %d nodes (%.3f per byte), %d steps (%.3f per byte)", len(s), p.nodes,
			float64(p.nodes)/float64(len(s)), p.steps, float64(p.steps)/float64(len(s)))
		if 4*p.nodes > opts.MaxNodes || 8*p.steps > opts.MaxSteps {
			t.Errorf("%d nodes and %d steps, not well inside the limits of %d and %d", p.nodes, p.steps, opts.MaxNodes,
				opts.MaxSteps)
		}
		unlimited, err := parse(s, context, Options{Scripting: true})
		if err != nil {
			t.Fatal(err)
		}
		if treeOf(t, p, context) != treeOf(t, unlimited, context) {
			t.Error("the tree with the limits is not the one without them")
		}
	}
}

// TestLimitsWPT parses each WPT test input with the limits of testLimits: it must stay well inside them, and give the
// tree that it gives without them. It reports the most steps and nodes per byte.
func TestLimitsWPT(t *testing.T) {
	var most struct {
		steps, nodes                float64 // per byte, of the inputs of 32 bytes or more
		stepsAt, nodesAt            string
		allSteps, allNodes, longest int
	}
	for _, f := range readTestFiles(t, "wpt-tree-construction.json.gz") {
		for _, tt := range readTreeTests(t, f.Name, f.Text) {
			var context *Node
			if tt.context != "" {
				context = wptContext(tt.context)
			}
			name := fmt.Sprintf("%s:%d", f.Name, tt.line)
			for _, scripting := range []bool{false, true} {
				opts := testLimits(Options{Scripting: scripting}, len(tt.data))
				p, err := parse(tt.data, context, opts)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if 4*p.nodes > opts.MaxNodes || 8*p.steps > opts.MaxSteps {
					t.Errorf("%s: %d nodes and %d steps, not well inside the limits of %d and %d", name, p.nodes,
						p.steps, opts.MaxNodes, opts.MaxSteps)
				}
				unlimited, err := parse(tt.data, context, Options{Scripting: scripting})
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if got, want := treeOf(t, p, context), treeOf(t, unlimited, context); got != want {
					t.Errorf("%s with limits:\n%s\nwithout:\n%s", name, got, want)
				}
				most.allSteps, most.allNodes = max(most.allSteps, p.steps), max(most.allNodes, p.nodes)
				most.longest = max(most.longest, len(tt.data))
				if n := float64(len(tt.data)); n >= 32 {
					if s := float64(p.steps) / n; s > most.steps {
						most.steps, most.stepsAt = s, name
					}
					if s := float64(p.nodes) / n; s > most.nodes {
						most.nodes, most.nodesAt = s, name
					}
				}
			}
		}
	}
	t.Logf("most steps %d, nodes %d (inputs of up to %d bytes); of 32 bytes or more, most steps per byte %.2f (%s), "+
		"nodes per byte %.2f (%s)", most.allSteps, most.allNodes, most.longest, most.steps, most.stepsAt, most.nodes,
		most.nodesAt)
}

// TestLimitCounts checks that a parse stops at the first node or step past a limit, as a document and as a fragment,
// and that limits that are not positive do not stop it.
func TestLimitCounts(t *testing.T) {
	const s = "<p><b>a</p>b<!--c--><table><tr><td>d</table>"
	for _, context := range []*Node{nil, fuzzContexts[0]} {
		nodes, steps, err := counted(s, context, Options{})
		if err != nil {
			t.Fatal(err)
		}
		for _, tt := range []struct {
			opts Options
			want error
		}{
			{Options{MaxNodes: nodes, MaxSteps: steps}, nil},
			{Options{MaxNodes: nodes - 1}, ErrLimit},
			{Options{MaxSteps: steps - 1}, ErrLimit},
			{Options{MaxNodes: -1, MaxSteps: -1}, nil},
		} {
			var tree bool
			name := "a document"
			if context == nil {
				doc, err2 := Parse(s, tt.opts)
				tree, err = doc != nil, err2
			} else {
				name = "a fragment"
				fragment, err2 := ParseFragment(s, context, tt.opts)
				tree, err = fragment != nil, err2
			}
			if !errors.Is(err, tt.want) || tree != (tt.want == nil) {
				t.Errorf("%s with %+v, which takes %d nodes and %d steps: a tree %t and the error %v, want %v", name,
					tt.opts, nodes, steps, tree, err, tt.want)
			}
		}
	}
}

// TestLimitCopies checks the nodes that an element copied from a formatting element counts: one, one for each
// attribute, and one for each 16 bytes of the names and values of the attributes.
func TestLimitCopies(t *testing.T) {
	value := strings.Repeat("v", 40)
	without, _, err := counted("<p><b>a</p>b", nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	with, _, err := counted(`<p><b c="`+value+`">a</p>b`, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// The attribute of the b element, and that of its copy with its bytes.
	if want := without + 2 + (len("c")+len(value))/16; with != want {
		t.Errorf("%d nodes, want %d", with, want)
	}
}

// TestLimitsPathological parses, as a document and as a fragment for a div element, inputs that take many steps or
// nodes for their length: with the limits of testLimits, each gives ErrLimit, or a tree if ok, in a time that the
// limits bound and that would grow faster than the input without them. The time bound is generous, for slow machines
// and the race detector. What Render writes of a tree within the limits is linear in them too.
func TestLimitsPathological(t *testing.T) {
	long := strings.Repeat("a", 63)
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"p end tags in nested elements", strings.Repeat("<div>", 10000) + strings.Repeat("</p>", 10000), false},
		{"formatting elements to reconstruct",
			"<p>" + numbered("<font a=%d>", 1000) + strings.Repeat("</p><p>x", 10000), false},
		{"text", strings.Repeat("a", 1<<20), true},
		{"attributes", "<a" + numbered(" a%d", 100000) + ">", true},
		{"forms in nested tables", strings.Repeat("<table><tr><td>", 5000) + strings.Repeat("<form>", 5000), false},
		{"end tags in nested svg", "<svg>" + strings.Repeat("<g>", 10000) + strings.Repeat("</x>", 10000), false},
		{"long end tags in svg",
			"<svg>" + strings.Repeat("<"+long+"1>", 1500) + strings.Repeat("</"+long+"2>", 1500), false},
		{"adoption agency", "<b>" + strings.Repeat("<span>", 10000) + "<div>" + strings.Repeat("<div>", 10000) + "</b>",
			false},
		{"attributes of html", "<html" + numbered(" a%d", 10000) + "><html" + numbered(" b%d", 10000) + ">", false},
		{"distinct formatting elements", numbered("<font a=%d>", 4000), false},
		// Render writes 5 bytes for each byte of the value of each copy.
		{"copies of a long attribute",
			`<p><b title='` + strings.Repeat(`"`, 4000) + `'>` + strings.Repeat("</p><p>x", 1000), false},
		{"copies of a long attribute within the limits",
			`<p><b title='` + strings.Repeat(`"`, 4000) + `'>` + strings.Repeat("</p><p>x", 18), true},
		{"nested templates", strings.Repeat("<template>", 100000), true},
	}
	for _, tt := range tests {
		for _, context := range []*Node{nil, fuzzContexts[0]} {
			name := tt.name + " as a document"
			if context != nil {
				name = tt.name + " as a fragment"
			}
			opts := testLimits(Options{Scripting: true}, len(tt.in))
			start := time.Now()
			p, err := parse(tt.in, context, opts)
			d := time.Since(start)
			t.Logf("%s: %d bytes, %d nodes, %d steps, %v: %v", name, len(tt.in), p.nodes, p.steps, d, err)
			if tt.ok != (err == nil) || err != nil && !errors.Is(err, ErrLimit) {
				t.Errorf("%s: the error %v, want ErrLimit %t", name, err, !tt.ok)
			}
			if d > timeBound {
				t.Errorf("%s: %v, want at most %v", name, d, timeBound)
			}
			if err != nil {
				continue
			}
			var out string
			if context == nil {
				out = Render(p.doc)
			} else {
				out = Render(p.redirectTo)
			}
			t.Logf("%s: Render writes %d bytes", name, len(out))
			if len(out) > 100*opts.MaxNodes {
				t.Errorf("%s: Render writes %d bytes for MaxNodes %d", name, len(out), opts.MaxNodes)
			}
		}
	}
}

// treeOf dumps the tree that p parsed: the document, or the fragment for context.
func treeOf(t testing.TB, p *parser, context *Node) string {
	t.Helper()
	if context != nil {
		return dumpTree(t, p.redirectTo)
	}
	return dumpTree(t, p.doc)
}
