package wpimport

import (
	"slices"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/html"
	"github.com/WhiteRoomLab/whiteroom/internal/wpblock"
)

// parseHTML reads s, HTML of a block, unless it is longer than that of one block may be, and counts its bytes among those
// that the conversion may read. The error is a legacy error.
func (c *bodyConv) parseHTML(s string) ([]*html.Node, error) {
	if len(s) > maxBlockHTMLBytes {
		return nil, legacyf("the HTML of the block has more than %d bytes", maxBlockHTMLBytes)
	}
	if err := c.readHTML(len(s)); err != nil {
		return nil, err
	}
	nodes, err := parseIn("div", s)
	if err != nil {
		return nil, legacyf("the HTML of the block is too complex to read")
	}
	return nodes, nil
}

// root reads the HTML of b, without that of its inner blocks, and returns its one element when comments and white space
// around it are left out. The element must be one of names, or any if there are none; if it is not, or there is no such
// element, the block is a legacy block.
func (c *bodyConv) root(b *wpblock.Block, names ...string) (*html.Node, error) {
	nodes, err := c.parseHTML(b.HTML())
	if err != nil {
		return nil, err
	}
	var first *html.Node
	rest := 0
	for _, n := range nodes {
		if n.Type == html.CommentNode || n.Type == html.TextNode && blank(n.Data) {
			continue
		}
		if rest++; rest == 1 {
			first = n
		}
	}
	if rest == 1 && first.Type == html.ElementNode && first.Namespace == html.HTML &&
		(len(names) == 0 || slices.Contains(names, first.Name)) {
		return first, nil
	}
	return nil, legacyf("the HTML of the block is not as WordPress writes it: expected %s", expectedText(names))
}

// expectedText lists the elements that root expected: <p>, <ul> or <ol>, <h2>, <h3> or <h4>.
func expectedText(names []string) string {
	switch len(names) {
	case 0:
		return "one element"
	case 1:
		return tagText(names[0])
	}
	tags := make([]string, len(names))
	for i, name := range names {
		tags[i] = tagText(name)
	}
	return strings.Join(tags[:len(tags)-1], ", ") + " or " + tags[len(tags)-1]
}

// stray notes, once, that the root of a block that has inner blocks holds something that is not imported: text that is
// not white space, or an element that keep does not accept, outside the inner blocks. name is the name of the block.
func (c *bodyConv) stray(root *html.Node, keep func(*html.Node) bool, name string, at noteAt) {
	for n := root.FirstChild; n != nil; n = n.NextSibling {
		switch {
		case n.Type == html.CommentNode, n.Type == html.TextNode && blank(n.Data):
		case n.Type == html.ElementNode && keep != nil && keep(n):
		default:
			c.note(at, Skipped, "the HTML of the block %q outside its inner blocks is not imported", name)
			return
		}
	}
}

// after returns the node that follows n in document order below root, or nil: the first child of n if descend is set
// and it has one, else the next sibling of n or of the nearest ancestor that has one. It does not recurse, as HTML can
// nest as deep as it is long.
func after(root, n *html.Node, descend bool) *html.Node {
	if descend && n.FirstChild != nil {
		return n.FirstChild
	}
	for ; n != nil && n != root; n = n.Parent {
		if n.NextSibling != nil {
			return n.NextSibling
		}
	}
	return nil
}

func childElements(n *html.Node) []*html.Node {
	var out []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			out = append(out, c)
		}
	}
	return out
}

// findElement returns the first element below n, in document order, that is in the HTML namespace and is one of names.
func findElement(n *html.Node, names ...string) *html.Node {
	for d := n.FirstChild; d != nil; d = after(n, d, true) {
		if d.Type == html.ElementNode && d.Namespace == html.HTML && slices.Contains(names, d.Name) {
			return d
		}
	}
	return nil
}

func classList(n *html.Node) []string {
	return strings.FieldsFunc(attr(n, "class"), isHTMLSpace)
}

func hasClass(n *html.Node, class string) bool {
	return slices.Contains(classList(n), class)
}

// isHTMLSpace reports whether r is ASCII white space as HTML has it.
func isHTMLSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\f' || r == '\r'
}

// elementText returns the text below n, without that of the elements that hold no text to read.
func elementText(n *html.Node) string {
	var b strings.Builder
	for d := n.FirstChild; d != nil; {
		descend := true
		switch {
		case d.Type == html.TextNode:
			b.WriteString(d.Data)
		case d.Type == html.ElementNode && d.Namespace == html.HTML && unreadElements[d.Name]:
			descend = false
		}
		d = after(n, d, descend)
	}
	return b.String()
}

var unreadElements = map[string]bool{"script": true, "style": true, "template": true, "noscript": true}
