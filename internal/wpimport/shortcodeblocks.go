package wpimport

import (
	"cmp"
	"math"
	"slices"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/html"
	"github.com/WhiteRoomLab/whiteroom/internal/wpblock"
	"github.com/WhiteRoomLab/whiteroom/internal/wpformat"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

// convertPiece makes the blocks of a shortcode, or of a URL that a provider embeds, that stands alone: on a line of its own, or
// in a paragraph of its own. admitted is how many of the blocks it counted among those converted. The error is a legacy error:
// the text of the piece is a legacy block.
func (c *bodyConv) convertPiece(pc *classicPiece, at noteAt) (blocks []content.Block, admitted int, err error) {
	switch pc.kind {
	case pieceEmbedURL:
		return []content.Block{c.embedBlock(pc.provider, pc.url)}, 0, nil
	case pieceTextURL:
		return nil, 0, legacyf("the URL is text")
	case pieceShortcode:
	}
	sc := pc.sc
	if len(sc.Attrs) > content.MaxTextBytes {
		return nil, 0, legacyf("the shortcode [%s] is not converted", sc.Name)
	}
	attrs := wpformat.ParseAttrs(sc.Attrs)
	switch sc.Name {
	case "caption", "wp_caption":
		return c.captionPiece(pc, attrs)
	case "gallery":
		return c.galleryPiece(attrs, at)
	case "embed":
		return c.embedPiece(pc, attrs)
	case "contact-form-7", "contact-form":
		return c.formPiece(pc, attrs)
	case "video", "audio":
		return nil, 0, legacyf("whiteroom has no %s blocks yet (stage 2)", sc.Name)
	case "playlist":
		return nil, 0, legacyf("whiteroom has no playlist blocks")
	}
	return nil, 0, legacyf("the shortcode [%s] is not converted", sc.Name)
}

func (c *bodyConv) embedBlock(provider, href string) content.Block {
	blk := c.newBlock(content.BlockEmbed)
	blk.Attrs.Provider, blk.Attrs.URL = provider, href
	return blk
}

// lostOfShortcode notes the settings of the shortcode name that whiteroom has no counterpart of.
func (c *bodyConv) lostOfShortcode(at noteAt, name string, items ...string) {
	c.noteItems(at, Skipped, "lost ", " of the shortcode ["+name+"]; whiteroom has no such settings", items...)
}

// phpEmpty is empty() of PHP for the string s.
func phpEmpty(s string) bool { return s == "" || s == "0" }

// phpTrim is trim() of PHP.
func phpTrim(s string) string { return strings.Trim(s, " \t\n\r\x00\v") }

// phpToInt is (int) of PHP for the string s: the white space, the sign and the digits at its start, or 0.
func phpToInt(s string) int64 {
	i := 0
	for i < len(s) && (s[i] == ' ' || '\t' <= s[i] && s[i] <= '\r') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	var n int64
	for ; i < len(s) && '0' <= s[i] && s[i] <= '9'; i++ {
		d := int64(s[i] - '0')
		if n > (math.MaxInt64-d)/10 {
			if neg {
				return math.MinInt64
			}
			return math.MaxInt64
		}
		n = n*10 + d
	}
	if neg {
		n = -n
	}
	return n
}

// idList is wp_parse_id_list of WordPress for the string s: the positive whole numbers that the words of s, which commas and
// white space part, make by absint, each once, in order. A word that is no number gives 0, which no post has.
func idList(s string) []int64 {
	var ids []int64
	seen := make(map[int64]bool)
	for _, word := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || '\t' <= r && r <= '\r' }) {
		n := phpToInt(word)
		if n < 0 {
			n = -n
		}
		if n >= 1 && !seen[n] {
			seen[n] = true
			ids = append(ids, n)
		}
	}
	return ids
}

// stripTags removes what lies between a "<" and the next ">".
func stripTags(s string) string {
	if !strings.Contains(s, "<") {
		return s
	}
	var b strings.Builder
	for {
		i := strings.IndexByte(s, '<')
		if i < 0 {
			break
		}
		b.WriteString(s[:i])
		j := strings.IndexByte(s[i:], '>')
		if j < 0 {
			b.WriteString(s[i:])
			return b.String()
		}
		s = s[i+j+1:]
	}
	b.WriteString(s)
	return b.String()
}

