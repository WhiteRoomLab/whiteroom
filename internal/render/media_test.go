package render

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

const (
	officeImage   = "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5" // office-image.json: 1600x1067, alt in en and ja, /media/会社外観.jpg
	enAltImage    = "med_01JB9A2C4E6G8J0M2P4R6T8W0Y" // 600x200, alt only in en
	noAltImage    = "med_01JB9A2C4E6G8J0M2P4R6T8W1Z" // without alt, and without a width and a height, so without files
	svgMedia      = "med_01JB9A2C4E6G8J0M2P4R6T8W2A" // of a type that media cannot have
	nilMedia      = "med_01JB9A2C4E6G8J0M2P4R6T8W3B" // found, but nil
	animatedImage = "med_01JB9A2C4E6G8J0M2P4R6T8W4C" // an 800x600 GIF, which testResolver takes for an animation

	officePath = "/media/%E4%BC%9A%E7%A4%BE%E5%A4%96%E8%A6%B3.jpg"
	guidePath  = "/media/%E4%BC%9A%E7%A4%BE%E6%A1%88%E5%86%85.pdf"

	// The files of officeImage: the published file, that of the default size and those of the sizes of mediaTheme.
	officeSrc  = officePath + "?v=afb0b01b"
	office640  = "/media/%E4%BC%9A%E7%A4%BE%E5%A4%96%E8%A6%B3-640x427.jpg?v=44854c51"
	office1024 = "/media/%E4%BC%9A%E7%A4%BE%E5%A4%96%E8%A6%B3-1024x683.jpg?v=37baaab8"
	office150  = "/media/%E4%BC%9A%E7%A4%BE%E5%A4%96%E8%A6%B3-150x150.jpg?v=27cb3948"
	office300  = "/media/%E4%BC%9A%E7%A4%BE%E5%A4%96%E8%A6%B3-300x300.jpg?v=32292e5b"

	// The attributes of the img of officeImage at the default size in a site without a base, but alt: src and srcset;
	// sizes, for an image shown at most 1600 pixels wide; width and height; and all of them.
	officeSrcset = `src="` + officeSrc + `" srcset="` + office640 + ` 640w, ` + officeSrc + ` 1600w"`
	officeSizes  = `sizes="(max-width: 1600px) 100vw, 1600px"`
	officeWH     = `width="1600" height="1067"`
	officeAttrs  = officeSrcset + " " + officeSizes + " " + officeWH
	// Those of officeImage at the size large of mediaTheme: src and srcset; and width and height.
	officeLargeSrcset = `src="` + office1024 + `" srcset="` + office1024 + ` 1024w, ` + officeSrc + ` 1600w"`
	officeLargeWH     = `width="1024" height="683"`

	animatedSrc = "/media/loading.gif?v=3cc3a587" // the published file of animatedImage
)

// mediaTheme is the settings file of a theme with the color primary, whose images have the variant rounded and whose
// files have button, and with the image sizes thumbnail, cropped to 150x150, and large, 1024 pixels wide.
const mediaTheme = "schemaVersion: 1\npresets:\n  color:\n    primary: \"#1e73be\"\n" +
	"blocks:\n  image:\n    variants:\n      rounded: 角丸\n  file:\n    variants:\n      button: ボタン\n" +
	"imageSizes:\n  thumbnail: {label: サムネイル, width: 150, height: 150, crop: true}\n  large: {label: 大, width: 1024}\n"

