package html

import (
	"slices"
	"strings"
)

// The rules for parsing tokens in foreign content (13.2.6.5), the integration points of the tree construction
// dispatcher (13.2.6) and the adjustments of the attributes of foreign elements (13.2.6.1).

func isMathMLTextIntegrationPoint(n *Node) bool {
	if n.Namespace != MathML {
		return false
	}
	switch n.Name {
	case "mi", "mo", "mn", "ms", "mtext":
		return true
	}
	return false
}

func (p *parser) isHTMLIntegrationPoint(n *Node) bool {
	switch n.Namespace {
	case MathML:
		return p.htmlAnnotations[n]
	case SVG:
		switch n.Name {
		case "foreignObject", "desc", "title":
			return true
		}
	case HTML:
	}
	return false
}

// htmlAnnotation reports whether a MathML element n is an annotation-xml element that is an HTML integration point.
// Its attributes are those of its start tag token: adjusting the attributes of a token does not rename encoding, and
// the fragment parsing algorithm makes the token of the context element from the attributes of the element.
func htmlAnnotation(n *Node) bool {
	if n.Name != "annotation-xml" {
		return false
	}
	for _, a := range n.Attrs {
		if a.Namespace == "" && a.Name == "encoding" {
			return equalFold(a.Value, "text/html") || equalFold(a.Value, "application/xhtml+xml")
		}
	}
	return false
}

// 13.2.6.5
func (p *parser) inForeignContent(tok *token) bool {
	switch tok.typ {
	case characterToken:
		p.foreignText(tok.data)
	case commentToken:
		p.insertComment(tok.data, nil, nil)
	case processingInstructionToken:
		p.insertProcessingInstruction(tok, nil, nil)
	case doctypeToken:
	case startTagToken:
		if breaksOut(tok) {
			return p.popForeign(tok)
		}
		p.foreignStartTag(tok)
	case endTagToken:
		if tok.name == "br" || tok.name == "p" {
			return p.popForeign(tok)
		}
		if n := p.currentNode(); tok.name == "script" && n.Namespace == SVG && n.Name == "script" {
			// Scripts are not run: what is left of the steps is popping the script element.
			p.pop()
			return false
		}
		return p.foreignEndTag(tok)
	case eofToken:
		// The tree construction dispatcher gives end-of-file tokens to the insertion mode.
	}
	return false
}

// foreignText inserts the characters s as foreign content does: U+0000 becomes U+FFFD, and the characters other than
// U+0000 and whitespace set the frameset-ok flag to "not ok".
func (p *parser) foreignText(s string) {
	for i := range len(s) {
		if c := s[i]; c != 0 && !isASCIIWhitespace(c) {
			p.framesetOK = false
			break
		}
	}
	p.insertText(strings.ReplaceAll(s, "\x00", replacement))
}

// breaksOut reports whether the start tag tok is one of those that foreign content handles with popForeign.
func breaksOut(tok *token) bool {
	switch tok.name {
	case "b", "big", "blockquote", "body", "br", "center", "code", "dd", "div", "dl", "dt", "em", "embed", "h1", "h2",
		"h3", "h4", "h5", "h6", "head", "hr", "i", "img", "li", "listing", "menu", "meta", "nobr", "ol", "p", "pre",
		"ruby", "s", "small", "span", "strong", "strike", "sub", "sup", "table", "tt", "u", "ul", "var":
		return true
	case "font":
		return slices.ContainsFunc(tok.attrs, func(a Attr) bool {
			return a.Name == "color" || a.Name == "face" || a.Name == "size"
		})
	}
	return false
}

// popForeign pops elements until the current node is a MathML text integration point, an HTML integration point or an
// HTML element, then processes tok by the rules of the insertion mode, and reports whether tok must be reprocessed.
func (p *parser) popForeign(tok *token) bool {
	for {
		n := p.currentNode()
		if n.Namespace == HTML || isMathMLTextIntegrationPoint(n) || p.isHTMLIntegrationPoint(n) {
			break
		}
		p.pop()
	}
	return p.process(p.mode, tok)
}

