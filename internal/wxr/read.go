package wxr

import (
	"cmp"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
)

const (
	wxrNS     = "http://wordpress.org/export/"
	contentNS = "http://purl.org/rss/1.0/modules/content/"
	dcNS      = "http://purl.org/dc/elements/1.1/"

	xmlSpace = " \t\r\n"
)

type nsKind uint8

const (
	nsOther   nsKind = iota // a namespace that the file does not use for WordPress
	nsRSS                   // none
	nsWP                    // wp:
	nsExcerpt               // excerpt:
	nsContent               // content:
	nsDC                    // dc:
)

// qname is the name of an element by what it means, not by its prefix, which the file chooses.
type qname struct {
	ns    nsKind
	local string
}

func rss(local string) qname     { return qname{nsRSS, local} }
func wp(local string) qname      { return qname{nsWP, local} }
func excerpt(local string) qname { return qname{nsExcerpt, local} }
func content(local string) qname { return qname{nsContent, local} }
func dc(local string) qname      { return qname{nsDC, local} }

func qnameOf(n xml.Name) qname {
	switch n.Space {
	case "":
		return rss(n.Local)
	case wxrNS + "1.0/", wxrNS + "1.1/", wxrNS + "1.2/":
		return wp(n.Local)
	case wxrNS + "1.0/excerpt/", wxrNS + "1.1/excerpt/", wxrNS + "1.2/excerpt/":
		return excerpt(n.Local)
	case contentNS:
		return content(n.Local)
	case dcNS:
		return dc(n.Local)
	}
	return qname{nsOther, n.Local}
}

func (q qname) String() string {
	switch q.ns {
	case nsWP:
		return "wp:" + q.local
	case nsExcerpt:
		return "excerpt:" + q.local
	case nsContent:
		return "content:" + q.local
	case nsDC:
		return "dc:" + q.local
	case nsOther, nsRSS:
	}
	return q.local
}

// displayName returns the name for a message.
func displayName(n xml.Name) string {
	if q := qnameOf(n); q.ns != nsOther {
		return q.String()
	}
	return "{" + n.Space + "}" + n.Local
}

// removals are the characters removed from a record.
type removals struct {
	list []removedChar // the first maxListed
	n    int
}

func (r *removals) reset() {
	r.list = r.list[:0]
	r.n = 0
}

func (r *removals) add(list []removedChar, n int) {
	r.list = append(r.list, list[:min(len(list), maxListed-len(r.list))]...)
	r.n += n
}

func (r *removals) note(kind RecordKind, index int) Note {
	noun := "characters"
	if r.n == 1 {
		noun = "character"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "removed %d %s that XML does not allow: ", r.n, noun)
	for i, c := range r.list {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "U+%04X at %d:%d", c.r, c.pos.Line, c.pos.Column)
	}
	if more := r.n - len(r.list); more > 0 {
		fmt.Fprintf(&sb, ", and %d more", more)
	}
	return Note{Pos: r.list[0].pos, Record: kind, Index: index, Msg: sb.String()}
}

// child is the start tag of an element in a record.
type child struct {
	name  qname
	start xml.StartElement
	pos   Pos
}

type parser struct {
	ctx  context.Context
	opts Options
	in   *input
	d    *xml.Decoder
	ex   *Export

	ntok   int
	depth  int
	tokPos Pos // where the token that the decoder is reading begins

	// The characters that the input removed before the last token and in it, and where the removed characters that
	// are in values go: to the record that is open, or to the channel.
	gap    []removedChar
	gapN   int
	carry  []removedChar
	carryN int
	rem    []removedChar
	remN   int
	acc    *removals
	chAcc  removals
	recAcc removals

	// The children that the open records have read, from scope.base, for finding the duplicates.
	seen  []qname
	scope struct {
		parent qname
		base   int
	}
	buf []byte
}

