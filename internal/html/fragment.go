package html

import "errors"

// ParseFragment parses s with the HTML fragment parsing algorithm (13.4) for the context element context, as innerHTML
// does, and returns the nodes of the fragment in order, without a parent. Of context, it reads the name, the namespace
// and the attributes, and the ancestors for a form element; it does not change it. Like Parse, it runs no scripts and
// reports no parse errors. It returns an error if context is not an HTML, SVG or MathML element.
func ParseFragment(s string, context *Node, opts Options) ([]*Node, error) {
	if context == nil || context.Type != ElementNode || context.Namespace > MathML {
		return nil, errors.New("html: the context of a fragment must be an HTML, SVG or MathML element")
	}
	// The context belongs to no document here, so the document of the parser stays in no-quirks mode.
	p := newParser(s, opts)
	p.context = context
	if context.Namespace == HTML {
		switch context.Name {
		case "title", "textarea":
			p.z.setState(rcdataState)
		case "style", "xmp", "iframe", "noembed", "noframes":
			p.z.setState(rawtextState)
		case "script":
			p.z.setState(scriptDataState)
		case "noscript":
			if opts.Scripting {
				p.z.setState(rawtextState)
			}
		case "plaintext":
			p.z.setState(plaintextState)
		}
	}
	root := &Node{Type: ElementNode, Namespace: HTML, Name: "html"}
	p.doc.AppendChild(root)
	p.push(root)
	fragment := &Node{Type: DocumentFragmentNode}
	p.redirectFrom, p.redirectTo = root, fragment
	if context.isHTML("template") {
		p.templateModes = append(p.templateModes, inTemplateMode)
	}
	if context.Namespace == MathML && htmlAnnotation(context) {
		p.htmlAnnotations[context] = true
	}
	p.resetInsertionMode()
	for n := context; n != nil; n = n.Parent {
		if n.isHTML("form") {
			p.form = n
			break
		}
	}
	p.run()
	var nodes []*Node
	for c := fragment.FirstChild; c != nil; c = fragment.FirstChild {
		fragment.RemoveChild(c)
		nodes = append(nodes, c)
	}
	return nodes, nil
}