// embedPiece converts [embed]: the address is its content, or else its attribute src.
func (c *bodyConv) embedPiece(pc *classicPiece, attrs wpformat.AttrList) ([]content.Block, int, error) {
	raw := ""
	if pc.sc.Closed {
		raw = strings.TrimSpace(stripTags(pc.sc.Content))
	}
	if raw == "" {
		src, _ := attrs.Get("src")
		raw = strings.TrimSpace(src)
	}
	if len(raw) > content.MaxTextBytes {
		return nil, 0, legacyf("the URL cannot be embedded: it is too long")
	}
	provider, href, err := embedOfURL(decodeRefs(raw))
	if err != nil {
		return nil, 0, err
	}
	blk := c.embedBlock(provider, href)
	var lost []string
	for _, name := range []string{"width", "height"} {
		if v, _ := attrs.Get(name); v != "" {
			lost = append(lost, name)
		}
	}
	c.lostOfShortcode(noteAt{id: blk.ID}, pc.sc.Name, lost...)
	return []content.Block{blk}, 0, nil
}

// formPiece converts [contact-form-7], which finds the form as the block of Contact Form 7 does, and [contact-form], which
// Contact Form 7 before 4 wrote, with the number that the form had then.
func (c *bodyConv) formPiece(pc *classicPiece, attrs wpformat.AttrList) ([]content.Block, int, error) {
	var form *wxr.Item
	var id int64
	title := ""
	var lost []string
	if pc.sc.Name == "contact-form-7" {
		hash, _ := attrs.Get("id")
		hash = phpTrim(hash)
		title, _ = attrs.Get("title")
		title = phpTrim(title)
		id = textID(hash)
		form = c.imp.findForm(id, hash, title)
		// The form is the form of the site: how it is shown is up to the theme.
		for _, name := range []string{"html_id", "html_name", "html_title", "html_class", "output"} {
			if v, ok := attrs.Get(name); ok && v != "" && (name != "output" || v != "form") {
				lost = append(lost, name)
			}
		}
	} else {
		first, _ := attrs.First()
		id = phpToInt(first.Value)
		form = c.imp.findFormByOldID(id)
	}
	blk, err := c.formBlock(form, id, title)
	if err != nil {
		return nil, 0, err
	}
	c.lostOfShortcode(noteAt{id: blk.ID}, pc.sc.Name, lost...)
	return []content.Block{blk}, 0, nil
}

// blankChildren reports whether the children of n are only white space, comments and line breaks.
func blankChildren(n *html.Node) bool {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		switch {
		case ch.Type == html.CommentNode, ch.Type == html.TextNode && blank(ch.Data), isElement(ch, "br"):
		default:
			return false
		}
	}
	return true
}

// trimBreaks removes the white space, the comments and the line breaks at the ends of the children of n.
func trimBreaks(n *html.Node) {
	removable := func(ch *html.Node) bool {
		return ch.Type == html.CommentNode || ch.Type == html.TextNode && blank(ch.Data) || isElement(ch, "br")
	}
	for n.FirstChild != nil && removable(n.FirstChild) {
		n.RemoveChild(n.FirstChild)
	}
	for n.LastChild != nil && removable(n.LastChild) {
		n.RemoveChild(n.LastChild)
	}
}

// captionAlign reads the attribute align of [caption].
func captionAlign(v string) string {
	switch v {
	case "alignleft":
		return "left"
	case "aligncenter":
		return "center"
	case "alignright":
		return "right"
	}
	return ""
}