// foreignStartTag handles "any other start tag" in foreign content.
func (p *parser) foreignStartTag(tok *token) {
	ns := p.adjustedCurrentNode().Namespace
	switch ns {
	case MathML:
		adjustMathMLAttrs(tok)
	case SVG:
		if name, ok := svgTagNames[tok.name]; ok {
			tok.name = name
		}
		adjustSVGAttrs(tok)
	case HTML:
	}
	adjustForeignAttrs(tok)
	if n := p.insertForeignElement(tok, ns); ns == MathML && htmlAnnotation(n) {
		p.htmlAnnotations[n] = true
	}
	if tok.selfClosing {
		// For an SVG script element, this is also what a script end tag does when scripts are not run.
		p.pop()
	}
}

// foreignEndTag handles "any other end tag" in foreign content, and reports whether tok must be reprocessed.
func (p *parser) foreignEndTag(tok *token) bool {
	for i := len(p.stack) - 1; i > 0; {
		if equalFold(p.stack[i].Name, tok.name) {
			p.popTo(i)
			return false
		}
		i--
		if p.stack[i].Namespace == HTML {
			return p.process(p.mode, tok)
		}
	}
	return false
}

// adjustMathMLAttrs adjusts MathML attributes for tok (13.2.6.1).
func adjustMathMLAttrs(tok *token) {
	for i := range tok.attrs {
		if tok.attrs[i].Name == "definitionurl" {
			tok.attrs[i].Name = "definitionURL"
		}
	}
}

// adjustSVGAttrs adjusts SVG attributes for tok (13.2.6.1).
func adjustSVGAttrs(tok *token) {
	for i := range tok.attrs {
		if name, ok := svgAttrNames[tok.attrs[i].Name]; ok {
			tok.attrs[i].Name = name
		}
	}
}

// adjustForeignAttrs adjusts foreign attributes for tok (13.2.6.1).
func adjustForeignAttrs(tok *token) {
	for i := range tok.attrs {
		if a, ok := foreignAttrs[tok.attrs[i].Name]; ok {
			tok.attrs[i].Namespace, tok.attrs[i].Name = a.Namespace, a.Name
		}
	}
}

// svgTagNames is the table of tag names of "any other start tag" in foreign content (13.2.6.5).
var svgTagNames = map[string]string{
	"altglyph":            "altGlyph",
	"altglyphdef":         "altGlyphDef",
	"altglyphitem":        "altGlyphItem",
	"animatecolor":        "animateColor",
	"animatemotion":       "animateMotion",
	"animatetransform":    "animateTransform",
	"clippath":            "clipPath",
	"feblend":             "feBlend",
	"fecolormatrix":       "feColorMatrix",
	"fecomponenttransfer": "feComponentTransfer",
	"fecomposite":         "feComposite",
	"feconvolvematrix":    "feConvolveMatrix",
	"fediffuselighting":   "feDiffuseLighting",
	"fedisplacementmap":   "feDisplacementMap",
	"fedistantlight":      "feDistantLight",
	"fedropshadow":        "feDropShadow",
	"feflood":             "feFlood",
	"fefunca":             "feFuncA",
	"fefuncb":             "feFuncB",
	"fefuncg":             "feFuncG",
	"fefuncr":             "feFuncR",
	"fegaussianblur":      "feGaussianBlur",
	"feimage":             "feImage",
	"femerge":             "feMerge",
	"femergenode":         "feMergeNode",
	"femorphology":        "feMorphology",
	"feoffset":            "feOffset",
	"fepointlight":        "fePointLight",
	"fespecularlighting":  "feSpecularLighting",
	"fespotlight":         "feSpotLight",
	"fetile":              "feTile",
	"feturbulence":        "feTurbulence",
	"foreignobject":       "foreignObject",
	"glyphref":            "glyphRef",
	"lineargradient":      "linearGradient",
	"radialgradient":      "radialGradient",
	"textpath":            "textPath",
}

