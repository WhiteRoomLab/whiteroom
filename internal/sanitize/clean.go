package sanitize

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/html"
)

// maxDepth is the most elements that a kept element is nested in, counting itself; deeper ones are unwrapped.
const maxDepth = 256

// cleaner removes from a tree what the policy of its kind does not keep, and collects the changes.
type cleaner struct {
	kind    Kind
	changes changes
	deep    bool // elements deeper than maxDepth were unwrapped
}

// action is what becomes of a node.
type action uint8

const (
	keep   action = iota
	remove        // with its descendants
	unwrap        // its children are kept in its place
)

// clean cleans the descendants of root in document order, without recursion: the input can nest elements deeply.
func (c *cleaner) clean(root *html.Node) {
	parent, n, depth := root, root.FirstChild, 0 // depth: of parent, in kept elements below root
	for {
		if n == nil {
			if parent == root {
				return
			}
			parent, n, depth = parent.Parent, parent.NextSibling, depth-1
			continue
		}
		next := n.NextSibling
		switch c.node(n, depth) {
		case keep:
			if n.FirstChild != nil {
				parent, next, depth = n, n.FirstChild, depth+1
			}
		case remove:
			parent.RemoveChild(n)
		case unwrap:
			if n.FirstChild != nil {
				next = n.FirstChild
			}
			for ch := n.FirstChild; ch != nil; ch = n.FirstChild {
				n.RemoveChild(ch)
				parent.InsertBefore(ch, n)
			}
			parent.RemoveChild(n)
		}
		n = next
	}
}

// node returns what becomes of n, a child of a kept element at depth (0 for the top), and cleans n if it is kept.
func (c *cleaner) node(n *html.Node, depth int) action {
	if n.Type == html.TextNode {
		return keep
	}
	if n.Type != html.ElementNode {
		return remove // comments, processing instructions and doctypes, silently
	}
	if n.Namespace != html.HTML {
		c.changes.addf("removed <%s> element and its content", name(n.Name))
		return remove
	}
	if dropped[n.Name] || htmlElements[n.Name] && c.kind != HTML {
		if void[n.Name] {
			c.changes.addf("removed <%s> element", n.Name)
		} else {
			c.changes.addf("removed <%s> element and its content", n.Name)
		}
		return remove
	}
	el := n.Name
	if el == "h1" {
		el = "h2"
	}
	if !elements[el] && (!htmlElements[el] || c.kind != HTML) {
		if void[el] {
			c.changes.addf("removed <%s> element", el)
		} else {
			c.changes.addf("removed <%s> element, keeping its content", name(el))
		}
		return unwrap
	}
	if depth >= maxDepth {
		if !c.deep {
			c.deep = true
			c.changes.addf("removed elements nested more than %d deep, keeping their content", maxDepth)
		}
		return unwrap
	}
	if el != n.Name {
		c.changes.addf("changed <%s> to <%s>", n.Name, el)
		n.Name = el
	}
	if needSrc[el] {
		if reason := srcProblem(n); reason != "" {
			c.changes.addf("removed <%s> element: %s", el, reason)
			return remove
		}
	}
	c.attrs(n)
	if el == "iframe" {
		c.emptyFrame(n)
	}
	return keep
}

// srcProblem returns why n, an element of needSrc, has no valid src, or "".
func srcProblem(n *html.Node) string {
	for _, a := range n.Attrs {
		if a.Namespace == "" && a.Name == "src" {
			if reason := checkURL(content.NormalizeHref(a.Value), urlAttrs[[2]string{n.Name, a.Name}]); reason != "" {
				return fmt.Sprintf("invalid src %q: %s", cut(a.Value), reason)
			}
			return ""
		}
	}
	return "missing src"
}