// mediaResolver returns a Resolver with the entries of linkResolver; the media of office-image.json and of
// company-guide-pdf.json (guideMedia) at /media/<file name>; enAltImage, noAltImage, svgMedia and animatedImage; and
// nilMedia.
func mediaResolver(t testing.TB) testResolver {
	t.Helper()
	var office, guide content.Media
	decodeTestdata(t, "office-image.json", office.UnmarshalJSON)
	decodeTestdata(t, "company-guide-pdf.json", guide.UnmarshalJSON)
	return testResolver{
		entries: linkResolver.entries,
		media: map[string]testMedium{
			officeImage: {&office, "/media/会社外観.jpg"},
			guideMedia:  {&guide, "/media/会社案内.pdf"},
			enAltImage: {
				&content.Media{
					ID: enAltImage, FileName: "sign.png", MIMEType: "image/png", Width: 600, Height: 200,
					Alt: content.Localized{"en": "Sign"},
				},
				"/media/sign.png",
			},
			noAltImage: {&content.Media{ID: noAltImage, FileName: "plain.webp", MIMEType: "image/webp"}, "/media/plain.webp"},
			svgMedia: {
				&content.Media{ID: svgMedia, FileName: "logo.svg", MIMEType: "image/svg+xml", Alt: content.Localized{"ja": "ロゴ"}},
				"/media/logo.svg",
			},
			nilMedia: {nil, "/media/nil.png"},
			animatedImage: {
				&content.Media{
					ID: animatedImage, FileName: "loading.gif", MIMEType: "image/gif", Width: 800, Height: 600,
					Alt: content.Localized{"ja": "読み込み中"},
				},
				"/media/loading.gif",
			},
		},
	}
}

// imageJSON returns the JSON of a list of an image with the ID b_000image and the attrs, a JSON object.
func imageJSON(attrs string) string {
	return `[{"type": "image", "id": "b_000image", "attrs": ` + attrs + `}]`
}

