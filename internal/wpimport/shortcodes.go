package wpimport

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/html"
	"github.com/WhiteRoomLab/whiteroom/internal/wpformat"
)

// registeredShortcodes are the names that WordPress and Contact Form 7 register: WordPress finds them by the rule of its
// regular expression, whatever follows the name.
var registeredShortcodes = map[string]bool{
	"audio": true, "caption": true, "contact-form": true, "contact-form-7": true, "embed": true, "gallery": true,
	"playlist": true, "video": true, "wp_caption": true,
}

// isShortcodeName reports whether name, the run of letters, digits, "_" and "-" after a "[" that FindShortcodes read, with
// the byte next after it, is a shortcode. WordPress does not know the names that plugins register, so any other name is a
// shortcode if it is made of lower case letters, digits, "_" and "-", starts with a letter, and is followed by white space, "]",
// "/" or "<" (wpautop puts a br before a line break in a tag): [PR], [1] and [a.b] are text.
func isShortcodeName(name string, next byte) bool {
	if registeredShortcodes[name] {
		return true
	}
	if name == "" || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for i := 1; i < len(name); i++ {
		switch c := name[i]; {
		case 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	switch next {
	case ' ', '\t', '\n', '\v', '\f', '\r', ']', '/', '<':
		return true
	}
	return false
}

type pieceKind uint8

const (
	pieceShortcode pieceKind = iota // a shortcode
	pieceEmbedURL                   // a URL alone on a line that a provider embeds
	pieceTextURL                    // a URL alone on a line that none embeds
)

// classicPiece is a shortcode or a URL in the HTML of the classic editor. While the HTML is read, an element stands for it.
type classicPiece struct {
	kind pieceKind
	name string // of the element that stands for it
	text string // what the element replaced, as written
	sc   wpformat.Shortcode
	// url is the address of a URL, with its character references undone; for one that a provider embeds, the address to embed,
	// and provider is the provider.
	url, provider string
}

func (pc *classicPiece) what() string {
	if pc.kind == pieceShortcode {
		return "the shortcode [" + pc.sc.Name + "]"
	}
	return "the URL"
}

const (
	pieceBase   = "x-wr-piece"
	pieceMarker = "<" + pieceBase
	// pieceNames is how many names the elements that stand for pieces have to choose from.
	pieceNames = 10000
)

const reasonCutPieces = "the shortcodes and URLs after the first %d are left as text"

// pieceNameFor returns the name of the elements that stand for the pieces of text, which is no element of text: it is
// x-wr-piece, or x-wr-piece-2 and the like if text has an element that starts with that name. It returns "" if text has
// elements of all the names.
func pieceNameFor(text string) string {
	var taken [pieceNames]bool
	baseTaken := false
	for i := 0; ; {
		k := strings.IndexByte(text[i:], '<')
		if k < 0 {
			break
		}
		i += k + 1
		if !hasPrefixFold(text[i:], pieceBase) {
			continue
		}
		baseTaken = true
		digits, ok := strings.CutPrefix(text[i+len(pieceBase):], "-")
		if !ok || digits == "" || digits[0] < '1' || digits[0] > '9' {
			continue
		}
		n := 0
		for j := 0; j < len(digits) && j < 4 && '0' <= digits[j] && digits[j] <= '9'; j++ {
			n = n*10 + int(digits[j]-'0')
			taken[n] = true
		}
	}
	if !baseTaken {
		return pieceBase
	}
	for n := 2; n < pieceNames; n++ {
		if !taken[n] {
			return pieceBase + "-" + strconv.Itoa(n)
		}
	}
	return ""
}

// hasPrefixFold reports whether s starts with prefix, a lower case name, in any case of ASCII letters.
func hasPrefixFold(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := range len(prefix) {
		c := s[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != prefix[i] {
			return false
		}
	}
	return true
}

// pieceNameOf chooses the name of the elements for the source st, once, and makes it the name of the conversion. It
// reports whether there is one.
func (c *bodyConv) pieceNameOf(st *classicState) bool {
	if !st.named {
		st.named = true
		if st.name = pieceNameFor(st.text); st.name == "" {
			c.note(noteAt{}, Review, "the shortcodes and URLs of %s are left as text: it has elements of all the names that stand for them", st.what)
		}
	}
	c.pieceName = st.name
	return st.name != ""
}

// pieceFilter is the isName that FindShortcodesAt is given. It takes the names that are shortcodes, only is one name if it is
// not empty, until the conversion has made as many pieces as it may. In code, which the walker code tells, only the names that
// WordPress registers are shortcodes: any other bracket is the code's own, and neither becomes a piece nor counts.
func (c *bodyConv) pieceFilter(only string, code *placeWalker) func(int, string, byte) bool {
	return func(at int, name string, next byte) bool {
		if only != "" && name != only || !isShortcodeName(name, next) {
			return false
		}
		if !registeredShortcodes[name] && code.inCode(at) {
			return false
		}
		if c.pieceLeft <= 0 {
			c.piecesCut = true
			return false
		}
		c.pieceLeft--
		return true
	}
}

// textShortcodeFilter is pieceFilter for a text that is read once and not kept: it limits only the shortcodes of one call.
func textShortcodeFilter() func(string, byte) bool {
	left := maxWPBlocks
	return func(name string, next byte) bool {
		if left <= 0 || !isShortcodeName(name, next) {
			return false
		}
		left--
		return true
	}
}

// registeredOnly limits isName to the names that WordPress registers, as in code.
func registeredOnly(isName func(string, byte) bool) func(string, byte) bool {
	return func(name string, next byte) bool { return registeredShortcodes[name] && isName(name, next) }
}

// findTextShortcodes is FindShortcodes for s, text that has no markup: a "<" in it is a character, which a character
// reference may have written, and makes no element that hides the brackets after it. The Attrs and the Content of what it
// finds have another byte in place of each "<".
func findTextShortcodes(s string, isName func(string, byte) bool) []wpformat.Shortcode {
	if strings.Contains(s, "<") {
		s = strings.ReplaceAll(s, "<", "\x01")
	}
	return wpformat.FindShortcodes(s, isName)
}

func (c *bodyConv) placeholder(i int) string {
	return "<" + c.pieceName + ` data-n="` + strconv.Itoa(i) + `"></` + c.pieceName + ">"
}

// pieceOf returns the piece that the element n stands for, or nil if n is no such element.
func (c *bodyConv) pieceOf(n *html.Node) *classicPiece {
	if c.pieceName == "" || !isElement(n, c.pieceName) {
		return nil
	}
	i, err := strconv.Atoi(attr(n, "data-n"))
	if err != nil || i < 0 || i >= len(c.pieces) || c.pieces[i].name != c.pieceName {
		return nil
	}
	return c.pieces[i]
}

// restorePieces puts the text of the pieces back where s has the elements that stand for them.
func (c *bodyConv) restorePieces(s string) string {
	if len(c.pieces) == 0 || !strings.Contains(s, pieceMarker) {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, pieceMarker)
		if i < 0 {
			break
		}
		b.WriteString(s[:i])
		s = s[i:]
		if pc, n := c.pieceAt(s); pc != nil {
			b.WriteString(pc.text)
			s = s[n:]
		} else {
			b.WriteByte('<')
			s = s[1:]
		}
	}
	b.WriteString(s)
	return b.String()
}

// pieceAt reads the element that stands for a piece at the start of s, and returns the piece and the length of the element.
func (c *bodyConv) pieceAt(s string) (*classicPiece, int) {
	name := pieceBase
	rest := s[len(pieceMarker):]
	if tail, ok := strings.CutPrefix(rest, "-"); ok {
		j := 0
		for j < len(tail) && '0' <= tail[j] && tail[j] <= '9' {
			j++
		}
		name += "-" + tail[:j]
		rest = tail[j:]
	}
	rest, ok := strings.CutPrefix(rest, ` data-n="`)
	if !ok {
		return nil, 0
	}
	j := 0
	for j < len(rest) && '0' <= rest[j] && rest[j] <= '9' {
		j++
	}
	i, err := strconv.Atoi(rest[:j])
	if err != nil || i >= len(c.pieces) || c.pieces[i].name != name {
		return nil, 0
	}
	tail := `"></` + name + ">"
	if !strings.HasPrefix(rest[j:], tail) {
		return nil, 0
	}
	return c.pieces[i], len(pieceMarker) + len(name) - len(pieceBase) + len(` data-n="`) + j + len(tail)
}

// strayPiece reports whether part, the HTML that the tree root was read from, has an element that stands for a piece that is
// text or the value of an attribute in the tree: the parser took the start of the element for a part of a tag, or of the text
// of an element that it reads as it is. The positions of those are not where WordPress cuts the HTML into elements.
func (c *bodyConv) strayPiece(part string, root *html.Node) bool {
	marker := "<" + c.pieceName + " "
	if c.pieceName == "" || !strings.Contains(part, marker) {
		return false
	}
	for d := root.FirstChild; d != nil; d = after(root, d, true) {
		switch d.Type {
		case html.TextNode, html.CommentNode:
			if strings.Contains(d.Data, marker) {
				return true
			}
		case html.ElementNode:
			for _, a := range d.Attrs {
				if strings.Contains(a.Value, marker) {
					return true
				}
			}
		case html.DocumentNode, html.DocumentFragmentNode, html.DoctypeNode, html.ProcessingInstructionNode:
		}
	}
	return false
}

// own returns a copy of s, which text of the classic editor holds, that is not tied to it, with the pieces that s has put back.
func (c *bodyConv) own(s string) string {
	if strings.Contains(s, pieceMarker) {
		return c.restorePieces(s)
	}
	return strings.Clone(s)
}

// A placeWalker tells whether a position in a text is where no element that stands for a piece can go: in a tag, a comment
// or a CDATA section as wp_html_split of WordPress cuts the text, or in the text of an element that the HTML parser reads as
// it is. It also tells whether a position is in code, between the start tag of a pre or code element and its end tag. The
// positions that it is asked about do not decrease.
type placeWalker struct {
	s          string
	start, end int
	next       int
	// depth is the pre and code elements that are open after the tags that end before the position asked about; delta is what the
	// tag at start..end changes it by.
	depth, delta int
}

// advance moves to the first tag that does not end before p, or the last.
func (w *placeWalker) advance(p int) {
	for w.end <= p && w.next < len(w.s) {
		w.depth, w.delta = max(0, w.depth+w.delta), 0
		k := strings.IndexByte(w.s[w.next:], '<')
		if k < 0 {
			w.next = len(w.s)
			break
		}
		start := w.next + k
		end := wpformat.ElementEnd(w.s, start)
		if rest := w.s[start+1:]; rest != "" && (isASCIILetter(rest[0]) || len(rest) > 1 && rest[0] == '/' && isASCIILetter(rest[1])) {
			// The parser ends a tag at a ">" that is not in a quoted value; WordPress at any ">".
			end = max(end, startTagEnd(w.s, start))
		}
		w.start, w.end, w.next = start, end, end
		w.delta = codeDelta(w.s[start:end])
		if name := rawTextName(w.s[start:end]); name != "" {
			w.end = rawTextEnd(w.s, end, name)
			w.next = w.end
		}
	}
}

func (w *placeWalker) blocked(p int) bool {
	w.advance(p)
	return w.start <= p && p < w.end
}

// inCode reports whether p is in a pre or code element.
func (w *placeWalker) inCode(p int) bool {
	w.advance(p)
	return w.depth > 0
}

// codeDelta is 1 for the start tag of a pre or code element, -1 for its end tag, and 0 for the other tags.
func codeDelta(tag string) int {
	delta := 1
	rest := tag[1:]
	if strings.HasPrefix(rest, "/") {
		delta, rest = -1, rest[1:]
	}
	for _, name := range [...]string{"pre", "code"} {
		if !hasPrefixFold(rest, name) {
			continue
		}
		if after := rest[len(name):]; after == "" || isTagSpace(after[0]) || after[0] == '/' || after[0] == '>' {
			return delta
		}
	}
	return 0
}

func isASCIILetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

func isTagSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r' }

// startTagEnd returns the end of the start tag that starts at the "<" at s[i], as the HTML parser reads it, where a quoted
// value of an attribute can hold a ">", or the end of s.
func startTagEnd(s string, i int) int {
	i++
	for i < len(s) && !isTagSpace(s[i]) && s[i] != '/' && s[i] != '>' {
		i++
	}
	for i < len(s) {
		for i < len(s) && (isTagSpace(s[i]) || s[i] == '/') {
			i++
		}
		if i >= len(s) {
			break
		}
		if s[i] == '>' {
			return i + 1
		}
		// The name of an attribute; a "=" at its start is part of it.
		i++
		for i < len(s) && !isTagSpace(s[i]) && s[i] != '/' && s[i] != '>' && s[i] != '=' {
			i++
		}
		for i < len(s) && isTagSpace(s[i]) {
			i++
		}
		if i >= len(s) || s[i] != '=' {
			continue
		}
		for i++; i < len(s) && isTagSpace(s[i]); i++ {
		}
		if i >= len(s) {
			break
		}
		if q := s[i]; q == '"' || q == '\'' {
			k := strings.IndexByte(s[i+1:], q)
			if k < 0 {
				return len(s)
			}
			i += k + 2
			continue
		}
		for i < len(s) && !isTagSpace(s[i]) && s[i] != '>' {
			i++
		}
	}
	return len(s)
}

// rawTextElements are the elements whose text the HTML parser reads as it is.
var rawTextElements = map[string]bool{
	"script": true, "style": true, "textarea": true, "title": true, "xmp": true, "iframe": true, "noembed": true,
	"noframes": true, "noscript": true, "plaintext": true,
}

// rawTextName returns the name of the start tag, if it is the tag of an element whose text is read as it is, and "" if not.
func rawTextName(tag string) string {
	if len(tag) < 3 || !isASCIILetter(tag[1]) {
		return ""
	}
	j := 1
	for j < len(tag) && (isASCIILetter(tag[j]) || '0' <= tag[j] && tag[j] <= '9') {
		j++
	}
	if j < len(tag) {
		switch tag[j] {
		case ' ', '\t', '\n', '\f', '\r', '/', '>':
		default:
			return ""
		}
	}
	if name := strings.ToLower(tag[1:j]); rawTextElements[name] {
		return name
	}
	return ""
}

// rawTextEnd returns the start of the end tag of the element name whose text starts at from, or the end of s.
func rawTextEnd(s string, from int, name string) int {
	if name == "plaintext" {
		return len(s)
	}
	for i := from; ; i++ {
		k := strings.IndexByte(s[i:], '<')
		if k < 0 {
			return len(s)
		}
		i += k
		if i+1 < len(s) && s[i+1] == '/' && hasPrefixFold(s[i+2:], name) {
			switch j := i + 2 + len(name); {
			case j >= len(s), s[j] == ' ', s[j] == '\t', s[j] == '\n', s[j] == '\f', s[j] == '\r', s[j] == '/', s[j] == '>':
				return i
			}
		}
	}
}

// A textEdit replaces the stretch of a text with an element that stands for the piece, or with text if piece is -1.
type textEdit struct {
	start, end int
	piece      int
	text       string
}

// applyEdits makes the text from s by the edits, which are in order and do not overlap.
func (c *bodyConv) applyEdits(s string, edits []textEdit) string {
	if len(edits) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + len(edits)*(len(c.placeholder(0))+8))
	pos := 0
	for _, e := range edits {
		b.WriteString(s[pos:e.start])
		if e.piece >= 0 {
			b.WriteString(c.placeholder(e.piece))
		} else {
			b.WriteString(e.text)
		}
		pos = e.end
	}
	b.WriteString(s[pos:])
	return b.String()
}

// embedPieces puts elements in the place of the shortcodes [embed] and of the URLs that the automatic embeds of WordPress try
// in the page, as WordPress finds them before it makes the paragraphs.
func (c *bodyConv) embedPieces(st *classicState, page string) string {
	hasEmbed := strings.Contains(page, "[embed")
	if !hasEmbed && !strings.Contains(page, "://") {
		return page
	}
	if !c.pieceNameOf(st) {
		return page
	}
	if hasEmbed {
		page = c.replaceShortcodes(page, "embed")
	}
	return c.replaceURLs(page)
}

// shortcodePieces puts elements in the place of the shortcodes of s, the HTML after WordPress has made the paragraphs, and
// makes the text of the escaped ones ([[foo]]) the text of the shortcode.
func (c *bodyConv) shortcodePieces(st *classicState, s string) string {
	if !strings.Contains(s, "[") || !c.pieceNameOf(st) {
		return s
	}
	return c.replaceShortcodes(s, "")
}

// replaceShortcodes replaces the shortcodes of s that pieceFilter(only) takes.
func (c *bodyConv) replaceShortcodes(s, only string) string {
	found := wpformat.FindShortcodesAt(s, c.pieceFilter(only, &placeWalker{s: s}))
	if len(found) == 0 {
		return s
	}
	var edits []textEdit
	walker := placeWalker{s: s}
	for _, sc := range found {
		start, end := sc.Start, sc.End
		if sc.Open && sc.Close {
			// [[foo]] is the text [foo]; the pass for [embed] leaves it to the pass for all.
			if only == "" && !walker.blocked(start) {
				edits = append(edits, textEdit{start: start, end: end, piece: -1, text: s[start+1 : end-1]})
			}
			continue
		}
		// [[foo] and [foo]] are the text [ or ] and the shortcode.
		if sc.Open {
			start++
		}
		if sc.Close {
			end--
		}
		if walker.blocked(start) {
			continue
		}
		pc := &classicPiece{kind: pieceShortcode, name: c.pieceName, text: c.own(s[start:end]), sc: sc}
		pc.sc.Name, pc.sc.Attrs, pc.sc.Content = strings.Clone(sc.Name), c.own(sc.Attrs), c.own(sc.Content)
		c.pieces = append(c.pieces, pc)
		edits = append(edits, textEdit{start: start, end: end, piece: len(c.pieces) - 1})
	}
	return c.applyEdits(s, edits)
}

// replaceURLs replaces the URLs of s that WordPress tries to embed with elements: those that a provider embeds, and those
// that none embeds, which WordPress shows as text.
func (c *bodyConv) replaceURLs(s string) string {
	spans := wpformat.AutoembedURLs(s)
	if len(spans) == 0 {
		return s
	}
	var edits []textEdit
	walker := placeWalker{s: s}
	for _, sp := range spans {
		if walker.blocked(sp.Start) {
			continue
		}
		if c.pieceLeft <= 0 {
			c.piecesCut = true
			break
		}
		c.pieceLeft--
		raw := s[sp.Start:sp.End]
		url := decodeRefs(raw)
		pc := &classicPiece{kind: pieceTextURL, name: c.pieceName, text: strings.Clone(raw), url: strings.Clone(url)}
		if provider, href, err := embedOfURL(url); err == nil {
			pc.kind, pc.provider, pc.url = pieceEmbedURL, provider, href
		}
		c.pieces = append(c.pieces, pc)
		edits = append(edits, textEdit{start: sp.Start, end: sp.End, piece: len(c.pieces) - 1})
	}
	return c.applyEdits(s, edits)
}

// urlProviders are the providers that embed the address of a URL on a line of its own, in the order they are tried.
var urlProviders = []string{"youtube", "vimeo", "x"}

// embedOfURL returns the provider that embeds the address raw, and the address as it is embedded: WordPress embeds the http
// address of a provider too, so an address that starts with http:// (in any case) is looked for, and returned, as an https
// address. The error is a legacy error, and says why no provider does.
func embedOfURL(raw string) (provider, href string, err error) {
	href = content.NormalizeHref(raw)
	if href == "" {
		return "", "", legacyf("the embed has no URL")
	}
	if err := content.CheckHref(href); err != nil {
		return "", "", legacyf("the URL cannot be embedded: %s", err)
	}
	href = httpsOf(href)
	for _, name := range urlProviders {
		if _, err := content.ParseEmbedURL(name, href); err == nil {
			return name, href, nil
		}
	}
	return "", "", legacyf("the URL cannot be embedded: %s", reasonEmbedURL)
}

const urlRecord = "the URL %q on its own line is text here; WordPress may have shown it as an embed, and whiteroom embeds only YouTube, Vimeo and X"

// reasonShortcodes is the reason for a legacy block of text that has the shortcodes names, each as [name].
func reasonShortcodes(names []string) string {
	shown := names[:min(len(names), maxLostShown)]
	list := strings.Join(shown, ", ")
	if more := len(names) - len(shown); more > 0 {
		list += fmt.Sprintf(", and %d more", more)
	}
	if len(names) == 1 {
		return "the text has the shortcode " + list + ", which whiteroom does not run"
	}
	return "the text has the shortcodes " + list + ", which whiteroom does not run"
}
