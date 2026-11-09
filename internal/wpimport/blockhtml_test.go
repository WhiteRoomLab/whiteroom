package wpimport

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/html"
	"github.com/WhiteRoomLab/whiteroom/internal/wpblock"
)

// htmlOf returns the nodes of markup as the children of a div.
func htmlOf(t *testing.T, markup string) *html.Node {
	t.Helper()
	return richRoot(t, markup)
}

func TestRoot(t *testing.T) {
	const notAsWritten = "the HTML of the block is not as WordPress writes it: expected "
	for _, tt := range []struct {
		name  string
		doc   string
		names []string
		want  string // the name of the element
		err   string
	}{
		{name: "a paragraph", doc: "<!-- wp:paragraph -->\n<p class=\"x\">Hi</p>\n<!-- /wp:paragraph -->", names: []string{"p"}, want: "p"},
		{name: "any name", doc: "<!-- wp:acme/a --><section>Hi</section><!-- /wp:acme/a -->", want: "section"},
		{name: "one of the names", doc: "<!-- wp:heading --><h3>Hi</h3><!-- /wp:heading -->", names: []string{"h2", "h3", "h4"}, want: "h3"},
		{name: "comments and white space around it", doc: "<!-- wp:paragraph --> \n<!-- x --><p>Hi</p>\t\r\n<!-- y -->\f<!-- /wp:paragraph -->", names: []string{"p"}, want: "p"},
		{name: "the HTML of the inner blocks is not read", doc: "<!-- wp:group --><div class=\"wp-block-group\"><!-- wp:paragraph --><p>Hi</p><!-- /wp:paragraph --></div><!-- /wp:group -->",
			names: []string{"div"}, want: "div"},
		{name: "an element that is not one of the names", doc: "<!-- wp:paragraph --><div>Hi</div><!-- /wp:paragraph -->", names: []string{"p"}, err: notAsWritten + "<p>"},
		{name: "two names", doc: "<!-- wp:list --><div></div><!-- /wp:list -->", names: []string{"ul", "ol"}, err: notAsWritten + "<ul> or <ol>"},
		{name: "three names", doc: "<!-- wp:heading --><p></p><!-- /wp:heading -->", names: []string{"h2", "h3", "h4"}, err: notAsWritten + "<h2>, <h3> or <h4>"},
		{name: "no name", doc: "<!-- wp:acme/a --><p>a</p><p>b</p><!-- /wp:acme/a -->", err: notAsWritten + "one element"},
		{name: "no HTML", doc: "<!-- wp:paragraph /-->", names: []string{"p"}, err: notAsWritten + "<p>"},
		{name: "text around the element", doc: "<!-- wp:paragraph -->x<p>Hi</p><!-- /wp:paragraph -->", names: []string{"p"}, err: notAsWritten + "<p>"},
		{name: "two elements", doc: "<!-- wp:paragraph --><p>a</p><p>b</p><!-- /wp:paragraph -->", names: []string{"p"}, err: notAsWritten + "<p>"},
		{name: "text only", doc: "<!-- wp:paragraph -->Hi<!-- /wp:paragraph -->", names: []string{"p"}, err: notAsWritten + "<p>"},
		{name: "an element of another namespace", doc: "<!-- wp:acme/a --><svg><path/></svg><!-- /wp:acme/a -->", err: notAsWritten + "one element"},
		{name: "an element with an odd name", doc: "<!-- wp:acme/a --><div></div><!-- /wp:acme/a -->", names: []string{"my_tag"}, err: notAsWritten + `<"my_tag">`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := richConv(bodyImport(), "", nil)
			blocks, err := wpblock.Parse(tt.doc, wpblock.Options{})
			if err != nil || len(blocks) != 1 {
				t.Fatalf("Parse = %v, %v", blocks, err)
			}
			root, err := c.root(&blocks[0], tt.names...)
			if tt.err != "" {
				var le *legacyError
				if !errors.As(err, &le) || le.reason != tt.err {
					t.Errorf("root = %v, %v, want the reason %q", root, err, tt.err)
				}
				return
			}
			if err != nil || root.Name != tt.want {
				t.Errorf("root = %v, %v, want %s", root, err, tt.want)
			}
		})
	}
}

