// Package sanitize cleans the HTML of html and legacy blocks (6.5.4): it keeps the elements, attributes and URLs that
// its policy allows, and reports what it changes.
package sanitize

import (
	"errors"
	"iter"
	"math"
	"slices"

	"github.com/WhiteRoomLab/whiteroom/internal/html"
)

// Kind is the kind of block whose HTML is sanitized, which decides the elements that are kept.
type Kind int

const (
	// Legacy is the strict kind, of the legacy blocks of imported content.
	Legacy Kind = iota
	// HTML is the kind of html blocks, which also keeps iframe, video, audio, source and track elements.
	HTML
)

type Options struct {
	Kind Kind
	// URL returns the URL to write for u, an allowed URL; nil keeps u.
	URL func(u string) string
	// ID reports whether to keep id, which follows the rule of anchors and is the first of the HTML, and reserves it.
	// Sanitize calls it for the ids that it keeps otherwise, in document order; nil keeps them.
	ID func(id string) bool
}

// Change is a change that Sanitize made Count times, such as removing an element, described by Msg.
type Change struct {
	Msg   string
	Count int
}

// ErrLimit is the error of Sanitize for HTML that passes the limits of the parser, or whose output would be too long
// for its length or pass the limits for its own length. It is html.ErrLimit.
var ErrLimit = html.ErrLimit

// ErrUnstable is the error of Sanitize for HTML whose output does not settle: parsing and cleaning it again still
// changes it after maxPasses times.
var ErrUnstable = errors.New("sanitize: the HTML does not settle into a stable form")

// maxPasses is the most times that Sanitize parses and cleans its output again; a variable for the tests.
var maxPasses = 4

// Sanitize parses s as the content of a div element, as browsers parse innerHTML, removes what the policy of opts.Kind
// does not keep, and returns the HTML of what is left, with the changes that it made. The HTML is stable: parsing it
// as browsers do gives the tree that it was written from, and with nil opts.URL and opts.ID, Sanitize returns it as
// it is, with no changes. On an error, ErrLimit or ErrUnstable, it returns no HTML and no changes.
func Sanitize(s string, opts Options) (string, []Change, error) {
	c := cleaner{kind: opts.Kind}
	popts, maxOut := limits(len(s))
	root, err := c.pass(s, popts)
	if err != nil {
		return "", nil, err
	}
	out, root, err := c.settle(html.Render(root), len(s), maxOut)
	if err != nil {
		return "", nil, err
	}
	if c.reserve(root, opts.ID) {
		// Ids of formatting elements count in the Noah's Ark clause of the parser, so the tree is checked again.
		if out, root, err = c.settle(html.Render(root), len(s), maxOut); err != nil {
			return "", nil, err
		}
	}
	if opts.URL != nil {
		// The tree stays: only the Noah's Ark clause reads attributes here, and it never compares two elements with URLs.
		rewriteURLs(root, opts.URL)
		out = html.Render(root)
	}
	return out, c.changes.result(), nil
}

// pass parses s as Sanitize parses its input, with opts, and cleans the tree.
func (c *cleaner) pass(s string, opts html.Options) (*html.Node, error) {
	nodes, err := html.ParseFragment(s, &html.Node{Type: html.ElementNode, Namespace: html.HTML, Name: "div"}, opts)
	if err != nil {
		return nil, err
	}
	root := &html.Node{Type: html.DocumentFragmentNode}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	c.clean(root)
	return root, nil
}

// settle parses out, the HTML of a cleaned tree, and cleans and writes it again until that changes nothing, and returns
// that HTML and its tree; n is the length of the input of Sanitize. Parsing what Render writes can give another tree
// (13.3), as when unwrapping left a div element in a p element.
func (c *cleaner) settle(out string, n, maxOut int) (string, *html.Node, error) {
	for range maxPasses {
		if len(out) > maxOut {
			return "", nil, ErrLimit
		}
		// Within the limits for out too, so that Sanitize(out) passes them.
		opts, _ := limits(min(n, len(out)))
		edits := c.edits
		root, err := c.pass(out, opts)
		if err != nil {
			return "", nil, err
		}
		again := html.Render(root)
		if again == out && c.edits == edits {
			return out, root, nil
		}
		out = again
	}
	return "", nil, ErrUnstable
}

// reserve calls id for the id of each element below root in document order, and removes the ids that it does not
// keep. It reports whether it removed any.
func (c *cleaner) reserve(root *html.Node, id func(string) bool) bool {
	if id == nil {
		return false
	}
	removed := false
	for n := range descendants(root) {
		i := slices.IndexFunc(n.Attrs, func(a html.Attr) bool { return a.Name == "id" })
		if i < 0 || id(n.Attrs[i].Value) {
			continue
		}
		c.addf("removed id attribute of <%s>: anchor %q is already used", n.Name, cut(n.Attrs[i].Value))
		n.Attrs = slices.Delete(slices.Clone(n.Attrs), i, i+1)
		removed = true
	}
	return removed
}

// rewriteURLs replaces the value of each URL attribute below root with what url returns for it.
func rewriteURLs(root *html.Node, url func(string) string) {
	for n := range descendants(root) {
		n.Attrs = slices.Clone(n.Attrs) // nodes may share an Attrs array
		for i, a := range n.Attrs {
			if urlAttrs[[2]string{n.Name, a.Name}] != noURL {
				n.Attrs[i].Value = url(a.Value)
			}
		}
	}
}

// descendants yields the elements below root in document order.
func descendants(root *html.Node) iter.Seq[*html.Node] {
	return func(yield func(*html.Node) bool) {
		for n := root.FirstChild; n != nil; {
			if n.Type == html.ElementNode && !yield(n) {
				return
			}
			if n.FirstChild != nil {
				n = n.FirstChild
				continue
			}
			for n.NextSibling == nil {
				if n = n.Parent; n == root {
					return
				}
			}
			n = n.NextSibling
		}
	}
}

// limits returns the options of the parser for an input of n bytes, and the most bytes of output. Within its limits,
// the parser can copy formatting elements with their attributes into a tree that Render writes in about 81 bytes a
// node, while the output of realistic HTML is at most 5 times as long as its input.
func limits(n int) (html.Options, int) {
	n = min(n, (math.MaxInt-1<<16)/64)
	return html.Options{Scripting: true, MaxNodes: n + 1024, MaxSteps: 64*n + 1<<16}, 16*n + 1<<16
}