// Read reads the WXR in r. It stops at the first problem in the file, which it returns as an *Error, without the part
// that it has read. The other errors are those of r and of ctx.
func Read(ctx context.Context, r io.Reader, opts Options) (*Export, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	o := opts.withDefaults()
	p := &parser{
		ctx:  ctx,
		opts: o,
		in:   newInput(r, o),
		ex: &Export{
			postIdx:   make(map[int64]int),
			termIdx:   make(map[termKey]int),
			slugIdx:   make(map[termKey]int),
			authorIdx: make(map[string]int),
		},
	}
	p.d = xml.NewDecoder(p.in)
	p.d.CharsetReader = p.charset
	if err := p.document(); err != nil {
		return nil, err
	}
	slices.SortStableFunc(p.ex.Notes, func(a, b Note) int {
		return cmp.Or(cmp.Compare(a.Line, b.Line), cmp.Compare(a.Column, b.Column))
	})
	return p.ex, nil
}

// charset accepts the encodings that are UTF-8, which encoding/xml does not know by these names.
func (p *parser) charset(label string, r io.Reader) (io.Reader, error) {
	if strings.EqualFold(label, "utf8") || strings.EqualFold(label, "us-ascii") {
		return r, nil
	}
	return nil, &Error{p.tokPos, fmt.Sprintf("unsupported encoding %q; convert the WXR to UTF-8", label)}
}

// next returns the next token and where it begins. It returns io.EOF at the end of the file after the root element.
func (p *parser) next() (xml.Token, Pos, error) {
	if p.ntok++; p.ntok%1024 == 0 {
		if err := p.ctx.Err(); err != nil {
			return nil, Pos{}, err
		}
	}
	// The characters removed before the next byte, which is where the token begins, come first.
	off := p.d.InputOffset()
	pos := p.in.pos(off)
	list, n := p.in.drain()
	p.gap = append(append(p.gap[:0], p.carry...), list...)
	p.gap = p.gap[:min(len(p.gap), maxListed)]
	p.gapN = p.carryN + n
	p.carry, p.carryN = p.carry[:0], 0
	p.tokPos = pos
	tok, err := p.d.Token()
	list, n = p.in.drain()
	p.rem, p.remN = append(p.rem[:0], list...), n
	if err != nil {
		return nil, pos, p.fail(err)
	}
	if _, ok := tok.(xml.EndElement); ok && p.d.InputOffset() == off {
		// The end of an element such as <a/>, which reads nothing, so what was removed before it is for the next token.
		p.carry, p.carryN = append(p.carry[:0], p.gap...), p.gapN
		p.gap, p.gapN = p.gap[:0], 0
	}
	switch t := tok.(type) {
	case xml.CharData:
		// Where the text that is not white space begins, which is what a problem with the text is about.
		if !blank(t) {
			pos = p.in.textPos
		}
	case xml.StartElement:
		if p.depth++; p.depth > p.opts.MaxDepth {
			return nil, pos, &Error{pos, fmt.Sprintf("elements are nested too deeply: at least %d levels (max %d)",
				p.opts.MaxDepth+1, p.opts.MaxDepth)}
		}
		if prefix, ok := undeclared(t); ok {
			return nil, pos, &Error{pos, fmt.Sprintf("undeclared namespace prefix %q", prefix)}
		}
	case xml.EndElement:
		p.depth--
	}
	return tok, pos, nil
}

// undeclared returns the prefix that the start tag uses without declaring it, which encoding/xml puts into Space.
func undeclared(t xml.StartElement) (string, bool) {
	undeclared := func(space string) bool { return space != "" && !strings.Contains(space, ":") }
	if undeclared(t.Name.Space) {
		return t.Name.Space, true
	}
	for _, a := range t.Attr {
		if a.Name.Space != "xmlns" && undeclared(a.Name.Space) {
			return a.Name.Space, true
		}
	}
	return "", false
}

