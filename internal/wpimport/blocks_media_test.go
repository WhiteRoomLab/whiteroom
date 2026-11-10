package wpimport

import (
	"fmt"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

const (
	photoURL  = "https://example.com/wp-content/uploads/2026/10/photo-scaled.jpg"
	bannerURL = "https://example.com/wp-content/uploads/2026/10/banner.png"
	reportURL = "https://example.com/wp-content/uploads/2026/10/report.pdf"
)

// bannerMeta is the _wp_attachment_metadata of a PNG of 1200 x 400 pixels with one smaller size.
var bannerMeta = phpArray(
	phpString("width"), phpInt(1200), phpString("height"), phpInt(400), phpString("file"), phpString("2026/10/banner.png"),
	phpString("sizes"), phpArray(
		phpString("medium"), phpArray(phpString("file"), phpString("banner-300x100.png"), phpString("width"), phpInt(300)),
	),
)

// mediaExport is the WXR of a site that the tests of the blocks of media, layout and references convert the bodies of:
// a page; the images 21, with an alt, and 22, without one; the PDF 23; the image 24, which is not imported; the forms
// 31, which is imported, 32, which is not, and 33; and the synced patterns 60 and up.
func mediaExport() *wxr.Export {
	pattern := func(id int64, title, text string) wxr.Item {
		return wxr.Item{ID: id, Type: "wp_block", Status: "publish", Title: title, Content: text}
	}
	form := func(id int64, title, hash string) wxr.Item {
		return wxr.Item{ID: id, Type: "wpcf7_contact_form", Status: "publish", Title: title, Meta: wxr.MetaList{{Key: "_hash", Value: hash}}}
	}
	return &wxr.Export{
		Channel: wxr.Channel{Link: "https://example.com"},
		Items: []wxr.Item{
			{ID: 12, Type: "page", Status: "publish", Title: "About", Link: "https://example.com/about/"},
			{
				ID: 21, Type: "attachment", Status: "inherit", Title: "Photo", Link: "https://example.com/photo/",
				AttachmentURL: photoURL,
				Meta: wxr.MetaList{
					{Key: "_wp_attached_file", Value: "2026/10/photo-scaled.jpg"}, {Key: "_wp_attachment_metadata", Value: photoMeta},
					{Key: "_wp_attachment_image_alt", Value: "A photo"},
				},
			},
			{
				ID: 22, Type: "attachment", Status: "inherit", Title: "Banner", Link: "https://example.com/banner/",
				AttachmentURL: bannerURL,
				Meta: wxr.MetaList{
					{Key: "_wp_attached_file", Value: "2026/10/banner.png"}, {Key: "_wp_attachment_metadata", Value: bannerMeta},
				},
			},
			{
				ID: 23, Type: "attachment", Status: "inherit", Title: "Report", Link: "https://example.com/?attachment_id=23",
				AttachmentURL: reportURL,
				Meta:          wxr.MetaList{{Key: "_wp_attached_file", Value: "2026/10/report.pdf"}},
			},
			{
				ID: 24, Type: "attachment", Status: "inherit", Title: "Lost", Link: "https://example.com/lost/",
				AttachmentURL: "https://example.com/wp-content/uploads/2026/10/lost.jpg",
				Meta:          wxr.MetaList{{Key: "_wp_attached_file", Value: "2026/10/lost.jpg"}},
			},
			form(31, "Contact", "1a2b3c4d5e6f7a8b"),
			form(32, "Quote", "9f8e7d6c5b4a3921"),
			form(33, "Contact", "0123456789abcdef"),
			pattern(60, "Call to action", wpBlock("heading", "", `<h2 class="wp-block-heading">Join</h2>`)+"\n"+wpPara("Buy <strong>now</strong>")),
			pattern(61, "Wrapper", wpPara("Before")+"\n"+`<!-- wp:block {"ref":60} /-->`+"\n"+wpPara("After")),
			pattern(62, "Loop", `<!-- wp:block {"ref":63} /-->`),
			pattern(63, "Loop back", wpPara("Back")+`<!-- wp:block {"ref":62} /-->`),
			pattern(64, "Self", `<!-- wp:block {"ref":64} /-->`),
			pattern(65, "Empty", ""),
			pattern(66, "Classic", "<p>Hello</p>"),
			pattern(67, "Three", wpPara("a")+wpPara("b")+wpPara("c")),
			{ID: 68, Type: "post", Status: "publish", Title: "Not a pattern", Content: wpPara("x")},
			pattern(69, "Columns", wpColumns(``, wpColumn(``, wpPara("a")), wpColumn(``, wpPara("b")), wpColumn(``, wpPara("c")))),
			pattern(70, "Buttons", wpButtons(``, wpButton(``, buttonLink("/a", "A")), wpButton(``, buttonLink("/b", "B")), wpButton(``, buttonLink("/c", "C")))),
			pattern(71, "", wpPara("Untitled")),
			pattern(72, "Say \"hi\" 日本語", wpPara("Quoted")),
			pattern(73, "Nested columns", wpColumns(``, wpColumn(``, wpPara("n")))),
			pattern(74, "With more", wpPara("one")+moreBlock+wpPara("two")),
			pattern(75, "Bad", imageBlock),
			pattern(76, "Stray", `<p>Hello</p><!-- /wp:acme/a --><p>World</p>`),
			{ID: 77, Type: "wp_block", Status: "draft", Title: "Draft", Content: wpPara("Hidden")},
			{ID: 78, Type: "wp_block", Status: "publish", Password: "secret", Title: "Locked", Content: wpPara("Hidden")},
		},
	}
}

// mediaIDs are the IDs in the site of the things of mediaExport that newMediaImport imports.
var mediaIDs = func() (ids struct{ about, photo, banner, report, contact string }) {
	imp, err := newMediaImport()
	if err != nil {
		panic(err)
	}
	id := func(post int64) string {
		_, to, _ := imp.IDs.Lookup(Object{Kind: ObjectPost, ID: post})
		return to
	}
	ids.about, ids.photo, ids.banner, ids.report, ids.contact = id(12), id(21), id(22), id(23), id(31)
	return ids
}()

// newMediaImport is the import of mediaExport in which the page 12, the images 21 and 22, the PDF 23 and the form 31 are
// imported, as the conversions of the entries, the media and the forms will have recorded them.
func newMediaImport() (*Import, error) {
	e := mediaExport()
	imp := New("site.xml", e, NewReport(Info{File: "site.xml"}))
	for _, a := range []struct {
		id   int64
		kind content.RefKind
		ref  content.Ref
	}{
		{12, content.RefEntry, content.Ref{Type: content.TypePage, Language: "ja"}},
		{21, content.RefMedia, content.Ref{Type: content.MediaImage}},
		{22, content.RefMedia, content.Ref{Type: content.MediaImage}},
		{23, content.RefMedia, content.Ref{Type: content.MediaPDF}},
		{31, content.RefForm, content.Ref{}},
	} {
		id, err := imp.IDs.Assign(PostObject(e.Post(a.id)), a.kind)
		if err != nil {
			return nil, err
		}
		imp.AddRef(a.kind, id, a.ref)
	}
	return imp, nil
}

func mediaImport(t testing.TB) *Import {
	t.Helper()
	imp, err := newMediaImport()
	if err != nil {
		t.Fatal(err)
	}
	return imp
}

// convertMedia converts text with the import imp, and checks what holds for every result, as textBody does.
func convertMedia(t *testing.T, imp *Import, site *content.Site, settings *Settings, text string) *BodyResult {
	t.Helper()
	res, err := imp.Body(BodyOptions{Site: site, Settings: settings, Item: bodyItem(text), Target: bodyTarget, Key: bodyKey})
	if err != nil {
		t.Fatalf("Body = %v", err)
	}
	checkBody(t, imp, site, res)
	checkNoPieces(t, text, imp, res)
	noGuard(t, imp)
	return res
}

// runMediaCases is runTextCases for bodies that refer to the things of mediaExport.
func runMediaCases(t *testing.T, cases []textCase) {
	t.Helper()
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			imp := mediaImport(t)
			res := convertMedia(t, imp, bodySite(tt.allowHTML), tt.settings, tt.text)
			got, err := res.Blocks.MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
			if want := withIDs(tt.want); string(got) != want {
				t.Errorf("the blocks are\n%s\nwant\n%s", got, want)
			}
			wantLines(t, imp.Report, bodyLines(tt.lines...))
		})
	}
}

// jsonImage is the JSON of an image block with the n-th ID: keys are the keys before the attributes, and attrs the
// attributes after media (the inside of the object), each empty if there are none.
func jsonImage(n int, media, keys, attrs string) string {
	if attrs != "" {
		attrs = "," + attrs
	}
	return jsonBlock("image", n, keys, fmt.Sprintf(`"media":%q%s`, media, attrs), "")
}

// wpImage writes an image block of WordPress with the attributes and the content of its figure.
func wpImage(attrs, figure string) string {
	return wpBlock("image", attrs, `<figure class="wp-block-image size-large">`+figure+`</figure>`)
}

// imgOf writes the img element of the file src with the alt, and the class that WordPress gives the image of the
// attachment id.
func imgOf(src, alt string, id int) string {
	return fmt.Sprintf(`<img src=%q alt=%q class="wp-image-%d"/>`, src, alt, id)
}