// attrs removes the attributes of n, a kept element, that it does not keep, and normalizes the others.
func (c *cleaner) attrs(n *html.Node) {
	if len(n.Attrs) == 0 {
		return
	}
	kept := make([]html.Attr, 0, len(n.Attrs)) // not in place: nodes may share an Attrs array
	var rel []string
	relAt, newTab := -1, false
	for _, a := range n.Attrs {
		if a.Namespace != "" || !globalAttrs[a.Name] && !slices.Contains(elementAttrs[n.Name], a.Name) {
			c.changes.addf("removed %s attribute of <%s>", name(qualifiedName(a)), n.Name)
			continue
		}
		if slices.ContainsFunc(kept, func(b html.Attr) bool { return b.Name == a.Name }) {
			continue // a repeated name, which the tokenizer drops too
		}
		if kind := urlAttrs[[2]string{n.Name, a.Name}]; kind != noURL {
			v := content.NormalizeHref(a.Value)
			if reason := checkURL(v, kind); reason != "" {
				c.changes.addf("removed %s attribute of <%s>: invalid URL %q: %s", a.Name, n.Name, cut(a.Value), reason)
				continue
			}
			a.Value = v
		}
		switch a.Name {
		case "class":
			if a.Value = c.class(a.Value, n.Name); a.Value == "" {
				continue
			}
		case "id":
			if reason := idProblem(a.Value); reason != "" {
				c.changes.addf("removed id attribute of <%s>: %s", n.Name, reason)
				continue
			}
		case "target":
			if asciiLower(a.Value) != "_blank" {
				c.changes.addf("removed target attribute of <%s>: only _blank is allowed", n.Name)
				continue
			}
			a.Value, newTab = "_blank", true
		case "rel":
			rel, relAt = c.rel(a.Value, n.Name), len(kept)
		}
		kept = append(kept, a)
	}
	if newTab {
		rel = slices.Insert(slices.DeleteFunc(rel, func(v string) bool { return v == "noopener" }), 0, "noopener")
	}
	switch {
	case relAt >= 0 && len(rel) > 0:
		kept[relAt].Value = strings.Join(rel, " ")
	case relAt >= 0:
		kept = slices.Delete(kept, relAt, relAt+1)
	case len(rel) > 0:
		kept = append(kept, html.Attr{Name: "rel", Value: strings.Join(rel, " ")})
	}
	n.Attrs = kept
}

// checkURL returns why v, a URL normalized by content.NormalizeHref, is not allowed for kind, or "".
func checkURL(v string, kind urlKind) string {
	if kind == linkURL && strings.HasPrefix(v, "#") {
		return ""
	}
	if err := content.CheckHref(v); err != nil {
		return cut(err.Error())
	}
	u, err := url.Parse(v)
	if err != nil {
		return "invalid URL" // content.CheckHref parsed v
	}
	switch {
	case slices.Contains(schemes[kind], u.Scheme):
		return ""
	case u.Scheme == "":
		return "must be an https URL"
	}
	return fmt.Sprintf("scheme %q is not allowed", u.Scheme)
}

// class returns v, the class of an element el, without the reserved classes, which it reports, and with single spaces.
func (c *cleaner) class(v, el string) string {
	var kept []string
	for _, t := range strings.FieldsFunc(v, isASCIISpace) {
		if content.ReservedClass(t) {
			c.changes.addf("removed class %q of <%s>: reserved for the core", cut(t), el)
			continue
		}
		kept = append(kept, t)
	}
	return strings.Join(kept, " ")
}

// rel returns the values of v, the rel of an element el, that are kept: in lower case, in order and without repeats.
// It reports the others.
func (c *cleaner) rel(v, el string) []string {
	var kept []string
	for _, t := range strings.FieldsFunc(v, isASCIISpace) {
		t = asciiLower(t)
		switch {
		case !slices.Contains(relValues, t):
			c.changes.addf("removed rel value %q of <%s>", cut(t), el)
		case !slices.Contains(kept, t):
			kept = append(kept, t)
		}
	}
	return kept
}

// idProblem returns why id breaks the rule of anchors (content.CheckName), or "".
func idProblem(id string) string {
	switch {
	case content.CheckName(id) == nil:
		return ""
	case id == "":
		return "empty id"
	case len(id) > content.MaxTextBytes:
		return fmt.Sprintf("invalid id %q: too long", cut(id))
	}
	return fmt.Sprintf(`invalid id %q: use ASCII letters, digits, "-" and "_"`, cut(id))
}

// emptyFrame removes the children of n, an iframe element: browsers do not show them, and Render writes them as they
// are. It reports them unless they are whitespace.
func (c *cleaner) emptyFrame(n *html.Node) {
	report := false
	for ch := n.FirstChild; ch != nil; ch = n.FirstChild {
		report = report || ch.Type != html.TextNode || strings.Trim(ch.Data, asciiSpace) != ""
		n.RemoveChild(ch)
	}
	if report {
		c.changes.addf("removed the content of <iframe>")
	}
}

// qualifiedName returns the name of a as Render writes it.
func qualifiedName(a html.Attr) string {
	if a.Namespace == "" || a.Namespace == "xmlns" && a.Name == "xmlns" {
		return a.Name
	}
	return a.Namespace + ":" + a.Name
}

const asciiSpace = "\t\n\f\r "

func isASCIISpace(r rune) bool {
	return strings.ContainsRune(asciiSpace, r)
}

func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}