// fail returns the error of the decoder as the error of Read.
func (p *parser) fail(err error) error {
	var e *Error
	var re *readError
	var se *xml.SyntaxError
	switch {
	case errors.Is(err, io.EOF) && p.depth == 0:
		return io.EOF
	case errors.As(err, &e):
		return e
	case errors.As(err, &re):
		return fmt.Errorf("reading the WXR: %w", re.err)
	case errors.As(err, &se):
		return &Error{p.in.pos(p.d.InputOffset()), "invalid XML: " + se.Msg}
	}
	return &Error{p.tokPos, "invalid XML: " + strings.TrimPrefix(err.Error(), "xml: ")}
}

// commit puts the characters that were removed before the last token, if gap, and in it, if tok, into the open record.
// It is for the text of a value and for the attributes, not for what is skipped or in a tag.
func (p *parser) commit(gap, tok bool) {
	if p.acc == nil {
		return
	}
	if gap && p.gapN > 0 {
		p.acc.add(p.gap, p.gapN)
	}
	if tok && p.remN > 0 {
		p.acc.add(p.rem, p.remN)
	}
}

func blank[T ~string | ~[]byte](v T) bool {
	for i := 0; i < len(v); i++ {
		if !isSpace(v[i]) {
			return false
		}
	}
	return true
}

func doctype(pos Pos) *Error {
	return &Error{pos, "DOCTYPE is not allowed"}
}

func outsideText(pos Pos) *Error {
	return &Error{pos, "unexpected text outside <rss>; WordPress may have written a PHP message into the export"}
}

func (p *parser) document() error {
	for {
		tok, pos, err := p.next()
		if errors.Is(err, io.EOF) {
			return &Error{pos, "not a WXR file: missing <rss>"}
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.CharData:
			if !blank(t) {
				return outsideText(pos)
			}
		case xml.Comment:
			p.generator(t)
		case xml.Directive:
			return doctype(pos)
		case xml.StartElement:
			if qnameOf(t.Name) != rss("rss") {
				return &Error{pos, fmt.Sprintf("not a WXR file: the root element is <%s>, not <rss>", displayName(t.Name))}
			}
			if err := p.rss(); err != nil {
				return err
			}
			return p.epilog()
		case xml.ProcInst, xml.EndElement:
		}
	}
}

// generator reads the version of WordPress from the comment before the root element, such as generator="WordPress/6.8.3"
// (WordPress 3.0 wrote wordpress in lower case).
func (p *parser) generator(c xml.Comment) {
	const attr, product = `generator="`, "wordpress/"
	_, rest, ok := strings.Cut(string(c), attr)
	if !ok || p.ex.WordPress != "" || len(rest) < len(product) || !strings.EqualFold(rest[:len(product)], product) {
		return
	}
	if v, _, ok := strings.Cut(rest[len(product):], `"`); ok {
		p.ex.WordPress = v
	}
}

func (p *parser) epilog() error {
	for {
		tok, pos, err := p.next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.CharData:
			if !blank(t) {
				return outsideText(pos)
			}
		case xml.Directive:
			return doctype(pos)
		case xml.StartElement:
			return &Error{pos, fmt.Sprintf("unexpected element <%s> after </rss>", displayName(t.Name))}
		case xml.Comment, xml.ProcInst, xml.EndElement:
		}
	}
}

// children reads the elements in the one that is open, through its end tag, and calls fn with the start tag of each,
// which must read the element through its end tag. The text between the elements must be blank. It returns where the
// end tag begins.
func (p *parser) children(parent qname, fn func(c child) error) (Pos, error) {
	outer, base := p.scope, len(p.seen)
	p.scope.parent, p.scope.base = parent, base
	defer func() {
		p.scope = outer
		p.seen = p.seen[:base]
	}()
	for {
		tok, pos, err := p.next()
		if err != nil {
			return pos, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if err := fn(child{qnameOf(t.Name), t, pos}); err != nil {
				return pos, err
			}
		case xml.CharData:
			if !blank(t) {
				return pos, &Error{pos, fmt.Sprintf("unexpected text in <%s>", parent)}
			}
		case xml.Directive:
			return pos, doctype(pos)
		case xml.EndElement:
			return pos, nil
		case xml.Comment, xml.ProcInst:
		}
	}
}