const (
	notImage  = "the image is not an imported attachment"
	pdfImage  = "the image is a PDF attachment"
	noAltLine = "review: the image has no alt; set alt, or decorative for a decorative image -> "
)

func TestMediaImages(t *testing.T) {
	photo, banner, about := mediaIDs.photo, mediaIDs.banner, mediaIDs.about
	sizes := &Settings{Blocks: emptyBlockSettings()}
	sizes.Blocks.ImageSizes["large"] = "wide-image"
	sizes.Blocks.ImageSizes["thumbnail"] = ""
	sizes.Blocks.ImageSizes["huge"] = "not a name!"
	sizes.Blocks.Variants["rounded"] = "round"
	sizeOf := func(slug string) string {
		return wpImage(`{"id":21,"sizeSlug":"`+slug+`"}`, imgOf(photoURL, "x", 21))
	}
	runMediaCases(t, []textCase{
		{name: "an image with an alt, a caption and a link to its file",
			text: wpImage(`{"id":21,"sizeSlug":"large","linkDestination":"media"}`,
				`<a href="`+photoURL+`">`+imgOf(photoURL, "One &amp; two", 21)+`</a><figcaption class="wp-element-caption">A <strong>caption</strong></figcaption>`),
			want: jsonList(jsonImage(0, photo, "", `"alt":"One & two","caption":[{"type":"text","text":"A "},{"type":"text","text":"caption","marks":[{"type":"bold"}]}],`+
				`"link":{"media":"`+photo+`"},"size":"large"`))},
		{name: "links to the page of the attachment, to an entry and to other sites",
			text: wpImage(`{"id":21}`, `<a href="https://example.com/photo/">`+imgOf(photoURL, "a", 21)+`</a>`) +
				wpImage(`{"id":21}`, `<a href="https://example.com/about/#team" target="_blank" rel="nofollow noopener">`+imgOf(photoURL, "b", 21)+`</a>`) +
				wpImage(`{"id":21}`, `<a href="https://other.example/x" rel="sponsored ugc">`+imgOf(photoURL, "c", 21)+`</a>`) +
				wpImage(`{"id":21}`, `<a href="javascript:alert(1)">`+imgOf(photoURL, "d", 21)+`</a>`) +
				wpImage(`{"id":21}`, `<a>`+imgOf(photoURL, "e", 21)+`</a>`),
			want: jsonList(
				jsonImage(0, photo, "", `"alt":"a","link":{"media":"`+photo+`"}`),
				jsonImage(1, photo, "", `"alt":"b","link":{"entry":"`+about+`","anchor":"team","newTab":true,"rel":["nofollow"]}`),
				jsonImage(2, photo, "", `"alt":"c","link":{"href":"https://other.example/x","rel":["sponsored","ugc"]}`),
				jsonImage(3, photo, "", `"alt":"d"`),
				jsonImage(4, photo, "", `"alt":"e"`)),
			lines: []string{`skipped: the link to "javascript:alert(1)" is removed: scheme "javascript" is not allowed -> /body/3`}},
		{name: "the size of the theme", settings: sizes,
			text: sizeOf("large") + sizeOf("full") + sizeOf("medium") + sizeOf("thumbnail") + sizeOf("huge") + sizeOf("大"),
			want: jsonList(
				jsonImage(0, photo, "", `"alt":"x","size":"wide-image"`),
				jsonImage(1, photo, "", `"alt":"x"`),
				jsonImage(2, photo, "", `"alt":"x","size":"medium"`),
				jsonImage(3, photo, "", `"alt":"x"`),
				jsonImage(4, photo, "", `"alt":"x"`),
				jsonImage(5, photo, "", `"alt":"x"`)),
			lines: []string{
				`skipped: the image size "thumbnail" is not imported: blocks.imageSizes maps it to nothing -> /body/3`,
				`skipped: the image size "huge" is not imported: it is not a name that whiteroom accepts -> /body/4`,
				`skipped: the image size "大" is not imported: it is not a name that whiteroom accepts -> /body/5`,
			}},
		{name: "the width and the aspect ratio",
			text: wpImage(`{"id":21,"width":"300px","aspectRatio":"16/9"}`, imgOf(photoURL, "a", 21)) +
				wpImage(`{"id":21,"width":300,"aspectRatio":"auto"}`, imgOf(photoURL, "b", 21)) +
				wpImage(`{"id":21,"width":"50%","aspectRatio":1.5}`, imgOf(photoURL, "c", 21)) +
				wpImage(`{"id":21,"width":"300","aspectRatio":"16:9"}`, imgOf(photoURL, "d", 21)) +
				wpImage(`{"id":21,"width":"-1px","aspectRatio":"0"}`, imgOf(photoURL, "e", 21)) +
				wpImage(`{"id":21,"width":"","aspectRatio":null}`, imgOf(photoURL, "f", 21)),
			want: jsonList(
				jsonImage(0, photo, "", `"alt":"a","width":"300px","aspectRatio":"16/9"`),
				jsonImage(1, photo, "", `"alt":"b","width":"300px"`),
				jsonImage(2, photo, "", `"alt":"c","width":"50%","aspectRatio":"1.5"`),
				jsonImage(3, photo, "", `"alt":"d"`),
				jsonImage(4, photo, "", `"alt":"e"`),
				jsonImage(5, photo, "", `"alt":"f"`)),
			lines: []string{
				`skipped: the width "300" is not imported: invalid length "300" -> /body/3`,
				`skipped: the aspectRatio "16:9" is not imported: invalid aspect ratio "16:9" -> /body/3`,
				`skipped: the width "-1px" is not imported: negative length "-1px" is not allowed here -> /body/4`,
				`skipped: the aspectRatio "0" is not imported: invalid aspect ratio "0" -> /body/4`,
			}},
		{name: "the alignment",
			text: wpImage(`{"id":21,"align":"left"}`, imgOf(photoURL, "a", 21)) + wpImage(`{"id":21,"align":"center"}`, imgOf(photoURL, "b", 21)) +
				wpImage(`{"id":21,"align":"right"}`, imgOf(photoURL, "c", 21)) + wpImage(`{"id":21,"align":"wide"}`, imgOf(photoURL, "d", 21)) +
				wpImage(`{"id":21,"align":"full"}`, imgOf(photoURL, "e", 21)) + wpImage(`{"id":21,"align":"none"}`, imgOf(photoURL, "f", 21)) +
				wpImage(`{"id":21,"align":"float"}`, imgOf(photoURL, "g", 21)),
			want: jsonList(
				jsonImage(0, photo, "", `"alt":"a","align":"left"`), jsonImage(1, photo, "", `"alt":"b","align":"center"`),
				jsonImage(2, photo, "", `"alt":"c","align":"right"`), jsonImage(3, photo, "", `"alt":"d","align":"wide"`),
				jsonImage(4, photo, "", `"alt":"e","align":"full"`), jsonImage(5, photo, "", `"alt":"f"`), jsonImage(6, photo, "", `"alt":"g"`)),
			lines: []string{lostLine("align", "core/image", "/body/6")}},
		{name: "the settings that are lost",
			text: wpImage(`{"id":21,"height":"200px","scale":"cover","lightbox":{"enabled":true},"focalPoint":{"x":0.5,"y":0.5},`+
				`"style":{"color":{"duotone":"x"},"shadow":"y"}}`, `<img src="`+photoURL+`" alt="a" title="Hi" class="wp-image-21"/>`) +
				wpImage(`{"id":21,"lightbox":{"enabled":false},"height":"","scale":null}`, imgOf(photoURL, "b", 21)),
			want: jsonList(jsonImage(0, photo, "", `"alt":"a"`), jsonImage(1, photo, "", `"alt":"b"`)),
			lines: []string{
				lostLine("lightbox, height, scale, title, focalPoint, style.color.duotone, style.shadow", "core/image", "/body/0"),
			}},
		{name: "the height of auto and the scale of cover with an aspect ratio are what the image shows, and are not lost",
			text: wpImage(`{"id":21,"width":"640px","height":"auto"}`, imgOf(photoURL, "a", 21)) +
				wpImage(`{"id":21,"aspectRatio":"16/9","scale":"cover"}`, imgOf(photoURL, "b", 21)) +
				wpImage(`{"id":21,"aspectRatio":"4/3","height":"auto","scale":"cover"}`, imgOf(photoURL, "c", 21)),
			want: jsonList(
				jsonImage(0, photo, "", `"alt":"a","width":"640px"`),
				jsonImage(1, photo, "", `"alt":"b","aspectRatio":"16/9"`),
				jsonImage(2, photo, "", `"alt":"c","aspectRatio":"4/3"`))},
		{name: "a height that is fixed, and a scale of contain or fill, or of cover without an aspect ratio, are lost",
			text: wpImage(`{"id":21,"height":"200px"}`, imgOf(photoURL, "a", 21)) +
				wpImage(`{"id":21,"aspectRatio":"16/9","scale":"contain"}`, imgOf(photoURL, "b", 21)) +
				wpImage(`{"id":21,"aspectRatio":"16/9","scale":"fill","height":"auto"}`, imgOf(photoURL, "c", 21)) +
				wpImage(`{"id":21,"scale":"cover"}`, imgOf(photoURL, "d", 21)) +
				wpImage(`{"id":21,"aspectRatio":"auto","scale":"cover","height":"300px"}`, imgOf(photoURL, "e", 21)) +
				wpImage(`{"id":21,"aspectRatio":"16:9","scale":"cover"}`, imgOf(photoURL, "f", 21)),
			want: jsonList(
				jsonImage(0, photo, "", `"alt":"a"`),
				jsonImage(1, photo, "", `"alt":"b","aspectRatio":"16/9"`),
				jsonImage(2, photo, "", `"alt":"c","aspectRatio":"16/9"`),
				jsonImage(3, photo, "", `"alt":"d"`),
				jsonImage(4, photo, "", `"alt":"e"`),
				jsonImage(5, photo, "", `"alt":"f"`)),
			lines: []string{
				lostLine("height", "core/image", "/body/0"), lostLine("scale", "core/image", "/body/1"), lostLine("scale", "core/image", "/body/2"),
				lostLine("scale", "core/image", "/body/3"), lostLine("height, scale", "core/image", "/body/4"),
				`skipped: the aspectRatio "16:9" is not imported: invalid aspect ratio "16:9" -> /body/5`, lostLine("scale", "core/image", "/body/5"),
			}},
		{name: "the border, the margin, the block style, the class and the anchor", settings: sizes,
			text: wpBlock("image", `{"id":21,"className":"is-style-rounded extra","style":{"border":{"radius":"8px"},"spacing":{"margin":{"top":"1rem"}}}}`,
				`<figure id="hero" class="wp-block-image is-style-rounded extra">`+imgOf(photoURL, "a", 21)+`</figure>`),
			want: jsonList(jsonImage(0, photo, `"variant":"round","className":"extra","anchor":"hero","style":{"margin":{"top":{"value":"1rem"}},"border":{"radius":{"value":"8px"}}}`, `"alt":"a"`))},
		{name: "a decorative image",
			text: wpImage(`{"id":22,"isDecorative":true}`, imgOf(bannerURL, "ignored", 22)) + wpImage(`{"id":22,"isDecorative":false}`, imgOf(bannerURL, "kept", 22)),
			want: jsonList(jsonBlock("image", 0, "", fmt.Sprintf(`"media":%q,"decorative":true`, banner), ""), jsonImage(1, banner, "", `"alt":"kept"`))},
		{name: "the alt",
			text: wpImage(`{"id":22}`, imgOf(bannerURL, "two&#10;lines  and  space", 22)) +
				wpImage(`{"id":22,"alt":"From the attribute"}`, imgOf(bannerURL, "", 22)) +
				wpImage(`{"id":22}`, imgOf(bannerURL, "", 22)) +
				wpImage(`{"id":22}`, imgOf(bannerURL, " ", 22)) +
				wpImage(`{"id":21}`, imgOf(photoURL, "", 21)),
			want: jsonList(
				jsonImage(0, banner, "", `"alt":"two lines  and  space"`), jsonImage(1, banner, "", `"alt":"From the attribute"`),
				jsonImage(2, banner, "", ""), jsonImage(3, banner, "", ""), jsonImage(4, photo, "", "")),
			lines: []string{noAltLine + "/body/2/attrs/alt", noAltLine + "/body/3/attrs/alt"}},
		{name: "an alt of nothing but a control character is none",
			text: wpImage(`{"id":22}`, `<img src="`+bannerURL+`" alt="`+string(rune(0x11))+`" class="wp-image-22"/>`) +
				wpImage(`{"id":22}`, `<img src="`+bannerURL+`" alt=" `+string(rune(0x11))+`a`+string(rune(0x7f))+` " class="wp-image-22"/>`),
			want:  jsonList(jsonImage(0, banner, "", ""), jsonImage(1, banner, "", `"alt":"a"`)),
			lines: []string{noAltLine + "/body/0/attrs/alt"}},
		{name: "the attachment by the class, if the address is of none",
			text: wpImage(``, `<img src="https://other.example/x.jpg" alt="a" class="size-full wp-image-21"/>`),
			want: jsonList(jsonImage(0, photo, "", `"alt":"a"`))},
		{name: "the attachment by the class, and by the address of the file",
			text: wpImage(``, imgOf(photoURL, "a", 21)) +
				wpImage(``, `<img src="https://example.com/wp-content/uploads/2026/10/photo-300x200-scaled.jpg" alt="b"/>`) +
				wpImage(``, `<img src="https://example.com/wp-content/uploads/2026/10/photo.jpg?ver=2" alt="c"/>`) +
				wpImage(``, `<img src="/wp-content/uploads/2026/10/banner.png" alt="d"/>`) +
				wpImage(``, `<img src="HTTPS://EXAMPLE.COM/wp-content/uploads/2026/10/banner-300x100.png" alt="e"/>`) +
				wpImage(`{"id":"22"}`, `<img src="https://other.example/x.png" alt="f"/>`),
			want: jsonList(
				jsonImage(0, photo, "", `"alt":"a"`), jsonImage(1, photo, "", `"alt":"b"`), jsonImage(2, photo, "", `"alt":"c"`),
				jsonImage(3, banner, "", `"alt":"d"`), jsonImage(4, banner, "", `"alt":"e"`), jsonImage(5, banner, "", `"alt":"f"`))},
		{name: "an image of an attachment that is not imported, and the like",
			text: wpImage(`{"id":24}`, imgOf("https://example.com/wp-content/uploads/2026/10/lost.jpg", "a", 24)) +
				wpImage(`{"id":99}`, imgOf("https://other.example/x.jpg", "b", 99)) +
				wpImage(``, `<img src="https://other.example/x.jpg" alt="c"/>`) +
				wpImage(``, `<img alt="d"/>`) +
				wpImage(`{"id":12}`, imgOf(photoURL, "e", 12)),
			want: jsonList(
				jsonLegacy(0, wpImage(`{"id":24}`, imgOf("https://example.com/wp-content/uploads/2026/10/lost.jpg", "a", 24)), notImage),
				jsonLegacy(1, wpImage(`{"id":99}`, imgOf("https://other.example/x.jpg", "b", 99)), notImage),
				jsonLegacy(2, wpImage(``, `<img src="https://other.example/x.jpg" alt="c"/>`), notImage),
				jsonLegacy(3, wpImage(``, `<img alt="d"/>`), notImage),
				jsonLegacy(4, wpImage(`{"id":12}`, imgOf(photoURL, "e", 12)), notImage)),
			lines: []string{
				legacyLine(notImage, "/body/0"), legacyLine(notImage, "/body/1"), legacyLine(notImage, "/body/2"),
				legacyLine(notImage, "/body/3"), legacyLine(notImage, "/body/4"),
			}},
		{name: "a PDF is not an image",
			text:  wpImage(`{"id":23}`, imgOf(reportURL, "a", 23)),
			want:  jsonList(jsonLegacy(0, wpImage(`{"id":23}`, imgOf(reportURL, "a", 23)), pdfImage)),
			lines: []string{legacyLine(pdfImage, "/body/0")}},
		{name: "an image in the div that WordPress wrote around an aligned image",
			text: wpBlock("image", `{"id":21,"align":"center"}`, `<div class="wp-block-image"><figure class="aligncenter size-large">`+
				imgOf(photoURL, "a", 21)+`<figcaption>Caption</figcaption></figure></div>`),
			want: jsonList(jsonImage(0, photo, "", `"alt":"a","caption":[{"type":"text","text":"Caption"}],"align":"center"`))},
		{name: "an image block without an image",
			text:  wpBlock("image", `{"id":21}`, `<figure class="wp-block-image"></figure>`),
			want:  jsonList(jsonLegacy(0, wpBlock("image", `{"id":21}`, `<figure class="wp-block-image"></figure>`), "the image block has no image")),
			lines: []string{legacyLine("the image block has no image", "/body/0")}},
		{name: "an image that is not in a figure",
			text:  wpBlock("image", `{"id":21}`, `<p>`+imgOf(photoURL, "a", 21)+`</p>`),
			want:  jsonList(jsonLegacy(0, wpBlock("image", `{"id":21}`, `<p>`+imgOf(photoURL, "a", 21)+`</p>`), notAsWritten+"<figure> or <div>")),
			lines: []string{legacyLine(notAsWritten+"<figure> or <div>", "/body/0")}},
		{name: "a caption that has an image",
			text: wpImage(`{"id":21}`, imgOf(photoURL, "a", 21)+`<figcaption>x <img src="y.png"></figcaption>`),
			// The image took the ID 0.
			want: jsonList(jsonLegacy(1, wpImage(`{"id":21}`, imgOf(photoURL, "a", 21)+`<figcaption>x <img src="y.png"></figcaption>`),
				"the text has <img>, which rich text cannot have")),
			lines: []string{legacyLine("the text has <img>, which rich text cannot have", "/body/0")}},
		{name: "an image that has attributes which are not JSON",
			text:  wpImage(`{bad}`, imgOf(photoURL, "a", 21)),
			want:  jsonList(jsonImage(0, photo, "", `"alt":"a"`)),
			lines: []string{`changed: the attributes of the block "core/image" are not valid JSON; they are ignored -> /body/0`}},
	})
}

