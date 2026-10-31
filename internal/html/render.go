package html

import "strings"

// Render writes nodes as HTML, each as the HTML fragment serialization algorithm (13.3) writes a child: a Document or
// DocumentFragment node as its children; an element as its start tag with the attributes in order and, unless it is
// void, its children (those of its Content for an HTML template element) and its end tag; a Text node as its data,
// escaped unless its parent is an HTML style, script, xmp, iframe, noembed, noframes, plaintext or noscript element
// (Render takes scripting as enabled, as browsers parse). It differs from 13.3 in its escapes, those of html/template,
// and where parsing what 13.3 writes would not give the tree again:
//   - Text is escaped as &amp;, &lt; and &gt;, attribute values also " and ' as &#34; and &#39;; U+00A0 is kept.
//   - U+0000 is written as U+FFFD.
//   - A pre, textarea or listing element whose first child is a Text node starting with a line feed gets one more
//     line feed after its start tag, which the parser drops.
//   - Nothing is written after the children of a plaintext element, not even end tags: the parser takes the rest of
//     the input as its text.
func Render(nodes ...*Node) string {
	var r renderer
	for _, n := range nodes {
		if r.node(n) {
			break
		}
	}
	return r.b.String()
}

type renderer struct {
	b     strings.Builder
	stack []frame // the walk below the node that node writes, without recursion
}

// frame is an element or a document (fragment) whose children are being written.
type frame struct {
	end  *Node // the element whose end tag follows the children, or nil
	next *Node // the next child to write
}

// node writes n and its descendants, and reports whether it wrote the children of a plaintext element, after which
// nothing is written.
func (r *renderer) node(n *Node) bool {
	r.start(n)
	for len(r.stack) > 0 {
		f := &r.stack[len(r.stack)-1]
		if c := f.next; c != nil {
			f.next = c.NextSibling
			r.start(c)
			continue
		}
		end := f.end
		r.stack = r.stack[:len(r.stack)-1]
		if end == nil {
			continue
		}
		if end.isHTML("plaintext") {
			return true
		}
		r.b.WriteString("</")
		r.raw(end.Name)
		r.b.WriteByte('>')
	}
	return false
}

// start writes n, or the start tag of an element n, and pushes the children of n that remain to be written.
func (r *renderer) start(n *Node) {
	switch n.Type {
	case DocumentNode, DocumentFragmentNode:
		r.stack = append(r.stack, frame{next: n.FirstChild})
	case ElementNode:
		r.b.WriteByte('<')
		r.raw(n.Name)
		for _, a := range n.Attrs {
			r.b.WriteByte(' ')
			if a.Namespace != "" && (a.Namespace != "xmlns" || a.Name != "xmlns") {
				r.raw(a.Namespace)
				r.b.WriteByte(':')
			}
			r.raw(a.Name)
			r.b.WriteString(`="`)
			r.escape(a.Value, true)
			r.b.WriteByte('"')
		}
		r.b.WriteByte('>')
		if n.Namespace != HTML {
			r.stack = append(r.stack, frame{end: n, next: n.FirstChild})
			return
		}
		switch n.Name {
		case "area", "base", "basefont", "bgsound", "br", "col", "embed", "frame", "hr", "img", "input", "keygen",
			"link", "meta", "param", "source", "track", "wbr":
			return
		case "pre", "textarea", "listing":
			if c := n.FirstChild; c != nil && c.Type == TextNode && strings.HasPrefix(c.Data, "\n") {
				r.b.WriteByte('\n')
			}
		}
		children := n
		if n.Name == "template" && n.Content != nil {
			children = n.Content
		}
		r.stack = append(r.stack, frame{end: n, next: children.FirstChild})
	case TextNode:
		if rawText(n.Parent) {
			r.raw(n.Data)
		} else {
			r.escape(n.Data, false)
		}
	case CommentNode:
		r.b.WriteString("<!--")
		r.raw(n.Data)
		r.b.WriteString("-->")
	case ProcessingInstructionNode:
		r.b.WriteString("<?")
		r.raw(n.Name)
		r.b.WriteByte(' ')
		r.raw(n.Data)
		r.b.WriteString("?>")
	case DoctypeNode:
		r.b.WriteString("<!DOCTYPE ")
		r.raw(n.Name)
		r.b.WriteByte('>')
	}
}

// raw writes s as it is, but U+0000 as U+FFFD.
func (r *renderer) raw(s string) {
	for {
		i := strings.IndexByte(s, 0)
		if i < 0 {
			r.b.WriteString(s)
			return
		}
		r.b.WriteString(s[:i])
		r.b.WriteString(replacement)
		s = s[i+1:]
	}
}

// escape writes s with "&", "<" and ">" escaped, '"' and "'" too if attr, and U+0000 as U+FFFD.
func (r *renderer) escape(s string, attr bool) {
	last := 0
	for i := range len(s) {
		var e string
		switch c := s[i]; {
		case c == '&':
			e = "&amp;"
		case c == '<':
			e = "&lt;"
		case c == '>':
			e = "&gt;"
		case c == '"' && attr:
			e = "&#34;"
		case c == '\'' && attr:
			e = "&#39;"
		case c == 0:
			e = replacement
		default:
			continue
		}
		r.b.WriteString(s[last:i])
		r.b.WriteString(e)
		last = i + 1
	}
	r.b.WriteString(s[last:])
}

// rawText reports whether the Text children of n are written as they are.
func rawText(n *Node) bool {
	if n == nil || n.Type != ElementNode || n.Namespace != HTML {
		return false
	}
	switch n.Name {
	case "style", "script", "xmp", "iframe", "noembed", "noframes", "plaintext", "noscript":
		return true
	}
	return false
}
