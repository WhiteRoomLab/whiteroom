package render

import (
	"html/template"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// Link is a link that a template writes as <a href="{{.Href}}" ...>.
type Link struct {
	Href   template.URL // encoded; trusted, so that html/template keeps tel: URLs
	Target string       // "_blank" to open in a new tab, or ""
	Rel    string       // "noopener" for a new tab, then the rel of the link, separated by spaces; "" if none
}

// Link returns the link of l outside blocks, such as a link field or an item of a menu, or nil if l is nil or its
// target cannot be linked, which the warnings tell, at the path "".
func (r *Renderer) Link(l *content.Link) (*Link, []Warning) {
	w := &walker{r: r}
	return w.link(l, ""), w.warnings
}

// link returns the link of l, at path, or nil if l is nil or its target cannot be linked, which it warns about. Of
// several targets, which only invalid content has, href comes first, then entry, media and anchor.
func (w *walker) link(l *content.Link, path string) *Link {
	if l == nil {
		return nil
	}
	var href string
	switch {
	case l.Href != "":
		if err := content.CheckHref(l.Href); err != nil {
			w.warnf(path, "invalid href %q: %v; rendered without the link", l.Href, err)
			return nil
		}
		href = w.r.hrefURL(l.Href)
	case l.Entry != "":
		_, p, ok := w.r.res.Entry(l.Entry)
		switch {
		case !ok:
			w.warnf(path, "entry %q does not exist; rendered without the link", l.Entry)
			return nil
		case p == "":
			w.warnf(path, "entry %q is not published; rendered without the link", l.Entry)
			return nil
		}
		if l.Anchor != "" {
			p += "#" + l.Anchor
		}
		href = w.r.sitePath(p)
	case l.Media != "":
		_, p, ok := w.r.res.Media(l.Media)
		if !ok {
			w.warnf(path, "media %q does not exist; rendered without the link", l.Media)
			return nil
		}
		href = w.r.sitePath(p)
	case l.Anchor != "":
		href = encodeURL("#" + l.Anchor)
	default:
		w.warnf(path, "link without a target; rendered without the link")
		return nil
	}
	x := &Link{Href: template.URL(href)}
	var rel []string
	if l.NewTab {
		x.Target = "_blank"
		rel = append(rel, "noopener")
	}
	for _, v := range l.Rel {
		rel = append(rel, string(v))
	}
	x.Rel = strings.Join(rel, " ")
	return x
}