// captionPiece converts [caption] and [wp_caption]: the first image of its content, with the link around it, and the text
// after them as the caption, or else the attribute caption. WordPress shows the image alone if the width is less than 1 or the
// caption is empty.
func (c *bodyConv) captionPiece(pc *classicPiece, attrs wpformat.AttrList) ([]content.Block, int, error) {
	get := func(name string) string { v, _ := attrs.Get(name); return v }
	nodes, err := c.parseHTML(pc.sc.Content)
	if err != nil {
		return nil, 0, err
	}
	root := &html.Node{Type: html.DocumentFragmentNode}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	img := findElement(root, "img")
	if img == nil {
		return nil, 0, legacyf("the caption has no image")
	}
	// WordPress takes the opening tag of a link that comes right before the image with it, and the end tag too if only white
	// space comes between them: what else the link holds is the caption.
	unit, link := img, (*html.Node)(nil)
	if a := img.Parent; isElement(a, "a") {
		link = a
		if onlyChild(a) == img {
			unit = a
		}
	}
	holder := &html.Node{Type: html.ElementNode, Namespace: html.HTML, Name: "figcaption"}
	if text, ok := attrs.Get("caption"); ok {
		// WordPress shows all of the content then, not only the image.
		if hasOtherContent(unit, root) {
			return nil, 0, legacyf("the shortcode [%s] has the attribute caption and content other than the image", pc.sc.Name)
		}
		capNodes, err := c.parseHTML(text)
		if err != nil {
			return nil, 0, err
		}
		for _, n := range capNodes {
			holder.AppendChild(n)
		}
	} else {
		// What comes before the image is not shown, as in WordPress; what comes after it is the caption, wherever it is.
		var rest []*html.Node
		for n := unit; n != root; n = n.Parent {
			for s := n.NextSibling; s != nil; s = s.NextSibling {
				rest = append(rest, s)
			}
		}
		for _, n := range rest {
			n.Parent.RemoveChild(n)
			holder.AppendChild(n)
		}
	}
	trimBreaks(holder)
	if phpToInt(get("width")) < 1 || blankChildren(holder) {
		holder = nil
	}
	if class := strings.TrimSpace(get("class")); class != "" {
		setAttr(img, "class", class+" "+attr(img, "class"))
	}
	id := int64(0)
	if rest, ok := strings.CutPrefix(get("id"), "attachment_"); ok {
		id = textID(rest)
	}
	blk, err := c.classicImageBlock(classicImageInput{img: img, link: link, caption: holder, id: id, align: captionAlign(get("align"))})
	if err != nil {
		return nil, 0, err
	}
	return []content.Block{blk}, 0, nil
}

// hasOtherContent reports whether root holds, besides unit and the elements around it, more than white space, comments and
// line breaks.
func hasOtherContent(unit, root *html.Node) bool {
	other := func(n *html.Node) bool {
		return n.Type != html.CommentNode && (n.Type != html.TextNode || trimSpaces(n.Data) != "") && !isElement(n, "br")
	}
	for n := unit; n != root; n = n.Parent {
		for s := n.PrevSibling; s != nil; s = s.PrevSibling {
			if other(s) {
				return true
			}
		}
		for s := n.NextSibling; s != nil; s = s.NextSibling {
			if other(s) {
				return true
			}
		}
	}
	return false
}

// setAttr sets the attribute name of n.
func setAttr(n *html.Node, name, value string) {
	for i := range n.Attrs {
		if a := &n.Attrs[i]; a.Namespace == "" && a.Name == name {
			a.Value = value
			return
		}
	}
	n.Attrs = append(n.Attrs, html.Attr{Name: name, Value: value})
}

// attachmentsOf returns the attachments whose parent is the post id, in the order of the WXR.
func (imp *Import) attachmentsOf(id int64) []*wxr.Item {
	if imp.WXR == nil {
		return nil
	}
	if imp.attachments == nil {
		imp.attachments = make(map[int64][]*wxr.Item)
		for i := range imp.WXR.Items {
			if it := &imp.WXR.Items[i]; it.Type == "attachment" && it.Parent >= 1 {
				imp.attachments[it.Parent] = append(imp.attachments[it.Parent], it)
			}
		}
	}
	return imp.attachments[id]
}

// galleryEntry is an attachment of a gallery: item is nil if the WXR has none with the ID.
type galleryEntry struct {
	id   int64
	item *wxr.Item
}

func (e galleryEntry) menuOrder() int64 {
	if e.item == nil {
		return 0
	}
	return e.item.MenuOrder
}

