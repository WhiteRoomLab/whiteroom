package wpimport

import (
	"cmp"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/html"
	"github.com/WhiteRoomLab/whiteroom/internal/wpblock"
)

func init() {
	converters["core/image"] = convertImage
	converters["core/gallery"] = convertGallery
	converters["core/file"] = convertFile
	converters["core/embed"] = convertEmbed
	converters["core/video"] = noBlocksYet("video")
	converters["core/audio"] = noBlocksYet("audio")
	// The embeds of WordPress before 5.6 are a block for each provider, such as core-embed/youtube.
	converterPrefixes["core-embed/"] = convertEmbed
}

const reasonNotImage = "the image is not an imported attachment"

// noBlocksYet is the converter of the blocks of the kind (video, audio), which whiteroom has in stage 2.
func noBlocksYet(kind string) converter {
	return func(*bodyConv, string, *wpblock.Block, bodyPlace, noteAt) ([]content.Block, error) {
		return nil, legacyf("whiteroom has no %s blocks yet (stage 2)", kind)
	}
}

func str(v *wpblock.Value) string {
	s, _ := v.Str()
	return s
}

// cleanLine returns s as one line without the spaces at its ends and without the control characters, which Body removes,
// so that text of control characters only is empty.
func cleanLine(s string) string {
	s, _ = oneLine(s)
	return strings.Trim(strings.Map(dropControl, s), " ")
}

// postID reads v as the ID of a post of WordPress: a whole number of 1 or more, which an attribute may write as text. It
// returns 0 if v is not one.
func postID(v *wpblock.Value) int64 {
	if n, ok := v.Int(); ok {
		return max(n, 0)
	}
	return textID(str(v))
}