func TestMediaLinkBaseCountsAsHTML(t *testing.T) {
	// The link around an image is resolved against the link of the item, as a link in a text is (TestLinkBaseCountsAsHTML):
	// what resolving adds to the address counts among the bytes of HTML that the conversion reads.
	path := longBasePath()
	itemLink := "https://example.com" + path
	image := func(n int) string {
		return wpImage(`{"id":21}`, fmt.Sprintf(`<a href="%d">`, n)+imgOf(photoURL, "a", 21)+`</a>`)
	}
	c, res := linkBytesBody(t, mediaImport(t), itemLink, len(itemLink), image)
	reason := fmt.Sprintf("the conversion has read more than %d bytes of HTML", maxHTMLBytes)
	// The third image took the ID 2 before its link was over the limit.
	want := withIDs(jsonList(
		jsonImage(0, mediaIDs.photo, "", `"alt":"a","link":{"href":"`+path+`1"}`),
		jsonImage(1, mediaIDs.photo, "", `"alt":"a","link":{"href":"`+path+`2"}`),
		jsonLegacy(3, image(3), reason)))
	if got, _ := res.Blocks.MarshalJSON(); string(got) != want {
		t.Errorf("the blocks are\n%s\nwant\n%s", got, want)
	}
	wantLines(t, c.imp.Report, bodyLines(legacyLine(reason, "/body/2"), stopLine("/body")))
}