// galleryEntries returns the attachments that [gallery] with the attributes shows, in the order that it shows them, as
// gallery_shortcode of WordPress finds them: those of ids or include, or else the children of the post id except exclude. The
// order is that of ids; failing that, or if orderby says so, menu_order and then ID (the other way if order is DESC). lost is
// set if orderby has a key that it does not follow.
func (c *bodyConv) galleryEntries(attrs wpformat.AttrList) (entries []galleryEntry, lost bool) {
	get := func(name string) string { v, _ := attrs.Get(name); return v }
	include, hasIDs := get("ids"), !phpEmpty(get("ids"))
	if !hasIDs {
		include = get("include")
	}
	var keys []string
	for _, key := range strings.Fields(get("orderby")) {
		if key == "menu_order" || key == "ID" || key == "post__in" {
			keys = append(keys, key)
		} else {
			lost = true
		}
	}
	if len(keys) == 0 {
		keys = []string{"menu_order", "ID"}
		if hasIDs {
			keys = []string{"post__in"}
		}
	}
	rank := make(map[int64]int)
	if !phpEmpty(include) {
		for i, id := range idList(include) {
			rank[id] = i
			var item *wxr.Item
			if c.imp.WXR != nil {
				item = c.imp.WXR.Post(id)
			}
			entries = append(entries, galleryEntry{id, item})
		}
	} else {
		parent := c.opts.Item.ID
		if v, ok := attrs.Get("id"); ok {
			parent = phpToInt(v)
		}
		excluded := make(map[int64]bool)
		for _, id := range idList(get("exclude")) {
			excluded[id] = true
		}
		for _, it := range c.imp.attachmentsOf(parent) {
			if !excluded[it.ID] {
				entries = append(entries, galleryEntry{it.ID, it})
			}
		}
	}
	desc := strings.EqualFold(strings.TrimSpace(get("order")), "DESC")
	slices.SortStableFunc(entries, func(a, b galleryEntry) int {
		for _, key := range keys {
			d := 0
			switch key {
			case "post__in":
				d = cmp.Compare(rank[a.id], rank[b.id])
			case "menu_order":
				d = cmp.Compare(a.menuOrder(), b.menuOrder())
			case "ID":
				d = cmp.Compare(a.id, b.id)
			}
			if desc && key != "post__in" {
				d = -d
			}
			if d != 0 {
				return d
			}
		}
		return 0
	})
	return entries, lost
}

// galleryCaption returns the caption of an image of a gallery: the excerpt of its attachment, as an element for the text of
// the caption to be made of, or nil if it has none or cannot be text.
func (c *bodyConv) galleryCaption(excerpt string) *html.Node {
	if blank(excerpt) {
		return nil
	}
	nodes, err := c.parseHTML(excerpt)
	if err != nil {
		return nil
	}
	holder := &html.Node{Type: html.ElementNode, Namespace: html.HTML, Name: "figcaption"}
	for _, n := range nodes {
		holder.AppendChild(n)
	}
	m := c.snapshot()
	_, err = c.richText(holder, false, noteAt{})
	c.restore(m)
	if err != nil {
		return nil
	}
	return holder
}

// galleryPiece converts [gallery] into the images of the attachments, one after the other: whiteroom has no gallery blocks
// yet. The images are counted among the blocks converted as they are made.
func (c *bodyConv) galleryPiece(attrs wpformat.AttrList, at noteAt) ([]content.Block, int, error) {
	get := func(name string) string { v, _ := attrs.Get(name); return v }
	entries, orderLost := c.galleryEntries(attrs)
	size := cmp.Or(strings.TrimSpace(get("size")), "thumbnail")
	var blocks []content.Block
	for _, e := range entries {
		if !c.admit(at) {
			break
		}
		mediaID, ok, why := c.attachment(e.id, content.MediaImage)
		if !ok {
			if why == "" {
				c.note(at, Skipped, "the image %d of the gallery is not imported: it is not an imported image", e.id)
			}
			// An attachment that is not an image is not in the gallery of WordPress either.
			continue
		}
		in := imageInput{img: &html.Node{Type: html.ElementNode, Namespace: html.HTML, Name: "img"}, id: e.id, size: size}
		if e.item != nil {
			in.caption = c.galleryCaption(e.item.Excerpt)
		}
		blk, err := c.imageBlock(in)
		if err != nil {
			continue
		}
		// The page of an attachment is where WordPress links to, unless it links to the file or to nothing; whiteroom has no such
		// pages.
		if get("link") != "none" {
			blk.Attrs.Link = &content.Link{Media: mediaID}
		}
		blocks = append(blocks, blk)
	}
	if len(blocks) == 0 {
		if !c.stopped {
			c.note(at, Skipped, "the empty gallery is removed")
		}
		return nil, 0, nil
	}
	first := noteAt{id: blocks[0].ID}
	c.note(first, Changed, "the gallery is %s here; whiteroom has no gallery blocks yet (stage 2)", count(len(blocks), "image"))
	var lost []string
	if v := get("columns"); v != "" {
		lost = append(lost, "columns")
	}
	if orderLost {
		lost = append(lost, "orderby")
	}
	c.lostOfShortcode(first, "gallery", lost...)
	return blocks, len(blocks), nil
}