// svgAttrNames is the table of adjust SVG attributes (13.2.6.1).
var svgAttrNames = map[string]string{
	"attributename":       "attributeName",
	"attributetype":       "attributeType",
	"basefrequency":       "baseFrequency",
	"baseprofile":         "baseProfile",
	"calcmode":            "calcMode",
	"clippathunits":       "clipPathUnits",
	"diffuseconstant":     "diffuseConstant",
	"edgemode":            "edgeMode",
	"filterunits":         "filterUnits",
	"glyphref":            "glyphRef",
	"gradienttransform":   "gradientTransform",
	"gradientunits":       "gradientUnits",
	"kernelmatrix":        "kernelMatrix",
	"kernelunitlength":    "kernelUnitLength",
	"keypoints":           "keyPoints",
	"keysplines":          "keySplines",
	"keytimes":            "keyTimes",
	"lengthadjust":        "lengthAdjust",
	"limitingconeangle":   "limitingConeAngle",
	"markerheight":        "markerHeight",
	"markerunits":         "markerUnits",
	"markerwidth":         "markerWidth",
	"maskcontentunits":    "maskContentUnits",
	"maskunits":           "maskUnits",
	"numoctaves":          "numOctaves",
	"pathlength":          "pathLength",
	"patterncontentunits": "patternContentUnits",
	"patterntransform":    "patternTransform",
	"patternunits":        "patternUnits",
	"pointsatx":           "pointsAtX",
	"pointsaty":           "pointsAtY",
	"pointsatz":           "pointsAtZ",
	"preservealpha":       "preserveAlpha",
	"preserveaspectratio": "preserveAspectRatio",
	"primitiveunits":      "primitiveUnits",
	"refx":                "refX",
	"refy":                "refY",
	"repeatcount":         "repeatCount",
	"repeatdur":           "repeatDur",
	"requiredextensions":  "requiredExtensions",
	"requiredfeatures":    "requiredFeatures",
	"specularconstant":    "specularConstant",
	"specularexponent":    "specularExponent",
	"spreadmethod":        "spreadMethod",
	"startoffset":         "startOffset",
	"stddeviation":        "stdDeviation",
	"stitchtiles":         "stitchTiles",
	"surfacescale":        "surfaceScale",
	"systemlanguage":      "systemLanguage",
	"tablevalues":         "tableValues",
	"targetx":             "targetX",
	"targety":             "targetY",
	"textlength":          "textLength",
	"viewbox":             "viewBox",
	"viewtarget":          "viewTarget",
	"xchannelselector":    "xChannelSelector",
	"ychannelselector":    "yChannelSelector",
	"zoomandpan":          "zoomAndPan",
}

// foreignAttrs is the table of adjust foreign attributes (13.2.6.1). Attr keeps no prefix: the namespace and the local
// name give it, as in 13.3.
var foreignAttrs = map[string]Attr{
	"xlink:actuate": {Namespace: "xlink", Name: "actuate"},
	"xlink:arcrole": {Namespace: "xlink", Name: "arcrole"},
	"xlink:href":    {Namespace: "xlink", Name: "href"},
	"xlink:role":    {Namespace: "xlink", Name: "role"},
	"xlink:show":    {Namespace: "xlink", Name: "show"},
	"xlink:title":   {Namespace: "xlink", Name: "title"},
	"xlink:type":    {Namespace: "xlink", Name: "type"},
	"xml:lang":      {Namespace: "xml", Name: "lang"},
	"xml:space":     {Namespace: "xml", Name: "space"},
	"xmlns":         {Namespace: "xmlns", Name: "xmlns"},
	"xmlns:xlink":   {Namespace: "xmlns", Name: "xlink"},
}