func TestMediaAddressesCountTheBase(t *testing.T) {
	// The address of an image and of a file is resolved against the link of the item to find the attachment, and counts the
	// length of the link, the base, as the address of a link does (TestLinkBaseCountsAsHTML): each time that an address without
	// a scheme is resolved, whether the attachment is found or not. The block whose address goes over the limit is a legacy
	// block, and the conversion stops.
	path := longBasePath()
	itemLink := "https://example.com" + path
	photoSrc, reportHref := "../wp-content/uploads/2026/10/photo-scaled.jpg", "../wp-content/uploads/2026/10/report.pdf"
	image := func(src string) func(n int) string {
		return func(n int) string {
			return wpBlock("image", ``, fmt.Sprintf(`<figure class="wp-block-image"><img src="%s?n=%d" alt="a"/></figure>`, src, n))
		}
	}
	file := func(attrs, href string) func(n int) string {
		return func(n int) string {
			return wpBlock("file", attrs, fmt.Sprintf(`<div class="wp-block-file"><a href="%s?n=%d">Report</a></div>`, href, n))
		}
	}
	reason := func() string { return fmt.Sprintf("the conversion has read more than %d bytes of HTML", maxHTMLBytes) }

	t.Run("an image by its src", func(t *testing.T) {
		c, res := linkBytesBody(t, mediaImport(t), itemLink, len(itemLink), image(photoSrc))
		// The third image is not made: the address is over the limit before it takes an ID.
		want := withIDs(jsonList(jsonImage(0, mediaIDs.photo, "", `"alt":"a"`), jsonImage(1, mediaIDs.photo, "", `"alt":"a"`),
			jsonLegacy(2, image(photoSrc)(3), reason())))
		if got, _ := res.Blocks.MarshalJSON(); string(got) != want {
			t.Errorf("the blocks are\n%s\nwant\n%s", got, want)
		}
		wantLines(t, c.imp.Report, bodyLines(legacyLine(reason(), "/body/2"), stopLine("/body")))
	})

	fileJSON := func(n int, label bool) string {
		attrs := fmt.Sprintf(`"media":%q`, mediaIDs.report)
		if label {
			attrs += `,"label":"Report"`
		}
		return jsonBlock("file", n, "", attrs, "")
	}
	byAttribute := func(int) string {
		return wpBlock("file", `{"href":"`+reportHref+`"}`, `<div class="wp-block-file"></div>`)
	}
	for _, tt := range []struct {
		name  string
		unit  func(n int) string
		label bool
	}{
		{"a file by the address of its link", file(``, reportHref), true},
		{"a file by its attribute href, without a link", byAttribute, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, res := linkBytesBody(t, mediaImport(t), itemLink, len(itemLink), tt.unit)
			want := withIDs(jsonList(fileJSON(0, tt.label), fileJSON(1, tt.label), jsonLegacy(2, tt.unit(3), reason())))
			if got, _ := res.Blocks.MarshalJSON(); string(got) != want {
				t.Errorf("the blocks are\n%s\nwant\n%s", got, want)
			}
			wantLines(t, c.imp.Report, bodyLines(legacyLine(reason(), "/body/2"), stopLine("/body")))
		})
	}

	// The count is the HTML of the six blocks and the length of the base for each address without a scheme that is resolved:
	// found or not, and for each of the two addresses of a file; an address with a scheme adds nothing.
	for _, tt := range []struct {
		name        string
		unit        func(n int) string
		resolutions int
	}{
		{"an image that is found", image(photoSrc), 1},
		{"an image that is not found", image("x.png"), 1},
		{"an image by an address with the scheme", image(photoURL), 0},
		{"a file by the address of its link, which is found", file(``, reportHref), 1},
		{"a file by its attribute and by its link, which are not found", file(`{"href":"../x.pdf"}`, "../y.pdf"), 2},
		{"a file by its attribute, which is found", file(`{"href":"`+reportHref+`"}`, "../y.pdf"), 1},
		{"a file by its attribute and its link with the scheme", file(`{"href":"https://other.example/x.pdf"}`, reportURL), 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// The limit leaves room for all the blocks.
			c, res := linkBytesBody(t, mediaImport(t), itemLink, 20*len(itemLink), tt.unit)
			if want := 6 * (blockHTMLBytes(t, tt.unit(1)) + tt.resolutions*len(itemLink)); len(res.Blocks) != 6 || c.stopped || c.htmlBytes != want {
				t.Errorf("the conversion gave %d blocks, stopped = %v and read %d bytes of HTML, want 6, false and %d", len(res.Blocks), c.stopped, c.htmlBytes, want)
			}
		})
	}
}

func TestMediaAttachments(t *testing.T) {
	imp := mediaImport(t)
	c := richConv(imp, "", nil)
	for _, tt := range []struct {
		src  string
		want int64
	}{
		{photoURL, 21},
		{"https://example.com/wp-content/uploads/2026/10/photo-300x200-scaled.jpg", 21},
		{"https://example.com/wp-content/uploads/2026/10/photo-150x150.jpg", 21},
		{"https://example.com/wp-content/uploads/2026/10/photo.jpg", 21},
		{"https://example.com/wp-content/uploads/2026/10/photo.jpg#x", 21},
		{" \thttps://example.com/photo/\n", 21},
		{"/photo/", 21},
		{"?attachment_id=22", 22},
		{"/wp-content/uploads/2026/10/banner.png", 22},
		{"https://EXAMPLE.com/wp-content/uploads/2026/10/banner.png", 22},
		{"https://example.com/about/", 12},
		{"https://example.com/wp-content/uploads/2026/10/other.png", 0},
		{"https://example.com:8080/wp-content/uploads/2026/10/banner.png", 0},
		{"http://other.example/wp-content/uploads/2026/10/banner.png", 0},
		{"data:image/png;base64,AAAA", 0},
		{"mailto:a@example.com", 0},
		{"", 0},
		{"https://example.com/%zz", 0},
	} {
		got, ok, err := c.attachmentByURL(tt.src)
		if err != nil || got != tt.want || ok != (tt.want > 0) {
			t.Errorf("attachmentByURL(%q) = %d, %v, %v, want %d", tt.src, got, ok, err, tt.want)
		}
	}

	for _, tt := range []struct {
		name   string
		id     int64
		accept string
		media  string
		ok     bool
		why    string
	}{
		{"an image", 21, content.MediaImage, mediaIDs.photo, true, ""},
		{"any kind", 23, "", mediaIDs.report, true, ""},
		{"a PDF where an image is wanted", 23, content.MediaImage, "", false, pdfImage},
		{"an entry", 12, "", "", false, ""},
		{"an attachment that is not imported", 24, "", "", false, ""},
		{"a post that is not in the WXR", 99, "", "", false, ""},
		{"no post", 0, "", "", false, ""},
	} {
		media, ok, why := c.attachment(tt.id, tt.accept)
		if media != tt.media || ok != tt.ok || why != tt.why {
			t.Errorf("%s: attachment(%d, %q) = %q, %v, %q, want %q, %v, %q", tt.name, tt.id, tt.accept, media, ok, why, tt.media, tt.ok, tt.why)
		}
	}

	for _, tt := range []struct {
		class string
		want  int64
	}{
		{"wp-image-12", 12}, {"a wp-image-7 b", 7}, {"wp-image-", 0}, {"wp-image-x", 0}, {"wp-image-0", 0}, {"wp-image-1x", 0},
		{"wp-image-99999999999999999999", 0}, {"size-full", 0}, {"", 0},
	} {
		img := richRoot(t, `<img class="`+tt.class+`">`).FirstChild
		if got := imageClassID(img); got != tt.want {
			t.Errorf("imageClassID(%q) = %d, want %d", tt.class, got, tt.want)
		}
	}
}