// trimSpaces removes the white space and the no-break spaces at the ends of s.
func trimSpaces(s string) string { return strings.Trim(s, " \t\n\f\r"+string(rune(0xA0))) }

// urlLine reports whether s is a URL as WordPress tries to embed one on a line of its own, and nothing else.
func urlLine(s string) bool {
	spans := wpformat.AutoembedURLs(s)
	return len(spans) == 1 && spans[0].Start == 0 && spans[0].End == len(s)
}

// lonePiece converts a paragraph of the block editor whose content is one shortcode, or one URL that a provider embeds, and
// nothing else, as the classic editor's shortcodes and URLs on a line of their own are converted, which WordPress runs and
// embeds. done is set if it made the blocks, which are none if it is an error. A paragraph of a URL that no provider embeds is
// not done: url is the URL, which the paragraph is to say is text.
func (c *bodyConv) lonePiece(root *html.Node, at noteAt) (blocks []content.Block, url string, done bool, err error) {
	first := root.FirstChild
	if first == nil || first.Type != html.TextNode {
		return nil, "", false, nil
	}
	text := trimSpaces(first.Data)
	switch {
	case strings.HasPrefix(text, "["):
		var children []*html.Node
		for ch := first; ch != nil; ch = ch.NextSibling {
			children = append(children, ch)
		}
		inner := trimSpaces(html.Render(children...))
		found := wpformat.FindShortcodes(inner, textShortcodeFilter())
		if len(found) != 1 || found[0].Start != 0 || found[0].End != len(inner) || found[0].Open || found[0].Close {
			return nil, "", false, nil
		}
		// The text of the shortcode is read as the classic editor's is: what is in it is not run.
		c.classicDepth++
		blocks, _, err = c.convertPiece(&classicPiece{kind: pieceShortcode, text: inner, sc: found[0]}, at)
		c.classicDepth--
		return blocks, "", true, err
	case first == root.LastChild && urlLine(text):
		provider, href, err := embedOfURL(text)
		if err != nil {
			return nil, text, false, nil
		}
		return []content.Block{c.embedBlock(provider, href)}, "", true, nil
	}
	return nil, "", false, nil
}

func isFalse(v *wpblock.Value) bool {
	b, ok := v.IsTrue()
	return ok && !b
}

// lostOfLone notes the settings of the paragraph b, whose p element is root, that the blocks of its one shortcode or URL do
// not have.
func (c *bodyConv) lostOfLone(b *wpblock.Block, root *html.Node, at noteAt) {
	var lost []string
	for i := range b.Attrs.Object {
		// A setting that is false is the default.
		if m := &b.Attrs.Object[i]; present(&m.Value) && !isFalse(&m.Value) {
			lost = append(lost, m.Name)
		}
	}
	if attr(root, "id") != "" && !slices.Contains(lost, "anchor") {
		lost = append(lost, "anchor")
	}
	c.lost(at, b.Name, lost...)
}

// shortcodeNames returns the names of the shortcodes below n, each as [name], in order, without repeats.
func (c *bodyConv) shortcodeNames(n *html.Node) []string {
	if len(c.pieces) == 0 {
		return nil
	}
	var names orderedSet
	for d := n.FirstChild; d != nil; d = after(n, d, true) {
		if pc := c.pieceOf(d); pc != nil && pc.kind == pieceShortcode {
			names.add("[" + pc.sc.Name + "]")
		}
	}
	return names.items
}