// skip reads the element that is open through its end tag, without looking at what is in it.
func (p *parser) skip() error {
	for depth := 1; depth > 0; {
		tok, pos, err := p.next()
		if err != nil {
			return err
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		case xml.Directive:
			return doctype(pos)
		case xml.CharData, xml.Comment, xml.ProcInst:
		}
	}
	return nil
}

// value reads the text of the element that is open, through its end tag, as it is written. name is the element.
func (p *parser) value(name qname) (string, error) {
	p.buf = p.buf[:0]
	for {
		tok, pos, err := p.next()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			p.buf = append(p.buf, t...)
			p.commit(true, true)
		case xml.StartElement:
			return "", &Error{pos, fmt.Sprintf("unexpected element <%s> in <%s>", displayName(t.Name), name)}
		case xml.Directive:
			return "", doctype(pos)
		case xml.EndElement:
			// A value that is only removed characters has no text, so they come before the end tag.
			p.commit(true, false)
			s := string(p.buf)
			if cap(p.buf) > 1<<20 {
				p.buf = nil
			}
			return s, nil
		case xml.Comment, xml.ProcInst:
			p.commit(true, false)
		}
	}
}

// once checks that the record that is open has not read c before.
func (p *parser) once(c child) error {
	if slices.Contains(p.seen[p.scope.base:], c.name) {
		return &Error{c.pos, fmt.Sprintf("duplicate <%s> in <%s>", c.name, p.scope.parent)}
	}
	p.seen = append(p.seen, c.name)
	return nil
}

// text reads the value of c into dst.
func (p *parser) text(c child, dst *string) error {
	if err := p.once(c); err != nil {
		return err
	}
	v, err := p.value(c.name)
	if err != nil {
		return err
	}
	*dst = v
	return nil
}

// integer reads the value of c, which must be an integer of at least lo.
func (p *parser) integer(c child, lo int64) (int64, error) {
	if err := p.once(c); err != nil {
		return 0, err
	}
	v, err := p.value(c.name)
	if err != nil {
		return 0, err
	}
	s := strings.Trim(v, xmlSpace)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < lo {
		use := "use an integer"
		if lo != math.MinInt64 {
			use += fmt.Sprintf(" from %d", lo)
		}
		return 0, &Error{c.pos, fmt.Sprintf("invalid <%s> %q: %s", c.name, s, use)}
	}
	return n, nil
}

// missing returns the error for a record that lacks the required child name.
func missing(pos Pos, name, parent qname) *Error {
	return &Error{pos, fmt.Sprintf("missing <%s> in <%s>", name, parent)}
}

func (p *parser) rss() error {
	found := false
	end, err := p.children(rss("rss"), func(c child) error {
		if c.name != rss("channel") {
			return p.skip()
		}
		if found {
			return &Error{c.pos, "more than one <channel> in <rss>"}
		}
		found = true
		return p.channel(c)
	})
	if err != nil {
		return err
	}
	if !found {
		return &Error{end, "not a WXR file: missing <channel> in <rss>"}
	}
	return nil
}

func (p *parser) channel(c child) error {
	ex := p.ex
	ch := &ex.Channel
	ch.Pos = c.pos
	p.acc = &p.chAcc
	end, err := p.children(c.name, func(cc child) error {
		switch cc.name {
		case wp("author"), wp("category"), wp("tag"), wp("term"), rss("item"):
			if ex.Version == "" {
				return &Error{cc.pos, fmt.Sprintf("not a WXR file: missing wp:wxr_version before <%s>", cc.name)}
			}
		}
		switch cc.name {
		case rss("title"):
			return p.text(cc, &ch.Title)
		case rss("link"):
			return p.text(cc, &ch.Link)
		case rss("description"):
			return p.text(cc, &ch.Description)
		case rss("pubDate"):
			return p.text(cc, &ch.PubDate)
		case rss("language"):
			return p.text(cc, &ch.Language)
		case rss("generator"):
			return p.generatorElement(cc)
		case wp("base_site_url"):
			return p.text(cc, &ch.BaseSiteURL)
		case wp("base_blog_url"):
			return p.text(cc, &ch.BaseBlogURL)
		case wp("wxr_version"):
			return p.version(cc)
		case wp("author"):
			return p.author(cc)
		case wp("category"):
			return p.term(cc, categoryForm)
		case wp("tag"):
			return p.term(cc, tagForm)
		case wp("term"):
			return p.term(cc, customForm)
		case rss("item"):
			return p.item(cc)
		}
		return p.skip()
	})
	if err != nil {
		return err
	}
	if ex.Version == "" {
		return &Error{end, "not a WXR file: missing wp:wxr_version"}
	}
	if p.chAcc.n > 0 {
		ex.Notes = append(ex.Notes, p.chAcc.note(RecordChannel, 0))
	}
	p.acc = nil
	return nil
}