func TestMediaGalleries(t *testing.T) {
	photo, banner := mediaIDs.photo, mediaIDs.banner
	nested := func(attrs string, blocks ...string) string {
		return wpBlock("gallery", attrs, `<figure class="wp-block-gallery has-nested-images columns-default is-cropped">`+strings.Join(blocks, "\n")+`</figure>`)
	}
	image := func(id int, src, alt string) string {
		return wpBlock("image", fmt.Sprintf(`{"id":%d,"sizeSlug":"large","linkDestination":"none"}`, id),
			`<figure class="wp-block-image size-large">`+imgOf(src, alt, id)+`</figure>`)
	}
	const captionHTML = `<figcaption class="blocks-gallery-caption wp-element-caption">The <em>caption</em></figcaption>`
	jsonCaption := func(n int) string {
		return fmt.Sprintf(`{"type":"paragraph","id":"<%d>","content":[{"type":"text","text":"The "},{"type":"text","text":"caption","marks":[{"type":"italic"}]}]}`, n)
	}
	changed := func(n int, pointer string) string {
		return fmt.Sprintf("changed: the gallery is %s here; whiteroom has no gallery blocks yet (stage 2) -> %s", count(n, "image"), pointer)
	}
	oldItem := func(id, src, alt, caption string) string {
		return `<li class="blocks-gallery-item"><figure><img src="` + src + `" alt="` + alt + `" data-id="` + id + `" class="wp-image-` + id + `"/>` + caption + `</figure></li>`
	}
	old := func(attrs, items string) string {
		return wpBlock("gallery", attrs, `<figure class="wp-block-gallery columns-2 is-cropped"><ul class="blocks-gallery-grid">`+items+`</ul></figure>`)
	}
	sizes := &Settings{Blocks: emptyBlockSettings()}
	sizes.Blocks.ImageSizes["medium"] = "middle"
	externalItem := `<li class="blocks-gallery-item"><figure><img src="https://other.example/x.jpg" alt="Two" data-id="99"/></figure></li>`
	externalHTML := `<li class="blocks-gallery-item"><figure><img src="https://other.example/x.jpg" alt="Two" data-id="99"></figure></li>`
	runMediaCases(t, []textCase{
		{name: "a gallery of WordPress 5.9 and later: its images in its place",
			text: nested(`{"columns":2,"imageCrop":false,"linkTo":"none"}`, image(21, photoURL, "a"), image(22, bannerURL, "b")),
			want: jsonList(jsonImage(0, photo, "", `"alt":"a","size":"large"`), jsonImage(1, banner, "", `"alt":"b","size":"large"`)),
			lines: []string{
				lostLine("columns, imageCrop", "core/gallery", "/body/0"), changed(2, "/body/0"),
			}},
		{name: "the caption of the gallery is a paragraph after the images",
			text:  nested(``, image(21, photoURL, "a"), captionHTML),
			want:  jsonList(jsonImage(0, photo, "", `"alt":"a","size":"large"`), jsonCaption(1)),
			lines: []string{changed(1, "/body/0")}},
		{name: "an image of the gallery that is not imported is a legacy block of its own text",
			text: nested(``, image(21, photoURL, "a"), image(99, "https://other.example/x.jpg", "b")),
			want: jsonList(jsonImage(0, photo, "", `"alt":"a","size":"large"`),
				jsonLegacy(1, image(99, "https://other.example/x.jpg", "b"), notImage)),
			lines: []string{changed(2, "/body/0"), legacyLine(notImage, "/body/1")}},
		{name: "the other blocks of the gallery too", text: nested(``, wpPara("x"), image(21, photoURL, "a")),
			want:  jsonList(jsonPara(0, "x"), jsonImage(1, photo, "", `"alt":"a","size":"large"`)),
			lines: []string{changed(2, "/body/0")}},
		{name: "a gallery of WordPress before 5.9: the images are in its HTML",
			text: old(`{"ids":[21,22],"columns":2,"sizeSlug":"medium"}`,
				oldItem("21", photoURL, "One", `<figcaption class="blocks-gallery-item__caption">Cap <b>one</b></figcaption>`)+oldItem("22", bannerURL, "", "")),
			settings: sizes,
			want: jsonList(
				jsonImage(0, photo, "", `"alt":"One","caption":[{"type":"text","text":"Cap "},{"type":"text","text":"one","marks":[{"type":"bold"}]}],"size":"middle"`),
				jsonImage(1, banner, "", `"size":"middle"`)),
			lines: []string{lostLine("columns", "core/gallery", "/body/0"), changed(2, "/body/0"), noAltLine + "/body/1/attrs/alt"}},
		{name: "an old gallery with links, the caption of the gallery, and an item that is not imported",
			text: wpBlock("gallery", `{"linkTo":"media","sizeSlug":"full"}`,
				`<figure class="wp-block-gallery columns-3 is-cropped"><ul class="blocks-gallery-grid"><li class="blocks-gallery-item"><figure><a href="`+photoURL+`">`+
					`<img src="`+photoURL+`" alt="One" data-id="21"/></a></figure></li>`+externalItem+`</ul><figcaption class="blocks-gallery-caption">Old caption</figcaption></figure>`),
			want: jsonList(jsonImage(0, photo, "", `"alt":"One","link":{"media":"`+photo+`"}`),
				jsonLegacy(1, externalHTML, notImage),
				`{"type":"paragraph","id":"<2>","content":[{"type":"text","text":"Old caption"}]}`),
			lines: []string{changed(2, "/body/0"), legacyLine(notImage, "/body/1")}},
		{name: "the images of an old gallery by their files, if they have no data-id",
			text: wpBlock("gallery", ``, `<ul class="wp-block-gallery columns-1 is-cropped"><li class="blocks-gallery-item"><figure><img src="`+photoURL+`" alt="One"/></figure></li></ul>`),
			want: jsonList(jsonImage(0, photo, "", `"alt":"One"`)), lines: []string{changed(1, "/body/0")}},
		{name: "an old gallery of figures",
			text: wpBlock("gallery", ``, `<figure class="wp-block-gallery"><figure><img src="`+photoURL+`" alt="One" data-id="21"/></figure><figure><img src="`+bannerURL+`" alt="Two" data-id="22"/></figure></figure>`),
			want: jsonList(jsonImage(0, photo, "", `"alt":"One"`), jsonImage(1, banner, "", `"alt":"Two"`)), lines: []string{changed(2, "/body/0")}},
		{name: "an item of an old gallery without an image, and one with a caption that has an image",
			text: old(``, `<li>text only</li><li><figure><img src="`+photoURL+`" alt="a" data-id="21"/><figcaption><img src="x.png"></figcaption></figure></li>`),
			want: jsonList(
				jsonLegacy(0, `<li>text only</li>`, "the gallery item has no image"),
				// The image of the second item took the ID 1.
				jsonLegacy(2, `<li><figure><img src="`+photoURL+`" alt="a" data-id="21"><figcaption><img src="x.png"></figcaption></figure></li>`, "the text has <img>, which rich text cannot have")),
			lines: []string{
				changed(2, "/body/0"), legacyLine("the gallery item has no image", "/body/0"),
				legacyLine("the text has <img>, which rich text cannot have", "/body/1"),
			}},
		{name: "the anchor, the class and the style of the gallery are lost with it",
			text: wpBlock("gallery", `{"anchor":"g","className":"wide","style":{"color":{"text":"red"}},"align":"wide","fixedHeight":true,"randomOrder":true,"aspectRatio":"1","linkTarget":"_blank","allowResize":true,"shortCodeTransforms":[]}`,
				`<figure id="g" class="wp-block-gallery has-nested-images">`+image(21, photoURL, "a")+`</figure>`),
			want: jsonList(jsonImage(0, photo, "", `"alt":"a","size":"large"`)),
			lines: []string{
				lostLine("anchor, className, style, randomOrder, fixedHeight, aspectRatio, align", "core/gallery", "/body/0"), changed(1, "/body/0"),
			}},
		{name: "a caption of the gallery that has no text is left out",
			text: nested(``, image(21, photoURL, "a"), `<figcaption class="blocks-gallery-caption"> </figcaption>`) +
				nested(``, image(21, photoURL, "b"), `<figcaption class="blocks-gallery-caption">`+string(rune(0x11))+`</figcaption>`),
			// The captions took the IDs 1 and 3.
			want:  jsonList(jsonImage(0, photo, "", `"alt":"a","size":"large"`), jsonImage(2, photo, "", `"alt":"b","size":"large"`)),
			lines: []string{changed(1, "/body/0"), changed(1, "/body/1")}},
		{name: "the anchor of the gallery in its HTML", text: wpBlock("gallery", ``, `<figure id="g" class="wp-block-gallery has-nested-images">`+image(21, photoURL, "a")+`</figure>`),
			want:  jsonList(jsonImage(0, photo, "", `"alt":"a","size":"large"`)),
			lines: []string{lostLine("anchor", "core/gallery", "/body/0"), changed(1, "/body/0")}},
		{name: "the image of an old gallery by its data-id only",
			text: old(``, `<li><figure><img src="https://other.example/x.jpg" alt="One" data-id="21"/></figure></li>`),
			want: jsonList(jsonImage(0, photo, "", `"alt":"One"`)), lines: []string{changed(1, "/body/0")}},
		{name: "a gallery without images is left out", text: nested(``) + old(``, "") + wpBlock("gallery", ``, `<ul class="wp-block-gallery"></ul>`),
			want: `[]`,
			lines: []string{
				"skipped: the empty gallery is removed -> /body", "skipped: the empty gallery is removed -> /body", "skipped: the empty gallery is removed -> /body",
			}},
		{name: "a gallery with a caption and no images is a paragraph",
			text:  wpBlock("gallery", ``, `<figure class="wp-block-gallery">`+captionHTML+`</figure>`),
			want:  jsonList(jsonCaption(0)),
			lines: []string{changed(0, "/body/0")}},
		{name: "text in the gallery outside its blocks", text: wpBlock("gallery", ``, `<figure class="wp-block-gallery">text`+image(21, photoURL, "a")+`</figure>`),
			want:  jsonList(jsonImage(0, photo, "", `"alt":"a","size":"large"`)),
			lines: []string{`skipped: the HTML of the block "core/gallery" outside its inner blocks is not imported -> /body`, changed(1, "/body/0")}},
		{name: "a gallery that is not a figure or a list", text: wpBlock("gallery", ``, `<div>x</div>`),
			want:  jsonList(jsonLegacy(0, wpBlock("gallery", ``, `<div>x</div>`), notAsWritten+"<figure> or <ul>")),
			lines: []string{legacyLine(notAsWritten+"<figure> or <ul>", "/body/0")}},
		{name: "a gallery in a group",
			text: wpGroup("", "div", nested(``, image(21, photoURL, "a"), image(22, bannerURL, "b")), wpPara("after")),
			want: jsonList(jsonBlock("group", 0, "", "", jsonList(jsonImage(1, photo, "", `"alt":"a","size":"large"`),
				jsonImage(2, banner, "", `"alt":"b","size":"large"`), jsonPara(3, "after")))),
			lines: []string{changed(2, "/body/0/content/0")}},
	})
}