func TestImage(t *testing.T) {
	const (
		noAlt   = "no alt in ja: neither the block nor its medium has one; rendered without the alt attribute"
		noFiles = `media "` + noAltImage + `" has no image files; rendered without srcset, width and height`
	)
	tests := []blockTest{
		{
			name: "alt, caption, newTab link and the other attributes",
			json: imageJSON(`{
				"media": "` + officeImage + `", "alt": "本社の外観",
				"caption": [{"type": "text", "text": "本社"}, {"type": "text", "text": "（2024年）", "marks": [{"type": "italic"}]}],
				"link": {"href": "https://example.com/office", "newTab": true},
				"size": "large", "width": "300px", "aspectRatio": "16/9", "align": "wide"
			}`),
			want: `<figure class="b-000image alignwide"><a href="https://example.com/office" target="_blank" rel="noopener">` +
				`<img ` + officeLargeSrcset + ` sizes="(max-width: 300px) 100vw, 300px" ` + officeLargeWH +
				` alt="本社の外観"></a><figcaption>本社<em>（2024年）</em></figcaption></figure>`,
			css: ".b-000image img {\n  width: 300px !important;\n  aspect-ratio: 16/9 !important;\n" +
				"  object-fit: cover !important;\n  height: auto !important;\n}\n",
		},
		{
			name: "decorative",
			json: imageJSON(`{"media": "` + officeImage + `", "decorative": true}`),
			want: `<figure><img ` + officeAttrs + ` alt=""></figure>`,
		},
		{
			name: "the alt of the medium",
			json: imageJSON(`{"media": "` + officeImage + `"}`),
			want: `<figure><img ` + officeAttrs + ` alt="会社外観"></figure>`,
		},
		{
			name:     "alt only in another language, and no srcset for an image of one file",
			json:     imageJSON(`{"media": "` + enAltImage + `"}`),
			want:     `<figure><img src="/media/sign.png?v=1fb1470a" width="600" height="200"></figure>`,
			warnings: []string{"/0/attrs/alt: " + noAlt},
		},
		{
			name:     "a medium without alt and without files",
			json:     imageJSON(`{"media": "` + noAltImage + `", "caption": [{"type": "text", "text": "無題"}]}`),
			want:     `<figure><img src="/media/plain.webp"><figcaption>無題</figcaption></figure>`,
			warnings: []string{"/0/attrs/media: " + noFiles, "/0/attrs/alt: " + noAlt},
		},
		{
			name: "an image size of the theme",
			json: imageJSON(`{"media": "` + officeImage + `", "alt": "a", "size": "large"}`),
			want: `<figure><img ` + officeLargeSrcset + ` sizes="(max-width: 1024px) 100vw, 1024px" ` + officeLargeWH +
				` alt="a"></figure>`,
		},
		{
			name: "an image size that crops",
			json: imageJSON(`{"media": "` + officeImage + `", "alt": "a", "size": "thumbnail"}`),
			want: `<figure><img src="` + office150 + `" srcset="` + office150 + ` 150w, ` + office300 + ` 300w" ` +
				`sizes="(max-width: 150px) 100vw, 150px" width="150" height="150" alt="a"></figure>`,
		},
		{
			name:     "an image size that the theme does not have",
			json:     imageJSON(`{"media": "` + officeImage + `", "alt": "a", "size": "huge"}`),
			want:     `<figure><img ` + officeAttrs + ` alt="a"></figure>`,
			warnings: []string{`/0/attrs/size: the theme has no image size "huge"; rendered at the default size`},
		},
		{
			name: "sizes by the width of the block in px, but not in %",
			json: `[
				` + decorativeImage("b_00image1", `"width": "240.5px"`) + `,
				` + decorativeImage("b_00image2", `"width": "2000px"`) + `,
				` + decorativeImage("b_00image3", `"width": "50%"`) + `,
				` + decorativeImage("b_00image4", `"size": "large", "width": "20rem"`) + `
			]`,
			want: `<figure class="b-00image1"><img ` + officeSrcset + ` sizes="(max-width: 241px) 100vw, 241px" ` +
				officeWH + ` alt=""></figure>` + "\n" +
				`<figure class="b-00image2"><img ` + officeSrcset + ` sizes="(max-width: 2000px) 100vw, 2000px" ` +
				officeWH + ` alt=""></figure>` + "\n" +
				`<figure class="b-00image3"><img ` + officeAttrs + ` alt=""></figure>` + "\n" +
				`<figure class="b-00image4"><img ` + officeLargeSrcset + ` sizes="(max-width: 1024px) 100vw, 1024px" ` +
				officeLargeWH + ` alt=""></figure>`,
			css: ".b-00image1 img {\n  width: 240.5px !important;\n  height: auto !important;\n}\n\n" +
				".b-00image2 img {\n  width: 2000px !important;\n  height: auto !important;\n}\n\n" +
				".b-00image3 img {\n  width: 50% !important;\n  height: auto !important;\n}\n\n" +
				".b-00image4 img {\n  width: 20rem !important;\n  height: auto !important;\n}\n",
		},
		{
			name: "an animation, at the default size and at image sizes",
			json: `[
				{"type": "image", "id": "b_00image1", "attrs": {"media": "` + animatedImage + `"}},
				{"type": "image", "id": "b_00image2", "attrs": {"media": "` + animatedImage + `", "size": "thumbnail", "width": "100px"}},
				{"type": "image", "id": "b_00image3", "attrs": {"media": "` + animatedImage + `", "size": "large"}}
			]`,
			want: `<figure><img src="` + animatedSrc + `" width="800" height="600" alt="読み込み中"></figure>` + "\n" +
				`<figure class="b-00image2"><img src="` + animatedSrc + `" width="150" height="113" alt="読み込み中">` +
				"</figure>\n" +
				`<figure><img src="` + animatedSrc + `" width="800" height="600" alt="読み込み中"></figure>`,
			css: ".b-00image2 img {\n  width: 100px !important;\n  height: auto !important;\n}\n",
		},
		{
			name: "missing medium",
			json: `[
				` + paragraphJSON("b_0paragr1", "a") + `,
				{"type": "image", "id": "b_000image", "variant": "x", "attrs": {"media": "` + missingMedia + `", "alt": "x"}},
				` + paragraphJSON("b_0paragr2", "b") + `
			]`,
			want:     "<p>a</p>\n<p>b</p>",
			warnings: []string{`/1/attrs/media: media "` + missingMedia + `" does not exist; not rendered`},
		},
		{
			name: "a PDF",
			json: `[
				` + paragraphJSON("b_0paragr1", "a") + `,
				{
					"type": "group", "id": "b_000group",
					"content": [{"type": "image", "id": "b_000image", "variant": "x", "attrs": {"media": "` + guideMedia + `", "alt": "x"}}]
				},
				` + paragraphJSON("b_0paragr2", "b") + `
			]`,
			want:     "<p>a</p>\n<div></div>\n<p>b</p>",
			warnings: []string{`/1/content/0/attrs/media: media "` + guideMedia + `" is not an image; not rendered`},
		},
		{
			name:     "a medium of a type that media cannot have",
			json:     imageJSON(`{"media": "` + svgMedia + `"}`),
			warnings: []string{`/0/attrs/media: media "` + svgMedia + `" is not an image; not rendered`},
		},
		{
			name:     "a nil medium",
			json:     imageJSON(`{"media": "` + nilMedia + `", "alt": "x"}`),
			warnings: []string{`/0/attrs/media: media "` + nilMedia + `" does not exist; not rendered`},
		},
		{
			name:     "no medium",
			json:     imageJSON(`{"alt": "x"}`),
			warnings: []string{`/0/attrs/media: media "" does not exist; not rendered`},
		},
		{
			name: "links to an entry and to a medium",
			json: `[
				{"type": "image", "id": "b_000image", "attrs": {"media": "` + officeImage + `", "alt": "a", "link": {"entry": "` +
				aboutEntry + `", "anchor": "access"}}},
				{"type": "image", "id": "b_00image2", "attrs": {"media": "` + officeImage + `", "alt": "b", "link": {"media": "` +
				guideMedia + `", "rel": ["nofollow"]}}}
			]`,
			want: `<figure><a href="/about/#access"><img ` + officeAttrs + ` alt="a"></a></figure>` + "\n" +
				`<figure><a href="` + guidePath + `" rel="nofollow"><img ` + officeAttrs + ` alt="b"></a></figure>`,
		},
		{
			name:     "unpublished link",
			json:     imageJSON(`{"media": "` + officeImage + `", "alt": "a", "link": {"entry": "` + draftEntry + `"}}`),
			want:     `<figure><img ` + officeAttrs + ` alt="a"></figure>`,
			warnings: []string{`/0/attrs/link: entry "` + draftEntry + `" is not published; rendered without the link`},
		},
		{
			name: "classes and anchor",
			json: `[{
				"type": "image", "id": "b_000image", "anchor": "photo", "variant": "rounded", "className": "shadow",
				"style": {"border": {"radius": {"value": "8px"}}}, "attrs": {"media": "` + officeImage + `", "decorative": true}
			}]`,
			want: `<figure id="photo" class="b-000image is-variant-rounded shadow"><img ` + officeAttrs +
				` alt=""></figure>`,
			css: ".b-000image {\n  border-radius: 8px !important;\n}\n",
		},
		{
			name: "escaping",
			json: imageJSON(`{
				"media": "` + officeImage + `", "alt": "\"<&'>",
				"caption": [{"type": "text", "text": "<b>\"&'</b>"}]
			}`),
			want: `<figure><img ` + officeAttrs + ` alt="&#34;&lt;&amp;&#39;&gt;">` +
				`<figcaption>&lt;b&gt;"&amp;'&lt;/b&gt;</figcaption></figure>`,
		},
		{
			name: "empty caption",
			json: imageJSON(`{"media": "` + officeImage + `", "alt": "a", "caption": []}`),
			want: `<figure><img ` + officeAttrs + ` alt="a"></figure>`,
		},
		{
			name: "warnings in document order",
			json: `[{
				"type": "image", "id": "b_000image", "variant": "big",
				"attrs": {
					"media": "` + noAltImage + `",
					"caption": [{"type": "text", "text": "c", "marks": [{"type": "link", "attrs": {"entry": "` + draftEntry + `"}}]}],
					"link": {"entry": "` + missingEntry + `"}, "size": "huge", "width": "1"
				}
			}]`,
			want: `<figure><img src="/media/plain.webp"><figcaption>c</figcaption></figure>`,
			warnings: []string{
				`/0/variant: the theme has no image variant "big"; rendered without its class`,
				"/0/attrs/media: " + noFiles,
				"/0/attrs/alt: " + noAlt,
				`/0/attrs/caption/0/marks/0: entry "` + draftEntry + `" is not published; rendered without the link`,
				`/0/attrs/link: entry "` + missingEntry + `" does not exist; rendered without the link`,
				`/0/attrs/size: the theme has no image size "huge"; rendered at the default size`,
				`/0/attrs/width: invalid length "1"; ignored`,
			},
		},
	}
	checkBlocks(t, tests, mediaTheme, "ja", "", mediaResolver(t))
	checkBlocks(t, []blockTest{
		{
			name: "the base and the alt of the medium in English",
			json: imageJSON(`{"media": "` + officeImage + `", "link": {"entry": "` + aboutEntry + `"}}`),
			want: `<figure><a href="/blog/about/"><img ` + strings.ReplaceAll(officeAttrs, "/media/", "/blog/media/") +
				` alt="Our office building"></a></figure>`,
		},
		{
			name: "the alt of the medium only in English",
			json: imageJSON(`{"media": "` + enAltImage + `"}`),
			want: `<figure><img src="/blog/media/sign.png?v=1fb1470a" width="600" height="200" alt="Sign"></figure>`,
		},
	}, mediaTheme, "en", "/blog", mediaResolver(t))
	// The commas of a base, which would separate the files of the srcset.
	checkBlocks(t, []blockTest{{
		name: "a base with a comma",
		json: imageJSON(`{"media": "` + officeImage + `", "alt": "a"}`),
		want: `<figure><img src="/a,b` + officeSrc + `" srcset="/a%2Cb` + office640 + ` 640w, /a%2Cb` + officeSrc +
			` 1600w" ` + officeSizes + " " + officeWH + ` alt="a"></figure>`,
	}}, mediaTheme, "ja", "/a,b", mediaResolver(t))
}

