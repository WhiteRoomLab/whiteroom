// Package sanitize cleans the HTML of html and legacy blocks (6.5.4): it keeps the elements, attributes and URLs that
// its policy allows, and reports what it changes.
package sanitize

import (
	"math"

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
	// ID reports whether to keep id, which follows the rule of anchors and is the first of the HTML, and reserves it;
	// nil keeps it.
	ID func(id string) bool
}

// Change is a change that Sanitize made Count times, such as removing an element, described by Msg.
type Change struct {
	Msg   string
	Count int
}

// ErrLimit is the error of Sanitize for HTML that passes the limits of the parser, or whose output would be too long
// for its length. It is html.ErrLimit.
var ErrLimit = html.ErrLimit

// Sanitize parses s as the content of a div element, as browsers parse innerHTML, removes what the policy of opts.Kind
// does not keep, and returns the HTML of what is left, with the changes that it made. On an error, it returns no HTML
// and no changes.
func Sanitize(s string, opts Options) (string, []Change, error) {
	popts, maxOut := limits(len(s))
	nodes, err := html.ParseFragment(s, &html.Node{Type: html.ElementNode, Namespace: html.HTML, Name: "div"}, popts)
	if err != nil {
		return "", nil, err
	}
	root := &html.Node{Type: html.DocumentFragmentNode}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	c := cleaner{kind: opts.Kind}
	c.clean(root)
	out := html.Render(root)
	if len(out) > maxOut {
		return "", nil, ErrLimit
	}
	return out, c.changes.result(), nil
}

// limits returns the options of the parser for an input of n bytes, and the most bytes of output. Within its limits,
// the parser can copy formatting elements with their attributes into a tree that Render writes in about 81 bytes a
// node, while the output of realistic HTML is at most 5 times as long as its input.
func limits(n int) (html.Options, int) {
	n = min(n, (math.MaxInt-1<<16)/64)
	return html.Options{Scripting: true, MaxNodes: n + 1024, MaxSteps: 64*n + 1<<16}, 16*n + 1<<16
}