func TestMediaFiles(t *testing.T) {
	report, photo := mediaIDs.report, mediaIDs.photo
	file := func(attrs, inner string) string {
		return wpBlock("file", attrs, `<div class="wp-block-file">`+inner+`</div>`)
	}
	button := func(href string) string {
		return `<a href="` + href + `" class="wp-block-file__button wp-element-button" download aria-describedby="x">Download</a>`
	}
	jsonFile := func(n int, media, keys, attrs string) string {
		return jsonBlock("file", n, keys, fmt.Sprintf(`"media":%q%s`, media, attrs), "")
	}
	notFile := "the file is not an imported attachment"
	runMediaCases(t, []textCase{
		{name: "a PDF with a link and a button",
			text: file(`{"id":23,"href":"`+reportURL+`"}`, `<a id="wp-block-file--media-1" href="`+reportURL+`">Annual report</a>`+button(reportURL)),
			want: jsonList(jsonFile(0, report, "", `,"label":"Annual report"`))},
		{name: "a file that shows a preview, and the target of its link",
			text: file(`{"id":23,"displayPreview":true,"textLinkTarget":"_blank","showDownloadButton":false}`,
				`<object class="wp-block-file__embed" data="`+reportURL+`" type="application/pdf" style="width:100%;height:600px"></object><a href="`+reportURL+`" target="_blank" rel="noreferrer noopener">Report</a>`),
			want:  jsonList(jsonFile(0, report, "", `,"label":"Report"`)),
			lines: []string{lostLine("displayPreview, textLinkTarget", "core/file", "/body/0")}},
		{name: "a preview that is off",
			text: file(`{"id":23,"displayPreview":false}`, `<a href="`+reportURL+`">Report</a>`),
			want: jsonList(jsonFile(0, report, "", `,"label":"Report"`))},
		{name: "the attachment by the address of the link, which is the first one without download",
			text: file(``, button(reportURL)+`<a href="`+reportURL+`">Report</a>`) + file(``, `<a href="/wp-content/uploads/2026/10/report.pdf">Relative</a>`),
			want: jsonList(jsonFile(0, report, "", `,"label":"Report"`), jsonFile(1, report, "", `,"label":"Relative"`))},
		{name: "the attachment by the attribute href", text: file(`{"href":"`+reportURL+`"}`, `<a href="https://other.example/x.pdf">Other</a>`),
			want: jsonList(jsonFile(0, report, "", `,"label":"Other"`))},
		{name: "an image is a file too", text: file(`{"id":21}`, `<a href="`+photoURL+`">Photo</a>`),
			want: jsonList(jsonFile(0, photo, "", `,"label":"Photo"`))},
		{name: "a file without a link has no label", text: file(`{"id":23}`, ``),
			want: jsonList(jsonFile(0, report, "", ""))},
		{name: "a label with marks and a line break", text: file(`{"id":23}`, `<a href="`+reportURL+`"><strong>Annual</strong><br>report</a>`),
			want:  jsonList(jsonFile(0, report, "", `,"label":"Annual report"`)),
			lines: []string{"changed: kept only the text of <strong> -> /body/0"}},
		{name: "the anchor and the class of the file", text: wpBlock("file", `{"id":23,"className":"docs"}`,
			`<div id="docs" class="wp-block-file docs"><a href="`+reportURL+`">Report</a></div>`),
			want: jsonList(jsonFile(0, report, `"className":"docs","anchor":"docs"`, `,"label":"Report"`))},
		{name: "files that are not imported",
			text: file(`{"id":24}`, `<a href="https://example.com/wp-content/uploads/2026/10/lost.jpg">Lost</a>`) +
				file(``, `<a href="https://other.example/x.pdf">Other</a>`) + file(``, `<a>Nothing</a>`) + file(`{"id":12}`, `<a href="`+reportURL+`">Page</a>`),
			want: jsonList(
				jsonLegacy(0, file(`{"id":24}`, `<a href="https://example.com/wp-content/uploads/2026/10/lost.jpg">Lost</a>`), notFile),
				jsonLegacy(1, file(``, `<a href="https://other.example/x.pdf">Other</a>`), notFile),
				jsonLegacy(2, file(``, `<a>Nothing</a>`), notFile),
				jsonLegacy(3, file(`{"id":12}`, `<a href="`+reportURL+`">Page</a>`), notFile)),
			lines: []string{legacyLine(notFile, "/body/0"), legacyLine(notFile, "/body/1"), legacyLine(notFile, "/body/2"), legacyLine(notFile, "/body/3")}},
		{name: "a file that is not in a div", text: wpBlock("file", `{"id":23}`, `<p><a href="`+reportURL+`">Report</a></p>`),
			want:  jsonList(jsonLegacy(0, wpBlock("file", `{"id":23}`, `<p><a href="`+reportURL+`">Report</a></p>`), notAsWritten+"<div>")),
			lines: []string{legacyLine(notAsWritten+"<div>", "/body/0")}},
	})
}

