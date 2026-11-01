package render

import (
	"cmp"
	"html/template"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// image returns the data of b, an image at path; ok is false if its medium does not exist or is not an image, which it
// warns about before anything else. It shows the files of the image size of b, or those of the default size if the
// theme has no such size, which it warns about.
func (w *walker) image(b *content.Block, path string) (Image, bool) {
	a := &b.Attrs
	m, src, ok := w.medium(b, path)
	if !ok {
		return Image{}, false
	}
	if m.Kind() != content.MediaImage {
		w.warnf(path+"/attrs/media", "media %q is not an image; not rendered", a.Media)
		return Image{}, false
	}
	s := w.begin(b, path)
	d := Image{Src: src, Size: a.Size, Width: a.Width, AspectRatio: a.AspectRatio, Align: a.Align}
	size, known := w.r.imageSize(a.Size)
	files, found := w.r.res.Image(a.Media, size)
	if !found {
		w.warnf(path+"/attrs/media", "media %q has no image files; rendered without srcset, width and height", a.Media)
	}
	if d.Alt, d.HasAlt = b.ImageAlt(m, w.r.language); !d.HasAlt {
		w.warnf(path+"/attrs/alt",
			"no alt in %s: neither the block nor its medium has one; rendered without the alt attribute", w.r.language)
	}
	d.Caption = template.HTML(w.richText(a.Caption, path+"/attrs/caption"))
	d.Link = w.link(a.Link, path+"/attrs/link")
	if !known {
		w.warnf(path+"/attrs/size", "the theme has no image size %q; rendered at the default size", a.Size)
	}
	width := w.length(path+"/attrs/width", a.Width, content.LengthRule{Percent: true})
	ratio := a.AspectRatio
	if ratio != "" {
		if err := content.CheckAspectRatio(ratio); err != nil {
			w.warnf(path+"/attrs/aspectRatio", "%v; ignored", err)
			ratio = ""
		}
	}
	if width != "" {
		s.img = append(s.img, important("width", width))
	}
	if ratio != "" {
		s.img = append(s.img, important("aspect-ratio", ratio), important("object-fit", "cover"))
	}
	if width != "" || ratio != "" {
		s.img = append(s.img, important("height", "auto"))
	}
	if found {
		d.Src = template.URL(w.r.sitePath(files.Src))
		d.SrcWidth, d.SrcHeight = files.Width, files.Height
		if len(files.Srcset) > 1 {
			d.Srcset, d.Sizes = w.r.srcset(files.Srcset), imgSizes(files.Width, width)
		}
	}
	s.addClass(w.choice(path+"/attrs", "align", a.Align, imageAlignClasses))
	d.Common = w.finish(s)
	return d, true
}

// imageSize returns the image size of the theme with the name, or nil for the default size if name is ""; known is
// false if the theme has no such size.
func (r *Renderer) imageSize(name string) (size *content.ImageSize, known bool) {
	if name == "" {
		return nil, true
	}
	if r.settings == nil {
		return nil, false
	}
	sizes := r.settings.ImageSizes
	if i := slices.IndexFunc(sizes, func(x content.ImageSize) bool { return x.Name == name }); i >= 0 {
		return &sizes[i], true
	}
	return nil, false
}

// srcset returns the srcset of files: the URL and the width of each, such as "/media/a-640x427.jpg?v=1a2b3c4d 640w",
// separated by ", ". The commas of the URLs are encoded, since they would separate the files.
func (r *Renderer) srcset(files []ImageFile) template.Srcset {
	var b strings.Builder
	for i, f := range files {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strings.ReplaceAll(r.sitePath(f.Path), ",", "%2C"))
		b.WriteString(" " + strconv.Itoa(f.Width) + "w")
	}
	return template.Srcset(b.String())
}

// maxSizesWidth is wider than any file of an image, so a wider block gets its widest file anyway.
const maxSizesWidth = 2 * content.MaxImageSide

// imgSizes returns the sizes of an image shown n pixels wide, or as wide as width, the width of its block, if that is a
// length in px (rounded up, from 1 to maxSizesWidth): as wide as the screen on narrower screens, else that wide.
func imgSizes(n int, width string) string {
	if v, ok := strings.CutSuffix(width, "px"); ok {
		if px, err := strconv.ParseFloat(v, 64); err == nil {
			n = int(math.Ceil(min(max(px, 1), maxSizesWidth)))
		}
	}
	px := strconv.Itoa(n) + "px"
	return "(max-width: " + px + ") 100vw, " + px
}

// file returns the data of b, a file at path; ok is false if its medium does not exist, which it warns about before
// anything else.
func (w *walker) file(b *content.Block, path string) (File, bool) {
	m, href, ok := w.medium(b, path)
	if !ok {
		return File{}, false
	}
	return File{
		Common:   w.common(b, path),
		Href:     href,
		Label:    cmp.Or(b.Attrs.Label, m.FileName),
		FileName: m.FileName,
		MIMEType: m.MIMEType,
		Size:     m.Size,
	}, true
}

// medium returns the medium of b, an image or a file at path, and the URL of its published file; ok is false if it does
// not exist, which it warns about.
func (w *walker) medium(b *content.Block, path string) (m *content.Media, url template.URL, ok bool) {
	m, p, ok := w.r.res.Media(b.Attrs.Media)
	if !ok || m == nil {
		w.warnf(path+"/attrs/media", "media %q does not exist; not rendered", b.Attrs.Media)
		return nil, "", false
	}
	return m, template.URL(w.r.sitePath(p)), true
}