func (p *parser) version(c child) error {
	var v string
	if err := p.text(c, &v); err != nil {
		return err
	}
	v = strings.Trim(v, xmlSpace)
	switch v {
	case "1.0", "1.1", "1.2":
		p.ex.Version = v
		return nil
	}
	return &Error{c.pos, fmt.Sprintf("unsupported WXR version %q; use 1.0, 1.1 or 1.2", v)}
}

// generatorElement reads the generator of the channel, which tells the version of WordPress if the comment does not.
func (p *parser) generatorElement(c child) error {
	if err := p.text(c, &p.ex.Channel.Generator); err != nil {
		return err
	}
	g := strings.TrimSpace(p.ex.Channel.Generator)
	for _, prefix := range []string{"http://wordpress.org/?v=", "https://wordpress.org/?v="} {
		if v, ok := strings.CutPrefix(g, prefix); ok && p.ex.WordPress == "" {
			p.ex.WordPress = v
		}
	}
	return nil
}

// begin opens a record, whose values the removed characters belong to.
func (p *parser) begin() {
	p.recAcc.reset()
	p.acc = &p.recAcc
}

// end closes the record at index of its kind, and notes its removed characters.
func (p *parser) end(kind RecordKind, index int) {
	if p.recAcc.n > 0 {
		p.ex.Notes = append(p.ex.Notes, p.recAcc.note(kind, index))
	}
	p.acc = &p.chAcc
}

func (p *parser) author(c child) error {
	ex := p.ex
	if len(ex.Authors) >= p.opts.MaxAuthors {
		return &Error{c.pos, fmt.Sprintf("too many authors: at least %d (max %d)", p.opts.MaxAuthors+1, p.opts.MaxAuthors)}
	}
	a := Author{Pos: c.pos}
	p.begin()
	_, err := p.children(c.name, func(cc child) error {
		switch cc.name {
		case wp("author_id"):
			id, err := p.integer(cc, 1)
			a.ID = id
			return err
		case wp("author_login"):
			return p.text(cc, &a.Login)
		case wp("author_email"):
			return p.text(cc, &a.Email)
		case wp("author_display_name"):
			return p.text(cc, &a.DisplayName)
		case wp("author_first_name"):
			return p.text(cc, &a.FirstName)
		case wp("author_last_name"):
			return p.text(cc, &a.LastName)
		}
		return p.skip()
	})
	if err != nil {
		return err
	}
	if blank(a.Login) {
		return missing(c.pos, wp("author_login"), c.name)
	}
	if i, ok := ex.authorIdx[a.Login]; ok {
		first := ex.Authors[i].Pos
		return &Error{c.pos, fmt.Sprintf("duplicate wp:author_login %q; the first is at %d:%d", a.Login, first.Line, first.Column)}
	}
	ex.authorIdx[a.Login] = len(ex.Authors)
	p.end(RecordAuthor, len(ex.Authors))
	ex.Authors = append(ex.Authors, a)
	return nil
}

// termForm tells the elements of wp:category, of wp:tag and of wp:term. The names that a form lacks are the zero qname,
// which no element has.
type termForm struct {
	element                                   qname
	taxonomy                                  string // that of the terms, if the elements do not tell
	taxonomyElement, slug, parent, name, desc qname
}