func TestMediaEmbeds(t *testing.T) {
	const (
		youtube = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
		vimeo   = "https://vimeo.com/123456"
		tweet   = "https://twitter.com/jack/status/20"
		maps    = "https://www.google.com/maps/embed?pb=!1m18"
	)
	embed := func(attrs, wrapper, extra string) string {
		return wpBlock("embed", attrs, `<figure class="wp-block-embed"><div class="wp-block-embed__wrapper">`+wrapper+`</div>`+extra+`</figure>`)
	}
	oldEmbed := func(name, attrs, wrapper string) string {
		return wpBlock(name, attrs, `<figure class="wp-block-embed"><div class="wp-block-embed__wrapper">`+wrapper+`</div></figure>`)
	}
	jsonEmbed := func(n int, keys, provider, href, caption string) string {
		attrs := fmt.Sprintf(`"provider":%q,"url":%q`, provider, href)
		if caption != "" {
			attrs += `,"caption":` + caption
		}
		return jsonBlock("embed", n, keys, attrs, "")
	}
	cannot := func(why string) string { return "the URL cannot be embedded: " + why }
	// The embeds that become legacy blocks, one after the other.
	var texts, wants, lines []string
	for i, c := range []struct{ text, reason string }{
		{embed(`{"url":"https://www.youtube.com/playlist?list=PL1","providerNameSlug":"youtube"}`, "x", ""), cannot("not a URL that youtube can embed")},
		{embed(`{"url":"http://vimeo.com/123456","providerNameSlug":"vimeo"}`, "x", ""), cannot("must be an https URL")},
		{embed(`{"url":"https://vimeo.com:8080/123456"}`, "x", ""), cannot("not a URL that a provider can embed")},
		{embed(`{"url":"javascript:alert(1)"}`, "x", ""), cannot(`scheme "javascript" is not allowed`)},
		{embed(`{"url":"https://example.com/x"}`, "x", ""), cannot("not a URL that a provider can embed")},
		{embed(`{"providerNameSlug":"youtube"}`, "\n\n", ""), "the embed has no URL"},
		{embed(`{"providerNameSlug":"instagram","url":"https://instagram.com/p/x"}`, "x", ""), `embeds of "instagram" are not allowed`},
		{oldEmbed("core-embed/spotify", `{"url":"https://open.spotify.com/track/x"}`, "x"), `embeds of "spotify" are not allowed`},
		{wpBlock("embed", `{"url":"`+youtube+`"}`, `<p>`+youtube+`</p>`), notAsWritten + "<figure> or <div>"},
	} {
		texts = append(texts, c.text)
		wants = append(wants, jsonLegacy(i, c.text, c.reason))
		lines = append(lines, legacyLine(c.reason, fmt.Sprintf("/body/%d", i)))
	}
	runMediaCases(t, []textCase{
		{name: "a video of YouTube with a caption, and what WordPress adds",
			text: embed(`{"url":"`+youtube+`","type":"video","providerNameSlug":"youtube","responsive":true,"className":"wp-embed-aspect-16-9 wp-has-aspect-ratio"}`,
				"\n"+youtube+"\n", `<figcaption class="wp-element-caption">Our <em>video</em></figcaption>`),
			want: jsonList(jsonEmbed(0, "", "youtube", youtube, `[{"type":"text","text":"Our "},{"type":"text","text":"video","marks":[{"type":"italic"}]}]`))},
		{name: "the address in the wrapper, if the block has none",
			text: embed(`{"providerNameSlug":"vimeo"}`, "\n"+vimeo+"/0a1b2c3d4e\n", ""),
			want: jsonList(jsonEmbed(0, "", "vimeo", vimeo+"/0a1b2c3d4e", ""))},
		{name: "the address with the character that HTML writes with a reference",
			text: embed(`{"providerNameSlug":"youtube"}`, "https://www.youtube.com/watch?v=dQw4w9WgXcQ&amp;t=1", ""),
			want: jsonList(jsonEmbed(0, "", "youtube", youtube+"&t=1", ""))},
		{name: "the embeds of WordPress before 5.6",
			text: oldEmbed("core-embed/twitter", `{"url":"`+tweet+`","type":"rich","className":"is-style-wide"}`, "\n"+tweet+"\n") +
				oldEmbed("core-embed/youtube", ``, "\n"+youtube+"\n") + oldEmbed("core-embed/vimeo", `{"url":"`+vimeo+`"}`, vimeo),
			want: jsonList(jsonEmbed(0, `"variant":"wide"`, "x", tweet, ""), jsonEmbed(1, "", "youtube", youtube, ""), jsonEmbed(2, "", "vimeo", vimeo, ""))},
		{name: "the name of the provider in the attribute, over the name of the block",
			text: oldEmbed("core-embed/vimeo", `{"providerNameSlug":"twitter","url":"`+tweet+`"}`, tweet) + embed(`{"providerNameSlug":"x","url":"`+tweet+`"}`, tweet, ""),
			want: jsonList(jsonEmbed(0, "", "x", tweet, ""), jsonEmbed(1, "", "x", tweet, ""))},
		{name: "an embed without the name of a provider: the first that takes the address",
			text: embed(``, youtube, "") + embed(``, vimeo, "") + embed(``, "https://x.com/jack/status/20", "") + embed(`{"url":"`+maps+`"}`, "x", ""),
			want: jsonList(jsonEmbed(0, "", "youtube", youtube, ""), jsonEmbed(1, "", "vimeo", vimeo, ""),
				jsonEmbed(2, "", "x", "https://x.com/jack/status/20", ""), jsonEmbed(3, "", "googleMaps", maps, ""))},
		{name: "the alignment of an embed is lost", text: embed(`{"url":"`+youtube+`","align":"wide"}`, youtube, ""),
			want: jsonList(jsonEmbed(0, "", "youtube", youtube, "")), lines: []string{lostLine("align", "core/embed", "/body/0")}},
		{name: "an embed that has attributes which are not JSON", text: embed(`{bad}`, youtube, ""),
			want:  jsonList(jsonEmbed(0, "", "youtube", youtube, "")),
			lines: []string{`changed: the attributes of the block "core/embed" are not valid JSON; they are ignored -> /body/0`}},
		{name: "embeds that become legacy blocks", text: strings.Join(texts, ""), want: jsonList(wants...), lines: lines},
	})
}

func TestMediaVideoAndAudio(t *testing.T) {
	video := wpBlock("video", `{"id":5}`, `<figure class="wp-block-video"><video controls src="x.mp4"></video></figure>`)
	audio := wpBlock("audio", `{"id":6}`, `<figure class="wp-block-audio"><audio controls src="x.mp3"></audio></figure>`)
	runMediaCases(t, []textCase{
		{name: "video and audio", text: video + audio,
			want: jsonList(jsonLegacy(0, video, "whiteroom has no video blocks yet (stage 2)"), jsonLegacy(1, audio, "whiteroom has no audio blocks yet (stage 2)")),
			lines: []string{
				legacyLine("whiteroom has no video blocks yet (stage 2)", "/body/0"), legacyLine("whiteroom has no audio blocks yet (stage 2)", "/body/1"),
			}},
	})
}

func TestMediaConvertersAreRegistered(t *testing.T) {
	for _, name := range []string{"core/image", "core/gallery", "core/file", "core/embed", "core/video", "core/audio"} {
		if converters[name] == nil {
			t.Errorf("%s has no converter", name)
		}
		if dynamicBlock(name) {
			t.Errorf("%s is a dynamic block", name)
		}
	}
	if converterPrefixes["core-embed/"] == nil {
		t.Error("core-embed/ has no converter")
	}
}

func TestMediaPrefixOfAConverter(t *testing.T) {
	// Only the blocks of the family go to its converter.
	other := `<!-- wp:acme/embed {"url":"https://www.youtube.com/watch?v=dQw4w9WgXcQ"} /-->`
	plain := `<!-- wp:core-embed {"url":"https://www.youtube.com/watch?v=dQw4w9WgXcQ"} /-->`
	runMediaCases(t, []textCase{
		{name: "blocks that are not of the family", text: other + plain,
			want:  jsonList(jsonLegacy(0, other, notConverted("acme/embed")), jsonLegacy(1, plain, notConverted("core/core-embed"))),
			lines: []string{legacyLine(notConverted("acme/embed"), "/body/0"), legacyLine(notConverted("core/core-embed"), "/body/1")}},
	})
}

func TestMediaItemsOfAnOldGalleryCountAsBlocks(t *testing.T) {
	lowerLimit(t, &maxWPBlocks, 3)
	item := `<li><figure><img src="` + photoURL + `" alt="a" data-id="21"/></figure></li>`
	imp := mediaImport(t)
	res := convertMedia(t, imp, bodySite(false), nil, wpBlock("gallery", ``, `<ul class="wp-block-gallery">`+strings.Repeat(item, 5)+`</ul>`))
	// The gallery is the first block, and two of its items are the second and the third.
	if len(res.Blocks) != 2 {
		t.Errorf("there are %d blocks, want 2", len(res.Blocks))
	}
	wantLines(t, imp.Report, bodyLines(
		fmt.Sprintf("skipped: the rest of the content is not imported: the conversion reads at most 3 blocks and %d bytes of HTML -> /body", maxHTMLBytes),
		"changed: the gallery is 2 images here; whiteroom has no gallery blocks yet (stage 2) -> /body/0"))
}

func TestMediaPartsThatAreTooLong(t *testing.T) {
	// The HTML of a part of a block that is made a legacy block must fit in a legacy block.
	long := strings.Repeat("a", content.MaxTextBytes)
	item := `<li class="blocks-gallery-item"><figure><img src="https://other.example/x.jpg" alt="` + long + `" data-id="99"></figure></li>`
	text := wpBlock("gallery", ``, `<ul class="wp-block-gallery">`+item+`</ul>`)
	imp := mediaImport(t)
	res := convertMedia(t, imp, bodySite(false), nil, text)
	if len(res.Blocks) != 0 {
		t.Errorf("the blocks are %d", len(res.Blocks))
	}
	wantLines(t, imp.Report, bodyLines(
		fmt.Sprintf("skipped: a part of the block is not imported: its HTML is too long: %d bytes (max %d) -> /body", len(item), content.MaxTextBytes),
		"skipped: the empty gallery is removed -> /body"))
}

func TestMediaGuardLeavesOutACaptionAndABackground(t *testing.T) {
	// A caption or a background that does not pass is left out: a legacy block of the gallery or the cover would hold the
	// text of the images and of the blocks next to it too. The caption is longer than a string may be, which the budget of a
	// rich text stops before the guard sees it, so the budget is raised here.
	lowerLimit(t, &maxRichBytes, 4*content.MaxTextBytes)
	long := strings.Repeat("a", content.MaxTextBytes+1)
	gallery := wpBlock("gallery", ``, `<figure class="wp-block-gallery has-nested-images">`+
		wpBlock("image", `{"id":21}`, `<figure class="wp-block-image">`+imgOf(photoURL, "a", 21)+`</figure>`)+
		`<figcaption>`+long+`</figcaption></figure>`)
	cover := wpBlock("cover", `{"id":21,"url":"`+photoURL+`","alt":"`+long+`"}`,
		`<div class="wp-block-cover"><img class="wp-block-cover__image-background wp-image-21" alt="" src="`+photoURL+`"/><div class="wp-block-cover__inner-container">`+wpPara("in")+`</div></div>`)
	for _, tt := range []struct {
		name, text string
		want       string
		lines      []string
	}{
		{"the caption of a gallery", gallery, jsonList(jsonImage(0, mediaIDs.photo, "", `"alt":"a"`)),
			[]string{
				fmt.Sprintf("skipped: the block is not imported: /1/content/0/text: too long: %d bytes (max %d) -> /body", len(long), content.MaxTextBytes),
				"changed: the gallery is 1 image here; whiteroom has no gallery blocks yet (stage 2) -> /body/0",
			}},
		{"the image in the background of a cover", cover,
			jsonList(jsonBlock("group", 0, "", "", jsonList(jsonPara(2, "in")))),
			[]string{
				fmt.Sprintf("skipped: the block is not imported: /0/attrs/alt: too long: %d bytes (max %d) -> /body/0/content", len(long), content.MaxTextBytes),
				"changed: the cover is a group here; whiteroom has no cover blocks yet (stage 2) -> /body/0",
			}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			imp := mediaImport(t)
			res, err := imp.Body(BodyOptions{Site: bodySite(false), Item: bodyItem(tt.text), Target: bodyTarget, Key: bodyKey})
			if err != nil {
				t.Fatal(err)
			}
			checkBody(t, imp, bodySite(false), res)
			got, _ := res.Blocks.MarshalJSON()
			if want := withIDs(tt.want); string(got) != want {
				t.Errorf("the blocks are\n%s\nwant\n%s", got, want)
			}
			wantLines(t, imp.Report, bodyLines(tt.lines...))
		})
	}
}