func TestImgSizes(t *testing.T) {
	tests := []struct {
		n     int
		width string
		want  int // the width in sizes
	}{
		{1600, "", 1600},
		{1600, "300px", 300},
		{1600, "240.5px", 241},
		{1600, ".1px", 1},
		{1600, "0px", 1},
		{1600, "1599.9px", 1600},
		{1600, "1600px", 1600},
		{1600, "2000px", 2000},
		{1600, "20000px", 20000},
		{1600, "20000.5px", 20000},
		{1600, "99999999999999999999999999px", 20000},
		{1600, "50%", 1600},
		{1600, "20rem", 1600},
		{1600, "100vw", 1600},
		{1600, "0", 1600},
		{150, "100px", 100},
		{150, "300px", 300},
	}
	for _, tt := range tests {
		want := fmt.Sprintf("(max-width: %dpx) 100vw, %dpx", tt.want, tt.want)
		if got := imgSizes(tt.n, tt.width); got != want {
			t.Errorf("imgSizes(%d, %q) = %q, want %q", tt.n, tt.width, got, want)
		}
	}
}

// TestPageImage checks the images outside blocks: at the default size, with the alt of the medium in the language of
// the page, and none for media that are not images.
func TestPageImage(t *testing.T) {
	const signSrc = "/media/sign.png?v=1fb1470a" // the published file of enAltImage
	tests := []struct {
		name, language, base, id string
		want                     *PageImage
	}{
		{
			name: "srcset and the alt in ja", language: "ja", id: officeImage,
			want: &PageImage{
				Src: officeSrc, Srcset: office640 + " 640w, " + officeSrc + " 1600w", Sizes: "(max-width: 1600px) 100vw, 1600px",
				Width: 1600, Height: 1067, Alt: "会社外観", HasAlt: true,
			},
		},
		{
			name: "the base and the alt in en", language: "en", base: "/blog", id: officeImage,
			want: &PageImage{
				Src: "/blog" + officeSrc, Srcset: "/blog" + office640 + " 640w, /blog" + officeSrc + " 1600w",
				Sizes: "(max-width: 1600px) 100vw, 1600px", Width: 1600, Height: 1067, Alt: "Our office building", HasAlt: true,
			},
		},
		{
			name: "a base with a comma", language: "ja", base: "/a,b", id: officeImage,
			want: &PageImage{
				Src: "/a,b" + officeSrc, Srcset: "/a%2Cb" + office640 + " 640w, /a%2Cb" + officeSrc + " 1600w",
				Sizes: "(max-width: 1600px) 100vw, 1600px", Width: 1600, Height: 1067, Alt: "会社外観", HasAlt: true,
			},
		},
		{
			name: "one file, and no alt in the language", language: "ja", id: enAltImage,
			want: &PageImage{Src: signSrc, Width: 600, Height: 200},
		},
		{
			name: "the alt only in en", language: "en", id: enAltImage,
			want: &PageImage{Src: signSrc, Width: 600, Height: 200, Alt: "Sign", HasAlt: true},
		},
		{
			name: "an animation", language: "ja", id: animatedImage,
			want: &PageImage{Src: animatedSrc, Width: 800, Height: 600, Alt: "読み込み中", HasAlt: true},
		},
		{name: "no files", language: "ja", id: noAltImage, want: &PageImage{Src: "/media/plain.webp"}},
		{name: "a PDF", language: "ja", id: guideMedia},
		{name: "a medium of a type that media cannot have", language: "ja", id: svgMedia},
		{name: "a nil medium", language: "ja", id: nilMedia},
		{name: "a missing medium", language: "ja", id: missingMedia},
		{name: "no medium", language: "ja"},
	}
	th := testTheme(t, map[string]string{"theme.yaml": mediaTheme})
	for _, tt := range tests {
		r := newRenderer(t, th, tt.language, tt.base, mediaResolver(t))
		if got := r.PageImage(tt.id); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: PageImage(%q) = %+v, want %+v", tt.name, tt.id, got, tt.want)
		}
	}
	if got := newRenderer(t, th, "ja", "", nil).PageImage(officeImage); got != nil {
		t.Errorf("PageImage() without a Resolver = %+v, want nil", got)
	}
}