func textID(s string) int64 {
	s = strings.TrimSpace(s)
	if !isDigits(s) {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// attachment finds the medium that the attachment of WordPress with the post ID id became. accept is the kind of medium
// that is wanted, such as content.MediaImage, or "" for any. why says so if the attachment is imported but of another
// kind. The caller has its own reason for an ID that is not an imported attachment.
func (c *bodyConv) attachment(id int64, accept string) (mediaID string, ok bool, why string) {
	kind, to, found := c.imp.IDs.Lookup(Object{Kind: ObjectPost, ID: id})
	if !found || kind != content.RefMedia {
		return "", false, ""
	}
	ref, found := c.imp.Ref(content.RefMedia, to)
	if !found {
		return "", false, ""
	}
	if accept != "" && ref.Type != accept {
		kindName := cmp.Or(ref.Type, "unknown")
		if ref.Type == content.MediaPDF {
			kindName = "PDF"
		}
		return "", false, fmt.Sprintf("the %s is a %s attachment", accept, kindName)
	}
	return to, true, ""
}

// attachmentByURL returns the post ID of the attachment of the old site that src, an address in a body, is a file or the
// page of. Relative addresses are from the link of the item. The error is a legacy error (see resolve).
func (c *bodyConv) attachmentByURL(src string) (int64, bool, error) {
	src = content.NormalizeHref(src)
	if src == "" {
		return 0, false, nil
	}
	ref, err := url.Parse(src)
	if err != nil {
		return 0, false, nil
	}
	abs, err := c.resolve(ref)
	if err != nil {
		return 0, false, err
	}
	id, _, same := c.imp.urlIndex().lookup(abs)
	return id, same && id >= 1, nil
}

// imageClassID returns the ID in the class wp-image-<n> of the img element, which WordPress gives to the image of an
// attachment, or 0.
func imageClassID(img *html.Node) int64 {
	for _, class := range classList(img) {
		if rest, ok := strings.CutPrefix(class, "wp-image-"); ok {
			if id := textID(rest); id >= 1 {
				return id
			}
		}
	}
	return 0
}

// imageMedia finds the attachment that the img element shows: the one with the ID attrID, which the attributes tell, if
// there is one, or else the one in the class of the element, or else the one whose file is its src. why is the reason
// for a legacy block if the image is not an imported image. The error is a legacy error (see attachmentByURL).
func (c *bodyConv) imageMedia(attrID int64, img *html.Node) (post int64, mediaID, why string, err error) {
	post = attrID
	if post < 1 {
		post = imageClassID(img)
	}
	if post < 1 {
		if post, _, err = c.attachmentByURL(attr(img, "src")); err != nil {
			return 0, "", "", err
		}
	}
	if post < 1 {
		return 0, "", reasonNotImage, nil
	}
	mediaID, ok, why := c.attachment(post, content.MediaImage)
	if !ok {
		return 0, "", cmp.Or(why, reasonNotImage), nil
	}
	return post, mediaID, "", nil
}

// mediaHasAlt reports whether the attachment with the post ID post has an alt, which an image without one shows.
func (c *bodyConv) mediaHasAlt(post int64) bool {
	if c.imp.WXR == nil {
		return false
	}
	it := c.imp.WXR.Post(post)
	if it == nil {
		return false
	}
	alt, _ := it.Meta.Get("_wp_attachment_image_alt")
	return strings.TrimSpace(alt) != ""
}

// linkAround returns the a element that holds the img element, below top, or nil.
func linkAround(img, top *html.Node) *html.Node {
	for n := img.Parent; n != nil && n != top; n = n.Parent {
		if isElement(n, "a") {
			return n
		}
	}
	return nil
}

// notImported notes that an item with the value written is not imported.
func (c *bodyConv) notImported(at noteAt, item, written, why string) {
	c.note(at, Skipped, "the %s %q is not imported: %s", item, written, why)
}

// imageInput is what imageBlock makes an image block of: the elements of the HTML of the image, and what the attributes of
// the block of WordPress tell.
type imageInput struct {
	img        *html.Node // the img element
	link       *html.Node // the a element around it, or nil
	caption    *html.Node // the figcaption element, or nil
	id         int64      // the ID of the attachment, if the attributes tell it
	size       string     // the slug of the size
	alt        string     // the alt, for an img that has none
	decorative bool
}

// imageBlock makes the image block of in. The error is a legacy error: the image is not an imported image, its caption has
// something that rich text cannot have, or its address or its link goes over the limit of the HTML that is read (see resolve).
func (c *bodyConv) imageBlock(in imageInput) (content.Block, error) {
	post, mediaID, why, err := c.imageMedia(in.id, in.img)
	if err != nil {
		return content.Block{}, err
	}
	if mediaID == "" {
		return content.Block{}, legacyf("%s", why)
	}
	blk := c.newBlock(content.BlockImage)
	own := noteAt{id: blk.ID}
	blk.Attrs.Media = mediaID
	if in.decorative {
		blk.Attrs.Decorative = true
	} else {
		alt := cleanLine(attr(in.img, "alt"))
		if alt == "" {
			alt = cleanLine(in.alt)
		}
		switch {
		case alt != "":
			blk.Attrs.Alt = alt
		case !c.mediaHasAlt(post):
			c.note(noteAt{id: blk.ID, sub: "/attrs/alt"}, Review, "the image has no alt; set alt, or decorative for a decorative image")
		}
	}
	if in.caption != nil {
		caption, err := c.richText(in.caption, false, own)
		if err != nil {
			return content.Block{}, err
		}
		blk.Attrs.Caption = caption
	}
	if in.link != nil {
		link, err := c.link(attr(in.link, "href"), attr(in.link, "target"), attr(in.link, "rel"), own)
		if err != nil {
			return content.Block{}, err
		}
		blk.Attrs.Link = link
	}
	blk.Attrs.Size = c.imageSize(in.size, own)
	return blk, nil
}

// imageSize returns the name in the theme of the size slug of WordPress, and notes why it is not imported if there is
// none. The full size is no size here.
func (c *bodyConv) imageSize(slug string, own noteAt) string {
	if slug == "" || slug == "full" {
		return ""
	}
	name, r := lookupName(c.blockNames().ImageSizes, slug)
	switch r {
	case nameMapped:
		return name
	case nameToNothing:
		c.note(own, Skipped, "the image size %q is not imported: blocks.imageSizes maps it to nothing", slug)
	case nameNotAccepted:
		c.note(own, Skipped, "the image size %q is not imported: it is not a name that whiteroom accepts", slug)
	}
	return ""
}

// leaveOut makes the validation guard leave out the block blk, which a converter returns next to blocks that come from
// other blocks of WordPress, if it does not pass, instead of making a legacy block of the text of the block of WordPress
// that the converter converts, which holds those too. (A block inside a block that the converter returns has no origin,
// and is left out.)
func (c *bodyConv) leaveOut(blk *content.Block, p bodyPlace) {
	c.origins[blk.ID] = origin{top: p.top, guard: true}
}

func convertImage(c *bodyConv, _ string, b *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
	root, err := c.root(b, "figure", "div")
	if err != nil {
		return nil, err
	}
	// Old WordPress wrote a div around the figure of an image that was aligned.
	holder := root
	if root.Name == "div" {
		if figure := childNamed(root, "figure"); figure != nil {
			holder = figure
		}
	}
	img := findElement(holder, "img")
	if img == nil {
		return nil, legacyf("the image block has no image")
	}
	a := b.Attrs
	decorative, _ := a.Get("isDecorative").IsTrue()
	blk, err := c.imageBlock(imageInput{
		img: img, link: linkAround(img, holder), caption: childNamed(holder, "figcaption"),
		id: postID(a.Get("id")), size: str(a.Get("sizeSlug")), alt: str(a.Get("alt")), decorative: decorative,
	})
	if err != nil {
		return nil, err
	}
	own := noteAt{id: blk.ID}
	if text, ok := lengthText(a.Get("width")); ok && text != "" {
		if err := content.CheckLength(text, content.LengthRule{Percent: true}); err != nil {
			c.notImported(own, "width", text, err.Error())
		} else {
			blk.Attrs.Width = text
		}
	}
	if text, ok := scalarText(a.Get("aspectRatio")); ok && text != "" && text != "auto" {
		if err := content.CheckAspectRatio(text); err != nil {
			c.notImported(own, "aspectRatio", text, err.Error())
		} else {
			blk.Attrs.AspectRatio = text
		}
	}
	var lost []string
	if on, _ := a.Get("lightbox").Get("enabled").IsTrue(); on {
		lost = append(lost, "lightbox")
	}
	// WordPress writes the height auto when an image is resized, and the scale cover for one with an aspect ratio; the
	// image shows so here.
	if h := a.Get("height"); present(h) && str(h) != "auto" {
		lost = append(lost, "height")
	}
	if sc := a.Get("scale"); present(sc) && (str(sc) != "cover" || blk.Attrs.AspectRatio == "") {
		lost = append(lost, "scale")
	}
	if present(a.Get("title")) || attr(img, "title") != "" {
		lost = append(lost, "title")
	}
	if present(a.Get("focalPoint")) {
		lost = append(lost, "focalPoint")
	}
	switch align := str(a.Get("align")); align {
	case "", "none":
	case "left", "center", "right", "wide", "full":
		blk.Attrs.Align = align
	default:
		lost = append(lost, "align")
	}
	c.lost(own, b.Name, lost...)
	c.applyProps(&blk, b, root, own)
	return []content.Block{blk}, nil
}

// elementsNamed returns the elements called name below root, in document order, without those inside one of them.
func elementsNamed(root *html.Node, name string) []*html.Node {
	var out []*html.Node
	for d := root.FirstChild; d != nil; {
		found := isElement(d, name)
		if found {
			out = append(out, d)
		}
		d = after(root, d, !found)
	}
	return out
}

func convertGallery(c *bodyConv, doc string, b *wpblock.Block, p bodyPlace, at noteAt) ([]content.Block, error) {
	root, err := c.root(b, "figure", "ul")
	if err != nil {
		return nil, err
	}
	// whiteroom has no gallery blocks yet: the images of the gallery are in its place, one after the other.
	var blocks []content.Block
	if len(b.Inner) > 0 {
		c.stray(root, func(n *html.Node) bool { return isElement(n, "figcaption") }, b.Name, at)
		blocks = c.flow(doc, b.Inner, p, at)
	} else {
		blocks = c.oldGallery(root, b, at)
	}
	images := len(blocks)
	if images == 0 && c.stopped {
		return nil, nil
	}
	if caption := childNamed(root, "figcaption"); caption != nil {
		para := c.newBlock(content.BlockParagraph)
		text, err := c.richText(caption, false, noteAt{id: para.ID})
		switch {
		case err != nil:
			// A legacy block of the gallery would hold the text of its images too.
			c.note(at, Skipped, "the caption of the gallery is not imported: %s", err)
		case !emptyText(text):
			para.Text = text
			// The images have the origins of their own blocks: the caption must not make a legacy block of all of them.
			c.leaveOut(&para, p)
			blocks = append(blocks, para)
		}
	}
	if len(blocks) == 0 {
		c.note(at, Skipped, "the empty gallery is removed")
		return nil, nil
	}
	first := noteAt{id: blocks[0].ID}
	c.note(first, Changed, "the gallery is %s here; whiteroom has no gallery blocks yet (stage 2)", count(images, "image"))
	c.lost(first, b.Name, galleryLost(b, root)...)
	return blocks, nil
}

// galleryLost returns the settings of the gallery that are lost with it: the anchor, and the attributes that are set.
// linkTo and linkTarget are not lost: the links are in the HTML of the images.
func galleryLost(b *wpblock.Block, root *html.Node) []string {
	var lost []string
	if present(b.Attrs.Get("anchor")) || attr(root, "id") != "" {
		lost = append(lost, "anchor")
	}
	for _, key := range []string{"className", "style", "columns", "imageCrop", "randomOrder", "fixedHeight", "aspectRatio", "align"} {
		if present(b.Attrs.Get(key)) {
			lost = append(lost, key)
		}
	}
	return lost
}

// oldGallery makes the images of a gallery of WordPress before 5.9, which has no blocks inside it: the list items of its
// HTML, or else its figures. An item that is not an imported image is a legacy block of its HTML.
func (c *bodyConv) oldGallery(root *html.Node, b *wpblock.Block, at noteAt) []content.Block {
	items := elementsNamed(root, "li")
	if len(items) == 0 {
		items = elementsNamed(root, "figure")
	}
	size := str(b.Attrs.Get("sizeSlug"))
	var out []content.Block
	for _, item := range items {
		if !c.admit(at) {
			break
		}
		img := findElement(item, "img")
		if img == nil {
			out = append(out, c.legacyHTML(html.Render(item), "the gallery item has no image", at)...)
			continue
		}
		blk, err := c.imageBlock(imageInput{
			img: img, link: linkAround(img, item), caption: findElement(item, "figcaption"),
			id: textID(attr(img, "data-id")), size: size,
		})
		if err != nil {
			out = append(out, c.legacyHTML(html.Render(item), err.Error(), at)...)
			continue
		}
		out = append(out, blk)
	}
	return out
}

func convertFile(c *bodyConv, _ string, b *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
	root, err := c.root(b, "div")
	if err != nil {
		return nil, err
	}
	var name *html.Node // the link with the name of the file; the link of the button has download
	for d := root.FirstChild; d != nil && name == nil; d = after(root, d, true) {
		if isElement(d, "a") && !hasAttr(d, "download") {
			name = d
		}
	}
	post := postID(b.Attrs.Get("id"))
	if post < 1 {
		if post, _, err = c.attachmentByURL(str(b.Attrs.Get("href"))); err != nil {
			return nil, err
		}
	}
	if post < 1 && name != nil {
		if post, _, err = c.attachmentByURL(attr(name, "href")); err != nil {
			return nil, err
		}
	}
	mediaID, ok, _ := c.attachment(post, "")
	if !ok {
		return nil, legacyf("the file is not an imported attachment")
	}
	blk := c.newBlock(content.BlockFile)
	own := noteAt{id: blk.ID}
	blk.Attrs.Media = mediaID
	if name != nil {
		if blk.Attrs.Label, err = c.plainText(name, own); err != nil {
			return nil, err
		}
	}
	var lost []string
	if on, _ := b.Attrs.Get("displayPreview").IsTrue(); on {
		lost = append(lost, "displayPreview")
	}
	if present(b.Attrs.Get("textLinkTarget")) {
		lost = append(lost, "textLinkTarget")
	}
	c.lost(own, b.Name, lost...)
	c.applyProps(&blk, b, root, own)
	return []content.Block{blk}, nil
}

// embedNames are the providers of WordPress that whiteroom embeds, by their names in WordPress.
var embedNames = map[string]string{"youtube": "youtube", "vimeo": "vimeo", "twitter": "x", "x": "x"}

// embedOrder is the order in which the providers are tried for an embed that does not name one.
var embedOrder = []string{"youtube", "vimeo", "x", "googleMaps"}

func convertEmbed(c *bodyConv, _ string, b *wpblock.Block, _ bodyPlace, _ noteAt) ([]content.Block, error) {
	root, err := c.root(b, "figure", "div")
	if err != nil {
		return nil, err
	}
	slug := str(b.Attrs.Get("providerNameSlug"))
	if rest, ok := strings.CutPrefix(b.Name, "core-embed/"); ok && slug == "" {
		slug = rest
	}
	provider := ""
	if slug != "" {
		if provider = embedNames[slug]; provider == "" {
			return nil, legacyf("embeds of %q are not allowed", slug)
		}
	}
	href := str(b.Attrs.Get("url"))
	if href == "" {
		if wrapper := findClass(root, "wp-block-embed__wrapper"); wrapper != nil {
			href = elementText(wrapper)
		}
	}
	if href = content.NormalizeHref(href); href == "" {
		return nil, legacyf("the embed has no URL")
	}
	if err := content.CheckHref(href); err != nil {
		return nil, legacyf("the URL cannot be embedded: %s", err)
	}
	if provider == "" {
		for _, name := range embedOrder {
			if _, err := content.ParseEmbedURL(name, href); err == nil {
				provider = name
				break
			}
		}
		if provider == "" {
			return nil, legacyf("the URL cannot be embedded: not a URL that a provider can embed")
		}
	} else if _, err := content.ParseEmbedURL(provider, href); err != nil {
		return nil, legacyf("the URL cannot be embedded: %s", err)
	}
	blk := c.newBlock(content.BlockEmbed)
	own := noteAt{id: blk.ID}
	blk.Attrs.Provider, blk.Attrs.URL = provider, href
	if figcaption := findElement(root, "figcaption"); figcaption != nil {
		if blk.Attrs.Caption, err = c.richText(figcaption, false, own); err != nil {
			return nil, err
		}
	}
	if present(b.Attrs.Get("align")) {
		c.lost(own, b.Name, "align")
	}
	c.applyProps(&blk, b, root, own)
	return []content.Block{blk}, nil
}
