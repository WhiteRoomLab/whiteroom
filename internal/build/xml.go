package build

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

// xmlHeader is the XML declaration that the XML files of the build start with.
const xmlHeader = `<?xml version="1.0" encoding="UTF-8"?>` + "\n"

var (
	// textEscaper escapes character data: &, < and >, which also keeps "]]>" out, as entities, and CR, which parsers
	// would read as LF, as a character reference. Line breaks and tabs stay as they are.
	textEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\r", "&#xD;")
	// plainEscaper escapes plain text in RSS as the RSS Best Practices Profile advises: &, < and > as hexadecimal
	// character references, which more aggregators show as text than entities.
	plainEscaper = strings.NewReplacer("&", "&#x26;", "<", "&#x3C;", ">", "&#x3E;", "\r", "&#xD;")
	// attrEscaper escapes the value of an attribute in double quotes, also its tabs and line breaks, which parsers
	// would read as spaces.
	attrEscaper = strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;",
		"\t", "&#x9;", "\n", "&#xA;", "\r", "&#xD;",
	)
)

// xmlChars returns s with U+FFFD in place of each character that XML 1.0 does not allow: the C0 controls other than
// tab, LF and CR, U+FFFE and U+FFFF, and each byte of invalid UTF-8.
func xmlChars(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' || r == 0xFFFE || r == 0xFFFF {
			return utf8.RuneError
		}
		return r
	}, s)
}

// xmlWriter writes XML with one element on each line, without indentation; text with line breaks, such as HTML, goes
// on as many lines.
type xmlWriter struct {
	bytes.Buffer
}

// start writes the start tag of the element name with attrs, pairs of names and values, and a line break.
func (w *xmlWriter) start(name string, attrs ...string) {
	w.tag(name, attrs)
	w.WriteString(">\n")
}

// end writes the end tag of the element name and a line break.
func (w *xmlWriter) end(name string) {
	w.WriteString("</" + name + ">\n")
}

// empty writes the element name with attrs and no content, as <name a="v"/>, and a line break.
func (w *xmlWriter) empty(name string, attrs ...string) {
	w.tag(name, attrs)
	w.WriteString("/>\n")
}

// text writes the element name with attrs and the character data s, escaped by textEscaper, and a line break.
func (w *xmlWriter) text(name, s string, attrs ...string) {
	w.element(name, textEscaper.Replace(xmlChars(s)), attrs)
}

// plain writes the element name with attrs and s, plain text in RSS, escaped by plainEscaper, and a line break.
func (w *xmlWriter) plain(name, s string, attrs ...string) {
	w.element(name, plainEscaper.Replace(xmlChars(s)), attrs)
}

func (w *xmlWriter) element(name, escaped string, attrs []string) {
	w.tag(name, attrs)
	w.WriteString(">" + escaped + "</" + name + ">\n")
}

// tag writes the start of a tag: "<", name, and the attributes attrs, pairs of names and values.
func (w *xmlWriter) tag(name string, attrs []string) {
	w.WriteString("<" + name)
	for i := 0; i < len(attrs); i += 2 {
		w.WriteString(" " + attrs[i] + `="` + attrEscaper.Replace(xmlChars(attrs[i+1])) + `"`)
	}
}