// fileJSON returns the JSON of a list of a file with the ID b_0000file and the attrs, a JSON object.
func fileJSON(attrs string) string {
	return `[{"type": "file", "id": "b_0000file", "attrs": ` + attrs + `}]`
}

func TestFile(t *testing.T) {
	tests := []blockTest{
		{
			name: "label",
			json: fileJSON(`{"media": "` + guideMedia + `", "label": "会社案内（PDF）"}`),
			want: `<div><a href="` + guidePath + `">会社案内（PDF）</a></div>`,
		},
		{
			name: "the file name of the medium",
			json: fileJSON(`{"media": "` + guideMedia + `"}`),
			want: `<div><a href="` + guidePath + `">会社案内.pdf</a></div>`,
		},
		{
			name: "an image",
			json: fileJSON(`{"media": "` + officeImage + `"}`),
			want: `<div><a href="` + officePath + `">会社外観.jpg</a></div>`,
		},
		{
			name: "a medium of a type that media cannot have",
			json: fileJSON(`{"media": "` + svgMedia + `", "label": "ロゴ"}`),
			want: `<div><a href="/media/logo.svg">ロゴ</a></div>`,
		},
		{
			name: "missing medium",
			json: `[
				` + paragraphJSON("b_0paragr1", "a") + `,
				{"type": "file", "id": "b_0000file", "variant": "x", "attrs": {"media": "` + missingMedia + `", "label": "x"}},
				` + paragraphJSON("b_0paragr2", "b") + `
			]`,
			want:     "<p>a</p>\n<p>b</p>",
			warnings: []string{`/1/attrs/media: media "` + missingMedia + `" does not exist; not rendered`},
		},
		{
			name:     "a nil medium",
			json:     fileJSON(`{"media": "` + nilMedia + `"}`),
			warnings: []string{`/0/attrs/media: media "` + nilMedia + `" does not exist; not rendered`},
		},
		{
			name: "classes and anchor",
			json: `[{
				"type": "file", "id": "b_0000file", "anchor": "guide", "variant": "button", "className": "download",
				"style": {"textColor": {"preset": "primary"}}, "attrs": {"media": "` + guideMedia + `"}
			}]`,
			want: `<div id="guide" class="b-0000file is-variant-button download"><a href="` + guidePath + `">会社案内.pdf</a></div>`,
			css:  ".b-0000file {\n  color: var(--color-primary) !important;\n}\n",
		},
		{
			name: "escaping",
			json: fileJSON(`{"media": "` + guideMedia + `", "label": "<b>\"&'</b>"}`),
			want: `<div><a href="` + guidePath + `">&lt;b&gt;&#34;&amp;&#39;&lt;/b&gt;</a></div>`,
		},
		{
			name:     "unknown variant",
			json:     `[{"type": "file", "id": "b_0000file", "variant": "big", "attrs": {"media": "` + guideMedia + `"}}]`,
			want:     `<div><a href="` + guidePath + `">会社案内.pdf</a></div>`,
			warnings: []string{`/0/variant: the theme has no file variant "big"; rendered without its class`},
		},
	}
	checkBlocks(t, tests, mediaTheme, "ja", "", mediaResolver(t))
	checkBlocks(t, []blockTest{{
		name: "the base",
		json: fileJSON(`{"media": "` + guideMedia + `"}`),
		want: `<div><a href="/blog` + guidePath + `">会社案内.pdf</a></div>`,
	}}, mediaTheme, "en", "/blog", mediaResolver(t))
}