func TestParseHTMLLimits(t *testing.T) {
	const doc = "<!-- wp:acme/a --><p>Hello</p><!-- /wp:acme/a -->"
	blocks, err := wpblock.Parse(doc, wpblock.Options{})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("the bytes that the conversion reads", func(t *testing.T) {
		lowerLimit(t, &maxHTMLBytes, 20)
		c := richConv(bodyImport(), "", nil)
		if _, err := c.root(&blocks[0], "p"); err != nil {
			t.Fatalf("the first %d bytes: %v", len("<p>Hello</p>"), err)
		}
		var le *legacyError
		_, err := c.root(&blocks[0], "p")
		if !errors.As(err, &le) || le.reason != "the conversion has read more than 20 bytes of HTML" {
			t.Errorf("root = %v, want the reason about the bytes", err)
		}
		if !c.stopped {
			t.Error("the conversion did not stop")
		}
	})

	t.Run("a tree that is too big", func(t *testing.T) {
		deep := &wpblock.Block{Content: []wpblock.Piece{{HTML: strings.Repeat("<div>", 5000), Inner: -1}}}
		c := richConv(bodyImport(), "", nil)
		_, err := c.parseHTML(deep.HTML())
		var le *legacyError
		if !errors.As(err, &le) || le.reason != "the HTML of the block is too complex to read" {
			t.Errorf("parseHTML = %v, want the reason about the complexity", err)
		}
	})
}

func TestStray(t *testing.T) {
	keepDiv := func(n *html.Node) bool { return n.Name == "div" && hasClass(n, "wp-block-group__inner-container") }
	for _, tt := range []struct {
		name string
		in   string
		keep func(*html.Node) bool
		note bool
	}{
		{name: "nothing", in: ""},
		{name: "white space and comments", in: " \n\t<!-- x -->\f\r "},
		{name: "the element that is kept", in: ` <div class="wp-block-group__inner-container"></div> `, keep: keepDiv},
		{name: "the element that is kept, and white space", in: "\n<!-- x --><div class=\"wp-block-group__inner-container\">text</div>\n", keep: keepDiv},
		{name: "an element that is not kept", in: `<div class="other"></div>`, keep: keepDiv, note: true},
		{name: "no element is kept", in: `<div class="wp-block-group__inner-container"></div>`, note: true},
		{name: "text", in: "stray", keep: keepDiv, note: true},
		{name: "text after the element", in: `<div class="wp-block-group__inner-container"></div> x `, keep: keepDiv, note: true},
		{name: "a no-break space", in: string(rune(0xA0)), keep: keepDiv, note: true},
		{name: "two things", in: "a<b>c</b><i></i>", keep: keepDiv, note: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			imp := bodyImport()
			c := richConv(imp, "", nil)
			c.stray(htmlOf(t, tt.in), tt.keep, "core/group", noteAt{})
			c.flush(nil)
			var want []string
			if tt.note {
				want = bodyLines(`skipped: the HTML of the block "core/group" outside its inner blocks is not imported -> /body`)
			}
			wantLines(t, imp.Report, want)
		})
	}
}

