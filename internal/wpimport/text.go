package wpimport

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/html"
)

// parseIn parses s as the content of the HTML element called context, with the limits of internal/sanitize.
func parseIn(context, s string) ([]*html.Node, error) {
	n := min(len(s), (math.MaxInt-1<<16)/64)
	opts := html.Options{Scripting: true, MaxNodes: n + 1024, MaxSteps: 64*n + 1<<16}
	return html.ParseFragment(s, &html.Node{Type: html.ElementNode, Namespace: html.HTML, Name: context}, opts)
}

// Elements whose content is not text that people read: htmlText drops it with the element.
var droppedElements = map[string]bool{
	"script": true, "style": true, "template": true, "noscript": true, "iframe": true, "object": true, "embed": true,
	"video": true, "audio": true, "canvas": true,
}

// Elements that htmlText sets apart from the text around them: by an empty line, or by a line break.
var (
	paragraphElements = map[string]bool{
		"p": true, "blockquote": true, "pre": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true,
		"h6": true, "ul": true, "ol": true, "dl": true, "table": true, "hr": true, "figure": true, "details": true,
		"section": true, "article": true, "aside": true, "header": true, "footer": true, "main": true, "nav": true,
		"form": true, "fieldset": true, "address": true, "hgroup": true, "dialog": true,
	}
	lineElements = map[string]bool{
		"div": true, "li": true, "dt": true, "dd": true, "tr": true, "caption": true, "figcaption": true,
		"summary": true, "thead": true, "tbody": true, "tfoot": true,
	}
)

// htmlText returns the text of s, a piece of HTML such as the description of a term: the characters, and the line breaks
// of br elements and of the ends of paragraphs and the like. It also returns what it lost, each once and in the order of
// the HTML: the address of a link, an image, and the name of every element but p and br. The error is html.ErrLimit.
func htmlText(s string) (text string, lost []string, err error) {
	if !strings.ContainsAny(s, "<&") {
		return tidyText(s), nil, nil
	}
	nodes, err := parseIn("div", s)
	if err != nil {
		return "", nil, err
	}
	root := &html.Node{Type: html.DocumentFragmentNode}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	w := textWriter{seen: make(map[string]bool)}
	// Not recursive: HTML can nest as deep as it is long.
	for n := root.FirstChild; n != nil; {
		if w.enter(n) && n.FirstChild != nil {
			n = n.FirstChild
			continue
		}
		for n != nil {
			w.leave(n)
			if n.NextSibling != nil {
				n = n.NextSibling
				break
			}
			if n = n.Parent; n == root {
				n = nil
			}
		}
	}
	return tidyText(w.b.String()), w.lost, nil
}

type textWriter struct {
	b    strings.Builder
	nl   int // the line breaks at the end of b
	lost []string
	seen map[string]bool
}

func (w *textWriter) lose(what string) {
	if !w.seen[what] {
		w.seen[what] = true
		w.lost = append(w.lost, what)
	}
}

func (w *textWriter) write(s string) {
	w.b.WriteString(s)
	if rest := strings.TrimRight(s, "\n"); rest == "" {
		w.nl += len(s)
	} else {
		w.nl = len(s) - len(rest)
	}
}

// separate ends the text with n line breaks, unless it is empty.
func (w *textWriter) separate(n int) {
	if w.b.Len() == 0 {
		return
	}
	for w.nl < n {
		w.write("\n")
	}
}

// enter handles the start of n, and reports whether to go on to its children.
func (w *textWriter) enter(n *html.Node) bool {
	if n.Type == html.TextNode {
		w.write(n.Data)
		return false
	}
	if n.Type != html.ElementNode {
		// Comments, doctypes and processing instructions have no text to read.
		return false
	}
	if n.Namespace != html.HTML || droppedElements[n.Name] {
		w.lose(tagText(n.Name))
		return false
	}
	switch n.Name {
	case "br":
		w.write("\n")
		return false
	case "img":
		if src := strings.TrimSpace(attr(n, "src")); src != "" {
			w.lose("an image " + strconv.Quote(src))
		} else {
			w.lose("<img>")
		}
		return false
	case "a":
		if href := strings.TrimSpace(attr(n, "href")); href != "" {
			w.lose("a link to " + strconv.Quote(href))
		} else {
			w.lose("<a>")
		}
		return true
	case "p":
	default:
		w.lose(tagText(n.Name))
	}
	w.separate(breaks(n.Name))
	return true
}

func (w *textWriter) leave(n *html.Node) {
	if n.Type != html.ElementNode || n.Namespace != html.HTML || droppedElements[n.Name] {
		return
	}
	switch n.Name {
	case "td", "th":
		w.write(" ")
	default:
		w.separate(breaks(n.Name))
	}
}

// breaks returns the line breaks that set the element apart from the text around it.
func breaks(name string) int {
	switch {
	case paragraphElements[name]:
		return 2
	case lineElements[name]:
		return 1
	}
	return 0
}

func attr(n *html.Node, name string) string {
	for _, a := range n.Attrs {
		if a.Namespace == "" && a.Name == name {
			return a.Value
		}
	}
	return ""
}

// tagText writes the name of an element as <name>, quoted if it has other than ASCII letters, digits and "-", so that the
// report stays on one line.
func tagText(name string) string {
	for i := range len(name) {
		switch c := name[i]; {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '-':
		default:
			return "<" + strconv.Quote(name) + ">"
		}
	}
	return "<" + name + ">"
}

// tidyText makes the line breaks LF, removes the space and the tabs at the ends of the lines and of the text, and leaves
// at most one empty line in a row.
func tidyText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	out := lines[:0]
	empty := 0
	for _, l := range lines {
		l = strings.TrimRight(l, " \t")
		if l == "" {
			if empty++; empty > 1 {
				continue
			}
		} else {
			empty = 0
		}
		out = append(out, l)
	}
	return strings.Trim(strings.Join(out, "\n"), " \t\n")
}

const maxLostShown = 10

// lostMessage is the sentence of the report about what htmlText lost.
func lostMessage(lost []string) string {
	msg := "kept only the text of the HTML; lost " + strings.Join(lost[:min(len(lost), maxLostShown)], ", ")
	if len(lost) > maxLostShown {
		msg += fmt.Sprintf(", and %d more", len(lost)-maxLostShown)
	}
	return msg
}

// decodeRefs undoes the character references of s, a name that WordPress keeps escaped, such as News &amp; Events, and
// leaves the rest as it is, "<" too.
func decodeRefs(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	// What a title element holds is text with character references, so "<" starts no tag in it.
	nodes, err := parseIn("title", s)
	if err != nil {
		return s
	}
	var b strings.Builder
	for _, n := range nodes {
		if n.Type != html.TextNode {
			return s
		}
		b.WriteString(n.Data)
	}
	return b.String()
}

// oneLine returns s with each line break and tab, as a line of content cannot have them, made a space, and without the
// space at its ends. It reports whether it replaced one.
func oneLine(s string) (string, bool) {
	replaced := false
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t', '\v', '\f', 0x85, 0x2028, 0x2029:
			replaced = true
			return ' '
		}
		return r
	}, s)
	return strings.Trim(s, " "), replaced
}