// TestMediaData checks the data of images and files, also the fields that the default templates do not write.
func TestMediaData(t *testing.T) {
	r := newRenderer(t, testTheme(t, map[string]string{"theme.yaml": mediaTheme}), "en", "/blog", mediaResolver(t))
	image := decodeBlocks(t, `[{
		"type": "image", "id": "b_000image", "anchor": "photo", "variant": "rounded",
		"attrs": {
			"media": "`+officeImage+`", "caption": [{"type": "text", "text": "Head office"}],
			"link": {"href": "https://example.com/office", "newTab": true},
			"size": "large", "width": "300px", "aspectRatio": "16/9", "align": "wide"
		}
	}]`)[0]
	w := &walker{r: r}
	got, ok := w.image(&image, "/0")
	want := Image{
		Common:      Common{ID: "b_000image", Anchor: "photo", Class: "b-000image alignwide is-variant-rounded"},
		Src:         "/blog" + office1024,
		Srcset:      "/blog" + office1024 + " 1024w, /blog" + officeSrc + " 1600w",
		Sizes:       "(max-width: 300px) 100vw, 300px",
		SrcWidth:    1024,
		SrcHeight:   683,
		Alt:         "Our office building",
		HasAlt:      true,
		Caption:     "Head office",
		Link:        &Link{Href: "https://example.com/office", Target: "_blank", Rel: "noopener"},
		Size:        "large",
		Width:       "300px",
		AspectRatio: "16/9",
		Align:       "wide",
	}
	if !ok || !reflect.DeepEqual(got, want) || w.warnings != nil {
		t.Errorf("image() = %+v, %v, warnings %q, want %+v", got, ok, w.warnings, want)
	}
	const css = ".b-000image img {\n  width: 300px !important;\n  aspect-ratio: 16/9 !important;\n" +
		"  object-fit: cover !important;\n  height: auto !important;\n}\n"
	if w.css.String() != css {
		t.Errorf("image() CSS:\n%s\nwant:\n%s", w.css, css)
	}
	noAlt := decodeBlocks(t, `[{"type": "image", "id": "b_00image2", "attrs": {"media": "`+noAltImage+`"}}]`)[0]
	w = &walker{r: r, list: 1}
	got, ok = w.image(&noAlt, "/3")
	want = Image{Common: Common{ID: "b_00image2"}, Src: "/blog/media/plain.webp"}
	wantWarnings := []Warning{
		{
			List: 1, Path: "/3/attrs/media",
			Msg: `media "` + noAltImage + `" has no image files; rendered without srcset, width and height`,
		},
		{
			List: 1, Path: "/3/attrs/alt",
			Msg: "no alt in en: neither the block nor its medium has one; rendered without the alt attribute",
		},
	}
	if !ok || !reflect.DeepEqual(got, want) || !slices.Equal(w.warnings, wantWarnings) {
		t.Errorf("image() = %+v, %v, warnings %q, want %+v and %q", got, ok, w.warnings, want, wantWarnings)
	}
	// An animation, without a srcset, at a size that the theme has and at one that it does not have.
	animated := decodeBlocks(t, `[
		{"type": "image", "id": "b_00image3", "attrs": {"media": "`+animatedImage+`", "alt": "a", "size": "thumbnail"}},
		{"type": "image", "id": "b_00image4", "attrs": {"media": "`+animatedImage+`", "alt": "b", "size": "huge"}}
	]`)
	w = &walker{r: r}
	got, ok = w.image(&animated[0], "/0")
	want = Image{
		Common: Common{ID: "b_00image3"}, Src: "/blog" + animatedSrc, SrcWidth: 150, SrcHeight: 113,
		Alt: "a", HasAlt: true, Size: "thumbnail",
	}
	if !ok || !reflect.DeepEqual(got, want) || w.warnings != nil {
		t.Errorf("image() = %+v, %v, warnings %q, want %+v", got, ok, w.warnings, want)
	}
	got, ok = w.image(&animated[1], "/1")
	want = Image{
		Common: Common{ID: "b_00image4"}, Src: "/blog" + animatedSrc, SrcWidth: 800, SrcHeight: 600,
		Alt: "b", HasAlt: true, Size: "huge",
	}
	wantWarnings = []Warning{
		{Path: "/1/attrs/size", Msg: `the theme has no image size "huge"; rendered at the default size`},
	}
	if !ok || !reflect.DeepEqual(got, want) || !slices.Equal(w.warnings, wantWarnings) {
		t.Errorf("image() = %+v, %v, warnings %q, want %+v and %q", got, ok, w.warnings, want, wantWarnings)
	}
	file := decodeBlocks(t, `[{"type": "file", "id": "b_0000file", "attrs": {"media": "`+guideMedia+`"}}]`)[0]
	w = &walker{r: r}
	gotFile, ok := w.file(&file, "/0")
	wantFile := File{
		Common: Common{ID: "b_0000file"}, Href: "/blog" + guidePath, Label: "会社案内.pdf", FileName: "会社案内.pdf",
		MIMEType: "application/pdf", Size: 2345678,
	}
	if !ok || gotFile != wantFile || w.warnings != nil {
		t.Errorf("file() = %+v, %v, warnings %q, want %+v", gotFile, ok, w.warnings, wantFile)
	}
	// Not rendered: the zero data.
	missing := decodeBlocks(t, `[{"type": "file", "id": "b_0000file", "attrs": {"media": "`+missingMedia+`"}}]`)[0]
	w = &walker{r: r}
	if gotFile, ok := w.file(&missing, "/0"); ok || gotFile != (File{}) || len(w.warnings) != 1 {
		t.Errorf("file() of a missing medium = %+v, %v, warnings %q, want nothing and a warning", gotFile, ok, w.warnings)
	}
	w = &walker{r: r}
	if got, ok := w.image(&missing, "/0"); ok || !reflect.DeepEqual(got, Image{}) || len(w.warnings) != 1 {
		t.Errorf("image() of a missing medium = %+v, %v, warnings %q, want nothing and a warning", got, ok, w.warnings)
	}
}