func TestHTMLHelpers(t *testing.T) {
	root := htmlOf(t, `<div id="a" class=" x  y&#9;z "><p class="p">one <b>two</b></p><!-- c --><ul><li class="x">three</li></ul><svg><title>t</title></svg>`+
		`<script>js()</script><template><i>no</i></template><noscript>ns</noscript><span>four</span></div>tail`)
	div := root.FirstChild

	if got := classList(div); !slices.Equal(got, []string{"x", "y", "z"}) {
		t.Errorf("classList = %q", got)
	}
	for class, want := range map[string]bool{"x": true, "z": true, "": false, "xy": false, "a": false} {
		if got := hasClass(div, class); got != want {
			t.Errorf("hasClass(%q) = %t, want %t", class, got, want)
		}
	}
	var names []string
	for _, n := range childElements(div) {
		names = append(names, n.Name)
	}
	if want := []string{"p", "ul", "svg", "script", "template", "noscript", "span"}; !slices.Equal(names, want) {
		t.Errorf("childElements = %q, want %q", names, want)
	}
	if n := findElement(div, "li"); n == nil || n.Name != "li" || !hasClass(n, "x") {
		t.Errorf("findElement(li) = %v", n)
	}
	if n := findElement(div, "b", "li"); n == nil || n.Name != "b" {
		t.Errorf("findElement(b, li) = %v, want the first in the document", n)
	}
	if n := findElement(div, "title"); n != nil {
		t.Errorf("findElement(title) = %v, want none: it is in another namespace", n)
	}
	if n := findElement(div, "div"); n != nil {
		t.Errorf("findElement(div) = %v, want none: the element itself is not below it", n)
	}
	if n := findElement(root.LastChild, "p"); n != nil {
		t.Errorf("findElement of a text = %v", n)
	}
	if got, want := elementText(div), "one twothreetfour"; got != want {
		t.Errorf("elementText = %q, want %q", got, want)
	}
	if got := elementText(root); got != "one twothreetfourtail" {
		t.Errorf("elementText(root) = %q", got)
	}

	// after goes through every node once, in the order of the document.
	var order []string
	for n := root.FirstChild; n != nil; n = after(root, n, true) {
		label := "!"
		if n.Type == html.ElementNode {
			label = n.Name
		}
		if n.Type == html.TextNode {
			label = "#" + n.Data
		}
		order = append(order, label)
	}
	want := []string{"div", "p", "#one ", "b", "#two", "!", "ul", "li", "#three", "svg", "title", "#t", "script", "#js()", "template", "noscript", "#ns", "span", "#four", "#tail"}
	if !slices.Equal(order, want) {
		t.Errorf("after goes through %q, want %q", order, want)
	}
	// Without descending, it skips the children.
	order = nil
	for n := root.FirstChild; n != nil; n = after(root, n, false) {
		if n.Type == html.ElementNode {
			order = append(order, n.Name)
		}
	}
	if want := []string{"div"}; !slices.Equal(order, want) {
		t.Errorf("after without descending goes through %q, want %q", order, want)
	}
}

// TestHTMLHelpersDeep checks that the helpers that go through the HTML do not recurse: with a stack that a walk of a hundred
// thousand levels would fill, they read elements that nest that deep.
func TestHTMLHelpersDeep(t *testing.T) {
	root := htmlOf(t, strings.Repeat("<span>", 100000)+"x"+strings.Repeat("</span>", 100000)+"y")
	withStackLimit(func() {
		if got := elementText(root); got != "xy" {
			t.Errorf("elementText = %q", got)
		}
		if n := findElement(root, "b"); n != nil {
			t.Errorf("findElement = %v", n)
		}
		if n := findClass(root, "x"); n != nil {
			t.Errorf("findClass = %v", n)
		}
		count := 0
		for n := root.FirstChild; n != nil; n = after(root, n, true) {
			count++
		}
		if count != 100000+2 {
			t.Errorf("after goes through %d nodes, want %d", count, 100000+2)
		}
	})
}

func TestHasTag(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want bool
	}{
		{"", false},
		{"<", false},
		{"<span>", true},
		{"a <SPAN class=x>", true},
		{"<p><Mark>x</mark></p>", true},
		{"<spa", false},
		{"<spam>", false},
		{"a < span", false},
		{"x<p>y</p>", false},
		{"<<<span", true},
		{"</span>", false},
		{"<b>mark</b> span", false},
	} {
		if got := hasTag(tt.in, "span", "mark"); got != tt.want {
			t.Errorf("hasTag(%q) = %t, want %t", tt.in, got, tt.want)
		}
	}
}