func TestMediaGalleryCaptionThatCannotBeText(t *testing.T) {
	// A caption that the budget of a rich text stops, or that has an image, is left out with a note at the gallery, with the
	// budget as it is. The images stay: a legacy block of the gallery would be one of the text of all of them.
	long := strings.Repeat("a", content.MaxTextBytes+1)
	budget := fmt.Sprintf("the text has more than %d bytes, counting %d for each piece of text and each break", maxRichBytes, richPieceBytes)
	image := wpBlock("image", `{"id":21}`, `<figure class="wp-block-image">`+imgOf(photoURL, "a", 21)+`</figure>`)
	oldGallery := func(caption string) string {
		return wpBlock("gallery", ``, `<figure class="wp-block-gallery"><ul class="blocks-gallery-grid"><li class="blocks-gallery-item"><figure>`+
			`<img src="`+photoURL+`" alt="a" data-id="21"/></figure></li></ul><figcaption>`+caption+`</figcaption></figure>`)
	}
	nestedGallery := func(caption string) string {
		return wpBlock("gallery", ``, `<figure class="wp-block-gallery has-nested-images">`+image+`<figcaption>`+caption+`</figcaption></figure>`)
	}
	for _, tt := range []struct {
		name, text, reason string
	}{
		{"a caption over the budget", nestedGallery(long), budget},
		{"a caption over the budget of an old gallery", oldGallery(long), budget},
		{"a caption with an image", nestedGallery(`x <img src="y.png">`), "the text has <img>, which rich text cannot have"},
		{"a caption with an image of an old gallery", oldGallery(`x <img src="y.png">`), "the text has <img>, which rich text cannot have"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			imp := mediaImport(t)
			res := convertMedia(t, imp, bodySite(false), nil, tt.text)
			got, _ := res.Blocks.MarshalJSON()
			if want := withIDs(jsonList(jsonImage(0, mediaIDs.photo, "", `"alt":"a"`))); string(got) != want {
				t.Errorf("the blocks are\n%s\nwant\n%s", got, want)
			}
			wantLines(t, imp.Report, bodyLines(
				"skipped: the caption of the gallery is not imported: "+tt.reason+" -> /body",
				"changed: the gallery is 1 image here; whiteroom has no gallery blocks yet (stage 2) -> /body/0"))
		})
	}
}

func TestMediaOrigins(t *testing.T) {
	// The guard can make a legacy block of the block of WordPress that a block was made from, if it knows which it is.
	photo := wpBlock("image", `{"id":21,"sizeSlug":"large"}`, `<figure class="wp-block-image">`+imgOf(photoURL, "a", 21)+`</figure>`)
	nested := wpBlock("gallery", ``, `<figure class="wp-block-gallery has-nested-images">`+photo+`<figcaption>Caption</figcaption></figure>`)
	old := wpBlock("gallery", ``, `<ul class="wp-block-gallery"><li><figure><img src="`+photoURL+`" alt="a" data-id="21"/></figure></li></ul>`)
	for _, tt := range []struct {
		name, text string
		want       []string // the name of the block of WordPress that each block is made from, or "-" for a block that is left out
	}{
		{"a gallery of WordPress 5.9 and later", nested, []string{"core/image", "-"}},
		{"an old gallery", old, []string{"core/gallery"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := newBodyConv(mediaImport(t), BodyOptions{Site: bodySite(false), Item: bodyItem(tt.text), Target: bodyTarget, Key: bodyKey})
			blocks, _ := c.run()
			if len(blocks) != len(tt.want) {
				t.Fatalf("the blocks are %d, want %d", len(blocks), len(tt.want))
			}
			for i, blk := range blocks {
				o, ok := c.origins[blk.ID]
				switch {
				case !ok:
					t.Errorf("the block %d has no origin", i)
				case tt.want[i] == "-" && !o.guard, tt.want[i] != "-" && (o.guard || o.b.Name != tt.want[i]):
					t.Errorf("the origin of the block %d is %+v, want %s", i, o, tt.want[i])
				}
			}
		})
	}
}

func TestMediaBlocksAttributesOfAnyType(t *testing.T) {
	// Whatever type an attribute has, the block converts to blocks that pass Validate, and the guard has nothing to do.
	img := imgOf(photoURL, "A", 21)
	buttonHTML := wpButton(``, buttonLink("/a", "A"))
	columnHTML := wpColumn(``, wpPara("a"))
	figure := func(inner string) string { return `<figure class="wp-block-image">` + inner + `</figure>` }
	galleryImage := wpBlock("image", `{"id":21}`, figure(img))
	cover := `<div class="wp-block-cover"><img class="wp-block-cover__image-background wp-image-21" src="` + photoURL + `"/>` +
		`<div class="wp-block-cover__inner-container">` + wpPara("a") + `</div></div>`
	common := []string{"className", "anchor", "style", "textColor", "backgroundColor", "gradient", "fontSize", "align"}
	with := func(own ...string) []string { return append(own, common...) }
	for _, blk := range []struct {
		name, markup string
		attrs        []string
	}{
		{"image", figure(`<a href="` + photoURL + `">` + img + `</a><figcaption>c</figcaption>`),
			with("id", "sizeSlug", "alt", "isDecorative", "width", "height", "scale", "aspectRatio", "title", "focalPoint", "lightbox")},
		{"gallery", `<figure class="wp-block-gallery has-nested-images">` + galleryImage + `</figure>`,
			with("sizeSlug", "columns", "imageCrop", "linkTo", "linkTarget", "randomOrder", "fixedHeight", "aspectRatio")},
		{"gallery", `<ul class="wp-block-gallery"><li><figure><img src="` + photoURL + `" data-id="21" alt="a"/></figure></li></ul>`,
			[]string{"sizeSlug", "ids", "columns"}},
		{"file", `<div class="wp-block-file"><a href="` + reportURL + `">R</a></div>`, with("id", "href", "displayPreview", "textLinkTarget")},
		{"embed", `<figure class="wp-block-embed"><div class="wp-block-embed__wrapper">https://vimeo.com/123456</div></figure>`,
			with("url", "providerNameSlug", "type")},
		{"buttons", `<div class="wp-block-buttons">` + buttonHTML + `</div>`, with("layout", "contentJustification")},
		{"button", `<div class="wp-block-button"><a class="wp-block-button__link" href="/a">A</a></div>`, with("width", "textAlign", "borderRadius", "tagName")},
		{"columns", `<div class="wp-block-columns">` + columnHTML + columnHTML + `</div>`, with("verticalAlignment", "isStackedOnMobile")},
		{"media-text", `<div class="wp-block-media-text"><figure class="wp-block-media-text__media">` + img + `</figure><div class="wp-block-media-text__content">` + wpPara("t") + `</div></div>`,
			with("mediaId", "mediaType", "mediaWidth", "mediaPosition", "mediaSizeSlug", "mediaAlt", "verticalAlignment", "isStackedOnMobile", "imageFill", "focalPoint", "useFeaturedImage")},
		{"cover", cover, with("id", "url", "alt", "backgroundType", "dimRatio", "overlayColor", "customOverlayColor", "customGradient", "hasParallax", "isRepeated",
			"focalPoint", "minHeight", "contentPosition", "useFeaturedImage", "tagName", "layout")},
		{"block", "", []string{"ref", "content"}},
		{"contact-form-7/contact-form-selector", `<div class="wp-block-contact-form-7-contact-form-selector">[contact-form-7]</div>`, with("id", "hash", "title")},
	} {
		for _, attr := range blk.attrs {
			for _, value := range []string{
				`null`, `true`, `false`, `0`, `-1`, `1.5`, `21`, `60`, `31`, `"x"`, `""`, `"21"`, `"60"`, `[]`, `[1]`, `{}`, `{"a":1}`,
				`"var:preset|color|x"`, `"large"`, `"1rem"`, `"50%"`,
			} {
				text := wpBlock(blk.name, `{"`+attr+`":`+value+`}`, blk.markup)
				// Inside columns too, where the blocks that hold columns are groups.
				for _, in := range []string{text, wpColumns(``, wpColumn(``, text))} {
					imp := mediaImport(t)
					res, err := imp.Body(BodyOptions{Site: bodySite(false), Settings: styleSettings(), Item: bodyItem(in), Target: bodyTarget, Key: bodyKey})
					if err != nil {
						t.Fatal(err)
					}
					checkBody(t, imp, bodySite(false), res)
					noGuard(t, imp)
					if t.Failed() {
						t.Fatalf("the body is %s", in)
					}
				}
			}
		}
	}
}
