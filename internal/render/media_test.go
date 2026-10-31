package render

import (
	"reflect"
	"slices"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

const (
	officeImage = "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5" // office-image.json: alt in en and ja, at /media/会社外観.jpg
	enAltImage  = "med_01JB9A2C4E6G8J0M2P4R6T8W0Y" // alt only in en
	noAltImage  = "med_01JB9A2C4E6G8J0M2P4R6T8W1Z" // without alt
	svgMedia    = "med_01JB9A2C4E6G8J0M2P4R6T8W2A" // of a type that media cannot have
	nilMedia    = "med_01JB9A2C4E6G8J0M2P4R6T8W3B" // found, but nil

	officePath = "/media/%E4%BC%9A%E7%A4%BE%E5%A4%96%E8%A6%B3.jpg"
	guidePath  = "/media/%E4%BC%9A%E7%A4%BE%E6%A1%88%E5%86%85.pdf"
)

// mediaTheme is the settings file of a theme with the color primary, whose images have the variant rounded and whose
// files have button.
const mediaTheme = "schemaVersion: 1\npresets:\n  color:\n    primary: \"#1e73be\"\n" +
	"blocks:\n  image:\n    variants:\n      rounded: 角丸\n  file:\n    variants:\n      button: ボタン\n"

// mediaResolver returns a Resolver with the entries of linkResolver; the media of office-image.json and of
// company-guide-pdf.json (guideMedia) at /media/<file name>; enAltImage, noAltImage and svgMedia; and nilMedia.
func mediaResolver(t *testing.T) testResolver {
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
				&content.Media{ID: enAltImage, FileName: "sign.png", MIMEType: "image/png", Alt: content.Localized{"en": "Sign"}},
				"/media/sign.png",
			},
			noAltImage: {&content.Media{ID: noAltImage, FileName: "plain.webp", MIMEType: "image/webp"}, "/media/plain.webp"},
			svgMedia: {
				&content.Media{ID: svgMedia, FileName: "logo.svg", MIMEType: "image/svg+xml", Alt: content.Localized{"ja": "ロゴ"}},
				"/media/logo.svg",
			},
			nilMedia: {nil, "/media/nil.png"},
		},
	}
}

// imageJSON returns the JSON of a list of an image with the ID b_000image and the attrs, a JSON object.
func imageJSON(attrs string) string {
	return `[{"type": "image", "id": "b_000image", "attrs": ` + attrs + `}]`
}

func TestImage(t *testing.T) {
	const noAlt = "no alt in ja: neither the block nor its medium has one; rendered without the alt attribute"
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
				`<img src="` + officePath + `" alt="本社の外観"></a><figcaption>本社<em>（2024年）</em></figcaption></figure>`,
			css: ".b-000image img {\n  width: 300px !important;\n  aspect-ratio: 16/9 !important;\n" +
				"  object-fit: cover !important;\n  height: auto !important;\n}\n",
		},
		{
			name: "decorative",
			json: imageJSON(`{"media": "` + officeImage + `", "decorative": true}`),
			want: `<figure><img src="` + officePath + `" alt=""></figure>`,
		},
		{
			name: "the alt of the medium",
			json: imageJSON(`{"media": "` + officeImage + `"}`),
			want: `<figure><img src="` + officePath + `" alt="会社外観"></figure>`,
		},
		{
			name:     "alt only in another language",
			json:     imageJSON(`{"media": "` + enAltImage + `"}`),
			want:     `<figure><img src="/media/sign.png"></figure>`,
			warnings: []string{"/0/attrs/alt: " + noAlt},
		},
		{
			name:     "a medium without alt",
			json:     imageJSON(`{"media": "` + noAltImage + `", "caption": [{"type": "text", "text": "無題"}]}`),
			want:     `<figure><img src="/media/plain.webp"><figcaption>無題</figcaption></figure>`,
			warnings: []string{"/0/attrs/alt: " + noAlt},
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
			want: `<figure><a href="/about/#access"><img src="` + officePath + `" alt="a"></a></figure>` + "\n" +
				`<figure><a href="` + guidePath + `" rel="nofollow"><img src="` + officePath + `" alt="b"></a></figure>`,
		},
		{
			name:     "unpublished link",
			json:     imageJSON(`{"media": "` + officeImage + `", "alt": "a", "link": {"entry": "` + draftEntry + `"}}`),
			want:     `<figure><img src="` + officePath + `" alt="a"></figure>`,
			warnings: []string{`/0/attrs/link: entry "` + draftEntry + `" is not published; rendered without the link`},
		},
		{
			name: "classes and anchor",
			json: `[{
				"type": "image", "id": "b_000image", "anchor": "photo", "variant": "rounded", "className": "shadow",
				"style": {"border": {"radius": {"value": "8px"}}}, "attrs": {"media": "` + officeImage + `", "decorative": true}
			}]`,
			want: `<figure id="photo" class="b-000image is-variant-rounded shadow"><img src="` + officePath + `" alt=""></figure>`,
			css:  ".b-000image {\n  border-radius: 8px !important;\n}\n",
		},
		{
			name: "escaping",
			json: imageJSON(`{
				"media": "` + officeImage + `", "alt": "\"<&'>",
				"caption": [{"type": "text", "text": "<b>\"&'</b>"}]
			}`),
			want: `<figure><img src="` + officePath + `" alt="&#34;&lt;&amp;&#39;&gt;"><figcaption>&lt;b&gt;"&amp;'&lt;/b&gt;</figcaption>` +
				"</figure>",
		},
		{
			name: "empty caption",
			json: imageJSON(`{"media": "` + officeImage + `", "alt": "a", "caption": []}`),
			want: `<figure><img src="` + officePath + `" alt="a"></figure>`,
		},
		{
			name: "warnings in document order",
			json: `[{
				"type": "image", "id": "b_000image", "variant": "big",
				"attrs": {
					"media": "` + noAltImage + `",
					"caption": [{"type": "text", "text": "c", "marks": [{"type": "link", "attrs": {"entry": "` + draftEntry + `"}}]}],
					"link": {"entry": "` + missingEntry + `"}
				}
			}]`,
			want: `<figure><img src="/media/plain.webp"><figcaption>c</figcaption></figure>`,
			warnings: []string{
				`/0/variant: the theme has no image variant "big"; rendered without its class`,
				"/0/attrs/alt: " + noAlt,
				`/0/attrs/caption/0/marks/0: entry "` + draftEntry + `" is not published; rendered without the link`,
				`/0/attrs/link: entry "` + missingEntry + `" does not exist; rendered without the link`,
			},
		},
	}
	checkBlocks(t, tests, mediaTheme, "ja", "", mediaResolver(t))
	checkBlocks(t, []blockTest{
		{
			name: "the base and the alt of the medium in English",
			json: imageJSON(`{"media": "` + officeImage + `", "link": {"entry": "` + aboutEntry + `"}}`),
			want: `<figure><a href="/blog/about/"><img src="/blog` + officePath + `" alt="Our office building"></a></figure>`,
		},
		{
			name: "the alt of the medium only in English",
			json: imageJSON(`{"media": "` + enAltImage + `"}`),
			want: `<figure><img src="/blog/media/sign.png" alt="Sign"></figure>`,
		},
	}, mediaTheme, "en", "/blog", mediaResolver(t))
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
		Src:         "/blog" + officePath,
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
	wantWarnings := []Warning{{
		List: 1, Path: "/3/attrs/alt",
		Msg: "no alt in en: neither the block nor its medium has one; rendered without the alt attribute",
	}}
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