var (
	categoryForm = termForm{
		element: wp("category"), taxonomy: "category",
		slug: wp("category_nicename"), parent: wp("category_parent"), name: wp("cat_name"),
		desc: wp("category_description"),
	}
	tagForm = termForm{
		element: wp("tag"), taxonomy: "post_tag",
		slug: wp("tag_slug"), name: wp("tag_name"), desc: wp("tag_description"),
	}
	customForm = termForm{
		element: wp("term"), taxonomyElement: wp("term_taxonomy"),
		slug: wp("term_slug"), parent: wp("term_parent"), name: wp("term_name"), desc: wp("term_description"),
	}
)

func (p *parser) term(c child, k termForm) error {
	ex := p.ex
	if len(ex.Terms) >= p.opts.MaxTerms {
		return &Error{c.pos, fmt.Sprintf("too many terms: at least %d (max %d)", p.opts.MaxTerms+1, p.opts.MaxTerms)}
	}
	t := Term{Pos: c.pos, Taxonomy: k.taxonomy}
	p.begin()
	_, err := p.children(c.name, func(cc child) error {
		switch cc.name {
		case wp("term_id"):
			id, err := p.integer(cc, 1)
			t.ID = id
			return err
		case wp("termmeta"):
			m, err := p.meta(cc)
			if err == nil {
				t.Meta = append(t.Meta, m)
			}
			return err
		case k.taxonomyElement:
			return p.text(cc, &t.Taxonomy)
		case k.slug:
			return p.text(cc, &t.Slug)
		case k.parent:
			return p.text(cc, &t.Parent)
		case k.name:
			return p.text(cc, &t.Name)
		case k.desc:
			return p.text(cc, &t.Description)
		}
		return p.skip()
	})
	if err != nil {
		return err
	}
	if blank(t.Taxonomy) {
		return missing(c.pos, k.taxonomyElement, c.name)
	}
	if blank(t.Slug) {
		return missing(c.pos, k.slug, c.name)
	}
	if t.ID > 0 {
		if i, ok := ex.termIdx[termKey{taxonomy: t.Taxonomy, id: t.ID}]; ok {
			first := ex.Terms[i].Pos
			return &Error{c.pos, fmt.Sprintf("duplicate wp:term_id %d in the taxonomy %q; the first is at %d:%d",
				t.ID, t.Taxonomy, first.Line, first.Column)}
		}
	}
	if i, ok := ex.slugIdx[termKey{taxonomy: t.Taxonomy, slug: t.Slug}]; ok {
		first := ex.Terms[i].Pos
		return &Error{c.pos, fmt.Sprintf("duplicate slug %q in the taxonomy %q; the first is at %d:%d",
			t.Slug, t.Taxonomy, first.Line, first.Column)}
	}
	if t.ID > 0 {
		ex.termIdx[termKey{taxonomy: t.Taxonomy, id: t.ID}] = len(ex.Terms)
	}
	ex.slugIdx[termKey{taxonomy: t.Taxonomy, slug: t.Slug}] = len(ex.Terms)
	p.end(RecordTerm, len(ex.Terms))
	ex.Terms = append(ex.Terms, t)
	return nil
}

// meta reads a wp:postmeta or a wp:termmeta, whose removed characters go to the record that it is in.
func (p *parser) meta(c child) (Meta, error) {
	m := Meta{Pos: c.pos}
	_, err := p.children(c.name, func(cc child) error {
		switch cc.name {
		case wp("meta_key"):
			return p.text(cc, &m.Key)
		case wp("meta_value"):
			return p.text(cc, &m.Value)
		}
		return p.skip()
	})
	if err != nil {
		return Meta{}, err
	}
	if blank(m.Key) {
		return Meta{}, missing(c.pos, wp("meta_key"), c.name)
	}
	return m, nil
}

