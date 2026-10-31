package html

import "errors"

// ParseFragment parses s with the HTML fragment parsing algorithm (13.4) for the context element context, as innerHTML
// does, and returns the nodes of the fragment in order, without a parent. Of context, it reads the name, the namespace
// and the attributes, and the ancestors for a form element; it does not change it. Like Parse, it runs no scripts and
// reports no parse errors. It returns an error if context is not an HTML, SVG or MathML element, and ErrLimit if the
// parse passes a limit of opts.
func ParseFragment(s string, context *Node, opts Options) ([]*Node, error) {
	if context == nil || context.Type != ElementNode || context.Namespace > MathML {
		return nil, errors.New("html: the context of a fragment must be an HTML, SVG or MathML element")
	}
	p, err := parse(s, context, opts)
	if err != nil {
		return nil, err
	}
	fragment := p.redirectTo
	var nodes []*Node
	for c := fragment.FirstChild; c != nil; c = fragment.FirstChild {
		fragment.RemoveChild(c)
		nodes = append(nodes, c)
	}
	return nodes, nil
}

// initFragment sets p up to parse a fragment for context (13.4): the insertions into the root element go to the
// fragment, the insertion target redirection map's only entry.
func (p *parser) initFragment(context *Node) {
	// The context belongs to no document here, so the document of the parser stays in no-quirks mode.
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
			if p.scripting {
				p.z.setState(rawtextState)
			}
		case "plaintext":
			p.z.setState(plaintextState)
		}
	}
	p.grow(2)
	root := &Node{Type: ElementNode, Namespace: HTML, Name: "html"}
	p.doc.AppendChild(root)
	p.push(root)
	p.redirectFrom, p.redirectTo = root, &Node{Type: DocumentFragmentNode}
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
}
