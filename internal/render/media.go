package render

import (
	"cmp"
	"html/template"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// image returns the data of b, an image at path; ok is false if its medium does not exist or is not an image, which it
// warns about before anything else.
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
	if d.Alt, d.HasAlt = b.ImageAlt(m, w.r.language); !d.HasAlt {
		w.warnf(path+"/attrs/alt",
			"no alt in %s: neither the block nor its medium has one; rendered without the alt attribute", w.r.language)
	}
	d.Caption = template.HTML(w.richText(a.Caption, path+"/attrs/caption"))
	d.Link = w.link(a.Link, path+"/attrs/link")
	d.Common = w.finish(s)
	return d, true
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