func (p *parser) item(c child) error {
	ex := p.ex
	if len(ex.Items) >= p.opts.MaxItems {
		return &Error{c.pos, fmt.Sprintf("too many items: at least %d (max %d)", p.opts.MaxItems+1, p.opts.MaxItems)}
	}
	it := Item{Pos: c.pos}
	p.begin()
	_, err := p.children(c.name, func(cc child) error {
		switch cc.name {
		case rss("title"):
			return p.text(cc, &it.Title)
		case rss("link"):
			return p.text(cc, &it.Link)
		case rss("pubDate"):
			return p.text(cc, &it.PubDate)
		case dc("creator"):
			return p.text(cc, &it.Creator)
		case rss("guid"):
			return p.text(cc, &it.GUID)
		case rss("description"):
			return p.text(cc, &it.Description)
		case content("encoded"):
			return p.text(cc, &it.Content)
		case excerpt("encoded"):
			return p.text(cc, &it.Excerpt)
		case wp("post_id"):
			id, err := p.integer(cc, 1)
			it.ID = id
			return err
		case wp("post_date"):
			return p.text(cc, &it.Date)
		case wp("post_date_gmt"):
			return p.text(cc, &it.DateGMT)
		case wp("post_modified"):
			return p.text(cc, &it.Modified)
		case wp("post_modified_gmt"):
			return p.text(cc, &it.ModifiedGMT)
		case wp("comment_status"):
			return p.text(cc, &it.CommentStatus)
		case wp("ping_status"):
			return p.text(cc, &it.PingStatus)
		case wp("post_name"):
			return p.text(cc, &it.Name)
		case wp("status"):
			return p.text(cc, &it.Status)
		case wp("post_parent"):
			n, err := p.integer(cc, 0)
			it.Parent = n
			return err
		case wp("menu_order"):
			n, err := p.integer(cc, math.MinInt64)
			it.MenuOrder = n
			return err
		case wp("post_type"):
			return p.text(cc, &it.Type)
		case wp("post_password"):
			return p.text(cc, &it.Password)
		case wp("is_sticky"):
			n, err := p.integer(cc, math.MinInt64)
			it.Sticky = n != 0
			return err
		case wp("attachment_url"):
			return p.text(cc, &it.AttachmentURL)
		case rss("category"):
			return p.termRef(cc, &it)
		case wp("postmeta"):
			m, err := p.meta(cc)
			if err == nil {
				it.Meta = append(it.Meta, m)
			}
			return err
		case wp("comment"):
			it.Comments++
		}
		return p.skip()
	})
	if err != nil {
		return err
	}
	if it.ID == 0 {
		return missing(c.pos, wp("post_id"), c.name)
	}
	if blank(it.Type) {
		return missing(c.pos, wp("post_type"), c.name)
	}
	if i, ok := ex.postIdx[it.ID]; ok {
		first := ex.Items[i].Pos
		return &Error{c.pos, fmt.Sprintf("duplicate wp:post_id %d; the first is at %d:%d", it.ID, first.Line, first.Column)}
	}
	ex.postIdx[it.ID] = len(ex.Items)
	p.end(RecordItem, len(ex.Items))
	ex.Items = append(ex.Items, it)
	return nil
}

// termRef reads a category element of it. WXR 1.0 also writes the categories and the tags without the attributes, which
// are not read.
func (p *parser) termRef(c child, it *Item) error {
	// The values of the attributes are in the start tag.
	p.commit(false, true)
	var domain, nicename string
	for _, a := range c.start.Attr {
		if a.Name.Space != "" {
			continue
		}
		switch a.Name.Local {
		case "domain":
			domain = a.Value
		case "nicename":
			nicename = a.Value
		}
	}
	name, err := p.value(c.name)
	if err != nil {
		return err
	}
	if domain != "" && nicename != "" {
		if domain == "tag" {
			domain = "post_tag"
		}
		it.Terms = append(it.Terms, TermRef{Pos: c.pos, Taxonomy: domain, Slug: nicename, Name: name})
	}
	return nil
}
